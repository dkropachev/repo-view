package cas

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
)

// Options controls resource bounds for a Store.
type Options struct {
	// MaxObjectBytes is the maximum number of bytes Put accepts and Read may
	// allocate. It must be positive.
	MaxObjectBytes int64
}

const (
	// These are format-independent safety ceilings, not tuning defaults. Evidence
	// publication stays comfortably below them, while a corrupt or abandoned
	// staging tree cannot force unbounded allocation during Open or recovery.
	hardMaxObjectBytes       int64 = 64 << 20
	hardMaxTransactionBytes  int64 = 512 << 20
	hardMaxTransactionPuts         = 256
	hardMaxStaleTransactions       = 128
	hardMaxStaleObjects            = hardMaxTransactionPuts
)

type renameNoReplaceFunc func(*os.File, string, *os.File, string) error

// Store is an immutable SHA-256 object store rooted at one existing directory.
// Its methods are safe for concurrent use, except that callers must keep the
// store open until all transactions have finished.
type Store struct {
	root           *os.Root
	path           string
	rootInfo       os.FileInfo
	maxObjectBytes int64

	stateMu sync.RWMutex
	closed  bool

	publishMu sync.Mutex

	// afterPublish is an internal deterministic fault-injection point used by
	// tests. Production stores leave it nil.
	afterPublish func(ObjectRef)

	// afterAtomicPublish, beforeCleanup, and afterCleanupRemove are internal
	// deterministic fault-injection points used by tests. Production stores
	// leave them nil.
	afterAtomicPublish func(ObjectRef) error
	beforeCleanup      func() error
	afterCleanupRemove func() error
	publicationRename  renameNoReplaceFunc
}

// Open opens an existing, absolute, clean directory as a store, creates its
// private internal layout, and recovers strictly validated staging directories
// when no transaction lease is active. The final path component must not be a
// symbolic link. Existing internal directories are accepted only when they are
// real directories with mode 0700 on the same filesystem.
func Open(path string, options Options) (*Store, error) {
	if options.MaxObjectBytes <= 0 {
		return nil, errors.New("CAS maximum object size must be positive")
	}
	if options.MaxObjectBytes > hardMaxObjectBytes {
		return nil, fmt.Errorf(
			"%w: CAS maximum object size %d exceeds hard limit %d",
			ErrTooLarge,
			options.MaxObjectBytes,
			hardMaxObjectBytes,
		)
	}
	if !atomicNoReplaceSupported() {
		return nil, errors.New("CAS atomic no-replace publication is unsupported on this platform")
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("CAS root must be an absolute, clean path")
	}

	before, err := os.Lstat(path)
	if err != nil {
		return nil, fmt.Errorf("lstat CAS root: %w", err)
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.IsDir() {
		return nil, errors.New("CAS root must be a real directory, not a symlink")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, fmt.Errorf("open CAS root: %w", err)
	}
	opened, err := root.Stat(".")
	if err != nil {
		root.Close()
		return nil, fmt.Errorf("stat opened CAS root: %w", err)
	}
	if !opened.IsDir() || !os.SameFile(before, opened) {
		root.Close()
		return nil, errors.New("CAS root changed while it was opened")
	}
	current, err := os.Lstat(path)
	if err != nil || current.Mode()&os.ModeSymlink != 0 ||
		!current.IsDir() || !os.SameFile(opened, current) {
		root.Close()
		return nil, errors.New("CAS root changed while it was opened")
	}

	store := &Store{
		root:           root,
		path:           path,
		rootInfo:       opened,
		maxObjectBytes: options.MaxObjectBytes,
	}
	if err := store.ensureLayout(); err != nil {
		root.Close()
		return nil, err
	}
	if err := store.RecoverStale(); err != nil && !errors.Is(err, ErrTransactionsActive) {
		root.Close()
		return nil, fmt.Errorf("recover stale CAS transactions: %w", err)
	}
	if err := store.probeAtomicNoReplace(); err != nil {
		root.Close()
		return nil, fmt.Errorf("probe CAS atomic no-replace publication: %w", err)
	}
	if err := store.validateRootBinding(); err != nil {
		root.Close()
		return nil, err
	}
	return store, nil
}

// Close releases the store's rooted directory descriptor. It does not delete
// objects or transaction data.
func (store *Store) Close() error {
	store.stateMu.Lock()
	defer store.stateMu.Unlock()
	if store.closed {
		return nil
	}
	store.closed = true
	return store.root.Close()
}

// Begin creates a private transaction staging directory beneath the store.
func (store *Store) Begin() (*Transaction, error) {
	store.stateMu.RLock()
	defer store.stateMu.RUnlock()
	if store.closed {
		return nil, errors.New("CAS store is closed")
	}
	if err := store.validateRootBinding(); err != nil {
		return nil, err
	}

	staging, stagingInfo, err := openPrivateDirectory(store.root, "staging")
	if err != nil {
		return nil, err
	}
	defer staging.Close()
	algorithm, algorithmInfo, err := store.openAlgorithmDirectory()
	if err != nil {
		return nil, err
	}
	defer algorithm.Close()
	if !sameFilesystem(stagingInfo, algorithmInfo) {
		return nil, errors.New("CAS staging and object directories are not on the same filesystem")
	}
	lease, leaseInfo, err := openTransactionLock(staging)
	if err != nil {
		return nil, err
	}
	if !sameFilesystem(stagingInfo, leaseInfo) {
		lease.Close()
		return nil, errors.New("CAS transaction lock is not on the staging filesystem")
	}
	if err := lockTransactionShared(lease); err != nil {
		lease.Close()
		return nil, fmt.Errorf("lock live CAS transaction: %w", err)
	}
	if err := validateCanonicalTransactionLock(staging, leaseInfo); err != nil {
		unlockTransaction(lease)
		lease.Close()
		return nil, err
	}
	releaseLease := true
	defer func() {
		if releaseLease {
			unlockTransaction(lease)
			lease.Close()
		}
	}()

	for attempt := 0; attempt < 32; attempt++ {
		name, nameErr := randomEntryName("tx-")
		if nameErr != nil {
			return nil, nameErr
		}
		if err := staging.Mkdir(name, privateDirectoryMode); err != nil {
			if errors.Is(err, fs.ErrExist) {
				continue
			}
			return nil, fmt.Errorf("create transaction staging directory: %w", err)
		}
		if err := staging.Chmod(name, privateDirectoryMode); err != nil {
			return nil, fmt.Errorf("set transaction staging directory mode: %w", err)
		}
		createdInfo, err := staging.Lstat(name)
		if err != nil {
			return nil, fmt.Errorf("lstat new transaction staging directory: %w", err)
		}
		if err := validatePrivateDirectoryInfo(name, createdInfo); err != nil {
			return nil, err
		}
		transactionRoot, transactionInfo, err := openPrivateDirectory(staging, name)
		if err != nil {
			return nil, errors.Join(err, removeDirectoryIfSame(staging, name, createdInfo))
		}
		if err := syncRoot(staging); err != nil {
			transactionRoot.Close()
			cleanupErr := removeDirectoryIfSame(staging, name, transactionInfo)
			return nil, errors.Join(
				fmt.Errorf("sync transaction staging parent: %w", err),
				cleanupErr,
			)
		}
		releaseLease = false
		return &Transaction{
			store:     store,
			directory: name,
			root:      transactionRoot,
			rootInfo:  transactionInfo,
			lease:     lease,
			staged:    make(map[string]*stagedObject),
			owned:     make(map[string]os.FileInfo),
		}, nil
	}
	return nil, errors.New("could not allocate a unique CAS transaction directory")
}

func removeDirectoryIfSame(parent *os.Root, name string, expected os.FileInfo) error {
	current, err := parent.Lstat(name)
	if err != nil {
		return fmt.Errorf("lstat owned directory %s for cleanup: %w", name, err)
	}
	if !os.SameFile(expected, current) {
		return fmt.Errorf("%w: owned directory %s changed before cleanup", ErrIntegrity, name)
	}
	if err := parent.Remove(name); err != nil {
		return fmt.Errorf("remove owned directory %s: %w", name, err)
	}
	return syncRoot(parent)
}

// Verify reads and authenticates ref without retaining its bytes.
func (store *Store) Verify(ctx context.Context, ref ObjectRef) error {
	return store.Copy(ctx, ref, io.Discard)
}

// Copy authenticates ref while streaming it to destination. If Copy returns an
// error, destination may contain a partial prefix and must be discarded.
func (store *Store) Copy(ctx context.Context, ref ObjectRef, destination io.Writer) error {
	if destination == nil {
		return errors.New("CAS copy destination is nil")
	}
	store.stateMu.RLock()
	defer store.stateMu.RUnlock()
	if store.closed {
		return errors.New("CAS store is closed")
	}
	if err := store.validateRootBinding(); err != nil {
		return err
	}
	if err := store.validateRef(ref); err != nil {
		return err
	}
	if err := store.verifyObject(ctx, ref, destination); err != nil {
		return err
	}
	return store.validateRootBinding()
}

// Read authenticates ref and returns its exact bytes. Allocation is bounded by
// Options.MaxObjectBytes.
func (store *Store) Read(ctx context.Context, ref ObjectRef) ([]byte, error) {
	var content bytes.Buffer
	if err := store.Copy(ctx, ref, &content); err != nil {
		return nil, err
	}
	return content.Bytes(), nil
}

// EnsureDurable idempotently verifies and syncs an exact bounded set of
// already-visible objects. It never creates or replaces an object. Evidence
// publication uses it to resolve a transient post-rename durability boundary.
func (store *Store) EnsureDurable(ctx context.Context, refs []ObjectRef) error {
	if len(refs) == 0 || len(refs) > hardMaxTransactionPuts {
		return fmt.Errorf(
			"%w: durability set must contain between 1 and %d references",
			ErrTooLarge,
			hardMaxTransactionPuts,
		)
	}
	store.stateMu.RLock()
	defer store.stateMu.RUnlock()
	if store.closed {
		return errors.New("CAS store is closed")
	}
	if err := store.validateRootBinding(); err != nil {
		return &ObjectOperationError{
			Ref: refs[0], Stage: "root_binding_before_sync", Err: err,
		}
	}
	unique := make(map[string]ObjectRef, len(refs))
	for _, ref := range refs {
		if err := store.validateRef(ref); err != nil {
			return &ObjectOperationError{
				Ref: ref, Stage: "validate_reference", Err: err,
			}
		}
		if previous, ok := unique[ref.Digest]; ok && previous.Size != ref.Size {
			return &ObjectOperationError{
				Ref:   ref,
				Stage: "validate_reference",
				Err: fmt.Errorf(
					"%w: one digest has inconsistent sizes",
					ErrIntegrity,
				),
			}
		}
		unique[ref.Digest] = ref
	}
	ordered := make([]ObjectRef, 0, len(unique))
	for _, ref := range unique {
		ordered = append(ordered, ref)
	}
	sort.Slice(ordered, func(left, right int) bool {
		return ordered[left].Digest < ordered[right].Digest
	})

	store.publishMu.Lock()
	defer store.publishMu.Unlock()
	for _, ref := range ordered {
		if err := ctx.Err(); err != nil {
			return &ObjectOperationError{
				Ref: ref, Stage: "context_before_sync", Err: err,
			}
		}
		if err := store.verifyObject(ctx, ref, io.Discard); err != nil {
			return &ObjectOperationError{
				Ref: ref, Stage: "verify_before_sync", Err: err,
			}
		}
		hexDigest := ref.hexDigest()
		shard, _, err := store.openShard(hexDigest, false)
		if err != nil {
			return &ObjectOperationError{
				Ref: ref, Stage: "open_shard", Err: err,
			}
		}
		durabilityErr := errors.Join(
			syncObjectAt(shard, hexDigest[2:]),
			syncRoot(shard),
			shard.Close(),
		)
		if durabilityErr != nil {
			return &ObjectOperationError{
				Ref: ref, Stage: "sync", Err: durabilityErr,
			}
		}
		if err := store.verifyObject(ctx, ref, io.Discard); err != nil {
			return &ObjectOperationError{
				Ref: ref, Stage: "verify_after_sync", Err: err,
			}
		}
	}
	if err := store.validateRootBinding(); err != nil {
		return &ObjectOperationError{
			Ref: refs[0], Stage: "root_binding_after_sync", Err: err,
		}
	}
	return nil
}

func (store *Store) ensureLayout() error {
	objects, _, err := ensurePrivateDirectory(store.root, "objects")
	if err != nil {
		return err
	}
	algorithm, algorithmInfo, err := ensurePrivateDirectory(objects, "sha256")
	objects.Close()
	if err != nil {
		return err
	}
	defer algorithm.Close()
	staging, stagingInfo, err := ensurePrivateDirectory(store.root, "staging")
	if err != nil {
		return err
	}
	defer staging.Close()
	if !sameFilesystem(algorithmInfo, stagingInfo) {
		return errors.New("CAS staging and object directories are not on the same filesystem")
	}
	lock, lockInfo, err := openTransactionLock(staging)
	if err != nil {
		return err
	}
	defer lock.Close()
	if !sameFilesystem(stagingInfo, lockInfo) {
		return errors.New("CAS transaction lock is not on the staging filesystem")
	}
	return nil
}

func (store *Store) openAlgorithmDirectory() (*os.Root, os.FileInfo, error) {
	objects, _, err := openPrivateDirectory(store.root, "objects")
	if err != nil {
		return nil, nil, err
	}
	defer objects.Close()
	return openPrivateDirectory(objects, "sha256")
}

func (store *Store) openShard(hexDigest string, create bool) (*os.Root, os.FileInfo, error) {
	algorithm, _, err := store.openAlgorithmDirectory()
	if err != nil {
		return nil, nil, err
	}
	defer algorithm.Close()
	if create {
		return ensurePrivateDirectory(algorithm, hexDigest[:2])
	}
	return openPrivateDirectory(algorithm, hexDigest[:2])
}

func (store *Store) validateRef(ref ObjectRef) error {
	if err := ref.Validate(); err != nil {
		return err
	}
	if ref.Size > store.maxObjectBytes {
		return fmt.Errorf(
			"%w: reference size %d exceeds limit %d",
			ErrTooLarge,
			ref.Size,
			store.maxObjectBytes,
		)
	}
	return nil
}

func (store *Store) validateRootBinding() error {
	if store == nil || store.root == nil || store.path == "" || store.rootInfo == nil {
		return fmt.Errorf("%w: CAS root binding is unavailable", ErrIntegrity)
	}
	opened, err := store.root.Stat(".")
	if err != nil || !opened.IsDir() || !os.SameFile(store.rootInfo, opened) {
		return fmt.Errorf("%w: opened CAS root changed", ErrIntegrity)
	}
	current, err := os.Lstat(store.path)
	if err != nil || current.Mode()&os.ModeSymlink != 0 || !current.IsDir() ||
		!os.SameFile(store.rootInfo, current) {
		return fmt.Errorf("%w: configured CAS root path was rebound", ErrIntegrity)
	}
	return nil
}

func sameFilesystem(first, second os.FileInfo) bool {
	firstID, firstOK := filesystemID(first)
	secondID, secondOK := filesystemID(second)
	return firstOK && secondOK && firstID == secondID
}
