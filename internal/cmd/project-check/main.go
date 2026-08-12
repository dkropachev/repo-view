package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/yapless/scopesifter/internal/projectcheck"
)

func main() {
	mode := flag.String("mode", "", "validation mode: json or no-bash")
	root := flag.String("root", ".", "repository root")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "project check: positional arguments are not accepted")
		os.Exit(2)
	}
	absRoot, err := filepath.Abs(*root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "project check: resolve root: %v\n", err)
		os.Exit(1)
	}
	switch *mode {
	case "json":
		err = projectcheck.ValidateJSON(absRoot)
	case "no-bash":
		err = projectcheck.ValidateNoBash(absRoot)
	default:
		err = fmt.Errorf("unsupported mode %q", *mode)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "project check %s: %v\n", strings.TrimSpace(*mode), err)
		os.Exit(1)
	}
}
