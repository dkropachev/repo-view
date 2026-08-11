package navigator

import (
	"sort"
	"strings"
	"unicode/utf8"
)

// swiftTreeDefinitions extracts source-level and member declarations useful
// to navigation. Local functions and local named types remain useful targets;
// ordinary local variables and parameter bindings are deliberately omitted.
type swiftTreeAnalysisContext struct {
	attachedStarts     []int
	declarationAllowed []bool
	errorContext       []bool
	suppressed         []bool
}

func newSwiftTreeAnalysisContext(
	source string,
	tree *swiftSyntaxTree,
	lexed swiftLexResult,
) swiftTreeAnalysisContext {
	if !validateSwiftSyntaxTree(tree, len(source)) {
		return swiftTreeAnalysisContext{}
	}
	context := swiftTreeAnalysisContext{
		attachedStarts:     swiftSyntaxAttachedStarts(source, tree),
		declarationAllowed: make([]bool, len(tree.nodes)),
		errorContext:       swiftSyntaxErrorContexts(tree),
		suppressed:         make([]bool, len(tree.nodes)),
	}
	boundaries := swiftTreeDeclarationBoundaries(
		source, tree, context.attachedStarts, lexed,
	)
	recoveryDescendant := swiftSyntaxErrorDescendants(tree)
	trailingRecovery := swiftTreeDeclarationTrailingRecovery(
		source, tree, lexed.tokens,
	)
	rejectedContainers := make([]cByteSpan, 0)
	for nodeIndex, node := range tree.nodes {
		if cValidSyntaxNodeIndex(tree, node.parent) {
			parent := tree.nodes[node.parent]
			context.suppressed[nodeIndex] = context.suppressed[node.parent] ||
				swiftTreeDeclarationKind(parent.kind) &&
					!context.declarationAllowed[node.parent]
		}
		if !swiftTreeDeclarationKind(node.kind) {
			continue
		}
		boundaryAllowed := nodeIndex < len(boundaries) && boundaries[nodeIndex] &&
			swiftTreeDeclarationPrefixValid(source, node)
		signatureRejected := swiftTreeDeclarationSignatureHasRecovery(
			tree, nodeIndex, recoveryDescendant,
		) || swiftSyntaxNodeFlagged(trailingRecovery, nodeIndex)
		if (!boundaryAllowed || signatureRejected) &&
			!swiftSyntaxNodeInError(context.errorContext, nodeIndex) &&
			swiftTreeDeclarationOwnsScope(tree, nodeIndex) {
			rejectedContainers = append(rejectedContainers, cByteSpan{
				start: node.startByte, end: node.endByte,
			})
		}
		if context.suppressed[nodeIndex] ||
			swiftSyntaxNodeInError(context.errorContext, nodeIndex) ||
			!boundaryAllowed || signatureRejected {
			continue
		}
		context.declarationAllowed[nodeIndex] = true
	}
	rejectedContainers = append(
		rejectedContainers,
		swiftTreeRejectedRecoveryBlockSpans(source, tree, lexed)...,
	)
	rejectedContainers = normalizeCSpans(rejectedContainers)
	for nodeIndex, node := range tree.nodes {
		position := sort.Search(len(rejectedContainers), func(index int) bool {
			return rejectedContainers[index].end > node.startByte
		})
		if position >= len(rejectedContainers) ||
			rejectedContainers[position].start >= node.startByte {
			continue
		}
		context.suppressed[nodeIndex] = true
		if swiftTreeDeclarationKind(node.kind) {
			context.declarationAllowed[nodeIndex] = false
		}
	}
	return context
}

// A bodyless declaration can be recovered as a valid declaration node followed
// by an ERROR sibling for an invalid suffix. Associate that suffix with the
// declaration only when it is on the same physical line and no explicit
// semicolon separates it; a later malformed statement must not invalidate an
// otherwise complete declaration.
func swiftTreeDeclarationTrailingRecovery(
	source string,
	tree *swiftSyntaxTree,
	tokens []swiftToken,
) []bool {
	result := make([]bool, len(tree.nodes))
	for _, parent := range tree.nodes {
		for position := range len(parent.children) {
			declarationIndex := parent.children[position]
			if !cValidSyntaxNodeIndex(tree, declarationIndex) ||
				!swiftTreeDeclarationKind(tree.nodes[declarationIndex].kind) ||
				swiftTreeDeclarationBodyStart(tree, declarationIndex) >= 0 {
				continue
			}
			nextPosition := position + 1
			for nextPosition < len(parent.children) {
				nextIndex := parent.children[nextPosition]
				if !cValidSyntaxNodeIndex(tree, nextIndex) ||
					!swiftTreeCommentNode(tree.nodes[nextIndex].kind) {
					break
				}
				nextPosition++
			}
			if nextPosition >= len(parent.children) {
				continue
			}
			recoveryIndex := parent.children[nextPosition]
			if !cValidSyntaxNodeIndex(tree, recoveryIndex) ||
				tree.nodes[recoveryIndex].kind != "ERROR" {
				continue
			}
			declaration := tree.nodes[declarationIndex]
			recovery := tree.nodes[recoveryIndex]
			if declaration.endByte > recovery.startByte ||
				swiftSourceHasPhysicalLineBreak(
					source[declaration.endByte:recovery.startByte],
				) || swiftTreeTokenBetween(
				tokens, declaration.endByte, recovery.startByte, ";",
			) || swiftTreeFirstTokenIs(
				tokens, declaration.endByte, recovery.endByte, ";",
			) {
				continue
			}
			result[declarationIndex] = true
		}
	}
	return result
}

// Tree-sitter can recover a malformed declaration as two source-level
// siblings: an ERROR containing the declaration head followed by a lambda
// containing the apparent body. In that shape the lambda's declarations are
// outside the rejected declaration node and would otherwise be promoted. Join
// only same-line, non-semicolon-separated siblings whose leading tokens still
// form a declaration; ordinary standalone and trailing closures remain valid.
func swiftTreeRejectedRecoveryBlockSpans(
	source string,
	tree *swiftSyntaxTree,
	lexed swiftLexResult,
) []cByteSpan {
	if !validateSwiftSyntaxTree(tree, len(source)) || len(lexed.tokens) == 0 {
		return nil
	}
	spans := make([]cByteSpan, 0)
	lineStarts := swiftLineStarts(source)
	firstLambda := swiftTreeFirstDescendantsOfKind(tree, "lambda_literal")
	for _, parent := range tree.nodes {
		for childPosition, childIndex := range parent.children {
			if !cValidSyntaxNodeIndex(tree, childIndex) ||
				tree.nodes[childIndex].kind != "ERROR" {
				continue
			}
			head := tree.nodes[childIndex]
			nextPosition := childPosition + 1
			for nextPosition < len(parent.children) {
				nextIndex := parent.children[nextPosition]
				if !cValidSyntaxNodeIndex(tree, nextIndex) ||
					!swiftTreeCommentNode(tree.nodes[nextIndex].kind) {
					break
				}
				nextPosition++
			}
			if nextPosition >= len(parent.children) {
				continue
			}
			nextIndex := parent.children[nextPosition]
			if !cValidSyntaxNodeIndex(tree, nextIndex) {
				continue
			}
			lambdaIndex := firstLambda[nextIndex]
			if lambdaIndex < 0 {
				continue
			}
			lambda := tree.nodes[lambdaIndex]
			if head.endByte > lambda.startByte ||
				swiftSourceHasPhysicalLineBreak(
					source[head.endByte:lambda.startByte],
				) || swiftTreeTokenBetween(
				lexed.tokens, head.endByte, lambda.startByte, ";",
			) {
				continue
			}
			header := swiftTreeTokensInSpan(
				lexed.tokens, head.startByte, lambda.startByte,
			)
			if swiftTreeRecoveryHeadRejectsBlock(
				source, tree, childIndex, header, lineStarts,
			) {
				spans = append(spans, cByteSpan{
					start: head.startByte,
					end:   tree.nodes[nextIndex].endByte,
				})
			}
		}
	}
	return normalizeCSpans(spans)
}

func swiftTreeCommentNode(kind string) bool {
	return kind == "comment" || kind == "multiline_comment"
}

func swiftTreeRecoveryHeadRejectsBlock(
	source string,
	tree *swiftSyntaxTree,
	nodeIndex int,
	header []swiftToken,
	lineStarts []int,
) bool {
	keywordIndex, keyword := swiftHeaderDeclaration(header)
	headerKind := swiftHeaderKindForKeyword(keyword)
	if keywordIndex < 0 || headerKind == swiftHeaderNone {
		return false
	}
	balance := swiftHeaderBalance{}
	for _, token := range header {
		balance.accept(token)
	}
	if balance.invalid || swiftUnbalancedDeclarationContainer(
		headerKind, balance, header, keywordIndex,
	) {
		return true
	}
	parser := swiftRecoveryParser{
		source:        source,
		lineStarts:    lineStarts,
		lineCount:     max(1, len(lineStarts)),
		header:        header,
		headerBalance: balance,
		headerKind:    headerKind,
		frames: []swiftRecoveryFrame{{
			kind: swiftTreeRecoveryFrameKind(tree, nodeIndex),
		}},
	}
	classified, _, _ := parser.classifyHeader(true)
	if classified == swiftHeaderNone {
		return true
	}
	switch classified {
	case swiftHeaderTypeAlias, swiftHeaderAssociatedType, swiftHeaderEnumCase,
		swiftHeaderImport, swiftHeaderOperator, swiftHeaderPrecedence:
		return true
	case swiftHeaderMacro:
		return !swiftHeaderContainsTopLevel(header, "=")
	case swiftHeaderNone, swiftHeaderType, swiftHeaderFunction,
		swiftHeaderProperty, swiftHeaderInit, swiftHeaderDeinit,
		swiftHeaderSubscript:
		return false
	}
	return false
}

func swiftTreeRecoveryFrameKind(
	tree *swiftSyntaxTree,
	nodeIndex int,
) swiftRecoveryFrameKind {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return swiftRecoverySource
	}
	for parent := tree.nodes[nodeIndex].parent; cValidSyntaxNodeIndex(tree, parent); {
		switch tree.nodes[parent].kind {
		case "enum_class_body":
			return swiftRecoveryEnum
		case "protocol_body":
			return swiftRecoveryProtocol
		case "class_body":
			return swiftRecoveryType
		case "computed_property":
			return swiftRecoveryProperty
		case "function_body", "lambda_literal":
			return swiftRecoveryFunction
		case "source_file":
			return swiftRecoverySource
		}
		next := tree.nodes[parent].parent
		if next >= parent {
			return swiftRecoverySource
		}
		parent = next
	}
	return swiftRecoverySource
}

func swiftTreeFirstDescendantsOfKind(
	tree *swiftSyntaxTree,
	kind string,
) []int {
	result := make([]int, len(tree.nodes))
	for index := range result {
		result[index] = -1
	}
	for nodeIndex := len(tree.nodes) - 1; nodeIndex >= 0; nodeIndex-- {
		if tree.nodes[nodeIndex].kind == kind {
			result[nodeIndex] = nodeIndex
			continue
		}
		for _, childIndex := range tree.nodes[nodeIndex].children {
			if cValidSyntaxNodeIndex(tree, childIndex) && result[childIndex] >= 0 {
				result[nodeIndex] = result[childIndex]
				break
			}
		}
	}
	return result
}

func swiftTreeTokensInSpan(
	tokens []swiftToken,
	start, end int,
) []swiftToken {
	first := sort.Search(len(tokens), func(index int) bool {
		return tokens[index].end > start
	})
	last := sort.Search(len(tokens), func(index int) bool {
		return tokens[index].start >= end
	})
	if first >= last {
		return nil
	}
	return tokens[first:last]
}

func swiftTreeTokenBetween(
	tokens []swiftToken,
	start, end int,
	want string,
) bool {
	for _, token := range swiftTreeTokensInSpan(tokens, start, end) {
		if token.text == want {
			return true
		}
	}
	return false
}

func swiftTreeFirstTokenIs(
	tokens []swiftToken,
	start, end int,
	want string,
) bool {
	spanTokens := swiftTreeTokensInSpan(tokens, start, end)
	return len(spanTokens) > 0 && spanTokens[0].text == want
}

func swiftTreeDefinitions(
	source string,
	lineCount int,
	tree *swiftSyntaxTree,
) []sourceDefinition {
	context := newSwiftTreeAnalysisContext(source, tree, lexSwift(source))
	return swiftTreeDefinitionsWithContext(source, lineCount, tree, context)
}

func swiftTreeDefinitionsWithContext(
	source string,
	lineCount int,
	tree *swiftSyntaxTree,
	context swiftTreeAnalysisContext,
) []sourceDefinition {
	if lineCount < 1 || !validateSwiftSyntaxTree(tree, len(source)) {
		return nil
	}

	positions := cSourcePositions{
		source:     source,
		lineStarts: cTreeLineStarts(source),
	}
	definitions := make([]sourceDefinition, 0)

	appendDefinition := func(
		nameIndex, scopeIndex int,
		symbol string,
		nameStart int,
		ownsScope bool,
	) {
		if !cValidSyntaxNodeIndex(tree, nameIndex) ||
			!cValidSyntaxNodeIndex(tree, scopeIndex) ||
			symbol == "" || nameStart < 0 || nameStart >= len(source) {
			return
		}
		line, column := positions.lineColumn(nameStart)
		scopeStart, scopeEnd := line, line
		ownedEndLine, ownedEndColumn := 0, 0
		if ownsScope {
			scope := tree.nodes[scopeIndex]
			start := swiftSyntaxAttachedScopeStart(
				tree, scopeIndex, context.attachedStarts,
			)
			end := swiftTreeDeclarationScopeEnd(tree, scopeIndex)
			if end < scope.endByte {
				end = scope.endByte
			}
			scopeStart, scopeEnd = positions.lineSpan(start, end)
			ownedEndLine, ownedEndColumn = positions.lineColumn(end)
		}
		if !ownsScope || ownedEndLine != scopeEnd {
			ownedEndColumn = 0
		}
		definitions = append(definitions, sourceDefinition{
			symbol: symbol, line: line, column: column,
			scopeStart: scopeStart, scopeEnd: scopeEnd,
			ownedEndColumn: ownedEndColumn, ownsScope: ownsScope,
		})
	}

	appendIdentifierDefinition := func(nameIndex, scopeIndex int, ownsScope bool) {
		symbol, nameStart, ok := swiftTreeIdentifierSourceName(
			source, tree, nameIndex,
		)
		if !ok {
			return
		}
		appendDefinition(
			nameIndex, scopeIndex, symbol, nameStart, ownsScope,
		)
	}

	for nodeIndex, node := range tree.nodes {
		if swiftSyntaxNodeInError(context.errorContext, nodeIndex) ||
			nodeIndex >= len(context.suppressed) || context.suppressed[nodeIndex] {
			continue
		}
		if swiftTreeDeclarationKind(node.kind) &&
			(nodeIndex >= len(context.declarationAllowed) ||
				!context.declarationAllowed[nodeIndex]) {
			continue
		}

		switch node.kind {
		case "class_declaration", "protocol_declaration":
			nameIndex := swiftTreeTypeDeclarationNameNode(tree, nodeIndex)
			appendIdentifierDefinition(
				nameIndex, nodeIndex,
				swiftTreeDeclarationOwnsScope(tree, nodeIndex),
			)

		case "function_declaration", "protocol_function_declaration":
			nameIndex := cDirectChildOfKinds(
				tree, nodeIndex, "simple_identifier", "custom_operator",
			)
			if nameIndex >= 0 {
				appendIdentifierDefinition(
					nameIndex, nodeIndex,
					swiftTreeDeclarationOwnsScope(tree, nodeIndex),
				)
			} else if symbol, nameStart, ok := swiftTreeFunctionDeclarationName(
				source, node,
			); ok {
				appendDefinition(
					nodeIndex, nodeIndex, symbol, nameStart,
					swiftTreeDeclarationOwnsScope(tree, nodeIndex),
				)
			}

		case "property_declaration", "protocol_property_declaration":
			if !swiftTreeOutlineProperty(tree, nodeIndex) {
				continue
			}
			ownsScope := swiftTreeDeclarationOwnsScope(tree, nodeIndex)
			for _, nameIndex := range swiftTreePropertyNameNodes(tree, nodeIndex) {
				appendIdentifierDefinition(nameIndex, nodeIndex, ownsScope)
			}

		case "typealias_declaration":
			if !swiftTreeOutlineTypeAlias(tree, nodeIndex) {
				continue
			}
			nameIndex := cDirectChildOfKind(tree, nodeIndex, "type_identifier")
			appendIdentifierDefinition(nameIndex, nodeIndex, false)

		case "associatedtype_declaration":
			nameIndex := cDirectChildOfKind(tree, nodeIndex, "type_identifier")
			appendIdentifierDefinition(nameIndex, nodeIndex, false)

		case "enum_entry":
			if parent := node.parent; !cValidSyntaxNodeIndex(tree, parent) ||
				tree.nodes[parent].kind != "enum_class_body" {
				continue
			}
			for _, nameIndex := range swiftTreeEnumCaseNameNodes(tree, nodeIndex) {
				appendIdentifierDefinition(nameIndex, nodeIndex, false)
			}

		case "init_declaration", "deinit_declaration", "subscript_declaration":
			keyword := strings.TrimSuffix(node.kind, "_declaration")
			if nameStart, ok := swiftTreeDeclarationKeywordStart(
				source, node, keyword,
			); ok {
				appendDefinition(
					nodeIndex, nodeIndex, keyword, nameStart,
					swiftTreeDeclarationOwnsScope(tree, nodeIndex),
				)
			}

		case "macro_declaration", "precedence_group_declaration":
			nameIndex := cDirectChildOfKind(tree, nodeIndex, "simple_identifier")
			appendIdentifierDefinition(
				nameIndex, nodeIndex,
				swiftTreeDeclarationOwnsScope(tree, nodeIndex),
			)

		case "operator_declaration":
			nameIndex := cDirectChildOfKind(tree, nodeIndex, "custom_operator")
			if nameIndex >= 0 {
				appendIdentifierDefinition(nameIndex, nodeIndex, false)
			} else if symbol, nameStart, ok := swiftTreeOperatorDeclarationName(
				source, node,
			); ok {
				appendDefinition(nodeIndex, nodeIndex, symbol, nameStart, false)
			}
		}
	}

	return cSortUniqueTreeDefinitions(definitions, lineCount)
}

func swiftTreeDeclarationKind(kind string) bool {
	switch kind {
	case "class_declaration", "protocol_declaration", "function_declaration",
		"protocol_function_declaration", "property_declaration",
		"protocol_property_declaration", "typealias_declaration",
		"associatedtype_declaration", "enum_entry", "init_declaration",
		"deinit_declaration", "subscript_declaration", "macro_declaration",
		"precedence_group_declaration", "operator_declaration",
		"import_declaration":
		return true
	default:
		return false
	}
}

type swiftTreeSignificantUnit struct {
	text       string
	start, end int
	token      bool
}

// swiftTreeDeclarationBoundaries rejects declarations that tree-sitter
// recovered out of an expression on the same physical line. Declaration-local
// validation cannot see such a prefix because the recovery node is a preceding
// sibling (for example, `foo struct Phantom {}`). Build the significant-unit
// index once so a hostile one-line source cannot turn the check quadratic.
func swiftTreeDeclarationBoundaries(
	source string,
	tree *swiftSyntaxTree,
	attachedStarts []int,
	lexed swiftLexResult,
) []bool {
	boundaries := make([]bool, len(tree.nodes))
	units := make([]swiftTreeSignificantUnit, 0,
		len(lexed.tokens)+len(lexed.stringSpans))
	for _, token := range lexed.tokens {
		if token.gap || token.start < 0 || token.end <= token.start ||
			token.end > len(source) {
			continue
		}
		units = append(units, swiftTreeSignificantUnit{
			text: token.text, start: token.start, end: token.end, token: true,
		})
	}
	for _, span := range lexed.stringSpans {
		if span.start < 0 || span.end <= span.start || span.end > len(source) {
			continue
		}
		units = append(units, swiftTreeSignificantUnit{
			start: span.start, end: span.end,
		})
	}
	sort.Slice(units, func(left, right int) bool {
		if units[left].start != units[right].start {
			return units[left].start < units[right].start
		}
		return units[left].end < units[right].end
	})

	for nodeIndex, node := range tree.nodes {
		if !swiftTreeDeclarationKind(node.kind) {
			continue
		}
		start := swiftSyntaxAttachedScopeStart(tree, nodeIndex, attachedStarts)
		position := sort.Search(len(units), func(index int) bool {
			return units[index].start >= start
		})
		if position == 0 {
			boundaries[nodeIndex] = true
			continue
		}
		previous := units[position-1]
		if previous.end > start {
			continue
		}
		if swiftSourceHasPhysicalLineBreak(source[previous.end:start]) {
			boundaries[nodeIndex] = true
			continue
		}
		if previous.token {
			switch previous.text {
			case "{", ";":
				boundaries[nodeIndex] = true
			case ":":
				boundaries[nodeIndex] = swiftTreeHasAncestorKind(
					tree, nodeIndex, "switch_entry",
				)
			case "in":
				boundaries[nodeIndex] = swiftTreeHasAncestorKind(
					tree, nodeIndex, "lambda_literal",
				)
			}
		}
	}
	return boundaries
}

func swiftTreeHasAncestorKind(
	tree *swiftSyntaxTree,
	nodeIndex int,
	kind string,
) bool {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return false
	}
	for parent := tree.nodes[nodeIndex].parent; cValidSyntaxNodeIndex(tree, parent); {
		if tree.nodes[parent].kind == kind {
			return true
		}
		next := tree.nodes[parent].parent
		if next >= parent {
			return false
		}
		parent = next
	}
	return false
}

func swiftSourceHasPhysicalLineBreak(source string) bool {
	return strings.ContainsAny(source, "\r\n")
}

func swiftTreeEnumCaseNameNodes(tree *swiftSyntaxTree, nodeIndex int) []int {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) ||
		tree.nodes[nodeIndex].kind != "enum_entry" {
		return nil
	}
	names := make([]int, 0, 1)
	wantName := true
	skippingRawValue := false
	for _, childIndex := range tree.nodes[nodeIndex].children {
		if !cValidSyntaxNodeIndex(tree, childIndex) {
			return nil
		}
		child := tree.nodes[childIndex]
		switch child.kind {
		case "=":
			skippingRawValue = true
			wantName = false
		case ",":
			skippingRawValue = false
			wantName = true
		case "simple_identifier":
			if skippingRawValue || !wantName {
				continue
			}
			names = append(names, childIndex)
			wantName = false
		}
	}
	return names
}

func swiftTreeTypeDeclarationNameNode(tree *swiftSyntaxTree, nodeIndex int) int {
	if nameIndex := cDirectChildOfKind(tree, nodeIndex, "type_identifier"); nameIndex >= 0 {
		return nameIndex
	}
	return cDirectChildOfKind(tree, nodeIndex, "user_type")
}

func swiftTreeDeclarationKeywordStart(
	source string,
	node swiftSyntaxNode,
	keyword string,
) (int, bool) {
	if keyword == "" || node.startByte < 0 || node.endByte <= node.startByte ||
		node.endByte > len(source) {
		return 0, false
	}
	tokens := swiftTreeDeclarationPrefixTokens(source, node)
	index, got := swiftHeaderDeclaration(tokens)
	if index < 0 || got != keyword {
		return 0, false
	}
	return node.startByte + tokens[index].start, true
}

func swiftTreeDeclarationPrefixValid(source string, node swiftSyntaxNode) bool {
	index, keyword := swiftHeaderDeclaration(swiftTreeDeclarationPrefixTokens(source, node))
	if index < 0 {
		return false
	}
	switch node.kind {
	case "class_declaration":
		return keyword == "class" || keyword == "struct" || keyword == "actor" ||
			keyword == "enum" || keyword == "extension"
	case "protocol_declaration":
		return keyword == "protocol"
	case "function_declaration", "protocol_function_declaration":
		return keyword == "func"
	case "property_declaration", "protocol_property_declaration":
		return keyword == "let" || keyword == "var"
	case "typealias_declaration":
		return keyword == "typealias"
	case "associatedtype_declaration":
		return keyword == "associatedtype"
	case "enum_entry":
		return keyword == "case"
	case "init_declaration":
		return keyword == "init"
	case "deinit_declaration":
		return keyword == "deinit"
	case "subscript_declaration":
		return keyword == "subscript"
	case "macro_declaration":
		return keyword == "macro"
	case "precedence_group_declaration":
		return keyword == "precedencegroup"
	case "operator_declaration":
		return keyword == "operator"
	case "import_declaration":
		return keyword == "import"
	default:
		return false
	}
}

func swiftTreeDeclarationPrefixTokens(source string, node swiftSyntaxNode) []swiftToken {
	if node.startByte < 0 || node.endByte <= node.startByte || node.endByte > len(source) {
		return nil
	}
	raw := source[node.startByte:node.endByte]
	tokens := make([]swiftToken, 0, 16)
	walkSwiftLexically(raw, swiftLexicalSink{token: func(token swiftToken) bool {
		if token.text == "{" || len(tokens) >= swiftMaximumHeaderTokens {
			return false
		}
		tokens = append(tokens, token)
		return true
	}})
	return tokens
}

func swiftTreeOperatorDeclarationName(
	source string,
	node swiftSyntaxNode,
) (string, int, bool) {
	keywordStart, ok := swiftTreeDeclarationKeywordStart(source, node, "operator")
	if !ok {
		return "", 0, false
	}
	start := keywordStart + len("operator")
	for start < node.endByte && swiftASCIIWhitespace(source[start]) {
		start++
	}
	end := start
	for end < node.endByte {
		character := source[end]
		if swiftASCIIWhitespace(character) || character == ':' || character == '{' {
			break
		}
		end++
	}
	if end <= start {
		return "", 0, false
	}
	return source[start:end], start, true
}

func swiftTreeFunctionDeclarationName(
	source string,
	node swiftSyntaxNode,
) (string, int, bool) {
	keywordStart, ok := swiftTreeDeclarationKeywordStart(source, node, "func")
	if !ok {
		return "", 0, false
	}
	start := keywordStart + len("func")
	for start < node.endByte && swiftASCIIWhitespace(source[start]) {
		start++
	}
	end := start
	for end < node.endByte {
		character := source[end]
		if swiftASCIIWhitespace(character) || character == '(' {
			break
		}
		end++
	}
	if end <= start {
		return "", 0, false
	}
	symbol := source[start:end]
	if !swiftOperatorSymbol(symbol) {
		return "", 0, false
	}
	return symbol, start, true
}

func swiftTreeDeclarationSignatureHasRecovery(
	tree *swiftSyntaxTree,
	nodeIndex int,
	recoveryDescendant []bool,
) bool {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return true
	}
	boundary := swiftTreeDeclarationBodyStart(tree, nodeIndex)
	for _, childIndex := range tree.nodes[nodeIndex].children {
		if !cValidSyntaxNodeIndex(tree, childIndex) {
			return true
		}
		child := tree.nodes[childIndex]
		if boundary >= 0 && child.startByte >= boundary {
			continue
		}
		if swiftSyntaxNodeFlagged(recoveryDescendant, childIndex) {
			return true
		}
	}
	return false
}

func swiftTreeDeclarationBodyStart(tree *swiftSyntaxTree, nodeIndex int) int {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return -1
	}
	node := tree.nodes[nodeIndex]
	for _, childIndex := range node.children {
		if !cValidSyntaxNodeIndex(tree, childIndex) {
			return -1
		}
		child := tree.nodes[childIndex]
		body := false
		switch node.kind {
		case "class_declaration":
			body = child.kind == "class_body" || child.kind == "enum_class_body"
		case "protocol_declaration":
			body = child.kind == "protocol_body"
		case "function_declaration", "protocol_function_declaration",
			"init_declaration", "deinit_declaration":
			body = child.kind == "function_body"
		case "property_declaration", "protocol_property_declaration",
			"subscript_declaration":
			body = child.kind == "computed_property"
		case "precedence_group_declaration":
			body = child.kind == "precedence_group_attributes"
		}
		if body {
			return child.startByte
		}
	}
	return -1
}

func swiftTreeDeclarationOwnsScope(tree *swiftSyntaxTree, nodeIndex int) bool {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return false
	}
	node := tree.nodes[nodeIndex]
	switch node.kind {
	case "class_declaration", "protocol_declaration", "function_declaration",
		"protocol_function_declaration", "init_declaration", "deinit_declaration",
		"subscript_declaration", "precedence_group_declaration":
		return true
	case "property_declaration", "protocol_property_declaration":
		return cDirectChildOfKind(tree, nodeIndex, "computed_property") >= 0 ||
			node.kind == "protocol_property_declaration"
	default:
		return false
	}
}

func swiftTreeDeclarationScopeEnd(
	tree *swiftSyntaxTree,
	nodeIndex int,
) int {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return 0
	}
	return tree.nodes[nodeIndex].endByte
}

func swiftTreeOutlineProperty(tree *swiftSyntaxTree, nodeIndex int) bool {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return false
	}
	parent := tree.nodes[nodeIndex].parent
	if !cValidSyntaxNodeIndex(tree, parent) {
		return false
	}
	switch tree.nodes[parent].kind {
	case "source_file", "class_body", "enum_class_body", "protocol_body":
		return true
	default:
		return false
	}
}

func swiftTreeOutlineTypeAlias(tree *swiftSyntaxTree, nodeIndex int) bool {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return false
	}
	parent := tree.nodes[nodeIndex].parent
	if !cValidSyntaxNodeIndex(tree, parent) {
		return false
	}
	switch tree.nodes[parent].kind {
	case "source_file", "class_body", "enum_class_body", "protocol_body", "statements":
		return true
	default:
		return false
	}
}

func swiftTreePropertyNameNodes(
	tree *swiftSyntaxTree,
	nodeIndex int,
) []int {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return nil
	}
	result := make([]int, 0, 1)
	for _, childIndex := range tree.nodes[nodeIndex].children {
		if !cValidSyntaxNodeIndex(tree, childIndex) {
			return nil
		}
		if tree.nodes[childIndex].kind != "pattern" {
			continue
		}
		if nameIndex, ok := swiftTreeSinglePatternIdentifier(tree, childIndex); ok {
			result = append(result, nameIndex)
		}
	}
	return result
}

func swiftTreeSinglePatternIdentifier(
	tree *swiftSyntaxTree,
	patternIndex int,
) (int, bool) {
	if !cValidSyntaxNodeIndex(tree, patternIndex) {
		return -1, false
	}
	nameIndex := -1
	stack := append([]int(nil), tree.nodes[patternIndex].children...)
	for len(stack) > 0 {
		last := len(stack) - 1
		nodeIndex := stack[last]
		stack = stack[:last]
		if !cValidSyntaxNodeIndex(tree, nodeIndex) {
			return -1, false
		}
		node := tree.nodes[nodeIndex]
		if node.kind == "simple_identifier" {
			if nameIndex >= 0 {
				return -1, false
			}
			nameIndex = nodeIndex
			continue
		}
		stack = append(stack, node.children...)
	}
	return nameIndex, nameIndex >= 0
}

func swiftTreeIdentifierSourceName(
	source string,
	tree *swiftSyntaxTree,
	nodeIndex int,
) (symbol string, nameStart int, ok bool) {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return "", 0, false
	}
	node := tree.nodes[nodeIndex]
	if node.startByte < 0 || node.endByte <= node.startByte ||
		node.endByte > len(source) {
		return "", 0, false
	}
	raw := source[node.startByte:node.endByte]
	if node.kind == "custom_operator" {
		return raw, node.startByte, swiftOperatorSymbol(raw)
	}
	if node.kind == "user_type" {
		nameBytes := make([]byte, 0, len(raw))
		for index := range len(raw) {
			if !swiftASCIIWhitespace(raw[index]) {
				nameBytes = append(nameBytes, raw[index])
			}
		}
		name := string(nameBytes)
		return name, node.startByte,
			name != "" && swiftDefinitionSymbolValid(name) && !swiftHardKeyword(name)
	}
	quoted := raw[0] == '`'
	text, end, relativeNameStart, valid := swiftIdentifierAt(raw, 0)
	if !valid || text == "" || end != len(raw) || !quoted && swiftHardKeyword(text) {
		return "", 0, false
	}
	return text, node.startByte + relativeNameStart, true
}

func swiftTreeScopes(
	source string,
	lineCount int,
	tree *swiftSyntaxTree,
) []cLineScope {
	context := newSwiftTreeAnalysisContext(source, tree, lexSwift(source))
	return swiftTreeScopesWithContext(source, lineCount, tree, context)
}

func swiftTreeScopesWithContext(
	source string,
	lineCount int,
	tree *swiftSyntaxTree,
	context swiftTreeAnalysisContext,
) []cLineScope {
	if lineCount < 1 || !validateSwiftSyntaxTree(tree, len(source)) {
		return nil
	}
	positions := cSourcePositions{
		source:     source,
		lineStarts: cTreeLineStarts(source),
	}
	scopes := make([]cLineScope, 0)
	for nodeIndex := range tree.nodes {
		if swiftSyntaxNodeInError(context.errorContext, nodeIndex) ||
			nodeIndex >= len(context.suppressed) || context.suppressed[nodeIndex] {
			continue
		}
		if swiftTreeDeclarationKind(tree.nodes[nodeIndex].kind) &&
			(nodeIndex >= len(context.declarationAllowed) ||
				!context.declarationAllowed[nodeIndex]) {
			continue
		}
		scopeIndex, attachDeclaration, ok := swiftTreeScopeDescriptor(tree, nodeIndex)
		if !ok || !cValidSyntaxNodeIndex(tree, scopeIndex) {
			continue
		}
		scope := tree.nodes[scopeIndex]
		start := scope.startByte
		if attachDeclaration {
			start = swiftSyntaxAttachedScopeStart(
				tree, scopeIndex, context.attachedStarts,
			)
		}
		end := swiftTreeDeclarationScopeEnd(tree, scopeIndex)
		if tree.nodes[scopeIndex].kind == "switch_entry" {
			end = max(end, swiftTreeSwitchEntryScopeEnd(tree, scopeIndex))
		}
		if end < scope.endByte {
			end = scope.endByte
		}
		startLine, endLine := positions.lineSpan(start, end)
		scopes = append(scopes, cLineScope{start: startLine, end: endLine})
	}
	return cNormalizeTreeLineScopes(scopes, lineCount)
}

func swiftTreeSwitchEntryScopeEnd(tree *swiftSyntaxTree, nodeIndex int) int {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) ||
		tree.nodes[nodeIndex].kind != "switch_entry" {
		return 0
	}
	parentIndex := tree.nodes[nodeIndex].parent
	if !cValidSyntaxNodeIndex(tree, parentIndex) {
		return tree.nodes[nodeIndex].endByte
	}
	found := false
	for _, siblingIndex := range tree.nodes[parentIndex].children {
		if siblingIndex == nodeIndex {
			found = true
			continue
		}
		if !found || !cValidSyntaxNodeIndex(tree, siblingIndex) {
			continue
		}
		if tree.nodes[siblingIndex].kind == "switch_entry" {
			return tree.nodes[siblingIndex].startByte + 1
		}
	}
	return tree.nodes[nodeIndex].endByte
}

func swiftTreeScopeDescriptor(
	tree *swiftSyntaxTree,
	nodeIndex int,
) (scopeIndex int, attachDeclaration, ok bool) {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return -1, false, false
	}
	node := tree.nodes[nodeIndex]
	switch node.kind {
	case "class_declaration", "protocol_declaration", "function_declaration",
		"protocol_function_declaration", "init_declaration", "deinit_declaration",
		"subscript_declaration", "precedence_group_declaration":
		return nodeIndex, true, swiftTreeDeclarationOwnsScope(tree, nodeIndex)
	case "property_declaration", "protocol_property_declaration":
		return nodeIndex, true, swiftTreeOutlineProperty(tree, nodeIndex) &&
			swiftTreeDeclarationOwnsScope(tree, nodeIndex)

	case "computed_getter", "computed_setter", "computed_modify", "lambda_literal",
		"if_statement", "guard_statement", "for_statement", "while_statement",
		"repeat_while_statement", "do_statement", "catch_block",
		"switch_statement", "switch_entry":
		return nodeIndex, false, true

	case "class_body", "enum_class_body", "protocol_body", "function_body",
		"computed_property", "precedence_group_attributes":
		if swiftTreeBodyOwnedByParent(tree, nodeIndex) {
			return -1, false, false
		}
		return nodeIndex, false, true

	default:
		return -1, false, false
	}
}

func swiftTreeBodyOwnedByParent(tree *swiftSyntaxTree, nodeIndex int) bool {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return false
	}
	parent := tree.nodes[nodeIndex].parent
	if !cValidSyntaxNodeIndex(tree, parent) {
		return false
	}
	parentKind := tree.nodes[parent].kind
	switch parentKind {
	case "class_declaration", "protocol_declaration", "function_declaration",
		"protocol_function_declaration", "property_declaration",
		"protocol_property_declaration", "init_declaration", "deinit_declaration",
		"subscript_declaration", "precedence_group_declaration",
		"computed_getter", "computed_setter", "computed_modify", "lambda_literal",
		"if_statement", "guard_statement", "for_statement", "while_statement",
		"repeat_while_statement", "do_statement", "catch_block",
		"switch_statement", "switch_entry":
		return true
	default:
		return false
	}
}

func swiftTreeImports(
	source string,
	lineCount int,
	tree *swiftSyntaxTree,
) []cLineSpan {
	context := newSwiftTreeAnalysisContext(source, tree, lexSwift(source))
	return swiftTreeImportsWithContext(source, lineCount, tree, context)
}

func swiftTreeImportsWithContext(
	source string,
	lineCount int,
	tree *swiftSyntaxTree,
	context swiftTreeAnalysisContext,
) []cLineSpan {
	if lineCount < 1 || !validateSwiftSyntaxTree(tree, len(source)) {
		return nil
	}
	positions := cSourcePositions{
		source:     source,
		lineStarts: cTreeLineStarts(source),
	}
	imports := make([]cLineSpan, 0)
	for nodeIndex, node := range tree.nodes {
		parentIndex := node.parent
		if node.kind != "import_declaration" ||
			!cValidSyntaxNodeIndex(tree, parentIndex) ||
			tree.nodes[parentIndex].kind != "source_file" ||
			swiftSyntaxNodeInError(context.errorContext, nodeIndex) ||
			nodeIndex >= len(context.declarationAllowed) ||
			!context.declarationAllowed[nodeIndex] {
			continue
		}
		start, end := positions.lineSpan(node.startByte, node.endByte)
		imports = append(imports, cLineSpan{start: start, end: end})
	}
	return cNormalizeTreeLineSpans(imports, lineCount)
}

func swiftSyntaxErrorContexts(tree *swiftSyntaxTree) []bool {
	if tree == nil || len(tree.nodes) == 0 {
		return nil
	}
	contexts := make([]bool, len(tree.nodes))
	for nodeIndex, node := range tree.nodes {
		contexts[nodeIndex] = swiftSyntaxRecoveryNode(tree, nodeIndex)
		if node.parent < 0 {
			continue
		}
		if node.parent >= nodeIndex || node.parent >= len(tree.nodes) {
			contexts[nodeIndex] = true
			continue
		}
		contexts[nodeIndex] = contexts[nodeIndex] || contexts[node.parent]
	}
	return contexts
}

func swiftSyntaxErrorDescendants(tree *swiftSyntaxTree) []bool {
	if tree == nil || len(tree.nodes) == 0 {
		return nil
	}
	descendants := make([]bool, len(tree.nodes))
	for nodeIndex := len(tree.nodes) - 1; nodeIndex >= 0; nodeIndex-- {
		node := tree.nodes[nodeIndex]
		if swiftSyntaxRecoveryNode(tree, nodeIndex) {
			descendants[nodeIndex] = true
		}
		if descendants[nodeIndex] && node.parent >= 0 &&
			node.parent < nodeIndex && node.parent < len(tree.nodes) {
			descendants[node.parent] = true
		}
	}
	return descendants
}

func swiftSyntaxErrorSpans(
	tree *swiftSyntaxTree,
	sourceLength int,
) []cByteSpan {
	if !validateSwiftSyntaxTree(tree, sourceLength) {
		return nil
	}
	spans := make([]cByteSpan, 0)
	for nodeIndex, node := range tree.nodes {
		if !swiftSyntaxRecoveryNode(tree, nodeIndex) {
			continue
		}
		start := max(0, min(node.startByte, sourceLength))
		end := max(start, min(node.endByte, sourceLength))
		if start == end {
			if end < sourceLength {
				end++
			} else if start > 0 {
				start--
			}
		}
		if start < end {
			spans = append(spans, cByteSpan{start: start, end: end})
		}
	}
	return cNormalizeSyntaxByteSpans(spans)
}

func swiftSyntaxRecoveryNode(tree *swiftSyntaxTree, nodeIndex int) bool {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return false
	}
	node := tree.nodes[nodeIndex]
	if node.kind == "ERROR" {
		return true
	}
	if nodeIndex == tree.root || node.startByte != node.endByte {
		return false
	}
	// Swift's external scanner may represent automatic semicolon insertion
	// with a zero-width hidden token. It is syntax, not a missing parser node.
	switch node.kind {
	case ";", "_implicit_semi", "_explicit_semi",
		"_automatic_semicolon", "automatic_semicolon":
		return false
	default:
		return true
	}
}

func swiftSyntaxNodeInError(errorContext []bool, nodeIndex int) bool {
	return nodeIndex < 0 || nodeIndex >= len(errorContext) || errorContext[nodeIndex]
}

func swiftSyntaxNodeFlagged(flags []bool, nodeIndex int) bool {
	return nodeIndex < 0 || nodeIndex >= len(flags) || flags[nodeIndex]
}

func swiftSyntaxAttachedStarts(
	source string,
	tree *swiftSyntaxTree,
) []int {
	if !validateSwiftSyntaxTree(tree, len(source)) {
		return nil
	}
	starts := make([]int, len(tree.nodes))
	for nodeIndex, node := range tree.nodes {
		starts[nodeIndex] = node.startByte
	}
	for _, parent := range tree.nodes {
		pendingStart, previousEnd := -1, -1
		for _, childIndex := range parent.children {
			if !cValidSyntaxNodeIndex(tree, childIndex) {
				pendingStart, previousEnd = -1, -1
				continue
			}
			child := tree.nodes[childIndex]
			adjacent := previousEnd >= 0 && previousEnd <= child.startByte &&
				swiftSourceAttachmentGap(source, previousEnd, child.startByte)
			if previousEnd >= 0 && !adjacent {
				pendingStart = -1
			}
			switch {
			case swiftKDocComment(source, child):
				if pendingStart < 0 {
					pendingStart = child.startByte
				}
			case child.kind == "comment" || child.kind == "multiline_comment":
				pendingStart = -1
			case swiftSyntaxAttachmentBridge(child.kind):
				if pendingStart >= 0 && adjacent {
					starts[childIndex] = pendingStart
				}
			default:
				if pendingStart >= 0 && adjacent {
					starts[childIndex] = pendingStart
				}
				pendingStart = -1
			}
			previousEnd = child.endByte
		}
	}

	// Unsupported syntax can make tree-sitter wrap the opening brace and an
	// immediately following KDoc in a recovery node. The comment then has no
	// concrete comment node to participate in the sibling walk above. Recover
	// only the same strict, whitespace-adjacent attachment from the lexical
	// comment stream; annotations included in a declaration naturally remain
	// part of that declaration's start offset.
	targets := make([]int, 0)
	for nodeIndex, node := range tree.nodes {
		if swiftSyntaxDocumentationTarget(node.kind) {
			targets = append(targets, nodeIndex)
		}
	}
	sort.Slice(targets, func(left, right int) bool {
		leftNode, rightNode := tree.nodes[targets[left]], tree.nodes[targets[right]]
		if leftNode.startByte != rightNode.startByte {
			return leftNode.startByte < rightNode.startByte
		}
		return targets[left] < targets[right]
	})
	walkSwiftLexically(source, swiftLexicalSink{
		comment: func(span cByteSpan) bool {
			if !swiftKDocSpan(source, span.start, span.end) {
				return true
			}
			position := sort.Search(len(targets), func(position int) bool {
				return tree.nodes[targets[position]].startByte >= span.end
			})
			if position >= len(targets) {
				return true
			}
			targetIndex := targets[position]
			target := tree.nodes[targetIndex]
			if swiftSourceAttachmentGap(source, span.end, target.startByte) {
				starts[targetIndex] = min(starts[targetIndex], span.start)
			}
			return true
		},
	})
	return starts
}

func swiftSyntaxDocumentationTarget(kind string) bool {
	switch kind {
	case "class_declaration", "protocol_declaration", "function_declaration",
		"protocol_function_declaration", "property_declaration",
		"protocol_property_declaration", "typealias_declaration",
		"associatedtype_declaration", "enum_entry", "init_declaration",
		"deinit_declaration", "subscript_declaration", "macro_declaration",
		"precedence_group_declaration", "operator_declaration":
		return true
	default:
		return false
	}
}

func swiftSyntaxAttachmentBridge(kind string) bool {
	return kind == "attribute" || kind == "modifiers" ||
		strings.HasSuffix(kind, "_modifier")
}

func swiftSyntaxAttachedScopeStart(
	tree *swiftSyntaxTree,
	nodeIndex int,
	starts []int,
) int {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return 0
	}
	start := tree.nodes[nodeIndex].startByte
	if nodeIndex < len(starts) && starts[nodeIndex] >= 0 &&
		starts[nodeIndex] <= tree.nodes[nodeIndex].startByte {
		start = min(start, starts[nodeIndex])
	}
	return start
}

func swiftKDocComment(source string, node swiftSyntaxNode) bool {
	return (node.kind == "comment" || node.kind == "multiline_comment") &&
		swiftKDocSpan(source, node.startByte, node.endByte)
}

func swiftKDocSpan(source string, start, end int) bool {
	if start < 0 || end <= start || end > len(source) {
		return false
	}
	comment := source[start:end]
	if !strings.HasPrefix(comment, "/**") && !strings.HasPrefix(comment, "///") {
		return false
	}
	for position := start; position > 0; {
		value, size := utf8.DecodeLastRuneInString(source[:position])
		if value == utf8.RuneError && size == 1 {
			return false
		}
		position -= size
		switch value {
		case '\n', '\r':
			return true
		case '\uFEFF':
			return position == 0
		case ' ', '\t', '\v', '\f', '\x00':
			continue
		default:
			return false
		}
	}
	return true
}
