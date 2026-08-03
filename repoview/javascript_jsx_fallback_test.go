package repoview

import (
	"slices"
	"strings"
	"testing"
)

func TestJavaScriptFallbackJSXCandidateMatchesConcreteMasks(t *testing.T) {
	t.Parallel()

	complexSource := `<>
  <UI.Panel data-id="> {" disabled {...spread} action={() => ({
    close: "}",
    regex: /[}]/,
    template: ` + "`raw ${fn({nested: \"}\"})}`" + `,
    child: <Row>{value < limit ? value : limit}</Row>,
  })}>
    raw // text /* still text */ function Fake() {}
    {/* real comment */ render({nested: <Child title={"x"}>{target}</Child>})}
  </UI.Panel>
</>`

	for _, source := range []string{
		`<target title="target" target target={target}>target {target}<target>{target}</target></target>`,
		complexSource,
		`<Host slot=<Child>hidden {value}</Child> after />`,
	} {
		t.Run(source[:min(len(source), 24)], func(t *testing.T) {
			t.Parallel()

			tree, ok := parseJavaScriptSyntax(source)
			if !ok || tree == nil || len(javascriptSyntaxErrorSpans(tree, len(source))) != 0 {
				t.Fatalf("fixture did not produce valid concrete JSX: %q", source)
			}
			candidate, ok := javascriptFallbackJSXCandidateAt(source, 0)
			if !ok || candidate.end != len(source) {
				t.Fatalf("candidate = %#v, %v; want end %d", candidate, ok, len(source))
			}
			want := javascriptConcreteJSXOnlyStringSpans(source, tree)
			if !slices.Equal(candidate.publicStringSpans, want) {
				t.Fatalf(
					"public JSX spans = %#v, want %#v\ngot:\n%s\nwant:\n%s",
					candidate.publicStringSpans,
					want,
					maskJavaScriptSource(source, candidate.publicStringSpans),
					maskJavaScriptSource(source, want),
				)
			}
		})
	}
}

func TestJavaScriptFallbackJSXCandidateSeparatesSearchAndLexicalMasks(t *testing.T) {
	t.Parallel()

	const source = `<Panel title="fake" action={() => render(target)}>
  hidden function Fake() {}
  <Child>{require("dependency")}</Child>
</Panel>`
	candidate, ok := javascriptFallbackJSXCandidateAt(source, 0)
	if !ok || candidate.end != len(source) {
		t.Fatalf("candidate = %#v, %v; want complete", candidate, ok)
	}

	public := maskJavaScriptSource(source, candidate.publicStringSpans)
	for _, retained := range []string{"<Panel", "</Panel>", "<Child>", "target", "require"} {
		if !strings.Contains(public, retained) {
			t.Fatalf("public mask removed %q:\n%s", retained, public)
		}
	}
	for _, removed := range []string{"title", "fake", "action", "hidden", "Fake"} {
		if strings.Contains(public, removed) {
			t.Fatalf("public mask retained %q:\n%s", removed, public)
		}
	}

	semantic := maskJavaScriptSource(source, candidate.lexicalSkipSpans)
	for _, retained := range []string{"() => render(target)", `require("dependency")`} {
		if !strings.Contains(semantic, retained) {
			t.Fatalf("semantic mask removed executable %q:\n%s", retained, semantic)
		}
	}
	for _, removed := range []string{"Panel", "Child", "title", "action", "hidden", "Fake"} {
		if strings.Contains(semantic, removed) {
			t.Fatalf("semantic mask retained JSX-only %q:\n%s", removed, semantic)
		}
	}
	if len(candidate.lexicalValueMarkers) != 2 {
		t.Fatalf("value markers = %#v, want outer and child", candidate.lexicalValueMarkers)
	}
	for _, marker := range candidate.lexicalValueMarkers {
		if marker.end != marker.start+1 || source[marker.start:marker.end] != ">" ||
			javascriptByteRangeExcluded(marker.start, marker.end, candidate.lexicalSkipSpans) {
			t.Fatalf("invalid or skipped value marker %#v", marker)
		}
	}
}

func TestJavaScriptFallbackJSXCandidateHandlesJavaScriptLexicalContexts(t *testing.T) {
	t.Parallel()

	const source = `<Root value={{
  quote: "}",
  regex: /[}\\/]/giu,
  division: total / divisor,
  template: ` + "`before ${call({close: \"}\", nested: `inner ${value}`})} after`" + `,
  comment: /* } </Root> */ value,
  line: (() => { // }
    if (ready) /}/.test(input);
    return <Nested>{value}</Nested>;
  })(),
}}>{target}</Root>`
	candidate, ok := javascriptFallbackJSXCandidateAt(source, 0)
	if !ok || candidate.end != len(source) {
		t.Fatalf("candidate = %#v, %v; want complete", candidate, ok)
	}
	masked := maskJavaScriptSource(source, candidate.publicStringSpans)
	for _, retained := range []string{
		`quote: "}"`, `regex: /[}\\/]/giu`, "total / divisor", "call({close:",
		"/}/.test(input)", "<Nested>", "{value}", "{target}",
	} {
		if !strings.Contains(masked, retained) {
			t.Fatalf("JSX mask removed executable %q:\n%s", retained, masked)
		}
	}
}

func TestJavaScriptFallbackJSXCandidateAcceptsCommentOnlyAttributeExpression(t *testing.T) {
	t.Parallel()

	const source = `<A value={/* comment */} />`
	tree, ok := parseJavaScriptSyntax(source)
	if !ok || tree == nil || len(javascriptSyntaxErrorSpans(tree, len(source))) != 0 {
		t.Fatal("fixture did not produce valid concrete JSX")
	}
	candidate, ok := javascriptFallbackJSXCandidateAt(source, 0)
	if !ok || candidate.end != len(source) {
		t.Fatalf("candidate = %#v, %v; want complete", candidate, ok)
	}
	if want := javascriptConcreteJSXOnlyStringSpans(source, tree); !slices.Equal(
		candidate.publicStringSpans, want,
	) {
		t.Fatalf("public JSX spans = %#v, want %#v", candidate.publicStringSpans, want)
	}
	commentStart := strings.Index(source, "/* comment */")
	fallback := scanJavaScriptFallback(source)
	if commentStart < 0 || !javascriptByteRangeExcluded(
		commentStart, commentStart+len("/* comment */"), fallback.comments,
	) {
		t.Fatalf("fallback comments = %#v, want embedded JSX comment", fallback.comments)
	}
}

func TestJavaScriptFallbackJSXCandidateRetainsNestedAttributeExpressions(t *testing.T) {
	t.Parallel()

	for _, source := range []string{
		`<Host slot=<Child>{require("dep")}</Child> />`,
		`<Host slot=<Child outside={require("dep")} /> />`,
	} {
		t.Run(source[:min(len(source), 32)], func(t *testing.T) {
			t.Parallel()

			tree, ok := parseJavaScriptSyntax(source)
			if !ok || tree == nil || len(javascriptSyntaxErrorSpans(tree, len(source))) != 0 {
				t.Fatal("fixture did not produce valid concrete JSX")
			}
			candidate, ok := javascriptFallbackJSXCandidateAt(source, 0)
			if !ok || candidate.end != len(source) {
				t.Fatalf("candidate = %#v, %v; want complete", candidate, ok)
			}
			if want := javascriptConcreteJSXOnlyStringSpans(source, tree); !slices.Equal(
				candidate.publicStringSpans, want,
			) {
				t.Fatalf("public JSX spans = %#v, want %#v", candidate.publicStringSpans, want)
			}

			public := maskJavaScriptSource(source, candidate.publicStringSpans)
			if !strings.Contains(public, "Host") || strings.Contains(public, "Child") ||
				strings.Contains(public, "require") || strings.Contains(public, "slot") {
				t.Fatalf("nested JSX-valued attribute was not publicly opaque:\n%s", public)
			}
			semantic := maskJavaScriptSource(source, candidate.lexicalSkipSpans)
			if !strings.Contains(semantic, `require("dep")`) || strings.Contains(semantic, "Host") ||
				strings.Contains(semantic, "Child") || strings.Contains(semantic, "slot") ||
				strings.Contains(semantic, "outside") {
				t.Fatalf("nested JSX executable body was not retained semantically:\n%s", semantic)
			}

			lexical := javascriptLexicalOnlyForTest("const view = " + source + ";")
			wantImports := []javascriptLineSpan{{start: 1, end: 1}}
			if !slices.Equal(lexical.imports, wantImports) {
				t.Fatalf("fallback imports = %#v, want %#v", lexical.imports, wantImports)
			}
		})
	}
}

func TestJavaScriptFallbackJSXExpressionsMaintainLexicalGoal(t *testing.T) {
	t.Parallel()

	tests := []struct {
		source      string
		wantImports bool
	}{
		{`const view = <A>secret{(() => {} / require("arrow"))}</A> / 2;`, true},
		{`const view = <A>secret{(function() {} / require("function"))}</A> / 2;`, true},
		{`const view = <A>secret{(class {} / require("class"))}</A> / 2;`, true},
		{"const view = <A>secret{(() => { let value\n/}/.test(value); })()}</A>;", false},
		{"const view = <A>secret{(() => { while (ready) { break\n/}/.test(value); } })()}</A>;", false},
	}
	for _, test := range tests {
		source := test.source
		t.Run(source[:min(len(source), 40)], func(t *testing.T) {
			t.Parallel()

			tree, ok := parseJavaScriptSyntax(source)
			concreteClean := ok && tree != nil &&
				len(javascriptSyntaxErrorSpans(tree, len(source))) == 0
			if !concreteClean && !strings.Contains(source, "break\n") {
				t.Fatalf("fixture did not produce clean concrete JSX: %q", source)
			}
			fallback := scanJavaScriptFallback(source)
			if concreteClean {
				concreteComments, concreteStrings := javascriptSyntaxMasks(source, tree)
				if !slices.Equal(fallback.comments, concreteComments) ||
					!slices.Equal(fallback.literals, concreteStrings) {
					t.Fatalf("JSX lexical-goal masks for %q = comments %#v strings %#v; want %#v %#v",
						source, fallback.comments, fallback.literals, concreteComments, concreteStrings)
				}
			}
			if len(fallback.jsxValues) != 1 {
				t.Fatalf("JSX lexical-goal roots for %q = %#v", source, fallback.jsxValues)
			}
			if strings.Contains(maskJavaScriptSource(source, fallback.literals), "secret") {
				t.Fatalf("JSX text remained visible for %q", source)
			}
			lexical := javascriptLexicalOnlyForTest(source)
			wantCount := 0
			if test.wantImports {
				wantCount = 1
			}
			if len(lexical.imports) != wantCount {
				t.Fatalf("JSX lexical-goal imports for %q = %#v", source, lexical.imports)
			}
		})
	}
}

func TestJavaScriptFallbackJSXExpressionsTreatKeywordPropertiesAndCallablesAsValues(t *testing.T) {
	t.Parallel()

	for _, expression := range []string{
		`object.if(value) / require("property") / 2`,
		`object?.switch(value) / require("optional") / 2`,
		`!function() {} / require("function") / 2`,
		`value && class {} / require("class") / 2`,
	} {
		source := "<A>{" + expression + "}</A>"
		candidate, ok := javascriptFallbackJSXCandidateAt(source, 0)
		if !ok || candidate.end != len(source) {
			t.Fatalf("JSX value expression was rejected for %q: %#v, %v", source, candidate, ok)
		}
		if got, want := javascriptLexicalOnlyForTest(source).imports,
			[]javascriptLineSpan{{start: 1, end: 1}}; !slices.Equal(got, want) {
			t.Fatalf("JSX value-expression imports for %q = %#v, want %#v", source, got, want)
		}
	}
}

func TestJavaScriptFallbackJSXCandidateRejectsMalformedInputTransactionally(t *testing.T) {
	t.Parallel()

	for _, source := range []string{
		`<A><B></A></B>`,
		`<A>unterminated`,
		`<A>{unterminated`,
		`<A title="unterminated></A>`,
		"<A>{`unterminated}</A>",
		`<A>{(/* missing close */ value}</A>`,
		`<A>{[value}</A>`,
		`<A p="a\"b"/>`,
		`<A {value}/>`,
		`<A {...}/>`,
		`<A value={}/>`,
	} {
		candidate, ok := javascriptFallbackJSXCandidateAt(source, 0)
		if ok || !javascriptFallbackJSXCandidateEmpty(candidate) {
			t.Fatalf("malformed %q returned %#v, %v", source, candidate, ok)
		}
	}
	for _, source := range []string{`a < b`, `<=`, `<<`, `<!-- legacy`, `</A>`} {
		if candidate, ok := javascriptFallbackJSXCandidateAt(source, strings.Index(source, "<")); ok || !javascriptFallbackJSXCandidateEmpty(candidate) {
			t.Fatalf("lookalike %q returned %#v, %v", source, candidate, ok)
		}
	}

	const trailing = `<A/>; function after() {}`
	candidate, ok := javascriptFallbackJSXCandidateAt(trailing, 0)
	if !ok || candidate.end != len(`<A/>`) {
		t.Fatalf("bounded candidate = %#v, %v", candidate, ok)
	}
}

func TestJavaScriptFallbackJSXCandidateUsesIterativeNesting(t *testing.T) {
	t.Parallel()

	const depth = 4_096
	var source strings.Builder
	source.Grow(depth*7 + len("target"))
	for range depth {
		source.WriteString("<A>")
	}
	source.WriteString("target")
	for range depth {
		source.WriteString("</A>")
	}
	content := source.String()
	candidate, ok := javascriptFallbackJSXCandidateAt(content, 0)
	if !ok || candidate.end != len(content) {
		t.Fatalf("deep candidate ended at %d, %v; want %d", candidate.end, ok, len(content))
	}
	if len(candidate.lexicalValueMarkers) != depth {
		t.Fatalf("deep value markers = %d, want %d", len(candidate.lexicalValueMarkers), depth)
	}
	if strings.Contains(maskJavaScriptSource(content, candidate.publicStringSpans), "target") {
		t.Fatal("deep JSX text remained searchable")
	}

	unterminated := strings.Repeat("<A>", depth)
	if candidate, ok := javascriptFallbackJSXCandidateAt(unterminated, 0); ok || !javascriptFallbackJSXCandidateEmpty(candidate) {
		t.Fatalf("deep malformed candidate returned %#v, %v", candidate, ok)
	}

	overLimitDepth := javascriptFallbackJSXMaximumFrames + 1
	overLimit := strings.Repeat("<A>", overLimitDepth) +
		strings.Repeat("</A>", overLimitDepth)
	if candidate, ok := javascriptFallbackJSXCandidateAt(overLimit, 0); ok ||
		!javascriptFallbackJSXCandidateEmpty(candidate) {
		t.Fatalf("over-limit candidate returned %#v, %v", candidate, ok)
	}
}

func TestJavaScriptFallbackJSXIntegrationMatchesConcreteAnalysis(t *testing.T) {
	t.Parallel()

	const source = `const view = <Panel value={{
  handler: () => { work(); },
}}>
  text // not a comment function Fake() {}
  {require("jsx-dependency")}
  {first}
  {/target/.test(value)}
</Panel>;
const adjacent = <A inside={() => require("hidden")} outside={require("shown")} />;
require("later");
`
	tree, ok := parseJavaScriptSyntax(source)
	if !ok || tree == nil || len(javascriptSyntaxErrorSpans(tree, len(source))) != 0 {
		t.Fatal("fixture did not produce clean concrete JSX")
	}
	concreteComments, concreteStrings := javascriptSyntaxMasks(source, tree)
	fallback := scanJavaScriptFallback(source)
	for name, masks := range map[string][2][]javascriptByteSpan{
		"comments": {fallback.comments, concreteComments},
		"strings":  {fallback.literals, concreteStrings},
		"both": {
			normalizeJavaScriptSpans(append(append([]javascriptByteSpan(nil), fallback.comments...), fallback.literals...)),
			normalizeJavaScriptSpans(append(append([]javascriptByteSpan(nil), concreteComments...), concreteStrings...)),
		},
	} {
		got := maskJavaScriptSource(source, masks[0])
		want := maskJavaScriptSource(source, masks[1])
		if got != want {
			t.Fatalf("%s fallback mask differs from concrete:\ngot:\n%s\nwant:\n%s", name, got, want)
		}
	}

	concrete := analyzeJavaScriptSource(source, strings.Count(source, "\n")+1)
	lexical := javascriptLexicalOnlyForTest(source)
	lexicalDefinitions := make([]sourceDefinition, 0, len(lexical.definitions))
	for _, candidate := range lexical.definitions {
		lexicalDefinitions = append(lexicalDefinitions, candidate.definition)
	}
	if got, want := javascriptDefinitionIdentities(
		sortUniqueJavaScriptDefinitions(lexicalDefinitions),
	), javascriptDefinitionIdentities(concrete.definitions); !slices.Equal(got, want) {
		t.Fatalf("fallback JSX definitions = %#v, want %#v", got, want)
	}
	if !slices.Equal(lexical.imports, concrete.imports) {
		t.Fatalf("fallback JSX imports = %#v, want %#v", lexical.imports, concrete.imports)
	}
}

func javascriptDefinitionIdentities(definitions []sourceDefinition) []javascriptDefinitionIdentity {
	identities := make([]javascriptDefinitionIdentity, 0, len(definitions))
	for _, definition := range definitions {
		identities = append(identities, javascriptDefinitionIdentity{
			symbol: definition.symbol,
			line:   definition.line,
			column: definition.column,
		})
	}
	return identities
}

func javascriptFallbackJSXCandidateEmpty(candidate javascriptFallbackJSXCandidateResult) bool {
	return candidate.end == 0 && len(candidate.publicStringSpans) == 0 &&
		len(candidate.lexicalSkipSpans) == 0 && len(candidate.lexicalValueMarkers) == 0 &&
		len(candidate.lexicalExpressionStarts) == 0 && len(candidate.lexicalExpressionEnds) == 0
}

func FuzzJavaScriptFallbackJSXCandidatePreservesCoordinates(f *testing.F) {
	f.Add("")
	f.Add(`<A/>`)
	f.Add(`<Panel value={call("value", /[}]/)}>{target}</Panel>`)
	f.Add(`<A><B></A></B>`)
	f.Add("<A>{`raw ${value}`}</A>")

	f.Fuzz(func(t *testing.T, source string) {
		candidate, ok, failureEnd := javascriptFallbackJSXCandidateAtWithFailureEnd(source, 0)
		if !ok {
			if !javascriptFallbackJSXCandidateEmpty(candidate) || failureEnd < 0 ||
				failureEnd > len(source) {
				t.Fatalf("failed candidate = %#v, failureEnd=%d", candidate, failureEnd)
			}
			return
		}
		if candidate.end <= 0 || candidate.end > len(source) || failureEnd != candidate.end {
			t.Fatalf("candidate end = %d, failureEnd=%d, source=%d",
				candidate.end, failureEnd, len(source))
		}
		for name, spans := range map[string][]javascriptByteSpan{
			"public": candidate.publicStringSpans,
			"skip":   candidate.lexicalSkipSpans,
			"value":  candidate.lexicalValueMarkers,
		} {
			previousEnd := -1
			for _, span := range spans {
				if span.start < 0 || span.start >= span.end || span.end > candidate.end ||
					span.start < previousEnd {
					t.Fatalf("%s spans = %#v for source length %d", name, spans, len(source))
				}
				previousEnd = span.end
			}
		}
		previousStart := -1
		for _, start := range candidate.lexicalExpressionStarts {
			if start <= 0 || start >= candidate.end || start < previousStart {
				t.Fatalf("expression starts = %#v for source length %d",
					candidate.lexicalExpressionStarts, len(source))
			}
			previousStart = start
		}
		previousEnd := -1
		for _, end := range candidate.lexicalExpressionEnds {
			if end <= 0 || end >= candidate.end || end < previousEnd {
				t.Fatalf("expression ends = %#v for source length %d",
					candidate.lexicalExpressionEnds, len(source))
			}
			previousEnd = end
		}
	})
}

func javascriptConcreteJSXOnlyStringSpans(
	source string,
	tree *javascriptSyntaxTree,
) []javascriptByteSpan {
	spans := make([]javascriptByteSpan, 0)
	for nodeIndex, node := range tree.nodes {
		switch node.kind {
		case "jsx_text":
			spans = append(spans, javascriptByteSpan{start: node.startByte, end: node.endByte})
		case "jsx_attribute":
			spans = append(spans, javascriptJSXAttributeSpans(source, tree, nodeIndex)...)
		}
	}
	return normalizeJavaScriptSpans(spans)
}
