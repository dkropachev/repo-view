package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/dkropachev/repo-view/benchmarks/tokenbench"
	"github.com/dkropachev/repo-view/benchmarks/tokenbench/harness"
	"github.com/dkropachev/repo-view/benchmarks/tokenbench/harness/fake"
	processadapter "github.com/dkropachev/repo-view/benchmarks/tokenbench/harness/process"
)

func main() {
	os.Exit(run(context.Background(), os.Args[1:], os.Stdout, os.Stderr))
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}
	var err error
	switch args[0] {
	case "validate":
		err = validateCommand(args[1:], stdout, stderr)
	case "plan":
		err = planCommand(ctx, args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		printUsage(stderr)
		return 2
	}
	if err != nil {
		fmt.Fprintf(stderr, "tokenbench: %v\n", err)
		return 1
	}
	return 0
}

func validateCommand(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("validate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	suitePath := flags.String("suite", "", "path to a suite JSON document")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *suitePath == "" || flags.NArg() != 0 {
		return errors.New("validate requires exactly --suite PATH")
	}
	loaded, err := tokenbench.LoadSuite(*suitePath)
	if err != nil {
		return err
	}
	suite := loaded.Suite()
	return writeJSON(stdout, struct {
		SchemaVersion string `json:"schema_version"`
		SuiteID       string `json:"suite_id"`
		SuiteSHA256   string `json:"suite_sha256"`
		PromptSHA256  string `json:"prompt_sha256"`
	}{
		SchemaVersion: tokenbench.SuiteSchemaVersion,
		SuiteID:       suite.ID,
		SuiteSHA256:   loaded.Digest(),
		PromptSHA256:  loaded.PromptDigest(),
	})
}

func planCommand(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
) error {
	flags := flag.NewFlagSet("plan", flag.ContinueOnError)
	flags.SetOutput(stderr)
	suitePath := flags.String("suite", "", "path to a suite JSON document")
	repoViewPath := flags.String(
		"repo-view-mcp",
		"",
		"absolute path to the repo-view MCP executable",
	)
	outputPath := flags.String("out", "-", "output plan path, or - for stdout")
	adapterCommand := flags.String(
		"adapter-command",
		"",
		"absolute external adapter command; omit only for harness_kind=fake",
	)
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *suitePath == "" || *repoViewPath == "" || flags.NArg() != 0 {
		return errors.New("plan requires --suite PATH and --repo-view-mcp PATH")
	}
	loaded, err := tokenbench.LoadSuite(*suitePath)
	if err != nil {
		return err
	}
	suite := loaded.Suite()
	adapter, err := resolveAdapter(suite, *adapterCommand)
	if err != nil {
		return err
	}
	prepared, err := tokenbench.PrepareSuite(ctx, loaded, adapter)
	if err != nil {
		return err
	}
	repoViewAbs, err := filepath.Abs(*repoViewPath)
	if err != nil {
		return fmt.Errorf("resolve repo-view MCP executable: %w", err)
	}
	tool, err := tokenbench.NewRepoViewTool(repoViewAbs)
	if err != nil {
		return err
	}
	pair, err := tokenbench.ResolvePair(prepared, tool)
	if err != nil {
		return err
	}
	plan, err := pair.Plan(ctx)
	if err != nil {
		return fmt.Errorf("build verified process plan: %w", err)
	}
	if err := plan.Validate(); err != nil {
		return err
	}
	if *outputPath == "-" {
		return writeJSON(stdout, plan)
	}
	return writeJSONFile(*outputPath, plan)
}

func resolveAdapter(suite tokenbench.Suite, command string) (harness.Adapter, error) {
	if command == "" {
		if suite.HarnessKind != "fake" {
			return nil, fmt.Errorf(
				"harness_kind %q requires --adapter-command",
				suite.HarnessKind,
			)
		}
		return fake.Adapter{}, nil
	}
	absolute, err := filepath.Abs(command)
	if err != nil {
		return nil, fmt.Errorf("resolve adapter command: %w", err)
	}
	return processadapter.New(processadapter.Config{
		Environment: make(map[string]string),
		Command:     absolute,
		Kind:        suite.HarnessKind,
		Timeout:     30 * time.Second,
	})
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func writeJSONFile(path string, value any) error {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("create plan: %w", err)
	}
	complete := false
	defer func() {
		if !complete {
			_ = file.Close()
			_ = os.Remove(path)
		}
	}()
	writeErr := writeJSON(file, value)
	if writeErr != nil {
		return fmt.Errorf("write plan: %w", writeErr)
	}
	if err := file.Sync(); err != nil {
		return fmt.Errorf("sync plan: %w", err)
	}
	closeErr := file.Close()
	if closeErr != nil {
		return fmt.Errorf("close plan: %w", closeErr)
	}
	complete = true
	return nil
}

func printUsage(writer io.Writer) {
	fmt.Fprintln(writer, "usage: tokenbench <validate|plan> [options]")
}
