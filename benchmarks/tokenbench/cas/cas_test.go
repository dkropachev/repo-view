package cas

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"testing"
)

const testMediaType = "application/vnd.tokenbench.test"

func TestObjectRefIsStrictAndPutIsBounded(t *testing.T) {
	valid := referenceFor([]byte("object"), testMediaType)
	if err := valid.Validate(); err != nil {
		t.Fatalf("Validate(valid ref): %v", err)
	}

	tests := []struct {
		name string
		ref  ObjectRef
	}{
		{"algorithm", ObjectRef{
			Digest:    "sha512:" + valid.hexDigest(),
			Size:      valid.Size,
			MediaType: valid.MediaType,
		}},
		{"uppercase digest", ObjectRef{
			Digest:    "sha256:" + "ABCDEF" + valid.hexDigest()[6:],
			Size:      valid.Size,
			MediaType: valid.MediaType,
		}},
		{"negative size", ObjectRef{
			Digest:    valid.Digest,
			Size:      -1,
			MediaType: valid.MediaType,
		}},
		{"parameterized media type", ObjectRef{
			Digest:    valid.Digest,
			Size:      valid.Size,
			MediaType: "application/json; charset=utf-8",
		}},
		{"uppercase media type", ObjectRef{
			Digest:    valid.Digest,
			Size:      valid.Size,
			MediaType: "Application/JSON",
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := test.ref.Validate(); !errors.Is(err, ErrInvalidObjectRef) {
				t.Fatalf("Validate() error = %v, want ErrInvalidObjectRef", err)
			}
		})
	}

	store, root := newTestStore(t, 8)
	transaction, err := store.Begin()
	if err != nil {
		t.Fatalf("Begin(): %v", err)
	}
	input := bytes.NewReader([]byte("123456789tail"))
	if _, err := transaction.Put(context.Background(), testMediaType, input); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Put(oversize) error = %v, want ErrTooLarge", err)
	}
	if consumed := input.Size() - int64(input.Len()); consumed != 9 {
		t.Fatalf("Put(oversize) consumed %d bytes, want limit+1 (9)", consumed)
	}
	if err := transaction.Abort(); err != nil {
		t.Fatalf("Abort(): %v", err)
	}
	assertNoTransactions(t, root)
}

func TestStoreAndTransactionHardBounds(t *testing.T) {
	root := filepath.Join(t.TempDir(), "cas")
	if err := os.Mkdir(root, privateDirectoryMode); err != nil {
		t.Fatal(err)
	}
	if _, err := Open(root, Options{MaxObjectBytes: hardMaxObjectBytes + 1}); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("oversized store option error = %v", err)
	}

	store, _ := newTestStore(t, 1)
	transaction, err := store.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for index := 0; index < hardMaxTransactionPuts; index++ {
		if _, err := transaction.Put(
			context.Background(),
			testMediaType,
			bytes.NewReader([]byte{byte(index)}),
		); err != nil {
			t.Fatalf("Put(%d): %v", index, err)
		}
	}
	if _, err := transaction.Put(
		context.Background(), testMediaType, bytes.NewReader([]byte{0}),
	); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("Put beyond hard count error = %v", err)
	}
	if err := transaction.Abort(); err != nil {
		t.Fatal(err)
	}
}

func TestEnsureDurableReportsExactObjectAndStage(t *testing.T) {
	store, root := newTestStore(t, 1<<20)
	ref := putAndCommit(t, store, []byte("durability target"))
	if err := os.Remove(objectPath(root, ref)); err != nil {
		t.Fatal(err)
	}

	err := store.EnsureDurable(context.Background(), []ObjectRef{ref})
	var operationErr *ObjectOperationError
	if !errors.As(err, &operationErr) {
		t.Fatalf("EnsureDurable() error = %v, want ObjectOperationError", err)
	}
	if operationErr.Ref != ref || operationErr.Stage != "verify_before_sync" ||
		!errors.Is(err, ErrIntegrity) {
		t.Fatalf("EnsureDurable() typed error = %+v, err=%v", operationErr, err)
	}
}

func TestRecoveryDirectoryEnumerationIsBounded(t *testing.T) {
	store, root := newTestStore(t, 1<<20)
	transaction, err := store.Begin()
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "staging", transaction.directory)
	for index := 0; index <= hardMaxStaleObjects; index++ {
		name := fmt.Sprintf("object-%032x", index)
		if err := os.WriteFile(filepath.Join(directory, name), nil, stagedFileMode); err != nil {
			t.Fatal(err)
		}
	}
	abandonTransactionForTest(t, transaction)
	if err := store.RecoverStale(); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("RecoverStale() error = %v, want ErrTooLarge", err)
	}
}

func TestConcurrentDuplicatePublication(t *testing.T) {
	store, root := newTestStore(t, 1<<20)
	content := bytes.Repeat([]byte("duplicate-content\n"), 64)
	const workers = 16
	stores := make([]*Store, workers)
	stores[0] = store
	for index := 1; index < workers; index++ {
		concurrentStore, err := Open(root, Options{MaxObjectBytes: 1 << 20})
		if err != nil {
			t.Fatalf("Open(concurrent store %d): %v", index, err)
		}
		stores[index] = concurrentStore
		t.Cleanup(func() {
			if err := concurrentStore.Close(); err != nil {
				t.Errorf("Close(concurrent store): %v", err)
			}
		})
	}

	type prepared struct {
		transaction *Transaction
		ref         ObjectRef
	}
	transactions := make([]prepared, workers)
	for index := range transactions {
		transaction, err := stores[index].Begin()
		if err != nil {
			t.Fatalf("Begin(%d): %v", index, err)
		}
		ref, err := transaction.Put(context.Background(), testMediaType, bytes.NewReader(content))
		if err != nil {
			t.Fatalf("Put(%d): %v", index, err)
		}
		transactions[index] = prepared{transaction: transaction, ref: ref}
		if ref != transactions[0].ref {
			t.Fatalf("Put(%d) ref = %#v, want %#v", index, ref, transactions[0].ref)
		}
	}

	start := make(chan struct{})
	errorsByWorker := make(chan error, workers)
	var group sync.WaitGroup
	for _, item := range transactions {
		group.Add(1)
		go func() {
			defer group.Done()
			<-start
			errorsByWorker <- item.transaction.Commit(context.Background(), item.ref)
		}()
	}
	close(start)
	group.Wait()
	close(errorsByWorker)
	for err := range errorsByWorker {
		if err != nil {
			t.Errorf("concurrent Commit(): %v", err)
		}
	}

	ref := transactions[0].ref
	got, err := store.Read(context.Background(), ref)
	if err != nil {
		t.Fatalf("Read(): %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Fatal("Read() returned different content")
	}
	info, err := os.Lstat(objectPath(root, ref))
	if err != nil {
		t.Fatalf("Lstat(object): %v", err)
	}
	if info.Mode().Perm() != objectFileMode || multipleLinks(info) {
		t.Fatalf("published object mode/links = %s/multiple:%t", info.Mode(), multipleLinks(info))
	}
	assertNoTransactions(t, root)
}

func TestAtomicPublicationIsReadableWithoutMultipleLinks(t *testing.T) {
	store, root := newTestStore(t, 1<<20)
	transaction, err := store.Begin()
	if err != nil {
		t.Fatalf("Begin(): %v", err)
	}
	content := []byte("atomically visible object")
	ref, err := transaction.Put(context.Background(), testMediaType, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("Put(): %v", err)
	}
	stagedName := transaction.staged[ref.hexDigest()].name

	published := make(chan struct{})
	release := make(chan struct{})
	store.afterAtomicPublish = func(got ObjectRef) error {
		if got != ref {
			return errors.New("unexpected published reference")
		}
		close(published)
		<-release
		return nil
	}
	commitResult := make(chan error, 1)
	go func() {
		commitResult <- transaction.Commit(context.Background(), ref)
	}()
	<-published

	got, readErr := store.Read(context.Background(), ref)
	info, statErr := os.Lstat(objectPath(root, ref))
	_, stagedErr := os.Lstat(filepath.Join(root, "staging", transaction.directory, stagedName))
	close(release)
	commitErr := <-commitResult

	if readErr != nil {
		t.Fatalf("Read(during publication): %v", readErr)
	}
	if !bytes.Equal(got, content) {
		t.Fatal("Read(during publication) returned different content")
	}
	if statErr != nil {
		t.Fatalf("Lstat(published object): %v", statErr)
	}
	if multipleLinks(info) {
		t.Fatal("atomically published object transiently had multiple links")
	}
	if !errors.Is(stagedErr, os.ErrNotExist) {
		t.Fatalf("Lstat(moved staged object) error = %v, want ErrNotExist", stagedErr)
	}
	if commitErr != nil {
		t.Fatalf("Commit(): %v", commitErr)
	}
	assertNoTransactions(t, root)
}

func TestCrashBoundaryReportsPublishedRootAndCleanupIsRetryable(t *testing.T) {
	store, root := newTestStore(t, 1<<20)
	transaction, err := store.Begin()
	if err != nil {
		t.Fatalf("Begin(): %v", err)
	}
	content := []byte("root survives an interruption after atomic rename")
	ref, err := transaction.Put(context.Background(), testMediaType, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("Put(): %v", err)
	}

	crashErr := errors.New("simulated crash after atomic publication")
	cleanupErr := errors.New("simulated stale staging cleanup failure")
	store.afterAtomicPublish = func(ObjectRef) error {
		return crashErr
	}
	cleanupAttempts := 0
	store.beforeCleanup = func() error {
		cleanupAttempts++
		if cleanupAttempts == 1 {
			return cleanupErr
		}
		return nil
	}

	err = transaction.Commit(context.Background(), ref)
	if !errors.Is(err, ErrRootPublished) ||
		!errors.Is(err, ErrCleanupPending) ||
		!errors.Is(err, crashErr) ||
		!errors.Is(err, cleanupErr) {
		t.Fatalf(
			"Commit() error = %v, want publication, cleanup, crash, and cleanup errors",
			err,
		)
	}
	if verifyErr := store.Verify(context.Background(), ref); verifyErr != nil {
		t.Fatalf("Verify(root after crash boundary): %v", verifyErr)
	}
	info, err := os.Lstat(objectPath(root, ref))
	if err != nil {
		t.Fatalf("Lstat(root after crash boundary): %v", err)
	}
	if multipleLinks(info) {
		t.Fatal("root left at crash boundary has multiple links")
	}
	if _, err := os.Lstat(filepath.Join(root, "staging", transaction.directory)); err != nil {
		t.Fatalf("Lstat(stale transaction): %v", err)
	}

	if err := transaction.Abort(); err != nil {
		t.Fatalf("Abort(retry cleanup): %v", err)
	}
	if cleanupAttempts != 2 {
		t.Fatalf("cleanup attempts = %d, want 2", cleanupAttempts)
	}
	if err := store.Verify(context.Background(), ref); err != nil {
		t.Fatalf("Verify(root after cleanup retry): %v", err)
	}
	assertNoTransactions(t, root)
}

func TestCleanupFailureAfterPublicationIsExplicit(t *testing.T) {
	store, root := newTestStore(t, 1<<20)
	transaction, err := store.Begin()
	if err != nil {
		t.Fatalf("Begin(): %v", err)
	}
	ref, err := transaction.Put(
		context.Background(),
		testMediaType,
		bytes.NewReader([]byte("published before cleanup fails")),
	)
	if err != nil {
		t.Fatalf("Put(): %v", err)
	}

	cleanupErr := errors.New("simulated cleanup failure")
	store.afterCleanupRemove = func() error {
		return cleanupErr
	}
	err = transaction.Commit(context.Background(), ref)
	if !errors.Is(err, ErrRootPublished) ||
		!errors.Is(err, ErrCleanupPending) ||
		!errors.Is(err, cleanupErr) {
		t.Fatalf("Commit() error = %v, want publication and cleanup errors", err)
	}
	if err := store.Verify(context.Background(), ref); err != nil {
		t.Fatalf("Verify(published root): %v", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "staging", transaction.directory)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("Lstat(removed transaction) error = %v, want ErrNotExist", err)
	}
	if err := transaction.Commit(context.Background(), ref); !errors.Is(err, ErrTransactionClosed) {
		t.Fatalf("second Commit() error = %v, want ErrTransactionClosed", err)
	}
	if err := transaction.Abort(); err != nil {
		t.Fatalf("Abort(retry cleanup): %v", err)
	}
	assertNoTransactions(t, root)
}

func TestCleanupFailureBeforeRootDoesNotClaimPublication(t *testing.T) {
	store, root := newTestStore(t, 1<<20)
	transaction, err := store.Begin()
	if err != nil {
		t.Fatalf("Begin(): %v", err)
	}
	ref, err := transaction.Put(
		context.Background(),
		testMediaType,
		bytes.NewReader([]byte("root remains staged")),
	)
	if err != nil {
		t.Fatalf("Put(): %v", err)
	}

	cleanupErr := errors.New("simulated cleanup failure")
	failed := false
	store.beforeCleanup = func() error {
		if !failed {
			failed = true
			return cleanupErr
		}
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = transaction.Commit(ctx, ref)
	if !errors.Is(err, context.Canceled) ||
		!errors.Is(err, ErrCleanupPending) ||
		!errors.Is(err, cleanupErr) {
		t.Fatalf("Commit() error = %v, want context cancellation and cleanup error", err)
	}
	if errors.Is(err, ErrRootPublished) {
		t.Fatalf("Commit() error = %v, must not claim root publication", err)
	}
	if err := store.Verify(context.Background(), ref); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("Verify(unpublished root) error = %v, want ErrIntegrity", err)
	}
	if err := transaction.Abort(); err != nil {
		t.Fatalf("Abort(retry cleanup): %v", err)
	}
	assertNoTransactions(t, root)
}

func TestCorruptExistingObjectIsNeverOverwritten(t *testing.T) {
	store, root := newTestStore(t, 1<<20)
	transaction, err := store.Begin()
	if err != nil {
		t.Fatalf("Begin(): %v", err)
	}
	content := []byte("correct immutable object")
	ref, err := transaction.Put(context.Background(), testMediaType, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("Put(): %v", err)
	}

	path := objectPath(root, ref)
	if err := os.MkdirAll(filepath.Dir(path), privateDirectoryMode); err != nil {
		t.Fatalf("MkdirAll(shard): %v", err)
	}
	corrupt := append([]byte(nil), content...)
	corrupt[0] ^= 0xff
	if err := os.WriteFile(path, corrupt, objectFileMode); err != nil {
		t.Fatalf("WriteFile(corrupt existing): %v", err)
	}

	err = transaction.Commit(context.Background(), ref)
	if !errors.Is(err, ErrIntegrity) {
		t.Fatalf("Commit() error = %v, want ErrIntegrity", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(existing): %v", err)
	}
	if !bytes.Equal(got, corrupt) {
		t.Fatal("Commit overwrote the corrupt existing object")
	}
	assertNoTransactions(t, root)
}

func TestReadRejectsFilesystemAndContentCorruption(t *testing.T) {
	type corruptionTest struct {
		name    string
		corrupt func(*testing.T, string, string, []byte)
	}
	tests := []corruptionTest{
		{"missing object", func(t *testing.T, _, object string, _ []byte) {
			if err := os.Remove(object); err != nil {
				t.Fatalf("Remove(object): %v", err)
			}
		}},
		{"bit-flipped object", func(t *testing.T, _, object string, content []byte) {
			flipped := append([]byte(nil), content...)
			flipped[len(flipped)-1] ^= 0xff
			if err := os.Chmod(object, stagedFileMode); err != nil {
				t.Fatalf("Chmod(object): %v", err)
			}
			if err := os.WriteFile(object, flipped, stagedFileMode); err != nil {
				t.Fatalf("WriteFile(flipped): %v", err)
			}
			if err := os.Chmod(object, objectFileMode); err != nil {
				t.Fatalf("Chmod(flipped): %v", err)
			}
		}},
		{"writable object", func(t *testing.T, _, object string, _ []byte) {
			if err := os.Chmod(object, stagedFileMode); err != nil {
				t.Fatalf("Chmod(object): %v", err)
			}
		}},
		{"object symlink", func(t *testing.T, root, object string, content []byte) {
			if err := os.Remove(object); err != nil {
				t.Fatalf("Remove(object): %v", err)
			}
			target := filepath.Join(root, "symlink-target")
			if err := os.WriteFile(target, content, objectFileMode); err != nil {
				t.Fatalf("WriteFile(target): %v", err)
			}
			if err := os.Symlink(target, object); err != nil {
				t.Fatalf("Symlink(object): %v", err)
			}
		}},
		{"object hardlink", func(t *testing.T, root, object string, content []byte) {
			if err := os.Remove(object); err != nil {
				t.Fatalf("Remove(object): %v", err)
			}
			target := filepath.Join(root, "hardlink-target")
			if err := os.WriteFile(target, content, objectFileMode); err != nil {
				t.Fatalf("WriteFile(target): %v", err)
			}
			if err := os.Link(target, object); err != nil {
				t.Skipf("hard links unavailable: %v", err)
			}
		}},
		{"nonregular object", func(t *testing.T, _, object string, _ []byte) {
			if err := os.Remove(object); err != nil {
				t.Fatalf("Remove(object): %v", err)
			}
			if err := os.Mkdir(object, privateDirectoryMode); err != nil {
				t.Fatalf("Mkdir(object): %v", err)
			}
		}},
		{"symlink shard", func(t *testing.T, root, object string, content []byte) {
			shard := filepath.Dir(object)
			if err := os.Remove(object); err != nil {
				t.Fatalf("Remove(object): %v", err)
			}
			if err := os.Remove(shard); err != nil {
				t.Fatalf("Remove(shard): %v", err)
			}
			target := filepath.Join(root, "shard-target")
			if err := os.Mkdir(target, privateDirectoryMode); err != nil {
				t.Fatalf("Mkdir(target shard): %v", err)
			}
			if err := os.WriteFile(filepath.Join(target, filepath.Base(object)), content, objectFileMode); err != nil {
				t.Fatalf("WriteFile(target object): %v", err)
			}
			if err := os.Symlink(target, shard); err != nil {
				t.Fatalf("Symlink(shard): %v", err)
			}
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store, root := newTestStore(t, 1<<20)
			content := []byte("authenticated object bytes")
			ref := putAndCommit(t, store, content)
			test.corrupt(t, root, objectPath(root, ref), content)

			if _, err := store.Read(context.Background(), ref); !errors.Is(err, ErrIntegrity) {
				t.Fatalf("Read(corrupt object) error = %v, want ErrIntegrity", err)
			}
		})
	}
}

func TestInterruptedCommitNeverPublishesRootAndCleansStaging(t *testing.T) {
	store, rootPath := newTestStore(t, 1<<20)
	transaction, err := store.Begin()
	if err != nil {
		t.Fatalf("Begin(): %v", err)
	}
	child, err := transaction.Put(
		context.Background(),
		"application/vnd.tokenbench.child",
		bytes.NewReader([]byte("child")),
	)
	if err != nil {
		t.Fatalf("Put(child): %v", err)
	}
	root, err := transaction.Put(
		context.Background(),
		"application/vnd.tokenbench.root",
		bytes.NewReader([]byte("root manifest")),
	)
	if err != nil {
		t.Fatalf("Put(root): %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	store.afterPublish = func(published ObjectRef) {
		if published == child {
			cancel()
		}
	}
	err = transaction.Commit(ctx, root)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Commit(interrupted) error = %v, want context.Canceled", err)
	}
	if err := store.Verify(context.Background(), child); err != nil {
		t.Fatalf("Verify(published child): %v", err)
	}
	if err := store.Verify(context.Background(), root); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("Verify(unpublished root) error = %v, want ErrIntegrity", err)
	}
	if err := transaction.Commit(context.Background(), root); !errors.Is(err, ErrTransactionClosed) {
		t.Fatalf("second Commit() error = %v, want ErrTransactionClosed", err)
	}
	assertNoTransactions(t, rootPath)
}

func TestSuccessfulCommitPublishesRootLast(t *testing.T) {
	store, _ := newTestStore(t, 1<<20)
	transaction, err := store.Begin()
	if err != nil {
		t.Fatalf("Begin(): %v", err)
	}
	first, err := transaction.Put(context.Background(), "application/vnd.tokenbench.child", bytes.NewReader([]byte("first")))
	if err != nil {
		t.Fatalf("Put(first): %v", err)
	}
	second, err := transaction.Put(context.Background(), "application/vnd.tokenbench.child", bytes.NewReader([]byte("second")))
	if err != nil {
		t.Fatalf("Put(second): %v", err)
	}
	root, err := transaction.Put(context.Background(), "application/vnd.tokenbench.root", bytes.NewReader([]byte("root")))
	if err != nil {
		t.Fatalf("Put(root): %v", err)
	}

	var order []ObjectRef
	store.afterPublish = func(ref ObjectRef) {
		order = append(order, ref)
	}
	if err := transaction.Commit(context.Background(), root); err != nil {
		t.Fatalf("Commit(): %v", err)
	}
	if len(order) != 3 || order[len(order)-1] != root {
		t.Fatalf("publication order = %#v, want root %#v last", order, root)
	}
	if !containsRef(order[:2], first) || !containsRef(order[:2], second) {
		t.Fatalf("publication order omitted a child: %#v", order)
	}
}

func TestFailedPutAndAbortCleanOnlyOwnedPaths(t *testing.T) {
	t.Run("failed put", func(t *testing.T) {
		store, root := newTestStore(t, 1<<20)
		transaction, err := store.Begin()
		if err != nil {
			t.Fatalf("Begin(): %v", err)
		}
		sentinel := errors.New("input failed")
		if _, err := transaction.Put(
			context.Background(),
			testMediaType,
			&failingReader{err: sentinel},
		); !errors.Is(err, sentinel) {
			t.Fatalf("Put(failing reader) error = %v, want sentinel", err)
		}
		if err := transaction.Abort(); err != nil {
			t.Fatalf("Abort(): %v", err)
		}
		assertNoTransactions(t, root)
	})

	t.Run("foreign staging entry", func(t *testing.T) {
		store, root := newTestStore(t, 1<<20)
		transaction, err := store.Begin()
		if err != nil {
			t.Fatalf("Begin(): %v", err)
		}
		if _, err := transaction.Put(context.Background(), testMediaType, bytes.NewReader([]byte("owned"))); err != nil {
			t.Fatalf("Put(): %v", err)
		}
		foreign, err := transaction.root.OpenFile(
			"foreign",
			os.O_WRONLY|os.O_CREATE|os.O_EXCL,
			stagedFileMode,
		)
		if err != nil {
			t.Fatalf("OpenFile(foreign): %v", err)
		}
		if err := foreign.Close(); err != nil {
			t.Fatalf("Close(foreign): %v", err)
		}
		directory := filepath.Join(root, "staging", transaction.directory)
		if err := transaction.Abort(); err == nil {
			t.Fatal("Abort() succeeded despite an unowned staging entry")
		}
		entries, err := os.ReadDir(directory)
		if err != nil {
			t.Fatalf("ReadDir(transaction): %v", err)
		}
		if len(entries) != 1 || entries[0].Name() != "foreign" {
			t.Fatalf("remaining entries = %#v, want only foreign", entries)
		}
		if err := os.Remove(filepath.Join(directory, "foreign")); err != nil {
			t.Fatalf("Remove(foreign): %v", err)
		}
		if err := transaction.Abort(); err != nil {
			t.Fatalf("Abort(retry cleanup): %v", err)
		}
		assertNoTransactions(t, root)
	})

	t.Run("replaced owned entry", func(t *testing.T) {
		store, root := newTestStore(t, 1<<20)
		transaction, err := store.Begin()
		if err != nil {
			t.Fatalf("Begin(): %v", err)
		}
		ref, err := transaction.Put(context.Background(), testMediaType, bytes.NewReader([]byte("owned")))
		if err != nil {
			t.Fatalf("Put(): %v", err)
		}
		stagedName := transaction.staged[ref.hexDigest()].name
		directory := filepath.Join(root, "staging", transaction.directory)
		stagedPath := filepath.Join(directory, stagedName)
		if err := os.Remove(stagedPath); err != nil {
			t.Fatalf("Remove(owned staged object): %v", err)
		}
		foreignContent := []byte("foreign replacement")
		if err := os.WriteFile(stagedPath, foreignContent, stagedFileMode); err != nil {
			t.Fatalf("WriteFile(replacement): %v", err)
		}
		if err := transaction.Abort(); !errors.Is(err, ErrIntegrity) {
			t.Fatalf("Abort() error = %v, want ErrIntegrity", err)
		}
		got, err := os.ReadFile(stagedPath)
		if err != nil {
			t.Fatalf("ReadFile(replacement): %v", err)
		}
		if !bytes.Equal(got, foreignContent) {
			t.Fatal("Abort mutated or removed the replacement entry")
		}
		if err := os.Remove(stagedPath); err != nil {
			t.Fatalf("Remove(replacement): %v", err)
		}
		if err := transaction.Abort(); err != nil {
			t.Fatalf("Abort(retry cleanup): %v", err)
		}
		assertNoTransactions(t, root)
	})
}

func TestAbortTreatsAlreadyRemovedTransactionAsCleaned(t *testing.T) {
	store, root := newTestStore(t, 1<<20)
	transaction, err := store.Begin()
	if err != nil {
		t.Fatalf("Begin(): %v", err)
	}
	directory := filepath.Join(root, "staging", transaction.directory)
	if err := os.Remove(directory); err != nil {
		t.Fatalf("Remove(transaction directory): %v", err)
	}
	if err := transaction.Abort(); err != nil {
		t.Fatalf("Abort(after external cleanup): %v", err)
	}
	assertNoTransactions(t, root)
}

func TestRecoverStaleRefusesLiveTransaction(t *testing.T) {
	store, root := newTestStore(t, 1<<20)
	transaction, err := store.Begin()
	if err != nil {
		t.Fatalf("Begin(): %v", err)
	}
	if err := store.RecoverStale(); !errors.Is(err, ErrTransactionsActive) {
		t.Fatalf("RecoverStale() error = %v, want ErrTransactionsActive", err)
	}
	if _, err := os.Lstat(filepath.Join(root, "staging", transaction.directory)); err != nil {
		t.Fatalf("Lstat(live transaction): %v", err)
	}
	if err := transaction.Abort(); err != nil {
		t.Fatalf("Abort(): %v", err)
	}
	if err := store.RecoverStale(); err != nil {
		t.Fatalf("RecoverStale(after Abort): %v", err)
	}
	assertNoTransactions(t, root)
}

func TestRecoverStaleRemovesAbandonedStagedObjects(t *testing.T) {
	store, root := newTestStore(t, 1<<20)
	transaction, err := store.Begin()
	if err != nil {
		t.Fatalf("Begin(): %v", err)
	}
	ref, err := transaction.Put(
		context.Background(),
		testMediaType,
		bytes.NewReader([]byte("abandoned staged object")),
	)
	if err != nil {
		t.Fatalf("Put(): %v", err)
	}
	abandonTransactionForTest(t, transaction)

	if err := store.RecoverStale(); err != nil {
		t.Fatalf("RecoverStale(): %v", err)
	}
	if err := store.Verify(context.Background(), ref); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("Verify(unpublished root) error = %v, want ErrIntegrity", err)
	}
	assertNoTransactions(t, root)
}

func TestRecoverStaleValidatesAllEntriesBeforeDeleting(t *testing.T) {
	store, root := newTestStore(t, 1<<20)
	transactions := make([]*Transaction, 2)
	for index := range transactions {
		transaction, err := store.Begin()
		if err != nil {
			t.Fatalf("Begin(%d): %v", index, err)
		}
		if _, err := transaction.Put(
			context.Background(),
			testMediaType,
			bytes.NewReader([]byte{byte(index)}),
		); err != nil {
			t.Fatalf("Put(%d): %v", index, err)
		}
		transactions[index] = transaction
	}
	foreign, err := transactions[1].root.OpenFile(
		"foreign",
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		stagedFileMode,
	)
	if err != nil {
		t.Fatalf("OpenFile(foreign): %v", err)
	}
	if err := foreign.Close(); err != nil {
		t.Fatalf("Close(foreign): %v", err)
	}
	for _, transaction := range transactions {
		abandonTransactionForTest(t, transaction)
	}

	if err := store.RecoverStale(); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("RecoverStale() error = %v, want ErrIntegrity", err)
	}
	for index, transaction := range transactions {
		if _, err := os.Lstat(filepath.Join(root, "staging", transaction.directory)); err != nil {
			t.Fatalf("Lstat(stale transaction %d): %v", index, err)
		}
	}
}

func TestDuplicatePublicationDoesNotMutateInputObject(t *testing.T) {
	store, root := newTestStore(t, 1<<20)
	content := []byte("immutable input object")
	ref := putAndCommit(t, store, content)
	path := objectPath(root, ref)
	beforeBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(before): %v", err)
	}
	beforeInfo, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("Lstat(before): %v", err)
	}

	input := append([]byte(nil), content...)
	inputSnapshot := append([]byte(nil), input...)
	transaction, err := store.Begin()
	if err != nil {
		t.Fatalf("Begin(duplicate): %v", err)
	}
	duplicate, err := transaction.Put(context.Background(), testMediaType, bytes.NewReader(input))
	if err != nil {
		t.Fatalf("Put(duplicate): %v", err)
	}
	if err := transaction.Commit(context.Background(), duplicate); err != nil {
		t.Fatalf("Commit(duplicate): %v", err)
	}
	if !bytes.Equal(input, inputSnapshot) {
		t.Fatal("Put mutated its input bytes")
	}

	afterBytes, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("ReadFile(after): %v", err)
	}
	afterInfo, err := os.Lstat(path)
	if err != nil {
		t.Fatalf("Lstat(after): %v", err)
	}
	if !bytes.Equal(afterBytes, beforeBytes) {
		t.Fatal("duplicate publication mutated existing object bytes")
	}
	if !os.SameFile(beforeInfo, afterInfo) || !sameObjectMetadata(beforeInfo, afterInfo) ||
		multipleLinks(afterInfo) {
		t.Fatal("duplicate publication replaced or changed existing object metadata")
	}
}

type failingReader struct {
	done bool
	err  error
}

func (reader *failingReader) Read(buffer []byte) (int, error) {
	if reader.done {
		return 0, reader.err
	}
	reader.done = true
	return copy(buffer, "partial"), nil
}

func newTestStore(t *testing.T, maxObjectBytes int64) (*Store, string) {
	t.Helper()
	if !atomicNoReplaceSupported() {
		t.Skip("platform has no safe atomic no-replace publication primitive")
	}
	root := filepath.Join(t.TempDir(), "cas")
	if err := os.Mkdir(root, privateDirectoryMode); err != nil {
		t.Fatalf("Mkdir(CAS root): %v", err)
	}
	store, err := Open(root, Options{MaxObjectBytes: maxObjectBytes})
	if err != nil {
		t.Fatalf("Open(): %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("Close(): %v", err)
		}
	})
	return store, root
}

func putAndCommit(t *testing.T, store *Store, content []byte) ObjectRef {
	t.Helper()
	transaction, err := store.Begin()
	if err != nil {
		t.Fatalf("Begin(): %v", err)
	}
	ref, err := transaction.Put(context.Background(), testMediaType, bytes.NewReader(content))
	if err != nil {
		t.Fatalf("Put(): %v", err)
	}
	if err := transaction.Commit(context.Background(), ref); err != nil {
		t.Fatalf("Commit(): %v", err)
	}
	return ref
}

func referenceFor(content []byte, mediaType string) ObjectRef {
	digest := sha256.Sum256(content)
	return ObjectRef{
		Digest:    digestPrefix + hex.EncodeToString(digest[:]),
		Size:      int64(len(content)),
		MediaType: mediaType,
	}
}

func objectPath(root string, ref ObjectRef) string {
	hexDigest := ref.hexDigest()
	return filepath.Join(root, "objects", "sha256", hexDigest[:2], hexDigest[2:])
}

func assertNoTransactions(t *testing.T, root string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join(root, "staging"))
	if err != nil {
		t.Fatalf("ReadDir(staging): %v", err)
	}
	var transactions []os.DirEntry
	for _, entry := range entries {
		if entry.Name() != transactionLockName {
			transactions = append(transactions, entry)
		}
	}
	if len(transactions) != 0 {
		t.Fatalf("transaction entries = %v, want none", transactions)
	}
}

func containsRef(refs []ObjectRef, wanted ObjectRef) bool {
	for _, ref := range refs {
		if ref == wanted {
			return true
		}
	}
	return false
}

func abandonTransactionForTest(t *testing.T, transaction *Transaction) {
	t.Helper()
	if transaction.root != nil {
		if err := transaction.root.Close(); err != nil {
			t.Fatalf("Close(abandoned transaction root): %v", err)
		}
		transaction.root = nil
	}
	if transaction.lease != nil {
		lease := transaction.lease
		transaction.lease = nil
		if err := errors.Join(unlockTransaction(lease), lease.Close()); err != nil {
			t.Fatalf("Release(abandoned transaction lease): %v", err)
		}
	}
}

var _ io.Reader = (*failingReader)(nil)
