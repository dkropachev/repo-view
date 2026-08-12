//go:build linux

package workspace

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/yapless/scopesifter/benchmarks/tokenbench/snapshot"
	"golang.org/x/sys/unix"
)

func TestScanCapturedWorktreeAcceptsCanonicalTree(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	content := []byte("content\n")
	if err := os.WriteFile(filepath.Join(root, "nested", "file"), content, 0o644); err != nil {
		t.Fatal(err)
	}
	directory, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = directory.Close() })
	base := []worktreeEntry{
		{path: ".", kind: snapshot.ManifestKindDirectory, mode: 0o700},
		{path: "nested", kind: snapshot.ManifestKindDirectory, mode: 0o755},
		{
			path: "nested/file", kind: snapshot.ManifestKindFile,
			digest: digest(content), mode: 0o644, size: int64(len(content)),
		},
	}
	entries, err := scanCapturedWorktree(context.Background(), directory, base, validLimits())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(entries, base) {
		t.Fatalf("captured entries = %#v, want %#v", entries, base)
	}
	if worktreeManifestDigest(entries) != worktreeManifestDigest(base) {
		t.Fatal("equal captured manifests produced different commitments")
	}
}

func TestScanCapturedWorktreeDoesNotChargeImmutableLowerBytesToUpper(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	content := []byte{'x'}
	for _, name := range []string{"one", "two"} {
		if err := os.WriteFile(filepath.Join(root, name), content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	directory, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = directory.Close() })
	base := []worktreeEntry{
		{path: ".", kind: snapshot.ManifestKindDirectory, mode: 0o700},
		{path: "one", kind: snapshot.ManifestKindFile, digest: digest(content), mode: 0o644, size: 1},
		{path: "two", kind: snapshot.ManifestKindFile, digest: digest(content), mode: 0o644, size: 1},
	}
	limits := validLimits()
	limits.MaximumUpperBytes = 1
	limits.MaximumFileBytes = 1
	limits.MaximumEntries = len(base)
	entries, err := scanCapturedWorktree(context.Background(), directory, base, limits)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(entries, base) {
		t.Fatalf("captured entries = %#v, want %#v", entries, base)
	}
}

func TestScanCapturedWorktreeRejectsUnrepresentableState(t *testing.T) {
	t.Parallel()
	tests := map[string]func(*testing.T, string){
		"empty directory": func(t *testing.T, root string) {
			t.Helper()
			if err := os.Mkdir(filepath.Join(root, "empty"), 0o755); err != nil {
				t.Fatal(err)
			}
		},
		"file mode": func(t *testing.T, root string) {
			t.Helper()
			if err := os.WriteFile(filepath.Join(root, "file"), []byte("x"), 0o600); err != nil {
				t.Fatal(err)
			}
		},
		"new directory mode": func(t *testing.T, root string) {
			t.Helper()
			if err := os.Mkdir(filepath.Join(root, "nested"), 0o700); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "nested", "file"), []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		"xattr": func(t *testing.T, root string) {
			t.Helper()
			path := filepath.Join(root, "file")
			if err := os.WriteFile(path, []byte("x"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := unix.Setxattr(path, "user.tokenbench", []byte("x"), 0); err != nil {
				t.Fatal(err)
			}
		},
	}
	for name, prepare := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if err := os.Chmod(root, 0o700); err != nil {
				t.Fatal(err)
			}
			prepare(t, root)
			directory, err := os.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			defer directory.Close()
			base := []worktreeEntry{{path: ".", kind: snapshot.ManifestKindDirectory, mode: 0o700}}
			if _, err := scanCapturedWorktree(
				context.Background(),
				directory,
				base,
				validLimits(),
			); err == nil {
				t.Fatal("unrepresentable captured tree was accepted")
			}
		})
	}
}

func TestScanCapturedWorktreeAllowsPreexistingOpaqueEmptyDirectory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "dependency"), 0o755); err != nil {
		t.Fatal(err)
	}
	directory, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = directory.Close() })
	base := []worktreeEntry{
		{path: ".", kind: snapshot.ManifestKindDirectory, mode: 0o700},
		{path: "dependency", kind: snapshot.ManifestKindDirectory, mode: 0o755},
	}
	entries, err := scanCapturedWorktree(
		context.Background(),
		directory,
		base,
		validLimits(),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(entries, base) {
		t.Fatalf("opaque empty directory entries = %#v, want %#v", entries, base)
	}
}

func TestScanCapturedWorktreeRejectsDirectoryModeDrift(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "nested", "file"), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	directory, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = directory.Close() })
	base := []worktreeEntry{
		{path: ".", kind: snapshot.ManifestKindDirectory, mode: 0o700},
		{path: "nested", kind: snapshot.ManifestKindDirectory, mode: 0o700},
	}
	if _, err := scanCapturedWorktree(
		context.Background(),
		directory,
		base,
		validLimits(),
	); err == nil || !errors.Is(err, errInvalidWorkspaceTree) ||
		!strings.Contains(err.Error(), "changed mode") {
		t.Fatalf("directory mode drift = %v", err)
	}
}

func TestScanCapturedWorktreeSeparatesPolicyLimitsFromIntegrityErrors(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.Chmod(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "file")
	if err := os.WriteFile(path, []byte("content"), 0o600); err != nil {
		t.Fatal(err)
	}
	directory, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	base := []worktreeEntry{{path: ".", kind: snapshot.ManifestKindDirectory, mode: 0o700}}
	if _, err := scanCapturedWorktree(
		context.Background(), directory, base, validLimits(),
	); !errors.Is(err, errInvalidWorkspaceTree) {
		t.Fatalf("noncanonical file mode error = %v", err)
	}
	if err := os.Chmod(path, 0o644); err != nil {
		t.Fatal(err)
	}
	limits := validLimits()
	limits.MaximumUpperBytes = 1
	limits.MaximumFileBytes = 1
	if _, err := scanCapturedWorktree(
		context.Background(), directory, base, limits,
	); !errors.Is(err, errWorkspaceTreeLimit) {
		t.Fatalf("file limit error = %v", err)
	}
	if err := directory.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := scanCapturedWorktree(
		context.Background(), directory, base, validLimits(),
	); err == nil || errors.Is(err, errInvalidWorkspaceTree) ||
		errors.Is(err, errWorkspaceTreeLimit) {
		t.Fatalf("closed-descriptor integrity error = %v", err)
	}
}
