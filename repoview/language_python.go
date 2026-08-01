package repoview

import "strings"

type pythonLanguage struct {
	languageDefinition
}

func newPythonLanguage() pythonLanguage {
	return pythonLanguage{newLanguageDefinition(
		"python",
		[]string{`^(?:async\s+def|def|class)\s+([A-Za-z_][A-Za-z0-9_]*)\b`},
		indentScope,
		pythonImports,
		commentStylePython,
		true,
	)}
}

func pythonImports(lines []string) (int, int, bool) {
	start, end := 0, 0
	for idx, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "import ") || strings.HasPrefix(trimmed, "from ") {
			if start == 0 {
				start = idx + 1
			}
			end = idx + 1
		}
	}
	return start, end, start > 0 && end >= start
}
