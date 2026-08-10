package repoview

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	// ChangedStateSchemaVersion is shared with the tokenbench snapshot
	// producer. Keep the decoder here independent: repo-view must remain a
	// usable library and executable without importing benchmark machinery.
	ChangedStateSchemaVersion = "tokenbench.changed-state-cache/v1"

	maximumChangedStateFileBytes   = int64(512 << 20)
	maximumChangedFiles            = 20_000
	maximumChangedPatchBytes       = 64 << 20
	maximumPerFilePatchBytes       = 16 << 20
	maximumAggregatePatchBytes     = 128 << 20
	maximumChangedSpansPerFile     = 100_000
	maximumChangedLine             = 1_000_000_000
	maximumExpandedChangedLines    = 100_000
	maximumChangedPathBytes        = 4_096
	maximumChangedPathDepth        = 128
	maximumChangedHeadSubjectBytes = 4_096
)

var canonicalObjectID = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)

// ChangedLineSpan is one inclusive, one-based range in the cached HEAD
// worktree.
type ChangedLineSpan struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// ChangedFileState is the complete cached Git state for one destination path.
// PreviousPath is populated only for renames and copies. Patch is kept per
// file so path filtering never has to parse or split an aggregate diff.
//
//nolint:govet,nolintlint // Field order defines canonical changed-state JSON.
type ChangedFileState struct {
	Path         string            `json:"path"`
	PreviousPath string            `json:"previous_path,omitempty"`
	Status       string            `json:"status"`
	Similarity   int               `json:"similarity"`
	Binary       bool              `json:"binary"`
	Lines        []ChangedLineSpan `json:"lines"`
	Patch        string            `json:"patch"`
	PatchSHA256  string            `json:"patch_sha256"`
}

// ChangedStateCache is the canonical, bounded wire format generated before a
// conformant benchmark arm starts. It deliberately contains no executable or
// repository-discovery configuration.
//
//nolint:govet,nolintlint // Field order defines canonical changed-state JSON.
type ChangedStateCache struct {
	SchemaVersion string             `json:"schema_version"`
	BaseCommit    string             `json:"base_commit"`
	HeadCommit    string             `json:"head_commit"`
	HeadSubject   string             `json:"head_subject"`
	ChangedFiles  []ChangedFileState `json:"changed_files"`
	Patch         string             `json:"patch"`
}

// NewWithChangedStateCache constructs a view whose changed and changed-only
// operations are backed exclusively by one authenticated cache. expectedBase
// and expectedHead are independent plan bindings and must exactly match the
// cache. No Git executable is discovered, retained, or invoked in this mode.
func NewWithChangedStateCache(
	root, cachePath, expectedSHA256, expectedBase, expectedHead string,
) (*RepoView, error) {
	view, err := New(root)
	if err != nil {
		return nil, err
	}
	cache, err := loadChangedStateCache(cachePath, expectedSHA256)
	if err != nil {
		return nil, err
	}
	if !canonicalObjectID.MatchString(expectedBase) ||
		!canonicalObjectID.MatchString(expectedHead) {
		return nil, errors.New("changed-state expected revisions are invalid")
	}
	if cache.BaseCommit != expectedBase {
		return nil, fmt.Errorf(
			"changed-state base mismatch: got %s, want %s",
			cache.BaseCommit,
			expectedBase,
		)
	}
	if cache.HeadCommit != expectedHead {
		return nil, fmt.Errorf(
			"changed-state head mismatch: got %s, want %s",
			cache.HeadCommit,
			expectedHead,
		)
	}
	view.changedState = &cache
	return view, nil
}

func loadChangedStateCache(path, expectedSHA256 string) (
	result ChangedStateCache,
	resultErr error,
) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return ChangedStateCache{}, errors.New(
			"changed-state cache path must be absolute and canonical",
		)
	}
	if !validSHA256(expectedSHA256) {
		return ChangedStateCache{}, errors.New("changed-state cache SHA-256 is invalid")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return ChangedStateCache{}, fmt.Errorf("resolve changed-state cache: %w", err)
	}
	if resolved != path {
		return ChangedStateCache{}, errors.New(
			"changed-state cache path must not traverse a symlink",
		)
	}
	before, err := os.Lstat(path)
	if err != nil {
		return ChangedStateCache{}, fmt.Errorf("inspect changed-state cache: %w", err)
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 ||
		hasMultipleLinks(before) {
		return ChangedStateCache{}, errors.New(
			"changed-state cache must be a regular, non-hard-linked file",
		)
	}
	if before.Size() < 0 || before.Size() > maximumChangedStateFileBytes {
		return ChangedStateCache{}, errors.New("changed-state cache exceeds its file limit")
	}
	file, err := openChangedStateCache(path)
	if err != nil {
		return ChangedStateCache{}, fmt.Errorf("open changed-state cache: %w", err)
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			result = ChangedStateCache{}
			resultErr = errors.Join(
				resultErr,
				fmt.Errorf("close changed-state cache: %w", closeErr),
			)
		}
	}()
	opened, err := file.Stat()
	if err != nil || !sameStableCacheFile(before, opened) {
		return ChangedStateCache{}, errors.New(
			"changed-state cache changed while opening",
		)
	}
	raw, err := io.ReadAll(io.LimitReader(file, maximumChangedStateFileBytes+1))
	if err != nil {
		return ChangedStateCache{}, fmt.Errorf("read changed-state cache: %w", err)
	}
	if int64(len(raw)) > maximumChangedStateFileBytes || int64(len(raw)) != before.Size() {
		return ChangedStateCache{}, errors.New("changed-state cache size changed while reading")
	}
	digest := sha256.Sum256(raw)
	actualSHA256 := hex.EncodeToString(digest[:])
	if actualSHA256 != expectedSHA256 {
		return ChangedStateCache{}, fmt.Errorf(
			"changed-state cache digest mismatch: got %s, want %s",
			actualSHA256,
			expectedSHA256,
		)
	}
	openedAfter, err := file.Stat()
	if err != nil {
		return ChangedStateCache{}, fmt.Errorf("reinspect changed-state cache: %w", err)
	}
	after, err := os.Lstat(path)
	if err != nil || !sameStableCacheFile(before, openedAfter) ||
		!sameStableCacheFile(before, after) {
		return ChangedStateCache{}, errors.New(
			"changed-state cache changed while reading",
		)
	}

	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var cache ChangedStateCache
	if err := decoder.Decode(&cache); err != nil {
		return ChangedStateCache{}, fmt.Errorf("decode changed-state cache: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			err = errors.New("multiple JSON values")
		}
		return ChangedStateCache{}, fmt.Errorf("decode changed-state cache trailing data: %w", err)
	}
	if err := cache.Validate(); err != nil {
		return ChangedStateCache{}, fmt.Errorf("validate changed-state cache: %w", err)
	}
	canonical, err := json.Marshal(cache)
	if err != nil {
		return ChangedStateCache{}, fmt.Errorf("encode changed-state cache canonically: %w", err)
	}
	if !bytes.Equal(raw, canonical) {
		return ChangedStateCache{}, errors.New("changed-state cache JSON is not canonical")
	}
	return cache, nil
}

func sameStableCacheFile(left, right os.FileInfo) bool {
	return left != nil && right != nil && os.SameFile(left, right) &&
		left.Mode() == right.Mode() && left.Size() == right.Size() &&
		left.ModTime().Equal(right.ModTime()) &&
		left.Mode().IsRegular() && right.Mode().IsRegular() &&
		left.Mode()&os.ModeSymlink == 0 && right.Mode()&os.ModeSymlink == 0 &&
		!hasMultipleLinks(left) && !hasMultipleLinks(right)
}

// Validate checks schema identity, canonical ordering and metadata, path
// safety, and all cache bounds without consulting Git or the filesystem.
func (cache ChangedStateCache) Validate() error {
	if cache.SchemaVersion != ChangedStateSchemaVersion {
		return fmt.Errorf("unexpected changed-state schema %q", cache.SchemaVersion)
	}
	if !canonicalObjectID.MatchString(cache.BaseCommit) ||
		!canonicalObjectID.MatchString(cache.HeadCommit) {
		return errors.New("changed-state revisions are invalid")
	}
	if len(cache.HeadSubject) > maximumChangedHeadSubjectBytes ||
		!utf8.ValidString(cache.HeadSubject) ||
		strings.ContainsRune(cache.HeadSubject, '\x00') ||
		strings.ContainsAny(cache.HeadSubject, "\r\n") {
		return errors.New("changed-state head subject is invalid")
	}
	if cache.ChangedFiles == nil || len(cache.ChangedFiles) > maximumChangedFiles {
		return errors.New("changed-state file list is nil or oversized")
	}
	previous := ""
	aggregatePatchBytes := 0
	aggregateChangedLines := 0
	for index, file := range cache.ChangedFiles {
		if !validChangedRepositoryPath(file.Path) || index != 0 && previous >= file.Path {
			return errors.New("changed-state files are not canonical and strictly sorted")
		}
		previous = file.Path
		switch file.Status {
		case "added", "deleted", "modified", "type-changed":
			if file.PreviousPath != "" || file.Similarity != 0 {
				return errors.New("changed-state non-rename metadata is noncanonical")
			}
		case "renamed", "copied":
			if !validChangedRepositoryPath(file.PreviousPath) ||
				file.PreviousPath == file.Path ||
				file.Similarity < 1 || file.Similarity > 100 {
				return errors.New("changed-state rename metadata is invalid")
			}
		default:
			return errors.New("changed-state status is invalid")
		}
		if file.Lines == nil || len(file.Lines) > maximumChangedSpansPerFile {
			return errors.New("changed-state line spans are nil or oversized")
		}
		lastEnd := 0
		for _, span := range file.Lines {
			if span.Start <= 0 || span.End < span.Start ||
				span.End > maximumChangedLine ||
				(lastEnd != 0 && span.Start <= lastEnd+1) {
				return errors.New("changed-state line spans are invalid or unmerged")
			}
			width := span.End - span.Start + 1
			if width > maximumExpandedChangedLines-aggregateChangedLines {
				return errors.New("changed-state expanded lines exceed their aggregate limit")
			}
			aggregateChangedLines += width
			lastEnd = span.End
		}
		if len(file.Patch) > maximumPerFilePatchBytes ||
			!utf8.ValidString(file.Patch) || strings.ContainsRune(file.Patch, '\x00') ||
			!validSHA256(file.PatchSHA256) ||
			sha256Hex([]byte(file.Patch)) != file.PatchSHA256 {
			return errors.New("changed-state per-file patch is invalid")
		}
		if aggregatePatchBytes > maximumAggregatePatchBytes-len(file.Patch) {
			return errors.New("changed-state per-file patches exceed their aggregate limit")
		}
		aggregatePatchBytes += len(file.Patch)
	}
	if len(cache.Patch) > maximumChangedPatchBytes || !utf8.ValidString(cache.Patch) ||
		strings.ContainsRune(cache.Patch, '\x00') {
		return errors.New("changed-state patch is invalid or oversized")
	}
	return nil
}

func validChangedRepositoryPath(value string) bool {
	if value == "" || len(value) > maximumChangedPathBytes ||
		!utf8.ValidString(value) || strings.ContainsRune(value, '\x00') ||
		strings.HasPrefix(value, "/") || filepath.IsAbs(filepath.FromSlash(value)) {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
	return clean == value && value != "." && value != ".." &&
		!strings.HasPrefix(value, "../") &&
		strings.Count(value, "/") <= maximumChangedPathDepth &&
		value != ".git" && !strings.HasPrefix(value, ".git/")
}

func sha256Hex(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func (cache ChangedStateCache) file(path string) (ChangedFileState, bool) {
	index := sort.Search(len(cache.ChangedFiles), func(index int) bool {
		return cache.ChangedFiles[index].Path >= path
	})
	if index >= len(cache.ChangedFiles) || cache.ChangedFiles[index].Path != path {
		return ChangedFileState{}, false
	}
	return cache.ChangedFiles[index], true
}
