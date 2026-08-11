//go:build linux

package workspace

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/scopesifter/scopesifter/benchmarks/tokenbench/snapshot"
	"golang.org/x/sys/unix"
)

const requirePrivilegedWorkspaceTestsEnvironment = "TOKENBENCH_REQUIRE_PRIVILEGED_TESTS"

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
	rootPath := filepath.Join(parentPath, "workspace")
	parentMount, err := containingPrivateMount(rootPath)
	if err != nil {
		t.Fatalf("private workspace parent mount: %v", err)
	}
	parent, underlyingRoot, underlyingInfo, leaf, err := claimWorkspaceRoot(rootPath)
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
		parent: parent, underlyingRoot: underlyingRoot, underlyingInfo: underlyingInfo,
		rootPath: rootPath, rootLeaf: leaf, namespace: namespace, parentMount: parentMount,
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
	if err := os.Mkdir(filepath.Join(lowerPath, ".git"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lowerPath, ".git", "config"), []byte("hidden\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	baseline := []byte("baseline\n")
	if err := os.WriteFile(filepath.Join(lowerPath, "README"), baseline, 0o644); err != nil {
		t.Fatal(err)
	}

	for iteration := range 2 {
		arm := privilegedTestArm(t, pair, lowerPath)
		fileInfo, err := os.Stat(filepath.Join(lowerPath, "README"))
		if err != nil {
			t.Fatal(err)
		}
		expected := []worktreeEntry{
			{path: ".", kind: snapshot.ManifestKindDirectory, mode: 0o700},
			{path: "README", kind: snapshot.ManifestKindFile, digest: digest(baseline), mode: uint32(fileInfo.Mode().Perm()), size: int64(len(baseline))},
		}
		actual, err := scanWorktree(context.Background(), arm.overlayRoot, limits)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(actual, expected) {
			t.Fatalf("arm %d initial tree = %#v, want %#v", iteration, actual, expected)
		}
		if err := verifyInitialWorktree(context.Background(), arm.overlayRoot, expected, limits); err != nil {
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
		}
		if err := os.WriteFile(filepath.Join(arm.paths.ModelRoot, "mutation"), []byte(fmt.Sprint(iteration)), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(arm.paths.CacheRoot, "cache"), []byte("private"), 0o600); err != nil {
			t.Fatal(err)
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
	if _, err := os.Lstat(rootPath); !os.IsNotExist(err) {
		t.Fatalf("workspace root remains after cleanup: %v", err)
	}
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
	arm.lower, err = openDirectoryNoSymlinks(lowerPath)
	if err == nil {
		arm.target, err = createDirectoryAt(pair.mountedRoot, worktreeDirectory)
	}
	if err == nil {
		arm.upper, err = createDirectoryAt(pair.mountedRoot, upperDirectory)
	}
	if err == nil {
		arm.work, err = createDirectoryAt(pair.mountedRoot, workDirectory)
	}
	if err == nil {
		arm.cache, err = createDirectoryAt(pair.mountedRoot, cacheDirectory)
	}
	if err == nil {
		arm.capture, err = createDirectoryAt(pair.mountedRoot, captureDirectory)
	}
	if err == nil {
		err = createGitWhiteout(arm.upper)
	}
	if err == nil {
		err = arm.attachOverlay()
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
