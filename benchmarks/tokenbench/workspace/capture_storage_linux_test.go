//go:build linux

package workspace

import (
	"reflect"
	"testing"

	"github.com/scopesifter/scopesifter/benchmarks/tokenbench/snapshot"
)

func TestCaptureFileUpdatesSkipsExactBaseFiles(t *testing.T) {
	t.Parallel()
	base := []worktreeEntry{
		{path: ".", kind: snapshot.ManifestKindDirectory, mode: 0o700},
		{path: "deleted", kind: snapshot.ManifestKindFile, digest: digest([]byte("delete")), mode: 0o644, size: 6},
		{path: "mode", kind: snapshot.ManifestKindFile, digest: digest([]byte("mode")), mode: 0o644, size: 4},
		{path: "same", kind: snapshot.ManifestKindFile, digest: digest([]byte("same")), mode: 0o644, size: 4},
	}
	result := []worktreeEntry{
		{path: ".", kind: snapshot.ManifestKindDirectory, mode: 0o700},
		{path: "mode", kind: snapshot.ManifestKindFile, digest: digest([]byte("mode")), mode: 0o755, size: 4},
		{path: "new", kind: snapshot.ManifestKindFile, digest: digest([]byte("new")), mode: 0o644, size: 3},
		{path: "same", kind: snapshot.ManifestKindFile, digest: digest([]byte("same")), mode: 0o644, size: 4},
	}
	want := []worktreeEntry{result[1], result[2]}
	if got := captureFileUpdates(base, result); !reflect.DeepEqual(got, want) {
		t.Fatalf("capture updates = %#v, want %#v", got, want)
	}
}

func TestEstimateCaptureScratchDoesNotChargeUnchangedBaseBlob(t *testing.T) {
	t.Parallel()
	base := []worktreeEntry{
		{path: ".", kind: snapshot.ManifestKindDirectory, mode: 0o700},
		{
			path: "large", kind: snapshot.ManifestKindFile,
			digest: digest([]byte("authenticated")), mode: 0o644, size: 1 << 30,
		},
	}
	unchanged, err := estimateCaptureScratch(base, base, 40, 4<<10)
	if err != nil {
		t.Fatal(err)
	}
	changed := append([]worktreeEntry(nil), base...)
	changed[1].digest = digest([]byte("changed"))
	changedEstimate, err := estimateCaptureScratch(base, changed, 40, 4<<10)
	if err != nil {
		t.Fatal(err)
	}
	if changedEstimate.bytes <= unchanged.bytes || changedEstimate.inodes <= unchanged.inodes {
		t.Fatalf("changed estimate = %#v, unchanged = %#v", changedEstimate, unchanged)
	}
	if unchanged.bytes >= 32<<20 {
		t.Fatalf("unchanged authenticated blob was charged as new storage: %#v", unchanged)
	}
}

func TestEstimateCaptureScratchSupportsBothObjectFormats(t *testing.T) {
	t.Parallel()
	entries := []worktreeEntry{
		{path: ".", kind: snapshot.ManifestKindDirectory, mode: 0o700},
		{path: "file", kind: snapshot.ManifestKindFile, digest: digest([]byte("x")), mode: 0o644, size: 1},
	}
	sha1, err := estimateCaptureScratch(nil, entries, 40, 4<<10)
	if err != nil {
		t.Fatal(err)
	}
	sha256, err := estimateCaptureScratch(nil, entries, 64, 4<<10)
	if err != nil {
		t.Fatal(err)
	}
	if sha1.bytes == 0 || sha1.inodes == 0 || sha1.bytes%(4<<10) != 0 {
		t.Fatalf("invalid SHA-1 estimate: %#v", sha1)
	}
	if sha256.bytes < sha1.bytes || sha256.inodes != sha1.inodes {
		t.Fatalf("SHA-256 estimate = %#v, SHA-1 = %#v", sha256, sha1)
	}
	if _, err := estimateCaptureScratch(nil, entries, 41, 4<<10); err == nil {
		t.Fatal("invalid object format was accepted")
	}
	if _, err := estimateCaptureScratch(nil, entries, 40, 0); err == nil {
		t.Fatal("zero block size was accepted")
	}
}

func TestCaptureStorageArithmeticRejectsOverflow(t *testing.T) {
	t.Parallel()
	if _, err := captureRoundToBlock(^uint64(0), 4<<10); err == nil {
		t.Fatal("overflowing block rounding was accepted")
	}
	if value, ok := captureCheckedAdd(^uint64(0), 1); ok || value != 0 {
		t.Fatalf("overflowing addition = %d, %v", value, ok)
	}
	if value, ok := captureCheckedMultiply(^uint64(0), 2); ok || value != 0 {
		t.Fatalf("overflowing multiplication = %d, %v", value, ok)
	}
	if _, err := captureLooseObjectBlocks(^uint64(0), 4<<10); err == nil {
		t.Fatal("overflowing loose object estimate was accepted")
	}
}
