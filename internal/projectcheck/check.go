// Package projectcheck implements repository-wide CI policy checks in Go.
package projectcheck

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// ValidateJSON parses every tracked JSON file as exactly one JSON value.
func ValidateJSON(root string) error {
	paths, err := trackedFiles(root)
	if err != nil {
		return err
	}
	var failures []error
	for _, path := range paths {
		if filepath.Ext(path) != ".json" {
			continue
		}
		file, err := os.Open(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			failures = append(failures, fmt.Errorf("open %s: %w", path, err))
			continue
		}
		decoder := json.NewDecoder(bufio.NewReader(file))
		var document any
		decodeErr := decoder.Decode(&document)
		if decodeErr == nil {
			var trailing any
			if err := decoder.Decode(&trailing); err == nil {
				decodeErr = errors.New("multiple JSON values")
			} else if !errors.Is(err, io.EOF) {
				decodeErr = fmt.Errorf("read trailing JSON: %w", err)
			}
		}
		closeErr := file.Close()
		if decodeErr != nil {
			failures = append(failures, fmt.Errorf("parse %s: %w", path, decodeErr))
		} else if closeErr != nil {
			failures = append(failures, fmt.Errorf("close %s: %w", path, closeErr))
		}
	}
	return errors.Join(failures...)
}

// ValidateNoBash rejects ScopeSifter-owned Bash scripts and execution markers.
// Go tests may contain inert shell fixture bytes, but executable process calls
// are inspected through the Go syntax tree as well.
func ValidateNoBash(root string) error {
	paths, err := trackedFiles(root)
	if err != nil {
		return err
	}
	var failures []error
	for _, path := range paths {
		if strings.EqualFold(filepath.Ext(path), ".sh") {
			failures = append(failures, fmt.Errorf("tracked shell-script path: %s", path))
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			failures = append(failures, fmt.Errorf("read %s: %w", path, err))
			continue
		}
		firstLine := bytes.SplitN(data, []byte("\n"), 2)[0]
		if bytes.HasPrefix(data, []byte("#!")) && bytes.Contains(bytes.ToLower(firstLine), bashName()) {
			failures = append(failures, fmt.Errorf("bash shebang: %s", path))
			continue
		}
		if filepath.Ext(path) == ".go" {
			if err := rejectGoBashExecution(path, data); err != nil {
				failures = append(failures, err)
			}
		}
		if isMakefile(path) {
			for lineNumber, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(line, "\t") && containsBashCommand(line) {
					failures = append(failures, fmt.Errorf("bash Make recipe in %s:%d", path, lineNumber+1))
				}
			}
		}
		if isWorkflow(path) {
			if err := rejectWorkflowBash(path, string(data)); err != nil {
				failures = append(failures, err)
			}
		}
	}
	return errors.Join(failures...)
}

func bashName() []byte {
	return []byte{'b', 'a', 's', 'h'}
}

func isMakefile(path string) bool {
	base := filepath.Base(path)
	return base == "Makefile" || strings.HasSuffix(base, ".mk")
}

func isWorkflow(path string) bool {
	return strings.HasPrefix(path, ".github/workflows/") &&
		(filepath.Ext(path) == ".yml" || filepath.Ext(path) == ".yaml")
}

func rejectWorkflowBash(path, content string) error {
	var failures []error
	blockIndent := -1
	for index, line := range strings.Split(content, "\n") {
		trimmed := strings.TrimSpace(line)
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		if blockIndent >= 0 {
			if trimmed == "" {
				continue
			}
			if indent > blockIndent {
				if containsBashCommand(trimmed) {
					failures = append(failures, fmt.Errorf("bash workflow command in %s:%d", path, index+1))
				}
				continue
			}
			blockIndent = -1
		}
		key, value, found := strings.Cut(trimmed, ":")
		if !found {
			continue
		}
		key = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(key), "-"))
		switch strings.ToLower(key) {
		case "shell":
			if isBashWord(strings.TrimSpace(value)) {
				failures = append(failures, fmt.Errorf("bash workflow shell in %s:%d", path, index+1))
			}
		case "run":
			value = strings.TrimSpace(value)
			if value == "|" || value == ">" || strings.HasPrefix(value, "|-") || strings.HasPrefix(value, ">-") {
				blockIndent = indent
			} else if containsBashCommand(value) {
				failures = append(failures, fmt.Errorf("bash workflow command in %s:%d", path, index+1))
			}
		}
	}
	return errors.Join(failures...)
}

func containsBashCommand(command string) bool {
	for _, field := range strings.Fields(command) {
		word := strings.Trim(field, "'\"`(){}[];,|&")
		if isBashWord(word) {
			return true
		}
	}
	return false
}

func isBashWord(word string) bool {
	word = strings.ToLower(strings.TrimSpace(word))
	return word == string(bashName()) || strings.HasSuffix(word, "/"+string(bashName()))
}

func rejectGoBashExecution(path string, data []byte) error {
	parsed, err := parser.ParseFile(token.NewFileSet(), path, data, 0)
	if err != nil {
		return fmt.Errorf("parse Go source %s: %w", path, err)
	}
	execAliases := map[string]struct{}{}
	syscallAliases := map[string]struct{}{}
	for _, imported := range parsed.Imports {
		value, err := strconv.Unquote(imported.Path.Value)
		if err != nil {
			continue
		}
		name := filepath.Base(value)
		if imported.Name != nil {
			name = imported.Name.Name
		}
		switch value {
		case "os/exec":
			execAliases[name] = struct{}{}
		case "syscall":
			syscallAliases[name] = struct{}{}
		}
	}
	var found bool
	ast.Inspect(parsed, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok || len(call.Args) == 0 {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		identifier, ok := selector.X.(*ast.Ident)
		if !ok {
			return true
		}
		_, isExec := execAliases[identifier.Name]
		_, isSyscall := syscallAliases[identifier.Name]
		execCall := isExec && (selector.Sel.Name == "Command" || selector.Sel.Name == "CommandContext")
		syscallCall := isSyscall && selector.Sel.Name == "Exec"
		if execCall || syscallCall {
			if command, ok := constantString(call.Args[0]); ok && isBashWord(command) {
				found = true
				return false
			}
		}
		return true
	})
	if found {
		return fmt.Errorf("bash process execution in %s", path)
	}
	return nil
}

func constantString(expression ast.Expr) (string, bool) {
	switch value := expression.(type) {
	case *ast.BasicLit:
		if value.Kind != token.STRING {
			return "", false
		}
		result, err := strconv.Unquote(value.Value)
		return result, err == nil
	case *ast.BinaryExpr:
		if value.Op != token.ADD {
			return "", false
		}
		left, leftOK := constantString(value.X)
		right, rightOK := constantString(value.Y)
		return left + right, leftOK && rightOK
	case *ast.ParenExpr:
		return constantString(value.X)
	default:
		return "", false
	}
}

func trackedFiles(root string) ([]string, error) {
	command := exec.Command("git", "ls-files", "-z")
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("list tracked files: %w", err)
	}
	parts := bytes.Split(output, []byte{0})
	paths := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) == 0 {
			continue
		}
		path := string(part)
		if filepath.IsAbs(path) || filepath.Clean(path) != filepath.FromSlash(path) {
			return nil, fmt.Errorf("unsafe tracked path: %q", path)
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, nil
}
