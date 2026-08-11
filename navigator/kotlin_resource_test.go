package navigator

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestKotlinOverConcreteByteTokenAndDepthCapsRetainsIndependentTail(t *testing.T) {
	tests := []struct {
		name      string
		source    string
		want      []string
		forbidden []string
	}{
		{
			name: "byte cap",
			source: "class ByteHead\n/*" +
				strings.Repeat(" class Fake { fun hidden() = Target() } ",
					kotlinMaximumConcreteParseBytes/32) +
				"*/\nclass ByteTail { fun recovered() { Target() } }\n",
			want:      []string{"ByteHead", "ByteTail", "recovered"},
			forbidden: []string{"Fake", "hidden", "Target"},
		},
		{
			name: "token cap",
			source: "class TokenHead { fun work() {\n" +
				strings.Repeat("value++;\n", kotlinMaximumConcreteTokens/3+1) +
				"} }\nclass TokenTail { fun recovered() = Unit }\n",
			want:      []string{"TokenTail", "recovered"},
			forbidden: []string{"value", "Unit"},
		},
		{
			name: "delimiter depth",
			source: "class DepthHead { fun work() {\n" +
				strings.Repeat("{\n", kotlinMaximumConcreteDelimiterDepth+1) +
				"Target()\n" +
				strings.Repeat("}\n", kotlinMaximumConcreteDelimiterDepth+1) +
				"} }\nclass DepthTail { fun recovered() = Unit }\n",
			want:      []string{"DepthTail", "recovered"},
			forbidden: []string{"Target", "Unit"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lexed := lexKotlin(test.source)
			if lexed.concreteEligible {
				t.Fatal("over-cap fixture remained concrete-eligible")
			}
			lines := kotlinTestLines(test.source)
			analysis := analyzeKotlinSource(test.source, len(lines))
			if analysis == nil {
				t.Fatal("analyzeKotlinSource returned nil")
			}
			if analysis.tree != nil {
				t.Fatal("over-cap fixture unexpectedly retained a concrete syntax tree")
			}
			symbols := kotlinTestDefinitionSymbols(analysis.definitions)
			for _, want := range test.want {
				if !slices.Contains(symbols, want) {
					t.Errorf("over-cap fallback lost %q: %#v", want, analysis.definitions)
				}
			}
			for _, forbidden := range test.forbidden {
				if slices.Contains(symbols, forbidden) {
					t.Errorf("over-cap fallback promoted %q: %#v", forbidden, analysis.definitions)
				}
			}
			kotlinTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
		})
	}
}

func TestKotlinConcreteCapBoundariesAreInclusive(t *testing.T) {
	atByteCap := strings.Repeat(" ", kotlinMaximumConcreteParseBytes)
	if lexed := lexKotlin(atByteCap); !lexed.concreteEligible {
		t.Fatal("source exactly at Kotlin byte cap was rejected")
	}
	if lexed := lexKotlin(atByteCap + " "); lexed.concreteEligible {
		t.Fatal("source over Kotlin byte cap remained concrete-eligible")
	}

	atTokenCap := strings.Repeat(";", kotlinMaximumConcreteTokens)
	if lexed := lexKotlin(atTokenCap); !lexed.concreteEligible ||
		lexed.lexicalUnits != kotlinMaximumConcreteTokens {
		t.Fatalf("source at token cap = (%t, %d), want eligible with %d units",
			lexed.concreteEligible, lexed.lexicalUnits, kotlinMaximumConcreteTokens)
	}
	if lexed := lexKotlin(atTokenCap + ";"); lexed.concreteEligible {
		t.Fatal("source over Kotlin token cap remained concrete-eligible")
	}

	atDepth := strings.Repeat("(", kotlinMaximumConcreteDelimiterDepth) + "0" +
		strings.Repeat(")", kotlinMaximumConcreteDelimiterDepth)
	if lexed := lexKotlin(atDepth); !lexed.concreteEligible ||
		lexed.maximumDelimiterDepth != kotlinMaximumConcreteDelimiterDepth {
		t.Fatalf("source at delimiter cap = (%t, %d), want eligible at %d",
			lexed.concreteEligible, lexed.maximumDelimiterDepth,
			kotlinMaximumConcreteDelimiterDepth)
	}
	if lexed := lexKotlin("(" + atDepth + ")"); lexed.concreteEligible {
		t.Fatal("source over Kotlin delimiter cap remained concrete-eligible")
	}
}

func TestKotlinLiteralAndCommentUnitsEnforceConcreteTokenCap(t *testing.T) {
	for name, unit := range map[string]string{
		"comments": "/**/ ",
		"literals": `"" `,
	} {
		t.Run(name, func(t *testing.T) {
			atCap := lexKotlin(strings.Repeat(unit, kotlinMaximumConcreteTokens))
			if !atCap.concreteEligible || atCap.lexicalUnits != kotlinMaximumConcreteTokens {
				t.Fatalf("units at cap = (%t, %d), want eligible with %d units",
					atCap.concreteEligible, atCap.lexicalUnits, kotlinMaximumConcreteTokens)
			}
			overCap := lexKotlin(strings.Repeat(unit, kotlinMaximumConcreteTokens+1))
			if overCap.concreteEligible ||
				overCap.lexicalUnits != kotlinMaximumConcreteTokens+1 {
				t.Fatalf("units over cap = (%t, %d), want ineligible with %d units",
					overCap.concreteEligible, overCap.lexicalUnits,
					kotlinMaximumConcreteTokens+1)
			}
		})
	}
}

func TestKotlinOpaqueSpanOverflowDisablesConcreteTreeAndKeepsTail(t *testing.T) {
	source := strings.Repeat("/**/", kotlinMaximumRetainedSpans+1) +
		"\nclass SpanTail { fun recovered() = Unit }\n"
	lexed := lexKotlin(source)
	if !lexed.spansTruncated || lexed.concreteEligible {
		t.Fatalf("opaque span overflow = (truncated %t, eligible %t), want true, false",
			lexed.spansTruncated, lexed.concreteEligible)
	}
	lines := kotlinTestLines(source)
	analysis := analyzeKotlinSource(source, len(lines))
	if analysis.tree != nil {
		t.Fatal("opaque span overflow unexpectedly retained a concrete syntax tree")
	}
	if got, want := kotlinTestDefinitionSymbols(analysis.definitions),
		[]string{"SpanTail", "recovered"}; !slices.Equal(got, want) {
		t.Fatalf("opaque span fallback definitions = %#v, want %#v", got, want)
	}
	kotlinTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
}

func TestKotlinTokenRetentionIsBoundedAndStreamingRecoveryKeepsMiddleAndTail(t *testing.T) {
	arguments := strings.Repeat("0,", kotlinMaximumRetainedTokens/4+2048) + "0"
	source := "@A(" + arguments + `)
class MiddleDefinition {
    fun middleMember() = Unit
}
` + "@A(" + arguments + `)
class TailDefinition {
    fun tailMember() = Unit
}
`

	lexed := lexKotlin(source)
	if !lexed.truncated {
		t.Fatal("fixture did not cross the Kotlin retained-token frontier")
	}
	if len(lexed.tokens) != kotlinMaximumRetainedTokens {
		t.Fatalf("retained Kotlin tokens = %d, want cap %d",
			len(lexed.tokens), kotlinMaximumRetainedTokens)
	}

	lines := kotlinTestLines(source)
	definitions := newKotlinLanguage().sourceDefinitions(lines)
	symbols := kotlinTestDefinitionSymbols(definitions)
	for _, want := range []string{
		"MiddleDefinition", "middleMember", "TailDefinition", "tailMember",
	} {
		if !slices.Contains(symbols, want) {
			t.Errorf("bounded streaming recovery lost %q: %#v", want, definitions)
		}
	}
	kotlinTestAssertDefinitionCoordinates(t, lines, definitions)
}

func TestKotlinDiscardedTokenGapIsHardDeclarationBoundary(t *testing.T) {
	headLimit := (kotlinMaximumRetainedTokens - 1) / 2
	tailLimit := kotlinMaximumRetainedTokens - headLimit - 1
	source := strings.Repeat(";", headLimit-1) + "class" +
		strings.Repeat(";", 64) + "Phantom { Target() }" +
		strings.Repeat(";", tailLimit-7)

	lexed := lexKotlin(source)
	if !lexed.truncated || len(lexed.tokens) != kotlinMaximumRetainedTokens {
		t.Fatalf("Kotlin retention = (%t, %d), want truncated cap %d",
			lexed.truncated, len(lexed.tokens), kotlinMaximumRetainedTokens)
	}
	definitions := newKotlinLanguage().sourceDefinitions([]string{source})
	for _, phantom := range []string{"Phantom", "Target"} {
		if slices.Contains(kotlinTestDefinitionSymbols(definitions), phantom) {
			t.Errorf("discarded middle joined unrelated tokens into %q: %#v",
				phantom, definitions)
		}
	}
}

func TestKotlinUnterminatedTripleStringAndNestedBlockCommentOwnTailToEOF(t *testing.T) {
	tests := []struct {
		name       string
		source     string
		span       func(kotlinLexResult) []cByteSpan
		want       []string
		forbidden  []string
		maskString bool
	}{
		{
			name: "triple string",
			source: `class BeforeTriple
val broken = """
class HiddenInString {
    fun phantom() = Target()
}
`,
			span:       func(result kotlinLexResult) []cByteSpan { return result.stringSpans },
			want:       []string{"BeforeTriple", "broken"},
			forbidden:  []string{"HiddenInString", "phantom", "Target"},
			maskString: true,
		},
		{
			name: "nested block comment",
			source: `class BeforeComment
/* outer
    /* nested */
    class HiddenInComment {
        fun phantom() = Target()
    }
`,
			span:      func(result kotlinLexResult) []cByteSpan { return result.commentSpans },
			want:      []string{"BeforeComment"},
			forbidden: []string{"HiddenInComment", "phantom", "Target"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lexed := lexKotlin(test.source)
			spans := test.span(lexed)
			if len(spans) == 0 || spans[len(spans)-1].end != len(test.source) {
				t.Fatalf("opaque tail spans = %#v, want final end %d", spans, len(test.source))
			}
			lines := kotlinTestLines(test.source)
			backend := prepareLanguageBackend(newKotlinLanguage(), lines)
			definitions := backend.sourceDefinitions(lines)
			symbols := kotlinTestDefinitionSymbols(definitions)
			for _, want := range test.want {
				if !slices.Contains(symbols, want) {
					t.Errorf("opaque tail lost %q: %#v", want, definitions)
				}
			}
			for _, forbidden := range test.forbidden {
				if slices.Contains(symbols, forbidden) {
					t.Errorf("opaque tail promoted %q: %#v", forbidden, definitions)
				}
			}
			masked := backend.searchLines(lines, !test.maskString, test.maskString)
			kotlinTestAssertLineWidths(t, lines, masked)
			for _, forbidden := range test.forbidden {
				if strings.Contains(strings.Join(masked, "\n"), forbidden) {
					t.Errorf("opaque tail mask retained %q: %#v", forbidden, masked)
				}
			}
			kotlinTestAssertDefinitionCoordinates(t, lines, definitions)
		})
	}
}

func TestKotlinNestedStringInterpolationPreservesSemanticExpressionMasks(t *testing.T) {
	const depth = 4
	source := `class InterpolationHost {
    val value = 1
    val text = ` + kotlinTestNestedInterpolation(depth) + `
    val excess = $$"literal $$${value}"
}
class InterpolationTail
`
	lines := kotlinTestLines(source)
	backend := prepareLanguageBackend(newKotlinLanguage(), lines)
	definitions := backend.sourceDefinitions(lines)
	symbols := kotlinTestDefinitionSymbols(definitions)
	for _, want := range []string{
		"InterpolationHost", "value", "text", "excess", "InterpolationTail",
	} {
		if !slices.Contains(symbols, want) {
			t.Errorf("nested interpolation lost definition %q: %#v", want, definitions)
		}
	}

	masked := backend.searchLines(lines, false, true)
	kotlinTestAssertLineWidths(t, lines, masked)
	textLine := kotlinTestLineContaining(t, lines, "val text")
	if count := strings.Count(masked[textLine-1], "value"); count != 1 {
		t.Fatalf("nested interpolation semantic mask retained %d value expressions, want 1: %q",
			count, masked[textLine-1])
	}
	if strings.Contains(masked[textLine-1], "${") {
		t.Fatalf("nested interpolation delimiters remained searchable: %q", masked[textLine-1])
	}
	excessLine := kotlinTestLineContaining(t, lines, "val excess")
	if count := strings.Count(masked[excessLine-1], "value"); count != 1 {
		t.Fatalf("excess-dollar mask retained %d value expressions, want 1: %q",
			count, masked[excessLine-1])
	}
	if strings.Contains(masked[excessLine-1], "$") ||
		strings.Contains(masked[excessLine-1], "literal") {
		t.Fatalf("excess-dollar literal text remained searchable: %q", masked[excessLine-1])
	}
	kotlinTestAssertDefinitionCoordinates(t, lines, definitions)
}

func TestKotlinInterpolationDepthCapBoundsLexingMaskingAndAnalysis(t *testing.T) {
	depth := kotlinMaximumInterpolationDepth + 32
	source := `class InterpolationHead {
    val value = 1
    val text = ` + kotlinTestNestedInterpolation(depth) + `
}

class InterpolationTail { fun recovered() = Unit }
`

	started := time.Now()
	opaqueCallbacks := 0
	walkKotlinLexically(source, kotlinLexicalSink{literal: func(span cByteSpan) bool {
		opaqueCallbacks++
		if span.start < 0 || span.end <= span.start || span.end > len(source) {
			t.Fatalf("invalid nested interpolation literal span: %#v", span)
		}
		return true
	}})
	if maximum := 2*kotlinMaximumInterpolationDepth + 1; opaqueCallbacks > maximum {
		t.Fatalf("nested interpolation emitted %d opaque spans, bounded maximum %d",
			opaqueCallbacks, maximum)
	}

	lines := kotlinTestLines(source)
	backend := prepareLanguageBackend(newKotlinLanguage(), lines)
	definitions := backend.sourceDefinitions(lines)
	for _, want := range []string{
		"InterpolationHead", "value", "text", "InterpolationTail", "recovered",
	} {
		if !slices.Contains(kotlinTestDefinitionSymbols(definitions), want) {
			t.Errorf("over-depth interpolation lost %q: %#v", want, definitions)
		}
	}
	for _, options := range [][2]bool{
		{false, false}, {true, false}, {false, true}, {true, true},
	} {
		masked := backend.searchLines(lines, options[0], options[1])
		kotlinTestAssertLineWidths(t, lines, masked)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("over-depth interpolation analysis took %s", elapsed)
	}
	kotlinTestAssertDefinitionCoordinates(t, lines, definitions)
}

func TestKotlinInterpolationDepthCapResyncScansLargeLineOnce(t *testing.T) {
	expression := strings.Repeat("payload", 512<<10)
	for range kotlinMaximumInterpolationDepth + 1 {
		expression = `"${` + expression + `}"`
	}
	source := "val text = " + expression + "\nclass DepthCapTail\n"

	started := time.Now()
	if withinBudget := walkKotlinLexically(source, kotlinLexicalSink{
		literal: func(cByteSpan) bool { return true },
	}); !withinBudget {
		t.Fatal("depth-cap resync unexpectedly exhausted the shared lookahead budget")
	}
	lines := kotlinTestLines(source)
	analysis := analyzeKotlinSource(source, len(lines))
	if analysis.tree != nil || analysis.lexed.concreteEligible {
		t.Fatal("over-depth interpolation unexpectedly retained a concrete tree")
	}
	for _, want := range []string{"text", "DepthCapTail"} {
		if !slices.Contains(kotlinTestDefinitionSymbols(analysis.definitions), want) {
			t.Fatalf("depth-cap resync lost %q: %#v", want, analysis.definitions)
		}
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("depth-cap resync took %s", elapsed)
	}
}

func TestKotlinNestedInterpolationLargePayloadExhaustsSharedLookaheadBudget(t *testing.T) {
	expression := strings.Repeat("payload", 96<<10)
	for range kotlinMaximumInterpolationDepth / 2 {
		expression = `"${` + expression + `}"`
	}
	source := "val text = " + expression +
		"\nval later = \"ok\" // hidden budget comment" +
		"\nval guarded = \"${\"class FakeInInterpolation\"}\"" +
		"\nval raw = \"\"\"\n${\"class HiddenInBudgetString\"}\n\"\"\"" +
		"\nclass BudgetTail\n"

	started := time.Now()
	if withinBudget := walkKotlinLexically(source, kotlinLexicalSink{
		literal: func(cByteSpan) bool { return true },
	}); withinBudget {
		t.Fatal("large nested interpolation did not reach the shared lookahead budget")
	}
	lines := kotlinTestLines(source)
	analysis := analyzeKotlinSource(source, len(lines))
	if analysis.tree != nil || analysis.lexed.concreteEligible {
		t.Fatal("lookahead-budget exhaustion retained a concrete Kotlin tree")
	}
	symbols := kotlinTestDefinitionSymbols(analysis.definitions)
	for _, want := range []string{"later", "guarded", "raw", "BudgetTail"} {
		if !slices.Contains(symbols, want) {
			t.Fatalf("lookahead-budget recovery lost %q: %#v", want, analysis.definitions)
		}
	}
	for _, forbidden := range []string{"FakeInInterpolation", "HiddenInBudgetString"} {
		if slices.Contains(symbols, forbidden) {
			t.Fatalf("lookahead-budget recovery promoted %q: %#v",
				forbidden, analysis.definitions)
		}
	}
	backend := prepareLanguageBackend(newKotlinLanguage(), lines)
	masked := backend.searchLines(lines, true, false)
	laterLine := kotlinTestLineContaining(t, lines, "val later")
	if strings.Contains(masked[laterLine-1], "hidden budget comment") {
		t.Fatalf("lookahead-budget recovery left comment searchable: %q",
			masked[laterLine-1])
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("large nested interpolation took %s", elapsed)
	}
}

func TestKotlinDegradedDollarRunMakesLinearProgress(t *testing.T) {
	source := strings.Repeat("$", 1<<20) + "\nclass DollarTail\n"
	budget := &kotlinLexicalBudget{exhausted: true}
	foundTail := false
	started := time.Now()
	kotlinWalkLexicallyWithState(source, kotlinLexicalSink{token: func(token kotlinToken) bool {
		if token.text == "DollarTail" {
			foundTail = true
		}
		return true
	}}, false, 0, budget)
	if !foundTail {
		t.Fatal("degraded dollar-run recovery lost independent tail")
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("degraded dollar-run recovery took %s", elapsed)
	}
}

func TestKotlinCompletionMarkerPreservesHeaderCap(t *testing.T) {
	const repeatedTokens = (kotlinMaximumHeaderTokens - 6) / 2
	source := "fun capped() = " + strings.Repeat("value + ", repeatedTokens) +
		"run { work() }" +
		strings.Repeat(".run { work() }", kotlinMaximumHeaderTokens/4) +
		"\nclass HeaderCapTail\n"
	lineCount := len(kotlinTestLines(source))
	parser := kotlinRecoveryParser{
		source: source, lineStarts: kotlinLineStarts(source), lineCount: lineCount,
		definitionSeen: make(map[kotlinDefinitionKey]int),
		frames:         []kotlinRecoveryFrame{{kind: kotlinRecoverySource, start: 1}},
	}
	maximumHeader := 0
	started := time.Now()
	walkKotlinLexically(source, kotlinLexicalSink{token: func(token kotlinToken) bool {
		if !parser.accept(token) {
			return false
		}
		maximumHeader = max(maximumHeader, len(parser.header))
		return true
	}})
	parser.flushHeader()
	for len(parser.frames) > 1 {
		parser.closeFrame(lineCount, 0)
	}
	if maximumHeader > kotlinMaximumHeaderTokens {
		t.Fatalf("restored Kotlin header grew to %d tokens, cap %d",
			maximumHeader, kotlinMaximumHeaderTokens)
	}
	if got, want := kotlinTestDefinitionSymbols(parser.definitions),
		[]string{"capped", "HeaderCapTail"}; !slices.Equal(got, want) {
		t.Fatalf("header-cap recovery definitions = %#v, want %#v", got, want)
	}
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("header-cap chained recovery took %s", elapsed)
	}
}

func TestKotlinFallbackFindsNamedDeclarationsInsideTemplateExpressions(t *testing.T) {
	const prefix = `val text = "${run {
    class Embedded
    fun inner() = Unit
    Embedded()
}}"
class TemplateTail
`
	source := prefix + strings.Repeat(" ", kotlinMaximumConcreteParseBytes+1)
	lines := kotlinTestLines(source)
	analysis := analyzeKotlinSource(source, len(lines))
	if analysis.tree != nil {
		t.Fatal("over-byte template fixture unexpectedly retained a concrete tree")
	}
	if got, want := kotlinTestDefinitionSymbols(analysis.definitions),
		[]string{"text", "Embedded", "inner", "TemplateTail"}; !slices.Equal(got, want) {
		t.Fatalf("template-expression fallback definitions = %#v, want %#v", got, want)
	}
	for _, definition := range analysis.definitions {
		if definition.symbol == "text" && (definition.ownsScope ||
			definition.scopeStart != definition.line || definition.scopeEnd != definition.line) {
			t.Fatalf("template initializer property owns interpolation scope: %#v", definition)
		}
	}
}

func TestKotlinDeepNestingInvalidUTF8AndMismatchedDelimitersMakeProgress(t *testing.T) {
	tests := []struct {
		name   string
		source string
		tail   string
	}{
		{
			name: "balanced deep braces",
			source: strings.Repeat("{\n", kotlinMaximumStructuralDepth+1024) +
				strings.Repeat("}\n", kotlinMaximumStructuralDepth+1024) +
				"class DeepTail { fun recovered() = Unit }\n",
			tail: "DeepTail",
		},
		{
			name: "deep nested comments",
			source: strings.Repeat("/*", kotlinMaximumStructuralDepth+1024) +
				" class Hidden { fun phantom() = Target() } " +
				strings.Repeat("*/", kotlinMaximumStructuralDepth+1024) +
				"\nclass CommentTail\n",
			tail: "CommentTail",
		},
		{
			name: "mismatched delimiters",
			source: strings.Repeat("([", kotlinMaximumConcreteDelimiterDepth+1024) +
				strings.Repeat(")]", kotlinMaximumConcreteDelimiterDepth+1024) +
				"\nclass MismatchTail\n",
			tail: "MismatchTail",
		},
		{
			name: "invalid UTF-8",
			source: strings.Repeat(string([]byte{0xff, '(', 0xfe, ')', '\n'}), 4096) +
				"class UTF8Tail { fun recovered() = Unit }\n",
			tail: "UTF8Tail",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			started := time.Now()
			lexed := lexKotlin(test.source)
			lines := kotlinTestLines(test.source)
			backend := prepareLanguageBackend(newKotlinLanguage(), lines)
			definitions := backend.sourceDefinitions(lines)
			if elapsed := time.Since(started); elapsed > 5*time.Second {
				t.Fatalf("bounded adversarial fixture took %s", elapsed)
			}
			if !slices.Contains(kotlinTestDefinitionSymbols(definitions), test.tail) {
				t.Errorf("adversarial recovery lost %q: %#v", test.tail, definitions)
			}
			previousEnd := 0
			for _, token := range lexed.tokens {
				if token.gap {
					continue
				}
				if token.start < previousEnd || token.end <= token.start ||
					token.end > len(test.source) {
					t.Fatalf("non-progressing or invalid token after byte %d: %#v",
						previousEnd, token)
				}
				previousEnd = token.end
			}
			for _, options := range [][2]bool{
				{false, false}, {true, false}, {false, true}, {true, true},
			} {
				masked := backend.searchLines(lines, options[0], options[1])
				kotlinTestAssertLineWidths(t, lines, masked)
			}
			kotlinTestAssertDefinitionCoordinates(t, lines, definitions)
		})
	}
}

func TestKotlinStructuralFrameOverflowKeepsNamedOuterScope(t *testing.T) {
	depth := kotlinMaximumStructuralDepth + 17
	source := "fun overflowOwner() {\n" +
		strings.Repeat("{\n", depth) +
		"Target()\n" +
		strings.Repeat("}\n", depth) +
		"}\nclass OverflowTail\n"
	lines := kotlinTestLines(source)
	outerCloseLine := kotlinTestLineContaining(t, lines, "class OverflowTail") - 1

	analysis := analyzeKotlinSource(source, len(lines))
	if analysis.tree != nil {
		t.Fatal("structural-overflow fixture unexpectedly retained a concrete syntax tree")
	}
	owner := kotlinTestFirstDefinition(t, analysis.definitions, "overflowOwner")
	if !owner.ownsScope || owner.scopeStart != 1 || owner.scopeEnd != outerCloseLine {
		t.Fatalf("overflowed named scope = %#v, want owning scope 1-%d",
			owner, outerCloseLine)
	}
	if !slices.Contains(kotlinTestDefinitionSymbols(analysis.definitions), "OverflowTail") {
		t.Fatalf("structural overflow lost independent tail: %#v", analysis.definitions)
	}
	if slices.Contains(kotlinTestDefinitionSymbols(analysis.definitions), "Target") {
		t.Fatalf("structural overflow promoted call: %#v", analysis.definitions)
	}
	kotlinTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
}

func TestKotlinByteCapFallbackSeparatesPropertyBodiesAndAnonymousObjectMembers(t *testing.T) {
	source := strings.Repeat(" ", kotlinMaximumConcreteParseBytes+1) + `
class FallbackHost {
    val callback = fun(value: Int) {
        val callbackLocal = value
        return value
    }

    val lambda = {
        val lambdaLocal = 1
        lambdaLocal
    }

    val singleton = object {
        val member = 1
    }
}
`
	lexed := lexKotlin(source)
	if lexed.concreteEligible {
		t.Fatal("property-body fixture remained concrete-eligible")
	}
	lines := kotlinTestLines(source)
	analysis := analyzeKotlinSource(source, len(lines))
	if analysis.tree != nil {
		t.Fatal("property-body fallback unexpectedly retained a concrete syntax tree")
	}
	symbols := kotlinTestDefinitionSymbols(analysis.definitions)
	for _, want := range []string{"FallbackHost", "callback", "lambda", "singleton", "member"} {
		if !slices.Contains(symbols, want) {
			t.Errorf("property-body fallback lost %q: %#v", want, analysis.definitions)
		}
	}
	for _, forbidden := range []string{"value", "callbackLocal", "lambdaLocal"} {
		if slices.Contains(symbols, forbidden) {
			t.Errorf("property-body fallback promoted local %q: %#v",
				forbidden, analysis.definitions)
		}
	}
	kotlinTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
}

func TestKotlinTinyLexDoesNotEagerlyAllocateRetentionTail(t *testing.T) {
	result := testing.Benchmark(func(b *testing.B) {
		b.Helper()
		for range b.N {
			_ = lexKotlin("class Tiny { val value = 1 }")
		}
	})
	if bytes := result.AllocedBytesPerOp(); bytes > 1<<20 {
		t.Fatalf("tiny Kotlin lexical analysis allocated %d bytes/op, want <= 1MiB", bytes)
	}
}

func TestKotlinPreparedBackendRefreshesMutatedInputAndIsConcurrent(t *testing.T) {
	first := kotlinTestLines("class First { fun work() { Target() } }")
	second := kotlinTestLines("class Second { fun other() = Unit }")
	prepared := prepareLanguageBackend(newKotlinLanguage(), first)

	if got, want := kotlinTestDefinitionSymbols(prepared.sourceDefinitions(first)),
		[]string{"First", "work"}; !slices.Equal(got, want) {
		t.Fatalf("prepared first definitions = %#v, want %#v", got, want)
	}
	if got, want := kotlinTestDefinitionSymbols(prepared.sourceDefinitions(second)),
		[]string{"Second", "other"}; !slices.Equal(got, want) {
		t.Fatalf("prepared stale-input definitions = %#v, want %#v", got, want)
	}

	first[0] = "class Mutated { fun changed() = Unit }"
	if got, want := kotlinTestDefinitionSymbols(prepared.sourceDefinitions(first)),
		[]string{"Mutated", "changed"}; !slices.Equal(got, want) {
		t.Fatalf("prepared mutated-input definitions = %#v, want %#v", got, want)
	}

	stableLines := kotlinTestLines(`package concurrent
import kotlin.collections.List

class Stable {
    fun work() {
        if (ready()) Target()
    }
}`)
	stable := prepareLanguageBackend(newKotlinLanguage(), stableLines)
	navigation := stable.(navigationScopeResolver)
	want := kotlinTestDefinitionSymbols(stable.sourceDefinitions(stableLines))
	defensive := stable.sourceDefinitions(stableLines)
	defensive[0].symbol = "corrupted"
	if got := kotlinTestDefinitionSymbols(stable.sourceDefinitions(stableLines)); !slices.Equal(got, want) {
		t.Fatalf("sourceDefinitions exposed prepared storage: got %#v, want %#v", got, want)
	}

	const workers = 32
	var wait sync.WaitGroup
	errors := make(chan string, workers)
	for worker := range workers {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			for iteration := range 32 {
				got := kotlinTestDefinitionSymbols(stable.sourceDefinitions(stableLines))
				if !slices.Equal(got, want) {
					errors <- fmt.Sprintf(
						"worker %d iteration %d definitions %#v, want %#v",
						worker, iteration, got, want,
					)
					return
				}
				_, _, _ = stable.importRange(stableLines)
				_ = stable.searchLines(stableLines, true, true)
				_, _ = stable.enclosingScope(stableLines, 6)
				_, _ = navigation.navigationScope(stableLines, 6)
			}
		}(worker)
	}
	wait.Wait()
	close(errors)
	for failure := range errors {
		t.Error(failure)
	}
}

func kotlinTestAssertLineWidths(t *testing.T, original, masked []string) {
	t.Helper()
	if len(masked) != len(original) {
		t.Fatalf("masked line count = %d, want %d", len(masked), len(original))
	}
	for index := range original {
		if len(masked[index]) != len(original[index]) {
			t.Fatalf("mask changed line %d byte width from %d to %d",
				index+1, len(original[index]), len(masked[index]))
		}
	}
}

func kotlinTestNestedInterpolation(depth int) string {
	expression := "value"
	for range max(0, depth) {
		expression = `"${` + expression + `}"`
	}
	return expression
}
