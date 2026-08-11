package navigator

import (
	"sort"
	"strings"
	"unicode/utf8"
)

type rustByteSpan struct {
	start int
	end   int
}

type rustLineSpan struct {
	start int
	end   int
}

type rustLineScope struct {
	start int
	end   int
}

type rustOpaqueTokenRange struct {
	start       int
	end         int
	visibleOpen int
}

type rustToken struct {
	text       string
	start, end int
	lineStart  bool
	raw        bool
}

type rustItemBoundaryKey struct {
	keywordIndex int
	itemBoundary bool
}

type rustItemBoundary struct {
	bodyIndex int
	endIndex  int
	recovered bool
}

type rustLexResult struct {
	trustedDefinitions   map[rustDefinitionIdentity]bool
	recoveredDefinitions map[rustDefinitionIdentity]bool
	delimiters           map[int]int
	commentSpans         []rustByteSpan
	stringSpans          []rustByteSpan
	syntaxOpaqueSpans    []rustByteSpan
	definitions          []sourceDefinition
	scopes               []rustLineScope
	imports              []rustLineSpan
	tokens               []rustToken
}

type rustLexer struct {
	itemBounds          map[rustItemBoundaryKey]rustItemBoundary
	recoveredItemStarts map[int]bool
	angleDelimiters     map[int]int
	source              string
	lineStarts          []int
	comments            []rustByteSpan
	strings             []rustByteSpan
	tokens              []rustToken
	itemScanWork        int
}

const rustItemScanBudgetFactor = 8

func lexRust(source string) rustLexResult {
	lexer := &rustLexer{source: source, lineStarts: rustLineStarts(source)}
	lexer.scan()
	delimiters := rustMatchDelimiters(lexer.tokens)
	lexer.angleDelimiters = rustMatchAngles(lexer.tokens, delimiters)
	opaqueRanges := lexer.opaqueTokenRanges(delimiters)
	scopes := lexer.resolveScopes(delimiters, opaqueRanges)
	definitions, trustedDefinitions, recoveredDefinitions := lexer.resolveDefinitions(delimiters)
	imports := lexer.resolveImports(delimiters)
	return rustLexResult{
		commentSpans:         normalizeRustSpans(lexer.comments),
		stringSpans:          normalizeRustSpans(lexer.strings),
		syntaxOpaqueSpans:    rustOpaqueByteSpans(lexer.tokens, opaqueRanges),
		definitions:          definitions,
		trustedDefinitions:   trustedDefinitions,
		recoveredDefinitions: recoveredDefinitions,
		scopes:               scopes,
		imports:              imports,
		tokens:               lexer.tokens,
		delimiters:           delimiters,
	}
}

func rustLineStarts(source string) []int {
	starts := []int{0}
	for offset := 0; offset < len(source); {
		if source[offset] == '\n' {
			offset++
			starts = append(starts, offset)
			continue
		}
		_, size := rustDecode(source, offset)
		offset += size
	}
	return starts
}

func rustDecode(source string, offset int) (rune, int) {
	if offset >= len(source) {
		return utf8.RuneError, 0
	}
	r, size := utf8.DecodeRuneInString(source[offset:])
	if size < 1 {
		size = 1
	}
	return r, size
}

func rustIdentifierStart(r rune) bool {
	return r == '_' || rustRuneInXIDRanges(r, rustXIDStartRanges[:])
}

func rustIdentifierContinue(r rune) bool {
	return rustRuneInXIDRanges(r, rustXIDContinueRanges[:])
}

func rustIdentifierEnd(source string, start int) int {
	r, size := rustDecode(source, start)
	if size == 0 || !rustIdentifierStart(r) {
		return start
	}
	end := start + size
	for end < len(source) {
		r, size = rustDecode(source, end)
		if size == 0 || !rustIdentifierContinue(r) {
			break
		}
		end += size
	}
	return end
}

func rustPreviousIdentifier(source string, offset int) bool {
	if offset <= 0 {
		return false
	}
	r, _ := utf8.DecodeLastRuneInString(source[:offset])
	return rustIdentifierContinue(r)
}

func (lexer *rustLexer) scan() {
	lineHasNonWhitespace := false
	for offset := 0; offset < len(lexer.source); {
		if offset == 0 && strings.HasPrefix(lexer.source, "\uFEFF") {
			lineHasNonWhitespace = true
			offset += len("\uFEFF")
			continue
		}
		shebangOffset := offset == 0 ||
			offset == len("\uFEFF") && strings.HasPrefix(lexer.source, "\uFEFF")
		if shebangOffset && strings.HasPrefix(lexer.source[offset:], "#!") &&
			!strings.HasPrefix(lexer.source[offset:], "#![") {
			end := rustLineEnd(lexer.source, offset)
			lexer.comments = append(lexer.comments, rustByteSpan{start: offset, end: end})
			lineHasNonWhitespace = true
			offset = end
			continue
		}
		if strings.HasPrefix(lexer.source[offset:], "//") {
			end := rustLineEnd(lexer.source, offset)
			lexer.comments = append(lexer.comments, rustByteSpan{start: offset, end: end})
			lineHasNonWhitespace = true
			offset = end
			continue
		}
		if strings.HasPrefix(lexer.source[offset:], "/*") {
			end := rustNestedCommentEnd(lexer.source, offset)
			lexer.comments = append(lexer.comments, rustByteSpan{start: offset, end: end})
			lineHasNonWhitespace = true
			offset = end
			continue
		}
		if end, ok := rustLiteralEnd(lexer.source, offset); ok {
			lexer.strings = append(lexer.strings, rustByteSpan{start: offset, end: end})
			lineHasNonWhitespace = true
			offset = max(end, offset+1)
			continue
		}
		if offset+2 <= len(lexer.source) && lexer.source[offset:offset+2] == "r#" &&
			!rustPreviousIdentifier(lexer.source, offset) {
			nameStart := offset + 2
			nameEnd := rustIdentifierEnd(lexer.source, nameStart)
			lexer.tokens = append(lexer.tokens, rustToken{
				text: lexer.source[offset:nameEnd], start: offset, end: nameEnd,
				lineStart: !lineHasNonWhitespace, raw: true,
			})
			lineHasNonWhitespace = true
			offset = max(nameEnd, offset+2)
			continue
		}
		r, size := rustDecode(lexer.source, offset)
		if rustIdentifierStart(r) {
			end := rustIdentifierEnd(lexer.source, offset)
			lexer.tokens = append(lexer.tokens, rustToken{
				text: lexer.source[offset:end], start: offset, end: end,
				lineStart: !lineHasNonWhitespace,
			})
			lineHasNonWhitespace = true
			offset = end
			continue
		}
		if !rustSpace(r) {
			lexer.tokens = append(lexer.tokens, rustToken{
				text: lexer.source[offset : offset+size], start: offset, end: offset + size,
				lineStart: !lineHasNonWhitespace,
			})
			lineHasNonWhitespace = true
		} else if r == '\n' {
			lineHasNonWhitespace = false
		}
		offset += size
	}
}

func rustSpace(r rune) bool {
	switch r {
	case '\u0009', '\u000A', '\u000B', '\u000C', '\u000D', '\u0020', '\u0085',
		'\u200E', '\u200F', '\u2028', '\u2029':
		return true
	default:
		return false
	}
}

func rustLineEnd(source string, start int) int {
	end := start
	for end < len(source) && source[end] != '\n' {
		_, size := rustDecode(source, end)
		end += size
	}
	return end
}

func rustNestedCommentEnd(source string, start int) int {
	depth := 1
	for offset := start + 2; offset < len(source); {
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
			_, size := rustDecode(source, offset)
			offset += size
		}
	}
	return len(source)
}

func rustLiteralEnd(source string, start int) (int, bool) {
	if start >= len(source) {
		return start, false
	}
	if source[start] == '"' {
		return rustQuotedEnd(source, start+1), true
	}
	if source[start] == '\'' {
		return rustCharLiteralEnd(source, start)
	}
	if rustPreviousIdentifier(source, start) {
		return start, false
	}
	if content, hashes, ok := rustRawStringStart(source, start); ok {
		return rustRawStringEnd(source, content, hashes), true
	}
	if start+1 < len(source) && (source[start] == 'b' || source[start] == 'c') &&
		source[start+1] == '"' {
		return rustQuotedEnd(source, start+2), true
	}
	if start+1 < len(source) && source[start] == 'b' && source[start+1] == '\'' {
		return rustCharLiteralEnd(source, start+1)
	}
	return start, false
}

func rustRawStringStart(source string, start int) (content, hashes int, ok bool) {
	offset := start
	switch {
	case strings.HasPrefix(source[offset:], "br") || strings.HasPrefix(source[offset:], "cr"):
		offset += 2
	case source[offset] == 'r':
		offset++
	default:
		return 0, 0, false
	}
	for offset < len(source) && source[offset] == '#' {
		hashes++
		if hashes > 255 {
			return 0, 0, false
		}
		offset++
	}
	if offset >= len(source) || source[offset] != '"' {
		return 0, 0, false
	}
	return offset + 1, hashes, true
}

func rustCharLiteralEnd(source string, quote int) (int, bool) {
	content := quote + 1
	if content >= len(source) || source[content] == '\r' || source[content] == '\n' ||
		source[content] == '\'' {
		return quote, false
	}

	end := content
	if source[content] == '\\' {
		var ok bool
		end, ok = rustEscapeEnd(source, content)
		if !ok {
			_, size := rustDecode(source, content+1)
			end = content + 1 + size
			if size == 0 || end >= len(source) || source[end] != '\'' {
				return quote, false
			}
		}
	} else {
		_, size := rustDecode(source, content)
		if size == 0 {
			return quote, false
		}
		end += size
	}
	if end >= len(source) || source[end] != '\'' {
		return quote, false
	}
	return end + 1, true
}

func rustEscapeEnd(source string, slash int) (int, bool) {
	if slash+1 >= len(source) {
		return slash, false
	}
	switch source[slash+1] {
	case '\\', '\'', '"', 'n', 'r', 't', '0':
		return slash + 2, true
	case 'x':
		if slash+3 >= len(source) || !rustHex(source[slash+2]) || !rustHex(source[slash+3]) {
			return slash, false
		}
		return slash + 4, true
	case 'u':
		if slash+2 >= len(source) || source[slash+2] != '{' {
			return slash, false
		}
		digits := 0
		for offset := slash + 3; offset < len(source); offset++ {
			switch {
			case rustHex(source[offset]):
				digits++
				if digits > 6 {
					return slash, false
				}
			case source[offset] == '_':
				// Separators do not contribute to the six-digit limit.
			case source[offset] == '}' && digits > 0:
				return offset + 1, true
			default:
				return slash, false
			}
		}
	}
	return slash, false
}

func rustHex(value byte) bool {
	return value >= '0' && value <= '9' || value >= 'a' && value <= 'f' ||
		value >= 'A' && value <= 'F'
}

func rustRawStringEnd(source string, content, hashes int) int {
	for offset := content; offset < len(source); {
		if source[offset] == '"' {
			end := offset + 1
			matched := 0
			for matched < hashes && end+matched < len(source) && source[end+matched] == '#' {
				matched++
			}
			if matched == hashes {
				return end + hashes
			}
		}
		_, size := rustDecode(source, offset)
		offset += size
	}
	return len(source)
}

func rustQuotedEnd(source string, content int) int {
	for offset := content; offset < len(source); {
		if source[offset] == '\\' {
			offset++
			if offset < len(source) {
				_, size := rustDecode(source, offset)
				offset += size
			}
			continue
		}
		if source[offset] == '"' {
			return offset + 1
		}
		_, size := rustDecode(source, offset)
		offset += size
	}
	return len(source)
}

func normalizeRustSpans(spans []rustByteSpan) []rustByteSpan {
	if len(spans) == 0 {
		return nil
	}
	sort.Slice(spans, func(left, right int) bool {
		if spans[left].start != spans[right].start {
			return spans[left].start < spans[right].start
		}
		return spans[left].end < spans[right].end
	})
	result := make([]rustByteSpan, 0, len(spans))
	for _, span := range spans {
		if span.start < 0 || span.end <= span.start {
			continue
		}
		if len(result) == 0 || span.start > result[len(result)-1].end {
			result = append(result, span)
			continue
		}
		if span.end > result[len(result)-1].end {
			result[len(result)-1].end = span.end
		}
	}
	return result
}

func maskRustSource(source string, spans []rustByteSpan) string {
	masked := []byte(source)
	for _, span := range normalizeRustSpans(spans) {
		start, end := max(span.start, 0), min(span.end, len(masked))
		for offset := start; offset < end; offset++ {
			if masked[offset] != '\r' && masked[offset] != '\n' {
				masked[offset] = ' '
			}
		}
	}
	return string(masked)
}

func rustMatchDelimiters(tokens []rustToken) map[int]int {
	result := make(map[int]int)
	stack := make([]int, 0, 16)
	var typeStacks [3][]int
	active := make([]bool, len(tokens))
	for index, token := range tokens {
		kind, opener, delimiter := rustDelimiterKind(token.text)
		if !delimiter {
			continue
		}
		if opener {
			stack = append(stack, index)
			typeStacks[kind] = append(typeStacks[kind], index)
			active[index] = true
			continue
		}

		matching := typeStacks[kind]
		for len(matching) > 0 && !active[matching[len(matching)-1]] {
			matching = matching[:len(matching)-1]
		}
		if len(matching) == 0 {
			typeStacks[kind] = matching
			continue
		}
		open := matching[len(matching)-1]
		typeStacks[kind] = matching[:len(matching)-1]
		for len(stack) > 0 {
			popped := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			active[popped] = false
			if popped == open {
				break
			}
		}
		result[open] = index
		result[index] = open
	}
	return result
}

func rustDelimiterKind(token string) (kind int, opener, delimiter bool) {
	switch token {
	case "(":
		return 0, true, true
	case ")":
		return 0, false, true
	case "[":
		return 1, true, true
	case "]":
		return 1, false, true
	case "{":
		return 2, true, true
	case "}":
		return 2, false, true
	default:
		return 0, false, false
	}
}

func rustMatchAngles(tokens []rustToken, delimiters map[int]int) map[int]int {
	type angleFrame struct {
		opens []int
		close int
	}
	result := make(map[int]int)
	frames := []angleFrame{{close: len(tokens)}}
	for index, token := range tokens {
		for len(frames) > 1 && frames[len(frames)-1].close < index {
			frames = frames[:len(frames)-1]
		}
		frame := &frames[len(frames)-1]
		if len(frame.opens) > 0 {
			prefixStart, hardItem := rustUnambiguousItemStart(tokens, index, delimiters)
			if hardItem && tokens[prefixStart].lineStart {
				constGeneric := !token.raw && token.text == "const" &&
					rustConstGenericParameter(
						tokens,
						index,
						frame.opens[len(frame.opens)-1],
						delimiters,
					)
				if !constGeneric {
					frame.opens = nil
				}
			}
		}
		if token.text == "(" || token.text == "[" || token.text == "{" {
			if closeIndex, paired := delimiters[index]; paired && closeIndex > index {
				if token.text == "{" && !rustBraceContinuesAngle(tokens, index) {
					frame.opens = nil
				}
				frames = append(frames, angleFrame{close: closeIndex})
				continue
			}
		}
		switch token.text {
		case "<":
			frame.opens = append(frame.opens, index)
		case ">":
			if rustJointPunctuation(tokens, index-1, index, "-", ">") ||
				rustJointPunctuation(tokens, index-1, index, "=", ">") ||
				rustJointPunctuation(tokens, index, index+1, ">", "=") ||
				len(frame.opens) == 0 {
				continue
			}
			openIndex := frame.opens[len(frame.opens)-1]
			frame.opens = frame.opens[:len(frame.opens)-1]
			result[openIndex] = index
			result[index] = openIndex
		case ";":
			frame.opens = nil
		}
	}
	return result
}

func rustBraceContinuesAngle(tokens []rustToken, index int) bool {
	if index <= 0 || index > len(tokens) {
		return false
	}
	switch tokens[index-1].text {
	case "<", ",", "=", "!":
		return true
	default:
		return false
	}
}

func rustConstGenericParameter(
	tokens []rustToken,
	constIndex int,
	angleOpen int,
	delimiters map[int]int,
) bool {
	if !rustConstGenericParameterBoundary(tokens, constIndex, angleOpen, delimiters) {
		return false
	}
	if _, named := rustNamedItemToken(tokens, constIndex, "const"); !named {
		return false
	}
	sawColon := false
	sawDefault := false
	nestedAngles := 0
	for index := constIndex + 2; index < len(tokens); index++ {
		if index > constIndex+2 {
			prefixStart, hardItem := rustUnambiguousItemStart(tokens, index, delimiters)
			if hardItem && tokens[prefixStart].lineStart {
				return false
			}
		}
		if tokens[index].text == "(" || tokens[index].text == "[" || tokens[index].text == "{" {
			if closeIndex, paired := delimiters[index]; paired && closeIndex > index {
				index = closeIndex
				continue
			}
		}
		switch tokens[index].text {
		case ":":
			sawColon = true
		case "=":
			sawDefault = true
		case "<":
			nestedAngles++
		case ",":
			if nestedAngles == 0 {
				return sawColon
			}
		case ";":
			return false
		case ">":
			if rustJointPunctuation(tokens, index-1, index, "-", ">") ||
				rustJointPunctuation(tokens, index-1, index, "=", ">") ||
				rustJointPunctuation(tokens, index, index+1, ">", "=") {
				continue
			}
			if nestedAngles > 0 {
				nestedAngles--
				continue
			}
			if sawDefault && rustJointPunctuation(tokens, index, index+1, ">", ">") {
				index++
				continue
			}
			return sawColon && rustGenericCloseCanPrecede(
				tokens,
				index,
				angleOpen,
			)
		}
	}
	return false
}

func rustConstGenericParameterBoundary(
	tokens []rustToken,
	constIndex int,
	angleOpen int,
	delimiters map[int]int,
) bool {
	index := constIndex - 1
	for index > angleOpen && tokens[index].text == "]" {
		openIndex, paired := delimiters[index]
		if !paired || openIndex <= angleOpen || tokens[openIndex].text != "[" ||
			openIndex == 0 || tokens[openIndex-1].text != "#" {
			return false
		}
		index = openIndex - 2
	}
	return index == angleOpen ||
		index > angleOpen && tokens[index].text == ","
}

func rustGenericCloseCanPrecede(
	tokens []rustToken,
	closeIndex int,
	angleOpen int,
) bool {
	if closeIndex+1 >= len(tokens) {
		return true
	}
	implParameters := angleOpen > 0 && !tokens[angleOpen-1].raw &&
		tokens[angleOpen-1].text == "impl"
	next := tokens[closeIndex+1].text
	switch next {
	case ">", ",", ")", "]", ";", "=", ":", "where":
		return true
	case "-":
		return rustJointPunctuation(tokens, closeIndex+1, closeIndex+2, "-", ">")
	case "!":
		return implParameters
	case "&", "*", "[", "<":
		return implParameters
	case "(", "{":
		return true
	}
	return implParameters && rustTokenIdentifier(tokens[closeIndex+1])
}

func rustJointPunctuation(
	tokens []rustToken,
	leftIndex int,
	rightIndex int,
	left string,
	right string,
) bool {
	return leftIndex >= 0 && rightIndex >= 0 && leftIndex < len(tokens) &&
		rightIndex < len(tokens) && tokens[leftIndex].text == left &&
		tokens[rightIndex].text == right && tokens[leftIndex].end == tokens[rightIndex].start
}

func (lexer *rustLexer) opaqueTokenRanges(delimiters map[int]int) []rustOpaqueTokenRange {
	tokens := lexer.tokens
	ranges := make([]rustOpaqueTokenRange, 0)
	for index := 0; index < len(tokens); index++ {
		if closeIndex, ok := rustAttributeClose(tokens, index, delimiters); ok {
			ranges = append(ranges, rustOpaqueTokenRange{
				start: index, end: closeIndex, visibleOpen: -1,
			})
			index = closeIndex
			continue
		}

		if !tokens[index].raw && (tokens[index].text == "macro_rules" || tokens[index].text == "macro") {
			if _, item := rustItemPrefixStart(tokens, index, delimiters); item {
				bodyIndex, endIndex, _ := lexer.itemEnd(index, item, delimiters)
				if bodyIndex >= 0 {
					ranges = append(ranges, rustOpaqueTokenRange{
						start: bodyIndex, end: endIndex, visibleOpen: bodyIndex,
					})
					index = endIndex
					continue
				}
			}
		}

		if tokens[index].text == "!" {
			if openIndex, closeIndex, ok := rustMacroInvocationBounds(tokens, index, delimiters); ok {
				ranges = append(ranges, rustOpaqueTokenRange{
					start: openIndex, end: closeIndex, visibleOpen: -1,
				})
				index = closeIndex
			}
		}
	}
	return ranges
}

func rustOpaqueByteSpans(tokens []rustToken, ranges []rustOpaqueTokenRange) []rustByteSpan {
	spans := make([]rustByteSpan, 0, len(ranges))
	for _, tokenRange := range ranges {
		if tokenRange.start < 0 || tokenRange.end < tokenRange.start ||
			tokenRange.end >= len(tokens) {
			continue
		}
		spans = append(spans, rustByteSpan{
			start: tokens[tokenRange.start].start,
			end:   tokens[tokenRange.end].end,
		})
	}
	return normalizeRustSpans(spans)
}

func (lexer *rustLexer) lineColumn(offset int) (int, int) {
	offset = max(0, min(offset, len(lexer.source)))
	lineIndex := sort.Search(len(lexer.lineStarts), func(index int) bool {
		return lexer.lineStarts[index] > offset
	}) - 1
	lineIndex = max(lineIndex, 0)
	return lineIndex + 1, offset - lexer.lineStarts[lineIndex] + 1
}

func (lexer *rustLexer) resolveScopes(
	delimiters map[int]int,
	opaqueRanges []rustOpaqueTokenRange,
) []rustLineScope {
	scopes := make([]rustLineScope, 0)
	opaqueIndex := 0
	for openIndex, token := range lexer.tokens {
		closeIndex, paired := delimiters[openIndex]
		if !paired || closeIndex <= openIndex || closeIndex >= len(lexer.tokens) ||
			token.text != "{" {
			continue
		}
		for opaqueIndex < len(opaqueRanges) && opaqueRanges[opaqueIndex].end < openIndex {
			opaqueIndex++
		}
		if opaqueIndex < len(opaqueRanges) &&
			openIndex >= opaqueRanges[opaqueIndex].start &&
			openIndex <= opaqueRanges[opaqueIndex].end &&
			openIndex != opaqueRanges[opaqueIndex].visibleOpen {
			continue
		}
		startOffset := lexer.tokens[openIndex].start
		if signatureIndex := rustLexicalScopeStart(lexer.tokens, openIndex, delimiters); signatureIndex >= 0 {
			startOffset = lexer.tokens[signatureIndex].start
		}
		startLine, _ := lexer.lineColumn(startOffset)
		endLine, _ := lexer.lineColumn(max(lexer.tokens[closeIndex].end-1, 0))
		if startLine >= 1 && endLine >= startLine {
			scopes = append(scopes, rustLineScope{start: startLine, end: endLine})
		}
	}
	sort.Slice(scopes, func(left, right int) bool {
		if scopes[left].start != scopes[right].start {
			return scopes[left].start < scopes[right].start
		}
		return scopes[left].end < scopes[right].end
	})
	unique := scopes[:0]
	for _, scope := range scopes {
		if len(unique) == 0 || unique[len(unique)-1] != scope {
			unique = append(unique, scope)
		}
	}
	return unique
}

func rustLexicalScopeStart(tokens []rustToken, openIndex int, delimiters map[int]int) int {
	start := openIndex
	for index := openIndex - 1; index >= 0; index-- {
		token := tokens[index].text
		if token == ";" || token == "{" || token == "}" {
			break
		}
		if closeIndex, exists := delimiters[index]; exists && closeIndex < index {
			index = closeIndex
			start = index
			continue
		}
		start = index
	}
	return start
}

func (lexer *rustLexer) resolveDefinitions(
	delimiters map[int]int,
) ([]sourceDefinition, map[rustDefinitionIdentity]bool, map[rustDefinitionIdentity]bool) {
	definitions := make([]sourceDefinition, 0)
	trusted := make(map[rustDefinitionIdentity]bool)
	recoveredDefinitions := make(map[rustDefinitionIdentity]bool)
	delimiterContexts := rustMatchedDelimiterContexts(
		lexer.tokens,
		delimiters,
		lexer.angleDelimiters,
	)
	for index := 0; index < len(lexer.tokens); index++ {
		if closeIndex, ok := rustAttributeClose(lexer.tokens, index, delimiters); ok {
			index = closeIndex
			continue
		}
		if lexer.tokens[index].text == "!" {
			if closeIndex, ok := rustMacroInvocationClose(lexer.tokens, index, delimiters); ok {
				index = closeIndex
			}
			continue
		}

		keyword := lexer.tokens[index]
		if keyword.raw || !rustDefinitionKeyword(keyword.text) {
			continue
		}
		prefixStart, item := rustItemPrefixStart(lexer.tokens, index, delimiters)
		insideNonItemGroup := delimiterContexts[index] == '(' || delimiterContexts[index] == '[' ||
			delimiterContexts[index] == '<'
		if insideNonItemGroup {
			continue
		}
		recoveredStart := lexer.recoveredItemStarts[prefixStart]
		if !item {
			implTypeContinuation := keyword.text == "impl" &&
				rustImplTypeContinuation(lexer.tokens, prefixStart)
			if !lexer.tokenStartsPhysicalLine(prefixStart) ||
				implTypeContinuation && !recoveredStart {
				continue
			}
		}
		if keyword.text == "const" && rustNextTokenText(lexer.tokens, index+1) == "fn" {
			continue
		}
		bodyIndex, endIndex, recovered := lexer.itemEnd(index, item, delimiters)

		var nameIndex int
		var ok bool
		if keyword.text == "impl" {
			nameIndex, ok = rustImplNameTokenWithin(
				lexer.tokens, index, bodyIndex, endIndex, delimiters,
			)
		} else {
			nameIndex, ok = rustNamedItemToken(lexer.tokens, index, keyword.text)
		}
		if !ok || nameIndex < 0 || nameIndex >= len(lexer.tokens) {
			if (keyword.text == "macro_rules" || keyword.text == "macro") && bodyIndex >= 0 {
				index = endIndex
			}
			continue
		}
		nameToken := lexer.tokens[nameIndex]
		if !rustTokenIdentifier(nameToken) || nameToken.text == "_" ||
			(!nameToken.raw && rustReservedIdentifier(nameToken.text)) {
			if (keyword.text == "macro_rules" || keyword.text == "macro") && bodyIndex >= 0 {
				index = endIndex
			}
			continue
		}

		if endIndex < nameIndex {
			endIndex = nameIndex
		}
		line, column := lexer.lineColumn(nameToken.start)
		scopeStartOffset := lexer.tokens[prefixStart].start
		scopeStartOffset = lexer.outerDocStart(scopeStartOffset)
		scopeStart, _ := lexer.lineColumn(scopeStartOffset)
		scopeEnd, _ := lexer.lineColumn(max(lexer.tokens[endIndex].end-1, 0))
		ownsScope := bodyIndex >= 0
		ownedEndColumn := 0
		exactBoundary := !recovered && endIndex >= 0 && endIndex < len(lexer.tokens) &&
			(lexer.tokens[endIndex].text == ";" ||
				bodyIndex >= 0 && delimiters[bodyIndex] == endIndex)
		if ownsScope && exactBoundary {
			ownedEndLine, exactEndColumn := lexer.lineColumn(lexer.tokens[endIndex].end)
			if ownedEndLine == scopeEnd {
				ownedEndColumn = exactEndColumn
			}
		}
		definition := sourceDefinition{
			symbol: nameToken.text, line: line, column: column,
			scopeStart: scopeStart, scopeEnd: scopeEnd,
			ownedEndColumn: ownedEndColumn, ownsScope: ownsScope,
		}
		definitions = append(definitions, definition)
		if item {
			trusted[rustDefinitionKey(definition)] = true
		}
		if recovered || recoveredStart {
			recoveredDefinitions[rustDefinitionKey(definition)] = true
		}
		if keyword.text == "enum" && bodyIndex >= 0 {
			variants, recoveredVariants := lexer.resolveEnumVariants(
				bodyIndex,
				endIndex,
				delimiters,
			)
			definitions = append(
				definitions,
				variants...,
			)
			for key := range recoveredVariants {
				recoveredDefinitions[key] = true
			}
		}

		if (keyword.text == "macro_rules" || keyword.text == "macro") && bodyIndex >= 0 {
			index = endIndex
		}
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
	unique := definitions[:0]
	for _, definition := range definitions {
		if len(unique) == 0 || rustDefinitionKey(unique[len(unique)-1]) != rustDefinitionKey(definition) {
			unique = append(unique, definition)
		}
	}
	return unique, trusted, recoveredDefinitions
}

func rustMatchedDelimiterContexts(
	tokens []rustToken,
	delimiters map[int]int,
	angleDelimiters map[int]int,
) []byte {
	type delimiterContext struct {
		open  int
		close int
	}
	contexts := make([]byte, len(tokens))
	stack := make([]delimiterContext, 0, 16)
	for index, token := range tokens {
		for len(stack) > 0 && stack[len(stack)-1].close < index {
			stack = stack[:len(stack)-1]
		}
		if len(stack) > 0 {
			contexts[index] = tokens[stack[len(stack)-1].open].text[0]
		}
		if token.text != "(" && token.text != "[" && token.text != "{" && token.text != "<" {
			continue
		}
		closeIndex, paired := delimiters[index]
		if token.text == "<" {
			closeIndex, paired = angleDelimiters[index]
		}
		if paired && closeIndex > index {
			stack = append(stack, delimiterContext{open: index, close: closeIndex})
		}
	}
	return contexts
}

func (lexer *rustLexer) resolveEnumVariants(
	openIndex, closeIndex int,
	delimiters map[int]int,
) ([]sourceDefinition, map[rustDefinitionIdentity]bool) {
	if openIndex < 0 || closeIndex <= openIndex || closeIndex >= len(lexer.tokens) ||
		lexer.tokens[openIndex].text != "{" || lexer.tokens[closeIndex].text != "}" {
		return nil, nil
	}

	variants := make([]sourceDefinition, 0)
	recoveredVariants := make(map[rustDefinitionIdentity]bool)
	recoveryActive := false
	for index := openIndex + 1; index < closeIndex; {
		prefixStart := index
		for index < closeIndex {
			attributeEnd, attribute := rustAttributeClose(lexer.tokens, index, delimiters)
			if !attribute || attributeEnd >= closeIndex {
				break
			}
			index = attributeEnd + 1
		}
		if index >= closeIndex {
			break
		}
		if lexer.tokens[index].text == "," {
			index++
			continue
		}

		nameIndex := index
		nameToken := lexer.tokens[nameIndex]
		variantEnd := nameIndex
		malformedDelimiter := false
		_, nameColumn := lexer.lineColumn(nameToken.start)
		index++
		for index < closeIndex && lexer.tokens[index].text != "," {
			if malformedDelimiter && lexer.tokenStartsPhysicalLine(index) {
				_, column := lexer.lineColumn(lexer.tokens[index].start)
				if column <= nameColumn && rustTokenIdentifier(lexer.tokens[index]) &&
					(lexer.tokens[index].raw ||
						!rustReservedIdentifier(lexer.tokens[index].text)) {
					recoveryActive = true
					break
				}
			}
			if nestedEnd, paired := delimiters[index]; paired && nestedEnd > index &&
				nestedEnd <= closeIndex {
				variantEnd = nestedEnd
				index = nestedEnd + 1
				continue
			}
			switch lexer.tokens[index].text {
			case "(", "[", "{", ")", "]", "}":
				if _, paired := delimiters[index]; !paired {
					malformedDelimiter = true
				}
			}
			variantEnd = index
			index++
		}
		if index < closeIndex && lexer.tokens[index].text == "," {
			index++
		}
		if !rustTokenIdentifier(nameToken) || nameToken.text == "_" ||
			(!nameToken.raw && rustReservedIdentifier(nameToken.text)) {
			continue
		}

		line, column := lexer.lineColumn(nameToken.start)
		scopeStartOffset := lexer.outerDocStart(lexer.tokens[prefixStart].start)
		scopeStart, _ := lexer.lineColumn(scopeStartOffset)
		scopeEnd, _ := lexer.lineColumn(max(lexer.tokens[variantEnd].end-1, 0))
		definition := sourceDefinition{
			symbol: nameToken.text, line: line, column: column,
			scopeStart: scopeStart, scopeEnd: scopeEnd,
		}
		variants = append(variants, definition)
		if recoveryActive {
			recoveredVariants[rustDefinitionKey(definition)] = true
		}
	}
	return variants, recoveredVariants
}

func rustDefinitionKeyword(keyword string) bool {
	switch keyword {
	case "fn", "struct", "union", "enum", "trait", "impl", "type", "const",
		"static", "mod", "macro_rules", "macro":
		return true
	default:
		return false
	}
}

func rustNextTokenText(tokens []rustToken, index int) string {
	if index < 0 || index >= len(tokens) {
		return ""
	}
	return tokens[index].text
}

func rustItemPrefixStart(tokens []rustToken, keywordIndex int, delimiters map[int]int) (int, bool) {
	start := keywordIndex
	for index := keywordIndex - 1; index >= 0; {
		token := tokens[index].text
		if token == ";" || token == "{" || token == "}" {
			return start, true
		}
		if token == "]" {
			openIndex, paired := delimiters[index]
			if !paired || openIndex <= 0 || tokens[openIndex].text != "[" ||
				tokens[openIndex-1].text != "#" {
				return start, false
			}
			start = openIndex - 1
			index = openIndex - 2
			continue
		}
		if token == ")" {
			openIndex, paired := delimiters[index]
			if !paired || openIndex <= 0 || tokens[openIndex-1].text != "pub" {
				return start, false
			}
			start = openIndex - 1
			index = openIndex - 2
			continue
		}
		switch token {
		case "pub", "unsafe", "safe", "async", "const", "extern", "default", "auto":
			start = index
			index--
		default:
			return start, false
		}
	}
	return start, true
}

func (lexer *rustLexer) tokenStartsPhysicalLine(tokenIndex int) bool {
	if tokenIndex < 0 || tokenIndex >= len(lexer.tokens) {
		return false
	}
	return lexer.tokens[tokenIndex].lineStart
}

func rustUnambiguousItemStart(
	tokens []rustToken,
	index int,
	delimiters map[int]int,
) (int, bool) {
	prefixStart, hardItem := rustHardItemStart(tokens, index, delimiters)
	if !hardItem || index < 0 || index >= len(tokens) {
		return prefixStart, false
	}
	keyword := tokens[index].text
	if keyword == "use" {
		return prefixStart, rustNextTokenText(tokens, index+1) != "<"
	}
	if keyword == "extern" {
		return prefixStart, true
	}
	if keyword == "impl" {
		return prefixStart, !rustImplTypeContinuation(tokens, prefixStart)
	}
	nameIndex, named := rustNamedItemToken(tokens, index, keyword)
	if !named || nameIndex < 0 || nameIndex >= len(tokens) {
		return prefixStart, false
	}
	nameToken := tokens[nameIndex]
	return prefixStart, rustTokenIdentifier(nameToken) && nameToken.text != "_" &&
		(nameToken.raw || !rustReservedIdentifier(nameToken.text))
}

func rustImplTypeContinuation(tokens []rustToken, prefixStart int) bool {
	if prefixStart <= 0 || prefixStart > len(tokens) {
		return false
	}
	cursor := prefixStart - 1
	for cursor >= 0 {
		switch tokens[cursor].text {
		case "mut", "const", "&", "*":
			cursor--
			continue
		}
		if cursor > 0 && tokens[cursor-1].text == "'" &&
			rustTokenIdentifier(tokens[cursor]) {
			cursor -= 2
			continue
		}
		break
	}
	if cursor >= 1 && tokens[cursor].text == ">" && tokens[cursor-1].text == "-" {
		return true
	}
	if cursor < 0 {
		return false
	}
	switch tokens[cursor].text {
	case "=", ":", ",", "+", "|", "<":
		return true
	default:
		return false
	}
}

func rustRecoveryItemStart(
	tokens []rustToken,
	index int,
	malformedDelimiter bool,
	delimiters map[int]int,
) (int, bool) {
	prefixStart, hardItem := rustUnambiguousItemStart(tokens, index, delimiters)
	if index < 0 || index >= len(tokens) {
		return prefixStart, false
	}
	if !tokens[index].raw && tokens[index].text == "impl" &&
		!malformedDelimiter && rustImplTypeContinuation(tokens, prefixStart) {
		return prefixStart, false
	}
	if hardItem {
		return prefixStart, true
	}
	if malformedDelimiter || (!tokens[index].raw && tokens[index].text == "impl") {
		return rustHardItemStart(tokens, index, delimiters)
	}
	return prefixStart, false
}

func rustHardItemStart(tokens []rustToken, index int, delimiters map[int]int) (int, bool) {
	if index < 0 || index >= len(tokens) || tokens[index].raw {
		return index, false
	}
	isHardItem := rustDefinitionKeyword(tokens[index].text) || tokens[index].text == "use" ||
		(tokens[index].text == "extern" && index+1 < len(tokens) &&
			!tokens[index+1].raw && tokens[index+1].text == "crate")
	if !isHardItem {
		return index, false
	}
	prefixStart, _ := rustItemPrefixStart(tokens, index, delimiters)
	return prefixStart, true
}

func rustNamedItemToken(tokens []rustToken, keywordIndex int, keyword string) (int, bool) {
	index := keywordIndex + 1
	if keyword == "static" && index < len(tokens) && tokens[index].text == "mut" {
		index++
	}
	if keyword == "macro_rules" {
		if index >= len(tokens) || tokens[index].text != "!" {
			return -1, false
		}
		index++
	}
	if index >= len(tokens) || !rustTokenIdentifier(tokens[index]) {
		return -1, false
	}
	return index, true
}

func rustImplNameTokenWithin(
	tokens []rustToken,
	implIndex, bodyIndex, endIndex int,
	delimiters map[int]int,
) (int, bool) {
	if endIndex <= implIndex {
		return -1, false
	}
	headerEnd := min(endIndex+1, len(tokens))
	if bodyIndex >= 0 {
		headerEnd = bodyIndex
	} else if endIndex >= 0 && endIndex < len(tokens) && tokens[endIndex].text == ";" {
		headerEnd = endIndex
	}
	start := implIndex + 1
	if start < headerEnd && tokens[start].text == "<" {
		if closeIndex, ok := rustAngleGroupEnd(tokens, start, headerEnd); ok {
			start = closeIndex + 1
		}
	}

	separator := -1
	angleDepth := 0
	for index := start; index < headerEnd; index++ {
		if tokens[index].text == "!" {
			if _, closeIndex, ok := rustMacroInvocationBounds(tokens, index, delimiters); ok &&
				closeIndex < headerEnd {
				index = closeIndex
				continue
			}
		}
		switch tokens[index].text {
		case "(", "[":
			if closeIndex, paired := delimiters[index]; paired && closeIndex < headerEnd {
				index = closeIndex
			}
		case "<":
			angleDepth++
		case ">":
			angleDepth = max(angleDepth-1, 0)
		default:
			if angleDepth == 0 && !tokens[index].raw && tokens[index].text == "where" {
				headerEnd = index
				index = headerEnd
				continue
			}
			if angleDepth == 0 && !tokens[index].raw && tokens[index].text == "for" &&
				(index+1 >= headerEnd || tokens[index+1].text != "<") {
				separator = index
			}
		}
	}
	// A trait implementation is named after its trait; an inherent
	// implementation is named after its target type.
	nameEnd := headerEnd
	if separator >= 0 {
		nameEnd = separator
	}
	for start < nameEnd && tokens[start].text == "(" {
		closeIndex, paired := delimiters[start]
		if !paired || closeIndex != nameEnd-1 {
			break
		}
		start++
		nameEnd = closeIndex
	}

	candidate := -1
	angleDepth = 0
	for index := start; index < nameEnd; index++ {
		if tokens[index].text == "!" {
			if _, closeIndex, ok := rustMacroInvocationBounds(tokens, index, delimiters); ok &&
				closeIndex < nameEnd {
				index = closeIndex
				continue
			}
		}
		switch tokens[index].text {
		case "(", "[":
			if closeIndex, paired := delimiters[index]; paired && closeIndex < nameEnd {
				index = closeIndex
			}
		case "<":
			angleDepth++
		case ">":
			angleDepth = max(angleDepth-1, 0)
		default:
			if angleDepth == 0 && tokens[index].text == "+" && candidate >= 0 {
				return candidate, true
			}
			if tokens[index].text == "-" && index+1 < nameEnd && tokens[index+1].text == ">" {
				return candidate, candidate >= 0
			}
			if angleDepth == 0 && rustTokenIdentifier(tokens[index]) &&
				(tokens[index].raw || !rustReservedIdentifier(tokens[index].text)) &&
				tokens[index].text != "dyn" &&
				(index == start || tokens[index-1].text != "'") {
				candidate = index
			}
		}
	}
	return candidate, candidate >= 0
}

func rustImplBodyTokenWithin(
	tokens []rustToken,
	implIndex int,
	limit int,
	delimiters map[int]int,
) int {
	angleDepth := 0
	for index := implIndex + 1; index < limit; index++ {
		if tokens[index].text == "!" {
			if _, closeIndex, ok := rustMacroInvocationBounds(tokens, index, delimiters); ok &&
				closeIndex < limit {
				index = closeIndex
				continue
			}
		}
		switch tokens[index].text {
		case "(", "[":
			if closeIndex, paired := delimiters[index]; paired && closeIndex < limit {
				index = closeIndex
			}
		case "<":
			angleDepth++
		case ">":
			angleDepth = max(angleDepth-1, 0)
		case "{":
			if angleDepth == 0 {
				return index
			}
			if closeIndex, paired := delimiters[index]; paired && closeIndex < limit {
				index = closeIndex
			}
		case ";":
			return -1
		}
	}
	return -1
}

func rustAngleGroupEnd(tokens []rustToken, openIndex, limit int) (int, bool) {
	depth := 0
	for index := openIndex; index < limit; index++ {
		switch tokens[index].text {
		case "<":
			depth++
		case ">":
			depth--
			if depth == 0 {
				return index, true
			}
		}
	}
	return openIndex, false
}

func (lexer *rustLexer) itemEnd(
	keywordIndex int,
	itemBoundary bool,
	delimiters map[int]int,
) (bodyIndex, endIndex int, recovered bool) {
	key := rustItemBoundaryKey{keywordIndex: keywordIndex, itemBoundary: itemBoundary}
	if boundary, exists := lexer.itemBounds[key]; exists {
		return boundary.bodyIndex, boundary.endIndex, boundary.recovered
	}
	defer func() {
		if lexer.itemBounds == nil {
			lexer.itemBounds = make(map[rustItemBoundaryKey]rustItemBoundary)
		}
		lexer.itemBounds[key] = rustItemBoundary{
			bodyIndex: bodyIndex,
			endIndex:  endIndex,
			recovered: recovered,
		}
	}()
	if keywordIndex < 0 || keywordIndex >= len(lexer.tokens) {
		return -1, max(keywordIndex, 0), false
	}
	angleStack := make([]int, 0, 4)
	unmatchedAngles := 0
	keyword := lexer.tokens[keywordIndex].text
	semicolonTerminated := keyword == "const" || keyword == "static" || keyword == "type"
	expressionBody := -1
	malformedDelimiter := false
	deferredRecovery := -1
	itemScanBudget := max(len(lexer.tokens)*rustItemScanBudgetFactor, 1_024)
	for index := keywordIndex + 1; index < len(lexer.tokens); index++ {
		lexer.itemScanWork++
		if len(angleStack) == 0 {
			prefixStart, hardItem := rustRecoveryItemStart(
				lexer.tokens, index, malformedDelimiter, delimiters,
			)
			if hardItem && lexer.tokenStartsPhysicalLine(prefixStart) {
				lexer.recordRecoveredItemStart(prefixStart)
				return expressionBody, max(keywordIndex, prefixStart-1), true
			}
		} else if unmatchedAngles > 0 {
			prefixStart, hardItem := rustUnambiguousItemStart(
				lexer.tokens, index, delimiters,
			)
			if !lexer.tokens[index].raw && lexer.tokens[index].text == "impl" {
				prefixStart, hardItem = rustHardItemStart(lexer.tokens, index, delimiters)
			}
			if hardItem && lexer.tokenStartsPhysicalLine(prefixStart) {
				ambiguousTypeItem := false
				if !lexer.tokens[index].raw {
					switch lexer.tokens[index].text {
					case "const":
						ambiguousTypeItem = rustConstGenericParameter(
							lexer.tokens,
							index,
							angleStack[len(angleStack)-1],
							delimiters,
						)
					case "impl":
						ambiguousTypeItem = rustImplTypeContinuation(lexer.tokens, prefixStart)
					}
				}
				if !ambiguousTypeItem {
					lexer.recordRecoveredItemStart(prefixStart)
					return expressionBody, max(keywordIndex, prefixStart-1), true
				}
				if deferredRecovery < 0 {
					deferredRecovery = prefixStart
				}
			}
		}
		if unmatchedAngles > 0 && deferredRecovery >= 0 && lexer.itemScanWork > itemScanBudget {
			lexer.recordRecoveredItemStart(deferredRecovery)
			return expressionBody, max(keywordIndex, deferredRecovery-1), true
		}
		if lexer.tokens[index].text == "!" &&
			(keyword != "macro_rules" || index != keywordIndex+1) {
			if _, closeIndex, ok := rustMacroInvocationBounds(
				lexer.tokens, index, delimiters,
			); ok {
				index = closeIndex
				continue
			}
		}
		switch lexer.tokens[index].text {
		case "(", "[":
			if closeIndex, paired := delimiters[index]; paired && closeIndex > index {
				index = closeIndex
			} else {
				malformedDelimiter = true
			}
		case ")", "]":
			if _, paired := delimiters[index]; !paired {
				malformedDelimiter = true
			}
		case "<":
			angleStack = append(angleStack, index)
			if _, paired := lexer.angleDelimiters[index]; !paired {
				unmatchedAngles++
			}
		case ">":
			if openIndex, paired := lexer.angleDelimiters[index]; paired &&
				len(angleStack) > 0 && angleStack[len(angleStack)-1] == openIndex {
				angleStack = angleStack[:len(angleStack)-1]
			}
		case "{":
			if len(angleStack) > 0 {
				if closeIndex, paired := delimiters[index]; paired && closeIndex > index {
					index = closeIndex
					continue
				}
			}
			if semicolonTerminated {
				if expressionBody < 0 && (keyword == "const" || keyword == "static") {
					expressionBody = index
				}
				if closeIndex, paired := delimiters[index]; paired && closeIndex > index {
					index = closeIndex
					continue
				}
				return expressionBody, len(lexer.tokens) - 1, false
			}
			if closeIndex, paired := delimiters[index]; paired && closeIndex > index {
				return index, closeIndex, false
			}
			return index, len(lexer.tokens) - 1, false
		case "}":
			if _, paired := delimiters[index]; !paired {
				malformedDelimiter = true
			}
		case ";":
			return expressionBody, index, false
		}
	}
	if deferredRecovery >= 0 {
		lexer.recordRecoveredItemStart(deferredRecovery)
		return expressionBody, max(keywordIndex, deferredRecovery-1), true
	}
	return -1, len(lexer.tokens) - 1, false
}

func (lexer *rustLexer) recordRecoveredItemStart(prefixStart int) {
	if prefixStart < 0 || prefixStart >= len(lexer.tokens) {
		return
	}
	if lexer.recoveredItemStarts == nil {
		lexer.recoveredItemStarts = make(map[int]bool)
	}
	lexer.recoveredItemStarts[prefixStart] = true
}

func rustAttributeClose(tokens []rustToken, index int, delimiters map[int]int) (int, bool) {
	if index >= len(tokens) || tokens[index].text != "#" {
		return 0, false
	}
	openIndex := index + 1
	if openIndex < len(tokens) && tokens[openIndex].text == "!" {
		openIndex++
	}
	if openIndex >= len(tokens) || tokens[openIndex].text != "[" {
		return 0, false
	}
	closeIndex, ok := delimiters[openIndex]
	return closeIndex, ok && closeIndex > openIndex
}

func rustMacroInvocationClose(tokens []rustToken, bangIndex int, delimiters map[int]int) (int, bool) {
	_, closeIndex, ok := rustMacroInvocationBounds(tokens, bangIndex, delimiters)
	return closeIndex, ok
}

func rustMacroInvocationBounds(tokens []rustToken, bangIndex int, delimiters map[int]int) (int, int, bool) {
	if bangIndex <= 0 || bangIndex >= len(tokens) || tokens[bangIndex].text != "!" {
		return 0, 0, false
	}
	previous := tokens[bangIndex-1]
	if !rustTokenIdentifier(previous) ||
		(!previous.raw && rustReservedIdentifier(previous.text)) {
		return 0, 0, false
	}
	openIndex := bangIndex + 1
	if bangIndex > 0 && !tokens[bangIndex-1].raw && tokens[bangIndex-1].text == "macro_rules" &&
		openIndex < len(tokens) && rustTokenIdentifier(tokens[openIndex]) {
		openIndex++
	}
	if openIndex >= len(tokens) ||
		(tokens[openIndex].text != "(" && tokens[openIndex].text != "[" && tokens[openIndex].text != "{") {
		return 0, 0, false
	}
	closeIndex, ok := delimiters[openIndex]
	if !ok || closeIndex <= openIndex {
		return openIndex, len(tokens) - 1, true
	}
	return openIndex, closeIndex, true
}

func rustTokenIdentifier(token rustToken) bool {
	if token.raw {
		if !strings.HasPrefix(token.text, "r#") || len(token.text) <= 2 {
			return false
		}
		name := token.text[2:]
		end := rustIdentifierEnd(name, 0)
		return rustRawIdentifierNameAllowed(name) && end == len(name) && end > 0
	}
	end := rustIdentifierEnd(token.text, 0)
	return end == len(token.text) && end > 0
}

func rustRawIdentifierNameAllowed(name string) bool {
	switch name {
	case "", "_", "crate", "self", "Self", "super":
		return false
	default:
		return true
	}
}

type rustDefinitionIdentity struct {
	symbol string
	line   int
	column int
}

func rustDefinitionKey(definition sourceDefinition) rustDefinitionIdentity {
	return rustDefinitionIdentity{
		symbol: definition.symbol,
		line:   definition.line,
		column: definition.column,
	}
}

func (lexer *rustLexer) outerDocStart(itemStart int) int {
	cursor := itemStart
	earliestOuterDoc := -1
	index := sort.Search(len(lexer.comments), func(index int) bool {
		return lexer.comments[index].end > itemStart
	}) - 1
	for ; index >= 0; index-- {
		span := lexer.comments[index]
		if !rustOnlyWhitespace(lexer.source[span.end:cursor]) {
			break
		}
		comment := lexer.source[span.start:span.end]
		if rustInnerDocComment(comment) {
			break
		}
		if rustOuterDocComment(comment) {
			earliestOuterDoc = span.start
		}
		cursor = span.start
	}
	if earliestOuterDoc >= 0 {
		return earliestOuterDoc
	}
	return itemStart
}

func rustOnlyWhitespace(source string) bool {
	for _, r := range source {
		if !rustSpace(r) {
			return false
		}
	}
	return true
}

func rustOuterDocComment(comment string) bool {
	return strings.HasPrefix(comment, "///") && !strings.HasPrefix(comment, "////") ||
		strings.HasPrefix(comment, "/**") && !strings.HasPrefix(comment, "/***")
}

func rustInnerDocComment(comment string) bool {
	return strings.HasPrefix(comment, "//!") || strings.HasPrefix(comment, "/*!")
}

func (lexer *rustLexer) resolveImports(delimiters map[int]int) []rustLineSpan {
	imports := make([]rustLineSpan, 0)
	for index := 0; index < len(lexer.tokens); index++ {
		if closeIndex, ok := rustAttributeClose(lexer.tokens, index, delimiters); ok {
			index = closeIndex
			continue
		}
		if !lexer.tokens[index].raw &&
			(lexer.tokens[index].text == "macro_rules" || lexer.tokens[index].text == "macro") {
			if _, item := rustItemPrefixStart(lexer.tokens, index, delimiters); item {
				if bodyIndex, endIndex, _ := lexer.itemEnd(index, item, delimiters); bodyIndex >= 0 {
					index = endIndex
					continue
				}
			}
		}
		if lexer.tokens[index].text == "!" {
			if closeIndex, ok := rustMacroInvocationClose(lexer.tokens, index, delimiters); ok {
				index = closeIndex
			}
			continue
		}
		keywordIndex := index
		isImport := !lexer.tokens[index].raw && lexer.tokens[index].text == "use"
		if isImport && rustNextTokenText(lexer.tokens, index+1) == "<" {
			continue
		}
		if !isImport && !lexer.tokens[index].raw && lexer.tokens[index].text == "extern" &&
			index+1 < len(lexer.tokens) && !lexer.tokens[index+1].raw &&
			lexer.tokens[index+1].text == "crate" {
			isImport = true
		}
		if !isImport {
			continue
		}
		prefixStart, ok := rustItemPrefixStart(lexer.tokens, keywordIndex, delimiters)
		if !ok && !lexer.tokenStartsPhysicalLine(prefixStart) {
			continue
		}
		endIndex := lexer.statementEnd(keywordIndex, delimiters)
		startOffset := lexer.outerDocStart(lexer.tokens[prefixStart].start)
		startLine, _ := lexer.lineColumn(startOffset)
		endLine, _ := lexer.lineColumn(max(lexer.tokens[endIndex].end-1, 0))
		imports = append(imports, rustLineSpan{start: startLine, end: endLine})
		index = endIndex
	}
	sort.Slice(imports, func(left, right int) bool {
		if imports[left].start != imports[right].start {
			return imports[left].start < imports[right].start
		}
		return imports[left].end < imports[right].end
	})
	unique := imports[:0]
	for _, span := range imports {
		if len(unique) == 0 || unique[len(unique)-1] != span {
			unique = append(unique, span)
		}
	}
	return unique
}

func (lexer *rustLexer) statementEnd(start int, delimiters map[int]int) int {
	for index := start + 1; index < len(lexer.tokens); index++ {
		prefixStart, hardItem := rustUnambiguousItemStart(
			lexer.tokens, index, delimiters,
		)
		if hardItem && lexer.tokenStartsPhysicalLine(prefixStart) {
			return max(start, prefixStart-1)
		}
		if lexer.tokens[index].text == "(" || lexer.tokens[index].text == "[" ||
			lexer.tokens[index].text == "{" {
			if closeIndex, paired := delimiters[index]; paired && closeIndex > index {
				index = closeIndex
				continue
			}
		}
		if lexer.tokens[index].text == ")" || lexer.tokens[index].text == "]" ||
			lexer.tokens[index].text == "}" {
			return max(start, index-1)
		}
		if lexer.tokens[index].text == ";" {
			return index
		}
	}
	return len(lexer.tokens) - 1
}

// rustReservedIdentifier reports words that cannot be ordinary identifiers in
// any supported Rust edition. Edition-specific and weak keywords (including
// async, await, dyn, gen, union, raw, safe, and macro_rules) remain valid name
// candidates because the lexical fallback does not know a crate's edition.
func rustReservedIdentifier(symbol string) bool {
	switch symbol {
	case "Self", "abstract", "as", "become", "box", "break", "const", "continue",
		"crate", "do", "else", "enum", "extern", "false", "final", "fn", "for",
		"if", "impl", "in", "let", "loop", "macro", "match", "mod", "move", "mut",
		"override", "priv", "pub", "ref", "return", "self", "static", "struct",
		"super", "trait", "true", "type", "typeof", "unsafe", "unsized",
		"use", "virtual", "where", "while", "yield":
		return true
	default:
		return false
	}
}
