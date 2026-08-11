package navigator

import (
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

type javaDefinitionSummary struct {
	symbol     string
	line       int
	column     int
	scopeStart int
	scopeEnd   int
	ownsScope  bool
}

func TestJavaBackendContractAndPublicIntegration(t *testing.T) {
	t.Parallel()

	backend := newJavaLanguage()
	if backend.name() != "java" {
		t.Fatalf("language name = %q, want java", backend.name())
	}
	if _, ok := any(backend).(sourceBackendPreparer); !ok {
		t.Fatal("Java backend does not prepare immutable source analyses")
	}
	if _, ok := any(backend).(linePreservingSourceCleaner); !ok {
		t.Fatal("Java backend does not provide line-preserving cleaning")
	}
	if _, ok := any(backend).(navigationScopeResolver); !ok {
		t.Fatal("Java backend does not provide named navigation scopes")
	}
	if _, ok := any(backend).(symbolOccurrenceCounter); !ok {
		t.Fatal("Java backend does not provide Java identifier boundaries")
	}
	if _, ok := any(backend).(sourceSymbolOccurrenceAugmenter); !ok {
		t.Fatal("Java backend does not augment split qualified-name occurrences")
	}
	if _, ok := any(backend).(sourceSymbolOccurrencePositionAugmenter); !ok {
		t.Fatal("Java backend does not position split qualified-name occurrences")
	}
	if registered := languageForExtension(".java"); registered.name() != "java" {
		t.Fatalf("registered .java backend = %q, want java", registered.name())
	} else if _, ok := registered.(javaLanguage); !ok {
		t.Fatalf("registered .java backend has type %T, want javaLanguage", registered)
	}

	const source = `package example;

public class Service {
    private int value;

    public void run() {
        target();
    }
}
`
	root := t.TempDir()
	writeFile(t, root, "Service.java", source)
	view := mustView(t, root)
	outline, err := view.Outline("Service.java", Options{Return: ReturnLocations})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := javaResultSymbols(outline.Results), []string{"Service", "value", "run"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("outline symbols = %#v, want %#v", got, want)
	}
	for _, result := range outline.Results {
		if result.Kind != "def" || result.Language != "java" || result.Path != "Service.java" {
			t.Fatalf("malformed Java outline result: %#v", result)
		}
	}

	response, err := view.Find("target", Options{
		Include: IncludeRefs,
		Return:  ReturnScope,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].Scope != "run" ||
		response.Results[0].StartLine != 6 || response.Results[0].EndLine != 8 {
		t.Fatalf("target scope = %#v, want run at 6-8", response.Results)
	}
}

func TestJavaMalformedTripleQuoteDoesNotConsumeFollowingDeclarations(t *testing.T) {
	t.Parallel()

	const source = `class C {
    String broken = """;
    void recovered() { target(); }
}`
	lines := javaTestLines(source)
	analysis := analyzeJavaSource(source, len(lines))
	if got := javaDefinitionSymbols(analysis.definitions); !slices.Contains(got, "recovered") {
		t.Fatalf("definitions = %#v, want recovered after malformed triple quote", got)
	}

	root := t.TempDir()
	writeFile(t, root, "C.java", source)
	view := mustView(t, root)
	response, err := view.Find("target", Options{
		Include:    IncludeRefs,
		Return:     ReturnLocations,
		NoComments: true,
		NoStrings:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].Line != 3 {
		t.Fatalf("target results = %#v, want one recovered reference on line 3",
			response.Results)
	}
	outline, err := view.Outline("C.java", Options{Return: ReturnLocations})
	if err != nil {
		t.Fatal(err)
	}
	if symbols := javaResultSymbols(outline.Results); !slices.Contains(symbols, "recovered") {
		t.Fatalf("outline symbols = %#v, want recovered", symbols)
	}
}

func TestJavaDefinitionsHaveExactConcreteMetadata(t *testing.T) {
	t.Parallel()

	const source = `/** Service documentation. */
@Deprecated
public class Service<T> {
    private int first = 1, second = 2;
    /** Constructor documentation. */
    public Service() {
        int local = 0;
    }
    @Deprecated
    public <R> R convert(
        R input
    ) {
        return input;
    }
}`
	javaAssertConcreteSyntax(t, source)
	lines := javaTestLines(source)
	definitions := newJavaLanguage().sourceDefinitions(lines)
	want := []javaDefinitionSummary{
		{
			symbol: "Service", line: 3, column: javaColumnContaining(t, lines, 3, "Service"),
			scopeStart: 1, scopeEnd: 15, ownsScope: true,
		},
		{
			symbol: "first", line: 4, column: javaColumnContaining(t, lines, 4, "first"),
			scopeStart: 4, scopeEnd: 4,
		},
		{
			symbol: "second", line: 4, column: javaColumnContaining(t, lines, 4, "second"),
			scopeStart: 4, scopeEnd: 4,
		},
		{
			symbol: "Service", line: 6, column: javaColumnContaining(t, lines, 6, "Service"),
			scopeStart: 5, scopeEnd: 8, ownsScope: true,
		},
		{
			symbol: "convert", line: 10, column: javaColumnContaining(t, lines, 10, "convert"),
			scopeStart: 9, scopeEnd: 14, ownsScope: true,
		},
	}
	if got := javaDefinitionSummaries(definitions); !reflect.DeepEqual(got, want) {
		t.Fatalf("definitions = %#v, want %#v", got, want)
	}
	for _, definition := range definitions {
		if definition.symbol == "local" || definition.symbol == "input" || definition.symbol == "T" ||
			definition.symbol == "R" {
			t.Fatalf("non-outline binding became a definition: %#v", definition)
		}
	}
}

func TestJavaDefinitionsExcludeNonDeclarations(t *testing.T) {
	t.Parallel()

	const source = `/** fake.call(); class JavadocType {} */
class Real {
    String field = "class StringType { void stringMethod() {} }";
    String text = """
        interface TextBlockType { void textMethod(); }
        """;

    void method(String parameter) {
        int local = 1;
        for (String item : items) {
            item.call();
        }
        try (Resource resource = open()) {
            object.invoke();
        } catch (Exception caught) {
            caught.printStackTrace();
        }
        if (object instanceof String pattern) {
            pattern.length();
        }
        Runnable task = lambdaParameter -> lambdaParameter.call();
        label: while (ready()) break label;
        new Constructed();
    }
}
// enum LineComment { FAKE }
/* record BlockComment(int fake) {} */`
	javaAssertConcreteSyntax(t, source)
	definitions := newJavaLanguage().sourceDefinitions(javaTestLines(source))
	if got, want := javaDefinitionSymbols(definitions), []string{"Real", "field", "text", "method"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("definitions = %#v, want %#v", got, want)
	}
}

func TestJavaImportsCoverOnlyLanguageImportForms(t *testing.T) {
	t.Parallel()

	const source = `package example;

import java.util.List;
import java.util.*;
import static java.util.Collections.emptyList;
import static java.util.Map.*;

class Imports {
    String ordinary = "import fake.StringType;";
    String block = """
        import fake.TextBlockType;
        """;
    void importThing() {}
    void call() { object.importMethod(); }
}
// import fake.LineComment;
/* import static fake.BlockComment.member; */`
	javaAssertConcreteSyntax(t, source)
	lines := javaTestLines(source)
	if start, end, ok := newJavaLanguage().importRange(lines); !ok || start != 3 || end != 6 {
		t.Fatalf("import range = %d-%d, %v; want 3-6, true", start, end, ok)
	}

	const fakeOnly = `package example;
import module java.base;
class Imports {
    String value = "import java.util.List;";
    void call() { requires(java.sql); }
}
// import fake.Comment;
`
	if start, end, ok := newJavaLanguage().importRange(javaTestLines(fakeOnly)); !ok || start != 2 || end != 2 {
		t.Fatalf("module import range = %d-%d, %v; want 2-2, true", start, end, ok)
	}
}

func TestJavaModuleDefinitionAndRequiresImports(t *testing.T) {
	t.Parallel()

	const source = `/** Module documentation. */
open module com.example.application {
    requires java.base;
    requires transitive java.logging;
    requires static java.sql;
    exports com.example.api;
    opens com.example.internal to framework.core;
    uses com.example.spi.Plugin;
    provides com.example.spi.Plugin with com.example.impl.PluginImpl;
}`
	javaAssertConcreteSyntax(t, source)
	lines := javaTestLines(source)
	backend := prepareLanguageBackend(newJavaLanguage(), lines)
	want := []javaDefinitionSummary{{
		symbol:     "com.example.application",
		line:       2,
		column:     javaColumnContaining(t, lines, 2, "com.example.application"),
		scopeStart: 1,
		scopeEnd:   10,
		ownsScope:  true,
	}}
	if got := javaDefinitionSummaries(backend.sourceDefinitions(lines)); !reflect.DeepEqual(got, want) {
		t.Fatalf("module definitions = %#v, want %#v", got, want)
	}
	if start, end, ok := backend.importRange(lines); !ok || start != 3 || end != 5 {
		t.Fatalf("module import range = %d-%d, %v; want requires at 3-5", start, end, ok)
	}
	if start, end := backend.enclosingScope(lines, 8); start != 1 || end != 10 {
		t.Fatalf("module enclosing scope = %d-%d, want attached declaration 1-10", start, end)
	}
	resolver := backend.(navigationScopeResolver)
	if start, end := resolver.navigationScope(lines, 8); start != 1 || end != 10 {
		t.Fatalf("module navigation scope = %d-%d, want 1-10", start, end)
	}
}

func TestJavaModernDeclarations(t *testing.T) {
	t.Parallel()

	const source = `sealed interface Shape permits Circle {
    double area();
    default String kind() { return "shape"; }
}

record Circle(double radius, int $count) implements Shape {
    Circle {
        if (radius < 0) throw new IllegalArgumentException();
    }
    public double area() { return Math.PI * radius * radius; }
}

enum State {
    READY(0),
    FAILED(1) {
        @Override int code() { return 1; }
    };

    private final int code;
    State(int code) { this.code = code; }
    int code() { return code; }
}

@interface Marker {
    String value() default "x";
    Class<?> type();
}`
	javaAssertConcreteSyntax(t, source)
	definitions := newJavaLanguage().sourceDefinitions(javaTestLines(source))
	want := []string{
		"Shape", "area", "kind",
		"Circle", "radius", "$count", "Circle", "area",
		"State", "READY", "FAILED", "code", "code", "State", "code",
		"Marker", "value", "type",
	}
	if got := javaDefinitionSymbols(definitions); !reflect.DeepEqual(got, want) {
		t.Fatalf("modern definitions = %#v, want %#v", got, want)
	}
	for _, index := range []int{1, 16, 17} {
		definition := definitions[index]
		if !definition.ownsScope || definition.scopeStart != definition.line ||
			definition.scopeEnd != definition.line {
			t.Fatalf("signature definition = %#v, want owning declaration line", definition)
		}
	}
	for _, symbol := range []string{"Shape", "area", "kind", "Circle", "State", "Marker", "value", "type"} {
		if !javaHasOwningDefinition(definitions, symbol) {
			t.Fatalf("modern definition %q has no owning declaration: %#v", symbol, definitions)
		}
	}
	for _, symbol := range []string{"radius", "$count", "READY"} {
		definition := javaFirstDefinition(t, definitions, symbol)
		if definition.ownsScope || definition.scopeStart != definition.line ||
			definition.scopeEnd != definition.line {
			t.Fatalf("definition %q = %#v, want non-owning physical line", symbol, definition)
		}
	}
	failed := javaFirstDefinition(t, definitions, "FAILED")
	if !failed.ownsScope || failed.scopeStart != 15 || failed.scopeEnd != 17 {
		t.Fatalf("FAILED definition = %#v, want owning enum body 15-17", failed)
	}
}

func TestJavaModernControlSyntaxDoesNotInventBindings(t *testing.T) {
	t.Parallel()

	const source = `record Pair(String left, String right) {}

class Patterns {
    String render(Object value) {
        if (value instanceof Pair(String left, var right)) {
            return left + right;
        }
        return switch (value) {
            case Pair(String first, String second) when !first.isEmpty() -> first + second;
            case String text -> text;
            case null, default -> "";
        };
    }
}`
	javaAssertConcreteSyntax(t, source)
	if got, want := javaDefinitionSymbols(
		newJavaLanguage().sourceDefinitions(javaTestLines(source)),
	), []string{"Pair", "left", "right", "Patterns", "render"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("pattern definitions = %#v, want %#v", got, want)
	}
}

func TestJavaRecordVarargsComponentIsDefinition(t *testing.T) {
	t.Parallel()

	const source = `record Arguments(@Deprecated String... values) {}`
	javaAssertConcreteSyntax(t, source)
	if got, want := javaDefinitionSymbols(
		newJavaLanguage().sourceDefinitions(javaTestLines(source)),
	), []string{"Arguments", "values"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("record varargs definitions = %#v, want %#v", got, want)
	}
}

func TestJavaCompactSourceMainIsADeclaration(t *testing.T) {
	t.Parallel()

	const source = `void main() {
    System.out.println("hello");
}`
	javaAssertConcreteSyntax(t, source)
	definitions := newJavaLanguage().sourceDefinitions(javaTestLines(source))
	if got, want := javaDefinitionSymbols(definitions), []string{"main"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("compact-source definitions = %#v, want %#v", got, want)
	}
	if definition := definitions[0]; !definition.ownsScope || definition.scopeStart != 1 ||
		definition.scopeEnd != 3 {
		t.Fatalf("compact-source main = %#v, want owning 1-3", definition)
	}
}

func TestJavaScopesPreferSmallestBlockAndNamedOwner(t *testing.T) {
	t.Parallel()

	const source = `/** Outer documentation. */
class Outer {
    /** Work documentation. */
    void work() {
        Runnable task = () -> {
            if (ready()) {
                target();
            }
            afterIf();
        };
    }

    static {
        synchronized (lock) {
            staticTarget();
        }
    }
}`
	javaAssertConcreteSyntax(t, source)
	lines := javaTestLines(source)
	backend := prepareLanguageBackend(newJavaLanguage(), lines)
	if start, end := backend.enclosingScope(lines, 7); start != 6 || end != 8 {
		t.Fatalf("if scope = %d-%d, want 6-8", start, end)
	}
	if start, end := backend.enclosingScope(lines, 9); start != 5 || end != 10 {
		t.Fatalf("lambda scope = %d-%d, want 5-10", start, end)
	}
	resolver := backend.(navigationScopeResolver)
	if start, end := resolver.navigationScope(lines, 7); start != 3 || end != 11 {
		t.Fatalf("method navigation scope = %d-%d, want 3-11", start, end)
	}
	if start, end := backend.enclosingScope(lines, 15); start != 14 || end != 16 {
		t.Fatalf("synchronized scope = %d-%d, want 14-16", start, end)
	}
	if start, end := resolver.navigationScope(lines, 15); start != 1 || end != 18 {
		t.Fatalf("initializer navigation scope = %d-%d, want class 1-18", start, end)
	}
}

func TestJavaScopesSeparateIfBranchesAndTryContinuations(t *testing.T) {
	t.Parallel()

	const source = `class Branches {
    void run() {
        if (ready())
        {
            thenTarget();
        }
        else
        {
            elseTarget();
        }
        try
        {
            tryTarget();
        }
        catch (Exception problem)
        {
            catchTarget();
        }
        finally
        {
            finallyTarget();
        }
    }
}`
	javaAssertConcreteSyntax(t, source)
	lines := javaTestLines(source)
	backend := newJavaLanguage()
	for _, test := range []struct {
		line      int
		wantStart int
		wantEnd   int
	}{
		{line: 5, wantStart: 3, wantEnd: 6},
		{line: 9, wantStart: 7, wantEnd: 10},
		{line: 13, wantStart: 11, wantEnd: 14},
		{line: 17, wantStart: 15, wantEnd: 18},
		{line: 21, wantStart: 19, wantEnd: 22},
	} {
		if start, end := backend.enclosingScope(lines, test.line); start != test.wantStart ||
			end != test.wantEnd {
			t.Fatalf(
				"scope on line %d = %d-%d, want %d-%d",
				test.line, start, end, test.wantStart, test.wantEnd,
			)
		}
	}
}

func TestJavaSearchMaskingAndCommentCleaningUnderstandTextBlocksAndTemplates(t *testing.T) {
	t.Parallel()

	const source = `class Search {
    String ordinary = "target // literal";
    String text = """
        target /* text-block literal */
        second target // still literal
        """;
    char character = 't';
    String template = STR."raw target \{call(target)} tail";
    // line target
    /* block target */ void run() { target(); } // trailing target
}`
	lines := javaTestLines(source)
	backend := prepareLanguageBackend(newJavaLanguage(), lines)
	searchable := backend.searchLines(lines, true, true)
	if len(searchable) != len(lines) || len(strings.Join(searchable, "\n")) != len(source) {
		t.Fatalf("search mask changed coordinates: %#v", searchable)
	}
	if got, want := javaLinesContainingSymbol(backend, searchable, "target"), []int{8, 10}; !reflect.DeepEqual(got, want) {
		t.Fatalf("searchable target lines = %#v, want %#v; mask=%#v", got, want, searchable)
	}
	if got := backend.(symbolOccurrenceCounter).countSymbolOccurrences(searchable[7], "target"); got != 1 {
		t.Fatalf("template expression target count = %d, want 1; mask=%q", got, searchable[7])
	}

	commentsOnly := backend.searchLines(lines, true, false)
	if !strings.Contains(commentsOnly[1], "target // literal") ||
		!strings.Contains(commentsOnly[3], "target /* text-block literal */") ||
		strings.Contains(commentsOnly[8], "target") ||
		strings.Contains(commentsOnly[9], "trailing target") {
		t.Fatalf("comment mask confused comments and literals: %#v", commentsOnly)
	}

	cleaned := backend.cleanSource(source, true, false)
	for _, literal := range []string{"target // literal", "target /* text-block literal */", "second target // still literal"} {
		if !strings.Contains(cleaned, literal) {
			t.Fatalf("comment cleaning lost literal %q:\n%s", literal, cleaned)
		}
	}
	for _, comment := range []string{"line target", "block target", "trailing target"} {
		if strings.Contains(cleaned, comment) {
			t.Fatalf("comment cleaning retained %q:\n%s", comment, cleaned)
		}
	}
	if !strings.Contains(cleaned, "target();") {
		t.Fatalf("comment cleaning lost executable code:\n%s", cleaned)
	}

	cleaner := backend.(linePreservingSourceCleaner)
	cleanedLines := cleaner.cleanSourceLines(lines, true, false)
	if len(cleanedLines) != len(lines) || !strings.Contains(cleanedLines[1], "target // literal") ||
		strings.Contains(cleanedLines[8], "line target") ||
		strings.Contains(cleanedLines[9], "trailing target") {
		t.Fatalf("line-preserving cleaning = %#v", cleanedLines)
	}
	ignored := backend.ignoredSearchLines(lines, true, false)
	if !ignored[9] || ignored[10] {
		t.Fatalf("ignored comment lines = %#v, want only full comment line 9", ignored)
	}

	line := `String url = "https://example.test/a//b"; target(); // remove target`
	stripped := backend.stripComment(line)
	if !strings.Contains(stripped, `"https://example.test/a//b"`) ||
		!strings.Contains(stripped, "target();") || strings.Contains(stripped, "remove target") {
		t.Fatalf("stripComment(%q) = %q", line, stripped)
	}

	for _, test := range []struct {
		name   string
		source string
		want   string
	}{
		{
			name:   "inline block",
			source: `before(); /* hidden */ after();`,
			want:   `before(); ` + strings.Repeat(" ", len(`/* hidden */`)) + ` after();`,
		},
		{
			name:   "translated line delimiter",
			source: `before(); \u002f\u002f hidden`,
			want:   `before();`,
		},
		{
			name:   "translated block delimiters",
			source: `before(); \u002f\u002a hidden \u002a\u002f after();`,
			want: `before(); ` + strings.Repeat(
				" ", len(`\u002f\u002a hidden \u002a\u002f`),
			) + ` after();`,
		},
		{
			name:   "translated newline ends raw line comment",
			source: `before(); // hidden \u000a after();`,
			want: `before(); ` + strings.Repeat(" ", len(`// hidden `)) +
				`\u000a after();`,
		},
		{
			name:   "translated newline ends translated line comment",
			source: `before(); \u002f\u002f hidden \u000a after();`,
			want: `before(); ` + strings.Repeat(
				" ", len(`\u002f\u002f hidden `),
			) + `\u000a after();`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := newJavaLanguage().stripComment(test.source); got != test.want {
				t.Fatalf("stripComment(%q) = %q, want %q", test.source, got, test.want)
			}
		})
	}
}

func TestJavaIdentifiersAndOccurrenceBoundaries(t *testing.T) {
	t.Parallel()

	const source = `class Café$Service {
    int $value;
    void naïve$run() {}
}
class \u0044emo {
    void \u0066oo() {}
}`
	definitions := newJavaLanguage().sourceDefinitions(javaTestLines(source))
	want := []string{"Café$Service", "$value", "naïve$run", `\u0044emo`, `\u0066oo`}
	if got := javaDefinitionSymbols(definitions); !reflect.DeepEqual(got, want) {
		t.Fatalf("Unicode definitions = %#v, want %#v", got, want)
	}

	counter := newJavaLanguage()
	line := "foo $foo foo$bar αfoo foo\u0301 obj.foo foo()"
	if got := counter.countSymbolOccurrences(line, "foo"); got != 3 {
		t.Fatalf("foo occurrences = %d, want 3", got)
	}
	if got := counter.countSymbolOccurrences(`\u0066oo(); \u0066ooBar();`, `\u0066oo`); got != 1 {
		t.Fatalf("escaped foo occurrences = %d, want 1", got)
	}

	root := t.TempDir()
	writeFile(t, root, "Names.java", source+"\nclass Use { Café$Service value; }\n")
	view := mustView(t, root)
	response, err := view.Find("Café$Service", Options{
		Include: IncludeBoth,
		Return:  ReturnLocations,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := resultLines(response.Results), []int{1, 8}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Unicode/$ Find lines = %#v, want %#v; results=%#v", got, want, response.Results)
	}
	partial, err := view.Find("Service", Options{Include: IncludeBoth, Return: ReturnLocations})
	if err != nil {
		t.Fatal(err)
	}
	if len(partial.Results) != 0 {
		t.Fatalf("partial $ identifier matched: %#v", partial.Results)
	}
}

func TestJavaSymbolOnLinePrefersMemberNames(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		source string
		want   string
	}{
		{name: "instance field", source: `obj.target;`, want: "target"},
		{name: "static field", source: `Type.TARGET;`, want: "TARGET"},
		{name: "method reference", source: `Type::target;`, want: "target"},
		{name: "member call", source: `obj.target();`, want: "target"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got, ok := newJavaLanguage().symbolOnLine([]string{test.source}, 1); !ok || got != test.want {
				t.Fatalf("symbolOnLine(%q) = %q, %v; want %q, true", test.source, got, ok, test.want)
			}
		})
	}
}

func TestJavaMalformedSourcesRecoverDefinitionsAndStayOpaque(t *testing.T) {
	t.Parallel()

	const malformed = `class Before {}
class Broken<T {
    void partial(
class After {
    void recovered() {}
}`
	definitions := newJavaLanguage().sourceDefinitions(javaTestLines(malformed))
	got := javaDefinitionSymbols(definitions)
	for _, symbol := range []string{"Before", "After", "recovered"} {
		if !slices.Contains(got, symbol) {
			t.Fatalf("malformed recovery lost %q: %#v", symbol, got)
		}
	}

	const unterminatedComment = `class Visible {}
/* unterminated comment
class Hidden { void hidden() {} }`
	if got, want := javaDefinitionSymbols(
		newJavaLanguage().sourceDefinitions(javaTestLines(unterminatedComment)),
	), []string{"Visible"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unterminated-comment definitions = %#v, want %#v", got, want)
	}

	const unterminatedTextBlock = `class Visible {}
class Holder { String value = """
class Hidden { void hidden() {} }`
	if got := javaDefinitionSymbols(
		newJavaLanguage().sourceDefinitions(javaTestLines(unterminatedTextBlock)),
	); slices.Contains(got, "Hidden") || slices.Contains(got, "hidden") {
		t.Fatalf("unterminated text block leaked definitions: %#v", got)
	}

	escaped := `cl\u0061ss \u0044emo { void run() {} }`
	if got, want := javaDefinitionSymbols(
		newJavaLanguage().sourceDefinitions(javaTestLines(escaped)),
	), []string{`\u0044emo`, "run"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("escaped-keyword recovery = %#v, want %#v", got, want)
	}

	invalidUTF8 := "class Before {}\nString value = \"" + string([]byte{0xff, 0xfe}) +
		"\";\nclass After {}\n// " + string([]byte{0xc0})
	if got, want := javaDefinitionSymbols(
		newJavaLanguage().sourceDefinitions(javaTestLines(invalidUTF8)),
	), []string{"Before", "After"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("invalid-UTF-8 definitions = %#v, want %#v", got, want)
	}

	// Java 25 permits statements before an explicit constructor invocation;
	// the pinned grammar may reject it, so bounded lexical recovery must retain
	// the constructor declaration without promoting its local variable.
	const flexibleConstructor = `class Flexible {
    Flexible() {
        int local = initialize();
        super();
    }
}`
	if got, want := javaDefinitionSymbols(
		newJavaLanguage().sourceDefinitions(javaTestLines(flexibleConstructor)),
	), []string{"Flexible", "Flexible"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("flexible-constructor recovery = %#v, want %#v", got, want)
	}
}

func TestJavaMalformedOperationsNeverPanic(t *testing.T) {
	t.Parallel()

	invalidUTF8 := "class " + string([]byte{0xff}) + " { void run( }\n"
	corpus := []string{
		"",
		"class Open<T {\n",
		"interface Open { void method(\n",
		"record Open(String value {\n",
		"enum Open { FIRST, SECOND { void run( }\n",
		"module open { requires transitive ;\n",
		"import java.util.{\nclass Later {}\n",
		"/* unterminated\nclass Hidden {}\n",
		"class Text { String value = \"\"\"unterminated\nclass Hidden {}\n",
		invalidUTF8,
	}
	for index, source := range corpus {
		t.Run("case_"+strconv.Itoa(index), func(t *testing.T) {
			t.Parallel()
			lines := strings.Split(source, "\n")
			backend := prepareLanguageBackend(newJavaLanguage(), lines)
			_ = backend.sourceDefinitions(lines)
			_, _, _ = backend.importRange(lines)
			for _, options := range [][2]bool{{false, false}, {true, false}, {false, true}, {true, true}} {
				searchable := backend.searchLines(lines, options[0], options[1])
				if len(searchable) != len(lines) || len(strings.Join(searchable, "\n")) != len(source) {
					t.Fatalf("search mask changed coordinates: %#v", searchable)
				}
			}
			_ = backend.ignoredSearchLines(lines, true, false)
			_ = backend.cleanSource(source, true, false)
			_, _ = backend.enclosingScope(lines, 1)
			_, _ = backend.enclosingScope(lines, len(lines))
			for _, line := range lines {
				_, _ = backend.definitionSymbol(line)
				_ = backend.stripComment(line)
			}
		})
	}
}

func TestJavaPreparedBackendRejectsStaleAndMutatedAnalysis(t *testing.T) {
	t.Parallel()

	first := []string{"class First { int value; }"}
	prepared, ok := prepareLanguageBackend(newJavaLanguage(), first).(javaLanguage)
	if !ok {
		t.Fatalf("prepared backend has type %T, want javaLanguage", prepared)
	}
	if got, want := javaDefinitionSymbols(prepared.sourceDefinitions(first)), []string{"First", "value"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("prepared definitions = %#v, want %#v", got, want)
	}

	second := []string{"class Second { void run() {} }"}
	if got, want := javaDefinitionSymbols(prepared.sourceDefinitions(second)), []string{"Second", "run"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("stale-slice definitions = %#v, want %#v", got, want)
	}

	first[0] = "class Mutated { long changed; }"
	if got, want := javaDefinitionSymbols(prepared.sourceDefinitions(first)), []string{"Mutated", "changed"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("mutated-source definitions = %#v, want %#v", got, want)
	}

	empty, ok := prepared.prepareSource(nil).(javaLanguage)
	if !ok || len(empty.sourceDefinitions(nil)) != 0 {
		t.Fatalf("empty prepared backend retained definitions: %#v", empty.sourceDefinitions(nil))
	}
}

func TestJavaSourceDefinitionsReturnsDefensiveCopy(t *testing.T) {
	t.Parallel()

	lines := []string{"class Original { int field; void method() {} }"}
	prepared := prepareLanguageBackend(newJavaLanguage(), lines)
	definitions := prepared.sourceDefinitions(lines)
	if len(definitions) != 3 {
		t.Fatalf("definitions = %#v, want class, field, and method", definitions)
	}
	definitions[0].symbol = "Corrupted"
	definitions[1].scopeStart = 99
	definitions = append(definitions, sourceDefinition{symbol: "Injected"})
	if len(definitions) != 4 {
		t.Fatalf("mutated caller-owned definitions = %#v, want appended entry", definitions)
	}

	if got, want := javaDefinitionSymbols(prepared.sourceDefinitions(lines)),
		[]string{"Original", "field", "method"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("definitions after caller mutation = %#v, want %#v", got, want)
	}
	refetched := prepared.sourceDefinitions(lines)
	if refetched[1].scopeStart == 99 {
		t.Fatalf("cached definition coordinates were mutable through returned slice: %#v", refetched[1])
	}
}

func TestJavaPreparedBackendSupportsConcurrentDistinctInputs(t *testing.T) {
	prepared := prepareLanguageBackend(
		newJavaLanguage(),
		[]string{"class Original { int value; }"},
	)
	results := make(chan error, 24)
	for index := range cap(results) {
		go func() {
			name := fmt.Sprintf("Type%d", index)
			field := fmt.Sprintf("field%d", index)
			method := fmt.Sprintf("method%d", index)
			lines := []string{fmt.Sprintf(
				`class %s { int %s; void %s() { target(); } }`,
				name, field, method,
			)}
			if got, want := javaDefinitionSymbols(prepared.sourceDefinitions(lines)),
				[]string{name, field, method}; !reflect.DeepEqual(got, want) {
				results <- fmt.Errorf("definitions for %s = %#v, want %#v", name, got, want)
				return
			}
			if searchable := prepared.searchLines(lines, true, true); !slices.Equal(searchable, lines) {
				results <- fmt.Errorf("search lines for %s = %#v", name, searchable)
				return
			}
			if _, _, ok := prepared.importRange(lines); ok {
				results <- fmt.Errorf("ordinary source %s unexpectedly has imports", name)
				return
			}
			results <- nil
		}()
	}
	for range cap(results) {
		if err := <-results; err != nil {
			t.Fatal(err)
		}
	}
}

func TestJavaOversizedAndTokenHeavyFallbackStaysBounded(t *testing.T) {
	const prefix = `import java.util.List;
class Oversized {
    int field;
    void run() { int local = 0; }
}
`
	oversized := prefix + strings.Repeat(" ", javaMaximumConcreteParseBytes-len(prefix)+1)
	if tree, ok := parseJavaSyntax(oversized); ok || tree != nil {
		t.Fatal("oversized Java source unexpectedly entered the concrete parser")
	}
	lines := strings.Split(oversized, "\n")
	backend := newJavaLanguage()
	if got, want := javaDefinitionSymbols(backend.sourceDefinitions(lines)),
		[]string{"Oversized", "field", "run"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("oversized definitions = %#v, want %#v", got, want)
	}
	if start, end, ok := backend.importRange(lines); !ok || start != 1 || end != 1 {
		t.Fatalf("oversized import range = %d-%d, %v; want 1-1, true", start, end, ok)
	}

	tokenHeavy := strings.Repeat(";", javaMaximumConcreteTokens+64) +
		"\nclass AfterTokens { int field; }"
	if count := javaLexicalTokenCount(tokenHeavy, javaMaximumConcreteTokens+1); count <= javaMaximumConcreteTokens {
		t.Fatalf("token-heavy fixture has %d tokens, want over %d", count, javaMaximumConcreteTokens)
	}
	if tree, ok := parseJavaSyntax(tokenHeavy); ok || tree != nil {
		t.Fatal("token-heavy Java source unexpectedly entered the concrete parser")
	}
	if got, want := javaDefinitionSymbols(
		backend.sourceDefinitions(strings.Split(tokenHeavy, "\n")),
	), []string{"AfterTokens", "field"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("token-heavy definitions = %#v, want %#v", got, want)
	}

	const storageSide = javaMaximumStoredLexicalTokens/2 + 256
	storageHeavy := strings.Repeat(";", storageSide) +
		"\nclass AfterStorageCap { int field; void run() {} }\n" +
		strings.Repeat(";", storageSide)
	storageAnalysis := analyzeJavaSource(
		storageHeavy, strings.Count(storageHeavy, "\n")+1,
	)
	if !storageAnalysis.lexed.truncated {
		t.Fatal("storage-heavy fixture did not exercise bounded token retention")
	}
	if got, want := javaDefinitionSymbols(storageAnalysis.definitions),
		[]string{"AfterStorageCap", "field", "run"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("storage-heavy definitions = %#v, want %#v", got, want)
	}
	gapStart, gapEnd, ok := javaStoredTokenGapRange(storageAnalysis.lexed.tokens)
	nameOffset := strings.Index(storageHeavy, "AfterStorageCap")
	if !ok || nameOffset < gapStart || nameOffset >= gapEnd {
		t.Fatalf("definition offset %d is outside retained-token gap %d-%d, %v",
			nameOffset, gapStart, gapEnd, ok)
	}
}

func TestJavaStreamedGapDefinitionsPreserveDeclarationContexts(t *testing.T) {
	const side = javaMaximumStoredLexicalTokens/2 + 256
	wrap := func(middle string) string {
		return strings.Repeat(";", side) + "\n" + middle + "\n" +
			strings.Repeat(";", side)
	}

	t.Run("anonymous-and-enum-bodies", func(t *testing.T) {
		source := wrap(`class Outer {
 Object holder = new Object() {
  int anonymousField;
  void anonymousRun() {}
 };
 enum E { ITEM {
  int enumField;
  void enumRun() {}
 }; }
}`)
		analysis := analyzeJavaSource(source, strings.Count(source, "\n")+1)
		if got, want := javaDefinitionSymbols(analysis.definitions), []string{
			"Outer", "holder", "anonymousField", "anonymousRun",
			"E", "ITEM", "enumField", "enumRun",
		}; !reflect.DeepEqual(got, want) {
			t.Fatalf("streamed nested definitions = %#v, want %#v", got, want)
		}
		for _, symbol := range []string{"Outer", "holder", "anonymousRun", "E", "ITEM", "enumRun"} {
			if !javaHasOwningDefinition(analysis.definitions, symbol) {
				t.Errorf("streamed definition %q lost scope ownership", symbol)
			}
		}
	})

	t.Run("module", func(t *testing.T) {
		source := wrap("open module com.example { requires java.base; }")
		analysis := analyzeJavaSource(source, strings.Count(source, "\n")+1)
		if got, want := javaDefinitionSymbols(analysis.definitions),
			[]string{"com.example"}; !reflect.DeepEqual(got, want) {
			t.Fatalf("streamed module definitions = %#v, want %#v", got, want)
		}
	})
}

func TestJavaStreamedGapDefinitionsCrossRetentionBoundaries(t *testing.T) {
	const firstStored = javaMaximumStoredLexicalTokens / 2
	for _, test := range []struct {
		name string
		text string
	}{
		{
			name: "name-is-final-prefix-token",
			text: strings.Repeat("; ", firstStored-2) +
				"class PrefixBoundary { int prefixField; } " +
				strings.Repeat("; ", firstStored+256),
		},
		{
			name: "name-is-first-tail-token",
			text: strings.Repeat("; ", firstStored) +
				"class TailBoundary { int tailField; } " +
				strings.Repeat("; ", firstStored-6),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			analysis := analyzeJavaSource(test.text, 1)
			if !analysis.lexed.truncated {
				t.Fatal("fixture did not truncate tokens")
			}
			symbols := javaDefinitionSymbols(analysis.definitions)
			for _, suffix := range []string{"Boundary", "Field"} {
				found := false
				for _, symbol := range symbols {
					found = found || strings.HasSuffix(symbol, suffix)
				}
				if !found {
					t.Fatalf("definitions = %#v, missing *%q at gap boundary",
						symbols, suffix)
				}
			}
		})
	}
}

func TestJavaStreamedGapDefinitionsDenseSegmentsStayBounded(t *testing.T) {
	const paddingTokens = javaMaximumStoredLexicalTokens/2 + 256
	fixture := func(middle string) (string, javaLexResult) {
		source := "class Huge {\n" + strings.Repeat("; ", paddingTokens) +
			"\n" + middle + "\n" + strings.Repeat("; ", paddingTokens) + "\n}\n"
		return source, lexJava(source)
	}

	t.Run("fields", func(t *testing.T) {
		const fields = 512
		source, lexed := fixture(strings.Repeat("Foo field; ", fields))
		allocations := testing.AllocsPerRun(1, func() {
			definitions := javaStreamedGapDefinitions(
				source, strings.Count(source, "\n")+1, lexed,
			)
			if len(definitions) != fields {
				panic("dense field definitions lost")
			}
		})
		if allocations > fields {
			t.Fatalf("dense streamed fields allocated %.0f objects, want at most %d",
				allocations, fields)
		}
	})

	t.Run("non-declarations", func(t *testing.T) {
		source, lexed := fixture(strings.Repeat("foo; ", 4<<10))
		allocations := testing.AllocsPerRun(1, func() {
			definitions := javaStreamedGapDefinitions(
				source, strings.Count(source, "\n")+1, lexed,
			)
			if len(definitions) != 0 {
				panic("identifier statement became definition")
			}
		})
		if allocations > 64 {
			t.Fatalf("dense non-declarations allocated %.0f objects", allocations)
		}
	})
}

func FuzzJavaLanguageMaintainsCoordinateContracts(f *testing.F) {
	for _, source := range []string{
		"",
		"class Ready { int field; void run() {} }\n",
		"record Point(int x, int y) { Point {} }\n",
		"module demo.app { requires transitive java.logging; }\n",
		"class Broken<T {\nvoid later() {}\n",
		"class Text { String value = \"\"\"unterminated\nclass Hidden {}\n",
		`cl\u0061ss \u0044emo { void \u0066oo() {} }`,
		string([]byte{'c', 'l', 'a', 's', 's', ' ', 0xff, ' ', '{', '}', '\n'}),
	} {
		f.Add(source)
	}

	f.Fuzz(func(t *testing.T, source string) {
		if len(source) > 256*1024 {
			t.Skip()
		}
		lines := strings.Split(source, "\n")
		backend := prepareLanguageBackend(newJavaLanguage(), lines)
		for _, definition := range backend.sourceDefinitions(lines) {
			if definition.symbol == "" || definition.line < 1 || definition.line > len(lines) ||
				definition.column < 1 || definition.scopeStart < 1 ||
				definition.scopeStart > definition.line || definition.scopeEnd < definition.line ||
				definition.scopeEnd > len(lines) {
				t.Fatalf("invalid definition coordinates: %#v", definition)
			}
		}
		if start, end, ok := backend.importRange(lines); ok &&
			(start < 1 || end < start || end > len(lines)) {
			t.Fatalf("invalid import range: %d-%d", start, end)
		}
		for _, options := range [][2]bool{{false, false}, {true, false}, {false, true}, {true, true}} {
			searchable := backend.searchLines(lines, options[0], options[1])
			if len(searchable) != len(lines) || len(strings.Join(searchable, "\n")) != len(source) {
				t.Fatalf("search mask changed coordinates: %#v", searchable)
			}
		}
		if cleaner, ok := backend.(linePreservingSourceCleaner); ok {
			cleaned := cleaner.cleanSourceLines(lines, true, false)
			if len(cleaned) != len(lines) {
				t.Fatalf("clean lines = %d, want %d", len(cleaned), len(lines))
			}
		}
		for _, lineNo := range []int{1, len(lines)} {
			start, end := backend.enclosingScope(lines, lineNo)
			if start < 1 || end < start || end > len(lines) {
				t.Fatalf("invalid scope for line %d: %d-%d", lineNo, start, end)
			}
			_ = bestSymbolOnLine(lines, lineNo, backend)
		}
		_ = backend.cleanSource(source, true, false)
		for _, line := range lines {
			_, _ = backend.definitionSymbol(line)
			_ = backend.stripComment(line)
		}
	})
}

func javaAssertConcreteSyntax(t *testing.T, source string) {
	t.Helper()
	tree, ok := parseJavaSyntax(source)
	if !ok || tree == nil {
		t.Fatal("parseJavaSyntax rejected valid Java source")
	}
	for _, node := range tree.nodes {
		if node.kind != "ERROR" {
			continue
		}
		start, end := node.startByte, node.endByte
		if start < 0 || start > len(source) {
			start = 0
		}
		if end < start || end > len(source) {
			end = start
		}
		t.Fatalf("valid Java source contains ERROR node at %d:%d: %q", start, end, source[start:end])
	}
}

func javaTestLines(source string) []string {
	return strings.Split(strings.TrimSuffix(source, "\n"), "\n")
}

func javaDefinitionSummaries(definitions []sourceDefinition) []javaDefinitionSummary {
	result := make([]javaDefinitionSummary, 0, len(definitions))
	for _, definition := range definitions {
		result = append(result, javaDefinitionSummary{
			symbol: definition.symbol, line: definition.line, column: definition.column,
			scopeStart: definition.scopeStart, scopeEnd: definition.scopeEnd,
			ownsScope: definition.ownsScope,
		})
	}
	return result
}

func javaDefinitionSymbols(definitions []sourceDefinition) []string {
	symbols := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		symbols = append(symbols, definition.symbol)
	}
	return symbols
}

func javaResultSymbols(results []Result) []string {
	symbols := make([]string, 0, len(results))
	for _, result := range results {
		symbols = append(symbols, result.Symbol)
	}
	return symbols
}

func javaColumnContaining(t *testing.T, lines []string, lineNo int, fragment string) int {
	t.Helper()
	if lineNo < 1 || lineNo > len(lines) {
		t.Fatalf("line %d is outside 1-%d", lineNo, len(lines))
	}
	column := strings.Index(lines[lineNo-1], fragment)
	if column < 0 {
		t.Fatalf("line %d does not contain %q: %q", lineNo, fragment, lines[lineNo-1])
	}
	return column + 1
}

func javaFirstDefinition(
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
	t.Fatalf("definition %q missing from %#v", symbol, definitions)
	return sourceDefinition{}
}

func javaHasOwningDefinition(definitions []sourceDefinition, symbol string) bool {
	for _, definition := range definitions {
		if definition.symbol == symbol && definition.ownsScope {
			return true
		}
	}
	return false
}

func javaLinesContainingSymbol(
	backend languageBackend,
	lines []string,
	symbol string,
) []int {
	counter := backend.(symbolOccurrenceCounter)
	result := make([]int, 0)
	for index, line := range lines {
		if counter.countSymbolOccurrences(line, symbol) > 0 {
			result = append(result, index+1)
		}
	}
	return result
}
