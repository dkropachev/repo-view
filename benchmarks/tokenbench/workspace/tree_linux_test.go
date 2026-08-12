//go:build linux

package workspace

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yapless/scopesifter/benchmarks/tokenbench/snapshot"
	"golang.org/x/sys/unix"
)

func TestExpectedWorktreeManifestSelectsSourceAndClearsDirectoryDigest(t *testing.T) {
	t.Parallel()
	root := "/snapshot"
	sourceRoot := filepath.Join(root, "source")
	fileDigest := digest([]byte("content\n"))
	inputs := snapshot.ExecutionInputs{
		SnapshotRoot: root,
		SourceRoot:   sourceRoot,
		Manifest: []snapshot.ManifestEntry{
			{SnapshotPath: root, Kind: snapshot.ManifestKindDirectory, SHA256: digest(nil)},
			{SnapshotPath: sourceRoot, Kind: snapshot.ManifestKindDirectory, Mode: 0o755, SHA256: digest(nil)},
			{SnapshotPath: filepath.Join(sourceRoot, ".git"), Kind: snapshot.ManifestKindDirectory, Mode: 0o700, SHA256: digest(nil)},
			{SnapshotPath: filepath.Join(sourceRoot, ".git", "config"), Kind: snapshot.ManifestKindFile, Mode: 0o444, Size: 3, SHA256: digest([]byte("git"))},
			{SnapshotPath: filepath.Join(sourceRoot, "nested"), Kind: snapshot.ManifestKindDirectory, Mode: 0o755, SHA256: digest(nil)},
			{SnapshotPath: filepath.Join(sourceRoot, "nested", "file.txt"), Kind: snapshot.ManifestKindFile, Mode: 0o644, Size: 8, SHA256: fileDigest},
			{SnapshotPath: filepath.Join(root, "tools", "ignored"), Kind: snapshot.ManifestKindFile, Mode: 0o555, Size: 1, SHA256: digest([]byte("x"))},
		},
	}
	got, err := expectedWorktreeManifest(inputs, validLimits())
	if err != nil {
		t.Fatal(err)
	}
	want := []worktreeEntry{
		{path: ".", kind: snapshot.ManifestKindDirectory, mode: 0o700},
		{path: "nested", kind: snapshot.ManifestKindDirectory, mode: 0o755},
		{path: "nested/file.txt", kind: snapshot.ManifestKindFile, digest: fileDigest, mode: 0o644, size: 8},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("manifest = %#v, want %#v", got, want)
	}
}

func TestExpectedWorktreeManifestRejectsInvalidSourceEntries(t *testing.T) {
	t.Parallel()
	root := "/snapshot/source"
	base := snapshot.ExecutionInputs{
		SourceRoot: root,
		Manifest: []snapshot.ManifestEntry{
			{SnapshotPath: root, Kind: snapshot.ManifestKindDirectory, Mode: 0o755},
		},
	}
	tests := map[string]snapshot.ManifestEntry{
		"duplicate root": {SnapshotPath: root, Kind: snapshot.ManifestKindDirectory, Mode: 0o755},
		"special kind":   {SnapshotPath: filepath.Join(root, "pipe"), Kind: "special", Mode: 0o600},
		"bad file digest": {SnapshotPath: filepath.Join(root, "file"), Kind: snapshot.ManifestKindFile,
			Mode: 0o644, Size: 1, SHA256: "bad"},
		"large file": {SnapshotPath: filepath.Join(root, "file"), Kind: snapshot.ManifestKindFile,
			Mode: 0o644, Size: validLimits().MaximumFileBytes + 1, SHA256: digest(nil)},
		"sized directory": {SnapshotPath: filepath.Join(root, "dir"), Kind: snapshot.ManifestKindDirectory,
			Mode: 0o755, Size: 1},
	}
	for name, entry := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			inputs := base.Clone()
			inputs.Manifest = append(inputs.Manifest, entry)
			if _, err := expectedWorktreeManifest(inputs, validLimits()); err == nil {
				t.Fatal("invalid source manifest entry was accepted")
			}
		})
	}
}

func TestScanWorktreeHashesRegularTreeAndRejectsSpecialEntries(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte("content\n")
	if err := os.WriteFile(filepath.Join(root, "nested", "file.txt"), content, 0o640); err != nil {
		t.Fatal(err)
	}
	directory, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = directory.Close() })
	entries, err := scanWorktree(context.Background(), directory, validLimits())
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 || entries[2].path != "nested/file.txt" ||
		entries[2].digest != digest(content) || entries[2].size != int64(len(content)) {
		t.Fatalf("scanned entries = %#v", entries)
	}

	if err := os.Symlink("nested/file.txt", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	if _, err := scanWorktree(context.Background(), directory, validLimits()); err == nil ||
		!strings.Contains(err.Error(), "special") {
		t.Fatalf("symlink scan = %v, want special-path rejection", err)
	}
}

func TestScanWorktreeRejectsHardLinksAndForbiddenGitMetadata(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	original := filepath.Join(root, "original")
	if err := os.WriteFile(original, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(original, filepath.Join(root, "alias")); err != nil {
		t.Fatal(err)
	}
	directory, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := scanWorktree(context.Background(), directory, validLimits()); err == nil ||
		!strings.Contains(err.Error(), "link") {
		t.Fatalf("hard-link scan = %v", err)
	}
	if err := directory.Close(); err != nil {
		t.Fatal(err)
	}

	root = t.TempDir()
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	directory, err = os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = directory.Close() })
	if _, err := scanWorktree(context.Background(), directory, validLimits()); err == nil ||
		!strings.Contains(err.Error(), "forbidden") {
		t.Fatalf("Git metadata scan = %v", err)
	}
}

func TestRemoveArmLayoutHandlesSymlinksAndSpecialEntries(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	for _, name := range []string{
		worktreeDirectory, upperDirectory, workDirectory, cacheDirectory, captureDirectory,
	} {
		if err := os.Mkdir(filepath.Join(root, name), 0o700); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("nowhere", filepath.Join(root, upperDirectory, "link")); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mkfifo(filepath.Join(root, cacheDirectory, "fifo"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, worktreeDirectory, "nested"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, worktreeDirectory, "nested", "file"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	directory, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = directory.Close() })
	if err := removeArmLayout(directory, testLayoutClaims(t, directory), validLimits()); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("workspace cleanup left %d entries", len(entries))
	}
}

func TestRemoveArmLayoutHandlesInvalidUTF8AndDeepTrees(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	rootDirectory, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rootDirectory.Close() })
	for _, name := range []string{
		worktreeDirectory, upperDirectory, workDirectory, cacheDirectory, captureDirectory,
	} {
		if err := unix.Mkdirat(int(rootDirectory.Fd()), name, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	cache, err := openDirectoryAt(rootDirectory, cacheDirectory)
	if err != nil {
		t.Fatal(err)
	}
	invalidName := string([]byte{'i', 0xff})
	descriptor, err := unix.Openat(
		int(cache.Fd()),
		invalidName,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC,
		0o600,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Close(descriptor); err != nil {
		t.Fatal(err)
	}
	if err := cache.Close(); err != nil {
		t.Fatal(err)
	}

	current, err := openDirectoryAt(rootDirectory, worktreeDirectory)
	if err != nil {
		t.Fatal(err)
	}
	for range maximumWorkspacePathDepth + 32 {
		if err := unix.Mkdirat(int(current.Fd()), "d", 0o700); err != nil {
			t.Fatal(err)
		}
		next, err := openDirectoryAt(current, "d")
		if err != nil {
			t.Fatal(err)
		}
		if err := current.Close(); err != nil {
			t.Fatal(err)
		}
		current = next
	}
	if err := current.Close(); err != nil {
		t.Fatal(err)
	}

	if err := removeArmLayout(rootDirectory, testLayoutClaims(t, rootDirectory), validLimits()); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("workspace cleanup left %d entries", len(entries))
	}
}

func testLayoutClaims(t *testing.T, root *os.File) []directoryClaim {
	t.Helper()
	claims := make([]directoryClaim, 0, 5)
	for _, name := range []string{
		worktreeDirectory, upperDirectory, workDirectory, cacheDirectory, captureDirectory,
	} {
		var stat unix.Stat_t
		if err := unix.Fstatat(int(root.Fd()), name, &stat, unix.AT_SYMLINK_NOFOLLOW); err != nil {
			t.Fatal(err)
		}
		claims = append(claims, directoryClaim{name: name, device: stat.Dev, inode: stat.Ino})
	}
	return claims
}

func TestValidWorktreeRelativePathRejectsAliasesAndInvalidBytes(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"", "/absolute", "../escape", "a/../b", "a//b", "a\x00b", string([]byte{'a', 0xff})} {
		if validWorktreeRelativePath(value) {
			t.Fatalf("invalid worktree path %q was accepted", value)
		}
	}
	if !validWorktreeRelativePath(".") || !validWorktreeRelativePath("nested/file") {
		t.Fatal("canonical worktree paths were rejected")
	}
}
