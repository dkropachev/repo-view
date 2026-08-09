package csharpgrammar

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	treesitter "github.com/dcosson/treesitter-go"
	treesitterparser "github.com/dcosson/treesitter-go/parser"
)

func TestLanguageMetadataAndScannerFactory(t *testing.T) {
	language := Language()
	if language == nil {
		t.Fatal("Language returned nil")
	}
	if language.Version != 15 {
		t.Fatalf("language ABI = %d, want 15", language.Version)
	}
	if language.ExternalTokenCount != csharpExternalTokenCount {
		t.Fatalf(
			"external tokens = %d, want %d",
			language.ExternalTokenCount,
			csharpExternalTokenCount,
		)
	}
	if language.NewExternalScanner == nil || language.NewExternalScanner() == nil {
		t.Fatal("language has no external scanner factory")
	}
	if Language() != language {
		t.Fatal("Language did not reuse its immutable generated tables")
	}
}

func TestCSharpGrammarParsesModernScannerSyntax(t *testing.T) {
	const source = `using System;
using System.Collections.Generic;
using System.Linq;

namespace Navigation;

public static class Samples
{
    public static string Render(int value)
    {
        var regular = $"value={value,4:000}";
        var verbatim = $@"C:\temp\{value}";
        var raw = """raw "value" text""";
        var rawInterpolation = $$"""value={{value}} literal={brace}""";
        Func<int, int> lambda = (ref item) => item;
        Func<int, int> verbatimLambda = (ref @value) => @value;
        Func<int, int> escapedLambda = (ref \u0076alue) => \u0076alue;
        Func<int, int> combiningLambda = (ref valué) => valué;
        return regular + verbatim + raw + rawInterpolation;
    }
}

public static class Extensions
{
    extension<T>(IEnumerable<T> source)
    {
        public bool IsEmpty => !source.Any();
    }
}
`

	tree := parseCSharpTestSource(t, source)
	found := make(map[string]bool)
	csharpWalkTestTree(t, tree.RootNode(), func(node treesitter.Node) {
		if node.IsMissing() || node.Symbol() == treesitter.SymbolError {
			t.Fatalf("valid C# produced recovery node %q at %d", node.Type(), node.StartByte())
		}
		found[node.Type()] = true
	})
	for _, kind := range []string{
		"file_scoped_namespace_declaration",
		"interpolated_string_expression",
		"raw_string_literal",
		"lambda_expression",
		"extension_declaration",
	} {
		if !found[kind] {
			t.Errorf("valid C# tree is missing %q", kind)
		}
	}
}

func TestCSharpGrammarParsesEscapedInterpolationBraces(t *testing.T) {
	for _, test := range []struct {
		name               string
		source             string
		wantInterpolations int
	}{
		{"regular escaped pair", `var value = $"{{escaped}}";`, 0},
		{"regular escaped pair before interpolation", `var value = $"{{{regularValue}}}";`, 1},
		{"verbatim escaped pair", `var value = $@"{{escaped}}";`, 0},
		{"verbatim escaped pair before interpolation", `var value = $@"{{{verbatimValue}}}";`, 1},
	} {
		t.Run(test.name, func(t *testing.T) {
			tree := parseCSharpTestSource(t, test.source)
			interpolations := 0
			csharpWalkTestTree(t, tree.RootNode(), func(node treesitter.Node) {
				if node.IsMissing() || node.Symbol() == treesitter.SymbolError {
					t.Fatalf("valid C# produced recovery node %q at %d",
						node.Type(), node.StartByte())
				}
				if node.Type() != "interpolation" {
					return
				}
				interpolations++
				if node.ChildCount() != 3 ||
					node.Child(0).Type() != "interpolation_brace" ||
					node.Child(1).Type() != "identifier" ||
					node.Child(2).Type() != "interpolation_brace" {
					t.Errorf("interpolation has malformed structure: %s", node.String())
				}
			})
			if interpolations != test.wantInterpolations {
				t.Errorf("interpolation nodes = %d, want %d; tree: %s",
					interpolations, test.wantInterpolations, tree.RootNode().String())
			}
		})
	}
}

func TestCSharpScannerStateRoundTripAndBounds(t *testing.T) {
	scanner := &csharpScanner{
		quoteCount: 7,
		interpolations: []csharpInterpolation{
			{
				dollarCount: 1,
				quoteCount:  1,
				stringType:  csharpStringRegular,
			},
			{
				dollarCount:    2,
				openBraceCount: 2,
				quoteCount:     3,
				stringType:     csharpStringRaw,
			},
		},
	}
	buffer := make([]byte, treesitter.TreeSitterSerializationBufferSize)
	size := scanner.Serialize(buffer)
	if size != 10 {
		t.Fatalf("serialized bytes = %d, want 10", size)
	}
	var restored csharpScanner
	restored.Deserialize(buffer[:size])
	if restored.quoteCount != scanner.quoteCount ||
		!slices.Equal(restored.interpolations, scanner.interpolations) {
		t.Fatalf("restored scanner = %#v, want %#v", restored, *scanner)
	}

	restored.Deserialize([]byte{1})
	if restored.quoteCount != 0 || len(restored.interpolations) != 0 {
		t.Fatalf("malformed state was retained: %#v", restored)
	}
	scanner.interpolations = make(
		[]csharpInterpolation,
		csharpMaximumInterpolationDepth+1,
	)
	if size := scanner.Serialize(buffer); size != 0 {
		t.Fatalf("over-depth state serialized to %d bytes", size)
	}
}

func TestCSharpScannerRejectsUnboundedRunsAndLambdaLookahead(t *testing.T) {
	if csharpScanTestToken(
		t,
		strings.Repeat("\"", csharpMaximumDelimiterRun+1),
		csharpRawStringStart,
	) {
		t.Fatal("overlong raw-string quote run was accepted")
	}
	if !csharpScanTestToken(t, "(ref value) => value", csharpLambdaParenOpen) {
		t.Fatal("C# 14 modified simple lambda was rejected")
	}
	for _, source := range []string{
		"(ref @value) => @value",
		`(ref \u0076alue) => \u0076alue`,
		"(ref valué) => valué",
		"(ref Δvalue) => Δvalue",
	} {
		if !csharpScanTestToken(t, source, csharpLambdaParenOpen) {
			t.Errorf("modified lambda identifier was rejected: %q", source)
		}
	}
	if csharpScanTestToken(t, "(value) => value", csharpLambdaParenOpen) {
		t.Fatal("ordinary lambda incorrectly used the modified-lambda token")
	}
	longIdentifier := "(ref " +
		strings.Repeat("a", csharpMaximumLambdaScanAdvances+1) + ") => value"
	if csharpScanTestToken(t, longIdentifier, csharpLambdaParenOpen) {
		t.Fatal("lambda beyond the scanner lookahead cap was accepted")
	}
}

func parseCSharpTestSource(t *testing.T, source string) *treesitter.Tree {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	parser := treesitterparser.NewParser()
	parser.SetLanguage(Language())
	tree := parser.ParseString(ctx, []byte(source))
	if tree == nil {
		t.Fatal("C# parser returned nil")
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("C# parser exceeded its test deadline: %v", err)
	}
	root := tree.RootNode()
	if root.IsNull() || root.Type() != "compilation_unit" ||
		root.EndByte() != uint32(len(source)) {
		t.Fatalf(
			"C# root = %q %d-%d, want compilation_unit 0-%d",
			root.Type(),
			root.StartByte(),
			root.EndByte(),
			len(source),
		)
	}
	return tree
}

func csharpWalkTestTree(
	t *testing.T,
	root treesitter.Node,
	visit func(treesitter.Node),
) {
	t.Helper()
	stack := []treesitter.Node{root}
	operations := 0
	for len(stack) > 0 {
		operations++
		if operations > 1_000_000 {
			t.Fatal("C# test tree exceeded traversal operation cap")
		}
		last := len(stack) - 1
		node := stack[last]
		stack = stack[:last]
		visit(node)
		for index := int(node.ChildCount()) - 1; index >= 0; index-- {
			stack = append(stack, node.Child(index))
		}
	}
}

func csharpScanTestToken(t *testing.T, source string, symbol int) bool {
	t.Helper()
	lexer := treesitter.NewLexer()
	lexer.SetInput(treesitter.NewStringInput([]byte(source)))
	lexer.Start(treesitter.Length{})
	validSymbols := make([]bool, csharpExternalTokenCount)
	validSymbols[symbol] = true
	return (&csharpScanner{}).Scan(lexer, validSymbols)
}
