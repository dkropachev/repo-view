package repoview

import (
	"regexp"
	"strings"
)

func braceScope(lines []string, lineNo int) (int, int, bool) {
	idx := lineNo - 1
	startIdx, ok := findBraceStart(lines, idx)
	if !ok {
		return 0, 0, false
	}

	depth := 0
	seenOpen := false
	for pos := startIdx; pos < len(lines); pos++ {
		for _, char := range withoutStrings(lines[pos]) {
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
		line := withoutStrings(lines[pos])
		for i := len(line) - 1; i >= 0; i-- {
			switch line[i] {
			case '}':
				depth++
			case '{':
				if depth == 0 {
					return signatureStart(lines, pos), true
				}
				depth--
				if depth == 0 {
					return signatureStart(lines, pos), true
				}
			}
		}
	}
	return 0, false
}

func signatureStart(lines []string, braceLine int) int {
	start := braceLine
	endPattern := regexp.MustCompile(`[;{}]\s*$`)
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
		if endPattern.MatchString(stripped) {
			break
		}
		start = pos
	}
	return start
}

func indentScope(lines []string, lineNo int) (int, int) {
	idx := lineNo - 1
	indent := leadingSpaces(lines[idx])

	start := idx
	for pos := idx; pos >= 0; pos-- {
		if strings.TrimSpace(lines[pos]) == "" {
			continue
		}
		current := leadingSpaces(lines[pos])
		if pos != idx && current < indent {
			start = pos
			break
		}
		start = pos
	}

	end := idx
	for pos := idx + 1; pos < len(lines); pos++ {
		if strings.TrimSpace(lines[pos]) == "" {
			end = pos
			continue
		}
		current := leadingSpaces(lines[pos])
		if current <= indent {
			break
		}
		end = pos
	}
	return start + 1, end + 1
}

func leadingSpaces(line string) int {
	count := 0
	for _, char := range line {
		if char != ' ' && char != '\t' {
			break
		}
		count++
	}
	return count
}

func withoutStrings(line string) string {
	var out strings.Builder
	inSingle := false
	inDouble := false
	escaped := false
	for _, char := range line {
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
		if char == '\'' && !inDouble {
			inSingle = !inSingle
			out.WriteRune(' ')
			continue
		}
		if char == '"' && !inSingle {
			inDouble = !inDouble
			out.WriteRune(' ')
			continue
		}
		if inSingle || inDouble {
			out.WriteRune(' ')
			continue
		}
		out.WriteRune(char)
	}
	return out.String()
}
