// Package processpolicy rejects explicit shell and script-runtime process
// invocations before they reach an operating-system process API.
package processpolicy

import (
	"fmt"
	"strings"
	"unicode"
)

// ViolationKind identifies why an invocation was rejected.
type ViolationKind string

const (
	// InvalidExecutable means that no executable name was supplied.
	InvalidExecutable ViolationKind = "invalid executable"
	// ScriptRuntime means that a value explicitly names a shell or script runtime.
	ScriptRuntime ViolationKind = "script runtime"
	// ScriptFile means that a value explicitly names a file with a script suffix.
	ScriptFile ViolationKind = "script file"

	// ExecutableArgumentIndex distinguishes the executable from an argument.
	ExecutableArgumentIndex = -1
)

// ViolationError describes the first prohibited reference in an invocation.
// ArgumentIndex is ExecutableArgumentIndex for the executable and is otherwise
// the zero-based index in the argument slice.
type ViolationError struct {
	Kind          ViolationKind
	Value         string
	Match         string
	ArgumentIndex int
}

// Error implements error.
func (e *ViolationError) Error() string {
	location := "executable"
	if e.ArgumentIndex != ExecutableArgumentIndex {
		location = fmt.Sprintf("argument %d", e.ArgumentIndex)
	}
	if e.Kind == InvalidExecutable {
		return fmt.Sprintf("process policy: %s is empty", location)
	}
	return fmt.Sprintf(
		"process policy: %s %q refers to prohibited %s %q",
		location,
		e.Value,
		e.Kind,
		e.Match,
	)
}

// ValidateExecutable checks only an executable name. It is intended for
// boundaries whose argument values are opaque data, such as model prompts.
func ValidateExecutable(executable string) error {
	return Validate(executable)
}

// ProhibitedReference reports whether value names a script runtime, a script
// file, or a script automation configuration according to this package's
// canonical lexical policy.
func ProhibitedReference(value string) bool {
	_, _, prohibited := prohibitedReference(value)
	return prohibited
}

// Validate checks an executable and its arguments for explicit shell, script
// runtime, and script-file references. The check is lexical: callers that run
// aliases, symlinks, package-manager hooks, or other dispatchers must also bind
// those executables to trusted identities and constrain their command grammar.
func Validate(executable string, arguments ...string) error {
	if strings.TrimSpace(executable) == "" {
		return &ViolationError{
			Kind:          InvalidExecutable,
			ArgumentIndex: ExecutableArgumentIndex,
			Value:         executable,
		}
	}
	if kind, match, prohibited := prohibitedReference(executable); prohibited {
		return &ViolationError{
			Kind:          kind,
			ArgumentIndex: ExecutableArgumentIndex,
			Value:         executable,
			Match:         match,
		}
	}
	for index, argument := range arguments {
		if kind, match, prohibited := prohibitedArgument(argument); prohibited {
			return &ViolationError{
				Kind:          kind,
				ArgumentIndex: index,
				Value:         argument,
				Match:         match,
			}
		}
	}
	return nil
}

func prohibitedArgument(argument string) (ViolationKind, string, bool) {
	// GNU env accepts an interpreter immediately after its -S option. Keep
	// this explicit because the ordinary delimiter scan intentionally does not
	// interpret arbitrary short-option clusters.
	trimmed := strings.TrimLeftFunc(argument, unicode.IsSpace)
	if len(trimmed) > 2 && trimmed[0] == '-' && trimmed[1] == 'S' {
		if kind, match, prohibited := prohibitedReference(trimmed[2:]); prohibited {
			return kind, match, true
		}
	}

	for _, splitWordJoiners := range []bool{false, true} {
		for _, candidate := range referenceCandidates(argument, splitWordJoiners) {
			if kind, match, prohibited := prohibitedReference(candidate); prohibited {
				return kind, match, true
			}
		}
	}
	return "", "", false
}

func prohibitedReference(reference string) (ViolationKind, string, bool) {
	base := normalizedBase(reference)
	if base == "" {
		return "", "", false
	}
	if isRuntimeName(base) {
		return ScriptRuntime, base, true
	}
	if suffix, found := scriptSuffix(base); found {
		return ScriptFile, suffix, true
	}
	if _, found := scriptConfigurationNames[base]; found {
		return ScriptFile, base, true
	}
	return "", "", false
}

func normalizedBase(reference string) string {
	value := strings.ToLower(strings.TrimSpace(reference))
	value = strings.ReplaceAll(value, `\`, "/")
	value = strings.TrimRight(value, "/")
	if separator := strings.LastIndexByte(value, '/'); separator >= 0 {
		value = value[separator+1:]
	}
	value = strings.Trim(value, `"'`)
	value = strings.TrimSuffix(value, ".exe")
	return value
}

func isRuntimeName(name string) bool {
	if _, found := runtimeNames[name]; found {
		return true
	}
	for _, prefix := range versionedRuntimePrefixes {
		if strings.HasPrefix(name, prefix) && isVersionTail(name[len(prefix):]) {
			return true
		}
	}
	return false
}

func isVersionTail(tail string) bool {
	if tail == "" || tail[0] < '0' || tail[0] > '9' {
		return false
	}
	for index, character := range tail {
		if (character >= '0' && character <= '9') || character == '.' {
			continue
		}
		if index == len(tail)-1 && strings.ContainsRune("dmstu", character) {
			continue
		}
		return false
	}
	return true
}

func scriptSuffix(name string) (string, bool) {
	for _, suffix := range scriptSuffixes {
		if strings.HasSuffix(name, suffix) {
			return suffix, true
		}
	}
	return "", false
}

func referenceCandidates(value string, splitWordJoiners bool) []string {
	return strings.FieldsFunc(value, func(character rune) bool {
		if !splitWordJoiners && (character == '-' || character == '_') {
			return false
		}
		return !unicode.IsLetter(character) &&
			!unicode.IsNumber(character) &&
			character != '.' &&
			character != '/' &&
			character != '\\'
	})
}

var runtimeNames = map[string]struct{}{
	"amm":            {},
	"ash":            {},
	"awk":            {},
	"babashka":       {},
	"bash":           {},
	"bb":             {},
	"bun":            {},
	"bunx":           {},
	"cscript":        {},
	"csh":            {},
	"csi":            {},
	"dash":           {},
	"dart":           {},
	"deno":           {},
	"dotnet-script":  {},
	"d8":             {},
	"elixir":         {},
	"elvish":         {},
	"escript":        {},
	"expect":         {},
	"fish":           {},
	"fsi":            {},
	"gawk":           {},
	"groovy":         {},
	"groovysh":       {},
	"guile":          {},
	"iex":            {},
	"ipython":        {},
	"ironpython":     {},
	"jjs":            {},
	"jruby":          {},
	"jsc":            {},
	"js":             {},
	"julia":          {},
	"jython":         {},
	"kotlin":         {},
	"ksh":            {},
	"lua":            {},
	"luajit":         {},
	"mawk":           {},
	"micropython":    {},
	"mksh":           {},
	"nawk":           {},
	"node":           {},
	"nodejs":         {},
	"nu":             {},
	"nushell":        {},
	"oil":            {},
	"osascript":      {},
	"osh":            {},
	"pdksh":          {},
	"perl":           {},
	"php":            {},
	"powershell":     {},
	"powershell_ise": {},
	"pwsh":           {},
	"py":             {},
	"pypy":           {},
	"python":         {},
	"pythonw":        {},
	"qjs":            {},
	"qjs-ng":         {},
	"r":              {},
	"racket":         {},
	"rscript":        {},
	"ruby":           {},
	"sed":            {},
	"sh":             {},
	"tcsh":           {},
	"tclsh":          {},
	"ts-node":        {},
	"tsx":            {},
	"wscript":        {},
	"wish":           {},
	"xonsh":          {},
	"ysh":            {},
	"zsh":            {},
	"clisp":          {},
	"clojure":        {},
	"cmd":            {},
	"cmd.com":        {},
	"command.com":    {},
	"pypy3":          {},
	"sbcl":           {},
	"scala":          {},
	"scala-cli":      {},
	"scheme":         {},
	"ts-node-script": {},
}

var versionedRuntimePrefixes = []string{
	"groovy",
	"guile",
	"ipython",
	"jruby",
	"julia",
	"lua",
	"luajit",
	"node",
	"nodejs",
	"perl",
	"php",
	"powershell",
	"pwsh",
	"pypy",
	"python",
	"pythonw",
	"rscript",
	"ruby",
	"tclsh",
	"wish",
}

var scriptSuffixes = []string{
	".applescript",
	".ash",
	".awk",
	".bash",
	".bat",
	".bats",
	".cjs",
	".clj",
	".cljs",
	".cmd",
	".command",
	".csh",
	".csx",
	".cts",
	".dart",
	".dash",
	".envrc",
	".exs",
	".fish",
	".fsx",
	".groovy",
	".gsh",
	".gvy",
	".gy",
	".js",
	".jsx",
	".jl",
	".ksh",
	".kts",
	".lua",
	".mjs",
	".mksh",
	".mts",
	".nu",
	".pex",
	".phar",
	".php",
	".pl",
	".pm",
	".ps1",
	".psd1",
	".psm1",
	".py",
	".pyc",
	".pyw",
	".pyz",
	".r",
	".rb",
	".rbw",
	".rmd",
	".sc",
	".sh",
	".tcl",
	".tcsh",
	".ts",
	".tsx",
	".vbe",
	".vbs",
	".wsf",
	".wsh",
	".zsh",
}

var scriptConfigurationNames = map[string]struct{}{
	".bash_login":   {},
	".bash_profile": {},
	".bashrc":       {},
	".kshrc":        {},
	".profile":      {},
	".zlogin":       {},
	".zprofile":     {},
	".zshenv":       {},
	".zshrc":        {},
}
