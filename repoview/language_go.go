package repoview

import "strings"

type goLanguage struct {
	languageDefinition
}

func newGoLanguage() goLanguage {
	return goLanguage{newLanguageDefinition(
		"go",
		[]string{
			`^func\s+(?:\([^)]*\)\s*)?([A-Za-z_][A-Za-z0-9_]*)\b`,
			`^type\s+([A-Za-z_][A-Za-z0-9_]*)\b`,
			`^([A-Za-z_][A-Za-z0-9_]*)\s+(?:struct|interface)\b`,
		},
		goScope,
		goImports,
		commentStyleCLike,
		false,
	)}
}

func goScope(lines []string, lineNo int) (int, int) {
	if start, end, ok := goDeclarationScope(lines, lineNo); ok {
		return start, end
	}
	return braceScopeResolver(lines, lineNo)
}

func goImports(lines []string) (int, int, bool) {
	start, end := 0, 0
	inBlock := false
	for idx, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "import (") {
			start = idx + 1
			inBlock = true
			continue
		}
		if start == 0 && strings.HasPrefix(trimmed, "import ") {
			start = idx + 1
			end = idx + 1
		}
		if inBlock && trimmed == ")" {
			end = idx + 1
			break
		}
	}
	return start, end, start > 0 && end >= start
}
