package grammargen

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestExecutableRunnerRejectsScriptRuntimeAndUnknownTools(t *testing.T) {
	t.Parallel()
	runner := executableRunner{}
	for _, name := range []string{"bash", "python3", "unreviewed-native-tool"} {
		if _, err := runner.run(context.Background(), t.TempDir(), name, "-c", "true"); err == nil ||
			!strings.Contains(err.Error(), "executable") {
			t.Fatalf("runner accepted %q: %v", name, err)
		}
	}
}

func TestVerifyCheckoutRejectsCleanFilterBeforeItCanExecute(t *testing.T) {
	root := t.TempDir()
	runGrammarGit(t, root, "init", "-q")
	runGrammarGit(t, root, "config", "user.name", "Grammar Test")
	runGrammarGit(t, root, "config", "user.email", "grammar@example.invalid")
	if err := os.WriteFile(filepath.Join(root, ".gitattributes"), []byte("*.c filter=answer\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "parser.c"), []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGrammarGit(t, root, "add", ".gitattributes", "parser.c")
	runGrammarGit(t, root, "commit", "-q", "-m", "initial")
	commit := strings.TrimSpace(grammarGit(t, root, "rev-parse", "HEAD"))
	filter := filepath.Join(t.TempDir(), "grammar-filter-test")
	copyGrammarTestExecutable(t, filter)
	runGrammarGit(t, root, "config", "filter.answer.clean", filter)
	if err := os.WriteFile(filepath.Join(root, "parser.c"), []byte("after\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := verifyCheckout(context.Background(), executableRunner{}, grammarSpec{
		upstreamName: "fixture", upstreamCommit: commit,
		dirtyMessage: "dirty", dirtyPaths: []string{"parser.c"},
	}, root)
	if err == nil || !strings.Contains(err.Error(), "can delegate execution") {
		t.Fatalf("configured clean filter was not rejected: %v", err)
	}
	if _, err := os.Stat(filter + ".marker"); !os.IsNotExist(err) {
		t.Fatalf("configured clean filter executed: %v", err)
	}
}

func TestVerifyCheckoutDoesNotExecuteInitializedSubmoduleCleanFilter(t *testing.T) {
	root := t.TempDir()
	runGrammarGit(t, root, "init", "-q")
	runGrammarGit(t, root, "config", "user.name", "Grammar Test")
	runGrammarGit(t, root, "config", "user.email", "grammar@example.invalid")
	submodule := filepath.Join(root, "parser.c")
	if err := os.Mkdir(submodule, 0o755); err != nil {
		t.Fatal(err)
	}
	runGrammarGit(t, submodule, "init", "-q")
	runGrammarGit(t, submodule, "config", "user.name", "Grammar Test")
	runGrammarGit(t, submodule, "config", "user.email", "grammar@example.invalid")
	if err := os.WriteFile(filepath.Join(submodule, ".gitattributes"), []byte("*.c filter=answer\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	childPath := filepath.Join(submodule, "child.c")
	if err := os.WriteFile(childPath, []byte("before\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	runGrammarGit(t, submodule, "add", ".gitattributes", "child.c")
	runGrammarGit(t, submodule, "commit", "-q", "-m", "initial child")
	childCommit := strings.TrimSpace(grammarGit(t, submodule, "rev-parse", "HEAD"))
	if err := os.WriteFile(filepath.Join(root, ".gitmodules"), []byte(
		"[submodule \"parser\"]\n\tpath = parser.c\n\turl = ./parser.c\n\tignore = none\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	runGrammarGit(t, root, "add", ".gitmodules")
	runGrammarGit(t, root, "update-index", "--add", "--cacheinfo", "160000,"+childCommit+",parser.c")
	runGrammarGit(t, root, "commit", "-q", "-m", "initial superproject")
	commit := strings.TrimSpace(grammarGit(t, root, "rev-parse", "HEAD"))
	filter := filepath.Join(t.TempDir(), "grammar-filter-test")
	copyGrammarTestExecutable(t, filter)
	runGrammarGit(t, submodule, "config", "filter.answer.clean", filter)
	runGrammarGit(t, submodule, "config", "filter.answer.required", "true")
	if err := os.WriteFile(childPath, []byte("after\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err := verifyCheckout(context.Background(), executableRunner{}, grammarSpec{
		upstreamName: "fixture", upstreamCommit: commit,
		dirtyMessage: "dirty", dirtyPaths: []string{"parser.c"},
	}, root)
	if err != nil {
		t.Fatalf("submodule dirty state should be ignored safely: %v", err)
	}
	if _, err := os.Stat(filter + ".marker"); !os.IsNotExist(err) {
		t.Fatalf("initialized submodule clean filter executed: %v", err)
	}
}

func TestMain(m *testing.M) {
	executable, err := os.Executable()
	if err == nil && filepath.Base(executable) == "grammar-filter-test" {
		if err := os.WriteFile(executable+".marker", []byte("executed"), 0o600); err != nil {
			os.Exit(98)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func grammarGit(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = root
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %q: %v: %s", arguments, err, output)
	}
	return string(output)
}

func runGrammarGit(t *testing.T, root string, arguments ...string) {
	t.Helper()
	grammarGit(t, root, arguments...)
}

func copyGrammarTestExecutable(t *testing.T, path string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o700); err != nil {
		t.Fatal(err)
	}
}

func TestGrammarGoEnvironmentForbidsDelegatingFallbacks(t *testing.T) {
	t.Setenv("PATH", "/tmp/untrusted-bin")
	t.Setenv("GOPROXY", "direct")
	t.Setenv("GOFLAGS", "-toolexec=/tmp/tool")
	got := grammarToolEnvironment("go")
	want := []string{
		"CGO_ENABLED=0", "GO111MODULE=on", "GOAUTH=off", "GOENV=off",
		"GOFLAGS=-mod=readonly -trimpath -buildvcs=false", "GOTOOLCHAIN=local",
		"GONOPROXY=none", "GONOSUMDB=", "GOPRIVATE=",
		"GOPROXY=https://proxy.golang.org", "GOSUMDB=sum.golang.org",
		"GOVCS=*:off", "GOWORK=off", "HOME=" + os.Getenv("HOME"), "PATH=",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("grammar Go environment = %q, want %q", got, want)
	}
}

func TestValidateGrammarInvocationRejectsDelegationAndAcceptsReviewedShapes(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	compact := filepath.Join(root, "internal", "swiftgrammar", "compact.go")
	input := filepath.Join(root, "work", "input")
	output := filepath.Join(root, "work", "output")
	valid := []struct {
		name      string
		arguments []string
	}{
		{name: "git", arguments: []string{"rev-parse", "HEAD"}},
		{name: "git", arguments: []string{
			"diff", "--no-ext-diff", "--no-textconv", "--ignore-submodules=dirty", "--quiet", "--", "grammar.js",
		}},
		{name: "go", arguments: []string{"run", compact, input, output}},
		{name: "tree-sitter", arguments: []string{"--version"}},
		{name: "tree-sitter", arguments: []string{
			"generate", "--abi", "14", "--no-bindings", "src/grammar.json",
		}},
	}
	for _, test := range valid {
		if err := validateGrammarInvocation(root, test.name, test.arguments); err != nil {
			t.Errorf("reviewed %s invocation rejected: %v", test.name, err)
		}
	}
	invalid := []struct {
		name      string
		arguments []string
	}{
		{name: "git", arguments: []string{"-c", "alias.run=!bash -c true", "run"}},
		{name: "go", arguments: []string{"run", "/tmp/unreviewed.go"}},
		{name: "tree-sitter", arguments: []string{"generate", "--abi", "15", "src/grammar.json"}},
	}
	for _, test := range invalid {
		if err := validateGrammarInvocation(root, test.name, test.arguments); err == nil {
			t.Errorf("unreviewed %s invocation accepted: %q", test.name, test.arguments)
		}
	}
}
