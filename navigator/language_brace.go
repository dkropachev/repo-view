package navigator

import "strings"

type braceLanguage struct {
	structuralLines []string
	languageDefinition
}

func newBraceLanguage(name string) braceLanguage {
	return braceLanguage{languageDefinition: newLanguageDefinition(
		name,
		[]string{
			`^(?:function|class)\s+([A-Za-z_][A-Za-z0-9_]*)\b`,
			`^.*\b([A-Za-z_][A-Za-z0-9_]*)\s*\([^;]*\)\s*\{?\s*$`,
		},
		braceScopeResolver,
		nil,
		commentStyleCLike,
		false,
	)}
}

func (b braceLanguage) prepareSource(lines []string) languageBackend {
	b.structuralLines = strings.Split(
		withoutBraceStrings(dropCLikeComments(strings.Join(lines, "\n"))),
		"\n",
	)
	return b
}

func (b braceLanguage) enclosingScope(lines []string, lineNo int) (int, int) {
	if len(b.structuralLines) == len(lines) {
		if start, end, ok := braceScopeFromStructural(
			lines, b.structuralLines, lineNo,
		); ok {
			return start, end
		}
		return lineNo, lineNo
	}
	return b.languageDefinition.enclosingScope(lines, lineNo)
}
