package navigator

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestCC23AndGNUExtensionRecoveryKeepsDeclarationsSemantic(t *testing.T) {
	t.Parallel()

	const source = `[[maybe_unused]]
constexpr int answer = 42;
unsigned _BitInt(17) bit_count;
enum color : unsigned char {
    COLOR_RED,
    COLOR_BLUE = 7,
};
typeof(answer) copy_of_answer;
typeof_unqual(answer) plain_answer;
auto inferred = 7;
nullptr_t null_value = nullptr;
int variadic(...);
int unnamed(int, double);
__declspec(dllexport) int ms_export(void);
__attribute__((cold)) int gnu_export(void);
int terminal_label(int ready)
{
done:
    if (ready) goto done;
last:
}
int statement_expression(void)
{
    int ordinary_local = ({ int nested_local = 1; nested_local; });
    int nested(int value) { return value; }
    return nested(ordinary_local);
}
`
	definitions := newCLanguage().sourceDefinitions(cHighLevelTestLines(source))
	want := []string{
		"answer", "bit_count", "color", "COLOR_RED", "COLOR_BLUE",
		"copy_of_answer", "plain_answer", "inferred", "null_value", "variadic",
		"unnamed", "ms_export", "gnu_export", "terminal_label",
		"statement_expression", "nested",
	}
	if got := cHighLevelDefinitionSymbols(definitions); !slices.Equal(got, want) {
		t.Fatalf("C23/GNU recovery definitions = %#v, want %#v", got, want)
	}
	for _, forbidden := range []string{
		"ready", "last", "done", "ordinary_local", "nested_local", "value",
		"cold", "dllexport",
	} {
		if slices.Contains(cHighLevelDefinitionSymbols(definitions), forbidden) {
			t.Errorf("C23/GNU non-definition %q was promoted: %#v", forbidden, definitions)
		}
	}

	terminal := cHighLevelDefinitionNamed(t, definitions, "terminal_label")
	if terminal.scopeStart != 16 || terminal.scopeEnd != 21 || !terminal.ownsScope {
		t.Fatalf("terminal-label function = %#v, want owning scope 16-21", terminal)
	}
	statement := cHighLevelDefinitionNamed(t, definitions, "statement_expression")
	if statement.scopeStart != 22 || statement.scopeEnd != 27 || !statement.ownsScope {
		t.Fatalf("statement-expression function = %#v, want owning scope 22-27", statement)
	}
	nested := cHighLevelDefinitionNamed(t, definitions, "nested")
	if nested.scopeStart != 25 || nested.scopeEnd != 25 || !nested.ownsScope {
		t.Fatalf("GNU nested function = %#v, want owning scope 25-25", nested)
	}
}

func TestCPreprocessorBranchesRemainVisibleButMacroBodiesStayOpaque(t *testing.T) {
	t.Parallel()

	const source = `#define DECLARE(name) int name(void)
#define JOIN(left, right) left ## right
#define STRINGIFY(value) #value
#define MAKE_FUNCTION(name) int name(void) { return 0; }
#if FEATURE
int enabled(void);
#else
int disabled(void);
#endif
#if 0
{
#endif
int recovered(void) { return 0; }
`
	definitions := newCLanguage().sourceDefinitions(cHighLevelTestLines(source))
	want := []string{
		"DECLARE", "JOIN", "STRINGIFY", "MAKE_FUNCTION", "enabled", "disabled", "recovered",
	}
	if got := cHighLevelDefinitionSymbols(definitions); !slices.Equal(got, want) {
		t.Fatalf("conditional definitions = %#v, want %#v", got, want)
	}
	for _, phantom := range []string{"name", "left", "right", "value"} {
		if slices.Contains(cHighLevelDefinitionSymbols(definitions), phantom) {
			t.Errorf("macro replacement token %q became a definition: %#v", phantom, definitions)
		}
	}
	recovered := cHighLevelDefinitionNamed(t, definitions, "recovered")
	if recovered.scopeStart != 13 || recovered.scopeEnd != 13 || !recovered.ownsScope {
		t.Fatalf("preprocessor-brace suffix recovery = %#v, want line 13 function", recovered)
	}
}

func TestCCleanCallExpressionsNeverBecomeLexicalDefinitions(t *testing.T) {
	t.Parallel()

	const source = `int caller(int bar)
{
    foo(bar);
    allocator(sizeof(int));
    object_handler(bar + 1);
    int local_prototype(int);
    return 0;
}
`
	definitions := newCLanguage().sourceDefinitions(cHighLevelTestLines(source))
	if got, want := cHighLevelDefinitionSymbols(definitions),
		[]string{"caller", "local_prototype"}; !slices.Equal(got, want) {
		t.Fatalf("call-expression definitions = %#v, want %#v", got, want)
	}
}

func TestCTranslationPhaseSplicesCRLFAndDigraphsKeepPhysicalCoordinates(t *testing.T) {
	t.Parallel()

	source := "#inc\\\r\nlude <stddef.h>\r\n" +
		"%:include \"digraph.h\"\r\n" +
		"#define SPLI\\\r\nCED_MACRO 1\r\n" +
		"%:define DIGRAPH_MACRO 1\r\n" +
		"int spli\\\r\nced(void)\r\n" +
		"<%\r\n" +
		"    return 0;\r\n" +
		"%>\r\n" +
		"int caller(void)\r\n" +
		"<%\r\n" +
		"    return spli\\\r\nced();\r\n" +
		"%>\r\n"
	lines := cHighLevelTestLines(source)
	backend := newCLanguage()
	if start, end, ok := backend.importRange(lines); !ok || start != 1 || end != 3 {
		t.Fatalf("spliced/digraph imports = %d-%d, %v; want 1-3, true", start, end, ok)
	}

	definitions := backend.sourceDefinitions(lines)
	wantSymbols := []string{"SPLICED_MACRO", "DIGRAPH_MACRO", "spliced", "caller"}
	if got := cHighLevelDefinitionSymbols(definitions); !slices.Equal(got, wantSymbols) {
		t.Fatalf("spliced/digraph definitions = %#v, want %#v", got, wantSymbols)
	}
	wantMetadata := []cHighLevelDefinitionSummary{
		{
			symbol: "SPLICED_MACRO", line: 4, column: 9, scopeStart: 4, scopeEnd: 5,
		},
		{
			symbol: "DIGRAPH_MACRO", line: 6, column: 10, scopeStart: 6, scopeEnd: 6,
		},
		{
			symbol: "spliced", line: 7, column: 5, scopeStart: 7, scopeEnd: 11, ownsScope: true,
		},
		{
			symbol: "caller", line: 12, column: 5, scopeStart: 12, scopeEnd: 16, ownsScope: true,
		},
	}
	for _, want := range wantMetadata {
		definition := cHighLevelDefinitionNamed(t, definitions, want.symbol)
		got := cHighLevelDefinitionSummaryOf(definition)
		if got != want {
			t.Errorf("%s metadata = %#v, want %#v", want.symbol, got, want)
		}
	}

	root := t.TempDir()
	writeFile(t, root, "translation.c", source)
	found, err := mustView(t, root).Find(
		"spliced",
		Options{Include: IncludeBoth, Return: ReturnLocations, NoComments: true, NoStrings: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := resultLines(found.Results), []int{7, 14}; !reflect.DeepEqual(got, want) {
		t.Fatalf("spliced Find lines = %#v, want physical starts %#v", got, want)
	}
	if got, want := cHighLevelResultKinds(found.Results), []string{"def", "ref"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("spliced Find kinds = %#v, want %#v", got, want)
	}
	for _, fragment := range []string{"spli", "ced"} {
		partial, err := mustView(t, root).Find(
			fragment,
			Options{Include: IncludeBoth, Return: ReturnLocations, NoComments: true, NoStrings: true},
		)
		if err != nil {
			t.Fatal(err)
		}
		if len(partial.Results) != 0 {
			t.Errorf("physical splice fragment %q matched: %#v", fragment, partial.Results)
		}
	}
}

func TestCSpliceOccurrenceWalkerAddsLogicalNamesAndRemovesFragments(t *testing.T) {
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
	backend := prepareLanguageBackend(newCLanguage(), lines).(cLanguage)
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
}

func TestCSpliceOccurrenceWalkerReportsExactCorrectionColumns(t *testing.T) {
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
	backend := prepareLanguageBackend(newCLanguage(), lines).(cLanguage)
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
	if !handled || !reflect.DeepEqual(corrections, want) {
		t.Fatalf("positional splice corrections = %#v, handled=%v; want %#v",
			corrections, handled, want)
	}
}

func TestCFindUsesExactSpliceCorrectionPositionsOnDenseLines(t *testing.T) {
	for _, test := range []struct {
		name, source, want string
	}{
		{
			name:   "spliced reference",
			source: "void first(void) {} void second(void) { tar\\\nget(); }\n",
			want:   "second",
		},
		{
			name:   "earlier physical reference",
			source: "void first(void) { target(); } void second(void) { tar\\\nget(); }\n",
			want:   "first",
		},
		{
			name:   "definition before spliced reference",
			source: "void target(void) {} void second(void) { tar\\\nget(); }\n",
			want:   "second",
		},
		{
			name: "rejected numeric fragment before reference",
			source: "void first(void) { double n = 1e\\\n" +
				"+target; } void second(void) { target(); }\n",
			want: "second",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, root, "dense.c", test.source)
			found, err := mustView(t, root).Find("target", Options{
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

func TestCStreamSymbolOnLineRespectsPreprocessorContext(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		source string
		line   int
		want   string
		ok     bool
	}{
		{
			name:   "defined operator",
			source: "#if defined(FEATURE)\n#endif\n",
			line:   1,
			want:   "FEATURE",
			ok:     true,
		},
		{
			name:   "directive keyword before parentheses",
			source: "#include(foo)\n",
			line:   1,
			want:   "foo",
			ok:     true,
		},
		{
			name:   "digraph directive marker",
			source: "%:if defined(DIGRAPH)\n%:endif\n",
			line:   1,
			want:   "DIGRAPH",
			ok:     true,
		},
		{
			name:   "stringification marker in replacement list",
			source: "#define STRINGIFY(include) \\\n#include\n",
			line:   2,
			want:   "include",
			ok:     true,
		},
		{
			name:   "call and member priority",
			source: "void caller(void) {\nleft + service.run(argument);\n}\n",
			line:   2,
			want:   "run",
			ok:     true,
		},
		{
			name:   "numeric only line",
			source: "void caller(void) {\n1234;\n}\n",
			line:   2,
		},
		{
			name:   "comment only line",
			source: "void caller(void) {\n/* no symbol */\nlater();\n}\n",
			line:   2,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			lines := strings.Split(strings.TrimSuffix(test.source, "\n"), "\n")
			backend := prepareLanguageBackend(newCLanguage(), lines).(cLanguage)
			got, ok := backend.symbolOnLine(lines, test.line)
			if got != test.want || ok != test.ok {
				t.Fatalf("symbolOnLine(%d) = %q, %v; want %q, %v",
					test.line, got, ok, test.want, test.ok)
			}
		})
	}
}

func TestCInvalidBracedUCNDoesNotSuppressSplicedSuffixOccurrence(t *testing.T) {
	t.Parallel()

	const source = "\\u{0061}\\\ntarget;\n"
	root := t.TempDir()
	writeFile(t, root, "invalid-ucn.c", source)
	response, err := mustView(t, root).Find(
		"target",
		Options{Include: IncludeBoth, Return: ReturnLocations},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := resultLines(response.Results), []int{2}; !slices.Equal(got, want) {
		t.Fatalf("target lines after invalid braced UCN = %#v, want %#v", got, want)
	}
}

func TestCMalformedDeclarationsRecoverIndependentSuffixes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		source string
	}{
		{
			name: "broken parameter list",
			source: `int before(void) {}
int broken(
int after(void) {}
`,
		},
		{
			name: "broken initializer",
			source: `int before(void) {}
int broken = { .member = ; };
int after(void) {}
`,
		},
		{
			name: "malformed enhanced enum",
			source: `int before(void) {}
enum Broken : { BROKEN_VALUE };
int after(void) {}
`,
		},
		{
			name: "unterminated conditional",
			source: `int before(void) {}
#if FEATURE
int branch_object;
int after(void) {}
`,
		},
		{
			name: "stray closing delimiters",
			source: `int before(void) {}
}]);
int after(void) {}
`,
		},
		{
			name: "newline terminates malformed string",
			source: `int before(void) {}
const char *broken = "unterminated
int after(void) {}
`,
		},
		{
			name: "malformed final suffix preserves prefix",
			source: `int before(void) {}
int unfinished(
`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			definitions := newCLanguage().sourceDefinitions(cHighLevelTestLines(test.source))
			symbols := cHighLevelDefinitionSymbols(definitions)
			if !slices.Contains(symbols, "before") {
				t.Fatalf("malformed source lost prefix definition: %#v", definitions)
			}
			if strings.Contains(test.source, "int after") && !slices.Contains(symbols, "after") {
				t.Fatalf("malformed source lost independent suffix definition: %#v", definitions)
			}
			for _, phantom := range []string{"member", "FEATURE"} {
				if slices.Contains(symbols, phantom) {
					t.Errorf("malformed syntax promoted %q: %#v", phantom, definitions)
				}
			}
		})
	}

	const unterminatedComment = `int visible(void) {}
/* unterminated comment
int hidden(void) {}
`
	if got := cHighLevelDefinitionSymbols(
		newCLanguage().sourceDefinitions(cHighLevelTestLines(unterminatedComment)),
	); !slices.Equal(got, []string{"visible"}) {
		t.Fatalf("unterminated block comment definitions = %#v, want visible only", got)
	}
}

func TestCDefinitionMergeKeepsConcreteMetadataAndGatesLexicalRecovery(t *testing.T) {
	t.Parallel()

	const source = "x\nx\nx\nx\nx\nx\nx\nx\n"
	lineStarts := cLineStarts(source)
	concrete := sourceDefinition{
		symbol: "exact", line: 1, column: 1,
		scopeStart: 1, scopeEnd: 3, ownsScope: true,
	}
	lexical := []sourceDefinition{
		{
			symbol: "exact", line: 1, column: 1,
			scopeStart: 1, scopeEnd: 8, ownsScope: true,
		},
		{symbol: "clean_guess", line: 4, column: 1, scopeStart: 4, scopeEnd: 4},
		{symbol: "error_candidate", line: 5, column: 1, scopeStart: 5, scopeEnd: 5},
		{symbol: "trusted_gap", line: 6, column: 1, scopeStart: 6, scopeEnd: 6},
		{symbol: "resynced_tail", line: 7, column: 1, scopeStart: 7, scopeEnd: 7},
	}
	trusted := map[cDefinitionIdentity]bool{cDefinitionKey(lexical[3]): true}
	recovered := map[cDefinitionIdentity]bool{cDefinitionKey(lexical[4]): true}
	definitions := cCombinedDefinitions(
		8,
		[]sourceDefinition{concrete},
		lexical,
		true,
		[]cByteSpan{{start: lineStarts[4], end: lineStarts[4] + 1}},
		lineStarts,
		trusted,
		recovered,
	)
	if got, want := cHighLevelDefinitionSymbols(definitions),
		[]string{"exact", "error_candidate", "trusted_gap", "resynced_tail"}; !slices.Equal(got, want) {
		t.Fatalf("gated definition merge = %#v, want %#v", got, want)
	}
	if definitions[0] != concrete {
		t.Fatalf("lexical duplicate changed concrete metadata: %#v", definitions[0])
	}
}

func TestCUnicodeUCNAndDollarIdentifiersUseExactBoundaries(t *testing.T) {
	t.Parallel()

	const source = `int café(void) { return 1; }
int r\u00E9sum\u00E9(void) { return 2; }
int dollar$value(void) { return 3; }
int caller(void)
{
    return café() + r\u00E9sum\u00E9() + dollar$value();
}
`
	definitions := newCLanguage().sourceDefinitions(cHighLevelTestLines(source))
	wantSymbols := []string{"café", `r\u00E9sum\u00E9`, "dollar$value", "caller"}
	if got := cHighLevelDefinitionSymbols(definitions); !slices.Equal(got, wantSymbols) {
		t.Fatalf("extended identifier definitions = %#v, want %#v", got, wantSymbols)
	}

	root := t.TempDir()
	writeFile(t, root, "identifiers.c", source)
	view := mustView(t, root)
	for _, test := range []struct {
		symbol string
		lines  []int
	}{
		{symbol: "café", lines: []int{1, 6}},
		{symbol: `r\u00E9sum\u00E9`, lines: []int{2, 6}},
		{symbol: "dollar$value", lines: []int{3, 6}},
	} {
		response, err := view.Find(test.symbol, Options{
			Include: IncludeBoth, Return: ReturnLocations, NoComments: true, NoStrings: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := resultLines(response.Results); !reflect.DeepEqual(got, test.lines) {
			t.Errorf("Find(%q) lines = %#v, want %#v", test.symbol, got, test.lines)
		}
	}
	for _, partial := range []string{"caf", "sum", "u00E9", "value"} {
		response, err := view.Find(partial, Options{Include: IncludeBoth, Return: ReturnLocations})
		if err != nil {
			t.Fatal(err)
		}
		if len(response.Results) != 0 {
			t.Errorf("partial extended identifier %q matched: %#v", partial, response.Results)
		}
	}

	inspected := cHighLevelInspectAtLine(t, view, "identifiers.c", 6)
	if inspected.Symbol != "café" || len(inspected.Results) != 1 ||
		inspected.Results[0].Scope != "caller" {
		t.Fatalf("Inspect extended call = %#v, want café in caller", inspected)
	}
}

func TestCUsesUnicode17XIDIdentifierProperties(t *testing.T) {
	t.Parallel()

	const unicode17 = "\U00011DB0"
	if !cIdentifierRune([]rune(unicode17)[0], true) {
		t.Fatalf("Unicode 17 XID_Start %U was rejected", []rune(unicode17)[0])
	}
	if cIdentifierRune('\u037a', true) {
		t.Fatal("non-XID U+037A was accepted as an identifier start")
	}
	if !cIdentifierRune('\u00b7', false) {
		t.Fatal("XID_Continue U+00B7 was rejected")
	}
	counter := newCLanguage()
	if got := counter.countSymbolOccurrences("1é", "é"); got != 0 {
		t.Fatalf("Unicode preprocessing-number suffix count = %d, want 0", got)
	}
	if got := counter.countSymbolOccurrences(`1\u00E9`, `\u00E9`); got != 0 {
		t.Fatalf("UCN preprocessing-number suffix count = %d, want 0", got)
	}

	source := "int " + unicode17 + "suffix(void) { return 1; }\n" +
		"int a\u00b7b(void) { return 2; }\n" +
		"int \u037abad(void) { return 3; }\n"
	definitions := newCLanguage().sourceDefinitions(cHighLevelTestLines(source))
	if got, want := cHighLevelDefinitionSymbols(definitions),
		[]string{unicode17 + "suffix", "a\u00b7b"}; !slices.Equal(got, want) {
		t.Fatalf("XID definitions = %#v, want %#v", got, want)
	}
}

func TestCInvalidUTF8AndMalformedInputsNeverPanic(t *testing.T) {
	t.Parallel()

	invalidUTF8 := "int before(void) {}\nconst char *payload = \"" +
		string([]byte{0xff, 0xfe}) + "\";\n// " + string([]byte{0xc0}) +
		"\nint after(void) {}\n"
	corpus := []string{
		"",
		"int open(\n",
		"struct Open { int field;\n",
		"enum Open : unsigned { FIRST,\n",
		"#include \\\n+  <open.h\n",
		"#define OPEN(x) \\\n+  ((x) + 1\n",
		"const char *text = \"unterminated\nint recovered(void) {}\n",
		"/* unterminated\nint hidden(void) {}\n",
		invalidUTF8,
	}
	for index, source := range corpus {
		t.Run(fmt.Sprintf("case_%d", index), func(t *testing.T) {
			t.Parallel()
			lines := cHighLevelTestLines(source)
			backend := prepareLanguageBackend(newCLanguage(), lines)
			_ = backend.sourceDefinitions(lines)
			_, _, _ = backend.importRange(lines)
			for _, options := range [][2]bool{
				{false, false}, {true, false}, {false, true}, {true, true},
			} {
				searchable := backend.searchLines(lines, options[0], options[1])
				if len(searchable) != len(lines) ||
					len(strings.Join(searchable, "\n")) != len(strings.Join(lines, "\n")) {
					t.Fatalf("search mask changed coordinates: %#v", searchable)
				}
			}
			_ = backend.ignoredSearchLines(lines, true, false)
			_ = backend.cleanSource(source, true, false)
			_, _ = backend.enclosingScope(lines, 1)
			_, _ = backend.enclosingScope(lines, len(lines))
			if resolver, ok := backend.(navigationScopeResolver); ok {
				_, _ = resolver.navigationScope(lines, 1)
				_, _ = resolver.navigationScope(lines, len(lines))
			}
			for _, line := range lines {
				_, _ = backend.definitionSymbol(line)
				_ = backend.stripComment(line)
			}
		})
	}

	root := t.TempDir()
	writeFile(t, root, "invalid.c", invalidUTF8)
	outline, err := mustView(t, root).Outline("invalid.c", Options{Return: ReturnLocations})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cHighLevelResultSymbols(outline.Results), []string{"before", "payload", "after"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("invalid-UTF-8 outline = %#v, want %#v", got, want)
	}
}

func TestCLargeAndDeepSmokeRetainsTrailingDeclarations(t *testing.T) {
	macro := "#define GIANT_MACRO(value) " + strings.Repeat("((value) + ", 4096) +
		"0" + strings.Repeat(")", 4096)
	comment := "/* " + strings.Repeat("{ int hidden(void); } ", 2048) + "*/"
	deepBody := strings.Repeat("    if (1) {\n", 128) +
		"        target();\n" + strings.Repeat("    }\n", 128)
	source := macro + "\n" + comment + "\nint deep(void)\n{\n" + deepBody +
		"    return 0;\n}\nint trailing(void) { return deep(); }\n"
	lines := cHighLevelTestLines(source)
	backend := prepareLanguageBackend(newCLanguage(), lines)
	definitions := backend.sourceDefinitions(lines)
	if got, want := cHighLevelDefinitionSymbols(definitions),
		[]string{"GIANT_MACRO", "deep", "trailing"}; !slices.Equal(got, want) {
		t.Fatalf("large/deep definitions = %#v, want %#v", got, want)
	}
	searchable := backend.searchLines(lines, true, true)
	if len(searchable) != len(lines) ||
		len(strings.Join(searchable, "\n")) != len(strings.Join(lines, "\n")) {
		t.Fatalf("large/deep search mask changed coordinates")
	}
	targetLine := cHighLevelLineContaining(t, lines, "target();")
	start, end := backend.(navigationScopeResolver).navigationScope(lines, targetLine)
	deep := cHighLevelDefinitionNamed(t, definitions, "deep")
	if start != deep.scopeStart || end != deep.scopeEnd {
		t.Fatalf("deep target navigation = %d-%d, want %d-%d",
			start, end, deep.scopeStart, deep.scopeEnd)
	}
}

func FuzzCHighLevelBackendNeverPanics(f *testing.F) {
	for _, source := range []string{
		"int main(void) { return 0; }\n",
		"#include <stddef.h>\n#define VALUE 1\n",
		"struct S { int field; }; enum E { A, B };\n",
		"int (*signal(int, void (*)(int)))(int);\n",
		"int old(a) int a; { return a; }\n",
		"constexpr int value = 1; unsigned _BitInt(9) bits;\n",
		"#if X\nint first(void);\n#else\nint second(void);\n#endif\n",
		"/* } */ int visible(void) { const char *s = \"{\"; }\n",
		"int broken(\nint recovered(void) {}\n",
		string([]byte{0xff, 0xfe, 0x00, '{', '}'}),
	} {
		f.Add(source)
	}

	f.Fuzz(func(t *testing.T, source string) {
		if len(source) > 64<<10 {
			t.Skip()
		}
		lines := cHighLevelTestLines(source)
		backend := prepareLanguageBackend(newCLanguage(), lines)
		definitions := backend.sourceDefinitions(lines)
		_, _, _ = backend.importRange(lines)
		_ = backend.ignoredSearchLines(lines, true, false)
		for _, options := range [][2]bool{
			{false, false}, {true, false}, {false, true}, {true, true},
		} {
			searchable := backend.searchLines(lines, options[0], options[1])
			if len(searchable) != len(lines) {
				t.Fatalf("searchable lines = %d, want %d", len(searchable), len(lines))
			}
		}
		_ = backend.cleanSource(source, true, false)
		_, _ = backend.enclosingScope(lines, 1)
		_, _ = backend.enclosingScope(lines, len(lines))
		if resolver, ok := backend.(navigationScopeResolver); ok {
			_, _ = resolver.navigationScope(lines, 1)
			_, _ = resolver.navigationScope(lines, len(lines))
		}
		for _, definition := range definitions {
			if definition.line < 1 || definition.line > len(lines) ||
				definition.column < 1 || definition.scopeStart < 1 ||
				definition.scopeEnd < definition.scopeStart || definition.scopeEnd > len(lines) {
				t.Fatalf("definition coordinates outside source: %#v (lines=%d)",
					definition, len(lines))
			}
		}
	})
}
