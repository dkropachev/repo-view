package repoview

import (
	"regexp"
	"strings"
)

var signatureEndPattern = regexp.MustCompile(`[;{}]\s*$`)

func braceScope(lines []string, lineNo int) (int, int, bool) {
	structuralLines := strings.Split(
		withoutBraceStrings(dropCLikeComments(strings.Join(lines, "\n"))),
		"\n",
	)
	return braceScopeFromStructural(lines, structuralLines, lineNo)
}

func braceScopeFromStructural(
	lines, structuralLines []string,
	lineNo int,
) (int, int, bool) {
	idx := lineNo - 1
	if idx < 0 || idx >= len(lines) || len(structuralLines) != len(lines) {
		return 0, 0, false
	}
	braceIdx, ok := findBraceStart(structuralLines, idx)
	if !ok {
		return 0, 0, false
	}
	startIdx := signatureStart(lines, braceIdx)

	depth := 0
	seenOpen := false
	for pos := startIdx; pos < len(structuralLines); pos++ {
		for _, char := range structuralLines[pos] {
			switch char {
			case '{':
				depth++
				seenOpen = true
			case '}':
				depth--
				if seenOpen && depth <= 0 {
					if startIdx+1 <= lineNo && lineNo <= pos+1 {
						return startIdx + 1, pos + 1, true
					}
					return 0, 0, false
				}
			}
		}
	}
	if startIdx+1 <= lineNo {
		return startIdx + 1, lineNo, true
	}
	return 0, 0, false
}

func findBraceStart(lines []string, idx int) (int, bool) {
	depth := 0
	for pos := idx; pos >= 0; pos-- {
		line := lines[pos]
		for i := len(line) - 1; i >= 0; i-- {
			switch line[i] {
			case '}':
				depth++
			case '{':
				if depth == 0 {
					return pos, true
				}
				depth--
				if depth == 0 {
					return pos, true
				}
			}
		}
	}
	return 0, false
}

func signatureStart(lines []string, braceLine int) int {
	start := braceLine
	for pos := braceLine - 1; pos >= 0; pos-- {
		stripped := strings.TrimSpace(lines[pos])
		if stripped == "" {
			break
		}
		if strings.HasPrefix(stripped, "//") || strings.HasPrefix(stripped, "/*") ||
			strings.HasPrefix(stripped, "*") || strings.HasPrefix(stripped, "#") {
			start = pos
			continue
		}
		if signatureEndPattern.MatchString(stripped) {
			break
		}
		start = pos
	}
	return start
}

func withoutStrings(line string) string {
	return withoutQuotedLiterals(line, false)
}

func withoutBraceStrings(source string) string {
	return withoutQuotedLiterals(source, true)
}

func withoutQuotedLiterals(source string, includeBackticks bool) string {
	var out strings.Builder
	inSingle := false
	inDouble := false
	inBacktick := false
	escaped := false
	for _, char := range source {
		if char == '\n' {
			out.WriteRune(char)
			escaped = false
			if !inBacktick {
				inSingle = false
				inDouble = false
			}
			continue
		}
		if escaped {
			escaped = false
			out.WriteRune(' ')
			continue
		}
		if char == '\\' {
			escaped = true
			out.WriteRune(' ')
			continue
		}
		if char == '\'' && !inDouble && !inBacktick {
			inSingle = !inSingle
			out.WriteRune(' ')
			continue
		}
		if char == '"' && !inSingle && !inBacktick {
			inDouble = !inDouble
			out.WriteRune(' ')
			continue
		}
		if includeBackticks && char == '`' && !inSingle && !inDouble {
			inBacktick = !inBacktick
			out.WriteRune(' ')
			continue
		}
		if inSingle || inDouble || inBacktick {
			out.WriteRune(' ')
			continue
		}
		out.WriteRune(char)
	}
	return out.String()
}
