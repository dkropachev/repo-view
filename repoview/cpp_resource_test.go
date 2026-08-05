package repoview

import (
	"slices"
	"strings"
	"testing"
)

func TestCPPTokenRetentionIsBoundedAndStreamingFallbackKeepsMiddleDefinition(t *testing.T) {
	// Position the only declaration exactly inside the discarded middle rather
	// than the retained head or tail.
	before := strings.Repeat(";", cppMaximumRetainedTokens/2+2048)
	after := strings.Repeat(";", cppMaximumRetainedTokens/2+2048)
	source := before + " int middle() { return 1; } " + after
	lexed := lexCPP(source)
	if !lexed.truncated {
		t.Fatal("fixture did not cross the retained-token frontier")
	}
	if len(lexed.tokens) > cppMaximumRetainedTokens {
		t.Fatalf("retained tokens = %d, cap %d", len(lexed.tokens), cppMaximumRetainedTokens)
	}
	if got := cppDefinitionSymbols(newCPPLanguage().sourceDefinitions([]string{source})); !slices.Contains(got, "middle") {
		t.Fatalf("streaming fallback lost middle definition: %#v", got)
	}
}

func TestCPPRetainedTailDoesNotTurnNestedContextualImportIntoDependency(t *testing.T) {
	prefix := "int caller() {\n" + strings.Repeat("value++;", cppMaximumRetainedTokens+1024)
	source := prefix + "\nimport fake;\n}\n"
	lexed := lexCPP(source)
	if !lexed.truncated {
		t.Fatal("fixture did not cross the retained-token frontier")
	}
	if len(lexed.imports) != 0 {
		t.Fatalf("nested tail import became dependency: %#v", lexed.imports)
	}
}

func TestCPPOverConcreteCapsFallbackRetainsIndependentTail(t *testing.T) {
	tests := []string{
		"/*" + strings.Repeat("opaque } < template fake();", cppMaximumConcreteParseBytes/16) +
			"*/\nint byte_tail() { return 1; }\n",
		"using Deep = " + strings.Repeat("Box<", cppMaximumConcreteAngleFrontier+1) +
			"int" + strings.Repeat(">", cppMaximumConcreteAngleFrontier+1) +
			";\nint angle_tail() { return 1; }\n",
		strings.Repeat("value + ", cppMaximumConcreteLexicalUnits+1) +
			"0;\nint token_tail() { return 1; }\n",
	}
	want := []string{"byte_tail", "angle_tail", "token_tail"}
	for index, source := range tests {
		preflight := preflightCPPSyntax(source)
		if preflight.concreteEligible {
			t.Fatalf("fixture %d unexpectedly concrete-eligible", index)
		}
		if tree, ok := parseCPPSyntaxWithPreflight(source, preflight); ok || tree != nil {
			t.Fatalf("fixture %d reached concrete parser", index)
		}
		got := cppDefinitionSymbols(newCPPLanguage().sourceDefinitions(cppTestLines(source)))
		if !slices.Contains(got, want[index]) {
			t.Fatalf("fixture %d lost tail %q: %#v", index, want[index], got)
		}
	}
}

func TestCPPOverConcreteCapDoesNotPromoteInitializerCallsToDefinitions(t *testing.T) {
	source := "auto holder = target();\n" +
		strings.Repeat(";", cppMaximumConcreteParseBytes+1)
	if preflight := preflightCPPSyntax(source); preflight.concreteEligible {
		t.Fatalf("fixture unexpectedly concrete-eligible: %#v", preflight)
	}
	definitions := cppDefinitionSymbols(
		newCPPLanguage().sourceDefinitions(cppTestLines(source)),
	)
	if slices.Contains(definitions, "target") {
		t.Fatalf("initializer call became a fallback definition: %#v", definitions)
	}
}

func TestCPPOverConcreteCapDoesNotPromoteNestedDeclaratorCalls(t *testing.T) {
	source := `int register_callback(int (*callback)(int));
auto holder(target());
` + strings.Repeat(";", cppMaximumConcreteParseBytes+1)
	definitions := cppDefinitionSymbols(
		newCPPLanguage().sourceDefinitions(cppTestLines(source)),
	)
	for _, want := range []string{"register_callback", "holder"} {
		if !slices.Contains(definitions, want) {
			t.Errorf("fallback definitions = %#v, missing %q", definitions, want)
		}
	}
	for _, phantom := range []string{"int", "callback", "target"} {
		if slices.Contains(definitions, phantom) {
			t.Errorf("nested declarator/call %q became a fallback definition: %#v",
				phantom, definitions)
		}
	}
}

func TestCPPImportPhantomFilterHandlesManySortedSpans(t *testing.T) {
	const count = 16 << 10
	lineStarts := make([]int, count)
	definitions := make([]sourceDefinition, count)
	imports := make([]cByteSpan, 0, count/2)
	for index := range count {
		lineStarts[index] = index * 8
		definitions[index] = sourceDefinition{
			symbol: "value", line: index + 1, column: 1,
		}
		if index%2 == 0 {
			imports = append(imports, cByteSpan{
				start: lineStarts[index], end: lineStarts[index] + 1,
			})
		}
	}

	filtered := cppFilterImportPhantoms(definitions, lineStarts, imports)
	if len(filtered) != count/2 {
		t.Fatalf("filtered definitions = %d, want %d", len(filtered), count/2)
	}
	for index, definition := range filtered {
		wantLine := index*2 + 2
		if definition.line != wantLine {
			t.Fatalf("filtered definition %d line = %d, want %d",
				index, definition.line, wantLine)
		}
	}
}

func TestCPPRejectedConcreteGateDoesNotAllocate(t *testing.T) {
	preflight := cppSyntaxPreflight{source: "int value;"}
	if allocations := testing.AllocsPerRun(100, func() {
		if tree, ok := parseCPPSyntaxWithPreflight("int value;", preflight); ok || tree != nil {
			t.Fatal("ineligible source reached parser")
		}
	}); allocations != 0 {
		t.Fatalf("rejected parser gate allocations = %v, want 0", allocations)
	}
}

func TestCPPConcretePreflightBoundsConditionalDirectiveDepth(t *testing.T) {
	atLimit := strings.Repeat("#if CONDITION\n", cppMaximumConcretePreprocessorDepth) +
		strings.Repeat("#endif\n", cppMaximumConcretePreprocessorDepth)
	if preflight := preflightCPPSyntax(atLimit); !preflight.concreteEligible {
		t.Fatalf("conditional depth at limit was rejected: %#v", preflight)
	}
	overLimit := strings.Repeat("#if CONDITION\n", cppMaximumConcretePreprocessorDepth+1) +
		strings.Repeat("#endif\n", cppMaximumConcretePreprocessorDepth+1)
	preflight := preflightCPPSyntax(overLimit)
	if preflight.concreteEligible {
		t.Fatalf("conditional depth over limit remained eligible: %#v", preflight)
	}
	allocations := testing.AllocsPerRun(100, func() {
		if tree, ok := parseCPPSyntaxWithPreflight(overLimit, preflight); ok || tree != nil {
			t.Fatal("rejected conditional frontier reached concrete parser")
		}
	})
	if allocations != 0 {
		t.Fatalf("rejected conditional parse allocated %.2f objects, want zero", allocations)
	}
}

func TestCPPTinyLexDoesNotEagerlyAllocateRetentionTail(t *testing.T) {
	result := testing.Benchmark(func(b *testing.B) {
		b.Helper()
		for range b.N {
			_ = lexCPP("int tiny();")
		}
	})
	if bytes := result.AllocedBytesPerOp(); bytes > 1<<20 {
		t.Fatalf("tiny lexical analysis allocated %d bytes/op, want <= 1MiB", bytes)
	}
}

func TestCPPDiscardedTokenGapIsHardDeclarationBoundary(t *testing.T) {
	headLimit := (cppMaximumRetainedTokens - 1) / 2
	tailLimit := cppMaximumRetainedTokens - headLimit - 1
	source := strings.Repeat(";", headLimit-1) + "namespace" +
		strings.Repeat(";", 64) + "Phantom{}" +
		strings.Repeat(";", tailLimit-3)

	lexed := lexCPP(source)
	if !lexed.truncated || len(lexed.tokens) != cppMaximumRetainedTokens {
		t.Fatalf("fixture retention = (%t, %d), want truncated cap %d",
			lexed.truncated, len(lexed.tokens), cppMaximumRetainedTokens)
	}
	if got := lexed.tokens[headLimit].text; got != cppTokenGap {
		t.Fatalf("retention boundary token = %q, want gap", got)
	}
	if got := cppDefinitionSymbols(cppLexicalDefinitions(source, lexed.tokens)); slices.Contains(got, "Phantom") {
		t.Fatalf("discarded middle joined unrelated declaration tokens: %#v", got)
	}

	tokens := []cToken{
		{text: "int", kind: cTokenIdentifier},
		{text: "candidate", kind: cTokenIdentifier},
		{text: "(", kind: cTokenPunctuation},
		{text: ")", kind: cTokenPunctuation},
		{text: cppTokenGap, kind: cTokenDirective},
		{text: "->", kind: cTokenPunctuation},
		{text: "int", kind: cTokenIdentifier},
		{text: ";", kind: cTokenPunctuation},
	}
	pairs := cppDelimiterPairs(tokens)
	if terminator := cppCallableTerminator(tokens, 4, pairs); terminator >= 0 {
		t.Fatalf("callable terminator crossed gap to token %d", terminator)
	}
	if cppTokenRangeContains(tokens, 4, len(tokens), "->") {
		t.Fatal("token range search crossed discarded gap")
	}
	if cppDeclarationEvidence(tokens, 5) {
		t.Fatal("declaration evidence crossed discarded gap")
	}
}
