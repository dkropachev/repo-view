package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunRejectsNonPositiveCases(t *testing.T) {
	for _, value := range []string{"0", "-1"} {
		t.Run(value, func(t *testing.T) {
			if status := run([]string{"--cases", value}); status != 2 {
				t.Fatalf("run --cases %s status = %d, want 2", value, status)
			}
		})
	}
}

func TestEnsureManagedRepositoriesClonesOnceAndReuses(t *testing.T) {
	remote := initializeValidationRepository(t, "remote")
	root := t.TempDir()
	specPath := filepath.Join(root, "repositories.tsv")
	cloneRoot := filepath.Join(root, "clones")
	if err := os.WriteFile(
		specPath,
		[]byte("sample\t"+remote+"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	repositories, source, err := ensureManagedRepositories(specPath, cloneRoot)
	if err != nil {
		t.Fatal(err)
	}
	if len(repositories) != 1 {
		t.Fatalf("repositories = %v, want one", repositories)
	}
	wantRepository := filepath.Join(cloneRoot, "sample")
	if repositories[0] != wantRepository {
		t.Fatalf("repository = %q, want %q", repositories[0], wantRepository)
	}
	if source != specPath+" -> "+cloneRoot {
		t.Fatalf("source = %q", source)
	}
	firstHead := validationGitOutput(t, wantRepository, "rev-parse", "HEAD")
	firstOriginHead := validationGitOutput(
		t,
		wantRepository,
		"rev-parse",
		"refs/remotes/origin/HEAD",
	)
	marker := filepath.Join(wantRepository, "local-marker")
	if err := os.WriteFile(marker, []byte("preserve me\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(
		filepath.Join(remote, "fixture.go"),
		[]byte("package fixture\n\nfunc Second() {}\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	validationGit(t, remote, "add", "fixture.go")
	validationGit(t, remote, "commit", "-m", "second")
	remoteHead := validationGitOutput(t, remote, "rev-parse", "HEAD")
	if remoteHead == firstHead {
		t.Fatal("remote fixture did not advance")
	}

	repositories, _, err = ensureManagedRepositories(specPath, cloneRoot)
	if err != nil {
		t.Fatal(err)
	}
	if repositories[0] != wantRepository {
		t.Fatalf("reused repository = %q, want %q", repositories[0], wantRepository)
	}
	if got := validationGitOutput(t, wantRepository, "rev-parse", "HEAD"); got != firstHead {
		t.Fatalf("existing clone changed from %s to %s", firstHead, got)
	}
	if got := validationGitOutput(
		t,
		wantRepository,
		"rev-parse",
		"refs/remotes/origin/HEAD",
	); got != firstOriginHead {
		t.Fatalf("existing clone fetched origin from %s to %s", firstOriginHead, got)
	}
	if content, err := os.ReadFile(marker); err != nil || string(content) != "preserve me\n" {
		t.Fatalf("existing clone marker changed: content=%q err=%v", content, err)
	}
}

func TestEnsureManagedRepositoriesReusesRelativeLocalOrigin(t *testing.T) {
	remote := initializeValidationRepository(t, "relative-remote")
	workingDirectory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	relativeRemote, err := filepath.Rel(workingDirectory, remote)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	specPath := filepath.Join(root, "repositories.tsv")
	cloneRoot := filepath.Join(root, "clones")
	if err := os.WriteFile(
		specPath,
		[]byte("sample\t"+relativeRemote+"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	if _, _, err := ensureManagedRepositories(specPath, cloneRoot); err != nil {
		t.Fatalf("initial clone: %v", err)
	}
	if _, _, err := ensureManagedRepositories(specPath, cloneRoot); err != nil {
		t.Fatalf("reuse relative clone: %v", err)
	}
}

func TestRepositoryOriginsResolveRelativeURLsFromTheirUseSites(t *testing.T) {
	root := t.TempDir()
	repository := filepath.Join(root, "clones", "sample")
	actualRemote := filepath.Join(root, "clones", "remote")
	if !repositoryOriginsEqual("../remote", actualRemote, repository) {
		t.Fatal("equivalent local origins were rejected")
	}
	if repositoryOriginsEqual("../remote", "../remote", repository) {
		t.Fatal("equal relative text with different resolution bases was accepted")
	}
}

func TestEnsureManagedRepositoriesRejectsConflicts(t *testing.T) {
	remoteA := initializeValidationRepository(t, "remote-a")
	remoteB := initializeValidationRepository(t, "remote-b")

	t.Run("wrong origin", func(t *testing.T) {
		root := t.TempDir()
		cloneRoot := filepath.Join(root, "clones")
		if err := os.MkdirAll(cloneRoot, 0o755); err != nil {
			t.Fatal(err)
		}
		validationGit(t, root, "clone", remoteA, filepath.Join(cloneRoot, "sample"))
		specPath := filepath.Join(root, "repositories.tsv")
		if err := os.WriteFile(specPath, []byte("sample "+remoteB+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, _, err := ensureManagedRepositories(specPath, cloneRoot)
		if err == nil || !strings.Contains(err.Error(), "has origin") {
			t.Fatalf("wrong origin error = %v", err)
		}
	})

	t.Run("unsafe name", func(t *testing.T) {
		root := t.TempDir()
		specPath := filepath.Join(root, "repositories.tsv")
		if err := os.WriteFile(specPath, []byte("../escape "+remoteA+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, _, err := ensureManagedRepositories(specPath, filepath.Join(root, "clones"))
		if err == nil || !strings.Contains(err.Error(), "invalid repository name") {
			t.Fatalf("unsafe name error = %v", err)
		}
	})

	t.Run("symlink clone root", func(t *testing.T) {
		root := t.TempDir()
		target := filepath.Join(root, "target")
		if err := os.Mkdir(target, 0o755); err != nil {
			t.Fatal(err)
		}
		cloneRoot := filepath.Join(root, "clones")
		if err := os.Symlink(target, cloneRoot); err != nil {
			t.Fatal(err)
		}
		specPath := filepath.Join(root, "repositories.tsv")
		if err := os.WriteFile(specPath, []byte("sample "+remoteA+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, _, err := ensureManagedRepositories(specPath, cloneRoot)
		if err == nil || !strings.Contains(err.Error(), "clone root") {
			t.Fatalf("symlink clone root error = %v", err)
		}
	})
}

func TestRunRejectsConflictingExistingRepositoryInputs(t *testing.T) {
	if status := run([]string{"--repo-list", "repos.txt", "--repo-root", "repos"}); status != 2 {
		t.Fatalf("status = %d, want 2", status)
	}
}

func TestReadSourceFilesIncludesJavaScriptAndTypeScriptModuleExtensions(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	for name, content := range map[string]string{
		"common.cjs":  "exports.value = 1;\n",
		"common.cts":  "export = { value: 1 };\n",
		"module.mjs":  "export const value = 1;\n",
		"module.mts":  "export const value: number = 1;\n",
		"source.ts":   "export const value: number = 1;\n",
		"view.tsx":    "export const View = () => <div />;\n",
		"ignored.txt": "const value = 1;\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files, err := readSourceFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(files))
	for _, file := range files {
		got = append(got, file.rel)
	}
	const want = "common.cjs,common.cts,module.mjs,module.mts,source.ts,view.tsx"
	if strings.Join(got, ",") != want {
		t.Fatalf("source files = %#v, want %s", got, want)
	}
}

func TestReadSourceFilesIncludesRegisteredCSharpAndCPPExtensions(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	for name, content := range map[string]string{
		"script.csx":  "public class ScriptEntry {}\n",
		"source.cxx":  "class Source {};\n",
		"ignored.txt": "class Ignored {};\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files, err := readSourceFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(files))
	for _, file := range files {
		got = append(got, file.rel)
	}
	const want = "script.csx,source.cxx"
	if strings.Join(got, ",") != want {
		t.Fatalf("registered source files = %#v, want %s", got, want)
	}
}

func TestReadSourceFilesIncludesKotlinSourceAndScriptExtensions(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	for name, content := range map[string]string{
		"build.gradle.kts": "plugins { kotlin(\"jvm\") }\n",
		"source.kt":        "class Source\n",
		"ignored.txt":      "class Ignored\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files, err := readSourceFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(files))
	for _, file := range files {
		got = append(got, file.rel)
	}
	const want = "build.gradle.kts,source.kt"
	if strings.Join(got, ",") != want {
		t.Fatalf("source files = %#v, want %s", got, want)
	}
}

func TestReadSourceFilesIncludesSwiftSourceExtension(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	for name, content := range map[string]string{
		"Package.swift": "import PackageDescription\nlet package = Package(name: \"Demo\")\n",
		"Source.swift":  "struct Source {}\n",
		"ignored.txt":   "struct Ignored {}\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files, err := readSourceFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(files))
	for _, file := range files {
		got = append(got, file.rel)
	}
	const want = "Package.swift,Source.swift"
	if strings.Join(got, ",") != want {
		t.Fatalf("Swift source files = %#v, want %s", got, want)
	}
}

func TestReadSourceFilesIncludesModulaSourceExtensions(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	for name, content := range map[string]string{
		"Program.mod": "MODULE Program; BEGIN END Program.\n",
		"Storage.def": "DEFINITION MODULE Storage; END Storage.\n",
		"ignored.txt": "MODULE Ignored; END Ignored.\n",
	} {
		if err := os.WriteFile(filepath.Join(root, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	files, err := readSourceFiles(root)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(files))
	for _, file := range files {
		got = append(got, file.rel)
	}
	const want = "Program.mod,Storage.def"
	if strings.Join(got, ",") != want {
		t.Fatalf("Modula source files = %#v, want %s", got, want)
	}
}

func TestBuildIndexFiltersModulaKeywordsOnlyForModulaFiles(t *testing.T) {
	t.Parallel()

	files := []fileData{
		{
			rel:  "Keywords.java",
			ext:  ".java",
			line: []string{"int MODULE = 1;", "MODULE++;", "int TYPE = 1;", "TYPE++;"},
		},
		{
			rel:  "Keywords.mod",
			ext:  ".mod",
			line: []string{"MODULE", "MODULE", "TYPE", "TYPE"},
		},
	}

	index := buildIndex(files)
	for _, symbol := range []string{"MODULE", "TYPE"} {
		locations := index[symbol]
		if len(locations) != 2 {
			t.Fatalf("%s locations = %#v, want two Java locations", symbol, locations)
		}
		for _, location := range locations {
			if location.path != "Keywords.java" {
				t.Fatalf("%s location = %#v, want only Java locations", symbol, location)
			}
		}
	}

	if got, want := strings.Join(selectSymbols(index, 2), ","), "MODULE,TYPE"; got != want {
		t.Fatalf("sampled symbols = %q, want %q", got, want)
	}
}

func TestIndependentContainsSymbolUsesUnicodeContinuationBoundaries(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		line string
		want bool
	}{
		{name: "independent", line: "helper()", want: true},
		{name: "Unicode letter before", line: "αhelper", want: false},
		{name: "combining acute after", line: "helper\u0301", want: false},
		{name: "combining Hebrew point after", line: "helper\u05C1", want: false},
		{name: "format character after", line: "helper\u200E", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := independentContainsSymbol(test.line, "helper"); got != test.want {
				t.Fatalf("independentContainsSymbol(%q, helper) = %v, want %v", test.line, got, test.want)
			}
		})
	}
}

func TestReadLinesAcceptsMinifiedJavaScriptLineOverOneMiB(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "minified.js")
	body := `const payload = "` + strings.Repeat("x", 1300<<10) + `";` + "\n"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}

	lines, err := readLines(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 1 || len(lines[0]) != len(body)-1 {
		t.Fatalf("lines = %d with first length %d, want one line of length %d", len(lines), len(lines[0]), len(body)-1)
	}
}

func TestValidateRepoAcceptsJavaScriptDefinitionAndReferenceOnSameLine(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(root, "fixture.js"),
		[]byte("const shared = shared || 1;\nconsole.log(shared);\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	validated, err := validateRepo(root, 1)
	if err != nil {
		t.Fatal(err)
	}
	if validated != 1 {
		t.Fatalf("validated symbols = %d, want 1", validated)
	}
}

func TestValidateRepoAcceptsSwiftDefinitionAndReferences(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	const source = `func shared() {}

struct Caller {
    func call() {
        shared()
    }
}
`
	if err := os.WriteFile(
		filepath.Join(root, "Fixture.swift"),
		[]byte(source),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	validated, err := validateRepo(root, 1)
	if err != nil {
		t.Fatal(err)
	}
	if validated != 1 {
		t.Fatalf("validated Swift symbols = %d, want 1", validated)
	}
}

func TestDiscoverReposIncludesLinkedWorktrees(t *testing.T) {
	repository := initializeValidationRepository(t, "worktree-source")
	root := t.TempDir()
	worktree := filepath.Join(root, "linked-worktree")
	validationGit(t, repository, "worktree", "add", worktree, "HEAD")

	repositories, _, err := discoverRepos("", root)
	if err != nil {
		t.Fatal(err)
	}
	if len(repositories) != 1 || repositories[0] != worktree {
		t.Fatalf("repositories = %#v, want [%q]", repositories, worktree)
	}
}

func TestReadSourceFilesPropagatesScannerErrors(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "oversized.go")
	if err := os.WriteFile(path, []byte(strings.Repeat("x", maximumSourceLineBytes+1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := readSourceFiles(root); err == nil ||
		!strings.Contains(err.Error(), "token too long") {
		t.Fatalf("readSourceFiles error = %v", err)
	}
}

func initializeValidationRepository(t *testing.T, name string) string {
	t.Helper()
	repository := filepath.Join(t.TempDir(), name)
	if err := os.Mkdir(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	validationGit(t, repository, "init", "-b", "main")
	validationGit(t, repository, "config", "user.name", "repo-view test")
	validationGit(t, repository, "config", "user.email", "repo-view@example.invalid")
	if err := os.WriteFile(
		filepath.Join(repository, "fixture.go"),
		[]byte("package fixture\n\nfunc First() {}\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	validationGit(t, repository, "add", "fixture.go")
	validationGit(t, repository, "commit", "-m", "initial")
	return repository
}

func validationGit(t *testing.T, directory string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
}

func validationGitOutput(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", directory}, arguments...)...)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return strings.TrimSpace(string(output))
}
