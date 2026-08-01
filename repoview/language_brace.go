package repoview

type braceLanguage struct {
	languageDefinition
}

func newBraceLanguage(name string) braceLanguage {
	return braceLanguage{newLanguageDefinition(
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

func registerBraceLanguages(registry map[string]languageBackend) {
	registerLanguage(registry, newBraceLanguage("javascript"), ".js")
	registerLanguage(registry, newBraceLanguage("mjs"), ".mjs")
	registerLanguage(registry, newBraceLanguage("jsx"), ".jsx")
	registerLanguage(registry, newBraceLanguage("typescript"), ".ts")
	registerLanguage(registry, newBraceLanguage("tsx"), ".tsx")
	registerLanguage(registry, newBraceLanguage("c"), ".c", ".h")
	registerLanguage(registry, newBraceLanguage("cpp"), ".cc", ".cpp", ".hpp")
	registerLanguage(registry, newBraceLanguage("cs"), ".cs")
	registerLanguage(registry, newBraceLanguage("java"), ".java")
	registerLanguage(registry, newBraceLanguage("kt"), ".kt")
	registerLanguage(registry, newBraceLanguage("swift"), ".swift")
	registerLanguage(registry, newBraceLanguage("mod"), ".mod")
}
