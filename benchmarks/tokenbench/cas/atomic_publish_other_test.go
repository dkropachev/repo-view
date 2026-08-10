//go:build !linux

package cas

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenFailsClosedWithoutAtomicNoReplace(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cas")
	if err := os.Mkdir(root, privateDirectoryMode); err != nil {
		t.Fatalf("Mkdir(CAS root): %v", err)
	}
	_, err := Open(root, Options{MaxObjectBytes: 1 << 20})
	if err == nil || !strings.Contains(err.Error(), "atomic no-replace") {
		t.Fatalf("Open() error = %v, want unsupported atomic no-replace error", err)
	}
}
