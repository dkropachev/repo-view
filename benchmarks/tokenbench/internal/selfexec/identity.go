// Package selfexec pins and verifies the identity of the executable image
// running the current tokenbench process.
package selfexec

// Identity is the canonical display path and SHA-256 digest of the exact
// executable inode running the current process.
type Identity struct {
	Path   string
	SHA256 string
}
