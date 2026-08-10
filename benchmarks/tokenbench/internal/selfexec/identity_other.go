//go:build !linux

package selfexec

import "errors"

// Current fails closed because tokenbench has no supported non-Linux
// capability for pinning the exact executable image backing this process.
func Current() (Identity, error) {
	return Identity{}, errors.New("pinned running executable identity is supported only on Linux")
}
