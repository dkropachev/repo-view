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
	"path"
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

// ValidateNoScripts rejects project-owned executable-script paths, script
// shebangs, and explicit script-runtime invocation. Go tests may contain inert
// language/provenance fixture bytes, but process calls are syntax-checked.
func ValidateNoScripts(root string) error {
	paths, err := trackedFiles(root)
	if err != nil {
		return err
	}
	makeTargets, err := collectMakeTargets(root, paths)
	if err != nil {
		return err
	}
	var failures []error
	for _, path := range paths {
		if isScriptPath(path) {
			failures = append(failures, fmt.Errorf("tracked script path: %s", path))
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			failures = append(failures, fmt.Errorf("read %s: %w", path, err))
			continue
		}
		firstLine := bytes.SplitN(data, []byte("\n"), 2)[0]
		if bytes.HasPrefix(data, []byte("#!")) && shebangUsesScriptRuntime(string(firstLine)) {
			failures = append(failures, fmt.Errorf("script shebang: %s", path))
			continue
		}
		if filepath.Ext(path) == ".go" {
			if err := rejectGoScriptExecution(path, data); err != nil {
				failures = append(failures, err)
			}
		}
		if isMakefile(path) {
			for lineNumber, line := range strings.Split(string(data), "\n") {
				if strings.HasPrefix(line, "\t") && containsScriptRuntimeCommand(line) {
					failures = append(failures, fmt.Errorf("script runtime in Make recipe %s:%d", path, lineNumber+1))
				}
			}
		}
		if isWorkflow(path) {
			if err := rejectWorkflowScripts(path, string(data), makeTargets); err != nil {
				failures = append(failures, err)
			}
		}
	}
	return errors.Join(failures...)
}

func scriptRuntimeNames() map[string]struct{} {
	return map[string]struct{}{
		"ash": {}, "bash": {}, "busybox": {}, "dash": {}, "fish": {},
		"ksh": {}, "node": {}, "perl": {}, "powershell": {}, "pwsh": {},
		"python": {}, "python2": {}, "python3": {}, "ruby": {}, "sh": {},
		"zsh": {},
	}
}

func isScriptPath(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".sh", ".bash", ".dash", ".ash", ".zsh", ".ksh", ".fish",
		".py", ".rb", ".pl", ".ps1", ".bat", ".cmd":
		return true
	default:
		return false
	}
}

func isMakefile(path string) bool {
	base := filepath.Base(path)
	return base == "Makefile" || strings.HasSuffix(base, ".mk")
}

func isWorkflow(path string) bool {
	return strings.HasPrefix(path, ".github/workflows/") &&
		(filepath.Ext(path) == ".yml" || filepath.Ext(path) == ".yaml")
}

func rejectWorkflowScripts(
	path, content string,
	makeTargets map[string]struct{},
) error {
	var failures []error
	lines := strings.Split(content, "\n")
	hasRun := false
	hasPOSIXShell := false
	for index := 0; index < len(lines); index++ {
		line := lines[index]
		trimmed := strings.TrimSpace(line)
		indent := len(line) - len(strings.TrimLeft(line, " \t"))
		key, value, found := strings.Cut(trimmed, ":")
		if !found {
			continue
		}
		key = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(key), "-"))
		switch strings.ToLower(key) {
		case "shell":
			if strings.TrimSpace(value) == "sh" {
				hasPOSIXShell = true
			} else {
				failures = append(failures, fmt.Errorf("workflow shell is not plain sh in %s:%d", path, index+1))
			}
		case "run":
			value = strings.TrimSpace(value)
			if value == "" {
				// `defaults.run` is a mapping whose nested `shell` fixes the
				// workflow runner; it is not an authored command.
				continue
			}
			hasRun = true
			if value == "|" || value == ">" || strings.HasPrefix(value, "|-") || strings.HasPrefix(value, ">-") {
				var commands []workflowCommand
				for next := index + 1; next < len(lines); next++ {
					nextLine := lines[next]
					nextTrimmed := strings.TrimSpace(nextLine)
					nextIndent := len(nextLine) - len(strings.TrimLeft(nextLine, " \t"))
					if nextTrimmed != "" && nextIndent <= indent {
						break
					}
					index = next
					if nextTrimmed != "" {
						commands = append(commands, workflowCommand{line: next + 1, text: nextTrimmed})
					}
				}
				if len(commands) != 1 {
					failures = append(failures, fmt.Errorf("workflow run block must contain one Make invocation in %s:%d", path, index+1))
				} else if err := validateWorkflowMakeCommand(commands[0].text, makeTargets); err != nil {
					failures = append(failures, fmt.Errorf("workflow run command in %s:%d: %w", path, commands[0].line, err))
				}
			} else if err := validateWorkflowMakeCommand(value, makeTargets); err != nil {
				failures = append(failures, fmt.Errorf("workflow run command in %s:%d: %w", path, index+1, err))
			}
		}
	}
	if hasRun && !hasPOSIXShell {
		failures = append(failures, fmt.Errorf("workflow with run commands lacks an explicit plain-sh runner: %s", path))
	}
	return errors.Join(failures...)
}

type workflowCommand struct {
	line int
	text string
}

func validateWorkflowMakeCommand(command string, makeTargets map[string]struct{}) error {
	fields := strings.Fields(command)
	if len(fields) != 2 || fields[0] != "make" || !validMakeTargetName(fields[1]) {
		return errors.New("must be exactly `make <target>`")
	}
	if _, found := makeTargets[fields[1]]; !found {
		return fmt.Errorf("unknown Make target %q", fields[1])
	}
	return nil
}

func collectMakeTargets(root string, paths []string) (map[string]struct{}, error) {
	targets := make(map[string]struct{})
	for _, path := range paths {
		if !isMakefile(path) {
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", path, err)
		}
		for _, line := range strings.Split(string(data), "\n") {
			if line == "" || strings.ContainsRune("\t ", rune(line[0])) {
				continue
			}
			left, _, found := strings.Cut(line, ":")
			if !found {
				continue
			}
			for _, target := range strings.Fields(left) {
				if validMakeTargetName(target) && !strings.HasPrefix(target, ".") {
					targets[target] = struct{}{}
				}
			}
		}
	}
	return targets, nil
}

func validMakeTargetName(target string) bool {
	if target == "" {
		return false
	}
	for _, character := range target {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			strings.ContainsRune("_./-", character) {
			continue
		}
		return false
	}
	return true
}

func containsScriptRuntimeCommand(command string) bool {
	for _, field := range strings.Fields(command) {
		word := strings.Trim(field, "'\"`(){}[];,|&")
		if isScriptRuntimeWord(word) {
			return true
		}
	}
	return false
}

func isScriptRuntimeWord(word string) bool {
	base := commandBase(word)
	if strings.HasPrefix(base, "python3.") {
		base = "python3"
	}
	_, found := scriptRuntimeNames()[base]
	return found
}

func commandBase(value string) string {
	normalized := strings.ReplaceAll(strings.TrimSpace(value), `\`, "/")
	base := strings.ToLower(path.Base(normalized))
	return strings.TrimSuffix(base, ".exe")
}

func shebangUsesScriptRuntime(line string) bool {
	fields := strings.Fields(strings.TrimSpace(strings.TrimPrefix(line, "#!")))
	if len(fields) == 0 {
		return false
	}
	interpreter := fields[0]
	if commandBase(interpreter) == "env" {
		for _, field := range fields[1:] {
			if strings.HasPrefix(field, "-") || strings.Contains(field, "=") {
				continue
			}
			interpreter = field
			break
		}
	}
	return isScriptRuntimeWord(interpreter)
}

func rejectGoScriptExecution(path string, data []byte) error {
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
		execCall := isExec && selector.Sel.Name == "Command"
		execContextCall := isExec && selector.Sel.Name == "CommandContext"
		syscallCall := isSyscall && selector.Sel.Name == "Exec"
		commandIndex := 0
		if execContextCall {
			commandIndex = 1
		}
		if (execCall || execContextCall || syscallCall) && len(call.Args) > commandIndex {
			if command, ok := constantString(call.Args[commandIndex]); ok &&
				isScriptRuntimeWord(command) {
				found = true
				return false
			}
		}
		return true
	})
	if found {
		return fmt.Errorf("script-runtime process execution in %s", path)
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
