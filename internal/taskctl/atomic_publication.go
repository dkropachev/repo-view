package taskctl

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
)

// atomicPublicationHooks carries the generation-start parent identity and
// exposes otherwise-unreachable publication boundaries to deterministic tests.
type atomicPublicationHooks struct {
	beforeDescriptorLink                func() error
	beforeLinkErrorCommitInspection     func(*os.File)
	afterDescriptorLinkBeforeValidation func() error
	expectedParent                      os.FileInfo
}

// writeAtomicPinned creates path through a descriptor-pinned canonical parent.
// Publication is intentionally create-only: replacing an existing pathname
// atomically cannot be bound to an already-open inode on Linux. The file stays
// anonymous until the kernel links its exact open descriptor directly at the
// absent destination, so there is no attacker-replaceable staging pathname.
//
// Once the descriptor link succeeds, publication is committed. Any later
// identity, durability, or close error is returned, but the destination is
// never removed or rolled back because its pathname may have been replaced.
func writeAtomicPinned(
	path string,
	data []byte,
	hooks atomicPublicationHooks,
) (resultErr error) {
	const mode = os.FileMode(0o644)
	if path == "" {
		return errors.New("output path is empty")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("make output path absolute: %w", err)
	}
	parentPath := filepath.Dir(absolute)
	destinationName := filepath.Base(absolute)
	if destinationName == "." || destinationName == string(filepath.Separator) {
		return errors.New("output path does not name a file")
	}

	parent, parentIdentity, err := openAtomicPublicationParent(parentPath)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, parent.Close()) }()
	if hooks.expectedParent != nil &&
		(!os.SameFile(hooks.expectedParent, parentIdentity) ||
			hooks.expectedParent.Mode() != parentIdentity.Mode()) {
		return errors.New("output directory changed since generation began")
	}
	if err := requireAtomicPublicationDestinationAbsent(parent, destinationName); err != nil {
		return err
	}

	directory, err := openAtomicPublicationDirectoryForSync(parent, parentIdentity)
	if err != nil {
		return err
	}
	directoryClosed := false
	defer func() {
		if !directoryClosed {
			resultErr = errors.Join(resultErr, directory.Close())
		}
	}()
	temporary, err := createAnonymousAtomicPublicationFile(directory)
	if err != nil {
		return fmt.Errorf("create anonymous atomic publication file: %w", err)
	}
	publicationCommitted := false
	defer func() {
		if !publicationCommitted {
			// The inode has no pathname unless the descriptor link committed.
			// Reclaim only through the owned descriptor; closing then destroys it.
			resultErr = errors.Join(
				resultErr,
				wrapAtomicPublicationReclaimError("truncate", temporary.Truncate(0)),
				wrapAtomicPublicationReclaimError("restore mode", temporary.Chmod(0o600)),
				wrapAtomicPublicationReclaimError("sync", temporary.Sync()),
			)
		}
		resultErr = errors.Join(resultErr, temporary.Close())
	}()
	temporaryIdentity, err := temporary.Stat()
	if err != nil {
		return fmt.Errorf("inspect anonymous atomic publication file: %w", err)
	}
	if err := validateAtomicPublicationFile(
		temporaryIdentity,
		temporaryIdentity,
		0o600,
		0,
		false,
	); err != nil {
		return err
	}

	written, err := temporary.Write(data)
	if err != nil {
		return fmt.Errorf("write anonymous atomic publication file: %w", err)
	}
	if written != len(data) {
		return ioErrShortWrite(written, len(data))
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync anonymous atomic publication file: %w", err)
	}
	writtenIdentity, err := temporary.Stat()
	if err != nil {
		return fmt.Errorf("reinspect anonymous atomic publication file: %w", err)
	}
	if err := validateAtomicPublicationFile(
		temporaryIdentity,
		writtenIdentity,
		0o600,
		int64(len(data)),
		false,
	); err != nil {
		return err
	}
	temporaryIdentity = writtenIdentity

	if err := temporary.Chmod(mode); err != nil {
		return fmt.Errorf("set atomic publication final mode: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync atomic publication final mode: %w", err)
	}
	finalIdentity, err := temporary.Stat()
	if err != nil {
		return fmt.Errorf("inspect atomic publication final inode: %w", err)
	}
	if err := validateAtomicPublicationFile(
		temporaryIdentity,
		finalIdentity,
		mode,
		int64(len(data)),
		false,
	); err != nil {
		return err
	}
	if err := validateAtomicPublicationContent(temporary, data); err != nil {
		return err
	}
	temporaryIdentity = finalIdentity
	if err := validateAtomicPublicationParent(parentPath, parent, parentIdentity); err != nil {
		return err
	}
	if hooks.beforeDescriptorLink != nil {
		if err := hooks.beforeDescriptorLink(); err != nil {
			return fmt.Errorf("atomic publication pre-link test hook: %w", err)
		}
	}
	if err := validateAtomicPublicationParent(parentPath, parent, parentIdentity); err != nil {
		return err
	}
	linkErr := linkAnonymousAtomicPublicationFile(temporary, directory, destinationName)
	if linkErr != nil {
		// If an unusual filesystem reports an error after creating the link,
		// the descriptor's link count is authoritative. Never truncate an inode
		// that may already be visible through a committed directory entry.
		if hooks.beforeLinkErrorCommitInspection != nil {
			hooks.beforeLinkErrorCommitInspection(temporary)
		}
		afterLink, statErr := temporary.Stat()
		if statErr != nil {
			// The link syscall is allowed to report an error after creating a
			// directory entry. If the descriptor can no longer prove a zero
			// link count, reclamation would risk truncating published bytes.
			publicationCommitted = true
			return errors.Join(
				fmt.Errorf("descriptor-bound atomic publication returned a link error: %w", linkErr),
				fmt.Errorf("cannot determine whether atomic publication committed: %w", statErr),
			)
		}
		if !atomicPublicationFileIsAnonymous(afterLink) {
			publicationCommitted = true
			return fmt.Errorf(
				"descriptor-bound atomic publication may have committed despite link error: %w",
				linkErr,
			)
		}
		// EEXIST is the kernel's atomic resolution of an absent-destination
		// race. It never replaces, opens, or mutates the winning object.
		if errors.Is(linkErr, os.ErrExist) {
			return errors.New(
				"output path appeared before create-only atomic publication; existing destination was not modified",
			)
		}
		return fmt.Errorf("link descriptor-bound atomic publication: %w", linkErr)
	}
	publicationCommitted = true
	if hooks.afterDescriptorLinkBeforeValidation != nil {
		if err := hooks.afterDescriptorLinkBeforeValidation(); err != nil {
			return fmt.Errorf("atomic publication post-link test hook: %w", err)
		}
	}
	publishedIdentity, err := parent.Lstat(destinationName)
	if err != nil {
		return fmt.Errorf("inspect atomically published file: %w", err)
	}
	if err := validateAtomicPublicationFile(
		temporaryIdentity,
		publishedIdentity,
		mode,
		int64(len(data)),
		true,
	); err != nil {
		return fmt.Errorf("validate atomically published file: %w", err)
	}
	if err := validateAtomicPublicationContent(temporary, data); err != nil {
		return fmt.Errorf("validate atomically published content: %w", err)
	}
	if err := validateAtomicPublicationParent(parentPath, parent, parentIdentity); err != nil {
		return err
	}
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync pinned output directory: %w", err)
	}
	directoryClosed = true
	if err := directory.Close(); err != nil {
		return fmt.Errorf("close pinned output directory after sync: %w", err)
	}
	if err := validateAtomicPublicationParent(parentPath, parent, parentIdentity); err != nil {
		return err
	}
	return nil
}

func openAtomicPublicationParent(path string) (*os.Root, os.FileInfo, error) {
	_, before, err := inspectCanonicalPublicationPath(path, true)
	if err != nil {
		return nil, nil, fmt.Errorf("output directory: %w", err)
	}
	parent, err := os.OpenRoot(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open output directory: %w", err)
	}
	if err := validateAtomicPublicationParent(path, parent, before); err != nil {
		return nil, nil, errors.Join(err, parent.Close())
	}
	return parent, before, nil
}

func validateAtomicPublicationParent(
	path string,
	parent *os.Root,
	want os.FileInfo,
) error {
	resolved, resolvedErr := filepath.EvalSymlinks(path)
	pinned, pinnedErr := parent.Lstat(".")
	current, currentErr := os.Lstat(path)
	if err := errors.Join(resolvedErr, pinnedErr, currentErr); err != nil {
		return fmt.Errorf("revalidate output directory pathname: %w", err)
	}
	if resolved != path ||
		!pinned.IsDir() || pinned.Mode()&os.ModeSymlink != 0 ||
		!current.IsDir() || current.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(want, pinned) || !os.SameFile(pinned, current) ||
		want.Mode() != pinned.Mode() || pinned.Mode() != current.Mode() {
		return errors.New("output directory pathname changed during publication")
	}
	return nil
}

func requireAtomicPublicationDestinationAbsent(parent *os.Root, name string) error {
	_, err := parent.Lstat(name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect output path: %w", err)
	}
	return errors.New(
		"output path already exists; atomic publication is create-only and did not modify it",
	)
}

func validateAtomicPublicationFile(
	want os.FileInfo,
	got os.FileInfo,
	mode os.FileMode,
	size int64,
	linked bool,
) error {
	linkStateSafe := atomicPublicationFileIsAnonymous(got)
	if linked {
		linkStateSafe = sourceAuditFileHasOneLink(got)
	}
	if want == nil || got == nil || !got.Mode().IsRegular() ||
		got.Mode()&os.ModeSymlink != 0 || !os.SameFile(want, got) ||
		!linkStateSafe || !sourceAuditFileOwnedByCurrentUser(got) ||
		got.Mode().Perm() != mode.Perm() || got.Size() != size {
		return errors.New("atomic publication file changed unexpectedly")
	}
	return nil
}

func validateAtomicPublicationContent(file *os.File, data []byte) error {
	if file == nil {
		return errors.New("atomic publication descriptor is missing")
	}
	hasher := sha256.New()
	read, err := io.Copy(hasher, io.NewSectionReader(file, 0, int64(len(data))))
	if err != nil {
		return fmt.Errorf("hash descriptor-bound atomic publication: %w", err)
	}
	want := sha256.Sum256(data)
	if read != int64(len(data)) || !bytes.Equal(hasher.Sum(nil), want[:]) {
		return errors.New("descriptor-bound atomic publication content changed unexpectedly")
	}
	return nil
}

func wrapAtomicPublicationReclaimError(operation string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s failed anonymous atomic publication inode: %w", operation, err)
}

func openAtomicPublicationDirectoryForSync(
	parent *os.Root,
	want os.FileInfo,
) (*os.File, error) {
	directory, err := parent.Open(".")
	if err != nil {
		return nil, fmt.Errorf("open pinned output directory for sync: %w", err)
	}
	info, err := directory.Stat()
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("inspect pinned output directory for sync: %w", err),
			directory.Close(),
		)
	}
	if !info.IsDir() || !os.SameFile(want, info) || want.Mode() != info.Mode() {
		return nil, errors.Join(
			errors.New("pinned output directory identity changed before sync"),
			directory.Close(),
		)
	}
	return directory, nil
}

func ioErrShortWrite(written, wanted int) error {
	return fmt.Errorf(
		"write anonymous atomic publication file: short write: wrote %d of %d bytes",
		written,
		wanted,
	)
}
