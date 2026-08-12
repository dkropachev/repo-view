package grammargen

import (
	"os"
	"path/filepath"
	"testing"
)

func TestInstallArtifactsReplacesAndCreatesAtomically(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	existing := filepath.Join(root, "existing")
	created := filepath.Join(root, "nested", "created")
	if err := os.WriteFile(existing, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := installArtifacts([]artifact{
		{path: existing, data: []byte("new")},
		{path: created, data: []byte("created")},
	}); err != nil {
		t.Fatalf("installArtifacts() error = %v", err)
	}
	for path, want := range map[string]string{existing: "new", created: "created"} {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if string(data) != want {
			t.Errorf("%s = %q, want %q", path, data, want)
		}
		info, err := os.Stat(path)
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0o644 {
			t.Errorf("%s mode = %o, want 644", path, info.Mode().Perm())
		}
	}
	matches, err := filepath.Glob(filepath.Join(root, ".*.old-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Errorf("backup files remain: %v", matches)
	}
}

func TestInstallArtifactsDoesNotReplaceBeforeAllStagingSucceeds(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	existing := filepath.Join(root, "existing")
	blockingFile := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(existing, []byte("old"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(blockingFile, []byte("block"), 0o600); err != nil {
		t.Fatal(err)
	}
	err := installArtifacts([]artifact{
		{path: existing, data: []byte("new")},
		{path: filepath.Join(blockingFile, "child"), data: []byte("never")},
	})
	if err == nil {
		t.Fatal("installArtifacts() succeeded, want staging error")
	}
	data, readErr := os.ReadFile(existing)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if string(data) != "old" {
		t.Fatalf("existing artifact = %q, want unchanged old data", data)
	}
}
