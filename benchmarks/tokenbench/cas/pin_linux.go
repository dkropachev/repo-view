//go:build linux

package cas

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

// pinStagedFile duplicates the already-open O_CREATE|O_EXCL descriptor. It
// therefore cannot follow or block on a replacement directory entry, and it
// keeps the original inode allocated until publication or cleanup finishes.
func pinStagedFile(file *os.File) (*os.File, error) {
	descriptor, err := unix.FcntlInt(file.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("duplicate staged file descriptor: %w", err)
	}
	return os.NewFile(uintptr(descriptor), file.Name()+" (inode pin)"), nil
}
