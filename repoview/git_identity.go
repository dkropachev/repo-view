package repoview

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

type gitExecutableIdentity struct {
	info   os.FileInfo
	path   string
	sha256 string
	mode   os.FileMode
}

func newGitExecutableIdentity(
	executable, expectedSHA256 string,
) (gitExecutableIdentity, error) {
	if !filepath.IsAbs(executable) || filepath.Clean(executable) != executable {
		return gitExecutableIdentity{}, errors.New(
			"git executable must be an absolute canonical path",
		)
	}
	if !validSHA256(expectedSHA256) {
		return gitExecutableIdentity{}, errors.New("git executable SHA-256 is invalid")
	}
	resolved, err := filepath.EvalSymlinks(executable)
	if err != nil {
		return gitExecutableIdentity{}, fmt.Errorf("resolve git executable: %w", err)
	}
	if resolved != executable {
		return gitExecutableIdentity{}, errors.New("git executable path must not be a symlink")
	}
	contentSHA256, info, err := stableExecutableSHA256(executable)
	if err != nil {
		return gitExecutableIdentity{}, err
	}
	if contentSHA256 != expectedSHA256 {
		return gitExecutableIdentity{}, fmt.Errorf(
			"git executable digest mismatch: got %s, want %s",
			contentSHA256,
			expectedSHA256,
		)
	}
	identity := gitExecutableIdentity{
		info:   info,
		path:   executable,
		sha256: expectedSHA256,
		mode:   info.Mode(),
	}
	if err := identity.verify(); err != nil {
		return gitExecutableIdentity{}, fmt.Errorf("pin git executable: %w", err)
	}
	return identity, nil
}

func (identity gitExecutableIdentity) verify() error {
	resolved, err := filepath.EvalSymlinks(identity.path)
	if err != nil {
		return fmt.Errorf("resolve pinned git executable: %w", err)
	}
	if resolved != identity.path {
		return errors.New("pinned git executable became a symlink")
	}
	digest, info, err := stableExecutableSHA256(identity.path)
	if err != nil {
		return err
	}
	switch {
	case !os.SameFile(identity.info, info):
		return errors.New("pinned git executable was replaced")
	case info.Mode() != identity.mode:
		return errors.New("pinned git executable mode changed")
	case digest != identity.sha256:
		return errors.New("pinned git executable content changed")
	default:
		return nil
	}
}

// command opens and authenticates the executable inode before constructing
// the subprocess. On Unix the child executes that already-open descriptor, so
// replacing the configured pathname after verification cannot redirect this
// invocation. The returned file must remain open through Cmd.Run/Output.
func (identity gitExecutableIdentity) command(
	arguments ...string,
) (*exec.Cmd, *os.File, error) {
	return identity.commandContext(context.Background(), arguments...)
}

func (identity gitExecutableIdentity) commandContext(
	ctx context.Context,
	arguments ...string,
) (*exec.Cmd, *os.File, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	file, err := identity.openVerified()
	if err != nil {
		return nil, nil, err
	}
	commandPath := identity.path
	if runtime.GOOS != "windows" {
		commandPath = "/dev/fd/3"
		if runtime.GOOS == "linux" {
			commandPath = "/proc/self/fd/3"
		}
	}
	command := exec.CommandContext(ctx, commandPath, arguments...)
	command.Args[0] = identity.path
	if runtime.GOOS != "windows" {
		command.ExtraFiles = []*os.File{file}
	}
	return command, file, nil
}

func (identity gitExecutableIdentity) openVerified() (
	result *os.File,
	resultErr error,
) {
	before, err := os.Lstat(identity.path)
	if err != nil {
		return nil, fmt.Errorf("inspect pinned git executable: %w", err)
	}
	if !os.SameFile(identity.info, before) || before.Mode() != identity.mode ||
		before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() ||
		hasMultipleLinks(before) {
		return nil, errors.New("pinned git executable identity changed before open")
	}
	file, err := os.Open(identity.path)
	if err != nil {
		return nil, fmt.Errorf("open pinned git executable: %w", err)
	}
	valid := false
	defer func() {
		if !valid {
			if closeErr := file.Close(); closeErr != nil {
				resultErr = errors.Join(
					resultErr,
					fmt.Errorf("close unaccepted git executable: %w", closeErr),
				)
			}
			result = nil
		}
	}()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil, errors.New("pinned git executable changed while opening")
	}
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return nil, fmt.Errorf("read pinned git executable: %w", err)
	}
	if hex.EncodeToString(hasher.Sum(nil)) != identity.sha256 {
		return nil, errors.New("pinned git executable content changed")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return nil, fmt.Errorf("rewind pinned git executable: %w", err)
	}
	after, err := os.Lstat(identity.path)
	if err != nil || !os.SameFile(before, after) || before.Mode() != after.Mode() ||
		before.Size() != after.Size() || !before.ModTime().Equal(after.ModTime()) {
		return nil, errors.New("pinned git executable changed while opening")
	}
	valid = true
	return file, nil
}

func stableExecutableSHA256(path string) (
	digest string,
	info os.FileInfo,
	resultErr error,
) {
	before, err := os.Lstat(path)
	if err != nil {
		return "", nil, fmt.Errorf("inspect git executable: %w", err)
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return "", nil, errors.New("git executable is not a regular file")
	}
	if runtime.GOOS != "windows" && before.Mode().Perm()&0o111 == 0 {
		return "", nil, errors.New("git executable is not executable")
	}
	if hasMultipleLinks(before) {
		return "", nil, errors.New("git executable must not be hard-linked")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", nil, fmt.Errorf("open git executable: %w", err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			resultErr = errors.Join(
				resultErr,
				fmt.Errorf("close git executable: %w", closeErr),
			)
			digest = ""
			info = nil
		}
	}()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return "", nil, errors.New("git executable changed before open")
	}
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", nil, fmt.Errorf("read git executable: %w", err)
	}
	openedAfter, err := file.Stat()
	if err != nil {
		return "", nil, fmt.Errorf("reinspect open git executable: %w", err)
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, openedAfter) || !os.SameFile(before, after) ||
		before.Size() != openedAfter.Size() || before.Size() != after.Size() ||
		before.Mode() != openedAfter.Mode() || before.Mode() != after.Mode() ||
		!before.ModTime().Equal(openedAfter.ModTime()) ||
		!before.ModTime().Equal(after.ModTime()) {
		return "", nil, errors.New("git executable changed while hashing")
	}
	return hex.EncodeToString(hasher.Sum(nil)), before, nil
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && value == hex.EncodeToString(decoded)
}
