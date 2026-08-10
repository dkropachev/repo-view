//go:build linux

package cas

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"
)

func atomicNoReplaceSupported() bool {
	return true
}

func renameNoReplace(
	sourceDirectory *os.File,
	sourceName string,
	destinationDirectory *os.File,
	destinationName string,
) error {
	return unix.Renameat2(
		int(sourceDirectory.Fd()),
		sourceName,
		int(destinationDirectory.Fd()),
		destinationName,
		unix.RENAME_NOREPLACE,
	)
}

// probeAtomicNoReplace exercises the primitive on the actual staging
// filesystem. A Linux build alone is not sufficient: older kernels and some
// network/FUSE filesystems reject RENAME_NOREPLACE at runtime.
func (store *Store) probeAtomicNoReplace() (resultErr error) {
	transaction, err := store.Begin()
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, transaction.Abort()) }()

	first, err := transaction.Put(
		context.Background(),
		"application/vnd.tokenbench.cas-probe",
		bytes.NewReader([]byte{1}),
	)
	if err != nil {
		return err
	}
	firstStaged := transaction.staged[first.hexDigest()]
	firstInfo := transaction.owned[firstStaged.name]
	firstPin := transaction.ownedPins[firstStaged.name]
	destinationName, err := randomEntryName("object-")
	if err != nil {
		return err
	}
	directory, err := transaction.root.Open(".")
	if err != nil {
		return err
	}
	renameErr := renameNoReplace(
		directory,
		firstStaged.name,
		directory,
		destinationName,
	)
	closeErr := directory.Close()
	if renameErr != nil || closeErr != nil {
		return errors.Join(
			fmt.Errorf("rename to absent probe destination: %w", renameErr),
			closeErr,
		)
	}
	delete(transaction.owned, firstStaged.name)
	delete(transaction.ownedPins, firstStaged.name)
	transaction.owned[destinationName] = firstInfo
	transaction.ownedPins[destinationName] = firstPin
	firstStaged.name = destinationName

	second, err := transaction.Put(
		context.Background(),
		"application/vnd.tokenbench.cas-probe",
		bytes.NewReader([]byte{2}),
	)
	if err != nil {
		return err
	}
	third, err := transaction.Put(
		context.Background(),
		"application/vnd.tokenbench.cas-probe",
		bytes.NewReader([]byte{3}),
	)
	if err != nil {
		return err
	}
	secondName := transaction.staged[second.hexDigest()].name
	thirdName := transaction.staged[third.hexDigest()].name
	secondBefore := transaction.owned[secondName]
	thirdBefore := transaction.owned[thirdName]
	directory, err = transaction.root.Open(".")
	if err != nil {
		return err
	}
	renameErr = renameNoReplace(directory, secondName, directory, thirdName)
	closeErr = directory.Close()
	if !errors.Is(renameErr, fs.ErrExist) || closeErr != nil {
		return errors.Join(
			fmt.Errorf("probe collision returned %w, want destination-exists", renameErr),
			closeErr,
		)
	}
	secondAfter, secondErr := transaction.root.Lstat(secondName)
	thirdAfter, thirdErr := transaction.root.Lstat(thirdName)
	if secondErr != nil || thirdErr != nil ||
		!os.SameFile(secondBefore, secondAfter) || !os.SameFile(thirdBefore, thirdAfter) {
		return errors.Join(
			fmt.Errorf("%w: no-replace probe changed a collision path", ErrIntegrity),
			secondErr,
			thirdErr,
		)
	}
	return nil
}
