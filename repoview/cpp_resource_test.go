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

func TestCPPStreamingFallbackKeepsMiddleClassDefinition(t *testing.T) {
	padding := strings.Repeat(";", cppMaximumRetainedTokens/2+64)
	source := padding + "class MiddleClass { int member; };" + padding
	lexed := lexCPP(source)
	if !lexed.truncated {
		t.Fatal("fixture did not cross the retained-token frontier")
	}
	if got := cppDefinitionSymbols(newCPPLanguage().sourceDefinitions([]string{source})); !slices.Contains(got, "MiddleClass") {
		t.Fatalf("streaming fallback lost middle class definition: %#v", got)
	}

	root := t.TempDir()
	writeFile(t, root, "middle.cpp", source)
	found, err := mustView(t, root).Find("MiddleClass", Options{
		Include: IncludeDefs, Return: ReturnLocations,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found.Results) != 1 || found.Results[0].Kind != "def" {
		t.Fatalf("middle class Find = %#v, want one definition", found.Results)
	}
}

func TestCPPStreamingFallbackTracksMiddleClassAndNamespaceBoundaries(t *testing.T) {
	padding := strings.Repeat(";", cppMaximumRetainedTokens/2+64)
	for _, testCase := range []struct {
		name, heading, symbol string
	}{
		{name: "class", heading: "class MiddleClass ", symbol: "MiddleClass"},
		{name: "namespace", heading: "namespace MiddleNamespace ", symbol: "MiddleNamespace"},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			middle := testCase.heading +
				"{ int field = target(); } int after = outside();"
			source := padding + middle + padding
			analysis := analyzeCPPSource(source, 1)
			if analysis.tree != nil || !analysis.lexed.truncated {
				t.Fatalf("fixture did not enter streamed fallback: tree=%v truncated=%v",
					analysis.tree != nil, analysis.lexed.truncated)
			}
			definition := cppDefinitionAt(t, analysis.definitions, testCase.symbol, 1)
			wantEndColumn := strings.Index(source, "} int after") + 2
			if !definition.ownsScope || definition.scopeEnd != 1 ||
				definition.ownedEndColumn != wantEndColumn {
				t.Fatalf("streamed %s definition = %#v, want exact end 1:%d",
					testCase.name, definition, wantEndColumn)
			}

			root := t.TempDir()
			writeFile(t, root, "middle.cpp", source)
			responses, err := mustView(t, root).FindMany(
				[]string{"target", "outside"},
				Options{Include: IncludeRefs, Return: ReturnScope},
			)
			if err != nil {
				t.Fatal(err)
			}
			if len(responses) != 2 || len(responses[0].Results) != 1 ||
				responses[0].Results[0].Scope != testCase.symbol ||
				len(responses[1].Results) != 1 || responses[1].Results[0].Scope != "" {
				t.Fatalf("streamed %s FindMany = %#v, want inside %s and top-level outside",
					testCase.name, responses, testCase.symbol)
			}
		})
	}
}

func TestCPPStreamingFallbackDoesNotInventMiddleOwnerEOFBoundaries(t *testing.T) {
	padding := strings.Repeat(";", cppMaximumRetainedTokens/2+64)
	for _, testCase := range []struct {
		heading, symbol string
	}{
		{heading: "class Unfinished ", symbol: "Unfinished"},
		{heading: "namespace UnfinishedNamespace ", symbol: "UnfinishedNamespace"},
	} {
		source := padding + testCase.heading + "{ int field = target();" + padding
		analysis := analyzeCPPSource(source, 1)
		if analysis.tree != nil || !analysis.lexed.truncated {
			t.Fatalf("fixture did not enter streamed fallback: tree=%v truncated=%v",
				analysis.tree != nil, analysis.lexed.truncated)
		}
		definition := cppDefinitionAt(t, analysis.definitions, testCase.symbol, 1)
		if definition.ownsScope || definition.ownedEndColumn != 0 {
			t.Errorf("unfinished streamed definition = %#v, want non-owning EOF recovery",
				definition)
		}
	}
}

func TestCPPStreamingFallbackRecoversMiddleCXXCallableScopes(t *testing.T) {
	padding := strings.Repeat(";", cppMaximumRetainedTokens/2+128)
	middle := strings.Join([]string{
		"class Middle {",
		"public:",
		"    Middle(Value value = Value{}) noexcept(noexcept(Value{})) : first_{1}, second_(2) { ctor_target(); } int after_ctor = class_target();",
		"    ~Middle() { dtor_target(); }",
		"    explicit operator bool() const noexcept { conversion_target(); }",
		"    Middle operator+(int) const { operator_target(); return {}; }",
		"    auto trailing() const -> int { trailing_target(); return 1; }",
		"};",
		"int global_value = global_target();",
	}, "\n")
	source := padding + "\n" + middle + "\n" + padding
	analysis := analyzeCPPSource(source, len(strings.Split(source, "\n")))
	if analysis.tree != nil || !analysis.lexed.truncated {
		t.Fatalf("fixture did not enter streamed fallback: tree=%v truncated=%v",
			analysis.tree != nil, analysis.lexed.truncated)
	}

	lines := strings.Split(source, "\n")
	for _, testCase := range []struct {
		symbol, closingFragment string
		line                    int
	}{
		{symbol: "Middle", line: 4, closingFragment: "} int after_ctor"},
		{symbol: "~Middle", line: 5, closingFragment: "}"},
		{symbol: "operator bool", line: 6, closingFragment: "}"},
		{symbol: "operator+", line: 7, closingFragment: "}"},
		{symbol: "trailing", line: 8, closingFragment: "}"},
	} {
		definition := cppDefinitionAt(
			t, analysis.definitions, testCase.symbol, testCase.line,
		)
		wantEndColumn := strings.LastIndex(
			lines[testCase.line-1], testCase.closingFragment,
		) + 2
		if !definition.ownsScope || definition.scopeStart != testCase.line ||
			definition.scopeEnd != testCase.line ||
			definition.ownedEndColumn != wantEndColumn {
			t.Errorf("streamed callable %q = %#v, want exact end %d:%d",
				testCase.symbol, definition, testCase.line, wantEndColumn)
		}
	}
	classDefinition := cppDefinitionAt(t, analysis.definitions, "Middle", 2)
	if !classDefinition.ownsScope || classDefinition.scopeEnd != 9 ||
		classDefinition.ownedEndColumn != 2 {
		t.Errorf("streamed class = %#v, want exact end 9:2", classDefinition)
	}

	root := t.TempDir()
	writeFile(t, root, "middle.cpp", source)
	queries := []string{
		"ctor_target", "class_target", "dtor_target", "conversion_target",
		"operator_target", "trailing_target", "global_target",
	}
	wantScopes := []string{
		"Middle", "Middle", "~Middle", "operator bool", "operator+", "trailing", "",
	}
	responses, err := mustView(t, root).FindMany(
		queries, Options{Include: IncludeRefs, Return: ReturnScope},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(responses) != len(queries) {
		t.Fatalf("FindMany responses = %d, want %d", len(responses), len(queries))
	}
	for index, response := range responses {
		if len(response.Results) != 1 || response.Results[0].Scope != wantScopes[index] {
			t.Errorf("FindMany(%q) = %#v, want scope %q",
				queries[index], response.Results, wantScopes[index])
		}
	}
}

func TestCPPStreamingFallbackRecoversQualifiedCXXCallables(t *testing.T) {
	padding := strings.Repeat(";", cppMaximumRetainedTokens/2+128)
	middle := strings.Join([]string{
		"class Qualified {",
		"public:",
		"    Qualified();",
		"    ~Qualified();",
		"    explicit operator bool() const;",
		"    int value_;",
		"};",
		"Qualified::Qualified() : value_{1} { qualified_ctor_target(); }",
		"Qualified::~Qualified() { qualified_dtor_target(); }",
		"Qualified::operator bool() const { qualified_conversion_target(); }",
	}, "\n")
	source := padding + "\n" + middle + "\n" + padding
	analysis := analyzeCPPSource(source, len(strings.Split(source, "\n")))
	if analysis.tree != nil || !analysis.lexed.truncated {
		t.Fatalf("fixture did not enter streamed fallback: tree=%v truncated=%v",
			analysis.tree != nil, analysis.lexed.truncated)
	}
	for _, testCase := range []struct {
		symbol string
		line   int
	}{
		{symbol: "Qualified", line: 9},
		{symbol: "~Qualified", line: 10},
		{symbol: "operator bool", line: 11},
	} {
		definition := cppDefinitionAt(
			t, analysis.definitions, testCase.symbol, testCase.line,
		)
		if !definition.ownsScope || definition.scopeStart != testCase.line ||
			definition.scopeEnd != testCase.line || definition.ownedEndColumn == 0 {
			t.Errorf("qualified streamed callable %q = %#v, want exact owning scope",
				testCase.symbol, definition)
		}
	}

	root := t.TempDir()
	writeFile(t, root, "qualified.cpp", source)
	queries := []string{
		"qualified_ctor_target", "qualified_dtor_target", "qualified_conversion_target",
	}
	wantScopes := []string{"Qualified", "~Qualified", "operator bool"}
	responses, err := mustView(t, root).FindMany(
		queries, Options{Include: IncludeRefs, Return: ReturnScope},
	)
	if err != nil {
		t.Fatal(err)
	}
	for index, response := range responses {
		if len(response.Results) != 1 || response.Results[0].Scope != wantScopes[index] {
			t.Errorf("FindMany(%q) = %#v, want scope %q",
				queries[index], response.Results, wantScopes[index])
		}
	}
}

func TestCPPStreamingFallbackDoesNotPromoteSpecialMemberCalls(t *testing.T) {
	source := "class Guard { void member() { Guard(); this->~Guard(); } };"
	scanner := newCPPOwningDefinitionStreamScanner(source, cLineStarts(source))
	cppCodeTokensObserved(source, nil, nil, nil, scanner.consume)
	if got := cppDefinitionCount(scanner.definitions, "Guard"); got != 1 {
		t.Fatalf("streamed Guard definitions = %d, want only the class: %#v",
			got, scanner.definitions)
	}
	if got := cppDefinitionCount(scanner.definitions, "~Guard"); got != 0 {
		t.Fatalf("explicit destructor call became %d definitions: %#v",
			got, scanner.definitions)
	}
}

func TestCPPOwningDefinitionStreamScannerDoesNotOwnElaboratedReturnType(t *testing.T) {
	source := "struct Result make() { target(); }"
	scanner := newCPPOwningDefinitionStreamScanner(source, cLineStarts(source))
	cppCodeTokensObserved(source, nil, nil, nil, scanner.consume)
	result := cppDefinitionAt(t, scanner.definitions, "Result", 1)
	if result.ownsScope {
		t.Fatalf("elaborated return type owned the function body: %#v", result)
	}
	makeDefinition := cppDefinitionAt(t, scanner.definitions, "make", 1)
	if !makeDefinition.ownsScope || makeDefinition.ownedEndColumn != len(source)+1 {
		t.Fatalf("function after elaborated return type = %#v, want exact body",
			makeDefinition)
	}
}

func TestCPPStreamingFallbackKeepsUnfinishedCallableNonOwning(t *testing.T) {
	padding := strings.Repeat(";", cppMaximumRetainedTokens/2+128)
	source := padding + "\nclass Unfinished {\nUnfinished() { eof_target();" + padding
	analysis := analyzeCPPSource(source, len(strings.Split(source, "\n")))
	if analysis.tree != nil || !analysis.lexed.truncated {
		t.Fatalf("fixture did not enter streamed fallback: tree=%v truncated=%v",
			analysis.tree != nil, analysis.lexed.truncated)
	}
	definition := cppDefinitionAt(t, analysis.definitions, "Unfinished", 3)
	if definition.ownsScope || definition.ownedEndColumn != 0 {
		t.Fatalf("unfinished streamed constructor = %#v, want non-owning EOF recovery",
			definition)
	}
}

func TestCPPOwningDefinitionStreamScannerBoundsCallableHeader(t *testing.T) {
	source := "Identifier;"
	scanner := newCPPOwningDefinitionStreamScanner(source, cLineStarts(source))
	identifier := cToken{
		kind: cTokenIdentifier, text: "Identifier", start: 0, end: len("Identifier"),
	}
	for range cppMaximumStreamingCallableTokens + 64 {
		scanner.consume(identifier)
	}
	if !scanner.statementOverflow || len(scanner.statement) != 0 {
		t.Fatalf("oversized callable header retained state: len=%d overflow=%v",
			len(scanner.statement), scanner.statementOverflow)
	}
	if cap(scanner.statement) > 2*cppMaximumStreamingCallableTokens {
		t.Fatalf("callable header capacity = %d, want bounded near %d",
			cap(scanner.statement), cppMaximumStreamingCallableTokens)
	}
	scanner.consume(cToken{text: ";", start: len("Identifier"), end: len(source)})
	if scanner.statementOverflow || len(scanner.statement) != 0 {
		t.Fatalf("statement boundary did not reset bounded header state: %#v", scanner)
	}
}

func TestCPPOwningDefinitionStreamScannerBoundsStructuralDepth(t *testing.T) {
	depth := cppMaximumStreamingOwnerDepth + 64
	source := "class Outer {" + strings.Repeat("{", depth) +
		strings.Repeat("}", depth) + "}"
	lineStarts := cLineStarts(source)
	scanner := newCPPOwningDefinitionStreamScanner(source, lineStarts)
	cppCodeTokensObserved(source, nil, nil, nil, scanner.consume)
	if len(scanner.frames) != 0 || scanner.overflowDepth != 0 {
		t.Fatalf("streaming owner stack did not unwind: frames=%d overflow=%d",
			len(scanner.frames), scanner.overflowDepth)
	}
	if cap(scanner.frames) > 2*cppMaximumStreamingOwnerDepth {
		t.Fatalf("streaming owner frame capacity = %d, want bounded near %d",
			cap(scanner.frames), cppMaximumStreamingOwnerDepth)
	}
	definition := cppDefinitionAt(t, scanner.definitions, "Outer", 1)
	if !definition.ownsScope || definition.ownedEndColumn != len(source)+1 {
		t.Fatalf("deep streamed owner = %#v, want confirmed outer boundary", definition)
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
