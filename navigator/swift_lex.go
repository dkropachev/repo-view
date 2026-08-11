package navigator

import (
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	swiftMaximumConcreteDirectiveDepth     = 128
	swiftMaximumConcreteRawDelimiterHashes = 255
	swiftMaximumRetainedTokens             = 256 << 10
	swiftMaximumRetainedSpans              = 256 << 10
	swiftMaximumStructuralDepth            = 512
	swiftMaximumHeaderTokens               = 4096
	swiftMaximumInterpolationDepth         = 128
	swiftMaximumSuspendedHeaders           = 64
	swiftLexicalLookaheadMultiplier        = 8
	swiftMinimumLookaheadBudget            = 64 << 10
)

const (
	swiftTokenGap     = "\x00swift-token-gap\x00"
	swiftLiteralToken = "\x00swift-literal\x00"
)

type swiftTokenKind uint8

const (
	swiftTokenIdentifier swiftTokenKind = iota
	swiftTokenNumber
	swiftTokenPunctuation
	swiftTokenInterpolationStart
	swiftTokenInterpolationEnd
)

type swiftToken struct {
	text       string
	start, end int
	nameStart  int
	kind       swiftTokenKind

	lineStart, gap   bool
	quotedIdentifier bool
}

type swiftLiteral struct {
	start, end int
	hashCount  int

	terminated, multiline, regex bool
}

type swiftLexResult struct {
	tokens                []swiftToken
	commentSpans          []cByteSpan
	stringSpans           []cByteSpan
	lexicalUnits          int
	maximumDelimiterDepth int
	maximumDirectiveDepth int
	maximumRawHashCount   int

	truncated, spansTruncated, concreteEligible bool
}

type swiftTokenRetention struct {
	head      []swiftToken
	tail      []swiftToken
	tailStart int
	total     int
}

func (retention *swiftTokenRetention) append(token swiftToken) {
	retention.total++
	headLimit := (swiftMaximumRetainedTokens - 1) / 2
	tailLimit := swiftMaximumRetainedTokens - headLimit
	if len(retention.head) < headLimit {
		retention.head = append(retention.head, token)
		return
	}
	if len(retention.tail) < tailLimit {
		retention.tail = append(retention.tail, token)
		return
	}
	retention.tail[retention.tailStart] = token
	retention.tailStart = (retention.tailStart + 1) % tailLimit
}

func (retention *swiftTokenRetention) result() ([]swiftToken, bool) {
	if retention.total <= swiftMaximumRetainedTokens {
		return append(retention.head, retention.tail...), false
	}
	tokens := make([]swiftToken, 0, swiftMaximumRetainedTokens)
	tokens = append(tokens, retention.head...)
	tokens = append(tokens, swiftToken{
		text: swiftTokenGap, kind: swiftTokenPunctuation, gap: true,
	})
	tailCount := swiftMaximumRetainedTokens - len(retention.head) - 1
	tailCount = min(tailCount, len(retention.tail))
	skip := len(retention.tail) - tailCount
	for index := range tailCount {
		position := (retention.tailStart + skip + index) % len(retention.tail)
		tokens = append(tokens, retention.tail[position])
	}
	return tokens, true
}

type swiftLexicalSink struct {
	comment      func(cByteSpan) bool
	literal      func(cByteSpan) bool
	literalState func(swiftLiteral) bool
	token        func(swiftToken) bool
}

type swiftLexicalBudget struct {
	remaining int
	exhausted bool
}

func newSwiftLexicalBudget(sourceLength int) *swiftLexicalBudget {
	maximumInt := int(^uint(0) >> 1)
	remaining := swiftMinimumLookaheadBudget
	if sourceLength > (maximumInt-remaining)/swiftLexicalLookaheadMultiplier {
		remaining = maximumInt
	} else if sourceLength > 0 {
		remaining += sourceLength * swiftLexicalLookaheadMultiplier
	}
	return &swiftLexicalBudget{remaining: remaining}
}

func (budget *swiftLexicalBudget) take(amount int) bool {
	if budget == nil || amount <= 0 {
		return true
	}
	if amount > budget.remaining {
		budget.remaining = 0
		budget.exhausted = true
		return false
	}
	budget.remaining -= amount
	return true
}

type swiftLexicalWalkStatus struct {
	maximumRawHashCount int
	unterminatedOpaque  bool
}

func lexSwift(source string) swiftLexResult {
	result := swiftLexResult{concreteEligible: true}
	var retention swiftTokenRetention
	appendSpan := func(destination *[]cByteSpan, span cByteSpan) {
		if span.start >= span.end {
			return
		}
		if len(*destination) < swiftMaximumRetainedSpans {
			*destination = append(*destination, span)
		} else {
			result.spansTruncated = true
		}
	}
	status, withinBudget := swiftWalkLexically(source, swiftLexicalSink{
		comment: func(span cByteSpan) bool {
			result.lexicalUnits++
			appendSpan(&result.commentSpans, span)
			return true
		},
		literal: func(span cByteSpan) bool {
			result.lexicalUnits++
			appendSpan(&result.stringSpans, span)
			return true
		},
		literalState: func(literal swiftLiteral) bool {
			result.maximumRawHashCount = max(
				result.maximumRawHashCount, literal.hashCount,
			)
			if !literal.terminated && (literal.multiline || literal.hashCount > 0) {
				result.concreteEligible = false
			}
			return true
		},
		token: func(token swiftToken) bool {
			retention.append(token)
			result.lexicalUnits++
			return true
		},
	})
	result.maximumRawHashCount = max(
		result.maximumRawHashCount, status.maximumRawHashCount,
	)
	if status.unterminatedOpaque {
		result.concreteEligible = false
	}
	result.tokens, result.truncated = retention.result()
	// Preserve one retained entry per lexical construct. Besides making the
	// retention frontier exact, this avoids adjacent comments consuming less
	// accounting space merely because their byte ranges happen to touch.
	maximumDepth, maximumDirectives, frontierEligible := swiftConcreteFrontier(source)
	result.maximumDelimiterDepth = maximumDepth
	result.maximumDirectiveDepth = maximumDirectives
	result.concreteEligible = result.concreteEligible && frontierEligible && withinBudget
	if result.lexicalUnits > swiftMaximumConcreteTokens ||
		len(source) > swiftMaximumConcreteParseBytes || result.spansTruncated ||
		result.maximumRawHashCount > swiftMaximumConcreteRawDelimiterHashes {
		result.concreteEligible = false
	}
	return result
}

func walkSwiftLexically(source string, sink swiftLexicalSink) bool {
	_, withinBudget := swiftWalkLexically(source, sink)
	return withinBudget
}

func swiftWalkLexically(
	source string,
	sink swiftLexicalSink,
) (swiftLexicalWalkStatus, bool) {
	walker := swiftLexicalWalker{
		source:        source,
		sink:          sink,
		budget:        newSwiftLexicalBudget(len(source)),
		lineOnlySpace: true,
	}
	walker.walkCode(false, 0)
	return walker.status, !walker.budget.exhausted
}

type swiftLexicalWalker struct {
	sink   swiftLexicalSink
	budget *swiftLexicalBudget
	source string
	status swiftLexicalWalkStatus

	offset, lineStateOffset int
	stop, lineOnlySpace     bool
}

func (walker *swiftLexicalWalker) walkCode(stopAtParen bool, depth int) bool {
	parentheses := 1
	for walker.offset < len(walker.source) && !walker.stop {
		if walker.offset == 0 && strings.HasPrefix(walker.source, "#!") {
			walker.scanShebang()
			continue
		}
		if walker.offset == 0 && strings.HasPrefix(walker.source, "\uFEFF") {
			walker.offset += len("\uFEFF")
			walker.lineStateOffset = walker.offset
			continue
		}
		character := walker.source[walker.offset]
		if swiftASCIIWhitespace(character) {
			walker.offset++
			continue
		}
		if stopAtParen {
			switch character {
			case '(':
				parentheses++
			case ')':
				parentheses--
				if parentheses == 0 {
					start := walker.offset
					walker.offset++
					walker.emitToken(swiftToken{
						text: ")", start: start, end: walker.offset,
						nameStart: start, kind: swiftTokenInterpolationEnd,
					})
					return true
				}
			}
		}

		switch {
		case strings.HasPrefix(walker.source[walker.offset:], "//"):
			walker.scanLineComment()
		case strings.HasPrefix(walker.source[walker.offset:], "/*"):
			walker.scanBlockComment()
		case character == '"':
			walker.scanString(0, depth)
		case character == '#':
			hashes, quotes, regex, ok := swiftHashDelimitedOpening(
				walker.source, walker.offset,
			)
			switch {
			case ok && !regex:
				walker.scanStringWithOpening(hashes, quotes, depth)
			case ok:
				if !walker.scanExtendedRegex(hashes) {
					walker.scanHashTokens(hashes)
				}
			default:
				walker.scanHashTokens(hashes)
			}
		case character == '/' && walker.regexCanStart() && walker.scanBareRegex():
		case character == '`':
			walker.scanQuotedIdentifier()
		case character == '$' && walker.offset+1 < len(walker.source) &&
			swiftIdentifierContinueAt(walker.source, walker.offset+1):
			walker.scanDollarIdentifier()
		case swiftIdentifierStartAt(walker.source, walker.offset):
			walker.scanIdentifier()
		case character >= '0' && character <= '9':
			walker.scanNumber()
		default:
			walker.scanPunctuationOrOperator()
		}
	}
	return false
}

func (walker *swiftLexicalWalker) scanShebang() {
	start := walker.offset
	for walker.offset < len(walker.source) &&
		walker.source[walker.offset] != '\n' && walker.source[walker.offset] != '\r' {
		walker.offset++
	}
	walker.emitComment(start, walker.offset, true)
}

func (walker *swiftLexicalWalker) emitToken(token swiftToken) {
	if walker.stop {
		return
	}
	if token.nameStart == 0 && token.start != 0 {
		token.nameStart = token.start
	}
	token.lineStart = walker.onlyHorizontalSpaceBefore(token.start)
	if walker.sink.token != nil && !walker.sink.token(token) {
		walker.stop = true
	}
}

func (walker *swiftLexicalWalker) onlyHorizontalSpaceBefore(offset int) bool {
	offset = max(0, min(offset, len(walker.source)))
	if offset < walker.lineStateOffset {
		return false
	}
	for walker.lineStateOffset < offset {
		character := walker.source[walker.lineStateOffset]
		walker.lineStateOffset++
		switch character {
		case '\n', '\r':
			walker.lineOnlySpace = true
		case ' ', '\t', '\v', '\f', '\x00':
		default:
			walker.lineOnlySpace = false
		}
	}
	return walker.lineOnlySpace
}

func (walker *swiftLexicalWalker) emitComment(start, end int, terminated bool) {
	if start >= end || walker.stop {
		return
	}
	if !terminated {
		walker.status.unterminatedOpaque = true
	}
	if walker.sink.comment != nil && !walker.sink.comment(cByteSpan{start: start, end: end}) {
		walker.stop = true
	}
}

func (walker *swiftLexicalWalker) emitLiteralSpan(start, end int) {
	if start >= end || walker.stop {
		return
	}
	if walker.sink.literal != nil && !walker.sink.literal(cByteSpan{start: start, end: end}) {
		walker.stop = true
	}
}

func (walker *swiftLexicalWalker) emitLiteralState(literal swiftLiteral) {
	if !literal.terminated && (literal.multiline || literal.hashCount > 0 || literal.regex) {
		walker.status.unterminatedOpaque = true
	}
	walker.status.maximumRawHashCount = max(
		walker.status.maximumRawHashCount, literal.hashCount,
	)
	if walker.sink.literalState != nil && !walker.sink.literalState(literal) {
		walker.stop = true
	}
}

func (walker *swiftLexicalWalker) scanLineComment() {
	start := walker.offset
	walker.offset += 2
	for walker.offset < len(walker.source) {
		if walker.source[walker.offset] == '\n' || walker.source[walker.offset] == '\r' {
			break
		}
		walker.offset++
	}
	walker.emitComment(start, walker.offset, true)
}

func (walker *swiftLexicalWalker) scanBlockComment() {
	start := walker.offset
	walker.offset += 2
	depth := 1
	for walker.offset < len(walker.source) && depth > 0 {
		switch {
		case strings.HasPrefix(walker.source[walker.offset:], "/*"):
			depth++
			walker.offset += 2
		case strings.HasPrefix(walker.source[walker.offset:], "*/"):
			depth--
			walker.offset += 2
		default:
			_, size := utf8.DecodeRuneInString(walker.source[walker.offset:])
			if size < 1 {
				size = 1
			}
			walker.offset += size
		}
	}
	walker.emitComment(start, walker.offset, depth == 0)
}

func (walker *swiftLexicalWalker) scanString(hashCount, depth int) {
	quotes := 1
	if strings.HasPrefix(walker.source[walker.offset:], `"""`) {
		quotes = 3
	}
	walker.scanStringWithOpening(hashCount, quotes, depth)
}

func (walker *swiftLexicalWalker) scanStringWithOpening(
	hashCount, quoteCount, interpolationDepth int,
) {
	start := walker.offset
	openerBytes := hashCount + quoteCount
	walker.offset = min(len(walker.source), walker.offset+openerBytes)
	segmentStart := start
	multiline := quoteCount == 3
	walker.status.maximumRawHashCount = max(
		walker.status.maximumRawHashCount, hashCount,
	)

	for walker.offset < len(walker.source) && !walker.stop {
		if swiftStringClosingAt(
			walker.source, walker.offset, hashCount, quoteCount,
		) {
			walker.offset += quoteCount + hashCount
			walker.emitLiteralSpan(segmentStart, walker.offset)
			walker.emitLiteralState(swiftLiteral{
				start: start, end: walker.offset, hashCount: hashCount,
				terminated: true, multiline: multiline,
			})
			return
		}
		if !multiline && (walker.source[walker.offset] == '\n' ||
			walker.source[walker.offset] == '\r') {
			walker.emitLiteralSpan(segmentStart, walker.offset)
			walker.emitLiteralState(swiftLiteral{
				start: start, end: walker.offset, hashCount: hashCount,
				multiline: false,
			})
			return
		}
		if interpolationDepth < swiftMaximumInterpolationDepth {
			prefixEnd, openParen, ok := swiftInterpolationOpening(
				walker.source, walker.offset, hashCount,
			)
			if ok {
				walker.emitLiteralSpan(segmentStart, prefixEnd)
				walker.offset = prefixEnd
				walker.emitToken(swiftToken{
					text: "(", start: openParen, end: openParen + 1,
					nameStart: openParen, kind: swiftTokenInterpolationStart,
				})
				closed := walker.walkCode(true, interpolationDepth+1)
				if !closed {
					walker.emitLiteralState(swiftLiteral{
						start: start, end: walker.offset, hashCount: hashCount,
						multiline: multiline,
					})
					return
				}
				segmentStart = max(openParen+1, walker.offset-1)
				continue
			}
		}
		if hashCount == 0 && walker.source[walker.offset] == '\\' {
			walker.offset++
			if walker.offset < len(walker.source) {
				_, size := utf8.DecodeRuneInString(walker.source[walker.offset:])
				if size < 1 {
					size = 1
				}
				walker.offset += size
			}
			continue
		}
		_, size := utf8.DecodeRuneInString(walker.source[walker.offset:])
		if size < 1 {
			size = 1
		}
		walker.offset += size
	}
	walker.emitLiteralSpan(segmentStart, walker.offset)
	walker.emitLiteralState(swiftLiteral{
		start: start, end: walker.offset, hashCount: hashCount,
		multiline: multiline,
	})
}

func swiftHashDelimitedOpening(
	source string,
	start int,
) (hashes, quotes int, regex, ok bool) {
	if start < 0 || start >= len(source) || source[start] != '#' {
		return 0, 0, false, false
	}
	position := start
	for position < len(source) && source[position] == '#' {
		position++
	}
	hashes = position - start
	if position >= len(source) {
		return hashes, 0, false, false
	}
	switch source[position] {
	case '"':
		quotes = 1
		if strings.HasPrefix(source[position:], `"""`) {
			quotes = 3
		}
		return hashes, quotes, false, true
	case '/':
		return hashes, 0, true, true
	default:
		return hashes, 0, false, false
	}
}

func swiftStringClosingAt(
	source string,
	start, hashCount, quoteCount int,
) bool {
	endQuotes := start + quoteCount
	end := endQuotes + hashCount
	if start < 0 || end > len(source) {
		return false
	}
	for position := start; position < endQuotes; position++ {
		if source[position] != '"' {
			return false
		}
	}
	for position := endQuotes; position < end; position++ {
		if source[position] != '#' {
			return false
		}
	}
	return true
}

func swiftInterpolationOpening(
	source string,
	start, hashCount int,
) (prefixEnd, openParen int, ok bool) {
	if start < 0 || start >= len(source) || source[start] != '\\' {
		return 0, 0, false
	}
	position := start + 1
	for range hashCount {
		if position >= len(source) || source[position] != '#' {
			return 0, 0, false
		}
		position++
	}
	if position >= len(source) || source[position] != '(' {
		return 0, 0, false
	}
	return position + 1, position, true
}

func (walker *swiftLexicalWalker) scanExtendedRegex(hashCount int) bool {
	if walker.budget.exhausted {
		return false
	}
	start := walker.offset
	position := start + hashCount + 1
	multiline := swiftPhysicalLineBreakWidth(walker.source, position) > 0
	lineOnlySpace := false
	for position < len(walker.source) {
		if !walker.budget.take(1) {
			return false
		}
		if lineBreakWidth := swiftPhysicalLineBreakWidth(
			walker.source, position,
		); lineBreakWidth > 0 {
			if !multiline {
				return false
			}
			if lineBreakWidth > 1 && !walker.budget.take(lineBreakWidth-1) {
				return false
			}
			position += lineBreakWidth
			lineOnlySpace = true
			continue
		}
		if walker.source[position] == '\\' {
			lineOnlySpace = false
			position++
			if position < len(walker.source) {
				lineBreakWidth := swiftPhysicalLineBreakWidth(walker.source, position)
				if lineBreakWidth > 0 {
					if !multiline || !walker.budget.take(lineBreakWidth) {
						return false
					}
					position += lineBreakWidth
					lineOnlySpace = true
				} else {
					_, size := utf8.DecodeRuneInString(walker.source[position:])
					size = max(size, 1)
					if !walker.budget.take(size) {
						return false
					}
					position += size
				}
			}
			continue
		}
		if walker.source[position] == '/' && (!multiline || lineOnlySpace) {
			closingEnd := position + 1
			for closingEnd < len(walker.source) &&
				closingEnd < position+1+hashCount && walker.source[closingEnd] == '#' {
				if !walker.budget.take(1) {
					return false
				}
				closingEnd++
			}
			if closingEnd == position+1+hashCount {
				walker.offset = closingEnd
				walker.emitLiteralSpan(start, closingEnd)
				walker.emitLiteralState(swiftLiteral{
					start: start, end: closingEnd, hashCount: hashCount,
					terminated: true, multiline: multiline, regex: true,
				})
				return true
			}
			position = closingEnd
			lineOnlySpace = false
			continue
		}
		_, size := utf8.DecodeRuneInString(walker.source[position:])
		size = max(size, 1)
		if size > 1 && !walker.budget.take(size-1) {
			return false
		}
		if !swiftLineOnlyIndentationByte(walker.source[position]) {
			lineOnlySpace = false
		}
		position += size
	}
	return false
}

func (walker *swiftLexicalWalker) regexCanStart() bool {
	start := walker.offset
	if start+1 >= len(walker.source) || walker.source[start+1] == '/' ||
		walker.source[start+1] == '*' || swiftBareRegexForbiddenOpening(walker.source[start+1]) {
		return false
	}
	return walker.expressionCanStartAt(start)
}

func (walker *swiftLexicalWalker) expressionCanStartAt(start int) bool {
	if walker.onlyHorizontalSpaceBefore(start) {
		return true
	}
	previous := swiftPreviousSignificantByte(walker.source, start)
	if previous < 0 {
		return true
	}
	switch walker.source[previous] {
	case '=', '(', '[', '{', ',', ':', ';':
		return true
	}
	previousRune, _ := utf8.DecodeLastRuneInString(walker.source[:previous+1])
	if previousRune != '.' && swiftOperatorHeadRune(previousRune) {
		return true
	}
	word := swiftPreviousWord(walker.source, start)
	switch word {
	case "return", "throw", "case", "in", "where", "try", "await", "if", "guard",
		"while", "switch", "consume", "yield", "each", "repeat":
		return true
	default:
		return false
	}
}

func (walker *swiftLexicalWalker) scanBareRegex() bool {
	if walker.budget.exhausted {
		return false
	}
	start := walker.offset
	position := start + 1
	classDepth := 0
	parentheses := 0
	for position < len(walker.source) {
		if !walker.budget.take(1) {
			return false
		}
		switch walker.source[position] {
		case '\n', '\r':
			return false
		case '\\':
			position++
			if position < len(walker.source) {
				if swiftPhysicalLineBreakWidth(walker.source, position) > 0 {
					return false
				}
				_, size := utf8.DecodeRuneInString(walker.source[position:])
				size = max(size, 1)
				if !walker.budget.take(size) {
					return false
				}
				position += size
			}
			continue
		case '[':
			classDepth++
		case ']':
			classDepth = max(0, classDepth-1)
		case '(':
			if classDepth == 0 {
				parentheses++
			}
		case ')':
			if classDepth == 0 {
				if parentheses == 0 {
					return false
				}
				parentheses--
			}
		case '/':
			if position > start+1 {
				if walker.source[position-1] == ' ' || walker.source[position-1] == '\t' {
					return false
				}
				position++
				walker.offset = position
				walker.emitLiteralSpan(start, position)
				walker.emitLiteralState(swiftLiteral{
					start: start, end: position, terminated: true, regex: true,
				})
				return true
			}
		}
		_, size := utf8.DecodeRuneInString(walker.source[position:])
		size = max(size, 1)
		if size > 1 && !walker.budget.take(size-1) {
			return false
		}
		position += size
	}
	return false
}

func (walker *swiftLexicalWalker) scanHashTokens(hashCount int) {
	start := walker.offset
	if walker.onlyHorizontalSpaceBefore(start) {
		for _, directive := range []string{"#elseif", "#endif", "#else", "#if"} {
			if swiftSourceWordAt(walker.source, start, directive) {
				walker.offset += len(directive)
				walker.emitToken(swiftToken{
					text: directive, start: start, end: walker.offset,
					nameStart: start, kind: swiftTokenPunctuation,
				})
				return
			}
		}
	}
	hashCount = max(1, min(hashCount, len(walker.source)-start))
	walker.status.maximumRawHashCount = max(
		walker.status.maximumRawHashCount, hashCount,
	)
	for range hashCount {
		tokenStart := walker.offset
		walker.offset++
		walker.emitToken(swiftToken{
			text: "#", start: tokenStart, end: walker.offset,
			nameStart: tokenStart, kind: swiftTokenPunctuation,
		})
		if walker.stop {
			return
		}
	}
}

func (walker *swiftLexicalWalker) scanQuotedIdentifier() {
	start := walker.offset
	walker.offset++
	nameStart := walker.offset
	terminated := false
	for walker.offset < len(walker.source) {
		value, size := utf8.DecodeRuneInString(walker.source[walker.offset:])
		if value == utf8.RuneError && size == 1 || value == '\n' || value == '\r' ||
			value == '\u0085' || value == '\u2028' || value == '\u2029' {
			break
		}
		if value == '`' {
			terminated = true
			break
		}
		walker.offset += max(size, 1)
	}
	nameEnd := walker.offset
	if terminated {
		walker.offset++
	}
	if !terminated || !swiftIdentifierTextValid(walker.source[nameStart:nameEnd]) {
		walker.emitToken(swiftToken{
			text: walker.source[start:walker.offset], start: start, end: walker.offset,
			nameStart: start, kind: swiftTokenPunctuation,
		})
		return
	}
	walker.emitToken(swiftToken{
		text: walker.source[nameStart:nameEnd], start: start, end: walker.offset,
		nameStart: nameStart, kind: swiftTokenIdentifier, quotedIdentifier: true,
	})
}

func (walker *swiftLexicalWalker) scanDollarIdentifier() {
	start := walker.offset
	walker.offset++
	if walker.offset < len(walker.source) && walker.source[walker.offset] >= '0' &&
		walker.source[walker.offset] <= '9' {
		for walker.offset < len(walker.source) && walker.source[walker.offset] >= '0' &&
			walker.source[walker.offset] <= '9' {
			walker.offset++
		}
	} else {
		walker.scanIdentifierTail()
	}
	walker.emitToken(swiftToken{
		text: walker.source[start:walker.offset], start: start, end: walker.offset,
		nameStart: start, kind: swiftTokenIdentifier,
	})
}

func (walker *swiftLexicalWalker) scanIdentifier() {
	start := walker.offset
	walker.scanIdentifierTail()
	walker.emitToken(swiftToken{
		text: walker.source[start:walker.offset], start: start, end: walker.offset,
		nameStart: start, kind: swiftTokenIdentifier,
	})
}

func (walker *swiftLexicalWalker) scanIdentifierTail() {
	for walker.offset < len(walker.source) {
		runeValue, size := utf8.DecodeRuneInString(walker.source[walker.offset:])
		if size < 1 || runeValue == utf8.RuneError && size == 1 ||
			!swiftIdentifierContinueRune(runeValue) {
			break
		}
		walker.offset += size
	}
}

func (walker *swiftLexicalWalker) scanNumber() {
	start := walker.offset
	walker.offset = swiftNumberLiteralEnd(walker.source, start)
	if walker.offset < len(walker.source) {
		value, size := utf8.DecodeRuneInString(walker.source[walker.offset:])
		if size > 0 && (value != utf8.RuneError || size != 1) &&
			swiftIdentifierContinueRune(value) {
			// Keep malformed adjacent suffixes opaque without consuming a range or
			// member-access dot after an otherwise complete numeric literal.
			walker.scanIdentifierTail()
		}
	}
	walker.emitToken(swiftToken{
		text: walker.source[start:walker.offset], start: start, end: walker.offset,
		nameStart: start, kind: swiftTokenNumber,
	})
}

func swiftNumberLiteralEnd(source string, start int) int {
	if start < 0 || start >= len(source) || source[start] < '0' || source[start] > '9' {
		return max(0, min(start, len(source)))
	}
	if start+1 < len(source) && source[start] == '0' {
		switch source[start+1] {
		case 'b', 'B':
			end, _ := swiftNumberDigitRunEnd(source, start+2, 2)
			return end
		case 'o', 'O':
			end, _ := swiftNumberDigitRunEnd(source, start+2, 8)
			return end
		case 'x', 'X':
			integerEnd, _ := swiftNumberDigitRunEnd(source, start+2, 16)
			if exponentEnd, ok := swiftNumberExponentEnd(source, integerEnd, 'p', 'P'); ok {
				return exponentEnd
			}
			if integerEnd < len(source) && source[integerEnd] == '.' &&
				(integerEnd+1 >= len(source) || source[integerEnd+1] != '.') {
				fractionEnd, hasFractionDigit := swiftNumberDigitRunEnd(
					source, integerEnd+1, 16,
				)
				if hasFractionDigit {
					if exponentEnd, ok := swiftNumberExponentEnd(
						source, fractionEnd, 'p', 'P',
					); ok {
						return exponentEnd
					}
				}
			}
			return integerEnd
		}
	}

	integerEnd, _ := swiftNumberDigitRunEnd(source, start, 10)
	end := integerEnd
	if end+1 < len(source) && source[end] == '.' && source[end+1] != '.' &&
		source[end+1] >= '0' && source[end+1] <= '9' {
		end, _ = swiftNumberDigitRunEnd(source, end+1, 10)
	}
	if exponentEnd, ok := swiftNumberExponentEnd(source, end, 'e', 'E'); ok {
		return exponentEnd
	}
	return end
}

func swiftNumberDigitRunEnd(source string, start, base int) (int, bool) {
	end := max(0, min(start, len(source)))
	hasDigit := false
	for end < len(source) {
		character := source[end]
		if character == '_' {
			end++
			continue
		}
		if !swiftNumberDigit(character, base) {
			break
		}
		hasDigit = true
		end++
	}
	return end, hasDigit
}

func swiftNumberDigit(character byte, base int) bool {
	switch {
	case character >= '0' && character <= '9':
		return int(character-'0') < base
	case character >= 'a' && character <= 'f':
		return base == 16
	case character >= 'A' && character <= 'F':
		return base == 16
	default:
		return false
	}
}

func swiftNumberExponentEnd(
	source string,
	start int,
	lower, upper byte,
) (int, bool) {
	if start < 0 || start >= len(source) ||
		(source[start] != lower && source[start] != upper) {
		return start, false
	}
	end := start + 1
	if end < len(source) && (source[end] == '+' || source[end] == '-') {
		end++
	}
	if end >= len(source) || source[end] < '0' || source[end] > '9' {
		return start, false
	}
	end, _ = swiftNumberDigitRunEnd(source, end, 10)
	return end, true
}

func (walker *swiftLexicalWalker) scanPunctuationOrOperator() {
	start := walker.offset
	switch {
	case start+1 < len(walker.source) && walker.source[start:start+2] == "::":
		walker.offset += 2
	case swiftOperatorRuneAt(walker.source, start):
		first, size := utf8.DecodeRuneInString(walker.source[start:])
		walker.offset += max(size, 1)
		prefixPosition := walker.expressionCanStartAt(start)
		for walker.offset < len(walker.source) {
			next, nextSize := utf8.DecodeRuneInString(walker.source[walker.offset:])
			if next == '/' && prefixPosition && walker.offset+1 < len(walker.source) &&
				walker.source[walker.offset+1] != '/' && walker.source[walker.offset+1] != '*' &&
				!swiftBareRegexForbiddenOpening(walker.source[walker.offset+1]) {
				break
			}
			if !swiftOperatorContinueRune(first, next) {
				break
			}
			walker.offset += max(nextSize, 1)
		}
	default:
		_, size := utf8.DecodeRuneInString(walker.source[start:])
		walker.offset += max(size, 1)
	}
	walker.emitToken(swiftToken{
		text: walker.source[start:walker.offset], start: start, end: walker.offset,
		nameStart: start, kind: swiftTokenPunctuation,
	})
}

func swiftConcreteFrontier(source string) (maximumDepth, maximumDirectives int, eligible bool) {
	eligible = true
	delimiters := make([]byte, 0, swiftMaximumConcreteDelimiterDepth)
	type directiveFrame struct{ baseDepth int }
	directives := make([]directiveFrame, 0, swiftMaximumConcreteDirectiveDepth)
	overflow := 0
	walkSwiftLexically(source, swiftLexicalSink{token: func(token swiftToken) bool {
		if token.gap {
			eligible = false
			return true
		}
		switch token.text {
		case "#if":
			maximumDirectives = max(maximumDirectives, len(directives)+overflow+1)
			if len(directives) >= swiftMaximumConcreteDirectiveDepth {
				overflow++
				eligible = false
			} else if overflow == 0 {
				directives = append(directives, directiveFrame{baseDepth: len(delimiters)})
			}
			return true
		case "#elseif", "#else":
			if overflow > 0 || len(directives) == 0 {
				eligible = false
				return true
			}
			base := directives[len(directives)-1].baseDepth
			if len(delimiters) != base {
				eligible = false
			}
			delimiters = delimiters[:min(base, len(delimiters))]
			return true
		case "#endif":
			if overflow > 0 {
				overflow--
				return true
			}
			if len(directives) == 0 {
				eligible = false
				return true
			}
			base := directives[len(directives)-1].baseDepth
			if len(delimiters) != base {
				eligible = false
			}
			delimiters = delimiters[:min(base, len(delimiters))]
			directives = directives[:len(directives)-1]
			return true
		}
		if token.kind == swiftTokenInterpolationStart {
			if len(delimiters) >= swiftMaximumConcreteDelimiterDepth {
				eligible = false
				return true
			}
			delimiters = append(delimiters, '(')
			maximumDepth = max(maximumDepth, len(delimiters))
			return true
		}
		if token.kind == swiftTokenInterpolationEnd {
			if len(delimiters) == 0 || delimiters[len(delimiters)-1] != '(' {
				eligible = false
			} else {
				delimiters = delimiters[:len(delimiters)-1]
			}
			return true
		}
		if len(token.text) != 1 {
			return true
		}
		switch token.text[0] {
		case '(', '[', '{':
			if len(delimiters) >= swiftMaximumConcreteDelimiterDepth {
				eligible = false
				return true
			}
			delimiters = append(delimiters, token.text[0])
			maximumDepth = max(maximumDepth, len(delimiters))
		case ')', ']', '}':
			var want byte
			switch token.text[0] {
			case ')':
				want = '('
			case ']':
				want = '['
			case '}':
				want = '{'
			}
			if len(delimiters) == 0 || delimiters[len(delimiters)-1] != want {
				eligible = false
			} else {
				delimiters = delimiters[:len(delimiters)-1]
			}
		}
		return true
	}})
	return maximumDepth, maximumDirectives,
		eligible && len(delimiters) == 0 && len(directives) == 0 && overflow == 0
}

func swiftASCIIWhitespace(character byte) bool {
	switch character {
	case ' ', '\t', '\n', '\r', '\v', '\f', '\x00':
		return true
	default:
		return false
	}
}

func swiftBareRegexForbiddenOpening(character byte) bool {
	return character == ' ' || character == '\t' || character == '\r' || character == '\n'
}

func swiftLineOnlyIndentationByte(character byte) bool {
	switch character {
	case ' ', '\t', '\v', '\f', '\x00':
		return true
	default:
		return false
	}
}

func swiftPhysicalLineBreakWidth(source string, offset int) int {
	if offset < 0 || offset >= len(source) {
		return 0
	}
	switch source[offset] {
	case '\n':
		return 1
	case '\r':
		if offset+1 < len(source) && source[offset+1] == '\n' {
			return 2
		}
		return 1
	default:
		return 0
	}
}

func swiftSourceAttachmentGap(source string, start, end int) bool {
	if start < 0 || end < start || end > len(source) {
		return false
	}
	lineBreaks := 0
	for offset := start; offset < end; {
		switch source[offset] {
		case ' ', '\t', '\v', '\f', '\x00':
			offset++
		case '\n':
			lineBreaks++
			offset++
		case '\r':
			lineBreaks++
			offset += swiftPhysicalLineBreakWidth(source, offset)
		default:
			return false
		}
		if lineBreaks > 1 {
			return false
		}
	}
	return true
}

func swiftPreviousSignificantByte(source string, offset int) int {
	for position := offset - 1; position >= 0; position-- {
		if !swiftASCIIWhitespace(source[position]) {
			return position
		}
	}
	return -1
}

func swiftPreviousWord(source string, offset int) string {
	position := offset
	for position > 0 && swiftASCIIWhitespace(source[position-1]) {
		position--
	}
	end := position
	for position > 0 {
		character := source[position-1]
		if character == '_' || character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' {
			position--
			continue
		}
		break
	}
	return source[position:end]
}

func swiftSourceWordAt(source string, start int, word string) bool {
	if start < 0 || start+len(word) > len(source) || source[start:start+len(word)] != word {
		return false
	}
	end := start + len(word)
	return end == len(source) || !swiftIdentifierContinueAt(source, end)
}

func swiftIdentifierStartAt(source string, offset int) bool {
	if offset < 0 || offset >= len(source) {
		return false
	}
	runeValue, size := utf8.DecodeRuneInString(source[offset:])
	return (runeValue != utf8.RuneError || size != 1) &&
		swiftIdentifierStartRune(runeValue)
}

func swiftIdentifierAt(
	source string,
	start int,
) (text string, end, nameStart int, ok bool) {
	if start < 0 || start >= len(source) {
		return "", start, start, false
	}
	if source[start] == '`' {
		cursor := start + 1
		for cursor < len(source) {
			value, size := utf8.DecodeRuneInString(source[cursor:])
			if value == utf8.RuneError && size == 1 {
				return "", start, start, false
			}
			size = max(size, 1)
			if value == '`' {
				text := source[start+1 : cursor]
				if !swiftIdentifierTextValid(text) {
					return "", start, start, false
				}
				return text, cursor + 1, start + 1, true
			}
			if value == '\n' || value == '\r' || value == '\u0085' ||
				value == '\u2028' || value == '\u2029' {
				return "", start, start, false
			}
			cursor += size
		}
		return "", start, start, false
	}
	value, size := utf8.DecodeRuneInString(source[start:])
	if value == utf8.RuneError && size == 1 || !swiftIdentifierStartRune(value) {
		return "", start, start, false
	}
	cursor := start + max(size, 1)
	for cursor < len(source) {
		value, size = utf8.DecodeRuneInString(source[cursor:])
		if value == utf8.RuneError && size == 1 || !swiftIdentifierContinueRune(value) {
			break
		}
		cursor += max(size, 1)
	}
	return source[start:cursor], cursor, start, true
}

func swiftIdentifierContinueAt(source string, offset int) bool {
	if offset < 0 || offset >= len(source) {
		return false
	}
	runeValue, size := utf8.DecodeRuneInString(source[offset:])
	return (runeValue != utf8.RuneError || size != 1) &&
		swiftIdentifierContinueRune(runeValue)
}

func swiftIdentifierStartRune(value rune) bool {
	return value >= 'A' && value <= 'Z' || value == '_' ||
		value >= 'a' && value <= 'z' ||
		value == 0x00a8 || value == 0x00aa || value == 0x00ad || value == 0x00af ||
		value >= 0x00b2 && value <= 0x00b5 ||
		value >= 0x00b7 && value <= 0x00ba ||
		value >= 0x00bc && value <= 0x00be ||
		value >= 0x00c0 && value <= 0x00d6 ||
		value >= 0x00d8 && value <= 0x00f6 ||
		value >= 0x00f8 && value <= 0x02ff ||
		value >= 0x0370 && value <= 0x167f ||
		value >= 0x1681 && value <= 0x180d ||
		value >= 0x180f && value <= 0x1dbf ||
		value >= 0x1e00 && value <= 0x1fff ||
		value >= 0x200b && value <= 0x200d ||
		value >= 0x202a && value <= 0x202e ||
		value >= 0x203f && value <= 0x2040 || value == 0x2054 ||
		value >= 0x2060 && value <= 0x206f ||
		value >= 0x2070 && value <= 0x20cf ||
		value >= 0x2100 && value <= 0x218f ||
		value >= 0x2460 && value <= 0x24ff ||
		value >= 0x2776 && value <= 0x2793 ||
		value >= 0x2c00 && value <= 0x2dff ||
		value >= 0x2e80 && value <= 0x2fff ||
		value >= 0x3004 && value <= 0x3007 ||
		value >= 0x3021 && value <= 0x302f ||
		value >= 0x3031 && value <= 0x303f ||
		value >= 0x3040 && value <= 0xd7ff ||
		value >= 0xf900 && value <= 0xfd3d ||
		value >= 0xfd40 && value <= 0xfdcf ||
		value >= 0xfdf0 && value <= 0xfe1f ||
		value >= 0xfe30 && value <= 0xfe44 ||
		value >= 0xfe47 && value <= 0xfffd ||
		swiftSupplementaryIdentifierHead(value)
}

func swiftIdentifierContinueRune(value rune) bool {
	return swiftIdentifierStartRune(value) || value >= '0' && value <= '9' ||
		value >= 0x0300 && value <= 0x036f ||
		value >= 0x1dc0 && value <= 0x1dff ||
		value >= 0x20d0 && value <= 0x20ff ||
		value >= 0xfe20 && value <= 0xfe2f
}

func swiftSupplementaryIdentifierHead(value rune) bool {
	if value < 0x10000 || value > 0xefffd {
		return false
	}
	// Swift admits every non-private-use supplementary-plane scalar except
	// each plane's terminal noncharacters U+?FFFE and U+?FFFF.
	return value&0xffff <= 0xfffd
}

func swiftIdentifierTextValid(value string) bool {
	if value == "" || !utf8.ValidString(value) {
		return false
	}
	first, size := utf8.DecodeRuneInString(value)
	if !swiftIdentifierStartRune(first) {
		return false
	}
	for offset := size; offset < len(value); {
		character, characterSize := utf8.DecodeRuneInString(value[offset:])
		if characterSize < 1 || !swiftIdentifierContinueRune(character) {
			return false
		}
		offset += characterSize
	}
	return true
}

func swiftOperatorRuneAt(source string, offset int) bool {
	if offset < 0 || offset >= len(source) {
		return false
	}
	value, _ := utf8.DecodeRuneInString(source[offset:])
	return swiftOperatorHeadRune(value)
}

func swiftOperatorHeadRune(value rune) bool {
	if strings.ContainsRune("/=+-!*%<>&|^?~.", value) {
		return true
	}
	return value >= 0x00a1 && value <= 0x00a7 || value == 0x00a9 ||
		value == 0x00ab || value == 0x00ac || value == 0x00ae ||
		value >= 0x00b0 && value <= 0x00b1 || value == 0x00b6 ||
		value == 0x00bb || value == 0x00bf || value == 0x00d7 || value == 0x00f7 ||
		value >= 0x2016 && value <= 0x2017 ||
		value >= 0x2020 && value <= 0x2027 ||
		value >= 0x2030 && value <= 0x203e ||
		value >= 0x2041 && value <= 0x2053 ||
		value >= 0x2055 && value <= 0x205e ||
		value >= 0x2190 && value <= 0x23ff ||
		value >= 0x2500 && value <= 0x2775 || value >= 0x2794 && value <= 0x2bff ||
		value >= 0x2e00 && value <= 0x2e7f ||
		value >= 0x3001 && value <= 0x3003 ||
		value >= 0x3008 && value <= 0x3020 ||
		value == 0x3030
}

func swiftOperatorContinueRune(first, value rune) bool {
	if value == '.' && first != '.' {
		return false
	}
	return swiftOperatorHeadRune(value) || value >= 0x0300 && value <= 0x036f ||
		value >= 0x1dc0 && value <= 0x1dff || value >= 0x20d0 && value <= 0x20ff ||
		value >= 0xfe00 && value <= 0xfe0f || value >= 0xfe20 && value <= 0xfe2f ||
		value >= 0xe0100 && value <= 0xe01ef
}

func swiftLineStarts(source string) []int {
	starts := []int{0}
	for index := range len(source) {
		if source[index] == '\n' {
			starts = append(starts, index+1)
		}
	}
	return starts
}

func swiftTokenLine(lineStarts []int, offset int) int {
	if len(lineStarts) == 0 {
		return 1
	}
	offset = max(offset, 0)
	return sort.Search(len(lineStarts), func(index int) bool {
		return lineStarts[index] > offset
	})
}

func swiftHardKeyword(word string) bool {
	switch word {
	case "associatedtype", "break", "case", "catch", "class", "continue",
		"Any", "as", "await", "default", "defer", "deinit", "do", "else",
		"enum", "extension",
		"fallthrough", "false", "fileprivate", "for", "func", "guard", "if",
		"import", "in", "init", "inout", "internal", "is", "let", "macro",
		"nil", "nonisolated", "open", "operator", "precedencegroup", "private", "protocol",
		"public", "rethrows", "return", "self", "Self", "static", "struct",
		"subscript", "super", "switch", "throw", "throws", "true", "try",
		"typealias", "var", "where", "while", "yield":
		return true
	default:
		return false
	}
}

func swiftNameToken(token swiftToken) bool {
	if token.kind != swiftTokenIdentifier || token.text == "" || token.gap {
		return false
	}
	return token.quotedIdentifier || !swiftHardKeyword(token.text)
}

func swiftOperatorSymbol(symbol string) bool {
	if symbol == "" || symbol == "." || symbol == "::" {
		return false
	}
	if !utf8.ValidString(symbol) {
		return false
	}
	first, size := utf8.DecodeRuneInString(symbol)
	if !swiftOperatorHeadRune(first) {
		return false
	}
	for offset := size; offset < len(symbol); {
		value, valueSize := utf8.DecodeRuneInString(symbol[offset:])
		if valueSize < 1 || !swiftOperatorContinueRune(first, value) {
			return false
		}
		offset += valueSize
	}
	return true
}
