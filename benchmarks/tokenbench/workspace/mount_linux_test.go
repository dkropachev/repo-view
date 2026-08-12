//go:build linux

package workspace

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseMountRecordPreservesKernelIdentity(t *testing.T) {
	t.Parallel()
	namespace := namespaceIdentity{device: 8, inode: 9}
	line := `42 41 0:39 / /workspace\040root rw,nosuid,nodev,noatime shared:7 - overlay overlay rw,lowerdir=/lower,index=off`
	record, err := parseMountRecord(line, namespace)
	if err != nil {
		t.Fatal(err)
	}
	want := mountRecord{
		namespace: namespace, id: 42, parentID: 41, majorMinor: "0:39",
		root: "/", point: "/workspace root",
		options:  []string{"rw", "nosuid", "nodev", "noatime"},
		optional: []string{"shared:7"}, filesystem: "overlay", source: "overlay",
		superOptions: []string{"rw", "lowerdir=/lower", "index=off"},
	}
	if !reflect.DeepEqual(record, want) {
		t.Fatalf("record = %#v, want %#v", record, want)
	}
}

func TestParseMountRecordRejectsMalformedOrAmbiguousFields(t *testing.T) {
	t.Parallel()
	valid := "42 41 0:39 / /workspace rw,nosuid,nodev,noatime - tmpfs tmpfs rw,size=4096"
	tests := map[string]string{
		"missing separator":  strings.Replace(valid, " - ", " ", 1),
		"zero mount ID":      strings.Replace(valid, "42 41", "0 41", 1),
		"duplicate option":   strings.Replace(valid, "rw,nosuid", "rw,rw", 1),
		"relative point":     strings.Replace(valid, "/workspace", "workspace", 1),
		"noncanonical point": strings.Replace(valid, "/workspace", "/workspace/../other", 1),
		"unknown escape":     strings.Replace(valid, "/workspace", `/workspace\000bad`, 1),
	}
	for name, line := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if _, err := parseMountRecord(line, namespaceIdentity{device: 1, inode: 2}); err == nil {
				t.Fatal("malformed mountinfo record was accepted")
			}
		})
	}
}

func TestMountPoliciesRejectMissingOrUnexpectedRestrictions(t *testing.T) {
	t.Parallel()
	parent := mountRecord{id: 10}
	tmpfs := mountRecord{
		id: 11, parentID: 10, root: "/", filesystem: "tmpfs",
		options: []string{"rw", "nosuid", "nodev", "noexec", "noatime"},
	}
	if err := validateTmpfsRecord(tmpfs, parent); err != nil {
		t.Fatal(err)
	}
	mutated := tmpfs
	mutated.options = []string{"rw", "nosuid", "nodev", "noatime"}
	if err := validateTmpfsRecord(mutated, parent); err == nil {
		t.Fatal("executable tmpfs was accepted")
	}

	overlay := mountRecord{
		id: 12, parentID: 11, root: "/", filesystem: "overlay",
		options: []string{"rw", "nosuid", "nodev", "noatime"},
		superOptions: []string{
			"rw", "lowerdir=/proc/self/fd/1", "upperdir=/proc/self/fd/2",
			"workdir=/proc/self/fd/3", "xino=off",
		},
	}
	if err := validateOverlayRecord(
		overlay,
		tmpfs,
		"/proc/self/fd/1",
		"/proc/self/fd/2",
		"/proc/self/fd/3",
	); err != nil {
		t.Fatal(err)
	}
	mutated = overlay
	mutated.options = append(append([]string(nil), overlay.options...), "noexec")
	if err := validateOverlayRecord(
		mutated,
		tmpfs,
		"/proc/self/fd/1",
		"/proc/self/fd/2",
		"/proc/self/fd/3",
	); err == nil {
		t.Fatal("non-executable model overlay was accepted")
	}
	mutated = overlay
	mutated.superOptions = append(append([]string(nil), overlay.superOptions...), "metacopy=on")
	if err := validateOverlayRecord(
		mutated,
		tmpfs,
		"/proc/self/fd/1",
		"/proc/self/fd/2",
		"/proc/self/fd/3",
	); err == nil {
		t.Fatal("unsafe overlay superblock policy was accepted")
	}
}

func TestPathWithinRequiresComponentBoundary(t *testing.T) {
	t.Parallel()
	for _, child := range []string{"/workspace", "/workspace/model", "/workspace/model/file"} {
		if !pathWithin("/workspace", child) {
			t.Fatalf("child %q was not recognized", child)
		}
	}
	for _, other := range []string{"/workspaces", "/", "/outside"} {
		if pathWithin("/workspace", other) {
			t.Fatalf("nonchild %q was accepted", other)
		}
	}
}

func TestRetainWorkspaceRootBorrowsExactExistingDirectory(t *testing.T) {
	t.Parallel()
	parentPath := t.TempDir()
	rootPath := filepath.Join(parentPath, "mountpoint")
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	root, info, err := retainWorkspaceRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	if info == nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("retained mountpoint = %#v", info)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	if current, err := os.Stat(rootPath); err != nil || !current.IsDir() {
		t.Fatalf("borrowed mountpoint was removed: %v", err)
	}
}

func TestRetainWorkspaceRootRejectsAbsentNonemptyAndSymlinkPaths(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	if _, _, err := retainWorkspaceRoot(filepath.Join(parent, "absent")); err == nil {
		t.Fatal("absent borrowed mountpoint was accepted")
	}
	existing := filepath.Join(parent, "existing")
	if err := os.Mkdir(existing, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(existing, "entry"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, _, err := retainWorkspaceRoot(existing); err == nil {
		t.Fatal("nonempty borrowed mountpoint was accepted")
	}
	link := filepath.Join(parent, "link")
	if err := os.Symlink(existing, link); err != nil {
		t.Fatal(err)
	}
	if _, _, err := retainWorkspaceRoot(link); err == nil {
		t.Fatal("symlink borrowed mountpoint was accepted")
	}
}

func TestVerifyRetainedWorkspaceRootRefusesReplacementWithoutMutatingIt(t *testing.T) {
	t.Parallel()
	parentPath := t.TempDir()
	rootPath := filepath.Join(parentPath, "mountpoint")
	retainedPath := filepath.Join(parentPath, "retained")
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	root, info, err := retainWorkspaceRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	if err := os.Rename(rootPath, retainedPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := verifyRetainedWorkspaceRoot(rootPath, root, info); err == nil {
		t.Fatal("replacement borrowed mountpoint was accepted")
	}
	if current, err := os.Stat(rootPath); err != nil || !current.IsDir() {
		t.Fatalf("replacement borrowed mountpoint was mutated: %v", err)
	}
	if current, err := os.Stat(retainedPath); err != nil || !os.SameFile(info, current) {
		t.Fatalf("retained borrowed mountpoint identity changed: %v", err)
	}
}

func TestRetainWorkspaceRootRequiresOwnedPrivateMountpoint(t *testing.T) {
	t.Parallel()
	rootPath := filepath.Join(t.TempDir(), "mountpoint")
	if err := os.Mkdir(rootPath, 0o755); err != nil {
		t.Fatal(err)
	}
	if _, _, err := retainWorkspaceRoot(rootPath); err == nil {
		t.Fatal("nonprivate borrowed mountpoint was accepted")
	}
}

func TestDirectoryIsEmptyUsesFreshStreamEveryTime(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "entry"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	directory, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = directory.Close() })
	for attempt := range 2 {
		if err := directoryIsEmpty(directory); err == nil {
			t.Fatalf("nonempty directory passed attempt %d", attempt)
		}
	}
}
