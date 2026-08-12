package source

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	pathpkg "path"
	"sort"
	"strings"
	"unicode/utf8"
)

// GitTreeEntry is one leaf from a recursive, full-tree Git listing. Regular
// entries name a blob; a 160000 entry names an opaque gitlink commit.
type GitTreeEntry struct {
	Path     string
	Mode     string
	ObjectID string
}

// GitBlobReader writes the exact unfiltered bytes of one requested Git blob.
type GitBlobReader func(context.Context, string, io.Writer) error

type objectTreeNode struct {
	directories map[string]*objectTreeNode
	entries     map[string]trackedEntry
}

type objectTreeItem struct {
	name      string
	mode      string
	objectID  []byte
	directory bool
}

type boundedObjectBlob struct {
	ctx    context.Context
	failed error
	buffer bytes.Buffer
}

func (blob *boundedObjectBlob) Write(content []byte) (int, error) {
	if blob.failed != nil {
		return 0, blob.failed
	}
	if err := blob.ctx.Err(); err != nil {
		blob.failed = err
		return 0, err
	}
	if int64(len(content)) > maximumRegularFileBytes-int64(blob.buffer.Len()) {
		blob.failed = fmt.Errorf("blob exceeds %d bytes", maximumRegularFileBytes)
		return 0, blob.failed
	}
	written, err := blob.buffer.Write(content)
	if err == nil && written != len(content) {
		err = io.ErrShortWrite
	}
	if err != nil {
		blob.failed = err
	}
	return written, err
}

// TreeDigestFromGitObjects authenticates a recursive Git tree listing against
// rootTreeID, authenticates every regular blob supplied by readBlob, and then
// computes the exact production TreeDigest framing without creating a
// worktree. Gitlinks stay opaque: their commit IDs are committed by the
// authenticated tree and by the digest, but their repositories are not read.
// directoryMode is the permission mode of the corresponding canonical
// worktree directories (for example, 0775); special mode bits are rejected.
func TreeDigestFromGitObjects(
	ctx context.Context,
	objectFormat string,
	rootTreeID string,
	directoryMode os.FileMode,
	entries []GitTreeEntry,
	readBlob GitBlobReader,
) (string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return "", err
	}
	objectIDLength, err := objectFormatIDLength(objectFormat)
	if err != nil {
		return "", err
	}
	if err := validateObjectID(rootTreeID, objectIDLength); err != nil {
		return "", fmt.Errorf("invalid root tree object ID: %w", err)
	}
	if directoryMode&^os.ModePerm != 0 {
		return "", errors.New("tree digest directory mode must contain only permission bits")
	}
	if readBlob == nil {
		return "", errors.New("git blob reader is missing")
	}

	root, tracked, directories, err := buildAuthenticatedObjectTree(
		objectIDLength,
		directoryMode,
		entries,
	)
	if err != nil {
		return "", err
	}
	var metadataBytes int64
	reconstructed, err := objectTreeID(ctx, objectFormat, root, &metadataBytes)
	if err != nil {
		return "", err
	}
	if reconstructed != rootTreeID {
		return "", fmt.Errorf(
			"reconstructed root tree ID is %s, want %s",
			reconstructed,
			rootTreeID,
		)
	}

	return hashTreeDigest(
		ctx,
		directories,
		tracked,
		func(ctx context.Context, entry trackedEntry) ([]byte, error) {
			blob := &boundedObjectBlob{ctx: ctx}
			readErr := readBlob(ctx, entry.objectID, blob)
			if err := errors.Join(readErr, blob.failed); err != nil {
				return nil, fmt.Errorf("read Git blob for %q: %w", entry.path, err)
			}
			content := bytes.Clone(blob.buffer.Bytes())
			objectID, err := gitBlobID(objectFormat, content)
			if err != nil {
				return nil, err
			}
			if objectID != entry.objectID {
				return nil, fmt.Errorf(
					"git blob for %q hashes to %s, want %s",
					entry.path,
					objectID,
					entry.objectID,
				)
			}
			return content, nil
		},
	)
}

func objectFormatIDLength(objectFormat string) (int, error) {
	switch objectFormat {
	case "sha1":
		return 40, nil
	case "sha256":
		return 64, nil
	default:
		return 0, fmt.Errorf("unsupported Git object format %q", objectFormat)
	}
}

func buildAuthenticatedObjectTree(
	objectIDLength int,
	directoryMode os.FileMode,
	entries []GitTreeEntry,
) (*objectTreeNode, []trackedEntry, []worktreeDirectory, error) {
	if len(entries) > maximumTrackedEntries {
		return nil, nil, nil, fmt.Errorf(
			"git tree exceeds %d tracked entries",
			maximumTrackedEntries,
		)
	}
	sorted := append([]GitTreeEntry(nil), entries...)
	sort.Slice(sorted, func(left, right int) bool {
		return sorted[left].Path < sorted[right].Path
	})
	root := &objectTreeNode{
		directories: make(map[string]*objectTreeNode),
		entries:     make(map[string]trackedEntry),
	}
	tracked := make([]trackedEntry, 0, len(sorted))
	requiredDirectories := map[string]struct{}{".": {}}
	directoryCount := 1
	for position, candidate := range sorted {
		if err := validateGitObjectTreePath(candidate.Path); err != nil {
			return nil, nil, nil, fmt.Errorf("unsafe Git tree path %q: %w", candidate.Path, err)
		}
		if err := validateTrackedMode(candidate.Mode); err != nil {
			return nil, nil, nil, fmt.Errorf("git tree path %q: %w", candidate.Path, err)
		}
		if err := validateObjectID(candidate.ObjectID, objectIDLength); err != nil {
			return nil, nil, nil, fmt.Errorf(
				"git tree path %q has invalid object ID: %w",
				candidate.Path,
				err,
			)
		}
		if position > 0 && sorted[position-1].Path == candidate.Path {
			return nil, nil, nil, fmt.Errorf("duplicate Git tree path %q", candidate.Path)
		}

		entry := trackedEntry{
			path: candidate.Path, mode: candidate.Mode, objectID: candidate.ObjectID,
		}
		tracked = append(tracked, entry)
		components := strings.Split(candidate.Path, "/")
		node := root
		currentDirectory := ""
		for _, component := range components[:len(components)-1] {
			if _, collision := node.entries[component]; collision {
				return nil, nil, nil, fmt.Errorf(
					"git tree path %q traverses file %q",
					candidate.Path,
					pathpkg.Join(currentDirectory, component),
				)
			}
			next, found := node.directories[component]
			if !found {
				directoryCount++
				if directoryCount > maximumWorktreeEntries {
					return nil, nil, nil, fmt.Errorf(
						"git tree exceeds %d directories",
						maximumWorktreeEntries,
					)
				}
				next = &objectTreeNode{
					directories: make(map[string]*objectTreeNode),
					entries:     make(map[string]trackedEntry),
				}
				node.directories[component] = next
			}
			node = next
			currentDirectory = pathpkg.Join(currentDirectory, component)
			if candidate.Mode != gitlinkMode {
				requiredDirectories[currentDirectory] = struct{}{}
			}
		}
		name := components[len(components)-1]
		if _, collision := node.directories[name]; collision {
			return nil, nil, nil, fmt.Errorf(
				"git tree path %q collides with a directory",
				candidate.Path,
			)
		}
		node.entries[name] = entry
	}

	directories := make([]worktreeDirectory, 0, len(requiredDirectories))
	for directory := range requiredDirectories {
		directories = append(directories, worktreeDirectory{
			path: directory,
			mode: os.ModeDir | directoryMode,
		})
	}
	sort.Slice(directories, func(left, right int) bool {
		return directories[left].path < directories[right].path
	})
	return root, tracked, directories, nil
}

func validateGitObjectTreePath(value string) error {
	if err := validateSourceRelativePath(value); err != nil {
		return err
	}
	switch {
	case !utf8.ValidString(value):
		return errors.New("path is not valid UTF-8")
	case strings.ContainsAny(value, "\r\n\\"):
		return errors.New("path contains a forbidden byte")
	}
	for _, component := range strings.Split(value, "/") {
		if strings.EqualFold(component, ".git") {
			return errors.New("path contains reserved .git component")
		}
	}
	return nil
}

func objectTreeID(
	ctx context.Context,
	objectFormat string,
	tree *objectTreeNode,
	metadataBytes *int64,
) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if tree == nil || metadataBytes == nil {
		return "", errors.New("git tree reconstruction is incomplete")
	}
	items := make([]objectTreeItem, 0, len(tree.entries)+len(tree.directories))
	for name, entry := range tree.entries {
		objectID, err := hex.DecodeString(entry.objectID)
		if err != nil {
			return "", err
		}
		items = append(items, objectTreeItem{
			name: name, mode: entry.mode, objectID: objectID,
		})
	}
	for name, child := range tree.directories {
		objectID, err := objectTreeID(ctx, objectFormat, child, metadataBytes)
		if err != nil {
			return "", err
		}
		decoded, err := hex.DecodeString(objectID)
		if err != nil {
			return "", err
		}
		items = append(items, objectTreeItem{
			name: name, mode: "40000", objectID: decoded, directory: true,
		})
	}
	sort.Slice(items, func(left, right int) bool {
		leftSuffix, rightSuffix := byte(0), byte(0)
		if items[left].directory {
			leftSuffix = '/'
		}
		if items[right].directory {
			rightSuffix = '/'
		}
		leftKey := append(append([]byte(nil), items[left].name...), leftSuffix)
		rightKey := append(append([]byte(nil), items[right].name...), rightSuffix)
		return bytes.Compare(leftKey, rightKey) < 0
	})
	var content bytes.Buffer
	for _, item := range items {
		itemBytes := len(item.mode) + 1 + len(item.name) + 1 + len(item.objectID)
		if int64(itemBytes) > maximumGitCommandOutputBytes-*metadataBytes {
			return "", fmt.Errorf(
				"git tree metadata exceeds %d bytes",
				maximumGitCommandOutputBytes,
			)
		}
		*metadataBytes += int64(itemBytes)
		_, _ = content.WriteString(item.mode)
		_ = content.WriteByte(' ')
		_, _ = content.WriteString(item.name)
		_ = content.WriteByte(0)
		_, _ = content.Write(item.objectID)
	}
	return gitObjectID(objectFormat, "tree", content.Bytes())
}
