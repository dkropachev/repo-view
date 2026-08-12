package taskctl

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"
)

const checksumManifestName = "SHA256SUMS"

type checksumLimits struct {
	maxEntries       int
	maxFiles         int
	maxPathBytes     int
	maxDepth         int
	maxFileBytes     int64
	maxTotalBytes    int64
	maxManifestBytes int
}

var defaultChecksumLimits = checksumLimits{
	maxEntries:       200_000,
	maxFiles:         100_000,
	maxPathBytes:     4_096,
	maxDepth:         256,
	maxFileBytes:     2 << 30,
	maxTotalBytes:    64 << 30,
	maxManifestBytes: 256 << 20,
}

type checksumEntry struct {
	info   os.FileInfo
	path   string
	digest [sha256.Size]byte
}

type checksumPathSnapshot struct {
	info      os.FileInfo
	directory bool
	digest    [sha256.Size]byte
}

// checksumTraversalHooks expose descriptor/path race boundaries to package
// tests. Production callers always use an empty value.
type checksumTraversalHooks struct {
	afterRootOpen             func() error
	afterDirectoryOpen        func(pass int, path string) error
	beforeDirectoryRevalidate func(pass int, path string) error
	beforeRootRevalidate      func(pass int) error
}

// BuildSHA256SUMS returns canonical GNU-style SHA256SUMS bytes captured by
// sequentially reading the single-link regular files below root. SHA256SUMS
// itself and explicitly named slash-relative files or directory subtrees are
// excluded. A concurrently writable tree is not an atomic snapshot: the
// result can combine file contents observed at different times. This helper is
// not admitted by taskctl's operational CLI or trusted launcher.
func BuildSHA256SUMS(root string, excludedPaths ...string) ([]byte, error) {
	return buildSHA256SUMS(root, excludedPaths, defaultChecksumLimits)
}

// ValidateSHA256SUMS compares a canonical manifest with sequential reads of
// the regular-file closure below root. Missing, extra, observed-changed, and
// unsupported entries are rejected, but validation is not an atomic claim
// about a concurrently writable tree. This helper is not admitted by
// taskctl's operational CLI or trusted launcher.
func ValidateSHA256SUMS(root string, manifest []byte, excludedPaths ...string) error {
	return validateSHA256SUMS(root, manifest, excludedPaths, defaultChecksumLimits)
}

// BuildPointerSHA256SUMS sequentially captures a canonical SHA256SUMS manifest
// for the explicitly included single-link regular files. Unlike
// BuildSHA256SUMS, it does not walk the root or inspect unrelated entries.
// Every include is a canonical slash-relative file path and may not traverse a
// symlink. The result is not an atomic snapshot of concurrently writable
// files, and this helper is not admitted by taskctl's operational CLI or
// trusted launcher.
func BuildPointerSHA256SUMS(root string, includedPaths ...string) ([]byte, error) {
	return buildPointerSHA256SUMS(root, includedPaths, defaultChecksumLimits)
}

// ValidatePointerSHA256SUMS compares canonical manifest bytes and the declared
// include set with sequential file reads. Files elsewhere below root are
// deliberately outside this sparse manifest's closure. Validation is not an
// atomic claim about concurrently writable files, and this helper is not
// admitted by taskctl's operational CLI or trusted launcher.
func ValidatePointerSHA256SUMS(root string, manifest []byte, includedPaths ...string) error {
	return validatePointerSHA256SUMS(root, manifest, includedPaths, defaultChecksumLimits)
}

func buildSHA256SUMS(root string, excludedPaths []string, limits checksumLimits) ([]byte, error) {
	entries, err := collectChecksumEntries(root, excludedPaths, limits)
	if err != nil {
		return nil, err
	}
	return marshalChecksumEntries(entries, limits)
}

func validateSHA256SUMS(
	root string,
	manifest []byte,
	excludedPaths []string,
	limits checksumLimits,
) error {
	if err := limits.validate(); err != nil {
		return err
	}
	exclusions, err := newChecksumExclusions(excludedPaths, limits)
	if err != nil {
		return err
	}
	listed, err := parseChecksumManifest(manifest, exclusions, limits)
	if err != nil {
		return err
	}
	actual, err := collectChecksumEntriesWithExclusions(root, exclusions, limits)
	if err != nil {
		return err
	}

	for listedIndex, actualIndex := 0, 0; listedIndex < len(listed) || actualIndex < len(actual); {
		switch {
		case listedIndex == len(listed):
			return fmt.Errorf(
				"extra file %q: present in tree but absent from %s",
				actual[actualIndex].path,
				checksumManifestName,
			)
		case actualIndex == len(actual):
			return fmt.Errorf(
				"missing file %q: listed in %s but absent from tree",
				listed[listedIndex].path,
				checksumManifestName,
			)
		case listed[listedIndex].path < actual[actualIndex].path:
			return fmt.Errorf(
				"missing file %q: listed in %s but absent from tree",
				listed[listedIndex].path,
				checksumManifestName,
			)
		case actual[actualIndex].path < listed[listedIndex].path:
			return fmt.Errorf(
				"extra file %q: present in tree but absent from %s",
				actual[actualIndex].path,
				checksumManifestName,
			)
		default:
			if !bytes.Equal(
				listed[listedIndex].digest[:],
				actual[actualIndex].digest[:],
			) {
				return fmt.Errorf("changed file %q: checksum mismatch", listed[listedIndex].path)
			}
			listedIndex++
			actualIndex++
		}
	}
	return nil
}

func buildPointerSHA256SUMS(
	root string,
	includedPaths []string,
	limits checksumLimits,
) ([]byte, error) {
	paths, err := canonicalPointerChecksumPaths(includedPaths, limits)
	if err != nil {
		return nil, err
	}
	entries, err := collectPointerChecksumEntries(root, paths, limits)
	if err != nil {
		return nil, err
	}
	return marshalChecksumEntries(entries, limits)
}

func validatePointerSHA256SUMS(
	root string,
	manifest []byte,
	includedPaths []string,
	limits checksumLimits,
) error {
	paths, err := canonicalPointerChecksumPaths(includedPaths, limits)
	if err != nil {
		return err
	}
	exclusions, err := newChecksumExclusions(nil, limits)
	if err != nil {
		return err
	}
	listed, err := parseChecksumManifest(manifest, exclusions, limits)
	if err != nil {
		return err
	}
	if err := validatePointerManifestPaths(listed, paths); err != nil {
		return err
	}
	actual, err := collectPointerChecksumEntries(root, paths, limits)
	if err != nil {
		return err
	}
	for index := range listed {
		if !bytes.Equal(listed[index].digest[:], actual[index].digest[:]) {
			return fmt.Errorf("changed file %q: checksum mismatch", listed[index].path)
		}
	}
	return nil
}

func canonicalPointerChecksumPaths(paths []string, limits checksumLimits) ([]string, error) {
	if err := limits.validate(); err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, errors.New("pointer checksum include set is empty")
	}
	if len(paths) > limits.maxFiles {
		return nil, fmt.Errorf("pointer checksum include set exceeds %d files", limits.maxFiles)
	}
	canonical := make([]string, 0, len(paths))
	seen := make(map[string]struct{}, len(paths))
	manifestBytes := 0
	for _, path := range paths {
		if err := validateChecksumPath(path, limits); err != nil {
			return nil, fmt.Errorf("invalid pointer checksum include %q: %w", path, err)
		}
		if path == checksumManifestName {
			return nil, fmt.Errorf("pointer checksum include may not name %s", checksumManifestName)
		}
		if _, exists := seen[path]; exists {
			return nil, fmt.Errorf("duplicate pointer checksum include %q", path)
		}
		lineBytes := sha256.Size*2 + 2 + len(path) + 1
		if lineBytes > limits.maxManifestBytes-manifestBytes {
			return nil, fmt.Errorf("%s exceeds %d bytes", checksumManifestName, limits.maxManifestBytes)
		}
		manifestBytes += lineBytes
		seen[path] = struct{}{}
		canonical = append(canonical, path)
	}
	sort.Strings(canonical)
	return canonical, nil
}

func validatePointerManifestPaths(listed []checksumEntry, included []string) error {
	for listedIndex, includedIndex := 0, 0; listedIndex < len(listed) || includedIndex < len(included); {
		switch {
		case listedIndex == len(listed):
			return fmt.Errorf("missing included path %q from %s", included[includedIndex], checksumManifestName)
		case includedIndex == len(included):
			return fmt.Errorf("extra path %q in %s is not explicitly included", listed[listedIndex].path, checksumManifestName)
		case listed[listedIndex].path < included[includedIndex]:
			return fmt.Errorf("extra path %q in %s is not explicitly included", listed[listedIndex].path, checksumManifestName)
		case included[includedIndex] < listed[listedIndex].path:
			return fmt.Errorf("missing included path %q from %s", included[includedIndex], checksumManifestName)
		default:
			listedIndex++
			includedIndex++
		}
	}
	return nil
}

func collectPointerChecksumEntries(
	root string,
	paths []string,
	limits checksumLimits,
) ([]checksumEntry, error) {
	rootHandle, err := openPointerChecksumRoot(root)
	if err != nil {
		return nil, err
	}
	rootInfo, err := rootHandle.Stat(".")
	if err != nil {
		return nil, errors.Join(
			fmt.Errorf("snapshot checksum root: %w", err),
			rootHandle.Close(),
		)
	}
	entries := make([]checksumEntry, 0, len(paths))
	var totalBytes int64
	for _, path := range paths {
		var info os.FileInfo
		digest, size, digestErr := digestPointerChecksumFile(
			rootHandle,
			path,
			limits.maxFileBytes,
			limits.maxTotalBytes,
			limits.maxTotalBytes-totalBytes,
			&info,
		)
		if digestErr != nil {
			err = fmt.Errorf("checksum explicitly included file %q: %w", path, digestErr)
			break
		}
		if size > limits.maxTotalBytes-totalBytes {
			err = fmt.Errorf("pointer checksum files exceed %d bytes", limits.maxTotalBytes)
			break
		}
		totalBytes += size
		entries = append(entries, checksumEntry{path: path, digest: digest, info: info})
	}
	if err == nil {
		err = validatePointerChecksumLinkCounts(entries)
	}
	if err == nil {
		err = revalidatePointerChecksumSnapshot(rootHandle, rootInfo, entries, limits)
	}
	if closeErr := rootHandle.Close(); closeErr != nil {
		err = errors.Join(err, fmt.Errorf("close checksum root: %w", closeErr))
	}
	if err != nil {
		return nil, err
	}
	return entries, nil
}

func openPointerChecksumRoot(root string) (*os.Root, error) {
	if root == "" {
		return nil, errors.New("checksum root is empty")
	}
	absolute, err := filepath.Abs(root)
	if err != nil {
		return nil, fmt.Errorf("make checksum root absolute: %w", err)
	}
	if isFilesystemRoot(absolute) {
		return nil, errors.New("checksum root must not be a filesystem root")
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return nil, fmt.Errorf("resolve checksum root: %w", err)
	}
	if filepath.Clean(absolute) != filepath.Clean(resolved) {
		return nil, errors.New("checksum root path must not traverse symlinks")
	}
	before, err := os.Lstat(absolute)
	if err != nil {
		return nil, fmt.Errorf("inspect checksum root: %w", err)
	}
	if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
		return nil, errors.New("checksum root is not a non-symlink directory")
	}
	handle, err := os.OpenRoot(absolute)
	if err != nil {
		return nil, fmt.Errorf("open checksum root: %w", err)
	}
	opened, statErr := handle.Stat(".")
	pathAfter, pathErr := os.Lstat(absolute)
	if joined := errors.Join(statErr, pathErr); joined != nil {
		return nil, errors.Join(joined, handle.Close())
	}
	if !opened.IsDir() || !pathAfter.IsDir() ||
		!os.SameFile(before, opened) || !os.SameFile(opened, pathAfter) {
		return nil, errors.Join(errors.New("checksum root changed while opening"), handle.Close())
	}
	manifestInfo, manifestErr := handle.Lstat(checksumManifestName)
	if manifestErr == nil && (!manifestInfo.Mode().IsRegular() || manifestInfo.Mode()&os.ModeSymlink != 0) {
		return nil, errors.Join(
			fmt.Errorf("%s output is not a regular non-symlink file", checksumManifestName),
			handle.Close(),
		)
	}
	if manifestErr != nil && !errors.Is(manifestErr, os.ErrNotExist) {
		return nil, errors.Join(manifestErr, handle.Close())
	}
	return handle, nil
}

type pointerChecksumDirectory struct {
	info   os.FileInfo
	parent *os.Root
	child  *os.Root
	name   string
}

func digestPointerChecksumFile(
	root *os.Root,
	path string,
	maxFileBytes int64,
	maxTotalBytes int64,
	remainingTotalBytes int64,
	snapshot *os.FileInfo,
) ([sha256.Size]byte, int64, error) {
	var digest [sha256.Size]byte
	if snapshot == nil {
		return digest, 0, errors.New("pointer checksum snapshot is required")
	}
	*snapshot = nil
	components := strings.Split(path, "/")
	current := root
	directories := make([]pointerChecksumDirectory, 0, len(components)-1)
	closeDirectories := func() error {
		var err error
		for index := range directories {
			index = len(directories) - 1 - index
			err = errors.Join(err, directories[index].child.Close())
		}
		return err
	}

	for _, component := range components[:len(components)-1] {
		before, err := current.Lstat(component)
		if err != nil {
			return digest, 0, errors.Join(err, closeDirectories())
		}
		if before.Mode()&os.ModeSymlink != 0 {
			return digest, 0, errors.Join(
				fmt.Errorf("path traverses symlink directory %q", component),
				closeDirectories(),
			)
		}
		if !before.IsDir() {
			return digest, 0, errors.Join(
				fmt.Errorf("path component %q is not a directory", component),
				closeDirectories(),
			)
		}
		child, err := current.OpenRoot(component)
		if err != nil {
			return digest, 0, errors.Join(err, closeDirectories())
		}
		opened, statErr := child.Stat(".")
		pathAfter, pathErr := current.Lstat(component)
		if joined := errors.Join(statErr, pathErr); joined != nil {
			return digest, 0, errors.Join(joined, child.Close(), closeDirectories())
		}
		if pathAfter.Mode()&os.ModeSymlink != 0 || !opened.IsDir() ||
			!os.SameFile(before, opened) || !os.SameFile(opened, pathAfter) {
			return digest, 0, errors.Join(
				fmt.Errorf("path directory %q changed while opening", component),
				child.Close(),
				closeDirectories(),
			)
		}
		directories = append(directories, pointerChecksumDirectory{
			parent: current,
			child:  child,
			name:   component,
			info:   opened,
		})
		current = child
	}

	name := components[len(components)-1]
	before, err := current.Lstat(name)
	if err != nil {
		return digest, 0, errors.Join(err, closeDirectories())
	}
	if before.Mode()&os.ModeSymlink != 0 {
		return digest, 0, errors.Join(errors.New("included path is a symlink"), closeDirectories())
	}
	if !before.Mode().IsRegular() {
		return digest, 0, errors.Join(errors.New("included path is not a regular file"), closeDirectories())
	}
	if !sourceAuditFileHasOneLink(before) {
		return digest, 0, errors.Join(
			errors.New("included file must have exactly one hard link"),
			closeDirectories(),
		)
	}
	if before.Size() > maxFileBytes {
		return digest, 0, errors.Join(
			fmt.Errorf("file exceeds %d bytes", maxFileBytes),
			closeDirectories(),
		)
	}
	if before.Size() > remainingTotalBytes {
		return digest, 0, errors.Join(
			fmt.Errorf("pointer checksum files exceed %d bytes", maxTotalBytes),
			closeDirectories(),
		)
	}
	file, err := current.Open(name)
	if err != nil {
		return digest, 0, errors.Join(err, closeDirectories())
	}
	opened, err := file.Stat()
	if err != nil {
		return digest, 0, errors.Join(err, file.Close(), closeDirectories())
	}
	if !opened.Mode().IsRegular() || !os.SameFile(before, opened) ||
		!stableChecksumFileInfo(before, opened) {
		return digest, 0, errors.Join(
			errors.New("file changed while opening"),
			file.Close(),
			closeDirectories(),
		)
	}

	readLimit := min(maxFileBytes, remainingTotalBytes)
	hasher := sha256.New()
	readBytes, readErr := io.Copy(hasher, io.LimitReader(file, readLimit+1))
	after, statErr := file.Stat()
	pathAfter, pathStatErr := current.Lstat(name)
	closeErr := file.Close()
	if err := errors.Join(readErr, statErr, pathStatErr, closeErr); err != nil {
		return digest, readBytes, errors.Join(err, closeDirectories())
	}
	if readBytes > maxFileBytes {
		return digest, readBytes, errors.Join(
			fmt.Errorf("file exceeds %d bytes", maxFileBytes),
			closeDirectories(),
		)
	}
	if readBytes > remainingTotalBytes {
		return digest, readBytes, errors.Join(
			fmt.Errorf("pointer checksum files exceed %d bytes", maxTotalBytes),
			closeDirectories(),
		)
	}
	if pathAfter.Mode()&os.ModeSymlink != 0 || !pathAfter.Mode().IsRegular() ||
		!os.SameFile(opened, after) || !os.SameFile(after, pathAfter) ||
		!stableChecksumFileInfo(opened, after) ||
		!stableChecksumFileInfo(after, pathAfter) ||
		readBytes != after.Size() {
		return digest, readBytes, errors.Join(
			errors.New("file changed while checksumming"),
			closeDirectories(),
		)
	}
	for _, directory := range directories {
		parentAfter, parentErr := directory.parent.Lstat(directory.name)
		childAfter, childErr := directory.child.Stat(".")
		if err := errors.Join(parentErr, childErr); err != nil {
			return digest, readBytes, errors.Join(err, closeDirectories())
		}
		if parentAfter.Mode()&os.ModeSymlink != 0 || !parentAfter.IsDir() ||
			!os.SameFile(directory.info, parentAfter) ||
			!os.SameFile(parentAfter, childAfter) {
			return digest, readBytes, errors.Join(
				errors.New("included path directory changed while checksumming"),
				closeDirectories(),
			)
		}
	}
	if err := closeDirectories(); err != nil {
		return digest, readBytes, err
	}
	copy(digest[:], hasher.Sum(nil))
	*snapshot = pathAfter
	return digest, readBytes, nil
}

func validatePointerChecksumLinkCounts(entries []checksumEntry) error {
	for _, entry := range entries {
		if entry.info == nil || !sourceAuditFileHasOneLink(entry.info) {
			return fmt.Errorf(
				"pointer checksum file %q must have exactly one hard link",
				entry.path,
			)
		}
	}
	return nil
}

func revalidatePointerChecksumSnapshot(
	root *os.Root,
	rootInfo os.FileInfo,
	entries []checksumEntry,
	limits checksumLimits,
) error {
	if root == nil || rootInfo == nil {
		return errors.New("pointer checksum root snapshot is incomplete")
	}
	openedRoot, err := root.Stat(".")
	if err != nil {
		return fmt.Errorf("revalidate pointer checksum root: %w", err)
	}
	pathRoot, err := os.Lstat(root.Name())
	if err != nil {
		return fmt.Errorf("revalidate pointer checksum root path: %w", err)
	}
	if !openedRoot.IsDir() || !pathRoot.IsDir() ||
		openedRoot.Mode()&os.ModeSymlink != 0 || pathRoot.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(rootInfo, openedRoot) || !os.SameFile(openedRoot, pathRoot) ||
		!stableFileInfo(rootInfo, openedRoot) || !stableFileInfo(openedRoot, pathRoot) {
		return errors.New("pointer checksum root changed while checksumming")
	}

	var totalBytes int64
	for _, entry := range entries {
		var info os.FileInfo
		digest, size, err := digestPointerChecksumFile(
			root,
			entry.path,
			limits.maxFileBytes,
			limits.maxTotalBytes,
			limits.maxTotalBytes-totalBytes,
			&info,
		)
		if err != nil {
			return fmt.Errorf("revalidate pointer checksum file %q: %w", entry.path, err)
		}
		if size > limits.maxTotalBytes-totalBytes {
			return fmt.Errorf("pointer checksum files exceed %d bytes", limits.maxTotalBytes)
		}
		totalBytes += size
		if entry.info == nil || !os.SameFile(entry.info, info) ||
			!stableChecksumFileInfo(entry.info, info) ||
			!sourceAuditFileHasOneLink(info) || digest != entry.digest {
			return fmt.Errorf("pointer checksum file %q changed after checksumming", entry.path)
		}
	}

	openedRootAfter, statErr := root.Stat(".")
	pathRootAfter, pathErr := os.Lstat(root.Name())
	if err := errors.Join(statErr, pathErr); err != nil {
		return fmt.Errorf("revalidate final pointer checksum root: %w", err)
	}
	if !openedRootAfter.IsDir() || !pathRootAfter.IsDir() ||
		openedRootAfter.Mode()&os.ModeSymlink != 0 || pathRootAfter.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(rootInfo, openedRootAfter) ||
		!os.SameFile(openedRootAfter, pathRootAfter) ||
		!stableFileInfo(rootInfo, openedRootAfter) ||
		!stableFileInfo(openedRootAfter, pathRootAfter) {
		return errors.New("pointer checksum root changed after checksumming")
	}
	return nil
}

func collectChecksumEntries(
	root string,
	excludedPaths []string,
	limits checksumLimits,
) ([]checksumEntry, error) {
	if err := limits.validate(); err != nil {
		return nil, err
	}
	exclusions, err := newChecksumExclusions(excludedPaths, limits)
	if err != nil {
		return nil, err
	}
	return collectChecksumEntriesWithExclusions(root, exclusions, limits)
}

func collectChecksumEntriesWithExclusions(
	root string,
	exclusions checksumExclusions,
	limits checksumLimits,
) ([]checksumEntry, error) {
	return collectChecksumEntriesWithExclusionsAndHooks(
		root,
		exclusions,
		limits,
		checksumTraversalHooks{},
	)
}

func collectChecksumEntriesWithExclusionsAndHooks(
	root string,
	exclusions checksumExclusions,
	limits checksumLimits,
	hooks checksumTraversalHooks,
) (result []checksumEntry, resultErr error) {
	if err := limits.validate(); err != nil {
		return nil, err
	}
	rootHandle, err := openPointerChecksumRoot(root)
	if err != nil {
		return nil, err
	}
	defer func() {
		resultErr = errors.Join(resultErr, rootHandle.Close())
	}()
	rootInfo, err := rootHandle.Stat(".")
	if err != nil {
		return nil, fmt.Errorf("snapshot checksum root: %w", err)
	}
	if hooks.afterRootOpen != nil {
		if err := hooks.afterRootOpen(); err != nil {
			return nil, fmt.Errorf("checksum traversal test hook after root open: %w", err)
		}
	}

	firstEntries, firstSnapshots, err := collectPinnedChecksumSnapshot(
		rootHandle,
		rootInfo,
		exclusions,
		limits,
		hooks,
		1,
	)
	if err != nil {
		return nil, fmt.Errorf("walk pinned checksum tree: %w", err)
	}
	if hooks.beforeRootRevalidate != nil {
		if err := hooks.beforeRootRevalidate(1); err != nil {
			return nil, fmt.Errorf("checksum traversal test hook before root revalidation: %w", err)
		}
	}
	if err := revalidatePinnedChecksumRoot(rootHandle, rootInfo); err != nil {
		return nil, err
	}

	_, secondSnapshots, err := collectPinnedChecksumSnapshot(
		rootHandle,
		rootInfo,
		exclusions,
		limits,
		hooks,
		2,
	)
	if err != nil {
		return nil, fmt.Errorf("revalidate pinned checksum tree: %w", err)
	}
	if err := compareChecksumSnapshots(firstSnapshots, secondSnapshots); err != nil {
		return nil, err
	}
	if hooks.beforeRootRevalidate != nil {
		if err := hooks.beforeRootRevalidate(2); err != nil {
			return nil, fmt.Errorf("checksum traversal test hook before final root revalidation: %w", err)
		}
	}
	if err := revalidatePinnedChecksumRoot(rootHandle, rootInfo); err != nil {
		return nil, err
	}
	return firstEntries, nil
}

type checksumSnapshotCollector struct {
	hooks      checksumTraversalHooks
	snapshots  map[string]checksumPathSnapshot
	exclusions checksumExclusions
	entries    []checksumEntry
	limits     checksumLimits
	entryCount int
	fileCount  int
	totalBytes int64
	pass       int
}

func collectPinnedChecksumSnapshot(
	root *os.Root,
	rootInfo os.FileInfo,
	exclusions checksumExclusions,
	limits checksumLimits,
	hooks checksumTraversalHooks,
	pass int,
) ([]checksumEntry, map[string]checksumPathSnapshot, error) {
	if root == nil || rootInfo == nil || !rootInfo.IsDir() {
		return nil, nil, errors.New("checksum root snapshot is incomplete")
	}
	collector := checksumSnapshotCollector{
		entries:    make([]checksumEntry, 0),
		snapshots:  map[string]checksumPathSnapshot{"": {info: rootInfo, directory: true}},
		exclusions: exclusions,
		limits:     limits,
		hooks:      hooks,
		pass:       pass,
	}
	if err := collector.walkDirectory(root, "", rootInfo); err != nil {
		return nil, nil, err
	}
	// Go string comparison is bytewise. Every retained path is canonical UTF-8.
	sort.Slice(collector.entries, func(left, right int) bool {
		return collector.entries[left].path < collector.entries[right].path
	})
	return collector.entries, collector.snapshots, nil
}

func (collector *checksumSnapshotCollector) walkDirectory(
	directory *os.Root,
	prefix string,
	expected os.FileInfo,
) error {
	entries, err := readPinnedChecksumDirectory(
		directory,
		expected,
		collector.limits.maxEntries-collector.entryCount,
		collector.limits.maxEntries,
	)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		collector.entryCount++
		if collector.entryCount > collector.limits.maxEntries {
			return fmt.Errorf(
				"checksum tree exceeds %d filesystem entries",
				collector.limits.maxEntries,
			)
		}
		relative := entry.Name()
		if prefix != "" {
			relative = prefix + "/" + relative
		}
		if err := validateChecksumPath(relative, collector.limits); err != nil {
			return fmt.Errorf("unsafe checksum path %q: %w", relative, err)
		}
		name := entry.Name()
		if relative == checksumManifestName {
			info, err := directory.Lstat(name)
			if err != nil {
				return fmt.Errorf("inspect checksum output: %w", err)
			}
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("%s output is not a regular non-symlink file", checksumManifestName)
			}
			continue
		}
		if collector.exclusions.contains(relative) {
			continue
		}

		before, err := directory.Lstat(name)
		if err != nil {
			return fmt.Errorf("inspect checksum path %q: %w", relative, err)
		}
		if before.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink is not admitted in checksum closure: %s", relative)
		}
		if before.IsDir() {
			if err := collector.walkChildDirectory(directory, name, relative, before); err != nil {
				return err
			}
			continue
		}
		if !before.Mode().IsRegular() {
			return fmt.Errorf("non-regular file is not admitted in checksum closure: %s", relative)
		}
		collector.fileCount++
		if collector.fileCount > collector.limits.maxFiles {
			return fmt.Errorf("checksum tree exceeds %d regular files", collector.limits.maxFiles)
		}
		if before.Size() > collector.limits.maxFileBytes {
			return fmt.Errorf(
				"checksum file %q exceeds %d bytes",
				relative,
				collector.limits.maxFileBytes,
			)
		}
		if before.Size() > collector.limits.maxTotalBytes-collector.totalBytes {
			return fmt.Errorf("checksum tree exceeds %d file bytes", collector.limits.maxTotalBytes)
		}
		if !sourceAuditFileHasOneLink(before) {
			return fmt.Errorf("checksum file %q must have exactly one hard link", relative)
		}
		digest, size, info, err := digestChecksumFileAt(
			directory,
			name,
			before,
			collector.limits.maxFileBytes,
		)
		if err != nil {
			return fmt.Errorf("checksum %q: %w", relative, err)
		}
		if size > collector.limits.maxTotalBytes-collector.totalBytes {
			return fmt.Errorf("checksum tree exceeds %d file bytes", collector.limits.maxTotalBytes)
		}
		collector.totalBytes += size
		collector.entries = append(
			collector.entries,
			checksumEntry{path: relative, digest: digest, info: info},
		)
		collector.snapshots[relative] = checksumPathSnapshot{info: info, digest: digest}
	}
	return nil
}

func (collector *checksumSnapshotCollector) walkChildDirectory(
	parent *os.Root,
	name, relative string,
	before os.FileInfo,
) (resultErr error) {
	child, err := parent.OpenRoot(name)
	if err != nil {
		return fmt.Errorf("open checksum directory %q: %w", relative, err)
	}
	defer func() { resultErr = errors.Join(resultErr, child.Close()) }()
	opened, statErr := child.Stat(".")
	pathAfter, pathErr := parent.Lstat(name)
	if err := errors.Join(statErr, pathErr); err != nil {
		return fmt.Errorf("pin checksum directory %q: %w", relative, err)
	}
	if pathAfter.Mode()&os.ModeSymlink != 0 || !opened.IsDir() || !pathAfter.IsDir() ||
		!os.SameFile(before, opened) || !os.SameFile(opened, pathAfter) ||
		!stableFileInfo(before, opened) || !stableFileInfo(opened, pathAfter) {
		return fmt.Errorf("checksum directory %q changed while opening", relative)
	}
	if collector.hooks.afterDirectoryOpen != nil {
		if err := collector.hooks.afterDirectoryOpen(collector.pass, relative); err != nil {
			return fmt.Errorf("checksum traversal test hook after opening %q: %w", relative, err)
		}
	}
	collector.snapshots[relative] = checksumPathSnapshot{info: opened, directory: true}
	if err := collector.walkDirectory(child, relative, opened); err != nil {
		return err
	}
	if collector.hooks.beforeDirectoryRevalidate != nil {
		if err := collector.hooks.beforeDirectoryRevalidate(collector.pass, relative); err != nil {
			return fmt.Errorf("checksum traversal test hook before revalidating %q: %w", relative, err)
		}
	}
	openedAfter, statErr := child.Stat(".")
	pathAfter, pathErr = parent.Lstat(name)
	if err := errors.Join(statErr, pathErr); err != nil {
		return fmt.Errorf("revalidate checksum directory %q: %w", relative, err)
	}
	if pathAfter.Mode()&os.ModeSymlink != 0 || !openedAfter.IsDir() || !pathAfter.IsDir() ||
		!os.SameFile(opened, openedAfter) || !os.SameFile(openedAfter, pathAfter) ||
		!stableFileInfo(opened, openedAfter) || !stableFileInfo(openedAfter, pathAfter) {
		return fmt.Errorf("checksum directory %q changed while checksumming", relative)
	}
	return nil
}

func readPinnedChecksumDirectory(
	root *os.Root,
	expected os.FileInfo,
	remainingEntries int,
	maximumEntries int,
) (result []os.DirEntry, resultErr error) {
	if root == nil || expected == nil || remainingEntries < 0 || maximumEntries <= 0 {
		return nil, errors.New("checksum directory snapshot is incomplete")
	}
	directory, err := root.Open(".")
	if err != nil {
		return nil, fmt.Errorf("open pinned checksum directory: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, directory.Close()) }()
	opened, err := directory.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.IsDir() || !os.SameFile(expected, opened) ||
		!stableFileInfo(expected, opened) {
		return nil, errors.New("checksum directory changed before enumeration")
	}
	for {
		request := min(4_096, remainingEntries-len(result)+1)
		batch, readErr := directory.ReadDir(request)
		result = append(result, batch...)
		if len(result) > remainingEntries {
			return nil, fmt.Errorf(
				"checksum tree exceeds %d filesystem entries",
				maximumEntries,
			)
		}
		if errors.Is(readErr, io.EOF) {
			break
		}
		if readErr != nil {
			return nil, readErr
		}
	}
	after, err := directory.Stat()
	if err != nil {
		return nil, err
	}
	if !after.IsDir() || !os.SameFile(opened, after) || !stableFileInfo(opened, after) {
		return nil, errors.New("checksum directory changed during enumeration")
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].Name() < result[right].Name()
	})
	return result, nil
}

func compareChecksumSnapshots(
	want, got map[string]checksumPathSnapshot,
) error {
	for path, wanted := range want {
		current, found := got[path]
		if !found {
			return fmt.Errorf("checksum closure changed after checksumming: missing path %q", path)
		}
		if wanted.info == nil || current.info == nil ||
			wanted.directory != current.directory ||
			!os.SameFile(wanted.info, current.info) ||
			!stableChecksumPathInfo(wanted, current.info) {
			return fmt.Errorf("checksum path %q changed after checksumming", path)
		}
		if !wanted.directory && wanted.digest != current.digest {
			return fmt.Errorf("checksum file %q changed after checksumming", path)
		}
	}
	for path := range got {
		if _, found := want[path]; !found {
			return fmt.Errorf("checksum closure changed after checksumming: unexpected path %q", path)
		}
	}
	return nil
}

func revalidateChecksumClosureSnapshot(
	absoluteRoot string,
	snapshots map[string]checksumPathSnapshot,
	exclusions checksumExclusions,
	limits checksumLimits,
) (resultErr error) {
	rootSnapshot, found := snapshots[""]
	if !found || rootSnapshot.info == nil || !rootSnapshot.directory {
		return errors.New("checksum root snapshot is incomplete")
	}
	root, err := openPointerChecksumRoot(absoluteRoot)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, root.Close()) }()
	opened, err := root.Stat(".")
	if err != nil || !os.SameFile(rootSnapshot.info, opened) {
		return errors.Join(errors.New("checksum root changed while checksumming"), err)
	}
	_, current, err := collectPinnedChecksumSnapshot(
		root,
		opened,
		exclusions,
		limits,
		checksumTraversalHooks{},
		2,
	)
	if err != nil {
		return fmt.Errorf("revalidate pinned checksum tree: %w", err)
	}
	if err := compareChecksumSnapshots(snapshots, current); err != nil {
		return err
	}
	return revalidatePinnedChecksumRoot(root, rootSnapshot.info)
}

func stableChecksumPathInfo(snapshot checksumPathSnapshot, current os.FileInfo) bool {
	if snapshot.directory {
		return stableFileInfo(snapshot.info, current)
	}
	return stableChecksumFileInfo(snapshot.info, current)
}

func revalidatePinnedChecksumRoot(root *os.Root, before os.FileInfo) error {
	if root == nil || before == nil {
		return errors.New("checksum root snapshot is incomplete")
	}
	opened, openedErr := root.Stat(".")
	after, pathErr := os.Lstat(root.Name())
	if err := errors.Join(openedErr, pathErr); err != nil {
		return fmt.Errorf("revalidate checksum root: %w", err)
	}
	resolved, err := filepath.EvalSymlinks(root.Name())
	if err != nil {
		return fmt.Errorf("resolve checksum root during revalidation: %w", err)
	}
	if resolved != root.Name() || !opened.IsDir() || !after.IsDir() ||
		opened.Mode()&os.ModeSymlink != 0 || after.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(before, opened) || !os.SameFile(opened, after) ||
		!stableFileInfo(before, opened) || !stableFileInfo(opened, after) {
		return errors.New("checksum root changed while checksumming")
	}
	return nil
}

func digestChecksumFileAt(
	parent *os.Root,
	name string,
	before os.FileInfo,
	maxFileBytes int64,
) ([sha256.Size]byte, int64, os.FileInfo, error) {
	var digest [sha256.Size]byte
	if parent == nil || before == nil {
		return digest, 0, nil, errors.New("checksum file snapshot is incomplete")
	}
	file, err := parent.Open(name)
	if err != nil {
		return digest, 0, nil, err
	}
	opened, err := file.Stat()
	if err != nil {
		return digest, 0, nil, errors.Join(err, file.Close())
	}
	if !opened.Mode().IsRegular() || !sourceAuditFileHasOneLink(opened) ||
		!os.SameFile(before, opened) || !stableChecksumFileInfo(before, opened) {
		return digest, 0, nil, errors.Join(errors.New("file changed while opening"), file.Close())
	}
	if opened.Size() > maxFileBytes {
		return digest, 0, nil, errors.Join(
			fmt.Errorf("file exceeds %d bytes", maxFileBytes),
			file.Close(),
		)
	}

	hasher := sha256.New()
	readBytes, readErr := io.Copy(hasher, io.LimitReader(file, maxFileBytes+1))
	after, statErr := file.Stat()
	pathAfter, pathStatErr := parent.Lstat(name)
	closeErr := file.Close()
	if err := errors.Join(readErr, statErr, pathStatErr, closeErr); err != nil {
		return digest, readBytes, nil, err
	}
	if readBytes > maxFileBytes {
		return digest, readBytes, nil, fmt.Errorf("file exceeds %d bytes", maxFileBytes)
	}
	if !pathAfter.Mode().IsRegular() ||
		!sourceAuditFileHasOneLink(after) || !sourceAuditFileHasOneLink(pathAfter) ||
		!os.SameFile(opened, after) || !os.SameFile(after, pathAfter) ||
		!stableChecksumFileInfo(opened, after) ||
		!stableChecksumFileInfo(after, pathAfter) ||
		readBytes != after.Size() {
		return digest, readBytes, nil, errors.New("file changed while checksumming")
	}
	copy(digest[:], hasher.Sum(nil))
	return digest, readBytes, pathAfter, nil
}

func stableFileInfo(left, right os.FileInfo) bool {
	return left.Mode() == right.Mode() &&
		left.Size() == right.Size() &&
		left.ModTime().Equal(right.ModTime())
}

func stableChecksumFileInfo(left, right os.FileInfo) bool {
	return stableFileInfo(left, right) &&
		sourceAuditFileHasOneLink(left) == sourceAuditFileHasOneLink(right)
}

func isFilesystemRoot(path string) bool {
	cleaned := filepath.Clean(path)
	return filepath.Dir(cleaned) == cleaned
}

func marshalChecksumEntries(entries []checksumEntry, limits checksumLimits) ([]byte, error) {
	manifest := make([]byte, 0, len(entries)*96)
	for _, entry := range entries {
		lineBytes := sha256.Size*2 + 2 + len(entry.path) + 1
		if lineBytes > limits.maxManifestBytes-len(manifest) {
			return nil, fmt.Errorf("%s exceeds %d bytes", checksumManifestName, limits.maxManifestBytes)
		}
		manifest = hex.AppendEncode(manifest, entry.digest[:])
		manifest = append(manifest, ' ', ' ')
		manifest = append(manifest, entry.path...)
		manifest = append(manifest, '\n')
	}
	return manifest, nil
}

func parseChecksumManifest(
	manifest []byte,
	exclusions checksumExclusions,
	limits checksumLimits,
) ([]checksumEntry, error) {
	if len(manifest) > limits.maxManifestBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", checksumManifestName, limits.maxManifestBytes)
	}
	if len(manifest) == 0 {
		return nil, nil
	}
	if manifest[len(manifest)-1] != '\n' {
		return nil, fmt.Errorf("%s must end with a newline", checksumManifestName)
	}

	entries := make([]checksumEntry, 0)
	previous := ""
	for offset, lineNumber := 0, 1; offset < len(manifest); lineNumber++ {
		if len(entries) == limits.maxFiles {
			return nil, fmt.Errorf("%s exceeds %d entries", checksumManifestName, limits.maxFiles)
		}
		lineEnd := bytes.IndexByte(manifest[offset:], '\n')
		if lineEnd < 0 {
			return nil, fmt.Errorf("%s must end with a newline", checksumManifestName)
		}
		line := manifest[offset : offset+lineEnd]
		offset += lineEnd + 1
		if len(line) < sha256.Size*2+3 || line[sha256.Size*2] != ' ' || line[sha256.Size*2+1] != ' ' {
			return nil, fmt.Errorf("invalid %s line %d", checksumManifestName, lineNumber)
		}
		if len(line) > sha256.Size*2+2+limits.maxPathBytes {
			return nil, fmt.Errorf("path on %s line %d exceeds %d bytes", checksumManifestName, lineNumber, limits.maxPathBytes)
		}
		digestText := line[:sha256.Size*2]
		if !isLowerHex(digestText) {
			return nil, fmt.Errorf("invalid lowercase SHA-256 on %s line %d", checksumManifestName, lineNumber)
		}
		path := string(line[sha256.Size*2+2:])
		if err := validateChecksumPath(path, limits); err != nil {
			return nil, fmt.Errorf("unsafe path on %s line %d: %w", checksumManifestName, lineNumber, err)
		}
		if exclusions.contains(path) {
			return nil, fmt.Errorf("excluded path %q appears in %s", path, checksumManifestName)
		}
		if len(entries) != 0 && previous >= path {
			if previous == path {
				return nil, fmt.Errorf("duplicate path %q in %s", path, checksumManifestName)
			}
			return nil, fmt.Errorf("paths in %s are not in bytewise order", checksumManifestName)
		}
		entry := checksumEntry{path: path}
		if _, err := hex.Decode(entry.digest[:], digestText); err != nil {
			return nil, fmt.Errorf("decode SHA-256 on %s line %d: %w", checksumManifestName, lineNumber, err)
		}
		entries = append(entries, entry)
		previous = path
	}
	return entries, nil
}

type checksumExclusions struct {
	paths map[string]struct{}
}

func newChecksumExclusions(paths []string, limits checksumLimits) (checksumExclusions, error) {
	if err := limits.validate(); err != nil {
		return checksumExclusions{}, err
	}
	maxConfiguredPaths := min(limits.maxEntries, limits.maxFiles)
	seen := make(map[string]struct{}, min(len(paths), maxConfiguredPaths))
	seen[checksumManifestName] = struct{}{}
	configuredPaths := 0
	for _, path := range paths {
		if err := validateChecksumPath(path, limits); err != nil {
			return checksumExclusions{}, fmt.Errorf("invalid checksum exclusion %q: %w", path, err)
		}
		if _, found := seen[path]; found {
			continue
		}
		if configuredPaths == maxConfiguredPaths {
			return checksumExclusions{}, fmt.Errorf(
				"checksum exclusion set exceeds %d paths",
				maxConfiguredPaths,
			)
		}
		seen[path] = struct{}{}
		configuredPaths++
	}
	return checksumExclusions{paths: seen}, nil
}

func (exclusions checksumExclusions) contains(path string) bool {
	for {
		if _, excluded := exclusions.paths[path]; excluded {
			return true
		}
		separator := strings.LastIndexByte(path, '/')
		if separator < 0 {
			return false
		}
		path = path[:separator]
	}
}

func validateConfiguredChecksumPath(path string) error {
	if path == "" || path == "." {
		return errors.New("path must name a file or directory below the root")
	}
	if filepath.IsAbs(path) || filepath.VolumeName(path) != "" {
		return errors.New("path must be relative")
	}
	if path == ".." || strings.HasPrefix(path, "../") {
		return errors.New("path must remain below the root")
	}
	if filepath.ToSlash(path) != path || pathpkg.Clean(path) != path {
		return errors.New("path must be a canonical slash-relative path")
	}
	if !utf8.ValidString(path) || strings.ContainsAny(path, "\\\x00\r\n") {
		return errors.New("path contains characters unsupported by the canonical checksum format")
	}
	return nil
}

func validateChecksumPath(path string, limits checksumLimits) error {
	if err := validateConfiguredChecksumPath(path); err != nil {
		return err
	}
	if len(path) > limits.maxPathBytes {
		return fmt.Errorf("path exceeds %d bytes", limits.maxPathBytes)
	}
	if strings.Count(path, "/")+1 > limits.maxDepth {
		return fmt.Errorf("path exceeds depth %d", limits.maxDepth)
	}
	return nil
}

func isLowerHex(value []byte) bool {
	for _, character := range value {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func (limits checksumLimits) validate() error {
	if limits.maxEntries <= 0 ||
		limits.maxFiles <= 0 ||
		limits.maxPathBytes <= 0 ||
		limits.maxDepth <= 0 ||
		limits.maxFileBytes <= 0 ||
		limits.maxFileBytes == int64(^uint64(0)>>1) ||
		limits.maxTotalBytes <= 0 ||
		limits.maxManifestBytes <= 0 {
		return errors.New("invalid checksum resource limits")
	}
	return nil
}
