package cas

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

type stagedObject struct {
	refs map[ObjectRef]struct{}
	name string
	size int64
}

// Transaction stages complete immutable objects and publishes one designated
// root after every other staged object. A transaction is consumed by Commit,
// whether Commit succeeds or fails.
type Transaction struct {
	rootInfo         os.FileInfo
	staged           map[string]*stagedObject
	ownedPins        map[string]*os.File
	root             *os.Root
	store            *Store
	lease            *os.File
	owned            map[string]os.FileInfo
	directory        string
	putCount         int
	stagedBytes      int64
	mu               sync.Mutex
	consumed         bool
	cleanupComplete  bool
	directoryRemoved bool
	rootPublished    bool
}

// Put streams one object into private staging, enforcing the store's byte
// bound while computing its SHA-256 digest. Put does not close source.
func (transaction *Transaction) Put(
	ctx context.Context,
	mediaType string,
	source io.Reader,
) (ObjectRef, error) {
	transaction.mu.Lock()
	defer transaction.mu.Unlock()
	if transaction.consumed {
		return ObjectRef{}, ErrTransactionClosed
	}
	if source == nil {
		return ObjectRef{}, errors.New("CAS object source is nil")
	}
	if transaction.putCount >= hardMaxTransactionPuts {
		return ObjectRef{}, fmt.Errorf(
			"%w: transaction exceeds %d Put operations",
			ErrTooLarge,
			hardMaxTransactionPuts,
		)
	}
	transaction.putCount++
	if !validMediaType(mediaType) {
		return ObjectRef{}, fmt.Errorf(
			"%w: media type must be a lowercase type/subtype without parameters",
			ErrInvalidObjectRef,
		)
	}
	if err := ctx.Err(); err != nil {
		return ObjectRef{}, err
	}

	name, object, err := transaction.createStagedFile()
	if err != nil {
		return ObjectRef{}, err
	}
	createdInfo, err := object.Stat()
	if err != nil {
		removeErr := transaction.root.Remove(name)
		if removeErr == nil {
			removeErr = syncRoot(transaction.root)
		}
		return ObjectRef{}, errors.Join(
			fmt.Errorf("stat new staged CAS object: %w", err),
			object.Close(),
			removeErr,
		)
	}
	// Duplicate the descriptor returned by O_CREATE|O_EXCL instead of opening
	// the pathname again. The duplicate pins the exact inode without following
	// a subsequently substituted symlink or blocking on a substituted FIFO.
	pin, err := pinStagedFile(object)
	if err != nil {
		removeErr := removeFileIfSame(transaction.root, name, createdInfo)
		closeErr := object.Close()
		return ObjectRef{}, errors.Join(
			fmt.Errorf("pin new staged CAS object: %w", err),
			removeErr,
			closeErr,
		)
	}
	pinnedInfo, pinErr := pin.Stat()
	if pinErr != nil || !os.SameFile(createdInfo, pinnedInfo) {
		removeErr := removeFileIfSame(transaction.root, name, createdInfo)
		pinCloseErr := pin.Close()
		objectCloseErr := object.Close()
		return ObjectRef{}, errors.Join(
			fmt.Errorf("%w: staged CAS object changed while pinning", ErrIntegrity),
			pinErr,
			removeErr,
			pinCloseErr,
			objectCloseErr,
		)
	}
	transaction.owned[name] = createdInfo
	transaction.ownedPins[name] = pin

	hexDigest, size, writeErr := writeBounded(
		ctx,
		object,
		source,
		transaction.store.maxObjectBytes,
	)
	if writeErr == nil {
		writeErr = object.Chmod(objectFileMode)
	}
	if writeErr == nil {
		writeErr = object.Sync()
	}
	openedInfo, statErr := object.Stat()
	if writeErr == nil && statErr != nil {
		writeErr = statErr
	}
	closeErr := object.Close()
	if writeErr == nil && closeErr != nil {
		writeErr = closeErr
	}
	if writeErr != nil {
		return ObjectRef{}, errors.Join(
			writeErr,
			transaction.removeOwnedLocked(name),
		)
	}
	if err := validateObjectInfo(name, openedInfo); err != nil {
		return ObjectRef{}, errors.Join(err, transaction.removeOwnedLocked(name))
	}
	transaction.owned[name] = openedInfo
	stagedInfo, err := transaction.root.Lstat(name)
	if err != nil || !os.SameFile(openedInfo, stagedInfo) ||
		!sameObjectMetadata(openedInfo, stagedInfo) {
		return ObjectRef{}, errors.Join(
			fmt.Errorf("%w: staged object changed after close", ErrIntegrity),
			transaction.removeOwnedLocked(name),
		)
	}
	if err := syncRoot(transaction.root); err != nil {
		return ObjectRef{}, errors.Join(err, transaction.removeOwnedLocked(name))
	}

	ref := ObjectRef{
		Digest:    digestPrefix + hexDigest,
		Size:      size,
		MediaType: mediaType,
	}
	if existing, ok := transaction.staged[hexDigest]; ok {
		if existing.size != size {
			return ObjectRef{}, errors.Join(
				fmt.Errorf("%w: staged digest has inconsistent sizes", ErrIntegrity),
				transaction.removeOwnedLocked(name),
			)
		}
		if err := verifyFileAt(ctx, transaction.root, existing.name, ref, io.Discard); err != nil {
			return ObjectRef{}, errors.Join(err, transaction.removeOwnedLocked(name))
		}
		existing.refs[ref] = struct{}{}
		if err := transaction.removeOwnedLocked(name); err != nil {
			return ObjectRef{}, err
		}
		return ref, nil
	}
	if size > hardMaxTransactionBytes-transaction.stagedBytes {
		return ObjectRef{}, errors.Join(
			fmt.Errorf(
				"%w: transaction staged bytes exceed %d",
				ErrTooLarge,
				hardMaxTransactionBytes,
			),
			transaction.removeOwnedLocked(name),
		)
	}
	transaction.staged[hexDigest] = &stagedObject{
		name: name,
		size: size,
		refs: map[ObjectRef]struct{}{ref: {}},
	}
	transaction.stagedBytes += size
	return ref, nil
}

// Commit preserves the original error-only API. New publication code should
// use CommitDetailed so visibility, durability, and uncertainty are not
// inferred from errors.Is checks.
func (transaction *Transaction) Commit(ctx context.Context, root ObjectRef) error {
	_, err := transaction.CommitDetailed(ctx, root)
	return err
}

// CommitDetailed publishes every staged object other than root in digest
// order, then root last, and returns a typed outcome even on error.
func (transaction *Transaction) CommitDetailed(
	ctx context.Context,
	root ObjectRef,
) (CommitResult, error) {
	transaction.mu.Lock()
	defer transaction.mu.Unlock()
	result := CommitResult{Root: root, State: CommitRetryable}
	if transaction.consumed {
		return result, ErrTransactionClosed
	}
	transaction.consumed = true

	transaction.store.stateMu.RLock()
	defer transaction.store.stateMu.RUnlock()
	if transaction.store.closed {
		return transaction.finishDetailed(result, errors.New("CAS store is closed"))
	}
	if err := transaction.store.validateRootBinding(); err != nil {
		result.State = CommitIndeterminate
		uncertain := root
		result.UncertainObject = &uncertain
		result.UncertainStage = "cas_root_binding_before_publication"
		return transaction.finishDetailed(result, err)
	}
	if err := transaction.store.validateRef(root); err != nil {
		return transaction.finishDetailed(result, err)
	}
	hexRoot := root.hexDigest()
	rootObject, ok := transaction.staged[hexRoot]
	if !ok {
		return transaction.finishDetailed(result,
			errors.New("CAS transaction root was not staged by this transaction"),
		)
	}
	if _, ok := rootObject.refs[root]; !ok {
		return transaction.finishDetailed(result,
			errors.New("CAS transaction root reference does not match a staged reference"),
		)
	}

	digests := make([]string, 0, len(transaction.staged)-1)
	for digest := range transaction.staged {
		if digest != hexRoot {
			digests = append(digests, digest)
		}
	}
	sort.Strings(digests)

	transaction.store.publishMu.Lock()
	defer transaction.store.publishMu.Unlock()
	for _, digest := range digests {
		if err := ctx.Err(); err != nil {
			return transaction.finishDetailed(result, err)
		}
		staged := transaction.staged[digest]
		ref := oneRef(staged.refs)
		outcome, err := transaction.publishLocked(ctx, ref, staged)
		if err != nil {
			if outcome.indeterminate {
				uncertain := ref
				result.State = CommitIndeterminate
				result.UncertainObject = &uncertain
				result.UncertainStage = outcome.stage
			}
			return transaction.finishDetailed(result, err)
		}
		if transaction.store.afterPublish != nil {
			transaction.store.afterPublish(ref)
		}
	}
	if err := ctx.Err(); err != nil {
		return transaction.finishDetailed(result, err)
	}
	outcome, err := transaction.publishLocked(ctx, root, rootObject)
	transaction.rootPublished = outcome.visible
	result.Durable = outcome.durable
	switch {
	case outcome.indeterminate:
		result.State = CommitIndeterminate
		uncertain := root
		result.UncertainObject = &uncertain
		result.UncertainStage = outcome.stage
	case outcome.visible && outcome.durable:
		result.State = CommitDurable
	case outcome.visible:
		result.State = CommitVisible
	default:
		result.State = CommitRetryable
	}
	if err != nil {
		return transaction.finishDetailed(result, err)
	}
	if transaction.store.afterPublish != nil {
		transaction.store.afterPublish(root)
	}
	if err := transaction.store.validateRootBinding(); err != nil {
		result.State = CommitIndeterminate
		result.Durable = false
		uncertain := root
		result.UncertainObject = &uncertain
		result.UncertainStage = "cas_root_binding_after_publication"
		return transaction.finishDetailed(result, err)
	}
	return transaction.finishDetailed(result, nil)
}

// Abort removes only this transaction's files and then its directory. It never
// recursively removes unrecognized entries. Abort may be called after a failed
// Commit, and may itself be retried until cleanup succeeds.
func (transaction *Transaction) Abort() error {
	transaction.mu.Lock()
	defer transaction.mu.Unlock()
	if transaction.cleanupComplete {
		return nil
	}
	transaction.consumed = true
	transaction.store.stateMu.RLock()
	defer transaction.store.stateMu.RUnlock()
	err := transaction.cleanupLocked()
	if err != nil {
		return errors.Join(ErrCleanupPending, err)
	}
	return nil
}

func (transaction *Transaction) createStagedFile() (string, *os.File, error) {
	for range 32 {
		name, err := randomEntryName("object-")
		if err != nil {
			return "", nil, err
		}
		object, err := transaction.root.OpenFile(
			name,
			os.O_WRONLY|os.O_CREATE|os.O_EXCL,
			stagedFileMode,
		)
		if err == nil {
			return name, object, nil
		}
		if !errors.Is(err, fs.ErrExist) {
			return "", nil, fmt.Errorf("create staged CAS object: %w", err)
		}
	}
	return "", nil, errors.New("could not allocate a unique staged CAS object")
}

type objectPublication struct {
	stage         string
	visible       bool
	durable       bool
	indeterminate bool
}

func (transaction *Transaction) publishLocked(
	ctx context.Context,
	ref ObjectRef,
	staged *stagedObject,
) (outcome objectPublication, resultErr error) {
	sourceInfo, err := transaction.ownedSourceInfoLocked(staged.name)
	if err != nil {
		return objectPublication{}, fmt.Errorf("validate staged object inode pin: %w", err)
	}
	if err := verifyFileAt(ctx, transaction.root, staged.name, ref, io.Discard); err != nil {
		return objectPublication{}, fmt.Errorf("verify staged object: %w", err)
	}
	verifiedInfo, err := transaction.ownedSourceInfoLocked(staged.name)
	if err != nil || !os.SameFile(sourceInfo, verifiedInfo) ||
		!sameObjectMetadata(sourceInfo, verifiedInfo) {
		return objectPublication{}, errors.Join(
			fmt.Errorf("%w: staged object changed while verifying", ErrIntegrity),
			err,
		)
	}

	hexDigest := ref.hexDigest()
	shard, shardInfo, err := transaction.store.openShard(hexDigest, true)
	if err != nil {
		return objectPublication{}, err
	}
	defer func() {
		resultErr = joinCloseError(resultErr, "close object shard after publication", shard)
	}()
	if !sameFilesystem(transaction.rootInfo, shardInfo) {
		return objectPublication{}, errors.New("CAS transaction and destination shard are not on the same filesystem")
	}
	if err := transaction.validatePublicationPaths(
		shardInfo,
		hexDigest,
		staged.name,
		sourceInfo,
	); err != nil {
		return objectPublication{}, err
	}

	sourceDirectory, err := transaction.root.Open(".")
	if err != nil {
		return objectPublication{}, fmt.Errorf("open transaction directory for publication: %w", err)
	}
	destinationDirectory, err := shard.Open(".")
	if err != nil {
		return objectPublication{}, joinCloseError(
			fmt.Errorf("open object shard for publication: %w", err),
			"close transaction directory after destination open failure",
			sourceDirectory,
		)
	}
	rename := renameNoReplaceFunc(renameNoReplace)
	if transaction.store.publicationRename != nil {
		rename = transaction.store.publicationRename
	}
	err = rename(sourceDirectory, staged.name, destinationDirectory, hexDigest[2:])
	sourceCloseErr := sourceDirectory.Close()
	destinationCloseErr := destinationDirectory.Close()
	closeErr := errors.Join(sourceCloseErr, destinationCloseErr)
	if errors.Is(err, fs.ErrExist) {
		if err := verifyExistingObject(ctx, shard, hexDigest[2:], ref); err != nil {
			return objectPublication{}, fmt.Errorf("verify existing object before deduplication: %w", err)
		}
		durabilityErr := errors.Join(
			syncObjectAt(shard, hexDigest[2:]),
			wrapError("sync existing object shard before deduplication", syncRoot(shard)),
		)
		if durabilityErr != nil {
			if canonicalErr := transaction.validateCanonicalShard(shardInfo, hexDigest); canonicalErr != nil {
				return objectPublication{
					indeterminate: true,
					stage:         "deduplicated_object_durability",
				}, errors.Join(ErrPublicationUnknown, durabilityErr, canonicalErr)
			}
			return objectPublication{visible: true}, errors.Join(durabilityErr, closeErr)
		}
		if err := transaction.validateCanonicalShard(shardInfo, hexDigest); err != nil {
			return objectPublication{
				indeterminate: true,
				stage:         "deduplicated_object_canonical_path",
			}, errors.Join(ErrPublicationUnknown, err, closeErr)
		}
		if err := transaction.removeOwnedLocked(staged.name); err != nil {
			return objectPublication{visible: true, durable: true}, err
		}
		delete(transaction.staged, hexDigest)
		if closeErr != nil {
			return objectPublication{visible: true, durable: true}, fmt.Errorf("close publication directories: %w", closeErr)
		}
		return objectPublication{visible: true, durable: true}, nil
	}

	var publicationErr error
	if err != nil {
		moved, known, inspectErr := transaction.inspectRenameOutcome(
			staged.name,
			hexDigest[2:],
			sourceInfo,
			shard,
		)
		if !known {
			return objectPublication{
					indeterminate: true,
					stage:         "atomic_rename",
				}, errors.Join(
					ErrPublicationUnknown,
					fmt.Errorf("publish CAS object without replacement: %w", err),
					inspectErr,
					closeErr,
				)
		}
		if !moved {
			return objectPublication{}, errors.Join(
				fmt.Errorf("publish CAS object without replacement: %w", err),
				inspectErr,
				closeErr,
			)
		}
		publicationErr = errors.Join(
			fmt.Errorf("atomic publication moved the object but reported an error: %w", err),
			closeErr,
		)
	} else if closeErr != nil {
		publicationErr = fmt.Errorf("close publication directories: %w", closeErr)
	}
	// From this point the rename is known to have moved our inode. Keep its
	// descriptor pinned until every canonical-path, digest, and durability
	// check has completed so inode-number reuse cannot create an ABA match.
	defer func() {
		resultErr = errors.Join(
			resultErr,
			transaction.releaseOwnedPinLocked(staged.name),
		)
	}()
	delete(transaction.staged, hexDigest)

	if err := transaction.confirmCanonicalPublication(
		shardInfo,
		hexDigest,
		shard,
		sourceInfo,
	); err != nil {
		return objectPublication{
				indeterminate: true,
				stage:         "canonical_publication",
			}, errors.Join(
				ErrPublicationUnknown,
				publicationErr,
				err,
			)
	}
	if transaction.store.afterAtomicPublish != nil {
		if err := transaction.store.afterAtomicPublish(ref); err != nil {
			publishErr := errors.Join(publicationErr, err)
			if canonicalErr := transaction.confirmCanonicalPublication(
				shardInfo,
				hexDigest,
				shard,
				sourceInfo,
			); canonicalErr != nil {
				return objectPublication{
					indeterminate: true,
					stage:         "canonical_publication_after_rename",
				}, errors.Join(ErrPublicationUnknown, publishErr, canonicalErr)
			}
			return objectPublication{visible: true}, publishErr
		}
	}
	destinationSyncErr := syncRoot(shard)
	sourceSyncErr := syncRoot(transaction.root)
	if destinationSyncErr != nil || sourceSyncErr != nil {
		syncErr := errors.Join(
			publicationErr,
			wrapError("sync published object shard", destinationSyncErr),
			wrapError("sync transaction directory after publication", sourceSyncErr),
		)
		if err := transaction.confirmCanonicalPublication(
			shardInfo,
			hexDigest,
			shard,
			sourceInfo,
		); err != nil {
			return objectPublication{
				indeterminate: true,
				stage:         "canonical_publication_after_sync",
			}, errors.Join(ErrPublicationUnknown, syncErr, err)
		}
		return objectPublication{
			visible: true,
			durable: destinationSyncErr == nil,
		}, syncErr
	}
	if err := verifyFileAt(ctx, shard, hexDigest[2:], ref, io.Discard); err != nil {
		publishErr := errors.Join(publicationErr, err)
		if canonicalErr := transaction.confirmCanonicalPublication(
			shardInfo,
			hexDigest,
			shard,
			sourceInfo,
		); canonicalErr != nil {
			return objectPublication{
				indeterminate: true,
				stage:         "canonical_publication_after_verification",
			}, errors.Join(ErrPublicationUnknown, publishErr, canonicalErr)
		}
		return objectPublication{visible: true, durable: true}, publishErr
	}
	if err := transaction.confirmCanonicalPublication(
		shardInfo,
		hexDigest,
		shard,
		sourceInfo,
	); err != nil {
		return objectPublication{
			indeterminate: true,
			stage:         "canonical_publication_final",
		}, errors.Join(ErrPublicationUnknown, publicationErr, err)
	}
	return objectPublication{visible: true, durable: true}, publicationErr
}

func (transaction *Transaction) ownedSourceInfoLocked(name string) (os.FileInfo, error) {
	expected, ok := transaction.owned[name]
	if !ok {
		return nil, fmt.Errorf("%w: staged object %s is not transaction-owned", ErrIntegrity, name)
	}
	pin := transaction.ownedPins[name]
	if pin == nil {
		return nil, fmt.Errorf("%w: staged object %s lost its inode pin", ErrIntegrity, name)
	}
	pinned, err := pin.Stat()
	if err != nil {
		return nil, fmt.Errorf("stat staged object %s inode pin: %w", name, err)
	}
	if !os.SameFile(expected, pinned) || !sameObjectMetadata(expected, pinned) {
		return nil, fmt.Errorf("%w: staged object %s inode pin changed", ErrIntegrity, name)
	}
	current, err := transaction.root.Lstat(name)
	if err != nil {
		return nil, fmt.Errorf("lstat staged object %s: %w", name, err)
	}
	if !os.SameFile(pinned, current) || !sameObjectMetadata(pinned, current) {
		return nil, fmt.Errorf("%w: staged object %s pathname differs from its inode pin", ErrIntegrity, name)
	}
	if err := validateObjectInfo(name, current); err != nil {
		return nil, err
	}
	return pinned, nil
}

func (transaction *Transaction) inspectRenameOutcome(
	sourceName string,
	destinationName string,
	sourceInfo os.FileInfo,
	shard *os.Root,
) (moved bool, known bool, err error) {
	destinationInfo, destinationErr := shard.Lstat(destinationName)
	if destinationErr == nil && os.SameFile(sourceInfo, destinationInfo) {
		if err := validateObjectInfo(destinationName, destinationInfo); err != nil {
			return false, false, err
		}
		return true, true, nil
	}

	currentSource, sourceErr := transaction.root.Lstat(sourceName)
	if sourceErr == nil && os.SameFile(sourceInfo, currentSource) {
		if err := validateObjectInfo(sourceName, currentSource); err != nil {
			return false, false, err
		}
		return false, true, destinationErr
	}
	return false, false, errors.Join(
		pathInspectionError("staged", sourceErr),
		pathInspectionError("destination", destinationErr),
	)
}

func pathInspectionError(pathKind string, err error) error {
	if err != nil {
		return fmt.Errorf("inspect %s path after publication error: %w", pathKind, err)
	}
	return fmt.Errorf("%w: %s path no longer identifies the expected inode", ErrIntegrity, pathKind)
}

func wrapError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", operation, err)
}

func (transaction *Transaction) validatePublicationPaths(
	shardInfo os.FileInfo,
	hexDigest string,
	stagedName string,
	sourceInfo os.FileInfo,
) error {
	transactionPath := filepath.Join("staging", transaction.directory)
	currentTransaction, err := transaction.store.root.Lstat(transactionPath)
	if err != nil || !os.SameFile(transaction.rootInfo, currentTransaction) {
		return fmt.Errorf("%w: transaction directory changed before publication", ErrIntegrity)
	}
	if err := transaction.validateCanonicalShard(shardInfo, hexDigest); err != nil {
		return err
	}

	currentSource, err := transaction.store.root.Lstat(
		filepath.Join(transactionPath, stagedName),
	)
	if err != nil || !os.SameFile(sourceInfo, currentSource) {
		return fmt.Errorf("%w: staged object path changed before publication", ErrIntegrity)
	}
	return nil
}

func (transaction *Transaction) validateCanonicalShard(
	shardInfo os.FileInfo,
	hexDigest string,
) error {
	shardPath := filepath.Join("objects", "sha256", hexDigest[:2])
	currentShard, err := transaction.store.root.Lstat(shardPath)
	if err != nil || !os.SameFile(shardInfo, currentShard) {
		return fmt.Errorf("%w: canonical object shard changed during publication", ErrIntegrity)
	}
	return validatePrivateDirectoryInfo(shardPath, currentShard)
}

func (transaction *Transaction) confirmCanonicalPublication(
	shardInfo os.FileInfo,
	hexDigest string,
	shard *os.Root,
	sourceInfo os.FileInfo,
) error {
	if err := transaction.validateCanonicalShard(shardInfo, hexDigest); err != nil {
		return err
	}
	destinationName := hexDigest[2:]
	destinationInfo, err := shard.Lstat(destinationName)
	if err != nil || !os.SameFile(sourceInfo, destinationInfo) {
		return fmt.Errorf("%w: published object does not match staged inode", ErrIntegrity)
	}
	if err := validateObjectInfo(destinationName, destinationInfo); err != nil {
		return err
	}
	if !sameObjectMetadata(sourceInfo, destinationInfo) {
		return fmt.Errorf("%w: published object metadata changed", ErrIntegrity)
	}
	return nil
}

func verifyExistingObject(
	ctx context.Context,
	shard *os.Root,
	name string,
	ref ObjectRef,
) error {
	return verifyFileAt(ctx, shard, name, ref, io.Discard)
}

func (transaction *Transaction) removeOwnedLocked(name string) error {
	expected, ok := transaction.owned[name]
	if !ok {
		return nil
	}
	pin := transaction.ownedPins[name]
	if pin == nil {
		return fmt.Errorf("%w: owned transaction file %s lost its inode pin", ErrIntegrity, name)
	}
	pinned, pinErr := pin.Stat()
	if pinErr != nil || !os.SameFile(expected, pinned) {
		return errors.Join(
			fmt.Errorf("%w: owned transaction file %s inode pin changed", ErrIntegrity, name),
			pinErr,
		)
	}
	current, err := transaction.root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return transaction.releaseOwnedPinLocked(name)
	}
	if err != nil {
		return fmt.Errorf("lstat owned transaction file %s: %w", name, err)
	}
	if !os.SameFile(pinned, current) {
		return fmt.Errorf(
			"%w: owned transaction file %s changed before cleanup",
			ErrIntegrity,
			name,
		)
	}
	if err := transaction.root.Remove(name); err != nil {
		return fmt.Errorf("remove owned transaction file %s: %w", name, err)
	}
	closeErr := transaction.releaseOwnedPinLocked(name)
	if err := syncRoot(transaction.root); err != nil {
		return errors.Join(
			closeErr,
			fmt.Errorf("sync transaction directory after removing %s: %w", name, err),
		)
	}
	return closeErr
}

func (transaction *Transaction) releaseOwnedPinLocked(name string) error {
	pin := transaction.ownedPins[name]
	delete(transaction.ownedPins, name)
	delete(transaction.owned, name)
	if pin == nil {
		return nil
	}
	if err := pin.Close(); err != nil {
		return fmt.Errorf("close owned transaction file %s inode pin: %w", name, err)
	}
	return nil
}

func removeFileIfSame(root *os.Root, name string, expected os.FileInfo) error {
	current, err := root.Lstat(name)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("lstat staged CAS object %s for cleanup: %w", name, err)
	}
	if !os.SameFile(expected, current) {
		return fmt.Errorf("%w: staged CAS object %s changed before cleanup", ErrIntegrity, name)
	}
	if err := root.Remove(name); err != nil {
		return fmt.Errorf("remove staged CAS object %s: %w", name, err)
	}
	return syncRoot(root)
}

func (transaction *Transaction) cleanupLocked() (resultErr error) {
	if transaction.cleanupComplete {
		return nil
	}
	if transaction.store.beforeCleanup != nil {
		if err := transaction.store.beforeCleanup(); err != nil {
			return fmt.Errorf("before transaction cleanup: %w", err)
		}
	}
	var cleanupErrors []error
	names := make([]string, 0, len(transaction.owned))
	for name := range transaction.owned {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if err := transaction.removeOwnedLocked(name); err != nil {
			cleanupErrors = append(cleanupErrors, err)
		}
	}
	if len(cleanupErrors) != 0 {
		return errors.Join(cleanupErrors...)
	}
	if transaction.root != nil {
		root := transaction.root
		transaction.root = nil
		if err := root.Close(); err != nil {
			return fmt.Errorf("close transaction root: %w", err)
		}
	}

	staging, _, err := openPrivateDirectory(transaction.store.root, "staging")
	if err != nil {
		cleanupErrors = append(cleanupErrors, err)
		return errors.Join(cleanupErrors...)
	}
	stagingOpen := true
	defer func() {
		if stagingOpen {
			resultErr = joinCloseError(resultErr, "close CAS staging after transaction cleanup", staging)
		}
	}()
	if !transaction.directoryRemoved {
		current, err := staging.Lstat(transaction.directory)
		if errors.Is(err, fs.ErrNotExist) {
			transaction.directoryRemoved = true
		} else if err != nil {
			return fmt.Errorf("lstat transaction for cleanup: %w", err)
		}
		if !transaction.directoryRemoved {
			if err := validatePrivateDirectoryInfo(transaction.directory, current); err != nil {
				return err
			}
			if !os.SameFile(transaction.rootInfo, current) {
				return errors.New("transaction directory changed before cleanup")
			}
			if err := staging.Remove(transaction.directory); err != nil {
				return fmt.Errorf("remove transaction directory: %w", err)
			}
			transaction.directoryRemoved = true
			if transaction.store.afterCleanupRemove != nil {
				if err := transaction.store.afterCleanupRemove(); err != nil {
					return fmt.Errorf("after removing transaction directory: %w", err)
				}
			}
		}
	}
	if err := syncRoot(staging); err != nil {
		return fmt.Errorf("sync staging after cleanup: %w", err)
	}
	if transaction.store.beforeCleanupClose != nil {
		if err := transaction.store.beforeCleanupClose(); err != nil {
			return fmt.Errorf("before closing transaction cleanup staging: %w", err)
		}
	}
	closeErr := staging.Close()
	stagingOpen = false
	if closeErr != nil {
		return fmt.Errorf("close CAS staging after transaction cleanup: %w", closeErr)
	}
	if transaction.lease != nil {
		lease := transaction.lease
		transaction.lease = nil
		if err := errors.Join(unlockTransaction(lease), lease.Close()); err != nil {
			return fmt.Errorf("release transaction cleanup lease: %w", err)
		}
	}
	transaction.cleanupComplete = true
	return nil
}

func (transaction *Transaction) finishDetailed(
	result CommitResult,
	commitErr error,
) (CommitResult, error) {
	cleanupErr := transaction.cleanupLocked()
	if cleanupErr != nil {
		result.CleanupPending = true
		cleanupErr = errors.Join(ErrCleanupPending, cleanupErr)
	}
	if result.State == CommitVisible && result.UncertainObject == nil {
		uncertain := result.Root
		result.UncertainObject = &uncertain
		result.UncertainStage = "root_durability"
	}
	if bindingErr := transaction.store.validateRootBinding(); bindingErr != nil {
		result.State = CommitIndeterminate
		result.Durable = false
		uncertain := result.Root
		result.UncertainObject = &uncertain
		result.UncertainStage = "cas_root_binding_after_cleanup"
		commitErr = errors.Join(commitErr, bindingErr)
	}
	if transaction.rootPublished && (commitErr != nil || cleanupErr != nil) {
		return result, errors.Join(
			ErrRootPublished,
			commitErr,
			cleanupErr,
			result.Validate(),
		)
	}
	return result, errors.Join(commitErr, cleanupErr, result.Validate())
}

func oneRef(refs map[ObjectRef]struct{}) ObjectRef {
	var selected ObjectRef
	for ref := range refs {
		if selected.Digest == "" || ref.MediaType < selected.MediaType {
			selected = ref
		}
	}
	if selected.Digest == "" {
		panic("staged CAS object has no references")
	}
	return selected
}

func writeBounded(
	ctx context.Context,
	destination io.Writer,
	source io.Reader,
	limit int64,
) (string, int64, error) {
	hash := sha256.New()
	var buffer [32 * 1024]byte
	var size int64
	emptyReads := 0
	for {
		if err := ctx.Err(); err != nil {
			return "", size, err
		}
		remaining := limit - size
		readLimit := int64(len(buffer))
		if remaining < readLimit {
			readLimit = remaining + 1
		}
		read, readErr := source.Read(buffer[:int(readLimit)])
		if read < 0 || int64(read) > readLimit {
			return "", size, errors.New("CAS input reader returned an invalid byte count")
		}
		if read > 0 {
			emptyReads = 0
			if int64(read) > remaining {
				return "", size, fmt.Errorf("%w: limit is %d bytes", ErrTooLarge, limit)
			}
			chunk := buffer[:read]
			written, err := destination.Write(chunk)
			if err != nil {
				return "", size, fmt.Errorf("write staged CAS object: %w", err)
			}
			if written != len(chunk) {
				return "", size, io.ErrShortWrite
			}
			if _, err := hash.Write(chunk); err != nil {
				return "", size, fmt.Errorf("hash staged CAS object: %w", err)
			}
			size += int64(read)
		} else if readErr == nil {
			emptyReads++
			if emptyReads >= 100 {
				return "", size, io.ErrNoProgress
			}
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			return "", size, fmt.Errorf("read CAS input: %w", readErr)
		}
	}
	return hex.EncodeToString(hash.Sum(nil)), size, nil
}
