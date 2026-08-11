package repoview

import (
	"sort"
	"strings"
	"unicode/utf8"
)

type cByteSpan struct {
	start int
	end   int
}

type cLineSpan struct {
	start int
	end   int
}

type cLineScope struct {
	start int
	end   int
}

type cTokenKind uint8

const (
	cTokenPunctuation cTokenKind = iota
	cTokenIdentifier
	cTokenNumber
	cTokenLiteral
	cTokenDirective
)

// cToken keeps the logical spelling but the physical byte range. In
// particular, translation-phase line splices are absent from text and remain
// represented by start/end, so all public coordinates stay byte-exact.
type cToken struct {
	text       string
	start, end int
	lineStart  bool
	kind       cTokenKind
	gapBefore  bool
}

type cDirective struct {
	kind, name         string
	start, end         int
	startLine, endLine int
	nameStart, nameEnd int
}

type cLexResult struct {
	source               string
	lineStarts           []int
	commentSpans         []cByteSpan
	stringSpans          []cByteSpan
	syntaxOpaqueSpans    []cByteSpan
	definitions          []sourceDefinition
	trustedDefinitions   map[cDefinitionIdentity]bool
	recoveredDefinitions map[cDefinitionIdentity]bool
	scopes               []cLineScope
	imports              []cLineSpan
	directives           []cDirective
	recoveryLines        map[int]bool
	tokens               []cToken
	lexicalUnits         int
	truncated            bool
	concreteEligible     bool
}

type cConditionalFrame struct {
	line int
}

type cLexicalContext uint8

const (
	cLexicalTranslation cLexicalContext = iota
	cLexicalAggregate
	cLexicalEnum
	cLexicalBlock
)

type cBraceKind uint8

const (
	cBraceBlock cBraceKind = iota
	cBraceInitializer
	cBraceAggregate
	cBraceEnum
	cBraceFunction
)

type cBraceClassification struct {
	kind        cBraceKind
	headerStart int
	nameIndex   int
}

type cDeclaratorCandidate struct {
	index      int
	isFunction bool
}

type cKNRFunction struct {
	headerStart int
	nameIndex   int
}

type cStreamOwner struct {
	prefix      []cToken
	name        cToken
	headerStart int
	kind        cBraceKind
}

type cStreamFrame struct {
	owner        *cStreamOwner
	statement    []cToken
	parenDepth   int
	bracketDepth int
	context      cLexicalContext
	poisoned     bool
}

type cStreamRecovery struct {
	frames            []cStreamFrame
	overflowDepth     int
	expectDirective   bool
	conditionalFrames [cMaximumConcretePreprocessorDepth]int
	conditionalDepth  int
}

type cLexicalResolver struct {
	lexer         *cLexer
	pairs         map[int]int
	knrFunctions  map[int]cKNRFunction
	knrSemicolons map[int]bool
	hasUnmatched  bool
}

type cLexer struct {
	trusted             map[cDefinitionIdentity]bool
	recoveryLines       map[int]bool
	recovered           map[cDefinitionIdentity]bool
	source              string
	streamDefinitions   []sourceDefinition
	tokens              []cToken
	directives          []cDirective
	scopes              []cLineScope
	definitions         []sourceDefinition
	opaque              []cByteSpan
	strings             []cByteSpan
	comments            []cByteSpan
	lineStarts          []int
	streamScopes        []cLineScope
	tailTokens          []cToken
	imports             []cLineSpan
	stream              cStreamRecovery
	conditionalFrames   [cMaximumConcretePreprocessorDepth]cConditionalFrame
	tailStart           int
	lexicalUnits        int
	conditionalDepth    int
	conditionalOverflow int
	truncated           bool
	resyncNext          bool
	concreteEligible    bool
}

const cRetainedTokenHead = cMaximumRetainedLexicalUnits / 2
const cMaximumStreamingStatementTokens = 4096

func lexC(source string) cLexResult {
	lexer := &cLexer{
		source:           source,
		lineStarts:       cLineStarts(source),
		trusted:          make(map[cDefinitionIdentity]bool),
		recovered:        make(map[cDefinitionIdentity]bool),
		recoveryLines:    make(map[int]bool),
		concreteEligible: len(source) <= cMaximumConcreteParseBytes,
	}
	lexer.scan()
	lexer.finishTokens()
	lexer.applyConcreteTokenGates()
	lexer.resolveScopesAndDefinitions()

	return cLexResult{
		source:               source,
		lineStarts:           lexer.lineStarts,
		commentSpans:         normalizeCSpans(lexer.comments),
		stringSpans:          normalizeCSpans(lexer.strings),
		syntaxOpaqueSpans:    normalizeCSpans(lexer.opaque),
		definitions:          cSortLexicalDefinitions(lexer.definitions),
		trustedDefinitions:   lexer.trusted,
		recoveredDefinitions: lexer.recovered,
		scopes:               cSortLineScopes(lexer.scopes),
		imports:              cSortLineSpans(lexer.imports),
		directives:           lexer.directives,
		recoveryLines:        lexer.recoveryLines,
		tokens:               lexer.tokens,
		lexicalUnits:         lexer.lexicalUnits,
		truncated:            lexer.truncated,
		concreteEligible:     lexer.concreteEligible,
	}
}

func cLineStarts(source string) []int {
	starts := make([]int, 1, strings.Count(source, "\n")+1)
	starts[0] = 0
	for offset := range len(source) {
		if source[offset] == '\n' {
			starts = append(starts, offset+1)
		}
	}
	return starts
}

func normalizeCSpans(spans []cByteSpan) []cByteSpan {
	if len(spans) == 0 {
		return nil
	}
	spans = append([]cByteSpan(nil), spans...)
	sort.Slice(spans, func(first, second int) bool {
		if spans[first].start != spans[second].start {
			return spans[first].start < spans[second].start
		}
		return spans[first].end < spans[second].end
	})
	normalized := spans[:0]
	for _, span := range spans {
		if span.start < 0 {
			span.start = 0
		}
		if span.end <= span.start {
			continue
		}
		if len(normalized) > 0 && span.start <= normalized[len(normalized)-1].end {
			normalized[len(normalized)-1].end = max(
				normalized[len(normalized)-1].end,
				span.end,
			)
			continue
		}
		normalized = append(normalized, span)
	}
	return normalized
}

func maskCSource(source string, spans []cByteSpan) string {
	if source == "" || len(spans) == 0 {
		return source
	}
	masked := []byte(source)
	for _, span := range normalizeCSpans(spans) {
		start := min(max(span.start, 0), len(masked))
		end := min(max(span.end, start), len(masked))
		for offset := start; offset < end; offset++ {
			if masked[offset] != '\n' && masked[offset] != '\r' {
				masked[offset] = ' '
			}
		}
	}
	return string(masked)
}

func (lexer *cLexer) scan() {
	lineHasToken := false
	for offset := 0; offset < len(lexer.source); {
		if splice := cSpliceLength(lexer.source, offset); splice > 0 {
			offset += splice
			continue
		}
		if offset == 0 && strings.HasPrefix(lexer.source, "\uFEFF") {
			offset += len("\uFEFF")
			continue
		}

		switch lexer.source[offset] {
		case ' ', '\t', '\v', '\f':
			offset++
			continue
		case '\r':
			lineHasToken = false
			offset++
			if offset < len(lexer.source) && lexer.source[offset] == '\n' {
				offset++
			}
			continue
		case '\n':
			lineHasToken = false
			offset++
			continue
		}

		if !lineHasToken {
			if markerEnd, ok := cDirectiveMarkerEnd(lexer.source, offset); ok {
				directiveStart := offset
				offset = lexer.scanDirective(offset, markerEnd)
				lineHasToken = !cContainsUnsplicedNewline(
					lexer.source, directiveStart, offset,
				)
				continue
			}
		}

		if end, kind, ok := cCommentEnd(lexer.source, offset, len(lexer.source)); ok {
			lexer.comments = append(lexer.comments, cByteSpan{start: offset, end: end})
			if kind == cLineComment {
				offset = end
				continue
			}
			if end == len(lexer.source) && !cLogicalSuffix(lexer.source, offset, end, "*/") {
				lexer.recoveryLines[lexer.lineAt(offset)] = true
			}
			if cContainsUnsplicedNewline(lexer.source, offset, end) {
				lineHasToken = false
			}
			offset = end
			continue
		}

		lineStart := !lineHasToken
		if end, ok := cLiteralEnd(lexer.source, offset, len(lexer.source)); ok {
			lexer.strings = append(lexer.strings, cByteSpan{start: offset, end: end})
			lexer.appendToken(cToken{
				text: cLogicalText(lexer.source, offset, end), start: offset, end: end,
				lineStart: lineStart, kind: cTokenLiteral,
			})
			if cLiteralEndedAtNewline(lexer.source, offset, end) {
				line := lexer.lineAt(offset)
				lexer.recoveryLines[line] = true
				if line+1 <= len(lexer.lineStarts) {
					lexer.recoveryLines[line+1] = true
				}
				lexer.resyncNext = true
			}
			offset = end
			lineHasToken = true
			continue
		}

		if end := cLogicalIdentifierEnd(lexer.source, offset); end > offset {
			lexer.appendToken(cToken{
				text: cLogicalText(lexer.source, offset, end), start: offset, end: end,
				lineStart: lineStart, kind: cTokenIdentifier,
			})
			offset = end
			lineHasToken = true
			continue
		}
		if cLogicalNumberStart(lexer.source, offset) {
			end := cLogicalNumberEnd(lexer.source, offset)
			lexer.appendToken(cToken{
				text: cLogicalText(lexer.source, offset, end), start: offset, end: end,
				lineStart: lineStart, kind: cTokenNumber,
			})
			offset = end
			lineHasToken = true
			continue
		}

		text, end := cPunctuationAt(lexer.source, offset)
		lexer.appendToken(cToken{
			text: text, start: offset, end: end, lineStart: lineStart,
			kind: cTokenPunctuation,
		})
		offset = end
		lineHasToken = true
	}
}

func cContainsUnsplicedNewline(source string, start, end int) bool {
	for offset := start; offset < end; {
		if splice := cSpliceLength(source, offset); splice > 0 && offset+splice <= end {
			offset += splice
			continue
		}
		if source[offset] == '\n' || source[offset] == '\r' {
			return true
		}
		offset++
	}
	return false
}

func (lexer *cLexer) appendToken(token cToken) {
	if lexer.resyncNext {
		token.gapBefore = true
		lexer.resyncNext = false
	}
	lexer.streamToken(token)
	lexer.lexicalUnits++
	if lexer.lexicalUnits > cMaximumConcreteLexicalUnits {
		lexer.concreteEligible = false
	}
	if !lexer.truncated && len(lexer.tokens) < cMaximumRetainedLexicalUnits {
		lexer.tokens = append(lexer.tokens, token)
		return
	}
	if !lexer.truncated {
		lexer.truncated = true
		lexer.tailTokens = append(
			make([]cToken, 0, cMaximumRetainedLexicalUnits-cRetainedTokenHead),
			lexer.tokens[cRetainedTokenHead:]...,
		)
		lexer.tokens = lexer.tokens[:cRetainedTokenHead]
	}
	if len(lexer.tailTokens) < cMaximumRetainedLexicalUnits-cRetainedTokenHead {
		lexer.tailTokens = append(lexer.tailTokens, token)
		return
	}
	lexer.tailTokens[lexer.tailStart] = token
	lexer.tailStart = (lexer.tailStart + 1) % len(lexer.tailTokens)
}

func (lexer *cLexer) streamToken(token cToken) {
	stream := &lexer.stream
	if len(stream.frames) == 0 {
		stream.frames = append(stream.frames, cStreamFrame{context: cLexicalTranslation})
	}
	if token.kind == cTokenDirective {
		lexer.streamDirectiveToken(token)
		return
	}
	stream.expectDirective = false
	if token.gapBefore {
		lexer.resetStreamStatement()
	}
	if stream.overflowDepth > 0 {
		switch token.text {
		case "{":
			stream.overflowDepth++
		case "}":
			stream.overflowDepth--
			if stream.overflowDepth == 0 {
				lexer.resetStreamStatement()
			}
		}
		return
	}

	frame := &stream.frames[len(stream.frames)-1]
	if token.text == "{" {
		if len(stream.frames) >= cMaximumConcreteDelimiterDepth+1 {
			stream.overflowDepth = 1
			frame.statement = frame.statement[:0]
			frame.poisoned = true
			return
		}
		lexer.startStreamFrame(token)
		return
	}
	if token.text == "}" {
		if len(stream.frames) == 1 {
			frame.statement = frame.statement[:0]
			frame.poisoned = true
			return
		}
		lexer.finishStreamFrame(token)
		return
	}

	frame = &stream.frames[len(stream.frames)-1]
	if token.text == ";" && frame.parenDepth == 0 && frame.bracketDepth == 0 {
		if !frame.poisoned {
			lexer.extractStreamStatement(frame.statement, frame.context)
		}
		frame.statement = frame.statement[:0]
		frame.poisoned = false
		return
	}
	if frame.context == cLexicalEnum && token.text == "," && frame.parenDepth == 0 &&
		frame.bracketDepth == 0 {
		if !frame.poisoned {
			lexer.extractStreamEnumerators(frame.statement)
		}
		frame.statement = frame.statement[:0]
		frame.poisoned = false
		return
	}
	if frame.poisoned {
		return
	}
	if len(frame.statement) >= cMaximumStreamingStatementTokens {
		frame.statement = frame.statement[:0]
		frame.poisoned = true
		return
	}
	frame.statement = append(frame.statement, token)
	switch token.text {
	case "(":
		frame.parenDepth++
	case ")":
		frame.parenDepth = max(0, frame.parenDepth-1)
	case "[":
		frame.bracketDepth++
	case "]":
		frame.bracketDepth = max(0, frame.bracketDepth-1)
	}
}

func (lexer *cLexer) streamDirectiveToken(token cToken) {
	stream := &lexer.stream
	lexer.resetStreamStatement()
	if token.text == "#" {
		stream.expectDirective = true
		return
	}
	if !stream.expectDirective {
		return
	}
	stream.expectDirective = false
	switch token.text {
	case "if", "ifdef", "ifndef":
		if stream.conditionalDepth < len(stream.conditionalFrames) {
			stream.conditionalFrames[stream.conditionalDepth] = len(stream.frames)
			stream.conditionalDepth++
		}
	case "else", "elif", "elifdef", "elifndef":
		if stream.conditionalDepth > 0 {
			lexer.restoreStreamFrames(
				stream.conditionalFrames[stream.conditionalDepth-1],
			)
		}
	case "endif":
		if stream.conditionalDepth > 0 {
			lexer.restoreStreamFrames(
				stream.conditionalFrames[stream.conditionalDepth-1],
			)
			stream.conditionalDepth--
		}
	}
}

func (lexer *cLexer) restoreStreamFrames(depth int) {
	stream := &lexer.stream
	depth = min(max(depth, 1), len(stream.frames))
	stream.frames = stream.frames[:depth]
	stream.overflowDepth = 0
	lexer.resetStreamStatement()
}

func (lexer *cLexer) resetStreamStatement() {
	stream := &lexer.stream
	if len(stream.frames) == 0 {
		return
	}
	frame := &stream.frames[len(stream.frames)-1]
	frame.statement = frame.statement[:0]
	frame.parenDepth = 0
	frame.bracketDepth = 0
	frame.poisoned = false
}

func (lexer *cLexer) startStreamFrame(open cToken) {
	stream := &lexer.stream
	parent := &stream.frames[len(stream.frames)-1]
	tokens := make([]cToken, 0, len(parent.statement)+1)
	tokens = append(tokens, parent.statement...)
	tokens = append(tokens, open)
	classification := cBraceClassification{
		kind: cBraceBlock, headerStart: len(tokens) - 1, nameIndex: -1,
	}
	if !parent.poisoned && len(parent.statement) > 0 {
		temporary := &cLexer{
			source: lexer.source, lineStarts: lexer.lineStarts, tokens: tokens,
			trusted:       make(map[cDefinitionIdentity]bool),
			recovered:     make(map[cDefinitionIdentity]bool),
			recoveryLines: make(map[int]bool),
		}
		pairs, _ := cLexicalDelimiterPairs(tokens)
		resolver := &cLexicalResolver{lexer: temporary, pairs: pairs}
		classification = resolver.classifyBrace(0, len(tokens)-1)
	}
	headerStart := open.start
	if len(parent.statement) > 0 {
		headerStart = parent.statement[0].start
	}
	owner := &cStreamOwner{
		kind: classification.kind, headerStart: headerStart,
		prefix: append([]cToken(nil), parent.statement...),
	}
	if classification.nameIndex >= 0 && classification.nameIndex < len(tokens) {
		owner.name = tokens[classification.nameIndex]
	}
	var context cLexicalContext
	switch classification.kind {
	case cBraceAggregate:
		context = cLexicalAggregate
	case cBraceEnum:
		context = cLexicalEnum
	case cBraceBlock, cBraceInitializer, cBraceFunction:
		context = cLexicalBlock
	}
	parent.statement = parent.statement[:0]
	parent.parenDepth = 0
	parent.bracketDepth = 0
	parent.poisoned = false
	stream.frames = append(stream.frames, cStreamFrame{
		context: context,
		owner:   owner,
	})
}

func (lexer *cLexer) finishStreamFrame(closingToken cToken) {
	stream := &lexer.stream
	frameIndex := len(stream.frames) - 1
	frame := &stream.frames[frameIndex]
	if frame.context == cLexicalEnum && !frame.poisoned {
		lexer.extractStreamEnumerators(frame.statement)
	}
	owner := frame.owner
	stream.frames = stream.frames[:frameIndex]
	if owner == nil {
		lexer.resetStreamStatement()
		return
	}
	scopeStart := lexer.lineAt(lexer.attachedDocumentationStart(owner.headerStart))
	scopeEnd := lexer.lineAt(max(closingToken.end-1, 0))
	if scopeStart > 0 && scopeEnd >= scopeStart {
		lexer.streamScopes = append(lexer.streamScopes, cLineScope{
			start: scopeStart, end: scopeEnd,
		})
	}
	if owner.kind == cBraceFunction || owner.kind == cBraceAggregate || owner.kind == cBraceEnum {
		if owner.name.kind == cTokenIdentifier && cSourceIdentifier(owner.name.text) &&
			!cKeyword(owner.name.text) {
			line, column := lexer.lineColumn(owner.name.start)
			ownedEndLine, ownedEndColumn := lexer.lineColumn(closingToken.end)
			if ownedEndLine != scopeEnd {
				ownedEndColumn = 0
			}
			lexer.streamDefinitions = append(lexer.streamDefinitions, sourceDefinition{
				symbol: owner.name.text, line: line, column: column,
				scopeStart: scopeStart, scopeEnd: scopeEnd,
				ownedEndColumn: ownedEndColumn, ownsScope: true,
			})
		}
	}
	parent := &stream.frames[len(stream.frames)-1]
	parent.statement = parent.statement[:0]
	parent.parenDepth = 0
	parent.bracketDepth = 0
	parent.poisoned = false
	if owner.kind != cBraceFunction && owner.kind != cBraceBlock {
		if len(owner.prefix) > cMaximumStreamingStatementTokens {
			parent.poisoned = true
			return
		}
		parent.statement = append(parent.statement, owner.prefix...)
	}
}

func (lexer *cLexer) extractStreamStatement(
	tokens []cToken,
	context cLexicalContext,
) {
	if len(tokens) == 0 || !cStatementTypeEvidence(tokens, 0, len(tokens)) {
		return
	}
	tokens = append([]cToken(nil), tokens...)
	temporary := &cLexer{
		source: lexer.source, lineStarts: lexer.lineStarts, tokens: tokens,
		trusted:       make(map[cDefinitionIdentity]bool),
		recovered:     make(map[cDefinitionIdentity]bool),
		recoveryLines: make(map[int]bool),
	}
	pairs, unmatched := cLexicalDelimiterPairs(tokens)
	if unmatched {
		return
	}
	resolver := &cLexicalResolver{lexer: temporary, pairs: pairs}
	resolver.extractStatement(0, len(tokens), context)
	lexer.streamDefinitions = append(lexer.streamDefinitions, temporary.definitions...)
}

func (lexer *cLexer) extractStreamEnumerators(tokens []cToken) {
	if len(tokens) == 0 {
		return
	}
	tokens = append([]cToken(nil), tokens...)
	temporary := &cLexer{
		source: lexer.source, lineStarts: lexer.lineStarts, tokens: tokens,
		trusted:       make(map[cDefinitionIdentity]bool),
		recovered:     make(map[cDefinitionIdentity]bool),
		recoveryLines: make(map[int]bool),
	}
	pairs, unmatched := cLexicalDelimiterPairs(tokens)
	if unmatched {
		return
	}
	resolver := &cLexicalResolver{lexer: temporary, pairs: pairs}
	resolver.extractEnumerators(0, len(tokens))
	lexer.streamDefinitions = append(lexer.streamDefinitions, temporary.definitions...)
}

func (lexer *cLexer) finishTokens() {
	if !lexer.truncated {
		return
	}
	ordered := make([]cToken, 0, cMaximumRetainedLexicalUnits)
	ordered = append(ordered, lexer.tokens...)
	ordered = append(ordered, lexer.tailTokens[lexer.tailStart:]...)
	ordered = append(ordered, lexer.tailTokens[:lexer.tailStart]...)
	if len(ordered) > cRetainedTokenHead {
		ordered[cRetainedTokenHead].gapBefore = true
		lexer.recoveryLines[lexer.lineAt(ordered[cRetainedTokenHead].start)] = true
	}
	lexer.tokens = ordered
	lexer.tailTokens = nil
	lexer.concreteEligible = false
}

type cCommentKind uint8

const (
	cBlockComment cCommentKind = iota
	cLineComment
)

func cCommentEnd(source string, start, limit int) (int, cCommentKind, bool) {
	if end, ok := cMatchLogical(source, start, "//", limit); ok {
		for offset := end; offset < limit; {
			if splice := cSpliceLength(source, offset); splice > 0 {
				offset += splice
				continue
			}
			if source[offset] == '\n' || source[offset] == '\r' {
				return offset, cLineComment, true
			}
			_, size := utf8.DecodeRuneInString(source[offset:limit])
			if size < 1 {
				size = 1
			}
			offset += size
		}
		return limit, cLineComment, true
	}
	if end, ok := cMatchLogical(source, start, "/*", limit); ok {
		for offset := end; offset < limit; {
			if closeEnd, closes := cMatchLogical(source, offset, "*/", limit); closes {
				return closeEnd, cBlockComment, true
			}
			if splice := cSpliceLength(source, offset); splice > 0 {
				offset += splice
				continue
			}
			_, size := utf8.DecodeRuneInString(source[offset:limit])
			if size < 1 {
				size = 1
			}
			offset += size
		}
		return limit, cBlockComment, true
	}
	return start, 0, false
}

func cLiteralEnd(source string, start, limit int) (int, bool) {
	quoteStart := start
	for _, prefix := range []string{"u8", "L", "u", "U"} {
		if end, ok := cMatchLogical(source, start, prefix, limit); ok {
			logical := cSkipSplices(source, end, limit)
			if logical < limit && (source[logical] == '\'' || source[logical] == '"') {
				quoteStart = logical
				break
			}
		}
	}
	if quoteStart >= limit || source[quoteStart] != '\'' && source[quoteStart] != '"' {
		return start, false
	}
	quote := source[quoteStart]
	for offset := quoteStart + 1; offset < limit; {
		if splice := cSpliceLength(source, offset); splice > 0 {
			offset += splice
			continue
		}
		if source[offset] == '\n' || source[offset] == '\r' {
			return offset, true
		}
		if source[offset] == quote {
			return offset + 1, true
		}
		if source[offset] == '\\' {
			offset++
			offset = cSkipSplices(source, offset, limit)
			if offset < limit {
				_, size := utf8.DecodeRuneInString(source[offset:limit])
				if size < 1 {
					size = 1
				}
				offset += size
			}
			continue
		}
		_, size := utf8.DecodeRuneInString(source[offset:limit])
		if size < 1 {
			size = 1
		}
		offset += size
	}
	return limit, true
}

func cLiteralEndedAtNewline(source string, start, end int) bool {
	return end < len(source) && end >= start &&
		(source[end] == '\n' || source[end] == '\r')
}

func cSpliceLength(source string, offset int) int {
	if offset < 0 || offset+1 >= len(source) || source[offset] != '\\' {
		return 0
	}
	switch source[offset+1] {
	case '\n':
		return 2
	case '\r':
		if offset+2 < len(source) && source[offset+2] == '\n' {
			return 3
		}
		return 2
	default:
		return 0
	}
}

func cSkipSplices(source string, offset, limit int) int {
	for offset < limit {
		splice := cSpliceLength(source, offset)
		if splice == 0 || offset+splice > limit {
			break
		}
		offset += splice
	}
	return offset
}

func cMatchLogical(source string, start int, want string, limit int) (int, bool) {
	offset := start
	for index := range len(want) {
		offset = cSkipSplices(source, offset, limit)
		if offset >= limit || source[offset] != want[index] {
			return start, false
		}
		offset++
	}
	return offset, true
}

func cLogicalSuffix(source string, start, end int, suffix string) bool {
	if start < 0 || end < start || end > len(source) {
		return false
	}
	return strings.HasSuffix(cLogicalText(source, start, end), suffix)
}

func cLogicalText(source string, start, end int) string {
	if start < 0 || end < start || end > len(source) {
		return ""
	}
	firstSplice := -1
	for offset := start; offset < end; offset++ {
		if cSpliceLength(source, offset) > 0 {
			firstSplice = offset
			break
		}
	}
	if firstSplice < 0 {
		return source[start:end]
	}
	var logical strings.Builder
	logical.Grow(end - start)
	logical.WriteString(source[start:firstSplice])
	for offset := firstSplice; offset < end; {
		if splice := cSpliceLength(source, offset); splice > 0 && offset+splice <= end {
			offset += splice
			continue
		}
		logical.WriteByte(source[offset])
		offset++
	}
	return logical.String()
}

func cLogicalIdentifierEnd(source string, start int) int {
	offset := start
	physicalEnd := start
	first := true
	for {
		unitStart := cSkipSplices(source, offset, len(source))
		if unitStart >= len(source) {
			break
		}
		end, ok := cLogicalIdentifierUnit(source, unitStart, first)
		if !ok {
			break
		}
		physicalEnd = end
		offset = end
		first = false
	}
	return physicalEnd
}

func cLogicalIdentifierUnit(source string, start int, first bool) (int, bool) {
	if character, end, ok := cLogicalUCN(source, start); ok {
		return end, cIdentifierRune(character, first)
	}
	if start < 0 || start >= len(source) {
		return start, false
	}
	character, size := utf8.DecodeRuneInString(source[start:])
	if character == utf8.RuneError && size == 1 {
		return start + 1, false
	}
	return start + size, cIdentifierRune(character, first)
}

func cLogicalUCN(source string, start int) (rune, int, bool) {
	offset, ok := cMatchLogical(source, start, "\\", len(source))
	if !ok {
		return 0, start, false
	}
	offset = cSkipSplices(source, offset, len(source))
	if offset >= len(source) || source[offset] != 'u' && source[offset] != 'U' {
		return 0, start, false
	}
	digitCount := 4
	if source[offset] == 'U' {
		digitCount = 8
	}
	offset++
	value := rune(0)
	for range digitCount {
		offset = cSkipSplices(source, offset, len(source))
		if offset >= len(source) {
			return 0, start, false
		}
		digit := source[offset]
		value <<= 4
		switch {
		case digit >= '0' && digit <= '9':
			value += rune(digit - '0')
		case digit >= 'a' && digit <= 'f':
			value += rune(digit-'a') + 10
		case digit >= 'A' && digit <= 'F':
			value += rune(digit-'A') + 10
		default:
			return 0, start, false
		}
		offset++
	}
	if value > utf8.MaxRune || value >= 0xD800 && value <= 0xDFFF {
		return 0, start, false
	}
	return value, offset, true
}

func cLogicalNumberStart(source string, offset int) bool {
	if offset < 0 || offset >= len(source) {
		return false
	}
	if source[offset] >= '0' && source[offset] <= '9' {
		return true
	}
	if source[offset] != '.' {
		return false
	}
	next := cSkipSplices(source, offset+1, len(source))
	return next < len(source) && source[next] >= '0' && source[next] <= '9'
}

func cLogicalNumberEnd(source string, start int) int {
	offset := start
	physicalEnd := start
	previous := byte(0)
	for {
		unitStart := cSkipSplices(source, offset, len(source))
		if unitStart >= len(source) {
			break
		}
		character := source[unitStart]
		allowed := character >= '0' && character <= '9' ||
			character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character == '_' || character == '.' || character == '$' || character == '\'' ||
			(character == '+' || character == '-') &&
				(previous == 'e' || previous == 'E' || previous == 'p' || previous == 'P')
		if allowed {
			previous = character
			physicalEnd = unitStart + 1
			offset = physicalEnd
			continue
		}
		if end, ok := cLogicalIdentifierUnit(source, unitStart, false); ok {
			physicalEnd = end
			offset = end
			previous = 0
			continue
		}
		break
	}
	return max(start+1, physicalEnd)
}

var cPunctuators = []struct {
	logical   string
	canonical string
}{
	{"%:%:", "##"}, {">>=", ">>="}, {"<<=", "<<="}, {"...", "..."},
	{"->", "->"}, {"++", "++"}, {"--", "--"}, {"<<", "<<"}, {">>", ">>"},
	{"<=", "<="}, {">=", ">="}, {"==", "=="}, {"!=", "!="}, {"&&", "&&"},
	{"||", "||"}, {"*=", "*="}, {"/=", "/="}, {"%=", "%="}, {"+=", "+="},
	{"-=", "-="}, {"&=", "&="}, {"^=", "^="}, {"|=", "|="}, {"##", "##"},
	{"<:", "["}, {":>", "]"}, {"<%", "{"}, {"%>", "}"}, {"%:", "#"},
}

func cPunctuationAt(source string, start int) (string, int) {
	for _, candidate := range cPunctuators {
		if end, ok := cMatchLogical(source, start, candidate.logical, len(source)); ok {
			return candidate.canonical, end
		}
	}
	_, size := utf8.DecodeRuneInString(source[start:])
	if size < 1 {
		size = 1
	}
	return source[start : start+size], start + size
}

func cDirectiveMarkerEnd(source string, start int) (int, bool) {
	if end, ok := cMatchLogical(source, start, "%:", len(source)); ok {
		return end, true
	}
	if start < len(source) && source[start] == '#' {
		return start + 1, true
	}
	return start, false
}

func cLogicalLineEnd(source string, start int) int {
	for offset := start; offset < len(source); {
		if splice := cSpliceLength(source, offset); splice > 0 {
			offset += splice
			continue
		}
		if source[offset] == '\n' || source[offset] == '\r' {
			return offset
		}
		_, size := utf8.DecodeRuneInString(source[offset:])
		if size < 1 {
			size = 1
		}
		offset += size
	}
	return len(source)
}

func cDirectiveTriviaEnd(source string, start, limit int) int {
	offset := start
	for offset < limit {
		offset = cSkipSplices(source, offset, limit)
		if offset >= limit {
			break
		}
		switch source[offset] {
		case ' ', '\t', '\v', '\f':
			offset++
			continue
		}
		if end, kind, ok := cCommentEnd(source, offset, limit); ok {
			if kind == cLineComment {
				return limit
			}
			offset = end
			continue
		}
		break
	}
	return offset
}

func (lexer *cLexer) scanDirective(start, markerEnd int) int {
	end := cLogicalLineEnd(lexer.source, markerEnd)
	keywordStart := cDirectiveTriviaEnd(lexer.source, markerEnd, end)
	keywordEnd := cLogicalIdentifierEndWithin(lexer.source, keywordStart, end)
	kind := cLogicalText(lexer.source, keywordStart, keywordEnd)
	startLine, endLine := lexer.lineAt(start), lexer.lineAt(max(start, end-1))
	directive := cDirective{
		kind: kind, start: start, end: end, startLine: startLine, endLine: endLine,
	}

	lexer.appendToken(cToken{
		text: "#", start: start, end: markerEnd, lineStart: true, kind: cTokenDirective,
	})
	if keywordEnd > keywordStart {
		lexer.appendToken(cToken{
			text: kind, start: keywordStart, end: keywordEnd, kind: cTokenDirective,
		})
	}

	operandStart := cDirectiveTriviaEnd(lexer.source, keywordEnd, end)
	macroNameEnd := -1
	if kind == "define" {
		nameEnd := cLogicalIdentifierEndWithin(lexer.source, operandStart, end)
		if nameEnd > operandStart {
			macroNameEnd = nameEnd
			directive.name = cLogicalText(lexer.source, operandStart, nameEnd)
			directive.nameStart, directive.nameEnd = operandStart, nameEnd
			lexer.appendToken(cToken{
				text: directive.name, start: operandStart, end: nameEnd, kind: cTokenDirective,
			})
			line, column := lexer.lineColumn(operandStart)
			definition := sourceDefinition{
				symbol: directive.name, line: line, column: column,
				scopeStart: line, scopeEnd: endLine,
			}
			lexer.addDefinition(definition, true, false)
			if nameEnd < end {
				lexer.opaque = append(lexer.opaque, cByteSpan{start: nameEnd, end: end})
			}
		}
	} else if operandStart < end {
		lexer.opaque = append(lexer.opaque, cByteSpan{start: operandStart, end: end})
	}
	lexer.gateDirectiveStructure(kind, operandStart, macroNameEnd, end)

	protectedHeader := cByteSpan{}
	switch kind {
	case "include", "include_next", "embed":
		lexer.imports = append(lexer.imports, cLineSpan{start: startLine, end: endLine})
		if headerEnd, ok := cAngleHeaderEnd(lexer.source, operandStart, end); ok {
			protectedHeader = cByteSpan{start: operandStart, end: headerEnd}
			lexer.strings = append(lexer.strings, protectedHeader)
		}
	case "if", "ifdef", "ifndef":
		lexer.openConditional(startLine)
	case "endif":
		lexer.closeConditional(endLine)
	}
	opaqueEnd := lexer.scanDirectiveOpaque(markerEnd, end, protectedHeader)
	lexer.directives = append(lexer.directives, directive)
	return max(end, opaqueEnd)
}

func (lexer *cLexer) gateDirectiveStructure(
	kind string,
	operandStart, macroNameEnd, end int,
) {
	switch kind {
	case "if", "elif":
		lexer.gateDirectiveTokens(operandStart, end)
	case "define":
		if macroNameEnd < 0 {
			return
		}
		open := cSkipSplices(lexer.source, macroNameEnd, end)
		if open >= end || lexer.source[open] != '(' {
			return
		}
		parameterEnd, balanced := cDirectiveParameterEnd(lexer.source, open, end)
		lexer.gateDirectiveTokens(open, parameterEnd)
		if cDirectiveParameterCount(lexer.source, open, parameterEnd) >
			cMaximumConcreteGroupsPerSegment {
			lexer.concreteEligible = false
		}
		if !balanced {
			lexer.concreteEligible = false
		}
	}
}

func cDirectiveParameterCount(source string, open, end int) int {
	depth, parameters := 0, 0
	hasCurrent := false
	for offset := open; offset < end; {
		if splice := cSpliceLength(source, offset); splice > 0 {
			offset += splice
			continue
		}
		if commentEnd, _, ok := cCommentEnd(source, offset, end); ok {
			offset = commentEnd
			continue
		}
		if literalEnd, ok := cLiteralEnd(source, offset, end); ok {
			if depth == 1 {
				hasCurrent = true
			}
			offset = literalEnd
			continue
		}
		switch source[offset] {
		case '(':
			depth++
			if depth > 1 {
				hasCurrent = true
			}
		case ')':
			if depth == 1 && hasCurrent {
				parameters++
				hasCurrent = false
			}
			depth = max(0, depth-1)
		case ',':
			if depth == 1 {
				parameters++
				hasCurrent = false
			}
		default:
			if depth == 1 && source[offset] != ' ' && source[offset] != '\t' &&
				source[offset] != '\v' && source[offset] != '\f' {
				hasCurrent = true
			}
		}
		_, size := utf8.DecodeRuneInString(source[offset:end])
		if size < 1 {
			size = 1
		}
		offset += size
	}
	return parameters
}

func cDirectiveParameterEnd(source string, open, limit int) (int, bool) {
	depth := 0
	for offset := open; offset < limit; {
		if splice := cSpliceLength(source, offset); splice > 0 {
			offset += splice
			continue
		}
		if commentEnd, _, ok := cCommentEnd(source, offset, limit); ok {
			offset = commentEnd
			continue
		}
		if literalEnd, ok := cLiteralEnd(source, offset, limit); ok {
			offset = literalEnd
			continue
		}
		switch source[offset] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return offset + 1, true
			}
		}
		_, size := utf8.DecodeRuneInString(source[offset:limit])
		if size < 1 {
			size = 1
		}
		offset += size
	}
	return limit, false
}

func (lexer *cLexer) gateDirectiveTokens(start, end int) {
	var stack [cMaximumConcreteDelimiterDepth]byte
	depth, overflow := 0, 0
	groups, prefix, ternaries := 0, 0, 0
	for offset := start; offset < end; {
		if splice := cSpliceLength(lexer.source, offset); splice > 0 {
			offset += splice
			continue
		}
		switch lexer.source[offset] {
		case ' ', '\t', '\v', '\f', '\r', '\n':
			offset++
			continue
		}
		if commentEnd, _, ok := cCommentEnd(lexer.source, offset, end); ok {
			offset = commentEnd
			continue
		}
		if literalEnd, ok := cLiteralEnd(lexer.source, offset, end); ok {
			offset = literalEnd
			continue
		}

		var text string
		if tokenEnd := cLogicalIdentifierEnd(lexer.source, offset); tokenEnd > offset &&
			tokenEnd <= end {
			text = cLogicalText(lexer.source, offset, tokenEnd)
			offset = tokenEnd
		} else if cLogicalNumberStart(lexer.source, offset) {
			tokenEnd := min(cLogicalNumberEnd(lexer.source, offset), end)
			text = cLogicalText(lexer.source, offset, tokenEnd)
			offset = tokenEnd
		} else {
			text, offset = cPunctuationAt(lexer.source, offset)
			if offset > end {
				offset = end
			}
		}
		lexer.lexicalUnits++
		if lexer.lexicalUnits > cMaximumConcreteLexicalUnits {
			lexer.concreteEligible = false
		}

		switch text {
		case "(", "[", "{":
			if text == "{" {
				groups = 0
			} else {
				groups++
				if groups > cMaximumConcreteGroupsPerSegment {
					lexer.concreteEligible = false
				}
			}
			if depth >= len(stack) {
				overflow++
				lexer.concreteEligible = false
			} else {
				stack[depth] = text[0]
				depth++
			}
		case ")", "]", "}":
			if overflow > 0 {
				overflow--
			} else if depth > 0 && cDelimiterCloses(stack[depth-1], text[0]) {
				depth--
			}
		}
		if cExpressionPrefixToken(text) {
			prefix++
			if prefix > cMaximumConcreteExpressionPrefix {
				lexer.concreteEligible = false
			}
		} else {
			prefix = 0
		}
		if text == "?" {
			ternaries++
			if ternaries > cMaximumConcreteExpressionPrefix {
				lexer.concreteEligible = false
			}
		}
	}
}

func cAngleHeaderEnd(source string, start, limit int) (int, bool) {
	start = cSkipSplices(source, start, limit)
	if start >= limit || source[start] != '<' {
		return start, false
	}
	for offset := start + 1; offset < limit; {
		if splice := cSpliceLength(source, offset); splice > 0 {
			offset += splice
			continue
		}
		if source[offset] == '>' {
			return offset + 1, true
		}
		offset++
	}
	return start, false
}

func cLogicalIdentifierEndWithin(source string, start, limit int) int {
	end := cLogicalIdentifierEnd(source, start)
	if end > limit {
		return start
	}
	return end
}

func (lexer *cLexer) scanDirectiveOpaque(start, end int, protected cByteSpan) int {
	consumedEnd := end
	for offset := start; offset < end; {
		if splice := cSpliceLength(lexer.source, offset); splice > 0 {
			offset += splice
			continue
		}
		if protected.start <= offset && offset < protected.end {
			offset = protected.end
			continue
		}
		if commentEnd, _, ok := cCommentEnd(lexer.source, offset, len(lexer.source)); ok {
			lexer.comments = append(lexer.comments, cByteSpan{start: offset, end: commentEnd})
			consumedEnd = max(consumedEnd, commentEnd)
			offset = commentEnd
			continue
		}
		if literalEnd, ok := cLiteralEnd(lexer.source, offset, end); ok {
			lexer.strings = append(lexer.strings, cByteSpan{start: offset, end: literalEnd})
			offset = literalEnd
			continue
		}
		_, size := utf8.DecodeRuneInString(lexer.source[offset:end])
		if size < 1 {
			size = 1
		}
		offset += size
	}
	return consumedEnd
}

func (lexer *cLexer) openConditional(line int) {
	if lexer.conditionalOverflow > 0 || lexer.conditionalDepth >= len(lexer.conditionalFrames) {
		lexer.conditionalOverflow++
		lexer.concreteEligible = false
		return
	}
	lexer.conditionalFrames[lexer.conditionalDepth] = cConditionalFrame{line: line}
	lexer.conditionalDepth++
}

func (lexer *cLexer) closeConditional(line int) {
	if lexer.conditionalOverflow > 0 {
		lexer.conditionalOverflow--
		return
	}
	if lexer.conditionalDepth == 0 {
		lexer.recoveryLines[line] = true
		return
	}
	lexer.conditionalDepth--
	start := lexer.conditionalFrames[lexer.conditionalDepth].line
	if start > 0 && line >= start {
		lexer.scopes = append(lexer.scopes, cLineScope{start: start, end: line})
	}
}

func (lexer *cLexer) lineAt(offset int) int {
	if offset < 0 {
		return 0
	}
	return sort.Search(len(lexer.lineStarts), func(index int) bool {
		return lexer.lineStarts[index] > offset
	})
}

func (lexer *cLexer) lineColumn(offset int) (int, int) {
	line := lexer.lineAt(offset)
	if line < 1 || line > len(lexer.lineStarts) {
		return 0, 0
	}
	return line, offset - lexer.lineStarts[line-1] + 1
}

func (lexer *cLexer) applyConcreteTokenGates() {
	var delimiterStack [cMaximumConcreteDelimiterDepth]byte
	depth, overflow := 0, 0
	groups, prefix, ternaries := 0, 0, 0
	for _, token := range lexer.tokens {
		if token.gapBefore {
			lexer.concreteEligible = false
			depth, overflow, groups, prefix, ternaries = 0, 0, 0, 0, 0
		}
		if token.kind == cTokenDirective {
			groups, prefix, ternaries = 0, 0, 0
			continue
		}
		switch token.text {
		case "(", "[", "{":
			if token.text == "{" {
				groups = 0
			} else {
				groups++
				if groups > cMaximumConcreteGroupsPerSegment {
					lexer.concreteEligible = false
				}
			}
			if depth >= len(delimiterStack) {
				overflow++
				lexer.concreteEligible = false
			} else {
				delimiterStack[depth] = token.text[0]
				depth++
			}
		case ")", "]", "}":
			if overflow > 0 {
				overflow--
			} else if depth > 0 && cDelimiterCloses(delimiterStack[depth-1], token.text[0]) {
				depth--
			}
		case ";":
			groups, ternaries = 0, 0
		}
		if cExpressionPrefixToken(token.text) {
			prefix++
			if prefix > cMaximumConcreteExpressionPrefix {
				lexer.concreteEligible = false
			}
		} else {
			prefix = 0
		}
		if token.text == "?" {
			ternaries++
			if ternaries > cMaximumConcreteExpressionPrefix {
				lexer.concreteEligible = false
			}
		}
	}

	for index, adjacent := 0, 0; index < len(lexer.tokens); {
		if lexer.tokens[index].gapBefore || lexer.tokens[index].kind == cTokenDirective {
			adjacent = 0
			index++
			continue
		}
		closeIndex, ok := cAttributeClose(lexer.tokens, index)
		if !ok {
			adjacent = 0
			index++
			continue
		}
		adjacent++
		if adjacent > cMaximumConcreteAdjacentAttributes {
			lexer.concreteEligible = false
		}
		index = closeIndex + 1
	}
}

func cDelimiterCloses(open, closing byte) bool {
	return open == '(' && closing == ')' || open == '[' && closing == ']' ||
		open == '{' && closing == '}'
}

func cExpressionPrefixToken(token string) bool {
	switch token {
	case "!", "~", "+", "-", "*", "&", "++", "--", "sizeof", "alignof",
		"_Alignof", "typeof", "typeof_unqual", "__typeof__":
		return true
	default:
		return false
	}
}

func cAttributeClose(tokens []cToken, start int) (int, bool) {
	if start < 0 || start+1 >= len(tokens) || tokens[start].text != "[" ||
		tokens[start+1].text != "[" || tokens[start+1].gapBefore {
		return start, false
	}
	depth := 1
	for index := start + 2; index+1 < len(tokens); index++ {
		if tokens[index].gapBefore || tokens[index].kind == cTokenDirective {
			return start, false
		}
		if tokens[index].text == "[" && tokens[index+1].text == "[" {
			depth++
			index++
			continue
		}
		if tokens[index].text == "]" && tokens[index+1].text == "]" {
			depth--
			if depth == 0 {
				return index + 1, true
			}
			index++
		}
	}
	return start, false
}

func (lexer *cLexer) addDefinition(
	definition sourceDefinition,
	trusted, recovered bool,
) {
	if definition.symbol == "" || definition.line < 1 || definition.column < 1 {
		return
	}
	if definition.scopeStart < 1 {
		definition.scopeStart = definition.line
	}
	if definition.scopeEnd < definition.scopeStart {
		definition.scopeEnd = definition.scopeStart
	}
	lexer.definitions = append(lexer.definitions, definition)
	key := cDefinitionKey(definition)
	if trusted {
		lexer.trusted[key] = true
	}
	if recovered {
		lexer.recovered[key] = true
	}
}

// resolveScopesAndDefinitions deliberately runs after the resource preflight.
// Its recovery is semicolon/brace bounded, so one malformed declaration cannot
// consume an otherwise independent clean suffix.
func (lexer *cLexer) resolveScopesAndDefinitions() {
	pairs, unmatched := cLexicalDelimiterPairs(lexer.tokens)
	resolver := &cLexicalResolver{
		lexer: lexer, pairs: pairs, hasUnmatched: unmatched,
		knrFunctions:  make(map[int]cKNRFunction),
		knrSemicolons: make(map[int]bool),
	}
	resolver.detectKNRFunctions()
	resolver.walk(0, len(lexer.tokens), cLexicalTranslation, 0)
	if lexer.truncated {
		lexer.definitions = append(lexer.definitions, lexer.streamDefinitions...)
		lexer.scopes = append(lexer.scopes, lexer.streamScopes...)
	}
}

func cLexicalDelimiterPairs(tokens []cToken) (map[int]int, bool) {
	pairs := make(map[int]int)
	var parentheses, brackets, braces []int
	type conditionalSnapshot struct {
		parentheses int
		brackets    int
		braces      int
	}
	conditionals := make([]conditionalSnapshot, 0, 8)
	hasUnmatched := false
	reset := func() {
		if len(parentheses)+len(brackets)+len(braces) > 0 {
			hasUnmatched = true
		}
		parentheses, brackets, braces = nil, nil, nil
	}
	restoreConditional := func(snapshot conditionalSnapshot) {
		if len(parentheses) > snapshot.parentheses || len(brackets) > snapshot.brackets ||
			len(braces) > snapshot.braces {
			hasUnmatched = true
		}
		parentheses = parentheses[:min(len(parentheses), snapshot.parentheses)]
		brackets = brackets[:min(len(brackets), snapshot.brackets)]
		braces = braces[:min(len(braces), snapshot.braces)]
	}
	expectingDirectiveKind := false
	for index, token := range tokens {
		if token.gapBefore {
			reset()
			conditionals = nil
			expectingDirectiveKind = false
		}
		if token.kind == cTokenDirective {
			if token.text == "#" {
				expectingDirectiveKind = true
				continue
			}
			if !expectingDirectiveKind {
				continue
			}
			expectingDirectiveKind = false
			switch token.text {
			case "if", "ifdef", "ifndef":
				conditionals = append(conditionals, conditionalSnapshot{
					parentheses: len(parentheses), brackets: len(brackets), braces: len(braces),
				})
			case "else", "elif", "elifdef", "elifndef":
				if len(conditionals) > 0 {
					restoreConditional(conditionals[len(conditionals)-1])
				}
			case "endif":
				if len(conditionals) > 0 {
					restoreConditional(conditionals[len(conditionals)-1])
					conditionals = conditionals[:len(conditionals)-1]
				}
			}
			continue
		}
		expectingDirectiveKind = false
		var stack *[]int
		switch token.text {
		case "(":
			parentheses = append(parentheses, index)
		case "[":
			brackets = append(brackets, index)
		case "{":
			braces = append(braces, index)
		case ")":
			stack = &parentheses
		case "]":
			stack = &brackets
		case "}":
			stack = &braces
		}
		if stack == nil || token.text == "(" || token.text == "[" || token.text == "{" {
			continue
		}
		if len(*stack) == 0 {
			hasUnmatched = true
			continue
		}
		floor := 0
		if len(conditionals) > 0 {
			snapshot := conditionals[len(conditionals)-1]
			switch token.text {
			case ")":
				floor = snapshot.parentheses
			case "]":
				floor = snapshot.brackets
			case "}":
				floor = snapshot.braces
			}
		}
		if len(*stack) <= floor {
			hasUnmatched = true
			continue
		}
		open := (*stack)[len(*stack)-1]
		*stack = (*stack)[:len(*stack)-1]
		pairs[open], pairs[index] = index, open
	}
	reset()
	return pairs, hasUnmatched
}

func (resolver *cLexicalResolver) detectKNRFunctions() {
	tokens := resolver.lexer.tokens
	for open, closingIndex := range resolver.pairs {
		if open >= closingIndex || tokens[open].text != "{" {
			continue
		}
		semicolons := make([]int, 0, 4)
		candidate := -1
		headerStart := open
		operations := 0
		for index := open - 1; index >= 0 && operations < 256; index, operations = index-1, operations+1 {
			if tokens[index].gapBefore || tokens[index].kind == cTokenDirective ||
				tokens[index].text == "{" || tokens[index].text == "}" {
				break
			}
			if tokens[index].text == ";" {
				semicolons = append(semicolons, index)
				continue
			}
			if tokens[index].kind != cTokenIdentifier || cKeyword(tokens[index].text) ||
				cDecorationIdentifier(tokens[index].text) || index+1 >= open ||
				tokens[index+1].text != "(" {
				continue
			}
			parameterClose, paired := resolver.pairs[index+1]
			if !paired || parameterClose >= open {
				continue
			}
			if len(semicolons) == 0 {
				// This is the ordinary declarator immediately owning the body,
				// not an earlier K&R header.
				break
			}
			if parameterClose+1 >= open || tokens[parameterClose+1].text == ";" ||
				!cHasDeclarationEvidence(tokens, max(0, index-16), index) {
				continue
			}
			candidate = index
			headerStart = cLexicalHeaderStart(tokens, index)
			break
		}
		if candidate < 0 {
			continue
		}
		resolver.knrFunctions[open] = cKNRFunction{
			headerStart: headerStart,
			nameIndex:   candidate,
		}
		for _, semicolon := range semicolons {
			if semicolon > candidate {
				resolver.knrSemicolons[semicolon] = true
			}
		}
	}
}

func (resolver *cLexicalResolver) walk(
	start, end int,
	context cLexicalContext,
	depth int,
) {
	tokens := resolver.lexer.tokens
	segmentStart := start
	for index := start; index < end; index++ {
		token := tokens[index]
		if token.gapBefore {
			segmentStart = index
		}
		if token.kind == cTokenDirective {
			segmentStart = index + 1
			continue
		}
		if token.text == ";" {
			if !resolver.knrSemicolons[index] {
				resolver.extractStatement(segmentStart, index, context)
			}
			segmentStart = index + 1
			continue
		}
		if token.text != "{" {
			continue
		}
		closingIndex, paired := resolver.pairs[index]
		if !paired || closingIndex <= index || closingIndex >= end {
			classification := resolver.classifyBrace(segmentStart, index)
			if classification.kind == cBraceFunction ||
				classification.kind == cBraceAggregate || classification.kind == cBraceEnum {
				resolver.addOwningDefinition(classification, index, true)
				resolver.addBraceScope(classification.headerStart, index)
			}
			segmentStart = index + 1
			continue
		}

		classification := resolver.classifyBrace(segmentStart, index)
		if knr, ok := resolver.knrFunctions[index]; ok {
			classification = cBraceClassification{
				kind: cBraceFunction, headerStart: knr.headerStart, nameIndex: knr.nameIndex,
			}
		}
		switch classification.kind {
		case cBraceFunction:
			resolver.addOwningDefinition(classification, closingIndex, false)
			resolver.addBraceScope(classification.headerStart, closingIndex)
			if depth < cMaximumConcreteDelimiterDepth {
				resolver.walk(index+1, closingIndex, cLexicalBlock, depth+1)
			}
			segmentStart = closingIndex + 1
		case cBraceAggregate, cBraceEnum:
			resolver.addOwningDefinition(classification, closingIndex, false)
			resolver.addBraceScope(classification.headerStart, closingIndex)
			if classification.kind == cBraceEnum {
				resolver.extractEnumerators(index+1, closingIndex)
			} else if depth < cMaximumConcreteDelimiterDepth {
				resolver.walk(index+1, closingIndex, cLexicalAggregate, depth+1)
			}
			// Keep the parent declaration open for aliases or objects after
			// the aggregate body.
		case cBraceInitializer:
			resolver.addBraceScope(classification.headerStart, closingIndex)
			if depth < cMaximumConcreteDelimiterDepth {
				resolver.walk(index+1, closingIndex, cLexicalBlock, depth+1)
			}
		case cBraceBlock:
			resolver.addBraceScope(classification.headerStart, closingIndex)
			if depth < cMaximumConcreteDelimiterDepth {
				resolver.walk(index+1, closingIndex, cLexicalBlock, depth+1)
			}
			segmentStart = closingIndex + 1
		}
		index = closingIndex
	}
}

func (resolver *cLexicalResolver) classifyBrace(start, open int) cBraceClassification {
	tokens := resolver.lexer.tokens
	start = cTrimLexicalStart(tokens, start, open)
	if function := resolver.functionCandidate(start, open); function >= 0 {
		return cBraceClassification{
			kind: cBraceFunction, headerStart: start, nameIndex: function,
		}
	}

	parenDepth, bracketDepth := 0, 0
	aggregateKeyword, tagIndex := "", -1
	hasAssignment := false
	for index := start; index < open; index++ {
		switch tokens[index].text {
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
				hasAssignment = true
			}
		}
		if parenDepth != 0 || bracketDepth != 0 {
			continue
		}
		if tokens[index].text == "struct" || tokens[index].text == "union" ||
			tokens[index].text == "enum" {
			aggregateKeyword = tokens[index].text
			tagIndex = cNextIdentifier(tokens, index+1, open)
		}
	}
	if aggregateKeyword != "" && !hasAssignment {
		kind := cBraceAggregate
		if aggregateKeyword == "enum" {
			kind = cBraceEnum
		}
		return cBraceClassification{
			kind: kind, headerStart: start, nameIndex: tagIndex,
		}
	}
	if hasAssignment || open > start && tokens[open-1].text == ")" &&
		!cControlHeader(tokens, start, open) {
		return cBraceClassification{kind: cBraceInitializer, headerStart: start, nameIndex: -1}
	}
	return cBraceClassification{kind: cBraceBlock, headerStart: start, nameIndex: -1}
}

func (resolver *cLexicalResolver) functionCandidate(start, open int) int {
	tokens := resolver.lexer.tokens
	best, bestDepth := -1, int(^uint(0)>>1)
	parenDepth, bracketDepth := 0, 0
	for index := start; index < open; index++ {
		if tokens[index].kind == cTokenIdentifier && !cKeyword(tokens[index].text) &&
			!cDecorationIdentifier(tokens[index].text) && index+1 < open &&
			tokens[index+1].text == "(" {
			closingIndex, paired := resolver.pairs[index+1]
			depth := parenDepth + bracketDepth
			if paired && closingIndex < open && depth < bestDepth &&
				cHasDeclarationEvidence(tokens, start, index) &&
				cDeclaratorPrefixSafe(tokens, start, index) {
				best, bestDepth = index, depth
			}
		}
		switch tokens[index].text {
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
	return best
}

func cHasDeclarationEvidence(tokens []cToken, start, name int) bool {
	for index := start; index < name; index++ {
		if tokens[index].kind != cTokenIdentifier || cDecorationIdentifier(tokens[index].text) {
			continue
		}
		if cTypeOrDeclarationSpecifier(tokens[index].text) {
			return true
		}
		if !cKeyword(tokens[index].text) {
			return true
		}
	}
	return false
}

func cDeclaratorPrefixSafe(tokens []cToken, start, name int) bool {
	for index := start; index < name; index++ {
		if tokens[index].kind != cTokenPunctuation {
			continue
		}
		switch tokens[index].text {
		case "*", "(", ")", "[", "]", ",", ":", "::", "...", "&", "&&", "+", "-":
			continue
		default:
			return false
		}
	}
	return true
}

func cTypeOrDeclarationSpecifier(text string) bool {
	switch text {
	case "alignas", "auto", "bool", "char", "const", "constexpr", "double", "enum",
		"extern", "float", "inline", "int", "long", "register", "restrict", "short",
		"signed", "static", "struct", "thread_local", "typedef", "typeof",
		"typeof_unqual", "union", "unsigned", "void", "volatile", "_Atomic", "_BitInt",
		"_Bool", "_Complex", "_Decimal32", "_Decimal64", "_Decimal128", "_Noreturn",
		"_Thread_local", "__auto_type", "__complex__", "__inline__", "__int128",
		"__restrict__", "__signed__", "__typeof__", "__volatile__":
		return true
	default:
		return false
	}
}

func cDecorationIdentifier(text string) bool {
	switch text {
	case "__attribute", "__attribute__", "__declspec", "__declspec__", "asm", "__asm",
		"__asm__":
		return true
	default:
		return false
	}
}

func cControlHeader(tokens []cToken, start, end int) bool {
	for index := start; index < end; index++ {
		if tokens[index].kind != cTokenIdentifier {
			continue
		}
		switch tokens[index].text {
		case "if", "for", "while", "switch", "do", "else":
			return true
		default:
			return false
		}
	}
	return false
}

func cTrimLexicalStart(tokens []cToken, start, end int) int {
	start = max(0, start)
	for start < end && (tokens[start].kind == cTokenDirective ||
		tokens[start].text == ";" || tokens[start].text == "}") {
		start++
	}
	return start
}

func cLexicalHeaderStart(tokens []cToken, nameIndex int) int {
	start := nameIndex
	for index := nameIndex - 1; index >= 0; index-- {
		if tokens[index].gapBefore || tokens[index].kind == cTokenDirective ||
			tokens[index].text == ";" || tokens[index].text == "{" || tokens[index].text == "}" {
			break
		}
		start = index
	}
	return start
}

func cNextIdentifier(tokens []cToken, start, end int) int {
	for index := start; index < end; index++ {
		if tokens[index].kind == cTokenIdentifier && !cKeyword(tokens[index].text) {
			return index
		}
		if tokens[index].text == "{" || tokens[index].text == ";" || tokens[index].text == "=" {
			break
		}
	}
	return -1
}

func (resolver *cLexicalResolver) addOwningDefinition(
	classification cBraceClassification,
	closingIndex int,
	localRecovery bool,
) {
	if classification.nameIndex < 0 || classification.nameIndex >= len(resolver.lexer.tokens) {
		return
	}
	token := resolver.lexer.tokens[classification.nameIndex]
	if token.kind != cTokenIdentifier || !cSourceIdentifier(token.text) || cKeyword(token.text) {
		return
	}
	line, column := resolver.lexer.lineColumn(token.start)
	startOffset := token.start
	if classification.headerStart >= 0 && classification.headerStart < len(resolver.lexer.tokens) {
		startOffset = resolver.lexer.tokens[classification.headerStart].start
	}
	startOffset = resolver.lexer.attachedDocumentationStart(startOffset)
	scopeStart := resolver.lexer.lineAt(startOffset)
	scopeEnd := resolver.lexer.lineAt(
		max(token.end, resolver.lexer.tokens[closingIndex].end) - 1,
	)
	ownedEndColumn := 0
	closingToken := resolver.lexer.tokens[closingIndex]
	if closingToken.text == "}" {
		ownedEndLine, exactEndColumn := resolver.lexer.lineColumn(closingToken.end)
		if ownedEndLine == scopeEnd {
			ownedEndColumn = exactEndColumn
		}
	}
	definition := sourceDefinition{
		symbol: token.text, line: line, column: column,
		scopeStart: scopeStart, scopeEnd: scopeEnd,
		ownedEndColumn: ownedEndColumn, ownsScope: true,
	}
	spliced := token.end-token.start != len(token.text)
	lineRecovery := resolver.lexer.recoveryLines[line]
	strongFunction := classification.kind == cBraceFunction && !localRecovery
	resolver.lexer.addDefinition(
		definition,
		spliced || strongFunction,
		localRecovery || lineRecovery || spliced,
	)
}

func (lexer *cLexer) attachedDocumentationStart(start int) int {
	best := start
	index := sort.Search(len(lexer.comments), func(index int) bool {
		return lexer.comments[index].end > start
	}) - 1
	nextStart := start
	for inspected := 0; index >= 0 && inspected < 64; index, inspected = index-1, inspected+1 {
		span := lexer.comments[index]
		if span.start < 0 || span.end < span.start || span.end > nextStart ||
			strings.TrimSpace(lexer.source[span.end:nextStart]) != "" {
			break
		}
		text := lexer.source[span.start:span.end]
		if strings.HasPrefix(text, "/**") || strings.HasPrefix(text, "/*!") ||
			strings.HasPrefix(text, "///") || strings.HasPrefix(text, "//!") {
			best = span.start
			nextStart = span.start
			continue
		}
		break
	}
	return best
}

func (resolver *cLexicalResolver) addBraceScope(headerStart, closingIndex int) {
	if closingIndex < 0 || closingIndex >= len(resolver.lexer.tokens) {
		return
	}
	startOffset := resolver.lexer.tokens[closingIndex].start
	if headerStart >= 0 && headerStart < len(resolver.lexer.tokens) {
		startOffset = resolver.lexer.tokens[headerStart].start
	}
	start := resolver.lexer.lineAt(startOffset)
	end := resolver.lexer.lineAt(max(resolver.lexer.tokens[closingIndex].end-1, 0))
	if start > 0 && end >= start {
		resolver.lexer.scopes = append(resolver.lexer.scopes, cLineScope{start: start, end: end})
	}
}

func (resolver *cLexicalResolver) extractEnumerators(start, end int) {
	for _, piece := range resolver.splitAtBaseCommas(start, end) {
		cut := piece[1]
		depth := 0
		for index := piece[0]; index < piece[1]; index++ {
			switch resolver.lexer.tokens[index].text {
			case "(", "[", "{":
				depth++
			case ")", "]", "}":
				depth = max(0, depth-1)
			case "=":
				if depth == 0 {
					cut = index
				}
			}
			if cut != piece[1] {
				break
			}
		}
		ignored := resolver.ignoredTokens(piece[0], cut)
		for index := piece[0]; index < cut; index++ {
			token := resolver.lexer.tokens[index]
			if ignored[index] || token.kind != cTokenIdentifier || cKeyword(token.text) ||
				!cSourceIdentifier(token.text) {
				continue
			}
			resolver.addNonOwningToken(index, false, false)
			break
		}
	}
}

func (resolver *cLexicalResolver) extractStatement(
	start, end int,
	context cLexicalContext,
) {
	start = cTrimLexicalStart(resolver.lexer.tokens, start, end)
	if start >= end || cNonDeclarationStatement(resolver.lexer.tokens[start].text) {
		return
	}
	typedef := cRangeHasToken(resolver.lexer.tokens, start, end, "typedef")
	typeEvidence := cStatementTypeEvidence(resolver.lexer.tokens, start, end)
	if !typeEvidence {
		return
	}

	pieces := resolver.splitAtBaseCommas(start, end)
	candidates := make([]cDeclaratorCandidate, 0, len(pieces))
	declarationEstablished := false
	for pieceIndex, piece := range pieces {
		if candidate, ok := resolver.chooseDeclarator(piece[0], piece[1]); ok {
			if pieceIndex == 0 {
				declarationEstablished = cHasDeclarationEvidence(
					resolver.lexer.tokens, start, candidate.index,
				)
			}
			if !declarationEstablished {
				continue
			}
			candidates = append(candidates, candidate)
		}
	}

	if tagIndex, forward := resolver.forwardTag(start, end, candidates); forward {
		resolver.addNonOwningToken(tagIndex, false, false)
	}
	if context == cLexicalEnum {
		return
	}
	hasAggregateBody := cRangeHasPairedBrace(resolver.lexer.tokens, resolver.pairs, start, end)
	knownUnsupported := cKnownUnsupportedLexicalStatement(
		resolver.lexer.tokens, start, end,
	)
	strongType := cStrongDeclarationEvidence(resolver.lexer.tokens, start, end)
	for _, candidate := range candidates {
		allowed := context == cLexicalTranslation || context == cLexicalAggregate || typedef ||
			candidate.isFunction
		if !allowed {
			continue
		}
		token := resolver.lexer.tokens[candidate.index]
		line, column := resolver.lexer.lineColumn(token.start)
		scopeStart, scopeEnd := line, line
		if hasAggregateBody {
			scopeStart = resolver.lexer.lineAt(resolver.lexer.tokens[start].start)
			scopeEnd = resolver.lexer.lineAt(max(token.end, resolver.lexer.tokens[end-1].end) - 1)
		}
		definition := sourceDefinition{
			symbol: token.text, line: line, column: column,
			scopeStart: scopeStart, scopeEnd: scopeEnd,
		}
		spliced := token.end-token.start != len(token.text)
		lineRecovery := resolver.lexer.recoveryLines[line]
		trusted := spliced || knownUnsupported || candidate.isFunction && strongType
		resolver.lexer.addDefinition(definition, trusted, spliced || lineRecovery)
	}
}

func cNonDeclarationStatement(first string) bool {
	switch first {
	case "break", "case", "continue", "default", "do", "else", "for", "goto", "if",
		"return", "switch", "while", "static_assert", "_Static_assert":
		return true
	default:
		return false
	}
}

func cStatementTypeEvidence(tokens []cToken, start, end int) bool {
	identifierCount := 0
	firstIdentifier := -1
	for index := start; index < end; index++ {
		if tokens[index].kind != cTokenIdentifier || cDecorationIdentifier(tokens[index].text) {
			continue
		}
		if cTypeOrDeclarationSpecifier(tokens[index].text) {
			return true
		}
		if !cKeyword(tokens[index].text) {
			if firstIdentifier < 0 {
				firstIdentifier = index
			}
			identifierCount++
		}
	}
	return identifierCount >= 2 && (firstIdentifier+1 >= end ||
		tokens[firstIdentifier+1].text != "(")
}

func cStrongDeclarationEvidence(tokens []cToken, start, end int) bool {
	for index := start; index < end; index++ {
		switch tokens[index].text {
		case "bool", "char", "double", "enum", "float", "int", "long", "short", "signed",
			"struct", "union", "unsigned", "void", "_Atomic", "_BitInt", "_Bool",
			"_Complex", "_Decimal32", "_Decimal64", "_Decimal128", "typeof",
			"typeof_unqual", "__typeof", "__typeof__":
			return true
		}
	}
	return false
}

func cKnownUnsupportedLexicalStatement(tokens []cToken, start, end int) bool {
	for index := start; index < end; index++ {
		switch tokens[index].text {
		case "__attribute", "__attribute__", "__declspec", "__declspec__", "nullptr_t",
			"typeof", "typeof_unqual", "__typeof", "__typeof__", "...":
			return true
		}
	}
	return false
}

func cRangeHasToken(tokens []cToken, start, end int, want string) bool {
	for index := start; index < end; index++ {
		if tokens[index].text == want {
			return true
		}
	}
	return false
}

func cRangeHasPairedBrace(tokens []cToken, pairs map[int]int, start, end int) bool {
	for index := start; index < end; index++ {
		if tokens[index].text == "{" {
			if closingIndex, ok := pairs[index]; ok && closingIndex < end {
				return true
			}
		}
	}
	return false
}

func (resolver *cLexicalResolver) splitAtBaseCommas(start, end int) [][2]int {
	pieces := make([][2]int, 0, 2)
	pieceStart := start
	parenDepth, bracketDepth, braceDepth := 0, 0, 0
	for index := start; index < end; index++ {
		switch resolver.lexer.tokens[index].text {
		case "(":
			parenDepth++
		case ")":
			parenDepth = max(0, parenDepth-1)
		case "[":
			bracketDepth++
		case "]":
			bracketDepth = max(0, bracketDepth-1)
		case "{":
			braceDepth++
		case "}":
			braceDepth = max(0, braceDepth-1)
		case ",":
			if parenDepth == 0 && bracketDepth == 0 && braceDepth == 0 {
				pieces = append(pieces, [2]int{pieceStart, index})
				pieceStart = index + 1
			}
		}
	}
	pieces = append(pieces, [2]int{pieceStart, end})
	return pieces
}

func (resolver *cLexicalResolver) chooseDeclarator(start, end int) (cDeclaratorCandidate, bool) {
	start = cTrimLexicalStart(resolver.lexer.tokens, start, end)
	if start >= end {
		return cDeclaratorCandidate{}, false
	}
	ignored := resolver.ignoredTokens(start, end)
	cut := end
	parenDepth, bracketDepth, braceDepth := 0, 0, 0
	for index := start; index < end; index++ {
		switch resolver.lexer.tokens[index].text {
		case "(":
			parenDepth++
		case ")":
			parenDepth = max(0, parenDepth-1)
		case "[":
			bracketDepth++
		case "]":
			bracketDepth = max(0, bracketDepth-1)
		case "{":
			braceDepth++
		case "}":
			braceDepth = max(0, braceDepth-1)
		case "=", ":":
			if parenDepth == 0 && bracketDepth == 0 && braceDepth == 0 {
				cut = index
			}
		}
		if cut != end {
			break
		}
	}

	bestFunction, bestDepth := -1, int(^uint(0)>>1)
	parenDepth, bracketDepth, braceDepth = 0, 0, 0
	for index := start; index < cut; index++ {
		token := resolver.lexer.tokens[index]
		if !ignored[index] && token.kind == cTokenIdentifier && !cKeyword(token.text) &&
			index+1 < cut && resolver.lexer.tokens[index+1].text == "(" {
			closingIndex, paired := resolver.pairs[index+1]
			depth := parenDepth + bracketDepth + braceDepth
			if paired && closingIndex < cut && depth < bestDepth {
				bestFunction, bestDepth = index, depth
			}
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
		case "{":
			braceDepth++
		case "}":
			braceDepth = max(0, braceDepth-1)
		}
	}
	if bestFunction >= 0 && cDeclaratorPrefixSafe(
		resolver.lexer.tokens, start, bestFunction,
	) {
		return cDeclaratorCandidate{index: bestFunction, isFunction: true}, true
	}

	bestPointer, bestPointerDepth := -1, int(^uint(0)>>1)
	parenDepth, bracketDepth, braceDepth = 0, 0, 0
	lastBaseIdentifier := -1
	for index := start; index < cut; index++ {
		token := resolver.lexer.tokens[index]
		depth := parenDepth + bracketDepth + braceDepth
		if !ignored[index] && token.kind == cTokenIdentifier && !cKeyword(token.text) &&
			!cDecorationIdentifier(token.text) {
			if depth == 0 {
				lastBaseIdentifier = index
			}
			if cPointerDeclaratorIdentifier(resolver.lexer.tokens, start, index) &&
				depth < bestPointerDepth {
				bestPointer, bestPointerDepth = index, depth
			}
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
		case "{":
			braceDepth++
		case "}":
			braceDepth = max(0, braceDepth-1)
		}
	}
	if bestPointer >= 0 && cDeclaratorPrefixSafe(
		resolver.lexer.tokens, start, bestPointer,
	) {
		return cDeclaratorCandidate{index: bestPointer}, true
	}
	if lastBaseIdentifier >= 0 && cDeclaratorPrefixSafe(
		resolver.lexer.tokens, start, lastBaseIdentifier,
	) {
		if lastBaseIdentifier+1 < cut && resolver.lexer.tokens[lastBaseIdentifier+1].text == "(" {
			if _, paired := resolver.pairs[lastBaseIdentifier+1]; !paired {
				return cDeclaratorCandidate{}, false
			}
		}
		return cDeclaratorCandidate{index: lastBaseIdentifier}, true
	}
	return cDeclaratorCandidate{}, false
}

func cPointerDeclaratorIdentifier(tokens []cToken, start, index int) bool {
	for previous := index - 1; previous >= start && index-previous <= 8; previous-- {
		switch tokens[previous].text {
		case "*":
			return true
		case "(", ",", ";", "=":
			return false
		}
		if tokens[previous].kind == cTokenIdentifier && !cKeyword(tokens[previous].text) {
			return false
		}
	}
	return false
}

func (resolver *cLexicalResolver) ignoredTokens(start, end int) map[int]bool {
	ignored := make(map[int]bool)
	tokens := resolver.lexer.tokens
	for index := start; index < end; index++ {
		if tokens[index].text == "[" && index+1 < end && tokens[index+1].text == "[" {
			if closingIndex, ok := cAttributeClose(tokens[:end], index); ok {
				for current := index; current <= closingIndex; current++ {
					ignored[current] = true
				}
				index = closingIndex
				continue
			}
		}
		if tokens[index].text == "{" {
			if closingIndex, ok := resolver.pairs[index]; ok && closingIndex < end {
				for current := index; current <= closingIndex; current++ {
					ignored[current] = true
				}
				index = closingIndex
				continue
			}
		}
		if tokens[index].text == "struct" || tokens[index].text == "union" ||
			tokens[index].text == "enum" {
			if tag := cNextIdentifier(tokens, index+1, end); tag >= 0 {
				ignored[tag] = true
			}
			continue
		}
		if !cTypeOperatorWithOperand(tokens[index].text) &&
			!cDecorationIdentifier(tokens[index].text) {
			continue
		}
		ignored[index] = true
		if index+1 < end && tokens[index+1].text == "(" {
			if closingIndex, ok := resolver.pairs[index+1]; ok && closingIndex < end {
				for current := index + 1; current <= closingIndex; current++ {
					ignored[current] = true
				}
				index = closingIndex
			}
		}
	}
	return ignored
}

func cTypeOperatorWithOperand(text string) bool {
	switch text {
	case "alignas", "_Alignas", "_Atomic", "_BitInt", "typeof", "typeof_unqual",
		"__typeof", "__typeof__":
		return true
	default:
		return false
	}
}

func (resolver *cLexicalResolver) forwardTag(
	start, end int,
	candidates []cDeclaratorCandidate,
) (int, bool) {
	if len(candidates) != 0 || cRangeHasPairedBrace(resolver.lexer.tokens, resolver.pairs, start, end) {
		return -1, false
	}
	parenDepth, bracketDepth := 0, 0
	for index := start; index < end; index++ {
		switch resolver.lexer.tokens[index].text {
		case "(":
			parenDepth++
		case ")":
			parenDepth = max(0, parenDepth-1)
		case "[":
			bracketDepth++
		case "]":
			bracketDepth = max(0, bracketDepth-1)
		}
		if parenDepth == 0 && bracketDepth == 0 &&
			(resolver.lexer.tokens[index].text == "struct" ||
				resolver.lexer.tokens[index].text == "union" ||
				resolver.lexer.tokens[index].text == "enum") {
			tag := cNextIdentifier(resolver.lexer.tokens, index+1, end)
			return tag, tag >= 0
		}
	}
	return -1, false
}

func (resolver *cLexicalResolver) addNonOwningToken(
	index int,
	recovered, trusted bool,
) {
	if index < 0 || index >= len(resolver.lexer.tokens) {
		return
	}
	token := resolver.lexer.tokens[index]
	if token.kind != cTokenIdentifier || !cSourceIdentifier(token.text) || cKeyword(token.text) {
		return
	}
	line, column := resolver.lexer.lineColumn(token.start)
	definition := sourceDefinition{
		symbol: token.text, line: line, column: column,
		scopeStart: line, scopeEnd: line,
	}
	spliced := token.end-token.start != len(token.text)
	lineRecovery := resolver.lexer.recoveryLines[line]
	resolver.lexer.addDefinition(
		definition, trusted || spliced, recovered || lineRecovery || spliced,
	)
}

func cSortLexicalDefinitions(definitions []sourceDefinition) []sourceDefinition {
	sort.SliceStable(definitions, func(first, second int) bool {
		if definitions[first].line != definitions[second].line {
			return definitions[first].line < definitions[second].line
		}
		if definitions[first].column != definitions[second].column {
			return definitions[first].column < definitions[second].column
		}
		return definitions[first].symbol < definitions[second].symbol
	})
	unique := definitions[:0]
	for _, definition := range definitions {
		if len(unique) == 0 || cDefinitionKey(unique[len(unique)-1]) != cDefinitionKey(definition) {
			unique = append(unique, definition)
		}
	}
	return unique
}

func cSortLineScopes(scopes []cLineScope) []cLineScope {
	sort.Slice(scopes, func(first, second int) bool {
		if scopes[first].start != scopes[second].start {
			return scopes[first].start < scopes[second].start
		}
		return scopes[first].end < scopes[second].end
	})
	unique := scopes[:0]
	for _, scope := range scopes {
		if scope.start < 1 || scope.end < scope.start {
			continue
		}
		if len(unique) == 0 || unique[len(unique)-1] != scope {
			unique = append(unique, scope)
		}
	}
	return unique
}

func cSortLineSpans(spans []cLineSpan) []cLineSpan {
	sort.Slice(spans, func(first, second int) bool {
		if spans[first].start != spans[second].start {
			return spans[first].start < spans[second].start
		}
		return spans[first].end < spans[second].end
	})
	unique := spans[:0]
	for _, span := range spans {
		if span.start < 1 || span.end < span.start {
			continue
		}
		if len(unique) == 0 || unique[len(unique)-1] != span {
			unique = append(unique, span)
		}
	}
	return unique
}
