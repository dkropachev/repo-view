package repoview

// walkAdditionalSymbolOccurrences reconciles physical-line searches with C's
// phase-2 line splicing. It streams the source instead of consuming the
// concrete-parser token retention window, so directive operands and the middle
// of very large files receive the same corrections as ordinary code.
func (c cLanguage) walkAdditionalSymbolOccurrences(
	lines []string,
	symbol string,
	visit func(lineNo, additionalCount int) bool,
) bool {
	return c.walkAdditionalSymbolOccurrencesWithVisitor(lines, symbol, visit, nil)
}

func (c cLanguage) walkAdditionalSymbolOccurrencesAt(
	lines []string,
	symbol string,
	visit func(
		lineNo, additionalCount int,
		addedColumns, removedColumns []int,
	) bool,
) bool {
	return c.walkAdditionalSymbolOccurrencesWithVisitor(lines, symbol, nil, visit)
}

func (c cLanguage) walkAdditionalSymbolOccurrencesWithVisitor(
	lines []string,
	symbol string,
	visit func(lineNo, additionalCount int) bool,
	visitAt func(
		lineNo, additionalCount int,
		addedColumns, removedColumns []int,
	) bool,
) bool {
	if visit == nil && visitAt == nil || !cSourceIdentifier(symbol) {
		return false
	}
	analysis := c.sourceAnalysis(lines)
	if analysis == nil || len(lines) == 0 {
		return true
	}
	nextLine := 1
	pendingAdjustment := 0
	var pendingAddedColumns []int
	var pendingRemovedColumns []int
	stopped := false
	emitThrough := func(lastLine int) bool {
		lastLine = min(lastLine, len(lines))
		for nextLine <= lastLine {
			var keepGoing bool
			if visitAt != nil {
				keepGoing = visitAt(
					nextLine, pendingAdjustment,
					pendingAddedColumns, pendingRemovedColumns,
				)
			} else {
				keepGoing = visit(nextLine, pendingAdjustment)
			}
			if !keepGoing {
				stopped = true
				return false
			}
			nextLine++
			pendingAdjustment = 0
			pendingAddedColumns = pendingAddedColumns[:0]
			pendingRemovedColumns = pendingRemovedColumns[:0]
		}
		return true
	}
	record := func(
		lineNo, adjustment, addedColumn int,
		removedColumns []int,
	) bool {
		if lineNo < nextLine {
			return true
		}
		if !emitThrough(lineNo - 1) {
			return false
		}
		pendingAdjustment += adjustment
		if addedColumn > 0 {
			pendingAddedColumns = append(pendingAddedColumns, addedColumn)
		}
		if len(removedColumns) > 0 {
			pendingRemovedColumns = append(pendingRemovedColumns, removedColumns...)
		}
		return true
	}

	lineCursor := 0
	lineAt := func(offset int) int {
		for lineCursor+1 < len(analysis.lineStarts) &&
			analysis.lineStarts[lineCursor+1] <= offset {
			lineCursor++
		}
		return lineCursor + 1
	}
	var removedColumnScratch []int
	recordToken := func(start, end int, text string, identifier bool) bool {
		if start < 0 || end <= start || end > len(analysis.source) {
			return true
		}
		if !cPhysicalRangeContainsNewline(analysis.source, start, end) {
			return true
		}
		firstLine := lineAt(start)
		for lineNo := firstLine; lineNo <= len(lines); lineNo++ {
			lineStart := max(start, analysis.lineStarts[lineNo-1])
			lineEnd := len(analysis.source)
			if lineNo < len(analysis.lineStarts) {
				lineEnd = analysis.lineStarts[lineNo]
				if lineEnd > lineStart && analysis.source[lineEnd-1] == '\n' {
					lineEnd--
				}
			}
			segmentEnd := min(end, lineEnd)
			adjustment := 0
			if visitAt != nil {
				removedColumnScratch = removedColumnScratch[:0]
			}
			if lineStart < segmentEnd {
				segment := analysis.source[lineStart:segmentEnd]
				if visitAt != nil {
					adjustment = -cWalkValidSymbolOccurrences(
						segment, symbol, func(start int) {
							removedColumnScratch = append(
								removedColumnScratch,
								lineStart-analysis.lineStarts[lineNo-1]+start+1,
							)
						},
					)
				} else {
					adjustment = -cCountValidSymbolOccurrences(segment, symbol)
				}
			}
			addedColumn := 0
			if identifier && lineNo == firstLine && text == symbol {
				adjustment++
				if visitAt != nil {
					addedColumn = start - analysis.lineStarts[firstLine-1] + 1
				}
			}
			if !record(lineNo, adjustment, addedColumn, removedColumnScratch) {
				return false
			}
			if end <= lineEnd || lineNo == len(lines) {
				break
			}
		}
		return true
	}

	commentIndex, stringIndex := 0, 0
	protectedEnd := func(offset int) int {
		for commentIndex < len(analysis.lexed.commentSpans) &&
			analysis.lexed.commentSpans[commentIndex].end <= offset {
			commentIndex++
		}
		for stringIndex < len(analysis.lexed.stringSpans) &&
			analysis.lexed.stringSpans[stringIndex].end <= offset {
			stringIndex++
		}
		end := offset
		if commentIndex < len(analysis.lexed.commentSpans) {
			span := analysis.lexed.commentSpans[commentIndex]
			if span.start <= offset && offset < span.end {
				end = max(end, span.end)
			}
		}
		if stringIndex < len(analysis.lexed.stringSpans) {
			span := analysis.lexed.stringSpans[stringIndex]
			if span.start <= offset && offset < span.end {
				end = max(end, span.end)
			}
		}
		return min(end, len(analysis.source))
	}

	for offset := 0; offset < len(analysis.source) && !stopped; {
		currentLine := lineAt(offset)
		if !emitThrough(currentLine - 1) {
			break
		}
		if end := protectedEnd(offset); end > offset {
			offset = end
			continue
		}
		if splice := cSpliceLength(analysis.source, offset); splice > 0 {
			offset += splice
			continue
		}
		if end := cLogicalIdentifierEnd(analysis.source, offset); end > offset {
			text := cLogicalText(analysis.source, offset, end)
			if !recordToken(offset, end, text, true) {
				break
			}
			offset = end
			continue
		}
		if cLogicalNumberStart(analysis.source, offset) {
			end := cLogicalNumberEnd(analysis.source, offset)
			if !recordToken(offset, end, "", false) {
				break
			}
			offset = end
			continue
		}
		_, end := cPunctuationAt(analysis.source, offset)
		if end <= offset {
			end = offset + 1
		}
		offset = min(end, len(analysis.source))
	}
	if !stopped {
		_ = emitThrough(len(lines))
	}
	return true
}

func cPhysicalRangeContainsNewline(source string, start, end int) bool {
	if start < 0 || end > len(source) || start >= end {
		return false
	}
	for offset := start; offset < end; offset++ {
		if source[offset] == '\n' || source[offset] == '\r' {
			return true
		}
	}
	return false
}

// cWalkLogicalCodeTokens streams preprocessing tokens outside comments. It
// emits literals and header names as adjacency barriers and intentionally
// includes directive operands and replacement lists, which remain searchable
// even though the concrete-parser resource preflight treats most of them as
// opaque.
func cWalkLogicalCodeTokens(
	analysis *cSourceAnalysis,
	proceed func(offset int) bool,
	visit func(cToken) bool,
) {
	if analysis == nil || visit == nil {
		return
	}
	commentIndex, stringIndex := 0, 0
	commentEnd := func(offset int) int {
		for commentIndex < len(analysis.lexed.commentSpans) &&
			analysis.lexed.commentSpans[commentIndex].end <= offset {
			commentIndex++
		}
		if commentIndex < len(analysis.lexed.commentSpans) {
			span := analysis.lexed.commentSpans[commentIndex]
			if span.start <= offset && offset < span.end {
				return min(span.end, len(analysis.source))
			}
		}
		return offset
	}
	stringEnd := func(offset int) int {
		for stringIndex < len(analysis.lexed.stringSpans) &&
			analysis.lexed.stringSpans[stringIndex].end <= offset {
			stringIndex++
		}
		if stringIndex < len(analysis.lexed.stringSpans) {
			span := analysis.lexed.stringSpans[stringIndex]
			if span.start <= offset && offset < span.end {
				return min(span.end, len(analysis.source))
			}
		}
		return offset
	}

	for offset := 0; offset < len(analysis.source); {
		if proceed != nil && !proceed(offset) {
			return
		}
		if end := commentEnd(offset); end > offset {
			offset = end
			continue
		}
		// Comments disappear into whitespace during translation, while string,
		// character, and header-name spans remain tokens. Preserve the latter as
		// adjacency barriers so an identifier before a literal cannot be mistaken
		// for a call or member expression.
		if end := stringEnd(offset); end > offset {
			if !visit(cToken{start: offset, end: end, kind: cTokenLiteral}) {
				return
			}
			offset = end
			continue
		}
		if splice := cSpliceLength(analysis.source, offset); splice > 0 {
			offset += splice
			continue
		}
		if offset == 0 && len(analysis.source) >= len("\uFEFF") &&
			analysis.source[:len("\uFEFF")] == "\uFEFF" {
			offset += len("\uFEFF")
			continue
		}
		switch analysis.source[offset] {
		case ' ', '\t', '\v', '\f', '\r', '\n':
			offset++
			continue
		}

		token := cToken{start: offset}
		if end := cLogicalIdentifierEnd(analysis.source, offset); end > offset {
			token.text = cLogicalText(analysis.source, offset, end)
			token.end = end
			token.kind = cTokenIdentifier
		} else if cLogicalNumberStart(analysis.source, offset) {
			token.end = cLogicalNumberEnd(analysis.source, offset)
			token.text = cLogicalText(analysis.source, offset, token.end)
			token.kind = cTokenNumber
		} else {
			token.text, token.end = cPunctuationAt(analysis.source, offset)
			token.kind = cTokenPunctuation
		}
		if token.end <= offset {
			token.end = offset + 1
		}
		token.end = min(token.end, len(analysis.source))
		if !visit(token) {
			return
		}
		offset = token.end
	}
}

func cStreamSymbolOnLine(
	analysis *cSourceAnalysis,
	lineNo int,
) (string, bool) {
	if analysis == nil || lineNo < 1 || lineNo > analysis.lineCount {
		return "", false
	}
	lineStart := analysis.lineStarts[lineNo-1]
	lineEnd := len(analysis.source)
	if lineNo < len(analysis.lineStarts) {
		lineEnd = analysis.lineStarts[lineNo]
	}
	firstSymbol, callSymbol, memberSymbol := "", "", ""
	previous := cToken{}
	previousTouches := false
	previousCandidate := false
	havePrevious := false
	directiveIndex := 0
	preparedDirective := -1
	directiveKeywordStart := -1

	cWalkLogicalCodeTokens(analysis, func(offset int) bool {
		// A trivia-only target needs no token beyond its physical end. One
		// lookahead token is needed only when an identifier touching the line
		// could become the preferred call candidate.
		return offset < lineEnd || previousTouches && previousCandidate
	}, func(token cToken) bool {
		touches := token.start < lineEnd && token.end > lineStart
		for directiveIndex < len(analysis.lexed.directives) &&
			analysis.lexed.directives[directiveIndex].end <= token.start {
			directiveIndex++
		}
		var directive *cDirective
		if directiveIndex < len(analysis.lexed.directives) {
			candidate := &analysis.lexed.directives[directiveIndex]
			if candidate.start <= token.start && token.start < candidate.end {
				directive = candidate
			}
		}
		if directive != nil && preparedDirective != directiveIndex {
			preparedDirective = directiveIndex
			directiveKeywordStart = -1
			if markerEnd, ok := cDirectiveMarkerEnd(
				analysis.source,
				directive.start,
			); ok {
				directiveKeywordStart = cDirectiveTriviaEnd(
					analysis.source,
					markerEnd,
					directive.end,
				)
			}
		}
		directiveKeyword := directive != nil && directive.kind != "" &&
			token.kind == cTokenIdentifier && token.text == directive.kind &&
			token.start == directiveKeywordStart
		definedOperator := directive != nil &&
			(directive.kind == "if" || directive.kind == "elif") &&
			token.kind == cTokenIdentifier && token.text == "defined"
		currentCandidate := token.kind == cTokenIdentifier &&
			cSourceIdentifier(token.text) && !cKeyword(token.text) &&
			!directiveKeyword && !definedOperator

		if havePrevious && previousTouches && token.text == "(" && callSymbol == "" &&
			previousCandidate {
			callSymbol = previous.text
		}
		if token.start >= lineEnd && !touches {
			return false
		}
		if touches && currentCandidate {
			if firstSymbol == "" {
				firstSymbol = token.text
			}
			if memberSymbol == "" && havePrevious &&
				(previous.text == "." || previous.text == "->") {
				memberSymbol = token.text
			}
		}
		previous = token
		previousTouches = touches
		previousCandidate = currentCandidate
		havePrevious = true
		return true
	})

	switch {
	case callSymbol != "":
		return callSymbol, true
	case memberSymbol != "":
		return memberSymbol, true
	case firstSymbol != "":
		return firstSymbol, true
	default:
		return "", false
	}
}
