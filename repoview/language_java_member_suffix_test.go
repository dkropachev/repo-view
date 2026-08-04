package repoview

import (
	"slices"
	"strings"
	"testing"
)

func TestJavaMalformedMemberPrefixRetainsValidMemberSuffix(t *testing.T) {
	prefixes := []struct {
		name   string
		source string
	}{
		{"trailing-dot", "Object broken = String.\n"},
		{"unclosed-call", "Object broken = call(\n"},
		{"unclosed-allocation", "Object broken = new Type(\n"},
		{"unclosed-ternary", "Object broken = value ?\n"},
		{"unclosed-switch-rule", "Object broken = switch (x) { case 1 ->\n"},
		{"unclosed-parenthesized-expression", "Object broken = (\n"},
		{"unclosed-array-initializer", "Object broken = new int[] {\n"},
		{"unclosed-method-parameters", "void broken(\n"},
		{"incomplete-throws", "void broken() throws\n"},
		{"incomplete-type-parameters", "<T extends Comparable<T>\n"},
		{"unclosed-annotation", "@Anno(\n"},
		{"unclosed-record-header", "record Broken(\n"},
		{"unclosed-class-type-parameters", "class Broken<T\n"},
		{"unclosed-static-initializer", "static { broken(\n"},
	}
	suffixes := []struct {
		name   string
		source string
	}{
		{
			name:   "primitive-and-void",
			source: "int goodField;\nvoid goodMethod() {}\n",
		},
		{
			name: "reference-types",
			source: "GoodType goodField;\n" +
				"GoodResult goodMethod() { return null; }\n",
		},
	}

	for _, prefix := range prefixes {
		for _, suffix := range suffixes {
			t.Run(prefix.name+"/"+suffix.name, func(t *testing.T) {
				source := "class Owner {\n" + prefix.source + suffix.source + "}\n"
				analysis := analyzeJavaSource(source, strings.Count(source, "\n")+1)
				javaAssertMemberSuffixSymbols(t, analysis, "Owner", "goodField", "goodMethod")
			})
		}
	}
}

func TestJavaConsecutiveMalformedMembersRetainValidSuffix(t *testing.T) {
	for _, test := range []struct {
		name   string
		source string
	}{
		{
			name: "flat-headers",
			source: `class Owner {
Object first = call(
Object second = String.
@Anno(
class Broken<T
GoodType goodField;
GoodResult goodMethod() { return null; }
}`,
		},
		{
			name: "stolen-brace-owner",
			source: `class Owner {
Object first = new int[] {
Object second = switch (value) { case 1 ->
static { broken(
GoodType goodField;
GoodResult goodMethod() { return null; }
}`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			analysis := analyzeJavaSource(test.source, strings.Count(test.source, "\n")+1)
			javaAssertMemberSuffixSymbols(t, analysis, "Owner", "goodField", "goodMethod")
		})
	}
}

func TestJavaMemberSuffixRecoveryDoesNotPromoteCleanContinuationsOrLocals(t *testing.T) {
	const source = `@interface Anno { int value(); }
class Owner<T extends Comparable<T>> {
    @Anno(
        value = 1
    )
    T annotated;

    Object fluent = String
        .valueOf(1);
    Object ternary = condition
        ? first
        : second;
    Object invoked = call(
        first,
        second
    );

    <R extends Comparable<R>>
    R generic(
        R parameter
    ) { return parameter; }

    record Nested(
        int component
    ) {}

    int[] array = new int[] {
        1,
        2
    };
    Object switched = switch (mode) {
        case 1 -> first;
        default -> second;
    };
    Object anonymous = new Object() {
        int nestedField;
        void nestedMethod() {}
    };

    static {
        int staticLocal;
        use(staticLocal);
    }
    void method() {
        int methodLocal;
        use(methodLocal);
    }
}`
	javaAssertConcreteSyntax(t, source)
	analysis := analyzeJavaSource(source, strings.Count(source, "\n")+1)
	javaAssertMemberSuffixSymbols(
		t, analysis,
		"Anno", "value", "Owner", "annotated", "fluent", "ternary", "invoked",
		"generic", "Nested", "component", "array", "switched", "anonymous", "method",
	)
	got := javaDefinitionSymbols(analysis.definitions)
	for _, forbidden := range []string{"parameter", "staticLocal", "methodLocal"} {
		if slices.Contains(got, forbidden) {
			t.Errorf("clean nested local %q became a source definition: %#v", forbidden, got)
		}
	}
}

func TestJavaMalformedLocalTypesDoNotAnchorMemberSuffixRecovery(t *testing.T) {
	tests := []struct {
		name      string
		localType string
		required  []string
	}{
		{
			name:      "class",
			localType: "class Local {}",
			required:  []string{"Owner", "Local"},
		},
		{
			name:      "record",
			localType: "record Local(int component) {}",
			required:  []string{"Owner", "Local", "component"},
		},
		{
			name:      "enum",
			localType: "enum Local { ONE }",
			required:  []string{"Owner", "Local", "ONE"},
		},
		{
			name:      "interface",
			localType: "interface Local { void member(); }",
			required:  []string{"Owner", "Local", "member"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			source := "class Owner {\n" +
				"    static { broken(\n" +
				"        " + test.localType + "\n" +
				"        GoodType local;\n" +
				"}\n"
			analysis := analyzeJavaSource(source, strings.Count(source, "\n")+1)
			javaAssertMemberSuffixSymbols(t, analysis, test.required...)
			if got := javaDefinitionSymbols(analysis.definitions); slices.Contains(got, "local") {
				t.Fatalf(
					"local type anchored malformed member suffix: definitions=%#v recovery=%#v",
					got, analysis.recoverySpans,
				)
			}
		})
	}
}

func TestJavaCompactRecordConstructorAnchorsRecoveredMemberSuffix(t *testing.T) {
	const source = `record Owner(int component) {
    Object broken = new int[] {
    Owner {}
    GoodType goodField;
}`
	analysis := analyzeJavaSource(source, strings.Count(source, "\n")+1)
	javaAssertMemberSuffixSymbols(t, analysis, "Owner", "component", "goodField")
}

func TestJavaExpressionMemberPrefixesRetainUnambiguousFieldSuffix(t *testing.T) {
	prefixes := []struct {
		name   string
		source string
	}{
		{"array-initializer", "Object broken = new int[] {\n"},
		{"switch-rule", "Object broken = switch (value) { case 1 ->\n"},
	}
	for _, prefix := range prefixes {
		t.Run(prefix.name+"/field", func(t *testing.T) {
			source := "class Owner {\n" + prefix.source + "GoodType goodField;\n}\n"
			analysis := analyzeJavaSource(source, strings.Count(source, "\n")+1)
			javaAssertMemberSuffixSymbols(t, analysis, "Owner", "goodField")
		})
		t.Run(prefix.name+"/nested-type", func(t *testing.T) {
			source := "class Owner {\n" + prefix.source +
				"class Nested { GoodType nestedField; }\n" +
				"GoodType goodField;\n}\n"
			analysis := analyzeJavaSource(source, strings.Count(source, "\n")+1)
			javaAssertMemberSuffixSymbols(
				t, analysis, "Owner", "Nested", "nestedField", "goodField",
			)
		})
	}
}

func TestJavaExpressionMemberRecoveryDoesNotPromoteMethodLocals(t *testing.T) {
	prefixes := []struct {
		name   string
		source string
	}{
		{"array-initializer", "Object broken = new int[] {\n"},
		{"switch-rule", "Object broken = switch (value) { case 1 ->\n"},
	}
	for _, prefix := range prefixes {
		t.Run(prefix.name, func(t *testing.T) {
			source := "class Owner {\nvoid method() {\n" + prefix.source +
				"GoodType local;\n}\n"
			analysis := analyzeJavaSource(source, strings.Count(source, "\n")+1)
			javaAssertMemberSuffixSymbols(t, analysis, "Owner", "method")
			if got := javaDefinitionSymbols(analysis.definitions); slices.Contains(got, "local") {
				t.Fatalf(
					"method-local expression became member: definitions=%#v recovery=%#v",
					got, analysis.recoverySpans,
				)
			}
		})
	}
}

func TestJavaNestedExpressionMemberPrefixesRetainFieldSuffix(t *testing.T) {
	prefixes := []struct {
		name   string
		source string
	}{
		{"nested-array", "Object broken = new Object[][] {\n{\n"},
		{
			"switch-then-array",
			"Object broken = switch (mode) { case 1 -> new Object[] {\n",
		},
		{
			"array-then-switch",
			"Object broken = new Object[] { switch (mode) { case 1 ->\n",
		},
		{"three-arrays", "Object broken = new Object[][][] {\n{\n{\n"},
	}
	for _, prefix := range prefixes {
		t.Run(prefix.name, func(t *testing.T) {
			source := "class Owner {\n" + prefix.source + "GoodType goodField;\n}\n"
			analysis := analyzeJavaSource(source, strings.Count(source, "\n")+1)
			javaAssertMemberSuffixSymbols(t, analysis, "Owner", "goodField")
		})
	}
}

func TestJavaNestedExpressionMemberRecoveryStopsAtMethodBody(t *testing.T) {
	const source = `class Owner {
    void method() {
        Object broken = new Object[][] {
        {
        GoodType local;
    }`
	analysis := analyzeJavaSource(source, strings.Count(source, "\n")+1)
	javaAssertMemberSuffixSymbols(t, analysis, "Owner", "method")
	if got := javaDefinitionSymbols(analysis.definitions); slices.Contains(got, "local") {
		t.Fatalf(
			"nested expression crossed method boundary: definitions=%#v recovery=%#v",
			got, analysis.recoverySpans,
		)
	}
}

func TestJavaSwitchColonGroupLambdasDoNotAnchorMemberRecovery(t *testing.T) {
	for _, test := range []struct {
		name   string
		lambda string
	}{
		{"expression", "Runnable task = item -> item;"},
		{"block", "Runnable task = item -> { use(item); };"},
	} {
		t.Run(test.name, func(t *testing.T) {
			source := "class Owner {\n" +
				"Object value = switch (mode) {\n" +
				"case 1:\n" + test.lambda + "\n" +
				"GoodType local;\n};\n}\n"
			analysis := analyzeJavaSource(source, strings.Count(source, "\n")+1)
			javaAssertMemberSuffixSymbols(t, analysis, "Owner", "value")
			got := javaDefinitionSymbols(analysis.definitions)
			for _, symbol := range []string{"task", "local"} {
				if slices.Contains(got, symbol) {
					t.Fatalf(
						"switch local %q became member: definitions=%#v recovery=%#v",
						symbol, got, analysis.recoverySpans,
					)
				}
			}
		})
	}
}

func TestJavaSwitchTernaryGuardRetainsFieldSuffix(t *testing.T) {
	const concrete = `class Check {
    Object check(Object value) {
        return switch (value) {
            case String text when ready ? first : second -> text;
            default -> null;
        };
    }
}`
	javaAssertConcreteSyntax(t, concrete)

	const source = `class Owner {
Object broken = switch (value) {
case String text when ready ? first : second ->
GoodType goodField;
}`
	analysis := analyzeJavaSource(source, strings.Count(source, "\n")+1)
	javaAssertMemberSuffixSymbols(t, analysis, "Owner", "goodField")
}

func TestJavaCompletedSwitchRulesRetainFollowingFieldSuffix(t *testing.T) {
	for _, rule := range []struct {
		name   string
		source string
	}{
		{"expression", "existing;"},
		{"lambda-expression", "item -> item;"},
		{"throw", "throw failure;"},
	} {
		t.Run(rule.name, func(t *testing.T) {
			source := "class Owner {\n" +
				"Object broken = switch (value) {\n" +
				"case 1 -> " + rule.source + "\n" +
				"GoodType goodField;\n}\n"
			analysis := analyzeJavaSource(source, strings.Count(source, "\n")+1)
			javaAssertMemberSuffixSymbols(t, analysis, "Owner", "goodField")
		})
	}
}

func TestJavaRecoveredFieldsAcceptEveryFieldOnlyModifier(t *testing.T) {
	for _, modifier := range []string{
		"volatile", "transient", "volatile transient", "transient volatile",
	} {
		t.Run(strings.ReplaceAll(modifier, " ", "-"), func(t *testing.T) {
			suffix := modifier + " GoodType goodField;\nvoid anchor() {}\n"
			javaAssertConcreteSyntax(t, "class Check {\n"+suffix+"}\n")
			for _, classKeyword := range []struct {
				name   string
				source string
			}{
				{"tree", "class"},
				{"tree-free", `cl\u0061ss`},
			} {
				t.Run(classKeyword.name, func(t *testing.T) {
					source := classKeyword.source +
						" Owner {\nObject broken = new int[] {\n" + suffix + "}\n"
					analysis := analyzeJavaSource(source, strings.Count(source, "\n")+1)
					javaAssertMemberSuffixSymbols(
						t, analysis, "Owner", "goodField", "anchor",
					)
				})
			}
		})
	}
}

func TestJavaMalformedMemberSuffixRecoveryStaysLinearPastHeaderBudget(t *testing.T) {
	source := func(repeats int) string {
		return "class Owner {\nObject broken = call(\n" +
			strings.Repeat("value + ", repeats) +
			"\nGoodType goodField;\nGoodResult goodMethod() { return null; }\n}\n"
	}
	analyze := func(text string) {
		analysis := analyzeJavaSource(text, strings.Count(text, "\n")+1)
		got := javaDefinitionSymbols(analysis.definitions)
		for _, symbol := range []string{"Owner", "goodField", "goodMethod"} {
			if !slices.Contains(got, symbol) {
				panic("valid member suffix was lost past the recovery budget")
			}
		}
	}

	small := source(javaMaximumRecoveryHeaderTokens / 2)
	large := source(javaMaximumRecoveryHeaderTokens * 2)
	analyze(small)
	analyze(large)
	smallAllocations := testing.AllocsPerRun(1, func() { analyze(small) })
	largeAllocations := testing.AllocsPerRun(1, func() { analyze(large) })
	// The large fixture has four times as many malformed-prefix tokens. Leave
	// headroom for parser growth while rejecting an overlapping suffix rescan.
	if largeAllocations > smallAllocations*6+4096 {
		t.Fatalf(
			"member-suffix allocations grew superlinearly: small=%.0f large=%.0f",
			smallAllocations, largeAllocations,
		)
	}
}

func TestJavaMemberInitializerPrefixRecoveryHandlesRepeatedBodies(t *testing.T) {
	const repeats = 64
	source := `cl\u0061ss Owner {
` + strings.Repeat(
		"Object value = switch (mode) { default -> null; }\n",
		repeats,
	) + "void anchor() {}\n"
	analysis := analyzeJavaSource(source, strings.Count(source, "\n")+1)
	if analysis.tree != nil {
		t.Fatal("Unicode translated fixture unexpectedly built concrete tree")
	}
	valueCount := 0
	for _, definition := range analysis.definitions {
		if definition.symbol == "value" {
			valueCount++
		}
	}
	if valueCount != repeats {
		t.Fatalf(
			"repeated initializer fields = %d, want %d; definitions=%#v",
			valueCount, repeats, javaDefinitionSymbols(analysis.definitions),
		)
	}
	javaAssertMemberSuffixSymbols(t, analysis, "Owner", "anchor")
}

func TestJavaMemberInitializerPrefixRecoveryStaysBoundedPastHeaderLimit(t *testing.T) {
	source := func(annotations int) string {
		return `cl\u0061ss Owner {
` + strings.Repeat("@A ", annotations) +
			"Object value = switch (mode) { default -> null; }\n" +
			"void anchor() {}\n"
	}
	analyze := func(text string) {
		analysis := analyzeJavaSource(text, strings.Count(text, "\n")+1)
		if analysis.tree != nil {
			panic("Unicode translated fixture unexpectedly built concrete tree")
		}
		got := javaDefinitionSymbols(analysis.definitions)
		for _, symbol := range []string{"Owner", "anchor"} {
			if !slices.Contains(got, symbol) {
				panic("valid suffix lost past initializer-prefix header limit")
			}
		}
	}

	small := source(javaMaximumRecoveryHeaderTokens / 4)
	large := source(javaMaximumRecoveryHeaderTokens)
	analyze(small)
	analyze(large)
	smallAllocations := testing.AllocsPerRun(1, func() { analyze(small) })
	largeAllocations := testing.AllocsPerRun(1, func() { analyze(large) })
	if largeAllocations > smallAllocations*6+4096 {
		t.Fatalf(
			"initializer-prefix allocations grew superlinearly: small=%.0f large=%.0f",
			smallAllocations, largeAllocations,
		)
	}
}

func javaAssertMemberSuffixSymbols(
	t *testing.T,
	analysis *javaSourceAnalysis,
	symbols ...string,
) {
	t.Helper()
	got := javaDefinitionSymbols(analysis.definitions)
	for _, symbol := range symbols {
		if !slices.Contains(got, symbol) {
			t.Errorf(
				"valid member suffix lost %q: definitions=%#v recovery=%#v",
				symbol, got, analysis.recoverySpans,
			)
		}
	}
}
