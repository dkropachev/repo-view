package navigator

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestKotlinControlFlowCallsAndDSLExpressionsNeverBecomeDefinitions(t *testing.T) {
	t.Parallel()

	const source = `class Calls {
    fun run(service: Service, values: List<Int>) {
        if (ready()) { Target() }
        when (service.state()) {
            State.Ready -> Target()
            else -> Unit
        }
        while (ready()) { Target() }
        do { Target() } while (ready())
        for (item in values) { Target(item) }
        try { Target() } catch (error: Exception) { Target(error) }
        with(service) { Target() }
        context(service) { Target() }
        Target()
        service.Target()
        service.Client.render<Result>()
        values.map { item -> item.transform() }
        val created = Constructed()
        val built = factory.build()
        val delegated by lazy { Target() }
        val callback = fun(value: Int) = value
        val reference = service::Target
        val type = Target::class
        get()
        set()
        run { Target() }
        apply { Target() }
    }
}
`
	definitions := newKotlinLanguage().sourceDefinitions(kotlinTestLines(source))
	if got, want := kotlinTestDefinitionSymbols(definitions),
		[]string{"Calls", "run"}; !slices.Equal(got, want) {
		t.Fatalf("call/control-flow definitions = %#v, want %#v", got, want)
	}
}

func TestKotlinContextualAndSoftKeywordsRemainIdentifiers(t *testing.T) {
	t.Parallel()

	const source = `class Contextual {
    val field = 1
    val value = 2
    val actual = 3
    val expect = 4

    fun data() = field
    fun context() = value
    fun get() = actual
    fun set() = expect

    fun run() {
        data()
        context()
        get()
        set()
        field.toString()
        value.hashCode()
    }
}
`
	definitions := newKotlinLanguage().sourceDefinitions(kotlinTestLines(source))
	want := []string{
		"Contextual", "field", "value", "actual", "expect", "data", "context", "get",
		"set", "run",
	}
	if got := kotlinTestDefinitionSymbols(definitions); !slices.Equal(got, want) {
		t.Fatalf("soft-keyword definitions = %#v, want %#v", got, want)
	}
	for _, symbol := range []string{"data", "context", "get", "set"} {
		count := 0
		for _, definition := range definitions {
			if definition.symbol == symbol {
				count++
			}
		}
		if count != 1 {
			t.Errorf("soft-keyword %q definition count = %d, want 1", symbol, count)
		}
	}
}

func TestKotlinFindClassifiesDefinitionsReferencesAndTemplateExpressions(t *testing.T) {
	t.Parallel()

	const source = `class Search {
    val literal = "target in literal"

    fun target(value: Int): Int = value

    fun caller(): Int {
        val text = "literal target ${target(1)}"
        // target in comment
        return target(2)
    }
}
`
	root := t.TempDir()
	writeFile(t, root, "Search.kt", source)
	view := mustView(t, root)
	response, err := view.Find("target", Options{
		Include:    IncludeBoth,
		Return:     ReturnLocations,
		NoComments: true,
		NoStrings:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantLines := []int{4, 7, 9}
	if got := kotlinTestResultLines(response.Results); !slices.Equal(got, wantLines) {
		t.Fatalf("Find target lines = %#v, want %#v", got, wantLines)
	}
	if got, want := kotlinTestResultKinds(response.Results),
		[]string{"def", "ref", "ref"}; !slices.Equal(got, want) {
		t.Fatalf("Find target kinds = %#v, want %#v", got, want)
	}

	partial, err := view.Find("targ", Options{Include: IncludeBoth, Return: ReturnLocations})
	if err != nil {
		t.Fatal(err)
	}
	if len(partial.Results) != 0 {
		t.Fatalf("partial Kotlin identifier matched: %#v", partial.Results)
	}

	inspected, err := view.Inspect(
		"Search.kt:9",
		Options{Include: IncludeScope, Return: ReturnScope},
	)
	if err != nil {
		t.Fatal(err)
	}
	if inspected.Symbol != "target" || len(inspected.Results) != 1 ||
		inspected.Results[0].Scope != "caller" || inspected.Results[0].StartLine != 6 ||
		inspected.Results[0].EndLine != 10 {
		t.Fatalf("Inspect target call = %#v, want target in caller at 6-10", inspected)
	}
}

func TestKotlinMalformedSourcesRecoverIndependentDeclarationsWithoutCallPhantoms(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		source    string
		required  []string
		forbidden []string
	}{
		{
			name: "broken parameter list",
			source: `class Owner {
    fun before() {}
    fun broken(
    ???
    fun after() { Target() }
}
`,
			required:  []string{"Owner", "before", "after"},
			forbidden: []string{"Target"},
		},
		{
			name: "ordinary string ends at newline",
			source: `class Before
val broken = "unterminated
class After {
    fun tail() { Target() }
}
`,
			required:  []string{"Before", "After", "tail"},
			forbidden: []string{"unterminated", "Target"},
		},
		{
			name: "stray closing delimiters",
			source: `class Before
} ] )
class After {
    fun tail() {}
}
`,
			required: []string{"Before", "After", "tail"},
		},
		{
			name: "broken generic and where clause",
			source: `class Before
class Broken<T
fun <R tail(value: R) where R Comparable<R> = value
class After { fun recovered() {} }
`,
			required:  []string{"Before", "After", "recovered"},
			forbidden: []string{"T", "R", "Comparable", "value"},
		},
		{
			name: "broken context header",
			source: `class Before
context(logger: Logger
fun broken() { service.Client.call() }
class After { fun recovered() {} }
`,
			required:  []string{"Before", "After", "recovered"},
			forbidden: []string{"logger", "Logger", "Client", "call"},
		},
		{
			name: "broken annotation",
			source: `class Before
@Broken(
class After { fun recovered() {} }
`,
			required:  []string{"Before", "After", "recovered"},
			forbidden: []string{"Broken"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			lines := kotlinTestLines(test.source)
			backend := prepareLanguageBackend(newKotlinLanguage(), lines)
			definitions := backend.sourceDefinitions(lines)
			symbols := kotlinTestDefinitionSymbols(definitions)
			for _, required := range test.required {
				if !slices.Contains(symbols, required) {
					t.Errorf("malformed source lost %q: %#v", required, definitions)
				}
			}
			for _, forbidden := range test.forbidden {
				if slices.Contains(symbols, forbidden) {
					t.Errorf("malformed source promoted %q: %#v", forbidden, definitions)
				}
			}
			kotlinTestAssertDefinitionCoordinates(t, lines, definitions)
			kotlinTestAssertMaskCoordinates(t, backend, lines)
		})
	}
}

func TestKotlinScriptRecoveryKeepsDeclarationsAfterMalformedDSL(t *testing.T) {
	t.Parallel()

	const source = `plugins {
    kotlin("jvm"
}

repositories { mavenCentral() }

class ScriptTail

fun tail() {
    Target()
}
`
	root := t.TempDir()
	writeFile(t, root, "broken.gradle.kts", source)
	outline, err := mustView(t, root).Outline(
		"broken.gradle.kts",
		Options{Return: ReturnLocations},
	)
	if err != nil {
		t.Fatal(err)
	}
	symbols := kotlinTestResultSymbols(outline.Results)
	for _, required := range []string{"ScriptTail", "tail"} {
		if !slices.Contains(symbols, required) {
			t.Errorf("malformed script lost %q: %#v", required, outline.Results)
		}
	}
	for _, forbidden := range []string{
		"plugins", "kotlin", "repositories", "mavenCentral", "Target",
	} {
		if slices.Contains(symbols, forbidden) {
			t.Errorf("malformed script promoted call %q: %#v", forbidden, outline.Results)
		}
	}
}

func TestKotlinInvalidUTF8AndIncompleteInputsNeverPanic(t *testing.T) {
	t.Parallel()

	invalidUTF8 := "class Before\nval payload = \"" + string([]byte{0xff, 0xfe}) +
		"\"\nclass After\n"
	corpus := []string{
		"",
		"class Open {\n",
		"fun open<T(\n",
		"context(logger: Logger\nfun open() {\n",
		"val raw = \"\"\"unterminated\n",
		"/* outer /* nested */ unterminated\nclass Hidden\n",
		"\ufeffpackage demo\r\nclass Ready\r\n",
		invalidUTF8,
	}
	for index, source := range corpus {
		t.Run(fmt.Sprintf("case_%d", index), func(t *testing.T) {
			t.Parallel()
			lines := kotlinTestLines(source)
			backend := prepareLanguageBackend(newKotlinLanguage(), lines)
			definitions := backend.sourceDefinitions(lines)
			kotlinTestAssertDefinitionCoordinates(t, lines, definitions)
			kotlinTestAssertMaskCoordinates(t, backend, lines)

			_, _, _ = backend.importRange(lines)
			_ = backend.ignoredSearchLines(lines, true, false)
			_ = backend.cleanSource(source, true, false)
			for lineNo := 1; lineNo <= len(lines); lineNo++ {
				start, end := backend.enclosingScope(lines, lineNo)
				if start < 1 || start > lineNo || end < lineNo || end > len(lines) {
					t.Fatalf("invalid scope for line %d: %d-%d of %d",
						lineNo, start, end, len(lines))
				}
				if resolver, ok := backend.(navigationScopeResolver); ok {
					navigationStart, navigationEnd := resolver.navigationScope(lines, lineNo)
					if navigationStart < 1 || navigationStart > lineNo ||
						navigationEnd < lineNo || navigationEnd > len(lines) {
						t.Fatalf("invalid navigation scope for line %d: %d-%d of %d",
							lineNo, navigationStart, navigationEnd, len(lines))
					}
				}
				_, _ = backend.definitionSymbol(lines[lineNo-1])
				_ = backend.stripComment(lines[lineNo-1])
			}
		})
	}
}

func TestKotlinLexicalFallbackFinalizesPlainLiteralInitializers(t *testing.T) {
	const source = "val text = \"ok\"\nval character = 'x'\nclass Tail\n"
	analysis := analyzeKotlinLexically(source, len(kotlinTestLines(source)))
	if got, want := kotlinTestDefinitionSymbols(analysis.definitions),
		[]string{"text", "character", "Tail"}; !slices.Equal(got, want) {
		t.Fatalf("plain-literal fallback definitions = %#v, want %#v", got, want)
	}
	for _, definition := range analysis.definitions {
		if definition.symbol != "Tail" && (definition.ownsScope ||
			definition.scopeStart != definition.line || definition.scopeEnd != definition.line) {
			t.Fatalf("plain-literal property owns a scope: %#v", definition)
		}
	}
}

func TestKotlinInterpolationFallbackPreservesNavigationAndDeclarationBoundaries(
	t *testing.T,
) {
	const source = `class Host {
    val constructor = 1
    val simple = "$constructor"
    val text = "${
        constructor()
    }"
}`
	cleanLines := kotlinTestLines(source)
	fallbackLines := kotlinTestLines(source +
		strings.Repeat(" ", kotlinMaximumConcreteParseBytes+1))
	clean := prepareLanguageBackend(newKotlinLanguage(), cleanLines)
	fallback := prepareLanguageBackend(newKotlinLanguage(), fallbackLines)
	targetLine := kotlinTestLineContaining(t, cleanLines, "constructor()")
	cleanStart, cleanEnd := clean.enclosingScope(cleanLines, targetLine)
	fallbackStart, fallbackEnd := fallback.enclosingScope(fallbackLines, targetLine)
	if cleanStart != fallbackStart || cleanEnd != fallbackEnd {
		t.Fatalf("interpolation fallback scope = %d-%d, clean scope %d-%d",
			fallbackStart, fallbackEnd, cleanStart, cleanEnd)
	}
	if got, want := kotlinTestDefinitionSymbols(fallback.sourceDefinitions(fallbackLines)),
		[]string{"Host", "constructor", "simple", "text"}; !slices.Equal(got, want) {
		t.Fatalf("interpolation fallback definitions = %#v, want %#v", got, want)
	}
}

func TestKotlinLiteralSentinelsPreserveExpressionAndDelegationScopes(t *testing.T) {
	const prefix = `fun render() = "a" +
    "b"

class Delegated : CharSequence by "abc" {
    constructor() : this("def")
    val member = 1
}
class Tail`
	source := prefix + strings.Repeat(" ", kotlinMaximumConcreteParseBytes+1)
	lines := kotlinTestLines(source)
	analysis := analyzeKotlinSource(source, len(lines))
	if analysis.tree != nil {
		t.Fatal("literal-sentinel fixture unexpectedly retained a concrete tree")
	}
	if got, want := kotlinTestDefinitionSymbols(analysis.definitions),
		[]string{"render", "Delegated", "constructor", "member", "Tail"}; !slices.Equal(got, want) {
		t.Fatalf("literal-sentinel fallback definitions = %#v, want %#v", got, want)
	}
	for _, definition := range analysis.definitions {
		switch definition.symbol {
		case "render":
			if !definition.ownsScope || definition.scopeStart != 1 || definition.scopeEnd != 2 {
				t.Fatalf("multiline expression function scope = %#v, want lines 1-2", definition)
			}
		case "Delegated":
			if !definition.ownsScope || definition.scopeEnd < 7 {
				t.Fatalf("delegated class lost real body scope: %#v", definition)
			}
		}
	}
}

func TestKotlinExpressionBodyControlBracesPreserveFunctionScope(t *testing.T) {
	const prefix = `fun choose(flag: Boolean) = if (flag) {
    1
} else {
    2
}

fun mapped() = run {
    work()
}.toString()

fun configured() = object {
    val member = 1
}.apply {
    val local = 2
}

fun wrapped() = consume(object {
    val nestedMember = 1
}) {
    val callbackLocal = 2
}

fun derived() = object : Base({ val ctorLocal = 1 }) {
    val derivedMember = 2
}

fun nested() = object : Base(object {
    val innerMember = 1
}) {
    val outerMember = 2
}

fun delegated() = object : Interface by object : Interface {
    val delegateMember = 1
} {
    val ownerMember = 2
}

class Tail`
	source := prefix + strings.Repeat(" ", kotlinMaximumConcreteParseBytes+1)
	lines := kotlinTestLines(source)
	analysis := analyzeKotlinSource(source, len(lines))
	if analysis.tree != nil {
		t.Fatal("expression-body control fixture unexpectedly retained a concrete tree")
	}
	if got, want := kotlinTestDefinitionSymbols(analysis.definitions),
		[]string{
			"choose", "mapped", "configured", "member", "wrapped", "nestedMember",
			"derived", "derivedMember", "nested", "innerMember", "outerMember",
			"delegated", "delegateMember", "ownerMember", "Tail",
		}; !slices.Equal(got, want) {
		t.Fatalf("expression-body fallback definitions = %#v, want %#v", got, want)
	}
	for _, definition := range analysis.definitions {
		switch definition.symbol {
		case "choose":
			if !definition.ownsScope || definition.scopeStart != 1 || definition.scopeEnd != 5 {
				t.Fatalf("if-expression function scope = %#v, want lines 1-5", definition)
			}
		case "mapped":
			if !definition.ownsScope || definition.scopeStart != 7 || definition.scopeEnd != 9 {
				t.Fatalf("run-expression function scope = %#v, want lines 7-9", definition)
			}
		case "configured":
			if !definition.ownsScope || definition.scopeStart != 11 || definition.scopeEnd != 15 {
				t.Fatalf("anonymous-object function scope = %#v, want lines 11-15", definition)
			}
		case "wrapped":
			if !definition.ownsScope || definition.scopeStart != 17 || definition.scopeEnd != 21 {
				t.Fatalf("call-wrapped object function scope = %#v, want lines 17-21", definition)
			}
		case "derived":
			if !definition.ownsScope || definition.scopeStart != 23 || definition.scopeEnd != 25 {
				t.Fatalf("supertype-lambda object scope = %#v, want lines 23-25", definition)
			}
		case "nested":
			if !definition.ownsScope || definition.scopeStart != 27 || definition.scopeEnd != 31 {
				t.Fatalf("nested-object function scope = %#v, want lines 27-31", definition)
			}
		case "delegated":
			if !definition.ownsScope || definition.scopeStart != 33 || definition.scopeEnd != 37 {
				t.Fatalf("same-depth nested-object scope = %#v, want lines 33-37", definition)
			}
		}
	}
}

func kotlinTestAssertMaskCoordinates(
	t *testing.T,
	backend languageBackend,
	lines []string,
) {
	t.Helper()
	for _, options := range [][2]bool{
		{false, false}, {true, false}, {false, true}, {true, true},
	} {
		searchable := backend.searchLines(lines, options[0], options[1])
		if len(searchable) != len(lines) {
			t.Fatalf("searchable lines = %d, want %d", len(searchable), len(lines))
		}
		for index, line := range searchable {
			if len(line) != len(lines[index]) {
				t.Fatalf("search masking changed line %d byte width from %d to %d",
					index+1, len(lines[index]), len(line))
			}
		}
	}
}

func kotlinTestResultLines(results []Result) []int {
	lines := make([]int, len(results))
	for index, result := range results {
		lines[index] = result.Line
	}
	return lines
}

func kotlinTestResultKinds(results []Result) []string {
	kinds := make([]string, len(results))
	for index, result := range results {
		kinds[index] = result.Kind
	}
	return kinds
}
