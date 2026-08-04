package repoview

import (
	"reflect"
	"testing"
)

func TestJavaModuleNamesAndModuleImportsIgnoreCommentsBetweenTokens(t *testing.T) {
	t.Parallel()

	const moduleSource = `module com /* comment */ . example {
    requires java.base;
}`
	javaAssertConcreteSyntax(t, moduleSource)
	moduleBackend := newJavaLanguage()
	moduleLines := javaTestLines(moduleSource)
	definitions := moduleBackend.sourceDefinitions(moduleLines)
	if got, want := javaDefinitionSymbols(definitions), []string{"com.example"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("module definitions = %#v, want %#v", got, want)
	}
	if start, end, ok := moduleBackend.importRange(moduleLines); !ok || start != 2 || end != 2 {
		t.Fatalf("requires range = %d-%d, %v; want 2-2, true", start, end, ok)
	}

	const importModule = `import /* comment */ module java.base;
import module.api.Type;
class C {}`
	backend := newJavaLanguage()
	lines := javaTestLines(importModule)
	if start, end, ok := backend.importRange(lines); !ok || start != 1 || end != 2 {
		t.Fatalf("module import range = %d-%d, %v; want 1-2, true", start, end, ok)
	}
}

func TestJavaMalformedModuleNamesDoNotBridgeOpaqueLiterals(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		source           string
		lexicalAuthority bool
	}{
		{
			name:   "string literal",
			source: `module foo "hidden" . bar {}`,
		},
		{
			name:             "string literal after escaped identifier",
			source:           `module f\u006fo "hidden" . bar {}`,
			lexicalAuthority: true,
		},
		{
			name:   "character literal",
			source: `module foo 'x' . bar {}`,
		},
		{
			name:   "literal before first component",
			source: `module "hidden" foo {}`,
		},
		{
			name:   "literal after last component",
			source: `module foo "hidden" {}`,
		},
		{
			name:   "literal between open and module",
			source: `open "hidden" module foo {}`,
		},
		{
			name:   "character between open and module",
			source: `open 'x' module foo {}`,
		},
		{
			name:   "legal annotation literal does not hide illegal header literal",
			source: `@Anno("allowed") open "hidden" module foo {}`,
		},
		{
			name:   "literal after annotation arguments",
			source: `@Anno("allowed") "hidden" open module foo {}`,
		},
		{
			name:   "literal between annotation marker and name",
			source: `@"hidden" Anno open module foo {}`,
		},
		{
			name:             "literal before escaped module keyword",
			source:           `open "hidden" mod\u0075le foo {}`,
			lexicalAuthority: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			analysis := analyzeJavaSource(test.source, 1)
			if test.lexicalAuthority {
				if !analysis.lexed.translatedEscapes || analysis.tree != nil {
					t.Fatalf("fixture did not force lexical authority: escapes=%v tree=%v",
						analysis.lexed.translatedEscapes, analysis.tree != nil)
				}
			} else if analysis.tree == nil {
				t.Fatal("fixture did not exercise the concrete-tree recovery path")
			}
			if analysis.tree != nil {
				treeDefinitions := javaTreeDefinitions(
					test.source,
					1,
					analysis.tree,
					javaSyntaxAttachedStarts(test.source, analysis.tree),
					javaSyntaxErrorContexts(analysis.tree),
				)
				if got := javaDefinitionSymbols(treeDefinitions); len(got) != 0 {
					t.Fatalf("concrete malformed module definitions = %#v, want none", got)
				}
			}
			if got := javaDefinitionSymbols(analysis.definitions); len(got) != 0 {
				t.Fatalf("malformed module definitions = %#v, want none", got)
			}
		})
	}
}

func TestJavaModuleAnnotationsMayContainOpaqueArguments(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		source           string
		want             string
		lexicalAuthority bool
	}{
		{
			name:   "literal argument",
			source: `@Anno("x") open module foo {}`,
			want:   "foo",
		},
		{
			name: "qualified annotations and nested arguments",
			source: `@pkg.Anno(
    value = "x",
    nested = @Other(value = 'y'),
    values = {"a", "b"}
)
open module foo /* comment */ . bar {}`,
			want: "foo.bar",
		},
		{
			name:             "escaped module keyword",
			source:           `@Anno("x") open mod\u0075le foo {}`,
			want:             "foo",
			lexicalAuthority: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if !test.lexicalAuthority {
				javaAssertConcreteSyntax(t, test.source)
			}
			analysis := analyzeJavaSource(
				test.source,
				len(javaTestLines(test.source)),
			)
			if test.lexicalAuthority {
				if !analysis.lexed.translatedEscapes || analysis.tree != nil {
					t.Fatalf("fixture did not force lexical authority: escapes=%v tree=%v",
						analysis.lexed.translatedEscapes, analysis.tree != nil)
				}
			} else if analysis.tree == nil {
				t.Fatal("fixture did not exercise concrete syntax")
			}
			if got, want := javaDefinitionSymbols(analysis.definitions), []string{test.want}; !reflect.DeepEqual(got, want) {
				t.Fatalf("annotated module definitions = %#v, want %#v", got, want)
			}
		})
	}
}

func TestJavaQualifiedSourceSymbolTreatsOnlyCommentsAsTrivia(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		source string
		want   string
		ok     bool
	}{
		{
			name:   "block comment",
			source: `foo /* bridge */ . bar`,
			want:   "foo.bar",
			ok:     true,
		},
		{
			name:   "line comment",
			source: "foo // bridge\n . bar",
			want:   "foo.bar",
			ok:     true,
		},
		{name: "string literal", source: `foo "hidden" . bar`},
		{name: "character literal", source: `foo 'x' . bar`},
		{name: "text block", source: "foo \"\"\"\nhidden\n\"\"\" . bar"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, start, ok := javaQualifiedSourceSymbol(test.source, 0, len(test.source))
			if got != test.want || ok != test.ok {
				t.Fatalf("qualified symbol = %q, %v, want %q, %v", got, ok, test.want, test.ok)
			}
			if ok && start != 0 {
				t.Fatalf("qualified symbol start = %d, want 0", start)
			}
		})
	}
}
