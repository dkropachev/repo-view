package navigator

import (
	"fmt"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestModulaConcreteByteTokenAndCommentDepthCapsAreInclusive(t *testing.T) {
	t.Parallel()

	bytePrefix := "MODULE Bytes;\n(*"
	byteSuffix := "*)\nEND Bytes."
	padding := modulaMaximumConcreteParseBytes - len(bytePrefix) - len(byteSuffix)
	if padding <= 0 {
		t.Fatal("Modula-2 concrete byte cap is smaller than the test frame")
	}
	atByteCap := bytePrefix + strings.Repeat(" ", padding) + byteSuffix
	if len(atByteCap) != modulaMaximumConcreteParseBytes {
		t.Fatalf("byte-cap fixture = %d bytes, want %d",
			len(atByteCap), modulaMaximumConcreteParseBytes)
	}
	if lexed := lexModula(atByteCap); !lexed.concreteEligible {
		t.Fatal("source exactly at Modula-2 byte cap was rejected")
	}
	if lexed := lexModula(atByteCap + " "); lexed.concreteEligible {
		t.Fatal("source over Modula-2 byte cap remained concrete-eligible")
	}

	tokenPrefix := "MODULE Tokens; BEGIN "
	tokenSuffix := " END Tokens."
	baseUnits := lexModula(tokenPrefix + tokenSuffix).lexicalUnits
	semicolonCount := modulaMaximumConcreteTokens - baseUnits
	if semicolonCount <= 0 {
		t.Fatal("Modula-2 concrete token cap is smaller than the test frame")
	}
	atTokenCap := tokenPrefix + strings.Repeat(";", semicolonCount) + tokenSuffix
	if lexed := lexModula(atTokenCap); !lexed.concreteEligible ||
		lexed.lexicalUnits != modulaMaximumConcreteTokens {
		t.Fatalf("source at token cap = (%t, %d), want eligible with %d units",
			lexed.concreteEligible, lexed.lexicalUnits, modulaMaximumConcreteTokens)
	}
	if lexed := lexModula(
		tokenPrefix + strings.Repeat(";", semicolonCount+1) + tokenSuffix,
	); lexed.concreteEligible || lexed.lexicalUnits != modulaMaximumConcreteTokens+1 {
		t.Fatalf("source over token cap = (%t, %d), want ineligible with %d units",
			lexed.concreteEligible, lexed.lexicalUnits, modulaMaximumConcreteTokens+1)
	}

	commentFixture := func(depth int) string {
		return "MODULE Comments;\n" + strings.Repeat("(*", depth) +
			"opaque" + strings.Repeat("*)", depth) + "\nEND Comments."
	}
	if lexed := lexModula(commentFixture(modulaMaximumCommentDepth)); !lexed.concreteEligible || lexed.maximumCommentDepth != modulaMaximumCommentDepth {
		t.Fatalf("source at comment-depth cap = (%t, %d), want eligible at %d",
			lexed.concreteEligible, lexed.maximumCommentDepth, modulaMaximumCommentDepth)
	}
	if lexed := lexModula(commentFixture(modulaMaximumCommentDepth + 1)); lexed.concreteEligible || lexed.maximumCommentDepth != modulaMaximumCommentDepth+1 {
		t.Fatalf("source over comment-depth cap = (%t, %d), want ineligible at %d",
			lexed.concreteEligible, lexed.maximumCommentDepth, modulaMaximumCommentDepth+1)
	}
}

func TestModulaOverConcreteFrontiersRetainIndependentTail(t *testing.T) {
	tests := []struct {
		name      string
		source    string
		forbidden []string
	}{
		{
			name: "byte cap",
			source: "MODULE ByteHead;\n(*" +
				strings.Repeat(
					" PROCEDURE Hidden; ", modulaMaximumConcreteParseBytes/len(" PROCEDURE Hidden; ")+1,
				) +
				"*)\nPROCEDURE ByteTail;\nBEGIN\nEND ByteTail;\nBEGIN\nEND ByteHead.\n",
			forbidden: []string{"Hidden"},
		},
		{
			name: "token cap",
			source: "MODULE TokenHead;\n" +
				strings.Repeat(";", modulaMaximumConcreteTokens+64) +
				"\nPROCEDURE TokenTail;\nBEGIN\nEND TokenTail;\nBEGIN\nEND TokenHead.\n",
		},
		{
			name: "comment depth",
			source: "MODULE CommentHead;\n" +
				strings.Repeat("(*", modulaMaximumCommentDepth+1) +
				" PROCEDURE Hidden; " +
				strings.Repeat("*)", modulaMaximumCommentDepth+1) +
				"\nPROCEDURE CommentTail;\nBEGIN\nEND CommentTail;\nBEGIN\nEND CommentHead.\n",
			forbidden: []string{"Hidden"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lexed := lexModula(test.source)
			if lexed.concreteEligible {
				t.Fatal("over-frontier fixture remained concrete-eligible")
			}
			lines := modulaTestLines(test.source)
			analysis := analyzeModulaSource(test.source, len(lines))
			if analysis == nil {
				t.Fatal("analyzeModulaSource returned nil")
			}
			if analysis.tree != nil {
				t.Fatal("over-frontier fixture unexpectedly retained a concrete tree")
			}
			var tail string
			switch test.name {
			case "byte cap":
				tail = "ByteTail"
			case "token cap":
				tail = "TokenTail"
			case "comment depth":
				tail = "CommentTail"
			}
			symbols := modulaTestDefinitionSymbols(analysis.definitions)
			if !slices.Contains(symbols, tail) {
				t.Errorf("over-frontier recovery lost %q: %#v", tail, analysis.definitions)
			}
			for _, forbidden := range test.forbidden {
				if slices.Contains(symbols, forbidden) {
					t.Errorf("opaque over-frontier text promoted %q: %#v",
						forbidden, analysis.definitions)
				}
			}
			modulaTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
		})
	}
}

func TestModulaTokenRetentionIsBoundedAndStreamingRecoveryKeepsTail(t *testing.T) {
	source := "MODULE Retention;\n" +
		strings.Repeat(";", modulaMaximumRetainedTokens/2+4096) +
		"\nPROCEDURE Middle;\nBEGIN\nEND Middle;\n" +
		strings.Repeat(";", modulaMaximumRetainedTokens/2+4096) +
		"\nPROCEDURE Tail;\nBEGIN\nEND Tail;\nBEGIN\nEND Retention.\n"
	lexed := lexModula(source)
	if !lexed.truncated {
		t.Fatal("fixture did not cross the Modula-2 retained-token frontier")
	}
	if len(lexed.tokens) != modulaMaximumRetainedTokens {
		t.Fatalf("retained tokens = %d, want cap %d",
			len(lexed.tokens), modulaMaximumRetainedTokens)
	}
	gapCount := 0
	for _, token := range lexed.tokens {
		if token.gap {
			gapCount++
		}
	}
	if gapCount != 1 {
		t.Fatalf("retained token gaps = %d, want exactly one", gapCount)
	}

	lines := modulaTestLines(source)
	definitions := newModulaLanguage().sourceDefinitions(lines)
	symbols := modulaTestDefinitionSymbols(definitions)
	for _, want := range []string{"Retention", "Middle", "Tail"} {
		if !slices.Contains(symbols, want) {
			t.Errorf("streaming recovery lost %q: %#v", want, definitions)
		}
	}
	modulaTestAssertDefinitionCoordinates(t, lines, definitions)
}

func TestModulaContentGateStreamsPriorityPastRetainedTokenHead(t *testing.T) {
	repetitions := modulaMaximumRetainedTokens/2 + 64
	source := "MODULE RetainedPriority[" + strings.Repeat("1 + ", repetitions) +
		"1];\nPROCEDURE Tail;\nBEGIN\nEND Tail;\nBEGIN\nEND RetainedPriority.\n"
	lexed := lexModula(source)
	if !lexed.truncated {
		t.Fatal("priority fixture did not cross the retained-token frontier")
	}
	if !modulaContentGate(lexed) {
		t.Fatal("streamed compilation-unit heading was rejected by the content gate")
	}
	started := time.Now()
	lines := modulaTestLines(source)
	analysis := analyzeModulaSource(source, len(lines))
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("streamed priority analysis took %s", elapsed)
	}
	if analysis == nil || analysis.tree != nil || !analysis.gated {
		t.Fatalf("streamed priority analysis = %#v, want gated fallback", analysis)
	}
	for _, symbol := range []string{"RetainedPriority", "Tail"} {
		if !modulaTestHasOwningDefinition(analysis.definitions, symbol) {
			t.Errorf("streamed priority lost owning %s: %#v", symbol, analysis.definitions)
		}
	}
}

func TestModulaOpaqueSpanRetentionIsBoundedAndStreamingRecoveryKeepsTail(t *testing.T) {
	source := "MODULE Spans;\n" + strings.Repeat("(**) ", modulaMaximumRetainedSpans+1) +
		"\nPROCEDURE Tail;\nBEGIN\nEND Tail;\nBEGIN\nEND Spans.\n"
	lexed := lexModula(source)
	if !lexed.spansTruncated || lexed.concreteEligible {
		t.Fatalf("opaque span overflow = (truncated %t, eligible %t), want true, false",
			lexed.spansTruncated, lexed.concreteEligible)
	}
	if len(lexed.commentSpans) != modulaMaximumRetainedSpans {
		t.Fatalf("retained comment spans = %d, want cap %d",
			len(lexed.commentSpans), modulaMaximumRetainedSpans)
	}
	lines := modulaTestLines(source)
	definitions := newModulaLanguage().sourceDefinitions(lines)
	if !slices.Contains(modulaTestDefinitionSymbols(definitions), "Tail") {
		t.Fatalf("opaque-span streaming recovery lost Tail: %#v", definitions)
	}
	modulaTestAssertDefinitionCoordinates(t, lines, definitions)
}

func TestModulaDiscardedTokenGapCannotJoinDeclarationHead(t *testing.T) {
	headLimit := (modulaMaximumRetainedTokens - 1) / 2
	tailLimit := modulaMaximumRetainedTokens - headLimit - 1
	source := "MODULE Gap;" + strings.Repeat(";", headLimit-4) + "PROCEDURE" +
		strings.Repeat(";", 64) + "Phantom;" +
		strings.Repeat(";", tailLimit+64) + "END Gap."
	lexed := lexModula(source)
	if !lexed.truncated || len(lexed.tokens) != modulaMaximumRetainedTokens {
		t.Fatalf("retention = (%t, %d), want truncated cap %d",
			lexed.truncated, len(lexed.tokens), modulaMaximumRetainedTokens)
	}
	definitions := analyzeModulaLexically(source, 1).definitions
	if slices.Contains(modulaTestDefinitionSymbols(definitions), "Phantom") {
		t.Fatalf("discarded token gap joined PROCEDURE to Phantom: %#v", definitions)
	}
}

func TestModulaDeclarationTokenBudgetAndStructuralDepthKeepTail(t *testing.T) {
	largeDeclaration := "MODULE HeaderBudget;\nCONST Huge = " +
		strings.Repeat("1 + ", modulaMaximumDeclarationTokens+64) +
		"1;\nPROCEDURE HeaderTail;\nBEGIN\nEND HeaderTail;\nBEGIN\nEND HeaderBudget.\n"

	depth := modulaMaximumStructuralDepth + 64
	deepStructure := "MODULE Deep;\nPROCEDURE Owner;\nBEGIN\n" +
		strings.Repeat("IF TRUE THEN\n", depth) +
		strings.Repeat("END;\n", depth) +
		"END Owner;\nPROCEDURE DepthTail;\nBEGIN\nEND DepthTail;\nBEGIN\nEND Deep.\n"

	for _, test := range []struct {
		name   string
		source string
		tail   string
	}{
		{name: "declaration token budget", source: largeDeclaration, tail: "HeaderTail"},
		{name: "structural depth", source: deepStructure, tail: "DepthTail"},
	} {
		t.Run(test.name, func(t *testing.T) {
			started := time.Now()
			lines := modulaTestLines(test.source)
			definitions := analyzeModulaLexically(test.source, len(lines)).definitions
			if elapsed := time.Since(started); elapsed > 5*time.Second {
				t.Fatalf("bounded recovery took %s", elapsed)
			}
			if !slices.Contains(modulaTestDefinitionSymbols(definitions), test.tail) {
				t.Fatalf("bounded recovery lost %q: %#v", test.tail, definitions)
			}
			modulaTestAssertDefinitionCoordinates(t, lines, definitions)
		})
	}
}

func TestModulaMalformedModuleSuffixIsRejectedAcrossHeaderCap(t *testing.T) {
	for _, targetTokens := range []int{
		modulaMaximumDeclarationTokens - 1,
		modulaMaximumDeclarationTokens,
		modulaMaximumDeclarationTokens + 1,
	} {
		t.Run(fmt.Sprintf("tokens_%d", targetTokens), func(t *testing.T) {
			header := "MODULE Fake" +
				strings.Repeat(" garbage", targetTokens-2)
			if got := len(lexModula(header).tokens); got != targetTokens {
				t.Fatalf("header tokens = %d, want %d", got, targetTokens)
			}
			source := "MODULE Outer;\n" + header + ";\n" +
				"PROCEDURE Hidden;\nBEGIN\nEND Hidden;\nEND Fake;\n" +
				"PROCEDURE Tail;\nBEGIN\nEND Tail;\nBEGIN\nEND Outer.\n"
			lines := modulaTestLines(source)
			definitions := analyzeModulaLexically(source, len(lines)).definitions
			symbols := modulaTestDefinitionSymbols(definitions)
			if slices.Contains(symbols, "Fake") {
				t.Fatalf("malformed %d-token module suffix promoted Fake: %#v",
					targetTokens, definitions)
			}
			if !modulaTestHasOwningDefinition(definitions, "Tail") {
				t.Fatalf("malformed %d-token module suffix lost Tail: %#v",
					targetTokens, definitions)
			}
		})
	}
}

func TestModulaDenseMalformedStatementsStayBounded(t *testing.T) {
	statementCount := modulaMaximumConcreteTokens / 4
	source := "MODULE Dense;\nBEGIN\n" + strings.Repeat("42;\n", statementCount) +
		"END Dense.\n"
	lexed := lexModula(source)
	if !lexed.concreteEligible {
		t.Fatalf("dense malformed fixture has %d lexical units, want concrete eligibility",
			lexed.lexicalUnits)
	}
	started := time.Now()
	tree, ok := parseModulaSyntax(source, lexed)
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("dense malformed statement parse took %s", elapsed)
	}
	if !ok || tree == nil {
		t.Fatal("dense malformed statement fixture produced no concrete tree")
	}
	if spans := modulaSyntaxErrorSpans(tree, len(source)); len(spans) != statementCount {
		t.Fatalf("dense malformed statement spans = %d, want %d",
			len(spans), statementCount)
	}
}

func TestModulaNearTokenCapPointerTypeIsBoundedAndKeepsTail(t *testing.T) {
	pointerDepth := modulaMaximumConcreteTokens/2 - 128
	source := "MODULE PointerDepth;\nTYPE Broken = " +
		strings.Repeat("POINTER TO ", pointerDepth) +
		"INTEGER;\nPROCEDURE Tail;\nBEGIN\nEND Tail;\nBEGIN\nEND PointerDepth.\n"
	lexed := lexModula(source)
	if !lexed.concreteEligible {
		t.Fatalf("near-token-cap pointer fixture is not concrete eligible: %d tokens",
			lexed.lexicalUnits)
	}
	if lexed.lexicalUnits < modulaMaximumConcreteTokens-512 {
		t.Fatalf("pointer fixture has only %d tokens, want near cap %d",
			lexed.lexicalUnits, modulaMaximumConcreteTokens)
	}

	started := time.Now()
	lines := modulaTestLines(source)
	analysis := analyzeModulaSource(source, len(lines))
	if elapsed := time.Since(started); elapsed > 5*time.Second {
		t.Fatalf("near-token-cap pointer analysis took %s", elapsed)
	}
	if analysis == nil {
		t.Fatal("analyzeModulaSource returned nil")
	}
	symbols := modulaTestDefinitionSymbols(analysis.definitions)
	if slices.Contains(symbols, "Broken") {
		t.Fatalf("over-depth pointer type was promoted: %#v", analysis.definitions)
	}
	if !slices.Contains(symbols, "Tail") {
		t.Fatalf("over-depth pointer type lost independent Tail: %#v", analysis.definitions)
	}
	modulaTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
}

func TestModulaDeepProcedureAndModuleOwnersStayBoundedAndKeepTail(t *testing.T) {
	ownerFixture := func(kind string, depth int) string {
		var source strings.Builder
		source.WriteString("MODULE OwnerDepth;\n")
		for index := range depth {
			fmt.Fprintf(&source, "%s Owner%d;\n", kind, index)
		}
		source.WriteString("BEGIN\n")
		for index := depth - 1; index >= 0; index-- {
			fmt.Fprintf(&source, "END Owner%d;\n", index)
			if kind == "MODULE" && index > 0 {
				source.WriteString("BEGIN\n")
			}
		}
		source.WriteString("PROCEDURE Tail;\nBEGIN\nEND Tail;\nBEGIN\nEND OwnerDepth.\n")
		return source.String()
	}

	for _, test := range []struct {
		name   string
		kind   string
		depth  int
		atEdge bool
	}{
		{name: "procedures at cap", kind: "PROCEDURE", depth: modulaMaximumStructuralDepth, atEdge: true},
		{name: "procedures over cap", kind: "PROCEDURE", depth: modulaMaximumStructuralDepth + 64},
		{name: "modules at cap", kind: "MODULE", depth: modulaMaximumStructuralDepth, atEdge: true},
		{name: "modules over cap", kind: "MODULE", depth: modulaMaximumStructuralDepth + 64},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := ownerFixture(test.kind, test.depth)
			started := time.Now()
			lines := modulaTestLines(source)
			analysis := analyzeModulaSource(source, len(lines))
			if elapsed := time.Since(started); elapsed > 5*time.Second {
				t.Fatalf("%s owner analysis took %s", test.name, elapsed)
			}
			if analysis == nil {
				t.Fatal("analyzeModulaSource returned nil")
			}
			if !slices.Contains(modulaTestDefinitionSymbols(analysis.definitions), "Tail") {
				t.Fatalf("%s lost independent Tail: %#v", test.name, analysis.definitions)
			}
			if test.atEdge && (analysis.tree == nil || len(analysis.recoverySpans) != 0) {
				t.Errorf("at-cap %s did not retain a recovery-free concrete tree: tree=%t spans=%#v",
					test.kind, analysis.tree != nil, analysis.recoverySpans)
			}
			if !test.atEdge && len(analysis.recoverySpans) == 0 && analysis.tree != nil {
				t.Errorf("over-depth %s retained a recovery-free concrete tree", test.kind)
			}
			modulaTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
		})
	}
}

func TestModulaDeepCommentsInvalidUTF8AndUnterminatedStringsMakeProgress(t *testing.T) {
	tests := []struct {
		name      string
		source    string
		tail      string
		forbidden []string
	}{
		{
			name: "deep comments",
			source: "MODULE Comments;\n" +
				strings.Repeat("(*", modulaMaximumCommentDepth+1024) +
				" PROCEDURE Hidden; " +
				strings.Repeat("*)", modulaMaximumCommentDepth+1024) +
				"\nPROCEDURE CommentTail;\nBEGIN\nEND CommentTail;\nBEGIN\nEND Comments.\n",
			tail:      "CommentTail",
			forbidden: []string{"Hidden"},
		},
		{
			name: "invalid UTF-8",
			source: "MODULE UTF8;\n(*" +
				strings.Repeat(string([]byte{0xff, 0xfe}), 4096) +
				"*)\nPROCEDURE UTF8Tail;\nBEGIN\nEND UTF8Tail;\nBEGIN\nEND UTF8.\n",
			tail: "UTF8Tail",
		},
		{
			name: "unterminated line string",
			source: "MODULE Strings;\nCONST Broken = \"unterminated\n" +
				"PROCEDURE StringTail;\nBEGIN\nEND StringTail;\nBEGIN\nEND Strings.\n",
			tail: "StringTail",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			started := time.Now()
			lexed := lexModula(test.source)
			lines := modulaTestLines(test.source)
			definitions := newModulaLanguage().sourceDefinitions(lines)
			if elapsed := time.Since(started); elapsed > 5*time.Second {
				t.Fatalf("adversarial recovery took %s", elapsed)
			}
			if !slices.Contains(modulaTestDefinitionSymbols(definitions), test.tail) {
				t.Errorf("adversarial recovery lost %q: %#v", test.tail, definitions)
			}
			for _, forbidden := range test.forbidden {
				if slices.Contains(modulaTestDefinitionSymbols(definitions), forbidden) {
					t.Errorf("opaque adversarial text promoted %q: %#v",
						forbidden, definitions)
				}
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
			modulaTestAssertDefinitionCoordinates(t, lines, definitions)
		})
	}
}

func TestModulaNestedCommentScanScalesLinearlyAndKeepsTail(t *testing.T) {
	fixture := func(depth int) string {
		return "MODULE Scale;\n" + strings.Repeat("(*", depth) + " opaque " +
			strings.Repeat("*)", depth) +
			"\nPROCEDURE Tail;\nBEGIN\nEND Tail;\nBEGIN\nEND Scale.\n"
	}
	measure := func(depth int) time.Duration {
		started := time.Now()
		walkModulaLexically(fixture(depth), modulaLexicalSink{})
		return time.Since(started)
	}
	minimum := func(depth, attempts int) time.Duration {
		best := time.Duration(1<<63 - 1)
		for range attempts {
			best = min(best, measure(depth))
		}
		return best
	}

	small := minimum(4<<10, 3)
	large := minimum(16<<10, 2)
	if limit := small*10 + 20*time.Millisecond; large > limit {
		t.Fatalf("nested-comment scan scaled superlinearly: 4K=%s, 16K=%s (limit %s)",
			small, large, limit)
	}
	source := fixture(16 << 10)
	definitions := analyzeModulaLexically(source, len(modulaTestLines(source))).definitions
	if !slices.Contains(modulaTestDefinitionSymbols(definitions), "Tail") {
		t.Fatalf("linear nested-comment recovery lost Tail: %#v", definitions)
	}
}

func TestModulaFallbackAllocationsScaleLinearly(t *testing.T) {
	fixture := func(count int) string {
		var source strings.Builder
		source.WriteString("MODULE Allocations;\nCONST\n")
		for index := range count {
			fmt.Fprintf(&source, "  Value%d = %d;\n", index, index)
		}
		source.WriteString("PROCEDURE Tail;\nBEGIN\nEND Tail;\nBEGIN\nEND Allocations.\n")
		return source.String()
	}
	measure := func(source string) int64 {
		lineCount := len(modulaTestLines(source))
		result := testing.Benchmark(func(b *testing.B) {
			b.Helper()
			for range b.N {
				analysis := analyzeModulaLexically(source, lineCount)
				if len(analysis.definitions) == 0 {
					panic("fallback allocation fixture lost definitions")
				}
			}
		})
		return result.AllocedBytesPerOp()
	}

	small := measure(fixture(256))
	large := measure(fixture(1024))
	if limit := small*7 + 1<<20; large > limit {
		t.Fatalf("fallback allocations grew superlinearly: small=%dB large=%dB (limit %dB)",
			small, large, limit)
	}
}

func TestModulaTinyLexDoesNotEagerlyAllocateRetentionTail(t *testing.T) {
	result := testing.Benchmark(func(b *testing.B) {
		b.Helper()
		for range b.N {
			_ = lexModula("MODULE Tiny; CONST Value = 1; END Tiny.")
		}
	})
	if bytes := result.AllocedBytesPerOp(); bytes > 1<<20 {
		t.Fatalf("tiny Modula-2 lexical analysis allocated %d bytes/op, want <= 1MiB", bytes)
	}
}

func TestModulaPreparedBackendRefreshesMutatedInputAndIsConcurrent(t *testing.T) {
	first := modulaTestLines(`MODULE First;
PROCEDURE Work;
BEGIN
END Work;
BEGIN
END First.
`)
	second := modulaTestLines(`MODULE Second;
PROCEDURE Other;
BEGIN
END Other;
BEGIN
END Second.
`)
	prepared := prepareLanguageBackend(newModulaLanguage(), first)
	if got, want := modulaTestDefinitionSymbols(prepared.sourceDefinitions(first)),
		[]string{"First", "Work"}; !slices.Equal(got, want) {
		t.Fatalf("prepared first definitions = %#v, want %#v", got, want)
	}
	if got, want := modulaTestDefinitionSymbols(prepared.sourceDefinitions(second)),
		[]string{"Second", "Other"}; !slices.Equal(got, want) {
		t.Fatalf("prepared stale-input definitions = %#v, want %#v", got, want)
	}
	inert := modulaTestLines("module example.com/cache\nrequire example.com/dependency v1.0.0\n")
	if got := prepared.sourceDefinitions(inert); len(got) != 0 {
		t.Fatalf("prepared gate transition parsed go.mod content: %#v", got)
	}
	if masked := prepared.searchLines(inert, true, true); len(masked) != len(inert) ||
		!strings.Contains(masked[1], "example.com/dependency") {
		t.Fatalf("prepared gate transition hid inert content: %#v", masked)
	}
	if got, want := modulaTestDefinitionSymbols(prepared.sourceDefinitions(second)),
		[]string{"Second", "Other"}; !slices.Equal(got, want) {
		t.Fatalf("prepared gate transition stayed inert: got %#v, want %#v", got, want)
	}

	first[0] = "MODULE Mutated;"
	first[len(first)-1] = "END Mutated."
	if got, want := modulaTestDefinitionSymbols(prepared.sourceDefinitions(first)),
		[]string{"Mutated", "Work"}; !slices.Equal(got, want) {
		t.Fatalf("prepared mutated-input definitions = %#v, want %#v", got, want)
	}

	stableLines := modulaTestLines(`MODULE Stable;
IMPORT IO;
PROCEDURE Work;
BEGIN
  IF ready THEN
    Target
  END
END Work;
BEGIN
END Stable.
`)
	stable := prepareLanguageBackend(newModulaLanguage(), stableLines)
	navigation := stable.(navigationScopeResolver)
	want := modulaTestDefinitionSymbols(stable.sourceDefinitions(stableLines))
	defensive := stable.sourceDefinitions(stableLines)
	defensive[0].symbol = "corrupted"
	if got := modulaTestDefinitionSymbols(stable.sourceDefinitions(stableLines)); !slices.Equal(got, want) {
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
				got := modulaTestDefinitionSymbols(stable.sourceDefinitions(stableLines))
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
