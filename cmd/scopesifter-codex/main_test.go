package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestMain(m *testing.M) {
	if runSignalLifecycleHelper() {
		os.Exit(0)
	}
	if os.Getenv("SCOPESIFTER_CODEX_TEST_HELPER") == "1" {
		fmt.Printf("PATH=%s\n", os.Getenv("PATH"))
		fmt.Printf("LIMIT=%s\n", os.Getenv("SCOPESIFTER_LIMIT_CAP"))
		fmt.Printf("CONTEXT=%s\n", os.Getenv("SCOPESIFTER_CONTEXT_CAP"))
		fmt.Printf("CODE_LINES=%s\n", os.Getenv("SCOPESIFTER_MAX_CODE_LINES_CAP"))
		fmt.Printf("PATCH_LINES=%s\n", os.Getenv("SCOPESIFTER_MAX_PATCH_LINES_CAP"))
		fmt.Printf("BUDGET=%s\n", os.Getenv("SCOPESIFTER_NAVIGATION_BUDGET_FILE"))
		fmt.Printf("COMMAND_CAP=%s\n", os.Getenv("SCOPESIFTER_NAVIGATION_COMMAND_CAP"))
		joined := strings.Join(os.Args[1:], "\n")
		fmt.Printf("HAS_INSTRUCTIONS=%t\n", strings.Contains(joined, "ScopeSifter CLI is available:"))
		fmt.Printf("HAS_FORCED=%t\n", strings.Contains(joined, "start with changed") || strings.Contains(joined, "Pick exactly one initial command"))
		fmt.Printf("HAS_REASONING=%t\n", strings.Contains(joined, `model_reasoning_effort="medium"`))
		fmt.Printf("HAS_ORIGINAL_ARGV=%t\n", strings.HasSuffix(joined, "exec\n--json\nprompt"))
		os.Exit(31)
	}
	os.Exit(m.Run())
}

func TestHasModuleDeclaration(t *testing.T) {
	t.Parallel()
	if !hasModuleDeclaration("module github.com/yapless/scopesifter\n\ngo 1.26\n") {
		t.Fatal("expected exact module declaration to be accepted")
	}
	for _, manifest := range []string{
		"module github.com/example/scopesifter\n",
		"// module github.com/yapless/scopesifter\n",
		"module github.com/yapless/scopesifter-extra\n",
	} {
		if hasModuleDeclaration(manifest) {
			t.Fatalf("unexpected module declaration match in %q", manifest)
		}
	}
}

func TestCommandRejectsInvalidConfigurationBeforeBuild(t *testing.T) {
	root := filepath.Join("..", "..")
	binary := filepath.Join(t.TempDir(), "scopesifter-codex")
	build := exec.Command(
		"go",
		"build",
		"-trimpath",
		"-o",
		binary,
		"./cmd/scopesifter-codex",
	)
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build launcher: %v\n%s", err, output)
	}
	baseEnvironment := make([]string, 0, len(os.Environ()))
	for _, variable := range os.Environ() {
		if !strings.HasPrefix(variable, "SCOPESIFTER_") {
			baseEnvironment = append(baseEnvironment, variable)
		}
	}
	for _, test := range []struct {
		name        string
		environment string
		want        string
	}{
		{"zero result limit", "SCOPESIFTER_LIMIT_CAP=0", "SCOPESIFTER_LIMIT_CAP must be a positive integer"},
		{"zero code line cap", "SCOPESIFTER_MAX_CODE_LINES_CAP=0", "SCOPESIFTER_MAX_CODE_LINES_CAP must be a positive integer"},
		{"zero patch line cap", "SCOPESIFTER_MAX_PATCH_LINES_CAP=0", "SCOPESIFTER_MAX_PATCH_LINES_CAP must be a positive integer"},
	} {
		t.Run(test.name, func(t *testing.T) {
			command := exec.Command(binary, "exec")
			command.Dir = root
			command.Env = append(append([]string(nil), baseEnvironment...), test.environment)
			output, err := command.CombinedOutput()
			var exitError *exec.ExitError
			if !errors.As(err, &exitError) || exitError.ExitCode() != 2 ||
				!strings.Contains(string(output), test.want) {
				t.Fatalf("error = %v, output = %q; want exit 2 containing %q", err, output, test.want)
			}
		})
	}
}

func TestCommandBuildsAndRunsCodexWithFailClosedEnvironment(t *testing.T) {
	root := filepath.Join("..", "..")
	temporary := t.TempDir()
	launcher := filepath.Join(temporary, "scopesifter-codex")
	build := exec.Command(
		"go",
		"build",
		"-trimpath",
		"-o",
		launcher,
		"./cmd/scopesifter-codex",
	)
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build launcher: %v\n%s", err, output)
	}
	testExecutable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fakeBin := filepath.Join(temporary, "fake-bin")
	if err := os.Mkdir(fakeBin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(testExecutable, filepath.Join(fakeBin, "codex")); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(temporary, "run directory")
	command := exec.Command(launcher, "exec", "--json", "prompt")
	command.Dir = root
	baseEnvironment := make([]string, 0, len(os.Environ()))
	for _, variable := range os.Environ() {
		if !strings.HasPrefix(variable, "SCOPESIFTER_") {
			baseEnvironment = append(baseEnvironment, variable)
		}
	}
	command.Env = append([]string(nil), baseEnvironment...)
	command.Env = append(command.Env,
		"PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"SCOPESIFTER_CODEX_TEST_HELPER=1",
		"SCOPESIFTER_CACHE_DIR="+filepath.Join(temporary, "cache"),
		"SCOPESIFTER_BIN_DIR="+binDir,
		"SCOPESIFTER_LIMIT_CAP=22",
		"SCOPESIFTER_CONTEXT_CAP=24",
		"SCOPESIFTER_MAX_CODE_LINES_CAP=62",
		"SCOPESIFTER_MAX_PATCH_LINES_CAP=302",
		"SCOPESIFTER_REASONING_EFFORT=medium",
		"SCOPESIFTER_NAVIGATION_COMMAND_CAP=2",
		"SCOPESIFTER_NAVIGATION_BUDGET_FILE=/must/not/leak",
	)
	output, commandErr := command.CombinedOutput()
	var exitError *exec.ExitError
	if !errors.As(commandErr, &exitError) || exitError.ExitCode() != 31 {
		t.Fatalf("error = %v, output = %s", commandErr, output)
	}
	text := string(output)
	for _, wanted := range []string{
		"PATH=" + binDir + string(os.PathListSeparator) + fakeBin,
		"LIMIT=22",
		"CONTEXT=24",
		"CODE_LINES=62",
		"PATCH_LINES=302",
		"BUDGET=\n",
		"COMMAND_CAP=\n",
		"HAS_INSTRUCTIONS=true",
		"HAS_FORCED=false",
		"HAS_REASONING=true",
		"HAS_ORIGINAL_ARGV=true",
	} {
		if !strings.Contains(text, wanted) {
			t.Errorf("output omits %q:\n%s", wanted, text)
		}
	}
	if _, err := os.Lstat(binDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary bin directory was not removed: %v", err)
	}
}
