package repoview

import "sort"

type modulaLexicalAnalysis struct {
	definitions []sourceDefinition
	scopes      []cLineScope
	imports     []cLineSpan
}

type modulaRecoverySection uint8

const (
	modulaRecoveryNoSection modulaRecoverySection = iota
	modulaRecoveryConstSection
	modulaRecoveryTypeSection
	modulaRecoveryVarSection
)

type modulaRecoveryFrame struct {
	name             string
	start            int
	bodyStart        int
	definitionIndex  int
	section          modulaRecoverySection
	definitionUnit   bool
	declarationPhase uint8
	body             bool
	procedure        bool
	invalid          bool
}

type modulaRecoveryControl struct {
	start, frameDepth int
	repeat            bool
}

type modulaRecoveryOwnerKind uint8

const (
	modulaRecoveryNoOwner modulaRecoveryOwnerKind = iota
	modulaRecoveryProcedureOwner
	modulaRecoveryModuleOwner
)

const (
	modulaRecoveryModuleSuffixAfterName uint8 = iota
	modulaRecoveryModuleSuffixPriority
	modulaRecoveryModuleSuffixDone
)

const (
	modulaRecoveryProcedureOverflowExpectOpen uint8 = iota
	modulaRecoveryProcedureOverflowParameters
	modulaRecoveryProcedureOverflowSuffix
)

const (
	modulaRecoveryImportOverflowStart uint8 = iota
	modulaRecoveryImportOverflowSource
	modulaRecoveryImportOverflowKeyword
	modulaRecoveryImportOverflowName
	modulaRecoveryImportOverflowComma
)

const (
	modulaRecoveryRawOwnerNone uint8 = iota
	modulaRecoveryRawOwnerProcedureName
	modulaRecoveryRawOwnerModuleKeyword
	modulaRecoveryRawOwnerDefinitionForOrName
	modulaRecoveryRawOwnerDefinitionLiteral
	modulaRecoveryRawOwnerModuleName
)

const (
	modulaRecoveryFormalStreamStart uint8 = iota
	modulaRecoveryFormalStreamNeedName
	modulaRecoveryFormalStreamAfterName
	modulaRecoveryFormalStreamTypeStart
	modulaRecoveryFormalStreamArrayOF
	modulaRecoveryFormalStreamAfterTypeName
	modulaRecoveryFormalStreamQualifiedName
	modulaRecoveryFormalStreamDefaultStart
	modulaRecoveryFormalStreamDefault
	modulaRecoveryFormalStreamOptionalDone
	modulaRecoveryFormalStreamExtendedDone
)

const (
	modulaRecoveryReturnStreamStart uint8 = iota
	modulaRecoveryReturnStreamTypeStart
	modulaRecoveryReturnStreamAfterTypeName
	modulaRecoveryReturnStreamQualifiedName
	modulaRecoveryReturnStreamOptionalDone
)

type modulaRecoveryFormalStream struct {
	defaultExpression modulaRecoveryExpressionStream
	defaultDepth      int
	state             uint8
	optional          bool
	extended          bool
	defaultSeen       bool
	valid             bool
}

type modulaRecoveryExpressionDelimiter struct {
	text             string
	content          bool
	allowEmpty       bool
	general          bool
	resultDesignator bool
	relation         bool
	rangeSeen        bool
	bySeen           bool
}

type modulaRecoveryExpressionStream struct {
	delimiters         []modulaRecoveryExpressionDelimiter
	expectOperand      bool
	allowSign          bool
	selector           bool
	designator         bool
	constructorAllowed bool
	rootRelation       bool
	attributeState     uint8
	sawOperand         bool
	valid              bool
}

const (
	modulaRecoveryAttributeNone uint8 = iota
	modulaRecoveryAttributeBuiltin
	modulaRecoveryAttributeOpenOuter
	modulaRecoveryAttributeOpenInner
	modulaRecoveryAttributeExpression
	modulaRecoveryAttributeSimpleClose
	modulaRecoveryAttributeAngleQualident
	modulaRecoveryAttributeAngleAfterQualident
	modulaRecoveryAttributeAngleFinalIdent
	modulaRecoveryAttributeAngleClose
	modulaRecoveryAttributeCloseInner
	modulaRecoveryAttributeCloseOuter
)

type modulaRecoveryReturnStream struct {
	state    uint8
	optional bool
	valid    bool
}

type modulaRecoveryParser struct {
	pairedDirectiveTokens          []modulaToken
	header                         []modulaToken
	frames                         []modulaRecoveryFrame
	controls                       []modulaRecoveryControl
	definitions                    []sourceDefinition
	scopes                         []cLineScope
	headerOverflowProcedureSegment []modulaToken
	lineStarts                     []int
	imports                        []cLineSpan
	headerOverflowModuleExpression modulaRecoveryExpressionStream
	headerOverflowName             modulaToken
	headerOverflowProcedureStream  modulaRecoveryFormalStream
	pendingRepeatControl           modulaRecoveryControl
	headerOverflowProcedureBracket int
	headerOverflowImportStart      int
	pairedDirectiveCount           int
	// ownerOverflow counts named procedure/module owners beyond the bounded
	// frame stack. Keeping only their depth lets recovery ignore their ENDs
	// without allowing those ENDs to unwind retained frames.
	ownerOverflow                          int
	pendingRepeatEnd                       int
	controlOverflow                        int
	pendingBareBodyEndLine                 int
	lineCount                              int
	headerOverflowStart                    int
	headerOverflowProcedureParen           int
	headerOverflowRawOwnerParen            int
	invalidBodyDepth                       int
	headerOverflowParen                    int
	headerOverflowBracket                  int
	headerOverflowBlocks                   int
	invalidBodyRepeatDepth                 int
	headerOverflowProcedureReturnStream    modulaRecoveryReturnStream
	headerOverflowProcedureSuffixStreaming bool
	headerOverflowLocalModule              bool
	invalidBody                            bool
	headerOverflowProcedureSawSection      bool
	ownerOverflowForwardEligible           bool
	headerOverflowProcedureStreaming       bool
	headerOverflowProcedureValid           bool
	invalidBodyEnd                         bool
	headerOverflowTracksBlocks             bool
	headerOverflowProcedureFormalAttribute bool
	headerOverflowProcedureAttribute       bool
	headerOverflowModuleValid              bool
	headerOverflowModuleSuffix             uint8
	headerOverflowSection                  modulaRecoverySection
	headerOverflowRawOwnerState            uint8
	headerOverflowRawOwnerDefinition       bool
	headerOverflowRawOwnerLocalModule      bool
	headerOverflowProcedurePhase           uint8
	headerOverflowImport                   bool
	headerOverflowImportValid              bool
	headerOverflowImportState              uint8
	headerOverflowDefinition               bool
	pendingBareBodyEnd                     bool
	headerOverflowOwner                    modulaRecoveryOwnerKind
	pendingRepeatCondition                 bool
	headerOverflow                         bool
	invalidBodyOwner                       bool
	statementStart                         bool
	pairedDirective                        bool
	pairedDirectiveHeader                  bool
	invalidBodyOwnerName                   bool
	pairedDirectiveOverflow                bool
	invalidBodyName                        bool
	pairedDirectiveExactAttribute          bool
	unmatchedDirective                     bool
}

// analyzeModulaLexically is deliberately independent of the concrete tree.
// It consumes the full lexical stream (including units beyond retained-token
// and concrete-parser frontiers) while retaining only one bounded declaration
// header and bounded structural stacks.
func analyzeModulaLexically(source string, lineCount int) modulaLexicalAnalysis {
	lineCount = max(lineCount, 1)
	parser := modulaRecoveryParser{
		lineStarts: modulaLineStarts(source), lineCount: lineCount,
		frames: []modulaRecoveryFrame{{
			name: "", start: 1, definitionIndex: -1, invalid: true,
		}},
		statementStart: true,
	}
	walkModulaLexically(source, modulaLexicalSink{token: func(token modulaToken) bool {
		parser.accept(token)
		return true
	}})
	parser.finishRepeatCondition()
	if parser.ownerOverflow > 0 || parser.unmatchedDirective {
		parser.clearHeader()
	} else {
		parser.finishHeader(parser.lineCount, "", 0)
	}
	for len(parser.frames) > 1 {
		parser.closeNamedFrame("", parser.lineCount, "", 0)
	}
	for len(parser.controls) > 0 {
		control := parser.controls[len(parser.controls)-1]
		parser.controls = parser.controls[:len(parser.controls)-1]
		parser.addScope(control.start, parser.lineCount)
	}
	return modulaLexicalAnalysis{
		definitions: modulaSortUniqueDefinitions(parser.definitions, lineCount),
		scopes:      cNormalizeTreeLineScopes(parser.scopes, lineCount),
		imports:     cNormalizeTreeLineSpans(parser.imports, lineCount),
	}
}

func (parser *modulaRecoveryParser) accept(token modulaToken) {
	if parser.pairedDirective {
		parser.acceptPairedDirectiveToken(token)
		return
	}
	if parser.unmatchedDirective {
		parser.acceptUnmatchedDirectiveToken(token)
		return
	}
	if token.text == "<*" {
		if token.directiveClosed {
			parser.startPairedDirective(token)
		} else {
			// An unmatched opener is not trivia-to-EOF. Its payload is still
			// untrusted syntax, so ignore it until a physical-line declaration
			// restart rather than allowing payload tokens to mutate owners or
			// bounded structural state.
			parser.unmatchedDirective = true
		}
		return
	}
	if parser.invalidBody {
		parser.acceptInvalidBody(token)
		return
	}
	if parser.ownerOverflow > 0 {
		parser.acceptOwnerOverflow(token)
		return
	}
	if token.gap {
		parser.clearHeader()
		return
	}
	if parser.headerOverflow && token.lineStart &&
		modulaRecoveryRestartToken(token.text) && !parser.headerOpen() {
		parser.currentFrame().declarationPhase = 2
		parser.clearHeader()
	}
	line := parser.line(token.start)
	if parser.currentBody() {
		parser.acceptBody(token, line)
		return
	}

	if token.lineStart && len(parser.header) > 0 &&
		modulaRecoveryRestartToken(token.text) &&
		!parser.headerOpen() &&
		!parser.headerContinuesWith(token) {
		parser.currentFrame().declarationPhase = 2
		parser.clearHeader()
	}
	if len(parser.header) == 0 && !parser.headerOverflow {
		switch token.text {
		case "CONST":
			parser.setSection(modulaRecoveryConstSection)
			parser.currentFrame().declarationPhase = 2
			return
		case "TYPE":
			parser.setSection(modulaRecoveryTypeSection)
			parser.currentFrame().declarationPhase = 2
			return
		case "VAR":
			parser.setSection(modulaRecoveryVarSection)
			parser.currentFrame().declarationPhase = 2
			return
		case "BEGIN":
			if parser.currentFrame().definitionUnit {
				frame := parser.currentFrame()
				frame.declarationPhase = 2
				parser.startInvalidBody(frame.invalid && len(parser.frames) > 1)
			} else {
				parser.startBody(line)
			}
			return
		case "EXCEPT":
			frame := parser.currentFrame()
			frame.declarationPhase = 2
			parser.startInvalidBody(len(parser.frames) > 1 &&
				(!frame.definitionUnit || frame.invalid))
			return
		case "FINALLY":
			frame := parser.currentFrame()
			if frame.definitionUnit || frame.procedure {
				frame.declarationPhase = 2
				parser.startInvalidBody(frame.procedure ||
					frame.invalid && len(parser.frames) > 1)
			} else {
				parser.startBody(line)
			}
			return
		}
	}
	if token.text == ";" && !parser.headerOpen() {
		if len(parser.header) == 0 && !parser.headerOverflow {
			parser.currentFrame().declarationPhase = 2
		}
		parser.finishHeader(line, ";", token.end)
		return
	}
	if token.text == "." && len(parser.header) > 0 &&
		parser.header[0].text == "END" {
		parser.finishHeader(line, ".", token.end)
		return
	}
	parser.appendHeader(token)
}

func (parser *modulaRecoveryParser) startPairedDirective(token modulaToken) {
	parser.pairedDirective = true
	parser.pairedDirectiveHeader = !parser.invalidBody && !parser.currentBody()
	parser.pairedDirectiveTokens = append(parser.pairedDirectiveTokens[:0], token)
	parser.pairedDirectiveOverflow = false
	parser.pairedDirectiveCount = 1
	parser.pairedDirectiveExactAttribute = true
}

func (parser *modulaRecoveryParser) acceptPairedDirectiveToken(token modulaToken) {
	// Buffer at most one declaration frontier. The directive is committed as
	// one atom at its close, so payload punctuation/keywords can never mutate
	// the overflow procedure, import, or structural state.
	parser.pairedDirectiveCount++
	switch parser.pairedDirectiveCount {
	case 2:
		parser.pairedDirectiveExactAttribute =
			token.kind == modulaTokenIdentifier && !modulaKeyword(token.text)
	case 3:
		parser.pairedDirectiveExactAttribute =
			parser.pairedDirectiveExactAttribute && token.text == "*>"
	default:
		parser.pairedDirectiveExactAttribute = false
	}
	if len(parser.pairedDirectiveTokens) < modulaMaximumDeclarationTokens {
		parser.pairedDirectiveTokens = append(parser.pairedDirectiveTokens, token)
	} else {
		parser.pairedDirectiveOverflow = true
	}
	if token.text != "*>" {
		return
	}

	header := parser.pairedDirectiveHeader
	tokens := parser.pairedDirectiveTokens
	overflow := parser.pairedDirectiveOverflow
	exactAttribute := parser.pairedDirectiveExactAttribute &&
		parser.pairedDirectiveCount == 3
	parser.resetPairedDirective()
	if !header {
		return
	}
	if parser.headerOverflow {
		parser.acceptHeaderOverflowDirective(exactAttribute)
		return
	}
	if !overflow && len(parser.header)+len(tokens) <=
		modulaMaximumDeclarationTokens {
		parser.header = append(parser.header, tokens...)
		return
	}
	parser.beginHeaderOverflow()
	parser.acceptHeaderOverflowDirective(exactAttribute)
}

func (parser *modulaRecoveryParser) acceptUnmatchedDirectiveToken(
	token modulaToken,
) {
	if !token.lineStart || !modulaRecoveryRestartToken(token.text) {
		return
	}
	// The opener's declaration is irrecoverable, but the next physical-line
	// declaration boundary remains independently useful.
	parser.unmatchedDirective = false
	parser.clearHeader()
	parser.accept(token)
}

func (parser *modulaRecoveryParser) resetPairedDirective() {
	parser.pairedDirective = false
	parser.pairedDirectiveHeader = false
	parser.pairedDirectiveTokens = parser.pairedDirectiveTokens[:0]
	parser.pairedDirectiveOverflow = false
	parser.pairedDirectiveCount = 0
	parser.pairedDirectiveExactAttribute = false
}

func modulaRecoveryDirectiveRangeEnd(
	tokens []modulaToken,
	start, limit int,
) int {
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

func modulaRecoveryProcedureAttributeDirectiveValid(
	tokens []modulaToken,
	start, end int,
) bool {
	return end-start == 3 && tokens[start].text == "<*" &&
		tokens[start].directiveClosed &&
		tokens[start+1].kind == modulaTokenIdentifier &&
		!modulaKeyword(tokens[start+1].text) && tokens[start+2].text == "*>"
}

func modulaRecoveryHeaderContainsDirective(tokens []modulaToken) bool {
	for _, token := range tokens {
		if token.text == "<*" {
			return true
		}
	}
	return false
}

func (parser *modulaRecoveryParser) startInvalidBody(owner bool) {
	parser.clearHeader()
	parser.invalidBody = true
	parser.invalidBodyEnd = false
	parser.invalidBodyName = false
	parser.invalidBodyOwnerName = false
	parser.invalidBodyOwner = owner
	parser.invalidBodyDepth = 0
	parser.invalidBodyRepeatDepth = 0
}

func (parser *modulaRecoveryParser) acceptInvalidBody(token modulaToken) {
	if token.gap {
		parser.finishInvalidBodyEnd(parser.line(token.start))
		return
	}
	if parser.invalidBodyEnd {
		if !parser.invalidBodyName && token.kind == modulaTokenIdentifier &&
			!modulaKeyword(token.text) {
			parser.invalidBodyName = true
			parser.invalidBodyOwnerName = parser.invalidBodyOwner &&
				len(parser.frames) > 1 && token.text == parser.currentFrame().name
			return
		}
		if token.text == ";" || token.text == "." {
			parser.finishInvalidBodyEnd(parser.line(token.start))
			return
		}
		if parser.finishInvalidBodyEnd(parser.line(token.start)) {
			parser.accept(token)
			return
		}
	}
	if token.text == "END" {
		parser.invalidBodyEnd = true
		return
	}
	if token.text == "UNTIL" {
		if parser.invalidBodyRepeatDepth > 0 && parser.invalidBodyDepth > 0 {
			parser.invalidBodyRepeatDepth--
			parser.invalidBodyDepth--
		}
		return
	}
	if token.text == "BEGIN" || modulaRecoveryControlStarter(token.text) {
		parser.invalidBodyDepth++
		if token.text == "REPEAT" {
			parser.invalidBodyRepeatDepth++
		}
	}
}

func (parser *modulaRecoveryParser) finishInvalidBodyEnd(line int) bool {
	if !parser.invalidBodyEnd {
		return false
	}
	bare := !parser.invalidBodyName
	ownerName := parser.invalidBodyOwnerName
	parser.invalidBodyEnd = false
	parser.invalidBodyName = false
	parser.invalidBodyOwnerName = false
	if parser.invalidBodyDepth > 0 {
		parser.invalidBodyDepth--
		return false
	}
	if !bare && !ownerName {
		return false
	}
	closeOwner := parser.invalidBodyOwner
	parser.invalidBody = false
	parser.invalidBodyOwner = false
	if closeOwner && len(parser.frames) > 1 {
		parser.closeNamedFrame(parser.currentFrame().name, line, "", 0)
	}
	return true
}

func (parser *modulaRecoveryParser) acceptOwnerOverflow(token modulaToken) {
	if token.gap {
		parser.clearHeader()
		return
	}
	if parser.headerOverflow && token.lineStart &&
		modulaRecoveryRestartToken(token.text) && !parser.headerOpen() {
		parser.clearHeader()
	}
	if token.lineStart && len(parser.header) > 0 &&
		modulaRecoveryRestartToken(token.text) &&
		!parser.headerOpen() {
		parser.clearHeader()
	}
	if len(parser.header) == 0 && !parser.headerOverflow && token.text == "BEGIN" {
		parser.ownerOverflowForwardEligible = false
		return
	}
	if token.text == ";" && !parser.headerOpen() {
		parser.finishOwnerOverflowHeader()
		return
	}
	if token.text == "." && len(parser.header) > 0 &&
		parser.header[0].text == "END" {
		parser.finishOwnerOverflowHeader()
		return
	}
	parser.appendHeader(token)
}

func (parser *modulaRecoveryParser) finishOwnerOverflowHeader() {
	if parser.headerOverflow {
		owner := parser.headerOverflowOwner
		parser.clearHeader()
		if owner != modulaRecoveryNoOwner {
			parser.ownerOverflow++
			parser.ownerOverflowForwardEligible =
				owner == modulaRecoveryProcedureOwner
		} else {
			parser.ownerOverflowForwardEligible = false
		}
		return
	}
	header := parser.header
	parser.header = nil
	if len(header) == 2 && header[0].text == "END" &&
		header[1].kind == modulaTokenIdentifier {
		parser.ownerOverflow--
		parser.ownerOverflowForwardEligible = false
		return
	}
	if len(header) == 1 && header[0].text == "FORWARD" &&
		parser.ownerOverflowForwardEligible {
		parser.ownerOverflow--
		parser.ownerOverflowForwardEligible = false
		return
	}
	if modulaRecoveryModuleHeader(header) {
		parser.ownerOverflowForwardEligible = false
		if _, _, ok := modulaRecoveryModuleName(header); ok {
			parser.ownerOverflow++
		} else if modulaRecoveryHeaderContainsDirective(header) {
			if _, _, hasRawOwner := modulaRecoveryRawModuleName(header); hasRawOwner {
				// Invalid owners still own their END in recovery. Retain only
				// bounded depth; never emit a definition for this heading.
				parser.ownerOverflow++
			}
		}
		return
	}
	if len(header) > 0 && header[0].text == "PROCEDURE" {
		parser.ownerOverflowForwardEligible = false
		if name, ok := modulaRawProcedureName(header); ok &&
			!modulaKeyword(name.text) {
			parser.ownerOverflow++
			parser.ownerOverflowForwardEligible = true
		}
		return
	}
	parser.ownerOverflowForwardEligible = false
}

func (parser *modulaRecoveryParser) headerContinuesWith(token modulaToken) bool {
	if token.text == "IMPORT" && len(parser.header) == 2 &&
		parser.header[0].text == "FROM" &&
		parser.header[1].kind == modulaTokenIdentifier &&
		!modulaKeyword(parser.header[1].text) {
		return true
	}
	if token.text != "PROCEDURE" || len(parser.header) < 2 {
		return false
	}
	last := parser.header[len(parser.header)-1].text
	switch parser.currentFrame().section {
	case modulaRecoveryTypeSection:
		return last == "=" || last == "TO" || last == "OF"
	case modulaRecoveryVarSection:
		return last == ":"
	case modulaRecoveryNoSection, modulaRecoveryConstSection:
		return false
	}
	return false
}

func (parser *modulaRecoveryParser) acceptBody(token modulaToken, line int) {
	parser.acceptRepeatConditionToken(token, line)
	if parser.pendingBareBodyEnd {
		pendingLine := parser.pendingBareBodyEndLine
		parser.pendingBareBodyEnd = false
		parser.pendingBareBodyEndLine = 0
		if modulaRecoveryOwnerRestartToken(token.text) {
			parser.closeNamedFrame(parser.currentFrame().name, pendingLine, "", 0)
			parser.accept(token)
			return
		}
	}
	if len(parser.header) > 0 {
		frameDepth := len(parser.frames)
		switch {
		case token.lineStart && token.text == "END":
			// A bare control END may be followed immediately by its owner's END.
			parser.finishBodyEnd(line-1, "", 0)
			if len(parser.frames) < frameDepth {
				parser.accept(token)
				return
			}
		case token.text == ";" || token.text == ".":
			parser.finishBodyEnd(line, token.text, token.end)
			return
		case len(parser.header) == 1 && token.kind == modulaTokenIdentifier &&
			(!modulaKeyword(token.text) || parser.currentFrame().invalid &&
				token.text == parser.currentFrame().name):
			parser.appendHeader(token)
			return
		default:
			parser.finishBodyEnd(line, "", 0)
			if len(parser.frames) < frameDepth {
				parser.accept(token)
				return
			}
			if parser.pendingBareBodyEnd {
				parser.accept(token)
				return
			}
		}
	}

	if token.text == "END" {
		parser.appendHeader(token)
		return
	}
	if token.text == "UNTIL" {
		parser.startRepeatCondition(line)
		parser.statementStart = false
		return
	}
	openedControl := parser.statementStart && modulaRecoveryControlStarter(token.text)
	if openedControl {
		parser.openControl(token.text, line)
	}
	switch token.text {
	case ";", ":", "THEN", "DO", "OF", "ELSE", "ELSIF", "EXCEPT", "FINALLY", "|":
		parser.statementStart = true
	case "LOOP", "REPEAT":
		parser.statementStart = openedControl
	default:
		parser.statementStart = false
	}
}

func (parser *modulaRecoveryParser) appendHeader(token modulaToken) {
	if parser.headerOverflow {
		parser.updateHeaderOverflow(token)
		return
	}
	if len(parser.header) >= modulaMaximumDeclarationTokens {
		parser.startHeaderOverflow(token)
		return
	}
	parser.header = append(parser.header, token)
}

func (parser *modulaRecoveryParser) startHeaderOverflow(token modulaToken) {
	parser.beginHeaderOverflow()
	parser.updateHeaderOverflow(token)
}

func (parser *modulaRecoveryParser) beginHeaderOverflow() {
	header := parser.header
	parser.headerOverflow = true
	if len(header) > 0 {
		parser.headerOverflowStart = parser.line(header[0].start)
	}
	if len(header) > 0 && header[0].text == "PROCEDURE" {
		if name, ok := modulaRawProcedureName(header); ok &&
			!modulaKeyword(name.text) {
			parser.headerOverflowOwner = modulaRecoveryProcedureOwner
			parser.headerOverflowName = name
			parser.headerOverflowDefinition = parser.currentFrame().definitionUnit
		}
	} else if name, definition, ok := modulaRecoveryRawModuleName(header); ok {
		parser.headerOverflowOwner = modulaRecoveryModuleOwner
		parser.headerOverflowName = name
		parser.headerOverflowDefinition = definition
		parser.headerOverflowLocalModule = header[0].text == "MODULE"
		parser.headerOverflowModuleValid = true
		parser.headerOverflowModuleSuffix = modulaRecoveryModuleSuffixAfterName
	} else if len(header) > 0 &&
		(header[0].text == "IMPORT" || header[0].text == "FROM") {
		parser.headerOverflowImport = true
		parser.headerOverflowImportStart = parser.line(header[0].start)
	} else if len(header) > 0 &&
		header[0].kind == modulaTokenIdentifier &&
		!modulaKeyword(header[0].text) {
		switch parser.currentFrame().section {
		case modulaRecoveryTypeSection, modulaRecoveryVarSection:
			parser.headerOverflowSection = parser.currentFrame().section
		case modulaRecoveryNoSection, modulaRecoveryConstSection:
		}
	}
	if parser.headerOverflowOwner == modulaRecoveryNoOwner {
		parser.initializeHeaderOverflowRawOwner(header)
	}
	if parser.headerOverflowOwner == modulaRecoveryProcedureOwner {
		parser.initializeHeaderOverflowProcedure(header)
	}
	if parser.headerOverflowImport {
		parser.initializeHeaderOverflowImport(header)
	}
	for index := 0; index < len(header); index++ {
		if header[index].text == "<*" {
			end := modulaRecoveryDirectiveRangeEnd(header, index, len(header))
			if end < 0 {
				if parser.headerOverflowOwner == modulaRecoveryModuleOwner {
					parser.headerOverflowModuleValid = false
				}
				break
			}
			if parser.headerOverflowOwner == modulaRecoveryModuleOwner {
				parser.headerOverflowModuleValid = false
			}
			index = end - 1
			continue
		}
		parser.updateHeaderOverflowModuleSuffix(header[index])
		parser.updateHeaderOverflowStructure(header[index])
	}
	parser.header = nil
}

func (parser *modulaRecoveryParser) updateHeaderOverflow(token modulaToken) {
	parser.acceptHeaderOverflowRawOwnerToken(token)
	if parser.headerOverflowOwner == modulaRecoveryProcedureOwner {
		parser.updateHeaderOverflowProcedure(token)
	}
	if parser.headerOverflowImport {
		parser.updateHeaderOverflowImport(token)
	}
	parser.updateHeaderOverflowModuleSuffix(token)
	parser.updateHeaderOverflowStructure(token)
}

func (parser *modulaRecoveryParser) updateHeaderOverflowModuleSuffix(
	token modulaToken,
) {
	if parser.headerOverflowOwner != modulaRecoveryModuleOwner ||
		!parser.headerOverflowModuleValid ||
		token.start <= parser.headerOverflowName.start {
		return
	}
	switch parser.headerOverflowModuleSuffix {
	case modulaRecoveryModuleSuffixAfterName:
		if parser.headerOverflowDefinition || token.text != "[" {
			parser.headerOverflowModuleValid = false
			return
		}
		parser.headerOverflowModuleSuffix = modulaRecoveryModuleSuffixPriority
		parser.headerOverflowModuleExpression =
			newModulaRecoveryExpressionStream()
	case modulaRecoveryModuleSuffixPriority:
		if token.text == "]" &&
			len(parser.headerOverflowModuleExpression.delimiters) == 0 {
			if !parser.headerOverflowModuleExpression.finish() {
				parser.headerOverflowModuleValid = false
				return
			}
			parser.headerOverflowModuleSuffix = modulaRecoveryModuleSuffixDone
			return
		}
		parser.headerOverflowModuleExpression.accept(token)
	case modulaRecoveryModuleSuffixDone:
		parser.headerOverflowModuleValid = false
	default:
		parser.headerOverflowModuleValid = false
	}
}

func (parser *modulaRecoveryParser) headerOverflowModuleHeaderValid() bool {
	if !parser.headerOverflowModuleValid {
		return false
	}
	return parser.headerOverflowModuleSuffix ==
		modulaRecoveryModuleSuffixAfterName ||
		parser.headerOverflowModuleSuffix == modulaRecoveryModuleSuffixDone
}

func (parser *modulaRecoveryParser) initializeHeaderOverflowRawOwner(
	header []modulaToken,
) {
	if len(header) == 0 {
		return
	}
	switch header[0].text {
	case "PROCEDURE":
		parser.headerOverflowRawOwnerState =
			modulaRecoveryRawOwnerProcedureName
		parser.headerOverflowRawOwnerDefinition =
			parser.currentFrame().definitionUnit
	case "MODULE":
		parser.headerOverflowRawOwnerState = modulaRecoveryRawOwnerModuleName
		parser.headerOverflowRawOwnerLocalModule = true
	case "DEFINITION":
		parser.headerOverflowRawOwnerState =
			modulaRecoveryRawOwnerModuleKeyword
		parser.headerOverflowRawOwnerDefinition = true
	case "IMPLEMENTATION":
		parser.headerOverflowRawOwnerState =
			modulaRecoveryRawOwnerModuleKeyword
	default:
		return
	}
	for index := 1; index < len(header) &&
		parser.headerOverflowRawOwnerState != modulaRecoveryRawOwnerNone; index++ {
		if header[index].text == "<*" {
			end := modulaRecoveryDirectiveRangeEnd(header, index, len(header))
			if end < 0 {
				parser.headerOverflowRawOwnerState = modulaRecoveryRawOwnerNone
				return
			}
			index = end - 1
			continue
		}
		parser.acceptHeaderOverflowRawOwnerToken(header[index])
	}
}

func (parser *modulaRecoveryParser) acceptHeaderOverflowRawOwnerToken(
	token modulaToken,
) {
	state := parser.headerOverflowRawOwnerState
	if state == modulaRecoveryRawOwnerNone {
		return
	}
	identifier := token.kind == modulaTokenIdentifier &&
		!modulaKeyword(token.text)
	switch state {
	case modulaRecoveryRawOwnerProcedureName:
		switch token.text {
		case "(":
			if parser.headerOverflowRawOwnerParen >=
				modulaMaximumStructuralDepth {
				parser.headerOverflowRawOwnerState =
					modulaRecoveryRawOwnerNone
				return
			}
			parser.headerOverflowRawOwnerParen++
			return
		case ")":
			if parser.headerOverflowRawOwnerParen <= 0 {
				parser.headerOverflowRawOwnerState =
					modulaRecoveryRawOwnerNone
				return
			}
			parser.headerOverflowRawOwnerParen--
			return
		}
		if parser.headerOverflowRawOwnerParen > 0 {
			return
		}
		if modulaProcedureMarker(token.text) {
			return
		}
		if !identifier {
			parser.headerOverflowRawOwnerState = modulaRecoveryRawOwnerNone
			return
		}
		parser.headerOverflowOwner = modulaRecoveryProcedureOwner
		parser.headerOverflowName = token
		parser.headerOverflowDefinition =
			parser.headerOverflowRawOwnerDefinition
		// A directive before the raw name is not a procedure attribute.
		// Keep the invalid owner only to protect its nested declarations/END.
		parser.headerOverflowProcedureValid = false
		parser.headerOverflowRawOwnerState = modulaRecoveryRawOwnerNone
	case modulaRecoveryRawOwnerModuleKeyword:
		if token.text != "MODULE" {
			parser.headerOverflowRawOwnerState = modulaRecoveryRawOwnerNone
			return
		}
		if parser.headerOverflowRawOwnerDefinition {
			parser.headerOverflowRawOwnerState =
				modulaRecoveryRawOwnerDefinitionForOrName
		} else {
			parser.headerOverflowRawOwnerState =
				modulaRecoveryRawOwnerModuleName
		}
	case modulaRecoveryRawOwnerDefinitionForOrName:
		if token.text == "FOR" {
			parser.headerOverflowRawOwnerState =
				modulaRecoveryRawOwnerDefinitionLiteral
			return
		}
		parser.captureHeaderOverflowRawModuleName(token, identifier)
	case modulaRecoveryRawOwnerDefinitionLiteral:
		if !modulaGNUStringToken(token) {
			parser.headerOverflowRawOwnerState = modulaRecoveryRawOwnerNone
			return
		}
		parser.headerOverflowRawOwnerState = modulaRecoveryRawOwnerModuleName
	case modulaRecoveryRawOwnerModuleName:
		parser.captureHeaderOverflowRawModuleName(token, identifier)
	default:
		parser.headerOverflowRawOwnerState = modulaRecoveryRawOwnerNone
	}
}

func (parser *modulaRecoveryParser) captureHeaderOverflowRawModuleName(
	token modulaToken,
	identifier bool,
) {
	if !identifier {
		parser.headerOverflowRawOwnerState = modulaRecoveryRawOwnerNone
		return
	}
	parser.headerOverflowOwner = modulaRecoveryModuleOwner
	parser.headerOverflowName = token
	parser.headerOverflowDefinition = parser.headerOverflowRawOwnerDefinition
	parser.headerOverflowLocalModule = parser.headerOverflowRawOwnerLocalModule
	// Directives cannot appear in module headings. Retain an invalid owner
	// frame, but never promote its raw name to a definition.
	parser.headerOverflowModuleValid = false
	parser.headerOverflowRawOwnerState = modulaRecoveryRawOwnerNone
}

func (parser *modulaRecoveryParser) acceptHeaderOverflowDirective(
	exactProcedureAttribute bool,
) {
	if parser.headerOverflowOwner == modulaRecoveryProcedureOwner {
		parser.updateHeaderOverflowProcedureDirective(exactProcedureAttribute)
	}
	if parser.headerOverflowOwner == modulaRecoveryModuleOwner {
		parser.headerOverflowModuleValid = false
	}
	if parser.headerOverflowImport {
		parser.headerOverflowImportValid = false
	}
}

func (parser *modulaRecoveryParser) updateHeaderOverflowStructure(
	token modulaToken,
) {
	atTopLevel := parser.headerOverflowParen == 0 &&
		parser.headerOverflowBracket == 0
	if !parser.headerOverflowTracksBlocks && atTopLevel {
		if parser.headerOverflowSection == modulaRecoveryTypeSection &&
			token.text == "=" ||
			parser.headerOverflowSection == modulaRecoveryVarSection &&
				token.text == ":" {
			parser.headerOverflowTracksBlocks = true
		}
	}
	switch token.text {
	case "(":
		parser.headerOverflowParen++
	case ")":
		if parser.headerOverflowParen > 0 {
			parser.headerOverflowParen--
		}
	case "[":
		parser.headerOverflowBracket++
	case "]":
		if parser.headerOverflowBracket > 0 {
			parser.headerOverflowBracket--
		}
	case "RECORD":
		if parser.headerOverflowTracksBlocks {
			parser.headerOverflowBlocks++
		}
	case "CASE":
		if parser.headerOverflowTracksBlocks && parser.headerOverflowBlocks > 0 {
			parser.headerOverflowBlocks++
		}
	case "END":
		if parser.headerOverflowBlocks > 0 {
			parser.headerOverflowBlocks--
		}
	}
}

func (parser *modulaRecoveryParser) initializeHeaderOverflowProcedure(
	header []modulaToken,
) {
	parser.headerOverflowProcedureValid = true
	parser.headerOverflowProcedurePhase = modulaRecoveryProcedureOverflowExpectOpen
	nameIndex, ok := modulaRecoveryProcedureName(
		header,
		parser.currentFrame().definitionUnit,
	)
	if !ok || nameIndex < 0 || nameIndex >= len(header) ||
		header[nameIndex].start != parser.headerOverflowName.start {
		parser.headerOverflowProcedureValid = false
		return
	}
	for index := nameIndex + 1; index < len(header); index++ {
		if header[index].text == "<*" {
			end := modulaRecoveryDirectiveRangeEnd(header, index, len(header))
			if end < 0 {
				parser.headerOverflowProcedureValid = false
				return
			}
			parser.updateHeaderOverflowProcedureDirective(
				modulaRecoveryProcedureAttributeDirectiveValid(
					header, index, end,
				),
			)
			index = end - 1
			continue
		}
		parser.updateHeaderOverflowProcedure(header[index])
	}
}

func (parser *modulaRecoveryParser) updateHeaderOverflowProcedure(
	token modulaToken,
) {
	if !parser.headerOverflowProcedureValid {
		return
	}
	if parser.headerOverflowProcedureAttribute {
		parser.headerOverflowProcedureValid = false
		return
	}
	switch parser.headerOverflowProcedurePhase {
	case modulaRecoveryProcedureOverflowExpectOpen:
		if token.text != "(" {
			parser.headerOverflowProcedureValid = false
			return
		}
		parser.headerOverflowProcedurePhase =
			modulaRecoveryProcedureOverflowParameters
		parser.headerOverflowProcedureParen = 1
	case modulaRecoveryProcedureOverflowParameters:
		if parser.headerOverflowProcedureFormalAttribute {
			atSectionEnd := token.text == ")" &&
				parser.headerOverflowProcedureParen == 1 &&
				parser.headerOverflowProcedureBracket == 0
			atSectionSeparator := token.text == ";" &&
				parser.headerOverflowProcedureParen == 1 &&
				parser.headerOverflowProcedureBracket == 0
			if !atSectionEnd && !atSectionSeparator {
				parser.headerOverflowProcedureValid = false
				return
			}
		}
		switch token.text {
		case "(":
			parser.headerOverflowProcedureParen++
			parser.appendHeaderOverflowProcedureToken(token)
		case ")":
			if parser.headerOverflowProcedureParen <= 0 {
				parser.headerOverflowProcedureValid = false
				return
			}
			if parser.headerOverflowProcedureParen == 1 {
				if parser.headerOverflowProcedureBracket != 0 {
					parser.headerOverflowProcedureValid = false
					return
				}
				parser.finishHeaderOverflowProcedureSection(true)
				parser.headerOverflowProcedureParen = 0
				parser.headerOverflowProcedurePhase =
					modulaRecoveryProcedureOverflowSuffix
				return
			}
			parser.headerOverflowProcedureParen--
			parser.appendHeaderOverflowProcedureToken(token)
		case "[":
			parser.headerOverflowProcedureBracket++
			parser.appendHeaderOverflowProcedureToken(token)
		case "]":
			if parser.headerOverflowProcedureBracket <= 0 {
				parser.headerOverflowProcedureValid = false
				return
			}
			parser.headerOverflowProcedureBracket--
			parser.appendHeaderOverflowProcedureToken(token)
		case ";":
			if parser.headerOverflowProcedureParen == 1 &&
				parser.headerOverflowProcedureBracket == 0 {
				parser.finishHeaderOverflowProcedureSection(false)
				return
			}
			parser.appendHeaderOverflowProcedureToken(token)
		default:
			parser.appendHeaderOverflowProcedureToken(token)
		}
		if parser.headerOverflowProcedureParen+
			parser.headerOverflowProcedureBracket > modulaMaximumStructuralDepth {
			parser.headerOverflowProcedureValid = false
		}
	case modulaRecoveryProcedureOverflowSuffix:
		parser.appendHeaderOverflowProcedureToken(token)
	default:
		parser.headerOverflowProcedureValid = false
	}
}

func (parser *modulaRecoveryParser) updateHeaderOverflowProcedureDirective(
	exact bool,
) {
	if !parser.headerOverflowProcedureValid {
		return
	}
	if !exact {
		parser.headerOverflowProcedureValid = false
		return
	}
	switch parser.headerOverflowProcedurePhase {
	case modulaRecoveryProcedureOverflowParameters:
		if parser.headerOverflowProcedureFormalAttribute ||
			parser.headerOverflowProcedureBracket != 0 {
			parser.headerOverflowProcedureValid = false
			return
		}
		if parser.headerOverflowProcedureStreaming {
			stream := parser.headerOverflowProcedureStream
			if stream.optional || stream.extended ||
				!stream.finish(parser.currentFrame().definitionUnit) {
				parser.headerOverflowProcedureValid = false
				return
			}
		} else {
			section := parser.headerOverflowProcedureSegment
			extended := len(section) == 1 && section[0].text == "..." ||
				len(section) > 0 && section[0].text == "["
			if extended || !modulaRecoveryFormalSectionValid(
				section,
				parser.currentFrame().definitionUnit,
			) {
				parser.headerOverflowProcedureValid = false
				return
			}
		}
		parser.headerOverflowProcedureFormalAttribute = true
	case modulaRecoveryProcedureOverflowSuffix:
		if parser.headerOverflowProcedureAttribute {
			parser.headerOverflowProcedureValid = false
			return
		}
		parser.headerOverflowProcedureAttribute = true
	default:
		parser.headerOverflowProcedureValid = false
	}
}

func (parser *modulaRecoveryParser) appendHeaderOverflowProcedureToken(
	token modulaToken,
) {
	if !parser.headerOverflowProcedureValid {
		return
	}
	if parser.headerOverflowProcedurePhase ==
		modulaRecoveryProcedureOverflowSuffix {
		if parser.headerOverflowProcedureSuffixStreaming {
			parser.headerOverflowProcedureReturnStream.accept(token)
			if !parser.headerOverflowProcedureReturnStream.valid {
				parser.headerOverflowProcedureValid = false
			}
			return
		}
		if len(parser.headerOverflowProcedureSegment) >=
			modulaMaximumDeclarationTokens {
			parser.headerOverflowProcedureSuffixStreaming = true
			parser.headerOverflowProcedureReturnStream =
				newModulaRecoveryReturnStream()
			for _, retained := range parser.headerOverflowProcedureSegment {
				parser.headerOverflowProcedureReturnStream.accept(retained)
			}
			parser.headerOverflowProcedureSegment = nil
			parser.headerOverflowProcedureReturnStream.accept(token)
			if !parser.headerOverflowProcedureReturnStream.valid {
				parser.headerOverflowProcedureValid = false
			}
			return
		}
		parser.headerOverflowProcedureSegment = append(
			parser.headerOverflowProcedureSegment,
			token,
		)
		return
	}
	if parser.headerOverflowProcedureStreaming {
		parser.headerOverflowProcedureStream.accept(token)
		if !parser.headerOverflowProcedureStream.valid {
			parser.headerOverflowProcedureValid = false
		}
		return
	}
	if len(parser.headerOverflowProcedureSegment) >=
		modulaMaximumDeclarationTokens {
		parser.headerOverflowProcedureStreaming = true
		parser.headerOverflowProcedureStream = newModulaRecoveryFormalStream()
		for _, retained := range parser.headerOverflowProcedureSegment {
			parser.headerOverflowProcedureStream.accept(retained)
		}
		parser.headerOverflowProcedureSegment = nil
		parser.headerOverflowProcedureStream.accept(token)
		if !parser.headerOverflowProcedureStream.valid {
			parser.headerOverflowProcedureValid = false
		}
		return
	}
	parser.headerOverflowProcedureSegment = append(
		parser.headerOverflowProcedureSegment,
		token,
	)
}

func (parser *modulaRecoveryParser) finishHeaderOverflowProcedureSection(
	final bool,
) {
	if !parser.headerOverflowProcedureValid {
		return
	}
	section := parser.headerOverflowProcedureSegment
	parser.headerOverflowProcedureSegment = nil
	streaming := parser.headerOverflowProcedureStreaming
	stream := parser.headerOverflowProcedureStream
	parser.headerOverflowProcedureStreaming = false
	parser.headerOverflowProcedureStream = modulaRecoveryFormalStream{}
	parser.headerOverflowProcedureFormalAttribute = false
	parser.headerOverflowProcedureSuffixStreaming = false
	parser.headerOverflowProcedureReturnStream = modulaRecoveryReturnStream{}
	if !streaming && len(section) == 0 {
		if final && !parser.headerOverflowProcedureSawSection {
			return
		}
		parser.headerOverflowProcedureValid = false
		return
	}
	var extended, valid bool
	if streaming {
		extended = stream.optional || stream.extended
		valid = stream.finish(parser.currentFrame().definitionUnit)
	} else {
		extended = len(section) == 1 && section[0].text == "..." ||
			section[0].text == "["
		valid = modulaRecoveryFormalSectionValid(
			section,
			parser.currentFrame().definitionUnit,
		)
	}
	if !final && extended || !valid {
		parser.headerOverflowProcedureValid = false
		return
	}
	parser.headerOverflowProcedureSawSection = true
}

func newModulaRecoveryFormalStream() modulaRecoveryFormalStream {
	return modulaRecoveryFormalStream{
		state: modulaRecoveryFormalStreamStart,
		valid: true,
	}
}

func newModulaRecoveryExpressionStream() modulaRecoveryExpressionStream {
	return modulaRecoveryExpressionStream{
		expectOperand: true,
		allowSign:     true,
		valid:         true,
	}
}

func (stream *modulaRecoveryExpressionStream) accept(token modulaToken) {
	if !stream.valid {
		return
	}
	identifier := token.kind == modulaTokenIdentifier &&
		!modulaKeyword(token.text)
	if stream.attributeState != modulaRecoveryAttributeNone {
		stream.acceptAttributeToken(token, identifier)
		return
	}
	operand := identifier || token.kind == modulaTokenNumber ||
		token.text == modulaLiteralToken
	if modulaKeyword(token.text) {
		switch token.text {
		case "__COLUMN__", "__DATE__", "__FILE__", "__FUNCTION__", "__LINE__":
			operand = true
		}
	}
	if stream.selector {
		if !identifier {
			stream.valid = false
			return
		}
		stream.selector = false
		stream.expectOperand = false
		stream.allowSign = false
		stream.designator = true
		stream.sawOperand = true
		stream.markExpressionContent()
		return
	}

	switch token.text {
	case "(":
		if len(stream.delimiters) >= modulaMaximumStructuralDepth ||
			!stream.expectOperand && !stream.designator {
			stream.valid = false
			return
		}
		call := !stream.expectOperand
		stream.delimiters = append(stream.delimiters,
			modulaRecoveryExpressionDelimiter{
				text: "(", allowEmpty: call,
				general: call || stream.currentExpressionGeneral(),
			},
		)
		stream.expectOperand = true
		stream.allowSign = true
		stream.designator = false
		stream.constructorAllowed = false
		return
	case "[":
		if stream.expectOperand || !stream.designator ||
			!stream.currentExpressionGeneral() ||
			len(stream.delimiters) >= modulaMaximumStructuralDepth {
			stream.valid = false
			return
		}
		stream.delimiters = append(stream.delimiters,
			modulaRecoveryExpressionDelimiter{
				text: "[", general: true, resultDesignator: true,
			},
		)
		stream.expectOperand = true
		stream.allowSign = true
		stream.designator = false
		stream.constructorAllowed = false
		return
	case "{":
		if !stream.expectOperand && !stream.constructorAllowed ||
			len(stream.delimiters) >= modulaMaximumStructuralDepth {
			stream.valid = false
			return
		}
		stream.delimiters = append(stream.delimiters,
			modulaRecoveryExpressionDelimiter{
				text: "{", allowEmpty: true,
			},
		)
		stream.expectOperand = true
		stream.allowSign = true
		stream.designator = false
		stream.constructorAllowed = false
		return
	case ")", "]", "}":
		expected := "("
		switch token.text {
		case "]":
			expected = "["
		case "}":
			expected = "{"
		}
		if len(stream.delimiters) == 0 ||
			stream.delimiters[len(stream.delimiters)-1].text != expected {
			stream.valid = false
			return
		}
		last := len(stream.delimiters) - 1
		delimiter := stream.delimiters[last]
		if stream.expectOperand &&
			(delimiter.content || !delimiter.allowEmpty) {
			stream.valid = false
			return
		}
		stream.delimiters = stream.delimiters[:last]
		stream.expectOperand = false
		stream.allowSign = false
		stream.designator = delimiter.resultDesignator
		stream.constructorAllowed = false
		stream.sawOperand = true
		stream.markExpressionContent()
		return
	}

	if operand {
		if !stream.expectOperand {
			stream.valid = false
			return
		}
		stream.expectOperand = false
		stream.allowSign = false
		stream.designator = identifier
		stream.constructorAllowed = identifier
		stream.sawOperand = true
		stream.markExpressionContent()
		return
	}

	switch token.text {
	case ".":
		if stream.expectOperand || !stream.designator {
			stream.valid = false
			return
		}
		stream.selector = true
	case "^":
		if stream.expectOperand || !stream.designator ||
			!stream.currentExpressionGeneral() {
			stream.valid = false
			return
		}
		stream.constructorAllowed = false
	case ",":
		if stream.expectOperand || len(stream.delimiters) == 0 {
			stream.valid = false
			return
		}
		delimiter := &stream.delimiters[len(stream.delimiters)-1]
		if delimiter.text == "(" && !delimiter.allowEmpty {
			stream.valid = false
			return
		}
		delimiter.relation = false
		delimiter.rangeSeen = false
		delimiter.bySeen = false
		stream.expectOperand = true
		stream.allowSign = true
		stream.designator = false
		stream.constructorAllowed = false
	case "+", "-":
		if stream.expectOperand {
			if !stream.allowSign {
				stream.valid = false
				return
			}
			stream.allowSign = false
			return
		}
		stream.expectOperand = true
		stream.allowSign = false
		stream.designator = false
		stream.constructorAllowed = false
	case "NOT":
		if !stream.expectOperand {
			stream.valid = false
			return
		}
		stream.allowSign = false
		stream.designator = false
		stream.constructorAllowed = false
	case "__ATTRIBUTE__":
		if !stream.expectOperand {
			stream.valid = false
			return
		}
		stream.attributeState = modulaRecoveryAttributeBuiltin
		stream.allowSign = false
		stream.designator = false
		stream.constructorAllowed = false
	case "=", "#", "<>", "<", "<=", ">", ">=", "IN":
		if stream.expectOperand {
			stream.valid = false
			return
		}
		if stream.expressionRelationSeen() {
			stream.valid = false
			return
		}
		stream.setExpressionRelation(true)
		stream.expectOperand = true
		stream.allowSign = true
		stream.designator = false
		stream.constructorAllowed = false
	case "..", "BY":
		if stream.expectOperand {
			stream.valid = false
			return
		}
		if len(stream.delimiters) == 0 {
			stream.valid = false
			return
		}
		delimiter := &stream.delimiters[len(stream.delimiters)-1]
		if delimiter.text != "{" || delimiter.bySeen ||
			token.text == ".." && delimiter.rangeSeen {
			stream.valid = false
			return
		}
		if token.text == ".." {
			delimiter.rangeSeen = true
		} else {
			delimiter.bySeen = true
		}
		delimiter.relation = false
		stream.expectOperand = true
		stream.allowSign = true
		stream.designator = false
		stream.constructorAllowed = false
	case "*", "/", "DIV", "MOD", "REM", "AND", "OR", "&":
		if stream.expectOperand {
			stream.valid = false
			return
		}
		stream.expectOperand = true
		stream.allowSign = false
		stream.designator = false
		stream.constructorAllowed = false
	default:
		stream.valid = false
	}
}

func (stream *modulaRecoveryExpressionStream) acceptAttributeToken(
	token modulaToken,
	identifier bool,
) {
	switch stream.attributeState {
	case modulaRecoveryAttributeBuiltin:
		if token.text != "__BUILTIN__" {
			stream.valid = false
			return
		}
		stream.attributeState = modulaRecoveryAttributeOpenOuter
	case modulaRecoveryAttributeOpenOuter:
		if token.text != "(" {
			stream.valid = false
			return
		}
		stream.attributeState = modulaRecoveryAttributeOpenInner
	case modulaRecoveryAttributeOpenInner:
		if token.text != "(" {
			stream.valid = false
			return
		}
		stream.attributeState = modulaRecoveryAttributeExpression
	case modulaRecoveryAttributeExpression:
		switch {
		case identifier:
			stream.attributeState = modulaRecoveryAttributeSimpleClose
		case token.text == "<":
			stream.attributeState = modulaRecoveryAttributeAngleQualident
		default:
			stream.valid = false
		}
	case modulaRecoveryAttributeSimpleClose:
		if token.text != ")" {
			stream.valid = false
			return
		}
		stream.attributeState = modulaRecoveryAttributeCloseOuter
	case modulaRecoveryAttributeAngleQualident:
		if !identifier {
			stream.valid = false
			return
		}
		stream.attributeState = modulaRecoveryAttributeAngleAfterQualident
	case modulaRecoveryAttributeAngleAfterQualident:
		switch token.text {
		case ".":
			stream.attributeState = modulaRecoveryAttributeAngleQualident
		case ",":
			stream.attributeState = modulaRecoveryAttributeAngleFinalIdent
		default:
			stream.valid = false
		}
	case modulaRecoveryAttributeAngleFinalIdent:
		if !identifier {
			stream.valid = false
			return
		}
		stream.attributeState = modulaRecoveryAttributeAngleClose
	case modulaRecoveryAttributeAngleClose:
		if token.text != ">" {
			stream.valid = false
			return
		}
		stream.attributeState = modulaRecoveryAttributeCloseInner
	case modulaRecoveryAttributeCloseInner:
		if token.text != ")" {
			stream.valid = false
			return
		}
		stream.attributeState = modulaRecoveryAttributeCloseOuter
	case modulaRecoveryAttributeCloseOuter:
		if token.text != ")" {
			stream.valid = false
			return
		}
		stream.attributeState = modulaRecoveryAttributeNone
		stream.expectOperand = false
		stream.allowSign = false
		stream.designator = false
		stream.constructorAllowed = false
		stream.sawOperand = true
		stream.markExpressionContent()
	default:
		stream.valid = false
	}
}

func (stream *modulaRecoveryExpressionStream) markExpressionContent() {
	if len(stream.delimiters) > 0 {
		stream.delimiters[len(stream.delimiters)-1].content = true
	}
}

func (stream *modulaRecoveryExpressionStream) currentExpressionGeneral() bool {
	if len(stream.delimiters) == 0 {
		return false
	}
	return stream.delimiters[len(stream.delimiters)-1].general
}

func (stream *modulaRecoveryExpressionStream) expressionRelationSeen() bool {
	if len(stream.delimiters) == 0 {
		return stream.rootRelation
	}
	return stream.delimiters[len(stream.delimiters)-1].relation
}

func (stream *modulaRecoveryExpressionStream) setExpressionRelation(value bool) {
	if len(stream.delimiters) == 0 {
		stream.rootRelation = value
		return
	}
	stream.delimiters[len(stream.delimiters)-1].relation = value
}

func (stream *modulaRecoveryExpressionStream) finish() bool {
	return stream.valid && stream.sawOperand && !stream.expectOperand &&
		!stream.selector && stream.attributeState == modulaRecoveryAttributeNone &&
		len(stream.delimiters) == 0
}

func (stream *modulaRecoveryFormalStream) accept(token modulaToken) {
	if !stream.valid {
		return
	}
	identifier := token.kind == modulaTokenIdentifier &&
		!modulaKeyword(token.text)
	switch stream.state {
	case modulaRecoveryFormalStreamStart:
		switch {
		case token.text == "[":
			stream.optional = true
			stream.state = modulaRecoveryFormalStreamNeedName
		case token.text == "VAR":
			stream.state = modulaRecoveryFormalStreamNeedName
		case token.text == "...":
			stream.extended = true
			stream.state = modulaRecoveryFormalStreamExtendedDone
		case identifier:
			stream.state = modulaRecoveryFormalStreamAfterName
		default:
			stream.valid = false
		}
	case modulaRecoveryFormalStreamNeedName:
		if !identifier {
			stream.valid = false
			return
		}
		stream.state = modulaRecoveryFormalStreamAfterName
	case modulaRecoveryFormalStreamAfterName:
		switch token.text {
		case ",":
			if stream.optional {
				stream.valid = false
				return
			}
			stream.state = modulaRecoveryFormalStreamNeedName
		case ":":
			stream.state = modulaRecoveryFormalStreamTypeStart
		default:
			stream.valid = false
		}
	case modulaRecoveryFormalStreamTypeStart:
		switch {
		case token.text == "ARRAY":
			stream.state = modulaRecoveryFormalStreamArrayOF
		case identifier:
			stream.state = modulaRecoveryFormalStreamAfterTypeName
		default:
			stream.valid = false
		}
	case modulaRecoveryFormalStreamArrayOF:
		if token.text != "OF" {
			stream.valid = false
			return
		}
		stream.state = modulaRecoveryFormalStreamTypeStart
	case modulaRecoveryFormalStreamAfterTypeName:
		switch token.text {
		case ".":
			stream.state = modulaRecoveryFormalStreamQualifiedName
		case "=":
			if !stream.optional {
				stream.valid = false
				return
			}
			stream.state = modulaRecoveryFormalStreamDefaultStart
		case "]":
			if !stream.optional {
				stream.valid = false
				return
			}
			stream.state = modulaRecoveryFormalStreamOptionalDone
		default:
			stream.valid = false
		}
	case modulaRecoveryFormalStreamQualifiedName:
		if !identifier {
			stream.valid = false
			return
		}
		stream.state = modulaRecoveryFormalStreamAfterTypeName
	case modulaRecoveryFormalStreamDefaultStart:
		if token.text == "]" {
			stream.valid = false
			return
		}
		stream.defaultSeen = true
		stream.defaultExpression = newModulaRecoveryExpressionStream()
		stream.state = modulaRecoveryFormalStreamDefault
		stream.acceptDefaultExpressionToken(token)
	case modulaRecoveryFormalStreamDefault:
		stream.acceptDefaultExpressionToken(token)
	case modulaRecoveryFormalStreamOptionalDone,
		modulaRecoveryFormalStreamExtendedDone:
		stream.valid = false
	default:
		stream.valid = false
	}
}

func (stream *modulaRecoveryFormalStream) acceptDefaultExpressionToken(
	token modulaToken,
) {
	if token.text == "]" && stream.defaultDepth == 0 {
		if !stream.defaultExpression.finish() {
			stream.valid = false
			return
		}
		stream.state = modulaRecoveryFormalStreamOptionalDone
		return
	}
	switch token.text {
	case "[":
		stream.defaultDepth++
	case "]":
		stream.defaultDepth--
	}
	stream.defaultExpression.accept(token)
	if !stream.defaultExpression.valid {
		stream.valid = false
	}
}

func (stream *modulaRecoveryFormalStream) finish(definition bool) bool {
	if !stream.valid {
		return false
	}
	if stream.optional {
		return stream.state == modulaRecoveryFormalStreamOptionalDone &&
			(!definition || stream.defaultSeen)
	}
	if stream.extended {
		return stream.state == modulaRecoveryFormalStreamExtendedDone
	}
	return stream.state == modulaRecoveryFormalStreamAfterTypeName
}

func (parser *modulaRecoveryParser) headerOverflowProcedureHeadingValid() bool {
	if !parser.headerOverflowProcedureValid ||
		parser.headerOverflowProcedurePhase != modulaRecoveryProcedureOverflowSuffix {
		return false
	}
	if parser.headerOverflowProcedureSuffixStreaming {
		return parser.headerOverflowProcedureReturnStream.finish()
	}
	suffix := parser.headerOverflowProcedureSegment
	if len(suffix) == 0 {
		return true
	}
	return len(suffix) > 1 && suffix[0].text == ":" &&
		modulaRecoveryReturnTypeValid(suffix, 1, len(suffix))
}

func newModulaRecoveryReturnStream() modulaRecoveryReturnStream {
	return modulaRecoveryReturnStream{
		state: modulaRecoveryReturnStreamStart,
		valid: true,
	}
}

func (stream *modulaRecoveryReturnStream) accept(token modulaToken) {
	if !stream.valid {
		return
	}
	identifier := token.kind == modulaTokenIdentifier &&
		!modulaKeyword(token.text)
	switch stream.state {
	case modulaRecoveryReturnStreamStart:
		if token.text != ":" {
			stream.valid = false
			return
		}
		stream.state = modulaRecoveryReturnStreamTypeStart
	case modulaRecoveryReturnStreamTypeStart:
		if token.text == "[" {
			if stream.optional {
				stream.valid = false
				return
			}
			stream.optional = true
			return
		}
		if !identifier {
			stream.valid = false
			return
		}
		stream.state = modulaRecoveryReturnStreamAfterTypeName
	case modulaRecoveryReturnStreamAfterTypeName:
		switch token.text {
		case ".":
			stream.state = modulaRecoveryReturnStreamQualifiedName
		case "]":
			if !stream.optional {
				stream.valid = false
				return
			}
			stream.state = modulaRecoveryReturnStreamOptionalDone
		default:
			stream.valid = false
		}
	case modulaRecoveryReturnStreamQualifiedName:
		if !identifier {
			stream.valid = false
			return
		}
		stream.state = modulaRecoveryReturnStreamAfterTypeName
	case modulaRecoveryReturnStreamOptionalDone:
		stream.valid = false
	default:
		stream.valid = false
	}
}

func (stream *modulaRecoveryReturnStream) finish() bool {
	if !stream.valid {
		return false
	}
	if stream.optional {
		return stream.state == modulaRecoveryReturnStreamOptionalDone
	}
	return stream.state == modulaRecoveryReturnStreamAfterTypeName
}

func (parser *modulaRecoveryParser) initializeHeaderOverflowImport(
	header []modulaToken,
) {
	parser.headerOverflowImportValid = true
	parser.headerOverflowImportState = modulaRecoveryImportOverflowStart
	for index := 0; index < len(header); index++ {
		if header[index].text == "<*" {
			end := modulaRecoveryDirectiveRangeEnd(header, index, len(header))
			parser.headerOverflowImportValid = false
			if end < 0 {
				return
			}
			index = end - 1
			continue
		}
		parser.updateHeaderOverflowImport(header[index])
	}
}

func (parser *modulaRecoveryParser) updateHeaderOverflowImport(
	token modulaToken,
) {
	if !parser.headerOverflowImportValid {
		return
	}
	switch parser.headerOverflowImportState {
	case modulaRecoveryImportOverflowStart:
		switch token.text {
		case "IMPORT":
			parser.headerOverflowImportState = modulaRecoveryImportOverflowName
		case "FROM":
			parser.headerOverflowImportState = modulaRecoveryImportOverflowSource
		default:
			parser.headerOverflowImportValid = false
		}
	case modulaRecoveryImportOverflowSource:
		if token.kind != modulaTokenIdentifier || modulaKeyword(token.text) {
			parser.headerOverflowImportValid = false
			return
		}
		parser.headerOverflowImportState = modulaRecoveryImportOverflowKeyword
	case modulaRecoveryImportOverflowKeyword:
		if token.text != "IMPORT" {
			parser.headerOverflowImportValid = false
			return
		}
		parser.headerOverflowImportState = modulaRecoveryImportOverflowName
	case modulaRecoveryImportOverflowName:
		if token.kind != modulaTokenIdentifier || modulaKeyword(token.text) {
			parser.headerOverflowImportValid = false
			return
		}
		parser.headerOverflowImportState = modulaRecoveryImportOverflowComma
	case modulaRecoveryImportOverflowComma:
		if token.text != "," {
			parser.headerOverflowImportValid = false
			return
		}
		parser.headerOverflowImportState = modulaRecoveryImportOverflowName
	default:
		parser.headerOverflowImportValid = false
	}
}

func (parser *modulaRecoveryParser) headerOverflowImportHeaderValid() bool {
	return parser.headerOverflowImportValid &&
		parser.headerOverflowImportState == modulaRecoveryImportOverflowComma
}

func (parser *modulaRecoveryParser) headerOpen() bool {
	if parser.headerOverflow {
		return parser.headerOverflowParen > 0 ||
			parser.headerOverflowBracket > 0 || parser.headerOverflowBlocks > 0
	}
	return modulaRecoveryHeaderOpen(
		parser.header,
		parser.headerBlockStart(parser.header),
	)
}

func (parser *modulaRecoveryParser) headerBlockStart(
	header []modulaToken,
) int {
	if len(header) == 0 {
		return -1
	}
	if header[0].kind != modulaTokenIdentifier || modulaKeyword(header[0].text) {
		return -1
	}
	switch parser.currentFrame().section {
	case modulaRecoveryTypeSection:
		if index := modulaRecoveryTopLevelToken(header, "="); index > 0 {
			return index + 1
		}
	case modulaRecoveryVarSection:
		if index := modulaRecoveryTopLevelToken(header, ":"); index > 0 {
			return index + 1
		}
	case modulaRecoveryNoSection, modulaRecoveryConstSection:
	}
	return -1
}

func (parser *modulaRecoveryParser) finishHeader(
	line int,
	terminator string,
	terminatorEnd int,
) {
	if parser.headerOverflow {
		parser.finishOverflowHeader(line)
		return
	}
	header := parser.header
	parser.header = nil
	if len(header) == 0 {
		return
	}
	if header[0].text == "END" {
		if len(header) == 2 && header[1].kind == modulaTokenIdentifier {
			parser.closeNamedFrame(header[1].text, line, terminator, terminatorEnd)
		}
		return
	}
	if header[0].text == "FORWARD" && parser.currentFrame().procedure {
		parser.closeNamedFrame(parser.currentFrame().name, line, "", 0)
		return
	}
	if modulaRecoveryModuleHeader(header) {
		parser.acceptModuleHeader(header, line)
		return
	}
	switch header[0].text {
	case "IMPORT", "FROM":
		frame := parser.currentFrame()
		if !frame.procedure && !frame.invalid && frame.declarationPhase == 0 &&
			modulaRecoveryImportHeaderValid(header) {
			parser.imports = append(parser.imports, cLineSpan{
				start: parser.line(header[0].start), end: line,
			})
		} else {
			frame.declarationPhase = 2
		}
		return
	case "EXPORT":
		parser.currentFrame().declarationPhase = max(
			parser.currentFrame().declarationPhase, 1,
		)
		return
	case "PROCEDURE":
		parser.acceptProcedureHeader(header, line)
		return
	case "MODULE":
		parser.acceptModuleHeader(header, line)
		return
	}
	parser.currentFrame().declarationPhase = 2
	parser.acceptSectionHeader(header, line)
}

func (parser *modulaRecoveryParser) finishOverflowHeader(line int) {
	owner := parser.headerOverflowOwner
	name := parser.headerOverflowName
	start := parser.headerOverflowStart
	definition := parser.headerOverflowDefinition
	localModule := parser.headerOverflowLocalModule
	moduleValid := parser.headerOverflowModuleHeaderValid()
	procedureValid := parser.headerOverflowProcedureHeadingValid()
	importHeader := parser.headerOverflowImport
	importValid := parser.headerOverflowImportHeaderValid()
	importStart := parser.headerOverflowImportStart
	parser.clearHeader()
	frame := parser.currentFrame()
	parentPhase := frame.declarationPhase
	if importHeader {
		if importValid && !frame.procedure && !frame.invalid && parentPhase == 0 {
			parser.imports = append(parser.imports, cLineSpan{
				start: importStart,
				end:   line,
			})
		} else {
			frame.declarationPhase = 2
		}
		return
	}
	frame.declarationPhase = 2
	if owner == modulaRecoveryNoOwner || name.kind != modulaTokenIdentifier ||
		modulaKeyword(name.text) {
		return
	}
	frame.section = modulaRecoveryNoSection
	if owner == modulaRecoveryModuleOwner {
		localInDefinition := localModule && len(parser.frames) > 1 &&
			frame.definitionUnit
		nonLocalNested := len(parser.frames) > 1 && !localModule
		parentInvalid := len(parser.frames) > 1 && frame.invalid
		duplicateUnit := len(parser.frames) == 1 && parentPhase != 0
		invalidModule := localInDefinition || nonLocalNested || parentInvalid ||
			duplicateUnit || !moduleValid
		definitionIndex := -1
		if !invalidModule {
			definitionIndex = parser.addDefinition(name, start, line, false)
		}
		if len(parser.frames) >= modulaMaximumStructuralDepth {
			parser.ownerOverflow++
			parser.ownerOverflowForwardEligible = false
			return
		}
		parser.frames = append(parser.frames, modulaRecoveryFrame{
			name: name.text, start: start, definitionIndex: definitionIndex,
			definitionUnit: definition, invalid: invalidModule,
		})
		return
	}
	invalidProcedure := frame.invalid || !procedureValid
	definitionIndex := -1
	if !invalidProcedure {
		definitionIndex = parser.addDefinition(
			name,
			start,
			line,
			frame.definitionUnit,
		)
	}
	if frame.definitionUnit {
		return
	}
	if len(parser.frames) >= modulaMaximumStructuralDepth {
		parser.ownerOverflow++
		parser.ownerOverflowForwardEligible = true
		return
	}
	parser.frames = append(parser.frames, modulaRecoveryFrame{
		name: name.text, start: start, definitionIndex: definitionIndex,
		procedure: true, invalid: invalidProcedure,
	})
}

func (parser *modulaRecoveryParser) acceptModuleHeader(
	header []modulaToken,
	line int,
) {
	parent := parser.currentFrame()
	parentPhase := parent.declarationPhase
	localInDefinition := header[0].text == "MODULE" && len(parser.frames) > 1 &&
		parent.definitionUnit
	nonLocalNested := len(parser.frames) > 1 && header[0].text != "MODULE"
	parentInvalid := len(parser.frames) > 1 && parent.invalid
	duplicateUnit := len(parser.frames) == 1 && parentPhase != 0
	parent.section = modulaRecoveryNoSection
	parent.declarationPhase = 2
	nameIndex, definition, ok := modulaRecoveryModuleName(header)
	if !ok || nameIndex < 0 || nameIndex >= len(header) {
		rawName, rawDefinition, hasRawOwner :=
			modulaRecoveryRawModuleName(header)
		if !hasRawOwner && modulaRecoveryHeaderContainsDirective(header) {
			rawName, rawDefinition, hasRawOwner =
				modulaRecoveryRawModuleNameAcrossDirectives(header)
		}
		if hasRawOwner {
			if len(parser.frames) >= modulaMaximumStructuralDepth {
				parser.ownerOverflow++
				parser.ownerOverflowForwardEligible = false
				return
			}
			parser.frames = append(parser.frames, modulaRecoveryFrame{
				name: rawName.text, start: parser.line(header[0].start),
				definitionIndex: -1, definitionUnit: rawDefinition,
				invalid: true,
			})
		}
		return
	}
	name := header[nameIndex]
	invalidModule := localInDefinition || nonLocalNested || parentInvalid || duplicateUnit
	definitionIndex := -1
	if !invalidModule {
		definitionIndex = parser.addDefinition(
			name, parser.line(header[0].start), line, false,
		)
	}
	if len(parser.frames) >= modulaMaximumStructuralDepth {
		parser.ownerOverflow++
		parser.ownerOverflowForwardEligible = false
		return
	}
	parser.frames = append(parser.frames, modulaRecoveryFrame{
		name: name.text, start: parser.line(header[0].start),
		definitionIndex: definitionIndex, definitionUnit: definition,
		invalid: invalidModule,
	})
	parser.setSection(modulaRecoveryNoSection)
}

func (parser *modulaRecoveryParser) acceptProcedureHeader(
	header []modulaToken,
	line int,
) {
	parent := parser.currentFrame()
	definitionUnit := parent.definitionUnit
	parent.section = modulaRecoveryNoSection
	parent.declarationPhase = 2
	rawName, hasRawOwner := modulaRawProcedureName(header)
	if parent.invalid {
		if hasRawOwner && len(parser.frames) >= modulaMaximumStructuralDepth {
			parser.ownerOverflow++
			parser.ownerOverflowForwardEligible = true
		} else if hasRawOwner {
			parser.frames = append(parser.frames, modulaRecoveryFrame{
				name: rawName.text, start: parser.line(header[0].start),
				definitionIndex: -1, procedure: true, invalid: true,
			})
		}
		return
	}
	nameIndex, ok := modulaRecoveryProcedureName(
		header, definitionUnit,
	)
	if !ok || !modulaRecoveryProcedureHeadingValid(
		header, nameIndex, definitionUnit,
	) {
		if hasRawOwner && !definitionUnit &&
			len(parser.frames) >= modulaMaximumStructuralDepth {
			parser.ownerOverflow++
			parser.ownerOverflowForwardEligible = true
		} else if hasRawOwner && !definitionUnit {
			parser.frames = append(parser.frames, modulaRecoveryFrame{
				name: rawName.text, start: parser.line(header[0].start),
				definitionIndex: -1, procedure: true, invalid: true,
			})
		}
		return
	}
	name := header[nameIndex]
	startLine := parser.line(header[0].start)
	definitionIndex := parser.addDefinition(name, startLine, line, definitionUnit)
	if definitionUnit {
		return
	}
	if len(parser.frames) >= modulaMaximumStructuralDepth {
		parser.ownerOverflow++
		parser.ownerOverflowForwardEligible = true
		return
	}
	parser.frames = append(parser.frames, modulaRecoveryFrame{
		name: name.text, start: startLine, definitionIndex: definitionIndex,
		procedure: true,
	})
}

func (parser *modulaRecoveryParser) acceptSectionHeader(
	header []modulaToken,
	line int,
) {
	if parser.currentFrame().invalid {
		return
	}
	section := parser.currentFrame().section
	switch section {
	case modulaRecoveryConstSection:
		if len(header) < 3 || header[0].kind != modulaTokenIdentifier ||
			header[1].text != "=" ||
			!modulaConstantExpressionRangeValid(header, 2, len(header)) {
			return
		}
		parser.addDefinition(header[0], parser.line(header[0].start), line, false)
	case modulaRecoveryTypeSection:
		if len(header) == 1 && parser.currentFrame().definitionUnit {
			parser.addDefinition(header[0], parser.line(header[0].start), line, false)
			return
		}
		if len(header) < 3 || header[0].kind != modulaTokenIdentifier ||
			header[1].text != "=" ||
			!modulaTypeRangeValid(header, 2, len(header)) {
			return
		}
		ownsScope := false
		for _, token := range header[2:] {
			if token.text == "RECORD" {
				ownsScope = true
				break
			}
		}
		parser.addDefinition(header[0], parser.line(header[0].start), line, ownsScope)
		parser.addTypeMemberDefinitions(header, 2, len(header), line)
	case modulaRecoveryVarSection:
		colon := modulaRecoveryTopLevelToken(header, ":")
		if colon <= 0 || colon+1 >= len(header) ||
			!modulaTypeRangeValid(header, colon+1, len(header)) {
			return
		}
		names, ok := modulaDeclaratorNames(header, 0, colon)
		if !ok {
			return
		}
		for _, name := range names {
			parser.addDefinition(name, parser.line(name.start), line, false)
		}
		parser.addTypeMemberDefinitions(header, colon+1, len(header), line)
	case modulaRecoveryNoSection:
		return
	}
}

func (parser *modulaRecoveryParser) addTypeMemberDefinitions(
	header []modulaToken,
	start, end, line int,
) {
	for _, member := range modulaTypeMemberNodes(header, start, end) {
		for _, child := range member.children {
			if child.kind != "identifier" || child.start >= child.end {
				continue
			}
			token, ok := modulaRecoveryTokenAt(header, child.start, child.end)
			if ok {
				parser.addDefinition(token, parser.line(token.start), line, false)
			}
		}
	}
}

func (parser *modulaRecoveryParser) finishBodyEnd(
	line int,
	terminator string,
	terminatorEnd int,
) {
	header := parser.header
	parser.header = nil
	if len(header) == 1 {
		closedControl := parser.closeControl(parser.line(header[0].start))
		if !closedControl && parser.controlOverflow == 0 &&
			!parser.hasCurrentFrameControl() && len(parser.frames) > 1 {
			parser.pendingBareBodyEnd = true
			parser.pendingBareBodyEndLine = line
		}
		parser.statementStart = true
		return
	}
	if len(header) == 2 && header[1].kind == modulaTokenIdentifier {
		parser.closeNamedFrame(header[1].text, line, terminator, terminatorEnd)
		parser.statementStart = true
	}
}

func (parser *modulaRecoveryParser) closeNamedFrame(
	name string,
	line int,
	terminator string,
	terminatorEnd int,
) {
	if len(parser.frames) <= 1 {
		return
	}
	if parser.pendingRepeatCondition &&
		parser.pendingRepeatControl.frameDepth >= len(parser.frames) {
		parser.finishRepeatCondition()
	}
	frame := parser.frames[len(parser.frames)-1]
	expectedTerminator := ";"
	if len(parser.frames) == 2 {
		expectedTerminator = "."
	}
	matched := name == frame.name && terminator == expectedTerminator
	if frame.definitionIndex >= 0 && frame.definitionIndex < len(parser.definitions) {
		definition := &parser.definitions[frame.definitionIndex]
		if matched {
			definition.ownsScope = true
			definition.scopeStart = frame.start
			definition.scopeEnd = max(frame.start, line)
			ownedEndLine, ownedEndColumn := modulaLineAndColumn(
				parser.lineStarts, terminatorEnd,
			)
			if terminatorEnd <= 0 || ownedEndLine != definition.scopeEnd {
				ownedEndColumn = 0
			}
			definition.ownedEndColumn = ownedEndColumn
			parser.addScope(frame.start, line)
		}
	}
	if matched && frame.bodyStart > 0 {
		parser.addScope(frame.bodyStart, line)
	}
	depth := len(parser.frames)
	parser.frames = parser.frames[:depth-1]
	for len(parser.controls) > 0 &&
		parser.controls[len(parser.controls)-1].frameDepth >= depth {
		control := parser.controls[len(parser.controls)-1]
		parser.controls = parser.controls[:len(parser.controls)-1]
		parser.addScope(control.start, line)
	}
	parser.controlOverflow = 0
	parser.pendingBareBodyEnd = false
	parser.pendingBareBodyEndLine = 0
}

func (parser *modulaRecoveryParser) startBody(line int) {
	frame := parser.currentFrame()
	frame.body = true
	frame.bodyStart = line
	frame.section = modulaRecoveryNoSection
	parser.statementStart = true
	_ = line
}

func (parser *modulaRecoveryParser) openControl(text string, line int) {
	if len(parser.controls) >= modulaMaximumStructuralDepth {
		parser.controlOverflow++
		return
	}
	parser.controls = append(parser.controls, modulaRecoveryControl{
		start: line, frameDepth: len(parser.frames), repeat: text == "REPEAT",
	})
}

func (parser *modulaRecoveryParser) closeControl(line int) bool {
	if parser.controlOverflow > 0 {
		parser.controlOverflow--
		return true
	}
	if len(parser.controls) == 0 {
		return false
	}
	last := len(parser.controls) - 1
	if parser.controls[last].repeat ||
		parser.controls[last].frameDepth != len(parser.frames) {
		return false
	}
	control := parser.controls[last]
	parser.controls = parser.controls[:last]
	parser.addScope(control.start, line)
	return true
}

func (parser *modulaRecoveryParser) hasCurrentFrameControl() bool {
	for index := len(parser.controls) - 1; index >= 0; index-- {
		if parser.controls[index].frameDepth == len(parser.frames) {
			return true
		}
		if parser.controls[index].frameDepth < len(parser.frames) {
			break
		}
	}
	return false
}

func (parser *modulaRecoveryParser) startRepeatCondition(line int) {
	parser.finishRepeatCondition()
	if parser.controlOverflow > 0 {
		parser.controlOverflow--
		return
	}
	for index := len(parser.controls) - 1; index >= 0; index-- {
		if parser.controls[index].repeat &&
			parser.controls[index].frameDepth == len(parser.frames) {
			control := parser.controls[index]
			parser.controls = append(parser.controls[:index], parser.controls[index+1:]...)
			parser.pendingRepeatCondition = true
			parser.pendingRepeatControl = control
			parser.pendingRepeatEnd = line
			return
		}
	}
}

func (parser *modulaRecoveryParser) acceptRepeatConditionToken(
	token modulaToken,
	line int,
) {
	if !parser.pendingRepeatCondition {
		return
	}
	boundary := token.lineStart && modulaRecoveryRestartToken(token.text)
	switch token.text {
	case ";", "END", "ELSE", "ELSIF", "EXCEPT", "FINALLY", "|", "UNTIL":
		boundary = true
	}
	if boundary {
		parser.finishRepeatCondition()
		return
	}
	parser.pendingRepeatEnd = max(parser.pendingRepeatEnd, line)
}

func (parser *modulaRecoveryParser) finishRepeatCondition() {
	if !parser.pendingRepeatCondition {
		return
	}
	parser.addScope(
		parser.pendingRepeatControl.start,
		max(parser.pendingRepeatControl.start, parser.pendingRepeatEnd),
	)
	parser.pendingRepeatCondition = false
	parser.pendingRepeatControl = modulaRecoveryControl{}
	parser.pendingRepeatEnd = 0
}

func (parser *modulaRecoveryParser) addDefinition(
	name modulaToken,
	startLine, endLine int,
	ownsScope bool,
) int {
	if name.kind != modulaTokenIdentifier || modulaKeyword(name.text) {
		return -1
	}
	line := parser.line(name.start)
	column := name.start - parser.lineStart(line) + 1
	definition := sourceDefinition{
		symbol: name.text, line: line, column: max(column, 1),
		scopeStart: line, scopeEnd: line, ownsScope: ownsScope,
	}
	if ownsScope {
		definition.scopeStart = max(1, min(startLine, parser.lineCount))
		definition.scopeEnd = max(definition.scopeStart, min(endLine, parser.lineCount))
	}
	parser.definitions = append(parser.definitions, definition)
	if ownsScope {
		parser.addScope(definition.scopeStart, definition.scopeEnd)
	}
	return len(parser.definitions) - 1
}

func (parser *modulaRecoveryParser) addScope(start, end int) {
	start = max(1, min(start, parser.lineCount))
	end = max(start, min(end, parser.lineCount))
	parser.scopes = append(parser.scopes, cLineScope{start: start, end: end})
}

func (parser *modulaRecoveryParser) line(offset int) int {
	return max(1, min(modulaTokenLine(parser.lineStarts, offset), parser.lineCount))
}

func (parser *modulaRecoveryParser) lineStart(line int) int {
	if line < 1 || line > len(parser.lineStarts) {
		return 0
	}
	return parser.lineStarts[line-1]
}

func (parser *modulaRecoveryParser) currentFrame() *modulaRecoveryFrame {
	return &parser.frames[len(parser.frames)-1]
}

func (parser *modulaRecoveryParser) currentBody() bool {
	return len(parser.frames) > 1 && parser.currentFrame().body
}

func (parser *modulaRecoveryParser) setSection(section modulaRecoverySection) {
	parser.currentFrame().section = section
}

func (parser *modulaRecoveryParser) clearHeader() {
	parser.header = nil
	parser.headerOverflow = false
	parser.headerOverflowOwner = modulaRecoveryNoOwner
	parser.headerOverflowName = modulaToken{}
	parser.headerOverflowStart = 0
	parser.headerOverflowDefinition = false
	parser.headerOverflowLocalModule = false
	parser.headerOverflowSection = modulaRecoveryNoSection
	parser.headerOverflowParen = 0
	parser.headerOverflowBracket = 0
	parser.headerOverflowBlocks = 0
	parser.headerOverflowTracksBlocks = false
	parser.headerOverflowProcedureValid = false
	parser.headerOverflowProcedurePhase = modulaRecoveryProcedureOverflowExpectOpen
	parser.headerOverflowProcedureParen = 0
	parser.headerOverflowProcedureBracket = 0
	parser.headerOverflowProcedureSawSection = false
	parser.headerOverflowProcedureSegment = nil
	parser.headerOverflowProcedureStreaming = false
	parser.headerOverflowProcedureStream = modulaRecoveryFormalStream{}
	parser.headerOverflowProcedureFormalAttribute = false
	parser.headerOverflowProcedureSuffixStreaming = false
	parser.headerOverflowProcedureReturnStream = modulaRecoveryReturnStream{}
	parser.headerOverflowProcedureAttribute = false
	parser.headerOverflowModuleValid = false
	parser.headerOverflowModuleSuffix = modulaRecoveryModuleSuffixAfterName
	parser.headerOverflowModuleExpression = modulaRecoveryExpressionStream{}
	parser.headerOverflowRawOwnerState = modulaRecoveryRawOwnerNone
	parser.headerOverflowRawOwnerDefinition = false
	parser.headerOverflowRawOwnerLocalModule = false
	parser.headerOverflowRawOwnerParen = 0
	parser.headerOverflowImport = false
	parser.headerOverflowImportValid = false
	parser.headerOverflowImportState = modulaRecoveryImportOverflowStart
	parser.headerOverflowImportStart = 0
	parser.resetPairedDirective()
	parser.unmatchedDirective = false
}

func modulaRecoveryModuleHeader(header []modulaToken) bool {
	if len(header) < 2 {
		return false
	}
	return header[0].text == "MODULE" ||
		(len(header) > 2 && (header[0].text == "DEFINITION" ||
			header[0].text == "IMPLEMENTATION") && header[1].text == "MODULE")
}

func modulaRecoveryModuleName(header []modulaToken) (int, bool, bool) {
	var index int
	definition := false
	switch header[0].text {
	case "MODULE":
		index = 1
	case "DEFINITION":
		definition = true
		index = 2
		if index < len(header) && header[index].text == "FOR" {
			index++
			if index >= len(header) || !modulaGNUStringToken(header[index]) {
				return -1, false, false
			}
			index++
		}
	case "IMPLEMENTATION":
		index = 2
	default:
		return -1, false, false
	}
	if index >= len(header) || header[index].kind != modulaTokenIdentifier ||
		modulaKeyword(header[index].text) {
		return -1, false, false
	}
	index++
	if index < len(header) {
		if definition || header[index].text != "[" ||
			!modulaRecoveryBalancedRange(header, index, "[", "]") {
			return -1, false, false
		}
	}
	return index - 1, definition, true
}

func modulaRecoveryRawModuleName(
	header []modulaToken,
) (modulaToken, bool, bool) {
	if len(header) < 2 {
		return modulaToken{}, false, false
	}
	var index int
	definition := false
	switch header[0].text {
	case "MODULE":
		index = 1
	case "DEFINITION":
		if len(header) < 3 || header[1].text != "MODULE" {
			return modulaToken{}, false, false
		}
		definition = true
		index = 2
		if index < len(header) && header[index].text == "FOR" {
			index++
			if index >= len(header) || !modulaGNUStringToken(header[index]) {
				return modulaToken{}, false, false
			}
			index++
		}
	case "IMPLEMENTATION":
		if len(header) < 3 || header[1].text != "MODULE" {
			return modulaToken{}, false, false
		}
		index = 2
	default:
		return modulaToken{}, false, false
	}
	if index >= len(header) || header[index].kind != modulaTokenIdentifier ||
		modulaKeyword(header[index].text) {
		return modulaToken{}, false, false
	}
	return header[index], definition, true
}

func modulaRecoveryRawModuleNameAcrossDirectives(
	header []modulaToken,
) (modulaToken, bool, bool) {
	if len(header) < 2 {
		return modulaToken{}, false, false
	}
	next := func(index int) (int, bool) {
		for index < len(header) && header[index].text == "<*" {
			end := modulaRecoveryDirectiveRangeEnd(header, index, len(header))
			if end < 0 {
				return -1, false
			}
			index = end
		}
		return index, index < len(header)
	}
	index := 1
	definition := false
	switch header[0].text {
	case "MODULE":
	case "DEFINITION", "IMPLEMENTATION":
		definition = header[0].text == "DEFINITION"
		var ok bool
		if index, ok = next(index); !ok || header[index].text != "MODULE" {
			return modulaToken{}, false, false
		}
		index++
	default:
		return modulaToken{}, false, false
	}
	var ok bool
	if index, ok = next(index); !ok {
		return modulaToken{}, false, false
	}
	if definition && header[index].text == "FOR" {
		index++
		if index, ok = next(index); !ok || !modulaGNUStringToken(header[index]) {
			return modulaToken{}, false, false
		}
		index++
		if index, ok = next(index); !ok {
			return modulaToken{}, false, false
		}
	}
	if header[index].kind != modulaTokenIdentifier ||
		modulaKeyword(header[index].text) {
		return modulaToken{}, false, false
	}
	return header[index], definition, true
}

func modulaRecoveryProcedureName(
	header []modulaToken,
	definition bool,
) (int, bool) {
	// Procedure prefixes are exact grammar productions: arbitrary balanced
	// parentheses are not attributes, and markers cannot become owner names.
	index := 1
	if definition {
		if index < len(header) && (header[index].text == "__BUILTIN__" ||
			header[index].text == "__INLINE__") {
			index++
		}
	} else if index < len(header) {
		switch header[index].text {
		case "__INLINE__":
			index++
		case "__ATTRIBUTE__":
			if index+6 >= len(header) ||
				header[index+1].text != "__BUILTIN__" ||
				header[index+2].text != "(" ||
				header[index+3].text != "(" ||
				header[index+4].kind != modulaTokenIdentifier ||
				modulaKeyword(header[index+4].text) ||
				header[index+5].text != ")" ||
				header[index+6].text != ")" {
				return -1, false
			}
			index += 7
		}
	}
	if index >= len(header) || header[index].kind != modulaTokenIdentifier ||
		modulaKeyword(header[index].text) {
		return -1, false
	}
	return index, true
}

func modulaRecoveryProcedureHeadingValid(
	header []modulaToken,
	nameIndex int,
	definition bool,
) bool {
	if nameIndex < 1 || nameIndex >= len(header) {
		return false
	}
	headingEnd := len(header)
	if headingEnd-nameIndex >= 4 && header[headingEnd-3].text == "<*" {
		suffix := header[headingEnd-3:]
		if !suffix[0].directiveClosed || suffix[1].kind != modulaTokenIdentifier ||
			modulaKeyword(suffix[1].text) || suffix[2].text != "*>" {
			return false
		}
		headingEnd -= 3
	}
	index := nameIndex + 1
	if index >= headingEnd {
		return true
	}
	if header[index].text != "(" {
		return false
	}
	end := modulaRecoveryMatchingDelimiter(header, index, "(", ")")
	if end < 0 {
		return false
	}
	if !modulaRecoveryFormalParametersValid(header, index+1, end, definition) {
		return false
	}
	index = end + 1
	if index == headingEnd {
		return true
	}
	return index+1 < headingEnd && header[index].text == ":" &&
		modulaRecoveryReturnTypeValid(header, index+1, headingEnd)
}

func modulaRecoveryReturnTypeValid(tokens []modulaToken, start, end int) bool {
	if start < end && tokens[start].text == "[" {
		if modulaRecoveryMatchingDelimiter(tokens, start, "[", "]") != end-1 {
			return false
		}
		start++
		end--
	}
	return modulaRecoveryTypeReferenceValid(tokens, start, end)
}

func modulaRecoveryFormalParametersValid(
	tokens []modulaToken,
	start, end int,
	definition bool,
) bool {
	if start == end {
		return true
	}
	segmentStart := start
	depth := 0
	for index := start; index <= end; index++ {
		atEnd := index == end
		if !atEnd {
			switch tokens[index].text {
			case "(", "[":
				depth++
			case ")", "]":
				depth--
			}
		}
		if atEnd || depth == 0 && tokens[index].text == ";" {
			section := tokens[segmentStart:index]
			extended := len(section) == 1 && section[0].text == "..." ||
				len(section) > 0 && section[0].text == "["
			if extended && !atEnd {
				return false
			}
			if !modulaRecoveryFormalSectionValid(section, definition) {
				return false
			}
			segmentStart = index + 1
		}
	}
	return segmentStart == end+1
}

func modulaRecoveryFormalSectionValid(
	tokens []modulaToken,
	definition bool,
) bool {
	if len(tokens) == 1 && tokens[0].text == "..." {
		return true
	}
	optional := len(tokens) >= 2 && tokens[0].text == "["
	if optional {
		closeIndex := modulaRecoveryMatchingDelimiter(tokens, 0, "[", "]")
		if closeIndex != len(tokens)-1 {
			return false
		}
		tokens = tokens[1:closeIndex]
	}
	if len(tokens) > 0 && tokens[0].text == "VAR" {
		if optional {
			return false
		}
		tokens = tokens[1:]
	}
	colon := modulaRecoveryTopLevelToken(tokens, ":")
	if colon <= 0 || colon+1 >= len(tokens) {
		return false
	}
	ok := modulaRecoveryIdentifierListValid(tokens, 0, colon)
	if !ok || optional && colon != 1 {
		return false
	}
	typeEnd := len(tokens)
	if optional {
		equals := modulaRecoveryTopLevelToken(tokens[colon+1:], "=")
		if definition && equals < 1 {
			return false
		}
		if equals >= 1 {
			equals += colon + 1
			if equals+1 >= len(tokens) {
				return false
			}
			typeEnd = equals
			if !modulaConstantExpressionRangeValid(tokens, equals+1, len(tokens)) {
				return false
			}
		}
	}
	if optional {
		withoutAttribute, ok := modulaWithoutTrailingBareAttribute(
			tokens, colon+1, typeEnd,
		)
		if !ok || withoutAttribute != typeEnd {
			return false
		}
	}
	return modulaRecoveryFormalTypeValid(tokens, colon+1, typeEnd)
}

func modulaRecoveryFormalTypeValid(tokens []modulaToken, start, end int) bool {
	var ok bool
	if end, ok = modulaWithoutTrailingBareAttribute(tokens, start, end); !ok {
		return false
	}
	for start < end && tokens[start].text == "ARRAY" {
		start++
		if start >= end || tokens[start].text != "OF" {
			return false
		}
		start++
	}
	return modulaRecoveryTypeReferenceValid(tokens, start, end)
}

func modulaRecoveryTypeReferenceValid(tokens []modulaToken, start, end int) bool {
	return modulaRecoveryQualifiedIdentifierValid(tokens, start, end)
}

func modulaRecoveryIdentifierListValid(
	tokens []modulaToken,
	start, end int,
) bool {
	if start < 0 || start >= end || end > len(tokens) {
		return false
	}
	for index := start; index < end; index++ {
		if (index-start)%2 == 0 {
			if tokens[index].kind != modulaTokenIdentifier ||
				modulaKeyword(tokens[index].text) {
				return false
			}
		} else if tokens[index].text != "," {
			return false
		}
	}
	return (end-start)%2 == 1
}

func modulaRecoveryQualifiedIdentifierValid(
	tokens []modulaToken,
	start, end int,
) bool {
	if start < 0 || start >= end || end > len(tokens) ||
		tokens[start].kind != modulaTokenIdentifier ||
		modulaKeyword(tokens[start].text) {
		return false
	}
	for index := start + 1; index < end; index += 2 {
		if index+1 >= end || tokens[index].text != "." ||
			tokens[index+1].kind != modulaTokenIdentifier ||
			modulaKeyword(tokens[index+1].text) {
			return false
		}
	}
	return true
}

func modulaRecoveryImportHeaderValid(header []modulaToken) bool {
	if len(header) < 2 {
		return false
	}
	start := 1
	if header[0].text == "FROM" {
		if len(header) < 4 || header[1].kind != modulaTokenIdentifier ||
			modulaKeyword(header[1].text) || header[2].text != "IMPORT" {
			return false
		}
		start = 3
	}
	return modulaImportListValid(header, start, len(header))
}

func modulaRecoveryHeaderOpen(header []modulaToken, blockStart int) bool {
	paren, bracket := 0, 0
	blocks := 0
	for index := 0; index < len(header); index++ {
		token := header[index]
		if token.text == "<*" {
			end := modulaRecoveryDirectiveRangeEnd(header, index, len(header))
			if end < 0 {
				return false
			}
			index = end - 1
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
			if blockStart >= 0 && index >= blockStart {
				blocks++
			}
		case "CASE":
			if blockStart >= 0 && index >= blockStart && blocks > 0 {
				blocks++
			}
		case "END":
			if blocks > 0 {
				blocks--
			}
		}
	}
	return paren > 0 || bracket > 0 || blocks > 0
}

func modulaRecoveryTopLevelToken(tokens []modulaToken, text string) int {
	paren, bracket := 0, 0
	for index := 0; index < len(tokens); index++ {
		token := tokens[index]
		if token.text == "<*" {
			end := modulaRecoveryDirectiveRangeEnd(tokens, index, len(tokens))
			if end < 0 {
				return -1
			}
			index = end - 1
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
		default:
			if paren == 0 && bracket == 0 && token.text == text {
				return index
			}
		}
	}
	return -1
}

func modulaRecoveryMatchingDelimiter(
	tokens []modulaToken,
	start int,
	openToken, closeToken string,
) int {
	depth := 0
	for index := start; index < len(tokens); index++ {
		switch tokens[index].text {
		case openToken:
			depth++
		case closeToken:
			depth--
			if depth == 0 {
				return index
			}
		}
	}
	return -1
}

func modulaRecoveryBalancedRange(
	tokens []modulaToken,
	start int,
	openToken, closeToken string,
) bool {
	return modulaRecoveryMatchingDelimiter(tokens, start, openToken, closeToken) == len(tokens)-1
}

func modulaRecoveryTokenAt(
	tokens []modulaToken,
	start, end int,
) (modulaToken, bool) {
	index := sort.Search(len(tokens), func(index int) bool {
		return tokens[index].start >= start
	})
	if index < len(tokens) && tokens[index].start == start && tokens[index].end == end {
		return tokens[index], true
	}
	return modulaToken{}, false
}

func modulaRecoveryRestartToken(text string) bool {
	switch text {
	case "CONST", "TYPE", "VAR", "PROCEDURE", "MODULE", "IMPORT", "FROM",
		"EXPORT", "BEGIN", "END":
		return true
	default:
		return false
	}
}

func modulaRecoveryOwnerRestartToken(text string) bool {
	switch text {
	case "CONST", "TYPE", "VAR", "PROCEDURE", "MODULE", "IMPORT", "FROM",
		"EXPORT", "BEGIN", "FINALLY":
		return true
	default:
		return false
	}
}

func modulaRecoveryControlStarter(text string) bool {
	switch text {
	case "IF", "CASE", "WHILE", "FOR", "LOOP", "WITH", "REPEAT":
		return true
	default:
		return false
	}
}
