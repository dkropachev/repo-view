package navigator

import (
	"sort"
	"strings"
	"unicode/utf8"
)

type pythonByteSpan struct {
	start int
	end   int
}

type pythonLineSpan struct {
	start int
	end   int
}

type pythonLineScope struct {
	start int
	end   int
}

type pythonLexResult struct {
	commentSpans       []pythonByteSpan
	stringSpans        []pythonByteSpan
	literalStringSpans []pythonByteSpan
	docstringSpans     []pythonByteSpan
	definitions        []sourceDefinition
	scopes             []pythonLineScope
	imports            []pythonLineSpan
}

type pythonStringLiteral struct {
	start, end int
	formatted  bool
	bytes      bool
	topLevel   bool
}

type pythonStatement struct {
	start, end int
	codeStart  int
	startLine  int
	endLine    int
	indent     int
}

type pythonHeader struct {
	kind       string
	statement  int
	startLine  int
	headerLine int
	endLine    int
	indent     int
	colon      int
	inline     bool
	definition int
}

type pythonLexer struct {
	topLiterals map[int]pythonStringLiteral
	topComments map[int]int

	source      string
	lineStarts  []int
	comments    []pythonByteSpan
	maskStrings []pythonByteSpan
	literals    []pythonStringLiteral
}

const pythonMaximumFormattedNesting = 64

var pythonHardKeywords = map[string]bool{
	"False": true, "None": true, "True": true,
	"and": true, "as": true, "assert": true, "async": true,
	"await": true, "break": true, "class": true, "continue": true,
	"def": true, "del": true, "elif": true, "else": true,
	"except": true, "finally": true, "for": true, "from": true,
	"global": true, "if": true, "import": true, "in": true,
	"is": true, "lambda": true, "nonlocal": true, "not": true,
	"or": true, "pass": true, "raise": true, "return": true,
	"try": true, "while": true, "with": true, "yield": true,
}

func lexPython(source string) pythonLexResult {
	l := &pythonLexer{source: source, lineStarts: pythonLineStarts(source)}
	l.scanTopLevel()
	l.initializeTopLevelLookups()

	statements := l.statements()
	definitions, headers := l.definitionsAndHeaders(statements)
	scopes := l.resolveScopes(statements, headers, definitions)
	imports := l.resolveImports(statements)
	docstrings := l.resolveDocstrings(statements, headers)

	literalSpans := make([]pythonByteSpan, 0, len(l.literals))
	for _, literal := range l.literals {
		literalSpans = append(literalSpans, pythonByteSpan{literal.start, literal.end})
	}

	return pythonLexResult{
		commentSpans:       normalizePythonSpans(l.comments),
		stringSpans:        normalizePythonSpans(l.maskStrings),
		literalStringSpans: normalizePythonSpans(literalSpans),
		docstringSpans:     normalizePythonSpans(docstrings),
		definitions:        definitions,
		scopes:             scopes,
		imports:            imports,
	}
}

func maskPythonSource(source string, noComments, noStrings bool) string {
	if (!noComments && !noStrings) || source == "" {
		return source
	}
	result := lexPython(source)
	spans := make([]pythonByteSpan, 0, len(result.commentSpans)+len(result.stringSpans))
	if noComments {
		spans = append(spans, result.commentSpans...)
	}
	if noStrings {
		spans = append(spans, result.stringSpans...)
	}
	masked := []byte(source)
	for _, span := range normalizePythonSpans(spans) {
		for idx := span.start; idx < span.end && idx < len(masked); idx++ {
			if masked[idx] != '\n' && masked[idx] != '\r' {
				masked[idx] = ' '
			}
		}
	}
	return string(masked)
}

func pythonLineStarts(source string) []int {
	starts := []int{0}
	for idx := 0; idx < len(source); {
		switch source[idx] {
		case '\r':
			idx++
			if idx < len(source) && source[idx] == '\n' {
				idx++
			}
			starts = append(starts, idx)
		case '\n':
			idx++
			starts = append(starts, idx)
		default:
			_, size := utf8.DecodeRuneInString(source[idx:])
			if size < 1 {
				size = 1
			}
			idx += size
		}
	}
	return starts
}

func (l *pythonLexer) lineColumn(offset int) (int, int) {
	if offset < 0 {
		offset = 0
	}
	if offset > len(l.source) {
		offset = len(l.source)
	}
	lineIndex := sort.Search(len(l.lineStarts), func(idx int) bool {
		return l.lineStarts[idx] > offset
	}) - 1
	if lineIndex < 0 {
		lineIndex = 0
	}
	return lineIndex + 1, offset - l.lineStarts[lineIndex] + 1
}

func pythonDecode(source string, offset int) (rune, int) {
	if offset >= len(source) {
		return utf8.RuneError, 0
	}
	r, size := utf8.DecodeRuneInString(source[offset:])
	if size < 1 {
		size = 1
	}
	return r, size
}

func pythonIdentifierStart(r rune) bool {
	return pythonRuneInXIDRanges(r, pythonXIDStartRanges)
}

func pythonIdentifierContinue(r rune) bool {
	return pythonRuneInXIDRanges(r, pythonXIDContinueRanges)
}

func pythonIdentifierEnd(source string, start int) int {
	r, size := pythonDecode(source, start)
	if size == 0 || !pythonIdentifierStart(r) {
		return start
	}
	end := start + size
	for end < len(source) {
		r, size = pythonDecode(source, end)
		if size == 0 || !pythonIdentifierContinue(r) {
			break
		}
		end += size
	}
	return end
}

func pythonIsHorizontalSpace(ch byte) bool {
	return ch == ' ' || ch == '\t' || ch == '\f' || ch == '\v'
}

func pythonNewlineEnd(source string, start int) int {
	if start >= len(source) {
		return start
	}
	if source[start] == '\r' && start+1 < len(source) && source[start+1] == '\n' {
		return start + 2
	}
	if source[start] == '\r' || source[start] == '\n' {
		return start + 1
	}
	return start
}

func pythonPrefix(source string, start int) (int, bool, bool, bool) {
	if start >= len(source) {
		return start, false, false, false
	}
	if source[start] == '\'' || source[start] == '"' {
		return start, false, false, true
	}
	end := start
	for end < len(source) && end-start < 2 {
		ch := source[end]
		if (ch < 'A' || ch > 'Z') && (ch < 'a' || ch > 'z') {
			break
		}
		end++
	}
	if end == start || end >= len(source) || (source[end] != '\'' && source[end] != '"') {
		return start, false, false, false
	}
	prefix := strings.ToLower(source[start:end])
	switch prefix {
	case "r", "u", "b", "f", "t", "br", "rb", "fr", "rf", "tr", "rt":
	default:
		return start, false, false, false
	}
	formatted := strings.ContainsAny(prefix, "ft")
	bytesLiteral := strings.Contains(prefix, "b")
	return end, formatted, bytesLiteral, true
}

func (l *pythonLexer) scanTopLevel() {
	for offset := 0; offset < len(l.source); {
		ch := l.source[offset]
		if pythonIsHorizontalSpace(ch) {
			offset++
			continue
		}
		if newlineEnd := pythonNewlineEnd(l.source, offset); newlineEnd > offset {
			offset = newlineEnd
			continue
		}
		if ch == '#' {
			end := pythonCommentEnd(l.source, offset)
			l.comments = append(l.comments, pythonByteSpan{offset, end})
			offset = end
			continue
		}
		if quote, _, _, ok := pythonPrefix(l.source, offset); ok &&
			(quote == offset || offset == 0 || !pythonPreviousIdentifier(l.source, offset)) {
			end := l.scanLiteral(offset, quote, true, 0)
			if end <= offset {
				end = offset + 1
			}
			offset = end
			continue
		}
		r, size := pythonDecode(l.source, offset)
		if pythonIdentifierStart(r) {
			offset = pythonIdentifierEnd(l.source, offset)
			continue
		}
		if ch >= '0' && ch <= '9' {
			offset = pythonNumberEnd(l.source, offset)
			continue
		}
		if size < 1 {
			size = 1
		}
		offset += size
	}
}

func pythonPreviousIdentifier(source string, offset int) bool {
	if offset <= 0 {
		return false
	}
	r, _ := utf8.DecodeLastRuneInString(source[:offset])
	return pythonIdentifierContinue(r)
}

func pythonCommentEnd(source string, start int) int {
	end := start
	for end < len(source) && source[end] != '\n' && source[end] != '\r' {
		_, size := pythonDecode(source, end)
		if size < 1 {
			size = 1
		}
		end += size
	}
	return end
}

func pythonNumberEnd(source string, start int) int {
	end := start
	for end < len(source) {
		ch := source[end]
		if (ch >= '0' && ch <= '9') || (ch >= 'A' && ch <= 'Z') ||
			(ch >= 'a' && ch <= 'z') || ch == '_' || ch == '.' {
			end++
			continue
		}
		if (ch == '+' || ch == '-') && end > start &&
			(source[end-1] == 'e' || source[end-1] == 'E' || source[end-1] == 'p' || source[end-1] == 'P') {
			end++
			continue
		}
		break
	}
	return end
}

func (l *pythonLexer) scanLiteral(start, quote int, topLevel bool, depth int) int {
	_, formatted, bytesLiteral, ok := pythonPrefix(l.source, start)
	if !ok || quote >= len(l.source) {
		return start
	}
	quoteChar := l.source[quote]
	delimiterWidth := 1
	if quote+2 < len(l.source) && l.source[quote+1] == quoteChar &&
		l.source[quote+2] == quoteChar {
		delimiterWidth = 3
	}
	content := quote + delimiterWidth
	var end int
	var preserved []pythonByteSpan
	preserveExpressions := formatted && depth <= pythonMaximumFormattedNesting
	if preserveExpressions {
		end, preserved = l.scanFormattedContent(content, quoteChar, delimiterWidth, depth)
	} else {
		end = pythonPlainStringEnd(l.source, content, quoteChar, delimiterWidth)
	}
	if end < content {
		end = content
	}
	l.literals = append(l.literals, pythonStringLiteral{
		start: start, end: end, formatted: formatted, bytes: bytesLiteral,
		topLevel: topLevel,
	})
	if preserveExpressions {
		l.maskStrings = append(l.maskStrings, pythonSpanComplement(start, end, preserved)...)
	} else {
		l.maskStrings = append(l.maskStrings, pythonByteSpan{start, end})
	}
	return end
}

func pythonPlainStringEnd(source string, content int, quote byte, width int) int {
	for offset := content; offset < len(source); {
		if source[offset] == '\\' {
			offset++
			if offset >= len(source) {
				return offset
			}
			if newlineEnd := pythonNewlineEnd(source, offset); newlineEnd > offset {
				offset = newlineEnd
				continue
			}
			_, size := pythonDecode(source, offset)
			if size < 1 {
				size = 1
			}
			offset += size
			continue
		}
		if width == 3 {
			if offset+2 < len(source) && source[offset] == quote &&
				source[offset+1] == quote && source[offset+2] == quote {
				return offset + 3
			}
		} else {
			if source[offset] == quote {
				return offset + 1
			}
			if source[offset] == '\r' || source[offset] == '\n' {
				return offset
			}
		}
		_, size := pythonDecode(source, offset)
		if size < 1 {
			size = 1
		}
		offset += size
	}
	return len(source)
}

func (l *pythonLexer) scanFormattedContent(
	content int,
	quote byte,
	width, depth int,
) (int, []pythonByteSpan) {
	offset := content
	var preserved []pythonByteSpan
	for offset < len(l.source) {
		if width == 3 {
			if offset+2 < len(l.source) && l.source[offset] == quote &&
				l.source[offset+1] == quote && l.source[offset+2] == quote {
				return offset + 3, preserved
			}
		} else {
			if l.source[offset] == quote {
				return offset + 1, preserved
			}
			if l.source[offset] == '\r' || l.source[offset] == '\n' {
				return offset, preserved
			}
		}
		if l.source[offset] == '\\' {
			if offset+1 < len(l.source) &&
				(l.source[offset+1] == '{' || l.source[offset+1] == '}') {
				offset++
				continue
			}
			offset++
			if offset >= len(l.source) {
				return offset, preserved
			}
			if newlineEnd := pythonNewlineEnd(l.source, offset); newlineEnd > offset {
				offset = newlineEnd
				continue
			}
			_, size := pythonDecode(l.source, offset)
			if size < 1 {
				size = 1
			}
			offset += size
			continue
		}
		if l.source[offset] == '{' {
			if offset+1 < len(l.source) && l.source[offset+1] == '{' {
				offset += 2
				continue
			}
			var next int
			var fields []pythonByteSpan
			next, fields = l.scanReplacementField(offset+1, quote, width, depth+1)
			preserved = append(preserved, fields...)
			if next <= offset {
				next = offset + 1
			}
			offset = next
			continue
		}
		if l.source[offset] == '}' && offset+1 < len(l.source) &&
			l.source[offset+1] == '}' {
			offset += 2
			continue
		}
		_, size := pythonDecode(l.source, offset)
		if size < 1 {
			size = 1
		}
		offset += size
	}
	return len(l.source), preserved
}

func (l *pythonLexer) scanReplacementField(
	start int,
	outerQuote byte,
	outerWidth, depth int,
) (int, []pythonByteSpan) {
	if depth > pythonMaximumFormattedNesting {
		return pythonFormattedNestingRecovery(l.source, start, outerWidth), nil
	}
	offset := start
	segmentStart := start
	stack := make([]byte, 0, 4)
	var preserved []pythonByteSpan
	for offset < len(l.source) {
		if l.source[offset] == '#' {
			end := pythonCommentEnd(l.source, offset)
			l.comments = append(l.comments, pythonByteSpan{offset, end})
			offset = end
			continue
		}
		if quote, _, _, ok := pythonPrefix(l.source, offset); ok &&
			(quote == offset || offset == segmentStart || !pythonPreviousIdentifier(l.source, offset)) {
			end := l.scanLiteral(offset, quote, false, depth+1)
			if end <= offset {
				end = offset + 1
			}
			offset = end
			continue
		}
		ch := l.source[offset]
		switch ch {
		case '(', '[', '{':
			stack = append(stack, ch)
			offset++
			continue
		case ')', ']':
			if len(stack) > 0 && pythonDelimitersMatch(stack[len(stack)-1], ch) {
				stack = stack[:len(stack)-1]
			}
			offset++
			continue
		case '}':
			if len(stack) > 0 {
				if stack[len(stack)-1] == '{' {
					stack = stack[:len(stack)-1]
				}
				offset++
				continue
			}
			if segmentStart < offset {
				preserved = append(preserved, pythonByteSpan{segmentStart, offset})
			}
			return offset + 1, preserved
		case '!':
			if len(stack) == 0 && (offset+1 >= len(l.source) || l.source[offset+1] != '=') {
				if segmentStart < offset {
					preserved = append(preserved, pythonByteSpan{segmentStart, offset})
				}
				offset++
				if offset < len(l.source) &&
					(l.source[offset] == 's' || l.source[offset] == 'r' || l.source[offset] == 'a') {
					offset++
				}
				if offset < len(l.source) && l.source[offset] == ':' {
					return l.scanFormatSpec(offset+1, outerQuote, outerWidth, depth, preserved)
				}
				for offset < len(l.source) && pythonIsHorizontalSpace(l.source[offset]) {
					offset++
				}
				if offset < len(l.source) && l.source[offset] == '}' {
					return offset + 1, preserved
				}
				return offset, preserved
			}
		case ':':
			if len(stack) == 0 && (offset+1 >= len(l.source) || l.source[offset+1] != '=') {
				if segmentStart < offset {
					preserved = append(preserved, pythonByteSpan{segmentStart, offset})
				}
				return l.scanFormatSpec(offset+1, outerQuote, outerWidth, depth, preserved)
			}
		}
		if outerWidth == 1 && (ch == '\r' || ch == '\n') {
			if segmentStart < offset {
				preserved = append(preserved, pythonByteSpan{segmentStart, offset})
			}
			return offset, preserved
		}
		_, size := pythonDecode(l.source, offset)
		if size < 1 {
			size = 1
		}
		offset += size
	}
	if segmentStart < offset {
		preserved = append(preserved, pythonByteSpan{segmentStart, offset})
	}
	return offset, preserved
}

func (l *pythonLexer) scanFormatSpec(
	start int,
	outerQuote byte,
	outerWidth, depth int,
	preserved []pythonByteSpan,
) (int, []pythonByteSpan) {
	if depth > pythonMaximumFormattedNesting {
		return pythonFormattedNestingRecovery(l.source, start, outerWidth), preserved
	}
	offset := start
	for offset < len(l.source) {
		if l.source[offset] == '}' {
			if offset+1 < len(l.source) && l.source[offset+1] == '}' {
				offset += 2
				continue
			}
			return offset + 1, preserved
		}
		if l.source[offset] == '{' {
			if offset+1 < len(l.source) && l.source[offset+1] == '{' {
				offset += 2
				continue
			}
			next, nested := l.scanReplacementField(offset+1, outerQuote, outerWidth, depth+1)
			preserved = append(preserved, nested...)
			if next <= offset {
				next = offset + 1
			}
			offset = next
			continue
		}
		if outerWidth == 1 && (l.source[offset] == '\r' || l.source[offset] == '\n') {
			return offset, preserved
		}
		_, size := pythonDecode(l.source, offset)
		if size < 1 {
			size = 1
		}
		offset += size
	}
	return offset, preserved
}

func pythonFormattedNestingRecovery(source string, start, outerWidth int) int {
	if outerWidth == 3 {
		return len(source)
	}
	for offset := start; offset < len(source); {
		if newlineEnd := pythonNewlineEnd(source, offset); newlineEnd > offset {
			return offset
		}
		_, size := pythonDecode(source, offset)
		if size < 1 {
			size = 1
		}
		offset += size
	}
	return len(source)
}

func pythonDelimitersMatch(open, closing byte) bool {
	return open == '(' && closing == ')' || open == '[' && closing == ']' ||
		open == '{' && closing == '}'
}

func pythonSpanComplement(start, end int, preserved []pythonByteSpan) []pythonByteSpan {
	if start >= end {
		return nil
	}
	clipped := make([]pythonByteSpan, 0, len(preserved))
	for _, span := range preserved {
		if span.start < start {
			span.start = start
		}
		if span.end > end {
			span.end = end
		}
		if span.start < span.end {
			clipped = append(clipped, span)
		}
	}
	clipped = normalizePythonSpans(clipped)
	result := make([]pythonByteSpan, 0, len(clipped)+1)
	cursor := start
	for _, span := range clipped {
		if cursor < span.start {
			result = append(result, pythonByteSpan{cursor, span.start})
		}
		if span.end > cursor {
			cursor = span.end
		}
	}
	if cursor < end {
		result = append(result, pythonByteSpan{cursor, end})
	}
	return result
}

func normalizePythonSpans(spans []pythonByteSpan) []pythonByteSpan {
	filtered := make([]pythonByteSpan, 0, len(spans))
	for _, span := range spans {
		if span.start >= 0 && span.start < span.end {
			filtered = append(filtered, span)
		}
	}
	sort.Slice(filtered, func(left, right int) bool {
		if filtered[left].start != filtered[right].start {
			return filtered[left].start < filtered[right].start
		}
		return filtered[left].end < filtered[right].end
	})
	result := filtered[:0]
	for _, span := range filtered {
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

func (l *pythonLexer) topCommentMap() map[int]int {
	return l.topComments
}

func (l *pythonLexer) initializeTopLevelLookups() {
	l.topLiterals = make(map[int]pythonStringLiteral)
	for _, literal := range l.literals {
		if !literal.topLevel {
			continue
		}
		if previous, exists := l.topLiterals[literal.start]; !exists || literal.end > previous.end {
			l.topLiterals[literal.start] = literal
		}
	}
	l.topComments = make(map[int]int, len(l.comments))
	for _, comment := range l.comments {
		if comment.end > l.topComments[comment.start] {
			l.topComments[comment.start] = comment.end
		}
	}
}

func (l *pythonLexer) statements() []pythonStatement {
	literalAt := l.topLiterals
	commentAt := l.topCommentMap()
	var statements []pythonStatement
	statementStart := -1
	codeStart := -1
	statementIndent := 0
	stack := make([]byte, 0, 8)
	lineStart := 0
	lineCodeSeen := false
	lineCommentStart := -1
	explicitContinuation := false

	finish := func(end int) {
		if statementStart < 0 || codeStart < 0 {
			statementStart = -1
			codeStart = -1
			return
		}
		trimmedEnd := end
		for trimmedEnd > codeStart && pythonIsHorizontalSpace(l.source[trimmedEnd-1]) {
			trimmedEnd--
		}
		if trimmedEnd > codeStart {
			startLine, _ := l.lineColumn(codeStart)
			endLine := startLine
			if trimmedEnd > codeStart {
				endLine, _ = l.lineColumn(trimmedEnd - 1)
			}
			statements = append(statements, pythonStatement{
				start: statementStart, end: trimmedEnd, codeStart: codeStart,
				startLine: startLine, endLine: endLine, indent: statementIndent,
			})
		}
		statementStart = -1
		codeStart = -1
	}

	for offset := 0; offset < len(l.source); {
		if !lineCodeSeen {
			probe := offset
			for probe < len(l.source) && pythonIsHorizontalSpace(l.source[probe]) {
				probe++
			}
			if probe == offset && statementStart >= 0 &&
				(len(stack) > 0 || explicitContinuation) {
				indent := pythonIndentWidth(l.source[lineStart:offset])
				hardRecovery := l.hardRecoveryAt(offset, codeStart)
				softRecovery := pythonLikelyTypeAliasAt(l.source, offset) && indent <= statementIndent
				if hardRecovery || softRecovery {
					finish(lineStart)
					stack = stack[:0]
					explicitContinuation = false
				}
			}
		}
		if literal, exists := literalAt[offset]; exists && literal.end > offset {
			if statementStart < 0 {
				statementStart = offset
				codeStart = offset
				statementIndent = pythonIndentBefore(l.source, lineStart, offset)
			}
			lineCodeSeen = true
			offset = literal.end
			continue
		}
		if end := commentAt[offset]; end > offset {
			lineCommentStart = offset
			offset = end
			continue
		}
		ch := l.source[offset]
		if newlineEnd := pythonNewlineEnd(l.source, offset); newlineEnd > offset {
			explicitContinuation = lineCommentStart < lineStart &&
				pythonExplicitContinuation(l.source, lineStart, offset)
			continued := len(stack) > 0 || explicitContinuation
			if statementStart >= 0 && !continued {
				finish(offset)
				explicitContinuation = false
			}
			offset = newlineEnd
			lineStart = offset
			lineCodeSeen = false
			lineCommentStart = -1
			continue
		}
		if pythonIsHorizontalSpace(ch) {
			offset++
			continue
		}
		if statementStart < 0 {
			statementStart = offset
			codeStart = offset
			statementIndent = pythonIndentBefore(l.source, lineStart, offset)
		}
		lineCodeSeen = true
		switch ch {
		case '(', '[', '{':
			stack = append(stack, ch)
		case ')', ']', '}':
			if len(stack) > 0 && pythonDelimitersMatch(stack[len(stack)-1], ch) {
				stack = stack[:len(stack)-1]
			}
		case ';':
			if len(stack) == 0 {
				finish(offset)
			}
		}
		_, size := pythonDecode(l.source, offset)
		if size < 1 {
			size = 1
		}
		offset += size
	}
	finish(len(l.source))
	return statements
}

func pythonIndentBefore(source string, lineStart, code int) int {
	if lineStart < 0 || lineStart > code || code > len(source) {
		return 0
	}
	return pythonIndentWidth(source[lineStart:code])
}

func pythonIndentWidth(prefix string) int {
	column := 0
	for idx := range len(prefix) {
		switch prefix[idx] {
		case ' ':
			column++
		case '\t':
			column = (column/8 + 1) * 8
		case '\f':
			column = 0
		default:
			return column
		}
	}
	return column
}

func pythonExplicitContinuation(source string, lineStart, newline int) bool {
	end := newline
	count := 0
	for end > lineStart && source[end-1] == '\\' {
		count++
		end--
	}
	return count == 1
}

func (l *pythonLexer) hardRecoveryAt(offset, statementCodeStart int) bool {
	if offset >= len(l.source) {
		return false
	}
	for _, keyword := range []string{"def", "class"} {
		if pythonWordAt(l.source, offset, keyword) {
			return true
		}
	}
	if pythonWordAt(l.source, offset, "import") {
		statementLead := pythonSkipLayout(l.source, statementCodeStart, offset)
		return !pythonWordAt(l.source, statementLead, "from")
	}
	if pythonWordAt(l.source, offset, "from") {
		return pythonLikelyFromImportAt(l.source, offset)
	}
	if pythonWordAt(l.source, offset, "async") {
		afterAsync := pythonSkipLayout(l.source, offset+len("async"), len(l.source))
		return pythonWordAt(l.source, afterAsync, "def")
	}
	return false
}

func pythonLikelyFromImportAt(source string, offset int) bool {
	if !pythonWordAt(source, offset, "from") {
		return false
	}
	for offset += len("from"); offset < len(source); {
		if pythonNewlineEnd(source, offset) > offset || source[offset] == '#' {
			return false
		}
		if pythonWordAt(source, offset, "import") {
			return true
		}
		_, size := pythonDecode(source, offset)
		if size < 1 {
			size = 1
		}
		offset += size
	}
	return false
}

func pythonLikelyTypeAliasAt(source string, offset int) bool {
	if !pythonWordAt(source, offset, "type") {
		return false
	}
	offset = pythonSkipLayout(source, offset+len("type"), len(source))
	nameEnd := pythonIdentifierEnd(source, offset)
	if nameEnd == offset || !pythonIdentifier(source[offset:nameEnd]) {
		return false
	}
	depth := 0
	const maximumRecoveryScan = 64 * 1024
	limit := min(len(source), nameEnd+maximumRecoveryScan)
	for offset = nameEnd; offset < limit; offset++ {
		switch source[offset] {
		case '#':
			offset = pythonCommentEnd(source, offset) - 1
		case '(', '[', '{':
			depth++
		case ')', ']', '}':
			if depth == 0 {
				return false
			}
			depth--
		case ':':
			if depth == 0 {
				return false
			}
		case '=':
			return depth == 0 && (offset+1 >= len(source) || source[offset+1] != '=')
		case ';':
			if depth == 0 {
				return false
			}
		}
	}
	return false
}

func pythonWordAt(source string, offset int, word string) bool {
	if offset < 0 || offset+len(word) > len(source) || source[offset:offset+len(word)] != word {
		return false
	}
	if offset > 0 {
		before, _ := utf8.DecodeLastRuneInString(source[:offset])
		if pythonIdentifierContinue(before) {
			return false
		}
	}
	if offset+len(word) < len(source) {
		after, _ := pythonDecode(source, offset+len(word))
		if pythonIdentifierContinue(after) {
			return false
		}
	}
	return true
}

func (l *pythonLexer) fullyMaskedSource() string {
	masked := []byte(l.source)
	spans := append([]pythonByteSpan(nil), l.comments...)
	for _, literal := range l.literals {
		if literal.topLevel {
			spans = append(spans, pythonByteSpan{literal.start, literal.end})
		}
	}
	for _, span := range normalizePythonSpans(spans) {
		for idx := span.start; idx < span.end && idx < len(masked); idx++ {
			if masked[idx] != '\n' && masked[idx] != '\r' {
				masked[idx] = ' '
			}
		}
	}
	return string(masked)
}

func pythonSkipLayout(source string, offset, end int) int {
	for offset < end {
		if pythonIsHorizontalSpace(source[offset]) || source[offset] == '\n' || source[offset] == '\r' {
			offset++
			continue
		}
		if source[offset] == '\\' {
			probe := offset + 1
			for probe < end && (source[probe] == ' ' || source[probe] == '\t' || source[probe] == '\f') {
				probe++
			}
			if newlineEnd := pythonNewlineEnd(source, probe); newlineEnd > probe {
				offset = newlineEnd
				continue
			}
		}
		break
	}
	return offset
}

func pythonLeadingDefinition(masked string, statement pythonStatement) (string, int, string, bool) {
	offset := pythonSkipLayout(masked, statement.codeStart, statement.end)
	var kind string
	switch {
	case pythonWordAt(masked, offset, "async"):
		offset = pythonSkipLayout(masked, offset+len("async"), statement.end)
		if !pythonWordAt(masked, offset, "def") {
			return "", 0, "", false
		}
		kind = "def"
		offset += len("def")
	case pythonWordAt(masked, offset, "def"):
		kind = "def"
		offset += len("def")
	case pythonWordAt(masked, offset, "class"):
		kind = "class"
		offset += len("class")
	case pythonWordAt(masked, offset, "type"):
		kind = "type"
		offset += len("type")
	default:
		return "", 0, "", false
	}
	offset = pythonSkipLayout(masked, offset, statement.end)
	nameEnd := pythonIdentifierEnd(masked, offset)
	if nameEnd == offset {
		return "", 0, "", false
	}
	name := masked[offset:nameEnd]
	if !pythonIdentifier(name) {
		return "", 0, "", false
	}
	if kind == "type" && !pythonHasTopLevelAssignment(masked, nameEnd, statement.end) {
		return "", 0, "", false
	}
	return name, offset, kind, true
}

func pythonHasTopLevelAssignment(source string, start, end int) bool {
	stack := make([]byte, 0, 4)
	for offset := start; offset < end; offset++ {
		switch source[offset] {
		case '(', '[', '{':
			stack = append(stack, source[offset])
		case ')', ']', '}':
			if len(stack) > 0 && pythonDelimitersMatch(stack[len(stack)-1], source[offset]) {
				stack = stack[:len(stack)-1]
			}
		case '=':
			if len(stack) == 0 &&
				(offset == start || !strings.ContainsRune("=!<>:", rune(source[offset-1]))) &&
				(offset+1 >= end || source[offset+1] != '=') {
				return true
			}
		}
	}
	return false
}

func pythonTopLevelColon(source string, start, end int) int {
	stack := make([]byte, 0, 8)
	for offset := start; offset < end; offset++ {
		switch source[offset] {
		case '(', '[', '{':
			stack = append(stack, source[offset])
		case ')', ']', '}':
			if len(stack) > 0 && pythonDelimitersMatch(stack[len(stack)-1], source[offset]) {
				stack = stack[:len(stack)-1]
			}
		case ':':
			if len(stack) == 0 && (offset+1 >= end || source[offset+1] != '=') {
				return offset
			}
		}
	}
	return -1
}

func pythonCompoundKind(masked string, statement pythonStatement) (string, int, bool) {
	offset := pythonSkipLayout(masked, statement.codeStart, statement.end)
	if pythonWordAt(masked, offset, "async") {
		afterAsync := pythonSkipLayout(masked, offset+len("async"), statement.end)
		for _, keyword := range []string{"def", "for", "with"} {
			if pythonWordAt(masked, afterAsync, keyword) {
				colon := pythonTopLevelColon(masked, afterAsync+len(keyword), statement.end)
				return "async " + keyword, colon, colon >= 0
			}
		}
		return "", -1, false
	}
	for _, keyword := range []string{
		"def", "class", "if", "elif", "else", "for", "while", "try",
		"except", "finally", "with", "match", "case",
	} {
		if pythonWordAt(masked, offset, keyword) {
			after := offset + len(keyword)
			if keyword == "except" && after < statement.end && masked[after] == '*' {
				after++
			}
			colon := pythonTopLevelColon(masked, after, statement.end)
			return keyword, colon, colon >= 0
		}
	}
	return "", -1, false
}

func pythonDecoratorStatement(masked string, statement pythonStatement) bool {
	offset := pythonSkipLayout(masked, statement.codeStart, statement.end)
	return offset < statement.end && masked[offset] == '@'
}

func (l *pythonLexer) rangeHasCode(masked string, start, end int) bool {
	literalAt := l.topLiterals
	commentAt := l.topCommentMap()
	for offset := start; offset < end; {
		if literal, exists := literalAt[offset]; exists && literal.end > offset {
			return true
		}
		if commentEnd := commentAt[offset]; commentEnd > offset {
			offset = commentEnd
			continue
		}
		if pythonIsHorizontalSpace(masked[offset]) || masked[offset] == '\r' || masked[offset] == '\n' {
			offset++
			continue
		}
		return true
	}
	return false
}

func (l *pythonLexer) definitionsAndHeaders(statements []pythonStatement) ([]sourceDefinition, []pythonHeader) {
	masked := l.fullyMaskedSource()
	definitions := make([]sourceDefinition, 0)
	headers := make([]pythonHeader, 0)
	pendingDecorators := make(map[int]int)

	for statementIndex, statement := range statements {
		for indent := range pendingDecorators {
			if indent > statement.indent {
				delete(pendingDecorators, indent)
			}
		}
		if pythonDecoratorStatement(masked, statement) {
			if _, exists := pendingDecorators[statement.indent]; !exists {
				pendingDecorators[statement.indent] = statement.startLine
			}
			continue
		}

		name, nameOffset, definitionKind, isDefinition := pythonLeadingDefinition(masked, statement)
		definitionIndex := -1
		definitionStart := statement.startLine
		if decoratedStart, exists := pendingDecorators[statement.indent]; exists &&
			(definitionKind == "def" || definitionKind == "class") {
			definitionStart = decoratedStart
		}
		delete(pendingDecorators, statement.indent)
		if isDefinition {
			line, column := l.lineColumn(nameOffset)
			definitionIndex = len(definitions)
			definitions = append(definitions, sourceDefinition{
				symbol: name, line: line, column: column,
				scopeStart: definitionStart, scopeEnd: statement.endLine,
				ownsScope: definitionKind != "type",
			})
		}

		compoundKind, colon, isCompound := pythonCompoundKind(masked, statement)
		if isCompound {
			headers = append(headers, pythonHeader{
				statement: statementIndex, kind: compoundKind, startLine: definitionStart,
				headerLine: statement.startLine, endLine: statement.endLine,
				indent: statement.indent, colon: colon,
				inline:     l.rangeHasCode(masked, colon+1, statement.end),
				definition: definitionIndex,
			})
		} else if isDefinition && definitionKind != "type" {
			// Keep malformed definitions navigable, but never let their missing
			// delimiter absorb a later declaration.
			headers = append(headers, pythonHeader{
				statement: statementIndex, kind: definitionKind, startLine: definitionStart,
				headerLine: statement.startLine, endLine: statement.endLine,
				indent: statement.indent, colon: -1, inline: true,
				definition: definitionIndex,
			})
		}
	}
	return definitions, headers
}

func (l *pythonLexer) resolveScopes(statements []pythonStatement, headers []pythonHeader, definitions []sourceDefinition) []pythonLineScope {
	scopes := make([]pythonLineScope, 0, len(headers))
	for _, header := range headers {
		endLine := header.endLine
		if !header.inline && header.colon >= 0 {
			lastBodyLine := 0
			nextOutsideLine := 0
			for idx := header.statement + 1; idx < len(statements); idx++ {
				candidate := statements[idx]
				if candidate.startLine == statements[header.statement].startLine {
					continue
				}
				if candidate.indent <= header.indent {
					nextOutsideLine = candidate.startLine
					break
				}
				if candidate.endLine > lastBodyLine {
					lastBodyLine = candidate.endLine
				}
			}
			if lastBodyLine > endLine {
				endLine = lastBodyLine
				trailingLimit := len(l.lineStarts)
				if nextOutsideLine > 0 {
					trailingLimit = nextOutsideLine - 1
				}
				endLine = l.trailingSuiteEnd(endLine, trailingLimit, header.indent)
			}
		}
		if endLine < header.startLine {
			endLine = header.startLine
		}
		scopes = append(scopes, pythonLineScope{start: header.startLine, end: endLine})
		if header.definition >= 0 && header.definition < len(definitions) {
			definitions[header.definition].scopeStart = header.startLine
			definitions[header.definition].scopeEnd = endLine
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

func (l *pythonLexer) trailingSuiteEnd(endLine, limit, headerIndent int) int {
	originalEnd := endLine
	candidateEnd := endLine
	insideCommentBlock := false
	limit = min(limit, len(l.lineStarts))
	for line := endLine + 1; line <= limit; line++ {
		start := l.lineStarts[line-1]
		end := len(l.source)
		if line < len(l.lineStarts) {
			end = l.lineStarts[line]
		}
		for end > start && (l.source[end-1] == '\n' || l.source[end-1] == '\r') {
			end--
		}
		probe := start
		for probe < end && pythonIsHorizontalSpace(l.source[probe]) {
			probe++
		}
		if probe == end {
			candidateEnd = line
			continue
		}
		if l.topComments[probe] > probe && pythonIndentWidth(l.source[start:probe]) > headerIndent {
			candidateEnd = line
			insideCommentBlock = true
			continue
		}
		break
	}
	if insideCommentBlock {
		return candidateEnd
	}
	return originalEnd
}

func pythonLeadingImportOffset(masked string, start, end int) (int, bool) {
	offset := pythonSkipLayout(masked, start, end)
	return offset, pythonWordAt(masked, offset, "import") ||
		pythonWordAt(masked, offset, "from")
}

func (l *pythonLexer) resolveImports(statements []pythonStatement) []pythonLineSpan {
	masked := l.fullyMaskedSource()
	imports := make([]pythonLineSpan, 0)
	for _, statement := range statements {
		importOffset, isImport := pythonLeadingImportOffset(
			masked,
			statement.codeStart,
			statement.end,
		)
		if !isImport {
			_, colon, compound := pythonCompoundKind(masked, statement)
			if compound && colon >= 0 {
				importOffset, isImport = pythonLeadingImportOffset(masked, colon+1, statement.end)
			}
		}
		if isImport {
			startLine, _ := l.lineColumn(importOffset)
			imports = append(imports, pythonLineSpan{start: startLine, end: statement.endLine})
		}
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

func (l *pythonLexer) resolveDocstrings(statements []pythonStatement, headers []pythonHeader) []pythonByteSpan {
	docstrings := make([]pythonByteSpan, 0, len(headers)+1)
	if len(statements) > 0 {
		if span, ok := l.docstringExpression(statements[0].codeStart, statements[0].end); ok {
			docstrings = append(docstrings, span)
		}
	}
	for _, header := range headers {
		if header.kind != "def" && header.kind != "async def" && header.kind != "class" {
			continue
		}
		statement := statements[header.statement]
		if header.colon < 0 {
			continue
		}
		if header.inline {
			if span, ok := l.docstringExpression(header.colon+1, statement.end); ok {
				docstrings = append(docstrings, span)
			}
			continue
		}
		for idx := header.statement + 1; idx < len(statements); idx++ {
			candidate := statements[idx]
			if candidate.startLine == statement.startLine {
				continue
			}
			if candidate.indent <= header.indent {
				break
			}
			if span, ok := l.docstringExpression(candidate.codeStart, candidate.end); ok {
				docstrings = append(docstrings, span)
			}
			break
		}
	}
	return docstrings
}

func (l *pythonLexer) docstringExpression(start, end int) (pythonByteSpan, bool) {
	if start < 0 {
		start = 0
	}
	if end > len(l.source) {
		end = len(l.source)
	}
	offset := start
	depth := 0
	found := false
	spanStart := -1
	spanEnd := -1
	for offset < end {
		if literal, exists := l.topLiterals[offset]; exists && literal.end <= end {
			if literal.bytes || literal.formatted {
				return pythonByteSpan{}, false
			}
			if spanStart < 0 {
				spanStart = offset
			}
			spanEnd = literal.end
			found = true
			offset = literal.end
			continue
		}
		if commentEnd := l.topComments[offset]; commentEnd > offset && commentEnd <= end {
			offset = commentEnd
			continue
		}
		ch := l.source[offset]
		if pythonIsHorizontalSpace(ch) || ch == '\r' || ch == '\n' {
			offset++
			continue
		}
		if ch == '\\' {
			probe := offset + 1
			for probe < end && pythonIsHorizontalSpace(l.source[probe]) {
				probe++
			}
			if newlineEnd := pythonNewlineEnd(l.source, probe); newlineEnd > probe && newlineEnd <= end {
				offset = newlineEnd
				continue
			}
			return pythonByteSpan{}, false
		}
		if ch == '(' {
			if spanStart < 0 {
				spanStart = offset
			}
			spanEnd = offset + 1
			depth++
			offset++
			continue
		}
		if ch == ')' {
			if depth == 0 {
				return pythonByteSpan{}, false
			}
			depth--
			spanEnd = offset + 1
			offset++
			continue
		}
		return pythonByteSpan{}, false
	}
	if !found || depth != 0 || spanStart < 0 || spanEnd <= spanStart {
		return pythonByteSpan{}, false
	}
	return pythonByteSpan{start: spanStart, end: spanEnd}, true
}
