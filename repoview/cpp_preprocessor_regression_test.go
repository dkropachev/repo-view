package repoview

import (
	"slices"
	"strings"
	"testing"
)

func TestCPPKeywordSpelledMacrosRemainNavigableDefinitions(t *testing.T) {
	t.Parallel()

	const source = `#define class struct
#define private public
#define new make_value
class Widget {
private:
    int value;
};
`
	definitions := newCPPLanguage().sourceDefinitions(cppTestLines(source))
	want := []string{"class", "private", "new", "Widget", "value"}
	if got := cppDefinitionSymbols(definitions); !slices.Equal(got, want) {
		t.Fatalf("keyword macro definitions = %#v, want %#v", got, want)
	}
	for _, macro := range []string{"class", "private", "new"} {
		if got := newCPPLanguage().countSymbolOccurrences(
			strings.Split(source, "\n")[slices.Index(want, macro)], macro,
		); got != 1 {
			t.Errorf("%s macro occurrence count = %d, want 1", macro, got)
		}
	}
}

func TestCPPLexicalFallbackKeepsSpacedAndDecltypeOperators(t *testing.T) {
	t.Parallel()

	source := `struct Memory {
    void* operator new [](unsigned long);
    explicit operator decltype(auto)() const;
    void operator()() const;
};
` + strings.Repeat(";", cppMaximumConcreteParseBytes+1)
	definitions := newCPPLanguage().sourceDefinitions(cppTestLines(source))
	symbols := cppDefinitionSymbols(definitions)
	for _, want := range []string{
		"Memory", "operator new []", "operator decltype(auto)", "operator()",
	} {
		if !slices.Contains(symbols, want) {
			t.Errorf("lexical fallback omitted %q: %#v", want, definitions)
		}
	}
	for _, unwanted := range []string{"operator", "operator decltype"} {
		if slices.Contains(symbols, unwanted) {
			t.Errorf("operator-id parentheses produced truncated %q definition: %#v",
				unwanted, definitions)
		}
	}
}

func TestCPPPhaseTwoSplicedIdentifiersRemainDefinitions(t *testing.T) {
	t.Parallel()

	const source = "int tar\\\nget();\nint \\u0\\\n3B3amma();\n"
	lines := cppTestLines(source)
	definitions := newCPPLanguage().sourceDefinitions(lines)
	want := []string{"target", `\u03B3amma`}
	if got := cppDefinitionSymbols(definitions); !slices.Equal(got, want) {
		t.Fatalf("spliced definitions = %#v, want %#v", got, want)
	}
	lineStarts := cLineStarts(strings.TrimSuffix(source, "\n"))
	for _, definition := range definitions {
		start := lineStarts[definition.line-1] + definition.column - 1
		end := cppLogicalIdentifierEnd(strings.TrimSuffix(source, "\n"), start)
		if end <= start || cLogicalText(strings.TrimSuffix(source, "\n"), start, end) != definition.symbol {
			t.Errorf("definition is not logically source-backed: %#v", definition)
		}
	}
}

func TestCPPUppercaseCExtensionIsIncludedByDefaultDiscovery(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFile(t, root, "uppercase.C", "int uppercase_target();\n")
	found, err := mustView(t, root).Find(
		"uppercase_target",
		Options{Include: IncludeDefs, Return: ReturnLocations},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(found.Results) != 1 || found.Results[0].Path != "uppercase.C" ||
		found.Results[0].Language != "cpp" {
		t.Fatalf("uppercase .C discovery = %#v, want one C++ definition", found.Results)
	}
}
