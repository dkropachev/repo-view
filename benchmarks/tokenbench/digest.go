package tokenbench

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
)

const maximumExecutableBytes = int64(1 << 30)

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
func FileSHA256(path string) (digest string, resultErr error) {
	before, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("hash %s: not a regular file", path)
	}
	if before.Size() < 0 || before.Size() > maximumExecutableBytes {
		return "", fmt.Errorf(
			"hash %s: executable exceeds %d bytes",
			path,
			maximumExecutableBytes,
		)
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			resultErr = errors.Join(
				resultErr,
				fmt.Errorf("close %s: %w", path, closeErr),
			)
			digest = ""
		}
	}()
	opened, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("inspect opened %s: %w", path, err)
	}
	if !opened.Mode().IsRegular() || !os.SameFile(before, opened) ||
		opened.Mode() != before.Mode() {
		return "", fmt.Errorf("hash %s: file changed before open", path)
	}
	hasher := sha256.New()
	written, err := io.Copy(hasher, io.LimitReader(file, maximumExecutableBytes+1))
	if err != nil {
		return "", fmt.Errorf("hash %s: %w", path, err)
	}
	if written > maximumExecutableBytes || written != before.Size() {
		return "", fmt.Errorf("hash %s: file size changed while hashing", path)
	}
	openedAfter, err := file.Stat()
	if err != nil {
		return "", fmt.Errorf("reinspect opened %s: %w", path, err)
	}
	after, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("reinspect %s: %w", path, err)
	}
	if !os.SameFile(before, openedAfter) || !os.SameFile(before, after) ||
		before.Size() != openedAfter.Size() || before.Size() != after.Size() ||
		before.Mode() != openedAfter.Mode() || before.Mode() != after.Mode() ||
		!before.ModTime().Equal(openedAfter.ModTime()) ||
		!before.ModTime().Equal(after.ModTime()) {
		return "", fmt.Errorf("hash %s: file changed while hashing", path)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}
