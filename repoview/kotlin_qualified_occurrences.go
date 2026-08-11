package repoview

type kotlinQualifiedOccurrenceToken struct {
	text       string
	identifier bool
}

// walkAdditionalSymbolOccurrences reconciles Find's physical-line counter
// with qualified Kotlin names. The ordinary counter sees one identifier at a
// time, while a qualified name is a token sequence that may cross whitespace,
// comments, or physical lines. Opaque literals break a match.
func (kotlin kotlinLanguage) walkAdditionalSymbolOccurrences(
	lines []string,
	symbol string,
	visit func(lineNo, additionalCount int) bool,
) bool {
	if visit == nil {
		return false
	}
	return kotlin.walkAdditionalSymbolOccurrencesAt(
		lines, symbol,
		func(lineNo, additionalCount int, _, _ []int) bool {
			return visit(lineNo, additionalCount)
		},
	)
}

func (kotlin kotlinLanguage) walkAdditionalSymbolOccurrencesAt(
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
	pattern, ok := kotlinQualifiedOccurrencePattern(symbol)
	if !ok {
		return false
	}
	analysis := kotlin.sourceAnalysis(lines)
	if analysis == nil || len(lines) == 0 {
		return true
	}
	prefix := make([]int, len(pattern))
	for index, matched := 1, 0; index < len(pattern); index++ {
		for matched > 0 && !kotlinQualifiedPatternTokenEqual(
			pattern[index], pattern[matched],
		) {
			matched = prefix[matched-1]
		}
		if kotlinQualifiedPatternTokenEqual(pattern[index], pattern[matched]) {
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
	matchLines := kotlinOccurrenceLineCursor{starts: analysis.lineStarts}
	frontierLines := kotlinOccurrenceLineCursor{starts: analysis.lineStarts}

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

	walkKotlinLexically(analysis.source, kotlinLexicalSink{
		comment: func(span cByteSpan) bool {
			return emitBeforeFrontier(matchFrontier(span.end))
		},
		literal: func(span cByteSpan) bool {
			matched = 0
			return emitBeforeFrontier(span.end)
		},
		token: func(token kotlinToken) bool {
			start := token.start
			if token.kind == kotlinTokenIdentifier {
				start = token.nameStart
			}
			starts[tokenCount%len(starts)] = start
			tokenCount++

			for matched > 0 && !kotlinQualifiedOccurrenceTokenEqual(
				token, pattern[matched],
			) {
				matched = prefix[matched-1]
			}
			if kotlinQualifiedOccurrenceTokenEqual(token, pattern[matched]) {
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

func kotlinQualifiedOccurrencePattern(
	symbol string,
) ([]kotlinQualifiedOccurrenceToken, bool) {
	if symbol == "" {
		return nil, false
	}
	pattern := make([]kotlinQualifiedOccurrenceToken, 0, 5)
	for offset := 0; offset < len(symbol); {
		text, end, _, quoted, ok := kotlinIdentifierAt(symbol, offset)
		if !ok || quoted || text == "" || end <= offset {
			return nil, false
		}
		pattern = append(pattern, kotlinQualifiedOccurrenceToken{
			text: text, identifier: true,
		})
		if end == len(symbol) {
			break
		}
		if symbol[end] != '.' || end+1 >= len(symbol) {
			return nil, false
		}
		pattern = append(pattern, kotlinQualifiedOccurrenceToken{text: "."})
		offset = end + 1
	}
	return pattern, len(pattern) >= 3
}

func kotlinQualifiedPatternTokenEqual(
	left, right kotlinQualifiedOccurrenceToken,
) bool {
	return left.identifier == right.identifier && left.text == right.text
}

func kotlinQualifiedOccurrenceTokenEqual(
	token kotlinToken,
	expected kotlinQualifiedOccurrenceToken,
) bool {
	if !expected.identifier {
		return token.kind == kotlinTokenPunctuation && token.text == expected.text
	}
	return token.kind == kotlinTokenIdentifier && token.text == expected.text
}

type kotlinOccurrenceLineCursor struct {
	starts []int
	index  int
	offset int
}

func (cursor *kotlinOccurrenceLineCursor) lineAt(offset int) int {
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
