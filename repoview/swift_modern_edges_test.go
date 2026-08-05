package repoview

import (
	"slices"
	"strings"
	"testing"
)

func TestSwiftModernDeclarationsUseStableNavigationSymbols(t *testing.T) {
	t.Parallel()

	const source = `precedencegroup ForwardPipePrecedence {
    associativity: left
    higherThan: AssignmentPrecedence
}

infix operator |>: ForwardPipePrecedence

func |> <Input, Output>(
    value: Input,
    transform: (Input) -> Output
) -> Output {
    transform(value)
}

@freestanding(expression)
macro stringify<T>(_ value: T) -> (T, String) =
    #externalMacro(module: "Macros", type: "StringifyMacro")

@attached(member, names: arbitrary)
macro Model() = #externalMacro(module: "Macros", type: "ModelMacro")

distributed actor Worker {
    distributed func fetch() async throws -> String { "value" }
}

struct Stream<each Element> {
    func consume<Failure: Error>(
        _ values: repeat each Element,
        failure: Failure
    ) throws(Failure) {
        for value in repeat each values {
            use(value)
        }
    }
}
`
	lines := swiftTestLines(source)
	definitions := newSwiftLanguage().sourceDefinitions(lines)
	want := []string{
		"ForwardPipePrecedence", "|>", "|>", "stringify", "Model", "Worker",
		"fetch", "Stream", "consume",
	}
	if got := swiftTestDefinitionSymbols(definitions); !slices.Equal(got, want) {
		t.Fatalf("modern Swift definitions = %#v, want %#v", got, want)
	}
	for _, forbidden := range []string{
		"Input", "Output", "T", "Element", "Failure", "value", "transform", "values",
		"failure", "externalMacro", "Macros", "StringifyMacro", "use",
	} {
		if slices.Contains(swiftTestDefinitionSymbols(definitions), forbidden) {
			t.Errorf("modern syntax promoted non-outline name %q: %#v", forbidden, definitions)
		}
	}
	swiftTestAssertDefinitionCoordinates(t, lines, definitions)

	for _, symbol := range []string{
		"ForwardPipePrecedence", "|>", "stringify", "Model", "Worker", "fetch",
		"Stream", "consume",
	} {
		if !swiftTestHasOwningDefinition(definitions, symbol) {
			t.Errorf("modern definition %q has no owning declaration: %#v", symbol, definitions)
		}
	}
}

func TestSwiftControlFlowResultBuildersAndCallsNeverBecomeDefinitions(t *testing.T) {
	t.Parallel()

	const source = `@main
struct DemoApp: App {
    @ViewBuilder
    var body: some View {
        VStack {
            Button("Go") { Target() }
        }
    }

    func run() async {
        if ready { Target() }
        guard let value else { return }
        for await item in stream { Target(item) }
        switch value {
        case .some(let payload): Target(payload)
        default: break
        }
        do { try Target() } catch let error { Target(error) }
        defer { Target() }
        await withTaskGroup(of: Void.self) { group in
            group.addTask { Target() }
        }
    }
}

let package = Package(
    name: "Demo",
    targets: [.target(name: "Demo")]
)
`
	definitions := newSwiftLanguage().sourceDefinitions(swiftTestLines(source))
	if got, want := swiftTestDefinitionSymbols(definitions),
		[]string{"DemoApp", "body", "run", "package"}; !slices.Equal(got, want) {
		t.Fatalf("Swift DSL/control-flow definitions = %#v, want %#v", got, want)
	}
	for _, forbidden := range []string{
		"App", "View", "VStack", "Button", "Target", "value", "item", "payload",
		"error", "group", "withTaskGroup", "addTask", "Package", "target",
	} {
		if slices.Contains(swiftTestDefinitionSymbols(definitions), forbidden) {
			t.Errorf("Swift DSL expression or binding %q became a definition: %#v",
				forbidden, definitions)
		}
	}
}

func TestSwiftRawMultilineStringsAndInterpolationPreserveOnlyExpressionCode(t *testing.T) {
	t.Parallel()

	const source = `struct Strings {
    let ordinary = "literal target \(target()) // not a comment"
    let multiline = """
        literal target
        \(target())
        """
    let raw = ##"literal target \#(FakeSingleHash()) \##(target())"##
    let rawMultiline = ##"""
        literal target
        \#(FakeSingleHash())
        \##(target())
        """##

    func target() -> Int { 1 }
    func caller() {
        target()
    }
}
`
	lines := swiftTestLines(source)
	backend := prepareLanguageBackend(newSwiftLanguage(), lines)
	definitions := backend.sourceDefinitions(lines)
	wantDefinitions := []string{
		"Strings", "ordinary", "multiline", "raw", "rawMultiline", "target", "caller",
	}
	if got := swiftTestDefinitionSymbols(definitions); !slices.Equal(got, wantDefinitions) {
		t.Fatalf("Swift string definitions = %#v, want %#v", got, wantDefinitions)
	}

	masked := backend.searchLines(lines, true, true)
	swiftTestAssertLineWidths(t, lines, masked)
	counter := backend.(symbolOccurrenceCounter)
	wantCounts := map[int]int{
		swiftTestLineContaining(t, lines, `let ordinary`):      1,
		swiftTestNthLineContaining(t, lines, `\(target())`, 2): 1,
		swiftTestLineContaining(t, lines, `\##(target())`):     1,
		swiftTestLineContaining(t, lines, `func target`):       1,
		swiftTestLineContaining(t, lines, `        target()`):  1,
	}
	// Two different strings contain a line spelled exactly "\##(target())".
	wantCounts[swiftTestNthLineContaining(t, lines, `\##(target())`, 2)] = 1
	for index, line := range masked {
		if got, want := counter.countSymbolOccurrences(line, "target"), wantCounts[index+1]; got != want {
			t.Errorf("masked line %d target count = %d, want %d; line=%q",
				index+1, got, want, line)
		}
	}
	if strings.Contains(strings.Join(masked, "\n"), "FakeSingleHash") {
		t.Fatalf("wrong-hash interpolation remained searchable: %#v", masked)
	}

	root := t.TempDir()
	writeFile(t, root, "Strings.swift", source)
	response, err := mustView(t, root).Find("target", Options{
		Include: IncludeBoth, Return: ReturnLocations,
		NoComments: true, NoStrings: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantLines := []int{
		swiftTestLineContaining(t, lines, `let ordinary`),
		swiftTestNthLineContaining(t, lines, `\(target())`, 2),
		swiftTestLineContaining(t, lines, `let raw =`),
		swiftTestNthLineContaining(t, lines, `\##(target())`, 2),
		swiftTestLineContaining(t, lines, `func target`),
		swiftTestLineContaining(t, lines, `        target()`),
	}
	wantKinds := []string{"ref", "ref", "ref", "ref", "def", "ref"}
	swiftTestAssertResultShape(t, response.Results, wantLines, wantKinds)
}

func TestSwiftRegexLiteralsDivisionAndCustomOperatorsStayDistinct(t *testing.T) {
	t.Parallel()

	const source = `precedencegroup MatchPrecedence { associativity: left }
infix operator =~: MatchPrecedence
func =~ (value: String, regex: Regex<Substring>) -> Bool {
    value.firstMatch(of: regex) != nil
}

func evaluate(value: String, total: Int, count: Int) -> Bool {
    let bare = /target\/\/still regex/
    let extended = #/target / slash // still regex/#
    let quotient = total / count
    return value =~ bare && value =~ extended && quotient > 0 && target(value)
}
`
	lines := swiftTestLines(source)
	definitions := newSwiftLanguage().sourceDefinitions(lines)
	if got, want := swiftTestDefinitionSymbols(definitions),
		[]string{"MatchPrecedence", "=~", "=~", "evaluate"}; !slices.Equal(got, want) {
		t.Fatalf("regex/operator definitions = %#v, want %#v", got, want)
	}

	backend := prepareLanguageBackend(newSwiftLanguage(), lines)
	masked := backend.searchLines(lines, true, true)
	swiftTestAssertLineWidths(t, lines, masked)
	counter := backend.(symbolOccurrenceCounter)
	for _, marker := range []string{"let bare", "let extended"} {
		lineNo := swiftTestLineContaining(t, lines, marker)
		if got := counter.countSymbolOccurrences(masked[lineNo-1], "target"); got != 0 {
			t.Errorf("regex literal on line %d retained target: %q", lineNo, masked[lineNo-1])
		}
	}
	returnLine := swiftTestLineContaining(t, lines, "return value")
	if got := counter.countSymbolOccurrences(masked[returnLine-1], "target"); got != 1 {
		t.Fatalf("code target count = %d, want 1: %q", got, masked[returnLine-1])
	}
	quotientLine := swiftTestLineContaining(t, lines, "let quotient")
	if !strings.Contains(masked[quotientLine-1], "/") {
		t.Errorf("division operator disappeared from code mask: %q", masked[quotientLine-1])
	}
	if !strings.Contains(masked[returnLine-1], "=~") {
		t.Errorf("custom operator disappeared from code mask: %q", masked[returnLine-1])
	}
}

func TestSwiftBareRegexExpressionAndOperatorDisambiguation(t *testing.T) {
	t.Parallel()

	const body = `infix operator =~
func =~ (lhs: Int, rhs: Regex<Substring>) -> Bool { true }
func bar(_ value: Int) -> Int { value }
func evaluate(_ condition: Bool, _ x: Int) {
    let addition = x + /target/
    let ternary = condition ? /firstTarget/ : /secondTarget/
    let negated = !/negatedTarget/
    let matched = x =~ /infixTarget/
    let operatorChain = bar(/x) + bar(/y)
    let trailingSpace = bar(/, /)
    let closingParenClass = /[)]validClassTarget/
    let nestedClass = /[[a])c]nestedClassTarget/
` + "    let verticalWhitespace = /\vverticalWhitespaceTarget\v/\n" +
		"    let formFeedWhitespace = /\fformFeedWhitespaceTarget\f/\n" +
		"    let nulWhitespace = /\x00nulWhitespaceTarget\x00/\n" + `}
struct RegexTail {}
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
			lexed := lexSwift(source)
			if got := !lexed.concreteEligible; got != test.wantTreeAbsent {
				t.Fatalf("concrete eligibility = %t, want %t", !got, !test.wantTreeAbsent)
			}
			lines := swiftTestLines(source)
			analysis := analyzeSwiftSource(strings.TrimSuffix(source, "\n"), len(lines))
			if test.wantTreeAbsent && analysis.tree != nil {
				t.Fatal("forced fallback fixture unexpectedly retained a concrete tree")
			} else if !test.wantTreeAbsent && analysis.tree == nil {
				t.Fatal("small regex-disambiguation fixture did not retain a concrete tree")
			}
			for _, required := range []string{"=~", "bar", "evaluate", "RegexTail"} {
				if !slices.Contains(swiftTestDefinitionSymbols(analysis.definitions), required) {
					t.Errorf("regex disambiguation lost %q: %#v", required, analysis.definitions)
				}
			}
			operatorDefinitions := 0
			for _, definition := range analysis.definitions {
				if definition.symbol == "=~" {
					operatorDefinitions++
				}
			}
			if operatorDefinitions != 2 {
				t.Errorf("custom =~ definition count = %d, want 2: %#v",
					operatorDefinitions, analysis.definitions)
			}

			backend := prepareLanguageBackend(newSwiftLanguage(), lines)
			masked := backend.searchLines(lines, true, true)
			swiftTestAssertLineWidths(t, lines, masked)
			counter := backend.(symbolOccurrenceCounter)
			for _, marker := range []string{
				"let addition", "let ternary", "let negated", "let matched",
				"let closingParenClass", "let nestedClass", "let verticalWhitespace",
				"let formFeedWhitespace", "let nulWhitespace",
			} {
				lineNo := swiftTestLineContaining(t, lines, marker)
				for _, symbol := range []string{
					"target", "firstTarget", "secondTarget", "negatedTarget", "infixTarget",
					"validClassTarget", "nestedClassTarget", "verticalWhitespaceTarget",
					"formFeedWhitespaceTarget", "nulWhitespaceTarget",
				} {
					if got := counter.countSymbolOccurrences(masked[lineNo-1], symbol); got != 0 {
						t.Errorf("regex operand line %d retained %q: %q", lineNo, symbol, masked[lineNo-1])
					}
				}
			}
			matchedLine := swiftTestLineContaining(t, lines, "let matched")
			if !strings.Contains(masked[matchedLine-1], "=~") {
				t.Errorf("custom =~ operator disappeared from regex operand line: %q",
					masked[matchedLine-1])
			}
			for _, marker := range []string{"let operatorChain", "let trailingSpace"} {
				lineNo := swiftTestLineContaining(t, lines, marker)
				if masked[lineNo-1] != lines[lineNo-1] {
					t.Errorf("operator candidate line %d was masked: got %q, want %q",
						lineNo, masked[lineNo-1], lines[lineNo-1])
				}
			}
		})
	}
}

func TestSwiftBareRegexAfterExpressionIntroducingKeywords(t *testing.T) {
	t.Parallel()

	const body = `func keywordRegexes() {
    consume /consumeTarget/
    yield /yieldTarget/
    each /eachTarget/
    repeat /repeatTarget/
}
struct KeywordRegexTail {}
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
			lexed := lexSwift(source)
			if got := !lexed.concreteEligible; got != test.wantTreeAbsent {
				t.Fatalf("concrete eligibility = %t, want %t", !got, !test.wantTreeAbsent)
			}
			lines := swiftTestLines(source)
			analysis := analyzeSwiftSource(strings.TrimSuffix(source, "\n"), len(lines))
			if test.wantTreeAbsent && analysis.tree != nil {
				t.Fatal("forced fallback fixture unexpectedly retained a concrete tree")
			} else if !test.wantTreeAbsent && analysis.tree == nil {
				t.Fatal("small keyword-regex fixture did not retain a concrete tree")
			}
			for _, required := range []string{"keywordRegexes", "KeywordRegexTail"} {
				if !slices.Contains(swiftTestDefinitionSymbols(analysis.definitions), required) {
					t.Errorf("keyword-regex recovery lost %q: %#v", required, analysis.definitions)
				}
			}
			backend := prepareLanguageBackend(newSwiftLanguage(), lines)
			masked := backend.searchLines(lines, true, true)
			swiftTestAssertLineWidths(t, lines, masked)
			counter := backend.(symbolOccurrenceCounter)
			for _, testCase := range []struct {
				keyword string
				marker  string
			}{
				{keyword: "consume", marker: "consumeTarget"},
				{keyword: "yield", marker: "yieldTarget"},
				{keyword: "each", marker: "eachTarget"},
				{keyword: "repeat", marker: "repeatTarget"},
			} {
				lineNo := swiftTestLineContaining(t, lines, testCase.keyword+" /")
				if got := counter.countSymbolOccurrences(masked[lineNo-1], testCase.marker); got != 0 {
					t.Errorf("bare regex after %s retained %q: %q",
						testCase.keyword, testCase.marker, masked[lineNo-1])
				}
			}
		})
	}
}

func TestSwiftBareRegexUnescapedSlashInsideClassDoesNotMaskTail(t *testing.T) {
	t.Parallel()

	const body = `func evaluate() {
    let slashInClass = /[)/]slashClassTarget/
}
struct SlashClassTail {}
`
	hashes := strings.Repeat("#", swiftMaximumConcreteRawDelimiterHashes+1)
	fallbackPrefix := "let opaque = " + hashes + `"literal"` + hashes + "\n"
	for _, test := range []struct {
		name   string
		prefix string
	}{
		{name: "small source"},
		{name: "forced lexical fallback", prefix: fallbackPrefix},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			source := test.prefix + body
			lines := swiftTestLines(source)
			backend := prepareLanguageBackend(newSwiftLanguage(), lines)
			masked := backend.searchLines(lines, true, true)
			swiftTestAssertLineWidths(t, lines, masked)
			lineNo := swiftTestLineContaining(t, lines, "let slashInClass")
			if got := backend.(symbolOccurrenceCounter).countSymbolOccurrences(
				masked[lineNo-1], "slashClassTarget",
			); got != 1 {
				t.Errorf("unescaped slash inside character class hid tail marker: %q",
					masked[lineNo-1])
			}
			definitions := backend.sourceDefinitions(lines)
			if !slices.Contains(swiftTestDefinitionSymbols(definitions), "SlashClassTail") {
				t.Errorf("malformed slash-class candidate swallowed independent tail: %#v",
					definitions)
			}
		})
	}
}

func TestSwiftBareRegexRejectsPhysicalLineBreakBodies(t *testing.T) {
	t.Parallel()

	const body = "func evaluateLineBreaks() {\n" +
		"    let leadingLF = /\nleadingLFTarget/\n" +
		"    let trailingLF = /trailingLFTarget\n/\n" +
		"    let leadingCR = /\rcarriageReturnTarget/\n" +
		"    let trailingCR = /trailingCarriageTarget\r/\n" +
		"}\nstruct LineBreakRegexTail {}\n"
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
			lexed := lexSwift(source)
			if got := !lexed.concreteEligible; got != test.wantTreeAbsent {
				t.Fatalf("concrete eligibility = %t, want %t", !got, !test.wantTreeAbsent)
			}
			lines := swiftTestLines(source)
			analysis := analyzeSwiftSource(strings.TrimSuffix(source, "\n"), len(lines))
			if test.wantTreeAbsent && analysis.tree != nil {
				t.Fatal("forced fallback fixture unexpectedly retained a concrete tree")
			} else if !test.wantTreeAbsent && analysis.tree == nil {
				t.Fatal("small physical-line-break fixture did not retain a concrete tree")
			}
			backend := prepareLanguageBackend(newSwiftLanguage(), lines)
			masked := backend.searchLines(lines, true, true)
			swiftTestAssertLineWidths(t, lines, masked)
			joined := strings.Join(masked, "\n")
			for _, marker := range []string{
				"leadingLFTarget", "trailingLFTarget", "carriageReturnTarget",
				"trailingCarriageTarget",
			} {
				if !strings.Contains(joined, marker) {
					t.Errorf("bare regex crossed a physical line break and hid %q: %#v",
						marker, masked)
				}
			}
			if !slices.Contains(
				swiftTestDefinitionSymbols(analysis.definitions), "LineBreakRegexTail",
			) {
				t.Errorf("physical-line-break recovery lost independent tail: %#v",
					analysis.definitions)
			}
		})
	}
}

func TestSwiftBareRegexRejectsEscapedPhysicalLineBreaks(t *testing.T) {
	t.Parallel()

	lineBreaks := []struct {
		name          string
		value         string
		requireHidden bool
	}{
		{name: "LF", value: "\n", requireHidden: true},
		{name: "CR", value: "\r"},
		{name: "CRLF", value: "\r\n", requireHidden: true},
	}
	hashes := strings.Repeat("#", swiftMaximumConcreteRawDelimiterHashes+1)
	fallbackPrefix := "let opaque = " + hashes + `"literal"` + hashes + "\n"
	for _, lineBreak := range lineBreaks {
		for _, mode := range []struct {
			name           string
			prefix         string
			wantTreeAbsent bool
		}{
			{name: "concrete"},
			{name: "forced lexical fallback", prefix: fallbackPrefix, wantTreeAbsent: true},
		} {
			t.Run(lineBreak.name+"/"+mode.name, func(t *testing.T) {
				t.Parallel()

				source := mode.prefix + "let broken = /abc\\" + lineBreak.value +
					"struct Hidden {}" + lineBreak.value + "/\n" +
					"struct EscapedLineTail { func recovered() {} }\n"
				lexed := lexSwift(source)
				if got := !lexed.concreteEligible; got != mode.wantTreeAbsent {
					t.Fatalf("concrete eligibility = %t, want %t", !got, !mode.wantTreeAbsent)
				}
				lines := swiftTestLines(source)
				analysis := analyzeSwiftSource(strings.TrimSuffix(source, "\n"), len(lines))
				if mode.wantTreeAbsent && analysis.tree != nil {
					t.Fatal("forced fallback fixture unexpectedly retained a concrete tree")
				} else if !mode.wantTreeAbsent && analysis.tree == nil {
					t.Fatal("small escaped-line-break fixture did not retain a concrete tree")
				}
				symbols := swiftTestDefinitionSymbols(analysis.definitions)
				for _, required := range []string{"EscapedLineTail", "recovered"} {
					if !slices.Contains(symbols, required) {
						t.Errorf("escaped-line-break recovery lost %q: %#v",
							required, analysis.definitions)
					}
				}
				if lineBreak.requireHidden && !slices.Contains(symbols, "Hidden") {
					t.Errorf("escaped %s recovery lost independent Hidden: %#v",
						lineBreak.name, analysis.definitions)
				}
				backend := prepareLanguageBackend(newSwiftLanguage(), lines)
				masked := backend.searchLines(lines, true, true)
				swiftTestAssertLineWidths(t, lines, masked)
				searchable := strings.Join(masked, "\n")
				for _, marker := range []string{"abc", "struct Hidden"} {
					if !strings.Contains(searchable, marker) {
						t.Errorf("invalid escaped-line-break regex masked %q: %#v", marker, masked)
					}
				}
			})
		}
	}
}

func TestSwiftExtendedRegexRequiresAValidSingleOrMultilineClose(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		source       string
		marker       string
		wantMasked   bool
		requiredTail string
	}{
		{
			name: "single line",
			source: `let value = #/singleTarget/#
struct SingleTail {}
`,
			marker: "singleTarget", wantMasked: true, requiredTail: "SingleTail",
		},
		{
			name: "multiline opening and close",
			source: `let value = #/
multiTarget
/#
struct MultiTail {}
`,
			marker: "multiTarget", wantMasked: true, requiredTail: "MultiTail",
		},
		{
			name: "single line opener cannot cross newline",
			source: `let value = #/crossTarget
/#
struct CrossTail {}
`,
			marker: "crossTarget", requiredTail: "CrossTail",
		},
		{
			name: "multiline close must start its own line",
			source: `let value = #/
inlineCloseTarget /#
struct InlineCloseTail {}
`,
			marker: "inlineCloseTarget", requiredTail: "InlineCloseTail",
		},
		{
			name: "missing multiline close",
			source: `let value = #/
missingCloseTarget
struct MissingCloseTail { func recovered() {} }
`,
			marker: "missingCloseTarget", requiredTail: "MissingCloseTail",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			lexed := lexSwift(test.source)
			if !lexed.concreteEligible {
				t.Fatal("small extended-regex fixture unexpectedly forced lexical fallback")
			}
			lines := swiftTestLines(test.source)
			analysis := analyzeSwiftSource(strings.TrimSuffix(test.source, "\n"), len(lines))
			if analysis.tree == nil {
				t.Fatal("small extended-regex fixture did not retain a concrete tree")
			}
			backend := prepareLanguageBackend(newSwiftLanguage(), lines)
			masked := backend.searchLines(lines, true, true)
			swiftTestAssertLineWidths(t, lines, masked)
			lineNo := swiftTestLineContaining(t, lines, test.marker)
			gotMasked := !strings.Contains(masked[lineNo-1], test.marker)
			if gotMasked != test.wantMasked {
				t.Errorf("extended regex marker %q masked = %t, want %t: %q",
					test.marker, gotMasked, test.wantMasked, masked[lineNo-1])
			}
			definitions := analysis.definitions
			if !slices.Contains(swiftTestDefinitionSymbols(definitions), test.requiredTail) {
				t.Errorf("extended regex recovery lost tail %q: %#v", test.requiredTail, definitions)
			}
		})
	}
}

func TestSwiftMultilineExtendedRegexCloseTrailersAndIndentationConcreteAndFallback(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                  string
		indentation           string
		trailer               string
		trailerMarker         string
		bodyMarker            string
		allowConcreteFallback bool
	}{
		{
			name: "member suffix", indentation: "    ",
			trailer: ".wholeMatch(in: input)", trailerMarker: "wholeMatch",
			bodyMarker: "memberSuffixTarget",
		},
		{
			name: "line comment", indentation: "    ",
			trailer: " // trailing comment", bodyMarker: "commentSuffixTarget",
		},
		{
			name: "following operator", indentation: "    ",
			trailer: " + other", trailerMarker: "+ other",
			bodyMarker: "operatorSuffixTarget",
		},
		{
			name: "following token", indentation: "    ",
			trailer: " as Regex<Substring>", trailerMarker: "as Regex",
			bodyMarker: "tokenSuffixTarget",
		},
		{
			name: "vertical tab indentation", indentation: "\v",
			bodyMarker: "verticalIndentTarget",
		},
		{
			name: "form feed indentation", indentation: "\f",
			bodyMarker: "formFeedIndentTarget",
		},
		{
			name: "NUL indentation", indentation: "\x00",
			bodyMarker: "nulIndentTarget", allowConcreteFallback: true,
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

				source := mode.prefix + "let value = #/\n" + test.bodyMarker + "\n" +
					test.indentation + "/#" + test.trailer +
					"\nstruct ExtendedTrailerTail { func recovered() {} }\n"
				lexed := lexSwift(source)
				if mode.wantTreeAbsent && lexed.concreteEligible {
					t.Fatal("forced fallback fixture remained concrete-eligible")
				} else if !mode.wantTreeAbsent && !test.allowConcreteFallback &&
					!lexed.concreteEligible {
					t.Fatal("small extended-regex trailer fixture is not concrete-eligible")
				}
				lines := swiftTestLines(source)
				analysis := analyzeSwiftSource(strings.TrimSuffix(source, "\n"), len(lines))
				if mode.wantTreeAbsent && analysis.tree != nil {
					t.Fatal("forced fallback fixture unexpectedly retained a concrete tree")
				} else if !mode.wantTreeAbsent && !test.allowConcreteFallback && analysis.tree == nil {
					t.Fatal("small extended-regex trailer fixture did not retain a concrete tree")
				}
				backend := prepareLanguageBackend(newSwiftLanguage(), lines)
				masked := backend.searchLines(lines, true, true)
				swiftTestAssertLineWidths(t, lines, masked)
				bodyLine := swiftTestLineContaining(t, lines, test.bodyMarker)
				if strings.Contains(masked[bodyLine-1], test.bodyMarker) {
					t.Errorf("multiline extended regex retained body marker %q: %q",
						test.bodyMarker, masked[bodyLine-1])
				}
				if test.trailerMarker != "" {
					closeLine := swiftTestLineContaining(t, lines, "/#"+test.trailer)
					if !strings.Contains(masked[closeLine-1], test.trailerMarker) {
						t.Errorf("extended-regex close hid trailer %q: %q",
							test.trailerMarker, masked[closeLine-1])
					}
				}
				definitions := analysis.definitions
				for _, required := range []string{"ExtendedTrailerTail", "recovered"} {
					if !slices.Contains(swiftTestDefinitionSymbols(definitions), required) {
						t.Errorf("extended-regex trailer recovery lost %q: %#v",
							required, definitions)
					}
				}
			})
		}
	}
}

func TestSwiftUnicodeEmojiAndEscapedIdentifiersUseExactByteCoordinates(t *testing.T) {
	t.Parallel()

	const prefix = `struct Κατάλογος {
    let café = 1
    let 🐈 = 2
    func 你好() { use(café, 🐈) }
    func `
	const suffix = "`class`() {}\n}\n"
	source := prefix + suffix
	lines := swiftTestLines(source)
	definitions := newSwiftLanguage().sourceDefinitions(lines)
	want := []string{"Κατάλογος", "café", "🐈", "你好", "class"}
	if got := swiftTestDefinitionSymbols(definitions); !slices.Equal(got, want) {
		t.Fatalf("Unicode Swift definitions = %#v, want %#v", got, want)
	}
	swiftTestAssertDefinitionCoordinates(t, lines, definitions)

	root := t.TempDir()
	writeFile(t, root, "Identifiers.swift", source)
	view := mustView(t, root)
	for _, symbol := range []string{"Κατάλογος", "café", "🐈", "你好", "class"} {
		response, err := view.Find(symbol, Options{Include: IncludeDefs, Return: ReturnLocations})
		if err != nil {
			t.Fatal(err)
		}
		if len(response.Results) != 1 || response.Results[0].Symbol != symbol {
			t.Errorf("Find(%q) = %#v, want exact definition", symbol, response.Results)
		}
	}
	for _, partial := range []string{"cafe", "🐈x", "las"} {
		response, err := view.Find(partial, Options{Include: IncludeBoth, Return: ReturnLocations})
		if err != nil {
			t.Fatal(err)
		}
		if len(response.Results) != 0 {
			t.Errorf("partial/canonicalized query %q matched: %#v", partial, response.Results)
		}
	}
}

func swiftTestNthLineContaining(t *testing.T, lines []string, marker string, occurrence int) int {
	t.Helper()
	found := 0
	for index, line := range lines {
		if !strings.Contains(line, marker) {
			continue
		}
		found++
		if found == occurrence {
			return index + 1
		}
	}
	t.Fatalf("marker %q occurrence %d is absent from source", marker, occurrence)
	return 0
}
