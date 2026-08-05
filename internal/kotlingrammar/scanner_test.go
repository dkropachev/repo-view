package kotlingrammar

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	treesitter "github.com/dcosson/treesitter-go"
	treesitterparser "github.com/dcosson/treesitter-go/parser"
)

func TestKotlinLanguageMetadataAndScannerFactory(t *testing.T) {
	language := Language()
	if language == nil {
		t.Fatal("Language returned nil")
	}
	if language.Version != 14 {
		t.Fatalf("language ABI = %d, want 14", language.Version)
	}
	if language.ExternalTokenCount != kotlinExternalTokenCount {
		t.Fatalf(
			"external tokens = %d, want %d",
			language.ExternalTokenCount,
			kotlinExternalTokenCount,
		)
	}
	if language.NewExternalScanner == nil {
		t.Fatal("language has no external scanner factory")
	}
	first, second := language.NewExternalScanner(), language.NewExternalScanner()
	if first == nil || second == nil || first == second {
		t.Fatal("external scanner factory did not return fresh scanners")
	}
	if Language() != language {
		t.Fatal("Language did not reuse its immutable generated tables")
	}
}

func TestKotlinScannerStateRoundTripAndValidation(t *testing.T) {
	scanner := &kotlinScanner{strings: []kotlinStringFrame{
		{prefixLength: 1},
		{triple: true, prefixLength: 2},
		{triple: true, prefixLength: kotlinMaximumInterpolationPrefix},
	}}
	buffer := make([]byte, treesitter.TreeSitterSerializationBufferSize)
	size := scanner.Serialize(buffer)
	if size != 6 {
		t.Fatalf("serialized bytes = %d, want 6", size)
	}
	wantBytes := []byte{
		kotlinRegularStringDelimiter, 1,
		kotlinTripleStringDelimiter, 2,
		kotlinTripleStringDelimiter, kotlinMaximumInterpolationPrefix,
	}
	if !slices.Equal(buffer[:size], wantBytes) {
		t.Fatalf("serialized state = %v, want %v", buffer[:size], wantBytes)
	}

	var restored kotlinScanner
	restored.Deserialize(buffer[:size])
	if !slices.Equal(restored.strings, scanner.strings) {
		t.Fatalf("restored frames = %#v, want %#v", restored.strings, scanner.strings)
	}

	for name, data := range map[string][]byte{
		"odd length":        {kotlinRegularStringDelimiter},
		"invalid delimiter": {'x', 1},
		"zero prefix":       {kotlinRegularStringDelimiter, 0},
		"over depth": slices.Repeat(
			[]byte{kotlinRegularStringDelimiter, 1},
			kotlinMaximumStringDepth+1,
		),
	} {
		t.Run(name, func(t *testing.T) {
			restored.strings = append(restored.strings[:0], kotlinStringFrame{
				prefixLength: 1,
			})
			restored.Deserialize(data)
			if len(restored.strings) != 0 {
				t.Fatalf("malformed state was retained: %#v", restored.strings)
			}
		})
	}

	if size := scanner.Serialize(make([]byte, 5)); size != 0 {
		t.Fatalf("state serialized into undersized buffer: %d bytes", size)
	}
	scanner.strings = make([]kotlinStringFrame, kotlinMaximumStringDepth+1)
	if size := scanner.Serialize(buffer); size != 0 {
		t.Fatalf("over-depth state serialized to %d bytes", size)
	}
	scanner.strings = []kotlinStringFrame{{}}
	if size := scanner.Serialize(buffer); size != 0 {
		t.Fatalf("zero-prefix frame serialized to %d bytes", size)
	}
}

func TestKotlinScannerStringTokens(t *testing.T) {
	regular := &kotlinScanner{}
	result := kotlinScanTestBytes(t, regular, []byte(`"value"`), kotlinStringStart)
	result.require(t, true, kotlinStringStart, 1)
	if !slices.Equal(regular.strings, []kotlinStringFrame{{prefixLength: 1}}) {
		t.Fatalf("regular string frame = %#v", regular.strings)
	}

	multiDollar := &kotlinScanner{}
	result = kotlinScanTestBytes(
		t,
		multiDollar,
		[]byte(`$$"""value"""`),
		kotlinStringStart,
	)
	result.require(t, true, kotlinStringStart, 5)
	if !slices.Equal(multiDollar.strings, []kotlinStringFrame{{
		triple:       true,
		prefixLength: 2,
	}}) {
		t.Fatalf("multi-dollar frame = %#v", multiDollar.strings)
	}

	for name, test := range map[string]struct {
		frame   kotlinStringFrame
		source  []byte
		valid   []int
		ok      bool
		symbol  int
		endByte uint32
		depth   int
	}{
		"identifier interpolation": {
			frame:   kotlinStringFrame{prefixLength: 1},
			source:  []byte(`$name`),
			valid:   []int{kotlinInterpolationIdentifierStart},
			ok:      true,
			symbol:  kotlinInterpolationIdentifierStart,
			endByte: 1,
			depth:   1,
		},
		"expression interpolation": {
			frame:   kotlinStringFrame{prefixLength: 1},
			source:  []byte(`${value}`),
			valid:   []int{kotlinInterpolationExpressionStart},
			ok:      true,
			symbol:  kotlinInterpolationExpressionStart,
			endByte: 2,
			depth:   1,
		},
		"multi-dollar identifier": {
			frame:   kotlinStringFrame{triple: true, prefixLength: 2},
			source:  []byte(`$$name`),
			valid:   []int{kotlinInterpolationIdentifierStart},
			ok:      true,
			symbol:  kotlinInterpolationIdentifierStart,
			endByte: 2,
			depth:   1,
		},
		"insufficient dollars are content": {
			frame:   kotlinStringFrame{triple: true, prefixLength: 2},
			source:  []byte(`$name`),
			valid:   []int{kotlinStringContent},
			ok:      true,
			symbol:  kotlinStringContent,
			endByte: 1,
			depth:   1,
		},
		"excess dollars are content": {
			frame:   kotlinStringFrame{prefixLength: 1},
			source:  []byte(`$$name`),
			valid:   []int{kotlinStringContent},
			ok:      true,
			symbol:  kotlinStringContent,
			endByte: 1,
			depth:   1,
		},
		"empty interpolation is rejected": {
			frame:  kotlinStringFrame{prefixLength: 1},
			source: []byte(`${}`),
			valid:  []int{kotlinInterpolationExpressionStart},
			depth:  1,
		},
		"regular end": {
			frame:   kotlinStringFrame{prefixLength: 1},
			source:  []byte(`"`),
			valid:   []int{kotlinStringContent},
			ok:      true,
			symbol:  kotlinStringEnd,
			endByte: 1,
		},
		"triple end": {
			frame:   kotlinStringFrame{triple: true, prefixLength: 1},
			source:  []byte(`"""`),
			valid:   []int{kotlinStringContent},
			ok:      true,
			symbol:  kotlinStringEnd,
			endByte: 3,
		},
		"triple content before end": {
			frame:   kotlinStringFrame{triple: true, prefixLength: 1},
			source:  []byte(`text"""`),
			valid:   []int{kotlinStringContent},
			ok:      true,
			symbol:  kotlinStringContent,
			endByte: 4,
			depth:   1,
		},
		"escaped dollar at end": {
			frame:   kotlinStringFrame{prefixLength: 1},
			source:  []byte(`\$"`),
			valid:   []int{kotlinStringContent},
			ok:      true,
			symbol:  kotlinStringEnd,
			endByte: 3,
		},
	} {
		t.Run(name, func(t *testing.T) {
			scanner := &kotlinScanner{strings: []kotlinStringFrame{test.frame}}
			result := kotlinScanTestBytes(t, scanner, test.source, test.valid...)
			result.require(t, test.ok, test.symbol, test.endByte)
			if len(scanner.strings) != test.depth {
				t.Fatalf("string depth = %d, want %d", len(scanner.strings), test.depth)
			}
		})
	}

	nulScanner := &kotlinScanner{strings: []kotlinStringFrame{{prefixLength: 1}}}
	result = kotlinScanTestBytes(
		t,
		nulScanner,
		[]byte{'a', 0, 'b', '"'},
		kotlinStringContent,
	)
	result.require(t, true, kotlinStringContent, 3)
}

func TestKotlinScannerNestedCommentsNULAndBounds(t *testing.T) {
	for name, source := range map[string][]byte{
		"nested":       []byte(`/* outer /* inner */ outer */`),
		"unterminated": []byte(`/* unterminated`),
		"literal NUL":  {'/', '*', 0, '*', '/'},
	} {
		t.Run(name, func(t *testing.T) {
			result := kotlinScanTestBytes(
				t,
				&kotlinScanner{},
				source,
				kotlinMultilineComment,
			)
			result.require(t, true, kotlinMultilineComment, uint32(len(source)))
		})
	}

	overDepth := strings.Repeat("/*", kotlinMaximumProbeDepth+1) +
		strings.Repeat("*/", kotlinMaximumProbeDepth+1)
	result := kotlinScanTestBytes(
		t,
		&kotlinScanner{},
		[]byte(overDepth),
		kotlinMultilineComment,
	)
	if result.ok {
		t.Fatal("over-depth multiline comment was accepted")
	}

	longComment := "/*" + strings.Repeat("x", kotlinMaximumExternalScanAdvances) + "*/"
	result = kotlinScanTestBytes(
		t,
		&kotlinScanner{},
		[]byte(longComment),
		kotlinMultilineComment,
	)
	if result.ok {
		t.Fatal("comment beyond the scanner advance cap was accepted")
	}

	longString := &kotlinScanner{strings: []kotlinStringFrame{{prefixLength: 1}}}
	result = kotlinScanTestBytes(
		t,
		longString,
		[]byte(strings.Repeat("x", kotlinMaximumExternalScanAdvances+1)+`"`),
		kotlinStringContent,
	)
	if result.ok || len(longString.strings) != 1 {
		t.Fatalf("overlong string result = %#v, depth = %d", result, len(longString.strings))
	}

	fullStack := &kotlinScanner{
		strings: slices.Repeat(
			[]kotlinStringFrame{{prefixLength: 1}},
			kotlinMaximumStringDepth,
		),
	}
	result = kotlinScanTestBytes(t, fullStack, []byte(`"`), kotlinStringStart)
	if result.ok || len(fullStack.strings) != kotlinMaximumStringDepth {
		t.Fatalf("full scanner stack accepted another frame: %#v", result)
	}
}

func TestKotlinScannerAutomaticSemicolonContexts(t *testing.T) {
	for name, test := range map[string]struct {
		source  string
		valid   []int
		ok      bool
		symbol  int
		endByte uint32
	}{
		"EOF": {
			valid:  []int{kotlinAutomaticSemicolon},
			ok:     true,
			symbol: kotlinAutomaticSemicolon,
		},
		"explicit semicolon": {
			source:  ";",
			valid:   []int{kotlinAutomaticSemicolon},
			ok:      true,
			symbol:  kotlinAutomaticSemicolon,
			endByte: 1,
		},
		"member continuation": {
			source: "\n.next",
			valid:  []int{kotlinAutomaticSemicolon},
		},
		"if else continuation": {
			source: "\nelse value",
			valid:  []int{kotlinAutomaticSemicolon},
		},
		"when else entry": {
			source: "\nelse -> value",
			valid:  []int{kotlinAutomaticSemicolon},
			ok:     true,
			symbol: kotlinAutomaticSemicolon,
		},
		"catch continuation": {
			source: "\ncatch (error: Throwable)",
			valid:  []int{kotlinAutomaticSemicolon},
		},
		"finally continuation": {
			source: "\nfinally {}",
			valid:  []int{kotlinAutomaticSemicolon},
		},
		"as continuation": {
			source: "\nas String",
			valid:  []int{kotlinAutomaticSemicolon},
		},
		"where continuation": {
			source: "\nwhere T : Any",
			valid:  []int{kotlinAutomaticSemicolon},
		},
		"prefix plus starts statement": {
			source: "\n+value",
			valid:  []int{kotlinAutomaticSemicolon},
			ok:     true,
			symbol: kotlinAutomaticSemicolon,
		},
		"not equal continuation": {
			source: "\n!= value",
			valid:  []int{kotlinAutomaticSemicolon},
		},
		"unary not starts statement": {
			source: "\n!value",
			valid:  []int{kotlinAutomaticSemicolon},
			ok:     true,
			symbol: kotlinAutomaticSemicolon,
		},
		"by is identifier without delegation context": {
			source: "\nby",
			valid:  []int{kotlinAutomaticSemicolon},
			ok:     true,
			symbol: kotlinAutomaticSemicolon,
		},
		"by continues delegation": {
			source: "\nby delegate",
			valid:  []int{kotlinAutomaticSemicolon, kotlinByDelegationHint},
		},
		"primary constructor keyword": {
			source:  "\nconstructor(value: Int)",
			valid:   []int{kotlinAutomaticSemicolon, kotlinPrimaryConstructorKeyword},
			ok:      true,
			symbol:  kotlinPrimaryConstructorKeyword,
			endByte: 12,
		},
		"primary constructor modifier": {
			source: "\npublic constructor(value: Int)",
			valid:  []int{kotlinAutomaticSemicolon, kotlinPrimaryConstructorKeyword},
		},
		"primary constructor annotation": {
			source: "\n@Inject(\"x\") public constructor(value: Int)",
			valid:  []int{kotlinAutomaticSemicolon, kotlinPrimaryConstructorKeyword},
		},
		"comment before continuation": {
			source: "\n// comment\nelse value",
			valid:  []int{kotlinAutomaticSemicolon},
		},
		"block comment token before continuation": {
			source:  "\n/* comment */.next",
			valid:   []int{kotlinAutomaticSemicolon, kotlinMultilineComment},
			ok:      true,
			symbol:  kotlinMultilineComment,
			endByte: 14,
		},
		"unterminated block comment at EOF": {
			source:  "\n/* comment",
			valid:   []int{kotlinAutomaticSemicolon, kotlinMultilineComment},
			ok:      true,
			symbol:  kotlinMultilineComment,
			endByte: 11,
		},
	} {
		t.Run(name, func(t *testing.T) {
			result := kotlinScanTestBytes(
				t,
				&kotlinScanner{},
				[]byte(test.source),
				test.valid...,
			)
			result.require(t, test.ok, test.symbol, test.endByte)
		})
	}

	result := kotlinScanTestBytes(
		t,
		&kotlinScanner{},
		[]byte(" constructor(value: Int)"),
		kotlinPrimaryConstructorKeyword,
	)
	result.require(t, true, kotlinPrimaryConstructorKeyword, 12)

	result = kotlinScanTestBytes(
		t,
		&kotlinScanner{},
		[]byte("."),
		kotlinImportDot,
	)
	result.require(t, true, kotlinImportDot, 1)
	result = kotlinScanTestBytes(
		t,
		&kotlinScanner{},
		[]byte(".\nimport next"),
		kotlinImportDot,
	)
	result.require(t, true, kotlinAutomaticSemicolon, 0)

	result = kotlinScanTestBytes(
		t,
		&kotlinScanner{},
		[]byte("by"),
		kotlinByDelegationHint,
	)
	if result.ok {
		t.Fatal("BY_DELEGATION_HINT was emitted instead of used as context only")
	}

	overlongProbe := "\n" + strings.Repeat("@A ", kotlinMaximumProbeDepth+1) +
		"constructor(value: Int)"
	result = kotlinScanTestBytes(
		t,
		&kotlinScanner{},
		[]byte(overlongProbe),
		kotlinAutomaticSemicolon,
		kotlinPrimaryConstructorKeyword,
	)
	if result.ok {
		t.Fatal("annotation chain beyond the probe cap was accepted")
	}
}

func TestKotlinGrammarParsesModernScannerSyntax(t *testing.T) {
	const source = `#!/usr/bin/env kotlin
@file:Suppress("unused")
package demo

import kotlin.collections.*

annotation class Inject

class Box
@Inject("primary")
public constructor(val value: String) {
    val ordinary = "value=$value"
    val raw = $$"""literal $value identifier $$value expression $${value.length}"""
    val nested = 1 /* outer /* inner */ outer */ + 2

    fun render(): String = when (value) {
        "" -> ordinary
        else -> raw
    }
}
`

	tree := parseKotlinTestSource(t, source)
	found := make(map[string]bool)
	kotlinWalkTestTree(t, tree.RootNode(), func(node treesitter.Node) {
		if node.IsMissing() || node.Symbol() == treesitter.SymbolError {
			t.Fatalf("valid Kotlin produced recovery node %q at %d", node.Type(), node.StartByte())
		}
		found[node.Type()] = true
	})
	for _, kind := range []string{
		"class_declaration",
		"primary_constructor",
		"string_literal",
		"multiline_comment",
	} {
		if !found[kind] {
			t.Errorf("valid Kotlin tree is missing %q", kind)
		}
	}
}

func TestKotlinScannerRejectsInvalidCalls(t *testing.T) {
	lexer := treesitter.NewLexer()
	lexer.SetInput(treesitter.NewStringInput([]byte(`"value"`)))
	lexer.Start(treesitter.Length{})
	if (&kotlinScanner{}).Scan(lexer, make([]bool, kotlinExternalTokenCount-1)) {
		t.Fatal("scanner accepted a truncated valid-symbol array")
	}
	if (&kotlinScanner{}).Scan(nil, make([]bool, kotlinExternalTokenCount)) {
		t.Fatal("scanner accepted a nil lexer")
	}
	var scanner *kotlinScanner
	if scanner.Scan(lexer, make([]bool, kotlinExternalTokenCount)) {
		t.Fatal("nil scanner accepted input")
	}
	if size := scanner.Serialize(make([]byte, 8)); size != 0 {
		t.Fatalf("nil scanner serialized %d bytes", size)
	}
	scanner.Deserialize([]byte{1, 2})
}

type kotlinScanTestResult struct {
	ok      bool
	symbol  int
	endByte uint32
}

func (result kotlinScanTestResult) require(
	t *testing.T,
	wantOK bool,
	wantSymbol int,
	wantEndByte uint32,
) {
	t.Helper()
	if result.ok != wantOK {
		t.Fatalf("scan success = %t, want %t (result %#v)", result.ok, wantOK, result)
	}
	if !wantOK {
		return
	}
	if result.symbol != wantSymbol || result.endByte != wantEndByte {
		t.Fatalf(
			"scan result = symbol %d end %d, want symbol %d end %d",
			result.symbol,
			result.endByte,
			wantSymbol,
			wantEndByte,
		)
	}
}

func kotlinScanTestBytes(
	t *testing.T,
	scanner *kotlinScanner,
	source []byte,
	valid ...int,
) kotlinScanTestResult {
	t.Helper()
	lexer := treesitter.NewLexer()
	lexer.SetInput(treesitter.NewStringInput(source))
	lexer.Start(treesitter.Length{})
	validSymbols := make([]bool, kotlinExternalTokenCount)
	for _, symbol := range valid {
		if symbol < 0 || symbol >= len(validSymbols) {
			t.Fatalf("invalid external test symbol %d", symbol)
		}
		validSymbols[symbol] = true
	}
	ok := scanner.Scan(lexer, validSymbols)
	return kotlinScanTestResult{
		ok:      ok,
		symbol:  int(lexer.ResultSymbol),
		endByte: lexer.TokenEndPosition.Bytes,
	}
}

func parseKotlinTestSource(t *testing.T, source string) *treesitter.Tree {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	parser := treesitterparser.NewParser()
	parser.SetLanguage(Language())
	tree := parser.ParseString(ctx, []byte(source))
	if tree == nil {
		t.Fatal("Kotlin parser returned nil")
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("Kotlin parser exceeded its test deadline: %v", err)
	}
	root := tree.RootNode()
	if root.IsNull() || root.Type() != "source_file" ||
		root.EndByte() != uint32(len(source)) {
		t.Fatalf(
			"Kotlin root = %q %d-%d, want source_file 0-%d",
			root.Type(),
			root.StartByte(),
			root.EndByte(),
			len(source),
		)
	}
	return tree
}

func kotlinWalkTestTree(
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
			t.Fatal("Kotlin test tree exceeded traversal operation cap")
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
