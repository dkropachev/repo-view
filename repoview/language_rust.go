package repoview

import "strings"

type rustLanguage struct {
	languageDefinition
}

func newRustLanguage() rustLanguage {
	return rustLanguage{newLanguageDefinition(
		"rust",
		[]string{`^(?:pub(?:\([^)]*\))?\s+)?(?:async\s+)?(?:fn|struct|enum|trait|impl)\s+([A-Za-z_][A-Za-z0-9_]*)\b`},
		braceScopeResolver,
		rustImports,
		commentStyleCLike,
		false,
	)}
}

func rustImports(lines []string) (int, int, bool) {
	start, end := 0, 0
	for idx, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "use ") {
			if start == 0 {
				start = idx + 1
			}
			end = idx + 1
		}
	}
	return start, end, start > 0 && end >= start
}
