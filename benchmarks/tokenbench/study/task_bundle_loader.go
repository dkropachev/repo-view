package study

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"io"
	"reflect"

	"github.com/scopesifter/scopesifter/benchmarks/tokenbench/cas"
)

// TaskArtifactAuthority is the only capability an authenticated task bundle
// needs from its caller. A *cas.Store satisfies this interface. Implementations
// must treat destination as disposable when they return an error.
//
// The loader does not trust Copy to authenticate content: it independently
// enforces the exact size and SHA-256 identity while Copy streams the object.
type TaskArtifactAuthority interface {
	Copy(context.Context, cas.ObjectRef, io.Writer) error
}

// TaskArtifactAccessError identifies the exact task-artifact boundary that
// failed. Its rendered form never includes the authority's arbitrary error
// text, artifact bytes, or caller-controlled paths; Unwrap retains the cause.
type TaskArtifactAccessError struct {
	Err    error
	Object cas.ObjectRef
	TaskID string
	Role   TaskArtifactRole
	Stage  string
}

func (err *TaskArtifactAccessError) Error() string {
	if err == nil {
		return "task artifact access failed"
	}
	return fmt.Sprintf(
		"task %q role %q object %s %s failed",
		err.TaskID,
		err.Role,
		err.Object.Digest,
		err.Stage,
	)
}

// Unwrap preserves context, CAS-integrity, and authority error classification.
func (err *TaskArtifactAccessError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.Err
}

// AuthenticatedTaskBundle is a fail-closed, immutable view of one completely
// authenticated TaskBundle. It retains only typed identities and the caller's
// CAS authority; it never retains the referenced artifact bytes.
type AuthenticatedTaskBundle struct {
	authority     TaskArtifactAuthority
	catalogSHA256 string
	bundleSHA256  string
	taskIndexes   map[string]int
	tasks         [taskCatalogTaskCount]AuthenticatedTaskArtifacts
}

// AuthenticatedTaskArtifacts is one immutable task entry. Its fixed-size
// array is copied when returned, so callers cannot mutate the loaded bundle.
type AuthenticatedTaskArtifacts struct {
	taskID        string
	family        CatalogTaskFamily
	artifacts     [4]AuthenticatedTaskArtifact
	artifactCount int
}

// AuthenticatedTaskArtifact is a typed handle to one exact CAS object. Read
// authenticates the object again before returning any bytes, closing the gap
// between bundle loading and later use.
type AuthenticatedTaskArtifact struct {
	authority TaskArtifactAuthority
	object    cas.ObjectRef
	taskID    string
	role      TaskArtifactRole
}

// LoadAuthenticatedTaskBundle validates the complete catalog/bundle pair and
// authenticates each distinct referenced CAS object in deterministic
// task/role order. Exact duplicate references in the same role are read once.
// No usable result is returned after cancellation or a partial failure.
func LoadAuthenticatedTaskBundle(
	ctx context.Context,
	catalog TaskCatalog,
	bundle TaskBundle,
	authority TaskArtifactAuthority,
) (*AuthenticatedTaskBundle, error) {
	if ctx == nil {
		return nil, errors.New("task bundle load context is nil")
	}
	if nilTaskArtifactAuthority(authority) {
		return nil, errors.New("task bundle CAS authority is nil")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	catalog, bundle, err := snapshotTaskBundleInputs(catalog, bundle)
	if err != nil {
		return nil, err
	}
	bundleSHA256, err := bundle.SHA256(catalog)
	if err != nil {
		return nil, fmt.Errorf("digest task bundle before artifact load: %w", err)
	}

	loaded := &AuthenticatedTaskBundle{
		authority:     authority,
		catalogSHA256: bundle.CatalogSHA256,
		bundleSHA256:  bundleSHA256,
		taskIndexes:   make(map[string]int, len(bundle.Tasks)),
	}
	type seenObject struct {
		object cas.ObjectRef
		role   TaskArtifactRole
	}
	seen := make(map[string]seenObject, taskBundleArtifactCount)
	for taskIndex, task := range bundle.Tasks {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		loadedTask := AuthenticatedTaskArtifacts{
			taskID:        task.TaskID,
			family:        catalog.Tasks[taskIndex].Family,
			artifactCount: len(task.Artifacts),
		}
		for artifactIndex, artifact := range task.Artifacts {
			previous, duplicate := seen[artifact.Object.Digest]
			if duplicate {
				if previous.object != artifact.Object || previous.role != artifact.Role {
					return nil, &TaskArtifactAccessError{
						Err: fmt.Errorf(
							"%w: digest is ambiguous across object metadata or artifact roles",
							cas.ErrIntegrity,
						),
						Object: artifact.Object,
						TaskID: task.TaskID,
						Role:   artifact.Role,
						Stage:  "load",
					}
				}
			} else {
				if err := authenticateTaskArtifact(ctx, authority, artifact.Object, io.Discard); err != nil {
					return nil, &TaskArtifactAccessError{
						Err:    err,
						Object: artifact.Object,
						TaskID: task.TaskID,
						Role:   artifact.Role,
						Stage:  "load",
					}
				}
				seen[artifact.Object.Digest] = seenObject{
					object: artifact.Object,
					role:   artifact.Role,
				}
			}
			loadedTask.artifacts[artifactIndex] = AuthenticatedTaskArtifact{
				authority: authority,
				object:    artifact.Object,
				taskID:    task.TaskID,
				role:      artifact.Role,
			}
		}
		loaded.tasks[taskIndex] = loadedTask
		loaded.taskIndexes[task.TaskID] = taskIndex
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return loaded, nil
}

// snapshotTaskBundleInputs severs every caller-owned slice and pointer before
// invoking the reentrant CAS authority. Canonical encode/decode also makes the
// snapshot itself pass the same strict public validation boundaries.
func snapshotTaskBundleInputs(
	catalog TaskCatalog,
	bundle TaskBundle,
) (TaskCatalog, TaskBundle, error) {
	catalogRaw, err := EncodeTaskCatalog(catalog)
	if err != nil {
		return TaskCatalog{}, TaskBundle{}, fmt.Errorf(
			"snapshot task bundle catalog: %w",
			err,
		)
	}
	catalogSnapshot, err := DecodeTaskCatalog(catalogRaw)
	if err != nil {
		return TaskCatalog{}, TaskBundle{}, fmt.Errorf(
			"decode task bundle catalog snapshot: %w",
			err,
		)
	}
	bundleRaw, err := EncodeTaskBundle(catalogSnapshot, bundle)
	if err != nil {
		return TaskCatalog{}, TaskBundle{}, fmt.Errorf(
			"snapshot task bundle: %w",
			err,
		)
	}
	bundleSnapshot, err := DecodeTaskBundle(catalogSnapshot, bundleRaw)
	if err != nil {
		return TaskCatalog{}, TaskBundle{}, fmt.Errorf(
			"decode task bundle snapshot: %w",
			err,
		)
	}
	return catalogSnapshot, bundleSnapshot, nil
}

// CatalogSHA256 returns the exact catalog identity committed by the bundle.
func (bundle *AuthenticatedTaskBundle) CatalogSHA256() string {
	if bundle == nil {
		return ""
	}
	return bundle.catalogSHA256
}

// BundleSHA256 returns the canonical task-bundle identity authenticated by
// LoadAuthenticatedTaskBundle.
func (bundle *AuthenticatedTaskBundle) BundleSHA256() string {
	if bundle == nil {
		return ""
	}
	return bundle.bundleSHA256
}

// TaskIDs returns a fresh, deterministically sorted copy of all task IDs.
func (bundle *AuthenticatedTaskBundle) TaskIDs() []string {
	if bundle == nil {
		return nil
	}
	result := make([]string, len(bundle.tasks))
	for index := range bundle.tasks {
		result[index] = bundle.tasks[index].taskID
	}
	return result
}

// Task returns a copied typed task entry. Arbitrary path or URI strings do not
// resolve because only exact validated catalog task IDs are indexed.
func (bundle *AuthenticatedTaskBundle) Task(taskID string) (AuthenticatedTaskArtifacts, bool) {
	if bundle == nil {
		return AuthenticatedTaskArtifacts{}, false
	}
	index, exists := bundle.taskIndexes[taskID]
	if !exists {
		return AuthenticatedTaskArtifacts{}, false
	}
	return bundle.tasks[index], true
}

// TaskID returns this entry's validated catalog task ID.
func (task AuthenticatedTaskArtifacts) TaskID() string {
	return task.taskID
}

// Family returns this entry's validated catalog task family.
func (task AuthenticatedTaskArtifacts) Family() CatalogTaskFamily {
	return task.family
}

// Roles returns a fresh copy of the exact lexical role order for this task.
func (task AuthenticatedTaskArtifacts) Roles() []TaskArtifactRole {
	roles := make([]TaskArtifactRole, task.artifactCount)
	for index := range task.artifactCount {
		roles[index] = task.artifacts[index].role
	}
	return roles
}

// Artifact resolves only a role present in the validated family contract.
func (task AuthenticatedTaskArtifacts) Artifact(
	role TaskArtifactRole,
) (AuthenticatedTaskArtifact, bool) {
	for index := range task.artifactCount {
		if task.artifacts[index].role == role {
			return task.artifacts[index], true
		}
	}
	return AuthenticatedTaskArtifact{}, false
}

// TaskID returns the task to which this handle is bound.
func (artifact AuthenticatedTaskArtifact) TaskID() string {
	return artifact.taskID
}

// Role returns the closed artifact role for this handle.
func (artifact AuthenticatedTaskArtifact) Role() TaskArtifactRole {
	return artifact.role
}

// Object returns a value copy of the exact authenticated CAS identity.
func (artifact AuthenticatedTaskArtifact) Object() cas.ObjectRef {
	return artifact.object
}

// Verify reauthenticates an artifact without retaining its bytes.
func (artifact AuthenticatedTaskArtifact) Verify(ctx context.Context) error {
	if err := artifact.validateHandle(ctx); err != nil {
		return err
	}
	if err := authenticateTaskArtifact(ctx, artifact.authority, artifact.object, io.Discard); err != nil {
		return &TaskArtifactAccessError{
			Err:    err,
			Object: artifact.object,
			TaskID: artifact.taskID,
			Role:   artifact.role,
			Stage:  "verify",
		}
	}
	return nil
}

// Read reauthenticates and returns one fresh caller-owned artifact byte slice.
// Allocation is bounded by the role ceiling (at most 64 MiB); bytes from a
// corrupt, replaced, short, oversized, canceled, or failed read are discarded.
func (artifact AuthenticatedTaskArtifact) Read(ctx context.Context) ([]byte, error) {
	if err := artifact.validateHandle(ctx); err != nil {
		return nil, err
	}
	var content bytes.Buffer
	content.Grow(int(artifact.object.Size))
	if err := authenticateTaskArtifact(ctx, artifact.authority, artifact.object, &content); err != nil {
		return nil, &TaskArtifactAccessError{
			Err:    err,
			Object: artifact.object,
			TaskID: artifact.taskID,
			Role:   artifact.role,
			Stage:  "read",
		}
	}
	return content.Bytes(), nil
}

func (artifact AuthenticatedTaskArtifact) validateHandle(ctx context.Context) error {
	if ctx == nil {
		return errors.New("task artifact context is nil")
	}
	if nilTaskArtifactAuthority(artifact.authority) || artifact.taskID == "" || artifact.role == "" {
		return errors.New("task artifact handle is uninitialized")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	return nil
}

func nilTaskArtifactAuthority(authority TaskArtifactAuthority) bool {
	if authority == nil {
		return true
	}
	value := reflect.ValueOf(authority)
	kind := value.Kind()
	if kind == reflect.Chan || kind == reflect.Func || kind == reflect.Interface ||
		kind == reflect.Map || kind == reflect.Pointer || kind == reflect.Slice {
		return value.IsNil()
	}
	return false
}

func authenticateTaskArtifact(
	ctx context.Context,
	authority TaskArtifactAuthority,
	ref cas.ObjectRef,
	destination io.Writer,
) error {
	if err := ref.Validate(); err != nil {
		return fmt.Errorf("validate CAS identity: %w", err)
	}
	if destination == nil {
		return errors.New("task artifact authentication destination is nil")
	}
	writer := &taskArtifactDigestWriter{
		ctx:          ctx,
		destination:  destination,
		digest:       sha256.New(),
		expectedSize: ref.Size,
	}
	copyErr := authority.Copy(ctx, ref, writer)
	if writer.err != nil || copyErr != nil {
		return errors.Join(writer.err, copyErr)
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if writer.size != ref.Size {
		return fmt.Errorf(
			"%w: authority yielded %d bytes, want %d",
			cas.ErrIntegrity,
			writer.size,
			ref.Size,
		)
	}
	gotDigest := hex.EncodeToString(writer.digest.Sum(nil))
	if "sha256:"+gotDigest != ref.Digest {
		return fmt.Errorf(
			"%w: authority yielded digest sha256:%s, want %s",
			cas.ErrIntegrity,
			gotDigest,
			ref.Digest,
		)
	}
	return nil
}

type taskArtifactDigestWriter struct {
	ctx          context.Context
	destination  io.Writer
	digest       hash.Hash
	err          error
	expectedSize int64
	size         int64
}

func (writer *taskArtifactDigestWriter) Write(content []byte) (int, error) {
	if writer.err != nil {
		return 0, writer.err
	}
	if err := writer.ctx.Err(); err != nil {
		writer.err = err
		return 0, err
	}
	if int64(len(content)) > writer.expectedSize-writer.size {
		writer.err = fmt.Errorf(
			"%w: authority yielded more than %d bytes",
			cas.ErrIntegrity,
			writer.expectedSize,
		)
		return 0, writer.err
	}
	if _, err := writer.digest.Write(content); err != nil {
		writer.err = fmt.Errorf("hash task artifact: %w", err)
		return 0, writer.err
	}
	written, err := writer.destination.Write(content)
	if err != nil {
		writer.err = fmt.Errorf("retain authenticated task artifact: %w", err)
		return written, writer.err
	}
	if written != len(content) {
		writer.err = io.ErrShortWrite
		return written, writer.err
	}
	writer.size += int64(written)
	return written, nil
}
