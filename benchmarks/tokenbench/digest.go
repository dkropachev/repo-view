package tokenbench

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
)

// SHA256 returns the lowercase hexadecimal digest of content.
func SHA256(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

// JSONSHA256 returns the digest of Go's deterministic JSON encoding for value.
func JSONSHA256(value any) (string, error) {
	content, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return SHA256(content), nil
}

// ValidSHA256 reports whether value is exactly one lowercase SHA-256 digest.
func ValidSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size {
		return false
	}
	return value == hex.EncodeToString(decoded)
}

// FileSHA256 hashes one stable regular file and rejects symlinks or a file that
// changes while it is open.
func FileSHA256(path string) (string, error) {
	content, err := readStableRegularFile(path)
	if err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}
	return SHA256(content), nil
}
