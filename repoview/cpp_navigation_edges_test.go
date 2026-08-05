package repoview

import (
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestCPPIdentifierOccurrenceWalkerUsesSignedLogicalTokenCorrections(t *testing.T) {
	t.Parallel()

	lines := []string{
		"int target\\",
		"_suffix;",
		"int tar\\",
		"get;",
		"double number = 1e\\",
		"+target;",
		"#define CALL() tar\\",
		"get()",
	}
	backend := prepareLanguageBackend(newCPPLanguage(), lines).(cppLanguage)
	collect := func(symbol string) ([]int, bool) {
		adjustments := make([]int, 0, len(lines))
		handled := backend.walkAdditionalSymbolOccurrences(
			lines,
			symbol,
			func(lineNo, adjustment int) bool {
				if lineNo != len(adjustments)+1 {
					t.Fatalf("walker visited line %d after %d lines", lineNo, len(adjustments))
				}
				adjustments = append(adjustments, adjustment)
				return true
			},
		)
		return adjustments, handled
	}
	for _, test := range []struct {
		symbol string
		want   []int
	}{
		{symbol: "target", want: []int{-1, 0, 1, 0, 0, -1, 1, 0}},
		{symbol: "tar", want: []int{0, 0, -1, 0, 0, 0, -1, 0}},
		{symbol: "get", want: []int{0, 0, 0, -1, 0, 0, 0, -1}},
	} {
		adjustments, handled := collect(test.symbol)
		if !handled || !slices.Equal(adjustments, test.want) {
			t.Errorf("%s splice adjustments = %#v, handled=%v; want %#v",
				test.symbol, adjustments, handled, test.want)
		}
	}

	root := t.TempDir()
	writeFile(t, root, "splice.cpp", strings.Join(lines, "\n")+"\n")
	view := mustView(t, root)
	found, err := view.Find(
		"target",
		Options{Include: IncludeBoth, Return: ReturnLocations, NoComments: true, NoStrings: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cppResultLines(found.Results), []int{3, 7}; !slices.Equal(got, want) {
		t.Fatalf("logical target Find lines = %#v, want %#v; results=%#v",
			got, want, found.Results)
	}
	if got, want := cppResultKinds(found.Results), []string{"def", "ref"}; !slices.Equal(got, want) {
		t.Fatalf("logical target Find kinds = %#v, want %#v; results=%#v",
			got, want, found.Results)
	}
	for _, fragment := range []string{"tar", "get"} {
		partial, findErr := view.Find(
			fragment,
			Options{Include: IncludeBoth, Return: ReturnLocations, NoComments: true, NoStrings: true},
		)
		if findErr != nil {
			t.Fatal(findErr)
		}
		if len(partial.Results) != 0 {
			t.Errorf("physical splice fragment %q matched: %#v", fragment, partial.Results)
		}
	}
}

func TestCPPCompositeOccurrenceReconciliationRemainsTokenBased(t *testing.T) {
	t.Parallel()

	lines := []string{
		"api\\",
		"::target();",
		"operator\\",
		"++(value);",
	}
	backend := prepareLanguageBackend(newCPPLanguage(), lines).(cppLanguage)
	for _, test := range []struct {
		symbol string
		line   int
	}{
		{symbol: "api::target", line: 1},
		{symbol: "operator++", line: 3},
	} {
		nonzero := map[int]int{}
		handled := backend.walkAdditionalSymbolOccurrences(
			lines,
			test.symbol,
			func(lineNo, adjustment int) bool {
				if adjustment != 0 {
					nonzero[lineNo] = adjustment
				}
				return true
			},
		)
		if !handled || len(nonzero) != 1 || nonzero[test.line] != 1 {
			t.Errorf("%s composite corrections = %#v, handled=%v; want line %d => 1",
				test.symbol, nonzero, handled, test.line)
		}
	}
}

func TestCPPInspectRejectsOpaqueMultilinePayloadsAndKeepsCloserSuffixes(t *testing.T) {
	t.Parallel()

	const source = `void caller() {
    const char* raw = R"tag(
raw_target();
)tag"; real_after_raw();
    /*
comment_target();
*/ real_after_comment();
    const char* text = "prefix\
string_target";
}
`
	lines := cppTestLines(source)
	root := t.TempDir()
	writeFile(t, root, "opaque.cpp", source)
	view := mustView(t, root)

	for _, payload := range []string{"raw_target", "comment_target", "string_target"} {
		lineNo := cppLineContaining(t, lines, payload)
		response, err := view.Inspect(
			"opaque.cpp:"+strconv.Itoa(lineNo),
			Options{Include: IncludeScope, Return: ReturnLocations},
		)
		if err != nil {
			t.Fatal(err)
		}
		if response.Symbol != "" {
			t.Errorf("Inspect opaque %s line symbol = %q, want none",
				payload, response.Symbol)
		}
	}
	for _, suffix := range []string{"real_after_raw", "real_after_comment"} {
		lineNo := cppLineContaining(t, lines, suffix)
		response, err := view.Inspect(
			"opaque.cpp:"+strconv.Itoa(lineNo),
			Options{Include: IncludeScope, Return: ReturnLocations},
		)
		if err != nil {
			t.Fatal(err)
		}
		if response.Symbol != suffix {
			t.Errorf("Inspect closer suffix line symbol = %q, want %q",
				response.Symbol, suffix)
		}
	}
}

func TestCPPInspectPrioritizesSpecialCallsAndNonCallTemplateHeads(t *testing.T) {
	t.Parallel()

	const source = `void caller() {
    obj.operator++();
    operator+(a, b);
    obj.~Widget();
    sink = &target<Custom>;
}
`
	lines := cppTestLines(source)
	root := t.TempDir()
	writeFile(t, root, "semantic.cpp", source)
	view := mustView(t, root)
	for _, test := range []struct {
		needle string
		want   string
	}{
		{needle: "obj.operator", want: "operator++"},
		{needle: "operator+(a", want: "operator+"},
		{needle: "~Widget", want: "~Widget"},
		{needle: "&target", want: "target"},
	} {
		lineNo := cppLineContaining(t, lines, test.needle)
		response, err := view.Inspect(
			"semantic.cpp:"+strconv.Itoa(lineNo),
			Options{Include: IncludeScope, Return: ReturnLocations},
		)
		if err != nil {
			t.Fatal(err)
		}
		if response.Symbol != test.want {
			t.Errorf("Inspect %q symbol = %q, want %q",
				test.needle, response.Symbol, test.want)
		}
	}
}
