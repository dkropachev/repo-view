package repoview

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestSwiftMalformedBareRegexCandidatesScaleAndKeepTail(t *testing.T) {
	fixture := func(repetitions int) string {
		return "func head() {\n_ = " + strings.Repeat(`/\`, repetitions) +
			"\n}\nstruct RegexTail { func recovered() {} }\n"
	}
	measure := func(repetitions int) time.Duration {
		started := time.Now()
		walkSwiftLexically(fixture(repetitions), swiftLexicalSink{})
		return time.Since(started)
	}
	minimum := func(repetitions, attempts int) time.Duration {
		best := time.Duration(1<<63 - 1)
		for range attempts {
			best = min(best, measure(repetitions))
		}
		return best
	}

	small := minimum(4<<10, 3)
	large := minimum(16<<10, 2)
	if limit := small*10 + 20*time.Millisecond; large > limit {
		t.Fatalf("malformed bare-regex scan scaled superlinearly: 4K=%s, 16K=%s (limit %s)",
			small, large, limit)
	}

	source := fixture(16 << 10)
	if _, withinBudget := swiftWalkLexically(source, swiftLexicalSink{}); !withinBudget {
		t.Fatal("linear malformed bare-regex scan unexpectedly exhausted lookahead budget")
	}
	lines := swiftTestLines(source)
	definitions := newSwiftLanguage().sourceDefinitions(lines)
	for _, required := range []string{"head", "RegexTail", "recovered"} {
		if !slices.Contains(swiftTestDefinitionSymbols(definitions), required) {
			t.Errorf("bounded malformed-regex recovery lost %q: %#v",
				required, definitions)
		}
	}
	swiftTestAssertDefinitionCoordinates(t, lines, definitions)
}

func TestSwiftMalformedExtendedRegexLookaheadBudgetKeepsTail(t *testing.T) {
	const repetitions = 8 << 10
	candidates := strings.Repeat("#/candidate ", repetitions)
	for _, test := range []struct {
		name          string
		apparentClose string
	}{
		{name: "missing close"},
		{name: "close only on later line", apparentClose: "/#\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := "func head() {\n_ = " + candidates + "\n" + test.apparentClose +
				"}\nstruct ExtendedRegexTail { func recovered() {} }\n"

			if _, withinBudget := swiftWalkLexically(source, swiftLexicalSink{}); withinBudget {
				t.Fatal("failed extended-regex retries did not exhaust the shared lookahead budget")
			}
			lexed := lexSwift(source)
			if lexed.concreteEligible {
				t.Fatal("extended-regex lookahead-budget exhaustion remained concrete-eligible")
			}
			lines := swiftTestLines(source)
			backend := prepareLanguageBackend(newSwiftLanguage(), lines)
			definitions := backend.sourceDefinitions(lines)
			for _, required := range []string{"head", "ExtendedRegexTail", "recovered"} {
				if !slices.Contains(swiftTestDefinitionSymbols(definitions), required) {
					t.Errorf("bounded malformed-extended-regex recovery lost %q: %#v",
						required, definitions)
				}
			}
			masked := backend.searchLines(lines, true, true)
			tailLine := swiftTestLineContaining(t, lines, "struct ExtendedRegexTail")
			if !strings.Contains(masked[tailLine-1], "ExtendedRegexTail") {
				t.Errorf("malformed extended-regex scan swallowed independent tail: %q",
					masked[tailLine-1])
			}
			swiftTestAssertDefinitionCoordinates(t, lines, definitions)
			swiftTestAssertLineWidths(t, lines, masked)
		})
	}
}

func TestSwiftBareHashRunsScaleAndKeepTail(t *testing.T) {
	measure := func(count int) time.Duration {
		source := strings.Repeat("#", count) +
			"\nstruct HashTail { func recovered() {} }\n"
		started := time.Now()
		walkSwiftLexically(source, swiftLexicalSink{})
		return time.Since(started)
	}
	minimum := func(count, attempts int) time.Duration {
		best := time.Duration(1<<63 - 1)
		for range attempts {
			best = min(best, measure(count))
		}
		return best
	}

	small := minimum(4<<10, 3)
	large := minimum(16<<10, 2)
	if limit := small*10 + 20*time.Millisecond; large > limit {
		t.Fatalf("bare-hash scan scaled superlinearly: 4K=%s, 16K=%s (limit %s)",
			small, large, limit)
	}

	source := strings.Repeat("#", 16<<10) +
		"\nstruct HashTail { func recovered() {} }\n"
	lexed := lexSwift(source)
	if lexed.concreteEligible ||
		lexed.maximumRawHashCount <= swiftMaximumConcreteRawDelimiterHashes {
		t.Fatalf("16K bare-hash frontier = (eligible %t, hashes %d), want false and > %d",
			lexed.concreteEligible, lexed.maximumRawHashCount,
			swiftMaximumConcreteRawDelimiterHashes)
	}
	lines := swiftTestLines(source)
	definitions := newSwiftLanguage().sourceDefinitions(lines)
	if got, want := swiftTestDefinitionSymbols(definitions),
		[]string{"HashTail", "recovered"}; !slices.Equal(got, want) {
		t.Fatalf("bare-hash tail definitions = %#v, want %#v", got, want)
	}
}

func TestSwiftExtendedRegexHashCandidatesScaleMaskAndKeepTail(t *testing.T) {
	fixture := func(hashCount, slashCount int) string {
		hashes := strings.Repeat("#", hashCount)
		return hashes + "/" + strings.Repeat("/", slashCount) + "/" + hashes +
			"\nstruct ExtendedRegexTail { func recovered() {} }\n"
	}
	measure := func(hashCount, slashCount int) time.Duration {
		started := time.Now()
		walkSwiftLexically(fixture(hashCount, slashCount), swiftLexicalSink{})
		return time.Since(started)
	}
	minimum := func(hashCount, slashCount, attempts int) time.Duration {
		best := time.Duration(1<<63 - 1)
		for range attempts {
			best = min(best, measure(hashCount, slashCount))
		}
		return best
	}

	small := minimum(2<<10, 2<<10, 3)
	large := minimum(8<<10, 8<<10, 2)
	if limit := small*10 + 20*time.Millisecond; large > limit {
		t.Fatalf("extended-regex close checks scaled superlinearly: 2K=%s, 8K=%s (limit %s)",
			small, large, limit)
	}

	source := fixture(8<<10, 8<<10)
	lines := swiftTestLines(source)
	backend := prepareLanguageBackend(newSwiftLanguage(), lines)
	masked := backend.searchLines(lines, true, true)
	if strings.TrimSpace(masked[0]) != "" {
		t.Errorf("extended regex retained %d non-space bytes on its source line",
			len(strings.TrimSpace(masked[0])))
	}
	definitions := backend.sourceDefinitions(lines)
	if got, want := swiftTestDefinitionSymbols(definitions),
		[]string{"ExtendedRegexTail", "recovered"}; !slices.Equal(got, want) {
		t.Fatalf("extended-regex tail definitions = %#v, want %#v", got, want)
	}
	swiftTestAssertLineWidths(t, lines, masked)
}

func TestSwiftPhysicalLineHeaderContinuationsScaleLikeCompactHeaders(t *testing.T) {
	const blocks = 64
	physicalBlock := "func broken(\n" + strings.Repeat("x\n", swiftMaximumHeaderTokens-2)
	compactBlock := "func broken(" + strings.Repeat("x ", swiftMaximumHeaderTokens-2) + "\n"
	tail := "struct HeaderTail { func recovered() {} }\n"
	physical := strings.Repeat(physicalBlock, blocks) + tail
	compact := strings.Repeat(compactBlock, blocks) + tail
	measure := func(source string) (time.Duration, []sourceDefinition) {
		started := time.Now()
		definitions := analyzeSwiftLexically(source, len(swiftTestLines(source))).definitions
		return time.Since(started), definitions
	}

	compactElapsed, compactDefinitions := measure(compact)
	physicalElapsed, physicalDefinitions := measure(physical)
	if limit := compactElapsed*20 + 100*time.Millisecond; physicalElapsed > limit {
		t.Fatalf("physical-line header continuation scan = %s, compact = %s (limit %s)",
			physicalElapsed, compactElapsed, limit)
	}
	for label, definitions := range map[string][]sourceDefinition{
		"compact": compactDefinitions, "physical": physicalDefinitions,
	} {
		if got, want := swiftTestDefinitionSymbols(definitions),
			[]string{"HeaderTail", "recovered"}; !slices.Equal(got, want) {
			t.Errorf("%s long-header tail definitions = %#v, want %#v", label, got, want)
		}
	}
}

func TestSwiftLargeCommaSeparatedBindingHeaderKeepsAllNamesAndTail(t *testing.T) {
	const count = 2 << 10
	names := make([]string, count)
	for index := range names {
		names[index] = fmt.Sprintf("binding%d", index)
	}
	source := "let " + strings.Join(names, ", ") +
		"\nstruct BindingHeaderTail { func recovered() {} }\n"
	lines := swiftTestLines(source)
	definitions := analyzeSwiftLexically(source, len(lines)).definitions
	if len(definitions) != count+2 {
		t.Fatalf("large binding definitions = %d, want %d", len(definitions), count+2)
	}
	for _, index := range []int{0, count / 2, count - 1} {
		if definitions[index].symbol != names[index] {
			t.Errorf("binding %d = %q, want %q", index, definitions[index].symbol, names[index])
		}
	}
	if got := swiftTestDefinitionSymbols(definitions[len(definitions)-2:]); !slices.Equal(
		got, []string{"BindingHeaderTail", "recovered"},
	) {
		t.Fatalf("large binding tail definitions = %#v", got)
	}
	swiftTestAssertDefinitionCoordinates(t, lines, definitions)
}

func TestSwiftOverConcreteFrontiersRetainIndependentTail(t *testing.T) {
	tests := []struct {
		name   string
		source string
		want   []string
	}{
		{
			name: "byte cap",
			source: "struct ByteHead {}\n/*" +
				strings.Repeat(" struct Fake { func hidden() { Target() } } ",
					swiftMaximumConcreteParseBytes/40) +
				"*/\nstruct ByteTail { func recovered() { Target() } }\n",
			want: []string{"ByteTail", "recovered"},
		},
		{
			name: "token cap",
			source: "struct TokenHead { func work() {\n" +
				strings.Repeat("value += 1\n", swiftMaximumConcreteTokens/3+1) +
				"} }\nstruct TokenTail { func recovered() {} }\n",
			want: []string{"TokenTail", "recovered"},
		},
		{
			name: "delimiter depth",
			source: "struct DepthHead { func work() {\n" +
				strings.Repeat("{\n", swiftMaximumConcreteDelimiterDepth+1) +
				"Target()\n" +
				strings.Repeat("}\n", swiftMaximumConcreteDelimiterDepth+1) +
				"} }\nstruct DepthTail { func recovered() {} }\n",
			want: []string{"DepthTail", "recovered"},
		},
		{
			name: "conditional compilation depth",
			source: strings.Repeat("#if FEATURE\n", swiftMaximumConcreteDirectiveDepth+1) +
				"struct NestedBranch {}\n" +
				strings.Repeat("#endif\n", swiftMaximumConcreteDirectiveDepth+1) +
				"struct DirectiveTail { func recovered() {} }\n",
			want: []string{"DirectiveTail", "recovered"},
		},
		{
			name: "raw delimiter hash cap",
			source: "let opaque = " +
				strings.Repeat("#", swiftMaximumConcreteRawDelimiterHashes+1) +
				`"Fake Target"` +
				strings.Repeat("#", swiftMaximumConcreteRawDelimiterHashes+1) +
				"\nstruct HashTail { func recovered() {} }\n",
			want: []string{"HashTail", "recovered"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lexed := lexSwift(test.source)
			if lexed.concreteEligible {
				t.Fatal("over-frontier fixture remained concrete-eligible")
			}
			lines := swiftTestLines(test.source)
			definitions := newSwiftLanguage().sourceDefinitions(lines)
			symbols := swiftTestDefinitionSymbols(definitions)
			for _, want := range test.want {
				if !slices.Contains(symbols, want) {
					t.Errorf("over-frontier fallback lost %q: %#v", want, definitions)
				}
			}
			for _, phantom := range []string{"Fake", "hidden", "Target"} {
				if slices.Contains(symbols, phantom) {
					t.Errorf("over-frontier fallback promoted %q: %#v", phantom, definitions)
				}
			}
			swiftTestAssertDefinitionCoordinates(t, lines, definitions)
		})
	}
}

func TestSwiftConcreteFrontierBoundariesAreInclusive(t *testing.T) {
	atByteCap := strings.Repeat(" ", swiftMaximumConcreteParseBytes)
	if lexed := lexSwift(atByteCap); !lexed.concreteEligible {
		t.Fatal("source exactly at Swift byte cap was rejected")
	}
	if lexed := lexSwift(atByteCap + " "); lexed.concreteEligible {
		t.Fatal("source over Swift byte cap remained concrete-eligible")
	}

	atTokenCap := strings.Repeat(";", swiftMaximumConcreteTokens)
	if lexed := lexSwift(atTokenCap); !lexed.concreteEligible ||
		lexed.lexicalUnits != swiftMaximumConcreteTokens {
		t.Fatalf("source at token cap = (%t, %d), want eligible with %d units",
			lexed.concreteEligible, lexed.lexicalUnits, swiftMaximumConcreteTokens)
	}
	if lexed := lexSwift(atTokenCap + ";"); lexed.concreteEligible {
		t.Fatal("source over Swift token cap remained concrete-eligible")
	}

	atDepth := strings.Repeat("(", swiftMaximumConcreteDelimiterDepth) + "0" +
		strings.Repeat(")", swiftMaximumConcreteDelimiterDepth)
	if lexed := lexSwift(atDepth); !lexed.concreteEligible ||
		lexed.maximumDelimiterDepth != swiftMaximumConcreteDelimiterDepth {
		t.Fatalf("source at delimiter cap = (%t, %d), want eligible at %d",
			lexed.concreteEligible, lexed.maximumDelimiterDepth,
			swiftMaximumConcreteDelimiterDepth)
	}
	if lexed := lexSwift("(" + atDepth + ")"); lexed.concreteEligible {
		t.Fatal("source over Swift delimiter cap remained concrete-eligible")
	}

	atDirectiveDepth := strings.Repeat("#if FEATURE\n", swiftMaximumConcreteDirectiveDepth) +
		strings.Repeat("#endif\n", swiftMaximumConcreteDirectiveDepth)
	if lexed := lexSwift(atDirectiveDepth); !lexed.concreteEligible ||
		lexed.maximumDirectiveDepth != swiftMaximumConcreteDirectiveDepth {
		t.Fatalf("source at directive cap = (%t, %d), want eligible at %d",
			lexed.concreteEligible, lexed.maximumDirectiveDepth,
			swiftMaximumConcreteDirectiveDepth)
	}
	if lexed := lexSwift("#if FEATURE\n" + atDirectiveDepth + "#endif\n"); lexed.concreteEligible {
		t.Fatal("source over Swift directive cap remained concrete-eligible")
	}

	atHashes := strings.Repeat("#", swiftMaximumConcreteRawDelimiterHashes) + `"value"` +
		strings.Repeat("#", swiftMaximumConcreteRawDelimiterHashes)
	if lexed := lexSwift(atHashes); !lexed.concreteEligible ||
		lexed.maximumRawHashCount != swiftMaximumConcreteRawDelimiterHashes {
		t.Fatalf("source at raw-hash cap = (%t, %d), want eligible at %d",
			lexed.concreteEligible, lexed.maximumRawHashCount,
			swiftMaximumConcreteRawDelimiterHashes)
	}
	overHashes := "#" + atHashes + "#"
	if lexed := lexSwift(overHashes); lexed.concreteEligible {
		t.Fatal("source over Swift raw-hash cap remained concrete-eligible")
	}
}

func TestSwiftDocumentationCommentRunsScaleLinearly(t *testing.T) {
	fixture := func(count int) string {
		return strings.Repeat("/**/", count) +
			"\nstruct DocumentationTail { func recovered() {} }\n"
	}
	measure := func(count int) (time.Duration, []sourceDefinition) {
		source := fixture(count)
		started := time.Now()
		definitions := analyzeSwiftLexically(source, len(swiftTestLines(source))).definitions
		return time.Since(started), definitions
	}
	minimum := func(count, attempts int) (time.Duration, []sourceDefinition) {
		best := time.Duration(1<<63 - 1)
		var bestDefinitions []sourceDefinition
		for range attempts {
			elapsed, definitions := measure(count)
			if elapsed < best {
				best = elapsed
				bestDefinitions = definitions
			}
		}
		return best, bestDefinitions
	}

	small, _ := minimum(8<<10, 3)
	large, definitions := minimum(32<<10, 2)
	if limit := small*10 + 20*time.Millisecond; large > limit {
		t.Fatalf("documentation-comment scan scaled superlinearly: 8K=%s, 32K=%s (limit %s)",
			small, large, limit)
	}
	if got, want := swiftTestDefinitionSymbols(definitions),
		[]string{"DocumentationTail", "recovered"}; !slices.Equal(got, want) {
		t.Fatalf("documentation-run tail definitions = %#v, want %#v", got, want)
	}
}

func TestSwiftRejectedRecoveryBlockBridgeAllocationsScaleLinearly(t *testing.T) {
	fixture := func(count int) (string, *swiftSyntaxTree, swiftLexResult) {
		var source strings.Builder
		for index := range count {
			fmt.Fprintf(
				&source,
				"import Module%d /* same-line gap */ { func hidden%d() {} }\n",
				index, index,
			)
		}
		source.WriteString("struct RecoveryBridgeTail { func recovered() {} }\n")
		text := strings.TrimSuffix(source.String(), "\n")
		lexed := lexSwift(text)
		if !lexed.concreteEligible {
			t.Fatalf("%d-pattern recovery-bridge fixture is not concrete-eligible", count)
		}
		tree, ok := parseSwiftSyntax(text, lexed)
		if !ok || !validateSwiftSyntaxTree(tree, len(text)) {
			t.Fatalf("%d-pattern recovery-bridge fixture did not parse", count)
		}
		if spans := swiftTreeRejectedRecoveryBlockSpans(text, tree, lexed); len(spans) != count {
			t.Fatalf("%d-pattern rejected spans = %d, want %d", count, len(spans), count)
		}
		definitions := swiftTreeDefinitions(text, len(swiftTestLines(text)), tree)
		symbols := swiftTestDefinitionSymbols(definitions)
		for _, required := range []string{"RecoveryBridgeTail", "recovered"} {
			if !slices.Contains(symbols, required) {
				t.Fatalf("%d-pattern recovery bridge lost %q: %#v",
					count, required, definitions)
			}
		}
		for _, symbol := range symbols {
			if strings.HasPrefix(symbol, "hidden") {
				t.Fatalf("%d-pattern recovery bridge promoted %q", count, symbol)
			}
		}
		return text, tree, lexed
	}
	measure := func(source string, tree *swiftSyntaxTree, lexed swiftLexResult) int64 {
		result := testing.Benchmark(func(b *testing.B) {
			b.Helper()
			for range b.N {
				spans := swiftTreeRejectedRecoveryBlockSpans(source, tree, lexed)
				if len(spans) == 0 {
					panic("recovery bridge unexpectedly produced no spans")
				}
			}
		})
		return result.AllocedBytesPerOp()
	}

	smallSource, smallTree, smallLexed := fixture(128)
	largeSource, largeTree, largeLexed := fixture(512)
	smallBytes := measure(smallSource, smallTree, smallLexed)
	largeBytes := measure(largeSource, largeTree, largeLexed)
	// The large source contains four times as many independent bridge matches.
	// Leave parser/runtime headroom while rejecting a per-match full-file index.
	if limit := smallBytes*6 + 1<<20; largeBytes > limit {
		t.Fatalf("recovery-bridge allocations grew superlinearly: small=%dB large=%dB (limit %dB)",
			smallBytes, largeBytes, limit)
	}
}

func TestSwiftByteOrderMarkPreservesLeadingDocumentationAttachment(t *testing.T) {
	const documentedSource = "\ufeff/// Leading documentation.\nstruct Documented {}\n"
	lines := swiftTestLines(documentedSource)
	analysis := analyzeSwiftSource(
		strings.TrimSuffix(documentedSource, "\n"), len(lines),
	)
	if analysis.tree == nil {
		t.Fatal("small BOM documentation fixture did not retain a concrete tree")
	}
	definition := swiftTestFirstDefinition(t, analysis.definitions, "Documented")
	if !definition.ownsScope || definition.line != 2 || definition.scopeStart != 1 ||
		definition.scopeEnd != 2 {
		t.Fatalf("BOM-documented declaration = %#v, want line 2 owning scope 1-2",
			definition)
	}
	swiftTestAssertDefinitionCoordinates(t, lines, analysis.definitions)

	const declarationSource = "\ufeffstruct ByteOrderMarked {}\n"
	declarationLines := swiftTestLines(declarationSource)
	declarationAnalysis := analyzeSwiftSource(
		strings.TrimSuffix(declarationSource, "\n"), len(declarationLines),
	)
	if declarationAnalysis.tree == nil {
		t.Fatal("small BOM declaration fixture did not retain a concrete tree")
	}
	declaration := swiftTestFirstDefinition(
		t, declarationAnalysis.definitions, "ByteOrderMarked",
	)
	if !declaration.ownsScope || declaration.line != 1 || declaration.scopeStart != 1 ||
		declaration.scopeEnd != 1 {
		t.Fatalf("BOM declaration = %#v, want owning scope on line 1", declaration)
	}
	swiftTestAssertDefinitionCoordinates(
		t, declarationLines, declarationAnalysis.definitions,
	)
}

func TestSwiftOpaqueSpanOverflowDisablesConcreteTreeAndKeepsTail(t *testing.T) {
	source := strings.Repeat("/**/", swiftMaximumRetainedSpans+1) +
		"\nstruct SpanTail { func recovered() {} }\n"
	lexed := lexSwift(source)
	if !lexed.spansTruncated || lexed.concreteEligible {
		t.Fatalf("opaque span overflow = (truncated %t, eligible %t), want true, false",
			lexed.spansTruncated, lexed.concreteEligible)
	}
	if len(lexed.commentSpans) != swiftMaximumRetainedSpans {
		t.Fatalf("retained comment spans = %d, want cap %d",
			len(lexed.commentSpans), swiftMaximumRetainedSpans)
	}
	lines := swiftTestLines(source)
	definitions := newSwiftLanguage().sourceDefinitions(lines)
	if got, want := swiftTestDefinitionSymbols(definitions),
		[]string{"SpanTail", "recovered"}; !slices.Equal(got, want) {
		t.Fatalf("opaque-span fallback definitions = %#v, want %#v", got, want)
	}
}

func TestSwiftTokenRetentionIsBoundedAndStreamingRecoveryKeepsMiddleAndTail(t *testing.T) {
	arguments := strings.Repeat("0,", swiftMaximumRetainedTokens/4+2048) + "0"
	source := "@A(" + arguments + `)
struct MiddleDefinition {
    func middleMember() {}
}
` + "@A(" + arguments + `)
struct TailDefinition {
    func tailMember() {}
}
`

	lexed := lexSwift(source)
	if !lexed.truncated {
		t.Fatal("fixture did not cross the Swift retained-token frontier")
	}
	if len(lexed.tokens) != swiftMaximumRetainedTokens {
		t.Fatalf("retained Swift tokens = %d, want cap %d",
			len(lexed.tokens), swiftMaximumRetainedTokens)
	}

	lines := swiftTestLines(source)
	definitions := newSwiftLanguage().sourceDefinitions(lines)
	for _, want := range []string{
		"MiddleDefinition", "middleMember", "TailDefinition", "tailMember",
	} {
		if !slices.Contains(swiftTestDefinitionSymbols(definitions), want) {
			t.Errorf("bounded streaming recovery lost %q: %#v", want, definitions)
		}
	}
	swiftTestAssertDefinitionCoordinates(t, lines, definitions)
}

func TestSwiftDiscardedTokenGapIsHardDeclarationBoundary(t *testing.T) {
	headLimit := (swiftMaximumRetainedTokens - 1) / 2
	tailLimit := swiftMaximumRetainedTokens - headLimit - 1
	source := strings.Repeat(";", headLimit-1) + "struct" +
		strings.Repeat(";", 64) + "Phantom { Target() }" +
		strings.Repeat(";", tailLimit-7)

	lexed := lexSwift(source)
	if !lexed.truncated || len(lexed.tokens) != swiftMaximumRetainedTokens {
		t.Fatalf("Swift retention = (%t, %d), want truncated cap %d",
			lexed.truncated, len(lexed.tokens), swiftMaximumRetainedTokens)
	}
	definitions := newSwiftLanguage().sourceDefinitions([]string{source})
	for _, phantom := range []string{"Phantom", "Target"} {
		if slices.Contains(swiftTestDefinitionSymbols(definitions), phantom) {
			t.Errorf("discarded middle joined unrelated tokens into %q: %#v",
				phantom, definitions)
		}
	}
}

func TestSwiftExtremeInterpolationHashAndOperatorRunsStayBounded(t *testing.T) {
	const repetitions = 16 << 10
	source := `struct Opaque {
    let text = ####"""
` + strings.Repeat(`literal \###(Fake()) \####(Real()) /* not comment */
`, repetitions) + `"""####
}
` + strings.Repeat("<=>!&|?+-*/%^~.\n", repetitions) +
		"struct ResourceTail { func recovered() {} }\n"

	lines := swiftTestLines(source)
	backend := prepareLanguageBackend(newSwiftLanguage(), lines)
	definitions := backend.sourceDefinitions(lines)
	symbols := swiftTestDefinitionSymbols(definitions)
	for _, want := range []string{"Opaque", "text", "ResourceTail", "recovered"} {
		if !slices.Contains(symbols, want) {
			t.Errorf("adversarial run lost %q: %#v", want, definitions)
		}
	}
	for _, phantom := range []string{"Fake", "Real"} {
		if slices.Contains(symbols, phantom) {
			t.Errorf("literal expression/call %q became a definition: %#v", phantom, definitions)
		}
	}
	swiftTestAssertLineWidths(t, lines, backend.searchLines(lines, true, true))
}

func TestSwiftPreparedBackendRefreshesMutatedInputAndIsConcurrent(t *testing.T) {
	first := swiftTestLines("struct First { func work() { Target() } }\n")
	second := swiftTestLines("struct Second { func other() {} }\n")
	prepared := prepareLanguageBackend(newSwiftLanguage(), first)

	if got, want := swiftTestDefinitionSymbols(prepared.sourceDefinitions(first)),
		[]string{"First", "work"}; !slices.Equal(got, want) {
		t.Fatalf("prepared first definitions = %#v, want %#v", got, want)
	}
	if got, want := swiftTestDefinitionSymbols(prepared.sourceDefinitions(second)),
		[]string{"Second", "other"}; !slices.Equal(got, want) {
		t.Fatalf("prepared stale-input definitions = %#v, want %#v", got, want)
	}

	first[0] = "struct Mutated { func changed() {} }"
	if got, want := swiftTestDefinitionSymbols(prepared.sourceDefinitions(first)),
		[]string{"Mutated", "changed"}; !slices.Equal(got, want) {
		t.Fatalf("prepared mutated-input definitions = %#v, want %#v", got, want)
	}

	stableLines := swiftTestLines(`import Foundation
struct Stable {
    func work() {
        if ready { Target() }
    }
}
`)
	stable := prepareLanguageBackend(newSwiftLanguage(), stableLines)
	want := swiftTestDefinitionSymbols(stable.sourceDefinitions(stableLines))
	const workers = 16
	var wait sync.WaitGroup
	errors := make(chan string, workers)
	for worker := range workers {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			for iteration := range 16 {
				got := swiftTestDefinitionSymbols(stable.sourceDefinitions(stableLines))
				if !reflect.DeepEqual(got, want) {
					errors <- fmt.Sprintf("worker %d iteration %d definitions %#v, want %#v",
						worker, iteration, got, want)
					return
				}
				_, _, _ = stable.importRange(stableLines)
				_ = stable.searchLines(stableLines, true, true)
				_, _ = stable.enclosingScope(stableLines, 4)
			}
		}(worker)
	}
	wait.Wait()
	close(errors)
	for failure := range errors {
		t.Error(failure)
	}
}

func FuzzSwiftBackendMaintainsCoordinateContracts(f *testing.F) {
	for _, source := range []string{
		"import Foundation\nstruct Value { let item = 1 }\n",
		"actor Worker { func run() async { Target() } }\n",
		`let text = ##"literal \#(Fake()) \##(Target())"##` + "\n",
		"let regex = #/target \\/ slash/#\nfunc tail() {}\n",
		"#if FEATURE\nstruct First {}\n#else\nstruct Second {}\n#endif\n",
		"struct Broken<T\nstruct Recovered {}\n",
		string([]byte{0xff, 0xfe, 0x00, '{', '}', '\n'}),
	} {
		f.Add(source)
	}

	f.Fuzz(func(t *testing.T, source string) {
		if len(source) > 64<<10 {
			t.Skip()
		}
		lines := swiftTestLines(source)
		backend := prepareLanguageBackend(newSwiftLanguage(), lines)
		definitions := backend.sourceDefinitions(lines)
		swiftTestAssertDefinitionCoordinates(t, lines, definitions)
		_, _, _ = backend.importRange(lines)
		for _, options := range [][2]bool{
			{false, false}, {true, false}, {false, true}, {true, true},
		} {
			searchable := backend.searchLines(lines, options[0], options[1])
			swiftTestAssertLineWidths(t, lines, searchable)
		}
		for lineNo := 1; lineNo <= len(lines); lineNo++ {
			start, end := backend.enclosingScope(lines, lineNo)
			if start < 1 || start > lineNo || end < lineNo || end > len(lines) {
				t.Fatalf("invalid scope for line %d: %d-%d of %d",
					lineNo, start, end, len(lines))
			}
		}
	})
}
