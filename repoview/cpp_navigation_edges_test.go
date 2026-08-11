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

func TestCPPIdentifierOccurrenceWalkerReportsExactCorrectionColumns(t *testing.T) {
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
	type correction struct {
		adjustment int
		added      []int
		removed    []int
	}
	corrections := make(map[int]correction)
	handled := backend.walkAdditionalSymbolOccurrencesAt(
		lines, "target",
		func(
			lineNo, adjustment int,
			addedColumns, removedColumns []int,
		) bool {
			if adjustment != 0 || len(addedColumns) > 0 || len(removedColumns) > 0 {
				corrections[lineNo] = correction{
					adjustment: adjustment,
					added:      append([]int(nil), addedColumns...),
					removed:    append([]int(nil), removedColumns...),
				}
			}
			return true
		},
	)
	want := map[int]correction{
		1: {adjustment: -1, removed: []int{strings.Index(lines[0], "target") + 1}},
		3: {adjustment: 1, added: []int{strings.Index(lines[2], "tar") + 1}},
		6: {adjustment: -1, removed: []int{strings.Index(lines[5], "target") + 1}},
		7: {adjustment: 1, added: []int{strings.Index(lines[6], "tar") + 1}},
	}
	if !handled || len(corrections) != len(want) {
		t.Fatalf("positional identifier corrections = %#v, handled=%v; want %#v",
			corrections, handled, want)
	}
	for lineNo, expected := range want {
		got := corrections[lineNo]
		if got.adjustment != expected.adjustment ||
			!slices.Equal(got.added, expected.added) ||
			!slices.Equal(got.removed, expected.removed) {
			t.Errorf("line %d correction = %#v, want %#v", lineNo, got, expected)
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

func TestCPPCompositeOccurrencesIncludePreprocessorDirectiveBodies(t *testing.T) {
	t.Parallel()

	const source = `#define CALL() api :: target()
#define COMMENT_CALL() api /* bridge */ :: target()
#define PREFIX api
::target();
void f() { CALL(); COMMENT_CALL(); }
`
	root := t.TempDir()
	writeFile(t, root, "directive.cpp", source)
	found, err := mustView(t, root).Find("api::target", Options{
		Include: IncludeRefs, Return: ReturnLocations,
		NoComments: true, NoStrings: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cppResultLines(found.Results), []int{1, 2}; !slices.Equal(got, want) {
		t.Fatalf("directive composite Find lines = %#v, want %#v; results=%#v",
			got, want, found.Results)
	}
}

func TestCPPCompositeReconciliationIgnoresOpaquePhysicalSpellings(t *testing.T) {
	t.Parallel()

	const source = `void f() { /* api::target */ api/**/::target(); }
`
	root := t.TempDir()
	writeFile(t, root, "opaque-spelling.cpp", source)
	found, err := mustView(t, root).Find("api::target", Options{
		Include: IncludeRefs, Return: ReturnLocations,
		NoComments: true, NoStrings: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cppResultLines(found.Results), []int{1}; !slices.Equal(got, want) {
		t.Fatalf("composite Find lines = %#v, want %#v; results=%#v",
			got, want, found.Results)
	}
}

func TestCPPCompositeOccurrenceWalkerReportsExactCodeCorrections(t *testing.T) {
	t.Parallel()

	line := "void f() { /* api::target */ 0x1.api::target; api::target(); api /*x*/ :: target(); }"
	lines := []string{line}
	backend := prepareLanguageBackend(newCPPLanguage(), lines).(cppLanguage)
	adjustment := 99
	var added, removed []int
	handled := backend.walkAdditionalSymbolOccurrencesAt(
		lines, "api::target",
		func(
			lineNo, additional int,
			addedColumns, removedColumns []int,
		) bool {
			if lineNo != 1 {
				t.Fatalf("composite walker visited line %d, want 1", lineNo)
			}
			adjustment = additional
			added = append([]int(nil), addedColumns...)
			removed = append([]int(nil), removedColumns...)
			return true
		},
	)
	falseStart := strings.Index(line, "0x1.api::target") + len("0x1.") + 1
	hiddenStart := strings.Index(line, "api /*x*/ :: target") + 1
	if !handled || adjustment != 0 ||
		!slices.Equal(added, []int{hiddenStart}) ||
		!slices.Equal(removed, []int{falseStart}) {
		t.Fatalf("composite corrections = adjustment %d, added %#v, removed %#v, handled %v",
			adjustment, added, removed, handled)
	}
}

func TestCPPCompositeOccurrenceWalkerRemovesUnmatchedCodeSpelling(t *testing.T) {
	t.Parallel()

	line := "void f() { /* api::target */ 0x1.api::target; }"
	lines := []string{line}
	backend := prepareLanguageBackend(newCPPLanguage(), lines).(cppLanguage)
	adjustment := 0
	var added, removed []int
	handled := backend.walkAdditionalSymbolOccurrencesAt(
		lines, "api::target",
		func(
			_ int, additional int,
			addedColumns, removedColumns []int,
		) bool {
			adjustment = additional
			added = append([]int(nil), addedColumns...)
			removed = append([]int(nil), removedColumns...)
			return true
		},
	)
	falseStart := strings.Index(line, "0x1.api::target") + len("0x1.") + 1
	if !handled || adjustment != -1 || len(added) != 0 ||
		!slices.Equal(removed, []int{falseStart}) {
		t.Fatalf("unmatched code correction = adjustment %d, added %#v, removed %#v, handled %v",
			adjustment, added, removed, handled)
	}

	legacyAdjustment := 0
	if !backend.walkAdditionalSymbolOccurrences(
		lines, "api::target",
		func(_ int, additional int) bool {
			legacyAdjustment = additional
			return true
		},
	) || legacyAdjustment != -1 {
		t.Fatalf("legacy unmatched code adjustment = %d, want -1", legacyAdjustment)
	}
}

func TestCPPFindUsesExactOccurrencePositionsOnDenseLines(t *testing.T) {
	for _, test := range []struct {
		name, source, symbol, want string
	}{
		{
			name:   "trivia-spanning composite",
			source: "class C { void first() {} void second() { api /*x*/ :: target(); } };\n",
			symbol: "api::target", want: "second",
		},
		{
			name:   "earlier physical composite",
			source: "class C { void first() { api::target(); } void second() { api /*x*/ :: target(); } };\n",
			symbol: "api::target", want: "first",
		},
		{
			name:   "numeric false spelling before hidden composite",
			source: "class C { void first() { double n = 0x1.api::target; } void second() { api /*x*/ :: target(); } };\n",
			symbol: "api::target", want: "second",
		},
		{
			name:   "opaque spelling before hidden composite",
			source: "class C { void first() { /* api::target */ } void second() { api /*x*/ :: target(); } };\n",
			symbol: "api::target", want: "second",
		},
		{
			name:   "spliced composite",
			source: "class C { void first() {} void second() { api\\\n::target(); } };\n",
			symbol: "api::target", want: "second",
		},
		{
			name:   "definition before spliced identifier reference",
			source: "void target() {} void second() { tar\\\nget(); }\n",
			symbol: "target", want: "second",
		},
		{
			name: "rejected numeric identifier before reference",
			source: "void first() { double n = 1e\\\n" +
				"+target; } void second() { target(); }\n",
			symbol: "target", want: "second",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, root, "dense.cpp", test.source)
			found, err := mustView(t, root).Find(test.symbol, Options{
				Include: IncludeRefs, Return: ReturnScope,
				NoComments: true, NoStrings: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(found.Results) != 1 || found.Results[0].Scope != test.want {
				t.Fatalf("dense positional Find = %#v, want scope %q",
					found.Results, test.want)
			}
		})
	}
}

func TestCPPCompositeReconciliationStreamsPastTokenRetention(t *testing.T) {
	paddingLines := cppMaximumRetainedTokens + 32
	source := strings.Repeat(";\n", paddingLines) +
		"void f() { api :: target(); }\n" +
		"void g() { api\\\n::target(); }\n"
	lines := cppTestLines(source)
	backend := prepareLanguageBackend(newCPPLanguage(), lines).(cppLanguage)
	if backend.analysis == nil || !backend.analysis.lexed.truncated {
		t.Fatal("fixture did not cross the retained-token frontier")
	}

	root := t.TempDir()
	writeFile(t, root, "truncated.cpp", source)
	found, err := mustView(t, root).Find("api::target", Options{
		Include: IncludeRefs, Return: ReturnLocations,
		NoComments: true, NoStrings: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []int{paddingLines + 1, paddingLines + 2}
	if got := cppResultLines(found.Results); !slices.Equal(got, want) {
		t.Fatalf("truncated composite Find lines = %#v, want %#v; results=%#v",
			got, want, found.Results)
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
