package repoview

import (
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
)

type cHighLevelDefinitionSummary struct {
	symbol     string
	line       int
	column     int
	scopeStart int
	scopeEnd   int
	ownsScope  bool
}

func TestCBackendContractAndRepoViewIntegration(t *testing.T) {
	t.Parallel()

	backend := newCLanguage()
	if backend.name() != "c" {
		t.Fatalf("language name = %q, want c", backend.name())
	}
	contracts := []struct {
		name        string
		implemented bool
	}{
		{name: "sourceBackendPreparer", implemented: cHighLevelImplements[sourceBackendPreparer](backend)},
		{name: "findScopeResolverPreparer", implemented: cHighLevelImplements[findScopeResolverPreparer](backend)},
		{name: "linePreservingSourceCleaner", implemented: cHighLevelImplements[linePreservingSourceCleaner](backend)},
		{name: "navigationScopeResolver", implemented: cHighLevelImplements[navigationScopeResolver](backend)},
		{name: "sourceScopeNameResolver", implemented: cHighLevelImplements[sourceScopeNameResolver](backend)},
		{name: "symbolOccurrenceCounter", implemented: cHighLevelImplements[symbolOccurrenceCounter](backend)},
		{name: "sourceSymbolOccurrenceAugmenter", implemented: cHighLevelImplements[sourceSymbolOccurrenceAugmenter](backend)},
		{name: "sourceSymbolOccurrencePositionAugmenter", implemented: cHighLevelImplements[sourceSymbolOccurrencePositionAugmenter](backend)},
		{name: "authoritativeSymbolOnLineResolver", implemented: cHighLevelImplements[authoritativeSymbolOnLineResolver](backend)},
	}
	for _, contract := range contracts {
		if !contract.implemented {
			t.Fatalf("C backend does not implement %s", contract.name)
		}
	}

	for _, extension := range []string{".c", ".h"} {
		registered := languageForExtension(extension)
		if registered.name() != "c" {
			t.Fatalf("registered %s backend name = %q, want c", extension, registered.name())
		}
		if _, generic := registered.(braceLanguage); generic {
			t.Fatalf("registered %s backend still uses generic braceLanguage", extension)
		}
		_, valueBackend := any(registered).(cLanguage)
		_, pointerBackend := any(registered).(*cLanguage)
		if !valueBackend && !pointerBackend {
			t.Fatalf("registered %s backend has type %T, want dedicated cLanguage", extension, registered)
		}
	}

	const source = `#include <stddef.h>
#define LIMIT 8
struct Item {
    int value;
};
int target(const struct Item *item);
int caller(struct Item *item)
{
    if (item != NULL) {
        return target(item);
    }
    return LIMIT;
}
`
	root := t.TempDir()
	writeFile(t, root, "fixture.c", source)
	view := mustView(t, root)
	outline, err := view.Outline("fixture.c", Options{Return: ReturnLocations})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cHighLevelResultSymbols(outline.Results),
		[]string{"LIMIT", "Item", "value", "target", "caller"}; !slices.Equal(got, want) {
		t.Fatalf("outline symbols = %#v, want %#v", got, want)
	}
	for _, result := range outline.Results {
		if result.Kind != "def" || result.Language != "c" || result.Path != "fixture.c" {
			t.Fatalf("malformed C outline result: %#v", result)
		}
	}

	found, err := view.Find("target", Options{Include: IncludeRefs, Return: ReturnScope})
	if err != nil {
		t.Fatal(err)
	}
	if len(found.Results) != 1 || found.Results[0].Scope != "caller" ||
		found.Results[0].StartLine != 7 || found.Results[0].EndLine != 13 {
		t.Fatalf("target scope = %#v, want caller at 7-13", found.Results)
	}

	inspected, err := view.Inspect(
		"fixture.c:10",
		Options{Include: IncludeScope, Return: ReturnScope},
	)
	if err != nil {
		t.Fatal(err)
	}
	if inspected.Symbol != "target" || len(inspected.Results) != 1 ||
		inspected.Results[0].Scope != "caller" || inspected.Results[0].StartLine != 7 ||
		inspected.Results[0].EndLine != 13 {
		t.Fatalf("Inspect target = %#v, want caller navigation scope", inspected)
	}
}

func TestCDefinitionSymbolRejectsExpressionsAndRecognizesDeclarations(t *testing.T) {
	t.Parallel()

	backend := newCLanguage()
	tests := []struct {
		name string
		line string
		want string
		ok   bool
	}{
		{name: "function definition", line: `int render(void) {`, want: "render", ok: true},
		{name: "prototype", line: `int render(const char *text);`, want: "render", ok: true},
		{name: "file object", line: `static unsigned total = 1;`, want: "total", ok: true},
		{name: "typedef", line: `typedef unsigned long word_t;`, want: "word_t", ok: true},
		{name: "tag", line: `struct Packet {`, want: "Packet", ok: true},
		{name: "macro", line: `#define BUFFER_SIZE 4096`, want: "BUFFER_SIZE", ok: true},
		{name: "call", line: `render();`},
		{name: "condition", line: `if (ready()) {`},
		// definitionSymbol is intentionally line-local; sourceDefinitions applies
		// the enclosing-scope policy that excludes ordinary locals.
		{name: "declaration without source context", line: `    int local = render();`, want: "local", ok: true},
		{name: "comment", line: `/* int hidden(void); */`},
		{name: "string", line: `const char *text = "int hidden(void);";`, want: "text", ok: true},
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

func TestCDefinitionsHaveExactMetadataAndSemanticPolicy(t *testing.T) {
	t.Parallel()

	const source = `#define LIMIT 8
typedef unsigned long size_word;
union Forward;
struct Packet {
    unsigned code;
    unsigned flags : 3;
    unsigned : 1;
    int (*dispatch)(struct Packet *);
    union {
        long alternate;
    };
};
enum State {
    STATE_IDLE,
    STATE_BUSY = LIMIT,
};
static int first = 1, *second, values[3];
int prototype(const struct Packet *packet);
static int
run(struct Packet *packet)
{
    int local = packet->code;
    static int local_static = 1;
    int local_prototype(int);
    typedef int local_word;
    struct Local {
        int field;
    };
    int nested(int value) { return value; }
    return nested(local) + local_static;
}
`
	lines := cHighLevelTestLines(source)
	definitions := newCLanguage().sourceDefinitions(lines)

	want := []cHighLevelDefinitionSummary{
		cHighLevelExpectedDefinition(t, lines, "LIMIT", "#define LIMIT", 1, 1, false),
		cHighLevelExpectedDefinition(t, lines, "size_word", "typedef unsigned", 2, 2, false),
		cHighLevelExpectedDefinition(t, lines, "Forward", "union Forward", 3, 3, false),
		cHighLevelExpectedDefinition(t, lines, "Packet", "struct Packet", 4, 12, true),
		cHighLevelExpectedDefinition(t, lines, "code", "unsigned code", 5, 5, false),
		cHighLevelExpectedDefinition(t, lines, "flags", "unsigned flags", 6, 6, false),
		cHighLevelExpectedDefinition(t, lines, "dispatch", "(*dispatch)", 8, 8, false),
		cHighLevelExpectedDefinition(t, lines, "alternate", "long alternate", 10, 10, false),
		cHighLevelExpectedDefinition(t, lines, "State", "enum State", 13, 16, true),
		cHighLevelExpectedDefinition(t, lines, "STATE_IDLE", "STATE_IDLE", 14, 14, false),
		cHighLevelExpectedDefinition(t, lines, "STATE_BUSY", "STATE_BUSY", 15, 15, false),
		cHighLevelExpectedDefinition(t, lines, "first", "static int first", 17, 17, false),
		cHighLevelExpectedDefinition(t, lines, "second", "*second", 17, 17, false),
		cHighLevelExpectedDefinition(t, lines, "values", "values[3]", 17, 17, false),
		cHighLevelExpectedDefinition(t, lines, "prototype", "int prototype", 18, 18, false),
		cHighLevelExpectedDefinition(t, lines, "run", "run(struct", 19, 31, true),
		cHighLevelExpectedDefinition(t, lines, "local_prototype", "local_prototype", 24, 24, false),
		cHighLevelExpectedDefinition(t, lines, "local_word", "local_word", 25, 25, false),
		cHighLevelExpectedDefinition(t, lines, "Local", "struct Local", 26, 28, true),
		cHighLevelExpectedDefinition(t, lines, "field", "int field", 27, 27, false),
		cHighLevelExpectedDefinition(t, lines, "nested", "int nested", 29, 29, true),
	}
	if got := cHighLevelDefinitionSummaries(definitions); !reflect.DeepEqual(got, want) {
		t.Fatalf("definitions =\n%#v\nwant\n%#v", got, want)
	}

	for _, forbidden := range []string{
		"packet", "local", "local_static", "value",
	} {
		if slices.Contains(cHighLevelDefinitionSymbols(definitions), forbidden) {
			t.Fatalf("parameter or ordinary local %q became an outline definition: %#v",
				forbidden, definitions)
		}
	}
}

func TestCComplexDeclaratorsFunctionPointersAndKAndRDefinitions(t *testing.T) {
	t.Parallel()

	const source = `int (*signal_handler(int signal_number, void (*handler)(int)))(int);
int (*global_callback)(double), plain_object, array_object[4];
typedef int (*callback_type)(int);
typedef struct Named {
    int member;
} NamedAlias;
struct Hooks {
    int (*on_event)(int code);
    unsigned flags, mode;
};
int legacy(value, callback)
int value;
int (*callback)();
{
    int ordinary_local = value;
    return callback(ordinary_local);
}
int (*factory(void))(double)
{
    return global_callback;
}
`
	lines := cHighLevelTestLines(source)
	definitions := newCLanguage().sourceDefinitions(lines)
	wantSymbols := []string{
		"signal_handler", "global_callback", "plain_object", "array_object",
		"callback_type", "Named", "member", "NamedAlias", "Hooks", "on_event",
		"flags", "mode", "legacy", "factory",
	}
	if got := cHighLevelDefinitionSymbols(definitions); !slices.Equal(got, wantSymbols) {
		t.Fatalf("complex declarator definitions = %#v, want %#v", got, wantSymbols)
	}

	for _, test := range []struct {
		symbol     string
		line       int
		column     int
		scopeStart int
		scopeEnd   int
		ownsScope  bool
	}{
		{symbol: "signal_handler", line: 1, column: 7, scopeStart: 1, scopeEnd: 1},
		{symbol: "global_callback", line: 2, column: 7, scopeStart: 2, scopeEnd: 2},
		{symbol: "plain_object", line: 2, column: 33, scopeStart: 2, scopeEnd: 2},
		{symbol: "array_object", line: 2, column: 47, scopeStart: 2, scopeEnd: 2},
		{symbol: "callback_type", line: 3, column: 15, scopeStart: 3, scopeEnd: 3},
		{symbol: "Named", line: 4, column: 16, scopeStart: 4, scopeEnd: 6, ownsScope: true},
		{symbol: "NamedAlias", line: 6, column: 3, scopeStart: 4, scopeEnd: 6},
		{symbol: "legacy", line: 11, column: 5, scopeStart: 11, scopeEnd: 17, ownsScope: true},
		{symbol: "factory", line: 18, column: 7, scopeStart: 18, scopeEnd: 21, ownsScope: true},
	} {
		definition := cHighLevelDefinitionNamed(t, definitions, test.symbol)
		got := cHighLevelDefinitionSummaryOf(definition)
		want := cHighLevelDefinitionSummary(test)
		if got != want {
			t.Errorf("definition %q = %#v, want %#v", test.symbol, got, want)
		}
	}

	for _, forbidden := range []string{
		"signal_number", "handler", "code", "value", "callback", "ordinary_local",
	} {
		if slices.Contains(cHighLevelDefinitionSymbols(definitions), forbidden) {
			t.Errorf("parameter or local %q became a definition: %#v", forbidden, definitions)
		}
	}
}

func TestCImportsCoverIncludeNextEmbedMacroOperandsAndContinuations(t *testing.T) {
	t.Parallel()

	const source = `/* project license */
#ifndef FIXTURE_H
#define FIXTURE_H
#define HEADER_FILE "dynamic.h"
#include <stddef.h>
# include "local.h" /* trailing comment */
#include_next <platform.h>
#include HEADER_FILE
#if __has_include( \
    <optional.h>)
# include \
    <optional.h>
#endif
#embed "blob.bin" \
    limit(1024)
const char *fake_directive = "#include <string-only.h>";
/* #include <comment-only.h> */
#define FAKE_INCLUDE "#include <macro-string.h>"
#endif
`
	lines := cHighLevelTestLines(source)
	backend := newCLanguage()
	start, end, ok := backend.importRange(lines)
	if !ok || start != 5 || end != 15 {
		t.Fatalf("import range = %d-%d, %v; want 5-15, true", start, end, ok)
	}

	wantMacros := []string{"FIXTURE_H", "HEADER_FILE", "FAKE_INCLUDE"}
	definitions := backend.sourceDefinitions(lines)
	for _, macro := range wantMacros {
		if !slices.Contains(cHighLevelDefinitionSymbols(definitions), macro) {
			t.Errorf("macro %q missing from definitions: %#v", macro, definitions)
		}
	}
	for _, phantom := range []string{"include", "optional", "string-only", "macro-string"} {
		if slices.Contains(cHighLevelDefinitionSymbols(definitions), phantom) {
			t.Errorf("preprocessor operand %q became a definition: %#v", phantom, definitions)
		}
	}

	root := t.TempDir()
	writeFile(t, root, "fixture.h", source)
	response, err := mustView(t, root).Inspect(
		"fixture.h:16",
		Options{Include: IncludeImports, Return: ReturnScope},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 2 {
		t.Fatalf("Inspect imports results = %#v, want scope plus imports", response.Results)
	}
	imports := response.Results[1]
	if imports.Kind != "imports" || imports.Language != "c" ||
		imports.StartLine != 5 || imports.EndLine != 15 ||
		imports.Code != strings.Join(lines[4:15], "\n") {
		t.Fatalf("import result = %#v, want exact dependency span", imports)
	}
}

func TestCSearchMaskingAndLinePreservingCleaning(t *testing.T) {
	t.Parallel()

	const source = `int target(void);
const char *text = "target // not comment";
int character = 't'; // target in line comment
/* target in leading block */ target();
/* target across
   another target */ target();
#define INVOKE_TARGET() target()
#define TARGET_TEXT "target"
#if 0
target();
#endif`
	lines := cHighLevelTestLines(source)
	backend := prepareLanguageBackend(newCLanguage(), lines)
	searchable := backend.searchLines(lines, true, true)
	if len(searchable) != len(lines) ||
		len(strings.Join(searchable, "\n")) != len(strings.Join(lines, "\n")) {
		t.Fatalf("search mask changed physical coordinates: %#v", searchable)
	}
	counter := backend.(symbolOccurrenceCounter)
	wantCounts := []int{1, 0, 0, 1, 0, 1, 1, 0, 0, 1, 0}
	for index, line := range searchable {
		if got := counter.countSymbolOccurrences(line, "target"); got != wantCounts[index] {
			t.Errorf("line %d target count = %d, want %d; masked=%q",
				index+1, got, wantCounts[index], line)
		}
	}

	commentsOnly := backend.searchLines(lines, true, false)
	if !strings.Contains(commentsOnly[1], "target // not comment") ||
		strings.Contains(commentsOnly[2], "target in line comment") ||
		strings.Contains(commentsOnly[4], "target across") {
		t.Fatalf("comment masking confused literals and comments: %#v", commentsOnly)
	}
	stringsOnly := backend.searchLines(lines, false, true)
	if strings.Contains(stringsOnly[1], "target // not comment") ||
		!strings.Contains(stringsOnly[2], "target in line comment") ||
		!strings.Contains(stringsOnly[4], "target across") {
		t.Fatalf("string masking confused comments and literals: %#v", stringsOnly)
	}

	cleaner := backend.(linePreservingSourceCleaner)
	cleanedLines := cleaner.cleanSourceLines(lines, true, false)
	if len(cleanedLines) != len(lines) ||
		!strings.Contains(cleanedLines[3], "target();") ||
		strings.Contains(strings.Join(cleanedLines, "\n"), "leading block") ||
		strings.Contains(strings.Join(cleanedLines, "\n"), "line comment") {
		t.Fatalf("line-preserving comment cleaning = %#v", cleanedLines)
	}
	cleaned := backend.cleanSource(source, true, false)
	if !strings.Contains(cleaned, `"target // not comment"`) ||
		!strings.Contains(cleaned, "target();") || strings.Contains(cleaned, "leading block") ||
		strings.Contains(cleaned, "line comment") {
		t.Fatalf("cleaned C source = %q", cleaned)
	}
}

func TestCScopesIgnoreOpaqueAndPreprocessorBraces(t *testing.T) {
	t.Parallel()

	const source = `/** outer documentation */
int outer(int ready)
{
    const char *text = "} target {";
    /* } target { */
#define BRACES } target {
#if 0
    {
#endif
    if (ready) {
        while (ready--) {
            target();
        }
    }
    return 0;
}
struct Config {
    int field;
};
int tail(void) { target(); }
`
	lines := cHighLevelTestLines(source)
	backend := prepareLanguageBackend(newCLanguage(), lines)
	if start, end := backend.enclosingScope(lines, 12); start != 11 || end != 13 {
		t.Fatalf("smallest while scope = %d-%d, want 11-13", start, end)
	}
	resolver := backend.(navigationScopeResolver)
	if start, end := resolver.navigationScope(lines, 12); start != 1 || end != 16 {
		t.Fatalf("outer navigation scope = %d-%d, want documented function 1-16", start, end)
	}
	if start, end := resolver.navigationScope(lines, 18); start != 17 || end != 19 {
		t.Fatalf("struct navigation scope = %d-%d, want 17-19", start, end)
	}
	if start, end := resolver.navigationScope(lines, 20); start != 20 || end != 20 {
		t.Fatalf("one-line tail navigation scope = %d-%d, want 20-20", start, end)
	}

	root := t.TempDir()
	writeFile(t, root, "scope.c", source)
	found, err := mustView(t, root).Find(
		"target",
		Options{Include: IncludeRefs, Return: ReturnScope, NoComments: true, NoStrings: true},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := cHighLevelResultLocations(found.Results), []string{"scope.c:1-16", "scope.c:20-20"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("target navigation results = %#v, want %#v", got, want)
	}
	if len(found.Results) != 2 || found.Results[0].Scope != "outer" ||
		found.Results[1].Scope != "tail" {
		t.Fatalf("target scope names = %#v, want outer and tail", found.Results)
	}
}

func TestCAuthoritativeScopeNameDoesNotForwardAttachLaterDeclaration(t *testing.T) {
	t.Parallel()

	lines := []string{
		"#if FEATURE",
		"target();",
		"int later(void)",
		"{",
		"    return 0;",
		"}",
		"#endif",
	}
	backend := prepareLanguageBackend(newCLanguage(), lines)
	if got := scopeName(lines, 2, backend); got != "" {
		t.Fatalf("preprocessor reference attached to later scope %q", got)
	}
	resolver := backend.(findScopeResolverPreparer).prepareFindScopeResolver(lines)
	if resolver == nil || resolver.scopeName(2) != "" {
		t.Fatalf("prepared resolver attached line 2 to later declaration: %#v", resolver)
	}
}

func TestCFindClassifiesPrototypesDefinitionsAndReferences(t *testing.T) {
	t.Parallel()

	const source = `int same_function(void);
int same_function(void)
{
    return 1;
}
int caller(void)
{
    same_function();
    return same_function();
}
const char *same_function_text = "same_function";
/* same_function(); */
`
	root := t.TempDir()
	writeFile(t, root, "fixture.c", source)
	view := mustView(t, root)

	response, err := view.Find("same_function", Options{
		Include: IncludeBoth, Return: ReturnLocations, NoComments: true, NoStrings: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := resultLines(response.Results), []int{1, 2, 8, 9}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Find lines = %#v, want %#v", got, want)
	}
	if got, want := cHighLevelResultKinds(response.Results), []string{"def", "def", "ref", "ref"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Find kinds = %#v, want %#v", got, want)
	}

	partial, err := view.Find("function", Options{Include: IncludeBoth, Return: ReturnLocations})
	if err != nil {
		t.Fatal(err)
	}
	if len(partial.Results) != 0 {
		t.Fatalf("partial C identifier matched: %#v", partial.Results)
	}
}

func TestCPreparedBackendIsDefensiveImmutableAndConcurrent(t *testing.T) {
	first := []string{
		"int first(void)",
		"{",
		"    target();",
		"}",
	}
	prepared := prepareLanguageBackend(newCLanguage(), first)
	if got, want := cHighLevelDefinitionSymbols(prepared.sourceDefinitions(first)), []string{"first"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("prepared definitions = %#v, want %#v", got, want)
	}

	returned := prepared.sourceDefinitions(first)
	returned[0].symbol = "corrupted"
	returned[0].scopeEnd = 999
	returned = append(returned, sourceDefinition{symbol: "injected"})
	if returned[len(returned)-1].symbol != "injected" {
		t.Fatal("caller-side append did not retain injected definition")
	}
	refetched := prepared.sourceDefinitions(first)
	if got := cHighLevelDefinitionSymbols(refetched); !slices.Equal(got, []string{"first"}) ||
		refetched[0].scopeEnd != 4 {
		t.Fatalf("caller mutated cached definitions through returned slice: %#v", refetched)
	}

	resolverPreparer := prepared.(findScopeResolverPreparer)
	oldResolver := resolverPreparer.prepareFindScopeResolver(first)
	if oldResolver == nil || oldResolver.scopeName(3) != "first" {
		t.Fatalf("old prepared resolver = %#v, want scope first", oldResolver)
	}

	first[0] = "int mutated(void)"
	if got := cHighLevelDefinitionSymbols(prepared.sourceDefinitions(first)); !slices.Equal(got, []string{"mutated"}) {
		t.Fatalf("same-slice mutation retained stale definitions: %#v", got)
	}
	newResolver := resolverPreparer.prepareFindScopeResolver(first)
	if newResolver == nil {
		t.Fatal("mutated source produced a nil prepared resolver")
	}
	if got := newResolver.scopeName(3); got != "mutated" {
		t.Fatalf("mutated resolver scope = %q, want mutated", got)
	}
	if got := oldResolver.scopeName(3); got != "first" {
		t.Fatalf("old immutable resolver changed to %q", got)
	}

	second := []string{"int second(void) { return 2; }"}
	if got := cHighLevelDefinitionSymbols(prepared.sourceDefinitions(second)); !slices.Equal(got, []string{"second"}) {
		t.Fatalf("distinct source reused stale definitions: %#v", got)
	}
	if empty := prepared.(sourceBackendPreparer).prepareSource(nil); len(empty.sourceDefinitions(nil)) != 0 {
		t.Fatalf("empty prepared backend retained definitions: %#v", empty.sourceDefinitions(nil))
	}

	const workers = 16
	var wait sync.WaitGroup
	errors := make(chan error, workers)
	wait.Add(workers)
	for index := range workers {
		go func(index int) {
			defer wait.Done()
			name := fmt.Sprintf("worker_%d", index)
			lines := []string{
				"int " + name + "(void)",
				"{",
				"    target();",
				"}",
			}
			for range 20 {
				definitions := prepared.sourceDefinitions(lines)
				if len(definitions) != 1 || definitions[0].symbol != name {
					errors <- fmt.Errorf("definitions for %s = %#v", name, definitions)
					return
				}
				searchable := prepared.searchLines(lines, true, true)
				if !slices.Equal(searchable, lines) {
					errors <- fmt.Errorf("search lines for %s = %#v", name, searchable)
					return
				}
				resolver := prepared.(findScopeResolverPreparer).prepareFindScopeResolver(lines)
				if resolver == nil || resolver.scopeName(3) != name {
					errors <- fmt.Errorf("resolver scope for %s is stale", name)
					return
				}
			}
		}(index)
	}
	wait.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}

	var resolverWait sync.WaitGroup
	resolverWait.Add(workers)
	for range workers {
		go func() {
			defer resolverWait.Done()
			for range 100 {
				if got := oldResolver.scopeName(3); got != "first" {
					t.Errorf("concurrent immutable scope = %q, want first", got)
					return
				}
				start, end := oldResolver.navigationScope(3)
				if start != 1 || end != 4 || oldResolver.definitionCount(1, "first") != 1 {
					t.Errorf("concurrent resolver metadata = %d-%d", start, end)
					return
				}
			}
		}()
	}
	resolverWait.Wait()
}

func cHighLevelImplements[T any](value any) bool {
	_, ok := value.(T)
	return ok
}

func cHighLevelTestLines(source string) []string {
	return strings.Split(strings.TrimSuffix(source, "\n"), "\n")
}

func cHighLevelDefinitionSummaries(definitions []sourceDefinition) []cHighLevelDefinitionSummary {
	result := make([]cHighLevelDefinitionSummary, 0, len(definitions))
	for _, definition := range definitions {
		result = append(result, cHighLevelDefinitionSummaryOf(definition))
	}
	return result
}

func cHighLevelDefinitionSummaryOf(
	definition sourceDefinition,
) cHighLevelDefinitionSummary {
	return cHighLevelDefinitionSummary{
		symbol: definition.symbol, line: definition.line, column: definition.column,
		scopeStart: definition.scopeStart, scopeEnd: definition.scopeEnd,
		ownsScope: definition.ownsScope,
	}
}

func cHighLevelDefinitionSymbols(definitions []sourceDefinition) []string {
	result := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		result = append(result, definition.symbol)
	}
	return result
}

func cHighLevelResultSymbols(results []Result) []string {
	result := make([]string, 0, len(results))
	for _, item := range results {
		result = append(result, item.Symbol)
	}
	return result
}

func cHighLevelResultKinds(results []Result) []string {
	result := make([]string, 0, len(results))
	for _, item := range results {
		result = append(result, item.Kind)
	}
	return result
}

func cHighLevelResultLocations(results []Result) []string {
	result := make([]string, 0, len(results))
	for _, item := range results {
		result = append(result, resultLocation(item))
	}
	return result
}

func cHighLevelExpectedDefinition(
	t *testing.T,
	lines []string,
	symbol, fragment string,
	scopeStart, scopeEnd int,
	ownsScope bool,
) cHighLevelDefinitionSummary {
	t.Helper()
	line := cHighLevelLineContaining(t, lines, fragment)
	column := strings.Index(lines[line-1], symbol)
	if column < 0 {
		t.Fatalf("line %d containing %q does not contain symbol %q: %q",
			line, fragment, symbol, lines[line-1])
	}
	return cHighLevelDefinitionSummary{
		symbol: symbol, line: line, column: column + 1,
		scopeStart: scopeStart, scopeEnd: scopeEnd, ownsScope: ownsScope,
	}
}

func cHighLevelDefinitionNamed(
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
	t.Fatalf("definition %q missing from %#v", symbol, definitions)
	return sourceDefinition{}
}

func cHighLevelLineContaining(t *testing.T, lines []string, fragment string) int {
	t.Helper()
	for index, line := range lines {
		if strings.Contains(line, fragment) {
			return index + 1
		}
	}
	t.Fatalf("fixture does not contain %q", fragment)
	return 0
}

func cHighLevelInspectAtLine(
	t *testing.T,
	view *RepoView,
	path string,
	line int,
) InspectResponse {
	t.Helper()
	response, err := view.Inspect(
		path+":"+strconv.Itoa(line),
		Options{Include: IncludeScope, Return: ReturnScope},
	)
	if err != nil {
		t.Fatal(err)
	}
	return response
}
