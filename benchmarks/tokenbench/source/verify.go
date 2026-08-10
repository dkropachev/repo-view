// Package source verifies that a benchmark source is clean, self-contained,
// stable, and bound to an expected content digest.
package source

import (
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // Git SHA-1 object IDs require the SHA-1 algorithm.
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	pathpkg "path"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
)

// Expected is the source commitment authored into a suite.
type Expected struct {
	Root                string
	Revision            string
	Base                string
	TreeSHA256          string
	GitExecutable       string
	GitExecutableSHA256 string
}

// Snapshot is the verified source identity used by both arms.
type Snapshot struct {
	Root                string `json:"root"`
	Revision            string `json:"revision"`
	Base                string `json:"base"`
	TreeSHA256          string `json:"tree_sha256"`
	GitMetadataSHA256   string `json:"git_metadata_sha256"`
	GitExecutable       string `json:"git_executable"`
	GitExecutableSHA256 string `json:"git_executable_sha256"`
}

// Verify rejects worktrees whose content or Git storage can depend on another
// checkout. It hashes stable bytes from every tracked regular file.
func Verify(ctx context.Context, expected Expected) (Snapshot, error) {
	root, err := filepath.Abs(expected.Root)
	if err != nil {
		return Snapshot{}, fmt.Errorf("resolve source root: %w", err)
	}
	root = filepath.Clean(root)
	git, err := resolvePinnedGitRunner(
		expected.GitExecutable,
		expected.GitExecutableSHA256,
	)
	if err != nil {
		return Snapshot{}, err
	}
	if err := requireStandaloneGitDir(ctx, git, root); err != nil {
		return Snapshot{}, err
	}
	actualRoot, err := git.output(ctx, root, "rev-parse", "--show-toplevel")
	if err != nil {
		return Snapshot{}, err
	}
	actualRootPath, err := filepath.Abs(strings.TrimSpace(actualRoot))
	if err != nil {
		return Snapshot{}, fmt.Errorf("resolve Git toplevel: %w", err)
	}
	if filepath.Clean(actualRootPath) != root {
		return Snapshot{}, fmt.Errorf(
			"source root %s is not Git toplevel %s",
			root,
			actualRootPath,
		)
	}
	if err := rejectGitAlternates(ctx, git, root); err != nil {
		return Snapshot{}, err
	}
	if err := rejectLocalOverrides(ctx, git, root); err != nil {
		return Snapshot{}, err
	}
	status, err := git.output(
		ctx,
		root,
		"status", "--porcelain=v1", "--untracked-files=all",
	)
	if err != nil {
		return Snapshot{}, err
	}
	if status != "" {
		return Snapshot{}, errors.New("source worktree is dirty or has untracked files")
	}
	others, err := git.outputBytes(ctx, root, "ls-files", "-z", "--others")
	if err != nil {
		return Snapshot{}, err
	}
	if len(others) != 0 {
		return Snapshot{}, errors.New(
			"source worktree contains untracked or ignored files",
		)
	}
	revision, err := git.output(ctx, root, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return Snapshot{}, err
	}
	revision = strings.TrimSpace(revision)
	if revision != expected.Revision {
		return Snapshot{}, fmt.Errorf(
			"source revision mismatch: got %s, want %s",
			revision,
			expected.Revision,
		)
	}
	base, err := git.output(
		ctx,
		root,
		"rev-parse", "--verify", expected.Base+"^{commit}",
	)
	if err != nil {
		return Snapshot{}, fmt.Errorf("verify source base: %w", err)
	}
	base = strings.TrimSpace(base)
	if base != expected.Base {
		return Snapshot{}, fmt.Errorf(
			"source base must be a full commit id: resolved %s from %s",
			base,
			expected.Base,
		)
	}
	if err := git.run(ctx, root, "merge-base", "--is-ancestor", expected.Base, revision); err != nil {
		return Snapshot{}, fmt.Errorf("source base is not an ancestor of revision: %w", err)
	}
	digest, err := treeDigest(ctx, git, root)
	if err != nil {
		return Snapshot{}, err
	}
	secondDigest, err := treeDigest(ctx, git, root)
	if err != nil {
		return Snapshot{}, err
	}
	if secondDigest != digest {
		return Snapshot{}, errors.New("source tree changed while it was hashed")
	}
	finalStatus, err := git.output(
		ctx,
		root,
		"status", "--porcelain=v1", "--untracked-files=all",
	)
	if err != nil {
		return Snapshot{}, err
	}
	if finalStatus != "" {
		return Snapshot{}, errors.New("source worktree changed during verification")
	}
	finalOthers, err := git.outputBytes(ctx, root, "ls-files", "-z", "--others")
	if err != nil {
		return Snapshot{}, err
	}
	if len(finalOthers) != 0 {
		return Snapshot{}, errors.New(
			"source worktree gained untracked or ignored files during verification",
		)
	}
	if digest != expected.TreeSHA256 {
		return Snapshot{}, fmt.Errorf(
			"source tree digest mismatch: got %s, want %s",
			digest,
			expected.TreeSHA256,
		)
	}
	metadataDigest, err := stableGitMetadataDigest(root)
	if err != nil {
		return Snapshot{}, err
	}
	if err := git.verify(); err != nil {
		return Snapshot{}, fmt.Errorf("reverify Git executable: %w", err)
	}
	return Snapshot{
		Root:                root,
		Revision:            revision,
		Base:                expected.Base,
		TreeSHA256:          digest,
		GitMetadataSHA256:   metadataDigest,
		GitExecutable:       git.path,
		GitExecutableSHA256: git.sha256,
	}, nil
}

// TreeDigest computes a framed SHA-256 over sorted tracked paths, modes, and
// exact stable file bytes. The index must exactly match HEAD, raw worktree bytes
// must match indexed blobs, and unsafe index flags or file types are rejected.
func TreeDigest(ctx context.Context, root string) (string, error) {
	git, err := resolveGitRunner()
	if err != nil {
		return "", err
	}
	return treeDigest(ctx, git, root)
}

func treeDigest(ctx context.Context, git gitRunner, root string) (string, error) {
	objectFormat, objectIDLength, err := gitObjectFormat(ctx, git, root)
	if err != nil {
		return "", err
	}
	listing, err := git.outputBytes(ctx, root, "ls-files", "-z", "--stage")
	if err != nil {
		return "", err
	}
	records := bytes.Split(listing, []byte{0})
	entries := make([]trackedEntry, 0, len(records))
	seen := make(map[string]struct{}, len(records))
	for _, record := range records {
		if len(record) == 0 {
			continue
		}
		entry, err := parseTrackedEntry(record, objectIDLength)
		if err != nil {
			return "", err
		}
		if _, exists := seen[entry.path]; exists {
			return "", fmt.Errorf("duplicate tracked path %q", entry.path)
		}
		seen[entry.path] = struct{}{}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(left, right int) bool {
		return entries[left].path < entries[right].path
	})
	if err := rejectUnsafeIndexFlags(ctx, git, root, entries); err != nil {
		return "", err
	}
	headEntries, err := headTreeEntries(ctx, git, root, objectIDLength)
	if err != nil {
		return "", err
	}
	if err := compareIndexToHEAD(entries, headEntries); err != nil {
		return "", err
	}
	directories, err := verifiedWorktreeDirectories(root, entries)
	if err != nil {
		return "", err
	}

	hasher := sha256.New()
	for _, directory := range directories {
		writeFrame(hasher, []byte("directory"))
		writeFrame(hasher, []byte(directory.path))
		writeFrame(hasher, []byte(directory.mode.String()))
	}
	for _, entry := range entries {
		path := filepath.Join(root, filepath.FromSlash(entry.path))
		content, info, err := readStableRegular(path)
		if err != nil {
			return "", fmt.Errorf("hash tracked path %q: %w", entry.path, err)
		}
		if hasMultipleLinks(info) {
			return "", fmt.Errorf(
				"tracked path %q is hard-linked outside the source",
				entry.path,
			)
		}
		executable := info.Mode().Perm()&0o111 != 0
		if executable != (entry.mode == "100755") {
			return "", fmt.Errorf(
				"tracked path %q executable mode differs from the Git index",
				entry.path,
			)
		}
		blobID, err := gitBlobID(objectFormat, content)
		if err != nil {
			return "", err
		}
		if blobID != entry.objectID {
			return "", fmt.Errorf(
				"tracked path %q raw bytes do not match indexed blob %s",
				entry.path,
				entry.objectID,
			)
		}
		writeFrame(hasher, []byte("file"))
		writeFrame(hasher, []byte(entry.mode))
		writeFrame(hasher, []byte(entry.path))
		writeFrame(hasher, content)
	}
	if err := git.verify(); err != nil {
		return "", fmt.Errorf("reverify Git executable: %w", err)
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

type worktreeDirectory struct {
	path string
	mode os.FileMode
}

func verifiedWorktreeDirectories(
	root string,
	entries []trackedEntry,
) ([]worktreeDirectory, error) {
	tracked := make(map[string]struct{}, len(entries))
	allowedDirectories := map[string]struct{}{".": {}}
	for _, entry := range entries {
		tracked[entry.path] = struct{}{}
		for directory := pathpkg.Dir(entry.path); directory != "."; directory = pathpkg.Dir(directory) {
			allowedDirectories[directory] = struct{}{}
		}
	}
	directories := make([]worktreeDirectory, 0, len(allowedDirectories))
	err := filepath.WalkDir(root, func(
		path string,
		entry os.DirEntry,
		walkErr error,
	) error {
		if walkErr != nil {
			return walkErr
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("resolve worktree path: %w", err)
		}
		relative = filepath.ToSlash(relative)
		if relative == ".git" {
			if !entry.IsDir() {
				return errors.New("source .git is not a directory")
			}
			return filepath.SkipDir
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("worktree contains symlink %q", relative)
		}
		if entry.IsDir() {
			if _, allowed := allowedDirectories[relative]; !allowed {
				return fmt.Errorf("worktree contains untracked directory %q", relative)
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			directories = append(directories, worktreeDirectory{
				path: relative,
				mode: info.Mode(),
			})
			return nil
		}
		if !entry.Type().IsRegular() {
			return fmt.Errorf("worktree contains nonregular path %q", relative)
		}
		if _, exists := tracked[relative]; !exists {
			return fmt.Errorf("worktree contains untracked path %q", relative)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if len(directories) != len(allowedDirectories) {
		return nil, errors.New("worktree directory structure does not match tracked paths")
	}
	sort.Slice(directories, func(left, right int) bool {
		return directories[left].path < directories[right].path
	})
	return directories, nil
}

type trackedEntry struct {
	path     string
	mode     string
	objectID string
}

func parseTrackedEntry(record []byte, objectIDLength int) (trackedEntry, error) {
	tab := bytes.IndexByte(record, '\t')
	if tab < 0 {
		return trackedEntry{}, fmt.Errorf("invalid git ls-files record %q", record)
	}
	metadata := strings.Fields(string(record[:tab]))
	if len(metadata) != 3 || metadata[2] != "0" {
		return trackedEntry{}, fmt.Errorf("invalid or conflicted index record %q", record)
	}
	mode := metadata[0]
	if err := validateTrackedMode(mode); err != nil {
		return trackedEntry{}, err
	}
	objectID := metadata[1]
	if err := validateObjectID(objectID, objectIDLength); err != nil {
		return trackedEntry{}, fmt.Errorf("invalid indexed object id: %w", err)
	}
	path := string(record[tab+1:])
	if path == "" || filepath.IsAbs(path) ||
		strings.HasPrefix(filepath.Clean(path), ".."+string(filepath.Separator)) {
		return trackedEntry{}, fmt.Errorf("unsafe tracked path %q", path)
	}
	return trackedEntry{path: path, mode: mode, objectID: objectID}, nil
}

func validateTrackedMode(mode string) error {
	switch mode {
	case "100644", "100755":
		return nil
	case "120000":
		return errors.New("tracked symlinks are not allowed")
	case "160000":
		return errors.New("git submodules are not allowed")
	default:
		return fmt.Errorf("unsupported tracked mode %q", mode)
	}
}

func gitObjectFormat(
	ctx context.Context,
	git gitRunner,
	root string,
) (string, int, error) {
	output, err := git.output(ctx, root, "rev-parse", "--show-object-format")
	if err != nil {
		return "", 0, fmt.Errorf("resolve Git object format: %w", err)
	}
	format := strings.TrimSpace(output)
	switch format {
	case "sha1":
		return format, sha1.Size * 2, nil
	case "sha256":
		return format, sha256.Size * 2, nil
	default:
		return "", 0, fmt.Errorf("unsupported Git object format %q", format)
	}
}

func validateObjectID(objectID string, expectedLength int) error {
	if len(objectID) != expectedLength {
		return fmt.Errorf(
			"object id %q has length %d, want %d",
			objectID,
			len(objectID),
			expectedLength,
		)
	}
	decoded, err := hex.DecodeString(objectID)
	if err != nil || hex.EncodeToString(decoded) != objectID {
		return fmt.Errorf("object id %q is not lowercase hexadecimal", objectID)
	}
	return nil
}

func gitBlobID(objectFormat string, content []byte) (string, error) {
	header := []byte("blob " + strconv.Itoa(len(content)) + "\x00")
	switch objectFormat {
	case "sha1":
		hasher := sha1.New() //nolint:gosec // Git SHA-1 repositories require SHA-1 object IDs.
		_, _ = hasher.Write(header)
		_, _ = hasher.Write(content)
		return hex.EncodeToString(hasher.Sum(nil)), nil
	case "sha256":
		hasher := sha256.New()
		_, _ = hasher.Write(header)
		_, _ = hasher.Write(content)
		return hex.EncodeToString(hasher.Sum(nil)), nil
	default:
		return "", fmt.Errorf("unsupported Git object format %q", objectFormat)
	}
}

func rejectUnsafeIndexFlags(
	ctx context.Context,
	git gitRunner,
	root string,
	entries []trackedEntry,
) error {
	listing, err := git.outputBytes(ctx, root, "ls-files", "-z", "-v")
	if err != nil {
		return err
	}
	paths := make(map[string]struct{}, len(entries))
	for _, record := range bytes.Split(listing, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		if len(record) < 3 || record[1] != ' ' {
			return fmt.Errorf("invalid git ls-files flag record %q", record)
		}
		path := string(record[2:])
		if record[0] != 'H' {
			return fmt.Errorf(
				"tracked path %q uses unsafe Git index flag %q",
				path,
				record[0],
			)
		}
		if _, exists := paths[path]; exists {
			return fmt.Errorf("duplicate flagged index path %q", path)
		}
		paths[path] = struct{}{}
	}
	if len(paths) != len(entries) {
		return errors.New("git index flag listing does not match staged entries")
	}
	for _, entry := range entries {
		if _, exists := paths[entry.path]; !exists {
			return fmt.Errorf(
				"tracked path %q is missing from Git index flag listing",
				entry.path,
			)
		}
	}
	return nil
}

func headTreeEntries(
	ctx context.Context,
	git gitRunner,
	root string,
	objectIDLength int,
) ([]trackedEntry, error) {
	listing, err := git.outputBytes(
		ctx,
		root,
		"ls-tree", "-r", "-z", "--full-tree", "HEAD",
	)
	if err != nil {
		return nil, err
	}
	entries := make([]trackedEntry, 0)
	seen := make(map[string]struct{})
	for _, record := range bytes.Split(listing, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		tab := bytes.IndexByte(record, '\t')
		if tab < 0 {
			return nil, fmt.Errorf("invalid git ls-tree record %q", record)
		}
		metadata := strings.Fields(string(record[:tab]))
		if len(metadata) != 3 {
			return nil, fmt.Errorf("invalid git ls-tree record %q", record)
		}
		mode := metadata[0]
		if err := validateTrackedMode(mode); err != nil {
			return nil, err
		}
		if metadata[1] != "blob" {
			return nil, fmt.Errorf(
				"unsupported HEAD object type %q for mode %s",
				metadata[1],
				mode,
			)
		}
		objectID := metadata[2]
		if err := validateObjectID(objectID, objectIDLength); err != nil {
			return nil, fmt.Errorf("invalid HEAD object id: %w", err)
		}
		path := string(record[tab+1:])
		if path == "" || filepath.IsAbs(path) ||
			strings.HasPrefix(filepath.Clean(path), ".."+string(filepath.Separator)) {
			return nil, fmt.Errorf("unsafe HEAD path %q", path)
		}
		if _, exists := seen[path]; exists {
			return nil, fmt.Errorf("duplicate HEAD path %q", path)
		}
		seen[path] = struct{}{}
		entries = append(entries, trackedEntry{
			path:     path,
			mode:     mode,
			objectID: objectID,
		})
	}
	sort.Slice(entries, func(left, right int) bool {
		return entries[left].path < entries[right].path
	})
	return entries, nil
}

func compareIndexToHEAD(index, head []trackedEntry) error {
	if len(index) != len(head) {
		return fmt.Errorf(
			"git index has %d tracked entries, but HEAD has %d",
			len(index),
			len(head),
		)
	}
	for position := range index {
		if index[position] != head[position] {
			return fmt.Errorf(
				"git index entry %+v does not match HEAD entry %+v",
				index[position],
				head[position],
			)
		}
	}
	return nil
}

func requireResolvedGitPath(
	ctx context.Context,
	git gitRunner,
	root string,
	expected string,
	label string,
	arguments ...string,
) error {
	output, err := git.output(ctx, root, arguments...)
	if err != nil {
		return err
	}
	actual := strings.TrimSpace(output)
	if !filepath.IsAbs(actual) {
		actual = filepath.Join(root, actual)
	}
	actual, err = filepath.EvalSymlinks(filepath.Clean(actual))
	if err != nil {
		return fmt.Errorf("resolve %s: %w", label, err)
	}
	expected, err = filepath.EvalSymlinks(filepath.Clean(expected))
	if err != nil {
		return fmt.Errorf("resolve expected %s: %w", label, err)
	}
	if actual != expected {
		return fmt.Errorf(
			"%s %s is outside standalone source metadata %s",
			label,
			actual,
			expected,
		)
	}
	return nil
}

func requireStandaloneGitDir(ctx context.Context, git gitRunner, root string) error {
	gitDirectory := filepath.Join(root, ".git")
	info, err := os.Lstat(gitDirectory)
	if err != nil {
		return fmt.Errorf("inspect source .git: %w", err)
	}
	if !info.IsDir() {
		return errors.New(
			"source must have a standalone .git directory; linked Git worktrees are not allowed",
		)
	}
	commonDirectoryFile := filepath.Join(gitDirectory, "commondir")
	if _, err := os.Lstat(commonDirectoryFile); err == nil {
		return errors.New(
			"source .git/commondir is not allowed; Git metadata must be self-contained",
		)
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("inspect source .git/commondir: %w", err)
	}
	objectsDirectory := filepath.Join(gitDirectory, "objects")
	if err := requireResolvedGitPath(
		ctx,
		git,
		root,
		gitDirectory,
		"Git directory",
		"rev-parse", "--absolute-git-dir",
	); err != nil {
		return err
	}
	if err := requireResolvedGitPath(
		ctx,
		git,
		root,
		gitDirectory,
		"Git common directory",
		"rev-parse", "--git-common-dir",
	); err != nil {
		return err
	}
	if err := requireResolvedGitPath(
		ctx,
		git,
		root,
		objectsDirectory,
		"Git object directory",
		"rev-parse", "--git-path", "objects",
	); err != nil {
		return err
	}
	if err := filepath.WalkDir(
		gitDirectory,
		func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("git metadata contains symlink %s", path)
			}
			if !entry.IsDir() && !entry.Type().IsRegular() {
				return fmt.Errorf("git metadata contains nonregular path %s", path)
			}
			if !entry.IsDir() {
				entryInfo, err := entry.Info()
				if err != nil {
					return err
				}
				if hasMultipleLinks(entryInfo) {
					return fmt.Errorf("git metadata is hard-linked outside the source: %s", path)
				}
			}
			return nil
		},
	); err != nil {
		return fmt.Errorf("verify local Git metadata: %w", err)
	}
	return nil
}

func rejectGitAlternates(ctx context.Context, git gitRunner, root string) error {
	for _, relative := range []string{
		"objects/info/alternates",
		"objects/info/http-alternates",
	} {
		path, err := git.output(ctx, root, "rev-parse", "--git-path", relative)
		if err != nil {
			return err
		}
		path = strings.TrimSpace(path)
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		content, err := os.ReadFile(filepath.Clean(path))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read Git alternates: %w", err)
		}
		if len(bytes.TrimSpace(content)) != 0 {
			return errors.New("git object alternates are not allowed")
		}
	}
	return nil
}

func rejectLocalOverrides(ctx context.Context, git gitRunner, root string) error {
	for _, relative := range []string{
		"info/attributes",
		"info/grafts",
		"info/sparse-checkout",
		"shallow",
	} {
		path, err := git.output(ctx, root, "rev-parse", "--git-path", relative)
		if err != nil {
			return err
		}
		path = strings.TrimSpace(path)
		if !filepath.IsAbs(path) {
			path = filepath.Join(root, path)
		}
		content, err := os.ReadFile(filepath.Clean(path))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read local Git metadata %s: %w", relative, err)
		}
		if len(bytes.TrimSpace(content)) != 0 {
			return fmt.Errorf("local Git %s overrides are not allowed", relative)
		}
	}
	replacements, err := git.output(ctx, root, "replace", "--list")
	if err != nil {
		return err
	}
	if strings.TrimSpace(replacements) != "" {
		return errors.New("git replacement objects are not allowed")
	}

	config, err := git.output(ctx, root, "config", "--local", "--name-only", "--list")
	if err != nil {
		return err
	}
	allowedConfig := map[string]struct{}{
		"core.bare":                     {},
		"core.filemode":                 {},
		"core.logallrefupdates":         {},
		"core.repositoryformatversion":  {},
		"extensions.compatobjectformat": {},
		"extensions.objectformat":       {},
	}
	for _, key := range strings.Fields(config) {
		lower := strings.ToLower(key)
		if _, allowed := allowedConfig[lower]; !allowed {
			return fmt.Errorf("unsafe local Git configuration %q", key)
		}
	}
	fileMode, err := git.output(ctx, root, "config", "--local", "--get", "core.filemode")
	if err != nil {
		return err
	}
	if strings.TrimSpace(fileMode) != "true" {
		return errors.New("local Git core.filemode must be true")
	}
	return nil
}

type gitMetadataEntry struct {
	info os.FileInfo
	path string
}

func stableGitMetadataDigest(root string) (string, error) {
	return stableGitMetadataDigestWithHook(root, nil)
}

func stableGitMetadataDigestWithHook(
	root string,
	betweenPasses func() error,
) (string, error) {
	first, err := gitMetadataDigest(root)
	if err != nil {
		return "", err
	}
	if betweenPasses != nil {
		if err := betweenPasses(); err != nil {
			return "", fmt.Errorf("run Git metadata stability hook: %w", err)
		}
	}
	second, err := gitMetadataDigest(root)
	if err != nil {
		return "", err
	}
	if second != first {
		return "", errors.New("git metadata changed while it was hashed")
	}
	return first, nil
}

func gitMetadataDigest(root string) (string, error) {
	gitDirectory := filepath.Join(root, ".git")
	hasher := sha256.New()
	entries := make([]gitMetadataEntry, 0)
	err := filepath.WalkDir(
		gitDirectory,
		func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			relative, err := filepath.Rel(gitDirectory, path)
			if err != nil {
				return fmt.Errorf("resolve Git metadata path: %w", err)
			}
			relative = filepath.ToSlash(relative)
			if relative == "" || relative == ".." ||
				strings.HasPrefix(relative, "../") {
				return fmt.Errorf("unsafe Git metadata path %q", relative)
			}
			if relative != "." && isTransientGitMetadata(relative) {
				return fmt.Errorf(
					"transient Git metadata path %q is not allowed",
					relative,
				)
			}
			info, err := os.Lstat(path)
			if err != nil {
				return fmt.Errorf("inspect Git metadata %q: %w", relative, err)
			}
			var kind string
			var content []byte
			switch {
			case info.IsDir():
				kind = "directory"
			case info.Mode().IsRegular():
				kind = "file"
				content, info, err = readStableRegular(path)
				if err != nil {
					return fmt.Errorf(
						"read Git metadata %q: %w",
						relative,
						err,
					)
				}
				if hasMultipleLinks(info) {
					return fmt.Errorf(
						"git metadata %q is hard-linked outside the source",
						relative,
					)
				}
			default:
				return fmt.Errorf(
					"git metadata %q is not a regular file or directory",
					relative,
				)
			}
			writeFrame(hasher, []byte(kind))
			writeFrame(hasher, []byte(relative))
			writeFrame(
				hasher,
				[]byte(strconv.FormatUint(uint64(info.Mode()), 8)),
			)
			writeFrame(hasher, content)
			entries = append(entries, gitMetadataEntry{path: path, info: info})
			return nil
		},
	)
	if err != nil {
		return "", fmt.Errorf("hash Git metadata: %w", err)
	}
	for _, entry := range entries {
		current, err := os.Lstat(entry.path)
		if err != nil {
			return "", fmt.Errorf("reinspect Git metadata: %w", err)
		}
		if !os.SameFile(entry.info, current) ||
			entry.info.Mode() != current.Mode() ||
			entry.info.Size() != current.Size() ||
			!entry.info.ModTime().Equal(current.ModTime()) {
			return "", fmt.Errorf(
				"git metadata path %s changed while it was hashed",
				entry.path,
			)
		}
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func isTransientGitMetadata(relative string) bool {
	base := filepath.Base(filepath.FromSlash(relative))
	if strings.HasSuffix(base, ".lock") || base == "gc.pid" {
		return true
	}
	if strings.HasPrefix(relative, "objects/") &&
		(strings.HasPrefix(base, "tmp_") ||
			strings.HasPrefix(base, "tmp-") ||
			strings.HasPrefix(base, "incoming-")) {
		return true
	}
	if strings.Contains(relative, "/") {
		return false
	}
	switch relative {
	case "AUTO_MERGE", "BISECT_LOG", "BISECT_START", "BISECT_TERMS",
		"CHERRY_PICK_HEAD", "MERGE_HEAD", "MERGE_MODE", "MERGE_MSG",
		"REBASE_HEAD", "REVERT_HEAD", "SQUASH_MSG", "rebase-apply",
		"rebase-merge", "sequencer":
		return true
	default:
		return false
	}
}

func readStableRegular(path string) ([]byte, os.FileInfo, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, nil, errors.New("path is not a regular file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, nil, errors.New("file changed before it was opened")
	}
	content, err := io.ReadAll(file)
	if err != nil {
		return nil, nil, err
	}
	openedAfter, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	after, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if !os.SameFile(before, openedAfter) || !os.SameFile(before, after) ||
		before.Size() != openedAfter.Size() || before.Size() != after.Size() ||
		!before.ModTime().Equal(openedAfter.ModTime()) ||
		!before.ModTime().Equal(after.ModTime()) {
		return nil, nil, errors.New("file changed while it was read")
	}
	return content, openedAfter, nil
}

func writeFrame(writer io.Writer, content []byte) {
	_, _ = io.WriteString(writer, strconv.Itoa(len(content)))
	_, _ = io.WriteString(writer, ":")
	_, _ = writer.Write(content)
}

type gitRunner struct {
	info   os.FileInfo
	path   string
	sha256 string
	mode   os.FileMode
}

func resolveGitRunner() (gitRunner, error) {
	path, err := exec.LookPath("git")
	if err != nil {
		return gitRunner{}, fmt.Errorf("resolve Git executable: %w", err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		return gitRunner{}, fmt.Errorf("resolve absolute Git executable: %w", err)
	}
	return resolveGitRunnerAt(path)
}

func resolvePinnedGitRunner(path, expectedSHA256 string) (gitRunner, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return gitRunner{}, errors.New("expected Git executable must be an absolute canonical path")
	}
	if len(expectedSHA256) != sha256.Size*2 {
		return gitRunner{}, errors.New("expected Git executable SHA-256 is invalid")
	}
	decoded, err := hex.DecodeString(expectedSHA256)
	if err != nil || hex.EncodeToString(decoded) != expectedSHA256 {
		return gitRunner{}, errors.New("expected Git executable SHA-256 is invalid")
	}
	runner, err := resolveGitRunnerAt(path)
	if err != nil {
		return gitRunner{}, err
	}
	if runner.path != path || runner.sha256 != expectedSHA256 {
		return gitRunner{}, fmt.Errorf(
			"git executable identity mismatch: got %s %s, want %s %s",
			runner.path,
			runner.sha256,
			path,
			expectedSHA256,
		)
	}
	return runner, nil
}

func resolveGitRunnerAt(path string) (gitRunner, error) {
	var err error
	path, err = filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return gitRunner{}, fmt.Errorf("resolve canonical Git executable: %w", err)
	}
	content, info, err := readStableRegular(path)
	if err != nil {
		return gitRunner{}, fmt.Errorf("read Git executable: %w", err)
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return gitRunner{}, fmt.Errorf("git executable %s is not executable", path)
	}
	if hasMultipleLinks(info) {
		return gitRunner{}, errors.New("git executable must not be hard-linked")
	}
	digest := sha256.Sum256(content)
	runner := gitRunner{
		path:   path,
		sha256: hex.EncodeToString(digest[:]),
		info:   info,
		mode:   info.Mode(),
	}
	if err := runner.verify(); err != nil {
		return gitRunner{}, fmt.Errorf("pin Git executable: %w", err)
	}
	return runner, nil
}

func (git gitRunner) verify() error {
	canonical, err := filepath.EvalSymlinks(git.path)
	if err != nil {
		return fmt.Errorf("resolve pinned Git executable: %w", err)
	}
	if canonical != git.path {
		return fmt.Errorf(
			"pinned Git executable became a symlink: got %s, want %s",
			canonical,
			git.path,
		)
	}
	content, info, err := readStableRegular(git.path)
	if err != nil {
		return fmt.Errorf("read pinned Git executable: %w", err)
	}
	if !os.SameFile(git.info, info) {
		return errors.New("pinned Git executable was replaced")
	}
	if hasMultipleLinks(info) {
		return errors.New("pinned Git executable became hard-linked")
	}
	if info.Mode() != git.mode {
		return errors.New("pinned Git executable mode changed")
	}
	if runtime.GOOS != "windows" && info.Mode().Perm()&0o111 == 0 {
		return errors.New("pinned Git executable is no longer executable")
	}
	digest := sha256.Sum256(content)
	if hex.EncodeToString(digest[:]) != git.sha256 {
		return errors.New("pinned Git executable content changed")
	}
	return nil
}

func (git gitRunner) output(
	ctx context.Context,
	root string,
	arguments ...string,
) (string, error) {
	output, err := git.outputBytes(ctx, root, arguments...)
	return string(output), err
}

func (git gitRunner) outputBytes(
	ctx context.Context,
	root string,
	arguments ...string,
) ([]byte, error) {
	if err := git.verify(); err != nil {
		return nil, fmt.Errorf("verify Git executable before invocation: %w", err)
	}
	command := exec.CommandContext(
		ctx,
		git.path,
		append([]string{"-C", root}, arguments...)...,
	)
	command.Env = gitEnvironment()
	output, commandErr := command.Output()
	if err := git.verify(); err != nil {
		return nil, fmt.Errorf("verify Git executable after invocation: %w", err)
	}
	if commandErr != nil {
		var exitError *exec.ExitError
		if errors.As(commandErr, &exitError) {
			return nil, fmt.Errorf(
				"git %s: %w: %s",
				strings.Join(arguments, " "),
				commandErr,
				bytes.TrimSpace(exitError.Stderr),
			)
		}
		return nil, fmt.Errorf(
			"git %s: %w",
			strings.Join(arguments, " "),
			commandErr,
		)
	}
	return output, nil
}

func (git gitRunner) run(ctx context.Context, root string, arguments ...string) error {
	if err := git.verify(); err != nil {
		return fmt.Errorf("verify Git executable before invocation: %w", err)
	}
	command := exec.CommandContext(
		ctx,
		git.path,
		append([]string{"-C", root}, arguments...)...,
	)
	command.Env = gitEnvironment()
	output, commandErr := command.CombinedOutput()
	if err := git.verify(); err != nil {
		return fmt.Errorf("verify Git executable after invocation: %w", err)
	}
	if commandErr != nil {
		return fmt.Errorf(
			"git %s: %w: %s",
			strings.Join(arguments, " "),
			commandErr,
			bytes.TrimSpace(output),
		)
	}
	return nil
}

func gitEnvironment() []string {
	environment := []string{
		"GIT_CONFIG_GLOBAL=" + os.DevNull,
		"GIT_CONFIG_SYSTEM=" + os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_TERMINAL_PROMPT=0",
		"LANG=C",
		"LC_ALL=C",
		"TZ=UTC",
	}
	if runtime.GOOS == "windows" {
		if systemRoot := os.Getenv("SystemRoot"); systemRoot != "" {
			environment = append(environment, "SystemRoot="+systemRoot)
		}
	}
	return environment
}
