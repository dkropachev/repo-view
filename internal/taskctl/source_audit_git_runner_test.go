package taskctl

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSourceAuditGitRunnerAuthenticatesExactPathAndDigest(t *testing.T) {
	git := sourceAuditTestGitRunner(t)
	if _, err := newSourceAuditGitRunner(sourceAuditGitIdentity{
		executable: git.identity.executable,
		sha256:     strings.Repeat("0", 64),
	}); err == nil || !strings.Contains(err.Error(), "want") {
		t.Fatalf("wrong Git digest error = %v", err)
	}

	symlink := filepath.Join(t.TempDir(), "git")
	if err := os.Symlink(git.identity.executable, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := newSourceAuditGitRunner(sourceAuditGitIdentity{
		executable: symlink,
		sha256:     git.identity.sha256,
	}); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlinked Git path error = %v", err)
	}
}

func TestSourceAuditGitRunnerRejectsHardLinkedExecutable(t *testing.T) {
	git := sourceAuditTestGitRunner(t)
	content, err := os.ReadFile(git.identity.executable)
	if err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	copyPath := filepath.Join(directory, "git-copy")
	if err := os.WriteFile(copyPath, content, 0o700); err != nil {
		t.Fatal(err)
	}
	aliasPath := filepath.Join(directory, "git-alias")
	if err := os.Link(copyPath, aliasPath); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	digest, err := hashSourceAuditGitFile(file, info.Size())
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		t.Fatal(errors.Join(err, closeErr))
	}
	if _, err := newSourceAuditGitRunner(sourceAuditGitIdentity{
		executable: copyPath,
		sha256:     digest,
	}); err == nil || !strings.Contains(err.Error(), "exactly one filesystem link") {
		t.Fatalf("hard-linked Git error = %v", err)
	}
}

func TestSourceAuditGitRunnerExecutesClosedQuery(t *testing.T) {
	git := sourceAuditTestGitRunner(t)
	repository := filepath.Join(t.TempDir(), "repository")
	runSourceAuditTestGit(t, "", nil, "init", "-q", repository)
	result, err := git.execute(
		context.Background(),
		repository,
		sourceAuditTestRepositoryInfo(t, repository),
		nil,
		"rev-parse", "--show-toplevel",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.success || string(result.stdout) != repository+"\n" {
		t.Fatalf("authenticated Git result = success:%t stdout:%q stderr:%q", result.success, result.stdout, result.stderr)
	}
}

func TestSourceAuditGitRunnerRejectsRepositoryReplacementAfterPin(t *testing.T) {
	git := sourceAuditTestGitRunner(t)
	base := t.TempDir()
	repository := filepath.Join(base, "repository")
	retained := filepath.Join(base, "retained")
	runSourceAuditTestGit(t, "", nil, "init", "-q", repository)
	expected := sourceAuditTestRepositoryInfo(t, repository)
	marker := filepath.Join(repository, "replacement-marker")

	sourceAuditRepositoryPinHook = func(stage, path string) error {
		if stage != "after-open" || path != repository {
			return nil
		}
		if err := os.Rename(repository, retained); err != nil {
			return err
		}
		if err := os.Mkdir(repository, 0o700); err != nil {
			return err
		}
		return os.WriteFile(marker, []byte("replacement\n"), 0o600)
	}
	t.Cleanup(func() { sourceAuditRepositoryPinHook = nil })

	_, err := git.execute(
		context.Background(), repository, expected, nil,
		"rev-parse", "--show-toplevel",
	)
	if err == nil || !strings.Contains(err.Error(), "admitted identity") {
		t.Fatalf("repository replacement error = %v", err)
	}
	content, readErr := os.ReadFile(marker)
	if readErr != nil || string(content) != "replacement\n" {
		t.Fatalf("replacement marker changed: content=%q error=%v", content, readErr)
	}
}

func TestSourceAuditGitRunnerDisablesPartialCloneFetch(t *testing.T) {
	git := sourceAuditTestGitRunner(t)
	repository := filepath.Join(t.TempDir(), "repository")
	runSourceAuditTestGit(t, "", nil, "init", "-q", repository)
	runSourceAuditTestGit(t, repository, nil, "remote", "add", "origin", "https://example.invalid/repository.git")
	runSourceAuditTestGit(t, repository, nil, "config", "remote.origin.promisor", "true")
	runSourceAuditTestGit(t, repository, nil, "config", "remote.origin.partialclonefilter", "blob:none")
	marker := filepath.Join(t.TempDir(), "helper-ran")
	runSourceAuditTestGit(t, repository, nil, "config", "remote.origin.url", "ext::touch "+marker)
	result, err := git.execute(
		context.Background(),
		repository,
		sourceAuditTestRepositoryInfo(t, repository),
		nil,
		"cat-file", "-e", strings.Repeat("f", 40)+"^{commit}",
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.success {
		t.Fatal("missing promised object unexpectedly exists")
	}
	if _, err := os.Lstat(marker); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial clone launched remote helper: %v", err)
	}
}
