package navigator

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestTypeScriptTSXFallbackGenericArrowDoesNotSuppressLaterJSX(t *testing.T) {
	t.Parallel()

	const source = `const identity = <T extends object>(value: T = /[)]/.exec(")") as T): T => value;
const view = <Panel title="secret">function Fake() {} require("phantom") {target}</Panel>;
const tail = require("tail");`
	fallback, lexed := typeScriptTSXFallbackForTest(source)
	if len(fallback.jsxValues) != 1 {
		t.Fatalf("JSX values = %#v, want one Panel value", fallback.jsxValues)
	}
	if got, want := typeScriptTSXLexicalDefinitionSymbols(lexed),
		[]string{"identity", "view", "tail"}; !slices.Equal(got, want) {
		t.Fatalf("definitions = %#v, want %#v", got, want)
	}
	if got, want := lexed.imports,
		[]javascriptLineSpan{{start: 3, end: 3}}; !slices.Equal(got, want) {
		t.Fatalf("imports = %#v, want %#v", got, want)
	}
	masked := maskJavaScriptSource(source, fallback.literals)
	for _, hidden := range []string{"secret", "Fake", "phantom"} {
		if strings.Contains(masked, hidden) {
			t.Fatalf("JSX text/attribute %q leaked through fallback mask: %q", hidden, masked)
		}
	}
	if !strings.Contains(masked, "target") {
		t.Fatalf("JSX expression was hidden by fallback mask: %q", masked)
	}
}

func TestTypeScriptTSXFallbackKeepsGenuineExtendsAttributeJSX(t *testing.T) {
	t.Parallel()

	const source = `const view = <T extends object>(visible)</T>;`
	fallback, lexed := typeScriptTSXFallbackForTest(source)
	if len(fallback.jsxValues) != 1 {
		t.Fatalf("JSX values = %#v, want genuine T element", fallback.jsxValues)
	}
	if got := typeScriptTSXLexicalDefinitionSymbols(lexed); !slices.Equal(got, []string{"view"}) {
		t.Fatalf("definitions = %#v, want view", got)
	}
	masked := maskJavaScriptSource(source, fallback.literals)
	if strings.Contains(masked, "visible") {
		t.Fatalf("genuine JSX text remained visible: %q", masked)
	}
}

func TestTypeScriptTSXFallbackSupportsJSXTypeArguments(t *testing.T) {
	t.Parallel()

	const source = `const view = <Component<Map<Key, Value>> title="secret">
  function Fake() {} require("phantom")
  <Select<Option> value={load<Component<Props>>(option)} />
</Component>;
const tail = require("tail");`
	fallback, lexed := typeScriptTSXFallbackForTest(source)
	if len(fallback.jsxValues) != 1 {
		t.Fatalf("JSX values = %#v, want one root value", fallback.jsxValues)
	}
	if got, want := typeScriptTSXLexicalDefinitionSymbols(lexed),
		[]string{"view", "tail"}; !slices.Equal(got, want) {
		t.Fatalf("definitions = %#v, want %#v", got, want)
	}
	if got, want := lexed.imports,
		[]javascriptLineSpan{{start: 5, end: 5}}; !slices.Equal(got, want) {
		t.Fatalf("imports = %#v, want %#v", got, want)
	}
	masked := maskJavaScriptSource(source, fallback.literals)
	for _, hidden := range []string{"secret", "Fake", "phantom"} {
		if strings.Contains(masked, hidden) {
			t.Fatalf("JSX content %q leaked through fallback mask: %q", hidden, masked)
		}
	}
	for _, visible := range []string{"Map", "Key", "Value", "Option", "Props", "option"} {
		if !strings.Contains(masked, visible) {
			t.Fatalf("type/expression symbol %q was hidden: %q", visible, masked)
		}
	}
}

func TestTypeScriptTSXFallbackTypeArgumentShadowIgnoresLiteralLookalikes(t *testing.T) {
	t.Parallel()

	const source = `const text = "<A<";
const pattern = /<B</;
const view = <Component<Props> title="secret">hidden</Component>;`
	fallback, lexed := typeScriptTSXFallbackForTest(source)
	if len(fallback.jsxValues) != 1 {
		t.Fatalf("JSX values = %#v, want Component after literal lookalikes", fallback.jsxValues)
	}
	if got, want := typeScriptTSXLexicalDefinitionSymbols(lexed),
		[]string{"text", "pattern", "view"}; !slices.Equal(got, want) {
		t.Fatalf("definitions = %#v, want %#v", got, want)
	}
	masked := maskJavaScriptSource(source, fallback.literals)
	if strings.Contains(masked, "secret") || strings.Contains(masked, "hidden") {
		t.Fatalf("JSX after literal lookalikes was not masked: %q", masked)
	}
}

func TestTypeScriptTSXFallbackGenericChecksStayBounded(t *testing.T) {
	const count = 10_000
	var source strings.Builder
	for index := range count {
		fmt.Fprintf(&source, "const f%d = <T extends object>(value: T) => value;\n", index)
	}
	source.WriteString(`const view = <Panel>function Fake() {} require("phantom")</Panel>;
const tail = require("tail");`)

	fallback, lexed := typeScriptTSXFallbackForTest(source.String())
	if len(fallback.jsxValues) != 1 {
		t.Fatalf("JSX values = %d, want one after %d generic arrows", len(fallback.jsxValues), count)
	}
	if got, want := lexed.imports,
		[]javascriptLineSpan{{start: count + 2, end: count + 2}}; !slices.Equal(got, want) {
		t.Fatalf("imports = %#v, want %#v", got, want)
	}
	definitions := typeScriptTSXLexicalDefinitionSymbols(lexed)
	if len(definitions) != count+2 || definitions[0] != "f0" ||
		definitions[count-1] != fmt.Sprintf("f%d", count-1) ||
		definitions[count] != "view" || definitions[count+1] != "tail" {
		t.Fatalf("bounded scan definitions = %d, first/last=%q/%q", len(definitions),
			definitions[0], definitions[len(definitions)-1])
	}

	malformed := strings.Repeat("<A", 20_000)
	if shadow := javascriptFallbackTSXTypeArgumentShadow(malformed); len(shadow) != len(malformed) {
		t.Fatalf("malformed type-argument shadow changed length: %d, want %d",
			len(shadow), len(malformed))
	}
}

func TestTypeScriptTSXTypeArgumentShadowBoundsLiteralCommentLookalikes(t *testing.T) {
	const count = 20_000
	source := `const noise = "` + strings.Repeat(`<A</*`, count) +
		`"; const view = <Panel<Props> />;`
	shadow := javascriptFallbackTSXTypeArgumentShadow(source)
	if len(shadow) != len(source) {
		t.Fatalf("type-argument shadow length = %d, want %d", len(shadow), len(source))
	}
	if !strings.Contains(shadow, "<Panel        />") {
		t.Fatalf("trailing JSX type arguments were not shadowed: %q",
			shadow[max(0, len(shadow)-80):])
	}
}

func FuzzTypeScriptTSXFallbackMaintainsCoordinateContracts(f *testing.F) {
	f.Add(`const id = <T,>(value: T) => value; const view = <C<P> />;`)
	f.Add(`const view = <C<Map<K, V>>>{load<C<P>>(value)}</C>;`)
	f.Add(`const broken = <T extends { value: string }>(value: T`)

	f.Fuzz(func(t *testing.T, source string) {
		fallback := scanJavaScriptFallbackFlavor(source, javascriptSyntaxFlavorTSX)
		for _, spans := range [][]javascriptByteSpan{
			fallback.comments,
			fallback.literals,
			fallback.lexicalSkips,
			fallback.lexicalLiterals,
			fallback.jsxValues,
		} {
			previousEnd := 0
			for index, span := range spans {
				if span.start < 0 || span.end < span.start || span.end > len(source) ||
					index > 0 && span.start < previousEnd {
					t.Fatalf("invalid fallback spans %#v for %q", spans, source)
				}
				previousEnd = span.end
			}
		}
		for _, span := range fallback.objectBraces {
			if span.start < 0 || span.end < span.start || span.end > len(source) {
				t.Fatalf("invalid object-brace span %#v for %q", span, source)
			}
		}
	})
}

func typeScriptTSXFallbackForTest(source string) (javascriptFallbackResult, javascriptLexResult) {
	fallback := scanJavaScriptFallbackFlavor(source, javascriptSyntaxFlavorTSX)
	return fallback, lexJavaScriptWithHints(
		source,
		fallback.comments,
		fallback.literals,
		true,
		nil,
		fallback,
	)
}

func typeScriptTSXLexicalDefinitionSymbols(lexed javascriptLexResult) []string {
	definitions := make([]sourceDefinition, 0, len(lexed.definitions))
	for _, candidate := range lexed.definitions {
		definitions = append(definitions, candidate.definition)
	}
	return typeScriptDefinitionSymbols(sortUniqueJavaScriptDefinitions(definitions))
}
