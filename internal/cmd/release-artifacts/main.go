package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/yapless/scopesifter/internal/releaseartifacts"
)

func main() {
	mode := flag.String("mode", "build", "operation: build or publish")
	root := flag.String("root", ".", "repository root")
	ref := flag.String("ref", os.Getenv("GITHUB_REF_NAME"), "release tag name")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "release artifacts: positional arguments are not accepted")
		os.Exit(2)
	}
	var err error
	switch *mode {
	case "build":
		err = releaseartifacts.Build(*root, *ref)
	case "publish":
		err = releaseartifacts.Publish(*root, *ref)
	default:
		err = fmt.Errorf("unknown mode %q", *mode)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "release artifacts: %v\n", err)
		os.Exit(1)
	}
}
