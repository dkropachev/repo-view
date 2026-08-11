//go:build linux

package workspace

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/scopesifter/scopesifter/benchmarks/tokenbench/snapshot"
	"golang.org/x/sys/unix"
)

const requirePrivilegedWorkspaceTestsEnvironment = "TOKENBENCH_REQUIRE_PRIVILEGED_TESTS"

type privilegedWorkspaceSnapshot struct {
	inputs snapshot.ExecutionInputs
}

func (authority *privilegedWorkspaceSnapshot) Reverify(ctx context.Context) error {
	return ctx.Err()
}

func (authority *privilegedWorkspaceSnapshot) Inputs() (snapshot.ExecutionInputs, error) {
	return authority.inputs, nil
}

func TestAuthoritiesFailClosedWithoutLiveState(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	var pair *PairAuthority
	if _, err := pair.Inputs(); err == nil {
		t.Fatal("nil pair published inputs")
	}
	if err := pair.Reverify(ctx); err == nil {
		t.Fatal("nil pair reverified")
	}
	if _, err := pair.BeginArm(ctx); err == nil {
		t.Fatal("nil pair created an arm")
	}
	if pair.Closed() {
		t.Fatal("nil pair reported successful cleanup")
	}
	var arm *ArmAuthority
	if _, err := arm.Paths(); err == nil {
		t.Fatal("nil arm published paths")
	}
	if err := arm.RequireFresh(ctx); err == nil {
		t.Fatal("nil arm reported freshness")
	}
	if err := arm.Reverify(ctx); err == nil {
		t.Fatal("nil arm reverified")
	}
	if _, err := arm.Capture(ctx); err == nil {
		t.Fatal("nil arm captured a workspace")
	}
	if arm.Closed() {
		t.Fatal("nil arm reported successful cleanup")
	}
}

func TestPrepareRejectsMissingAuthorityBeforeFilesystemMutation(t *testing.T) {
	t.Parallel()
	root := filepath.Join(t.TempDir(), "workspace")
	if _, err := Prepare(context.Background(), nil, PrepareRequest{
		Root: root, Limits: validLimits(),
	}); err == nil {
		t.Fatal("missing snapshot authority was accepted")
	}
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		t.Fatalf("failed preparation mutated the workspace path: %v", err)
	}
}

func TestArmCleanupReleasesPartialConstructionBeforeTargetExists(t *testing.T) {
	t.Parallel()
	rootPath := t.TempDir()
	if err := os.Chmod(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	mountedRoot, err := os.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mountedRoot.Close() })
	lower, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	pair := &PairAuthority{
		mountedRoot: mountedRoot,
		rootPath:    rootPath,
		inputs:      Inputs{Limits: validLimits()},
	}
	arm := &ArmAuthority{pair: pair, lower: lower}
	pair.active = arm
	if err := arm.closeLocked(); err != nil {
		t.Fatalf("cleanup partial arm: %v", err)
	}
	if !arm.released || arm.lower != nil || pair.active != nil {
		t.Fatalf("partial arm retained authority: %#v", arm)
	}
	if err := directoryIsEmpty(mountedRoot); err != nil {
		t.Fatalf("partial arm left workspace state: %v", err)
	}
}

func TestArmCleanupOwnsDirectoryAfterPostMkdirFailure(t *testing.T) {
	t.Parallel()
	rootPath := t.TempDir()
	if err := os.Chmod(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	mountedRoot, err := os.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = mountedRoot.Close() })
	if err := unix.Mkdirat(int(mountedRoot.Fd()), worktreeDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	var created unix.Stat_t
	if err := unix.Fstatat(
		int(mountedRoot.Fd()),
		worktreeDirectory,
		&created,
		unix.AT_SYMLINK_NOFOLLOW,
	); err != nil {
		t.Fatal(err)
	}
	pair := &PairAuthority{
		mountedRoot: mountedRoot,
		rootPath:    rootPath,
		inputs:      Inputs{Limits: validLimits()},
	}
	arm := &ArmAuthority{
		pair: pair,
		layoutClaims: []directoryClaim{{
			name: worktreeDirectory, device: created.Dev, inode: created.Ino,
		}},
	}
	pair.active = arm
	if err := arm.closeLocked(); err != nil {
		t.Fatalf("cleanup post-mkdir construction failure: %v", err)
	}
	if !arm.released || pair.active != nil {
		t.Fatal("post-mkdir construction authority was retained")
	}
	if err := directoryIsEmpty(mountedRoot); err != nil {
		t.Fatalf("post-mkdir construction left workspace state: %v", err)
	}
}

func TestCreateDirectoryAtNormalizesRestrictiveUmask(t *testing.T) {
	rootPath := t.TempDir()
	if err := os.Chmod(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })

	oldMask := unix.Umask(0o777)
	directory, claim, createErr := createDirectoryAt(root, worktreeDirectory)
	unix.Umask(oldMask)
	if createErr != nil {
		t.Fatalf("create directory with restrictive umask: %v", createErr)
	}
	if !claim.valid() {
		t.Fatal("created directory omitted its cleanup claim")
	}
	info, err := directory.Stat()
	if err != nil || info.Mode().Perm() != 0o700 || !sameFileClaim(info, claim) {
		t.Fatalf("normalized directory = %#v, %v", info, err)
	}
	if err := directory.Close(); err != nil {
		t.Fatal(err)
	}
	if err := removeArmLayout(root, []directoryClaim{claim}, validLimits()); err != nil {
		t.Fatalf("cleanup normalized directory: %v", err)
	}
}

func TestArmCleanupRetriesExactClaimAfterReplacementIsRestored(t *testing.T) {
	t.Parallel()
	rootPath := t.TempDir()
	if err := os.Chmod(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	root, err := os.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })
	directory, claim, err := createDirectoryAt(root, worktreeDirectory)
	if err != nil {
		t.Fatal(err)
	}
	pair := &PairAuthority{mountedRoot: root, inputs: Inputs{Limits: validLimits()}}
	arm := &ArmAuthority{
		pair: pair, target: directory, layoutClaims: []directoryClaim{claim},
	}
	pair.active = arm
	originalPath := filepath.Join(rootPath, worktreeDirectory)
	retainedPath := filepath.Join(rootPath, "retained-worktree")
	if err := os.Rename(originalPath, retainedPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(originalPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := arm.closeLocked(); err == nil {
		t.Fatal("cleanup removed a replacement directory")
	}
	if arm.released || pair.active != arm {
		t.Fatal("failed cleanup released its retry authority")
	}
	if current, err := os.Stat(originalPath); err != nil || !current.IsDir() {
		t.Fatalf("replacement directory was mutated: %v", err)
	}
	if err := os.Remove(originalPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(retainedPath, originalPath); err != nil {
		t.Fatal(err)
	}
	if err := arm.closeLocked(); err != nil {
		t.Fatalf("retry cleanup after restoring exact claim: %v", err)
	}
	if !arm.released || pair.active != nil {
		t.Fatal("successful retry retained arm authority")
	}
	if err := directoryIsEmpty(root); err != nil {
		t.Fatalf("successful retry left workspace state: %v", err)
	}
}

func TestCodeSnapshotStateRequiresOneCleanBaseRevision(t *testing.T) {
	t.Parallel()
	inputs := snapshotStateFixture()
	if err := validateCodeSnapshotState(inputs); err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*snapshot.ExecutionInputs){
		"different base": func(value *snapshot.ExecutionInputs) {
			value.SourceBaseRevision = strings.Repeat("b", 40)
		},
		"different cache base": func(value *snapshot.ExecutionInputs) {
			value.ChangedStateCache.BaseCommit = strings.Repeat("b", 40)
		},
		"changed file": func(value *snapshot.ExecutionInputs) {
			value.ChangedStateCache.ChangedFiles = []snapshot.ChangedFileState{{Path: "file"}}
		},
		"patch": func(value *snapshot.ExecutionInputs) {
			value.ChangedStateCache.Patch = "diff --git a/a b/a\n"
		},
		"changed identity": func(value *snapshot.ExecutionInputs) {
			value.ChangedState.ChangedFileCount = 1
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			mutated := inputs.Clone()
			mutate(&mutated)
			if err := validateCodeSnapshotState(mutated); err == nil {
				t.Fatal("nonclean code snapshot state was accepted")
			}
		})
	}
}

func snapshotStateFixture() snapshot.ExecutionInputs {
	revision := strings.Repeat("a", 40)
	return snapshot.ExecutionInputs{
		SourceRevision:     revision,
		SourceBaseRevision: revision,
		ChangedStateCache: snapshot.ChangedStateCache{
			BaseCommit: revision,
			HeadCommit: revision,
		},
	}
}

func TestPrivilegedWorkspaceMountLifecycle(t *testing.T) {
	if os.Getenv(requirePrivilegedWorkspaceTestsEnvironment) != "1" {
		t.Skip("privileged workspace mount lifecycle is exercised by the required kernel lane")
	}
	if os.Geteuid() != 0 {
		t.Fatal("required privileged workspace test is not running as root")
	}

	parentPath := t.TempDir()
	if err := os.Chmod(parentPath, 0o700); err != nil {
		t.Fatal(err)
	}
	rootPath := filepath.Join(parentPath, "workspace")
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	parentMount, err := containingPrivateMount(rootPath)
	if err != nil {
		t.Fatalf("private workspace parent mount: %v", err)
	}
	underlyingRoot, underlyingInfo, err := retainWorkspaceRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	namespace, err := currentMountNamespaceIdentity()
	if err != nil {
		t.Fatal(err)
	}
	limits := Limits{
		MaximumUpperBytes:   64 << 20,
		MaximumEntries:      4_096,
		MaximumFileBytes:    8 << 20,
		MaximumPatchBytes:   4 << 20,
		MaximumChangedFiles: 1_024,
	}
	pair := &PairAuthority{
		underlyingRoot: underlyingRoot, underlyingInfo: underlyingInfo,
		rootPath: rootPath, namespace: namespace, parentMount: parentMount,
		paths: ArmPaths{
			ModelRoot: filepath.Join(rootPath, worktreeDirectory),
			CacheRoot: filepath.Join(rootPath, cacheDirectory),
		},
		inputs: Inputs{Limits: limits},
	}
	t.Cleanup(func() {
		if !pair.Closed() {
			if err := pair.Close(); err != nil {
				t.Errorf("cleanup workspace pair: %v", err)
			}
		}
	})
	if err := pair.attachBoundedTmpfs(limits); err != nil {
		t.Fatalf("attach bounded tmpfs: %v", err)
	}
	if err := rejectDescendantMounts(rootPath); err != nil {
		t.Fatalf("fresh tmpfs mount topology: %v", err)
	}

	lowerPath := t.TempDir()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	gitPath, err = filepath.EvalSymlinks(gitPath)
	if err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, "", gitPath, "init", "--quiet", "--object-format=sha1", lowerPath)
	baseline := []byte("baseline\n")
	if err := os.WriteFile(filepath.Join(lowerPath, "README"), baseline, 0o644); err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, lowerPath, gitPath, "add", "--", "README")
	runFixtureGit(
		t,
		lowerPath,
		gitPath,
		"-c", "user.name=Tokenbench",
		"-c", "user.email=tokenbench@example.invalid",
		"commit", "--quiet", "-m", "base",
	)
	baseRevision := strings.TrimSpace(runFixtureGit(t, lowerPath, gitPath, "rev-parse", "HEAD"))
	fileInfo, err := os.Stat(filepath.Join(lowerPath, "README"))
	if err != nil {
		t.Fatal(err)
	}
	baseManifest := []worktreeEntry{
		{path: ".", kind: snapshot.ManifestKindDirectory, mode: 0o700},
		{
			path: "README", kind: snapshot.ManifestKindFile, digest: digest(baseline),
			mode: uint32(fileInfo.Mode().Perm()), size: int64(len(baseline)),
		},
	}
	gitExecutable, err := os.Open(gitPath)
	if err != nil {
		t.Fatal(err)
	}
	pair.verifierGit = gitExecutable
	pair.verifierInfo, err = gitExecutable.Stat()
	if err != nil {
		t.Fatal(err)
	}
	pair.gitObjects, err = os.Open(filepath.Join(lowerPath, ".git", "objects"))
	if err != nil {
		t.Fatal(err)
	}
	pair.objectsInfo, err = pair.gitObjects.Stat()
	if err != nil {
		t.Fatal(err)
	}
	pair.baseRevision = baseRevision
	pair.baseManifest = baseManifest
	snapshotCommitment := digest([]byte("privileged snapshot authority"))
	baseTreeSHA256 := digest([]byte("privileged source tree"))
	changedStateSHA256 := digest(nil)
	pair.snapshot = &privilegedWorkspaceSnapshot{
		inputs: snapshot.ExecutionInputs{
			Commitment: snapshotCommitment, SourceRoot: lowerPath,
			SourceTreeSHA256: baseTreeSHA256,
			ChangedState:     snapshot.ChangedStateIdentity{SHA256: changedStateSHA256},
		},
	}
	pair.inputs = Inputs{
		SchemaVersion: InputsSchemaVersion, ModelRoot: pair.paths.ModelRoot,
		ImmutableLowerRoot: lowerPath, BaseTreeSHA256: baseTreeSHA256,
		SnapshotCommitment: snapshotCommitment, ChangedStateSHA256: changedStateSHA256,
		MountPolicySHA256: requiredMountPolicySHA256, Limits: limits,
	}
	pair.inputs.Commitment = pair.inputs.ComputeCommitment()
	if err := pair.inputs.Validate(); err != nil {
		t.Fatal(err)
	}

	for iteration := range 2 {
		arm := privilegedTestArm(t, pair, lowerPath)
		actual, err := scanWorktree(context.Background(), arm.overlayRoot, limits)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(actual, baseManifest) {
			t.Fatalf("arm %d initial tree = %#v, want %#v", iteration, actual, baseManifest)
		}
		if err := verifyInitialWorktree(context.Background(), arm.overlayRoot, baseManifest, limits); err != nil {
			t.Fatalf("arm %d initial tree: %v", iteration, err)
		}
		content, err := os.ReadFile(filepath.Join(arm.paths.ModelRoot, "README"))
		if err != nil || string(content) != string(baseline) {
			t.Fatalf("arm %d baseline = %q, %v", iteration, content, err)
		}
		if _, err := os.Lstat(filepath.Join(arm.paths.ModelRoot, ".git")); !os.IsNotExist(err) {
			t.Fatalf("arm %d exposed immutable Git metadata: %v", iteration, err)
		}
		if _, err := os.Lstat(filepath.Join(arm.paths.ModelRoot, "mutation")); !os.IsNotExist(err) {
			t.Fatalf("arm %d inherited an earlier arm mutation: %v", iteration, err)
		}
		if err := directoryIsEmpty(arm.cache); err != nil {
			t.Fatalf("arm %d cache is not fresh: %v", iteration, err)
		}
		if iteration == 0 {
			assertPrivilegedCacheReplacementRejected(t, arm)
			assertPrivilegedPrivateMountRejected(t, arm, lowerPath)
			assertPrivilegedDescendantMountRejected(t, arm, lowerPath)
			if err := os.WriteFile(
				filepath.Join(arm.paths.ModelRoot, "mutation"),
				[]byte("captured\n"),
				0o644,
			); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(arm.paths.CacheRoot, "cache"), []byte("private"), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if _, err := scanCapturedWorktree(
			context.Background(),
			arm.overlayRoot,
			baseManifest,
			limits,
		); err != nil {
			t.Fatalf("scan arm %d before capture: %v", iteration, err)
		}
		outcome, err := arm.Capture(context.Background())
		if err != nil {
			t.Fatalf("capture arm %d: %v", iteration, err)
		}
		wantStatus := StatusNoChange
		if iteration == 0 {
			wantStatus = StatusCaptured
		}
		if outcome.Status != wantStatus {
			t.Fatalf("arm %d capture = %#v, want status %q", iteration, outcome, wantStatus)
		}
		if err := outcome.Validate(limits); err != nil {
			t.Fatalf("arm %d capture outcome: %v", iteration, err)
		}
		cached, err := arm.Capture(context.Background())
		if err != nil || !reflect.DeepEqual(cached, outcome) {
			t.Fatalf("arm %d cached capture = %#v, %v", iteration, cached, err)
		}
		if _, err := arm.Paths(); err == nil {
			t.Fatal("frozen workspace still published launch paths")
		}
		if err := arm.RequireFresh(context.Background()); err == nil {
			t.Fatal("frozen workspace was launchable")
		}
		if err := os.WriteFile(
			filepath.Join(arm.paths.ModelRoot, "write-after-freeze"),
			[]byte("forbidden"),
			0o600,
		); err == nil {
			t.Fatal("read-only workspace freeze permitted a new file")
		}
		if err := arm.Reverify(context.Background()); err != nil {
			t.Fatalf("reverify captured arm %d: %v", iteration, err)
		}
		if err := arm.Close(); err != nil {
			t.Fatalf("close arm %d: %v", iteration, err)
		}
		if !arm.Closed() {
			t.Fatalf("arm %d did not report strong cleanup", iteration)
		}
		if err := directoryIsEmpty(pair.mountedRoot); err != nil {
			t.Fatalf("arm %d left tmpfs state: %v", iteration, err)
		}
	}
	if err := pair.Close(); err != nil {
		t.Fatalf("close workspace pair: %v", err)
	}
	if !pair.Closed() {
		t.Fatal("workspace pair did not report strong cleanup")
	}
	rootInfo, err := os.Stat(rootPath)
	if err != nil || !rootInfo.IsDir() {
		t.Fatalf("borrowed workspace mountpoint was removed: %v", err)
	}
	rootDirectory, err := os.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := directoryIsEmpty(rootDirectory); err != nil {
		_ = rootDirectory.Close()
		t.Fatalf("borrowed workspace mountpoint was not returned empty: %v", err)
	}
	if err := rootDirectory.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPrivilegedWorkspaceMinimumEntryLimitReservesPrivateLayout(t *testing.T) {
	if os.Getenv(requirePrivilegedWorkspaceTestsEnvironment) != "1" {
		t.Skip("privileged workspace inode reserve is exercised by the required kernel lane")
	}
	if os.Geteuid() != 0 {
		t.Fatal("required privileged workspace test is not running as root")
	}

	rootPath := filepath.Join(t.TempDir(), "workspace")
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	parentMount, err := containingPrivateMount(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	underlyingRoot, underlyingInfo, err := retainWorkspaceRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	namespace, err := currentMountNamespaceIdentity()
	if err != nil {
		t.Fatal(err)
	}
	limits := Limits{
		MaximumUpperBytes:   64 << 20,
		MaximumEntries:      1,
		MaximumFileBytes:    1,
		MaximumPatchBytes:   1,
		MaximumChangedFiles: 1,
	}
	pair := &PairAuthority{
		underlyingRoot: underlyingRoot,
		underlyingInfo: underlyingInfo,
		rootPath:       rootPath,
		namespace:      namespace,
		parentMount:    parentMount,
		inputs:         Inputs{Limits: limits},
	}
	if err := pair.attachBoundedTmpfs(limits); err != nil {
		t.Fatalf("attach minimum-entry tmpfs: %v", err)
	}
	t.Cleanup(func() {
		if !pair.Closed() {
			_ = pair.Close()
		}
	})
	for _, name := range []string{
		worktreeDirectory,
		upperDirectory,
		workDirectory,
		cacheDirectory,
		captureDirectory,
	} {
		directory, _, err := createDirectoryAt(pair.mountedRoot, name)
		if err != nil {
			t.Fatalf("create reserved private directory %s: %v", name, err)
		}
		if name == upperDirectory {
			if err := createGitWhiteout(directory); err != nil {
				_ = directory.Close()
				t.Fatalf("create reserved Git whiteout: %v", err)
			}
		}
		if err := directory.Close(); err != nil {
			t.Fatal(err)
		}
	}
	if err := pair.Close(); err != nil {
		t.Fatalf("close minimum-entry pair: %v", err)
	}
	if !pair.Closed() {
		t.Fatal("minimum-entry pair did not release all authority")
	}
	if current, err := os.Stat(rootPath); err != nil || !current.IsDir() {
		t.Fatalf("borrowed minimum-entry mountpoint was removed: %v", err)
	}
}

func TestPrivilegedWorkspaceCleanupFollowsRelocatedActiveMounts(t *testing.T) {
	requirePrivilegedWorkspaceTest(t)

	basePath := t.TempDir()
	parentPath := filepath.Join(basePath, "borrowed-parent")
	movedParentPath := filepath.Join(basePath, "moved-parent")
	rootPath := filepath.Join(parentPath, "workspace")
	if err := os.MkdirAll(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	limits := Limits{
		MaximumUpperBytes:   64 << 20,
		MaximumEntries:      32,
		MaximumFileBytes:    8 << 20,
		MaximumPatchBytes:   4 << 20,
		MaximumChangedFiles: 16,
	}
	pair := privilegedMountedPair(t, rootPath, limits)
	lowerPath := t.TempDir()
	if err := os.Mkdir(filepath.Join(lowerPath, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	arm := privilegedTestArm(t, pair, lowerPath)
	rootMountID := pair.rootMount.id
	overlayMountID := arm.overlayMount.id

	if err := os.Rename(parentPath, movedParentPath); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	replacementMarker := filepath.Join(rootPath, "replacement")
	if err := os.WriteFile(replacementMarker, []byte("caller-owned"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := pair.Close(); err != nil {
		t.Fatalf("cleanup relocated active mounts: %v", err)
	}
	if !pair.Closed() || !arm.Closed() {
		t.Fatal("relocated workspace authority did not report strong cleanup")
	}
	for _, id := range []uint64{overlayMountID, rootMountID} {
		if _, err := mountRecordByID(id); err == nil {
			t.Fatalf("retained mount ID %d survived cleanup", id)
		}
	}
	movedRoot, err := os.Open(filepath.Join(movedParentPath, "workspace"))
	if err != nil {
		t.Fatal(err)
	}
	if err := directoryIsEmpty(movedRoot); err != nil {
		_ = movedRoot.Close()
		t.Fatalf("relocated borrowed mountpoint was not returned empty: %v", err)
	}
	if err := movedRoot.Close(); err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(replacementMarker)
	if err != nil || string(content) != "caller-owned" {
		t.Fatalf("replacement caller path was mutated: %q, %v", content, err)
	}
}

func TestPrivilegedWorkspaceCleanupFollowsRootRelocatedDuringAttach(t *testing.T) {
	requirePrivilegedWorkspaceTest(t)

	parentPath := t.TempDir()
	rootPath := filepath.Join(parentPath, "workspace")
	movedRootPath := filepath.Join(parentPath, "moved-workspace")
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	limits := Limits{
		MaximumUpperBytes:   64 << 20,
		MaximumEntries:      8,
		MaximumFileBytes:    8 << 20,
		MaximumPatchBytes:   4 << 20,
		MaximumChangedFiles: 8,
	}
	parentMount, err := containingPrivateMount(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	underlyingRoot, underlyingInfo, err := retainWorkspaceRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	namespace, err := currentMountNamespaceIdentity()
	if err != nil {
		t.Fatal(err)
	}
	pair := &PairAuthority{
		underlyingRoot: underlyingRoot,
		underlyingInfo: underlyingInfo,
		rootPath:       rootPath,
		namespace:      namespace,
		parentMount:    parentMount,
		inputs:         Inputs{Limits: limits},
	}
	if err := os.Rename(rootPath, movedRootPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := pair.attachBoundedTmpfs(limits); err == nil {
		t.Fatal("attach accepted a relocated borrowed-root pathname")
	}
	rootMountID := pair.rootMount.id
	if rootMountID == 0 {
		t.Fatal("failed attachment omitted retained mount identity")
	}
	if err := pair.Close(); err != nil {
		t.Fatalf("cleanup root relocated during attach: %v", err)
	}
	if !pair.Closed() {
		t.Fatal("failed attachment did not release workspace authority")
	}
	if _, err := mountRecordByID(rootMountID); err == nil {
		t.Fatalf("retained tmpfs mount ID %d survived failed attachment", rootMountID)
	}
	if current, err := os.Stat(rootPath); err != nil || !current.IsDir() {
		t.Fatalf("replacement root was mutated: %v", err)
	}
	movedRoot, err := os.Open(movedRootPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := directoryIsEmpty(movedRoot); err != nil {
		_ = movedRoot.Close()
		t.Fatalf("relocated root was not returned empty: %v", err)
	}
	if err := movedRoot.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPrivilegedWorkspaceRestrictiveUmaskConstructionAndCleanup(t *testing.T) {
	requirePrivilegedWorkspaceTest(t)

	rootPath := filepath.Join(t.TempDir(), "workspace")
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	limits := Limits{
		MaximumUpperBytes:   64 << 20,
		MaximumEntries:      32,
		MaximumFileBytes:    8 << 20,
		MaximumPatchBytes:   4 << 20,
		MaximumChangedFiles: 16,
	}
	pair := privilegedMountedPair(t, rootPath, limits)
	lowerPath := t.TempDir()
	if err := os.Mkdir(filepath.Join(lowerPath, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	var arm *ArmAuthority
	func() {
		oldMask := unix.Umask(0o777)
		defer unix.Umask(oldMask)
		arm = privilegedTestArm(t, pair, lowerPath)
	}()
	for _, directory := range []*os.File{arm.target, arm.upper, arm.work, arm.cache, arm.capture} {
		info, err := directory.Stat()
		if err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("restrictive-umask directory = %#v, %v", info, err)
		}
	}
	if err := pair.Close(); err != nil {
		t.Fatalf("cleanup restrictive-umask workspace: %v", err)
	}
	if !pair.Closed() || !arm.Closed() {
		t.Fatal("restrictive-umask workspace retained authority")
	}
}

func TestPrivilegedWorkspaceUnidentifiedPostMkdirClosesPair(t *testing.T) {
	requirePrivilegedWorkspaceTest(t)

	rootPath := filepath.Join(t.TempDir(), "workspace")
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	limits := Limits{
		MaximumUpperBytes:   64 << 20,
		MaximumEntries:      8,
		MaximumFileBytes:    8 << 20,
		MaximumPatchBytes:   4 << 20,
		MaximumChangedFiles: 8,
	}
	pair := privilegedMountedPair(t, rootPath, limits)
	if err := unix.Mkdirat(int(pair.mountedRoot.Fd()), worktreeDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	arm := &ArmAuthority{
		pair:         pair,
		layoutClaims: []directoryClaim{{name: worktreeDirectory}},
	}
	pair.active = arm
	pair.tainted = true
	pair.closing = true
	rootMountID := pair.rootMount.id
	if err := pair.Close(); err != nil {
		t.Fatalf("close pair after unidentified post-mkdir state: %v", err)
	}
	if !pair.Closed() || !arm.Closed() {
		t.Fatal("unidentified post-mkdir state retained authority")
	}
	if _, err := mountRecordByID(rootMountID); err == nil {
		t.Fatalf("tmpfs mount ID %d survived tainted-pair cleanup", rootMountID)
	}
	root, err := os.Open(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := directoryIsEmpty(root); err != nil {
		_ = root.Close()
		t.Fatalf("borrowed root retained unidentified residue: %v", err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestPrivilegedWorkspaceMaximumEntriesIncludesCacheRoot(t *testing.T) {
	requirePrivilegedWorkspaceTest(t)

	rootPath := filepath.Join(t.TempDir(), "workspace")
	if err := os.Mkdir(rootPath, 0o700); err != nil {
		t.Fatal(err)
	}
	limits := Limits{
		MaximumUpperBytes:   64 << 20,
		MaximumEntries:      1,
		MaximumFileBytes:    1,
		MaximumPatchBytes:   1,
		MaximumChangedFiles: 1,
	}
	pair := privilegedMountedPair(t, rootPath, limits)
	lowerPath := t.TempDir()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	gitPath, err = filepath.EvalSymlinks(gitPath)
	if err != nil {
		t.Fatal(err)
	}
	runFixtureGit(t, "", gitPath, "init", "--quiet", "--object-format=sha1", lowerPath)
	runFixtureGit(
		t,
		lowerPath,
		gitPath,
		"-c", "user.name=Tokenbench",
		"-c", "user.email=tokenbench@example.invalid",
		"commit", "--quiet", "--allow-empty", "-m", "base",
	)
	pair.baseRevision = strings.TrimSpace(runFixtureGit(t, lowerPath, gitPath, "rev-parse", "HEAD"))
	pair.baseManifest = []worktreeEntry{{
		path: ".", kind: snapshot.ManifestKindDirectory, mode: 0o700,
	}}
	pair.verifierGit, err = os.Open(gitPath)
	if err != nil {
		t.Fatal(err)
	}
	pair.verifierInfo, err = pair.verifierGit.Stat()
	if err != nil {
		t.Fatal(err)
	}
	pair.gitObjects, err = os.Open(filepath.Join(lowerPath, ".git", "objects"))
	if err != nil {
		t.Fatal(err)
	}
	pair.objectsInfo, err = pair.gitObjects.Stat()
	if err != nil {
		t.Fatal(err)
	}
	snapshotCommitment := digest([]byte("minimum-entry snapshot authority"))
	baseTreeSHA256 := digest([]byte("minimum-entry source tree"))
	changedStateSHA256 := digest(nil)
	pair.snapshot = &privilegedWorkspaceSnapshot{inputs: snapshot.ExecutionInputs{
		Commitment: snapshotCommitment, SourceRoot: lowerPath,
		SourceTreeSHA256: baseTreeSHA256,
		ChangedState:     snapshot.ChangedStateIdentity{SHA256: changedStateSHA256},
	}}
	pair.inputs = Inputs{
		SchemaVersion: InputsSchemaVersion, ModelRoot: pair.paths.ModelRoot,
		ImmutableLowerRoot: lowerPath, BaseTreeSHA256: baseTreeSHA256,
		SnapshotCommitment: snapshotCommitment, ChangedStateSHA256: changedStateSHA256,
		MountPolicySHA256: requiredMountPolicySHA256, Limits: limits,
	}
	pair.inputs.Commitment = pair.inputs.ComputeCommitment()
	if err := pair.inputs.Validate(); err != nil {
		t.Fatal(err)
	}
	arm := privilegedTestArm(t, pair, lowerPath)
	var stats unix.Statfs_t
	if err := unix.Fstatfs(int(pair.mountedRoot.Fd()), &stats); err != nil {
		t.Fatal(err)
	}
	if stats.Ffree != uint64(limits.MaximumEntries) {
		t.Fatalf("model-consumable inodes = %d, want %d", stats.Ffree, limits.MaximumEntries)
	}
	if err := os.WriteFile(filepath.Join(arm.paths.CacheRoot, "one"), nil, 0o600); err != nil {
		t.Fatalf("consume committed cache inode: %v", err)
	}
	if err := os.WriteFile(filepath.Join(arm.paths.CacheRoot, "two"), nil, 0o600); !errors.Is(err, unix.ENOSPC) {
		t.Fatalf("cache exceeded maximum_entries: %v", err)
	}
	if err := os.WriteFile(filepath.Join(arm.paths.ModelRoot, "model"), nil, 0o600); !errors.Is(err, unix.ENOSPC) {
		t.Fatalf("worktree escaped cache-consumed maximum_entries: %v", err)
	}
	reserved := len(arm.inodeReserve)
	if reserved == 0 {
		t.Fatal("minimum-limit arm retained no private capture inode reserve")
	}
	outcome, err := arm.Capture(context.Background())
	if err != nil {
		t.Fatalf("capture minimum-limit no-change tree: %v", err)
	}
	if outcome.Status != StatusNoChange {
		t.Fatalf("minimum-limit capture = %#v, want status %q", outcome, StatusNoChange)
	}
	if err := outcome.Validate(limits); err != nil {
		t.Fatalf("minimum-limit capture outcome: %v", err)
	}
	if len(arm.inodeReserve) != 0 {
		t.Fatal("released capture inode reserve retained descriptors")
	}
	if err := pair.Close(); err != nil {
		t.Fatalf("cleanup minimum-limit overlay and cache: %v", err)
	}
	if !pair.Closed() || !arm.Closed() {
		t.Fatal("minimum-limit overlay and cache retained authority")
	}
}

func requirePrivilegedWorkspaceTest(t *testing.T) {
	t.Helper()
	if os.Getenv(requirePrivilegedWorkspaceTestsEnvironment) != "1" {
		t.Skip("privileged workspace behavior is exercised by the required kernel lane")
	}
	if os.Geteuid() != 0 {
		t.Fatal("required privileged workspace test is not running as root")
	}
}

func privilegedMountedPair(t *testing.T, rootPath string, limits Limits) *PairAuthority {
	t.Helper()
	parentMount, err := containingPrivateMount(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	underlyingRoot, underlyingInfo, err := retainWorkspaceRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	namespace, err := currentMountNamespaceIdentity()
	if err != nil {
		t.Fatal(err)
	}
	pair := &PairAuthority{
		underlyingRoot: underlyingRoot,
		underlyingInfo: underlyingInfo,
		rootPath:       rootPath,
		namespace:      namespace,
		parentMount:    parentMount,
		paths: ArmPaths{
			ModelRoot: filepath.Join(rootPath, worktreeDirectory),
			CacheRoot: filepath.Join(rootPath, cacheDirectory),
		},
		inputs: Inputs{Limits: limits},
	}
	if err := pair.attachBoundedTmpfs(limits); err != nil {
		_ = pair.Close()
		t.Fatalf("attach bounded test tmpfs: %v", err)
	}
	t.Cleanup(func() {
		if !pair.Closed() {
			_ = pair.Close()
		}
	})
	return pair
}

func assertPrivilegedPrivateMountRejected(
	t *testing.T,
	arm *ArmAuthority,
	source string,
) {
	t.Helper()
	target := arm.paths.CacheRoot
	mounted := false
	defer func() {
		if mounted {
			_ = unix.Unmount(target, unix.UMOUNT_NOFOLLOW)
		}
	}()
	if err := unix.Mount(source, target, "", unix.MS_BIND, ""); err != nil {
		t.Fatalf("create private-layout mount probe: %v", err)
	}
	mounted = true
	if err := arm.reverifyLocked(context.Background(), false); err == nil {
		t.Fatal("private-layout descendant mount was accepted")
	}
	if err := unix.Unmount(target, unix.UMOUNT_NOFOLLOW); err != nil {
		t.Fatalf("remove private-layout mount probe: %v", err)
	}
	mounted = false
	if err := arm.reverifyLocked(context.Background(), false); err != nil {
		t.Fatalf("reverify after private-layout mount removal: %v", err)
	}
}

func assertPrivilegedCacheReplacementRejected(t *testing.T, arm *ArmAuthority) {
	t.Helper()
	cachePath := arm.paths.CacheRoot
	retainedPath := cachePath + "-retained"
	if err := os.Rename(cachePath, retainedPath); err != nil {
		t.Fatal(err)
	}
	restored := false
	defer func() {
		if !restored {
			_ = os.RemoveAll(cachePath)
			_ = os.Rename(retainedPath, cachePath)
		}
	}()
	if err := os.Mkdir(cachePath, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cachePath, "stale"), []byte("stale"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := arm.reverifyLocked(context.Background(), false); err == nil ||
		!strings.Contains(err.Error(), "cache") {
		t.Fatalf("replacement cache path reverify = %v", err)
	}
	if err := os.RemoveAll(cachePath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(retainedPath, cachePath); err != nil {
		t.Fatal(err)
	}
	restored = true
	if err := arm.reverifyLocked(context.Background(), false); err != nil {
		t.Fatalf("reverify restored cache path: %v", err)
	}
}

func assertPrivilegedDescendantMountRejected(
	t *testing.T,
	arm *ArmAuthority,
	source string,
) {
	t.Helper()
	target := filepath.Join(arm.paths.ModelRoot, "nested-mount")
	if err := os.Mkdir(target, 0o700); err != nil {
		t.Fatal(err)
	}
	mounted := false
	defer func() {
		if mounted {
			_ = unix.Unmount(target, unix.UMOUNT_NOFOLLOW)
		}
	}()
	if err := unix.Mount(source, target, "", unix.MS_BIND, ""); err != nil {
		t.Fatalf("create descendant mount probe: %v", err)
	}
	mounted = true
	if err := arm.reverifyLocked(context.Background(), false); err == nil ||
		!strings.Contains(err.Error(), "descendant mount") {
		t.Fatalf("descendant mount reverify = %v", err)
	}
	if err := unix.Unmount(target, unix.UMOUNT_NOFOLLOW); err != nil {
		t.Fatalf("remove descendant mount probe: %v", err)
	}
	mounted = false
	if err := os.Remove(target); err != nil {
		t.Fatal(err)
	}
	if err := arm.reverifyLocked(context.Background(), false); err != nil {
		t.Fatalf("reverify after descendant mount removal: %v", err)
	}
}

func privilegedTestArm(t *testing.T, pair *PairAuthority, lowerPath string) *ArmAuthority {
	t.Helper()
	arm := &ArmAuthority{pair: pair, paths: pair.paths}
	pair.active = arm
	var err error
	if pair.sourceRoot == nil {
		pair.sourceRoot, err = openDirectoryNoSymlinks(lowerPath)
		if err == nil {
			pair.sourceInfo, err = pair.sourceRoot.Stat()
		}
	}
	if err == nil {
		arm.lower, err = duplicateRetainedFile(
			pair.sourceRoot,
			pair.sourceInfo,
			"privileged test source-root descriptor",
		)
	}
	if err == nil {
		arm.target, err = arm.createDirectoryLocked(worktreeDirectory)
	}
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
	if err == nil {
		err = createGitWhiteout(arm.upper)
	}
	if err == nil {
		err = arm.attachOverlay()
	}
	if err == nil {
		err = arm.retainInodeBudget()
	}
	if err != nil {
		cleanupErr := arm.closeLocked()
		t.Fatalf("create privileged test arm: %v", errorsJoinText(err, cleanupErr))
	}
	if err := arm.reverifyLocked(context.Background(), false); err != nil {
		t.Fatalf("reverify privileged test arm: %v", err)
	}
	return arm
}

func errorsJoinText(values ...error) string {
	parts := make([]string, 0, len(values))
	for _, value := range values {
		if value != nil {
			parts = append(parts, value.Error())
		}
	}
	return strings.Join(parts, "; ")
}
