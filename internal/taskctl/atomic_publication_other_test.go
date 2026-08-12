//go:build !linux

package taskctl

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteAtomicPinnedFailsClosedWithoutDescriptorPublication(t *testing.T) {
	parent := t.TempDir()
	output := filepath.Join(parent, "report.json")
	err := writeAtomicPinned(
		output,
		[]byte("must not be published\n"),
		atomicPublicationHooks{},
	)
	if err == nil || !strings.Contains(err.Error(), "supported only on Linux") {
		t.Fatalf("writeAtomicPinned() error = %v, want platform rejection", err)
	}
	if _, err := os.Lstat(output); !os.IsNotExist(err) {
		t.Fatalf("unsupported platform created output: %v", err)
	}
	entries, err := os.ReadDir(parent)
	if err != nil || len(entries) != 0 {
		t.Fatalf("unsupported publication residue = %v, %v", entries, err)
	}
}
