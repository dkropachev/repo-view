package cas

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"os"
)

const (
	privateDirectoryMode fs.FileMode = 0o700
	stagedFileMode       fs.FileMode = 0o600
	objectFileMode       fs.FileMode = 0o400
	transactionLockMode  fs.FileMode = 0o600
	transactionLockName              = ".transactions.lock"
)

var errMultipleLinks = errors.New("CAS object has multiple hard links")

func ensurePrivateDirectory(parent *os.Root, name string) (*os.Root, os.FileInfo, error) {
	err := parent.Mkdir(name, privateDirectoryMode)
	created := err == nil
	switch {
	case err == nil:
		// Validate and sync below.
	case errors.Is(err, fs.ErrExist):
		// A concurrent creator may have won. Validate the entry below.
	default:
		return nil, nil, fmt.Errorf("create private directory %s: %w", name, err)
	}
	if created {
		if err := parent.Chmod(name, privateDirectoryMode); err != nil {
			return nil, nil, fmt.Errorf("set private directory mode on %s: %w", name, err)
		}
	}
	directory, info, err := openPrivateDirectory(parent, name)
	if err != nil {
		return nil, nil, err
	}
	// Sync even when a concurrent process created the directory. Its process
	// may have stopped after mkdir and before making the parent entry durable.
	if err := syncRoot(parent); err != nil {
		return nil, nil, joinCloseError(
			fmt.Errorf("sync parent containing %s: %w", name, err),
			"close private directory after parent sync failure",
			directory,
		)
	}
	return directory, info, nil
}

func openPrivateDirectory(parent *os.Root, name string) (*os.Root, os.FileInfo, error) {
	before, err := parent.Lstat(name)
	if err != nil {
		return nil, nil, fmt.Errorf("lstat directory %s: %w", name, err)
	}
	if err := validatePrivateDirectoryInfo(name, before); err != nil {
		return nil, nil, err
	}

	directory, err := parent.OpenRoot(name)
	if err != nil {
		return nil, nil, fmt.Errorf("open directory %s: %w", name, err)
	}
	opened, err := directory.Stat(".")
	if err != nil {
		return nil, nil, joinCloseError(
			fmt.Errorf("stat opened directory %s: %w", name, err),
			"close private directory after stat failure",
			directory,
		)
	}
	if err := validatePrivateDirectoryInfo(name, opened); err != nil {
		return nil, nil, joinCloseError(
			err,
			"close invalid private directory",
			directory,
		)
	}
	if !os.SameFile(before, opened) {
		return nil, nil, joinCloseError(
			fmt.Errorf("%w: directory %s changed while opening", ErrIntegrity, name),
			"close changed private directory",
			directory,
		)
	}

	current, err := parent.Lstat(name)
	if err != nil {
		return nil, nil, joinCloseError(
			fmt.Errorf("%w: re-lstat directory %s: %w", ErrIntegrity, name, err),
			"close private directory after re-lstat failure",
			directory,
		)
	}
	if err := validatePrivateDirectoryInfo(name, current); err != nil {
		return nil, nil, joinCloseError(
			err,
			"close revalidated private directory",
			directory,
		)
	}
	if !os.SameFile(opened, current) {
		return nil, nil, joinCloseError(
			fmt.Errorf("%w: directory %s changed while opening", ErrIntegrity, name),
			"close changed private directory",
			directory,
		)
	}
	return directory, opened, nil
}

func validatePrivateDirectoryInfo(name string, info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("%w: %s is not a real directory", ErrIntegrity, name)
	}
	if info.Mode().Perm() != privateDirectoryMode {
		return fmt.Errorf(
			"%w: directory %s has mode %s, want %s",
			ErrIntegrity,
			name,
			info.Mode().Perm(),
			privateDirectoryMode,
		)
	}
	return nil
}

func syncRoot(root *os.Root) error {
	directory, err := root.Open(".")
	if err != nil {
		return fmt.Errorf("open directory for sync: %w", err)
	}
	if err := directory.Sync(); err != nil {
		return joinCloseError(
			fmt.Errorf("sync directory: %w", err),
			"close directory after sync failure",
			directory,
		)
	}
	if err := directory.Close(); err != nil {
		return fmt.Errorf("close synced directory: %w", err)
	}
	return nil
}

func joinCloseError(
	resultErr error,
	operation string,
	closer interface{ Close() error },
) error {
	closeErr := wrapError(operation, closer.Close())
	if closeErr == nil {
		return resultErr
	}
	return errors.Join(resultErr, closeErr)
}

func openTransactionLock(staging *os.Root) (*os.File, os.FileInfo, error) {
	lock, err := staging.OpenFile(
		transactionLockName,
		os.O_RDWR|os.O_CREATE|os.O_EXCL,
		transactionLockMode,
	)
	created := err == nil
	if errors.Is(err, fs.ErrExist) {
		lock, err = staging.OpenFile(transactionLockName, os.O_RDWR, 0)
	}
	if err != nil {
		return nil, nil, fmt.Errorf("open transaction lock: %w", err)
	}
	if created {
		if err := lock.Chmod(transactionLockMode); err != nil {
			lock.Close()
			return nil, nil, fmt.Errorf("set transaction lock mode: %w", err)
		}
	}

	opened, err := lock.Stat()
	if err != nil {
		closeErr := lock.Close()
		return nil, nil, errors.Join(
			fmt.Errorf("stat transaction lock: %w", err),
			closeErr,
		)
	}
	if err := validateTransactionLockInfo(opened); err != nil {
		lock.Close()
		return nil, nil, err
	}
	current, err := staging.Lstat(transactionLockName)
	if err != nil || !os.SameFile(opened, current) {
		lock.Close()
		return nil, nil, fmt.Errorf("%w: transaction lock changed while opening", ErrIntegrity)
	}
	if err := validateTransactionLockInfo(current); err != nil {
		lock.Close()
		return nil, nil, err
	}
	if err := syncRoot(staging); err != nil {
		lock.Close()
		return nil, nil, fmt.Errorf("sync staging containing transaction lock: %w", err)
	}
	return lock, opened, nil
}

func validateTransactionLockInfo(info os.FileInfo) error {
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: transaction lock is not a regular file", ErrIntegrity)
	}
	if info.Mode().Perm() != transactionLockMode {
		return fmt.Errorf(
			"%w: transaction lock has mode %s, want %s",
			ErrIntegrity,
			info.Mode().Perm(),
			transactionLockMode,
		)
	}
	if multipleLinks(info) {
		return fmt.Errorf("%w: transaction lock has multiple links", ErrIntegrity)
	}
	return nil
}

func validateCanonicalTransactionLock(staging *os.Root, expected os.FileInfo) error {
	current, err := staging.Lstat(transactionLockName)
	if err != nil || !os.SameFile(expected, current) {
		return fmt.Errorf("%w: canonical transaction lock changed", ErrIntegrity)
	}
	return validateTransactionLockInfo(current)
}

func randomEntryName(prefix string) (string, error) {
	var entropy [16]byte
	if _, err := rand.Read(entropy[:]); err != nil {
		return "", fmt.Errorf("generate private entry name: %w", err)
	}
	return prefix + hex.EncodeToString(entropy[:]), nil
}
