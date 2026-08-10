package tokenbench

import (
	"errors"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"
)

// ValidateTreatmentNeutrality rejects task text that names, recommends, or
// forbids a navigation/tool mechanism. Tokenbench measures availability of
// repo_view; a prompt-side tool policy would be a second treatment even when
// its bytes were copied to both arms.
func ValidateTreatmentNeutrality(
	developerInstructions string,
	prompt []byte,
) error {
	if err := validateNeutralText(
		"developer_instructions",
		developerInstructions,
	); err != nil {
		return err
	}
	if !utf8.Valid(prompt) {
		return errors.New("prompt must be valid UTF-8")
	}
	return validateNeutralText("prompt", string(prompt))
}

func validateNeutralText(label, value string) error {
	tokens := neutralityTokens(value)
	for index, token := range tokens {
		compact := strings.NewReplacer("_", "", "-", "").Replace(token)
		switch {
		case compact == "repoview" || compact == "mcp" ||
			compact == "modelcontextprotocol":
			return fmt.Errorf(
				"%s names the benchmark treatment %q",
				label,
				token,
			)
		case index+1 < len(tokens) && token == "repo" && tokens[index+1] == "view":
			return fmt.Errorf("%s names the repo_view treatment", label)
		case index+2 < len(tokens) && token == "model" &&
			tokens[index+1] == "context" && tokens[index+2] == "protocol":
			return fmt.Errorf("%s names the MCP treatment", label)
		case index+2 < len(tokens) && token == "m" &&
			tokens[index+1] == "c" && tokens[index+2] == "p":
			return fmt.Errorf("%s names the MCP treatment", label)
		}
	}

	imperatives := map[string]struct{}{
		"call": {}, "consult": {}, "execute": {}, "invoke": {},
		"prefer": {}, "query": {}, "rely": {}, "run": {}, "use": {},
	}
	mechanisms := map[string]struct{}{
		"cli": {}, "command": {}, "commands": {}, "git": {},
		"navigation": {}, "navigator": {}, "shell": {}, "terminal": {},
		"tool": {}, "tools": {},
	}
	for left, token := range tokens {
		if _, imperative := imperatives[token]; !imperative {
			continue
		}
		end := left + 7
		if end > len(tokens) {
			end = len(tokens)
		}
		for right := left + 1; right < end; right++ {
			if _, mechanism := mechanisms[tokens[right]]; mechanism {
				return fmt.Errorf(
					"%s contains a tool-use policy near %q and %q",
					label,
					token,
					tokens[right],
				)
			}
		}
	}
	for index, token := range tokens {
		if token != "available" && token != "access" {
			continue
		}
		end := index + 5
		if end > len(tokens) {
			end = len(tokens)
		}
		for next := index + 1; next < end; next++ {
			if tokens[next] == "tool" || tokens[next] == "tools" {
				return fmt.Errorf("%s contains an available-tool hint", label)
			}
		}
	}
	return nil
}

func neutralityTokens(value string) []string {
	var tokens []string
	start := -1
	for index, character := range value {
		word := unicode.IsLetter(character) || unicode.IsDigit(character) ||
			character == '_' || character == '-'
		if word && start < 0 {
			start = index
		}
		if !word && start >= 0 {
			tokens = append(tokens, strings.ToLower(value[start:index]))
			start = -1
		}
	}
	if start >= 0 {
		tokens = append(tokens, strings.ToLower(value[start:]))
	}
	return tokens
}
