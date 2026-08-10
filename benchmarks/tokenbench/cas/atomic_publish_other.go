//go:build !linux

package cas

import (
	"errors"
	"os"
)

func atomicNoReplaceSupported() bool {
	// Fail closed instead of emulating no-replace with a check followed by a
	// replacement-capable rename or a transient multi-link publication.
	return false
}

func renameNoReplace(
	_ *os.File,
	_ string,
	_ *os.File,
	_ string,
) error {
	return errors.New("atomic no-replace rename is unsupported on this platform")
}

func (store *Store) probeAtomicNoReplace() error {
	return errors.New("atomic no-replace rename is unsupported on this platform")
}
