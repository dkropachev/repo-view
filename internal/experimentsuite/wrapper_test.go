package experimentsuite

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepoViewWrapperMechanicalSemanticsWithSpacedPaths(t *testing.T) {
	for _, name := range []string{"bash", "git", "go"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skipf("%s is required: %v", name, err)
		}
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Join(t.TempDir(), "wrapper paths")
	if err := os.Mkdir(root, 0o755); err != nil {
		t.Fatal(err)
	}
	repoAlias := filepath.Join(root, "repo source")
	if err := os.Symlink(repoRoot, repoAlias); err != nil {
		t.Fatal(err)
	}
	worktree := filepath.Join(root, "target worktree")
	if err := os.Mkdir(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	runGitCommand(t, worktree, "init", "-q")
	runGitCommand(t, worktree, "config", "user.name", "Repo View Test")
	runGitCommand(t, worktree, "config", "user.email", "repo-view@example.invalid")
	targetFile := filepath.Join(worktree, "target.go")
	if err := os.WriteFile(targetFile, []byte("package target\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGitCommand(t, worktree, "add", "target.go")
	runGitCommand(t, worktree, "commit", "-qm", "base")
	base := strings.TrimSpace(runGitCommand(t, worktree, "rev-parse", "HEAD"))
	if err := os.WriteFile(
		targetFile,
		[]byte("package target\n\nconst Changed = true\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	runGitCommand(t, worktree, "add", "target.go")
	runGitCommand(t, worktree, "commit", "-qm", "target")

	fakeBin := filepath.Join(root, "fake tools")
	if err := os.Mkdir(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	fakeCodex := filepath.Join(fakeBin, "codex")
	if err := os.WriteFile(fakeCodex, []byte(`#!/usr/bin/env bash
command="repo-view changed --root . --base ${REPO_VIEW_REQUIRED_BASE_COMMIT} --return context --context 1 --limit 5 --max-code-lines 6 --max-patch-lines 7 --json"
printf '{"type":"item.started","item":{"id":"command-1","type":"command_execution","command":"%s"}}\n' "${command}"
repo-view changed \
  --root . \
  --base "${REPO_VIEW_REQUIRED_BASE_COMMIT}" \
  --return context \
  --context 1 \
  --limit 5 \
  --max-code-lines 6 \
  --max-patch-lines 7 \
  --json
status=$?
printf '{"type":"item.completed","item":{"id":"command-1","type":"command_execution","command":"%s","status":"%s","exit_code":%d}}\n' \
  "${command}" "$(if [[ ${status} -eq 0 ]]; then printf completed; else printf failed; fi)" "${status}"
exit "${status}"
`), 0o755); err != nil {
		t.Fatal(err)
	}

	cacheDir := filepath.Join(root, "cache directory")
	binParent := filepath.Join(root, "binary parent")
	if err := os.Mkdir(binParent, 0o755); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(binParent, "binary directory")
	wrapper := filepath.Join(repoAlias, "scripts", "codex-with-repo-view")
	command := exec.Command("bash", wrapper, "exec", "--json", "ignored prompt")
	command.Dir = worktree
	command.Env = append(os.Environ(),
		"PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"REPO_VIEW_CACHE_DIR="+cacheDir,
		"REPO_VIEW_BIN_DIR="+binDir,
		"REPO_VIEW_CHANGED_RETURN=context",
		"REPO_VIEW_CHANGED_CONTEXT=1",
		"REPO_VIEW_CHANGED_LIMIT=5",
		"REPO_VIEW_CHANGED_MAX_CODE_LINES=6",
		"REPO_VIEW_CHANGED_MAX_PATCH_LINES=7",
		"REPO_VIEW_NAVIGATION_CONTEXT_CAP=2",
		"REPO_VIEW_NAVIGATION_COMMAND_CAP=1",
		"REPO_VIEW_REQUIRED_ROOT="+worktree,
		"REPO_VIEW_REQUIRED_BASE_COMMIT="+base,
		"REPO_VIEW_REQUIRED_CHANGED_RETURN=context",
		"REPO_VIEW_REQUIRED_CHANGED_CONTEXT=1",
		"REPO_VIEW_REQUIRE_NAVIGATION_SEMANTICS=1",
		"GOENV=/does/not/exist/go.env",
		"GOWORK=/does/not/exist/go.work",
		"GOFLAGS=-toolexec=/does/not/exist/toolexec",
		"GOTOOLCHAIN=go1.99.0",
		"GOOS=plan9",
		"GOARCH=386",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("wrapper failed with spaced paths: %v\n%s", err, output)
	}
	for _, required := range []string{
		`"base_commit":"` + base + `"`,
		`"navigation_budget":{"used":1,"limit":1,"remaining":0}`,
	} {
		if !strings.Contains(string(output), required) {
			t.Fatalf("wrapper output lacks %q:\n%s", required, output)
		}
	}
}

func TestRepoViewWrapperRejectsUnsafeCacheAndBinPaths(t *testing.T) {
	if _, err := exec.LookPath("bash"); err != nil {
		t.Skipf("bash is required: %v", err)
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	wrapper := filepath.Join(repoRoot, "scripts", "codex-with-repo-view")
	root := t.TempDir()
	symlinkTarget := filepath.Join(root, "symlink target")
	if err := os.Mkdir(symlinkTarget, 0o755); err != nil {
		t.Fatal(err)
	}
	cacheSymlink := filepath.Join(root, "cache symlink")
	if err := os.Symlink(symlinkTarget, cacheSymlink); err != nil {
		t.Fatal(err)
	}
	binParentSymlink := filepath.Join(root, "bin parent symlink")
	if err := os.Symlink(symlinkTarget, binParentSymlink); err != nil {
		t.Fatal(err)
	}
	existingBinParent := filepath.Join(root, "existing bin parent")
	if err := os.Mkdir(existingBinParent, 0o755); err != nil {
		t.Fatal(err)
	}
	existingBin := filepath.Join(existingBinParent, "existing bin")
	if err := os.Mkdir(existingBin, 0o755); err != nil {
		t.Fatal(err)
	}

	for _, current := range []struct {
		name      string
		cachePath string
		binPath   string
		want      string
	}{
		{
			name:      "cache symlink",
			cachePath: cacheSymlink,
			want:      "REPO_VIEW_CACHE_DIR must not contain a symlink component",
		},
		{
			name:      "bin parent symlink",
			cachePath: filepath.Join(root, "safe cache one"),
			binPath:   filepath.Join(binParentSymlink, "bin"),
			want:      "REPO_VIEW_BIN_DIR must not contain a symlink component",
		},
		{
			name:      "existing bin target",
			cachePath: filepath.Join(root, "safe cache two"),
			binPath:   existingBin,
			want:      "REPO_VIEW_BIN_DIR target must not already exist",
		},
	} {
		t.Run(current.name, func(t *testing.T) {
			command := exec.Command("bash", wrapper, "exec", "--json", "ignored")
			command.Env = append(os.Environ(),
				"REPO_VIEW_CACHE_DIR="+current.cachePath,
				"REPO_VIEW_BIN_DIR="+current.binPath,
				"REPO_VIEW_NAVIGATION_COMMAND_CAP=0",
				"REPO_VIEW_REQUIRED_ROOT=",
				"REPO_VIEW_REQUIRED_BASE_COMMIT=",
				"REPO_VIEW_REQUIRED_CHANGED_RETURN=",
				"REPO_VIEW_REQUIRED_CHANGED_CONTEXT=",
				"REPO_VIEW_REQUIRE_NAVIGATION_SEMANTICS=",
			)
			output, err := command.CombinedOutput()
			if err == nil {
				t.Fatalf("wrapper accepted unsafe path:\n%s", output)
			}
			if !strings.Contains(string(output), current.want) {
				t.Fatalf("wrapper output lacks %q:\n%s", current.want, output)
			}
		})
	}
}

func runGitCommand(t *testing.T, directory string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = directory
	command.Env = append(
		os.Environ(),
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+filepath.Join(t.TempDir(), "empty-git-config"),
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
	return string(output)
}
