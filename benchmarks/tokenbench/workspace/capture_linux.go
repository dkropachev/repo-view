//go:build linux

package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"

	"golang.org/x/sys/unix"
)

// Capture freezes this arm read-only and derives one bounded result from the
// exact merged tree. A future code runner is responsible for calling it only
// after it has synchronously proved the arm cgroup empty. Capture accepts no
// caller-authored path, command, Git identity, or outcome field.
func (arm *ArmAuthority) Capture(ctx context.Context) (Outcome, error) {
	if ctx == nil {
		return Outcome{}, errors.New("workspace context is required")
	}
	if arm == nil || arm.pair == nil {
		return Outcome{}, errors.New("workspace arm authority is closed")
	}
	arm.pair.mu.Lock()
	defer arm.pair.mu.Unlock()
	if arm.outcome != nil {
		return cloneOutcome(*arm.outcome), nil
	}
	if arm.captureErr != nil {
		return Outcome{}, arm.captureErr
	}
	if arm.pair.active != arm || arm.closing || arm.released || arm.capturing {
		return Outcome{}, errors.New("workspace arm authority is closed")
	}
	arm.capturing = true
	outcome, err := arm.captureLocked(ctx)
	arm.capturing = false
	if err != nil {
		arm.captureErr = err
		return Outcome{}, err
	}
	arm.outcome = &outcome
	return cloneOutcome(outcome), nil
}

func (arm *ArmAuthority) captureLocked(ctx context.Context) (Outcome, error) {
	if err := arm.reverifyLocked(ctx, true); err != nil {
		return Outcome{}, err
	}
	if err := arm.freezeLocked(); err != nil {
		return Outcome{}, err
	}
	if err := arm.releaseCaptureInodeReserve(); err != nil {
		return Outcome{}, err
	}
	if err := arm.reverifyLocked(ctx, true); err != nil {
		return Outcome{}, err
	}
	result, err := scanCapturedWorktree(
		ctx,
		arm.overlayRoot,
		arm.pair.baseManifest,
		arm.pair.inputs.Limits,
	)
	initialDigest := worktreeManifestDigest(arm.pair.baseManifest)
	if err != nil {
		status := StatusInvalidTree
		violation := "invalid_workspace"
		switch {
		case errors.Is(err, errWorkspaceTreeLimit):
			status = StatusLimitExceeded
			violation = "workspace_tree_limit"
		case !errors.Is(err, errInvalidWorkspaceTree):
			return Outcome{}, fmt.Errorf("scan frozen workspace for capture: %w", err)
		}
		outcome := Outcome{
			SchemaVersion:     OutcomeSchemaVersion,
			Status:            status,
			InitialTreeSHA256: initialDigest,
			ViolationCode:     violation,
		}
		finalized, validateErr := arm.finalizeCaptureOutcome(ctx, outcome, nil)
		if validateErr != nil {
			return Outcome{}, errors.Join(err, validateErr)
		}
		return finalized, nil
	}
	return arm.capturePatchLocked(ctx, initialDigest, result)
}

func (arm *ArmAuthority) releaseCaptureInodeReserve() error {
	if !arm.frozen || arm.capture == nil {
		return errors.New("workspace must be frozen before releasing capture infrastructure")
	}
	var releaseErr error
	for index := range arm.inodeReserve {
		releaseErr = errors.Join(
			releaseErr,
			closeFile(&arm.inodeReserve[index], "workspace capture inode reserve descriptor"),
		)
	}
	arm.inodeReserve = nil
	if releaseErr != nil {
		return releaseErr
	}
	if err := arm.capture.Sync(); err != nil {
		return fmt.Errorf("sync released capture inode reserve: %w", err)
	}
	return nil
}

func (arm *ArmAuthority) freezeLocked() error {
	if arm.frozen {
		return errors.New("workspace arm is already frozen")
	}
	if err := unix.Syncfs(int(arm.overlayRoot.Fd())); err != nil {
		return fmt.Errorf("sync writable workspace before capture: %w", err)
	}
	before := arm.overlayMount
	if err := unix.MountSetattr(
		int(arm.overlayRoot.Fd()),
		"",
		uint(unix.AT_EMPTY_PATH),
		&unix.MountAttr{Attr_set: unix.MOUNT_ATTR_RDONLY},
	); err != nil {
		return fmt.Errorf("freeze writable workspace read-only: %w", err)
	}
	arm.frozen = true
	after, err := mountRecordForDescriptor(arm.overlayFile)
	if err != nil {
		return fmt.Errorf("read frozen workspace mount identity: %w", err)
	}
	if err := validateFrozenOverlayIdentity(before, after); err != nil {
		return err
	}
	arm.overlayMount = after
	if err := validateFrozenOverlayOptions(before.options, after.options); err != nil {
		return err
	}
	info, err := arm.overlayRoot.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(arm.overlayInfo, info) {
		return errors.New("frozen workspace overlay inode identity changed")
	}
	if err := verifyMountRecord(arm.paths.ModelRoot, info, arm.overlayMount); err != nil {
		return fmt.Errorf("verify frozen workspace overlay: %w", err)
	}
	arm.freezeVerified = true
	return nil
}

func validateFrozenOverlayTransition(before, after mountRecord) error {
	if err := validateFrozenOverlayIdentity(before, after); err != nil {
		return err
	}
	return validateFrozenOverlayOptions(before.options, after.options)
}

func validateFrozenOverlayIdentity(before, after mountRecord) error {
	before.options = nil
	after.options = nil
	before.point, after.point = "", ""
	before.parentID, after.parentID = 0, 0
	if !reflect.DeepEqual(before, after) {
		return errors.New("workspace overlay identity changed while freezing")
	}
	return nil
}

func validateFrozenOverlayOptions(beforeOptions, afterOptions []string) error {
	if !hasMountOption(beforeOptions, "rw") || hasMountOption(beforeOptions, "ro") ||
		!hasMountOption(afterOptions, "ro") || hasMountOption(afterOptions, "rw") {
		return errors.New("workspace overlay read-only transition is invalid")
	}
	if !reflect.DeepEqual(
		mountOptionsWithoutAccessMode(beforeOptions),
		mountOptionsWithoutAccessMode(afterOptions),
	) {
		return errors.New("workspace overlay options changed while freezing")
	}
	for _, required := range []string{"nosuid", "nodev", "noatime"} {
		if !hasMountOption(afterOptions, required) {
			return fmt.Errorf("frozen workspace overlay lost %s", required)
		}
	}
	if hasMountOption(afterOptions, "noexec") {
		return errors.New("frozen workspace overlay unexpectedly became non-executable")
	}
	return nil
}

func mountOptionsWithoutAccessMode(options []string) []string {
	result := make([]string, 0, len(options))
	for _, option := range options {
		if option != "rw" && option != "ro" {
			result = append(result, option)
		}
	}
	return result
}

func (arm *ArmAuthority) refreshFrozenOverlayMount() error {
	if !arm.frozen || arm.freezeVerified {
		return nil
	}
	current, err := mountRecordForDescriptor(arm.overlayFile)
	if err != nil {
		return fmt.Errorf("read frozen workspace mount during cleanup: %w", err)
	}
	if err := validateFrozenOverlayIdentity(arm.overlayMount, current); err != nil {
		return err
	}
	if hasMountOption(arm.overlayMount.options, "rw") {
		if err := validateFrozenOverlayOptions(arm.overlayMount.options, current.options); err != nil {
			return err
		}
	} else if !reflect.DeepEqual(arm.overlayMount.options, current.options) {
		return errors.New("frozen workspace overlay options changed during cleanup")
	}
	arm.overlayMount = current
	return nil
}

func cloneOutcome(source Outcome) Outcome {
	clone := source
	if source.Patch != nil {
		clone.Patch = append([]byte{}, source.Patch...)
	}
	return clone
}
