//go:build linux

package snapshot

import (
	"bytes"
	"context"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"
	"unsafe"

	"github.com/scopesifter/scopesifter/benchmarks/tokenbench/source"
	"golang.org/x/sys/unix"
)

// BuildRequest names an absent private root and one validated set of explicit
// origins. Filesystem policy is deliberately not an input.
type BuildRequest struct {
	Root    string
	Origins OriginInputs
}

type inodePin struct {
	file  *os.File
	info  os.FileInfo
	entry ManifestEntry
	rel   string
}

// RetainedPath is a caller-owned duplicate of one inode pinned by a live
// Authority. Entry is the exact manifest identity associated with File.
type RetainedPath struct {
	File  *os.File
	Entry ManifestEntry
}

// Authority is the nonserializable live proof behind ExecutionInputs. It pins
// the snapshot root and every inode until Close. Decoding JSON can never create
// one.
type Authority struct {
	rootInfo os.FileInfo
	closeErr error
	parent   *os.File
	root     *os.File
	inputs   ExecutionInputs
	origins  OriginInputs
	mount    MountIdentity
	pins     []inodePin
	mu       sync.Mutex
	mounted  bool
	closed   bool
	released bool
}

// Inputs returns a deep defensive copy of the serializable audit commitment.
func (authority *Authority) Inputs() (ExecutionInputs, error) {
	if authority == nil {
		return ExecutionInputs{}, errors.New("execution snapshot authority is closed")
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if authority.closed {
		return ExecutionInputs{}, errors.New("execution snapshot authority is closed")
	}
	return cloneInputs(authority.inputs), nil
}

// RetainPaths reverifies the complete snapshot and duplicates the exact
// retained inode pins for paths. It never reopens a published audit path.
// The caller owns every returned descriptor.
func (authority *Authority) RetainPaths(
	ctx context.Context,
	paths []string,
) ([]RetainedPath, error) {
	if ctx == nil {
		return nil, errors.New("execution snapshot context is required")
	}
	if authority == nil {
		return nil, errors.New("execution snapshot authority is closed")
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if authority.closed {
		return nil, errors.New("execution snapshot authority is closed")
	}
	if err := authority.reverifyLocked(ctx); err != nil {
		return nil, err
	}
	return retainPinnedPaths(authority.pins, paths)
}

func retainPinnedPaths(
	pins []inodePin,
	paths []string,
) ([]RetainedPath, error) {
	return retainPinnedPathsWith(pins, paths, duplicateRetainedPath)
}

func retainPinnedPathsWith(
	pins []inodePin,
	paths []string,
	duplicate func(inodePin, string) (*os.File, error),
) (_ []RetainedPath, resultErr error) {
	if len(paths) == 0 {
		return nil, errors.New("at least one snapshot path must be retained")
	}
	pinsByPath := make(map[string]inodePin, len(pins))
	for _, pin := range pins {
		path := pin.entry.SnapshotPath
		if _, exists := pinsByPath[path]; exists {
			return nil, fmt.Errorf("snapshot pin path %q is duplicated", path)
		}
		pinsByPath[path] = pin
	}
	retained := make([]RetainedPath, 0, len(paths))
	defer func() {
		if resultErr == nil {
			return
		}
		resultErr = errors.Join(resultErr, closeRetainedPaths(retained))
		retained = nil
	}()
	seen := make(map[string]struct{}, len(paths))
	for _, path := range paths {
		if _, exists := seen[path]; exists {
			return nil, fmt.Errorf("requested snapshot path %q is duplicated", path)
		}
		seen[path] = struct{}{}
		pin, exists := pinsByPath[path]
		if !exists || pin.file == nil || pin.info == nil {
			return nil, fmt.Errorf("snapshot path %q has no retained inode", path)
		}
		file, err := duplicate(pin, path)
		if err != nil {
			return nil, fmt.Errorf("duplicate retained snapshot path %q: %w", path, err)
		}
		retained = append(retained, RetainedPath{
			File: file, Entry: cloneManifestEntry(pin.entry),
		})
	}
	return retained, nil
}

func duplicateRetainedPath(pin inodePin, path string) (*os.File, error) {
	descriptor, err := unix.FcntlInt(pin.file.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(descriptor), path)
	opened, err := file.Stat()
	if err != nil || !os.SameFile(pin.info, opened) ||
		pin.info.Mode() != opened.Mode() || pin.info.Size() != opened.Size() ||
		!pin.info.ModTime().Equal(opened.ModTime()) {
		closeErr := file.Close()
		return nil, errors.Join(
			errors.New("retained snapshot inode changed while duplicating"),
			err,
			closeErr,
		)
	}
	return file, nil
}

func closeRetainedPaths(paths []RetainedPath) error {
	var resultErr error
	for index := len(paths) - 1; index >= 0; index-- {
		if paths[index].File != nil {
			resultErr = errors.Join(resultErr, paths[index].File.Close())
		}
	}
	return resultErr
}

// Build copies all inputs into a newly-created root, enables fs-verity on
// every regular file, validates the copied source using the copied verifier
// Git, syncs every directory, and retains an open descriptor for every inode.
func Build(ctx context.Context, request BuildRequest) (_ *Authority, resultErr error) {
	if ctx == nil {
		return nil, errors.New("execution snapshot context is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if err := request.Origins.Validate(); err != nil {
		return nil, fmt.Errorf("validate snapshot origins: %w", err)
	}
	rootPath := filepath.Clean(request.Root)
	if !validAbsolutePath(rootPath) || rootPath == "/" || rootPath != request.Root {
		return nil, errors.New("snapshot root must be absolute, canonical, and non-root")
	}
	if _, err := currentMountNamespaceIdentity(); err != nil {
		return nil, fmt.Errorf("preflight snapshot mount namespace: %w", err)
	}
	if _, err := containingPrivateMount(rootPath); err != nil {
		return nil, fmt.Errorf("preflight snapshot mount propagation: %w", err)
	}
	root, parent, rootInfo, err := claimSnapshotRoot(request.Root)
	if err != nil {
		return nil, err
	}
	created := true
	mounted := false
	var mountIdentity MountIdentity
	var cleanupPins []inodePin
	defer func() {
		if resultErr == nil {
			return
		}
		if mounted {
			if verifyErr := verifyMountIdentity(rootPath, rootInfo, mountIdentity); verifyErr != nil {
				resultErr = errors.Join(
					resultErr,
					fmt.Errorf("mounted snapshot residue retained at %q: %w", rootPath, verifyErr),
				)
			} else if unmountErr := unix.Unmount(rootPath, 0); unmountErr != nil {
				resultErr = errors.Join(
					resultErr,
					fmt.Errorf("mounted snapshot residue retained at %q: %w", rootPath, unmountErr),
				)
			} else {
				mounted = false
			}
		}
		if len(cleanupPins) != 0 {
			resultErr = errors.Join(resultErr, closePins(cleanupPins))
			cleanupPins = nil
		}
		if root != nil {
			resultErr = errors.Join(resultErr, root.Close())
		}
		if parent != nil {
			resultErr = errors.Join(resultErr, parent.Close())
		}
		if created && !mounted {
			if cleanupErr := removeEmptyCreatedSnapshot(rootPath, rootInfo); cleanupErr != nil {
				resultErr = errors.Join(
					resultErr,
					fmt.Errorf("snapshot residue retained at %q: %w", rootPath, cleanupErr),
				)
			}
		}
	}()

	sourcePath := filepath.Join(rootPath, "source")
	toolsPath := filepath.Join(rootPath, "tools")
	toolboxPath := filepath.Join(rootPath, "toolbox")
	cachePath := filepath.Join(rootPath, "cache")
	for _, directory := range []string{toolsPath, toolboxPath, cachePath} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			return nil, fmt.Errorf("create snapshot directory %s: %w", directory, err)
		}
	}
	if err := copyTree(ctx, request.Origins.Source.Root, sourcePath); err != nil {
		return nil, fmt.Errorf("copy source snapshot: %w", err)
	}
	toolCopies := []struct {
		name          string
		origin        FileOrigin
		destination   string
		requireStatic bool
	}{
		{"Codex", request.Origins.Codex, filepath.Join(toolsPath, "codex"), true},
		{"scopesifter", request.Origins.ScopeSifter, filepath.Join(toolsPath, "scopesifter"), true},
		// Verifier Git runs only during snapshot construction. Requiring a static
		// image commits all bytes it can execute without an untracked loader or
		// shared-library surface.
		{"verifier Git", request.Origins.Git, filepath.Join(toolsPath, "verifier-git"), true},
		{"bash", request.Origins.Bash, filepath.Join(toolboxPath, "bash"), true},
		{"runner/arm-init", request.Origins.Runner, filepath.Join(toolsPath, "runner-arm-init"), true},
	}
	for _, item := range toolCopies {
		if err := copyExecutable(
			ctx,
			item.origin,
			item.destination,
			item.requireStatic,
		); err != nil {
			return nil, fmt.Errorf("copy %s image: %w", item.name, err)
		}
	}
	utilityOrigins := request.Origins.Utilities.named()
	utilityNames := make([]string, 0, len(utilityOrigins))
	for name := range utilityOrigins {
		utilityNames = append(utilityNames, name)
	}
	sort.Strings(utilityNames)
	for _, name := range utilityNames {
		if err := copyExecutable(
			ctx,
			utilityOrigins[name],
			filepath.Join(toolboxPath, name),
			true,
		); err != nil {
			return nil, fmt.Errorf("copy %s utility image: %w", name, err)
		}
	}
	allExecutablePaths := append([]string{
		filepath.Join(toolsPath, "codex"),
		filepath.Join(toolsPath, "scopesifter"),
		filepath.Join(toolsPath, "verifier-git"),
		filepath.Join(toolboxPath, "bash"),
		filepath.Join(toolsPath, "runner-arm-init"),
	}, utilityPathsForRoot(toolboxPath).values()...)
	if err := validateNativeExecutableABIs(allExecutablePaths); err != nil {
		return nil, err
	}

	gitPath := filepath.Join(toolsPath, "verifier-git")
	verified, err := source.Verify(ctx, source.Expected{
		Root:                sourcePath,
		Revision:            request.Origins.Source.Revision,
		Base:                request.Origins.Source.Base,
		TreeSHA256:          request.Origins.Source.TreeSHA256,
		GitExecutable:       gitPath,
		GitExecutableSHA256: request.Origins.Git.SHA256,
	})
	if err != nil {
		return nil, fmt.Errorf("verify copied source snapshot: %w", err)
	}
	if verified.GitMetadataSHA256 != request.Origins.Source.GitMetadataSHA256 {
		return nil, errors.New("copied Git metadata identity differs from its origin")
	}
	changedCache, changedRaw, err := buildChangedStateCache(
		ctx,
		sourcePath,
		gitPath,
		request.Origins.Git.SHA256,
		verified.Base,
		verified.Revision,
	)
	if err != nil {
		return nil, err
	}
	changedPath := filepath.Join(cachePath, "changed-state.json")
	changedDigest, _, err := writeImmutableFile(changedPath, changedRaw, 0o444)
	if err != nil {
		return nil, fmt.Errorf("write changed-state cache: %w", err)
	}

	if err := os.Chmod(toolsPath, 0o500); err != nil {
		return nil, fmt.Errorf("seal snapshot tools directory: %w", err)
	}
	if err := os.Chmod(toolboxPath, 0o500); err != nil {
		return nil, fmt.Errorf("seal snapshot toolbox directory: %w", err)
	}
	if err := os.Chmod(cachePath, 0o500); err != nil {
		return nil, fmt.Errorf("seal snapshot cache directory: %w", err)
	}
	if err := os.Chmod(rootPath, 0o500); err != nil {
		return nil, fmt.Errorf("seal snapshot root directory: %w", err)
	}
	if err := syncTreeDirectories(ctx, rootPath); err != nil {
		return nil, err
	}
	if err := parent.Sync(); err != nil {
		return nil, fmt.Errorf("sync snapshot parent after construction: %w", err)
	}
	mountIdentity, err = establishReadOnlySelfBind(rootPath, rootInfo)
	if err != nil {
		return nil, err
	}
	mounted = true
	preMountRoot := root
	root = nil
	if err := preMountRoot.Close(); err != nil {
		return nil, fmt.Errorf("close pre-mount snapshot root: %w", err)
	}
	root, err = os.Open(rootPath)
	if err != nil {
		return nil, fmt.Errorf("open read-only snapshot mount: %w", err)
	}
	mountedInfo, err := root.Stat()
	if err != nil || !os.SameFile(rootInfo, mountedInfo) {
		return nil, errors.Join(errors.New("read-only snapshot mount changed root inode"), err)
	}

	manifest, pins, err := pinManifest(ctx, rootPath, root)
	if err != nil {
		return nil, err
	}
	pinnedRoot := root
	root = nil
	cleanupPins = pins
	manifestDigest, err := manifestSHA256(manifest)
	if err != nil {
		return nil, err
	}
	inputs := ExecutionInputs{
		SchemaVersion:          ExecutionSchemaVersion,
		SnapshotRoot:           rootPath,
		SourceRoot:             sourcePath,
		GitMetadataRoot:        filepath.Join(sourcePath, ".git"),
		CodexExecutable:        filepath.Join(toolsPath, "codex"),
		ScopeSifterExecutable:  filepath.Join(toolsPath, "scopesifter"),
		VerifierGitExecutable:  gitPath,
		BashExecutable:         filepath.Join(toolboxPath, "bash"),
		Utilities:              utilityPathsForRoot(toolboxPath),
		ToolboxRoot:            toolboxPath,
		RunnerExecutable:       filepath.Join(toolsPath, "runner-arm-init"),
		ArmInitExecutable:      filepath.Join(toolsPath, "runner-arm-init"),
		RunnerArmInitSameImage: true,
		SourceRevision:         verified.Revision,
		SourceBaseRevision:     verified.Base,
		SourceTreeSHA256:       verified.TreeSHA256,
		GitMetadataSHA256:      verified.GitMetadataSHA256,
		OriginCommitment:       request.Origins.Commitment,
		ChangedState: ChangedStateIdentity{
			SchemaVersion:     ChangedStateSchemaVersion,
			Path:              changedPath,
			SHA256:            changedDigest,
			BaseCommit:        changedCache.BaseCommit,
			HeadCommit:        changedCache.HeadCommit,
			HeadSubjectSHA256: digest([]byte(changedCache.HeadSubject)),
			ChangedFileCount:  len(changedCache.ChangedFiles),
			PatchBytes:        len(changedCache.Patch),
			PerFilePatchBytes: changedStatePerFilePatchBytes(changedCache),
		},
		ChangedStateCache: changedCache,
		PathIsolation:     mountIdentity,
		Manifest:          manifest,
		ManifestSHA256:    manifestDigest,
		ReadOnlyPaths:     []string{sourcePath, changedPath},
		ExecutablePaths: append([]string{
			filepath.Join(toolsPath, "codex"),
			filepath.Join(toolsPath, "scopesifter"),
			filepath.Join(toolboxPath, "bash"),
		}, utilityPathsForRoot(toolboxPath).values()...),
	}
	sort.Strings(inputs.ExecutablePaths)
	inputs.Commitment, err = executionCommitment(inputs)
	if err != nil {
		return nil, err
	}
	if err := inputs.Validate(); err != nil {
		return nil, fmt.Errorf("validate built execution snapshot: %w", err)
	}
	authority := &Authority{
		inputs:   cloneInputs(inputs),
		origins:  request.Origins,
		parent:   parent,
		root:     pinnedRoot,
		rootInfo: rootInfo,
		mount:    mountIdentity,
		pins:     pins,
		mounted:  true,
	}
	if err := authority.reverifyLocked(ctx); err != nil {
		return nil, fmt.Errorf("initial snapshot reverify: %w", err)
	}
	cleanupPins = nil
	parent = nil
	created = false
	mounted = false
	return authority, nil
}

func claimSnapshotRoot(path string) (*os.File, *os.File, os.FileInfo, error) {
	if !validAbsolutePath(path) || path == "/" {
		return nil, nil, nil, errors.New("snapshot root must be absolute, canonical, and non-root")
	}
	parentPath := filepath.Dir(path)
	resolvedParent, err := filepath.EvalSymlinks(parentPath)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("resolve snapshot parent: %w", err)
	}
	if resolvedParent != parentPath {
		return nil, nil, nil, errors.New("snapshot parent path contains a symbolic link")
	}
	parent, err := os.Open(parentPath)
	if err != nil {
		return nil, nil, nil, fmt.Errorf("open snapshot parent: %w", err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		parent.Close()
		if errors.Is(err, os.ErrExist) {
			return nil, nil, nil, errors.New("snapshot root must not already exist")
		}
		return nil, nil, nil, fmt.Errorf("claim snapshot root: %w", err)
	}
	root, err := os.Open(path)
	if err != nil {
		parent.Close()
		_ = os.Remove(path)
		return nil, nil, nil, fmt.Errorf("open claimed snapshot root: %w", err)
	}
	info, err := root.Stat()
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0o700 {
		root.Close()
		parent.Close()
		_ = os.Remove(path)
		return nil, nil, nil, errors.New("claimed snapshot root is not a private directory")
	}
	pathInfo, err := os.Lstat(path)
	if err != nil || !os.SameFile(info, pathInfo) {
		root.Close()
		parent.Close()
		_ = os.Remove(path)
		return nil, nil, nil, errors.New("snapshot root changed while it was claimed")
	}
	if err := parent.Sync(); err != nil {
		root.Close()
		parent.Close()
		_ = os.Remove(path)
		return nil, nil, nil, fmt.Errorf("sync snapshot parent: %w", err)
	}
	return root, parent, info, nil
}

type directoryMetadata struct {
	modTime time.Time
	path    string
	mode    os.FileMode
}

func copyTree(ctx context.Context, origin, destination string) error {
	if !validAbsolutePath(origin) {
		return errors.New("source origin root must be absolute and canonical")
	}
	if err := rejectDescendantMounts(origin); err != nil {
		return fmt.Errorf("source origin mount topology: %w", err)
	}
	rootInfo, err := os.Lstat(origin)
	if err != nil || rootInfo.Mode()&os.ModeSymlink != 0 || !rootInfo.IsDir() {
		return errors.New("source origin root is not a real directory")
	}
	rootDevice, ok := linuxDevice(rootInfo)
	if !ok {
		return errors.New("source origin root lacks Linux device identity")
	}
	directories := make([]directoryMetadata, 0, 1024)
	entries := 0
	var totalBytes int64
	err = filepath.WalkDir(origin, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		entries++
		if entries > maximumManifestEntries-8 {
			return errors.New("source snapshot exceeds its entry limit")
		}
		relative, err := filepath.Rel(origin, path)
		if err != nil || !validRelativePath(relative) {
			return fmt.Errorf("source path %q is not canonical", path)
		}
		destinationPath := destination
		if relative != "." {
			destinationPath = filepath.Join(destination, relative)
		}
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		device, ok := linuxDevice(info)
		if !ok || device != rootDevice {
			return fmt.Errorf("source path %q crosses a filesystem device boundary", relative)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("source snapshot contains symbolic link %q", relative)
		}
		switch {
		case info.IsDir():
			if err := os.Mkdir(destinationPath, 0o700); err != nil {
				return err
			}
			directories = append(directories, directoryMetadata{
				path: destinationPath, mode: info.Mode(), modTime: info.ModTime(),
			})
		case info.Mode().IsRegular():
			if hasMultipleLinks(info) {
				return fmt.Errorf("source file %q has multiple hard links", relative)
			}
			if info.Size() < 0 || info.Size() > maximumRegularFileBytes ||
				totalBytes > maximumSnapshotBytes-info.Size() {
				return fmt.Errorf("source file %q exceeds snapshot byte limits", relative)
			}
			totalBytes += info.Size()
			if _, err := copyRegularFile(ctx, path, destinationPath, info.Mode()); err != nil {
				return err
			}
		default:
			return fmt.Errorf("source path %q is not a regular file or directory", relative)
		}
		return nil
	})
	if err != nil {
		return errors.Join(err, rejectDescendantMounts(origin))
	}
	for index := len(directories) - 1; index >= 0; index-- {
		directory := directories[index]
		if err := os.Chmod(directory.path, directory.mode.Perm()); err != nil {
			return fmt.Errorf("restore source directory mode: %w", err)
		}
		if err := os.Chtimes(directory.path, directory.modTime, directory.modTime); err != nil {
			return fmt.Errorf("restore source directory timestamp: %w", err)
		}
	}
	if err := rejectDescendantMounts(origin); err != nil {
		return fmt.Errorf("source origin mount topology changed while copying: %w", err)
	}
	return nil
}

func linuxDevice(info os.FileInfo) (uint64, bool) {
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil || stat.Dev == 0 {
		return 0, false
	}
	return stat.Dev, true
}

func copyExecutable(
	ctx context.Context,
	origin FileOrigin,
	destination string,
	requireStatic bool,
) error {
	before, err := os.Lstat(origin.Path)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() ||
		before.Mode().Perm()&0o111 == 0 || hasMultipleLinks(before) {
		return errors.New("origin executable is not a single-link executable regular file")
	}
	digest, err := copyRegularFile(ctx, origin.Path, destination, 0o555)
	if err != nil {
		return err
	}
	if digest != origin.SHA256 {
		return fmt.Errorf("copied executable digest %s does not match origin %s", digest, origin.SHA256)
	}
	file, err := os.Open(destination)
	if err != nil {
		return err
	}
	identity, isELF, identityErr := inspectELF(file)
	closeErr := file.Close()
	if identityErr != nil || closeErr != nil {
		return errors.Join(identityErr, closeErr)
	}
	if !isELF {
		return errors.New("publishable executable origin is not an ELF image")
	}
	if requireStatic && (!identity.Static || identity.Interpreter != "" ||
		identity.LoaderSHA256 != "" || len(identity.Needed) != 0) {
		return errors.New("publishable executable origin is dynamically linked")
	}
	return nil
}

func validateNativeExecutableABIs(paths []string) error {
	native, err := inspectELFABIPath("/proc/self/exe")
	if err != nil {
		return fmt.Errorf("identify native builder ELF ABI: %w", err)
	}
	for _, path := range paths {
		identity, err := inspectELFABIPath(path)
		if err != nil {
			return fmt.Errorf("identify executable %q ELF ABI: %w", path, err)
		}
		if native != identity {
			return fmt.Errorf("executable %q ELF ABI differs from the native runner", path)
		}
	}
	return nil
}

type executableABI struct {
	class   elf.Class
	data    elf.Data
	machine elf.Machine
}

func inspectELFABIPath(path string) (identity executableABI, resultErr error) {
	file, err := os.Open(path)
	if err != nil {
		return executableABI{}, err
	}
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	parsed, err := elf.NewFile(file)
	if err != nil {
		return executableABI{}, err
	}
	return executableABI{class: parsed.Class, data: parsed.Data, machine: parsed.Machine}, nil
}

func copyRegularFile(
	ctx context.Context,
	origin, destination string,
	mode os.FileMode,
) (digestValue string, resultErr error) {
	before, err := os.Lstat(origin)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() ||
		hasMultipleLinks(before) || before.Size() < 0 || before.Size() > maximumRegularFileBytes {
		return "", errors.New("copy origin is not a bounded single-link regular file")
	}
	descriptor, err := unix.Open(origin, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", fmt.Errorf("open copy origin: %w", err)
	}
	sourceFile := os.NewFile(uintptr(descriptor), origin)
	defer func() { resultErr = errors.Join(resultErr, sourceFile.Close()) }()
	opened, err := sourceFile.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return "", errors.New("copy origin changed while opening")
	}
	destinationFile, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", fmt.Errorf("create snapshot file: %w", err)
	}
	destinationOpen := true
	defer func() {
		if destinationOpen {
			resultErr = errors.Join(resultErr, destinationFile.Close())
		}
	}()
	hasher := sha256.New()
	limited := &io.LimitedReader{R: contextReader{ctx: ctx, reader: sourceFile}, N: maximumRegularFileBytes + 1}
	written, err := io.CopyBuffer(io.MultiWriter(destinationFile, hasher), limited, make([]byte, 128<<10))
	if err != nil {
		return "", fmt.Errorf("copy snapshot file: %w", err)
	}
	if written != opened.Size() || limited.N == 0 {
		return "", errors.New("copy origin size changed or exceeded its limit")
	}
	if err := destinationFile.Sync(); err != nil {
		return "", fmt.Errorf("sync snapshot file: %w", err)
	}
	if err := destinationFile.Chmod(mode.Perm()); err != nil {
		return "", fmt.Errorf("set snapshot file mode: %w", err)
	}
	if err := os.Chtimes(destination, opened.ModTime(), opened.ModTime()); err != nil {
		return "", fmt.Errorf("set snapshot file timestamp: %w", err)
	}
	if err := destinationFile.Close(); err != nil {
		destinationOpen = false
		return "", err
	}
	destinationOpen = false
	after, err := os.Lstat(origin)
	if err != nil || !sameStableFile(opened, after) {
		return "", errors.New("copy origin changed while copying")
	}
	digestValue = hex.EncodeToString(hasher.Sum(nil))
	_, err = enableAndMeasureFSVerity(destination)
	if err != nil {
		return "", err
	}
	return digestValue, nil
}

type contextReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader contextReader) Read(content []byte) (int, error) {
	if err := reader.ctx.Err(); err != nil {
		return 0, err
	}
	read, err := reader.reader.Read(content)
	if err == nil {
		if contextErr := reader.ctx.Err(); contextErr != nil {
			return read, contextErr
		}
	}
	return read, err
}

func enableAndMeasureFSVerity(path string) (measurement string, resultErr error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	blockSize, err := fsVerityMerkleBlockSize(file)
	if err != nil {
		return "", fmt.Errorf("select fs-verity Merkle block size for %s: %w", path, err)
	}
	argument := unix.FsverityEnableArg{
		Version:        1,
		Hash_algorithm: unix.FS_VERITY_HASH_ALG_SHA256,
		Block_size:     blockSize,
	}
	_, _, errno := unix.Syscall(
		unix.SYS_IOCTL,
		file.Fd(),
		uintptr(unix.FS_IOC_ENABLE_VERITY),
		uintptr(unsafe.Pointer(&argument)),
	)
	if errno != 0 {
		return "", fmt.Errorf("enable fs-verity for %s: %w", path, errno)
	}
	measurement, err = measureFSVerity(file)
	if err != nil {
		return "", fmt.Errorf("measure fs-verity for %s: %w", path, err)
	}
	flags, err := unix.IoctlGetInt(int(file.Fd()), unix.FS_IOC_GETFLAGS)
	if err != nil || flags&unix.FS_VERITY_FL == 0 {
		return "", errors.Join(errors.New("fs-verity flag was not retained"), err)
	}
	return measurement, nil
}

func fsVerityMerkleBlockSize(file *os.File) (uint32, error) {
	if file == nil {
		return 0, errors.New("fs-verity file is required")
	}
	var filesystem unix.Statfs_t
	if err := unix.Fstatfs(int(file.Fd()), &filesystem); err != nil {
		return 0, fmt.Errorf("inspect fs-verity filesystem: %w", err)
	}
	pageSize := uint64(os.Getpagesize())
	filesystemBlockSize := uint64(filesystem.Bsize)
	blockSize := min(pageSize, filesystemBlockSize)
	if blockSize < 1024 || blockSize > uint64(^uint32(0)) || blockSize&(blockSize-1) != 0 {
		return 0, fmt.Errorf(
			"unsupported fs-verity page/filesystem block sizes %d/%d",
			pageSize,
			filesystemBlockSize,
		)
	}
	return uint32(blockSize), nil
}

type fsVerityDigest struct {
	Algorithm uint16
	Size      uint16
	Digest    [64]byte
}

func measureFSVerity(file *os.File) (string, error) {
	value := fsVerityDigest{Size: uint16(len(fsVerityDigest{}.Digest))}
	_, _, errno := unix.Syscall(
		unix.SYS_IOCTL,
		file.Fd(),
		uintptr(unix.FS_IOC_MEASURE_VERITY),
		uintptr(unsafe.Pointer(&value)),
	)
	if errno != 0 {
		return "", errno
	}
	if value.Algorithm != unix.FS_VERITY_HASH_ALG_SHA256 || value.Size != 32 {
		return "", fmt.Errorf(
			"unexpected fs-verity measurement algorithm=%d size=%d",
			value.Algorithm,
			value.Size,
		)
	}
	return hex.EncodeToString(value.Digest[:value.Size]), nil
}

func inspectELF(file *os.File) (ELFIdentity, bool, error) {
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return ELFIdentity{}, false, err
	}
	var magic [4]byte
	if _, err := io.ReadFull(file, magic[:]); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			_, _ = file.Seek(0, io.SeekStart)
			return ELFIdentity{}, false, nil
		}
		return ELFIdentity{}, false, err
	}
	if magic != [4]byte{0x7f, 'E', 'L', 'F'} {
		_, _ = file.Seek(0, io.SeekStart)
		return ELFIdentity{}, false, nil
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return ELFIdentity{}, false, err
	}
	parsed, err := elf.NewFile(file)
	if err != nil {
		return ELFIdentity{}, false, fmt.Errorf("parse ELF image: %w", err)
	}
	interpreter := ""
	for _, program := range parsed.Progs {
		if program.Type != elf.PT_INTERP {
			continue
		}
		if interpreter != "" || program.Filesz == 0 || program.Filesz > maximumPathBytes {
			return ELFIdentity{}, false, errors.New("ELF interpreter table is invalid")
		}
		content, err := io.ReadAll(io.LimitReader(program.Open(), maximumPathBytes+1))
		if err != nil || len(content) == 0 || len(content) > maximumPathBytes || content[len(content)-1] != 0 {
			return ELFIdentity{}, false, errors.New("ELF interpreter is invalid")
		}
		interpreter = string(content[:len(content)-1])
		if !validAbsolutePath(interpreter) {
			return ELFIdentity{}, false, errors.New("ELF interpreter path is not canonical")
		}
	}
	needed, err := parsed.DynString(elf.DT_NEEDED)
	if err != nil && !errors.Is(err, elf.ErrNoSymbols) {
		return ELFIdentity{}, false, fmt.Errorf("inspect ELF dependencies: %w", err)
	}
	if errors.Is(err, elf.ErrNoSymbols) {
		needed = []string{}
	}
	needed = canonicalELFDependencies(needed)
	sort.Strings(needed)
	for index, library := range needed {
		if library == "" || len(library) > 512 || strings.ContainsRune(library, '\x00') ||
			index != 0 && needed[index-1] == library {
			return ELFIdentity{}, false, errors.New("ELF dependencies are invalid")
		}
	}
	loaderDigest := ""
	if interpreter != "" {
		loaderDigest, err = hashStableFile(interpreter)
		if err != nil {
			return ELFIdentity{}, false, fmt.Errorf("identify ELF loader: %w", err)
		}
	}
	identity := ELFIdentity{
		Class:        parsed.Class.String(),
		Data:         parsed.Data.String(),
		Machine:      parsed.Machine.String(),
		Type:         parsed.Type.String(),
		Interpreter:  interpreter,
		LoaderSHA256: loaderDigest,
		Needed:       needed,
		Static:       interpreter == "" && len(needed) == 0,
	}
	if err := identity.validate(); err != nil {
		return ELFIdentity{}, false, err
	}
	_, _ = file.Seek(0, io.SeekStart)
	return identity, true, nil
}

func canonicalELFDependencies(needed []string) []string {
	if needed == nil {
		return []string{}
	}
	return needed
}

func hashStableFile(path string) (digestValue string, resultErr error) {
	before, err := os.Lstat(path)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() ||
		hasMultipleLinks(before) || before.Size() < 0 || before.Size() > maximumRegularFileBytes {
		return "", errors.New("file identity is not a bounded single-link regular file")
	}
	descriptor, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return "", err
	}
	file := os.NewFile(uintptr(descriptor), path)
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return "", errors.New("file changed while opening")
	}
	return hashOpenFile(file, opened.Size())
}

func hashOpenFile(file *os.File, size int64) (string, error) {
	if size < 0 || size > maximumRegularFileBytes {
		return "", errors.New("file size is outside snapshot limits")
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	hasher := sha256.New()
	limited := &io.LimitedReader{R: file, N: maximumRegularFileBytes + 1}
	written, err := io.CopyBuffer(hasher, limited, make([]byte, 128<<10))
	if err != nil {
		return "", err
	}
	if written != size || limited.N == 0 {
		return "", errors.New("file size changed while hashing")
	}
	_, _ = file.Seek(0, io.SeekStart)
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func writeImmutableFile(
	path string,
	content []byte,
	mode os.FileMode,
) (digestValue, verityMeasurement string, resultErr error) {
	if int64(len(content)) > maximumRegularFileBytes {
		return "", "", errors.New("immutable file exceeds snapshot limit")
	}
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return "", "", err
	}
	open := true
	defer func() {
		if open {
			resultErr = errors.Join(resultErr, file.Close())
		}
	}()
	if _, err := file.Write(content); err != nil {
		return "", "", err
	}
	if err := file.Sync(); err != nil {
		return "", "", err
	}
	if err := file.Chmod(mode.Perm()); err != nil {
		return "", "", err
	}
	if err := file.Close(); err != nil {
		open = false
		return "", "", err
	}
	open = false
	measurement, err := enableAndMeasureFSVerity(path)
	if err != nil {
		return "", "", err
	}
	return digest(content), measurement, nil
}

func pinManifest(
	ctx context.Context,
	rootPath string,
	root *os.File,
) ([]ManifestEntry, []inodePin, error) {
	type manifestPin struct {
		entry ManifestEntry
		pin   inodePin
	}
	items := make([]manifestPin, 0, 4096)
	var totalBytes int64
	err := filepath.WalkDir(rootPath, func(path string, directoryEntry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if len(items) >= maximumManifestEntries {
			return errors.New("execution snapshot exceeds its entry limit")
		}
		relative, err := filepath.Rel(rootPath, path)
		if err != nil || !validRelativePath(relative) {
			return errors.New("execution snapshot contains a noncanonical path")
		}
		logical, ok := logicalOriginForPath(rootPath, path)
		if !ok {
			return fmt.Errorf("execution snapshot contains unsupported path %q", path)
		}
		before, err := os.Lstat(path)
		if err != nil || before.Mode()&os.ModeSymlink != 0 {
			return errors.New("execution snapshot path changed or is a symbolic link")
		}
		var file *os.File
		if relative == "." {
			file = root
		} else {
			flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW
			if before.IsDir() {
				flags |= unix.O_DIRECTORY
			}
			descriptor, openErr := unix.Openat(int(root.Fd()), relative, flags, 0)
			if openErr != nil {
				return openErr
			}
			file = os.NewFile(uintptr(descriptor), path)
		}
		opened, err := file.Stat()
		if err != nil || !os.SameFile(before, opened) {
			if relative != "." {
				_ = file.Close()
			}
			return errors.New("execution snapshot path changed while pinning")
		}
		entry := ManifestEntry{
			LogicalOrigin: logical,
			SnapshotPath:  path,
			Mode:          uint32(opened.Mode().Perm()),
		}
		switch {
		case opened.IsDir():
			entry.Kind = ManifestKindDirectory
			entry.SHA256 = emptySHA256
		case opened.Mode().IsRegular():
			if hasMultipleLinks(opened) || opened.Size() < 0 ||
				opened.Size() > maximumRegularFileBytes ||
				totalBytes > maximumSnapshotBytes-opened.Size() {
				if relative != "." {
					_ = file.Close()
				}
				return errors.New("execution snapshot file violates hard-link or byte limits")
			}
			totalBytes += opened.Size()
			entry.Kind = ManifestKindFile
			entry.Size = opened.Size()
			entry.SHA256, err = hashOpenFile(file, opened.Size())
			if err == nil {
				entry.FSVerityMeasurement, err = measureFSVerity(file)
			}
			if err != nil {
				if relative != "." {
					_ = file.Close()
				}
				return err
			}
			entry.FSVerity = true
			entry.FSVerityAlgorithm = FSVerityAlgorithm
			identity, isELF, inspectErr := inspectELF(file)
			if inspectErr != nil {
				if relative != "." {
					_ = file.Close()
				}
				return inspectErr
			}
			if isELF {
				entry.ELF = &identity
			}
		default:
			if relative != "." {
				_ = file.Close()
			}
			return errors.New("execution snapshot contains a special inode")
		}
		items = append(items, manifestPin{
			entry: entry,
			pin:   inodePin{file: file, info: opened, entry: entry, rel: relative},
		})
		return nil
	})
	if err != nil {
		pins := make([]inodePin, 0, len(items))
		for _, item := range items {
			if item.pin.rel != "." {
				pins = append(pins, item.pin)
			}
		}
		return nil, nil, errors.Join(
			fmt.Errorf("pin execution snapshot manifest: %w", err),
			closePins(pins),
		)
	}
	sort.Slice(items, func(left, right int) bool {
		return items[left].entry.LogicalOrigin < items[right].entry.LogicalOrigin
	})
	manifest := make([]ManifestEntry, len(items))
	pins := make([]inodePin, len(items))
	for index, item := range items {
		manifest[index] = item.entry
		item.pin.entry = item.entry
		pins[index] = item.pin
	}
	return manifest, pins, nil
}

// Reverify proves that every retained inode, pathname, fs-verity measurement,
// content digest, ELF identity, and source/Git identity still matches.
func (authority *Authority) Reverify(ctx context.Context) error {
	if ctx == nil {
		return errors.New("execution snapshot context is required")
	}
	if authority == nil {
		return errors.New("execution snapshot authority is closed")
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	if authority.closed {
		return errors.New("execution snapshot authority is closed")
	}
	return authority.reverifyLocked(ctx)
}

func (authority *Authority) reverifyLocked(ctx context.Context) error {
	if err := authority.inputs.Validate(); err != nil {
		return err
	}
	if err := authority.origins.Validate(); err != nil ||
		authority.origins.Commitment != authority.inputs.OriginCommitment {
		return errors.Join(errors.New("snapshot origin authority is invalid"), err)
	}
	if !authority.mounted || !reflect.DeepEqual(authority.mount, authority.inputs.PathIsolation) {
		return errors.New("snapshot mount authority is absent")
	}
	if err := verifyMountIdentity(
		authority.inputs.SnapshotRoot,
		authority.rootInfo,
		authority.mount,
	); err != nil {
		return fmt.Errorf("reverify snapshot read-only mount: %w", err)
	}
	pathRoot, err := os.Lstat(authority.inputs.SnapshotRoot)
	if err != nil || !os.SameFile(authority.rootInfo, pathRoot) {
		return errors.New("execution snapshot root pathname changed")
	}
	rebuilt := make([]ManifestEntry, len(authority.pins))
	for index, pin := range authority.pins {
		if err := ctx.Err(); err != nil {
			return err
		}
		opened, err := pin.file.Stat()
		if err != nil || !os.SameFile(pin.info, opened) ||
			opened.Mode().Perm() != os.FileMode(pin.entry.Mode) ||
			(pin.entry.Kind == ManifestKindFile && opened.Size() != pin.entry.Size) {
			return fmt.Errorf("snapshot inode %q metadata changed", pin.entry.SnapshotPath)
		}
		pathFile, err := openPinnedPath(authority.root, pin.rel, pin.entry.Kind)
		if err != nil {
			return err
		}
		pathInfo, statErr := pathFile.Stat()
		closeErr := pathFile.Close()
		if statErr != nil || closeErr != nil || !os.SameFile(opened, pathInfo) {
			return errors.Join(
				fmt.Errorf("snapshot pathname %q changed", pin.entry.SnapshotPath),
				statErr,
				closeErr,
			)
		}
		entry := pin.entry
		if entry.Kind == ManifestKindFile {
			actualDigest, hashErr := hashOpenFile(pin.file, opened.Size())
			actualVerity, verityErr := measureFSVerity(pin.file)
			identity, isELF, elfErr := inspectELF(pin.file)
			if hashErr != nil || verityErr != nil || elfErr != nil {
				return errors.Join(hashErr, verityErr, elfErr)
			}
			if actualDigest != entry.SHA256 || actualVerity != entry.FSVerityMeasurement {
				return fmt.Errorf("snapshot file %q identity changed", entry.SnapshotPath)
			}
			if isELF {
				entry.ELF = &identity
			} else {
				entry.ELF = nil
			}
		}
		rebuilt[index] = entry
	}
	if !equalManifest(rebuilt, authority.inputs.Manifest) {
		return errors.New("execution snapshot manifest changed")
	}
	verified, err := source.Verify(ctx, source.Expected{
		Root:                authority.inputs.SourceRoot,
		Revision:            authority.inputs.SourceRevision,
		Base:                authority.inputs.SourceBaseRevision,
		TreeSHA256:          authority.inputs.SourceTreeSHA256,
		GitExecutable:       authority.inputs.VerifierGitExecutable,
		GitExecutableSHA256: authority.origins.Git.SHA256,
	})
	if err != nil {
		return fmt.Errorf("reverify immutable source snapshot: %w", err)
	}
	if verified.GitMetadataSHA256 != authority.inputs.GitMetadataSHA256 {
		return errors.New("immutable Git metadata identity changed")
	}
	return nil
}

func openPinnedPath(root *os.File, relative, kind string) (*os.File, error) {
	if relative == "." {
		descriptor, err := unix.Dup(int(root.Fd()))
		if err != nil {
			return nil, err
		}
		unix.CloseOnExec(descriptor)
		return os.NewFile(uintptr(descriptor), root.Name()), nil
	}
	flags := unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW
	if kind == ManifestKindDirectory {
		flags |= unix.O_DIRECTORY
	}
	descriptor, err := unix.Openat(int(root.Fd()), relative, flags, 0)
	if err != nil {
		return nil, fmt.Errorf("open pinned snapshot path %q: %w", relative, err)
	}
	return os.NewFile(uintptr(descriptor), relative), nil
}

// Close releases live execution authority. It deliberately does not delete
// the committed image: deletion is a separate lifecycle concern and a failed
// same-UID pathname check must never redirect recursive removal.
func (authority *Authority) Close() error {
	if authority == nil {
		return nil
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	return authority.closeLocked()
}

// Closed reports whether the mount and every descriptor capability were
// released successfully. Evidence publication requires this stronger state,
// not merely an authority that has stopped accepting new operations.
func (authority *Authority) Closed() bool {
	if authority == nil {
		return false
	}
	authority.mu.Lock()
	defer authority.mu.Unlock()
	return authority.released && authority.closed && !authority.mounted &&
		authority.root == nil && authority.parent == nil
}

func (authority *Authority) closeLocked() error {
	if authority.released {
		return nil
	}
	if authority.closed {
		return authority.closeErr
	}
	if authority.mounted {
		if err := verifyMountIdentity(
			authority.inputs.SnapshotRoot,
			authority.rootInfo,
			authority.mount,
		); err != nil {
			return fmt.Errorf("refuse to unmount changed snapshot authority: %w", err)
		}
		if err := unix.Unmount(authority.inputs.SnapshotRoot, 0); err != nil {
			// Keep all descriptors and authority live so an EBUSY cleanup can be
			// retried after the target releases its working directory.
			return fmt.Errorf("unmount read-only execution snapshot: %w", err)
		}
		authority.mounted = false
	}
	authority.closed = true
	var resultErr error
	seenRoot := false
	for index := len(authority.pins) - 1; index >= 0; index-- {
		pin := &authority.pins[index]
		if pin.file == nil {
			continue
		}
		if pin.file == authority.root {
			seenRoot = true
		}
		resultErr = errors.Join(resultErr, pin.file.Close())
		pin.file = nil
	}
	if !seenRoot && authority.root != nil {
		resultErr = errors.Join(resultErr, authority.root.Close())
	}
	authority.root = nil
	if authority.parent != nil {
		resultErr = errors.Join(resultErr, authority.parent.Close())
		authority.parent = nil
	}
	authority.closeErr = resultErr
	authority.released = resultErr == nil
	return resultErr
}

// RequireConformant repeats the kernel mount proof used by BindAdapter.
func (authority *Authority) RequireConformant(ctx context.Context) error {
	if err := authority.Reverify(ctx); err != nil {
		return err
	}
	return nil
}

func closePins(pins []inodePin) error {
	var resultErr error
	seen := make(map[*os.File]struct{}, len(pins))
	for index := len(pins) - 1; index >= 0; index-- {
		file := pins[index].file
		if file == nil {
			continue
		}
		if _, exists := seen[file]; exists {
			continue
		}
		seen[file] = struct{}{}
		resultErr = errors.Join(resultErr, file.Close())
	}
	return resultErr
}

func syncTreeDirectories(ctx context.Context, root string) error {
	directories := make([]string, 0, 1024)
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.IsDir() {
			directories = append(directories, path)
		}
		return nil
	})
	if err != nil {
		return err
	}
	for index := len(directories) - 1; index >= 0; index-- {
		directory, err := os.Open(directories[index])
		if err != nil {
			return err
		}
		syncErr := directory.Sync()
		closeErr := directory.Close()
		if syncErr != nil || closeErr != nil {
			return errors.Join(syncErr, closeErr)
		}
	}
	return nil
}

// removeEmptyCreatedSnapshot performs only a single nonrecursive rmdir after
// proving the pathname still identifies the claimed inode. A partially built
// tree is intentionally retained for explicit recovery; recursively walking a
// mutable pathname during an error path could cross a replacement or mount.
func removeEmptyCreatedSnapshot(path string, expected os.FileInfo) error {
	current, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil || expected == nil || !os.SameFile(expected, current) {
		return errors.Join(errors.New("refuse to remove changed snapshot root"), err)
	}
	return os.Remove(path)
}

func validRelativePath(value string) bool {
	if value == "." {
		return true
	}
	return value != "" && len(value) <= maximumPathBytes &&
		!filepath.IsAbs(value) && filepath.Clean(value) == value &&
		value != ".." && !strings.HasPrefix(value, ".."+string(filepath.Separator)) &&
		strings.Count(filepath.ToSlash(value), "/") <= maximumPathDepth
}

func hasMultipleLinks(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return !ok || stat == nil || stat.Nlink != 1
}

func sameStableFile(left, right os.FileInfo) bool {
	return os.SameFile(left, right) && left.Mode() == right.Mode() &&
		left.Size() == right.Size() && left.ModTime().Equal(right.ModTime())
}

func equalManifest(left, right []ManifestEntry) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		leftRaw, leftErr := json.Marshal(left[index])
		rightRaw, rightErr := json.Marshal(right[index])
		if leftErr != nil || rightErr != nil || !bytes.Equal(leftRaw, rightRaw) {
			return false
		}
	}
	return true
}
