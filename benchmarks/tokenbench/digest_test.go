package tokenbench

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestFileSHA256StreamsStableContentAndRejectsOversizedFiles(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "executable")
	content := []byte(strings.Repeat("streamed-content\n", 1<<15))
	if err := os.WriteFile(path, content, 0o700); err != nil {
		t.Fatal(err)
	}
	digest, err := FileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	if digest != SHA256(content) {
		t.Fatalf("digest = %s, want %s", digest, SHA256(content))
	}

	oversized := filepath.Join(t.TempDir(), "oversized")
	file, err := os.OpenFile(oversized, os.O_CREATE|os.O_WRONLY, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maximumExecutableBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := FileSHA256(oversized); err == nil ||
		!strings.Contains(err.Error(), "executable exceeds") {
		t.Fatalf("oversized executable error = %v, want byte limit", err)
	}
}
