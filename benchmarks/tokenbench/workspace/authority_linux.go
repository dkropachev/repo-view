//go:build linux

package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sync"

	"github.com/scopesifter/scopesifter/benchmarks/tokenbench/snapshot"
	"golang.org/x/sys/unix"
)

const (
	worktreeDirectory = "worktree"
	upperDirectory    = "upper"
	workDirectory     = "work"
	cacheDirectory    = "cache"
	captureDirectory  = "capture"
)

// PairAuthority owns the private bounded tmpfs shared sequentially by both
// arms. Only Prepare can construct live state; decoded Inputs are never used as
// authority.
type PairAuthority struct {
	underlyingInfo os.FileInfo
	mountedInfo    os.FileInfo
	closeErr       error
	snapshot       *snapshot.Authority
	active         *ArmAuthority
	underlyingRoot *os.File
	mountedRoot    *os.File
	rootMountFile  *os.File
	paths          ArmPaths
	rootPath       string
	rootMount      mountRecord
	parentMount    mountRecord
	baseManifest   []worktreeEntry
	inputs         Inputs
	namespace      namespaceIdentity
	mu             sync.Mutex
	mounted        bool
	closing        bool
	tainted        bool
	released       bool
}

// ArmAuthority owns one fresh overlay and its fixed model-visible paths. It is
// one-shot and cannot be reconstructed from ArmPaths.
type ArmAuthority struct {
	overlayInfo  os.FileInfo
	closeErr     error
	overlayFile  *os.File
	target       *os.File
	work         *os.File
	cache        *os.File
	pair         *PairAuthority
	overlayRoot  *os.File
	capture      *os.File
	upper        *os.File
	lower        *os.File
	paths        ArmPaths
	layoutClaims []directoryClaim
	inodeReserve []*os.File
	overlayMount mountRecord
	mounted      bool
	closing      bool
	released     bool
}

// Prepare creates a code-owned bounded tmpfs only after binding it to a live
// immutable snapshot authority. It does not create an arm overlay.
func Prepare(
	ctx context.Context,
	snapshotAuthority *snapshot.Authority,
	request PrepareRequest,
) (_ *PairAuthority, resultErr error) {
	if ctx == nil {
		return nil, errors.New("workspace context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if snapshotAuthority == nil {
		return nil, errors.New("live snapshot authority is required")
	}
	if !validAbsoluteNonRootPath(request.Root) || filepath.Clean(request.Root) != request.Root {
		return nil, errors.New("workspace root must be absolute, canonical, and non-root")
	}
	if err := request.Limits.Validate(); err != nil {
		return nil, fmt.Errorf("validate workspace limits: %w", err)
	}
	if err := snapshotAuthority.Reverify(ctx); err != nil {
		return nil, fmt.Errorf("reverify snapshot before workspace preparation: %w", err)
	}
	snapshotInputs, err := snapshotAuthority.Inputs()
	if err != nil {
		return nil, err
	}
	if err := validateCodeSnapshotInputs(snapshotInputs); err != nil {
		return nil, err
	}
	if pathsOverlap(request.Root, snapshotInputs.SnapshotRoot) {
		return nil, errors.New("workspace root must be disjoint from the immutable snapshot")
	}
	baseManifest, err := expectedWorktreeManifest(snapshotInputs, request.Limits)
	if err != nil {
		return nil, err
	}

	namespace, err := currentMountNamespaceIdentity()
	if err != nil {
		return nil, fmt.Errorf("identify workspace mount namespace: %w", err)
	}
	if namespace.device != snapshotInputs.PathIsolation.MountNamespaceDevice ||
		namespace.inode != snapshotInputs.PathIsolation.MountNamespaceInode {
		return nil, errors.New("workspace and immutable snapshot are in different mount namespaces")
	}
	parentMount, err := containingPrivateMount(request.Root)
	if err != nil {
		return nil, fmt.Errorf("verify workspace parent mount: %w", err)
	}
	underlyingRoot, underlyingInfo, err := retainWorkspaceRoot(request.Root)
	if err != nil {
		return nil, err
	}

	pair := &PairAuthority{
		snapshot:       snapshotAuthority,
		underlyingRoot: underlyingRoot,
		underlyingInfo: underlyingInfo,
		rootPath:       request.Root,
		namespace:      namespace,
		parentMount:    parentMount,
		baseManifest:   baseManifest,
	}
	defer func() {
		if resultErr == nil {
			return
		}
		pair.mu.Lock()
		cleanupErr := pair.closeLocked()
		pair.mu.Unlock()
		resultErr = errors.Join(resultErr, cleanupErr)
	}()

	if err := pair.attachBoundedTmpfs(request.Limits); err != nil {
		return nil, err
	}
	pair.paths = ArmPaths{
		ModelRoot: filepath.Join(request.Root, worktreeDirectory),
		CacheRoot: filepath.Join(request.Root, cacheDirectory),
	}
	pair.inputs = Inputs{
		SchemaVersion:      InputsSchemaVersion,
		ModelRoot:          pair.paths.ModelRoot,
		ImmutableLowerRoot: snapshotInputs.SourceRoot,
		BaseTreeSHA256:     snapshotInputs.SourceTreeSHA256,
		SnapshotCommitment: snapshotInputs.Commitment,
		ChangedStateSHA256: snapshotInputs.ChangedState.SHA256,
		MountPolicySHA256:  requiredMountPolicySHA256,
		Limits:             request.Limits,
	}
	pair.inputs.Commitment = pair.inputs.ComputeCommitment()
	if err := pair.inputs.Validate(); err != nil {
		return nil, fmt.Errorf("validate prepared workspace inputs: %w", err)
	}
	if err := pair.reverifyLocked(ctx, false); err != nil {
		return nil, err
	}
	return pair, nil
}

func validateCodeSnapshotInputs(inputs snapshot.ExecutionInputs) error {
	if err := inputs.Validate(); err != nil {
		return fmt.Errorf("validate snapshot inputs for code workspace: %w", err)
	}
	return validateCodeSnapshotState(inputs)
}

func validateCodeSnapshotState(inputs snapshot.ExecutionInputs) error {
	switch {
	case inputs.SourceRevision != inputs.SourceBaseRevision:
		return errors.New("code workspace requires identical snapshot source and base revisions")
	case inputs.ChangedStateCache.BaseCommit != inputs.SourceRevision ||
		inputs.ChangedStateCache.HeadCommit != inputs.SourceRevision:
		return errors.New("code workspace changed-state revisions do not match the source revision")
	case len(inputs.ChangedStateCache.ChangedFiles) != 0 ||
		inputs.ChangedStateCache.Patch != "" ||
		inputs.ChangedState.ChangedFileCount != 0 ||
		inputs.ChangedState.PatchBytes != 0 ||
		inputs.ChangedState.PerFilePatchBytes != 0:
		return errors.New("code workspace requires an empty initial changed-state cache")
	default:
		return nil
	}
}

// Inputs returns a defensive copy of the audit commitment while the pair is
// live. It does not confer mount authority.
func (pair *PairAuthority) Inputs() (Inputs, error) {
	if pair == nil {
		return Inputs{}, errors.New("workspace pair authority is closed")
	}
	pair.mu.Lock()
	defer pair.mu.Unlock()
	if pair.closing || pair.released {
		return Inputs{}, errors.New("workspace pair authority is closed")
	}
	return pair.inputs, nil
}

// Reverify proves the live tmpfs, immutable lower snapshot, and any active
// overlay still match their retained kernel identities.
func (pair *PairAuthority) Reverify(ctx context.Context) error {
	if ctx == nil {
		return errors.New("workspace context is required")
	}
	if pair == nil {
		return errors.New("workspace pair authority is closed")
	}
	pair.mu.Lock()
	defer pair.mu.Unlock()
	return pair.reverifyLocked(ctx, true)
}

func (pair *PairAuthority) reverifyLocked(ctx context.Context, includeArm bool) error {
	if pair.closing || pair.released || !pair.mounted {
		return errors.New("workspace pair authority is closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := pair.inputs.Validate(); err != nil {
		return err
	}
	if current, err := currentMountNamespaceIdentity(); err != nil || current != pair.namespace {
		return errors.Join(errors.New("workspace mount namespace changed"), err)
	}
	if err := pair.snapshot.Reverify(ctx); err != nil {
		return fmt.Errorf("reverify workspace immutable lower snapshot: %w", err)
	}
	currentInputs, err := pair.snapshot.Inputs()
	if err != nil {
		return err
	}
	if currentInputs.Commitment != pair.inputs.SnapshotCommitment ||
		currentInputs.SourceRoot != pair.inputs.ImmutableLowerRoot ||
		currentInputs.SourceTreeSHA256 != pair.inputs.BaseTreeSHA256 ||
		currentInputs.ChangedState.SHA256 != pair.inputs.ChangedStateSHA256 {
		return errors.New("workspace immutable lower snapshot identity changed")
	}
	if err := verifyMountRecord(pair.rootPath, pair.mountedInfo, pair.rootMount); err != nil {
		return fmt.Errorf("reverify workspace tmpfs: %w", err)
	}
	if err := verifyTmpfsBounds(pair.mountedRoot, pair.inputs.Limits); err != nil {
		return err
	}
	if parent, err := mountRecordByID(pair.parentMount.id); err != nil ||
		!reflect.DeepEqual(parent, pair.parentMount) {
		return errors.Join(errors.New("workspace parent mount identity changed"), err)
	}
	if includeArm && pair.active != nil {
		if err := pair.active.reverifyLocked(ctx, false); err != nil {
			return err
		}
	}
	return nil
}

// BeginArm creates one fresh overlay without accepting an arm label, order,
// repetition, writable path, or cache path from its caller.
func (pair *PairAuthority) BeginArm(ctx context.Context) (_ *ArmAuthority, resultErr error) {
	if ctx == nil {
		return nil, errors.New("workspace context is required")
	}
	if pair == nil {
		return nil, errors.New("workspace pair authority is closed")
	}
	pair.mu.Lock()
	defer pair.mu.Unlock()
	if err := pair.reverifyLocked(ctx, false); err != nil {
		return nil, err
	}
	if pair.active != nil {
		return nil, errors.New("workspace pair already has an active arm")
	}

	arm := &ArmAuthority{pair: pair, paths: pair.paths}
	pair.active = arm
	defer func() {
		if resultErr == nil {
			return
		}
		cleanupErr := arm.closeLocked()
		if pair.tainted && pair.active == nil {
			cleanupErr = errors.Join(cleanupErr, pair.closeLocked())
		}
		resultErr = errors.Join(resultErr, cleanupErr)
	}()
	if err := arm.createLocked(ctx); err != nil {
		return nil, err
	}
	return arm, nil
}

func (arm *ArmAuthority) createLocked(ctx context.Context) error {
	var err error
	arm.lower, err = openDirectoryNoSymlinks(arm.pair.inputs.ImmutableLowerRoot)
	if err != nil {
		return fmt.Errorf("open immutable workspace lower root: %w", err)
	}
	arm.target, err = arm.createDirectoryLocked(worktreeDirectory)
	if err == nil {
		arm.upper, err = arm.createDirectoryLocked(upperDirectory)
	}
	if err == nil {
		arm.work, err = arm.createDirectoryLocked(workDirectory)
	}
	if err == nil {
		arm.cache, err = arm.createDirectoryLocked(cacheDirectory)
	}
	if err == nil {
		arm.capture, err = arm.createDirectoryLocked(captureDirectory)
	}
	if err != nil {
		return fmt.Errorf("create fresh workspace layout: %w", err)
	}
	if err := createGitWhiteout(arm.upper); err != nil {
		return err
	}
	if err := arm.attachOverlay(); err != nil {
		return err
	}
	if err := arm.retainInodeBudget(); err != nil {
		return err
	}
	return arm.requireFreshLocked(ctx)
}

func (arm *ArmAuthority) createDirectoryLocked(name string) (*os.File, error) {
	directory, claim, err := createDirectoryAt(arm.pair.mountedRoot, name)
	if claim.created() {
		arm.layoutClaims = append(arm.layoutClaims, claim)
		if !claim.valid() {
			arm.pair.tainted = true
			arm.pair.closing = true
		}
	}
	return directory, err
}

// Paths returns the sole stable paths intended for the contained model. Upper,
// work, capture, and immutable-lower paths are not exposed here.
func (arm *ArmAuthority) Paths() (ArmPaths, error) {
	if arm == nil || arm.pair == nil {
		return ArmPaths{}, errors.New("workspace arm authority is closed")
	}
	arm.pair.mu.Lock()
	defer arm.pair.mu.Unlock()
	if arm.pair.active != arm || arm.closing || arm.released {
		return ArmPaths{}, errors.New("workspace arm authority is closed")
	}
	return arm.paths, nil
}

// RequireFresh repeats the initial-tree, empty-cache, mount, and snapshot
// proofs immediately before a future runner launches a child.
func (arm *ArmAuthority) RequireFresh(ctx context.Context) error {
	if ctx == nil {
		return errors.New("workspace context is required")
	}
	if arm == nil || arm.pair == nil {
		return errors.New("workspace arm authority is closed")
	}
	arm.pair.mu.Lock()
	defer arm.pair.mu.Unlock()
	return arm.requireFreshLocked(ctx)
}

func (arm *ArmAuthority) requireFreshLocked(ctx context.Context) error {
	if err := arm.reverifyLocked(ctx, true); err != nil {
		return err
	}
	if err := directoryIsEmpty(arm.cache); err != nil {
		return fmt.Errorf("workspace cache is not fresh: %w", err)
	}
	if err := directoryIsEmpty(arm.capture); err != nil {
		return fmt.Errorf("workspace capture scratch is not fresh: %w", err)
	}
	if err := verifyInitialWorktree(
		ctx,
		arm.overlayRoot,
		arm.pair.baseManifest,
		arm.pair.inputs.Limits,
	); err != nil {
		return err
	}
	return nil
}

// Reverify proves the arm mount and its parent pair remain live. It does not
// require the intentionally writable tree to remain fresh.
func (arm *ArmAuthority) Reverify(ctx context.Context) error {
	if ctx == nil {
		return errors.New("workspace context is required")
	}
	if arm == nil || arm.pair == nil {
		return errors.New("workspace arm authority is closed")
	}
	arm.pair.mu.Lock()
	defer arm.pair.mu.Unlock()
	return arm.reverifyLocked(ctx, true)
}

func (arm *ArmAuthority) reverifyLocked(ctx context.Context, verifyPair bool) error {
	if arm.pair.active != arm || arm.closing || arm.released || !arm.mounted {
		return errors.New("workspace arm authority is closed")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if verifyPair {
		if err := arm.pair.reverifyLocked(ctx, false); err != nil {
			return err
		}
	}
	info, err := arm.overlayRoot.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(arm.overlayInfo, info) {
		return errors.New("workspace overlay descriptor identity changed")
	}
	if err := verifyMountRecord(arm.paths.ModelRoot, info, arm.overlayMount); err != nil {
		return fmt.Errorf("reverify workspace overlay: %w", err)
	}
	if arm.overlayMount.parentID != arm.pair.rootMount.id {
		return errors.New("workspace overlay escaped its bounded tmpfs parent")
	}
	if err := arm.verifyPrivateLayoutLocked(); err != nil {
		return err
	}
	if err := verifyArmMountTopology(arm.pair.rootPath, arm.overlayMount); err != nil {
		return err
	}
	return nil
}

func (arm *ArmAuthority) verifyPrivateLayoutLocked() error {
	for _, directory := range []struct {
		file *os.File
		name string
	}{
		{file: arm.upper, name: upperDirectory},
		{file: arm.work, name: workDirectory},
		{file: arm.cache, name: cacheDirectory},
		{file: arm.capture, name: captureDirectory},
	} {
		if err := verifyDirectoryPathAt(
			arm.pair.mountedRoot,
			directory.name,
			directory.file,
		); err != nil {
			return fmt.Errorf("reverify workspace %s directory: %w", directory.name, err)
		}
	}
	return nil
}

// Close normally unmounts and removes this arm. A failed attempt keeps the arm
// active so the same retained authority can retry cleanup.
func (arm *ArmAuthority) Close() error {
	if arm == nil || arm.pair == nil {
		return nil
	}
	arm.pair.mu.Lock()
	defer arm.pair.mu.Unlock()
	return arm.closeLocked()
}

func (arm *ArmAuthority) closeLocked() error {
	if arm.released {
		return nil
	}
	arm.closing = true
	var attemptErr error
	if arm.mounted {
		record, err := mountpointForUnmount(arm.overlayFile, arm.overlayInfo, arm.overlayMount)
		if err != nil {
			attemptErr = errors.Join(attemptErr, fmt.Errorf("resolve retained workspace overlay: %w", err))
		} else if err := arm.verifyPrivateLayoutLocked(); err != nil {
			attemptErr = errors.Join(attemptErr, err)
		} else if err := rejectMountDescendants(record.id); err != nil {
			attemptErr = errors.Join(attemptErr, err)
		} else {
			if arm.overlayMount.id == 0 {
				arm.overlayMount = record
			}
			attemptErr = errors.Join(
				attemptErr,
				closeFile(&arm.overlayRoot, "workspace overlay descriptor"),
				closeFile(&arm.overlayFile, "workspace overlay mount descriptor"),
			)
			if err := unix.Unmount(record.point, unix.UMOUNT_NOFOLLOW); err != nil {
				attemptErr = errors.Join(attemptErr, fmt.Errorf("unmount retained workspace overlay: %w", err))
			} else {
				arm.mounted = false
			}
		}
	}
	if arm.mounted {
		arm.closeErr = attemptErr
		return arm.closeErr
	}
	for index := range arm.inodeReserve {
		attemptErr = errors.Join(
			attemptErr,
			closeFile(&arm.inodeReserve[index], "workspace inode reserve descriptor"),
		)
	}
	arm.inodeReserve = nil
	attemptErr = errors.Join(
		attemptErr,
		closeFile(&arm.target, "workspace target descriptor"),
		closeFile(&arm.upper, "workspace upper descriptor"),
		closeFile(&arm.work, "workspace work descriptor"),
		closeFile(&arm.cache, "workspace cache descriptor"),
		closeFile(&arm.capture, "workspace capture descriptor"),
		closeFile(&arm.lower, "workspace lower descriptor"),
	)
	switch {
	case len(arm.layoutClaims) == 0:
		// Construction can fail before the first owned layout inode exists.
	case arm.pair.tainted:
		// If the kernel could not identify a just-created directory, no
		// pathname is safe to remove. The entire code-owned tmpfs is normally
		// unmounted by BeginArm's failure cleanup instead.
	case arm.pair.mountedRoot == nil:
		attemptErr = errors.Join(attemptErr, errors.New("workspace tmpfs descriptor is absent"))
	default:
		var topologyErr error
		if arm.pair.rootMount.id != 0 {
			topologyErr = rejectMountDescendants(arm.pair.rootMount.id)
		}
		if topologyErr != nil {
			attemptErr = errors.Join(attemptErr, topologyErr)
		} else {
			attemptErr = errors.Join(
				attemptErr,
				removeArmLayout(arm.pair.mountedRoot, arm.layoutClaims, arm.pair.inputs.Limits),
			)
		}
	}
	arm.closeErr = attemptErr
	arm.released = attemptErr == nil
	if arm.released {
		arm.closing = false
		arm.pair.active = nil
	}
	return arm.closeErr
}

func closeFile(file **os.File, description string) error {
	if file == nil || *file == nil {
		return nil
	}
	err := (*file).Close()
	*file = nil
	if err != nil {
		return fmt.Errorf("close %s: %w", description, err)
	}
	return nil
}

// Closed reports strong cleanup success, not merely that Close was attempted.
func (arm *ArmAuthority) Closed() bool {
	if arm == nil || arm.pair == nil {
		return false
	}
	arm.pair.mu.Lock()
	defer arm.pair.mu.Unlock()
	return arm.released && !arm.mounted && arm.pair.active != arm &&
		arm.overlayRoot == nil && arm.overlayFile == nil && arm.target == nil &&
		arm.upper == nil && arm.work == nil && arm.cache == nil &&
		arm.capture == nil && arm.lower == nil && len(arm.inodeReserve) == 0
}

// Close releases the active arm first and then the exact tmpfs, descriptors,
// and claimed underlying directory. No lazy unmount is used.
func (pair *PairAuthority) Close() error {
	if pair == nil {
		return nil
	}
	pair.mu.Lock()
	defer pair.mu.Unlock()
	return pair.closeLocked()
}

func (pair *PairAuthority) closeLocked() error {
	if pair.released {
		return nil
	}
	pair.closing = true
	var attemptErr error
	if pair.active != nil {
		active := pair.active
		attemptErr = errors.Join(attemptErr, active.closeLocked())
		if !active.released {
			pair.closeErr = attemptErr
			return pair.closeErr
		}
	}
	if pair.mounted {
		record, err := mountpointForUnmount(pair.rootMountFile, pair.mountedInfo, pair.rootMount)
		if err != nil {
			attemptErr = errors.Join(attemptErr, fmt.Errorf("resolve retained workspace tmpfs: %w", err))
		} else if err := rejectMountDescendants(record.id); err != nil {
			attemptErr = errors.Join(attemptErr, err)
		} else {
			if pair.rootMount.id == 0 {
				pair.rootMount = record
			}
			attemptErr = errors.Join(
				attemptErr,
				closeFile(&pair.mountedRoot, "workspace tmpfs descriptor"),
				closeFile(&pair.rootMountFile, "workspace tmpfs mount descriptor"),
			)
			if err := unix.Unmount(record.point, unix.UMOUNT_NOFOLLOW); err != nil {
				attemptErr = errors.Join(attemptErr, fmt.Errorf("unmount retained workspace tmpfs: %w", err))
			} else {
				pair.mounted = false
			}
		}
	}
	if pair.mounted {
		pair.closeErr = attemptErr
		return pair.closeErr
	}
	if pair.underlyingRoot != nil {
		if err := verifyRetainedWorkspaceRootDescriptor(
			pair.underlyingRoot,
			pair.underlyingInfo,
		); err != nil {
			pair.closeErr = errors.Join(attemptErr, err)
			return pair.closeErr
		}
		if err := directoryIsEmpty(pair.underlyingRoot); err != nil {
			pair.closeErr = errors.Join(
				attemptErr,
				fmt.Errorf("borrowed workspace mountpoint is not empty after unmount: %w", err),
			)
			return pair.closeErr
		}
	}
	if pair.underlyingRoot != nil {
		attemptErr = errors.Join(attemptErr, closeFile(&pair.underlyingRoot, "borrowed workspace mountpoint descriptor"))
	}
	pair.closeErr = attemptErr
	pair.released = attemptErr == nil
	if pair.released {
		pair.closing = false
	}
	return pair.closeErr
}

// Closed reports whether every owned mount, descriptor, and claimed path was
// successfully released.
func (pair *PairAuthority) Closed() bool {
	if pair == nil {
		return false
	}
	pair.mu.Lock()
	defer pair.mu.Unlock()
	return pair.released && !pair.mounted && pair.active == nil &&
		pair.underlyingRoot == nil && pair.mountedRoot == nil && pair.rootMountFile == nil
}
