package taskctl

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSourceAuditInputSnapshotEnumeratesAndRevalidatesExactInputs(t *testing.T) {
	bindings, selections := writeSourceAuditInputSnapshotFixture(t)
	snapshot, err := newSourceAuditInputSnapshot(bindings, selections)
	if err != nil {
		t.Fatal(err)
	}
	wantPaths := []string{bindings, selections}
	gotPaths := snapshot.inputPaths()
	if len(gotPaths) != sourceAuditInputFileCount {
		t.Fatalf("input path count = %d, want %d", len(gotPaths), sourceAuditInputFileCount)
	}
	for index, path := range wantPaths {
		if gotPaths[index] != path {
			t.Fatalf("inputPaths()[%d] = %q, want %q", index, gotPaths[index], path)
		}
		data, readErr := snapshot.bytesFor(path)
		if readErr != nil || len(data) == 0 {
			t.Fatalf("bytesFor(%q) = %q, %v", path, data, readErr)
		}
		copyData := append([]byte(nil), data...)
		copyData[0] ^= 0xff
		again, readErr := snapshot.bytesFor(path)
		if readErr != nil || bytes.Equal(copyData, again) {
			t.Fatal("bytesFor returned mutable snapshot state")
		}
	}
	gotPaths[0] = "changed"
	if snapshot.inputPaths()[0] == "changed" {
		t.Fatal("inputPaths returned mutable snapshot state")
	}
	if _, err := snapshot.bytesFor(filepath.Join(t.TempDir(), "extra")); err == nil {
		t.Fatal("bytesFor admitted an unknown input")
	}
	if err := snapshot.revalidate(); err != nil {
		t.Fatalf("stable snapshot revalidation: %v", err)
	}
}

func TestSourceAuditInputSnapshotRejectsChangedBytes(t *testing.T) {
	bindings, selections := writeSourceAuditInputSnapshotFixture(t)
	snapshot, err := newSourceAuditInputSnapshot(bindings, selections)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(selections, []byte("changed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.revalidate(); err == nil ||
		!strings.Contains(err.Error(), "source selections changed") {
		t.Fatalf("changed-input error = %v", err)
	}
}

func TestSourceAuditInputSnapshotRejectsSymlinkAndAlias(t *testing.T) {
	t.Run("symlink", func(t *testing.T) {
		bindings, selections := writeSourceAuditInputSnapshotFixture(t)
		if err := os.Remove(selections); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(bindings, selections); err != nil {
			t.Fatal(err)
		}
		if _, err := newSourceAuditInputSnapshot(bindings, selections); err == nil ||
			!strings.Contains(err.Error(), "symlink") {
			t.Fatalf("symlink error = %v", err)
		}
	})
	t.Run("hardlink", func(t *testing.T) {
		bindings, selections := writeSourceAuditInputSnapshotFixture(t)
		if err := os.Remove(selections); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(bindings, selections); err != nil {
			t.Fatal(err)
		}
		if _, err := newSourceAuditInputSnapshot(bindings, selections); err == nil ||
			!strings.Contains(err.Error(), "single-link") {
			t.Fatalf("hardlink error = %v", err)
		}
	})
	t.Run("same path", func(t *testing.T) {
		bindings, _ := writeSourceAuditInputSnapshotFixture(t)
		if _, err := newSourceAuditInputSnapshot(bindings, bindings); err == nil ||
			!strings.Contains(err.Error(), "same canonical path") {
			t.Fatalf("same-path error = %v", err)
		}
	})
}

func writeSourceAuditInputSnapshotFixture(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	bindings := filepath.Join(root, "repository-bindings.json")
	selections := filepath.Join(root, "source-selections.json")
	if err := os.WriteFile(bindings, []byte("bindings\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(selections, []byte("selections\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	return bindings, selections
}
