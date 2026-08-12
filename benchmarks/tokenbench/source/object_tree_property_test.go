package source

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestObjectTreePropertyEmptyTreeMatchesProduction(t *testing.T) {
	root := newObjectTreePropertyRepository(t, "sha1")
	commitEmptyObjectTreeProperty(t, root, "empty tree")
	setObjectTreePropertyDirectoryMode(t, 0o710, root)

	_, blobReads := compareObjectTreePropertyToProduction(
		t,
		root,
		"sha1",
		0o710,
	)
	if blobReads != 0 {
		t.Fatalf("empty tree triggered %d blob reads", blobReads)
	}
}

func TestObjectTreePropertyNestedGitlinkOnlyAbsentAndEmptyMatchProduction(
	t *testing.T,
) {
	root := newObjectTreePropertyRepository(t, "sha1")
	commitEmptyObjectTreeProperty(t, root, "gitlink target")
	target := strings.TrimSpace(git(t, root, "rev-parse", "HEAD"))
	const gitlink = "vendor/nested/dependency"
	runGit(
		t,
		root,
		"update-index",
		"--add",
		"--cacheinfo",
		gitlinkMode+","+target+","+gitlink,
	)
	commit(t, root, "nested opaque gitlink")
	setObjectTreePropertyDirectoryMode(t, 0o750, root)

	absentDigest, blobReads := compareObjectTreePropertyToProduction(
		t,
		root,
		"sha1",
		0o750,
	)
	if blobReads != 0 {
		t.Fatalf("gitlink-only tree triggered %d blob reads", blobReads)
	}

	gitlinkPath := filepath.Join(root, filepath.FromSlash(gitlink))
	if err := os.MkdirAll(gitlinkPath, 0o711); err != nil {
		t.Fatal(err)
	}
	materializedDigest, blobReads := compareObjectTreePropertyToProduction(
		t,
		root,
		"sha1",
		0o750,
	)
	if blobReads != 0 {
		t.Fatalf("materialized gitlink-only tree triggered %d blob reads", blobReads)
	}
	if materializedDigest != absentDigest {
		t.Fatalf(
			"empty nested gitlink changed digest: absent %s, materialized %s",
			absentDigest,
			materializedDigest,
		)
	}
}

func TestObjectTreePropertyGitDirectoryOrderingMatchesProduction(t *testing.T) {
	root := newObjectTreePropertyRepository(t, "sha1")
	writeFile(t, filepath.Join(root, "foo", "child"), "directory child\n")
	writeFile(t, filepath.Join(root, "foo.bar"), "adjacent file\n")
	runGit(t, root, "add", "foo/child", "foo.bar")
	commit(t, root, "Git directory ordering edge")
	setObjectTreePropertyDirectoryMode(t, 0o755, root, filepath.Join(root, "foo"))

	names := bytes.Split(
		[]byte(git(t, root, "ls-tree", "--name-only", "-z", "HEAD")),
		[]byte{0},
	)
	if len(names) < 3 || string(names[0]) != "foo.bar" || string(names[1]) != "foo" {
		t.Fatalf("top-level Git tree order = %q, want [foo.bar foo]", names)
	}
	compareObjectTreePropertyToProduction(t, root, "sha1", 0o755)
}

func TestObjectTreePropertyAlternateDirectoryModesMatchProduction(t *testing.T) {
	root := newObjectTreePropertyRepository(t, "sha1")
	directory := filepath.Join(root, "nested")
	deepDirectory := filepath.Join(directory, "deeper")
	writeFile(t, filepath.Join(deepDirectory, "file.txt"), "directory modes\n")
	runGit(t, root, "add", "nested/deeper/file.txt")
	commit(t, root, "alternate directory modes")

	digests := make(map[string]os.FileMode)
	for _, mode := range []os.FileMode{0o700, 0o711, 0o750, 0o775} {
		t.Run(fmt.Sprintf("%#o", mode), func(t *testing.T) {
			setObjectTreePropertyDirectoryMode(
				t,
				mode,
				root,
				directory,
				deepDirectory,
			)
			digest, _ := compareObjectTreePropertyToProduction(
				t,
				root,
				"sha1",
				mode,
			)
			if previous, exists := digests[digest]; exists {
				t.Fatalf(
					"directory modes %#o and %#o produced the same digest %s",
					previous,
					mode,
					digest,
				)
			}
			digests[digest] = mode
		})
	}
}

func TestObjectTreePropertySHA256NestedExecutableAndGitlinkMatchProduction(
	t *testing.T,
) {
	root := newObjectTreePropertyRepository(t, "sha256")
	commitEmptyObjectTreeProperty(t, root, "SHA-256 gitlink target")
	target := strings.TrimSpace(git(t, root, "rev-parse", "HEAD"))
	executableDirectory := filepath.Join(root, "src", "deep")
	executable := filepath.Join(executableDirectory, "run")
	writeFile(t, executable, "#!/bin/sh\nexit 0\n")
	if err := os.Chmod(executable, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "src/deep/run")
	runGit(
		t,
		root,
		"update-index",
		"--add",
		"--cacheinfo",
		gitlinkMode+","+target+",vendor/deep/module",
	)
	commit(t, root, "SHA-256 executable and gitlink")
	setObjectTreePropertyDirectoryMode(
		t,
		0o755,
		root,
		filepath.Join(root, "src"),
		executableDirectory,
	)

	_, entries := testGitObjectTree(t, root)
	var executableSeen, gitlinkSeen bool
	for _, entry := range entries {
		switch {
		case entry.Path == "src/deep/run" && entry.Mode == "100755":
			executableSeen = len(entry.ObjectID) == 64
		case entry.Path == "vendor/deep/module" && entry.Mode == gitlinkMode:
			gitlinkSeen = len(entry.ObjectID) == 64
		}
	}
	if !executableSeen || !gitlinkSeen {
		t.Fatalf(
			"SHA-256 tree lacks expected executable/gitlink entries: %+v",
			entries,
		)
	}
	compareObjectTreePropertyToProduction(t, root, "sha256", 0o755)
}

func TestObjectTreePropertyCancellation(t *testing.T) {
	t.Run("before reconstruction", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		blobReads := 0
		_, err := TreeDigestFromGitObjects(
			ctx,
			"sha1",
			strings.Repeat("0", 40),
			0o755,
			nil,
			func(context.Context, string, io.Writer) error {
				blobReads++
				return nil
			},
		)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("canceled reconstruction error = %v, want context.Canceled", err)
		}
		if blobReads != 0 {
			t.Fatalf("pre-canceled digest triggered %d blob reads", blobReads)
		}
	})

	t.Run("during blob read", func(t *testing.T) {
		root := newObjectTreePropertyRepository(t, "sha1")
		writeFile(t, filepath.Join(root, "file.txt"), "cancel me\n")
		runGit(t, root, "add", "file.txt")
		commit(t, root, "cancel object read")
		setObjectTreePropertyDirectoryMode(t, 0o700, root)
		rootTreeID, entries := testGitObjectTree(t, root)
		ctx, cancel := context.WithCancel(context.Background())
		blobReads := 0
		_, err := TreeDigestFromGitObjects(
			ctx,
			"sha1",
			rootTreeID,
			0o700,
			entries,
			func(_ context.Context, _ string, destination io.Writer) error {
				blobReads++
				cancel()
				_, writeErr := io.WriteString(destination, "cancel me\n")
				return writeErr
			},
		)
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("mid-read cancellation error = %v, want context.Canceled", err)
		}
		if blobReads != 1 {
			t.Fatalf("mid-read cancellation triggered %d blob reads, want 1", blobReads)
		}
	})
}

func TestObjectTreePropertyCoreBounds(t *testing.T) {
	t.Run("tracked entry count", func(t *testing.T) {
		entries := make([]GitTreeEntry, maximumTrackedEntries+1)
		_, err := TreeDigestFromGitObjects(
			context.Background(),
			"sha1",
			strings.Repeat("0", 40),
			0o755,
			entries,
			func(context.Context, string, io.Writer) error { return nil },
		)
		if err == nil || !strings.Contains(
			err.Error(),
			fmt.Sprintf("exceeds %d tracked entries", maximumTrackedEntries),
		) {
			t.Fatalf("entry-count error = %v, want configured bound", err)
		}
	})

	t.Run("regular blob bytes", func(t *testing.T) {
		root := newObjectTreePropertyRepository(t, "sha1")
		writeFile(t, filepath.Join(root, "file.txt"), "bounded\n")
		runGit(t, root, "add", "file.txt")
		commit(t, root, "bounded blob")
		rootTreeID, entries := testGitObjectTree(t, root)
		oversized := make([]byte, maximumRegularFileBytes+1)
		_, err := TreeDigestFromGitObjects(
			context.Background(),
			"sha1",
			rootTreeID,
			0o700,
			entries,
			func(_ context.Context, _ string, destination io.Writer) error {
				_, writeErr := destination.Write(oversized)
				return writeErr
			},
		)
		if err == nil || !strings.Contains(
			err.Error(),
			fmt.Sprintf("blob exceeds %d bytes", maximumRegularFileBytes),
		) {
			t.Fatalf("oversized-blob error = %v, want configured bound", err)
		}
	})

	t.Run("directory special bits", func(t *testing.T) {
		_, err := TreeDigestFromGitObjects(
			context.Background(),
			"sha1",
			strings.Repeat("0", 40),
			os.ModeSetuid|0o755,
			nil,
			func(context.Context, string, io.Writer) error { return nil },
		)
		if err == nil || !strings.Contains(err.Error(), "only permission bits") {
			t.Fatalf("special-directory-mode error = %v, want rejection", err)
		}
	})
}

func compareObjectTreePropertyToProduction(
	t *testing.T,
	root, objectFormat string,
	directoryMode os.FileMode,
) (string, int) {
	t.Helper()
	want, err := TreeDigest(context.Background(), root)
	if err != nil {
		t.Fatalf("production TreeDigest: %v", err)
	}
	rootTreeID, entries := testGitObjectTree(t, root)
	for left, right := 0, len(entries)-1; left < right; left, right = left+1, right-1 {
		entries[left], entries[right] = entries[right], entries[left]
	}
	blobReads := 0
	got, err := TreeDigestFromGitObjects(
		context.Background(),
		objectFormat,
		rootTreeID,
		directoryMode,
		entries,
		func(ctx context.Context, objectID string, destination io.Writer) error {
			blobReads++
			return testReadGitBlob(ctx, root, objectID, destination)
		},
	)
	if err != nil {
		t.Fatalf("TreeDigestFromGitObjects: %v", err)
	}
	if got != want {
		t.Fatalf("object digest = %s, production worktree digest = %s", got, want)
	}
	return got, blobReads
}

func newObjectTreePropertyRepository(t *testing.T, objectFormat string) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "repository")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	arguments := []string{"-C", root, "init", "--quiet"}
	if objectFormat != "sha1" {
		arguments = append(arguments, "--object-format="+objectFormat)
	}
	command := exec.Command("git", arguments...)
	command.Env = append(
		os.Environ(),
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_SYSTEM="+os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
	)
	if output, err := command.CombinedOutput(); err != nil {
		if objectFormat == "sha256" {
			t.Skipf(
				"installed Git does not support SHA-256 repositories: %v: %s",
				err,
				output,
			)
		}
		t.Fatalf("initialize %s Git repository: %v: %s", objectFormat, err, output)
	}
	return root
}

func commitEmptyObjectTreeProperty(t *testing.T, root, message string) {
	t.Helper()
	runGit(
		t,
		root,
		"-c",
		"user.name=Tokenbench Test",
		"-c",
		"user.email=tokenbench@example.invalid",
		"commit",
		"--quiet",
		"--allow-empty",
		"-m",
		message,
	)
}

func setObjectTreePropertyDirectoryMode(
	t *testing.T,
	mode os.FileMode,
	paths ...string,
) {
	t.Helper()
	for _, path := range paths {
		if err := os.Chmod(path, mode); err != nil {
			t.Fatal(err)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != mode.Perm() {
			t.Skipf(
				"filesystem does not preserve directory mode: got %#o, want %#o",
				got,
				mode.Perm(),
			)
		}
	}
}
