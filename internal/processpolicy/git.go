package processpolicy

import (
	"fmt"
	"net/url"
	"path/filepath"
	"strings"
)

// ValidateGit rejects Git invocation forms that can delegate execution to an
// arbitrary program. Callers must still provide an isolated Git environment
// and authenticate the Git executable when the invocation is security-bound.
func ValidateGit(arguments ...string) error {
	if len(arguments) == 0 {
		return fmt.Errorf("process policy: Git subcommand is required")
	}
	if len(arguments) == 1 && arguments[0] == "--version" {
		return nil
	}
	index := 0
	for index < len(arguments) {
		switch argument := arguments[index]; {
		case argument == "--literal-pathspecs":
			index++
		case argument == "--no-pager":
			index++
		case argument == "--version" && index == len(arguments)-1:
			return nil
		case strings.HasPrefix(argument, "--git-dir=") ||
			strings.HasPrefix(argument, "--work-tree="):
			if strings.HasSuffix(argument, "=") {
				return fmt.Errorf("process policy: empty Git repository path option")
			}
			index++
		case argument == "-C":
			if index+1 >= len(arguments) || arguments[index+1] == "" {
				return fmt.Errorf("process policy: malformed Git -C option")
			}
			index += 2
		case argument == "-c":
			if index+1 >= len(arguments) || !safeGitConfig(arguments[index+1]) {
				return fmt.Errorf("process policy: unapproved Git configuration")
			}
			index += 2
		case strings.HasPrefix(argument, "-c"):
			if !safeGitConfig(strings.TrimPrefix(argument, "-c")) {
				return fmt.Errorf("process policy: unapproved Git configuration")
			}
			index++
		case strings.HasPrefix(argument, "-"):
			return fmt.Errorf("process policy: unapproved Git global option %q", argument)
		default:
			return validateGitSubcommand(argument, arguments[index+1:])
		}
	}
	return fmt.Errorf("process policy: Git subcommand is required")
}

func safeGitConfig(value string) bool {
	_, approved := map[string]struct{}{
		"color.ui=false":              {},
		"core.fsmonitor=false":        {},
		"core.pager=cat":              {},
		"core.quotePath=true":         {},
		"core.untrackedCache=false":   {},
		"diff.external=":              {},
		"diff.ignoreSubmodules=dirty": {},
		"diff.renameLimit=20000":      {},
		"diff.renames=true":           {},
		"merge.renameLimit=20000":     {},
	}[value]
	return approved
}

func validateGitSubcommand(command string, arguments []string) error {
	if _, approved := safeGitSubcommands[command]; !approved {
		return fmt.Errorf("process policy: unapproved Git subcommand %q", command)
	}
	if command == "config" && !validReadOnlyGitConfig(arguments) {
		return fmt.Errorf("process policy: Git configuration mutation is forbidden")
	}
	if command == "replace" && (len(arguments) != 1 || arguments[0] != "--list") {
		return fmt.Errorf("process policy: only Git replace --list is approved")
	}
	if command == "hash-object" &&
		(!containsExact(arguments, "-w") || !containsExact(arguments, "--no-filters")) {
		return fmt.Errorf("process policy: Git hash-object must write without filters")
	}
	if command == "clone" && !validBoundedGitClone(arguments) {
		return fmt.Errorf("process policy: Git clone arguments are outside the bounded role")
	}
	if command == "cat-file" {
		for _, argument := range arguments {
			if argument == "--filters" || strings.HasPrefix(argument, "--filters=") {
				return fmt.Errorf("process policy: Git cat-file filters are forbidden")
			}
		}
	}
	for _, argument := range arguments {
		longName := argument
		if separator := strings.IndexByte(longName, '='); separator >= 0 {
			longName = longName[:separator]
		}
		switch {
		case gitSignatureFormat(argument):
			return fmt.Errorf("process policy: Git signature-verifying format %q is forbidden", argument)
		case argument == "-c" || strings.HasPrefix(argument, "-c") && len(argument) > 2:
			return fmt.Errorf("process policy: Git subcommand configuration is forbidden")
		case argument == "--config-env" || strings.HasPrefix(argument, "--config-env="):
			return fmt.Errorf("process policy: Git config environment is forbidden")
		case argument == "--exec-path" || strings.HasPrefix(argument, "--exec-path="):
			return fmt.Errorf("process policy: Git executable-path override is forbidden")
		case acceptedLongOptionPrefix(longName, "--ext-diff", len("--ext-diff")) ||
			acceptedLongOptionPrefix(longName, "--textconv", 3) ||
			acceptedLongOptionPrefix(longName, "--open-files-in-pager", 4) ||
			acceptedLongOptionPrefix(longName, "--show-signature", len("--show-signature")):
			return fmt.Errorf("process policy: Git external-program option %q is forbidden", argument)
		case command == "grep" && (argument == "-O" || strings.HasPrefix(argument, "-O")):
			return fmt.Errorf("process policy: Git grep pager option %q is forbidden", argument)
		}
	}
	return nil
}

func gitSignatureFormat(argument string) bool {
	return strings.Contains(argument, "%G") || strings.Contains(argument, "%(signature:")
}

func acceptedLongOptionPrefix(candidate, option string, minimum int) bool {
	return strings.HasPrefix(candidate, "--") && len(candidate) >= minimum &&
		len(candidate) <= len(option) && strings.HasPrefix(option, candidate)
}

func validBoundedGitClone(arguments []string) bool {
	if len(arguments) != 6 || arguments[0] != "--depth" || arguments[1] != "1" ||
		arguments[2] != "--no-tags" || arguments[3] != "--" ||
		!cleanAbsoluteProcessPath(arguments[5]) {
		return false
	}
	return safeGitCloneSource(arguments[4])
}

func safeGitCloneSource(source string) bool {
	if source == "" || strings.HasPrefix(source, "-") || strings.Contains(source, "::") ||
		strings.ContainsAny(source, "\x00\r\n") {
		return false
	}
	if filepath.IsAbs(source) {
		return filepath.Clean(source) == source
	}
	parsed, err := url.Parse(source)
	if err != nil {
		return false
	}
	if parsed.Scheme == "https" {
		return parsed.Host != "" && parsed.User == nil && parsed.RawQuery == "" && parsed.Fragment == ""
	}
	if parsed.Scheme != "" || parsed.Host != "" || strings.Contains(source, ":") {
		return false
	}
	return filepath.Clean(source) == source && source != "." && source != ".." &&
		!strings.HasPrefix(source, ".."+string(filepath.Separator))
}

func cleanAbsoluteProcessPath(value string) bool {
	return filepath.IsAbs(value) && filepath.Clean(value) == value
}

func validReadOnlyGitConfig(arguments []string) bool {
	filtered := make([]string, 0, len(arguments))
	for _, argument := range arguments {
		switch argument {
		case "--local", "--worktree", "--includes", "--no-includes", "--name-only", "--show-origin", "--show-scope", "--null", "-z":
			continue
		default:
			filtered = append(filtered, argument)
		}
	}
	if len(filtered) == 1 {
		return filtered[0] == "--list" || filtered[0] == "-l"
	}
	if len(filtered) == 2 {
		switch filtered[0] {
		case "--get", "--get-all", "--get-regexp":
			return filtered[1] != "" && !strings.HasPrefix(filtered[1], "-")
		}
	}
	return len(filtered) == 3 && filtered[0] == "--get-urlmatch" &&
		filtered[1] != "" && filtered[2] != ""
}

func containsExact(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

var safeGitSubcommands = map[string]struct{}{
	"apply": {}, "cat-file": {}, "clone": {}, "config": {}, "diff": {}, "for-each-ref": {},
	"grep": {}, "hash-object": {}, "ls-files": {}, "ls-tree": {}, "merge-base": {},
	"read-tree": {}, "replace": {}, "rev-parse": {}, "show": {}, "status": {},
	"update-index": {}, "write-tree": {},
}
