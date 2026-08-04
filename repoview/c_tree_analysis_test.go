package repoview

import (
	"reflect"
	"strings"
	"testing"

	clanguage "github.com/dcosson/treesitter-go/languages/c"
)

func TestCTreeDefinitionsCoverConcreteCDeclarations(t *testing.T) {
	t.Parallel()

	source := cTreeAnalysisSource(
		"int value, *pointer, array[SIZE], initialized = seed;",
		"extern int prototype(int, char *);",
		"int (*callback)(int);",
		"typedef unsigned long size_type, *size_ptr;",
		"typedef size_type Alias, *AliasPtr;",
		"struct Point {",
		"    int x, y;",
		"    unsigned flags : 3;",
		"    int : 0;",
		"};",
		"union Value {",
		"    int integer;",
		"    const char *text;",
		"};",
		"enum Color {",
		"    RED,",
		"    GREEN = 4,",
		"};",
		"typedef struct {",
		"    int member;",
		"} Anonymous, *AnonymousPtr;",
		"#define OBJECT 42",
		"#define APPLY(x) ((x) + 1)",
		"#define MULTI(x) \\",
		"    ((x) + 1)",
		"/** run docs. */",
		"static inline int run(int parameter) {",
		"    int local = parameter;",
		"    typedef int LocalAlias;",
		"    struct LocalTag {",
		"        int nested;",
		"    };",
		"    return local;",
		"}",
		"int old(a, b)",
		"int a;",
		"char *b;",
		"{",
		"    return a;",
		"}",
	)
	tree := cTreeAnalysisTestParse(t, source)
	lines := strings.Split(source, "\n")
	definitions := cTreeDefinitions(source, len(lines), tree)

	gotSymbols := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		gotSymbols = append(gotSymbols, definition.symbol)
		cTreeAnalysisAssertDefinitionCoordinate(t, lines, definition)
	}
	wantSymbols := []string{
		"value", "pointer", "array", "initialized",
		"prototype", "callback",
		"size_type", "size_ptr",
		"Alias", "AliasPtr",
		"Point", "x", "y", "flags",
		"Value", "integer", "text",
		"Color", "RED", "GREEN",
		"member", "Anonymous", "AnonymousPtr",
		"OBJECT", "APPLY", "MULTI",
		"run", "LocalAlias", "LocalTag", "nested", "old",
	}
	if !reflect.DeepEqual(gotSymbols, wantSymbols) {
		t.Fatalf("definitions = %#v, want %#v", gotSymbols, wantSymbols)
	}

	cTreeAnalysisAssertOwnedScope(t, definitions, "Point", 6, 10)
	cTreeAnalysisAssertOwnedScope(t, definitions, "Value", 11, 14)
	cTreeAnalysisAssertOwnedScope(t, definitions, "Color", 15, 18)
	cTreeAnalysisAssertOwnedScope(t, definitions, "Anonymous", 19, 21)
	cTreeAnalysisAssertOwnedScope(t, definitions, "AnonymousPtr", 19, 21)
	cTreeAnalysisAssertOwnedScope(t, definitions, "MULTI", 24, 25)
	cTreeAnalysisAssertOwnedScope(t, definitions, "run", 26, 34)
	cTreeAnalysisAssertOwnedScope(t, definitions, "LocalTag", 30, 32)
	cTreeAnalysisAssertOwnedScope(t, definitions, "old", 35, 40)

	for _, symbol := range []string{
		"SIZE", "seed", "parameter", "local", "a", "b",
	} {
		if cTreeAnalysisDefinitionNamed(definitions, symbol) != nil {
			t.Fatalf("non-definition %q was indexed: %#v", symbol, definitions)
		}
	}
}

func TestCTreeDefinitionsExcludeOrdinaryLocalsAndKeepNestedItems(t *testing.T) {
	t.Parallel()

	source := cTreeAnalysisSource(
		"void outer(int parameter) {",
		"    int local;",
		"    static int local_static;",
		"    extern int local_extern;",
		"    int (*local_callback)(int);",
		"    int local_prototype(int);",
		"    for (int index = 0; index < parameter; index++) {",
		"        int loop_local;",
		"    }",
		"    typedef int LocalAlias;",
		"    struct Local {",
		"        int field;",
		"    };",
		"    int nested(int argument) {",
		"        int hidden;",
		"        return argument;",
		"    }",
		"}",
	)
	tree := cTreeAnalysisTestParse(t, source)
	lines := strings.Split(source, "\n")
	definitions := cTreeDefinitions(source, len(lines), tree)
	got := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		got = append(got, definition.symbol)
		cTreeAnalysisAssertDefinitionCoordinate(t, lines, definition)
	}
	want := []string{"outer", "local_prototype", "LocalAlias", "Local", "field", "nested"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("definitions = %#v, want %#v", got, want)
	}
	cTreeAnalysisAssertOwnedScope(t, definitions, "outer", 1, 18)
	cTreeAnalysisAssertOwnedScope(t, definitions, "Local", 11, 13)
	cTreeAnalysisAssertOwnedScope(t, definitions, "nested", 14, 17)
}

func TestCTreeImportsScopesAndDocumentationCoordinates(t *testing.T) {
	t.Parallel()

	source := cTreeAnalysisSource(
		"#include <one.h>",
		"#if FEATURE",
		"#include \"two.h\"",
		"#else",
		"#include HEADER",
		"#endif",
		"",
		"/** aggregate docs. */",
		"struct Box {",
		"    int field;",
		"};",
		"",
		"/** function docs. */",
		"int flow(int x) {",
		"    if (x) {",
		"        x++;",
		"    } else {",
		"        x--;",
		"    }",
		"    for (int index = 0; index < x; index++) {",
		"        x += index;",
		"    }",
		"    {",
		"        x = 0;",
		"    }",
		"    return x;",
		"}",
	)
	tree := cTreeAnalysisTestParse(t, source)

	imports := cTreeImports(source, tree)
	wantImports := []cLineSpan{{start: 1, end: 1}, {start: 3, end: 3}, {start: 5, end: 5}}
	if !reflect.DeepEqual(imports, wantImports) {
		t.Fatalf("imports = %#v, want %#v", imports, wantImports)
	}

	scopes := cTreeScopes(source, tree)
	for _, want := range []cLineScope{
		{start: 2, end: 6},
		{start: 4, end: 5},
		{start: 8, end: 11},
		{start: 13, end: 27},
		{start: 15, end: 19},
		{start: 15, end: 17},
		{start: 17, end: 19},
		{start: 20, end: 22},
		{start: 23, end: 25},
	} {
		if !cTreeAnalysisHasScope(scopes, want) {
			t.Errorf("scopes %#v do not contain %#v", scopes, want)
		}
	}

	definitions := cTreeDefinitions(
		source, len(strings.Split(source, "\n")), tree,
	)
	cTreeAnalysisAssertOwnedScope(t, definitions, "Box", 8, 11)
	cTreeAnalysisAssertOwnedScope(t, definitions, "flow", 13, 27)
}

func TestCTreeErrorsAreStrictButWholeRootWrapperIsTransparent(t *testing.T) {
	t.Parallel()

	malformed := cTreeAnalysisSource(
		"int good_before;",
		"int broken( {",
		"    const char *text = \"unterminated;",
		"}",
		"struct Missing { int field;",
		"int good_after(void) { return 1; }",
	)
	tree := cTreeAnalysisTestParse(t, malformed)
	definitions := cTreeDefinitions(
		malformed, len(strings.Split(malformed, "\n")), tree,
	)
	got := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		got = append(got, definition.symbol)
	}
	if want := []string{"good_before"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("malformed definitions = %#v, want %#v", got, want)
	}
	if spans := cSyntaxErrorSpans(tree, len(malformed)); len(spans) == 0 {
		t.Fatal("malformed source produced no concrete ERROR spans")
	}

	missingDeclarator := cTreeAnalysisSource(
		"int good(void) {}",
		"int broken(",
		"int after(void) { return 2; }",
	)
	missingTree := cTreeAnalysisTestParse(t, missingDeclarator)
	missingDefinitions := cTreeDefinitions(
		missingDeclarator,
		len(strings.Split(missingDeclarator, "\n")),
		missingTree,
	)
	if got, want := cTreeAnalysisDefinitionSymbols(missingDefinitions),
		[]string{"good"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("missing declarator definitions = %#v, want %#v", got, want)
	}
	if spans := cSyntaxErrorSpans(missingTree, len(missingDeclarator)); len(spans) == 0 {
		t.Fatal("zero-width missing declarator produced no recovery span")
	}

	const valid = "int kept(void) { return 0; }"
	wrapped := cTreeAnalysisTestParse(t, valid)
	wrapped.nodes = append([]cSyntaxNode(nil), wrapped.nodes...)
	wrapped.nodes[wrapped.root].kind = "ERROR"
	if contexts := cSyntaxErrorContexts(wrapped); contexts[wrapped.root] {
		t.Fatalf("whole-file ERROR wrapper was treated as an error context: %#v", contexts)
	}
	if spans := cSyntaxErrorSpans(wrapped, len(valid)); len(spans) != 0 {
		t.Fatalf("whole-file ERROR wrapper produced spans: %#v", spans)
	}
	wrappedDefinitions := cTreeDefinitions(valid, 1, wrapped)
	if len(wrappedDefinitions) != 1 || wrappedDefinitions[0].symbol != "kept" {
		t.Fatalf("wrapped definitions = %#v, want kept", wrappedDefinitions)
	}
}

func TestCTreeRejectsMissingNamesAndMalformedIncludes(t *testing.T) {
	t.Parallel()

	source := cTreeAnalysisSource(
		"struct Bits {",
		"    int : 0;",
		"    int named : 1;",
		"};",
		"#include <valid.h>",
		"#include",
	)
	tree := cTreeAnalysisTestParse(t, source)
	definitions := cTreeDefinitions(
		source, len(strings.Split(source, "\n")), tree,
	)
	got := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		got = append(got, definition.symbol)
	}
	if want := []string{"Bits", "named"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("definitions = %#v, want %#v", got, want)
	}
	if gotImports, want := cTreeImports(source, tree),
		[]cLineSpan{{start: 5, end: 5}}; !reflect.DeepEqual(gotImports, want) {
		t.Fatalf("imports = %#v, want %#v", gotImports, want)
	}
}

func TestCTreeForwardTagDoesNotPromoteLaterTypeUses(t *testing.T) {
	t.Parallel()

	source := cTreeAnalysisSource(
		"struct Forward;",
		"struct Forward object;",
		"void use(void) {",
		"    struct Forward local;",
		"}",
	)
	definitions := cTreeDefinitions(
		source,
		len(strings.Split(source, "\n")),
		cTreeAnalysisTestParse(t, source),
	)
	if got, want := cTreeAnalysisDefinitionSymbols(definitions),
		[]string{"Forward", "object", "use"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("forward-tag definitions = %#v, want %#v", got, want)
	}
	forward := cTreeAnalysisDefinitionNamed(definitions, "Forward")
	if forward == nil || forward.line != 1 || forward.scopeStart != 1 ||
		forward.scopeEnd != 1 || forward.ownsScope {
		t.Fatalf("forward-tag metadata = %#v, want non-owning line 1", forward)
	}
}

func TestCTreeDeclaratorUnwrapIsDepthBounded(t *testing.T) {
	t.Parallel()

	const wrappers = cMaximumDeclaratorUnwrapDepth + 1
	tree := &cSyntaxTree{
		nodes: make([]cSyntaxNode, wrappers+1),
		root:  0,
	}
	for index := range wrappers {
		tree.nodes[index] = cSyntaxNode{
			kind:      "pointer_declarator",
			children:  []int{index + 1},
			startByte: 0,
			endByte:   1,
			parent:    index - 1,
		}
	}
	tree.nodes[wrappers] = cSyntaxNode{
		kind:      "identifier",
		startByte: 0,
		endByte:   1,
		parent:    wrappers - 1,
	}
	if name, ok := cUnwrapDeclaratorName(tree, tree.root, "identifier"); ok {
		t.Fatalf("over-depth declarator unexpectedly unwrapped: %#v", name)
	}
}

func TestCTreeEnumeratorRecoveryLookaheadHasALinearFrontier(t *testing.T) {
	t.Parallel()

	const enumeratorCount = 32 << 10
	tree := &cSyntaxTree{
		nodes: make([]cSyntaxNode, 1, enumeratorCount*2+2),
		root:  0,
	}
	children := make([]int, 0, enumeratorCount*2)
	enumerators := make([]int, 0, enumeratorCount)
	recoveryAfter := enumeratorCount / 2
	for index := range enumeratorCount {
		enumeratorIndex := len(tree.nodes)
		tree.nodes = append(tree.nodes, cSyntaxNode{
			kind:      "enumerator",
			startByte: index * 2,
			endByte:   index*2 + 1,
			parent:    tree.root,
		})
		children = append(children, enumeratorIndex)
		enumerators = append(enumerators, enumeratorIndex)
		if index == recoveryAfter {
			recoveryIndex := len(tree.nodes)
			tree.nodes = append(tree.nodes, cSyntaxNode{
				kind:      "ERROR",
				startByte: index*2 + 1,
				endByte:   index*2 + 2,
				parent:    tree.root,
			})
			children = append(children, recoveryIndex)
		}
		if index+1 < enumeratorCount {
			commaIndex := len(tree.nodes)
			tree.nodes = append(tree.nodes, cSyntaxNode{
				kind:      ",",
				startByte: index*2 + 1,
				endByte:   index*2 + 2,
				parent:    tree.root,
			})
			children = append(children, commaIndex)
		}
	}
	tree.nodes[tree.root] = cSyntaxNode{
		kind:      "enumerator_list",
		children:  children,
		startByte: 0,
		endByte:   enumeratorCount * 2,
		parent:    -1,
	}

	flags, childVisits := cSyntaxRecoveryBeforeNextSiblingFlags(tree, "enumerator")
	if childVisits != len(children) {
		t.Fatalf("recovery lookahead visited %d child edges, want exactly %d",
			childVisits, len(children))
	}
	for index, enumeratorIndex := range enumerators {
		want := index == recoveryAfter
		if flags[enumeratorIndex] != want {
			t.Fatalf("enumerator %d recovery flag = %v, want %v",
				index, flags[enumeratorIndex], want)
		}
	}
}

func TestCTreeTrailingDocumentationNeverAttachesToNextOwner(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		prefix string
	}{
		{name: "trailing slash angle", prefix: "int prior; ///< trailing docs"},
		{name: "trailing bang angle", prefix: "int prior; //!< trailing docs"},
		{name: "trailing block star angle", prefix: "int prior; /**< trailing docs */"},
		{name: "trailing block bang angle", prefix: "int prior; /*!< trailing docs */"},
		{name: "generic slash after code", prefix: "int prior; /// trailing docs"},
		{name: "generic block after code", prefix: "int prior; /** trailing docs */"},
		{name: "orphan slash angle", prefix: "///< trailing docs"},
		{name: "orphan bang angle", prefix: "//!< trailing docs"},
		{name: "orphan block star angle", prefix: "/**< trailing docs */"},
		{name: "orphan block bang angle", prefix: "/*!< trailing docs */"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			source := cTreeAnalysisSource(
				test.prefix,
				"int next(void)",
				"{",
				"    return 0;",
				"}",
			)
			tree := cTreeAnalysisTestParse(t, source)
			definitions := cTreeDefinitions(source, 5, tree)
			next := cTreeAnalysisDefinitionNamed(definitions, "next")
			if next == nil {
				t.Fatalf("next definition missing: %#v", definitions)
			}
			if next.scopeStart != 2 || next.scopeEnd != 5 || !next.ownsScope {
				t.Fatalf("next definition = %#v, want owning scope 2-5", *next)
			}

			analysis := analyzeCSource(source, 5)
			start, end := analysis.scopeResolver.navigationScope(1)
			if start != 1 || end != 1 || analysis.scopeResolver.scopeName(1) != "" {
				t.Fatalf("trailing documentation navigation = %d-%d/%q, want 1-1/empty",
					start, end, analysis.scopeResolver.scopeName(1))
			}
		})
	}

	const leading = `    /// leading documentation
int next(void)
{
    return 0;
}`
	leadingTree := cTreeAnalysisTestParse(t, leading)
	definitions := cTreeDefinitions(leading, 5, leadingTree)
	next := cTreeAnalysisDefinitionNamed(definitions, "next")
	if next == nil || next.scopeStart != 1 || next.scopeEnd != 5 || !next.ownsScope {
		t.Fatalf("leading documentation definition = %#v, want owning scope 1-5", next)
	}
	analysis := analyzeCSource(leading, 5)
	start, end := analysis.scopeResolver.navigationScope(1)
	if start != 1 || end != 5 || analysis.scopeResolver.scopeName(1) != "next" {
		t.Fatalf("leading documentation navigation = %d-%d/%q, want 1-5/next",
			start, end, analysis.scopeResolver.scopeName(1))
	}

	const bomLeading = "\uFEFF/// leading documentation\n" +
		"int next(void)\n{\n    return 0;\n}"
	bomTree := cTreeAnalysisTestParse(t, bomLeading)
	bomDefinitions := cTreeDefinitions(bomLeading, 5, bomTree)
	bomNext := cTreeAnalysisDefinitionNamed(bomDefinitions, "next")
	if bomNext == nil || bomNext.scopeStart != 1 || bomNext.scopeEnd != 5 ||
		!bomNext.ownsScope {
		t.Fatalf("BOM-leading documentation definition = %#v, want owning scope 1-5",
			bomNext)
	}
}

func cTreeAnalysisSource(lines ...string) string {
	return strings.Join(lines, "\n")
}

func cTreeAnalysisTestParse(t *testing.T, source string) *cSyntaxTree {
	t.Helper()
	tree, ok := parseTreeSitterSyntax(source, clanguage.Language())
	if !ok || tree == nil {
		t.Fatal("C concrete parse failed")
	}
	return tree
}

func cTreeAnalysisAssertDefinitionCoordinate(
	t *testing.T,
	lines []string,
	definition sourceDefinition,
) {
	t.Helper()
	if definition.line < 1 || definition.line > len(lines) ||
		definition.column < 1 {
		t.Fatalf("invalid definition coordinate: %#v", definition)
	}
	line := lines[definition.line-1]
	start := definition.column - 1
	end := start + len(definition.symbol)
	if start < 0 || end > len(line) || line[start:end] != definition.symbol {
		t.Fatalf(
			"definition coordinate %#v does not select its symbol from %q",
			definition, line,
		)
	}
}

func cTreeAnalysisDefinitionNamed(
	definitions []sourceDefinition,
	symbol string,
) *sourceDefinition {
	for index := range definitions {
		if definitions[index].symbol == symbol {
			return &definitions[index]
		}
	}
	return nil
}

func cTreeAnalysisDefinitionSymbols(definitions []sourceDefinition) []string {
	symbols := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		symbols = append(symbols, definition.symbol)
	}
	return symbols
}

func cTreeAnalysisAssertOwnedScope(
	t *testing.T,
	definitions []sourceDefinition,
	symbol string,
	start, end int,
) {
	t.Helper()
	definition := cTreeAnalysisDefinitionNamed(definitions, symbol)
	if definition == nil {
		t.Fatalf("missing definition %q in %#v", symbol, definitions)
	}
	if !definition.ownsScope ||
		definition.scopeStart != start || definition.scopeEnd != end {
		t.Fatalf(
			"definition %q = %#v, want owned scope %d-%d",
			symbol, *definition, start, end,
		)
	}
}

func cTreeAnalysisHasScope(scopes []cLineScope, want cLineScope) bool {
	for _, scope := range scopes {
		if scope == want {
			return true
		}
	}
	return false
}
