package repoview

import (
	"strings"
	"testing"
)

func TestJavaLexicalOwnedEndColumnsTrackExactPhysicalBoundaries(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		closing string
	}{
		{name: "plain closing brace", closing: "}"},
		{name: "Unicode escaped closing brace", closing: `\u007d`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			source := "class C {\n  void first() {\n  " + testCase.closing +
				" Object field = target;\n}\n"
			lines := strings.Split(source, "\n")
			lexed := lexJava(source)
			definitions := analyzeJavaLexically(
				source, len(lines), lexed,
			).definitions
			definition := javaFirstDefinition(t, definitions, "first")
			wantColumn := strings.Index(lines[2], testCase.closing) +
				len(testCase.closing) + 1
			if definition.scopeEnd != 3 || definition.ownedEndColumn != wantColumn {
				t.Fatalf("first definition = %#v, want scope end 3 at column %d",
					definition, wantColumn)
			}
		})
	}
}

func TestJavaConcreteOwnedEndColumnTracksPlainMultilineClosure(t *testing.T) {
	source := "class C {\n  void first() {\n  } Object field = target;\n}\n"
	analysis := analyzeJavaSource(source, strings.Count(source, "\n")+1)
	if analysis.tree == nil || len(analysis.recoverySpans) != 0 {
		t.Fatalf("fixture lacks clean concrete authority: tree=%v recovery=%#v",
			analysis.tree != nil, analysis.recoverySpans)
	}
	definition := javaFirstDefinition(t, analysis.definitions, "first")
	if definition.scopeEnd != 3 || definition.ownedEndColumn != 4 {
		t.Fatalf("concrete first definition = %#v, want scope end 3 at column 4",
			definition)
	}
}

func TestJavaLexicalOwnedEndColumnsCoverNonBraceAndRejectSyntheticEnds(t *testing.T) {
	t.Run("scoped field ends at semicolon", func(t *testing.T) {
		source := "class C {\n  Object field = new Object() {}; Object later = target;\n}\n"
		lines := strings.Split(source, "\n")
		definitions := analyzeJavaLexically(
			source, len(lines), lexJava(source),
		).definitions
		definition := javaFirstDefinition(t, definitions, "field")
		wantColumn := strings.Index(lines[1], ";") + 2
		if !definition.ownsScope || definition.ownedEndColumn != wantColumn {
			t.Fatalf("field definition = %#v, want exact end column %d",
				definition, wantColumn)
		}
	})

	t.Run("unmatched body has no exact end", func(t *testing.T) {
		source := "class C {\n  void first() {\n  // trailing }"
		definitions := analyzeJavaLexically(
			source, strings.Count(source, "\n")+1, lexJava(source),
		).definitions
		definition := javaFirstDefinition(t, definitions, "first")
		if definition.ownedEndColumn != 0 {
			t.Fatalf("unmatched first definition = %#v, want no exact end column",
				definition)
		}
	})
}

func TestMergeJavaDefinitionsDoesNotReuseStaleConcreteEndColumn(t *testing.T) {
	concrete := sourceDefinition{
		symbol: "first", line: 1, column: 6,
		scopeStart: 1, scopeEnd: 2, ownedEndColumn: 9, ownsScope: true,
	}
	lexical := sourceDefinition{
		symbol: "first", line: 1, column: 6,
		scopeStart: 1, scopeEnd: 3, ownedEndColumn: 5, ownsScope: true,
	}
	definitions := mergeJavaDefinitions(
		[]sourceDefinition{concrete}, []sourceDefinition{lexical}, false,
		[]int{0, 0, 0, 1},
	)
	if len(definitions) != 1 || definitions[0] != lexical {
		t.Fatalf("merged definitions = %#v, want recovered endpoint %#v",
			definitions, lexical)
	}

	lexical.ownedEndColumn = 0
	definitions = mergeJavaDefinitions(
		[]sourceDefinition{concrete}, []sourceDefinition{lexical}, false,
		[]int{0, 0, 0, 1},
	)
	if len(definitions) != 1 || definitions[0].ownedEndColumn != 0 {
		t.Fatalf("merged definitions = %#v, inherited stale concrete endpoint",
			definitions)
	}
}

func TestJavaStreamedGapDefinitionsRetainExactClosingColumn(t *testing.T) {
	const side = javaMaximumStoredLexicalTokens/2 + 256
	padding := strings.Repeat("; ", side)
	source := padding + "\nclass C {\n  void first() {\n  } Object field = target;\n}\n" +
		padding
	analysis := analyzeJavaSource(source, strings.Count(source, "\n")+1)
	if !analysis.lexed.truncated {
		t.Fatal("fixture did not enter retained-token gap recovery")
	}
	definition := javaFirstDefinition(t, analysis.definitions, "first")
	if definition.scopeEnd != 4 || definition.ownedEndColumn != 4 {
		t.Fatalf("streamed first definition = %#v, want scope end 4 at column 4",
			definition)
	}
}

func TestFindJavaReferenceAfterExactMultilineClosingBoundary(t *testing.T) {
	for _, testCase := range []struct {
		name    string
		closing string
	}{
		{name: "plain closing brace", closing: "}"},
		{name: "Unicode escaped closing brace", closing: `\u007d`},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			source := "class C {\n  void first() {\n  " + testCase.closing +
				" Object field = target;\n}\n"
			writeFile(t, root, "C.java", source)
			response, err := mustView(t, root).Find("target", Options{
				Include: IncludeRefs,
				Return:  ReturnScope,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(response.Results) != 1 || response.Results[0].Scope != "C" {
				t.Fatalf("results = %#v, want one reference in C scope",
					response.Results)
			}
		})
	}
}
