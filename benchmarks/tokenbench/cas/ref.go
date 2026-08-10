// Package cas provides a small immutable, content-addressed object store.
//
// Objects are addressed by a strict SHA-256 reference. Writes are staged in a
// private transaction directory, synced, and published without replacing an
// existing object. Reads verify both filesystem invariants and object content.
package cas

import (
	"errors"
	"fmt"
	"strings"
)

const (
	digestPrefix = "sha256:"
	digestHexLen = 64
)

var (
	// ErrInvalidObjectRef means an ObjectRef is not in the canonical form
	// accepted by this package.
	ErrInvalidObjectRef = errors.New("invalid CAS object reference")

	// ErrIntegrity means an object is absent or does not satisfy the immutable
	// store's path, file-type, link-count, size, mode, or digest invariants.
	ErrIntegrity = errors.New("CAS object integrity failure")

	// ErrTooLarge means an input or reference exceeds the store's configured
	// object-size bound.
	ErrTooLarge = errors.New("CAS object exceeds configured size limit")

	// ErrTransactionClosed means a transaction was already committed, aborted,
	// or consumed by a failed commit.
	ErrTransactionClosed = errors.New("CAS transaction is closed")

	// ErrCleanupPending means Commit or Abort could not finish removing the
	// transaction's private staging state. Abort may be retried while the Store
	// remains open.
	ErrCleanupPending = errors.New("CAS transaction cleanup is incomplete")

	// ErrPublicationUnknown means an atomic publication operation reported an
	// error and the pinned source and destination directories no longer proved
	// whether the staged inode became canonically addressable. The transaction
	// is consumed; callers may Verify the intended root and must use Abort only
	// for staging cleanup.
	ErrPublicationUnknown = errors.New("CAS object publication outcome is unknown")

	// ErrTransactionsActive means stale-transaction recovery was skipped
	// because at least one process still holds a live transaction lease.
	ErrTransactionsActive = errors.New("CAS transactions are active")

	// ErrRootPublished means Commit published, or verified an existing copy of,
	// the designated root, but a later durability or staging-cleanup step
	// failed. The caller must not republish the transaction; Abort may be used
	// to retry cleanup.
	ErrRootPublished = errors.New("CAS transaction root was published")
)

// ObjectRef identifies exact immutable bytes and carries their asserted media
// type. Digest is always "sha256:" followed by 64 lowercase hexadecimal
// characters. MediaType is a lowercase type/subtype without parameters.
type ObjectRef struct {
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
	MediaType string `json:"media_type"`
}

// CommitState reports the strongest fact proved about the designated root.
// Errors remain diagnostic; callers must use this state for recovery choices.
type CommitState string

const (
	CommitRetryable     CommitState = "retryable"
	CommitDurable       CommitState = "durable"
	CommitVisible       CommitState = "visible"
	CommitIndeterminate CommitState = "indeterminate"
)

// CommitResult is returned by CommitDetailed even when publication reports an
// error. Root is always the caller's intended root. UncertainObject and
// UncertainStage identify the exact boundary that could not be classified.
type CommitResult struct {
	Root            ObjectRef
	UncertainObject *ObjectRef
	UncertainStage  string
	State           CommitState
	Durable         bool
	CleanupPending  bool
}

// ObjectOperationError identifies the exact immutable object and operation
// boundary that failed during an idempotent maintenance operation. Callers can
// recover it with errors.As without parsing an error string.
type ObjectOperationError struct {
	Ref   ObjectRef
	Stage string
	Err   error
}

func (err *ObjectOperationError) Error() string {
	if err == nil {
		return "CAS object operation failed"
	}
	return fmt.Sprintf(
		"CAS object %s operation %s failed: %v",
		err.Ref.Digest,
		err.Stage,
		err.Err,
	)
}

// Unwrap preserves errors.Is/errors.As classification for the underlying
// filesystem, context, or integrity failure.
func (err *ObjectOperationError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Err
}

// Validate checks the typed commit-state invariants.
func (result CommitResult) Validate() error {
	if err := result.Root.Validate(); err != nil {
		return fmt.Errorf("commit root: %w", err)
	}
	if (result.UncertainObject == nil) != (result.UncertainStage == "") {
		return errors.New("commit uncertainty object and stage must appear together")
	}
	if result.UncertainObject != nil {
		if err := result.UncertainObject.Validate(); err != nil {
			return fmt.Errorf("commit uncertain object: %w", err)
		}
	}
	switch result.State {
	case CommitDurable:
		if !result.Durable || result.UncertainObject != nil {
			return errors.New("durable commit result is inconsistent")
		}
	case CommitVisible:
		if result.Durable || result.UncertainObject == nil {
			return errors.New("visible commit result is inconsistent")
		}
	case CommitIndeterminate:
		if result.Durable || result.UncertainObject == nil {
			return errors.New("indeterminate commit result is inconsistent")
		}
	case CommitRetryable:
		if result.Durable || result.UncertainObject != nil {
			return errors.New("retryable commit result is inconsistent")
		}
	default:
		return errors.New("commit state is invalid")
	}
	return nil
}

// Validate checks that ref is canonical. It does not read an object.
func (ref ObjectRef) Validate() error {
	if len(ref.Digest) != len(digestPrefix)+digestHexLen ||
		!strings.HasPrefix(ref.Digest, digestPrefix) {
		return fmt.Errorf(
			"%w: digest must be sha256 followed by 64 lowercase hex characters",
			ErrInvalidObjectRef,
		)
	}
	for _, character := range ref.Digest[len(digestPrefix):] {
		if !isLowerHex(character) {
			return fmt.Errorf(
				"%w: digest must be sha256 followed by 64 lowercase hex characters",
				ErrInvalidObjectRef,
			)
		}
	}
	if ref.Size < 0 {
		return fmt.Errorf("%w: size must not be negative", ErrInvalidObjectRef)
	}
	if !validMediaType(ref.MediaType) {
		return fmt.Errorf(
			"%w: media type must be a lowercase type/subtype without parameters",
			ErrInvalidObjectRef,
		)
	}
	return nil
}

func (ref ObjectRef) hexDigest() string {
	return ref.Digest[len(digestPrefix):]
}

func isLowerHex(character rune) bool {
	return character >= '0' && character <= '9' ||
		character >= 'a' && character <= 'f'
}

func validMediaType(value string) bool {
	if value == "" || len(value) > 255 || value != strings.ToLower(value) {
		return false
	}
	parts := strings.Split(value, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return false
	}
	for _, part := range parts {
		for _, character := range part {
			if !isMediaTypeCharacter(character) {
				return false
			}
		}
	}
	return true
}

func isMediaTypeCharacter(character rune) bool {
	return character >= 'a' && character <= 'z' ||
		character >= '0' && character <= '9' ||
		strings.ContainsRune("!#$&^_.+-", character)
}
