package processpolicy

import (
	"bytes"
	"errors"
	"fmt"
	"slices"
	"strings"
)

const maximumGitConfigurationBytes = 1 << 20

// GitRepositoryConfigArguments is the read-only preflight that must precede a
// Git command which can inspect worktree bytes. With the caller's isolated
// global/system environment it reports local and active per-worktree keys.
// Includes are deliberately disabled and their keys are rejected, preventing
// the preflight itself from opening attacker-selected paths.
func GitRepositoryConfigArguments() []string {
	return []string{"config", "--no-includes", "--null", "--name-only", "--list"}
}

// IsGitRepositoryConfigQuery reports whether arguments exactly match the
// bounded preflight query.
func IsGitRepositoryConfigQuery(arguments []string) bool {
	return slices.Equal(arguments, GitRepositoryConfigArguments())
}

// ValidateGitWorktreeConfig rejects local configuration keys through which a
// read-only worktree operation can start another program.
func ValidateGitWorktreeConfig(output []byte) error {
	if len(output) > maximumGitConfigurationBytes {
		return errors.New("process policy: local Git configuration is too large")
	}
	if len(output) != 0 && output[len(output)-1] != 0 {
		return errors.New("process policy: local Git configuration is not NUL terminated")
	}
	for record := range bytes.SplitSeq(output, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		name := strings.ToLower(string(record))
		if !validGitConfigName(name) {
			return fmt.Errorf("process policy: malformed local Git configuration name %q", record)
		}
		if delegatingGitConfigName(name) {
			return fmt.Errorf("process policy: local Git configuration %q can delegate execution", name)
		}
	}
	return nil
}

func validGitConfigName(name string) bool {
	if name == "" || strings.ContainsAny(name, "\x00\r\n\t =") {
		return false
	}
	for _, part := range strings.Split(name, ".") {
		if part == "" {
			return false
		}
	}
	return true
}

func delegatingGitConfigName(name string) bool {
	parts := strings.Split(name, ".")
	last := parts[len(parts)-1]
	switch {
	case strings.HasPrefix(name, "alias."),
		name == "core.attributesfile",
		name == "core.excludesfile",
		name == "core.hookspath",
		name == "core.gitproxy",
		name == "core.sshcommand",
		name == "core.worktree",
		name == "diff.external",
		name == "include.path",
		strings.HasPrefix(name, "includeif.") && last == "path",
		name == "interactive.difffilter",
		name == "sequence.editor",
		name == "core.editor",
		name == "core.askpass",
		name == "credential.helper",
		strings.HasPrefix(name, "credential.") && last == "helper",
		strings.HasPrefix(name, "filter.") && (last == "clean" || last == "smudge" || last == "process"),
		strings.HasPrefix(name, "diff.") && (last == "command" || last == "textconv"),
		strings.HasPrefix(name, "merge.") && last == "driver",
		strings.HasPrefix(name, "difftool.") && (last == "cmd" || last == "path"),
		strings.HasPrefix(name, "mergetool.") && (last == "cmd" || last == "path"),
		strings.HasPrefix(name, "gpg.") && last == "program",
		strings.HasPrefix(name, "pager."):
		return true
	default:
		return false
	}
}
