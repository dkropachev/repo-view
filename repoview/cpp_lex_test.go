package repoview

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestCPPLexRawStringsUseExactDelimitersAndOpaquePayloads(t *testing.T) {
	t.Parallel()

	const source = `// R"comment(fake)comment"
const char *ordinary = "R\"not raw";
const char *first = R"(plain // { target })";
const char *second = u8R"TAG(payload )wrong\" /* target */ )TAG"_suffix;
const wchar_t *third = LR"xy(
#include <fake.hpp>
} target(); {
)xy";
int visible() { return 1; }
`
	lexed := lexCPP(source)
	if got, want := len(lexed.stringSpans), 4; got != want {
		t.Fatalf("literal spans = %d, want %d: %#v", got, want, lexed.stringSpans)
	}
	masked := maskCSource(source, append(
		append([]cByteSpan(nil), lexed.commentSpans...), lexed.stringSpans...,
	))
	if strings.Contains(masked, "fake.hpp") || strings.Contains(masked, "target") {
		t.Fatalf("raw payload survived opaque mask:\n%s", masked)
	}
	if !strings.Contains(masked, "visible") || !strings.Contains(masked, "_suffix") {
		t.Fatalf("code or user-defined suffix was swallowed:\n%s", masked)
	}
	if got := cppDefinitionSymbols(newCPPLanguage().sourceDefinitions(cppTestLines(source))); !slices.Contains(got, "visible") {
		t.Fatalf("raw payload hid trailing definition: %#v", got)
	}

	unterminated := `const char *raw = R"tag(payload } int phantom();`
	unterminatedLexed := lexCPP(unterminated)
	if len(unterminatedLexed.stringSpans) != 1 ||
		unterminatedLexed.stringSpans[0].end != len(unterminated) {
		t.Fatalf("unterminated raw span = %#v, want tail ownership", unterminatedLexed.stringSpans)
	}
	if got := cppDefinitionSymbols(newCPPLanguage().sourceDefinitions([]string{unterminated})); len(got) != 0 {
		t.Fatalf("unterminated raw literal produced definitions: %#v", got)
	}
}

func TestCPPIdentifiersCoverUCNRecoveryFormsAndRejectInvalidStarts(t *testing.T) {
	t.Parallel()

	// Basic-character UCN spellings are deliberately tolerated so navigation
	// remains useful on malformed or extension-heavy source. Every accepted
	// spelling still resolves to an XID character and remains source-backed.
	for _, identifier := range []string{
		"ordinary", "κόσμος", `\u0061lpha`, `\U000003B2eta`,
		`\u{3B3}amma`, `\N{LATIN CAPITAL LETTER A}lpha`,
		`\N{KELVIN SIGN}elvin`, `accent\N{COMBINING ACUTE ACCENT}`,
		"$extension",
	} {
		if !cppSourceIdentifier(identifier) {
			t.Errorf("supported identifier spelling %q rejected", identifier)
		}
	}
	for _, identifier := range []string{
		"", "1name", `\u0030name`, `\u{30}name`, `\u{}`, `\u{110000}`,
		`\u{100000041}`, `\U0000D800`, `\N{}name`, `\N{SPACE}name`,
		`\N{LETTER}name`, `\N{LATIN CAPITAL LETTER MADE UP}name`,
		"\\N{bad\nname}", "bad-name",
	} {
		if cppSourceIdentifier(identifier) {
			t.Errorf("invalid identifier %q accepted", identifier)
		}
	}

	counter := newCPPLanguage()
	line := `\u{3B3}amma + \u{3B3}amma2 + 1\u{3B3}amma + "\u{3B3}amma"`
	if got := counter.countSymbolOccurrences(line, `\u{3B3}amma`); got != 2 {
		t.Fatalf("braced UCN occurrences = %d, want 2", got)
	}
}

func TestCPPNamedUniversalCharactersResolveExactUnicodeNames(t *testing.T) {
	t.Parallel()

	for name, want := range map[string]rune{
		"LATIN CAPITAL LETTER A":     'A',
		"KELVIN SIGN":                '\u212A',
		"COMBINING ACUTE ACCENT":     '\u0301',
		"CJK UNIFIED IDEOGRAPH-4E00": '\u4E00',
		"HANGUL SYLLABLE GA":         '\uAC00',
		"TANGUT IDEOGRAPH-17000":     '\U00017000',
		"LATIN CAPITAL LETTER GHA":   '\u01A2',
	} {
		got, ok := cppNamedUCNRune(name)
		if !ok || got != want {
			t.Errorf("named UCN %q = %U, %v; want %U, true", name, got, ok, want)
		}
	}
	for _, name := range []string{
		"LETTER", "LATIN CAPITAL LETTER MADE UP", "kelvin sign",
		"CJK UNIFIED IDEOGRAPH-04E00", "CJK UNIFIED IDEOGRAPH-4e00",
	} {
		if got, ok := cppNamedUCNRune(name); ok {
			t.Errorf("nonexistent named UCN %q resolved to %U", name, got)
		}
	}
}

func TestCPPLogicalIdentifiersRemoveSplicesInsideUniversalCharacterNames(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		physical string
		logical  string
	}{
		{physical: "\\u0\\\n3B3amma", logical: `\u03B3amma`},
		{physical: "\\u{3\\\nB3}amma", logical: `\u{3B3}amma`},
		{
			physical: "\\N{GREEK SMALL LETTER GA\\\nMMA}amma",
			logical:  `\N{GREEK SMALL LETTER GAMMA}amma`,
		},
	} {
		end := cppLogicalIdentifierEnd(test.physical, 0)
		if end != len(test.physical) {
			t.Errorf("logical identifier %q ended at %d, want %d",
				test.physical, end, len(test.physical))
			continue
		}
		if got := cLogicalText(test.physical, 0, end); got != test.logical {
			t.Errorf("logical identifier text = %q, want %q", got, test.logical)
		}
	}
	if end := cppLogicalIdentifierEnd("\\u{110\\\n000}invalid", 0); end != 0 {
		t.Fatalf("spliced out-of-range UCN ended at %d, want rejection", end)
	}
}

func TestCPPLexModulesAreContextualAndHeaderUnitsAreImports(t *testing.T) {
	t.Parallel()

	const source = `module;
export module demo.core:part;
import std;
export import demo.util;
import :detail;
import <vector>;
import "local.hpp";
int import = 1;
void caller() {
    import(runtime);
    const char *text = "import fake.module;";
}
`
	lexed := lexCPP(source)
	if got, want := cppDefinitionSymbols(lexed.trustedDefinitions),
		[]string{"demo.core:part"}; !slices.Equal(got, want) {
		t.Fatalf("trusted module definitions = %#v, want %#v", got, want)
	}
	wantImports := []cLineSpan{
		{start: 3, end: 3}, {start: 4, end: 4}, {start: 5, end: 5},
		{start: 6, end: 6}, {start: 7, end: 7},
	}
	if !reflect.DeepEqual(lexed.imports, wantImports) {
		t.Fatalf("module imports = %#v, want %#v", lexed.imports, wantImports)
	}
}

func TestCPPModuleScannerRejectsMalformedOperandsAndPreservesContextualIdentifiers(t *testing.T) {
	t.Parallel()

	const source = `int keep = 1; import demo; int after = 2;
module bad..name;
import <>;
int module() { return keep; }
int import(int value) { return value; }
`
	lines := cppTestLines(source)
	backend := newCPPLanguage()
	if start, end, ok := backend.importRange(lines); !ok || start != 1 || end != 1 {
		t.Fatalf("same-line valid import range = %d-%d, %v; want 1-1, true", start, end, ok)
	}
	if got, want := cppDefinitionSymbols(backend.sourceDefinitions(lines)),
		[]string{"keep", "after", "module", "import"}; !slices.Equal(got, want) {
		t.Fatalf("contextual identifier definitions = %#v, want %#v", got, want)
	}
	malformed := cppTestLines("module bad..name;\nimport <>;\n")
	if _, _, ok := backend.importRange(malformed); ok {
		t.Fatal("malformed module/import operands became dependencies")
	}
}

func TestCPPOccurrenceCounterUsesIdentifiersNumbersAndCompositeBoundaries(t *testing.T) {
	t.Parallel()

	counter := newCPPLanguage()
	for _, test := range []struct {
		line   string
		symbol string
		want   int
	}{
		{line: "target target2 _target target", symbol: "target", want: 2},
		{line: "0x1.targetp0 42_target target", symbol: "target", want: 1},
		{line: `"target" /* target */ target`, symbol: "target", want: 3},
		{line: "api::target api::targeted xapi::target", symbol: "api::target", want: 1},
		{line: "~Box(); Other::~Box(); ~Boxes();", symbol: "~Box", want: 2},
		{line: "operator+(a, b); operator++(a);", symbol: "operator+", want: 1},
	} {
		if got := counter.countSymbolOccurrences(test.line, test.symbol); got != test.want {
			t.Errorf("count(%q, %q) = %d, want %d", test.line, test.symbol, got, test.want)
		}
	}
}
