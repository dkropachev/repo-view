package repoview

import (
	"sort"
)

type modulaSyntaxNode = treeSitterSyntaxNode
type modulaSyntaxTree = treeSitterSyntaxTree

type modulaParseNode struct {
	kind     string
	children []*modulaParseNode
	start    int
	end      int
}

type modulaConcreteParser struct {
	source string
	tokens []modulaToken
	index  int

	structuralDepth    int
	structuralOverflow bool
}

type modulaDeclarationContext struct {
	definition, local, procedure bool
	phase                        uint8
	exportSeen                   bool
}

type modulaRecordState struct {
	boundary       string
	segmentStart   int
	defaultAllowed bool
}

type modulaTypeBlock struct {
	record *modulaRecordState
	kind   string
}

// parseModulaSyntax builds a strict, position-safe concrete tree without a C
// parser or generated runtime. A gated but malformed unit still returns a
// structurally valid tree containing ERROR nodes; the independent recovery
// pass may then recover declarations only at grammar resynchronization points.
func parseModulaSyntax(
	source string,
	lexed modulaLexResult,
) (*modulaSyntaxTree, bool) {
	if !lexed.concreteEligible || !modulaContentGate(lexed) ||
		len(source) > modulaMaximumConcreteParseBytes ||
		lexed.lexicalUnits > modulaMaximumConcreteTokens {
		return nil, false
	}
	parser := modulaConcreteParser{source: source, tokens: lexed.tokens}
	root := &modulaParseNode{kind: "source_file", start: 0, end: len(source)}
	if unit := parser.parseCompilationUnit(); unit != nil {
		root.children = append(root.children, unit)
	}
	if parser.structuralOverflow {
		return nil, false
	}
	for parser.index < len(parser.tokens) {
		root.children = append(root.children, parser.errorAt(parser.index))
		parser.index++
	}
	tree, ok := modulaFlattenSyntaxTree(root, len(source))
	if !ok || !validateModulaSyntaxTree(tree, len(source)) {
		return nil, false
	}
	return tree, true
}

func validateModulaSyntaxTree(tree *modulaSyntaxTree, sourceLength int) bool {
	if !validateTreeSitterSyntaxTree(tree, sourceLength) {
		return false
	}
	root := tree.nodes[tree.root]
	return root.kind == "source_file" && root.startByte == 0 &&
		root.endByte == sourceLength
}

func modulaFlattenSyntaxTree(
	root *modulaParseNode,
	sourceLength int,
) (*modulaSyntaxTree, bool) {
	if root == nil || sourceLength < 0 {
		return nil, false
	}
	tree := &modulaSyntaxTree{root: 0}
	var appendNode func(*modulaParseNode, int) (int, bool)
	appendNode = func(node *modulaParseNode, parent int) (int, bool) {
		if node == nil || node.kind == "" || node.start < 0 ||
			node.start > node.end || node.end > sourceLength {
			return -1, false
		}
		sort.SliceStable(node.children, func(first, second int) bool {
			left, right := node.children[first], node.children[second]
			if left.start != right.start {
				return left.start < right.start
			}
			return left.end < right.end
		})
		previousEnd := node.start
		for _, child := range node.children {
			if child == nil || child.start < previousEnd || child.start < node.start ||
				child.end > node.end {
				return -1, false
			}
			previousEnd = child.end
		}
		index := len(tree.nodes)
		tree.nodes = append(tree.nodes, modulaSyntaxNode{
			kind: node.kind, startByte: node.start, endByte: node.end, parent: parent,
		})
		for _, child := range node.children {
			childIndex, ok := appendNode(child, index)
			if !ok {
				return -1, false
			}
			tree.nodes[index].children = append(tree.nodes[index].children, childIndex)
		}
		return index, true
	}
	rootIndex, ok := appendNode(root, -1)
	if !ok || rootIndex != 0 {
		return nil, false
	}
	return tree, true
}

func (parser *modulaConcreteParser) parseCompilationUnit() *modulaParseNode {
	if len(parser.tokens) == 0 {
		return nil
	}
	start := parser.current().start
	definition := false
	switch parser.current().text {
	case "DEFINITION":
		definition = true
		parser.index++
		if !parser.consume("MODULE") {
			return parser.errorRange(start, parser.currentStart())
		}
	case "IMPLEMENTATION":
		parser.index++
		if !parser.consume("MODULE") {
			return parser.errorRange(start, parser.currentStart())
		}
	case "MODULE":
		parser.index++
	default:
		return parser.errorAt(parser.index)
	}

	if definition && parser.at("FOR") {
		parser.index++
		if !modulaGNUStringToken(parser.current()) {
			return parser.errorRange(start, parser.currentStart())
		}
		parser.index++
	}
	name, ok := parser.consumeName()
	if !ok {
		return parser.errorRange(start, parser.currentStart())
	}
	unit := &modulaParseNode{
		kind: "module_declaration", start: start, end: name.end,
		children: []*modulaParseNode{modulaIdentifierNode(name)},
	}
	if !definition && parser.at("[") {
		if priorityError := parser.consumeModulePriority(); priorityError != nil {
			unit.children = append(unit.children, priorityError)
		}
	}
	if !parser.consume(";") {
		unit.children = append(unit.children, parser.errorAt(parser.index))
		parser.recoverTo(";", "END")
		parser.consume(";")
	}
	parser.parseModuleDeclarations(unit, modulaDeclarationContext{definition: definition})
	if parser.at("BEGIN") {
		unit.children = append(unit.children, parser.parseStatementBlock(true))
	} else if !definition && parser.at("FINALLY") {
		unit.children = append(unit.children, parser.parseStatementBlock(true))
	}
	matched, end := parser.parseOwnerEnd(unit, name.text, ".")
	unit.end = max(unit.end, end)
	if !matched {
		unit.kind = "module_declaration_mismatched_end"
	}
	return unit
}

func (parser *modulaConcreteParser) parseModuleDeclarations(
	parent *modulaParseNode,
	context modulaDeclarationContext,
) {
	for parser.index < len(parser.tokens) && !parser.at("END") &&
		(!parser.at("BEGIN") || context.definition) &&
		(!parser.at("FINALLY") || context.definition || context.procedure) &&
		(!context.procedure || !parser.at("FINALLY") && !parser.at("EXCEPT")) {
		switch parser.current().text {
		case "IMPORT", "FROM":
			node := parser.parseImport()
			if context.procedure || context.phase != 0 {
				node.kind = "ERROR"
				node.children = nil
			}
			if node.kind == "ERROR" {
				context.phase = 2
			}
			parent.children = append(parent.children, node)
		case "EXPORT":
			node := parser.parseExport()
			if context.procedure || !context.definition && !context.local ||
				context.phase > 1 || context.exportSeen {
				node.kind = "ERROR"
				node.children = nil
			}
			context.exportSeen = true
			context.phase = max(context.phase, 1)
			parent.children = append(parent.children, node)
		case "CONST":
			context.phase = 2
			parser.index++
			parser.parseConstantSection(parent)
		case "TYPE":
			context.phase = 2
			parser.index++
			parser.parseTypeSection(parent, context.definition)
		case "VAR":
			context.phase = 2
			parser.index++
			parser.parseVariableSection(parent)
		case "PROCEDURE":
			context.phase = 2
			parent.children = append(parent.children, parser.parseProcedure(context.definition))
		case "MODULE":
			context.phase = 2
			node := parser.parseLocalModule()
			if context.definition {
				node.kind = "ERROR"
				node.children = nil
			}
			parent.children = append(parent.children, node)
		case "DEFINITION", "IMPLEMENTATION":
			context.phase = 2
			parent.children = append(parent.children, parser.skipInvalidNestedUnit())
		case "<*":
			start := parser.index
			end := modulaDirectiveRangeEnd(parser.tokens, start, len(parser.tokens))
			if end < 0 {
				restart := modulaUnmatchedDirectiveRestart(
					parser.tokens, start+1,
				)
				errorEnd := len(parser.source)
				parser.index = len(parser.tokens)
				if restart >= 0 {
					errorEnd = parser.tokens[restart].start
					parser.index = restart
				}
				parent.children = append(parent.children, parser.errorRange(
					parser.tokens[start].start, errorEnd,
				))
			} else {
				parent.children = append(parent.children, parser.errorRange(
					parser.tokens[start].start, parser.tokens[end-1].end,
				))
				parser.index = end
			}
		case "BEGIN", "FINALLY":
			context.phase = 2
			// A definition module has no statement block. Consume the invalid
			// block as one recovery node and resume at the following definition.
			parent.children = append(parent.children, parser.skipInvalidBeginBlock())
		default:
			context.phase = 2
			parent.children = append(parent.children, parser.errorAt(parser.index))
			parser.recoverDeclarationBoundary()
		}
	}
}

func (parser *modulaConcreteParser) skipInvalidNestedUnit() *modulaParseNode {
	startIndex := parser.index
	start := parser.current().start
	semicolon, restart := parser.findDeclarationSemicolonBoundary(startIndex+1, false)
	if restart >= 0 {
		parser.index = restart
		return parser.errorRange(start, parser.currentStart())
	}
	if semicolon < 0 {
		parser.index = len(parser.tokens)
		return parser.errorRange(start, len(parser.source))
	}
	header := parser.tokens[startIndex:semicolon]
	nameIndex, _, valid := modulaRecoveryModuleName(header)
	end := parser.tokens[semicolon].end
	parser.index = semicolon + 1
	if valid && nameIndex >= 0 && nameIndex < len(header) {
		end = parser.skipInvalidNestedUnitEnd(header[nameIndex].text, end)
	}
	return parser.errorRange(start, end)
}

func (parser *modulaConcreteParser) skipInvalidNestedUnitEnd(
	name string,
	minimumEnd int,
) int {
	for parser.index+2 < len(parser.tokens) {
		if parser.at("<*") {
			if directiveEnd := modulaDirectiveRangeEnd(
				parser.tokens, parser.index, len(parser.tokens),
			); directiveEnd >= 0 {
				parser.index = directiveEnd
				continue
			}
		}
		if parser.tokens[parser.index].text == "END" &&
			parser.tokens[parser.index+1].text == name &&
			(parser.tokens[parser.index+2].text == ";" ||
				parser.tokens[parser.index+2].text == ".") {
			end := parser.tokens[parser.index+2].end
			parser.index += 3
			return end
		}
		parser.index++
	}
	return minimumEnd
}

func (parser *modulaConcreteParser) parseConstantSection(parent *modulaParseNode) {
	for parser.currentIsDeclarationName() {
		name, _ := parser.consumeName()
		declaration := &modulaParseNode{
			kind: "constant_declaration", start: name.start, end: name.end,
			children: []*modulaParseNode{modulaIdentifierNode(name)},
		}
		if !parser.consume("=") {
			declaration.kind = "ERROR"
			declaration.children = nil
			parser.recoverTo(";", "END")
		} else {
			rhsStart := parser.index
			semicolon, restart := parser.findDeclarationBoundary(parser.index, false)
			switch {
			case restart >= 0:
				declaration.kind = "ERROR"
				declaration.children = nil
				parser.index = restart
			case semicolon < 0 || semicolon == rhsStart ||
				!modulaConstantExpressionRangeValid(
					parser.tokens, rhsStart, semicolon,
				):
				declaration.kind = "ERROR"
				declaration.children = nil
				parser.recoverTo(";", "END")
			default:
				parser.index = semicolon
			}
		}
		if parser.at(";") {
			declaration.end = parser.current().end
			parser.index++
		} else {
			declaration.end = max(declaration.end, parser.currentStart())
		}
		parent.children = append(parent.children, declaration)
		if modulaDeclarationSectionStarter(parser.currentText()) {
			return
		}
	}
}

func modulaConstantExpressionRangeValid(
	tokens []modulaToken,
	start, end int,
) bool {
	return modulaConstantExpressionRangeError(tokens, start, end) < 0
}

func modulaConstantExpressionRangeError(
	tokens []modulaToken,
	start, end int,
) int {
	parser := modulaExpressionParser{
		tokens: tokens, start: start, index: start, end: end,
		constant: true, errorIndex: -1,
	}
	if parser.validRange(false) {
		return -1
	}
	return parser.offendingIndex()
}

type modulaExpressionParser struct {
	tokens            []modulaToken
	start, index, end int
	depth, errorIndex int
	constant          bool
}

func modulaExpressionRangeError(tokens []modulaToken, start, end int) int {
	parser := modulaExpressionParser{
		tokens: tokens, start: start, index: start, end: end, errorIndex: -1,
	}
	if parser.validRange(false) {
		return -1
	}
	return parser.offendingIndex()
}

func modulaCaseLabelRangeError(tokens []modulaToken, start, end int) int {
	parser := modulaExpressionParser{
		tokens: tokens, start: start, index: start, end: end,
		constant: true, errorIndex: -1,
	}
	if parser.validRange(true) {
		return -1
	}
	return parser.offendingIndex()
}

func modulaStatementRangeError(tokens []modulaToken, start, end int) int {
	parser := modulaExpressionParser{
		tokens: tokens, start: start, index: start, end: end, errorIndex: -1,
	}
	if start < 0 || start >= end || end > len(tokens) {
		parser.failAt(start)
		return parser.offendingIndex()
	}
	switch tokens[start].text {
	case "EXIT", "RETRY":
		if end == start+1 {
			return -1
		}
		parser.failAt(start + 1)
		return parser.offendingIndex()
	case "RETURN":
		if end == start+1 {
			return -1
		}
		return modulaExpressionRangeError(tokens, start+1, end)
	case "ASM":
		return modulaASMStatementRangeError(tokens, start, end)
	}

	if !parser.parseDesignator() {
		return parser.offendingIndex()
	}
	if parser.index == end {
		return -1
	}
	switch parser.currentText() {
	case ":=":
		parser.index++
		if !parser.parseExpression() {
			return parser.offendingIndex()
		}
	case "(":
		if !parser.parseExpressionList("(", ")", true) {
			return parser.offendingIndex()
		}
	default:
		parser.failAt(parser.index)
		return parser.offendingIndex()
	}
	if parser.index != end {
		parser.failAt(parser.index)
		return parser.offendingIndex()
	}
	return -1
}

func modulaDesignatorRangeError(tokens []modulaToken, start, end int) int {
	parser := modulaExpressionParser{
		tokens: tokens, start: start, index: start, end: end, errorIndex: -1,
	}
	if start < 0 || start >= end || end > len(tokens) ||
		!parser.parseDesignator() {
		parser.failAt(start)
		return parser.offendingIndex()
	}
	if parser.index != end {
		parser.failAt(parser.index)
		return parser.offendingIndex()
	}
	return -1
}

func modulaForHeaderRangeError(tokens []modulaToken, start, end int) int {
	parser := modulaExpressionParser{
		tokens: tokens, start: start, index: start, end: end, errorIndex: -1,
	}
	if start < 0 || start >= end || end > len(tokens) ||
		tokens[start].gap || tokens[start].kind != modulaTokenIdentifier ||
		modulaKeyword(tokens[start].text) {
		parser.failAt(start)
		return parser.offendingIndex()
	}
	parser.index++
	if !parser.consume(":=") {
		parser.failAt(parser.index)
		return parser.offendingIndex()
	}
	if !parser.parseExpression() {
		return parser.offendingIndex()
	}
	if !parser.consume("TO") {
		parser.failAt(parser.index)
		return parser.offendingIndex()
	}
	if !parser.parseExpression() {
		return parser.offendingIndex()
	}
	if parser.consume("BY") {
		constant := modulaExpressionParser{
			tokens: tokens, start: parser.index, index: parser.index, end: end,
			constant: true, errorIndex: -1,
		}
		if !constant.parseExpression() {
			return constant.offendingIndex()
		}
		parser.index = constant.index
	}
	if parser.index != end {
		parser.failAt(parser.index)
		return parser.offendingIndex()
	}
	return -1
}

func modulaASMStatementRangeError(tokens []modulaToken, start, end int) int {
	parser := modulaExpressionParser{
		tokens: tokens, start: start, index: start, end: end, errorIndex: -1,
	}
	parser.index++
	parser.consume("VOLATILE")
	if !parser.consume("(") {
		parser.failAt(parser.index)
		return parser.offendingIndex()
	}
	opening := parser.index - 1
	closing := modulaRecoveryMatchingDelimiter(
		tokens[opening:end], 0, "(", ")",
	)
	if closing < 0 {
		parser.failAt(opening)
		return parser.offendingIndex()
	}
	closing += opening
	if closing != end-1 {
		parser.failAt(closing + 1)
		return parser.offendingIndex()
	}

	payloadStart, payloadEnd := opening+1, closing
	colons, invalid := modulaTopLevelSeparators(
		tokens, payloadStart, payloadEnd, ":",
	)
	if invalid >= 0 {
		parser.failAt(invalid)
		return parser.offendingIndex()
	}
	if len(colons) > 3 {
		parser.failAt(colons[3])
		return parser.offendingIndex()
	}
	bounds := make([]int, 0, len(colons)+2)
	bounds = append(bounds, payloadStart)
	bounds = append(bounds, colons...)
	bounds = append(bounds, payloadEnd)
	if errorIndex := modulaConstantExpressionRangeError(
		tokens, bounds[0], bounds[1],
	); errorIndex >= 0 {
		return errorIndex
	}
	for group := 1; group < len(bounds)-1; group++ {
		groupStart := bounds[group] + 1
		groupEnd := bounds[group+1]
		var errorIndex int
		if group < 3 {
			errorIndex = modulaASMListRangeError(tokens, groupStart, groupEnd)
		} else {
			errorIndex = modulaASMTrashRangeError(tokens, groupStart, groupEnd)
		}
		if errorIndex >= 0 {
			return errorIndex
		}
	}
	return -1
}

func modulaASMListRangeError(tokens []modulaToken, start, end int) int {
	if start == end {
		return -1
	}
	commas, invalid := modulaTopLevelSeparators(tokens, start, end, ",")
	if invalid >= 0 {
		return invalid
	}
	segmentStart := start
	for index, comma := range commas {
		if segmentStart == comma {
			// The grammar's optional first element permits one leading comma,
			// after which every repetition must contain an AsmElement.
			if index != 0 || comma != start {
				return comma
			}
		} else if errorIndex := modulaASMElementRangeError(
			tokens, segmentStart, comma,
		); errorIndex >= 0 {
			return errorIndex
		}
		segmentStart = comma + 1
	}
	if segmentStart >= end {
		return max(start, end-1)
	}
	return modulaASMElementRangeError(tokens, segmentStart, end)
}

func modulaASMElementRangeError(tokens []modulaToken, start, end int) int {
	parser := modulaExpressionParser{
		tokens: tokens, start: start, index: start, end: end, errorIndex: -1,
	}
	if start < 0 || start >= end || end > len(tokens) {
		parser.failAt(start)
		return parser.offendingIndex()
	}
	constraintStart := start
	if tokens[constraintStart].text == "[" {
		if constraintStart+2 >= end || tokens[constraintStart+1].gap ||
			tokens[constraintStart+1].kind != modulaTokenIdentifier ||
			modulaKeyword(tokens[constraintStart+1].text) ||
			tokens[constraintStart+2].text != "]" {
			parser.failAt(constraintStart)
			return parser.offendingIndex()
		}
		constraintStart += 3
	}
	if constraintStart >= end || tokens[end-1].text != ")" {
		parser.failAt(max(constraintStart, end-1))
		return parser.offendingIndex()
	}
	depth, operandOpen := 0, -1
	for index := end - 1; index >= constraintStart; index-- {
		switch tokens[index].text {
		case ")":
			depth++
		case "(":
			depth--
			if depth == 0 {
				operandOpen = index
				index = constraintStart - 1
			}
		}
	}
	if operandOpen <= constraintStart {
		parser.failAt(max(constraintStart, operandOpen))
		return parser.offendingIndex()
	}
	if errorIndex := modulaConstantExpressionRangeError(
		tokens, constraintStart, operandOpen,
	); errorIndex >= 0 {
		return errorIndex
	}
	return modulaExpressionRangeError(tokens, operandOpen+1, end-1)
}

func modulaASMTrashRangeError(tokens []modulaToken, start, end int) int {
	if start == end {
		return -1
	}
	commas, invalid := modulaTopLevelSeparators(tokens, start, end, ",")
	if invalid >= 0 {
		return invalid
	}
	segmentStart := start
	for index, comma := range commas {
		if segmentStart == comma {
			if index != 0 || comma != start {
				return comma
			}
		} else if errorIndex := modulaConstantExpressionRangeError(
			tokens, segmentStart, comma,
		); errorIndex >= 0 {
			return errorIndex
		}
		segmentStart = comma + 1
	}
	if segmentStart >= end {
		return max(start, end-1)
	}
	if errorIndex := modulaConstantExpressionRangeError(
		tokens, segmentStart, end,
	); errorIndex >= 0 {
		return errorIndex
	}
	return -1
}

func modulaTopLevelSeparators(
	tokens []modulaToken,
	start, end int,
	separator string,
) ([]int, int) {
	if start < 0 || start > end || end > len(tokens) {
		return nil, max(start, 0)
	}
	openings := make([]int, 0, 8)
	expected := make([]string, 0, 8)
	separators := make([]int, 0, 4)
	for index := start; index < end; index++ {
		switch tokens[index].text {
		case "(":
			openings = append(openings, index)
			expected = append(expected, ")")
		case "[":
			openings = append(openings, index)
			expected = append(expected, "]")
		case "{":
			openings = append(openings, index)
			expected = append(expected, "}")
		case ")", "]", "}":
			if len(expected) == 0 || expected[len(expected)-1] != tokens[index].text {
				return nil, index
			}
			openings = openings[:len(openings)-1]
			expected = expected[:len(expected)-1]
		default:
			if len(expected) == 0 && tokens[index].text == separator {
				separators = append(separators, index)
			}
		}
		if len(expected) > modulaMaximumStructuralDepth {
			return nil, index
		}
	}
	if len(openings) > 0 {
		return nil, openings[0]
	}
	return separators, -1
}

func (parser *modulaExpressionParser) validRange(caseLabels bool) bool {
	if parser.start < 0 || parser.start >= parser.end ||
		parser.end > len(parser.tokens) {
		parser.failAt(parser.start)
		return false
	}
	var valid bool
	if caseLabels {
		valid = parser.parseCaseLabel()
	} else {
		valid = parser.parseExpression()
	}
	if caseLabels && valid {
		for parser.consume(",") {
			if !parser.parseCaseLabel() {
				valid = false
				break
			}
		}
	}
	if valid && parser.index != parser.end {
		parser.failAt(parser.index)
		valid = false
	}
	return valid
}

func (parser *modulaExpressionParser) parseCaseLabel() bool {
	if !parser.parseExpression() {
		return false
	}
	if parser.consume("..") {
		return parser.parseExpression()
	}
	return true
}

func (parser *modulaExpressionParser) parseExpression() bool {
	if !parser.parseSimpleExpression() {
		return false
	}
	if modulaRelationOperator(parser.currentText()) {
		parser.index++
		return parser.parseSimpleExpression()
	}
	return true
}

func (parser *modulaExpressionParser) parseSimpleExpression() bool {
	if parser.at("+") || parser.at("-") {
		parser.index++
	}
	if !parser.parseTerm() {
		return false
	}
	for modulaAddOperator(parser.currentText()) {
		parser.index++
		if !parser.parseTerm() {
			return false
		}
	}
	return true
}

func (parser *modulaExpressionParser) parseTerm() bool {
	if !parser.parseFactor() {
		return false
	}
	for modulaMultiplyOperator(parser.currentText()) {
		parser.index++
		if !parser.parseFactor() {
			return false
		}
	}
	return true
}

func (parser *modulaExpressionParser) parseFactor() bool {
	if parser.index >= parser.end {
		return parser.failAt(parser.index)
	}
	token := parser.tokens[parser.index]
	if token.gap || token.text == "<*" || token.text == "*>" {
		return parser.failAt(parser.index)
	}
	if token.kind == modulaTokenNumber || token.text == modulaLiteralToken {
		parser.index++
		return true
	}
	if token.kind == modulaTokenIdentifier && !modulaKeyword(token.text) {
		return parser.parseNamedFactor()
	}
	switch token.text {
	case "NOT":
		if !parser.enterDepth() {
			return false
		}
		parser.index++
		valid := parser.parseFactor()
		parser.depth--
		return valid
	case "(":
		if !parser.enterDepth() {
			return false
		}
		parser.index++
		valid := parser.parseExpression() && parser.require(")")
		parser.depth--
		return valid
	case "{":
		return parser.parseConstructor()
	case "__ATTRIBUTE__":
		return parser.parseConstAttribute()
	case "__COLUMN__", "__DATE__", "__FILE__", "__FUNCTION__", "__LINE__":
		parser.index++
		return true
	default:
		return parser.failAt(parser.index)
	}
}

func (parser *modulaExpressionParser) parseNamedFactor() bool {
	if !parser.parseQualident() {
		return false
	}
	if parser.at("{") {
		return parser.parseConstructor()
	}
	if !parser.constant {
		for {
			switch parser.currentText() {
			case "[":
				if !parser.parseExpressionList("[", "]", false) {
					return false
				}
			case "^":
				parser.index++
			case ".":
				parser.index++
				if !parser.requireName() {
					return false
				}
			default:
				goto selectorsDone
			}
		}
	}

selectorsDone:
	if parser.at("(") {
		constant := parser.constant
		parser.constant = false
		valid := parser.parseExpressionList("(", ")", true)
		parser.constant = constant
		return valid
	}
	return true
}

func (parser *modulaExpressionParser) parseDesignator() bool {
	if !parser.parseQualident() {
		return false
	}
	for {
		switch parser.currentText() {
		case "[":
			constant := parser.constant
			parser.constant = false
			valid := parser.parseExpressionList("[", "]", false)
			parser.constant = constant
			if !valid {
				return false
			}
		case "^":
			parser.index++
		case ".":
			parser.index++
			if !parser.requireName() {
				return false
			}
		default:
			return true
		}
	}
}

func (parser *modulaExpressionParser) parseExpressionList(
	open, closing string,
	emptyAllowed bool,
) bool {
	if !parser.at(open) || !parser.enterDepth() {
		return false
	}
	parser.index++
	if parser.consume(closing) {
		parser.depth--
		if emptyAllowed {
			return true
		}
		return parser.failAt(parser.index - 1)
	}
	if !parser.parseExpression() {
		return false
	}
	for parser.consume(",") {
		if !parser.parseExpression() {
			return false
		}
	}
	if !parser.require(closing) {
		return false
	}
	parser.depth--
	return true
}

func (parser *modulaExpressionParser) parseConstructor() bool {
	if !parser.at("{") || !parser.enterDepth() {
		return false
	}
	constant := parser.constant
	parser.constant = true
	defer func() { parser.constant = constant }()
	parser.index++
	if parser.consume("}") {
		parser.depth--
		return true
	}
	for {
		if !parser.parseExpression() {
			return false
		}
		if parser.consume("..") && !parser.parseExpression() {
			return false
		}
		if parser.consume("BY") && !parser.parseExpression() {
			return false
		}
		if !parser.consume(",") {
			break
		}
	}
	if !parser.require("}") {
		return false
	}
	parser.depth--
	return true
}

func (parser *modulaExpressionParser) parseConstAttribute() bool {
	parser.index++
	if !parser.require("__BUILTIN__") || !parser.require("(") {
		return false
	}
	if !parser.enterDepth() || !parser.require("(") {
		return false
	}
	if !parser.enterDepth() {
		return false
	}
	if parser.consume("<") {
		if !parser.parseQualident() || !parser.require(",") ||
			!parser.requireName() || !parser.require(">") {
			return false
		}
	} else if !parser.requireName() {
		return false
	}
	if !parser.require(")") {
		return false
	}
	parser.depth--
	if !parser.require(")") {
		return false
	}
	parser.depth--
	return true
}

func (parser *modulaExpressionParser) parseQualident() bool {
	if !parser.requireName() {
		return false
	}
	for parser.consume(".") {
		if !parser.requireName() {
			return false
		}
	}
	return true
}

func (parser *modulaExpressionParser) requireName() bool {
	if parser.index >= parser.end || parser.tokens[parser.index].gap ||
		parser.tokens[parser.index].kind != modulaTokenIdentifier ||
		modulaKeyword(parser.tokens[parser.index].text) {
		return parser.failAt(parser.index)
	}
	parser.index++
	return true
}

func (parser *modulaExpressionParser) require(text string) bool {
	if !parser.consume(text) {
		return parser.failAt(parser.index)
	}
	return true
}

func (parser *modulaExpressionParser) consume(text string) bool {
	if !parser.at(text) {
		return false
	}
	parser.index++
	return true
}

func (parser *modulaExpressionParser) at(text string) bool {
	return parser.index < parser.end && parser.tokens[parser.index].text == text
}

func (parser *modulaExpressionParser) currentText() string {
	if parser.index >= parser.end {
		return ""
	}
	return parser.tokens[parser.index].text
}

func (parser *modulaExpressionParser) enterDepth() bool {
	if parser.depth >= modulaMaximumStructuralDepth {
		return parser.failAt(parser.index)
	}
	parser.depth++
	return true
}

func (parser *modulaExpressionParser) failAt(index int) bool {
	if parser.errorIndex < 0 {
		parser.errorIndex = index
	}
	return false
}

func (parser *modulaExpressionParser) offendingIndex() int {
	if len(parser.tokens) == 0 {
		return 0
	}
	if parser.errorIndex >= 0 && parser.errorIndex < len(parser.tokens) {
		return parser.errorIndex
	}
	if parser.end > 0 {
		return min(parser.end-1, len(parser.tokens)-1)
	}
	return min(max(parser.start, 0), len(parser.tokens)-1)
}

func modulaRelationOperator(text string) bool {
	switch text {
	case "=", "#", "<>", "<", "<=", ">", ">=", "IN":
		return true
	default:
		return false
	}
}

func modulaAddOperator(text string) bool {
	switch text {
	case "+", "-", "OR":
		return true
	default:
		return false
	}
}

func modulaMultiplyOperator(text string) bool {
	switch text {
	case "*", "/", "DIV", "MOD", "REM", "AND", "&":
		return true
	default:
		return false
	}
}

func (parser *modulaConcreteParser) parseTypeSection(
	parent *modulaParseNode,
	definition bool,
) {
	for parser.currentIsDeclarationName() {
		name, _ := parser.consumeName()
		declaration := &modulaParseNode{
			kind: "type_declaration", start: name.start, end: name.end,
			children: []*modulaParseNode{modulaIdentifierNode(name)},
		}
		if !parser.consume("=") {
			if !definition || !parser.at(";") {
				declaration.kind = "ERROR"
				declaration.children = nil
				semicolon, restart := parser.findDeclarationSemicolonBoundary(
					parser.index, true,
				)
				switch {
				case restart >= 0:
					parser.index = restart
				case semicolon >= 0:
					parser.index = semicolon
				default:
					parser.index = len(parser.tokens)
				}
			}
		} else {
			rhsStart := parser.index
			semicolon, restart := parser.findDeclarationBoundary(rhsStart, true)
			switch {
			case restart >= 0:
				declaration.kind = "ERROR"
				declaration.children = nil
				parser.index = restart
			case semicolon < 0 || semicolon == rhsStart ||
				!modulaTypeRangeValid(parser.tokens, rhsStart, semicolon):
				declaration.kind = "ERROR"
				declaration.children = nil
				if semicolon >= 0 {
					parser.index = semicolon
				} else {
					parser.recoverTo(";", "END")
				}
			default:
				if modulaTypeRangeContainsRecord(parser.tokens, rhsStart, semicolon) {
					declaration.kind = "record_type_declaration"
				}
				declaration.children = append(
					declaration.children,
					modulaTypeMemberNodes(parser.tokens, rhsStart, semicolon)...,
				)
				parser.index = semicolon
			}
		}
		if parser.at(";") {
			declaration.end = parser.current().end
			parser.index++
		} else {
			declaration.end = max(declaration.end, parser.currentStart())
		}
		parent.children = append(parent.children, declaration)
		if modulaDeclarationSectionStarter(parser.currentText()) {
			return
		}
	}
}

func (parser *modulaConcreteParser) parseVariableSection(parent *modulaParseNode) {
	for parser.currentIsDeclarationName() {
		startIndex := parser.index
		colon := parser.findTopLevelToken(parser.index, ":", ";")
		if colon < 0 {
			parent.children = append(parent.children, parser.errorAt(parser.index))
			semicolon, restart := parser.findDeclarationSemicolonBoundary(
				startIndex, false,
			)
			switch {
			case restart >= 0:
				parser.index = restart
			case semicolon >= 0:
				parser.index = semicolon + 1
			default:
				parser.index = len(parser.tokens)
			}
			continue
		}
		names, ok := modulaDeclaratorNames(parser.tokens, startIndex, colon)
		semicolon, restart := parser.findDeclarationBoundary(colon+1, true)
		if restart >= 0 {
			parent.children = append(parent.children, parser.errorAt(startIndex))
			parser.index = restart
			continue
		}
		if !ok || len(names) == 0 || semicolon < 0 || semicolon == colon+1 ||
			!modulaTypeRangeValid(parser.tokens, colon+1, semicolon) {
			parent.children = append(parent.children, parser.errorAt(startIndex))
			if semicolon >= 0 {
				parser.index = semicolon
			} else {
				parser.recoverTo(";", "END")
			}
			parser.consume(";")
			continue
		}
		declaration := &modulaParseNode{
			kind: "variable_declaration", start: names[0].start,
			end: parser.tokens[semicolon].end,
		}
		for _, name := range names {
			declaration.children = append(declaration.children, modulaIdentifierNode(name))
		}
		declaration.children = append(
			declaration.children,
			modulaTypeMemberNodes(parser.tokens, colon+1, semicolon)...,
		)
		parent.children = append(parent.children, declaration)
		parser.index = semicolon + 1
		if modulaDeclarationSectionStarter(parser.currentText()) {
			return
		}
	}
}

func (parser *modulaConcreteParser) parseProcedure(
	definition bool,
) *modulaParseNode {
	headingStart := parser.index
	startToken := parser.current()
	parser.index++
	semicolon, restart := parser.findDeclarationSemicolonBoundary(parser.index, false)
	if restart >= 0 {
		parser.index = restart
		return parser.errorRange(startToken.start, parser.currentStart())
	}
	if semicolon < 0 {
		parser.index = len(parser.tokens)
		return parser.errorRange(startToken.start, len(parser.source))
	}
	heading := parser.tokens[headingStart:semicolon]
	nameIndex, validName := modulaRecoveryProcedureName(heading, definition)
	rawName, hasRawName := modulaRawProcedureName(heading)
	validHeading := validName &&
		modulaRecoveryProcedureHeadingValid(heading, nameIndex, definition)
	parser.index = semicolon + 1
	if !validHeading {
		end := parser.tokens[semicolon].end
		if !definition && hasRawName {
			end = parser.skipMalformedNamedOwner(rawName.text, end)
		}
		return parser.errorRange(startToken.start, end)
	}
	name := heading[nameIndex]
	procedure := &modulaParseNode{
		kind: "procedure_declaration", start: startToken.start,
		end:      parser.tokens[semicolon].end,
		children: []*modulaParseNode{modulaIdentifierNode(name)},
	}
	procedure.end = parser.tokens[semicolon].end
	if definition {
		return procedure
	}
	if parser.at("FORWARD") {
		forwardStart := parser.index
		parser.index++
		next, restart := parser.findDeclarationSemicolonBoundary(parser.index, false)
		if next >= 0 {
			if next != parser.index {
				procedure.children = append(
					procedure.children,
					parser.errorRange(
						parser.tokens[parser.index].start,
						parser.tokens[next].end,
					),
				)
			}
			procedure.end = parser.tokens[next].end
			parser.index = next + 1
		} else {
			procedure.children = append(procedure.children, parser.errorAt(forwardStart))
			if restart >= 0 {
				parser.index = restart
			}
		}
		procedure.kind = "procedure_signature_declaration"
		return procedure
	}
	if parser.structuralDepth >= modulaMaximumStructuralDepth {
		parser.structuralOverflow = true
		parser.index = len(parser.tokens)
		return procedure
	}
	parser.structuralDepth++
	defer func() { parser.structuralDepth-- }()
	parser.parseModuleDeclarations(procedure, modulaDeclarationContext{procedure: true})
	if parser.at("BEGIN") {
		procedure.children = append(procedure.children, parser.parseStatementBlock(false))
	} else if parser.at("FINALLY") || parser.at("EXCEPT") {
		procedure.children = append(procedure.children, parser.parseStatementBlock(false))
	}
	matched, end := parser.parseOwnerEnd(procedure, name.text, ";")
	procedure.end = max(procedure.end, end)
	if !matched {
		procedure.kind = "procedure_declaration_mismatched_end"
	}
	return procedure
}

func (parser *modulaConcreteParser) parseLocalModule() *modulaParseNode {
	startIndex := parser.index
	start := parser.current().start
	parser.index++
	name, ok := parser.consumeName()
	if !ok {
		semicolon, restart := parser.findDeclarationSemicolonBoundary(
			parser.index, false,
		)
		if restart >= 0 {
			parser.index = restart
			return parser.errorRange(start, parser.currentStart())
		}
		if semicolon < 0 {
			parser.index = len(parser.tokens)
			return parser.errorRange(start, len(parser.source))
		}
		header := parser.tokens[startIndex:semicolon]
		rawName, hasRawName := modulaRawLocalModuleName(header)
		end := parser.tokens[semicolon].end
		parser.index = semicolon + 1
		if hasRawName {
			end = parser.skipInvalidNestedUnitEnd(rawName.text, end)
		}
		return parser.errorRange(start, end)
	}
	module := &modulaParseNode{
		kind: "module_declaration", start: start, end: name.end,
		children: []*modulaParseNode{modulaIdentifierNode(name)},
	}
	if parser.at("[") {
		if priorityError := parser.consumeModulePriority(); priorityError != nil {
			module.children = append(module.children, priorityError)
		}
	}
	if !parser.consume(";") {
		module.children = append(module.children, parser.errorAt(parser.index))
		parser.recoverTo(";", "END")
		parser.consume(";")
	}
	if parser.structuralDepth >= modulaMaximumStructuralDepth {
		parser.structuralOverflow = true
		parser.index = len(parser.tokens)
		return module
	}
	parser.structuralDepth++
	defer func() { parser.structuralDepth-- }()
	parser.parseModuleDeclarations(module, modulaDeclarationContext{local: true})
	if parser.at("BEGIN") {
		module.children = append(module.children, parser.parseStatementBlock(true))
	} else if parser.at("FINALLY") {
		module.children = append(module.children, parser.parseStatementBlock(true))
	}
	matched, end := parser.parseOwnerEnd(module, name.text, ";")
	module.end = max(module.end, end)
	if !matched {
		module.kind = "module_declaration_mismatched_end"
	}
	return module
}

func (parser *modulaConcreteParser) parseImport() *modulaParseNode {
	startIndex := parser.index
	start := parser.current().start
	valid := true
	if parser.at("FROM") {
		parser.index++
		if _, ok := parser.consumeName(); !ok || !parser.consume("IMPORT") {
			valid = false
		}
	} else {
		parser.index++
	}
	listStart := parser.index
	semicolon, restart := parser.findDeclarationSemicolonBoundary(listStart, false)
	if restart >= 0 {
		parser.index = restart
		return parser.errorRange(start, max(parser.currentStart(), parser.tokens[startIndex].end))
	}
	if semicolon < 0 {
		parser.index = len(parser.tokens)
		return parser.errorRange(start, len(parser.source))
	}
	valid = valid && modulaImportListValid(parser.tokens, listStart, semicolon)
	end := parser.tokens[semicolon].end
	parser.index = semicolon + 1
	if !valid {
		return parser.errorRange(start, max(end, parser.tokens[startIndex].end))
	}
	return &modulaParseNode{kind: "import_clause", start: start, end: end}
}

func (parser *modulaConcreteParser) parseExport() *modulaParseNode {
	start := parser.current().start
	parser.index++
	if parser.at("QUALIFIED") || parser.at("UNQUALIFIED") {
		parser.index++
	}
	listStart := parser.index
	semicolon, restart := parser.findDeclarationSemicolonBoundary(listStart, false)
	if restart >= 0 {
		parser.index = restart
		return parser.errorRange(start, parser.currentStart())
	}
	if semicolon < 0 {
		parser.index = len(parser.tokens)
		return parser.errorRange(start, len(parser.source))
	}
	if !modulaImportListValid(parser.tokens, listStart, semicolon) {
		end := parser.tokens[semicolon].end
		parser.index = semicolon + 1
		return parser.errorRange(start, end)
	}
	end := parser.tokens[semicolon].end
	parser.index = semicolon + 1
	return &modulaParseNode{kind: "export_clause", start: start, end: end}
}

func (parser *modulaConcreteParser) parseStatementBlock(
	allowFinally bool,
) *modulaParseNode {
	begin := parser.current()
	parser.index++
	block := &modulaParseNode{kind: "block", start: begin.start, end: begin.end}
	if begin.text == "EXCEPT" || begin.text == "FINALLY" && !allowFinally {
		block.kind = "invalid_block"
		block.children = append(
			block.children,
			parser.errorRange(begin.start, begin.end),
		)
	}
	type openBlock struct {
		node           *modulaParseNode
		kind           string
		required       string
		repeat         bool
		elseSeen       bool
		caseNeedsLabel bool
		headerTokens   int
		headerStart    int
	}
	stack := make([]openBlock, 0, 8)
	delimiters := make([]modulaToken, 0, 8)
	type statementErrorKey struct {
		parent     *modulaParseNode
		start, end int
	}
	seenStatementErrors := make(map[statementErrorKey]struct{}, 8)
	var previous modulaToken
	statementActive := false
	statementTokens := 0
	statementAssign := false
	statementStartIndex := -1
	repeatNeedsExpression := false
	repeatCondition := false
	var repeatConditionNode *modulaParseNode
	inFinal := begin.text == "FINALLY"
	exceptSeen := false
	appendStatementError := func(node *modulaParseNode) {
		var children *[]*modulaParseNode
		if len(stack) == 0 {
			children = &block.children
		} else {
			children = &stack[len(stack)-1].node.children
		}
		if node.kind == "ERROR" {
			parent := block
			if len(stack) > 0 {
				parent = stack[len(stack)-1].node
			}
			key := statementErrorKey{
				parent: parent, start: node.start, end: node.end,
			}
			if _, exists := seenStatementErrors[key]; exists {
				return
			}
			seenStatementErrors[key] = struct{}{}
		}
		*children = append(*children, node)
	}
	markCurrentControlError := func(token modulaToken) {
		if len(stack) == 0 {
			appendStatementError(parser.errorRange(token.start, token.end))
			return
		}
		current := &stack[len(stack)-1]
		current.node.kind = "invalid_block"
		current.node.children = append(
			current.node.children,
			parser.errorRange(token.start, token.end),
		)
	}
	finishStatement := func() {
		expressionContext := repeatCondition || len(stack) > 0 &&
			(stack[len(stack)-1].required != "" ||
				stack[len(stack)-1].caseNeedsLabel)
		validateExpression := func(start, end, emptyFallback int) {
			if start >= end {
				appendStatementError(parser.errorAt(emptyFallback))
				return
			}
			if errorIndex := modulaExpressionRangeError(
				parser.tokens, start, end,
			); errorIndex >= 0 {
				appendStatementError(parser.errorAt(errorIndex))
			}
		}
		if len(delimiters) == 0 && statementTokens > 0 {
			switch {
			case repeatCondition:
				validateExpression(statementStartIndex, parser.index, statementStartIndex)
			case len(stack) > 0 && stack[len(stack)-1].caseNeedsLabel:
				if statementStartIndex >= parser.index {
					appendStatementError(parser.errorAt(statementStartIndex))
				} else if errorIndex := modulaCaseLabelRangeError(
					parser.tokens, statementStartIndex, parser.index,
				); errorIndex >= 0 {
					appendStatementError(parser.errorAt(errorIndex))
				}
			case !expressionContext:
				if errorIndex := modulaStatementRangeError(
					parser.tokens, statementStartIndex, parser.index,
				); errorIndex >= 0 {
					appendStatementError(parser.errorAt(errorIndex))
				}
			}
		}
		if len(delimiters) > 0 {
			opening := delimiters[0]
			appendStatementError(parser.errorRange(opening.start, opening.end))
		}
		if repeatConditionNode != nil {
			if statementTokens > 0 && parser.index > statementStartIndex {
				repeatConditionNode.end = max(
					repeatConditionNode.end,
					parser.tokens[parser.index-1].end,
				)
			}
			repeatConditionNode = nil
		}
		delimiters = delimiters[:0]
		statementActive = false
		statementTokens = 0
		statementAssign = false
		statementStartIndex = -1
		repeatCondition = false
	}
	consumeOrdinary := func(token modulaToken) {
		expressionContext := repeatCondition || len(stack) > 0 &&
			(stack[len(stack)-1].required != "" ||
				stack[len(stack)-1].caseNeedsLabel)
		previousEndsStatement := previous.kind == modulaTokenIdentifier &&
			!modulaKeyword(previous.text) ||
			previous.kind == modulaTokenNumber || previous.text == modulaLiteralToken ||
			previous.text == ")" || previous.text == "]" || previous.text == "}" ||
			previous.text == "END" || previous.text == "EXIT" ||
			previous.text == "RETRY"
		startsPrimary := token.kind == modulaTokenIdentifier &&
			!modulaKeyword(token.text) || token.kind == modulaTokenNumber ||
			token.text == modulaLiteralToken
		if len(delimiters) == 0 && statementActive &&
			previousEndsStatement && startsPrimary {
			appendStatementError(parser.errorRange(token.start, token.end))
		}
		if len(delimiters) == 0 && statementActive && !expressionContext &&
			(token.text == "RETURN" || token.text == "EXIT" ||
				token.text == "RETRY" || token.text == "ASM") {
			appendStatementError(parser.errorRange(token.start, token.end))
		}
		if statementTokens == 0 {
			statementStartIndex = parser.index
		}
		if token.text == ":=" {
			statementAssign = true
		}
		if len(stack) > 0 && stack[len(stack)-1].required != "" {
			control := &stack[len(stack)-1]
			control.headerTokens++
		}
		switch token.text {
		case "(", "[", "{":
			if len(delimiters) >= modulaMaximumStructuralDepth {
				parser.structuralOverflow = true
				parser.index = len(parser.tokens)
				return
			}
			delimiters = append(delimiters, token)
		case ")", "]", "}":
			var expected string
			switch token.text {
			case ")":
				expected = "("
			case "]":
				expected = "["
			case "}":
				expected = "{"
			}
			if len(delimiters) == 0 ||
				delimiters[len(delimiters)-1].text != expected {
				appendStatementError(parser.errorRange(token.start, token.end))
			} else {
				delimiters = delimiters[:len(delimiters)-1]
			}
		}
		previous = token
		statementActive = true
		statementTokens++
		repeatNeedsExpression = false
		parser.index++
	}
	openControl := func(token modulaToken) {
		missingBoundary := statementActive || len(delimiters) > 0 ||
			repeatNeedsExpression
		repeatNeedsExpression = false
		finishStatement()
		node := &modulaParseNode{kind: "block", start: token.start, end: token.end}
		if len(stack) == 0 {
			block.children = append(block.children, node)
		} else {
			stack[len(stack)-1].node.children = append(
				stack[len(stack)-1].node.children, node,
			)
		}
		if len(stack) >= modulaMaximumStructuralDepth {
			parser.structuralOverflow = true
			parser.index = len(parser.tokens)
			return
		}
		control := openBlock{
			node: node, kind: token.text, headerStart: parser.index + 1,
		}
		switch token.text {
		case "IF":
			control.required = "THEN"
		case "CASE":
			control.required = "OF"
		case "WHILE", "FOR", "WITH":
			control.required = "DO"
		case "REPEAT":
			control.repeat = true
		}
		stack = append(stack, control)
		if missingBoundary {
			markCurrentControlError(token)
		}
		parser.index++
	}
	controlHeaderError := func(control openBlock) int {
		if control.headerTokens == 0 {
			return control.headerStart
		}
		switch control.kind {
		case "FOR":
			return modulaForHeaderRangeError(
				parser.tokens, control.headerStart, parser.index,
			)
		case "WITH":
			return modulaDesignatorRangeError(
				parser.tokens, control.headerStart, parser.index,
			)
		default:
			return modulaExpressionRangeError(
				parser.tokens, control.headerStart, parser.index,
			)
		}
	}
	for parser.index < len(parser.tokens) {
		token := parser.current()
		switch token.text {
		case "<*":
			finishStatement()
			directiveEnd := modulaDirectiveRangeEnd(
				parser.tokens, parser.index, len(parser.tokens),
			)
			if directiveEnd < 0 {
				restart := modulaUnmatchedDirectiveRestart(
					parser.tokens, parser.index+1,
				)
				errorEnd := len(parser.source)
				parser.index = len(parser.tokens)
				if restart >= 0 {
					errorEnd = parser.tokens[restart].start
					parser.index = restart
				}
				appendStatementError(parser.errorRange(token.start, errorEnd))
				continue
			}
			appendStatementError(parser.errorRange(
				token.start, parser.tokens[directiveEnd-1].end,
			))
			parser.index = directiveEnd
		case "IMPORT", "FROM", "EXPORT", "CONST", "TYPE", "VAR", "MODULE", "PROCEDURE":
			finishStatement()
			var end int
			semicolon, restart := parser.findDeclarationSemicolonBoundary(
				parser.index+1, false,
			)
			switch {
			case semicolon >= 0:
				end = parser.tokens[semicolon].end
				parser.index = semicolon + 1
			case restart >= 0:
				end = parser.tokens[restart].start
				parser.index = restart
			default:
				end = len(parser.source)
				parser.index = len(parser.tokens)
			}
			errorNode := parser.errorRange(token.start, end)
			if len(stack) == 0 {
				block.children = append(block.children, errorNode)
			} else {
				stack[len(stack)-1].node.children = append(
					stack[len(stack)-1].node.children, errorNode,
				)
			}
		case "IF", "CASE", "WHILE", "FOR", "LOOP", "WITH", "REPEAT":
			openControl(token)
		case "BEGIN":
			appendStatementError(parser.errorRange(token.start, token.end))
			consumeOrdinary(token)
		case "UNTIL":
			if repeatNeedsExpression {
				appendStatementError(parser.errorRange(token.start, token.end))
				repeatNeedsExpression = false
			}
			if len(stack) == 0 || !stack[len(stack)-1].repeat {
				appendStatementError(parser.errorRange(token.start, token.end))
				finishStatement()
				parser.index++
				continue
			}
			finishStatement()
			repeatConditionNode = stack[len(stack)-1].node
			repeatConditionNode.end = token.end
			stack = stack[:len(stack)-1]
			repeatNeedsExpression = true
			repeatCondition = true
			parser.index++
		case "END":
			if repeatNeedsExpression {
				appendStatementError(parser.errorRange(token.start, token.end))
				repeatNeedsExpression = false
			}
			caseHadIncompleteLabel := len(stack) > 0 &&
				stack[len(stack)-1].caseNeedsLabel && statementActive
			finishStatement()
			if len(stack) == 0 {
				block.end = token.end
				return block
			}
			last := len(stack) - 1
			if stack[last].repeat || stack[last].required != "" ||
				caseHadIncompleteLabel {
				markCurrentControlError(token)
			}
			stack[last].node.end = token.end
			stack = stack[:last]
			previous = token
			statementActive = true
			parser.index++
		case ";":
			if repeatNeedsExpression {
				appendStatementError(parser.errorRange(token.start, token.end))
				repeatNeedsExpression = false
			}
			finishStatement()
			parser.index++
		case "THEN", "DO", "OF":
			if repeatNeedsExpression {
				appendStatementError(parser.errorRange(token.start, token.end))
				repeatNeedsExpression = false
			}
			boundaryValid := len(stack) > 0 &&
				stack[len(stack)-1].required == token.text &&
				statementActive && len(delimiters) == 0
			headerError := -1
			if boundaryValid {
				headerError = controlHeaderError(stack[len(stack)-1])
			}
			valid := boundaryValid && headerError < 0
			if !valid {
				errorToken := token
				if headerError >= 0 && headerError < len(parser.tokens) {
					errorToken = parser.tokens[headerError]
				}
				markCurrentControlError(errorToken)
			}
			finishStatement()
			if len(stack) > 0 && stack[len(stack)-1].required == token.text {
				stack[len(stack)-1].required = ""
				if token.text == "OF" {
					stack[len(stack)-1].caseNeedsLabel = true
				}
			}
			parser.index++
		case "ELSE":
			caseHadIncompleteLabel := len(stack) > 0 &&
				stack[len(stack)-1].caseNeedsLabel && statementActive
			valid := len(stack) > 0 && stack[len(stack)-1].required == "" &&
				(stack[len(stack)-1].kind == "IF" ||
					stack[len(stack)-1].kind == "CASE") &&
				!stack[len(stack)-1].elseSeen &&
				!caseHadIncompleteLabel
			if !valid {
				markCurrentControlError(token)
			} else {
				stack[len(stack)-1].elseSeen = true
				stack[len(stack)-1].caseNeedsLabel = false
			}
			finishStatement()
			parser.index++
		case "ELSIF":
			valid := len(stack) > 0 && stack[len(stack)-1].kind == "IF" &&
				stack[len(stack)-1].required == "" &&
				!stack[len(stack)-1].elseSeen
			if !valid {
				markCurrentControlError(token)
			} else {
				control := &stack[len(stack)-1]
				control.required = "THEN"
				control.headerTokens = 0
				control.headerStart = parser.index + 1
			}
			finishStatement()
			parser.index++
		case "|":
			caseHadIncompleteLabel := len(stack) > 0 &&
				stack[len(stack)-1].caseNeedsLabel && statementActive
			valid := len(stack) > 0 && stack[len(stack)-1].kind == "CASE" &&
				stack[len(stack)-1].required == "" &&
				!stack[len(stack)-1].elseSeen &&
				!caseHadIncompleteLabel
			if !valid {
				markCurrentControlError(token)
			}
			finishStatement()
			if valid {
				stack[len(stack)-1].caseNeedsLabel = true
			}
			parser.index++
		case ":":
			if len(delimiters) > 0 {
				consumeOrdinary(token)
				continue
			}
			valid := len(stack) > 0 && stack[len(stack)-1].kind == "CASE" &&
				stack[len(stack)-1].required == "" &&
				stack[len(stack)-1].caseNeedsLabel && statementActive &&
				!statementAssign
			if !valid {
				markCurrentControlError(token)
			}
			finishStatement()
			if valid {
				stack[len(stack)-1].caseNeedsLabel = false
			}
			parser.index++
		case "EXCEPT":
			valid := len(stack) == 0 && !exceptSeen
			if !valid {
				appendStatementError(parser.errorRange(token.start, token.end))
			} else {
				exceptSeen = true
			}
			finishStatement()
			parser.index++
		case "FINALLY":
			valid := allowFinally && len(stack) == 0 && !inFinal
			if !valid {
				appendStatementError(parser.errorRange(token.start, token.end))
			} else {
				inFinal = true
				exceptSeen = false
			}
			finishStatement()
			parser.index++
		default:
			consumeOrdinary(token)
		}
	}
	finishStatement()
	for _, pending := range stack {
		pending.node.end = len(parser.source)
		pending.node.children = append(pending.node.children, &modulaParseNode{
			kind: "ERROR", start: len(parser.source), end: len(parser.source),
		})
	}
	block.end = len(parser.source)
	block.children = append(block.children, &modulaParseNode{
		kind: "ERROR", start: len(parser.source), end: len(parser.source),
	})
	return block
}

func (parser *modulaConcreteParser) skipInvalidBeginBlock() *modulaParseNode {
	start := parser.current().start
	parser.index++
	// Invalid definition/procedure bodies still need typed, bounded balancing:
	// UNTIL closes only REPEAT, while END closes the other retained controls.
	controls := make([]bool, 0, 8) // true denotes REPEAT
	controlOverflow := 0
	for parser.index < len(parser.tokens) {
		if parser.at("<*") {
			if directiveEnd := modulaDirectiveRangeEnd(
				parser.tokens, parser.index, len(parser.tokens),
			); directiveEnd >= 0 {
				parser.index = directiveEnd
				continue
			}
		}
		switch parser.current().text {
		case "IF", "CASE", "WHILE", "FOR", "LOOP", "WITH", "BEGIN":
			if len(controls) >= modulaMaximumStructuralDepth {
				controlOverflow++
			} else {
				controls = append(controls, false)
			}
			parser.index++
			continue
		case "REPEAT":
			if len(controls) >= modulaMaximumStructuralDepth {
				controlOverflow++
			} else {
				controls = append(controls, true)
			}
			parser.index++
			continue
		case "UNTIL":
			if controlOverflow > 0 {
				controlOverflow--
			} else if len(controls) > 0 && controls[len(controls)-1] {
				controls = controls[:len(controls)-1]
			}
			parser.index++
			continue
		case "END":
			end := parser.current().end
			parser.index++
			named := false
			if parser.index < len(parser.tokens) &&
				parser.current().kind == modulaTokenIdentifier &&
				!modulaKeyword(parser.current().text) {
				named = true
				end = parser.current().end
				parser.index++
			}
			if controlOverflow > 0 {
				controlOverflow--
				if parser.at(";") || parser.at(".") {
					parser.index++
				}
				continue
			}
			if len(controls) > 0 {
				if !controls[len(controls)-1] {
					controls = controls[:len(controls)-1]
				}
				if parser.at(";") || parser.at(".") {
					parser.index++
				}
				continue
			}
			if named {
				if parser.at(";") || parser.at(".") {
					parser.index++
				}
				continue
			}
			if parser.at(";") {
				end = parser.current().end
				parser.index++
			}
			return parser.errorRange(start, end)
		}
		parser.index++
	}
	return parser.errorRange(start, len(parser.source))
}

func (parser *modulaConcreteParser) skipMalformedNamedOwner(
	name string,
	minimumEnd int,
) int {
	for index := parser.index; index+2 < len(parser.tokens); index++ {
		if parser.tokens[index].text == "<*" {
			if directiveEnd := modulaDirectiveRangeEnd(
				parser.tokens, index, len(parser.tokens),
			); directiveEnd >= 0 {
				index = directiveEnd - 1
				continue
			}
		}
		if parser.tokens[index].text == "END" &&
			parser.tokens[index+1].text == name &&
			parser.tokens[index+2].text == ";" {
			end := parser.tokens[index+2].end
			parser.index = index + 3
			return end
		}
	}
	return minimumEnd
}

func (parser *modulaConcreteParser) parseOwnerEnd(
	parent *modulaParseNode,
	ownerName, terminator string,
) (bool, int) {
	matched := true
	if !parser.at("END") {
		parent.children = append(parent.children, parser.errorAt(parser.index))
		return false, parser.currentStart()
	}
	end := parser.current().end
	parser.index++
	closingName, ok := parser.consumeName()
	if !ok {
		parent.children = append(parent.children, parser.errorRange(end, end))
		matched = false
	} else {
		end = closingName.end
		if closingName.text != ownerName {
			parent.children = append(parent.children, &modulaParseNode{
				kind: "ERROR", start: closingName.start, end: closingName.end,
			})
			matched = false
		}
	}
	if !parser.at(terminator) {
		if parser.at(";") || parser.at(".") {
			wrong := parser.current()
			parent.children = append(
				parent.children,
				parser.errorRange(wrong.start, wrong.end),
			)
			end = wrong.end
			parser.index++
		} else {
			parent.children = append(parent.children, parser.errorRange(end, end))
		}
		matched = false
	} else {
		end = parser.current().end
		parser.index++
	}
	return matched, end
}

func (parser *modulaConcreteParser) findDeclarationSemicolonBoundary(
	start int,
	typeBlocks bool,
) (semicolon, restart int) {
	paren, bracket := 0, 0
	blocks := make([]string, 0, 4)
	unmatchedDirective := false
	for index := start; index < len(parser.tokens); index++ {
		if parser.tokens[index].text == "<*" {
			if directiveEnd := modulaDirectiveRangeEnd(
				parser.tokens, index, len(parser.tokens),
			); directiveEnd >= 0 {
				index = directiveEnd - 1
				continue
			}
			unmatchedDirective = true
			continue
		}
		token := parser.tokens[index]
		if token.lineStart && modulaDeclarationSectionStarter(token.text) &&
			(unmatchedDirective || paren == 0 && bracket == 0 && len(blocks) == 0) &&
			!modulaProcedureTypeContinuation(parser.tokens, index) {
			return -1, index
		}
		if unmatchedDirective {
			continue
		}
		switch token.text {
		case "(":
			paren++
		case ")":
			paren--
		case "[":
			bracket++
		case "]":
			bracket--
		case "RECORD":
			if typeBlocks {
				blocks = append(blocks, token.text)
			}
		case "CASE":
			if typeBlocks && len(blocks) > 0 {
				blocks = append(blocks, token.text)
			}
		case "END":
			if typeBlocks && len(blocks) > 0 {
				blocks = blocks[:len(blocks)-1]
			}
		case ";":
			if paren == 0 && bracket == 0 && len(blocks) == 0 {
				return index, -1
			}
		}
		if paren < 0 || bracket < 0 || paren+bracket+len(blocks) >
			modulaMaximumStructuralDepth {
			return -1, -1
		}
	}
	return -1, -1
}

func (parser *modulaConcreteParser) findDeclarationBoundary(
	start int,
	typeBlocks bool,
) (semicolon, restart int) {
	paren, bracket := 0, 0
	blocks := make([]string, 0, 4)
	unmatchedDirective := false
	for index := start; index < len(parser.tokens); index++ {
		token := parser.tokens[index]
		if token.text == "<*" {
			if directiveEnd := modulaDirectiveRangeEnd(
				parser.tokens, index, len(parser.tokens),
			); directiveEnd >= 0 {
				index = directiveEnd - 1
				continue
			}
			unmatchedDirective = true
			continue
		}
		if index >= start && token.lineStart &&
			(unmatchedDirective || paren == 0 && bracket == 0 && len(blocks) == 0) &&
			modulaDeclarationSectionStarter(token.text) &&
			!modulaProcedureTypeContinuation(parser.tokens, index) {
			return -1, index
		}
		if unmatchedDirective {
			continue
		}
		switch token.text {
		case "(":
			paren++
		case ")":
			paren--
		case "[":
			bracket++
		case "]":
			bracket--
		case "RECORD":
			if typeBlocks {
				blocks = append(blocks, token.text)
			}
		case "CASE":
			if typeBlocks && len(blocks) > 0 {
				blocks = append(blocks, token.text)
			}
		case "END":
			if typeBlocks && len(blocks) > 0 {
				blocks = blocks[:len(blocks)-1]
			}
		case ";":
			if paren == 0 && bracket == 0 && len(blocks) == 0 {
				return index, -1
			}
		}
		if paren < 0 || bracket < 0 || paren+bracket+len(blocks) >
			modulaMaximumStructuralDepth {
			return -1, -1
		}
	}
	return -1, -1
}

func modulaProcedureTypeContinuation(tokens []modulaToken, index int) bool {
	if index <= 0 || index >= len(tokens) || tokens[index].text != "PROCEDURE" {
		return false
	}
	switch tokens[index-1].text {
	case "=", ":", "TO", "OF":
		return true
	default:
		return false
	}
}

func (parser *modulaConcreteParser) findTopLevelToken(
	start int,
	want, stop string,
) int {
	paren, bracket := 0, 0
	for index := start; index < len(parser.tokens); index++ {
		if parser.tokens[index].text == "<*" {
			if directiveEnd := modulaDirectiveRangeEnd(
				parser.tokens, index, len(parser.tokens),
			); directiveEnd >= 0 {
				index = directiveEnd - 1
				continue
			}
			return -1
		}
		switch parser.tokens[index].text {
		case "(":
			paren++
		case ")":
			paren--
		case "[":
			bracket++
		case "]":
			bracket--
		default:
			if paren == 0 && bracket == 0 {
				if parser.tokens[index].text == want {
					return index
				}
				if parser.tokens[index].text == stop {
					return -1
				}
			}
		}
	}
	return -1
}

func (parser *modulaConcreteParser) skipBalanced(open, closing string) bool {
	if !parser.at(open) {
		return false
	}
	depth := 0
	for parser.index < len(parser.tokens) {
		if parser.at("<*") {
			if directiveEnd := modulaDirectiveRangeEnd(
				parser.tokens, parser.index, len(parser.tokens),
			); directiveEnd >= 0 {
				parser.index = directiveEnd
				continue
			}
		}
		switch parser.current().text {
		case open:
			depth++
		case closing:
			depth--
		}
		parser.index++
		if depth == 0 {
			return true
		}
		if depth > modulaMaximumStructuralDepth {
			return false
		}
	}
	return false
}

func (parser *modulaConcreteParser) consumeModulePriority() *modulaParseNode {
	openIndex := parser.index
	opening := parser.current()
	if !parser.skipBalanced("[", "]") {
		return parser.errorRange(opening.start, parser.currentStart())
	}
	closeIndex := parser.index - 1
	if closeIndex <= openIndex+1 ||
		!modulaConstantExpressionRangeValid(
			parser.tokens, openIndex+1, closeIndex,
		) {
		return parser.errorRange(opening.start, parser.tokens[closeIndex].end)
	}
	return nil
}

func (parser *modulaConcreteParser) consumeName() (modulaToken, bool) {
	if parser.index >= len(parser.tokens) ||
		parser.current().kind != modulaTokenIdentifier ||
		modulaKeyword(parser.current().text) || parser.current().gap {
		return modulaToken{}, false
	}
	token := parser.current()
	parser.index++
	return token, true
}

func (parser *modulaConcreteParser) currentIsDeclarationName() bool {
	if parser.index >= len(parser.tokens) || parser.current().gap ||
		parser.current().kind != modulaTokenIdentifier {
		return false
	}
	return !modulaKeyword(parser.current().text)
}

func (parser *modulaConcreteParser) recoverDeclarationBoundary() {
	start := parser.index
	parser.recoverTo(";", "BEGIN", "END", "CONST", "TYPE", "VAR", "PROCEDURE", "MODULE")
	if parser.at(";") {
		parser.index++
	}
	if parser.index == start && parser.index < len(parser.tokens) {
		parser.index++
	}
}

func (parser *modulaConcreteParser) recoverTo(tokens ...string) {
	for parser.index < len(parser.tokens) {
		if parser.at("<*") {
			if directiveEnd := modulaDirectiveRangeEnd(
				parser.tokens, parser.index, len(parser.tokens),
			); directiveEnd >= 0 {
				parser.index = directiveEnd
				continue
			}
		}
		for _, token := range tokens {
			if parser.at(token) {
				return
			}
		}
		parser.index++
	}
}

func (parser *modulaConcreteParser) consume(text string) bool {
	if !parser.at(text) {
		return false
	}
	parser.index++
	return true
}

func (parser *modulaConcreteParser) at(text string) bool {
	return parser.index < len(parser.tokens) && !parser.tokens[parser.index].gap &&
		parser.tokens[parser.index].text == text
}

func (parser *modulaConcreteParser) current() modulaToken {
	if parser.index >= len(parser.tokens) {
		return modulaToken{start: len(parser.source), end: len(parser.source)}
	}
	return parser.tokens[parser.index]
}

func (parser *modulaConcreteParser) currentText() string {
	if parser.index >= len(parser.tokens) {
		return ""
	}
	return parser.tokens[parser.index].text
}

func (parser *modulaConcreteParser) currentStart() int {
	return parser.current().start
}

func (parser *modulaConcreteParser) errorAt(index int) *modulaParseNode {
	if index < 0 || index >= len(parser.tokens) {
		return &modulaParseNode{
			kind: "ERROR", start: len(parser.source), end: len(parser.source),
		}
	}
	token := parser.tokens[index]
	return &modulaParseNode{kind: "ERROR", start: token.start, end: token.end}
}

func (parser *modulaConcreteParser) errorRange(start, end int) *modulaParseNode {
	start = max(0, min(start, len(parser.source)))
	end = max(start, min(end, len(parser.source)))
	return &modulaParseNode{kind: "ERROR", start: start, end: end}
}

func modulaIdentifierNode(token modulaToken) *modulaParseNode {
	return &modulaParseNode{
		kind: "identifier", start: token.nameStart, end: token.end,
	}
}

func modulaRawLocalModuleName(header []modulaToken) (modulaToken, bool) {
	if len(header) < 2 || header[0].text != "MODULE" {
		return modulaToken{}, false
	}
	index := 1
	for index < len(header) && header[index].text == "<*" {
		directiveEnd := modulaDirectiveRangeEnd(header, index, len(header))
		if directiveEnd < 0 {
			return modulaToken{}, false
		}
		index = directiveEnd
	}
	if index >= len(header) || header[index].kind != modulaTokenIdentifier ||
		modulaKeyword(header[index].text) {
		return modulaToken{}, false
	}
	return header[index], true
}

func modulaRawProcedureName(header []modulaToken) (modulaToken, bool) {
	if len(header) < 2 || header[0].text != "PROCEDURE" {
		return modulaToken{}, false
	}
	index := 1
	for index < len(header) && modulaProcedureMarker(header[index].text) {
		index++
	}
	for index < len(header) && header[index].text == "<*" {
		directiveEnd := modulaDirectiveRangeEnd(header, index, len(header))
		if directiveEnd < 0 {
			return modulaToken{}, false
		}
		index = directiveEnd
	}
	if index < len(header) && header[index].text == "(" {
		end := modulaRecoveryMatchingDelimiter(header, index, "(", ")")
		if end < 0 {
			return modulaToken{}, false
		}
		index = end + 1
	}
	if index >= len(header) || header[index].kind != modulaTokenIdentifier {
		return modulaToken{}, false
	}
	if modulaKeyword(header[index].text) &&
		(len(header[index].text) < 2 || header[index].text[:2] != "__") {
		return modulaToken{}, false
	}
	return header[index], true
}

func modulaTypeRangeContainsRecord(tokens []modulaToken, start, end int) bool {
	for index := start; index < end && index < len(tokens); index++ {
		if tokens[index].text == "RECORD" {
			return true
		}
	}
	return false
}

func modulaProcedureMarker(text string) bool {
	switch text {
	case "__BUILTIN__", "__INLINE__", "__ATTRIBUTE__":
		return true
	default:
		return false
	}
}

func modulaDeclarationSectionStarter(text string) bool {
	switch text {
	case "CONST", "TYPE", "VAR", "PROCEDURE", "MODULE", "IMPORT", "FROM",
		"EXPORT", "BEGIN", "END":
		return true
	default:
		return false
	}
}

func modulaImportListValid(tokens []modulaToken, start, end int) bool {
	if start < 0 || end <= start || end > len(tokens) {
		return false
	}
	expectName := true
	for index := start; index < end; index++ {
		text := tokens[index].text
		if expectName {
			if tokens[index].kind != modulaTokenIdentifier || modulaKeyword(text) {
				return false
			}
			expectName = false
			continue
		}
		switch text {
		case ",":
			expectName = true
		default:
			return false
		}
	}
	return !expectName
}

func modulaDeclaratorNames(
	tokens []modulaToken,
	start, end int,
) ([]modulaToken, bool) {
	if start < 0 || end <= start || end > len(tokens) {
		return nil, false
	}
	names := make([]modulaToken, 0, 4)
	index := start
	for index < end {
		if tokens[index].kind != modulaTokenIdentifier ||
			modulaKeyword(tokens[index].text) {
			return nil, false
		}
		names = append(names, tokens[index])
		index++
		if index < end && tokens[index].text == "[" {
			opening := index
			depth := 0
			for index < end {
				switch tokens[index].text {
				case "[":
					depth++
				case "]":
					depth--
				}
				index++
				if depth == 0 {
					break
				}
			}
			if depth != 0 {
				return nil, false
			}
			if index <= opening+2 || !modulaConstantExpressionRangeValid(
				tokens, opening+1, index-1,
			) {
				return nil, false
			}
		}
		if index == end {
			break
		}
		if tokens[index].text != "," {
			return nil, false
		}
		index++
	}
	return names, len(names) > 0
}

func modulaIdentifierListNames(
	tokens []modulaToken,
	start, end int,
) ([]modulaToken, bool) {
	if start < 0 || end <= start || end > len(tokens) {
		return nil, false
	}
	names := make([]modulaToken, 0, 4)
	expectName := true
	for index := start; index < end; index++ {
		if expectName {
			if tokens[index].kind != modulaTokenIdentifier ||
				modulaKeyword(tokens[index].text) {
				return nil, false
			}
			names = append(names, tokens[index])
			expectName = false
			continue
		}
		if tokens[index].text != "," {
			return nil, false
		}
		expectName = true
	}
	return names, len(names) > 0 && !expectName
}

func modulaDirectiveRangeEnd(tokens []modulaToken, start, limit int) int {
	if start < 0 || start >= limit || limit > len(tokens) ||
		tokens[start].text != "<*" || !tokens[start].directiveClosed {
		return -1
	}
	for index := start + 1; index < limit; index++ {
		if tokens[index].text == "*>" {
			return index + 1
		}
	}
	return -1
}

func modulaUnmatchedDirectiveRestart(tokens []modulaToken, start int) int {
	for index := max(start, 0); index < len(tokens); index++ {
		if tokens[index].text == "<*" {
			if directiveEnd := modulaDirectiveRangeEnd(
				tokens, index, len(tokens),
			); directiveEnd >= 0 {
				index = directiveEnd - 1
				continue
			}
		}
		if tokens[index].lineStart &&
			modulaDeclarationSectionStarter(tokens[index].text) {
			return index
		}
	}
	return -1
}

func modulaDefaultAttributeRangeValid(
	tokens []modulaToken,
	start, end int,
) bool {
	return end-start >= 6 && tokens[start].text == "<*" &&
		modulaDirectiveRangeEnd(tokens, start, end) == end &&
		tokens[start+1].kind == modulaTokenIdentifier &&
		!modulaKeyword(tokens[start+1].text) &&
		tokens[start+2].text == "(" && tokens[end-2].text == ")" &&
		modulaConstantExpressionRangeValid(tokens, start+3, end-2)
}

func modulaBareAttributeRangeValid(
	tokens []modulaToken,
	start, end int,
) bool {
	return end-start == 3 && tokens[start].text == "<*" &&
		modulaDirectiveRangeEnd(tokens, start, end) == end &&
		tokens[start+1].kind == modulaTokenIdentifier &&
		!modulaKeyword(tokens[start+1].text) && tokens[start+2].text == "*>"
}

func modulaWithoutTrailingBareAttribute(
	tokens []modulaToken,
	start, end int,
) (int, bool) {
	if end-start < 3 || tokens[end-1].text != "*>" {
		return end, true
	}
	attributeStart := end - 3
	if !modulaBareAttributeRangeValid(tokens, attributeStart, end) {
		return end, false
	}
	return attributeStart, attributeStart > start
}

func modulaFieldAttributeRangeValid(
	tokens []modulaToken,
	start, end int,
) bool {
	if end-start < 3 || tokens[start].text != "<*" ||
		modulaDirectiveRangeEnd(tokens, start, end) != end {
		return false
	}
	index := start + 1
	for index < end-1 {
		if tokens[index].kind != modulaTokenIdentifier ||
			modulaKeyword(tokens[index].text) {
			return false
		}
		index++
		if index < end-1 && tokens[index].text == "(" {
			opening := index
			depth := 0
			for index < end-1 {
				switch tokens[index].text {
				case "(":
					depth++
				case ")":
					depth--
				}
				index++
				if depth == 0 {
					break
				}
			}
			if depth != 0 || index <= opening+2 ||
				!modulaConstantExpressionRangeValid(tokens, opening+1, index-1) {
				return false
			}
		}
		if index == end-1 {
			return true
		}
		if tokens[index].text != "," {
			return false
		}
		index++
	}
	return false
}

func modulaTypeWithoutTrailingAttribute(
	tokens []modulaToken,
	start, end int,
) (int, bool) {
	if end <= start || end > len(tokens) || tokens[end-1].text != "*>" {
		return end, true
	}
	for index := end - 2; index >= start; index-- {
		if tokens[index].text != "<*" {
			continue
		}
		if modulaDirectiveRangeEnd(tokens, index, end) != end ||
			!modulaDefaultAttributeRangeValid(tokens, index, end) {
			return end, false
		}
		return index, index > start
	}
	return end, false
}

func modulaTypeRangeValid(tokens []modulaToken, start, end int) bool {
	if start < 0 || start >= end || end > len(tokens) {
		return false
	}
	var ok bool
	if end, ok = modulaTypeWithoutTrailingAttribute(tokens, start, end); !ok ||
		start >= end {
		return false
	}
	parser := modulaTypeParser{tokens: tokens, index: start, end: end}
	return parser.parseType(0) && parser.index == end
}

// modulaTypeParser consumes each token at most once. Recursive calls are
// reserved for genuinely nested type constructors and are capped at the same
// structural frontier as the concrete parser; long POINTER TO chains are
// handled iteratively so malformed near-token-cap declarations remain linear
// and stack-safe.
type modulaTypeParser struct {
	tokens         []modulaToken
	members        []*modulaParseNode
	index          int
	end            int
	collectMembers bool
}

func (parser *modulaTypeParser) parseType(depth int) bool {
	for parser.at("POINTER") {
		if depth >= modulaMaximumStructuralDepth {
			return false
		}
		depth++
		parser.index++
		if !parser.require("TO") {
			return false
		}
	}
	if parser.index >= parser.end || parser.tokens[parser.index].gap {
		return false
	}
	token := parser.tokens[parser.index]
	if token.kind == modulaTokenIdentifier && !modulaKeyword(token.text) ||
		token.text == "(" || token.text == "[" {
		return parser.parseSimpleType(depth)
	}
	if depth >= modulaMaximumStructuralDepth {
		return false
	}
	switch token.text {
	case "ARRAY":
		return parser.parseArrayType(depth + 1)
	case "SET", "PACKEDSET":
		parser.index++
		return parser.require("OF") && parser.parseSimpleType(depth+1)
	case "PROCEDURE":
		return parser.parseProcedureType(depth + 1)
	case "RECORD":
		return parser.parseRecordType(depth + 1)
	default:
		return false
	}
}

func (parser *modulaTypeParser) parseSimpleType(depth int) bool {
	if parser.index >= parser.end || parser.tokens[parser.index].gap {
		return false
	}
	if parser.tokens[parser.index].kind == modulaTokenIdentifier &&
		!modulaKeyword(parser.tokens[parser.index].text) {
		if !parser.parseQualident() {
			return false
		}
		if parser.at("[") {
			return parser.parseSubrangeType(depth)
		}
		return true
	}
	switch parser.currentText() {
	case "(":
		return parser.parseEnumeration(depth)
	case "[":
		return parser.parseSubrangeType(depth)
	default:
		return false
	}
}

func (parser *modulaTypeParser) parseArrayType(depth int) bool {
	parser.index++
	if !parser.parseSimpleType(depth) {
		return false
	}
	for parser.consume(",") {
		if !parser.parseSimpleType(depth) {
			return false
		}
	}
	return parser.require("OF") && parser.parseType(depth)
}

func (parser *modulaTypeParser) parseEnumeration(depth int) bool {
	start := parser.index
	if depth >= modulaMaximumStructuralDepth || !parser.consume("(") ||
		!parser.requireName() {
		return false
	}
	for parser.consume(",") {
		if !parser.requireName() {
			return false
		}
	}
	if !parser.require(")") {
		return false
	}
	if parser.collectMembers {
		if members, ok := modulaEnumerationNodes(
			parser.tokens, start, parser.index,
		); ok {
			parser.members = append(parser.members, members...)
		}
	}
	return true
}

func (parser *modulaTypeParser) parseSubrangeType(depth int) bool {
	if depth >= modulaMaximumStructuralDepth || !parser.consume("[") ||
		!parser.parseConstExpression(depth+1) || !parser.require("..") ||
		!parser.parseConstExpression(depth+1) {
		return false
	}
	return parser.require("]")
}

func (parser *modulaTypeParser) parseProcedureType(depth int) bool {
	parser.index++
	if !parser.consume("(") {
		return true
	}
	if depth >= modulaMaximumStructuralDepth {
		return false
	}
	if !parser.consume(")") {
		for {
			if parser.consume("...") {
				// The ellipsis is a complete procedure-type parameter.
			} else {
				parser.consume("VAR")
				if !parser.parseFormalType(depth + 1) {
					return false
				}
			}
			if !parser.consume(",") {
				break
			}
		}
		if !parser.require(")") {
			return false
		}
	}
	if !parser.consume(":") {
		return true
	}
	if parser.consume("[") {
		return parser.parseQualident() && parser.require("]")
	}
	return parser.parseQualident()
}

func (parser *modulaTypeParser) parseFormalType(depth int) bool {
	arrayDepth := depth
	for parser.consume("ARRAY") {
		if arrayDepth >= modulaMaximumStructuralDepth || !parser.require("OF") {
			return false
		}
		arrayDepth++
	}
	return parser.parseQualident()
}

func (parser *modulaTypeParser) parseRecordType(depth int) bool {
	parser.index++
	if parser.at("<*") && !parser.consumeDefaultAttribute() {
		return false
	}
	if !parser.parseRecordFieldSequence(depth, "END") {
		return false
	}
	return parser.require("END")
}

func (parser *modulaTypeParser) parseRecordFieldSequence(
	depth int,
	stops ...string,
) bool {
	for {
		if parser.atAny(stops...) {
			return true
		}
		// FieldList is optional in the GNU grammar, so empty lists between
		// semicolons and a trailing semicolon are both accepted.
		if parser.consume(";") {
			continue
		}
		if !parser.parseRecordFieldList(depth) {
			return false
		}
		if parser.atAny(stops...) {
			return true
		}
		if !parser.consume(";") {
			return false
		}
	}
}

func (parser *modulaTypeParser) parseRecordFieldList(depth int) bool {
	if parser.at("CASE") {
		if depth >= modulaMaximumStructuralDepth {
			return false
		}
		return parser.parseRecordCase(depth + 1)
	}
	if !parser.requireName() {
		return false
	}
	for parser.consume(",") {
		if !parser.requireName() {
			return false
		}
	}
	if !parser.require(":") || !parser.parseType(depth) {
		return false
	}
	if parser.at("<*") {
		return parser.consumeFieldAttribute()
	}
	return true
}

func (parser *modulaTypeParser) parseRecordCase(depth int) bool {
	parser.index++
	if parser.currentIsName() {
		parser.index++
		if parser.consume(":") && !parser.parseQualident() {
			return false
		}
	} else if parser.consume(":") && !parser.parseQualident() {
		return false
	}
	if !parser.require("OF") {
		return false
	}
	for parser.consume("|") {
		// Variant is optional, so leading and repeated separators denote
		// empty alternatives in the GNU/ISO grammar.
	}
	if !parser.at("ELSE") && !parser.at("END") {
		if !parser.parseRecordVariant(depth) {
			return false
		}
		for parser.at("|") {
			for parser.consume("|") {
			}
			if parser.at("ELSE") || parser.at("END") {
				break
			}
			if !parser.parseRecordVariant(depth) {
				return false
			}
		}
	}
	if parser.consume("ELSE") &&
		!parser.parseRecordFieldSequence(depth, "END") {
		return false
	}
	return parser.require("END")
}

func (parser *modulaTypeParser) parseRecordVariant(depth int) bool {
	if !parser.parseCaseLabelList(depth) || !parser.require(":") {
		return false
	}
	return parser.parseRecordFieldSequence(depth, "|", "ELSE", "END")
}

func (parser *modulaTypeParser) parseCaseLabelList(depth int) bool {
	if !parser.parseConstExpression(depth) {
		return false
	}
	if parser.consume("..") && !parser.parseConstExpression(depth) {
		return false
	}
	for parser.consume(",") {
		if !parser.parseConstExpression(depth) {
			return false
		}
		if parser.consume("..") && !parser.parseConstExpression(depth) {
			return false
		}
	}
	return true
}

func (parser *modulaTypeParser) parseConstExpression(depth int) bool {
	expression := modulaExpressionParser{
		tokens: parser.tokens, start: parser.index, index: parser.index,
		end: parser.end, depth: depth, constant: true, errorIndex: -1,
	}
	if !expression.parseExpression() {
		return false
	}
	parser.index = expression.index
	return true
}

func (parser *modulaTypeParser) consumeDefaultAttribute() bool {
	attributeEnd := modulaDirectiveRangeEnd(
		parser.tokens, parser.index, parser.end,
	)
	if attributeEnd < 0 || !modulaDefaultAttributeRangeValid(
		parser.tokens, parser.index, attributeEnd,
	) {
		return false
	}
	parser.index = attributeEnd
	return true
}

func (parser *modulaTypeParser) consumeFieldAttribute() bool {
	attributeEnd := modulaDirectiveRangeEnd(
		parser.tokens, parser.index, parser.end,
	)
	if attributeEnd < 0 || !modulaFieldAttributeRangeValid(
		parser.tokens, parser.index, attributeEnd,
	) {
		return false
	}
	parser.index = attributeEnd
	return true
}

func (parser *modulaTypeParser) parseQualident() bool {
	if !parser.requireName() {
		return false
	}
	for parser.consume(".") {
		if !parser.requireName() {
			return false
		}
	}
	return true
}

func (parser *modulaTypeParser) requireName() bool {
	if !parser.currentIsName() {
		return false
	}
	parser.index++
	return true
}

func (parser *modulaTypeParser) currentIsName() bool {
	return parser.index < parser.end && !parser.tokens[parser.index].gap &&
		parser.tokens[parser.index].kind == modulaTokenIdentifier &&
		!modulaKeyword(parser.tokens[parser.index].text)
}

func (parser *modulaTypeParser) require(text string) bool {
	return parser.consume(text)
}

func (parser *modulaTypeParser) consume(text string) bool {
	if !parser.at(text) {
		return false
	}
	parser.index++
	return true
}

func (parser *modulaTypeParser) at(text string) bool {
	return parser.index < parser.end && parser.tokens[parser.index].text == text
}

func (parser *modulaTypeParser) atAny(texts ...string) bool {
	current := parser.currentText()
	for _, text := range texts {
		if current == text {
			return true
		}
	}
	return false
}

func (parser *modulaTypeParser) currentText() string {
	if parser.index >= parser.end {
		return ""
	}
	return parser.tokens[parser.index].text
}

func modulaTypeMemberNodes(
	tokens []modulaToken,
	start, end int,
) []*modulaParseNode {
	if start < 0 || end <= start || end > len(tokens) {
		return nil
	}
	if baseEnd, ok := modulaTypeWithoutTrailingAttribute(
		tokens, start, end,
	); ok {
		end = baseEnd
	}
	memberParser := modulaTypeParser{
		tokens: tokens, index: start, end: end, collectMembers: true,
	}
	if !memberParser.parseType(0) || memberParser.index != end {
		return nil
	}

	blocks := make([]modulaTypeBlock, 0, 8)
	members := append([]*modulaParseNode(nil), memberParser.members...)
	for index := start; index < end; index++ {
		text := tokens[index].text
		if text == "<*" {
			directiveEnd := modulaDirectiveRangeEnd(tokens, index, end)
			if directiveEnd < 0 {
				continue
			}
			if state := modulaCurrentRecordState(blocks); state != nil &&
				state.defaultAllowed && state.boundary == "field" &&
				state.segmentStart == index {
				state.defaultAllowed = false
				state.segmentStart = directiveEnd
			}
			index = directiveEnd - 1
			continue
		}
		if state := modulaCurrentRecordState(blocks); state != nil {
			state.defaultAllowed = false
		}
		switch text {
		case "RECORD":
			state := &modulaRecordState{
				segmentStart: index + 1, boundary: "field", defaultAllowed: true,
			}
			blocks = append(blocks, modulaTypeBlock{kind: "RECORD", record: state})
		case "CASE":
			state := modulaCurrentRecordState(blocks)
			if state == nil {
				continue
			}
			blocks = append(blocks, modulaTypeBlock{kind: "CASE", record: state})
			if index+2 < end && tokens[index+1].kind == modulaTokenIdentifier &&
				(tokens[index+2].text == ":" || tokens[index+2].text == "OF") {
				name := tokens[index+1]
				members = append(members, &modulaParseNode{
					kind: "variant_tag_declaration", start: name.start, end: name.end,
					children: []*modulaParseNode{modulaIdentifierNode(name)},
				})
			}
			state.boundary = "case_header"
		case "END":
			if len(blocks) > 0 {
				blocks = blocks[:len(blocks)-1]
			}
		case "OF", "|":
			if state := modulaCurrentRecordState(blocks); state != nil {
				state.segmentStart = index + 1
				state.boundary = "variant_label"
			}
		case "ELSE", ";":
			if state := modulaCurrentRecordState(blocks); state != nil {
				state.segmentStart = index + 1
				state.boundary = "field"
			}
		case ":":
			state := modulaCurrentRecordState(blocks)
			if state == nil {
				continue
			}
			if state.boundary == "variant_label" {
				state.segmentStart = index + 1
				state.boundary = "field"
				continue
			}
			if state.boundary != "field" {
				continue
			}
			names, ok := modulaIdentifierListNames(tokens, state.segmentStart, index)
			if !ok {
				state.boundary = "type"
				continue
			}
			field := &modulaParseNode{
				kind: "record_field_declaration", start: names[0].start,
				end: tokens[index].end,
			}
			for _, name := range names {
				field.children = append(field.children, modulaIdentifierNode(name))
			}
			members = append(members, field)
			state.boundary = "type"
		}
	}
	sort.SliceStable(members, func(first, second int) bool {
		return members[first].start < members[second].start
	})
	return members
}

func modulaCurrentRecordState(blocks []modulaTypeBlock) *modulaRecordState {
	for index := len(blocks) - 1; index >= 0; index-- {
		if blocks[index].record != nil {
			return blocks[index].record
		}
	}
	return nil
}

func modulaEnumerationNodes(
	tokens []modulaToken,
	start, end int,
) ([]*modulaParseNode, bool) {
	if end-start < 2 || tokens[start].text != "(" || tokens[end-1].text != ")" {
		return nil, false
	}
	members := make([]*modulaParseNode, 0, (end-start)/2)
	expectName := true
	for index := start + 1; index < end-1; index++ {
		if expectName {
			if tokens[index].kind != modulaTokenIdentifier ||
				modulaKeyword(tokens[index].text) {
				return nil, false
			}
			name := tokens[index]
			members = append(members, &modulaParseNode{
				kind: "enum_member", start: name.start, end: name.end,
				children: []*modulaParseNode{modulaIdentifierNode(name)},
			})
			expectName = false
		} else {
			if tokens[index].text != "," {
				return nil, false
			}
			expectName = true
		}
	}
	if expectName {
		return nil, false
	}
	return members, true
}

func modulaSyntaxErrorSpans(
	tree *modulaSyntaxTree,
	sourceLength int,
) []cByteSpan {
	if !validateModulaSyntaxTree(tree, sourceLength) {
		return nil
	}
	spans := make([]cByteSpan, 0)
	for index, node := range tree.nodes {
		if node.kind != "ERROR" && (index == tree.root || node.startByte != node.endByte) {
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
	return normalizeCSpans(spans)
}

func modulaTreeDefinitions(
	source string,
	lineCount int,
	tree *modulaSyntaxTree,
) []sourceDefinition {
	if lineCount < 1 || !validateModulaSyntaxTree(tree, len(source)) {
		return nil
	}
	lineStarts := modulaLineStarts(source)
	definitions := make([]sourceDefinition, 0)
	for index, node := range tree.nodes {
		ownsScope, declaration := modulaDeclarationNodeKind(node.kind)
		if !declaration {
			continue
		}
		for _, childIndex := range node.children {
			if childIndex < 0 || childIndex >= len(tree.nodes) {
				continue
			}
			child := tree.nodes[childIndex]
			if child.kind != "identifier" || child.startByte >= child.endByte {
				continue
			}
			line, column := modulaLineAndColumn(lineStarts, child.startByte)
			scopeStart, _ := modulaLineAndColumn(lineStarts, node.startByte)
			scopeEnd, _ := modulaLineAndColumn(
				lineStarts, max(node.startByte, node.endByte-1),
			)
			ownedEndLine, ownedEndColumn := modulaLineAndColumn(
				lineStarts, node.endByte,
			)
			if !ownsScope || ownedEndLine != scopeEnd {
				ownedEndColumn = 0
			}
			definitions = append(definitions, sourceDefinition{
				symbol: source[child.startByte:child.endByte], line: line, column: column,
				scopeStart: scopeStart, scopeEnd: scopeEnd,
				ownedEndColumn: ownedEndColumn, ownsScope: ownsScope,
			})
		}
		_ = index
	}
	return modulaSortUniqueDefinitions(definitions, lineCount)
}

func modulaTreeScopes(
	source string,
	lineCount int,
	tree *modulaSyntaxTree,
) []cLineScope {
	if lineCount < 1 || !validateModulaSyntaxTree(tree, len(source)) {
		return nil
	}
	lineStarts := modulaLineStarts(source)
	scopes := make([]cLineScope, 0)
	for _, node := range tree.nodes {
		ownsScope, declaration := modulaDeclarationNodeKind(node.kind)
		if node.kind != "block" && (!declaration || !ownsScope) {
			continue
		}
		start, _ := modulaLineAndColumn(lineStarts, node.startByte)
		end, _ := modulaLineAndColumn(lineStarts, max(node.startByte, node.endByte-1))
		scopes = append(scopes, cLineScope{start: start, end: end})
	}
	return cNormalizeTreeLineScopes(scopes, lineCount)
}

func modulaTreeImports(
	source string,
	lineCount int,
	tree *modulaSyntaxTree,
) []cLineSpan {
	if lineCount < 1 || !validateModulaSyntaxTree(tree, len(source)) {
		return nil
	}
	lineStarts := modulaLineStarts(source)
	imports := make([]cLineSpan, 0)
	for _, node := range tree.nodes {
		if node.kind != "import_clause" {
			continue
		}
		start, _ := modulaLineAndColumn(lineStarts, node.startByte)
		end, _ := modulaLineAndColumn(lineStarts, max(node.startByte, node.endByte-1))
		imports = append(imports, cLineSpan{start: start, end: end})
	}
	return cNormalizeTreeLineSpans(imports, lineCount)
}

func modulaDeclarationNodeKind(kind string) (ownsScope, declaration bool) {
	switch kind {
	case "module_declaration", "procedure_declaration":
		return true, true
	case "record_type_declaration":
		return true, true
	case "procedure_signature_declaration", "type_declaration":
		return false, true
	case "module_declaration_mismatched_end", "procedure_declaration_mismatched_end",
		"module_declaration_unterminated", "procedure_declaration_unterminated":
		return false, true
	case "constant_declaration", "variable_declaration", "record_field_declaration",
		"variant_tag_declaration", "enum_member":
		return false, true
	default:
		return false, false
	}
}

func modulaLineAndColumn(lineStarts []int, offset int) (int, int) {
	line := modulaTokenLine(lineStarts, offset)
	line = max(1, min(line, len(lineStarts)))
	return line, offset - lineStarts[line-1] + 1
}

func modulaSortUniqueDefinitions(
	definitions []sourceDefinition,
	lineCount int,
) []sourceDefinition {
	for index := range definitions {
		definitions[index] = normalizeCDefinition(definitions[index], lineCount)
	}
	sort.SliceStable(definitions, func(first, second int) bool {
		left, right := definitions[first], definitions[second]
		if left.line != right.line {
			return left.line < right.line
		}
		if left.column != right.column {
			return left.column < right.column
		}
		return left.symbol < right.symbol
	})
	unique := definitions[:0]
	for _, definition := range definitions {
		if definition.symbol == "" {
			continue
		}
		if len(unique) > 0 {
			last := &unique[len(unique)-1]
			if last.symbol == definition.symbol && last.line == definition.line &&
				last.column == definition.column {
				if definition.ownsScope && !last.ownsScope {
					*last = definition
				} else if definition.ownsScope == last.ownsScope {
					last.scopeStart = min(last.scopeStart, definition.scopeStart)
					if definition.scopeEnd > last.scopeEnd {
						last.scopeEnd = definition.scopeEnd
						last.ownedEndColumn = definition.ownedEndColumn
					} else if definition.scopeEnd == last.scopeEnd &&
						last.ownedEndColumn == 0 {
						last.ownedEndColumn = definition.ownedEndColumn
					}
				}
				continue
			}
		}
		unique = append(unique, definition)
	}
	return unique
}
