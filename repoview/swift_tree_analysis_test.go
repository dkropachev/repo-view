package repoview

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestSwiftTreeAnalysisExtractsDefinitionsScopesImportsAndDocumentation(t *testing.T) {
	t.Parallel()

	const source = `import Foundation
import struct Collections.OrderedDictionary

/// Service documentation.
@MainActor
final class Service<T> {
    enum State {
        case idle
        case active(Int)
    }

    static let shared = Service<Int>()
    private var storage = 0

    var value: Int {
        get { storage }
        set { storage = newValue }
    }

    init(value: Int) {
        storage = value
    }

    deinit {
        cleanup()
    }

    subscript(index: Int) -> Int {
        storage
    }

    func run() {
        let localValue = 1
        func local() { consume(localValue) }
        struct LocalType {}
        if storage > 0 {
            local()
        }
    }

    struct Nested {}
}

extension Service {
    func extended() {}
}

typealias Alias = OrderedDictionary<String, Int>
`
	lines := swiftTestLines(source)
	tree := swiftTreeTestParse(t, source)
	if spans := swiftSyntaxErrorSpans(tree, len(source)); len(spans) != 0 {
		t.Fatalf("valid Swift recovery spans = %#v, want none", spans)
	}

	definitions := swiftTreeDefinitions(source, len(lines), tree)
	want := []string{
		"Service", "State", "idle", "active", "shared", "storage", "value",
		"init", "deinit", "subscript", "run", "local", "LocalType", "Nested",
		"Service", "extended", "Alias",
	}
	if got := swiftTestDefinitionSymbols(definitions); !slices.Equal(got, want) {
		t.Fatalf("concrete Swift definitions =\n%#v\nwant\n%#v", got, want)
	}
	for _, forbidden := range []string{
		"T", "Int", "index", "localValue", "newValue", "consume", "cleanup",
		"OrderedDictionary", "MainActor",
	} {
		if slices.Contains(swiftTestDefinitionSymbols(definitions), forbidden) {
			t.Errorf("non-outline binding %q became a concrete definition: %#v",
				forbidden, definitions)
		}
	}
	swiftTestAssertDefinitionCoordinates(t, lines, definitions)

	service := swiftTestFirstDefinition(t, definitions, "Service")
	serviceDoc := swiftTestLineContaining(t, lines, "/// Service documentation")
	serviceEnd := swiftTestLineContaining(t, lines, "extension Service") - 2
	if !service.ownsScope || service.scopeStart != serviceDoc || service.scopeEnd != serviceEnd {
		t.Fatalf("documented Service scope = %#v, want %d-%d", service, serviceDoc, serviceEnd)
	}
	for _, symbol := range []string{"idle", "active", "shared", "storage"} {
		definition := swiftTestFirstDefinition(t, definitions, symbol)
		if definition.ownsScope || definition.scopeStart != definition.line ||
			definition.scopeEnd != definition.line {
			t.Errorf("non-owning concrete definition %q = %#v", symbol, definition)
		}
	}

	wantImports := []cLineSpan{{start: 1, end: 1}, {start: 2, end: 2}}
	if got := swiftTreeImports(source, len(lines), tree); !reflect.DeepEqual(got, wantImports) {
		t.Fatalf("concrete Swift imports = %#v, want %#v", got, wantImports)
	}

	scopes := swiftTreeScopes(source, len(lines), tree)
	for _, wantScope := range []cLineScope{
		{start: serviceDoc, end: serviceEnd},
		{
			start: swiftTestLineContaining(t, lines, "func run"),
			end:   swiftTestLineContaining(t, lines, "struct Nested") - 2,
		},
		{
			start: swiftTestLineContaining(t, lines, "if storage"),
			end:   swiftTestLineContaining(t, lines, "if storage") + 2,
		},
	} {
		if !slices.Contains(scopes, wantScope) {
			t.Errorf("concrete scopes do not contain %#v: %#v", wantScope, scopes)
		}
	}
}

func TestSwiftTreeDefinitionsPreferDeclaredNamesOverExpressionIdentifiers(t *testing.T) {
	t.Parallel()

	const source = `enum State {
    case ready = DefaultReady
}

struct Fixture {
    static let value = DefaultValue()
    var computed: Int { Factory.make() }
    func call(argument: Int = DefaultArgument()) {}
}
`
	lines := swiftTestLines(source)
	definitions := swiftTreeDefinitions(source, len(lines), swiftTreeTestParse(t, source))
	want := []string{"State", "ready", "Fixture", "value", "computed", "call"}
	if got := swiftTestDefinitionSymbols(definitions); !slices.Equal(got, want) {
		t.Fatalf("initializer definitions = %#v, want %#v", got, want)
	}
	for _, expression := range []string{
		"DefaultReady", "DefaultValue", "Factory", "make", "argument", "DefaultArgument",
	} {
		if slices.Contains(swiftTestDefinitionSymbols(definitions), expression) {
			t.Errorf("initializer identifier %q became a definition", expression)
		}
	}
}

func TestSwiftTreeExtractsFixedOperatorFunctionNames(t *testing.T) {
	t.Parallel()

	const source = `struct Value {
    static func == (lhs: Self, rhs: Self) -> Bool { true }
    prefix static func ! (value: Self) -> Bool { false }
}
`
	lines := swiftTestLines(source)
	tree := swiftTreeTestParse(t, source)
	if spans := swiftSyntaxErrorSpans(tree, len(source)); len(spans) != 0 {
		t.Fatalf("fixed operator fixture recovery spans = %#v, want none", spans)
	}
	definitions := swiftTreeDefinitions(source, len(lines), tree)
	want := []string{"Value", "==", "!"}
	if got := swiftTestDefinitionSymbols(definitions); !slices.Equal(got, want) {
		t.Fatalf("fixed operator definitions = %#v, want %#v", got, want)
	}
	for _, operator := range []string{"==", "!"} {
		definition := swiftTestFirstDefinition(t, definitions, operator)
		if !definition.ownsScope || definition.scopeStart != definition.line ||
			definition.scopeEnd != definition.line {
			t.Errorf("fixed operator %q scope = %#v, want owning declaration line",
				operator, definition)
		}
	}
	swiftTestAssertDefinitionCoordinates(t, lines, definitions)
}

func TestSwiftTreeImportsRejectTypeAndFunctionLocalRecoveryImports(t *testing.T) {
	t.Parallel()

	const source = `import Foundation

struct Holder {
    import Darwin

    func run() {
        import Dispatch
    }
}
`
	lines := swiftTestLines(source)
	lexed := lexSwift(source)
	if !lexed.concreteEligible {
		t.Fatal("small nested-import fixture is not concrete-eligible")
	}
	tree, ok := parseSwiftSyntax(source, lexed)
	if !ok || !validateSwiftSyntaxTree(tree, len(source)) {
		t.Fatal("nested-import fixture did not produce a validated concrete tree")
	}
	want := []cLineSpan{{start: 1, end: 1}}
	if got := swiftTreeImports(source, len(lines), tree); !reflect.DeepEqual(got, want) {
		t.Fatalf("concrete imports = %#v, want file-scope import only %#v", got, want)
	}

	backend := newSwiftLanguage()
	if start, end, ok := backend.importRange(lines); !ok || start != 1 || end != 1 {
		t.Fatalf("merged import range = %d-%d, %v; want 1-1, true", start, end, ok)
	}
}

func TestSwiftSyntaxErrorSpansCoverErrorAndMissingNodes(t *testing.T) {
	t.Parallel()

	tree := &swiftSyntaxTree{
		root: 0,
		nodes: []swiftSyntaxNode{
			{kind: "source_file", startByte: 0, endByte: 12, parent: -1, children: []int{1, 2}},
			{kind: "ERROR", startByte: 2, endByte: 5, parent: 0},
			{kind: "simple_identifier", startByte: 9, endByte: 9, parent: 0},
		},
	}
	if !validateSwiftSyntaxTree(tree, 12) {
		t.Fatal("synthetic Swift syntax tree is invalid")
	}
	want := []cByteSpan{{start: 2, end: 5}, {start: 9, end: 10}}
	if got := swiftSyntaxErrorSpans(tree, 12); !reflect.DeepEqual(got, want) {
		t.Fatalf("Swift recovery spans = %#v, want %#v", got, want)
	}
}

func TestSwiftTreeTrailingRecoveryInvalidatesOnlySameLineBodylessDeclarations(t *testing.T) {
	t.Parallel()

	malformed := []struct {
		name        string
		source      string
		forbidden   string
		checkImport bool
	}{
		{
			name: "operator", source: "prefix operator + nonsense\nfunc tail() {}",
			forbidden: "+",
		},
		{
			name: "property", source: "let broken nonsense\nfunc tail() {}",
			forbidden: "broken",
		},
		{
			name:      "associated type",
			source:    "protocol P { associatedtype Broken nonsense }\nfunc tail() {}",
			forbidden: "Broken",
		},
		{
			name: "import", source: "import Foundation nonsense\nfunc tail() {}",
			checkImport: true,
		},
	}
	for _, malformedCase := range malformed {
		t.Run("same line/"+malformedCase.name, func(t *testing.T) {
			t.Parallel()

			tree := swiftTreeTestParseRecovery(t, malformedCase.source)
			if spans := swiftSyntaxErrorSpans(tree, len(malformedCase.source)); len(spans) == 0 {
				t.Fatal("malformed direct-tree fixture has no pinned recovery span")
			}
			definitions := swiftTreeDefinitions(
				malformedCase.source, len(swiftTestLines(malformedCase.source)), tree,
			)
			symbols := swiftTestDefinitionSymbols(definitions)
			if !slices.Contains(symbols, "tail") {
				t.Errorf("same-line recovery lost tail: %#v", definitions)
			}
			if malformedCase.forbidden != "" && slices.Contains(symbols, malformedCase.forbidden) {
				t.Errorf("same-line recovery promoted %q: %#v",
					malformedCase.forbidden, definitions)
			}
			if malformedCase.checkImport {
				if imports := swiftTreeImports(
					malformedCase.source, len(swiftTestLines(malformedCase.source)), tree,
				); len(imports) != 0 {
					t.Errorf("same-line malformed import spans = %#v, want none", imports)
				}
			}
			swiftTestAssertDefinitionCoordinates(
				t, swiftTestLines(malformedCase.source), definitions,
			)
		})
	}

	controls := []struct {
		name       string
		source     string
		required   string
		wantImport bool
	}{
		{name: "operator newline", source: "prefix operator +\n)\nfunc tail() {}", required: "+"},
		{name: "operator semicolon", source: "prefix operator +; )\nfunc tail() {}", required: "+"},
		{name: "property newline", source: "let kept = 1\n)\nfunc tail() {}", required: "kept"},
		{name: "property semicolon", source: "let kept = 1; )\nfunc tail() {}", required: "kept"},
		{
			name:   "associated type newline",
			source: "protocol P {\nassociatedtype Kept\n)\n}\nfunc tail() {}", required: "Kept",
		},
		{
			name:   "associated type semicolon",
			source: "protocol P { associatedtype Kept; )\n}\nfunc tail() {}", required: "Kept",
		},
		{
			name: "import newline", source: "import Foundation\n)\nfunc tail() {}",
			wantImport: true,
		},
		{
			name: "import semicolon", source: "import Foundation; )\nfunc tail() {}",
			wantImport: true,
		},
	}
	for _, control := range controls {
		t.Run("separated/"+control.name, func(t *testing.T) {
			t.Parallel()

			tree := swiftTreeTestParseRecovery(t, control.source)
			if spans := swiftSyntaxErrorSpans(tree, len(control.source)); len(spans) == 0 {
				t.Fatal("separator control does not contain its independent recovery")
			}
			lines := swiftTestLines(control.source)
			definitions := swiftTreeDefinitions(control.source, len(lines), tree)
			symbols := swiftTestDefinitionSymbols(definitions)
			for _, required := range []string{control.required, "tail"} {
				if required != "" && !slices.Contains(symbols, required) {
					t.Errorf("separated recovery lost %q: %#v", required, definitions)
				}
			}
			if control.wantImport {
				want := []cLineSpan{{start: 1, end: 1}}
				if got := swiftTreeImports(control.source, len(lines), tree); !reflect.DeepEqual(got, want) {
					t.Errorf("separated import spans = %#v, want %#v", got, want)
				}
			}
			swiftTestAssertDefinitionCoordinates(t, lines, definitions)
		})
	}
}

func TestSwiftTreeRejectedRecoveryBlocksRespectCommentsAndPhysicalSeparators(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		source     string
		visible    string
		wantReject bool
	}{
		{
			name: "same line", source: "import Foundation { func hidden() {} }\nfunc tail() {}",
			visible: "hidden", wantReject: true,
		},
		{
			name:    "same line comment gap",
			source:  "import Foundation /* gap */ { func hidden() {} }\nfunc tail() {}",
			visible: "hidden", wantReject: true,
		},
		{
			name: "newline", source: "import Foundation\n{ func visible() {} }\nfunc tail() {}",
			visible: "visible",
		},
		{
			name: "semicolon", source: "import Foundation; { func visible() {} }\nfunc tail() {}",
			visible: "visible",
		},
		{
			name:    "LF inside comment",
			source:  "import Foundation /* gap\ncontinuation */ { func visible() {} }\nfunc tail() {}",
			visible: "visible",
		},
		{
			name:    "CR inside comment",
			source:  "import Foundation /* gap\rcontinuation */ { func visible() {} }\nfunc tail() {}",
			visible: "visible",
		},
		{
			name:    "CRLF inside comment",
			source:  "import Foundation /* gap\r\ncontinuation */ { func visible() {} }\nfunc tail() {}",
			visible: "visible",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			tree := swiftTreeTestParseRecovery(t, test.source)
			lexed := lexSwift(test.source)
			gotSpans := swiftTreeRejectedRecoveryBlockSpans(test.source, tree, lexed)
			if test.wantReject {
				blockEnd := strings.Index(test.source, "\nfunc tail")
				wantSpans := []cByteSpan{{start: 0, end: blockEnd}}
				if !reflect.DeepEqual(gotSpans, wantSpans) {
					t.Errorf("rejected recovery block spans = %#v, want %#v", gotSpans, wantSpans)
				}
			} else if len(gotSpans) != 0 {
				t.Errorf("separated recovery block spans = %#v, want none", gotSpans)
			}

			lines := swiftTestLines(test.source)
			definitions := swiftTreeDefinitions(test.source, len(lines), tree)
			symbols := swiftTestDefinitionSymbols(definitions)
			if !slices.Contains(symbols, "tail") {
				t.Errorf("recovery-block fixture lost tail: %#v", definitions)
			}
			if test.wantReject {
				if slices.Contains(symbols, test.visible) {
					t.Errorf("rejected recovery block promoted %q: %#v", test.visible, definitions)
				}
				if imports := swiftTreeImports(test.source, len(lines), tree); len(imports) != 0 {
					t.Errorf("rejected recovery block retained import: %#v", imports)
				}
			} else if !slices.Contains(symbols, test.visible) {
				t.Errorf("separated recovery block lost %q: %#v", test.visible, definitions)
			}
			swiftTestAssertDefinitionCoordinates(t, lines, definitions)
		})
	}
}

func TestSwiftConcreteParserResourceGates(t *testing.T) {
	t.Parallel()

	const source = "func kept() {}\n"
	for _, test := range []struct {
		name  string
		lexed swiftLexResult
	}{
		{
			name: "ineligible",
			lexed: swiftLexResult{
				lexicalUnits: 4,
			},
		},
		{
			name: "token limit",
			lexed: swiftLexResult{
				concreteEligible: true,
				lexicalUnits:     swiftMaximumConcreteTokens + 1,
			},
		},
		{
			name: "delimiter limit",
			lexed: swiftLexResult{
				concreteEligible:      true,
				maximumDelimiterDepth: swiftMaximumConcreteDelimiterDepth + 1,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if tree, ok := parseSwiftSyntax(source, test.lexed); ok || tree != nil {
				t.Fatalf("over-frontier parse = %#v, %t; want nil, false", tree, ok)
			}
		})
	}
	overBytes := strings.Repeat(" ", swiftMaximumConcreteParseBytes+1)
	if tree, ok := parseSwiftSyntax(
		overBytes,
		swiftLexResult{concreteEligible: true},
	); ok || tree != nil {
		t.Fatalf("over-byte parse = %#v, %t; want nil, false", tree, ok)
	}
}

func TestSwiftConcreteTreeRejectsInvalidRootAndCoordinates(t *testing.T) {
	t.Parallel()

	invalid := []*swiftSyntaxTree{
		nil,
		{
			root: 0,
			nodes: []swiftSyntaxNode{
				{kind: "declaration", startByte: 0, endByte: 4, parent: -1},
			},
		},
		{
			root: 0,
			nodes: []swiftSyntaxNode{
				{kind: "source_file", startByte: 1, endByte: 4, parent: -1},
			},
		},
	}
	for index, tree := range invalid {
		if validateSwiftSyntaxTree(tree, 4) {
			t.Errorf("invalid tree %d was accepted: %#v", index, tree)
		}
	}
}

func swiftTreeTestParse(t *testing.T, source string) *swiftSyntaxTree {
	t.Helper()
	lexed := lexSwift(source)
	if !lexed.concreteEligible {
		t.Fatal("small valid Swift fixture is not concrete-eligible")
	}
	tree, ok := parseSwiftSyntax(source, lexed)
	if !ok || !validateSwiftSyntaxTree(tree, len(source)) {
		t.Fatal("valid Swift fixture did not produce a validated concrete tree")
	}
	return tree
}

func swiftTreeTestParseRecovery(t *testing.T, source string) *swiftSyntaxTree {
	t.Helper()
	lexed := lexSwift(source)
	lexed.concreteEligible = true
	tree, ok := parseSwiftSyntax(source, lexed)
	if !ok || !validateSwiftSyntaxTree(tree, len(source)) {
		t.Fatal("Swift recovery fixture did not produce a validated concrete tree")
	}
	return tree
}
