package navigator

import (
	"strings"
	"unicode"
)

// csharpLexicalSourceAnalysis is the bounded, full-source structural view used
// when the concrete grammar is unavailable and as recovery input beside ERROR
// nodes. The scanner never retains the discarded middle of a large token
// stream; this analyzer likewise keeps only one capped declaration header per
// active structural frame.
type csharpLexicalSourceAnalysis struct {
	definitions []sourceDefinition
	scopes      []cLineScope
	imports     []cLineSpan
}

type csharpRecoveryContext uint8

const (
	csharpRecoveryCompilation csharpRecoveryContext = iota
	csharpRecoveryNamespace
	csharpRecoveryType
	csharpRecoveryEnum
	csharpRecoveryBlock
)

type csharpRecoveryFrame struct {
	typeName         string
	statement        []csharpToken
	ownerDefinitions []int
	startLine        int
	parenDepth       int
	bracketDepth     int
	inlineBraceDepth int
	lastTokenLine    int
	kind             csharpRecoveryContext
	poisoned         bool
}

type csharpRecoveryConditional struct {
	baseDepth       int
	branchStartLine int
}

type csharpRecoveryAnalyzer struct {
	source              string
	lineStarts          []int
	frames              []csharpRecoveryFrame
	conditionals        []csharpRecoveryConditional
	definitions         []sourceDefinition
	scopes              []cLineScope
	imports             []cLineSpan
	lineCount           int
	ignoreUntil         int
	overflow            int
	conditionalOverflow int
}

type csharpRecoveryOpen struct {
	typeName         string
	ownerDefinitions []int
	startLine        int
	kind             csharpRecoveryContext
}

// analyzeCSharpLexically walks every physical source byte. Its memory use is
// bounded by the structural/header/output caps even when lexCSharp retains a
// head/tail token gap for concrete-parser preflight.
func analyzeCSharpLexically(source string, lineCount int) csharpLexicalSourceAnalysis {
	lineStarts := csharpLineStarts(source)
	lineCount = max(lineCount, len(lineStarts))
	lineCount = max(lineCount, 1)
	analyzer := csharpRecoveryAnalyzer{
		source:     source,
		lineStarts: lineStarts,
		lineCount:  lineCount,
		frames: []csharpRecoveryFrame{{
			kind: csharpRecoveryCompilation,
		}},
	}
	analyzer.walkFullSource()
	analyzer.finish()
	return csharpLexicalSourceAnalysis{
		definitions: csharpSortUniqueDefinitions(analyzer.definitions, lineCount),
		scopes:      cNormalizeTreeLineScopes(analyzer.scopes, lineCount),
		imports:     cNormalizeTreeLineSpans(analyzer.imports, lineCount),
	}
}

func (analyzer *csharpRecoveryAnalyzer) walkFullSource() {
	base := 0
	for attempts := 0; base < len(analyzer.source) &&
		attempts <= csharpMaximumConcretePreprocessorDepth; attempts++ {
		resynchronize := -1
		segment := analyzer.source[base:]
		walkCSharpLexically(segment, csharpLexicalSink{
			literal: func(span cByteSpan) bool {
				absolute := cByteSpan{start: base + span.start, end: base + span.end}
				if absolute.end == len(analyzer.source) {
					resynchronize = csharpRecoveryLiteralResynchronization(
						analyzer.source, absolute.start,
					)
				}
				return true
			},
			token: func(token csharpToken) bool {
				token.start += base
				token.end += base
				analyzer.accept(token)
				return true
			},
		})
		if resynchronize <= base || resynchronize >= len(analyzer.source) {
			break
		}
		analyzer.resetCurrentStatement()
		base = resynchronize
	}
}

func (analyzer *csharpRecoveryAnalyzer) accept(token csharpToken) {
	if token.start < analyzer.ignoreUntil || len(analyzer.frames) == 0 {
		return
	}
	line := csharpTokenLine(analyzer.lineStarts, token.start)
	line = max(1, min(line, analyzer.lineCount))
	if token.text == "#" && token.lineStart {
		analyzer.handleDirective(token.start, line)
		return
	}
	if analyzer.resynchronizeStatement(token, line) {
		analyzer.resetCurrentStatement()
	}

	if analyzer.overflow > 0 {
		switch token.text {
		case "{":
			analyzer.overflow++
		case "}":
			analyzer.overflow--
		}
		return
	}

	frame := &analyzer.frames[len(analyzer.frames)-1]
	switch token.text {
	case "(":
		analyzer.appendToken(frame, token, line)
		frame.parenDepth = csharpRecoverySaturatingIncrement(frame.parenDepth)
		return
	case ")":
		if frame.parenDepth == 0 {
			analyzer.resetFrameStatement(frame)
			return
		}
		analyzer.appendToken(frame, token, line)
		frame.parenDepth--
		return
	case "[":
		analyzer.appendToken(frame, token, line)
		frame.bracketDepth = csharpRecoverySaturatingIncrement(frame.bracketDepth)
		return
	case "]":
		if frame.bracketDepth == 0 {
			analyzer.resetFrameStatement(frame)
			return
		}
		analyzer.appendToken(frame, token, line)
		frame.bracketDepth--
		return
	case "{":
		if frame.parenDepth > 0 || frame.bracketDepth > 0 || frame.inlineBraceDepth > 0 {
			analyzer.appendToken(frame, token, line)
			frame.inlineBraceDepth = csharpRecoverySaturatingIncrement(
				frame.inlineBraceDepth,
			)
			return
		}
		analyzer.openBrace(line)
		return
	case "}":
		if frame.inlineBraceDepth > 0 {
			analyzer.appendToken(frame, token, line)
			frame.inlineBraceDepth--
			return
		}
		analyzer.closeBrace(line)
		return
	case ";":
		if frame.parenDepth == 0 && frame.bracketDepth == 0 &&
			frame.inlineBraceDepth == 0 {
			analyzer.finishStatement(frame, token, line)
			return
		}
	case ",":
		if frame.kind == csharpRecoveryEnum && frame.parenDepth == 0 &&
			frame.bracketDepth == 0 && frame.inlineBraceDepth == 0 {
			analyzer.finishEnumMember(frame, line)
			return
		}
	}

	analyzer.appendToken(frame, token, line)
}

func (analyzer *csharpRecoveryAnalyzer) appendToken(
	frame *csharpRecoveryFrame,
	token csharpToken,
	line int,
) {
	if frame == nil {
		return
	}
	frame.lastTokenLine = line
	if frame.poisoned {
		return
	}
	if len(frame.statement) >= csharpMaximumHeaderTokens {
		frame.statement = nil
		frame.poisoned = true
		return
	}
	frame.statement = append(frame.statement, token)
}

func (analyzer *csharpRecoveryAnalyzer) openBrace(line int) {
	parentIndex := len(analyzer.frames) - 1
	parent := &analyzer.frames[parentIndex]
	open := analyzer.classifyOpen(parent, line)
	analyzer.resetFrameStatement(parent)
	if len(analyzer.frames) >= csharpMaximumStructuralDepth {
		analyzer.overflow = 1
		return
	}
	analyzer.frames = append(analyzer.frames, csharpRecoveryFrame{
		ownerDefinitions: open.ownerDefinitions,
		typeName:         open.typeName,
		startLine:        max(1, min(open.startLine, line)),
		kind:             open.kind,
	})
}

func (analyzer *csharpRecoveryAnalyzer) closeBrace(line int) {
	if len(analyzer.frames) <= 1 {
		analyzer.resetCurrentStatement()
		return
	}
	frame := &analyzer.frames[len(analyzer.frames)-1]
	if frame.kind == csharpRecoveryEnum {
		analyzer.finishEnumMember(frame, line)
	}
	analyzer.closeTopFrame(line)
	analyzer.resetCurrentStatement()
}

func (analyzer *csharpRecoveryAnalyzer) closeTopFrame(endLine int) {
	if len(analyzer.frames) <= 1 {
		return
	}
	last := len(analyzer.frames) - 1
	frame := analyzer.frames[last]
	endLine = max(frame.startLine, min(endLine, analyzer.lineCount))
	analyzer.addScope(frame.startLine, endLine)
	for _, definitionIndex := range frame.ownerDefinitions {
		if definitionIndex < 0 || definitionIndex >= len(analyzer.definitions) {
			continue
		}
		definition := &analyzer.definitions[definitionIndex]
		definition.scopeEnd = max(definition.line, endLine)
	}
	analyzer.frames = analyzer.frames[:last]
}

func (analyzer *csharpRecoveryAnalyzer) classifyOpen(
	frame *csharpRecoveryFrame,
	line int,
) csharpRecoveryOpen {
	startLine := line
	if frame != nil && len(frame.statement) > 0 {
		startLine = analyzer.tokenLine(frame.statement[0])
	}
	result := csharpRecoveryOpen{startLine: startLine, kind: csharpRecoveryBlock}
	if frame == nil || frame.poisoned {
		return result
	}
	tokens := frame.statement
	if csharpRecoveryTypeDeclarationContext(frame.kind) {
		if descriptor, ok := csharpRecoveryTypeDescriptor(tokens); ok {
			definition := analyzer.addDefinition(
				descriptor.symbol, descriptor.start, startLine, line, true,
			)
			result.ownerDefinitions = csharpRecoveryDefinitionIndex(definition)
			result.typeName = descriptor.nameToken.text
			if descriptor.kind == "enum" {
				result.kind = csharpRecoveryEnum
			} else {
				result.kind = csharpRecoveryType
			}
			if descriptor.record {
				analyzer.addRecordParameters(tokens, descriptor, line)
			}
			return result
		}
	}
	if descriptor, ok := csharpRecoveryNamespaceDescriptor(tokens); ok {
		definition := analyzer.addDefinition(
			descriptor.symbol, descriptor.start, startLine, line, true,
		)
		result.ownerDefinitions = csharpRecoveryDefinitionIndex(definition)
		result.kind = csharpRecoveryNamespace
		return result
	}
	if frame.kind == csharpRecoveryType && csharpRecoveryExtensionHeader(tokens) {
		result.kind = csharpRecoveryType
		return result
	}
	if frame.kind == csharpRecoveryType {
		if indices, handled := analyzer.addOperator(tokens, startLine, line, true); handled {
			result.ownerDefinitions = indices
			return result
		}
		if index, handled := analyzer.addDestructor(tokens, startLine, line, true); handled {
			result.ownerDefinitions = csharpRecoveryDefinitionIndex(index)
			return result
		}
		if index, handled := analyzer.addIndexer(tokens, startLine, line, true); handled {
			result.ownerDefinitions = csharpRecoveryDefinitionIndex(index)
			return result
		}
	}
	if frame.kind == csharpRecoveryType || frame.kind == csharpRecoveryBlock ||
		frame.kind == csharpRecoveryCompilation || frame.kind == csharpRecoveryNamespace {
		if index, handled := analyzer.addCallable(
			tokens, frame, startLine, line, true,
		); handled {
			result.ownerDefinitions = csharpRecoveryDefinitionIndex(index)
			return result
		}
	}
	if frame.kind == csharpRecoveryType {
		if csharpRecoveryTopLevelToken(tokens, "=") >= 0 {
			analyzer.addFields(tokens, startLine, line)
			return result
		}
		if index, handled := analyzer.addProperty(tokens, startLine, line, true); handled {
			result.ownerDefinitions = csharpRecoveryDefinitionIndex(index)
			return result
		}
	}
	return result
}

func (analyzer *csharpRecoveryAnalyzer) finishStatement(
	frame *csharpRecoveryFrame,
	terminator csharpToken,
	line int,
) {
	if frame == nil || frame.poisoned {
		analyzer.resetFrameStatement(frame)
		return
	}
	tokens := frame.statement
	startLine := line
	if len(tokens) > 0 {
		startLine = analyzer.tokenLine(tokens[0])
	}
	if frame.kind == csharpRecoveryCompilation || frame.kind == csharpRecoveryNamespace {
		if analyzer.addImport(tokens, startLine, line) {
			analyzer.resetFrameStatement(frame)
			return
		}
		if descriptor, ok := csharpRecoveryNamespaceDescriptor(tokens); ok {
			index := analyzer.addDefinition(
				descriptor.symbol, descriptor.start,
				startLine, analyzer.lineCount, true,
			)
			if index >= 0 {
				analyzer.addScope(startLine, analyzer.lineCount)
			}
			analyzer.resetFrameStatement(frame)
			return
		}
	}
	if csharpRecoveryTypeDeclarationContext(frame.kind) {
		if descriptor, ok := csharpRecoveryTypeDescriptor(tokens); ok {
			ownsScope := descriptor.record && descriptor.parameterOpen >= 0
			index := analyzer.addDefinition(
				descriptor.symbol, descriptor.start,
				startLine, line, ownsScope,
			)
			if ownsScope && index >= 0 {
				analyzer.addScope(startLine, line)
			}
			if descriptor.record {
				analyzer.addRecordParameters(tokens, descriptor, line)
			}
			analyzer.resetFrameStatement(frame)
			return
		}
	}
	if descriptor, ok := csharpRecoveryDelegateDescriptor(tokens); ok {
		analyzer.addDefinition(descriptor.symbol, descriptor.start, startLine, line, false)
		analyzer.resetFrameStatement(frame)
		return
	}
	if frame.kind == csharpRecoveryType {
		if _, handled := analyzer.addOperator(tokens, startLine, line, false); handled {
			analyzer.resetFrameStatement(frame)
			return
		}
		if _, handled := analyzer.addDestructor(tokens, startLine, line, false); handled {
			analyzer.resetFrameStatement(frame)
			return
		}
		if _, handled := analyzer.addIndexer(tokens, startLine, line, true); handled {
			analyzer.resetFrameStatement(frame)
			return
		}
	}
	if frame.kind == csharpRecoveryType || frame.kind == csharpRecoveryBlock ||
		frame.kind == csharpRecoveryCompilation || frame.kind == csharpRecoveryNamespace {
		if _, handled := analyzer.addCallable(tokens, frame, startLine, line, false); handled {
			analyzer.resetFrameStatement(frame)
			return
		}
	}
	if frame.kind == csharpRecoveryType {
		if csharpRecoveryTokenIndex(tokens, "=>") >= 0 {
			if _, handled := analyzer.addProperty(tokens, startLine, line, true); handled {
				analyzer.resetFrameStatement(frame)
				return
			}
		}
		analyzer.addFields(tokens, startLine, line)
	}
	_ = terminator
	analyzer.resetFrameStatement(frame)
}

func (analyzer *csharpRecoveryAnalyzer) finishEnumMember(
	frame *csharpRecoveryFrame,
	line int,
) {
	if frame == nil || frame.poisoned || len(frame.statement) == 0 {
		analyzer.resetFrameStatement(frame)
		return
	}
	if token, ok := csharpRecoveryDeclaratorName(frame.statement); ok {
		analyzer.addDefinition(token.text, token.start, analyzer.tokenLine(token), line, false)
	}
	analyzer.resetFrameStatement(frame)
}

func (analyzer *csharpRecoveryAnalyzer) finish() {
	if len(analyzer.frames) > 0 {
		root := &analyzer.frames[0]
		if !root.poisoned && len(root.statement) > 0 {
			analyzer.finishStatement(root, csharpToken{}, analyzer.lineCount)
		}
	}
	for len(analyzer.frames) > 1 {
		analyzer.closeTopFrame(analyzer.lineCount)
	}
	for len(analyzer.conditionals) > 0 {
		conditional := analyzer.conditionals[len(analyzer.conditionals)-1]
		analyzer.conditionals = analyzer.conditionals[:len(analyzer.conditionals)-1]
		analyzer.addScope(conditional.branchStartLine, analyzer.lineCount)
	}
}

func (analyzer *csharpRecoveryAnalyzer) handleDirective(offset, line int) {
	end := csharpLineEnd(analyzer.source, offset)
	analyzer.ignoreUntil = max(analyzer.ignoreUntil, end)
	analyzer.resetCurrentStatement()
	bodyStart := min(offset+1, end)
	raw := analyzer.source[bodyStart:end]
	leftTrimmed := strings.TrimLeftFunc(raw, unicode.IsSpace)
	textStart := bodyStart + len(raw) - len(leftTrimmed)
	text := strings.TrimSpace(raw)
	if strings.HasPrefix(text, ":") {
		name := csharpRecoveryDirectiveWord(strings.TrimSpace(strings.TrimPrefix(text, ":")))
		if name == "package" || name == "sdk" || name == "project" {
			analyzer.addImportSpan(line, line)
		}
		return
	}
	if strings.HasPrefix(text, "!") {
		return
	}
	name := csharpRecoveryDirectiveWord(text)
	switch name {
	case "define":
		remainderStart := min(textStart+len(name), end)
		remainder := analyzer.source[remainderStart:end]
		remainder = strings.TrimLeftFunc(remainder, unicode.IsSpace)
		start := end - len(remainder)
		identifierEnd := csharpIdentifierEnd(analyzer.source, start)
		if identifierEnd > start {
			analyzer.addDefinition(
				analyzer.source[start:identifierEnd], start, line, line, false,
			)
		}
	case "if":
		if len(analyzer.conditionals) >= csharpMaximumConcretePreprocessorDepth {
			analyzer.conditionalOverflow++
			return
		}
		analyzer.conditionals = append(analyzer.conditionals, csharpRecoveryConditional{
			baseDepth: len(analyzer.frames), branchStartLine: line,
		})
	case "elif", "else":
		if analyzer.conditionalOverflow > 0 || len(analyzer.conditionals) == 0 {
			return
		}
		conditional := &analyzer.conditionals[len(analyzer.conditionals)-1]
		analyzer.closeFramesTo(conditional.baseDepth, max(conditional.branchStartLine, line-1))
		analyzer.addScope(conditional.branchStartLine, max(conditional.branchStartLine, line-1))
		conditional.branchStartLine = line
	case "endif":
		if analyzer.conditionalOverflow > 0 {
			analyzer.conditionalOverflow--
			return
		}
		if len(analyzer.conditionals) == 0 {
			return
		}
		last := len(analyzer.conditionals) - 1
		conditional := analyzer.conditionals[last]
		analyzer.conditionals = analyzer.conditionals[:last]
		analyzer.closeFramesTo(conditional.baseDepth, max(conditional.branchStartLine, line-1))
		analyzer.addScope(conditional.branchStartLine, line)
	}
}

func (analyzer *csharpRecoveryAnalyzer) closeFramesTo(depth, line int) {
	depth = max(1, min(depth, len(analyzer.frames)))
	for len(analyzer.frames) > depth {
		analyzer.closeTopFrame(line)
	}
	analyzer.overflow = 0
	analyzer.resetCurrentStatement()
}

func (analyzer *csharpRecoveryAnalyzer) addImport(
	tokens []csharpToken,
	startLine, endLine int,
) bool {
	start := csharpRecoveryDeclarationPrefix(tokens)
	if start >= len(tokens) {
		return false
	}
	usingIndex := start
	if tokens[usingIndex].text == "global" && usingIndex+1 < len(tokens) &&
		tokens[usingIndex+1].text == "using" {
		usingIndex++
	}
	if tokens[usingIndex].text == "using" {
		alias, hasAlias, directive := csharpRecoveryUsingDirective(tokens, usingIndex)
		if !directive {
			return false
		}
		analyzer.addImportSpan(startLine, endLine)
		if hasAlias {
			analyzer.addDefinition(
				alias.text, alias.start, analyzer.tokenLine(alias), endLine, false,
			)
		}
		return true
	}
	if tokens[usingIndex].text == "extern" && usingIndex+2 < len(tokens) &&
		tokens[usingIndex+1].text == "alias" &&
		csharpRecoveryIdentifier(tokens[usingIndex+2]) {
		analyzer.addImportSpan(startLine, endLine)
		alias := tokens[usingIndex+2]
		analyzer.addDefinition(
			alias.text, alias.start, analyzer.tokenLine(alias), endLine, false,
		)
		return true
	}
	return false
}

func csharpRecoveryUsingDirective(
	tokens []csharpToken,
	usingIndex int,
) (alias csharpToken, hasAlias, directive bool) {
	if usingIndex < 0 || usingIndex >= len(tokens) || tokens[usingIndex].text != "using" {
		return csharpToken{}, false, false
	}
	rest := tokens[usingIndex+1:]
	if len(rest) == 0 {
		return csharpToken{}, false, false
	}
	rawEquals := csharpRecoveryTokenIndex(rest, "=")
	topLevelEquals := csharpRecoveryTopLevelToken(rest, "=")
	if rawEquals >= 0 {
		if rawEquals != topLevelEquals {
			return csharpToken{}, false, false
		}
		prefix := rest[:rawEquals]
		if len(prefix) > 0 && prefix[0].text == "unsafe" {
			prefix = prefix[1:]
		}
		if len(prefix) != 1 || !csharpRecoveryIdentifier(prefix[0]) {
			return csharpToken{}, false, false
		}
		target := rest[rawEquals+1:]
		if !csharpRecoveryPlausibleAliasType(target) {
			return csharpToken{}, false, false
		}
		if targetName, ok := csharpRecoveryLastIdentifier(target); ok &&
			csharpRecoveryCallAfterName(target, targetName) {
			return csharpToken{}, false, false
		}
		return prefix[0], true, true
	}
	for len(rest) > 0 && (rest[0].text == "static" || rest[0].text == "unsafe") {
		rest = rest[1:]
	}
	return csharpToken{}, false, csharpRecoveryUsingName(rest)
}

func csharpRecoveryPlausibleAliasType(tokens []csharpToken) bool {
	if csharpRecoveryPlausibleType(tokens, false) {
		return true
	}
	if !csharpRecoveryPlausibleType(tokens, true) {
		return false
	}
	functionPointer := len(tokens) > 2 && tokens[0].text == "delegate" &&
		tokens[1].text == "*" && csharpRecoveryTokenIndex(tokens[2:], "<") >= 0
	for index, token := range tokens {
		if token.text != "void" {
			continue
		}
		if functionPointer || index+1 < len(tokens) && tokens[index+1].text == "*" {
			continue
		}
		return false
	}
	return true
}

func csharpRecoveryUsingName(tokens []csharpToken) bool {
	if !csharpRecoveryPlausibleType(tokens, false) {
		return false
	}
	previousWord := false
	for _, token := range tokens {
		word := csharpRecoveryIdentifier(token) ||
			csharpRecoveryTypeKeyword(token.text, false)
		if word && previousWord {
			return false
		}
		previousWord = word
		switch token.text {
		case "(", ")", "[", "]", "?", "*":
			return false
		}
	}
	return true
}

func (analyzer *csharpRecoveryAnalyzer) addRecordParameters(
	tokens []csharpToken,
	descriptor csharpRecoveryTypeDeclaration,
	endLine int,
) {
	if descriptor.parameterOpen < 0 || descriptor.parameterClose <= descriptor.parameterOpen {
		return
	}
	parameters := tokens[descriptor.parameterOpen+1 : descriptor.parameterClose]
	for _, segment := range csharpRecoverySplitTopLevel(parameters, ",") {
		beforeDefault := segment
		if equals := csharpRecoveryTopLevelToken(segment, "="); equals >= 0 {
			beforeDefault = segment[:equals]
		}
		if token, ok := csharpRecoveryLastIdentifier(beforeDefault); ok {
			analyzer.addDefinition(
				token.text, token.start, analyzer.tokenLine(token), endLine, false,
			)
		}
	}
}

func (analyzer *csharpRecoveryAnalyzer) addFields(
	tokens []csharpToken,
	startLine, endLine int,
) {
	start := csharpRecoveryDeclarationPrefix(tokens)
	if start >= len(tokens) || csharpRecoveryFieldRejected(tokens[start:]) {
		return
	}
	fieldTokens := tokens[start:]
	if fieldTokens[0].text == "event" {
		fieldTokens = fieldTokens[1:]
	}
	segments := csharpRecoverySplitTopLevel(fieldTokens, ",")
	for segmentIndex, segment := range segments {
		beforeInitializer := segment
		if equals := csharpRecoveryTopLevelToken(segment, "="); equals >= 0 {
			beforeInitializer = segment[:equals]
		}
		token, ok := csharpRecoveryLastIdentifier(beforeInitializer)
		if !ok || segmentIndex == 0 && !csharpRecoveryFieldHasType(beforeInitializer, token) {
			continue
		}
		analyzer.addDefinition(
			token.text, token.start, analyzer.tokenLine(token), endLine, false,
		)
	}
	_ = startLine
}

func (analyzer *csharpRecoveryAnalyzer) addCallable(
	tokens []csharpToken,
	frame *csharpRecoveryFrame,
	startLine, endLine int,
	ownsScope bool,
) (int, bool) {
	descriptor, ok := csharpRecoveryCallableDescriptor(tokens, frame)
	if !ok {
		return -1, false
	}
	return analyzer.addDefinition(
		descriptor.name.text, descriptor.name.start,
		startLine, endLine, ownsScope,
	), true
}

func (analyzer *csharpRecoveryAnalyzer) addProperty(
	tokens []csharpToken,
	startLine, endLine int,
	ownsScope bool,
) (int, bool) {
	start := csharpRecoveryDeclarationPrefix(tokens)
	if start >= len(tokens) || csharpRecoveryControlHeader(tokens[start:]) {
		return -1, false
	}
	declarationEnd := len(tokens)
	relative := tokens[start:]
	rawArrow := csharpRecoveryTokenIndex(relative, "=>")
	topLevelArrow := csharpRecoveryTopLevelToken(relative, "=>")
	rawEquals := csharpRecoveryTokenIndex(relative, "=")
	if rawArrow >= 0 {
		if rawArrow != topLevelArrow || rawEquals >= 0 && rawEquals < rawArrow {
			return -1, false
		}
		declarationEnd = start + rawArrow
	} else if rawEquals >= 0 {
		return -1, false
	}
	declaration := tokens[start:declarationEnd]
	if len(declaration) == 0 {
		return -1, false
	}
	if declaration[0].text == "event" {
		declaration = declaration[1:]
	}
	if thisToken, ok := csharpRecoveryTokenNamed(declaration, "this"); ok {
		return analyzer.addDefinition(
			"this", thisToken.start, startLine, endLine, ownsScope,
		), true
	}
	name, ok := csharpRecoveryLastIdentifier(declaration)
	if !ok || !csharpRecoveryFieldHasType(declaration, name) ||
		csharpRecoveryCallAfterName(declaration, name) {
		return -1, false
	}
	return analyzer.addDefinition(
		name.text, name.start, startLine, endLine, ownsScope,
	), true
}

func (analyzer *csharpRecoveryAnalyzer) addIndexer(
	tokens []csharpToken,
	startLine, endLine int,
	ownsScope bool,
) (int, bool) {
	start := csharpRecoveryDeclarationPrefix(tokens)
	if start >= len(tokens) {
		return -1, false
	}
	thisToken, ok := csharpRecoveryTokenNamed(tokens[start:], "this")
	if !ok || csharpRecoveryTopLevelToken(tokens[start:], "[") < 0 {
		return -1, false
	}
	return analyzer.addDefinition(
		"this", thisToken.start, startLine, endLine, ownsScope,
	), true
}

func (analyzer *csharpRecoveryAnalyzer) addDestructor(
	tokens []csharpToken,
	startLine, endLine int,
	ownsScope bool,
) (int, bool) {
	for index := 0; index+2 < len(tokens); index++ {
		if tokens[index].text != "~" || !csharpRecoveryIdentifier(tokens[index+1]) ||
			tokens[index+2].text != "(" {
			continue
		}
		return analyzer.addDefinition(
			"~"+tokens[index+1].text, tokens[index].start,
			startLine, endLine, ownsScope,
		), true
	}
	return -1, false
}

func (analyzer *csharpRecoveryAnalyzer) addOperator(
	tokens []csharpToken,
	startLine, endLine int,
	ownsScope bool,
) ([]int, bool) {
	operatorIndex := csharpRecoveryTokenIndex(tokens, "operator")
	if operatorIndex < 0 {
		return nil, false
	}
	parameterOpen := csharpRecoveryNextToken(tokens, operatorIndex+1, "(")
	if parameterOpen < 0 {
		return nil, false
	}
	prefix := ""
	start := tokens[operatorIndex].start
	for index := range operatorIndex {
		if tokens[index].text == "implicit" || tokens[index].text == "explicit" {
			prefix = tokens[index].text
			start = tokens[index].start
		}
	}
	part := operatorIndex + 1
	checked := false
	if part < parameterOpen && tokens[part].text == "checked" {
		checked = true
		part++
	}
	if part >= parameterOpen {
		return nil, false
	}
	var symbol string
	if prefix != "" {
		typeText := csharpRecoveryCompactTokens(tokens[part:parameterOpen])
		if typeText == "" {
			return nil, false
		}
		symbol = prefix + " operator "
		if checked {
			symbol += "checked "
		}
		symbol += typeText
	} else {
		operatorText := tokens[part].text
		if !csharpOperatorPunctuation(operatorText) {
			return nil, false
		}
		symbol = "operator"
		if checked {
			symbol += " checked"
		}
		symbol += operatorText
	}
	index := analyzer.addDefinition(symbol, start, startLine, endLine, ownsScope)
	return csharpRecoveryDefinitionIndex(index), true
}

func (analyzer *csharpRecoveryAnalyzer) addDefinition(
	symbol string,
	start, scopeStart, scopeEnd int,
	ownsScope bool,
) int {
	if symbol == "" || start < 0 || start >= len(analyzer.source) ||
		len(analyzer.definitions) >= csharpMaximumRetainedTokens {
		return -1
	}
	line, column := csharpLineColumn(analyzer.source, analyzer.lineStarts, start)
	if line < 1 || line > analyzer.lineCount || column < 1 {
		return -1
	}
	scopeStart = max(1, min(scopeStart, line))
	scopeEnd = max(line, min(scopeEnd, analyzer.lineCount))
	analyzer.definitions = append(analyzer.definitions, sourceDefinition{
		symbol: symbol, line: line, column: column,
		scopeStart: scopeStart, scopeEnd: scopeEnd, ownsScope: ownsScope,
	})
	return len(analyzer.definitions) - 1
}

func (analyzer *csharpRecoveryAnalyzer) addScope(start, end int) {
	if len(analyzer.scopes) >= csharpMaximumRetainedTokens {
		return
	}
	start = max(1, min(start, analyzer.lineCount))
	end = max(start, min(end, analyzer.lineCount))
	analyzer.scopes = append(analyzer.scopes, cLineScope{start: start, end: end})
}

func (analyzer *csharpRecoveryAnalyzer) addImportSpan(start, end int) {
	if len(analyzer.imports) >= csharpMaximumRetainedTokens {
		return
	}
	start = max(1, min(start, analyzer.lineCount))
	end = max(start, min(end, analyzer.lineCount))
	analyzer.imports = append(analyzer.imports, cLineSpan{start: start, end: end})
}

func (analyzer *csharpRecoveryAnalyzer) tokenLine(token csharpToken) int {
	return max(1, min(csharpTokenLine(analyzer.lineStarts, token.start), analyzer.lineCount))
}

func (analyzer *csharpRecoveryAnalyzer) resetCurrentStatement() {
	if len(analyzer.frames) > 0 {
		analyzer.resetFrameStatement(&analyzer.frames[len(analyzer.frames)-1])
	}
}

func (analyzer *csharpRecoveryAnalyzer) resetFrameStatement(frame *csharpRecoveryFrame) {
	if frame == nil {
		return
	}
	frame.statement = frame.statement[:0]
	frame.parenDepth = 0
	frame.bracketDepth = 0
	frame.inlineBraceDepth = 0
	frame.lastTokenLine = 0
	frame.poisoned = false
}

func (analyzer *csharpRecoveryAnalyzer) resynchronizeStatement(
	token csharpToken,
	line int,
) bool {
	if !token.lineStart || len(analyzer.frames) == 0 {
		return false
	}
	frame := &analyzer.frames[len(analyzer.frames)-1]
	if len(frame.statement) == 0 && !frame.poisoned || line <= frame.lastTokenLine {
		return false
	}
	if !csharpRecoveryStrongDeclarationStart(token.text) {
		return false
	}
	if csharpRecoveryOnlyAttributes(frame.statement) {
		return false
	}
	return frame.poisoned || frame.parenDepth > 0 || frame.bracketDepth > 0 ||
		frame.inlineBraceDepth > 0 || len(frame.statement) > 0
}

func csharpRecoverySaturatingIncrement(value int) int {
	if value >= csharpMaximumStructuralDepth {
		return csharpMaximumStructuralDepth
	}
	return value + 1
}

func csharpRecoveryDefinitionIndex(index int) []int {
	if index < 0 {
		return nil
	}
	return []int{index}
}

func csharpRecoveryDirectiveWord(text string) string {
	end := 0
	for end < len(text) && text[end] >= 'a' && text[end] <= 'z' {
		end++
	}
	return text[:end]
}

func csharpRecoveryLiteralResynchronization(source string, literalStart int) int {
	lineEnd := csharpLineEnd(source, literalStart)
	for offset := lineEnd; offset < len(source); {
		for offset < len(source) && (source[offset] == '\r' || source[offset] == '\n') {
			offset++
		}
		lineStart := offset
		for offset < len(source) && (source[offset] == ' ' || source[offset] == '\t') {
			offset++
		}
		identifierEnd := csharpIdentifierEnd(source, offset)
		if identifierEnd > offset &&
			csharpRecoveryStrongDeclarationStart(source[offset:identifierEnd]) {
			return offset
		}
		offset = csharpLineEnd(source, lineStart)
		if offset == lineStart {
			offset++
		}
	}
	return -1
}

func csharpRecoveryStrongDeclarationStart(value string) bool {
	switch value {
	case "namespace", "class", "struct", "interface", "enum", "record", "delegate",
		"public", "private", "protected", "internal", "file", "static", "abstract",
		"sealed", "partial", "readonly", "ref", "unsafe", "extern", "event", "void":
		return true
	default:
		return false
	}
}

func csharpRecoveryOnlyAttributes(tokens []csharpToken) bool {
	if len(tokens) == 0 || tokens[0].text != "[" || tokens[len(tokens)-1].text != "]" {
		return false
	}
	depth := 0
	for _, token := range tokens {
		switch token.text {
		case "[":
			depth++
		case "]":
			depth--
			if depth < 0 {
				return false
			}
		}
	}
	return depth == 0
}

type csharpRecoveryTypeDeclaration struct {
	symbol         string
	kind           string
	nameToken      csharpToken
	start          int
	parameterOpen  int
	parameterClose int
	record         bool
}

type csharpRecoveryNamedDeclaration struct {
	symbol string
	start  int
}

type csharpRecoveryCallable struct {
	name csharpToken
}

type csharpRecoveryDepth struct {
	paren, bracket, brace, angle int
}

func csharpRecoveryTypeDescriptor(tokens []csharpToken) (csharpRecoveryTypeDeclaration, bool) {
	start := csharpRecoveryDeclarationPrefix(tokens)
	if start >= len(tokens) || csharpRecoveryTopLevelToken(tokens[start:], "=") >= 0 {
		return csharpRecoveryTypeDeclaration{}, false
	}
	kind := tokens[start].text
	record := kind == "record"
	if record && start+1 < len(tokens) &&
		(tokens[start+1].text == "class" || tokens[start+1].text == "struct") {
		start++
		kind = tokens[start].text
	}
	if kind != "class" && kind != "struct" && kind != "interface" &&
		kind != "enum" && !record {
		return csharpRecoveryTypeDeclaration{}, false
	}
	nameIndex := start + 1
	if record && tokens[start].text == "record" {
		nameIndex = start + 1
	}
	if nameIndex >= len(tokens) || !csharpRecoveryIdentifier(tokens[nameIndex]) {
		return csharpRecoveryTypeDeclaration{}, false
	}
	name := tokens[nameIndex]
	result := csharpRecoveryTypeDeclaration{
		symbol: name.text, kind: kind, nameToken: name, start: name.start,
		parameterOpen: -1, parameterClose: -1, record: record,
	}
	for _, pair := range csharpRecoveryTopLevelParenPairs(tokens) {
		if pair[0] > nameIndex {
			result.parameterOpen, result.parameterClose = pair[0], pair[1]
			break
		}
	}
	return result, true
}

func csharpRecoveryTypeDeclarationContext(kind csharpRecoveryContext) bool {
	return kind == csharpRecoveryCompilation || kind == csharpRecoveryNamespace ||
		kind == csharpRecoveryType
}

func csharpRecoveryNamespaceDescriptor(tokens []csharpToken) (csharpRecoveryNamedDeclaration, bool) {
	start := csharpRecoveryDeclarationPrefix(tokens)
	if start >= len(tokens) || tokens[start].text != "namespace" {
		return csharpRecoveryNamedDeclaration{}, false
	}
	parts := make([]string, 0, min(len(tokens)-start-1, csharpMaximumHeaderTokens))
	nameStart := -1
	for _, token := range tokens[start+1:] {
		if csharpRecoveryIdentifier(token) || token.text == "." || token.text == "::" {
			if nameStart < 0 {
				nameStart = token.start
			}
			parts = append(parts, token.text)
			continue
		}
		break
	}
	if nameStart < 0 || len(parts) == 0 {
		return csharpRecoveryNamedDeclaration{}, false
	}
	symbol := strings.Join(parts, "")
	if !csharpQualifiedSourceName(symbol) {
		return csharpRecoveryNamedDeclaration{}, false
	}
	return csharpRecoveryNamedDeclaration{symbol: symbol, start: nameStart}, true
}

func csharpRecoveryDelegateDescriptor(tokens []csharpToken) (csharpRecoveryNamedDeclaration, bool) {
	start := csharpRecoveryDeclarationPrefix(tokens)
	if start >= len(tokens) || tokens[start].text != "delegate" {
		return csharpRecoveryNamedDeclaration{}, false
	}
	for _, pair := range csharpRecoveryTopLevelParenPairs(tokens) {
		name, _, ok := csharpRecoveryNameBeforeParameter(tokens, pair[0])
		if ok {
			return csharpRecoveryNamedDeclaration{symbol: name.text, start: name.start}, true
		}
	}
	return csharpRecoveryNamedDeclaration{}, false
}

func csharpRecoveryCallableDescriptor(
	tokens []csharpToken,
	frame *csharpRecoveryFrame,
) (csharpRecoveryCallable, bool) {
	start := csharpRecoveryDeclarationPrefix(tokens)
	if start >= len(tokens) || csharpRecoveryControlHeader(tokens[start:]) ||
		csharpRecoveryTopLevelToken(tokens[:start], "=") >= 0 {
		return csharpRecoveryCallable{}, false
	}
	headerEnd := len(tokens)
	for _, terminator := range []string{"=>", "="} {
		if index := csharpRecoveryTopLevelToken(tokens[:headerEnd], terminator); index >= 0 {
			headerEnd = index
		}
	}
	for _, pair := range csharpRecoveryTopLevelParenPairs(tokens[:headerEnd]) {
		name, nameIndex, ok := csharpRecoveryNameBeforeParameter(tokens, pair[0])
		if !ok || name.text == "this" || name.text == "base" {
			continue
		}
		if csharpRecoveryTopLevelToken(tokens[:pair[0]], "=") >= 0 {
			continue
		}
		candidatePrefix := tokens[start:pair[0]]
		if csharpRecoveryTokenIndex(candidatePrefix, "=>") >= 0 ||
			csharpRecoveryTokenIndex(candidatePrefix, "=") >= 0 {
			continue
		}
		constructor := frame != nil && frame.kind == csharpRecoveryType &&
			frame.typeName != "" && name.text == frame.typeName
		if nameIndex <= start && !constructor {
			continue
		}
		if nameIndex > 0 && tokens[nameIndex-1].text == "." &&
			!csharpRecoveryExplicitInterfaceCallable(tokens, start, nameIndex) {
			continue
		}
		return csharpRecoveryCallable{name: name}, true
	}
	return csharpRecoveryCallable{}, false
}

func csharpRecoveryExplicitInterfaceCallable(
	tokens []csharpToken,
	start, nameIndex int,
) bool {
	if start < 0 || nameIndex <= start+1 || nameIndex > len(tokens) ||
		tokens[nameIndex-1].text != "." {
		return false
	}
	qualifierEnd := nameIndex - 1
	for split := start + 1; split < qualifierEnd; split++ {
		// A return type and the implemented interface are separate grammar
		// operands. Qualified invocations have punctuation, rather than a
		// lexical gap, at every possible split.
		if tokens[split-1].end >= tokens[split].start {
			continue
		}
		if csharpRecoveryPlausibleType(tokens[start:split], true) &&
			csharpRecoveryPlausibleType(tokens[split:qualifierEnd], false) {
			return true
		}
	}
	return false
}

func csharpRecoveryPlausibleType(tokens []csharpToken, allowVoid bool) bool {
	if len(tokens) == 0 {
		return false
	}
	depth := csharpRecoveryDepth{}
	words := 0
	for _, token := range tokens {
		if token.text == "await" || token.text == "var" {
			return false
		}
		if csharpRecoveryIdentifier(token) || csharpRecoveryTypeKeyword(token.text, allowVoid) {
			words++
			continue
		}
		switch token.text {
		case ".", "::", ",", "?", "*":
		case "<":
			depth.angle++
		case ">":
			if depth.angle < 1 {
				return false
			}
			depth.angle--
		case ">>":
			if depth.angle < 2 {
				return false
			}
			depth.angle -= 2
		case ">>>":
			if depth.angle < 3 {
				return false
			}
			depth.angle -= 3
		case "[":
			depth.bracket++
		case "]":
			if depth.bracket < 1 {
				return false
			}
			depth.bracket--
		case "(":
			depth.paren++
		case ")":
			if depth.paren < 1 {
				return false
			}
			depth.paren--
		default:
			return false
		}
	}
	if depth.paren != 0 || depth.bracket != 0 || depth.angle != 0 || words == 0 {
		return false
	}
	switch tokens[0].text {
	case ".", "::", ",", "?", "*", ">", ">>", ">>>", "]":
		return false
	}
	switch tokens[len(tokens)-1].text {
	case ".", "::", ",", "<", "[", "(":
		return false
	}
	return true
}

func csharpRecoveryTypeKeyword(value string, allowVoid bool) bool {
	if value == "void" {
		return allowVoid
	}
	switch value {
	case "bool", "byte", "char", "decimal", "double", "float", "int", "long",
		"object", "sbyte", "short", "string", "uint", "ulong", "ushort", "delegate",
		"ref", "readonly":
		return true
	default:
		return false
	}
}

func csharpRecoveryCallAfterName(tokens []csharpToken, name csharpToken) bool {
	nameIndex := -1
	for index, token := range tokens {
		if token.start == name.start && token.end == name.end {
			nameIndex = index
			break
		}
	}
	if nameIndex < 0 {
		return false
	}
	for _, pair := range csharpRecoveryTopLevelParenPairs(tokens) {
		if pair[0] > nameIndex {
			return true
		}
	}
	return false
}

func csharpRecoveryNameBeforeParameter(
	tokens []csharpToken,
	open int,
) (csharpToken, int, bool) {
	if open <= 0 || open > len(tokens) {
		return csharpToken{}, -1, false
	}
	index := open - 1
	if tokens[index].text == ">" || tokens[index].text == ">>" ||
		tokens[index].text == ">>>" {
		depth := 0
		for ; index >= 0; index-- {
			switch tokens[index].text {
			case ">":
				depth++
			case ">>":
				depth += 2
			case ">>>":
				depth += 3
			case "<":
				depth--
				if depth <= 0 {
					index--
					goto found
				}
			}
		}
	}
found:
	if index < 0 || !csharpRecoveryIdentifier(tokens[index]) {
		return csharpToken{}, -1, false
	}
	return tokens[index], index, true
}

func csharpRecoveryExtensionHeader(tokens []csharpToken) bool {
	start := csharpRecoveryDeclarationPrefix(tokens)
	if start+1 >= len(tokens) || tokens[start].text != "extension" {
		return false
	}
	if tokens[start+1].text == "(" {
		return true
	}
	if tokens[start+1].text != "<" {
		return false
	}
	angleDepth := 0
	for index := start + 1; index < len(tokens); index++ {
		switch tokens[index].text {
		case "<":
			angleDepth++
		case ">":
			angleDepth--
		case ">>":
			angleDepth -= 2
		case ">>>":
			angleDepth -= 3
		}
		if angleDepth < 0 {
			return false
		}
		if angleDepth == 0 {
			return index+1 < len(tokens) && tokens[index+1].text == "("
		}
	}
	return false
}

func csharpRecoveryDeclarationPrefix(tokens []csharpToken) int {
	index := 0
	for index < len(tokens) && tokens[index].text == "[" {
		depth := 0
		for index < len(tokens) {
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
	}
	for index < len(tokens) && csharpRecoveryModifier(tokens[index].text) {
		index++
	}
	return index
}

func csharpRecoveryModifier(value string) bool {
	switch value {
	case "public", "private", "protected", "internal", "file", "static", "abstract",
		"sealed", "partial", "readonly", "ref", "unsafe", "extern", "async", "new",
		"virtual", "override", "volatile", "const", "fixed", "required", "scoped":
		return true
	default:
		return false
	}
}

func csharpRecoveryControlHeader(tokens []csharpToken) bool {
	if len(tokens) == 0 {
		return false
	}
	switch tokens[0].text {
	case "if", "for", "foreach", "while", "do", "switch", "lock", "using",
		"try", "catch", "finally", "else", "fixed", "checked", "unchecked",
		"return", "throw", "yield", "new", "await":
		return true
	default:
		return false
	}
}

func csharpRecoveryFieldRejected(tokens []csharpToken) bool {
	if len(tokens) == 0 || csharpRecoveryControlHeader(tokens) {
		return true
	}
	for _, rejected := range []string{"namespace", "class", "struct", "interface", "enum", "record", "delegate"} {
		if tokens[0].text == rejected {
			return true
		}
	}
	rawEquals := csharpRecoveryTokenIndex(tokens, "=")
	topLevelEquals := csharpRecoveryTopLevelToken(tokens, "=")
	if rawEquals >= 0 && topLevelEquals < 0 {
		return true
	}
	header := tokens
	if topLevelEquals >= 0 {
		header = tokens[:topLevelEquals]
	}
	if csharpRecoveryTokenIndex(header, "=>") >= 0 {
		return true
	}
	name, ok := csharpRecoveryLastIdentifier(header)
	return !ok || !csharpRecoveryFieldHasType(header, name) ||
		csharpRecoveryCallAfterName(header, name)
}

func csharpRecoveryFieldHasType(tokens []csharpToken, name csharpToken) bool {
	for index, token := range tokens {
		if token.start == name.start && token.end == name.end {
			return index > 0
		}
	}
	return false
}

func csharpRecoveryDeclaratorName(tokens []csharpToken) (csharpToken, bool) {
	if equals := csharpRecoveryTopLevelToken(tokens, "="); equals >= 0 {
		tokens = tokens[:equals]
	}
	return csharpRecoveryLastIdentifier(tokens)
}

func csharpRecoveryLastIdentifier(tokens []csharpToken) (csharpToken, bool) {
	for index := len(tokens) - 1; index >= 0; index-- {
		if csharpRecoveryIdentifier(tokens[index]) {
			return tokens[index], true
		}
	}
	return csharpToken{}, false
}

func csharpRecoveryIdentifier(token csharpToken) bool {
	return token.kind == csharpTokenIdentifier && token.text != "" &&
		csharpSourceIdentifier(token.text) && !csharpRecoveryReservedIdentifier(token)
}

func csharpRecoveryReservedIdentifier(token csharpToken) bool {
	if token.text == "" || token.text[0] == '@' || strings.Contains(token.text, "\\") {
		return false
	}
	switch token.text {
	case "abstract", "as", "base", "bool", "break", "byte", "case", "catch", "char",
		"checked", "class", "const", "continue", "decimal", "default", "delegate", "do",
		"double", "else", "enum", "event", "explicit", "extern", "false", "finally",
		"fixed", "float", "for", "foreach", "goto", "if", "implicit", "in", "int",
		"interface", "internal", "is", "lock", "long", "namespace", "new", "null",
		"object", "operator", "out", "override", "params", "private", "protected",
		"public", "readonly", "ref", "return", "sbyte", "sealed", "short", "sizeof",
		"stackalloc", "static", "string", "struct", "switch", "this", "throw", "true",
		"try", "typeof", "uint", "ulong", "unchecked", "unsafe", "ushort", "using",
		"virtual", "void", "volatile", "while":
		return true
	default:
		return false
	}
}

func csharpRecoveryTokenNamed(tokens []csharpToken, name string) (csharpToken, bool) {
	for _, token := range tokens {
		if token.text == name {
			return token, true
		}
	}
	return csharpToken{}, false
}

func csharpRecoveryTokenIndex(tokens []csharpToken, name string) int {
	for index, token := range tokens {
		if token.text == name {
			return index
		}
	}
	return -1
}

func csharpRecoveryNextToken(tokens []csharpToken, start int, name string) int {
	for index := max(0, start); index < len(tokens); index++ {
		if tokens[index].text == name {
			return index
		}
	}
	return -1
}

func csharpRecoveryTopLevelToken(tokens []csharpToken, name string) int {
	depth := csharpRecoveryDepth{}
	for index, token := range tokens {
		if csharpRecoveryDepthZero(depth) && token.text == name {
			return index
		}
		csharpRecoveryAdvanceDepth(&depth, token.text)
	}
	return -1
}

func csharpRecoverySplitTopLevel(tokens []csharpToken, separator string) [][]csharpToken {
	result := make([][]csharpToken, 0, 4)
	start := 0
	depth := csharpRecoveryDepth{}
	for index, token := range tokens {
		if csharpRecoveryDepthZero(depth) && token.text == separator {
			result = append(result, tokens[start:index])
			start = index + 1
			continue
		}
		csharpRecoveryAdvanceDepth(&depth, token.text)
	}
	return append(result, tokens[start:])
}

func csharpRecoveryTopLevelParenPairs(tokens []csharpToken) [][2]int {
	pairs := make([][2]int, 0, 2)
	stack := make([]int, 0, 8)
	bracket := 0
	for index, token := range tokens {
		switch token.text {
		case "[":
			bracket++
		case "]":
			bracket = max(0, bracket-1)
		case "(":
			if bracket == 0 {
				stack = append(stack, index)
			}
		case ")":
			if bracket == 0 && len(stack) > 0 {
				open := stack[len(stack)-1]
				stack = stack[:len(stack)-1]
				if len(stack) == 0 {
					pairs = append(pairs, [2]int{open, index})
				}
			}
		}
	}
	return pairs
}

func csharpRecoveryDepthZero(depth csharpRecoveryDepth) bool {
	return depth.paren == 0 && depth.bracket == 0 && depth.brace == 0 && depth.angle == 0
}

func csharpRecoveryAdvanceDepth(depth *csharpRecoveryDepth, value string) {
	if depth == nil {
		return
	}
	switch value {
	case "(":
		depth.paren++
	case ")":
		depth.paren = max(0, depth.paren-1)
	case "[":
		depth.bracket++
	case "]":
		depth.bracket = max(0, depth.bracket-1)
	case "{":
		depth.brace++
	case "}":
		depth.brace = max(0, depth.brace-1)
	case "<":
		depth.angle++
	case ">":
		depth.angle = max(0, depth.angle-1)
	case ">>":
		depth.angle = max(0, depth.angle-2)
	case ">>>":
		depth.angle = max(0, depth.angle-3)
	}
}

func csharpRecoveryCompactTokens(tokens []csharpToken) string {
	var result strings.Builder
	for _, token := range tokens {
		result.WriteString(token.text)
	}
	return result.String()
}
