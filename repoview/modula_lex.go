package repoview

import (
	"sort"
	"strings"
	"unicode/utf8"
)

const (
	modulaMaximumConcreteParseBytes = 8 << 20
	modulaMaximumConcreteTokens     = 64 << 10
	modulaMaximumRetainedTokens     = 256 << 10
	modulaMaximumRetainedSpans      = 256 << 10
	modulaMaximumStructuralDepth    = 512
	modulaMaximumDeclarationTokens  = 4096
	modulaMaximumCommentDepth       = 512
)

const (
	modulaTokenGap     = "\x00modula-token-gap\x00"
	modulaLiteralToken = "\x00modula-literal\x00"
)

type modulaTokenKind uint8

const (
	modulaTokenIdentifier modulaTokenKind = iota
	modulaTokenNumber
	modulaTokenPunctuation
)

// modulaToken retains logical spelling together with byte-exact physical
// coordinates. Trivia is represented by gaps between tokens, never by
// rewritten source offsets.
type modulaToken struct {
	text       string
	start, end int
	nameStart  int
	kind       modulaTokenKind
	lineStart  bool
	gap        bool
	// directiveClosed is set only on a top-level <* token when the lexer can
	// see its first unshielded *> token. Recovery uses it to keep malformed,
	// paired directives atomic without letting an unmatched opener hide EOF.
	directiveClosed bool
}

type modulaLexResult struct {
	tokens              []modulaToken
	commentSpans        []cByteSpan
	stringSpans         []cByteSpan
	pragmaSpans         []cByteSpan
	lexicalUnits        int
	maximumCommentDepth int

	truncated, spansTruncated, concreteEligible, contentGate bool
}

type modulaLexicalSink struct {
	comment func(cByteSpan) bool
	literal func(cByteSpan) bool
	pragma  func(cByteSpan) bool
	token   func(modulaToken) bool
}

type modulaTokenRetention struct {
	head      []modulaToken
	tail      []modulaToken
	tailStart int
	total     int
}

const (
	modulaGateStart uint8 = iota
	modulaGateModuleKeyword
	modulaGateDefinitionForOrName
	modulaGateDefinitionLiteral
	modulaGateName
	modulaGateAfterName
	modulaGatePriority
	modulaGateAfterPriority
)

// modulaContentGateStream recognizes only the first compilation-unit heading.
// It consumes the unretained token stream, so a valid priority expression can
// cross the head/tail retention frontier without turning a Modula-2 file inert.
type modulaContentGateStream struct {
	state         uint8
	priorityDepth int
	definition    bool
	directive     bool
	done          bool
	valid         bool
}

func (gate *modulaContentGateStream) accept(token modulaToken) {
	if gate.done {
		return
	}
	if token.gap {
		gate.done = true
		return
	}
	if gate.state == modulaGatePriority {
		if gate.directive {
			if token.text == "*>" {
				gate.directive = false
			}
			return
		}
		if token.text == "<*" {
			if !token.directiveClosed {
				gate.done = true
				return
			}
			gate.directive = true
			return
		}
		switch token.text {
		case "[":
			gate.priorityDepth++
			if gate.priorityDepth > modulaMaximumStructuralDepth {
				gate.done = true
			}
		case "]":
			gate.priorityDepth--
			if gate.priorityDepth == 0 {
				gate.state = modulaGateAfterPriority
			} else if gate.priorityDepth < 0 {
				gate.done = true
			}
		}
		return
	}

	identifier := token.kind == modulaTokenIdentifier &&
		!modulaKeyword(token.text)
	switch gate.state {
	case modulaGateStart:
		switch token.text {
		case "MODULE":
			gate.state = modulaGateName
		case "DEFINITION":
			gate.definition = true
			gate.state = modulaGateModuleKeyword
		case "IMPLEMENTATION":
			gate.state = modulaGateModuleKeyword
		default:
			gate.done = true
		}
	case modulaGateModuleKeyword:
		if token.text != "MODULE" {
			gate.done = true
			return
		}
		if gate.definition {
			gate.state = modulaGateDefinitionForOrName
		} else {
			gate.state = modulaGateName
		}
	case modulaGateDefinitionForOrName:
		if token.text == "FOR" {
			gate.state = modulaGateDefinitionLiteral
			return
		}
		if !identifier {
			gate.done = true
			return
		}
		gate.state = modulaGateAfterName
	case modulaGateDefinitionLiteral:
		if !modulaGNUStringToken(token) {
			gate.done = true
			return
		}
		gate.state = modulaGateName
	case modulaGateName:
		if !identifier {
			gate.done = true
			return
		}
		gate.state = modulaGateAfterName
	case modulaGateAfterName:
		switch {
		case token.text == ";":
			gate.valid = true
			gate.done = true
		case !gate.definition && token.text == "[":
			gate.priorityDepth = 1
			gate.state = modulaGatePriority
		default:
			gate.done = true
		}
	case modulaGateAfterPriority:
		if token.text == ";" {
			gate.valid = true
		}
		gate.done = true
	default:
		gate.done = true
	}
}

func (retention *modulaTokenRetention) append(token modulaToken) {
	retention.total++
	headLimit := (modulaMaximumRetainedTokens - 1) / 2
	tailLimit := modulaMaximumRetainedTokens - headLimit
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

func (retention *modulaTokenRetention) result() ([]modulaToken, bool) {
	if retention.total <= modulaMaximumRetainedTokens {
		return append(retention.head, retention.tail...), false
	}
	tokens := make([]modulaToken, 0, modulaMaximumRetainedTokens)
	tokens = append(tokens, retention.head...)
	tokens = append(tokens, modulaToken{
		text: modulaTokenGap, kind: modulaTokenPunctuation, gap: true,
	})
	tailCount := modulaMaximumRetainedTokens - len(tokens)
	tailCount = min(tailCount, len(retention.tail))
	skip := len(retention.tail) - tailCount
	for index := range tailCount {
		position := (retention.tailStart + skip + index) % len(retention.tail)
		tokens = append(tokens, retention.tail[position])
	}
	return tokens, true
}

func lexModula(source string) modulaLexResult {
	result := modulaLexResult{concreteEligible: len(source) <= modulaMaximumConcreteParseBytes}
	var retention modulaTokenRetention
	var contentGate modulaContentGateStream
	appendSpan := func(destination *[]cByteSpan, span cByteSpan) bool {
		result.lexicalUnits++
		if span.start >= span.end {
			return true
		}
		if len(*destination) < modulaMaximumRetainedSpans {
			*destination = append(*destination, span)
		} else {
			result.spansTruncated = true
		}
		return true
	}
	walker := modulaLexicalWalker{
		source: source,
		sink: modulaLexicalSink{
			comment: func(span cByteSpan) bool {
				return appendSpan(&result.commentSpans, span)
			},
			literal: func(span cByteSpan) bool {
				return appendSpan(&result.stringSpans, span)
			},
			pragma: func(span cByteSpan) bool {
				return appendSpan(&result.pragmaSpans, span)
			},
			token: func(token modulaToken) bool {
				result.lexicalUnits++
				contentGate.accept(token)
				retention.append(token)
				return true
			},
		},
		lineOnlySpace: true,
	}
	walker.walk()
	result.maximumCommentDepth = walker.maximumCommentDepth
	result.contentGate = contentGate.valid
	result.tokens, result.truncated = retention.result()
	if walker.unterminatedOpaque || walker.invalidEncoding ||
		result.truncated || result.spansTruncated ||
		result.lexicalUnits > modulaMaximumConcreteTokens ||
		result.maximumCommentDepth > modulaMaximumCommentDepth {
		result.concreteEligible = false
	}
	return result
}

func walkModulaLexically(source string, sink modulaLexicalSink) bool {
	walker := modulaLexicalWalker{
		source: source, sink: sink, lineOnlySpace: true,
	}
	walker.walk()
	return !walker.stopped
}

type modulaLexicalWalker struct {
	source string
	sink   modulaLexicalSink

	offset, maximumCommentDepth int
	directiveEnd                int
	lineOnlySpace               bool
	directiveSearchExhausted    bool
	unterminatedOpaque          bool
	invalidEncoding             bool
	stopped                     bool
}

func (walker *modulaLexicalWalker) walk() {
	for walker.offset < len(walker.source) && !walker.stopped {
		if walker.offset == 0 && strings.HasPrefix(walker.source, "\uFEFF") {
			walker.offset += len("\uFEFF")
			continue
		}
		if strings.HasPrefix(walker.source[walker.offset:], "(*") {
			walker.scanComment()
			continue
		}
		if strings.HasPrefix(walker.source[walker.offset:], "<*") ||
			strings.HasPrefix(walker.source[walker.offset:], "*>") {
			start := walker.offset
			walker.offset += 2
			directiveClosed := false
			if walker.source[start:walker.offset] == "<*" &&
				walker.directiveEnd == 0 && !walker.directiveSearchExhausted {
				if end, ok := modulaDirectiveEnd(walker.source, walker.offset); ok {
					walker.directiveEnd = end
					directiveClosed = true
				} else {
					walker.directiveSearchExhausted = true
				}
			}
			walker.emitToken(modulaToken{
				text: walker.source[start:walker.offset], start: start,
				end: walker.offset, nameStart: start,
				kind: modulaTokenPunctuation, lineStart: walker.lineOnlySpace,
				directiveClosed: directiveClosed,
			})
			if walker.offset == walker.directiveEnd {
				walker.directiveEnd = 0
			}
			continue
		}
		if walker.source[walker.offset] == '#' &&
			modulaPhysicalColumnOne(walker.source, walker.offset) {
			walker.scanCPPLineMarker()
			continue
		}

		r, size := utf8.DecodeRuneInString(walker.source[walker.offset:])
		if r == utf8.RuneError && size == 1 {
			walker.invalidEncoding = true
			walker.emitToken(modulaToken{
				text:  walker.source[walker.offset : walker.offset+1],
				start: walker.offset, end: walker.offset + 1,
				nameStart: walker.offset, kind: modulaTokenPunctuation,
				lineStart: walker.lineOnlySpace,
			})
			walker.offset++
			continue
		}
		if modulaWhitespace(r) {
			walker.consumeWhitespace(size, r)
			continue
		}
		if r == '\'' || r == '"' {
			walker.scanString(byte(r))
			continue
		}

		start := walker.offset
		token := modulaToken{
			start: start, nameStart: start, lineStart: walker.lineOnlySpace,
		}
		if modulaIdentifierStart(r) {
			walker.offset += size
			for walker.offset < len(walker.source) {
				next, nextSize := utf8.DecodeRuneInString(walker.source[walker.offset:])
				if next == utf8.RuneError && nextSize == 1 ||
					!modulaIdentifierContinue(next) {
					break
				}
				walker.offset += nextSize
			}
			token.kind = modulaTokenIdentifier
		} else if modulaASCIIDigit(r) || r == '.' && modulaLeadingDotNumber(
			walker.source, walker.offset,
		) {
			walker.offset = modulaNumberEnd(walker.source, walker.offset)
			token.kind = modulaTokenNumber
		} else {
			if canonical, width, ok := modulaAlternatePunctuation(
				walker.source, start,
			); ok {
				walker.offset = start + width
				token.end = walker.offset
				token.text = canonical
				token.kind = modulaTokenPunctuation
				walker.emitToken(token)
				continue
			}
			walker.offset += size
			if size == 1 && strings.HasPrefix(walker.source[start:], "...") {
				walker.offset = start + 3
			} else if size == 1 && walker.offset < len(walker.source) &&
				modulaDoublePunctuation(walker.source[start:walker.offset+1]) {
				walker.offset++
			}
			token.kind = modulaTokenPunctuation
		}
		token.end = walker.offset
		token.text = walker.source[token.start:token.end]
		walker.emitToken(token)
	}
}

// modulaDirectiveEnd returns the byte immediately after the first top-level
// *> visible to GNU Modula-2's lexer. Strings, nested comments, and physical
// column-one preprocessor markers shield delimiter spellings.
func modulaDirectiveEnd(source string, start int) (int, bool) {
	for offset := max(start, 0); offset < len(source); {
		switch {
		case strings.HasPrefix(source[offset:], "*>"):
			return offset + 2, true
		case strings.HasPrefix(source[offset:], "(*"):
			offset = modulaDirectiveSkipComment(source, offset)
		case source[offset] == '#' && modulaPhysicalColumnOne(source, offset):
			for offset < len(source) && source[offset] != '\n' && source[offset] != '\r' {
				offset++
			}
		case source[offset] == '\'' || source[offset] == '"':
			delimiter := source[offset]
			offset++
			for offset < len(source) && source[offset] != '\n' {
				character := source[offset]
				offset++
				if character == delimiter {
					break
				}
			}
		default:
			_, size := utf8.DecodeRuneInString(source[offset:])
			if size < 1 {
				size = 1
			}
			offset += size
		}
	}
	return 0, false
}

func modulaDirectiveSkipComment(source string, start int) int {
	offset := start + 2
	depth := 1
	for offset < len(source) && depth > 0 {
		switch {
		case depth == 1 && strings.HasPrefix(source[offset:], "<*"):
			// Mirror scanComment's GNU COMMENT1 substate. Within it, (* is
			// payload; either *> resumes the comment or *) recovers by closing
			// the surrounding comment.
			offset += 2
			directiveDone := false
			for offset < len(source) {
				switch {
				case strings.HasPrefix(source[offset:], "*>"):
					offset += 2
					directiveDone = true
				case strings.HasPrefix(source[offset:], "*)"):
					offset += 2
					depth = 0
					directiveDone = true
				default:
					_, size := utf8.DecodeRuneInString(source[offset:])
					if size < 1 {
						size = 1
					}
					offset += size
				}
				if directiveDone {
					break
				}
			}
		case strings.HasPrefix(source[offset:], "(*"):
			depth++
			offset += 2
		case strings.HasPrefix(source[offset:], "*)"):
			depth--
			offset += 2
		default:
			_, size := utf8.DecodeRuneInString(source[offset:])
			if size < 1 {
				size = 1
			}
			offset += size
		}
	}
	return offset
}

func (walker *modulaLexicalWalker) scanCPPLineMarker() {
	start := walker.offset
	end := start
	for end < len(walker.source) && walker.source[end] != '\n' &&
		walker.source[end] != '\r' {
		end++
	}
	walker.offset = end
	walker.emitSpan(walker.sink.pragma, cByteSpan{start: start, end: end})
}

func modulaPhysicalColumnOne(source string, offset int) bool {
	return offset == 0 || offset > 0 && source[offset-1] == '\n'
}

func (walker *modulaLexicalWalker) consumeWhitespace(size int, r rune) {
	if r == '\r' {
		walker.offset += size
		if walker.offset < len(walker.source) && walker.source[walker.offset] == '\n' {
			walker.offset++
		}
		walker.lineOnlySpace = true
		return
	}
	walker.offset += size
	if r == '\n' {
		walker.lineOnlySpace = true
	}
}

func (walker *modulaLexicalWalker) scanComment() {
	start := walker.offset
	walker.offset += 2
	depth := 1
	walker.maximumCommentDepth = max(walker.maximumCommentDepth, depth)
	for walker.offset < len(walker.source) && depth > 0 {
		switch {
		case depth == 1 && strings.HasPrefix(walker.source[walker.offset:], "<*"):
			// GNU Modula-2 treats a directive inside a level-one comment as an
			// opaque substate. In particular, `*)` in its payload cannot close
			// the surrounding comment.
			walker.offset += 2
			directiveDone := false
			for walker.offset < len(walker.source) {
				switch {
				case strings.HasPrefix(walker.source[walker.offset:], "*>"):
					walker.offset += 2
					directiveDone = true
				case strings.HasPrefix(walker.source[walker.offset:], "*)"):
					// Recover the malformed directive at the surrounding
					// comment delimiter, matching the compiler lexer.
					walker.offset += 2
					depth = 0
					directiveDone = true
				default:
					walker.consumeOpaqueByte()
				}
				if directiveDone {
					break
				}
			}
			if !directiveDone {
				walker.unterminatedOpaque = true
			}
		case strings.HasPrefix(walker.source[walker.offset:], "(*"):
			depth++
			walker.maximumCommentDepth = max(walker.maximumCommentDepth, depth)
			walker.offset += 2
		case strings.HasPrefix(walker.source[walker.offset:], "*)"):
			depth--
			walker.offset += 2
		default:
			walker.consumeOpaqueByte()
		}
	}
	if depth != 0 {
		walker.unterminatedOpaque = true
	}
	walker.emitSpan(walker.sink.comment, cByteSpan{start: start, end: walker.offset})
}

func (walker *modulaLexicalWalker) scanString(delimiter byte) {
	start := walker.offset
	walker.offset++
	terminated := false
	for walker.offset < len(walker.source) {
		character := walker.source[walker.offset]
		if character == '\n' {
			break
		}
		if character == delimiter {
			walker.offset++
			terminated = true
			break
		}
		walker.offset++
	}
	if !terminated {
		walker.unterminatedOpaque = true
	}
	walker.emitSpan(walker.sink.literal, cByteSpan{start: start, end: walker.offset})
	walker.emitToken(modulaToken{
		text: modulaLiteralToken, start: start, end: walker.offset,
		nameStart: start, kind: modulaTokenNumber,
		lineStart: walker.lineOnlySpace,
	})
}

func (walker *modulaLexicalWalker) consumeOpaqueByte() {
	if walker.offset >= len(walker.source) {
		return
	}
	if walker.source[walker.offset] == '\r' {
		walker.offset++
		if walker.offset < len(walker.source) && walker.source[walker.offset] == '\n' {
			walker.offset++
		}
		walker.lineOnlySpace = true
		return
	}
	if walker.source[walker.offset] == '\n' {
		walker.offset++
		walker.lineOnlySpace = true
		return
	}
	_, size := utf8.DecodeRuneInString(walker.source[walker.offset:])
	if size < 1 {
		size = 1
	}
	walker.offset += size
}

func (walker *modulaLexicalWalker) emitSpan(
	consumer func(cByteSpan) bool,
	span cByteSpan,
) {
	if consumer != nil && !consumer(span) {
		walker.stopped = true
	}
}

func (walker *modulaLexicalWalker) emitToken(token modulaToken) {
	walker.lineOnlySpace = false
	if walker.sink.token != nil && !walker.sink.token(token) {
		walker.stopped = true
	}
}

func modulaWhitespace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\r'
}

func modulaIdentifierStart(r rune) bool {
	return r == '_' || r >= 'A' && r <= 'Z' || r >= 'a' && r <= 'z'
}

func modulaIdentifierContinue(r rune) bool {
	return modulaIdentifierStart(r) || modulaASCIIDigit(r)
}

func modulaASCIIDigit(r rune) bool {
	return r >= '0' && r <= '9'
}

func modulaLeadingDotNumber(source string, start int) bool {
	return modulaLeadingDotRealEnd(source, start) > start+1
}

func modulaNumberEnd(source string, start int) int {
	if start < 0 || start >= len(source) {
		return start
	}
	if source[start] == '.' {
		return modulaLeadingDotRealEnd(source, start)
	}

	digitsEnd := start
	for digitsEnd < len(source) && modulaASCIIByteDigit(source[digitsEnd]) {
		digitsEnd++
	}
	end := digitsEnd

	// GNU Modula-2's suffixed integer rules compete by longest match.
	if digitsEnd < len(source) &&
		(source[digitsEnd] == 'B' || source[digitsEnd] == 'C') {
		end = max(end, digitsEnd+1)
	}
	binaryEnd := start
	for binaryEnd < len(source) &&
		(source[binaryEnd] == '0' || source[binaryEnd] == '1') {
		binaryEnd++
	}
	if binaryEnd > start && binaryEnd < len(source) && source[binaryEnd] == 'A' {
		end = max(end, binaryEnd+1)
	}
	hexEnd := start
	for hexEnd < len(source) && (modulaASCIIByteDigit(source[hexEnd]) ||
		source[hexEnd] >= 'A' && source[hexEnd] <= 'F') {
		hexEnd++
	}
	if hexEnd > start && hexEnd < len(source) && source[hexEnd] == 'H' {
		end = max(end, hexEnd+1)
	}

	if digitsEnd < len(source) && source[digitsEnd] == '.' &&
		(digitsEnd+1 >= len(source) || source[digitsEnd+1] != '.') {
		realEnd := digitsEnd + 1
		for realEnd < len(source) && modulaASCIIByteDigit(source[realEnd]) {
			realEnd++
		}
		hasFraction := realEnd > digitsEnd+1
		exponentEnd := modulaExponentEnd(source, realEnd)
		if hasFraction {
			end = max(end, realEnd)
		}
		if exponentEnd > realEnd {
			end = max(end, exponentEnd)
		}
	}
	return max(start+1, end)
}

func modulaLeadingDotRealEnd(source string, start int) int {
	if start < 0 || start+1 >= len(source) || source[start] != '.' ||
		source[start+1] == '.' {
		return start + 1
	}
	offset := start + 1
	for offset < len(source) && modulaASCIIByteDigit(source[offset]) {
		offset++
	}
	hasFraction := offset > start+1
	if exponentEnd := modulaExponentEnd(source, offset); exponentEnd > offset {
		return exponentEnd
	}
	if hasFraction {
		return offset
	}
	return start + 1
}

func modulaExponentEnd(source string, start int) int {
	if start < 0 || start >= len(source) || source[start] != 'E' {
		return start
	}
	offset := start + 1
	if offset < len(source) && (source[offset] == '+' || source[offset] == '-') {
		offset++
	}
	digitsStart := offset
	for offset < len(source) && modulaASCIIByteDigit(source[offset]) {
		offset++
	}
	if offset == digitsStart {
		return start
	}
	return offset
}

func modulaASCIIByteDigit(character byte) bool {
	return character >= '0' && character <= '9'
}

func modulaAlternatePunctuation(source string, start int) (string, int, bool) {
	if start < 0 || start >= len(source) {
		return "", 0, false
	}
	for _, alternate := range []struct {
		physical, logical string
	}{
		{physical: "(!", logical: "["},
		{physical: "!)", logical: "]"},
		{physical: "(:", logical: "{"},
		{physical: ":)", logical: "}"},
	} {
		if strings.HasPrefix(source[start:], alternate.physical) {
			return alternate.logical, len(alternate.physical), true
		}
	}
	switch source[start] {
	case '@':
		return "^", 1, true
	case '!':
		return "|", 1, true
	case '~':
		return "NOT", 1, true
	default:
		return "", 0, false
	}
}

func modulaDoublePunctuation(text string) bool {
	switch text {
	case ":=", "<=", ">=", "<>", "..", "->", "::":
		return true
	default:
		return false
	}
}

func modulaLineStarts(source string) []int {
	starts := []int{0}
	for offset := 0; offset < len(source); offset++ {
		if source[offset] == '\n' {
			starts = append(starts, offset+1)
		}
	}
	return starts
}

func modulaTokenLine(lineStarts []int, offset int) int {
	if len(lineStarts) == 0 {
		return 1
	}
	offset = max(offset, 0)
	return sort.Search(len(lineStarts), func(index int) bool {
		return lineStarts[index] > offset
	})
}

// GNU Modula-2's lexer maps these predefined macros to the same stringtok
// terminal as a quoted literal. __LINE__ and __COLUMN__ instead map to the
// integer terminal and intentionally do not belong here.
func modulaGNUStringToken(token modulaToken) bool {
	if token.gap {
		return false
	}
	switch token.text {
	case modulaLiteralToken, "__DATE__", "__FILE__", "__FUNCTION__":
		return true
	default:
		return false
	}
}

func modulaContentGate(lexed modulaLexResult) bool {
	return lexed.contentGate
}

func modulaKeyword(text string) bool {
	switch text {
	case "AND", "ARRAY", "ASM", "BEGIN", "BY", "CASE", "CONST", "DEFINITION",
		"DIV", "DO", "ELSE", "ELSIF", "END", "EXCEPT", "EXIT", "EXPORT",
		"FINALLY", "FOR", "FORWARD", "FROM", "IF", "IMPLEMENTATION", "IMPORT",
		"IN", "LOOP", "MOD", "MODULE", "NOT", "OF", "OR", "PACKEDSET",
		"POINTER", "PROCEDURE", "QUALIFIED", "RECORD", "REM", "REPEAT",
		"RETRY", "RETURN", "SET", "THEN", "TO", "TYPE", "UNQUALIFIED",
		"UNTIL", "VAR", "VOLATILE", "WHILE", "WITH", "__ATTRIBUTE__",
		"__BUILTIN__", "__COLUMN__", "__DATE__", "__FILE__", "__FUNCTION__",
		"__INLINE__", "__LINE__":
		return true
	default:
		return false
	}
}
