//go:build linux

package workspace

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"strings"

	"github.com/scopesifter/scopesifter/benchmarks/tokenbench/snapshot"
	"golang.org/x/sys/unix"
)

func scanCapturedWorktree(
	ctx context.Context,
	root *os.File,
	base []worktreeEntry,
	limits Limits,
) ([]worktreeEntry, error) {
	entries, err := scanWorktree(ctx, root, limits)
	if err != nil {
		return nil, err
	}
	baseDirectories := make(map[string]uint32)
	baseEmptyDirectories := make(map[string]struct{})
	for _, entry := range base {
		if entry.kind == snapshot.ManifestKindDirectory {
			baseDirectories[entry.path] = entry.mode
			baseEmptyDirectories[entry.path] = struct{}{}
		}
	}
	for _, entry := range base {
		if entry.path == "." {
			continue
		}
		parent := entry.path
		for parent != "." {
			separator := strings.LastIndexByte(parent, '/')
			if separator < 0 {
				parent = "."
			} else {
				parent = parent[:separator]
			}
			delete(baseEmptyDirectories, parent)
		}
	}
	for _, entry := range entries {
		switch entry.kind {
		case snapshot.ManifestKindDirectory:
			if entry.path == "." {
				if entry.mode != 0o700 {
					return nil, invalidWorkspaceTree("root mode is invalid")
				}
				continue
			}
			if baseMode, exists := baseDirectories[entry.path]; exists {
				if entry.mode != baseMode {
					return nil, invalidWorkspaceTree("directory %q changed mode", entry.path)
				}
			} else if entry.mode != 0o755 {
				return nil, invalidWorkspaceTree("new directory %q has a noncanonical mode", entry.path)
			}
		case snapshot.ManifestKindFile:
			if entry.mode != 0o644 && entry.mode != 0o755 {
				return nil, invalidWorkspaceTree("file %q has a noncanonical mode", entry.path)
			}
		default:
			return nil, errors.New("captured workspace contains an unsupported entry")
		}
	}
	if err := validateCapturedDirectory(ctx, root, ".", true, baseEmptyDirectories); err != nil {
		return nil, err
	}
	return entries, nil
}

func validateCapturedDirectory(
	ctx context.Context,
	directory *os.File,
	relative string,
	isRoot bool,
	baseEmptyDirectories map[string]struct{},
) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := rejectOpenFileXattrs(directory, relative); err != nil {
		return err
	}
	names, err := directoryNames(directory)
	if err != nil {
		return err
	}
	if !isRoot && len(names) == 0 {
		if _, existedEmpty := baseEmptyDirectories[relative]; !existedEmpty {
			return invalidWorkspaceTree("new or newly-empty directory %q", relative)
		}
	}
	for _, name := range names {
		path := name
		if relative != "." {
			path = relative + "/" + name
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
				return errors.Join(errors.New("captured directory changed while opening"), statErr)
			}
			walkErr := validateCapturedDirectory(ctx, child, path, false, baseEmptyDirectories)
			closeErr := child.Close()
			if closeErr != nil {
				if walkErr != nil {
					return fmt.Errorf(
						"validate captured directory %q (%s) and close it: %w",
						path,
						walkErr.Error(),
						closeErr,
					)
				}
				return fmt.Errorf("close validated captured directory %q: %w", path, closeErr)
			}
			if walkErr != nil {
				return walkErr
			}
		case unix.S_IFREG:
			descriptor, err := unix.Openat(
				int(directory.Fd()),
				name,
				unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
				0,
			)
			if err != nil {
				return err
			}
			file := os.NewFile(uintptr(descriptor), path)
			var opened unix.Stat_t
			statErr := unix.Fstat(descriptor, &opened)
			xattrErr := rejectOpenFileXattrs(file, path)
			closeErr := file.Close()
			if statErr != nil || !sameStatIdentity(before, opened) || closeErr != nil {
				return errors.Join(
					errors.New("captured file identity changed or its descriptor did not close"),
					statErr,
					closeErr,
				)
			}
			if xattrErr != nil {
				return xattrErr
			}
		default:
			return invalidWorkspaceTree("special path %q", path)
		}
	}
	return nil
}

func rejectOpenFileXattrs(file *os.File, path string) error {
	if file == nil {
		return errors.New("captured workspace descriptor is absent")
	}
	var names [4 << 10]byte
	size, err := unix.Flistxattr(int(file.Fd()), names[:])
	if errors.Is(err, unix.ERANGE) {
		return invalidWorkspaceTree("path %q has an oversized extended-attribute list", path)
	}
	if err != nil {
		return fmt.Errorf("list captured workspace attributes for %q: %w", path, err)
	}
	if size != 0 {
		return invalidWorkspaceTree("path %q has extended attributes %q", path, names[:size])
	}
	return nil
}

func worktreeManifestDigest(entries []worktreeEntry) string {
	ordered := append([]worktreeEntry(nil), entries...)
	sort.Slice(ordered, func(left, right int) bool { return ordered[left].path < ordered[right].path })
	hasher := sha256.New()
	for _, entry := range ordered {
		writeFrame(hasher, []byte(entry.path))
		writeFrame(hasher, []byte(entry.kind))
		writeFrame(hasher, []byte(entry.digest))
		writeInt64(hasher, int64(entry.mode))
		writeInt64(hasher, entry.size)
	}
	return hex.EncodeToString(hasher.Sum(nil))
}
