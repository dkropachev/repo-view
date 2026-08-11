package navigator

import (
	"reflect"
	"strings"
	"testing"
)

func TestCPPTreeAnalysisExtractsModernDefinitionsScopesAndIncludes(t *testing.T) {
	t.Parallel()

	const source = `#include <vector>
/** namespace docs. */
namespace outer::inner {
namespace alias = outer;
using Count = unsigned long;
template<class T>
concept Sized = requires(T value) { value.size(); };
/** class docs. */
template<class T>
class Box {
public:
    Box() = default;
    ~Box() = default;
    Box& operator=(const Box&) = delete;
    explicit operator bool() const { return true; }
    T field, other;
    T value() const { return field; }
};
int prototype(int value);
auto [left, right] = pair;
}
`
	tree, ok := parseCPPSyntax(source)
	if !ok {
		t.Fatal("parseCPPSyntax rejected valid modern C++")
	}
	lineCount := len(strings.Split(strings.TrimSuffix(source, "\n"), "\n"))
	definitions := cppTreeDefinitions(source, lineCount, tree)
	symbols := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		symbols = append(symbols, definition.symbol)
		line := strings.Split(source, "\n")[definition.line-1]
		start := definition.column - 1
		end := start + len(definition.symbol)
		if start < 0 || end > len(line) || line[start:end] != definition.symbol {
			t.Errorf("invalid coordinate for %#v in %q", definition, line)
		}
	}
	want := []string{
		"outer::inner", "alias", "Count", "Sized", "Box", "Box", "~Box",
		"operator=", "operator bool", "field", "other", "value", "prototype",
		"left", "right",
	}
	if !reflect.DeepEqual(symbols, want) {
		t.Fatalf("definitions = %#v, want %#v", symbols, want)
	}

	if imports := cppTreeImports(source, tree); !reflect.DeepEqual(
		imports, []cLineSpan{{start: 1, end: 1}},
	) {
		t.Fatalf("imports = %#v, want include on line 1", imports)
	}

	scopes := cppTreeScopes(source, tree)
	for _, wantScope := range []cLineScope{
		{start: 2, end: 21},
		{start: 8, end: 18},
		{start: 15, end: 15},
		{start: 17, end: 17},
	} {
		found := false
		for _, scope := range scopes {
			if scope == wantScope {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("scopes %#v do not contain %#v", scopes, wantScope)
		}
	}
}
