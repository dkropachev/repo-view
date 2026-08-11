package navigator

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestJavaScriptPreparedBackendRejectsStaleAndMutatedAnalysis(t *testing.T) {
	t.Parallel()

	first := []string{"function first() {}"}
	prepared, ok := prepareLanguageBackend(
		newJavaScriptLanguage("javascript"), first,
	).(javascriptLanguage)
	if !ok || prepared.analysis == nil {
		t.Fatal("JavaScript backend was not prepared")
	}
	if got := javascriptDefinitionSymbols(prepared.sourceDefinitions(first)); !slices.Equal(got, []string{"first"}) {
		t.Fatalf("prepared definitions = %#v, want first", got)
	}

	second := []string{"function second() {}"}
	if got := javascriptDefinitionSymbols(prepared.sourceDefinitions(second)); !slices.Equal(got, []string{"second"}) {
		t.Fatalf("stale-slice definitions = %#v, want second", got)
	}

	first[0] = "function mutated() {}"
	if got := javascriptDefinitionSymbols(prepared.sourceDefinitions(first)); !slices.Equal(got, []string{"mutated"}) {
		t.Fatalf("mutated-source definitions = %#v, want mutated", got)
	}

	empty, ok := prepared.prepareSource(nil).(javascriptLanguage)
	if !ok || empty.analysis != nil || len(empty.sourceDefinitions(nil)) != 0 {
		t.Fatalf("empty prepared backend retained analysis: %#v", empty.analysis)
	}
}

func TestJavaScriptPreparedBackendRecomputesEveryDerivedView(t *testing.T) {
	t.Parallel()

	lines := []string{
		`import "dependency";`,
		`function first() {`,
		`  const value = "target";`,
		`}`,
	}
	prepared := prepareLanguageBackend(newJavaScriptLanguage("javascript"), lines)
	if start, end, ok := prepared.importRange(lines); !ok || start != 1 || end != 1 {
		t.Fatalf("prepared import range = %d-%d, %v", start, end, ok)
	}
	if start, end := prepared.enclosingScope(lines, 3); start != 2 || end != 4 {
		t.Fatalf("prepared scope = %d-%d, want 2-4", start, end)
	}
	if strings.Contains(prepared.searchLines(lines, true, true)[2], "target") {
		t.Fatal("prepared string payload remained searchable")
	}

	lines[0] = `const dependency = 1;`
	lines[1] = `function changed() {`
	lines[2] = `  const value = target;`
	if _, _, ok := prepared.importRange(lines); ok {
		t.Fatal("mutated ordinary code retained a stale import range")
	}
	if got := javascriptDefinitionSymbols(prepared.sourceDefinitions(lines)); !slices.Equal(got, []string{"dependency", "changed", "value"}) {
		t.Fatalf("mutated definitions = %#v", got)
	}
	if !strings.Contains(prepared.searchLines(lines, true, true)[2], "target") {
		t.Fatal("mutated executable identifier retained a stale string mask")
	}
	if start, end := prepared.enclosingScope(lines, 3); start != 2 || end != 4 {
		t.Fatalf("mutated scope = %d-%d, want 2-4", start, end)
	}
}

func TestJavaScriptPreparedBackendSupportsConcurrentDistinctInputs(t *testing.T) {
	t.Parallel()

	prepared := prepareLanguageBackend(
		newJavaScriptLanguage("javascript"),
		[]string{"function original() {}"},
	)
	results := make(chan error, 32)
	for index := range cap(results) {
		go func() {
			name := fmt.Sprintf("function_%d", index)
			lines := []string{"function " + name + "() {}"}
			definitions := prepared.sourceDefinitions(lines)
			if len(definitions) != 1 || definitions[0].symbol != name {
				results <- fmt.Errorf("definitions for %s = %#v", name, definitions)
				return
			}
			if masked := prepared.searchLines(lines, true, true); !slices.Equal(masked, lines) {
				results <- fmt.Errorf("search lines for %s = %#v", name, masked)
				return
			}
			results <- nil
		}()
	}
	for range cap(results) {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}

func TestJavaScriptCRLFAndUnicodeLineTerminatorsKeepPhysicalCoordinates(t *testing.T) {
	t.Parallel()

	source := strings.Join([]string{
		`import "dependency";`,
		`/** docs */`,
		`export function crlf(`,
		`  value,`,
		`) {`,
		`  if (value) {`,
		`    target();`,
		`  }`,
		`}`,
	}, "\r\n")
	lines := strings.Split(source, "\n")
	backend := newJavaScriptLanguage("javascript")
	definitions := backend.sourceDefinitions(lines)
	if len(definitions) != 1 || definitions[0].symbol != "crlf" ||
		definitions[0].line != 3 || definitions[0].scopeStart != 2 ||
		definitions[0].scopeEnd != 9 {
		t.Fatalf("CRLF definitions = %#v", definitions)
	}
	if start, end, ok := backend.importRange(lines); !ok || start != 1 || end != 1 {
		t.Fatalf("CRLF imports = %d-%d, %v; want 1-1, true", start, end, ok)
	}
	if start, end := backend.enclosingScope(lines, 7); start != 6 || end != 8 {
		t.Fatalf("CRLF scope = %d-%d, want 6-8", start, end)
	}

	logicalLines := []string{"// hidden\u2028const visible = target; // trailing\u2029target();"}
	searchable := backend.searchLines(logicalLines, true, true)
	if len(searchable) != 1 || len(searchable[0]) != len(logicalLines[0]) {
		t.Fatalf("Unicode-terminator mask changed physical coordinates: %#v", searchable)
	}
	if strings.Contains(searchable[0], "hidden") || strings.Contains(searchable[0], "trailing") ||
		backend.countSymbolOccurrences(searchable[0], "target") != 2 {
		t.Fatalf("Unicode line terminators did not end comments: %q", searchable[0])
	}
}

func TestJavaScriptMalformedSourcesRecoverWithoutPanics(t *testing.T) {
	t.Parallel()

	backend := newJavaScriptLanguage("javascript")
	const mixed = `function valid1() { return 1; }
function broken( { return; }
function valid2() { return 2; }
`
	if got, want := javascriptDefinitionSymbols(
		backend.sourceDefinitions(javascriptTestLines(mixed)),
	), []string{"valid1", "broken", "valid2"}; !slices.Equal(got, want) {
		t.Fatalf("mixed definitions = %#v, want %#v", got, want)
	}

	const incompleteClass = `class Foo {
  constructor(value) { this.value = value; }
  method(
}
function later() {}
`
	if got, want := javascriptDefinitionSymbols(
		backend.sourceDefinitions(javascriptTestLines(incompleteClass)),
	), []string{"Foo", "constructor", "later"}; !slices.Equal(got, want) {
		t.Fatalf("incomplete-class definitions = %#v, want %#v", got, want)
	}

	const unterminatedComment = `function visible() {}
/* unterminated
function hidden() {}
`
	if got := javascriptDefinitionSymbols(
		backend.sourceDefinitions(javascriptTestLines(unterminatedComment)),
	); !slices.Equal(got, []string{"visible"}) {
		t.Fatalf("unterminated-comment definitions = %#v, want visible", got)
	}

	const unterminatedTemplate = "const raw = `unterminated\nfunction hidden() {}\n"
	if got := javascriptDefinitionSymbols(
		backend.sourceDefinitions(javascriptTestLines(unterminatedTemplate)),
	); !slices.Equal(got, []string{"raw"}) {
		t.Fatalf("unterminated-template definitions = %#v, want raw", got)
	}

	invalidUTF8 := "function before() {}\nconst payload = \"" + string([]byte{0xff, 0xfe}) +
		"\";\nfunction after() {}\n// " + string([]byte{0xc0}) + "\n"
	if got, want := javascriptDefinitionSymbols(
		backend.sourceDefinitions(javascriptTestLines(invalidUTF8)),
	), []string{"before", "payload", "after"}; !slices.Equal(got, want) {
		t.Fatalf("invalid-UTF-8 definitions = %#v, want %#v", got, want)
	}

	corpus := []string{
		"",
		"function open(\n",
		"const fn = (value) =>\n",
		"let value = (1 + 2));\nlet after = 3;\n",
		"let first = 1;\n@#$%^&\nlet second = 2;\n",
		"class Broken {\n  method(\n}\nfunction later() {}\n",
		"const text = \"unterminated\nconst after = 1;\n",
		"const text = `unterminated ${value\nconst after = 1;\n",
		"if (left === === right) { call(); }\n",
		"let first = 1 2 3;\nlet second = 2;\n",
		invalidUTF8,
	}
	for index, source := range corpus {
		t.Run(fmt.Sprintf("case_%d", index), func(t *testing.T) {
			t.Parallel()
			lines := strings.Split(source, "\n")
			prepared := prepareLanguageBackend(backend, lines)
			definitions := prepared.sourceDefinitions(lines)
			for _, definition := range definitions {
				if definition.line < 1 || definition.line > len(lines) ||
					definition.column < 1 || definition.scopeStart < 1 ||
					definition.scopeStart > definition.line ||
					definition.scopeEnd < definition.line || definition.scopeEnd > len(lines) {
					t.Fatalf("invalid definition coordinates: %#v", definition)
				}
			}
			_, _, _ = prepared.importRange(lines)
			searchable := prepared.searchLines(lines, true, true)
			if len(searchable) != len(lines) ||
				len(strings.Join(searchable, "\n")) != len(source) {
				t.Fatalf("search mask changed coordinates: %#v", searchable)
			}
			_ = prepared.ignoredSearchLines(lines, true, false)
			_ = prepared.cleanSource(source, true, false)
			_, _ = prepared.enclosingScope(lines, 1)
			_, _ = prepared.enclosingScope(lines, len(lines))
			for _, line := range lines {
				_, _ = prepared.definitionSymbol(line)
				_ = prepared.stripComment(line)
			}
		})
	}

	root := t.TempDir()
	writeFile(t, root, "fixture.js", invalidUTF8)
	outline, err := mustView(t, root).Outline("fixture.js", Options{Return: ReturnLocations})
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(outline.Results))
	for _, result := range outline.Results {
		got = append(got, result.Symbol)
	}
	if want := []string{"before", "payload", "after"}; !slices.Equal(got, want) {
		t.Fatalf("invalid-UTF-8 outline = %#v, want %#v", got, want)
	}
}

func TestJavaScriptKnownValidGrammarErrorsDoNotHideLaterDefinitions(t *testing.T) {
	t.Parallel()

	const source = `using = function(props) {};
const rest = [...warnings, ...(force ? errors : [])];
function after() {}
`
	analysis := analyzeJavaScriptSource(
		strings.TrimSuffix(source, "\n"),
		len(javascriptTestLines(source)),
	)
	if len(analysis.recoverySpans) == 0 {
		t.Fatal("fixture no longer exercises the pinned grammar's recovery path")
	}
	if got, want := javascriptDefinitionSymbols(analysis.definitions), []string{"rest", "after"}; !slices.Equal(got, want) {
		t.Fatalf("definitions = %#v, want %#v", got, want)
	}
}

func TestJavaScriptASIArrayAssignmentsAreNotDeclarations(t *testing.T) {
	t.Parallel()

	const source = `function parse() {
  let argv
  [args, argv] = parseKnown();
  return argv;
}
`
	if got, want := javascriptDefinitionSymbols(
		newJavaScriptLanguage("javascript").sourceDefinitions(javascriptTestLines(source)),
	), []string{"parse", "argv"}; !slices.Equal(got, want) {
		t.Fatalf("definitions = %#v, want %#v", got, want)
	}
}

func TestJavaScriptMalformedLiteralsDoNotInventDefinitions(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		source string
		want   []string
	}{
		{
			name:   "unterminated string",
			source: "const text = \"function Fake() {}\nfunction real() {}\n",
			want:   []string{"text", "real"},
		},
		{
			name:   "unterminated regex",
			source: "const pattern = /function Fake() {}\nfunction real() {}\n",
			want:   []string{"pattern", "real"},
		},
		{
			name:   "error inside standalone block",
			source: "call()\n{\n  @\n}\nfunction real() {}\n",
			want:   []string{"real"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got := javascriptDefinitionSymbols(
				newJavaScriptLanguage("javascript").sourceDefinitions(
					javascriptTestLines(test.source),
				),
			)
			if !slices.Equal(got, test.want) {
				t.Fatalf("definitions = %#v, want %#v", got, test.want)
			}
			if test.name == "error inside standalone block" {
				lines := javascriptTestLines(test.source)
				if start, end := newJavaScriptLanguage("javascript").enclosingScope(lines, 1); start != 1 || end != 1 {
					t.Fatalf("standalone call scope = %d-%d, want 1-1", start, end)
				}
			}
		})
	}
}

func TestJavaScriptUnicodeEscapesPreserveRawSpelling(t *testing.T) {
	t.Parallel()

	const escaped = `\u{00000061}`
	const source = `const \u{00000061} = 1;
\u{00000061};
`
	definitions := newJavaScriptLanguage("javascript").sourceDefinitions(
		javascriptTestLines(source),
	)
	if len(definitions) != 1 || definitions[0].symbol != escaped {
		t.Fatalf("escaped definitions = %#v, want %q", definitions, escaped)
	}
	for _, invalid := range []string{`\u{}`, `\u{110000}`, `\u{D800}`} {
		if javascriptSourceIdentifier(invalid) {
			t.Fatalf("invalid Unicode escape %q was accepted", invalid)
		}
	}
	root := t.TempDir()
	writeFile(t, root, "escaped.js", source)
	response, err := mustView(t, root).Find(escaped, Options{
		Include: IncludeBoth, Return: ReturnLocations,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := resultLines(response.Results); !slices.Equal(got, []int{1, 2}) {
		t.Fatalf("Find(%q) lines = %#v, want 1,2", escaped, got)
	}
}

func TestJavaScriptUsesUnicode17IdentifierProperties(t *testing.T) {
	t.Parallel()

	for name, ranges := range map[string][]javascriptIdentifierRange{
		"start":    javascriptUnicode17IDStartDelta[:],
		"continue": javascriptUnicode17IDContinueDelta[:],
	} {
		for index, span := range ranges {
			if span.first > span.last {
				t.Fatalf("%s range %d is reversed: %#v", name, index, span)
			}
			if index > 0 && ranges[index-1].last >= span.first {
				t.Fatalf("%s ranges %d and %d overlap", name, index-1, index)
			}
		}
	}
	for _, span := range javascriptUnicode17IDStartDelta {
		if !javascriptIdentifierContinueRune(span.first) ||
			!javascriptIdentifierContinueRune(span.last) {
			t.Fatalf("ID_Start delta is not in ID_Continue: %#v", span)
		}
	}

	const unicode17 = "\U00011DB0"
	if !javascriptIdentifierStartRune([]rune(unicode17)[0]) {
		t.Fatalf("Unicode 17 identifier start %U was rejected", []rune(unicode17)[0])
	}
	if javascriptIdentifierStartRune('\u30FB') {
		t.Fatal("continue-only U+30FB was accepted as ID_Start")
	}
	if javascriptIdentifierStartRune('\u2E2F') || javascriptIdentifierContinueRune('\u2E2F') {
		t.Fatal("Unicode 17 removed U+2E2F from identifier properties")
	}
	if !javascriptIdentifierContinueRune('\u30FB') {
		t.Fatal("Unicode 17 ID_Continue U+30FB was rejected")
	}

	source := "const " + unicode17 + "suffix = 1;\n" +
		"const a\u30FBb = 2;\n" +
		unicode17 + "suffix;\n" +
		"a\u30FBb;\n"
	definitions := newJavaScriptLanguage("javascript").sourceDefinitions(
		javascriptTestLines(source),
	)
	if got, want := javascriptDefinitionSymbols(definitions),
		[]string{unicode17 + "suffix", "a\u30FBb"}; !slices.Equal(got, want) {
		t.Fatalf("Unicode 17 definitions = %#v, want %#v", got, want)
	}

	root := t.TempDir()
	writeFile(t, root, "unicode.js", source)
	view := mustView(t, root)
	for _, symbol := range []string{unicode17 + "suffix", "a\u30FBb"} {
		response, err := view.Find(symbol, Options{Include: IncludeBoth, Return: ReturnLocations})
		if err != nil {
			t.Fatal(err)
		}
		if got := resultLines(response.Results); !slices.Equal(got, []int{1, 3}) &&
			symbol == unicode17+"suffix" {
			t.Fatalf("Find(%q) lines = %#v, want 1,3", symbol, got)
		} else if symbol == "a\u30FBb" && !slices.Equal(got, []int{2, 4}) {
			t.Fatalf("Find(%q) lines = %#v, want 2,4", symbol, got)
		}
	}
}

func TestJavaScriptFallbackRegexFlagsAlwaysAdvance(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		source string
		want   int
	}{
		{source: "/a/1", want: 4},
		{source: "/a/\u200C", want: len("/a/\u200C")},
		{source: "/a/\u0301", want: len("/a/\u0301")},
	} {
		if got := javascriptRegexLiteralEnd(test.source, 0); got != test.want {
			t.Fatalf("javascriptRegexLiteralEnd(%q) = %d, want %d", test.source, got, test.want)
		}
	}
}

func TestJavaScriptLexicalRecoveryRetainsLiteralValuesAndContextualOf(t *testing.T) {
	t.Parallel()

	const source = `const plain = "value"
const of = 1;
const first = source.of, handler = () => {
  work();
};
`
	comments, literals := javascriptFallbackMasks(source)
	lexed := lexJavaScript(source, comments, literals, true, nil)
	definitions := make([]sourceDefinition, 0, len(lexed.definitions))
	for _, candidate := range lexed.definitions {
		definitions = append(definitions, candidate.definition)
	}
	if got, want := javascriptDefinitionSymbols(
		sortUniqueJavaScriptDefinitions(definitions),
	), []string{"plain", "of", "first", "handler"}; !slices.Equal(got, want) {
		t.Fatalf("lexical definitions = %#v, want %#v", got, want)
	}
	plain := javascriptDefinitionNamed(t, definitions, "plain")
	if plain.ownsScope || plain.scopeStart != 1 || plain.scopeEnd != 1 {
		t.Fatalf("plain definition = %#v, want physical line only", plain)
	}
	handler := javascriptDefinitionNamed(t, definitions, "handler")
	if !handler.ownsScope || handler.scopeStart != 3 || handler.scopeEnd != 5 {
		t.Fatalf("handler definition = %#v, want owning 3-5", handler)
	}
}

func TestJavaScriptLexicalRecoveryFindsOnlyCanonicalTopLevelRequire(t *testing.T) {
	t.Parallel()

	const source = `const loaded = require("dependency");
require("side-effect");
require(variable, "not-canonical");
require("not-canonical", extra);
object.require("member");
require?.("optional");
function nested() { require("runtime"); }
const callback = () => { require("callback"); };
`
	comments, literals := javascriptFallbackMasks(source)
	lexed := lexJavaScript(source, comments, literals, true, nil)
	if want := []javascriptLineSpan{{start: 1, end: 1}, {start: 2, end: 2}}; !slices.Equal(lexed.imports, want) {
		t.Fatalf("lexical imports = %#v, want %#v", lexed.imports, want)
	}

	const incomplete = `const loaded = require("dependency"`
	lines := javascriptTestLines(incomplete)
	if start, end, ok := newJavaScriptLanguage("javascript").importRange(lines); !ok || start != 1 || end != 1 {
		t.Fatalf("incomplete require range = %d-%d, %v; want 1-1, true", start, end, ok)
	}
}

func TestJavaScriptFallbackLegacyHTMLCloseCommentsUseLogicalTerminators(t *testing.T) {
	t.Parallel()

	for _, separator := range []string{"\r", "\u2028", "\u2029"} {
		source := "code" + separator + "  --> hidden" + separator + "visible"
		comments, _ := javascriptFallbackMasks(source)
		masked := maskJavaScriptSource(source, comments)
		if strings.Contains(masked, "hidden") || !strings.Contains(masked, "visible") ||
			len(masked) != len(source) {
			t.Fatalf("separator %q fallback mask = %q", separator, masked)
		}
	}
}

func TestJavaScriptFallbackLegacyHTMLCloseCommentScanStaysLinear(t *testing.T) {
	t.Parallel()

	source := strings.Repeat("a-->", 1<<15) + " @\n  --> hidden\nvisible"
	comments, _ := javascriptFallbackMasks(source)
	masked := maskJavaScriptSource(source, comments)
	if strings.Contains(masked, "hidden") || !strings.Contains(masked, "visible") ||
		strings.Count(masked, "-->") != 1<<15 {
		t.Fatalf("large legacy-comment mask was incorrect")
	}
}

func TestJavaScriptEarlierSiblingErrorIndexMatchesSiblingOrder(t *testing.T) {
	t.Parallel()

	tree := &javascriptSyntaxTree{
		root: 0,
		nodes: []javascriptSyntaxNode{
			{kind: "program", parent: -1, children: []int{1, 2, 3, 4}},
			{kind: "identifier", parent: 0},
			{kind: "ERROR", parent: 0},
			{kind: "variable_declarator", parent: 0},
			{kind: "variable_declarator", parent: 0},
		},
	}
	if got, want := javascriptEarlierSiblingErrors(tree), []bool{false, false, false, true, true}; !slices.Equal(got, want) {
		t.Fatalf("earlier-sibling errors = %#v, want %#v", got, want)
	}
}

func TestJavaScriptMalformedDeclarationRecoveryStaysBounded(t *testing.T) {
	t.Parallel()

	var source strings.Builder
	for index := range 20_000 {
		fmt.Fprintf(&source, "const x%d = 1 ", index)
	}
	source.WriteString("@")
	comments, literals := javascriptFallbackMasks(source.String())
	lexed := lexJavaScript(source.String(), comments, literals, true, nil)
	if len(lexed.definitions) != 1 || lexed.definitions[0].definition.symbol != "x0" {
		t.Fatalf("malformed declaration recovery returned %d definitions, want only x0", len(lexed.definitions))
	}
}

func TestJavaScriptFallbackDistinguishesRegexFromDivision(t *testing.T) {
	t.Parallel()

	const source = `numerator / target / denominator;
numerator /= target;
fn() / target / other;
count++ / target / other;
if (ready) /target[//]+\/end/giu.test(value);
while (ready) /target/.exec(value);
const arrow = () => /target/;
const template = ` + "`${left / target / right}`" + `;
function matcher() { return /target/; }
if (ready) {} /target/.test(value);
const object = {value: target / divisor};
`
	comments, literals := javascriptFallbackMasks(source)
	masked := strings.Split(maskJavaScriptSource(source, append(comments, literals...)), "\n")
	wantCounts := []int{1, 1, 1, 1, 0, 0, 0, 1, 0, 0, 1, 0}
	backend := newJavaScriptLanguage("javascript")
	for index, line := range masked {
		if got := backend.countSymbolOccurrences(line, "target"); got != wantCounts[index] {
			t.Fatalf("line %d target count = %d, want %d; masked=%q", index+1, got, wantCounts[index], line)
		}
	}
}

func TestJavaScriptFallbackTreatsSlashAfterAnonymousClassAsRegex(t *testing.T) {
	t.Parallel()

	const source = `export default class {}
/function fake()/;
/require("fake")/;
export function after() {}
`
	fallback := scanJavaScriptFallback(source)
	masked := maskJavaScriptSource(source, fallback.literals)
	if strings.Contains(masked, "fake") || strings.Contains(masked, "require") {
		t.Fatalf("anonymous-class regex payload remained searchable:\n%s", masked)
	}
	analysis := analyzeJavaScriptSource(source, 5)
	if got, want := javascriptDefinitionSymbols(analysis.definitions), []string{"after"}; !slices.Equal(got, want) {
		t.Fatalf("anonymous-class regex definitions = %#v, want %#v", got, want)
	}
	if len(analysis.imports) != 0 {
		t.Fatalf("anonymous-class regex created imports: %#v", analysis.imports)
	}
}

func TestJavaScriptFallbackDoesNotCarryAsyncAcrossLineTerminator(t *testing.T) {
	t.Parallel()

	const source = `const value = async
function visible() {}
/require("fake")/.test(value);
`
	analysis := analyzeJavaScriptSource(source, 4)
	if analysis.tree == nil || len(analysis.recoverySpans) != 0 {
		t.Fatal("async-line-terminator fixture did not parse cleanly")
	}
	_, concreteStrings := javascriptSyntaxMasks(source, analysis.tree)
	_, fallbackStrings := javascriptFallbackMasks(source)
	if !slices.Equal(fallbackStrings, concreteStrings) {
		t.Fatalf("async-line-terminator strings = %#v, want %#v",
			fallbackStrings, concreteStrings)
	}
	lexical := javascriptLexicalOnlyForTest(source)
	if len(lexical.imports) != 0 {
		t.Fatalf("async-line-terminator regex created imports: %#v", lexical.imports)
	}
}

func TestJavaScriptFallbackKeepsDivisionAfterCallableExpressionBodies(t *testing.T) {
	t.Parallel()

	for _, source := range []string{
		"const Value = class {} / 2;\nfunction after() {}\n",
		"const value = function() {} / 2;\nfunction after() {}\n",
		"const value = () => {} / 2;\nfunction after() {}\n",
	} {
		tree, ok := parseJavaScriptSyntax(source)
		if !ok || tree == nil || len(javascriptSyntaxErrorSpans(tree, len(source))) != 0 {
			t.Fatalf("fixture did not parse cleanly: %q", source)
		}
		concreteComments, concreteStrings := javascriptSyntaxMasks(source, tree)
		fallbackComments, fallbackStrings := javascriptFallbackMasks(source)
		if !slices.Equal(fallbackComments, concreteComments) ||
			!slices.Equal(fallbackStrings, concreteStrings) {
			t.Fatalf("callable division masks = comments %#v strings %#v; want %#v %#v",
				fallbackComments, fallbackStrings, concreteComments, concreteStrings)
		}
	}
}

func TestJavaScriptFallbackKeepsExecutableDivisionTails(t *testing.T) {
	t.Parallel()

	for _, source := range []string{
		`const value = class {} / require("class-tail");`,
		`const value = function() {} / require("function-tail");`,
		`const value = () => {} / require("arrow-tail");`,
		`object.default / require("keyword-property");`,
		`object?.class / require("optional-keyword-property");`,
	} {
		analysis := analyzeJavaScriptSource(source, 1)
		if analysis.tree == nil || len(analysis.recoverySpans) != 0 {
			t.Fatalf("fixture did not parse cleanly: %q", source)
		}
		lexical := javascriptLexicalOnlyForTest(source)
		if want := []javascriptLineSpan{{start: 1, end: 1}}; !slices.Equal(lexical.imports, want) {
			t.Fatalf("division-tail imports for %q = %#v, want %#v", source, lexical.imports, want)
		}
		_, concreteStrings := javascriptSyntaxMasks(source, analysis.tree)
		_, fallbackStrings := javascriptFallbackMasks(source)
		if !slices.Equal(fallbackStrings, concreteStrings) {
			t.Fatalf("division-tail strings for %q = %#v, want %#v",
				source, fallbackStrings, concreteStrings)
		}
	}
}

func TestJavaScriptFallbackExpiresKeywordPropertyCallableState(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		source string
		eager  bool
	}{
		{`const object = { class: 1, fn: () => {} / require("class-key") };`, true},
		{`const object = { function: 1, fn: () => {} / require("function-key") };`, true},
		{`const object = { const: value / require("const-key") };`, true},
		{`class Value { var = value / require("var-field"); }`, false},
	} {
		source := test.source
		analysis := analyzeJavaScriptSource(source, 1)
		if analysis.tree == nil || len(analysis.recoverySpans) != 0 {
			t.Fatalf("fixture did not parse cleanly: %q", source)
		}
		lexical := javascriptLexicalOnlyForTest(source)
		want := []javascriptLineSpan(nil)
		if test.eager {
			want = []javascriptLineSpan{{start: 1, end: 1}}
		}
		if !slices.Equal(lexical.imports, want) {
			t.Fatalf("keyword-property division imports for %q = %#v, want %#v",
				source, lexical.imports, want)
		}
		_, concreteStrings := javascriptSyntaxMasks(source, analysis.tree)
		_, fallbackStrings := javascriptFallbackMasks(source)
		if !slices.Equal(fallbackStrings, concreteStrings) {
			t.Fatalf("keyword-property division strings for %q = %#v, want %#v",
				source, fallbackStrings, concreteStrings)
		}
	}
}

func TestJavaScriptFallbackRecognizesForAwaitRegexAndScope(t *testing.T) {
	t.Parallel()

	const source = `for await (const value of values) /target/.test(value);`
	tree, ok := parseJavaScriptSyntax(source)
	if !ok || tree == nil || len(javascriptSyntaxErrorSpans(tree, len(source))) != 0 {
		t.Fatal("for-await fixture did not parse cleanly")
	}
	_, concreteStrings := javascriptSyntaxMasks(source, tree)
	_, fallbackStrings := javascriptFallbackMasks(source)
	if !slices.Equal(fallbackStrings, concreteStrings) {
		t.Fatalf("for-await fallback strings = %#v, want %#v", fallbackStrings, concreteStrings)
	}
	lexical := javascriptLexicalOnlyForTest(source)
	if want := []javascriptLineScope{{start: 1, end: 1}}; !slices.Equal(lexical.scopes, want) {
		t.Fatalf("for-await scopes = %#v, want %#v", lexical.scopes, want)
	}
}

func TestJavaScriptFallbackRecognizesRegexAfterUninitializedBindingASI(t *testing.T) {
	t.Parallel()

	for _, source := range []string{
		"let value\n/function fake()/.test(value);",
		"var value\n/require(\"fake\")/.test(value);",
	} {
		analysis := analyzeJavaScriptSource(source, 2)
		if analysis.tree == nil || len(analysis.recoverySpans) != 0 {
			t.Fatalf("fixture did not parse cleanly: %q", source)
		}
		_, concreteStrings := javascriptSyntaxMasks(source, analysis.tree)
		_, fallbackStrings := javascriptFallbackMasks(source)
		if !slices.Equal(fallbackStrings, concreteStrings) {
			t.Fatalf("binding-ASI strings for %q = %#v, want %#v",
				source, fallbackStrings, concreteStrings)
		}
		lexical := javascriptLexicalOnlyForTest(source)
		definitions := make([]sourceDefinition, 0, len(lexical.definitions))
		for _, candidate := range lexical.definitions {
			definitions = append(definitions, candidate.definition)
		}
		if got, want := javascriptDefinitionSymbols(sortUniqueJavaScriptDefinitions(definitions)),
			[]string{"value"}; !slices.Equal(got, want) {
			t.Fatalf("binding-ASI definitions for %q = %#v, want %#v", source, got, want)
		}
		if len(lexical.imports) != 0 {
			t.Fatalf("binding-ASI regex created imports for %q: %#v", source, lexical.imports)
		}
	}
}

func TestJavaScriptFallbackRecognizesRegexAfterRestrictedStatementASI(t *testing.T) {
	t.Parallel()

	for _, source := range []string{
		"while (ready) { break\n/function fake()/.test(value); }",
		"while (ready) { continue\n/require(\"fake\")/.test(value); }",
		"outer: while (ready) { break outer\n/function fake()/.test(value); }",
		"debugger\n/function fake()/.test(value);",
	} {
		_, fallbackStrings := javascriptFallbackMasks(source)
		if len(fallbackStrings) != 1 || fallbackStrings[0].start < 0 ||
			fallbackStrings[0].end > len(source) ||
			!strings.HasPrefix(source[fallbackStrings[0].start:fallbackStrings[0].end], "/") {
			t.Fatalf("restricted-ASI strings for %q = %#v", source, fallbackStrings)
		}
		analysis := analyzeJavaScriptSource(source, 2)
		if len(analysis.imports) != 0 || slices.Contains(
			javascriptDefinitionSymbols(analysis.definitions), "fake",
		) {
			t.Fatalf("restricted-ASI regex leaked syntax for %q: defs=%#v imports=%#v",
				source, analysis.definitions, analysis.imports)
		}
	}
}

func TestJavaScriptCleanSyntaxSkipsBoundedLexicalRecovery(t *testing.T) {
	t.Parallel()

	const depth = 64
	var source strings.Builder
	source.WriteString("const Root = ")
	for index := range depth {
		fmt.Fprintf(&source, "class Nested%d extends ", index)
	}
	source.WriteString("class Leaf {}")
	for range depth {
		source.WriteString(" {}")
	}
	source.WriteString(";\n")
	text := source.String()
	analysis := analyzeJavaScriptSource(text, 1)
	if analysis.tree == nil || len(analysis.recoverySpans) != 0 {
		t.Fatal("valid nested class expressions entered syntax recovery")
	}
	if len(analysis.lexed.tokens) != 0 || len(analysis.lexed.definitions) != 0 {
		t.Fatal("clean syntax performed unnecessary lexical recovery")
	}
	if got, want := len(analysis.definitions), depth+2; got != want {
		t.Fatalf("nested class definitions = %d, want %d", got, want)
	}
}

func TestJavaScriptInspectSelectsConcreteExpressionSymbols(t *testing.T) {
	t.Parallel()

	const source = `function caller() {
  client.session.request(argument);
  new Factory();
  factory[Model]();
  outer(inner());
  void ` + "`Wrong() ${actual()}`" + `;
  <Panel title="Wrong()">Wrong() {render(value)}</Panel>;
  "Wrong()"; right();
  service.make().run();
  client?.session?.request?.();
  object.final;
  return result;
}
client
  .session
  .request();
`
	root := t.TempDir()
	writeFile(t, root, "fixture.js", source)
	view := mustView(t, root)
	for _, test := range []struct {
		line int
		want string
	}{
		{line: 1, want: "caller"},
		{line: 2, want: "request"},
		{line: 3, want: "Factory"},
		{line: 4, want: "factory"},
		{line: 5, want: "outer"},
		{line: 6, want: "actual"},
		{line: 7, want: "render"},
		{line: 8, want: "right"},
		{line: 9, want: "run"},
		{line: 10, want: "request"},
		{line: 11, want: "final"},
		{line: 12, want: "result"},
		{line: 14, want: "client"},
		{line: 15, want: "session"},
		{line: 16, want: "request"},
	} {
		response, err := view.Inspect(
			fmt.Sprintf("fixture.js:%d", test.line),
			Options{Include: IncludeScope, Return: ReturnScope},
		)
		if err != nil {
			t.Fatal(err)
		}
		if response.Symbol != test.want {
			t.Fatalf("Inspect line %d symbol = %q, want %q", test.line, response.Symbol, test.want)
		}
	}
}

func TestJavaScriptAndTypeScriptInspectRejectNumericFragments(t *testing.T) {
	t.Parallel()

	for _, extension := range []string{"js", "ts"} {
		t.Run(extension, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			path := "fixture." + extension
			writeFile(t, root, path, "1e10;\n")
			response, err := mustView(t, root).Inspect(
				path+":1",
				Options{Include: IncludeScope, Return: ReturnLocations},
			)
			if err != nil {
				t.Fatal(err)
			}
			if response.Symbol != "" || len(response.Results) != 1 ||
				response.Results[0].Symbol != "" {
				t.Fatalf("numeric Inspect response = %#v", response)
			}
		})
	}
}

func TestJavaScriptMalformedInspectPreservesECMAScriptNames(t *testing.T) {
	t.Parallel()

	const source = `function broken(
$target(
this.#field(
\u0061lias(
`
	lines := javascriptTestLines(source)
	backend := prepareLanguageBackend(newJavaScriptLanguage("javascript"), lines)
	for _, test := range []struct {
		line int
		want string
	}{
		{line: 2, want: "$target"},
		{line: 3, want: "#field"},
		{line: 4, want: `\u0061lias`},
	} {
		if got, found := backend.(javascriptLanguage).symbolOnLine(lines, test.line); !found || got != test.want {
			t.Fatalf("line %d symbol = %q, %v; want %q, true", test.line, got, found, test.want)
		}
	}

	root := t.TempDir()
	writeFile(t, root, "broken.js", source)
	view := mustView(t, root)
	for _, test := range []struct {
		line int
		want string
	}{{2, "$target"}, {3, "#field"}, {4, `\u0061lias`}} {
		response, err := view.Inspect(
			fmt.Sprintf("broken.js:%d", test.line),
			Options{Include: IncludeScope, Return: ReturnScope},
		)
		if err != nil {
			t.Fatal(err)
		}
		if response.Symbol != test.want {
			t.Fatalf("Inspect line %d symbol = %q, want %q", test.line, response.Symbol, test.want)
		}
	}
}

func TestJavaScriptFindUsesECMAScriptBoundariesAndAllExtensions(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFile(t, root, "alpha.js", "function shared() {}\nshared();\n")
	writeFile(t, root, "beta.mjs", "export function shared() {}\n")
	writeFile(t, root, "gamma.cjs", "exports.shared = function() {};\n")
	writeFile(t, root, "delta.jsx", "export const shared = () => <div />;\n")
	writeFile(t, root, "boundaries.js", "foo;\nobj.foo;\n$foo; foo$bar; foo\u200Cbar; foo\u200Dbar; #foo;\n")

	view := mustView(t, root)
	definitions, err := view.Find("shared", Options{Include: IncludeDefs, Return: ReturnLocations})
	if err != nil {
		t.Fatal(err)
	}
	gotLanguages := make(map[string]string)
	for _, result := range definitions.Results {
		gotLanguages[result.Path] = result.Language
	}
	wantLanguages := map[string]string{
		"alpha.js": "javascript", "beta.mjs": "mjs", "gamma.cjs": "cjs", "delta.jsx": "jsx",
	}
	if !reflect.DeepEqual(gotLanguages, wantLanguages) {
		t.Fatalf("extension results = %#v, want %#v", gotLanguages, wantLanguages)
	}

	references, err := view.Find("foo", Options{
		Include: IncludeRefs, Return: ReturnLocations, NoComments: true, NoStrings: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(references.Results) != 2 || references.Results[0].Path != "boundaries.js" ||
		references.Results[0].Line != 1 || references.Results[1].Line != 2 {
		t.Fatalf("boundary results = %#v, want two standalone/member references", references.Results)
	}
}

func TestJavaScriptFindSeparatesDefinitionAndReferenceOnOneLine(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFile(t, root, "target.js", "const target = target;\n")
	response, err := mustView(t, root).Find("target", Options{
		Include: IncludeBoth, Return: ReturnLocations,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 2 || response.Results[0].Kind != "def" ||
		response.Results[1].Kind != "ref" || response.Results[0].Line != 1 ||
		response.Results[1].Line != 1 {
		t.Fatalf("Find results = %#v, want def and ref on line 1", response.Results)
	}
}

func TestJavaScriptCommentCleaningPreservesLiteralSyntax(t *testing.T) {
	t.Parallel()

	const source = `#!/usr/bin/env node
const url = "https://example.test/path";
/** documentation */
function run() {
  const pattern = /\/\//;
  return url; // trailing comment
}
`
	lines := javascriptTestLines(source)
	backend := prepareLanguageBackend(newJavaScriptLanguage("javascript"), lines)
	cleaner := backend.(linePreservingSourceCleaner)
	cleanedLines := cleaner.cleanSourceLines(lines, true, false)
	if len(cleanedLines) != len(lines) ||
		!strings.Contains(cleanedLines[1], "https://example.test/path") ||
		!strings.Contains(cleanedLines[4], `/\/\//`) ||
		strings.Contains(cleanedLines[5], "trailing comment") {
		t.Fatalf("cleaned lines = %#v", cleanedLines)
	}
	ignored := backend.ignoredSearchLines(lines, true, false)
	if !ignored[1] || !ignored[3] || ignored[6] {
		t.Fatalf("ignored comment lines = %#v", ignored)
	}
	cleaned := backend.cleanSource(strings.TrimSuffix(source, "\n"), true, false)
	if strings.Contains(cleaned, "documentation") || strings.Contains(cleaned, "trailing comment") ||
		!strings.Contains(cleaned, "https://example.test/path") {
		t.Fatalf("cleaned source = %q", cleaned)
	}
}

func TestJavaScriptSearchMasksRegexJSXAndTemplatePayloadsExactly(t *testing.T) {
	t.Parallel()

	const source = `if (ready) /target/.test(input);
const ratio = total / target / unit;
const assignment = target /= 2;
const arrow = () => /target[/]value/giu;
const jsx = <target title="target" target target={target}>target {target}<target>{target}</target></target>;
const template = ` + "`target ${target} nested ${`raw target ${target}`}`" + `;
`
	lines := javascriptTestLines(source)
	backend := prepareLanguageBackend(newJavaScriptLanguage("javascript"), lines)
	searchable := backend.searchLines(lines, true, true)
	wantCounts := []int{0, 1, 1, 0, 7, 2}
	for index, line := range searchable {
		if got := backend.(symbolOccurrenceCounter).countSymbolOccurrences(line, "target"); got != wantCounts[index] {
			t.Fatalf("line %d target count = %d, want %d; masked=%q", index+1, got, wantCounts[index], line)
		}
	}
	if len(strings.Join(searchable, "\n")) != len(strings.Join(lines, "\n")) {
		t.Fatal("search masking changed byte coordinates")
	}
}

func TestJavaScriptLexicalFallbackMatchesControlImportAndScopeContracts(t *testing.T) {
	t.Parallel()

	const source = `/** dependency wrapper */
if (ready) {
  require("dependency");
}
const object = { import: value, export: from };
object.import = value;
object.export = from;
const callback = (item) => {
  if (item) {
    work(item);
  }
};
`
	lexed := javascriptLexicalOnlyForTest(source)
	if want := []javascriptLineSpan{{start: 1, end: 4}}; !slices.Equal(lexed.imports, want) {
		t.Fatalf("lexical imports = %#v, want %#v", lexed.imports, want)
	}
	for _, want := range []javascriptLineScope{
		{start: 2, end: 4},
		{start: 8, end: 12},
		{start: 9, end: 11},
	} {
		if !slices.Contains(lexed.scopes, want) {
			t.Fatalf("lexical scopes = %#v, missing %#v", lexed.scopes, want)
		}
	}
}

func TestJavaScriptLexicalFallbackKeepsCallChainImportOwner(t *testing.T) {
	t.Parallel()

	const source = `new Function("first", "second", function() {
  const value = work();
  return value;
}())(require("dependency"), target);
`
	concrete := analyzeJavaScriptSource(source, 5)
	if concrete.tree == nil || len(concrete.recoverySpans) != 0 {
		t.Fatal("call-chain import fixture did not parse cleanly")
	}
	lexical := javascriptLexicalOnlyForTest(source)
	want := []javascriptLineSpan{{start: 1, end: 4}}
	if !slices.Equal(concrete.imports, want) || !slices.Equal(lexical.imports, want) {
		t.Fatalf("call-chain imports = concrete %#v, fallback %#v; want %#v",
			concrete.imports, lexical.imports, want)
	}
}

func TestJavaScriptLexicalFallbackSeparatesSemicolonlessRequireDeclarations(t *testing.T) {
	t.Parallel()

	const source = `var feature = require("feature").default
var region = require("region").default
var fs = require("fs")
var path = require("path")
`
	concrete := analyzeJavaScriptSource(source, 5)
	lexical := javascriptLexicalOnlyForTest(source)
	want := []javascriptLineSpan{
		{start: 1, end: 1},
		{start: 2, end: 2},
		{start: 3, end: 3},
		{start: 4, end: 4},
	}
	if !slices.Equal(concrete.imports, want) || !slices.Equal(lexical.imports, want) {
		t.Fatalf("semicolonless imports = concrete %#v, fallback %#v; want %#v",
			concrete.imports, lexical.imports, want)
	}
}

func TestJavaScriptLexicalFallbackKeepsTaggedTemplateImportOwner(t *testing.T) {
	t.Parallel()

	const source = "const result = tag\n`value ${require(\"dependency\")}`;\n"
	concrete := analyzeJavaScriptSource(source, 3)
	if concrete.tree == nil || len(concrete.recoverySpans) != 0 {
		t.Fatal("tagged-template import fixture did not parse cleanly")
	}
	lexical := javascriptLexicalOnlyForTest(source)
	want := []javascriptLineSpan{{start: 1, end: 2}}
	if !slices.Equal(concrete.imports, want) || !slices.Equal(lexical.imports, want) {
		t.Fatalf("tagged-template imports = concrete %#v, fallback %#v; want %#v",
			concrete.imports, lexical.imports, want)
	}
}

func TestJavaScriptLexicalFallbackMatchesDeferredRequireContexts(t *testing.T) {
	t.Parallel()

	const source = `function declared(value = require("function-default")) { require("function-body"); }
const arrow = (value = require("arrow-default")) => require("arrow-body");
const object = { "quoted"(value = require("method-default")) { require("method-body"); } };
class Child extends factory(require("heritage"), {}) {
  "quoted"(value = require("class-default")) { require("class-body"); }
  field = require("field");
}
require("top",);
`
	concrete := analyzeJavaScriptSource(source, strings.Count(source, "\n")+1)
	if concrete.tree == nil || len(concrete.recoverySpans) != 0 {
		t.Fatal("deferred-require fixture did not produce clean concrete syntax")
	}
	lexical := javascriptLexicalOnlyForTest(source)
	if !slices.Equal(lexical.imports, concrete.imports) {
		t.Fatalf("deferred fallback imports = %#v, want %#v",
			lexical.imports, concrete.imports)
	}
	if want := []javascriptLineSpan{{start: 4, end: 7}, {start: 8, end: 8}}; !slices.Equal(lexical.imports, want) {
		t.Fatalf("deferred fallback imports = %#v, want %#v", lexical.imports, want)
	}
}

func TestJavaScriptRequireImportsFollowClassEvaluationTiming(t *testing.T) {
	t.Parallel()

	const source = `const object = {
  [require("object-key")]() {}
};
@decorate(require("class-decorator"))
class Service extends require("heritage") {
  @decorate(require("method-decorator"))
  [require("method-key")](value = require("method-default")) {
    require("method-body");
  }
  @decorate(require("instance-decorator"))
  [require("instance-key")] = require("instance-value");
  @decorate(require("static-decorator"))
  static [require("static-key")] = require("static-value");
  static {
    require("static-block");
    const deferred = () => require("static-block-arrow");
  }
}
`
	concrete := analyzeJavaScriptSource(source, strings.Count(source, "\n")+1)
	if concrete.tree == nil || len(concrete.recoverySpans) != 0 {
		t.Fatal("class-evaluation fixture did not produce clean concrete syntax")
	}
	want := []javascriptLineSpan{{start: 1, end: 3}, {start: 4, end: 18}}
	if !slices.Equal(concrete.imports, want) {
		t.Fatalf("concrete class-evaluation imports = %#v, want %#v", concrete.imports, want)
	}
	lexical := javascriptLexicalOnlyForTest(source)
	if !slices.Equal(lexical.imports, want) {
		t.Fatalf("fallback class-evaluation imports = %#v, want %#v", lexical.imports, want)
	}
}

func TestJavaScriptRequireEvaluationBoundariesPerCall(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		source string
		eager  bool
	}{
		{"object computed method key", `const value = { [require("key")]() {} };`, true},
		{"object computed method default", `const value = { [key](item = require("default")) {} };`, false},
		{"object computed method body", `const value = { [key]() { require("body"); } };`, false},
		{"class decorator", `@decorate(require("class-decorator")) class Value {}`, true},
		{"method decorator", `class Value { @decorate(require("method-decorator")) method() {} }`, true},
		{"class computed method key", `class Value { [require("method-key")]() {} }`, true},
		{"class computed method default", `class Value { [key](item = require("default")) {} }`, false},
		{"class computed method body", `class Value { [key]() { require("body"); } }`, false},
		{"instance field decorator", `class Value { @decorate(require("field-decorator")) field; }`, true},
		{"instance computed field key", `class Value { [require("field-key")]; }`, true},
		{"instance field initializer", `class Value { field = require("instance-value"); }`, false},
		{"static field decorator", `class Value { @decorate(require("static-decorator")) static field; }`, true},
		{"static computed field key", `class Value { static [require("static-key")]; }`, true},
		{"static field initializer", `class Value { static field = require("static-value"); }`, true},
		{"static block", `class Value { static { require("static-block"); } }`, true},
		{"static block function", `class Value { static { function later() { require("function-body"); } } }`, false},
		{"static block arrow", `class Value { static { const later = () => require("arrow-body"); } }`, false},
		{"method named static", `class Value { static() { require("body"); } }`, false},
		{"method named class", `const value = { class() { require("body"); } };`, false},
		{"method named if", `const value = { if() { require("body"); } };`, false},
		{"instance field named static", `class Value { static = require("instance-static"); }`, false},
		{"static field named static", `class Value { static static = require("static-static"); }`, true},
		{"static contextual name before ASI", "class Value {\n  static\n  field = require(\"instance\");\n}", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			analysis := analyzeJavaScriptSource(test.source, 1)
			if analysis.tree == nil || len(analysis.recoverySpans) != 0 {
				t.Fatalf("fixture did not parse cleanly: recovery=%#v", analysis.recoverySpans)
			}
			lexical := javascriptLexicalOnlyForTest(test.source)
			wantCount := 0
			if test.eager {
				wantCount = 1
			}
			if len(analysis.imports) != wantCount || len(lexical.imports) != wantCount {
				t.Fatalf("imports = concrete %#v, fallback %#v; eager=%v",
					analysis.imports, lexical.imports, test.eager)
			}
		})
	}

	malformed := "class Value {\n  static\n  field = require(\"instance\");\n  @\n}\n"
	analysis := analyzeJavaScriptSource(malformed, 6)
	if analysis.tree == nil || len(analysis.recoverySpans) == 0 {
		t.Fatal("malformed static-ASI fixture did not enter concrete recovery")
	}
	if len(analysis.imports) != 0 {
		t.Fatalf("malformed static-ASI fixture created an eager import: %#v", analysis.imports)
	}
}

func TestJavaScriptLexicalFallbackScopesQuotedObjectMethods(t *testing.T) {
	t.Parallel()

	const source = `const object = {
  "quoted-key"() {
    first();
  },
  'second key'(
    value
  ) {
    second(value);
  }
};
class Service {
  "third key"() {
    third();
  }
}
`
	lexed := javascriptLexicalOnlyForTest(source)
	want := []javascriptLineScope{
		{start: 2, end: 4},
		{start: 5, end: 9},
		{start: 11, end: 15},
		{start: 12, end: 14},
	}
	if !slices.Equal(lexed.scopes, want) {
		t.Fatalf("quoted method scopes = %#v, want %#v", lexed.scopes, want)
	}
}

func TestJavaScriptLexicalFallbackMatchesDecoratedDefinitionsAndScopes(t *testing.T) {
	t.Parallel()

	const source = `@sealed
export class Service {
  /** method docs */
  @memo method() {
    work();
  }
  @memo field = () => {
    work();
  };
}
`
	concrete := analyzeJavaScriptSource(source, strings.Count(source, "\n")+1)
	if concrete.tree == nil || len(concrete.recoverySpans) != 0 {
		t.Fatal("decorated fixture did not produce clean concrete syntax")
	}
	lexical := javascriptLexicalOnlyForTest(source)
	definitions := make([]sourceDefinition, 0, len(lexical.definitions))
	for _, candidate := range lexical.definitions {
		definitions = append(definitions, candidate.definition)
	}
	definitions = sortUniqueJavaScriptDefinitions(definitions)
	if !slices.Equal(definitions, concrete.definitions) {
		t.Fatalf("decorated fallback definitions = %#v, want %#v",
			definitions, concrete.definitions)
	}
	lexicalScopes := mergeJavaScriptScopes(
		strings.Count(source, "\n")+1, nil, lexical.scopes, definitions, true, nil,
	)
	if !slices.Equal(lexicalScopes, concrete.scopes) {
		t.Fatalf("decorated fallback scopes = %#v, want %#v", lexicalScopes, concrete.scopes)
	}
}

func TestJavaScriptLexicalFallbackScopesComputedAndReservedNameMethods(t *testing.T) {
	t.Parallel()

	const source = `const object = {
  [computed](value) {
    work(value);
  },
  static() {
    work();
  },
  class() {
    work();
  },
  if() {
    work();
  },
};
class Service {
  [computed](value) {
    work(value);
  }
  static() {
    work();
  }
}
`
	lexical := javascriptLexicalOnlyForTest(source)
	for _, want := range []javascriptLineScope{
		{start: 2, end: 4},
		{start: 5, end: 7},
		{start: 8, end: 10},
		{start: 11, end: 13},
		{start: 16, end: 18},
		{start: 19, end: 21},
	} {
		if !slices.Contains(lexical.scopes, want) {
			t.Fatalf("fallback method scopes = %#v, missing %#v", lexical.scopes, want)
		}
	}
	definitions := make([]sourceDefinition, 0, len(lexical.definitions))
	for _, candidate := range lexical.definitions {
		definitions = append(definitions, candidate.definition)
	}
	if got, want := javascriptDefinitionSymbols(sortUniqueJavaScriptDefinitions(definitions)),
		[]string{"object", "static", "class", "if", "Service", "static"}; !slices.Equal(got, want) {
		t.Fatalf("reserved-name method definitions = %#v, want %#v", got, want)
	}
}

func TestJavaScriptLexicalFallbackScopesOnlySwitchClauses(t *testing.T) {
	t.Parallel()

	const source = `const object = {
  case: first,
  default: second,
  method() {
    return target.default + target.case;
  },
};
switch (kind) {
case "first":
  first();
  break;
default:
  second();
}
const selected = object.default;
`
	lexed := javascriptLexicalOnlyForTest(source)
	want := []javascriptLineScope{
		{start: 4, end: 6},
		{start: 8, end: 14},
		{start: 9, end: 11},
		{start: 12, end: 13},
	}
	if !slices.Equal(lexed.scopes, want) {
		t.Fatalf("contextual default scopes = %#v, want %#v", lexed.scopes, want)
	}
}

func TestJavaScriptLexicalFallbackScopesOptionalCatchAndTryChain(t *testing.T) {
	t.Parallel()

	const source = `try
{
  work();
}
catch
{
  recover();
}
finally
{
  finish();
}
tail();
`
	lexed := javascriptLexicalOnlyForTest(source)
	want := []javascriptLineScope{
		{start: 1, end: 12},
		{start: 5, end: 8},
		{start: 9, end: 12},
	}
	if !slices.Equal(lexed.scopes, want) {
		t.Fatalf("optional catch scopes = %#v, want %#v", lexed.scopes, want)
	}
}

func TestJavaScriptLexicalFallbackScopesLabeledStatements(t *testing.T) {
	t.Parallel()

	const source = `empty: ;
single:
  work();
outer: inner:
  if (ready) work();
block: {
  work();
}
`
	concrete := analyzeJavaScriptSource(source, 8)
	if concrete.tree == nil || len(concrete.recoverySpans) != 0 {
		t.Fatal("labeled-statement fixture did not parse cleanly")
	}
	lexical := javascriptLexicalOnlyForTest(source)
	definitions := make([]sourceDefinition, 0, len(lexical.definitions))
	for _, candidate := range lexical.definitions {
		definitions = append(definitions, candidate.definition)
	}
	lexicalScopes := mergeJavaScriptScopes(8, nil, lexical.scopes, definitions, true, nil)
	if !slices.Equal(lexicalScopes, concrete.scopes) {
		t.Fatalf("labeled scopes = %#v, want %#v", lexicalScopes, concrete.scopes)
	}
}

func TestJavaScriptLexicalFallbackRespectsContextualPrefixLineBreaks(t *testing.T) {
	t.Parallel()

	const source = `class Service {
  static
  first() {}
  get
  second() {}
  async
  third() {}
  callback = () => {}
  next() {}
  nested = (() => {})()
}
async
function standalone() {}
export default
function exported() {}
`
	lineCount := strings.Count(source, "\n") + 1
	concrete := analyzeJavaScriptSource(source, lineCount)
	if concrete.tree == nil || len(concrete.recoverySpans) != 0 {
		t.Fatalf("contextual-prefix fixture did not parse cleanly: %#v", concrete.recoverySpans)
	}
	lexical := javascriptLexicalOnlyForTest(source)
	definitions := make([]sourceDefinition, 0, len(lexical.definitions))
	for _, candidate := range lexical.definitions {
		definitions = append(definitions, candidate.definition)
	}
	definitions = sortUniqueJavaScriptDefinitions(definitions)
	if !slices.Equal(definitions, concrete.definitions) {
		t.Fatalf("contextual-prefix definitions = %#v, want %#v", definitions, concrete.definitions)
	}
	lexicalScopes := mergeJavaScriptScopes(
		lineCount, nil, lexical.scopes, definitions, true, nil,
	)
	if !slices.Equal(lexicalScopes, concrete.scopes) {
		t.Fatalf("contextual-prefix scopes = %#v, want %#v", lexicalScopes, concrete.scopes)
	}
}

func TestJavaScriptLexicalFallbackAttachesObjectPropertyJSDoc(t *testing.T) {
	t.Parallel()

	const source = `const object = {
  /** handler docs */
  handler: () => {
    work();
  },
};
`
	concrete := analyzeJavaScriptSource(source, 6)
	lexical := javascriptLexicalOnlyForTest(source)
	definitions := make([]sourceDefinition, 0, len(lexical.definitions))
	for _, candidate := range lexical.definitions {
		definitions = append(definitions, candidate.definition)
	}
	if got, want := javascriptDefinitionNamed(t, definitions, "handler"),
		javascriptDefinitionNamed(t, concrete.definitions, "handler"); got != want {
		t.Fatalf("object-property JSDoc definition = %#v, want %#v", got, want)
	}
}

func TestJavaScriptClassContextRejectsPropertyAccess(t *testing.T) {
	t.Parallel()

	const source = `function wrap(){return c.class=i,c}function Br(){return c.name=i,c}
({class: value}); classObj.class = value; classObj?.class;
const plain = classObj.class;
class Derived extends classObj.class {
  method() {}
}
`
	lexed := javascriptLexicalOnlyForTest(source)
	definitions := make([]sourceDefinition, 0, len(lexed.definitions))
	for _, candidate := range lexed.definitions {
		definitions = append(definitions, candidate.definition)
	}
	if got, want := javascriptDefinitionSymbols(sortUniqueJavaScriptDefinitions(definitions)),
		[]string{"wrap", "Br", "plain", "Derived", "method"}; !slices.Equal(got, want) {
		t.Fatalf("property class definitions = %#v, want %#v", got, want)
	}
	if plain := javascriptDefinitionNamed(t, definitions, "plain"); plain.ownsScope {
		t.Fatalf("property class initializer owns scope: %#v", plain)
	}
}

func TestJavaScriptLexicalFallbackHandlesClassMembersAndCallableBoundaries(t *testing.T) {
	t.Parallel()

	const source = `class Child extends factory({ nested: true }) {
  method() {}
  plain
  callback = () => {}
  tail
}

exports.plus = function () {} + other;
const object = { plus: function () {} + other };
(exports).first = function () {};
(module.exports).second = class {};
exports.third = function () {}
exports.fourth = function () {}
`
	lexed := javascriptLexicalOnlyForTest(source)
	definitions := make([]sourceDefinition, 0, len(lexed.definitions))
	for _, candidate := range lexed.definitions {
		definitions = append(definitions, candidate.definition)
	}
	if got, want := javascriptDefinitionSymbols(sortUniqueJavaScriptDefinitions(definitions)),
		[]string{"Child", "method", "plain", "callback", "tail", "object", "first", "second", "third", "fourth"}; !slices.Equal(got, want) {
		t.Fatalf("lexical definitions = %#v, want %#v", got, want)
	}
	third := javascriptDefinitionNamed(t, definitions, "third")
	if third.scopeEnd != 12 {
		t.Fatalf("third scope = %#v, want line 12 only", third)
	}
}

func TestJavaScriptLexicalFallbackRetainsSloppyScriptBindings(t *testing.T) {
	t.Parallel()

	const source = `var await = 1, yield = 2, let = 3, static = 4, implements = 5;
function await() {}
`
	lexed := javascriptLexicalOnlyForTest(source)
	definitions := make([]sourceDefinition, 0, len(lexed.definitions))
	for _, candidate := range lexed.definitions {
		definitions = append(definitions, candidate.definition)
	}
	if got, want := javascriptDefinitionSymbols(sortUniqueJavaScriptDefinitions(definitions)),
		[]string{"await", "yield", "let", "static", "implements", "await"}; !slices.Equal(got, want) {
		t.Fatalf("sloppy bindings = %#v, want %#v", got, want)
	}
}

func TestJavaScriptLexicalFallbackFindsNestedDestructuringBindingsInSourceOrder(t *testing.T) {
	t.Parallel()

	const source = `const {
  plain,
  key: renamed,
  nested: {leaf, assigned = fallback},
  array: [head, , ...tail],
  shorthand = fallback,
  ...rest
} = source;
`
	lexed := javascriptLexicalOnlyForTest(source)
	definitions := make([]sourceDefinition, 0, len(lexed.definitions))
	for _, candidate := range lexed.definitions {
		definitions = append(definitions, candidate.definition)
	}
	if got, want := javascriptDefinitionSymbols(definitions),
		[]string{"plain", "renamed", "leaf", "assigned", "head", "tail", "shorthand", "rest"}; !slices.Equal(got, want) {
		t.Fatalf("destructuring definitions = %#v, want %#v", got, want)
	}
}

func TestJavaScriptLexicalBindingTraversalIsStackSafe(t *testing.T) {
	t.Parallel()

	const depth = 20_000
	var source strings.Builder
	source.Grow(depth*7 + 4)
	for range depth {
		source.WriteString("[name,")
	}
	source.WriteString("leaf")
	for range depth {
		source.WriteByte(']')
	}
	tokens := tokenizeJavaScript(source.String(), nil, nil, depth*4+1)
	names := javascriptLexBindingNames(
		tokens, 0, len(tokens)-1, javascriptMatchDelimiters(tokens),
	)
	if len(names) != depth+1 {
		t.Fatalf("deep binding count = %d, want %d", len(names), depth+1)
	}
	for index := 1; index < len(names); index++ {
		if names[index] <= names[index-1] {
			t.Fatalf("deep binding indexes are not ordered at %d: %d <= %d",
				index, names[index], names[index-1])
		}
	}
}

func TestJavaScriptCompactLexicalTokensAndDelimiterPairsPreserveCoordinates(t *testing.T) {
	t.Parallel()

	const source = "\u03b1(\"value\", [item]);\nnext"
	comments, literals := javascriptFallbackMasks(source)
	tokens := tokenizeJavaScript(source, comments, literals, 0)
	if len(tokens) != 10 {
		t.Fatalf("tokens = %#v, want 10", tokens)
	}
	for _, token := range tokens {
		if token.startOffset() < 0 || token.endOffset() > len(source) {
			t.Fatalf("invalid compact token %#v", token)
		}
		raw := source[token.startOffset():token.endOffset()]
		if token.literal() {
			if raw != `"value"` || token.text != javascriptLexicalValueToken {
				t.Fatalf("literal token = %#v over %q", token, raw)
			}
		} else if raw != token.text {
			t.Fatalf("compact token = %#v over %q", token, raw)
		}
	}
	if !tokens[2].literal() || tokens[0].startsLine() != true ||
		!tokens[len(tokens)-1].startsLine() {
		t.Fatalf("token flags = %#v", tokens)
	}
	pairs := javascriptMatchDelimiters(tokens)
	if closingIndex, ok := pairs.get(1); !ok || closingIndex != 7 || pairs.at(closingIndex) != 1 ||
		pairs.at(-1) != -1 || pairs.at(len(tokens)) != -1 {
		t.Fatalf("delimiter pairs = %#v", pairs)
	}
	crossed := javascriptMatchDelimiters([]javascriptToken{
		newJavaScriptToken("(", 0, true, false),
		newJavaScriptToken("[", 1, false, false),
		newJavaScriptToken(")", 2, false, false),
		newJavaScriptToken("]", 3, false, false),
	})
	if crossed.at(0) != 2 || crossed.at(1) != -1 || crossed.at(3) != -1 {
		t.Fatalf("crossed delimiter pairs = %#v", crossed)
	}
}

func TestJavaScriptLiteralPayloadsCannotBecomeRecoverySyntax(t *testing.T) {
	t.Parallel()

	const source = `"const"; "function"; "if"; "target"; "("; ")";
const real = 1;
`
	lexed := javascriptLexicalOnlyForTest(source)
	definitions := make([]sourceDefinition, 0, len(lexed.definitions))
	for _, candidate := range lexed.definitions {
		definitions = append(definitions, candidate.definition)
	}
	if got, want := javascriptDefinitionSymbols(definitions), []string{"real"}; !slices.Equal(got, want) {
		t.Fatalf("literal payload definitions = %#v, want %#v", got, want)
	}
	if len(lexed.scopes) != 0 || lexed.delimiters.at(0) != -1 {
		t.Fatalf("literal payload syntax leaked: scopes=%#v delimiters=%#v",
			lexed.scopes, lexed.delimiters)
	}
}

func TestJavaScriptLexicalFallbackSeparatesJSXTextFromExpressions(t *testing.T) {
	t.Parallel()

	const source = `const view = <Panel title="target" value={handler}>
  hidden function Fake() {} require("fake")
  {target}
  {render("target", /target/, ` + "`raw target ${target}`" + `)}
  {require("jsx-dependency")}
</Panel>;
export function after() {}
const loaded = require("later");
`
	fallback := scanJavaScriptFallback(source)
	masked := maskJavaScriptSource(source, fallback.literals)
	for _, hidden := range []string{"hidden", "Fake", `require("fake")`, `title="target"`, "raw target"} {
		if strings.Contains(masked, hidden) {
			t.Fatalf("fallback JSX mask retained %q:\n%s", hidden, masked)
		}
	}
	for _, visible := range []string{"<Panel", "render", "target", "require", "after", "loaded"} {
		if !strings.Contains(masked, visible) {
			t.Fatalf("fallback JSX mask removed %q:\n%s", visible, masked)
		}
	}

	lexed := javascriptLexicalOnlyForTest(source)
	definitions := make([]sourceDefinition, 0, len(lexed.definitions))
	for _, candidate := range lexed.definitions {
		definitions = append(definitions, candidate.definition)
	}
	symbols := javascriptDefinitionSymbols(sortUniqueJavaScriptDefinitions(definitions))
	if slices.Contains(symbols, "Fake") || slices.Contains(symbols, "handler") ||
		slices.Contains(symbols, "target") || !slices.Contains(symbols, "view") ||
		!slices.Contains(symbols, "after") || !slices.Contains(symbols, "loaded") {
		t.Fatalf("fallback JSX definitions = %#v", symbols)
	}
	if want := []javascriptLineSpan{{start: 1, end: 6}, {start: 8, end: 8}}; !slices.Equal(lexed.imports, want) {
		t.Fatalf("fallback JSX imports = %#v, want %#v", lexed.imports, want)
	}
}

func TestJavaScriptNestedJSXAttributeExpressionsRemainSemantic(t *testing.T) {
	t.Parallel()

	for _, source := range []string{
		`const view = <Host slot=<Child>{require("dep")}</Child> />;`,
		`const view = <Host slot=<Child outside={require("dep")} /> />;`,
	} {
		analysis := analyzeJavaScriptSource(source, 1)
		if analysis.tree == nil || len(analysis.recoverySpans) != 0 {
			t.Fatalf("fixture did not parse cleanly: %q", source)
		}
		want := []javascriptLineSpan{{start: 1, end: 1}}
		if !slices.Equal(analysis.imports, want) {
			t.Fatalf("concrete nested-JSX imports for %q = %#v, want %#v",
				source, analysis.imports, want)
		}
		if lexical := javascriptLexicalOnlyForTest(source); !slices.Equal(lexical.imports, want) {
			t.Fatalf("fallback nested-JSX imports for %q = %#v, want %#v",
				source, lexical.imports, want)
		}
		masked := maskJavaScriptSource(source, analysis.stringSpans)
		if strings.Contains(masked, "require") || strings.Contains(masked, "Child") {
			t.Fatalf("public nested-JSX attribute was not opaque: %q", masked)
		}
	}

	const deferred = `const view = <Host slot=<Child outside={() => require("hidden")} /> />;`
	concrete := analyzeJavaScriptSource(deferred, 1)
	lexical := javascriptLexicalOnlyForTest(deferred)
	if len(concrete.imports) != 0 || len(lexical.imports) != 0 {
		t.Fatalf("deferred nested-JSX import leaked: concrete=%#v fallback=%#v",
			concrete.imports, lexical.imports)
	}
	concreteView := javascriptDefinitionNamed(t, concrete.definitions, "view")
	lexicalDefinitions := make([]sourceDefinition, 0, len(lexical.definitions))
	for _, candidate := range lexical.definitions {
		lexicalDefinitions = append(lexicalDefinitions, candidate.definition)
	}
	lexicalView := javascriptDefinitionNamed(t, lexicalDefinitions, "view")
	if !concreteView.ownsScope || !lexicalView.ownsScope {
		t.Fatalf("nested-JSX callable scope = concrete %#v, fallback %#v",
			concreteView, lexicalView)
	}
}

func TestJavaScriptFallbackMalformedJSXRetriesStayBounded(t *testing.T) {
	t.Parallel()

	source := strings.Repeat("<A>", 20_000) + "\nfunction after() {}\n"
	lexed := javascriptLexicalOnlyForTest(source)
	for _, candidate := range lexed.definitions {
		if candidate.definition.symbol == "after" {
			return
		}
	}
	t.Fatal("bounded malformed JSX recovery lost the trailing definition")
}

func TestJavaScriptFallbackRejectsBareCommonJSEqualsWithoutPanicking(t *testing.T) {
	t.Parallel()

	for _, source := range []string{"=", "==", "(=)", "export ="} {
		analysis := analyzeJavaScriptSource(source, 1)
		if len(analysis.definitions) != 0 {
			t.Fatalf("malformed CommonJS source %q definitions = %#v, want none",
				source, analysis.definitions)
		}
	}
}

func TestJavaScriptFallbackRejectsImpossibleContextualRegexCandidates(t *testing.T) {
	t.Parallel()

	for _, word := range []string{"of", "let", "await", "yield", "using"} {
		for _, tail := range []string{"2", "denominator", "/* same line */ 2", "(2)", "[2]"} {
			source := word + ` / require("dependency") / ` + tail + `;`
			if got, want := javascriptLexicalOnlyForTest(source).imports,
				[]javascriptLineSpan{{start: 1, end: 1}}; !slices.Equal(got, want) {
				t.Fatalf("contextual division imports for %q = %#v, want %#v", source, got, want)
			}
		}
	}

	for _, source := range []string{
		`for (const value of /require("fake")/) {}`,
		`for (const [value = left in right] of /require("fake")/) {}`,
		`for (const {[left in right]: value} of /require("fake")/) {}`,
		`async function run() { await /require("fake")/; }`,
		`function* run() { yield /require("fake")/; }`,
		`let value = /require("fake")/;`,
		`using value = /require("fake")/;`,
	} {
		if imports := javascriptLexicalOnlyForTest(source).imports; len(imports) != 0 {
			t.Fatalf("genuine regex for %q created imports: %#v", source, imports)
		}
	}

	for _, word := range []string{"of", "let", "await", "yield", "using"} {
		jsx := "<A>{" + word + ` / require("dependency") / (2)}</A>`
		if got, want := javascriptLexicalOnlyForTest(jsx).imports,
			[]javascriptLineSpan{{start: 1, end: 1}}; !slices.Equal(got, want) {
			t.Fatalf("JSX contextual division imports for %q = %#v, want %#v", word, got, want)
		}
	}
}

func TestJavaScriptFallbackKeepsMemberValueExpressionsOutOfMethodContext(t *testing.T) {
	t.Parallel()

	for _, source := range []string{
		`const object = { value: async function() {} / require("dependency") / (2) };`,
		`const object = { value: (left, function() {} / require("dependency") / (2)) };`,
		`const object = { value: choose(left, class {} / require("dependency") / (2)) };`,
		`const object = { value: (left * function() {} / require("dependency") / (2)) };`,
		`class Value { field = async function() {} / require("dependency") / (2); }`,
	} {
		concrete := analyzeJavaScriptSource(source, 1)
		if concrete.tree == nil || len(concrete.recoverySpans) != 0 {
			t.Fatalf("member-value fixture did not parse cleanly: %q", source)
		}
		if lexical := javascriptLexicalOnlyForTest(source); !slices.Equal(
			lexical.imports, concrete.imports,
		) {
			t.Fatalf("member-value imports for %q = %#v, want %#v",
				source, lexical.imports, concrete.imports)
		}
		_, literals := javascriptFallbackMasks(source)
		if masked := maskJavaScriptSource(source, literals); !strings.Contains(masked, "require") {
			t.Fatalf("member-value division was masked as regex for %q", source)
		}
	}
}

func TestJavaScriptFallbackTracksAwaitAndYieldFunctionContexts(t *testing.T) {
	t.Parallel()

	for _, source := range []string{
		`(await / require("dependency") / (2));`,
		`((yield / require("dependency") / (2)));`,
		`const object = { value: (yield / require("dependency") / (2)) };`,
		`for (const value of await / require("dependency") / (2)) {}`,
		`for (const value of yield / require("dependency") / (2)) {}`,
		`for (const value of of / require("dependency") / (2)) {}`,
		`<A>{(yield / require("dependency") / (2))}</A>`,
	} {
		want := []javascriptLineSpan{{start: 1, end: 1}}
		if got := javascriptLexicalOnlyForTest(source).imports; !slices.Equal(got, want) {
			t.Fatalf("sloppy contextual imports for %q = %#v, want %#v", source, got, want)
		}
	}

	for _, source := range []string{
		`function ordinary() { (await / require("dependency") / (2)); }`,
		`function ordinary() { const value = { nested: (yield / require("dependency") / (2)) }; }`,
	} {
		_, literals := javascriptFallbackMasks(source)
		if masked := maskJavaScriptSource(source, literals); !strings.Contains(masked, "require") {
			t.Fatalf("ordinary-function contextual division was masked as regex for %q", source)
		}
	}

	for _, source := range []string{
		`async function run() { (await /require("fake")/); }`,
		`function* run() { (yield /require("fake")/); }`,
		`const object = { async run() { (await /require("fake")/); } };`,
		`const object = { *run() { (yield /require("fake")/); } };`,
		`const object = { async *run() { (await /require("fake")/); (yield /alsoFake()/); } };`,
		`const run = async () => { (await /require("fake")/); };`,
		`const run = async value => (await /require("fake")/);`,
		`async function render() { return <A>{(await /require("fake")/)}</A>; }`,
		`function* render() { return <A>{(yield /require("fake")/)}</A>; }`,
	} {
		if got := javascriptLexicalOnlyForTest(source).imports; len(got) != 0 {
			t.Fatalf("contextual operator regex for %q created imports: %#v", source, got)
		}
	}

	for _, source := range []string{
		`const object = { async [({ [inner]() {} }).key]() { await /require("fake")/; } };`,
		`const object = { *[({ [inner]() {} }).key]() { yield /require("fake")/; } };`,
		`class Value { async [({ [inner]() {} }).key]() { await /require("fake")/; } }`,
		`const view = <A>{({ async [({ [inner]() {} }).key]() { await /require("fake")/; } })}</A>;`,
	} {
		concrete := analyzeJavaScriptSource(source, 1)
		if concrete.tree == nil || len(concrete.recoverySpans) != 0 {
			t.Fatalf("nested-computed fixture did not parse cleanly: %q", source)
		}
		_, fallbackLiterals := javascriptFallbackMasks(source)
		if !slices.Equal(fallbackLiterals, concrete.stringSpans) {
			t.Fatalf("nested-computed literals for %q = %#v, want %#v",
				source, fallbackLiterals, concrete.stringSpans)
		}
	}
}

func TestJavaScriptFallbackDoesNotTreatLabelsAsBindingDeclarations(t *testing.T) {
	t.Parallel()

	const source = "let:\nvalue\n/ require(\"dependency\") / 2;"
	concrete := analyzeJavaScriptSource(source, 3)
	lexical := javascriptLexicalOnlyForTest(source)
	if !slices.Equal(lexical.imports, concrete.imports) ||
		!slices.Equal(lexical.scopes, concrete.scopes) {
		t.Fatalf("contextual label fallback = imports %#v scopes %#v, want %#v %#v",
			lexical.imports, lexical.scopes, concrete.imports, concrete.scopes)
	}
}

func TestJavaScriptFallbackDoesNotLeakRestrictedASIFromMemberNames(t *testing.T) {
	t.Parallel()

	for _, source := range []string{
		"object.break()\n/ require(\"dependency\") / 2;",
		"object?.continue()\n/ require(\"dependency\") / 2;",
		"const object = { debugger() {} }\n/ require(\"dependency\") / 2;",
		"const Value = class { break() {} }\n/ require(\"dependency\") / 2;",
	} {
		concrete := analyzeJavaScriptSource(source, 2)
		lexical := javascriptLexicalOnlyForTest(source)
		if !slices.Equal(lexical.imports, concrete.imports) {
			t.Fatalf("restricted member imports for %q = %#v, want %#v",
				source, lexical.imports, concrete.imports)
		}
	}
}

func TestJavaScriptFallbackDoesNotCreateControlScopesForMethods(t *testing.T) {
	t.Parallel()

	for _, source := range []string{
		"class C {\n  if() {}\n  else() {}\n}",
		"class C {\n  try() {}\n  catch() {}\n  finally() {}\n}",
		"class C {\n  do() {}\n  while() {}\n}",
		"const object = { if() {}, else() {}, catch() {} };",
	} {
		concrete := analyzeJavaScriptSource(source, strings.Count(source, "\n")+1)
		lexical := javascriptLexicalOnlyForTest(source)
		if !slices.Equal(lexical.scopes, concrete.scopes) {
			t.Fatalf("control-word method scopes for %q = %#v, want %#v",
				source, lexical.scopes, concrete.scopes)
		}
	}
}

func TestJavaScriptFallbackRecognizesClassNamedObjectMethodScope(t *testing.T) {
	t.Parallel()

	const source = `const object = {
  class() { require("deferred"); }
};`
	concrete := analyzeJavaScriptSource(source, 3)
	lexical := javascriptLexicalOnlyForTest(source)
	lexicalDefinitions := make([]sourceDefinition, 0, len(lexical.definitions))
	for _, candidate := range lexical.definitions {
		lexicalDefinitions = append(lexicalDefinitions, candidate.definition)
	}
	lexicalDefinitions = sortUniqueJavaScriptDefinitions(lexicalDefinitions)
	if !reflect.DeepEqual(lexicalDefinitions, concrete.definitions) {
		t.Fatalf("class-named method definitions = %#v, want %#v",
			lexicalDefinitions, concrete.definitions)
	}
}

func TestJavaScriptFallbackTreatsControlWordPropertiesAsValues(t *testing.T) {
	t.Parallel()

	for _, property := range []string{"if", "for", "while", "with", "switch", "catch"} {
		for _, access := range []string{".", "?."} {
			source := "object" + access + property + `(value) / require("dependency") / 2;`
			if got, want := javascriptLexicalOnlyForTest(source).imports,
				[]javascriptLineSpan{{start: 1, end: 1}}; !slices.Equal(got, want) {
				t.Fatalf("control-property imports for %q = %#v, want %#v", source, got, want)
			}
		}
	}

	const multiline = `const value =
  object.if(
    require("dependency")
  );`
	concrete := analyzeJavaScriptSource(multiline, 4)
	lexical := javascriptLexicalOnlyForTest(multiline)
	if !slices.Equal(lexical.imports, concrete.imports) ||
		!slices.Equal(lexical.scopes, concrete.scopes) {
		t.Fatalf("control-property analysis = imports %#v scopes %#v, want %#v %#v",
			lexical.imports, lexical.scopes, concrete.imports, concrete.scopes)
	}
}

func TestJavaScriptFallbackClassifiesOperatorPrefixedCallablesAsValues(t *testing.T) {
	t.Parallel()

	for _, source := range []string{
		`!function() {} / require("dependency") / 2;`,
		`value + function() {} / require("dependency") / 2;`,
		`value && class {} / require("dependency") / 2;`,
		`typeof function() {} / require("dependency") / 2;`,
		`void class {} / require("dependency") / 2;`,
	} {
		analysis := analyzeJavaScriptSource(source, 1)
		if analysis.tree == nil || len(analysis.recoverySpans) != 0 {
			t.Fatalf("operator-callable fixture did not parse cleanly: %q", source)
		}
		if got, want := javascriptLexicalOnlyForTest(source).imports,
			[]javascriptLineSpan{{start: 1, end: 1}}; !slices.Equal(got, want) {
			t.Fatalf("operator-callable imports for %q = %#v, want %#v", source, got, want)
		}
	}
}

func TestJavaScriptFallbackExpiresIncompleteAndMemberClassState(t *testing.T) {
	t.Parallel()

	tests := []struct {
		source string
		want   []javascriptLineSpan
	}{
		{
			source: "class Missing\nconst fn = () => {} / require(\"dependency\");",
			want:   []javascriptLineSpan{{start: 2, end: 2}},
		},
		{
			source: "export { class as C } from \"module\";\n" +
				"const object = { fn: () => {} / require(\"dependency\") };",
			want: []javascriptLineSpan{{start: 1, end: 1}, {start: 2, end: 2}},
		},
		{
			source: "class C {\n  class\n  static field = () => {} / require(\"dependency\");\n}",
			want:   []javascriptLineSpan{{start: 1, end: 4}},
		},
		{
			source: "class C {\n  #class\n  static field = () => {} / require(\"dependency\");\n}",
			want:   []javascriptLineSpan{{start: 1, end: 4}},
		},
	}
	for _, test := range tests {
		if got := javascriptLexicalOnlyForTest(test.source).imports; !slices.Equal(got, test.want) {
			t.Fatalf("stale-class imports for %q = %#v, want %#v", test.source, got, test.want)
		}
	}
}

func TestJavaScriptFallbackMarksGrammarForcedStatementStarts(t *testing.T) {
	t.Parallel()

	for _, source := range []string{
		"var value\n[require(\"dependency\")];",
		"var value\n`template ${require(\"dependency\")}`;",
	} {
		if got, want := javascriptLexicalOnlyForTest(source).imports,
			[]javascriptLineSpan{{start: 2, end: 2}}; !slices.Equal(got, want) {
			t.Fatalf("forced-statement imports for %q = %#v, want %#v", source, got, want)
		}
	}

	const jsx = "let value\n<A>secret {require(\"dependency\")}</A>;"
	fallback := scanJavaScriptFallback(jsx)
	if len(fallback.jsxValues) != 1 ||
		strings.Contains(maskJavaScriptSource(jsx, fallback.literals), "secret") {
		t.Fatalf("binding-ASI JSX was not recognized: values=%#v mask=%q",
			fallback.jsxValues, maskJavaScriptSource(jsx, fallback.literals))
	}
	if got, want := javascriptLexicalOnlyForTest(jsx).imports,
		[]javascriptLineSpan{{start: 2, end: 2}}; !slices.Equal(got, want) {
		t.Fatalf("binding-ASI JSX imports = %#v, want %#v", got, want)
	}

	const restricted = "while (ready) { break\n<A>secret {require(\"dependency\")}</A>; }"
	restrictedFallback := scanJavaScriptFallback(restricted)
	if len(restrictedFallback.jsxValues) != 1 || strings.Contains(
		maskJavaScriptSource(restricted, restrictedFallback.literals), "secret",
	) {
		t.Fatalf("restricted-ASI JSX was not recognized: values=%#v mask=%q",
			restrictedFallback.jsxValues,
			maskJavaScriptSource(restricted, restrictedFallback.literals))
	}
}

func TestJavaScriptFallbackStartsAfterNonValueBraces(t *testing.T) {
	t.Parallel()

	for _, source := range []string{
		"function declared() {}\n(require(\"dependency\"));",
		"class Declared {}\n[require(\"dependency\")];",
		"{ work(); }\n+require(\"dependency\");",
		"if (ready) {}\n(require(\"dependency\"));",
	} {
		if got, want := javascriptLexicalOnlyForTest(source).imports,
			[]javascriptLineSpan{{start: 2, end: 2}}; !slices.Equal(got, want) {
			t.Fatalf("post-brace imports for %q = %#v, want %#v", source, got, want)
		}
	}
}

func TestJavaScriptFallbackKeepsLineStartCallablesInsideExpressions(t *testing.T) {
	t.Parallel()

	const eager = `const value = condition ?
  class {} / require("dependency") : fallback;`
	if got, want := javascriptLexicalOnlyForTest(eager).imports,
		[]javascriptLineSpan{{start: 1, end: 2}}; !slices.Equal(got, want) {
		t.Fatalf("multiline callable-expression imports = %#v, want %#v", got, want)
	}

	for _, source := range []string{
		"const fn = () =>\n  class extends require(\"runtime\") {};",
		"const fn = () => left +\n  class extends require(\"runtime\") {};",
	} {
		if imports := javascriptLexicalOnlyForTest(source).imports; len(imports) != 0 {
			t.Fatalf("concise-arrow class heritage escaped deferral for %q: %#v", source, imports)
		}
	}
}

func TestJavaScriptFallbackKeepsKeywordFieldsSeparateFromFollowingMethods(t *testing.T) {
	t.Parallel()

	for _, keyword := range []string{"function", "class"} {
		source := "class C {\n  " + keyword + "\n  method() {}\n}"
		concrete := analyzeJavaScriptSource(source, 4)
		lexical := javascriptLexicalOnlyForTest(source)
		lexicalDefinitions := make([]sourceDefinition, 0, len(lexical.definitions))
		for _, candidate := range lexical.definitions {
			lexicalDefinitions = append(lexicalDefinitions, candidate.definition)
		}
		lexicalDefinitions = sortUniqueJavaScriptDefinitions(lexicalDefinitions)
		if !reflect.DeepEqual(lexicalDefinitions, concrete.definitions) {
			t.Fatalf("keyword-field definitions for %q = %#v, want %#v",
				keyword, lexicalDefinitions, concrete.definitions)
		}
	}
}

func TestJavaScriptFallbackSeparatesSemicolonlessClassFields(t *testing.T) {
	t.Parallel()

	for _, source := range []string{
		"class C {\n  static first = one\n  static second = two\n  static third = three\n}",
		"class C {\n  first = one\n  static second = two\n}",
		"class C {\n  static first\n  static second\n}",
	} {
		concrete := analyzeJavaScriptSource(source, strings.Count(source, "\n")+1)
		lexical := javascriptLexicalOnlyForTest(source)
		lexicalDefinitions := make([]sourceDefinition, 0, len(lexical.definitions))
		for _, candidate := range lexical.definitions {
			lexicalDefinitions = append(lexicalDefinitions, candidate.definition)
		}
		lexicalDefinitions = sortUniqueJavaScriptDefinitions(lexicalDefinitions)
		if !reflect.DeepEqual(lexicalDefinitions, concrete.definitions) {
			t.Fatalf("semicolonless class definitions for %q = %#v, want %#v",
				source, lexicalDefinitions, concrete.definitions)
		}
	}
}

func TestJavaScriptComputedClassFieldsDoNotNameTheirInitializers(t *testing.T) {
	t.Parallel()

	const source = "class C {\n  static [first] = initializer\n  [second] = other\n}"
	analysis := analyzeJavaScriptSource(source, 4)
	if got, want := javascriptDefinitionSymbols(analysis.definitions),
		[]string{"C"}; !slices.Equal(got, want) {
		t.Fatalf("computed-field definitions = %#v, want %#v", got, want)
	}
	if got := javascriptDefinitionSymbols(
		newJavaScriptLanguage("javascript").sourceDefinitions(javascriptTestLines(source)),
	); !slices.Equal(got, []string{"C"}) {
		t.Fatalf("public computed-field definitions = %#v, want C", got)
	}
}

func TestJavaScriptFallbackDoesNotDuplicateUnbracedDoWhileScope(t *testing.T) {
	t.Parallel()

	const source = "do\n  require(\"dependency\");\nwhile (condition);"
	concrete := analyzeJavaScriptSource(source, 3)
	lexical := javascriptLexicalOnlyForTest(source)
	if !slices.Equal(lexical.scopes, concrete.scopes) ||
		!slices.Equal(lexical.imports, concrete.imports) {
		t.Fatalf("do-while fallback = scopes %#v imports %#v, want %#v %#v",
			lexical.scopes, lexical.imports, concrete.scopes, concrete.imports)
	}
}

func javascriptLexicalOnlyForTest(source string) javascriptLexResult {
	fallback := scanJavaScriptFallback(source)
	return lexJavaScriptWithHints(
		source, fallback.comments, fallback.literals, true, nil, fallback,
	)
}

func FuzzJavaScriptBackendMaintainsCoordinateContracts(f *testing.F) {
	f.Add("")
	f.Add("function main() { return /value/.test(`raw ${value}`); }\n")
	f.Add("const view = <Panel title=\"value\">text {render(value)}</Panel>;\n")
	f.Add("function broken( {\nfunction after() {}\n")
	f.Add("\x00\xff\xfe\nconst after = 1;\n")

	f.Fuzz(func(t *testing.T, source string) {
		lines := strings.Split(source, "\n")
		backend := prepareLanguageBackend(newJavaScriptLanguage("javascript"), lines)
		for _, definition := range backend.sourceDefinitions(lines) {
			if definition.line < 1 || definition.line > len(lines) || definition.column < 1 ||
				definition.scopeStart < 1 || definition.scopeStart > definition.line ||
				definition.scopeEnd < definition.line || definition.scopeEnd > len(lines) {
				t.Fatalf("invalid definition coordinates: %#v", definition)
			}
		}
		for _, options := range [][2]bool{{false, false}, {true, false}, {false, true}, {true, true}} {
			searchable := backend.searchLines(lines, options[0], options[1])
			if len(searchable) != len(lines) || len(strings.Join(searchable, "\n")) != len(source) {
				t.Fatalf("search mask changed coordinates: %#v", searchable)
			}
		}
		_, _, _ = backend.importRange(lines)
		_, _ = backend.enclosingScope(lines, 1)
		_, _ = backend.enclosingScope(lines, len(lines))
		_ = backend.cleanSource(source, true, false)
	})
}

func javascriptDefinitionSymbols(definitions []sourceDefinition) []string {
	symbols := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		symbols = append(symbols, definition.symbol)
	}
	return symbols
}
