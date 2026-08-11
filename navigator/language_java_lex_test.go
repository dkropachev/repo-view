package navigator

import (
	"reflect"
	"strings"
	"testing"
)

type javaQuotedLiteralTestPart struct {
	text       string
	expression bool
}

type javaQuotedLiteralTestSink struct {
	source    string
	parts     []javaQuotedLiteralTestPart
	stopAfter int
}

func (sink *javaQuotedLiteralTestSink) consumeJavaQuotedLiteralPart(
	span javaByteSpan,
	expression bool,
) bool {
	if sink == nil || span.start < 0 || span.end < span.start || span.end > len(sink.source) {
		return false
	}
	sink.parts = append(sink.parts, javaQuotedLiteralTestPart{
		text: sink.source[span.start:span.end], expression: expression,
	})
	return sink.stopAfter <= 0 || len(sink.parts) < sink.stopAfter
}

func TestJavaSimpleQuotedLiteralScannerMatchesStreamingEnd(t *testing.T) {
	t.Parallel()

	tests := []struct {
		source string
		quote  rune
		closed bool
	}{
		{source: `"plain" trailing`, quote: '"', closed: true},
		{source: `"escaped \\" quote" trailing`, quote: '"', closed: true},
		{source: "\"line\nrest", quote: '"'},
		{source: `"unterminated`, quote: '"'},
		{source: "\"\"\"\ntext \\\" quoted\n\"\"\" trailing", quote: '"', closed: true},
		{source: `'x' trailing`, quote: '\'', closed: true},
		{source: `'\\'' trailing`, quote: '\'', closed: true},
	}
	for _, test := range tests {
		input := newJavaUnicodeInput(test.source)
		gotEnd, interpolated, closed := javaSimpleQuotedLiteralEnd(
			&input, 0, len(test.source), test.quote, false,
		)
		if interpolated {
			t.Fatalf("ordinary literal %q reported interpolation", test.source)
		}
		wantEnd := javaStreamQuotedLiteralParts(
			&input, 0, len(test.source), test.quote, false, nil,
		)
		if gotEnd != wantEnd {
			t.Fatalf("literal end for %q = %d, want %d", test.source, gotEnd, wantEnd)
		}
		if closed != test.closed {
			t.Fatalf("literal closed for %q = %v, want %v", test.source, closed, test.closed)
		}
	}
}

func TestJavaQuotedLiteralStreamPreservesPartOrderAndCoordinates(t *testing.T) {
	t.Parallel()

	const source = `"head \{call("nested")} tail"`
	input := newJavaUnicodeInput(source)
	sink := &javaQuotedLiteralTestSink{source: source}
	if end := javaStreamQuotedLiteralParts(
		&input, 0, len(source), '"', true, sink,
	); end != len(source) {
		t.Fatalf("stream end = %d, want %d", end, len(source))
	}
	want := []javaQuotedLiteralTestPart{
		{text: `"head \{`},
		{text: `call("nested")`, expression: true},
		{text: `} tail"`},
	}
	if !reflect.DeepEqual(sink.parts, want) {
		t.Fatalf("stream parts = %#v, want %#v", sink.parts, want)
	}
}

func TestJavaTemplateCountOnlyStopsBeforeLargeSuffix(t *testing.T) {
	const expressions = 1 << 18
	source := javaDenseTemplateLiteral(expressions)
	input := newJavaUnicodeInput(source)
	processor := []javaToken{
		{text: "STR", value: "STR", identifier: true},
		{text: ".", value: "."},
	}
	firstPartEnd := strings.Index(source, "{") + 1
	if firstPartEnd <= 0 {
		t.Fatal("dense template fixture has no interpolation")
	}

	sink := &javaQuotedLiteralTestSink{source: source, stopAfter: 1}
	if end := javaStreamQuotedLiteralParts(
		&input, 0, len(source), '"', true, sink,
	); end != firstPartEnd {
		t.Fatalf("stopped stream end = %d, want first part end %d", end, firstPartEnd)
	}
	if len(sink.parts) != 1 || sink.parts[0].expression {
		t.Fatalf("stopped stream consumed %#v, want one literal fragment", sink.parts)
	}

	var units int
	allocations := testing.AllocsPerRun(10, func() {
		scanner := javaLexScanner{
			source: source, input: &input, countLimit: 1, countOnly: true,
			recentTokens: processor,
		}
		scanner.scanRange(0, len(source))
		units = scanner.lexicalUnits
		if !scanner.countStopped {
			t.Fatal("count-only scanner did not stop at its configured limit")
		}
	})
	if units != 1 {
		t.Fatalf("count-only lexical units = %d, want 1", units)
	}
	if allocations > 1 {
		t.Fatalf("large-template early-stop allocations = %.0f, want at most 1", allocations)
	}
}

func TestJavaDenseTemplateFragmentStorageStaysBounded(t *testing.T) {
	const expressions = javaMaximumStoredLexicalSpans + 1<<10
	source := `STR.` + javaDenseTemplateLiteral(expressions)
	var lexed javaLexResult
	allocations := testing.AllocsPerRun(1, func() {
		lexed = lexJava(source)
	})
	if got, want := lexed.lexicalUnits, expressions*2+3; got != want {
		t.Fatalf("dense template lexical units = %d, want %d", got, want)
	}
	if len(lexed.stringSpans) == 0 ||
		len(lexed.stringSpans) > javaMaximumStoredLexicalSpans {
		t.Fatalf("dense template retained %d spans, want 1-%d",
			len(lexed.stringSpans), javaMaximumStoredLexicalSpans)
	}
	if cap(lexed.stringSpans) > javaMaximumStoredLexicalSpans*2 {
		t.Fatalf("dense template span capacity = %d, want at most %d",
			cap(lexed.stringSpans), javaMaximumStoredLexicalSpans*2)
	}
	if allocations > 256 {
		t.Fatalf("dense template allocations = %.0f, want at most 256", allocations)
	}
}

func TestJavaTemplateMasksStayOrderedAroundNestedLiterals(t *testing.T) {
	t.Parallel()

	const source = `class C { String value = STR."outer \{call("nested target")} tail"; }`
	lexed := lexJava(source)
	for index := 1; index < len(lexed.stringSpans); index++ {
		if lexed.stringSpans[index].start < lexed.stringSpans[index-1].end {
			t.Fatalf("string masks are not ordered: %#v", lexed.stringSpans)
		}
	}
	masked := maskJavaSource(source, lexed.stringSpans)
	if !strings.Contains(masked, "call(") || strings.Contains(masked, "nested target") ||
		strings.Contains(masked, "outer") || strings.Contains(masked, "tail") {
		t.Fatalf("template mask lost executable code or leaked literal text: %q", masked)
	}
}

func TestJavaNestedTemplateQuotedLiteralMatchesConcreteAndLexicalAuthority(t *testing.T) {
	t.Parallel()

	const source = `class C { String s = STR."outer \{STR."inner \{call("}")} tail"} end"; }`
	plain := analyzeJavaSource(source, 1)
	if plain.tree == nil || len(plain.recoverySpans) != 0 {
		t.Fatalf("plain fixture is not clean concrete syntax: tree=%v recovery=%#v",
			plain.tree != nil, plain.recoverySpans)
	}

	escapedSource := strings.Replace(source, "class", `cl\u0061ss`, 1)
	escaped := analyzeJavaSource(escapedSource, 1)
	if !escaped.lexed.translatedEscapes || escaped.tree != nil {
		t.Fatalf("escaped fixture did not force lexical authority: escapes=%v tree=%v",
			escaped.lexed.translatedEscapes, escaped.tree != nil)
	}
	if got, want := javaDefinitionSymbols(escaped.definitions),
		javaDefinitionSymbols(plain.definitions); !reflect.DeepEqual(got, want) {
		t.Fatalf("lexical definitions = %#v, want concrete %#v", got, want)
	}
	if !reflect.DeepEqual(escaped.scopes, plain.scopes) {
		t.Fatalf("lexical scopes = %#v, want concrete %#v", escaped.scopes, plain.scopes)
	}

	for _, fixture := range []struct {
		name     string
		source   string
		analysis *javaSourceAnalysis
	}{
		{name: "concrete", source: source, analysis: plain},
		{name: "lexical", source: escapedSource, analysis: escaped},
	} {
		masked := maskJavaSource(fixture.source, fixture.analysis.stringSpans)
		if strings.Count(masked, "call") != 1 {
			t.Fatalf("%s masks lost nested executable call: %q", fixture.name, masked)
		}
		for _, literal := range []string{"outer", "inner", "tail", " end"} {
			if strings.Contains(masked, literal) {
				t.Fatalf("%s masks leaked literal %q: %q", fixture.name, literal, masked)
			}
		}
	}
}

func TestJavaChainedTemplatesMatchConcreteAndLexicalAuthority(t *testing.T) {
	t.Parallel()

	const source = `class C {
    Object direct = P."FIRST_LITERAL \{firstExec()}"."SECOND_LITERAL \{secondExec()}";
    Object nested = STR."OUTER_LITERAL \{P."INNER_LITERAL \{innerExec()}"."CHAIN_LITERAL \{chainExec()}"} TAIL_LITERAL";
    Object plain = P."PLAIN_LITERAL"."FINAL_LITERAL \{plainExec()}";
    Object brace = STR."BRACE_OUTER \{new Processor() {}."BRACE_LITERAL \{braceExec()}"} BRACE_TAIL";
}`
	plain := analyzeJavaSource(source, strings.Count(source, "\n")+1)
	if plain.tree == nil || len(plain.recoverySpans) != 0 {
		t.Fatalf("plain fixture is not clean concrete syntax: tree=%v recovery=%#v",
			plain.tree != nil, plain.recoverySpans)
	}

	escapedSource := strings.Replace(source, "class", `cl\u0061ss`, 1)
	escaped := analyzeJavaSource(escapedSource, strings.Count(escapedSource, "\n")+1)
	if !escaped.lexed.translatedEscapes || escaped.tree != nil {
		t.Fatalf("escaped fixture did not force lexical authority: escapes=%v tree=%v",
			escaped.lexed.translatedEscapes, escaped.tree != nil)
	}
	if got, want := javaDefinitionSymbols(escaped.definitions),
		javaDefinitionSymbols(plain.definitions); !reflect.DeepEqual(got, want) {
		t.Fatalf("lexical definitions = %#v, want concrete %#v", got, want)
	}
	if !reflect.DeepEqual(escaped.scopes, plain.scopes) {
		t.Fatalf("lexical scopes = %#v, want concrete %#v", escaped.scopes, plain.scopes)
	}

	for _, fixture := range []struct {
		name     string
		source   string
		analysis *javaSourceAnalysis
	}{
		{name: "concrete", source: source, analysis: plain},
		{name: "lexical", source: escapedSource, analysis: escaped},
	} {
		masked := maskJavaSource(fixture.source, fixture.analysis.stringSpans)
		for _, executable := range []string{
			"firstExec", "secondExec", "innerExec", "chainExec", "plainExec", "braceExec",
		} {
			if strings.Count(masked, executable) != 1 {
				t.Fatalf("%s masks lost chained executable %q: %q",
					fixture.name, executable, masked)
			}
		}
		for _, literal := range []string{
			"FIRST_LITERAL", "SECOND_LITERAL", "OUTER_LITERAL", "INNER_LITERAL",
			"CHAIN_LITERAL", "TAIL_LITERAL", "PLAIN_LITERAL", "FINAL_LITERAL",
			"BRACE_OUTER", "BRACE_LITERAL", "BRACE_TAIL",
		} {
			if strings.Contains(masked, literal) {
				t.Fatalf("%s masks leaked chained literal %q: %q",
					fixture.name, literal, masked)
			}
		}
	}
}

type javaLexicalStreamTestEvent struct {
	kind javaLexicalStreamEventKind
	text string
}

func TestJavaLexicalEventStreamOrdersTokensAndOpaqueFragments(t *testing.T) {
	t.Parallel()

	const source = `left /* comment */ right "str" 'c' """
text
""" STR."head \{call("arg")} tail" done`
	events := make([]javaLexicalStreamTestEvent, 0, 16)
	if complete := streamJavaLexicalEvents(source, func(event javaLexicalStreamEvent) bool {
		switch event.kind {
		case javaLexicalStreamToken:
			if event.token.start < 0 || event.token.end <= event.token.start ||
				event.token.end > len(source) {
				t.Fatalf("invalid token coordinates: %#v", event.token)
			}
			events = append(events, javaLexicalStreamTestEvent{
				kind: event.kind, text: event.token.value,
			})
		case javaLexicalStreamOpaque:
			if event.span.start < 0 || event.span.end <= event.span.start ||
				event.span.end > len(source) {
				t.Fatalf("invalid opaque coordinates: %#v", event.span)
			}
			events = append(events, javaLexicalStreamTestEvent{
				kind: event.kind, text: source[event.span.start:event.span.end],
			})
		case javaLexicalStreamComment:
			t.Fatalf("default stream unexpectedly emitted comment %#v", event)
		}
		return true
	}); !complete {
		t.Fatal("full event stream reported an early stop")
	}

	want := []javaLexicalStreamTestEvent{
		{kind: javaLexicalStreamToken, text: "left"},
		{kind: javaLexicalStreamToken, text: "right"},
		{kind: javaLexicalStreamOpaque, text: `"str"`},
		{kind: javaLexicalStreamOpaque, text: `'c'`},
		{kind: javaLexicalStreamOpaque, text: "\"\"\"\ntext\n\"\"\""},
		{kind: javaLexicalStreamToken, text: "STR"},
		{kind: javaLexicalStreamToken, text: "."},
		{kind: javaLexicalStreamOpaque, text: `"head \{`},
		{kind: javaLexicalStreamToken, text: "call"},
		{kind: javaLexicalStreamToken, text: "("},
		{kind: javaLexicalStreamOpaque, text: `"arg"`},
		{kind: javaLexicalStreamToken, text: ")"},
		{kind: javaLexicalStreamOpaque, text: `} tail"`},
		{kind: javaLexicalStreamToken, text: "done"},
	}
	if !reflect.DeepEqual(events, want) {
		t.Fatalf("stream events = %#v, want %#v", events, want)
	}
}

func TestJavaLexicalEventStreamPreservesChainedTemplateContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		source     string
		wantTokens []string
		wantCode   []string
	}{
		{
			name:       "interpolated chain",
			source:     `P."ONE \{x}"."TWO \{alpha /* bridge */ . beta}"`,
			wantTokens: []string{"P", ".", "x", ".", "alpha", ".", "beta"},
			wantCode:   []string{"x", "alpha", "beta"},
		},
		{
			name:   "nested in outer interpolation",
			source: `STR."OUTER \{P."ONE \{x}"."TWO \{nestedCall()}"} TAIL"`,
			wantTokens: []string{
				"STR", ".", "P", ".", "x", ".", "nestedCall", "(", ")",
			},
			wantCode: []string{"x", "nestedCall"},
		},
		{
			name:       "non-interpolated first argument",
			source:     `P."PLAIN"."TWO \{plainCall()}"`,
			wantTokens: []string{"P", ".", ".", "plainCall", "(", ")"},
			wantCode:   []string{"plainCall"},
		},
		{
			name:       "text block chain",
			source:     "P.\"\"\"\nPLAIN\n\"\"\".\"\"\"\nTWO \\{textCall()}\n\"\"\"",
			wantTokens: []string{"P", ".", ".", "textCall", "(", ")"},
			wantCode:   []string{"textCall"},
		},
		{
			name:       "anonymous processor terminal",
			source:     `new Processor() {}."TWO \{braceCall()}"`,
			wantTokens: []string{"new", "Processor", "(", ")", "{", "}", ".", "braceCall", "(", ")"},
			wantCode:   []string{"braceCall"},
		},
		{
			name:   "anonymous processor inside outer interpolation",
			source: `STR."OUTER \{new Processor() {}."TWO \{nestedBraceCall()}"} TAIL"`,
			wantTokens: []string{
				"STR", ".", "new", "Processor", "(", ")", "{", "}", ".",
				"nestedBraceCall", "(", ")",
			},
			wantCode: []string{"nestedBraceCall"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			tokens := make([]string, 0, len(test.wantTokens))
			opaque := make([]string, 0)
			if complete := streamJavaLexicalEvents(test.source, func(event javaLexicalStreamEvent) bool {
				switch event.kind {
				case javaLexicalStreamToken:
					tokens = append(tokens, event.token.value)
				case javaLexicalStreamOpaque:
					opaque = append(opaque, test.source[event.span.start:event.span.end])
				case javaLexicalStreamComment:
					t.Fatal("default stream emitted a comment event")
				}
				return true
			}); !complete {
				t.Fatal("chained template stream stopped early")
			}
			if !reflect.DeepEqual(tokens, test.wantTokens) {
				t.Fatalf("stream tokens = %#v, want %#v; opaque=%#v",
					tokens, test.wantTokens, opaque)
			}
			for _, code := range test.wantCode {
				for _, fragment := range opaque {
					if strings.Contains(fragment, code) {
						t.Fatalf("executable %q remained opaque in %#v", code, opaque)
					}
				}
			}
		})
	}
}

func TestJavaUnterminatedLiteralDoesNotCreateTemplateResultContext(t *testing.T) {
	t.Parallel()

	const source = "\"unterminated\n.\"OPAQUE \\{hiddenCall()}\""
	tokens := make([]string, 0, 1)
	opaque := make([]string, 0, 2)
	if complete := streamJavaLexicalEvents(source, func(event javaLexicalStreamEvent) bool {
		switch event.kind {
		case javaLexicalStreamToken:
			tokens = append(tokens, event.token.value)
		case javaLexicalStreamOpaque:
			opaque = append(opaque, source[event.span.start:event.span.end])
		case javaLexicalStreamComment:
			t.Fatalf("default stream unexpectedly emitted comment %#v", event)
		}
		return true
	}); !complete {
		t.Fatal("malformed literal stream stopped early")
	}
	if want := []string{"."}; !reflect.DeepEqual(tokens, want) {
		t.Fatalf("malformed literal tokens = %#v, want %#v", tokens, want)
	}
	if len(opaque) != 2 || !strings.Contains(opaque[1], "hiddenCall") {
		t.Fatalf("unterminated literal exposed chained-looking contents: %#v", opaque)
	}
}

func TestJavaLexicalEventStreamCanStopImmediately(t *testing.T) {
	t.Parallel()

	const source = `one /* trivia */ two "opaque" three four`
	count := 0
	complete := streamJavaLexicalEvents(source, func(javaLexicalStreamEvent) bool {
		count++
		return count < 3
	})
	if complete || count != 3 {
		t.Fatalf("stopped stream = (complete=%v, events=%d), want (false, 3)",
			complete, count)
	}
}

func TestJavaLexicalEventStreamIncludesMiddleBeyondRetainedTokenLimit(t *testing.T) {
	const sideTokens = javaMaximumStoredLexicalTokens
	source := strings.Repeat("a ", sideTokens) + "middle " +
		strings.Repeat("z ", sideTokens)
	tokenCount := 0
	middleIndex := 0
	if complete := streamJavaLexicalEvents(source, func(event javaLexicalStreamEvent) bool {
		if event.kind != javaLexicalStreamToken {
			t.Fatalf("token-only fixture emitted %#v", event)
		}
		tokenCount++
		if event.token.value == "middle" {
			middleIndex = tokenCount
		}
		return true
	}); !complete {
		t.Fatal("large token stream stopped early")
	}
	if got, want := tokenCount, sideTokens*2+1; got != want {
		t.Fatalf("streamed tokens = %d, want %d", got, want)
	}
	if got, want := middleIndex, sideTokens+1; got != want {
		t.Fatalf("middle token index = %d, want %d", got, want)
	}
}

func TestJavaLexicalEventStreamDoesNotRetainOpaqueSpanOverflow(t *testing.T) {
	const opaqueCount = javaMaximumStoredLexicalSpans + 2
	source := strings.Repeat(`"x"+`, opaqueCount) + "end"

	gotOpaque := 0
	gotTokens := 0
	allocations := testing.AllocsPerRun(1, func() {
		gotOpaque = 0
		gotTokens = 0
		if complete := streamJavaLexicalEvents(source, func(event javaLexicalStreamEvent) bool {
			switch event.kind {
			case javaLexicalStreamOpaque:
				gotOpaque++
			case javaLexicalStreamToken:
				gotTokens++
			case javaLexicalStreamComment:
				t.Fatalf("default stream unexpectedly emitted comment %#v", event)
			}
			return true
		}); !complete {
			t.Fatal("large opaque stream stopped early")
		}
	})
	if gotOpaque != opaqueCount || gotTokens != opaqueCount+1 {
		t.Fatalf("streamed (opaque=%d, tokens=%d), want (%d, %d)",
			gotOpaque, gotTokens, opaqueCount, opaqueCount+1)
	}
	if allocations > 16 {
		t.Fatalf("large opaque stream allocated %.0f objects, want at most 16", allocations)
	}
}

func TestJavaLexicalEventStreamMakesEveryJLSNumericFormAtomic(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		source     string
		wantValue  string
		wantTokens []string
	}{
		{name: "decimal zero", source: "0", wantValue: "0"},
		{name: "decimal integer", source: "1_234L", wantValue: "1_234L"},
		{name: "octal integer", source: "0_7l", wantValue: "0_7l"},
		{name: "hex integer", source: "0xCAFE_BABEL", wantValue: "0xCAFE_BABEL"},
		{name: "binary integer", source: "0b1010_0101L", wantValue: "0b1010_0101L"},
		{name: "decimal point", source: "1.", wantValue: "1."},
		{name: "leading point", source: ".5", wantValue: ".5"},
		{name: "decimal exponent", source: "1e-3", wantValue: "1e-3"},
		{name: "decimal separators", source: "1_2.3_4E+5_6D", wantValue: "1_2.3_4E+5_6D"},
		{name: "suffix-only float", source: "08f", wantValue: "08f"},
		{name: "hex float", source: "0x1.deadp0", wantValue: "0x1.deadp0"},
		{name: "hex fraction only", source: "0X.8P-1F", wantValue: "0X.8P-1F"},
		{name: "hex exponent", source: "0x1p4D", wantValue: "0x1p4D"},
		{name: "hex trailing point", source: "0x1.p0", wantValue: "0x1.p0"},
		{
			name:      "translated coordinates",
			source:    `\u0030x1\u002edeadp\u002d0F`,
			wantValue: "0x1.deadp-0F",
		},
		{
			name:       "hex member dot remains separate",
			source:     "0x1.foo",
			wantTokens: []string{"0x1", ".", "foo"},
		},
		{
			name:       "incomplete hex float does not consume name",
			source:     "0x1.dead",
			wantTokens: []string{"0x1", ".", "dead"},
		},
		{
			name:       "complete hex float keeps following member",
			source:     "0x1.deadp0.foo",
			wantTokens: []string{"0x1.deadp0", ".", "foo"},
		},
		{
			name:       "decimal exponent keeps following member",
			source:     "1e10.foo",
			wantTokens: []string{"1e10", ".", "foo"},
		},
		{
			name:       "following identifier remains separate",
			source:     "123name",
			wantTokens: []string{"123", "name"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			tokens := make([]javaToken, 0, 3)
			if !streamJavaLexicalEvents(test.source, func(event javaLexicalStreamEvent) bool {
				if event.kind != javaLexicalStreamToken {
					t.Fatalf("numeric fixture emitted non-token event %#v", event)
				}
				tokens = append(tokens, event.token)
				return true
			}) {
				t.Fatal("numeric stream stopped early")
			}
			if test.wantValue != "" {
				if len(tokens) != 1 || tokens[0].identifier ||
					tokens[0].text != test.source || tokens[0].value != test.wantValue ||
					tokens[0].start != 0 || tokens[0].end != len(test.source) {
					t.Fatalf("numeric token = %#v, want one atomic raw %q logical %q",
						tokens, test.source, test.wantValue)
				}
				return
			}
			got := make([]string, 0, len(tokens))
			for _, token := range tokens {
				got = append(got, token.value)
			}
			if !reflect.DeepEqual(got, test.wantTokens) {
				t.Fatalf("stream tokens = %#v, want %#v", got, test.wantTokens)
			}
			if len(tokens) == 0 || tokens[0].identifier {
				t.Fatalf("leading numeric token is not atomic/non-identifier: %#v", tokens)
			}
		})
	}
}

func TestJavaLexicalEventStreamUsesMaximalMunchOperators(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		source string
		want   string
	}{
		{source: ">>>=", want: ">>>="},
		{source: ">>>", want: ">>>"},
		{source: ">>=", want: ">>="},
		{source: ">>", want: ">>"},
		{source: ">=", want: ">="},
		{source: "<<=", want: "<<="},
		{source: "<<", want: "<<"},
		{source: "<=", want: "<="},
		{source: "...", want: "..."},
		{source: "->", want: "->"},
		{source: "--", want: "--"},
		{source: "-=", want: "-="},
		{source: "::", want: "::"},
		{source: "==", want: "=="},
		{source: "!=", want: "!="},
		{source: "&&", want: "&&"},
		{source: "&=", want: "&="},
		{source: "||", want: "||"},
		{source: "|=", want: "|="},
		{source: "++", want: "++"},
		{source: "+=", want: "+="},
		{source: "*=", want: "*="},
		{source: "/=", want: "/="},
		{source: "%=", want: "%="},
		{source: "^=", want: "^="},
		{source: `\u003e\u003e\u003e\u003d`, want: ">>>="},
	} {
		t.Run(test.source, func(t *testing.T) {
			t.Parallel()
			var tokens []javaToken
			if !streamJavaLexicalEvents(test.source, func(event javaLexicalStreamEvent) bool {
				if event.kind != javaLexicalStreamToken {
					t.Fatalf("operator emitted non-token event %#v", event)
				}
				tokens = append(tokens, event.token)
				return true
			}) {
				t.Fatal("operator stream stopped early")
			}
			if len(tokens) != 1 || tokens[0].text != test.want ||
				tokens[0].value != test.want || tokens[0].start != 0 ||
				tokens[0].end != len(test.source) {
				t.Fatalf("operator tokens = %#v, want one %q token", tokens, test.want)
			}
		})
	}
}

func TestJavaLexicalEventStreamReusesInputAndOptionallyReportsComments(t *testing.T) {
	t.Parallel()

	const source = `\u0061 /* block */ . // line
\u0062 "opaque"`
	input := newJavaUnicodeInput(source)
	type streamEvent struct {
		kind javaLexicalStreamEventKind
		text string
	}
	collect := func(options javaLexicalStreamOptions) []streamEvent {
		events := make([]streamEvent, 0, 8)
		if !streamJavaLexicalEventsFromInput(
			&input, options,
			func(event javaLexicalStreamEvent) bool {
				var text string
				switch event.kind {
				case javaLexicalStreamToken:
					text = event.token.value
				case javaLexicalStreamOpaque, javaLexicalStreamComment:
					text = source[event.span.start:event.span.end]
				}
				events = append(events, streamEvent{kind: event.kind, text: text})
				return true
			},
		) {
			t.Fatal("prepared-input stream stopped early")
		}
		return events
	}

	withoutComments := collect(javaLexicalStreamOptions{})
	wantWithout := []streamEvent{
		{kind: javaLexicalStreamToken, text: "a"},
		{kind: javaLexicalStreamToken, text: "."},
		{kind: javaLexicalStreamToken, text: "b"},
		{kind: javaLexicalStreamOpaque, text: `"opaque"`},
	}
	if !reflect.DeepEqual(withoutComments, wantWithout) {
		t.Fatalf("default prepared stream = %#v, want %#v", withoutComments, wantWithout)
	}

	withComments := collect(javaLexicalStreamOptions{comments: true})
	wantWith := []streamEvent{
		{kind: javaLexicalStreamToken, text: "a"},
		{kind: javaLexicalStreamComment, text: "/* block */"},
		{kind: javaLexicalStreamToken, text: "."},
		{kind: javaLexicalStreamComment, text: "// line"},
		{kind: javaLexicalStreamToken, text: "b"},
		{kind: javaLexicalStreamOpaque, text: `"opaque"`},
	}
	if !reflect.DeepEqual(withComments, wantWith) {
		t.Fatalf("comment-aware prepared stream = %#v, want %#v", withComments, wantWith)
	}
	if streamJavaLexicalEventsFromInput(nil, javaLexicalStreamOptions{}, nil) {
		t.Fatal("nil prepared input reported a complete stream")
	}
}

func TestJavaLexicalEventStreamReportsCommentsBeyondStoredSpanLimit(t *testing.T) {
	const commentCount = javaMaximumStoredLexicalSpans + 2
	source := strings.Repeat("/* exact */a ", commentCount)
	input := newJavaUnicodeInput(source)
	comments := 0
	if !streamJavaLexicalEventsFromInput(
		&input, javaLexicalStreamOptions{comments: true},
		func(event javaLexicalStreamEvent) bool {
			if event.kind == javaLexicalStreamComment {
				comments++
				if got := source[event.span.start:event.span.end]; got != "/* exact */" {
					t.Fatalf("comment event = %q, want exact block comment", got)
				}
			}
			return true
		},
	) {
		t.Fatal("large comment stream stopped early")
	}
	if comments != commentCount {
		t.Fatalf("streamed comments = %d, want %d", comments, commentCount)
	}
}

func TestJavaCommentSpansRemainDistinctAcrossJavadocGaps(t *testing.T) {
	t.Parallel()

	const source = "/** first */ \n// ordinary\n/** second */\nclass C {}"
	lexed := lexJava(source)
	if len(lexed.commentSpans) != 3 {
		t.Fatalf("comment spans = %#v, want three distinct comment groups", lexed.commentSpans)
	}
}

func TestJavaTranslatedLinePrefixWhitespace(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		source string
		want   bool
	}{
		{name: "start of input", source: `/// docs`, want: true},
		{name: "raw horizontal whitespace", source: " \t\f/// docs", want: true},
		{name: "trailing source comment", source: `code; /// docs`, want: false},
		{name: "raw newline", source: "code;\n\t/// docs", want: true},
		{name: "escaped newline", source: "code;\\u000a\t/// docs", want: true},
		{name: "escaped CR and whitespace", source: `code;\u000d\u0009/// docs`, want: true},
		{name: "code after escaped newline", source: `code;\u000a more /// docs`, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			offset := strings.Index(test.source, "///")
			if offset < 0 {
				t.Fatalf("fixture does not contain Markdown comment: %q", test.source)
			}
			input := newJavaUnicodeInput(test.source)
			if got := javaTranslatedLinePrefixWhitespace(&input, offset); got != test.want {
				t.Fatalf("line prefix whitespace for %q = %v, want %v",
					test.source, got, test.want)
			}
		})
	}
}

func TestJavaMarkdownDocumentationCommentsAttachToDeclarations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		source         string
		wantScopeStart int
	}{
		{
			name:           "consecutive group",
			source:         "/// first\n/// second\nclass C {}",
			wantScopeStart: 1,
		},
		{
			name:           "closer group supersedes stale group",
			source:         "/// stale\n// ordinary\n/// closest\nclass C {}",
			wantScopeStart: 3,
		},
		{
			name:           "ordinary comment remains transparent after docs",
			source:         "/// docs\n// ordinary\nclass C {}",
			wantScopeStart: 1,
		},
		{
			name:           "blank line leaves only closest group",
			source:         "/// stale\n\n/// closest\nclass C {}",
			wantScopeStart: 3,
		},
		{
			name:           "four slashes are markdown docs",
			source:         "//// docs\nclass C {}",
			wantScopeStart: 1,
		},
		{
			name:           "split slashes are ordinary comment",
			source:         "// / ordinary\nclass C {}",
			wantScopeStart: 2,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			javaAssertConcreteSyntax(t, test.source)
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

func TestJavaMarkdownDocumentationMustBeginAfterHorizontalWhitespace(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		source         string
		wantScopeStart int
	}{
		{
			name: "trailing triple slash is ordinary",
			source: "class Outer {\n" +
				"    int value; /// trailing\n" +
				"    class Inner {}\n}",
			wantScopeStart: 3,
		},
		{
			name: "tabs and form feed are allowed",
			source: "class Outer {\n" +
				"\t\f/// docs\n" +
				"    class Inner {}\n}",
			wantScopeStart: 2,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			javaAssertConcreteSyntax(t, test.source)
			definition := javaFirstDefinition(
				t,
				newJavaLanguage().sourceDefinitions(javaTestLines(test.source)),
				"Inner",
			)
			if definition.scopeStart != test.wantScopeStart {
				t.Fatalf("Inner scopeStart = %d, want %d; definition=%#v",
					definition.scopeStart, test.wantScopeStart, definition)
			}
		})
	}
}

func TestJavaDenseOpaqueSpanStorageStaysBounded(t *testing.T) {
	const sourceBytes = 8 << 20
	const unit = `""/**/`
	repetitions := sourceBytes / len(unit)
	source := strings.Repeat(unit, repetitions)

	var analysis *javaSourceAnalysis
	allocations := testing.AllocsPerRun(1, func() {
		analysis = analyzeJavaSource(source, 1)
	})
	if analysis == nil {
		t.Fatal("dense analysis is nil")
	}
	if got, want := analysis.lexed.lexicalUnits, repetitions*2; got != want {
		t.Fatalf("dense lexical units = %d, want %d", got, want)
	}
	if analysis.tree != nil {
		t.Fatal("dense fallback fixture unexpectedly retained a concrete tree")
	}
	for name, spans := range map[string][]javaByteSpan{
		"comments": analysis.commentSpans,
		"strings":  analysis.stringSpans,
	} {
		if len(spans) > javaMaximumStoredLexicalSpans {
			t.Fatalf("dense %s retained %d spans, want at most %d",
				name, len(spans), javaMaximumStoredLexicalSpans)
		}
		if cap(spans) > javaMaximumStoredLexicalSpans*2 {
			t.Fatalf("dense %s retained capacity %d, want at most %d",
				name, cap(spans), javaMaximumStoredLexicalSpans*2)
		}
	}
	if len(analysis.commentSpans) == 0 || len(analysis.stringSpans) == 0 ||
		&analysis.commentSpans[0] != &analysis.lexed.commentSpans[0] ||
		&analysis.stringSpans[0] != &analysis.lexed.stringSpans[0] {
		t.Fatal("tree-free final masks did not reuse lexical span storage")
	}
	if allocations > 256 {
		t.Fatalf("dense analysis allocations = %.0f, want at most 256", allocations)
	}
}

func TestJavaDenseProcessorLiteralsAvoidPerLiteralAllocations(t *testing.T) {
	const sourceBytes = 1 << 20
	tests := []struct {
		name string
		unit string
	}{
		{name: "ordinary processor string", unit: `STR."" `},
		{name: "processor text block", unit: "STR.\"\"\"\ntext\n\"\"\" "},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := strings.Repeat(test.unit, sourceBytes/len(test.unit))
			var lexed javaLexResult
			allocations := testing.AllocsPerRun(1, func() {
				lexed = lexJava(source)
			})
			if len(lexed.stringSpans) == 0 ||
				len(lexed.stringSpans) > javaMaximumStoredLexicalSpans {
				t.Fatalf("dense processor retained %d string spans", len(lexed.stringSpans))
			}
			if allocations > 256 {
				t.Fatalf("dense processor allocations = %.0f, want at most 256", allocations)
			}
		})
	}
}

func BenchmarkJavaDenseOpaqueSpanAnalysis(b *testing.B) {
	const sourceBytes = 8 << 20
	const unit = `""/**/`
	source := strings.Repeat(unit, sourceBytes/len(unit))
	b.ReportAllocs()
	b.SetBytes(int64(len(source)))
	b.ResetTimer()
	for range b.N {
		analysis := analyzeJavaSource(source, 1)
		if len(analysis.commentSpans) == 0 || len(analysis.stringSpans) == 0 {
			b.Fatal("dense analysis lost opaque masks")
		}
	}
}

func BenchmarkJavaDenseProcessorLiteralAnalysis(b *testing.B) {
	const sourceBytes = 8 << 20
	const unit = `STR."" `
	source := strings.Repeat(unit, sourceBytes/len(unit))
	b.ReportAllocs()
	b.SetBytes(int64(len(source)))
	b.ResetTimer()
	for range b.N {
		analysis := analyzeJavaSource(source, 1)
		if len(analysis.stringSpans) == 0 {
			b.Fatal("dense processor analysis lost string masks")
		}
	}
}

func BenchmarkJavaTemplateCountEarlyStop(b *testing.B) {
	const expressions = 1 << 18
	source := javaDenseTemplateLiteral(expressions)
	input := newJavaUnicodeInput(source)
	processor := []javaToken{
		{text: "STR", value: "STR", identifier: true},
		{text: ".", value: "."},
	}
	b.ReportAllocs()
	b.ResetTimer()
	b.ReportMetric(float64(len(source)), "source-B/op")
	for range b.N {
		scanner := javaLexScanner{
			source: source, input: &input, countLimit: 1, countOnly: true,
			recentTokens: processor,
		}
		scanner.scanRange(0, len(source))
		if scanner.lexicalUnits != 1 || !scanner.countStopped {
			b.Fatal("count-only template scan did not stop after its first fragment")
		}
	}
}

func BenchmarkJavaDenseTemplateStreaming(b *testing.B) {
	const expressions = javaMaximumStoredLexicalSpans + 1<<10
	source := `STR.` + javaDenseTemplateLiteral(expressions)
	b.ReportAllocs()
	b.SetBytes(int64(len(source)))
	b.ResetTimer()
	for range b.N {
		lexed := lexJava(source)
		if len(lexed.stringSpans) == 0 ||
			len(lexed.stringSpans) > javaMaximumStoredLexicalSpans {
			b.Fatal("dense template scan lost its bounded masks")
		}
	}
}

func javaDenseTemplateLiteral(expressions int) string {
	var source strings.Builder
	source.Grow(expressions*5 + 2)
	source.WriteByte('"')
	for range expressions {
		source.WriteString(`\{x}`)
	}
	source.WriteByte('"')
	return source.String()
}
