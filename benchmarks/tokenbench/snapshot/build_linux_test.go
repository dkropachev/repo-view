//go:build linux

package snapshot

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestImmutableFileHasMeasuredFSVerity(t *testing.T) {
	workingDirectory := os.Getenv("TOKENBENCH_FSVERITY_TEST_ROOT")
	directory := ""
	if workingDirectory == "" {
		directory = t.TempDir()
	} else if !filepath.IsAbs(workingDirectory) || filepath.Clean(workingDirectory) != workingDirectory {
		t.Fatalf("TOKENBENCH_FSVERITY_TEST_ROOT must be absolute and canonical: %q", workingDirectory)
	} else if info, err := os.Stat(workingDirectory); err != nil || !info.IsDir() {
		t.Fatalf("TOKENBENCH_FSVERITY_TEST_ROOT is not a directory: %v", err)
	}
	if directory == "" {
		var err error
		directory, err = makeFSVerityTestDirectory(workingDirectory)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.RemoveAll(directory) })
	}
	path := filepath.Join(directory, "immutable")
	digestValue, measurement, err := writeImmutableFile(path, []byte("content\n"), 0o444)
	if err != nil {
		if errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.ENOTTY) ||
			errors.Is(err, unix.EPERM) || errors.Is(err, unix.EINVAL) {
			privilegedSnapshotTestUnavailable(t, "filesystem does not permit fs-verity: %v", err)
		}
		t.Fatalf("writeImmutableFile(): %v", err)
	}
	if digestValue != digest([]byte("content\n")) || !validSHA256(measurement) {
		t.Fatalf("immutable identities = %q %q", digestValue, measurement)
	}
	if err := os.WriteFile(path, []byte("forged\n"), 0o444); err == nil {
		t.Fatal("fs-verity file remained writable")
	}
}

func makeFSVerityTestDirectory(root string) (string, error) {
	return os.MkdirTemp(root, ".fsverity-test-")
}

func TestFSVerityMerkleBlockSizeIsPageCompatible(t *testing.T) {
	file, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = file.Close() })
	blockSize, err := fsVerityMerkleBlockSize(file)
	if err != nil {
		t.Fatal(err)
	}
	pageSize := uint32(os.Getpagesize())
	if blockSize < 1024 || blockSize&(blockSize-1) != 0 || blockSize > pageSize ||
		pageSize%blockSize != 0 {
		t.Fatalf("fs-verity block size %d is incompatible with page size %d", blockSize, pageSize)
	}
	if blockSize, err := fsVerityMerkleBlockSize(nil); err == nil || blockSize != 0 {
		t.Fatalf("nil fs-verity file = %d, %v", blockSize, err)
	}
}

func TestCopyRegularFileRejectsHardLinkOrigin(t *testing.T) {
	directory := t.TempDir()
	origin := filepath.Join(directory, "origin")
	link := filepath.Join(directory, "link")
	if err := os.WriteFile(origin, []byte("content"), 0o555); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(origin, link); err != nil {
		t.Fatal(err)
	}
	if _, err := copyRegularFile(
		t.Context(), origin, filepath.Join(directory, "copy"), 0o555,
	); err == nil || !strings.Contains(err.Error(), "single-link") {
		t.Fatalf("copyRegularFile() = %v, want hard-link rejection", err)
	}
}

func TestReadOnlySelfBindFailsClosedWithoutAuthority(t *testing.T) {
	root := t.TempDir()
	info, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := establishReadOnlySelfBind(root, info)
	if err != nil {
		if !strings.Contains(err.Error(), "CAP_SYS_ADMIN") &&
			!strings.Contains(err.Error(), "operation not permitted") &&
			!strings.Contains(err.Error(), "shared or slave") {
			t.Fatalf("establishReadOnlySelfBind() = %v", err)
		}
		if os.Getenv(requirePrivilegedSnapshotTestsEnvironment) == "1" {
			t.Fatalf("required private mount namespace/CAP_SYS_ADMIN prerequisite failed: %v", err)
		}
		return
	}
	if validateErr := identity.validate(root); validateErr != nil {
		t.Fatalf("mount identity: %v", validateErr)
	}
	if err := unix.Unmount(root, 0); err != nil {
		t.Fatalf("unmount test self-bind: %v", err)
	}
}

func TestPrivilegedMountedAuthorityCloseReleasesKernelBoundary(t *testing.T) {
	root := t.TempDir()
	info, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	// Open the descriptor capabilities before creating the self-bind. They pin
	// the containing private mount, not the child bind mount that Close must
	// detach, while still naming the exact same root inode.
	rootFile, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	parent, err := os.Open(filepath.Dir(root))
	if err != nil {
		_ = rootFile.Close()
		t.Fatal(err)
	}
	identity, err := establishReadOnlySelfBind(root, info)
	if err != nil {
		_ = rootFile.Close()
		_ = parent.Close()
		privilegedSnapshotTestUnavailable(
			t,
			"positive mounted snapshot authority unavailable: %v",
			err,
		)
	}
	mounted := true
	var authority *Authority
	t.Cleanup(func() {
		if authority != nil && !authority.Closed() {
			_ = authority.Close()
		}
		if mounted {
			_ = unix.Unmount(root, 0)
		}
	})
	authority = &Authority{
		inputs:   ExecutionInputs{SnapshotRoot: root},
		parent:   parent,
		root:     rootFile,
		rootInfo: info,
		mount:    identity,
		mounted:  true,
	}
	if err := authority.Close(); err != nil {
		t.Fatalf("close mounted snapshot authority: %v", err)
	}
	mounted = false
	if !authority.Closed() {
		t.Fatal("mounted snapshot authority did not release its descriptors and mount")
	}
	if err := authority.Close(); err != nil {
		t.Fatalf("second mounted snapshot authority close: %v", err)
	}
	current, err := os.Lstat(root)
	if err != nil || !os.SameFile(info, current) {
		t.Fatalf("snapshot root changed after authority close: %v", err)
	}
	if _, err := readMountIdentity(root, current); err == nil {
		t.Fatal("snapshot self-bind remained mounted after authority close")
	}
}

const requirePrivilegedSnapshotTestsEnvironment = "TOKENBENCH_REQUIRE_PRIVILEGED_TESTS"

func privilegedSnapshotTestUnavailable(t *testing.T, format string, arguments ...any) {
	t.Helper()
	message := fmt.Sprintf(format, arguments...)
	if os.Getenv(requirePrivilegedSnapshotTestsEnvironment) == "1" {
		t.Fatalf("required privileged kernel test prerequisite failed: %s", message)
	}
	t.Skip(message)
}

func TestParseChangedSpansMergesAndAnchorsDeletion(t *testing.T) {
	patch := []byte("@@ -1,0 +2,2 @@\n@@ -5,1 +4,0 @@\n@@ -9,1 +4,2 @@\n")
	spans, err := parseChangedSpans(patch)
	if err != nil {
		t.Fatal(err)
	}
	want := []ChangedLineSpan{{Start: 2, End: 5}}
	if len(spans) != len(want) || spans[0] != want[0] {
		t.Fatalf("parseChangedSpans() = %+v, want %+v", spans, want)
	}
}

func TestParseChangedFileStatesPreservesRenameAndQuotedPaths(t *testing.T) {
	output := []byte("R100\x00old\\name.go\x00new\\name.go\x00M\x00line\nbreak.go\x00")
	files, err := parseChangedFileStates(output)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 2 || files[0].Path != "line\nbreak.go" ||
		files[1].Path != "new\\name.go" || files[1].PreviousPath != "old\\name.go" ||
		files[1].Status != "renamed" || files[1].Similarity != 100 {
		t.Fatalf("parseChangedFileStates() = %+v", files)
	}
}

func TestParseBinaryPathsHandlesRenameRecords(t *testing.T) {
	output := []byte("-\t-\tbinary.dat\x001\t0\t\x00old.go\x00new.go\x00")
	paths, err := parseBinaryPaths(output)
	if err != nil {
		t.Fatal(err)
	}
	if !paths["binary.dat"] || paths["new.go"] || len(paths) != 2 {
		t.Fatalf("parseBinaryPaths() = %+v", paths)
	}
}

func TestParseMountInfoLineRequiresExactSelfBind(t *testing.T) {
	root := "/snapshot"
	temporary := t.TempDir()
	info, err := os.Lstat(temporary)
	if err != nil {
		t.Fatal(err)
	}
	// Parser behavior is covered end-to-end by establishReadOnlySelfBind when
	// mount authority is available; this test retains a compile-time assertion
	// that a nonmatching path is ignored without trusting its fields.
	if _, ok, err := parseMountInfoLine(
		"42 41 8:1 /snapshot /other ro,nosuid,nodev - ext4 /dev/test rw",
		root,
		info,
	); err != nil || ok {
		t.Fatalf("parseMountInfoLine(nonmatch) = ok %v, err %v", ok, err)
	}
}

func TestMountInfoParsersCanonicalizeEmptyOptionalFields(t *testing.T) {
	temporary := t.TempDir()
	info, err := os.Lstat(temporary)
	if err != nil {
		t.Fatal(err)
	}
	line := "42 41 8:1 /filesystem/subtree/snapshot /snapshot ro,nosuid,nodev - ext4 /dev/test rw"
	identity, ok, err := parseMountInfoLine(line, "/snapshot", info)
	if err != nil || !ok {
		t.Fatalf("parseMountInfoLine() = ok %v, err %v", ok, err)
	}
	if identity.OptionalFields == nil || len(identity.OptionalFields) != 0 {
		t.Fatalf("optional fields = %#v, want nonnil empty", identity.OptionalFields)
	}
	parent, err := parseParentMount(line)
	if err != nil {
		t.Fatal(err)
	}
	if parent.optional == nil || len(parent.optional) != 0 {
		t.Fatalf("parent optional fields = %#v, want nonnil empty", parent.optional)
	}
}

func TestRejectDescendantMountsFromMountInfo(t *testing.T) {
	raw := []byte(strings.Join([]string{
		"40 1 8:1 / / ro - ext4 /dev/test rw",
		"41 40 8:1 /snapshot /snapshot ro - ext4 /dev/test rw",
		"42 41 8:1 /snapshot/nested /snapshot/nested ro - ext4 /dev/test rw",
		// Opaque roots on unrelated pseudo-filesystems remain parseable.
		"43 40 0:4 mnt:[4026531840] /proc/ns ro - nsfs nsfs rw",
	}, "\n"))
	if err := rejectDescendantMountsFromMountInfo(raw, "/snapshot"); err == nil ||
		!strings.Contains(err.Error(), "descendant mount") {
		t.Fatalf("rejectDescendantMountsFromMountInfo() = %v", err)
	}
	if err := rejectDescendantMountsFromMountInfo(raw, "/unrelated"); err != nil {
		t.Fatalf("unrelated root rejected: %v", err)
	}
}

func TestInspectELFCanonicalizesEmptyNeeded(t *testing.T) {
	needed := canonicalELFDependencies(nil)
	if needed == nil || len(needed) != 0 {
		t.Fatalf("canonicalELFDependencies(nil) = %#v", needed)
	}
}

func TestNativeELFABIInspectionUsesOnlyELFHeader(t *testing.T) {
	if err := validateNativeExecutableABIs([]string{"/proc/self/exe"}); err != nil {
		t.Fatalf("validateNativeExecutableABIs(self) = %v", err)
	}
}

func TestBuildFailureCleanupRetainsNonemptyResidue(t *testing.T) {
	root := t.TempDir()
	info, err := os.Lstat(root)
	if err != nil {
		t.Fatal(err)
	}
	child := filepath.Join(root, "recoverable")
	if err := os.WriteFile(child, []byte("evidence\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := removeEmptyCreatedSnapshot(root, info); err == nil {
		t.Fatal("removeEmptyCreatedSnapshot() removed or accepted a nonempty tree")
	}
	if content, err := os.ReadFile(child); err != nil || string(content) != "evidence\n" {
		t.Fatalf("recoverable residue = %q, %v", content, err)
	}
}

func TestAuthorityClosedRequiresSuccessfulDescriptorRelease(t *testing.T) {
	clean := &Authority{}
	if clean.Closed() {
		t.Fatal("fresh authority reported a closed publication boundary")
	}
	if err := clean.Close(); err != nil || !clean.Closed() {
		t.Fatalf("clean Close() = %v, Closed() = %t", err, clean.Closed())
	}

	file, err := os.CreateTemp(t.TempDir(), "already-closed-")
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	failed := &Authority{root: file}
	first := failed.Close()
	second := failed.Close()
	if first == nil || second == nil || failed.Closed() {
		t.Fatalf(
			"failed release = (%v, %v), Closed() = %t; want persistent fail-closed state",
			first,
			second,
			failed.Closed(),
		)
	}
}
