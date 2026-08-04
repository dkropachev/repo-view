package repoview

import (
	"reflect"
	"testing"
)

func TestJava26PrimitivePatternsParseConcretely(t *testing.T) {
	t.Parallel()

	const source = `class PrimitivePatterns {
    boolean exact(short value) {
        return value instanceof byte narrowed && narrowed > 0;
    }
    void route(long value) {
        switch (value) {
            case long item when item < 0L -> negative(item);
            case 0L -> zero();
            default -> positive(value);
        }
    }
}`
	javaAssertConcreteSyntax(t, source)
	lines := javaTestLines(source)
	backend := newJavaLanguage().prepareSource(lines).(javaLanguage)
	if got, want := javaDefinitionSymbols(backend.sourceDefinitions(lines)),
		[]string{"PrimitivePatterns", "exact", "route"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Java 26 primitive-pattern definitions = %#v, want %#v", got, want)
	}
	for _, test := range []struct {
		line   int
		symbol string
	}{
		{line: 7, symbol: "negative"},
		{line: 8, symbol: "zero"},
		{line: 9, symbol: "positive"},
	} {
		if symbol, ok := backend.symbolOnLine(lines, test.line); !ok || symbol != test.symbol {
			t.Errorf("line %d symbol = %q, %v; want %q, true",
				test.line, symbol, ok, test.symbol)
		}
	}
}

func TestJavaLexicalSelectionHandlesAnnotatedGenericDottedRecordPattern(t *testing.T) {
	t.Parallel()

	const source = `class C {
    boolean run(Object input) {
		return input instanceof @Marker Outer.Inner<String>(int x) && ready();
    }
}`
	lines := javaTestLines(source)
	backend := newJavaLanguage().prepareSource(lines).(javaLanguage)
	if symbol, ok := backend.symbolOnLine(lines, 3); !ok || symbol != "ready" {
		t.Fatalf("annotated generic record-pattern symbol = %q, %v; want ready, true",
			symbol, ok)
	}
}

func TestJavaMalformedInstanceofRecordPatternIsRejected(t *testing.T) {
	t.Parallel()

	const source = `input instanceof @Marker Outer.Inner<java.util.Map<String>(int x)`
	lexed := lexJava(source)
	delimiters := analyzeJavaDelimiters(lexed.tokens)
	instanceof := -1
	for index, token := range lexed.tokens {
		if token.value == "instanceof" {
			instanceof = index
			break
		}
	}
	if instanceof < 0 {
		t.Fatal("fixture has no instanceof token")
	}
	if start, end, ok := javaLexicalInstanceofRecordPattern(
		lexed.tokens, delimiters, instanceof,
	); ok {
		t.Fatalf("malformed record pattern accepted at %d-%d", start, end)
	}
}
