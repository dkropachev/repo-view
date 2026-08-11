package navigator

import (
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestCConcreteParserResourceCaps(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		atCap   string
		overCap string
	}{
		{
			name:    "delimiter depth",
			atCap:   cResourceNestedInitializer(cMaximumConcreteDelimiterDepth),
			overCap: cResourceNestedInitializer(cMaximumConcreteDelimiterDepth + 1),
		},
		{
			name:    "preprocessor depth",
			atCap:   cResourceConditionalSource(cMaximumConcretePreprocessorDepth),
			overCap: cResourceConditionalSource(cMaximumConcretePreprocessorDepth + 1),
		},
		{
			name: "adjacent attributes",
			atCap: strings.Repeat("[[gnu::unused]] ", cMaximumConcreteAdjacentAttributes) +
				"int value;\n",
			overCap: strings.Repeat("[[gnu::unused]] ", cMaximumConcreteAdjacentAttributes+1) +
				"int value;\n",
		},
		{
			name:    "groups per segment",
			atCap:   cResourceGroupedInitializer(cMaximumConcreteGroupsPerSegment),
			overCap: cResourceGroupedInitializer(cMaximumConcreteGroupsPerSegment + 1),
		},
		{
			name: "expression prefix",
			atCap: "int value = " + strings.Repeat("!", cMaximumConcreteExpressionPrefix) +
				"ready;\n",
			overCap: "int value = " + strings.Repeat("!", cMaximumConcreteExpressionPrefix+1) +
				"ready;\n",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			atCap := lexC(test.atCap)
			if !atCap.concreteEligible {
				t.Fatal("source at the documented cap was rejected")
			}
			overCap := lexC(test.overCap)
			if overCap.concreteEligible {
				t.Fatal("source above the documented cap remained parser-eligible")
			}
			if tree, ok := parseCSyntaxWithLexed(test.overCap, overCap); ok || tree != nil {
				t.Fatal("over-cap source reached the concrete parser")
			}
		})
	}
}

func TestCConcreteParserAcceptsBoundaryAttributeFrontier(t *testing.T) {
	t.Parallel()

	source := strings.Repeat(
		"[[gnu::unused]] ",
		cMaximumConcreteAdjacentAttributes,
	) + "int boundary_value;\n"
	tree, ok := parseCSyntax(source)
	if !ok || tree == nil || !validateCSyntaxTree(tree, len(source)) {
		t.Fatal("valid source at the adjacent-attribute cap did not produce a validated tree")
	}
	for _, node := range tree.nodes {
		if node.kind == "ERROR" {
			t.Fatal("valid source at the adjacent-attribute cap produced ERROR")
		}
	}
}

func TestCConcreteParserLexicalUnitAndByteCaps(t *testing.T) {
	t.Parallel()

	var source strings.Builder
	for index := range cMaximumConcreteLexicalUnits / 3 {
		source.WriteString("int v")
		source.WriteString(strconv.Itoa(index))
		source.WriteString(";\n")
	}
	if lexed := lexC(source.String()); !lexed.concreteEligible {
		t.Fatal("source below the lexical-unit cap was rejected")
	}
	source.WriteString("int overflow;\n")
	if lexed := lexC(source.String()); lexed.concreteEligible {
		t.Fatal("source above the lexical-unit cap remained eligible")
	}

	oversized := "/*" + strings.Repeat("x", cMaximumConcreteParseBytes) + "*/"
	if tree, ok := parseCSyntax(oversized); ok || tree != nil {
		t.Fatal("source above the byte cap reached the concrete parser")
	}
}

func TestCConcreteResourceGatesIgnoreOpaqueAndDirectiveReplacementText(t *testing.T) {
	t.Parallel()

	hot := strings.Repeat("([[!?:#if [[gnu::unused]] ", 512)
	source := "const char *text = \"" + hot + "\";\n" +
		"char character = '}';\n" +
		"/* " + hot + " */\n" +
		"#define HOT(value) " + hot + "\n" +
		"#include <path/with/((((brackets.h>\n" +
		"int tail(void) { return 0; }\n"
	lexed := lexC(source)
	if !lexed.concreteEligible {
		t.Fatal("opaque text or preprocessor replacement tokens tripped a concrete gate")
	}
	definitions := newCLanguage().sourceDefinitions(strings.Split(strings.TrimSuffix(source, "\n"), "\n"))
	if got := cHighLevelDefinitionSymbols(definitions); !slices.Equal(got, []string{"text", "character", "HOT", "tail"}) {
		t.Fatalf("opaque gate fixture definitions = %#v", got)
	}
}

func TestCOverCapFallbackRetainsIndependentTailDefinitions(t *testing.T) {
	t.Parallel()

	tests := []string{
		strings.Repeat("[[gnu::unused]] ", cMaximumConcreteAdjacentAttributes+1) +
			"int attributed;\nint tail(void) { return 0; }\n",
		"int broken = " + strings.Repeat("(", cMaximumConcreteDelimiterDepth+1) +
			"0;\nint tail(void) { return 0; }\n",
		strings.Repeat("#if READY\n", cMaximumConcretePreprocessorDepth+1) +
			"int branch_value;\nint tail(void) { return 0; }\n",
	}
	for index, source := range tests {
		t.Run(strconv.Itoa(index), func(t *testing.T) {
			t.Parallel()
			lexed := lexC(source)
			if lexed.concreteEligible {
				t.Fatal("over-cap recovery fixture unexpectedly parser-eligible")
			}
			if got := cHighLevelDefinitionSymbols(
				newCLanguage().sourceDefinitions(strings.Split(strings.TrimSuffix(source, "\n"), "\n")),
			); !slices.Contains(got, "tail") {
				t.Fatalf("fallback lost independent tail definition: %#v", got)
			}
		})
	}
}

func TestCRejectedConcreteGateDoesNotAllocate(t *testing.T) {
	lexed := cLexResult{concreteEligible: false}
	if allocations := testing.AllocsPerRun(100, func() {
		if tree, ok := parseCSyntaxWithLexed("int value;", lexed); ok || tree != nil {
			t.Fatal("ineligible source reached parser")
		}
	}); allocations != 0 {
		t.Fatalf("rejected concrete gate allocations = %v, want 0", allocations)
	}
}

func TestCFindScopeIndexCapsLineProportionalStorage(t *testing.T) {
	t.Parallel()

	definition := sourceDefinition{
		symbol: "owner", line: 2, column: 1,
		scopeStart: 2, scopeEnd: 4, ownsScope: true,
	}
	scope := cLineScope{start: 1, end: 5}
	dense := newCPreparedFindScopeResolver(
		[]sourceDefinition{definition},
		[]cLineScope{scope},
		cMaximumIndexedFindLines,
	)
	if got := len(dense.ownerByLine); got != cMaximumIndexedFindLines+1 {
		t.Fatalf("dense owner index length = %d", got)
	}
	if start, end := dense.navigationScope(3); start != 2 || end != 4 {
		t.Fatalf("dense owner scope = %d-%d, want 2-4", start, end)
	}

	sparse := newCPreparedFindScopeResolver(
		[]sourceDefinition{definition},
		[]cLineScope{scope},
		cMaximumIndexedFindLines+1,
	)
	if len(sparse.ownerByLine) != 0 || len(sparse.scopeByLine) != 0 ||
		len(sparse.scopes) != 1 || len(sparse.ownerRuns) != 1 || len(sparse.scopeRuns) != 1 {
		t.Fatalf("sparse resolver retained dense indexes: %#v", sparse)
	}
	if start, end := sparse.navigationScope(3); start != 2 || end != 4 ||
		sparse.scopeName(3) != "owner" {
		t.Fatalf("sparse owner metadata = %d-%d, %q", start, end, sparse.scopeName(3))
	}
}

func TestCDenseAndRunLengthScopeIndexesUseIdenticalTieBreaks(t *testing.T) {
	t.Parallel()

	definitions := []sourceDefinition{
		{
			symbol: "First", line: 4, column: 3,
			scopeStart: 1, scopeEnd: 5, ownsScope: true,
		},
		{
			symbol: "Second", line: 5, column: 3,
			scopeStart: 1, scopeEnd: 5, ownsScope: true,
		},
		{
			symbol: "Nested", line: 2, column: 5,
			scopeStart: 2, scopeEnd: 3, ownsScope: true,
		},
	}
	scopes := []cLineScope{{start: 1, end: 5}, {start: 2, end: 3}}
	dense := newCPreparedFindScopeResolver(definitions, scopes, 16)
	sparse := newCPreparedFindScopeResolver(
		definitions,
		scopes,
		cMaximumIndexedFindLines+1,
	)
	for lineNo := 1; lineNo <= 8; lineNo++ {
		denseStart, denseEnd := dense.navigationScope(lineNo)
		sparseStart, sparseEnd := sparse.navigationScope(lineNo)
		if denseStart != sparseStart || denseEnd != sparseEnd ||
			dense.scopeName(lineNo) != sparse.scopeName(lineNo) {
			t.Errorf("line %d dense=%d-%d/%q sparse=%d-%d/%q",
				lineNo,
				denseStart,
				denseEnd,
				dense.scopeName(lineNo),
				sparseStart,
				sparseEnd,
				sparse.scopeName(lineNo),
			)
		}
	}
	if got := dense.scopeName(4); got != "First" {
		t.Fatalf("identical-scope tie chose %q, want first definition", got)
	}
}

func TestCFindScopeRunIndexMatchesDensePainting(t *testing.T) {
	t.Parallel()

	const lineCount = 127
	definitions := make([]sourceDefinition, 0, 256)
	scopes := make([]cLineScope, 0, 256)
	for index := range 256 {
		start := index*37%lineCount + 1
		end := min(lineCount, start+index*19%31)
		definitions = append(definitions, sourceDefinition{
			symbol: "owner_" + strconv.Itoa(index),
			line:   start, column: 1,
			scopeStart: start, scopeEnd: end, ownsScope: true,
		})
		scopes = append(scopes, cLineScope{start: start, end: end})
	}
	dense := newCPreparedFindScopeResolver(definitions, scopes, lineCount)
	ownerRuns := cBuildFindScopeRuns(
		cDefinitionOwnerCandidates(definitions, lineCount),
		lineCount,
	)
	scopeRuns := cBuildFindScopeRuns(
		cEnclosingScopeCandidates(scopes, lineCount),
		lineCount,
	)
	for lineNo := 1; lineNo <= lineCount; lineNo++ {
		if got, want := cFindScopeRunValue(ownerRuns, lineNo), dense.ownerByLine[lineNo]; got != want {
			t.Errorf("owner run line %d = %d, want %d", lineNo, got, want)
		}
		gotScope := cFindScopeRunValue(scopeRuns, lineNo)
		wantScope := cFindScopeRunValueForDenseScope(scopes, dense.scopeByLine[lineNo])
		if gotScope != wantScope {
			t.Errorf("scope run line %d = %d, want %d", lineNo, gotScope, wantScope)
		}
	}
}

func cFindScopeRunValueForDenseScope(scopes []cLineScope, want cLineScope) int {
	if want.start == 0 {
		return 0
	}
	for index, scope := range scopes {
		if scope == want {
			return index + 1
		}
	}
	return -1
}

func TestCSpliceOccurrencesStreamAcrossRetainedTokenGap(t *testing.T) {
	prefixLines := cRetainedTokenHead/2 + 20
	suffixLines := cRetainedTokenHead/2 + 100
	lines := make([]string, 0, prefixLines+suffixLines+16)
	lines = append(lines, "void retained_gap_fixture(void) {")
	for range prefixLines {
		lines = append(lines, "0;")
	}
	targetLine := len(lines) + 1
	lines = append(lines, "tar\\", "get;")

	type symbolCase struct {
		name string
		line int
		want string
		ok   bool
	}
	appendCase := func(name, source, want string, ok bool) symbolCase {
		lineNo := len(lines) + 1
		lines = append(lines, source)
		return symbolCase{name: name, line: lineNo, want: want, ok: ok}
	}
	symbolCases := []symbolCase{
		appendCase(
			"call beats earlier member and identifier",
			"first_value + object.member_value + selected_call();",
			"selected_call",
			true,
		),
		appendCase(
			"member beats earlier identifier",
			"first_value + object.selected_member;",
			"selected_member",
			true,
		),
		appendCase("numeric literal", "0x1.deadp0;", "", false),
		appendCase("comment", "/* hidden_call(); */", "", false),
		appendCase("string literal", `"hidden_call();";`, "", false),
		appendCase("header name", "#include <hidden/header.h>", "", false),
		appendCase(
			"defined operand",
			"#if defined(FEATURE_FLAG)",
			"FEATURE_FLAG",
			true,
		),
	}
	lines = append(lines, "#endif")
	longLine := len(lines) + 1
	lines = append(lines,
		strings.Repeat("0 + ", 16<<10)+"long_gap_call();")
	for range suffixLines {
		lines = append(lines, "0;")
	}
	lines = append(lines, "}")

	backend := prepareLanguageBackend(newCLanguage(), lines).(cLanguage)
	if backend.analysis == nil || !backend.analysis.lexed.truncated {
		t.Fatal("retention-gap fixture did not truncate lexical tokens")
	}
	for _, token := range backend.analysis.lexed.tokens {
		if token.text == "target" {
			t.Fatal("retention-gap fixture accidentally retained the logical target token")
		}
	}
	gapLines := []int{targetLine, targetLine + 1, longLine}
	for _, test := range symbolCases {
		gapLines = append(gapLines, test.line)
	}
	for _, lineNo := range gapLines {
		if !cTestRetainedTokenGapContainsLine(backend.analysis, lineNo) {
			t.Fatalf("line %d is not wholly inside the retained-token gap", lineNo)
		}
	}
	for _, lineNo := range []int{targetLine, targetLine + 1} {
		if symbol, ok := backend.symbolOnLine(lines, lineNo); !ok || symbol != "target" {
			t.Errorf("retention-gap symbol on line %d = %q, %v; want target",
				lineNo, symbol, ok)
		}
	}
	for _, test := range symbolCases {
		t.Run(test.name, func(t *testing.T) {
			got, ok := backend.symbolOnLine(lines, test.line)
			if got != test.want || ok != test.ok {
				t.Fatalf("symbolOnLine(%d) = %q, %v; want %q, %v",
					test.line, got, ok, test.want, test.ok)
			}
		})
	}
	if symbol, ok := backend.symbolOnLine(lines, longLine); !ok || symbol != "long_gap_call" {
		t.Errorf("long retention-gap line symbol = %q, %v; want long_gap_call, true",
			symbol, ok)
	}

	for _, test := range []struct {
		symbol     string
		adjustment int
	}{
		{symbol: "target", adjustment: 1},
		{symbol: "tar", adjustment: -1},
		{symbol: "get", adjustment: -1},
	} {
		nonzero := map[int]int{}
		handled := backend.walkAdditionalSymbolOccurrences(
			lines,
			test.symbol,
			func(lineNo, adjustment int) bool {
				if adjustment != 0 {
					nonzero[lineNo] = adjustment
				}
				return true
			},
		)
		wantLine := targetLine
		if test.symbol == "get" {
			wantLine++
		}
		if !handled || len(nonzero) != 1 || nonzero[wantLine] != test.adjustment {
			t.Errorf("%s retention-gap corrections = %#v, handled=%v; want line %d => %d",
				test.symbol, nonzero, handled, wantLine, test.adjustment)
		}
	}

	root := t.TempDir()
	writeFile(t, root, "retained-gap.c", strings.Join(lines, "\n")+"\n")
	view := mustView(t, root)
	for _, lineNo := range []int{targetLine, targetLine + 1} {
		inspected, err := view.Inspect(
			"retained-gap.c:"+strconv.Itoa(lineNo),
			Options{Include: IncludeScope, Return: ReturnLocations},
		)
		if err != nil {
			t.Fatal(err)
		}
		if inspected.Symbol != "target" {
			t.Errorf("Inspect splice line %d symbol = %q, want target",
				lineNo, inspected.Symbol)
		}
	}
	found, err := view.Find(
		"target",
		Options{Include: IncludeBoth, Return: ReturnLocations},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := resultLines(found.Results), []int{targetLine}; !slices.Equal(got, want) {
		t.Fatalf("Find retained-gap splice lines = %#v, want %#v", got, want)
	}
}

func cTestRetainedTokenGapContainsLine(analysis *cSourceAnalysis, lineNo int) bool {
	if analysis == nil || lineNo < 1 || lineNo > analysis.lineCount {
		return false
	}
	gapStart, gapEnd := -1, -1
	for index, token := range analysis.lexed.tokens {
		if !token.gapBefore || index == 0 {
			continue
		}
		gapStart = analysis.lexed.tokens[index-1].end
		gapEnd = token.start
		break
	}
	if gapStart < 0 || gapEnd < gapStart {
		return false
	}
	lineStart := analysis.lineStarts[lineNo-1]
	lineEnd := len(analysis.source)
	if lineNo < len(analysis.lineStarts) {
		lineEnd = analysis.lineStarts[lineNo]
	}
	return gapStart <= lineStart && lineEnd <= gapEnd
}

func TestCMalformedBracedUCNScanningHasABoundedFrontier(t *testing.T) {
	t.Parallel()

	const sequences = 16 << 10
	source := strings.Repeat(`\u{`, sequences)
	for offset := 0; offset < len(source); offset += len(`\u{`) {
		if _, _, ok := cIdentifierUCN(source, offset); ok {
			t.Fatalf("nonstandard braced UCN accepted at byte %d", offset)
		}
	}
}

func cResourceNestedInitializer(depth int) string {
	return "int value = " + strings.Repeat("(", depth) + "0" +
		strings.Repeat(")", depth) + ";\n"
}

func cResourceConditionalSource(depth int) string {
	return strings.Repeat("#if READY\n", depth) + "int value;\n" +
		strings.Repeat("#endif\n", depth)
}

func cResourceGroupedInitializer(groups int) string {
	var source strings.Builder
	source.WriteString("int values[] = {")
	for index := range groups {
		if index > 0 {
			source.WriteByte(',')
		}
		source.WriteString("(0)")
	}
	source.WriteString("};\n")
	return source.String()
}
