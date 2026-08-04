package repoview

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestJavaUnicodeEscapesPrecedeAllLexicalDecisions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		source string
		want   []string
	}{
		{
			name:   "escaped line comment hides declaration",
			source: "class Safe {}\n" + `\u002f\u002f class Hidden {}`,
			want:   []string{"Safe"},
		},
		{
			name:   "escaped line terminator ends comment",
			source: `// comment \u000a class Visible {}`,
			want:   []string{"Visible"},
		},
		{
			name:   "escaped keywords and separators",
			source: `\u0063lass Escaped \u007b int value\u003b \u007d`,
			want:   []string{"Escaped", "value"},
		},
		{
			name:   "supplementary identifier from surrogate escapes",
			source: `class \uD835\uDC9C { void \uD835\uDC9CMethod() {} }`,
			want:   []string{`\uD835\uDC9C`, `\uD835\uDC9CMethod`},
		},
		{
			name:   "identifier ignorable control escape",
			source: `class A\u0001B { void m\u0001n() {} }`,
			want:   []string{`A\u0001B`, `m\u0001n`},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			analysis := analyzeJavaSource(
				test.source, strings.Count(test.source, "\n")+1,
			)
			if !analysis.lexed.translatedEscapes {
				t.Fatal("fixture did not exercise JLS Unicode translation")
			}
			if got := javaDefinitionSymbols(analysis.definitions); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("definitions = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestJavaUnicodeEscapeEligibilityAndCoordinatePreservingMasks(t *testing.T) {
	t.Parallel()

	const source = `class Visible {
    String literal = "\\u002f\\u002f not a comment";
    String escapedQuotes = \u0022masked target\u0022;
}
// prefix \u000a class AfterComment {}`
	analysis := analyzeJavaSource(source, strings.Count(source, "\n")+1)
	if got, want := javaDefinitionSymbols(analysis.definitions),
		[]string{"Visible", "literal", "escapedQuotes", "AfterComment"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("definitions = %#v, want %#v", got, want)
	}

	backend := newJavaLanguage().analysisForSource(source, strings.Count(source, "\n")+1)
	prepared := newJavaLanguage()
	prepared.analysis = backend
	lines := strings.Split(source, "\n")
	searchable := prepared.searchLines(lines, true, true)
	if strings.Contains(strings.Join(searchable, "\n"), "masked target") {
		t.Fatalf("escaped quotes did not create an opaque literal: %#v", searchable)
	}
	if !strings.Contains(searchable[len(searchable)-1], "class AfterComment") {
		t.Fatalf("escaped line terminator kept visible code inside comment: %q", searchable[len(searchable)-1])
	}
	if len(strings.Join(searchable, "\n")) != len(source) {
		t.Fatalf("search mask changed byte coordinates: got %d, want %d",
			len(strings.Join(searchable, "\n")), len(source))
	}
}

func TestJavaUnicodeEscapeBackslashEligibility(t *testing.T) {
	t.Parallel()

	tests := []struct {
		source string
		want   int
	}{
		{source: `\u0063lass C {}`, want: 1},
		{source: `\\u0063lass C {}`, want: 0},
		{source: `\\\u0063lass C {}`, want: 1},
		{source: `\u005c\u0063lass C {}`, want: 2},
	}
	for _, test := range tests {
		input := newJavaUnicodeInput(test.source)
		if got := len(input.escapes); got != test.want {
			t.Fatalf("Unicode escapes in %q = %d, want %d", test.source, got, test.want)
		}
	}
}

func TestJavaTemplateDepthCapMasksUnscannedExpression(t *testing.T) {
	t.Parallel()

	nested := `"TARGET"`
	for range javaMaximumTemplateDepth + 2 {
		nested = `STR."\{` + nested + `}"`
	}
	const prefixSource = "class Templates { Object value = "
	source := prefixSource + nested + ";\nvoid recovered() { target(); }\n}"
	lexed := lexJava(source)
	target := strings.Index(nested, "TARGET")
	if target < 0 {
		t.Fatal("fixture lost TARGET")
	}
	// The absolute offset includes the declaration prefix used above.
	prefix := len(prefixSource)
	if !javaByteRangeIntersects(prefix+target, prefix+target+len("TARGET"), lexed.stringSpans) {
		t.Fatalf("depth-capped template leaked deepest literal; spans=%#v", lexed.stringSpans)
	}
	masked := maskJavaSearchSource(source, lexed.input, true, true)
	if strings.Contains(masked, "TARGET") {
		t.Fatalf("depth-capped template leaked deepest literal:\n%s", masked)
	}
	if !strings.Contains(masked, "void recovered() { target(); }") {
		t.Fatalf("depth-capped template masked unrelated following code:\n%s", masked)
	}
}

func TestJavaTemplateDepthRecoveryPreservesBalancedBoundaries(t *testing.T) {
	t.Parallel()

	nested := `"opaque"`
	for range javaMaximumTemplateDepth + 2 {
		nested = `STR."\{` + nested + `}"`
	}

	t.Run("same-line-code", func(t *testing.T) {
		t.Parallel()

		source := "class C { Object value = " + nested +
			"; void recovered() { target(); } }"
		javaAssertConcreteSyntax(t, source)
		analysis := analyzeJavaSource(source, 1)
		masked := maskJavaSearchSource(source, analysis.lexed.input, true, true)
		if !strings.Contains(masked, "void recovered() { target(); }") {
			t.Fatalf("deep balanced template masked same-line code:\n%s", masked)
		}
		if strings.Contains(masked, "opaque") {
			t.Fatalf("deep balanced template leaked opaque tail:\n%s", masked)
		}
		if got := javaDefinitionSymbols(analysis.definitions); !slices.Contains(got, "recovered") {
			t.Fatalf("deep balanced definitions = %#v, missing recovered", got)
		}
	})

	t.Run("outer-text-block-tail", func(t *testing.T) {
		t.Parallel()

		source := "class C {\n  Object value = STR.\"\"\"\n    \\{" + nested +
			"}\n    literalTarget\n    \"\"\";\n}"
		javaAssertConcreteSyntax(t, source)
		analysis := analyzeJavaSource(source, strings.Count(source, "\n")+1)
		masked := maskJavaSearchSource(source, analysis.lexed.input, true, true)
		if strings.Contains(masked, "literalTarget") || strings.Contains(masked, "opaque") {
			t.Fatalf("deep text-block template leaked opaque contents:\n%s", masked)
		}
	})
}

func TestJavaTemplateDepthRecoveryHandlesStructuredExpressions(t *testing.T) {
	t.Parallel()

	nested := `leaf("}", '}', /* } */ () -> { return value; })`
	for index := range javaMaximumTemplateDepth + 8 {
		if index%3 == 0 {
			nested = `STR."literal } \\{ignored} \{` + nested + `} tail"`
			continue
		}
		nested = `STR."head \{({ int x = 1; /* } */ }) == null ? ` +
			nested + ` : fallback()} end"`
	}
	source := `class C { Object value = ` + nested +
		`; void after() { afterTarget(); } }`
	input := newJavaUnicodeInput(source)
	masked := maskJavaSearchSource(source, &input, true, true)
	if !strings.Contains(masked, `void after() { afterTarget(); }`) {
		t.Fatalf("deep structured template swallowed valid tail:\n%s", masked)
	}
	for _, hidden := range []string{`literal`, `ignored`, ` tail`, ` end`} {
		if strings.Contains(masked, hidden) {
			t.Fatalf("deep structured template leaked %q:\n%s", hidden, masked)
		}
	}
}

func TestJavaTemplateDepthRecoveryHandlesNestedTextBlockAndLineComment(t *testing.T) {
	t.Parallel()

	nested := "P.\"\"\"\n  deepLiteral \\{value // } commentBrace\n" +
		"    + other} deepTail\n  \"\"\""
	for range javaMaximumTemplateDepth + 2 {
		nested = `P."\{` + nested + `}"`
	}
	source := `class C { Object value = ` + nested +
		`; void after() { afterTarget(); } }`
	javaAssertConcreteSyntax(t, source)
	input := newJavaUnicodeInput(source)
	masked := maskJavaSearchSource(source, &input, true, true)
	if !strings.Contains(masked, `void after() { afterTarget(); }`) {
		t.Fatalf("deep nested text-block template swallowed valid tail:\n%s", masked)
	}
	for _, hidden := range []string{"deepLiteral", "commentBrace", "deepTail"} {
		if strings.Contains(masked, hidden) {
			t.Fatalf("deep nested text-block template leaked %q:\n%s", hidden, masked)
		}
	}
}

func TestJavaTemplateRecoveryFrameCapFailsConservatively(t *testing.T) {
	t.Parallel()

	nested := `"leaf"`
	for range javaMaximumTemplateRecoveryFrames/2 + 2 {
		nested = `P."\{` + nested + `}"`
	}
	input := newJavaUnicodeInput(nested)
	quote := strings.IndexByte(nested, '"')
	if quote < 0 {
		t.Fatal("fixture has no template quote")
	}
	end, recovered := javaTemplateRecoveryBoundaryEnd(
		&input, quote, len(nested),
	)
	if recovered || end != len(nested) {
		t.Fatalf("over-cap recovery = (%d, %v), want (%d, false)",
			end, recovered, len(nested))
	}
}

func TestJavaTemplateRecoveryRejectsMalformedBoundaries(t *testing.T) {
	t.Parallel()

	for _, source := range []string{
		`"head \{value`,
		"\"unterminated\nnext",
		`"head \{value /* unterminated`,
		`"head \{P."unterminated`,
	} {
		input := newJavaUnicodeInput(source)
		if end, recovered := javaTemplateRecoveryBoundaryEnd(
			&input, 0, len(source),
		); recovered || end < 0 || end > len(source) {
			t.Fatalf("malformed recovery for %q = (%d, %v)",
				source, end, recovered)
		}
	}
}

func TestJava26Unicode17IdentifierTables(t *testing.T) {
	t.Parallel()

	unicode16Letter := string(rune(0x11380))
	unicode17Letter := string(rune(0x10940))
	unicode17Part := string(rune(0x1ACF))
	literal := "class " + unicode16Letter + "Valid { void " +
		unicode17Letter + "method" + unicode17Part + "() {} }"
	if got, want := javaDefinitionSymbols(
		newJavaLanguage().sourceDefinitions(javaTestLines(literal)),
	), []string{
		unicode16Letter + "Valid", unicode17Letter + "method" + unicode17Part,
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("literal Unicode 17 definitions = %#v, want %#v", got, want)
	}

	escaped := `class \u088FValid { void \uD802\uDD40method\u1ACF() {} }`
	if got, want := javaDefinitionSymbols(
		newJavaLanguage().sourceDefinitions(javaTestLines(escaped)),
	), []string{`\u088FValid`, `\uD802\uDD40method\u1ACF`}; !reflect.DeepEqual(got, want) {
		t.Fatalf("escaped Unicode 17 definitions = %#v, want %#v", got, want)
	}

	// Other_ID_Start belongs to Unicode identifiers, not Java identifiers.
	if javaIdentifierStartRune('\u2118') || javaIdentifierStartRune('\u309B') {
		t.Fatal("Unicode Other_ID_Start character accepted as Java identifier start")
	}
	for _, value := range []rune{0x088F, 0x20C1, 0x10940, 0x323B0} {
		if !javaIdentifierStartRune(value) || !javaIdentifierContinueRune(value) {
			t.Errorf("Unicode 17 identifier-start rune U+%04X was rejected", value)
		}
	}
	for _, value := range []rune{0x1ACF, 0x11B60, 0x11DE9} {
		if javaIdentifierStartRune(value) || !javaIdentifierContinueRune(value) {
			t.Errorf("Unicode 17 identifier-part-only rune U+%04X was misclassified", value)
		}
	}
	for name, ranges := range map[string][]javaRuneRange{
		"start": javaUnicode17IdentifierStartRanges,
		"part":  javaUnicode17IdentifierPartRanges,
	} {
		for index, interval := range ranges {
			if interval.first > interval.last || index > 0 &&
				ranges[index-1].last >= interval.first {
				t.Fatalf("%s identifier ranges are not strictly ordered at %#v",
					name, interval)
			}
			if !javaRuneInRanges(interval.first, ranges) ||
				!javaRuneInRanges(interval.last, ranges) {
				t.Fatalf("%s identifier range endpoints are not searchable: %#v",
					name, interval)
			}
		}
	}
}

func TestJavaOccurrenceBoundariesUseTranslatedUnicodeInput(t *testing.T) {
	t.Parallel()

	counter := newJavaLanguage()
	tests := []struct {
		line   string
		symbol string
		want   int
	}{
		{line: `\u0020foo`, symbol: "foo", want: 1},
		{line: `foo\u0042`, symbol: "foo", want: 0},
		{line: `\u0066oo()`, symbol: `\u0066oo`, want: 1},
		{line: `x\u0020foo`, symbol: "foo", want: 1},
		{line: `\uD804\uDF80foo`, symbol: "foo", want: 0},
		{line: `\\u0020foo`, symbol: "foo", want: 0},
	}
	for _, test := range tests {
		if got := counter.countSymbolOccurrences(test.line, test.symbol); got != test.want {
			t.Fatalf("countSymbolOccurrences(%q, %q) = %d, want %d",
				test.line, test.symbol, got, test.want)
		}
	}
}

func TestJavaOccurrenceCountingPreservesOverlappingQualifiedSpellings(t *testing.T) {
	t.Parallel()

	counter := newJavaLanguage()
	for _, test := range []struct {
		name   string
		line   string
		symbol string
	}{
		{name: "literal", line: "a.a.a", symbol: "a.a"},
		{
			name:   "raw Unicode escapes",
			line:   `\u0061.\u0061.\u0061`,
			symbol: `\u0061.\u0061`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := counter.countSymbolOccurrences(test.line, test.symbol); got != 2 {
				t.Fatalf("countSymbolOccurrences(%q, %q) = %d, want 2",
					test.line, test.symbol, got)
			}
		})
	}
}

func TestJavaOccurrenceCountingIsLinearForIdentifierInternalMatches(t *testing.T) {
	t.Parallel()

	const lineBytes = 256 << 10
	line := strings.Repeat("a", lineBytes)
	symbol := strings.Repeat("a", lineBytes/2)
	if got := newJavaLanguage().countSymbolOccurrences(line, symbol); got != 0 {
		t.Fatalf("identifier-internal occurrence count = %d, want 0", got)
	}
}

func TestJavaTranslatedOccurrenceCountingUsesBoundedStorage(t *testing.T) {
	const matches = 64 << 10
	line := `\u0020` + strings.Repeat("x ", matches)
	counter := newJavaLanguage()
	if got := counter.countSymbolOccurrences(line, "x"); got != matches {
		t.Fatalf("dense occurrence count = %d, want %d", got, matches)
	}
	lastCount := 0
	allocations := testing.AllocsPerRun(3, func() {
		lastCount = counter.countSymbolOccurrences(line, "x")
	})
	if lastCount != matches {
		t.Fatalf("dense measured occurrence count = %d, want %d", lastCount, matches)
	}
	if allocations > 8 {
		t.Fatalf("dense occurrence allocations = %.1f, want at most 8", allocations)
	}
}

func BenchmarkJavaTranslatedDenseOccurrenceCounting(b *testing.B) {
	const matches = 256 << 10
	line := `\u0020` + strings.Repeat("x ", matches)
	counter := newJavaLanguage()
	b.ReportAllocs()
	b.SetBytes(int64(len(line)))
	b.ResetTimer()
	for range b.N {
		if got := counter.countSymbolOccurrences(line, "x"); got != matches {
			b.Fatalf("dense occurrence count = %d, want %d", got, matches)
		}
	}
}

func BenchmarkJavaIdentifierInternalOccurrenceScaling(b *testing.B) {
	benchmark := func(b *testing.B, lineBytes int) {
		b.Helper()
		line := strings.Repeat("a", lineBytes)
		symbol := strings.Repeat("a", lineBytes/2)
		counter := newJavaLanguage()
		b.ReportAllocs()
		b.SetBytes(int64(len(line)))
		b.ResetTimer()
		for range b.N {
			if got := counter.countSymbolOccurrences(line, symbol); got != 0 {
				b.Fatalf("identifier-internal occurrence count = %d, want 0", got)
			}
		}
	}
	b.Run("64KiB", func(b *testing.B) { benchmark(b, 64<<10) })
	b.Run("256KiB", func(b *testing.B) { benchmark(b, 256<<10) })
}

func TestJavaTranslatedCommentsAttachOnlyJavadocsAcrossLogicalWhitespace(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		source         string
		wantScopeStart int
	}{
		{
			name:           "ordinary escaped comment",
			source:         "\\u002f\\u002f ordinary\nclass C {}",
			wantScopeStart: 2,
		},
		{
			name: "escaped Javadoc",
			source: "\\u002f\\u002a\\u002a docs \\u002a\\u002f\n" +
				"class C {}",
			wantScopeStart: 1,
		},
		{
			name: "logical blank line breaks attachment",
			source: "\\u002f\\u002a\\u002a docs \\u002a\\u002f" +
				"\\u000a\\u000a\nclass C {}",
			wantScopeStart: 2,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			definition := javaFirstDefinition(
				t,
				newJavaLanguage().sourceDefinitions(javaTestLines(test.source)),
				"C",
			)
			if definition.scopeStart != test.wantScopeStart {
				t.Fatalf("C scopeStart = %d, want %d; definition=%#v",
					definition.scopeStart, test.wantScopeStart, definition)
			}
		})
	}
}
