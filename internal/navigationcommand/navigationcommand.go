// Package navigationcommand validates standalone ScopeSifter commands found
// in launcher transcripts.
package navigationcommand

import (
	"fmt"
	"regexp"
	"strings"
)

var scopeSifterPattern = regexp.MustCompile(
	`(?:^|[\n\r\t ;|&('"` + "`" + `])(?:[^ \t\n\r;|&=('"` + "`" + `]+/)?` +
		`scopesifter(?:\.bin)?\s+(changed|find|inspect|outline)(?:\s|$)`,
)

var jsonPattern = regexp.MustCompile(`(?:^|[ \t])--json(?:[ \t]|$)`)

// ValidatedScopeSifterSubcommand returns the subcommand of a safe standalone
// ScopeSifter navigation command. An empty result means no navigation
// invocation is lexically visible.
func ValidatedScopeSifterSubcommand(command string) (string, error) {
	count, err := validateScopeSifterCommand(command)
	if err != nil || count == 0 {
		return "", err
	}
	match := scopeSifterPattern.FindStringSubmatch(command)
	if len(match) != 2 {
		return "", fmt.Errorf("scopesifter navigation subcommand is ambiguous")
	}
	return match[1], nil
}

func validateScopeSifterCommand(command string) (int, error) {
	matches := scopeSifterPattern.FindAllStringSubmatchIndex(command, -1)
	if len(matches) == 0 {
		return 0, nil
	}
	if len(matches) != 1 {
		return len(matches), fmt.Errorf("scopesifter navigation must be one standalone invocation")
	}
	body, ok := standaloneShellBody(command)
	if !ok {
		return 1, fmt.Errorf("scopesifter navigation is not a standalone shell command")
	}
	bodyMatches := scopeSifterPattern.FindAllStringSubmatchIndex(body, -1)
	if len(bodyMatches) != 1 || bodyMatches[0][0] != 0 {
		return 1, fmt.Errorf("scopesifter navigation is not the executed shell command")
	}
	if strings.Count(body, "scopesifter") != 1 ||
		strings.ContainsAny(body, "\n\r;&|`<>$*?[]{}\\()~#") {
		return 1, fmt.Errorf("scopesifter navigation contains shell composition or expansion")
	}
	if len(jsonPattern.FindAllStringIndex(body, -1)) != 1 {
		return 1, fmt.Errorf("scopesifter navigation must request exactly one JSON response")
	}
	return 1, nil
}

func standaloneShellBody(command string) (string, bool) {
	if strings.HasPrefix(command, "scopesifter ") ||
		strings.HasPrefix(command, "scopesifter.bin ") {
		return command, strings.TrimSpace(command) == command
	}
	for _, prefix := range []string{
		"/usr/bin/zsh -lc ",
		"/bin/zsh -lc ",
		"/bin/sh -lc ",
		"zsh -lc ",
		"sh -lc ",
	} {
		if !strings.HasPrefix(command, prefix) {
			continue
		}
		quoted := strings.TrimPrefix(command, prefix)
		if len(quoted) < 2 {
			return "", false
		}
		quote := quoted[0]
		if (quote != '\'' && quote != '"') || quoted[len(quoted)-1] != quote {
			return "", false
		}
		body := quoted[1 : len(quoted)-1]
		return body, strings.TrimSpace(body) == body
	}
	return "", false
}
