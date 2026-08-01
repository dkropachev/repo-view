package repoview

import (
	"regexp"
	"sort"
	"strings"
)

type scopeResolver func(lines []string, lineNo int) (int, int)
type importResolver func(lines []string) (int, int, bool)

type commentStyle int

const (
	commentStyleCLike commentStyle = iota
	commentStylePython
)

// languageBackend contains every language-dependent navigation decision.
// Backends are stateless and shared by all RepoView instances.
type languageBackend interface {
	name() string
	definitionSymbol(line string) (string, bool)
	enclosingScope(lines []string, lineNo int) (int, int)
	importRange(lines []string) (int, int, bool)
	cleanSource(source string, dropComments, dropDocstrings bool) string
	ignoredSearchLines(lines []string, dropComments, dropDocstrings bool) map[int]bool
	stripComment(line string) string
}

type languageDefinition struct {
	resolveScope      scopeResolver
	resolveImports    importResolver
	languageName      string
	definitions       []*regexp.Regexp
	comments          commentStyle
	supportsDocstring bool
}

func newLanguageDefinition(
	name string,
	patterns []string,
	scope scopeResolver,
	imports importResolver,
	comments commentStyle,
	supportsDocstrings bool,
) languageDefinition {
	definitions := make([]*regexp.Regexp, 0, len(patterns))
	for _, pattern := range patterns {
		definitions = append(definitions, regexp.MustCompile(pattern))
	}
	return languageDefinition{
		languageName:      name,
		definitions:       definitions,
		resolveScope:      scope,
		resolveImports:    imports,
		comments:          comments,
		supportsDocstring: supportsDocstrings,
	}
}

func (l languageDefinition) name() string {
	return l.languageName
}

func (l languageDefinition) definitionSymbol(line string) (string, bool) {
	stripped := strings.TrimSpace(line)
	for _, pattern := range l.definitions {
		match := pattern.FindStringSubmatch(stripped)
		if len(match) == 2 {
			return match[1], true
		}
	}
	return "", false
}

func (l languageDefinition) enclosingScope(lines []string, lineNo int) (int, int) {
	if l.resolveScope == nil {
		return lineNo, lineNo
	}
	return l.resolveScope(lines, lineNo)
}

func (l languageDefinition) importRange(lines []string) (int, int, bool) {
	if l.resolveImports == nil {
		return 0, 0, false
	}
	return l.resolveImports(lines)
}

func (l languageDefinition) cleanSource(source string, dropComments, dropDocstrings bool) string {
	cleaned := source
	if dropDocstrings && l.supportsDocstring {
		cleaned = dropPythonDocstrings(cleaned)
	}
	if dropComments {
		switch l.comments {
		case commentStylePython:
			cleaned = dropPythonComments(cleaned)
		case commentStyleCLike:
			cleaned = dropCLikeComments(cleaned)
		}
	}
	if dropComments || dropDocstrings {
		cleaned = dropBlankArtifactLines(cleaned)
	}
	return cleaned
}

func (l languageDefinition) ignoredSearchLines(
	lines []string,
	dropComments, dropDocstrings bool,
) map[int]bool {
	ignored := map[int]bool{}
	if dropComments {
		for idx, line := range lines {
			trimmed := strings.TrimSpace(line)
			switch l.comments {
			case commentStylePython:
				if strings.HasPrefix(trimmed, "#") {
					ignored[idx+1] = true
				}
			case commentStyleCLike:
				if strings.HasPrefix(trimmed, "//") ||
					strings.HasPrefix(trimmed, "/*") ||
					strings.HasPrefix(trimmed, "*") {
					ignored[idx+1] = true
				}
			}
		}
	}
	if dropDocstrings && l.supportsDocstring {
		markPythonDocstringLines(lines, ignored)
	}
	return ignored
}

func (l languageDefinition) stripComment(line string) string {
	if l.comments == commentStylePython {
		return stripHashComment(line)
	}
	return stripSlashComment(line)
}

func markPythonDocstringLines(lines []string, ignored map[int]bool) {
	inDocstring := false
	quote := ""
	for idx, line := range lines {
		trimmed := strings.TrimSpace(line)
		if inDocstring {
			ignored[idx+1] = true
			if strings.Contains(trimmed, quote) {
				inDocstring = false
			}
			continue
		}
		if strings.HasPrefix(trimmed, `"""`) || strings.HasPrefix(trimmed, `'''`) {
			quote = trimmed[:3]
			ignored[idx+1] = true
			if strings.Count(trimmed, quote) < 2 {
				inDocstring = true
			}
		}
	}
}

var languagesByExtension = buildLanguageRegistry()

func buildLanguageRegistry() map[string]languageBackend {
	registry := map[string]languageBackend{}
	registerLanguage(registry, newGoLanguage(), ".go")
	registerLanguage(registry, newPythonLanguage(), ".py")
	registerLanguage(registry, newRustLanguage(), ".rs")
	registerBraceLanguages(registry)
	return registry
}

func registerLanguage(registry map[string]languageBackend, backend languageBackend, extensions ...string) {
	for _, extension := range extensions {
		if _, exists := registry[extension]; exists {
			panic("duplicate language extension: " + extension)
		}
		registry[extension] = backend
	}
}

func languageForExtension(extension string) languageBackend {
	if backend, ok := languagesByExtension[extension]; ok {
		return backend
	}
	return newBraceLanguage(strings.TrimPrefix(extension, "."))
}

func supportedExtensions() []string {
	extensions := make([]string, 0, len(languagesByExtension))
	for extension := range languagesByExtension {
		extensions = append(extensions, extension)
	}
	sort.Strings(extensions)
	return extensions
}

func braceScopeResolver(lines []string, lineNo int) (int, int) {
	if start, end, ok := braceScope(lines, lineNo); ok {
		return start, end
	}
	return lineNo, lineNo
}
