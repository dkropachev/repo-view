//go:build linux

package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	pathpkg "path"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/scopesifter/scopesifter/benchmarks/tokenbench/snapshot"
	"golang.org/x/sys/unix"
)

const maximumWorkspacePathDepth = 256

type worktreeEntry struct {
	path   string
	kind   string
	digest string
	mode   uint32
	size   int64
}

func expectedWorktreeManifest(
	inputs snapshot.ExecutionInputs,
	limits Limits,
) ([]worktreeEntry, error) {
	entries := make([]worktreeEntry, 0, len(inputs.Manifest))
	seen := make(map[string]struct{}, len(inputs.Manifest))
	for _, entry := range inputs.Manifest {
		relative, err := filepath.Rel(inputs.SourceRoot, entry.SnapshotPath)
		if err != nil || relative == ".." || filepath.IsAbs(relative) ||
			strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			continue
		}
		relative = filepath.ToSlash(relative)
		if relative == ".git" || strings.HasPrefix(relative, ".git/") {
			continue
		}
		if !validWorktreeRelativePath(relative) {
			return nil, fmt.Errorf("snapshot manifest contains invalid worktree path %q", relative)
		}
		if _, exists := seen[relative]; exists {
			return nil, fmt.Errorf("snapshot worktree path %q is duplicated", relative)
		}
		seen[relative] = struct{}{}
		converted := worktreeEntry{
			path: relative, kind: entry.Kind, mode: entry.Mode,
			size: entry.Size,
		}
		switch entry.Kind {
		case snapshot.ManifestKindDirectory:
			if entry.Size != 0 {
				return nil, fmt.Errorf("snapshot worktree directory %q has a size", relative)
			}
			if relative == "." {
				converted.mode = 0o700
			}
		case snapshot.ManifestKindFile:
			if entry.Size < 0 || entry.Size > limits.MaximumFileBytes ||
				!validSHA256(entry.SHA256) {
				return nil, fmt.Errorf("snapshot worktree file %q exceeds workspace limits", relative)
			}
			converted.digest = entry.SHA256
		default:
			return nil, fmt.Errorf("snapshot worktree path %q has unsupported kind", relative)
		}
		entries = append(entries, converted)
	}
	if _, exists := seen["."]; !exists {
		return nil, errors.New("snapshot manifest omits the worktree root")
	}
	if len(entries) > limits.MaximumEntries {
		return nil, errors.New("snapshot worktree exceeds the workspace entry limit")
	}
	sort.Slice(entries, func(left, right int) bool { return entries[left].path < entries[right].path })
	return entries, nil
}

func validWorktreeRelativePath(value string) bool {
	if value == "." {
		return true
	}
	return value != "" && len(value) <= maximumPathBytes && utf8.ValidString(value) &&
		!strings.ContainsRune(value, '\x00') && !pathpkg.IsAbs(value) &&
		pathpkg.Clean(value) == value && value != ".." &&
		!strings.HasPrefix(value, "../") &&
		strings.Count(value, "/") <= maximumWorkspacePathDepth
}

func verifyInitialWorktree(
	ctx context.Context,
	root *os.File,
	expected []worktreeEntry,
	limits Limits,
) error {
	if root == nil {
		return errors.New("workspace overlay descriptor is absent")
	}
	actual, err := scanWorktree(ctx, root, limits)
	if err != nil {
		return fmt.Errorf("scan initial writable workspace: %w", err)
	}
	if !reflect.DeepEqual(actual, expected) {
		return errors.New("initial writable workspace does not match the committed base tree")
	}
	return nil
}

func scanWorktree(ctx context.Context, root *os.File, limits Limits) ([]worktreeEntry, error) {
	var rootStat unix.Stat_t
	if err := unix.Fstat(int(root.Fd()), &rootStat); err != nil {
		return nil, err
	}
	entries := []worktreeEntry{{
		path: ".", kind: snapshot.ManifestKindDirectory,
		mode: rootStat.Mode & 0o777,
	}}
	if len(entries) > limits.MaximumEntries {
		return nil, errors.New("workspace tree exceeds its entry limit")
	}
	if err := scanDirectory(
		ctx,
		root,
		".",
		limits,
		&entries,
	); err != nil {
		return nil, err
	}
	sort.Slice(entries, func(left, right int) bool { return entries[left].path < entries[right].path })
	return entries, nil
}

func scanDirectory(
	ctx context.Context,
	directory *os.File,
	relative string,
	limits Limits,
	entries *[]worktreeEntry,
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	names, err := directoryNames(directory)
	if err != nil {
		return err
	}
	for _, name := range names {
		if err := ctx.Err(); err != nil {
			return err
		}
		if !validPathComponent(name) {
			return errors.New("workspace contains an invalid path component")
		}
		path := name
		if relative != "." {
			path = relative + "/" + name
		}
		if !validWorktreeRelativePath(path) || path == ".git" || strings.HasPrefix(path, ".git/") {
			return fmt.Errorf("workspace contains forbidden path %q", path)
		}
		if len(*entries) >= limits.MaximumEntries {
			return errors.New("workspace tree exceeds its entry limit")
		}
		var before unix.Stat_t
		if err := unix.Fstatat(
			int(directory.Fd()),
			name,
			&before,
			unix.AT_SYMLINK_NOFOLLOW,
		); err != nil {
			return err
		}
		switch before.Mode & unix.S_IFMT {
		case unix.S_IFDIR:
			child, err := openDirectoryAt(directory, name)
			if err != nil {
				return err
			}
			var opened unix.Stat_t
			statErr := unix.Fstat(int(child.Fd()), &opened)
			if statErr != nil || !sameStatIdentity(before, opened) {
				_ = child.Close()
				return errors.Join(errors.New("workspace directory changed while opening"), statErr)
			}
			*entries = append(*entries, worktreeEntry{
				path: path, kind: snapshot.ManifestKindDirectory, mode: before.Mode & 0o777,
			})
			scanErr := scanDirectory(ctx, child, path, limits, entries)
			closeErr := child.Close()
			if scanErr != nil || closeErr != nil {
				return errors.Join(scanErr, closeErr)
			}
		case unix.S_IFREG:
			entry, err := scanRegularFile(directory, name, path, before, limits.MaximumFileBytes)
			if err != nil {
				return err
			}
			*entries = append(*entries, entry)
		default:
			return fmt.Errorf("workspace contains special path %q", path)
		}
	}
	return nil
}

func scanRegularFile(
	directory *os.File,
	name, path string,
	before unix.Stat_t,
	maximumBytes int64,
) (entry worktreeEntry, resultErr error) {
	if before.Nlink != 1 || before.Size < 0 || before.Size > maximumBytes {
		return worktreeEntry{}, fmt.Errorf("workspace file %q violates link or byte limits", path)
	}
	descriptor, err := unix.Openat(
		int(directory.Fd()),
		name,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return worktreeEntry{}, err
	}
	file := os.NewFile(uintptr(descriptor), path)
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	var opened unix.Stat_t
	if err := unix.Fstat(descriptor, &opened); err != nil || !sameStatIdentity(before, opened) {
		return worktreeEntry{}, errors.Join(errors.New("workspace file changed while opening"), err)
	}
	hasher := sha256.New()
	limited := &io.LimitedReader{R: file, N: maximumBytes + 1}
	written, err := io.CopyBuffer(hasher, limited, make([]byte, 128<<10))
	if err != nil || written != before.Size || limited.N == 0 {
		return worktreeEntry{}, errors.Join(errors.New("workspace file changed while hashing"), err)
	}
	var after unix.Stat_t
	if err := unix.Fstat(descriptor, &after); err != nil || !sameStatIdentity(before, after) {
		return worktreeEntry{}, errors.Join(errors.New("workspace file identity changed while hashing"), err)
	}
	return worktreeEntry{
		path: path, kind: snapshot.ManifestKindFile, mode: before.Mode & 0o777,
		size: before.Size, digest: hex.EncodeToString(hasher.Sum(nil)),
	}, nil
}

func sameStatIdentity(left, right unix.Stat_t) bool {
	return left.Dev == right.Dev && left.Ino == right.Ino && left.Mode == right.Mode &&
		left.Nlink == right.Nlink && left.Size == right.Size &&
		left.Mtim == right.Mtim && left.Ctim == right.Ctim
}

func validPathComponent(name string) bool {
	return name != "" && name != "." && name != ".." &&
		len(name) <= maximumPathBytes && utf8.ValidString(name) &&
		!strings.ContainsAny(name, "/\x00")
}

func directoryNames(directory *os.File) ([]string, error) {
	stream, err := openDirectoryAt(directory, ".")
	if err != nil {
		return nil, err
	}
	names, readErr := stream.Readdirnames(-1)
	closeErr := stream.Close()
	if readErr != nil || closeErr != nil {
		return nil, errors.Join(readErr, closeErr)
	}
	sort.Strings(names)
	for index := 1; index < len(names); index++ {
		if names[index-1] == names[index] {
			return nil, errors.New("directory returned duplicate entries")
		}
	}
	return names, nil
}

func removeArmLayout(root *os.File, limits Limits) error {
	if root == nil {
		return errors.New("workspace tmpfs descriptor is absent")
	}
	var rootStat unix.Stat_t
	if err := unix.Fstat(int(root.Fd()), &rootStat); err != nil {
		return err
	}
	budget := int64(limits.MaximumEntries) + 64
	var resultErr error
	for _, name := range []string{
		worktreeDirectory,
		upperDirectory,
		workDirectory,
		cacheDirectory,
		captureDirectory,
	} {
		resultErr = errors.Join(
			resultErr,
			removeDirectoryTreeAt(root, name, rootStat.Dev, &budget, 0),
		)
	}
	resultErr = errors.Join(resultErr, root.Sync())
	return resultErr
}

func removeDirectoryTreeAt(
	parent *os.File,
	name string,
	rootDevice uint64,
	budget *int64,
	depth int,
) error {
	if depth > maximumWorkspacePathDepth {
		return errors.New("workspace cleanup depth exceeds its limit")
	}
	directory, err := openDirectoryAt(parent, name)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	var resultErr error
	names, readErr := directoryNames(directory)
	resultErr = errors.Join(resultErr, readErr)
	for _, childName := range names {
		if *budget <= 0 {
			resultErr = errors.Join(resultErr, errors.New("workspace cleanup entry limit exceeded"))
			break
		}
		*budget--
		if !validPathComponent(childName) {
			resultErr = errors.Join(resultErr, errors.New("workspace cleanup found an invalid name"))
			continue
		}
		var stat unix.Stat_t
		if err := unix.Fstatat(
			int(directory.Fd()),
			childName,
			&stat,
			unix.AT_SYMLINK_NOFOLLOW,
		); err != nil {
			resultErr = errors.Join(resultErr, err)
			continue
		}
		if stat.Dev != rootDevice {
			resultErr = errors.Join(resultErr, errors.New("workspace cleanup crossed a filesystem boundary"))
			continue
		}
		if stat.Mode&unix.S_IFMT == unix.S_IFDIR {
			resultErr = errors.Join(
				resultErr,
				removeDirectoryTreeAt(directory, childName, rootDevice, budget, depth+1),
			)
			continue
		}
		resultErr = errors.Join(
			resultErr,
			unix.Unlinkat(int(directory.Fd()), childName, 0),
		)
	}
	resultErr = errors.Join(resultErr, directory.Sync(), directory.Close())
	if resultErr != nil {
		return resultErr
	}
	return unix.Unlinkat(int(parent.Fd()), name, unix.AT_REMOVEDIR)
}
