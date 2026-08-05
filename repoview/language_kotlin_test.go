package repoview

import (
	"slices"
	"strings"
	"testing"
)

func TestKotlinBackendContractRegistrationAndPublicIntegration(t *testing.T) {
	t.Parallel()

	backend := newKotlinLanguage()
	if backend.name() != "kt" {
		t.Fatalf("language name = %q, want kt", backend.name())
	}
	contracts := []struct {
		name        string
		implemented bool
	}{
		{name: "sourceBackendPreparer", implemented: kotlinTestImplements[sourceBackendPreparer](backend)},
		{name: "findScopeResolverPreparer", implemented: kotlinTestImplements[findScopeResolverPreparer](backend)},
		{name: "linePreservingSourceCleaner", implemented: kotlinTestImplements[linePreservingSourceCleaner](backend)},
		{name: "navigationScopeResolver", implemented: kotlinTestImplements[navigationScopeResolver](backend)},
		{name: "sourceScopeNameResolver", implemented: kotlinTestImplements[sourceScopeNameResolver](backend)},
		{name: "symbolOccurrenceCounter", implemented: kotlinTestImplements[symbolOccurrenceCounter](backend)},
		{name: "sourceSymbolOccurrenceAugmenter", implemented: kotlinTestImplements[sourceSymbolOccurrenceAugmenter](backend)},
		{name: "authoritativeSymbolOnLineResolver", implemented: kotlinTestImplements[authoritativeSymbolOnLineResolver](backend)},
	}
	for _, contract := range contracts {
		if !contract.implemented {
			t.Errorf("Kotlin backend does not implement %s", contract.name)
		}
	}

	for _, extension := range []string{".kt", ".kts"} {
		registered := languageForExtension(extension)
		if registered.name() != "kt" {
			t.Errorf("registered %s language = %q, want kt", extension, registered.name())
		}
		if _, generic := registered.(braceLanguage); generic {
			t.Errorf("registered %s still uses generic braceLanguage", extension)
		}
		if !defaultExtensions()[extension] {
			t.Errorf("registered %s is absent from default discovery", extension)
		}
	}

	const source = `package demo.navigation
import kotlin.collections.List as KotlinList

class Service(val id: Int) {
    val value = 1

    fun run() {
        Target()
    }
}
`
	root := t.TempDir()
	writeFile(t, root, "Service.kt", source)
	view := mustView(t, root)
	outline, err := view.Outline("Service.kt", Options{Return: ReturnLocations})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := kotlinTestResultSymbols(outline.Results),
		[]string{"Service", "id", "value", "run"}; !slices.Equal(got, want) {
		t.Fatalf("Kotlin outline symbols = %#v, want %#v", got, want)
	}
	for _, result := range outline.Results {
		if result.Kind != "def" || result.Language != "kt" || result.Path != "Service.kt" {
			t.Errorf("malformed Kotlin outline result: %#v", result)
		}
	}

	found, err := view.Find("Target", Options{
		Include: IncludeRefs,
		Return:  ReturnScope,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found.Results) != 1 || found.Results[0].Scope != "run" ||
		found.Results[0].StartLine != 7 || found.Results[0].EndLine != 9 {
		t.Fatalf("Target reference scope = %#v, want run at 7-9", found.Results)
	}

	inspected, err := view.Inspect(
		"Service.kt:8",
		Options{Include: IncludeScope, Return: ReturnScope},
	)
	if err != nil {
		t.Fatal(err)
	}
	if inspected.Symbol != "Target" || len(inspected.Results) != 1 ||
		inspected.Results[0].Scope != "run" {
		t.Fatalf("Inspect Target = %#v, want Target in run", inspected)
	}
}

func TestKotlinDefinitionSymbolRecognizesDeclarationsAndRejectsExpressions(t *testing.T) {
	t.Parallel()

	backend := newKotlinLanguage()
	tests := []struct {
		name string
		line string
		want string
		ok   bool
	}{
		{name: "data class", line: `public data class User<T>(val value: T) {`, want: "User", ok: true},
		{name: "fun interface", line: `fun interface Action<T> {`, want: "Action", ok: true},
		{name: "data object", line: `data object Empty : Shape {`, want: "Empty", ok: true},
		{name: "annotation class", line: `annotation class Marker(val tag: String)`, want: "Marker", ok: true},
		{name: "value class", line: `@JvmInline value class Id(val raw: String)`, want: "Id", ok: true},
		{name: "type alias", line: `typealias Handler<T> = suspend (T) -> Unit`, want: "Handler", ok: true},
		{name: "generic extension", line: `suspend fun <T> List<T>.render(value: T) {`, want: "render", ok: true},
		{name: "extension property", line: `val String.slug: String get() = lowercase()`, want: "slug", ok: true},
		{name: "named companion", line: `companion object Factory {`, want: "Factory", ok: true},
		{name: "secondary constructor", line: `constructor(value: Int) : this() {`, want: "constructor", ok: true},
		{name: "if", line: `if (ready()) { Target() }`},
		{name: "when", line: `when (value) { else -> Target() }`},
		{name: "for", line: `for (item in items) { Target(item) }`},
		{name: "context call", line: `context(logger) { Target() }`},
		{name: "direct call", line: `Target()`},
		{name: "qualified generic call", line: `service.Client.render<Result>()`},
		{name: "annotation use", line: `@Marker("value")`},
		{name: "anonymous function", line: `fun(value: Int) = value`},
		{name: "object literal", line: `object : Shape {}`},
		{name: "comment", line: `// fun Hidden() {}`},
		{name: "string", line: `val text = "fun Hidden() {}"`, want: "text", ok: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, ok := backend.definitionSymbol(test.line)
			if got != test.want || ok != test.ok {
				t.Fatalf("definitionSymbol(%q) = %q, %v; want %q, %v",
					test.line, got, ok, test.want, test.ok)
			}
		})
	}
}

func TestKotlinDefinitionsCoverModernNamedDeclarationsAndExcludeBindings(t *testing.T) {
	t.Parallel()

	const source = `/** Marker documentation. */
@Target(AnnotationTarget.CLASS)
annotation class Marker(val tag: String)

sealed interface Shape {
    val area: Double
    fun draw()
}

/** Circle documentation. */
@Marker("circle")
data class Circle<T : Number>(
    val radius: T,
    plain: String,
    vararg val labels: String,
) : Shape where T : Comparable<T> {
    companion object Factory {
        const val DEFAULT = 1
    }

    constructor(radius: T) : this(radius, "", "") {
        consume(radius)
    }

    override val area: Double
        get() = radius.toDouble()

    var title: String = ""
        private set

    val cache by lazy {
        compute()
    }

    suspend fun <R> R.render(value: T): String where R : Any =
        "$this:$value"

    fun outer(parameter: Int) {
        fun local() = Unit
        class LocalType
        val localValue = parameter
        val (left, right) = pair()
    }

    class Nested {
        typealias Names = Set<String>
    }
}

data object Empty : Shape

enum class State {
    Ready,
    Busy {
        override fun code() = 1
    };

    abstract fun code(): Int
}

fun interface Mapper<in T, out R> {
    operator fun invoke(value: T): R
}

@JvmInline
value class Id(val raw: String)

typealias Callback<T> = suspend (T) -> Unit
`
	lines := kotlinTestLines(source)
	definitions := newKotlinLanguage().sourceDefinitions(lines)
	want := []string{
		"Marker", "tag", "Shape", "area", "draw", "Circle", "radius", "labels",
		"Factory", "DEFAULT", "constructor", "area", "title", "cache", "render",
		"outer", "local", "LocalType", "Nested", "Names", "Empty", "State", "Ready",
		"Busy", "code", "code", "Mapper", "invoke", "Id", "raw", "Callback",
	}
	if got := kotlinTestDefinitionSymbols(definitions); !slices.Equal(got, want) {
		t.Fatalf("modern Kotlin definitions =\n%#v\nwant\n%#v", got, want)
	}
	for _, forbidden := range []string{
		"Target", "AnnotationTarget", "plain", "T", "R", "value", "parameter",
		"localValue", "left", "right", "get", "set", "lazy", "compute", "consume",
	} {
		if slices.Contains(kotlinTestDefinitionSymbols(definitions), forbidden) {
			t.Errorf("non-outline binding or call %q became a definition: %#v",
				forbidden, definitions)
		}
	}
	kotlinTestAssertDefinitionCoordinates(t, lines, definitions)

	for _, symbol := range []string{
		"Marker", "Shape", "draw", "Circle", "Factory", "constructor", "area", "cache",
		"render", "outer", "local", "LocalType", "Nested", "Empty", "State", "Busy",
		"code", "Mapper", "invoke", "Id",
	} {
		if !kotlinTestHasOwningDefinition(definitions, symbol) {
			t.Errorf("definition %q has no owning declaration: %#v", symbol, definitions)
		}
	}
	for _, symbol := range []string{"tag", "radius", "labels", "DEFAULT", "Ready", "raw"} {
		definition := kotlinTestFirstDefinition(t, definitions, symbol)
		if definition.ownsScope || definition.scopeStart != definition.line ||
			definition.scopeEnd != definition.line {
			t.Errorf("non-owning definition %q has scope %#v", symbol, definition)
		}
	}

	circle := kotlinTestFirstDefinition(t, definitions, "Circle")
	if circle.scopeStart != kotlinTestLineContaining(t, lines, "Circle documentation") ||
		circle.scopeEnd != kotlinTestLineContaining(t, lines, "data object Empty")-2 {
		t.Errorf("Circle scope = %#v, want documented full declaration", circle)
	}
}

func TestKotlinPackagesImportsAliasesAndFakeSyntax(t *testing.T) {
	t.Parallel()

	const source = `@file:Suppress("unused")
package demo.` + "`when`" + `.navigation

import kotlin.collections.*
import java.util.concurrent.ConcurrentHashMap as ConcurrentMap
import demo.Target

class Imports {
    val text = "import fake.Hidden"

    fun work() {
        importThing()
        objectValue.importMember()
    }
}
// import fake.Comment
/* import fake.Block */
`
	lines := kotlinTestLines(source)
	backend := newKotlinLanguage()
	if start, end, ok := backend.importRange(lines); !ok || start != 4 || end != 6 {
		t.Fatalf("Kotlin import range = %d-%d, %v; want 4-6, true", start, end, ok)
	}
	if got, want := kotlinTestDefinitionSymbols(backend.sourceDefinitions(lines)),
		[]string{"Imports", "text", "work"}; !slices.Equal(got, want) {
		t.Fatalf("import fixture definitions = %#v, want %#v", got, want)
	}

	const fakeOnly = `package demo

fun importThing() {
    importValue("fake")
    objectValue.importMember()
    val text = "import fake.String"
}
// import fake.Comment
/* import fake.Block */
`
	if start, end, ok := backend.importRange(kotlinTestLines(fakeOnly)); ok {
		t.Fatalf("fake-only imports = %d-%d, true; want none", start, end)
	}

	root := t.TempDir()
	writeFile(t, root, "Imports.kt", source)
	response, err := mustView(t, root).Inspect(
		"Imports.kt:12",
		Options{Include: IncludeImports, Return: ReturnScope},
	)
	if err != nil {
		t.Fatal(err)
	}
	foundImports := false
	for _, result := range response.Results {
		if result.Kind != "imports" {
			continue
		}
		foundImports = true
		if result.Language != "kt" || result.StartLine != 4 || result.EndLine != 6 ||
			result.Code != strings.Join(lines[3:6], "\n") {
			t.Errorf("Kotlin import result = %#v, want exact lines 4-6", result)
		}
	}
	if !foundImports {
		t.Fatalf("Inspect result has no imports entry: %#v", response.Results)
	}
}

func TestKotlinScriptsAreDiscoveredWithoutPromotingDSLCalls(t *testing.T) {
	t.Parallel()

	const source = `#!/usr/bin/env kotlin
@file:Suppress("unused")
import kotlin.test.Test

val versionName = "1.0"

class ScriptService

fun build() {
    println(versionName)
}

plugins { kotlin("jvm") version "2.4.10" }
repositories { mavenCentral() }
dependencies { implementation("demo:library:1.0") }
tasks.register<Test>("test") { useJUnitPlatform() }
`
	root := t.TempDir()
	writeFile(t, root, "build.gradle.kts", source)
	view := mustView(t, root)
	outline, err := view.Outline("build.gradle.kts", Options{Return: ReturnLocations})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := kotlinTestResultSymbols(outline.Results),
		[]string{"versionName", "ScriptService", "build"}; !slices.Equal(got, want) {
		t.Fatalf("Kotlin script definitions = %#v, want %#v", got, want)
	}
	for _, forbidden := range []string{
		"Test", "println", "plugins", "kotlin", "repositories", "mavenCentral",
		"dependencies", "implementation", "tasks", "register", "useJUnitPlatform",
	} {
		if slices.Contains(kotlinTestResultSymbols(outline.Results), forbidden) {
			t.Errorf("script DSL call %q became a definition: %#v", forbidden, outline.Results)
		}
	}

	found, err := view.Find("versionName", Options{
		Include: IncludeBoth,
		Return:  ReturnLocations,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found.Results) != 2 || found.Results[0].Kind != "def" ||
		found.Results[1].Kind != "ref" {
		t.Fatalf("script versionName results = %#v, want def and ref", found.Results)
	}
}

func kotlinTestLines(source string) []string {
	return strings.Split(strings.TrimSuffix(source, "\n"), "\n")
}

func kotlinTestDefinitionSymbols(definitions []sourceDefinition) []string {
	symbols := make([]string, len(definitions))
	for index, definition := range definitions {
		symbols[index] = definition.symbol
	}
	return symbols
}

func kotlinTestResultSymbols(results []Result) []string {
	symbols := make([]string, len(results))
	for index, result := range results {
		symbols[index] = result.Symbol
	}
	return symbols
}

func kotlinTestFirstDefinition(
	t *testing.T,
	definitions []sourceDefinition,
	symbol string,
) sourceDefinition {
	t.Helper()
	for _, definition := range definitions {
		if definition.symbol == symbol {
			return definition
		}
	}
	t.Fatalf("missing Kotlin definition %q in %#v", symbol, definitions)
	return sourceDefinition{}
}

func kotlinTestHasOwningDefinition(definitions []sourceDefinition, symbol string) bool {
	for _, definition := range definitions {
		if definition.symbol == symbol && definition.ownsScope {
			return true
		}
	}
	return false
}

func kotlinTestLineContaining(t *testing.T, lines []string, marker string) int {
	t.Helper()
	for index, line := range lines {
		if strings.Contains(line, marker) {
			return index + 1
		}
	}
	t.Fatalf("marker %q is absent from source", marker)
	return 0
}

func kotlinTestAssertDefinitionCoordinates(
	t *testing.T,
	lines []string,
	definitions []sourceDefinition,
) {
	t.Helper()
	for _, definition := range definitions {
		if definition.symbol == "" || definition.line < 1 || definition.line > len(lines) ||
			definition.column < 1 || definition.scopeStart < 1 ||
			definition.scopeStart > definition.line || definition.scopeEnd < definition.line ||
			definition.scopeEnd > len(lines) {
			t.Fatalf("invalid Kotlin definition coordinates: %#v (lines=%d)",
				definition, len(lines))
		}
		line := lines[definition.line-1]
		if definition.column > len(line) ||
			!strings.HasPrefix(line[definition.column-1:], definition.symbol) {
			t.Fatalf("Kotlin definition is not source-backed: %#v in %q", definition, line)
		}
	}
}

func kotlinTestImplements[T any](value any) bool {
	_, ok := value.(T)
	return ok
}
