package repoview

import (
	"slices"
	"strings"
	"testing"
)

func TestCSharpTreeAnalysisMatchesGeneratedGrammarShapes(t *testing.T) {
	const source = `extern alias Legacy;
global using Alias = global::System.Collections.Generic;
namespace Demo.Tools
{
    /// <summary>Person documentation.</summary>
    [Obsolete]
    public record class Person(string Name, int Age)
    {
        private int first, second;
        private Func<int> factory = () => { int nested = 0; return nested; };
		public event Action Opened, Closed;
		public event Action Changed { add { } remove { } }
		public string Title { get; init; }
		public ref readonly Person Current => ref primary;
		public string this[int index] => index.ToString();
        public Person(int seed) : this("", seed) { }
        public void Run() { int Local(int value) => value; }
        public static Person operator +(Person left, Person right) => left;
        public static implicit operator byte(Person value) => 0;
        public static explicit operator checked short(Person value) => 0;
        ~Person() { }
    } // Person

    public static class Extensions
    {
        extension<T>(IEnumerable<T> source)
        {
            public bool IsEmpty => !source.Any();
            public IEnumerable<T> Take(int count) => source.Take(count);
        }
        extension<T>(IEnumerable<T>)
        {
            public static IEnumerable<T> Empty => [];
            public static IEnumerable<T> operator +(IEnumerable<T> left, T right) => [];
        }
    }

    public struct Counter
    {
        public void operator +=(int delta) { }
        public void operator ++() { }
        public static Counter operator checked +(Counter left, Counter right) => left;
    }

    public readonly record struct Key(int Value);
    public class @class { int \u03B3amma; void método() { } }
}`

	lineCount := strings.Count(source, "\n") + 1
	tree, ok := parseCSharpSyntax(source, lexCSharp(source))
	if !ok || tree == nil {
		t.Fatal("generated C# grammar did not produce a concrete syntax tree")
	}
	definitions := csharpTreeDefinitions(source, lineCount, tree)
	want := []string{
		"Legacy", "Alias", "Demo.Tools", "Person", "Name", "Age", "first", "second",
		"factory", "Opened", "Closed", "Changed", "Title", "Current", "this", "Person", "Run",
		"Local", "operator+", "implicit operator byte", "explicit operator checked short",
		"~Person", "Extensions", "IsEmpty", "Take", "Empty", "operator+", "Counter",
		"operator+=", "operator++", "operator checked+", "Key", "Value", "@class",
		`\u03B3amma`, "método",
	}
	if got := csharpTreeDefinitionSymbols(definitions); !slices.Equal(got, want) {
		t.Fatalf("concrete C# definitions =\n%#v\nwant\n%#v", got, want)
	}
	for _, forbidden := range []string{
		"extension", "T", "source", "count", "left", "right", "delta", "index",
		"seed", "value", "nested",
	} {
		if slices.Contains(csharpTreeDefinitionSymbols(definitions), forbidden) {
			t.Errorf("non-outline binding %q became a concrete definition", forbidden)
		}
	}

	person := csharpTreeDefinition(t, definitions, "Person")
	if person.scopeStart != csharpTreeLineContaining(t, source, "/// <summary>") ||
		person.scopeEnd != csharpTreeLineContaining(t, source, "} // Person") ||
		!person.ownsScope {
		t.Errorf("documented record scope = %#v", person)
	}
	key := csharpTreeDefinition(t, definitions, "Key")
	if !key.ownsScope || key.scopeStart != key.line || key.scopeEnd != key.line {
		t.Errorf("positional bodyless record scope = %#v", key)
	}
	for _, symbol := range []string{"Name", "Age", "Value"} {
		property := csharpTreeDefinition(t, definitions, symbol)
		if property.ownsScope || property.scopeStart != property.line ||
			property.scopeEnd != property.line {
			t.Errorf("positional property %q scope = %#v", symbol, property)
		}
	}

	imports := csharpTreeImports(source, lineCount, tree)
	if wantImports := []cLineSpan{{start: 1, end: 1}, {start: 2, end: 2}}; !slices.Equal(imports, wantImports) {
		t.Fatalf("concrete C# imports = %#v, want %#v", imports, wantImports)
	}
}

func csharpTreeDefinitionSymbols(definitions []sourceDefinition) []string {
	result := make([]string, len(definitions))
	for index, definition := range definitions {
		result[index] = definition.symbol
	}
	return result
}

func csharpTreeDefinition(
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
	t.Fatalf("missing concrete C# definition %q in %#v", symbol, definitions)
	return sourceDefinition{}
}

func csharpTreeLineContaining(t *testing.T, source, marker string) int {
	t.Helper()
	offset := strings.Index(source, marker)
	if offset < 0 {
		t.Fatalf("marker %q is absent from C# fixture", marker)
	}
	return strings.Count(source[:offset], "\n") + 1
}

func TestCSharpTreeDefinitionsPreferNamesOverInitializerIdentifiers(t *testing.T) {
	const source = `enum E { B = A }
class C {
    string P { get; } = DefaultP;
}
record R(int X = DefaultX, params string[] Values);`
	want := []string{"E", "B", "C", "P", "R", "X", "Values"}
	lines := strings.Split(source, "\n")
	analysis := analyzeCSharpSource(source, len(lines))
	if analysis.tree == nil {
		t.Fatal("valid initializer fixture did not produce a concrete C# tree")
	}
	if got := csharpTreeDefinitionSymbols(csharpTreeDefinitions(
		source, len(lines), analysis.tree,
	)); !slices.Equal(got, want) {
		t.Fatalf("concrete initializer definitions = %#v, want %#v", got, want)
	}
	if got := csharpTreeDefinitionSymbols(analysis.definitions); !slices.Equal(got, want) {
		t.Fatalf("merged initializer definitions = %#v, want %#v", got, want)
	}
	for _, phantom := range []string{"A", "DefaultP", "DefaultX"} {
		if slices.Contains(csharpTreeDefinitionSymbols(analysis.definitions), phantom) {
			t.Errorf("initializer identifier %q became a definition", phantom)
		}
	}
}
