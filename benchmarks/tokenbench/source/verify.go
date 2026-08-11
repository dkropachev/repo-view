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

const (
	maximumGitCommandOutputBytes = 64 << 20
	maximumGitCommandErrorBytes  = 64 << 10
	maximumTrackedEntries        = 100_000
	maximumWorktreeEntries       = 200_000
	maximumMetadataEntries       = 500_000
	maximumRegularFileBytes      = 64 << 20
	maximumTrackedTreeBytes      = 1 << 30
	maximumGitMetadataBytes      = 2 << 30
	maximumSourcePathBytes       = 4_096
	maximumSourcePathDepth       = 128
	maximumLocalOverrideBytes    = 1 << 20
)

var errGitCommandOutputLimit = errors.New("git command output exceeds the configured limit")

type boundedCommandOutput struct {
	bytes.Buffer
	limit int
}

func (output *boundedCommandOutput) Write(content []byte) (int, error) {
	remaining := output.limit - output.Len()
	if remaining <= 0 {
		return 0, errGitCommandOutputLimit
	}
	if len(content) > remaining {
		written, _ := output.Buffer.Write(content[:remaining])
		return written, errGitCommandOutputLimit
	}
	return output.Buffer.Write(content)
}

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
// checkout. It hashes stable bytes from every tracked regular file and commits
// opaque, uninitialized gitlinks without reading another repository.
func Verify(ctx context.Context, expected Expected) (
	snapshot Snapshot,
	resultErr error,
) {
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
	defer func() {
		closeGitRunner(git, &resultErr)
		if resultErr != nil {
			snapshot = Snapshot{}
		}
	}()
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
	if err := preflightOpaqueGitlinks(ctx, git, root); err != nil {
		return Snapshot{}, err
	}
	status, err := git.output(
		ctx,
		root, "status", "--porcelain=v1", "--untracked-files=all",
		"--ignore-submodules=all",
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
		root, "status", "--porcelain=v1", "--untracked-files=all",
		"--ignore-submodules=all",
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
	if err := preflightOpaqueGitlinks(ctx, git, root); err != nil {
		return Snapshot{}, err
	}
	if digest != expected.TreeSHA256 {
		return Snapshot{}, fmt.Errorf(
			"source tree digest mismatch: got %s, want %s",
			digest,
			expected.TreeSHA256,
		)
	}
	metadataDigest, err := stableGitMetadataDigestContext(ctx, root)
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

// TreeDigest computes a framed SHA-256 over sorted tracked paths and modes,
// exact stable regular-file bytes, and opaque gitlink commit IDs. The index must
// exactly match HEAD, raw worktree bytes must match indexed blobs, and a gitlink
// must be absent or a stable, empty, unmounted real directory.
func TreeDigest(ctx context.Context, root string) (
	digest string,
	resultErr error,
) {
	git, err := resolveGitRunner()
	if err != nil {
		return "", err
	}
	defer func() {
		closeGitRunner(git, &resultErr)
		if resultErr != nil {
			digest = ""
		}
	}()
	return treeDigest(ctx, git, root)
}

func treeDigest(ctx context.Context, git gitRunner, root string) (string, error) {
	objectFormat, entries, err := verifiedTrackedEntries(ctx, git, root)
	if err != nil {
		return "", err
	}
	gitlinks, err := verifiedGitlinks(root, entries)
	if err != nil {
		return "", err
	}
	directories, err := verifiedWorktreeDirectories(ctx, root, entries, gitlinks)
	if err != nil {
		return "", err
	}
	afterGitlinks, err := verifiedGitlinks(root, entries)
	if err != nil {
		return "", err
	}
	if !sameGitlinkMaterializations(gitlinks, afterGitlinks) {
		return "", errors.New("gitlink materialization changed while source was hashed")
	}

	hasher := sha256.New()
	for _, directory := range directories {
		writeFrame(hasher, []byte("directory"))
		writeFrame(hasher, []byte(directory.path))
		writeFrame(hasher, []byte(directory.mode.String()))
	}
	var trackedBytes int64
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		if entry.mode == gitlinkMode {
			writeFrame(hasher, []byte("gitlink"))
			writeFrame(hasher, []byte(entry.mode))
			writeFrame(hasher, []byte(entry.path))
			writeFrame(hasher, []byte(entry.objectID))
			continue
		}
		path := filepath.Join(root, filepath.FromSlash(entry.path))
		content, info, err := readStableRegularContext(
			ctx,
			path,
			maximumRegularFileBytes,
		)
		if err != nil {
			return "", fmt.Errorf("hash tracked path %q: %w", entry.path, err)
		}
		if trackedBytes > maximumTrackedTreeBytes-int64(len(content)) {
			return "", fmt.Errorf(
				"tracked source exceeds %d bytes",
				maximumTrackedTreeBytes,
			)
		}
		trackedBytes += int64(len(content))
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

func verifiedTrackedEntries(
	ctx context.Context,
	git gitRunner,
	root string,
) (string, []trackedEntry, error) {
	objectFormat, objectIDLength, err := gitObjectFormat(ctx, git, root)
	if err != nil {
		return "", nil, err
	}
	listing, err := git.outputBytes(ctx, root, "ls-files", "-z", "--stage")
	if err != nil {
		return "", nil, err
	}
	entries := make([]trackedEntry, 0, 1_024)
	seen := make(map[string]struct{}, 1_024)
	for record := range bytes.SplitSeq(listing, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		if len(entries) >= maximumTrackedEntries {
			return "", nil, fmt.Errorf(
				"source exceeds %d tracked entries",
				maximumTrackedEntries,
			)
		}
		entry, err := parseTrackedEntry(record, objectIDLength)
		if err != nil {
			return "", nil, err
		}
		if _, exists := seen[entry.path]; exists {
			return "", nil, fmt.Errorf("duplicate tracked path %q", entry.path)
		}
		seen[entry.path] = struct{}{}
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(left, right int) bool {
		return entries[left].path < entries[right].path
	})
	if err := rejectUnsafeIndexFlags(ctx, git, root, entries); err != nil {
		return "", nil, err
	}
	headEntries, err := headTreeEntries(ctx, git, root, objectIDLength)
	if err != nil {
		return "", nil, err
	}
	if err := compareIndexToHEAD(entries, headEntries); err != nil {
		return "", nil, err
	}
	return objectFormat, entries, nil
}

type worktreeDirectory struct {
	path string
	mode os.FileMode
}

func verifiedWorktreeDirectories(
	ctx context.Context,
	root string,
	entries []trackedEntry,
	gitlinks map[string]gitlinkMaterialization,
) ([]worktreeDirectory, error) {
	tracked := make(map[string]struct{}, len(entries)-len(gitlinks))
	allowedDirectories := map[string]struct{}{".": {}}
	for _, entry := range entries {
		if entry.mode == gitlinkMode {
			if !gitlinks[entry.path].present {
				continue
			}
		} else {
			tracked[entry.path] = struct{}{}
		}
		for directory := pathpkg.Dir(entry.path); directory != "."; directory = pathpkg.Dir(directory) {
			allowedDirectories[directory] = struct{}{}
			if len(allowedDirectories) > maximumWorktreeEntries {
				return nil, fmt.Errorf(
					"tracked directory structure exceeds %d entries",
					maximumWorktreeEntries,
				)
			}
		}
	}
	directories := make([]worktreeDirectory, 0, len(allowedDirectories))
	seenGitlinks := make(map[string]struct{}, len(gitlinks))
	walked := 0
	err := filepath.WalkDir(root, func(
		path string,
		entry os.DirEntry,
		walkErr error,
	) error {
		if walkErr != nil {
			return walkErr
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		walked++
		if walked > maximumWorktreeEntries {
			return fmt.Errorf(
				"worktree exceeds %d entries",
				maximumWorktreeEntries,
			)
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return fmt.Errorf("resolve worktree path: %w", err)
		}
		relative = filepath.ToSlash(relative)
		if relative != "." {
			if err := validateSourceRelativePath(relative); err != nil {
				return fmt.Errorf("unsafe worktree path %q: %w", relative, err)
			}
		}
		if relative == ".git" {
			if !entry.IsDir() {
				return errors.New("source .git is not a directory")
			}
			return filepath.SkipDir
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("worktree contains symlink %q", relative)
		}
		if gitlink, exists := gitlinks[relative]; exists {
			if !gitlink.present {
				return fmt.Errorf("absent gitlink %q appeared while source was hashed", relative)
			}
			if !entry.IsDir() {
				return fmt.Errorf("gitlink %q is not an uninitialized directory", relative)
			}
			seenGitlinks[relative] = struct{}{}
			return filepath.SkipDir
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
	for path, gitlink := range gitlinks {
		_, seen := seenGitlinks[path]
		if seen != gitlink.present {
			return nil, fmt.Errorf("gitlink %q materialization changed while source was hashed", path)
		}
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

const gitlinkMode = "160000"

func preflightOpaqueGitlinks(ctx context.Context, git gitRunner, root string) error {
	_, entries, err := verifiedTrackedEntries(ctx, git, root)
	if err != nil {
		return err
	}
	before, err := verifiedGitlinks(root, entries)
	if err != nil {
		return err
	}
	after, err := verifiedGitlinks(root, entries)
	if err != nil {
		return err
	}
	if !sameGitlinkMaterializations(before, after) {
		return errors.New("gitlink materialization changed during source preflight")
	}
	return nil
}

func verifiedGitlinks(
	root string,
	entries []trackedEntry,
) (map[string]gitlinkMaterialization, error) {
	gitlinks := make(map[string]gitlinkMaterialization)
	for _, entry := range entries {
		if entry.mode != gitlinkMode {
			continue
		}
		materialization, err := verifyOpaqueGitlink(root, entry.path)
		if err != nil {
			return nil, fmt.Errorf("verify opaque gitlink %q: %w", entry.path, err)
		}
		gitlinks[entry.path] = materialization
	}
	return gitlinks, nil
}

func sameGitlinkMaterializations(
	left, right map[string]gitlinkMaterialization,
) bool {
	if len(left) != len(right) {
		return false
	}
	for path, materialization := range left {
		if right[path] != materialization {
			return false
		}
	}
	return true
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
	if err := validateSourceRelativePath(path); err != nil {
		return trackedEntry{}, fmt.Errorf("unsafe tracked path %q: %w", path, err)
	}
	return trackedEntry{path: path, mode: mode, objectID: objectID}, nil
}

func validateSourceRelativePath(value string) error {
	switch {
	case value == "":
		return errors.New("path is empty")
	case len(value) > maximumSourcePathBytes:
		return fmt.Errorf("path exceeds %d bytes", maximumSourcePathBytes)
	case strings.ContainsRune(value, '\x00'):
		return errors.New("path contains NUL")
	case pathpkg.IsAbs(value):
		return errors.New("path is absolute")
	case pathpkg.Clean(value) != value:
		return errors.New("path is not canonical")
	case value == ".." || strings.HasPrefix(value, "../"):
		return errors.New("path escapes the source root")
	case strings.Count(value, "/") > maximumSourcePathDepth:
		return fmt.Errorf("path exceeds depth %d", maximumSourcePathDepth)
	default:
		return nil
	}
}

func validateTrackedMode(mode string) error {
	switch mode {
	case "100644", "100755":
		return nil
	case "120000":
		return errors.New("tracked symlinks are not allowed")
	case gitlinkMode:
		return nil
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
	count := 0
	for record := range bytes.SplitSeq(listing, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		count++
		if count > maximumTrackedEntries {
			return fmt.Errorf(
				"git index flag listing exceeds %d entries",
				maximumTrackedEntries,
			)
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
	for record := range bytes.SplitSeq(listing, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		if len(entries) >= maximumTrackedEntries {
			return nil, fmt.Errorf(
				"HEAD exceeds %d tracked entries",
				maximumTrackedEntries,
			)
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
		expectedType := "blob"
		if mode == gitlinkMode {
			expectedType = "commit"
		}
		if metadata[1] != expectedType {
			return nil, fmt.Errorf(
				"unsupported HEAD object type %q for mode %s; want %s",
				metadata[1], mode, expectedType,
			)
		}
		objectID := metadata[2]
		if err := validateObjectID(objectID, objectIDLength); err != nil {
			return nil, fmt.Errorf("invalid HEAD object id: %w", err)
		}
		path := string(record[tab+1:])
		if err := validateSourceRelativePath(path); err != nil {
			return nil, fmt.Errorf("unsafe HEAD path %q: %w", path, err)
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
		func() func(string, os.DirEntry, error) error {
			entries := 0
			var totalBytes int64
			return func(path string, entry os.DirEntry, walkErr error) error {
				if walkErr != nil {
					return walkErr
				}
				if err := ctx.Err(); err != nil {
					return err
				}
				entries++
				if entries > maximumMetadataEntries {
					return fmt.Errorf(
						"git metadata exceeds %d entries",
						maximumMetadataEntries,
					)
				}
				relative, err := filepath.Rel(gitDirectory, path)
				if err != nil {
					return fmt.Errorf("resolve Git metadata path: %w", err)
				}
				relative = filepath.ToSlash(relative)
				if relative != "." {
					if err := validateSourceRelativePath(relative); err != nil {
						return fmt.Errorf("unsafe Git metadata path %q: %w", relative, err)
					}
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
					if entryInfo.Size() < 0 || entryInfo.Size() > maximumRegularFileBytes {
						return fmt.Errorf(
							"git metadata file exceeds %d bytes: %s",
							maximumRegularFileBytes,
							path,
						)
					}
					if totalBytes > maximumGitMetadataBytes-entryInfo.Size() {
						return fmt.Errorf(
							"git metadata exceeds %d bytes",
							maximumGitMetadataBytes,
						)
					}
					totalBytes += entryInfo.Size()
					if hasMultipleLinks(entryInfo) {
						return fmt.Errorf("git metadata is hard-linked outside the source: %s", path)
					}
				}
				return nil
			}
		}(),
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
		content, _, err := readStableRegularContext(
			ctx,
			filepath.Clean(path),
			maximumLocalOverrideBytes,
		)
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
		content, _, err := readStableRegularContext(
			ctx,
			filepath.Clean(path),
			maximumLocalOverrideBytes,
		)
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
	return stableGitMetadataDigestContext(context.Background(), root)
}

func stableGitMetadataDigestWithHook(
	root string,
	betweenPasses func() error,
) (string, error) {
	return stableGitMetadataDigestContextWithHook(
		context.Background(),
		root,
		betweenPasses,
	)
}

func stableGitMetadataDigestContext(ctx context.Context, root string) (string, error) {
	return stableGitMetadataDigestContextWithHook(ctx, root, nil)
}

func stableGitMetadataDigestContextWithHook(
	ctx context.Context,
	root string,
	betweenPasses func() error,
) (string, error) {
	first, err := gitMetadataDigestContext(ctx, root)
	if err != nil {
		return "", err
	}
	if betweenPasses != nil {
		if err := betweenPasses(); err != nil {
			return "", fmt.Errorf("run Git metadata stability hook: %w", err)
		}
	}
	second, err := gitMetadataDigestContext(ctx, root)
	if err != nil {
		return "", err
	}
	if second != first {
		return "", errors.New("git metadata changed while it was hashed")
	}
	return first, nil
}

func gitMetadataDigest(root string) (string, error) {
	return gitMetadataDigestContext(context.Background(), root)
}

func gitMetadataDigestContext(ctx context.Context, root string) (string, error) {
	gitDirectory := filepath.Join(root, ".git")
	hasher := sha256.New()
	entries := make([]gitMetadataEntry, 0)
	var totalBytes int64
	err := filepath.WalkDir(
		gitDirectory,
		func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			if len(entries) >= maximumMetadataEntries {
				return fmt.Errorf(
					"git metadata exceeds %d entries",
					maximumMetadataEntries,
				)
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
			if relative != "." {
				if err := validateSourceRelativePath(relative); err != nil {
					return fmt.Errorf("unsafe Git metadata path %q: %w", relative, err)
				}
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
				content, info, err = readStableRegularContext(
					ctx,
					path,
					maximumRegularFileBytes,
				)
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
				if totalBytes > maximumGitMetadataBytes-int64(len(content)) {
					return fmt.Errorf(
						"git metadata exceeds %d bytes",
						maximumGitMetadataBytes,
					)
				}
				totalBytes += int64(len(content))
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
		if err := ctx.Err(); err != nil {
			return "", err
		}
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
	return readStableRegularContext(
		context.Background(),
		path,
		maximumRegularFileBytes,
	)
}

type cancelableReader struct {
	ctx    context.Context
	reader io.Reader
}

func (reader cancelableReader) Read(content []byte) (int, error) {
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

func readStableRegularContext(
	ctx context.Context,
	path string,
	maximumBytes int64,
) (content []byte, info os.FileInfo, resultErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if maximumBytes < 0 {
		return nil, nil, errors.New("regular file byte limit must be nonnegative")
	}
	before, err := os.Lstat(path)
	if err != nil {
		return nil, nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, nil, errors.New("path is not a regular file")
	}
	if before.Size() < 0 || before.Size() > maximumBytes {
		return nil, nil, fmt.Errorf(
			"regular file exceeds %d bytes",
			maximumBytes,
		)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, nil, err
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			resultErr = errors.Join(
				resultErr,
				fmt.Errorf("close regular file: %w", closeErr),
			)
			content = nil
			info = nil
		}
	}()
	opened, err := file.Stat()
	if err != nil {
		return nil, nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, nil, errors.New("file changed before it was opened")
	}
	content, err = io.ReadAll(cancelableReader{
		ctx: ctx,
		reader: io.LimitReader(
			file,
			maximumBytes+1,
		),
	})
	if err != nil {
		return nil, nil, err
	}
	if int64(len(content)) > maximumBytes {
		return nil, nil, fmt.Errorf(
			"regular file exceeds %d bytes",
			maximumBytes,
		)
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
		before.Mode() != openedAfter.Mode() || before.Mode() != after.Mode() ||
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
	info       os.FileInfo
	executable *os.File
	closeFn    func() error
	path       string
	sha256     string
	mode       os.FileMode
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
		identityErr := fmt.Errorf(
			"git executable identity mismatch: got %s %s, want %s %s",
			runner.path,
			runner.sha256,
			path,
			expectedSHA256,
		)
		if closeErr := runner.close(); closeErr != nil {
			identityErr = errors.Join(
				identityErr,
				fmt.Errorf("close mismatched Git executable: %w", closeErr),
			)
		}
		return gitRunner{}, identityErr
	}
	return runner, nil
}

func resolveGitRunnerAt(path string) (result gitRunner, resultErr error) {
	if !pinnedGitExecutionSupported() {
		return gitRunner{}, errors.New("pinned Git executable invocation is supported only on Linux")
	}
	path, err := filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		return gitRunner{}, fmt.Errorf("resolve canonical Git executable: %w", err)
	}
	before, err := os.Lstat(path)
	if err != nil {
		return gitRunner{}, fmt.Errorf("inspect Git executable: %w", err)
	}
	if err := validatePinnedGitExecutableInfo(before); err != nil {
		return gitRunner{}, err
	}
	executable, err := os.Open(path)
	if err != nil {
		return gitRunner{}, fmt.Errorf("open Git executable: %w", err)
	}
	valid := false
	defer func() {
		if !valid {
			if closeErr := executable.Close(); closeErr != nil {
				resultErr = errors.Join(
					resultErr,
					fmt.Errorf("close unaccepted Git executable: %w", closeErr),
				)
			}
		}
	}()
	opened, err := executable.Stat()
	if err != nil {
		return gitRunner{}, fmt.Errorf("inspect opened Git executable: %w", err)
	}
	if err := validateUnchangedPinnedGitExecutable(before, opened); err != nil {
		return gitRunner{}, err
	}
	digest, err := hashPinnedGitExecutable(executable, opened.Size())
	if err != nil {
		return gitRunner{}, fmt.Errorf("hash pinned Git executable: %w", err)
	}
	afterHash, err := executable.Stat()
	if err != nil {
		return gitRunner{}, fmt.Errorf("reinspect pinned Git executable: %w", err)
	}
	if err := validateUnchangedPinnedGitExecutable(opened, afterHash); err != nil {
		return gitRunner{}, err
	}
	pathInfo, err := os.Lstat(path)
	if err != nil {
		return gitRunner{}, fmt.Errorf("reinspect Git executable path: %w", err)
	}
	if err := validateUnchangedPinnedGitExecutable(afterHash, pathInfo); err != nil {
		return gitRunner{}, err
	}
	runner := gitRunner{
		executable: executable,
		path:       path,
		sha256:     digest,
		info:       afterHash,
		mode:       afterHash.Mode(),
		closeFn:    executable.Close,
	}
	if err := runner.verify(); err != nil {
		return gitRunner{}, fmt.Errorf("pin Git executable: %w", err)
	}
	valid = true
	return runner, nil
}

func (git gitRunner) verify() error {
	if git.executable == nil {
		return errors.New("pinned Git executable descriptor is unavailable")
	}
	beforeHash, err := git.executable.Stat()
	if err != nil {
		return fmt.Errorf("inspect pinned Git executable: %w", err)
	}
	if err := validateUnchangedPinnedGitExecutable(git.info, beforeHash); err != nil {
		return err
	}
	digest, err := hashPinnedGitExecutable(git.executable, beforeHash.Size())
	if err != nil {
		return fmt.Errorf("hash pinned Git executable: %w", err)
	}
	afterHash, err := git.executable.Stat()
	if err != nil {
		return fmt.Errorf("reinspect pinned Git executable: %w", err)
	}
	if err := validateUnchangedPinnedGitExecutable(beforeHash, afterHash); err != nil {
		return err
	}
	if digest != git.sha256 {
		return errors.New("pinned Git executable content changed")
	}
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
	pathInfo, err := os.Lstat(git.path)
	if err != nil {
		return fmt.Errorf("inspect pinned Git executable path: %w", err)
	}
	if err := validatePinnedGitExecutableInfo(pathInfo); err != nil {
		return err
	}
	if !os.SameFile(afterHash, pathInfo) {
		return errors.New("pinned Git executable path no longer identifies the opened inode")
	}
	if pathInfo.Mode() != git.mode {
		return errors.New("pinned Git executable mode changed")
	}
	if err := validateUnchangedPinnedGitExecutable(afterHash, pathInfo); err != nil {
		return err
	}
	finalInfo, err := git.executable.Stat()
	if err != nil {
		return fmt.Errorf("finally inspect pinned Git executable: %w", err)
	}
	return validateUnchangedPinnedGitExecutable(pathInfo, finalInfo)
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
	command, err := newPinnedGitCommand(
		ctx,
		git.executable,
		git.path,
		append([]string{"-C", root}, arguments...),
	)
	if err != nil {
		return nil, err
	}
	command.Env = gitEnvironment()
	stdout := &boundedCommandOutput{limit: maximumGitCommandOutputBytes}
	stderr := &boundedCommandOutput{limit: maximumGitCommandErrorBytes}
	command.Stdout = stdout
	command.Stderr = stderr
	commandErr := command.Run()
	if err := git.verify(); err != nil {
		return nil, fmt.Errorf("verify Git executable after invocation: %w", err)
	}
	if commandErr != nil {
		if stderr.Len() != 0 {
			return nil, fmt.Errorf(
				"git %s: %w: %s",
				strings.Join(arguments, " "),
				commandErr,
				bytes.TrimSpace(stderr.Bytes()),
			)
		}
		return nil, fmt.Errorf(
			"git %s: %w",
			strings.Join(arguments, " "),
			commandErr,
		)
	}
	return append([]byte(nil), stdout.Bytes()...), nil
}

func (git gitRunner) run(ctx context.Context, root string, arguments ...string) error {
	if err := git.verify(); err != nil {
		return fmt.Errorf("verify Git executable before invocation: %w", err)
	}
	command, err := newPinnedGitCommand(
		ctx,
		git.executable,
		git.path,
		append([]string{"-C", root}, arguments...),
	)
	if err != nil {
		return err
	}
	command.Env = gitEnvironment()
	stdout := &boundedCommandOutput{limit: maximumGitCommandOutputBytes}
	stderr := &boundedCommandOutput{limit: maximumGitCommandErrorBytes}
	command.Stdout = stdout
	command.Stderr = stderr
	commandErr := command.Run()
	if err := git.verify(); err != nil {
		return fmt.Errorf("verify Git executable after invocation: %w", err)
	}
	if commandErr != nil {
		return fmt.Errorf(
			"git %s: %w: stdout=%s stderr=%s",
			strings.Join(arguments, " "),
			commandErr,
			bytes.TrimSpace(stdout.Bytes()),
			bytes.TrimSpace(stderr.Bytes()),
		)
	}
	return nil
}

func (git gitRunner) close() error {
	if git.closeFn != nil {
		return git.closeFn()
	}
	if git.executable == nil {
		return nil
	}
	return git.executable.Close()
}

func closeGitRunner(git gitRunner, resultErr *error) {
	if closeErr := git.close(); closeErr != nil {
		*resultErr = errors.Join(
			*resultErr,
			fmt.Errorf("close Git executable: %w", closeErr),
		)
	}
}

func validatePinnedGitExecutableInfo(info os.FileInfo) error {
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 ||
		info.Mode()&os.ModeSymlink != 0 {
		return errors.New("git executable is not an executable regular file")
	}
	if !pinnedGitExecutableHasOneLink(info) {
		return errors.New("git executable must have exactly one filesystem link")
	}
	return nil
}

func validateUnchangedPinnedGitExecutable(want, got os.FileInfo) error {
	if err := validatePinnedGitExecutableInfo(got); err != nil {
		return err
	}
	if !os.SameFile(want, got) || want.Mode() != got.Mode() ||
		want.Size() != got.Size() || !want.ModTime().Equal(got.ModTime()) {
		return errors.New("pinned Git executable content changed")
	}
	return nil
}

func hashPinnedGitExecutable(executable *os.File, size int64) (string, error) {
	hash := sha256.New()
	if _, err := io.Copy(hash, io.NewSectionReader(executable, 0, size)); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
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
