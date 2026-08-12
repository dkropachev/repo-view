//go:build linux

package taskctl

import (
	"errors"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func createAnonymousAtomicPublicationFile(directory *os.File) (*os.File, error) {
	if directory == nil {
		return nil, errors.New("atomic publication directory descriptor is missing")
	}
	descriptor, err := unix.Openat(
		int(directory.Fd()),
		".",
		unix.O_RDWR|unix.O_CLOEXEC|unix.O_TMPFILE,
		0o600,
	)
	if err != nil {
		return nil, err
	}
	// open(2) applies the process umask even to O_TMPFILE. Reset the exact
	// owned inode mode through its descriptor before any identity validation.
	if err := unix.Fchmod(descriptor, 0o600); err != nil {
		_ = unix.Close(descriptor)
		return nil, err
	}
	file := os.NewFile(uintptr(descriptor), "anonymous atomic publication")
	if file == nil {
		_ = unix.Close(descriptor)
		return nil, errors.New("construct anonymous atomic publication descriptor")
	}
	return file, nil
}

func linkAnonymousAtomicPublicationFile(
	file *os.File,
	directory *os.File,
	destinationName string,
) error {
	if file == nil || directory == nil || destinationName == "" {
		return errors.New("descriptor-bound atomic publication is incomplete")
	}
	return unix.Linkat(
		int(file.Fd()),
		"",
		int(directory.Fd()),
		destinationName,
		unix.AT_EMPTY_PATH,
	)
}

func atomicPublicationFileIsAnonymous(info os.FileInfo) bool {
	status, ok := info.Sys().(*syscall.Stat_t)
	return ok && status.Nlink == 0
}
