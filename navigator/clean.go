package navigator

import (
	"strings"
)

func dropPythonComments(source string) string {
	lines := strings.Split(source, "\n")
	for i, line := range lines {
		lines[i] = stripHashComment(line)
	}
	return strings.Join(lines, "\n")
}

func dropPythonDocstrings(source string) string {
	lines := strings.Split(source, "\n")
	out := make([]string, 0, len(lines))
	inDocstring := false
	quote := ""

	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if inDocstring {
			if strings.Contains(trimmed, quote) {
				inDocstring = false
			}
			continue
		}
		if strings.HasPrefix(trimmed, `"""`) || strings.HasPrefix(trimmed, `'''`) {
			quote = trimmed[:3]
			if strings.Count(trimmed, quote) < 2 {
				inDocstring = true
			}
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func dropCLikeComments(source string) string {
	const (
		cLikeCode = iota
		cLikeSingleQuoted
		cLikeDoubleQuoted
		cLikeBacktickQuoted
		cLikeLineComment
		cLikeBlockComment
	)
	state := cLikeCode
	var cleaned strings.Builder
	cleaned.Grow(len(source))
	for index := 0; index < len(source); {
		current := source[index]
		next := byte(0)
		if index+1 < len(source) {
			next = source[index+1]
		}
		switch state {
		case cLikeCode:
			switch {
			case current == '/' && next == '/':
				state = cLikeLineComment
				index += 2
			case current == '/' && next == '*':
				cleaned.WriteByte(' ')
				state = cLikeBlockComment
				index += 2
			case current == '\'':
				state = cLikeSingleQuoted
				cleaned.WriteByte(current)
				index++
			case current == '"':
				state = cLikeDoubleQuoted
				cleaned.WriteByte(current)
				index++
			case current == '`':
				state = cLikeBacktickQuoted
				cleaned.WriteByte(current)
				index++
			default:
				cleaned.WriteByte(current)
				index++
			}
		case cLikeSingleQuoted, cLikeDoubleQuoted, cLikeBacktickQuoted:
			cleaned.WriteByte(current)
			index++
			if current == '\n' && state != cLikeBacktickQuoted {
				state = cLikeCode
				continue
			}
			if current == '\\' && index < len(source) {
				cleaned.WriteByte(source[index])
				index++
				continue
			}
			if state == cLikeSingleQuoted && current == '\'' ||
				state == cLikeDoubleQuoted && current == '"' ||
				state == cLikeBacktickQuoted && current == '`' {
				state = cLikeCode
			}
		case cLikeLineComment:
			if current == '\n' {
				cleaned.WriteByte(current)
				state = cLikeCode
			}
			index++
		case cLikeBlockComment:
			switch {
			case current == '*' && next == '/':
				state = cLikeCode
				index += 2
			case current == '\n':
				cleaned.WriteByte(current)
				index++
			default:
				index++
			}
		}
	}
	return cleaned.String()
}

func stripHashComment(line string) string {
	inSingle := false
	inDouble := false
	escaped := false
	for i, char := range line {
		if escaped {
			escaped = false
			continue
		}
		if char == '\\' {
			escaped = true
			continue
		}
		if char == '\'' && !inDouble {
			inSingle = !inSingle
			continue
		}
		if char == '"' && !inSingle {
			inDouble = !inDouble
			continue
		}
		if char == '#' && !inSingle && !inDouble {
			return strings.TrimRight(line[:i], " \t")
		}
	}
	return line
}

func stripSlashComment(line string) string {
	inSingle := false
	inDouble := false
	escaped := false
	for i := range len(line) - 1 {
		char := rune(line[i])
		next := rune(line[i+1])
		if escaped {
			escaped = false
			continue
		}
		if char == '\\' {
			escaped = true
			continue
		}
		if char == '\'' && !inDouble {
			inSingle = !inSingle
			continue
		}
		if char == '"' && !inSingle {
			inDouble = !inDouble
			continue
		}
		if char == '/' && next == '/' && !inSingle && !inDouble {
			return strings.TrimRight(line[:i], " \t")
		}
	}
	return line
}

func dropBlankArtifactLines(source string) string {
	lines := strings.Split(source, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimRight(line, " \t")
		if strings.TrimSpace(line) == "" {
			continue
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}
