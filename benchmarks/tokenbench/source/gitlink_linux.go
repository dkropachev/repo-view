//go:build linux

package source

import (
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

func verifyOpaqueGitlink(root, relative string) (
	materialization gitlinkMaterialization,
	resultErr error,
) {
	rootDescriptor, err := unix.Open(
		root,
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return gitlinkMaterialization{}, fmt.Errorf("open source root: %w", err)
	}
	defer func() {
		resultErr = errors.Join(resultErr, unix.Close(rootDescriptor))
	}()

	how := &unix.OpenHow{
		Flags: uint64(unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC),
		Resolve: uint64(
			unix.RESOLVE_BENEATH |
				unix.RESOLVE_NO_MAGICLINKS |
				unix.RESOLVE_NO_SYMLINKS |
				unix.RESOLVE_NO_XDEV,
		),
	}
	descriptor, err := unix.Openat2(rootDescriptor, relative, how)
	if errors.Is(err, unix.ENOENT) {
		return gitlinkMaterialization{}, nil
	}
	if errors.Is(err, unix.EXDEV) {
		return gitlinkMaterialization{}, errors.New("gitlink crosses a descendant mount")
	}
	if errors.Is(err, unix.ELOOP) {
		return gitlinkMaterialization{}, errors.New("gitlink traverses a symbolic link")
	}
	if err != nil {
		return gitlinkMaterialization{}, fmt.Errorf(
			"open empty directory without links or mounts: %w",
			err,
		)
	}
	directory := os.NewFile(uintptr(descriptor), relative)
	if directory == nil {
		_ = unix.Close(descriptor)
		return gitlinkMaterialization{}, errors.New("retain gitlink directory descriptor")
	}
	defer func() {
		resultErr = errors.Join(resultErr, directory.Close())
	}()

	var before unix.Stat_t
	if err := unix.Fstat(descriptor, &before); err != nil {
		return gitlinkMaterialization{}, fmt.Errorf("stat gitlink directory: %w", err)
	}
	if before.Mode&unix.S_IFMT != unix.S_IFDIR {
		return gitlinkMaterialization{}, errors.New("gitlink is not a real directory")
	}
	if names, err := directory.Readdirnames(1); err == nil || len(names) != 0 {
		return gitlinkMaterialization{}, errors.New("gitlink directory is initialized or nonempty")
	} else if !errors.Is(err, io.EOF) {
		return gitlinkMaterialization{}, fmt.Errorf("read gitlink directory: %w", err)
	}
	var after unix.Stat_t
	if err := unix.Fstat(descriptor, &after); err != nil {
		return gitlinkMaterialization{}, fmt.Errorf("restat gitlink directory: %w", err)
	}
	beforeIdentity := linuxGitlinkMaterialization(before)
	if beforeIdentity != linuxGitlinkMaterialization(after) {
		return gitlinkMaterialization{}, errors.New("gitlink directory changed while inspected")
	}
	return beforeIdentity, nil
}

func linuxGitlinkMaterialization(stat unix.Stat_t) gitlinkMaterialization {
	return gitlinkMaterialization{
		device:    stat.Dev,
		inode:     stat.Ino,
		linkCount: stat.Nlink,
		size:      stat.Size,
		mtimeSec:  stat.Mtim.Sec,
		mtimeNsec: stat.Mtim.Nsec,
		ctimeSec:  stat.Ctim.Sec,
		ctimeNsec: stat.Ctim.Nsec,
		mode:      stat.Mode,
		present:   true,
	}
}
