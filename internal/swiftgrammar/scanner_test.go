package swiftgrammar

import (
	"context"
	"encoding/binary"
	"strings"
	"testing"
	"time"

	treesitter "github.com/dcosson/treesitter-go"
	treesitterparser "github.com/dcosson/treesitter-go/parser"
)

func TestSwiftLanguageMetadataAndScannerFactory(t *testing.T) {
	language := Language()
	if language == nil {
		t.Fatal("Language returned nil")
	}
	if language.Version != 14 {
		t.Fatalf("language ABI = %d, want 14", language.Version)
	}
	if language.StateCount != 10331 || language.ExternalTokenCount != swiftExternalTokenCount {
		t.Fatalf(
			"language states/external tokens = %d/%d, want 10331/%d",
			language.StateCount,
			language.ExternalTokenCount,
			swiftExternalTokenCount,
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

func TestSwiftScannerStateRoundTripAndValidation(t *testing.T) {
	scanner := &swiftScanner{ongoingRawStringHashCount: 0xfefe}
	buffer := make([]byte, treesitter.TreeSitterSerializationBufferSize)
	size := scanner.Serialize(buffer)
	if size != 4 {
		t.Fatalf("serialized bytes = %d, want 4", size)
	}
	if got := binary.BigEndian.Uint32(buffer[:size]); got != 0xfefe {
		t.Fatalf("serialized count = %#x, want 0xfefe", got)
	}

	var restored swiftScanner
	restored.Deserialize(buffer[:size])
	if restored.ongoingRawStringHashCount != scanner.ongoingRawStringHashCount {
		t.Fatalf(
			"restored count = %d, want %d",
			restored.ongoingRawStringHashCount,
			scanner.ongoingRawStringHashCount,
		)
	}
	for name, data := range map[string][]byte{
		"empty":      nil,
		"short":      {0, 1, 2},
		"long":       {0, 0, 0, 1, 2},
		"over limit": {0, 1, 0, 1},
	} {
		t.Run(name, func(t *testing.T) {
			restored.ongoingRawStringHashCount = 7
			restored.Deserialize(data)
			if restored.ongoingRawStringHashCount != 0 {
				t.Fatalf("malformed state retained count %d", restored.ongoingRawStringHashCount)
			}
		})
	}
	if size := scanner.Serialize(make([]byte, 3)); size != 0 {
		t.Fatalf("state serialized into short buffer: %d", size)
	}
	scanner.ongoingRawStringHashCount = swiftMaximumRawStringHashes + 1
	if size := scanner.Serialize(buffer); size != 0 {
		t.Fatalf("over-limit state serialized to %d bytes", size)
	}
}

func TestSwiftScannerNestedComments(t *testing.T) {
	source := "/* outer /* inner */ tail */x"
	result := swiftScanTest(t, &swiftScanner{}, source, swiftBlockComment)
	result.require(t, true, swiftBlockComment, uint32(len(source)-1))

	for _, malformed := range []string{
		"/* unterminated",
		strings.Repeat("/*", swiftMaximumCommentDepth+1) +
			strings.Repeat("*/", swiftMaximumCommentDepth+1),
		"/*" + strings.Repeat("x", swiftMaximumExternalScanAdvances+1) + "*/",
	} {
		if swiftScanTest(t, &swiftScanner{}, malformed, swiftBlockComment).ok {
			t.Errorf("malformed or over-limit comment was accepted: length %d", len(malformed))
		}
	}
}

func TestSwiftScannerDirectivesAndHashSymbol(t *testing.T) {
	for name, test := range map[string]struct {
		source string
		symbol int
		end    uint32
	}{
		"if":       {source: "#if DEBUG", symbol: swiftDirectiveIf, end: 3},
		"elseif":   {source: "#elseif DEBUG", symbol: swiftDirectiveElseIf, end: 7},
		"else":     {source: "#else", symbol: swiftDirectiveElse, end: 5},
		"endif":    {source: "#endif", symbol: swiftDirectiveEndIf, end: 6},
		"ifPrefix": {source: "#ifconfig", symbol: swiftDirectiveIf, end: 3},
		"macro":    {source: "#available", symbol: swiftHashSymbol, end: 1},
	} {
		t.Run(name, func(t *testing.T) {
			result := swiftScanTest(
				t,
				&swiftScanner{},
				test.source,
				swiftRawStringPart,
				test.symbol,
			)
			result.require(t, true, test.symbol, test.end)
		})
	}
	if swiftScanTest(
		t,
		&swiftScanner{},
		"##not-a-string",
		swiftRawStringPart,
	).ok {
		t.Fatal("multiple hashes without a raw string were accepted")
	}
}

func TestSwiftScannerRawStringsAndInterpolation(t *testing.T) {
	for name, source := range map[string]string{
		"single hash":   `#"value"#`,
		"multiple hash": `###"value # text"###`,
	} {
		t.Run(name, func(t *testing.T) {
			result := swiftScanTest(t, &swiftScanner{}, source, swiftRawStringPart)
			result.require(t, true, swiftRawStringEndPart, uint32(len(source)))
		})
	}

	scanner := &swiftScanner{}
	first := `##"prefix \##(value)`
	result := swiftScanTest(t, scanner, first, swiftRawStringPart)
	result.require(t, true, swiftRawStringPart, uint32(strings.Index(first, `\##(`)))
	if scanner.ongoingRawStringHashCount != 2 {
		t.Fatalf("raw interpolation hash count = %d, want 2", scanner.ongoingRawStringHashCount)
	}
	continuation := ` suffix"##`
	result = swiftScanTest(
		t,
		scanner,
		continuation,
		swiftRawStringPart,
		swiftRawStringContinuingIndicator,
	)
	result.require(t, true, swiftRawStringEndPart, uint32(len(continuation)))
	if scanner.ongoingRawStringHashCount != 0 {
		t.Fatalf("closed raw string retained hash count %d", scanner.ongoingRawStringHashCount)
	}

	for _, malformed := range []string{
		`##"value"#`,
		strings.Repeat("#", swiftMaximumRawStringHashes+1) + `"value"`,
	} {
		if swiftScanTest(t, &swiftScanner{}, malformed, swiftRawStringPart).ok {
			t.Errorf("malformed or over-limit raw string accepted: length %d", len(malformed))
		}
	}
}

func TestSwiftScannerFixedAndCustomOperators(t *testing.T) {
	tests := []struct {
		name   string
		source string
		symbol int
	}{
		{name: "arrow", source: "-> ", symbol: swiftArrowOperator},
		{name: "dot", source: ". ", symbol: swiftDotOperator},
		{name: "and", source: "&& ", symbol: swiftConjunctionOperator},
		{name: "or", source: "|| ", symbol: swiftDisjunctionOperator},
		{name: "nil coalescing", source: "?? ", symbol: swiftNilCoalescingOperator},
		{name: "equal", source: "= ", symbol: swiftEqualSign},
		{name: "equal equal", source: "== ", symbol: swiftEqualEqual},
		{name: "plus whitespace", source: "+ ", symbol: swiftPlusThenWhitespace},
		{name: "minus whitespace", source: "- ", symbol: swiftMinusThenWhitespace},
		{name: "bang", source: "! ", symbol: swiftBang},
		{name: "throws", source: "throws ", symbol: swiftThrowsKeyword},
		{name: "rethrows", source: "rethrows ", symbol: swiftRethrowsKeyword},
		{name: "default", source: "default ", symbol: swiftDefaultKeyword},
		{name: "where", source: "where ", symbol: swiftWhereKeyword},
		{name: "else", source: "else ", symbol: swiftElseKeyword},
		{name: "catch", source: "catch ", symbol: swiftCatchKeyword},
		{name: "as", source: "as ", symbol: swiftAsKeyword},
		{name: "as question", source: "as? ", symbol: swiftAsQuestion},
		{name: "as bang", source: "as! ", symbol: swiftAsBang},
		{name: "async", source: "async ", symbol: swiftAsyncKeyword},
		{name: "custom ascii", source: "<+> ", symbol: swiftCustomOperator},
		{name: "custom unicode", source: "⊕ ", symbol: swiftCustomOperator},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			result := swiftScanTest(t, &swiftScanner{}, test.source, test.symbol)
			result.require(t, true, test.symbol, uint32(len(test.source)-1))
		})
	}

	if swiftScanTest(
		t,
		&swiftScanner{},
		"! ",
		swiftBang,
		swiftFakeTryBang,
	).ok {
		t.Fatal("bang token was not suppressed by fake try-bang")
	}
	for _, reserved := range swiftReservedOperators {
		if swiftScanTest(t, &swiftScanner{}, reserved+" ", swiftCustomOperator).ok {
			t.Errorf("reserved operator %q accepted as custom", reserved)
		}
	}
	for _, identifier := range []string{
		"async_value",
		"async\u0301",
		"async😀",
		"async9",
	} {
		if result := swiftScanTest(
			t,
			&swiftScanner{},
			identifier,
			swiftAsyncKeyword,
		); result.ok {
			t.Errorf("identifier prefix %q was accepted as async keyword: %#v",
				identifier, result)
		}
	}
	longOperator := strings.Repeat("~", swiftMaximumExternalScanAdvances+1)
	if swiftScanTest(t, &swiftScanner{}, longOperator, swiftCustomOperator).ok {
		t.Fatal("custom operator beyond scanner cap was accepted")
	}
}

func TestSwiftGrammarDoesNotSplitKeywordPrefixesAtUnderscores(t *testing.T) {
	const source = `if condition {}
else_value()
do {}
catch_error()
`
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	parser := treesitterparser.NewParser()
	parser.SetLanguage(Language())
	tree := parser.ParseString(ctx, []byte(source))
	if tree == nil || ctx.Err() != nil {
		t.Fatalf("parse failed: tree=%v err=%v", tree, ctx.Err())
	}
	swiftWalkTestTree(t, tree.RootNode(), func(node treesitter.Node) {
		if node.IsMissing() || node.Symbol() == treesitter.SymbolError {
			t.Fatalf("valid Swift produced recovery node %q at %d",
				node.Type(), node.StartByte())
		}
	})
}

func TestSwiftScannerSemicolonDecisions(t *testing.T) {
	result := swiftScanTest(
		t,
		&swiftScanner{},
		";next",
		swiftImplicitSemicolon,
		swiftExplicitSemicolon,
	)
	result.require(t, true, swiftExplicitSemicolon, 1)
	result = swiftScanTest(
		t,
		&swiftScanner{},
		"\nnext",
		swiftImplicitSemicolon,
		swiftExplicitSemicolon,
	)
	result.require(t, true, swiftImplicitSemicolon, 1)

	for _, continuation := range []string{
		"\n? value",
		"\n: value",
		"\n{ value",
		"\n// comment\n&& value",
	} {
		if swiftScanTest(
			t,
			&swiftScanner{},
			continuation,
			swiftImplicitSemicolon,
			swiftExplicitSemicolon,
			swiftConjunctionOperator,
		).ok {
			t.Errorf("continuation %q emitted a semicolon", continuation)
		}
	}
	if swiftScanTest(
		t,
		&swiftScanner{},
		"\n// comment\nnext",
		swiftImplicitSemicolon,
		swiftExplicitSemicolon,
	).ok {
		t.Error("line comment after a newline emitted a semicolon")
	}
	continuation := "\n&& value"
	result = swiftScanTest(
		t,
		&swiftScanner{},
		continuation,
		swiftImplicitSemicolon,
		swiftExplicitSemicolon,
		swiftConjunctionOperator,
	)
	result.require(
		t,
		true,
		swiftConjunctionOperator,
		uint32(strings.Index(continuation, " value")),
	)
}

func TestSwiftScannerRejectsInvalidCalls(t *testing.T) {
	valid := make([]bool, swiftExternalTokenCount)
	if (&swiftScanner{}).Scan(nil, valid) {
		t.Fatal("nil lexer was accepted")
	}
	lexer := swiftTestLexer("")
	var nilScanner *swiftScanner
	if nilScanner.Scan(lexer, valid) {
		t.Fatal("nil scanner was accepted")
	}
	if (&swiftScanner{}).Scan(lexer, valid[:len(valid)-1]) {
		t.Fatal("short valid-symbol vector was accepted")
	}
}

func TestSwiftGrammarParsesScannerSyntax(t *testing.T) {
	const source = `#if DEBUG
struct Box<T> {
    let value: T
    func run() async throws -> T { value }
}
#else
actor Box<T> {
    let value: T
}
#endif

let raw = ##"value \##(1)"##
infix operator <+>
`
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	parser := treesitterparser.NewParser()
	parser.SetLanguage(Language())
	tree := parser.ParseString(ctx, []byte(source))
	if tree == nil {
		t.Fatal("Swift parser returned nil")
	}
	if err := ctx.Err(); err != nil {
		t.Fatalf("Swift parser exceeded deadline: %v", err)
	}
	root := tree.RootNode()
	if root.IsNull() || root.Type() != "source_file" ||
		root.EndByte() != uint32(len(source)) {
		t.Fatalf(
			"Swift root = %q %d-%d, want source_file 0-%d",
			root.Type(),
			root.StartByte(),
			root.EndByte(),
			len(source),
		)
	}
	swiftWalkTestTree(t, root, func(node treesitter.Node) {
		if node.IsMissing() || node.Symbol() == treesitter.SymbolError {
			t.Fatalf("valid Swift produced recovery node %q at %d", node.Type(), node.StartByte())
		}
	})
}

type swiftScanResult struct {
	ok      bool
	symbol  int
	endByte uint32
}

func (result swiftScanResult) require(
	t *testing.T,
	wantOK bool,
	wantSymbol int,
	wantEnd uint32,
) {
	t.Helper()
	if result.ok != wantOK || result.symbol != wantSymbol || result.endByte != wantEnd {
		t.Fatalf(
			"scan = ok:%v symbol:%d end:%d, want ok:%v symbol:%d end:%d",
			result.ok,
			result.symbol,
			result.endByte,
			wantOK,
			wantSymbol,
			wantEnd,
		)
	}
}

func swiftScanTest(
	t *testing.T,
	scanner *swiftScanner,
	source string,
	valid ...int,
) swiftScanResult {
	t.Helper()
	lexer := swiftTestLexer(source)
	validSymbols := make([]bool, swiftExternalTokenCount)
	for _, symbol := range valid {
		if symbol < 0 || symbol >= len(validSymbols) {
			t.Fatalf("invalid test symbol %d", symbol)
		}
		validSymbols[symbol] = true
	}
	ok := scanner.Scan(lexer, validSymbols)
	if ok && !lexer.MarkEndCalled() {
		lexer.TokenEndPosition = lexer.CurrentPosition()
	}
	return swiftScanResult{
		ok:      ok,
		symbol:  int(lexer.ResultSymbol),
		endByte: lexer.TokenEndPosition.Bytes,
	}
}

func swiftTestLexer(source string) *treesitter.Lexer {
	lexer := treesitter.NewLexer()
	lexer.SetInput(treesitter.NewStringInput([]byte(source)))
	lexer.Start(treesitter.Length{})
	return lexer
}

func swiftWalkTestTree(
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
			t.Fatal("Swift test tree exceeded traversal operation cap")
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
