package repoview

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestChangedPinsHeadAcrossRefMovement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell Git wrapper fixture is not portable to Windows")
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	runRealGit := func(args ...string) string {
		t.Helper()
		command := exec.Command(realGit, args...)
		command.Dir = root
		output, commandErr := command.CombinedOutput()
		if commandErr != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), commandErr, output)
		}
		return strings.TrimSpace(string(output))
	}
	runRealGit("init")
	runRealGit("config", "user.email", "repo-view@example.test")
	runRealGit("config", "user.name", "repo-view test")
	writeFile(t, root, "found.go", "package demo\n\nvar Value = 0\n")
	runRealGit("add", "found.go")
	runRealGit("commit", "-m", "base")
	base := runRealGit("rev-parse", "HEAD")

	writeFile(t, root, "found.go", "package demo\n\nvar Value = 1\n")
	runRealGit("add", "found.go")
	runRealGit("commit", "-m", "head B")
	headB := runRealGit("rev-parse", "HEAD")

	writeFile(t, root, "found.go", "package demo\n\nvar Value = 2\n")
	runRealGit("add", "found.go")
	treeC := runRealGit("write-tree")
	headC := runRealGit("commit-tree", treeC, "-p", base, "-m", "head C")
	runRealGit("reset", "--hard", headB)

	wrapperRoot := t.TempDir()
	wrapperPath := filepath.Join(wrapperRoot, "git")
	statePath := filepath.Join(wrapperRoot, "flipped")
	wrapper := `#!/bin/sh
real_git=` + shellSingleQuote(realGit) + `
flip_commit=` + shellSingleQuote(headC) + `
flip_state=` + shellSingleQuote(statePath) + `
flip=0
for argument in "$@"; do
  case "$argument" in
    HEAD|'HEAD^{commit}') flip=1 ;;
  esac
done
if [ "$flip" -eq 1 ] && [ ! -e "$flip_state" ]; then
  "$real_git" "$@"
  status=$?
  if [ "$status" -eq 0 ]; then
    : > "$flip_state"
    "$real_git" update-ref HEAD "$flip_commit" >/dev/null 2>&1 || exit 97
  fi
  exit "$status"
fi
exec "$real_git" "$@"
`
	if err := os.WriteFile(wrapperPath, []byte(wrapper), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", wrapperRoot+string(os.PathListSeparator)+os.Getenv("PATH"))

	response, err := mustView(t, root).Changed(Options{
		Base: base, Return: ReturnLine, MaxPatchLines: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.HeadCommit != headB || response.HeadSubject != "head B" ||
		!strings.Contains(response.Patch, "+var Value = 1") ||
		strings.Contains(response.Patch, "+var Value = 2") ||
		len(response.Results) != 1 || response.Results[0].Line != 3 ||
		response.Results[0].Code != "var Value = 1" {
		t.Fatalf("response mixed moving HEAD snapshots: %#v", response)
	}
	if moved := runRealGit("rev-parse", "HEAD"); moved != headC {
		t.Fatalf("wrapper did not move HEAD: got %s, want %s", moved, headC)
	}
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func TestChangedBaseReadsCommittedHeadSnapshot(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "repo-view@example.test")
	runGit(t, root, "config", "user.name", "repo-view test")
	writeFile(t, root, "found.go", "package demo\n\nfunc before() {}\n")
	runGit(t, root, "add", "found.go")
	runGit(t, root, "commit", "-m", "base")

	view := mustView(t, root)
	base := mustGitText(t, view, "rev-parse", "HEAD")
	writeFile(t, root, "found.go", "package demo\n\nfunc committed() {}\n")
	runGit(t, root, "add", "found.go")
	runGit(t, root, "commit", "-m", "head")
	head := mustGitText(t, view, "rev-parse", "HEAD")

	// Shift every worktree coordinate after HEAD without committing it.
	writeFile(t, root, "found.go", "// dirty insertion\npackage demo\n\nfunc committed() {}\n")
	lines, clean, err := view.readGitLinesAtRevision("found.go", head)
	if err != nil {
		t.Fatal(err)
	}
	if clean != "found.go" || len(lines) != 3 || lines[2] != "func committed() {}" {
		t.Fatalf("HEAD snapshot = %q, %q", clean, lines)
	}

	response, err := view.Changed(Options{
		Base: base, Return: ReturnLine, MaxPatchLines: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].Path != "found.go" ||
		response.Results[0].Line != 3 ||
		response.Results[0].Code != "func committed() {}" {
		t.Fatalf("base response used dirty worktree coordinates: %#v", response)
	}
}

func TestChangedBaseReadsRenamedHeadPathAndReportsDeletedPath(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "repo-view@example.test")
	runGit(t, root, "config", "user.name", "repo-view test")
	writeFile(t, root, "deleted.go", "package demo\n\nfunc deleted() {}\n")
	writeFile(t, root, "before.go", strings.Join([]string{
		"package demo", "", "// one", "// two", "// three", "// four",
		"// five", "// six", "// seven", "// eight", "func before() {}", "",
	}, "\n"))
	runGit(t, root, "add", "deleted.go", "before.go")
	runGit(t, root, "commit", "-m", "base")

	view := mustView(t, root)
	base := mustGitText(t, view, "rev-parse", "HEAD")
	if err := os.Remove(filepath.Join(root, "deleted.go")); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "mv", "before.go", "after.go")
	writeFile(t, root, "after.go", strings.Join([]string{
		"package demo", "", "// one", "// two", "// three", "// four",
		"// five", "// six", "// seven", "// eight", "func after() {}", "",
	}, "\n"))
	runGit(t, root, "add", "-A")
	runGit(t, root, "commit", "-m", "rename and delete")
	head := mustGitText(t, view, "rev-parse", "HEAD")
	if err := os.Remove(filepath.Join(root, "after.go")); err != nil {
		t.Fatal(err)
	}

	lines, _, err := view.readGitLinesAtRevision("after.go", head)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 11 || lines[10] != "func after() {}" {
		t.Fatalf("renamed HEAD snapshot = %#v", lines)
	}
	if _, _, err := view.readGitLinesAtRevision("deleted.go", head); err == nil {
		t.Fatal("deleted HEAD path unexpectedly resolved")
	}

	response, err := view.Changed(Options{
		Base: base, Return: ReturnLine, MaxPatchLines: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	deletedResult := false
	renamedResult := false
	for _, result := range response.Results {
		switch result.Path {
		case "deleted.go":
			deletedResult = result.Kind == "file"
		case "after.go":
			renamedResult = result.Line == 11 && result.Code == "func after() {}"
		}
	}
	if !deletedResult || !renamedResult {
		t.Fatalf("base rename/delete response = %#v", response)
	}
}

func TestReadGitLinesAtRevisionTreatsPathLiterallyAndRejectsSymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("colon and symbolic-link fixture is not portable to Windows")
	}
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "repo-view@example.test")
	runGit(t, root, "config", "user.name", "repo-view test")
	literal := ":(glob)-[literal]*.go"
	writeFile(t, root, literal, "package literal\n")
	if err := os.Symlink(literal, filepath.Join(root, "linked.go")); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	runGit(t, root, "--literal-pathspecs", "add", "--", literal, "linked.go")
	runGit(t, root, "commit", "-m", "literal path")

	view := mustView(t, root)
	head := mustGitText(t, view, "rev-parse", "HEAD")
	lines, clean, err := view.readGitLinesAtRevision(literal, head)
	if err != nil {
		t.Fatal(err)
	}
	if clean != literal || len(lines) != 1 || lines[0] != "package literal" {
		t.Fatalf("literal HEAD path = %q, %#v", clean, lines)
	}
	if _, _, err := view.readGitLinesAtRevision("linked.go", head); err == nil ||
		!strings.Contains(err.Error(), "not a regular file") {
		t.Fatalf("symlink HEAD path error = %v", err)
	}
}

func mustGitText(t *testing.T, view *RepoView, args ...string) string {
	t.Helper()
	value, err := view.gitText(args...)
	if err != nil {
		t.Fatal(err)
	}
	return value
}
