package repoview

type typescriptLanguage struct {
	javascriptLanguage
}

func newTypeScriptLanguage(name string, tsx bool) typescriptLanguage {
	backend := newJavaScriptLanguage(name)
	backend.flavor = javascriptSyntaxFlavorTypeScript
	if tsx {
		backend.flavor = javascriptSyntaxFlavorTSX
	}
	return typescriptLanguage{javascriptLanguage: backend}
}

func registerTypeScriptLanguages(registry map[string]languageBackend) {
	registerLanguage(registry, newTypeScriptLanguage("typescript", false), ".ts")
	registerLanguage(registry, newTypeScriptLanguage("tsx", true), ".tsx")
	registerLanguage(registry, newTypeScriptLanguage("mts", false), ".mts")
	registerLanguage(registry, newTypeScriptLanguage("cts", false), ".cts")
}

func (t typescriptLanguage) prepareSource(lines []string) languageBackend {
	prepared := t.javascriptLanguage.prepareSource(lines)
	t.javascriptLanguage = prepared.(javascriptLanguage)
	return t
}
