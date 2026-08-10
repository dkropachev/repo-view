//go:build linux

package tokenbench

import (
	"errors"
	"os"
	"strings"

	"golang.org/x/sys/unix"
)

func openArtifactRootNoSymlinks(path string) (_ *os.File, resultErr error) {
	filesystemRoot, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	defer func() { resultErr = errors.Join(resultErr, unix.Close(filesystemRoot)) }()
	descriptor, err := unix.Openat2(filesystemRoot, strings.TrimPrefix(path, "/"), &unix.OpenHow{
		Flags: uint64(unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC),
		Resolve: uint64(unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS |
			unix.RESOLVE_NO_MAGICLINKS),
	})
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(descriptor), path), nil
}

func openArtifactFileNoSymlinks(root *os.File, relative string) (*os.File, error) {
	descriptor, err := unix.Openat2(int(root.Fd()), relative, &unix.OpenHow{
		Flags: uint64(unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW),
		Resolve: uint64(unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS |
			unix.RESOLVE_NO_MAGICLINKS),
	})
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(descriptor), relative), nil
}
