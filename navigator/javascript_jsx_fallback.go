package navigator

import (
	"strings"
	"unicode/utf8"
)

const javascriptFallbackJSXMaximumFrames = 8192

// javascriptFallbackJSXCandidateResult describes one complete JSX value. Public
// string spans match the concrete parser's JSX-only masking policy. Lexical skip
// spans hide JSX markup, text, and expression-wrapper braces from JavaScript
// recovery while leaving each embedded expression body intact. Value-marker
// spans contain a final '>' byte that a semantic tokenizer can represent as
// javascriptLexicalValueToken.
type javascriptFallbackJSXCandidateResult struct {
	publicStringSpans       []javascriptByteSpan
	lexicalSkipSpans        []javascriptByteSpan
	lexicalValueMarkers     []javascriptByteSpan
	lexicalExpressionStarts []int
	lexicalExpressionEnds   []int
	lexicalStatementStarts  []int
	end                     int
}

type javascriptFallbackJSXFrameKind uint8

const (
	javascriptFallbackJSXElementFrame javascriptFallbackJSXFrameKind = iota
	javascriptFallbackJSXExpressionFrame
	javascriptFallbackJSXTemplateFrame
)

type javascriptFallbackJSXStage uint8

const (
	javascriptFallbackJSXOpening javascriptFallbackJSXStage = iota
	javascriptFallbackJSXAttributes
	javascriptFallbackJSXChildren
	javascriptFallbackJSXAfterChild
	javascriptFallbackJSXAfterChildExpression
	javascriptFallbackJSXAfterAttributeExpression
	javascriptFallbackJSXAfterSpreadExpression
	javascriptFallbackJSXAfterAttributeElement
	javascriptFallbackJSXExpression
	javascriptFallbackJSXAfterNestedElement
	javascriptFallbackJSXAfterTemplate
	javascriptFallbackJSXTemplateRaw
	javascriptFallbackJSXTemplateAfterExpression
)

type javascriptFallbackJSXFrame struct {
	previousToken         string
	previousWord          string
	parenControls         []bool
	parenAsyncArrows      []bool
	parenForHeaders       []bool
	parenForSeparators    []bool
	parenForBracketDepths []int
	parenForBraceDepths   []int
	braceBlocks           []bool
	braceValues           []bool
	braceClasses          []bool
	braceParenDepths      []int
	braceBracketDepths    []int
	braceFunctionModes    []javascriptFallbackFunctionMode
	braceFunctionIDs      []int
	pendingCallables      []javascriptFallbackCallable
	computedMembers       []javascriptFallbackComputedMember

	nameStart            int
	nameEnd              int
	lexicalStart         int
	attributeStart       int
	publicSnapshot       int
	bracketDepth         int
	pendingArrowID       int
	pendingArrowPosition int
	inheritedFunctionID  int
	bindingParenDepth    int
	bindingBracketDepth  int
	bindingBraceDepth    int

	kind                    javascriptFallbackJSXFrameKind
	stage                   javascriptFallbackJSXStage
	fragment                bool
	expressionAllowed       bool
	logicalLineWhitespace   bool
	pendingArrowMode        javascriptFallbackFunctionMode
	closedParenAsyncArrow   bool
	identifierAsyncArrow    bool
	directMemberPrefix      bool
	directMemberPrefixAsync bool
	directMemberPrefixGen   bool
	directMemberCandidate   bool
	directMemberAsync       bool
	directMemberGenerator   bool
	inheritedFunctionMode   javascriptFallbackFunctionMode
	asyncExpression         bool
	asyncPending            bool
	bindingDeclaration      bool
	bindingNeedsInitializer bool
	bindingCanEnd           bool
	restrictedStatementASI  bool
}

type javascriptFallbackJSXParser struct {
	source string
	frames []javascriptFallbackJSXFrame

	publicStringSpans       []javascriptByteSpan
	lexicalSkipSpans        []javascriptByteSpan
	lexicalValueMarkers     []javascriptByteSpan
	lexicalExpressionStarts []int
	lexicalExpressionEnds   []int
	lexicalStatementStarts  []int

	start          int
	offset         int
	steps          int
	maxSteps       int
	nextCallableID int

	inheritedFunctionMode javascriptFallbackFunctionMode
}

// javascriptFallbackJSXCandidateAt parses a single JSX value beginning at
// start. Callers must still decide whether JavaScript expression context permits
// JSX. Failure is transactional: no partial masks or end offset are returned.
func javascriptFallbackJSXCandidateAt(
	source string,
	start int,
) (javascriptFallbackJSXCandidateResult, bool) {
	result, ok, _ := javascriptFallbackJSXCandidateAtWithFailureEnd(source, start)
	return result, ok
}

func javascriptFallbackJSXCandidateAtWithFailureEnd(
	source string,
	start int,
) (javascriptFallbackJSXCandidateResult, bool, int) {
	return javascriptFallbackJSXCandidateAtWithFunctionMode(
		source, start, javascriptFallbackNoFunction,
	)
}

func javascriptFallbackJSXCandidateAtWithFunctionMode(
	source string,
	start int,
	functionMode javascriptFallbackFunctionMode,
) (javascriptFallbackJSXCandidateResult, bool, int) {
	if !javascriptFallbackJSXStartAt(source, start) {
		return javascriptFallbackJSXCandidateResult{}, false, min(start+1, len(source))
	}
	remaining := len(source) - start
	maxSteps := int(^uint(0) >> 1)
	if remaining <= (maxSteps-64)/16 {
		maxSteps = remaining*16 + 64
	}
	parser := javascriptFallbackJSXParser{
		source:                source,
		start:                 start,
		offset:                start,
		maxSteps:              maxSteps,
		inheritedFunctionMode: functionMode,
		frames: []javascriptFallbackJSXFrame{{
			kind:  javascriptFallbackJSXElementFrame,
			stage: javascriptFallbackJSXOpening,
		}},
	}
	if functionMode != javascriptFallbackNoFunction {
		parser.nextCallableID = 1
	}
	if !parser.parse() {
		return javascriptFallbackJSXCandidateResult{}, false,
			max(min(start+1, len(source)), parser.offset)
	}
	return javascriptFallbackJSXCandidateResult{
		end:                     parser.offset,
		publicStringSpans:       normalizeJavaScriptSpans(parser.publicStringSpans),
		lexicalSkipSpans:        normalizeJavaScriptSpans(parser.lexicalSkipSpans),
		lexicalValueMarkers:     normalizeJavaScriptSpans(parser.lexicalValueMarkers),
		lexicalExpressionStarts: parser.lexicalExpressionStarts,
		lexicalExpressionEnds:   parser.lexicalExpressionEnds,
		lexicalStatementStarts:  parser.lexicalStatementStarts,
	}, true, parser.offset
}

func (parser *javascriptFallbackJSXParser) parse() bool {
	for len(parser.frames) > 0 {
		parser.steps++
		if parser.steps > parser.maxSteps ||
			len(parser.frames) > len(parser.source)-parser.start+1 {
			return false
		}
		frameIndex := len(parser.frames) - 1
		switch parser.frames[frameIndex].kind {
		case javascriptFallbackJSXElementFrame:
			if !parser.stepElement(frameIndex) {
				return false
			}
		case javascriptFallbackJSXExpressionFrame:
			if !parser.stepExpression(frameIndex) {
				return false
			}
		case javascriptFallbackJSXTemplateFrame:
			if !parser.stepTemplate(frameIndex) {
				return false
			}
		default:
			return false
		}
	}
	return parser.offset > parser.start
}

func (parser *javascriptFallbackJSXParser) stepElement(frameIndex int) bool {
	frame := &parser.frames[frameIndex]
	switch frame.stage {
	case javascriptFallbackJSXOpening:
		return parser.openElement(frameIndex)
	case javascriptFallbackJSXAttributes:
		return parser.scanElementAttributes(frameIndex)
	case javascriptFallbackJSXChildren:
		return parser.scanElementChildren(frameIndex)
	case javascriptFallbackJSXAfterChild:
		frame.stage = javascriptFallbackJSXChildren
		return true
	case javascriptFallbackJSXAfterChildExpression:
		parser.appendSkip(parser.offset-1, parser.offset)
		parser.lexicalExpressionEnds = append(parser.lexicalExpressionEnds, parser.offset-1)
		frame.stage = javascriptFallbackJSXChildren
		return true
	case javascriptFallbackJSXAfterAttributeExpression:
		if parser.offset <= parser.start || parser.source[parser.offset-1] != '}' {
			return false
		}
		parser.appendPublic(parser.offset-1, parser.offset)
		parser.appendSkip(parser.offset-1, parser.offset)
		parser.lexicalExpressionEnds = append(parser.lexicalExpressionEnds, parser.offset-1)
		frame.lexicalStart = parser.offset
		frame.stage = javascriptFallbackJSXAttributes
		return true
	case javascriptFallbackJSXAfterSpreadExpression:
		parser.appendSkip(parser.offset-1, parser.offset)
		parser.lexicalExpressionEnds = append(parser.lexicalExpressionEnds, parser.offset-1)
		frame.lexicalStart = parser.offset
		frame.stage = javascriptFallbackJSXAttributes
		return true
	case javascriptFallbackJSXAfterAttributeElement:
		parser.publicStringSpans = parser.publicStringSpans[:frame.publicSnapshot]
		parser.appendPublic(frame.attributeStart, parser.offset)
		frame.lexicalStart = parser.offset
		frame.stage = javascriptFallbackJSXAttributes
		return true
	case javascriptFallbackJSXExpression, javascriptFallbackJSXAfterNestedElement,
		javascriptFallbackJSXAfterTemplate, javascriptFallbackJSXTemplateRaw,
		javascriptFallbackJSXTemplateAfterExpression:
		return false
	}
	return false
}

func (parser *javascriptFallbackJSXParser) openElement(frameIndex int) bool {
	frame := &parser.frames[frameIndex]
	if parser.offset < 0 || parser.offset >= len(parser.source) ||
		parser.source[parser.offset] != '<' ||
		strings.HasPrefix(parser.source[parser.offset:], "<!--") ||
		strings.HasPrefix(parser.source[parser.offset:], "</") {
		return false
	}
	frame.lexicalStart = parser.offset
	parser.offset++
	if parser.offset < len(parser.source) && parser.source[parser.offset] == '>' {
		frame.fragment = true
		frame.nameStart, frame.nameEnd = -1, -1
		parser.offset++
		parser.appendSkip(frame.lexicalStart, parser.offset)
		frame.stage = javascriptFallbackJSXChildren
		return true
	}
	nameEnd, ok := javascriptFallbackJSXNameEnd(parser.source, parser.offset)
	if !ok {
		return false
	}
	frame.nameStart, frame.nameEnd = parser.offset, nameEnd
	parser.offset = nameEnd
	frame.stage = javascriptFallbackJSXAttributes
	return true
}

func (parser *javascriptFallbackJSXParser) scanElementAttributes(frameIndex int) bool {
	frame := &parser.frames[frameIndex]
	parser.offset = javascriptFallbackJSXSkipWhitespace(parser.source, parser.offset)
	if parser.offset >= len(parser.source) {
		return false
	}
	if strings.HasPrefix(parser.source[parser.offset:], "/>") {
		greater := parser.offset + 1
		parser.appendSkip(frame.lexicalStart, greater)
		parser.appendValueMarker(greater)
		parser.offset = greater + 1
		parser.frames = parser.frames[:frameIndex]
		return true
	}
	if parser.source[parser.offset] == '>' {
		parser.offset++
		parser.appendSkip(frame.lexicalStart, parser.offset)
		frame.stage = javascriptFallbackJSXChildren
		return true
	}
	if parser.source[parser.offset] == '{' {
		if !javascriptFallbackJSXSpreadAttributeStartsAt(parser.source, parser.offset+1) {
			return false
		}
		parser.appendSkip(frame.lexicalStart, parser.offset)
		parser.appendSkip(parser.offset, parser.offset+1)
		parser.lexicalExpressionStarts = append(parser.lexicalExpressionStarts, parser.offset+1)
		frame.stage = javascriptFallbackJSXAfterSpreadExpression
		parser.offset++
		return parser.pushExpression()
	}

	attributeStart := parser.offset
	nameEnd, ok := javascriptFallbackJSXNameEnd(parser.source, parser.offset)
	if !ok {
		return false
	}
	parser.offset = nameEnd
	afterName := javascriptFallbackJSXSkipWhitespace(parser.source, parser.offset)
	if afterName >= len(parser.source) || parser.source[afterName] != '=' {
		parser.appendPublic(attributeStart, nameEnd)
		parser.offset = afterName
		return true
	}
	parser.offset = javascriptFallbackJSXSkipWhitespace(parser.source, afterName+1)
	if parser.offset >= len(parser.source) {
		return false
	}
	switch parser.source[parser.offset] {
	case '\'', '"':
		end, closed := javascriptFallbackJSXQuotedEnd(
			parser.source, parser.offset, parser.source[parser.offset],
		)
		if !closed {
			return false
		}
		parser.offset = end
		parser.appendPublic(attributeStart, end)
		return true
	case '{':
		if !javascriptFallbackJSXExpressionStartsAt(parser.source, parser.offset+1) {
			return false
		}
		parser.appendPublic(attributeStart, parser.offset+1)
		parser.appendSkip(frame.lexicalStart, parser.offset)
		parser.appendSkip(parser.offset, parser.offset+1)
		parser.lexicalExpressionStarts = append(parser.lexicalExpressionStarts, parser.offset+1)
		frame.stage = javascriptFallbackJSXAfterAttributeExpression
		parser.offset++
		return parser.pushExpression()
	case '<':
		if !javascriptFallbackJSXStartAt(parser.source, parser.offset) {
			return false
		}
		frame.attributeStart = attributeStart
		frame.publicSnapshot = len(parser.publicStringSpans)
		parser.appendSkip(frame.lexicalStart, parser.offset)
		frame.stage = javascriptFallbackJSXAfterAttributeElement
		return parser.pushElement()
	default:
		return false
	}
}

func (parser *javascriptFallbackJSXParser) scanElementChildren(frameIndex int) bool {
	frame := &parser.frames[frameIndex]
	if parser.offset >= len(parser.source) {
		return false
	}
	if parser.source[parser.offset] == '{' {
		parser.appendSkip(parser.offset, parser.offset+1)
		parser.lexicalExpressionStarts = append(parser.lexicalExpressionStarts, parser.offset+1)
		frame.stage = javascriptFallbackJSXAfterChildExpression
		parser.offset++
		return parser.pushExpression()
	}
	if parser.source[parser.offset] != '<' {
		start := parser.offset
		for parser.offset < len(parser.source) &&
			parser.source[parser.offset] != '<' && parser.source[parser.offset] != '{' {
			parser.offset++
		}
		parser.appendText(start, parser.offset)
		parser.appendSkip(start, parser.offset)
		return true
	}
	if strings.HasPrefix(parser.source[parser.offset:], "</") {
		return parser.closeElement(frameIndex)
	}
	if !javascriptFallbackJSXStartAt(parser.source, parser.offset) {
		return false
	}
	frame.stage = javascriptFallbackJSXAfterChild
	return parser.pushElement()
}

func (parser *javascriptFallbackJSXParser) closeElement(frameIndex int) bool {
	frame := &parser.frames[frameIndex]
	closingStart := parser.offset
	cursor := parser.offset + 2
	if frame.fragment {
		if cursor >= len(parser.source) || parser.source[cursor] != '>' {
			return false
		}
	} else {
		nameEnd, ok := javascriptFallbackJSXNameEnd(parser.source, cursor)
		if !ok || frame.nameStart < 0 || frame.nameEnd > len(parser.source) ||
			parser.source[cursor:nameEnd] != parser.source[frame.nameStart:frame.nameEnd] {
			return false
		}
		cursor = javascriptFallbackJSXSkipWhitespace(parser.source, nameEnd)
		if cursor >= len(parser.source) || parser.source[cursor] != '>' {
			return false
		}
	}
	greater := cursor
	parser.appendSkip(closingStart, greater)
	parser.appendValueMarker(greater)
	parser.offset = greater + 1
	parser.frames = parser.frames[:frameIndex]
	return true
}

func (parser *javascriptFallbackJSXParser) stepExpression(frameIndex int) bool {
	frame := &parser.frames[frameIndex]
	if frame.stage == javascriptFallbackJSXAfterNestedElement {
		frame.stage = javascriptFallbackJSXExpression
		frame.previousToken = "jsx"
		frame.previousWord = ""
		frame.expressionAllowed = false
		return true
	}
	if frame.stage == javascriptFallbackJSXAfterTemplate {
		frame.stage = javascriptFallbackJSXExpression
		frame.previousToken = "template"
		frame.previousWord = ""
		frame.expressionAllowed = false
		return true
	}
	if frame.stage != javascriptFallbackJSXExpression {
		return false
	}
	if parser.offset >= len(parser.source) {
		return false
	}
	if size := javascriptWhitespaceSize(parser.source, parser.offset); size > 0 {
		if javascriptLineTerminatorSize(parser.source, parser.offset) > 0 {
			frame.logicalLineWhitespace = true
		}
		parser.offset += size
		return true
	}
	if strings.HasPrefix(parser.source[parser.offset:], "<!--") ||
		frame.logicalLineWhitespace && strings.HasPrefix(parser.source[parser.offset:], "-->") {
		parser.offset = javascriptLineTerminatorOffset(parser.source, parser.offset)
		frame.logicalLineWhitespace = false
		return true
	}
	startsLogicalLine := frame.logicalLineWhitespace
	if strings.HasPrefix(parser.source[parser.offset:], "//") {
		end := javascriptLineTerminatorOffset(parser.source, parser.offset+2)
		frame.logicalLineWhitespace = !javascriptLineHasTokenAfterComment(
			parser.source, parser.offset, end, !frame.logicalLineWhitespace,
		)
		parser.offset = end
		return true
	}
	if strings.HasPrefix(parser.source[parser.offset:], "/*") {
		relative := strings.Index(parser.source[parser.offset+2:], "*/")
		if relative < 0 {
			return false
		}
		end := parser.offset + relative + 4
		frame.logicalLineWhitespace = !javascriptLineHasTokenAfterComment(
			parser.source, parser.offset, end, !frame.logicalLineWhitespace,
		)
		parser.offset = end
		return true
	}
	frame.logicalLineWhitespace = false
	if frame.applyLineTerminatorASI(parser.source, parser.offset, startsLogicalLine) &&
		parser.source[parser.offset] != ';' && parser.source[parser.offset] != '}' {
		parser.lexicalStatementStarts = append(
			parser.lexicalStatementStarts, parser.offset,
		)
	}
	if parser.source[parser.offset] == '}' {
		if len(frame.braceBlocks) == 0 {
			if len(frame.parenControls) != 0 || frame.bracketDepth != 0 {
				return false
			}
			parser.offset++
			parser.frames = parser.frames[:frameIndex]
			return true
		}
		block := frame.braceBlocks[len(frame.braceBlocks)-1]
		frame.braceBlocks = frame.braceBlocks[:len(frame.braceBlocks)-1]
		value := false
		if len(frame.braceValues) > 0 {
			value = frame.braceValues[len(frame.braceValues)-1]
			frame.braceValues = frame.braceValues[:len(frame.braceValues)-1]
		}
		if len(frame.braceClasses) > 0 {
			frame.braceClasses = frame.braceClasses[:len(frame.braceClasses)-1]
		}
		if len(frame.braceParenDepths) > 0 {
			frame.braceParenDepths = frame.braceParenDepths[:len(frame.braceParenDepths)-1]
		}
		if len(frame.braceBracketDepths) > 0 {
			frame.braceBracketDepths = frame.braceBracketDepths[:len(frame.braceBracketDepths)-1]
		}
		if len(frame.braceFunctionModes) > 0 {
			frame.braceFunctionModes = frame.braceFunctionModes[:len(frame.braceFunctionModes)-1]
		}
		if len(frame.braceFunctionIDs) > 0 {
			frame.braceFunctionIDs = frame.braceFunctionIDs[:len(frame.braceFunctionIDs)-1]
		}
		frame.expireUnreachablePendingCallables()
		frame.clearDirectMemberCandidate()
		frame.clearDirectMemberPrefix()
		frame.closedParenAsyncArrow = false
		frame.identifierAsyncArrow = false
		frame.previousToken = "}"
		frame.previousWord = ""
		frame.expressionAllowed = block && !value
		if frame.bindingDeclaration && frame.bindingNeedsInitializer &&
			frame.bindingAtCurrentDepth() {
			frame.bindingCanEnd = true
		}
		parser.offset++
		return true
	}
	if parser.source[parser.offset] == '<' && frame.expressionAllowed &&
		javascriptFallbackJSXStartAt(parser.source, parser.offset) {
		frame.stage = javascriptFallbackJSXAfterNestedElement
		return parser.pushElement()
	}
	switch parser.source[parser.offset] {
	case '\'', '"':
		end := javascriptQuotedLiteralEnd(
			parser.source, parser.offset, parser.source[parser.offset],
		)
		if end <= parser.offset || end > len(parser.source) ||
			parser.source[end-1] != parser.source[parser.offset] {
			return false
		}
		parser.offset = end
		frame.previousToken = "literal"
		frame.previousWord = ""
		frame.expressionAllowed = false
		frame.clearDirectMemberCandidate()
		frame.clearDirectMemberPrefix()
		frame.closedParenAsyncArrow = false
		frame.identifierAsyncArrow = false
		return true
	case '`':
		frame.stage = javascriptFallbackJSXAfterTemplate
		parser.offset++
		return parser.pushTemplate()
	case '/':
		frame.clearDirectMemberCandidate()
		frame.clearDirectMemberPrefix()
		frame.closedParenAsyncArrow = false
		frame.identifierAsyncArrow = false
		bindingASI := startsLogicalLine && frame.bindingCanEndAtCurrentDepth()
		restrictedASI := startsLogicalLine && frame.restrictedStatementASI
		if frame.expressionAllowed || bindingASI || restrictedASI {
			if end, ok := javascriptFallbackJSXRegexEnd(parser.source, parser.offset); ok {
				if bindingASI || restrictedASI ||
					javascriptFallbackRegexCanEnd(parser.source, end) {
					parser.offset = end
					frame.previousToken = "regex"
					frame.previousWord = ""
					frame.expressionAllowed = false
					if bindingASI {
						frame.clearBindingDeclaration()
					}
					frame.restrictedStatementASI = false
					return true
				}
			}
		}
		frame.previousToken = "/"
		frame.previousWord = ""
		frame.expressionAllowed = true
		parser.offset++
		if parser.offset < len(parser.source) && parser.source[parser.offset] == '=' {
			parser.offset++
		}
		return true
	}
	if javascriptIdentifierStartAt(parser.source, parser.offset) {
		end := javascriptIdentifierSourceEnd(parser.source, parser.offset)
		word := parser.source[parser.offset:end]
		decoded := javascriptDecodedIdentifier(word)
		previousToken := frame.previousToken
		previousWord := frame.previousWord
		if startsLogicalLine {
			frame.restrictedStatementASI = false
			frame.clearDirectMemberPrefix()
		}
		propertyName := previousToken == "." || previousToken == "?."
		memberName := frame.identifierIsDirectMemberName(previousToken, startsLogicalLine)
		contextualOfOperator := decoded == "of" && frame.contextualOfIsOperator(previousToken)
		memberPrefixAsync := frame.directMemberPrefixAsync
		memberPrefixGenerator := frame.directMemberPrefixGen
		frame.identifierAsyncArrow = !startsLogicalLine && previousWord == "async" &&
			frame.asyncPending && !propertyName && !memberName
		frame.closedParenAsyncArrow = false
		if !propertyName && !memberName && javascriptHardDeclarationToken(decoded) &&
			!javascriptFallbackKeywordStartsValue(previousToken, previousWord) &&
			previousWord != "export" && previousWord != "default" {
			frame.expirePendingCallablesAtCurrentDepth()
		}
		if frame.bindingCanEndAtCurrentDepth() && startsLogicalLine {
			frame.clearBindingDeclaration()
		}
		bindingStarted := false
		if !propertyName && !memberName && frame.bindingDeclarationStarts(
			decoded, previousToken, previousWord, startsLogicalLine,
		) && javascriptFallbackBindingKeywordPlausible(parser.source, end, decoded) {
			frame.beginBindingDeclaration()
			bindingStarted = true
		}
		if !propertyName && !memberName && (decoded == "function" || decoded == "class") &&
			javascriptFallbackCallableKeywordPlausible(parser.source, end, decoded) {
			value := javascriptFallbackKeywordStartsValue(previousToken, previousWord)
			asyncFunction := false
			if decoded == "function" && previousWord == "async" &&
				frame.asyncPending && !startsLogicalLine {
				value = frame.asyncExpression
				asyncFunction = true
			}
			parser.nextCallableID++
			frame.pendingCallables = append(frame.pendingCallables, javascriptFallbackCallable{
				parenDepth: len(frame.parenControls), bracketDepth: frame.bracketDepth,
				braceDepth: len(frame.braceBlocks), id: parser.nextCallableID,
				value: value, class: decoded == "class", function: decoded == "function",
				mode: javascriptFallbackFunctionModeFor(asyncFunction, false),
			})
		}
		if memberName {
			frame.directMemberCandidate = true
			frame.directMemberAsync = memberPrefixAsync
			frame.directMemberGenerator = memberPrefixGenerator
			frame.clearDirectMemberPrefix()
			switch decoded {
			case "async":
				frame.directMemberPrefix = true
				frame.directMemberPrefixAsync = true
				frame.directMemberPrefixGen = memberPrefixGenerator
			case "get", "set", "static":
				frame.directMemberPrefix = true
				frame.directMemberPrefixAsync = memberPrefixAsync
				frame.directMemberPrefixGen = memberPrefixGenerator
			}
		} else {
			frame.clearDirectMemberCandidate()
			frame.clearDirectMemberPrefix()
		}
		switch {
		case propertyName:
			frame.asyncPending = false
		case decoded == "async":
			frame.asyncExpression = javascriptFallbackKeywordStartsValue(
				previousToken, previousWord,
			)
			frame.asyncPending = true
		default:
			frame.asyncPending = false
		}
		frame.previousToken = word
		frame.previousWord = decoded
		if propertyName || memberName && javascriptControlHeaderKeyword(decoded) {
			frame.previousWord = ""
		}
		if decoded == "await" && previousWord == "for" {
			frame.previousWord = "for"
		}
		if contextualOfOperator || decoded == "in" && frame.inForHeaderBeforeSeparator() {
			frame.parenForSeparators[len(frame.parenForSeparators)-1] = true
		}
		if !propertyName && !memberName &&
			(decoded == "break" || decoded == "continue" || decoded == "debugger") {
			frame.restrictedStatementASI = true
		}
		switch {
		case propertyName:
			frame.expressionAllowed = false
		case (decoded == "let" || decoded == "using") && !bindingStarted:
			frame.expressionAllowed = false
		case decoded == "of" && !contextualOfOperator:
			frame.expressionAllowed = false
		case (decoded == "await" || decoded == "yield") &&
			!frame.contextualAwaitOrYieldIsOperator(decoded):
			frame.expressionAllowed = false
		default:
			frame.expressionAllowed = javascriptKeywordAllowsExpression(decoded)
		}
		if frame.bindingDeclaration && frame.bindingNeedsInitializer &&
			frame.bindingAtCurrentDepth() &&
			!javascriptLexicalBindingDeclarationToken(decoded) {
			frame.bindingCanEnd = true
		}
		parser.offset = end
		return true
	}
	if javascriptNumberStart(parser.source, parser.offset) {
		parser.offset = javascriptNumberEnd(parser.source, parser.offset)
		frame.previousToken = "number"
		frame.previousWord = ""
		frame.expressionAllowed = false
		frame.clearDirectMemberCandidate()
		frame.clearDirectMemberPrefix()
		frame.closedParenAsyncArrow = false
		frame.identifierAsyncArrow = false
		return true
	}
	return parser.scanExpressionPunctuation(frame, startsLogicalLine)
}

func (parser *javascriptFallbackJSXParser) scanExpressionPunctuation(
	frame *javascriptFallbackJSXFrame,
	startsLogicalLine bool,
) bool {
	start := parser.offset
	end := javascriptPunctuationEnd(parser.source, start)
	if end <= start || end > len(parser.source) {
		return false
	}
	token := parser.source[start:end]
	switch token {
	case "(":
		if frame.directMemberCandidate {
			parser.nextCallableID++
			frame.pendingCallables = append(frame.pendingCallables, javascriptFallbackCallable{
				parenDepth: len(frame.parenControls), bracketDepth: frame.bracketDepth,
				braceDepth: len(frame.braceBlocks), id: parser.nextCallableID,
				function: true,
				mode: javascriptFallbackFunctionModeFor(
					frame.directMemberAsync, frame.directMemberGenerator,
				),
			})
		}
		frame.parenControls = append(
			frame.parenControls, javascriptControlHeaderKeyword(frame.previousWord),
		)
		frame.parenAsyncArrows = append(frame.parenAsyncArrows,
			!startsLogicalLine && frame.previousWord == "async" && frame.asyncPending)
		frame.parenForHeaders = append(frame.parenForHeaders, frame.previousWord == "for")
		frame.parenForSeparators = append(frame.parenForSeparators, false)
		frame.parenForBracketDepths = append(frame.parenForBracketDepths, frame.bracketDepth)
		frame.parenForBraceDepths = append(frame.parenForBraceDepths, len(frame.braceBlocks))
		frame.clearDirectMemberCandidate()
		frame.clearDirectMemberPrefix()
		frame.closedParenAsyncArrow = false
		frame.identifierAsyncArrow = false
		frame.expressionAllowed = true
	case ")":
		if len(frame.parenControls) == 0 {
			return false
		}
		control := frame.parenControls[len(frame.parenControls)-1]
		frame.parenControls = frame.parenControls[:len(frame.parenControls)-1]
		asyncArrow := false
		if len(frame.parenAsyncArrows) > 0 {
			asyncArrow = frame.parenAsyncArrows[len(frame.parenAsyncArrows)-1]
			frame.parenAsyncArrows = frame.parenAsyncArrows[:len(frame.parenAsyncArrows)-1]
		}
		if len(frame.parenForHeaders) > 0 {
			frame.parenForHeaders = frame.parenForHeaders[:len(frame.parenForHeaders)-1]
		}
		if len(frame.parenForSeparators) > 0 {
			frame.parenForSeparators = frame.parenForSeparators[:len(frame.parenForSeparators)-1]
		}
		if len(frame.parenForBracketDepths) > 0 {
			frame.parenForBracketDepths = frame.parenForBracketDepths[:len(frame.parenForBracketDepths)-1]
		}
		if len(frame.parenForBraceDepths) > 0 {
			frame.parenForBraceDepths = frame.parenForBraceDepths[:len(frame.parenForBraceDepths)-1]
		}
		frame.expireUnreachablePendingCallables()
		frame.clearDirectMemberCandidate()
		frame.clearDirectMemberPrefix()
		frame.closedParenAsyncArrow = asyncArrow
		frame.identifierAsyncArrow = false
		frame.expressionAllowed = control
	case "[":
		if frame.identifierIsDirectMemberName(frame.previousToken, startsLogicalLine) {
			frame.computedMembers = append(
				frame.computedMembers,
				javascriptFallbackComputedMember{
					bracketDepth: frame.bracketDepth + 1,
					braceDepth:   len(frame.braceBlocks),
					async:        frame.directMemberPrefixAsync,
					generator:    frame.directMemberPrefixGen,
				},
			)
		}
		frame.bracketDepth++
		frame.clearDirectMemberCandidate()
		frame.clearDirectMemberPrefix()
		frame.closedParenAsyncArrow = false
		frame.identifierAsyncArrow = false
		frame.expressionAllowed = true
	case "]":
		if frame.bracketDepth == 0 {
			return false
		}
		computedMember := javascriptFallbackComputedMember{}
		computedMemberClosed := false
		if len(frame.computedMembers) > 0 {
			computedMember = frame.computedMembers[len(frame.computedMembers)-1]
			computedMemberClosed = computedMember.bracketDepth == frame.bracketDepth &&
				computedMember.braceDepth == len(frame.braceBlocks)
		}
		frame.bracketDepth--
		frame.expireUnreachablePendingCallables()
		frame.clearDirectMemberCandidate()
		frame.clearDirectMemberPrefix()
		if computedMemberClosed {
			frame.directMemberCandidate = true
			frame.directMemberAsync = computedMember.async
			frame.directMemberGenerator = computedMember.generator
			frame.computedMembers = frame.computedMembers[:len(frame.computedMembers)-1]
		}
		frame.closedParenAsyncArrow = false
		frame.identifierAsyncArrow = false
		frame.expressionAllowed = false
		if frame.bindingDeclaration && frame.bindingNeedsInitializer &&
			frame.bindingAtCurrentDepth() {
			frame.bindingCanEnd = true
		}
	case "{":
		block := javascriptFallbackBraceIsBlock(frame.previousToken, frame.expressionAllowed)
		value := !block || frame.previousToken == "=>"
		classBrace := false
		functionMode, functionID := frame.activeFunctionMode()
		if len(frame.pendingCallables) > 0 {
			pending := frame.pendingCallables[len(frame.pendingCallables)-1]
			if pending.parenDepth == len(frame.parenControls) &&
				pending.bracketDepth == frame.bracketDepth &&
				pending.braceDepth == len(frame.braceBlocks) &&
				(pending.class || !pending.arrow && frame.previousToken == ")" ||
					pending.arrow && frame.previousToken == "=>") {
				value = pending.value
				classBrace = pending.class
				if pending.function {
					functionMode = pending.mode
					functionID = pending.id
				}
				frame.truncatePendingCallables(len(frame.pendingCallables) - 1)
			}
		}
		frame.braceBlocks = append(frame.braceBlocks, block)
		frame.braceValues = append(frame.braceValues, value)
		frame.braceClasses = append(frame.braceClasses, classBrace)
		frame.braceParenDepths = append(frame.braceParenDepths, len(frame.parenControls))
		frame.braceBracketDepths = append(frame.braceBracketDepths, frame.bracketDepth)
		frame.braceFunctionModes = append(frame.braceFunctionModes, functionMode)
		frame.braceFunctionIDs = append(frame.braceFunctionIDs, functionID)
		frame.clearDirectMemberCandidate()
		frame.clearDirectMemberPrefix()
		frame.closedParenAsyncArrow = false
		frame.identifierAsyncArrow = false
		frame.expressionAllowed = true
	case "++", "--":
		frame.clearDirectMemberCandidate()
		frame.clearDirectMemberPrefix()
		frame.closedParenAsyncArrow = false
		frame.identifierAsyncArrow = false
		frame.expressionAllowed = false
	case ".", "?.":
		frame.clearDirectMemberCandidate()
		frame.clearDirectMemberPrefix()
		frame.closedParenAsyncArrow = false
		frame.identifierAsyncArrow = false
		frame.expressionAllowed = false
	case ";":
		frame.expirePendingCallablesAtCurrentDepth()
		frame.expressionAllowed = true
		if frame.bindingDeclaration && frame.bindingAtCurrentDepth() {
			frame.clearBindingDeclaration()
		}
		frame.restrictedStatementASI = false
		frame.clearDirectMemberCandidate()
		frame.clearDirectMemberPrefix()
		frame.closedParenAsyncArrow = false
		frame.identifierAsyncArrow = false
	case ",":
		frame.expirePendingCallablesAtCurrentDepth()
		frame.expressionAllowed = true
		if frame.bindingDeclaration && frame.bindingAtCurrentDepth() {
			frame.bindingNeedsInitializer = true
			frame.bindingCanEnd = false
		}
		frame.clearDirectMemberCandidate()
		frame.clearDirectMemberPrefix()
		frame.closedParenAsyncArrow = false
		frame.identifierAsyncArrow = false
	case "=":
		frame.expirePendingCallablesAtCurrentDepth()
		frame.expressionAllowed = true
		if frame.bindingDeclaration && frame.bindingAtCurrentDepth() {
			frame.bindingNeedsInitializer = false
			frame.bindingCanEnd = false
		}
		frame.clearDirectMemberCandidate()
		frame.clearDirectMemberPrefix()
		frame.closedParenAsyncArrow = false
		frame.identifierAsyncArrow = false
	case "=>":
		frame.expirePendingCallablesAtCurrentDepth()
		parser.nextCallableID++
		frame.pendingCallables = append(frame.pendingCallables, javascriptFallbackCallable{
			parenDepth: len(frame.parenControls), bracketDepth: frame.bracketDepth,
			braceDepth: len(frame.braceBlocks), id: parser.nextCallableID,
			previousArrowPosition: frame.pendingArrowPosition,
			value:                 true, function: true, arrow: true,
			mode: javascriptFallbackFunctionModeFor(
				frame.identifierAsyncArrow || frame.closedParenAsyncArrow, false,
			),
		})
		frame.pendingArrowMode = frame.pendingCallables[len(frame.pendingCallables)-1].mode
		frame.pendingArrowID = parser.nextCallableID
		frame.pendingArrowPosition = len(frame.pendingCallables)
		frame.clearDirectMemberCandidate()
		frame.clearDirectMemberPrefix()
		frame.closedParenAsyncArrow = false
		frame.identifierAsyncArrow = false
		frame.expressionAllowed = true
	case "*":
		functionGenerator := false
		if len(frame.pendingCallables) > 0 {
			pending := &frame.pendingCallables[len(frame.pendingCallables)-1]
			if pending.function && !pending.arrow &&
				pending.parenDepth == len(frame.parenControls) &&
				pending.bracketDepth == frame.bracketDepth &&
				pending.braceDepth == len(frame.braceBlocks) {
				asyncFunction := pending.mode == javascriptFallbackAsyncFunction ||
					pending.mode == javascriptFallbackAsyncGeneratorFunction
				pending.mode = javascriptFallbackFunctionModeFor(asyncFunction, true)
				functionGenerator = true
			}
		}
		if !functionGenerator &&
			frame.identifierIsDirectMemberName(frame.previousToken, startsLogicalLine) {
			asyncMember := frame.directMemberPrefixAsync
			frame.clearDirectMemberCandidate()
			frame.clearDirectMemberPrefix()
			frame.directMemberPrefix = true
			frame.directMemberPrefixAsync = asyncMember
			frame.directMemberPrefixGen = true
		} else if !functionGenerator {
			frame.clearDirectMemberCandidate()
			frame.clearDirectMemberPrefix()
		}
		frame.closedParenAsyncArrow = false
		frame.identifierAsyncArrow = false
		frame.expressionAllowed = true
	case ":", "?", "!", "~", "+", "-", "%", "&", "|", "^",
		"<", ">", "<=", ">=", "==", "!=", "===", "!==", "+=", "-=",
		"*=", "/=", "%=", "&&", "||", "??", "&&=", "||=", "??=", "**", "**=",
		"<<", ">>", ">>>", "<<=", ">>=", ">>>=", "&=", "|=", "^=":
		frame.clearDirectMemberCandidate()
		frame.clearDirectMemberPrefix()
		frame.closedParenAsyncArrow = false
		frame.identifierAsyncArrow = false
		frame.expressionAllowed = true
	default:
		frame.clearDirectMemberCandidate()
		frame.clearDirectMemberPrefix()
		frame.closedParenAsyncArrow = false
		frame.identifierAsyncArrow = false
	}
	frame.previousToken = token
	frame.previousWord = ""
	parser.offset = end
	return true
}

func (frame *javascriptFallbackJSXFrame) bindingDeclarationStarts(
	word, previousToken, previousWord string,
	startsLine bool,
) bool {
	if !javascriptLexicalBindingDeclarationToken(word) ||
		previousToken == "." || previousToken == "?." {
		return false
	}
	if len(frame.braceClasses) > 0 && frame.braceClasses[len(frame.braceClasses)-1] {
		return false
	}
	if len(frame.braceBlocks) > 0 && !frame.braceBlocks[len(frame.braceBlocks)-1] {
		return false
	}
	switch previousToken {
	case "", ";", "}":
		return true
	case "{":
		return len(frame.braceBlocks) == 0 || frame.braceBlocks[len(frame.braceBlocks)-1]
	case "(":
		return len(frame.parenControls) > 0 && frame.parenControls[len(frame.parenControls)-1]
	}
	return previousWord == "export" || previousWord == "for" || startsLine
}

func (frame *javascriptFallbackJSXFrame) applyLineTerminatorASI(
	source string,
	offset int,
	startsLine bool,
) bool {
	if !startsLine {
		return false
	}
	forced := false
	if frame.restrictedStatementASI {
		frame.restrictedStatementASI = false
		frame.expressionAllowed = true
		forced = true
	}
	if frame.bindingCanEndAtCurrentDepth() &&
		!frame.bindingDeclarationContinuesAt(source, offset) {
		frame.clearBindingDeclaration()
		frame.expressionAllowed = true
		forced = true
	}
	if forced {
		frame.expirePendingCallablesAtCurrentDepth()
	}
	return forced
}

func (frame *javascriptFallbackJSXFrame) bindingDeclarationContinuesAt(
	source string,
	offset int,
) bool {
	if offset < 0 || offset >= len(source) {
		return false
	}
	switch source[offset] {
	case '=', ',':
		return true
	}
	if !javascriptIdentifierStartAt(source, offset) || len(frame.parenControls) == 0 ||
		!frame.parenControls[len(frame.parenControls)-1] {
		return false
	}
	end := javascriptIdentifierSourceEnd(source, offset)
	word := javascriptDecodedIdentifier(source[offset:end])
	return word == "in" || word == "of"
}

func (frame *javascriptFallbackJSXFrame) expirePendingCallablesAtCurrentDepth() {
	end := len(frame.pendingCallables)
	for end > 0 {
		pending := frame.pendingCallables[end-1]
		unreachable := pending.parenDepth > len(frame.parenControls) ||
			pending.bracketDepth > frame.bracketDepth ||
			pending.braceDepth > len(frame.braceBlocks)
		current := pending.parenDepth == len(frame.parenControls) &&
			pending.bracketDepth == frame.bracketDepth &&
			pending.braceDepth == len(frame.braceBlocks)
		if !unreachable && !current {
			break
		}
		end--
	}
	if end < len(frame.pendingCallables) {
		frame.truncatePendingCallables(end)
	}
}

func (frame *javascriptFallbackJSXFrame) expireUnreachablePendingCallables() {
	end := len(frame.pendingCallables)
	for end > 0 {
		pending := frame.pendingCallables[end-1]
		if pending.parenDepth <= len(frame.parenControls) &&
			pending.bracketDepth <= frame.bracketDepth &&
			pending.braceDepth <= len(frame.braceBlocks) {
			break
		}
		end--
	}
	if end < len(frame.pendingCallables) {
		frame.truncatePendingCallables(end)
	}
}

func (frame *javascriptFallbackJSXFrame) truncatePendingCallables(length int) {
	length = max(0, min(length, len(frame.pendingCallables)))
	for frame.pendingArrowPosition > length {
		position := frame.pendingArrowPosition
		frame.pendingArrowPosition = frame.pendingCallables[position-1].previousArrowPosition
	}
	frame.pendingCallables = frame.pendingCallables[:length]
	if frame.pendingArrowPosition == 0 {
		frame.pendingArrowMode = javascriptFallbackNoFunction
		frame.pendingArrowID = 0
		return
	}
	pending := frame.pendingCallables[frame.pendingArrowPosition-1]
	frame.pendingArrowMode = pending.mode
	frame.pendingArrowID = pending.id
}

func (frame *javascriptFallbackJSXFrame) identifierIsDirectMemberName(
	previousToken string,
	startsLogicalLine bool,
) bool {
	if len(frame.braceBlocks) == 0 ||
		len(frame.braceParenDepths) != len(frame.braceBlocks) ||
		len(frame.braceBracketDepths) != len(frame.braceBlocks) ||
		len(frame.parenControls) != frame.braceParenDepths[len(frame.braceParenDepths)-1] ||
		frame.bracketDepth != frame.braceBracketDepths[len(frame.braceBracketDepths)-1] {
		return false
	}
	classMember := frame.braceClasses[len(frame.braceClasses)-1]
	objectMember := !frame.braceBlocks[len(frame.braceBlocks)-1]
	if !classMember && !objectMember {
		return false
	}
	if frame.directMemberPrefix {
		return true
	}
	if objectMember {
		return previousToken == "{" || previousToken == ","
	}
	switch previousToken {
	case "{", ";", "}":
		return true
	}
	return startsLogicalLine && javascriptFallbackTokenCanEndExpression(previousToken)
}

func (frame *javascriptFallbackJSXFrame) contextualOfIsOperator(
	previousToken string,
) bool {
	return frame.inForHeaderBeforeSeparator() &&
		javascriptFallbackTokenCanEndExpression(previousToken)
}

func (frame *javascriptFallbackJSXFrame) inForHeaderBeforeSeparator() bool {
	return len(frame.parenForHeaders) > 0 &&
		len(frame.parenForHeaders) == len(frame.parenControls) &&
		len(frame.parenForSeparators) == len(frame.parenControls) &&
		len(frame.parenForBracketDepths) == len(frame.parenControls) &&
		len(frame.parenForBraceDepths) == len(frame.parenControls) &&
		frame.parenForHeaders[len(frame.parenForHeaders)-1] &&
		!frame.parenForSeparators[len(frame.parenForSeparators)-1] &&
		frame.bracketDepth == frame.parenForBracketDepths[len(frame.parenForBracketDepths)-1] &&
		len(frame.braceBlocks) == frame.parenForBraceDepths[len(frame.parenForBraceDepths)-1]
}

func (frame *javascriptFallbackJSXFrame) contextualAwaitOrYieldIsOperator(
	word string,
) bool {
	mode, _ := frame.activeFunctionMode()
	return mode.treatsContextualWordAsOperator(word)
}

func (parser *javascriptFallbackJSXParser) activeFunctionMode() (
	javascriptFallbackFunctionMode,
	int,
) {
	mode := parser.inheritedFunctionMode
	id := 0
	if mode != javascriptFallbackNoFunction {
		id = 1
	}
	for index := len(parser.frames) - 1; index >= 0; index-- {
		if parser.frames[index].kind != javascriptFallbackJSXExpressionFrame {
			continue
		}
		candidateMode, candidateID := parser.frames[index].activeFunctionMode()
		if candidateID >= id {
			mode, id = candidateMode, candidateID
		}
		break
	}
	return mode, id
}

func (frame *javascriptFallbackJSXFrame) activeFunctionMode() (
	javascriptFallbackFunctionMode,
	int,
) {
	bestID := frame.inheritedFunctionID
	mode := frame.inheritedFunctionMode
	if len(frame.braceFunctionModes) > 0 &&
		len(frame.braceFunctionIDs) == len(frame.braceFunctionModes) &&
		frame.braceFunctionIDs[len(frame.braceFunctionIDs)-1] >= bestID {
		bestID = frame.braceFunctionIDs[len(frame.braceFunctionIDs)-1]
		mode = frame.braceFunctionModes[len(frame.braceFunctionModes)-1]
	}
	if frame.pendingArrowID > bestID {
		bestID = frame.pendingArrowID
		mode = frame.pendingArrowMode
	}
	return mode, bestID
}

func (frame *javascriptFallbackJSXFrame) clearDirectMemberPrefix() {
	frame.directMemberPrefix = false
	frame.directMemberPrefixAsync = false
	frame.directMemberPrefixGen = false
}

func (frame *javascriptFallbackJSXFrame) clearDirectMemberCandidate() {
	frame.directMemberCandidate = false
	frame.directMemberAsync = false
	frame.directMemberGenerator = false
}

func (frame *javascriptFallbackJSXFrame) beginBindingDeclaration() {
	frame.bindingDeclaration = true
	frame.bindingNeedsInitializer = true
	frame.bindingCanEnd = false
	frame.bindingParenDepth = len(frame.parenControls)
	frame.bindingBracketDepth = frame.bracketDepth
	frame.bindingBraceDepth = len(frame.braceBlocks)
}

func (frame *javascriptFallbackJSXFrame) bindingAtCurrentDepth() bool {
	return frame.bindingDeclaration && frame.bindingParenDepth == len(frame.parenControls) &&
		frame.bindingBracketDepth == frame.bracketDepth &&
		frame.bindingBraceDepth == len(frame.braceBlocks)
}

func (frame *javascriptFallbackJSXFrame) bindingCanEndAtCurrentDepth() bool {
	return frame.bindingNeedsInitializer && frame.bindingCanEnd && frame.bindingAtCurrentDepth()
}

func (frame *javascriptFallbackJSXFrame) clearBindingDeclaration() {
	frame.bindingDeclaration = false
	frame.bindingNeedsInitializer = false
	frame.bindingCanEnd = false
}

func (parser *javascriptFallbackJSXParser) stepTemplate(frameIndex int) bool {
	frame := &parser.frames[frameIndex]
	if frame.stage == javascriptFallbackJSXTemplateAfterExpression {
		frame.stage = javascriptFallbackJSXTemplateRaw
		return true
	}
	if frame.stage != javascriptFallbackJSXTemplateRaw || parser.offset >= len(parser.source) {
		return false
	}
	if parser.source[parser.offset] == '\\' {
		parser.offset++
		if parser.offset < len(parser.source) {
			_, size := utf8.DecodeRuneInString(parser.source[parser.offset:])
			if size < 1 {
				size = 1
			}
			parser.offset += size
		}
		return true
	}
	if parser.source[parser.offset] == '`' {
		parser.offset++
		parser.frames = parser.frames[:frameIndex]
		return true
	}
	if strings.HasPrefix(parser.source[parser.offset:], "${") {
		frame.stage = javascriptFallbackJSXTemplateAfterExpression
		parser.offset += 2
		return parser.pushExpression()
	}
	_, size := utf8.DecodeRuneInString(parser.source[parser.offset:])
	if size < 1 {
		size = 1
	}
	parser.offset += size
	return true
}

func (parser *javascriptFallbackJSXParser) pushElement() bool {
	if len(parser.frames) >= javascriptFallbackJSXMaximumFrames ||
		len(parser.frames) >= len(parser.source)-parser.start+1 {
		return false
	}
	parser.frames = append(parser.frames, javascriptFallbackJSXFrame{
		kind:  javascriptFallbackJSXElementFrame,
		stage: javascriptFallbackJSXOpening,
	})
	return true
}

func (parser *javascriptFallbackJSXParser) pushExpression() bool {
	if len(parser.frames) >= javascriptFallbackJSXMaximumFrames ||
		len(parser.frames) >= len(parser.source)-parser.start+1 {
		return false
	}
	functionMode, functionID := parser.activeFunctionMode()
	parser.frames = append(parser.frames, javascriptFallbackJSXFrame{
		kind:                  javascriptFallbackJSXExpressionFrame,
		stage:                 javascriptFallbackJSXExpression,
		expressionAllowed:     true,
		logicalLineWhitespace: true,
		inheritedFunctionMode: functionMode,
		inheritedFunctionID:   functionID,
	})
	return true
}

func (parser *javascriptFallbackJSXParser) pushTemplate() bool {
	if len(parser.frames) >= javascriptFallbackJSXMaximumFrames ||
		len(parser.frames) >= len(parser.source)-parser.start+1 {
		return false
	}
	parser.frames = append(parser.frames, javascriptFallbackJSXFrame{
		kind:  javascriptFallbackJSXTemplateFrame,
		stage: javascriptFallbackJSXTemplateRaw,
	})
	return true
}

func (parser *javascriptFallbackJSXParser) appendPublic(start, end int) {
	if start >= 0 && start < end && end <= len(parser.source) {
		parser.publicStringSpans = append(
			parser.publicStringSpans, javascriptByteSpan{start: start, end: end},
		)
	}
}

func (parser *javascriptFallbackJSXParser) appendText(start, end int) {
	for offset := start; offset < end; {
		size := javascriptWhitespaceSize(parser.source, offset)
		if size < 1 {
			parser.appendPublic(start, end)
			return
		}
		offset += size
	}
}

func (parser *javascriptFallbackJSXParser) appendSkip(start, end int) {
	if start >= 0 && start < end && end <= len(parser.source) {
		parser.lexicalSkipSpans = append(
			parser.lexicalSkipSpans, javascriptByteSpan{start: start, end: end},
		)
	}
}

func (parser *javascriptFallbackJSXParser) appendValueMarker(offset int) {
	if offset >= 0 && offset < len(parser.source) && parser.source[offset] == '>' {
		parser.lexicalValueMarkers = append(
			parser.lexicalValueMarkers,
			javascriptByteSpan{start: offset, end: offset + 1},
		)
	}
}

func javascriptFallbackJSXStartAt(source string, offset int) bool {
	if offset < 0 || offset+1 >= len(source) || source[offset] != '<' ||
		strings.HasPrefix(source[offset:], "<!--") ||
		strings.HasPrefix(source[offset:], "</") {
		return false
	}
	if source[offset+1] == '>' {
		return true
	}
	_, ok := javascriptFallbackJSXNameEnd(source, offset+1)
	return ok
}

func javascriptFallbackJSXNameEnd(source string, start int) (int, bool) {
	end, ok := javascriptFallbackJSXNamePartEnd(source, start)
	if !ok {
		return start, false
	}
	for end < len(source) && (source[end] == '.' || source[end] == ':') {
		var partOK bool
		end, partOK = javascriptFallbackJSXNamePartEnd(source, end+1)
		if !partOK {
			return start, false
		}
	}
	return end, true
}

func javascriptFallbackJSXNamePartEnd(source string, start int) (int, bool) {
	if !javascriptIdentifierStartAt(source, start) {
		return start, false
	}
	_, size, ok := javascriptIdentifierRune(source, start)
	if !ok || size < 1 {
		return start, false
	}
	offset := start + size
	for offset < len(source) {
		if source[offset] == '-' {
			offset++
			continue
		}
		r, runeSize, runeOK := javascriptIdentifierRune(source, offset)
		if !runeOK || runeSize < 1 || !javascriptIdentifierContinueRune(r) {
			break
		}
		offset += runeSize
	}
	return offset, true
}

func javascriptFallbackJSXSkipWhitespace(source string, offset int) int {
	for offset < len(source) {
		size := javascriptWhitespaceSize(source, offset)
		if size < 1 {
			break
		}
		offset += size
	}
	return offset
}

func javascriptFallbackJSXQuotedEnd(source string, start int, quote byte) (int, bool) {
	for offset := start + 1; offset < len(source); {
		if source[offset] == quote {
			return offset + 1, true
		}
		_, size := utf8.DecodeRuneInString(source[offset:])
		if size < 1 {
			size = 1
		}
		offset += size
	}
	return len(source), false
}

func javascriptFallbackJSXExpressionStartsAt(source string, offset int) bool {
	sawComment := false
	for offset < len(source) {
		if size := javascriptWhitespaceSize(source, offset); size > 0 {
			offset += size
			continue
		}
		if strings.HasPrefix(source[offset:], "//") {
			sawComment = true
			offset = javascriptLineTerminatorOffset(source, offset+2)
			continue
		}
		if strings.HasPrefix(source[offset:], "/*") {
			end := strings.Index(source[offset+2:], "*/")
			if end < 0 {
				return false
			}
			sawComment = true
			offset += end + 4
			continue
		}
		break
	}
	return offset < len(source) && (source[offset] != '}' || sawComment)
}

func javascriptFallbackJSXSpreadAttributeStartsAt(source string, offset int) bool {
	offset = javascriptFallbackJSXSkipExpressionTrivia(source, offset)
	if !strings.HasPrefix(source[offset:], "...") {
		return false
	}
	offset = javascriptFallbackJSXSkipExpressionTrivia(source, offset+3)
	return offset < len(source) && source[offset] != '}'
}

func javascriptFallbackJSXSkipExpressionTrivia(source string, offset int) int {
	for offset < len(source) {
		if size := javascriptWhitespaceSize(source, offset); size > 0 {
			offset += size
			continue
		}
		if strings.HasPrefix(source[offset:], "//") {
			offset = javascriptLineTerminatorOffset(source, offset+2)
			continue
		}
		if strings.HasPrefix(source[offset:], "/*") {
			end := strings.Index(source[offset+2:], "*/")
			if end < 0 {
				return len(source)
			}
			offset += end + 4
			continue
		}
		break
	}
	return offset
}

func javascriptFallbackJSXRegexEnd(source string, start int) (int, bool) {
	inClass := false
	for offset := start + 1; offset < len(source); {
		if javascriptLineTerminatorSize(source, offset) > 0 {
			return offset, false
		}
		if source[offset] == '\\' {
			offset++
			if offset >= len(source) {
				return len(source), false
			}
			_, size := utf8.DecodeRuneInString(source[offset:])
			if size < 1 {
				size = 1
			}
			offset += size
			continue
		}
		switch source[offset] {
		case '[':
			inClass = true
		case ']':
			inClass = false
		case '/':
			if !inClass {
				offset++
				for offset < len(source) && javascriptIdentifierContinueAt(source, offset) {
					_, size, ok := javascriptIdentifierRune(source, offset)
					if !ok || size < 1 {
						break
					}
					offset += size
				}
				return offset, true
			}
		}
		_, size := utf8.DecodeRuneInString(source[offset:])
		if size < 1 {
			size = 1
		}
		offset += size
	}
	return len(source), false
}
