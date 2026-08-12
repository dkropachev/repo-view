package grammargen

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

type commandCall struct {
	directory string
	name      string
	arguments []string
}

type fixtureRunner struct {
	commit           string
	rawGenerated     []byte
	generatedParser  []byte
	tableMagic       string
	tableReplacement string
	diffError        error
	calls            []commandCall
	sawPublicRewrite bool
}

func (runner *fixtureRunner) run(
	_ context.Context,
	directory string,
	name string,
	arguments ...string,
) ([]byte, error) {
	runner.calls = append(runner.calls, commandCall{
		directory: directory,
		name:      name,
		arguments: slices.Clone(arguments),
	})
	switch name {
	case "git":
		if slices.Equal(arguments, []string{"rev-parse", "HEAD"}) {
			return []byte(runner.commit + "\n"), nil
		}
		if len(arguments) >= 3 && slices.Equal(arguments[:3], []string{"diff", "--quiet", "--"}) {
			return nil, runner.diffError
		}
		return nil, errors.New("unexpected git invocation")
	case "npm":
		parserPath := filepath.Join(directory, "src", "parser.c")
		if err := os.WriteFile(parserPath, runner.generatedParser, 0o644); err != nil {
			return nil, err
		}
		return nil, nil
	case "go":
		if len(arguments) < 2 || arguments[0] != "run" {
			return nil, errors.New("unexpected go invocation")
		}
		switch {
		case strings.HasPrefix(
			arguments[1],
			"github.com/dcosson/treesitter-go/cmd/tsgo-generate@",
		):
			output, ok := flagValue(arguments, "-output")
			if !ok {
				return nil, errors.New("generator output flag missing")
			}
			return nil, os.WriteFile(output, runner.rawGenerated, 0o644)
		case filepath.Base(arguments[1]) == "compact.go":
			if len(arguments) != 4 {
				return nil, errors.New("unexpected compact invocation")
			}
			rewritten, err := os.ReadFile(arguments[2])
			if err != nil {
				return nil, err
			}
			runner.sawPublicRewrite = !strings.Contains(
				string(rewritten),
				"treesitter-go/internal",
			) && strings.Contains(string(rewritten), `core "github.com/dcosson/treesitter-go"`)
			rewritten = append(
				rewritten,
				[]byte("\nfunc compactedFixture() {\n"+runner.tableReplacement+
					"\t_, _ = parseTable, smallParseTable\n}\n")...,
			)
			if err := os.WriteFile(arguments[2], rewritten, 0o644); err != nil {
				return nil, err
			}
			tables := append([]byte(runner.tableMagic), make([]byte, 8)...)
			return nil, os.WriteFile(arguments[3], tables, 0o644)
		case filepath.Base(arguments[1]) == "split.go":
			if len(arguments) != 4 {
				return nil, errors.New("unexpected split invocation")
			}
			input, err := os.ReadFile(arguments[2])
			if err != nil {
				return nil, err
			}
			return nil, os.WriteFile(arguments[3], input, 0o644)
		default:
			return nil, errors.New("unexpected go run target")
		}
	default:
		return nil, errors.New("unexpected executable")
	}
}

func flagValue(arguments []string, name string) (string, bool) {
	for index := 0; index+1 < len(arguments); index++ {
		if arguments[index] == name {
			return arguments[index+1], true
		}
	}
	return "", false
}

func TestGeneratePinnedGrammarWithFakeExecutables(t *testing.T) {
	t.Parallel()

	sourceRoot := t.TempDir()
	repositoryRoot := t.TempDir()
	outputDirectory := "internal/fixturegrammar"
	if err := os.MkdirAll(filepath.Join(sourceRoot, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	parser := []byte("parser input")
	scanner := []byte("scanner input")
	writeFixture(t, filepath.Join(sourceRoot, "src", "parser.c"), parser)
	writeFixture(t, filepath.Join(sourceRoot, "src", "scanner.c"), scanner)
	if err := os.MkdirAll(filepath.Join(repositoryRoot, outputDirectory), 0o755); err != nil {
		t.Fatal(err)
	}
	oldOutput := filepath.Join(repositoryRoot, outputDirectory, "language_generated.go")
	writeFixture(t, oldOutput, []byte("old generated output"))

	const replacement = "\tparseTable, smallParseTable := fixtureGeneratedParseTables()\n\n"
	spec := grammarSpec{
		name:             "fixture",
		upstreamName:     "tree-sitter-fixture",
		packageName:      "fixturegrammar",
		outputDirectory:  outputDirectory,
		upstreamCommit:   "pinned-commit",
		parserPath:       "src/parser.c",
		dirtyMessage:     "fixture inputs have local changes",
		tableMagic:       "FIXTURE!",
		tableReplacement: replacement,
		dirtyPaths:       []string{"src/parser.c", "src/scanner.c"},
		pins: []filePin{
			{path: "src/parser.c", digest: digestBytes(parser), label: "parser.c"},
			{path: "src/scanner.c", digest: digestBytes(scanner), label: "scanner.c"},
		},
		correctABIVersion: true,
	}
	runner := &fixtureRunner{
		commit:           spec.upstreamCommit,
		rawGenerated:     []byte(generatedFixture),
		tableMagic:       spec.tableMagic,
		tableReplacement: spec.tableReplacement,
	}
	if err := generate(
		context.Background(),
		spec,
		sourceRoot,
		repositoryRoot,
		runner,
	); err != nil {
		t.Fatalf("generate() error = %v", err)
	}
	if !runner.sawPublicRewrite {
		t.Fatal("compact helper did not receive the public-facade rewrite")
	}
	generated, err := os.ReadFile(oldOutput)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		`core "github.com/dcosson/treesitter-go"`,
		"Version: 15,",
		replacement,
	} {
		if !strings.Contains(string(generated), required) {
			t.Errorf("generated output omitted %q", required)
		}
	}
	tables, err := os.ReadFile(filepath.Join(repositoryRoot, outputDirectory, "language_tables.bin"))
	if err != nil {
		t.Fatal(err)
	}
	if string(tables[:8]) != spec.tableMagic {
		t.Fatalf("table magic = %q, want %q", tables[:8], spec.tableMagic)
	}
	assertPinnedCommands(t, runner.calls, spec)
	workspaces, err := filepath.Glob(
		filepath.Join(repositoryRoot, outputDirectory, ".grammar-generate-*"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(workspaces) != 0 {
		t.Errorf("temporary workspaces remain: %v", workspaces)
	}
}

func TestGenerateSwiftStagesParserAndCorpusWithFakeExecutables(t *testing.T) {
	t.Parallel()

	sourceRoot := t.TempDir()
	repositoryRoot := t.TempDir()
	outputDirectory := "internal/swiftfixture"
	grammar := []byte("grammar input")
	scanner := []byte("scanner input")
	corpusA := []byte("corpus a")
	corpusB := []byte("corpus b")
	for path, data := range map[string][]byte{
		"src/grammar.json":       grammar,
		"src/scanner.c":          scanner,
		"test/corpus/first.txt":  corpusA,
		"test/corpus/second.txt": corpusB,
	} {
		writeFixture(t, filepath.Join(sourceRoot, filepath.FromSlash(path)), data)
	}
	if err := os.MkdirAll(filepath.Join(repositoryRoot, outputDirectory), 0o755); err != nil {
		t.Fatal(err)
	}
	generatedParser := []byte("generated parser")
	corpusPins := []filePin{
		{path: "test/corpus/first.txt", digest: digestBytes(corpusA), label: "corpus/first.txt"},
		{path: "test/corpus/second.txt", digest: digestBytes(corpusB), label: "corpus/second.txt"},
	}
	pins := []filePin{
		{path: "src/grammar.json", digest: digestBytes(grammar), label: "grammar.json"},
		{path: "src/scanner.c", digest: digestBytes(scanner), label: "scanner.c"},
	}
	pins = append(pins, corpusPins...)
	const replacement = "\tparseTable, smallParseTable := swiftFixtureTables()\n\n"
	spec := grammarSpec{
		name:             "swift",
		upstreamName:     "tree-sitter-swift",
		packageName:      "swiftgrammar",
		outputDirectory:  outputDirectory,
		upstreamCommit:   "swift-pinned-commit",
		dirtyMessage:     "Swift inputs have local changes",
		tableMagic:       "SWIFTFIX",
		tableReplacement: replacement,
		dirtyPaths:       []string{"src/grammar.json", "src/scanner.c", "test/corpus"},
		pins:             pins,
		corpusPins:       corpusPins,
		generatedParser: &filePin{
			path:   "src/parser.c",
			digest: digestBytes(generatedParser),
			label:  "generated parser.c",
		},
		rawGoDigest: digestBytes([]byte(generatedFixture)),
		splitLexer:  true,
	}
	runner := &fixtureRunner{
		commit:           spec.upstreamCommit,
		rawGenerated:     []byte(generatedFixture),
		generatedParser:  generatedParser,
		tableMagic:       spec.tableMagic,
		tableReplacement: spec.tableReplacement,
	}
	if err := generate(
		context.Background(),
		spec,
		sourceRoot,
		repositoryRoot,
		runner,
	); err != nil {
		t.Fatalf("generate() error = %v", err)
	}
	for name, want := range map[string][]byte{"first.txt": corpusA, "second.txt": corpusB} {
		path := filepath.Join(
			repositoryRoot,
			outputDirectory,
			"testdata",
			"tree-sitter-swift-corpus",
			name,
		)
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(got, want) {
			t.Errorf("copied %s = %q, want %q", name, got, want)
		}
	}
	if !hasCommand(runner.calls, "npm", "--package=tree-sitter-cli@0.23.0", "--abi", "14", "--no-bindings") {
		t.Error("pinned tree-sitter-cli ABI 14 invocation not observed")
	}
	if !hasGoTool(runner.calls, "split.go") {
		t.Error("Swift lexer split invocation not observed")
	}
}

func TestVerifyCheckoutRejectsDirtyPinnedInputs(t *testing.T) {
	t.Parallel()

	runner := &fixtureRunner{commit: "commit", diffError: errors.New("dirty")}
	spec := grammarSpec{
		upstreamName:   "tree-sitter-fixture",
		upstreamCommit: "commit",
		dirtyMessage:   "pinned inputs have local changes",
		dirtyPaths:     []string{"src/parser.c"},
	}
	err := verifyCheckout(context.Background(), runner, spec, t.TempDir())
	if err == nil || err.Error() != spec.dirtyMessage {
		t.Fatalf("verifyCheckout() error = %v, want %q", err, spec.dirtyMessage)
	}
}

func writeFixture(t *testing.T, path string, data []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertPinnedCommands(t *testing.T, calls []commandCall, spec grammarSpec) {
	t.Helper()
	wantDiff := append([]string{"diff", "--quiet", "--"}, spec.dirtyPaths...)
	if !hasExactCommand(calls, "git", wantDiff...) {
		t.Errorf("git dirty-input invocation not observed: %v", wantDiff)
	}
	if !hasCommand(
		calls,
		"go",
		"github.com/dcosson/treesitter-go/cmd/tsgo-generate@v0.1.0",
		"-package",
		spec.packageName,
	) {
		t.Error("pinned tsgo-generate invocation not observed")
	}
	if !hasGoTool(calls, "compact.go") {
		t.Error("grammar table compaction invocation not observed")
	}
}

func hasExactCommand(calls []commandCall, name string, arguments ...string) bool {
	for _, call := range calls {
		if call.name == name && slices.Equal(call.arguments, arguments) {
			return true
		}
	}
	return false
}

func hasCommand(calls []commandCall, name string, arguments ...string) bool {
	for _, call := range calls {
		if call.name != name {
			continue
		}
		matched := true
		for _, argument := range arguments {
			if !slices.Contains(call.arguments, argument) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

func hasGoTool(calls []commandCall, tool string) bool {
	for _, call := range calls {
		if call.name == "go" && len(call.arguments) >= 2 && filepath.Base(call.arguments[1]) == tool {
			return true
		}
	}
	return false
}
