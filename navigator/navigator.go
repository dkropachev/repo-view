package navigator

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
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
	// allowing one navigation operation to consume unbounded memory or time.
	maximumSourceLineBytes   = (8 << 20) + bufio.MaxScanTokenSize
	maximumSourceFileBytes   = 64 << 20
	maximumSourceTreeBytes   = 1 << 30
	maximumSourceTreeEntries = 100_000
	maximumRepositoryDepth   = 128
	maximumRepositoryPathLen = 4_096
)

var errRepositoryRootChanged = errors.New("repository root changed after opening")

type View struct {
	ctx       context.Context
	pinnedGit *gitExecutableIdentity
	rootInfo  os.FileInfo
	root      string
}

func New(root string) (*View, error) {
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
	return &View{
		rootInfo: info,
		root:     filepath.Clean(resolved),
		ctx:      context.Background(),
	}, nil
}

// WithContext returns an independent view whose filesystem walks, source
// scans, and Git subprocesses are canceled with ctx. View values returned
// by New are safe to use as immutable templates for concurrent request-local
// clones.
func (r *View) WithContext(ctx context.Context) *View {
	clone := *r
	if ctx == nil {
		ctx = context.Background()
	}
	clone.ctx = ctx
	return &clone
}

func (r *View) operationContext() context.Context {
	if r.ctx == nil {
		return context.Background()
	}
	return r.ctx
}

func (r *View) checkContext() error {
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
func NewWithGit(root, executable, expectedSHA256 string) (*View, error) {
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

func (r *View) sourceFiles() ([]string, error) {
	return r.repositoryFiles(true)
}

// repositoryFiles returns every regular, non-symlink repository file when
// sourceOnly is false. Source-only walks retain the language-extension and
// byte-budget checks required before content scanning.
func (r *View) repositoryFiles(sourceOnly bool) ([]string, error) {
	extensions := defaultExtensions()
	excludes := defaultExcludes()
	type pendingDirectory struct {
		info     os.FileInfo
		relative string
	}
	directories := []pendingDirectory{{info: r.rootInfo, relative: "."}}
	var paths []string
	var sourceBytes int64
	entryCount := 1
	for index := 0; index < len(directories); index++ {
		if err := r.checkContext(); err != nil {
			return nil, err
		}
		pending := directories[index]
		directory, opened, err := r.openRelativeDirectory(pending.relative)
		if err != nil {
			return nil, err
		}
		if !os.SameFile(pending.info, opened) {
			_ = directory.Close()
			return nil, fmt.Errorf(
				"repository directory changed while scanning: %q",
				pending.relative,
			)
		}
		file, err := directory.Open(".")
		if err != nil {
			_ = directory.Close()
			return nil, err
		}
		entries, readErr := file.ReadDir(-1)
		closeFileErr := file.Close()
		if readErr != nil {
			_ = directory.Close()
			return nil, readErr
		}
		if closeFileErr != nil {
			_ = directory.Close()
			return nil, closeFileErr
		}
		for _, entry := range entries {
			if err := r.checkContext(); err != nil {
				_ = directory.Close()
				return nil, err
			}
			entryCount++
			if entryCount > maximumSourceTreeEntries {
				_ = directory.Close()
				return nil, fmt.Errorf(
					"repository tree exceeds %d entries",
					maximumSourceTreeEntries,
				)
			}
			name := entry.Name()
			info, statErr := directory.Lstat(name)
			if statErr != nil {
				_ = directory.Close()
				return nil, statErr
			}
			if info.Mode()&os.ModeSymlink != 0 {
				continue
			}
			relative := name
			if pending.relative != "." {
				relative = path.Join(pending.relative, name)
			}
			if len(relative) > maximumRepositoryPathLen ||
				strings.Count(relative, "/") > maximumRepositoryDepth {
				_ = directory.Close()
				return nil, fmt.Errorf(
					"repository path exceeds traversal limits: %q",
					relative,
				)
			}
			if info.IsDir() {
				if !excludes[name] {
					directories = append(directories, pendingDirectory{
						info: info, relative: relative,
					})
				}
				continue
			}
			if !info.Mode().IsRegular() || (sourceOnly && !extensions[path.Ext(relative)]) {
				continue
			}
			if sourceOnly {
				if info.Size() < 0 || info.Size() > maximumSourceFileBytes {
					_ = directory.Close()
					return nil, fmt.Errorf(
						"source file exceeds %d bytes: %s",
						maximumSourceFileBytes,
						relative,
					)
				}
				if sourceBytes > maximumSourceTreeBytes-info.Size() {
					_ = directory.Close()
					return nil, fmt.Errorf(
						"repository source exceeds %d bytes",
						maximumSourceTreeBytes,
					)
				}
				sourceBytes += info.Size()
			}
			paths = append(paths, filepath.Join(r.root, filepath.FromSlash(relative)))
		}
		if err := directory.Close(); err != nil {
			return nil, err
		}
	}
	if err := r.verifyRootIdentity(); err != nil {
		return nil, err
	}
	sort.Strings(paths)
	return paths, nil
}

func defaultExtensions() map[string]bool {
	extensions := make(map[string]bool, len(languagesByExtension))
	for _, extension := range SupportedExtensions() {
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

func (r *View) readRelativeLines(relative string) ([]string, string, error) {
	file, opened, clean, err := r.openRelativeRegularFile(relative)
	if err != nil {
		return nil, "", err
	}
	if opened.Size() < 0 || opened.Size() > maximumSourceFileBytes {
		_ = file.Close()
		return nil, "", fmt.Errorf(
			"source file exceeds %d bytes: %s",
			maximumSourceFileBytes,
			clean,
		)
	}
	ctx := r.operationContext()
	scanner := bufio.NewScanner(io.LimitReader(file, maximumSourceFileBytes+1))
	scanner.Buffer(make([]byte, 1024), maximumSourceLineBytes)
	var lines []string
	for scanner.Scan() {
		select {
		case <-ctx.Done():
			_ = file.Close()
			return nil, "", ctx.Err()
		default:
		}
		lines = append(lines, scanner.Text())
	}
	if err := scanner.Err(); err != nil {
		_ = file.Close()
		return nil, "", err
	}
	if err := r.verifyRelativeRegularFileUnchanged(clean, file, opened); err != nil {
		_ = file.Close()
		return nil, "", err
	}
	if err := file.Close(); err != nil {
		return nil, "", err
	}
	return lines, clean, nil
}

func cleanRepositoryPath(relative string) (string, error) {
	native := filepath.FromSlash(relative)
	if relative == "" ||
		(filepath.Separator == '\\' && strings.Contains(relative, `\`)) ||
		strings.ContainsRune(relative, '\x00') ||
		path.IsAbs(relative) ||
		filepath.IsAbs(native) ||
		filepath.VolumeName(native) != "" {
		return "", fmt.Errorf("repository path must be a nonempty relative slash path: %q", relative)
	}
	clean := path.Clean(relative)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("repository path escapes the root: %q", relative)
	}
	return filepath.ToSlash(clean), nil
}

func (r *View) openRelativeRegularFile(
	relative string,
) (*os.File, os.FileInfo, string, error) {
	clean, err := cleanRepositoryPath(relative)
	if err != nil {
		return nil, nil, "", err
	}
	root, err := r.openVerifiedRoot()
	if err != nil {
		return nil, nil, "", err
	}
	native := filepath.FromSlash(clean)
	components := strings.Split(native, string(filepath.Separator))
	root, err = descendVerifiedDirectories(
		root,
		components[:len(components)-1],
		relative,
	)
	if err != nil {
		return nil, nil, "", err
	}
	defer func() {
		_ = root.Close()
	}()

	name := components[len(components)-1]
	before, err := root.Lstat(name)
	if err != nil {
		return nil, nil, "", fmt.Errorf("inspect repository path %q: %w", relative, err)
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return nil, nil, "", fmt.Errorf("repository path is not a regular file: %q", relative)
	}
	file, err := root.OpenFile(name, regularFileOpenFlags(), 0)
	if err != nil {
		return nil, nil, "", fmt.Errorf("open repository path %q: %w", relative, err)
	}
	opened, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, nil, "", err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		_ = file.Close()
		return nil, nil, "", fmt.Errorf("repository file changed while opening: %q", relative)
	}
	return file, opened, clean, nil
}

func descendVerifiedDirectories(
	root *os.Root,
	components []string,
	relative string,
) (*os.Root, error) {
	for _, component := range components {
		before, statErr := root.Lstat(component)
		if statErr != nil {
			_ = root.Close()
			return nil, fmt.Errorf(
				"inspect repository path %q: %w", relative, statErr,
			)
		}
		if !before.IsDir() || before.Mode()&os.ModeSymlink != 0 {
			_ = root.Close()
			return nil, fmt.Errorf(
				"repository path %q traverses a non-directory or symbolic link", relative,
			)
		}
		next, openErr := root.OpenRoot(component)
		if openErr != nil {
			_ = root.Close()
			return nil, fmt.Errorf(
				"open repository path %q: %w", relative, openErr,
			)
		}
		after, statErr := next.Stat(".")
		if statErr != nil {
			_ = next.Close()
			_ = root.Close()
			return nil, fmt.Errorf(
				"verify repository path %q: %w", relative, statErr,
			)
		}
		if !after.IsDir() || !os.SameFile(before, after) {
			_ = next.Close()
			_ = root.Close()
			return nil, fmt.Errorf(
				"repository path %q changed while opening", relative,
			)
		}
		if err := root.Close(); err != nil {
			_ = next.Close()
			return nil, fmt.Errorf(
				"close repository path %q: %w", relative, err,
			)
		}
		root = next
	}
	return root, nil
}

func (r *View) openVerifiedRoot() (*os.Root, error) {
	root, err := os.OpenRoot(r.root)
	if err != nil {
		return nil, fmt.Errorf("open repository root: %w", err)
	}
	opened, err := root.Stat(".")
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("inspect repository root: %w", err)
	}
	if !opened.IsDir() || !os.SameFile(r.rootInfo, opened) {
		_ = root.Close()
		return nil, errRepositoryRootChanged
	}
	return root, nil
}

func (r *View) openRelativeDirectory(
	relative string,
) (*os.Root, os.FileInfo, error) {
	root, err := r.openVerifiedRoot()
	if err != nil {
		return nil, nil, err
	}
	if relative != "." {
		clean, cleanErr := cleanRepositoryPath(relative)
		if cleanErr != nil {
			_ = root.Close()
			return nil, nil, cleanErr
		}
		components := strings.Split(
			filepath.FromSlash(clean),
			string(filepath.Separator),
		)
		root, err = descendVerifiedDirectories(root, components, relative)
		if err != nil {
			return nil, nil, err
		}
	}
	opened, err := root.Stat(".")
	if err != nil {
		_ = root.Close()
		return nil, nil, err
	}
	return root, opened, nil
}

func (r *View) verifyRootIdentity() error {
	root, err := r.openVerifiedRoot()
	if err != nil {
		return err
	}
	return root.Close()
}

func (r *View) verifyRelativeRegularFileUnchanged(
	relative string,
	file *os.File,
	opened os.FileInfo,
) error {
	finished, err := file.Stat()
	if err != nil {
		return err
	}
	afterFile, after, _, err := r.openRelativeRegularFile(relative)
	if err != nil {
		return err
	}
	if err := afterFile.Close(); err != nil {
		return err
	}
	if !os.SameFile(opened, finished) ||
		!os.SameFile(opened, after) ||
		opened.Size() != finished.Size() ||
		!opened.ModTime().Equal(finished.ModTime()) {
		return fmt.Errorf("repository file changed while reading: %q", relative)
	}
	return nil
}

func (r *View) validateRelativeRegularFile(relative string) (string, error) {
	file, _, clean, err := r.openRelativeRegularFile(relative)
	if err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	return clean, nil
}

func (r *View) snapshotRelativeRegularFile(
	relative, snapshotPath string,
	bytesRemaining *int64,
) error {
	source, opened, clean, err := r.openRelativeRegularFile(relative)
	if err != nil {
		return err
	}
	defer source.Close()
	if opened.Size() < 0 || opened.Size() > *bytesRemaining {
		return fmt.Errorf("%w: %q", errSnapshotBudget, relative)
	}

	destination, err := os.OpenFile(
		snapshotPath,
		os.O_WRONLY|os.O_CREATE|os.O_EXCL,
		opened.Mode().Perm(),
	)
	if err != nil {
		return err
	}
	copied, copyErr := io.CopyN(destination, source, *bytesRemaining+1)
	closeErr := destination.Close()
	if copyErr != nil && !errors.Is(copyErr, io.EOF) {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if copied > *bytesRemaining {
		return fmt.Errorf("%w: %q", errSnapshotBudget, relative)
	}
	*bytesRemaining -= copied
	if err := r.verifyRelativeRegularFileUnchanged(clean, source, opened); err != nil {
		return err
	}
	if err := os.Chmod(snapshotPath, opened.Mode().Perm()); err != nil {
		return err
	}
	return nil
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
