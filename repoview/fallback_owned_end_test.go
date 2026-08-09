package repoview

import (
	"strings"
	"testing"
)

func TestCFallbackOwnedEndColumnsTrackConfirmedClosings(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		source  string
		closing string
		endLine int
	}{
		{
			name:    "plain brace",
			source:  "void first(void) { inside(); } int field = outside();\n",
			closing: "}",
			endLine: 1,
		},
		{
			name:    "digraph brace",
			source:  "void first(void) <% inside(); %> int field = outside();\n",
			closing: "%>",
			endLine: 1,
		},
		{
			name:    "spliced name",
			source:  "void fi\\\nrst(void) {\n} int field = outside();\n",
			closing: "}",
			endLine: 3,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			definition := fallbackEndpointDefinition(
				t, lexC(testCase.source).definitions, "first",
			)
			lines := strings.Split(testCase.source, "\n")
			wantColumn := strings.Index(lines[testCase.endLine-1], testCase.closing) +
				len(testCase.closing) + 1
			if definition.scopeEnd != testCase.endLine ||
				definition.ownedEndColumn != wantColumn {
				t.Fatalf("first definition = %#v, want end %d:%d",
					definition, testCase.endLine, wantColumn)
			}
		})
	}

	unfinished := fallbackEndpointDefinition(
		t,
		lexC("void first(void) {\n  inside();\n").definitions,
		"first",
	)
	if unfinished.ownedEndColumn != 0 {
		t.Fatalf("unfinished C definition = %#v, want no exact end", unfinished)
	}
}

func TestCPPFallbackOwnedEndColumnsTrackConfirmedClosings(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		source  string
		closing string
		endLine int
	}{
		{
			name:    "plain brace",
			source:  "void first() { inside(); } int field = outside();\n",
			closing: "}",
			endLine: 1,
		},
		{
			name:    "digraph brace",
			source:  "void first() <% inside(); %> int field = outside();\n",
			closing: "%>",
			endLine: 1,
		},
		{
			name:    "spliced name",
			source:  "void fi\\\nrst() {\n} int field = outside();\n",
			closing: "}",
			endLine: 3,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			analysis := lexCPP(testCase.source)
			definition := fallbackEndpointDefinition(
				t, analysis.fallbackDefinitions, "first",
			)
			lines := strings.Split(testCase.source, "\n")
			wantColumn := strings.Index(lines[testCase.endLine-1], testCase.closing) +
				len(testCase.closing) + 1
			if definition.scopeEnd != testCase.endLine ||
				definition.ownedEndColumn != wantColumn {
				t.Fatalf("first definition = %#v, want end %d:%d",
					definition, testCase.endLine, wantColumn)
			}
		})
	}

	unfinishedSource := "void first() {\n  inside();\n"
	unfinished := fallbackEndpointDefinition(
		t, lexCPP(unfinishedSource).fallbackDefinitions, "first",
	)
	if unfinished.ownedEndColumn != 0 {
		t.Fatalf("unfinished C++ definition = %#v, want no exact end", unfinished)
	}
}

func TestCAndCPPStreamedGapDefinitionsRetainExactClosingColumn(t *testing.T) {
	const side = cMaximumRetainedLexicalUnits/2 + 256
	padding := strings.Repeat("; ", side)

	t.Run("C", func(t *testing.T) {
		source := padding + "\nvoid first(void) {\n} int field = target;\n" + padding
		lexed := lexC(source)
		if !lexed.truncated {
			t.Fatal("fixture did not enter C streamed-gap recovery")
		}
		definition := fallbackEndpointDefinition(t, lexed.definitions, "first")
		if definition.scopeEnd != 3 || definition.ownedEndColumn != 2 {
			t.Fatalf("streamed C definition = %#v, want exact end 3:2", definition)
		}
	})

	t.Run("C plus plus", func(t *testing.T) {
		source := padding + "\nvoid first() {\n} int field = target;\n" + padding
		lexed := lexCPP(source)
		if !lexed.truncated {
			t.Fatal("fixture did not enter C++ streamed-gap recovery")
		}
		definition := fallbackEndpointDefinition(
			t, lexed.fallbackDefinitions, "first",
		)
		if definition.scopeEnd != 3 || definition.ownedEndColumn != 2 {
			t.Fatalf("streamed C++ definition = %#v, want exact end 3:2", definition)
		}
	})
}

func TestFindCAndCPPReferencesRespectFallbackClosingColumns(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		path       string
		definition string
	}{
		{name: "C", path: "fixture.c", definition: "void first(void)"},
		{name: "C plus plus", path: "fixture.cpp", definition: "void first()"},
	} {
		t.Run(testCase.name+" spliced definition", func(t *testing.T) {
			root := t.TempDir()
			definition := strings.Replace(testCase.definition, "first", "fi\\\nrst", 1)
			writeFile(t, root, testCase.path,
				definition+" {\n} int field = target();\n")
			response, err := mustView(t, root).Find("target", Options{
				Include: IncludeRefs,
				Return:  ReturnScope,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(response.Results) != 1 || response.Results[0].Scope != "" {
				t.Fatalf("spliced result = %#v, want top-level scope", response.Results)
			}
		})

		t.Run(testCase.name+" digraph definition", func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, root, testCase.path,
				testCase.definition+" <% inside(); %> int field = outside();\n")
			for _, query := range []struct {
				symbol string
				scope  string
			}{
				{symbol: "inside", scope: "first"},
				{symbol: "outside", scope: ""},
			} {
				response, err := mustView(t, root).Find(query.symbol, Options{
					Include: IncludeRefs,
					Return:  ReturnScope,
				})
				if err != nil {
					t.Fatal(err)
				}
				if len(response.Results) != 1 ||
					response.Results[0].Scope != query.scope {
					t.Errorf("Find(%q) = %#v, want scope %q",
						query.symbol, response.Results, query.scope)
				}
			}
		})
	}
}

func TestModulaStreamedFallbackTracksConfirmedNamedEnds(t *testing.T) {
	source := "MODULE M; PROCEDURE First; BEGIN END First; " +
		strings.Repeat(";", modulaMaximumConcreteTokens+64) +
		" BEGIN target; END M.\n"
	analysis := analyzeModulaSource(source, strings.Count(source, "\n")+1)
	if analysis.tree != nil {
		t.Fatal("fixture did not force streamed fallback")
	}
	first := fallbackEndpointDefinition(t, analysis.definitions, "First")
	wantFirstColumn := strings.Index(source, "END First;") + len("END First;") + 1
	if first.scopeEnd != 1 || first.ownedEndColumn != wantFirstColumn {
		t.Fatalf("First definition = %#v, want exact end 1:%d",
			first, wantFirstColumn)
	}
	module := fallbackEndpointDefinition(t, analysis.definitions, "M")
	wantModuleColumn := strings.LastIndex(source, "END M.") + len("END M.") + 1
	if module.scopeEnd != 1 || module.ownedEndColumn != wantModuleColumn {
		t.Fatalf("M definition = %#v, want exact end 1:%d",
			module, wantModuleColumn)
	}

	root := t.TempDir()
	writeFile(t, root, "fixture.mod", source)
	response, err := mustView(t, root).Find("target", Options{
		Include: IncludeRefs,
		Return:  ReturnScope,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].Scope != "M" {
		t.Fatalf("streamed fallback result = %#v, want M scope", response.Results)
	}
}

func TestModulaStreamedFallbackDoesNotInventEOFEndpoint(t *testing.T) {
	source := "MODULE M; PROCEDURE First; BEGIN target;\n"
	definitions := analyzeModulaLexically(
		source, strings.Count(source, "\n")+1,
	).definitions
	for _, symbol := range []string{"M", "First"} {
		definition := fallbackEndpointDefinition(t, definitions, symbol)
		if definition.ownedEndColumn != 0 {
			t.Errorf("unfinished %s definition = %#v, want no exact end",
				symbol, definition)
		}
	}
}

func TestCPPCombinedDefinitionsMovesOwnedEndpointWithWiderScope(t *testing.T) {
	concrete := sourceDefinition{
		symbol: "first", line: 1, column: 6,
		scopeStart: 1, scopeEnd: 2, ownedEndColumn: 9, ownsScope: true,
	}
	lexical := sourceDefinition{
		symbol: "first", line: 1, column: 6,
		scopeStart: 1, scopeEnd: 3, ownedEndColumn: 5, ownsScope: true,
	}
	definitions := cppCombinedDefinitions(
		3, []sourceDefinition{concrete}, []sourceDefinition{lexical},
	)
	if len(definitions) != 1 || definitions[0].scopeEnd != 3 ||
		definitions[0].ownedEndColumn != 5 {
		t.Fatalf("merged definitions = %#v, want wider scope endpoint 3:5",
			definitions)
	}

	lexical.ownedEndColumn = 0
	definitions = cppCombinedDefinitions(
		3, []sourceDefinition{concrete}, []sourceDefinition{lexical},
	)
	if len(definitions) != 1 || definitions[0].ownedEndColumn != 0 {
		t.Fatalf("merged definitions = %#v, inherited stale endpoint", definitions)
	}
}

func fallbackEndpointDefinition(
	t *testing.T,
	definitions []sourceDefinition,
	symbol string,
) sourceDefinition {
	t.Helper()
	for _, definition := range definitions {
		if definition.symbol == symbol {
			return definition
		}
	}
	t.Fatalf("missing definition %q in %#v", symbol, definitions)
	return sourceDefinition{}
}
