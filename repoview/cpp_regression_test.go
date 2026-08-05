package repoview

import (
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestCPPDefinitionsCoverDeclaratorsLinkageTemplatesAndSpecialFunctions(t *testing.T) {
	t.Parallel()

	const source = `typedef int (*Callback)(double);
namespace library {
struct Forward;
template<class T> struct Box;
class Widget {
public:
    static int shared;
    int field, *pointer;
    int (*callback)(int);
    Widget();
    ~Widget();
    Widget& operator++();
    int operator[](unsigned index) const;
    explicit operator const char*() const;
    friend void swap(Widget&, Widget&);
};
enum Plain { first, second = 2 };
enum class Scoped : unsigned { alpha, beta };
}
library::Widget::Widget() = default;
library::Widget::~Widget() = default;
library::Widget& library::Widget::operator++() { return *this; }
int library::Widget::operator[](unsigned index) const { return field + index; }
template<class T> T convert(T value) { return value; }
extern "C" { int c_api(int); }
auto [left, right] = pair;
int caller() {
    using LocalAlias = int;
    struct Local { int member; };
    Widget local(first);
    return c_api(left);
}
`
	definitions := newCPPLanguage().sourceDefinitions(cppTestLines(source))
	want := []string{
		"Callback", "library", "Forward", "Box", "Widget", "shared", "field",
		"pointer", "callback", "Widget", "~Widget", "operator++", "operator[]",
		"operator const char*", "swap", "Plain", "first", "second", "Scoped",
		"alpha", "beta", "Widget", "~Widget", "operator++", "operator[]",
		"convert", "c_api", "left", "right", "caller", "LocalAlias", "Local",
		"member",
	}
	if got := cppDefinitionSymbols(definitions); !slices.Equal(got, want) {
		t.Fatalf("declarator definitions =\n%#v\nwant\n%#v", got, want)
	}
	for _, forbidden := range []string{"T", "index", "value", "local"} {
		if slices.Contains(cppDefinitionSymbols(definitions), forbidden) {
			t.Errorf("parameter/local %q became definition: %#v", forbidden, definitions)
		}
	}
	for _, definition := range definitions {
		cppAssertDefinitionCoordinate(t, cppTestLines(source), definition)
	}
}

func TestCPPMalformedRecoveryKeepsIndependentDeclarationsWithoutPhantoms(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		source string
		want   []string
	}{
		{
			name: "unclosed declarator",
			source: `int before();
int broken(
int after();
`,
			want: []string{"before", "after"},
		},
		{
			name: "bad template sibling",
			source: `template<class T
class Broken;
int recovered() { return 1; }
`,
			want: []string{"Broken", "recovered"},
		},
		{
			name: "ordinary string resynchronizes at newline",
			source: `const char *broken = "unterminated
int recovered() { return 1; }
`,
			want: []string{"recovered"},
		},
		{
			name: "unterminated block comment owns tail",
			source: `int before();
/* int hidden();
int hidden_tail();
`,
			want: []string{"before"},
		},
		{
			name: "unterminated raw string owns tail",
			source: `int before();
const char *raw = R"tag(int hidden();
int hidden_tail();
`,
			want: []string{"before"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			lines := cppTestLines(test.source)
			definitions := newCPPLanguage().sourceDefinitions(lines)
			if got := cppDefinitionSymbols(definitions); !slices.Equal(got, test.want) {
				t.Fatalf("definitions = %#v, want %#v", got, test.want)
			}
			for _, definition := range definitions {
				cppAssertDefinitionCoordinate(t, lines, definition)
			}
		})
	}
}

func TestCPPOperatorDefinitionsRemainSourceBackedAndBounded(t *testing.T) {
	t.Parallel()

	const source = `struct Iterator {
    Iterator& operator=(const Iterator& value) noexcept;
    Iterator& operator++() { return *this; }
    int& operator*() const;
	int* operator->() const;
	bool operator==(const Iterator&) const = default;
	explicit operator bool() const noexcept { return true; }
	void* operator new [](unsigned long);
	explicit operator decltype(auto)() const;
};
void adjacent();
`
	lines := cppTestLines(source)
	definitions := newCPPLanguage().sourceDefinitions(lines)
	want := []string{
		"Iterator", "operator=", "operator++", "operator*", "operator->",
		"operator==", "operator bool", "operator new []",
		"operator decltype(auto)", "adjacent",
	}
	if got := cppDefinitionSymbols(definitions); !slices.Equal(got, want) {
		t.Fatalf("operator definitions = %#v, want %#v", got, want)
	}
	for _, definition := range definitions {
		cppAssertDefinitionCoordinate(t, lines, definition)
		if strings.ContainsAny(definition.symbol, ";{}\r\n") {
			t.Errorf("operator symbol swallowed declarator/body: %#v", definition)
		}
	}
}

func TestCPPSpecialFunctionDefinitionsTreatCommentsAsTriviaAndRemainSourceBacked(t *testing.T) {
	t.Parallel()

	const source = `struct Trivia {
	~ /* destructor bridge */ Trivia();
	bool operator /* equality bridge */ ==(const Trivia&) const;
	void* operator new /* array bridge */ [](unsigned long);
	explicit operator bool /* punctuation: (;{} */ () const;
	explicit operator decltype /* decltype bridge */ (auto)() const;
};
long double operator /* literal bridge */ "" /* suffix bridge */ _tag(long double);
`
	lines := cppTestLines(source)
	definitions := newCPPLanguage().sourceDefinitions(lines)
	want := []string{
		"Trivia",
		"~ /* destructor bridge */ Trivia",
		"operator /* equality bridge */ ==",
		"operator new /* array bridge */ []",
		"operator bool /* punctuation: (;{} */",
		"operator decltype /* decltype bridge */ (auto)",
		`operator /* literal bridge */ "" /* suffix bridge */ _tag`,
	}
	if got := cppDefinitionSymbols(definitions); !slices.Equal(got, want) {
		t.Fatalf("comment-trivia operator definitions = %#v, want %#v", got, want)
	}
	for _, definition := range definitions {
		cppAssertDefinitionCoordinate(t, lines, definition)
	}
}

func TestCPPConversionOperatorDoesNotTruncateCoAwaitIdentifierPrefix(t *testing.T) {
	t.Parallel()

	const source = `struct co_awaiting {};
struct Awaitable {
	explicit operator co_awaiting() const;
};
`
	lines := cppTestLines(source)
	definitions := newCPPLanguage().sourceDefinitions(lines)
	want := []string{"co_awaiting", "Awaitable", "operator co_awaiting"}
	if got := cppDefinitionSymbols(definitions); !slices.Equal(got, want) {
		t.Fatalf("co_await prefix definitions = %#v, want %#v", got, want)
	}
	for _, definition := range definitions {
		cppAssertDefinitionCoordinate(t, lines, definition)
	}
}

func TestCPPDefinitionsRecoverNestedNamespaceTriviaLiteralOperatorsAndGuides(t *testing.T) {
	t.Parallel()

	const source = `#define \u{3B3}amma 1
#define \N{LATIN CAPITAL LETTER A}lpha 2
namespace outer :: inner { int spaced(); }
namespace abi::inline current { int versioned(); }
template<class T> struct Box {
    friend class Friend;
};
Box(int) -> Box<int>;
long double operator "" _km(long double);
`
	lines := cppTestLines(source)
	definitions := newCPPLanguage().sourceDefinitions(lines)
	want := []string{
		`\u{3B3}amma`, `\N{LATIN CAPITAL LETTER A}lpha`, "inner", "spaced",
		"current", "versioned", "Box", "Friend", "Box", `operator "" _km`,
	}
	if got := cppDefinitionSymbols(definitions); !slices.Equal(got, want) {
		t.Fatalf("edge definitions = %#v, want %#v", got, want)
	}
	for _, definition := range definitions {
		cppAssertDefinitionCoordinate(t, lines, definition)
	}
}

func TestCPPPreparedBackendRefreshesMutatedInputAndIsConcurrent(t *testing.T) {
	const first = `namespace one { int first(); }`
	const second = `namespace two { int second(); }`
	lines := cppTestLines(first)
	prepared := prepareLanguageBackend(newCPPLanguage(), lines)
	if got := cppDefinitionSymbols(prepared.sourceDefinitions(lines)); !slices.Equal(got, []string{"one", "first"}) {
		t.Fatalf("first definitions = %#v", got)
	}
	copy(lines, cppTestLines(second))
	if got := cppDefinitionSymbols(prepared.sourceDefinitions(lines)); !slices.Equal(got, []string{"two", "second"}) {
		t.Fatalf("mutated definitions = %#v", got)
	}

	stableLines := cppTestLines(first + "\n" + second)
	stable := prepareLanguageBackend(newCPPLanguage(), stableLines)
	want := cppDefinitionSymbols(stable.sourceDefinitions(stableLines))
	var wait sync.WaitGroup
	for range 16 {
		wait.Add(1)
		go func() {
			defer wait.Done()
			for range 100 {
				if got := cppDefinitionSymbols(stable.sourceDefinitions(stableLines)); !reflect.DeepEqual(got, want) {
					t.Errorf("concurrent definitions = %#v, want %#v", got, want)
					return
				}
				_, _ = stable.enclosingScope(stableLines, 1)
				_ = stable.searchLines(stableLines, true, true)
			}
		}()
	}
	wait.Wait()
}

func cppAssertDefinitionCoordinate(
	t *testing.T,
	lines []string,
	definition sourceDefinition,
) {
	t.Helper()
	if definition.line < 1 || definition.line > len(lines) || definition.column < 1 ||
		definition.scopeStart < 1 || definition.scopeEnd < definition.scopeStart ||
		definition.scopeEnd > len(lines) {
		t.Fatalf("definition outside source: %#v (lines=%d)", definition, len(lines))
	}
	line := lines[definition.line-1]
	start := definition.column - 1
	end := start + len(definition.symbol)
	if start >= 0 && end <= len(line) && line[start:end] == definition.symbol {
		return
	}
	source := strings.Join(lines, "\n")
	lineStarts := cLineStarts(source)
	physicalStart := lineStarts[definition.line-1] + definition.column - 1
	if physicalStart >= 0 && physicalStart < len(source) &&
		cppSourceIdentifier(definition.symbol) {
		logicalEnd := cppLogicalIdentifierEnd(source, physicalStart)
		if logicalEnd > physicalStart && logicalEnd <= len(source) &&
			cLogicalText(source, physicalStart, logicalEnd) == definition.symbol {
			return
		}
	}
	for _, trusted := range lexCPP(source).trustedDefinitions {
		if cDefinitionKey(trusted) == cDefinitionKey(definition) {
			return
		}
	}
	t.Fatalf("definition is not physically or logically source-backed: %#v in %q",
		definition, line)
}

func FuzzCPPBackendMaintainsCoordinateContracts(f *testing.F) {
	for _, source := range []string{
		"int main() { return 0; }\n",
		"namespace a::b { template<class T> class Box {}; }\n",
		"struct S { S(); ~S(); explicit operator bool() const; };\n",
		"auto [left, right] = pair;\n",
		"template<class T> concept C = requires(T value) { value.run(); };\n",
		"export module demo.core;\nimport std;\n",
		"export module demo /*bridge*/ . core : part;\n",
		"int tar\\\nget();\n",
		"const char *raw = R\"tag(} fake(); {)tag\"; int real();\n",
		"int broken(\nint recovered();\n",
		string([]byte{0xff, 0xfe, 0x00, '{', '}'}),
	} {
		f.Add(source)
	}
	f.Fuzz(func(t *testing.T, source string) {
		if len(source) > 64<<10 {
			t.Skip()
		}
		lines := cppTestLines(source)
		backend := prepareLanguageBackend(newCPPLanguage(), lines)
		definitions := backend.sourceDefinitions(lines)
		_, _, _ = backend.importRange(lines)
		_ = backend.ignoredSearchLines(lines, true, false)
		for _, options := range [][2]bool{
			{false, false}, {true, false}, {false, true}, {true, true},
		} {
			searchable := backend.searchLines(lines, options[0], options[1])
			if len(searchable) != len(lines) ||
				len(strings.Join(searchable, "\n")) != len(strings.Join(lines, "\n")) {
				t.Fatalf("search mask changed coordinates")
			}
		}
		_ = backend.cleanSource(source, true, false)
		for _, lineNo := range []int{1, len(lines)} {
			_, _ = backend.enclosingScope(lines, lineNo)
			if resolver, ok := backend.(navigationScopeResolver); ok {
				_, _ = resolver.navigationScope(lines, lineNo)
			}
		}
		for _, definition := range definitions {
			cppAssertDefinitionCoordinate(t, lines, definition)
		}
	})
}
