package repoview

import (
	"sort"
	"strings"
	"sync"
	"unicode/utf8"

	"golang.org/x/text/unicode/runenames"
)

const (
	cppMaximumRetainedTokens       = 256 << 10
	cppMaximumRawDelimiter         = 16
	cppMaximumModuleTokens         = 1024
	cppMaximumModuleNameBytes      = 16 << 10
	cppMaximumModuleAttributeDepth = 96
	cppTokenGap                    = "\x00cpp-gap"
)

// cppLexResult is the bounded lexical view used when the concrete C++ grammar
// cannot represent a construct (notably modules) or declines an adversarial
// input. Coordinates always refer to the original physical source.
type cppLexResult struct {
	commentSpans        []cByteSpan
	stringSpans         []cByteSpan
	opaqueSpans         []cByteSpan
	definitions         []sourceDefinition
	trustedDefinitions  []sourceDefinition
	fallbackDefinitions []sourceDefinition
	scopes              []cLineScope
	imports             []cLineSpan
	moduleSpans         []cByteSpan
	tokens              []cToken
	lineStarts          []int
	concreteEligible    bool
	truncated           bool
}

type cppModuleScan struct {
	definitions []sourceDefinition
	imports     []cLineSpan
	spans       []cByteSpan
}

type cppNamedUCNEntry struct {
	name      string
	character rune
}

var cppNamedUCNs struct {
	entries []cppNamedUCNEntry
	once    sync.Once
}

// C++23 accepts Unicode correction aliases (plus control/alternate aliases,
// whose characters cannot occur in identifiers). x/text exposes canonical
// names but not NameAliases.txt, so retain the small Unicode 15 correction
// set used by its Go 1.26 tables. Entries that are not XID_Continue are
// discarded when the reverse index is built.
var cppNamedUCNCorrectionAliases = [...]cppNamedUCNEntry{
	{name: "LATIN CAPITAL LETTER GHA", character: '\u01A2'},
	{name: "LATIN SMALL LETTER GHA", character: '\u01A3'},
	{name: "ARABIC SMALL HIGH LIGATURE ALEF WITH YEH BARREE", character: '\u0616'},
	{name: "SYRIAC SUBLINEAR COLON SKEWED LEFT", character: '\u0709'},
	{name: "KANNADA LETTER LLLA", character: '\u0CDE'},
	{name: "LAO LETTER FO FON", character: '\u0E9D'},
	{name: "LAO LETTER FO FAY", character: '\u0E9F'},
	{name: "LAO LETTER RO", character: '\u0EA3'},
	{name: "LAO LETTER LO", character: '\u0EA5'},
	{name: "TIBETAN MARK BKA- SHOG GI MGO RGYAN", character: '\u0FD0'},
	{name: "HANGUL JONGSEONG YESIEUNG-KIYEOK", character: '\u11EC'},
	{name: "HANGUL JONGSEONG YESIEUNG-SSANGKIYEOK", character: '\u11ED'},
	{name: "HANGUL JONGSEONG SSANGYESIEUNG", character: '\u11EE'},
	{name: "HANGUL JONGSEONG YESIEUNG-KHIEUKH", character: '\u11EF'},
	{name: "SUNDANESE LETTER ARCHAIC I", character: '\u1BBD'},
	{name: "WEIERSTRASS ELLIPTIC FUNCTION", character: '\u2118'},
	{name: "MICR ON US SYMBOL", character: '\u2448'},
	{name: "MICR DASH SYMBOL", character: '\u2449'},
	{name: "LEFTWARDS TRIANGLE-HEADED ARROW WITH DOUBLE VERTICAL STROKE", character: '\u2B7A'},
	{name: "RIGHTWARDS TRIANGLE-HEADED ARROW WITH DOUBLE VERTICAL STROKE", character: '\u2B7C'},
	{name: "YI SYLLABLE ITERATION MARK", character: '\uA015'},
	{name: "MYANMAR LETTER KHAMTI LLA", character: '\uAA6E'},
	{name: "PRESENTATION FORM FOR VERTICAL RIGHT WHITE LENTICULAR BRACKET", character: '\uFE18'},
	{name: "CUNEIFORM SIGN NU11 TENU", character: '\U000122D4'},
	{name: "CUNEIFORM SIGN NU11 OVER NU11 BUR OVER BUR", character: '\U000122D5'},
	{name: "MEDEFAIDRIN CAPITAL LETTER H", character: '\U00016E56'},
	{name: "MEDEFAIDRIN CAPITAL LETTER NG", character: '\U00016E57'},
	{name: "MEDEFAIDRIN SMALL LETTER H", character: '\U00016E76'},
	{name: "MEDEFAIDRIN SMALL LETTER NG", character: '\U00016E77'},
	{name: "HENTAIGANA LETTER E-1", character: '\U0001B001'},
	{name: "BYZANTINE MUSICAL SYMBOL FTHORA SKLIRON CHROMA VASIS", character: '\U0001D0C5'},
}

func lexCPP(source string) cppLexResult {
	rawSpans := cppRawStringSpans(source)
	// C preprocessing directives are shared by both languages. Masking raw
	// strings first prevents comment-looking payload from reaching the C
	// directive scanner while preserving every physical byte and newline.
	base := lexC(maskCSource(source, rawSpans))
	comments := normalizeCSpans(base.commentSpans)
	stringsAndHeaders := normalizeCSpans(append(
		append([]cByteSpan(nil), base.stringSpans...), rawSpans...,
	))
	directiveSpans := make([]cByteSpan, 0, len(base.directives))
	for _, directive := range base.directives {
		directiveSpans = append(directiveSpans, cByteSpan{
			start: directive.start,
			end:   directive.end,
		})
	}

	moduleScanner := newCPPModuleStreamScanner(source, base.lineStarts)
	tokens, truncated := cppCodeTokensObserved(
		source,
		comments,
		stringsAndHeaders,
		normalizeCSpans(directiveSpans),
		moduleScanner.consume,
	)
	moduleScanner.finishSource()
	modules := moduleScanner.result
	trustedDefinitions := cppDirectiveDefinitions(source, base.directives)
	trustedDefinitions = append(trustedDefinitions, modules.definitions...)
	fallbackDefinitions := cppLexicalDefinitions(source, tokens)
	fallbackDefinitions = append(
		fallbackDefinitions,
		cppCStreamingFallbackDefinitions(source, base.definitions, modules.spans)...,
	)
	definitions := append(append([]sourceDefinition(nil), trustedDefinitions...), fallbackDefinitions...)

	imports := append([]cLineSpan(nil), base.imports...)
	imports = append(imports, modules.imports...)
	scopes := append([]cLineScope(nil), base.scopes...)
	scopes = append(scopes, cppLexicalBraceScopes(source, tokens)...)

	return cppLexResult{
		commentSpans: comments,
		stringSpans:  stringsAndHeaders,
		opaqueSpans: normalizeCSpans(append(
			append([]cByteSpan(nil), comments...), stringsAndHeaders...,
		)),
		definitions:         cppSortDefinitions(definitions, len(cLineStarts(source))),
		trustedDefinitions:  cppSortDefinitions(trustedDefinitions, len(cLineStarts(source))),
		fallbackDefinitions: cppSortDefinitions(fallbackDefinitions, len(cLineStarts(source))),
		scopes:              cSortLineScopes(scopes),
		imports:             cSortLineSpans(imports),
		moduleSpans:         normalizeCSpans(modules.spans),
		tokens:              tokens,
		lineStarts:          cLineStarts(source),
		concreteEligible:    base.concreteEligible && !truncated,
		truncated:           truncated,
	}
}

// The hardened C scanner already maintains a bounded streaming declaration
// state beyond its retained token window. C++ reuses only its identifier-only,
// source-backed results. This preserves ordinary functions and declarations in
// an adversarial middle gap without importing C's language policy for classes,
// operators, templates, or modules.
func cppCStreamingFallbackDefinitions(
	source string,
	definitions []sourceDefinition,
	moduleSpans []cByteSpan,
) []sourceDefinition {
	lineStarts := cLineStarts(source)
	filtered := make([]sourceDefinition, 0, len(definitions))
	for _, definition := range definitions {
		if !cppSourceIdentifier(definition.symbol) || cppKeyword(definition.symbol) ||
			definition.line < 1 || definition.line > len(lineStarts) ||
			definition.column < 1 {
			continue
		}
		start := lineStarts[definition.line-1] + definition.column - 1
		if start < 0 || start >= len(source) {
			continue
		}
		end := cppLogicalIdentifierEnd(source, start)
		if end <= start || end > len(source) ||
			cLogicalText(source, start, end) != definition.symbol {
			continue
		}
		spanIndex := sort.Search(len(moduleSpans), func(index int) bool {
			return moduleSpans[index].end > start
		})
		if spanIndex < len(moduleSpans) && moduleSpans[spanIndex].start <= start {
			continue
		}
		filtered = append(filtered, definition)
	}
	return filtered
}

// cppRawStringSpans recognizes the standard encoding prefixes and exact raw
// delimiter match. An unterminated, otherwise-valid raw literal owns the tail;
// treating its payload as code would create worse phantom definitions/scopes.
func cppRawStringSpans(source string) []cByteSpan {
	spans := make([]cByteSpan, 0)
	for offset := 0; offset < len(source); {
		if splice := cSpliceLength(source, offset); splice > 0 {
			offset += splice
			continue
		}
		if end, _, ok := cCommentEnd(source, offset, len(source)); ok {
			offset = end
			continue
		}
		if end, ok := cppRawStringEnd(source, offset); ok {
			spans = append(spans, cByteSpan{start: offset, end: end})
			offset = end
			continue
		}
		if end, ok := cLiteralEnd(source, offset, len(source)); ok {
			offset = max(offset+1, end)
			continue
		}
		_, size := utf8.DecodeRuneInString(source[offset:])
		if size < 1 {
			size = 1
		}
		offset += size
	}
	return normalizeCSpans(spans)
}

func cppRawStringEnd(source string, start int) (int, bool) {
	prefixEnd := -1
	for _, prefix := range []string{"u8R\"", "uR\"", "UR\"", "LR\"", "R\""} {
		if strings.HasPrefix(source[start:], prefix) {
			prefixEnd = start + len(prefix)
			break
		}
	}
	if prefixEnd < 0 {
		return start, false
	}
	delimiterEnd := prefixEnd
	for delimiterEnd < len(source) && source[delimiterEnd] != '(' {
		if delimiterEnd-prefixEnd >= cppMaximumRawDelimiter ||
			!cppRawDelimiterByte(source[delimiterEnd]) {
			return start, false
		}
		delimiterEnd++
	}
	if delimiterEnd >= len(source) || source[delimiterEnd] != '(' {
		return start, false
	}
	delimiter := source[prefixEnd:delimiterEnd]
	closing := ")" + delimiter + "\""
	if relative := strings.Index(source[delimiterEnd+1:], closing); relative >= 0 {
		return delimiterEnd + 1 + relative + len(closing), true
	}
	return len(source), true
}

func cppRawDelimiterByte(character byte) bool {
	return character > 0x20 && character != '(' && character != ')' &&
		character != '\\' && character != 0x7f
}

// cppCodeTokensObserved exposes every code token to observe before the
// bounded head/tail retention policy discards it. The observer must not retain
// token text: it can be an arbitrarily large view into source.
func cppCodeTokensObserved(
	source string,
	comments, stringsAndHeaders, directives []cByteSpan,
	observe func(cToken),
) ([]cToken, bool) {
	comments = normalizeCSpans(comments)
	literals := normalizeCSpans(stringsAndHeaders)
	directives = normalizeCSpans(directives)
	headLimit := (cppMaximumRetainedTokens - 1) / 2
	tailLimit := cppMaximumRetainedTokens - headLimit - 1
	head := make([]cToken, 0, min(len(source)/3+1, headLimit))
	tail := make([]cToken, 0, min(len(source)/3+1, tailLimit))
	tailNext := 0
	totalTokens := 0
	retain := func(token cToken) {
		totalTokens++
		if len(head) < headLimit {
			head = append(head, token)
			return
		}
		if len(tail) < tailLimit {
			tail = append(tail, token)
			return
		}
		tail[tailNext] = token
		tailNext = (tailNext + 1) % tailLimit
	}
	emit := func(token cToken) {
		if observe != nil {
			observe(token)
		}
		retain(token)
	}
	commentIndex, literalIndex, directiveIndex := 0, 0, 0
	lineHasToken := false

	for offset := 0; offset < len(source); {
		for commentIndex < len(comments) && comments[commentIndex].end <= offset {
			commentIndex++
		}
		for literalIndex < len(literals) && literals[literalIndex].end <= offset {
			literalIndex++
		}
		for directiveIndex < len(directives) && directives[directiveIndex].end <= offset {
			directiveIndex++
		}
		// Keep adjacent spans separate: import/**/"header" must emit the
		// literal after skipping the comment. Directives take precedence over
		// comments and literals nested in their physical range.
		span, opaque, isLiteral := cByteSpan{}, false, false
		switch {
		case directiveIndex < len(directives) && directives[directiveIndex].start <= offset:
			span, opaque = directives[directiveIndex], true
		case commentIndex < len(comments) && comments[commentIndex].start <= offset:
			span, opaque = comments[commentIndex], true
		case literalIndex < len(literals) && literals[literalIndex].start <= offset:
			span, opaque, isLiteral = literals[literalIndex], true, true
		}
		if opaque {
			if isLiteral {
				emit(cToken{
					text: source[span.start:span.end], start: span.start, end: span.end,
					lineStart: !lineHasToken, kind: cTokenLiteral,
				})
			}
			if cContainsUnsplicedNewline(source, span.start, span.end) {
				lineHasToken = false
			} else if isLiteral {
				lineHasToken = true
			}
			offset = span.end
			continue
		}
		if splice := cSpliceLength(source, offset); splice > 0 {
			offset += splice
			continue
		}
		if offset == 0 && strings.HasPrefix(source, "\uFEFF") {
			offset += len("\uFEFF")
			continue
		}
		switch source[offset] {
		case ' ', '\t', '\v', '\f':
			offset++
			continue
		case '\r':
			lineHasToken = false
			offset++
			if offset < len(source) && source[offset] == '\n' {
				offset++
			}
			continue
		case '\n':
			lineHasToken = false
			offset++
			continue
		}

		token := cToken{start: offset, lineStart: !lineHasToken}
		if end := cppLogicalIdentifierEnd(source, offset); end > offset {
			token.end = end
			token.text = cLogicalText(source, offset, end)
			token.kind = cTokenIdentifier
		} else if cLogicalNumberStart(source, offset) {
			token.end = cppLogicalNumberEnd(source, offset)
			token.text = cLogicalText(source, offset, token.end)
			token.kind = cTokenNumber
		} else {
			token.text, token.end = cppPunctuationAt(source, offset)
			token.kind = cTokenPunctuation
		}
		if token.end <= offset {
			token.end = offset + 1
		}
		emit(token)
		offset = min(token.end, len(source))
		lineHasToken = true
	}
	truncated := totalTokens > headLimit+tailLimit
	if !truncated {
		return append(head, tail...), false
	}
	orderedTail := make([]cToken, 0, len(tail))
	orderedTail = append(orderedTail, tail[tailNext:]...)
	orderedTail = append(orderedTail, tail[:tailNext]...)
	gapStart := 0
	if len(head) > 0 {
		gapStart = head[len(head)-1].end
	}
	if len(orderedTail) > 0 {
		gapStart = min(max(gapStart, 0), orderedTail[0].start)
	}
	tokens := make([]cToken, 0, len(head)+1+len(orderedTail))
	tokens = append(tokens, head...)
	tokens = append(tokens, cToken{
		text: cppTokenGap, start: gapStart, end: gapStart,
		kind: cTokenDirective, lineStart: true,
	})
	tokens = append(tokens, orderedTail...)
	return tokens, true
}

func cppPunctuationAt(source string, start int) (string, int) {
	for _, punctuator := range []string{
		"<=>", "->*", ">>=", "<<=", "...", "::", ".*", "->",
		"++", "--", "<<", ">>", "<=", ">=", "==", "!=", "&&", "||",
		"*=", "/=", "%=", "+=", "-=", "&=", "^=", "|=", "##",
	} {
		if end, ok := cMatchLogical(source, start, punctuator, len(source)); ok {
			return punctuator, end
		}
	}
	return cPunctuationAt(source, start)
}

func cppLogicalNumberEnd(source string, start int) int {
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
		if end, ok := cppLogicalIdentifierUnit(source, unitStart, false); ok {
			physicalEnd, offset, previous = end, end, 0
			continue
		}
		break
	}
	return max(start+1, physicalEnd)
}

func cppLogicalIdentifierEnd(source string, start int) int {
	offset, physicalEnd, first := start, start, true
	for {
		unitStart := cSkipSplices(source, offset, len(source))
		if unitStart >= len(source) {
			break
		}
		end, ok := cppLogicalIdentifierUnit(source, unitStart, first)
		if !ok {
			break
		}
		physicalEnd, offset, first = end, end, false
	}
	return physicalEnd
}

func cppLogicalIdentifierUnit(source string, start int, first bool) (int, bool) {
	if character, end, ok := cppLogicalIdentifierUCN(source, start); ok {
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

const cppMaximumNamedUCNBytes = 128

// Translation phase 2 removes line splices before universal-character-names
// are recognized. Preserve the physical end while decoding the logical escape
// so a splice at any byte remains part of the same identifier token.
func cppLogicalIdentifierUCN(source string, start int) (rune, int, bool) {
	offset, ok := cMatchLogical(source, start, "\\", len(source))
	if !ok {
		return 0, start, false
	}
	offset = cSkipSplices(source, offset, len(source))
	if offset >= len(source) {
		return 0, start, false
	}
	kind := source[offset]
	if kind != 'N' && kind != 'u' && kind != 'U' {
		return 0, start, false
	}
	offset++
	offset = cSkipSplices(source, offset, len(source))

	if kind == 'N' {
		if offset >= len(source) || source[offset] != '{' {
			return 0, start, false
		}
		offset++
		var name [cppMaximumNamedUCNBytes]byte
		nameLength := 0
		for {
			offset = cSkipSplices(source, offset, len(source))
			if offset >= len(source) {
				return 0, start, false
			}
			character := source[offset]
			if character == '}' {
				if nameLength == 0 {
					return 0, start, false
				}
				value, valid := cppNamedUCNRune(string(name[:nameLength]))
				return value, offset + 1, valid
			}
			validNameByte := character == ' ' || character == '-' ||
				character >= 'A' && character <= 'Z' ||
				character >= '0' && character <= '9'
			if !validNameByte || nameLength >= len(name) {
				return 0, start, false
			}
			name[nameLength] = character
			nameLength++
			offset++
		}
	}

	braced := kind == 'u' && offset < len(source) && source[offset] == '{'
	if braced {
		offset++
	}
	digitsRequired := 4
	if kind == 'U' {
		digitsRequired = 8
	}
	value := uint64(0)
	digits := 0
	validValue := true
	for {
		offset = cSkipSplices(source, offset, len(source))
		if !braced && digits == digitsRequired {
			if !validValue || value > utf8.MaxRune ||
				value >= 0xD800 && value <= 0xDFFF {
				return 0, start, false
			}
			return rune(value), offset, true
		}
		if offset >= len(source) {
			return 0, start, false
		}
		if braced && source[offset] == '}' {
			if digits == 0 || !validValue || value > utf8.MaxRune ||
				value >= 0xD800 && value <= 0xDFFF {
				return 0, start, false
			}
			return rune(value), offset + 1, true
		}
		digit, valid := cppHexDigit(source[offset])
		if !valid {
			return 0, start, false
		}
		if value > uint64(utf8.MaxRune)>>4 {
			validValue = false
		} else {
			value = value<<4 + digit
			if value > utf8.MaxRune {
				validValue = false
			}
		}
		digits++
		offset++
	}
}

func cppHexDigit(digit byte) (uint64, bool) {
	switch {
	case digit >= '0' && digit <= '9':
		return uint64(digit - '0'), true
	case digit >= 'a' && digit <= 'f':
		return uint64(digit-'a') + 10, true
	case digit >= 'A' && digit <= 'F':
		return uint64(digit-'A') + 10, true
	default:
		return 0, false
	}
}

func cppIdentifierUCN(source string, start int) (rune, int, bool) {
	if start < 0 || start+2 > len(source) || source[start] != '\\' {
		return 0, start, false
	}
	if source[start+1] == 'N' {
		if start+3 > len(source) || source[start+2] != '{' {
			return 0, start, false
		}
		end := strings.IndexByte(source[start+3:], '}')
		if end < 1 {
			return 0, start, false
		}
		name := source[start+3 : start+3+end]
		for _, character := range name {
			if character != ' ' && character != '-' &&
				(character < 'A' || character > 'Z') &&
				(character < '0' || character > '9') {
				return 0, start, false
			}
		}
		if character, ok := cppNamedUCNRune(name); ok {
			return character, start + 3 + end + 1, true
		}
		return 0, start, false
	}
	if source[start+1] != 'u' && source[start+1] != 'U' {
		return 0, start, false
	}
	if source[start+1] == 'u' && start+3 <= len(source) && source[start+2] == '{' {
		end := strings.IndexByte(source[start+3:], '}')
		if end < 1 {
			return 0, start, false
		}
		value, valid := cppHexRune(source[start+3 : start+3+end])
		if !valid {
			return 0, start, false
		}
		return value, start + 3 + end + 1, true
	}
	digits := 4
	if source[start+1] == 'U' {
		digits = 8
	}
	end := start + 2 + digits
	if end > len(source) {
		return 0, start, false
	}
	value, valid := cppHexRune(source[start+2 : end])
	return value, end, valid
}

func cppNamedUCNRune(name string) (rune, bool) {
	if character, ok := cppAlgorithmicNamedUCNRune(name); ok {
		return character, true
	}
	if character, ok := cppUnicode17ExplicitNamedUCNRune(name); ok {
		return character, true
	}
	cppNamedUCNs.once.Do(func() {
		// cppIdentifierUCN is only used while scanning identifiers, whose callers
		// immediately enforce XID_Start/XID_Continue. Restricting the reverse
		// index to XID_Continue preserves those decisions while bounding startup
		// work to identifier characters instead of every Unicode scalar value.
		entries := make([]cppNamedUCNEntry, 0, 40_000)
		for _, characterRange := range rustXIDContinueRanges {
			for character := characterRange.first; character <= characterRange.last; character++ {
				unicodeName := runenames.Name(character)
				if unicodeName == "<Hangul Syllable>" {
					unicodeName = cppHangulSyllableName(character)
				}
				// Other angle-bracket values are UnicodeData range labels, not
				// character names. CJK and Tangut names are handled without
				// allocating every algorithmically generated name above.
				if unicodeName != "" && unicodeName[0] != '<' {
					entries = append(entries, cppNamedUCNEntry{
						name: unicodeName, character: character,
					})
				}
			}
		}
		for _, alias := range cppNamedUCNCorrectionAliases {
			if cIdentifierRune(alias.character, false) {
				entries = append(entries, alias)
			}
		}
		sort.Slice(entries, func(left, right int) bool {
			return entries[left].name < entries[right].name
		})
		cppNamedUCNs.entries = entries
	})

	index := sort.Search(len(cppNamedUCNs.entries), func(index int) bool {
		return cppNamedUCNs.entries[index].name >= name
	})
	if index >= len(cppNamedUCNs.entries) || cppNamedUCNs.entries[index].name != name {
		return 0, false
	}
	return cppNamedUCNs.entries[index].character, true
}

func cppAlgorithmicNamedUCNRune(name string) (rune, bool) {
	return cppUnicode17AlgorithmicNamedUCNRune(name)
}

func cppHangulSyllableName(character rune) string {
	const (
		hangulBase   = 0xAC00
		hangulCount  = 11172
		hangulVCount = 21
		hangulTCount = 28
	)
	if character < hangulBase || character >= hangulBase+hangulCount {
		return ""
	}
	leading := [...]string{
		"G", "GG", "N", "D", "DD", "R", "M", "B", "BB", "S",
		"SS", "NG", "J", "JJ", "C", "K", "T", "P", "H",
	}
	vowel := [...]string{
		"A", "AE", "YA", "YAE", "EO", "E", "YEO", "YE", "O", "WA",
		"WAE", "OE", "YO", "U", "WEO", "WE", "WI", "YU", "EU", "YI", "I",
	}
	trailing := [...]string{
		"", "G", "GG", "GS", "N", "NJ", "NH", "D", "L", "LG", "LM", "LB",
		"LS", "LT", "LP", "LH", "M", "B", "BS", "S", "SS", "NG", "J", "C",
		"K", "T", "P", "H",
	}
	index := int(character - hangulBase)
	return "HANGUL SYLLABLE " +
		leading[index/(hangulVCount*hangulTCount)] +
		vowel[(index%(hangulVCount*hangulTCount))/hangulTCount] +
		trailing[index%hangulTCount]
}

func cppHexRune(digits string) (rune, bool) {
	if digits == "" {
		return 0, false
	}
	value := uint64(0)
	for _, digit := range []byte(digits) {
		value <<= 4
		switch {
		case digit >= '0' && digit <= '9':
			value += uint64(digit - '0')
		case digit >= 'a' && digit <= 'f':
			value += uint64(digit-'a') + 10
		case digit >= 'A' && digit <= 'F':
			value += uint64(digit-'A') + 10
		default:
			return 0, false
		}
		if value > utf8.MaxRune {
			return 0, false
		}
	}
	return rune(value), value < 0xD800 || value > 0xDFFF
}

func cppSourceIdentifier(identifier string) bool {
	if identifier == "" {
		return false
	}
	for offset, first := 0, true; offset < len(identifier); first = false {
		if character, end, ok := cppIdentifierUCN(identifier, offset); ok {
			if !cIdentifierRune(character, first) {
				return false
			}
			offset = end
			continue
		}
		character, size := utf8.DecodeRuneInString(identifier[offset:])
		if character == utf8.RuneError && size == 1 || !cIdentifierRune(character, first) {
			return false
		}
		offset += size
	}
	return true
}

func cppTreeIdentifierSourceName(source string, start, end int) bool {
	return start >= 0 && end > start && end <= len(source) &&
		cppSourceIdentifier(source[start:end]) && !cppKeyword(source[start:end])
}

func cppDirectiveDefinitions(source string, directives []cDirective) []sourceDefinition {
	positions := cSourcePositions{source: source, lineStarts: cLineStarts(source)}
	definitions := make([]sourceDefinition, 0)
	for _, directive := range directives {
		if directive.kind != "define" {
			continue
		}
		name, nameStart, nameEnd := directive.name, directive.nameStart, directive.nameEnd
		if nameStart < 0 || nameEnd <= nameStart || nameEnd > len(source) ||
			!cppSourceIdentifier(name) {
			markerEnd, markerOK := cDirectiveMarkerEnd(source, directive.start)
			if !markerOK {
				continue
			}
			keywordStart := cDirectiveTriviaEnd(source, markerEnd, directive.end)
			keywordEnd := cLogicalIdentifierEndWithin(source, keywordStart, directive.end)
			nameStart = cDirectiveTriviaEnd(source, keywordEnd, directive.end)
			nameEnd = cppLogicalIdentifierEnd(source, nameStart)
			if nameEnd <= nameStart || nameEnd > directive.end {
				continue
			}
			name = cLogicalText(source, nameStart, nameEnd)
			if !cppSourceIdentifier(name) {
				continue
			}
		}
		line, column := positions.lineColumn(nameStart)
		definitions = append(definitions, sourceDefinition{
			symbol: name, line: line, column: column,
			scopeStart: line, scopeEnd: max(line, directive.endLine),
			ownsScope: directive.endLine > line,
		})
	}
	return definitions
}

type cppModuleOperandMode uint8

const (
	cppModuleOperandUnknown cppModuleOperandMode = iota
	cppModuleOperandNamed
	cppModuleOperandAngleHeader
	cppModuleOperandStringHeader
)

type cppModuleAttributeState uint8

const (
	cppModuleAttributeNone cppModuleAttributeState = iota
	cppModuleAttributeFirstOpen
	cppModuleAttributeBody
	cppModuleAttributeFinalClose
	cppModuleAttributeComplete
)

// cppModuleStreamScanner observes the complete token stream before bounded
// retention drops its middle. Its candidate state has fixed-size metadata and
// a bounded canonical-name builder; only reported contextual spans, accepted
// declarations, and dependencies contribute output-sized memory.
type cppModuleStreamScanner struct {
	positions      cSourcePositions
	result         cppModuleScan
	candidate      cppModuleStreamCandidate
	braceDepth     int
	statementStart bool
	exportPending  bool
	exportStart    int
}

type cppModuleStreamCandidate struct {
	name             strings.Builder
	attributeStack   [cppMaximumModuleAttributeDepth]byte
	start            int
	nameStart        int
	operandTokens    int
	namedIdentifiers int
	headerUnits      int
	attributeDepth   int
	mode             cppModuleOperandMode
	attributeState   cppModuleAttributeState
	declaration      bool
	exported         bool
	active           bool
	valid            bool
	expectIdentifier bool
	colonSeen        bool
	leadingColon     bool
	privateFragment  bool
	headerClosed     bool
	topLevel         bool
}

func newCPPModuleStreamScanner(source string, lineStarts []int) *cppModuleStreamScanner {
	return &cppModuleStreamScanner{
		positions:      cSourcePositions{source: source, lineStarts: lineStarts},
		statementStart: true,
	}
}

func (scanner *cppModuleStreamScanner) consume(token cToken) {
	if token.lineStart && (scanner.candidate.active || scanner.exportPending) {
		if scanner.candidate.active {
			scanner.finishCandidate(token.start, false)
		}
		scanner.exportPending = false
		scanner.statementStart = true
	}
	if scanner.candidate.active {
		scanner.consumeCandidate(token)
		return
	}

	if scanner.exportPending {
		scanner.exportPending = false
		if token.text == "module" || token.text == "import" {
			scanner.beginCandidate(token, scanner.exportStart, true)
			return
		}
		// The earlier export token made this an ordinary declaration. Process
		// only delimiters from the current token; it cannot start a module.
		scanner.statementStart = false
		scanner.consumeStructure(token)
		return
	}

	if scanner.statementStart {
		switch token.text {
		case "export":
			scanner.exportPending = true
			scanner.exportStart = token.start
			scanner.statementStart = false
			return
		case "module", "import":
			scanner.beginCandidate(token, token.start, false)
			return
		}
	}
	scanner.consumeStructure(token)
}

func (scanner *cppModuleStreamScanner) beginCandidate(
	keyword cToken,
	start int,
	exported bool,
) {
	scanner.candidate = cppModuleStreamCandidate{
		start:            start,
		nameStart:        -1,
		declaration:      keyword.text == "module",
		exported:         exported,
		topLevel:         scanner.braceDepth == 0,
		active:           true,
		valid:            true,
		expectIdentifier: true,
	}
	scanner.statementStart = false
}

func (scanner *cppModuleStreamScanner) consumeCandidate(token cToken) {
	candidate := &scanner.candidate
	if token.text == ";" {
		if !candidate.attributeInProgress() || candidate.attributeDepth == 0 {
			scanner.finishCandidate(token.end, !candidate.attributeInProgress())
			scanner.statementStart = true
			return
		}
	}
	// module(...) and import(...) keep their contextual-identifier meaning.
	// Once an operand token has appeared, parentheses instead make a malformed
	// module statement whose full span must suppress parser/C phantoms.
	if candidate.operandTokens == 0 && token.text == "(" {
		scanner.candidate = cppModuleStreamCandidate{}
		scanner.statementStart = false
		return
	}
	if !candidate.attributeInProgress() &&
		(token.text == "{" || token.text == "}") {
		scanner.finishCandidate(token.start, false)
		scanner.consumeStructure(token)
		return
	}

	candidate.operandTokens++
	if candidate.operandTokens > cppMaximumModuleTokens {
		candidate.valid = false
		return
	}
	candidate.consumeOperand(token)
}

func (candidate *cppModuleStreamCandidate) consumeOperand(token cToken) {
	if candidate.attributeState != cppModuleAttributeNone {
		candidate.consumeAttribute(token)
		return
	}
	if token.text == "[" && candidate.operandComplete() {
		candidate.attributeState = cppModuleAttributeFirstOpen
		return
	}
	if candidate.mode == cppModuleOperandUnknown {
		switch {
		case token.text == "<":
			candidate.mode = cppModuleOperandAngleHeader
			candidate.valid = candidate.valid && !candidate.declaration
			return
		case token.kind == cTokenLiteral:
			candidate.mode = cppModuleOperandStringHeader
			candidate.valid = candidate.valid && !candidate.declaration &&
				strings.HasPrefix(token.text, "\"")
			return
		default:
			candidate.mode = cppModuleOperandNamed
		}
	}

	switch candidate.mode {
	case cppModuleOperandUnknown:
		candidate.valid = false
	case cppModuleOperandAngleHeader:
		if candidate.headerClosed {
			candidate.valid = false
			return
		}
		if token.text == ">" {
			candidate.headerClosed = true
			return
		}
		candidate.headerUnits++
	case cppModuleOperandStringHeader:
		candidate.valid = false
	case cppModuleOperandNamed:
		candidate.consumeNamedOperand(token)
	}
}

func (candidate *cppModuleStreamCandidate) operandComplete() bool {
	switch candidate.mode {
	case cppModuleOperandNamed:
		return candidate.namedIdentifiers > 0 && !candidate.expectIdentifier
	case cppModuleOperandAngleHeader:
		return candidate.headerClosed && candidate.headerUnits > 0
	case cppModuleOperandStringHeader:
		return true
	case cppModuleOperandUnknown:
		return false
	}
	return false
}

func (candidate *cppModuleStreamCandidate) attributeInProgress() bool {
	return candidate.attributeState == cppModuleAttributeFirstOpen ||
		candidate.attributeState == cppModuleAttributeBody ||
		candidate.attributeState == cppModuleAttributeFinalClose
}

func (candidate *cppModuleStreamCandidate) attributesComplete() bool {
	return candidate.attributeState == cppModuleAttributeNone ||
		candidate.attributeState == cppModuleAttributeComplete
}

func (candidate *cppModuleStreamCandidate) consumeAttribute(token cToken) {
	switch candidate.attributeState {
	case cppModuleAttributeNone:
		candidate.valid = false
	case cppModuleAttributeFirstOpen:
		if token.text != "[" {
			candidate.valid = false
			return
		}
		candidate.attributeState = cppModuleAttributeBody
	case cppModuleAttributeBody:
		switch token.text {
		case "(", "[", "{":
			if candidate.attributeDepth >= len(candidate.attributeStack) {
				candidate.valid = false
				return
			}
			candidate.attributeStack[candidate.attributeDepth] = token.text[0]
			candidate.attributeDepth++
		case ")", "]", "}":
			if candidate.attributeDepth == 0 {
				if token.text == "]" {
					candidate.attributeState = cppModuleAttributeFinalClose
					return
				}
				candidate.valid = false
				return
			}
			open := candidate.attributeStack[candidate.attributeDepth-1]
			if !cppDelimiterTokensClose(string(open), token.text) {
				candidate.valid = false
				return
			}
			candidate.attributeDepth--
		}
	case cppModuleAttributeFinalClose:
		if token.text != "]" {
			candidate.valid = false
			return
		}
		candidate.attributeState = cppModuleAttributeComplete
	case cppModuleAttributeComplete:
		if token.text != "[" {
			candidate.valid = false
			return
		}
		candidate.attributeState = cppModuleAttributeFirstOpen
	}
}

func (candidate *cppModuleStreamCandidate) consumeNamedOperand(token cToken) {
	if candidate.expectIdentifier {
		if token.text == ":" && candidate.operandTokens == 1 && !candidate.colonSeen {
			candidate.colonSeen = true
			candidate.leadingColon = true
			candidate.appendName(token.text)
			return
		}
		specialPrivate := candidate.declaration && candidate.leadingColon &&
			candidate.namedIdentifiers == 0 && token.text == "private"
		contextualKeyword := token.text == "module" || token.text == "import"
		if token.kind != cTokenIdentifier || contextualKeyword ||
			cppKeyword(token.text) && !specialPrivate {
			candidate.valid = false
			candidate.expectIdentifier = false
			return
		}
		if candidate.nameStart < 0 {
			candidate.nameStart = token.start
		}
		candidate.namedIdentifiers++
		candidate.privateFragment = specialPrivate
		candidate.expectIdentifier = false
		candidate.appendName(token.text)
		return
	}

	switch token.text {
	case ".":
		candidate.expectIdentifier = true
		candidate.appendName(token.text)
	case ":":
		if candidate.colonSeen {
			candidate.valid = false
			return
		}
		candidate.colonSeen = true
		candidate.expectIdentifier = true
		candidate.appendName(token.text)
	default:
		candidate.valid = false
	}
}

func (candidate *cppModuleStreamCandidate) appendName(text string) {
	if !candidate.declaration || !candidate.valid {
		return
	}
	if len(text) > cppMaximumModuleNameBytes-candidate.name.Len() {
		candidate.valid = false
		return
	}
	candidate.name.WriteString(text)
}

func (scanner *cppModuleStreamScanner) finishCandidate(end int, terminated bool) {
	candidate := &scanner.candidate
	if !candidate.active {
		return
	}
	if end > candidate.start {
		scanner.result.spans = append(scanner.result.spans, cByteSpan{
			start: candidate.start,
			end:   end,
		})
	}
	valid := terminated && candidate.valid && candidate.topLevel &&
		candidate.attributesComplete()
	switch candidate.mode {
	case cppModuleOperandUnknown:
		valid = valid && candidate.declaration && !candidate.exported
	case cppModuleOperandNamed:
		valid = valid && candidate.namedIdentifiers > 0 && !candidate.expectIdentifier
		if candidate.declaration && candidate.leadingColon {
			valid = valid && candidate.privateFragment &&
				candidate.namedIdentifiers == 1 && !candidate.exported
		}
	case cppModuleOperandAngleHeader:
		valid = valid && !candidate.declaration && candidate.headerClosed &&
			candidate.headerUnits > 0
	case cppModuleOperandStringHeader:
		valid = valid && !candidate.declaration
	}

	if valid {
		startLine, _ := scanner.positions.lineColumn(candidate.start)
		endLine, _ := scanner.positions.lineColumn(max(candidate.start, end-1))
		if !candidate.declaration {
			scanner.result.imports = append(scanner.result.imports, cLineSpan{
				start: startLine,
				end:   endLine,
			})
		} else if !candidate.privateFragment && candidate.mode != cppModuleOperandUnknown {
			name := candidate.name.String()
			if name != "" && candidate.nameStart >= 0 {
				line, column := scanner.positions.lineColumn(candidate.nameStart)
				scanner.result.definitions = append(scanner.result.definitions, sourceDefinition{
					symbol: name, line: line, column: column,
					scopeStart: startLine, scopeEnd: endLine,
				})
			}
		}
	}
	scanner.candidate = cppModuleStreamCandidate{}
}

func (scanner *cppModuleStreamScanner) finishSource() {
	if scanner.candidate.active {
		scanner.finishCandidate(len(scanner.positions.source), false)
	}
}

func (scanner *cppModuleStreamScanner) consumeStructure(token cToken) {
	switch token.text {
	case "{":
		scanner.braceDepth++
		scanner.statementStart = true
	case "}":
		if scanner.braceDepth > 0 {
			scanner.braceDepth--
		}
		scanner.statementStart = true
	case ";":
		scanner.statementStart = true
	default:
		scanner.statementStart = false
	}
}

func cppLexicalDefinitions(source string, tokens []cToken) []sourceDefinition {
	positions := cSourcePositions{source: source, lineStarts: cLineStarts(source)}
	pairs := cppDelimiterPairs(tokens)
	definitions := make([]sourceDefinition, 0)
	appendName := func(nameIndex, scopeIndex int, ownsScope bool) {
		if nameIndex < 0 || nameIndex >= len(tokens) {
			return
		}
		name := tokens[nameIndex]
		if name.kind != cTokenIdentifier || cppKeyword(name.text) {
			return
		}
		line, column := positions.lineColumn(name.start)
		scopeStart, scopeEnd := line, line
		if scopeIndex >= 0 && scopeIndex < len(tokens) {
			closing := pairs[scopeIndex]
			if closing > scopeIndex {
				scopeStart, _ = positions.lineColumn(tokens[scopeIndex].start)
				scopeEnd, _ = positions.lineColumn(max(tokens[scopeIndex].start, tokens[closing].end-1))
			}
		}
		definitions = append(definitions, sourceDefinition{
			symbol: name.text, line: line, column: column,
			scopeStart: min(line, scopeStart), scopeEnd: max(line, scopeEnd),
			ownsScope: ownsScope && scopeEnd >= scopeStart,
		})
	}

	for index := range tokens {
		token := tokens[index]
		switch token.text {
		case "namespace":
			name := cppNextIdentifierToken(tokens, index+1, min(len(tokens), index+32))
			brace := cppNextTokenBefore(tokens, index+1, "{", ";", 128)
			if name >= 0 && brace >= 0 {
				appendName(name, brace, true)
			}
		case "class", "struct", "union", "enum":
			if token.text == "class" && index > 0 &&
				(tokens[index-1].text == "<" || tokens[index-1].text == ",") {
				continue
			}
			name := cppNextIdentifierToken(tokens, index+1, min(len(tokens), index+32))
			if name < 0 || cppKeyword(tokens[name].text) {
				continue
			}
			brace := cppNextTokenBefore(tokens, name+1, "{", ";", 256)
			appendName(name, brace, brace >= 0)
		case "concept":
			name := cppNextIdentifierToken(tokens, index+1, min(len(tokens), index+8))
			appendName(name, -1, false)
		case "using":
			name := cppNextIdentifierToken(tokens, index+1, min(len(tokens), index+8))
			if name >= 0 && name+1 < len(tokens) && tokens[name+1].text == "=" {
				appendName(name, -1, false)
			}
		}
	}

	for open, close := range pairs {
		if open < 1 || close <= open || tokens[open].text != "(" {
			continue
		}
		nameIndex, symbol := cppCallableBefore(source, tokens, open)
		if nameIndex < 0 || symbol == "" || cppControlKeyword(symbol) {
			continue
		}
		if cppCallableInsideParentheses(tokens, open, pairs) {
			continue
		}
		terminator := cppCallableTerminator(tokens, close+1, pairs)
		if terminator < 0 {
			continue
		}
		ownsScope := tokens[terminator].text == "{" || tokens[terminator].text == "try"
		if !ownsScope && !cppDeclarationEvidence(tokens, nameIndex) &&
			!cppConversionOperatorSymbol(symbol) &&
			!cppTokenRangeContains(tokens, close+1, terminator, "->") {
			continue
		}
		if ownsScope && tokens[terminator].text == "try" {
			terminator = cppNextTokenBefore(tokens, terminator+1, "{", ";", 256)
			ownsScope = terminator >= 0
		}
		line, column := positions.lineColumn(tokens[nameIndex].start)
		scopeStart, scopeEnd := line, line
		if ownsScope {
			closing := pairs[terminator]
			if closing <= terminator {
				// definitionSymbol is intentionally useful on an incomplete
				// signature line. Keep the declaration coordinate, but never
				// manufacture an owning scope without its closing brace.
				ownsScope = false
			} else {
				scopeStart, _ = positions.lineColumn(tokens[terminator].start)
				scopeEnd, _ = positions.lineColumn(max(tokens[terminator].start, tokens[closing].end-1))
			}
		}
		definitions = append(definitions, sourceDefinition{
			symbol: symbol, line: line, column: column,
			scopeStart: min(line, scopeStart), scopeEnd: max(line, scopeEnd),
			ownsScope: ownsScope,
		})
	}
	return definitions
}

func cppConversionOperatorSymbol(symbol string) bool {
	if !strings.HasPrefix(symbol, "operator") {
		return false
	}
	operand := strings.TrimSpace(strings.TrimPrefix(symbol, "operator"))
	if operand == "" || operand == "new" || operand == "delete" || operand == "co_await" {
		return false
	}
	return cppLogicalIdentifierEnd(operand, 0) > 0
}

func cppTokenRangeContains(tokens []cToken, start, end int, want string) bool {
	start = max(0, start)
	end = min(end, len(tokens))
	for index := start; index < end; index++ {
		if tokens[index].text == cppTokenGap {
			return false
		}
		if tokens[index].text == want {
			return true
		}
	}
	return false
}

func cppDelimiterPairs(tokens []cToken) map[int]int {
	pairs := make(map[int]int)
	stack := make([]int, 0, 64)
	for index, token := range tokens {
		switch token.text {
		case "(", "[", "{":
			stack = append(stack, index)
		case ")", "]", "}":
			if len(stack) == 0 || !cppDelimiterTokensClose(tokens[stack[len(stack)-1]].text, token.text) {
				continue
			}
			open := stack[len(stack)-1]
			stack = stack[:len(stack)-1]
			pairs[open], pairs[index] = index, open
		case cppTokenGap:
			stack = stack[:0]
		}
	}
	return pairs
}

func cppDelimiterTokensClose(open, closing string) bool {
	return open == "(" && closing == ")" || open == "[" && closing == "]" ||
		open == "{" && closing == "}"
}

func cppNextIdentifierToken(tokens []cToken, start, end int) int {
	end = min(end, len(tokens))
	for index := max(start, 0); index < end; index++ {
		if tokens[index].text == cppTokenGap {
			break
		}
		if tokens[index].kind == cTokenIdentifier && !cppKeyword(tokens[index].text) {
			return index
		}
		if tokens[index].text == ";" || tokens[index].text == "{" {
			break
		}
	}
	return -1
}

func cppNextTokenBefore(tokens []cToken, start int, want, stop string, budget int) int {
	for index, end := start, min(len(tokens), start+budget); index < end; index++ {
		if tokens[index].text == cppTokenGap {
			return -1
		}
		if tokens[index].text == want {
			return index
		}
		if tokens[index].text == stop || tokens[index].text == "}" {
			return -1
		}
	}
	return -1
}

func cppCallableBefore(source string, tokens []cToken, open int) (int, string) {
	if open <= 0 {
		return -1, ""
	}
	// operator(), operator[], operator punctuation and conversion operators.
	for start := max(0, open-12); start < open; start++ {
		if tokens[start].text != "operator" {
			continue
		}
		if start > 0 && (tokens[start-1].text == "." || tokens[start-1].text == "->") ||
			!cppOperatorIDBefore(tokens, start, open) {
			continue
		}
		symbolStart := tokens[start].start
		symbolEnd := tokens[open-1].end
		if symbolStart < 0 || symbolEnd <= symbolStart || symbolEnd > len(source) ||
			strings.ContainsAny(source[symbolStart:symbolEnd], "\r\n") {
			continue
		}
		return start, strings.TrimRight(source[symbolStart:symbolEnd], " \t\v\f")
	}
	index := open - 1
	if tokens[index].kind == cTokenIdentifier && !cppKeyword(tokens[index].text) {
		if index > 0 && (tokens[index-1].text == "." || tokens[index-1].text == "->") {
			return -1, ""
		}
		if index > 0 && tokens[index-1].text == "~" {
			start := tokens[index-1].start
			end := tokens[index].end
			if start >= 0 && end > start && end <= len(source) {
				return index - 1, source[start:end]
			}
			return -1, ""
		}
		return index, tokens[index].text
	}
	return -1, ""
}

func cppOperatorIDBefore(tokens []cToken, operatorIndex, parameterOpen int) bool {
	if operatorIndex < 0 || operatorIndex+1 >= parameterOpen ||
		parameterOpen > len(tokens) {
		return false
	}
	parts := tokens[operatorIndex+1 : parameterOpen]
	if parts[len(parts)-1].text == "decltype" {
		return false
	}
	if len(parts) == 1 {
		switch parts[0].text {
		case "new", "delete", "co_await", "<=>", "->*", ">>=", "<<=", "->",
			"++", "--", "<<", ">>", "<=", ">=", "==", "!=", "&&", "||",
			"+=", "-=", "*=", "/=", "%=", "^=", "&=", "|=", "+", "-", "*",
			"/", "%", "^", "&", "|", "~", "!", "=", "<", ">", ",":
			return true
		}
	}
	if len(parts) == 2 && (parts[0].text == "(" && parts[1].text == ")" ||
		parts[0].text == "[" && parts[1].text == "]") {
		return true
	}
	if len(parts) == 3 && (parts[0].text == "new" || parts[0].text == "delete") &&
		parts[1].text == "[" && parts[2].text == "]" {
		return true
	}
	if parts[0].kind == cTokenLiteral && strings.HasPrefix(parts[0].text, `""`) {
		return len(parts) == 2 && parts[1].kind == cTokenIdentifier
	}

	// A conversion-type-id may contain qualified/template names and pointer or
	// reference operators, but never a completed parameter list, statement, or
	// function body. Requiring an identifier prevents punctuation operators
	// from falling through this path.
	hasIdentifier := false
	angleDepth := 0
	decltypeDepth := 0
	for index, part := range parts {
		if decltypeDepth > 0 {
			switch part.text {
			case "(":
				decltypeDepth++
			case ")":
				decltypeDepth--
			}
			continue
		}
		if part.kind == cTokenIdentifier {
			hasIdentifier = true
			continue
		}
		switch part.text {
		case "::", "*", "&", "&&", ",":
		case "(":
			if index == 0 || parts[index-1].text != "decltype" {
				return false
			}
			decltypeDepth = 1
		case "<":
			angleDepth++
		case ">":
			angleDepth = max(0, angleDepth-1)
		case ">>":
			angleDepth = max(0, angleDepth-2)
		default:
			return false
		}
	}
	return hasIdentifier && angleDepth == 0 && decltypeDepth == 0
}

func cppCallableInsideParentheses(
	tokens []cToken,
	callOpen int,
	pairs map[int]int,
) bool {
	for enclosing := callOpen - 1; enclosing >= 0 && enclosing >= callOpen-128; enclosing-- {
		if tokens[enclosing].text == cppTokenGap {
			break
		}
		if tokens[enclosing].text != "(" || pairs[enclosing] <= callOpen {
			continue
		}
		return true
	}
	return false
}

func cppCallableTerminator(tokens []cToken, start int, pairs map[int]int) int {
	for index, budget := start, 0; index < len(tokens) && budget < 512; index, budget = index+1, budget+1 {
		switch tokens[index].text {
		case cppTokenGap:
			return -1
		case "{", ";", "try":
			return index
		case "( ":
			// unreachable canonical token; retained for defensive source variants.
		case "(", "[":
			if closing := pairs[index]; closing > index {
				index = closing
			}
		case "}":
			return -1
		}
	}
	return -1
}

func cppDeclarationEvidence(tokens []cToken, name int) bool {
	if name < 1 {
		return false
	}
	evidence := false
	for index, budget := name-1, 0; index >= 0 && budget < 64; index, budget = index-1, budget+1 {
		if tokens[index].text == cppTokenGap || tokens[index].text == ";" ||
			tokens[index].text == "{" || tokens[index].text == "}" {
			break
		}
		if cppDeclarationEvidenceBarrier(tokens[index].text) {
			return false
		}
		if tokens[index].kind == cTokenIdentifier &&
			(!cppKeyword(tokens[index].text) || cppTypeKeyword(tokens[index].text)) {
			evidence = true
		}
	}
	return evidence
}

func cppDeclarationEvidenceBarrier(text string) bool {
	switch text {
	case "=", "+=", "-=", "*=", "/=", "%=", "^=", "&=", "|=",
		"<<=", ">>=", "?", "return", "co_return", "co_yield", "throw":
		return true
	default:
		return false
	}
}

func cppLexicalBraceScopes(source string, tokens []cToken) []cLineScope {
	positions := cSourcePositions{source: source, lineStarts: cLineStarts(source)}
	pairs := cppDelimiterPairs(tokens)
	scopes := make([]cLineScope, 0)
	for open, close := range pairs {
		if tokens[open].text != "{" || close <= open {
			continue
		}
		start, _ := positions.lineColumn(tokens[open].start)
		end, _ := positions.lineColumn(max(tokens[open].start, tokens[close].end-1))
		if start > 0 && end >= start {
			scopes = append(scopes, cLineScope{start: start, end: end})
		}
	}
	return scopes
}

func cppSortDefinitions(definitions []sourceDefinition, lineCount int) []sourceDefinition {
	sort.SliceStable(definitions, func(first, second int) bool {
		if definitions[first].line != definitions[second].line {
			return definitions[first].line < definitions[second].line
		}
		if definitions[first].column != definitions[second].column {
			return definitions[first].column < definitions[second].column
		}
		return definitions[first].symbol < definitions[second].symbol
	})
	seen := make(map[cDefinitionIdentity]bool, len(definitions))
	unique := definitions[:0]
	for _, definition := range definitions {
		definition = normalizeCDefinition(definition, lineCount)
		if definition.symbol == "" || !cppNavigationSymbol(definition.symbol) {
			continue
		}
		key := cDefinitionKey(definition)
		if seen[key] {
			continue
		}
		seen[key] = true
		unique = append(unique, definition)
	}
	return unique
}

func cppNavigationSymbol(symbol string) bool {
	if cppSourceIdentifier(symbol) {
		// A preprocessing macro name may have the same spelling as a C++
		// keyword. Declaration extractors reject keywords in their grammar
		// context; navigation must still be able to expose and find the trusted
		// directive definition.
		return true
	}
	if strings.HasPrefix(symbol, "~") {
		return cppTreeDestructorNameEnd(symbol, 0, len(symbol)) == len(symbol)
	}
	if strings.HasPrefix(symbol, "operator") && len(symbol) > len("operator") {
		return true
	}
	parts := strings.FieldsFunc(symbol, func(character rune) bool {
		return character == '.' || character == ':'
	})
	if len(parts) == 0 {
		return false
	}
	for _, part := range parts {
		if !cppSourceIdentifier(part) {
			return false
		}
	}
	return true
}

func cppKeyword(identifier string) bool {
	_, exists := cppKeywords[identifier]
	return exists
}

func cppControlKeyword(identifier string) bool {
	switch identifier {
	case "if", "for", "while", "switch", "catch", "sizeof", "alignof",
		"decltype", "requires", "static_assert", "return", "new", "delete":
		return true
	default:
		return false
	}
}

func cppTypeKeyword(identifier string) bool {
	switch identifier {
	case "auto", "bool", "char", "char8_t", "char16_t", "char32_t", "double",
		"float", "int", "long", "short", "signed", "unsigned", "void", "wchar_t",
		"const", "volatile", "constexpr", "consteval", "static", "extern", "inline":
		return true
	default:
		return false
	}
}

var cppKeywords = map[string]struct{}{
	"alignas": {}, "alignof": {}, "and": {}, "and_eq": {}, "asm": {}, "auto": {},
	"bitand": {}, "bitor": {}, "bool": {}, "break": {}, "case": {}, "catch": {},
	"char": {}, "char8_t": {}, "char16_t": {}, "char32_t": {}, "class": {},
	"compl": {}, "concept": {}, "const": {}, "consteval": {}, "constexpr": {},
	"constinit": {}, "const_cast": {}, "continue": {}, "co_await": {},
	"co_return": {}, "co_yield": {}, "decltype": {}, "default": {}, "delete": {},
	"do": {}, "double": {}, "dynamic_cast": {}, "else": {}, "enum": {},
	"explicit": {}, "export": {}, "extern": {}, "false": {}, "float": {},
	"for": {}, "friend": {}, "goto": {}, "if": {}, "inline": {}, "int": {},
	"long": {}, "mutable": {}, "namespace": {}, "new": {},
	"noexcept": {}, "not": {}, "not_eq": {}, "nullptr": {}, "operator": {},
	"or": {}, "or_eq": {}, "private": {}, "protected": {}, "public": {},
	"reflexpr": {}, "register": {}, "reinterpret_cast": {}, "requires": {},
	"return": {}, "short": {}, "signed": {}, "sizeof": {}, "static": {},
	"static_assert": {}, "static_cast": {}, "struct": {}, "switch": {},
	"synchronized": {}, "template": {}, "this": {}, "thread_local": {},
	"throw": {}, "true": {}, "try": {}, "typedef": {}, "typeid": {},
	"typename": {}, "union": {}, "unsigned": {}, "using": {}, "virtual": {},
	"void": {}, "volatile": {}, "wchar_t": {}, "while": {}, "xor": {},
	"xor_eq": {},
}
