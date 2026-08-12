package grammargen

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/yapless/scopesifter/internal/gitdiffcontract"
	"github.com/yapless/scopesifter/internal/processpolicy"
)

type commandRunner interface {
	run(context.Context, string, string, ...string) ([]byte, error)
}

type executableRunner struct{}

func (executableRunner) run(
	ctx context.Context,
	directory string,
	name string,
	arguments ...string,
) ([]byte, error) {
	if err := validateGrammarInvocation(directory, name, arguments); err != nil {
		return nil, err
	}
	commandArguments := arguments
	if name == "git" {
		if !processpolicy.IsGitRepositoryConfigQuery(arguments) {
			commandArguments = append(gitdiffcontract.InvocationPrefix(), arguments...)
		}
		if err := processpolicy.ValidateGit(commandArguments...); err != nil {
			return nil, fmt.Errorf("reject grammar Git invocation: %w", err)
		}
	}
	command, nativeFile, err := processpolicy.NativeCommandContext(ctx, name, commandArguments...)
	if err != nil {
		return nil, fmt.Errorf("pin native grammar tool: %w", err)
	}
	command.Dir = directory
	command.Env = grammarToolEnvironment(name)
	output, err := command.CombinedOutput()
	closeErr := nativeFile.Close()
	if err != nil {
		output = bytes.TrimSpace(output)
		if len(output) > 0 {
			return output, fmt.Errorf("run %s: %w: %s", name, err, output)
		}
		return output, fmt.Errorf("run %s: %w", name, err)
	}
	if closeErr != nil {
		return output, fmt.Errorf("close native grammar tool %s: %w", name, closeErr)
	}
	return output, nil
}

func grammarToolEnvironment(name string) []string {
	pathValue := os.Getenv("PATH")
	switch name {
	case "git":
		return gitdiffcontract.Environment(os.DevNull)
	case "go":
		return []string{
			"CGO_ENABLED=0", "GO111MODULE=on", "GOAUTH=off", "GOENV=off",
			"GOFLAGS=-mod=readonly -trimpath -buildvcs=false", "GOTOOLCHAIN=local",
			"GONOPROXY=none", "GONOSUMDB=", "GOPRIVATE=",
			"GOPROXY=https://proxy.golang.org", "GOSUMDB=sum.golang.org",
			"GOVCS=*:off", "GOWORK=off", "HOME=" + os.Getenv("HOME"), "PATH=",
		}
	case "tree-sitter":
		return []string{
			"HOME=" + os.Getenv("HOME"), "LANG=C", "LC_ALL=C", "PATH=" + pathValue,
		}
	default:
		return nil
	}
}

func validateGrammarInvocation(directory, name string, arguments []string) error {
	if err := processpolicy.ValidateExecutable(name); err != nil {
		return fmt.Errorf("reject grammar tool executable: %w", err)
	}
	switch name {
	case "git":
		switch {
		case processpolicy.IsGitRepositoryConfigQuery(arguments):
			return nil
		case slices.Equal(arguments, []string{"rev-parse", "HEAD"}):
			return nil
		case len(arguments) > 6 && slices.Equal(
			arguments[:6],
			[]string{"diff", "--no-ext-diff", "--no-textconv", "--ignore-submodules=dirty", "--quiet", "--"},
		):
			for _, path := range arguments[6:] {
				if !safeGrammarRelativePath(path) {
					return fmt.Errorf("unsafe grammar Git path %q", path)
				}
			}
			return nil
		default:
			return fmt.Errorf("unsupported grammar Git invocation %q", arguments)
		}
	case "go":
		if err := processpolicy.Validate(name, arguments...); err != nil {
			return fmt.Errorf("reject grammar Go invocation: %w", err)
		}
		if validGrammarGoInvocation(directory, arguments) {
			return nil
		}
		return fmt.Errorf("unsupported grammar Go invocation %q", arguments)
	case "tree-sitter":
		if err := processpolicy.Validate(name, arguments...); err != nil {
			return fmt.Errorf("reject grammar tree-sitter invocation: %w", err)
		}
		wantGenerate := []string{
			"generate", "--abi", "14", "--no-bindings", "src/grammar.json",
		}
		if slices.Equal(arguments, []string{"--version"}) || slices.Equal(arguments, wantGenerate) {
			return nil
		}
		return fmt.Errorf("unsupported grammar tree-sitter invocation %q", arguments)
	default:
		return fmt.Errorf("unsupported grammar tool executable %q", name)
	}
}

func validGrammarGoInvocation(directory string, arguments []string) bool {
	if len(arguments) == 8 && arguments[0] == "run" &&
		arguments[1] == "github.com/dcosson/treesitter-go/cmd/tsgo-generate@"+
			treeSitterGeneratorVersion &&
		arguments[2] == "-parser" && cleanAbsolutePath(arguments[3]) &&
		arguments[4] == "-package" && validGrammarPackage(arguments[5]) &&
		arguments[6] == "-output" && cleanAbsolutePath(arguments[7]) {
		return true
	}
	if len(arguments) != 4 || arguments[0] != "run" ||
		!cleanAbsolutePath(arguments[1]) || !pathWithin(directory, arguments[1]) ||
		!cleanAbsolutePath(arguments[2]) || !cleanAbsolutePath(arguments[3]) {
		return false
	}
	relative, err := filepath.Rel(directory, arguments[1])
	if err != nil {
		return false
	}
	relative = filepath.ToSlash(relative)
	return relative == "internal/csharpgrammar/compact.go" ||
		relative == "internal/kotlingrammar/compact.go" ||
		relative == "internal/swiftgrammar/compact.go" ||
		relative == "internal/swiftgrammar/split.go"
}

func validGrammarPackage(name string) bool {
	return name == "csharpgrammar" || name == "kotlingrammar" || name == "swiftgrammar"
}

func cleanAbsolutePath(value string) bool {
	return filepath.IsAbs(value) && filepath.Clean(value) == value
}

func pathWithin(root, candidate string) bool {
	relative, err := filepath.Rel(root, candidate)
	return err == nil && relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func safeGrammarRelativePath(value string) bool {
	return value != "" && !filepath.IsAbs(value) && filepath.Clean(value) == value &&
		value != ".." && !strings.HasPrefix(value, ".."+string(filepath.Separator)) &&
		!strings.HasPrefix(value, "-")
}
