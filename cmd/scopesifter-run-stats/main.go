package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/yapless/scopesifter/internal/runstats"
)

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	flags := flag.NewFlagSet("scopesifter-run-stats", flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	inputPath := flags.String("input", "", "Codex JSONL transcript")
	outputPath := flags.String("output", "-", "JSON stats output path, or - for stdout")
	dotPath := flags.String("dot-output", "", "optional Graphviz DOT output path")
	markdownPath := flags.String("markdown-output", "", "optional Markdown call-graph output path")
	pretty := flags.Bool("pretty", true, "indent JSON output")
	flags.Usage = func() {
		fmt.Fprintln(flags.Output(), "Usage: scopesifter-run-stats --input RUN.jsonl [options]")
		flags.PrintDefaults()
	}
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if *inputPath == "" || flags.NArg() != 0 {
		flags.Usage()
		return 2
	}

	input, err := os.Open(*inputPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	stats, err := runstats.Analyze(input)
	closeErr := input.Close()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if closeErr != nil {
		fmt.Fprintln(os.Stderr, closeErr)
		return 1
	}

	if err := writeJSON(*outputPath, stats, *pretty); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if *dotPath != "" {
		if err := writeFile(*dotPath, func(output io.Writer) error {
			return runstats.WriteDOT(output, stats)
		}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	if *markdownPath != "" {
		if err := writeFile(*markdownPath, func(output io.Writer) error {
			return runstats.WriteMarkdown(output, stats)
		}); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	return 0
}

func writeFile(path string, write func(io.Writer) error) error {
	output, err := os.Create(path)
	if err != nil {
		return err
	}
	writeErr := write(output)
	closeErr := output.Close()
	if writeErr != nil {
		return writeErr
	}
	return closeErr
}

func writeJSON(path string, value any, pretty bool) error {
	var output io.Writer = os.Stdout
	var file *os.File
	if path != "-" {
		var err error
		file, err = os.Create(path)
		if err != nil {
			return err
		}
		output = file
	}

	encoder := json.NewEncoder(output)
	if pretty {
		encoder.SetIndent("", "  ")
	}
	err := encoder.Encode(value)
	if file != nil {
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
	}
	return err
}
