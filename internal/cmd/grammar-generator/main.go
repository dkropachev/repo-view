// Command grammar-generator regenerates checksum-pinned language grammars.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/yapless/scopesifter/internal/grammargen"
)

func main() {
	if err := run(context.Background(), os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(ctx context.Context, arguments []string, stderr io.Writer) error {
	flags := flag.NewFlagSet("grammar-generator", flag.ContinueOnError)
	flags.SetOutput(stderr)
	language := flags.String(
		"language",
		os.Getenv("GRAMMAR_LANGUAGE"),
		"grammar language: csharp, kotlin, or swift",
	)
	source := flags.String(
		"source",
		os.Getenv("GRAMMAR_SOURCE"),
		"path to the pinned upstream grammar checkout",
	)
	repository := flags.String("repo", ".", "path to the ScopeSifter repository root")
	flags.Usage = func() {
		fmt.Fprintln(
			stderr,
			"usage: grammar-generator -language {csharp|kotlin|swift} -source DIRECTORY [-repo DIRECTORY]",
		)
		flags.PrintDefaults()
	}
	if err := flags.Parse(arguments); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	if flags.NArg() != 0 {
		flags.Usage()
		return fmt.Errorf("unexpected positional arguments: %v", flags.Args())
	}
	return grammargen.Generate(ctx, *language, *source, *repository)
}
