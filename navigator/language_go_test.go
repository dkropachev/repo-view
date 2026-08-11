package navigator

import (
	"go/parser"
	"go/token"
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestGoDefinitionMatcherCoversDeclarationForms(t *testing.T) {
	t.Parallel()
	backend := newGoLanguage()
	tests := []struct {
		name string
		line string
		want string
		ok   bool
	}{
		{name: "function", line: "func Run() {}", want: "Run", ok: true},
		{name: "method", line: "func (cache *Cache[K, V]) Lookup(key K) (V, bool) {", want: "Lookup", ok: true},
		{name: "unicode function", line: "func 函数() {}", want: "函数", ok: true},
		{name: "generic type", line: "type Set[T comparable] map[T]struct{}", want: "Set", ok: true},
		{name: "type alias", line: "type Strings = []string", want: "Strings", ok: true},
		{name: "constant", line: "const DefaultLimit = 50", want: "DefaultLimit", ok: true},
		{name: "variable", line: "var ErrClosed = errors.New(\"closed\")", want: "ErrClosed", ok: true},
		{name: "call", line: "Run()", ok: false},
		{name: "condition", line: "if ready() {", ok: false},
		{name: "function literal", line: "handler := func() {}", ok: false},
		{name: "comment", line: "// func Hidden() {}", ok: false},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, ok := backend.definitionSymbol(test.line)
			if got != test.want || ok != test.ok {
				t.Fatalf("definitionSymbol(%q) = %q, %v; want %q, %v", test.line, got, ok, test.want, test.ok)
			}
		})
	}
}

func TestGoOutlineParsesConcreteDeclarations(t *testing.T) {
	t.Parallel()
	const source = `package fixture

const (
	Alpha = iota
	Beta
)

var First, Second = 1, 2

type (
	ID string
	Pair[T comparable] struct {
		Value T
	}
	Handler func(T) error
)

type Alias = Pair[int]

func Transform[T any](
	value T,
) T {
	return value
}

func (pair *Pair[T]) ValueOr(
	fallback T,
) T {
	return pair.Value
}

func Δelta() {}
`
	assertParsesAsGo(t, source)

	root := t.TempDir()
	writeFile(t, root, "fixture.go", source)
	view := mustView(t, root)
	response, err := view.Outline("fixture.go", Options{Return: ReturnLine})
	if err != nil {
		t.Fatal(err)
	}

	wantSymbols := []string{
		"Alpha", "Beta", "First", "Second", "ID", "Pair", "Handler",
		"Alias", "Transform", "ValueOr", "Δelta",
	}
	wantLines := []int{4, 5, 8, 8, 11, 12, 15, 18, 20, 26, 32}
	gotSymbols := make([]string, 0, len(response.Results))
	gotLines := make([]int, 0, len(response.Results))
	for _, result := range response.Results {
		gotSymbols = append(gotSymbols, result.Symbol)
		gotLines = append(gotLines, result.Line)
		if result.Kind != "def" || result.Scope != result.Symbol || result.Language != "go" {
			t.Fatalf("malformed definition result: %#v", result)
		}
	}
	if !reflect.DeepEqual(gotSymbols, wantSymbols) {
		t.Fatalf("symbols = %#v, want %#v", gotSymbols, wantSymbols)
	}
	if !reflect.DeepEqual(gotLines, wantLines) {
		t.Fatalf("lines = %#v, want %#v", gotLines, wantLines)
	}
}

func TestGoFindClassifiesGroupedDeclarationsAsDefinitions(t *testing.T) {
	t.Parallel()
	const source = `package fixture

var First, Second = 1, 2

func use() int {
	return Second
}
`
	assertParsesAsGo(t, source)

	root := t.TempDir()
	writeFile(t, root, "fixture.go", source)
	view := mustView(t, root)
	response, err := view.Find("Second", Options{Include: IncludeDefs, Return: ReturnLocations})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].Line != 3 || response.Results[0].Kind != "def" {
		t.Fatalf("definition results = %#v", response.Results)
	}
}

func TestGoOutlineIgnoresDefinitionLikeNonDeclarations(t *testing.T) {
	t.Parallel()
	const source = `package fixture

var Raw = ` + "`" + `func Fake() {}
type Phantom struct{}` + "`" + `

/*
func Hidden() {}
*/
type Holder struct {
	Nested struct{}
}

func Real() {
	if ready() {
		callback := func() {}
		callback()
	}
}

const (
	_ = iota
	Visible
)
`
	assertParsesAsGo(t, source)

	root := t.TempDir()
	writeFile(t, root, "fixture.go", source)
	view := mustView(t, root)
	response, err := view.Outline("fixture.go", Options{Return: ReturnLocations})
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(response.Results))
	for _, result := range response.Results {
		got = append(got, result.Symbol)
	}
	want := []string{"Raw", "Holder", "Real", "Visible"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("symbols = %#v, want %#v", got, want)
	}

	fake, err := view.Find("Fake", Options{Include: IncludeDefs, Return: ReturnLocations})
	if err != nil {
		t.Fatal(err)
	}
	if len(fake.Results) != 0 {
		t.Fatalf("raw string text reported as definitions: %#v", fake.Results)
	}
}

func TestGoOutlineHandlesMultilineAndCommentSeparatedFunctions(t *testing.T) {
	t.Parallel()
	const source = `package fixture

type Receiver struct{}

func (
	receiver *Receiver,
) Multiline() {}

func /* separation */ 函数() {}
`
	assertParsesAsGo(t, source)

	root := t.TempDir()
	writeFile(t, root, "fixture.go", source)
	view := mustView(t, root)
	response, err := view.Outline("fixture.go", Options{Return: ReturnScope})
	if err != nil {
		t.Fatal(err)
	}
	gotSymbols := make([]string, 0, len(response.Results))
	gotLines := make([]int, 0, len(response.Results))
	for _, result := range response.Results {
		gotSymbols = append(gotSymbols, result.Symbol)
		gotLines = append(gotLines, result.Line)
	}
	if want := []string{"Receiver", "Multiline", "函数"}; !reflect.DeepEqual(gotSymbols, want) {
		t.Fatalf("symbols = %#v, want %#v", gotSymbols, want)
	}
	if want := []int{3, 7, 9}; !reflect.DeepEqual(gotLines, want) {
		t.Fatalf("lines = %#v, want %#v", gotLines, want)
	}
	if result := response.Results[1]; result.StartLine != 5 || result.EndLine != 7 {
		t.Fatalf("multiline method scope = %#v", result)
	}
}

func TestGoUnicodeNavigationUsesIdentifierBoundaries(t *testing.T) {
	t.Parallel()
	const source = `package fixture

func 函数() {}

func caller() {
	函数()
	_ = "函数数"
}
`
	assertParsesAsGo(t, source)

	root := t.TempDir()
	writeFile(t, root, "fixture.go", source)
	view := mustView(t, root)
	response, err := view.Find("函数", Options{
		Include:    IncludeBoth,
		Return:     ReturnLocations,
		NoComments: true,
		NoStrings:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := resultLines(response.Results), []int{3, 6}; !reflect.DeepEqual(got, want) {
		t.Fatalf("lines = %#v, want %#v", got, want)
	}

	partial, err := view.Find("数", Options{Include: IncludeBoth, Return: ReturnLocations, NoStrings: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(partial.Results) != 0 {
		t.Fatalf("partial Unicode identifier matched: %#v", partial.Results)
	}

	inspected, err := view.Inspect("fixture.go:6", Options{Include: IncludeScope, Return: ReturnScope})
	if err != nil {
		t.Fatal(err)
	}
	if inspected.Symbol != "函数" || len(inspected.Results) != 1 || inspected.Results[0].Scope != "caller" {
		t.Fatalf("inspect response = %#v", inspected)
	}
}

func TestGoUsesPhysicalLinesWhenSourceHasLineDirectives(t *testing.T) {
	t.Parallel()
	const source = `package fixture
//line generated.go:100
import "fmt"

func Directed() {
	fmt.Println("ok")
}

func caller() { Directed() }
`
	assertParsesAsGo(t, source)

	root := t.TempDir()
	writeFile(t, root, "fixture.go", source)
	view := mustView(t, root)
	outline, err := view.Outline("fixture.go", Options{Return: ReturnLocations})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := resultLines(outline.Results), []int{5, 9}; !reflect.DeepEqual(got, want) {
		t.Fatalf("outline lines = %#v, want %#v", got, want)
	}

	found, err := view.Find("Directed", Options{Include: IncludeBoth, Return: ReturnLocations})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := resultLines(found.Results), []int{5, 9}; !reflect.DeepEqual(got, want) {
		t.Fatalf("find lines = %#v, want %#v", got, want)
	}
	if found.Results[0].Kind != "def" || found.Results[1].Kind != "ref" {
		t.Fatalf("find kinds = %#v", found.Results)
	}

	inspected, err := view.Inspect("fixture.go:6", Options{Include: IncludeAll, Return: ReturnScope})
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range inspected.Results {
		if result.StartLine < 1 || result.EndLine > 9 {
			t.Fatalf("logical //line position leaked into result: %#v", result)
		}
	}
}

func TestGoFindSeparatesDefinitionsAndReferencesOnOneLine(t *testing.T) {
	t.Parallel()
	const source = `package fixture

type SameNode struct{ Next *SameNode }

func SameLine() { SameLine() }
`
	assertParsesAsGo(t, source)

	root := t.TempDir()
	writeFile(t, root, "fixture.go", source)
	view := mustView(t, root)
	for _, symbol := range []string{"SameNode", "SameLine"} {
		response, err := view.Find(symbol, Options{Include: IncludeBoth, Return: ReturnLocations})
		if err != nil {
			t.Fatal(err)
		}
		if len(response.Results) != 2 || response.Results[0].Kind != "def" || response.Results[1].Kind != "ref" {
			t.Fatalf("%s results = %#v", symbol, response.Results)
		}
		if response.Results[0].Line != response.Results[1].Line {
			t.Fatalf("%s results are not on one line: %#v", symbol, response.Results)
		}
	}
}

func TestGoFindUsesColumnsToSeparateAdjacentTopLevelScopes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		source    string
		symbol    string
		wantLine  int
		wantScope string
	}{
		{
			name: "same-line sibling function",
			source: `package demo
func first() { inside() }; func second() { target() }
`,
			symbol:    "target",
			wantLine:  2,
			wantScope: "second",
		},
		{
			name: "same-line top-level declaration after function",
			source: `package demo
func first() { inside() }; var later = outside()
`,
			symbol:   "outside",
			wantLine: 2,
		},
		{
			name: "top-level declaration after multiline function",
			source: `package demo
func first() {
	inside()
}; var later = beyond()
`,
			symbol:   "beyond",
			wantLine: 4,
		},
		{
			name: "top-level declaration after multiline function literal",
			source: `package demo
var holder = func() {
	inside()
}; var later = afterLiteral()
`,
			symbol:   "afterLiteral",
			wantLine: 4,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			assertParsesAsGo(t, testCase.source)
			root := t.TempDir()
			writeFile(t, root, "fixture.go", testCase.source)

			response, err := mustView(t, root).Find(testCase.symbol, Options{
				Include: IncludeRefs,
				Return:  ReturnScope,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(response.Results) != 1 {
				t.Fatalf("results = %#v", response.Results)
			}
			result := response.Results[0]
			if result.Line != testCase.wantLine ||
				result.StartLine != testCase.wantLine ||
				result.EndLine != testCase.wantLine ||
				result.Scope != testCase.wantScope {
				t.Fatalf(
					"result = %#v, want line/range %d and scope %q",
					result, testCase.wantLine, testCase.wantScope,
				)
			}
		})
	}
}

func TestGoInspectPrefersCodeOverLiteralsAndGenericReceivers(t *testing.T) {
	t.Parallel()
	const source = `package fixture

import (
	"fmt"
	"slices"
)

func caller() {
	_ = "Wrong()"; fmt.Println("ok")
	_ = slices.Clone[[]int](nil)
	_ = client.Transport.RoundTrip(nil)
	_ = selectorValue.MiddleField.FinalField
	_ = client.
		Transport.
		RoundTrip(nil)
}
`
	assertParsesAsGo(t, source)

	root := t.TempDir()
	writeFile(t, root, "fixture.go", source)
	view := mustView(t, root)
	tests := []struct {
		location string
		want     string
	}{
		{location: "fixture.go:9", want: "Println"},
		{location: "fixture.go:10", want: "Clone"},
		{location: "fixture.go:11", want: "RoundTrip"},
		{location: "fixture.go:12", want: "FinalField"},
		{location: "fixture.go:13", want: "client"},
		{location: "fixture.go:14", want: "Transport"},
		{location: "fixture.go:15", want: "RoundTrip"},
	}
	for _, test := range tests {
		response, err := view.Inspect(test.location, Options{Include: IncludeScope, Return: ReturnScope})
		if err != nil {
			t.Fatal(err)
		}
		if response.Symbol != test.want {
			t.Fatalf("Inspect(%q) symbol = %q, want %q", test.location, response.Symbol, test.want)
		}
	}
}

func TestGoScopeUsesSyntaxInsteadOfBracesInLiteralsAndComments(t *testing.T) {
	t.Parallel()
	const source = `package fixture

func Render() string {
	raw := ` + "`" + `first
}
last // still literal` + "`" + `
	/* a misleading closing brace: }
	   and an opening brace: { */
	if len(raw) > 0 {
		values := map[string]string{"brace": "}"}
		_ = values
	}
	return raw
}

func Next() {}
`
	assertParsesAsGo(t, source)
	lines := strings.Split(strings.TrimSuffix(source, "\n"), "\n")
	lineNo := lineContaining(t, lines, "return raw")
	start, end := newGoLanguage().enclosingScope(lines, lineNo)
	if start != 3 || end != 14 {
		t.Fatalf("scope = %d-%d, want 3-14", start, end)
	}
}

func TestGoImportsCoverConcreteDeclarationForms(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		source    string
		wantStart int
		wantEnd   int
	}{
		{
			name: "multiple single declarations without required whitespace",
			source: `package fixture
import"fmt"
import alias "os"
`,
			wantStart: 2,
			wantEnd:   3,
		},
		{
			name: "grouped aliases",
			source: `package fixture
import(
	_ "embed"
	dot "strings"
)
`,
			wantStart: 2,
			wantEnd:   5,
		},
		{
			name: "cgo preamble belongs to import evidence",
			source: `package fixture
/*
#include <stdlib.h>
*/
import "C"
`,
			wantStart: 2,
			wantEnd:   5,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assertParsesAsGo(t, test.source)
			lines := strings.Split(strings.TrimSuffix(test.source, "\n"), "\n")
			start, end, ok := newGoLanguage().importRange(lines)
			if !ok || start != test.wantStart || end != test.wantEnd {
				t.Fatalf("imports = %d-%d, %v; want %d-%d, true", start, end, ok, test.wantStart, test.wantEnd)
			}
		})
	}
}

func TestGoCommentCleaningPreservesStringAndRuneLiterals(t *testing.T) {
	t.Parallel()
	const source = `package fixture

var/* separation */ Global = 1

func literals() {
	_ = ` + "`" + `https://example.test/a/*literal*/b//tail

literal trailing spaces  ` + "`" + `
	_ = "https://example.test//path"
	_ = '/'
	value := 1 /* explanation */
	_ = value // line-note
}
`
	assertParsesAsGo(t, source)
	cleaned := newGoLanguage().cleanSource(source, true, false)
	assertParsesAsGo(t, cleaned)
	for _, literal := range []string{
		"https://example.test/a/*literal*/b//tail",
		"tail\n\nliteral trailing spaces  `",
		"https://example.test//path",
		"'/'",
	} {
		if !strings.Contains(cleaned, literal) {
			t.Fatalf("cleaned source dropped literal %q:\n%s", literal, cleaned)
		}
	}
	for _, comment := range []string{"separation", "explanation", "line-note"} {
		if strings.Contains(cleaned, comment) {
			t.Fatalf("cleaned source retained comment %q:\n%s", comment, cleaned)
		}
	}
}

func TestGoFindIgnoresLexicalCommentsAndStrings(t *testing.T) {
	t.Parallel()
	const source = `package fixture

func target() {}

func caller() {
	target()
	_ = "target"
	_ = ` + "`" + `first target
second target` + "`" + `
	/* first target
	second target */
	// target
	_ = "target on a mixed line"; target()
}
`
	assertParsesAsGo(t, source)

	root := t.TempDir()
	writeFile(t, root, "fixture.go", source)
	view := mustView(t, root)
	response, err := view.Find("target", Options{
		Include:    IncludeRefs,
		Return:     ReturnLocations,
		NoComments: true,
		NoStrings:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []int{6, 13}
	if got := resultLines(response.Results); !reflect.DeepEqual(got, want) {
		t.Fatalf("reference lines = %#v, want %#v; results = %#v", got, want, response.Results)
	}
}

func TestGoDefinitionsIncludeLocalDeclarationsAndInterfaceMethods(t *testing.T) {
	t.Parallel()
	const source = `package fixture

func outer() {
	const LocalConst = 1
	var LocalVar = LocalConst
	type Local struct{}
	_, _ = LocalVar, Local{}
}

type Worker interface {
	Work() error
}

type implementation struct{}

func (implementation) Work() error { return nil }

func _() {}
`
	assertParsesAsGo(t, source)

	root := t.TempDir()
	writeFile(t, root, "fixture.go", source)
	view := mustView(t, root)
	outline, err := view.Outline("fixture.go", Options{Return: ReturnLocations})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{
		"outer", "LocalConst", "LocalVar", "Local", "Worker", "Work",
		"implementation", "Work",
	}
	got := make([]string, 0, len(outline.Results))
	for _, result := range outline.Results {
		got = append(got, result.Symbol)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("symbols = %#v, want %#v", got, want)
	}

	methods, err := view.Find("Work", Options{Include: IncludeDefs, Return: ReturnLocations})
	if err != nil {
		t.Fatal(err)
	}
	if len(methods.Results) != 2 {
		t.Fatalf("method definitions = %#v", methods.Results)
	}
	local, err := view.Find("Local", Options{Include: IncludeDefs, Return: ReturnLocations})
	if err != nil {
		t.Fatal(err)
	}
	if len(local.Results) != 1 || local.Results[0].Line != 6 {
		t.Fatalf("local type definitions = %#v", local.Results)
	}
}

func TestGoImportsIgnoreImportTextInCommentsAndRawStrings(t *testing.T) {
	t.Parallel()
	const source = `package fixture

const raw = ` + "`" + `
import "not/a/real/import"
` + "`" + `

/*
import "also/not/an/import"
*/
func caller() { _ = raw }
`
	assertParsesAsGo(t, source)
	lines := strings.Split(strings.TrimSuffix(source, "\n"), "\n")
	if start, end, ok := newGoLanguage().importRange(lines); ok {
		t.Fatalf("imports = %d-%d, true; want no imports", start, end)
	}

	root := t.TempDir()
	writeFile(t, root, "fixture.go", source)
	view := mustView(t, root)
	response, err := view.Inspect("fixture.go:10", Options{Include: IncludeImports, Return: ReturnScope})
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range response.Results {
		if result.Kind == "imports" {
			t.Fatalf("fake import evidence = %#v", response.Results)
		}
	}
}

func TestGoImportFallbackMasksCommentsAndStrings(t *testing.T) {
	t.Parallel()
	const fakeSource = `package fixture
func Broken(
var raw = ` + "`" + `
import "not/a/real/import"
` + "`" + `
/*
import "also/not/an/import"
*/
`
	fakeLines := strings.Split(strings.TrimSuffix(fakeSource, "\n"), "\n")
	if start, end, ok := newGoLanguage().importRange(fakeLines); ok {
		t.Fatalf("fallback imports = %d-%d, true; want no imports", start, end)
	}

	const realSource = `package fixture
func Broken(
import"fmt"
`
	realLines := strings.Split(strings.TrimSuffix(realSource, "\n"), "\n")
	start, end, ok := newGoLanguage().importRange(realLines)
	if !ok || start != 3 || end != 3 {
		t.Fatalf("fallback imports = %d-%d, %v; want 3-3, true", start, end, ok)
	}

	const openGroupSource = `package fixture
func Broken(
import (
	"fmt"
`
	openGroupLines := strings.Split(strings.TrimSuffix(openGroupSource, "\n"), "\n")
	start, end, ok = newGoLanguage().importRange(openGroupLines)
	if !ok || start != 3 || end != 4 {
		t.Fatalf("open-group fallback imports = %d-%d, %v; want 3-4, true", start, end, ok)
	}

	groupForms := []struct {
		name    string
		source  string
		wantEnd int
	}{
		{
			name: "tab before parenthesis",
			source: `package fixture
func Broken(
import	(
	"fmt"
)
`,
			wantEnd: 5,
		},
		{
			name: "multiple spaces before parenthesis",
			source: `package fixture
func Broken(
import  (
	"fmt"
)
`,
			wantEnd: 5,
		},
		{
			name: "comment before parenthesis",
			source: `package fixture
func Broken(
import /* note */ (
	"fmt"
)
`,
			wantEnd: 5,
		},
		{
			name: "parenthesis on following line",
			source: `package fixture
func Broken(
import
(
	"fmt"
)
`,
			wantEnd: 6,
		},
	}
	for _, form := range groupForms {
		formLines := strings.Split(strings.TrimSuffix(form.source, "\n"), "\n")
		start, end, ok = newGoLanguage().importRange(formLines)
		if !ok || start != 3 || end != form.wantEnd {
			t.Fatalf(
				"%s fallback imports = %d-%d, %v; want 3-%d, true",
				form.name,
				start,
				end,
				ok,
				form.wantEnd,
			)
		}
	}

	const partialASTSource = `package fixture
import "fmt"
func Broken(
import "os"
`
	partialASTLines := strings.Split(strings.TrimSuffix(partialASTSource, "\n"), "\n")
	start, end, ok = newGoLanguage().importRange(partialASTLines)
	if !ok || start != 2 || end != 4 {
		t.Fatalf("merged AST/fallback imports = %d-%d, %v; want 2-4, true", start, end, ok)
	}

	const brokenCgoSource = `package fixture
func Broken(
/*
#include <stdlib.h>
*/
import "C"
`
	brokenCgoLines := strings.Split(strings.TrimSuffix(brokenCgoSource, "\n"), "\n")
	start, end, ok = newGoLanguage().importRange(brokenCgoLines)
	if !ok || start != 3 || end != 6 {
		t.Fatalf("fallback cgo imports = %d-%d, %v; want 3-6, true", start, end, ok)
	}

	const separatedCommentSource = `package fixture
func Broken(
/* unrelated */

import "fmt"
`
	separatedLines := strings.Split(strings.TrimSuffix(separatedCommentSource, "\n"), "\n")
	start, end, ok = newGoLanguage().importRange(separatedLines)
	if !ok || start != 5 || end != 5 {
		t.Fatalf("blank-separated fallback imports = %d-%d, %v; want 5-5, true", start, end, ok)
	}

	const trailingCommentSource = `package fixture
func Broken(
value // unrelated
import "fmt"
`
	trailingLines := strings.Split(strings.TrimSuffix(trailingCommentSource, "\n"), "\n")
	start, end, ok = newGoLanguage().importRange(trailingLines)
	if !ok || start != 4 || end != 4 {
		t.Fatalf("trailing-comment fallback imports = %d-%d, %v; want 4-4, true", start, end, ok)
	}
}

func TestGoContextCleaningStartsBeforeMultilineComment(t *testing.T) {
	t.Parallel()
	const source = `package fixture

/*
secret one
secret two
*/
func ContextTarget() {}
`
	assertParsesAsGo(t, source)

	root := t.TempDir()
	writeFile(t, root, "fixture.go", source)
	view := mustView(t, root)
	response, err := view.Find("ContextTarget", Options{
		Include:      IncludeDefs,
		Return:       ReturnContext,
		Context:      2,
		DropComments: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 {
		t.Fatalf("results = %#v", response.Results)
	}
	code := response.Results[0].Code
	if strings.Contains(code, "secret") || strings.Contains(code, "*/") ||
		!strings.Contains(code, "func ContextTarget()") {
		t.Fatalf("cleaned context = %q", code)
	}
}

func TestGoDefinitionsStayInPhysicalFileOrder(t *testing.T) {
	t.Parallel()
	const source = `package fixture

var (
	First = func() int {
		const Nested = 1
		return Nested
	}()
	Later = 2
)
`
	assertParsesAsGo(t, source)

	root := t.TempDir()
	writeFile(t, root, "fixture.go", source)
	view := mustView(t, root)
	outline, err := view.Outline("fixture.go", Options{Return: ReturnLocations})
	if err != nil {
		t.Fatal(err)
	}
	gotSymbols := make([]string, 0, len(outline.Results))
	for _, result := range outline.Results {
		gotSymbols = append(gotSymbols, result.Symbol)
	}
	if want := []string{"First", "Nested", "Later"}; !reflect.DeepEqual(gotSymbols, want) {
		t.Fatalf("symbols = %#v, want %#v", gotSymbols, want)
	}
	if got, want := resultLines(outline.Results), []int{4, 5, 8}; !reflect.DeepEqual(got, want) {
		t.Fatalf("lines = %#v, want %#v", got, want)
	}
}

func TestGoDefinitionsOnOneLineStayInLexicalOrder(t *testing.T) {
	t.Parallel()
	const source = `package fixture
var ( First = func() int { const Nested = 1; return Nested }(); Later = 2 )
`
	assertParsesAsGo(t, source)
	lines := strings.Split(strings.TrimSuffix(source, "\n"), "\n")
	definitions := newGoLanguage().sourceDefinitions(lines)
	got := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		got = append(got, definition.symbol)
	}
	if want := []string{"First", "Nested", "Later"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("symbols = %#v, want %#v", got, want)
	}
}

func TestGoParenthesizedInterfacesExposeMethods(t *testing.T) {
	t.Parallel()
	const source = `package fixture

type Direct (interface {
	DirectMethod() error
})

type Alias = (interface {
	AliasMethod()
})
`
	assertParsesAsGo(t, source)

	root := t.TempDir()
	writeFile(t, root, "fixture.go", source)
	view := mustView(t, root)
	outline, err := view.Outline("fixture.go", Options{Return: ReturnLocations})
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(outline.Results))
	for _, result := range outline.Results {
		got = append(got, result.Symbol)
	}
	if want := []string{"Direct", "DirectMethod", "Alias", "AliasMethod"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("symbols = %#v, want %#v", got, want)
	}
}

func TestGoInterfaceMethodsAreDefinitionsWhereverInterfacesOccur(t *testing.T) {
	t.Parallel()
	const source = `package fixture

type Box[T interface {
	Accept(T) error
}] struct {
	Value T
}

func constrained[T interface {
	Transform(T) T
}](value T) T {
	return value
}

var service interface {
	Serve() error
}

type Outer interface {
	interface {
		Nested()
	}
}
`
	assertParsesAsGo(t, source)

	root := t.TempDir()
	writeFile(t, root, "fixture.go", source)
	view := mustView(t, root)
	outline, err := view.Outline("fixture.go", Options{Return: ReturnLocations})
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(outline.Results))
	for _, result := range outline.Results {
		got = append(got, result.Symbol)
	}
	want := []string{
		"Box", "Accept", "constrained", "Transform", "service", "Serve", "Outer", "Nested",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("symbols = %#v, want %#v", got, want)
	}
	for _, symbol := range []string{"Accept", "Transform", "Serve", "Nested"} {
		response, findErr := view.Find(symbol, Options{
			Include: IncludeDefs,
			Return:  ReturnLocations,
		})
		if findErr != nil {
			t.Fatal(findErr)
		}
		if len(response.Results) != 1 || response.Results[0].Kind != "def" {
			t.Fatalf("definitions for %q = %#v", symbol, response.Results)
		}
	}
}

func TestGoCommentCleaningPreservesRawWhitespaceAfterComment(t *testing.T) {
	t.Parallel()
	const source = `package fixture

func raw() string {
	return /* remove */ ` + "`" + `first line has two spaces` + "  \n" + `second` + "`" + `
}
`
	assertParsesAsGo(t, source)
	cleaned := newGoLanguage().cleanSource(source, true, false)
	assertParsesAsGo(t, cleaned)
	if strings.Contains(cleaned, "remove") ||
		!strings.Contains(cleaned, "first line has two spaces  \nsecond") {
		t.Fatalf("cleaned source changed raw literal data:\n%s", cleaned)
	}
}

func TestGoAnonymousCompositeProvidesNamedScope(t *testing.T) {
	t.Parallel()
	const source = `package fixture

var cfg = struct {
	Nested struct {
		Value int
	}
	After int
}{}
`
	assertParsesAsGo(t, source)

	root := t.TempDir()
	writeFile(t, root, "fixture.go", source)
	view := mustView(t, root)
	response, err := view.Inspect("fixture.go:7", Options{Include: IncludeScope, Return: ReturnScope})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 {
		t.Fatalf("results = %#v", response.Results)
	}
	result := response.Results[0]
	if result.Scope != "cfg" || result.StartLine != 3 || result.EndLine != 8 {
		t.Fatalf("composite scope = %#v", result)
	}
}

func TestGoTypedAnonymousCompositeProvidesNamedScope(t *testing.T) {
	t.Parallel()
	const source = `package fixture

var existing struct {
	Nested struct {
		Value int
	}
	After int
}

var cfg struct {
	Nested struct {
		Value int
	}
	After int
} = existing
`
	assertParsesAsGo(t, source)

	root := t.TempDir()
	writeFile(t, root, "fixture.go", source)
	view := mustView(t, root)
	response, err := view.Inspect("fixture.go:14", Options{Include: IncludeScope, Return: ReturnScope})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 {
		t.Fatalf("results = %#v", response.Results)
	}
	result := response.Results[0]
	if result.Scope != "cfg" || result.StartLine != 10 || result.EndLine != 15 {
		t.Fatalf("typed composite scope = %#v", result)
	}
}

func TestGoWrappedAnonymousTypesProvideNamedScopes(t *testing.T) {
	t.Parallel()
	const source = `package fixture

type Box[T any] struct{}

var pointer *struct {
	PointerField int
}

var slice []struct {
	SliceField int
}

var array [2]struct {
	ArrayField int
}

var mapping map[string]struct {
	MapField int
}

var channel chan struct {
	ChannelField int
}

var generic Box[struct {
	GenericField int
}]

var callback func() struct {
	ReturnField int
}

var service *interface {
	Serve() error
} // service type
`
	assertParsesAsGo(t, source)

	lines := strings.Split(strings.TrimSuffix(source, "\n"), "\n")
	root := t.TempDir()
	writeFile(t, root, "fixture.go", source)
	view := mustView(t, root)
	tests := []struct {
		symbol string
		field  string
		start  string
		end    string
	}{
		{symbol: "pointer", field: "PointerField", start: "var pointer", end: "PointerField int"},
		{symbol: "slice", field: "SliceField", start: "var slice", end: "SliceField int"},
		{symbol: "array", field: "ArrayField", start: "var array", end: "ArrayField int"},
		{symbol: "mapping", field: "MapField", start: "var mapping", end: "MapField int"},
		{symbol: "channel", field: "ChannelField", start: "var channel", end: "ChannelField int"},
		{symbol: "generic", field: "GenericField", start: "var generic", end: "}]"},
		{symbol: "callback", field: "ReturnField", start: "var callback", end: "ReturnField int"},
		{symbol: "service", field: "service type", start: "var service", end: "Serve() error"},
	}
	for _, test := range tests {
		t.Run(test.symbol, func(t *testing.T) {
			t.Parallel()
			lineNo := lineContaining(t, lines, test.field)
			response, err := view.Inspect(
				"fixture.go:"+strconv.Itoa(lineNo),
				Options{Include: IncludeScope, Return: ReturnScope},
			)
			if err != nil {
				t.Fatal(err)
			}
			if len(response.Results) != 1 {
				t.Fatalf("results = %#v", response.Results)
			}
			result := response.Results[0]
			wantStart := lineContaining(t, lines, test.start)
			wantEnd := lineContaining(t, lines, test.end) + 1
			if test.end == "}]" {
				wantEnd = lineContaining(t, lines, "}]")
			}
			if result.Scope != test.symbol || result.StartLine != wantStart || result.EndLine != wantEnd {
				t.Fatalf("scope = %#v; want %q at %d-%d", result, test.symbol, wantStart, wantEnd)
			}
		})
	}
}

func TestGoNestedScopedValueExpressionsProvideNamedScopes(t *testing.T) {
	t.Parallel()
	const source = `package fixture

func wrap[T any](value T) T { return value }

var allocated = new(struct {
	AllocatedField int
})

var wrapped = wrap(struct {
	WrappedField int
}{})

var converted = (*struct {
	ConvertedField int
})(nil)
`
	assertParsesAsGo(t, source)

	lines := strings.Split(strings.TrimSuffix(source, "\n"), "\n")
	root := t.TempDir()
	writeFile(t, root, "fixture.go", source)
	view := mustView(t, root)
	tests := []struct {
		symbol string
		field  string
		start  string
		end    string
	}{
		{symbol: "allocated", field: "AllocatedField", start: "var allocated", end: "})"},
		{symbol: "wrapped", field: "WrappedField", start: "var wrapped", end: "}{})"},
		{symbol: "converted", field: "ConvertedField", start: "var converted", end: "})(nil)"},
	}
	for _, test := range tests {
		t.Run(test.symbol, func(t *testing.T) {
			t.Parallel()
			lineNo := lineContaining(t, lines, test.field)
			response, err := view.Inspect(
				"fixture.go:"+strconv.Itoa(lineNo),
				Options{Include: IncludeScope, Return: ReturnScope},
			)
			if err != nil {
				t.Fatal(err)
			}
			if len(response.Results) != 1 {
				t.Fatalf("results = %#v", response.Results)
			}
			result := response.Results[0]
			wantStart := lineContaining(t, lines, test.start)
			wantEnd := lineContaining(t, lines, test.end)
			if result.Scope != test.symbol || result.StartLine != wantStart || result.EndLine != wantEnd {
				t.Fatalf("scope = %#v; want %q at %d-%d", result, test.symbol, wantStart, wantEnd)
			}
		})
	}
}

func TestGoLexicalMaskingHandlesCRLF(t *testing.T) {
	t.Parallel()
	source := strings.Join([]string{
		"package fixture",
		"func caller() {",
		"\t_ = `first target",
		"second target`",
		"\t/* first target",
		"\tsecond target */",
		"\t// third target",
		"\ttarget()",
		"}",
		"",
	}, "\r\n")
	assertParsesAsGo(t, source)
	lines := strings.Split(strings.TrimSuffix(source, "\n"), "\n")
	searchable := newGoLanguage().searchLines(lines, true, true)
	found := make([]int, 0)
	for idx, line := range searchable {
		if countSymbolOccurrences(line, "target") > 0 {
			found = append(found, idx+1)
		}
	}
	if want := []int{8}; !reflect.DeepEqual(found, want) {
		t.Fatalf("search lines = %#v, want %#v; masked = %#v", found, want, searchable)
	}
	cleaned := newGoLanguage().cleanSource(source, true, false)
	if strings.Contains(cleaned, "second target */") ||
		!strings.Contains(cleaned, "first target\r\nsecond target`") {
		t.Fatalf("CRLF cleaning corrupted source: %q", cleaned)
	}
}

func TestGoLexicalMaskingHandlesPreservedCommentCR(t *testing.T) {
	t.Parallel()
	const source = "package fixture\nfunc caller() { /* *\r/ still comment */target() }\n"
	assertParsesAsGo(t, source)
	lines := strings.Split(strings.TrimSuffix(source, "\n"), "\n")
	searchable := newGoLanguage().searchLines(lines, true, true)
	if got := countSymbolOccurrences(searchable[1], "target"); got != 1 {
		t.Fatalf("target occurrences = %d, want 1; masked line = %q", got, searchable[1])
	}
	cleaned := newGoLanguage().cleanSource(source, true, false)
	assertParsesAsGo(t, cleaned)
	if !strings.Contains(cleaned, "target()") {
		t.Fatalf("cleaned source masked code after comment: %q", cleaned)
	}
}

func TestGoInspectSkipsLanguageKeywords(t *testing.T) {
	t.Parallel()
	const source = `package fixture

func keywords(values []int) map[string]int {
	for range values {
		break
	}
	return map[string]int{}
}
`
	assertParsesAsGo(t, source)

	root := t.TempDir()
	writeFile(t, root, "fixture.go", source)
	view := mustView(t, root)
	tests := []struct {
		location string
		want     string
	}{
		{location: "fixture.go:4", want: "values"},
		{location: "fixture.go:7", want: "string"},
	}
	for _, test := range tests {
		response, err := view.Inspect(test.location, Options{Include: IncludeScope, Return: ReturnScope})
		if err != nil {
			t.Fatal(err)
		}
		if response.Symbol != test.want {
			t.Fatalf("Inspect(%q) symbol = %q, want %q", test.location, response.Symbol, test.want)
		}
	}
}

func TestGoBackendChangesPreserveNonGoCallableIdentifiers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		path     string
		source   string
		location string
		want     string
	}{
		{
			name:     "python map",
			path:     "fixture.py",
			source:   "def caller():\n    return map(values)\n",
			location: "fixture.py:2",
			want:     "map",
		},
		{
			name:     "javascript select",
			path:     "fixture.js",
			source:   "function caller() {\n  return select();\n}\n",
			location: "fixture.js:2",
			want:     "select",
		},
		{
			name:     "rust map",
			path:     "fixture.rs",
			source:   "fn caller() {\n    map();\n}\n",
			location: "fixture.rs:2",
			want:     "map",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeFile(t, root, test.path, test.source)
			view := mustView(t, root)
			response, err := view.Inspect(test.location, Options{
				Include: IncludeScope,
				Return:  ReturnScope,
			})
			if err != nil {
				t.Fatal(err)
			}
			if response.Symbol != test.want {
				t.Fatalf("symbol = %q, want %q", response.Symbol, test.want)
			}
		})
	}
}

func TestGoIncompleteSourceMergesRecoveredAndFallbackDefinitions(t *testing.T) {
	t.Parallel()
	const source = `package fixture

func Good() {}
func Broken(
func Later() {}

var text = ` + "`" + `func NotCode() {}` + "`" + `
`
	lines := strings.Split(strings.TrimSuffix(source, "\n"), "\n")
	definitions := newGoLanguage().sourceDefinitions(lines)
	got := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		got = append(got, definition.symbol)
	}
	if !containsAll(got, "Good", "Broken", "Later", "text") {
		t.Fatalf("definitions = %#v", got)
	}
	for _, symbol := range got {
		if symbol == "NotCode" {
			t.Fatalf("raw string text became a fallback definition: %#v", got)
		}
	}
}

func FuzzGoBackendHandlesIncompleteSource(f *testing.F) {
	f.Add("package sample\nfunc Ready() {}\n")
	f.Add("package sample\nfunc InProgress() {\n\tif true {\n")
	f.Add("package sample\nvar raw = `unterminated\n}")
	f.Add("package sample\n//line generated.go:9000\nfunc Directed() {}")
	f.Fuzz(func(t *testing.T, source string) {
		backend := newGoLanguage()
		lines := strings.Split(source, "\n")
		_ = backend.sourceDefinitions(lines)
		_, _, _ = backend.importRange(lines)
		searchable := backend.searchLines(lines, true, true)
		if len(searchable) != len(lines) {
			t.Fatalf("search line count = %d, want %d", len(searchable), len(lines))
		}
		cleaned := backend.cleanSourceLines(lines, true, false)
		if len(cleaned) != len(lines) {
			t.Fatalf("cleaned line count = %d, want %d", len(cleaned), len(lines))
		}
		_, _ = backend.enclosingScope(lines, 1)
		_, _ = backend.enclosingScope(lines, len(lines))
	})
}

func assertParsesAsGo(t *testing.T, source string) {
	t.Helper()
	if _, err := parser.ParseFile(token.NewFileSet(), "fixture.go", source, parser.AllErrors); err != nil {
		t.Fatalf("fixture is not valid Go: %v\n%s", err, source)
	}
}

func lineContaining(t *testing.T, lines []string, fragment string) int {
	t.Helper()
	for idx, line := range lines {
		if strings.Contains(line, fragment) {
			return idx + 1
		}
	}
	t.Fatalf("fixture does not contain %q", fragment)
	return 0
}

func resultLines(results []Result) []int {
	lines := make([]int, 0, len(results))
	for _, result := range results {
		lines = append(lines, result.Line)
	}
	return lines
}

func containsAll(symbols []string, wanted ...string) bool {
	seen := make(map[string]bool, len(symbols))
	for _, symbol := range symbols {
		seen[symbol] = true
	}
	for _, symbol := range wanted {
		if !seen[symbol] {
			return false
		}
	}
	return true
}
