package repoview

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestCSharpSearchMaskingCleaningFindAndInspect(t *testing.T) {
	t.Parallel()

	const source = `namespace Demo;
public sealed class SearchFixture
{
    private const string Regular = "target // literal";
    private const string Verbatim = @"target ""quoted"" // literal";
    private const string Raw = """
target // raw
} target {
""";

    private int Interpolated(int value)
    {
        var text = $"literal target {target(value)}";
        /* target in block
           target continuation */
        // target in line comment
        return target(value);
    }

    private int target(int value) => value;
}
`
	lines := csharpTestLines(source)
	backend := prepareLanguageBackend(newCSharpLanguage(), lines)
	searchable := backend.searchLines(lines, true, true)
	if len(searchable) != len(lines) ||
		len(strings.Join(searchable, "\n")) != len(strings.Join(lines, "\n")) {
		t.Fatalf("search mask changed physical coordinates: %#v", searchable)
	}
	counter := backend.(symbolOccurrenceCounter)
	wantCodeLines := map[int]int{
		csharpLineContaining(t, lines, `$"literal target`): 1,
		csharpLineContaining(t, lines, "return target"):    1,
		csharpLineContaining(t, lines, "int target("):      1,
	}
	for index, line := range searchable {
		if got, want := counter.countSymbolOccurrences(line, "target"), wantCodeLines[index+1]; got != want {
			t.Errorf("masked line %d target count = %d, want %d; line=%q",
				index+1, got, want, line)
		}
	}

	commentsOnly := backend.searchLines(lines, true, false)
	for _, marker := range []string{
		`"target // literal"`, `@"target ""quoted"" // literal"`,
		"target // raw", "} target {",
	} {
		lineNo := csharpLineContaining(t, lines, marker)
		if !strings.Contains(commentsOnly[lineNo-1], "target") {
			t.Errorf("comment-only masking removed string payload on line %d: %q",
				lineNo, commentsOnly[lineNo-1])
		}
	}
	for _, marker := range []string{"target in block", "target continuation", "target in line comment"} {
		lineNo := csharpLineContaining(t, lines, marker)
		if strings.Contains(commentsOnly[lineNo-1], "target") {
			t.Errorf("comment-only masking retained comment on line %d: %q",
				lineNo, commentsOnly[lineNo-1])
		}
	}

	stringsOnly := backend.searchLines(lines, false, true)
	for _, marker := range []string{"target // literal", `@"target`, "target // raw", "} target {"} {
		lineNo := csharpLineContaining(t, lines, marker)
		if strings.Contains(stringsOnly[lineNo-1], "target") {
			t.Errorf("string-only masking retained literal payload on line %d: %q",
				lineNo, stringsOnly[lineNo-1])
		}
	}
	for _, marker := range []string{"target in block", "target continuation", "target in line comment"} {
		lineNo := csharpLineContaining(t, lines, marker)
		if !strings.Contains(stringsOnly[lineNo-1], "target") {
			t.Errorf("string-only masking removed comment on line %d: %q",
				lineNo, stringsOnly[lineNo-1])
		}
	}
	interpolationLine := csharpLineContaining(t, lines, `$"literal target`)
	if got := counter.countSymbolOccurrences(stringsOnly[interpolationLine-1], "target"); got != 1 {
		t.Fatalf("interpolated expression target count = %d, want 1; masked=%q",
			got, stringsOnly[interpolationLine-1])
	}

	cleaner := backend.(linePreservingSourceCleaner)
	cleanedLines := cleaner.cleanSourceLines(lines, true, false)
	if len(cleanedLines) != len(lines) {
		t.Fatalf("cleaned lines = %d, want %d", len(cleanedLines), len(lines))
	}
	cleaned := strings.Join(cleanedLines, "\n")
	if !strings.Contains(cleaned, `"target // literal"`) ||
		!strings.Contains(cleaned, "target // raw") ||
		strings.Contains(cleaned, "target in block") ||
		strings.Contains(cleaned, "target continuation") ||
		strings.Contains(cleaned, "target in line comment") {
		t.Fatalf("line-preserving C# comment cleaning = %q", cleaned)
	}

	root := t.TempDir()
	writeFile(t, root, "search.cs", source)
	view := mustView(t, root)
	found, err := view.Find("target", Options{
		Include: IncludeBoth, Return: ReturnLocations,
		NoComments: true, NoStrings: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantLines := []int{
		interpolationLine,
		csharpLineContaining(t, lines, "return target"),
		csharpLineContaining(t, lines, "int target("),
	}
	if got := resultLines(found.Results); !slices.Equal(got, wantLines) {
		t.Fatalf("Find target lines = %#v, want %#v", got, wantLines)
	}
	if got, want := csharpResultKinds(found.Results), []string{"ref", "ref", "def"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("Find target kinds = %#v, want %#v", got, want)
	}

	for _, marker := range []string{"target // raw", "target in block", "target in line comment"} {
		lineNo := csharpLineContaining(t, lines, marker)
		response, inspectErr := view.Inspect(
			fmt.Sprintf("search.cs:%d", lineNo),
			Options{Include: IncludeScope, Return: ReturnScope},
		)
		if inspectErr != nil {
			t.Fatal(inspectErr)
		}
		if response.Symbol != "" {
			t.Errorf("Inspect on opaque line %d selected %q", lineNo, response.Symbol)
		}
	}
	callLine := csharpLineContaining(t, lines, "return target")
	inspected, err := view.Inspect(
		fmt.Sprintf("search.cs:%d", callLine),
		Options{Include: IncludeScope, Return: ReturnScope},
	)
	if err != nil {
		t.Fatal(err)
	}
	if inspected.Symbol != "target" || len(inspected.Results) != 1 ||
		inspected.Results[0].Scope != "Interpolated" {
		t.Fatalf("Inspect target call = %#v, want target in Interpolated", inspected)
	}
}

func TestCSharpControlFlowCallsCommentsAndStringsNeverBecomeDefinitions(t *testing.T) {
	t.Parallel()

	const source = `public sealed class Fixture
{
    public void Run()
    {
        if (ready) { Target(); }
        while (ready) { Target(); }
        foreach (var item in items) { Target(item); }
        switch (value) { default: break; }
        lock (gate) { Target(); }
        using (stream) { Target(); }
        try { Target(); } catch (Exception error) { Target(error); }
        Target();
        var created = new Target();
        var text = "public void StringPhantom() {";
        /* public void BlockPhantom() { */
        // public void LinePhantom() {
    }
}
`
	definitions := newCSharpLanguage().sourceDefinitions(csharpTestLines(source))
	if got, want := csharpDefinitionSymbols(definitions), []string{"Fixture", "Run"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("definitions = %#v, want %#v", got, want)
	}
}

func TestCSharpFindClassifiesInterfaceAndConcreteMethods(t *testing.T) {
	t.Parallel()

	const source = `namespace Demo;
public interface IWorker
{
    void Work();
}
public sealed class Worker : IWorker
{
    public void Work()
    {
    }
    public void Caller()
    {
        Work();
    }
}
`
	root := t.TempDir()
	writeFile(t, root, "worker.cs", source)
	view := mustView(t, root)
	response, err := view.Find("Work", Options{
		Include: IncludeBoth, Return: ReturnLocations,
		NoComments: true, NoStrings: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := resultLines(response.Results), []int{4, 8, 13}; !slices.Equal(got, want) {
		t.Fatalf("Work lines = %#v, want %#v", got, want)
	}
	if got, want := csharpResultKinds(response.Results), []string{"def", "def", "ref"}; !slices.Equal(got, want) {
		t.Fatalf("Work kinds = %#v, want %#v", got, want)
	}

	partial, err := view.Find("Wor", Options{Include: IncludeBoth, Return: ReturnLocations})
	if err != nil {
		t.Fatal(err)
	}
	if len(partial.Results) != 0 {
		t.Fatalf("partial C# identifier matched: %#v", partial.Results)
	}
}

func TestCSharpMalformedSourcesRecoverIndependentDeclarations(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		source string
	}{
		{
			name: "broken parameter list",
			source: `public class Fixture {
    public void Before() { }
    public void Broken(
    ???
    public void After() { }
}
`,
		},
		{
			name: "newline terminates malformed regular string",
			source: `public class Fixture {
    public void Before() { }
    string broken = "unterminated
    public void After() { }
}
`,
		},
		{
			name: "stray closing delimiters",
			source: `public class Fixture {
    public void Before() { }
    }]);
    public void After() { }
}
`,
		},
		{
			name: "broken generic declaration",
			source: `public class Fixture {
    public void Before() { }
    public TResult Broken<TValue, TResult(TValue value)
    public void After() { }
}
`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			lines := csharpTestLines(test.source)
			backend := prepareLanguageBackend(newCSharpLanguage(), lines)
			definitions := backend.sourceDefinitions(lines)
			symbols := csharpDefinitionSymbols(definitions)
			for _, required := range []string{"Fixture", "Before", "After"} {
				if !slices.Contains(symbols, required) {
					t.Errorf("malformed source lost %q: %#v", required, definitions)
				}
			}
			for _, forbidden := range []string{"TValue", "TResult", "value"} {
				if slices.Contains(symbols, forbidden) {
					t.Errorf("malformed declaration promoted %q: %#v", forbidden, definitions)
				}
			}
			for _, options := range [][2]bool{
				{false, false}, {true, false}, {false, true}, {true, true},
			} {
				searchable := backend.searchLines(lines, options[0], options[1])
				if len(searchable) != len(lines) ||
					len(strings.Join(searchable, "\n")) != len(strings.Join(lines, "\n")) {
					t.Fatalf("search mask changed malformed-source coordinates: %#v", searchable)
				}
			}
		})
	}

	const unterminatedComment = `public class Fixture {
    public void Before() { }
    /* unterminated comment
    public void Hidden() { }
}
`
	if got, want := csharpDefinitionSymbols(
		newCSharpLanguage().sourceDefinitions(csharpTestLines(unterminatedComment)),
	), []string{"Fixture", "Before"}; !slices.Equal(got, want) {
		t.Fatalf("unterminated-comment definitions = %#v, want %#v", got, want)
	}
}

func TestCSharpInvalidUTF8AndIncompleteInputsNeverPanic(t *testing.T) {
	t.Parallel()

	invalidUTF8 := "public class Before {}\nstring payload = \"" +
		string([]byte{0xff, 0xfe}) + "\";\npublic class After {}\n"
	corpus := []string{
		"",
		"public class Open {\n",
		"public void Open<T(\n",
		"#if FEATURE\npublic class Conditional {\n",
		"var raw = \"\"\"unterminated\n",
		"/* unterminated\npublic class Hidden {}\n",
		invalidUTF8,
	}
	for index, source := range corpus {
		t.Run(fmt.Sprintf("case_%d", index), func(t *testing.T) {
			t.Parallel()
			lines := csharpTestLines(source)
			backend := prepareLanguageBackend(newCSharpLanguage(), lines)
			definitions := backend.sourceDefinitions(lines)
			_, _, _ = backend.importRange(lines)
			_ = backend.ignoredSearchLines(lines, true, false)
			_ = backend.cleanSource(source, true, false)
			for _, options := range [][2]bool{
				{false, false}, {true, false}, {false, true}, {true, true},
			} {
				searchable := backend.searchLines(lines, options[0], options[1])
				if len(searchable) != len(lines) {
					t.Fatalf("searchable lines = %d, want %d", len(searchable), len(lines))
				}
			}
			_, _ = backend.enclosingScope(lines, 1)
			_, _ = backend.enclosingScope(lines, len(lines))
			if resolver, ok := backend.(navigationScopeResolver); ok {
				_, _ = resolver.navigationScope(lines, 1)
				_, _ = resolver.navigationScope(lines, len(lines))
			}
			for _, definition := range definitions {
				if definition.line < 1 || definition.line > len(lines) ||
					definition.column < 1 || definition.scopeStart < 1 ||
					definition.scopeEnd < definition.scopeStart ||
					definition.scopeEnd > len(lines) {
					t.Fatalf("definition outside source coordinates: %#v (lines=%d)",
						definition, len(lines))
				}
			}
			for _, line := range lines {
				_, _ = backend.definitionSymbol(line)
				_ = backend.stripComment(line)
			}
		})
	}
}

func csharpLineContaining(t *testing.T, lines []string, marker string) int {
	t.Helper()
	for index, line := range lines {
		if strings.Contains(line, marker) {
			return index + 1
		}
	}
	t.Fatalf("marker %q is absent from source", marker)
	return 0
}

func csharpResultKinds(results []Result) []string {
	kinds := make([]string, len(results))
	for index, result := range results {
		kinds[index] = result.Kind
	}
	return kinds
}
