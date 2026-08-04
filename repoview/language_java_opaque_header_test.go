package repoview

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestJavaDeclarationHeadersAllowLiteralsOnlyInExpressions(t *testing.T) {
	t.Parallel()

	const source = `@Tag("type") class Good {
    @Tag('f') String first = "one", second = 'y';
    @Tag("m") void method(@Tag('p') String parameter) { use("body"); }
}
@interface Tag { String value() default "default"; }
enum Choice { REAL("argument"); Choice(String value) {} }
record Item(@Tag("component") String value) {}
class Commented {
    int /* comment */ field;
    void /* comment */ commentedMethod() {}
}`
	javaAssertConcreteSyntax(t, source)
	want := []string{
		"Good", "first", "second", "method", "Tag", "value", "Choice", "REAL",
		"Choice", "Item", "value", "Commented", "field", "commentedMethod",
	}
	analysis := analyzeJavaSource(source, strings.Count(source, "\n")+1)
	if got := javaDefinitionSymbols(analysis.definitions); !reflect.DeepEqual(got, want) {
		t.Fatalf("integrated definitions = %#v, want %#v", got, want)
	}
	lexical := javaRecoveryAnalysis(source)
	if got := javaDefinitionSymbols(lexical.definitions); !reflect.DeepEqual(got, want) {
		t.Fatalf("lexical definitions = %#v, want %#v", got, want)
	}
}

func TestJavaFieldInitializersMayContainOpaqueLiterals(t *testing.T) {
	t.Parallel()

	t.Run("text block", func(t *testing.T) {
		t.Parallel()

		const source = `class TextBlock {
    String value = """
        allowed
        """;
}`
		javaAssertConcreteSyntax(t, source)
		want := []string{"TextBlock", "value"}
		analysis := analyzeJavaSource(source, strings.Count(source, "\n")+1)
		if got := javaDefinitionSymbols(analysis.definitions); !reflect.DeepEqual(got, want) {
			t.Fatalf("integrated definitions = %#v, want %#v", got, want)
		}
		if got := javaDefinitionSymbols(javaRecoveryAnalysis(source).definitions); !reflect.DeepEqual(got, want) {
			t.Fatalf("lexical definitions = %#v, want %#v", got, want)
		}
	})

	t.Run("unicode escaped quotes", func(t *testing.T) {
		t.Parallel()

		const source = `class Escaped { String value = \u0022allowed\u0022; }`
		want := []string{"Escaped", "value"}
		analysis := analyzeJavaSource(source, 1)
		if !analysis.lexed.translatedEscapes || analysis.tree != nil {
			t.Fatalf("fixture did not force lexical authority: escapes=%v tree=%v",
				analysis.lexed.translatedEscapes, analysis.tree != nil)
		}
		if got := javaDefinitionSymbols(analysis.definitions); !reflect.DeepEqual(got, want) {
			t.Fatalf("integrated definitions = %#v, want %#v", got, want)
		}
		if got := javaDefinitionSymbols(javaRecoveryAnalysis(source).definitions); !reflect.DeepEqual(got, want) {
			t.Fatalf("lexical definitions = %#v, want %#v", got, want)
		}
	})
}

func TestJavaOpaqueFragmentsCannotBridgeDeclarationHeaders(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name             string
		source           string
		forbidden        []string
		required         []string
		concreteRecovery bool
		lexicalAuthority bool
	}{
		{
			name:      "string before class name",
			source:    `class "hidden" Fake {}`,
			forbidden: []string{"Fake"},
		},
		{
			name:      "character before class name",
			source:    `class 'x' Fake {}`,
			forbidden: []string{"Fake"},
		},
		{
			name:      "text block before class name",
			source:    "class \"\"\"\nhidden\n\"\"\" Fake {}",
			forbidden: []string{"Fake"},
		},
		{
			name:             "unicode escaped quotes before class name",
			source:           `class \u0022hidden\u0022 Fake {}`,
			forbidden:        []string{"Fake"},
			lexicalAuthority: true,
		},
		{
			name:      "literal after class name",
			source:    `class Fake "hidden" { int leaked; }`,
			forbidden: []string{"Fake", "leaked"},
		},
		{
			name:      "interface name",
			source:    `interface "hidden" Fake {}`,
			forbidden: []string{"Fake"},
		},
		{
			name:      "record name",
			source:    `record "hidden" Fake() {}`,
			forbidden: []string{"Fake"},
		},
		{
			name:      "enum name",
			source:    `enum "hidden" Fake { ONE }`,
			forbidden: []string{"Fake"},
		},
		{
			name:             "literal between modifier and type keyword",
			source:           `public "hidden" class Fake {}`,
			forbidden:        []string{"Fake"},
			concreteRecovery: true,
		},
		{
			name:      "literal after annotation",
			source:    `@Tag("allowed") "hidden" class Fake {}`,
			forbidden: []string{"Fake"},
		},
		{
			name:      "field prefix",
			source:    `class Owner { int "hidden" fakeField; }`,
			forbidden: []string{"fakeField"},
			required:  []string{"Owner"},
		},
		{
			name:      "field suffix",
			source:    `class Owner { int fakeField "hidden"; }`,
			forbidden: []string{"fakeField"},
			required:  []string{"Owner"},
		},
		{
			name:      "method prefix",
			source:    `class Owner { void "hidden" fakeMethod() {} }`,
			forbidden: []string{"fakeMethod"},
			required:  []string{"Owner"},
		},
		{
			name:      "method suffix",
			source:    `class Owner { void fakeMethod "hidden" () {} }`,
			forbidden: []string{"fakeMethod"},
			required:  []string{"Owner"},
		},
		{
			name:             "method parameter",
			source:           `class Owner { void fakeMethod(String "hidden" parameter) {} }`,
			forbidden:        []string{"fakeMethod"},
			required:         []string{"Owner"},
			concreteRecovery: true,
		},
		{
			name:             "record component",
			source:           `record Fake(String value "hidden") {}`,
			forbidden:        []string{"Fake", "value"},
			concreteRecovery: true,
		},
		{
			name:      "later comma declarator",
			source:    `class Owner { String first = "allowed", "hidden" fakeField; }`,
			forbidden: []string{"fakeField"},
			required:  []string{"Owner", "first"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			analysis := analyzeJavaSource(
				test.source, strings.Count(test.source, "\n")+1,
			)
			if test.lexicalAuthority {
				if !analysis.lexed.translatedEscapes || analysis.tree != nil {
					t.Fatalf("fixture did not force lexical authority: escapes=%v tree=%v",
						analysis.lexed.translatedEscapes, analysis.tree != nil)
				}
			} else if test.concreteRecovery && analysis.tree == nil {
				t.Fatal("fixture did not exercise concrete-tree recovery")
			}
			javaAssertOpaqueHeaderSymbols(t, "integrated", analysis.definitions,
				test.required, test.forbidden)

			lexical := javaRecoveryAnalysis(test.source)
			javaAssertOpaqueHeaderSymbols(t, "lexical", lexical.definitions,
				test.required, test.forbidden)
		})
	}
}

func TestJavaOpaqueFragmentsCannotBridgeEnumConstantHeaders(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name      string
		source    string
		forbidden []string
	}{
		{
			name:      "literal before constant",
			source:    `enum Choice { "hidden" FAKE, REAL("allowed"); }`,
			forbidden: []string{"FAKE"},
		},
		{
			name:      "literal after constant",
			source:    `enum Choice { FAKE "hidden", REAL("allowed"); }`,
			forbidden: []string{"FAKE"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			analysis := analyzeJavaSource(test.source, 1)
			javaAssertOpaqueHeaderSymbols(t, "integrated", analysis.definitions,
				[]string{"Choice", "REAL"}, test.forbidden)
			lexical := javaRecoveryAnalysis(test.source)
			javaAssertOpaqueHeaderSymbols(t, "lexical", lexical.definitions,
				[]string{"Choice", "REAL"}, test.forbidden)
		})
	}
}

func TestJavaOpaqueFragmentsCannotBridgeImportHeaders(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		source string
	}{
		{
			name:   "ordinary import",
			source: `import java "hidden" .util.List; class Real {}`,
		},
		{
			name:   "static import",
			source: `import static java "hidden" .util.List.value; class Real {}`,
		},
		{
			name:   "module import",
			source: `import module java "hidden" .base; class Real {}`,
		},
		{
			name:   "requires directive",
			source: `module demo { requires java "hidden" .base; }`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			analysis := analyzeJavaSource(
				test.source, strings.Count(test.source, "\n")+1,
			)
			if len(analysis.imports) != 0 {
				t.Fatalf("integrated import spans = %#v, want none", analysis.imports)
			}
			if lexical := javaRecoveryAnalysis(test.source); len(lexical.imports) != 0 {
				t.Fatalf("lexical import spans = %#v, want none", lexical.imports)
			}
		})
	}

	const source = `package java "hidden" .util;
import java.util.List;
class Real {}`
	analysis := analyzeJavaSource(source, strings.Count(source, "\n")+1)
	if got, want := analysis.imports, []javaLineSpan{{start: 2, end: 2}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("valid import after malformed package = %#v, want %#v", got, want)
	}
	javaAssertOpaqueHeaderSymbols(t, "integrated", analysis.definitions,
		[]string{"Real"}, nil)
}

func TestJavaOpaqueFragmentsCannotPrecedeModuleHeaders(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		source string
	}{
		{"plain module", `"hidden" module fake.zero {}`},
		{"open module", `"hidden" open module fake.open {}`},
		{"annotated open module", `"hidden" @Anno("allowed") open module fake.one {}`},
		{"character before annotation", `'x' @Anno("allowed") module fake.two {}`},
		{"text block before annotation", "\"\"\"\nhidden\n\"\"\" @Anno(\"allowed\") module fake.three {}"},
		{"unicode literal before annotation", `\u0022hidden\u0022 @Anno("allowed") module fake.four {}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			analysis := analyzeJavaSource(
				test.source, strings.Count(test.source, "\n")+1,
			)
			javaAssertOpaqueHeaderSymbols(
				t, "integrated", analysis.definitions, nil,
				[]string{"fake.zero", "fake.open", "fake.one", "fake.two", "fake.three", "fake.four"},
			)
			lexical := javaRecoveryAnalysis(test.source)
			javaAssertOpaqueHeaderSymbols(
				t, "lexical", lexical.definitions, nil,
				[]string{"fake.zero", "fake.open", "fake.one", "fake.two", "fake.three", "fake.four"},
			)
		})
	}
}

func TestJavaStreamedGapRejectsOpaqueDeclarationHeaders(t *testing.T) {
	const paddingTokens = javaMaximumStoredLexicalTokens/2 + 256
	padding := strings.Repeat("; ", paddingTokens)
	for _, test := range []struct {
		name      string
		middle    string
		required  []string
		forbidden []string
		counts    map[string]int
	}{
		{
			name:      "type header",
			middle:    `class Fake "hidden" { int leaked; }`,
			forbidden: []string{"Fake", "leaked"},
		},
		{
			name:     "constructor parameter",
			middle:   `class Owner { Owner(String "hidden" parameter) {} String good = "allowed"; }`,
			required: []string{"Owner", "good"},
			counts:   map[string]int{"Owner": 1},
		},
		{
			name:      "enum constants",
			middle:    `enum Choice { FAKE "hidden", REAL("allowed"); }`,
			required:  []string{"Choice", "REAL"},
			forbidden: []string{"FAKE"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := padding + "\n" + test.middle + "\n" + padding
			analysis := analyzeJavaSource(source, strings.Count(source, "\n")+1)
			if !analysis.lexed.truncated {
				t.Fatal("fixture did not enter retained-token gap recovery")
			}
			javaAssertOpaqueHeaderSymbols(t, "streamed", analysis.definitions,
				test.required, test.forbidden)
			for symbol, want := range test.counts {
				got := 0
				for _, definition := range analysis.definitions {
					if definition.symbol == symbol {
						got++
					}
				}
				if got != want {
					t.Errorf("%s count = %d, want %d; definitions=%#v",
						symbol, got, want, analysis.definitions)
				}
			}
		})
	}
}

func javaAssertOpaqueHeaderSymbols(
	t *testing.T,
	path string,
	definitions []sourceDefinition,
	required, forbidden []string,
) {
	t.Helper()
	symbols := javaDefinitionSymbols(definitions)
	for _, symbol := range required {
		if !slices.Contains(symbols, symbol) {
			t.Errorf("%s definitions = %#v, missing %q", path, symbols, symbol)
		}
	}
	for _, symbol := range forbidden {
		if slices.Contains(symbols, symbol) {
			t.Errorf("%s definitions = %#v, unexpectedly contain %q", path, symbols, symbol)
		}
	}
}
