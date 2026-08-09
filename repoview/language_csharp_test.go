package repoview

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

type csharpDefinitionSummary struct {
	symbol     string
	line       int
	column     int
	scopeStart int
	scopeEnd   int
	ownsScope  bool
}

func TestCSharpBackendContractAndRegistration(t *testing.T) {
	t.Parallel()

	backend := newCSharpLanguage()
	if backend.name() != "cs" {
		t.Fatalf("language name = %q, want cs", backend.name())
	}
	contracts := []struct {
		name        string
		implemented bool
	}{
		{name: "sourceBackendPreparer", implemented: csharpImplements[sourceBackendPreparer](backend)},
		{name: "findScopeResolverPreparer", implemented: csharpImplements[findScopeResolverPreparer](backend)},
		{name: "linePreservingSourceCleaner", implemented: csharpImplements[linePreservingSourceCleaner](backend)},
		{name: "navigationScopeResolver", implemented: csharpImplements[navigationScopeResolver](backend)},
		{name: "sourceScopeNameResolver", implemented: csharpImplements[sourceScopeNameResolver](backend)},
		{name: "symbolOccurrenceCounter", implemented: csharpImplements[symbolOccurrenceCounter](backend)},
		{name: "sourceSymbolOccurrenceAugmenter", implemented: csharpImplements[sourceSymbolOccurrenceAugmenter](backend)},
		{name: "sourceSymbolOccurrencePositionAugmenter", implemented: csharpImplements[sourceSymbolOccurrencePositionAugmenter](backend)},
		{name: "authoritativeSymbolOnLineResolver", implemented: csharpImplements[authoritativeSymbolOnLineResolver](backend)},
	}
	for _, contract := range contracts {
		if !contract.implemented {
			t.Errorf("C# backend does not implement %s", contract.name)
		}
	}

	for _, extension := range []string{".cs", ".csx"} {
		registered := languageForExtension(extension)
		if registered.name() != "cs" {
			t.Errorf("registered %s language = %q, want cs", extension, registered.name())
		}
		if _, generic := registered.(braceLanguage); generic {
			t.Errorf("registered %s still uses generic braceLanguage", extension)
		}
		_, valueBackend := any(registered).(csharpLanguage)
		_, pointerBackend := any(registered).(*csharpLanguage)
		if !valueBackend && !pointerBackend {
			t.Errorf("registered %s backend = %T, want dedicated csharpLanguage", extension, registered)
		}
		if !defaultExtensions()[extension] {
			t.Errorf("registered %s is not in default source discovery", extension)
		}
	}
}

func TestCSharpDefinitionSymbolRecognizesDeclarationsAndRejectsExpressions(t *testing.T) {
	t.Parallel()

	backend := newCSharpLanguage()
	tests := []struct {
		name string
		line string
		want string
		ok   bool
	}{
		{name: "namespace", line: `namespace Demo.App;`, want: "Demo.App", ok: true},
		{name: "modified class", line: `public sealed class Service {`, want: "Service", ok: true},
		{name: "interface", line: `internal interface IService {`, want: "IService", ok: true},
		{name: "delegate", line: `public delegate void Handler(string value);`, want: "Handler", ok: true},
		{name: "generic method", line: `public T Map<T>(T value) where T : class {`, want: "Map", ok: true},
		{name: "abstract method", line: `public abstract int Parse(string text);`, want: "Parse", ok: true},
		{name: "property", line: `public int Value { get; init; }`, want: "Value", ok: true},
		{name: "event", line: `public event EventHandler Changed;`, want: "Changed", ok: true},
		{name: "using alias", line: `global using Text = System.Text.StringBuilder;`, want: "Text", ok: true},
		{name: "condition", line: `if (ready) {`},
		{name: "foreach", line: `foreach (var item in items) {`},
		{name: "using statement", line: `using (stream) {`},
		{name: "using var declaration", line: `using var stream = Open();`},
		{name: "using typed declaration", line: `using Stream stream = Open();`},
		{name: "invocation", line: `Target();`},
		{name: "qualified invocation", line: `System.Console.WriteLine();`},
		{name: "comment", line: `// public void Phantom() {`},
		{name: "literal", line: `const string text = "public void Phantom() {";`, want: "text", ok: true},
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

func TestCSharpDefinitionsHaveExactMetadataAndExcludeNonDeclarations(t *testing.T) {
	t.Parallel()

	const source = `namespace Demo.App
{
    public delegate int Converter(string value);

    public interface IService
    {
        string Name { get; }
        event EventHandler Changed;
        int Load(string value);
    }

    public enum State
    {
        Idle,
        Busy = 2,
    }

    public sealed class Service : IService
    {
        private readonly int first = 1, second = 2;
        public string Name { get; init; } = "";
        public event EventHandler Changed;

        public Service(int seed)
        {
            int local = seed;
        }

        public int Load(string value)
        {
            int local = value.Length;
            void LocalFunction() { }
            if (local > 0) {
                LocalFunction();
            }
            return Helper(local);
        }

        private int Helper(int value) => value;
    }
}
`
	lines := csharpTestLines(source)
	definitions := newCSharpLanguage().sourceDefinitions(lines)
	want := []csharpDefinitionSummary{
		csharpExpectedDefinition(t, lines, "Demo.App", 1, 1, 41, true),
		csharpExpectedDefinition(t, lines, "Converter", 3, 3, 3, false),
		csharpExpectedDefinition(t, lines, "IService", 5, 5, 10, true),
		csharpExpectedDefinition(t, lines, "Name", 7, 7, 7, true),
		csharpExpectedDefinition(t, lines, "Changed", 8, 8, 8, false),
		csharpExpectedDefinition(t, lines, "Load", 9, 9, 9, false),
		csharpExpectedDefinition(t, lines, "State", 12, 12, 16, true),
		csharpExpectedDefinition(t, lines, "Idle", 14, 14, 14, false),
		csharpExpectedDefinition(t, lines, "Busy", 15, 15, 15, false),
		csharpExpectedDefinition(t, lines, "Service", 18, 18, 40, true),
		csharpExpectedDefinition(t, lines, "first", 20, 20, 20, false),
		csharpExpectedDefinition(t, lines, "second", 20, 20, 20, false),
		csharpExpectedDefinition(t, lines, "Name", 21, 21, 21, true),
		csharpExpectedDefinition(t, lines, "Changed", 22, 22, 22, false),
		csharpExpectedDefinition(t, lines, "Service", 24, 24, 27, true),
		csharpExpectedDefinition(t, lines, "Load", 29, 29, 37, true),
		csharpExpectedDefinition(t, lines, "LocalFunction", 32, 32, 32, true),
		csharpExpectedDefinition(t, lines, "Helper", 39, 39, 39, true),
	}
	if got := csharpDefinitionSummaries(definitions); !reflect.DeepEqual(got, want) {
		t.Fatalf("definitions =\n%#v\nwant\n%#v", got, want)
	}

	symbols := csharpDefinitionSymbols(definitions)
	for _, forbidden := range []string{
		"value", "seed", "local", "Length", "get", "init", "if",
	} {
		if slices.Contains(symbols, forbidden) {
			t.Errorf("non-declaration %q became an outline definition: %#v", forbidden, symbols)
		}
	}
}

func TestCSharpImportsCoverAliasesGlobalUsingsAndExcludeUsingStatements(t *testing.T) {
	t.Parallel()

	const source = `extern alias Legacy;
global using System;
global using static System.Math;
global using Text = System.Text.StringBuilder;
using System.Collections.Generic;

namespace Demo;

public sealed class Holder
{
    public void Run()
    {
        using var stream = Open();
        using (stream)
        {
            _ = Text.Empty;
        }
    }
}
`
	lines := csharpTestLines(source)
	backend := newCSharpLanguage()
	start, end, ok := backend.importRange(lines)
	if !ok || start != 1 || end != 5 {
		t.Fatalf("import range = %d-%d, %v; want 1-5, true", start, end, ok)
	}

	definitions := backend.sourceDefinitions(lines)
	symbols := csharpDefinitionSymbols(definitions)
	for _, alias := range []string{"Text"} {
		if !slices.Contains(symbols, alias) {
			t.Errorf("alias %q missing from definitions: %#v", alias, symbols)
		}
	}
	for _, phantom := range []string{"System", "Math", "Collections", "stream", "Open"} {
		if slices.Contains(symbols, phantom) {
			t.Errorf("using operand or local %q became a definition: %#v", phantom, symbols)
		}
	}

	const topLevelStatements = `using System;
using var resource = Open();
using Stream other = Open();
System.Console.WriteLine(resource);
`
	for _, extension := range []string{".cs", ".csx"} {
		topLevelBackend := languageForExtension(extension)
		topLevelLines := csharpTestLines(topLevelStatements)
		if start, end, ok := topLevelBackend.importRange(topLevelLines); !ok ||
			start != 1 || end != 1 {
			t.Errorf("%s top-level import range = %d-%d, %v; want 1-1, true",
				extension, start, end, ok)
		}
		if got := csharpDefinitionSymbols(
			topLevelBackend.sourceDefinitions(topLevelLines),
		); len(got) != 0 {
			t.Errorf("%s top-level using declarations became definitions: %#v",
				extension, got)
		}
	}

	root := t.TempDir()
	writeFile(t, root, "imports.cs", source)
	response, err := mustView(t, root).Inspect(
		"imports.cs:16",
		Options{Include: IncludeImports, Return: ReturnScope},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 2 {
		t.Fatalf("Inspect imports results = %#v, want scope plus imports", response.Results)
	}
	imports := response.Results[1]
	if imports.Kind != "imports" || imports.Language != "cs" ||
		imports.StartLine != 1 || imports.EndLine != 5 ||
		imports.Code != strings.Join(lines[:5], "\n") {
		t.Fatalf("import result = %#v, want exact using span", imports)
	}
}

func TestCSharpNamedNavigationScopesAndPublicFindInspect(t *testing.T) {
	t.Parallel()

	const source = `namespace Demo;
public sealed class Service
{
    public int Run(int value)
    {
        if (value > 0)
        {
            return Target(value);
        }
        return Target(0);
    }
    private int Target(int value) => value;
}
`
	lines := csharpTestLines(source)
	backend := prepareLanguageBackend(newCSharpLanguage(), lines)
	if start, end := backend.enclosingScope(lines, 8); start != 6 || end != 9 {
		t.Fatalf("smallest enclosing scope = %d-%d, want if scope 6-9", start, end)
	}
	resolver := backend.(navigationScopeResolver)
	if start, end := resolver.navigationScope(lines, 8); start != 4 || end != 11 {
		t.Fatalf("named navigation scope = %d-%d, want Run 4-11", start, end)
	}
	if got := scopeName(lines, 8, backend); got != "Run" {
		t.Fatalf("scope name = %q, want Run", got)
	}

	root := t.TempDir()
	writeFile(t, root, "service.cs", source)
	view := mustView(t, root)
	found, err := view.Find("Target", Options{
		Include: IncludeRefs, Return: ReturnScope, NoComments: true, NoStrings: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found.Results) != 1 || found.Results[0].Language != "cs" ||
		found.Results[0].Scope != "Run" || found.Results[0].StartLine != 4 ||
		found.Results[0].EndLine != 11 {
		t.Fatalf("Target reference scope = %#v, want Run at 4-11", found.Results)
	}

	inspected, err := view.Inspect(
		"service.cs:8",
		Options{Include: IncludeScope, Return: ReturnScope},
	)
	if err != nil {
		t.Fatal(err)
	}
	if inspected.Symbol != "Target" || len(inspected.Results) != 1 ||
		inspected.Results[0].Scope != "Run" || inspected.Results[0].StartLine != 4 ||
		inspected.Results[0].EndLine != 11 {
		t.Fatalf("Inspect Target = %#v, want Target in Run", inspected)
	}
}

func TestCSharpCSXIsDiscoveredByFindAndUsesDedicatedBackend(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFile(t, root, "regular.cs", "public sealed class RegularEntry {}\n")
	writeFile(t, root, "script.csx", "public static class ScriptEntry {}\n")
	view := mustView(t, root)

	response, err := view.Find("ScriptEntry", Options{
		Include: IncludeDefs, Return: ReturnLocations,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].Path != "script.csx" ||
		response.Results[0].Kind != "def" || response.Results[0].Language != "cs" {
		t.Fatalf(".csx discovery result = %#v, want ScriptEntry definition", response.Results)
	}

	outline, err := view.Outline("script.csx", Options{Return: ReturnLocations})
	if err != nil {
		t.Fatal(err)
	}
	if got := csharpResultSymbols(outline.Results); !slices.Equal(got, []string{"ScriptEntry"}) {
		t.Fatalf(".csx outline symbols = %#v, want ScriptEntry", got)
	}
}

func csharpImplements[Contract any](backend any) bool {
	_, ok := backend.(Contract)
	return ok
}

func csharpTestLines(source string) []string {
	return strings.Split(strings.TrimSuffix(source, "\n"), "\n")
}

func csharpExpectedDefinition(
	t *testing.T,
	lines []string,
	symbol string,
	line, scopeStart, scopeEnd int,
	ownsScope bool,
) csharpDefinitionSummary {
	t.Helper()
	if line < 1 || line > len(lines) {
		t.Fatalf("expected definition %q line %d outside %d lines", symbol, line, len(lines))
	}
	column := strings.Index(lines[line-1], symbol) + 1
	if column < 1 {
		t.Fatalf("expected definition %q is absent from line %d: %q", symbol, line, lines[line-1])
	}
	return csharpDefinitionSummary{
		symbol: symbol, line: line, column: column,
		scopeStart: scopeStart, scopeEnd: scopeEnd, ownsScope: ownsScope,
	}
}

func csharpDefinitionSummaries(definitions []sourceDefinition) []csharpDefinitionSummary {
	result := make([]csharpDefinitionSummary, 0, len(definitions))
	for _, definition := range definitions {
		result = append(result, csharpDefinitionSummary{
			symbol: definition.symbol, line: definition.line, column: definition.column,
			scopeStart: definition.scopeStart, scopeEnd: definition.scopeEnd,
			ownsScope: definition.ownsScope,
		})
	}
	return result
}

func csharpDefinitionSymbols(definitions []sourceDefinition) []string {
	result := make([]string, len(definitions))
	for index, definition := range definitions {
		result[index] = definition.symbol
	}
	return result
}

func csharpResultSymbols(results []Result) []string {
	result := make([]string, len(results))
	for index, item := range results {
		result[index] = item.Symbol
	}
	return result
}
