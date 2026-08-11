package navigator

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

const javaGapRegressionPaddingTokens = javaMaximumStoredLexicalTokens

type javaGapRegressionSnapshot struct {
	definitions []sourceDefinition
	scopes      []javaLineScope
	imports     []javaLineSpan
}

func javaGapRegressionSnapshotWithin(
	analysis *javaSourceAnalysis,
	firstLine, lineCount int,
) javaGapRegressionSnapshot {
	lastLine := firstLine + lineCount - 1
	var snapshot javaGapRegressionSnapshot
	for _, definition := range analysis.definitions {
		if definition.line < firstLine || definition.line > lastLine {
			continue
		}
		definition.line -= firstLine - 1
		definition.scopeStart -= firstLine - 1
		definition.scopeEnd -= firstLine - 1
		snapshot.definitions = append(snapshot.definitions, definition)
	}
	for _, scope := range analysis.scopes {
		if scope.end < firstLine || scope.start > lastLine {
			continue
		}
		snapshot.scopes = append(snapshot.scopes, javaLineScope{
			start: scope.start - firstLine + 1,
			end:   scope.end - firstLine + 1,
		})
	}
	for _, span := range analysis.imports {
		if span.end < firstLine || span.start > lastLine {
			continue
		}
		snapshot.imports = append(snapshot.imports, javaLineSpan{
			start: span.start - firstLine + 1,
			end:   span.end - firstLine + 1,
		})
	}
	return snapshot
}

func javaAssertGapRegressionParity(t *testing.T, fixture string) {
	t.Helper()
	fixture = strings.Trim(fixture, "\n")
	fixtureLines := strings.Count(fixture, "\n") + 1
	baseline := analyzeJavaSource(fixture, fixtureLines)
	if baseline.lexed.truncated {
		t.Fatal("baseline unexpectedly exceeded retained-token storage")
	}

	padding := strings.Repeat("; ", javaGapRegressionPaddingTokens)
	source := padding + "\n" + fixture + "\n" + padding
	gap := analyzeJavaSource(source, strings.Count(source, "\n")+1)
	if !gap.lexed.truncated {
		t.Fatal("padded fixture did not enter retained-token gap recovery")
	}
	gapStart, gapEnd, ok := javaStoredTokenGapRange(gap.lexed.tokens)
	fixtureStart := len(padding) + 1
	fixtureEnd := fixtureStart + len(fixture)
	if !ok || fixtureStart < gapStart || fixtureEnd > gapEnd {
		t.Fatalf("fixture bytes %d-%d are not wholly in token gap %d-%d, %v",
			fixtureStart, fixtureEnd, gapStart, gapEnd, ok)
	}

	want := javaGapRegressionSnapshotWithin(baseline, 1, fixtureLines)
	got := javaGapRegressionSnapshotWithin(gap, 2, fixtureLines)
	if !reflect.DeepEqual(
		javaDefinitionSummaries(got.definitions),
		javaDefinitionSummaries(want.definitions),
	) {
		t.Errorf("definition parity mismatch\nbaseline: %#v\ngap:      %#v",
			want.definitions, got.definitions)
	}
	if !reflect.DeepEqual(got.scopes, want.scopes) {
		t.Errorf("scope parity mismatch\nbaseline: %#v\ngap:      %#v",
			want.scopes, got.scopes)
	}
	if !reflect.DeepEqual(got.imports, want.imports) {
		t.Errorf("import parity mismatch\nbaseline: %#v\ngap:      %#v",
			want.imports, got.imports)
	}
}

func TestJavaRetainedGapDeclarationRegressionParity(t *testing.T) {
	for _, test := range []struct {
		name         string
		fixture      string
		recoveryOnly bool
	}{
		{
			name: "compact-compilation-root",
			fixture: `/** greeting docs */
Str\u0069ng greeting = "hi";
int first = 1, second = 2;
vo\u0069d main() {
    if (ready()) run();
}
String render(int value) { return format(value); }
System.out.println("not a declaration");
helper();
<String>helper();
this.<String>qualified();`,
			// Top-level members are intentional fragments for recovery-mode consumers,
			// not a valid Java compilation unit.
			recoveryOnly: true,
		},
		{
			name:    "annotated-owner-constructor",
			fixture: `class Alpha { @Deprecated Alpha() {} }`,
		},
		{
			name: "constructors-and-annotation-defaults",
			fixture: `@interface Nested { int value(); }
@interface Config {
    int[] values() default {1, 2};
    Nested nested() default @Nested(value = 1);
}
class Alpha<T extends Number> {
    /** generic constructor docs */
    @Deprecated
    <R extends Number> Alpha(R value) {}
}
record Pair(int value) {
    /** compact constructor docs */
    @Deprecated
    Pair { if (value < 0) throw new IllegalArgumentException(); }
}`,
		},
		{
			name: "scoped-comma-and-array-initializers",
			fixture: `class Initializers {
    /** comma owners */
    Object first = new Object() {
        void one() {}
    }, second = new Object() {
        void two() {}
    };
    Runnable firstTask = () -> {
        one();
    }, secondTask = () -> {
        two();
    };
    Object[] values = {
        new Object() { void nested() {} }
    };
    Runnable[] tasks = {
        () -> { first(); },
        () -> second()
    };
    int after;
}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if !test.recoveryOnly {
				javaAssertConcreteSyntax(t, test.fixture)
			}
			javaAssertGapRegressionParity(t, test.fixture)
		})
	}
}

func TestJavaRetainedGapControlScopeRegressionParity(t *testing.T) {
	for _, test := range []struct {
		name    string
		fixture string
	}{
		{
			name: "aggregate-unbraced-and-owned-scopes",
			fixture: `class Controls {
    /** static initializer docs */
    static {
        initialize();
    }
    Runnable task = () -> {
        if (ready()) run();
    };
    int choose(int value) {
        return switch (value) {
            case 1 -> { yield one(); }
            case 2 -> two();
            default -> zero();
        };
    }
    void work(boolean ready, int[] values) throws Exception {
        if (ready) {
            yes();
        } else {
            no();
        }
        if (ready) yes(); else no();
        while (ready) work();
        do work(); while (ready);
        for (int i = 0; i < 2; i++) use(i);
        for (int value : values) use(value);
        outer: while (ready) break outer;
        try {
            work();
        } catch (RuntimeException failure) {
            recover(failure);
        } finally {
            finish();
        }
        try (AutoCloseable resource = open()) {
            use(resource);
        } catch (Exception failure) {
            recover(failure);
        }
    }
}`,
		},
		{
			name: "while-if-else",
			fixture: `class C {
    void run(boolean a, boolean b) {
        while (a)
            if (b)
                yes();
            else
                no();
    }
}`,
		},
		{
			name: "if-while-terminal",
			fixture: `class C {
    void run(boolean a, boolean b) {
        if (a)
            while (b)
                work();
    }
}`,
		},
		{
			name: "for-synchronized-block",
			fixture: `class C {
    void run(Object lock) {
        for (;;)
            synchronized (lock) {
                work();
            }
    }
}`,
		},
		{
			name: "label-unbraced-loop",
			fixture: `class C {
    void run(boolean ready) {
        retry:
            while (ready)
                work();
    }
}`,
		},
		{
			name: "stacked-labels",
			fixture: `class C {
    void run(boolean ready) {
        outer:
        inner:
            while (ready)
                work();
    }
}`,
		},
		{
			name: "if-label-loop",
			fixture: `class C {
    void run(boolean a, boolean b) {
        if (a)
            retry:
                while (b)
                    work();
    }
}`,
		},
		{
			name: "braced-outer-unbraced-inner",
			fixture: `class C {
    void run(boolean a, boolean b) {
        if (a) {
            while (b)
                work();
        }
    }
}`,
		},
		{
			name: "unbraced-outer-braced-inner-else",
			fixture: `class C {
    void run(boolean a, boolean b) {
        if (a)
            while (b) {
                work();
            }
        else
            fallback();
    }
}`,
		},
		{
			name: "triple-dangling-else",
			fixture: `class C {
    void run(boolean a, boolean b, boolean c) {
        if (a)
            if (b)
                if (c)
                    first();
                else
                    second();
            else
                third();
        else
            fourth();
    }
}`,
		},
		{
			name: "else-if-mixed-bodies",
			fixture: `class C {
    void run(boolean a, boolean b) {
        if (a) {
            first();
        } else if (b)
            second();
        else {
            third();
        }
    }
}`,
		},
		{
			name: "braced-do-with-inner-if",
			fixture: `class C {
    void run(boolean a, boolean ready) {
        do {
            if (a)
                work();
        } while (ready);
    }
}`,
		},
		{
			name: "unbraced-do-if-else",
			fixture: `class C {
    void run(boolean a, boolean ready) {
        do
            if (a)
                yes();
            else
                no();
        while (ready);
    }
}`,
		},
		{
			name: "nested-unbraced-do",
			fixture: `class C {
    void run(boolean a, boolean b) {
        do
            do
                work();
            while (a);
        while (b);
    }
}`,
		},
		{
			name: "labeled-do",
			fixture: `class C {
    void run(boolean ready) {
        again:
            do
                work();
            while (ready);
    }
}`,
		},
		{
			name: "nested-ternary-is-not-label",
			fixture: `class C {
    Object run(boolean a, boolean b) {
        return a
            ? b
                ? first()
                : second()
            : third();
    }
}`,
		},
		{
			name: "assert-ternary-is-not-label",
			fixture: `class C {
    void run(boolean condition, boolean choose) {
        assert condition
            : choose ? first() : second();
    }
}`,
		},
		{
			name: "enhanced-for-colon-is-not-label",
			fixture: `class C {
    void run(Iterable<String> values) {
        for (var value : values)
            work(value);
    }
}`,
		},
		{
			name: "try-multiple-catches-finally",
			fixture: `class C {
    void run() {
        try {
            work();
        } catch (First failure) {
            first();
        } catch (Second failure) {
            second();
        } finally {
            finish();
        }
    }
}`,
		},
		{
			name: "labeled-switch",
			fixture: `class C {
    void run(int value) {
        choose:
            switch (value) {
                case 1:
                    first();
                    break choose;
                default:
                    fallback();
            }
    }
}`,
		},
		{
			name: "dangling-else",
			fixture: `class C {
    void run(boolean a, boolean b) {
        if (a)
            if (b)
                yes();
            else
                no();
    }
}`,
		},
		{
			name: "nested-else-chain",
			fixture: `class C {
    void run(boolean a, boolean b) {
        if (a)
            if (b) yes();
            else no();
        else outerNo();
    }
}`,
		},
		{
			name: "unbraced-into-braced",
			fixture: `class C {
    void run(boolean a, boolean b) {
        if (a)
            while (b) {
                work();
            }
    }
}`,
		},
		{
			name: "multiline-label-to-brace",
			fixture: `class C {
    void run(boolean ready) {
        retry:
            while (ready) {
                break retry;
            }
    }
}`,
		},
		{
			name: "ternary-is-not-label",
			fixture: `class C {
    void run() {
        Object value = condition
            ? left
            : right;
    }
}`,
		},
		{
			name: "assert-is-not-label",
			fixture: `class C {
    void run(boolean condition, Object message) {
        assert condition
            : message;
    }
}`,
		},
		{
			name: "multiline-braced-do-while",
			fixture: `class C {
    void run(boolean ready) {
        do {
            work();
        }
        while (ready);
    }
}`,
		},
		{
			name: "multiline-unbraced-do-while",
			fixture: `class C {
    void run(boolean ready) {
        do
            work();
        while (ready);
    }
}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			javaAssertConcreteSyntax(t, test.fixture)
			javaAssertGapRegressionParity(t, test.fixture)
		})
	}
}

func TestJavaRetainedGapCrossBoundaryScopeCorrections(t *testing.T) {
	const fillerTokens = javaMaximumStoredLexicalTokens + 32
	filler := strings.Repeat("; ", fillerTokens)

	t.Run("documented-owners-cross-gap", func(t *testing.T) {
		source := "/** Class docs. */\nclass Cross {\n" +
			"    /** Method docs. */\n    void work() {\n" + filler +
			"\n    }\n}\n"
		analysis := analyzeJavaSource(source, strings.Count(source, "\n")+1)
		if !analysis.lexed.truncated {
			t.Fatal("fixture did not enter retained-token gap recovery")
		}
		for _, want := range []sourceDefinition{
			{symbol: "Cross", line: 2, scopeStart: 1, scopeEnd: 7, ownsScope: true},
			{symbol: "work", line: 4, scopeStart: 3, scopeEnd: 6, ownsScope: true},
		} {
			got := javaGapRegressionDefinitionsNamed(analysis.definitions, want.symbol)
			if len(got) != 1 || got[0].line != want.line ||
				got[0].scopeStart != want.scopeStart || got[0].scopeEnd != want.scopeEnd ||
				!got[0].ownsScope {
				t.Errorf("%s metadata = %#v, want line/start/end %d/%d/%d owning",
					want.symbol, got, want.line, want.scopeStart, want.scopeEnd)
			}
		}
	})

	t.Run("control-scope-cross-gap", func(t *testing.T) {
		source := "class Cross {\n    void work() {\n        if (true) {\n" +
			filler + "\n        }\n        after();\n    }\n}\n"
		analysis := analyzeJavaSource(source, strings.Count(source, "\n")+1)
		want := javaLineScope{start: 3, end: 5}
		if !javaGapRegressionHasScope(analysis.scopes, want) {
			t.Fatalf("missing exact cross-gap control scope %#v; scopes=%#v",
				want, analysis.scopes)
		}
	})

	t.Run("same-start-sibling-survives-correction", func(t *testing.T) {
		source := "class Cross {\n    Runnable shorty = () -> {}; void work() {\n" +
			filler + "\n    }\n}\n"
		analysis := analyzeJavaSource(source, strings.Count(source, "\n")+1)
		want := javaLineScope{start: 2, end: 2}
		if !javaGapRegressionHasScope(analysis.scopes, want) {
			t.Fatalf("same-start sibling scope %#v was removed; scopes=%#v",
				want, analysis.scopes)
		}
	})
}

type javaGapRegressionSymbolExpectation struct {
	fragment string
	want     string
}

func javaAssertGapRegressionSymbols(
	t *testing.T,
	fixture string,
	expectations []javaGapRegressionSymbolExpectation,
) {
	t.Helper()
	fixture = strings.Trim(fixture, "\n")
	javaAssertConcreteSyntax(t, fixture)
	baselineLines := javaTestLines(fixture)
	baseline := javaPreparedSymbolTestBackend(baselineLines)

	padding := strings.Repeat("; ", javaGapRegressionPaddingTokens)
	source := padding + "\n" + fixture + "\n" + padding
	gapLines := javaTestLines(source)
	gap := javaPreparedSymbolTestBackend(gapLines)
	if gap.analysis == nil || !gap.analysis.lexed.truncated {
		t.Fatal("padded symbol fixture did not enter retained-token gap recovery")
	}
	gapStart, gapEnd, ok := javaStoredTokenGapRange(gap.analysis.lexed.tokens)
	fixtureStart := len(padding) + 1
	if !ok || fixtureStart < gapStart || fixtureStart+len(fixture) > gapEnd {
		t.Fatalf("symbol fixture is not wholly in gap %d-%d, %v", gapStart, gapEnd, ok)
	}

	for _, expectation := range expectations {
		lineNo := javaSymbolTestLine(t, baselineLines, expectation.fragment)
		want, wantOK := baseline.symbolOnLine(baselineLines, lineNo)
		if !wantOK || want != expectation.want {
			t.Fatalf("baseline %q symbol = %q,%v; want %q,true",
				expectation.fragment, want, wantOK, expectation.want)
		}
		got, gotOK := gap.symbolOnLine(gapLines, lineNo+1)
		if !gotOK || got != expectation.want {
			t.Errorf("gap %q symbol = %q,%v; want %q,true",
				expectation.fragment, got, gotOK, expectation.want)
		}
	}
}

func javaGapRegressionContextualSymbol(
	t *testing.T,
	target string,
	padded bool,
) (string, bool) {
	t.Helper()
	var source strings.Builder
	source.WriteString("class C { void work() {\n")
	if padded {
		source.WriteString(strings.Repeat("; ", javaGapRegressionPaddingTokens))
		source.WriteByte('\n')
		source.WriteString(strings.Repeat("; ", javaMaximumRecoveryHeaderTokens+1))
	}
	source.WriteString(target)
	if padded {
		source.WriteString(strings.Repeat("; ", javaMaximumRecoveryHeaderTokens+96))
		source.WriteByte('\n')
		source.WriteString(strings.Repeat("; ", javaGapRegressionPaddingTokens))
	}
	source.WriteString("\n} }\n")
	lines := javaTestLines(source.String())
	backend := javaPreparedSymbolTestBackend(lines)
	lineNo := javaSymbolTestLine(t, lines, target)
	return backend.symbolOnLine(lines, lineNo)
}

func TestJavaRetainedGapContextualAndWrappedSymbolSelection(t *testing.T) {
	t.Run("contextual-keywords-on-overbudget-lines", func(t *testing.T) {
		for _, test := range []javaGapRegressionSymbolExpectation{
			{fragment: "requires Foo;", want: "Foo"},
			{fragment: "requires transitive Foo;", want: "Foo"},
			{fragment: "if (true) yield result;", want: "result"},
			{fragment: "while (true) yield result;", want: "result"},
			{fragment: "for (;;) yield result;", want: "result"},
			{fragment: "synchronized (this) yield result;", want: "result"},
		} {
			t.Run(test.fragment, func(t *testing.T) {
				want, wantOK := javaGapRegressionContextualSymbol(t, test.fragment, false)
				if !wantOK || want != test.want {
					t.Fatalf("baseline symbol = %q,%v; want %q,true",
						want, wantOK, test.want)
				}
				got, gotOK := javaGapRegressionContextualSymbol(t, test.fragment, true)
				if !gotOK || got != test.want {
					t.Errorf("gap symbol = %q,%v; want %q,true",
						got, gotOK, test.want)
				}
			})
		}
	})

	t.Run("wrapped-terminal-calls", func(t *testing.T) {
		javaAssertGapRegressionSymbols(t, `class Selectors {
    void work(int n) {
        values[index()].member().terminal();
        new Holder(factory()).terminal();
        (new Holder(factory())).terminal();
        (values[index()]).member().terminal();
        ((Holder) factory()).terminal();
        new int[size()].clone();
        (condition ? first() : second()).terminal();
        (first() + second()).terminal();
        (first()).terminal();
        ((Supplier<Object>) () -> factory()).get().terminal();
        (switch (n) { default -> factory(); }).terminal();
    }
}`, []javaGapRegressionSymbolExpectation{
			{fragment: "values[index()].member", want: "terminal"},
			{fragment: "new Holder(factory()).terminal", want: "terminal"},
			{fragment: "(new Holder(factory()))", want: "terminal"},
			{fragment: "(values[index()])", want: "terminal"},
			{fragment: "((Holder) factory())", want: "terminal"},
			{fragment: "new int[size()]", want: "clone"},
			{fragment: "condition ? first", want: "terminal"},
			{fragment: "first() + second", want: "terminal"},
			{fragment: "(first()).terminal", want: "terminal"},
			{fragment: "Supplier<Object>", want: "terminal"},
			{fragment: "switch (n) { default", want: "terminal"},
		})
	})
}

func TestJavaRetainedGapRecoveryBudgets(t *testing.T) {
	t.Run("comment-trivia-does-not-consume-header-budget", func(t *testing.T) {
		comments := strings.Repeat("/* trivia */ ", javaMaximumRecoveryHeaderTokens+1)
		fixture := comments + "class Middle {}"
		javaAssertConcreteSyntax(t, fixture)
		javaAssertGapRegressionParity(t, fixture)
	})

	t.Run("pending-owner-storage-is-aggregate-capped", func(t *testing.T) {
		const depth = 32
		const commaOwners = 132
		source := javaGapRegressionPendingOwnerSource(depth, commaOwners)
		lexed := lexJava(source)
		if !lexed.truncated {
			t.Fatal("pending-owner fixture did not enter retained-token gap recovery")
		}
		result := analyzeJavaStreamedGap(
			source, strings.Count(source, "\n")+1, lexed,
		)
		pending := 0
		for _, definition := range result.definitions {
			if strings.HasPrefix(definition.symbol, "pre_") ||
				strings.HasPrefix(definition.symbol, "nested_") {
				pending++
			}
		}
		requested := depth * (commaOwners + 1)
		if requested <= javaMaximumRecoveryHeaderTokens || pending < 1 ||
			pending > javaMaximumRecoveryHeaderTokens {
			t.Fatalf("pending owners = %d for %d requested, want 1..%d",
				pending, requested, javaMaximumRecoveryHeaderTokens)
		}
	})

	t.Run("brace-depth-cap-recovers-tail", func(t *testing.T) {
		padding := strings.Repeat("; ", javaGapRegressionPaddingTokens)
		middle := "class Deep { void run() {" +
			strings.Repeat("{", javaMaximumRecoveryHeaderTokens+8) +
			strings.Repeat("}", javaMaximumRecoveryHeaderTokens+8) +
			"} void after() {} }"
		source := padding + "\n" + middle + "\n" + padding
		analysis := analyzeJavaSource(source, strings.Count(source, "\n")+1)
		if !analysis.lexed.truncated {
			t.Fatal("brace-depth fixture did not enter retained-token gap recovery")
		}
		if got, want := javaDefinitionSymbols(analysis.definitions),
			[]string{"Deep", "run", "after"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("definitions after depth-cap unwind = %#v, want %#v", got, want)
		}
	})

	for _, test := range []struct {
		name   string
		middle string
		want   []string
	}{
		{
			name: "large-javadoc",
			middle: "class Payloads { int before; /**" +
				strings.Repeat("x", 256<<10) +
				"*/ void documented() {} int after; }",
			want: []string{"Payloads", "before", "documented", "after"},
		},
		{
			name: "large-string",
			middle: "class Payloads { int before; String opaque = \"" +
				strings.Repeat("x", 256<<10) +
				"\"; int after; }",
			want: []string{"Payloads", "before", "opaque", "after"},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			padding := strings.Repeat("; ", javaGapRegressionPaddingTokens)
			source := padding + "\n" + test.middle + "\n" + padding
			lexed := lexJava(source)
			if !lexed.truncated {
				t.Fatal("large-payload fixture did not enter retained-token gap recovery")
			}
			assertDefinitions := func() {
				result := analyzeJavaStreamedGap(
					source, strings.Count(source, "\n")+1, lexed,
				)
				if got := javaDefinitionSymbols(result.definitions); !reflect.DeepEqual(got, test.want) {
					panic(fmt.Sprintf("large-payload definitions = %#v, want %#v", got, test.want))
				}
			}
			assertDefinitions()
			allocations := testing.AllocsPerRun(1, assertDefinitions)
			if allocations > 512 {
				t.Fatalf("large-payload streamed recovery allocated %.0f objects, want <= 512",
					allocations)
			}
		})
	}

	t.Run("oversized-header-degrades-conservatively", func(t *testing.T) {
		padding := strings.Repeat("; ", javaGapRegressionPaddingTokens)
		middle := "class HeaderBudget { int before; long omitted = 0L" +
			strings.Repeat(" + 1L", javaMaximumRecoveryHeaderTokens+1) +
			"; int after; }"
		source := padding + "\n" + middle + "\n" + padding
		analysis := analyzeJavaSource(source, strings.Count(source, "\n")+1)
		if !analysis.lexed.truncated {
			t.Fatal("oversized-header fixture did not enter retained-token gap recovery")
		}
		counts := make(map[string]int)
		for _, definition := range analysis.definitions {
			counts[definition.symbol]++
		}
		for _, symbol := range []string{"HeaderBudget", "before", "after"} {
			if counts[symbol] != 1 {
				t.Errorf("%s definition count = %d; definitions=%#v",
					symbol, counts[symbol], analysis.definitions)
			}
		}
		if counts["omitted"] != 0 {
			t.Fatalf("over-budget declaration was recovered unsafely: %#v",
				analysis.definitions)
		}

		allocations := testing.AllocsPerRun(2, func() {
			result := analyzeJavaStreamedGap(
				source, strings.Count(source, "\n")+1, analysis.lexed,
			)
			if len(result.definitions) == 0 {
				panic("bounded gap recovery lost every definition")
			}
		})
		if allocations > 256 {
			t.Fatalf("over-budget streamed recovery allocated %.0f objects, want <= 256",
				allocations)
		}
	})
}

func javaGapRegressionDefinitionsNamed(
	definitions []sourceDefinition,
	symbol string,
) []sourceDefinition {
	var matches []sourceDefinition
	for _, definition := range definitions {
		if definition.symbol == symbol {
			matches = append(matches, definition)
		}
	}
	return matches
}

func javaGapRegressionHasScope(scopes []javaLineScope, want javaLineScope) bool {
	for _, scope := range scopes {
		if scope == want {
			return true
		}
	}
	return false
}

func javaGapRegressionPendingOwnerSource(depth, commaOwners int) string {
	var source strings.Builder
	source.WriteString("class PendingRoot {\n")
	for level := range depth {
		source.WriteString("Runnable ")
		for owner := range commaOwners {
			fmt.Fprintf(&source, "pre_%d_%d = () -> consume(), ", level, owner)
		}
		fmt.Fprintf(&source, "nested_%d = new Runnable() {\n", level)
	}
	source.WriteString(strings.Repeat("; ", javaMaximumStoredLexicalTokens+1024))
	source.WriteByte('\n')
	for level := depth - 1; level >= 0; level-- {
		source.WriteString("public void run() { consume(); }\n};\n")
	}
	source.WriteString("}\n")
	return source.String()
}
