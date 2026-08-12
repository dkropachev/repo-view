package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/yapless/scopesifter/internal/codexlauncher"
)

const moduleDeclaration = "module github.com/yapless/scopesifter"

func main() {
	root, err := findScopesifterRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	os.Exit(codexlauncher.Run(
		root,
		os.Args[1:],
		os.Environ(),
		codexlauncher.Streams{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr},
	))
}

func findScopesifterRoot() (string, error) {
	candidates := make([]string, 0, 2)
	if executable, err := os.Executable(); err == nil {
		if resolved, resolveErr := filepath.EvalSymlinks(executable); resolveErr == nil {
			executable = resolved
		}
		candidates = append(candidates, filepath.Dir(executable))
	}
	if workingDirectory, err := os.Getwd(); err == nil {
		candidates = append(candidates, workingDirectory)
	}
	seen := make(map[string]struct{})
	for _, candidate := range candidates {
		for {
			candidate, _ = filepath.Abs(candidate)
			if _, duplicate := seen[candidate]; !duplicate {
				seen[candidate] = struct{}{}
				manifest, err := os.ReadFile(filepath.Join(candidate, "go.mod"))
				if err == nil && hasModuleDeclaration(string(manifest)) {
					return candidate, nil
				}
			}
			parent := filepath.Dir(candidate)
			if parent == candidate {
				break
			}
			candidate = parent
		}
	}
	return "", fmt.Errorf(
		"cannot locate the ScopeSifter source root; build this command from the repository",
	)
}

func hasModuleDeclaration(manifest string) bool {
	for _, line := range strings.Split(manifest, "\n") {
		if strings.TrimSpace(line) == moduleDeclaration {
			return true
		}
	}
	return false
}
