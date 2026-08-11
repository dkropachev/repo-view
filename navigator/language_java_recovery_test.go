package navigator

import (
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestJavaDelimiterMismatchRecoveryPreservesCompatiblePairs(t *testing.T) {
	t.Parallel()

	tokens := javaRecoveryTokens("{", "(", "[", ")", "}", "[", "}")
	analysis := analyzeJavaDelimiters(tokens)
	if got, want := analysis.pairs, []int{4, 3, -1, 1, 0, -1, -1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("delimiter pairs = %#v, want %#v", got, want)
	}
	if got, want := analysis.braceOwner, []int{-1, 0, 0, 0, 0, -1, -1}; !reflect.DeepEqual(got, want) {
		t.Fatalf("brace owners = %#v, want %#v", got, want)
	}
}

func TestJavaDelimiterMismatchRecoveryMatchesReference(t *testing.T) {
	t.Parallel()

	state := uint64(1)
	next := func(limit int) int {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		return int(state % uint64(limit))
	}
	values := []string{"(", "[", "{", ")", "]", "}", "identifier"}
	for sample := range 2_000 {
		tokens := make([]javaToken, 64)
		for index := range tokens {
			value := values[next(len(values))]
			tokens[index] = javaToken{text: value, value: value}
			if next(31) == 0 {
				tokens[index] = javaToken{text: ";", value: ";", gap: true}
			}
		}
		got := analyzeJavaDelimiters(tokens)
		want := javaReferenceDelimiterAnalysis(tokens)
		if !reflect.DeepEqual(got.pairs, want.pairs) ||
			!reflect.DeepEqual(got.braceOwner, want.braceOwner) {
			t.Fatalf(
				"sample %d mismatch:\n tokens=%#v\n got pairs=%#v owners=%#v\n want pairs=%#v owners=%#v",
				sample, tokens, got.pairs, got.braceOwner, want.pairs, want.braceOwner,
			)
		}
	}
}

func TestJavaLexicalRecoveryAcceptsOnlyRealModuleHeadersAndRequires(t *testing.T) {
	t.Parallel()

	const source = `class Labels {
    module: {
        requires: while (ready()) break requires;
    }
}
label: module fake.name {
    requires java.sql;
}
@Marker(values = {One.VALUE, Two.VALUE})
open module good /* comment */ . name {
    requires static transitive java.sql;
    requires java.logging;
    requires(java.desktop);
}`
	analysis := javaRecoveryAnalysis(source)
	if got, want := javaDefinitionSymbols(analysis.definitions), []string{"Labels", "good.name"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("lexical module definitions = %#v, want %#v", got, want)
	}
	if got, want := analysis.imports, []javaLineSpan{{start: 11, end: 11}, {start: 12, end: 12}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("lexical requires spans = %#v, want %#v", got, want)
	}
}

func TestJavaLexicalRecoverySkipsAnnotationsAroundMembers(t *testing.T) {
	t.Parallel()

	const source = `interface Api {
    @pkg.Flag(enabled = true, factory = Factory.make())
    default void run() {}
    @Marker(factory = Factory.make())
    String lookup();
    public @Marker(flag = true) static final int count = 1;
}
@interface Meta {
    @Marker(factory = Factory.make())
    String value() default "x";
}`
	analysis := javaRecoveryAnalysis(source)
	if got, want := javaDefinitionSymbols(analysis.definitions),
		[]string{"Api", "run", "lookup", "count", "Meta", "value"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("annotated lexical definitions = %#v, want %#v", got, want)
	}
}

func TestJavaLexicalRecoveryIndexesNestedTypeMembersOnce(t *testing.T) {
	t.Parallel()

	const source = `class Outer {
    int before;
    class Inner {
        int nested;
        void work();
    }
    int after;
    void owner() {
        class Local {
            int localField;
        }
        int localVariable;
    }
}`
	analysis := javaRecoveryAnalysis(source)
	if got, want := javaDefinitionSymbols(analysis.definitions), []string{
		"Outer", "before", "Inner", "nested", "work", "after", "owner", "Local", "localField",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("nested lexical definitions = %#v, want %#v", got, want)
	}
}

func TestJavaLexicalRecoveryDoesNotScopeArrayInitializers(t *testing.T) {
	t.Parallel()

	const source = `class Arrays {
    int[] flat = {
        1, 2
    };
    int[][] nested = {
        {1},
        {2}
    };
    int[] made = new int[] {
        3
    };
    @Values(numbers = {1, 2}) int[] annotated = {4};
    Runnable task = new Runnable() {
        public void run() {}
    };
}`
	analysis := javaRecoveryAnalysis(source)
	definitions := make(map[string]sourceDefinition, len(analysis.definitions))
	for _, definition := range analysis.definitions {
		definitions[definition.symbol] = definition
	}
	for _, symbol := range []string{"flat", "nested", "made", "annotated"} {
		definition, ok := definitions[symbol]
		if !ok {
			t.Fatalf("array field %q missing from %#v", symbol, analysis.definitions)
		}
		if definition.ownsScope {
			t.Fatalf("array field %q unexpectedly owns scope: %#v", symbol, definition)
		}
	}
	if definition, ok := definitions["task"]; !ok || !definition.ownsScope {
		t.Fatalf("anonymous-class field scope = %#v, true; want owned scope", definition)
	}
	for _, unwanted := range []javaLineScope{
		{start: 2, end: 4},
		{start: 5, end: 8},
		{start: 9, end: 11},
		{start: 12, end: 12},
	} {
		for _, scope := range analysis.scopes {
			if scope == unwanted {
				t.Fatalf("array initializer became lexical scope %#v; scopes=%#v", unwanted, analysis.scopes)
			}
		}
	}
}

func TestJavaAuthoritativeRecoverySkipsAnnotationDefaultCalls(t *testing.T) {
	t.Parallel()

	const source = `\u0040interface Options {
    String value() default helper();
    String qualified() default Factory.make();
}
\u0069nterface Runner {
    default void run() {}
}`
	analysis := javaAuthoritativeRecoveryAnalysis(t, source)
	if got, want := javaDefinitionSymbols(analysis.definitions),
		[]string{"Options", "value", "qualified", "Runner", "run"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("default-expression definitions = %#v, want %#v", got, want)
	}
}

func TestJavaAuthoritativeRecoveryIndexesAnonymousAndEnumBodies(t *testing.T) {
	t.Parallel()

	const source = `cl\u0061ss Host {
    Runnable task = new Runnable() {
        int field;
        public void run() {}
        Runnable nested = new Runnable() {
            void nestedRun() {}
        };
    };
}
enum State {
    READY {
        int code;
        void execute() {}
        Runnable nested = new Runnable() {
            void enumNested() {}
        };
    };
}`
	analysis := javaAuthoritativeRecoveryAnalysis(t, source)
	if got, want := javaDefinitionSymbols(analysis.definitions), []string{
		"Host", "task", "field", "run", "nested", "nestedRun",
		"State", "READY", "code", "execute", "nested", "enumNested",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("anonymous-body definitions = %#v, want %#v", got, want)
	}
}

func TestJavaAuthoritativeRecoverySeparatesSingleEnumConstantFromFields(t *testing.T) {
	t.Parallel()

	const source = `en\u0075m State {
    READY;
    int code;
}`
	analysis := javaAuthoritativeRecoveryAnalysis(t, source)
	if got, want := javaDefinitionSymbols(analysis.definitions),
		[]string{"State", "READY", "code"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("single-constant enum definitions = %#v, want %#v", got, want)
	}
}

func TestJavaAuthoritativeRecoveryIndexesCompactSourceMethodsOnly(t *testing.T) {
	t.Parallel()

	const source = `vo\u0069d main() {
    int local = 0;
    helper();
}
String render(int value) {
    return format(value);
}
<T> T identity(T value) {
    return value;
}
System.out.println("not a declaration");
helper();
<String>helper();
this.<String>qualified();
(factory()).run();`
	analysis := javaAuthoritativeRecoveryAnalysis(t, source)
	if got, want := javaDefinitionSymbols(analysis.definitions),
		[]string{"main", "render", "identity"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("compact-source definitions = %#v, want %#v", got, want)
	}
	for _, definition := range analysis.definitions {
		if !definition.ownsScope {
			t.Fatalf("compact method does not own its declaration: %#v", definition)
		}
	}
}

func TestJavaAuthoritativeRecoverySplitsGenericRecordComponents(t *testing.T) {
	t.Parallel()

	const source = `\u0072ecord Generic(
    Map<String, List<Integer>> mapping,
    Map<String, Map<Integer, List<Long>>> deep,
    int count
) {}`
	analysis := javaAuthoritativeRecoveryAnalysis(t, source)
	if got, want := javaDefinitionSymbols(analysis.definitions),
		[]string{"Generic", "mapping", "deep", "count"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("generic record definitions = %#v, want %#v", got, want)
	}
}

func TestJavaRecoveryJavadocsCrossOrdinaryCommentsButNotBlankLines(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		source string
		want   int
	}{
		{
			name:   "ordinary comment after docs",
			source: "/** docs */\n// ordinary\ncl\\u0061ss C {}",
			want:   1,
		},
		{
			name:   "adjacent ordinary and docs",
			source: "/* ordinary *//** docs */\ncl\\u0061ss C {}",
			want:   1,
		},
		{
			name:   "blank line breaks attachment",
			source: "/** docs */\n// ordinary\n\ncl\\u0061ss C {}",
			want:   4,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			analysis := javaAuthoritativeRecoveryAnalysis(t, test.source)
			definition := javaFirstDefinition(t, analysis.definitions, "C")
			if definition.scopeStart != test.want {
				t.Fatalf("C scopeStart = %d, want %d; definition=%#v",
					definition.scopeStart, test.want, definition)
			}
		})
	}
}

func TestJavaRecoveryAttachesMarkdownDocCommentGroups(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		source string
		want   int
	}{
		{"raw group", "/// first\n/// second\ncl\\u0061ss C {}", 1},
		{"escaped slashes", `\u002f\u002f\u002f docs
cl\u0061ss C {}`, 1},
		{"mixed slashes", `//\u002f docs
cl\u0061ss C {}`, 1},
		{"four slashes", "//// docs\ncl\\u0061ss C {}", 1},
		{"spaced slash is ordinary", "// / docs\ncl\\u0061ss C {}", 2},
		{"trailing markdown is ordinary", "int value; /// trailing\ncl\\u0061ss C {}", 2},
		{
			"escaped trailing markdown is ordinary",
			`int value; \u002f\u002f\u002f trailing
cl\u0061ss C {}`,
			2,
		},
		{
			"escaped logical newline starts markdown",
			`int value;\u000a\u002f\u002f\u002f docs
cl\u0061ss C {}`,
			1,
		},
		{
			"closest markdown group",
			"/// stale\n// ordinary\n/// closest\ncl\\u0061ss C {}",
			3,
		},
		{
			"ordinary after sole markdown",
			"/// docs\n// ordinary\ncl\\u0061ss C {}",
			1,
		},
		{"blank line breaks", "/// docs\n\ncl\\u0061ss C {}", 3},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			analysis := javaAuthoritativeRecoveryAnalysis(t, test.source)
			definition := javaFirstDefinition(t, analysis.definitions, "C")
			if definition.scopeStart != test.want {
				t.Fatalf("C scopeStart = %d, want %d; definition=%#v",
					definition.scopeStart, test.want, definition)
			}
		})
	}
}

func TestJavaAuthoritativeRecoveryDoesNotAttachDocsToControlScopes(t *testing.T) {
	t.Parallel()

	const source = `cl\u0061ss Controls {
    void run() {
        /** condition note */
        if (ready()) {
            target();
        }
        /** try note */
        try {
            target();
        } finally {
            cleanup();
        }
        /** synchronized note */
        synchronized (lock) {
            target();
        }
        /** plain note */
        {
            target();
        }
    }
}`
	analysis := javaAuthoritativeRecoveryAnalysis(t, source)
	for _, want := range []javaLineScope{
		{start: 4, end: 6},
		{start: 8, end: 10},
		{start: 10, end: 12},
		{start: 14, end: 16},
		{start: 18, end: 20},
	} {
		found := false
		for _, scope := range analysis.scopes {
			found = found || scope == want
		}
		if !found {
			t.Fatalf("control scopes = %#v, missing %#v", analysis.scopes, want)
		}
	}
}

func TestJavaAuthoritativeRecoveryScopesFieldDeclaratorsIndependently(t *testing.T) {
	t.Parallel()

	const source = `cl\u0061ss Fields {
    Runnable scalar = plain, task = () -> { target(); };
    Object value = this.<String, Integer, Long>factory();
}`
	analysis := javaAuthoritativeRecoveryAnalysis(t, source)
	if got, want := javaDefinitionSymbols(analysis.definitions),
		[]string{"Fields", "scalar", "task", "value"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("field definitions = %#v, want %#v", got, want)
	}
	scalar := javaFirstDefinition(t, analysis.definitions, "scalar")
	task := javaFirstDefinition(t, analysis.definitions, "task")
	if scalar.ownsScope || !task.ownsScope {
		t.Fatalf("multi-declarator ownership = scalar %#v, task %#v", scalar, task)
	}
}

func TestJavaAuthoritativeRecoveryCollapsesMalformedDeclaratorCommaRuns(t *testing.T) {
	t.Parallel()

	const source = `cl\u0061ss Fields {
    int first,,,,last;
    int tail;
}`
	analysis := javaAuthoritativeRecoveryAnalysis(t, source)
	if got, want := javaDefinitionSymbols(analysis.definitions),
		[]string{"Fields", "first", "last", "tail"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("field definitions = %#v, want %#v", got, want)
	}
}

func TestJavaDenseMalformedFieldRecoveryRemainsLinear(t *testing.T) {
	const count = 1 << 17

	t.Run("consecutive declarator commas", func(t *testing.T) {
		tokens := make([]javaToken, 0, count+3)
		tokens = append(tokens,
			javaRecoveryToken("int", false),
			javaRecoveryToken("first", true),
		)
		for range count {
			tokens = append(tokens, javaRecoveryToken(",", false))
		}
		last := len(tokens)
		tokens = append(tokens, javaRecoveryToken("last", true))
		delimiters := analyzeJavaDelimiters(tokens)
		typeArguments := javaAnalyzeExpressionTypeArguments(
			tokens, delimiters, 0, len(tokens),
		)
		if got, want := javaFieldDeclaratorNames(
			tokens, delimiters, 0, len(tokens), typeArguments,
		), []int{1, last}; !reflect.DeepEqual(got, want) {
			t.Fatalf("declarator indexes = %#v, want %#v", got, want)
		}
	})

	t.Run("unclosed allocation type arguments", func(t *testing.T) {
		tokens := make([]javaToken, 0, count*2+2)
		tokens = append(tokens,
			javaRecoveryToken("new", false),
			javaRecoveryToken("Bad", true),
		)
		for range count {
			tokens = append(tokens,
				javaRecoveryToken("<", false),
				javaRecoveryToken("T", true),
			)
		}
		delimiters := analyzeJavaDelimiters(tokens)
		typeArguments := javaAnalyzeExpressionTypeArguments(
			tokens, delimiters, 0, len(tokens),
		)
		if !javaSegmentContainsMalformedAllocation(
			tokens, delimiters, 0, len(tokens), typeArguments,
		) {
			t.Fatal("unclosed allocation type arguments were accepted")
		}
	})

	t.Run("relational initializer", func(t *testing.T) {
		tokens := make([]javaToken, 0, count*2+4)
		tokens = append(tokens,
			javaRecoveryToken("boolean", false),
			javaRecoveryToken("value", true),
			javaRecoveryToken("=", false),
			javaRecoveryToken("left", true),
		)
		for range count {
			tokens = append(tokens,
				javaRecoveryToken("<", false),
				javaRecoveryToken("right", true),
			)
		}
		delimiters := analyzeJavaDelimiters(tokens)
		typeArguments := javaAnalyzeExpressionTypeArguments(
			tokens, delimiters, 0, len(tokens),
		)
		if got, want := javaFieldDeclaratorNames(
			tokens, delimiters, 0, len(tokens), typeArguments,
		), []int{1}; !reflect.DeepEqual(got, want) {
			t.Fatalf("declarator indexes = %#v, want %#v", got, want)
		}
	})
}

func TestJavaExpressionTypeArgumentAnalysisMatchesSingleStartReference(t *testing.T) {
	t.Parallel()

	type tokenKind struct {
		value      string
		identifier bool
	}
	kinds := []tokenKind{
		{"<", false}, {">", false}, {">>", false}, {">>>", false},
		{"=", false}, {";", false}, {"->", false}, {"::", false},
		{"(", false}, {")", false}, {"[", false}, {"]", false},
		{".", false}, {",", false}, {"new", false}, {"instanceof", false},
		{"Name", true}, {"Type", true}, {"method", true},
	}
	state := uint64(1)
	next := func(limit int) int {
		state ^= state << 13
		state ^= state >> 7
		state ^= state << 17
		return int(state % uint64(limit))
	}
	for sample := range 2_000 {
		tokens := make([]javaToken, 96)
		for index := range tokens {
			kind := kinds[next(len(kinds))]
			tokens[index] = javaRecoveryToken(kind.value, kind.identifier)
			if next(67) == 0 {
				tokens[index].gap = true
			}
		}
		delimiters := analyzeJavaDelimiters(tokens)
		analysis := javaAnalyzeExpressionTypeArguments(
			tokens, delimiters, 0, len(tokens),
		)
		for index, token := range tokens {
			if token.value != "<" {
				continue
			}
			got := analysis.close(index)
			want := javaExpressionTypeArgumentClose(
				tokens, delimiters, index, len(tokens),
			)
			if got != want {
				t.Fatalf("sample %d token %d: shared close=%d, reference=%d; tokens=%#v",
					sample, index, got, want, tokens)
			}
		}
	}
}

func TestJavaAuthoritativeRecoverySharesAttachedScopeAcrossFieldDeclarators(t *testing.T) {
	t.Parallel()

	const source = `cl\u0061ss Fields {
    /** Shared field documentation. */
    // An ordinary comment is transparent after documentation.
    // A second bridge must not make attachment declarator-dependent.
    Runnable first = () -> {}, second = () -> {}, third = () -> {};
}`
	analysis := javaAuthoritativeRecoveryAnalysis(t, source)
	if got, want := javaDefinitionSymbols(analysis.definitions),
		[]string{"Fields", "first", "second", "third"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("field definitions = %#v, want %#v", got, want)
	}
	for _, symbol := range []string{"first", "second", "third"} {
		definition := javaFirstDefinition(t, analysis.definitions, symbol)
		if !definition.ownsScope || definition.scopeStart != 2 || definition.scopeEnd != 5 {
			t.Fatalf("%s scope = %#v, want attached field scope 2-5", symbol, definition)
		}
	}
}

func TestJavaAuthoritativeRecoverySkipsAnnotatedGenericArgumentCommas(t *testing.T) {
	t.Parallel()

	const source = `cl\u0061ss C {
    Object x = new Foo<@A(flag = true) String, Integer, Long>();
}`
	analysis := javaAuthoritativeRecoveryAnalysis(t, source)
	if got, want := javaDefinitionSymbols(analysis.definitions), []string{"C", "x"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("annotated generic definitions = %#v, want %#v", got, want)
	}
}

func TestJavaAuthoritativeRecoverySkipsMethodReferenceTypeArguments(t *testing.T) {
	t.Parallel()

	const source = `cl\u0061ss C {
    Supplier<X> s = Foo::<A, B, C>new;
    Function<X, Y> f = Foo::<A, B, C>convert;
}`
	analysis := javaAuthoritativeRecoveryAnalysis(t, source)
	if got, want := javaDefinitionSymbols(analysis.definitions), []string{"C", "s", "f"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("method-reference definitions = %#v, want %#v", got, want)
	}
}

func TestJavaRecoveryRestartsMalformedRunsAndKeepsValidTail(t *testing.T) {
	const count = 8 << 10
	tests := []struct {
		name        string
		source      string
		wantSymbols []string
		wantImports bool
	}{
		{
			name: "callable",
			source: "class C { " + strings.Repeat("foo() ", count) +
				"void valid() {} }",
			wantSymbols: []string{"C", "valid"},
		},
		{
			name: "module",
			source: strings.Repeat("module bad ", count) +
				"module good { requires java.sql; }",
			wantSymbols: []string{"good"},
			wantImports: true,
		},
		{
			name: "requires",
			source: "module good { " + strings.Repeat("requires bad ", count) +
				"requires java.sql; }",
			wantSymbols: []string{"good"},
			wantImports: true,
		},
		{
			name: "import",
			source: strings.Repeat("import bad ", count) +
				"import java.util.List; class After {}",
			wantSymbols: []string{"After"},
			wantImports: true,
		},
		{
			name: "allocation",
			source: "class C { Object value = " + strings.Repeat("new Bad ", count) +
				"new Runnable() { void valid() {} }; }",
			wantSymbols: []string{"C", "valid"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			analysis := javaRecoveryAnalysis(test.source)
			if got := javaDefinitionSymbols(analysis.definitions); !reflect.DeepEqual(
				got, test.wantSymbols,
			) {
				t.Fatalf("definitions = %#v, want %#v", got, test.wantSymbols)
			}
			if got := len(analysis.imports) > 0; got != test.wantImports {
				t.Fatalf("has imports = %v, want %v; spans=%#v",
					got, test.wantImports, analysis.imports)
			}
		})
	}
}

func TestJavaRecoveryRestartsVoidMethodAfterUnterminatedInitializer(t *testing.T) {
	t.Parallel()

	const source = `cl\u0061ss C {
    int broken = compute() + fallback()
    void valid() {}
}`
	analysis := javaAuthoritativeRecoveryAnalysis(t, source)
	if got, want := javaDefinitionSymbols(analysis.definitions),
		[]string{"C", "valid"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("definitions = %#v, want safe recovered tail %#v", got, want)
	}
}

func TestJavaRecoveryRestartsTypeAfterIncompleteTopLevelHeader(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		source string
	}{
		{
			name: "import",
			source: `import java.util.
class Tail { int field; void method() {} }`,
		},
		{
			name: "package",
			source: `package broken.
class Tail { int field; void method() {} }`,
		},
		{
			name:   "translated line break",
			source: `import java.util.\u000aclass Tail { int field; void method() {} }`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			analysis := analyzeJavaSource(
				test.source, strings.Count(test.source, "\n")+1,
			)
			if got, want := javaDefinitionSymbols(analysis.definitions),
				[]string{"Tail", "field", "method"}; !reflect.DeepEqual(got, want) {
				t.Fatalf("recovered definitions = %#v, want %#v", got, want)
			}
		})
	}
}

func TestJavaRecoveryKeepsMultilineClassLiteralsOutOfDeclarations(t *testing.T) {
	t.Parallel()

	const source = `cl\u0061ss Holder {
    Class<?> first = String
        .class;
    Class<?> second = String.
        class;
    void method() {}
}`
	analysis := javaAuthoritativeRecoveryAnalysis(t, source)
	if got, want := javaDefinitionSymbols(analysis.definitions),
		[]string{"Holder", "first", "second", "method"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("multiline class-literal definitions = %#v, want %#v", got, want)
	}
}

func TestJavaAuthoritativeRecoverySupportsAnnotatedVariableDeclaratorDims(t *testing.T) {
	t.Parallel()

	const source = `cl\u0061ss C {
    int x @A [] = {};
    int a, y @B [] = {};
}`
	analysis := javaAuthoritativeRecoveryAnalysis(t, source)
	if got, want := javaDefinitionSymbols(analysis.definitions),
		[]string{"C", "x", "a", "y"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("annotated declarator dims = %#v, want %#v", got, want)
	}
}

func TestJavaAuthoritativeRecoveryDistinguishesMethodDimsFromArrayInitializers(t *testing.T) {
	t.Parallel()

	const source = `cl\u0061ss C {
    int oldStyle()[] {
        return null;
    }
    int annotated() @A [] {
        return null;
    }
}`
	analysis := javaAuthoritativeRecoveryAnalysis(t, source)
	if got, want := javaDefinitionSymbols(analysis.definitions),
		[]string{"C", "oldStyle", "annotated"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("method dims definitions = %#v, want %#v", got, want)
	}
	for _, want := range []javaLineScope{{start: 2, end: 4}, {start: 5, end: 7}} {
		if !slices.Contains(analysis.scopes, want) {
			t.Fatalf("method dims scopes = %#v, missing %#v", analysis.scopes, want)
		}
	}
}

func TestJavaAuthoritativeRecoverySkipsAnnotationDefaultArrayBody(t *testing.T) {
	t.Parallel()

	const source = `@interf\u0061ce A {
    int[] value() default {
        1, 2
    }
    ;
}`
	analysis := javaAuthoritativeRecoveryAnalysis(t, source)
	value := javaFirstDefinition(t, analysis.definitions, "value")
	if !value.ownsScope || value.scopeStart != 2 || value.scopeEnd != 5 {
		t.Fatalf("annotation array element scope = %#v, want owned 2-5", value)
	}
}

func TestJavaAuthoritativeRecoveryComparesLogicalConstructorNames(t *testing.T) {
	t.Parallel()

	classAnalysis := javaAuthoritativeRecoveryAnalysis(t,
		`cl\u0061ss Foo { F\u006fo() {} }`)
	if got, want := javaDefinitionSymbols(classAnalysis.definitions),
		[]string{"Foo", `F\u006fo`}; !reflect.DeepEqual(got, want) {
		t.Fatalf("escaped constructor definitions = %#v, want %#v", got, want)
	}
	recordAnalysis := javaAuthoritativeRecoveryAnalysis(t,
		`rec\u006frd Foo(int x) { F\u006fo {} }`)
	if got, want := javaDefinitionSymbols(recordAnalysis.definitions),
		[]string{"Foo", "x", `F\u006fo`}; !reflect.DeepEqual(got, want) {
		t.Fatalf("escaped compact constructor definitions = %#v, want %#v", got, want)
	}
}

func TestJavaAuthoritativeRecoveryAttachesDocsToEnumConstantBody(t *testing.T) {
	t.Parallel()

	const source = `en\u0075m E {
    /** docs */
    A {
        void f() {}
    };
}`
	analysis := javaAuthoritativeRecoveryAnalysis(t, source)
	constant := javaFirstDefinition(t, analysis.definitions, "A")
	if !constant.ownsScope || constant.scopeStart != 2 || constant.scopeEnd != 5 {
		t.Fatalf("documented enum constant = %#v, want owned 2-5", constant)
	}
}

func TestJavaAuthoritativeRecoveryKeepsInitializerAndLambdaScopes(t *testing.T) {
	t.Parallel()

	const source = `cl\u0061ss C {
    Runnable r =
        () -> {
            target();
        };
}`
	analysis := javaAuthoritativeRecoveryAnalysis(t, source)
	field := javaFirstDefinition(t, analysis.definitions, "r")
	if !field.ownsScope || field.scopeStart != 2 || field.scopeEnd != 5 {
		t.Fatalf("lambda field = %#v, want owned 2-5", field)
	}
	if want := (javaLineScope{start: 3, end: 5}); !slices.Contains(analysis.scopes, want) {
		t.Fatalf("lambda scopes = %#v, missing %#v", analysis.scopes, want)
	}
}

func TestJavaAuthoritativeRecoveryAddsUnbracedControlScope(t *testing.T) {
	t.Parallel()

	const source = `cl\u0061ss C {
    void f() {
        if (ready())
            target();
    }
}`
	analysis := javaAuthoritativeRecoveryAnalysis(t, source)
	if want := (javaLineScope{start: 3, end: 4}); !slices.Contains(analysis.scopes, want) {
		t.Fatalf("unbraced control scopes = %#v, missing %#v", analysis.scopes, want)
	}
}

func TestJavaAuthoritativeRecoveryAddsLoopLabelAndExpressionLambdaScopes(t *testing.T) {
	t.Parallel()

	const source = `cl\u0061ss C {
    void f() {
        for (;;) target();
        label:
            target();
        Runnable task = () ->
            target();
    }
}`
	analysis := javaAuthoritativeRecoveryAnalysis(t, source)
	for _, want := range []javaLineScope{
		{start: 3, end: 3},
		{start: 4, end: 5},
		{start: 6, end: 7},
	} {
		if !slices.Contains(analysis.scopes, want) {
			t.Fatalf("unbraced scopes = %#v, missing %#v", analysis.scopes, want)
		}
	}
}

func TestJavaAuthoritativeRecoveryMatchesRecursiveStatementScopes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		source string
		want   []javaLineScope
	}{
		{
			name: "braced do while includes trailing condition",
			source: `class C {
 void f() {
  do {
   target();
  } while (
   ready()
  );
 }
}`,
			want: []javaLineScope{{1, 9}, {2, 8}, {3, 7}},
		},
		{
			name: "unbraced do while includes trailing condition",
			source: `class C {
 void f() {
  do
   target();
  while (
   ready()
  );
 }
}`,
			want: []javaLineScope{{1, 9}, {2, 8}, {3, 7}},
		},
		{
			name: "switch expression owns every arrow rule",
			source: `class C {
 int f(int v) {
  return switch (v) {
   case 1 ->
    first();
   case 2 -> {
    yield second();
   }
   default ->
    fallback();
  };
 }
}`,
			want: []javaLineScope{{1, 13}, {2, 12}, {3, 11}, {4, 5}, {6, 8}, {9, 10}},
		},
		{
			name: "colon switch owns every statement group",
			source: `class C {
 void f(int v) {
  switch (v) {
   case 1:
    first();
    break;
   case 2:
   default:
    fallback();
  }
 }
}`,
			want: []javaLineScope{{1, 12}, {2, 11}, {3, 10}, {4, 6}, {7, 7}, {8, 9}},
		},
		{
			name: "dangling else and else if own recursive branches",
			source: `class C {
 void f() {
  if (a())
   if (b())
    first();
   else
    second();
  else if (c())
   third();
  else
   fourth();
 }
}`,
			want: []javaLineScope{
				{1, 13}, {2, 12}, {3, 7}, {3, 11}, {4, 5},
				{4, 7}, {6, 7}, {8, 9}, {8, 11}, {10, 11},
			},
		},
		{
			name: "combined null default is one switch rule",
			source: `class C {
 int f(Object value) {
  return switch (value) {
   case null,
        default ->
    1;
  };
 }
}`,
			want: []javaLineScope{{1, 9}, {2, 8}, {3, 7}, {4, 6}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			plain := analyzeJavaSource(test.source, strings.Count(test.source, "\n")+1)
			if plain.tree == nil || len(plain.recoverySpans) != 0 {
				t.Fatalf("plain fixture is not a clean syntax authority: tree=%v recovery=%#v",
					plain.tree != nil, plain.recoverySpans)
			}
			if !reflect.DeepEqual(plain.scopes, test.want) {
				t.Fatalf("tree scopes = %#v, want exact %#v", plain.scopes, test.want)
			}

			escapedSource := strings.Replace(test.source, "class", `cl\u0061ss`, 1)
			escaped := javaAuthoritativeRecoveryAnalysis(t, escapedSource)
			if !reflect.DeepEqual(escaped.scopes, test.want) {
				t.Fatalf("lexical scopes = %#v, want authoritative %#v",
					escaped.scopes, test.want)
			}
		})
	}
}

func TestJavaAuthoritativeRecoveryMatchesBroadStatementScopes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{"braced if else", "if (a()) {\n x();\n} else {\n y();\n}"},
		{"labeled braced loop", "outer:\n while (a()) {\n  break outer;\n }"},
		{"try catch finally", "try {\n x();\n} catch (Exception e) {\n y();\n} finally {\n z();\n}"},
		{"try with resources", "try (var x = open()) {\n use(x);\n} catch (Exception e) {\n fail();\n}"},
		{"nested unbraced statements", "while (a())\n if (b())\n  x();\n else\n  y();"},
		{"nested do statements", "do\n if (a())\n  x();\n else\n  y();\nwhile (ready());"},
		{"arrow switch statement", "switch (v) {\n case 1 ->\n  x();\n default ->\n  throw new Error();\n}"},
		{"guarded switch rule", "switch (v) {\n case String s when s.isEmpty() ->\n  x();\n default -> y();\n}"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			source := "class C {\n void f() {\n" + test.body + "\n }\n}"
			plain := analyzeJavaSource(source, strings.Count(source, "\n")+1)
			if plain.tree == nil || len(plain.recoverySpans) != 0 {
				t.Fatalf("plain fixture is not a clean syntax authority: tree=%v recovery=%#v",
					plain.tree != nil, plain.recoverySpans)
			}
			escapedSource := strings.Replace(source, "class", `cl\u0061ss`, 1)
			escaped := javaAuthoritativeRecoveryAnalysis(t, escapedSource)
			if !reflect.DeepEqual(escaped.scopes, plain.scopes) {
				t.Fatalf("tree scopes=%#v\nlexical scopes=%#v", plain.scopes, escaped.scopes)
			}
		})
	}
}

func TestJavaAuthoritativeRecoveryMatchesExpressionLambdaScopes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		source string
		want   []javaLineScope
	}{
		{
			name: "declaration semicolon is outside lambda",
			source: `class C {
 void f() {
  Runnable r = () ->
   target()
  ;
 }
}`,
			want: []javaLineScope{{1, 7}, {2, 6}, {3, 4}},
		},
		{
			name: "nested expression lambda is one body",
			source: `class C {
 void f() {
  Function<X, Function<Y, Z>> f =
   x ->
    y ->
     z;
 }
}`,
			want: []javaLineScope{{1, 8}, {2, 7}, {4, 6}, {5, 6}},
		},
		{
			name: "explicit generic invocation comma is inside body",
			source: `class C {
 void f() {
  Function<X, Y> f = x ->
   Factory.<String,
    Integer>build(x);
 }
}`,
			want: []javaLineScope{{1, 7}, {2, 6}, {3, 5}},
		},
		{
			name: "parameterized construction comma is inside body",
			source: `class C {
 void f() {
  Function<X, Pair<String, Integer>> f = x ->
   new Pair<String,
    Integer>(x, x);
 }
}`,
			want: []javaLineScope{{1, 7}, {2, 6}, {3, 5}},
		},
		{
			name: "parameterized method reference comma is inside body",
			source: `class C {
 void f() {
  Function<X, Supplier<Y>> f = x ->
   Type<String,
    Integer>::target;
 }
}`,
			want: []javaLineScope{{1, 7}, {2, 6}, {3, 5}},
		},
		{
			name: "instanceof wildcard comma is inside body",
			source: `class C {
 void f() {
  Predicate<Object> p = x ->
   x instanceof Map<?,
    ?>;
 }
}`,
			want: []javaLineScope{{1, 7}, {2, 6}, {3, 5}},
		},
		{
			name: "instanceof pattern wildcard comma is inside body",
			source: `class C {
 void f() {
  Predicate<Object> p = x ->
   x instanceof Map<?,
    ?> values;
 }
}`,
			want: []javaLineScope{{1, 7}, {2, 6}, {3, 5}},
		},
		{
			name: "annotated instanceof wildcard comma is inside body",
			source: `class C {
 void f() {
  Predicate<Object> p = x ->
   x instanceof @Marker Map<?,
    ?>;
 }
}`,
			want: []javaLineScope{{1, 7}, {2, 6}, {3, 5}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			plain := analyzeJavaSource(test.source, strings.Count(test.source, "\n")+1)
			if plain.tree == nil || len(plain.recoverySpans) != 0 {
				t.Fatalf("plain fixture is not a clean syntax authority: tree=%v recovery=%#v",
					plain.tree != nil, plain.recoverySpans)
			}
			if !reflect.DeepEqual(plain.scopes, test.want) {
				t.Fatalf("tree scopes = %#v, want exact %#v", plain.scopes, test.want)
			}
			escapedSource := strings.Replace(test.source, "class", `cl\u0061ss`, 1)
			escaped := javaAuthoritativeRecoveryAnalysis(t, escapedSource)
			if !reflect.DeepEqual(escaped.scopes, test.want) {
				t.Fatalf("lexical scopes = %#v, want authoritative %#v",
					escaped.scopes, test.want)
			}
		})
	}
}

func TestJavaAuthoritativeRecoveryIncludesOwnedDeclarationScopes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		source  string
		escaped string
		want    []javaLineScope
	}{
		{
			name: "documented interface method",
			source: `interface Api {
 /** docs */
 void run(
  int value
 );
}`,
			escaped: `interf\u0061ce Api {
 /** docs */
 void run(
  int value
 );
}`,
			want: []javaLineScope{{1, 6}, {2, 5}},
		},
		{
			name: "abstract and native methods",
			source: `abstract class C {
 abstract void work(
  int value
 );
 native int nativeCall();
}`,
			escaped: `abstract cl\u0061ss C {
 abstract void work(
  int value
 );
 native int nativeCall();
}`,
			want: []javaLineScope{{1, 6}, {2, 4}, {5, 5}},
		},
		{
			name: "scoped field initializers",
			source: `class C {
 /** docs */
 Runnable task = () -> {
  target();
 };
 Object value = new Object() {
  void nested() {}
 };
}`,
			escaped: `cl\u0061ss C {
 /** docs */
 Runnable task = () -> {
  target();
 };
 Object value = new Object() {
  void nested() {}
 };
}`,
			want: []javaLineScope{{1, 9}, {2, 5}, {3, 5}, {6, 8}, {7, 7}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			plain := analyzeJavaSource(test.source, strings.Count(test.source, "\n")+1)
			if plain.tree == nil || len(plain.recoverySpans) != 0 {
				t.Fatalf("plain fixture is not a clean syntax authority: tree=%v recovery=%#v",
					plain.tree != nil, plain.recoverySpans)
			}
			if !reflect.DeepEqual(plain.scopes, test.want) {
				t.Fatalf("tree scopes = %#v, want exact %#v", plain.scopes, test.want)
			}
			escaped := javaAuthoritativeRecoveryAnalysis(t, test.escaped)
			if !reflect.DeepEqual(escaped.scopes, test.want) {
				t.Fatalf("lexical scopes = %#v, want authoritative %#v",
					escaped.scopes, test.want)
			}
		})
	}
}

func TestJavaAuthoritativeRecoveryTreatsAnnotationShorthandAsArray(t *testing.T) {
	t.Parallel()

	const source = `/** docs */
@pkg.Marker({ONE, TWO})
class C {
 @Marker({THREE, FOUR})
 void run() {}
}`
	plain := analyzeJavaSource(source, strings.Count(source, "\n")+1)
	if plain.tree == nil || len(plain.recoverySpans) != 0 {
		t.Fatalf("plain fixture is not a clean syntax authority: tree=%v recovery=%#v",
			plain.tree != nil, plain.recoverySpans)
	}
	if got, want := plain.scopes, []javaLineScope{{1, 6}, {4, 5}}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tree scopes = %#v, want exact %#v", got, want)
	}
	escapedSource := strings.Replace(source, "Marker", `M\u0061rker`, 1)
	escaped := javaAuthoritativeRecoveryAnalysis(t, escapedSource)
	if !reflect.DeepEqual(escaped.scopes, plain.scopes) {
		t.Fatalf("tree scopes=%#v\nlexical scopes=%#v", plain.scopes, escaped.scopes)
	}
	if !reflect.DeepEqual(
		javaDefinitionSummaries(escaped.definitions),
		javaDefinitionSummaries(plain.definitions),
	) {
		t.Fatalf("tree definitions=%#v\nlexical definitions=%#v",
			plain.definitions, escaped.definitions)
	}
}

func TestJavaAuthoritativeRecoveryMatchesCorpusDefinitionEdges(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		source  string
		escaped string
		want    []string
	}{
		{
			name: "enum constructor argument commas",
			source: `class C {
 enum E {
  FIRST("x", Tag.NAMED),
  SECOND;
 }
}`,
			escaped: `class C {
 enum E {
  FIRST("\u0078", Tag.NAMED),
  SECOND;
 }
}`,
			want: []string{"C", "E", "FIRST", "SECOND"},
		},
		{
			name: "generic record components",
			source: `class C {
 record R<T extends String>(
  T value
 ) {}
}`,
			escaped: `class C {
 record R<T extends Str\u0069ng>(
  T value
 ) {}
}`,
			want: []string{"C", "R", "value"},
		},
		{
			name: "class literal field initializer",
			source: `class C {
 String name = C.class.getName();
}`,
			escaped: `class C {
 String name = C.class.getN\u0061me();
}`,
			want: []string{"C", "name"},
		},
		{
			name: "call expressions stay in field initializers",
			source: `class C {
 int sum = foo() + bar();
 int choice = flag ? left() : right();
 int nested = first() * (second() - third());
}`,
			escaped: `class C {
 int sum = f\u006fo() + bar();
 int choice = flag ? left() : right();
 int nested = first() * (second() - third());
}`,
			want: []string{"C", "sum", "choice", "nested"},
		},
		{
			name: "initializer local type",
			source: `class C {
 Runnable task = () -> {
  class
   Local {}
 };
}`,
			escaped: `class C {
 Runnable task = () -> {
  cl\u0061ss
   Local {}
 };
}`,
			want: []string{"C", "task", "Local"},
		},
		{
			name: "synchronized method modifier",
			source: `class C {
 synchronized void run() {}
}`,
			escaped: `class C {
 synchronized void run() \u007b\u007d
}`,
			want: []string{"C", "run"},
		},
		{
			name: "wildcard super generic method",
			source: `class C {
 <T extends Box<? super Value>>
 T method(T value) { return value; }
}`,
			escaped: `class C {
 <T extends Box<? super Value>>
 T method(T value) { return v\u0061lue; }
}`,
			want: []string{"C", "method"},
		},
		{
			name: "annotation type attached prefix",
			source: `/** docs */
@Retention(RUNTIME)
@interface Note {}`,
			escaped: `/** docs */
@Retent\u0069on(RUNTIME)
@interface Note {}`,
			want: []string{"Note"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			plain := analyzeJavaSource(test.source, strings.Count(test.source, "\n")+1)
			if plain.tree == nil || len(plain.recoverySpans) != 0 {
				t.Fatalf("plain fixture is not a clean syntax authority: tree=%v recovery=%#v",
					plain.tree != nil, plain.recoverySpans)
			}
			if got := javaDefinitionSymbols(plain.definitions); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("tree definitions = %#v, want %#v", got, test.want)
			}
			escaped := javaAuthoritativeRecoveryAnalysis(t, test.escaped)
			if got := javaDefinitionSymbols(escaped.definitions); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("lexical definitions = %#v, want %#v", got, test.want)
			}
			if !reflect.DeepEqual(
				javaDefinitionSummaries(escaped.definitions),
				javaDefinitionSummaries(plain.definitions),
			) {
				t.Fatalf("tree definitions=%#v\nlexical definitions=%#v",
					plain.definitions, escaped.definitions)
			}
			if test.name == "initializer local type" {
				task := javaFirstDefinition(t, escaped.definitions, "task")
				if !task.ownsScope {
					t.Fatalf("initializer field does not own scope: %#v", task)
				}
			}
		})
	}
}

func TestJavaAuthoritativeRecoveryMatchesContextualBraceScopes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		source string
		want   []javaLineScope
	}{
		{
			name: "labeled block keeps block and label scopes",
			source: `class C {
 void f() {
  outer:
  {
   class Nested {}
  }
 }
}`,
			want: []javaLineScope{{1, 8}, {2, 7}, {3, 6}, {4, 6}, {5, 5}},
		},
		{
			name: "nested labels remain statements",
			source: `class C {
 void f() {
  outer:
   inner:
    target();
 }
}`,
			want: []javaLineScope{{1, 7}, {2, 6}, {3, 5}, {4, 5}},
		},
		{
			name: "multiline ternaries are not labels",
			source: `class C {
 void f() {
  int firstValue = flag ? object.value
   : fallback;
  int secondValue = outer ? inner ? first
   : second : third;
 }
}`,
			want: []javaLineScope{{1, 8}, {2, 7}},
		},
		{
			name: "single line ternary is not a label",
			source: `class C {
 void f() {
  int value = flag ? object.value : fallback;
 }
}`,
			want: []javaLineScope{{1, 5}, {2, 4}},
		},
		{
			name: "lambda arrow inside if condition does not anchor brace",
			source: `class C {
 void f() {
  if (peekToken(
   token -> token == LPAREN
  )) {
   target();
  }
 }
}`,
			want: []javaLineScope{{1, 9}, {2, 8}, {3, 7}, {4, 4}},
		},
		{
			name: "allocation inside if condition does not anchor brace",
			source: `class C {
 void f() {
  if (check(
   new Value()
  )) {
   target();
  }
 }
}`,
			want: []javaLineScope{{1, 9}, {2, 8}, {3, 7}},
		},
		{
			name: "case colon stops if brace backtracking",
			source: `class C {
 void f(int value) {
  switch (value) {
   case 1:
    if (ready()) {
     target();
    }
  }
 }
}`,
			want: []javaLineScope{{1, 10}, {2, 9}, {3, 8}, {4, 7}, {5, 7}},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			plain := analyzeJavaSource(test.source, strings.Count(test.source, "\n")+1)
			if plain.tree == nil || len(plain.recoverySpans) != 0 {
				t.Fatalf("plain fixture is not a clean syntax authority: tree=%v recovery=%#v",
					plain.tree != nil, plain.recoverySpans)
			}
			if !reflect.DeepEqual(plain.scopes, test.want) {
				t.Fatalf("tree scopes = %#v, want exact %#v", plain.scopes, test.want)
			}
			escapedSource := strings.Replace(test.source, "class", `cl\u0061ss`, 1)
			escaped := javaAuthoritativeRecoveryAnalysis(t, escapedSource)
			if !reflect.DeepEqual(escaped.scopes, test.want) {
				t.Fatalf("lexical scopes = %#v, want authoritative %#v",
					escaped.scopes, test.want)
			}
		})
	}
}

func TestJavaAuthoritativeRecoverySkipsGenericEnumConstantBodyScopes(t *testing.T) {
	t.Parallel()

	const source = `class C {
 enum E {
  /** first */
  FIRST {
   void firstMethod() {}
  },
  /** second */
  SECOND {
   void secondMethod() {}
  };
 }
}`
	plain := analyzeJavaSource(source, strings.Count(source, "\n")+1)
	if plain.tree == nil || len(plain.recoverySpans) != 0 {
		t.Fatalf("plain fixture is not a clean syntax authority: tree=%v recovery=%#v",
			plain.tree != nil, plain.recoverySpans)
	}
	want := []javaLineScope{{1, 12}, {2, 11}, {3, 6}, {5, 5}, {7, 10}, {9, 9}}
	if !reflect.DeepEqual(plain.scopes, want) {
		t.Fatalf("tree scopes = %#v, want exact %#v", plain.scopes, want)
	}
	escapedSource := strings.Replace(source, "first", `f\u0069rst`, 1)
	escaped := javaAuthoritativeRecoveryAnalysis(t, escapedSource)
	if !reflect.DeepEqual(escaped.scopes, want) {
		t.Fatalf("lexical scopes = %#v, want authoritative %#v", escaped.scopes, want)
	}
}

func TestJavaLexicalStatementRecoveryDepthIsBounded(t *testing.T) {
	const depth = javaMaximumRecoveryStatementDepth * 8
	var source strings.Builder
	source.WriteString(`cl\u0061ss C { void f() { `)
	for range depth {
		source.WriteString("if (ready()) ")
	}
	source.WriteString("target(); } }")

	analysis := javaAuthoritativeRecoveryAnalysis(t, source.String())
	if got, want := javaDefinitionSymbols(analysis.definitions), []string{"C", "f"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("deep recovery definitions = %#v, want %#v", got, want)
	}
	if want := (javaLineScope{start: 1, end: 1}); !slices.Contains(analysis.scopes, want) {
		t.Fatalf("deep recovery scopes = %#v, missing bounded tail %#v", analysis.scopes, want)
	}
}

func TestJavaLexicalLabelIndexPreservesNestedStatementContexts(t *testing.T) {
	t.Parallel()

	const source = `if (ready) outer: middle: inner: target();
condition ? falseLabel : alsoFalse : target();
work(); actual: nested: target();`
	lexed := lexJava(source)
	starts := javaIndexLexicalLabelStarts(
		lexed.tokens, analyzeJavaDelimiters(lexed.tokens),
	)
	got := make([]string, 0)
	for index, label := range starts {
		if label {
			got = append(got, lexed.tokens[index].value)
		}
	}
	if want := []string{"outer", "middle", "inner", "actual", "nested"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("indexed labels = %#v, want %#v", got, want)
	}
}

func TestJavaAuthoritativeRecoveryKeepsDeepNestedLabelScopes(t *testing.T) {
	t.Parallel()

	const depth = 256
	source := javaNestedLabelRecoverySource(depth)
	escaped := strings.Replace(source, "class", `cl\u0061ss`, 1)
	analysis := javaAuthoritativeRecoveryAnalysis(t, escaped)
	for line := 3; line < 3+depth; line++ {
		want := javaLineScope{start: line, end: 3 + depth}
		if !slices.Contains(analysis.scopes, want) {
			t.Fatalf("nested-label scopes missing %#v; count=%d", want, len(analysis.scopes))
		}
	}
}

func TestJavaAuthoritativeRecoveryMatchesDeepStatementSuffixScopes(t *testing.T) {
	t.Parallel()

	const depth = javaMaximumRecoveryStatementDepth + 8
	var source strings.Builder
	source.WriteString("class C {\n void f() {\n")
	for range depth {
		source.WriteString("  if (ready())\n")
	}
	source.WriteString("   target();\n }\n}")
	raw := source.String()
	lineCount := strings.Count(raw, "\n") + 1
	want := make([]javaLineScope, 0, depth+2)
	want = append(want,
		javaLineScope{start: 1, end: lineCount},
		javaLineScope{start: 2, end: lineCount - 1},
	)
	for line := 3; line < lineCount-2; line++ {
		want = append(want, javaLineScope{start: line, end: lineCount - 2})
	}

	plain := analyzeJavaSource(raw, lineCount)
	if plain.tree == nil || len(plain.recoverySpans) != 0 {
		t.Fatalf("plain fixture is not a clean syntax authority: tree=%v recovery=%#v",
			plain.tree != nil, plain.recoverySpans)
	}
	if !reflect.DeepEqual(plain.scopes, want) {
		t.Fatalf("tree scope count = %d, want %d; tail=%#v",
			len(plain.scopes), len(want), plain.scopes[max(0, len(plain.scopes)-12):])
	}
	escapedSource := strings.Replace(raw, "class", `cl\u0061ss`, 1)
	escaped := javaAuthoritativeRecoveryAnalysis(t, escapedSource)
	if !reflect.DeepEqual(escaped.scopes, want) {
		t.Fatalf("lexical scope count = %d, want %d; tail=%#v",
			len(escaped.scopes), len(want), escaped.scopes[max(0, len(escaped.scopes)-12):])
	}
}

func TestJavaLexicalExpressionScopeStopsAtTokenGap(t *testing.T) {
	t.Parallel()

	tokens := javaRecoveryTokens("target", "omitted", ";")
	tokens[1].gap = true
	if end := javaLexicalExpressionEnd(tokens, analyzeJavaDelimiters(tokens), 0); end != -1 {
		t.Fatalf("expression end across token gap = %d, want -1", end)
	}
}

func TestJavaLexicalNestedSwitchRecoveryStaysBounded(t *testing.T) {
	const depth = 2 << 10
	source := javaNestedSwitchRecoverySource(depth)
	escapedSource := strings.Replace(source, "class", `cl\u0061ss`, 1)
	analysis := javaAuthoritativeRecoveryAnalysis(t, escapedSource)
	if got, want := javaDefinitionSymbols(analysis.definitions), []string{"C", "f"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("nested-switch definitions = %#v, want %#v", got, want)
	}
}

func TestJavaAuthoritativeRecoveryKeepsLongValidField(t *testing.T) {
	t.Parallel()

	const terms = 8 << 10
	raw := javaLongFieldRecoverySource(terms, `"x"`)
	plain := analyzeJavaSource(raw, strings.Count(raw, "\n")+1)
	if plain.tree == nil || len(plain.recoverySpans) != 0 {
		t.Fatalf("plain fixture is not a clean syntax authority: tree=%v recovery=%#v",
			plain.tree != nil, plain.recoverySpans)
	}
	if got, want := javaDefinitionSymbols(plain.definitions), []string{"C", "VALUE"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("tree definitions = %#v, want %#v", got, want)
	}

	escapedSource := javaLongFieldRecoverySource(terms, `"\u0078"`)
	escaped := javaAuthoritativeRecoveryAnalysis(t, escapedSource)
	if !reflect.DeepEqual(escaped.definitions, plain.definitions) {
		t.Fatalf("tree definitions=%#v\nlexical definitions=%#v",
			plain.definitions, escaped.definitions)
	}
}

func TestJavaRecoveryIncludesJava25ModuleImports(t *testing.T) {
	t.Parallel()

	const raw = "import module M;"
	rawAnalysis := analyzeJavaSource(raw, 1)
	if rawAnalysis.tree == nil || len(rawAnalysis.recoverySpans) == 0 {
		t.Fatalf("module-import grammar probe changed: tree=%v recovery=%#v",
			rawAnalysis.tree != nil, rawAnalysis.recoverySpans)
	}
	foundError := false
	for _, node := range rawAnalysis.tree.nodes {
		if node.kind == "ERROR" && node.startByte >= 0 && node.endByte <= len(raw) &&
			raw[node.startByte:node.endByte] == "M" {
			foundError = true
			break
		}
	}
	if !foundError {
		t.Fatalf("module-import grammar probe no longer contains ERROR(M): %#v",
			rawAnalysis.recoverySpans)
	}

	tests := []struct {
		name       string
		source     string
		start, end int
	}{
		{"raw simple", raw, 1, 1},
		{"escaped import keyword", `imp\u006frt module M;`, 1, 1},
		{"escaped module keyword", `import mod\u0075le M;`, 1, 1},
		{"attached multiline", "/** doc */\nimport module\n M;", 1, 3},
		{"incomplete", "import module M", 1, 1},
		{"qualified type beginning with module", "import module.api.Type;", 1, 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			lines := javaTestLines(test.source)
			start, end, ok := newJavaLanguage().importRange(lines)
			if !ok || start != test.start || end != test.end {
				t.Fatalf("import range = %d-%d, %v; want %d-%d, true",
					start, end, ok, test.start, test.end)
			}
		})
	}
}

func TestJavaAuthoritativeRecoveryIndexesCompactSourceFields(t *testing.T) {
	t.Parallel()

	const source = `Str\u0069ng greeting = "hi";
void main() {
    IO.println(greeting);
}`
	analysis := javaAuthoritativeRecoveryAnalysis(t, source)
	if got, want := javaDefinitionSymbols(analysis.definitions),
		[]string{"greeting", "main"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("compact source definitions = %#v, want %#v", got, want)
	}
}

func TestJavaAuthoritativeRecoveryIndexesRichCompactFieldsOnly(t *testing.T) {
	t.Parallel()

	const source = `package sample;
import java.util.List;
/** docs */
Runnable first = () -> {
    target();
}, second = new Runnable() {
    public void run() {}
};
int a, b;
Str\u0069ng greeting = "hi";
void main() {}`
	analysis := javaAuthoritativeRecoveryAnalysis(t, source)
	if got, want := javaDefinitionSymbols(analysis.definitions), []string{
		"first", "second", "run", "a", "b", "greeting", "main",
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("rich compact definitions = %#v, want %#v", got, want)
	}
	for _, symbol := range []string{"first", "second"} {
		definition := javaFirstDefinition(t, analysis.definitions, symbol)
		if !definition.ownsScope || definition.scopeStart != 3 || definition.scopeEnd != 8 {
			t.Fatalf("compact scoped field %q = %#v, want owned 3-8", symbol, definition)
		}
	}
	for _, symbol := range []string{"a", "b", "greeting"} {
		if definition := javaFirstDefinition(t, analysis.definitions, symbol); definition.ownsScope {
			t.Fatalf("compact scalar field %q owns scope: %#v", symbol, definition)
		}
	}
}

func TestJavaAuthoritativeRecoverySkipsAnglesInsideTypeParameterAnnotations(t *testing.T) {
	t.Parallel()

	const source = `<@A(flag = 1 > 0) T> void ma\u0069n() {}`
	analysis := javaAuthoritativeRecoveryAnalysis(t, source)
	if got, want := javaDefinitionSymbols(analysis.definitions),
		[]string{`ma\u0069n`}; !reflect.DeepEqual(got, want) {
		t.Fatalf("annotated compact type parameters = %#v, want %#v", got, want)
	}
}

func TestJavaMalformedRepeatedTypeHeadersRemainLinear(t *testing.T) {
	const count = 1 << 17
	source := strings.Repeat("class Identifier ", count)
	analysis := javaRecoveryAnalysis(source)
	if len(analysis.definitions) != 0 {
		t.Fatalf("malformed headers produced definitions: %#v", analysis.definitions)
	}
}

func BenchmarkJavaDelimiterMismatchRecovery(b *testing.B) {
	const count = 32 << 10
	tokens := make([]javaToken, 0, count*2)
	for range count {
		tokens = append(tokens, javaToken{text: "[", value: "["})
	}
	for range count {
		tokens = append(tokens, javaToken{text: ")", value: ")"})
	}
	b.ReportAllocs()
	b.SetBytes(int64(len(tokens)))
	b.ResetTimer()
	for range b.N {
		_ = analyzeJavaDelimiters(tokens)
	}
}

func BenchmarkJavaLexicalNestedTypeRecovery(b *testing.B) {
	const depth = 512
	var source strings.Builder
	for index := range depth {
		source.WriteString("class C")
		source.WriteString(strconv.Itoa(index))
		source.WriteString(" { int field")
		source.WriteString(strconv.Itoa(index))
		source.WriteString(";\n")
	}
	for range depth {
		source.WriteString("}\n")
	}
	text := source.String()
	lexed := lexJava(text)
	lineCount := strings.Count(text, "\n") + 1
	b.ReportAllocs()
	b.SetBytes(int64(len(text)))
	b.ResetTimer()
	for range b.N {
		_ = analyzeJavaLexically(text, lineCount, lexed)
	}
}

func BenchmarkJavaLexicalRepeatedMalformedHeaders(b *testing.B) {
	const count = 1 << 17
	source := strings.Repeat("class Identifier ", count)
	lexed := lexJava(source)
	b.ReportAllocs()
	b.SetBytes(int64(len(source)))
	b.ResetTimer()
	for range b.N {
		_ = analyzeJavaLexically(source, 1, lexed)
	}
}

func BenchmarkJavaLexicalRecoveryRestarts(b *testing.B) {
	const count = 16 << 10
	benchmarks := []struct {
		name   string
		source string
	}{
		{"callable", "class C { " + strings.Repeat("foo() ", count) + "void valid() {} }"},
		{"module", strings.Repeat("module bad ", count) + "module good {}"},
		{"requires", "module good { " + strings.Repeat("requires bad ", count) +
			"requires java.sql; }"},
		{"import", strings.Repeat("import bad ", count) + "import java.util.List;"},
		{"allocation", strings.Repeat("new Bad ", count) + "new Runnable() {};"},
		{"nested braces", strings.Repeat("{", count) + strings.Repeat("}", count)},
	}
	for _, benchmark := range benchmarks {
		b.Run(benchmark.name, func(b *testing.B) {
			lexed := lexJava(benchmark.source)
			b.ReportAllocs()
			b.SetBytes(int64(len(benchmark.source)))
			b.ResetTimer()
			for range b.N {
				_ = analyzeJavaLexically(benchmark.source, 1, lexed)
			}
		})
	}
}

func BenchmarkJavaLexicalNestedStatementRecovery(b *testing.B) {
	const depth = javaMaximumRecoveryStatementDepth * 16
	var source strings.Builder
	source.WriteString(`cl\u0061ss C { void f() { `)
	for range depth {
		source.WriteString("if (ready()) ")
	}
	source.WriteString("target(); } }")
	text := source.String()
	lexed := lexJava(text)
	b.ReportAllocs()
	b.SetBytes(int64(len(text)))
	b.ResetTimer()
	for range b.N {
		_ = analyzeJavaLexically(text, 1, lexed)
	}
}

func BenchmarkJavaLexicalNestedSwitchRecovery(b *testing.B) {
	for _, depth := range []int{256, 1 << 10, 4 << 10} {
		b.Run(strconv.Itoa(depth), func(b *testing.B) {
			text := javaNestedSwitchRecoverySource(depth)
			lexed := lexJava(text)
			b.ReportAllocs()
			b.SetBytes(int64(len(text)))
			b.ResetTimer()
			for range b.N {
				_ = analyzeJavaLexically(text, 1, lexed)
			}
		})
	}
}

func BenchmarkJavaLexicalLongFieldRecovery(b *testing.B) {
	for _, terms := range []int{4 << 10, 16 << 10, 64 << 10} {
		b.Run(strconv.Itoa(terms), func(b *testing.B) {
			text := javaLongFieldRecoverySource(terms, `"x"`)
			lexed := lexJava(text)
			b.ReportAllocs()
			b.SetBytes(int64(len(text)))
			b.ResetTimer()
			for range b.N {
				_ = analyzeJavaLexically(text, 4, lexed)
			}
		})
	}
}

func BenchmarkJavaLexicalScopedFieldAttachmentScaling(b *testing.B) {
	for _, size := range []int{256, 1 << 10, 4 << 10} {
		b.Run(strconv.Itoa(size), func(b *testing.B) {
			text := javaScopedFieldRecoverySource(size, size)
			lexed := lexJava(text)
			lineCount := strings.Count(text, "\n") + 1
			b.ReportAllocs()
			b.SetBytes(int64(len(text)))
			b.ResetTimer()
			for range b.N {
				analysis := analyzeJavaLexically(text, lineCount, lexed)
				if len(analysis.definitions) != size+1 {
					b.Fatalf("definitions = %d, want %d", len(analysis.definitions), size+1)
				}
			}
		})
	}
}

func BenchmarkJavaLexicalNestedLabelRecovery(b *testing.B) {
	for _, depth := range []int{1 << 10, 4 << 10, 16 << 10} {
		b.Run(strconv.Itoa(depth), func(b *testing.B) {
			text := javaNestedLabelRecoverySource(depth)
			lexed := lexJava(text)
			lineCount := strings.Count(text, "\n") + 1
			b.ReportAllocs()
			b.SetBytes(int64(len(text)))
			b.ResetTimer()
			for range b.N {
				_ = analyzeJavaLexically(text, lineCount, lexed)
			}
		})
	}
}

func javaScopedFieldRecoverySource(commentCount, declaratorCount int) string {
	var source strings.Builder
	source.Grow(commentCount*12 + declaratorCount*24 + 64)
	source.WriteString("class C {\n/** shared */\n")
	for index := range commentCount {
		source.WriteString("// bridge ")
		source.WriteString(strconv.Itoa(index))
		source.WriteByte('\n')
	}
	source.WriteString("Runnable ")
	for index := range declaratorCount {
		if index > 0 {
			source.WriteString(", ")
		}
		source.WriteByte('f')
		source.WriteString(strconv.Itoa(index))
		source.WriteString(" = () -> {}")
	}
	source.WriteString(";\n}")
	return source.String()
}

func javaNestedLabelRecoverySource(depth int) string {
	var source strings.Builder
	source.Grow(depth*16 + 64)
	source.WriteString("class C {\n void f() {\n")
	for index := range depth {
		source.WriteByte('l')
		source.WriteString(strconv.Itoa(index))
		source.WriteString(":\n")
	}
	source.WriteString("  target();\n }\n}")
	return source.String()
}

func javaNestedSwitchRecoverySource(depth int) string {
	var source strings.Builder
	source.Grow(depth*32 + 64)
	source.WriteString("class C { void f(int v) { ")
	for range depth {
		source.WriteString("switch (v) { default -> { ")
	}
	source.WriteString("target(); ")
	for range depth {
		source.WriteString("} } ")
	}
	source.WriteString("} }")
	return source.String()
}

func javaLongFieldRecoverySource(terms int, literal string) string {
	var source strings.Builder
	source.Grow(terms*6 + 64)
	source.WriteString("class C {\n static final String VALUE =\n  ")
	for index := range terms {
		if index > 0 {
			source.WriteString(" + ")
		}
		source.WriteString(literal)
	}
	source.WriteString(";\n}")
	return source.String()
}

func javaRecoveryAnalysis(source string) javaLexicalAnalysis {
	return analyzeJavaLexically(source, strings.Count(source, "\n")+1, lexJava(source))
}

func javaAuthoritativeRecoveryAnalysis(t *testing.T, source string) *javaSourceAnalysis {
	t.Helper()
	analysis := analyzeJavaSource(source, strings.Count(source, "\n")+1)
	if !analysis.lexed.translatedEscapes || analysis.tree != nil {
		t.Fatalf("fixture did not force lexical authority: escapes=%v tree=%v",
			analysis.lexed.translatedEscapes, analysis.tree != nil)
	}
	return analysis
}

func javaRecoveryTokens(values ...string) []javaToken {
	tokens := make([]javaToken, 0, len(values))
	for index, value := range values {
		tokens = append(tokens, javaToken{
			text: value, value: value, start: index, end: index + 1,
		})
	}
	return tokens
}

func javaRecoveryToken(value string, identifier bool) javaToken {
	return javaToken{text: value, value: value, identifier: identifier}
}

func javaReferenceDelimiterAnalysis(tokens []javaToken) javaDelimiterAnalysis {
	analysis := javaDelimiterAnalysis{
		pairs:      make([]int, len(tokens)),
		braceOwner: make([]int, len(tokens)),
	}
	for index := range analysis.pairs {
		analysis.pairs[index] = -1
		analysis.braceOwner[index] = -1
	}
	type opener struct {
		value string
		index int
	}
	stack := make([]opener, 0, 32)
	braceStack := make([]int, 0, 16)
	for index, token := range tokens {
		if token.gap {
			stack = stack[:0]
			braceStack = braceStack[:0]
			continue
		}
		if len(braceStack) > 0 {
			analysis.braceOwner[index] = braceStack[len(braceStack)-1]
		}
		switch token.value {
		case "(", "[", "{":
			stack = append(stack, opener{value: token.value, index: index})
			if token.value == "{" {
				braceStack = append(braceStack, index)
			}
		case ")", "]", "}":
			wanted := map[string]string{")": "(", "]": "[", "}": "{"}[token.value]
			match := -1
			for cursor := len(stack) - 1; cursor >= 0; cursor-- {
				if stack[cursor].value == wanted {
					match = cursor
					break
				}
			}
			if match < 0 {
				continue
			}
			openIndex := stack[match].index
			analysis.pairs[openIndex], analysis.pairs[index] = index, openIndex
			stack = stack[:match]
			if wanted != "{" {
				continue
			}
			for len(braceStack) > 0 {
				last := braceStack[len(braceStack)-1]
				braceStack = braceStack[:len(braceStack)-1]
				if last == openIndex {
					break
				}
			}
		}
	}
	return analysis
}
