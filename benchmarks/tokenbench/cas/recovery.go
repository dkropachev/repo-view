package cas

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"sort"
)

type staleTransaction struct {
	name    string
	info    os.FileInfo
	objects []staleObject
}

type staleObject struct {
	info os.FileInfo
	name string
}

// RecoverStale removes transaction directories abandoned by processes that no
// longer hold a live transaction lease. It never races a live CAS transaction:
// if any transaction is active, it returns ErrTransactionsActive without
// scanning or removing staging entries. Recovery validates every entry before
// deleting any of them and refuses unrecognized names or filesystem objects.
func (store *Store) RecoverStale() (resultErr error) {
	store.stateMu.RLock()
	defer store.stateMu.RUnlock()
	if store.closed {
		return errors.New("CAS store is closed")
	}
	if err := store.validateRootBinding(); err != nil {
		return err
	}

	staging, stagingInfo, err := openPrivateDirectory(store.root, "staging")
	if err != nil {
		return err
	}
	defer func() {
		resultErr = joinCloseError(resultErr, "close CAS staging after recovery", staging)
	}()
	lock, lockInfo, err := openTransactionLock(staging)
	if err != nil {
		return err
	}
	if !sameFilesystem(stagingInfo, lockInfo) {
		return joinCloseError(
			errors.New("CAS transaction lock is not on the staging filesystem"),
			"close misplaced CAS transaction lock",
			lock,
		)
	}
	locked, err := tryLockTransactionExclusive(lock)
	if err != nil {
		return joinCloseError(
			fmt.Errorf("lock stale CAS recovery: %w", err),
			"close CAS transaction lock after lock failure",
			lock,
		)
	}
	if !locked {
		return joinCloseError(
			ErrTransactionsActive,
			"close active CAS transaction lock probe",
			lock,
		)
	}
	defer func() {
		resultErr = errors.Join(
			resultErr,
			wrapError("unlock stale CAS recovery", unlockTransaction(lock)),
			wrapError("close stale CAS recovery lock", lock.Close()),
		)
	}()
	if err := validateCanonicalTransactionLock(staging, lockInfo); err != nil {
		return err
	}

	transactions, err := store.inspectStaleTransactions(staging, stagingInfo)
	if err != nil {
		return err
	}
	for _, transaction := range transactions {
		if err := store.removeStaleTransaction(staging, transaction); err != nil {
			return err
		}
	}
	if err := syncRoot(staging); err != nil {
		return err
	}
	return store.validateRootBinding()
}

func (store *Store) inspectStaleTransactions(
	staging *os.Root,
	stagingInfo os.FileInfo,
) ([]staleTransaction, error) {
	names, err := rootEntryNames(staging, hardMaxStaleTransactions+1)
	if err != nil {
		return nil, fmt.Errorf("list CAS staging for recovery: %w", err)
	}
	transactions := make([]staleTransaction, 0, len(names))
	for _, name := range names {
		if name == transactionLockName {
			continue
		}
		if len(transactions) >= hardMaxStaleTransactions {
			return nil, fmt.Errorf(
				"%w: CAS staging exceeds %d stale transactions",
				ErrTooLarge,
				hardMaxStaleTransactions,
			)
		}
		if !validRandomEntryName(name, "tx-") {
			return nil, fmt.Errorf("%w: unrecognized staging entry %q", ErrIntegrity, name)
		}
		transactionRoot, transactionInfo, err := openPrivateDirectory(staging, name)
		if err != nil {
			return nil, err
		}
		if !sameFilesystem(stagingInfo, transactionInfo) {
			return nil, joinCloseError(
				fmt.Errorf("%w: stale transaction is on another filesystem", ErrIntegrity),
				"close stale transaction on another filesystem",
				transactionRoot,
			)
		}
		objectNames, err := rootEntryNames(transactionRoot, hardMaxStaleObjects)
		if err != nil {
			return nil, joinCloseError(
				fmt.Errorf("list stale transaction %s: %w", name, err),
				"close stale transaction after listing failure",
				transactionRoot,
			)
		}
		transaction := staleTransaction{name: name, info: transactionInfo}
		for _, objectName := range objectNames {
			if !validRandomEntryName(objectName, "object-") {
				return nil, joinCloseError(
					fmt.Errorf(
						"%w: unrecognized entry %q in stale transaction %s",
						ErrIntegrity,
						objectName,
						name,
					),
					"close stale transaction containing unrecognized entry",
					transactionRoot,
				)
			}
			objectInfo, err := transactionRoot.Lstat(objectName)
			if err != nil {
				return nil, joinCloseError(
					fmt.Errorf("lstat stale transaction object: %w", err),
					"close stale transaction after object lstat failure",
					transactionRoot,
				)
			}
			if err := store.validateStaleObject(objectName, objectInfo, transactionInfo); err != nil {
				return nil, joinCloseError(
					err,
					"close stale transaction containing invalid object",
					transactionRoot,
				)
			}
			transaction.objects = append(transaction.objects, staleObject{
				name: objectName,
				info: objectInfo,
			})
		}
		if err := transactionRoot.Close(); err != nil {
			return nil, fmt.Errorf("close inspected stale transaction: %w", err)
		}
		transactions = append(transactions, transaction)
	}
	return transactions, nil
}

func (store *Store) removeStaleTransaction(
	staging *os.Root,
	transaction staleTransaction,
) error {
	transactionRoot, transactionInfo, err := openPrivateDirectory(staging, transaction.name)
	if benignStaleTransactionAbsence(err) {
		return nil
	}
	if err != nil {
		return err
	}
	if !os.SameFile(transaction.info, transactionInfo) {
		return joinCloseError(
			fmt.Errorf("%w: stale transaction changed before cleanup", ErrIntegrity),
			"close changed stale transaction",
			transactionRoot,
		)
	}
	for _, object := range transaction.objects {
		current, err := transactionRoot.Lstat(object.name)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil || !os.SameFile(object.info, current) {
			return joinCloseError(
				errors.Join(
					fmt.Errorf("%w: stale object %s changed before cleanup", ErrIntegrity, object.name),
					err,
				),
				"close stale transaction containing changed object",
				transactionRoot,
			)
		}
		if err := store.validateStaleObject(object.name, current, transactionInfo); err != nil {
			return joinCloseError(
				err,
				"close stale transaction containing invalid object",
				transactionRoot,
			)
		}
		if err := transactionRoot.Remove(object.name); err != nil {
			return joinCloseError(
				fmt.Errorf("remove stale object %s: %w", object.name, err),
				"close stale transaction after removal failure",
				transactionRoot,
			)
		}
	}
	if err := syncRoot(transactionRoot); err != nil {
		return joinCloseError(
			fmt.Errorf("sync stale transaction cleanup: %w", err),
			"close stale transaction after sync failure",
			transactionRoot,
		)
	}
	if err := transactionRoot.Close(); err != nil {
		return fmt.Errorf("close stale transaction cleanup root: %w", err)
	}
	return removeDirectoryIfSame(staging, transaction.name, transaction.info)
}

func (store *Store) validateStaleObject(
	name string,
	info os.FileInfo,
	transactionInfo os.FileInfo,
) error {
	if info.Mode()&os.ModeSymlink != 0 || !info.Mode().IsRegular() {
		return fmt.Errorf("%w: stale object %s is not a regular file", ErrIntegrity, name)
	}
	mode := info.Mode().Perm()
	if mode&^stagedFileMode != 0 {
		return fmt.Errorf("%w: stale object %s has invalid mode %s", ErrIntegrity, name, mode)
	}
	if multipleLinks(info) {
		return fmt.Errorf("%w: stale object %s has multiple links", ErrIntegrity, name)
	}
	if !sameFilesystem(transactionInfo, info) {
		return fmt.Errorf("%w: stale object %s is on another filesystem", ErrIntegrity, name)
	}
	return nil
}

func benignStaleTransactionAbsence(err error) bool {
	return errors.Is(err, fs.ErrNotExist) && !errors.Is(err, ErrIntegrity)
}

func rootEntryNames(root *os.Root, maximum int) ([]string, error) {
	if maximum <= 0 {
		return nil, errors.New("directory entry limit must be positive")
	}
	directory, err := root.Open(".")
	if err != nil {
		return nil, err
	}
	names := make([]string, 0, maximum)
	var readErr error
	for len(names) <= maximum {
		var entries []fs.DirEntry
		entries, readErr = directory.ReadDir(minimumInt(64, maximum+1-len(names)))
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		if readErr != nil || len(entries) == 0 {
			break
		}
	}
	closeErr := directory.Close()
	if readErr != nil && !errors.Is(readErr, io.EOF) || closeErr != nil {
		return nil, errors.Join(readErr, closeErr)
	}
	if len(names) > maximum {
		return nil, fmt.Errorf("%w: directory contains more than %d entries", ErrTooLarge, maximum)
	}
	sort.Strings(names)
	return names, nil
}

func minimumInt(left, right int) int {
	if left < right {
		return left
	}
	return right
}

func validRandomEntryName(name string, prefix string) bool {
	if len(name) != len(prefix)+32 || name[:len(prefix)] != prefix {
		return false
	}
	for _, character := range name[len(prefix):] {
		if !isLowerHex(character) {
			return false
		}
	}
	return true
}
