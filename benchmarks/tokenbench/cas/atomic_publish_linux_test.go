//go:build linux

package cas

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRenameErrorAfterMoveIsClassifiedAsPublished(t *testing.T) {
	store, root := newTestStore(t, 1<<20)
	transaction, err := store.Begin()
	if err != nil {
		t.Fatalf("Begin(): %v", err)
	}
	ref, err := transaction.Put(
		context.Background(),
		testMediaType,
		bytes.NewReader([]byte("rename completed despite reported error")),
	)
	if err != nil {
		t.Fatalf("Put(): %v", err)
	}

	renameErr := errors.New("simulated ambiguous rename error")
	store.publicationRename = func(
		sourceDirectory *os.File,
		sourceName string,
		destinationDirectory *os.File,
		destinationName string,
	) error {
		if err := renameNoReplace(
			sourceDirectory,
			sourceName,
			destinationDirectory,
			destinationName,
		); err != nil {
			return err
		}
		return renameErr
	}

	err = transaction.Commit(context.Background(), ref)
	if !errors.Is(err, ErrRootPublished) || !errors.Is(err, renameErr) {
		t.Fatalf("Commit() error = %v, want ErrRootPublished and rename error", err)
	}
	if errors.Is(err, ErrPublicationUnknown) {
		t.Fatalf("Commit() error = %v, publication should have been classified", err)
	}
	if err := store.Verify(context.Background(), ref); err != nil {
		t.Fatalf("Verify(published root): %v", err)
	}
	info, err := os.Lstat(objectPath(root, ref))
	if err != nil {
		t.Fatalf("Lstat(published root): %v", err)
	}
	if multipleLinks(info) {
		t.Fatal("published root has multiple links")
	}
	assertNoTransactions(t, root)
}

func TestIndeterminateRenameErrorIsReportedAsUnknown(t *testing.T) {
	store, root := newTestStore(t, 1<<20)
	transaction, err := store.Begin()
	if err != nil {
		t.Fatalf("Begin(): %v", err)
	}
	ref, err := transaction.Put(
		context.Background(),
		testMediaType,
		bytes.NewReader([]byte("lost at an indeterminate rename boundary")),
	)
	if err != nil {
		t.Fatalf("Put(): %v", err)
	}

	renameErr := errors.New("simulated indeterminate rename error")
	store.publicationRename = func(
		sourceDirectory *os.File,
		sourceName string,
		_ *os.File,
		_ string,
	) error {
		if err := unix.Unlinkat(int(sourceDirectory.Fd()), sourceName, 0); err != nil {
			return err
		}
		return renameErr
	}

	err = transaction.Commit(context.Background(), ref)
	if !errors.Is(err, ErrPublicationUnknown) || !errors.Is(err, renameErr) {
		t.Fatalf("Commit() error = %v, want ErrPublicationUnknown and rename error", err)
	}
	if errors.Is(err, ErrRootPublished) {
		t.Fatalf("Commit() error = %v, must not claim root publication", err)
	}
	if err := store.Verify(context.Background(), ref); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("Verify(unpublished root) error = %v, want ErrIntegrity", err)
	}
	assertNoTransactions(t, root)
}

func TestCommitDetailedIdentifiesUnknownChildAndRoot(t *testing.T) {
	for _, test := range []struct {
		name      string
		withChild bool
	}{
		{"root", false},
		{"child", true},
	} {
		t.Run(test.name, func(t *testing.T) {
			store, _ := newTestStore(t, 1<<20)
			transaction, err := store.Begin()
			if err != nil {
				t.Fatal(err)
			}
			var child ObjectRef
			if test.withChild {
				child, err = transaction.Put(
					context.Background(),
					"application/vnd.tokenbench.child",
					bytes.NewReader([]byte("uncertain child")),
				)
				if err != nil {
					t.Fatal(err)
				}
			}
			root, err := transaction.Put(
				context.Background(),
				"application/vnd.tokenbench.root",
				bytes.NewReader([]byte("intended root")),
			)
			if err != nil {
				t.Fatal(err)
			}
			renameErr := errors.New("indeterminate test rename")
			store.publicationRename = func(
				sourceDirectory *os.File,
				sourceName string,
				_ *os.File,
				_ string,
			) error {
				if err := unix.Unlinkat(int(sourceDirectory.Fd()), sourceName, 0); err != nil {
					return err
				}
				return renameErr
			}
			result, err := transaction.CommitDetailed(context.Background(), root)
			want := root
			if test.withChild {
				want = child
			}
			if !errors.Is(err, ErrPublicationUnknown) ||
				result.Root != root || result.State != CommitIndeterminate ||
				result.UncertainObject == nil || *result.UncertainObject != want ||
				result.UncertainStage != "atomic_rename" || result.Durable {
				t.Fatalf("CommitDetailed() = %+v, %v; uncertain object want %+v", result, err, want)
			}
		})
	}
}

func TestCommitDetailedSeparatesVisibilityDurabilityAndCleanup(t *testing.T) {
	t.Run("visible before destination sync", func(t *testing.T) {
		store, _ := newTestStore(t, 1<<20)
		transaction, err := store.Begin()
		if err != nil {
			t.Fatal(err)
		}
		root, err := transaction.Put(
			context.Background(), testMediaType, bytes.NewReader([]byte("visible only")),
		)
		if err != nil {
			t.Fatal(err)
		}
		boundaryErr := errors.New("stop before directory sync")
		store.afterAtomicPublish = func(ObjectRef) error { return boundaryErr }
		result, err := transaction.CommitDetailed(context.Background(), root)
		if !errors.Is(err, ErrRootPublished) || !errors.Is(err, boundaryErr) ||
			result.State != CommitVisible || result.Durable ||
			result.UncertainObject == nil || *result.UncertainObject != root ||
			result.UncertainStage != "root_durability" {
			t.Fatalf("visible result = %+v, err=%v", result, err)
		}
	})

	t.Run("durable root with cleanup pending", func(t *testing.T) {
		store, _ := newTestStore(t, 1<<20)
		transaction, err := store.Begin()
		if err != nil {
			t.Fatal(err)
		}
		root, err := transaction.Put(
			context.Background(), testMediaType, bytes.NewReader([]byte("durable root")),
		)
		if err != nil {
			t.Fatal(err)
		}
		cleanupErr := errors.New("cleanup remains")
		store.afterCleanupRemove = func() error { return cleanupErr }
		result, err := transaction.CommitDetailed(context.Background(), root)
		if !errors.Is(err, ErrRootPublished) || !errors.Is(err, ErrCleanupPending) ||
			result.State != CommitDurable || !result.Durable || !result.CleanupPending {
			t.Fatalf("durable cleanup result = %+v, err=%v", result, err)
		}
		store.afterCleanupRemove = nil
		if err := transaction.Abort(); err != nil {
			t.Fatal(err)
		}
	})
}

func TestCommitDetailedRejectsReboundCASRootPath(t *testing.T) {
	store, rootPath := newTestStore(t, 1<<20)
	transaction, err := store.Begin()
	if err != nil {
		t.Fatal(err)
	}
	root, err := transaction.Put(
		context.Background(), testMediaType, bytes.NewReader([]byte("rebound root")),
	)
	if err != nil {
		t.Fatal(err)
	}
	displaced := rootPath + "-displaced"
	if err := os.Rename(rootPath, displaced); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(rootPath, privateDirectoryMode); err != nil {
		t.Fatal(err)
	}
	result, err := transaction.CommitDetailed(context.Background(), root)
	if !errors.Is(err, ErrIntegrity) || result.State != CommitIndeterminate ||
		result.Root != root || result.UncertainObject == nil ||
		*result.UncertainObject != root || result.Durable {
		t.Fatalf("rebound result = %+v, err=%v", result, err)
	}
	if _, err := os.Lstat(objectPath(rootPath, root)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("replacement CAS received object: %v", err)
	}
}

func TestRenameErrorWithStagedInodeIntactIsUnpublished(t *testing.T) {
	store, root := newTestStore(t, 1<<20)
	transaction, err := store.Begin()
	if err != nil {
		t.Fatalf("Begin(): %v", err)
	}
	ref, err := transaction.Put(
		context.Background(),
		testMediaType,
		bytes.NewReader([]byte("rename failed before changing either path")),
	)
	if err != nil {
		t.Fatalf("Put(): %v", err)
	}

	renameErr := errors.New("simulated pre-rename failure")
	store.publicationRename = func(*os.File, string, *os.File, string) error {
		return renameErr
	}
	err = transaction.Commit(context.Background(), ref)
	if !errors.Is(err, renameErr) {
		t.Fatalf("Commit() error = %v, want rename error", err)
	}
	if errors.Is(err, ErrPublicationUnknown) || errors.Is(err, ErrRootPublished) {
		t.Fatalf("Commit() error = %v, want definitely unpublished", err)
	}
	if err := store.Verify(context.Background(), ref); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("Verify(unpublished root) error = %v, want ErrIntegrity", err)
	}
	assertNoTransactions(t, root)
}

func TestCanonicalShardSwapDoesNotClaimRootPublication(t *testing.T) {
	store, root := newTestStore(t, 1<<20)
	transaction, err := store.Begin()
	if err != nil {
		t.Fatalf("Begin(): %v", err)
	}
	ref, err := transaction.Put(
		context.Background(),
		testMediaType,
		bytes.NewReader([]byte("published into a displaced shard")),
	)
	if err != nil {
		t.Fatalf("Put(): %v", err)
	}

	swapErr := errors.New("simulated failure after displacing shard")
	store.afterAtomicPublish = func(ObjectRef) error {
		shard := filepath.Dir(objectPath(root, ref))
		if err := os.Rename(shard, shard+"-displaced"); err != nil {
			return err
		}
		if err := os.Mkdir(shard, privateDirectoryMode); err != nil {
			return err
		}
		return swapErr
	}
	err = transaction.Commit(context.Background(), ref)
	if !errors.Is(err, ErrPublicationUnknown) ||
		!errors.Is(err, ErrIntegrity) ||
		!errors.Is(err, swapErr) {
		t.Fatalf("Commit() error = %v, want unknown, integrity, and hook errors", err)
	}
	if errors.Is(err, ErrRootPublished) {
		t.Fatalf("Commit() error = %v, must not claim canonical root publication", err)
	}
	if err := store.Verify(context.Background(), ref); !errors.Is(err, ErrIntegrity) {
		t.Fatalf("Verify(canonical root) error = %v, want ErrIntegrity", err)
	}
	assertNoTransactions(t, root)
}

func TestOpenRecoversProcessCrashAfterAtomicPublication(t *testing.T) {
	const (
		childMarker = "TOKENBENCH_CAS_CRASH_CHILD"
		rootEnv     = "TOKENBENCH_CAS_CRASH_ROOT"
	)
	content := []byte("root atomically moved immediately before process crash")
	if os.Getenv(childMarker) == "1" {
		crashChildAfterAtomicPublication(os.Getenv(rootEnv), content)
		os.Exit(0)
	}

	root := filepath.Join(t.TempDir(), "cas")
	if err := os.Mkdir(root, privateDirectoryMode); err != nil {
		t.Fatalf("Mkdir(CAS root): %v", err)
	}
	store, err := Open(root, Options{MaxObjectBytes: 1 << 20})
	if err != nil {
		t.Fatalf("Open(initial): %v", err)
	}
	if err := store.Close(); err != nil {
		t.Fatalf("Close(initial): %v", err)
	}

	command := exec.Command(os.Args[0], "-test.run=^TestOpenRecoversProcessCrashAfterAtomicPublication$")
	command.Env = append(os.Environ(), childMarker+"=1", rootEnv+"="+root)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("crash subprocess: %v\n%s", err, output)
	}

	recovered, err := Open(root, Options{MaxObjectBytes: 1 << 20})
	if err != nil {
		t.Fatalf("Open(recovering): %v", err)
	}
	t.Cleanup(func() {
		if err := recovered.Close(); err != nil {
			t.Errorf("Close(recovered): %v", err)
		}
	})
	ref := referenceFor(content, testMediaType)
	if err := recovered.Verify(context.Background(), ref); err != nil {
		t.Fatalf("Verify(root after process crash): %v", err)
	}
	info, err := os.Lstat(objectPath(root, ref))
	if err != nil {
		t.Fatalf("Lstat(root after process crash): %v", err)
	}
	if multipleLinks(info) {
		t.Fatal("root recovered after process crash has multiple links")
	}
	assertNoTransactions(t, root)
}

func crashChildAfterAtomicPublication(root string, content []byte) {
	fail := func(operation string, err error) {
		if err != nil {
			fmt.Fprintf(os.Stderr, "%s: %v\n", operation, err)
			os.Exit(2)
		}
	}
	store, err := Open(root, Options{MaxObjectBytes: 1 << 20})
	fail("Open", err)
	transaction, err := store.Begin()
	fail("Begin", err)
	ref, err := transaction.Put(context.Background(), testMediaType, bytes.NewReader(content))
	fail("Put", err)
	staged := transaction.staged[ref.hexDigest()]
	shard, _, err := store.openShard(ref.hexDigest(), true)
	fail("openShard", err)
	sourceDirectory, err := transaction.root.Open(".")
	fail("open transaction directory", err)
	destinationDirectory, err := shard.Open(".")
	fail("open shard directory", err)
	fail(
		"renameNoReplace",
		renameNoReplace(
			sourceDirectory,
			staged.name,
			destinationDirectory,
			ref.hexDigest()[2:],
		),
	)
	// Deliberately bypass every close, directory sync, transaction cleanup, and
	// lease release. Process exit is the crash boundary under test.
}
