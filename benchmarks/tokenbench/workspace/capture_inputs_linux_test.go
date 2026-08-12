//go:build linux

package workspace

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/scopesifter/scopesifter/benchmarks/tokenbench/snapshot"
	"golang.org/x/sys/unix"
)

func TestExactSnapshotEntryRequiresOneExactPath(t *testing.T) {
	t.Parallel()
	want := snapshot.ManifestEntry{SnapshotPath: "/snapshot/tool"}
	inputs := snapshot.ExecutionInputs{Manifest: []snapshot.ManifestEntry{want}}
	if got, err := exactSnapshotEntry(inputs, want.SnapshotPath); err != nil || got != want {
		t.Fatalf("exact entry = %#v, %v", got, err)
	}
	if _, err := exactSnapshotEntry(inputs, "/snapshot/missing"); err == nil {
		t.Fatal("missing snapshot entry was accepted")
	}
	inputs.Manifest = append(inputs.Manifest, want)
	if _, err := exactSnapshotEntry(inputs, want.SnapshotPath); err == nil {
		t.Fatal("duplicate snapshot entry was accepted")
	}
}

func TestVerifyRetainedSnapshotRoles(t *testing.T) {
	t.Parallel()
	directoryPath := t.TempDir()
	if err := os.Chmod(directoryPath, 0o700); err != nil {
		t.Fatal(err)
	}
	directory, err := os.Open(directoryPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = directory.Close() })
	directoryEntry := snapshot.ManifestEntry{
		SnapshotPath: directoryPath, Kind: snapshot.ManifestKindDirectory, Mode: 0o700,
	}
	if _, err := verifyRetainedSnapshotRole(
		snapshot.RetainedPath{File: directory, Entry: directoryEntry},
		snapshot.ManifestKindDirectory,
		false,
	); err != nil {
		t.Fatal(err)
	}

	filePath := filepath.Join(t.TempDir(), "git")
	content := []byte("git\n")
	if err := os.WriteFile(filePath, content, 0o555); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(filePath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	fileEntry := snapshot.ManifestEntry{
		SnapshotPath: filePath, Kind: snapshot.ManifestKindFile, Mode: 0o555,
		Size: int64(len(content)), SHA256: digest(content), FSVerity: true,
		FSVerityAlgorithm: snapshot.FSVerityAlgorithm,
	}
	retained := snapshot.RetainedPath{File: file, Entry: fileEntry}
	if _, err := verifyRetainedSnapshotRole(
		retained,
		snapshot.ManifestKindFile,
		true,
	); err != nil {
		t.Fatal(err)
	}
	changed := fileEntry
	changed.FSVerity = false
	if _, err := verifyRetainedSnapshotRole(
		snapshot.RetainedPath{File: file, Entry: changed},
		snapshot.ManifestKindFile,
		true,
	); err == nil {
		t.Fatal("non-verity retained executable was accepted")
	}
}

func TestDuplicateRetainedSourceIgnoresRelocatedPathReplacement(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	originalPath := filepath.Join(parent, "source")
	movedPath := filepath.Join(parent, "moved")
	if err := os.Mkdir(originalPath, 0o700); err != nil {
		t.Fatal(err)
	}
	original, err := os.Open(originalPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = original.Close() })
	originalInfo, err := original.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(originalPath, movedPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(originalPath, 0o700); err != nil {
		t.Fatal(err)
	}
	replacementInfo, err := os.Stat(originalPath)
	if err != nil {
		t.Fatal(err)
	}
	duplicate, err := duplicateRetainedFile(original, originalInfo, "test source")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = duplicate.Close() })
	duplicateInfo, err := duplicate.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if !os.SameFile(originalInfo, duplicateInfo) || os.SameFile(replacementInfo, duplicateInfo) {
		t.Fatal("source duplicate selected the replacement audit path")
	}
	flags, err := unix.FcntlInt(duplicate.Fd(), unix.F_GETFD, 0)
	if err != nil || flags&unix.FD_CLOEXEC == 0 {
		t.Fatalf("source duplicate is not close-on-exec: flags=%d err=%v", flags, err)
	}
}

func TestVerifyCaptureInputsRequiresEveryRetainedIdentity(t *testing.T) {
	t.Parallel()
	openDirectory := func(path string) (*os.File, os.FileInfo) {
		t.Helper()
		if err := os.Mkdir(path, 0o700); err != nil {
			t.Fatal(err)
		}
		file, err := os.Open(path)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = file.Close() })
		info, err := file.Stat()
		if err != nil {
			t.Fatal(err)
		}
		return file, info
	}
	root := t.TempDir()
	sourceRoot, sourceInfo := openDirectory(filepath.Join(root, "source"))
	objects, objectsInfo := openDirectory(filepath.Join(root, "objects"))
	gitPath := filepath.Join(root, "git")
	if err := os.WriteFile(gitPath, []byte("git"), 0o555); err != nil {
		t.Fatal(err)
	}
	git, err := os.Open(gitPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = git.Close() })
	gitInfo, err := git.Stat()
	if err != nil {
		t.Fatal(err)
	}
	pair := &PairAuthority{
		sourceRoot: sourceRoot, sourceInfo: sourceInfo,
		verifierGit: git, verifierInfo: gitInfo,
		gitObjects: objects, objectsInfo: objectsInfo,
	}
	if err := pair.verifyCaptureInputsLocked(); err != nil {
		t.Fatal(err)
	}
	replacementPath := filepath.Join(root, "replacement")
	if err := os.WriteFile(replacementPath, []byte("git"), 0o555); err != nil {
		t.Fatal(err)
	}
	replacement, err := os.Open(replacementPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = replacement.Close() })
	pair.verifierGit = replacement
	if err := pair.verifyCaptureInputsLocked(); err == nil {
		t.Fatal("replacement capture executable descriptor was accepted")
	}
}
