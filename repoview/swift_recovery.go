package repoview

import (
	"sort"
	"strings"
)

type swiftLexicalAnalysis struct {
	definitions []sourceDefinition
	scopes      []cLineScope
	imports     []cLineSpan
}

type swiftRecoveryFrameKind uint8

const (
	swiftRecoverySource swiftRecoveryFrameKind = iota
	swiftRecoveryType
	swiftRecoveryEnum
	swiftRecoveryProtocol
	swiftRecoveryFunction
	swiftRecoveryProperty
	swiftRecoverySwitch
	swiftRecoveryControl
)

type swiftRecoveryFrame struct {
	definitionIndexes []int
	suspendedHeader   []swiftToken
	suspendedBalance  swiftHeaderBalance
	suspendedDefStart int
	suspendedScopeAt  int
	suspendedImportAt int
	start             int
	caseStart         int
	suspendedKind     swiftHeaderKind
	kind              swiftRecoveryFrameKind
	suspendedGap      bool
	suspendedControl  bool
	suspendedClass    bool
	emitScope         bool
	suppressed        bool
}

type swiftRecoveryConditional struct {
	baseDepth       int
	branchStartLine int
}

type swiftDefinitionKey struct {
	symbol       string
	line, column int
}

type swiftRecoveryParser struct {
	definitionSeen  map[swiftDefinitionKey]int
	source          string
	scopes          []cLineScope
	lineStarts      []int
	docComments     []cByteSpan
	header          []swiftToken
	imports         []cLineSpan
	frames          []swiftRecoveryFrame
	conditionals    []swiftRecoveryConditional
	definitions     []sourceDefinition
	headerBalance   swiftHeaderBalance
	conditionalOver int
	lastTokenLine   int
	lastTokenEnd    int
	headerDefStart  int
	headerScopeAt   int
	headerImportAt  int
	suspendedCount  int
	frameOverflow   int
	lineCount       int
	tokenLineIndex  int
	headerKind      swiftHeaderKind
	headerGap       bool
	headerControl   bool
	headerClassSeen bool
}

func analyzeSwiftLexically(source string, lineCount int) swiftLexicalAnalysis {
	lineCount = max(lineCount, 1)
	parser := swiftRecoveryParser{
		source:         source,
		lineStarts:     swiftLineStarts(source),
		lineCount:      lineCount,
		definitionSeen: make(map[swiftDefinitionKey]int),
		frames: []swiftRecoveryFrame{{
			kind: swiftRecoverySource, start: 1,
		}},
	}
	walkSwiftLexically(source, swiftLexicalSink{
		comment: func(span cByteSpan) bool {
			if swiftDocumentationComment(source, span) {
				parser.docComments = append(parser.docComments, span)
			}
			return true
		},
		literal: func(span cByteSpan) bool {
			parser.acceptToken(swiftToken{
				text: swiftLiteralToken, start: span.start, end: span.end,
				nameStart: span.start, kind: swiftTokenPunctuation,
			})
			return true
		},
		token: func(token swiftToken) bool {
			parser.acceptToken(token)
			return true
		},
	})
	parser.finishHeader(parser.lineCount)
	for len(parser.conditionals) > 0 {
		conditional := parser.conditionals[len(parser.conditionals)-1]
		parser.conditionals = parser.conditionals[:len(parser.conditionals)-1]
		parser.closeFramesTo(conditional.baseDepth, parser.lineCount)
		parser.addScope(conditional.branchStartLine, parser.lineCount)
	}
	parser.closeFramesTo(1, parser.lineCount)
	parser.attachDocumentation()
	return swiftLexicalAnalysis{
		definitions: swiftSortUniqueDefinitions(parser.definitions, lineCount),
		scopes:      cNormalizeTreeLineScopes(parser.scopes, lineCount),
		imports:     cNormalizeTreeLineSpans(parser.imports, lineCount),
	}
}

func (parser *swiftRecoveryParser) acceptToken(token swiftToken) {
	for parser.tokenLineIndex+1 < len(parser.lineStarts) &&
		parser.lineStarts[parser.tokenLineIndex+1] <= token.start {
		parser.tokenLineIndex++
	}
	line := max(1, min(parser.tokenLineIndex+1, parser.lineCount))
	physicalBreak := parser.lastTokenEnd > 0 &&
		swiftRecoveryGapHasPhysicalLineBreak(
			parser.source, parser.lastTokenEnd, token.start,
		)
	if parser.lastTokenLine > 0 &&
		(line > parser.lastTokenLine || token.lineStart || physicalBreak) {
		parser.prepareForLineStart(token, line)
	}
	parser.lastTokenLine = line
	parser.lastTokenEnd = max(parser.lastTokenEnd, token.end)

	switch token.text {
	case "#if", "#elseif", "#else", "#endif":
		parser.finishHeader(max(1, line-1))
		parser.acceptConditional(token.text, line)
		return
	case ";":
		parser.finishHeader(line)
		return
	case "{":
		parser.openBrace(line)
		return
	case "}":
		parser.finishHeader(line)
		parser.closeBrace(line, token.end)
		return
	}

	if token.lineStart && (token.text == "case" || token.text == "default") &&
		parser.currentFrameKind() == swiftRecoverySwitch {
		parser.finishHeader(max(1, line-1))
		parser.startSwitchCase(line)
	}
	parser.appendHeader(token)
}

func swiftRecoveryGapHasPhysicalLineBreak(source string, start, end int) bool {
	start = max(0, min(start, len(source)))
	end = max(start, min(end, len(source)))
	for index := start; index < end; index++ {
		if source[index] == '\n' || source[index] == '\r' {
			return true
		}
	}
	return false
}

func (parser *swiftRecoveryParser) prepareForLineStart(token swiftToken, line int) {
	if token.kind == swiftTokenInterpolationStart {
		return
	}
	if len(parser.header) == 0 && !parser.headerGap {
		return
	}
	if parser.headerGap {
		parser.clearHeader()
		return
	}
	if swiftDeclarationLineStarter(token) {
		openBalance := !parser.headerBalance.invalid &&
			(parser.headerBalance.parentheses > 0 || parser.headerBalance.brackets > 0 ||
				parser.headerBalance.angles > 0)
		callableHeader := parser.headerKind == swiftHeaderFunction ||
			parser.headerKind == swiftHeaderInit ||
			parser.headerKind == swiftHeaderSubscript ||
			parser.headerKind == swiftHeaderMacro
		attributeContinuation := token.text == "@" &&
			(openBalance || callableHeader && swiftHeaderContinues(parser.header, token) ||
				parser.headerKind == swiftHeaderProperty &&
					len(parser.header) > 0 && parser.header[len(parser.header)-1].text != "=" &&
					swiftHeaderContinues(parser.header, token))
		parameterLabel := openBalance && callableHeader &&
			swiftParameterLabelAt(parser.source, token.end)
		if attributeContinuation || parameterLabel {
			return
		}
		parser.finishHeader(max(1, line-1))
		return
	}
	balance := parser.headerBalance
	if balance.invalid {
		parser.clearHeader()
		return
	}
	if balance.parentheses > 0 || balance.brackets > 0 || balance.angles > 0 {
		return
	}
	kind := parser.headerKind
	switch kind {
	case swiftHeaderType, swiftHeaderFunction, swiftHeaderInit,
		swiftHeaderDeinit, swiftHeaderSubscript, swiftHeaderMacro:
		// These may put effects, constraints, return types, or an opening body
		// on a following physical line. A new declaration starter above is the
		// hard resynchronization boundary for a bodyless member.
		return
	case swiftHeaderProperty:
		if token.text == "{" || swiftHeaderContinues(parser.header, token) {
			return
		}
	case swiftHeaderNone, swiftHeaderTypeAlias, swiftHeaderAssociatedType,
		swiftHeaderOperator, swiftHeaderPrecedence, swiftHeaderEnumCase,
		swiftHeaderImport:
	}
	parser.finishHeader(max(1, line-1))
}

func swiftParameterLabelAt(source string, offset int) bool {
	var ok bool
	offset, ok = swiftSkipParameterTrivia(source, offset)
	if !ok {
		return false
	}
	if offset < len(source) && source[offset] == ':' {
		return true
	}
	_, end, _, ok := swiftIdentifierAt(source, offset)
	if !ok {
		return false
	}
	end, ok = swiftSkipParameterTrivia(source, end)
	if !ok {
		return false
	}
	return end < len(source) && source[end] == ':'
}

func swiftSkipParameterTrivia(source string, offset int) (int, bool) {
	for offset < len(source) {
		if swiftASCIIWhitespace(source[offset]) {
			offset++
			continue
		}
		if strings.HasPrefix(source[offset:], "//") {
			offset += 2
			for offset < len(source) && source[offset] != '\n' && source[offset] != '\r' {
				offset++
			}
			continue
		}
		if !strings.HasPrefix(source[offset:], "/*") {
			return offset, true
		}
		offset += 2
		depth := 1
		for offset < len(source) && depth > 0 {
			switch {
			case strings.HasPrefix(source[offset:], "/*"):
				depth++
				offset += 2
			case strings.HasPrefix(source[offset:], "*/"):
				depth--
				offset += 2
			default:
				offset++
			}
		}
		if depth != 0 {
			return offset, false
		}
	}
	return offset, true
}

func (parser *swiftRecoveryParser) appendHeader(token swiftToken) {
	if parser.headerGap {
		return
	}
	if len(parser.header) >= swiftMaximumHeaderTokens {
		parser.preserveOverflowProperty()
		parser.header = parser.header[:0]
		parser.headerBalance = swiftHeaderBalance{}
		parser.headerKind = swiftHeaderNone
		parser.headerControl = false
		parser.headerClassSeen = false
		parser.headerGap = true
		return
	}
	if len(parser.header) == 0 {
		parser.headerDefStart = len(parser.definitions)
		parser.headerScopeAt = len(parser.scopes)
		parser.headerImportAt = len(parser.imports)
	}
	parser.observeHeaderToken(token)
	parser.header = append(parser.header, token)
}

func (parser *swiftRecoveryParser) preserveOverflowProperty() {
	if parser.headerKind != swiftHeaderProperty ||
		!parser.headerBalance.propertyInitializer || parser.currentFrameSuppressed() {
		return
	}
	keywordIndex, keyword := swiftHeaderDeclaration(parser.header)
	if keyword != "let" && keyword != "var" {
		return
	}
	startLine := swiftTokenLine(parser.lineStarts, parser.header[0].start)
	endLine := swiftTokenLine(
		parser.lineStarts, parser.header[len(parser.header)-1].start,
	)
	for _, candidate := range swiftPropertyCandidates(
		parser.header, keywordIndex+1, false,
	) {
		candidate.ownsScope = false
		parser.addDefinition(candidate, startLine, endLine)
	}
}

func (parser *swiftRecoveryParser) clearHeader() {
	parser.header = parser.header[:0]
	parser.headerBalance = swiftHeaderBalance{}
	parser.headerKind = swiftHeaderNone
	parser.headerControl = false
	parser.headerClassSeen = false
	parser.headerGap = false
	parser.headerDefStart = len(parser.definitions)
	parser.headerScopeAt = len(parser.scopes)
	parser.headerImportAt = len(parser.imports)
}

func (parser *swiftRecoveryParser) observeHeaderToken(token swiftToken) {
	topLevel := parser.headerBalance.parentheses == 0 &&
		parser.headerBalance.brackets == 0
	if topLevel && !token.quotedIdentifier {
		if parser.headerKind == swiftHeaderNone && !parser.headerControl {
			if len(parser.header) > 0 {
				previous := parser.header[len(parser.header)-1].text
				if previous == "." || previous == "::" {
					parser.headerBalance.accept(token)
					return
				}
			}
			switch token.text {
			case "if", "guard", "for", "while", "switch", "catch", "return",
				"throw", "defer", "do":
				parser.headerControl = true
			default:
				parser.headerKind = swiftHeaderKindForKeyword(token.text)
				parser.headerClassSeen = token.text == "class" &&
					parser.headerKind == swiftHeaderType
			}
		} else if parser.headerClassSeen &&
			(parser.currentFrameKind() == swiftRecoveryType ||
				parser.currentFrameKind() == swiftRecoveryEnum) {
			switch token.text {
			case "func":
				parser.headerKind = swiftHeaderFunction
			case "var":
				parser.headerKind = swiftHeaderProperty
			case "subscript":
				parser.headerKind = swiftHeaderSubscript
			}
		}
	}
	parser.headerBalance.accept(token)
}

func (parser *swiftRecoveryParser) finishHeader(endLine int) {
	defer parser.clearHeader()
	if parser.headerGap || len(parser.header) == 0 || parser.currentFrameSuppressed() {
		return
	}
	balance := parser.headerBalance
	if balance.invalid || balance.parentheses != 0 || balance.brackets != 0 ||
		balance.angles != 0 {
		parser.rollbackHeaderSideEffects()
		return
	}
	kind, candidates, importSpan := parser.classifyHeader(false)
	if kind == swiftHeaderNone {
		parser.rollbackHeaderSideEffects()
		return
	}
	if importSpan.start > 0 {
		parser.imports = append(parser.imports, importSpan)
	}
	if len(candidates) == 0 {
		return
	}
	startLine := swiftTokenLine(parser.lineStarts, parser.header[0].start)
	startLine = max(1, min(startLine, parser.lineCount))
	endLine = max(startLine, min(endLine, parser.lineCount))
	for _, candidate := range candidates {
		parser.addDefinition(candidate, startLine, endLine)
	}
}

func (parser *swiftRecoveryParser) openBrace(line int) {
	if parser.frameOverflow > 0 {
		parser.frameOverflow++
		parser.clearHeader()
		return
	}
	if len(parser.frames) >= swiftMaximumStructuralDepth {
		parser.frameOverflow = 1
		parser.clearHeader()
		return
	}

	balance := parser.headerBalance
	if !parser.headerGap && len(parser.header) > 0 &&
		(balance.parentheses > 0 || balance.brackets > 0 || balance.angles > 0) {
		keywordIndex, declaration := swiftHeaderDeclaration(parser.header)
		malformedDeclaration := parser.headerKind != swiftHeaderNone && declaration == "" ||
			declaration != "" && swiftUnbalancedDeclarationContainer(
				parser.headerKind, balance, parser.header, keywordIndex,
			)
		suppressed := parser.currentFrameSuppressed() ||
			malformedDeclaration
		if malformedDeclaration {
			parser.rollbackHeaderSideEffects()
		}
		frame := swiftRecoveryFrame{
			kind: swiftRecoveryControl, start: line,
			emitScope: !suppressed, suppressed: suppressed,
		}
		if !malformedDeclaration &&
			parser.suspendedCount < swiftMaximumSuspendedHeaders {
			frame.suspendedHeader = append([]swiftToken(nil), parser.header...)
			frame.suspendedBalance = parser.headerBalance
			frame.suspendedDefStart = parser.headerDefStart
			frame.suspendedScopeAt = parser.headerScopeAt
			frame.suspendedImportAt = parser.headerImportAt
			frame.suspendedKind = parser.headerKind
			frame.suspendedGap = parser.headerGap
			frame.suspendedControl = parser.headerControl
			frame.suspendedClass = parser.headerClassSeen
			parser.suspendedCount++
		}
		parser.frames = append(parser.frames, frame)
		parser.clearHeader()
		return
	}

	headerKind, candidates, importSpan := parser.classifyHeader(true)
	headRejected := parser.currentFrameSuppressed() ||
		headerKind == swiftHeaderNone && parser.headerKind != swiftHeaderNone &&
			!parser.headerControl
	if headRejected {
		parser.rollbackHeaderSideEffects()
		candidates = nil
	} else if importSpan.start > 0 {
		parser.imports = append(parser.imports, importSpan)
	}
	suppressChildren := headRejected ||
		swiftHeaderSuppressesBraceChildren(headerKind, parser.header)
	startLine := line
	if len(parser.header) > 0 {
		startLine = swiftTokenLine(parser.lineStarts, parser.header[0].start)
	}
	startLine = max(1, min(startLine, parser.lineCount))
	frameKind := swiftRecoveryControl
	switch headerKind {
	case swiftHeaderType:
		frameKind = swiftRecoveryType
		if swiftHeaderDeclarationKeyword(parser.header) == "enum" {
			frameKind = swiftRecoveryEnum
		} else if swiftHeaderDeclarationKeyword(parser.header) == "protocol" {
			frameKind = swiftRecoveryProtocol
		}
	case swiftHeaderFunction, swiftHeaderInit, swiftHeaderDeinit,
		swiftHeaderSubscript, swiftHeaderMacro, swiftHeaderOperator,
		swiftHeaderPrecedence:
		frameKind = swiftRecoveryFunction
	case swiftHeaderProperty:
		if swiftHeaderContainsTopLevel(parser.header, "switch") {
			frameKind = swiftRecoverySwitch
		} else {
			frameKind = swiftRecoveryProperty
		}
	case swiftHeaderNone, swiftHeaderTypeAlias, swiftHeaderAssociatedType,
		swiftHeaderEnumCase, swiftHeaderImport:
		if swiftHeaderContainsTopLevel(parser.header, "switch") {
			frameKind = swiftRecoverySwitch
		}
	}
	emitScope := !headRejected && (headerKind != swiftHeaderNone ||
		parser.headerKind == swiftHeaderNone || parser.headerControl)
	frame := swiftRecoveryFrame{
		kind: frameKind, start: startLine, emitScope: emitScope,
		suppressed: suppressChildren,
	}
	for _, candidate := range candidates {
		index := parser.addDefinition(candidate, startLine, line)
		if index >= 0 && parser.definitions[index].ownsScope {
			frame.definitionIndexes = append(frame.definitionIndexes, index)
		}
	}
	parser.frames = append(parser.frames, frame)
	parser.clearHeader()
}

func swiftHeaderSuppressesBraceChildren(kind swiftHeaderKind, tokens []swiftToken) bool {
	switch kind {
	case swiftHeaderTypeAlias, swiftHeaderAssociatedType, swiftHeaderOperator,
		swiftHeaderPrecedence, swiftHeaderEnumCase, swiftHeaderImport:
		return true
	case swiftHeaderMacro:
		return !swiftHeaderContainsTopLevel(tokens, "=")
	case swiftHeaderNone, swiftHeaderType, swiftHeaderFunction, swiftHeaderProperty,
		swiftHeaderInit, swiftHeaderDeinit, swiftHeaderSubscript:
		return false
	}
	return false
}

func swiftUnbalancedDeclarationContainer(
	kind swiftHeaderKind,
	balance swiftHeaderBalance,
	tokens []swiftToken,
	keywordIndex int,
) bool {
	switch kind {
	case swiftHeaderType, swiftHeaderTypeAlias, swiftHeaderAssociatedType,
		swiftHeaderDeinit, swiftHeaderOperator, swiftHeaderPrecedence:
		return true
	case swiftHeaderFunction, swiftHeaderInit, swiftHeaderSubscript, swiftHeaderMacro:
		if balance.angles > 0 {
			return true
		}
		return (balance.parentheses > 0 || balance.brackets > 0) &&
			!swiftCallableDefaultClosurePlausible(tokens, keywordIndex)
	case swiftHeaderProperty:
		return !balance.propertyInitializer
	case swiftHeaderEnumCase:
		return !swiftAssociatedValueDefaultClosurePlausible(tokens, keywordIndex)
	case swiftHeaderImport:
		return true
	case swiftHeaderNone:
		return false
	}
	return false
}

func swiftCallableDefaultClosurePlausible(tokens []swiftToken, keywordIndex int) bool {
	parameterOpen := -1
	for index := keywordIndex + 1; index < len(tokens); index++ {
		if tokens[index].text == "(" {
			parameterOpen = index
			break
		}
	}
	return swiftParameterDefaultClosurePlausible(tokens, parameterOpen)
}

func swiftAssociatedValueDefaultClosurePlausible(
	tokens []swiftToken,
	keywordIndex int,
) bool {
	parameterOpen := -1
	for index := keywordIndex + 1; index < len(tokens); index++ {
		if tokens[index].text == "(" {
			parameterOpen = index
			break
		}
	}
	return swiftParameterDefaultClosurePlausible(tokens, parameterOpen)
}

func swiftParameterDefaultClosurePlausible(tokens []swiftToken, parameterOpen int) bool {
	if parameterOpen < 0 || parameterOpen >= len(tokens) ||
		tokens[parameterOpen].text != "(" {
		return false
	}
	parentheses, brackets, angles := 1, 0, 0
	segmentStart := parameterOpen + 1
	colon, equal := -1, -1
	for index := parameterOpen + 1; index < len(tokens); index++ {
		text := tokens[index].text
		closeCount := swiftGenericAngleCloseCount(text)
		if closeCount > 0 && angles > 0 && parentheses == 1 && brackets == 0 {
			angles = max(0, angles-closeCount)
			continue
		}
		topParameter := parentheses == 1 && brackets == 0 && angles == 0
		if topParameter {
			switch text {
			case ",":
				segmentStart = index + 1
				colon, equal = -1, -1
				continue
			case ":":
				if colon < segmentStart {
					colon = index
				}
				continue
			case "=":
				if equal < segmentStart {
					equal = index
				}
				continue
			}
		}
		switch text {
		case "(":
			if parentheses == 0 && brackets == 0 && angles == 0 {
				segmentStart = index + 1
				colon, equal = -1, -1
			}
			parentheses++
		case ")":
			parentheses--
		case "[":
			brackets++
		case "]":
			brackets = max(0, brackets-1)
		case "<":
			if parentheses == 1 && brackets == 0 {
				angles++
			}
		}
	}
	return colon >= segmentStart && equal > colon+1
}

func (parser *swiftRecoveryParser) closeBrace(line, endOffset int) {
	if parser.frameOverflow > 0 {
		parser.frameOverflow--
		return
	}
	if len(parser.frames) <= 1 {
		parser.clearHeader()
		return
	}
	frame := parser.frames[len(parser.frames)-1]
	parser.frames = parser.frames[:len(parser.frames)-1]
	endColumn := 0
	if line >= 1 && line <= len(parser.lineStarts) && endOffset > 0 {
		endColumn = endOffset - parser.lineStarts[line-1] + 1
	}
	parser.closeFrame(frame, line, endColumn)
	if len(frame.suspendedHeader) > 0 {
		parser.header = frame.suspendedHeader
		parser.headerBalance = frame.suspendedBalance
		parser.headerKind = frame.suspendedKind
		parser.headerGap = frame.suspendedGap
		parser.headerControl = frame.suspendedControl
		parser.headerClassSeen = frame.suspendedClass
		parser.headerDefStart = frame.suspendedDefStart
		parser.headerScopeAt = frame.suspendedScopeAt
		parser.headerImportAt = frame.suspendedImportAt
		if len(parser.header) < swiftMaximumHeaderTokens {
			parser.header = append(parser.header, swiftToken{
				text: swiftLiteralToken, nameStart: -1,
				kind: swiftTokenPunctuation,
			})
		}
		parser.suspendedCount = max(0, parser.suspendedCount-1)
	}
}

func (parser *swiftRecoveryParser) rollbackHeaderSideEffects() {
	definitionStart := max(0, min(parser.headerDefStart, len(parser.definitions)))
	for index := definitionStart; index < len(parser.definitions); index++ {
		definition := parser.definitions[index]
		key := swiftDefinitionKey{
			symbol: definition.symbol, line: definition.line, column: definition.column,
		}
		if seenIndex, exists := parser.definitionSeen[key]; exists &&
			seenIndex >= definitionStart {
			delete(parser.definitionSeen, key)
		}
	}
	parser.definitions = parser.definitions[:definitionStart]
	scopeStart := max(0, min(parser.headerScopeAt, len(parser.scopes)))
	parser.scopes = parser.scopes[:scopeStart]
	importStart := max(0, min(parser.headerImportAt, len(parser.imports)))
	parser.imports = parser.imports[:importStart]
}

func (parser *swiftRecoveryParser) closeFrame(
	frame swiftRecoveryFrame,
	line, ownedEndColumn int,
) {
	line = max(frame.start, min(line, parser.lineCount))
	if frame.emitScope {
		if frame.caseStart > 0 {
			parser.addScope(frame.caseStart, line)
		}
		parser.addScope(frame.start, line)
	}
	for _, index := range frame.definitionIndexes {
		if index >= 0 && index < len(parser.definitions) {
			definition := &parser.definitions[index]
			definition.scopeEnd = max(definition.scopeEnd, line)
			if ownedEndColumn > 0 && definition.scopeEnd == line {
				definition.ownedEndColumn = ownedEndColumn
			}
		}
	}
}

func (parser *swiftRecoveryParser) closeFramesTo(depth, line int) {
	depth = max(1, min(depth, len(parser.frames)))
	for len(parser.frames) > depth {
		frame := parser.frames[len(parser.frames)-1]
		parser.frames = parser.frames[:len(parser.frames)-1]
		parser.closeFrame(frame, line, 0)
		if len(frame.suspendedHeader) > 0 {
			parser.suspendedCount = max(0, parser.suspendedCount-1)
		}
	}
}

func (parser *swiftRecoveryParser) acceptConditional(directive string, line int) {
	switch directive {
	case "#if":
		if len(parser.conditionals) >= swiftMaximumConcreteDirectiveDepth {
			parser.conditionalOver++
			return
		}
		parser.conditionals = append(parser.conditionals, swiftRecoveryConditional{
			baseDepth: len(parser.frames), branchStartLine: line,
		})
	case "#elseif", "#else":
		if parser.conditionalOver > 0 || len(parser.conditionals) == 0 {
			return
		}
		conditional := &parser.conditionals[len(parser.conditionals)-1]
		parser.closeFramesTo(conditional.baseDepth, max(conditional.branchStartLine, line-1))
		parser.addScope(conditional.branchStartLine, line)
		conditional.branchStartLine = line
	case "#endif":
		if parser.conditionalOver > 0 {
			parser.conditionalOver--
			return
		}
		if len(parser.conditionals) == 0 {
			return
		}
		last := len(parser.conditionals) - 1
		conditional := parser.conditionals[last]
		parser.conditionals = parser.conditionals[:last]
		parser.closeFramesTo(conditional.baseDepth, max(conditional.branchStartLine, line-1))
		parser.addScope(conditional.branchStartLine, line)
	}
}

func (parser *swiftRecoveryParser) startSwitchCase(line int) {
	if len(parser.frames) == 0 {
		return
	}
	frame := &parser.frames[len(parser.frames)-1]
	if frame.kind != swiftRecoverySwitch {
		return
	}
	if frame.caseStart > 0 {
		parser.addScope(frame.caseStart, line)
	}
	frame.caseStart = line
}

func (parser *swiftRecoveryParser) currentFrameKind() swiftRecoveryFrameKind {
	if len(parser.frames) == 0 {
		return swiftRecoverySource
	}
	return parser.frames[len(parser.frames)-1].kind
}

func (parser *swiftRecoveryParser) currentFrameSuppressed() bool {
	return len(parser.frames) > 0 && parser.frames[len(parser.frames)-1].suppressed
}

func (parser *swiftRecoveryParser) addScope(start, end int) {
	start = max(1, start)
	end = min(parser.lineCount, end)
	if start <= end {
		parser.scopes = append(parser.scopes, cLineScope{start: start, end: end})
	}
}

type swiftHeaderKind uint8

const (
	swiftHeaderNone swiftHeaderKind = iota
	swiftHeaderType
	swiftHeaderFunction
	swiftHeaderProperty
	swiftHeaderTypeAlias
	swiftHeaderAssociatedType
	swiftHeaderInit
	swiftHeaderDeinit
	swiftHeaderSubscript
	swiftHeaderMacro
	swiftHeaderOperator
	swiftHeaderPrecedence
	swiftHeaderEnumCase
	swiftHeaderImport
)

func swiftHeaderKindForKeyword(keyword string) swiftHeaderKind {
	switch keyword {
	case "class", "struct", "actor", "enum", "protocol", "extension":
		return swiftHeaderType
	case "func":
		return swiftHeaderFunction
	case "let", "var":
		return swiftHeaderProperty
	case "typealias":
		return swiftHeaderTypeAlias
	case "associatedtype":
		return swiftHeaderAssociatedType
	case "init":
		return swiftHeaderInit
	case "deinit":
		return swiftHeaderDeinit
	case "subscript":
		return swiftHeaderSubscript
	case "macro":
		return swiftHeaderMacro
	case "operator":
		return swiftHeaderOperator
	case "precedencegroup":
		return swiftHeaderPrecedence
	case "case":
		return swiftHeaderEnumCase
	case "import":
		return swiftHeaderImport
	default:
		return swiftHeaderNone
	}
}

type swiftDefinitionCandidate struct {
	symbol    string
	nameStart int
	ownsScope bool
}

func (parser *swiftRecoveryParser) classifyHeader(
	hasBody bool,
) (swiftHeaderKind, []swiftDefinitionCandidate, cLineSpan) {
	if parser.headerGap || len(parser.header) == 0 {
		return swiftHeaderNone, nil, cLineSpan{}
	}
	keywordIndex, keyword := swiftHeaderDeclaration(parser.header)
	if keywordIndex < 0 {
		return swiftHeaderNone, nil, cLineSpan{}
	}
	startLine := swiftTokenLine(parser.lineStarts, parser.header[0].start)
	endLine := swiftTokenLine(parser.lineStarts, parser.header[len(parser.header)-1].end)
	switch keyword {
	case "import":
		if !parser.allowsImport() || hasBody ||
			!swiftImportHeaderValid(parser.header, keywordIndex+1) {
			return swiftHeaderNone, nil, cLineSpan{}
		}
		return swiftHeaderImport, nil, cLineSpan{start: startLine, end: endLine}
	case "class", "struct", "actor", "enum", "protocol":
		name := swiftNextHeaderName(parser.header, keywordIndex+1)
		if name.text == "" ||
			!swiftNominalHeaderSuffixValid(parser.header, keywordIndex+2) {
			return swiftHeaderNone, nil, cLineSpan{}
		}
		return swiftHeaderType, swiftCandidate(name, true), cLineSpan{}
	case "extension":
		symbol, nameStart, valid := swiftExtensionTarget(parser.header, keywordIndex+1)
		if !valid {
			return swiftHeaderNone, nil, cLineSpan{}
		}
		if symbol == "" {
			return swiftHeaderType, nil, cLineSpan{}
		}
		return swiftHeaderType, []swiftDefinitionCandidate{{
			symbol: symbol, nameStart: nameStart, ownsScope: true,
		}}, cLineSpan{}
	case "func":
		name := swiftNextFunctionName(parser.header, keywordIndex+1)
		if !swiftFunctionNameShape(parser.header, keywordIndex+1, name) ||
			!swiftFunctionHeaderSuffixValid(parser.header, keywordIndex+2) {
			return swiftHeaderNone, nil, cLineSpan{}
		}
		return swiftHeaderFunction, swiftCandidate(name, true), cLineSpan{}
	case "let", "var":
		if !parser.allowsProperty() {
			return swiftHeaderNone, nil, cLineSpan{}
		}
		candidates := swiftPropertyCandidates(parser.header, keywordIndex+1, hasBody)
		if !swiftPropertyHeaderValid(parser.header, keywordIndex+1, candidates) {
			return swiftHeaderNone, nil, cLineSpan{}
		}
		return swiftHeaderProperty, candidates, cLineSpan{}
	case "typealias":
		name := swiftNextHeaderName(parser.header, keywordIndex+1)
		if name.text == "" ||
			!swiftTypeAliasHeaderValid(parser.header, keywordIndex+2) {
			return swiftHeaderNone, nil, cLineSpan{}
		}
		return swiftHeaderTypeAlias, swiftCandidate(name, false), cLineSpan{}
	case "associatedtype":
		if parser.currentFrameKind() != swiftRecoveryProtocol &&
			parser.currentFrameKind() != swiftRecoveryType {
			return swiftHeaderNone, nil, cLineSpan{}
		}
		name := swiftNextHeaderName(parser.header, keywordIndex+1)
		if name.text == "" ||
			!swiftAssociatedTypeHeaderValid(parser.header, keywordIndex+2) {
			return swiftHeaderNone, nil, cLineSpan{}
		}
		return swiftHeaderAssociatedType, swiftCandidate(name, false), cLineSpan{}
	case "init":
		if !parser.allowsTypeMember() ||
			!swiftInitializerHeaderValid(parser.header, keywordIndex+1) {
			return swiftHeaderNone, nil, cLineSpan{}
		}
		return swiftHeaderInit, []swiftDefinitionCandidate{{
			symbol: "init", nameStart: parser.header[keywordIndex].nameStart,
			ownsScope: true,
		}}, cLineSpan{}
	case "deinit":
		if !parser.allowsTypeMember() || !hasBody ||
			keywordIndex+1 != len(parser.header) {
			return swiftHeaderNone, nil, cLineSpan{}
		}
		return swiftHeaderDeinit, []swiftDefinitionCandidate{{
			symbol: "deinit", nameStart: parser.header[keywordIndex].nameStart,
			ownsScope: true,
		}}, cLineSpan{}
	case "subscript":
		if !parser.allowsTypeMember() || !hasBody ||
			!swiftSubscriptHeaderValid(parser.header, keywordIndex+1) {
			return swiftHeaderNone, nil, cLineSpan{}
		}
		return swiftHeaderSubscript, []swiftDefinitionCandidate{{
			symbol: "subscript", nameStart: parser.header[keywordIndex].nameStart,
			ownsScope: true,
		}}, cLineSpan{}
	case "macro":
		name := swiftNextHeaderName(parser.header, keywordIndex+1)
		if name.text == "" ||
			!swiftMacroHeaderValid(parser.header, keywordIndex+2, hasBody) {
			return swiftHeaderNone, nil, cLineSpan{}
		}
		return swiftHeaderMacro, swiftCandidate(name, true), cLineSpan{}
	case "operator":
		name := swiftNextOperatorName(parser.header, keywordIndex+1)
		if name.text == "" ||
			!swiftOperatorHeaderValid(parser.header, keywordIndex) {
			return swiftHeaderNone, nil, cLineSpan{}
		}
		return swiftHeaderOperator, swiftCandidate(name, true), cLineSpan{}
	case "precedencegroup":
		name := swiftNextHeaderName(parser.header, keywordIndex+1)
		if name.text == "" || !hasBody || keywordIndex+2 != len(parser.header) {
			return swiftHeaderNone, nil, cLineSpan{}
		}
		return swiftHeaderPrecedence, swiftCandidate(name, true), cLineSpan{}
	case "case":
		if parser.currentFrameKind() != swiftRecoveryEnum {
			return swiftHeaderNone, nil, cLineSpan{}
		}
		return swiftHeaderEnumCase,
			swiftEnumCaseCandidates(parser.header, keywordIndex+1), cLineSpan{}
	default:
		return swiftHeaderNone, nil, cLineSpan{}
	}
}

func swiftCandidate(token swiftToken, ownsScope bool) []swiftDefinitionCandidate {
	if !swiftNameToken(token) && !swiftOperatorSymbol(token.text) {
		return nil
	}
	return []swiftDefinitionCandidate{{
		symbol: token.text, nameStart: token.nameStart, ownsScope: ownsScope,
	}}
}

func (parser *swiftRecoveryParser) addDefinition(
	candidate swiftDefinitionCandidate,
	startLine, endLine int,
) int {
	if candidate.symbol == "" || candidate.nameStart < 0 ||
		candidate.nameStart >= len(parser.source) {
		return -1
	}
	line := swiftTokenLine(parser.lineStarts, candidate.nameStart)
	line = max(1, min(line, parser.lineCount))
	column := candidate.nameStart - parser.lineStarts[line-1] + 1
	scopeStart, scopeEnd := line, line
	if candidate.ownsScope {
		scopeStart = min(line, max(1, startLine))
		scopeEnd = max(line, min(parser.lineCount, endLine))
	}
	definition := sourceDefinition{
		symbol: candidate.symbol, line: line, column: column,
		scopeStart: scopeStart, scopeEnd: scopeEnd, ownsScope: candidate.ownsScope,
	}
	key := swiftDefinitionKey{symbol: definition.symbol, line: line, column: column}
	if index, exists := parser.definitionSeen[key]; exists {
		current := &parser.definitions[index]
		if definition.ownsScope && !current.ownsScope {
			*current = definition
		} else if definition.ownsScope == current.ownsScope {
			current.scopeStart = min(current.scopeStart, definition.scopeStart)
			current.scopeEnd = max(current.scopeEnd, definition.scopeEnd)
		}
		return index
	}
	parser.definitionSeen[key] = len(parser.definitions)
	parser.definitions = append(parser.definitions, definition)
	return len(parser.definitions) - 1
}

func (parser *swiftRecoveryParser) allowsProperty() bool {
	switch parser.currentFrameKind() {
	case swiftRecoverySource, swiftRecoveryType, swiftRecoveryEnum, swiftRecoveryProtocol:
		return true
	case swiftRecoveryFunction, swiftRecoveryProperty, swiftRecoverySwitch,
		swiftRecoveryControl:
		return false
	}
	return false
}

func (parser *swiftRecoveryParser) allowsTypeMember() bool {
	switch parser.currentFrameKind() {
	case swiftRecoveryType, swiftRecoveryEnum, swiftRecoveryProtocol:
		return true
	case swiftRecoverySource, swiftRecoveryFunction, swiftRecoveryProperty,
		swiftRecoverySwitch, swiftRecoveryControl:
		return false
	}
	return false
}

func (parser *swiftRecoveryParser) allowsImport() bool {
	return parser.currentFrameKind() == swiftRecoverySource
}

func swiftHeaderDeclaration(tokens []swiftToken) (int, string) {
	for index := 0; index < len(tokens); {
		token := tokens[index]
		if token.gap || token.quotedIdentifier {
			return -1, ""
		}
		if token.text == "@" {
			next, ok := swiftSkipDeclarationAttribute(tokens, index)
			if !ok {
				return -1, ""
			}
			index = next
			continue
		}
		if token.text == "class" {
			if index+1 < len(tokens) {
				switch tokens[index+1].text {
				case "func", "init", "var", "subscript":
					index++
					continue
				}
			}
			return index, token.text
		}
		if swiftDeclarationModifier(token.text) {
			index++
			if index < len(tokens) && tokens[index].text == "(" {
				next, ok := swiftSkipBalancedHeaderTokens(tokens, index)
				if !ok {
					return -1, ""
				}
				index = next
			}
			continue
		}
		if swiftHeaderKindForKeyword(token.text) != swiftHeaderNone {
			return index, token.text
		}
		return -1, ""
	}
	return -1, ""
}

func swiftHeaderDeclarationKeyword(tokens []swiftToken) string {
	_, keyword := swiftHeaderDeclaration(tokens)
	return keyword
}

func swiftSkipDeclarationAttribute(tokens []swiftToken, start int) (int, bool) {
	index := start + 1
	if index >= len(tokens) || tokens[index].kind != swiftTokenIdentifier {
		return start, false
	}
	index++
	for index+1 < len(tokens) && tokens[index].text == "." &&
		tokens[index+1].kind == swiftTokenIdentifier {
		index += 2
	}
	if index < len(tokens) && tokens[index].text == "(" {
		return swiftSkipBalancedHeaderTokens(tokens, index)
	}
	return index, true
}

func swiftSkipBalancedHeaderTokens(tokens []swiftToken, start int) (int, bool) {
	if start < 0 || start >= len(tokens) || tokens[start].text != "(" {
		return start, false
	}
	depth := 0
	for index := start; index < len(tokens); index++ {
		switch tokens[index].text {
		case "(":
			depth++
		case ")":
			depth--
			if depth == 0 {
				return index + 1, true
			}
		}
	}
	return start, false
}

func swiftDeclarationModifier(word string) bool {
	switch word {
	case "public", "private", "fileprivate", "internal", "open", "package",
		"final", "indirect", "distributed", "nonisolated", "isolated", "static",
		"convenience", "dynamic", "lazy", "optional", "override", "required",
		"mutating", "nonmutating", "weak", "unowned", "unsafe", "nonsending",
		"borrowing", "consuming", "prefix", "infix", "postfix":
		return true
	default:
		return false
	}
}

func swiftNextHeaderName(tokens []swiftToken, start int) swiftToken {
	if start >= 0 && start < len(tokens) && swiftNameToken(tokens[start]) {
		return tokens[start]
	}
	return swiftToken{}
}

func swiftNextFunctionName(tokens []swiftToken, start int) swiftToken {
	if start >= 0 && start < len(tokens) &&
		(swiftNameToken(tokens[start]) || swiftOperatorSymbol(tokens[start].text)) {
		return tokens[start]
	}
	return swiftToken{}
}

func swiftFunctionNameShape(tokens []swiftToken, nameIndex int, name swiftToken) bool {
	if name.text == "" || nameIndex < 0 || nameIndex+1 >= len(tokens) {
		return false
	}
	next := tokens[nameIndex+1].text
	if next == "(" {
		return true
	}
	return next == "<"
}

func swiftFunctionHeaderSuffixValid(tokens []swiftToken, start int) bool {
	return swiftCallableHeaderSuffixValid(tokens, start, true, true, true)
}

func swiftCallableHeaderSuffixValid(
	tokens []swiftToken,
	start int,
	allowReturn, allowEffects, allowIUOReturn bool,
) bool {
	index := start
	if index < len(tokens) &&
		(tokens[index].text == "<" || tokens[index].text == "?<" ||
			tokens[index].text == "!<") {
		var ok bool
		index, ok = swiftSkipNominalGenericClause(tokens, index)
		if !ok {
			return false
		}
	}
	if index >= len(tokens) || tokens[index].text != "(" {
		return false
	}
	for index < len(tokens) && tokens[index].text == "(" {
		next, balanced := swiftSkipBalancedHeaderTokens(tokens, index)
		if !balanced || !swiftCallableParameterClauseValid(tokens, index+1, next-1) {
			return false
		}
		index = next
	}
	seenAsync, seenThrows := false, false
	for index < len(tokens) {
		switch tokens[index].text {
		case "async":
			if !allowEffects || seenAsync || seenThrows {
				return false
			}
			seenAsync = true
			index++
		case "throws", "rethrows":
			if !allowEffects || seenThrows {
				return false
			}
			seenThrows = true
			effect := tokens[index].text
			index++
			if index < len(tokens) && tokens[index].text == "(" {
				next, balanced := swiftSkipBalancedHeaderTokens(tokens, index)
				if effect != "throws" || !balanced ||
					!swiftTypedThrowsClauseValid(tokens, index+1, next-1) {
					return false
				}
				index = next
			}
		default:
			goto effectsDone
		}
	}

effectsDone:
	if index < len(tokens) && tokens[index].text == "->" {
		if !allowReturn {
			return false
		}
		index++
		returnStart := index
		for index < len(tokens) &&
			(tokens[index].text != "where" || tokens[index].quotedIdentifier) {
			index++
		}
		if !swiftTypeTokensValidContext(tokens, returnStart, index, allowIUOReturn) {
			return false
		}
	}
	if index < len(tokens) && tokens[index].text == "where" &&
		!tokens[index].quotedIdentifier {
		return swiftWhereRequirementsValid(tokens, index+1)
	}
	return index == len(tokens)
}

func swiftCallableParameterClauseValid(tokens []swiftToken, start, end int) bool {
	if start == end {
		return true
	}
	parentheses, brackets, angles := 0, 0, 0
	segmentStart := start
	for index := start; index <= end; index++ {
		if index == end {
			return segmentStart == end ||
				swiftCallableParameterSegmentValid(tokens, segmentStart, end)
		}
		text := tokens[index].text
		closeCount := swiftGenericAngleCloseCount(text)
		if closeCount > 0 && angles > 0 {
			angles = max(0, angles-closeCount)
			continue
		}
		if parentheses == 0 && brackets == 0 && angles == 0 && text == "," {
			if !swiftCallableParameterSegmentValid(tokens, segmentStart, index) {
				return false
			}
			segmentStart = index + 1
			continue
		}
		switch text {
		case "(":
			parentheses++
		case ")":
			parentheses--
		case "[":
			brackets++
		case "]":
			brackets--
		case "<":
			angles++
		}
	}
	return false
}

func swiftCallableParameterSegmentValid(tokens []swiftToken, start, end int) bool {
	if start >= end {
		return false
	}
	parentheses, brackets, angles := 0, 0, 0
	colon, equal := -1, -1
	for index := start; index < end; index++ {
		text := tokens[index].text
		closeCount := swiftGenericAngleCloseCount(text)
		if closeCount > 0 && angles > 0 {
			angles = max(0, angles-closeCount)
			continue
		}
		if parentheses == 0 && brackets == 0 && angles == 0 {
			if equal >= 0 {
				continue
			}
			switch text {
			case ":":
				if colon >= 0 || equal >= 0 {
					return false
				}
				colon = index
				continue
			case "=":
				if colon < 0 || equal >= 0 {
					return false
				}
				equal = index
				continue
			}
		}
		switch text {
		case "(":
			parentheses++
		case ")":
			parentheses--
		case "[":
			brackets++
		case "]":
			brackets--
		case "<":
			angles++
		}
	}
	if colon <= start {
		return false
	}
	typeEnd := end
	if equal >= 0 {
		typeEnd = equal
		if equal+1 >= end ||
			!swiftDefaultExpressionShapeValid(tokens, equal+1, end) {
			return false
		}
	}
	if typeEnd > colon+1 && tokens[typeEnd-1].text == "..." {
		typeEnd--
	}
	return swiftParameterNameTokensValid(tokens, start, colon) &&
		swiftTypeTokensValidContext(tokens, colon+1, typeEnd, true)
}

func swiftDefaultExpressionShapeValid(tokens []swiftToken, start, end int) bool {
	previousOperand := false
	for index := start; index < end; index++ {
		token := tokens[index]
		operand := swiftExpressionOperandToken(token)
		if operand && previousOperand &&
			!swiftExpressionWordOperator(tokens[index-1].text) &&
			!swiftExpressionWordOperator(token.text) {
			return false
		}
		previousOperand = operand
	}
	return start < end
}

func swiftExpressionOperandToken(token swiftToken) bool {
	return token.kind == swiftTokenIdentifier || token.kind == swiftTokenNumber ||
		token.text == swiftLiteralToken
}

func swiftExpressionWordOperator(text string) bool {
	switch text {
	case "as", "is", "await", "try", "consume", "copy", "each", "repeat",
		"some", "any":
		return true
	default:
		return false
	}
}

func swiftParameterNameTokensValid(tokens []swiftToken, start, end int) bool {
	names := 0
	for index := start; index < end; index++ {
		if tokens[index].text == "@" {
			if names > 0 {
				return false
			}
			next, ok := swiftSkipDeclarationAttribute(tokens[:end], index)
			if !ok {
				return false
			}
			index = next - 1
			continue
		}
		if names == 0 && swiftParameterPrefixModifier(tokens[index].text) {
			continue
		}
		if tokens[index].kind == swiftTokenIdentifier && !tokens[index].gap ||
			tokens[index].text == "_" {
			names++
			if names > 2 {
				return false
			}
			continue
		}
		return false
	}
	return names > 0
}

func swiftParameterPrefixModifier(text string) bool {
	switch text {
	case "isolated", "borrowing", "consuming", "inout", "sending", "__owned",
		"__shared", "repeat", "each":
		return true
	default:
		return false
	}
}

func swiftTypedThrowsClauseValid(tokens []swiftToken, start, end int) bool {
	return swiftTypeTokensValidMode(tokens, start, end, false, false)
}

func swiftInitializerHeaderValid(tokens []swiftToken, start int) bool {
	index := start
	if index < len(tokens) && (tokens[index].text == "?" || tokens[index].text == "!") {
		index++
	}
	return swiftCallableHeaderSuffixValid(tokens, index, false, true, true)
}

func swiftSubscriptHeaderValid(tokens []swiftToken, start int) bool {
	return swiftCallableHeaderSuffixValid(tokens, start, true, false, true)
}

func swiftMacroHeaderValid(tokens []swiftToken, start int, hasBody bool) bool {
	equal := swiftFindTopLevelToken(tokens, start, "=")
	signatureEnd := len(tokens)
	if equal >= 0 {
		signatureEnd = equal
	}
	if !swiftCallableHeaderSuffixValid(tokens[:signatureEnd], start, true, false, false) {
		return false
	}
	if equal < 0 {
		return !hasBody
	}
	return equal+1 < len(tokens) || hasBody
}

func swiftTypeAliasHeaderValid(tokens []swiftToken, start int) bool {
	index := start
	if index < len(tokens) && tokens[index].text == "<" {
		var ok bool
		index, ok = swiftSkipNominalGenericClause(tokens, index)
		if !ok {
			return false
		}
	}
	return index < len(tokens) && tokens[index].text == "=" &&
		swiftTypeTokensValid(tokens, index+1, len(tokens))
}

func swiftAssociatedTypeHeaderValid(tokens []swiftToken, start int) bool {
	index := start
	if index < len(tokens) && tokens[index].text == ":" {
		index++
		end := swiftFirstTopLevelToken(tokens, index, "where", "=")
		if !swiftTypeTokensValid(tokens, index, end) {
			return false
		}
		index = end
	}
	if index < len(tokens) && tokens[index].text == "where" {
		equal := swiftFindTopLevelToken(tokens, index+1, "=")
		whereEnd := len(tokens)
		if equal >= 0 {
			whereEnd = equal
		}
		if !swiftWhereRequirementsValid(tokens[:whereEnd], index+1) {
			return false
		}
		index = whereEnd
	}
	if index < len(tokens) && tokens[index].text == "=" {
		return swiftTypeTokensValid(tokens, index+1, len(tokens))
	}
	return index == len(tokens)
}

func swiftTypeTokensValid(tokens []swiftToken, start, end int) bool {
	return swiftTypeTokensValidContext(tokens, start, end, false)
}

func swiftTypeTokensValidContext(
	tokens []swiftToken,
	start, end int,
	allowIUO bool,
) bool {
	return swiftTypeTokensValidMode(tokens, start, end, allowIUO, true)
}

func swiftTypeTokensValidMode(
	tokens []swiftToken,
	start, end int,
	allowIUO, allowAttributes bool,
) bool {
	if start < 0 || end > len(tokens) || start >= end {
		return false
	}
	index := start
	for index < end {
		for index < end && tokens[index].text == "@" {
			if !allowAttributes {
				return false
			}
			next, ok := swiftSkipInheritanceAttribute(tokens[:end], index)
			if !ok {
				return false
			}
			index = next
		}
		if index >= end {
			return false
		}
		if tokens[index].text == "repeat" && index+1 < end {
			index++
		}
		for index < end && index+1 < end &&
			swiftTypePrefix(tokens[index].text, allowIUO) {
			index++
		}
		if index >= end {
			return false
		}
		switch tokens[index].text {
		case "(":
			next, ok := swiftSkipBalancedHeaderTokens(tokens[:end], index)
			if !ok || !swiftTupleTypeValid(
				tokens, index, next, allowIUO, allowAttributes,
			) {
				return false
			}
			index = next
		case "[":
			next, ok := swiftSkipBalancedSquareTokens(tokens[:end], index)
			if !ok || !swiftBracketTypeValid(
				tokens, index, next, allowIUO, allowAttributes,
			) {
				return false
			}
			index = next
		default:
			if !swiftTypeNameToken(tokens[index]) {
				return false
			}
			index++
		}
		for index < end {
			switch tokens[index].text {
			case "<":
				next, ok := swiftSkipTypeGenericClause(tokens[:end], index)
				if !ok {
					return false
				}
				index = next
			case ".", "::":
				if index+1 >= end || !swiftTypeNameToken(tokens[index+1]) {
					return false
				}
				index += 2
			case "?", "!", "??", "???":
				if !swiftTypeOptionalSuffix(tokens[index].text, allowIUO) {
					return false
				}
				index++
			default:
				if swiftTypeOptionalSuffix(tokens[index].text, allowIUO) {
					index++
					continue
				}
				goto typeSuffixDone
			}
		}

	typeSuffixDone:
		seenEffect := false
		if index < end && tokens[index].text == "async" {
			seenEffect = true
			index++
		}
		if index < end &&
			(tokens[index].text == "throws" || tokens[index].text == "rethrows") {
			seenEffect = true
			effect := tokens[index].text
			index++
			if index < end && tokens[index].text == "(" {
				next, ok := swiftSkipBalancedHeaderTokens(tokens[:end], index)
				if effect != "throws" || !ok ||
					!swiftTypeTokensValidMode(
						tokens, index+1, next-1, false, false,
					) {
					return false
				}
				index = next
			}
		}
		if index < end && tokens[index].text == "->" {
			index++
			continue
		}
		if seenEffect {
			return false
		}
		if index < end && tokens[index].text == "&" {
			index++
			continue
		}
		return index == end
	}
	return false
}

func swiftBracketTypeValid(
	tokens []swiftToken,
	open, next int,
	allowIUO, allowAttributes bool,
) bool {
	if next <= open+2 || next > len(tokens) || tokens[next-1].text != "]" {
		return false
	}
	start, end := open+1, next-1
	if of := swiftFindTopLevelToken(tokens[:end], start, "of"); of >= 0 {
		return swiftInlineArrayCountValid(tokens, start, of) &&
			swiftTypeTokensValidMode(
				tokens, of+1, end, allowIUO, allowAttributes,
			)
	}
	colon := swiftFindTopLevelToken(tokens[:end], start, ":")
	if colon < 0 {
		return swiftTypeTokensValidMode(
			tokens, start, end, allowIUO, allowAttributes,
		)
	}
	return swiftTypeTokensValidMode(
		tokens, start, colon, allowIUO, allowAttributes,
	) && swiftTypeTokensValidMode(
		tokens, colon+1, end, allowIUO, allowAttributes,
	)
}

func swiftInlineArrayCountValid(tokens []swiftToken, start, end int) bool {
	return end == start+1 &&
		(tokens[start].kind == swiftTokenIdentifier ||
			tokens[start].kind == swiftTokenNumber)
}

func swiftTupleTypeValid(
	tokens []swiftToken,
	open, next int,
	allowIUO, allowAttributes bool,
) bool {
	if next <= open+1 || next > len(tokens) || tokens[next-1].text != ")" {
		return false
	}
	start, end := open+1, next-1
	if start == end {
		return true
	}
	itemStart := start
	for itemStart < end {
		comma := swiftFindTopLevelToken(tokens[:end], itemStart, ",")
		itemEnd := end
		if comma >= 0 {
			itemEnd = comma
		}
		if !swiftTupleTypeItemValid(
			tokens, itemStart, itemEnd, allowIUO, allowAttributes,
		) {
			return false
		}
		if comma < 0 {
			return true
		}
		itemStart = comma + 1
		if itemStart == end {
			return true
		}
	}
	return false
}

func swiftTupleTypeItemValid(
	tokens []swiftToken,
	start, end int,
	allowIUO, allowAttributes bool,
) bool {
	if start >= end {
		return false
	}
	colon := swiftFindTopLevelToken(tokens[:end], start, ":")
	typeStart := start
	if colon >= 0 {
		if !swiftTupleTypeLabelValid(tokens, start, colon) {
			return false
		}
		typeStart = colon + 1
	}
	for typeStart < end && swiftTupleTypeModifier(tokens[typeStart].text) {
		typeStart++
	}
	if typeStart < end && tokens[end-1].text == "..." {
		end--
	}
	return swiftTypeTokensValidMode(
		tokens, typeStart, end, allowIUO, allowAttributes,
	)
}

func swiftTupleTypeLabelValid(tokens []swiftToken, start, end int) bool {
	if end == start+1 {
		return tokens[start].kind == swiftTokenIdentifier || tokens[start].text == "_"
	}
	return end == start+2 && tokens[start].text == "_" &&
		tokens[start+1].kind == swiftTokenIdentifier
}

func swiftTupleTypeModifier(text string) bool {
	switch text {
	case "inout", "borrowing", "consuming", "sending", "isolated":
		return true
	default:
		return false
	}
}

func swiftTypeOptionalSuffix(text string, allowIUO bool) bool {
	if text == "" {
		return false
	}
	end := len(text)
	if text[end-1] == '!' {
		if !allowIUO {
			return false
		}
		end--
	}
	if end == 0 {
		return true
	}
	for index := range end {
		if text[index] != '?' {
			return false
		}
	}
	return true
}

func swiftTypePrefix(text string, allowOwnership bool) bool {
	switch text {
	case "any", "some", "each", "~":
		return true
	case "inout", "borrowing", "consuming", "sending", "isolated":
		return allowOwnership
	default:
		return false
	}
}

func swiftTypeNameToken(token swiftToken) bool {
	return swiftNameToken(token) || token.kind == swiftTokenIdentifier &&
		(token.text == "Any" || token.text == "Self")
}

func swiftPropertyHeaderValid(
	tokens []swiftToken,
	start int,
	candidates []swiftDefinitionCandidate,
) bool {
	if start >= len(tokens) || len(candidates) == 0 &&
		tokens[start].text != "(" && tokens[start].text != "_" {
		return false
	}
	if !swiftPropertyAnnotationsValid(tokens, start) {
		return false
	}
	parentheses, brackets, angles := 0, 0, 0
	bindingStart := true
	seenBindingName := false
	suffixSeen := false
	typePending := false
	for index := start; index < len(tokens); index++ {
		text := tokens[index].text
		closeCount := swiftGenericAngleCloseCount(text)
		if closeCount > 0 && angles > 0 {
			angles = max(0, angles-closeCount)
			typePending = false
			continue
		}
		topLevel := parentheses == 0 && brackets == 0 && angles == 0
		if topLevel {
			switch text {
			case ",":
				if typePending {
					return false
				}
				bindingStart, seenBindingName, suffixSeen = true, false, false
				continue
			case ":":
				if !seenBindingName || typePending {
					return false
				}
				typePending = true
				suffixSeen = true
				bindingStart = false
				continue
			case "=":
				if typePending {
					return false
				}
				bindingStart = false
				suffixSeen = true
				continue
			}
			if bindingStart {
				seenBindingName = swiftNameToken(tokens[index])
				bindingStart = false
			} else if seenBindingName && !suffixSeen && !typePending &&
				swiftNameToken(tokens[index]) {
				return false
			}
		}
		switch text {
		case "(":
			parentheses++
		case ")":
			parentheses = max(0, parentheses-1)
		case "[":
			brackets++
		case "]":
			brackets = max(0, brackets-1)
		case "<":
			angles++
		}
		if typePending {
			typePending = false
		}
	}
	return !typePending
}

func swiftPropertyAnnotationsValid(tokens []swiftToken, start int) bool {
	parentheses, brackets, angles := 0, 0, 0
	annotationStart := -1
	inInitializer := false
	inWhereClause := false
	for index := start; index < len(tokens); index++ {
		text := tokens[index].text
		closeCount := swiftGenericAngleCloseCount(text)
		if closeCount > 0 && angles > 0 {
			angles = max(0, angles-closeCount)
			continue
		}
		topLevel := parentheses == 0 && brackets == 0 && angles == 0
		if topLevel {
			if text == "where" && annotationStart >= 0 && !inInitializer {
				inWhereClause = true
			}
			switch {
			case inWhereClause:
			case text == ":":
				if !inInitializer {
					if annotationStart >= 0 {
						return false
					}
					annotationStart = index + 1
				}
				continue
			case text == "=":
				if annotationStart >= 0 &&
					!swiftPropertyTypeAnnotationValid(tokens, annotationStart, index) {
					return false
				}
				annotationStart = -1
				inInitializer = true
				continue
			case text == ",":
				if annotationStart >= 0 &&
					!swiftPropertyTypeAnnotationValid(tokens, annotationStart, index) {
					return false
				}
				annotationStart = -1
				inInitializer = false
				continue
			}
		}
		switch text {
		case "(":
			parentheses++
		case ")":
			parentheses = max(0, parentheses-1)
		case "[":
			brackets++
		case "]":
			brackets = max(0, brackets-1)
		case "<":
			if !inInitializer {
				angles++
			}
		}
	}
	return annotationStart < 0 ||
		swiftPropertyTypeAnnotationValid(tokens, annotationStart, len(tokens))
}

func swiftPropertyTypeAnnotationValid(tokens []swiftToken, start, end int) bool {
	where := swiftFindTopLevelToken(tokens[:end], start, "where")
	if where < 0 {
		return swiftTypeTokensValidContext(tokens, start, end, true)
	}
	return swiftTypeTokensValidContext(tokens, start, where, true) &&
		swiftWhereRequirementsValid(tokens[:end], where+1)
}

func swiftImportHeaderValid(tokens []swiftToken, start int) bool {
	index := start
	if index < len(tokens) && swiftImportKind(tokens[index].text) {
		index++
	}
	if index >= len(tokens) || !swiftImportPathToken(tokens[index]) {
		return false
	}
	index++
	for index < len(tokens) {
		if tokens[index].text != "." || index+1 >= len(tokens) ||
			!swiftImportPathToken(tokens[index+1]) {
			return false
		}
		index += 2
	}
	return true
}

func swiftImportKind(text string) bool {
	switch text {
	case "typealias", "struct", "class", "enum", "protocol", "let", "var",
		"func":
		return true
	default:
		return false
	}
}

func swiftImportPathToken(token swiftToken) bool {
	return token.kind == swiftTokenIdentifier && token.text != "" && !token.gap
}

func swiftOperatorHeaderValid(tokens []swiftToken, keywordIndex int) bool {
	if keywordIndex != 1 || keywordIndex+1 >= len(tokens) {
		return false
	}
	switch tokens[0].text {
	case "prefix", "infix", "postfix":
	default:
		return false
	}
	index := keywordIndex + 2
	if index == len(tokens) {
		return true
	}
	return index+2 == len(tokens) && tokens[index].text == ":" &&
		swiftNameToken(tokens[index+1])
}

func swiftFirstTopLevelToken(tokens []swiftToken, start int, wants ...string) int {
	end := len(tokens)
	for _, want := range wants {
		if index := swiftFindTopLevelToken(tokens, start, want); index >= 0 {
			end = min(end, index)
		}
	}
	return end
}

func swiftFindTopLevelToken(tokens []swiftToken, start int, want string) int {
	parentheses, brackets, angles := 0, 0, 0
	for index := start; index < len(tokens); index++ {
		text := tokens[index].text
		closeCount := swiftGenericAngleCloseCount(text)
		if closeCount > 0 && angles > 0 {
			angles = max(0, angles-closeCount)
			continue
		}
		if parentheses == 0 && brackets == 0 && angles == 0 && text == want &&
			!tokens[index].quotedIdentifier {
			return index
		}
		switch text {
		case "(":
			parentheses++
		case ")":
			parentheses = max(0, parentheses-1)
		case "[":
			brackets++
		case "]":
			brackets = max(0, brackets-1)
		case "<":
			angles++
		}
	}
	return -1
}

func swiftNextOperatorName(tokens []swiftToken, start int) swiftToken {
	if start >= 0 && start < len(tokens) && swiftOperatorSymbol(tokens[start].text) {
		return tokens[start]
	}
	return swiftToken{}
}

func swiftNominalHeaderSuffixValid(tokens []swiftToken, start int) bool {
	index := start
	if index < len(tokens) && tokens[index].text == "<" {
		var ok bool
		index, ok = swiftSkipNominalGenericClause(tokens, index)
		if !ok {
			return false
		}
	}
	if index < len(tokens) && tokens[index].text == ":" {
		var ok bool
		index, ok = swiftInheritanceClauseEnd(tokens, index+1)
		if !ok {
			return false
		}
	}
	if index < len(tokens) && tokens[index].text == "where" &&
		!tokens[index].quotedIdentifier {
		return swiftWhereRequirementsValid(tokens, index+1)
	}
	return index == len(tokens)
}

func swiftSkipNominalGenericClause(tokens []swiftToken, start int) (int, bool) {
	return swiftSkipGenericClause(tokens, start, true)
}

func swiftSkipTypeGenericClause(tokens []swiftToken, start int) (int, bool) {
	return swiftSkipGenericClause(tokens, start, false)
}

func swiftSkipGenericClause(
	tokens []swiftToken,
	start int,
	allowInlineWhere bool,
) (int, bool) {
	if start < 0 || start >= len(tokens) ||
		(tokens[start].text != "<" && tokens[start].text != "?<" &&
			tokens[start].text != "!<") {
		return start, false
	}
	angleDepth := 1
	parentheses, brackets := 0, 0
	itemStart := start + 1
	haveItem := false
	for index := start + 1; index < len(tokens); index++ {
		text := tokens[index].text
		closeCount := swiftGenericAngleCloseCount(text)
		if closeCount > 0 {
			if parentheses != 0 || brackets != 0 || closeCount > angleDepth {
				return start, false
			}
			angleDepth -= closeCount
			if angleDepth == 0 {
				if itemStart == index {
					if !haveItem {
						return start, false
					}
				} else if !swiftClosingGenericClauseItemValid(
					tokens, itemStart, index, closeCount, allowInlineWhere,
				) {
					return start, false
				}
				return index + 1, true
			}
			continue
		}
		topLevel := angleDepth == 1 && parentheses == 0 && brackets == 0
		switch text {
		case "(":
			parentheses++
		case ")":
			if parentheses == 0 {
				return start, false
			}
			parentheses--
		case "[":
			brackets++
		case "]":
			if brackets == 0 {
				return start, false
			}
			brackets--
		case "<":
			angleDepth++
		case ",":
			if topLevel {
				if itemStart == index ||
					!swiftGenericClauseItemValid(
						tokens, itemStart, index, allowInlineWhere,
					) {
					return start, false
				}
				haveItem = true
				itemStart = index + 1
				continue
			}
		}
	}
	return start, false
}

func swiftClosingGenericClauseItemValid(
	tokens []swiftToken,
	start, end, closeCount int,
	declarationParameters bool,
) bool {
	closePrefix := strings.TrimSuffix(
		tokens[end].text, strings.Repeat(">", closeCount),
	)
	if closeCount == 1 && closePrefix == "" {
		return swiftGenericClauseItemValid(
			tokens, start, end, declarationParameters,
		)
	}
	item := make([]swiftToken, 0, end-start+1)
	item = append(item, tokens[start:end]...)
	item = append(item, swiftToken{
		text: closePrefix + strings.Repeat(">", closeCount-1), nameStart: -1,
		kind: swiftTokenPunctuation,
	})
	return swiftGenericClauseItemValid(item, 0, len(item), declarationParameters)
}

func swiftGenericClauseItemValid(
	tokens []swiftToken,
	start, end int,
	declarationParameters bool,
) bool {
	if start >= end {
		return false
	}
	index := start
	for index < end && tokens[index].text == "@" {
		next, ok := swiftSkipDeclarationAttribute(tokens[:end], index)
		if !ok {
			return false
		}
		index = next
	}
	if index >= end {
		return false
	}
	relation := swiftFirstTopLevelToken(tokens[:end], index, ":", "=")
	where := swiftFindTopLevelToken(tokens[:end], index, "where")
	if !declarationParameters {
		return relation >= end && where < 0 &&
			swiftTypeTokensValidContext(tokens, index, end, true)
	}
	if where >= 0 {
		return swiftGenericParameterHeadValid(tokens, index, where) &&
			swiftWhereRequirementsValid(tokens[:end], where+1)
	}
	if relation < end {
		if tokens[relation].text != ":" {
			return false
		}
		return swiftGenericParameterHeadValid(tokens, index, relation) &&
			swiftTypeTokensValid(tokens, relation+1, end)
	}
	return swiftGenericParameterHeadValid(tokens, index, end)
}

func swiftGenericParameterHeadValid(tokens []swiftToken, start, end int) bool {
	if end == start+1 {
		return swiftNameToken(tokens[start])
	}
	if end == start+2 && tokens[start].text == "let" &&
		swiftNameToken(tokens[start+1]) {
		return true
	}
	return tokens[start].text == "each" &&
		swiftTypeTokensValid(tokens, start+1, end)
}

func swiftInheritanceClauseEnd(tokens []swiftToken, start int) (int, bool) {
	end := len(tokens)
	if where := swiftFindTopLevelToken(tokens, start, "where"); where >= 0 {
		end = where
	}
	segmentStart := start
	for segmentStart < end {
		comma := swiftFindTopLevelToken(tokens[:end], segmentStart, ",")
		segmentEnd := end
		if comma >= 0 {
			segmentEnd = comma
		}
		if !swiftInheritanceSpecifierValid(tokens, segmentStart, segmentEnd) {
			return start, false
		}
		if comma < 0 {
			break
		}
		segmentStart = comma + 1
	}
	if segmentStart >= end {
		return start, false
	}
	return end, true
}

func swiftInheritanceSpecifierValid(tokens []swiftToken, start, end int) bool {
	if start >= end {
		return false
	}
	if tokens[start].text == "[" {
		next, ok := swiftSkipBalancedSquareTokens(tokens[:end], start)
		if !ok || next >= end ||
			!swiftInheritanceAttributeListValid(tokens, start+1, next-1) ||
			swiftFindTopLevelToken(tokens[:end], next, "->") < 0 {
			return false
		}
		return swiftTypeTokensValid(tokens, next, end)
	}
	head := start
	for head < end && tokens[head].text == "@" {
		next, ok := swiftSkipInheritanceAttribute(tokens[:end], head)
		if !ok {
			return false
		}
		head = next
	}
	if head >= end {
		return false
	}
	if !tokens[head].quotedIdentifier {
		switch tokens[head].text {
		case "any", "some", "each", "repeat":
			return false
		case "class":
			return head+1 == end
		}
	}
	return swiftTypeTokensValid(tokens, start, end)
}

func swiftInheritanceAttributeListValid(tokens []swiftToken, start, end int) bool {
	if start >= end {
		return false
	}
	index := start
	for index < end {
		if tokens[index].text != "@" {
			return false
		}
		next, ok := swiftSkipDeclarationAttribute(tokens[:end], index)
		if !ok {
			return false
		}
		index = next
	}
	return true
}

func swiftSkipInheritanceAttribute(tokens []swiftToken, start int) (int, bool) {
	index := start + 1
	if index >= len(tokens) || tokens[index].kind != swiftTokenIdentifier {
		return start, false
	}
	index++
	for index+1 < len(tokens) && tokens[index].text == "." &&
		tokens[index+1].kind == swiftTokenIdentifier {
		index += 2
	}
	if index >= len(tokens) || tokens[index].text != "(" {
		return index, true
	}
	next, ok := swiftSkipBalancedHeaderTokens(tokens, index)
	if !ok {
		return start, false
	}
	if next < len(tokens) && tokens[next].text == "(" {
		return next, true
	}
	if next < len(tokens) {
		switch tokens[next].text {
		case "async", "throws", "rethrows", "->":
			return index, true
		}
	}
	return next, true
}

func swiftWhereRequirementsValid(tokens []swiftToken, start int) bool {
	if start < 0 || start >= len(tokens) {
		return false
	}
	itemStart := start
	for itemStart < len(tokens) {
		comma := swiftFindTopLevelToken(tokens, itemStart, ",")
		itemEnd := len(tokens)
		if comma >= 0 {
			itemEnd = comma
		}
		if !swiftWhereRequirementValid(tokens, itemStart, itemEnd) {
			return false
		}
		if comma < 0 {
			return true
		}
		itemStart = comma + 1
		if itemStart == len(tokens) {
			return true
		}
	}
	return false
}

func swiftWhereRequirementValid(tokens []swiftToken, start, end int) bool {
	if start >= end {
		return false
	}
	relation := swiftFirstTopLevelToken(tokens[:end], start, ":", "==", "=")
	if relation >= end ||
		swiftFirstTopLevelToken(tokens[:end], relation+1, ":", "==", "=") < end {
		return false
	}
	if !swiftTypeTokensValid(tokens, start, relation) {
		return false
	}
	return swiftTypeTokensValidContext(
		tokens, relation+1, end, tokens[relation].text == ":",
	)
}

func swiftGenericAngleCloseCount(text string) int {
	end := len(text)
	start := end
	for start > 0 && text[start-1] == '>' {
		start--
	}
	if start == end {
		return 0
	}
	prefix := text[:start]
	if prefix != "" && prefix != "!" {
		for index := range len(prefix) {
			if prefix[index] != '?' {
				return 0
			}
		}
	}
	return end - start
}

func swiftExtensionTarget(tokens []swiftToken, start int) (string, int, bool) {
	if start < 0 || start >= len(tokens) {
		return "", -1, false
	}
	targetEnd := swiftFirstTopLevelToken(tokens, start, ":", "where")
	if !swiftExtensionTypeValid(tokens, start, targetEnd) {
		return "", -1, false
	}
	index := targetEnd
	if index < len(tokens) && tokens[index].text == ":" {
		if !swiftExtensionConformanceSuffixValid(tokens, index+1) {
			return "", -1, false
		}
		index = len(tokens)
	} else if index < len(tokens) && tokens[index].text == "where" {
		if !swiftWhereRequirementsValid(tokens, index+1) {
			return "", -1, false
		}
		index = len(tokens)
	}
	if index != len(tokens) {
		return "", -1, false
	}
	symbol, nameStart := swiftSimpleExtensionOwner(tokens, start, targetEnd)
	return symbol, nameStart, true
}

func swiftExtensionTypeValid(tokens []swiftToken, start, end int) bool {
	return swiftTypeTokensValid(tokens, start, end)
}

func swiftSkipBalancedSquareTokens(tokens []swiftToken, start int) (int, bool) {
	if start < 0 || start >= len(tokens) || tokens[start].text != "[" {
		return start, false
	}
	depth := 0
	for index := start; index < len(tokens); index++ {
		switch tokens[index].text {
		case "[":
			depth++
		case "]":
			depth--
			if depth == 0 {
				return index + 1, true
			}
		}
	}
	return start, false
}

func swiftSimpleExtensionOwner(tokens []swiftToken, start, end int) (string, int) {
	if start < 0 || start >= end || !swiftNameToken(tokens[start]) {
		return "", -1
	}
	var builder strings.Builder
	nameStart := tokens[start].nameStart
	builder.WriteString(tokens[start].text)
	index := start + 1
	for index < end {
		if (tokens[index].text != "." && tokens[index].text != "::") ||
			index+1 >= end || !swiftNameToken(tokens[index+1]) {
			return "", -1
		}
		builder.WriteString(tokens[index].text)
		builder.WriteString(tokens[index+1].text)
		index += 2
	}
	return builder.String(), nameStart
}

func swiftExtensionConformanceSuffixValid(tokens []swiftToken, start int) bool {
	index, ok := swiftInheritanceClauseEnd(tokens, start)
	if !ok {
		return false
	}
	if index < len(tokens) && tokens[index].text == "where" {
		return swiftWhereRequirementsValid(tokens, index+1)
	}
	return index == len(tokens)
}

func swiftPropertyCandidates(
	tokens []swiftToken,
	start int,
	hasBody bool,
) []swiftDefinitionCandidate {
	result := make([]swiftDefinitionCandidate, 0, 2)
	parentheses, brackets := 0, 0
	wantName := true
	inWhereClause := false
	for index := start; index < len(tokens); index++ {
		token := tokens[index]
		if token.kind == swiftTokenInterpolationStart {
			parentheses++
			continue
		}
		if token.kind == swiftTokenInterpolationEnd {
			parentheses = max(0, parentheses-1)
			continue
		}
		if parentheses == 0 && brackets == 0 && token.text == "where" &&
			!token.quotedIdentifier {
			inWhereClause = true
		}
		if wantName && parentheses == 0 && brackets == 0 &&
			token.text != "," {
			if swiftNameToken(token) && token.text != "_" {
				result = append(result, swiftDefinitionCandidate{
					symbol: token.text, nameStart: token.nameStart, ownsScope: hasBody,
				})
				wantName = false
				continue
			}
			// A simple property binding must put its declared name first.
			// Once a tuple, case, wildcard, or other pattern starts, never
			// reinterpret a later initializer/call identifier as that name.
			wantName = false
		}
		switch token.text {
		case "(":
			parentheses++
		case ")":
			parentheses = max(0, parentheses-1)
		case "[":
			brackets++
		case "]":
			brackets = max(0, brackets-1)
		case ",":
			if !inWhereClause && parentheses == 0 && brackets == 0 &&
				swiftPropertyBindingStarts(tokens, index+1) {
				wantName = true
			}
		}
	}
	return result
}

func swiftPropertyBindingStarts(tokens []swiftToken, start int) bool {
	if start < 0 || start >= len(tokens) {
		return false
	}
	first := tokens[start]
	if first.text == "(" || first.text == "_" {
		return true
	}
	if !swiftNameToken(first) {
		return false
	}
	if start+1 >= len(tokens) {
		return true
	}
	switch tokens[start+1].text {
	case ":", "=", ",", "{":
		return true
	default:
		return false
	}
}

func swiftEnumCaseCandidates(tokens []swiftToken, start int) []swiftDefinitionCandidate {
	result := make([]swiftDefinitionCandidate, 0, 2)
	parentheses, brackets := 0, 0
	wantName := true
	skippingRawValue := false
	for index := start; index < len(tokens); index++ {
		token := tokens[index]
		switch token.text {
		case "(":
			parentheses++
		case ")":
			parentheses = max(0, parentheses-1)
		case "[":
			brackets++
		case "]":
			brackets = max(0, brackets-1)
		case ",":
			if parentheses == 0 && brackets == 0 {
				wantName = true
				skippingRawValue = false
			}
		case "=":
			if parentheses == 0 && brackets == 0 {
				skippingRawValue = true
				wantName = false
			}
		default:
			if !skippingRawValue && wantName && parentheses == 0 && brackets == 0 &&
				swiftNameToken(token) {
				result = append(result, swiftDefinitionCandidate{
					symbol: token.text, nameStart: token.nameStart,
				})
				wantName = false
			}
		}
	}
	return result
}

type swiftHeaderBalance struct {
	parentheses, brackets, angles int
	invalid                       bool
	declarationSeen               bool
	propertyDeclaration           bool
	propertyInitializer           bool
	expectFunctionName            bool
}

func (balance *swiftHeaderBalance) accept(token swiftToken) {
	if token.gap {
		balance.invalid = true
		return
	}
	if token.kind == swiftTokenInterpolationStart {
		balance.parentheses++
		return
	}
	if token.kind == swiftTokenInterpolationEnd {
		if balance.parentheses == 0 {
			balance.invalid = true
		} else {
			balance.parentheses--
		}
		return
	}

	functionName := balance.expectFunctionName
	if balance.expectFunctionName && token.text != "func" {
		balance.expectFunctionName = false
	}
	switch token.text {
	case "(":
		balance.parentheses++
	case ")":
		if balance.parentheses == 0 {
			balance.invalid = true
		} else {
			balance.parentheses--
		}
	case "[":
		balance.brackets++
	case "]":
		if balance.brackets == 0 {
			balance.invalid = true
		} else {
			balance.brackets--
		}
	case "=":
		if balance.propertyDeclaration && balance.parentheses == 0 &&
			balance.brackets == 0 && balance.angles == 0 {
			balance.propertyInitializer = true
		}
	case ",":
		if balance.propertyDeclaration && balance.parentheses == 0 &&
			balance.brackets == 0 && balance.angles == 0 {
			balance.propertyInitializer = false
		}
	case "<":
		if balance.declarationSeen && balance.parentheses == 0 &&
			balance.brackets == 0 && !balance.propertyInitializer && !functionName {
			balance.angles++
		}
	default:
		closeCount := swiftGenericAngleCloseCount(token.text)
		if closeCount > 0 && balance.angles > 0 && balance.parentheses == 0 &&
			balance.brackets == 0 {
			balance.angles = max(0, balance.angles-closeCount)
		}
	}
	if token.quotedIdentifier {
		return
	}
	switch token.text {
	case "let", "var":
		balance.declarationSeen = true
		balance.propertyDeclaration = true
	case "func":
		balance.declarationSeen = true
		balance.expectFunctionName = true
	case "class", "struct", "actor", "enum", "protocol", "extension",
		"typealias", "associatedtype", "macro", "init", "subscript":
		balance.declarationSeen = true
	}
}

func swiftDeclarationLineStarter(token swiftToken) bool {
	if token.quotedIdentifier {
		return false
	}
	return token.text == "@" || token.text == "class" ||
		swiftDeclarationModifier(token.text) ||
		swiftHeaderKindForKeyword(token.text) != swiftHeaderNone
}

func swiftHeaderContinues(header []swiftToken, next swiftToken) bool {
	if len(header) == 0 {
		return false
	}
	last := header[len(header)-1].text
	switch last {
	case "=", ",", ".", "::", "->", ":", "where", "&&", "||", "??":
		return true
	}
	switch next.text {
	case ".", "::", "where", "async", "throws", "rethrows", "->", "{":
		return true
	default:
		return false
	}
}

func swiftHeaderContainsTopLevel(tokens []swiftToken, want string) bool {
	parentheses, brackets := 0, 0
	for _, token := range tokens {
		switch token.text {
		case "(":
			parentheses++
		case ")":
			parentheses = max(0, parentheses-1)
		case "[":
			brackets++
		case "]":
			brackets = max(0, brackets-1)
		default:
			if parentheses == 0 && brackets == 0 && token.text == want {
				return true
			}
		}
	}
	return false
}

func (parser *swiftRecoveryParser) attachDocumentation() {
	if len(parser.docComments) == 0 || len(parser.definitions) == 0 {
		return
	}
	for index := range parser.definitions {
		definition := &parser.definitions[index]
		if !definition.ownsScope || definition.scopeStart < 1 ||
			definition.scopeStart > len(parser.lineStarts) {
			continue
		}
		anchor := parser.lineStarts[definition.scopeStart-1]
		commentIndex := sort.Search(len(parser.docComments), func(position int) bool {
			return parser.docComments[position].end > anchor
		}) - 1
		if commentIndex < 0 {
			continue
		}
		comment := parser.docComments[commentIndex]
		if !swiftSourceAttachmentGap(parser.source, comment.end, anchor) {
			continue
		}
		start := comment.start
		for commentIndex > 0 {
			previous := parser.docComments[commentIndex-1]
			if !swiftSourceAttachmentGap(parser.source, previous.end, start) {
				break
			}
			start = previous.start
			commentIndex--
		}
		definition.scopeStart = min(
			definition.scopeStart, swiftTokenLine(parser.lineStarts, start),
		)
	}
}

func swiftDocumentationComment(source string, span cByteSpan) bool {
	return swiftKDocSpan(source, span.start, span.end)
}

func swiftSortUniqueDefinitions(
	definitions []sourceDefinition,
	lineCount int,
) []sourceDefinition {
	normalized := definitions[:0]
	for _, definition := range definitions {
		definition = normalizeCDefinition(definition, lineCount)
		if definition.symbol != "" && swiftDefinitionSymbolValid(definition.symbol) {
			normalized = append(normalized, definition)
		}
	}
	sort.SliceStable(normalized, func(first, second int) bool {
		if normalized[first].line != normalized[second].line {
			return normalized[first].line < normalized[second].line
		}
		if normalized[first].column != normalized[second].column {
			return normalized[first].column < normalized[second].column
		}
		return normalized[first].symbol < normalized[second].symbol
	})
	unique := normalized[:0]
	for _, definition := range normalized {
		if len(unique) > 0 {
			last := unique[len(unique)-1]
			if last.symbol == definition.symbol && last.line == definition.line &&
				last.column == definition.column {
				continue
			}
		}
		unique = append(unique, definition)
	}
	return unique
}
