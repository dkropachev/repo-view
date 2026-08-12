// Package workflowrunner executes the narrow set of Make targets reviewed for
// GitHub Actions without evaluating workflow run text as a shell program.
package workflowrunner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/yapless/scopesifter/internal/processpolicy"
	"github.com/yapless/scopesifter/internal/projectcheck"
)

const maximumRunFileSize = 128

// Run reads the Actions run file as data, validates its single target, and
// invokes the repository-root Makefile through a native Make executable.
func Run(
	ctx context.Context,
	root string,
	runFile string,
	stdout io.Writer,
	stderr io.Writer,
) error {
	target, err := readTarget(runFile)
	if err != nil {
		return err
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve repository root: %w", err)
	}
	if err := projectcheck.ValidateNoScripts(absRoot); err != nil {
		return fmt.Errorf("validate no-script project policy: %w", err)
	}
	if err := projectcheck.ValidateTrackedMakeTarget(absRoot, target); err != nil {
		return fmt.Errorf("validate tracked Make graph: %w", err)
	}
	arguments := makeArguments(target)
	if err := processpolicy.Validate("make", arguments...); err != nil {
		return fmt.Errorf("reject Make invocation: %w", err)
	}
	command, makeFile, err := processpolicy.NativeCommandContext(ctx, "make", arguments...)
	if err != nil {
		return fmt.Errorf("pin native Make executable: %w", err)
	}
	command.Dir = absRoot
	command.Env = sanitizedEnvironment(os.Environ())
	command.Stdin = nil
	command.Stdout = stdout
	command.Stderr = stderr
	runErr := command.Run()
	closeErr := makeFile.Close()
	if runErr != nil {
		return fmt.Errorf("run Make target %s: %w", target, runErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close native Make image: %w", closeErr)
	}
	return nil
}

func makeArguments(target string) []string {
	return []string{
		"--no-builtin-rules",
		"--no-builtin-variables",
		"-f",
		"Makefile",
		"--",
		target,
	}
}

func readTarget(path string) (string, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("inspect workflow run file: %w", err)
	}
	if !before.Mode().IsRegular() {
		return "", errors.New("workflow run file is not a regular file")
	}
	if before.Size() > maximumRunFileSize {
		return "", errors.New("workflow run file is too large")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open workflow run file: %w", err)
	}
	defer file.Close()
	after, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("inspect opened workflow run file: %w", err)
	}
	if !os.SameFile(before, after) || !after.Mode().IsRegular() {
		return "", errors.New("workflow run file changed while it was opened")
	}
	content, err := io.ReadAll(io.LimitReader(file, maximumRunFileSize+1))
	if err != nil {
		return "", fmt.Errorf("read workflow run file: %w", err)
	}
	if len(content) > maximumRunFileSize {
		return "", errors.New("workflow run file is too large")
	}
	targetBytes := bytes.TrimSuffix(content, []byte{'\n'})
	if bytes.HasSuffix(targetBytes, []byte{'\r'}) {
		targetBytes = bytes.TrimSuffix(targetBytes, []byte{'\r'})
	}
	target := string(targetBytes)
	if err := projectcheck.ValidateWorkflowTarget(target); err != nil {
		return "", err
	}
	return target, nil
}

func sanitizedEnvironment(environment []string) []string {
	replacements := map[string]string{
		"CGO_ENABLED":  "0",
		"GO111MODULE":  "on",
		"GOAUTH":       "off",
		"GOCACHEPROG":  "",
		"GOENV":        "off",
		"GOFLAGS":      "-mod=readonly -trimpath -buildvcs=false",
		"GONOPROXY":    "none",
		"GONOSUMDB":    "",
		"GOPRIVATE":    "",
		"GOPROXY":      "https://proxy.golang.org",
		"GOSUMDB":      "sum.golang.org",
		"GOTOOLCHAIN":  "local",
		"GOVCS":        "*:off",
		"GOWORK":       "off",
		"GNUMAKEFLAGS": "",
		"MAKEFILES":    "",
		"MAKEFLAGS":    "",
		"MFLAGS":       "",
	}
	result := make([]string, 0, len(environment)+len(replacements))
	for _, entry := range environment {
		name, _, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		if workflowEnvironmentVariableControlled(name) {
			continue
		}
		result = append(result, entry)
	}
	keys := make([]string, 0, len(replacements))
	for name := range replacements {
		keys = append(keys, name)
	}
	sort.Strings(keys)
	for _, name := range keys {
		result = append(result, name+"="+replacements[name])
	}
	return result
}

func workflowEnvironmentVariableControlled(name string) bool {
	if strings.HasPrefix(name, "GO") || strings.HasPrefix(name, "CGO") {
		return true
	}
	switch name {
	case "AR", "CC", "CXX", "FC", "GCCGO", "PKG_CONFIG",
		"GNUMAKEFLAGS", "MAKEFILES", "MAKEFLAGS", "MFLAGS":
		return true
	default:
		return false
	}
}
