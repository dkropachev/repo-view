//go:build linux

package selfexec

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"syscall"
)

var (
	processIdentityOnce sync.Once
	processIdentity     pinnedIdentity
)

type pinnedIdentity struct {
	mu       sync.Mutex
	file     *os.File
	fileInfo os.FileInfo
	identity Identity
	err      error
}

// Current returns the identity of the exact executable inode running this
// process. The /proc/self/exe descriptor is deliberately retained for the
// process lifetime; the mutable display path is never used as hash authority.
func Current() (Identity, error) {
	processIdentityOnce.Do(initialize)
	processIdentity.mu.Lock()
	defer processIdentity.mu.Unlock()
	if processIdentity.err != nil {
		return Identity{}, processIdentity.err
	}
	if err := verifyPinnedIdentity(); err != nil {
		processIdentity.err = err
		return Identity{}, err
	}
	return processIdentity.identity, nil
}

func initialize() {
	file, err := os.Open("/proc/self/exe")
	if err != nil {
		processIdentity.err = fmt.Errorf("open running executable image: %w", err)
		return
	}
	// Do not close file after a successful initialization. Keeping the inode
	// pinned is the basis of all subsequent identity verification.

	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		processIdentity.err = fmt.Errorf("inspect running executable image: %w", err)
		return
	}
	if err := validateExecutableInfo(info, "running executable image"); err != nil {
		_ = file.Close()
		processIdentity.err = err
		return
	}
	digest, err := digestOpenFile(file, info.Size())
	if err != nil {
		_ = file.Close()
		processIdentity.err = fmt.Errorf("hash running executable image: %w", err)
		return
	}
	afterHash, err := file.Stat()
	if err != nil {
		_ = file.Close()
		processIdentity.err = fmt.Errorf("reinspect running executable image: %w", err)
		return
	}
	if err := validateUnchangedFile(info, afterHash, "running executable image"); err != nil {
		_ = file.Close()
		processIdentity.err = err
		return
	}

	path, pathInfo, err := resolveDisplayPath()
	if err != nil {
		_ = file.Close()
		processIdentity.err = err
		return
	}
	if !os.SameFile(afterHash, pathInfo) {
		_ = file.Close()
		processIdentity.err = errors.New("canonical executable path does not identify the running executable inode")
		return
	}
	finalInfo, err := file.Stat()
	if err != nil {
		_ = file.Close()
		processIdentity.err = fmt.Errorf("reinspect running executable image after resolving its path: %w", err)
		return
	}
	if err := validateUnchangedFile(afterHash, finalInfo, "running executable image"); err != nil {
		_ = file.Close()
		processIdentity.err = err
		return
	}
	if err := validateUnchangedFile(pathInfo, finalInfo, "canonical executable path"); err != nil {
		_ = file.Close()
		processIdentity.err = err
		return
	}

	processIdentity.file = file
	processIdentity.fileInfo = finalInfo
	processIdentity.identity = Identity{Path: path, SHA256: digest}
}

func verifyPinnedIdentity() error {
	beforeHash, err := processIdentity.file.Stat()
	if err != nil {
		return fmt.Errorf("inspect pinned running executable image: %w", err)
	}
	if err := validateUnchangedFile(
		processIdentity.fileInfo,
		beforeHash,
		"pinned running executable image",
	); err != nil {
		return err
	}
	digest, err := digestOpenFile(processIdentity.file, beforeHash.Size())
	if err != nil {
		return fmt.Errorf("reverify pinned running executable image: %w", err)
	}
	afterHash, err := processIdentity.file.Stat()
	if err != nil {
		return fmt.Errorf("reinspect pinned running executable image: %w", err)
	}
	if err := validateUnchangedFile(beforeHash, afterHash, "pinned running executable image"); err != nil {
		return err
	}
	if digest != processIdentity.identity.SHA256 {
		return errors.New("pinned running executable image content changed")
	}

	path, pathInfo, err := resolveDisplayPath()
	if err != nil {
		return err
	}
	if path != processIdentity.identity.Path {
		return errors.New("canonical executable path changed after identity was pinned")
	}
	if !os.SameFile(afterHash, pathInfo) {
		return errors.New("canonical executable path no longer identifies the pinned running executable inode")
	}
	if err := validateUnchangedFile(afterHash, pathInfo, "canonical executable path"); err != nil {
		return err
	}
	finalInfo, err := processIdentity.file.Stat()
	if err != nil {
		return fmt.Errorf("reinspect pinned running executable image after resolving its path: %w", err)
	}
	if err := validateUnchangedFile(afterHash, finalInfo, "pinned running executable image"); err != nil {
		return err
	}
	if err := validateUnchangedFile(pathInfo, finalInfo, "canonical executable path"); err != nil {
		return err
	}
	return nil
}

func resolveDisplayPath() (string, os.FileInfo, error) {
	path, err := os.Executable()
	if err != nil {
		return "", nil, fmt.Errorf("resolve executable display path: %w", err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", nil, fmt.Errorf("make executable display path absolute: %w", err)
	}
	path, err = filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return "", nil, fmt.Errorf("canonicalize executable display path: %w", err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return "", nil, fmt.Errorf("make canonical executable path absolute: %w", err)
	}
	path = filepath.Clean(path)
	if !filepath.IsAbs(path) {
		return "", nil, errors.New("canonical executable path is not absolute")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", nil, fmt.Errorf("inspect canonical executable path: %w", err)
	}
	if err := validateExecutableInfo(info, "canonical executable path"); err != nil {
		return "", nil, err
	}
	return path, info, nil
}

func digestOpenFile(file *os.File, size int64) (string, error) {
	hash := sha256.New()
	if _, err := io.Copy(hash, io.NewSectionReader(file, 0, size)); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func validateExecutableInfo(info os.FileInfo, label string) error {
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf("%s is not an executable regular file", label)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		return fmt.Errorf("%s has no Linux inode metadata", label)
	}
	if stat.Nlink != 1 {
		return fmt.Errorf("%s must have exactly one filesystem link", label)
	}
	return nil
}

func validateUnchangedFile(want, got os.FileInfo, label string) error {
	if err := validateExecutableInfo(got, label); err != nil {
		return err
	}
	if !os.SameFile(want, got) ||
		want.Mode() != got.Mode() ||
		want.Size() != got.Size() ||
		!want.ModTime().Equal(got.ModTime()) {
		return fmt.Errorf("%s changed after identity was pinned", label)
	}
	return nil
}
