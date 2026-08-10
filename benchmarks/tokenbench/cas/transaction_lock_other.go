//go:build !linux

package cas

import (
	"errors"
	"os"
)

func lockTransactionShared(_ *os.File) error {
	return errors.New("transaction locking is unsupported on this platform")
}

func tryLockTransactionExclusive(_ *os.File) (bool, error) {
	return false, errors.New("transaction locking is unsupported on this platform")
}

func unlockTransaction(_ *os.File) error {
	return errors.New("transaction locking is unsupported on this platform")
}
