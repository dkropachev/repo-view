package repoview

import "strings"

type swiftQualifiedOccurrenceToken struct {
	text       string
	identifier bool
}

// walkAdditionalSymbolOccurrences reconciles Find's physical-line counter
// with qualified Swift names. Whitespace and comments are lexical trivia and
// may separate the components; opaque literals terminate a partial match.
func (swift swiftLanguage) walkAdditionalSymbolOccurrences(
	lines []string,
	symbol string,
	visit func(lineNo, additionalCount int) bool,
) bool {
	if visit == nil {
		return false
	}
	return swift.walkAdditionalSymbolOccurrencesAt(
		lines, symbol,
		func(lineNo, additionalCount int, _, _ []int) bool {
			return visit(lineNo, additionalCount)
		},
	)
}

func (swift swiftLanguage) walkAdditionalSymbolOccurrencesAt(
	lines []string,
	symbol string,
	visit func(
		lineNo, additionalCount int,
		addedColumns, removedColumns []int,
	) bool,
) bool {
	if visit == nil {
		return false
	}
	pattern, ok := swiftQualifiedOccurrencePattern(symbol)
	if !ok {
		return false
	}
	analysis := swift.sourceAnalysis(lines)
	if analysis == nil || len(lines) == 0 {
		return true
	}
	prefix := make([]int, len(pattern))
	for index, matched := 1, 0; index < len(pattern); index++ {
		for matched > 0 && !swiftQualifiedPatternTokenEqual(
			pattern[index], pattern[matched],
		) {
			matched = prefix[matched-1]
		}
		if swiftQualifiedPatternTokenEqual(pattern[index], pattern[matched]) {
			matched++
		}
		prefix[index] = matched
	}

	starts := make([]int, len(pattern))
	tokenCount := 0
	matched := 0
	nextLine := 1
	pendingAdjustment := 0
	pendingColumns := make([]int, 0, 1)
	visitorStopped := false
	matchLines := swiftOccurrenceLineCursor{starts: analysis.lineStarts}
	frontierLines := swiftOccurrenceLineCursor{starts: analysis.lineStarts}

	emitThrough := func(lastLine int) bool {
		lastLine = min(lastLine, len(lines))
		for nextLine <= lastLine {
			if !visit(nextLine, pendingAdjustment, pendingColumns, nil) {
				visitorStopped = true
				return false
			}
			nextLine++
			pendingAdjustment = 0
			pendingColumns = pendingColumns[:0]
		}
		return true
	}
	record := func(lineNo, start int) bool {
		if lineNo < nextLine {
			return true
		}
		if !emitThrough(lineNo - 1) {
			return false
		}
		if pendingAdjustment < int(^uint(0)>>1) {
			pendingAdjustment++
		}
		if lineNo <= len(analysis.lineStarts) {
			column := start - analysis.lineStarts[lineNo-1] + 1
			pendingColumns = append(pendingColumns, column)
		}
		return true
	}
	matchFrontier := func(fallback int) int {
		if matched == 0 {
			return fallback
		}
		return starts[(tokenCount-matched)%len(starts)]
	}
	emitBeforeFrontier := func(frontier int) bool {
		return emitThrough(frontierLines.lineAt(frontier) - 1)
	}

	walkSwiftLexically(analysis.source, swiftLexicalSink{
		comment: func(span cByteSpan) bool {
			return emitBeforeFrontier(matchFrontier(span.end))
		},
		literal: func(span cByteSpan) bool {
			matched = 0
			return emitBeforeFrontier(span.end)
		},
		token: func(token swiftToken) bool {
			start := token.start
			if token.kind == swiftTokenIdentifier {
				start = token.nameStart
			}
			starts[tokenCount%len(starts)] = start
			tokenCount++

			for matched > 0 && !swiftQualifiedOccurrenceTokenEqual(
				token, pattern, matched,
			) {
				matched = prefix[matched-1]
			}
			if swiftQualifiedOccurrenceTokenEqual(token, pattern, matched) {
				matched++
			}
			if matched == len(pattern) {
				start := starts[(tokenCount-len(pattern))%len(starts)]
				if !record(matchLines.lineAt(start), start) {
					return false
				}
				matched = prefix[matched-1]
			}
			return emitBeforeFrontier(matchFrontier(token.end))
		},
	})
	if !visitorStopped {
		_ = emitThrough(len(lines))
	}
	return true
}

func swiftQualifiedOccurrencePattern(
	symbol string,
) ([]swiftQualifiedOccurrenceToken, bool) {
	if symbol == "" {
		return nil, false
	}
	pattern := make([]swiftQualifiedOccurrenceToken, 0, 5)
	for offset := 0; offset < len(symbol); {
		text, end, nameStart, ok := swiftIdentifierAt(symbol, offset)
		if !ok || nameStart != offset || text == "" || end <= offset {
			return nil, false
		}
		pattern = append(pattern, swiftQualifiedOccurrenceToken{
			text: text, identifier: true,
		})
		if end == len(symbol) {
			break
		}
		separator := ""
		switch {
		case symbol[end] == '.':
			separator = "."
		case strings.HasPrefix(symbol[end:], "::"):
			separator = "::"
		}
		if separator == "" || end+len(separator) >= len(symbol) {
			return nil, false
		}
		pattern = append(pattern, swiftQualifiedOccurrenceToken{text: separator})
		offset = end + len(separator)
	}
	return pattern, len(pattern) >= 3
}

func swiftQualifiedPatternTokenEqual(
	left, right swiftQualifiedOccurrenceToken,
) bool {
	return left.identifier == right.identifier && left.text == right.text
}

func swiftQualifiedOccurrenceTokenEqual(
	token swiftToken,
	pattern []swiftQualifiedOccurrenceToken,
	index int,
) bool {
	if index < 0 || index >= len(pattern) {
		return false
	}
	expected := pattern[index]
	if !expected.identifier {
		return token.kind == swiftTokenPunctuation && token.text == expected.text
	}
	return token.kind == swiftTokenIdentifier && token.text == expected.text
}

type swiftOccurrenceLineCursor struct {
	starts []int
	index  int
	offset int
}

func (cursor *swiftOccurrenceLineCursor) lineAt(offset int) int {
	if cursor == nil || len(cursor.starts) == 0 {
		return 1
	}
	if offset < cursor.offset {
		cursor.index = 0
	}
	cursor.offset = max(0, offset)
	for cursor.index+1 < len(cursor.starts) &&
		cursor.starts[cursor.index+1] <= cursor.offset {
		cursor.index++
	}
	return cursor.index + 1
}
