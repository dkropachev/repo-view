package repoview

import (
	"strings"
	"testing"
)

func TestKotlinLexicalOwnedEndColumnsTrackOnlyExactPhysicalBoundaries(t *testing.T) {
	t.Run("balanced closing brace", func(t *testing.T) {
		const source = `class C {
  context(Logger)
  fun first() {
  } val field = target()
}`
		definitions := analyzeKotlinLexically(
			source, len(kotlinTestLines(source)),
		).definitions
		definition := kotlinTestFirstDefinition(t, definitions, "first")
		if definition.scopeEnd != 4 || definition.ownedEndColumn != 4 {
			t.Fatalf("first definition = %#v, want scope end 4 at column 4", definition)
		}
	})

	t.Run("semicolon terminator", func(t *testing.T) {
		const source = `class C {
  fun first() = Unit; val field = target()
}`
		lines := kotlinTestLines(source)
		definitions := analyzeKotlinLexically(source, len(lines)).definitions
		definition := kotlinTestFirstDefinition(t, definitions, "first")
		wantColumn := strings.Index(lines[1], ";") + 2
		if definition.scopeEnd != 2 || definition.ownedEndColumn != wantColumn {
			t.Fatalf("first definition = %#v, want scope end 2 at column %d",
				definition, wantColumn)
		}
	})

	t.Run("EOF recovery is not exact", func(t *testing.T) {
		const source = `class C {
  context(Logger)
  fun unfinished() {`
		lineCount := len(kotlinTestLines(source))
		groups := map[string][]sourceDefinition{
			"lexical": analyzeKotlinLexically(source, lineCount).definitions,
			"merged":  analyzeKotlinSource(source, lineCount).definitions,
		}
		for name, definitions := range groups {
			definition := kotlinTestFirstDefinition(t, definitions, "unfinished")
			if definition.ownedEndColumn != 0 {
				t.Errorf("%s unfinished definition claimed an exact boundary: %#v",
					name, definition)
			}
		}
	})
}

func TestFindKotlinReferenceAfterExactMultilineClosingBoundary(t *testing.T) {
	const source = `class C {
  context(Logger)
  fun first() {
  } val field = target()
}`
	root := t.TempDir()
	writeFile(t, root, "C.kt", source)

	response, err := mustView(t, root).Find("target", Options{
		Include: IncludeRefs,
		Return:  ReturnScope,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].Scope != "C" {
		t.Fatalf("results = %#v, want one reference in C scope", response.Results)
	}
}

func TestKotlinMergeDefinitionsDoesNotReuseStaleConcreteEndColumn(t *testing.T) {
	concrete := sourceDefinition{
		symbol: "first", line: 1, column: 5,
		scopeStart: 1, scopeEnd: 2, ownedEndColumn: 9, ownsScope: true,
	}
	lexical := sourceDefinition{
		symbol: "first", line: 1, column: 5,
		scopeStart: 1, scopeEnd: 2, ownsScope: true,
	}
	definitions := kotlinMergeDefinitions(
		2, []sourceDefinition{concrete}, []sourceDefinition{lexical},
		false, nil, nil, "",
	)
	if len(definitions) != 1 || definitions[0].ownedEndColumn != 0 {
		t.Fatalf("merged definitions = %#v, inherited stale concrete endpoint",
			definitions)
	}
}
