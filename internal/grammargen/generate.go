package grammargen

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"go/format"
	"os"
	"path/filepath"
	"strings"
)

// Generate regenerates a pinned grammar and installs all validated outputs.
func Generate(ctx context.Context, language, sourceRoot, repositoryRoot string) error {
	if language == "" {
		return errors.New("grammar language is required")
	}
	if sourceRoot == "" {
		return errors.New("grammar source directory is required")
	}
	if repositoryRoot == "" {
		return errors.New("repository root is required")
	}
	spec, err := specFor(language)
	if err != nil {
		return err
	}
	return generate(ctx, spec, sourceRoot, repositoryRoot, executableRunner{})
}

func generate(
	ctx context.Context,
	spec grammarSpec,
	sourceRoot string,
	repositoryRoot string,
	runner commandRunner,
) error {
	canonicalSource, err := canonicalDirectory(sourceRoot)
	if err != nil {
		return fmt.Errorf("resolve grammar source: %w", err)
	}
	canonicalRepository, err := canonicalDirectory(repositoryRoot)
	if err != nil {
		return fmt.Errorf("resolve repository root: %w", err)
	}
	outputRoot, err := canonicalDirectory(
		filepath.Join(canonicalRepository, filepath.FromSlash(spec.outputDirectory)),
	)
	if err != nil {
		return fmt.Errorf("resolve grammar output directory: %w", err)
	}

	if err := verifyCheckout(ctx, runner, spec, canonicalSource); err != nil {
		return err
	}

	workRoot, err := os.MkdirTemp(outputRoot, ".grammar-generate-")
	if err != nil {
		return fmt.Errorf("create grammar workspace: %w", err)
	}
	defer os.RemoveAll(workRoot)

	parserSource, err := prepareParser(ctx, runner, spec, canonicalSource, workRoot)
	if err != nil {
		return err
	}
	rawGenerated := filepath.Join(workRoot, "language.raw.go")
	if _, err := runner.run(
		ctx,
		canonicalRepository,
		"go",
		"run",
		"github.com/dcosson/treesitter-go/cmd/tsgo-generate@"+treeSitterGeneratorVersion,
		"-parser",
		parserSource,
		"-package",
		spec.packageName,
		"-output",
		rawGenerated,
	); err != nil {
		return fmt.Errorf("generate %s Go grammar: %w", spec.name, err)
	}
	if spec.rawGoDigest != "" {
		if err := verifyFile(rawGenerated, spec.rawGoDigest, "raw generated Go"); err != nil {
			return err
		}
	}

	rawSource, err := os.ReadFile(rawGenerated)
	if err != nil {
		return fmt.Errorf("read raw generated Go: %w", err)
	}
	rewrittenSource, err := rewriteGenerated(rawSource, spec.correctABIVersion)
	if err != nil {
		return fmt.Errorf("rewrite generated Go: %w", err)
	}
	// Keep the intermediate extensionless so go run passes it to the compact
	// helper as data rather than interpreting it as another source file.
	rewrittenPath := filepath.Join(workRoot, "language.rewritten")
	if err := os.WriteFile(rewrittenPath, rewrittenSource, 0o644); err != nil {
		return fmt.Errorf("write rewritten generated Go: %w", err)
	}

	tablePath := filepath.Join(workRoot, "language_tables.bin")
	compactTool := filepath.Join(outputRoot, "compact.go")
	if _, err := runner.run(
		ctx,
		canonicalRepository,
		"go",
		"run",
		compactTool,
		rewrittenPath,
		tablePath,
	); err != nil {
		return fmt.Errorf("compact %s grammar tables: %w", spec.name, err)
	}

	finalPath := rewrittenPath
	if spec.splitLexer {
		finalPath = filepath.Join(workRoot, "language.final.go")
		splitTool := filepath.Join(outputRoot, "split.go")
		if _, err := runner.run(
			ctx,
			canonicalRepository,
			"go",
			"run",
			splitTool,
			rewrittenPath,
			finalPath,
		); err != nil {
			return fmt.Errorf("split %s generated lexer: %w", spec.name, err)
		}
	}

	finalSource, err := os.ReadFile(finalPath)
	if err != nil {
		return fmt.Errorf("read final generated Go: %w", err)
	}
	if !spec.splitLexer {
		finalSource, err = format.Source(finalSource)
		if err != nil {
			return fmt.Errorf("format final generated Go: %w", err)
		}
	}
	tableData, err := os.ReadFile(tablePath)
	if err != nil {
		return fmt.Errorf("read generated table asset: %w", err)
	}
	if err := verifyCompactedOutputs(spec, finalSource, tableData); err != nil {
		return err
	}
	if spec.finalGoDigest != "" && digestBytes(finalSource) != spec.finalGoDigest {
		return fmt.Errorf(
			"unexpected final generated Go checksum: got %s, want %s",
			digestBytes(finalSource),
			spec.finalGoDigest,
		)
	}
	if spec.tableDigest != "" && digestBytes(tableData) != spec.tableDigest {
		return fmt.Errorf(
			"unexpected generated table asset checksum: got %s, want %s",
			digestBytes(tableData),
			spec.tableDigest,
		)
	}

	artifacts := []artifact{
		{path: filepath.Join(outputRoot, "language_generated.go"), data: finalSource},
		{path: filepath.Join(outputRoot, "language_tables.bin"), data: tableData},
	}
	for _, pin := range spec.corpusPins {
		data, err := os.ReadFile(filepath.Join(canonicalSource, filepath.FromSlash(pin.path)))
		if err != nil {
			return fmt.Errorf("read %s: %w", pin.label, err)
		}
		artifacts = append(artifacts, artifact{
			path: filepath.Join(
				outputRoot,
				"testdata",
				"tree-sitter-swift-corpus",
				filepath.Base(pin.path),
			),
			data: data,
		})
	}
	if err := installArtifacts(artifacts); err != nil {
		return fmt.Errorf("install generated grammar artifacts: %w", err)
	}
	return nil
}

func canonicalDirectory(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	canonical, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", err
	}
	info, err := os.Stat(canonical)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory", canonical)
	}
	return canonical, nil
}

func verifyCheckout(
	ctx context.Context,
	runner commandRunner,
	spec grammarSpec,
	sourceRoot string,
) error {
	output, err := runner.run(ctx, sourceRoot, "git", "rev-parse", "HEAD")
	if err != nil {
		return fmt.Errorf("read %s revision: %w", spec.upstreamName, err)
	}
	if strings.TrimSpace(string(output)) != spec.upstreamCommit {
		return fmt.Errorf(
			"%s must be checked out at %s",
			spec.upstreamName,
			spec.upstreamCommit,
		)
	}
	diffArguments := append([]string{"diff", "--quiet", "--"}, spec.dirtyPaths...)
	if _, err := runner.run(ctx, sourceRoot, "git", diffArguments...); err != nil {
		return errors.New(spec.dirtyMessage)
	}
	for _, pin := range spec.pins {
		if err := verifyFile(
			filepath.Join(sourceRoot, filepath.FromSlash(pin.path)),
			pin.digest,
			pin.label,
		); err != nil {
			return err
		}
	}
	return nil
}

func prepareParser(
	ctx context.Context,
	runner commandRunner,
	spec grammarSpec,
	sourceRoot string,
	workRoot string,
) (string, error) {
	if spec.generatedParser == nil {
		return filepath.Join(sourceRoot, filepath.FromSlash(spec.parserPath)), nil
	}
	grammarRoot := filepath.Join(workRoot, "grammar")
	grammarSource := filepath.Join(grammarRoot, "src", "grammar.json")
	if err := os.MkdirAll(filepath.Dir(grammarSource), 0o755); err != nil {
		return "", fmt.Errorf("create generated-parser workspace: %w", err)
	}
	input, err := os.ReadFile(filepath.Join(sourceRoot, "src", "grammar.json"))
	if err != nil {
		return "", fmt.Errorf("read grammar.json: %w", err)
	}
	if err := os.WriteFile(grammarSource, input, 0o644); err != nil {
		return "", fmt.Errorf("stage grammar.json: %w", err)
	}
	if _, err := runner.run(
		ctx,
		grammarRoot,
		"npm",
		"exec",
		"--yes",
		"--package=tree-sitter-cli@"+treeSitterCLIVersion,
		"--",
		"tree-sitter",
		"generate",
		"--abi",
		"14",
		"--no-bindings",
		"src/grammar.json",
	); err != nil {
		return "", fmt.Errorf("generate Swift parser.c: %w", err)
	}
	parserSource := filepath.Join(grammarRoot, filepath.FromSlash(spec.generatedParser.path))
	if err := verifyFile(
		parserSource,
		spec.generatedParser.digest,
		spec.generatedParser.label,
	); err != nil {
		return "", err
	}
	return parserSource, nil
}

func verifyCompactedOutputs(spec grammarSpec, source, tables []byte) error {
	if !bytes.Contains(source, []byte(strings.TrimSpace(spec.tableReplacement))) {
		return fmt.Errorf("compacted %s grammar does not load its generated tables", spec.name)
	}
	if len(tables) < len(spec.tableMagic) || string(tables[:len(spec.tableMagic)]) != spec.tableMagic {
		return fmt.Errorf("compacted %s grammar has an invalid table header", spec.name)
	}
	return nil
}
