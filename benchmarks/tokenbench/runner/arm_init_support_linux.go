//go:build linux

package runner

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

type pinnedCommonExecutable struct {
	file       *os.File
	launchFile *os.File
	path       string
	digest     string
	info       os.FileInfo
}

func (pinned *pinnedCommonExecutable) close() error {
	if pinned == nil {
		return nil
	}
	var resultErr error
	if pinned.file != nil {
		resultErr = errors.Join(resultErr, pinned.file.Close())
		pinned.file = nil
	}
	if pinned.launchFile != nil {
		resultErr = errors.Join(resultErr, pinned.launchFile.Close())
		pinned.launchFile = nil
	}
	return resultErr
}

const executableSealMask = unix.F_SEAL_WRITE | unix.F_SEAL_GROW |
	unix.F_SEAL_SHRINK | unix.F_SEAL_EXEC | unix.F_SEAL_SEAL

func prepareArmInit(requireStatic bool) (*pinnedCommonExecutable, int, error) {
	abi, err := landlockABI()
	if err != nil {
		return nil, 0, err
	}
	if abi < minimumLandlockABI {
		return nil, 0, fmt.Errorf("Landlock ABI %d is below required ABI %d", abi, minimumLandlockABI)
	}
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return nil, 0, fmt.Errorf("disable runner dumpability: %w", err)
	}
	launcher, err := pinExecutable("/proc/self/exe", "", requireStatic, false, false)
	if err != nil {
		return nil, 0, fmt.Errorf("pin arm-init executable: %w", err)
	}
	return launcher, abi, nil
}

func pinExecutable(
	path string,
	expectedDigest string,
	requireStatic bool,
	requireReadOnly bool,
	requireSingleLink bool,
) (*pinnedCommonExecutable, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("executable path must be absolute and canonical")
	}
	var before os.FileInfo
	var err error
	if path != "/proc/self/exe" {
		before, err = os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if before.Mode()&os.ModeSymlink != 0 {
			return nil, errors.New("executable path is a symbolic link")
		}
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	valid := false
	defer func() {
		if !valid {
			_ = file.Close()
		}
	}()
	opened, err := file.Stat()
	if err != nil || !opened.Mode().IsRegular() || opened.Mode().Perm()&0o111 == 0 {
		return nil, errors.New("executable is not an executable regular file")
	}
	if before != nil && !os.SameFile(before, opened) {
		return nil, errors.New("executable changed while opening")
	}
	if requireReadOnly && opened.Mode().Perm()&0o222 != 0 {
		return nil, errors.New("common executable must have no writable mode bits")
	}
	if requireSingleLink && executableLinkCount(opened) != 1 {
		return nil, errors.New("common executable must have exactly one link")
	}
	digest, err := hashOpenFile(file)
	if err != nil {
		return nil, err
	}
	if expectedDigest != "" && digest != expectedDigest {
		return nil, errors.New("executable digest does not match its approved identity")
	}
	if requireStatic {
		if err := validateStaticELF(file); err != nil {
			return nil, err
		}
	}
	if before != nil {
		after, err := os.Lstat(path)
		if err != nil || !os.SameFile(opened, after) {
			return nil, errors.New("executable path changed while pinning")
		}
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	launchFile, err := sealExecutable(file, digest)
	if err != nil {
		return nil, err
	}
	valid = true
	return &pinnedCommonExecutable{
		file: file, launchFile: launchFile, path: path, digest: digest, info: opened,
	}, nil
}

func (pinned *pinnedCommonExecutable) reverify() error {
	if pinned == nil || pinned.file == nil {
		return errors.New("pinned executable is closed")
	}
	opened, err := pinned.file.Stat()
	if err != nil || !os.SameFile(opened, pinned.info) ||
		opened.Mode() != pinned.info.Mode() || opened.Size() != pinned.info.Size() ||
		!opened.ModTime().Equal(pinned.info.ModTime()) ||
		executableLinkCount(opened) != executableLinkCount(pinned.info) {
		return errors.New("pinned executable metadata changed")
	}
	digest, err := hashOpenFile(pinned.file)
	if err != nil {
		return err
	}
	if digest != pinned.digest {
		return errors.New("pinned executable content changed")
	}
	if pinned.path != "/proc/self/exe" {
		pathInfo, err := os.Lstat(pinned.path)
		if err != nil || !os.SameFile(opened, pathInfo) {
			return errors.New("pinned executable pathname changed")
		}
	}
	_, err = pinned.file.Seek(0, io.SeekStart)
	if err != nil {
		return err
	}
	if pinned.launchFile == nil {
		return errors.New("sealed executable image is closed")
	}
	seals, err := unix.FcntlInt(pinned.launchFile.Fd(), unix.F_GET_SEALS, 0)
	if err != nil || seals&executableSealMask != executableSealMask {
		return errors.New("sealed executable image lost its immutable seal set")
	}
	launchDigest, err := hashOpenFile(pinned.launchFile)
	if err != nil {
		return err
	}
	if launchDigest != pinned.digest {
		return errors.New("sealed executable image digest changed")
	}
	_, err = pinned.launchFile.Seek(0, io.SeekStart)
	return err
}

func sealExecutable(source *os.File, expectedDigest string) (*os.File, error) {
	descriptor, err := unix.MemfdCreate(
		"tokenbench-executable-v1",
		unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING|unix.MFD_EXEC,
	)
	if err != nil {
		return nil, fmt.Errorf("create executable memfd: %w", err)
	}
	writable := os.NewFile(uintptr(descriptor), "tokenbench-executable-v1")
	valid := false
	defer func() {
		if !valid {
			_ = writable.Close()
		}
	}()
	if _, err := source.Seek(0, io.SeekStart); err != nil {
		return nil, err
	}
	if _, err := io.Copy(writable, source); err != nil {
		return nil, fmt.Errorf("copy executable into memfd: %w", err)
	}
	if err := writable.Sync(); err != nil {
		return nil, fmt.Errorf("sync executable memfd: %w", err)
	}
	if err := writable.Chmod(0o555); err != nil {
		return nil, fmt.Errorf("set executable memfd mode: %w", err)
	}
	digest, err := hashOpenFile(writable)
	if err != nil {
		return nil, err
	}
	if digest != expectedDigest {
		return nil, errors.New("executable memfd copy digest mismatch")
	}
	if _, err := unix.FcntlInt(writable.Fd(), unix.F_ADD_SEALS, executableSealMask); err != nil {
		return nil, fmt.Errorf("seal executable memfd: %w", err)
	}
	seals, err := unix.FcntlInt(writable.Fd(), unix.F_GET_SEALS, 0)
	if err != nil || seals&executableSealMask != executableSealMask {
		return nil, errors.New("executable memfd did not retain its immutable seal set")
	}
	readOnlyDescriptor, err := unix.Open(
		fmt.Sprintf("/proc/self/fd/%d", writable.Fd()),
		unix.O_RDONLY|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		return nil, fmt.Errorf("reopen sealed executable memfd read-only: %w", err)
	}
	readOnly := os.NewFile(uintptr(readOnlyDescriptor), "tokenbench-executable-v1-ro")
	if err := writable.Close(); err != nil {
		_ = readOnly.Close()
		return nil, err
	}
	valid = true
	return readOnly, nil
}

func hashOpenFile(file *os.File) (string, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func validateStaticELF(file *os.File) error {
	parsed, err := elf.NewFile(file)
	if err != nil {
		return fmt.Errorf("parse executable ELF: %w", err)
	}
	for _, program := range parsed.Progs {
		if program.Type == elf.PT_INTERP {
			return errors.New("executable has a mutable dynamic interpreter")
		}
	}
	needed, err := parsed.DynString(elf.DT_NEEDED)
	if err != nil {
		return fmt.Errorf("inspect executable dynamic dependencies: %w", err)
	}
	if len(needed) != 0 {
		return fmt.Errorf("executable has dynamic dependencies: %s", strings.Join(needed, ", "))
	}
	return nil
}

func executableLinkCount(info os.FileInfo) uint64 {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return 0
	}
	return uint64(stat.Nlink)
}

func requireFSVerity(file *os.File) error {
	if file == nil {
		return errors.New("fs-verity executable is closed")
	}
	flags, err := unix.IoctlGetInt(int(file.Fd()), unix.FS_IOC_GETFLAGS)
	if err != nil {
		return fmt.Errorf("inspect executable fs-verity flag: %w", err)
	}
	if flags&unix.FS_VERITY_FL == 0 {
		return errors.New("conformant executable is not protected by fs-verity")
	}
	return nil
}

func normalizeWritablePaths(paths []string) ([]string, error) {
	result := append([]string(nil), paths...)
	sort.Strings(result)
	for index, path := range result {
		if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
			return nil, fmt.Errorf("Landlock writable path %q is not absolute, canonical, and non-root", path)
		}
		if path == cgroupMountPath || strings.HasPrefix(path, cgroupMountPath+"/") ||
			path == "/proc" || strings.HasPrefix(path, "/proc/") ||
			path == "/sys" || strings.HasPrefix(path, "/sys/") {
			return nil, fmt.Errorf("Landlock writable path %q overlaps a protected kernel filesystem", path)
		}
		if index != 0 && result[index-1] == path {
			return nil, fmt.Errorf("Landlock writable path %q is duplicated", path)
		}
	}
	return result, nil
}

func normalizePolicyPaths(paths []string, kind string) ([]string, error) {
	result := append([]string(nil), paths...)
	sort.Strings(result)
	for index, path := range result {
		if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
			return nil, fmt.Errorf(
				"Landlock %s path %q is not absolute, canonical, and non-root",
				kind,
				path,
			)
		}
		if path == cgroupMountPath || strings.HasPrefix(path, cgroupMountPath+"/") ||
			path == "/proc" || strings.HasPrefix(path, "/proc/") ||
			path == "/sys" || strings.HasPrefix(path, "/sys/") ||
			path == "/dev" || strings.HasPrefix(path, "/dev/") {
			return nil, fmt.Errorf("Landlock %s path %q overlaps a protected host filesystem", kind, path)
		}
		if index != 0 && result[index-1] == path {
			return nil, fmt.Errorf("Landlock %s path %q is duplicated", kind, path)
		}
	}
	return result, nil
}

func openWritableRoots(paths []string) ([]*os.File, error) {
	roots := make([]*os.File, 0, len(paths))
	valid := false
	defer func() {
		if !valid {
			for _, root := range roots {
				_ = root.Close()
			}
		}
	}()
	for _, path := range paths {
		before, err := os.Lstat(path)
		if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.IsDir() {
			return nil, fmt.Errorf("Landlock writable root %q is not a real directory", path)
		}
		descriptor, err := unix.Open(path, unix.O_PATH|unix.O_DIRECTORY|unix.O_NOFOLLOW|unix.O_CLOEXEC, 0)
		if err != nil {
			return nil, fmt.Errorf("open Landlock writable root %q: %w", path, err)
		}
		root := os.NewFile(uintptr(descriptor), path)
		opened, err := root.Stat()
		if err != nil || !os.SameFile(before, opened) {
			_ = root.Close()
			return nil, fmt.Errorf("Landlock writable root %q changed while opening", path)
		}
		roots = append(roots, root)
	}
	valid = true
	return roots, nil
}

func openPolicyRoots(paths []string, executable, requireVerity bool) ([]*os.File, error) {
	roots := make([]*os.File, 0, len(paths))
	valid := false
	defer func() {
		if !valid {
			for _, root := range roots {
				_ = root.Close()
			}
		}
	}()
	for _, path := range paths {
		before, err := os.Lstat(path)
		if err != nil || before.Mode()&os.ModeSymlink != 0 ||
			!before.Mode().IsRegular() && !before.IsDir() {
			return nil, fmt.Errorf("Landlock input root %q is not a real file or directory", path)
		}
		if executable && (!before.Mode().IsRegular() || before.Mode().Perm()&0o111 == 0) {
			return nil, fmt.Errorf("Landlock executable %q is not an executable regular file", path)
		}
		flags := unix.O_PATH | unix.O_NOFOLLOW | unix.O_CLOEXEC
		if before.IsDir() {
			flags |= unix.O_DIRECTORY
		}
		descriptor, err := unix.Open(path, flags, 0)
		if err != nil {
			return nil, fmt.Errorf("open Landlock input root %q: %w", path, err)
		}
		root := os.NewFile(uintptr(descriptor), path)
		opened, err := root.Stat()
		if err != nil || !os.SameFile(before, opened) || opened.Mode() != before.Mode() ||
			opened.Size() != before.Size() || !opened.ModTime().Equal(before.ModTime()) {
			_ = root.Close()
			return nil, fmt.Errorf("Landlock input root %q changed while opening", path)
		}
		if requireVerity {
			if err := requireFSVerity(root); err != nil {
				_ = root.Close()
				return nil, fmt.Errorf("verify Landlock executable %q: %w", path, err)
			}
		}
		roots = append(roots, root)
	}
	valid = true
	return roots, nil
}

func openDevNullRule() (*os.File, error) {
	descriptor, err := unix.Open("/dev/null", unix.O_PATH|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(descriptor), "/dev/null")
	info, err := file.Stat()
	if err != nil || info.Mode()&os.ModeDevice == 0 || info.Mode()&os.ModeCharDevice == 0 {
		_ = file.Close()
		return nil, errors.New("/dev/null is not a character device")
	}
	return file, nil
}

func probeArmInitBoundary(
	manager *cgroupManager,
	armInit, common, devNull *os.File,
	timeout time.Duration,
) error {
	arm, err := manager.newArm()
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	command := exec.CommandContext(ctx, "/proc/self/fd/3")
	command.Args = []string{"tokenbench-arm-init-probe"}
	command.ExtraFiles = []*os.File{armInit, armInit, common, devNull}
	command.Env = []string{
		armInitMarkerEnvironment + "=" + armInitVersion,
		armInitProbeEnvironment + "=" + armInitVersion,
		armInitFDLayoutEnvironment + "=" + formatArmInitFDLayout(
			0,
			0,
			0,
			[]uint16{},
			[]uint16{},
		),
	}
	command.Dir = "/"
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := configureContainedCommand(command, arm); err != nil {
		_ = arm.killAndRemove(timeout)
		return err
	}
	runErr := command.Run()
	cleanupErr := arm.killAndRemove(timeout)
	want := armInitVersion + ":atomic-cgroup+landlock+no-new-privs+target-dumpable+seccomp\n"
	if runErr != nil || cleanupErr != nil || stdout.String() != want || stderr.Len() != 0 {
		return fmt.Errorf(
			"arm-init startup probe failed: run=%v cleanup=%v stdout=%q stderr=%q",
			runErr,
			cleanupErr,
			stdout.String(),
			stderr.String(),
		)
	}
	return nil
}
