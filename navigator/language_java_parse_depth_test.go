package navigator

import (
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestJavaConcreteParserDelimiterDepthGate(t *testing.T) {
	t.Run("each-delimiter-kind", func(t *testing.T) {
		for _, delimiters := range []string{"()", "[]", "{}"} {
			t.Run(delimiters, func(t *testing.T) {
				atLimit := strings.Repeat(delimiters[:1], javaMaximumConcreteDelimiterDepth) +
					strings.Repeat(delimiters[1:], javaMaximumConcreteDelimiterDepth)
				if !javaConcreteDelimiterDepthEligible(lexJava(atLimit).tokens) {
					t.Fatalf("%q nest at limit was rejected", delimiters)
				}
				overLimit := delimiters[:1] + atLimit + delimiters[1:]
				if javaConcreteDelimiterDepthEligible(lexJava(overLimit).tokens) {
					t.Fatalf("%q nest over limit was accepted", delimiters)
				}
			})
		}
	})

	t.Run("mixed", func(t *testing.T) {
		var opens strings.Builder
		for index := range javaMaximumConcreteDelimiterDepth {
			opens.WriteByte("([{"[index%3])
		}
		openText := opens.String()
		closers := make([]byte, len(openText))
		for index := range openText {
			switch openText[len(openText)-1-index] {
			case '(':
				closers[index] = ')'
			case '[':
				closers[index] = ']'
			case '{':
				closers[index] = '}'
			}
		}
		if !javaConcreteDelimiterDepthEligible(lexJava(openText + string(closers)).tokens) {
			t.Fatal("mixed nest at limit was rejected")
		}
		if javaConcreteDelimiterDepthEligible(lexJava("(" + openText + string(closers) + ")").tokens) {
			t.Fatal("mixed nest over limit was accepted")
		}
	})

	t.Run("mismatches-do-not-hide-depth", func(t *testing.T) {
		if !javaConcreteDelimiterDepthEligible(lexJava("([)]").tokens) {
			t.Fatal("small malformed nest should remain eligible for concrete recovery")
		}
		malformed := strings.Repeat("([)]", javaMaximumConcreteDelimiterDepth+1)
		if javaConcreteDelimiterDepthEligible(lexJava(malformed).tokens) {
			t.Fatal("mismatched closers hid an over-depth nest")
		}
	})

	t.Run("retained-token-gap-is-ineligible", func(t *testing.T) {
		if javaConcreteDelimiterDepthEligible([]javaToken{{gap: true}}) {
			t.Fatal("retained-token gap was accepted for concrete parsing")
		}
	})

	t.Run("opaque-delimiters-are-ignored", func(t *testing.T) {
		opaque := strings.Repeat("([{", javaMaximumConcreteDelimiterDepth+1)
		source := "class Opaque { String value = \"" + opaque +
			"\"; /* " + opaque + " */ int goodField; }"
		analysis := analyzeJavaSource(source, 1)
		if analysis.tree == nil || len(analysis.recoverySpans) != 0 {
			t.Fatalf("opaque delimiters lost concrete syntax authority: tree=%v recovery=%#v",
				analysis.tree != nil, analysis.recoverySpans)
		}
	})

	t.Run("all-opaque-kinds-ignore-every-frontier", func(t *testing.T) {
		opaque := strings.Repeat("case default Type<Type ([{", javaMaximumConcreteLabelsPerBrace+1)
		source := "class Opaque {\n String ordinary = \"" + opaque + "\";\n" +
			" String block = \"\"\"\n" + opaque + "\n \"\"\";\n" +
			" char character = '<';\n // " + opaque + "\n /* " + opaque +
			" */ GoodType goodField;\n}"
		analysis := analyzeJavaSource(source, strings.Count(source, "\n")+1)
		if analysis.tree == nil || len(analysis.recoverySpans) != 0 {
			t.Fatalf("opaque frontiers lost concrete authority: tree=%v recovery=%#v",
				analysis.tree != nil, analysis.recoverySpans)
		}
	})

	t.Run("unicode-translation-precedes-gate", func(t *testing.T) {
		escapedLiteral := "class Opaque { String value = \"" +
			strings.Repeat(`\u003c`, javaMaximumConcreteGenericDepth+1) + "\"; }"
		literalLexed := lexJava(escapedLiteral)
		if !literalLexed.translatedEscapes ||
			!javaConcreteDelimiterDepthEligible(literalLexed.tokens) {
			t.Fatal("escaped delimiters inside a translated literal affected the gate")
		}

		escapedCode := "class Owner " +
			strings.Repeat(`\u007b`, javaMaximumConcreteDelimiterDepth+1)
		codeLexed := lexJava(escapedCode)
		if !codeLexed.translatedEscapes ||
			javaConcreteDelimiterDepthEligible(codeLexed.tokens) {
			t.Fatal("escaped code delimiters did not reach the depth gate")
		}
		if analysis := analyzeJavaSource(escapedCode, 1); analysis.tree != nil {
			t.Fatal("Unicode-translated source entered the raw concrete parser")
		}
	})
}

func TestJavaConcreteParserGenericDepthGate(t *testing.T) {
	t.Run("angle-boundary", func(t *testing.T) {
		atLimit := strings.Repeat("Type<", javaMaximumConcreteGenericDepth) +
			"Leaf" + strings.Repeat(">", javaMaximumConcreteGenericDepth)
		if !javaConcreteDelimiterDepthEligible(lexJava(atLimit).tokens) {
			t.Fatal("generic nest at limit was rejected")
		}
		overLimit := "Type<" + atLimit + ">"
		if javaConcreteDelimiterDepthEligible(lexJava(overLimit).tokens) {
			t.Fatal("generic nest over limit was accepted")
		}
	})

	t.Run("combined-closers", func(t *testing.T) {
		for _, source := range []string{
			"Type<A<B<C>>>",
			"Type<A<B<C> >>",
			"Type<A<B<C>> >",
		} {
			if !javaConcreteDelimiterDepthEligible(lexJava(source).tokens) {
				t.Fatalf("balanced generic source %q was rejected", source)
			}
		}
	})

	t.Run("balanced-source-boundary", func(t *testing.T) {
		atLimit := javaBalancedGenericDepthSource(javaMaximumConcreteGenericDepth)
		analysis := analyzeJavaSource(atLimit, 1)
		if analysis.tree == nil || len(analysis.recoverySpans) != 0 {
			t.Fatalf("at-limit generic source lost concrete authority: tree=%v recovery=%#v",
				analysis.tree != nil, analysis.recoverySpans)
		}
		if got, want := javaDefinitionSymbols(analysis.definitions),
			[]string{"Owner", "value", "goodField", "goodMethod"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("at-limit generic definitions = %#v, want %#v", got, want)
		}

		overLimit := javaBalancedGenericDepthSource(javaMaximumConcreteGenericDepth + 1)
		fallback := analyzeJavaSource(overLimit, 1)
		if fallback.tree != nil {
			t.Fatal("over-limit balanced generic source entered concrete parser")
		}
		if got, want := javaDefinitionSymbols(fallback.definitions),
			[]string{"Owner", "value", "goodField", "goodMethod"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("fallback generic definitions = %#v, want %#v", got, want)
		}
	})

	t.Run("ordinary-operators-reset-frontier", func(t *testing.T) {
		var source strings.Builder
		source.WriteString("class Comparisons {")
		for index := range javaMaximumConcreteGenericDepth * 2 {
			source.WriteString(" boolean value")
			source.WriteString(strconv.Itoa(index))
			source.WriteString(" = ")
			source.WriteString("left")
			source.WriteString(strconv.Itoa(index))
			source.WriteString(" < right")
			source.WriteString(strconv.Itoa(index))
			source.WriteString(";")
		}
		source.WriteString(" int shifted = left << 2; }")
		analysis := analyzeJavaSource(source.String(), 1)
		if analysis.tree == nil || len(analysis.recoverySpans) != 0 {
			t.Fatalf("ordinary operators lost concrete authority: tree=%v recovery=%#v",
				analysis.tree != nil, analysis.recoverySpans)
		}
	})

	t.Run("many-balanced-generic-members-use-bounded-fallback", func(t *testing.T) {
		build := func(methods int) string {
			var source strings.Builder
			source.WriteString("class ManyGenericMembers { <T> ManyGenericMembers(T value) {}")
			for index := range methods {
				source.WriteString(" <T> T method")
				source.WriteString(strconv.Itoa(index))
				source.WriteString("(T value) { return value; }")
			}
			source.WriteString(" }")
			return source.String()
		}

		atLimit := build(javaMaximumConcreteGenericDepth - 1)
		analysis := analyzeJavaSource(atLimit, 1)
		if analysis.tree == nil || len(analysis.recoverySpans) != 0 {
			t.Fatalf("at-limit generic members lost concrete authority: tree=%v recovery=%#v",
				analysis.tree != nil, analysis.recoverySpans)
		}

		overLimit := build(javaMaximumConcreteGenericDepth)
		fallback := analyzeJavaSource(overLimit, 1)
		if fallback.tree != nil {
			t.Fatal("over-limit generic members entered concrete parser")
		}
		if symbols := javaDefinitionSymbols(fallback.definitions); !slices.Contains(symbols, "ManyGenericMembers") ||
			!slices.Contains(symbols, "method127") {
			t.Fatalf("generic-member fallback definitions = %#v", symbols)
		}
	})

	t.Run("balanced-generic-expression-groups-remain-capped", func(t *testing.T) {
		atLimit := javaBalancedCloseGenericFrontierSource(javaMaximumConcreteGenericDepth)
		if !javaConcreteDelimiterDepthEligible(lexJava(atLimit).tokens) {
			t.Fatal("balanced-close generic frontier at limit was rejected")
		}
		overLimit := javaBalancedCloseGenericFrontierSource(
			javaMaximumConcreteGenericDepth + 1,
		)
		if javaConcreteDelimiterDepthEligible(lexJava(overLimit).tokens) {
			t.Fatal("generic closers erased an over-limit expression frontier")
		}
		fallback := analyzeJavaSource(overLimit, strings.Count(overLimit, "\n")+1)
		if fallback.tree != nil {
			t.Fatal("balanced-close generic frontier entered concrete parser")
		}
		for _, symbol := range []string{"Owner", "goodField", "goodMethod"} {
			if !slices.Contains(javaDefinitionSymbols(fallback.definitions), symbol) {
				t.Fatalf("balanced-close fallback lost tail %q: %#v",
					symbol, javaDefinitionSymbols(fallback.definitions))
			}
		}
	})
}

func TestJavaConcreteParserSwitchLabelFrontierGate(t *testing.T) {
	t.Run("missing-separators", func(t *testing.T) {
		atLimit := javaUnresolvedSwitchLabelDepthSource(javaMaximumConcreteLabelsPerBrace)
		if !javaConcreteDelimiterDepthEligible(lexJava(atLimit).tokens) {
			t.Fatal("pending switch labels at limit were rejected")
		}
		overLimit := javaUnresolvedSwitchLabelDepthSource(javaMaximumConcreteLabelsPerBrace + 1)
		if javaConcreteDelimiterDepthEligible(lexJava(overLimit).tokens) {
			t.Fatal("pending switch labels over limit were accepted")
		}
		analysis := analyzeJavaSource(
			overLimit, strings.Count(overLimit, "\n")+1,
		)
		if analysis.tree != nil {
			t.Fatal("pending switch labels over limit entered concrete parser")
		}
		for _, symbol := range []string{"Owner", "goodField", "goodMethod"} {
			if !slices.Contains(javaDefinitionSymbols(analysis.definitions), symbol) {
				t.Fatalf("pending-label fallback lost tail %q: %#v",
					symbol, javaDefinitionSymbols(analysis.definitions))
			}
		}
	})

	t.Run("nested-recovery-braces-do-not-reset-owner", func(t *testing.T) {
		atLimit := javaNestedBraceSwitchLabelFrontierSource(javaMaximumConcreteLabelsPerBrace)
		if !javaConcreteDelimiterDepthEligible(lexJava(atLimit).tokens) {
			t.Fatal("nested-brace label frontier at limit was rejected")
		}
		overLimit := javaNestedBraceSwitchLabelFrontierSource(
			javaMaximumConcreteLabelsPerBrace + 1,
		)
		if javaConcreteDelimiterDepthEligible(lexJava(overLimit).tokens) {
			t.Fatal("nested recovery braces erased an over-limit label frontier")
		}
		analysis := analyzeJavaSource(overLimit, strings.Count(overLimit, "\n")+1)
		if analysis.tree != nil {
			t.Fatal("nested-brace label frontier entered concrete parser")
		}
		for _, symbol := range []string{"Owner", "goodField", "goodMethod"} {
			if !slices.Contains(javaDefinitionSymbols(analysis.definitions), symbol) {
				t.Fatalf("nested-brace label fallback lost tail %q: %#v",
					symbol, javaDefinitionSymbols(analysis.definitions))
			}
		}
	})

	t.Run("resolved-labels-at-limit-stay-concrete", func(t *testing.T) {
		var source strings.Builder
		source.WriteString("class Labels { int value = switch (input) {\n")
		for index := range javaMaximumConcreteLabelsPerBrace - 1 {
			source.WriteString("case ")
			source.WriteString(strconv.Itoa(index))
			source.WriteString(" -> ")
			source.WriteString(strconv.Itoa(index))
			source.WriteString(";\n")
		}
		source.WriteString("default -> -1;\n}; }")
		analysis := analyzeJavaSource(
			source.String(), strings.Count(source.String(), "\n")+1,
		)
		if analysis.tree == nil || len(analysis.recoverySpans) != 0 {
			t.Fatalf("resolved labels lost concrete authority: tree=%v recovery=%#v",
				analysis.tree != nil, analysis.recoverySpans)
		}
	})

	t.Run("separate-switch-bodies-have-separate-budgets", func(t *testing.T) {
		var source strings.Builder
		source.WriteString("class SeparateSwitches {")
		for index := range javaMaximumConcreteLabelsPerBrace + 32 {
			source.WriteString(" int method")
			source.WriteString(strconv.Itoa(index))
			source.WriteString("(int value) { return switch (value) { default -> ")
			source.WriteString(strconv.Itoa(index))
			source.WriteString("; }; }")
		}
		source.WriteString(" }")
		analysis := analyzeJavaSource(source.String(), 1)
		if analysis.tree == nil || len(analysis.recoverySpans) != 0 {
			t.Fatalf("separate switch bodies lost concrete authority: tree=%v recovery=%#v",
				analysis.tree != nil, analysis.recoverySpans)
		}
	})

	t.Run("ternary-guard-and-combined-label", func(t *testing.T) {
		const source = `class Labels {
	    Object check(Object value) {
	        return switch (value) {
	            case String text when ready ? first : second -> text;
	            case null, default -> null;
	        };
	    }
}`
		analysis := analyzeJavaSource(source, strings.Count(source, "\n")+1)
		if analysis.tree == nil || len(analysis.recoverySpans) != 0 {
			t.Fatalf("valid guarded labels lost concrete authority: tree=%v recovery=%#v",
				analysis.tree != nil, analysis.recoverySpans)
		}
	})
}

func TestJavaConcreteParserDelimiterDepthBoundary(t *testing.T) {
	for _, fixture := range []struct {
		name   string
		source func(int) string
	}{
		{name: "parentheses", source: javaBalancedParenthesisDepthSource},
		{name: "brackets", source: javaBalancedBracketDepthSource},
		{name: "array-braces", source: javaBalancedArrayBraceDepthSource},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			atLimit := fixture.source(javaMaximumConcreteDelimiterDepth - 1)
			analysis := analyzeJavaSource(atLimit, 1)
			if analysis.tree == nil || len(analysis.recoverySpans) != 0 {
				t.Fatalf("at-limit source lost concrete syntax authority: tree=%v recovery=%#v",
					analysis.tree != nil, analysis.recoverySpans)
			}
			if got, want := javaDefinitionSymbols(analysis.definitions),
				[]string{"Owner", "value", "goodField", "goodMethod"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("at-limit definitions = %#v, want %#v", got, want)
			}

			overLimit := fixture.source(javaMaximumConcreteDelimiterDepth)
			if tree, ok := parseJavaSyntax(overLimit); ok || tree != nil {
				t.Fatal("over-depth balanced source entered concrete parser")
			}
			fallback := analyzeJavaSource(overLimit, 1)
			if fallback.tree != nil {
				t.Fatal("over-depth balanced source retained concrete syntax authority")
			}
			if got, want := javaDefinitionSymbols(fallback.definitions),
				[]string{"Owner", "value", "goodField", "goodMethod"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("fallback definitions = %#v, want %#v", got, want)
			}
		})
	}
}

func TestJavaConcreteParserMalformedArrayDepthFallbackRecoversTail(t *testing.T) {
	source := javaUnclosedArrayDeclarationDepthSource(javaMaximumConcreteDelimiterDepth + 1)
	if tree, ok := parseJavaSyntax(source); ok || tree != nil {
		t.Fatal("over-depth malformed arrays entered concrete parser")
	}
	analysis := analyzeJavaSource(source, strings.Count(source, "\n")+1)
	if analysis.tree != nil {
		t.Fatal("over-depth malformed arrays retained concrete syntax authority")
	}
	symbols := javaDefinitionSymbols(analysis.definitions)
	for _, symbol := range []string{"Owner", "goodField", "goodMethod"} {
		if !slices.Contains(symbols, symbol) {
			t.Fatalf("malformed-array definitions = %#v, missing tail %q", symbols, symbol)
		}
	}
}

func TestJavaConcreteParserMalformedGenericDepthFallbackRecoversTail(t *testing.T) {
	source := javaUnclosedGenericDepthSource(javaMaximumConcreteGenericDepth + 1)
	if tree, ok := parseJavaSyntax(source); ok || tree != nil {
		t.Fatal("over-depth malformed generics entered concrete parser")
	}
	analysis := analyzeJavaSource(source, strings.Count(source, "\n")+1)
	if analysis.tree != nil {
		t.Fatal("over-depth malformed generics retained concrete syntax authority")
	}
	symbols := javaDefinitionSymbols(analysis.definitions)
	for _, symbol := range []string{"Owner", "goodField", "goodMethod"} {
		if !slices.Contains(symbols, symbol) {
			t.Fatalf("malformed-generic definitions = %#v, missing tail %q", symbols, symbol)
		}
	}
}

func TestJavaConcreteParserGenericFrontierSurvivesBalancedBraces(t *testing.T) {
	atLimit := javaBalancedBraceGenericFrontierSource(javaMaximumConcreteGenericDepth)
	if !javaConcreteDelimiterDepthEligible(lexJava(atLimit).tokens) {
		t.Fatal("balanced-brace generic frontier at limit was rejected")
	}
	overLimit := javaBalancedBraceGenericFrontierSource(javaMaximumConcreteGenericDepth + 1)
	if javaConcreteDelimiterDepthEligible(lexJava(overLimit).tokens) {
		t.Fatal("balanced braces erased an over-limit generic frontier")
	}
	analysis := analyzeJavaSource(overLimit, strings.Count(overLimit, "\n")+1)
	if analysis.tree != nil {
		t.Fatal("balanced-brace generic frontier entered concrete parser")
	}
	for _, symbol := range []string{"Owner", "goodField", "goodMethod"} {
		if !slices.Contains(javaDefinitionSymbols(analysis.definitions), symbol) {
			t.Fatalf("balanced-brace fallback lost tail %q: %#v",
				symbol, javaDefinitionSymbols(analysis.definitions))
		}
	}
}

func TestJavaConcreteParserDepthGateStorageIsConstant(t *testing.T) {
	fixtures := [][]javaToken{
		lexJava(strings.Repeat("{", javaMaximumConcreteDelimiterDepth+1)).tokens,
		lexJava(strings.Repeat("{", javaMaximumConcreteDelimiterDepth*32)).tokens,
		lexJava(strings.Repeat("Type<", javaMaximumConcreteGenericDepth+1)).tokens,
		lexJava(javaBalancedBraceGenericFrontierSource(
			javaMaximumConcreteGenericDepth + 1,
		)).tokens,
		lexJava(javaBalancedCloseGenericFrontierSource(
			javaMaximumConcreteGenericDepth + 1,
		)).tokens,
		lexJava(javaUnresolvedSwitchLabelDepthSource(
			javaMaximumConcreteLabelsPerBrace + 1,
		)).tokens,
		lexJava(javaNestedBraceSwitchLabelFrontierSource(
			javaMaximumConcreteLabelsPerBrace + 1,
		)).tokens,
	}
	assertRejected := func(tokens []javaToken) {
		if javaConcreteDelimiterDepthEligible(tokens) {
			panic("over-depth delimiter sequence accepted")
		}
	}
	for index, fixture := range fixtures {
		if allocations := testing.AllocsPerRun(100, func() {
			assertRejected(fixture)
		}); allocations != 0 {
			t.Fatalf("depth gate fixture %d allocations = %g, want zero", index, allocations)
		}
	}

	for _, depth := range []int{
		javaMaximumConcreteDelimiterDepth + 1,
		javaMaximumConcreteDelimiterDepth * 4,
	} {
		source := javaUnclosedArrayDeclarationDepthSource(depth)
		analysis := analyzeJavaSource(source, strings.Count(source, "\n")+1)
		if analysis.tree != nil {
			t.Fatalf("depth %d entered concrete parser", depth)
		}
		if !slices.Contains(javaDefinitionSymbols(analysis.definitions), "goodMethod") {
			t.Fatalf("depth %d lost tail method", depth)
		}
	}

	for name, source := range map[string]string{
		"generic": javaUnclosedGenericDepthSource(javaMaximumConcreteGenericDepth * 4),
		"labels":  javaUnresolvedSwitchLabelDepthSource(javaMaximumConcreteLabelsPerBrace * 4),
	} {
		analysis := analyzeJavaSource(source, strings.Count(source, "\n")+1)
		if analysis.tree != nil {
			t.Fatalf("large %s frontier entered concrete parser", name)
		}
		if !slices.Contains(javaDefinitionSymbols(analysis.definitions), "goodMethod") {
			t.Fatalf("large %s frontier lost tail method", name)
		}
	}
}

func javaBalancedParenthesisDepthSource(depth int) string {
	return "class Owner { int value = " + strings.Repeat("(", depth) + "0" +
		strings.Repeat(")", depth) + "; GoodType goodField; void goodMethod() {} }"
}

func javaBalancedBracketDepthSource(depth int) string {
	return "class Owner { int value = " + strings.Repeat("values[", depth) + "0" +
		strings.Repeat("]", depth) + "; GoodType goodField; void goodMethod() {} }"
}

func javaBalancedArrayBraceDepthSource(depth int) string {
	return "class Owner { int" + strings.Repeat("[]", depth) + " value = " +
		strings.Repeat("{", depth) + "0" + strings.Repeat("}", depth) +
		"; GoodType goodField; void goodMethod() {} }"
}

func javaBalancedGenericDepthSource(depth int) string {
	return "class Owner { Type" + strings.Repeat("<Type", depth) +
		strings.Repeat(">", depth) +
		" value; GoodType goodField; void goodMethod() {} }"
}

func javaUnclosedArrayDeclarationDepthSource(depth int) string {
	var source strings.Builder
	source.WriteString("class Owner {\n")
	for index := range depth {
		source.WriteString(" Object broken")
		source.WriteString(strconv.Itoa(index))
		source.WriteString(" = new int[] {\n")
	}
	source.WriteString(" GoodType goodField;\n void goodMethod() {}\n}\n")
	return source.String()
}

func javaUnclosedGenericDepthSource(depth int) string {
	return "class Owner {\n Object broken = new Type" + strings.Repeat("<Type", depth) +
		";\n GoodType goodField;\n void goodMethod() {}\n}\n"
}

func javaBalancedBraceGenericFrontierSource(depth int) string {
	return "class Owner {\n Object broken = " +
		strings.Repeat("new Type<Type { } ", depth) +
		"value;\n GoodType goodField;\n void goodMethod() {}\n}\n"
}

func javaBalancedCloseGenericFrontierSource(depth int) string {
	return "class Owner {\n Object broken = " +
		strings.Repeat("new Type<Type> ", depth) +
		"value;\n GoodType goodField;\n void goodMethod() {}\n}\n"
}

func javaUnresolvedSwitchLabelDepthSource(depth int) string {
	return "class Owner {\n Object value = switch (input) {\n " +
		strings.Repeat("case Type item when ready\n ", depth) +
		"};\n GoodType goodField;\n void goodMethod() {}\n}\n"
}

func javaNestedBraceSwitchLabelFrontierSource(depth int) string {
	return "class Owner {\n Object value = switch (input) {\n " +
		strings.Repeat("{ case Type item when ready }\n ", depth) +
		"};\n GoodType goodField;\n void goodMethod() {}\n}\n"
}

func BenchmarkJavaConcreteParserLabelBraceFrontierFallback(b *testing.B) {
	for _, depth := range []int{128, 256, 512, 1024, 2048, 4096} {
		source := javaNestedBraceSwitchLabelFrontierSource(depth)
		b.Run(strconv.Itoa(depth), func(b *testing.B) {
			b.ReportAllocs()
			for b.Loop() {
				_ = analyzeJavaSource(source, 1)
			}
		})
	}
}
