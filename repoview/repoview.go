package repoview

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	// Match the JavaScript backend's 8 MiB per-line budget, with one
	// Scanner-sized margin for a line terminator and read-ahead. The other
	// limits make repository traversal and source reads fail closed instead of
	// allowing an MCP request to consume unbounded memory or time.
	maximumSourceLineBytes   = (8 << 20) + bufio.MaxScanTokenSize
	maximumSourceFileBytes   = 64 << 20
	maximumSourceTreeBytes   = 1 << 30
	maximumSourceTreeEntries = 100_000
	maximumRepositoryDepth   = 128
	maximumRepositoryPathLen = 4_096
)

type RepoView struct {
	pinnedGit *gitExecutableIdentity
	root      string
	ctx       context.Context
}

func New(root string) (*RepoView, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return nil, fmt.Errorf("resolve repository root: %w", err)
	}
	info, err := os.Stat(resolved)
	if err != nil {
		return nil, fmt.Errorf("inspect repository root: %w", err)
	}
	if !info.IsDir() {
		return nil, fmt.Errorf("repository root is not a directory: %s", root)
	}
	return &RepoView{
		root: filepath.Clean(resolved),
		ctx:  context.Background(),
	}, nil
}

// WithContext returns an independent view whose filesystem walks, source
// scans, and Git subprocesses are canceled with ctx. RepoView values returned
// by New are safe to use as immutable templates for concurrent request-local
// clones.
func (r *RepoView) WithContext(ctx context.Context) *RepoView {
	clone := *r
	if ctx == nil {
		ctx = context.Background()
	}
	clone.ctx = ctx
	return &clone
}

func (r *RepoView) operationContext() context.Context {
	if r.ctx == nil {
		return context.Background()
	}
	return r.ctx
}

func (r *RepoView) checkContext() error {
	select {
	case <-r.operationContext().Done():
		return r.operationContext().Err()
	default:
		return nil
	}
}

// NewWithGit constructs a repository view whose Git-backed operations use one
// exact executable. The path and SHA-256 are verified at construction and
// immediately before and after every Git subprocess. Non-Git navigation has
// the same behavior as New.
func NewWithGit(root, executable, expectedSHA256 string) (*RepoView, error) {
	view, err := New(root)
	if err != nil {
		return nil, err
	}
	identity, err := newGitExecutableIdentity(executable, expectedSHA256)
	if err != nil {
		return nil, err
	}
	view.pinnedGit = &identity
	return view, nil
}

func (r *RepoView) sourceFiles() ([]string, error) {
	extensions := defaultExtensions()
	excludes := defaultExcludes()
	var paths []string
	var sourceBytes int64
	entries := 0
	err := filepath.WalkDir(r.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := r.checkContext(); err != nil {
			return err
		}
		entries++
		if entries > maximumSourceTreeEntries {
			return fmt.Errorf(
				"repository tree exceeds %d entries",
				maximumSourceTreeEntries,
			)
		}
		relative, err := filepath.Rel(r.root, path)
		if err != nil {
			return fmt.Errorf("resolve repository path: %w", err)
		}
		relative = filepath.ToSlash(relative)
		if len(relative) > maximumRepositoryPathLen ||
			strings.Count(relative, "/") > maximumRepositoryDepth {
			return fmt.Errorf("repository path exceeds traversal limits: %q", relative)
		}
		name := d.Name()
		if d.IsDir() {
			if path != r.root && excludes[name] {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&fs.ModeSymlink != 0 {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		if info.Mode().IsRegular() && extensions[filepath.Ext(path)] {
			if info.Size() < 0 || info.Size() > maximumSourceFileBytes {
				return fmt.Errorf(
					"source file exceeds %d bytes: %s",
					maximumSourceFileBytes,
					relative,
				)
			}
			if sourceBytes > maximumSourceTreeBytes-info.Size() {
				return fmt.Errorf(
					"repository source exceeds %d bytes",
					maximumSourceTreeBytes,
				)
			}
			sourceBytes += info.Size()
			paths = append(paths, path)
		}
		return nil
	})
	sort.Strings(paths)
	return paths, err
}

func defaultExtensions() map[string]bool {
	extensions := make(map[string]bool, len(languagesByExtension))
	for _, extension := range supportedExtensions() {
		extensions[extension] = true
	}
	return extensions
}

func defaultExcludes() map[string]bool {
	return map[string]bool{
		".cache": true, ".git": true, ".hg": true, ".svn": true, ".venv": true,
		"build": true, "dist": true, "node_modules": true, "target": true,
		"vendor": true,
	}
}

func (r *RepoView) readRelativeLines(relative string) ([]string, string, error) {
	clean, fullPath, err := r.resolveRegularPath(relative)
	if err != nil {
		return nil, "", err
	}
	lines, err := readLinesContext(r.operationContext(), fullPath)
	if err != nil {
		return nil, "", err
	}
	resolvedAfter, err := filepath.EvalSymlinks(fullPath)
	if err != nil {
		return nil, "", fmt.Errorf("verify repository path %q: %w", relative, err)
	}
	if filepath.Clean(resolvedAfter) != fullPath {
		return nil, "", fmt.Errorf("repository path %q traverses a symbolic link", relative)
	}
	return lines, clean, nil
}

func (r *RepoView) resolveRegularPath(relative string) (string, string, error) {
	native := filepath.FromSlash(relative)
	if relative == "" ||
		strings.Contains(relative, `\`) ||
		strings.ContainsRune(relative, '\x00') ||
		path.IsAbs(relative) ||
		filepath.IsAbs(native) ||
		filepath.VolumeName(native) != "" {
		return "", "", fmt.Errorf("repository path must be a nonempty relative slash path: %q", relative)
	}
	clean := path.Clean(relative)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", "", fmt.Errorf("repository path escapes the root: %q", relative)
	}
	fullPath := filepath.Clean(filepath.Join(r.root, filepath.FromSlash(clean)))
	within, err := filepath.Rel(r.root, fullPath)
	if err != nil || within == ".." || strings.HasPrefix(within, ".."+string(filepath.Separator)) {
		return "", "", fmt.Errorf("repository path escapes the root: %q", relative)
	}
	resolved, err := filepath.EvalSymlinks(fullPath)
	if err != nil {
		return "", "", fmt.Errorf("resolve repository path %q: %w", relative, err)
	}
	if filepath.Clean(resolved) != fullPath {
		return "", "", fmt.Errorf("repository path %q traverses a symbolic link", relative)
	}
	info, err := os.Lstat(fullPath)
	if err != nil {
		return "", "", fmt.Errorf("inspect repository path %q: %w", relative, err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", "", fmt.Errorf("repository path is not a regular file: %q", relative)
	}
	return filepath.ToSlash(clean), fullPath, nil
}

func readLines(path string) ([]string, error) {
	return readLinesContext(context.Background(), path)
}

func readLinesContext(ctx context.Context, path string) ([]string, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("source path is not a regular file: %s", path)
	}
	if before.Size() < 0 || before.Size() > maximumSourceFileBytes {
		return nil, fmt.Errorf(
			"source file exceeds %d bytes: %s",
			maximumSourceFileBytes,
			path,
		)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		_ = file.Close()
		return nil, fmt.Errorf("source file changed while opening: %s", path)
	}

	scanner := bufio.NewScanner(io.LimitReader(file, maximumSourceFileBytes+1))
	scanner.Buffer(make([]byte, 1024), maximumSourceLineBytes)
	var lines []string
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, ctx.Err()
		default:
		}
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		_ = file.Close()
		return nil, err
	}
	finished, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	after, err := os.Lstat(path)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !os.SameFile(opened, finished) ||
		!os.SameFile(opened, after) ||
		after.Mode()&os.ModeSymlink != 0 ||
		opened.Size() != finished.Size() ||
		!opened.ModTime().Equal(finished.ModTime()) {
		_ = file.Close()
		return nil, fmt.Errorf("source file changed while reading: %s", path)
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	return lines, nil
}

func countSymbolOccurrences(line, symbol string) int {
	if symbol == "" {
		return 0
	}
	count := 0
	offset := 0
	for offset <= len(line)-len(symbol) {
		relative := strings.Index(line[offset:], symbol)
		if relative < 0 {
			break
		}
		pos := offset + relative
		before, _ := utf8.DecodeLastRuneInString(line[:pos])
		beforeOK := pos == 0 || !isIdent(before)
		after := pos + len(symbol)
		afterRune, _ := utf8.DecodeRuneInString(line[after:])
		afterOK := after >= len(line) || !isIdent(afterRune)
		if beforeOK && afterOK {
			count++
		}
		_, size := utf8.DecodeRuneInString(line[pos:])
		if size == 0 {
			size = 1
		}
		offset = pos + size
	}
	return count
}

func isIdent(r rune) bool {
	return pythonIdentifierContinue(r) || r == '_' || unicode.In(
		r,
		unicode.L,
		unicode.Nl,
		unicode.Nd,
		unicode.Mn,
		unicode.Mc,
		unicode.Pc,
		unicode.Other_ID_Start,
		unicode.Other_ID_Continue,
	)
}

func definitionCount(definitions []sourceDefinition, lineNo int, symbol string) int {
	count := 0
	for _, definition := range definitions {
		if definition.line == lineNo && definition.symbol == symbol {
			count++
		}
	}
	return count
}
