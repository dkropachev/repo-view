package repoview

import (
	"regexp"
	"strings"
)

func cleanSource(source, ext string, dropComments, dropDocstrings bool) string {
	cleaned := source
	if dropDocstrings && ext == ".py" {
		cleaned = dropPythonDocstrings(cleaned)
	}
	if dropComments {
		if ext == ".py" {
			cleaned = dropPythonComments(cleaned)
		} else {
			cleaned = dropCLikeComments(cleaned)
		}
	}
	if dropComments || dropDocstrings {
		cleaned = dropBlankArtifactLines(cleaned)
	}
	return cleaned
}

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
	block := regexp.MustCompile(`(?s)/\*.*?\*/`).ReplaceAllString(source, "")
	lines := strings.Split(block, "\n")
	for i, line := range lines {
		lines[i] = stripSlashComment(line)
	}
	return strings.Join(lines, "\n")
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
