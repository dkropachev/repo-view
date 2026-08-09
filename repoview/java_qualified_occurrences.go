package repoview

import "strings"

type javaOccurrenceColumnState struct {
	added   []int
	removed []int
}

// walkAdditionalSymbolOccurrences visits every physical source line in order
// with the number of qualified Java-name occurrences that the ordinary raw
// substring search cannot see. A qualified name may cross whitespace,
// comments, physical lines, or a Unicode-escaped dot. Opaque literals break a
// match. The visitor controls early termination, so Find does not build a map
// proportional to the number of matching lines before applying its limit.
//
// The result is false when symbol is not one canonical qualified Java name; in
// that case no line is visited and Find uses its ordinary line walk.
func (j javaLanguage) walkAdditionalSymbolOccurrences(
	lines []string,
	symbol string,
	visit func(lineNo, additionalCount int) bool,
) bool {
	if visit == nil {
		return false
	}
	return j.walkAdditionalSymbolOccurrencesWithVisitor(lines, symbol, visit, nil)
}

func (j javaLanguage) walkAdditionalSymbolOccurrencesAt(
	lines []string,
	symbol string,
	visit func(
		lineNo, additionalCount int,
		addedColumns, removedColumns []int,
	) bool,
) bool {
	return j.walkAdditionalSymbolOccurrencesWithVisitor(lines, symbol, nil, visit)
}

func (j javaLanguage) walkAdditionalSymbolOccurrencesWithVisitor(
	lines []string,
	symbol string,
	visit func(lineNo, additionalCount int) bool,
	visitAt func(
		lineNo, additionalCount int,
		addedColumns, removedColumns []int,
	) bool,
) bool {
	if visit == nil && visitAt == nil {
		return false
	}
	pattern, ok := javaQualifiedOccurrencePattern(symbol)
	if !ok {
		if _, unqualified := javaUnqualifiedOccurrencePattern(symbol); !unqualified {
			return false
		}
		if visit != nil {
			return j.walkUnqualifiedNumericOccurrenceAdjustments(lines, symbol, visit)
		}
		return j.walkUnqualifiedNumericOccurrenceAdjustmentsAt(
			lines, symbol, visitAt,
		)
	}
	analysis := j.sourceAnalysis(lines)
	if analysis == nil || len(lines) == 0 {
		return true
	}
	prefix := make([]int, len(pattern))
	javaBuildQualifiedOccurrencePrefix(pattern, prefix)
	raw := newJavaRawOccurrenceCursor(analysis.source, symbol)

	// One ring entry per query token is enough to recover the start of the
	// current KMP suffix. Storage is proportional to the query, never the file.
	starts := make([]int, len(pattern))
	tokenCount := 0
	matched := 0
	nextLine := 1
	pendingLine := 0
	pendingAdjustment := 0
	var pendingColumns *javaOccurrenceColumnState
	if visitAt != nil {
		pendingColumns = &javaOccurrenceColumnState{}
	}
	visitorStopped := false
	numericSpans := make([]javaByteSpan, 0)
	numericHead := 0

	startBoundaries := newJavaTranslatedBoundaryCursor(analysis.lexed.input)
	endBoundaries := newJavaTranslatedBoundaryCursor(analysis.lexed.input)
	rawStartBoundaries := newJavaTranslatedBoundaryCursor(analysis.lexed.input)
	rawEndBoundaries := newJavaTranslatedBoundaryCursor(analysis.lexed.input)
	matchLineCursor := javaMonotonicLineCursor{starts: analysis.lineStarts}
	frontierLineCursor := javaMonotonicLineCursor{starts: analysis.lineStarts}

	emitThrough := func(lastLine int) bool {
		lastLine = min(lastLine, len(lines))
		for nextLine <= lastLine {
			adjustment := 0
			var addedColumns, removedColumns []int
			if pendingLine == nextLine {
				adjustment = pendingAdjustment
				if pendingColumns != nil {
					addedColumns = pendingColumns.added
					removedColumns = pendingColumns.removed
				}
				pendingLine, pendingAdjustment = 0, 0
			}
			var keepGoing bool
			if visitAt != nil {
				keepGoing = visitAt(
					nextLine, adjustment, addedColumns, removedColumns,
				)
			} else {
				keepGoing = visit(nextLine, adjustment)
			}
			if pendingLine == 0 && pendingColumns != nil {
				pendingColumns.added = pendingColumns.added[:0]
				pendingColumns.removed = pendingColumns.removed[:0]
			}
			if !keepGoing {
				visitorStopped = true
				return false
			}
			nextLine++
		}
		return true
	}

	recordAdjustment := func(
		lineNo, adjustment, addedColumn, removedColumn int,
	) bool {
		if adjustment == 0 && addedColumn <= 0 && removedColumn <= 0 {
			return true
		}
		if pendingLine == 0 {
			pendingLine, pendingAdjustment = lineNo, adjustment
			if pendingColumns != nil {
				if addedColumn > 0 {
					pendingColumns.added = append(pendingColumns.added, addedColumn)
				}
				if removedColumn > 0 {
					pendingColumns.removed = append(pendingColumns.removed, removedColumn)
				}
			}
			return true
		}
		if pendingLine != lineNo {
			// The safe-frontier invariant normally emits an earlier completed
			// line before a match can complete on a later one. Keep correctness
			// explicit if that invariant changes with a future matcher.
			if !emitThrough(lineNo-1) || pendingAdjustment != 0 {
				return false
			}
			pendingLine, pendingAdjustment = lineNo, adjustment
			if pendingColumns != nil {
				if addedColumn > 0 {
					pendingColumns.added = append(pendingColumns.added, addedColumn)
				}
				if removedColumn > 0 {
					pendingColumns.removed = append(pendingColumns.removed, removedColumn)
				}
			}
			return true
		}
		if adjustment > 0 && pendingAdjustment > int(^uint(0)>>1)-adjustment {
			pendingAdjustment = int(^uint(0) >> 1)
		} else {
			pendingAdjustment += adjustment
		}
		if pendingColumns != nil {
			if addedColumn > 0 {
				pendingColumns.added = append(pendingColumns.added, addedColumn)
			}
			if removedColumn > 0 {
				pendingColumns.removed = append(pendingColumns.removed, removedColumn)
			}
		}
		return true
	}
	rawMatchLines := javaMonotonicLineCursor{starts: analysis.lineStarts}
	advanceRaw := func(end int) bool {
		raw.advanceMatches(end, func(start, matchEnd int) {
			for numericHead < len(numericSpans) && numericSpans[numericHead].end <= start {
				numericHead++
			}
			if numericHead < len(numericSpans) &&
				numericSpans[numericHead].start <= start &&
				start < numericSpans[numericHead].end &&
				javaQualifiedOccurrenceBoundaries(
					analysis.source, start, matchEnd,
					&rawStartBoundaries, &rawEndBoundaries,
				) {
				lineNo := rawMatchLines.lineAt(start)
				column := start - analysis.lineStarts[lineNo-1] + 1
				if !recordAdjustment(lineNo, -1, 0, column) {
					visitorStopped = true
				}
			}
		})
		cutoff := raw.offset - len(symbol)
		for numericHead < len(numericSpans) && numericSpans[numericHead].end <= cutoff {
			numericHead++
		}
		if numericHead > 1024 && numericHead*2 >= len(numericSpans) {
			copy(numericSpans, numericSpans[numericHead:])
			numericSpans = numericSpans[:len(numericSpans)-numericHead]
			numericHead = 0
		}
		return !visitorStopped
	}

	streamJavaLexicalEventsFromInput(
		analysis.lexed.input,
		javaLexicalStreamOptions{},
		func(event javaLexicalStreamEvent) bool {
			var frontier int
			switch event.kind {
			case javaLexicalStreamOpaque:
				if !advanceRaw(event.span.end) {
					return false
				}
				matched = 0
				frontier = event.span.end
			case javaLexicalStreamComment:
				if !advanceRaw(event.span.end) {
					return false
				}
				if matched > 0 {
					frontier = starts[(tokenCount-matched)%len(starts)]
				} else {
					frontier = event.span.end
				}
			case javaLexicalStreamToken:
				token := event.token
				if token.numeric {
					numericSpans = append(numericSpans, javaByteSpan{
						start: token.start, end: token.end,
					})
				}
				if !advanceRaw(token.end) {
					return false
				}
				starts[tokenCount%len(starts)] = token.start
				tokenCount++

				for matched > 0 &&
					!javaQualifiedOccurrenceTokenEqual(token, pattern[matched]) {
					matched = prefix[matched-1]
				}
				if javaQualifiedOccurrenceTokenEqual(token, pattern[matched]) {
					matched++
				}
				if matched == len(pattern) {
					start := starts[(tokenCount-len(pattern))%len(starts)]
					end := token.end
					if javaQualifiedOccurrenceBoundaries(
						analysis.source, start, end,
						&startBoundaries, &endBoundaries,
					) && !raw.matchesAt(start, end) {
						lineNo := matchLineCursor.lineAt(start)
						column := start - analysis.lineStarts[lineNo-1] + 1
						if !recordAdjustment(lineNo, 1, column, 0) {
							return false
						}
					}
					matched = prefix[matched-1]
				}
				if matched > 0 {
					frontier = starts[(tokenCount-matched)%len(starts)]
				} else {
					frontier = token.end
				}
			}

			// A future qualified match can still start on frontier's physical
			// line, but never on an earlier line. Finalize those earlier lines now
			// so the caller can apply its unique-result limit during the scan.
			return emitThrough(frontierLineCursor.lineAt(frontier) - 1)
		},
	)
	if visitorStopped {
		return true
	}
	_ = emitThrough(len(lines))
	return true
}

func (j javaLanguage) walkUnqualifiedNumericOccurrenceAdjustments(
	lines []string,
	symbol string,
	visit func(lineNo, adjustment int) bool,
) bool {
	return j.walkUnqualifiedNumericOccurrenceAdjustmentsWithVisitor(
		lines, symbol, visit, nil,
	)
}

func (j javaLanguage) walkUnqualifiedNumericOccurrenceAdjustmentsAt(
	lines []string,
	symbol string,
	visit func(
		lineNo, adjustment int,
		addedColumns, removedColumns []int,
	) bool,
) bool {
	return j.walkUnqualifiedNumericOccurrenceAdjustmentsWithVisitor(
		lines, symbol, nil, visit,
	)
}

func (j javaLanguage) walkUnqualifiedNumericOccurrenceAdjustmentsWithVisitor(
	lines []string,
	symbol string,
	visit func(lineNo, adjustment int) bool,
	visitAt func(
		lineNo, adjustment int,
		addedColumns, removedColumns []int,
	) bool,
) bool {
	analysis := j.sourceAnalysis(lines)
	if analysis == nil || len(lines) == 0 {
		return true
	}
	prefix := make([]int, len(symbol))
	javaBuildRawSymbolPrefix(symbol, prefix)
	startBoundaries := newJavaTranslatedBoundaryCursor(analysis.lexed.input)
	endBoundaries := newJavaTranslatedBoundaryCursor(analysis.lexed.input)
	matchLines := javaMonotonicLineCursor{starts: analysis.lineStarts}
	frontierLines := javaMonotonicLineCursor{starts: analysis.lineStarts}

	nextLine := 1
	pendingLine := 0
	pendingAdjustment := 0
	var pendingColumns *javaOccurrenceColumnState
	if visitAt != nil {
		pendingColumns = &javaOccurrenceColumnState{}
	}
	visitorStopped := false
	emitThrough := func(lastLine int) bool {
		lastLine = min(lastLine, len(lines))
		for nextLine <= lastLine {
			adjustment := 0
			var removedColumns []int
			if pendingLine == nextLine {
				adjustment = pendingAdjustment
				if pendingColumns != nil {
					removedColumns = pendingColumns.removed
				}
				pendingLine, pendingAdjustment = 0, 0
			}
			var keepGoing bool
			if visitAt != nil {
				keepGoing = visitAt(nextLine, adjustment, nil, removedColumns)
			} else {
				keepGoing = visit(nextLine, adjustment)
			}
			if pendingLine == 0 && pendingColumns != nil {
				pendingColumns.removed = pendingColumns.removed[:0]
			}
			if !keepGoing {
				visitorStopped = true
				return false
			}
			nextLine++
		}
		return true
	}
	record := func(lineNo, column int) bool {
		if pendingLine == 0 {
			pendingLine, pendingAdjustment = lineNo, -1
			if pendingColumns != nil && column > 0 {
				pendingColumns.removed = append(pendingColumns.removed, column)
			}
			return true
		}
		if pendingLine != lineNo {
			if !emitThrough(lineNo-1) || pendingAdjustment != 0 {
				return false
			}
			pendingLine, pendingAdjustment = lineNo, -1
			if pendingColumns != nil && column > 0 {
				pendingColumns.removed = append(pendingColumns.removed, column)
			}
			return true
		}
		pendingAdjustment--
		if pendingColumns != nil && column > 0 {
			pendingColumns.removed = append(pendingColumns.removed, column)
		}
		return true
	}

	streamJavaLexicalEventsFromInput(
		analysis.lexed.input,
		javaLexicalStreamOptions{comments: true},
		func(event javaLexicalStreamEvent) bool {
			frontier := 0
			switch event.kind {
			case javaLexicalStreamOpaque, javaLexicalStreamComment:
				frontier = event.span.end
			case javaLexicalStreamToken:
				token := event.token
				frontier = token.end
				if token.numeric {
					recordFailed := false
					javaWalkRawSymbolOccurrencesInToken(
						analysis.source, symbol, prefix, token,
						&startBoundaries, &endBoundaries,
						func(start int) {
							lineNo := matchLines.lineAt(start)
							column := start - analysis.lineStarts[lineNo-1] + 1
							if !record(lineNo, column) {
								recordFailed = true
							}
						},
					)
					if recordFailed {
						return false
					}
				}
			}
			return emitThrough(frontierLines.lineAt(frontier) - 1)
		},
	)
	if visitorStopped {
		return true
	}
	_ = emitThrough(len(lines))
	return true
}

func javaUnqualifiedOccurrencePattern(symbol string) (javaToken, bool) {
	var pattern javaToken
	count := 0
	complete := streamJavaLexicalEvents(symbol, func(event javaLexicalStreamEvent) bool {
		if event.kind != javaLexicalStreamToken {
			return false
		}
		pattern = event.token
		count++
		return count <= 1
	})
	return pattern, complete && count == 1 && pattern.start == 0 &&
		pattern.end == len(symbol) && javaTokenIsSourceName(pattern)
}

func javaWalkRawSymbolOccurrencesInToken(
	source, symbol string,
	prefix []int,
	token javaToken,
	startBoundaries, endBoundaries *javaTranslatedBoundaryCursor,
	visit func(start int),
) int {
	if !token.numeric || symbol == "" || token.start < 0 || token.end > len(source) ||
		token.end <= token.start || len(prefix) < len(symbol) {
		return 0
	}
	count := 0
	matched := 0
	for offset := token.start; offset < token.end; offset++ {
		for matched > 0 && source[offset] != symbol[matched] {
			matched = prefix[matched-1]
		}
		if source[offset] != symbol[matched] {
			continue
		}
		matched++
		if matched != len(symbol) {
			continue
		}
		start := offset + 1 - len(symbol)
		end := offset + 1
		if javaQualifiedOccurrenceBoundaries(
			source, start, end, startBoundaries, endBoundaries,
		) {
			count++
			if visit != nil {
				visit(start)
			}
		}
		matched = prefix[matched-1]
	}
	return count
}

func javaQualifiedOccurrencePattern(symbol string) ([]javaToken, bool) {
	if !strings.Contains(symbol, ".") && !strings.Contains(symbol, `\u`) {
		return nil, false
	}
	tokens := make([]javaToken, 0, 8)
	complete := streamJavaLexicalEvents(symbol, func(event javaLexicalStreamEvent) bool {
		if event.kind != javaLexicalStreamToken {
			return false
		}
		tokens = append(tokens, event.token)
		return true
	})
	if !complete || len(tokens) < 3 || len(tokens)%2 == 0 ||
		tokens[0].start != 0 || tokens[len(tokens)-1].end != len(symbol) {
		return nil, false
	}
	for index, token := range tokens {
		if token.gap || index > 0 && tokens[index-1].end != token.start {
			return nil, false
		}
		if index%2 == 0 {
			if !javaTokenIsSourceName(token) {
				return nil, false
			}
			continue
		}
		if token.value != "." {
			return nil, false
		}
	}
	return tokens, true
}

func javaBuildQualifiedOccurrencePrefix(pattern []javaToken, prefix []int) {
	if len(prefix) < len(pattern) {
		return
	}
	for index, matched := 1, 0; index < len(pattern); index++ {
		for matched > 0 && !javaQualifiedOccurrenceTokenEqual(pattern[index], pattern[matched]) {
			matched = prefix[matched-1]
		}
		if javaQualifiedOccurrenceTokenEqual(pattern[index], pattern[matched]) {
			matched++
		}
		prefix[index] = matched
	}
}

func javaQualifiedOccurrenceTokenEqual(left, right javaToken) bool {
	if right.value == "." {
		return left.value == "."
	}
	return javaTokenIsSourceName(left) && left.text == right.text
}

func javaQualifiedOccurrenceBoundaries(
	source string,
	start, end int,
	starts, ends *javaTranslatedBoundaryCursor,
) bool {
	if start < 0 || end <= start || end > len(source) || starts == nil || ends == nil {
		return false
	}
	begin := starts.at(start)
	finish := ends.at(end)
	return begin.valid && finish.valid &&
		(start == 0 || !javaIdentifierContinueRune(begin.before)) &&
		(end == len(source) || !javaIdentifierContinueRune(finish.after))
}

// javaRawOccurrenceCursor runs byte KMP in parallel with the lexical stream.
// A completed token match is already counted by Find's raw line search exactly
// when the same raw symbol ends at the same byte. This proof is deterministic
// and constant-time per completed lexical match; it avoids both unsound span-
// length heuristics and repeated long substring comparisons.
type javaRawOccurrenceCursor struct {
	source       string
	pattern      string
	prefix       []int
	offset       int
	matched      int
	lastMatchEnd int
}

func newJavaRawOccurrenceCursor(source, pattern string) javaRawOccurrenceCursor {
	cursor := javaRawOccurrenceCursor{
		source: source, pattern: pattern, lastMatchEnd: -1,
	}
	cursor.prefix = make([]int, len(pattern))
	for index, matched := 1, 0; index < len(pattern); index++ {
		for matched > 0 && pattern[index] != pattern[matched] {
			matched = cursor.prefix[matched-1]
		}
		if pattern[index] == pattern[matched] {
			matched++
		}
		cursor.prefix[index] = matched
	}
	return cursor
}

func (cursor *javaRawOccurrenceCursor) advanceMatches(
	end int,
	visit func(start, end int),
) {
	if cursor == nil || len(cursor.pattern) == 0 {
		return
	}
	end = max(cursor.offset, min(end, len(cursor.source)))
	for cursor.offset < end {
		value := cursor.source[cursor.offset]
		for cursor.matched > 0 && value != cursor.pattern[cursor.matched] {
			cursor.matched = cursor.prefix[cursor.matched-1]
		}
		if value == cursor.pattern[cursor.matched] {
			cursor.matched++
		}
		cursor.offset++
		if cursor.matched == len(cursor.pattern) {
			cursor.lastMatchEnd = cursor.offset
			if visit != nil {
				visit(cursor.offset-len(cursor.pattern), cursor.offset)
			}
			cursor.matched = cursor.prefix[cursor.matched-1]
		}
	}
}

func (cursor *javaRawOccurrenceCursor) matchesAt(start, end int) bool {
	return cursor != nil && cursor.lastMatchEnd == end &&
		end-start == len(cursor.pattern)
}

type javaMonotonicLineCursor struct {
	starts []int
	index  int
	offset int
}

func (cursor *javaMonotonicLineCursor) lineAt(offset int) int {
	if cursor == nil || len(cursor.starts) == 0 {
		return 1
	}
	// Matcher frontiers and completed-match starts never move backwards. Keep
	// a defensive fallback for direct tests or future callers that violate that
	// contract without turning the normal stream into O(tokens log lines).
	if offset < cursor.offset {
		cursor.index = 0
	}
	cursor.offset = offset
	for cursor.index+1 < len(cursor.starts) &&
		cursor.starts[cursor.index+1] <= offset {
		cursor.index++
	}
	return cursor.index + 1
}
