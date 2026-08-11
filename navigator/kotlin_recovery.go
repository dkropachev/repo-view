package navigator

import (
	"sort"
	"strings"
)

const kotlinCompletedExpressionBodyToken = "\x00kotlin-expression-body\x00"

type kotlinLexicalAnalysis struct {
	definitions []sourceDefinition
	scopes      []cLineScope
	imports     []cLineSpan
}

type kotlinRecoveryFrameKind uint8

const (
	kotlinRecoverySource kotlinRecoveryFrameKind = iota
	kotlinRecoveryType
	kotlinRecoveryEnum
	kotlinRecoveryFunction
	kotlinRecoveryProperty
	kotlinRecoveryControl
	kotlinRecoveryInterpolation
)

type kotlinRecoveryFrame struct {
	definitionIndexes []int
	start             int
	kind              kotlinRecoveryFrameKind
	enumEntriesDone   bool
}

type kotlinDefinitionKey struct {
	symbol       string
	line, column int
}

type kotlinSuspendedHeader struct {
	header                                       []kotlinToken
	frameDepth, lastTokenLine                    int
	completionParenDepth, completionBracketDepth int
	markCompleted, completedObject               bool
}

type kotlinRecoveryParser struct {
	source                string
	lineStarts            []int
	header                []kotlinToken
	frames                []kotlinRecoveryFrame
	definitions           []sourceDefinition
	scopes                []cLineScope
	imports               []cLineSpan
	definitionSeen        map[kotlinDefinitionKey]int
	suspendedHeaders      []kotlinSuspendedHeader
	lineCount             int
	lastTokenLine         int
	frameOverflow         int
	interpolationOverflow int
}

func analyzeKotlinLexically(source string, lineCount int) kotlinLexicalAnalysis {
	lineCount = max(lineCount, 1)
	parser := kotlinRecoveryParser{
		source:         source,
		lineStarts:     kotlinLineStarts(source),
		lineCount:      lineCount,
		definitionSeen: make(map[kotlinDefinitionKey]int),
		frames: []kotlinRecoveryFrame{{
			kind: kotlinRecoverySource, start: 1,
		}},
	}
	walkKotlinLexically(source, kotlinLexicalSink{
		literalState: parser.acceptLiteralStart,
		token:        parser.accept,
	})
	parser.flushHeader()
	for len(parser.frames) > 1 {
		parser.closeFrame(lineCount, 0)
	}
	return kotlinLexicalAnalysis{
		definitions: kotlinSortUniqueDefinitions(parser.definitions, lineCount),
		scopes:      cNormalizeTreeLineScopes(parser.scopes, lineCount),
		imports:     cNormalizeTreeLineSpans(parser.imports, lineCount),
	}
}

func (parser *kotlinRecoveryParser) acceptLiteralStart(literal kotlinLiteral) bool {
	line := max(kotlinTokenLine(parser.lineStarts, literal.start), 1)
	if line <= parser.lastTokenLine || len(parser.header) == 0 {
		return true
	}
	boundary := kotlinToken{
		text: kotlinLiteralToken, start: literal.start, end: literal.start + 1,
		nameStart: literal.start, kind: kotlinTokenNumber, lineStart: true,
	}
	if kotlinDeclarationRestartsMalformedHeader(parser.header, boundary) ||
		parser.kotlinHeaderEndsBefore(boundary) {
		parser.flushHeader()
		parser.header = parser.header[:0]
	}
	return true
}

func (parser *kotlinRecoveryParser) accept(token kotlinToken) bool {
	if token.gap {
		parser.flushHeader()
		parser.header = parser.header[:0]
		return true
	}
	line := kotlinTokenLine(parser.lineStarts, token.start)
	if line < 1 {
		line = 1
	}
	if token.interpolation {
		parser.acceptInterpolationBoundary(token, line)
		return true
	}
	if token.lineStart && len(parser.header) > 0 &&
		(kotlinDeclarationRestartsMalformedHeader(parser.header, token) ||
			parser.kotlinHeaderEndsBefore(token)) {
		parser.flushHeader()
		parser.header = parser.header[:0]
	}
	parser.lastTokenLine = line

	if token.kind != kotlinTokenPunctuation {
		parser.appendHeader(token)
		return true
	}
	switch token.text {
	case "{":
		if kind, ok := kotlinFunctionExpressionBodyFrameKind(parser.header); ok {
			if parser.suspendHeaderWithKind(line, kind, true) {
				return true
			}
			parser.flushHeader()
			parser.header = parser.header[:0]
		}
		if kotlinHeaderDelimiterDepth(parser.header) > 0 {
			if parser.suspendHeader(line) {
				return true
			}
			parser.flushHeader()
			parser.header = parser.header[:0]
		}
		kind, definitions := parser.analyzeHeader(parser.header, true, line)
		parser.header = parser.header[:0]
		if len(parser.frames) >= kotlinMaximumStructuralDepth {
			parser.frameOverflow++
			return true
		}
		start := line
		for _, index := range definitions {
			if index >= 0 && index < len(parser.definitions) {
				start = min(start, parser.definitions[index].scopeStart)
			}
		}
		parser.frames = append(parser.frames, kotlinRecoveryFrame{
			kind: kind, start: start, definitionIndexes: definitions,
		})
	case "}":
		parser.flushHeaderAtBoundary(
			line, parser.exactBoundaryColumn(line, token.start),
		)
		parser.header = parser.header[:0]
		if parser.frameOverflow > 0 {
			parser.frameOverflow--
			break
		}
		if frame := parser.currentFrame(); frame != nil &&
			frame.kind == kotlinRecoveryInterpolation {
			break
		}
		parser.closeFrame(
			line, parser.exactBoundaryColumn(line, token.end),
		)
		parser.restoreSuspendedHeader(line)
	case ";":
		parser.flushHeaderAtBoundary(
			line, parser.exactBoundaryColumn(line, token.end),
		)
		parser.header = parser.header[:0]
		if frame := parser.currentFrame(); frame != nil && frame.kind == kotlinRecoveryEnum {
			frame.enumEntriesDone = true
		}
	case ",":
		if frame := parser.currentFrame(); frame != nil && frame.kind == kotlinRecoveryEnum &&
			!frame.enumEntriesDone && kotlinHeaderDelimiterDepth(parser.header) == 0 {
			parser.flushHeader()
			parser.header = parser.header[:0]
			break
		}
		parser.appendHeader(token)
	default:
		parser.appendHeader(token)
	}
	return true
}

func (parser *kotlinRecoveryParser) acceptInterpolationBoundary(
	token kotlinToken,
	line int,
) {
	parser.lastTokenLine = line
	if token.text == "{" {
		if len(parser.suspendedHeaders) == kotlinMaximumSuspendedHeaders ||
			len(parser.frames) >= kotlinMaximumStructuralDepth {
			parser.interpolationOverflow++
			parser.flushHeader()
			parser.header = parser.header[:0]
			return
		}
		parser.suspendedHeaders = append(parser.suspendedHeaders, kotlinSuspendedHeader{
			header:        parser.header,
			frameDepth:    len(parser.frames),
			lastTokenLine: parser.lastTokenLine,
		})
		parser.header = nil
		parser.frames = append(parser.frames, kotlinRecoveryFrame{
			kind: kotlinRecoveryInterpolation, start: max(line, 1),
		})
		return
	}
	parser.flushHeader()
	parser.header = parser.header[:0]
	if parser.interpolationOverflow > 0 {
		parser.interpolationOverflow--
		return
	}
	for len(parser.frames) > 1 {
		frame := parser.currentFrame()
		if frame != nil && frame.kind == kotlinRecoveryInterpolation {
			break
		}
		parser.closeFrame(line, 0)
		parser.restoreSuspendedHeader(line)
		parser.flushHeader()
		parser.header = parser.header[:0]
	}
	if frame := parser.currentFrame(); frame != nil &&
		frame.kind == kotlinRecoveryInterpolation {
		parser.frames = parser.frames[:len(parser.frames)-1]
	}
	parser.restoreSuspendedHeader(line)
}

func (parser *kotlinRecoveryParser) suspendHeader(line int) bool {
	return parser.suspendHeaderWithKind(line, kotlinRecoveryControl, false)
}

func (parser *kotlinRecoveryParser) suspendHeaderWithKind(
	line int,
	kind kotlinRecoveryFrameKind,
	markCompleted bool,
) bool {
	if len(parser.suspendedHeaders) >= kotlinMaximumSuspendedHeaders ||
		len(parser.frames) >= kotlinMaximumStructuralDepth {
		return false
	}
	completionParenDepth, completionBracketDepth := 0, 0
	if markCompleted {
		completionParenDepth, completionBracketDepth =
			kotlinHeaderDelimiterDepths(parser.header)
	}
	parser.suspendedHeaders = append(parser.suspendedHeaders, kotlinSuspendedHeader{
		header:                 parser.header,
		frameDepth:             len(parser.frames),
		lastTokenLine:          parser.lastTokenLine,
		completionParenDepth:   completionParenDepth,
		completionBracketDepth: completionBracketDepth,
		markCompleted:          markCompleted,
		completedObject:        markCompleted && kind == kotlinRecoveryType,
	})
	parser.header = nil
	parser.frames = append(parser.frames, kotlinRecoveryFrame{
		kind: kind, start: max(line, 1),
	})
	return true
}

func kotlinFunctionExpressionBodyFrameKind(
	header []kotlinToken,
) (kotlinRecoveryFrameKind, bool) {
	if kotlinTopLevelToken(header, "fun") < 0 {
		return kotlinRecoveryControl, false
	}
	parenDepth, bracketDepth := 0, 0
	assignment := -1
	for index, token := range header {
		if token.kind != kotlinTokenPunctuation {
			continue
		}
		switch token.text {
		case "(":
			parenDepth++
		case ")":
			parenDepth = max(0, parenDepth-1)
		case "[":
			bracketDepth++
		case "]":
			bracketDepth = max(0, bracketDepth-1)
		case "=":
			if parenDepth == 0 && bracketDepth == 0 {
				assignment = index
			}
		}
	}
	if assignment < 0 {
		return kotlinRecoveryControl, false
	}
	if kotlinObjectIntroducesCurrentBrace(header[assignment+1:]) {
		return kotlinRecoveryType, true
	}
	return kotlinRecoveryControl, true
}

func kotlinObjectIntroducesCurrentBrace(header []kotlinToken) bool {
	currentParenDepth, currentBracketDepth := kotlinHeaderDelimiterDepths(header)
	parenDepth, bracketDepth := 0, 0
	active := 0
	for _, token := range header {
		if kotlinKeywordValue(token, "object") &&
			parenDepth == currentParenDepth && bracketDepth == currentBracketDepth {
			active++
		}
		if token.text == kotlinCompletedExpressionBodyToken && token.quotedIdentifier &&
			token.start == currentParenDepth && token.end == currentBracketDepth {
			active = max(0, active-1)
		}
		if token.kind != kotlinTokenPunctuation {
			continue
		}
		switch token.text {
		case "(":
			parenDepth++
		case ")":
			parenDepth = max(0, parenDepth-1)
		case "[":
			bracketDepth++
		case "]":
			bracketDepth = max(0, bracketDepth-1)
		}
	}
	return active > 0
}

func kotlinHeaderDelimiterDepths(header []kotlinToken) (int, int) {
	parenDepth, bracketDepth := 0, 0
	for _, token := range header {
		if token.kind != kotlinTokenPunctuation {
			continue
		}
		switch token.text {
		case "(":
			parenDepth++
		case ")":
			parenDepth = max(0, parenDepth-1)
		case "[":
			bracketDepth++
		case "]":
			bracketDepth = max(0, bracketDepth-1)
		}
	}
	return parenDepth, bracketDepth
}

func (parser *kotlinRecoveryParser) restoreSuspendedHeader(line int) {
	if len(parser.suspendedHeaders) == 0 {
		return
	}
	index := len(parser.suspendedHeaders) - 1
	suspended := parser.suspendedHeaders[index]
	if suspended.frameDepth != len(parser.frames) {
		return
	}
	parser.suspendedHeaders = parser.suspendedHeaders[:index]
	parser.header = suspended.header
	if suspended.markCompleted {
		parser.appendHeader(kotlinToken{
			text:  kotlinCompletedExpressionBodyToken,
			start: suspended.completionParenDepth,
			end:   suspended.completionBracketDepth,
			kind:  kotlinTokenNumber, quotedIdentifier: suspended.completedObject,
		})
	}
	parser.lastTokenLine = max(suspended.lastTokenLine, line)
}

func (parser *kotlinRecoveryParser) appendHeader(token kotlinToken) {
	if len(parser.header) >= kotlinMaximumHeaderTokens {
		parser.flushHeader()
		parser.header = parser.header[:0]
	}
	parser.header = append(parser.header, token)
}

func (parser *kotlinRecoveryParser) kotlinHeaderEndsBefore(next kotlinToken) bool {
	if kotlinHeaderIsDeclarationPrefix(parser.header, next) {
		return false
	}
	if kotlinHeaderDelimiterDepth(parser.header) > 0 {
		return false
	}
	if kotlinHeaderContinuation(parser.header[len(parser.header)-1]) {
		return false
	}
	if next.kind == kotlinTokenPunctuation {
		switch next.text {
		case "{", "=", ":", ".", "?.", "::", "->":
			return false
		}
	} else if !next.quotedIdentifier {
		switch next.text {
		case "where", "by", "field", "get", "set", "else", "catch", "finally":
			return false
		}
	}
	return true
}

func kotlinHeaderIsDeclarationPrefix(header []kotlinToken, next kotlinToken) bool {
	if next.kind != kotlinTokenIdentifier || next.quotedIdentifier ||
		(next.text != "class" && next.text != "interface" && next.text != "object" &&
			next.text != "fun" && next.text != "typealias" && next.text != "val" &&
			next.text != "var" && next.text != "constructor") {
		return false
	}
	for _, token := range header {
		if kotlinDeclarationKeywordToken(token) {
			return false
		}
	}
	if len(header) > 0 && (kotlinPunctuationToken(header[0], "@") ||
		kotlinKeywordValue(header[0], "context")) {
		return true
	}
	for _, token := range header {
		if token.kind != kotlinTokenIdentifier || token.quotedIdentifier {
			return false
		}
		switch token.text {
		case "public", "private", "protected", "internal", "data", "enum", "annotation",
			"sealed", "value", "expect", "actual", "abstract", "open", "final", "inline",
			"suspend", "tailrec", "operator", "infix", "external", "const", "lateinit",
			"override", "inner":
		default:
			return false
		}
	}
	return len(header) > 0
}

func kotlinDeclarationRestartsMalformedHeader(
	header []kotlinToken,
	next kotlinToken,
) bool {
	if !kotlinStrongDeclarationStart(next) || !kotlinHeaderHasDeclaration(header) {
		return false
	}
	parenDepth, bracketDepth, angleDepth := 0, 0, 0
	for _, token := range header {
		if token.kind != kotlinTokenPunctuation {
			continue
		}
		switch token.text {
		case "(":
			parenDepth++
		case ")":
			parenDepth = max(0, parenDepth-1)
		case "[":
			bracketDepth++
		case "]":
			bracketDepth = max(0, bracketDepth-1)
		case "<":
			angleDepth++
		case ">":
			angleDepth = max(0, angleDepth-1)
		}
	}
	if (parenDepth > 0 || bracketDepth > 0 || angleDepth > 0) &&
		kotlinPunctuationToken(next, "@") {
		return false
	}
	if parenDepth > 0 && kotlinTypeDeclarationKeyword(header) >= 0 &&
		kotlinClassParameterContinuation(next) {
		return false
	}
	return parenDepth > 0 || bracketDepth > 0 || angleDepth > 0
}

func kotlinClassParameterContinuation(token kotlinToken) bool {
	if kotlinPunctuationToken(token, "@") || kotlinKeywordValue(token, "val") ||
		kotlinKeywordValue(token, "var") {
		return true
	}
	if token.kind != kotlinTokenIdentifier || token.quotedIdentifier {
		return false
	}
	switch token.text {
	case "public", "private", "protected", "internal", "override", "vararg",
		"crossinline", "noinline":
		return true
	default:
		return false
	}
}

func kotlinStrongDeclarationStart(token kotlinToken) bool {
	if kotlinPunctuationToken(token, "@") {
		return true
	}
	if token.kind != kotlinTokenIdentifier || token.quotedIdentifier {
		return false
	}
	switch token.text {
	case "class", "interface", "object", "fun", "typealias", "val", "var",
		"public", "private", "protected", "internal", "data", "enum", "annotation",
		"sealed", "value", "expect", "actual", "context":
		return true
	default:
		return false
	}
}

func kotlinHeaderHasDeclaration(header []kotlinToken) bool {
	for _, token := range header {
		if kotlinDeclarationKeywordToken(token) || kotlinPunctuationToken(token, "@") ||
			kotlinKeywordValue(token, "context") {
			return true
		}
	}
	return false
}

func kotlinHeaderContinuation(token kotlinToken) bool {
	if token.kind == kotlinTokenPunctuation {
		switch token.text {
		case "(", "[", "<", ".", "?.", "::", ",", ":", "=", "->", "+", "-",
			"*", "/", "%", "&&", "||", "?:", "..", "..<", "!in", "!is":
			return true
		}
	}
	if token.kind == kotlinTokenIdentifier && !token.quotedIdentifier {
		switch token.text {
		case "where", "by", "as", "is", "in":
			return true
		}
	}
	return false
}

func kotlinHeaderDelimiterDepth(tokens []kotlinToken) int {
	depth := 0
	for _, token := range tokens {
		if token.kind != kotlinTokenPunctuation {
			continue
		}
		switch token.text {
		case "(", "[":
			depth++
		case ")", "]":
			depth = max(0, depth-1)
		}
	}
	return depth
}

func kotlinPunctuationToken(token kotlinToken, value string) bool {
	return token.kind == kotlinTokenPunctuation && token.text == value
}

func kotlinKeywordValue(token kotlinToken, value string) bool {
	return token.kind == kotlinTokenIdentifier && !token.quotedIdentifier &&
		token.text == value
}

func kotlinDeclarationKeywordToken(token kotlinToken) bool {
	if token.kind != kotlinTokenIdentifier || token.quotedIdentifier {
		return false
	}
	switch token.text {
	case "class", "interface", "object", "fun", "typealias", "val", "var",
		"constructor":
		return true
	default:
		return false
	}
}

func (parser *kotlinRecoveryParser) flushHeader() {
	parser.flushHeaderAtBoundary(0, 0)
}

func (parser *kotlinRecoveryParser) flushHeaderAtBoundary(
	boundaryLine, ownedEndColumn int,
) {
	if len(parser.header) == 0 {
		return
	}
	endLine := parser.lastTokenLine
	if endLine < 1 {
		endLine = kotlinTokenLine(parser.lineStarts, parser.header[len(parser.header)-1].end)
	}
	_, definitions := parser.analyzeHeader(parser.header, false, max(endLine, 1))
	if boundaryLine < 1 || ownedEndColumn < 1 {
		return
	}
	for _, definitionIndex := range definitions {
		if definitionIndex < 0 || definitionIndex >= len(parser.definitions) {
			continue
		}
		definition := &parser.definitions[definitionIndex]
		if definition.ownsScope && definition.scopeEnd == boundaryLine {
			definition.ownedEndColumn = ownedEndColumn
		}
	}
}

func (parser *kotlinRecoveryParser) analyzeHeader(
	header []kotlinToken,
	ownsBody bool,
	endLine int,
) (kotlinRecoveryFrameKind, []int) {
	header = kotlinHeaderAfterGap(header)
	if len(header) == 0 {
		return kotlinRecoveryControl, nil
	}
	if parser.recordImport(header, endLine) {
		return kotlinRecoveryControl, nil
	}

	if keyword := kotlinTypeDeclarationKeyword(header); keyword >= 0 {
		name := kotlinNextDeclarationIdentifier(header, keyword+1)
		if name >= 0 {
			definition := parser.appendDefinition(header[name], true, endLine)
			parser.appendClassParameterProperties(header, keyword, name, endLine)
			kind := kotlinRecoveryType
			if kotlinHeaderContainsBefore(header, "enum", keyword) {
				kind = kotlinRecoveryEnum
			}
			return kind, kotlinValidDefinitionIndexes(definition)
		}
	}

	if keyword := kotlinTopLevelToken(header, "typealias"); keyword >= 0 {
		name := kotlinNextDeclarationIdentifier(header, keyword+1)
		if name >= 0 {
			definition := parser.appendDefinition(header[name], false, endLine)
			return kotlinRecoveryProperty, kotlinValidDefinitionIndexes(definition)
		}
	}

	if keyword := kotlinPropertyKeyword(header); keyword >= 0 && parser.propertyAllowed() {
		if name := kotlinPropertyNameToken(header, keyword); name >= 0 {
			propertyOwns := ownsBody || kotlinPropertyHeaderOwnsScope(header, keyword)
			definition := parser.appendDefinition(header[name], propertyOwns, endLine)
			kind := kotlinRecoveryControl
			if propertyOwns {
				kind = kotlinRecoveryProperty
			}
			if ownsBody && kotlinTopLevelToken(header[keyword+1:], "object") >= 0 {
				kind = kotlinRecoveryType
			}
			return kind, kotlinValidDefinitionIndexes(definition)
		}
	}

	if keyword := kotlinObjectDeclarationKeyword(header); keyword >= 0 &&
		kotlinTopLevelToken(header, "fun") < 0 {
		name := kotlinNextDeclarationIdentifier(header, keyword+1)
		if name >= 0 {
			definition := parser.appendDefinition(header[name], true, endLine)
			return kotlinRecoveryType, kotlinValidDefinitionIndexes(definition)
		}
		return kotlinRecoveryType, nil
	}

	if keyword := kotlinTopLevelToken(header, "fun"); keyword >= 0 &&
		(keyword+1 >= len(header) || !kotlinKeywordValue(header[keyword+1], "interface")) {
		if name := kotlinFunctionNameToken(header, keyword); name >= 0 {
			definition := parser.appendDefinition(header[name], true, endLine)
			return kotlinRecoveryFunction, kotlinValidDefinitionIndexes(definition)
		}
		return kotlinRecoveryFunction, nil
	}

	if keyword := kotlinTopLevelToken(header, "constructor"); keyword >= 0 &&
		parser.secondaryConstructorAllowed(header, keyword) {
		definition := parser.appendDefinition(header[keyword], true, endLine)
		return kotlinRecoveryFunction, kotlinValidDefinitionIndexes(definition)
	}

	if parser.enumEntryAllowed() {
		if name := kotlinEnumEntryName(header); name >= 0 {
			definition := parser.appendDefinition(header[name], ownsBody, endLine)
			return kotlinRecoveryType, kotlinValidDefinitionIndexes(definition)
		}
	}

	return kotlinRecoveryControl, nil
}

func kotlinHeaderAfterGap(header []kotlinToken) []kotlinToken {
	for index := len(header) - 1; index >= 0; index-- {
		if header[index].gap {
			return header[index+1:]
		}
	}
	return header
}

func kotlinTypeDeclarationKeyword(header []kotlinToken) int {
	for index, token := range header {
		if (kotlinKeywordValue(token, "class") || kotlinKeywordValue(token, "interface")) &&
			(index == 0 || !kotlinPunctuationToken(header[index-1], "::") &&
				!kotlinPunctuationToken(header[index-1], ".") &&
				!kotlinPunctuationToken(header[index-1], "?.")) {
			return index
		}
	}
	return -1
}

func kotlinObjectDeclarationKeyword(header []kotlinToken) int {
	for index, token := range header {
		if kotlinKeywordValue(token, "object") &&
			(index == 0 || !kotlinPunctuationToken(header[index-1], ".") &&
				!kotlinPunctuationToken(header[index-1], "?.")) {
			return index
		}
	}
	return -1
}

func kotlinNextDeclarationIdentifier(header []kotlinToken, start int) int {
	for index := max(start, 0); index < len(header); index++ {
		token := header[index]
		if token.kind == kotlinTokenPunctuation &&
			(token.text == "<" || token.text == "(" || token.text == ":" || token.text == "{") {
			return -1
		}
		if token.kind == kotlinTokenIdentifier &&
			(!kotlinHardKeyword(token.text) || token.quotedIdentifier) {
			return index
		}
	}
	return -1
}

func kotlinFunctionNameToken(header []kotlinToken, keyword int) int {
	limit := len(header)
	depth := 0
	for index := keyword + 1; index < len(header); index++ {
		if header[index].kind != kotlinTokenPunctuation {
			continue
		}
		switch header[index].text {
		case "<", "[":
			depth++
		case ">", "]":
			depth = max(0, depth-1)
		case "=", "{":
			if depth == 0 {
				limit = index
				index = len(header)
			}
		}
	}
	parenDepth := 0
	lastOpen := -1
	for index := keyword + 1; index < limit; index++ {
		if header[index].kind != kotlinTokenPunctuation {
			continue
		}
		switch header[index].text {
		case "(":
			if parenDepth == 0 {
				lastOpen = index
			}
			parenDepth++
		case ")":
			parenDepth = max(0, parenDepth-1)
		}
	}
	if lastOpen < 0 {
		return -1
	}
	for index := lastOpen - 1; index > keyword; index-- {
		token := header[index]
		if token.kind == kotlinTokenIdentifier &&
			(!kotlinHardKeyword(token.text) || token.quotedIdentifier) {
			return index
		}
		if token.kind == kotlinTokenPunctuation &&
			(token.text == ")" || token.text == "]") {
			break
		}
	}
	return -1
}

func kotlinPropertyKeyword(header []kotlinToken) int {
	depth := 0
	for index, token := range header {
		if token.kind == kotlinTokenPunctuation {
			switch token.text {
			case "(":
				depth++
			case ")":
				depth = max(0, depth-1)
			}
		}
		if depth == 0 && (kotlinKeywordValue(token, "val") ||
			kotlinKeywordValue(token, "var")) {
			return index
		}
	}
	return -1
}

func kotlinPropertyNameToken(header []kotlinToken, keyword int) int {
	if keyword+1 >= len(header) || kotlinPunctuationToken(header[keyword+1], "(") ||
		kotlinPunctuationToken(header[keyword+1], "[") {
		return -1
	}
	limit := len(header)
	parenDepth, bracketDepth, angleDepth := 0, 0, 0

propertyHeader:
	for index := keyword + 1; index < len(header); index++ {
		token := header[index]
		switch {
		case kotlinPunctuationToken(token, "("):
			parenDepth++
		case kotlinPunctuationToken(token, ")"):
			parenDepth = max(0, parenDepth-1)
		case kotlinPunctuationToken(token, "["):
			bracketDepth++
		case kotlinPunctuationToken(token, "]"):
			bracketDepth = max(0, bracketDepth-1)
		case kotlinPunctuationToken(token, "<"):
			angleDepth++
		case kotlinPunctuationToken(token, ">"):
			angleDepth = max(0, angleDepth-1)
		case (kotlinPunctuationToken(token, ":") ||
			kotlinPunctuationToken(token, "=") || kotlinPunctuationToken(token, "{") ||
			index > keyword+1 && (kotlinKeywordValue(token, "by") ||
				kotlinKeywordValue(token, "get") || kotlinKeywordValue(token, "set"))) &&
			parenDepth == 0 && bracketDepth == 0 && angleDepth == 0:
			limit = index
			break propertyHeader
		}
	}
	for index := limit - 1; index > keyword; index-- {
		if header[index].kind == kotlinTokenIdentifier &&
			(!kotlinHardKeyword(header[index].text) || header[index].quotedIdentifier) {
			return index
		}
	}
	return -1
}

func kotlinPropertyHeaderOwnsScope(header []kotlinToken, keyword int) bool {
	for index := keyword + 1; index < len(header); index++ {
		if kotlinPunctuationToken(header[index], "->") ||
			kotlinKeywordValue(header[index], "field") ||
			kotlinKeywordValue(header[index], "get") ||
			kotlinKeywordValue(header[index], "set") ||
			kotlinKeywordValue(header[index], "by") {
			return true
		}
	}
	return false
}

func (parser *kotlinRecoveryParser) appendClassParameterProperties(
	header []kotlinToken,
	keyword, name, endLine int,
) {
	open := -1
	for index := name + 1; index < len(header); index++ {
		if kotlinPunctuationToken(header[index], "(") {
			open = index
			break
		}
		if kotlinPunctuationToken(header[index], "{") ||
			kotlinPunctuationToken(header[index], ":") {
			break
		}
	}
	if open < 0 {
		return
	}
	depth := 0
	for index := open; index < len(header); index++ {
		if header[index].kind != kotlinTokenPunctuation &&
			!kotlinKeywordValue(header[index], "val") &&
			!kotlinKeywordValue(header[index], "var") {
			continue
		}
		switch header[index].text {
		case "(":
			depth++
		case ")":
			depth--
			if depth == 0 {
				return
			}
		case "val", "var":
			if depth == 1 {
				property := kotlinNextDeclarationIdentifier(header, index+1)
				if property >= 0 {
					parser.appendDefinition(header[property], false, endLine)
				}
			}
		}
	}
	_ = keyword
}

func kotlinEnumEntryName(header []kotlinToken) int {
	for index := 0; index < len(header); {
		token := header[index]
		if kotlinPunctuationToken(token, "@") {
			index = kotlinTokenAfterAnnotation(header, index)
			continue
		}
		if token.kind != kotlinTokenIdentifier ||
			kotlinHardKeyword(token.text) && !token.quotedIdentifier {
			return -1
		}
		if kotlinSoftKeyword(token.text) && !token.quotedIdentifier {
			index++
			continue
		}
		if index > 0 && (kotlinPunctuationToken(header[index-1], ".") ||
			kotlinPunctuationToken(header[index-1], "?.")) {
			return -1
		}
		return index
	}
	return -1
}

func kotlinTokenAfterAnnotation(header []kotlinToken, annotation int) int {
	index := annotation + 1
	if index >= len(header) {
		return index
	}
	if kotlinPunctuationToken(header[index], "[") {
		depth := 0
		for index < len(header) {
			if header[index].kind != kotlinTokenPunctuation {
				index++
				continue
			}
			switch header[index].text {
			case "[":
				depth++
			case "]":
				depth--
				if depth == 0 {
					return index + 1
				}
			}
			index++
		}
		return index
	}
	if header[index].kind != kotlinTokenIdentifier {
		return index
	}
	index++
	if index < len(header) && kotlinPunctuationToken(header[index], ":") {
		index++
		if index < len(header) && header[index].kind == kotlinTokenIdentifier {
			index++
		}
	}
	for index+1 < len(header) && kotlinPunctuationToken(header[index], ".") &&
		header[index+1].kind == kotlinTokenIdentifier {
		index += 2
	}
	if index >= len(header) || !kotlinPunctuationToken(header[index], "(") {
		return index
	}
	depth := 0
	for index < len(header) {
		if header[index].kind != kotlinTokenPunctuation {
			index++
			continue
		}
		switch header[index].text {
		case "(":
			depth++
		case ")":
			depth--
			if depth == 0 {
				return index + 1
			}
		}
		index++
	}
	return index
}

func (parser *kotlinRecoveryParser) appendDefinition(
	token kotlinToken,
	ownsScope bool,
	endLine int,
) int {
	if token.kind != kotlinTokenIdentifier || token.text == "" {
		return -1
	}
	line, column := (cSourcePositions{
		source: parser.source, lineStarts: parser.lineStarts,
	}).lineColumn(token.nameStart)
	if line < 1 || column < 1 || line > parser.lineCount {
		return -1
	}
	definition := normalizeCDefinition(sourceDefinition{
		symbol: token.text, line: line, column: column,
		scopeStart: line, scopeEnd: max(line, min(endLine, parser.lineCount)),
		ownsScope: ownsScope,
	}, parser.lineCount)
	if !ownsScope {
		definition.scopeStart = line
		definition.scopeEnd = line
	}
	if ownsScope {
		definition.scopeStart = parser.attachedDeclarationStart(line)
	}
	key := kotlinDefinitionKey{
		symbol: definition.symbol, line: definition.line, column: definition.column,
	}
	if existing, ok := parser.definitionSeen[key]; ok {
		current := &parser.definitions[existing]
		if definition.ownsScope && !current.ownsScope {
			*current = definition
		} else if definition.ownsScope == current.ownsScope {
			current.scopeStart = min(current.scopeStart, definition.scopeStart)
			if definition.scopeEnd > current.scopeEnd {
				current.scopeEnd = definition.scopeEnd
				current.ownedEndColumn = definition.ownedEndColumn
			} else if definition.scopeEnd == current.scopeEnd &&
				current.ownedEndColumn == 0 {
				current.ownedEndColumn = definition.ownedEndColumn
			}
		}
		return existing
	}
	parser.definitionSeen[key] = len(parser.definitions)
	parser.definitions = append(parser.definitions, definition)
	if ownsScope && definition.scopeEnd >= definition.scopeStart {
		parser.scopes = append(parser.scopes, cLineScope{
			start: definition.scopeStart, end: definition.scopeEnd,
		})
	}
	return len(parser.definitions) - 1
}

func (parser *kotlinRecoveryParser) attachedDeclarationStart(line int) int {
	start := max(1, min(line, parser.lineCount))
	for previous := start - 1; previous >= 1; {
		text := strings.TrimSpace(parser.physicalLine(previous))
		if text == "" {
			break
		}
		if strings.HasPrefix(text, "@") || strings.HasPrefix(text, "context(") ||
			strings.HasPrefix(text, "context (") {
			start = previous
			previous--
			continue
		}
		if strings.HasSuffix(text, ")") {
			annotationStart := parser.multilineAnnotationStart(previous)
			if annotationStart > 0 {
				start = annotationStart
				previous = annotationStart - 1
				continue
			}
		}
		if strings.HasSuffix(text, "*/") {
			docStart := previous
			for docStart >= 1 {
				candidate := strings.TrimSpace(parser.physicalLine(docStart))
				if strings.HasPrefix(candidate, "/**") {
					start = docStart
					previous = docStart - 1
					break
				}
				if candidate == "" {
					docStart = 0
					break
				}
				docStart--
			}
			if docStart > 0 {
				continue
			}
		}
		break
	}
	return start
}

func (parser *kotlinRecoveryParser) multilineAnnotationStart(endLine int) int {
	balance := 0
	for line := endLine; line >= 1 && endLine-line < 64; line-- {
		text := strings.TrimSpace(parser.physicalLine(line))
		if text == "" {
			return 0
		}
		balance += strings.Count(text, ")") - strings.Count(text, "(")
		if strings.HasPrefix(text, "@") && balance <= 0 {
			return line
		}
	}
	return 0
}

func (parser *kotlinRecoveryParser) physicalLine(line int) string {
	if line < 1 || line > len(parser.lineStarts) {
		return ""
	}
	start := parser.lineStarts[line-1]
	end := len(parser.source)
	if line < len(parser.lineStarts) {
		end = parser.lineStarts[line]
	}
	return parser.source[start:end]
}

func kotlinValidDefinitionIndexes(index int) []int {
	if index < 0 {
		return nil
	}
	return []int{index}
}

func (parser *kotlinRecoveryParser) exactBoundaryColumn(line, offset int) int {
	if line < 1 || line > len(parser.lineStarts) || offset < 0 ||
		offset > len(parser.source) {
		return 0
	}
	lineStart := parser.lineStarts[line-1]
	lineEnd := len(parser.source)
	if line < len(parser.lineStarts) {
		lineEnd = parser.lineStarts[line]
	}
	if offset < lineStart || offset > lineEnd {
		return 0
	}
	return offset - lineStart + 1
}

func (parser *kotlinRecoveryParser) closeFrame(
	endLine, ownedEndColumn int,
) {
	if len(parser.frames) <= 1 {
		return
	}
	index := len(parser.frames) - 1
	frame := parser.frames[index]
	parser.frames = parser.frames[:index]
	if frame.kind == kotlinRecoveryInterpolation {
		return
	}
	endLine = max(frame.start, min(max(endLine, 1), parser.lineCount))
	parser.scopes = append(parser.scopes, cLineScope{start: frame.start, end: endLine})
	for _, definitionIndex := range frame.definitionIndexes {
		if definitionIndex < 0 || definitionIndex >= len(parser.definitions) {
			continue
		}
		definition := &parser.definitions[definitionIndex]
		definition.ownsScope = true
		definition.scopeStart = min(definition.scopeStart, frame.start)
		if endLine >= definition.scopeEnd {
			definition.scopeEnd = endLine
			definition.ownedEndColumn = ownedEndColumn
		}
	}
}

func (parser *kotlinRecoveryParser) currentFrame() *kotlinRecoveryFrame {
	if len(parser.frames) == 0 {
		return nil
	}
	return &parser.frames[len(parser.frames)-1]
}

func (parser *kotlinRecoveryParser) propertyAllowed() bool {
	for index := len(parser.frames) - 1; index >= 0; index-- {
		switch parser.frames[index].kind {
		case kotlinRecoveryFunction, kotlinRecoveryProperty, kotlinRecoveryControl,
			kotlinRecoveryInterpolation:
			return false
		case kotlinRecoveryType, kotlinRecoveryEnum, kotlinRecoverySource:
			return true
		}
	}
	return true
}

func (parser *kotlinRecoveryParser) secondaryConstructorAllowed(
	header []kotlinToken,
	keyword int,
) bool {
	frame := parser.currentFrame()
	return frame != nil &&
		(frame.kind == kotlinRecoveryType || frame.kind == kotlinRecoveryEnum) &&
		keyword+1 < len(header) && kotlinPunctuationToken(header[keyword+1], "(")
}

func (parser *kotlinRecoveryParser) enumEntryAllowed() bool {
	frame := parser.currentFrame()
	return frame != nil && frame.kind == kotlinRecoveryEnum && !frame.enumEntriesDone
}

func (parser *kotlinRecoveryParser) recordImport(header []kotlinToken, endLine int) bool {
	start := kotlinFirstMeaningfulHeaderToken(header)
	if start < 0 || !kotlinKeywordValue(header[start], "import") ||
		parser.currentFrame() == nil || parser.currentFrame().kind != kotlinRecoverySource {
		return false
	}
	if start+1 >= len(header) || kotlinPunctuationToken(header[start+1], "(") {
		return false
	}
	line := kotlinTokenLine(parser.lineStarts, header[start].start)
	if line < 1 || endLine < line {
		return false
	}
	parser.imports = append(parser.imports, cLineSpan{
		start: line, end: min(endLine, parser.lineCount),
	})
	return true
}

func kotlinFirstMeaningfulHeaderToken(header []kotlinToken) int {
	for index, token := range header {
		if !kotlinPunctuationToken(token, "@") &&
			!kotlinPunctuationToken(token, "[") &&
			!kotlinPunctuationToken(token, "]") {
			return index
		}
	}
	return -1
}

func kotlinTopLevelToken(header []kotlinToken, value string) int {
	parenDepth, bracketDepth := 0, 0
	for index, token := range header {
		if parenDepth == 0 && bracketDepth == 0 && kotlinKeywordValue(token, value) {
			return index
		}
		if token.kind != kotlinTokenPunctuation {
			continue
		}
		switch token.text {
		case "(":
			parenDepth++
		case ")":
			parenDepth = max(0, parenDepth-1)
		case "[":
			bracketDepth++
		case "]":
			bracketDepth = max(0, bracketDepth-1)
		}
	}
	return -1
}

func kotlinHeaderContainsBefore(header []kotlinToken, value string, limit int) bool {
	limit = min(max(limit, 0), len(header))
	for _, token := range header[:limit] {
		if kotlinKeywordValue(token, value) {
			return true
		}
	}
	return false
}

func kotlinSortUniqueDefinitions(
	definitions []sourceDefinition,
	lineCount int,
) []sourceDefinition {
	for index := range definitions {
		definitions[index] = normalizeCDefinition(definitions[index], lineCount)
	}
	sort.SliceStable(definitions, func(left, right int) bool {
		if definitions[left].line != definitions[right].line {
			return definitions[left].line < definitions[right].line
		}
		if definitions[left].column != definitions[right].column {
			return definitions[left].column < definitions[right].column
		}
		return definitions[left].symbol < definitions[right].symbol
	})
	result := definitions[:0]
	for _, definition := range definitions {
		if definition.symbol == "" || definition.line < 1 || definition.line > lineCount {
			continue
		}
		if len(result) > 0 {
			previous := &result[len(result)-1]
			if previous.symbol == definition.symbol && previous.line == definition.line &&
				previous.column == definition.column {
				if definition.ownsScope && !previous.ownsScope {
					*previous = definition
				} else if definition.ownsScope == previous.ownsScope {
					previous.scopeStart = min(previous.scopeStart, definition.scopeStart)
					if definition.scopeEnd > previous.scopeEnd {
						previous.scopeEnd = definition.scopeEnd
						previous.ownedEndColumn = definition.ownedEndColumn
					} else if definition.scopeEnd == previous.scopeEnd &&
						previous.ownedEndColumn == 0 {
						previous.ownedEndColumn = definition.ownedEndColumn
					}
				}
				continue
			}
		}
		result = append(result, definition)
	}
	return result
}
