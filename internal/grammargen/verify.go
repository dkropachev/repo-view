package grammargen

import (
	"crypto/sha256"
	"fmt"
	"io"
	"os"
)

func verifyFile(path, expectedDigest, label string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open %s: %w", label, err)
	}
	digest := sha256.New()
	if _, err := io.Copy(digest, file); err != nil {
		_ = file.Close()
		return fmt.Errorf("hash %s: %w", label, err)
	}
	if err := file.Close(); err != nil {
		return fmt.Errorf("close %s: %w", label, err)
	}
	actualDigest := fmt.Sprintf("%x", digest.Sum(nil))
	if actualDigest != expectedDigest {
		return fmt.Errorf("unexpected %s checksum: got %s, want %s", label, actualDigest, expectedDigest)
	}
	return nil
}

func digestBytes(data []byte) string {
	return fmt.Sprintf("%x", sha256.Sum256(data))
}
