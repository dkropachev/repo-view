package repoview

import (
	"slices"
	"strings"
	"testing"
)

func TestCSharpModernDeclarationsCoverCurrentCSharp14Surface(t *testing.T) {
	t.Parallel()

	const source = `namespace Modern.Navigation;

public partial record class Person<T>(string Name, int Age) where T : notnull
{
    public const int First = 1, Second = 2;
    private T primary, secondary;
    public event Action? Opened, Closed;
    public required string Title { get; init; }
    public ref readonly T Current => ref primary;

    public partial Person(int value);
    public partial event Action<int>? Changed;
    public partial string Label { get; set; }
    public partial string this[int index] { get; set; }
}

public partial record class Person<T>
{
    public partial Person(int value) : this(default!, value) { }
    public partial event Action<int>? Changed
    {
        add { }
        remove { }
    }
    public partial string Label
    {
        get => field;
        set => field = value;
    }
    public partial string this[int index]
    {
        get => index.ToString();
        set { }
    }

    public static Person<T> operator +(Person<T> left, Person<T> right) => left;
    public static implicit operator byte(Person<T> value) => 0;
    public static explicit operator checked short(Person<T> value) => 0;
    ~Person() { }
    void IDisposable.Dispose() { }
}

public readonly record struct Key(int Value);
public sealed class Service(string dependency)
{
    public string Run(int parameter) => dependency;
}
public ref struct Buffer<T>(T value) where T : allows ref struct { }
public interface IContract
{
    string Name { get; }
    string this[int index] { get; }
    event Action Changed;
    void Run();
}
public enum State { Ready, Busy = 2 }
public delegate TResult Mapper<in T, out TResult>(T value);
`

	definitions := newCSharpLanguage().sourceDefinitions(csharpTestLines(source))
	want := []string{
		"Modern.Navigation",
		"Person", "Name", "Age", "First", "Second", "primary", "secondary",
		"Opened", "Closed", "Title", "Current", "Person", "Changed", "Label", "this",
		"Person", "Person", "Changed", "Label", "this", "operator+",
		"implicit operator byte", "explicit operator checked short", "~Person", "Dispose",
		"Key", "Value", "Service", "Run", "Buffer", "IContract", "Name", "this",
		"Changed", "Run", "State", "Ready", "Busy", "Mapper",
	}
	if got := csharpDefinitionSymbols(definitions); !slices.Equal(got, want) {
		t.Fatalf("modern C# definitions =\n%#v\nwant\n%#v", got, want)
	}

	for _, forbidden := range []string{
		"T", "dependency", "value", "parameter", "index", "field", "add", "remove",
		"get", "set", "init",
	} {
		if slices.Contains(csharpDefinitionSymbols(definitions), forbidden) {
			t.Errorf("non-outline binding %q became a definition: %#v", forbidden, definitions)
		}
	}

	for _, symbol := range []string{
		"Modern.Navigation", "Person", "Title", "Current", "Changed", "Label", "this",
		"operator+", "implicit operator byte", "explicit operator checked short", "~Person",
		"Dispose", "Key", "Service", "Run", "Buffer", "IContract", "State",
	} {
		if !csharpHasOwningDefinition(definitions, symbol) {
			t.Errorf("definition %q has no owning declaration: %#v", symbol, definitions)
		}
	}
	for _, symbol := range []string{
		"Name", "Age", "First", "Second", "primary", "secondary", "Opened", "Closed",
		"Value", "Ready", "Busy",
	} {
		definition := csharpFirstDefinition(t, definitions, symbol)
		if definition.ownsScope || definition.scopeStart != definition.line ||
			definition.scopeEnd != definition.line {
			t.Errorf("non-owning definition %q has scope %#v", symbol, definition)
		}
	}
}

func TestCSharp14ExtensionBlocksAndInstanceOperatorsHaveStableSymbols(t *testing.T) {
	t.Parallel()

	const source = `public static class SequenceExtensions
{
    extension<T>(IEnumerable<T> source)
    {
        public bool IsEmpty => !source.Any();
        public IEnumerable<T> Take(int count) => source.Take(count);
    }

    extension<T>(IEnumerable<T>)
    {
        public static IEnumerable<T> Empty => [];
        public static IEnumerable<T> operator +(IEnumerable<T> left, T right) => [.. left, right];
    }
}

public struct Counter
{
    public void operator +=(int delta) { }
    public void operator ++() { }
    public static Counter operator checked +(Counter left, Counter right) => left;
}`

	definitions := newCSharpLanguage().sourceDefinitions(csharpTestLines(source))
	want := []string{
		"SequenceExtensions", "IsEmpty", "Take", "Empty", "operator+", "Counter",
		"operator+=", "operator++", "operator checked+",
	}
	if got := csharpDefinitionSymbols(definitions); !slices.Equal(got, want) {
		t.Fatalf("C# 14 extension/operator definitions = %#v, want %#v", got, want)
	}
	for _, forbidden := range []string{"extension", "T", "source", "count", "left", "right", "delta"} {
		if slices.Contains(csharpDefinitionSymbols(definitions), forbidden) {
			t.Errorf("extension receiver/parameter %q became definition: %#v", forbidden, definitions)
		}
	}
}

func TestCSharpTopLevelAndNestedLocalFunctionPolicy(t *testing.T) {
	t.Parallel()

	const source = `using System;

var topLevelLocal = 1;
Console.WriteLine(topLevelLocal);

int Compute(int parameter)
{
    var ordinaryLocal = parameter;
    int Nested(int nestedParameter) => nestedParameter + ordinaryLocal;
    Func<int, int> lambda = lambdaParameter => lambdaParameter;
    if (ordinaryLocal is int pattern) return Nested(pattern);
    return 0;
}

public class AfterTopLevel
{
    public void Member() { }
}`

	definitions := newCSharpLanguage().sourceDefinitions(csharpTestLines(source))
	if got, want := csharpDefinitionSymbols(definitions),
		[]string{"Compute", "Nested", "AfterTopLevel", "Member"}; !slices.Equal(got, want) {
		t.Fatalf("top-level/local-function policy = %#v, want %#v", got, want)
	}
	for _, forbidden := range []string{
		"Main", "topLevelLocal", "parameter", "ordinaryLocal", "nestedParameter", "lambda",
		"lambdaParameter", "pattern",
	} {
		if slices.Contains(csharpDefinitionSymbols(definitions), forbidden) {
			t.Errorf("local binding %q became definition: %#v", forbidden, definitions)
		}
	}
}

func TestCSharpRawVerbatimAndInterpolatedStringsPreserveOnlyExpressionCode(t *testing.T) {
	t.Parallel()

	const source = `class Strings
{
    string ordinary = "class FakeOrdinary { void Hidden() {} }";
    string verbatim = @"namespace FakeVerbatim { ""quoted"" }";
    string raw = """
        record FakeRaw(int Hidden);
        braces { } // still literal
        """;
    string manyQuotes = """""
        """ class FakeQuotes { }
        """"";
    string interpolated = $"literal FakeInterpolation {{ {RealRegular()} }}";
    string interpolatedVerbatim = $@"literal {RealVerbatim()} // not a comment";
    string rawOne = $"""
        literal FakeRawInterpolation
        {RealRaw()}
        """;
    string rawTwo = $$"""
        literal single braces {FakeSingleBrace()}
        {{RealDoubleBrace()}}
        """;
    ReadOnlySpan<byte> utf8 = """FakeUtf8()"""u8;

    void Tail() { RealTail(); }
}`

	lines := csharpTestLines(source)
	definitions := newCSharpLanguage().sourceDefinitions(lines)
	want := []string{
		"Strings", "ordinary", "verbatim", "raw", "manyQuotes", "interpolated",
		"interpolatedVerbatim", "rawOne", "rawTwo", "utf8", "Tail",
	}
	if got := csharpDefinitionSymbols(definitions); !slices.Equal(got, want) {
		t.Fatalf("string fixture definitions = %#v, want %#v", got, want)
	}

	root := t.TempDir()
	writeFile(t, root, "Strings.cs", source)
	view := mustView(t, root)
	for _, symbol := range []string{
		"RealRegular", "RealVerbatim", "RealRaw", "RealDoubleBrace", "RealTail",
	} {
		response, err := view.Find(symbol, Options{
			Include:   IncludeRefs,
			Return:    ReturnLocations,
			NoStrings: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(response.Results) != 1 {
			t.Errorf("Find(%q) with strings masked = %#v, want one expression reference",
				symbol, response.Results)
		}
	}
	for _, hidden := range []string{
		"FakeOrdinary", "FakeVerbatim", "FakeRaw", "FakeQuotes", "FakeInterpolation",
		"FakeRawInterpolation", "FakeSingleBrace", "FakeUtf8",
	} {
		response, err := view.Find(hidden, Options{
			Include:   IncludeRefs,
			Return:    ReturnLocations,
			NoStrings: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(response.Results) != 0 {
			t.Errorf("string literal text %q remained searchable: %#v", hidden, response.Results)
		}
	}
}

func TestCSharpUTF8StringSuffixesRemainPartOfEveryLiteralForm(t *testing.T) {
	t.Parallel()

	const source = `class Encodings
{
    ReadOnlySpan<byte> regularLower = "regular lower"u8;
    ReadOnlySpan<byte> regularUpper = "regular upper"U8;
    ReadOnlySpan<byte> verbatimLower = @"verbatim lower"u8;
    ReadOnlySpan<byte> verbatimUpper = @"verbatim upper"U8;
    ReadOnlySpan<byte> rawLower = """raw lower"""u8;
    ReadOnlySpan<byte> rawUpper = """raw upper"""U8;
}`

	root := t.TempDir()
	writeFile(t, root, "Encodings.cs", source)
	view := mustView(t, root)
	for _, suffix := range []string{"u8", "U8"} {
		response, err := view.Find(suffix, Options{
			Include:   IncludeRefs,
			Return:    ReturnLocations,
			NoStrings: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(response.Results) != 0 {
			t.Errorf("UTF-8 suffix %q remained searchable: %#v", suffix, response.Results)
		}
	}
}

func TestCSharpCompositeSymbolsRoundTripAcrossTriviaAndPhysicalLines(t *testing.T) {
	t.Parallel()

	const source = `namespace Alpha
    .
    Beta;

public class Number
{
    public static Number operator
        +
        (Number left, Number right) => left;

    public void Use()
    {
        Alpha
            /* qualified-name trivia */ .
            Beta
            . Target();
    }
}`

	lines := csharpTestLines(source)
	definitions := newCSharpLanguage().sourceDefinitions(lines)
	for _, symbol := range []string{"Alpha.Beta", "operator+"} {
		if !slices.Contains(csharpDefinitionSymbols(definitions), symbol) {
			t.Fatalf("fixture did not emit canonical definition %q: %#v", symbol, definitions)
		}
	}

	root := t.TempDir()
	writeFile(t, root, "Composite.cs", source)
	view := mustView(t, root)
	for _, symbol := range []string{"Alpha.Beta", "operator+"} {
		response, err := view.Find(symbol, Options{
			Include: IncludeDefs,
			Return:  ReturnLocations,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(response.Results) != 1 || response.Results[0].Kind != "def" {
			t.Errorf("Find(%q) definitions = %#v, want one canonical definition",
				symbol, response.Results)
		}
	}

	response, err := view.Find("Alpha.Beta.Target", Options{
		Include: IncludeRefs,
		Return:  ReturnLocations,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].Kind != "ref" ||
		response.Results[0].Line != 13 {
		t.Fatalf("multiline qualified reference = %#v, want one reference on line 13",
			response.Results)
	}
}

func TestCSharpCompositePositionCorrectionsCoverOverlappingMatches(t *testing.T) {
	t.Parallel()

	lines := []string{"a.a.a"}
	backend := prepareLanguageBackend(newCSharpLanguage(), lines).(csharpLanguage)
	adjustment := 0
	var added, removed []int
	handled := backend.walkAdditionalSymbolOccurrencesAt(
		lines, "a.a",
		func(_ int, count int, addedColumns, removedColumns []int) bool {
			adjustment = count
			added = append([]int(nil), addedColumns...)
			removed = append([]int(nil), removedColumns...)
			return true
		},
	)
	if !handled || adjustment != 1 || !slices.Equal(added, []int{3}) || len(removed) != 0 {
		t.Fatalf("overlap corrections = handled %v, count %d, added %#v, removed %#v",
			handled, adjustment, added, removed)
	}
}

func TestCSharpDestructorFindRejectsUnaryComplementSequences(t *testing.T) {
	t.Parallel()

	const source = `class C
{
    ~ C() {}

    void Use()
    {
        int C = 1;
        _ = ~ C;
        _ = ~C;
    }
}`
	root := t.TempDir()
	writeFile(t, root, "Destructor.cs", source)
	view := mustView(t, root)

	definitions, err := view.Find("~C", Options{
		Include: IncludeDefs,
		Return:  ReturnLocations,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(definitions.Results) != 1 || definitions.Results[0].Kind != "def" ||
		definitions.Results[0].Line != 3 {
		t.Fatalf("spaced destructor definitions = %#v, want one definition on line 3",
			definitions.Results)
	}

	references, err := view.Find("~C", Options{
		Include: IncludeRefs,
		Return:  ReturnLocations,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(references.Results) != 0 {
		t.Fatalf("unary complement sequences became destructor references: %#v",
			references.Results)
	}
}

func TestCSharpIdentifiersUseRawSourceIdentityAndExactByteCoordinates(t *testing.T) {
	t.Parallel()

	const source = `namespace Κατάλογος.Navigation;
public class @class
{
    int café;
    int \u03B3amma;
    int cl\u0061ss;
    void método() { café++; }
}`

	lines := csharpTestLines(source)
	definitions := newCSharpLanguage().sourceDefinitions(lines)
	want := []string{
		"Κατάλογος.Navigation", "@class", "café", `\u03B3amma`, `cl\u0061ss`, "método",
	}
	if got := csharpDefinitionSymbols(definitions); !slices.Equal(got, want) {
		t.Fatalf("Unicode/raw identifier definitions = %#v, want %#v", got, want)
	}
	for _, definition := range definitions {
		if definition.line < 1 || definition.line > len(lines) || definition.column < 1 {
			t.Fatalf("invalid identifier coordinate: %#v", definition)
		}
		line := lines[definition.line-1]
		if definition.symbol != "Κατάλογος.Navigation" &&
			!strings.HasPrefix(line[definition.column-1:], definition.symbol) {
			t.Errorf("definition is not source-backed at byte coordinate: %#v in %q",
				definition, line)
		}
	}

	root := t.TempDir()
	writeFile(t, root, "Identifiers.cs", source)
	view := mustView(t, root)
	for _, raw := range []string{"@class", `\u03B3amma`, `cl\u0061ss`} {
		response, err := view.Find(raw, Options{Include: IncludeDefs, Return: ReturnLocations})
		if err != nil {
			t.Fatal(err)
		}
		if len(response.Results) != 1 || response.Results[0].Symbol != raw {
			t.Errorf("raw Find(%q) = %#v, want exact definition", raw, response.Results)
		}
	}
	for _, nonRaw := range []string{"class", "γamma"} {
		response, err := view.Find(nonRaw, Options{Include: IncludeDefs, Return: ReturnLocations})
		if err != nil {
			t.Fatal(err)
		}
		if len(response.Results) != 0 {
			t.Errorf("canonicalized query %q matched raw identifier: %#v", nonRaw, response.Results)
		}
	}
}

func TestCSharpDirectivesKeepBranchesNavigableAndIsolateDelimiterState(t *testing.T) {
	t.Parallel()

	const source = `#define FEATURE
string fake = "#if X class StringBranch {} #endif";
// #if X class CommentBranch {} #endif
#if FEATURE
class Enabled { void InEnabled() { } }
#else
class Disabled { void InDisabled() { } }
#endif

#if false
{
#endif
class Recovered { void Tail() { } }

#if OUTER
class FirstBranch { }
#elif OTHER
class SecondBranch { }
#else
class ThirdBranch { }
#endif
`

	definitions := newCSharpLanguage().sourceDefinitions(csharpTestLines(source))
	want := []string{
		"FEATURE", "Enabled", "InEnabled", "Disabled", "InDisabled", "Recovered", "Tail",
		"FirstBranch", "SecondBranch", "ThirdBranch",
	}
	if got := csharpDefinitionSymbols(definitions); !slices.Equal(got, want) {
		t.Fatalf("directive branch definitions = %#v, want %#v", got, want)
	}
	for _, phantom := range []string{"StringBranch", "CommentBranch", "fake"} {
		if slices.Contains(csharpDefinitionSymbols(definitions), phantom) {
			t.Errorf("directive/string phantom %q became definition: %#v", phantom, definitions)
		}
	}
	recovered := csharpFirstDefinition(t, definitions, "Recovered")
	if !recovered.ownsScope || recovered.scopeStart != 13 || recovered.scopeEnd != 13 {
		t.Errorf("inactive-branch brace contaminated recovered scope: %#v", recovered)
	}
}

func TestCSharpDefineWithTrailingWhitespaceRemainsFindable(t *testing.T) {
	t.Parallel()

	const source = "#define FEATURE   \nclass Enabled {}\n"
	root := t.TempDir()
	writeFile(t, root, "Feature.cs", source)

	response, err := mustView(t, root).Find("FEATURE", Options{
		Include: IncludeDefs,
		Return:  ReturnLocations,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].Kind != "def" ||
		response.Results[0].Language != "cs" || response.Results[0].Line != 1 {
		t.Fatalf("Find FEATURE = %#v, want exact C# definition on line 1", response.Results)
	}
}

func TestCSharpEscapedBraceBeforeInterpolationRemainsFindable(t *testing.T) {
	t.Parallel()

	const source = `class Example
{
    object value;
    string Render() => $"{{{value}}}";
}
`
	root := t.TempDir()
	writeFile(t, root, "Interpolation.cs", source)

	response, err := mustView(t, root).Find("value", Options{
		Include: IncludeRefs,
		Return:  ReturnLocations,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].Kind != "ref" ||
		response.Results[0].Language != "cs" || response.Results[0].Line != 4 {
		t.Fatalf("Find interpolated value = %#v, want C# reference on line 4",
			response.Results)
	}
}

func TestCSharpImportsCoverAliasesGlobalUsingAndFileAppDependencies(t *testing.T) {
	t.Parallel()

	const source = `#!/usr/bin/env dotnet
#:package Spectre.Console@1.0.0
#:sdk Microsoft.NET.Sdk.Web
#:project ../Shared/Shared.csproj
#:property PublishAot=false
extern alias Legacy;
global using System;
global using static System.Math;
global using Collections = global::System.Collections.Generic;
using unsafe Pointer = int*;

class Uses
{
    string text = "using Fake = Hidden.Type;";
    void Work()
    {
        using var resource = Open();
        using (resource) { }
    }
}
// using CommentOnly = Hidden.Type;
`
	lines := csharpTestLines(source)
	backend := newCSharpLanguage()
	if start, end, ok := backend.importRange(lines); !ok || start != 2 || end != 10 {
		t.Fatalf("C# import range = %d-%d, %v; want 2-10, true", start, end, ok)
	}
	if got, want := csharpDefinitionSymbols(backend.sourceDefinitions(lines)),
		[]string{"Legacy", "Collections", "Pointer", "Uses", "text", "Work"}; !slices.Equal(got, want) {
		t.Fatalf("import fixture definitions = %#v, want %#v", got, want)
	}

	const configurationOnly = `#:property PublishAot=true
string text = "#:package Fake@1.0";
// #:project Fake.csproj
`
	if start, end, ok := backend.importRange(csharpTestLines(configurationOnly)); ok {
		t.Fatalf("configuration-only file has imports %d-%d, true; want none", start, end)
	}
}

func TestCSharpMalformedRecoveryKeepsIndependentDeclarationsWithoutPhantoms(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		source    string
		want      []string
		forbidden []string
	}{
		{
			name: "broken parameter list",
			source: `class Owner {
    void Before() { }
    void Broken(
    void After() { }
}`,
			want:      []string{"Owner", "Before", "After"},
			forbidden: []string{"Broken"},
		},
		{
			name: "ordinary string ends recovery at newline",
			source: `class Owner {
    string broken = "unterminated
    void After() { Target(); }
}`,
			want:      []string{"Owner", "After"},
			forbidden: []string{"unterminated", "Target"},
		},
		{
			name: "unterminated raw string resynchronizes at declaration",
			source: `class Before { }
string broken = """
    fake class Hidden { }
class After { void Tail() { } }`,
			want:      []string{"Before", "After", "Tail"},
			forbidden: []string{"Hidden"},
		},
		{
			name: "stray delimiters",
			source: `class Before { }
} ] )
class After { void Tail() { } }`,
			want: []string{"Before", "After", "Tail"},
		},
		{
			name: "broken attribute and generic",
			source: `class Before { }
[Broken(
class Generic<T
class After { void Tail() { } }`,
			want:      []string{"Before", "After", "Tail"},
			forbidden: []string{"Broken", "Generic", "T"},
		},
		{
			name: "unmatched generic before expression body",
			source: `class Before { }
class Owner {
    Task<Result P => service.Client.Compute();
}
class After { void Tail() { } }`,
			want:      []string{"Before", "Owner", "After", "Tail"},
			forbidden: []string{"Result", "P", "Client", "Compute"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			definitions := newCSharpLanguage().sourceDefinitions(csharpTestLines(test.source))
			if got := csharpDefinitionSymbols(definitions); !slices.Equal(got, test.want) {
				t.Fatalf("malformed recovery definitions = %#v, want %#v", got, test.want)
			}
			for _, forbidden := range test.forbidden {
				if slices.Contains(csharpDefinitionSymbols(definitions), forbidden) {
					t.Errorf("malformed syntax promoted %q: %#v", forbidden, definitions)
				}
			}
			csharpAssertDefinitionCoordinates(t, csharpTestLines(test.source), definitions)
		})
	}
}

func TestCSharpContextualIdentifiersAndCallsAreNotGloballyMisclassified(t *testing.T) {
	t.Parallel()

	const source = `class Contextual
{
    int record, required, scoped, field, extension;
    void partial() { }
    void Caller()
    {
        record();
        extension<int>(value);
		service.Run();
		service.Client.Run();
		System.Console.WriteLine();
		new Constructed();
		var local = factory.Build();
		if (local is { required: var pattern }) Target(pattern);
	}
	async void AsyncCaller()
	{
		await service.Client.RunAsync();
	}
}`
	definitions := newCSharpLanguage().sourceDefinitions(csharpTestLines(source))
	want := []string{
		"Contextual", "record", "required", "scoped", "field", "extension", "partial", "Caller",
		"AsyncCaller",
	}
	if got := csharpDefinitionSymbols(definitions); !slices.Equal(got, want) {
		t.Fatalf("contextual identifier definitions = %#v, want %#v", got, want)
	}
	for _, phantom := range []string{
		"Run", "WriteLine", "Constructed", "local", "Build", "pattern", "Target", "RunAsync",
	} {
		if slices.Contains(csharpDefinitionSymbols(definitions), phantom) {
			t.Errorf("expression/binding %q became definition: %#v", phantom, definitions)
		}
	}
}

func TestCSharpContextualTypesDoNotConfuseLocalsAndLocalFunctions(t *testing.T) {
	t.Parallel()

	const source = `class record { }
class extension { }
class Context
{
    extension Member()
    {
        int memberLocal = 0;
        return new();
    }
    void Work()
    {
        record value = new();
        extension Local()
        {
            int local = 0;
            return new();
        }
    }
}`
	want := []string{"record", "extension", "Context", "Member", "Work", "Local"}
	definitions := newCSharpLanguage().sourceDefinitions(csharpTestLines(source))
	if got := csharpDefinitionSymbols(definitions); !slices.Equal(got, want) {
		t.Fatalf("contextual-type definitions = %#v, want %#v", got, want)
	}
	for _, phantom := range []string{"memberLocal", "value", "local"} {
		if slices.Contains(csharpDefinitionSymbols(definitions), phantom) {
			t.Errorf("contextual-type local %q became a definition: %#v",
				phantom, definitions)
		}
	}
}

func TestCSharpBodyRecoveryKeepsCleanContextualReturnDeclaration(t *testing.T) {
	t.Parallel()

	const source = `class file { }
class Context
{
    file Load()
    {
        ???
    }
}`
	want := []string{"file", "Context", "Load"}
	definitions := newCSharpLanguage().sourceDefinitions(csharpTestLines(source))
	if got := csharpDefinitionSymbols(definitions); !slices.Equal(got, want) {
		t.Fatalf("body-recovery definitions = %#v, want %#v", got, want)
	}
	load := csharpFirstDefinition(t, definitions, "Load")
	if !load.ownsScope || load.scopeStart != 4 || load.scopeEnd != 7 {
		t.Fatalf("body-recovery method scope = %#v, want owning 4-7", load)
	}
}

func TestCSharpScopesAndInspectPreferNamedMemberOverInnerBlocks(t *testing.T) {
	t.Parallel()

	const source = `/// <summary>Service docs.</summary>
[Obsolete]
class Service
{
    string Value
    {
        get
        {
            if (Ready())
            {
                return Target();
            }
            return Fallback();
        }
    }
}`
	lines := csharpTestLines(source)
	backend := prepareLanguageBackend(newCSharpLanguage(), lines)
	if start, end := backend.enclosingScope(lines, 11); start != 9 || end != 12 {
		t.Fatalf("smallest C# if scope = %d-%d, want 9-12", start, end)
	}
	resolver, ok := backend.(navigationScopeResolver)
	if !ok {
		t.Fatal("C# backend does not expose named navigation scopes")
	}
	if start, end := resolver.navigationScope(lines, 11); start != 5 || end != 15 {
		t.Fatalf("property navigation scope = %d-%d, want 5-15", start, end)
	}

	root := t.TempDir()
	writeFile(t, root, "Service.cs", source)
	response, err := mustView(t, root).Inspect(
		"Service.cs:11", Options{Include: IncludeScope, Return: ReturnScope},
	)
	if err != nil {
		t.Fatal(err)
	}
	if response.Symbol != "Target" || len(response.Results) != 1 ||
		response.Results[0].Scope != "Value" || response.Results[0].StartLine != 5 ||
		response.Results[0].EndLine != 15 {
		t.Fatalf("C# Inspect target = %#v, want Target in Value at 5-15", response)
	}
	service := csharpFirstDefinition(t, backend.sourceDefinitions(lines), "Service")
	if service.scopeStart != 1 || service.scopeEnd != 16 {
		t.Errorf("documented/attributed type scope = %#v, want 1-16", service)
	}
}

func csharpFirstDefinition(
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
	t.Fatalf("missing C# definition %q in %#v", symbol, definitions)
	return sourceDefinition{}
}

func csharpHasOwningDefinition(definitions []sourceDefinition, symbol string) bool {
	for _, definition := range definitions {
		if definition.symbol == symbol && definition.ownsScope {
			return true
		}
	}
	return false
}

func csharpAssertDefinitionCoordinates(
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
			t.Fatalf("invalid C# definition coordinate: %#v", definition)
		}
	}
}
