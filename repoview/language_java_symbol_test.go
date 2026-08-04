package repoview

import (
	"strconv"
	"strings"
	"testing"
)

func TestJavaSymbolSelectionRanksCallsAndMembers(t *testing.T) {
	t.Parallel()

	const source = `class Symbols {
    void inspect() {
        factory().build();
        new Type().target();
        service.make().run();
        outer(inner());
        first(); second();
        Object selected = root.middle.target;
        java.util.List value = null;
        Object retained = factory().field;
        obj.<String>genericTarget();
		factory()[index].build();
		factory().items[index].build();
    }
}`
	javaAssertConcreteSyntax(t, source)
	lines := javaTestLines(source)
	backend := javaPreparedSymbolTestBackend(lines)

	for _, test := range []struct {
		fragment string
		want     string
	}{
		{fragment: "factory().build()", want: "build"},
		{fragment: "new Type().target()", want: "target"},
		{fragment: "service.make().run()", want: "run"},
		{fragment: "outer(inner())", want: "outer"},
		{fragment: "first(); second()", want: "first"},
		{fragment: "root.middle.target", want: "target"},
		{fragment: "java.util.List value", want: "List"},
		{fragment: "factory().field", want: "factory"},
		{fragment: "obj.<String>genericTarget()", want: "genericTarget"},
		{fragment: "factory()[index].build()", want: "build"},
		{fragment: "factory().items[index].build()", want: "build"},
	} {
		t.Run(test.want+"_from_"+strings.ReplaceAll(test.fragment, " ", "_"), func(t *testing.T) {
			t.Parallel()
			javaAssertSymbolTestFragment(t, backend, lines, test.fragment, test.want)
		})
	}
}

func TestJavaSymbolSelectionTracksMultilineChains(t *testing.T) {
	t.Parallel()

	const source = `class Symbols {
    void inspect() {
        client
            .session()
            .request();
    }
}`
	javaAssertConcreteSyntax(t, source)
	lines := javaTestLines(source)
	backend := javaPreparedSymbolTestBackend(lines)

	for _, test := range []struct {
		fragment string
		want     string
	}{
		{fragment: "        client", want: "client"},
		{fragment: ".session()", want: "session"},
		{fragment: ".request()", want: "request"},
	} {
		javaAssertSymbolTestFragment(t, backend, lines, test.fragment, test.want)
	}
}

func TestJavaLexicalMemberContinuationSkipsNonterminalMembers(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		source string
		member string
	}{
		{source: "root.middle.target", member: "middle"},
		{source: "Outer.Inner<String>.field", member: "Inner"},
	} {
		t.Run(test.member, func(t *testing.T) {
			t.Parallel()
			lexed := lexJava(test.source)
			delimiters := analyzeJavaDelimiters(lexed.tokens)
			memberIndex := -1
			for index, token := range lexed.tokens {
				if token.value == test.member {
					memberIndex = index
					break
				}
			}
			if memberIndex < 0 || !javaLexicalMemberContinuesOnLine(
				lexed.tokens, delimiters, memberIndex, 0, len(test.source),
			) {
				t.Fatalf("member %q in %q was not recognized as nonterminal",
					test.member, test.source)
			}
		})
	}
}

func TestJavaSymbolSelectionHandlesReferencesPatternsAndStatements(t *testing.T) {
	t.Parallel()

	const source = `record Point(int x) {}
class Symbols {
    Object inspect(Object input) {
        Runnable direct = Type::target;
		Runnable nested = Type.Nested::target;
		Runnable generic = Type::<String>target;
		Runnable annotatedGeneric = Type::<@A(flag = 1 > 0) String>target;
		java.util.function.Supplier<Type> constructor = Type::new;
		java.util.function.Supplier<Type<String>> genericConstructor = Type<String>::new;
		java.util.function.Supplier<Outer.Inner<String>> nestedGenericConstructor = Outer.Inner<String>::new;
		java.util.function.Supplier<?> annotatedConstructor = Type<@A(flag = 1 > 0) String>::new;
        var value = source;
		Object conditional = (input instanceof String) ? factory() : fallback();
		boolean matched = input instanceof Point(int x) && ready();
        Object selected = switch (input) {
            case Point(int x) when ready() -> x;
            case String s when (condition) -> s;
			case Integer i when when() -> i;
            default -> {
				if (ready())
					yield result;
				else
					yield fallback;
            }
        };
        return selected;
    }
}`
	javaAssertConcreteSyntax(t, source)
	lines := javaTestLines(source)
	backend := javaPreparedSymbolTestBackend(lines)

	for _, test := range []struct {
		fragment string
		want     string
	}{
		{fragment: "Type::target", want: "target"},
		{fragment: "Type.Nested::target", want: "target"},
		{fragment: "Type::<String>target", want: "target"},
		{fragment: "Type::<@A(flag = 1 > 0) String>target", want: "target"},
		{fragment: "Type::new", want: "Type"},
		{fragment: "Type<String>::new", want: "Type"},
		{fragment: "Outer.Inner<String>::new", want: "Inner"},
		{fragment: "Type<@A(flag = 1 > 0) String>::new", want: "Type"},
		{fragment: "var value = source", want: "value"},
		{fragment: "input instanceof String", want: "factory"},
		{fragment: "input instanceof Point", want: "ready"},
		{fragment: "case Point(int x) when ready()", want: "ready"},
		{fragment: "case String s when (condition)", want: "condition"},
		{fragment: "case Integer i when when()", want: "when"},
		{fragment: "yield result", want: "result"},
		{fragment: "yield fallback", want: "fallback"},
		{fragment: "return selected", want: "selected"},
	} {
		javaAssertSymbolTestFragment(t, backend, lines, test.fragment, test.want)
	}
}

func TestJavaSymbolSelectionPreservesContextualIdentifierCalls(t *testing.T) {
	t.Parallel()

	const source = `class Symbols {
    void inspect() {
        var();
        module();
        record();
        when();
        obj.yield();
    }
}`
	javaAssertConcreteSyntax(t, source)
	lines := javaTestLines(source)
	backend := javaPreparedSymbolTestBackend(lines)

	for _, want := range []string{"var", "module", "record", "when", "yield"} {
		fragment := want + "()"
		if want == "yield" {
			fragment = "obj.yield()"
		}
		javaAssertSymbolTestFragment(t, backend, lines, fragment, want)
	}
}

func TestJavaSymbolSelectionDoesNotGloballySuppressContextualNames(t *testing.T) {
	t.Parallel()

	const source = `class ContextualNames {
    int inspect() {
        int var = 1;
        var = source;
        Object direct =
            var -> var;
        Object parenthesized =
            (var) -> var;
        int yield = 1;
		if (ready())
			yield = result;
		if (ready())
			yield++;
		yield &= mask;
		yield[index] = value;
        return yield;
    }
}`
	javaAssertConcreteSyntax(t, source)
	lines := javaTestLines(source)
	backend := javaPreparedSymbolTestBackend(lines)

	for _, test := range []struct {
		fragment string
		want     string
	}{
		{fragment: "int var = 1", want: "var"},
		{fragment: "var = source", want: "var"},
		{fragment: "var -> var", want: "var"},
		{fragment: "(var) -> var", want: "var"},
		{fragment: "int yield = 1", want: "yield"},
		{fragment: "yield = result", want: "yield"},
		{fragment: "yield++", want: "yield"},
		{fragment: "yield &= mask", want: "yield"},
		{fragment: "yield[index]", want: "yield"},
		{fragment: "return yield", want: "yield"},
	} {
		javaAssertSymbolTestFragment(t, backend, lines, test.fragment, test.want)
	}
}

func TestJavaSymbolSelectionRanksAnnotatedLocalDeclarators(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name         string
		source       string
		wantTreeFree bool
	}{
		{
			name: "tree",
			source: `class AnnotatedLocal {
    void inspect() {
        @A final var value = source;
    }
}`,
		},
		{
			name: "unicode fallback",
			source: `class AnnotatedLocal {
    void inspect() {
        @A final \u0076ar value = source;
    }
}`,
			wantTreeFree: true,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			lines := javaTestLines(test.source)
			backend := javaPreparedSymbolTestBackend(lines)
			if test.wantTreeFree && (backend.analysis == nil || backend.analysis.tree != nil) {
				t.Fatal("escaped annotated declaration did not force lexical fallback")
			}
			if !test.wantTreeFree {
				javaAssertConcreteSyntax(t, test.source)
			}
			javaAssertSymbolTestFragment(t, backend, lines, "value = source", "value")
		})
	}
}

func TestJavaSymbolSelectionPrefersDefinitionsAndModuleNames(t *testing.T) {
	t.Parallel()

	const declarations = `@Anno(value = true) class Target {
    @Anno(value = true) void method() { helper(); }
}`
	javaAssertConcreteSyntax(t, declarations)
	declarationLines := javaTestLines(declarations)
	declarationBackend := javaPreparedSymbolTestBackend(declarationLines)
	javaAssertSymbolTestFragment(t, declarationBackend, declarationLines, "class Target", "Target")
	javaAssertSymbolTestFragment(t, declarationBackend, declarationLines, "void method", "method")

	const moduleSource = `module Host {
    requires M;
	requires static N;
	requires transitive static O;
	requires static transitive P;
}`
	javaAssertConcreteSyntax(t, moduleSource)
	moduleLines := javaTestLines(moduleSource)
	moduleBackend := javaPreparedSymbolTestBackend(moduleLines)
	javaAssertSymbolTestFragment(t, moduleBackend, moduleLines, "requires M", "M")
	javaAssertSymbolTestFragment(t, moduleBackend, moduleLines, "requires static N", "N")
	javaAssertSymbolTestFragment(t, moduleBackend, moduleLines, "requires transitive static O", "O")
	javaAssertSymbolTestFragment(t, moduleBackend, moduleLines, "requires static transitive P", "P")
}

func TestJavaSymbolSelectionIgnoresOpaqueText(t *testing.T) {
	t.Parallel()

	const source = `class Opaque {
    void inspect() {
        String quoted = "fake().target()"; real();
        /* hidden().member */ visible;
        String block = """
            blocked().target()
            """; actual();
    }
}`
	javaAssertConcreteSyntax(t, source)
	lines := javaTestLines(source)
	backend := javaPreparedSymbolTestBackend(lines)

	javaAssertSymbolTestFragment(t, backend, lines, "real()", "real")
	javaAssertSymbolTestFragment(t, backend, lines, "visible", "visible")
	javaAssertSymbolTestFragment(t, backend, lines, "actual()", "actual")

	blockedLine := javaSymbolTestLine(t, lines, "blocked().target()")
	if got, ok := backend.symbolOnLine(lines, blockedLine); ok {
		t.Fatalf("symbolOnLine(text-block line) = %q, true; want no symbol", got)
	}
}

func TestJavaNumericLiteralsDoNotBecomeNavigationSymbols(t *testing.T) {
	t.Parallel()

	const source = `class Numbers {
	    double hexFloat() {
	        return 0x1.deadp0;
	    }
	    double decimalExponent() {
	        return 1e10;
	    }
	    long hexadecimal() {
	        return 0xCAFE_BABEL;
	    }
	    int binary() {
	        return 0b1010_0101;
	    }
	    double leadingPoint() {
	        return .5D;
	    }
	}`
	javaAssertConcreteSyntax(t, source)
	lines := javaTestLines(source)
	backend := javaPreparedSymbolTestBackend(lines)
	for _, fragment := range []string{
		"0x1.deadp0", "1e10", "0xCAFE_BABEL", "0b1010_0101", ".5D",
	} {
		lineNo := javaSymbolTestLine(t, lines, fragment)
		if symbol, ok := backend.symbolOnLine(lines, lineNo); ok {
			t.Fatalf("symbolOnLine(%q) = %q, true; want no numeric symbol",
				fragment, symbol)
		}
		if symbol := bestSymbolOnLine(lines, lineNo, backend); symbol != "" {
			t.Fatalf("bestSymbolOnLine(%q) = %q, want empty", fragment, symbol)
		}
	}

	root := t.TempDir()
	writeFile(t, root, "Numbers.java", source)
	response, err := mustView(t, root).Inspect("Numbers.java:3", Options{
		Include: IncludeBoth,
		Return:  ReturnLocations,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Symbol != "" {
		t.Fatalf("Inspect numeric literal symbol = %q, want empty", response.Symbol)
	}
}

func TestJavaSymbolSelectionStreamsUnretainedMiddleLines(t *testing.T) {
	const paddingTokens = javaMaximumStoredLexicalTokens/2 + 256

	var source strings.Builder
	source.Grow(paddingTokens*4 + 128)
	source.WriteString("class Huge {\n")
	source.WriteString(strings.Repeat("; ", paddingTokens))
	source.WriteString("\n    middleCall();\n    0x1.deadp0;\n    ")
	source.WriteString(strings.Repeat("a + ", javaMaximumRecoveryHeaderTokens+64))
	source.WriteString("middlePriority(); ")
	// Keep the only invocation outside both bounded line flanks. The gap path
	// must select it online instead of merely moving the retained blind spot.
	source.WriteString(strings.Repeat("; ", javaMaximumRecoveryHeaderTokens+96))
	source.WriteString("\n")
	source.WriteString(strings.Repeat("; ", paddingTokens))
	source.WriteString("\n}\n")
	text := source.String()
	lines := javaTestLines(text)
	backend := javaPreparedSymbolTestBackend(lines)
	if backend.analysis == nil || !backend.analysis.lexed.truncated {
		t.Fatal("large fixture did not overflow retained Java tokens")
	}

	middleLine := javaSymbolTestLine(t, lines, "middleCall()")
	numericLine := javaSymbolTestLine(t, lines, "0x1.deadp0")
	longLine := javaSymbolTestLine(t, lines, "middlePriority()")
	for _, lineNo := range []int{middleLine, numericLine, longLine} {
		lineStart, lineEnd, ok := javaPhysicalLineByteRange(
			backend.analysis.source, backend.analysis.lineStarts, lineNo, lineNo,
		)
		if !ok || !javaStoredTokenGapIntersectsLine(
			backend.analysis.lexed.tokens, lineStart, lineEnd,
		) {
			t.Fatalf("line %d is not inside the retained-token gap", lineNo)
		}
	}
	if symbol, ok := backend.symbolOnLine(lines, middleLine); !ok || symbol != "middleCall" {
		t.Fatalf("symbolOnLine(unretained call) = %q, %v; want middleCall, true",
			symbol, ok)
	}
	if symbol := bestSymbolOnLine(lines, middleLine, backend); symbol != "middleCall" {
		t.Fatalf("bestSymbolOnLine(unretained call) = %q, want middleCall", symbol)
	}
	if symbol, ok := backend.symbolOnLine(lines, numericLine); ok {
		t.Fatalf("symbolOnLine(unretained numeric literal) = %q, true; want none", symbol)
	}
	if symbol := bestSymbolOnLine(lines, numericLine, backend); symbol != "" {
		t.Fatalf("bestSymbolOnLine(unretained numeric literal) = %q, want empty", symbol)
	}
	if symbol, ok := backend.symbolOnLine(lines, longLine); !ok || symbol != "middlePriority" {
		t.Fatalf("symbolOnLine(long unretained call line) = %q, %v; want middlePriority, true",
			symbol, ok)
	}
	if symbol := bestSymbolOnLine(lines, longLine, backend); symbol != "middlePriority" {
		t.Fatalf("bestSymbolOnLine(long unretained call line) = %q, want middlePriority",
			symbol)
	}

	root := t.TempDir()
	writeFile(t, root, "Huge.java", text)
	view := mustView(t, root)
	for _, test := range []struct {
		line int
		want string
	}{
		{line: middleLine, want: "middleCall"},
		{line: numericLine, want: ""},
		{line: longLine, want: "middlePriority"},
	} {
		response, err := view.Inspect(
			"Huge.java:"+strconv.Itoa(test.line),
			Options{Include: IncludeBoth, Return: ReturnLocations},
		)
		if err != nil {
			t.Fatal(err)
		}
		if response.Symbol != test.want {
			t.Fatalf("Inspect line %d symbol = %q, want %q",
				test.line, response.Symbol, test.want)
		}
	}
}

func TestJavaStreamedCallRejectsAnnotationRunsWithBoundedAllocations(t *testing.T) {
	const annotations = 2 << 10
	source := strings.Repeat("@A() ", annotations)
	input := newJavaUnicodeInput(source)
	allocations := testing.AllocsPerRun(1, func() {
		if symbol, ok := javaStreamedCallOnLine(&input, 0, len(source)); ok {
			panic("annotation name became call: " + symbol)
		}
	})
	if allocations > 64 {
		t.Fatalf("streamed call selector allocated %.0f times for rejected candidates",
			allocations)
	}
}

func TestJavaSymbolSelectionFallsBackForUnicodeEscapes(t *testing.T) {
	t.Parallel()

	const source = `class UnicodeFallback {
    Object inspect(Object input) {
        \u0066actory().\u0074arget();
		factory(inner()).build();
		factory(inner().x()).build();
		factory()[index].build();
		factory().items[index].build();
		Object terminalMember = root.middle.target;
		Object genericMember = Outer.Inner<String>.field;
		Runnable reference = Type\u003a\u003a\u003cString\u003etarget;
		Runnable annotatedReference = Type::<@A(flag = 1 > 0) String>target;
		java.util.function.Supplier<Type<String>> genericConstructor = Type\u003cString\u003e\u003a\u003anew;
		java.util.function.Supplier<Outer.Inner<String>> nestedGenericConstructor = Outer.Inner\u003cString\u003e\u003a\u003anew;
		java.util.function.Supplier<?> annotatedConstructor = Type<@A(flag = 1 > 0) String>::new;
        \u0076ar value = source;
        Object selected = switch (input) {
            case Point(int x) \u0077hen ready() -> x;
            case String s \u0077hen (condition) -> s;
			case Integer i \u0077hen when() -> i;
            default -> {
				if (ready())
					\u0079ield result;
				else
					\u0079ield fallback;
            }
        };
        var a = \u0076ar();
        var b = module();
		var c = record();
		var d = when();
		var e = obj.yield();
		obj.\u0074arget\u0028\u0029;
		int var = 1;
		var = source;
		Object direct =
			var -> var;
		Object parenthesized =
			(var) -> var;
		int yield = 1;
		if (ready())
			yield = result;
		if (ready())
			yield++;
		yield &= mask;
		yield[index] = value;
		return yield;
        return selected;
    }
}`
	lines := javaTestLines(source)
	backend := javaPreparedSymbolTestBackend(lines)
	if backend.analysis == nil || backend.analysis.tree != nil {
		t.Fatal("Unicode-escape source did not force the tree-free lexical path")
	}

	for _, test := range []struct {
		fragment string
		want     string
	}{
		{fragment: `\u0066actory().\u0074arget()`, want: `\u0074arget`},
		{fragment: "factory(inner()).build()", want: "build"},
		{fragment: "factory(inner().x()).build()", want: "build"},
		{fragment: "factory()[index].build()", want: "build"},
		{fragment: "factory().items[index].build()", want: "build"},
		{fragment: "root.middle.target", want: "target"},
		{fragment: "Outer.Inner<String>.field", want: "field"},
		{fragment: `Type\u003a\u003a\u003cString\u003etarget`, want: "target"},
		{fragment: "Type::<@A(flag = 1 > 0) String>target", want: "target"},
		{fragment: `Type\u003cString\u003e\u003a\u003anew`, want: "Type"},
		{fragment: `Outer.Inner\u003cString\u003e\u003a\u003anew`, want: "Inner"},
		{fragment: "Type<@A(flag = 1 > 0) String>::new", want: "Type"},
		{fragment: `\u0076ar value = source`, want: "value"},
		{fragment: `case Point(int x) \u0077hen ready()`, want: "ready"},
		{fragment: `case String s \u0077hen (condition)`, want: "condition"},
		{fragment: `case Integer i \u0077hen when()`, want: "when"},
		{fragment: `\u0079ield result`, want: "result"},
		{fragment: `\u0079ield fallback`, want: "fallback"},
		{fragment: `var a = \u0076ar()`, want: `\u0076ar`},
		{fragment: "var b = module()", want: "module"},
		{fragment: "var c = record()", want: "record"},
		{fragment: "var d = when()", want: "when"},
		{fragment: "var e = obj.yield()", want: "yield"},
		{fragment: `obj.\u0074arget\u0028\u0029`, want: `\u0074arget`},
		{fragment: "int var = 1", want: "var"},
		{fragment: "var = source", want: "var"},
		{fragment: "var -> var", want: "var"},
		{fragment: "(var) -> var", want: "var"},
		{fragment: "int yield = 1", want: "yield"},
		{fragment: "yield = result", want: "yield"},
		{fragment: "yield++", want: "yield"},
		{fragment: "yield &= mask", want: "yield"},
		{fragment: "yield[index]", want: "yield"},
		{fragment: "return yield", want: "yield"},
	} {
		javaAssertSymbolTestFragment(t, backend, lines, test.fragment, test.want)
	}

	const moduleSource = `module Host {
    requires \u004d;
	requires static N;
	requires transitive static O;
	requires static transitive P;
}`
	moduleLines := javaTestLines(moduleSource)
	moduleBackend := javaPreparedSymbolTestBackend(moduleLines)
	if moduleBackend.analysis == nil || moduleBackend.analysis.tree != nil {
		t.Fatal("escaped module source did not force the tree-free lexical path")
	}
	javaAssertSymbolTestFragment(t, moduleBackend, moduleLines, `requires \u004d`, `\u004d`)
	javaAssertSymbolTestFragment(t, moduleBackend, moduleLines, "requires static N", "N")
	javaAssertSymbolTestFragment(t, moduleBackend, moduleLines, "requires transitive static O", "O")
	javaAssertSymbolTestFragment(t, moduleBackend, moduleLines, "requires static transitive P", "P")

	const patternBoundarySource = `class PatternBoundary {
    Object inspect(Object input) {
        Object conditional = (input instanceof String) ? factory() : fallback();
        boolean matched = input instanceof Point(int x) && ready();
        return conditional;
    }
    void legacy(int selector) {
        switch (selector) {
            case 1:
                factory();
        }
    }
    String escaped = "\u0020";
}`
	patternLines := javaTestLines(patternBoundarySource)
	patternBackend := javaPreparedSymbolTestBackend(patternLines)
	if patternBackend.analysis == nil || patternBackend.analysis.tree != nil {
		t.Fatal("pattern-boundary source did not force the tree-free lexical path")
	}
	javaAssertSymbolTestFragment(t, patternBackend, patternLines, "input instanceof String", "factory")
	javaAssertSymbolTestFragment(t, patternBackend, patternLines, "input instanceof Point", "ready")
	javaAssertSymbolTestFragment(t, patternBackend, patternLines, "                factory()", "factory")
}

func TestJavaSymbolSelectionPrefersLexicalResultsOnRecoveryLines(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		source   string
		fragment string
		want     string
	}{
		{
			name:     "new module import",
			source:   "import module M;",
			fragment: "import module M",
			want:     "M",
		},
		{
			name: "malformed invocation",
			source: `class Broken {
    void inspect() {
        obj.target(];
    }
}`,
			fragment: "obj.target(]",
			want:     "target",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			lines := javaTestLines(test.source)
			backend := javaPreparedSymbolTestBackend(lines)
			lineNo := javaSymbolTestLine(t, lines, test.fragment)
			if backend.analysis == nil || !javaLineRangeTouchesRecovery(
				lineNo, lineNo, backend.analysis.recoveryPrefix,
			) {
				t.Fatal("fixture line did not enter syntax recovery")
			}
			javaAssertSymbolTestFragment(t, backend, lines, test.fragment, test.want)
		})
	}
}

func TestJavaLexicalConstructorReferencesStayWithinTheirTypeOperand(t *testing.T) {
	t.Parallel()

	const source = `cl\u0061ss References {
    void inspect() {
        java.util.function.Supplier<Outer.Inner<String>> reference =
            Outer
                .Inner<String>
                ::new;
    }
}`
	lines := javaTestLines(source)
	backend := javaPreparedSymbolTestBackend(lines)
	if backend.analysis == nil || backend.analysis.tree != nil {
		t.Fatal("escaped source did not force lexical symbol selection")
	}
	javaAssertSymbolTestFragment(t, backend, lines, "            Outer", "Outer")
	javaAssertSymbolTestFragment(t, backend, lines, "                .Inner", "Inner")
	referenceLine := javaSymbolTestLine(t, lines, "                ::new")
	if got, ok := backend.symbolOnLine(lines, referenceLine); ok {
		t.Fatalf("symbolOnLine(::new-only line) = %q, true; want no symbol", got)
	}
}

func TestJavaManyConstructorReferencesDoNotRescanEarlierPrefixes(t *testing.T) {
	t.Parallel()

	const references = 2 << 10
	lines := javaTestLines(javaConstructorReferenceStressSource(references))
	backend := javaPreparedSymbolTestBackend(lines)
	if backend.analysis == nil || backend.analysis.tree != nil {
		t.Fatal("escaped stress source did not force lexical symbol selection")
	}
	blankLine := javaOnlyBlankSymbolTestLine(t, lines)
	if got, ok := backend.symbolOnLine(lines, blankLine); ok {
		t.Fatalf("symbolOnLine(trailing blank) = %q, true; want no symbol", got)
	}
}

func TestJavaNestedGuardSymbolSelectionUsesSingleTreePass(t *testing.T) {
	t.Parallel()

	const depth = 48
	source := javaNestedGuardStressSource(depth)
	javaAssertConcreteSyntax(t, source)
	lines := javaTestLines(source)
	backend := javaPreparedSymbolTestBackend(lines)
	javaAssertSymbolTestFragment(t, backend, lines, "case Object leaf when leaf", "leaf")
	blankLine := javaOnlyBlankSymbolTestLine(t, lines)
	if got, ok := backend.symbolOnLine(lines, blankLine); ok {
		t.Fatalf("symbolOnLine(nested-guard trailing blank) = %q, true; want no symbol", got)
	}
}

func TestJavaSymbolSelectionRejectsInvalidLines(t *testing.T) {
	t.Parallel()

	lines := []string{"value;"}
	backend := javaPreparedSymbolTestBackend(lines)
	for _, lineNo := range []int{0, 2} {
		if got, ok := backend.symbolOnLine(lines, lineNo); ok {
			t.Fatalf("symbolOnLine(line %d) = %q, true; want no symbol", lineNo, got)
		}
	}
}

func BenchmarkJavaConstructorReferenceSymbolScaling(b *testing.B) {
	benchmark := func(b *testing.B, references int) {
		b.Helper()
		lines := javaTestLines(javaConstructorReferenceStressSource(references))
		backend := javaPreparedSymbolTestBackend(lines)
		blankLine := javaOnlyBlankSymbolTestLine(b, lines)
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			if symbol, ok := backend.symbolOnLine(lines, blankLine); ok {
				b.Fatalf("symbolOnLine(trailing blank) = %q, true", symbol)
			}
		}
	}
	b.Run("1024_references", func(b *testing.B) { benchmark(b, 1<<10) })
	b.Run("2048_references", func(b *testing.B) { benchmark(b, 2<<10) })
}

func BenchmarkJavaNestedGuardSymbolScaling(b *testing.B) {
	benchmark := func(b *testing.B, depth int) {
		b.Helper()
		lines := javaTestLines(javaNestedGuardStressSource(depth))
		backend := javaPreparedSymbolTestBackend(lines)
		blankLine := javaOnlyBlankSymbolTestLine(b, lines)
		b.ReportAllocs()
		b.ResetTimer()
		for range b.N {
			if symbol, ok := backend.symbolOnLine(lines, blankLine); ok {
				b.Fatalf("symbolOnLine(trailing blank) = %q, true", symbol)
			}
		}
	}
	b.Run("depth_32", func(b *testing.B) { benchmark(b, 32) })
	b.Run("depth_128", func(b *testing.B) { benchmark(b, 128) })
}

func javaConstructorReferenceStressSource(references int) string {
	var source strings.Builder
	source.Grow(references*64 + 128)
	source.WriteString("cl\\u0061ss ConstructorReferences {\n    void inspect() {\n")
	for index := range references {
		source.WriteString("        java.util.function.Supplier<ConstructorReferences> ref")
		source.WriteString(strconv.Itoa(index))
		source.WriteString(" = ConstructorReferences::new;\n")
	}
	source.WriteString("        \n    }\n}")
	return source.String()
}

func javaNestedGuardStressSource(depth int) string {
	var source strings.Builder
	source.Grow(depth*128 + 256)
	source.WriteString("class NestedGuards {\n    boolean inspect(Object value) {\n")
	source.WriteString("        return switch (value) {\n")
	for level := range depth {
		source.WriteString("            case Object level")
		source.WriteString(strconv.Itoa(level))
		source.WriteString(" when (switch (value) {\n")
	}
	source.WriteString("            case Object leaf when leaf != null -> true;\n")
	for range depth {
		source.WriteString("            default -> false;\n            }) -> true;\n")
	}
	source.WriteString("            default -> false;\n        };\n    }\n    \n}")
	return source.String()
}

func javaOnlyBlankSymbolTestLine(tb testing.TB, lines []string) int {
	tb.Helper()
	lineNo := 0
	for index, line := range lines {
		if strings.TrimSpace(line) != "" {
			continue
		}
		if lineNo != 0 {
			tb.Fatalf("fixture has multiple blank lines: %d and %d", lineNo, index+1)
		}
		lineNo = index + 1
	}
	if lineNo == 0 {
		tb.Fatal("fixture has no blank line")
	}
	return lineNo
}

func javaPreparedSymbolTestBackend(lines []string) javaLanguage {
	return newJavaLanguage().prepareSource(lines).(javaLanguage)
}

func javaAssertSymbolTestFragment(
	t *testing.T,
	backend javaLanguage,
	lines []string,
	fragment, want string,
) {
	t.Helper()
	lineNo := javaSymbolTestLine(t, lines, fragment)
	got, ok := backend.symbolOnLine(lines, lineNo)
	if !ok || got != want {
		t.Fatalf("symbolOnLine(%q) = %q, %v; want %q, true", lines[lineNo-1], got, ok, want)
	}
	if got := bestSymbolOnLine(lines, lineNo, backend); got != want {
		t.Fatalf("bestSymbolOnLine(%q) = %q; want %q", lines[lineNo-1], got, want)
	}
}

func javaSymbolTestLine(t *testing.T, lines []string, fragment string) int {
	t.Helper()
	lineNo := 0
	for index, line := range lines {
		if !strings.Contains(line, fragment) {
			continue
		}
		if lineNo != 0 {
			t.Fatalf("fragment %q occurs on multiple lines", fragment)
		}
		lineNo = index + 1
	}
	if lineNo == 0 {
		t.Fatalf("fragment %q does not occur in source", fragment)
	}
	return lineNo
}
