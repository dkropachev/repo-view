package repoview

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestPythonDefinitionsCoverModernConcreteSyntax(t *testing.T) {
	t.Parallel()
	const source = `@cache(
    enabled=True,
)
@pkg.decorate
async def café[
    T,
](
    参数: T,
) -> T:
    class Δelta:
        def 内部(self):
            def вложенная():
                return 参数
            return вложенная()
    match 参数:
        case {"value": captured}:
            return client.transport.request(captured)
        case _:
            return None

type Résultat[T] = tuple[T, T]
@registry.register
class OneLine: pass
`
	lines := pythonTestLines(source)
	definitions := newPythonLanguage().sourceDefinitions(lines)
	type summary struct {
		symbol string
		line   int
		start  int
		end    int
	}
	got := make([]summary, 0, len(definitions))
	for _, definition := range definitions {
		got = append(got, summary{
			symbol: definition.symbol,
			line:   definition.line,
			start:  definition.scopeStart,
			end:    definition.scopeEnd,
		})
	}
	want := []summary{
		{symbol: "café", line: 5, start: 1, end: 19},
		{symbol: "Δelta", line: 10, start: 10, end: 14},
		{symbol: "内部", line: 11, start: 11, end: 14},
		{symbol: "вложенная", line: 12, start: 12, end: 13},
		{symbol: "Résultat", line: 21, start: 21, end: 21},
		{symbol: "OneLine", line: 23, start: 22, end: 23},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("definitions = %#v, want %#v", got, want)
	}

	root := t.TempDir()
	writeFile(t, root, "fixture.py", source)
	outline, err := mustView(t, root).Outline("fixture.py", Options{Return: ReturnLocations})
	if err != nil {
		t.Fatal(err)
	}
	gotSymbols := make([]string, 0, len(outline.Results))
	for _, result := range outline.Results {
		gotSymbols = append(gotSymbols, result.Symbol)
		if result.Kind != "def" || result.Language != "python" {
			t.Fatalf("malformed outline result: %#v", result)
		}
	}
	if wantSymbols := []string{"café", "Δelta", "内部", "вложенная", "Résultat", "OneLine"}; !reflect.DeepEqual(gotSymbols, wantSymbols) {
		t.Fatalf("outline symbols = %#v, want %#v", gotSymbols, wantSymbols)
	}

	caseLine := lineContaining(t, lines, "client.transport.request")
	start, end := newPythonLanguage().enclosingScope(lines, caseLine)
	if start != 16 || end != 17 {
		t.Fatalf("case scope = %d-%d, want 16-17", start, end)
	}
}

func TestPythonDefinitionsIgnoreCommentsAndEveryStringForm(t *testing.T) {
	t.Parallel()
	const source = `# def Commented(): pass
short = "class Short: pass"
raw = r'def Raw(): pass'
triple = """
async def TripleFake(): pass
"""
raw_triple = r'''
class RawTripleFake: pass
'''
formatted = f"def FormattedFake(): {real_call()}"
raw_formatted = fr"class RawFormattedFake: {other_call()}"
template = t"def TemplateFake(): {template_call()}"
raw_template = tr"class RawTemplateFake: {raw_template_call()}"
def Real(): pass
`
	definitions := newPythonLanguage().sourceDefinitions(pythonTestLines(source))
	if len(definitions) != 1 || definitions[0].symbol != "Real" || definitions[0].line != 14 {
		t.Fatalf("definitions = %#v, want Real on line 14", definitions)
	}
}

func TestPythonDefinitionsRequirePythonXIDNames(t *testing.T) {
	t.Parallel()
	const source = "def if(): pass\n" +
		"class return: pass\n" +
		"def \u037a(): pass\n" +
		"def \u216b(): pass\n" +
		"def \uff49\uff46(): pass\n" +
		"def \u1c89(): pass\n"
	definitions := newPythonLanguage().sourceDefinitions(pythonTestLines(source))
	got := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		got = append(got, definition.symbol)
	}
	if want := []string{"Ⅻ", "ｉｆ", "Ᲊ"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("definitions = %#v, want %#v", got, want)
	}
}

func TestPythonGeneratedXIDTablesAreOrdered(t *testing.T) {
	t.Parallel()
	for name, ranges := range map[string][]pythonXIDRange{
		"start":    pythonXIDStartRanges,
		"continue": pythonXIDContinueRanges,
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
	for _, span := range pythonXIDStartRanges {
		if !pythonIdentifierContinue(span.first) || !pythonIdentifierContinue(span.last) {
			t.Fatalf("XID_Start range is not contained in XID_Continue: %#v", span)
		}
	}
}

func TestPythonSearchMaskingPreservesFAndTStringExpressions(t *testing.T) {
	t.Parallel()
	source := strings.Join([]string{
		`# target() literal_only`,
		`plain = "target() # literal_only"`,
		`raw = r"target\" # literal_only"`,
		`triple = r'''first target literal_only`,
		`# second target literal_only`,
		`third target literal_only'''`,
		`continued = "first target literal_only \`,
		`second target literal_only"`,
		`formatted = f"literal_only target {target():{width}}"`,
		`raw_formatted = fr"literal_only target {target():{width}}"`,
		`template = t"literal_only target {target():{width}}"`,
		`raw_template = tr"literal_only target {target():{width}}"`,
		`target()  # target() literal_only`,
		`mixed = "# target literal_only"; target()`,
	}, "\r\n")
	lines := strings.Split(source, "\n")
	searchable := newPythonLanguage().searchLines(lines, true, true)
	if len(searchable) != len(lines) {
		t.Fatalf("search line count = %d, want %d", len(searchable), len(lines))
	}

	targetLines := pythonLinesContainingSymbol(searchable, "target")
	if want := []int{9, 10, 11, 12, 13, 14}; !reflect.DeepEqual(targetLines, want) {
		t.Fatalf("target lines = %#v, want %#v; masked = %#v", targetLines, want, searchable)
	}
	if got, want := pythonLinesContainingSymbol(searchable, "width"), []int{9, 10, 11, 12}; !reflect.DeepEqual(got, want) {
		t.Fatalf("width lines = %#v, want %#v; masked = %#v", got, want, searchable)
	}
	if got := pythonLinesContainingSymbol(searchable, "literal_only"); len(got) != 0 {
		t.Fatalf("string literal text remained searchable on lines %#v: %#v", got, searchable)
	}

	root := t.TempDir()
	writeFile(t, root, "fixture.py", source)
	response, err := mustView(t, root).Find("target", Options{
		Include:    IncludeRefs,
		Return:     ReturnLocations,
		NoComments: true,
		NoStrings:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := resultLines(response.Results); !reflect.DeepEqual(got, targetLines) {
		t.Fatalf("RepoView target lines = %#v, want %#v; results = %#v", got, targetLines, response.Results)
	}
}

func TestPythonFormattedBackslashesDoNotEscapeReplacementFields(t *testing.T) {
	t.Parallel()
	lines := []string{
		`raw = rf"\{target()}"`,
		`formatted = f"\{other()}"`,
		`template = tr"\{third()}"`,
	}
	searchable := newPythonLanguage().searchLines(lines, true, true)
	for index, symbol := range []string{"target", "other", "third"} {
		if countSymbolOccurrences(searchable[index], symbol) != 1 {
			t.Fatalf("line %d lost %q replacement field: %q", index+1, symbol, searchable[index])
		}
	}
}

func TestPythonDocstringsAreSemanticStringStatements(t *testing.T) {
	t.Parallel()
	const source = `r"""module raw docs
continued module docs"""
assigned = """assigned literal"""
"""later module literal"""
class UnicodeDocs:
    u"class unicode docs"
    value = 1
class AdjacentDocs:
    "adjacent " "class docs"
    value = 1
def parenthesized_docs():
    ("parenthesized function docs")
    target()
def raw_docs():
    r"raw function docs"
    return 1
def later_literal():
    value = 1
    """later function literal"""
    return value
def bytes_literal():
    b"bytes literal"
    return 1
def formatted_literal(value):
    f"formatted literal {value}"
    return value
def template_literal(value):
    t"template literal {value}"
    return value
def same_line(): "inline docs"; target()
if ready:
    """control suite literal"""
`
	backend := newPythonLanguage()
	lines := pythonTestLines(source)
	ignored := backend.ignoredSearchLines(lines, false, true)
	gotIgnored := make([]int, 0)
	for lineNo := 1; lineNo <= len(lines); lineNo++ {
		if ignored[lineNo] {
			gotIgnored = append(gotIgnored, lineNo)
		}
	}
	if want := []int{1, 2, 6, 9, 12, 15}; !reflect.DeepEqual(gotIgnored, want) {
		t.Fatalf("ignored docstring lines = %#v, want %#v", gotIgnored, want)
	}

	cleaned := backend.cleanSource(source, false, true)
	for _, removed := range []string{
		"module raw docs", "continued module docs", "class unicode docs",
		"adjacent ", "parenthesized function docs", "raw function docs", "inline docs",
	} {
		if strings.Contains(cleaned, removed) {
			t.Fatalf("cleaned source retained docstring %q:\n%s", removed, cleaned)
		}
	}
	for _, preserved := range []string{
		"assigned literal", "later module literal", "later function literal",
		"bytes literal", "formatted literal", "template literal", "control suite literal",
	} {
		if !strings.Contains(cleaned, preserved) {
			t.Fatalf("cleaned source removed non-docstring %q:\n%s", preserved, cleaned)
		}
	}
	if strings.Count(cleaned, "target()") != 2 || !strings.Contains(cleaned, "def same_line():") {
		t.Fatalf("cleaning lost code adjacent to a docstring:\n%s", cleaned)
	}

	root := t.TempDir()
	writeFile(t, root, "fixture.py", source)
	response, err := mustView(t, root).Find("target", Options{
		Include:        IncludeRefs,
		Return:         ReturnLine,
		DropDocstrings: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := resultLines(response.Results), []int{13, 30}; !reflect.DeepEqual(got, want) {
		t.Fatalf("target lines = %#v, want %#v; results = %#v", got, want, response.Results)
	}
	for _, result := range response.Results {
		if strings.Contains(result.Code, "docs") || !strings.Contains(result.Code, "target()") {
			t.Fatalf("cleaned target result = %#v", result)
		}
	}
}

func TestPythonImportRangeCoversConcreteStatements(t *testing.T) {
	t.Parallel()
	const source = `text = "import fake"
# from fake import value
blob = '''import fake
from fake import value
'''
import os; import sys as system
from . import local
from ..pkg import (
    first,
    second as alias,  # comment
)
from ...other \
    import third
def load():
    import inside
if TYPE_CHECKING:
    from optional import Feature
try:
    import optional_backend
except ImportError:
    optional_backend = None
after = "from fake import value"
`
	lines := pythonTestLines(source)
	start, end, ok := newPythonLanguage().importRange(lines)
	if !ok || start != 6 || end != 19 {
		t.Fatalf("imports = %d-%d, %v; want 6-19, true", start, end, ok)
	}

	const fakeOnly = `# import comment_only
short = "from fake import name"
triple = '''
import triple_fake
'''
`
	if fakeStart, fakeEnd, fakeOK := newPythonLanguage().importRange(pythonTestLines(fakeOnly)); fakeOK {
		t.Fatalf("fake-only imports = %d-%d, true; want none", fakeStart, fakeEnd)
	}
}

func TestPythonImportRangeRecoversIncompleteStatements(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		source    string
		wantStart int
		wantEnd   int
	}{
		{
			name: "open parenthesized import",
			source: `from package import (
    first,
    second as alias,
`,
			wantStart: 1,
			wantEnd:   3,
		},
		{
			name: "resynchronizes after broken header",
			source: `import before
def broken(
from pending import (
    one,
    two as alias,
`,
			wantStart: 1,
			wantEnd:   5,
		},
		{
			name: "complete explicit continuation",
			source: `from package \
    import value
`,
			wantStart: 1,
			wantEnd:   2,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			start, end, ok := newPythonLanguage().importRange(pythonTestLines(test.source))
			if !ok || start != test.wantStart || end != test.wantEnd {
				t.Fatalf(
					"imports = %d-%d, %v; want %d-%d, true",
					start, end, ok, test.wantStart, test.wantEnd,
				)
			}
		})
	}
}

func TestPythonCommentBackslashDoesNotContinueNextStatement(t *testing.T) {
	t.Parallel()
	const source = `value = 1 # \
def after(): pass
import package
`
	lexed := lexPython(strings.Join(pythonTestLines(source), "\n"))
	if len(lexed.definitions) != 1 || lexed.definitions[0].symbol != "after" ||
		lexed.definitions[0].line != 2 {
		t.Fatalf("fallback definitions = %#v, want after on line 2", lexed.definitions)
	}
	if len(lexed.imports) != 1 || lexed.imports[0] != (pythonLineSpan{start: 3, end: 3}) {
		t.Fatalf("fallback imports = %#v, want line 3", lexed.imports)
	}
}

func TestPythonInlineImportStartsAfterMultilineHeader(t *testing.T) {
	t.Parallel()
	const source = `if (
    ready
): import package
`
	start, end, ok := newPythonLanguage().importRange(pythonTestLines(source))
	if !ok || start != 3 || end != 3 {
		t.Fatalf("imports = %d-%d, %v; want 3-3, true", start, end, ok)
	}
}

func TestPythonScopesUseSmallestSuiteAndNamedDefinition(t *testing.T) {
	t.Parallel()
	const existingContract = `import os
from pathlib import Path

def run():
    if True:
        print(Path.cwd())
`
	contractLines := pythonTestLines(existingContract)
	if start, end := newPythonLanguage().enclosingScope(contractLines, 6); start != 5 || end != 6 {
		t.Fatalf("existing nested-if scope = %d-%d, want 5-6", start, end)
	}

	const source = `@decorator
def outer():
    if ready:
        for item in items:
            try:
                with lock:
                    deep_target()
            except Error:
                recover_target()
            finally:
                cleanup_target()
        else:
            exhausted_target()
    after_target()

def sibling():
    sibling_target()

top_target()
`
	lines := pythonTestLines(source)
	tests := []struct {
		fragment string
		start    int
		end      int
	}{
		{fragment: "deep_target", start: 6, end: 7},
		{fragment: "recover_target", start: 8, end: 9},
		{fragment: "cleanup_target", start: 10, end: 11},
		{fragment: "exhausted_target", start: 12, end: 13},
		{fragment: "after_target", start: 1, end: 14},
		{fragment: "sibling_target", start: 16, end: 17},
		{fragment: "top_target", start: 19, end: 19},
	}
	backend := newPythonLanguage()
	for _, test := range tests {
		lineNo := lineContaining(t, lines, test.fragment)
		start, end := backend.enclosingScope(lines, lineNo)
		if start != test.start || end != test.end {
			t.Errorf("%s scope = %d-%d, want %d-%d", test.fragment, start, end, test.start, test.end)
		}
	}

	root := t.TempDir()
	writeFile(t, root, "fixture.py", source)
	view := mustView(t, root)
	for _, test := range []struct {
		line  int
		start int
		end   int
		scope string
	}{
		{line: 7, start: 1, end: 14, scope: "outer"},
		{line: 14, start: 1, end: 14, scope: "outer"},
		{line: 17, start: 16, end: 17, scope: "sibling"},
	} {
		response, err := view.Inspect(
			fmt.Sprintf("fixture.py:%d", test.line),
			Options{Include: IncludeScope, Return: ReturnScope},
		)
		if err != nil {
			t.Fatal(err)
		}
		if len(response.Results) != 1 {
			t.Fatalf("Inspect line %d results = %#v", test.line, response.Results)
		}
		result := response.Results[0]
		if result.StartLine != test.start || result.EndLine != test.end || result.Scope != test.scope {
			t.Fatalf("Inspect line %d = %#v; want %d-%d in %q", test.line, result, test.start, test.end, test.scope)
		}
	}
}

func TestPythonScopesRespectTabsAndFormFeeds(t *testing.T) {
	t.Parallel()
	source := strings.Join([]string{
		"def tabbed():",
		"\tif ready:",
		"\t\ttab_target()",
		"\tafter_tab()",
		"def fed():",
		"\f    if ready:",
		"\f        form_target()",
		"\f    after_form()",
	}, "\n")
	lines := strings.Split(source, "\n")
	backend := newPythonLanguage()
	for _, test := range []struct {
		line  int
		start int
		end   int
	}{
		{line: 3, start: 2, end: 3},
		{line: 4, start: 1, end: 4},
		{line: 7, start: 6, end: 7},
		{line: 8, start: 5, end: 8},
	} {
		start, end := backend.enclosingScope(lines, test.line)
		if start != test.start || end != test.end {
			t.Errorf("line %d scope = %d-%d, want %d-%d", test.line, start, end, test.start, test.end)
		}
	}

	root := t.TempDir()
	writeFile(t, root, "fixture.py", source)
	response, err := mustView(t, root).Inspect(
		"fixture.py:7",
		Options{Include: IncludeScope, Return: ReturnScope},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].Scope != "fed" ||
		response.Results[0].StartLine != 5 || response.Results[0].EndLine != 8 {
		t.Fatalf("form-feed scope = %#v", response.Results)
	}
}

func TestPythonCRLFCoordinatesRemainPhysical(t *testing.T) {
	t.Parallel()
	source := strings.Join([]string{
		"import os",
		"",
		"@decorator",
		"async def crlf_call(",
		"    value,",
		"):",
		"    if value:",
		"        target()",
	}, "\r\n")
	lines := strings.Split(source, "\n")
	backend := newPythonLanguage()
	definitions := backend.sourceDefinitions(lines)
	if len(definitions) != 1 || definitions[0].symbol != "crlf_call" ||
		definitions[0].line != 4 || definitions[0].scopeStart != 3 || definitions[0].scopeEnd != 8 {
		t.Fatalf("CRLF definitions = %#v", definitions)
	}
	if start, end, ok := backend.importRange(lines); !ok || start != 1 || end != 1 {
		t.Fatalf("CRLF imports = %d-%d, %v; want 1-1, true", start, end, ok)
	}
	if start, end := backend.enclosingScope(lines, 8); start != 7 || end != 8 {
		t.Fatalf("CRLF scope = %d-%d, want 7-8", start, end)
	}
}

func TestPythonInspectSelectsConcreteExpressionSymbols(t *testing.T) {
	t.Parallel()
	const source = `@pkg.decorate(option)
def caller():
    first = client.session.request(argument)
    second = factory[Model]()
    third = mapping.get("key")
    fourth = outer(inner())
    fifth = f"Wrong() {actual()}"
    sixth = t"Wrong() {template_call()}"
    seventh = "Wrong()"; right()
    eighth = type(value)
    yield item
    return result
    match subject:
        case captured:
            handle(captured)
    callable_match = match()
    callable_case = case()
    attribute = client.session.final
    chain = (
        client
        .session
        .request()
    )
    pass
`
	root := t.TempDir()
	writeFile(t, root, "fixture.py", source)
	view := mustView(t, root)
	tests := []struct {
		line int
		want string
	}{
		{line: 1, want: "decorate"},
		{line: 2, want: "caller"},
		{line: 3, want: "request"},
		{line: 4, want: "factory"},
		{line: 5, want: "get"},
		{line: 6, want: "outer"},
		{line: 7, want: "actual"},
		{line: 8, want: "template_call"},
		{line: 9, want: "right"},
		{line: 10, want: "type"},
		{line: 11, want: "item"},
		{line: 12, want: "result"},
		{line: 13, want: "subject"},
		{line: 14, want: "captured"},
		{line: 15, want: "handle"},
		{line: 16, want: "match"},
		{line: 17, want: "case"},
		{line: 18, want: "final"},
		{line: 20, want: "client"},
		{line: 21, want: "session"},
		{line: 22, want: "request"},
		{line: 24, want: ""},
	}
	for _, test := range tests {
		t.Run(fmt.Sprintf("line_%d", test.line), func(t *testing.T) {
			t.Parallel()
			response, err := view.Inspect(
				fmt.Sprintf("fixture.py:%d", test.line),
				Options{Include: IncludeScope, Return: ReturnScope},
			)
			if err != nil {
				t.Fatal(err)
			}
			if response.Symbol != test.want {
				t.Fatalf("symbol = %q, want %q", response.Symbol, test.want)
			}
		})
	}
}

func TestPythonFallbackDistinguishesSoftKeywordExpressions(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		line string
		want string
	}{
		{line: "match [key]", want: "match"},
		{line: "case[item]", want: "case"},
		{line: "match: int = 1", want: "match"},
		{line: "case: str", want: "case"},
		{line: "match(subject):", want: "subject"},
		{line: "case captured:", want: "captured"},
		{line: "match()", want: "match"},
	} {
		t.Run(test.line, func(t *testing.T) {
			t.Parallel()
			if got := pythonSymbolOnLine(test.line); got != test.want {
				t.Fatalf("symbol = %q, want %q", got, test.want)
			}
		})
	}
}

func TestPythonFallsBackForSymbolsInsideSyntaxErrors(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		source string
		line   int
		want   string
	}{
		{name: "unterminated call", source: "value = target(\n", line: 1, want: "target"},
		{name: "after broken definition", source: "def broken(\ntarget()\n", line: 2, want: "target"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			lines := pythonTestLines(test.source)
			backend, ok := prepareLanguageBackend(newPythonLanguage(), lines).(pythonLanguage)
			if !ok || backend.analysis == nil || backend.analysis.tree == nil {
				t.Fatal("expected a prepared concrete syntax tree")
			}
			if got, found := backend.symbolOnLine(lines, test.line); !found || got != test.want {
				t.Fatalf("symbol = %q, %v; want %q, true", got, found, test.want)
			}
		})
	}
}

func TestPythonInspectUsesOutermostConcreteCallTarget(t *testing.T) {
	t.Parallel()
	const source = `def caller():
    first = super().method()
    second = outer(client.inner())
    third = service.make().run()
`
	root := t.TempDir()
	writeFile(t, root, "fixture.py", source)
	view := mustView(t, root)
	for _, test := range []struct {
		line int
		want string
	}{
		{line: 2, want: "method"},
		{line: 3, want: "outer"},
		{line: 4, want: "run"},
	} {
		response, err := view.Inspect(
			fmt.Sprintf("fixture.py:%d", test.line),
			Options{Include: IncludeScope, Return: ReturnScope},
		)
		if err != nil {
			t.Fatal(err)
		}
		if response.Symbol != test.want {
			t.Fatalf("line %d symbol = %q, want %q", test.line, response.Symbol, test.want)
		}
	}
}

func TestPythonKeepsRepeatedTypeAliasesOnOneLine(t *testing.T) {
	t.Parallel()
	definitions := newPythonLanguage().sourceDefinitions(
		[]string{"type Alias = int; type Alias = str"},
	)
	if len(definitions) != 2 {
		t.Fatalf("definitions = %#v, want two Alias definitions", definitions)
	}
	if definitions[0].symbol != "Alias" || definitions[1].symbol != "Alias" ||
		definitions[0].column >= definitions[1].column {
		t.Fatalf("definitions = %#v, want ordered distinct columns", definitions)
	}
}

func TestPythonRejectsTypeCallAssignmentsAsAliases(t *testing.T) {
	t.Parallel()
	const source = `def outer(mock):
    type(mock)._mock_check_sig = checksig
    type(mock).__signature__ = signature
`
	definitions := newPythonLanguage().sourceDefinitions(pythonTestLines(source))
	if len(definitions) != 1 || definitions[0].symbol != "outer" {
		t.Fatalf("definitions = %#v, want only outer", definitions)
	}
}

func TestPythonPreparedBackendRejectsStaleAnalysis(t *testing.T) {
	t.Parallel()
	first := []string{"def first(): pass"}
	prepared, ok := prepareLanguageBackend(newPythonLanguage(), first).(pythonLanguage)
	if !ok || prepared.analysis == nil {
		t.Fatal("Python backend was not prepared")
	}
	if definitions := prepared.sourceDefinitions(first); len(definitions) != 1 ||
		definitions[0].symbol != "first" {
		t.Fatalf("prepared definitions = %#v", definitions)
	}
	second := []string{"def second(): pass"}
	if definitions := prepared.sourceDefinitions(second); len(definitions) != 1 ||
		definitions[0].symbol != "second" {
		t.Fatalf("stale prepared definitions = %#v", definitions)
	}
	empty, ok := prepared.prepareSource(nil).(pythonLanguage)
	if !ok || empty.analysis != nil {
		t.Fatal("preparing empty input retained an analysis")
	}
}

func TestPythonFindUsesXIDContinueBoundaries(t *testing.T) {
	t.Parallel()
	const source = "a = object()\n" +
		"a\u0301 = object()\n" +
		"var = object()\n" +
		"var\u00b7name = object()\n" +
		"a\n" +
		"a\u0301\n" +
		"var\n" +
		"var\u00b7name\n"
	root := t.TempDir()
	writeFile(t, root, "fixture.py", source)
	view := mustView(t, root)
	for _, test := range []struct {
		symbol string
		lines  []int
	}{
		{symbol: "a", lines: []int{1, 5}},
		{symbol: "a\u0301", lines: []int{2, 6}},
		{symbol: "var", lines: []int{3, 7}},
		{symbol: "var\u00b7name", lines: []int{4, 8}},
	} {
		response, err := view.Find(test.symbol, Options{
			Include:    IncludeRefs,
			Return:     ReturnLocations,
			NoComments: true,
			NoStrings:  true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := resultLines(response.Results); !reflect.DeepEqual(got, test.lines) {
			t.Fatalf("Find(%q) lines = %#v, want %#v; results = %#v", test.symbol, got, test.lines, response.Results)
		}
	}
}

func TestPythonFindTreatsLetterNumbersAsIdentifierCharacters(t *testing.T) {
	t.Parallel()
	if got := countSymbolOccurrences("Ⅻx = 1", "x"); got != 0 {
		t.Fatalf("partial match after Letter_Number = %d, want 0", got)
	}
}

func TestPythonIncompleteAndInvalidSourcesRecoverWithoutPanics(t *testing.T) {
	t.Parallel()
	const incomplete = `def good(): pass
def broken(
    value,
def later(): pass
payload = '''unterminated
def hidden(): pass
`
	backend := newPythonLanguage()
	definitions := backend.sourceDefinitions(pythonTestLines(incomplete))
	got := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		got = append(got, definition.symbol)
	}
	if want := []string{"good", "broken", "later"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("incomplete definitions = %#v, want %#v", got, want)
	}

	invalidUTF8 := "def before(): pass\npayload = \"" + string([]byte{0xff, 0xfe}) +
		"\"\ndef after(): pass\n# " + string([]byte{0xc0}) + "\n"
	invalidDefinitions := backend.sourceDefinitions(pythonTestLines(invalidUTF8))
	invalidSymbols := make([]string, 0, len(invalidDefinitions))
	for _, definition := range invalidDefinitions {
		invalidSymbols = append(invalidSymbols, definition.symbol)
	}
	if want := []string{"before", "after"}; !reflect.DeepEqual(invalidSymbols, want) {
		t.Fatalf("invalid-UTF-8 definitions = %#v, want %#v", invalidSymbols, want)
	}

	corpus := []string{
		"",
		"def open(\n",
		"x = f'{value:{width'\ndef recovered(): pass\n",
		"value = t'''unterminated\n# still string\ndef hidden(): pass",
		invalidUTF8,
	}
	for index, source := range corpus {
		t.Run(fmt.Sprintf("case_%d", index), func(t *testing.T) {
			t.Parallel()
			lines := strings.Split(source, "\n")
			_ = backend.sourceDefinitions(lines)
			_, _, _ = backend.importRange(lines)
			searchable := backend.searchLines(lines, true, true)
			if len(searchable) != len(lines) {
				t.Fatalf("search line count = %d, want %d", len(searchable), len(lines))
			}
			_ = backend.ignoredSearchLines(lines, true, true)
			_ = backend.cleanSource(source, true, true)
			_, _ = backend.enclosingScope(lines, 1)
			_, _ = backend.enclosingScope(lines, len(lines))
			for _, line := range lines {
				_, _ = backend.definitionSymbol(line)
				_ = backend.stripComment(line)
			}
		})
	}
	deepCall := "factory" + strings.Repeat("()", 512)
	if symbol, ok := backend.symbolOnLine([]string{deepCall}, 1); !ok || symbol != "factory" {
		t.Fatalf("deep call symbol = %q, %v; want factory, true", symbol, ok)
	}

	root := t.TempDir()
	writeFile(t, root, "fixture.py", invalidUTF8)
	view := mustView(t, root)
	outline, err := view.Outline("fixture.py", Options{Return: ReturnLocations})
	if err != nil {
		t.Fatal(err)
	}
	outlineSymbols := make([]string, 0, len(outline.Results))
	for _, result := range outline.Results {
		outlineSymbols = append(outlineSymbols, result.Symbol)
	}
	if want := []string{"before", "after"}; !reflect.DeepEqual(outlineSymbols, want) {
		t.Fatalf("invalid-UTF-8 outline = %#v, want %#v", outlineSymbols, want)
	}
}

func TestPythonRecoveryDoesNotSplitValidUnindentedContinuations(t *testing.T) {
	t.Parallel()
	const source = `async def collect(source):
    first = (
type,
)
    return [
item
async for item in source
]
`
	lines := pythonTestLines(source)
	definitions := newPythonLanguage().sourceDefinitions(lines)
	if len(definitions) != 1 || definitions[0].symbol != "collect" ||
		definitions[0].scopeStart != 1 || definitions[0].scopeEnd != 8 {
		t.Fatalf("definitions = %#v, want collect spanning 1-8", definitions)
	}
	if start, end := newPythonLanguage().enclosingScope(lines, 7); start != 1 || end != 8 {
		t.Fatalf("async-comprehension scope = %d-%d, want 1-8", start, end)
	}
}

func TestPythonFallbackResynchronizesHardDeclarations(t *testing.T) {
	t.Parallel()
	for _, source := range []string{
		"def broken(\n    value,\n    def later(): pass\n",
		"value = \\\n    def later(): pass\n",
		"value = \\   # invalid continuation\n    def later(): pass\n",
	} {
		definitions := lexPython(source).definitions
		foundLater := false
		for _, definition := range definitions {
			foundLater = foundLater || definition.symbol == "later"
		}
		if !foundLater {
			t.Fatalf("fallback definitions = %#v, want later for %q", definitions, source)
		}
	}

	const imports = `def broken(
    value,
    import nested
    from package import name
`
	if spans := lexPython(imports).imports; len(spans) != 2 ||
		spans[0] != (pythonLineSpan{start: 3, end: 3}) ||
		spans[1] != (pythonLineSpan{start: 4, end: 4}) {
		t.Fatalf("fallback imports = %#v, want lines 3 and 4", spans)
	}
}

func TestPythonFallbackPreservesValidKeywordContinuations(t *testing.T) {
	t.Parallel()
	const source = `from package \
    import value

def generate(items):
    result = (yield
from items)
`
	lexed := lexPython(strings.Join(pythonTestLines(source), "\n"))
	if len(lexed.imports) != 1 || lexed.imports[0] != (pythonLineSpan{start: 1, end: 2}) {
		t.Fatalf("imports = %#v, want lines 1-2", lexed.imports)
	}
	if len(lexed.definitions) != 1 || lexed.definitions[0].symbol != "generate" ||
		lexed.definitions[0].scopeStart != 4 || lexed.definitions[0].scopeEnd != 6 {
		t.Fatalf("definitions = %#v, want generate spanning lines 4-6", lexed.definitions)
	}
}

func TestPythonFallbackScopesIncludeIndentedTrailingTrivia(t *testing.T) {
	t.Parallel()
	const source = `def first():
    target()

    # trailing body comment

# outside comment
def after(): pass
`
	lines := pythonTestLines(source)
	lexed := lexPython(strings.Join(lines, "\n"))
	if len(lexed.definitions) != 2 || lexed.definitions[0].symbol != "first" ||
		lexed.definitions[0].scopeEnd != 5 || lexed.definitions[1].symbol != "after" {
		t.Fatalf("fallback definitions = %#v, want first through line 5 and after", lexed.definitions)
	}
	analysis := &pythonSourceAnalysis{
		source: strings.Join(lines, "\n"), lexed: lexed,
		definitions: lexed.definitions, lines: lines, lineCount: len(lines),
	}
	backend := newPythonLanguage()
	backend.analysis = analysis
	if start, end := backend.navigationScope(lines, 4); start != 1 || end != 5 {
		t.Fatalf("trailing-comment navigation scope = %d-%d, want 1-5", start, end)
	}
	if start, end := backend.navigationScope(lines, 6); start != 6 || end != 6 {
		t.Fatalf("outside-comment navigation scope = %d-%d, want 6-6", start, end)
	}
}

func TestPythonFormattedNestingBudgetRecoversAtNextLine(t *testing.T) {
	t.Parallel()
	field := "value"
	for range pythonMaximumFormattedNesting + 32 {
		field = "value:{" + field + "}"
	}
	source := "payload = f'{" + field + "}'\ndef after(): pass\n"
	definitions := lexPython(source).definitions
	if len(definitions) != 1 || definitions[0].symbol != "after" || definitions[0].line != 2 {
		t.Fatalf("definitions = %#v, want after on line 2", definitions)
	}
}

func TestPythonConcreteCallUnwrapHonorsDepthBudget(t *testing.T) {
	t.Parallel()
	source := "factory" + strings.Repeat("()", pythonMaximumSyntaxUnwrapDepth+32)
	tree, ok := parsePythonSyntax(source)
	if !ok || tree == nil {
		t.Fatalf("parse deep call: tree=%v, ok=%v", tree != nil, ok)
	}
	outermost := -1
	outermostSize := -1
	for nodeIndex, node := range tree.nodes {
		if node.kind == "call" && node.endByte-node.startByte > outermostSize {
			outermost = nodeIndex
			outermostSize = node.endByte - node.startByte
		}
	}
	if outermost < 0 {
		t.Fatal("deep call tree contains no call node")
	}
	if identifier := pythonCalledIdentifierNode(tree, outermost); identifier >= 0 {
		t.Fatalf("deep call unexpectedly unwrapped past depth budget to node %d", identifier)
	}
	if symbol, ok := newPythonLanguage().symbolOnLine([]string{source}, 1); !ok || symbol != "factory" {
		t.Fatalf("bounded deep-call fallback = %q, %v; want factory, true", symbol, ok)
	}
}

func pythonTestLines(source string) []string {
	return strings.Split(strings.TrimSuffix(source, "\n"), "\n")
}

func pythonLinesContainingSymbol(lines []string, symbol string) []int {
	found := make([]int, 0)
	for index, line := range lines {
		if countSymbolOccurrences(line, symbol) > 0 {
			found = append(found, index+1)
		}
	}
	return found
}

func FuzzPythonLanguageNeverPanics(f *testing.F) {
	for _, source := range []string{
		"",
		"def valid(value):\n    return f'{value!r}'\n",
		"def broken(\nfrom package import (\n    value,\n",
		"payload = t'''unterminated\ndef hidden(): pass",
		"@decorator\r\nasync def café(参数):\r\n\treturn 参数\r\n",
		string([]byte{'d', 'e', 'f', ' ', 0xff, '(', ')', ':', '\n'}),
	} {
		f.Add(source)
	}
	f.Fuzz(func(t *testing.T, source string) {
		if len(source) > 256*1024 {
			t.Skip()
		}
		lines := strings.Split(source, "\n")
		backend := prepareLanguageBackend(newPythonLanguage(), lines)
		definitions := backend.sourceDefinitions(lines)
		for _, definition := range definitions {
			if definition.symbol == "" || definition.line < 1 || definition.line > len(lines) ||
				definition.scopeStart < 1 || definition.scopeStart > definition.line ||
				definition.scopeEnd < definition.line || definition.scopeEnd > len(lines) {
				t.Fatalf("invalid definition: %#v", definition)
			}
		}
		if start, end, ok := backend.importRange(lines); ok &&
			(start < 1 || end < start || end > len(lines)) {
			t.Fatalf("invalid import range: %d-%d", start, end)
		}
		for _, options := range [][2]bool{{false, false}, {true, false}, {false, true}, {true, true}} {
			searchable := backend.searchLines(lines, options[0], options[1])
			if len(searchable) != len(lines) {
				t.Fatalf("search lines = %d, want %d", len(searchable), len(lines))
			}
		}
		if cleaner, ok := backend.(linePreservingSourceCleaner); ok {
			cleaned := cleaner.cleanSourceLines(lines, true, true)
			if len(cleaned) != len(lines) {
				t.Fatalf("clean lines = %d, want %d", len(cleaned), len(lines))
			}
		}
		for _, lineNo := range []int{1, len(lines)} {
			start, end := backend.enclosingScope(lines, lineNo)
			if start < 1 || end < start || end > len(lines) {
				t.Fatalf("invalid scope for line %d: %d-%d", lineNo, start, end)
			}
			_ = bestSymbolOnLine(lines, lineNo, backend)
		}
		masked := maskPythonSource(source, true, true)
		if len(masked) != len(source) {
			t.Fatalf("mask length = %d, want %d", len(masked), len(source))
		}
	})
}
