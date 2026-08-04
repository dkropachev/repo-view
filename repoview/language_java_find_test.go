package repoview

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
)

func TestJavaFindRoundTripsQualifiedNamesAcrossTrivia(t *testing.T) {
	t.Parallel()

	const source = `module com /* bridge */ .
    example {
    requires java /* bridge */ .
        sql;
}`
	root := t.TempDir()
	writeFile(t, root, "module-info.java", source)
	view := mustView(t, root)

	outline, err := view.Outline("module-info.java", Options{Return: ReturnLocations})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := javaResultSymbols(outline.Results), []string{"com.example"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("outline symbols = %#v, want %#v", got, want)
	}

	javaAssertFindResult(t, view, "com.example", IncludeDefs, "def", 1)
	javaAssertFindResult(t, view, "java.sql", IncludeRefs, "ref", 3)
}

func TestJavaFindMatchesQualifiedImportsAndReferencesAcrossTrivia(t *testing.T) {
	t.Parallel()

	const source = `package demo;
import java /* bridge */ .
    util.List;
import static java
    . util.Collections.emptyList;
import module foo /* bridge */ .
    bar;
import module /* contextual identifier */ .
    api.Type;
class Use {
    Object value = alpha /* bridge */ . beta;
}`
	root := t.TempDir()
	writeFile(t, root, "Use.java", source)
	view := mustView(t, root)

	for _, test := range []struct {
		symbol string
		line   int
	}{
		{symbol: "java.util.List", line: 2},
		{symbol: "java.util.Collections.emptyList", line: 4},
		{symbol: "foo.bar", line: 6},
		{symbol: "module.api.Type", line: 8},
		{symbol: "alpha.beta", line: 11},
	} {
		javaAssertFindResult(t, view, test.symbol, IncludeRefs, "ref", test.line)
	}
}

func TestJavaFindRoundTripsRawIdentifiersAndEscapedDots(t *testing.T) {
	t.Parallel()

	const source = `module c\u006fm \u002e ex\u0061mple {
    requires j\u0061va \u002e sql;
}`
	root := t.TempDir()
	writeFile(t, root, "module-info.java", source)
	view := mustView(t, root)

	outline, err := view.Outline("module-info.java", Options{Return: ReturnLocations})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := javaResultSymbols(outline.Results), []string{`c\u006fm.ex\u0061mple`}; !reflect.DeepEqual(got, want) {
		t.Fatalf("escaped outline symbols = %#v, want %#v", got, want)
	}
	javaAssertFindResult(t, view, `c\u006fm.ex\u0061mple`, IncludeDefs, "def", 1)
	javaAssertFindResult(t, view, `j\u0061va.sql`, IncludeRefs, "ref", 2)

	decoded, err := view.Find("com.example", Options{
		Include: IncludeBoth,
		Return:  ReturnLocations,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded.Results) != 0 {
		t.Fatalf("decoded spelling matched raw escaped identifiers: %#v", decoded.Results)
	}
}

func TestJavaQualifiedOccurrenceAugmentationRejectsOpaqueAndDuplicateMatches(t *testing.T) {
	t.Parallel()

	const source = `class C {
    Object opaque = alpha "literal" . beta;
    Object text = first """literal""" . second;
    Object boundary = myalpha.betax;
    Object contiguous = plain.name;
}`
	lines := javaTestLines(source)
	prepared, ok := newJavaLanguage().prepareSource(lines).(javaLanguage)
	if !ok {
		t.Fatal("prepared Java backend has unexpected type")
	}
	for _, symbol := range []string{"alpha.beta", "first.second", "plain.name"} {
		if got := javaAdditionalOccurrenceCounts(prepared, lines, symbol); len(got) != 0 {
			t.Fatalf("additional occurrences for %q = %#v, want none", symbol, got)
		}
	}

	staleLines := javaTestLines("class D { Object value = fresh /* bridge */ . name; }")
	if got, want := javaAdditionalOccurrenceCounts(prepared, staleLines, "fresh.name"),
		map[int]int{1: 1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stale-input additional occurrences = %#v, want %#v", got, want)
	}
	overlapLines := javaTestLines(
		"class E { Object value = a /* first */ . a /* second */ . a; }",
	)
	if got, want := javaAdditionalOccurrenceCounts(prepared, overlapLines, "a.a"),
		map[int]int{1: 2}; !reflect.DeepEqual(got, want) {
		t.Fatalf("overlapping additional occurrences = %#v, want %#v", got, want)
	}
}

func TestJavaQualifiedFindRejectsNumericFragmentsAndProvesRawIdentity(t *testing.T) {
	t.Parallel()

	const source = `class C {
    double hexadecimal = 0x1\u002eap0;
    Object exponent = 1e10 /* bridge */ . foo;
    Object validHexName = x1 /* bridge */ . ap0;
    Object validExponentName = e10 /* bridge */ . foo;
    double namedHexadecimal = 0x1.deadp0 /* bridge */ . foo;
    Object validHexadecimalName = deadp0 /* bridge */ . foo;
    Object redistributed = a . b   .c;
}`
	root := t.TempDir()
	writeFile(t, root, "C.java", source)
	view := mustView(t, root)

	javaAssertFindResult(t, view, "x1.ap0", IncludeRefs, "ref", 4)
	javaAssertFindResult(t, view, "e10.foo", IncludeRefs, "ref", 5)
	javaAssertFindResult(t, view, "deadp0.foo", IncludeRefs, "ref", 7)
	// The raw query and source spans have the same byte length but different
	// spellings. A span-length duplicate heuristic used to lose this match.
	javaAssertFindResult(t, view, `a.b\u002ec`, IncludeRefs, "ref", 8)
}

func TestJavaQualifiedFindDoesNotEnterHexadecimalFloatingPointLiteral(t *testing.T) {
	t.Parallel()

	const source = `class C {
    double numeric = 0x1.deadp0 /* bridge */ . foo;
    double translated = \u0030x1\u002edeadp0 /* bridge */ . foo;
	double rawPrefixOnly = 0x1.deadp0.fooBar;
    Object valid = deadp0 /* bridge */ . foo;
}`
	root := t.TempDir()
	writeFile(t, root, "C.java", source)
	view := mustView(t, root)

	javaAssertFindResult(t, view, "deadp0.foo", IncludeRefs, "ref", 5)
	response, err := view.Find("x1.deadp0", Options{
		Include: IncludeRefs,
		Return:  ReturnLocations,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 0 {
		t.Fatalf("qualified query entered hexadecimal floats: %#v", response.Results)
	}
}

func TestJavaUnqualifiedFindDoesNotEnterNumericTokens(t *testing.T) {
	t.Parallel()

	const source = `class C {
    double first = 0x1.deadp0;
    double second = 0xA.f00p2;
    Object realExponent = deadp0;
    Object realFraction = f00p2;
}`
	root := t.TempDir()
	writeFile(t, root, "C.java", source)
	view := mustView(t, root)
	for _, test := range []struct {
		symbol string
		line   int
	}{
		{symbol: "deadp0", line: 4},
		{symbol: "f00p2", line: 5},
	} {
		response, err := view.Find(test.symbol, Options{
			Include: IncludeRefs,
			Return:  ReturnLocations,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(response.Results) != 1 || response.Results[0].Line != test.line {
			t.Fatalf("Find(%q) results = %#v, want only real identifier on line %d",
				test.symbol, response.Results, test.line)
		}
	}
}

func TestJavaNumericFindCorrectionPreservesRealSameLineMatch(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		source string
		symbol string
	}{
		{
			name:   "unqualified",
			source: `class C { double n = 0x1.deadp0; Object real = deadp0; }`,
			symbol: "deadp0",
		},
		{
			name:   "qualified",
			source: `class C { double n = 0x1.deadp0.foo; Object real = deadp0.foo; }`,
			symbol: "deadp0.foo",
		},
		{
			name:   "qualified mixed negative then positive",
			source: `class C { double n = 0x1.deadp0.foo; Object real = deadp0 /* gap */ . foo; }`,
			symbol: "deadp0.foo",
		},
		{
			name:   "qualified mixed positive then negative",
			source: `class C { Object real = deadp0 /* gap */ . foo; double n = 0x1.deadp0.foo; }`,
			symbol: "deadp0.foo",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeFile(t, root, "C.java", test.source)
			javaAssertFindResult(
				t, mustView(t, root), test.symbol, IncludeRefs, "ref", 1,
			)
		})
	}
}

func TestJavaQualifiedOccurrenceWalkerAttributesOverlapsAndRefreshesMutations(t *testing.T) {
	t.Parallel()

	lines := javaTestLines(`class C {
    Object value = a /* first
        bridge */ . a /* second
        bridge */ . a;
}`)
	prepared := newJavaLanguage().prepareSource(lines).(javaLanguage)
	got := javaAdditionalOccurrenceCounts(prepared, lines, "a.a")
	want := map[int]int{2: 1, 3: 1}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("multi-line overlapping occurrences = %#v, want %#v", got, want)
	}

	mutable := []string{"class D { Object value = old /* bridge */ . name; }"}
	prepared = newJavaLanguage().prepareSource(mutable).(javaLanguage)
	mutable[0] = "class D { Object value = fresh /* bridge */ . name; }"
	if got := javaAdditionalOccurrenceCounts(prepared, mutable, "old.name"); len(got) != 0 {
		t.Fatalf("same-slice mutation retained old match: %#v", got)
	}
	if got, want := javaAdditionalOccurrenceCounts(prepared, mutable, "fresh.name"),
		map[int]int{1: 1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("same-slice mutation occurrences = %#v, want %#v", got, want)
	}
}

func TestJavaQualifiedFindHonorsCommentsStringsAndTemplateExpressions(t *testing.T) {
	t.Parallel()

	const source = `class C {
    // alpha.beta
    String ordinary = "alpha.beta";
    Object code = alpha /* bridge */ . beta;
    String templateLiteral = STR."alpha.beta";
    String templateExpression = STR."value \{alpha /* bridge */ . beta}";
}`
	root := t.TempDir()
	writeFile(t, root, "C.java", source)
	view := mustView(t, root)

	tests := []struct {
		name string
		opts Options
		want []int
	}{
		{
			name: "default",
			opts: Options{Include: IncludeRefs, Return: ReturnLocations},
			want: []int{2, 3, 4, 5, 6},
		},
		{
			name: "no-comments",
			opts: Options{Include: IncludeRefs, Return: ReturnLocations, NoComments: true},
			want: []int{3, 4, 5, 6},
		},
		{
			name: "no-strings",
			opts: Options{Include: IncludeRefs, Return: ReturnLocations, NoStrings: true},
			want: []int{2, 4, 6},
		},
		{
			name: "code-only",
			opts: Options{
				Include: IncludeRefs, Return: ReturnLocations,
				NoComments: true, NoStrings: true,
			},
			want: []int{4, 6},
		},
		{
			name: "drop-comments",
			opts: Options{
				Include: IncludeRefs, Return: ReturnLocations,
				DropComments: true,
			},
			want: []int{3, 4, 5, 6},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			response, err := view.Find("alpha.beta", test.opts)
			if err != nil {
				t.Fatal(err)
			}
			got := make([]int, 0, len(response.Results))
			for _, result := range response.Results {
				got = append(got, result.Line)
			}
			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("result lines = %#v, want %#v; results = %#v",
					got, test.want, response.Results)
			}
		})
	}
}

func TestJavaQualifiedFindSeesChainedTemplateExpressions(t *testing.T) {
	t.Parallel()

	const source = `class C {
    Object direct = P."ONE \{x}"."TWO \{alpha /* direct */ . beta}";
    Object nested = STR."OUTER \{P."ONE \{x}"."TWO \{alpha /* nested */ . beta}"} TAIL";
    Object plain = P."PLAIN"."TWO \{alpha /* plain */ . beta}";
    Object brace = STR."OUTER \{new Processor() {}."TWO \{alpha /* brace */ . beta}"} TAIL";
    String opaque = "alpha.beta";
}`
	root := t.TempDir()
	writeFile(t, root, "C.java", source)
	view := mustView(t, root)

	response, err := view.Find("alpha.beta", Options{
		Include:    IncludeRefs,
		Return:     ReturnLocations,
		NoComments: true,
		NoStrings:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := make([]int, 0, len(response.Results))
	for _, result := range response.Results {
		got = append(got, result.Line)
	}
	if want := []int{2, 3, 4, 5}; !reflect.DeepEqual(got, want) {
		t.Fatalf("chained template result lines = %#v, want %#v; results=%#v",
			got, want, response.Results)
	}
}

func TestJavaQualifiedFindClassifiesDefinitionsReferencesAndScopes(t *testing.T) {
	t.Parallel()

	const moduleSource = `module foo /* definition */ . bar {
    requires foo /* reference */ . bar;
}`
	root := t.TempDir()
	writeFile(t, root, "module-info.java", moduleSource)
	view := mustView(t, root)

	for _, test := range []struct {
		include Include
		kinds   []string
		lines   []int
	}{
		{include: IncludeDefs, kinds: []string{"def"}, lines: []int{1}},
		{include: IncludeRefs, kinds: []string{"ref"}, lines: []int{2}},
		{include: IncludeBoth, kinds: []string{"def", "ref"}, lines: []int{1, 2}},
	} {
		response, err := view.Find("foo.bar", Options{
			Include: test.include,
			Return:  ReturnLocations,
		})
		if err != nil {
			t.Fatal(err)
		}
		gotKinds := make([]string, 0, len(response.Results))
		gotLines := make([]int, 0, len(response.Results))
		for _, result := range response.Results {
			gotKinds = append(gotKinds, result.Kind)
			gotLines = append(gotLines, result.Line)
		}
		if !reflect.DeepEqual(gotKinds, test.kinds) ||
			!reflect.DeepEqual(gotLines, test.lines) {
			t.Fatalf("Find include %q = (%#v, %#v), want (%#v, %#v)",
				test.include, gotKinds, gotLines, test.kinds, test.lines)
		}
	}

	const scopeSource = `class C {
    void first() {
        alpha /* one */ . beta();
        alpha /* two */ . beta();
    }
    void second() {
        alpha /* three */ . beta();
    }
}`
	writeFile(t, root, "C.java", scopeSource)
	view = mustView(t, root)
	response, err := view.Find("alpha.beta", Options{
		Include: IncludeRefs,
		Return:  ReturnScope,
		Limit:   1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].Scope != "first" ||
		!response.ResultsTruncated {
		t.Fatalf("scope-deduplicated limited response = %#v", response)
	}
}

func TestJavaQualifiedFindManyPreservesFairLimitsAndDuplicateSelectors(t *testing.T) {
	t.Parallel()

	const source = `class C {
    void first() { alpha /* one */ . beta(); }
    void second() { alpha /* two */ . beta(); }
    void third() { gamma /* three */ . delta(); }
}`
	root := t.TempDir()
	writeFile(t, root, "C.java", source)
	view := mustView(t, root)

	responses, err := view.FindMany(
		[]string{"alpha.beta", "gamma.delta", "missing.name"},
		Options{Include: IncludeRefs, Return: ReturnLocations, Limit: 3},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(responses) != 3 || len(responses[0].Results) != 1 ||
		!responses[0].ResultsTruncated || len(responses[1].Results) != 1 ||
		responses[1].ResultsTruncated || len(responses[2].Results) != 0 ||
		responses[2].ResultsTruncated {
		t.Fatalf("fair qualified responses = %#v", responses)
	}

	duplicates, err := view.FindMany(
		[]string{"alpha.beta", "alpha.beta"},
		Options{Include: IncludeRefs, Return: ReturnLocations, Limit: 2},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(duplicates) != 2 {
		t.Fatalf("duplicate selector responses = %#v", duplicates)
	}
	for _, response := range duplicates {
		if len(response.Results) != 1 || !response.ResultsTruncated {
			t.Fatalf("duplicate selector response = %#v", response)
		}
	}
}

func TestJavaQualifiedOccurrenceWalkerStreamsLimitsWithoutLineMap(t *testing.T) {
	const lineCount = 16 << 10
	source := strings.Repeat("alpha /* bridge */ . beta;\n", lineCount)
	lines := javaTestLines(source)
	prepared := newJavaLanguage().prepareSource(lines).(javaLanguage)

	visited := 0
	handled := prepared.walkAdditionalSymbolOccurrences(
		lines, "alpha.beta",
		func(lineNo, additional int) bool {
			visited++
			if lineNo != 1 || additional != 1 {
				t.Fatalf("first visit = line %d count %d, want line 1 count 1",
					lineNo, additional)
			}
			return false
		},
	)
	if !handled || visited != 1 {
		t.Fatalf("handled = %v, visits = %d, want true and 1", handled, visited)
	}

	allocations := testing.AllocsPerRun(3, func() {
		count := 0
		if !prepared.walkAdditionalSymbolOccurrences(
			lines, "alpha.beta",
			func(_ int, additional int) bool {
				count += additional
				return true
			},
		) || count != lineCount {
			panic("qualified occurrence walk lost matches")
		}
	})
	if allocations > 32 {
		t.Fatalf("streaming walk allocated %.0f objects, want at most 32", allocations)
	}
}

func TestJavaQualifiedOccurrenceWalkerCoversRetainedMiddleAndOpaqueOverflow(t *testing.T) {
	t.Run("retained-token-middle", func(t *testing.T) {
		var source strings.Builder
		source.Grow(2*javaMaximumStoredLexicalTokens + 80)
		source.WriteString("class C {")
		source.WriteString(strings.Repeat(";", javaMaximumStoredLexicalTokens))
		source.WriteString("alpha /* bridge */ . beta;")
		source.WriteString(strings.Repeat(";", javaMaximumStoredLexicalTokens))
		source.WriteByte('}')
		lines := javaTestLines(source.String())
		prepared := newJavaLanguage().prepareSource(lines).(javaLanguage)
		if !prepared.analysis.lexed.truncated {
			t.Fatal("adversarial source did not exceed retained-token storage")
		}
		if got, want := javaAdditionalOccurrenceCounts(prepared, lines, "alpha.beta"),
			map[int]int{1: 1}; !reflect.DeepEqual(got, want) {
			t.Fatalf("middle occurrence = %#v, want %#v", got, want)
		}
	})

	t.Run("opaque-span-overflow", func(t *testing.T) {
		var source strings.Builder
		source.Grow(javaMaximumStoredLexicalSpans*4 + 80)
		source.WriteString(strings.Repeat(`"x";`, javaMaximumStoredLexicalSpans+1))
		source.WriteString("alpha /* bridge */ . beta;")
		source.WriteString(`"tail";`)
		lines := javaTestLines(source.String())
		prepared := newJavaLanguage().prepareSource(lines).(javaLanguage)
		if len(prepared.analysis.stringSpans) != javaMaximumStoredLexicalSpans {
			t.Fatalf("stored string spans = %d, want overflow cap %d",
				len(prepared.analysis.stringSpans), javaMaximumStoredLexicalSpans)
		}
		if got, want := javaAdditionalOccurrenceCounts(prepared, lines, "alpha.beta"),
			map[int]int{1: 1}; !reflect.DeepEqual(got, want) {
			t.Fatalf("post-overflow occurrence = %#v, want %#v", got, want)
		}
	})
}

func TestJavaQualifiedFindSearchMasksRemainExactAfterSpanOverflow(t *testing.T) {
	t.Run("comments", func(t *testing.T) {
		var source strings.Builder
		source.Grow(javaMaximumStoredLexicalSpans*6 + 100)
		source.WriteString(strings.Repeat("/*x*/\n", javaMaximumStoredLexicalSpans))
		source.WriteString("class C { Object x = a.b; Object y = c /* real */ . d; }\n")
		source.WriteString("/* overflow */")
		root := t.TempDir()
		writeFile(t, root, "C.java", source.String())
		view := mustView(t, root)
		for _, options := range []Options{
			{Include: IncludeRefs, Return: ReturnLocations, NoComments: true},
			{Include: IncludeRefs, Return: ReturnLocations, DropComments: true},
		} {
			for _, symbol := range []string{"a.b", "c.d"} {
				response, err := view.Find(symbol, options)
				if err != nil {
					t.Fatal(err)
				}
				if len(response.Results) != 1 ||
					response.Results[0].Line != javaMaximumStoredLexicalSpans+1 {
					t.Fatalf("Find(%q, %#v) = %#v, want real code line",
						symbol, options, response.Results)
				}
			}
		}
	})

	t.Run("strings", func(t *testing.T) {
		var source strings.Builder
		source.Grow(javaMaximumStoredLexicalSpans*5 + 100)
		source.WriteString(strings.Repeat("\"x\";\n", javaMaximumStoredLexicalSpans))
		source.WriteString("class C { Object x = a.b; Object y = c /* real */ . d; }\n")
		source.WriteString("\"overflow\";")
		root := t.TempDir()
		writeFile(t, root, "C.java", source.String())
		view := mustView(t, root)
		for _, symbol := range []string{"a.b", "c.d"} {
			response, err := view.Find(symbol, Options{
				Include:   IncludeRefs,
				Return:    ReturnLocations,
				NoStrings: true,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(response.Results) != 1 ||
				response.Results[0].Line != javaMaximumStoredLexicalSpans+1 {
				t.Fatalf("Find(%q, NoStrings) = %#v, want real code line",
					symbol, response.Results)
			}
		}
	})
}

func TestJavaQualifiedOccurrencePatternExceedsRetainedTokenLimit(t *testing.T) {
	components := javaMaximumStoredLexicalTokens/2 + 1
	var symbol strings.Builder
	symbol.Grow(components*2 - 1)
	for index := range components {
		if index > 0 {
			symbol.WriteByte('.')
		}
		symbol.WriteByte('a')
	}
	query := symbol.String()
	bridge := len(query) / 2
	for query[bridge] != '.' {
		bridge++
	}
	source := query[:bridge] + " /* bridge */ . " + query[bridge+1:]
	lines := []string{source}
	prepared := newJavaLanguage().prepareSource(lines).(javaLanguage)
	if got, want := javaAdditionalOccurrenceCounts(prepared, lines, query),
		map[int]int{1: 1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("long-query occurrence = %#v, want %#v", got, want)
	}
}

func javaAdditionalOccurrenceCounts(
	backend javaLanguage,
	lines []string,
	symbol string,
) map[int]int {
	counts := make(map[int]int)
	backend.walkAdditionalSymbolOccurrences(
		lines, symbol,
		func(lineNo, additional int) bool {
			if additional > 0 {
				counts[lineNo] = additional
			}
			return true
		},
	)
	return counts
}

func javaAssertFindResult(
	t *testing.T,
	view *RepoView,
	symbol string,
	include Include,
	wantKind string,
	wantLine int,
) {
	t.Helper()
	response, err := view.Find(symbol, Options{
		Include: include,
		Return:  ReturnLocations,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 {
		t.Fatalf("Find(%q) results = %#v, want one", symbol, response.Results)
	}
	result := response.Results[0]
	if result.Kind != wantKind || result.Line != wantLine || result.Language != "java" {
		t.Fatalf("Find(%q) result = %#v, want %s on line %d",
			symbol, result, wantKind, wantLine)
	}
}

func BenchmarkJavaQualifiedOccurrenceAugmentationScaling(b *testing.B) {
	for _, components := range []int{1 << 10, 4 << 10, 16 << 10} {
		b.Run(strconv.Itoa(components), func(b *testing.B) {
			var source strings.Builder
			source.Grow(components*18 + 40)
			source.WriteString("class C { Object value = ")
			for index := range components {
				if index > 0 {
					source.WriteString(" /* bridge */ . ")
				}
				source.WriteByte('a')
			}
			source.WriteString("; }")
			lines := javaTestLines(source.String())
			prepared := newJavaLanguage().prepareSource(lines).(javaLanguage)
			b.ReportAllocs()
			b.SetBytes(int64(source.Len()))
			b.ResetTimer()
			for range b.N {
				got := javaAdditionalOccurrenceCounts(prepared, lines, "a.a")
				if got[1] != components-1 {
					b.Fatalf("additional occurrences = %#v, want %d on line 1",
						got, components-1)
				}
			}
		})
	}
}
