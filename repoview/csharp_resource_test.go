package repoview

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"sync"
	"testing"
)

func TestCSharpOverConcreteByteTokenAndDepthCapsRetainsIndependentTail(t *testing.T) {
	tests := []struct {
		name      string
		source    string
		want      []string
		forbidden []string
	}{
		{
			name: "byte cap",
			source: "class Head { }\n/*" +
				strings.Repeat(" opaque { < Fake(); ", csharpMaximumConcreteParseBytes/16) +
				`*/
using unsafe Callback = delegate*<void>;
using unsafe VoidPointer = void*;
class extension<T> { }
class ByteTail
{
    int first, second;
    bool Ready => !service.Client.Any();
    int Transform(int value) => service.Client.Map(value);
    void IDisposable.Dispose() { }
    extension<int> GenericMember()
    {
        int genericLocal = 0;
        return new();
    }
    void Recovered()
    {
        int Local(int value) => service.Client.Convert(value);
        System.Console.WriteLine();
    }
}
`,
			want: []string{
				"Callback", "VoidPointer", "extension", "ByteTail", "first", "second", "Ready",
				"Transform", "Dispose", "GenericMember", "Recovered", "Local",
			},
			forbidden: []string{"Any", "Map", "genericLocal", "Convert", "WriteLine"},
		},
		{
			name: "token cap",
			source: "class TokenHost { void Work() {\n" +
				strings.Repeat("value++;\n", csharpMaximumConcreteTokens/3+1) +
				"} }\nclass TokenTail { }\n",
			want: []string{"TokenTail"},
		},
		{
			name: "delimiter depth",
			source: "class Deep { void Work() {\n" +
				strings.Repeat("{\n", csharpMaximumConcreteDelimiterDepth+1) +
				"Target();\n" +
				strings.Repeat("}\n", csharpMaximumConcreteDelimiterDepth+1) +
				"} }\nclass DepthTail { }\n",
			want: []string{"DepthTail"},
		},
		{
			name: "preprocessor depth",
			source: strings.Repeat("#if FEATURE\n", csharpMaximumConcretePreprocessorDepth+1) +
				"class NestedBranch { }\n" +
				strings.Repeat("#endif\n", csharpMaximumConcretePreprocessorDepth+1) +
				"class DirectiveTail { }\n",
			want: []string{"DirectiveTail"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			lines := csharpTestLines(test.source)
			if lexed := lexCSharp(test.source); lexed.concreteEligible {
				t.Fatal("over-cap fixture remained concrete-eligible")
			}
			analysis := analyzeCSharpSource(test.source, len(lines))
			if analysis == nil {
				t.Fatal("analyzeCSharpSource returned nil")
			}
			if analysis.tree != nil {
				t.Fatal("over-cap fixture unexpectedly retained a concrete syntax tree")
			}
			definitions := newCSharpLanguage().sourceDefinitions(lines)
			symbols := csharpDefinitionSymbols(definitions)
			for _, want := range test.want {
				if !slices.Contains(symbols, want) {
					t.Errorf("over-cap fallback lost %q: %#v", want, definitions)
				}
			}
			for _, forbidden := range test.forbidden {
				if slices.Contains(symbols, forbidden) {
					t.Errorf("over-cap fallback promoted call %q: %#v", forbidden, definitions)
				}
			}
			if test.name == "byte cap" {
				if start, end, ok := newCSharpLanguage().importRange(lines); !ok ||
					start != 3 || end != 4 {
					t.Errorf("fallback alias import range = %d-%d, %v; want 3-4, true",
						start, end, ok)
				}
				for _, symbol := range []string{"first", "second"} {
					definition := csharpFirstDefinition(t, definitions, symbol)
					if definition.ownsScope {
						t.Errorf("fallback field %q unexpectedly owns a scope: %#v",
							symbol, definition)
					}
				}
			}
			csharpAssertDefinitionCoordinates(t, lines, definitions)
		})
	}
}

func TestCSharpConcreteCapBoundariesAreInclusive(t *testing.T) {
	atByteCap := strings.Repeat(" ", csharpMaximumConcreteParseBytes)
	if lexed := lexCSharp(atByteCap); !lexed.concreteEligible {
		t.Fatal("source exactly at C# byte cap was rejected")
	}
	overByteCap := atByteCap + " "
	if lexed := lexCSharp(overByteCap); lexed.concreteEligible {
		t.Fatal("source over C# byte cap remained concrete-eligible")
	}

	atTokenCap := strings.Repeat(";", csharpMaximumConcreteTokens)
	if lexed := lexCSharp(atTokenCap); !lexed.concreteEligible ||
		lexed.lexicalUnits != csharpMaximumConcreteTokens {
		t.Fatalf("source at token cap = (%t, %d), want eligible with %d units",
			lexed.concreteEligible, lexed.lexicalUnits, csharpMaximumConcreteTokens)
	}
	overTokenCap := atTokenCap + ";"
	if lexed := lexCSharp(overTokenCap); lexed.concreteEligible {
		t.Fatal("source over C# token cap remained concrete-eligible")
	}

	atDepth := strings.Repeat("(", csharpMaximumConcreteDelimiterDepth) + "0" +
		strings.Repeat(")", csharpMaximumConcreteDelimiterDepth)
	if lexed := lexCSharp(atDepth); !lexed.concreteEligible ||
		lexed.maximumDepth != csharpMaximumConcreteDelimiterDepth {
		t.Fatalf("source at delimiter cap = (%t, %d), want eligible at %d",
			lexed.concreteEligible, lexed.maximumDepth, csharpMaximumConcreteDelimiterDepth)
	}
	overDepth := "(" + atDepth + ")"
	if lexed := lexCSharp(overDepth); lexed.concreteEligible {
		t.Fatal("source over C# delimiter cap remained concrete-eligible")
	}

	atPreprocessorDepth := strings.Repeat("#if FEATURE\n", csharpMaximumConcretePreprocessorDepth) +
		strings.Repeat("#endif\n", csharpMaximumConcretePreprocessorDepth)
	if lexed := lexCSharp(atPreprocessorDepth); !lexed.concreteEligible ||
		lexed.preprocessorDepth != csharpMaximumConcretePreprocessorDepth {
		t.Fatalf("source at preprocessor cap = (%t, %d), want eligible at %d",
			lexed.concreteEligible, lexed.preprocessorDepth,
			csharpMaximumConcretePreprocessorDepth)
	}
	overPreprocessorDepth := "#if FEATURE\n" + atPreprocessorDepth + "#endif\n"
	if lexed := lexCSharp(overPreprocessorDepth); lexed.concreteEligible {
		t.Fatal("source over C# preprocessor cap remained concrete-eligible")
	}
}

func TestCSharpOpaqueSpanOverflowDisablesConcreteTreeAndKeepsTail(t *testing.T) {
	source := strings.Repeat("/**/", csharpMaximumRetainedSpans+1) +
		"\nclass SpanTail { void Recovered() { } }\n"
	lexed := lexCSharp(source)
	if !lexed.spansTruncated || lexed.concreteEligible {
		t.Fatalf("opaque span overflow = (truncated %t, eligible %t), want true, false",
			lexed.spansTruncated, lexed.concreteEligible)
	}
	lines := csharpTestLines(source)
	analysis := analyzeCSharpSource(source, len(lines))
	if analysis.tree != nil {
		t.Fatal("opaque span overflow unexpectedly retained a concrete syntax tree")
	}
	want := []string{"SpanTail", "Recovered"}
	if got := csharpDefinitionSymbols(analysis.definitions); !slices.Equal(got, want) {
		t.Fatalf("opaque span fallback definitions = %#v, want %#v", got, want)
	}
}

func TestCSharpTokenRetentionIsBoundedAndFullSourceScannerKeepsMiddleAndTail(t *testing.T) {
	attributeArguments := strings.Repeat("0,", csharpMaximumRetainedTokens/4+2048) + "0"
	source := "[A(" + attributeArguments + `)]
class MiddleDefinition
{
    void MiddleMember() { }
}
` + "[A(" + attributeArguments + `)]
class TailDefinition { void TailMember() { } }
`

	lexed := lexCSharp(source)
	if !lexed.truncated {
		t.Fatal("fixture did not cross the C# retained-token frontier")
	}
	if len(lexed.tokens) > csharpMaximumRetainedTokens {
		t.Fatalf("retained C# tokens = %d, cap %d",
			len(lexed.tokens), csharpMaximumRetainedTokens)
	}

	definitions := newCSharpLanguage().sourceDefinitions(csharpTestLines(source))
	for _, want := range []string{
		"MiddleDefinition", "MiddleMember", "TailDefinition", "TailMember",
	} {
		if !slices.Contains(csharpDefinitionSymbols(definitions), want) {
			t.Errorf("bounded full-source recovery lost %q: %#v", want, definitions)
		}
	}
	csharpAssertDefinitionCoordinates(t, csharpTestLines(source), definitions)
}

func TestCSharpDiscardedTokenGapIsHardDeclarationAndScopeBoundary(t *testing.T) {
	headLimit := (csharpMaximumRetainedTokens - 1) / 2
	tailLimit := csharpMaximumRetainedTokens - headLimit - 1
	source := strings.Repeat(";", headLimit-1) + "class" +
		strings.Repeat(";", 64) + "Phantom { Target(); }" +
		strings.Repeat(";", tailLimit-7)

	lexed := lexCSharp(source)
	if !lexed.truncated || len(lexed.tokens) != csharpMaximumRetainedTokens {
		t.Fatalf("C# retention = (%t, %d), want truncated cap %d",
			lexed.truncated, len(lexed.tokens), csharpMaximumRetainedTokens)
	}
	definitions := newCSharpLanguage().sourceDefinitions([]string{source})
	for _, phantom := range []string{"Phantom", "Target"} {
		if slices.Contains(csharpDefinitionSymbols(definitions), phantom) {
			t.Errorf("discarded middle joined unrelated tokens into %q: %#v",
				phantom, definitions)
		}
	}
}

func TestCSharpStreamingImportsRecoverDiscardedMiddleWithoutNestedPhantoms(t *testing.T) {
	insideMethod := "class Host { void Work() {\n" +
		strings.Repeat("value++;\n", csharpMaximumRetainedTokens/3+1024) +
		"using NestedAlias = Fake.Dependency;\n" +
		"} }\n"
	if lexed := lexCSharp(insideMethod); !lexed.truncated {
		t.Fatal("nested-using fixture did not truncate retained tokens")
	}
	backend := newCSharpLanguage()
	if start, end, ok := backend.importRange(csharpTestLines(insideMethod)); ok {
		t.Fatalf("nested malformed using became import %d-%d, true", start, end)
	}

	prefix := "#region " +
		strings.Repeat("filler ", csharpMaximumRetainedTokens/2+1024) +
		"\n#endregion\n"
	source := `global using Head.Dependency;
` + prefix + `global using Middle.Dependency;
` + prefix + `global using Tail.Dependency;
class Tail { }
`
	lexed := lexCSharp(source)
	if !lexed.truncated {
		t.Fatal("middle-import fixture did not truncate retained tokens")
	}
	start, end, ok := backend.importRange(csharpTestLines(source))
	if !ok || start != 1 || end != strings.Count(source, "\n")-1 {
		t.Fatalf("streamed import range = %d-%d, %v; want first through tail using", start, end, ok)
	}
}

func TestCSharpLargeOpaqueLiteralsAndDirectivesDoNotInventDefinitions(t *testing.T) {
	const repetitions = 8 << 10
	source := `class Opaque
{
    string raw = """
` + strings.Repeat(`class Fake { void Hidden() {} } ${{{{ #if FAKE
`, repetitions) + `
""";
}
` + strings.Repeat("#if FEATURE\n", csharpMaximumConcretePreprocessorDepth+1) +
		"class BranchTail { }\n" +
		strings.Repeat("#endif\n", csharpMaximumConcretePreprocessorDepth+1) +
		"class FinalTail { }\n"

	definitions := newCSharpLanguage().sourceDefinitions(csharpTestLines(source))
	symbols := csharpDefinitionSymbols(definitions)
	for _, want := range []string{"Opaque", "raw", "BranchTail", "FinalTail"} {
		if !slices.Contains(symbols, want) {
			t.Errorf("large opaque fixture lost %q: %#v", want, symbols)
		}
	}
	for _, phantom := range []string{"Fake", "Hidden"} {
		if slices.Contains(symbols, phantom) {
			t.Errorf("large raw literal promoted %q: %#v", phantom, symbols)
		}
	}
}

func TestCSharpTinyLexDoesNotEagerlyAllocateRetentionTail(t *testing.T) {
	result := testing.Benchmark(func(b *testing.B) {
		b.Helper()
		for range b.N {
			_ = lexCSharp("class Tiny { int value; }")
		}
	})
	if bytes := result.AllocedBytesPerOp(); bytes > 1<<20 {
		t.Fatalf("tiny C# lexical analysis allocated %d bytes/op, want <= 1MiB", bytes)
	}
}

func TestCSharpPreparedBackendRefreshesMutatedInputAndIsConcurrent(t *testing.T) {
	first := csharpTestLines("class First { void Work() { Target(); } }")
	second := csharpTestLines("class Second { void Other() { } }")
	prepared := prepareLanguageBackend(newCSharpLanguage(), first)

	if got, want := csharpDefinitionSymbols(prepared.sourceDefinitions(first)),
		[]string{"First", "Work"}; !slices.Equal(got, want) {
		t.Fatalf("prepared first definitions = %#v, want %#v", got, want)
	}
	if got, want := csharpDefinitionSymbols(prepared.sourceDefinitions(second)),
		[]string{"Second", "Other"}; !slices.Equal(got, want) {
		t.Fatalf("prepared stale-input definitions = %#v, want %#v", got, want)
	}

	first[0] = "class Mutated { void Changed() { } }"
	if got, want := csharpDefinitionSymbols(prepared.sourceDefinitions(first)),
		[]string{"Mutated", "Changed"}; !slices.Equal(got, want) {
		t.Fatalf("prepared mutated-input definitions = %#v, want %#v", got, want)
	}

	stableLines := csharpTestLines(`namespace Concurrent;
class Stable
{
    void Work()
    {
        if (Ready()) Target();
    }
}`)
	stable := prepareLanguageBackend(newCSharpLanguage(), stableLines)
	want := csharpDefinitionSymbols(stable.sourceDefinitions(stableLines))
	const workers = 32
	var wait sync.WaitGroup
	errors := make(chan string, workers)
	for worker := range workers {
		wait.Add(1)
		go func(worker int) {
			defer wait.Done()
			for iteration := range 32 {
				got := csharpDefinitionSymbols(stable.sourceDefinitions(stableLines))
				if !reflect.DeepEqual(got, want) {
					errors <- fmt.Sprintf("worker %d iteration %d definitions %#v, want %#v",
						worker, iteration, got, want)
					return
				}
				_, _, _ = stable.importRange(stableLines)
				_ = stable.searchLines(stableLines, true, true)
				_, _ = stable.enclosingScope(stableLines, 6)
			}
		}(worker)
	}
	wait.Wait()
	close(errors)
	for failure := range errors {
		t.Error(failure)
	}
}

func FuzzCSharpBackendMaintainsCoordinateContracts(f *testing.F) {
	for _, source := range []string{
		"namespace N; record R(int Value);\n",
		"class C { string P { get; init; } void M() { Target(); } }\n",
		"public static class E { extension(string s) { public bool Empty => s.Length == 0; } }\n",
		"class C { string s = $$\"\"\"literal {x} {{Target()}}\"\"\"; }\n",
		"#if X\nclass First {}\n#else\nclass Second {}\n#endif\n",
		"class Broken { void M(\nclass Recovered {}\n",
		"global using System.Collections.Generic;\nclass C {}\n",
		string([]byte{0xff, 0xfe, 0x00, '{', '}', '\n'}),
	} {
		f.Add(source)
	}

	f.Fuzz(func(t *testing.T, source string) {
		if len(source) > 64<<10 {
			t.Skip()
		}
		lines := csharpTestLines(source)
		backend := prepareLanguageBackend(newCSharpLanguage(), lines)
		definitions := backend.sourceDefinitions(lines)
		csharpAssertDefinitionCoordinates(t, lines, definitions)
		_, _, _ = backend.importRange(lines)
		for _, options := range [][2]bool{
			{false, false}, {true, false}, {false, true}, {true, true},
		} {
			searchable := backend.searchLines(lines, options[0], options[1])
			if len(searchable) != len(lines) {
				t.Fatalf("search lines = %d, want %d", len(searchable), len(lines))
			}
			for lineNo, line := range searchable {
				if len(line) != len(lines[lineNo]) {
					t.Fatalf("search masking changed line %d byte width from %d to %d",
						lineNo+1, len(lines[lineNo]), len(line))
				}
			}
		}
		for lineNo := 1; lineNo <= len(lines); lineNo++ {
			start, end := backend.enclosingScope(lines, lineNo)
			if start < 1 || start > lineNo || end < lineNo || end > len(lines) {
				t.Fatalf("invalid scope for line %d: %d-%d of %d", lineNo, start, end, len(lines))
			}
		}
	})
}
