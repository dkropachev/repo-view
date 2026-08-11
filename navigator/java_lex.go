package navigator

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	javaMaximumStoredLexicalTokens = 256 << 10
	// A source accepted by the concrete parser can have at most this many
	// lexical units, so its masks remain exact. Larger fallback-only sources
	// retain a conservative overflow range instead of one span per literal or
	// comment.
	javaMaximumStoredLexicalSpans     = javaMaximumConcreteTokens
	javaMaximumTemplateDepth          = 32
	javaMaximumTemplateRecoveryFrames = 4096
)

type javaByteSpan struct {
	start int
	end   int
}

// javaUnicodeInput records the first JLS lexical translation without changing
// source byte offsets. A cursor yields translated characters together with the
// raw byte range that produced each character, so recovery and masking can
// make decisions on the Java input stream while still reporting coordinates in
// the original file.
type javaUnicodeInput struct {
	source            string
	escapes           []int
	escapedLineStarts []int
}

type javaInputUnit struct {
	start   int
	end     int
	value   rune
	escaped bool
}

type javaInputCursor struct {
	input       *javaUnicodeInput
	offset      int
	limit       int
	escapeIndex int
}

type javaToken struct {
	text             string
	value            string
	start            int
	end              int
	identifier       bool
	numeric          bool
	gap              bool
	expressionResult bool
}

// javaLexicalStreamEventKind distinguishes searchable Java tokens from opaque
// literal fragments and optionally reported comments. Whitespace is always
// trivia. The default stream also keeps comments as trivia, so a consumer can
// bridge comments while resetting state at every literal boundary.
type javaLexicalStreamEventKind uint8

const (
	javaLexicalStreamToken javaLexicalStreamEventKind = iota
	javaLexicalStreamOpaque
	javaLexicalStreamComment
)

type javaLexicalStreamEvent struct {
	token javaToken
	span  javaByteSpan
	kind  javaLexicalStreamEventKind
}

// javaLexicalStreamSink returns false to stop scanning immediately. The
// scanner never retains events, tokens, or spans on behalf of the sink.
type javaLexicalStreamSink func(javaLexicalStreamEvent) bool

// javaLexicalStreamOptions controls optional trivia events for callers that
// need to reproduce search masks. Literal fragments are always reported as
// opaque events; comments remain trivia unless comments is set.
type javaLexicalStreamOptions struct {
	comments bool
}

type javaLexResult struct {
	input             *javaUnicodeInput
	tokens            []javaToken
	commentSpans      []javaByteSpan
	stringSpans       []javaByteSpan
	lexicalUnits      int
	truncated         bool
	translatedEscapes bool
}

type javaLexScanner struct {
	input           *javaUnicodeInput
	streamSink      javaLexicalStreamSink
	source          string
	tokens          []javaToken
	tailTokens      []javaToken
	recentTokens    []javaToken
	comments        []javaByteSpan
	strings         []javaByteSpan
	storeLimit      int
	countLimit      int
	tokenCount      int
	lexicalUnits    int
	depth           int
	tailNext        int
	countOnly       bool
	countStopped    bool
	streamOnly      bool
	streamStopped   bool
	streamComments  bool
	tokenOverflow   bool
	commentOverflow bool
	stringOverflow  bool
}

func newJavaUnicodeInput(source string) javaUnicodeInput {
	input := javaUnicodeInput{source: source}
	candidateCount := strings.Count(source, `\u`)
	if candidateCount == 0 {
		return input
	}
	input.escapes = make([]int, 0, candidateCount)

	backslashRun := 0
	previousTranslated := false
	for offset := 0; offset < len(source); {
		if source[offset] != '\\' {
			_, size := utf8.DecodeRuneInString(source[offset:])
			if size < 1 {
				size = 1
			}
			offset += size
			backslashRun = 0
			previousTranslated = false
			continue
		}

		eligible := previousTranslated || backslashRun%2 == 0
		if eligible {
			value, width, ok := javaUnicodeEscapeAt(source, offset, len(source))
			if ok {
				input.escapes = append(input.escapes, offset)
				if value == '\r' || value == '\n' {
					input.escapedLineStarts = append(
						input.escapedLineStarts, offset+width,
					)
				}
				offset += width
				previousTranslated = true
				if value == '\\' {
					backslashRun++
				} else {
					backslashRun = 0
				}
				continue
			}
		}

		offset++
		backslashRun++
		previousTranslated = false
	}
	return input
}

func (input *javaUnicodeInput) cursor(start, limit int) javaInputCursor {
	if input == nil {
		return javaInputCursor{}
	}
	start = max(0, min(start, len(input.source)))
	limit = max(start, min(limit, len(input.source)))
	escapeIndex := sort.Search(len(input.escapes), func(index int) bool {
		return input.escapes[index] >= start
	})
	return javaInputCursor{
		input:       input,
		offset:      start,
		limit:       limit,
		escapeIndex: escapeIndex,
	}
}

func (cursor *javaInputCursor) next() (javaInputUnit, bool) {
	if cursor == nil || cursor.input == nil || cursor.offset >= cursor.limit {
		return javaInputUnit{}, false
	}
	for cursor.escapeIndex < len(cursor.input.escapes) &&
		cursor.input.escapes[cursor.escapeIndex] < cursor.offset {
		cursor.escapeIndex++
	}
	if cursor.escapeIndex < len(cursor.input.escapes) {
		escapeStart := cursor.input.escapes[cursor.escapeIndex]
		value, width, valid := javaUnicodeEscapeAt(
			cursor.input.source, escapeStart, cursor.limit,
		)
		if valid && escapeStart == cursor.offset {
			cursor.escapeIndex++
			cursor.offset = escapeStart + width
			return javaInputUnit{
				value:   value,
				start:   escapeStart,
				end:     cursor.offset,
				escaped: true,
			}, true
		}
	}

	start := cursor.offset
	value, size := utf8.DecodeRuneInString(cursor.input.source[start:cursor.limit])
	if size < 1 {
		size = 1
	}
	cursor.offset = min(start+size, cursor.limit)
	return javaInputUnit{value: value, start: start, end: cursor.offset}, true
}

func (cursor *javaInputCursor) peek() (javaInputUnit, bool) {
	if cursor == nil {
		return javaInputUnit{}, false
	}
	probe := *cursor
	return probe.next()
}

func (cursor *javaInputCursor) nextCodePoint() (rune, int, int, bool, bool) {
	first, ok := cursor.next()
	if !ok {
		return 0, 0, 0, false, false
	}
	if first.value < 0xD800 || first.value > 0xDBFF {
		return first.value, first.start, first.end, first.escaped, true
	}

	probe := *cursor
	second, ok := probe.next()
	if !ok || second.value < 0xDC00 || second.value > 0xDFFF {
		return first.value, first.start, first.end, first.escaped, true
	}
	*cursor = probe
	value := rune(0x10000) + (first.value-rune(0xD800))*0x400 +
		(second.value - rune(0xDC00))
	return value, first.start, second.end, first.escaped || second.escaped, true
}

// javaTranslatedLinePrefixWhitespace reports whether the translated input
// between the latest logical line terminator and offset consists only of the
// horizontal whitespace permitted before an end-of-line documentation
// comment. Escaped line starts keep repeated checks linear even when many
// logical lines occupy one physical source line.
func javaTranslatedLinePrefixWhitespace(input *javaUnicodeInput, offset int) bool {
	if input == nil || offset < 0 || offset > len(input.source) {
		return false
	}
	lineStart := 0
	index := sort.Search(len(input.escapedLineStarts), func(index int) bool {
		return input.escapedLineStarts[index] > offset
	}) - 1
	if index >= 0 {
		lineStart = input.escapedLineStarts[index]
	}
	if rawBreak := strings.LastIndexAny(input.source[lineStart:offset], "\r\n"); rawBreak >= 0 {
		lineStart += rawBreak + 1
	}

	cursor := input.cursor(lineStart, offset)
	for {
		unit, ok := cursor.next()
		if !ok {
			return true
		}
		switch unit.value {
		case ' ', '\t', '\f':
		default:
			return false
		}
	}
}

func lexJava(source string) javaLexResult {
	input := newJavaUnicodeInput(source)
	scanner := &javaLexScanner{
		source:     source,
		input:      &input,
		storeLimit: javaMaximumStoredLexicalTokens,
	}
	scanner.scanRange(0, len(source))
	tokens := scanner.storedTokens()
	return javaLexResult{
		input:             &input,
		tokens:            tokens,
		commentSpans:      normalizeJavaSpans(scanner.comments),
		stringSpans:       normalizeJavaSpans(scanner.strings),
		lexicalUnits:      scanner.lexicalUnits,
		truncated:         scanner.tokenOverflow,
		translatedEscapes: len(input.escapes) > 0,
	}
}

func javaLexicalTokenCount(source string, limit int) int {
	input := newJavaUnicodeInput(source)
	scanner := &javaLexScanner{
		source:     source,
		input:      &input,
		countLimit: max(limit, 1),
		countOnly:  true,
	}
	scanner.scanRange(0, len(source))
	return scanner.lexicalUnits
}

// streamJavaLexicalEvents walks the complete translated Java input without
// the retained-token and retained-span limits used by lexJava. Token events
// are emitted in source order. String literals, character literals, text
// blocks, and each literal fragment of a string template emit opaque events;
// executable template expressions continue to emit their ordinary token and
// opaque events. Comments and whitespace are trivia and emit no event.
//
// The sink may stop the walk by returning false. The result is true only when
// the complete source was consumed. Apart from the Unicode-translation index
// shared with the ordinary lexer, scanner storage is bounded independently of
// source size.
func streamJavaLexicalEvents(source string, sink javaLexicalStreamSink) bool {
	input := newJavaUnicodeInput(source)
	return streamJavaLexicalEventsFromInput(
		&input, javaLexicalStreamOptions{}, sink,
	)
}

// streamJavaLexicalEventsFromInput reuses a prepared Unicode translation so a
// caller with javaLexResult.input does not preprocess the source again. It
// otherwise has the same ordered, non-retaining contract as
// streamJavaLexicalEvents. With options.comments, each exact lexical comment
// span is additionally emitted as javaLexicalStreamComment.
func streamJavaLexicalEventsFromInput(
	input *javaUnicodeInput,
	options javaLexicalStreamOptions,
	sink javaLexicalStreamSink,
) bool {
	if input == nil {
		return false
	}
	scanner := &javaLexScanner{
		source: input.source, input: input, streamOnly: true,
		streamComments: options.comments, streamSink: sink,
	}
	scanner.scanRange(0, len(input.source))
	return !scanner.streamStopped
}

func (s *javaLexScanner) scanRange(start, end int) {
	if s == nil || s.input == nil || s.countStopped || start < 0 || end < start ||
		end > len(s.source) {
		return
	}
	cursor := s.input.cursor(start, end)
	for !s.countStopped {
		unit, ok := cursor.peek()
		if !ok {
			return
		}
		if javaInputWhitespace(unit.value) {
			_, _ = cursor.next()
			continue
		}
		if unit.value == '/' {
			probe := cursor
			first, _ := probe.next()
			second, hasSecond := probe.peek()
			if hasSecond && second.value == '/' {
				_, _ = probe.next()
				spanEnd := second.end
				for {
					candidate, exists := probe.peek()
					if !exists || candidate.value == '\n' || candidate.value == '\r' {
						break
					}
					candidate, _ = probe.next()
					spanEnd = candidate.end
				}
				s.addComment(first.start, spanEnd)
				cursor = probe
				continue
			}
			if hasSecond && second.value == '*' {
				_, _ = probe.next()
				spanEnd := end
				for {
					candidate, exists := probe.next()
					if !exists {
						break
					}
					if candidate.value != '*' {
						continue
					}
					closing, exists := probe.peek()
					if exists && closing.value == '/' {
						closing, _ = probe.next()
						spanEnd = closing.end
						break
					}
				}
				s.addComment(first.start, spanEnd)
				cursor = probe
				continue
			}
		}
		if unit.value == '"' {
			template := s.javaTemplateLiteralAllowed()
			spanEnd, interpolated, closed := javaSimpleQuotedLiteralEnd(
				s.input, unit.start, end, '"', template,
			)
			if !interpolated {
				s.addString(unit.start, max(spanEnd, unit.end))
				if closed {
					s.markExpressionResult()
				} else {
					s.recentTokens = s.recentTokens[:0]
				}
				cursor = s.input.cursor(max(spanEnd, unit.end), end)
				continue
			}
			spanEnd, closed = javaStreamQuotedLiteralPartsAtDepth(
				s.input, unit.start, end, '"', template, s.depth+1, s,
			)
			if s.countStopped {
				return
			}
			if closed {
				s.markExpressionResult()
			} else {
				s.recentTokens = s.recentTokens[:0]
			}
			cursor = s.input.cursor(max(spanEnd, unit.end), end)
			continue
		}
		if unit.value == '\'' {
			spanEnd, _, closed := javaSimpleQuotedLiteralEnd(
				s.input, unit.start, end, '\'', false,
			)
			s.addString(unit.start, max(spanEnd, unit.end))
			if closed {
				s.markExpressionResult()
			} else {
				s.recentTokens = s.recentTokens[:0]
			}
			cursor = s.input.cursor(max(spanEnd, unit.end), end)
			continue
		}
		if token, ok := javaLogicalIdentifierToken(s.input, &cursor); ok {
			s.addToken(token)
			continue
		}
		if token, ok := javaLogicalNumericToken(s.input, &cursor); ok {
			s.addToken(token)
			continue
		}

		s.addToken(javaLogicalPunctuationToken(&cursor))
	}
}

func (s *javaLexScanner) consumeJavaQuotedLiteralPart(
	span javaByteSpan,
	expression bool,
) bool {
	if s == nil || s.countStopped {
		return false
	}
	if !expression || s.depth >= javaMaximumTemplateDepth {
		s.addString(span.start, span.end)
		return !s.countStopped
	}
	// A template expression starts a fresh token context. In particular, a
	// quote at its beginning must not inherit the outer processor and dot and
	// be mistaken for another template.
	s.recentTokens = s.recentTokens[:0]
	s.depth++
	s.scanRange(span.start, span.end)
	s.depth--
	return !s.countStopped
}

func (s *javaLexScanner) addToken(token javaToken) {
	if s == nil || s.countStopped {
		return
	}
	s.tokenCount++
	s.addLexicalUnit()
	if len(s.recentTokens) < 2 {
		s.recentTokens = append(s.recentTokens, token)
	} else {
		s.recentTokens[0], s.recentTokens[1] = s.recentTokens[1], token
	}
	if s.streamSink != nil && !s.streamSink(javaLexicalStreamEvent{
		kind: javaLexicalStreamToken, token: token,
	}) {
		s.streamStopped = true
		s.countStopped = true
		return
	}
	if s.streamOnly {
		return
	}
	if s.countOnly {
		return
	}
	firstLimit := max(s.storeLimit/2, 1)
	tailLimit := max(s.storeLimit-firstLimit, 0)
	if len(s.tokens) < firstLimit {
		s.tokens = append(s.tokens, token)
		return
	}
	if tailLimit == 0 {
		s.tokenOverflow = true
		return
	}
	if len(s.tailTokens) < tailLimit {
		s.tailTokens = append(s.tailTokens, token)
		return
	}
	s.tokenOverflow = true
	s.tailTokens[s.tailNext] = token
	s.tailNext = (s.tailNext + 1) % tailLimit
}

// markExpressionResult retains only syntactic context. It must never call
// addToken: the marker has no source coordinates and must not appear in the
// retained token list or lexical event stream. A following real dot can then
// recognize another template argument chained from the completed literal.
func (s *javaLexScanner) markExpressionResult() {
	if s == nil {
		return
	}
	marker := javaToken{expressionResult: true}
	if cap(s.recentTokens) == 0 {
		s.recentTokens = append(s.recentTokens, marker)
		return
	}
	s.recentTokens = s.recentTokens[:1]
	s.recentTokens[0] = marker
}

func (s *javaLexScanner) storedTokens() []javaToken {
	if s == nil || len(s.tailTokens) == 0 {
		return s.tokens
	}
	capacity := len(s.tokens) + len(s.tailTokens)
	if s.tokenOverflow {
		capacity++
	}
	result := make([]javaToken, 0, capacity)
	result = append(result, s.tokens...)
	if !s.tokenOverflow {
		return append(result, s.tailTokens...)
	}
	earliest := s.tailTokens[s.tailNext].start
	result = append(result, javaToken{
		text: ";", value: ";", start: earliest, end: earliest, gap: true,
	})
	result = append(result, s.tailTokens[s.tailNext:]...)
	result = append(result, s.tailTokens[:s.tailNext]...)
	return result
}

func (s *javaLexScanner) addComment(start, end int) {
	if s == nil || start < 0 || end <= start || end > len(s.source) {
		return
	}
	s.addLexicalUnit()
	if s.streamComments && s.streamSink != nil && !s.streamSink(javaLexicalStreamEvent{
		kind: javaLexicalStreamComment,
		span: javaByteSpan{start: start, end: end},
	}) {
		s.streamStopped = true
		s.countStopped = true
		return
	}
	if s.countOnly || s.streamOnly {
		return
	}
	s.addStoredSpan(&s.comments, &s.commentOverflow, start, end, false)
}

func (s *javaLexScanner) addString(start, end int) {
	if s == nil || start < 0 || end <= start || end > len(s.source) {
		return
	}
	s.addLexicalUnit()
	if s.streamSink != nil && !s.streamSink(javaLexicalStreamEvent{
		kind: javaLexicalStreamOpaque,
		span: javaByteSpan{start: start, end: end},
	}) {
		s.streamStopped = true
		s.countStopped = true
		return
	}
	if s.streamOnly {
		return
	}
	if s.countOnly {
		return
	}
	s.addStoredSpan(&s.strings, &s.stringOverflow, start, end, true)
}

func (s *javaLexScanner) addStoredSpan(
	spans *[]javaByteSpan,
	overflow *bool,
	start, end int,
	coalesceInvariantGaps bool,
) {
	if s == nil || spans == nil || overflow == nil || start < 0 || end <= start ||
		end > len(s.source) {
		return
	}
	stored := *spans
	if *overflow {
		last := &stored[len(stored)-1]
		last.start = min(last.start, start)
		last.end = max(last.end, end)
		return
	}
	if len(stored) > 0 {
		last := &stored[len(stored)-1]
		overlapsOrAdjacent := start <= last.end && end >= last.start
		invariantGap := coalesceInvariantGaps && start >= last.end &&
			javaMaskInvariantGap(s.source, last.end, start)
		if overlapsOrAdjacent || invariantGap {
			last.start = min(last.start, start)
			last.end = max(last.end, end)
			return
		}
	}
	if len(stored) < javaMaximumStoredLexicalSpans {
		*spans = append(*spans, javaByteSpan{start: start, end: end})
		return
	}

	// Once exact storage is exhausted, retain a single conservative range.
	// Every concrete-parser input is below this threshold, so only bounded
	// lexical fallback can hide executable gaps between pathological numbers of
	// opaque regions.
	last := &stored[len(stored)-1]
	last.start = min(last.start, start)
	last.end = max(last.end, end)
	*overflow = true
}

func javaMaskInvariantGap(source string, start, end int) bool {
	if start < 0 || end < start || end > len(source) {
		return false
	}
	for offset := start; offset < end; offset++ {
		switch source[offset] {
		case ' ', '\n', '\r':
		default:
			return false
		}
	}
	return true
}

func (s *javaLexScanner) addLexicalUnit() {
	if s == nil || s.countStopped {
		return
	}
	s.lexicalUnits++
	if s.countOnly && s.lexicalUnits >= s.countLimit {
		s.countStopped = true
	}
}

func (s *javaLexScanner) javaTemplateLiteralAllowed() bool {
	if s == nil || len(s.recentTokens) < 2 ||
		s.recentTokens[len(s.recentTokens)-1].text != "." {
		return false
	}
	processor := s.recentTokens[len(s.recentTokens)-2]
	return processor.expressionResult || processor.identifier ||
		processor.text == ")" || processor.text == "]" || processor.text == "}"
}

// javaSimpleQuotedLiteralEnd scans an ordinary string, character literal, or
// text block without constructing result slices. For a possible template it
// stops at the first real interpolation so the full splitter can handle the
// executable regions; processor literals without interpolation keep the fast
// path.
func javaSimpleQuotedLiteralEnd(
	input *javaUnicodeInput,
	start, limit int,
	quote rune,
	detectTemplate bool,
) (end int, interpolated, closed bool) {
	if input == nil || start < 0 || start >= limit || limit > len(input.source) {
		return start, false, false
	}
	cursor := input.cursor(start, limit)
	opening, ok := cursor.next()
	if !ok || opening.value != quote {
		return start, false, false
	}

	triple := false
	if quote == '"' {
		cursor, triple = javaTextBlockOpening(cursor)
	}

	backslashRun := 0
	for {
		unit, exists := cursor.next()
		if !exists {
			return limit, false, false
		}
		if detectTemplate && unit.value == '\\' && backslashRun%2 == 0 {
			openingBrace, braceOK := cursor.peek()
			if braceOK && openingBrace.value == '{' {
				return unit.start, true, false
			}
		}

		if triple {
			if unit.value == quote && backslashRun%2 == 0 {
				probe := cursor
				second, secondOK := probe.next()
				third, thirdOK := probe.next()
				if secondOK && thirdOK && second.value == quote && third.value == quote {
					return third.end, false, true
				}
			}
		} else {
			if unit.value == quote && backslashRun%2 == 0 {
				return unit.end, false, true
			}
			if unit.value == '\n' || unit.value == '\r' {
				return unit.start, false, false
			}
		}
		if unit.value == '\\' {
			backslashRun++
		} else {
			backslashRun = 0
		}
	}
}

// javaQuotedLiteralPartSink consumes each opaque fragment or executable
// interpolation before the splitter searches for the next part. Returning
// false lets bounded count-only scans stop without visiting the literal tail.
type javaQuotedLiteralPartSink interface {
	consumeJavaQuotedLiteralPart(span javaByteSpan, expression bool) bool
}

func javaStreamQuotedLiteralParts(
	input *javaUnicodeInput,
	start, limit int,
	quote rune,
	template bool,
	sink javaQuotedLiteralPartSink,
) int {
	end, _ := javaStreamQuotedLiteralPartsAtDepth(
		input, start, limit, quote, template, 1, sink,
	)
	return end
}

func javaStreamQuotedLiteralPartsAtDepth(
	input *javaUnicodeInput,
	start, limit int,
	quote rune,
	template bool,
	templateDepth int,
	sink javaQuotedLiteralPartSink,
) (end int, closed bool) {
	if input == nil || start < 0 || start >= limit || limit > len(input.source) {
		return start, false
	}
	if template && templateDepth > javaMaximumTemplateDepth {
		recoveryEnd, recovered := javaTemplateRecoveryBoundaryEnd(
			input, start, limit,
		)
		javaYieldQuotedLiteralPart(sink, start, recoveryEnd, false)
		return recoveryEnd, recovered
	}
	cursor := input.cursor(start, limit)
	opening, ok := cursor.next()
	if !ok || opening.value != quote {
		return start, false
	}
	triple := false
	if quote == '"' {
		cursor, triple = javaTextBlockOpening(cursor)
	}

	literalStart := start
	backslashRun := 0
	for {
		unit, exists := cursor.next()
		if !exists {
			javaYieldQuotedLiteralPart(sink, literalStart, limit, false)
			return limit, false
		}

		if template && unit.value == '\\' && backslashRun%2 == 0 {
			probe := cursor
			openingBrace, braceOK := probe.next()
			if braceOK && openingBrace.value == '{' {
				if !javaYieldQuotedLiteralPart(
					sink, literalStart, openingBrace.end, false,
				) {
					return openingBrace.end, false
				}
				expressionStart := openingBrace.end
				expressionEnd, closingEnd, found := javaTemplateExpressionEndAtDepth(
					input, expressionStart, limit, templateDepth,
				)
				if !found {
					// Once recursion reaches its configured limit, mask through a
					// safe physical-line boundary. This keeps the unclassified nested
					// expression opaque without swallowing unrelated later code.
					recoveryEnd := max(expressionStart, min(closingEnd, limit))
					javaYieldQuotedLiteralPart(
						sink, expressionStart, recoveryEnd, false,
					)
					return recoveryEnd, false
				}
				if !javaYieldQuotedLiteralPart(
					sink, expressionStart, expressionEnd, true,
				) {
					return expressionEnd, false
				}
				literalStart = expressionEnd
				cursor = input.cursor(closingEnd, limit)
				backslashRun = 0
				continue
			}
		}

		if triple {
			if unit.value == quote && backslashRun%2 == 0 {
				probe := cursor
				second, secondOK := probe.next()
				third, thirdOK := probe.next()
				if secondOK && thirdOK && second.value == quote && third.value == quote {
					javaYieldQuotedLiteralPart(sink, literalStart, third.end, false)
					return third.end, true
				}
			}
		} else {
			if unit.value == quote && backslashRun%2 == 0 {
				javaYieldQuotedLiteralPart(sink, literalStart, unit.end, false)
				return unit.end, true
			}
			if unit.value == '\n' || unit.value == '\r' {
				javaYieldQuotedLiteralPart(sink, literalStart, unit.start, false)
				return unit.start, false
			}
		}
		if unit.value == '\\' {
			backslashRun++
		} else {
			backslashRun = 0
		}
	}
}

func javaYieldQuotedLiteralPart(
	sink javaQuotedLiteralPartSink,
	start, end int,
	expression bool,
) bool {
	if end <= start || sink == nil {
		return true
	}
	return sink.consumeJavaQuotedLiteralPart(
		javaByteSpan{start: start, end: end}, expression,
	)
}

// javaTextBlockOpening recognizes the two quotes after an already-consumed
// opening quote only when the JLS text-block delimiter is followed by optional
// horizontal whitespace and a line terminator. On failure it leaves the
// cursor unchanged so the first two quotes form an ordinary empty string and
// the remaining quote is scanned independently.
func javaTextBlockOpening(cursor javaInputCursor) (javaInputCursor, bool) {
	probe := cursor
	second, secondOK := probe.next()
	third, thirdOK := probe.next()
	if !secondOK || !thirdOK || second.value != '"' || third.value != '"' {
		return cursor, false
	}
	lookahead := probe
	for {
		unit, ok := lookahead.next()
		if !ok {
			return cursor, false
		}
		switch unit.value {
		case ' ', '\t', '\f':
			continue
		case '\n', '\r':
			return probe, true
		default:
			return cursor, false
		}
	}
}

type javaTemplateRecoveryFrameKind uint8

const (
	javaTemplateRecoveryLiteral javaTemplateRecoveryFrameKind = iota
	javaTemplateRecoveryExpression
)

// javaTemplateRecoveryFrame is the explicit, bounded replacement for deeper
// recursive template scans. Literal frames remember only quote state;
// expression frames remember their brace balance and the two-token context
// needed to recognize another nested template processor.
type javaTemplateRecoveryFrame struct {
	recent       javaRecentTokenPair
	braceDepth   int
	backslashRun int
	kind         javaTemplateRecoveryFrameKind
	triple       bool
}

// javaTemplateRecoveryBoundaryEnd finds the closing quote of one template
// after the precise recursive scanner reaches its configured depth. It walks
// the source once with an explicit capped stack, understands ordinary and
// text-block templates, comments, literals, and interpolation braces, and
// deliberately treats the complete recovered template as opaque. That keeps
// deep executable contents conservative without losing same-line code after
// a balanced template or resuming inside an outer text-block tail.
func javaTemplateRecoveryBoundaryEnd(
	input *javaUnicodeInput,
	start, limit int,
) (int, bool) {
	if input == nil || start < 0 || start >= limit || limit > len(input.source) {
		return limit, false
	}
	cursor := input.cursor(start, limit)
	opening, ok := cursor.next()
	if !ok || opening.value != '"' {
		return limit, false
	}
	cursor, triple := javaTextBlockOpening(cursor)
	frames := make([]javaTemplateRecoveryFrame, 1, javaMaximumTemplateDepth+1)
	frames[0] = javaTemplateRecoveryFrame{
		kind: javaTemplateRecoveryLiteral, triple: triple,
	}

	push := func(frame javaTemplateRecoveryFrame) bool {
		if len(frames) >= javaMaximumTemplateRecoveryFrames {
			return false
		}
		frames = append(frames, frame)
		return true
	}
	closeLiteral := func(end int) (int, bool, bool) {
		frames = frames[:len(frames)-1]
		if len(frames) == 0 {
			return end, true, true
		}
		parent := &frames[len(frames)-1]
		if parent.kind != javaTemplateRecoveryExpression {
			return limit, false, true
		}
		parent.recent.markExpressionResult()
		return 0, false, false
	}

	for len(frames) > 0 {
		frame := &frames[len(frames)-1]
		if frame.kind == javaTemplateRecoveryLiteral {
			unit, exists := cursor.next()
			if !exists {
				return limit, false
			}
			if unit.value == '\\' && frame.backslashRun%2 == 0 {
				braceProbe := cursor
				openingBrace, braceOK := braceProbe.next()
				if braceOK && openingBrace.value == '{' {
					frame.backslashRun = 0
					if !push(javaTemplateRecoveryFrame{
						kind: javaTemplateRecoveryExpression, braceDepth: 1,
					}) {
						return limit, false
					}
					cursor = braceProbe
					continue
				}
			}

			if frame.triple {
				if unit.value == '"' && frame.backslashRun%2 == 0 {
					closingProbe := cursor
					second, secondOK := closingProbe.next()
					third, thirdOK := closingProbe.next()
					if secondOK && thirdOK && second.value == '"' && third.value == '"' {
						cursor = closingProbe
						if end, found, done := closeLiteral(third.end); done {
							return end, found
						}
						continue
					}
				}
			} else {
				if unit.value == '"' && frame.backslashRun%2 == 0 {
					if end, found, done := closeLiteral(unit.end); done {
						return end, found
					}
					continue
				}
				if unit.value == '\n' || unit.value == '\r' {
					return unit.start, false
				}
			}
			if unit.value == '\\' {
				frame.backslashRun++
			} else {
				frame.backslashRun = 0
			}
			continue
		}

		unit, exists := cursor.peek()
		if !exists {
			return limit, false
		}
		if javaInputWhitespace(unit.value) {
			_, _ = cursor.next()
			continue
		}
		if unit.value == '"' || unit.value == '\'' {
			template := unit.value == '"' && frame.recent.templateLiteralAllowed()
			if template {
				_, _ = cursor.next()
				var nestedTriple bool
				cursor, nestedTriple = javaTextBlockOpening(cursor)
				if !push(javaTemplateRecoveryFrame{
					kind: javaTemplateRecoveryLiteral, triple: nestedTriple,
				}) {
					return limit, false
				}
				continue
			}
			end, _, closed := javaSimpleQuotedLiteralEnd(
				input, unit.start, limit, unit.value, false,
			)
			cursor = input.cursor(max(end, unit.end), limit)
			if closed {
				frame.recent.markExpressionResult()
			} else {
				frame.recent.count = 0
			}
			continue
		}
		if unit.value == '/' {
			probe := cursor
			_, _ = probe.next()
			next, hasNext := probe.next()
			if hasNext && next.value == '/' {
				for {
					candidate, more := probe.peek()
					if !more || candidate.value == '\n' || candidate.value == '\r' {
						break
					}
					_, _ = probe.next()
				}
				cursor = probe
				continue
			}
			if hasNext && next.value == '*' {
				for {
					candidate, more := probe.next()
					if !more {
						break
					}
					if candidate.value != '*' {
						continue
					}
					closing, more := probe.peek()
					if more && closing.value == '/' {
						_, _ = probe.next()
						break
					}
				}
				cursor = probe
				continue
			}
		}
		if token, identifier := javaLogicalIdentifierToken(input, &cursor); identifier {
			frame.recent.add(token)
			continue
		}
		if token, numeric := javaLogicalNumericToken(input, &cursor); numeric {
			frame.recent.add(token)
			continue
		}

		token := javaLogicalPunctuationToken(&cursor)
		frame.recent.add(token)
		switch token.value {
		case "{":
			frame.braceDepth++
		case "}":
			frame.braceDepth--
			if frame.braceDepth == 0 {
				frames = frames[:len(frames)-1]
				if len(frames) == 0 ||
					frames[len(frames)-1].kind != javaTemplateRecoveryLiteral {
					return limit, false
				}
				frames[len(frames)-1].backslashRun = 0
			}
		}
	}
	return limit, false
}

type javaRecentTokenPair struct {
	tokens [2]javaToken
	count  int
}

func (recent *javaRecentTokenPair) add(token javaToken) {
	if recent == nil {
		return
	}
	if recent.count < len(recent.tokens) {
		recent.tokens[recent.count] = token
		recent.count++
		return
	}
	recent.tokens[0], recent.tokens[1] = recent.tokens[1], token
}

func (recent *javaRecentTokenPair) markExpressionResult() {
	if recent == nil {
		return
	}
	recent.tokens[0] = javaToken{expressionResult: true}
	recent.count = 1
}

func (recent *javaRecentTokenPair) templateLiteralAllowed() bool {
	if recent == nil || recent.count < 2 || recent.tokens[1].value != "." {
		return false
	}
	processor := recent.tokens[0]
	return processor.expressionResult || processor.identifier ||
		processor.value == ")" || processor.value == "]" || processor.value == "}"
}

// javaTemplateExpressionEndAtDepth finds the brace that closes one template
// interpolation. Unlike an ordinary brace matcher, it understands nested
// templates and their executable interpolations, so a quote inside an inner
// expression cannot be mistaken for the end of that nested template. The
// configured template-depth cap bounds recursion; exceeding it makes the
// caller conservatively mask the unclassified remainder.
func javaTemplateExpressionEndAtDepth(
	input *javaUnicodeInput,
	start, limit, templateDepth int,
) (int, int, bool) {
	if input == nil || start < 0 || start > limit || limit > len(input.source) {
		return limit, limit, false
	}
	if templateDepth < 1 || templateDepth > javaMaximumTemplateDepth {
		return limit, limit, false
	}
	depth := 1
	var recent javaRecentTokenPair
	cursor := input.cursor(start, limit)
	for {
		unit, ok := cursor.peek()
		if !ok {
			return limit, limit, false
		}
		if javaInputWhitespace(unit.value) {
			_, _ = cursor.next()
			continue
		}
		if unit.value == '"' || unit.value == '\'' {
			template := unit.value == '"' && recent.templateLiteralAllowed()
			closed := true
			if template {
				end, found := javaTemplateLiteralBoundaryEnd(
					input, unit.start, limit, templateDepth+1,
				)
				if !found {
					return end, end, false
				}
				cursor = input.cursor(max(end, unit.end), limit)
			} else {
				end, _, literalClosed := javaSimpleQuotedLiteralEnd(
					input, unit.start, limit, unit.value, false,
				)
				closed = literalClosed
				cursor = input.cursor(max(end, unit.end), limit)
			}
			if closed {
				recent.markExpressionResult()
			} else {
				recent.count = 0
			}
			continue
		}
		if unit.value == '/' {
			probe := cursor
			_, _ = probe.next()
			next, exists := probe.next()
			if exists && next.value == '/' {
				for {
					candidate, more := probe.peek()
					if !more || candidate.value == '\n' || candidate.value == '\r' {
						break
					}
					_, _ = probe.next()
				}
				cursor = probe
				continue
			}
			if exists && next.value == '*' {
				for {
					candidate, more := probe.next()
					if !more {
						break
					}
					if candidate.value != '*' {
						continue
					}
					closing, more := probe.peek()
					if more && closing.value == '/' {
						_, _ = probe.next()
						break
					}
				}
				cursor = probe
				continue
			}
		}
		if token, identifier := javaLogicalIdentifierToken(input, &cursor); identifier {
			recent.add(token)
			continue
		}

		token := javaLogicalPunctuationToken(&cursor)
		recent.add(token)
		switch token.value {
		case "{":
			depth++
		case "}":
			depth--
			if depth == 0 {
				return token.start, token.end, true
			}
		}
	}
}

// javaTemplateLiteralBoundaryEnd skips one nested template while an enclosing
// interpolation is being matched. It does not allocate fragments or tokens;
// it only validates enough structure to locate the template's closing quote.
func javaTemplateLiteralBoundaryEnd(
	input *javaUnicodeInput,
	start, limit, templateDepth int,
) (int, bool) {
	if input == nil || start < 0 || start >= limit || limit > len(input.source) {
		return limit, false
	}
	if templateDepth > javaMaximumTemplateDepth {
		return javaTemplateRecoveryBoundaryEnd(input, start, limit)
	}
	cursor := input.cursor(start, limit)
	opening, ok := cursor.next()
	if !ok || opening.value != '"' {
		return limit, false
	}

	cursor, triple := javaTextBlockOpening(cursor)

	backslashRun := 0
	for {
		unit, exists := cursor.next()
		if !exists {
			return limit, false
		}
		if unit.value == '\\' && backslashRun%2 == 0 {
			braceProbe := cursor
			openingBrace, braceOK := braceProbe.next()
			if braceOK && openingBrace.value == '{' {
				_, closingEnd, found := javaTemplateExpressionEndAtDepth(
					input, openingBrace.end, limit, templateDepth,
				)
				if !found {
					return closingEnd, false
				}
				cursor = input.cursor(closingEnd, limit)
				backslashRun = 0
				continue
			}
		}

		if triple {
			if unit.value == '"' && backslashRun%2 == 0 {
				closingProbe := cursor
				second, secondOK := closingProbe.next()
				third, thirdOK := closingProbe.next()
				if secondOK && thirdOK && second.value == '"' && third.value == '"' {
					return third.end, true
				}
			}
		} else {
			if unit.value == '"' && backslashRun%2 == 0 {
				return unit.end, true
			}
			if unit.value == '\n' || unit.value == '\r' {
				return unit.start, false
			}
		}
		if unit.value == '\\' {
			backslashRun++
		} else {
			backslashRun = 0
		}
	}
}

// javaLogicalNumericToken recognizes one complete JLS integer or floating-
// point literal on the translated input stream. Numeric literals are emitted
// as ordinary non-identifier tokens by the full lexical stream. Keeping the
// literal atomic prevents qualified-name matching from entering hexadecimal
// significands or exponents while leaving a following member dot untouched.
func javaLogicalNumericToken(
	input *javaUnicodeInput,
	cursor *javaInputCursor,
) (javaToken, bool) {
	if input == nil || cursor == nil {
		return javaToken{}, false
	}
	startCursor := *cursor
	first, ok := startCursor.peek()
	if !ok {
		return javaToken{}, false
	}

	best := startCursor
	switch {
	case first.value == '.':
		afterDot := startCursor
		_, _ = afterDot.next()
		digits, hasDigits := javaNumericDigits(afterDot, 10)
		if !hasDigits {
			return javaToken{}, false
		}
		best = javaNumericFloatingTail(digits, 'e', 'E')
	case first.value >= '0' && first.value <= '9':
		afterFirst := startCursor
		first, _ = afterFirst.next()
		decimalDigits, _ := javaNumericDigits(startCursor, 10)

		// Decimal floating-point forms beginning with Digits.
		if afterDot, hasDot := javaNumericConsume(decimalDigits, '.'); hasDot {
			fractionEnd := afterDot
			if fraction, hasFraction := javaNumericDigits(afterDot, 10); hasFraction {
				fractionEnd = fraction
			}
			best = javaFurtherNumericCursor(
				best, javaNumericFloatingTail(fractionEnd, 'e', 'E'),
			)
		}
		if exponent, hasExponent := javaNumericExponent(decimalDigits, 'e', 'E'); hasExponent {
			best = javaFurtherNumericCursor(
				best, javaNumericOptionalSuffix(exponent, "fFdD"),
			)
		}
		if suffixed, hasSuffix := javaNumericSuffix(decimalDigits, "fFdD"); hasSuffix {
			best = javaFurtherNumericCursor(best, suffixed)
		}

		// Integer forms. A leading zero is a complete decimal numeral by
		// itself, and may also begin an octal numeral whose underscores are
		// separators after that initial digit.
		integerEnd := afterFirst
		if first.value != '0' {
			integerEnd = decimalDigits
		} else if octal, hasOctal := javaNumericOctalTail(afterFirst); hasOctal {
			integerEnd = octal
		}
		best = javaFurtherNumericCursor(
			best, javaNumericOptionalSuffix(integerEnd, "lL"),
		)

		if first.value == '0' {
			if hexadecimal, valid := javaHexadecimalNumericToken(afterFirst); valid {
				best = javaFurtherNumericCursor(best, hexadecimal)
			}
			if binary, valid := javaBinaryNumericToken(afterFirst); valid {
				best = javaFurtherNumericCursor(best, binary)
			}
		}
	default:
		return javaToken{}, false
	}

	if best.offset <= startCursor.offset {
		return javaToken{}, false
	}
	*cursor = best
	raw := input.source[startCursor.offset:best.offset]
	return javaToken{
		text:    raw,
		value:   javaTranslatedSourceText(input, startCursor.offset, best.offset),
		start:   startCursor.offset,
		end:     best.offset,
		numeric: true,
	}, true
}

func javaHexadecimalNumericToken(afterZero javaInputCursor) (javaInputCursor, bool) {
	afterPrefix, hasPrefix := javaNumericConsumeEither(afterZero, 'x', 'X')
	if !hasPrefix {
		return afterZero, false
	}
	hexDigits, hasHexDigits := javaNumericDigits(afterPrefix, 16)
	best := afterZero
	if hasHexDigits {
		best = javaNumericOptionalSuffix(hexDigits, "lL")
	}

	significand := hexDigits
	validSignificand := hasHexDigits
	if dotEnd, hasDot := javaNumericConsume(hexDigits, '.'); hasDot {
		significand = dotEnd
		if fraction, hasFraction := javaNumericDigits(dotEnd, 16); hasFraction {
			significand = fraction
			validSignificand = true
		} else if !hasHexDigits {
			validSignificand = false
		}
	}
	if validSignificand {
		if exponent, hasExponent := javaNumericExponent(significand, 'p', 'P'); hasExponent {
			best = javaFurtherNumericCursor(
				best, javaNumericOptionalSuffix(exponent, "fFdD"),
			)
		}
	}
	return best, best.offset > afterZero.offset
}

func javaBinaryNumericToken(afterZero javaInputCursor) (javaInputCursor, bool) {
	afterPrefix, hasPrefix := javaNumericConsumeEither(afterZero, 'b', 'B')
	if !hasPrefix {
		return afterZero, false
	}
	digits, hasDigits := javaNumericDigits(afterPrefix, 2)
	if !hasDigits {
		return afterZero, false
	}
	return javaNumericOptionalSuffix(digits, "lL"), true
}

func javaNumericOctalTail(afterZero javaInputCursor) (javaInputCursor, bool) {
	probe := afterZero
	for {
		next := probe
		unit, ok := next.next()
		if !ok || unit.value != '_' {
			break
		}
		probe = next
	}
	return javaNumericDigits(probe, 8)
}

// javaNumericDigits implements the JLS Digits/HexDigits/OctalDigits/
// BinaryDigits shape: a digit is required at each end, with arbitrary runs of
// underscores permitted only between digits.
func javaNumericDigits(
	start javaInputCursor,
	radix int,
) (javaInputCursor, bool) {
	first := start
	unit, ok := first.next()
	if !ok || !javaNumericDigit(unit.value, radix) {
		return start, false
	}
	result := first
	for {
		probe := result
		unit, ok = probe.next()
		if !ok {
			break
		}
		if javaNumericDigit(unit.value, radix) {
			result = probe
			continue
		}
		if unit.value != '_' {
			break
		}
		continued := false
		for {
			afterUnderscore := probe
			unit, ok = afterUnderscore.next()
			if !ok {
				return result, true
			}
			if unit.value == '_' {
				probe = afterUnderscore
				continue
			}
			if javaNumericDigit(unit.value, radix) {
				result = afterUnderscore
				continued = true
			}
			break
		}
		if continued {
			continue
		}
		break
	}
	return result, true
}

func javaNumericDigit(value rune, radix int) bool {
	switch {
	case value >= '0' && value <= '1':
		return radix >= 2
	case value >= '2' && value <= '7':
		return radix >= 8
	case value >= '8' && value <= '9':
		return radix >= 10
	case value >= 'a' && value <= 'f', value >= 'A' && value <= 'F':
		return radix >= 16
	default:
		return false
	}
}

func javaNumericFloatingTail(
	start javaInputCursor,
	firstIndicator, secondIndicator rune,
) javaInputCursor {
	result := start
	if exponent, ok := javaNumericExponent(
		start, firstIndicator, secondIndicator,
	); ok {
		result = exponent
	}
	return javaNumericOptionalSuffix(result, "fFdD")
}

func javaNumericExponent(
	start javaInputCursor,
	firstIndicator, secondIndicator rune,
) (javaInputCursor, bool) {
	afterIndicator, ok := javaNumericConsumeEither(
		start, firstIndicator, secondIndicator,
	)
	if !ok {
		return start, false
	}
	afterSign := afterIndicator
	if signed, hasSign := javaNumericConsumeEither(afterIndicator, '+', '-'); hasSign {
		afterSign = signed
	}
	digits, hasDigits := javaNumericDigits(afterSign, 10)
	if !hasDigits {
		return start, false
	}
	return digits, true
}

func javaNumericOptionalSuffix(start javaInputCursor, suffixes string) javaInputCursor {
	if suffixed, ok := javaNumericSuffix(start, suffixes); ok {
		return suffixed
	}
	return start
}

func javaNumericSuffix(
	start javaInputCursor,
	suffixes string,
) (javaInputCursor, bool) {
	probe := start
	unit, ok := probe.next()
	if !ok || !strings.ContainsRune(suffixes, unit.value) {
		return start, false
	}
	return probe, true
}

func javaNumericConsume(start javaInputCursor, value rune) (javaInputCursor, bool) {
	probe := start
	unit, ok := probe.next()
	if !ok || unit.value != value {
		return start, false
	}
	return probe, true
}

func javaNumericConsumeEither(
	start javaInputCursor,
	first, second rune,
) (javaInputCursor, bool) {
	probe := start
	unit, ok := probe.next()
	if !ok || unit.value != first && unit.value != second {
		return start, false
	}
	return probe, true
}

func javaFurtherNumericCursor(first, second javaInputCursor) javaInputCursor {
	if second.offset > first.offset {
		return second
	}
	return first
}

func javaTranslatedSourceText(input *javaUnicodeInput, start, end int) string {
	if input == nil || start < 0 || end <= start || end > len(input.source) {
		return ""
	}
	escapeIndex := sort.Search(len(input.escapes), func(index int) bool {
		return input.escapes[index] >= start
	})
	if escapeIndex >= len(input.escapes) || input.escapes[escapeIndex] >= end {
		return input.source[start:end]
	}
	var translated strings.Builder
	translated.Grow(end - start)
	cursor := input.cursor(start, end)
	for {
		unit, ok := cursor.next()
		if !ok {
			return translated.String()
		}
		translated.WriteRune(unit.value)
	}
}

func javaLogicalIdentifierToken(
	input *javaUnicodeInput,
	cursor *javaInputCursor,
) (javaToken, bool) {
	if input == nil || cursor == nil {
		return javaToken{}, false
	}
	probe := *cursor
	value, start, end, escaped, ok := probe.nextCodePoint()
	if !ok || !javaIdentifierStartRune(value) {
		return javaToken{}, false
	}

	var decoded strings.Builder
	if escaped {
		decoded.WriteRune(value)
	}
	for {
		candidate := probe
		nextValue, nextStart, nextEnd, nextEscaped, exists := candidate.nextCodePoint()
		if !exists || !javaIdentifierContinueRune(nextValue) {
			break
		}
		if nextEscaped && decoded.Len() == 0 {
			decoded.WriteString(input.source[start:nextStart])
		}
		if decoded.Len() > 0 {
			if nextEscaped {
				decoded.WriteRune(nextValue)
			} else {
				decoded.WriteString(input.source[nextStart:nextEnd])
			}
		}
		probe = candidate
		end = nextEnd
	}

	text := input.source[start:end]
	logicalValue := text
	if decoded.Len() > 0 {
		logicalValue = decoded.String()
	}
	*cursor = probe
	return javaToken{
		text:       text,
		value:      logicalValue,
		start:      start,
		end:        end,
		identifier: true,
	}, true
}

func javaLogicalPunctuationToken(cursor *javaInputCursor) javaToken {
	if cursor == nil || cursor.input == nil {
		return javaToken{}
	}
	units := make([]javaInputUnit, 0, 4)
	probe := *cursor
	for len(units) < cap(units) {
		unit, ok := probe.next()
		if !ok {
			break
		}
		units = append(units, unit)
	}
	if len(units) == 0 {
		return javaToken{}
	}

	width, logical := javaLogicalOperatorToken(units)
	if width == 1 {
		if units[0].escaped {
			logical = string(units[0].value)
		} else {
			logical = cursor.input.source[units[0].start:units[0].end]
		}
	}
	for range width {
		_, _ = cursor.next()
	}
	return javaToken{
		text:  logical,
		value: logical,
		start: units[0].start,
		end:   units[width-1].end,
	}
}

func javaLogicalOperatorToken(units []javaInputUnit) (int, string) {
	if len(units) == 0 {
		return 0, ""
	}
	first := units[0].value
	switch first {
	case '>':
		if len(units) >= 4 && units[1].value == '>' && units[2].value == '>' &&
			units[3].value == '=' {
			return 4, ">>>="
		}
		if len(units) >= 3 && units[1].value == '>' {
			switch units[2].value {
			case '>':
				return 3, ">>>"
			case '=':
				return 3, ">>="
			}
		}
		if len(units) >= 2 {
			switch units[1].value {
			case '>':
				return 2, ">>"
			case '=':
				return 2, ">="
			}
		}
	case '<':
		if len(units) >= 3 && units[1].value == '<' && units[2].value == '=' {
			return 3, "<<="
		}
		if len(units) >= 2 {
			switch units[1].value {
			case '<':
				return 2, "<<"
			case '=':
				return 2, "<="
			}
		}
	case '.':
		if len(units) >= 3 && units[1].value == '.' && units[2].value == '.' {
			return 3, "..."
		}
	case '-':
		if len(units) >= 2 {
			switch units[1].value {
			case '>':
				return 2, "->"
			case '-':
				return 2, "--"
			case '=':
				return 2, "-="
			}
		}
	case ':':
		if len(units) >= 2 && units[1].value == ':' {
			return 2, "::"
		}
	case '=':
		if len(units) >= 2 && units[1].value == '=' {
			return 2, "=="
		}
	case '!':
		if len(units) >= 2 && units[1].value == '=' {
			return 2, "!="
		}
	case '&':
		if len(units) >= 2 {
			switch units[1].value {
			case '&':
				return 2, "&&"
			case '=':
				return 2, "&="
			}
		}
	case '|':
		if len(units) >= 2 {
			switch units[1].value {
			case '|':
				return 2, "||"
			case '=':
				return 2, "|="
			}
		}
	case '+':
		if len(units) >= 2 {
			switch units[1].value {
			case '+':
				return 2, "++"
			case '=':
				return 2, "+="
			}
		}
	case '*':
		if len(units) >= 2 && units[1].value == '=' {
			return 2, "*="
		}
	case '/':
		if len(units) >= 2 && units[1].value == '=' {
			return 2, "/="
		}
	case '%':
		if len(units) >= 2 && units[1].value == '=' {
			return 2, "%="
		}
	case '^':
		if len(units) >= 2 && units[1].value == '=' {
			return 2, "^="
		}
	}
	return 1, ""
}

// javaIdentifierToken is intentionally a raw-range helper for validating
// concrete tree-sitter identifier nodes. The lexical scanner above uses the
// full JLS Unicode-input cursor and becomes authoritative whenever translation
// occurred.
func javaIdentifierToken(source string, start, limit int) (javaToken, int, bool) {
	if start < 0 || start >= limit || limit > len(source) {
		return javaToken{}, start, false
	}
	r, size := utf8.DecodeRuneInString(source[start:limit])
	decodedEscape := false
	if source[start] == '\\' {
		var ok bool
		r, size, ok = javaUnicodeEscapeAt(source, start, limit)
		if !ok {
			return javaToken{}, start, false
		}
		decodedEscape = true
	}
	if !javaIdentifierStartRune(r) {
		return javaToken{}, start, false
	}
	offset := start + size
	var decoded strings.Builder
	if decodedEscape {
		decoded.WriteRune(r)
	}
	for offset < limit {
		nextRune, nextSize := utf8.DecodeRuneInString(source[offset:limit])
		escaped := false
		if source[offset] == '\\' {
			var ok bool
			nextRune, nextSize, ok = javaUnicodeEscapeAt(source, offset, limit)
			if !ok {
				break
			}
			escaped = true
		}
		if !javaIdentifierContinueRune(nextRune) {
			break
		}
		if escaped && decoded.Len() == 0 {
			decoded.WriteString(source[start:offset])
		}
		if decoded.Len() > 0 {
			if escaped {
				decoded.WriteRune(nextRune)
			} else {
				decoded.WriteString(source[offset : offset+nextSize])
			}
		}
		offset += nextSize
	}
	text := source[start:offset]
	value := text
	if decoded.Len() > 0 {
		value = decoded.String()
	}
	return javaToken{
		text:       text,
		value:      value,
		start:      start,
		end:        offset,
		identifier: true,
	}, offset, true
}

func javaUnicodeEscapeAt(source string, start, limit int) (rune, int, bool) {
	if start < 0 || start+6 > limit || source[start] != '\\' || source[start+1] != 'u' {
		return 0, 0, false
	}
	offset := start + 1
	for offset < limit && source[offset] == 'u' {
		offset++
	}
	if offset+4 > limit {
		return 0, 0, false
	}
	value := rune(0)
	for index := range 4 {
		digit, ok := javaHexDigit(source[offset+index])
		if !ok {
			return 0, 0, false
		}
		value = value<<4 | rune(digit)
	}
	return value, offset + 4 - start, true
}

func javaHexDigit(character byte) (int, bool) {
	switch {
	case character >= '0' && character <= '9':
		return int(character - '0'), true
	case character >= 'a' && character <= 'f':
		return int(character-'a') + 10, true
	case character >= 'A' && character <= 'F':
		return int(character-'A') + 10, true
	default:
		return 0, false
	}
}

func javaInputWhitespace(character rune) bool {
	switch character {
	case ' ', '\t', '\n', '\r', '\f':
		return true
	default:
		return false
	}
}

func javaIdentifierStartRune(r rune) bool {
	return r == '$' || r == '_' || unicode.IsLetter(r) ||
		unicode.In(r, unicode.Nl, unicode.Sc, unicode.Pc) ||
		javaRuneInRanges(r, javaUnicode17IdentifierStartRanges)
}

func javaIdentifierContinueRune(r rune) bool {
	return javaIdentifierStartRune(r) || javaIdentifierIgnorableRune(r) ||
		unicode.IsDigit(r) || unicode.In(r, unicode.Nd, unicode.Mn, unicode.Mc) ||
		javaRuneInRanges(r, javaUnicode17IdentifierPartRanges)
}

func javaIdentifierIgnorableRune(r rune) bool {
	return r >= 0 && r <= 0x0008 || r >= 0x000E && r <= 0x001B ||
		r >= 0x007F && r <= 0x009F || unicode.Is(unicode.Cf, r)
}

type javaRuneRange struct {
	first rune
	last  rune
}

func javaRuneInRanges(value rune, ranges []javaRuneRange) bool {
	index := sort.Search(len(ranges), func(index int) bool {
		return ranges[index].last >= value
	})
	return index < len(ranges) && ranges[index].first <= value
}

// Go 1.26 carries Unicode 15.0 tables, while Java 26's Character predicates
// use Unicode 17.0. These are the general-category additions accepted by
// Character.isJavaIdentifierStart/Part in Unicode 15.1 through 17.0. Keeping
// the compact overlay here lets the scanner follow the current Java grammar
// without a generated multi-megabyte Unicode table.
// Source: https://www.unicode.org/Public/17.0.0/ucd/UnicodeData.txt
var javaUnicode17IdentifierStartRanges = []javaRuneRange{
	{first: 0x088F, last: 0x088F},
	{first: 0x0C5C, last: 0x0C5C},
	{first: 0x0CDC, last: 0x0CDC},
	{first: 0x1C89, last: 0x1C8A},
	{first: 0x20C1, last: 0x20C1},
	{first: 0xA7CB, last: 0xA7CF},
	{first: 0xA7D2, last: 0xA7D2},
	{first: 0xA7D4, last: 0xA7D4},
	{first: 0xA7DA, last: 0xA7DC},
	{first: 0xA7F1, last: 0xA7F1},
	{first: 0x105C0, last: 0x105F3},
	{first: 0x10940, last: 0x10959},
	{first: 0x10D4A, last: 0x10D65},
	{first: 0x10D6F, last: 0x10D85},
	{first: 0x10EC2, last: 0x10EC7},
	{first: 0x11380, last: 0x11389},
	{first: 0x1138B, last: 0x1138B},
	{first: 0x1138E, last: 0x1138E},
	{first: 0x11390, last: 0x113B5},
	{first: 0x113B7, last: 0x113B7},
	{first: 0x113D1, last: 0x113D1},
	{first: 0x113D3, last: 0x113D3},
	{first: 0x11BC0, last: 0x11BE0},
	{first: 0x11DB0, last: 0x11DDB},
	{first: 0x13460, last: 0x143FA},
	{first: 0x16100, last: 0x1611D},
	{first: 0x16D40, last: 0x16D6C},
	{first: 0x16EA0, last: 0x16EB8},
	{first: 0x16EBB, last: 0x16ED3},
	{first: 0x16FF2, last: 0x16FF6},
	{first: 0x187F8, last: 0x187FF},
	{first: 0x18CFF, last: 0x18CFF},
	{first: 0x18D09, last: 0x18D1E},
	{first: 0x18D80, last: 0x18DF2},
	{first: 0x1E5D0, last: 0x1E5ED},
	{first: 0x1E5F0, last: 0x1E5F0},
	{first: 0x1E6C0, last: 0x1E6DE},
	{first: 0x1E6E0, last: 0x1E6E2},
	{first: 0x1E6E4, last: 0x1E6E5},
	{first: 0x1E6E7, last: 0x1E6ED},
	{first: 0x1E6F0, last: 0x1E6F4},
	{first: 0x1E6FE, last: 0x1E6FF},
	{first: 0x2B73A, last: 0x2B73F},
	{first: 0x2CEA2, last: 0x2CEAD},
	{first: 0x2EBF0, last: 0x2EE5D},
	{first: 0x323B0, last: 0x33479},
}

var javaUnicode17IdentifierPartRanges = []javaRuneRange{
	{first: 0x088F, last: 0x088F},
	{first: 0x0897, last: 0x0897},
	{first: 0x0C5C, last: 0x0C5C},
	{first: 0x0CDC, last: 0x0CDC},
	{first: 0x1ACF, last: 0x1ADD},
	{first: 0x1AE0, last: 0x1AEB},
	{first: 0x1C89, last: 0x1C8A},
	{first: 0x20C1, last: 0x20C1},
	{first: 0xA7CB, last: 0xA7CF},
	{first: 0xA7D2, last: 0xA7D2},
	{first: 0xA7D4, last: 0xA7D4},
	{first: 0xA7DA, last: 0xA7DC},
	{first: 0xA7F1, last: 0xA7F1},
	{first: 0x105C0, last: 0x105F3},
	{first: 0x10940, last: 0x10959},
	{first: 0x10D40, last: 0x10D65},
	{first: 0x10D69, last: 0x10D6D},
	{first: 0x10D6F, last: 0x10D85},
	{first: 0x10EC2, last: 0x10EC7},
	{first: 0x10EFA, last: 0x10EFC},
	{first: 0x11380, last: 0x11389},
	{first: 0x1138B, last: 0x1138B},
	{first: 0x1138E, last: 0x1138E},
	{first: 0x11390, last: 0x113B5},
	{first: 0x113B7, last: 0x113C0},
	{first: 0x113C2, last: 0x113C2},
	{first: 0x113C5, last: 0x113C5},
	{first: 0x113C7, last: 0x113CA},
	{first: 0x113CC, last: 0x113D3},
	{first: 0x113E1, last: 0x113E2},
	{first: 0x116D0, last: 0x116E3},
	{first: 0x11B60, last: 0x11B67},
	{first: 0x11BC0, last: 0x11BE0},
	{first: 0x11BF0, last: 0x11BF9},
	{first: 0x11DB0, last: 0x11DDB},
	{first: 0x11DE0, last: 0x11DE9},
	{first: 0x11F5A, last: 0x11F5A},
	{first: 0x13460, last: 0x143FA},
	{first: 0x16100, last: 0x16139},
	{first: 0x16D40, last: 0x16D6C},
	{first: 0x16D70, last: 0x16D79},
	{first: 0x16EA0, last: 0x16EB8},
	{first: 0x16EBB, last: 0x16ED3},
	{first: 0x16FF2, last: 0x16FF6},
	{first: 0x187F8, last: 0x187FF},
	{first: 0x18CFF, last: 0x18CFF},
	{first: 0x18D09, last: 0x18D1E},
	{first: 0x18D80, last: 0x18DF2},
	{first: 0x1CCF0, last: 0x1CCF9},
	{first: 0x1E5D0, last: 0x1E5FA},
	{first: 0x1E6C0, last: 0x1E6DE},
	{first: 0x1E6E0, last: 0x1E6F5},
	{first: 0x1E6FE, last: 0x1E6FF},
	{first: 0x2B73A, last: 0x2B73F},
	{first: 0x2CEA2, last: 0x2CEAD},
	{first: 0x2EBF0, last: 0x2EE5D},
	{first: 0x323B0, last: 0x33479},
}

func normalizeJavaSpans(spans []javaByteSpan) []javaByteSpan {
	if len(spans) == 0 {
		return nil
	}
	sort.Slice(spans, func(first, second int) bool {
		if spans[first].start != spans[second].start {
			return spans[first].start < spans[second].start
		}
		return spans[first].end < spans[second].end
	})
	normalized := spans[:0]
	for _, span := range spans {
		if span.start < 0 || span.end <= span.start {
			continue
		}
		if len(normalized) == 0 || span.start > normalized[len(normalized)-1].end {
			normalized = append(normalized, span)
			continue
		}
		if span.end > normalized[len(normalized)-1].end {
			normalized[len(normalized)-1].end = span.end
		}
	}
	return normalized
}
