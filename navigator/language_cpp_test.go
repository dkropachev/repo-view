package navigator

import (
	"slices"
	"strconv"
	"strings"
	"testing"
)

type cppDefinitionSummary struct {
	symbol     string
	line       int
	column     int
	scopeStart int
	scopeEnd   int
	ownsScope  bool
}

func TestCPPBackendContractRegistryAndPublicIntegration(t *testing.T) {
	t.Parallel()

	backend := newCPPLanguage()
	if backend.name() != "cpp" {
		t.Fatalf("language name = %q, want cpp", backend.name())
	}
	contracts := []struct {
		name        string
		implemented bool
	}{
		{name: "sourceBackendPreparer", implemented: cppImplements[sourceBackendPreparer](backend)},
		{name: "findScopeResolverPreparer", implemented: cppImplements[findScopeResolverPreparer](backend)},
		{name: "linePreservingSourceCleaner", implemented: cppImplements[linePreservingSourceCleaner](backend)},
		{name: "navigationScopeResolver", implemented: cppImplements[navigationScopeResolver](backend)},
		{name: "sourceScopeNameResolver", implemented: cppImplements[sourceScopeNameResolver](backend)},
		{name: "symbolOccurrenceCounter", implemented: cppImplements[symbolOccurrenceCounter](backend)},
		{name: "sourceSymbolOccurrenceAugmenter", implemented: cppImplements[sourceSymbolOccurrenceAugmenter](backend)},
		{name: "sourceSymbolOccurrencePositionAugmenter", implemented: cppImplements[sourceSymbolOccurrencePositionAugmenter](backend)},
		{name: "authoritativeSymbolOnLineResolver", implemented: cppImplements[authoritativeSymbolOnLineResolver](backend)},
	}
	for _, contract := range contracts {
		if !contract.implemented {
			t.Errorf("C++ backend does not implement %s", contract.name)
		}
	}

	for _, extension := range []string{
		".C", ".cc", ".cpp", ".c++", ".hpp", ".h++", ".tcc", ".inl",
		".ii", ".ixx", ".cppm", ".ccm",
	} {
		registered := languageForExtension(extension)
		if registered.name() != "cpp" {
			t.Errorf("registered %s language = %q, want cpp", extension, registered.name())
		}
		if _, generic := registered.(braceLanguage); generic {
			t.Errorf("registered %s still uses generic braceLanguage", extension)
		}
	}

	const source = `#include <vector>
#define LIMIT 8
namespace api {
template<class T>
class Box {
public:
    explicit Box(T value) : value_(value) {}
    T value() const { return value_; }
private:
    T value_;
};
int target(int value);
}
int caller()
{
    api::Box<int> box{LIMIT};
    return api::target(box.value());
}
`
	lines := cppTestLines(source)
	if analysis := analyzeCPPSource(strings.Join(lines, "\n"), len(lines)); analysis == nil {
		t.Fatal("analyzeCPPSource returned nil for valid C++")
	}

	root := t.TempDir()
	writeFile(t, root, "fixture.cpp", source)
	view := mustView(t, root)
	outline, err := view.Outline("fixture.cpp", Options{Return: ReturnLocations})
	if err != nil {
		t.Fatal(err)
	}
	wantOutline := []string{
		"LIMIT", "api", "Box", "Box", "value", "value_", "target", "caller",
	}
	if got := cppResultSymbols(outline.Results); !slices.Equal(got, wantOutline) {
		t.Fatalf("outline symbols = %#v, want %#v", got, wantOutline)
	}
	for _, result := range outline.Results {
		if result.Kind != "def" || result.Language != "cpp" || result.Path != "fixture.cpp" {
			t.Fatalf("malformed C++ outline result: %#v", result)
		}
	}

	found, err := view.Find("target", Options{
		Include: IncludeRefs,
		Return:  ReturnScope,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found.Results) != 1 || found.Results[0].Scope != "caller" ||
		found.Results[0].StartLine != 14 || found.Results[0].EndLine != 18 {
		t.Fatalf("target reference scope = %#v, want caller at 14-18", found.Results)
	}

	inspected, err := view.Inspect(
		"fixture.cpp:17",
		Options{Include: IncludeScope, Return: ReturnScope},
	)
	if err != nil {
		t.Fatal(err)
	}
	if inspected.Symbol != "target" || len(inspected.Results) != 1 ||
		inspected.Results[0].Scope != "caller" || inspected.Results[0].StartLine != 14 ||
		inspected.Results[0].EndLine != 18 {
		t.Fatalf("Inspect target = %#v, want target in caller", inspected)
	}
}

func TestCPPDefinitionsHaveExactModernSemanticPolicy(t *testing.T) {
	t.Parallel()

	const source = `#define LIMIT 8
namespace outer::inner {
namespace alias = outer;
enum class State : unsigned {
    Idle,
    Busy = LIMIT,
};
using Count = unsigned long;
template<class T>
concept Sized = requires(T value) { value.size(); };
/** Box documentation. */
template<class T>
class Box {
public:
    Box() = default;
    ~Box() = default;
    Box& operator=(const Box&) = delete;
    explicit operator bool() const { return true; }
    T field, other;
    /** Value documentation. */
    T value() const { return field; }
};
int prototype(int value);
inline int first = 1, *second = nullptr;
auto [left, right] = pair;
}
int caller()
{
    Box<int> local(first);
    auto lambda = [local](int parameter) { return parameter; };
    if (lambda(first)) { return first; }
    return 0;
}
`
	lines := cppTestLines(source)
	definitions := newCPPLanguage().sourceDefinitions(lines)
	wantSymbols := []string{
		"LIMIT", "outer::inner", "alias", "State", "Idle", "Busy", "Count",
		"Sized", "Box", "Box", "~Box", "operator=", "operator bool", "field",
		"other", "value", "prototype", "first", "second", "left", "right", "caller",
	}
	if got := cppDefinitionSymbols(definitions); !slices.Equal(got, wantSymbols) {
		t.Fatalf("definitions = %#v, want %#v", got, wantSymbols)
	}
	for _, forbidden := range []string{
		"T", "value", "local", "lambda", "parameter", "ready", "if",
	} {
		if forbidden == "value" {
			// The method is a definition; its parameter on line 23 is not a
			// second definition with the same spelling.
			if cppDefinitionCount(definitions, forbidden) != 1 {
				t.Errorf("value definitions = %d, want method only", cppDefinitionCount(definitions, forbidden))
			}
			continue
		}
		if slices.Contains(cppDefinitionSymbols(definitions), forbidden) {
			t.Errorf("non-outline binding %q became a definition: %#v", forbidden, definitions)
		}
	}

	boxLine := cppLineContaining(t, lines, "class Box")
	box := cppDefinitionAt(t, definitions, "Box", boxLine)
	boxDocLine := cppLineContaining(t, lines, "/** Box documentation.")
	boxEnd := cppLineContaining(t, lines, "int prototype") - 1
	if got, want := cppSummarizeDefinition(box), (cppDefinitionSummary{
		symbol:     "Box",
		line:       boxLine,
		column:     strings.Index(lines[boxLine-1], "Box") + 1,
		scopeStart: boxDocLine,
		scopeEnd:   boxEnd,
		ownsScope:  true,
	}); got != want {
		t.Errorf("Box metadata = %#v, want %#v", got, want)
	}

	valueLine := cppLineContaining(t, lines, "T value()")
	value := cppDefinitionAt(t, definitions, "value", valueLine)
	valueDocLine := cppLineContaining(t, lines, "/** Value documentation.")
	if got, want := cppSummarizeDefinition(value), (cppDefinitionSummary{
		symbol:     "value",
		line:       valueLine,
		column:     strings.Index(lines[valueLine-1], "value") + 1,
		scopeStart: valueDocLine,
		scopeEnd:   valueLine,
		ownsScope:  true,
	}); got != want {
		t.Errorf("value metadata = %#v, want %#v", got, want)
	}

	for _, symbol := range []string{"Box", "~Box", "operator="} {
		lineNo := cppLineContaining(t, lines, symbol+"(")
		definition := cppDefinitionAt(t, definitions, symbol, lineNo)
		if definition.ownsScope || definition.scopeStart != lineNo || definition.scopeEnd != lineNo {
			t.Errorf("declaration-only special member %q owns a scope: %#v", symbol, definition)
		}
	}
}

func TestCPPDefinitionSymbolRejectsExpressionsAndRecognizesSpecialFunctions(t *testing.T) {
	t.Parallel()

	backend := newCPPLanguage()
	tests := []struct {
		name string
		line string
		want string
		ok   bool
	}{
		{name: "noexcept function", line: `int render() noexcept {`, want: "render", ok: true},
		{name: "prototype", line: `int render(int);`, want: "render", ok: true},
		{name: "constructor", line: `Widget::Widget() {`, want: "Widget", ok: true},
		{name: "destructor", line: `Widget::~Widget() = default;`, want: "~Widget", ok: true},
		{name: "operator", line: `Value operator+(Value, Value);`, want: "operator+", ok: true},
		{name: "conversion", line: `explicit operator bool() const {`, want: "operator bool", ok: true},
		{name: "call", line: `service.render();`},
		{name: "condition", line: `if (ready()) {`},
		{name: "cast", line: `static_cast<Value>(input);`},
		{name: "comment", line: `/* int hidden(); */`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, ok := backend.definitionSymbol(test.line)
			if got != test.want || ok != test.ok {
				t.Fatalf("definitionSymbol(%q) = %q, %v; want %q, %v",
					test.line, got, ok, test.want, test.ok)
			}
		})
	}
}

func TestCPPImportsCoverHeadersModulesAndRejectFakeSyntax(t *testing.T) {
	t.Parallel()

	const source = `/* project license */
module;
#include <vector>
# include_next "platform.hpp"
#define HEADER_FILE "dynamic.hpp"
#include HEADER_FILE
export module demo.core:part;
import std;
export import demo.util;
import :detail;
import <string>;
const char *fake = "import fake.module; #include <fake.hpp>";
/* import comment.only; */
void import_name();
`
	lines := cppTestLines(source)
	backend := newCPPLanguage()
	start, end, ok := backend.importRange(lines)
	if !ok || start != 3 || end != 11 {
		t.Fatalf("import range = %d-%d, %v; want 3-11, true", start, end, ok)
	}
	definitions := backend.sourceDefinitions(lines)
	for _, symbol := range []string{"HEADER_FILE", "demo.core:part", "import_name"} {
		if !slices.Contains(cppDefinitionSymbols(definitions), symbol) {
			t.Errorf("definition %q missing: %#v", symbol, definitions)
		}
	}
	for _, phantom := range []string{"std", "demo.util", "detail", "fake.module", "comment.only"} {
		if slices.Contains(cppDefinitionSymbols(definitions), phantom) {
			t.Errorf("import operand %q became a definition: %#v", phantom, definitions)
		}
	}

	fakeOnly := cppTestLines(`void caller() {
    import(fake_name);
    const char *text = "import string_only;";
    // import comment_only;
}`)
	if fakeStart, fakeEnd, fakeOK := backend.importRange(fakeOnly); fakeOK {
		t.Fatalf("fake-only imports = %d-%d, true; want none", fakeStart, fakeEnd)
	}

	root := t.TempDir()
	writeFile(t, root, "module.cpp", source)
	response, err := mustView(t, root).Inspect(
		"module.cpp:12",
		Options{Include: IncludeImports, Return: ReturnScope},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 2 {
		t.Fatalf("Inspect imports results = %#v, want scope plus imports", response.Results)
	}
	imports := response.Results[1]
	if imports.Kind != "imports" || imports.Language != "cpp" ||
		imports.StartLine != 3 || imports.EndLine != 11 ||
		imports.Code != strings.Join(lines[2:11], "\n") {
		t.Fatalf("import result = %#v, want exact dependency span", imports)
	}
}

func TestCPPRawStringsSearchMasksAndScopesKeepCoordinates(t *testing.T) {
	t.Parallel()

	const source = `/** Worker documentation. */
namespace api {
class Worker {
public:
    int run(bool ready)
    {
        const char *raw = R"TAG(
} target(); {
#include <fake.hpp> // target
)TAG";
        if (ready) {
            while (ready) {
                target();
            }
        } else {
            fallback();
        }
        // target in a real comment
        return 0;
    }
};
}
`
	lines := cppTestLines(source)
	backend := prepareLanguageBackend(newCPPLanguage(), lines)
	searchable := backend.searchLines(lines, true, true)
	if len(searchable) != len(lines) ||
		len(strings.Join(searchable, "\n")) != len(strings.Join(lines, "\n")) {
		t.Fatalf("search mask changed physical coordinates")
	}
	counter := backend.(symbolOccurrenceCounter)
	realTargetLine := cppLineContaining(t, lines, "                target();")
	for lineIndex, line := range searchable {
		want := 0
		if lineIndex+1 == realTargetLine {
			want = 1
		}
		if got := counter.countSymbolOccurrences(line, "target"); got != want {
			t.Errorf("masked line %d target count = %d, want %d; %q",
				lineIndex+1, got, want, line)
		}
	}

	commentsOnly := backend.searchLines(lines, true, false)
	rawPayloadLine := cppLineContaining(t, lines, "} target(); {")
	if !strings.Contains(commentsOnly[rawPayloadLine-1], "target") ||
		!strings.Contains(commentsOnly[rawPayloadLine], "// target") {
		t.Fatalf("comment masking interpreted raw-string payload: %#v", commentsOnly)
	}
	actualCommentLine := cppLineContaining(t, lines, "target in a real comment")
	if strings.Contains(commentsOnly[actualCommentLine-1], "target") {
		t.Fatalf("real line comment survived comment mask: %q", commentsOnly[actualCommentLine-1])
	}

	if start, end := backend.enclosingScope(lines, realTargetLine); start != 12 || end != 14 {
		t.Fatalf("smallest while scope = %d-%d, want 12-14", start, end)
	}
	resolver := backend.(navigationScopeResolver)
	if start, end := resolver.navigationScope(lines, realTargetLine); start != 5 || end != 20 {
		t.Fatalf("target navigation scope = %d-%d, want run at 5-20", start, end)
	}
	fallbackLine := cppLineContaining(t, lines, "fallback();")
	if start, end := backend.enclosingScope(lines, fallbackLine); start != 15 || end != 17 {
		t.Fatalf("else scope = %d-%d, want 15-17", start, end)
	}
	if got := scopeName(lines, realTargetLine, backend); got != "run" {
		t.Fatalf("target scope name = %q, want run", got)
	}

	definitions := backend.sourceDefinitions(lines)
	worker := cppDefinitionAt(t, definitions, "Worker", 3)
	if worker.scopeEnd != 21 || !worker.ownsScope {
		t.Fatalf("Worker scope = %#v, want 3-21 owner", worker)
	}
	api := cppDefinitionAt(t, definitions, "api", 2)
	if api.scopeStart != 1 || api.scopeEnd != 22 || !api.ownsScope {
		t.Fatalf("documented namespace scope = %#v, want 1-22 owner", api)
	}
}

func TestCPPFindAndInspectUseQualifiedNamesAndLexicalAuthority(t *testing.T) {
	t.Parallel()

	const source = `namespace api {
int target(int value);
}
#if defined(FEATURE_FLAG)
#endif
int caller()
{
    R"raw(target in raw string)raw";
    0x1.targetp0;
    first + service.template run<int>(argument);
    return api /* bridge */ ::
        target(1);
}
`
	lines := cppTestLines(source)
	root := t.TempDir()
	writeFile(t, root, "find.cpp", source)
	view := mustView(t, root)

	found, err := view.Find("target", Options{
		Include:    IncludeBoth,
		Return:     ReturnLocations,
		NoStrings:  true,
		NoComments: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cppResultLines(found.Results), []int{2, 12}; !slices.Equal(got, want) {
		t.Fatalf("Find target lines = %#v, want %#v; results=%#v", got, want, found.Results)
	}
	if got, want := cppResultKinds(found.Results), []string{"def", "ref"}; !slices.Equal(got, want) {
		t.Fatalf("Find target kinds = %#v, want %#v", got, want)
	}
	partial, err := view.Find("targetp", Options{Include: IncludeBoth, Return: ReturnLocations})
	if err != nil {
		t.Fatal(err)
	}
	if len(partial.Results) != 0 {
		t.Fatalf("preprocessing-number fragment matched: %#v", partial.Results)
	}
	qualified, err := view.Find("api::target", Options{
		Include:    IncludeRefs,
		Return:     ReturnLocations,
		NoStrings:  true,
		NoComments: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cppResultLines(qualified.Results), []int{11}; !slices.Equal(got, want) {
		t.Fatalf("qualified Find lines = %#v, want %#v; results=%#v", got, want, qualified.Results)
	}

	for _, test := range []struct {
		line int
		want string
	}{
		{line: 4, want: "FEATURE_FLAG"},
		{line: 8, want: ""},
		{line: 9, want: ""},
		{line: 10, want: "run"},
		{line: 11, want: "api"},
		{line: 12, want: "target"},
	} {
		response, inspectErr := view.Inspect(
			"find.cpp:"+strconv.Itoa(test.line),
			Options{Include: IncludeScope, Return: ReturnLocations},
		)
		if inspectErr != nil {
			t.Fatal(inspectErr)
		}
		if response.Symbol != test.want {
			t.Errorf("Inspect line %d symbol = %q, want %q", test.line, response.Symbol, test.want)
		}
	}
	if symbol, ok := newCPPLanguage().symbolOnLine([]string{"x + target();"}, 1); !ok || symbol != "target" {
		t.Fatalf("Inspect call priority = %q, %v; want target, true", symbol, ok)
	}

	backend := prepareLanguageBackend(newCPPLanguage(), lines)
	if got := scopeName(lines, 4, backend); got != "" {
		t.Fatalf("preprocessor reference attached to later scope %q", got)
	}
}

func cppTestLines(source string) []string {
	return strings.Split(strings.TrimSuffix(source, "\n"), "\n")
}

func cppImplements[Contract any](backend any) bool {
	_, ok := backend.(Contract)
	return ok
}

func cppDefinitionSymbols(definitions []sourceDefinition) []string {
	symbols := make([]string, len(definitions))
	for index, definition := range definitions {
		symbols[index] = definition.symbol
	}
	return symbols
}

func cppDefinitionCount(definitions []sourceDefinition, symbol string) int {
	count := 0
	for _, definition := range definitions {
		if definition.symbol == symbol {
			count++
		}
	}
	return count
}

func cppDefinitionAt(
	t *testing.T,
	definitions []sourceDefinition,
	symbol string,
	line int,
) sourceDefinition {
	t.Helper()
	for _, definition := range definitions {
		if definition.symbol == symbol && definition.line == line {
			return definition
		}
	}
	t.Fatalf("definition %q on line %d missing from %#v", symbol, line, definitions)
	return sourceDefinition{}
}

func cppSummarizeDefinition(definition sourceDefinition) cppDefinitionSummary {
	return cppDefinitionSummary{
		symbol: definition.symbol, line: definition.line, column: definition.column,
		scopeStart: definition.scopeStart, scopeEnd: definition.scopeEnd,
		ownsScope: definition.ownsScope,
	}
}

func cppLineContaining(t *testing.T, lines []string, fragment string) int {
	t.Helper()
	for index, line := range lines {
		if strings.Contains(line, fragment) {
			return index + 1
		}
	}
	t.Fatalf("source does not contain %q", fragment)
	return 0
}

func cppResultSymbols(results []Result) []string {
	symbols := make([]string, len(results))
	for index, result := range results {
		symbols[index] = result.Symbol
	}
	return symbols
}

func cppResultLines(results []Result) []int {
	lines := make([]int, len(results))
	for index, result := range results {
		lines[index] = result.Line
	}
	return lines
}

func cppResultKinds(results []Result) []string {
	kinds := make([]string, len(results))
	for index, result := range results {
		kinds[index] = result.Kind
	}
	return kinds
}
