package repoview

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

type RepoView struct {
	root string
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
	return &RepoView{root: filepath.Clean(resolved)}, nil
}

func (r *RepoView) sourceFiles() ([]string, error) {
	extensions := defaultExtensions()
	excludes := defaultExcludes()
	var paths []string
	err := filepath.WalkDir(r.root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
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
	lines, err := readLines(fullPath)
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
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("source path is not a regular file: %s", path)
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

	scanner := bufio.NewScanner(file)
	scanner.Buffer(make([]byte, 1024), 1024*1024)
	var lines []string
	for scanner.Scan() {
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
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
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
