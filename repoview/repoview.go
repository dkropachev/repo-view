package repoview

import (
	"bufio"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"unicode"
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

func ignoredSearchLines(lines []string, ext string, dropComments, dropDocstrings bool) map[int]bool {
	ignored := map[int]bool{}
	if dropComments {
		for idx, line := range lines {
			trimmed := strings.TrimSpace(line)
			if ext == ".py" && strings.HasPrefix(trimmed, "#") {
				ignored[idx+1] = true
			}
			if ext != ".py" && (strings.HasPrefix(trimmed, "//") || strings.HasPrefix(trimmed, "/*") || strings.HasPrefix(trimmed, "*")) {
				ignored[idx+1] = true
			}
		}
	}
	if dropDocstrings && ext == ".py" {
		inDocstring := false
		quote := ""
		for idx, line := range lines {
			trimmed := strings.TrimSpace(line)
			if inDocstring {
				ignored[idx+1] = true
				if strings.Contains(trimmed, quote) {
					inDocstring = false
				}
				continue
			}
			if strings.HasPrefix(trimmed, `"""`) || strings.HasPrefix(trimmed, `'''`) {
				quote = trimmed[:3]
				ignored[idx+1] = true
				if strings.Count(trimmed, quote) < 2 {
					inDocstring = true
				}
			}
		}
	}
	return ignored
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
	return map[string]bool{
		".c": true, ".cc": true, ".cpp": true, ".cs": true, ".go": true,
		".h": true, ".hpp": true, ".java": true, ".js": true, ".jsx": true,
		".kt": true, ".mjs": true, ".mod": true, ".py": true, ".rs": true,
		".swift": true, ".ts": true, ".tsx": true,
	}
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

func containsSymbol(line, symbol string) bool {
	pos := strings.Index(line, symbol)
	for pos >= 0 {
		beforeOK := pos == 0 || !isIdent(rune(line[pos-1]))
		after := pos + len(symbol)
		afterOK := after >= len(line) || !isIdent(rune(line[after]))
		if beforeOK && afterOK {
			return true
		}
		next := strings.Index(line[pos+1:], symbol)
		if next < 0 {
			return false
		}
		pos += next + 1
	}
	return false
}

func isIdent(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r)
}

func looksLikeDefinition(line, symbol, ext string) bool {
	if found, ok := definitionSymbol(line, ext); ok {
		return found == symbol
	}
	escaped := regexp.QuoteMeta(symbol)
	stripped := strings.TrimSpace(line)
	switch ext {
	case ".go":
		return regexp.MustCompile(`^func\s+(\([^)]*\)\s*)?`+escaped+`\b`).MatchString(stripped) ||
			regexp.MustCompile(`^type\s+`+escaped+`\b`).MatchString(stripped)
	case ".py":
		return regexp.MustCompile(`^(async\s+def|def|class)\s+` + escaped + `\b`).MatchString(stripped)
	case ".rs":
		return regexp.MustCompile(`^(pub(\([^)]*\))?\s+)?(async\s+)?(fn|struct|enum|trait|impl)\s+` + escaped + `\b`).MatchString(stripped)
	default:
		return regexp.MustCompile(`^(function|class)\s+` + escaped + `\b`).MatchString(stripped)
	}
}
