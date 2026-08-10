package source

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestVerifyCleanStandaloneSource(t *testing.T) {
	t.Parallel()
	repository := newRepository(t)
	digest, err := TreeDigest(context.Background(), repository.root)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := Verify(context.Background(), expectedSource(
		t, repository.root, repository.head, repository.base, digest,
	))
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Root != repository.root || snapshot.Revision != repository.head ||
		snapshot.Base != repository.base || snapshot.TreeSHA256 != digest {
		t.Fatalf("unexpected snapshot: %+v", snapshot)
	}
	if !filepath.IsAbs(snapshot.GitExecutable) {
		t.Fatalf("Git executable is not absolute: %q", snapshot.GitExecutable)
	}
	gitContent, _, err := readStableRegular(snapshot.GitExecutable)
	if err != nil {
		t.Fatal(err)
	}
	gitDigest := sha256.Sum256(gitContent)
	if snapshot.GitExecutableSHA256 != hex.EncodeToString(gitDigest[:]) {
		t.Fatalf(
			"unexpected Git executable digest: got %s",
			snapshot.GitExecutableSHA256,
		)
	}
	metadataDigest, err := stableGitMetadataDigest(repository.root)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.GitMetadataSHA256 != metadataDigest {
		t.Fatalf(
			"unexpected Git metadata digest: got %s, want %s",
			snapshot.GitMetadataSHA256,
			metadataDigest,
		)
	}
}

func TestVerifyRejectsDirtySource(t *testing.T) {
	t.Parallel()
	repository := newRepository(t)
	digest, err := TreeDigest(context.Background(), repository.root)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repository.root, "untracked.txt"), "dirty")
	_, err = Verify(context.Background(), expectedSource(
		t, repository.root, repository.head, repository.base, digest,
	))
	if err == nil || !strings.Contains(err.Error(), "dirty") {
		t.Fatalf("expected dirty-source error, got %v", err)
	}
}

func TestVerifyRejectsIgnoredFiles(t *testing.T) {
	t.Parallel()
	repository := newRepository(t)
	writeFile(t, filepath.Join(repository.root, ".gitignore"), "ignored.log\n")
	runGit(t, repository.root, "add", ".gitignore")
	commit(t, repository.root, "ignore rule")
	repository.head = strings.TrimSpace(git(t, repository.root, "rev-parse", "HEAD"))
	digest, err := TreeDigest(context.Background(), repository.root)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repository.root, "ignored.log"), "model-visible\n")
	_, err = Verify(context.Background(), expectedSource(
		t, repository.root, repository.head, repository.base, digest,
	))
	if err == nil || !strings.Contains(err.Error(), "ignored") {
		t.Fatalf("expected ignored-file error, got %v", err)
	}
}

func TestTreeDigestRejectsUntrackedEmptyDirectory(t *testing.T) {
	t.Parallel()
	repository := newRepository(t)
	if err := os.Mkdir(filepath.Join(repository.root, "model-visible-empty"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := TreeDigest(context.Background(), repository.root); err == nil ||
		!strings.Contains(err.Error(), "untracked directory") {
		t.Fatalf("expected untracked-directory error, got %v", err)
	}
}

func TestGitEnvironmentIsCodeOwned(t *testing.T) {
	t.Setenv("LD_PRELOAD", "/tmp/answer-key.so")
	t.Setenv("HOME", "/tmp/answer-key-home")
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("PATH", "/tmp/answer-key-path")
	joined := "\n" + strings.Join(gitEnvironment(), "\n") + "\n"
	for _, forbidden := range []string{
		"LD_PRELOAD=",
		"HOME=",
		"GIT_CONFIG_COUNT=",
		"PATH=",
	} {
		if strings.Contains(joined, "\n"+forbidden) {
			t.Fatalf("ambient variable %q escaped into Git environment", forbidden)
		}
	}
	for _, required := range []string{
		"GIT_CONFIG_GLOBAL=",
		"GIT_CONFIG_SYSTEM=",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_OPTIONAL_LOCKS=0",
		"LC_ALL=C",
	} {
		if !strings.Contains(joined, "\n"+required) {
			t.Fatalf("code-owned Git environment is missing %q", required)
		}
	}
}

func TestVerifyRejectsObjectAlternates(t *testing.T) {
	t.Parallel()
	repository := newRepository(t)
	digest, err := TreeDigest(context.Background(), repository.root)
	if err != nil {
		t.Fatal(err)
	}
	alternates := filepath.Join(repository.root, ".git", "objects", "info", "alternates")
	writeFile(t, alternates, "/external/object/store\n")
	_, err = Verify(context.Background(), expectedSource(
		t, repository.root, repository.head, repository.base, digest,
	))
	if err == nil || !strings.Contains(err.Error(), "alternates") {
		t.Fatalf("expected alternates error, got %v", err)
	}
}

func TestVerifyRejectsHardLinkedObjectStore(t *testing.T) {
	t.Parallel()
	repository := newRepository(t)
	digest, err := TreeDigest(context.Background(), repository.root)
	if err != nil {
		t.Fatal(err)
	}
	object := filepath.Join(
		repository.root,
		".git", "objects", repository.head[:2], repository.head[2:],
	)
	outside := filepath.Join(t.TempDir(), "shared-object")
	if err := os.Link(object, outside); err != nil {
		t.Fatal(err)
	}
	_, err = Verify(context.Background(), expectedSource(
		t, repository.root, repository.head, repository.base, digest,
	))
	if err == nil || !strings.Contains(err.Error(), "hard-linked") {
		t.Fatalf("expected hard-link error, got %v", err)
	}
}

func TestVerifyRejectsLinkedWorktree(t *testing.T) {
	t.Parallel()
	repository := newRepository(t)
	linked := filepath.Join(t.TempDir(), "linked")
	runGit(t, repository.root, "worktree", "add", "--detach", linked, repository.head)
	_, err := Verify(context.Background(), expectedSource(
		t, linked, repository.head, repository.base, strings.Repeat("0", 64),
	))
	if err == nil || !strings.Contains(err.Error(), "standalone") {
		t.Fatalf("expected linked-worktree error, got %v", err)
	}
}

func TestVerifyRejectsAssumeUnchangedIndexFlag(t *testing.T) {
	t.Parallel()
	repository := newRepository(t)
	digest, err := TreeDigest(context.Background(), repository.root)
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, repository.root, "update-index", "--assume-unchanged", "file.txt")
	writeFile(t, filepath.Join(repository.root, "file.txt"), "hidden change\n")
	_, err = Verify(context.Background(), expectedSource(
		t, repository.root, repository.head, repository.base, digest,
	))
	if err == nil || !strings.Contains(err.Error(), "index flag") {
		t.Fatalf("expected unsafe index-flag error, got %v", err)
	}
}

func TestVerifyRejectsSkipWorktreeIndexFlag(t *testing.T) {
	t.Parallel()
	repository := newRepository(t)
	digest, err := TreeDigest(context.Background(), repository.root)
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, repository.root, "update-index", "--skip-worktree", "file.txt")
	writeFile(t, filepath.Join(repository.root, "file.txt"), "hidden change\n")
	_, err = Verify(context.Background(), expectedSource(
		t, repository.root, repository.head, repository.base, digest,
	))
	if err == nil || !strings.Contains(err.Error(), "index flag") {
		t.Fatalf("expected unsafe index-flag error, got %v", err)
	}
}

func TestVerifyRejectsCommonDirectoryFile(t *testing.T) {
	t.Parallel()
	repository := newRepository(t)
	digest, err := TreeDigest(context.Background(), repository.root)
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(repository.root, ".git", "commondir"), "../external\n")
	_, err = Verify(context.Background(), expectedSource(
		t, repository.root, repository.head, repository.base, digest,
	))
	if err == nil || !strings.Contains(err.Error(), "commondir") {
		t.Fatalf("expected commondir error, got %v", err)
	}
}

func TestVerifyRejectsExternalObjectDirectorySymlink(t *testing.T) {
	t.Parallel()
	repository := newRepository(t)
	digest, err := TreeDigest(context.Background(), repository.root)
	if err != nil {
		t.Fatal(err)
	}
	objects := filepath.Join(repository.root, ".git", "objects")
	externalObjects := filepath.Join(t.TempDir(), "external-objects")
	if err := os.Rename(objects, externalObjects); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(externalObjects, objects); err != nil {
		t.Fatal(err)
	}
	_, err = Verify(context.Background(), expectedSource(
		t, repository.root, repository.head, repository.base, digest,
	))
	if err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected external-object-directory error, got %v", err)
	}
}

func TestTreeDigestRejectsHardLinkedTrackedFile(t *testing.T) {
	t.Parallel()
	repository := newRepository(t)
	outside := filepath.Join(t.TempDir(), "shared-worktree-file")
	if err := os.Link(filepath.Join(repository.root, "file.txt"), outside); err != nil {
		t.Fatal(err)
	}
	if _, err := TreeDigest(context.Background(), repository.root); err == nil ||
		!strings.Contains(err.Error(), "hard-linked") {
		t.Fatalf("expected tracked-file hard-link error, got %v", err)
	}
}

func TestVerifyRejectsHardLinkedGitMetadata(t *testing.T) {
	t.Parallel()
	repository := newRepository(t)
	digest, err := TreeDigest(context.Background(), repository.root)
	if err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "shared-index")
	if err := os.Link(filepath.Join(repository.root, ".git", "index"), outside); err != nil {
		t.Fatal(err)
	}
	_, err = Verify(context.Background(), expectedSource(
		t, repository.root, repository.head, repository.base, digest,
	))
	if err == nil || !strings.Contains(err.Error(), "hard-linked") {
		t.Fatalf("expected Git-metadata hard-link error, got %v", err)
	}
}

func TestGitRunnerIgnoresPATHChangesAfterResolution(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses POSIX executable and symlink semantics")
	}
	repository := newRepository(t)
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	realGit, err = filepath.Abs(realGit)
	if err != nil {
		t.Fatal(err)
	}
	initialPath := t.TempDir()
	if err := os.Symlink(realGit, filepath.Join(initialPath, "git")); err != nil {
		t.Fatal(err)
	}
	replacementPath := t.TempDir()
	fakeGit := filepath.Join(replacementPath, "git")
	writeFile(t, fakeGit, "#!/bin/sh\nexit 97\n")
	if err := os.Chmod(fakeGit, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", initialPath)
	runner, err := resolveGitRunner()
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", replacementPath)
	output, err := runner.output(
		context.Background(),
		repository.root,
		"rev-parse", "HEAD",
	)
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(output) != repository.head {
		t.Fatalf("unexpected pinned-Git output %q", output)
	}
}

func TestGitRunnerRejectsExecutableMutation(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses POSIX executable semantics")
	}
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(realGit)
	if err != nil {
		t.Fatal(err)
	}
	pathDirectory := t.TempDir()
	gitCopy := filepath.Join(pathDirectory, "git")
	if err := os.WriteFile(gitCopy, content, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", pathDirectory)
	runner, err := resolveGitRunner()
	if err != nil {
		t.Fatal(err)
	}
	content[len(content)/2] ^= 0xff
	if err := os.WriteFile(gitCopy, content, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := runner.verify(); err == nil || !strings.Contains(err.Error(), "content changed") {
		t.Fatalf("expected pinned executable mutation error, got %v", err)
	}
}

func TestVerifyRejectsWrongExpectedGitIdentity(t *testing.T) {
	repository := newRepository(t)
	digest, err := TreeDigest(context.Background(), repository.root)
	if err != nil {
		t.Fatal(err)
	}
	expected := expectedSource(
		t,
		repository.root,
		repository.head,
		repository.base,
		digest,
	)
	expected.GitExecutableSHA256 = strings.Repeat("0", 64)
	if _, err := Verify(context.Background(), expected); err == nil ||
		!strings.Contains(err.Error(), "identity mismatch") {
		t.Fatalf("wrong expected Git digest was accepted: %v", err)
	}
}

func TestVerifyIgnoresPATHAfterExpectedGitIsPinned(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test uses a POSIX fake executable")
	}
	repository := newRepository(t)
	digest, err := TreeDigest(context.Background(), repository.root)
	if err != nil {
		t.Fatal(err)
	}
	expected := expectedSource(
		t,
		repository.root,
		repository.head,
		repository.base,
		digest,
	)
	fakeDirectory := t.TempDir()
	fakeGit := filepath.Join(fakeDirectory, "git")
	writeFile(t, fakeGit, "#!/bin/sh\nexit 97\n")
	if err := os.Chmod(fakeGit, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", fakeDirectory)
	if _, err := Verify(context.Background(), expected); err != nil {
		t.Fatalf("ambient PATH redirected pinned Git: %v", err)
	}
}

func TestGitMetadataDigestBindsRefsConfigAndObjects(t *testing.T) {
	t.Parallel()
	tests := map[string]func(*testing.T, testRepository){
		"config": func(t *testing.T, repository testRepository) {
			t.Helper()
			path := filepath.Join(repository.root, ".git", "config")
			file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := file.WriteString("\n[tokenbench]\n\tchanged = true\n"); err != nil {
				_ = file.Close()
				t.Fatal(err)
			}
			if err := file.Close(); err != nil {
				t.Fatal(err)
			}
		},
		"object": func(t *testing.T, repository testRepository) {
			t.Helper()
			writeFile(
				t,
				filepath.Join(
					repository.root,
					".git", "objects", "aa", strings.Repeat("b", 38),
				),
				"additional object bytes",
			)
		},
		"ref": func(t *testing.T, repository testRepository) {
			t.Helper()
			writeFile(
				t,
				filepath.Join(repository.root, ".git", "refs", "heads", "extra"),
				repository.head+"\n",
			)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			repository := newRepository(t)
			before, err := gitMetadataDigest(repository.root)
			if err != nil {
				t.Fatal(err)
			}
			mutate(t, repository)
			after, err := gitMetadataDigest(repository.root)
			if err != nil {
				t.Fatal(err)
			}
			if before == after {
				t.Fatalf("Git metadata digest did not bind additional %s", name)
			}
		})
	}
}

func TestStableGitMetadataDigestRejectsChangeBetweenPasses(t *testing.T) {
	t.Parallel()
	repository := newRepository(t)
	_, err := stableGitMetadataDigestWithHook(repository.root, func() error {
		path := filepath.Join(repository.root, ".git", "config")
		file, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0)
		if err != nil {
			return err
		}
		if _, err := file.WriteString("\n[tokenbench-race]\n\tchanged = true\n"); err != nil {
			_ = file.Close()
			return err
		}
		return file.Close()
	})
	if err == nil || !strings.Contains(err.Error(), "changed while") {
		t.Fatalf("expected racing Git metadata error, got %v", err)
	}
}

func TestGitMetadataDigestRejectsTransientState(t *testing.T) {
	t.Parallel()
	repository := newRepository(t)
	writeFile(t, filepath.Join(repository.root, ".git", "index.lock"), "locked")
	if _, err := gitMetadataDigest(repository.root); err == nil ||
		!strings.Contains(err.Error(), "transient") {
		t.Fatalf("expected transient-metadata error, got %v", err)
	}
}

func TestTreeDigestRejectsTrackedSymlink(t *testing.T) {
	t.Parallel()
	repository := newRepository(t)
	link := filepath.Join(repository.root, "link")
	if err := os.Symlink("file.txt", link); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository.root, "add", "link")
	if _, err := TreeDigest(context.Background(), repository.root); err == nil ||
		!strings.Contains(err.Error(), "symlink") {
		t.Fatalf("expected symlink error, got %v", err)
	}
}

func TestTreeDigestRejectsContentDriftFromIndex(t *testing.T) {
	t.Parallel()
	repository := newRepository(t)
	path := filepath.Join(repository.root, "file.txt")
	writeFile(t, path, "different content\n")
	if _, err := TreeDigest(context.Background(), repository.root); err == nil ||
		!strings.Contains(err.Error(), "indexed blob") {
		t.Fatalf("expected indexed-blob mismatch, got %v", err)
	}
}

func TestTreeDigestRejectsIndexDriftFromHEAD(t *testing.T) {
	t.Parallel()
	repository := newRepository(t)
	writeFile(t, filepath.Join(repository.root, "file.txt"), "staged change\n")
	runGit(t, repository.root, "add", "file.txt")
	if _, err := TreeDigest(context.Background(), repository.root); err == nil ||
		!strings.Contains(err.Error(), "does not match HEAD") {
		t.Fatalf("expected HEAD mismatch, got %v", err)
	}
}

func TestTreeDigestSupportsSHA256Repository(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "sha256-repository")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("git", "-C", root, "init", "--quiet", "--object-format=sha256")
	command.Env = append(
		os.Environ(),
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_SYSTEM="+os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Skipf("installed Git does not support SHA-256 repositories: %v: %s", err, output)
	}
	writeFile(t, filepath.Join(root, "file.txt"), "sha256 content\n")
	runGit(t, root, "add", "file.txt")
	commit(t, root, "sha256 source")
	if _, err := TreeDigest(context.Background(), root); err != nil {
		t.Fatal(err)
	}
}

func TestTreeDigestRejectsIndexFilesystemModeDrift(t *testing.T) {
	t.Parallel()
	repository := newRepository(t)
	path := filepath.Join(repository.root, "file.txt")
	if err := os.Chmod(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := TreeDigest(context.Background(), repository.root); err == nil ||
		!strings.Contains(err.Error(), "executable mode") {
		t.Fatalf("expected executable-mode error, got %v", err)
	}
}

func TestVerifyRejectsReplacementObjects(t *testing.T) {
	t.Parallel()
	repository := newRepository(t)
	digest, err := TreeDigest(context.Background(), repository.root)
	if err != nil {
		t.Fatal(err)
	}
	runGit(t, repository.root, "replace", repository.base, repository.head)
	_, err = Verify(context.Background(), expectedSource(
		t, repository.root, repository.head, repository.base, digest,
	))
	if err == nil || !strings.Contains(err.Error(), "replacement") {
		t.Fatalf("expected replacement-object error, got %v", err)
	}
}

func TestVerifyRejectsUnallowlistedLocalConfiguration(t *testing.T) {
	t.Parallel()
	repository := newRepository(t)
	digest, err := TreeDigest(context.Background(), repository.root)
	if err != nil {
		t.Fatal(err)
	}
	runGit(
		t,
		repository.root,
		"config",
		"remote.origin.url",
		"https://credential@example.invalid/repository.git",
	)

	_, err = Verify(context.Background(), expectedSource(
		t, repository.root, repository.head, repository.base, digest,
	))
	if err == nil || !strings.Contains(err.Error(), "unsafe local Git configuration") {
		t.Fatalf("expected unsafe-config error, got %v", err)
	}
}

type testRepository struct {
	root string
	head string
	base string
}

func expectedSource(
	t *testing.T,
	root, revision, base, treeSHA256 string,
) Expected {
	t.Helper()
	git, err := resolveGitRunner()
	if err != nil {
		t.Fatal(err)
	}
	return Expected{
		Root:                root,
		Revision:            revision,
		Base:                base,
		TreeSHA256:          treeSHA256,
		GitExecutable:       git.path,
		GitExecutableSHA256: git.sha256,
	}
}

func newRepository(t *testing.T) testRepository {
	t.Helper()
	root := filepath.Join(t.TempDir(), "repository")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "init", "--quiet")
	writeFile(t, filepath.Join(root, "file.txt"), "base\n")
	runGit(t, root, "add", "file.txt")
	commit(t, root, "base")
	base := strings.TrimSpace(git(t, root, "rev-parse", "HEAD"))
	writeFile(t, filepath.Join(root, "file.txt"), "head\n")
	runGit(t, root, "add", "file.txt")
	commit(t, root, "head")
	head := strings.TrimSpace(git(t, root, "rev-parse", "HEAD"))
	return testRepository{root: root, head: head, base: base}
}

func commit(t *testing.T, root, message string) {
	t.Helper()
	runGit(
		t,
		root,
		"-c", "user.name=Tokenbench Test",
		"-c", "user.email=tokenbench@example.invalid",
		"commit", "--quiet", "-m", message,
	)
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func git(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	command.Env = append(
		os.Environ(),
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_SYSTEM="+os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
	return string(output)
}

func runGit(t *testing.T, root string, arguments ...string) {
	t.Helper()
	git(t, root, arguments...)
}
