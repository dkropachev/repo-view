package navigator

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	kotlinMaximumRetainedTokens      = 256 << 10
	kotlinMaximumRetainedSpans       = 256 << 10
	kotlinMaximumStructuralDepth     = 512
	kotlinMaximumHeaderTokens        = 4096
	kotlinMaximumInterpolationDepth  = 128
	kotlinMaximumInterpolationPrefix = 255
	kotlinMaximumSuspendedHeaders    = 64
	kotlinLexicalLookaheadMultiplier = 8
	kotlinMinimumLookaheadBudget     = 64 << 10
)

const (
	kotlinTokenGap     = "\x00kotlin-token-gap\x00"
	kotlinLiteralToken = "\x00kotlin-literal\x00"
)

type kotlinTokenKind uint8

const (
	kotlinTokenIdentifier kotlinTokenKind = iota
	kotlinTokenNumber
	kotlinTokenPunctuation
)

type kotlinToken struct {
	text             string
	start, end       int
	nameStart        int
	kind             kotlinTokenKind
	lineStart, gap   bool
	quotedIdentifier bool
	interpolation    bool
}

type kotlinLexResult struct {
	tokens                []kotlinToken
	commentSpans          []cByteSpan
	stringSpans           []cByteSpan
	lexicalUnits          int
	maximumDelimiterDepth int
	truncated             bool
	spansTruncated        bool
	concreteEligible      bool
}

type kotlinTokenRetention struct {
	head      []kotlinToken
	tail      []kotlinToken
	tailStart int
	total     int
}

func (retention *kotlinTokenRetention) append(token kotlinToken) {
	retention.total++
	headLimit := (kotlinMaximumRetainedTokens - 1) / 2
	tailLimit := kotlinMaximumRetainedTokens - headLimit
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

func (retention *kotlinTokenRetention) result() ([]kotlinToken, bool) {
	if retention.total <= kotlinMaximumRetainedTokens {
		return append(retention.head, retention.tail...), false
	}
	tokens := make([]kotlinToken, 0, kotlinMaximumRetainedTokens)
	tokens = append(tokens, retention.head...)
	tokens = append(tokens, kotlinToken{
		text: kotlinTokenGap, kind: kotlinTokenPunctuation, gap: true,
	})
	tailCount := kotlinMaximumRetainedTokens - len(retention.head) - 1
	tailCount = min(tailCount, len(retention.tail))
	skip := len(retention.tail) - tailCount
	for index := range tailCount {
		position := (retention.tailStart + skip + index) % len(retention.tail)
		tokens = append(tokens, retention.tail[position])
	}
	return tokens, true
}

type kotlinLexicalSink struct {
	comment      func(cByteSpan) bool
	literal      func(cByteSpan) bool
	literalState func(kotlinLiteral) bool
	token        func(kotlinToken) bool
}

type kotlinLexicalBudget struct {
	remaining int
	exhausted bool
	degraded  bool
}

func newKotlinLexicalBudget(sourceLength int) *kotlinLexicalBudget {
	maximumInt := int(^uint(0) >> 1)
	remaining := kotlinMinimumLookaheadBudget
	if sourceLength > (maximumInt-remaining)/kotlinLexicalLookaheadMultiplier {
		remaining = maximumInt
	} else if sourceLength > 0 {
		remaining += sourceLength * kotlinLexicalLookaheadMultiplier
	}
	return &kotlinLexicalBudget{remaining: remaining}
}

func (budget *kotlinLexicalBudget) take(amount int) bool {
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

func lexKotlin(source string) kotlinLexResult {
	result := kotlinLexResult{concreteEligible: true}
	var retention kotlinTokenRetention
	appendSpan := func(destination *[]cByteSpan, span cByteSpan) bool {
		if len(*destination) < kotlinMaximumRetainedSpans {
			*destination = append(*destination, span)
		} else {
			result.spansTruncated = true
		}
		return true
	}
	withinLookaheadBudget := walkKotlinLexically(source, kotlinLexicalSink{
		comment: func(span cByteSpan) bool {
			result.lexicalUnits++
			return appendSpan(&result.commentSpans, span)
		},
		literal: func(span cByteSpan) bool {
			return appendSpan(&result.stringSpans, span)
		},
		literalState: func(literal kotlinLiteral) bool {
			if !literal.terminated {
				result.concreteEligible = false
			}
			return true
		},
		token: func(token kotlinToken) bool {
			retention.append(token)
			result.lexicalUnits++
			return true
		},
	})
	result.tokens, result.truncated = retention.result()
	result.commentSpans = normalizeCSpans(result.commentSpans)
	result.stringSpans = normalizeCSpans(result.stringSpans)
	maximumDepth, frontierEligible := kotlinConcreteFrontier(source)
	result.maximumDelimiterDepth = maximumDepth
	result.concreteEligible = result.concreteEligible && frontierEligible &&
		withinLookaheadBudget
	if result.lexicalUnits > kotlinMaximumConcreteTokens ||
		len(source) > kotlinMaximumConcreteParseBytes || result.spansTruncated {
		result.concreteEligible = false
	}
	return result
}

func walkKotlinLexically(source string, sink kotlinLexicalSink) bool {
	budget := newKotlinLexicalBudget(len(source))
	kotlinWalkLexicallyWithState(source, sink, false, 0, budget)
	return !budget.exhausted
}

func kotlinWalkLexicallyWithState(
	source string,
	sink kotlinLexicalSink,
	lineHasCode bool,
	interpolationDepth int,
	budget *kotlinLexicalBudget,
) {
	for offset := 0; offset < len(source); {
		if offset == 0 && strings.HasPrefix(source, "\uFEFF") {
			offset += len("\uFEFF")
			continue
		}
		if strings.HasPrefix(source[offset:], "//") {
			end := kotlinLineEnd(source, offset)
			if sink.comment != nil && !sink.comment(cByteSpan{start: offset, end: end}) {
				return
			}
			lineHasCode = true
			offset = end
			continue
		}
		if strings.HasPrefix(source[offset:], "/*") {
			end := kotlinBlockCommentEnd(source, offset)
			if sink.comment != nil && !sink.comment(cByteSpan{start: offset, end: end}) {
				return
			}
			lineHasCode = kotlinRangeEndsWithCodeLine(source, offset, end, lineHasCode)
			offset = max(offset+2, end)
			continue
		}
		if literal, ok := kotlinLiteralAtDepth(
			source, offset, interpolationDepth, budget,
		); ok {
			if sink.literalState != nil && !sink.literalState(literal) {
				return
			}
			if !kotlinEmitLiteral(source, literal, sink, interpolationDepth, budget) {
				return
			}
			if sink.token != nil && !sink.token(kotlinToken{
				text:  kotlinLiteralToken,
				start: max(literal.start, literal.end-1), end: literal.end,
				nameStart: literal.start, kind: kotlinTokenNumber,
			}) {
				return
			}
			lineHasCode = kotlinRangeEndsWithCodeLine(
				source, offset, literal.end, lineHasCode,
			)
			offset = max(offset+1, literal.end)
			continue
		}

		r, size := utf8.DecodeRuneInString(source[offset:])
		size = max(size, 1)
		if kotlinWhitespace(r) {
			if kotlinLineBreak(r) {
				lineHasCode = false
			}
			offset += size
			continue
		}

		token := kotlinToken{
			start: offset, nameStart: offset, lineStart: !lineHasCode,
		}
		if text, end, nameStart, quoted, ok := kotlinIdentifierAt(source, offset); ok {
			token.text = text
			token.end = end
			token.nameStart = nameStart
			token.kind = kotlinTokenIdentifier
			token.quotedIdentifier = quoted
		} else if kotlinNumberStart(source, offset) {
			token.end = kotlinNumberEnd(source, offset)
			token.text = source[offset:token.end]
			token.kind = kotlinTokenNumber
		} else {
			token.text, token.end = kotlinPunctuationAt(source, offset)
			token.kind = kotlinTokenPunctuation
		}
		if token.end <= offset {
			token.end = min(len(source), offset+size)
		}
		if sink.token != nil && !sink.token(token) {
			return
		}
		lineHasCode = true
		offset = token.end
	}
}

type kotlinLiteral struct {
	start, end               int
	contentStart, contentEnd int
	dollars                  int
	triple, character        bool
	terminated               bool
}

func kotlinLiteralAtDepth(
	source string,
	start, interpolationDepth int,
	budget *kotlinLexicalBudget,
) (kotlinLiteral, bool) {
	if start < 0 || start >= len(source) {
		return kotlinLiteral{}, false
	}
	if budget != nil && budget.exhausted {
		if budget.degraded {
			return kotlinConservativeLiteralAt(source, start)
		}
		return kotlinRecoveryLiteralAt(source, start)
	}
	if source[start] == '\'' {
		end, terminated := kotlinQuotedEnd(source, start+1, '\'', budget)
		literal := kotlinLiteral{
			start: start, end: end, contentStart: start + 1,
			contentEnd: max(start+1, end-boolInt(terminated)),
			character:  true, terminated: terminated,
		}
		return kotlinRecoverExhaustedLiteral(
			source, literal, interpolationDepth, budget,
		), true
	}
	cursor := start
	dollars := 0
	for cursor < len(source) && source[cursor] == '$' {
		if !budget.take(1) {
			literal := kotlinLiteral{
				start: start, end: max(start+1, cursor),
				contentStart: start, contentEnd: max(start+1, cursor),
			}
			return kotlinRecoverExhaustedLiteral(
				source, literal, interpolationDepth, budget,
			), true
		}
		dollars++
		cursor++
	}
	if cursor >= len(source) || source[cursor] != '"' {
		return kotlinLiteral{}, false
	}
	quotes := kotlinByteRun(source, cursor, '"')
	if !budget.take(quotes) {
		literal := kotlinLiteral{
			start: start, end: max(start+1, cursor),
			contentStart: cursor, contentEnd: max(cursor, start+1),
		}
		return kotlinRecoverExhaustedLiteral(
			source, literal, interpolationDepth, budget,
		), true
	}
	triple := quotes >= 3
	requiredDollars := kotlinRequiredInterpolationDollars(dollars)
	if !triple {
		end, terminated := kotlinRegularStringEnd(
			source, cursor+1, requiredDollars, interpolationDepth, budget,
		)
		literal := kotlinLiteral{
			start: start, end: end, contentStart: cursor + 1,
			contentEnd: max(cursor+1, end-boolInt(terminated)), dollars: dollars,
			terminated: terminated,
		}
		return kotlinRecoverExhaustedLiteral(
			source, literal, interpolationDepth, budget,
		), true
	}
	contentStart := cursor + 3
	end, terminated := kotlinTripleQuotedEnd(
		source, contentStart, requiredDollars, interpolationDepth, budget,
	)
	contentEnd := end
	if terminated {
		contentEnd = max(contentStart, end-3)
	}
	literal := kotlinLiteral{
		start: start, end: end, contentStart: contentStart, contentEnd: contentEnd,
		dollars: dollars, triple: true, terminated: terminated,
	}
	return kotlinRecoverExhaustedLiteral(
		source, literal, interpolationDepth, budget,
	), true
}

func kotlinRecoverExhaustedLiteral(
	source string,
	literal kotlinLiteral,
	interpolationDepth int,
	budget *kotlinLexicalBudget,
) kotlinLiteral {
	if interpolationDepth != 0 || literal.terminated || literal.end >= len(source) {
		return literal
	}
	if literal.triple {
		literal.end, literal.terminated = kotlinDegradedTripleQuotedEnd(
			source,
			max(literal.contentStart, literal.end),
			kotlinRequiredInterpolationDollars(literal.dollars),
		)
		literal.contentEnd = literal.end
		if literal.terminated {
			literal.contentEnd = max(literal.contentStart, literal.end-3)
		}
		return literal
	}
	if budget == nil || !budget.exhausted {
		return literal
	}
	literal.end = kotlinLineEnd(source, max(literal.start+1, literal.end))
	literal.contentEnd = max(literal.contentStart, literal.end)
	literal.terminated = false
	return literal
}

func kotlinRecoveryLiteralAt(source string, start int) (kotlinLiteral, bool) {
	if source[start] != '\'' && source[start] != '"' &&
		(source[start] != '$' || start+1 >= len(source) || source[start+1] != '"') {
		return kotlinLiteral{}, false
	}
	localBudget := newKotlinLexicalBudget(len(source) - start)
	localBudget.degraded = true
	return kotlinLiteralAtDepth(source, start, 0, localBudget)
}

func kotlinConservativeLiteralAt(source string, start int) (kotlinLiteral, bool) {
	if source[start] == '\'' {
		end, terminated := kotlinQuotedEnd(source, start+1, '\'', nil)
		return kotlinLiteral{
			start: start, end: end, contentStart: start + 1,
			contentEnd: max(start+1, end-boolInt(terminated)),
			character:  true, terminated: terminated,
		}, true
	}
	cursor := start
	dollars := 0
	if source[cursor] == '$' {
		if cursor+1 >= len(source) || source[cursor+1] != '"' {
			return kotlinLiteral{}, false
		}
		dollars = 1
		cursor++
	}
	if cursor >= len(source) || source[cursor] != '"' {
		return kotlinLiteral{}, false
	}
	quotes := kotlinByteRun(source, cursor, '"')
	if quotes < 3 {
		end, terminated := kotlinDegradedRegularStringEnd(
			source, cursor+1, kotlinRequiredInterpolationDollars(dollars),
		)
		return kotlinLiteral{
			start: start, end: end, contentStart: cursor + 1,
			contentEnd: max(cursor+1, end-boolInt(terminated)),
			dollars:    dollars, terminated: terminated,
		}, true
	}
	contentStart := cursor + 3
	end, terminated := kotlinDegradedTripleQuotedEnd(
		source, contentStart, kotlinRequiredInterpolationDollars(dollars),
	)
	contentEnd := end
	if terminated {
		contentEnd = max(contentStart, end-3)
	}
	return kotlinLiteral{
		start: start, end: end, contentStart: contentStart, contentEnd: contentEnd,
		dollars: dollars, triple: true, terminated: terminated,
	}, true
}

func kotlinDegradedRegularStringEnd(
	source string,
	offset, requiredDollars int,
) (int, bool) {
	escaped := false
	for offset < len(source) {
		r, size := utf8.DecodeRuneInString(source[offset:])
		size = max(size, 1)
		if kotlinLineBreak(r) {
			return offset, false
		}
		if escaped {
			escaped = false
			offset += size
			continue
		}
		switch source[offset] {
		case '\\':
			escaped = true
			offset++
		case '"':
			return offset + 1, true
		case '$':
			run := kotlinByteRun(source, offset, '$')
			after := offset + run
			if run >= requiredDollars && after < len(source) && source[after] == '{' {
				return kotlinLineEnd(source, offset), false
			}
			offset = after
		default:
			offset += size
		}
	}
	return len(source), false
}

func kotlinDegradedTripleQuotedEnd(
	source string,
	offset, requiredDollars int,
) (int, bool) {
	for offset < len(source) {
		if source[offset] == '"' {
			run := kotlinByteRun(source, offset, '"')
			if run >= 3 {
				return offset + run, true
			}
			offset += run
			continue
		}
		if source[offset] == '$' {
			run := kotlinByteRun(source, offset, '$')
			after := offset + run
			if run >= requiredDollars && after < len(source) && source[after] == '{' {
				return len(source), false
			}
			offset = after
			continue
		}
		_, size := utf8.DecodeRuneInString(source[offset:])
		offset += max(size, 1)
	}
	return len(source), false
}

func kotlinEmitLiteral(
	source string,
	literal kotlinLiteral,
	sink kotlinLexicalSink,
	interpolationDepth int,
	budget *kotlinLexicalBudget,
) bool {
	if sink.literal == nil && sink.token == nil && sink.comment == nil &&
		sink.literalState == nil {
		return true
	}
	if literal.character || !literal.terminated {
		return sink.literal == nil || sink.literal(cByteSpan{
			start: literal.start, end: literal.end,
		})
	}
	requiredDollars := kotlinRequiredInterpolationDollars(literal.dollars)
	opaqueStart := literal.start
	for cursor := literal.contentStart; cursor < literal.contentEnd; {
		if !literal.triple && source[cursor] == '\\' {
			next := min(literal.contentEnd, cursor+2)
			if !budget.take(next - cursor) {
				break
			}
			cursor = next
			continue
		}
		if source[cursor] != '$' {
			_, size := utf8.DecodeRuneInString(source[cursor:literal.contentEnd])
			size = max(size, 1)
			if !budget.take(size) {
				break
			}
			cursor += size
			continue
		}
		run := kotlinByteRun(source[:literal.contentEnd], cursor, '$')
		if !budget.take(max(run, 1)) {
			break
		}
		if run < requiredDollars {
			cursor += max(run, 1)
			continue
		}
		after := cursor + run
		if after < literal.contentEnd && source[after] == '{' {
			if interpolationDepth >= kotlinMaximumInterpolationDepth {
				break
			}
			expressionEnd, closeEnd, found := kotlinInterpolationEndDepth(
				source, after+1, literal.contentEnd, interpolationDepth+1, budget,
			)
			if !found {
				break
			}
			if sink.literal != nil && opaqueStart < after+1 && !sink.literal(cByteSpan{
				start: opaqueStart, end: after + 1,
			}) {
				return false
			}
			if sink.token != nil && !sink.token(kotlinToken{
				text: "{", start: after, end: after + 1, nameStart: after,
				kind: kotlinTokenPunctuation, interpolation: true,
			}) {
				return false
			}
			if !kotlinWalkLexicalRange(
				source,
				after+1,
				expressionEnd,
				kotlinLexicalSink{
					comment:      sink.comment,
					literal:      sink.literal,
					literalState: sink.literalState,
					token:        sink.token,
				},
				interpolationDepth+1,
				budget,
			) {
				return false
			}
			if sink.token != nil && !sink.token(kotlinToken{
				text: "}", start: expressionEnd, end: closeEnd, nameStart: expressionEnd,
				kind: kotlinTokenPunctuation, interpolation: true,
			}) {
				return false
			}
			opaqueStart = expressionEnd
			cursor = closeEnd
			continue
		}
		if text, identifierEnd, nameStart, quoted, ok := kotlinIdentifierAt(source, after); ok &&
			text != "" && !quoted && !kotlinHardKeyword(text) {
			if sink.literal != nil && opaqueStart < nameStart && !sink.literal(cByteSpan{
				start: opaqueStart, end: nameStart,
			}) {
				return false
			}
			if sink.token != nil && !sink.token(kotlinToken{
				text: text, start: after, end: identifierEnd, nameStart: nameStart,
				kind: kotlinTokenIdentifier,
			}) {
				return false
			}
			opaqueStart = identifierEnd
			cursor = identifierEnd
			continue
		}
		cursor = after
	}
	return sink.literal == nil || opaqueStart >= literal.end || sink.literal(cByteSpan{
		start: opaqueStart, end: literal.end,
	})
}

func kotlinInterpolationEndDepth(
	source string,
	start, limit, interpolationDepth int,
	budget *kotlinLexicalBudget,
) (int, int, bool) {
	depth := 0
	for offset := start; offset < limit; {
		if strings.HasPrefix(source[offset:limit], "//") {
			end := kotlinLineEndWithin(source, offset, limit)
			if !budget.take(max(1, end-offset)) {
				return 0, 0, false
			}
			offset = end
			continue
		}
		if strings.HasPrefix(source[offset:limit], "/*") {
			end := min(limit, kotlinBlockCommentEnd(source[:limit], offset))
			if !budget.take(max(1, end-offset)) {
				return 0, 0, false
			}
			offset = end
			continue
		}
		if literal, ok := kotlinLiteralAtDepth(
			source[:limit], offset, interpolationDepth, budget,
		); ok {
			if !literal.terminated {
				return 0, 0, false
			}
			offset = max(offset+1, literal.end)
			continue
		}
		switch source[offset] {
		case '{':
			if !budget.take(1) {
				return 0, 0, false
			}
			depth++
			offset++
		case '}':
			if !budget.take(1) {
				return 0, 0, false
			}
			if depth == 0 {
				return offset, offset + 1, true
			}
			depth--
			offset++
		default:
			_, size := utf8.DecodeRuneInString(source[offset:limit])
			size = max(size, 1)
			if !budget.take(size) {
				return 0, 0, false
			}
			offset += size
		}
	}
	return 0, 0, false
}

func kotlinWalkLexicalRange(
	source string,
	start, end int,
	sink kotlinLexicalSink,
	interpolationDepth int,
	budget *kotlinLexicalBudget,
) bool {
	if start < 0 || end < start || end > len(source) {
		return false
	}
	completed := true
	local := kotlinLexicalSink{}
	if sink.comment != nil {
		local.comment = func(span cByteSpan) bool {
			completed = sink.comment(cByteSpan{start: start + span.start, end: start + span.end})
			return completed
		}
	}
	if sink.literal != nil {
		local.literal = func(span cByteSpan) bool {
			completed = sink.literal(cByteSpan{start: start + span.start, end: start + span.end})
			return completed
		}
	}
	if sink.literalState != nil {
		local.literalState = func(literal kotlinLiteral) bool {
			literal.start += start
			literal.end += start
			literal.contentStart += start
			literal.contentEnd += start
			completed = sink.literalState(literal)
			return completed
		}
	}
	if sink.token != nil {
		local.token = func(token kotlinToken) bool {
			token.start += start
			token.end += start
			token.nameStart += start
			completed = sink.token(token)
			return completed
		}
	}
	kotlinWalkLexicallyWithState(
		source[start:end], local, true, interpolationDepth, budget,
	)
	return completed
}

func kotlinConcreteFrontier(source string) (maximumDepth int, eligible bool) {
	eligible = true
	delimiters := make([]byte, 0, kotlinMaximumConcreteDelimiterDepth)
	overflow := 0
	withinLookaheadBudget := walkKotlinLexically(source, kotlinLexicalSink{
		literal: func(cByteSpan) bool { return true },
		token: func(token kotlinToken) bool {
			if token.kind != kotlinTokenPunctuation {
				return true
			}
			switch token.text {
			case "(", "[", "{":
				if overflow > 0 || len(delimiters) == kotlinMaximumConcreteDelimiterDepth {
					overflow++
					eligible = false
					maximumDepth = max(maximumDepth,
						kotlinMaximumConcreteDelimiterDepth+overflow)
					return true
				}
				delimiters = append(delimiters, token.text[0])
				maximumDepth = max(maximumDepth, len(delimiters))
			case ")", "]", "}":
				if overflow > 0 {
					overflow--
					return true
				}
				want := byte('(')
				switch token.text {
				case "]":
					want = '['
				case "}":
					want = '{'
				}
				if len(delimiters) > 0 && delimiters[len(delimiters)-1] == want {
					delimiters = delimiters[:len(delimiters)-1]
				}
			}
			return true
		},
	})
	return maximumDepth, eligible && withinLookaheadBudget
}

func kotlinIdentifierAt(
	source string,
	start int,
) (text string, end, nameStart int, quoted, ok bool) {
	if start < 0 || start >= len(source) {
		return "", start, start, false, false
	}
	if source[start] == '`' {
		cursor := start + 1
		for cursor < len(source) {
			r, size := utf8.DecodeRuneInString(source[cursor:])
			size = max(size, 1)
			if source[cursor] == '`' {
				if cursor == start+1 {
					return "", start, start, false, false
				}
				return source[start+1 : cursor], cursor + 1, start + 1, true, true
			}
			if kotlinLineBreak(r) {
				return "", start, start, false, false
			}
			cursor += size
		}
		return "", start, start, false, false
	}
	r, size := utf8.DecodeRuneInString(source[start:])
	if !kotlinIdentifierStartRune(r) || r == utf8.RuneError && size == 1 {
		return "", start, start, false, false
	}
	cursor := start + max(size, 1)
	for cursor < len(source) {
		r, size = utf8.DecodeRuneInString(source[cursor:])
		if !kotlinIdentifierContinueRune(r) || r == utf8.RuneError && size == 1 {
			break
		}
		cursor += max(size, 1)
	}
	return source[start:cursor], cursor, start, false, true
}

func kotlinIdentifierStartRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.Is(unicode.Nl, r)
}

func kotlinIdentifierContinueRune(r rune) bool {
	return kotlinIdentifierStartRune(r) || unicode.IsDigit(r) ||
		unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Mc, r) ||
		unicode.Is(unicode.Pc, r)
}

func kotlinKeywordToken(token kotlinToken) bool {
	return token.kind == kotlinTokenIdentifier && !token.quotedIdentifier &&
		kotlinHardKeyword(token.text)
}

func kotlinHardKeyword(value string) bool {
	switch value {
	case "as", "break", "class", "continue", "do", "else", "false", "for",
		"fun", "if", "in", "interface", "is", "null", "object", "package",
		"return", "super", "this", "throw", "true", "try", "typealias",
		"typeof", "val", "var", "when", "while":
		return true
	default:
		return false
	}
}

func kotlinSoftKeyword(value string) bool {
	switch value {
	case "abstract", "actual", "annotation", "by", "catch", "companion",
		"const", "constructor", "context", "crossinline", "data", "delegate",
		"dynamic", "enum", "expect", "external", "field", "file", "final",
		"finally", "get", "import", "infix", "init", "inline", "inner",
		"internal", "lateinit", "noinline", "open", "operator", "out",
		"override", "param", "private", "property", "protected", "public",
		"receiver", "reified", "sealed", "set", "setparam", "suspend",
		"tailrec", "value", "vararg", "where":
		return true
	default:
		return false
	}
}

func kotlinNumberStart(source string, offset int) bool {
	return offset >= 0 && offset < len(source) &&
		source[offset] >= '0' && source[offset] <= '9'
}

func kotlinNumberEnd(source string, start int) int {
	offset := start
	base := byte(10)
	if start+1 < len(source) && source[start] == '0' {
		switch source[start+1] {
		case 'x', 'X':
			base = 16
			offset += 2
		case 'b', 'B':
			base = 2
			offset += 2
		}
	}
	for offset < len(source) && kotlinNumericDigit(source[offset], base) {
		offset++
	}

	if base == 10 {
		if offset+1 < len(source) && source[offset] == '.' &&
			source[offset+1] >= '0' && source[offset+1] <= '9' {
			offset += 2
			for offset < len(source) && kotlinNumericDigit(source[offset], 10) {
				offset++
			}
		}
		if exponentEnd, ok := kotlinDecimalExponentEnd(source, offset); ok {
			offset = exponentEnd
		}
		if offset < len(source) && (source[offset] == 'f' || source[offset] == 'F') {
			offset++
		} else {
			offset = kotlinIntegerSuffixEnd(source, offset)
		}
	} else {
		offset = kotlinIntegerSuffixEnd(source, offset)
	}

	// Keep malformed alphanumeric tails atomic so invalid literals such as
	// 123fake cannot leak identifier-shaped search or declaration tokens.
	for offset < len(source) {
		r, size := utf8.DecodeRuneInString(source[offset:])
		if !kotlinIdentifierContinueRune(r) || r == utf8.RuneError && size == 1 {
			break
		}
		offset += max(size, 1)
	}
	return max(start+1, offset)
}

func kotlinNumericDigit(character, base byte) bool {
	if character == '_' {
		return true
	}
	if character >= '0' && character <= '9' {
		return character-'0' < base
	}
	return base == 16 && (character >= 'a' && character <= 'f' ||
		character >= 'A' && character <= 'F')
}

func kotlinDecimalExponentEnd(source string, start int) (int, bool) {
	if start >= len(source) || source[start] != 'e' && source[start] != 'E' {
		return start, false
	}
	offset := start + 1
	if offset < len(source) && (source[offset] == '+' || source[offset] == '-') {
		offset++
	}
	digitStart := offset
	for offset < len(source) && kotlinNumericDigit(source[offset], 10) {
		offset++
	}
	return offset, offset > digitStart
}

func kotlinIntegerSuffixEnd(source string, start int) int {
	if start >= len(source) {
		return start
	}
	switch source[start] {
	case 'u', 'U':
		start++
		if start < len(source) && (source[start] == 'l' || source[start] == 'L') {
			start++
		}
	case 'l', 'L':
		start++
	}
	return start
}

func kotlinRequiredInterpolationDollars(prefix int) int {
	return min(max(1, prefix), kotlinMaximumInterpolationPrefix)
}

func kotlinPunctuationAt(source string, offset int) (string, int) {
	if offset < 0 || offset >= len(source) {
		return "", offset
	}
	for _, width := range []int{3, 2} {
		end := offset + width
		if end > len(source) {
			continue
		}
		candidate := source[offset:end]
		switch candidate {
		case "..<", "===", "!==", ">>=", "<<=", "->", "::", "?.", "?:",
			"!!", "==", "!=", "<=", ">=", "&&", "||", "++", "--", "+=",
			"-=", "*=", "/=", "%=", "..", "<<", ">>":
			return candidate, end
		}
	}
	_, size := utf8.DecodeRuneInString(source[offset:])
	end := min(len(source), offset+max(size, 1))
	return source[offset:end], end
}

func kotlinQuotedEnd(
	source string,
	offset int,
	quote byte,
	budget *kotlinLexicalBudget,
) (int, bool) {
	escaped := false
	for offset < len(source) {
		r, size := utf8.DecodeRuneInString(source[offset:])
		size = max(size, 1)
		if !budget.take(size) {
			return offset, false
		}
		if kotlinLineBreak(r) {
			return offset, false
		}
		if escaped {
			escaped = false
			offset += size
			continue
		}
		if source[offset] == '\\' {
			escaped = true
			offset++
			continue
		}
		if source[offset] == quote {
			return offset + 1, true
		}
		offset += size
	}
	return len(source), false
}

func kotlinRegularStringEnd(
	source string,
	offset, requiredDollars, interpolationDepth int,
	budget *kotlinLexicalBudget,
) (int, bool) {
	escaped := false
	for offset < len(source) {
		r, size := utf8.DecodeRuneInString(source[offset:])
		size = max(size, 1)
		if !budget.take(size) {
			return offset, false
		}
		if kotlinLineBreak(r) {
			return offset, false
		}
		if escaped {
			escaped = false
			offset += size
			continue
		}
		switch source[offset] {
		case '\\':
			escaped = true
			offset++
		case '"':
			return offset + 1, true
		case '$':
			run := kotlinByteRun(source, offset, '$')
			if !budget.take(max(0, run-size)) {
				return offset, false
			}
			after := offset + run
			if run >= requiredDollars && after < len(source) && source[after] == '{' {
				if interpolationDepth >= kotlinMaximumInterpolationDepth {
					return kotlinStringRecoveryEnd(source, offset, interpolationDepth), false
				}
				_, closeEnd, found := kotlinInterpolationEndDepth(
					source, after+1, len(source), interpolationDepth+1, budget,
				)
				if !found {
					return kotlinStringRecoveryEnd(source, offset, interpolationDepth), false
				}
				offset = closeEnd
				continue
			}
			offset = after
		default:
			offset += size
		}
	}
	return len(source), false
}

func kotlinTripleQuotedEnd(
	source string,
	offset, requiredDollars, interpolationDepth int,
	budget *kotlinLexicalBudget,
) (int, bool) {
	for offset < len(source) {
		if source[offset] == '"' {
			run := kotlinByteRun(source, offset, '"')
			if !budget.take(run) {
				return offset, false
			}
			if run >= 3 {
				return offset + run, true
			}
			offset += run
			continue
		}
		if source[offset] == '$' {
			run := kotlinByteRun(source, offset, '$')
			if !budget.take(max(run, 1)) {
				return offset, false
			}
			after := offset + run
			if run >= requiredDollars && after < len(source) && source[after] == '{' {
				if interpolationDepth >= kotlinMaximumInterpolationDepth {
					return offset, false
				}
				_, closeEnd, found := kotlinInterpolationEndDepth(
					source, after+1, len(source), interpolationDepth+1, budget,
				)
				if !found {
					return offset, false
				}
				offset = closeEnd
				continue
			}
			offset = after
			continue
		}
		_, size := utf8.DecodeRuneInString(source[offset:])
		size = max(size, 1)
		if !budget.take(size) {
			return offset, false
		}
		offset += size
	}
	return len(source), false
}

func kotlinStringRecoveryEnd(source string, offset, interpolationDepth int) int {
	if interpolationDepth > 0 {
		return offset
	}
	return kotlinLineEnd(source, offset)
}

func kotlinBlockCommentEnd(source string, start int) int {
	offset := min(len(source), start+2)
	depth := 1
	for offset < len(source) {
		switch {
		case strings.HasPrefix(source[offset:], "/*"):
			depth++
			offset += 2
		case strings.HasPrefix(source[offset:], "*/"):
			depth--
			offset += 2
			if depth == 0 {
				return offset
			}
		default:
			_, size := utf8.DecodeRuneInString(source[offset:])
			offset += max(size, 1)
		}
	}
	return len(source)
}

func kotlinByteRun(source string, start int, character byte) int {
	end := start
	for end < len(source) && source[end] == character {
		end++
	}
	return end - start
}

func kotlinLineEnd(source string, start int) int {
	end := max(start, 0)
	for end < len(source) {
		r, size := utf8.DecodeRuneInString(source[end:])
		if kotlinLineBreak(r) {
			break
		}
		end += max(size, 1)
	}
	return end
}

func kotlinLineEndWithin(source string, start, limit int) int {
	end := max(start, 0)
	for end < limit {
		r, size := utf8.DecodeRuneInString(source[end:limit])
		if kotlinLineBreak(r) {
			break
		}
		end += max(size, 1)
	}
	return end
}

func kotlinRangeEndsWithCodeLine(source string, start, end int, before bool) bool {
	end = max(start, min(end, len(source)))
	lastBreakEnd := -1
	for offset := start; offset < end; {
		r, size := utf8.DecodeRuneInString(source[offset:end])
		size = max(size, 1)
		if kotlinLineBreak(r) {
			lastBreakEnd = offset + size
		}
		offset += size
	}
	if lastBreakEnd < 0 {
		return before || end > start
	}
	return strings.TrimSpace(source[lastBreakEnd:end]) != ""
}

func kotlinWhitespace(r rune) bool {
	return unicode.IsSpace(r) || r == '\uFEFF'
}

func kotlinLineBreak(r rune) bool {
	return r == '\n' || r == '\r' || r == '\u0085' || r == '\u2028' || r == '\u2029'
}

func kotlinLineStarts(source string) []int {
	return cTreeLineStarts(source)
}

func kotlinTokenLine(lineStarts []int, offset int) int {
	return sort.Search(len(lineStarts), func(index int) bool { return lineStarts[index] > offset })
}

func boolInt(value bool) int {
	if value {
		return 1
	}
	return 0
}
