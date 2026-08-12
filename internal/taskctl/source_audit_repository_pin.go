package taskctl

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// sourceAuditRepositoryPin keeps the exact repository directory admitted by
// binding validation open for a native-Git invocation. The child enters this
// descriptor, never the mutable repository pathname.
type sourceAuditRepositoryPin struct {
	expected os.FileInfo
	file     *os.File
	path     string
}

var sourceAuditRepositoryPinHook func(stage, path string) error

func openSourceAuditRepositoryPin(
	path string,
	expected os.FileInfo,
) (*sourceAuditRepositoryPin, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path ||
		expected == nil || !expected.IsDir() || expected.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("source-audit repository pin is incomplete")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, fmt.Errorf("resolve source-audit repository pin: %w", err)
	}
	if resolved != path {
		return nil, errors.New("source-audit repository pin traverses a symlink")
	}
	before, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("inspect source-audit repository pin: %w", err)
	}
	if err := validateSourceAuditRepositoryPinIdentity(expected, before); err != nil {
		return nil, err
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("open source-audit repository pin: %w", err)
	}
	accepted := false
	defer func() {
		if !accepted {
			_ = file.Close()
		}
	}()
	pin := &sourceAuditRepositoryPin{path: path, expected: expected, file: file}
	if err := pin.validate(); err != nil {
		return nil, err
	}
	if sourceAuditRepositoryPinHook != nil {
		if err := sourceAuditRepositoryPinHook("after-open", path); err != nil {
			return nil, fmt.Errorf("source-audit repository pin hook: %w", err)
		}
	}
	accepted = true
	return pin, nil
}

func validateSourceAuditRepositoryPinIdentity(want, got os.FileInfo) error {
	if want == nil || got == nil || !got.IsDir() || got.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(want, got) || want.Mode() != got.Mode() ||
		!want.ModTime().Equal(got.ModTime()) {
		return errors.New("source-audit repository changed from its admitted identity")
	}
	return nil
}

func (pin *sourceAuditRepositoryPin) validate() error {
	if pin == nil || pin.file == nil || pin.path == "" || pin.expected == nil {
		return errors.New("source-audit repository pin is incomplete")
	}
	opened, openedErr := pin.file.Stat()
	current, pathErr := os.Lstat(pin.path)
	resolved, resolveErr := filepath.EvalSymlinks(pin.path)
	if err := errors.Join(openedErr, pathErr, resolveErr); err != nil {
		return fmt.Errorf("revalidate source-audit repository pin: %w", err)
	}
	if resolved != pin.path {
		return errors.New("source-audit repository pin path changed or traverses a symlink")
	}
	if err := validateSourceAuditRepositoryPinIdentity(pin.expected, opened); err != nil {
		return err
	}
	if err := validateSourceAuditRepositoryPinIdentity(pin.expected, current); err != nil {
		return err
	}
	return nil
}

func (pin *sourceAuditRepositoryPin) close() error {
	if pin == nil || pin.file == nil {
		return nil
	}
	validationErr := pin.validate()
	closeErr := pin.file.Close()
	pin.file = nil
	if closeErr != nil {
		closeErr = fmt.Errorf("close source-audit repository pin: %w", closeErr)
	}
	return errors.Join(validationErr, closeErr)
}
