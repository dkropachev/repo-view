package source

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestTreeDigestFromGitObjectsMatchesWorktreeDigest(t *testing.T) {
	repository := newRepository(t)
	if err := os.Chmod(repository.root, 0o775); err != nil {
		t.Fatal(err)
	}
	regular := filepath.Join(repository.root, "nested", "data.txt")
	executable := filepath.Join(repository.root, "nested", "scripts", "tool")
	writeFile(t, regular, "regular bytes\n")
	writeFile(t, executable, "#!/bin/sh\nexit 0\n")
	for _, directory := range []string{
		filepath.Dir(regular),
		filepath.Dir(executable),
	} {
		if err := os.Chmod(directory, 0o775); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Chmod(regular, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(executable, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, repository.root, "add", "nested/data.txt", "nested/scripts/tool")
	runGit(
		t,
		repository.root,
		"update-index",
		"--add",
		"--cacheinfo",
		gitlinkMode+","+repository.base+",vendor/dependency",
	)
	commit(t, repository.root, "object tree digest")

	want, err := TreeDigest(context.Background(), repository.root)
	if err != nil {
		t.Fatal(err)
	}
	rootTreeID, entries := testGitObjectTree(t, repository.root)
	for left, right := 0, len(entries)-1; left < right; left, right = left+1, right-1 {
		entries[left], entries[right] = entries[right], entries[left]
	}
	readBlobs := 0
	got, err := TreeDigestFromGitObjects(
		context.Background(),
		"sha1",
		rootTreeID,
		0o775,
		entries,
		func(ctx context.Context, objectID string, destination io.Writer) error {
			readBlobs++
			return testReadGitBlob(ctx, repository.root, objectID, destination)
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("object digest = %s, worktree digest = %s", got, want)
	}
	if readBlobs != len(entries)-1 {
		t.Fatalf("blob reads = %d, want %d regular entries", readBlobs, len(entries)-1)
	}
}

func TestTreeDigestFromGitObjectsAuthenticatesTreeAndBlobs(t *testing.T) {
	repository := newRepository(t)
	if err := os.Chmod(repository.root, 0o775); err != nil {
		t.Fatal(err)
	}
	rootTreeID, entries := testGitObjectTree(t, repository.root)
	readCalls := 0
	reader := func(ctx context.Context, objectID string, destination io.Writer) error {
		readCalls++
		return testReadGitBlob(ctx, repository.root, objectID, destination)
	}

	drifted := append([]GitTreeEntry(nil), entries...)
	drifted[0].ObjectID = strings.Repeat("0", 40)
	if _, err := TreeDigestFromGitObjects(
		context.Background(), "sha1", rootTreeID, 0o775, drifted, reader,
	); err == nil || !strings.Contains(err.Error(), "reconstructed root tree ID") {
		t.Fatalf("tree drift error = %v, want root-tree authentication", err)
	}
	if readCalls != 0 {
		t.Fatalf("tree drift triggered %d blob reads before root authentication", readCalls)
	}

	if _, err := TreeDigestFromGitObjects(
		context.Background(),
		"sha1",
		rootTreeID,
		0o775,
		entries,
		func(context.Context, string, io.Writer) error {
			_, err := io.WriteString(io.Discard, "ignored")
			return err
		},
	); err == nil || !strings.Contains(err.Error(), "hashes to") {
		t.Fatalf("blob drift error = %v, want blob authentication", err)
	}
}

func TestTreeDigestFromGitObjectsMatchesSHA256Worktree(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sha256-repository")
	if err := os.Mkdir(root, 0o775); err != nil {
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
	if err := os.Chmod(root, 0o775); err != nil {
		t.Fatal(err)
	}
	writeFile(t, filepath.Join(root, "file.txt"), "sha256 content\n")
	if err := os.Chmod(filepath.Join(root, "file.txt"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "add", "file.txt")
	commit(t, root, "sha256 object tree")
	want, err := TreeDigest(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	rootTreeID, entries := testGitObjectTree(t, root)
	got, err := TreeDigestFromGitObjects(
		context.Background(),
		"sha256",
		rootTreeID,
		0o775,
		entries,
		func(ctx context.Context, objectID string, destination io.Writer) error {
			return testReadGitBlob(ctx, root, objectID, destination)
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("SHA-256 object digest = %s, worktree digest = %s", got, want)
	}
}

func TestTreeDigestFromGitObjectsRejectsUnsafeEntries(t *testing.T) {
	validID := strings.Repeat("a", 40)
	tests := []struct {
		name    string
		entries []GitTreeEntry
		want    string
	}{
		{"symlink", []GitTreeEntry{{"link", "120000", validID}}, "symlinks"},
		{"unsafe mode", []GitTreeEntry{{"file", "100664", validID}}, "unsupported tracked mode"},
		{"absolute", []GitTreeEntry{{"/file", "100644", validID}}, "absolute"},
		{"escape", []GitTreeEntry{{"../file", "100644", validID}}, "escapes"},
		{"reserved metadata", []GitTreeEntry{{".GIT/config", "100644", validID}}, "reserved .git"},
		{"backslash", []GitTreeEntry{{`a\b`, "100644", validID}}, "forbidden byte"},
		{"newline", []GitTreeEntry{{"a\nb", "100644", validID}}, "forbidden byte"},
		{"invalid UTF-8", []GitTreeEntry{{string([]byte{'a', 0xff}), "100644", validID}}, "valid UTF-8"},
		{"duplicate", []GitTreeEntry{{"file", "100644", validID}, {"file", "100644", validID}}, "duplicate"},
		{"file-directory collision", []GitTreeEntry{{"a", "100644", validID}, {"a/b", "100644", validID}}, "traverses file"},
		{"directory-file collision", []GitTreeEntry{{"a/b", "100644", validID}, {"a", "100644", validID}}, "traverses file"},
		{"uppercase object ID", []GitTreeEntry{{"file", "100644", strings.Repeat("A", 40)}}, "lowercase hexadecimal"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := TreeDigestFromGitObjects(
				context.Background(),
				"sha1",
				strings.Repeat("0", 40),
				0o775,
				test.entries,
				func(context.Context, string, io.Writer) error { return nil },
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func testGitObjectTree(t *testing.T, repository string) (string, []GitTreeEntry) {
	t.Helper()
	rootTreeID := strings.TrimSpace(git(t, repository, "rev-parse", "HEAD^{tree}"))
	listing := []byte(git(t, repository, "ls-tree", "-r", "-z", "--full-tree", "HEAD"))
	entries := make([]GitTreeEntry, 0)
	for _, record := range bytes.Split(listing, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		parts := bytes.SplitN(record, []byte{'\t'}, 2)
		if len(parts) != 2 {
			t.Fatalf("malformed ls-tree record %q", record)
		}
		metadata := strings.Fields(string(parts[0]))
		if len(metadata) != 3 {
			t.Fatalf("malformed ls-tree metadata %q", parts[0])
		}
		entries = append(entries, GitTreeEntry{
			Path: string(parts[1]), Mode: metadata[0], ObjectID: metadata[2],
		})
	}
	return rootTreeID, entries
}

func testReadGitBlob(
	ctx context.Context,
	repository string,
	objectID string,
	destination io.Writer,
) error {
	command := exec.CommandContext(ctx, "git", "-C", repository, "cat-file", "blob", objectID)
	command.Env = append(
		os.Environ(),
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_SYSTEM="+os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
	)
	command.Stdout = destination
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("git cat-file: %w: %s", err, stderr.Bytes())
	}
	return nil
}
