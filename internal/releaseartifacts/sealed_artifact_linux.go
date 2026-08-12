//go:build linux

package releaseartifacts

import (
	"errors"
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func createSealedReleaseArtifact(name string, content []byte) (_ *os.File, resultErr error) {
	descriptor, err := unix.MemfdCreate(
		"scopesifter-release-"+name,
		unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING,
	)
	if err != nil {
		return nil, fmt.Errorf("create release artifact memfd: %w", err)
	}
	file := os.NewFile(uintptr(descriptor), name)
	if file == nil {
		_ = unix.Close(descriptor)
		return nil, errors.New("construct release artifact memfd")
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, file.Close())
		}
	}()
	if err := writeAll(file, content); err != nil {
		return nil, fmt.Errorf("write release artifact memfd: %w", err)
	}
	if err := file.Sync(); err != nil {
		return nil, fmt.Errorf("sync release artifact memfd: %w", err)
	}
	if err := file.Chmod(0o400); err != nil {
		return nil, fmt.Errorf("set release artifact memfd mode: %w", err)
	}
	required := unix.F_SEAL_WRITE | unix.F_SEAL_GROW | unix.F_SEAL_SHRINK | unix.F_SEAL_SEAL
	if _, err := unix.FcntlInt(file.Fd(), unix.F_ADD_SEALS, required); err != nil {
		return nil, fmt.Errorf("seal release artifact memfd: %w", err)
	}
	if err := verifySealedReleaseArtifact(file); err != nil {
		return nil, err
	}
	return file, nil
}

func verifySealedReleaseArtifact(file *os.File) error {
	if file == nil {
		return errors.New("release artifact descriptor is missing")
	}
	required := unix.F_SEAL_WRITE | unix.F_SEAL_GROW | unix.F_SEAL_SHRINK | unix.F_SEAL_SEAL
	seals, err := unix.FcntlInt(file.Fd(), unix.F_GET_SEALS, 0)
	if err != nil {
		return fmt.Errorf("inspect release artifact seals: %w", err)
	}
	if seals&required != required {
		return errors.New("release artifact descriptor lacks required immutable seals")
	}
	return nil
}
