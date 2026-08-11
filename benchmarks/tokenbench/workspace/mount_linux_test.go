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

func TestClaimWorkspaceRootCreatesAndRemovesExactAbsentDirectory(t *testing.T) {
	t.Parallel()
	parentPath := t.TempDir()
	rootPath := filepath.Join(parentPath, "claimed")
	parent, root, info, leaf, err := claimWorkspaceRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	if leaf != "claimed" || info == nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		t.Fatalf("claim = %q, %#v", leaf, info)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	if err := removeClaimedRoot(parent, leaf, info); err != nil {
		t.Fatal(err)
	}
	if err := parent.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(rootPath); !os.IsNotExist(err) {
		t.Fatalf("claimed root remains: %v", err)
	}
}

func TestClaimWorkspaceRootRejectsExistingPathAndSymlink(t *testing.T) {
	t.Parallel()
	parent := t.TempDir()
	existing := filepath.Join(parent, "existing")
	if err := os.Mkdir(existing, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := claimWorkspaceRoot(existing); err == nil {
		t.Fatal("existing workspace root was accepted")
	}
	link := filepath.Join(parent, "link")
	if err := os.Symlink(existing, link); err != nil {
		t.Fatal(err)
	}
	if _, _, _, _, err := claimWorkspaceRoot(link); err == nil {
		t.Fatal("symlink workspace root was accepted")
	}
}

func TestRemoveClaimedRootRefusesReplacementPath(t *testing.T) {
	t.Parallel()
	parentPath := t.TempDir()
	rootPath := filepath.Join(parentPath, "claimed")
	retainedPath := filepath.Join(parentPath, "retained")
	parent, root, info, leaf, err := claimWorkspaceRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = root.Close()
		_ = parent.Close()
	})
	if err := os.Rename(rootPath, retainedPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := removeClaimedRoot(parent, leaf, info); err == nil {
		t.Fatal("replacement workspace root was removed")
	}
	if current, err := os.Stat(rootPath); err != nil || !current.IsDir() {
		t.Fatalf("replacement workspace root was not retained: %v", err)
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
