package navigator

import (
	"slices"
	"strings"
	"testing"
)

func TestKotlin24ContextBackingFieldAndAllTargetRecovery(t *testing.T) {
	t.Parallel()

	const source = `class Store {
    @all:Tracked
    val entries: List<String>
        field = []

    context(logger: Logger)
    fun load(): List<String> = entries

    context(cache: Cache)
    val first: String
        get() = cache.first()
}

context(Logger)
fun legacyReceiver() = Unit

fun caller(logger: Logger) {
    context(logger) { load() }
    load(logger = logger)
}
`
	lines := kotlinTestLines(source)
	definitions := newKotlinLanguage().sourceDefinitions(lines)
	want := []string{"Store", "entries", "load", "first", "legacyReceiver", "caller"}
	if got := kotlinTestDefinitionSymbols(definitions); !slices.Equal(got, want) {
		t.Fatalf("Kotlin 2.4 recovery definitions = %#v, want %#v", got, want)
	}
	for _, forbidden := range []string{
		"all", "Tracked", "field", "logger", "Logger", "cache", "Cache", "context",
	} {
		if slices.Contains(kotlinTestDefinitionSymbols(definitions), forbidden) {
			t.Errorf("context/backing-field syntax promoted %q: %#v", forbidden, definitions)
		}
	}
	kotlinTestAssertDefinitionCoordinates(t, lines, definitions)

	entries := kotlinTestFirstDefinition(t, definitions, "entries")
	if !entries.ownsScope || entries.scopeStart != 2 || entries.scopeEnd != 4 {
		t.Errorf("explicit-backing-field property = %#v, want attributed scope 2-4", entries)
	}
	first := kotlinTestFirstDefinition(t, definitions, "first")
	if !first.ownsScope || first.scopeStart != 9 || first.scopeEnd != 11 {
		t.Errorf("context property = %#v, want scope 9-11", first)
	}
}

func TestKotlinExtensionsGenericsAndDefinitelyNonNullTypes(t *testing.T) {
	t.Parallel()

	const source = `fun <T> List<T>.stableSorted(): List<T> where T : Comparable<T> = this

val <T> List<T>.lastOrNull: T?
    get() = if (isEmpty()) null else this[size - 1]

fun ((Int) -> String).invokeTwice(value: Int): String = this(this(value).length)

inline fun <reified T> decode(value: Any): T = value as T

fun <T> requireDefinitelyNonNull(value: T & Any): T & Any = value

class Box<out T>(val value: T) {
    operator fun plus(other: Box<@UnsafeVariance T>): Box<T> = this
    infix fun join(other: Box<@UnsafeVariance T>): Box<T> = this
}

fun calls(service: Service, values: List<Int>) {
    values.stableSorted()
    values.lastOrNull
    service.Client.render<Result>()
    Box(1)
    with(service) { run() }
}
`
	lines := kotlinTestLines(source)
	definitions := newKotlinLanguage().sourceDefinitions(lines)
	want := []string{
		"stableSorted", "lastOrNull", "invokeTwice", "decode", "requireDefinitelyNonNull",
		"Box", "value", "plus", "join", "calls",
	}
	if got := kotlinTestDefinitionSymbols(definitions); !slices.Equal(got, want) {
		t.Fatalf("generic/extension definitions = %#v, want %#v", got, want)
	}
	for _, forbidden := range []string{
		"T", "Comparable", "List", "Int", "String", "other", "service", "values",
		"stableSorted", // A declaration exists exactly once; the call must not add another.
		"lastOrNull",   // A declaration exists exactly once; the access must not add another.
		"render", "Result", "with", "run",
	} {
		count := 0
		for _, definition := range definitions {
			if definition.symbol == forbidden {
				count++
			}
		}
		wantCount := 0
		if forbidden == "stableSorted" || forbidden == "lastOrNull" {
			wantCount = 1
		}
		if count != wantCount {
			t.Errorf("definition count for %q = %d, want %d: %#v",
				forbidden, count, wantCount, definitions)
		}
	}
	kotlinTestAssertDefinitionCoordinates(t, lines, definitions)
}

func TestKotlinAnnotationsAttachScopesWithoutInventingDefinitions(t *testing.T) {
	t.Parallel()

	const source = `@file:[JvmName("Annotated") Suppress("unused")]
package demo

/** Service documentation. */
@Deprecated("old")
class Service(
    @all:Tracked
    @param:Inject("dependency")
    val dependency: Dependency,
) {
    /** Property documentation. */
    @get:Tracked
    @set:Tracked
    @field:Tracked
    @delegate:Tracked
    var value: String by delegate()
        private set

    /** Function documentation. */
    @Trace(
        name = "run",
    )
    fun run(@param:Inject("input") input: String) = input
}
`
	lines := kotlinTestLines(source)
	definitions := newKotlinLanguage().sourceDefinitions(lines)
	want := []string{"Service", "dependency", "value", "run"}
	if got := kotlinTestDefinitionSymbols(definitions); !slices.Equal(got, want) {
		t.Fatalf("annotated definitions = %#v, want %#v", got, want)
	}
	for _, forbidden := range []string{
		"JvmName", "Suppress", "Deprecated", "all", "Tracked", "param", "Inject",
		"get", "set", "field", "delegate", "Trace", "name", "input", "Dependency",
	} {
		if slices.Contains(kotlinTestDefinitionSymbols(definitions), forbidden) {
			t.Errorf("annotation/type/parameter %q became definition: %#v",
				forbidden, definitions)
		}
	}

	service := kotlinTestFirstDefinition(t, definitions, "Service")
	if service.scopeStart != 4 || service.scopeEnd != len(lines) {
		t.Errorf("annotated Service scope = %#v, want 4-%d", service, len(lines))
	}
	value := kotlinTestFirstDefinition(t, definitions, "value")
	if !value.ownsScope || value.scopeStart != 11 || value.scopeEnd != 17 {
		t.Errorf("annotated property scope = %#v, want 11-17", value)
	}
	run := kotlinTestFirstDefinition(t, definitions, "run")
	if !run.ownsScope || run.scopeStart != 19 || run.scopeEnd != 23 {
		t.Errorf("annotated function scope = %#v, want 19-23", run)
	}
	kotlinTestAssertDefinitionCoordinates(t, lines, definitions)
}

func TestKotlinNestedCommentsTemplatesAndMultiDollarMasking(t *testing.T) {
	t.Parallel()

	const source = `/* outer target and class CommentFake {
   /* nested target and fun NestedFake() {} */
   trailing target }
*/
class Strings {
    val ordinary = "literal target ${target()} and $target"
    val raw = """literal target // raw ${target()} class RawFake {}"""
    val multi = $$"""literal $target and real $${target()}"""

    fun target(): String = "ok"

    fun tail() {
        // target in line comment
        /* target in block comment */ target()
    }
}
`
	lines := kotlinTestLines(source)
	backend := prepareLanguageBackend(newKotlinLanguage(), lines)
	if got, want := kotlinTestDefinitionSymbols(backend.sourceDefinitions(lines)),
		[]string{"Strings", "ordinary", "raw", "multi", "target", "tail"}; !slices.Equal(got, want) {
		t.Fatalf("string/comment definitions = %#v, want %#v", got, want)
	}

	searchable := backend.searchLines(lines, true, true)
	if len(searchable) != len(lines) {
		t.Fatalf("search lines = %d, want %d", len(searchable), len(lines))
	}
	for index, line := range searchable {
		if len(line) != len(lines[index]) {
			t.Fatalf("search masking changed line %d byte width from %d to %d",
				index+1, len(lines[index]), len(line))
		}
	}
	counter := backend.(symbolOccurrenceCounter)
	wantCounts := map[int]int{
		kotlinTestLineContaining(t, lines, `val ordinary`): 2,
		kotlinTestLineContaining(t, lines, `val raw`):      1,
		kotlinTestLineContaining(t, lines, `val multi`):    1,
		kotlinTestLineContaining(t, lines, `fun target`):   1,
	}
	wantCounts[kotlinTestLineContaining(t, lines, `/* target in block comment */`)] = 1
	for index, line := range searchable {
		if got, want := counter.countSymbolOccurrences(line, "target"), wantCounts[index+1]; got != want {
			t.Errorf("masked line %d target count = %d, want %d; line=%q",
				index+1, got, want, line)
		}
	}

	commentsOnly := backend.searchLines(lines, true, false)
	if !strings.Contains(commentsOnly[5], "literal target") ||
		strings.Contains(strings.Join(commentsOnly[:4], "\n"), "target") ||
		strings.Contains(commentsOnly[12], "target in line comment") {
		t.Fatalf("comment-only masking confused comments and literals: %#v", commentsOnly)
	}
	stringsOnly := backend.searchLines(lines, false, true)
	if strings.Contains(stringsOnly[5], "literal target") ||
		!strings.Contains(stringsOnly[12], "target in line comment") {
		t.Fatalf("string-only masking confused comments and literals: %#v", stringsOnly)
	}

	cleaner := backend.(linePreservingSourceCleaner)
	cleaned := cleaner.cleanSourceLines(lines, true, false)
	if len(cleaned) != len(lines) ||
		strings.Contains(strings.Join(cleaned[:4], "\n"), "target") ||
		!strings.Contains(cleaned[5], "literal target") {
		t.Fatalf("line-preserving nested-comment cleaning = %#v", cleaned)
	}
}

func TestKotlinAnnotatedEnumEntriesExcludeAnnotationNames(t *testing.T) {
	t.Parallel()

	const source = `enum class State { @Mark("ready") Ready, @demo.Other Busy }`
	lines := kotlinTestLines(source)
	want := []string{"State", "Ready", "Busy"}
	for name, definitions := range map[string][]sourceDefinition{
		"lexical": analyzeKotlinLexically(source, len(lines)).definitions,
		"merged":  newKotlinLanguage().sourceDefinitions(lines),
	} {
		if got := kotlinTestDefinitionSymbols(definitions); !slices.Equal(got, want) {
			t.Errorf("%s annotated enum definitions = %#v, want %#v", name, got, want)
		}
	}
}

func TestKotlinNumericReceiversAndInterpolationPrefixCapRemainSearchable(t *testing.T) {
	t.Parallel()

	const numeric = `val values = listOf(1.toString(), 0xFF.toInt(), 42L.hashCode(), 1.5.compareTo(0.0), 0b10.countOneBits(), 123fake)`
	counter := newKotlinLanguage()
	for _, symbol := range []string{
		"toString", "toInt", "hashCode", "compareTo", "countOneBits",
	} {
		if got := counter.countSymbolOccurrences(numeric, symbol); got != 1 {
			t.Errorf("numeric receiver %q count = %d, want 1", symbol, got)
		}
	}
	if got := counter.countSymbolOccurrences(numeric, "fake"); got != 0 {
		t.Errorf("malformed numeric tail count = %d, want 0", got)
	}

	prefix := strings.Repeat("$", kotlinMaximumInterpolationPrefix+1)
	trigger := strings.Repeat("$", kotlinMaximumInterpolationPrefix)
	lines := []string{
		"val target = 1",
		"val text = " + prefix + `"opaque ` + trigger + `{target}"`,
	}
	searchable := newKotlinLanguage().searchLines(lines, true, true)
	if strings.Contains(searchable[1], "opaque") ||
		counter.countSymbolOccurrences(searchable[1], "target") != 1 {
		t.Fatalf("capped multi-dollar interpolation mask = %q", searchable[1])
	}
}

func TestKotlinQuotedKeywordAndPunctuationNamesStayIdentifiers(t *testing.T) {
	t.Parallel()

	const source = "val `class` = 1\nval `{` = 2\nfun `}`() = Unit\n" +
		"val `typealias` = 3\nclass Tail\n"
	want := []string{"class", "{", "}", "typealias", "Tail"}
	lines := kotlinTestLines(source)
	for name, definitions := range map[string][]sourceDefinition{
		"lexical": analyzeKotlinLexically(source, len(lines)).definitions,
		"merged":  newKotlinLanguage().sourceDefinitions(lines),
	} {
		if got := kotlinTestDefinitionSymbols(definitions); !slices.Equal(got, want) {
			t.Errorf("%s quoted definitions = %#v, want %#v", name, got, want)
		}
	}

	const findSource = "fun `when`() {}\nfun caller(value: Int) {\n" +
		"    when (value) { else -> Unit }\n    `when`()\n}\n"
	root := t.TempDir()
	writeFile(t, root, "Quoted.kt", findSource)
	response, err := mustView(t, root).Find(
		"when", Options{Include: IncludeBoth, Return: ReturnLocations},
	)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := kotlinTestResultLines(response.Results), []int{1, 4}; !slices.Equal(got, want) {
		t.Fatalf("Find quoted hard keyword lines = %#v, want %#v; results=%#v",
			got, want, response.Results)
	}
}

func TestKotlinInvalidSimpleTemplatesStayOpaque(t *testing.T) {
	t.Parallel()

	line := "val text = \"$class $this $`fake` $field\""
	masked := newKotlinLanguage().searchLines([]string{line}, true, true)[0]
	counter := newKotlinLanguage()
	for _, hidden := range []string{"class", "this", "fake"} {
		if got := counter.countSymbolOccurrences(masked, hidden); got != 0 {
			t.Errorf("invalid simple template %q count = %d in %q", hidden, got, masked)
		}
	}
	if got := counter.countSymbolOccurrences(masked, "field"); got != 1 {
		t.Errorf("valid soft-keyword template count = %d in %q, want 1", got, masked)
	}
}

func TestKotlinRecoverySkipsDefaultLambdaBodiesInsideDeclarationHeaders(t *testing.T) {
	t.Parallel()

	const source = `class Callbacks(
    val callback: () -> Unit = {
        class DefaultLocal
        fun defaultLocal() = Unit
        defaultWork()
    },
) {
    fun run(
        block: () -> Unit = {
            class NestedDefault
            nestedDefault()
        },
    ) {
        realBody()
    }
}
class Tail
`
	lines := kotlinTestLines(source)
	definitions := analyzeKotlinLexically(source, len(lines)).definitions
	if got, want := kotlinTestDefinitionSymbols(definitions),
		[]string{
			"Callbacks", "callback", "DefaultLocal", "defaultLocal", "run",
			"NestedDefault", "Tail",
		}; !slices.Equal(got, want) {
		t.Fatalf("default-lambda lexical definitions = %#v, want %#v", got, want)
	}
	callbacks := kotlinTestFirstDefinition(t, definitions, "Callbacks")
	if !callbacks.ownsScope || callbacks.scopeEnd != 16 {
		t.Errorf("Callbacks scope = %#v, want through actual class close line 16", callbacks)
	}
	callback := kotlinTestFirstDefinition(t, definitions, "callback")
	if callback.ownsScope || callback.scopeStart != 2 || callback.scopeEnd != 2 {
		t.Errorf("constructor property scope = %#v, want non-owning line 2", callback)
	}
	run := kotlinTestFirstDefinition(t, definitions, "run")
	if !run.ownsScope || run.scopeStart != 8 || run.scopeEnd != 15 {
		t.Errorf("run scope = %#v, want actual body 8-15", run)
	}

	const unterminated = "fun broken(block: () -> Unit = {\n    work()\nclass Tail\n"
	broken := analyzeKotlinLexically(unterminated, 3).definitions
	if got := kotlinTestDefinitionSymbols(broken); !slices.Contains(got, "Tail") {
		t.Fatalf("unterminated default lambda lost tail declaration: %#v", broken)
	}

	const annotated = `fun annotated(
    @Ann block: () -> Unit,
) {
    block()
}
class Box<
    @Ann T,
> {
}
`
	annotatedLines := kotlinTestLines(annotated)
	annotatedDefinitions := analyzeKotlinLexically(
		annotated, len(annotatedLines),
	).definitions
	if got, want := kotlinTestDefinitionSymbols(annotatedDefinitions),
		[]string{"annotated", "Box"}; !slices.Equal(got, want) {
		t.Fatalf("annotated multiline header definitions = %#v, want %#v", got, want)
	}
	if definition := kotlinTestFirstDefinition(t, annotatedDefinitions, "annotated"); definition.scopeEnd != 5 {
		t.Errorf("annotated function scope = %#v, want through line 5", definition)
	}
	if definition := kotlinTestFirstDefinition(t, annotatedDefinitions, "Box"); definition.scopeStart != 6 || definition.scopeEnd != 9 {
		t.Errorf("annotated generic class scope = %#v, want 6-9", definition)
	}
}

func TestKotlinIdentifiersPreserveRawSourceIdentityAndCoordinates(t *testing.T) {
	t.Parallel()

	const sourcePrefix = `class Κατάλογος {
    val café = 1
    fun field() {}
    fun `
	const sourceSuffix = `() {}
    val `
	const source = sourcePrefix + "`when`" + sourceSuffix + "`name with spaces`" + ` = 1
}
`
	lines := kotlinTestLines(source)
	definitions := newKotlinLanguage().sourceDefinitions(lines)
	want := []string{"Κατάλογος", "café", "field", "when", "name with spaces"}
	if got := kotlinTestDefinitionSymbols(definitions); !slices.Equal(got, want) {
		t.Fatalf("Unicode/backtick definitions = %#v, want %#v", got, want)
	}
	kotlinTestAssertDefinitionCoordinates(t, lines, definitions)

	root := t.TempDir()
	writeFile(t, root, "Identifiers.kt", source)
	view := mustView(t, root)
	for _, raw := range []string{"Κατάλογος", "café", "when", "name with spaces"} {
		response, err := view.Find(raw, Options{Include: IncludeDefs, Return: ReturnLocations})
		if err != nil {
			t.Fatal(err)
		}
		if len(response.Results) != 1 || response.Results[0].Symbol != raw {
			t.Errorf("raw Find(%q) = %#v, want exact definition", raw, response.Results)
		}
	}
	for _, partial := range []string{"`when`", "name", "caféSuffix"} {
		response, err := view.Find(partial, Options{Include: IncludeDefs, Return: ReturnLocations})
		if err != nil {
			t.Fatal(err)
		}
		if len(response.Results) != 0 {
			t.Errorf("partial/canonicalized query %q matched raw identifier: %#v",
				partial, response.Results)
		}
	}
}

func TestKotlinScopesPreferSmallestBlockAndNamedOwner(t *testing.T) {
	t.Parallel()

	const source = `/** Service documentation. */
class Service {
    val value: String
        get() {
            if (ready()) {
                return target()
            }
            return fallback()
        }

    fun run() {
        values.forEach { value ->
            when (value) {
                1 -> {
                    target()
                }
                else -> Unit
            }
        }
    }
}
`
	lines := kotlinTestLines(source)
	backend := prepareLanguageBackend(newKotlinLanguage(), lines)
	if start, end := backend.enclosingScope(lines, 6); start != 5 || end != 7 {
		t.Fatalf("getter if scope = %d-%d, want 5-7", start, end)
	}
	resolver := backend.(navigationScopeResolver)
	if start, end := resolver.navigationScope(lines, 6); start != 3 || end != 9 {
		t.Fatalf("getter navigation scope = %d-%d, want property 3-9", start, end)
	}
	if start, end := backend.enclosingScope(lines, 15); start != 14 || end != 16 {
		t.Fatalf("when-entry scope = %d-%d, want 14-16", start, end)
	}
	if start, end := resolver.navigationScope(lines, 15); start != 11 || end != 20 {
		t.Fatalf("run navigation scope = %d-%d, want 11-20", start, end)
	}
	if got := scopeName(lines, 6, backend); got != "value" {
		t.Fatalf("getter scope name = %q, want value", got)
	}
	if got := scopeName(lines, 15, backend); got != "run" {
		t.Fatalf("when-entry scope name = %q, want run", got)
	}
}
