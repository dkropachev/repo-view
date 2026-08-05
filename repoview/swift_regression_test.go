package repoview

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestSwiftGrammarGapRecoveryKeepsIndependentModernDeclarations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		source    string
		required  []string
		forbidden []string
	}{
		{
			name: "optional cast followed by nil coalescing",
			source: `struct CastGap {
    func convert(_ value: Any) -> String {
        value as? String ?? "fallback"
    }
    func tail() { Target() }
}
struct CastTail {}
`,
			required:  []string{"CastGap", "convert", "tail", "CastTail"},
			forbidden: []string{"Any", "String", "Target"},
		},
		{
			name: "generic member type is not comparison",
			source: `struct GenericGap {
    func size() -> Int {
        MemoryLayout<Result<Int>>.size
    }
    func tail() {}
}
struct GenericTail {}
`,
			required:  []string{"GenericGap", "size", "tail", "GenericTail"},
			forbidden: []string{"MemoryLayout", "Result", "Int"},
		},
		{
			name: "Swift 6.3 module selector",
			source: `struct SelectorGap {
    func call() {
        ModuleA::getValue()
    }
    func tail() {}
}
struct SelectorTail {}
`,
			required:  []string{"SelectorGap", "call", "tail", "SelectorTail"},
			forbidden: []string{"ModuleA", "getValue"},
		},
		{
			name: "integer generic parameter and inline array",
			source: `struct Vector<let count: Int> {
    var storage: [count of UInt8]
    func tail() {}
}
struct InlineArrayTail {}
`,
			required:  []string{"Vector", "storage", "tail", "InlineArrayTail"},
			forbidden: []string{"count", "Int", "UInt8"},
		},
		{
			name: "sending and isolated deinitializer",
			source: `final class TransferBox {
    isolated deinit { cleanup() }
    func transfer(_ value: sending Value) -> sending Value { value }
    func tail() {}
}
struct TransferTail {}
`,
			required:  []string{"TransferBox", "deinit", "transfer", "tail", "TransferTail"},
			forbidden: []string{"cleanup", "value", "Value"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			lines := swiftTestLines(test.source)
			backend := prepareLanguageBackend(newSwiftLanguage(), lines)
			definitions := backend.sourceDefinitions(lines)
			symbols := swiftTestDefinitionSymbols(definitions)
			for _, required := range test.required {
				if !slices.Contains(symbols, required) {
					t.Errorf("grammar-gap recovery lost %q: %#v", required, definitions)
				}
			}
			for _, forbidden := range test.forbidden {
				if slices.Contains(symbols, forbidden) {
					t.Errorf("grammar-gap recovery promoted %q: %#v", forbidden, definitions)
				}
			}
			swiftTestAssertDefinitionCoordinates(t, lines, definitions)
			swiftTestAssertLineWidths(t, lines, backend.searchLines(lines, true, true))
		})
	}
}

func TestSwiftLexicalFallbackRetainsFixedOperatorFunctions(t *testing.T) {
	t.Parallel()

	hashes := strings.Repeat("#", swiftMaximumConcreteRawDelimiterHashes+1)
	source := "let opaque = " + hashes + `"literal"` + hashes + `
struct Value {
    static func == (lhs: Self, rhs: Self) -> Bool { true }
    prefix static func ! (value: Self) -> Bool { false }
    static func < (lhs: Self, rhs: Self) -> Bool { false }
}
`
	lexed := lexSwift(source)
	if lexed.concreteEligible || lexed.maximumRawHashCount <= swiftMaximumConcreteRawDelimiterHashes {
		t.Fatalf("forced fallback eligibility = %t with %d hashes; want false above %d",
			lexed.concreteEligible, lexed.maximumRawHashCount,
			swiftMaximumConcreteRawDelimiterHashes)
	}
	lines := swiftTestLines(source)
	analysis := analyzeSwiftSource(strings.TrimSuffix(source, "\n"), len(lines))
	if analysis.tree != nil {
		t.Fatal("over-hash fixture unexpectedly retained a concrete syntax tree")
	}
	want := []string{"opaque", "Value", "==", "!", "<"}
	if got := swiftTestDefinitionSymbols(analysis.definitions); !slices.Equal(got, want) {
		t.Fatalf("lexical fixed operator definitions = %#v, want %#v", got, want)
	}
	for _, operator := range []string{"==", "!", "<"} {
		if !swiftTestHasOwningDefinition(analysis.definitions, operator) {
			t.Errorf("lexical fixed operator %q has no owning definition: %#v",
				operator, analysis.definitions)
		}
	}
	swiftTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
}

func TestSwiftLanguageReferenceIdentifierHeadsSurviveRecoveryAndFallback(t *testing.T) {
	t.Parallel()

	names := []string{
		"\u00a8accent",
		"\u00b2superscript",
		"\u200bzeroWidth",
		"\ufeffinteriorBOM",
	}
	body := "let " + names[0] + " = 1\n" +
		"let " + names[1] + " = 2\n" +
		"let " + names[2] + " = 3\n" +
		"let " + names[3] + " = 4\n" +
		"struct IdentifierTail {}\n"
	hashes := strings.Repeat("#", swiftMaximumConcreteRawDelimiterHashes+1)
	fallbackPrefix := "let opaque = " + hashes + `"literal"` + hashes + "\n"
	tests := []struct {
		name           string
		prefix         string
		wantTreeAbsent bool
	}{
		{name: "concrete eligible"},
		{name: "forced lexical fallback", prefix: fallbackPrefix, wantTreeAbsent: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			source := test.prefix + body
			lexed := lexSwift(source)
			if got := !lexed.concreteEligible; got != test.wantTreeAbsent {
				t.Fatalf("concrete eligibility = %t, want %t", !got, !test.wantTreeAbsent)
			}
			lines := swiftTestLines(source)
			analysis := analyzeSwiftSource(strings.TrimSuffix(source, "\n"), len(lines))
			if test.wantTreeAbsent && analysis.tree != nil {
				t.Fatal("forced fallback fixture unexpectedly retained a concrete tree")
			}
			symbols := swiftTestDefinitionSymbols(analysis.definitions)
			for _, name := range names {
				if !slices.Contains(symbols, name) {
					t.Errorf("Swift identifier head %U lost definition %q: %#v",
						[]rune(name)[0], name, analysis.definitions)
				}
			}
			if slices.Contains(symbols, "accent") || slices.Contains(symbols, "superscript") ||
				slices.Contains(symbols, "zeroWidth") || slices.Contains(symbols, "interiorBOM") {
				t.Errorf("identifier head was split from its suffix: %#v", analysis.definitions)
			}
			swiftTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
		})
	}
}

func TestSwiftCombiningCustomOperatorSurvivesConcreteAndFallback(t *testing.T) {
	t.Parallel()

	const operator = "+\u0300"
	body := "infix operator " + operator + "\n" +
		"func " + operator + " (lhs: Int, rhs: Int) -> Int { lhs + rhs }\n" +
		"func apply(lhs: Int, rhs: Int) -> Int { lhs " + operator + " rhs }\n"
	lexed := lexSwift(body)
	if !lexed.concreteEligible {
		t.Fatal("small combining-operator fixture is not concrete-eligible")
	}
	tree, ok := parseSwiftSyntax(body, lexed)
	if !ok || !validateSwiftSyntaxTree(tree, len(body)) {
		t.Fatal("combining-operator fixture did not produce a validated concrete tree")
	}
	if spans := swiftSyntaxErrorSpans(tree, len(body)); len(spans) != 0 {
		t.Fatalf("pinned grammar rejected legal combining operator: %#v", spans)
	}

	hashes := strings.Repeat("#", swiftMaximumConcreteRawDelimiterHashes+1)
	fallbackPrefix := "let opaque = " + hashes + `"literal"` + hashes + "\n"
	for _, test := range []struct {
		name           string
		prefix         string
		wantTreeAbsent bool
	}{
		{name: "concrete"},
		{name: "forced lexical fallback", prefix: fallbackPrefix, wantTreeAbsent: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			source := test.prefix + body
			lines := swiftTestLines(source)
			parsedSource := strings.TrimSuffix(source, "\n")
			analysis := analyzeSwiftSource(parsedSource, len(lines))
			if test.wantTreeAbsent && analysis.tree != nil {
				t.Fatal("forced fallback fixture unexpectedly retained a concrete tree")
			}
			definitionCount := 0
			for _, definition := range analysis.definitions {
				if definition.symbol == operator {
					definitionCount++
				}
			}
			if definitionCount != 2 {
				t.Errorf("combining operator definition count = %d, want 2: %#v",
					definitionCount, analysis.definitions)
			}
			if slices.Contains(swiftTestDefinitionSymbols(analysis.definitions), "+") {
				t.Errorf("combining operator split into bare +: %#v", analysis.definitions)
			}

			backend := prepareLanguageBackend(newSwiftLanguage(), lines)
			masked := backend.searchLines(lines, true, true)
			useLine := swiftTestLineContaining(t, lines, "func apply")
			if got := backend.(symbolOccurrenceCounter).countSymbolOccurrences(
				masked[useLine-1], operator,
			); got != 1 {
				t.Errorf("combining operator occurrences = %d, want 1: %q",
					got, masked[useLine-1])
			}
			swiftTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
		})
	}
}

func TestSwiftCustomOperatorHeadRangesAndDotPlacement(t *testing.T) {
	t.Parallel()

	validOperators := []string{"\u2016", "\u2020", "\u3001", ".+."}
	identifierOnly := []string{"\u203f", "\u3004"}
	invalid := []string{"\u2018", "\u2028", "+."}
	var validOnly strings.Builder
	for _, symbol := range validOperators {
		fmt.Fprintf(&validOnly, "func %s () {}\n", symbol)
	}
	lexed := lexSwift(validOnly.String())
	tree, ok := parseSwiftSyntax(validOnly.String(), lexed)
	if !ok || !validateSwiftSyntaxTree(tree, validOnly.Len()) {
		t.Fatal("official custom-operator fixture did not produce a validated tree")
	}
	if spans := swiftSyntaxErrorSpans(tree, validOnly.Len()); len(spans) != 0 {
		t.Fatalf("pinned grammar rejected official custom operators: %#v", spans)
	}

	var body strings.Builder
	body.WriteString(validOnly.String())
	for _, symbol := range identifierOnly {
		fmt.Fprintf(&body, "func %s () {}\n", symbol)
	}
	for _, symbol := range invalid {
		fmt.Fprintf(&body, "func %s () {}\n", symbol)
	}
	for _, symbol := range identifierOnly {
		fmt.Fprintf(&body, "infix operator %s\n", symbol)
	}
	body.WriteString("struct OperatorTail {}\n")
	hashes := strings.Repeat("#", swiftMaximumConcreteRawDelimiterHashes+1)
	fallbackPrefix := "let opaque = " + hashes + `"literal"` + hashes + "\n"
	for _, test := range []struct {
		name           string
		prefix         string
		wantTreeAbsent bool
	}{
		{name: "concrete eligible"},
		{name: "forced lexical fallback", prefix: fallbackPrefix, wantTreeAbsent: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			source := test.prefix + body.String()
			lines := swiftTestLines(source)
			analysis := analyzeSwiftSource(strings.TrimSuffix(source, "\n"), len(lines))
			if test.wantTreeAbsent && analysis.tree != nil {
				t.Fatal("forced fallback fixture unexpectedly retained a concrete tree")
			}
			counts := make(map[string]int)
			for _, definition := range analysis.definitions {
				counts[definition.symbol]++
			}
			for _, symbol := range append(append([]string(nil), validOperators...), identifierOnly...) {
				if counts[symbol] != 1 {
					t.Errorf("valid function name %q definition count = %d, want 1: %#v",
						symbol, counts[symbol], analysis.definitions)
				}
			}
			for _, symbol := range append(append([]string(nil), invalid...), "+") {
				if counts[symbol] != 0 {
					t.Errorf("invalid operator/prefix %q became a definition: %#v",
						symbol, analysis.definitions)
				}
			}

			counter := newSwiftLanguage()
			for _, symbol := range invalid {
				if got := counter.countSymbolOccurrences("lhs "+symbol+" rhs", symbol); got != 0 {
					t.Errorf("invalid operator %q occurrence count = %d, want 0", symbol, got)
				}
			}
			for _, symbol := range identifierOnly {
				if got := counter.countSymbolOccurrences("lhs "+symbol+" rhs", symbol); got != 1 {
					t.Errorf("legal identifier %q occurrence count = %d, want 1", symbol, got)
				}
			}
			swiftTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
		})
	}
}

func TestSwiftCommaSeparatedEnumRawCasesSurviveConcreteAndFallback(t *testing.T) {
	t.Parallel()

	const body = `enum Direction: Int {
    case north = 1, south = 2
    case east = DefaultEast, west = Factory.make()
}
struct EnumTail {}
`
	hashes := strings.Repeat("#", swiftMaximumConcreteRawDelimiterHashes+1)
	fallbackPrefix := "let opaque = " + hashes + `"literal"` + hashes + "\n"
	for _, test := range []struct {
		name           string
		prefix         string
		wantTreeAbsent bool
	}{
		{name: "concrete eligible"},
		{name: "forced lexical fallback", prefix: fallbackPrefix, wantTreeAbsent: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			source := test.prefix + body
			lexed := lexSwift(source)
			if got := !lexed.concreteEligible; got != test.wantTreeAbsent {
				t.Fatalf("concrete eligibility = %t, want %t", !got, !test.wantTreeAbsent)
			}
			lines := swiftTestLines(source)
			analysis := analyzeSwiftSource(strings.TrimSuffix(source, "\n"), len(lines))
			if test.wantTreeAbsent && analysis.tree != nil {
				t.Fatal("forced fallback fixture unexpectedly retained a concrete tree")
			}
			symbols := swiftTestDefinitionSymbols(analysis.definitions)
			for _, required := range []string{
				"Direction", "north", "south", "east", "west", "EnumTail",
			} {
				if !slices.Contains(symbols, required) {
					t.Errorf("comma-separated enum case lost %q: %#v",
						required, analysis.definitions)
				}
			}
			for _, expression := range []string{"DefaultEast", "Factory", "make"} {
				if slices.Contains(symbols, expression) {
					t.Errorf("raw-value expression %q became a definition: %#v",
						expression, analysis.definitions)
				}
			}
			swiftTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
		})
	}
}

func TestSwiftLexicalFallbackSeparatesComparisonAndGenericPropertyBindings(t *testing.T) {
	t.Parallel()

	hashes := strings.Repeat("#", swiftMaximumConcreteRawDelimiterHashes+1)
	source := "let opaque = " + hashes + `"literal"` + hashes + `
let first = x < y, second = 2
let third: Box<Item> = make(), fourth = 4
let (left, right) = pair()
struct BindingTail {}
`
	lines := swiftTestLines(source)
	analysis := analyzeSwiftSource(strings.TrimSuffix(source, "\n"), len(lines))
	if analysis.tree != nil {
		t.Fatal("over-hash fixture unexpectedly retained a concrete syntax tree")
	}
	want := []string{"opaque", "first", "second", "third", "fourth", "BindingTail"}
	if got := swiftTestDefinitionSymbols(analysis.definitions); !slices.Equal(got, want) {
		t.Fatalf("comparison/generic binding definitions = %#v, want %#v", got, want)
	}
	for _, expression := range []string{
		"x", "y", "Box", "Item", "make", "left", "right", "pair",
	} {
		if slices.Contains(swiftTestDefinitionSymbols(analysis.definitions), expression) {
			t.Errorf("binding expression/pattern %q became a definition: %#v",
				expression, analysis.definitions)
		}
	}
	swiftTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
}

func TestSwiftExtensionsRequireWellFormedQualifiedTargets(t *testing.T) {
	t.Parallel()

	const validBody = `extension Outer.Inner {
    func nested() {}
}
struct ExtensionTail {}
`
	malformed := []struct {
		name        string
		declaration string
	}{
		{name: "leading dot", declaration: "extension .Foo {}"},
		{name: "trailing dot", declaration: "extension Foo. {}"},
		{name: "leading colon", declaration: "extension :Foo {}"},
		{name: "trailing colon", declaration: "extension Foo: {}"},
		{name: "repeated dots", declaration: "extension Foo..Bar {}"},
		{name: "repeated module separators", declaration: "extension Foo::::Bar {}"},
		{name: "mixed repeated separators", declaration: "extension Foo.::Bar {}"},
	}
	hashes := strings.Repeat("#", swiftMaximumConcreteRawDelimiterHashes+1)
	fallbackPrefix := "let opaque = " + hashes + `"literal"` + hashes + "\n"
	modes := []struct {
		name           string
		prefix         string
		wantTreeAbsent bool
	}{
		{name: "concrete eligible"},
		{name: "forced lexical fallback", prefix: fallbackPrefix, wantTreeAbsent: true},
	}

	for _, mode := range modes {
		t.Run("valid/"+mode.name, func(t *testing.T) {
			t.Parallel()

			source := mode.prefix + validBody
			lines := swiftTestLines(source)
			analysis := analyzeSwiftSource(strings.TrimSuffix(source, "\n"), len(lines))
			if mode.wantTreeAbsent && analysis.tree != nil {
				t.Fatal("forced fallback fixture unexpectedly retained a concrete tree")
			}
			for _, required := range []string{"Outer.Inner", "nested", "ExtensionTail"} {
				if !slices.Contains(swiftTestDefinitionSymbols(analysis.definitions), required) {
					t.Errorf("valid qualified extension lost %q: %#v",
						required, analysis.definitions)
				}
			}
			swiftTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
		})

		for _, malformedCase := range malformed {
			t.Run(malformedCase.name+"/"+mode.name, func(t *testing.T) {
				t.Parallel()

				source := mode.prefix + malformedCase.declaration +
					"\nstruct ExtensionTail {}\n"
				lines := swiftTestLines(source)
				analysis := analyzeSwiftSource(strings.TrimSuffix(source, "\n"), len(lines))
				if mode.wantTreeAbsent && analysis.tree != nil {
					t.Fatal("forced fallback fixture unexpectedly retained a concrete tree")
				}
				want := []string{"ExtensionTail"}
				if mode.prefix != "" {
					want = append([]string{"opaque"}, want...)
				}
				if got := swiftTestDefinitionSymbols(analysis.definitions); !slices.Equal(got, want) {
					t.Errorf("malformed extension definitions = %#v, want %#v",
						got, want)
				}
				swiftTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
			})
		}
	}
}

func TestSwiftInlineCommentUnicodeSeparatorsDoNotCreateDeclarationBoundaries(t *testing.T) {
	t.Parallel()

	const body = "foo /* next-line separator \u0085 */ struct PhantomNEL { func hiddenNEL() {} }\n" +
		"foo /* line separator \u2028 */ struct PhantomLS { func hiddenLS() {} }\n" +
		"foo /* paragraph separator \u2029 */ struct PhantomPS { func hiddenPS() {} }\n" +
		"let value = 1 \u2028 /// trailing documentation lookalike\n" +
		"struct CommentBoundaryTail { func visible() {} }\n"
	hashes := strings.Repeat("#", swiftMaximumConcreteRawDelimiterHashes+1)
	fallbackPrefix := "let opaque = " + hashes + `"literal"` + hashes + "\n"
	for _, test := range []struct {
		name           string
		prefix         string
		wantTreeAbsent bool
	}{
		{name: "concrete"},
		{name: "forced lexical fallback", prefix: fallbackPrefix, wantTreeAbsent: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			source := test.prefix + body
			lines := swiftTestLines(source)
			analysis := analyzeSwiftSource(strings.TrimSuffix(source, "\n"), len(lines))
			if test.wantTreeAbsent && analysis.tree != nil {
				t.Fatal("forced fallback fixture unexpectedly retained a concrete tree")
			} else if !test.wantTreeAbsent && analysis.tree == nil {
				t.Fatal("small Unicode-comment fixture did not retain a concrete tree")
			}
			symbols := swiftTestDefinitionSymbols(analysis.definitions)
			for _, required := range []string{"CommentBoundaryTail", "visible"} {
				if !slices.Contains(symbols, required) {
					t.Errorf("Unicode-comment recovery lost %q: %#v", required, analysis.definitions)
				}
			}
			for _, forbidden := range []string{
				"PhantomNEL", "hiddenNEL", "PhantomLS", "hiddenLS", "PhantomPS", "hiddenPS",
			} {
				if slices.Contains(symbols, forbidden) {
					t.Errorf("Unicode separator inside inline comment promoted %q: %#v",
						forbidden, analysis.definitions)
				}
			}
			tail := swiftTestFirstDefinition(t, analysis.definitions, "CommentBoundaryTail")
			if !tail.ownsScope || tail.scopeStart != tail.line {
				t.Errorf("Unicode-separated trailing KDoc attached to later declaration: %#v", tail)
			}
			swiftTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
		})
	}
}

func TestSwiftDocumentationLineLeadingControlsConcreteAndFallback(t *testing.T) {
	t.Parallel()

	controls := []struct {
		name         string
		prefix       string
		wantAttached bool
	}{
		{name: "vertical tab", prefix: "\v", wantAttached: true},
		{name: "form feed", prefix: "\f", wantAttached: true},
		{name: "NUL", prefix: "\x00", wantAttached: true},
		{name: "next-line separator", prefix: "\u0085"},
		{name: "line separator", prefix: "\u2028"},
		{name: "paragraph separator", prefix: "\u2029"},
	}
	hashes := strings.Repeat("#", swiftMaximumConcreteRawDelimiterHashes+1)
	fallbackPrefix := "let opaque = " + hashes + `"literal"` + hashes + "\n"
	for _, control := range controls {
		for _, mode := range []struct {
			name           string
			prefix         string
			wantTreeAbsent bool
		}{
			{name: "concrete"},
			{name: "forced lexical fallback", prefix: fallbackPrefix, wantTreeAbsent: true},
		} {
			t.Run(control.name+"/"+mode.name, func(t *testing.T) {
				t.Parallel()

				source := mode.prefix + control.prefix +
					"/// control documentation\nstruct Attached {}\n"
				lines := swiftTestLines(source)
				analysis := analyzeSwiftSource(strings.TrimSuffix(source, "\n"), len(lines))
				if mode.wantTreeAbsent && analysis.tree != nil {
					t.Fatal("forced fallback fixture unexpectedly retained a concrete tree")
				} else if !mode.wantTreeAbsent && analysis.tree == nil {
					t.Fatal("small documentation-control fixture did not retain a concrete tree")
				}
				attached := swiftTestFirstDefinition(t, analysis.definitions, "Attached")
				wantStart := attached.line
				if control.wantAttached {
					wantStart--
				}
				if !attached.ownsScope || attached.scopeStart != wantStart ||
					attached.scopeEnd != attached.line {
					t.Errorf("documentation after %s = %#v, want owning scope %d-%d",
						control.name, attached, wantStart, attached.line)
				}
				swiftTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
			})
		}
	}
}

func TestSwiftBlockDocumentationGapControlsConcreteAndFallback(t *testing.T) {
	t.Parallel()

	gaps := []struct {
		name         string
		gap          string
		wantAttached bool
	}{
		{name: "space", gap: " ", wantAttached: true},
		{name: "tab", gap: "\t", wantAttached: true},
		{name: "vertical tab", gap: "\v", wantAttached: true},
		{name: "form feed", gap: "\f", wantAttached: true},
		{name: "NUL", gap: "\x00", wantAttached: true},
		{name: "next-line separator", gap: "\u0085"},
		{name: "line separator", gap: "\u2028"},
		{name: "paragraph separator", gap: "\u2029"},
	}
	hashes := strings.Repeat("#", swiftMaximumConcreteRawDelimiterHashes+1)
	fallbackPrefix := "let opaque = " + hashes + `"literal"` + hashes + "\n"
	for _, gap := range gaps {
		for _, mode := range []struct {
			name           string
			prefix         string
			wantTreeAbsent bool
		}{
			{name: "concrete"},
			{name: "forced lexical fallback", prefix: fallbackPrefix, wantTreeAbsent: true},
		} {
			t.Run(gap.name+"/"+mode.name, func(t *testing.T) {
				t.Parallel()

				source := mode.prefix + "/** block documentation */" + gap.gap +
					"\nstruct BlockGapAttached {}\n"
				lines := swiftTestLines(source)
				analysis := analyzeSwiftSource(strings.TrimSuffix(source, "\n"), len(lines))
				if mode.wantTreeAbsent && analysis.tree != nil {
					t.Fatal("forced fallback fixture unexpectedly retained a concrete tree")
				} else if !mode.wantTreeAbsent && analysis.tree == nil {
					t.Fatal("small block-documentation fixture did not retain a concrete tree")
				}
				attached := swiftTestFirstDefinition(t, analysis.definitions, "BlockGapAttached")
				wantStart := attached.line
				if gap.wantAttached {
					wantStart--
				}
				if !attached.ownsScope || attached.scopeStart != wantStart ||
					attached.scopeEnd != attached.line {
					t.Errorf("block documentation across %s gap = %#v, want owning scope %d-%d",
						gap.name, attached, wantStart, attached.line)
				}
				swiftTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
			})
		}
	}
}

func TestSwiftExactWhitespaceControlsDeclarationAndDirectiveBoundaries(t *testing.T) {
	t.Parallel()

	const body = "\x00struct NulLeading {}\n" +
		"\u0085struct PhantomNEL {}\n" +
		"\u2028struct PhantomLS {}\n" +
		"\u2029struct PhantomPS {}\n" +
		"\v#if FEATURE\nstruct VTDirective {}\n#endif\n" +
		"\f#if FEATURE\nstruct FFDirective {}\n#endif\n" +
		"\x00#if FEATURE\nstruct NULDirective {}\n#endif\n" +
		"struct ExactWhitespaceTail {}\n"
	hashes := strings.Repeat("#", swiftMaximumConcreteRawDelimiterHashes+1)
	fallbackPrefix := "let opaque = " + hashes + `"literal"` + hashes + "\n"
	for _, mode := range []struct {
		name           string
		prefix         string
		wantTreeAbsent bool
	}{
		{name: "concrete"},
		{name: "forced lexical fallback", prefix: fallbackPrefix, wantTreeAbsent: true},
	} {
		t.Run(mode.name, func(t *testing.T) {
			t.Parallel()

			source := mode.prefix + body
			lexed := lexSwift(source)
			directives := 0
			for _, token := range lexed.tokens {
				if token.text == "#if" {
					directives++
				}
			}
			if directives != 3 {
				t.Errorf("VT/FF/NUL line-leading #if token count = %d, want 3", directives)
			}
			lines := swiftTestLines(source)
			analysis := analyzeSwiftSource(strings.TrimSuffix(source, "\n"), len(lines))
			if mode.wantTreeAbsent && analysis.tree != nil {
				t.Fatal("forced fallback fixture unexpectedly retained a concrete tree")
			} else if !mode.wantTreeAbsent && analysis.tree == nil {
				t.Fatal("small exact-whitespace fixture did not retain a concrete tree")
			}
			symbols := swiftTestDefinitionSymbols(analysis.definitions)
			for _, required := range []string{
				"NulLeading", "VTDirective", "FFDirective", "NULDirective", "ExactWhitespaceTail",
			} {
				if !slices.Contains(symbols, required) {
					t.Errorf("exact Swift whitespace recovery lost %q: %#v",
						required, analysis.definitions)
				}
			}
			for _, forbidden := range []string{"PhantomNEL", "PhantomLS", "PhantomPS"} {
				if slices.Contains(symbols, forbidden) {
					t.Errorf("non-Swift Unicode separator promoted %q: %#v",
						forbidden, analysis.definitions)
				}
			}
			swiftTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
		})
	}
}

func TestSwiftExactWhitespaceOperatorAndQualifiedTypeNamesConcreteAndFallback(t *testing.T) {
	t.Parallel()

	const acceptedBody = "infix operator\v=~\n" +
		"func\f+++ (_ value: Int) -> Int { value }\n" +
		"extension Outer\v.\fInner {\n    func whitespaceMember() {}\n}\n"
	const nulQualifiedBody = "extension NulOuter\x00.\x00Inner {\n" +
		"    func nulWhitespaceMember() {}\n}\n"
	const tail = "struct ExactTreeWhitespaceTail {}\n"
	directSource := acceptedBody + tail
	directLines := swiftTestLines(directSource)
	directLexed := lexSwift(directSource)
	if !directLexed.concreteEligible {
		t.Fatal("small exact-whitespace direct-tree fixture is not concrete-eligible")
	}
	directTree, ok := parseSwiftSyntax(directSource, directLexed)
	if !ok || !validateSwiftSyntaxTree(directTree, len(directSource)) {
		t.Fatal("exact-whitespace fixture did not produce a validated concrete tree")
	}
	if spans := swiftSyntaxErrorSpans(directTree, len(directSource)); len(spans) != 0 {
		t.Fatalf("grammar-accepted exact-whitespace fixture recovery spans = %#v, want none", spans)
	}
	directDefinitions := swiftTreeDefinitions(directSource, len(directLines), directTree)
	directWant := []string{"=~", "+++", "Outer.Inner", "whitespaceMember", "ExactTreeWhitespaceTail"}
	if got := swiftTestDefinitionSymbols(directDefinitions); !slices.Equal(got, directWant) {
		t.Fatalf("direct-tree exact-whitespace definitions = %#v, want %#v", got, directWant)
	}
	directCoordinateDefinitions := make([]sourceDefinition, 0, len(directDefinitions)-1)
	for _, definition := range directDefinitions {
		if definition.symbol != "Outer.Inner" {
			directCoordinateDefinitions = append(directCoordinateDefinitions, definition)
		}
	}
	swiftTestAssertDefinitionCoordinates(t, directLines, directCoordinateDefinitions)
	qualified := swiftTestFirstDefinition(t, directDefinitions, "Outer.Inner")
	if wantColumn := strings.Index(directLines[qualified.line-1], "Outer") + 1; qualified.column != wantColumn {
		t.Errorf("direct-tree qualified extension column = %d, want %d: %#v",
			qualified.column, wantColumn, qualified)
	}

	body := acceptedBody + nulQualifiedBody + tail
	hashes := strings.Repeat("#", swiftMaximumConcreteRawDelimiterHashes+1)
	fallbackPrefix := "let opaque = " + hashes + `"literal"` + hashes + "\n"
	for _, mode := range []struct {
		name           string
		prefix         string
		wantTreeAbsent bool
	}{
		{name: "concrete with lexical recovery for NUL"},
		{name: "forced lexical fallback", prefix: fallbackPrefix, wantTreeAbsent: true},
	} {
		t.Run(mode.name, func(t *testing.T) {
			t.Parallel()

			source := mode.prefix + body
			lines := swiftTestLines(source)
			analysis := analyzeSwiftSource(strings.TrimSuffix(source, "\n"), len(lines))
			if mode.wantTreeAbsent && analysis.tree != nil {
				t.Fatal("forced fallback fixture unexpectedly retained a concrete tree")
			} else if !mode.wantTreeAbsent && analysis.tree == nil {
				t.Fatal("small exact-whitespace fixture did not retain a concrete tree")
			}
			symbols := swiftTestDefinitionSymbols(analysis.definitions)
			for _, required := range []string{
				"=~", "+++", "Outer.Inner", "whitespaceMember", "NulOuter.Inner",
				"nulWhitespaceMember", "ExactTreeWhitespaceTail",
			} {
				if !slices.Contains(symbols, required) {
					t.Errorf("exact-whitespace analysis lost %q: %#v", required, analysis.definitions)
				}
			}
			for _, fragment := range []string{"Outer", "Inner", "NulOuter"} {
				if slices.Contains(symbols, fragment) {
					t.Errorf("qualified user type split out %q: %#v", fragment, analysis.definitions)
				}
			}
			coordinateDefinitions := make([]sourceDefinition, 0, len(analysis.definitions)-2)
			for _, definition := range analysis.definitions {
				if definition.symbol != "Outer.Inner" && definition.symbol != "NulOuter.Inner" {
					coordinateDefinitions = append(coordinateDefinitions, definition)
				}
			}
			swiftTestAssertDefinitionCoordinates(t, lines, coordinateDefinitions)
			for _, symbol := range []string{"Outer.Inner", "NulOuter.Inner"} {
				definition := swiftTestFirstDefinition(t, analysis.definitions, symbol)
				head := strings.SplitN(symbol, ".", 2)[0]
				if wantColumn := strings.Index(lines[definition.line-1], head) + 1; definition.column != wantColumn {
					t.Errorf("qualified extension %q column = %d, want %d: %#v",
						symbol, definition.column, wantColumn, definition)
				}
			}
		})
	}
}

func TestSwiftMalformedNominalAndFunctionHeadersSuppressContainersAndChildren(t *testing.T) {
	t.Parallel()

	malformed := []struct {
		name        string
		declaration string
		container   string
	}{
		{name: "unclosed generic", declaration: "struct Broken<T { func hidden() {} }", container: "Broken"},
		{name: "unclosed nominal paren", declaration: "struct Broken( { func hidden() {} }", container: "Broken"},
		{name: "unclosed function generic paren", declaration: "func broken<T( { func hidden() {} }", container: "broken"},
		{name: "function missing parameter before default", declaration: "func broken(= { func hidden() {} })", container: "broken"},
		{name: "function missing parameter type", declaration: "func broken(x: = { func hidden() {} })", container: "broken"},
		{name: "property unclosed paren", declaration: "var broken( { func hidden() {} }", container: "broken"},
		{name: "import unclosed paren", declaration: "import Foo( { func hidden() {} }", container: "Foo"},
		{name: "empty inheritance", declaration: "struct Broken: { func hidden() {} }", container: "Broken"},
		{name: "empty where clause", declaration: "struct Broken where { func hidden() {} }", container: "Broken"},
		{name: "stray comma", declaration: "struct Broken, { func hidden() {} }", container: "Broken"},
		{name: "unexpected suffix", declaration: "struct Broken nonsense { func hidden() {} }", container: "Broken"},
		{name: "trailing inheritance comma", declaration: "struct Broken: P, { func hidden() {} }", container: "Broken"},
		{name: "incomplete where constraint", declaration: "struct Broken<T> where T: { func hidden() {} }", container: "Broken"},
	}
	hashes := strings.Repeat("#", swiftMaximumConcreteRawDelimiterHashes+1)
	fallbackPrefix := "let opaque = " + hashes + `"literal"` + hashes + "\n"
	for _, test := range malformed {
		body := test.declaration + "\nstruct MalformedHeaderTail { func recovered() {} }\n"
		t.Run(test.name+"/direct concrete tree", func(t *testing.T) {
			t.Parallel()

			lexed := lexSwift(body)
			lexed.concreteEligible = true
			tree, ok := parseSwiftSyntax(body, lexed)
			if !ok || !validateSwiftSyntaxTree(tree, len(body)) {
				t.Fatal("malformed header did not produce a validated recovery tree")
			}
			definitions := swiftTreeDefinitions(body, len(swiftTestLines(body)), tree)
			symbols := swiftTestDefinitionSymbols(definitions)
			for _, required := range []string{"MalformedHeaderTail", "recovered"} {
				if !slices.Contains(symbols, required) {
					t.Errorf("concrete malformed-header recovery lost %q: %#v", required, definitions)
				}
			}
			for _, forbidden := range []string{test.container, "hidden"} {
				if slices.Contains(symbols, forbidden) {
					t.Errorf("concrete malformed header promoted %q: %#v", forbidden, definitions)
				}
			}
			swiftTestAssertDefinitionCoordinates(t, swiftTestLines(body), definitions)
		})

		t.Run(test.name+"/forced lexical fallback", func(t *testing.T) {
			t.Parallel()

			source := fallbackPrefix + body
			lines := swiftTestLines(source)
			analysis := analyzeSwiftSource(strings.TrimSuffix(source, "\n"), len(lines))
			if analysis.tree != nil {
				t.Fatal("forced fallback fixture unexpectedly retained a concrete tree")
			}
			symbols := swiftTestDefinitionSymbols(analysis.definitions)
			for _, required := range []string{"MalformedHeaderTail", "recovered"} {
				if !slices.Contains(symbols, required) {
					t.Errorf("fallback malformed-header recovery lost %q: %#v",
						required, analysis.definitions)
				}
			}
			for _, forbidden := range []string{test.container, "hidden"} {
				if slices.Contains(symbols, forbidden) {
					t.Errorf("fallback malformed header promoted %q: %#v",
						forbidden, analysis.definitions)
				}
			}
			swiftTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
		})
	}
}

func TestSwiftValidNominalSuffixesSurviveConcreteAndFallback(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		declaration string
		owner       string
	}{
		{
			name:        "generic inheritance and where",
			declaration: "struct Good<T>: P, Q where T: R { func visible() {} }",
			owner:       "Good",
		},
		{
			name:        "trailing generic comma",
			declaration: "struct Good<T,> { func visible() {} }",
			owner:       "Good",
		},
		{
			name:        "trailing where comma",
			declaration: "struct Good<T> where T: R, { func visible() {} }",
			owner:       "Good",
		},
		{
			name:        "tuple equality constraint",
			declaration: "struct Good<T> where T == (Int, Int) { func visible() {} }",
			owner:       "Good",
		},
		{
			name:        "protocol composition inheritance",
			declaration: "struct Good: P & Q { func visible() {} }",
			owner:       "Good",
		},
		{
			name:        "unchecked conformance",
			declaration: "struct Good: @unchecked Sendable { func visible() {} }",
			owner:       "Good",
		},
		{
			name:        "primary associated type protocol",
			declaration: "protocol Good<T>: Q where T: R { func visible() }",
			owner:       "Good",
		},
		{
			name:        "retroactive qualified extension",
			declaration: "extension Module.Type: @retroactive P, Sendable { func visible() {} }",
			owner:       "Module.Type",
		},
	}
	hashes := strings.Repeat("#", swiftMaximumConcreteRawDelimiterHashes+1)
	fallbackPrefix := "let opaque = " + hashes + `"literal"` + hashes + "\n"
	for _, test := range tests {
		body := test.declaration + "\nstruct ValidNominalTail {}\n"
		for _, mode := range []struct {
			name           string
			prefix         string
			wantTreeAbsent bool
		}{
			{name: "concrete"},
			{name: "forced lexical fallback", prefix: fallbackPrefix, wantTreeAbsent: true},
		} {
			t.Run(test.name+"/"+mode.name, func(t *testing.T) {
				t.Parallel()

				source := mode.prefix + body
				lines := swiftTestLines(source)
				analysis := analyzeSwiftSource(strings.TrimSuffix(source, "\n"), len(lines))
				if mode.wantTreeAbsent && analysis.tree != nil {
					t.Fatal("forced fallback fixture unexpectedly retained a concrete tree")
				} else if !mode.wantTreeAbsent && analysis.tree == nil {
					t.Fatal("valid nominal suffix fixture did not retain a concrete tree")
				}
				symbols := swiftTestDefinitionSymbols(analysis.definitions)
				for _, required := range []string{test.owner, "visible", "ValidNominalTail"} {
					if !slices.Contains(symbols, required) {
						t.Errorf("valid nominal suffix lost %q: %#v", required, analysis.definitions)
					}
				}
				for _, forbidden := range []string{"T", "P", "Q", "R", "Int", "Sendable", "retroactive"} {
					if slices.Contains(symbols, forbidden) {
						t.Errorf("nominal suffix component %q became a definition: %#v",
							forbidden, analysis.definitions)
					}
				}
				swiftTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
			})
		}
	}
}

func TestSwiftDeclarationSuffixesRespectPinnedGrammarConcreteAndFallback(t *testing.T) {
	t.Parallel()

	malformed := []struct {
		name         string
		declaration  string
		required     []string
		forbidden    []string
		rejectImport bool
	}{
		{
			name: "initializer suffix", declaration: "class C { init nonsense { func hidden() {} } }",
			required: []string{"C"}, forbidden: []string{"init", "hidden"},
		},
		{
			name: "deinitializer suffix", declaration: "class C { deinit nonsense { func hidden() {} } }",
			required: []string{"C"}, forbidden: []string{"deinit", "hidden"},
		},
		{
			name: "subscript suffix", declaration: "struct C { subscript nonsense { func hidden() {} } }",
			required: []string{"C"}, forbidden: []string{"subscript", "hidden"},
		},
		{
			name: "associated type suffix", declaration: "protocol P { associatedtype Broken nonsense }",
			required: []string{"P"}, forbidden: []string{"Broken"},
		},
		{
			name: "type alias suffix", declaration: "typealias Broken nonsense",
			forbidden: []string{"Broken"},
		},
		{
			name: "type alias missing RHS", declaration: "typealias MissingValue",
			forbidden: []string{"MissingValue"},
		},
		{
			name:        "type alias empty RHS block",
			declaration: "typealias Broken = { func hidden() {} }",
			forbidden:   []string{"Broken", "hidden"},
		},
		{
			name: "macro suffix", declaration: "macro broken nonsense { func hidden() {} }",
			forbidden: []string{"broken", "hidden"},
		},
		{
			name: "precedence group suffix", declaration: "precedencegroup Broken nonsense { func hidden() {} }",
			forbidden: []string{"Broken", "hidden"},
		},
		{
			name: "operator suffix", declaration: "prefix operator + nonsense",
			forbidden: []string{"+"},
		},
		{
			name: "property suffix", declaration: "let broken nonsense",
			forbidden: []string{"broken"},
		},
		{
			name: "import suffix block", declaration: "import Foo { func hidden() {} }",
			forbidden: []string{"hidden"}, rejectImport: true,
		},
		{
			name:        "deinitializer missing body",
			declaration: "class C { deinit\nfunc sibling() {} }",
			required:    []string{"C", "sibling"}, forbidden: []string{"deinit"},
		},
		{
			name:        "subscript missing body",
			declaration: "struct C { subscript(_ i: Int) -> Int\nfunc sibling() {} }",
			required:    []string{"C", "sibling"},
			forbidden:   []string{"subscript", "i", "Int"},
		},
		{
			name: "precedence group missing body", declaration: "precedencegroup P",
			forbidden: []string{"P"},
		},
	}
	valid := []struct {
		name        string
		declaration string
		required    []string
	}{
		{name: "initializer", declaration: "class ValidInit { init() {} }", required: []string{"ValidInit", "init"}},
		{name: "deinitializer", declaration: "class ValidDeinit { deinit {} }", required: []string{"ValidDeinit", "deinit"}},
		{
			name:        "subscript",
			declaration: "struct ValidSubscript { subscript(_ index: Int) -> Int { index } }",
			required:    []string{"ValidSubscript", "subscript"},
		},
		{
			name:        "failable generic typed-throwing initializer",
			declaration: "class ValidComplexInit { init?<T>(_ x: T) async throws(E) where T: P { func initHelper() {} } }",
			required:    []string{"ValidComplexInit", "init", "initHelper"},
		},
		{
			name:        "associated types",
			declaration: "protocol ValidAssociated { associatedtype A; associatedtype B: Q; associatedtype C = Int }",
			required:    []string{"ValidAssociated", "A", "B", "C"},
		},
		{name: "type alias", declaration: "typealias ValidAlias<T,> = Array<T>", required: []string{"ValidAlias"}},
		{
			name: "macro closure", declaration: "macro ValidMacro() = { func visibleMacro() {} }",
			required: []string{"ValidMacro", "visibleMacro"},
		},
		{
			name:        "precedence group",
			declaration: "precedencegroup ValidPrecedence {\nassociativity: left\nhigherThan: AdditionPrecedence\n}",
			required:    []string{"ValidPrecedence"},
		},
		{name: "operator", declaration: "infix operator <+>: ValidPrecedence", required: []string{"<+>"}},
		{name: "property", declaration: "let validProperty = 1", required: []string{"validProperty"}},
	}

	hashes := strings.Repeat("#", swiftMaximumConcreteRawDelimiterHashes+1)
	fallbackPrefix := "let opaque = " + hashes + `"literal"` + hashes + "\n"
	modes := []struct {
		name           string
		prefix         string
		wantTreeAbsent bool
	}{
		{name: "concrete"},
		{name: "forced lexical fallback", prefix: fallbackPrefix, wantTreeAbsent: true},
	}

	for _, malformedCase := range malformed {
		for _, mode := range modes {
			t.Run("malformed/"+malformedCase.name+"/"+mode.name, func(t *testing.T) {
				t.Parallel()

				source := mode.prefix + malformedCase.declaration + "\nfunc suffixTail() {}\n"
				parsedSource := strings.TrimSuffix(source, "\n")
				lines := swiftTestLines(source)
				analysis := analyzeSwiftSource(parsedSource, len(lines))
				if mode.wantTreeAbsent && analysis.tree != nil {
					t.Fatal("forced fallback fixture unexpectedly retained a concrete tree")
				} else if !mode.wantTreeAbsent {
					if analysis.tree == nil {
						t.Fatal("small malformed declaration fixture did not retain a concrete tree")
					}
					if spans := swiftSyntaxErrorSpans(analysis.tree, len(parsedSource)); len(spans) == 0 {
						t.Fatal("pinned grammar unexpectedly accepted malformed declaration")
					}
				}
				symbols := swiftTestDefinitionSymbols(analysis.definitions)
				requiredSymbols := append([]string(nil), malformedCase.required...)
				requiredSymbols = append(requiredSymbols, "suffixTail")
				for _, required := range requiredSymbols {
					if !slices.Contains(symbols, required) {
						t.Errorf("malformed declaration lost %q: %#v", required, analysis.definitions)
					}
				}
				for _, forbidden := range malformedCase.forbidden {
					if slices.Contains(symbols, forbidden) {
						t.Errorf("malformed declaration promoted %q: %#v", forbidden, analysis.definitions)
					}
				}
				if malformedCase.rejectImport {
					backend := prepareLanguageBackend(newSwiftLanguage(), lines)
					if start, end, ok := backend.importRange(lines); ok {
						t.Errorf("malformed import range = %d-%d, want absent", start, end)
					}
				}
				swiftTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
			})
		}
	}

	for _, validCase := range valid {
		for _, mode := range modes {
			t.Run("valid/"+validCase.name+"/"+mode.name, func(t *testing.T) {
				t.Parallel()

				source := mode.prefix + validCase.declaration + "\nfunc suffixTail() {}\n"
				parsedSource := strings.TrimSuffix(source, "\n")
				lines := swiftTestLines(source)
				analysis := analyzeSwiftSource(parsedSource, len(lines))
				if mode.wantTreeAbsent && analysis.tree != nil {
					t.Fatal("forced fallback fixture unexpectedly retained a concrete tree")
				} else if !mode.wantTreeAbsent {
					if analysis.tree == nil {
						t.Fatal("valid declaration fixture did not retain a concrete tree")
					}
					if spans := swiftSyntaxErrorSpans(analysis.tree, len(parsedSource)); len(spans) != 0 {
						t.Fatalf("valid declaration recovery spans = %#v, want none", spans)
					}
				}
				symbols := swiftTestDefinitionSymbols(analysis.definitions)
				requiredSymbols := append([]string(nil), validCase.required...)
				requiredSymbols = append(requiredSymbols, "suffixTail")
				for _, required := range requiredSymbols {
					if !slices.Contains(symbols, required) {
						t.Errorf("valid declaration lost %q: %#v", required, analysis.definitions)
					}
				}
				swiftTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
			})
		}
	}
}

func TestSwiftMalformedAndValidFunctionSuffixesConcreteAndFallback(t *testing.T) {
	t.Parallel()

	malformed := []struct {
		name        string
		declaration string
	}{
		{name: "generic without parameter clause", declaration: "func broken<T> { func hidden() {} }"},
		{name: "unexpected suffix", declaration: "func broken() nonsense { func hidden() {} }"},
		{name: "missing return type", declaration: "func broken() -> { func hidden() {} }"},
		{name: "empty where clause", declaration: "func broken() where { func hidden() {} }"},
	}
	valid := []struct {
		name        string
		declaration string
	}{
		{name: "trailing generic comma", declaration: "func good<T,>() { func visible() {} }"},
		{name: "trailing where comma", declaration: "func good<T>() where T: P, { func visible() {} }"},
	}
	hashes := strings.Repeat("#", swiftMaximumConcreteRawDelimiterHashes+1)
	fallbackPrefix := "let opaque = " + hashes + `"literal"` + hashes + "\n"
	modes := []struct {
		name           string
		prefix         string
		wantTreeAbsent bool
	}{
		{name: "concrete"},
		{name: "forced lexical fallback", prefix: fallbackPrefix, wantTreeAbsent: true},
	}

	for _, test := range malformed {
		for _, mode := range modes {
			t.Run("malformed/"+test.name+"/"+mode.name, func(t *testing.T) {
				t.Parallel()

				source := mode.prefix + test.declaration +
					"\nstruct FunctionSuffixTail { func recovered() {} }\n"
				lines := swiftTestLines(source)
				analysis := analyzeSwiftSource(strings.TrimSuffix(source, "\n"), len(lines))
				if mode.wantTreeAbsent && analysis.tree != nil {
					t.Fatal("forced fallback fixture unexpectedly retained a concrete tree")
				} else if !mode.wantTreeAbsent && analysis.tree == nil {
					t.Fatal("small malformed-function fixture did not retain a concrete tree")
				}
				symbols := swiftTestDefinitionSymbols(analysis.definitions)
				for _, required := range []string{"FunctionSuffixTail", "recovered"} {
					if !slices.Contains(symbols, required) {
						t.Errorf("malformed function suffix lost %q: %#v", required, analysis.definitions)
					}
				}
				for _, forbidden := range []string{"broken", "hidden"} {
					if slices.Contains(symbols, forbidden) {
						t.Errorf("malformed function suffix promoted %q: %#v",
							forbidden, analysis.definitions)
					}
				}
				swiftTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
			})
		}
	}

	for _, test := range valid {
		for _, mode := range modes {
			t.Run("valid/"+test.name+"/"+mode.name, func(t *testing.T) {
				t.Parallel()

				source := mode.prefix + test.declaration +
					"\nstruct FunctionSuffixTail {}\n"
				lines := swiftTestLines(source)
				parsedSource := strings.TrimSuffix(source, "\n")
				analysis := analyzeSwiftSource(parsedSource, len(lines))
				switch {
				case mode.wantTreeAbsent && analysis.tree != nil:
					t.Fatal("forced fallback fixture unexpectedly retained a concrete tree")
				case !mode.wantTreeAbsent && analysis.tree == nil:
					t.Fatal("valid function suffix fixture did not retain a concrete tree")
				case !mode.wantTreeAbsent:
					if spans := swiftSyntaxErrorSpans(analysis.tree, len(parsedSource)); len(spans) != 0 {
						t.Fatalf("valid function suffix recovery spans = %#v, want none", spans)
					}
				}
				symbols := swiftTestDefinitionSymbols(analysis.definitions)
				for _, required := range []string{"good", "visible", "FunctionSuffixTail"} {
					if !slices.Contains(symbols, required) {
						t.Errorf("valid function suffix lost %q: %#v", required, analysis.definitions)
					}
				}
				for _, forbidden := range []string{"T", "P"} {
					if slices.Contains(symbols, forbidden) {
						t.Errorf("valid function suffix component %q became a definition: %#v",
							forbidden, analysis.definitions)
					}
				}
				swiftTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
			})
		}
	}
}

func TestSwiftFunctionTypeProtocolInheritanceSurvivesConcreteAndFallback(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		declaration string
		required    []string
	}{
		{
			name:        "plain function type",
			declaration: "protocol Callable: (Int) -> Void { func call(_ x: Int) }",
			required:    []string{"Callable", "call"},
		},
		{
			name:        "attributed async throwing function type",
			declaration: "protocol AsyncCallable: @Sendable (Int) async throws -> Void {}",
			required:    []string{"AsyncCallable"},
		},
	}
	hashes := strings.Repeat("#", swiftMaximumConcreteRawDelimiterHashes+1)
	fallbackPrefix := "let opaque = " + hashes + `"literal"` + hashes + "\n"
	for _, test := range tests {
		for _, mode := range []struct {
			name           string
			prefix         string
			wantTreeAbsent bool
		}{
			{name: "concrete"},
			{name: "forced lexical fallback", prefix: fallbackPrefix, wantTreeAbsent: true},
		} {
			t.Run(test.name+"/"+mode.name, func(t *testing.T) {
				t.Parallel()

				source := mode.prefix + test.declaration + "\nstruct FunctionInheritanceTail {}\n"
				parsedSource := strings.TrimSuffix(source, "\n")
				lines := swiftTestLines(source)
				analysis := analyzeSwiftSource(parsedSource, len(lines))
				if mode.wantTreeAbsent && analysis.tree != nil {
					t.Fatal("forced fallback fixture unexpectedly retained a concrete tree")
				} else if !mode.wantTreeAbsent {
					if analysis.tree == nil {
						t.Fatal("valid function-type inheritance fixture did not retain a concrete tree")
					}
					if spans := swiftSyntaxErrorSpans(analysis.tree, len(parsedSource)); len(spans) != 0 {
						t.Fatalf("function-type inheritance recovery spans = %#v, want none", spans)
					}
				}
				symbols := swiftTestDefinitionSymbols(analysis.definitions)
				requiredSymbols := append([]string(nil), test.required...)
				requiredSymbols = append(requiredSymbols, "FunctionInheritanceTail")
				for _, required := range requiredSymbols {
					if !slices.Contains(symbols, required) {
						t.Errorf("function-type inheritance lost %q: %#v", required, analysis.definitions)
					}
				}
				for _, forbidden := range []string{"Int", "Void", "Sendable", "x"} {
					if slices.Contains(symbols, forbidden) {
						t.Errorf("function-type component %q became a definition: %#v",
							forbidden, analysis.definitions)
					}
				}
				swiftTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
			})
		}
	}
}

func TestSwiftRepeatedCallableParameterClausesSurviveConcreteAndFallback(t *testing.T) {
	t.Parallel()

	const body = `func curried(_ x: Int)(_ y: Int) {}
class CurriedInit {
    init(_ x: Int)(_ y: Int) {}
}
struct CurriedSubscript {
    subscript(_ x: Int)(_ y: Int) -> Int { x + y }
}
macro CurriedMacro(_ x: Int)(_ y: Int) =
    #externalMacro(module: "Macros", type: "CurriedMacro")
struct RepeatedClauseTail {}
`
	hashes := strings.Repeat("#", swiftMaximumConcreteRawDelimiterHashes+1)
	fallbackPrefix := "let opaque = " + hashes + `"literal"` + hashes + "\n"
	for _, mode := range []struct {
		name           string
		prefix         string
		wantTreeAbsent bool
	}{
		{name: "concrete"},
		{name: "forced lexical fallback", prefix: fallbackPrefix, wantTreeAbsent: true},
	} {
		t.Run(mode.name, func(t *testing.T) {
			t.Parallel()

			source := mode.prefix + body
			parsedSource := strings.TrimSuffix(source, "\n")
			lines := swiftTestLines(source)
			analysis := analyzeSwiftSource(parsedSource, len(lines))
			if mode.wantTreeAbsent && analysis.tree != nil {
				t.Fatal("forced fallback fixture unexpectedly retained a concrete tree")
			} else if !mode.wantTreeAbsent {
				if analysis.tree == nil {
					t.Fatal("valid repeated-parameter fixture did not retain a concrete tree")
				}
				if spans := swiftSyntaxErrorSpans(analysis.tree, len(parsedSource)); len(spans) != 0 {
					t.Fatalf("repeated-parameter recovery spans = %#v, want none", spans)
				}
			}
			symbols := swiftTestDefinitionSymbols(analysis.definitions)
			for _, required := range []string{
				"curried", "CurriedInit", "init", "CurriedSubscript", "subscript",
				"CurriedMacro", "RepeatedClauseTail",
			} {
				if !slices.Contains(symbols, required) {
					t.Errorf("repeated callable clauses lost %q: %#v", required, analysis.definitions)
				}
			}
			for _, forbidden := range []string{"x", "y", "Int", "Macros", "externalMacro"} {
				if slices.Contains(symbols, forbidden) {
					t.Errorf("repeated-clause signature component %q became a definition: %#v",
						forbidden, analysis.definitions)
				}
			}
			swiftTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
		})
	}
}

func TestSwiftGenericAndCallableHeadersRespectPinnedGrammar(t *testing.T) {
	t.Parallel()

	valid := []struct {
		name        string
		declaration string
		required    []string
	}{
		{
			name: "parameter pack", declaration: "struct Pack<each T> { func visible() {} }",
			required: []string{"Pack", "visible"},
		},
		{
			name: "attributed parameter", declaration: "struct Attributed<@Attr T> { func visible() {} }",
			required: []string{"Attributed", "visible"},
		},
		{
			name:        "inline generic where",
			declaration: "struct InlineWhere<T where T: P> { func visible() {} }",
			required:    []string{"InlineWhere", "visible"},
		},
		{
			name:        "attributed parameter pack",
			declaration: "struct AttributedPack<@Attr each T> { func visible() {} }",
			required:    []string{"AttributedPack", "visible"},
		},
		{
			name:        "array-shaped parameter pack",
			declaration: "struct S<each [T]> { func visible() {} }",
			required:    []string{"S", "visible"},
		},
		{
			name:        "contextual each identifier",
			declaration: "struct EachIdentifier<each> { func visible() {} }",
			required:    []string{"EachIdentifier", "visible"},
		},
		{
			name:        "typed throws and trailing where comma",
			declaration: "func typed<T>(_ x: T) throws(E) where T: P, { func visible() {} }",
			required:    []string{"typed", "visible"},
		},
		{
			name:        "same-type where requirement",
			declaration: "struct S<T> where T = U { func visible() {} }",
			required:    []string{"S", "visible"},
		},
		{
			name:        "implicitly unwrapped where constraint",
			declaration: "struct S<T> where T: P! { func visible() {} }",
			required:    []string{"S", "visible"},
		},
		{
			name: "empty callable", declaration: "func empty() {}",
			required: []string{"empty"},
		},
		{
			name:        "external and internal parameter names",
			declaration: "func labels(_ x: Int, label y: Int) {}",
			required:    []string{"labels"},
		},
		{
			name:        "attributed modified variadic default parameters",
			declaration: `func decorated(@Attr borrowing value: Int..., other: String = "x") {}`,
			required:    []string{"decorated"},
		},
		{
			name:        "complex type alias",
			declaration: "typealias Complex<T> = @Sendable (Array<T>, [String: T?]) async throws(E) -> Result<T, E>",
			required:    []string{"Complex"},
		},
		{
			name:        "complex associated types",
			declaration: "protocol ComplexProtocol { associatedtype Value = [String: (Int, Int)?]; associatedtype Handler: P & Q }",
			required:    []string{"ComplexProtocol", "Value", "Handler"},
		},
		{
			name:        "complex property type",
			declaration: "let complexProperty: @Sendable (Array<Int>, [String: Int?]) async throws(E) -> Result<Int, E> = value",
			required:    []string{"complexProperty"},
		},
		{
			name: "bodyless macro", declaration: "macro good()",
			required: []string{"good"},
		},
		{
			name: "double optional alias", declaration: "typealias A = Int??",
			required: []string{"A"},
		},
		{
			name: "triple optional alias", declaration: "typealias A = Int???",
			required: []string{"A"},
		},
		{
			name: "nested optional generic alias", declaration: "typealias A = S<Int??>",
			required: []string{"A"},
		},
		{
			name:        "optional parameter type",
			declaration: "func f(_ x: Int??) { func visible() {} }",
			required:    []string{"f", "visible"},
		},
		{
			name:        "optional return type",
			declaration: "func f() -> Int?? { func visible() {} }",
			required:    []string{"f", "visible"},
		},
		{
			name:        "backticked qualified return type",
			declaration: "func f() -> Module.`where` { func visible() {} }",
			required:    []string{"f", "visible"},
		},
		{
			name:        "backticked tuple label return type",
			declaration: "func f() -> (`where`: Int, value: String) { func visible() {} }",
			required:    []string{"f", "visible"},
		},
		{
			name:        "backticked nested generic return type",
			declaration: "func f() -> Box<Module.`where`> { func visible() {} }",
			required:    []string{"f", "visible"},
		},
		{
			name:        "implicitly unwrapped callable types",
			declaration: "func iuo(_ x: Int!) -> Int! { x }",
			required:    []string{"iuo"},
		},
		{
			name:        "implicitly unwrapped property",
			declaration: "var iuoProperty: Int!",
			required:    []string{"iuoProperty"},
		},
		{
			name: "prefix operator", declaration: "prefix operator +",
			required: []string{"+"},
		},
		{
			name: "postfix operator", declaration: "postfix operator +",
			required: []string{"+"},
		},
		{
			name: "type pack expansion", declaration: "typealias RepeatedType = repeat T",
			required: []string{"RepeatedType"},
		},
		{
			name:        "each type pack expansion",
			declaration: "typealias RepeatedEachType = repeat each T",
			required:    []string{"RepeatedEachType"},
		},
		{
			name:        "ternary default parameter",
			declaration: "func ternary(x: Int = flag ? 1 : 0) {}",
			required:    []string{"ternary"},
		},
		{
			name:        "function type inheritance",
			declaration: "struct S: Int -> Void { func visible() {} }",
			required:    []string{"S", "visible"},
		},
		{
			name:        "attributed function type inheritance",
			declaration: "struct S: @Sendable Int -> Void { func visible() {} }",
			required:    []string{"S", "visible"},
		},
		{
			name: "contextual each type identifier", declaration: "typealias A = each",
			required: []string{"A"},
		},
		{
			name: "contextual repeat type identifier", declaration: "typealias A = repeat",
			required: []string{"A"},
		},
		{
			name: "contextual borrowing type identifier", declaration: "typealias A = borrowing",
			required: []string{"A"},
		},
		{
			name: "contextual consuming type identifier", declaration: "typealias A = consuming",
			required: []string{"A"},
		},
		{
			name: "contextual isolated type identifier", declaration: "typealias A = isolated",
			required: []string{"A"},
		},
		{
			name: "contextual sending type identifier", declaration: "typealias A = sending",
			required: []string{"A"},
		},
		{
			name: "contextual repeated each identifier", declaration: "typealias A = each each",
			required: []string{"A"},
		},
		{
			name: "contextual repeat each identifier", declaration: "typealias A = repeat each",
			required: []string{"A"},
		},
		{
			name: "contextual any each identifier", declaration: "typealias A = any each",
			required: []string{"A"},
		},
		{
			name:        "tuple element borrowing modifier",
			declaration: "typealias A = (label: Int, _ value: borrowing String)",
			required:    []string{"A"},
		},
		{
			name:        "parenthesized inout function parameter",
			declaration: "typealias A = (inout Int) -> Void",
			required:    []string{"A"},
		},
	}
	invalid := []struct {
		name        string
		declaration string
		required    []string
		forbidden   []string
	}{
		{
			name: "missing generic constraint type", declaration: "typealias Broken = S<T:>",
			forbidden: []string{"Broken"},
		},
		{
			name: "missing generic argument", declaration: "typealias Broken = S<: P>",
			forbidden: []string{"Broken"},
		},
		{
			name: "constraint inside generic argument", declaration: "typealias A = Box<T: P>",
			forbidden: []string{"A"},
		},
		{
			name: "default inside generic argument", declaration: "typealias A = Box<T = U>",
			forbidden: []string{"A"},
		},
		{
			name:        "defaulted declaration generic parameter",
			declaration: "struct S<T = U> { func hidden() {} }",
			forbidden:   []string{"S", "hidden"},
		},
		{
			name:        "parenthesized generic parameter head",
			declaration: "struct S<(T)> { func hidden() {} }",
			forbidden:   []string{"S", "hidden"},
		},
		{
			name:        "array generic parameter head",
			declaration: "struct S<[T]> { func hidden() {} }",
			forbidden:   []string{"S", "hidden"},
		},
		{
			name:        "existential generic parameter head",
			declaration: "struct S<any P> { func hidden() {} }",
			forbidden:   []string{"S", "hidden"},
		},
		{
			name:        "repeat generic parameter head",
			declaration: "struct S<repeat T> { func hidden() {} }",
			forbidden:   []string{"S", "hidden"},
		},
		{
			name:        "optional generic parameter head",
			declaration: "struct S<T?> { func hidden() {} }",
			forbidden:   []string{"S", "hidden"},
		},
		{
			name:        "qualified generic parameter head",
			declaration: "struct S<Module.T> { func hidden() {} }",
			forbidden:   []string{"S", "hidden"},
		},
		{
			name:        "function generic missing constraint",
			declaration: "func bad<T:>() { func hidden() {} }",
			forbidden:   []string{"bad", "hidden"},
		},
		{
			name:        "function parameter missing type",
			declaration: "func bad(x:) { func hidden() {} }",
			forbidden:   []string{"bad", "hidden"},
		},
		{
			name:        "function parameter missing annotation",
			declaration: "func bad(x) { func hidden() {} }",
			forbidden:   []string{"bad", "hidden"},
		},
		{
			name:        "too many parameter names",
			declaration: "func f(a b c: Int) { func hidden() {} }",
			forbidden:   []string{"f", "hidden"},
		},
		{
			name:        "empty default expression",
			declaration: "func f(_ x: Int =) { func hidden() {} }",
			forbidden:   []string{"f", "hidden"},
		},
		{
			name:        "adjacent integer default expressions",
			declaration: "func f(_ x: Int = 1 2) { func hidden() {} }",
			forbidden:   []string{"f", "hidden"},
		},
		{
			name:        "adjacent identifier default expressions",
			declaration: "func f(_ x: Int = value nonsense) { func hidden() {} }",
			forbidden:   []string{"f", "hidden"},
		},
		{
			name:        "empty dictionary return type",
			declaration: "func f() -> [:] { func hidden() {} }",
			forbidden:   []string{"f", "hidden"},
		},
		{
			name:        "empty dictionary parameter type",
			declaration: "func f(_ x: [:]) { func hidden() {} }",
			forbidden:   []string{"f", "hidden"},
		},
		{
			name:        "dictionary parameter missing value type",
			declaration: "func f(_ x: [Int:]) { func hidden() {} }",
			forbidden:   []string{"f", "hidden"},
		},
		{
			name:        "malformed repeated parameter clause",
			declaration: "func bad(x: Int = 0)(= { func hidden() {} }) {}",
			forbidden:   []string{"bad", "hidden"},
		},
		{
			name:        "malformed repeated clause after default closure",
			declaration: "func bad(callback: () -> Void = { func firstHidden() {} })(= { func hidden() {} }) {}",
			forbidden:   []string{"bad", "firstHidden", "hidden"},
		},
		{
			name:        "empty typed throws",
			declaration: "func bad() throws() { func hidden() {} }",
			forbidden:   []string{"bad", "hidden"},
		},
		{
			name:        "multiple typed throws types",
			declaration: "func bad() throws(E, F) { func hidden() {} }",
			forbidden:   []string{"bad", "hidden"},
		},
		{
			name:        "typed rethrows",
			declaration: "func bad() rethrows(E) { func hidden() {} }",
			forbidden:   []string{"bad", "hidden"},
		},
		{
			name:        "adjacent typed throws types",
			declaration: "func bad() throws(E F) { func hidden() {} }",
			forbidden:   []string{"bad", "hidden"},
		},
		{
			name:        "adjacent return types",
			declaration: "func bad() -> Int String { func hidden() {} }",
			forbidden:   []string{"bad", "hidden"},
		},
		{
			name:        "initializer missing parameters",
			declaration: "class C { init { func hidden() {} } }",
			forbidden:   []string{"init", "hidden"},
		},
		{
			name:        "throwing subscript",
			declaration: "struct C { subscript(_ i: Int) throws(E) -> Int { func hidden() {}; return i } }",
			required:    []string{"C"}, forbidden: []string{"subscript", "hidden"},
		},
		{
			name:        "throwing macro",
			declaration: "macro bad() throws(E) = { func hidden() {} }",
			forbidden:   []string{"bad", "hidden"},
		},
		{
			name:        "macro missing definition",
			declaration: "macro bad() { func hidden() {} }",
			forbidden:   []string{"bad", "hidden"},
		},
		{
			name:        "duplicate fixity",
			declaration: "prefix postfix operator +",
			forbidden:   []string{"+"},
		},
		{
			name:        "access-modified operator",
			declaration: "public prefix operator +",
			forbidden:   []string{"+"},
		},
		{
			name:        "attributed operator",
			declaration: "@Attr prefix operator +",
			forbidden:   []string{"+"},
		},
		{
			name:        "adjacent type alias RHS",
			declaration: "typealias Bad = Int nonsense",
			forbidden:   []string{"Bad"},
		},
		{
			name:        "adjacent associated type RHS",
			declaration: "protocol P { associatedtype Bad = Int nonsense }",
			required:    []string{"P"}, forbidden: []string{"Bad"},
		},
		{
			name:        "adjacent property annotation",
			declaration: "let broken: Int String",
			forbidden:   []string{"broken"},
		},
		{
			name:        "implicitly unwrapped alias",
			declaration: "typealias A = Int!",
			forbidden:   []string{"A"},
		},
		{
			name:        "empty dictionary type alias",
			declaration: "typealias A = [:]",
			forbidden:   []string{"A"},
		},
		{
			name:        "adjacent generic parameters",
			declaration: "struct S<T U> { func hidden() {} }",
			forbidden:   []string{"S", "hidden"},
		},
		{
			name:        "generic attribute missing parameter",
			declaration: "struct S<@Attr> { func hidden() {} }",
			forbidden:   []string{"S", "hidden"},
		},
		{
			name:        "empty array generic constraint",
			declaration: "struct S<T: []> { func hidden() {} }",
			forbidden:   []string{"S", "hidden"},
		},
		{
			name:        "adjacent where constrained types",
			declaration: "struct S<T> where T U: P { func hidden() {} }",
			forbidden:   []string{"S", "hidden"},
		},
		{
			name:        "existential inheritance",
			declaration: "struct S: any P { func hidden() {} }",
			forbidden:   []string{"S", "hidden"},
		},
		{
			name:        "adjacent tuple return types",
			declaration: "func f() -> (Int String) { func hidden() {} }",
			forbidden:   []string{"f", "hidden"},
		},
		{
			name:        "adjacent tuple parameter types",
			declaration: "func f(_ x: (Int String)) { func hidden() {} }",
			forbidden:   []string{"f", "hidden"},
		},
		{
			name:        "attributed typed throws",
			declaration: "func f() throws(@Sendable E) { func hidden() {} }",
			forbidden:   []string{"f", "hidden"},
		},
		{
			name:        "implicitly unwrapped macro result",
			declaration: "macro M() -> Int!",
			forbidden:   []string{"M"},
		},
		{
			name:        "inout type alias",
			declaration: "typealias A = inout Int",
			forbidden:   []string{"A"},
		},
		{
			name:        "borrowing modifier type alias",
			declaration: "typealias A = borrowing Int",
			forbidden:   []string{"A"},
		},
		{
			name:        "tuple element with adjacent labels",
			declaration: "typealias A = (first second: Int)",
			forbidden:   []string{"A"},
		},
		{
			name:        "tuple element attribute before label",
			declaration: "typealias A = (@Attr label: Int)",
			forbidden:   []string{"A"},
		},
	}
	hashes := strings.Repeat("#", swiftMaximumConcreteRawDelimiterHashes+1)
	fallbackPrefix := "let opaque = " + hashes + `"literal"` + hashes + "\n"
	modes := []struct {
		name           string
		prefix         string
		wantTreeAbsent bool
	}{
		{name: "concrete"},
		{name: "forced lexical fallback", prefix: fallbackPrefix, wantTreeAbsent: true},
	}

	for _, validCase := range valid {
		for _, mode := range modes {
			t.Run("valid/"+validCase.name+"/"+mode.name, func(t *testing.T) {
				t.Parallel()

				source := mode.prefix + validCase.declaration + "\nstruct GenericCallableTail {}\n"
				parsedSource := strings.TrimSuffix(source, "\n")
				lines := swiftTestLines(source)
				analysis := analyzeSwiftSource(parsedSource, len(lines))
				if mode.wantTreeAbsent && analysis.tree != nil {
					t.Fatal("forced fallback fixture unexpectedly retained a concrete tree")
				} else if !mode.wantTreeAbsent {
					if analysis.tree == nil {
						t.Fatal("valid generic/callable fixture did not retain a concrete tree")
					}
					if spans := swiftSyntaxErrorSpans(analysis.tree, len(parsedSource)); len(spans) != 0 {
						t.Fatalf("valid generic/callable recovery spans = %#v, want none", spans)
					}
				}
				symbols := swiftTestDefinitionSymbols(analysis.definitions)
				requiredSymbols := append([]string(nil), validCase.required...)
				requiredSymbols = append(requiredSymbols, "GenericCallableTail")
				for _, required := range requiredSymbols {
					if !slices.Contains(symbols, required) {
						t.Errorf("valid generic/callable fixture lost %q: %#v",
							required, analysis.definitions)
					}
				}
				swiftTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
			})
		}
	}

	for _, invalidCase := range invalid {
		for _, mode := range modes {
			t.Run("invalid/"+invalidCase.name+"/"+mode.name, func(t *testing.T) {
				t.Parallel()

				source := mode.prefix + invalidCase.declaration +
					"\nstruct GenericCallableTail { func recovered() {} }\n"
				parsedSource := strings.TrimSuffix(source, "\n")
				lines := swiftTestLines(source)
				analysis := analyzeSwiftSource(parsedSource, len(lines))
				if mode.wantTreeAbsent && analysis.tree != nil {
					t.Fatal("forced fallback fixture unexpectedly retained a concrete tree")
				} else if !mode.wantTreeAbsent {
					if analysis.tree == nil {
						t.Fatal("small malformed generic/callable fixture did not retain a concrete tree")
					}
					if spans := swiftSyntaxErrorSpans(analysis.tree, len(parsedSource)); len(spans) == 0 {
						t.Fatal("pinned grammar unexpectedly accepted malformed generic/callable fixture")
					}
				}
				symbols := swiftTestDefinitionSymbols(analysis.definitions)
				requiredSymbols := append([]string(nil), invalidCase.required...)
				requiredSymbols = append(requiredSymbols, "GenericCallableTail", "recovered")
				for _, required := range requiredSymbols {
					if !slices.Contains(symbols, required) {
						t.Errorf("malformed generic/callable fixture lost %q: %#v",
							required, analysis.definitions)
					}
				}
				for _, forbidden := range invalidCase.forbidden {
					if slices.Contains(symbols, forbidden) {
						t.Errorf("malformed generic/callable fixture promoted %q: %#v",
							forbidden, analysis.definitions)
					}
				}
				swiftTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
			})
		}
	}
}

func TestSwiftPropertyWhereClausesRespectPinnedGrammar(t *testing.T) {
	t.Parallel()

	valid := []struct {
		name        string
		declaration string
		expected    []string
	}{
		{
			name:        "stored property",
			declaration: "let x: T where T: P",
			expected:    []string{"x"},
		},
		{
			name:        "computed property",
			declaration: "let x: T where T: P { 1 }",
			expected:    []string{"x"},
		},
		{
			name:        "protocol property requirement",
			declaration: "protocol P { var x: T where T: Q { get } }",
			expected:    []string{"P", "x"},
		},
		{
			name:        "stored property with multiple requirements",
			declaration: "let x: T where T: P, U: Q",
			expected:    []string{"x"},
		},
		{
			name:        "computed property with multiple requirements",
			declaration: "let x: T where T: P, U: Q { 1 }",
			expected:    []string{"x"},
		},
		{
			name:        "protocol property requirement with multiple requirements",
			declaration: "protocol P { var x: T where T: Q, U: R { get } }",
			expected:    []string{"P", "x"},
		},
	}
	invalid := []struct {
		name        string
		declaration string
		forbidden   []string
	}{
		{
			name:        "adjacent where constrained types",
			declaration: "let x: T where T U: P",
			forbidden:   []string{"x"},
		},
	}
	hashes := strings.Repeat("#", swiftMaximumConcreteRawDelimiterHashes+1)
	fallbackPrefix := "let opaque = " + hashes + `"literal"` + hashes + "\n"
	modes := []struct {
		name           string
		prefix         string
		wantTreeAbsent bool
	}{
		{name: "concrete"},
		{name: "forced lexical fallback", prefix: fallbackPrefix, wantTreeAbsent: true},
	}

	for _, validCase := range valid {
		for _, mode := range modes {
			t.Run("valid/"+validCase.name+"/"+mode.name, func(t *testing.T) {
				t.Parallel()

				source := mode.prefix + validCase.declaration + "\nstruct PropertyWhereTail {}\n"
				parsedSource := strings.TrimSuffix(source, "\n")
				lines := swiftTestLines(source)
				analysis := analyzeSwiftSource(parsedSource, len(lines))
				switch {
				case mode.wantTreeAbsent && analysis.tree != nil:
					t.Fatal("forced fallback fixture unexpectedly retained a concrete tree")
				case !mode.wantTreeAbsent && analysis.tree == nil:
					t.Fatal("valid property-where fixture did not retain a concrete tree")
				case !mode.wantTreeAbsent:
					if spans := swiftSyntaxErrorSpans(analysis.tree, len(parsedSource)); len(spans) != 0 {
						t.Fatalf("valid property-where recovery spans = %#v, want none", spans)
					}
				}
				want := append([]string(nil), validCase.expected...)
				if mode.wantTreeAbsent {
					want = append([]string{"opaque"}, want...)
				}
				want = append(want, "PropertyWhereTail")
				if got := swiftTestDefinitionSymbols(analysis.definitions); !slices.Equal(got, want) {
					t.Errorf("valid property-where definitions = %#v, want %#v: %#v",
						got, want, analysis.definitions)
				}
				swiftTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
			})
		}
	}

	for _, invalidCase := range invalid {
		for _, mode := range modes {
			t.Run("invalid/"+invalidCase.name+"/"+mode.name, func(t *testing.T) {
				t.Parallel()

				source := mode.prefix + invalidCase.declaration + "\nstruct PropertyWhereTail {}\n"
				parsedSource := strings.TrimSuffix(source, "\n")
				lines := swiftTestLines(source)
				analysis := analyzeSwiftSource(parsedSource, len(lines))
				switch {
				case mode.wantTreeAbsent && analysis.tree != nil:
					t.Fatal("forced fallback fixture unexpectedly retained a concrete tree")
				case !mode.wantTreeAbsent && analysis.tree == nil:
					t.Fatal("malformed property-where fixture did not retain a concrete tree")
				case !mode.wantTreeAbsent:
					if spans := swiftSyntaxErrorSpans(analysis.tree, len(parsedSource)); len(spans) == 0 {
						t.Fatal("pinned grammar unexpectedly accepted malformed property-where fixture")
					}
				}
				symbols := swiftTestDefinitionSymbols(analysis.definitions)
				if !slices.Contains(symbols, "PropertyWhereTail") {
					t.Errorf("malformed property-where fixture lost recovery tail: %#v",
						analysis.definitions)
				}
				for _, forbidden := range invalidCase.forbidden {
					if slices.Contains(symbols, forbidden) {
						t.Errorf("malformed property-where fixture promoted %q: %#v",
							forbidden, analysis.definitions)
					}
				}
				swiftTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
			})
		}
	}
}

func TestSwiftNestedGenericCloseRunsSurviveConcreteAndFallback(t *testing.T) {
	t.Parallel()

	const body = `struct Box<T: Outer<Inner<Int>>> {
    func value() {}
}
struct DeepBox<T: Outer<Middle<Inner<Int>>>> {
    func deepValue() {}
}
struct OptionalContainer<T> {
    var value: Outer<Inner<T?>>
    var iuoValue: Outer<Inner<T!>>
}
typealias OptionalNested<T> = Outer<Inner<T?>>
typealias IUONested<T> = Outer<Inner<T!>>
struct GenericCloseTail {}
`
	hashes := strings.Repeat("#", swiftMaximumConcreteRawDelimiterHashes+1)
	fallbackPrefix := "let opaque = " + hashes + `"literal"` + hashes + "\n"
	for _, mode := range []struct {
		name           string
		prefix         string
		wantTreeAbsent bool
	}{
		{name: "concrete"},
		{name: "forced lexical fallback", prefix: fallbackPrefix, wantTreeAbsent: true},
	} {
		t.Run(mode.name, func(t *testing.T) {
			t.Parallel()

			source := mode.prefix + body
			lines := swiftTestLines(source)
			analysis := analyzeSwiftSource(strings.TrimSuffix(source, "\n"), len(lines))
			if mode.wantTreeAbsent && analysis.tree != nil {
				t.Fatal("forced fallback fixture unexpectedly retained a concrete tree")
			} else if !mode.wantTreeAbsent && analysis.tree == nil {
				t.Fatal("valid nested-generic fixture did not retain a concrete tree")
			}
			symbols := swiftTestDefinitionSymbols(analysis.definitions)
			for _, required := range []string{
				"Box", "value", "DeepBox", "deepValue", "OptionalContainer", "iuoValue",
				"OptionalNested", "IUONested", "GenericCloseTail",
			} {
				if !slices.Contains(symbols, required) {
					t.Errorf("nested generic close run lost %q: %#v", required, analysis.definitions)
				}
			}
			for _, forbidden := range []string{"T", "Outer", "Middle", "Inner", "Int"} {
				if slices.Contains(symbols, forbidden) {
					t.Errorf("nested generic component %q became a definition: %#v",
						forbidden, analysis.definitions)
				}
			}
			valueDefinitions := 0
			for _, definition := range analysis.definitions {
				if definition.symbol == "value" {
					valueDefinitions++
				}
			}
			if valueDefinitions != 2 {
				t.Errorf("nested generic value definition count = %d, want 2: %#v",
					valueDefinitions, analysis.definitions)
			}
			swiftTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
		})
	}
}

func TestSwiftDefaultClosureInFunctionHeaderKeepsNestedHelper(t *testing.T) {
	t.Parallel()

	const body = `func outer(
    callback: () -> Void = {
        func defaultHelper() {}
    }
) {}
let property = {
    func propertyHelper() {}
}
enum Defaulted {
    case defaulted(handler: () -> Void = {
        func enumHelper() {}
    })
}
struct DefaultClosureTail {}
`
	hashes := strings.Repeat("#", swiftMaximumConcreteRawDelimiterHashes+1)
	fallbackPrefix := "let opaque = " + hashes + `"literal"` + hashes + "\n"
	for _, mode := range []struct {
		name           string
		prefix         string
		wantTreeAbsent bool
	}{
		{name: "concrete"},
		{name: "forced lexical fallback", prefix: fallbackPrefix, wantTreeAbsent: true},
	} {
		t.Run(mode.name, func(t *testing.T) {
			t.Parallel()

			source := mode.prefix + body
			lines := swiftTestLines(source)
			parsedSource := strings.TrimSuffix(source, "\n")
			analysis := analyzeSwiftSource(parsedSource, len(lines))
			switch {
			case mode.wantTreeAbsent && analysis.tree != nil:
				t.Fatal("forced fallback fixture unexpectedly retained a concrete tree")
			case !mode.wantTreeAbsent && analysis.tree == nil:
				t.Fatal("valid default-closure fixture did not retain a concrete tree")
			case !mode.wantTreeAbsent:
				if spans := swiftSyntaxErrorSpans(analysis.tree, len(parsedSource)); len(spans) != 0 {
					t.Fatalf("valid default-closure fixture recovery spans = %#v, want none", spans)
				}
			}
			symbols := swiftTestDefinitionSymbols(analysis.definitions)
			for _, required := range []string{
				"outer", "defaultHelper", "property", "propertyHelper", "Defaulted",
				"defaulted", "enumHelper", "DefaultClosureTail",
			} {
				if !slices.Contains(symbols, required) {
					t.Errorf("default closure lost %q: %#v", required, analysis.definitions)
				}
			}
			for _, forbidden := range []string{"callback", "Void"} {
				if slices.Contains(symbols, forbidden) {
					t.Errorf("default-closure signature component %q became a definition: %#v",
						forbidden, analysis.definitions)
				}
			}
			swiftTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
		})
	}
}

func TestSwiftCommentTriviaBetweenKeywordLikeParameterLabelsPreservesMethod(t *testing.T) {
	t.Parallel()

	const body = `struct KeywordLabels {
    func f(actor /* trivia */ value: Int) {}
}
struct KeywordLabelTail {}
`
	hashes := strings.Repeat("#", swiftMaximumConcreteRawDelimiterHashes+1)
	fallbackPrefix := "let opaque = " + hashes + `"literal"` + hashes + "\n"
	for _, test := range []struct {
		name           string
		prefix         string
		wantTreeAbsent bool
	}{
		{name: "concrete"},
		{name: "forced lexical fallback", prefix: fallbackPrefix, wantTreeAbsent: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			source := test.prefix + body
			lines := swiftTestLines(source)
			analysis := analyzeSwiftSource(strings.TrimSuffix(source, "\n"), len(lines))
			if test.wantTreeAbsent && analysis.tree != nil {
				t.Fatal("forced fallback fixture unexpectedly retained a concrete tree")
			} else if !test.wantTreeAbsent && analysis.tree == nil {
				t.Fatal("small parameter-trivia fixture did not retain a concrete tree")
			}
			symbols := swiftTestDefinitionSymbols(analysis.definitions)
			for _, required := range []string{"KeywordLabels", "f", "KeywordLabelTail"} {
				if !slices.Contains(symbols, required) {
					t.Errorf("commented parameter labels lost %q: %#v", required, analysis.definitions)
				}
			}
			for _, forbidden := range []string{"actor", "value", "Int"} {
				if slices.Contains(symbols, forbidden) {
					t.Errorf("parameter label/name %q became a definition: %#v",
						forbidden, analysis.definitions)
				}
			}
			swiftTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
		})
	}
}

func TestSwiftExtensionTargetsSurviveConcreteAndFallback(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		target        string
		member        string
		requiredOwner string
	}{
		{name: "qualified generic", target: "Outer<Int>.Inner", member: "qualifiedGenericMember"},
		{name: "array", target: "[Int]", member: "arrayMember"},
		{name: "dictionary", target: "[String: Int]", member: "dictionaryMember"},
		{name: "tuple", target: "(Int, Int)", member: "tupleMember"},
		{name: "function", target: "(Int) -> Void", member: "functionMember"},
		{name: "optional", target: "Foo?", member: "optionalMember"},
		{name: "existential", target: "any Foo", member: "existentialMember"},
		{name: "opaque", target: "some Foo", member: "opaqueMember"},
		{name: "pack", target: "each Foo", member: "packMember"},
		{name: "pack expansion", target: "repeat each Foo", member: "packExpansionMember"},
		{name: "suppressed", target: "~Copyable", member: "suppressedMember"},
		{name: "generic", target: "Foo<T>", member: "genericMember"},
		{name: "protocol composition", target: "P & Q", member: "visible"},
		{name: "array nested type", target: "[Int].Index", member: "visible"},
		{name: "typed throwing function", target: "(Int) throws(E) -> Void", member: "visible"},
		{name: "unparenthesized function", target: "Int -> Void", member: "visible"},
		{name: "existential suppressed type", target: "any ~Copyable", member: "visible"},
		{name: "chained optional", target: "Foo??", member: "visible"},
		{
			name: "function conformance", target: "Foo: (Int) -> Void",
			member: "functionConformanceMember", requiredOwner: "Foo",
		},
	}
	hashes := strings.Repeat("#", swiftMaximumConcreteRawDelimiterHashes+1)
	fallbackPrefix := "let opaque = " + hashes + `"literal"` + hashes + "\n"
	for _, test := range tests {
		for _, mode := range []struct {
			name           string
			prefix         string
			wantTreeAbsent bool
		}{
			{name: "concrete"},
			{name: "forced lexical fallback", prefix: fallbackPrefix, wantTreeAbsent: true},
		} {
			t.Run(test.name+"/"+mode.name, func(t *testing.T) {
				t.Parallel()

				body := "extension " + test.target + " { func " + test.member + "() {} }\n" +
					"struct ExtensionTargetTail {}\n"
				source := mode.prefix + body
				parsedSource := strings.TrimSuffix(source, "\n")
				lines := swiftTestLines(source)
				analysis := analyzeSwiftSource(parsedSource, len(lines))
				if mode.wantTreeAbsent && analysis.tree != nil {
					t.Fatal("forced fallback fixture unexpectedly retained a concrete tree")
				} else if !mode.wantTreeAbsent {
					if analysis.tree == nil {
						t.Fatal("valid extension-target fixture did not retain a concrete tree")
					}
					if spans := swiftSyntaxErrorSpans(analysis.tree, len(parsedSource)); len(spans) != 0 {
						t.Fatalf("extension-target recovery spans = %#v, want none", spans)
					}
				}
				symbols := swiftTestDefinitionSymbols(analysis.definitions)
				for _, required := range []string{test.member, "ExtensionTargetTail"} {
					if !slices.Contains(symbols, required) {
						t.Errorf("extension target lost %q: %#v", required, analysis.definitions)
					}
				}
				if test.requiredOwner != "" && !slices.Contains(symbols, test.requiredOwner) {
					t.Errorf("extension target lost owner %q: %#v",
						test.requiredOwner, analysis.definitions)
				}
				swiftTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
			})
		}
	}
}

func TestSwiftImportHeadersRespectPinnedGrammarConcreteAndFallback(t *testing.T) {
	t.Parallel()

	valid := []struct {
		name          string
		body          string
		required      []string
		allowRecovery bool
	}{
		{name: "module", body: "import Foundation"},
		{name: "qualified", body: "import Foundation.Submodule"},
		{name: "kind", body: "import struct Module.Type"},
		{name: "testable", body: "@testable import Foundation"},
		{name: "testable kind", body: "@testable import struct Module.Type"},
		{name: "access control", body: "private import Foundation"},
		{
			name:          "newline independent closure",
			body:          "import Foundation\n{ func visibleImportClosure() {} }",
			required:      []string{"visibleImportClosure"},
			allowRecovery: true,
		},
		{
			name:          "LF in comment before independent closure",
			body:          "import Foundation /* gap\ncontinuation */ { func visibleImportClosure() {} }",
			required:      []string{"visibleImportClosure"},
			allowRecovery: true,
		},
		{
			name:          "CR in comment before independent closure",
			body:          "import Foundation /* gap\rcontinuation */ { func visibleImportClosure() {} }",
			required:      []string{"visibleImportClosure"},
			allowRecovery: true,
		},
		{
			name:          "CRLF in comment before independent closure",
			body:          "import Foundation /* gap\r\ncontinuation */ { func visibleImportClosure() {} }",
			required:      []string{"visibleImportClosure"},
			allowRecovery: true,
		},
		{
			name:     "semicolon independent closure",
			body:     "import Foundation; { func visibleImportClosure() {} }",
			required: []string{"visibleImportClosure"},
		},
	}
	invalid := []struct {
		name      string
		body      string
		forbidden []string
	}{
		{name: "missing module", body: "import"},
		{name: "kind missing path", body: "import struct"},
		{name: "unsupported macro kind", body: "import macro Foundation.SomeMacro"},
		{name: "trailing dot", body: "import Foundation."},
		{name: "arbitrary suffix", body: "import Foundation nonsense"},
		{
			name: "same-line block", body: "import Foundation { func hiddenImportClosure() {} }",
			forbidden: []string{"hiddenImportClosure"},
		},
		{
			name:      "same-line comment gap block",
			body:      "import Foundation /* gap */ { func hiddenImportClosure() {} }",
			forbidden: []string{"hiddenImportClosure"},
		},
	}
	hashes := strings.Repeat("#", swiftMaximumConcreteRawDelimiterHashes+1)
	fallbackPrefix := "let opaque = " + hashes + `"literal"` + hashes + "\n"
	modes := []struct {
		name           string
		prefix         string
		wantTreeAbsent bool
	}{
		{name: "concrete"},
		{name: "forced lexical fallback", prefix: fallbackPrefix, wantTreeAbsent: true},
	}

	for _, validCase := range valid {
		for _, mode := range modes {
			t.Run("valid/"+validCase.name+"/"+mode.name, func(t *testing.T) {
				t.Parallel()

				source := mode.prefix + validCase.body + "\nfunc importTail() {}\n"
				parsedSource := strings.TrimSuffix(source, "\n")
				lines := swiftTestLines(source)
				analysis := analyzeSwiftSource(parsedSource, len(lines))
				if mode.wantTreeAbsent && analysis.tree != nil {
					t.Fatal("forced fallback fixture unexpectedly retained a concrete tree")
				} else if !mode.wantTreeAbsent {
					if analysis.tree == nil {
						t.Fatal("valid import fixture did not retain a concrete tree")
					}
					if spans := swiftSyntaxErrorSpans(analysis.tree, len(parsedSource)); len(spans) != 0 && !validCase.allowRecovery {
						t.Fatalf("valid import recovery spans = %#v, want none", spans)
					}
				}
				symbols := swiftTestDefinitionSymbols(analysis.definitions)
				requiredSymbols := append([]string(nil), validCase.required...)
				requiredSymbols = append(requiredSymbols, "importTail")
				for _, required := range requiredSymbols {
					if !slices.Contains(symbols, required) {
						t.Errorf("valid import fixture lost %q: %#v", required, analysis.definitions)
					}
				}
				importLine := swiftTestLineContaining(t, lines, "import ")
				backend := prepareLanguageBackend(newSwiftLanguage(), lines)
				if start, end, ok := backend.importRange(lines); !ok || start != importLine || end != importLine {
					t.Errorf("valid import range = %d-%d, %t; want %d-%d, true",
						start, end, ok, importLine, importLine)
				}
				swiftTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
			})
		}
	}

	for _, invalidCase := range invalid {
		for _, mode := range modes {
			t.Run("invalid/"+invalidCase.name+"/"+mode.name, func(t *testing.T) {
				t.Parallel()

				source := mode.prefix + invalidCase.body + "\nfunc importTail() {}\n"
				parsedSource := strings.TrimSuffix(source, "\n")
				lines := swiftTestLines(source)
				analysis := analyzeSwiftSource(parsedSource, len(lines))
				if mode.wantTreeAbsent && analysis.tree != nil {
					t.Fatal("forced fallback fixture unexpectedly retained a concrete tree")
				} else if !mode.wantTreeAbsent {
					if analysis.tree == nil {
						t.Fatal("small malformed import fixture did not retain a concrete tree")
					}
					if spans := swiftSyntaxErrorSpans(analysis.tree, len(parsedSource)); len(spans) == 0 {
						t.Fatal("pinned grammar unexpectedly accepted malformed import")
					}
				}
				symbols := swiftTestDefinitionSymbols(analysis.definitions)
				if !slices.Contains(symbols, "importTail") {
					t.Errorf("malformed import lost tail: %#v", analysis.definitions)
				}
				for _, forbidden := range invalidCase.forbidden {
					if slices.Contains(symbols, forbidden) {
						t.Errorf("malformed import promoted %q: %#v", forbidden, analysis.definitions)
					}
				}
				backend := prepareLanguageBackend(newSwiftLanguage(), lines)
				if start, end, ok := backend.importRange(lines); ok {
					t.Errorf("malformed import range = %d-%d, want absent", start, end)
				}
				swiftTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
			})
		}
	}
}

func TestSwiftRetroactiveExtensionSurvivesConcreteAndFallback(t *testing.T) {
	t.Parallel()

	const body = `extension Example: @retroactive ProtocolName {
    func retroactiveMember() {}
}
struct RetroactiveTail {}
`
	hashes := strings.Repeat("#", swiftMaximumConcreteRawDelimiterHashes+1)
	fallbackPrefix := "let opaque = " + hashes + `"literal"` + hashes + "\n"
	for _, test := range []struct {
		name           string
		prefix         string
		wantTreeAbsent bool
	}{
		{name: "concrete"},
		{name: "forced lexical fallback", prefix: fallbackPrefix, wantTreeAbsent: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			source := test.prefix + body
			lines := swiftTestLines(source)
			analysis := analyzeSwiftSource(strings.TrimSuffix(source, "\n"), len(lines))
			if test.wantTreeAbsent && analysis.tree != nil {
				t.Fatal("forced fallback fixture unexpectedly retained a concrete tree")
			} else if !test.wantTreeAbsent && analysis.tree == nil {
				t.Fatal("small retroactive-extension fixture did not retain a concrete tree")
			}
			symbols := swiftTestDefinitionSymbols(analysis.definitions)
			for _, required := range []string{"Example", "retroactiveMember", "RetroactiveTail"} {
				if !slices.Contains(symbols, required) {
					t.Errorf("retroactive extension lost %q: %#v", required, analysis.definitions)
				}
			}
			for _, forbidden := range []string{"retroactive", "ProtocolName"} {
				if slices.Contains(symbols, forbidden) {
					t.Errorf("retroactive conformance detail %q became a definition: %#v",
						forbidden, analysis.definitions)
				}
			}
			swiftTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
		})
	}
}

func TestSwiftBorrowingAndConsumingInstanceMethodsSurviveConcreteAndFallback(t *testing.T) {
	t.Parallel()

	const body = `struct OwnershipMethods {
    borrowing func inspect() {}
    consuming func consume() {}
}
struct OwnershipTail {}
`
	hashes := strings.Repeat("#", swiftMaximumConcreteRawDelimiterHashes+1)
	fallbackPrefix := "let opaque = " + hashes + `"literal"` + hashes + "\n"
	for _, test := range []struct {
		name           string
		prefix         string
		wantTreeAbsent bool
	}{
		{name: "concrete"},
		{name: "forced lexical fallback", prefix: fallbackPrefix, wantTreeAbsent: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			source := test.prefix + body
			lines := swiftTestLines(source)
			analysis := analyzeSwiftSource(strings.TrimSuffix(source, "\n"), len(lines))
			if test.wantTreeAbsent && analysis.tree != nil {
				t.Fatal("forced fallback fixture unexpectedly retained a concrete tree")
			} else if !test.wantTreeAbsent && analysis.tree == nil {
				t.Fatal("small ownership-method fixture did not retain a concrete tree")
			}
			symbols := swiftTestDefinitionSymbols(analysis.definitions)
			for _, required := range []string{"OwnershipMethods", "inspect", "consume", "OwnershipTail"} {
				if !slices.Contains(symbols, required) {
					t.Errorf("ownership-modified method lost %q: %#v", required, analysis.definitions)
				}
			}
			for _, forbidden := range []string{"borrowing", "consuming"} {
				if slices.Contains(symbols, forbidden) {
					t.Errorf("ownership modifier %q became a definition: %#v",
						forbidden, analysis.definitions)
				}
			}
			swiftTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
		})
	}
}

func TestSwiftExplicitInitializerTrailingClosureKeepsNestedFunction(t *testing.T) {
	t.Parallel()

	const body = `func outer() {
    Builder.init() {
        func nestedHelper() {}
    }
}
struct InitializerTail {}
`
	hashes := strings.Repeat("#", swiftMaximumConcreteRawDelimiterHashes+1)
	fallbackPrefix := "let opaque = " + hashes + `"literal"` + hashes + "\n"
	for _, test := range []struct {
		name           string
		prefix         string
		wantTreeAbsent bool
	}{
		{name: "concrete"},
		{name: "forced lexical fallback", prefix: fallbackPrefix, wantTreeAbsent: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			source := test.prefix + body
			lines := swiftTestLines(source)
			analysis := analyzeSwiftSource(strings.TrimSuffix(source, "\n"), len(lines))
			if test.wantTreeAbsent && analysis.tree != nil {
				t.Fatal("forced fallback fixture unexpectedly retained a concrete tree")
			} else if !test.wantTreeAbsent && analysis.tree == nil {
				t.Fatal("small explicit-initializer fixture did not retain a concrete tree")
			}
			symbols := swiftTestDefinitionSymbols(analysis.definitions)
			for _, required := range []string{"outer", "nestedHelper", "InitializerTail"} {
				if !slices.Contains(symbols, required) {
					t.Errorf("initializer trailing closure lost %q: %#v",
						required, analysis.definitions)
				}
			}
			for _, forbidden := range []string{"Builder", "init"} {
				if slices.Contains(symbols, forbidden) {
					t.Errorf("initializer call component %q became a definition: %#v",
						forbidden, analysis.definitions)
				}
			}
			nested := swiftTestFirstDefinition(t, analysis.definitions, "nestedHelper")
			if !nested.ownsScope {
				t.Errorf("nested helper lost owning scope: %#v", nested)
			}
			swiftTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
		})
	}
}

func TestSwiftLexicalFallbackKeepsMultilineAttributedFunctionSignature(t *testing.T) {
	t.Parallel()

	hashes := strings.Repeat("#", swiftMaximumConcreteRawDelimiterHashes+1)
	source := "let opaque = " + hashes + `"literal"` + hashes + `
struct Worker {
    func perform(
        class: Int,
        func value: Int,
        completion:
            @escaping @Sendable () -> Void
    ) {
        completion()
    }
    func tail() {}
}
`
	lines := swiftTestLines(source)
	analysis := analyzeSwiftSource(strings.TrimSuffix(source, "\n"), len(lines))
	if analysis.tree != nil {
		t.Fatal("over-hash fixture unexpectedly retained a concrete syntax tree")
	}
	want := []string{"opaque", "Worker", "perform", "tail"}
	if got := swiftTestDefinitionSymbols(analysis.definitions); !slices.Equal(got, want) {
		t.Fatalf("multiline attributed function definitions = %#v, want %#v", got, want)
	}
	perform := swiftTestFirstDefinition(t, analysis.definitions, "perform")
	wantStart := swiftTestLineContaining(t, lines, "func perform")
	wantEnd := swiftTestLineContaining(t, lines, "func tail") - 1
	if !perform.ownsScope || perform.scopeStart != wantStart || perform.scopeEnd != wantEnd {
		t.Fatalf("multiline attributed function scope = %#v, want owning %d-%d",
			perform, wantStart, wantEnd)
	}
	for _, parameter := range []string{"class", "value", "completion"} {
		if slices.Contains(swiftTestDefinitionSymbols(analysis.definitions), parameter) {
			t.Errorf("multiline parameter label/name %q became a definition: %#v",
				parameter, analysis.definitions)
		}
	}
	swiftTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
}

func TestSwiftShebangPayloadIsOpaqueToConcreteAndScopeAnalysis(t *testing.T) {
	t.Parallel()

	payloads := []struct {
		name string
		text string
	}{
		{name: "arbitrary interpreter payload", text: "#! { arbitrary interpreter payload"},
		{name: "nested delimiters", text: "#!/usr/bin/swift ({["},
	}
	boundaries := []struct {
		name string
		text string
	}{
		{name: "LF", text: "\n"},
		{name: "CR", text: "\r"},
		{name: "CRLF", text: "\r\n"},
	}
	for _, payload := range payloads {
		for _, boundary := range boundaries {
			t.Run(payload.name+"/"+boundary.name, func(t *testing.T) {
				t.Parallel()

				source := payload.text + boundary.text + "struct Tail {}\n"
				parsedSource := strings.TrimSuffix(source, "\n")
				lines := swiftTestLines(source)
				forcedLexed := lexSwift(parsedSource)
				forcedLexed.concreteEligible = true
				directTree, ok := parseSwiftSyntax(parsedSource, forcedLexed)
				if !ok || !validateSwiftSyntaxTree(directTree, len(parsedSource)) {
					t.Fatal("pinned shebang grammar fixture did not produce a concrete tree")
				}
				if spans := swiftSyntaxErrorSpans(directTree, len(parsedSource)); len(spans) != 0 {
					t.Fatalf("pinned shebang recovery spans = %#v, want none", spans)
				}

				lexed := lexSwift(parsedSource)
				if !lexed.concreteEligible {
					t.Fatal("line-initial shebang payload disabled concrete parsing")
				}
				analysis := analyzeSwiftSource(parsedSource, len(lines))
				if analysis.tree == nil {
					t.Fatal("line-initial shebang fixture did not retain a concrete tree")
				}
				if spans := swiftSyntaxErrorSpans(analysis.tree, len(parsedSource)); len(spans) != 0 {
					t.Fatalf("concrete shebang recovery spans = %#v, want none", spans)
				}
				if got := swiftTestDefinitionSymbols(analysis.definitions); !slices.Equal(got, []string{"Tail"}) {
					t.Fatalf("shebang definitions = %#v, want Tail only", got)
				}
				tail := swiftTestFirstDefinition(t, analysis.definitions, "Tail")
				wantLine := strings.Count(payload.text+boundary.text, "\n") + 1
				if !tail.ownsScope || tail.line != wantLine || tail.scopeStart != wantLine ||
					tail.scopeEnd != wantLine {
					t.Errorf("shebang Tail scope = %#v, want owning line %d", tail, wantLine)
				}
				backend := prepareLanguageBackend(newSwiftLanguage(), lines)
				if start, end := backend.enclosingScope(lines, wantLine); start != wantLine || end != wantLine {
					t.Errorf("shebang Tail enclosing scope = %d-%d, want %d-%d",
						start, end, wantLine, wantLine)
				}
				if start, end := backend.(navigationScopeResolver).navigationScope(
					lines, wantLine,
				); start != wantLine || end != wantLine {
					t.Errorf("shebang Tail navigation scope = %d-%d, want %d-%d",
						start, end, wantLine, wantLine)
				}
				swiftTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
			})
		}
	}

	t.Run("not at byte zero", func(t *testing.T) {
		t.Parallel()

		const source = "let prefix = 0\n#! { arbitrary interpreter payload\nstruct Tail {}"
		lexed := lexSwift(source)
		if lexed.concreteEligible || lexed.maximumDelimiterDepth == 0 {
			t.Fatalf("noninitial shebang lookalike = (eligible %t, delimiter depth %d), want false, positive",
				lexed.concreteEligible, lexed.maximumDelimiterDepth)
		}
		analysis := analyzeSwiftSource(source, len(swiftTestLines(source)))
		if analysis.tree != nil {
			t.Fatal("noninitial shebang lookalike unexpectedly retained a concrete tree")
		}
	})
}

func TestSwiftLineLeadingInterpolationsRetainDeclarationsConcreteAndFallback(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{
			name: "same-line control",
			body: `let x = "\({ func visible() {} })"
struct Tail {}
`,
		},
		{
			name: "continued ordinary string",
			body: `let x = "before\
\({ func visible() {} })"
struct Tail {}
`,
		},
		{
			name: "line-leading multiline interpolation",
			body: `let x = """
\({ func visible() {} })
"""
struct Tail {}
`,
		},
		{
			name: "multiline text before interpolation",
			body: `let x = """
text \({ func visible() {} })
"""
struct Tail {}
`,
		},
		{
			name: "line-leading raw multiline interpolation",
			body: `let x = ##"""
\##({ func visible() {} })
"""##
struct Tail {}
`,
		},
	}
	hashes := strings.Repeat("#", swiftMaximumConcreteRawDelimiterHashes+1)
	fallbackPrefix := "let opaque = " + hashes + `"literal"` + hashes + "\n"
	for _, test := range tests {
		for _, mode := range []struct {
			name           string
			prefix         string
			wantTreeAbsent bool
		}{
			{name: "concrete"},
			{name: "forced lexical fallback", prefix: fallbackPrefix, wantTreeAbsent: true},
		} {
			t.Run(test.name+"/"+mode.name, func(t *testing.T) {
				t.Parallel()

				source := mode.prefix + test.body
				parsedSource := strings.TrimSuffix(source, "\n")
				lines := swiftTestLines(source)
				analysis := analyzeSwiftSource(parsedSource, len(lines))
				if mode.wantTreeAbsent && analysis.tree != nil {
					t.Fatal("forced fallback fixture unexpectedly retained a concrete tree")
				} else if !mode.wantTreeAbsent {
					if analysis.tree == nil {
						t.Fatal("valid interpolation fixture did not retain a concrete tree")
					}
					if spans := swiftSyntaxErrorSpans(analysis.tree, len(parsedSource)); len(spans) != 0 {
						t.Fatalf("valid interpolation recovery spans = %#v, want none", spans)
					}
				}
				symbols := swiftTestDefinitionSymbols(analysis.definitions)
				for _, required := range []string{"x", "visible", "Tail"} {
					if !slices.Contains(symbols, required) {
						t.Errorf("interpolation fixture lost %q: %#v", required, analysis.definitions)
					}
				}
				swiftTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
			})
		}
	}
}

func TestSwiftDocumentationRequiresLineLeadingComment(t *testing.T) {
	t.Parallel()

	const body = `struct Documentation {
    let first = 1 /// trailing line documentation lookalike
    func afterLine() {}

    let second = 2 /** trailing block documentation lookalike */
    func afterBlock() {}

    /// Actual documentation.
    func documented() {}
}
`
	hashes := strings.Repeat("#", swiftMaximumConcreteRawDelimiterHashes+1)
	fallbackPrefix := "let opaque = " + hashes + `"literal"` + hashes + "\n"
	for _, test := range []struct {
		name           string
		prefix         string
		wantTreeAbsent bool
	}{
		{name: "concrete eligible"},
		{name: "forced lexical fallback", prefix: fallbackPrefix, wantTreeAbsent: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			source := test.prefix + body
			lines := swiftTestLines(source)
			parsedSource := strings.TrimSuffix(source, "\n")
			analysis := analyzeSwiftSource(parsedSource, len(lines))
			if test.wantTreeAbsent && analysis.tree != nil {
				t.Fatal("forced fallback fixture unexpectedly retained a concrete tree")
			}
			for _, symbol := range []string{"afterLine", "afterBlock"} {
				definition := swiftTestFirstDefinition(t, analysis.definitions, symbol)
				if !definition.ownsScope || definition.scopeStart != definition.line {
					t.Errorf("trailing doc lookalike widened %q scope: %#v",
						symbol, definition)
				}
			}
			documented := swiftTestFirstDefinition(t, analysis.definitions, "documented")
			docLine := swiftTestLineContaining(t, lines, "/// Actual documentation")
			if !documented.ownsScope || documented.scopeStart != docLine {
				t.Errorf("line-leading documentation did not attach: %#v, want start %d",
					documented, docLine)
			}
			swiftTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
		})
	}
}

func TestSwiftHardDeclarationNamesRequireBackticks(t *testing.T) {
	t.Parallel()

	hardNames := []string{"as", "await", "Any", "nonisolated", "yield"}
	contextualNames := []string{
		"actor", "async", "consume", "discard", "each", "lazy", "repeat", "package",
		"borrowing", "consuming",
	}
	var body strings.Builder
	body.WriteString("struct KeywordNames {\n")
	for _, name := range hardNames {
		fmt.Fprintf(&body, "    func %s() {}\n", name)
	}
	for _, name := range hardNames {
		fmt.Fprintf(&body, "    func `%s`() {}\n", name)
	}
	body.WriteString("}\n")
	for _, name := range contextualNames {
		fmt.Fprintf(&body, "let %s = 1\n", name)
	}
	body.WriteString("struct KeywordTail {}\n")

	hashes := strings.Repeat("#", swiftMaximumConcreteRawDelimiterHashes+1)
	fallbackPrefix := "let opaque = " + hashes + `"literal"` + hashes + "\n"
	for _, test := range []struct {
		name           string
		prefix         string
		wantTreeAbsent bool
	}{
		{name: "concrete eligible"},
		{name: "forced lexical fallback", prefix: fallbackPrefix, wantTreeAbsent: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			source := test.prefix + body.String()
			lines := swiftTestLines(source)
			analysis := analyzeSwiftSource(strings.TrimSuffix(source, "\n"), len(lines))
			if test.wantTreeAbsent && analysis.tree != nil {
				t.Fatal("forced fallback fixture unexpectedly retained a concrete tree")
			}
			counts := make(map[string]int)
			for _, definition := range analysis.definitions {
				counts[definition.symbol]++
			}
			for _, name := range hardNames {
				if counts[name] != 1 {
					t.Errorf("hard name %q definition count = %d, want quoted declaration only: %#v",
						name, counts[name], analysis.definitions)
				}
			}
			for _, name := range contextualNames {
				if counts[name] != 1 {
					t.Errorf("contextual identifier %q definition count = %d, want 1: %#v",
						name, counts[name], analysis.definitions)
				}
			}
			swiftTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
		})
	}
}

func TestSwiftKeywordOccurrenceCountingUsesLexicalContext(t *testing.T) {
	t.Parallel()

	counter := newSwiftLanguage()
	syntax := map[string]string{
		"as":          "let cast = value as Any",
		"await":       "let result = await operation()",
		"Any":         "let value: Any",
		"nonisolated": "nonisolated func work() {}",
		"yield":       "yield &storage",
	}
	for symbol, line := range syntax {
		if got := counter.countSymbolOccurrences(line, symbol); got != 0 {
			t.Errorf("keyword syntax %q count in %q = %d, want 0", symbol, line, got)
		}
		quotedAndMember := "`" + symbol + "`() ; object." + symbol
		if got := counter.countSymbolOccurrences(quotedAndMember, symbol); got != 2 {
			t.Errorf("quoted/member %q count in %q = %d, want 2",
				symbol, quotedAndMember, got)
		}
	}
}

func TestSwiftQuotedIdentifiersRequireClosingDelimiterAndLegalContents(t *testing.T) {
	t.Parallel()

	malformed := []struct {
		name        string
		declaration string
	}{
		{name: "unterminated", declaration: "struct `Foo"},
		{name: "qualified contents", declaration: "struct `Foo.Bar` {}"},
		{name: "next line separator", declaration: "struct `Foo\u0085Bar` {}"},
		{name: "line separator", declaration: "struct `Foo\u2028Bar` {}"},
		{name: "paragraph separator", declaration: "struct `Foo\u2029Bar` {}"},
	}
	hashes := strings.Repeat("#", swiftMaximumConcreteRawDelimiterHashes+1)
	fallbackPrefix := "let opaque = " + hashes + `"literal"` + hashes + "\n"
	modes := []struct {
		name           string
		prefix         string
		wantTreeAbsent bool
	}{
		{name: "concrete eligible"},
		{name: "forced lexical fallback", prefix: fallbackPrefix, wantTreeAbsent: true},
	}

	for _, mode := range modes {
		t.Run("valid keyword/"+mode.name, func(t *testing.T) {
			t.Parallel()

			source := mode.prefix + "struct `class` {}\nstruct QuotedTail {}\n"
			lines := swiftTestLines(source)
			analysis := analyzeSwiftSource(strings.TrimSuffix(source, "\n"), len(lines))
			if mode.wantTreeAbsent && analysis.tree != nil {
				t.Fatal("forced fallback fixture unexpectedly retained a concrete tree")
			}
			for _, required := range []string{"class", "QuotedTail"} {
				if !slices.Contains(swiftTestDefinitionSymbols(analysis.definitions), required) {
					t.Errorf("valid quoted identifier lost %q: %#v",
						required, analysis.definitions)
				}
			}
			swiftTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
		})

		for _, malformedCase := range malformed {
			t.Run(malformedCase.name+"/"+mode.name, func(t *testing.T) {
				t.Parallel()

				source := mode.prefix + malformedCase.declaration +
					"\nstruct QuotedTail {}\n"
				lines := swiftTestLines(source)
				analysis := analyzeSwiftSource(strings.TrimSuffix(source, "\n"), len(lines))
				if mode.wantTreeAbsent && analysis.tree != nil {
					t.Fatal("forced fallback fixture unexpectedly retained a concrete tree")
				}
				want := []string{"QuotedTail"}
				if mode.prefix != "" {
					want = append([]string{"opaque"}, want...)
				}
				if got := swiftTestDefinitionSymbols(analysis.definitions); !slices.Equal(got, want) {
					t.Errorf("malformed quoted identifier definitions = %#v, want %#v",
						got, want)
				}
				swiftTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
			})
		}
	}
}

func TestSwiftDeclarationKeywordLocationsIgnoreUnicodeAttributeSuffixes(t *testing.T) {
	t.Parallel()

	const body = `struct Located {
    @πinit
    init() {}
    @πfunc
    static func == (lhs: Self, rhs: Self) -> Bool { true }
}
`
	lexed := lexSwift(body)
	tree, ok := parseSwiftSyntax(body, lexed)
	if !ok || !validateSwiftSyntaxTree(tree, len(body)) {
		t.Fatal("Unicode attribute fixture did not produce a validated concrete tree")
	}
	if spans := swiftSyntaxErrorSpans(tree, len(body)); len(spans) != 0 {
		t.Fatalf("Unicode attribute fixture recovery spans = %#v, want none", spans)
	}

	hashes := strings.Repeat("#", swiftMaximumConcreteRawDelimiterHashes+1)
	fallbackPrefix := "let opaque = " + hashes + `"literal"` + hashes + "\n"
	for _, test := range []struct {
		name           string
		prefix         string
		wantTreeAbsent bool
	}{
		{name: "concrete"},
		{name: "forced lexical fallback", prefix: fallbackPrefix, wantTreeAbsent: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			source := test.prefix + body
			lines := swiftTestLines(source)
			analysis := analyzeSwiftSource(strings.TrimSuffix(source, "\n"), len(lines))
			if test.wantTreeAbsent && analysis.tree != nil {
				t.Fatal("forced fallback fixture unexpectedly retained a concrete tree")
			}
			for _, expected := range []struct {
				symbol string
				marker string
			}{
				{symbol: "init", marker: "    init()"},
				{symbol: "==", marker: "    static func =="},
			} {
				matches := make([]sourceDefinition, 0, 1)
				for _, definition := range analysis.definitions {
					if definition.symbol == expected.symbol {
						matches = append(matches, definition)
					}
				}
				if len(matches) != 1 {
					t.Errorf("%q definition count = %d, want 1: %#v",
						expected.symbol, len(matches), analysis.definitions)
					continue
				}
				lineNo := swiftTestLineContaining(t, lines, expected.marker)
				wantColumn := strings.Index(lines[lineNo-1], expected.symbol) + 1
				if matches[0].line != lineNo || matches[0].column != wantColumn {
					t.Errorf("%q location = %d:%d, want %d:%d: %#v",
						expected.symbol, matches[0].line, matches[0].column,
						lineNo, wantColumn, matches[0])
				}
			}
			swiftTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
		})
	}
}

func TestSwiftClassMemberModifiersRetainCallableAndComputedMembers(t *testing.T) {
	t.Parallel()

	const body = `class Factory {
    class func make() { let local = 1 }
    class var shared: Int { let getterLocal = 1; return getterLocal }
    class subscript(index: Int) -> Int { let subLocal = index; return subLocal }
}
`
	hashes := strings.Repeat("#", swiftMaximumConcreteRawDelimiterHashes+1)
	fallbackPrefix := "let opaque = " + hashes + `"literal"` + hashes + "\n"
	for _, test := range []struct {
		name           string
		prefix         string
		wantTreeAbsent bool
	}{
		{name: "concrete"},
		{name: "forced lexical fallback", prefix: fallbackPrefix, wantTreeAbsent: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			source := test.prefix + body
			lines := swiftTestLines(source)
			analysis := analyzeSwiftSource(strings.TrimSuffix(source, "\n"), len(lines))
			if test.wantTreeAbsent && analysis.tree != nil {
				t.Fatal("forced fallback fixture unexpectedly retained a concrete tree")
			}
			symbols := swiftTestDefinitionSymbols(analysis.definitions)
			for _, required := range []string{"Factory", "make", "shared", "subscript"} {
				if !slices.Contains(symbols, required) {
					t.Errorf("class member modifier lost %q: %#v", required, analysis.definitions)
				}
			}
			for _, local := range []string{"local", "getterLocal", "subLocal", "index"} {
				if slices.Contains(symbols, local) {
					t.Errorf("class member local/parameter %q became a definition: %#v",
						local, analysis.definitions)
				}
			}
			for _, symbol := range []string{"make", "shared", "subscript"} {
				definition := swiftTestFirstDefinition(t, analysis.definitions, symbol)
				if !definition.ownsScope || definition.scopeStart != definition.line ||
					definition.scopeEnd != definition.line {
					t.Errorf("one-line class member %q scope = %#v, want owning line",
						symbol, definition)
				}
			}
			swiftTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
		})
	}
}

func TestSwiftMalformedDeclarationsRequireImmediateNames(t *testing.T) {
	t.Parallel()

	malformed := []struct {
		name        string
		declaration string
		forbidden   []string
	}{
		{
			name: "typealias", declaration: "typealias = Phantom",
			forbidden: []string{"Phantom"},
		},
		{
			name: "struct", declaration: "struct <T> Phantom {}",
			forbidden: []string{"T", "Phantom"},
		},
		{
			name: "macro", declaration: "macro (x: Int) = Phantom",
			forbidden: []string{"x", "Int", "Phantom"},
		},
		{
			name: "precedence group", declaration: "precedencegroup { associativity: left }",
			forbidden: []string{"associativity", "left"},
		},
		{
			name: "function attribute", declaration: "func @A phantom() {}",
			forbidden: []string{"A", "phantom"},
		},
		{
			name: "operator", declaration: "prefix operator : P +",
			forbidden: []string{"P", "+"},
		},
	}
	hashes := strings.Repeat("#", swiftMaximumConcreteRawDelimiterHashes+1)
	fallbackPrefix := "let opaque = " + hashes + `"literal"` + hashes + "\n"
	for _, mode := range []struct {
		name           string
		prefix         string
		wantTreeAbsent bool
	}{
		{name: "concrete eligible"},
		{name: "forced lexical fallback", prefix: fallbackPrefix, wantTreeAbsent: true},
	} {
		for _, malformedCase := range malformed {
			t.Run(malformedCase.name+"/"+mode.name, func(t *testing.T) {
				t.Parallel()

				source := mode.prefix + malformedCase.declaration +
					"\nstruct ImmediateTail { func recovered() {} }\n"
				lines := swiftTestLines(source)
				analysis := analyzeSwiftSource(strings.TrimSuffix(source, "\n"), len(lines))
				if mode.wantTreeAbsent && analysis.tree != nil {
					t.Fatal("forced fallback fixture unexpectedly retained a concrete tree")
				}
				symbols := swiftTestDefinitionSymbols(analysis.definitions)
				for _, required := range []string{"ImmediateTail", "recovered"} {
					if !slices.Contains(symbols, required) {
						t.Errorf("malformed declaration lost tail %q: %#v",
							required, analysis.definitions)
					}
				}
				for _, forbidden := range malformedCase.forbidden {
					if slices.Contains(symbols, forbidden) {
						t.Errorf("malformed declaration promoted %q: %#v",
							forbidden, analysis.definitions)
					}
				}
				swiftTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
			})
		}
	}
}

func TestSwiftDeclarationPrefixesMustBeAttributesOrModifiers(t *testing.T) {
	t.Parallel()

	malformed := []struct {
		name        string
		declaration string
		forbidden   []string
	}{
		{
			name: "identifier prefix", declaration: "foo struct Phantom {}",
			forbidden: []string{"foo", "Phantom"},
		},
		{
			name: "expression prefix", declaration: "value + func fake() {}",
			forbidden: []string{"value", "+", "fake"},
		},
		{
			name:        "malformed attribute prefix",
			declaration: "@Broken value struct Phantom {}",
			forbidden:   []string{"Broken", "value", "Phantom"},
		},
	}
	const valid = `@Module.Attribute(flag: nested(value: 1)) public final struct ValidPrefix {
    func kept() {}
}
`
	hashes := strings.Repeat("#", swiftMaximumConcreteRawDelimiterHashes+1)
	fallbackPrefix := "let opaque = " + hashes + `"literal"` + hashes + "\n"
	modes := []struct {
		name           string
		prefix         string
		wantTreeAbsent bool
	}{
		{name: "concrete eligible"},
		{name: "forced lexical fallback", prefix: fallbackPrefix, wantTreeAbsent: true},
	}

	for _, mode := range modes {
		t.Run("valid/"+mode.name, func(t *testing.T) {
			t.Parallel()

			source := mode.prefix + valid
			lines := swiftTestLines(source)
			analysis := analyzeSwiftSource(strings.TrimSuffix(source, "\n"), len(lines))
			if mode.wantTreeAbsent && analysis.tree != nil {
				t.Fatal("forced fallback fixture unexpectedly retained a concrete tree")
			}
			for _, required := range []string{"ValidPrefix", "kept"} {
				if !slices.Contains(swiftTestDefinitionSymbols(analysis.definitions), required) {
					t.Errorf("valid declaration prefix lost %q: %#v",
						required, analysis.definitions)
				}
			}
			swiftTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
		})

		for _, malformedCase := range malformed {
			t.Run(malformedCase.name+"/"+mode.name, func(t *testing.T) {
				t.Parallel()

				source := mode.prefix + malformedCase.declaration +
					"\nstruct PrefixTail { func recovered() {} }\n"
				lines := swiftTestLines(source)
				analysis := analyzeSwiftSource(strings.TrimSuffix(source, "\n"), len(lines))
				if mode.wantTreeAbsent && analysis.tree != nil {
					t.Fatal("forced fallback fixture unexpectedly retained a concrete tree")
				}
				symbols := swiftTestDefinitionSymbols(analysis.definitions)
				for _, required := range []string{"PrefixTail", "recovered"} {
					if !slices.Contains(symbols, required) {
						t.Errorf("malformed prefix lost tail %q: %#v",
							required, analysis.definitions)
					}
				}
				for _, forbidden := range malformedCase.forbidden {
					if slices.Contains(symbols, forbidden) {
						t.Errorf("malformed prefix promoted %q: %#v",
							forbidden, analysis.definitions)
					}
				}
				swiftTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
			})
		}
	}
}

func TestSwiftMalformedSameLinePrefixesDoNotLeakScopesOrImports(t *testing.T) {
	t.Parallel()

	const body = `foo struct Phantom {
    Target()
}
struct Real {
    func visible() { Target() }
}
foo import Foundation
import Dispatch
struct PrefixBoundaryTail {}
`
	hashes := strings.Repeat("#", swiftMaximumConcreteRawDelimiterHashes+1)
	fallbackPrefix := "let opaque = " + hashes + `"literal"` + hashes + "\n"
	for _, test := range []struct {
		name           string
		prefix         string
		wantTreeAbsent bool
	}{
		{name: "concrete"},
		{name: "forced lexical fallback", prefix: fallbackPrefix, wantTreeAbsent: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			source := test.prefix + body
			lines := swiftTestLines(source)
			parsedSource := strings.TrimSuffix(source, "\n")
			analysis := analyzeSwiftSource(parsedSource, len(lines))
			if test.wantTreeAbsent && analysis.tree != nil {
				t.Fatal("forced fallback fixture unexpectedly retained a concrete tree")
			}
			symbols := swiftTestDefinitionSymbols(analysis.definitions)
			for _, required := range []string{"Real", "visible", "PrefixBoundaryTail"} {
				if !slices.Contains(symbols, required) {
					t.Errorf("same-line prefix recovery lost %q: %#v",
						required, analysis.definitions)
				}
			}
			for _, forbidden := range []string{"Phantom", "Target", "Foundation"} {
				if slices.Contains(symbols, forbidden) {
					t.Errorf("same-line malformed prefix promoted %q: %#v",
						forbidden, analysis.definitions)
				}
			}

			backend := prepareLanguageBackend(newSwiftLanguage(), lines)
			malformedBodyLine := swiftTestNthLineContaining(t, lines, "Target()", 1)
			if start, end := backend.enclosingScope(lines, malformedBodyLine); start != malformedBodyLine ||
				end != malformedBodyLine {
				t.Errorf("malformed-prefix enclosing scope = %d-%d, want isolated line %d",
					start, end, malformedBodyLine)
			}
			if start, end := backend.(navigationScopeResolver).navigationScope(
				lines, malformedBodyLine,
			); start != malformedBodyLine || end != malformedBodyLine {
				t.Errorf("malformed-prefix navigation scope = %d-%d, want isolated line %d",
					start, end, malformedBodyLine)
			}
			visibleLine := swiftTestLineContaining(t, lines, "func visible")
			if start, end := backend.enclosingScope(lines, visibleLine); start != visibleLine ||
				end != visibleLine {
				t.Errorf("valid newline-tail function scope = %d-%d, want %d-%d",
					start, end, visibleLine, visibleLine)
			}
			validImportLine := swiftTestLineContaining(t, lines, "import Dispatch")
			if start, end, ok := backend.importRange(lines); !ok || start != validImportLine ||
				end != validImportLine {
				t.Errorf("same-line malformed import range = %d-%d, %t; want %d-%d, true",
					start, end, ok, validImportLine, validImportLine)
			}

			if !test.wantTreeAbsent {
				malformedStart := swiftTestLineContaining(t, lines, "foo struct Phantom")
				malformedEnd := swiftTestLineContaining(t, lines, "struct Real") - 1
				for _, scope := range swiftTreeScopes(parsedSource, len(lines), analysis.tree) {
					if scope.start <= malformedEnd && scope.end >= malformedStart {
						t.Errorf("concrete malformed-prefix scope leaked: %#v", scope)
					}
				}
				wantImports := []cLineSpan{{start: validImportLine, end: validImportLine}}
				if got := swiftTreeImports(parsedSource, len(lines), analysis.tree); !reflect.DeepEqual(
					got, wantImports,
				) {
					t.Errorf("concrete same-line import spans = %#v, want %#v",
						got, wantImports)
				}
			}
			swiftTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
		})
	}
}

func TestSwiftLexicalFallbackSwitchExpressionOwnsCaseAndNavigationScopes(t *testing.T) {
	t.Parallel()

	hashes := strings.Repeat("#", swiftMaximumConcreteRawDelimiterHashes+1)
	source := "let opaque = " + hashes + `"literal"` + hashes + `
let result = switch value {
case 0:
    10
case 1:
    20
default:
    30
}
struct SwitchTail {}
`
	lines := swiftTestLines(source)
	backend := prepareLanguageBackend(newSwiftLanguage(), lines)
	analysis := analyzeSwiftSource(strings.TrimSuffix(source, "\n"), len(lines))
	if analysis.tree != nil {
		t.Fatal("over-hash fixture unexpectedly retained a concrete syntax tree")
	}
	for _, required := range []string{"opaque", "result", "SwitchTail"} {
		if !slices.Contains(swiftTestDefinitionSymbols(analysis.definitions), required) {
			t.Errorf("switch-expression fallback lost %q: %#v", required, analysis.definitions)
		}
	}
	result := swiftTestFirstDefinition(t, analysis.definitions, "result")
	wantStart := swiftTestLineContaining(t, lines, "let result")
	wantEnd := swiftTestLineContaining(t, lines, "struct SwitchTail") - 1
	if !result.ownsScope || result.scopeStart != wantStart || result.scopeEnd != wantEnd {
		t.Fatalf("switch-expression property scope = %#v, want owning %d-%d",
			result, wantStart, wantEnd)
	}

	resolver := backend.(navigationScopeResolver)
	cases := []struct {
		valueMarker string
		startMarker string
		endMarker   string
	}{
		{valueMarker: "    10", startMarker: "case 0", endMarker: "case 1"},
		{valueMarker: "    20", startMarker: "case 1", endMarker: "default"},
		{valueMarker: "    30", startMarker: "default", endMarker: "struct SwitchTail"},
	}
	for _, switchCase := range cases {
		lineNo := swiftTestLineContaining(t, lines, switchCase.valueMarker)
		caseStart := swiftTestLineContaining(t, lines, switchCase.startMarker)
		caseEnd := swiftTestLineContaining(t, lines, switchCase.endMarker)
		if switchCase.endMarker == "struct SwitchTail" {
			caseEnd--
		}
		if start, end := backend.enclosingScope(lines, lineNo); start != caseStart || end != caseEnd {
			t.Errorf("switch case scope at line %d = %d-%d, want %d-%d",
				lineNo, start, end, caseStart, caseEnd)
		}
		if start, end := resolver.navigationScope(lines, lineNo); start != wantStart || end != wantEnd {
			t.Errorf("switch navigation scope at line %d = %d-%d, want %d-%d",
				lineNo, start, end, wantStart, wantEnd)
		}
	}
	swiftTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
}

func TestSwiftTopLevelDestructuringDoesNotPromoteInitializerCall(t *testing.T) {
	t.Parallel()

	const source = `struct Before {}
let ordinary = pair()
let (left, right) = pair()
struct After {}
`
	lines := swiftTestLines(source)
	definitions := newSwiftLanguage().sourceDefinitions(lines)
	want := []string{"Before", "ordinary", "After"}
	if got := swiftTestDefinitionSymbols(definitions); !slices.Equal(got, want) {
		t.Fatalf("destructuring definitions = %#v, want %#v", got, want)
	}
	for _, forbidden := range []string{"left", "right", "pair"} {
		if slices.Contains(swiftTestDefinitionSymbols(definitions), forbidden) {
			t.Errorf("destructuring/initializer name %q became a definition: %#v",
				forbidden, definitions)
		}
	}
	swiftTestAssertDefinitionCoordinates(t, lines, definitions)
}

func TestSwiftOrdinaryUnterminatedStringStopsAtPhysicalNewline(t *testing.T) {
	t.Parallel()

	const source = `struct Before {}
let broken = "unterminated Target() // still a string
struct After {
    func tail() { Target() }
}
`
	lines := swiftTestLines(source)
	backend := prepareLanguageBackend(newSwiftLanguage(), lines)
	definitions := backend.sourceDefinitions(lines)
	want := []string{"Before", "broken", "After", "tail"}
	if got := swiftTestDefinitionSymbols(definitions); !slices.Equal(got, want) {
		t.Fatalf("unterminated ordinary string definitions = %#v, want %#v", got, want)
	}

	masked := backend.searchLines(lines, true, true)
	swiftTestAssertLineWidths(t, lines, masked)
	counter := backend.(symbolOccurrenceCounter)
	if got := counter.countSymbolOccurrences(masked[1], "Target"); got != 0 {
		t.Errorf("unterminated literal retained Target: %q", masked[1])
	}
	if got := counter.countSymbolOccurrences(masked[3], "Target"); got != 1 {
		t.Errorf("tail code lost Target: %q", masked[3])
	}
}

func TestSwiftUnterminatedMultilineLiteralAndNestedCommentOwnTailToEOF(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		source     string
		want       []string
		forbidden  []string
		maskString bool
	}{
		{
			name: "multiline string",
			source: `struct BeforeString {}
let broken = """
struct HiddenInString {
    func phantom() { Target() }
}
`,
			want:       []string{"BeforeString", "broken"},
			forbidden:  []string{"HiddenInString", "phantom", "Target"},
			maskString: true,
		},
		{
			name: "raw multiline string",
			source: `struct BeforeRaw {}
let broken = ###"""
struct HiddenInRaw {
    func phantom() { Target() }
}
`,
			want:       []string{"BeforeRaw", "broken"},
			forbidden:  []string{"HiddenInRaw", "phantom", "Target"},
			maskString: true,
		},
		{
			name: "nested block comment",
			source: `struct BeforeComment {}
/* outer
    /* nested */
    struct HiddenInComment {
        func phantom() { Target() }
    }
`,
			want:      []string{"BeforeComment"},
			forbidden: []string{"HiddenInComment", "phantom", "Target"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			lines := swiftTestLines(test.source)
			backend := prepareLanguageBackend(newSwiftLanguage(), lines)
			definitions := backend.sourceDefinitions(lines)
			if got := swiftTestDefinitionSymbols(definitions); !slices.Equal(got, test.want) {
				t.Fatalf("opaque-tail definitions = %#v, want %#v", got, test.want)
			}
			for _, forbidden := range test.forbidden {
				if slices.Contains(swiftTestDefinitionSymbols(definitions), forbidden) {
					t.Errorf("opaque tail promoted %q: %#v", forbidden, definitions)
				}
			}
			masked := backend.searchLines(lines, !test.maskString, test.maskString)
			swiftTestAssertLineWidths(t, lines, masked)
			for _, forbidden := range test.forbidden {
				if strings.Contains(strings.Join(masked, "\n"), forbidden) {
					t.Errorf("opaque tail mask retained %q: %#v", forbidden, masked)
				}
			}
		})
	}
}

func TestSwiftMalformedSourcesRecoverIndependentDeclarationsWithoutCallPhantoms(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		source    string
		required  []string
		forbidden []string
	}{
		{
			name: "broken parameter list",
			source: `struct Owner {
    func before() {}
    func broken(
    ???
    func after() { Target() }
}
`,
			required:  []string{"Owner", "before", "after"},
			forbidden: []string{"broken", "Target"},
		},
		{
			name: "stray closing delimiters",
			source: `struct Before {}
} ] )
struct After { func recovered() {} }
`,
			required: []string{"Before", "After", "recovered"},
		},
		{
			name: "broken generic and where clause",
			source: `struct Before {}
struct Broken<T
func tail<R(_ value: R) where R Comparable<R> { service.Client.call() }
struct After { func recovered() {} }
`,
			required:  []string{"Before", "After", "recovered"},
			forbidden: []string{"Broken", "T", "R", "Comparable", "Client", "call"},
		},
		{
			name: "broken attribute",
			source: `struct Before {}
@Broken(
struct After { func recovered() {} }
`,
			required:  []string{"Before", "After", "recovered"},
			forbidden: []string{"Broken"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			lines := swiftTestLines(test.source)
			backend := prepareLanguageBackend(newSwiftLanguage(), lines)
			definitions := backend.sourceDefinitions(lines)
			symbols := swiftTestDefinitionSymbols(definitions)
			for _, required := range test.required {
				if !slices.Contains(symbols, required) {
					t.Errorf("malformed source lost %q: %#v", required, definitions)
				}
			}
			for _, forbidden := range test.forbidden {
				if slices.Contains(symbols, forbidden) {
					t.Errorf("malformed source promoted %q: %#v", forbidden, definitions)
				}
			}
			swiftTestAssertDefinitionCoordinates(t, lines, definitions)
		})
	}
}

func TestSwiftInspectSelectsTerminalExpressionSymbols(t *testing.T) {
	t.Parallel()

	const source = `final class Service {
    @objc func run() {}

    func inspect() {
        let request = client.api.request()
        let built = Factory.make()
        let selected = ModuleA::getValue()
        let member = value.member
        let path = \Service.run
        let selector = #selector(Service.run)
        let expanded = #myMacro(argument)
        let interpolated = "value: \(Factory.make())"
    }
}
`
	lines := swiftTestLines(source)
	root := t.TempDir()
	writeFile(t, root, "Inspect.swift", source)
	view := mustView(t, root)
	tests := []struct {
		marker string
		want   string
	}{
		{marker: "client.api.request", want: "request"},
		{marker: "Factory.make", want: "make"},
		{marker: "ModuleA::getValue", want: "getValue"},
		{marker: "value.member", want: "member"},
		{marker: `\Service.run`, want: "run"},
		{marker: "#selector", want: "run"},
		{marker: "#myMacro", want: "myMacro"},
		{marker: "interpolated", want: "make"},
	}
	for _, test := range tests {
		lineNo := swiftTestLineContaining(t, lines, test.marker)
		t.Run(fmt.Sprintf("line_%d_%s", lineNo, test.want), func(t *testing.T) {
			t.Parallel()

			response, err := view.Inspect(
				fmt.Sprintf("Inspect.swift:%d", lineNo),
				Options{Include: IncludeScope, Return: ReturnScope},
			)
			if err != nil {
				t.Fatal(err)
			}
			if response.Symbol != test.want || len(response.Results) != 1 ||
				response.Results[0].Scope != "inspect" ||
				response.Results[0].StartLine != 4 || response.Results[0].EndLine != 13 {
				t.Fatalf("Inspect line %d = %#v, want %q in inspect at 4-13",
					lineNo, response, test.want)
			}
		})
	}
}

func TestSwiftNestedCommentsMaskWithoutChangingPhysicalCoordinates(t *testing.T) {
	t.Parallel()

	const source = `struct Comments {
    /* outer Target
       /* nested Target */
       still Target */
    let value = Target()
    // Target in line comment
    let text = "Target in string"
}
`
	lines := swiftTestLines(source)
	backend := prepareLanguageBackend(newSwiftLanguage(), lines)
	commentsMasked := backend.searchLines(lines, true, false)
	stringsMasked := backend.searchLines(lines, false, true)
	swiftTestAssertLineWidths(t, lines, commentsMasked)
	swiftTestAssertLineWidths(t, lines, stringsMasked)
	for _, marker := range []string{"outer Target", "nested Target", "still Target", "line comment"} {
		lineNo := swiftTestLineContaining(t, lines, marker)
		if strings.Contains(commentsMasked[lineNo-1], "Target") {
			t.Errorf("comment mask retained Target on line %d: %q", lineNo, commentsMasked[lineNo-1])
		}
		if !strings.Contains(stringsMasked[lineNo-1], "Target") {
			t.Errorf("string-only mask removed comment Target on line %d: %q", lineNo, stringsMasked[lineNo-1])
		}
	}
	stringLine := swiftTestLineContaining(t, lines, "Target in string")
	if !strings.Contains(commentsMasked[stringLine-1], "Target") ||
		strings.Contains(stringsMasked[stringLine-1], "Target") {
		t.Fatalf("comment/string masking confused literal line: comments=%q strings=%q",
			commentsMasked[stringLine-1], stringsMasked[stringLine-1])
	}
}
