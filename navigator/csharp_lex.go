package navigator

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	csharpMaximumConcreteParseBytes        = 8 << 20
	csharpMaximumConcreteTokens            = 64 << 10
	csharpMaximumConcreteDelimiterDepth    = 128
	csharpMaximumConcretePreprocessorDepth = 128
	csharpMaximumRetainedTokens            = 256 << 10
	csharpMaximumRetainedSpans             = 256 << 10
	csharpMaximumStructuralDepth           = 512
	csharpMaximumHeaderTokens              = 4096
)

const csharpTokenGap = "\x00csharp-token-gap\x00"

type csharpTokenKind uint8

const (
	csharpTokenIdentifier csharpTokenKind = iota
	csharpTokenNumber
	csharpTokenPunctuation
	csharpTokenDirective
)

type csharpToken struct {
	text       string
	start, end int
	kind       csharpTokenKind
	lineStart  bool
	gap        bool
}

type csharpLexResult struct {
	tokens            []csharpToken
	commentSpans      []cByteSpan
	stringSpans       []cByteSpan
	lexicalUnits      int
	maximumDepth      int
	preprocessorDepth int
	truncated         bool
	spansTruncated    bool
	concreteEligible  bool
}

type csharpTokenRetention struct {
	head      []csharpToken
	tail      []csharpToken
	tailStart int
	total     int
}

func (retention *csharpTokenRetention) append(token csharpToken) {
	retention.total++
	headLimit := (csharpMaximumRetainedTokens - 1) / 2
	// Keep one additional tail token until truncation is certain. Without that
	// spare slot, an input containing exactly the cap silently lost its final
	// token even though result reported truncated=false.
	tailLimit := csharpMaximumRetainedTokens - headLimit
	if len(retention.head) < headLimit {
		retention.head = append(retention.head, token)
		return
	}
	if tailLimit < 1 {
		return
	}
	if len(retention.tail) < tailLimit {
		retention.tail = append(retention.tail, token)
		return
	}
	retention.tail[retention.tailStart] = token
	retention.tailStart = (retention.tailStart + 1) % tailLimit
}

func (retention *csharpTokenRetention) result() ([]csharpToken, bool) {
	if retention.total <= csharpMaximumRetainedTokens {
		return append(retention.head, retention.tail...), false
	}
	tokens := make([]csharpToken, 0, csharpMaximumRetainedTokens)
	tokens = append(tokens, retention.head...)
	tokens = append(tokens, csharpToken{
		text: csharpTokenGap, kind: csharpTokenDirective, gap: true,
	})
	tailCount := csharpMaximumRetainedTokens - len(retention.head) - 1
	if tailCount > len(retention.tail) {
		tailCount = len(retention.tail)
	}
	// tailStart identifies the oldest retained token. Skip any spare item so
	// the synthetic gap is followed by the newest possible physical suffix.
	skip := len(retention.tail) - tailCount
	for index := range tailCount {
		position := (retention.tailStart + skip + index) % len(retention.tail)
		tokens = append(tokens, retention.tail[position])
	}
	return tokens, true
}

type csharpLexicalSink struct {
	comment func(cByteSpan) bool
	literal func(cByteSpan) bool
	token   func(csharpToken) bool
}

// lexCSharp retains a bounded head and tail for parser preflight and tests.
// Structural recovery and search masking use the same scanner in streaming
// mode, so declarations or references in a discarded middle remain visible.
func lexCSharp(source string) csharpLexResult {
	result := csharpLexResult{concreteEligible: true}
	var retention csharpTokenRetention
	appendSpan := func(destination *[]cByteSpan, span cByteSpan) bool {
		if len(*destination) < csharpMaximumRetainedSpans {
			*destination = append(*destination, span)
		} else {
			result.spansTruncated = true
		}
		return true
	}
	walkCSharpLexically(source, csharpLexicalSink{
		comment: func(span cByteSpan) bool {
			return appendSpan(&result.commentSpans, span)
		},
		literal: func(span cByteSpan) bool {
			return appendSpan(&result.stringSpans, span)
		},
		token: func(token csharpToken) bool {
			retention.append(token)
			result.lexicalUnits++
			return true
		},
	})
	result.tokens, result.truncated = retention.result()
	result.commentSpans = normalizeCSpans(result.commentSpans)
	result.stringSpans = normalizeCSpans(result.stringSpans)
	result.maximumDepth, result.preprocessorDepth, result.concreteEligible =
		csharpConcreteFrontiers(source)
	if result.lexicalUnits > csharpMaximumConcreteTokens ||
		len(source) > csharpMaximumConcreteParseBytes || result.spansTruncated {
		result.concreteEligible = false
	}
	return result
}

// walkCSharpLexically emits physical source coordinates. Literal events cover
// delimiters and payload; interpolated expressions are intentionally treated
// as part of the literal here. The concrete tree can recover their code nodes,
// while this conservative fallback prevents payload braces from corrupting
// scopes when syntax is incomplete.
func walkCSharpLexically(source string, sink csharpLexicalSink) {
	csharpWalkLexicallyWithState(source, sink, false)
}

func csharpWalkLexicallyWithState(
	source string,
	sink csharpLexicalSink,
	lineHasCode bool,
) {
	for offset := 0; offset < len(source); {
		if offset == 0 && strings.HasPrefix(source, "\uFEFF") {
			offset += len("\uFEFF")
			continue
		}
		if strings.HasPrefix(source[offset:], "//") {
			end := csharpLineEnd(source, offset)
			if sink.comment != nil && !sink.comment(cByteSpan{start: offset, end: end}) {
				return
			}
			lineHasCode = true
			offset = end
			continue
		}
		if strings.HasPrefix(source[offset:], "/*") {
			end := csharpBlockCommentEnd(source, offset)
			if sink.comment != nil && !sink.comment(cByteSpan{start: offset, end: end}) {
				return
			}
			lineHasCode = csharpRangeEndsWithCodeLine(source, offset, end, lineHasCode)
			offset = end
			continue
		}
		if end, ok := csharpLiteralEnd(source, offset); ok {
			if !csharpEmitLiteral(source, offset, end, sink) {
				return
			}
			lineHasCode = csharpRangeEndsWithCodeLine(source, offset, end, lineHasCode)
			offset = max(offset+1, end)
			continue
		}

		r, size := utf8.DecodeRuneInString(source[offset:])
		if size < 1 {
			size = 1
		}
		if csharpWhitespace(r) {
			if csharpLineBreak(r) {
				lineHasCode = false
			}
			offset += size
			continue
		}

		token := csharpToken{start: offset, lineStart: !lineHasCode}
		if end := csharpIdentifierEnd(source, offset); end > offset {
			token.end = end
			token.text = source[offset:end]
			token.kind = csharpTokenIdentifier
		} else if csharpNumberStart(source, offset) {
			token.end = csharpNumberEnd(source, offset)
			token.text = source[offset:token.end]
			token.kind = csharpTokenNumber
		} else {
			token.text, token.end = csharpPunctuationAt(source, offset)
			token.kind = csharpTokenPunctuation
			if token.text == "#" && token.lineStart {
				token.kind = csharpTokenDirective
			}
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

// csharpEmitLiteral masks interpolated text while recursively exposing the
// embedded expression as ordinary code. This gives NoStrings its C# meaning:
// `$"payload {Target()}"` hides payload but keeps Target searchable.
func csharpEmitLiteral(
	source string,
	start, end int,
	sink csharpLexicalSink,
) bool {
	prefix, ok := csharpInterpolationPrefix(source, start, end)
	if !ok {
		return sink.literal == nil || sink.literal(cByteSpan{start: start, end: end})
	}
	if !prefix.terminated {
		// Once a multiline literal has been resynchronized, treating braces in
		// its malformed payload as interpolation holes would promote arbitrary
		// text to code. Preserve the whole damaged region as opaque instead.
		return sink.literal == nil || sink.literal(cByteSpan{start: start, end: end})
	}
	opaqueStart := start
	for cursor := prefix.contentStart; cursor < prefix.contentEnd; {
		if source[cursor] == '\\' && !prefix.verbatim && !prefix.raw {
			cursor = min(prefix.contentEnd, cursor+2)
			continue
		}
		if source[cursor] != '{' {
			cursor++
			continue
		}
		run := csharpByteRun(source, cursor, '{')
		if !prefix.raw && prefix.braces == 1 && run >= 2 {
			cursor += 2
			continue
		}
		if prefix.raw && (run < prefix.braces || run-prefix.braces >= prefix.braces) ||
			!prefix.raw && run != prefix.braces {
			cursor += run
			continue
		}
		expressionStart := cursor + run
		expressionEnd, closeEnd, found := csharpInterpolationExpressionEnd(
			source, expressionStart, prefix.contentEnd, prefix.braces,
		)
		if !found {
			break
		}
		if sink.literal != nil && opaqueStart < expressionStart &&
			!sink.literal(cByteSpan{start: opaqueStart, end: expressionStart}) {
			return false
		}
		// Search masking needs comments and nested literals inside the hole, but
		// structural recovery must treat the complete interpolated literal as one
		// initializer. Feeding expression tokens into the declaration stream can
		// turn an otherwise simple field into a call-shaped malformed statement.
		expressionSink := sink
		expressionSink.token = nil
		if !csharpWalkLexicalRange(source, expressionStart, expressionEnd, expressionSink) {
			return false
		}
		opaqueStart = expressionEnd
		cursor = closeEnd
	}
	return sink.literal == nil || opaqueStart >= end ||
		sink.literal(cByteSpan{start: opaqueStart, end: end})
}

type csharpInterpolationInfo struct {
	contentStart int
	contentEnd   int
	braces       int
	verbatim     bool
	raw          bool
	terminated   bool
}

func csharpInterpolationPrefix(
	source string,
	start, end int,
) (csharpInterpolationInfo, bool) {
	if start < 0 || end <= start || end > len(source) {
		return csharpInterpolationInfo{}, false
	}
	cursor := start
	verbatim := false
	if source[cursor] == '@' {
		verbatim = true
		cursor++
	}
	dollars := 0
	for cursor < end && source[cursor] == '$' {
		dollars++
		cursor++
	}
	if cursor < end && source[cursor] == '@' {
		verbatim = true
		cursor++
	}
	if dollars < 1 || cursor >= end || source[cursor] != '"' {
		return csharpInterpolationInfo{}, false
	}
	quotes := csharpByteRun(source, cursor, '"')
	raw := quotes >= 3 && !verbatim
	if !raw {
		quotes = 1
		dollars = 1
	}
	coreEnd := end
	contentEnd := coreEnd
	terminated := csharpInterpolationTerminatedAt(
		source, cursor, contentEnd, quotes, verbatim, raw,
	)
	if terminated {
		contentEnd -= quotes
	}
	contentStart := min(contentEnd, cursor+quotes)
	return csharpInterpolationInfo{
		contentStart: contentStart,
		contentEnd:   contentEnd,
		braces:       dollars,
		verbatim:     verbatim,
		raw:          raw,
		terminated:   terminated,
	}, true
}

func csharpInterpolationTerminatedAt(
	source string,
	quoteStart, end, quotes int,
	verbatim, raw bool,
) bool {
	if quotes < 1 || end-quoteStart < quotes || end > len(source) {
		return false
	}
	closingStart := end - quotes
	if csharpByteRun(source, closingStart, '"') < quotes {
		return false
	}
	if raw {
		return true
	}
	if verbatim {
		runStart := closingStart
		for runStart > quoteStart+1 && source[runStart-1] == '"' {
			runStart--
		}
		return (end-runStart)%2 == 1
	}
	backslashes := 0
	for offset := closingStart - 1; offset > quoteStart && source[offset] == '\\'; offset-- {
		backslashes++
	}
	return backslashes%2 == 0
}

func csharpInterpolationExpressionEnd(
	source string,
	start, limit, closeCount int,
) (expressionEnd, closeEnd int, found bool) {
	return csharpInterpolationExpressionEndDepth(source, start, limit, closeCount, 0)
}

func csharpInterpolationExpressionEndDepth(
	source string,
	start, limit, closeCount, recursionDepth int,
) (expressionEnd, closeEnd int, found bool) {
	braceDepth := 0
	parenDepth := 0
	bracketDepth := 0
	expressionEnd = -1
	for offset := start; offset < limit; {
		if strings.HasPrefix(source[offset:limit], "//") {
			offset = csharpLineEndWithin(source, offset, limit)
			continue
		}
		if strings.HasPrefix(source[offset:limit], "/*") {
			end := strings.Index(source[min(limit, offset+2):limit], "*/")
			if end < 0 {
				return 0, 0, false
			}
			offset = offset + 2 + end + 2
			continue
		}
		if recursionDepth < csharpMaximumStructuralDepth {
			literalEnd, ok := csharpLiteralEndDepth(source[:limit], offset, recursionDepth+1)
			if ok {
				offset = max(offset+1, literalEnd)
				continue
			}
		}
		switch source[offset] {
		case '{':
			braceDepth++
			offset++
		case '}':
			run := csharpByteRun(source[:limit], offset, '}')
			if braceDepth == 0 && run >= closeCount &&
				run-closeCount < closeCount {
				if expressionEnd < 0 {
					expressionEnd = offset
				}
				return expressionEnd, offset + closeCount, true
			}
			if braceDepth > 0 {
				braceDepth--
			}
			offset++
		case '(':
			parenDepth++
			offset++
		case ')':
			parenDepth = max(0, parenDepth-1)
			offset++
		case '[':
			bracketDepth++
			offset++
		case ']':
			bracketDepth = max(0, bracketDepth-1)
			offset++
		case ',', ':':
			if expressionEnd < 0 && braceDepth == 0 && parenDepth == 0 &&
				bracketDepth == 0 &&
				(source[offset] != ':' || offset+1 >= limit || source[offset+1] != ':') {
				expressionEnd = offset
			}
			offset++
		default:
			_, size := utf8.DecodeRuneInString(source[offset:limit])
			offset += max(size, 1)
		}
	}
	return 0, 0, false
}

func csharpWalkLexicalRange(
	source string,
	start, end int,
	sink csharpLexicalSink,
) bool {
	if start < 0 || end < start || end > len(source) {
		return false
	}
	completed := true
	local := csharpLexicalSink{}
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
	if sink.token != nil {
		local.token = func(token csharpToken) bool {
			token.start += start
			token.end += start
			// A preprocessor directive cannot begin inside an interpolation
			// expression, even when that expression spans a physical newline.
			if token.text == "#" {
				token.kind = csharpTokenPunctuation
				token.lineStart = false
			}
			completed = sink.token(token)
			return completed
		}
	}
	csharpWalkLexicallyWithState(source[start:end], local, true)
	return completed
}

func csharpLineEndWithin(source string, start, limit int) int {
	end := max(0, start)
	for end < limit {
		r, size := utf8.DecodeRuneInString(source[end:limit])
		if csharpLineBreak(r) {
			break
		}
		end += max(size, 1)
	}
	return end
}

func csharpConcreteFrontiers(source string) (maximumDepth, maximumPreprocessorDepth int, eligible bool) {
	eligible = true
	delimiters := make([]byte, 0, csharpMaximumConcreteDelimiterDepth)
	type conditionalFrontier struct {
		delimiterDepth int
		overflow       int
	}
	var conditionals [csharpMaximumConcretePreprocessorDepth]conditionalFrontier
	conditionalDepth := 0
	delimiterOverflow := 0
	delimiterFloor := 0
	directiveLineEnd := -1
	expectDirectiveName := false
	walkCSharpLexically(source, csharpLexicalSink{token: func(token csharpToken) bool {
		if token.gap {
			eligible = false
			return false
		}
		if directiveLineEnd >= 0 && token.start >= directiveLineEnd {
			directiveLineEnd = -1
			expectDirectiveName = false
		}
		if token.text == "#" && token.lineStart {
			directiveLineEnd = csharpLineEnd(source, token.start)
			expectDirectiveName = true
			return true
		}
		if directiveLineEnd >= 0 && token.start < directiveLineEnd {
			if !expectDirectiveName {
				return true
			}
			expectDirectiveName = false
			switch token.text {
			case "if":
				conditionalDepth++
				maximumPreprocessorDepth = max(maximumPreprocessorDepth, conditionalDepth)
				if conditionalDepth > csharpMaximumConcretePreprocessorDepth {
					eligible = false
				} else {
					conditionals[conditionalDepth-1] = conditionalFrontier{
						delimiterDepth: len(delimiters),
						overflow:       delimiterOverflow,
					}
					delimiterFloor = len(delimiters)
				}
			case "elif", "else":
				if conditionalDepth > 0 &&
					conditionalDepth <= csharpMaximumConcretePreprocessorDepth {
					frontier := conditionals[conditionalDepth-1]
					delimiters = delimiters[:frontier.delimiterDepth]
					delimiterOverflow = frontier.overflow
					delimiterFloor = frontier.delimiterDepth
				}
			case "endif":
				if conditionalDepth > 0 {
					if conditionalDepth <= csharpMaximumConcretePreprocessorDepth {
						frontier := conditionals[conditionalDepth-1]
						delimiters = delimiters[:frontier.delimiterDepth]
						delimiterOverflow = frontier.overflow
					}
					conditionalDepth--
					if conditionalDepth > 0 &&
						conditionalDepth <= csharpMaximumConcretePreprocessorDepth {
						delimiterFloor = conditionals[conditionalDepth-1].delimiterDepth
					} else {
						delimiterFloor = 0
					}
				}
			}
			return true
		}
		switch token.text {
		case "(", "[", "{":
			if delimiterOverflow > 0 ||
				len(delimiters) == csharpMaximumConcreteDelimiterDepth {
				delimiterOverflow++
				eligible = false
				maximumDepth = max(maximumDepth,
					csharpMaximumConcreteDelimiterDepth+delimiterOverflow)
				return true
			}
			delimiters = append(delimiters, token.text[0])
			maximumDepth = max(maximumDepth, len(delimiters))
		case ")", "]", "}":
			if delimiterOverflow > 0 {
				delimiterOverflow--
				return true
			}
			want := byte('(')
			switch token.text {
			case "]":
				want = '['
			case "}":
				want = '{'
			}
			if len(delimiters) > delimiterFloor &&
				delimiters[len(delimiters)-1] == want {
				delimiters = delimiters[:len(delimiters)-1]
			}
		}
		return true
	}})
	return maximumDepth, maximumPreprocessorDepth, eligible
}

func csharpIdentifierEnd(source string, start int) int {
	if start < 0 || start >= len(source) {
		return start
	}
	offset := start
	if source[offset] == '@' {
		offset++
		if offset >= len(source) {
			return start
		}
	}
	r, end, ok := csharpIdentifierUnit(source, offset)
	if !ok || !csharpIdentifierStartRune(r) {
		return start
	}
	offset = end
	for offset < len(source) {
		r, end, ok = csharpIdentifierUnit(source, offset)
		if !ok || !csharpIdentifierContinueRune(r) {
			break
		}
		offset = end
	}
	return offset
}

func csharpIdentifierUnit(source string, offset int) (rune, int, bool) {
	if r, end, ok := csharpIdentifierEscape(source, offset); ok {
		return r, end, true
	}
	if offset < 0 || offset >= len(source) {
		return 0, offset, false
	}
	r, size := utf8.DecodeRuneInString(source[offset:])
	if size < 1 || r == utf8.RuneError && size == 1 {
		return 0, offset + max(size, 1), false
	}
	return r, offset + size, true
}

func csharpIdentifierEscape(source string, start int) (rune, int, bool) {
	if start < 0 || start+2 > len(source) || source[start] != '\\' ||
		(source[start+1] != 'u' && source[start+1] != 'U') {
		return 0, start, false
	}
	digits := 4
	if source[start+1] == 'U' {
		digits = 8
	}
	end := start + 2 + digits
	if end > len(source) {
		return 0, start, false
	}
	value := uint32(0)
	for _, digit := range []byte(source[start+2 : end]) {
		value <<= 4
		switch {
		case digit >= '0' && digit <= '9':
			value += uint32(digit - '0')
		case digit >= 'a' && digit <= 'f':
			value += uint32(digit-'a') + 10
		case digit >= 'A' && digit <= 'F':
			value += uint32(digit-'A') + 10
		default:
			return 0, start, false
		}
	}
	r := rune(value)
	if !utf8.ValidRune(r) || r >= 0xD800 && r <= 0xDFFF {
		return 0, start, false
	}
	return r, end, true
}

func csharpIdentifierStartRune(r rune) bool {
	return r == '_' || unicode.IsLetter(r) || unicode.Is(unicode.Nl, r)
}

func csharpIdentifierContinueRune(r rune) bool {
	return csharpIdentifierStartRune(r) || unicode.Is(unicode.Mn, r) ||
		unicode.Is(unicode.Mc, r) || unicode.Is(unicode.Nd, r) ||
		unicode.Is(unicode.Pc, r) || unicode.Is(unicode.Cf, r)
}

func csharpSourceIdentifier(identifier string) bool {
	return identifier != "" && csharpIdentifierEnd(identifier, 0) == len(identifier)
}

func csharpKeywordToken(token csharpToken) bool {
	if token.kind != csharpTokenIdentifier || token.text == "" || token.text[0] == '@' ||
		strings.Contains(token.text, "\\") {
		return false
	}
	return csharpKeyword(token.text)
}

func csharpKeyword(value string) bool {
	switch value {
	case "abstract", "as", "base", "bool", "break", "byte", "case", "catch",
		"char", "checked", "class", "const", "continue", "decimal", "default",
		"delegate", "do", "double", "else", "enum", "event", "explicit", "extern",
		"false", "finally", "fixed", "float", "for", "foreach", "goto", "if",
		"implicit", "in", "int", "interface", "internal", "is", "lock", "long",
		"namespace", "new", "null", "object", "operator", "out", "override", "params",
		"private", "protected", "public", "readonly", "ref", "return", "sbyte",
		"sealed", "short", "sizeof", "stackalloc", "static", "string", "struct",
		"switch", "this", "throw", "true", "try", "typeof", "uint", "ulong",
		"unchecked", "unsafe", "ushort", "using", "virtual", "void", "volatile",
		"while", "add", "alias", "and", "ascending", "async", "await", "by",
		"descending", "dynamic", "equals", "extension", "field", "file", "from",
		"get", "global", "group", "init", "into", "join", "let", "managed",
		"nameof", "not", "notnull", "on", "or", "orderby", "partial", "record",
		"remove", "required", "scoped", "select", "set", "unmanaged", "value",
		"var", "when", "where", "with", "yield":
		return true
	default:
		return false
	}
}

func csharpNumberStart(source string, offset int) bool {
	if offset < 0 || offset >= len(source) {
		return false
	}
	return source[offset] >= '0' && source[offset] <= '9' ||
		source[offset] == '.' && offset+1 < len(source) &&
			source[offset+1] >= '0' && source[offset+1] <= '9'
}

func csharpNumberEnd(source string, start int) int {
	offset := start
	for offset < len(source) {
		character := source[offset]
		if character >= '0' && character <= '9' || character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' || character == '_' {
			offset++
			continue
		}
		// A decimal point belongs to a C# real literal only when a decimal
		// digit follows it. Otherwise it starts member access or the range
		// punctuator, as in 1.ToString(), 1._member, and 1..end.
		if character == '.' && offset+1 < len(source) &&
			source[offset+1] >= '0' && source[offset+1] <= '9' {
			offset++
			continue
		}
		if (character == '+' || character == '-') && offset > start &&
			(source[offset-1] == 'e' || source[offset-1] == 'E') {
			offset++
			continue
		}
		break
	}
	return max(start+1, offset)
}

func csharpPunctuationAt(source string, offset int) (string, int) {
	if offset < 0 || offset >= len(source) {
		return "", offset
	}
	for _, width := range []int{4, 3, 2} {
		end := offset + width
		if end > len(source) {
			continue
		}
		candidate := source[offset:end]
		switch candidate {
		case ">>>=", "??=", "<<=", ">>=", ">>>", "...", "=>", "==", "!=",
			"<=", ">=", "++", "--", "&&", "||", "??", "?.", "::", "->",
			"+=", "-=", "*=", "/=", "%=", "&=", "|=", "^=", "<<", ">>", "..":
			return candidate, end
		}
	}
	_, size := utf8.DecodeRuneInString(source[offset:])
	if size < 1 {
		size = 1
	}
	end := min(len(source), offset+size)
	return source[offset:end], end
}

func csharpLiteralEnd(source string, start int) (int, bool) {
	return csharpLiteralEndDepth(source, start, 0)
}

func csharpLiteralEndDepth(source string, start, recursionDepth int) (int, bool) {
	if start < 0 || start >= len(source) {
		return start, false
	}
	if source[start] == '\'' {
		end, _ := csharpRegularQuotedEndStatus(source, start+1, '\'')
		return end, true
	}
	prefixEnd := start
	dollars := 0
	verbatim := false
	if source[prefixEnd] == '@' {
		verbatim = true
		prefixEnd++
		if prefixEnd < len(source) && source[prefixEnd] == '$' {
			dollars++
			prefixEnd++
		}
	} else {
		for prefixEnd < len(source) && source[prefixEnd] == '$' {
			dollars++
			prefixEnd++
		}
		if prefixEnd < len(source) && source[prefixEnd] == '@' {
			verbatim = true
			prefixEnd++
		}
	}
	if prefixEnd >= len(source) || source[prefixEnd] != '"' {
		return start, false
	}
	quotes := csharpByteRun(source, prefixEnd, '"')
	if quotes >= 3 && !verbatim {
		var end int
		var terminated bool
		if dollars > 0 {
			end, terminated = csharpInterpolatedRawStringEnd(
				source, prefixEnd+quotes, quotes, dollars, recursionDepth,
			)
		} else {
			end, terminated = csharpRawStringEndStatus(source, prefixEnd+quotes, quotes)
		}
		if !terminated {
			end = csharpMalformedMultilineLiteralEnd(source, start)
		} else if dollars == 0 {
			end = csharpUTF8StringSuffixEnd(source, end)
		}
		return end, true
	}
	if dollars > 1 {
		return start, false
	}
	if verbatim {
		var end int
		var terminated bool
		if dollars == 1 {
			end, terminated = csharpInterpolatedVerbatimStringEnd(
				source, prefixEnd+1, recursionDepth,
			)
		} else {
			end, terminated = csharpVerbatimStringEndStatus(source, prefixEnd+1)
		}
		if !terminated {
			end = csharpMalformedMultilineLiteralEnd(source, start)
		} else if dollars == 0 {
			end = csharpUTF8StringSuffixEnd(source, end)
		}
		return end, true
	}
	var end int
	var terminated bool
	if dollars == 1 {
		end, terminated = csharpInterpolatedRegularStringEnd(
			source, prefixEnd+1, recursionDepth,
		)
	} else {
		end, terminated = csharpRegularQuotedEndStatus(source, prefixEnd+1, '"')
	}
	if terminated && dollars == 0 {
		end = csharpUTF8StringSuffixEnd(source, end)
	}
	return end, true
}

func csharpRegularQuotedEndStatus(source string, offset int, quote byte) (int, bool) {
	escaped := false
	for offset < len(source) {
		character := source[offset]
		r, size := utf8.DecodeRuneInString(source[offset:])
		if csharpLineBreak(r) {
			return offset, false
		}
		if escaped {
			escaped = false
			offset += max(size, 1)
			continue
		}
		if character == '\\' {
			escaped = true
			offset++
			continue
		}
		if character == quote {
			return offset + 1, true
		}
		offset += max(size, 1)
	}
	return len(source), false
}

func csharpVerbatimStringEndStatus(source string, offset int) (int, bool) {
	for offset < len(source) {
		if source[offset] != '"' {
			_, size := utf8.DecodeRuneInString(source[offset:])
			offset += max(size, 1)
			continue
		}
		if offset+1 < len(source) && source[offset+1] == '"' {
			offset += 2
			continue
		}
		return offset + 1, true
	}
	return len(source), false
}

func csharpRawStringEndStatus(source string, offset, quotes int) (int, bool) {
	for offset < len(source) {
		if source[offset] != '"' {
			_, size := utf8.DecodeRuneInString(source[offset:])
			offset += max(size, 1)
			continue
		}
		run := csharpByteRun(source, offset, '"')
		if run >= quotes {
			return offset + quotes, true
		}
		offset += run
	}
	return len(source), false
}

func csharpInterpolatedRegularStringEnd(
	source string,
	offset, recursionDepth int,
) (int, bool) {
	for offset < len(source) {
		r, size := utf8.DecodeRuneInString(source[offset:])
		if csharpLineBreak(r) {
			return offset, false
		}
		switch source[offset] {
		case '\\':
			offset++
			if offset < len(source) {
				_, escapedSize := utf8.DecodeRuneInString(source[offset:])
				offset += max(escapedSize, 1)
			}
		case '"':
			return offset + 1, true
		case '{':
			run := csharpByteRun(source, offset, '{')
			if run >= 2 {
				offset += 2
				continue
			}
			_, closeEnd, found := csharpInterpolationExpressionEndDepth(
				source, offset+1, len(source), 1, recursionDepth+1,
			)
			if !found {
				return csharpLineEnd(source, offset), false
			}
			offset = closeEnd
		default:
			offset += max(size, 1)
		}
	}
	return len(source), false
}

func csharpInterpolatedVerbatimStringEnd(
	source string,
	offset, recursionDepth int,
) (int, bool) {
	for offset < len(source) {
		if source[offset] == '"' {
			if offset+1 < len(source) && source[offset+1] == '"' {
				offset += 2
				continue
			}
			return offset + 1, true
		}
		if source[offset] == '{' {
			run := csharpByteRun(source, offset, '{')
			if run >= 2 {
				offset += 2
				continue
			}
			_, closeEnd, found := csharpInterpolationExpressionEndDepth(
				source, offset+1, len(source), 1, recursionDepth+1,
			)
			if !found {
				return len(source), false
			}
			offset = closeEnd
			continue
		}
		_, size := utf8.DecodeRuneInString(source[offset:])
		offset += max(size, 1)
	}
	return len(source), false
}

func csharpInterpolatedRawStringEnd(
	source string,
	offset, quotes, braces, recursionDepth int,
) (int, bool) {
	for offset < len(source) {
		if source[offset] == '"' {
			run := csharpByteRun(source, offset, '"')
			if run >= quotes {
				return offset + quotes, true
			}
			offset += run
			continue
		}
		if source[offset] == '{' {
			run := csharpByteRun(source, offset, '{')
			if run >= braces && run-braces < braces {
				_, closeEnd, found := csharpInterpolationExpressionEndDepth(
					source, offset+run, len(source), braces, recursionDepth+1,
				)
				if !found {
					return len(source), false
				}
				offset = closeEnd
				continue
			}
			offset += run
			continue
		}
		_, size := utf8.DecodeRuneInString(source[offset:])
		offset += max(size, 1)
	}
	return len(source), false
}

func csharpUTF8StringSuffixEnd(source string, end int) int {
	if end < 0 || end+2 > len(source) ||
		(source[end:end+2] != "u8" && source[end:end+2] != "U8") {
		return end
	}
	if end+2 < len(source) && csharpIdentifierEnd(source, end) > end+2 {
		return end
	}
	return end + 2
}

func csharpMalformedMultilineLiteralEnd(source string, literalStart int) int {
	offset := csharpLineEnd(source, literalStart)
	for offset < len(source) {
		r, size := utf8.DecodeRuneInString(source[offset:])
		if !csharpLineBreak(r) {
			offset = csharpLineEnd(source, offset)
			continue
		}
		offset += max(size, 1)
		if r == '\r' && offset < len(source) && source[offset] == '\n' {
			offset++
		}
		lineStart := offset
		for offset < len(source) && (source[offset] == ' ' || source[offset] == '\t') {
			offset++
		}
		if csharpMalformedLiteralDeclarationStart(source, offset) {
			return offset
		}
		offset = csharpLineEnd(source, lineStart)
	}
	return len(source)
}

func csharpMalformedLiteralDeclarationStart(source string, offset int) bool {
	if offset < 0 || offset >= len(source) {
		return false
	}
	if source[offset] == '#' {
		return true
	}
	end := csharpIdentifierEnd(source, offset)
	if end <= offset {
		return false
	}
	switch source[offset:end] {
	case "namespace", "class", "struct", "interface", "enum", "record", "delegate",
		"public", "private", "protected", "internal", "file", "static", "abstract",
		"sealed", "partial", "readonly", "ref", "unsafe", "extern", "global",
		"using", "event", "void":
		return true
	default:
		return false
	}
}

func csharpByteRun(source string, start int, character byte) int {
	end := start
	for end < len(source) && source[end] == character {
		end++
	}
	return end - start
}

func csharpLineEnd(source string, start int) int {
	end := max(start, 0)
	for end < len(source) {
		r, size := utf8.DecodeRuneInString(source[end:])
		if csharpLineBreak(r) {
			break
		}
		end += max(size, 1)
	}
	return end
}

func csharpBlockCommentEnd(source string, start int) int {
	if end := strings.Index(source[min(len(source), start+2):], "*/"); end >= 0 {
		return min(len(source), start+2+end+2)
	}
	return len(source)
}

func csharpRangeEndsWithCodeLine(source string, start, end int, before bool) bool {
	end = max(start, min(end, len(source)))
	lastBreakEnd := -1
	for offset := start; offset < end; {
		r, size := utf8.DecodeRuneInString(source[offset:end])
		size = max(size, 1)
		if csharpLineBreak(r) {
			lastBreakEnd = offset + size
		}
		offset += size
	}
	if lastBreakEnd < 0 {
		return before || end > start
	}
	return strings.TrimSpace(source[lastBreakEnd:end]) != ""
}

func csharpWhitespace(r rune) bool {
	return unicode.IsSpace(r) || r == '\uFEFF'
}

func csharpLineBreak(r rune) bool {
	return r == '\n' || r == '\r' || r == '\u0085' || r == '\u2028' || r == '\u2029'
}

func csharpLineStarts(source string) []int {
	return cTreeLineStarts(source)
}

func csharpLineColumn(source string, lineStarts []int, offset int) (int, int) {
	positions := cSourcePositions{source: source, lineStarts: lineStarts}
	return positions.lineColumn(offset)
}

func csharpTokenLine(lineStarts []int, offset int) int {
	return sort.Search(len(lineStarts), func(index int) bool { return lineStarts[index] > offset })
}
