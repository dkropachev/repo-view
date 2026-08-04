package repoview

import (
	"sort"
	"strings"
	"unicode/utf8"
)

type javascriptFallbackScanner struct {
	source        string
	jsxSource     string
	previousToken string
	previousWord  string

	comments               []javascriptByteSpan
	stringsAndRegex        []javascriptByteSpan
	jsxStringSpans         []javascriptByteSpan
	jsxLexicalSkipSpans    []javascriptByteSpan
	jsxLexicalValueMarkers []javascriptByteSpan
	jsxExpressionStarts    []int
	jsxExpressionEnds      []int
	jsxRootValues          []javascriptByteSpan
	parenControls          []bool
	parenAsyncArrows       []bool
	parenForHeaders        []bool
	parenForSeparators     []bool
	parenForBracketDepths  []int
	parenForBraceDepths    []int
	braceBlocks            []bool
	braceValues            []bool
	braceClasses           []bool
	braceParenDepths       []int
	braceBracketDepths     []int
	braceFunctionModes     []javascriptFallbackFunctionMode
	braceFunctionIDs       []int
	braceStarts            []int
	pendingCallables       []javascriptFallbackCallable
	computedMembers        []javascriptFallbackComputedMember
	forcedStatementStarts  []int
	objectBraces           []javascriptByteSpan
	templateSubstitutions  []int

	jsxSkipIndex         int
	jsxValueIndex        int
	jsxExpressionIndex   int
	jsxRetryAfter        int
	bracketDepth         int
	pendingArrowID       int
	pendingArrowPosition int
	nextCallableID       int
	bindingParenDepth    int
	bindingBracketDepth  int
	bindingBraceDepth    int
	concreteUnits        int
	concreteUnitLimit    int

	pendingArrowMode        javascriptFallbackFunctionMode
	closedParenAsyncArrow   bool
	directMemberPrefix      bool
	identifierAsyncArrow    bool
	directMemberPrefixAsync bool
	directMemberPrefixGen   bool
	directMemberCandidate   bool
	directMemberAsync       bool
	directMemberGenerator   bool
	asyncExpression         bool
	asyncPending            bool
	expressionAllowed       bool
	logicalLineWhitespace   bool
	bindingDeclaration      bool
	bindingNeedsInitializer bool
	bindingCanEnd           bool
	restrictedStatementASI  bool
	concreteBudgetExceeded  bool
	jsxDisabled             bool
	tsx                     bool
}

type javascriptFallbackCallable struct {
	parenDepth            int
	bracketDepth          int
	braceDepth            int
	id                    int
	previousArrowPosition int
	value                 bool
	class                 bool
	function              bool
	arrow                 bool
	mode                  javascriptFallbackFunctionMode
}

type javascriptFallbackComputedMember struct {
	bracketDepth int
	braceDepth   int
	async        bool
	generator    bool
}

type javascriptFallbackFunctionMode uint8

const (
	javascriptFallbackNoFunction javascriptFallbackFunctionMode = iota
	javascriptFallbackOrdinaryFunction
	javascriptFallbackAsyncFunction
	javascriptFallbackGeneratorFunction
	javascriptFallbackAsyncGeneratorFunction
)

func javascriptFallbackFunctionModeFor(async, generator bool) javascriptFallbackFunctionMode {
	switch {
	case async && generator:
		return javascriptFallbackAsyncGeneratorFunction
	case async:
		return javascriptFallbackAsyncFunction
	case generator:
		return javascriptFallbackGeneratorFunction
	default:
		return javascriptFallbackOrdinaryFunction
	}
}

func (mode javascriptFallbackFunctionMode) treatsContextualWordAsOperator(word string) bool {
	switch word {
	case "await":
		return mode == javascriptFallbackAsyncFunction ||
			mode == javascriptFallbackAsyncGeneratorFunction
	case "yield":
		return mode == javascriptFallbackGeneratorFunction ||
			mode == javascriptFallbackAsyncGeneratorFunction
	default:
		return false
	}
}

type javascriptFallbackResult struct {
	comments              []javascriptByteSpan
	literals              []javascriptByteSpan
	lexicalSkips          []javascriptByteSpan
	lexicalLiterals       []javascriptByteSpan
	lexicalReplacements   []javascriptTokenReplacement
	jsxValues             []javascriptByteSpan
	objectBraces          []javascriptByteSpan
	forcedStatementStarts []int
	tokenCapacity         int
	available             bool
}

type javascriptTokenReplacement struct {
	text   string
	offset int
}

func scanJavaScriptFallback(source string) javascriptFallbackResult {
	return scanJavaScriptFallbackFlavor(source, javascriptSyntaxFlavorJavaScript)
}

func scanJavaScriptFallbackFlavor(
	source string,
	flavor javascriptSyntaxFlavor,
) javascriptFallbackResult {
	jsxSource := source
	if flavor == javascriptSyntaxFlavorTSX {
		jsxSource = javascriptFallbackTSXTypeArgumentShadow(source)
	}
	scanner := &javascriptFallbackScanner{
		source:                source,
		jsxSource:             jsxSource,
		expressionAllowed:     true,
		logicalLineWhitespace: true,
		jsxDisabled:           !flavor.permitsJSX(),
		tsx:                   flavor == javascriptSyntaxFlavorTSX,
	}
	scanner.scan()
	publicLiterals := append(
		append([]javascriptByteSpan(nil), scanner.stringsAndRegex...),
		scanner.jsxStringSpans...,
	)
	lexicalLiterals := append(
		append([]javascriptByteSpan(nil), scanner.stringsAndRegex...),
		scanner.jsxLexicalValueMarkers...,
	)
	replacements := make(
		[]javascriptTokenReplacement, 0,
		len(scanner.jsxExpressionStarts)+len(scanner.jsxExpressionEnds),
	)
	for _, start := range scanner.jsxExpressionStarts {
		replacements = append(replacements, javascriptTokenReplacement{
			offset: start - 1,
			text:   "(",
		})
	}
	for _, end := range scanner.jsxExpressionEnds {
		replacements = append(replacements, javascriptTokenReplacement{
			offset: end,
			text:   ")",
		})
	}
	sort.Slice(replacements, func(first, second int) bool {
		return replacements[first].offset < replacements[second].offset
	})
	objectBraces := scanner.objectBraces
	if objectBraces == nil {
		objectBraces = make([]javascriptByteSpan, 0)
	}
	return javascriptFallbackResult{
		comments:              normalizeJavaScriptSpans(scanner.comments),
		literals:              normalizeJavaScriptSpans(publicLiterals),
		lexicalSkips:          javascriptSpansWithoutReplacementBytes(scanner.jsxLexicalSkipSpans, replacements),
		lexicalLiterals:       normalizeJavaScriptSpans(lexicalLiterals),
		lexicalReplacements:   replacements,
		jsxValues:             normalizeJavaScriptSpans(scanner.jsxRootValues),
		objectBraces:          objectBraces,
		forcedStatementStarts: append([]int(nil), scanner.forcedStatementStarts...),
		tokenCapacity:         scanner.concreteUnits + len(replacements),
		available:             true,
	}
}

func javascriptSpansWithoutReplacementBytes(
	spans []javascriptByteSpan,
	replacements []javascriptTokenReplacement,
) []javascriptByteSpan {
	spans = normalizeJavaScriptSpans(append([]javascriptByteSpan(nil), spans...))
	result := make([]javascriptByteSpan, 0, len(spans)+len(replacements))
	replacementIndex := 0
	for _, span := range spans {
		for replacementIndex < len(replacements) &&
			replacements[replacementIndex].offset < span.start {
			replacementIndex++
		}
		start := span.start
		for replacementIndex < len(replacements) &&
			replacements[replacementIndex].offset < span.end {
			offset := replacements[replacementIndex].offset
			if offset > start {
				result = append(result, javascriptByteSpan{start: start, end: offset})
			}
			start = max(start, offset+1)
			replacementIndex++
		}
		if start < span.end {
			result = append(result, javascriptByteSpan{start: start, end: span.end})
		}
	}
	return normalizeJavaScriptSpans(result)
}

func javascriptFallbackMasks(source string) ([]javascriptByteSpan, []javascriptByteSpan) {
	result := scanJavaScriptFallback(source)
	return result.comments, result.literals
}

const (
	javascriptFallbackTSXMaximumLookaheadBytes = 4 << 10
	javascriptFallbackTSXMaximumTypeDepth      = 128
)

// javascriptFallbackTSXTypeArgumentShadow replaces JSX type arguments with
// same-width whitespace before the existing JSX recovery parser sees them.
// Public coordinates and search visibility remain tied to the original source;
// only JSX markup recognition uses the shadow. Every successful range advances
// the scan past that range, and every failed check has a fixed lookahead cap.
func javascriptFallbackTSXTypeArgumentShadow(source string) string {
	var shadow []byte
	for offset := 0; offset < len(source); {
		candidateSource := source[:min(
			len(source), offset+javascriptFallbackTSXMaximumLookaheadBytes,
		)]
		if source[offset] != '<' || offset+1 >= len(candidateSource) ||
			strings.HasPrefix(candidateSource[offset:], "</") ||
			strings.HasPrefix(candidateSource[offset:], "<!--") {
			_, size := utf8.DecodeRuneInString(source[offset:])
			if size < 1 {
				size = 1
			}
			offset += size
			continue
		}
		nameEnd, nameOK := javascriptFallbackJSXNameEnd(candidateSource, offset+1)
		if !nameOK || nameEnd >= len(candidateSource) || candidateSource[nameEnd] != '<' {
			offset++
			continue
		}
		typeEnd, typeOK := javascriptFallbackTSXAngleEnd(candidateSource, nameEnd)
		if !typeOK || !javascriptFallbackTSXTypeArgumentsCanContinueJSX(
			candidateSource, typeEnd,
		) {
			offset++
			continue
		}
		if shadow == nil {
			shadow = []byte(source)
		}
		for cursor := nameEnd; cursor < typeEnd; {
			if size := javascriptLineTerminatorSize(source, cursor); size > 0 {
				cursor += size
				continue
			}
			shadow[cursor] = ' '
			cursor++
		}
		offset = typeEnd
	}
	if shadow == nil {
		return source
	}
	return string(shadow)
}

func javascriptFallbackTSXTypeArgumentsCanContinueJSX(source string, offset int) bool {
	offset = javascriptFallbackSkipTrivia(source, offset)
	if offset >= len(source) {
		return false
	}
	if strings.HasPrefix(source[offset:], "/>") || source[offset] == '>' ||
		source[offset] == '{' {
		return true
	}
	return javascriptIdentifierStartAt(source, offset)
}

// javascriptFallbackTSXGenericArrowAt recognizes the unambiguous generic-arrow
// prefix forms required by TSX (a comma, constraint, default, or const modifier
// in the type-parameter list). A genuine JSX element is rejected only after a
// complete parameter list and arrow token are found.
func javascriptFallbackTSXGenericArrowAt(source string, start int) bool {
	if start < 0 || start >= len(source) {
		return false
	}
	source = source[:min(len(source), start+javascriptFallbackTSXMaximumLookaheadBytes)]
	angleEnd, ok := javascriptFallbackTSXAngleEnd(source, start)
	if !ok || !javascriptFallbackTSXGenericParameterMarker(source, start, angleEnd) {
		return false
	}
	cursor := javascriptFallbackSkipTrivia(source, angleEnd)
	if cursor >= len(source) || source[cursor] != '(' {
		return false
	}
	parameterEnd, ok := javascriptFallbackTSXDelimiterEnd(source, cursor, start)
	if !ok {
		return false
	}
	cursor = javascriptFallbackSkipTrivia(source, parameterEnd)
	if strings.HasPrefix(source[cursor:], "=>") {
		return true
	}
	if cursor >= len(source) || source[cursor] != ':' {
		return false
	}
	return javascriptFallbackTSXReturnTypeHasArrow(source, cursor+1, start)
}

func javascriptFallbackTSXAngleEnd(source string, start int) (int, bool) {
	if start < 0 || start >= len(source) || source[start] != '<' {
		return start, false
	}
	limit := min(len(source), start+javascriptFallbackTSXMaximumLookaheadBytes)
	source = source[:limit]
	depth := 0
	for offset := start; offset < limit; {
		if strings.HasPrefix(source[offset:], "//") {
			offset = javascriptLineTerminatorOffset(source, offset+2)
			continue
		}
		if strings.HasPrefix(source[offset:], "/*") {
			end := strings.Index(source[offset+2:], "*/")
			if end < 0 {
				return start, false
			}
			offset += end + 4
			continue
		}
		switch source[offset] {
		case '\'', '"':
			end := javascriptQuotedLiteralEnd(source, offset, source[offset])
			if end <= offset || end > limit || source[end-1] != source[offset] {
				return start, false
			}
			offset = end
			continue
		case '`':
			end, closed := javascriptFallbackTSXTemplateEnd(source, offset, limit)
			if !closed {
				return start, false
			}
			offset = end
			continue
		case '<':
			depth++
			if depth > javascriptFallbackTSXMaximumTypeDepth {
				return start, false
			}
		case '>':
			if offset > start && source[offset-1] == '=' {
				offset++
				continue
			}
			depth--
			if depth == 0 {
				return offset + 1, true
			}
			if depth < 0 {
				return start, false
			}
		}
		_, size := utf8.DecodeRuneInString(source[offset:])
		if size < 1 {
			size = 1
		}
		offset += size
	}
	return start, false
}

func javascriptFallbackTSXTemplateEnd(source string, start, limit int) (int, bool) {
	for offset := start + 1; offset < limit; {
		if source[offset] == '\\' {
			offset++
			if offset >= limit {
				return start, false
			}
			_, size := utf8.DecodeRuneInString(source[offset:])
			if size < 1 {
				size = 1
			}
			offset += size
			continue
		}
		if source[offset] == '`' {
			return offset + 1, true
		}
		_, size := utf8.DecodeRuneInString(source[offset:])
		if size < 1 {
			size = 1
		}
		offset += size
	}
	return start, false
}

func javascriptFallbackTSXGenericParameterMarker(source string, start, end int) bool {
	if start < 0 || end <= start+2 || end > len(source) {
		return false
	}
	source = source[:end]
	for offset := start + 1; offset < end-1; {
		if strings.HasPrefix(source[offset:], "//") {
			offset = javascriptLineTerminatorOffset(source, offset+2)
			continue
		}
		if strings.HasPrefix(source[offset:], "/*") {
			commentEnd := strings.Index(source[offset+2:], "*/")
			if commentEnd < 0 {
				return false
			}
			offset += commentEnd + 4
			continue
		}
		if source[offset] == ',' || source[offset] == '=' {
			return true
		}
		if source[offset] == '\'' || source[offset] == '"' {
			offset = javascriptQuotedLiteralEnd(source, offset, source[offset])
			continue
		}
		if javascriptIdentifierStartAt(source, offset) {
			wordEnd := javascriptIdentifierSourceEnd(source, offset)
			word := javascriptDecodedIdentifier(source[offset:wordEnd])
			if word == "extends" || word == "const" {
				return true
			}
			offset = wordEnd
			continue
		}
		_, size := utf8.DecodeRuneInString(source[offset:])
		if size < 1 {
			size = 1
		}
		offset += size
	}
	return false
}

func javascriptFallbackTSXDelimiterEnd(source string, start, origin int) (int, bool) {
	if start < 0 || start >= len(source) || source[start] != '(' {
		return start, false
	}
	limit := min(len(source), origin+javascriptFallbackTSXMaximumLookaheadBytes)
	source = source[:limit]
	stack := []byte{'('}
	for offset := start + 1; offset < limit; {
		if strings.HasPrefix(source[offset:], "//") {
			offset = javascriptLineTerminatorOffset(source, offset+2)
			continue
		}
		if strings.HasPrefix(source[offset:], "/*") {
			end := strings.Index(source[offset+2:], "*/")
			if end < 0 {
				return start, false
			}
			offset += end + 4
			continue
		}
		if source[offset] == '/' {
			if end, closed := javascriptFallbackJSXRegexEnd(source, offset); closed &&
				end <= limit {
				offset = end
				continue
			}
		}
		switch source[offset] {
		case '\'', '"':
			end := javascriptQuotedLiteralEnd(source, offset, source[offset])
			if end <= offset || end > limit || source[end-1] != source[offset] {
				return start, false
			}
			offset = end
			continue
		case '`':
			end, closed := javascriptFallbackTSXTemplateEnd(source, offset, limit)
			if !closed {
				return start, false
			}
			offset = end
			continue
		case '(', '[', '{':
			stack = append(stack, source[offset])
			if len(stack) > javascriptFallbackTSXMaximumTypeDepth {
				return start, false
			}
		case ')', ']', '}':
			if len(stack) == 0 || !javascriptFallbackTSXMatchingDelimiter(
				stack[len(stack)-1], source[offset],
			) {
				return start, false
			}
			stack = stack[:len(stack)-1]
			if len(stack) == 0 {
				return offset + 1, true
			}
		}
		_, size := utf8.DecodeRuneInString(source[offset:])
		if size < 1 {
			size = 1
		}
		offset += size
	}
	return start, false
}

func javascriptFallbackTSXMatchingDelimiter(open, closing byte) bool {
	return open == '(' && closing == ')' || open == '[' && closing == ']' ||
		open == '{' && closing == '}'
}

func javascriptFallbackTSXReturnTypeHasArrow(source string, start, origin int) bool {
	limit := min(len(source), origin+javascriptFallbackTSXMaximumLookaheadBytes)
	source = source[:limit]
	stack := make([]byte, 0, 4)
	angleDepth := 0
	for offset := start; offset < limit; {
		if strings.HasPrefix(source[offset:], "//") {
			offset = javascriptLineTerminatorOffset(source, offset+2)
			continue
		}
		if strings.HasPrefix(source[offset:], "/*") {
			end := strings.Index(source[offset+2:], "*/")
			if end < 0 {
				return false
			}
			offset += end + 4
			continue
		}
		if len(stack) == 0 && angleDepth == 0 && strings.HasPrefix(source[offset:], "=>") {
			return true
		}
		switch source[offset] {
		case '\'', '"':
			end := javascriptQuotedLiteralEnd(source, offset, source[offset])
			if end <= offset || end > limit || source[end-1] != source[offset] {
				return false
			}
			offset = end
			continue
		case '`':
			end, closed := javascriptFallbackTSXTemplateEnd(source, offset, limit)
			if !closed {
				return false
			}
			offset = end
			continue
		case '(', '[', '{':
			stack = append(stack, source[offset])
			if len(stack) > javascriptFallbackTSXMaximumTypeDepth {
				return false
			}
		case ')', ']', '}':
			if len(stack) == 0 || !javascriptFallbackTSXMatchingDelimiter(
				stack[len(stack)-1], source[offset],
			) {
				return false
			}
			stack = stack[:len(stack)-1]
		case '<':
			angleDepth++
			if angleDepth > javascriptFallbackTSXMaximumTypeDepth {
				return false
			}
		case '>':
			if offset > start && source[offset-1] == '=' {
				break
			}
			if angleDepth > 0 {
				angleDepth--
			}
		case ';':
			if len(stack) == 0 && angleDepth == 0 {
				return false
			}
		}
		_, size := utf8.DecodeRuneInString(source[offset:])
		if size < 1 {
			size = 1
		}
		offset += size
	}
	return false
}

func (scanner *javascriptFallbackScanner) scan() {
	for offset := 0; offset < len(scanner.source); {
		scanner.beginJSXExpression(offset)
		if scanner.consumeJSXLexicalSpan(&offset) {
			continue
		}
		if size := javascriptWhitespaceSize(scanner.source, offset); size > 0 {
			if javascriptLineTerminatorSize(scanner.source, offset) > 0 {
				scanner.logicalLineWhitespace = true
			}
			offset += size
			continue
		}
		scanner.concreteUnits++
		if scanner.concreteUnitLimit > 0 &&
			scanner.concreteUnits > scanner.concreteUnitLimit {
			scanner.concreteBudgetExceeded = true
			return
		}
		if (offset == 0 || offset == len("\uFEFF") && strings.HasPrefix(scanner.source, "\uFEFF")) &&
			strings.HasPrefix(scanner.source[offset:], "#!") {
			end := javascriptLineTerminatorOffset(scanner.source, offset+2)
			scanner.comments = append(scanner.comments, javascriptByteSpan{start: offset, end: end})
			scanner.logicalLineWhitespace = false
			offset = end
			continue
		}
		if strings.HasPrefix(scanner.source[offset:], "<!--") ||
			scanner.logicalLineWhitespace && strings.HasPrefix(scanner.source[offset:], "-->") {
			end := javascriptLineTerminatorOffset(scanner.source, offset)
			scanner.comments = append(scanner.comments, javascriptByteSpan{start: offset, end: end})
			scanner.logicalLineWhitespace = false
			offset = end
			continue
		}
		startsLogicalLine := scanner.logicalLineWhitespace
		if strings.HasPrefix(scanner.source[offset:], "//") {
			end := javascriptLineTerminatorOffset(scanner.source, offset+2)
			scanner.comments = append(scanner.comments, javascriptByteSpan{start: offset, end: end})
			scanner.logicalLineWhitespace = !javascriptLineHasTokenAfterComment(
				scanner.source, offset, end, !scanner.logicalLineWhitespace,
			)
			offset = end
			continue
		}
		if strings.HasPrefix(scanner.source[offset:], "/*") {
			end := strings.Index(scanner.source[offset+2:], "*/")
			if end < 0 {
				end = len(scanner.source)
			} else {
				end += offset + 4
			}
			scanner.comments = append(scanner.comments, javascriptByteSpan{start: offset, end: end})
			scanner.logicalLineWhitespace = !javascriptLineHasTokenAfterComment(
				scanner.source, offset, end, !scanner.logicalLineWhitespace,
			)
			offset = end
			continue
		}
		scanner.logicalLineWhitespace = false
		scanner.applyLineTerminatorASI(offset, startsLogicalLine)
		if !scanner.jsxDisabled && scanner.concreteUnitLimit == 0 &&
			offset >= scanner.jsxRetryAfter &&
			scanner.expressionAllowed && scanner.source[offset] == '<' &&
			(!scanner.tsx || !javascriptFallbackTSXGenericArrowAt(scanner.source, offset)) {
			candidate, ok, failureEnd := javascriptFallbackJSXCandidateAtWithFunctionMode(
				scanner.jsxSource, offset, scanner.activeFunctionMode(),
			)
			scanner.jsxRetryAfter = max(offset+1, failureEnd)
			if ok {
				scanner.jsxStringSpans = append(
					scanner.jsxStringSpans, candidate.publicStringSpans...,
				)
				scanner.jsxLexicalSkipSpans = append(
					scanner.jsxLexicalSkipSpans, candidate.lexicalSkipSpans...,
				)
				scanner.jsxLexicalValueMarkers = append(
					scanner.jsxLexicalValueMarkers, candidate.lexicalValueMarkers...,
				)
				scanner.jsxExpressionStarts = append(
					scanner.jsxExpressionStarts, candidate.lexicalExpressionStarts...,
				)
				scanner.jsxExpressionEnds = append(
					scanner.jsxExpressionEnds, candidate.lexicalExpressionEnds...,
				)
				scanner.jsxRootValues = append(scanner.jsxRootValues, javascriptByteSpan{
					start: offset,
					end:   candidate.end,
				})
				scanner.forcedStatementStarts = append(
					scanner.forcedStatementStarts, candidate.lexicalStatementStarts...,
				)
				continue
			}
		}

		switch scanner.source[offset] {
		case '\'', '"':
			end := javascriptQuotedLiteralEnd(scanner.source, offset, scanner.source[offset])
			scanner.stringsAndRegex = append(
				scanner.stringsAndRegex,
				javascriptByteSpan{start: offset, end: end},
			)
			scanner.recordValueToken(scanner.source[offset:end])
			offset = end
			continue
		case '`':
			end, substitution := scanner.scanTemplateRaw(offset)
			if substitution {
				scanner.expressionAllowed = true
			} else {
				scanner.recordValueToken("template")
			}
			offset = end
			continue
		case '/':
			scanner.clearDirectMemberCandidate()
			scanner.clearDirectMemberPrefix()
			scanner.closedParenAsyncArrow = false
			scanner.identifierAsyncArrow = false
			bindingASI := startsLogicalLine && scanner.bindingCanEndAtCurrentDepth()
			restrictedASI := startsLogicalLine && scanner.restrictedStatementASI
			if scanner.expressionAllowed || bindingASI || restrictedASI {
				end := javascriptRegexLiteralEnd(scanner.source, offset)
				if end > offset+1 && (bindingASI || restrictedASI ||
					javascriptFallbackRegexCanEnd(scanner.source, end)) {
					scanner.stringsAndRegex = append(
						scanner.stringsAndRegex,
						javascriptByteSpan{start: offset, end: end},
					)
					scanner.recordValueToken(scanner.source[offset:end])
					if bindingASI {
						scanner.clearBindingDeclaration()
					}
					scanner.restrictedStatementASI = false
					offset = end
					continue
				}
			}
			scanner.previousToken = "/"
			scanner.previousWord = ""
			scanner.expressionAllowed = true
			offset++
			if offset < len(scanner.source) && scanner.source[offset] == '=' {
				offset++
			}
			continue
		case '}':
			if len(scanner.templateSubstitutions) > 0 &&
				scanner.templateSubstitutions[len(scanner.templateSubstitutions)-1] == 0 {
				scanner.templateSubstitutions = scanner.templateSubstitutions[:len(scanner.templateSubstitutions)-1]
				end, substitution := scanner.scanTemplateRaw(offset)
				if substitution {
					scanner.expressionAllowed = true
				} else {
					scanner.recordValueToken("template")
				}
				offset = end
				continue
			}
		}

		if javascriptIdentifierStartAt(scanner.source, offset) {
			end := javascriptIdentifierSourceEnd(scanner.source, offset)
			word := scanner.source[offset:end]
			decoded := javascriptDecodedIdentifier(word)
			previousToken := scanner.previousToken
			previousWord := scanner.previousWord
			if startsLogicalLine {
				scanner.restrictedStatementASI = false
				scanner.clearDirectMemberPrefix()
			}
			propertyName := previousToken == "." || previousToken == "?."
			memberName := scanner.identifierIsDirectMemberName(previousToken, startsLogicalLine)
			contextualOfOperator := decoded == "of" &&
				scanner.contextualOfIsOperator(previousToken)
			memberPrefixAsync := scanner.directMemberPrefixAsync
			memberPrefixGenerator := scanner.directMemberPrefixGen
			scanner.identifierAsyncArrow = !startsLogicalLine && previousWord == "async" &&
				scanner.asyncPending && !propertyName && !memberName
			scanner.closedParenAsyncArrow = false
			if !propertyName && !memberName && javascriptHardDeclarationToken(decoded) &&
				!javascriptFallbackKeywordStartsValue(previousToken, previousWord) &&
				previousWord != "export" && previousWord != "default" {
				scanner.expirePendingCallablesAtCurrentDepth()
			}
			if scanner.bindingCanEndAtCurrentDepth() && startsLogicalLine {
				scanner.clearBindingDeclaration()
			}
			bindingStarted := false
			if !propertyName && !memberName && scanner.bindingDeclarationStarts(
				decoded, previousToken, previousWord, startsLogicalLine,
			) && javascriptFallbackBindingKeywordPlausible(scanner.source, end, decoded) {
				scanner.beginBindingDeclaration()
				bindingStarted = true
			}
			if !propertyName && !memberName && (decoded == "function" || decoded == "class") &&
				javascriptFallbackCallableKeywordPlausible(scanner.source, end, decoded) {
				value := javascriptFallbackKeywordStartsValue(previousToken, previousWord)
				asyncFunction := false
				if decoded == "function" && previousWord == "async" &&
					scanner.asyncPending && !startsLogicalLine {
					value = scanner.asyncExpression
					asyncFunction = true
				}
				scanner.nextCallableID++
				scanner.pendingCallables = append(
					scanner.pendingCallables,
					javascriptFallbackCallable{
						parenDepth: len(scanner.parenControls), bracketDepth: scanner.bracketDepth,
						braceDepth: len(scanner.braceBlocks), id: scanner.nextCallableID,
						value: value, class: decoded == "class", function: decoded == "function",
						mode: javascriptFallbackFunctionModeFor(asyncFunction, false),
					},
				)
			}
			if memberName {
				scanner.directMemberCandidate = true
				scanner.directMemberAsync = memberPrefixAsync
				scanner.directMemberGenerator = memberPrefixGenerator
				scanner.clearDirectMemberPrefix()
				switch decoded {
				case "async":
					scanner.directMemberPrefix = true
					scanner.directMemberPrefixAsync = true
					scanner.directMemberPrefixGen = memberPrefixGenerator
				case "get", "set", "static":
					scanner.directMemberPrefix = true
					scanner.directMemberPrefixAsync = memberPrefixAsync
					scanner.directMemberPrefixGen = memberPrefixGenerator
				}
			} else {
				scanner.clearDirectMemberCandidate()
				scanner.clearDirectMemberPrefix()
			}
			switch {
			case propertyName:
				scanner.asyncPending = false
			case decoded == "async":
				scanner.asyncExpression = javascriptFallbackKeywordStartsValue(
					previousToken, previousWord,
				)
				scanner.asyncPending = true
			default:
				scanner.asyncPending = false
			}
			scanner.previousToken = word
			scanner.previousWord = decoded
			if propertyName || memberName && javascriptControlHeaderKeyword(decoded) {
				scanner.previousWord = ""
			}
			if decoded == "await" && previousWord == "for" {
				scanner.previousWord = "for"
			}
			if contextualOfOperator || decoded == "in" && scanner.inForHeaderBeforeSeparator() {
				scanner.parenForSeparators[len(scanner.parenForSeparators)-1] = true
			}
			if !propertyName && !memberName &&
				(decoded == "break" || decoded == "continue" || decoded == "debugger") {
				scanner.restrictedStatementASI = true
			}
			switch {
			case propertyName:
				scanner.expressionAllowed = false
			case (decoded == "let" || decoded == "using") && !bindingStarted:
				scanner.expressionAllowed = false
			case decoded == "of" && !contextualOfOperator:
				scanner.expressionAllowed = false
			case (decoded == "await" || decoded == "yield") &&
				!scanner.contextualAwaitOrYieldIsOperator(decoded):
				scanner.expressionAllowed = false
			default:
				scanner.expressionAllowed = javascriptKeywordAllowsExpression(decoded)
			}
			if scanner.bindingDeclaration && scanner.bindingNeedsInitializer &&
				scanner.bindingAtCurrentDepth() &&
				!javascriptLexicalBindingDeclarationToken(decoded) {
				scanner.bindingCanEnd = true
			}
			offset = end
			continue
		}
		if javascriptNumberStart(scanner.source, offset) {
			end := javascriptNumberEnd(scanner.source, offset)
			scanner.recordValueToken(scanner.source[offset:end])
			offset = end
			continue
		}

		offset = scanner.scanPunctuation(offset, startsLogicalLine)
	}
	for index, block := range scanner.braceBlocks {
		if !block && index < len(scanner.braceStarts) {
			scanner.objectBraces = append(scanner.objectBraces, javascriptByteSpan{
				start: scanner.braceStarts[index],
				end:   len(scanner.source),
			})
		}
	}
}

func (scanner *javascriptFallbackScanner) beginJSXExpression(offset int) {
	for scanner.jsxExpressionIndex < len(scanner.jsxExpressionStarts) &&
		scanner.jsxExpressionStarts[scanner.jsxExpressionIndex] < offset {
		scanner.jsxExpressionIndex++
	}
	if scanner.jsxExpressionIndex >= len(scanner.jsxExpressionStarts) ||
		scanner.jsxExpressionStarts[scanner.jsxExpressionIndex] != offset {
		return
	}
	scanner.previousToken = "("
	scanner.previousWord = ""
	scanner.expressionAllowed = true
	scanner.jsxExpressionIndex++
}

func (scanner *javascriptFallbackScanner) consumeJSXLexicalSpan(offset *int) bool {
	for scanner.jsxSkipIndex < len(scanner.jsxLexicalSkipSpans) &&
		scanner.jsxLexicalSkipSpans[scanner.jsxSkipIndex].end <= *offset {
		scanner.jsxSkipIndex++
	}
	for scanner.jsxValueIndex < len(scanner.jsxLexicalValueMarkers) &&
		scanner.jsxLexicalValueMarkers[scanner.jsxValueIndex].end <= *offset {
		scanner.jsxValueIndex++
	}
	if scanner.jsxSkipIndex < len(scanner.jsxLexicalSkipSpans) {
		span := scanner.jsxLexicalSkipSpans[scanner.jsxSkipIndex]
		if span.start <= *offset {
			end := min(span.end, len(scanner.source))
			for cursor := *offset; cursor < end; {
				if size := javascriptLineTerminatorSize(scanner.source, cursor); size > 0 {
					scanner.logicalLineWhitespace = true
					cursor += size
					continue
				}
				_, size := utf8.DecodeRuneInString(scanner.source[cursor:])
				if size < 1 {
					size = 1
				}
				cursor += size
			}
			*offset = end
			return true
		}
	}
	if scanner.jsxValueIndex < len(scanner.jsxLexicalValueMarkers) {
		span := scanner.jsxLexicalValueMarkers[scanner.jsxValueIndex]
		if span.start <= *offset {
			scanner.concreteUnits++
			scanner.recordValueToken("jsx")
			scanner.logicalLineWhitespace = false
			*offset = min(span.end, len(scanner.source))
			scanner.jsxValueIndex++
			return true
		}
	}
	return false
}

func (scanner *javascriptFallbackScanner) scanTemplateRaw(start int) (int, bool) {
	for offset := start + 1; offset < len(scanner.source); {
		if scanner.source[offset] == '\\' {
			offset++
			if offset < len(scanner.source) {
				_, size := utf8.DecodeRuneInString(scanner.source[offset:])
				if size < 1 {
					size = 1
				}
				offset += size
			}
			continue
		}
		if scanner.source[offset] == '`' {
			scanner.stringsAndRegex = append(
				scanner.stringsAndRegex,
				javascriptByteSpan{start: start, end: offset + 1},
			)
			return offset + 1, false
		}
		if strings.HasPrefix(scanner.source[offset:], "${") {
			scanner.stringsAndRegex = append(
				scanner.stringsAndRegex,
				javascriptByteSpan{start: start, end: offset + 2},
			)
			scanner.templateSubstitutions = append(scanner.templateSubstitutions, 0)
			return offset + 2, true
		}
		_, size := utf8.DecodeRuneInString(scanner.source[offset:])
		if size < 1 {
			size = 1
		}
		offset += size
	}
	scanner.stringsAndRegex = append(
		scanner.stringsAndRegex,
		javascriptByteSpan{start: start, end: len(scanner.source)},
	)
	return len(scanner.source), false
}

func (scanner *javascriptFallbackScanner) scanPunctuation(offset int, startsLogicalLine bool) int {
	character := scanner.source[offset]
	next := byte(0)
	if offset+1 < len(scanner.source) {
		next = scanner.source[offset+1]
	}
	switch character {
	case '(':
		if scanner.directMemberCandidate {
			scanner.nextCallableID++
			scanner.pendingCallables = append(scanner.pendingCallables, javascriptFallbackCallable{
				parenDepth: len(scanner.parenControls), bracketDepth: scanner.bracketDepth,
				braceDepth: len(scanner.braceBlocks), id: scanner.nextCallableID,
				function: true,
				mode: javascriptFallbackFunctionModeFor(
					scanner.directMemberAsync, scanner.directMemberGenerator,
				),
			})
		}
		control := javascriptControlHeaderKeyword(scanner.previousWord)
		scanner.parenControls = append(scanner.parenControls, control)
		scanner.parenAsyncArrows = append(scanner.parenAsyncArrows,
			!startsLogicalLine && scanner.previousWord == "async" && scanner.asyncPending)
		scanner.parenForHeaders = append(scanner.parenForHeaders, scanner.previousWord == "for")
		scanner.parenForSeparators = append(scanner.parenForSeparators, false)
		scanner.parenForBracketDepths = append(scanner.parenForBracketDepths, scanner.bracketDepth)
		scanner.parenForBraceDepths = append(scanner.parenForBraceDepths, len(scanner.braceBlocks))
		scanner.clearDirectMemberCandidate()
		scanner.clearDirectMemberPrefix()
		scanner.closedParenAsyncArrow = false
		scanner.identifierAsyncArrow = false
		scanner.previousToken = "("
		scanner.previousWord = ""
		scanner.expressionAllowed = true
	case ')':
		control := false
		asyncArrow := false
		if len(scanner.parenControls) > 0 {
			control = scanner.parenControls[len(scanner.parenControls)-1]
			scanner.parenControls = scanner.parenControls[:len(scanner.parenControls)-1]
		}
		if len(scanner.parenAsyncArrows) > 0 {
			asyncArrow = scanner.parenAsyncArrows[len(scanner.parenAsyncArrows)-1]
			scanner.parenAsyncArrows = scanner.parenAsyncArrows[:len(scanner.parenAsyncArrows)-1]
		}
		if len(scanner.parenForHeaders) > 0 {
			scanner.parenForHeaders = scanner.parenForHeaders[:len(scanner.parenForHeaders)-1]
		}
		if len(scanner.parenForSeparators) > 0 {
			scanner.parenForSeparators = scanner.parenForSeparators[:len(scanner.parenForSeparators)-1]
		}
		if len(scanner.parenForBracketDepths) > 0 {
			scanner.parenForBracketDepths = scanner.parenForBracketDepths[:len(scanner.parenForBracketDepths)-1]
		}
		if len(scanner.parenForBraceDepths) > 0 {
			scanner.parenForBraceDepths = scanner.parenForBraceDepths[:len(scanner.parenForBraceDepths)-1]
		}
		scanner.expireUnreachablePendingCallables()
		scanner.clearDirectMemberCandidate()
		scanner.clearDirectMemberPrefix()
		scanner.closedParenAsyncArrow = asyncArrow
		scanner.identifierAsyncArrow = false
		scanner.previousToken = ")"
		scanner.previousWord = ""
		scanner.expressionAllowed = control
	case '[':
		if scanner.identifierIsDirectMemberName(scanner.previousToken, startsLogicalLine) {
			scanner.computedMembers = append(
				scanner.computedMembers,
				javascriptFallbackComputedMember{
					bracketDepth: scanner.bracketDepth + 1,
					braceDepth:   len(scanner.braceBlocks),
					async:        scanner.directMemberPrefixAsync,
					generator:    scanner.directMemberPrefixGen,
				},
			)
		}
		scanner.bracketDepth++
		scanner.clearDirectMemberCandidate()
		scanner.clearDirectMemberPrefix()
		scanner.closedParenAsyncArrow = false
		scanner.identifierAsyncArrow = false
		scanner.previousToken = "["
		scanner.previousWord = ""
		scanner.expressionAllowed = true
	case ']':
		computedMember := javascriptFallbackComputedMember{}
		computedMemberClosed := false
		if len(scanner.computedMembers) > 0 {
			computedMember = scanner.computedMembers[len(scanner.computedMembers)-1]
			computedMemberClosed = computedMember.bracketDepth == scanner.bracketDepth &&
				computedMember.braceDepth == len(scanner.braceBlocks)
		}
		if scanner.bracketDepth > 0 {
			scanner.bracketDepth--
		}
		scanner.expireUnreachablePendingCallables()
		scanner.clearDirectMemberCandidate()
		scanner.clearDirectMemberPrefix()
		if computedMemberClosed {
			scanner.directMemberCandidate = true
			scanner.directMemberAsync = computedMember.async
			scanner.directMemberGenerator = computedMember.generator
			scanner.computedMembers = scanner.computedMembers[:len(scanner.computedMembers)-1]
		}
		scanner.closedParenAsyncArrow = false
		scanner.identifierAsyncArrow = false
		scanner.previousToken = "]"
		scanner.previousWord = ""
		scanner.expressionAllowed = false
		if scanner.bindingDeclaration && scanner.bindingNeedsInitializer &&
			scanner.bindingAtCurrentDepth() {
			scanner.bindingCanEnd = true
		}
	case '{':
		block := javascriptFallbackBraceIsBlock(scanner.previousToken, scanner.expressionAllowed)
		value := !block || scanner.previousToken == "=>"
		classBrace := false
		functionMode, functionID := scanner.activeFunctionContext()
		if len(scanner.pendingCallables) > 0 {
			pending := scanner.pendingCallables[len(scanner.pendingCallables)-1]
			if pending.parenDepth == len(scanner.parenControls) &&
				pending.bracketDepth == scanner.bracketDepth &&
				pending.braceDepth == len(scanner.braceBlocks) &&
				(pending.class || !pending.arrow && scanner.previousToken == ")" ||
					pending.arrow && scanner.previousToken == "=>") {
				value = pending.value
				classBrace = pending.class
				if pending.function {
					functionMode = pending.mode
					functionID = pending.id
				}
				scanner.truncatePendingCallables(len(scanner.pendingCallables) - 1)
			}
		}
		scanner.braceBlocks = append(scanner.braceBlocks, block)
		scanner.braceValues = append(scanner.braceValues, value)
		scanner.braceClasses = append(scanner.braceClasses, classBrace)
		scanner.braceParenDepths = append(scanner.braceParenDepths, len(scanner.parenControls))
		scanner.braceBracketDepths = append(scanner.braceBracketDepths, scanner.bracketDepth)
		scanner.braceFunctionModes = append(scanner.braceFunctionModes, functionMode)
		scanner.braceFunctionIDs = append(scanner.braceFunctionIDs, functionID)
		scanner.braceStarts = append(scanner.braceStarts, offset)
		scanner.clearDirectMemberCandidate()
		scanner.clearDirectMemberPrefix()
		scanner.closedParenAsyncArrow = false
		scanner.identifierAsyncArrow = false
		if len(scanner.templateSubstitutions) > 0 {
			scanner.templateSubstitutions[len(scanner.templateSubstitutions)-1]++
		}
		scanner.previousToken = "{"
		scanner.previousWord = ""
		scanner.expressionAllowed = true
	case '}':
		block := true
		value := false
		braceStart := -1
		if len(scanner.braceBlocks) > 0 {
			block = scanner.braceBlocks[len(scanner.braceBlocks)-1]
			scanner.braceBlocks = scanner.braceBlocks[:len(scanner.braceBlocks)-1]
		}
		if len(scanner.braceValues) > 0 {
			value = scanner.braceValues[len(scanner.braceValues)-1]
			scanner.braceValues = scanner.braceValues[:len(scanner.braceValues)-1]
		}
		if len(scanner.braceClasses) > 0 {
			scanner.braceClasses = scanner.braceClasses[:len(scanner.braceClasses)-1]
		}
		if len(scanner.braceParenDepths) > 0 {
			scanner.braceParenDepths = scanner.braceParenDepths[:len(scanner.braceParenDepths)-1]
		}
		if len(scanner.braceBracketDepths) > 0 {
			scanner.braceBracketDepths = scanner.braceBracketDepths[:len(scanner.braceBracketDepths)-1]
		}
		if len(scanner.braceFunctionModes) > 0 {
			scanner.braceFunctionModes = scanner.braceFunctionModes[:len(scanner.braceFunctionModes)-1]
		}
		if len(scanner.braceFunctionIDs) > 0 {
			scanner.braceFunctionIDs = scanner.braceFunctionIDs[:len(scanner.braceFunctionIDs)-1]
		}
		if len(scanner.braceStarts) > 0 {
			braceStart = scanner.braceStarts[len(scanner.braceStarts)-1]
			scanner.braceStarts = scanner.braceStarts[:len(scanner.braceStarts)-1]
		}
		scanner.expireUnreachablePendingCallables()
		scanner.clearDirectMemberCandidate()
		scanner.clearDirectMemberPrefix()
		scanner.closedParenAsyncArrow = false
		scanner.identifierAsyncArrow = false
		if !block && braceStart >= 0 {
			scanner.objectBraces = append(scanner.objectBraces, javascriptByteSpan{
				start: braceStart,
				end:   offset + 1,
			})
		}
		if len(scanner.templateSubstitutions) > 0 &&
			scanner.templateSubstitutions[len(scanner.templateSubstitutions)-1] > 0 {
			scanner.templateSubstitutions[len(scanner.templateSubstitutions)-1]--
		}
		scanner.previousToken = "}"
		scanner.previousWord = ""
		scanner.expressionAllowed = block && !value
		if scanner.bindingDeclaration && scanner.bindingNeedsInitializer &&
			scanner.bindingAtCurrentDepth() {
			scanner.bindingCanEnd = true
		}
	case '+', '-':
		scanner.clearDirectMemberCandidate()
		scanner.clearDirectMemberPrefix()
		scanner.closedParenAsyncArrow = false
		scanner.identifierAsyncArrow = false
		if next == character {
			scanner.previousToken = scanner.source[offset : offset+2]
			scanner.previousWord = ""
			scanner.expressionAllowed = false
			return offset + 2
		}
		scanner.previousToken = scanner.source[offset : offset+1]
		scanner.previousWord = ""
		scanner.expressionAllowed = true
	case '=':
		if next == '>' {
			scanner.expirePendingCallablesAtCurrentDepth()
			scanner.nextCallableID++
			scanner.pendingCallables = append(scanner.pendingCallables, javascriptFallbackCallable{
				parenDepth: len(scanner.parenControls), bracketDepth: scanner.bracketDepth,
				braceDepth: len(scanner.braceBlocks), id: scanner.nextCallableID,
				previousArrowPosition: scanner.pendingArrowPosition,
				value:                 true, function: true, arrow: true,
				mode: javascriptFallbackFunctionModeFor(
					scanner.identifierAsyncArrow || scanner.closedParenAsyncArrow, false,
				),
			})
			scanner.pendingArrowMode = scanner.pendingCallables[len(scanner.pendingCallables)-1].mode
			scanner.pendingArrowID = scanner.nextCallableID
			scanner.pendingArrowPosition = len(scanner.pendingCallables)
			scanner.clearDirectMemberCandidate()
			scanner.clearDirectMemberPrefix()
			scanner.closedParenAsyncArrow = false
			scanner.identifierAsyncArrow = false
			scanner.previousToken = "=>"
			scanner.previousWord = ""
			scanner.expressionAllowed = true
			return offset + 2
		}
		scanner.expirePendingCallablesAtCurrentDepth()
		scanner.clearDirectMemberCandidate()
		scanner.clearDirectMemberPrefix()
		scanner.closedParenAsyncArrow = false
		scanner.identifierAsyncArrow = false
		scanner.previousToken = "="
		scanner.previousWord = ""
		scanner.expressionAllowed = true
		if scanner.bindingDeclaration && scanner.bindingAtCurrentDepth() {
			scanner.bindingNeedsInitializer = false
			scanner.bindingCanEnd = false
		}
	case ';':
		scanner.expirePendingCallablesAtCurrentDepth()
		scanner.previousToken = ";"
		scanner.previousWord = ""
		scanner.expressionAllowed = true
		if scanner.bindingDeclaration && scanner.bindingAtCurrentDepth() {
			scanner.clearBindingDeclaration()
		}
		scanner.restrictedStatementASI = false
		scanner.clearDirectMemberCandidate()
		scanner.clearDirectMemberPrefix()
		scanner.closedParenAsyncArrow = false
		scanner.identifierAsyncArrow = false
	case ',':
		scanner.expirePendingCallablesAtCurrentDepth()
		scanner.previousToken = ","
		scanner.previousWord = ""
		scanner.expressionAllowed = true
		if scanner.bindingDeclaration && scanner.bindingAtCurrentDepth() {
			scanner.bindingNeedsInitializer = true
			scanner.bindingCanEnd = false
		}
		scanner.clearDirectMemberCandidate()
		scanner.clearDirectMemberPrefix()
		scanner.closedParenAsyncArrow = false
		scanner.identifierAsyncArrow = false
	case '*':
		functionGenerator := false
		if len(scanner.pendingCallables) > 0 {
			pending := &scanner.pendingCallables[len(scanner.pendingCallables)-1]
			if pending.function && !pending.arrow &&
				pending.parenDepth == len(scanner.parenControls) &&
				pending.bracketDepth == scanner.bracketDepth &&
				pending.braceDepth == len(scanner.braceBlocks) {
				asyncFunction := pending.mode == javascriptFallbackAsyncFunction ||
					pending.mode == javascriptFallbackAsyncGeneratorFunction
				pending.mode = javascriptFallbackFunctionModeFor(asyncFunction, true)
				functionGenerator = true
			}
		}
		if !functionGenerator &&
			scanner.identifierIsDirectMemberName(scanner.previousToken, startsLogicalLine) {
			asyncMember := scanner.directMemberPrefixAsync
			scanner.clearDirectMemberCandidate()
			scanner.clearDirectMemberPrefix()
			scanner.directMemberPrefix = true
			scanner.directMemberPrefixAsync = asyncMember
			scanner.directMemberPrefixGen = true
		} else if !functionGenerator {
			scanner.clearDirectMemberCandidate()
			scanner.clearDirectMemberPrefix()
		}
		scanner.closedParenAsyncArrow = false
		scanner.identifierAsyncArrow = false
		scanner.previousToken = "*"
		scanner.previousWord = ""
		scanner.expressionAllowed = true
	case ':', '?', '!', '~', '%', '&', '|', '^', '<', '>':
		scanner.clearDirectMemberCandidate()
		scanner.clearDirectMemberPrefix()
		scanner.closedParenAsyncArrow = false
		scanner.identifierAsyncArrow = false
		scanner.previousToken = scanner.source[offset : offset+1]
		scanner.previousWord = ""
		scanner.expressionAllowed = true
	case '.':
		scanner.clearDirectMemberCandidate()
		scanner.clearDirectMemberPrefix()
		scanner.closedParenAsyncArrow = false
		scanner.identifierAsyncArrow = false
		scanner.previousToken = "."
		scanner.previousWord = ""
		scanner.expressionAllowed = false
	default:
		scanner.clearDirectMemberCandidate()
		scanner.clearDirectMemberPrefix()
		scanner.closedParenAsyncArrow = false
		scanner.identifierAsyncArrow = false
		scanner.previousToken = scanner.source[offset : offset+1]
		scanner.previousWord = ""
	}
	return offset + 1
}

func (scanner *javascriptFallbackScanner) bindingDeclarationStarts(
	word, previousToken, previousWord string,
	startsLine bool,
) bool {
	if !javascriptLexicalBindingDeclarationToken(word) ||
		previousToken == "." || previousToken == "?." {
		return false
	}
	if len(scanner.braceClasses) > 0 && scanner.braceClasses[len(scanner.braceClasses)-1] {
		return false
	}
	if len(scanner.braceBlocks) > 0 && !scanner.braceBlocks[len(scanner.braceBlocks)-1] {
		return false
	}
	switch previousToken {
	case "", ";", "}":
		return true
	case "{":
		return len(scanner.braceBlocks) == 0 ||
			scanner.braceBlocks[len(scanner.braceBlocks)-1]
	case "(":
		return len(scanner.parenControls) > 0 &&
			scanner.parenControls[len(scanner.parenControls)-1]
	}
	if previousWord == "export" || previousWord == "for" || startsLine {
		return true
	}
	return false
}

func (scanner *javascriptFallbackScanner) applyLineTerminatorASI(
	offset int,
	startsLine bool,
) {
	if !startsLine {
		return
	}
	forced := false
	if scanner.restrictedStatementASI {
		scanner.restrictedStatementASI = false
		scanner.expressionAllowed = true
		forced = true
	}
	if scanner.bindingCanEndAtCurrentDepth() &&
		!scanner.bindingDeclarationContinuesAt(offset) {
		scanner.clearBindingDeclaration()
		scanner.expressionAllowed = true
		forced = true
	}
	if !forced {
		return
	}
	scanner.expirePendingCallablesAtCurrentDepth()
	if offset < len(scanner.source) && scanner.source[offset] != ';' &&
		scanner.source[offset] != '}' {
		scanner.forcedStatementStarts = append(scanner.forcedStatementStarts, offset)
	}
}

func (scanner *javascriptFallbackScanner) bindingDeclarationContinuesAt(offset int) bool {
	if offset < 0 || offset >= len(scanner.source) {
		return false
	}
	switch scanner.source[offset] {
	case '=', ',':
		return true
	}
	if !javascriptIdentifierStartAt(scanner.source, offset) ||
		len(scanner.parenControls) == 0 ||
		!scanner.parenControls[len(scanner.parenControls)-1] {
		return false
	}
	end := javascriptIdentifierSourceEnd(scanner.source, offset)
	word := javascriptDecodedIdentifier(scanner.source[offset:end])
	return word == "in" || word == "of"
}

func (scanner *javascriptFallbackScanner) expirePendingCallablesAtCurrentDepth() {
	end := len(scanner.pendingCallables)
	for end > 0 {
		pending := scanner.pendingCallables[end-1]
		unreachable := pending.parenDepth > len(scanner.parenControls) ||
			pending.bracketDepth > scanner.bracketDepth ||
			pending.braceDepth > len(scanner.braceBlocks)
		current := pending.parenDepth == len(scanner.parenControls) &&
			pending.bracketDepth == scanner.bracketDepth &&
			pending.braceDepth == len(scanner.braceBlocks)
		if !unreachable && !current {
			break
		}
		end--
	}
	if end < len(scanner.pendingCallables) {
		scanner.truncatePendingCallables(end)
	}
}

func (scanner *javascriptFallbackScanner) expireUnreachablePendingCallables() {
	end := len(scanner.pendingCallables)
	for end > 0 {
		pending := scanner.pendingCallables[end-1]
		if pending.parenDepth <= len(scanner.parenControls) &&
			pending.bracketDepth <= scanner.bracketDepth &&
			pending.braceDepth <= len(scanner.braceBlocks) {
			break
		}
		end--
	}
	if end < len(scanner.pendingCallables) {
		scanner.truncatePendingCallables(end)
	}
}

func (scanner *javascriptFallbackScanner) truncatePendingCallables(length int) {
	length = max(0, min(length, len(scanner.pendingCallables)))
	for scanner.pendingArrowPosition > length {
		position := scanner.pendingArrowPosition
		scanner.pendingArrowPosition = scanner.pendingCallables[position-1].previousArrowPosition
	}
	scanner.pendingCallables = scanner.pendingCallables[:length]
	if scanner.pendingArrowPosition == 0 {
		scanner.pendingArrowMode = javascriptFallbackNoFunction
		scanner.pendingArrowID = 0
		return
	}
	pending := scanner.pendingCallables[scanner.pendingArrowPosition-1]
	scanner.pendingArrowMode = pending.mode
	scanner.pendingArrowID = pending.id
}

func (scanner *javascriptFallbackScanner) identifierIsDirectMemberName(
	previousToken string,
	startsLogicalLine bool,
) bool {
	if len(scanner.braceBlocks) == 0 ||
		len(scanner.braceParenDepths) != len(scanner.braceBlocks) ||
		len(scanner.braceBracketDepths) != len(scanner.braceBlocks) ||
		len(scanner.parenControls) != scanner.braceParenDepths[len(scanner.braceParenDepths)-1] ||
		scanner.bracketDepth != scanner.braceBracketDepths[len(scanner.braceBracketDepths)-1] {
		return false
	}
	classMember := scanner.braceClasses[len(scanner.braceClasses)-1]
	objectMember := !scanner.braceBlocks[len(scanner.braceBlocks)-1]
	if !classMember && !objectMember {
		return false
	}
	if scanner.directMemberPrefix {
		return true
	}
	if objectMember {
		return previousToken == "{" || previousToken == ","
	}
	switch previousToken {
	case "{", ";", "}":
		return true
	}
	return startsLogicalLine && javascriptFallbackTokenCanEndExpression(previousToken)
}

func (scanner *javascriptFallbackScanner) contextualOfIsOperator(
	previousToken string,
) bool {
	return scanner.inForHeaderBeforeSeparator() &&
		javascriptFallbackTokenCanEndExpression(previousToken)
}

func (scanner *javascriptFallbackScanner) inForHeaderBeforeSeparator() bool {
	return len(scanner.parenForHeaders) > 0 &&
		len(scanner.parenForHeaders) == len(scanner.parenControls) &&
		len(scanner.parenForSeparators) == len(scanner.parenControls) &&
		len(scanner.parenForBracketDepths) == len(scanner.parenControls) &&
		len(scanner.parenForBraceDepths) == len(scanner.parenControls) &&
		scanner.parenForHeaders[len(scanner.parenForHeaders)-1] &&
		!scanner.parenForSeparators[len(scanner.parenForSeparators)-1] &&
		scanner.bracketDepth == scanner.parenForBracketDepths[len(scanner.parenForBracketDepths)-1] &&
		len(scanner.braceBlocks) == scanner.parenForBraceDepths[len(scanner.parenForBraceDepths)-1]
}

func (scanner *javascriptFallbackScanner) contextualAwaitOrYieldIsOperator(
	word string,
) bool {
	return scanner.activeFunctionMode().treatsContextualWordAsOperator(word)
}

func (scanner *javascriptFallbackScanner) activeFunctionMode() javascriptFallbackFunctionMode {
	mode, _ := scanner.activeFunctionContext()
	return mode
}

func (scanner *javascriptFallbackScanner) activeFunctionContext() (
	javascriptFallbackFunctionMode,
	int,
) {
	bestID := 0
	mode := javascriptFallbackNoFunction
	if len(scanner.braceFunctionModes) > 0 &&
		len(scanner.braceFunctionIDs) == len(scanner.braceFunctionModes) {
		bestID = scanner.braceFunctionIDs[len(scanner.braceFunctionIDs)-1]
		mode = scanner.braceFunctionModes[len(scanner.braceFunctionModes)-1]
	}
	if scanner.pendingArrowID > bestID {
		bestID = scanner.pendingArrowID
		mode = scanner.pendingArrowMode
	}
	return mode, bestID
}

func (scanner *javascriptFallbackScanner) clearDirectMemberPrefix() {
	scanner.directMemberPrefix = false
	scanner.directMemberPrefixAsync = false
	scanner.directMemberPrefixGen = false
}

func (scanner *javascriptFallbackScanner) clearDirectMemberCandidate() {
	scanner.directMemberCandidate = false
	scanner.directMemberAsync = false
	scanner.directMemberGenerator = false
}

func javascriptFallbackTokenCanEndExpression(token string) bool {
	if javascriptSourceName(token) || javascriptNumberStart(token, 0) ||
		len(token) > 0 && (token[0] == '\'' || token[0] == '"' || token[0] == '`') {
		return true
	}
	switch token {
	case ")", "]", "}", "++", "--":
		return true
	default:
		return false
	}
}

func javascriptFallbackBindingKeywordPlausible(source string, offset int, word string) bool {
	if word != "let" && word != "using" {
		return true
	}
	offset = javascriptFallbackSkipTrivia(source, offset)
	return offset < len(source) && (javascriptIdentifierStartAt(source, offset) ||
		source[offset] == '{' || source[offset] == '[')
}

func javascriptFallbackCallableKeywordPlausible(source string, offset int, word string) bool {
	offset = javascriptFallbackSkipTrivia(source, offset)
	if offset >= len(source) {
		return false
	}
	if word == "function" {
		if source[offset] == '*' {
			offset = javascriptFallbackSkipTrivia(source, offset+1)
		}
		return offset < len(source) &&
			(source[offset] == '(' || javascriptIdentifierStartAt(source, offset))
	}
	return source[offset] == '{' || javascriptIdentifierStartAt(source, offset)
}

func javascriptFallbackSkipTrivia(source string, offset int) int {
	for offset < len(source) {
		if size := javascriptWhitespaceSize(source, offset); size > 0 {
			offset += size
			continue
		}
		if strings.HasPrefix(source[offset:], "//") {
			offset = javascriptLineTerminatorOffset(source, offset+2)
			continue
		}
		if strings.HasPrefix(source[offset:], "/*") {
			end := strings.Index(source[offset+2:], "*/")
			if end < 0 {
				return len(source)
			}
			offset += end + 4
			continue
		}
		return offset
	}
	return offset
}

func (scanner *javascriptFallbackScanner) beginBindingDeclaration() {
	scanner.bindingDeclaration = true
	scanner.bindingNeedsInitializer = true
	scanner.bindingCanEnd = false
	scanner.bindingParenDepth = len(scanner.parenControls)
	scanner.bindingBracketDepth = scanner.bracketDepth
	scanner.bindingBraceDepth = len(scanner.braceBlocks)
}

func (scanner *javascriptFallbackScanner) bindingAtCurrentDepth() bool {
	return scanner.bindingDeclaration &&
		scanner.bindingParenDepth == len(scanner.parenControls) &&
		scanner.bindingBracketDepth == scanner.bracketDepth &&
		scanner.bindingBraceDepth == len(scanner.braceBlocks)
}

func (scanner *javascriptFallbackScanner) bindingCanEndAtCurrentDepth() bool {
	return scanner.bindingNeedsInitializer && scanner.bindingCanEnd &&
		scanner.bindingAtCurrentDepth()
}

func (scanner *javascriptFallbackScanner) clearBindingDeclaration() {
	scanner.bindingDeclaration = false
	scanner.bindingNeedsInitializer = false
	scanner.bindingCanEnd = false
}

func (scanner *javascriptFallbackScanner) recordValueToken(token string) {
	scanner.previousToken = token
	scanner.previousWord = ""
	scanner.expressionAllowed = false
	scanner.clearDirectMemberCandidate()
	scanner.clearDirectMemberPrefix()
	scanner.closedParenAsyncArrow = false
	scanner.identifierAsyncArrow = false
}

func javascriptFallbackBraceIsBlock(previousToken string, expressionAllowed bool) bool {
	switch previousToken {
	case "", ";", "{", "}", ")", "=>", "class", "else", "try", "finally", "do":
		return true
	case "=", "(", "[", ",", ":", "?", "return", "throw":
		return false
	default:
		return !expressionAllowed
	}
}

func javascriptFallbackKeywordStartsValue(previousToken, previousWord string) bool {
	switch previousToken {
	case "=", "(", "[", ",", ":", "?", "=>", "!", "~", "+", "-", "*",
		"/", "%", "**", "&&", "||", "??", "&", "|", "^", "<<", ">>",
		">>>", "<", ">", "<=", ">=", "==", "!=", "===", "!==", "+=", "-=",
		"*=", "/=", "%=", "&=", "|=", "^=", "&&=", "||=", "??=":
		return true
	}
	switch previousWord {
	case "return", "throw", "yield", "await", "new", "extends", "in", "of",
		"instanceof", "delete", "void", "typeof", "case":
		return true
	default:
		return false
	}
}

func javascriptFallbackRegexCanEnd(source string, offset int) bool {
	for offset < len(source) {
		if size := javascriptWhitespaceSize(source, offset); size > 0 {
			if javascriptLineTerminatorSize(source, offset) > 0 {
				return true
			}
			offset += size
			continue
		}
		if strings.HasPrefix(source[offset:], "//") {
			return true
		}
		if strings.HasPrefix(source[offset:], "/*") {
			end := strings.Index(source[offset+2:], "*/")
			if end < 0 {
				return true
			}
			end += offset + 4
			for cursor := offset + 2; cursor < end-2; {
				if size := javascriptLineTerminatorSize(source, cursor); size > 0 {
					return true
				} else if size := javascriptWhitespaceSize(source, cursor); size > 0 {
					cursor += size
				} else {
					_, size := utf8.DecodeRuneInString(source[cursor:])
					if size < 1 {
						size = 1
					}
					cursor += size
				}
			}
			offset = end
			continue
		}
		break
	}
	if offset >= len(source) {
		return true
	}
	if javascriptNumberStart(source, offset) || source[offset] == '\'' ||
		source[offset] == '"' {
		return false
	}
	if !javascriptIdentifierStartAt(source, offset) {
		return true
	}
	end := javascriptIdentifierSourceEnd(source, offset)
	switch javascriptDecodedIdentifier(source[offset:end]) {
	case "in", "instanceof", "of":
		return true
	default:
		return false
	}
}

func javascriptControlHeaderKeyword(word string) bool {
	switch word {
	case "if", "for", "while", "with", "switch", "catch":
		return true
	default:
		return false
	}
}

func javascriptKeywordAllowsExpression(word string) bool {
	switch word {
	case "return", "throw", "case", "delete", "void", "typeof", "new", "yield",
		"await", "else", "do", "in", "instanceof", "of", "extends", "default",
		"const", "let", "var", "using", "import", "export", "function", "class":
		return true
	case "this", "super", "true", "false", "null":
		return false
	default:
		return false
	}
}

func javascriptWhitespaceSize(source string, offset int) int {
	if offset < 0 || offset >= len(source) {
		return 0
	}
	switch source[offset] {
	case ' ', '\t', '\v', '\f', '\r', '\n':
		return 1
	}
	r, size := utf8.DecodeRuneInString(source[offset:])
	if r == '\u00A0' || r == '\uFEFF' || r == '\u2028' || r == '\u2029' ||
		r == '\u1680' || r >= '\u2000' && r <= '\u200A' || r == '\u202F' ||
		r == '\u205F' || r == '\u3000' {
		return size
	}
	return 0
}

func javascriptLineTerminatorOffset(source string, offset int) int {
	for offset < len(source) {
		if source[offset] == '\r' || source[offset] == '\n' {
			return offset
		}
		r, size := utf8.DecodeRuneInString(source[offset:])
		if r == '\u2028' || r == '\u2029' {
			return offset
		}
		if size < 1 {
			size = 1
		}
		offset += size
	}
	return len(source)
}

func javascriptQuotedLiteralEnd(source string, start int, quote byte) int {
	for offset := start + 1; offset < len(source); {
		if source[offset] == quote {
			return offset + 1
		}
		if source[offset] == '\r' || source[offset] == '\n' {
			return offset
		}
		r, size := utf8.DecodeRuneInString(source[offset:])
		if r == '\u2028' || r == '\u2029' {
			return offset
		}
		if source[offset] == '\\' {
			offset++
			if offset >= len(source) {
				return offset
			}
			if source[offset] == '\r' {
				offset++
				if offset < len(source) && source[offset] == '\n' {
					offset++
				}
				continue
			}
			if source[offset] == '\n' {
				offset++
				continue
			}
			_, size = utf8.DecodeRuneInString(source[offset:])
			if size < 1 {
				size = 1
			}
			offset += size
			continue
		}
		if size < 1 {
			size = 1
		}
		offset += size
	}
	return len(source)
}

func javascriptRegexLiteralEnd(source string, start int) int {
	inClass := false
	for offset := start + 1; offset < len(source); {
		if source[offset] == '\r' || source[offset] == '\n' {
			return offset
		}
		r, size := utf8.DecodeRuneInString(source[offset:])
		if r == '\u2028' || r == '\u2029' {
			return offset
		}
		if source[offset] == '\\' {
			offset++
			if offset < len(source) {
				_, size = utf8.DecodeRuneInString(source[offset:])
				if size < 1 {
					size = 1
				}
				offset += size
			}
			continue
		}
		switch source[offset] {
		case '[':
			inClass = true
		case ']':
			inClass = false
		case '/':
			if !inClass {
				offset++
				for offset < len(source) && javascriptIdentifierContinueAt(source, offset) {
					_, size, ok := javascriptIdentifierRune(source, offset)
					if !ok || size < 1 {
						break
					}
					offset += size
				}
				return offset
			}
		}
		if size < 1 {
			size = 1
		}
		offset += size
	}
	return len(source)
}

func javascriptLineTerminatorSize(source string, offset int) int {
	if offset < 0 || offset >= len(source) {
		return 0
	}
	if source[offset] == '\r' || source[offset] == '\n' {
		return 1
	}
	r, size := utf8.DecodeRuneInString(source[offset:])
	if r == '\u2028' || r == '\u2029' {
		return size
	}
	return 0
}

func javascriptIdentifierStartAt(source string, offset int) bool {
	r, _, ok := javascriptIdentifierRune(source, offset)
	return ok && javascriptIdentifierStartRune(r)
}

func javascriptIdentifierContinueAt(source string, offset int) bool {
	r, _, ok := javascriptIdentifierRune(source, offset)
	return ok && javascriptIdentifierContinueRune(r)
}

func javascriptIdentifierSourceEnd(source string, start int) int {
	offset := start
	for offset < len(source) {
		r, size, ok := javascriptIdentifierRune(source, offset)
		if !ok || (offset == start && !javascriptIdentifierStartRune(r)) ||
			(offset > start && !javascriptIdentifierContinueRune(r)) {
			break
		}
		offset += size
	}
	return offset
}

func javascriptNumberStart(source string, offset int) bool {
	if offset < 0 || offset >= len(source) {
		return false
	}
	return source[offset] >= '0' && source[offset] <= '9' ||
		source[offset] == '.' && offset+1 < len(source) &&
			source[offset+1] >= '0' && source[offset+1] <= '9'
}

func javascriptNumberEnd(source string, start int) int {
	offset := start
	for offset < len(source) {
		character := source[offset]
		if character >= '0' && character <= '9' || character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' || character == '_' || character == '.' {
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
	return offset
}

type javascriptToken struct {
	text     string
	position uint64
}

const (
	javascriptTokenLineStart uint64 = 1 << (62 + iota)
	javascriptTokenLiteral
	javascriptTokenLengthMask uint64 = 1<<30 - 1
)

const (
	javascriptLexicalValueToken    = "\x00value"
	javascriptLexicalTemplateToken = "\x00template"
)

func newJavaScriptToken(text string, start int, lineStart, literal bool) javascriptToken {
	flags := uint64(0)
	if lineStart {
		flags |= javascriptTokenLineStart
	}
	if literal {
		flags |= javascriptTokenLiteral
	}
	length := len(text)
	maximumStart := uint64(^uint32(0))
	if uint64(start) > maximumStart {
		// Sources large enough to exceed this compact representation cannot be
		// materialized by the parser budget in practice. Clamp defensively so
		// malformed caller input cannot wrap coordinates.
		start = int(maximumStart)
	}
	if uint64(length) > javascriptTokenLengthMask {
		length = int(javascriptTokenLengthMask)
	}
	if literal {
		if strings.HasPrefix(text, "`") {
			text = javascriptLexicalTemplateToken
		} else {
			text = javascriptLexicalValueToken
		}
	}
	return javascriptToken{
		text: text,
		position: uint64(uint32(start)) |
			uint64(length)<<32 | flags,
	}
}

func (token javascriptToken) startOffset() int {
	return int(uint32(token.position))
}

func (token javascriptToken) endOffset() int {
	return token.startOffset() + int(token.position>>32&javascriptTokenLengthMask)
}

func (token javascriptToken) startsLine() bool {
	return token.position&javascriptTokenLineStart != 0
}

func (token javascriptToken) literal() bool {
	return token.position&javascriptTokenLiteral != 0
}

type javascriptLexDefinition struct {
	definition sourceDefinition
	strong     bool
	force      bool
}

type javascriptLexResult struct {
	delimiters  javascriptDelimiterPairs
	definitions []javascriptLexDefinition
	imports     []javascriptLineSpan
	scopes      []javascriptLineScope
	tokens      []javascriptToken
}

const javascriptMaximumRecoveryLookahead = 256

func lexJavaScript(
	source string,
	commentSpans, literalSpans []javascriptByteSpan,
	recoverSyntax bool,
	recoveryLines map[int]bool,
) javascriptLexResult {
	return lexJavaScriptWithHints(
		source, commentSpans, literalSpans, recoverSyntax, recoveryLines,
		javascriptFallbackResult{},
	)
}

func lexJavaScriptWithHints(
	source string,
	commentSpans, literalSpans []javascriptByteSpan,
	recoverSyntax bool,
	recoveryLines map[int]bool,
	hints javascriptFallbackResult,
) javascriptLexResult {
	return lexJavaScriptWithHintsFlavor(
		source,
		commentSpans,
		literalSpans,
		recoverSyntax,
		recoveryLines,
		hints,
		javascriptSyntaxFlavorJavaScript,
	)
}

func lexJavaScriptWithHintsFlavor(
	source string,
	commentSpans, literalSpans []javascriptByteSpan,
	recoverSyntax bool,
	recoveryLines map[int]bool,
	hints javascriptFallbackResult,
	flavor javascriptSyntaxFlavor,
) javascriptLexResult {
	if !recoverSyntax {
		return javascriptLexResult{}
	}
	tokenComments := commentSpans
	tokenLiterals := literalSpans
	if hints.available {
		tokenComments = append(
			append([]javascriptByteSpan(nil), hints.comments...),
			hints.lexicalSkips...,
		)
		tokenLiterals = hints.lexicalLiterals
	}
	tokens := tokenizeJavaScriptWithReplacements(
		source, tokenComments, tokenLiterals, hints.lexicalReplacements,
		hints.tokenCapacity,
	)
	delimiters := javascriptMatchDelimiters(tokens)
	objectBraces := hints.objectBraces
	if objectBraces == nil {
		objectBraces = scanJavaScriptFallback(source).objectBraces
	}
	forcedStatementStarts := javascriptLexicalForcedStatementStarts(
		tokens, hints.forcedStatementStarts,
	)
	boundaries := javascriptBuildLexBoundaries(
		source, tokens, delimiters, objectBraces, forcedStatementStarts,
	)
	definitions := javascriptLexDefinitions(
		source, tokens, delimiters, boundaries, recoveryLines, objectBraces,
		commentSpans, flavor.isTypeScript(),
	)
	imports := javascriptLexImports(
		source, tokens, delimiters, boundaries, commentSpans, hints.jsxValues,
		objectBraces,
	)
	scopes := javascriptLexScopes(
		source, tokens, delimiters, boundaries, commentSpans, objectBraces,
	)
	if flavor.isTypeScript() {
		typeScript := typeScriptLexRecovery(
			source, flavor, tokens, delimiters, boundaries, commentSpans, recoveryLines,
		)
		definitions = typeScriptDefinitionsOutsideRecoveryCoverage(
			definitions, typeScript.covered,
		)
		definitions = javascriptLexUniqueDefinitions(append(
			definitions, typeScript.definitions...,
		))
		imports = typeScriptImportsOutsideRecoveryCoverage(imports, typeScript.covered)
		imports = normalizeJavaScriptLineSpans(append(imports, typeScript.imports...))
		scopes = typeScriptScopesOutsideRecoveryCoverage(scopes, typeScript.covered)
		scopes = normalizeJavaScriptScopes(append(scopes, typeScript.scopes...))
	}
	return javascriptLexResult{
		tokens:      tokens,
		delimiters:  delimiters,
		definitions: definitions,
		imports:     imports,
		scopes:      scopes,
	}
}

func javascriptLexicalForcedStatementStarts(
	tokens []javascriptToken,
	offsets []int,
) []bool {
	if len(tokens) == 0 || len(offsets) == 0 {
		return nil
	}
	offsets = append([]int(nil), offsets...)
	sort.Ints(offsets)
	starts := make([]bool, len(tokens))
	offsetIndex := 0
	for index, token := range tokens {
		start := token.startOffset()
		if offsetIndex < len(offsets) && offsets[offsetIndex] <= start {
			starts[index] = true
			for offsetIndex < len(offsets) && offsets[offsetIndex] <= start {
				offsetIndex++
			}
		}
	}
	return starts
}

func tokenizeJavaScript(
	source string,
	commentSpans, literalSpans []javascriptByteSpan,
	capacity int,
) []javascriptToken {
	return tokenizeJavaScriptWithReplacements(
		source, commentSpans, literalSpans, nil, capacity,
	)
}

func tokenizeJavaScriptWithReplacements(
	source string,
	commentSpans, literalSpans []javascriptByteSpan,
	replacements []javascriptTokenReplacement,
	capacity int,
) []javascriptToken {
	comments := normalizeJavaScriptSpans(append([]javascriptByteSpan(nil), commentSpans...))
	literals := normalizeJavaScriptSpans(append([]javascriptByteSpan(nil), literalSpans...))
	replacements = append([]javascriptTokenReplacement(nil), replacements...)
	sort.Slice(replacements, func(first, second int) bool {
		return replacements[first].offset < replacements[second].offset
	})
	commentIndex, literalIndex, replacementIndex := 0, 0, 0
	tokens := make([]javascriptToken, 0, max(capacity, 0))
	lineHasToken := false
	for offset := 0; offset < len(source); {
		for replacementIndex < len(replacements) &&
			replacements[replacementIndex].offset < offset {
			replacementIndex++
		}
		for commentIndex < len(comments) && comments[commentIndex].end <= offset {
			commentIndex++
		}
		for literalIndex < len(literals) && literals[literalIndex].end <= offset {
			literalIndex++
		}
		if replacementIndex < len(replacements) &&
			replacements[replacementIndex].offset == offset {
			tokens = append(tokens, newJavaScriptToken(
				replacements[replacementIndex].text, offset, !lineHasToken, false,
			))
			lineHasToken = true
			offset++
			replacementIndex++
			continue
		}
		if commentIndex < len(comments) && comments[commentIndex].start <= offset {
			end := min(comments[commentIndex].end, len(source))
			lineHasToken = javascriptLineHasTokenAfterComment(
				source, offset, end, lineHasToken,
			)
			offset = end
			continue
		}
		if literalIndex < len(literals) && literals[literalIndex].start <= offset {
			end := min(literals[literalIndex].end, len(source))
			tokens = append(tokens, newJavaScriptToken(
				source[offset:end], offset, !lineHasToken, true,
			))
			lineHasToken = javascriptLineHasTokenAfterSpan(
				source, offset, end, true,
			)
			offset = end
			continue
		}
		if size := javascriptWhitespaceSize(source, offset); size > 0 {
			r, _ := utf8.DecodeRuneInString(source[offset:])
			if source[offset] == '\r' || source[offset] == '\n' || r == '\u2028' || r == '\u2029' {
				lineHasToken = false
			}
			offset += size
			continue
		}
		start := offset
		switch {
		case source[offset] == '#' && offset+1 < len(source) &&
			javascriptIdentifierStartAt(source, offset+1):
			offset = javascriptIdentifierSourceEnd(source, offset+1)
		case javascriptIdentifierStartAt(source, offset):
			offset = javascriptIdentifierSourceEnd(source, offset)
		case javascriptNumberStart(source, offset):
			offset = javascriptNumberEnd(source, offset)
		default:
			offset = javascriptPunctuationEnd(source, offset)
		}
		if offset <= start {
			offset = start + 1
		}
		tokens = append(tokens, newJavaScriptToken(
			source[start:offset], start, !lineHasToken, false,
		))
		lineHasToken = true
	}
	return tokens
}

func javascriptLineHasTokenAfterComment(source string, start, end int, before bool) bool {
	lineHasToken := before
	for offset := start; offset < end; {
		if source[offset] == '\r' || source[offset] == '\n' {
			lineHasToken = false
			offset++
			continue
		}
		r, size := utf8.DecodeRuneInString(source[offset:])
		if r == '\u2028' || r == '\u2029' {
			lineHasToken = false
		}
		if size < 1 {
			size = 1
		}
		offset += size
	}
	return lineHasToken
}

func javascriptLineHasTokenAfterSpan(source string, start, end int, before bool) bool {
	lineHasToken := before
	for offset := start; offset < end; {
		if source[offset] == '\r' || source[offset] == '\n' {
			lineHasToken = false
			offset++
			continue
		}
		r, size := utf8.DecodeRuneInString(source[offset:])
		if r == '\u2028' || r == '\u2029' {
			lineHasToken = false
		} else if javascriptWhitespaceSize(source, offset) == 0 {
			lineHasToken = true
		}
		if size < 1 {
			size = 1
		}
		offset += size
	}
	return lineHasToken
}

func javascriptPunctuationEnd(source string, offset int) int {
	for _, punctuation := range []string{
		">>>=", "===", "!==", ">>>", "**=", "&&=", "||=", "??=", "...",
		"=>", "==", "!=", "<=", ">=", "++", "--", "&&", "||", "??", "?.",
		"**", "<<", ">>", "+=", "-=", "*=", "/=", "%=", "&=", "|=", "^=",
	} {
		if strings.HasPrefix(source[offset:], punctuation) {
			return offset + len(punctuation)
		}
	}
	_, size := utf8.DecodeRuneInString(source[offset:])
	if size < 1 {
		size = 1
	}
	return offset + size
}

type javascriptDelimiterPairs struct {
	pairs []uint32
}

func (pairs javascriptDelimiterPairs) get(index int) (int, bool) {
	if index < 0 || index >= len(pairs.pairs) || pairs.pairs[index] == 0 {
		return 0, false
	}
	return int(pairs.pairs[index] - 1), true
}

func (pairs javascriptDelimiterPairs) at(index int) int {
	paired, ok := pairs.get(index)
	if !ok {
		return -1
	}
	return paired
}

func javascriptMatchDelimiters(tokens []javascriptToken) javascriptDelimiterPairs {
	result := javascriptDelimiterPairs{pairs: make([]uint32, len(tokens))}
	stack := make([]int, 0, 16)
	var typeStacks [3][]int
	active := make([]bool, len(tokens))
	for index, token := range tokens {
		kind, opener, delimiter := javascriptDelimiterKind(token.text)
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
		result.pairs[open] = uint32(index) + 1
		result.pairs[index] = uint32(open) + 1
	}
	return result
}

type javascriptLexBoundaries struct {
	statementScan []uint32
	controlEnds   []uint32
	forcedStarts  []bool
}

func (boundaries javascriptLexBoundaries) statementEnd(start int) int {
	if start < 0 || start+1 >= len(boundaries.statementScan) {
		return -1
	}
	return int(boundaries.statementScan[start+1]) - 1
}

func (boundaries javascriptLexBoundaries) controlEnd(start int) int {
	if start < 0 || start >= len(boundaries.controlEnds) ||
		boundaries.controlEnds[start] == 0 {
		return -1
	}
	return int(boundaries.controlEnds[start]) - 1
}

func javascriptBuildLexBoundaries(
	source string,
	tokens []javascriptToken,
	delimiters javascriptDelimiterPairs,
	objectBraces []javascriptByteSpan,
	forcedStarts []bool,
) javascriptLexBoundaries {
	count := len(tokens)
	objectStarts := make(map[int]bool, len(objectBraces))
	for _, span := range objectBraces {
		objectStarts[span.start] = true
	}
	valueBraceStarts := javascriptLexicalValueBraceStarts(tokens, delimiters, objectStarts)
	contexts, _ := javascriptLexicalBraceContexts(source, tokens, delimiters, objectBraces)
	valueBraceEnds := make([]bool, count)
	for start := range valueBraceStarts {
		if end, paired := delimiters.get(start); paired && end > start {
			valueBraceEnds[end] = true
		}
	}
	statementScan := make([]uint32, count+1)
	statementScan[count] = uint32(count)
	for index := count - 1; index >= 0; index-- {
		if _, opener, delimiter := javascriptDelimiterKind(tokens[index].text); delimiter && opener && delimiters.at(index) > index {
			statementScan[index] = statementScan[delimiters.at(index)+1]
			continue
		}
		switch {
		case tokens[index].text == ";":
			statementScan[index] = uint32(index + 1)
		case index < len(forcedStarts) && forcedStarts[index]:
			statementScan[index] = uint32(index)
		case index > 0 && tokens[index-1].text == "}" &&
			!valueBraceEnds[index-1] &&
			!javascriptLexControlContinuationAt(tokens, index, delimiters, contexts):
			statementScan[index] = uint32(index)
		case index > 0 && tokens[index].startsLine() &&
			javascriptHardDeclarationToken(tokens[index].text) &&
			javascriptLexHardStatementStart(tokens, index, delimiters) &&
			!javascriptLexContinuesExportDeclaration(tokens, index):
			statementScan[index] = uint32(index)
		case index > 0 && tokens[index].startsLine() &&
			javascriptLexStatementCanEnd(tokens[index-1]) &&
			tokens[index].text != ")" && tokens[index].text != "]" &&
			tokens[index].text != "}" &&
			!javascriptTokenContinuesExpression(tokens[index]):
			statementScan[index] = uint32(index)
		default:
			statementScan[index] = statementScan[index+1]
		}
	}

	controlEnds := make([]uint32, count)
	handlerTails := make([]uint32, count)
	boundaries := javascriptLexBoundaries{
		statementScan: statementScan,
		controlEnds:   controlEnds,
		forcedStarts:  forcedStarts,
	}
	for index := count - 1; index >= 0; index-- {
		if !javascriptLexControlScopeToken(tokens[index].text) {
			continue
		}
		if index < len(contexts) &&
			(contexts[index] == javascriptLexicalBraceObject ||
				contexts[index] == javascriptLexicalBraceClass) &&
			javascriptLexMethodAt(tokens, index, delimiters) {
			continue
		}
		bodyStart := javascriptLexControlBodyStart(tokens, index, delimiters)
		if bodyStart < 0 || bodyStart >= count {
			continue
		}
		var end int
		if tokens[bodyStart].text == "{" && delimiters.at(bodyStart) > bodyStart {
			end = delimiters.at(bodyStart)
		} else if controlEnd := boundaries.controlEnd(bodyStart); controlEnd > bodyStart {
			end = controlEnd
		} else {
			end = boundaries.statementEnd(bodyStart)
		}
		if end < bodyStart {
			continue
		}
		switch tokens[index].text {
		case "if":
			next := end + 1
			if next < count && tokens[next].text == "else" {
				end = max(end, boundaries.controlEnd(next))
			}
		case "try":
			next := end + 1
			if next < count && (tokens[next].text == "catch" ||
				tokens[next].text == "finally") && handlerTails[next] != 0 {
				end = max(end, int(handlerTails[next])-1)
			}
		case "do":
			if end+1 < count && tokens[end+1].text == "while" {
				conditionOpen := end + 2
				if conditionClose, paired := delimiters.get(conditionOpen); paired &&
					conditionClose > conditionOpen {
					end = conditionClose
					if end+1 < count && tokens[end+1].text == ";" {
						end++
					}
				}
			}
		}
		controlEnds[index] = uint32(end + 1)
		if tokens[index].text == "catch" || tokens[index].text == "finally" {
			tail := end
			next := tail + 1
			if next < count && (tokens[next].text == "catch" ||
				tokens[next].text == "finally") && handlerTails[next] != 0 {
				tail = max(tail, int(handlerTails[next])-1)
			}
			handlerTails[index] = uint32(tail + 1)
		}
	}
	return boundaries
}

func javascriptLexControlBodyStart(
	tokens []javascriptToken,
	start int,
	delimiters javascriptDelimiterPairs,
) int {
	if start < 0 || start >= len(tokens) {
		return -1
	}
	if start > 0 && (tokens[start-1].text == "." || tokens[start-1].text == "?.") {
		return -1
	}
	bodyStart := start + 1
	if tokens[start].text == "for" && bodyStart < len(tokens) &&
		tokens[bodyStart].text == "await" {
		bodyStart++
	}
	switch tokens[start].text {
	case "catch":
		if bodyStart < len(tokens) && tokens[bodyStart].text == "{" {
			return bodyStart
		}
		fallthrough
	case "if", "for", "while", "with", "switch":
		if bodyStart >= len(tokens) || tokens[bodyStart].text != "(" {
			return -1
		}
		closingIndex, paired := delimiters.get(bodyStart)
		if !paired || closingIndex <= bodyStart {
			return -1
		}
		return closingIndex + 1
	case "else", "do", "try", "finally":
		return bodyStart
	default:
		return -1
	}
}

func javascriptDelimiterKind(token string) (kind int, opener, delimiter bool) {
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

func javascriptLexDefinitions(
	source string,
	tokens []javascriptToken,
	delimiters javascriptDelimiterPairs,
	boundaries javascriptLexBoundaries,
	recoveryLines map[int]bool,
	objectBraces []javascriptByteSpan,
	commentSpans []javascriptByteSpan,
	typeScript bool,
) []javascriptLexDefinition {
	positions := javascriptSourcePositions{source: source, lineStarts: javascriptLineStarts(source)}
	definitions := make([]javascriptLexDefinition, 0)
	needsPropertyContexts := javascriptLexNeedsPropertyContexts(tokens, delimiters)
	var contexts []uint8
	var containerEnds []int32
	if needsPropertyContexts {
		contexts, containerEnds = javascriptLexicalBraceContexts(
			source, tokens, delimiters, objectBraces,
		)
	}
	for index, token := range tokens {
		switch token.text {
		case "function":
			if index < len(contexts) && contexts[index] == javascriptLexicalBraceClass &&
				javascriptLexClassMemberKeywordAt(tokens, index) {
				continue
			}
			if !javascriptLexDefinitionCandidate(
				tokens, index, delimiters, positions, recoveryLines,
			) {
				continue
			}
			if definition, ok := javascriptLexFunctionDefinition(
				tokens, index, delimiters, positions,
			); ok {
				definitions = append(definitions, javascriptLexDefinition{
					definition: definition,
					strong:     true,
				})
			}
		case "class":
			if index < len(contexts) && contexts[index] == javascriptLexicalBraceClass &&
				javascriptLexClassMemberKeywordAt(tokens, index) {
				continue
			}
			if !javascriptLexDefinitionCandidate(
				tokens, index, delimiters, positions, recoveryLines,
			) {
				continue
			}
			if definition, ok := javascriptLexClassDefinition(
				tokens, index, delimiters, positions,
			); ok {
				definitions = append(definitions, javascriptLexDefinition{
					definition: definition,
					strong:     true,
				})
			}
		case "const", "let", "var", "using":
			if !javascriptLexDefinitionCandidate(
				tokens, index, delimiters, positions, recoveryLines,
			) {
				continue
			}
			definitions = append(
				definitions,
				javascriptLexVariableDefinitions(
					tokens, index, delimiters, positions, typeScript,
				)...,
			)
		}
	}
	if !needsPropertyContexts {
		return javascriptLexUniqueDefinitions(javascriptLexAttachDefinitionScopes(
			source, tokens, delimiters, boundaries, positions, commentSpans,
			objectBraces, definitions,
		))
	}
	definitions = append(
		definitions,
		javascriptLexMethodDefinitions(
			source, tokens, delimiters, positions, recoveryLines, contexts, commentSpans,
		)...,
	)
	definitions = append(
		definitions,
		javascriptLexCallablePropertyDefinitions(
			source,
			tokens,
			delimiters,
			positions,
			recoveryLines,
			contexts,
			containerEnds,
			commentSpans,
			typeScript,
		)...,
	)
	return javascriptLexUniqueDefinitions(javascriptLexAttachDefinitionScopes(
		source, tokens, delimiters, boundaries, positions, commentSpans,
		objectBraces, definitions,
	))
}

func javascriptLexAttachDefinitionScopes(
	source string,
	tokens []javascriptToken,
	delimiters javascriptDelimiterPairs,
	boundaries javascriptLexBoundaries,
	positions javascriptSourcePositions,
	commentSpans []javascriptByteSpan,
	objectBraces []javascriptByteSpan,
	definitions []javascriptLexDefinition,
) []javascriptLexDefinition {
	statementStarts := javascriptLexicalStatementStarts(
		source, tokens, delimiters, objectBraces, boundaries.forcedStarts,
	)
	for index := range definitions {
		candidate := &definitions[index]
		definition := &candidate.definition
		if !candidate.strong || !definition.ownsScope || definition.line < 1 ||
			definition.line > len(positions.lineStarts) {
			continue
		}
		offset := positions.lineStarts[definition.line-1] + definition.column - 1
		tokenIndex := sort.Search(len(tokens), func(index int) bool {
			return tokens[index].startOffset() >= offset
		})
		if tokenIndex >= len(tokens) || tokens[tokenIndex].startOffset() != offset {
			continue
		}
		statementStart := statementStarts[tokenIndex]
		if tokenIndex > 0 && tokens[tokenIndex-1].text == "class" {
			prefixStart := javascriptLexDecoratorPrefixStart(
				tokens, tokenIndex-1, delimiters,
			)
			statementStart = statementStarts[prefixStart]
		}
		statementEnd := boundaries.statementEnd(statementStart)
		startOffset := javascriptLexAttachedStart(
			source, tokens[statementStart].startOffset(), commentSpans,
		)
		definition.scopeStart, _ = positions.lineColumn(startOffset)
		if statementEnd >= statementStart && statementEnd < len(tokens) {
			endLine, _ := positions.lineColumn(max(tokens[statementEnd].endOffset()-1, 0))
			definition.scopeEnd = max(definition.scopeEnd, endLine)
		}
	}
	return definitions
}

func javascriptLexNeedsPropertyContexts(
	tokens []javascriptToken,
	delimiters javascriptDelimiterPairs,
) bool {
	for index, token := range tokens {
		if token.text == "=" || token.text == ":" || token.text == "class" ||
			javascriptLexMethodAt(tokens, index, delimiters) {
			return true
		}
	}
	return false
}

func javascriptLexUniqueDefinitions(
	definitions []javascriptLexDefinition,
) []javascriptLexDefinition {
	sort.SliceStable(definitions, func(first, second int) bool {
		left, right := definitions[first].definition, definitions[second].definition
		if left.line != right.line {
			return left.line < right.line
		}
		if left.column != right.column {
			return left.column < right.column
		}
		return left.symbol < right.symbol
	})
	unique := definitions[:0]
	seen := make(map[javascriptDefinitionIdentity]int, len(definitions))
	for _, candidate := range definitions {
		definition := candidate.definition
		key := javascriptDefinitionIdentity{
			symbol: definition.symbol,
			line:   definition.line,
			column: definition.column,
		}
		if prior, exists := seen[key]; exists {
			strong := unique[prior].strong || candidate.strong
			force := unique[prior].force || candidate.force
			if candidate.force && !unique[prior].force ||
				javascriptDefinitionHasWiderScope(
					candidate.definition, unique[prior].definition,
				) {
				unique[prior] = candidate
			}
			unique[prior].strong = strong
			unique[prior].force = force
			continue
		}
		seen[key] = len(unique)
		unique = append(unique, candidate)
	}
	return unique
}

func javascriptDefinitionHasWiderScope(candidate, current sourceDefinition) bool {
	if !candidate.ownsScope {
		return false
	}
	if !current.ownsScope {
		return true
	}
	return candidate.scopeStart <= current.scopeStart &&
		candidate.scopeEnd >= current.scopeEnd &&
		(candidate.scopeStart < current.scopeStart || candidate.scopeEnd > current.scopeEnd)
}

func javascriptLexDefinitionCandidate(
	tokens []javascriptToken,
	index int,
	delimiters javascriptDelimiterPairs,
	positions javascriptSourcePositions,
	recoveryLines map[int]bool,
) bool {
	if !javascriptLexDeclarationStart(tokens, index, delimiters) {
		return false
	}
	if recoveryLines == nil {
		return true
	}
	line, _ := positions.lineColumn(tokens[index].startOffset())
	return recoveryLines[line]
}

func javascriptLexFunctionDefinition(
	tokens []javascriptToken,
	keywordIndex int,
	delimiters javascriptDelimiterPairs,
	positions javascriptSourcePositions,
) (sourceDefinition, bool) {
	cursor := keywordIndex + 1
	if cursor < len(tokens) && tokens[cursor].text == "*" {
		cursor++
	}
	if cursor >= len(tokens) || !javascriptLexBindingName(tokens[cursor].text) {
		return sourceDefinition{}, false
	}
	nameIndex := cursor
	scopeToken := keywordIndex
	if keywordIndex > 0 && tokens[keywordIndex-1].text == "async" {
		scopeToken = keywordIndex - 1
	}
	line, column := positions.lineColumn(tokens[nameIndex].startOffset())
	scopeStart, _ := positions.lineColumn(tokens[scopeToken].startOffset())
	definition := sourceDefinition{
		symbol:     tokens[nameIndex].text,
		line:       line,
		column:     column,
		scopeStart: scopeStart,
		scopeEnd:   line,
	}
	parameterOpen := javascriptNextToken(tokens, nameIndex+1)
	if parameterOpen < 0 {
		return definition, true
	}
	parameterClose, paired := delimiters.get(parameterOpen)
	if !paired || parameterClose <= parameterOpen {
		return definition, true
	}
	bodyIndex := parameterClose + 1
	if bodyIndex < len(tokens) && tokens[bodyIndex].text == "{" {
		if bodyEnd, bodyPaired := delimiters.get(bodyIndex); bodyPaired && bodyEnd > bodyIndex {
			definition.scopeEnd, _ = positions.lineColumn(max(tokens[bodyEnd].endOffset()-1, 0))
			definition.ownsScope = true
		}
	}
	return definition, true
}

func javascriptLexClassDefinition(
	tokens []javascriptToken,
	keywordIndex int,
	delimiters javascriptDelimiterPairs,
	positions javascriptSourcePositions,
) (sourceDefinition, bool) {
	nameIndex := keywordIndex + 1
	if nameIndex >= len(tokens) || !javascriptLexBindingName(tokens[nameIndex].text) {
		return sourceDefinition{}, false
	}
	line, column := positions.lineColumn(tokens[nameIndex].startOffset())
	prefixStart := javascriptLexDecoratorPrefixStart(tokens, keywordIndex, delimiters)
	scopeStart, _ := positions.lineColumn(tokens[prefixStart].startOffset())
	definition := sourceDefinition{
		symbol:     tokens[nameIndex].text,
		line:       line,
		column:     column,
		scopeStart: scopeStart,
		scopeEnd:   line,
	}
	if bodyStart := javascriptLexClassBodyStart(tokens, keywordIndex, delimiters, 0); bodyStart >= 0 {
		if bodyEnd, paired := delimiters.get(bodyStart); paired && bodyEnd > bodyStart {
			definition.scopeEnd, _ = positions.lineColumn(max(tokens[bodyEnd].endOffset()-1, 0))
			definition.ownsScope = true
		}
	}
	return definition, true
}

func javascriptLexClassBodyStart(
	tokens []javascriptToken,
	classIndex int,
	delimiters javascriptDelimiterPairs,
	depth int,
) int {
	if classIndex < 0 || classIndex >= len(tokens) ||
		depth > javascriptMaximumSyntaxUnwrapDepth {
		return -1
	}
	limit := min(len(tokens), classIndex+javascriptMaximumRecoveryLookahead)
	for index := classIndex + 1; index < limit; index++ {
		if tokens[index].text == "class" &&
			javascriptLexClassExpressionCandidate(tokens, index) {
			nestedBody := javascriptLexClassBodyStart(tokens, index, delimiters, depth+1)
			if nestedBody >= 0 {
				if nestedEnd, paired := delimiters.get(nestedBody); paired && nestedEnd > nestedBody {
					index = nestedEnd
					continue
				}
			}
		}
		if tokens[index].text == "{" {
			return index
		}
		if (tokens[index].text == "(" || tokens[index].text == "[") &&
			delimiters.at(index) > index {
			index = delimiters.at(index)
			continue
		}
		if tokens[index].text == ";" || index > classIndex+1 &&
			tokens[index].startsLine() && javascriptHardDeclarationToken(tokens[index].text) {
			return -1
		}
	}
	return -1
}

func javascriptLexVariableDefinitions(
	tokens []javascriptToken,
	keywordIndex int,
	delimiters javascriptDelimiterPairs,
	positions javascriptSourcePositions,
	typeScript bool,
) []javascriptLexDefinition {
	if keywordIndex < 0 || keywordIndex >= len(tokens) || keywordIndex+1 >= len(tokens) ||
		tokens[keywordIndex+1].text == "=" {
		return nil
	}
	definitions := make([]javascriptLexDefinition, 0)
	cursor := keywordIndex + 1
	for cursor < len(tokens) {
		if tokens[cursor].text == ";" {
			break
		}
		patternStart, patternEnd := cursor, cursor
		if (tokens[cursor].text == "{" || tokens[cursor].text == "[") &&
			delimiters.at(cursor) > cursor {
			patternEnd = delimiters.at(cursor)
		} else if !javascriptLexBindingName(tokens[cursor].text) {
			break
		}
		names := javascriptLexBindingNames(tokens, patternStart, patternEnd, delimiters)
		cursor = patternEnd + 1
		if typeScript && cursor < len(tokens) && tokens[cursor].text == ":" {
			cursor = typeScriptLexVariableTypeEnd(tokens, cursor+1, delimiters)
		}
		initializerStart := -1
		initializerEnd := patternEnd
		if cursor < len(tokens) && tokens[cursor].text == "=" {
			initializerStart = cursor + 1
			separator := javascriptLexInitializerSeparator(
				tokens, initializerStart, delimiters, typeScript,
			)
			initializerEnd = max(initializerStart, separator-1)
			cursor = separator
		}
		ownsScope, scopeEnd := javascriptLexInitializerScope(
			tokens, initializerStart, initializerEnd, delimiters, positions,
		)
		for _, nameIndex := range names {
			line, column := positions.lineColumn(tokens[nameIndex].startOffset())
			endLine := line
			if ownsScope {
				endLine = max(line, scopeEnd)
			}
			definitions = append(definitions, javascriptLexDefinition{
				definition: sourceDefinition{
					symbol:     tokens[nameIndex].text,
					line:       line,
					column:     column,
					scopeStart: line,
					scopeEnd:   endLine,
					ownsScope:  ownsScope,
				},
				strong: true,
			})
		}
		if cursor >= len(tokens) || tokens[cursor].text != "," {
			break
		}
		cursor++
	}
	return definitions
}

func javascriptLexInitializerSeparator(
	tokens []javascriptToken,
	start int,
	delimiters javascriptDelimiterPairs,
	typeScript bool,
) int {
	for index := start; index < len(tokens); index++ {
		if tokens[index].startsLine() &&
			javascriptLexApparentBindingDeclaration(tokens, index, delimiters) {
			return index
		}
		if (tokens[index].text == "(" || tokens[index].text == "[" || tokens[index].text == "{") &&
			delimiters.at(index) > index {
			index = delimiters.at(index)
			continue
		}
		if typeScript && tokens[index].text == "<" {
			if end := typeScriptLexGenericArgumentEnd(tokens, index, delimiters); end > index {
				index = end
				continue
			}
		}
		if tokens[index].text == "," || tokens[index].text == ";" {
			return index
		}
		if tokens[index].text == ")" || tokens[index].text == "]" ||
			tokens[index].text == "}" {
			return index
		}
		if index > start &&
			javascriptHardDeclarationToken(tokens[index].text) &&
			javascriptTokenCanEnd(tokens[index-1]) &&
			(tokens[index].text != "import" || index+1 >= len(tokens) ||
				tokens[index+1].text != "(" && tokens[index+1].text != ".") {
			return index
		}
		if index > start && tokens[index].startsLine() &&
			javascriptTokenCanEnd(tokens[index-1]) &&
			!javascriptTokenContinuesExpression(tokens[index]) {
			return index
		}
	}
	return len(tokens)
}

func javascriptLexApparentBindingDeclaration(
	tokens []javascriptToken,
	index int,
	delimiters javascriptDelimiterPairs,
) bool {
	if index < 0 || index+1 >= len(tokens) ||
		!javascriptLexicalBindingDeclarationToken(tokens[index].text) {
		return false
	}
	cursor := index + 1
	switch {
	case tokens[cursor].text == "{" || tokens[cursor].text == "[":
		closingIndex, paired := delimiters.get(cursor)
		if !paired || closingIndex <= cursor {
			return false
		}
		cursor = closingIndex + 1
	case javascriptLexBindingName(tokens[cursor].text):
		cursor++
	default:
		return false
	}
	return cursor >= len(tokens) || tokens[cursor].text == "=" ||
		tokens[cursor].text == "," || tokens[cursor].text == ";" ||
		tokens[cursor].startsLine()
}

func javascriptLexInitializerScope(
	tokens []javascriptToken,
	start, end int,
	delimiters javascriptDelimiterPairs,
	positions javascriptSourcePositions,
) (bool, int) {
	if start < 0 || start >= len(tokens) || end < start {
		return false, 0
	}
	scoped := false
	for index := start; index <= end && index < len(tokens); index++ {
		switch tokens[index].text {
		case "=>", "function":
			scoped = true
		case "class":
			scoped = scoped || javascriptLexClassExpressionCandidate(tokens, index) ||
				javascriptLexMethodAt(tokens, index, delimiters)
		default:
			if javascriptLexMethodAt(tokens, index, delimiters) {
				scoped = true
			}
		}
	}
	if !scoped {
		return false, 0
	}
	line, _ := positions.lineColumn(max(tokens[min(end, len(tokens)-1)].endOffset()-1, 0))
	return true, line
}

func javascriptLexBindingNames(
	tokens []javascriptToken,
	start, end int,
	delimiters javascriptDelimiterPairs,
) []int {
	type bindingRange struct {
		start int
		end   int
	}
	pending := []bindingRange{{start: start, end: end}}
	names := make([]int, 0)
	for len(pending) > 0 {
		last := len(pending) - 1
		current := pending[last]
		pending = pending[:last]
		if current.start < 0 || current.end < current.start || current.end >= len(tokens) {
			continue
		}
		if current.start == current.end {
			if javascriptLexBindingName(tokens[current.start].text) {
				names = append(names, current.start)
			}
			continue
		}
		if tokens[current.start].text != "{" && tokens[current.start].text != "[" {
			continue
		}

		objectPattern := tokens[current.start].text == "{"
		firstChild := len(pending)
		entryStart := current.start + 1
		for entryStart < current.end {
			entryEnd := javascriptLexTopLevelSeparator(
				tokens, entryStart, current.end, ",", delimiters,
			)
			if entryEnd < 0 {
				entryEnd = current.end
			}
			nextEntryStart := entryEnd + 1
			trimmedStart := entryStart
			if trimmedStart < entryEnd && tokens[trimmedStart].text == "..." {
				trimmedStart++
			}
			defaultIndex := javascriptLexTopLevelSeparator(
				tokens, trimmedStart, entryEnd, "=", delimiters,
			)
			if defaultIndex >= 0 {
				entryEnd = defaultIndex
			}
			bindingStart := trimmedStart
			if objectPattern {
				colon := javascriptLexTopLevelSeparator(
					tokens, trimmedStart, entryEnd, ":", delimiters,
				)
				if colon >= 0 {
					bindingStart = colon + 1
				}
			}
			if bindingStart < entryEnd {
				bindingEnd := entryEnd - 1
				if (tokens[bindingStart].text == "{" || tokens[bindingStart].text == "[") &&
					delimiters.at(bindingStart) >= bindingStart &&
					delimiters.at(bindingStart) < entryEnd {
					bindingEnd = delimiters.at(bindingStart)
				}
				pending = append(pending, bindingRange{start: bindingStart, end: bindingEnd})
			}
			entryStart = nextEntryStart
		}
		for left, right := firstChild, len(pending)-1; left < right; left, right = left+1, right-1 {
			pending[left], pending[right] = pending[right], pending[left]
		}
	}
	return names
}

func javascriptLexTopLevelSeparator(
	tokens []javascriptToken,
	start, end int,
	separator string,
	delimiters javascriptDelimiterPairs,
) int {
	for index := start; index < end && index < len(tokens); index++ {
		if (tokens[index].text == "(" || tokens[index].text == "[" || tokens[index].text == "{") &&
			delimiters.at(index) > index && delimiters.at(index) < end {
			index = delimiters.at(index)
			continue
		}
		if tokens[index].text == separator {
			return index
		}
	}
	return -1
}

func javascriptLexMethodDefinitions(
	source string,
	tokens []javascriptToken,
	delimiters javascriptDelimiterPairs,
	positions javascriptSourcePositions,
	recoveryLines map[int]bool,
	contexts []uint8,
	commentSpans []javascriptByteSpan,
) []javascriptLexDefinition {
	definitions := make([]javascriptLexDefinition, 0)
	for nameIndex := range tokens {
		line, column := positions.lineColumn(tokens[nameIndex].startOffset())
		if recoveryLines != nil && !recoveryLines[line] {
			continue
		}
		if nameIndex >= len(contexts) ||
			(contexts[nameIndex] != javascriptLexicalBraceObject &&
				contexts[nameIndex] != javascriptLexicalBraceClass) ||
			!javascriptLexPropertyName(tokens[nameIndex].text) ||
			!javascriptLexMethodAt(tokens, nameIndex, delimiters) {
			continue
		}
		if javascriptControlHeaderKeyword(javascriptDecodedIdentifier(tokens[nameIndex].text)) &&
			!javascriptLexPlausibleMethodParameters(tokens, nameIndex, delimiters) {
			continue
		}
		prefixStart := javascriptLexMethodPrefixStart(tokens, nameIndex)
		decoratorStart := javascriptLexDecoratorPrefixStart(tokens, prefixStart, delimiters)
		if decoratorStart < prefixStart {
			prefixStart = decoratorStart
		}
		if prefixStart > 0 && !javascriptMethodBoundaryToken(tokens[prefixStart-1].text) &&
			(!tokens[prefixStart].startsLine() ||
				!javascriptTokenCanEnd(tokens[prefixStart-1])) {
			continue
		}
		parameterOpen := nameIndex + 1
		parameterClose := delimiters.at(parameterOpen)
		bodyIndex := parameterClose + 1
		scopeOffset := javascriptLexAttachedStart(
			source, tokens[prefixStart].startOffset(), commentSpans,
		)
		scopeStart, _ := positions.lineColumn(scopeOffset)
		definition := sourceDefinition{
			symbol:     tokens[nameIndex].text,
			line:       line,
			column:     column,
			scopeStart: scopeStart,
			scopeEnd:   line,
		}
		if bodyEnd, paired := delimiters.get(bodyIndex); paired && bodyEnd > bodyIndex {
			definition.scopeEnd, _ = positions.lineColumn(max(tokens[bodyEnd].endOffset()-1, 0))
			definition.ownsScope = true
		}
		definitions = append(definitions, javascriptLexDefinition{definition: definition})
	}
	return definitions
}

const (
	javascriptLexicalBraceBlock uint8 = iota
	javascriptLexicalBraceObject
	javascriptLexicalBraceClass
)

func javascriptLexCallablePropertyDefinitions(
	source string,
	tokens []javascriptToken,
	delimiters javascriptDelimiterPairs,
	positions javascriptSourcePositions,
	recoveryLines map[int]bool,
	contexts []uint8,
	containerEnds []int32,
	commentSpans []javascriptByteSpan,
	typeScript bool,
) []javascriptLexDefinition {
	definitions := make([]javascriptLexDefinition, 0)
	for index, token := range tokens {
		line, column := positions.lineColumn(token.startOffset())
		if recoveryLines != nil && !recoveryLines[line] {
			continue
		}

		if token.text == "=" {
			symbol, nameStart, commonJS := javascriptLexCommonJSExportName(
				source, tokens, index, delimiters,
			)
			if !commonJS {
				continue
			}
			if !javascriptLexPotentialDirectCallable(tokens, index+1) {
				continue
			}
			valueEnd := javascriptLexInitializerSeparator(
				tokens, index+1, delimiters, typeScript,
			)
			if scopeEnd, callable := javascriptLexDirectCallable(
				tokens, index+1, valueEnd, delimiters, positions,
			); callable {
				nameLine, nameColumn := positions.lineColumn(nameStart)
				definitions = append(definitions, javascriptLexDefinition{
					definition: sourceDefinition{
						symbol:     symbol,
						line:       nameLine,
						column:     nameColumn,
						scopeStart: nameLine,
						scopeEnd:   max(nameLine, scopeEnd),
						ownsScope:  true,
					},
					strong: true,
				})
			}
			continue
		}

		if !javascriptLexPropertyName(token.text) {
			continue
		}
		context := javascriptLexicalBraceBlock
		if index < len(contexts) {
			context = contexts[index]
		}
		switch context {
		case javascriptLexicalBraceObject:
			if index+1 >= len(tokens) || tokens[index+1].text != ":" {
				continue
			}
			if index == 0 ||
				(tokens[index-1].text != "{" && tokens[index-1].text != ",") {
				continue
			}
			containerEnd := len(tokens)
			if index < len(containerEnds) && containerEnds[index] > 0 {
				containerEnd = int(containerEnds[index])
			}
			valueEnd := javascriptLexTopLevelSeparator(
				tokens, index+2, containerEnd, ",", delimiters,
			)
			if valueEnd < 0 {
				valueEnd = containerEnd
			}
			scopeEnd, callable := javascriptLexDirectCallable(
				tokens, index+2, valueEnd, delimiters, positions,
			)
			if !callable {
				continue
			}
			scopeOffset := javascriptLexAttachedStart(
				source, token.startOffset(), commentSpans,
			)
			scopeStart, _ := positions.lineColumn(scopeOffset)
			definitions = append(definitions, javascriptLexDefinition{
				definition: sourceDefinition{
					symbol:     token.text,
					line:       line,
					column:     column,
					scopeStart: scopeStart,
					scopeEnd:   max(line, scopeEnd),
					ownsScope:  true,
				},
			})
		case javascriptLexicalBraceClass:
			decoratorStart := javascriptLexDecoratorPrefixStart(tokens, index, delimiters)
			decorated := decoratorStart < index && tokens[decoratorStart].text == "@"
			nextIsBoundary := index+1 >= len(tokens) || tokens[index+1].text == "=" ||
				tokens[index+1].text == ";" || tokens[index+1].text == "}" ||
				tokens[index+1].startsLine()
			insideDecoratorName := index > 0 && (tokens[index-1].text == "@" ||
				tokens[index-1].text == "." || tokens[index-1].text == "?.")
			if !javascriptLexClassFieldName(tokens, index) &&
				(!decorated || !nextIsBoundary || insideDecoratorName) {
				continue
			}
			definition := sourceDefinition{
				symbol: token.text, line: line, column: column,
				scopeStart: line, scopeEnd: line,
			}
			if index+1 < len(tokens) && tokens[index+1].text == "=" {
				containerEnd := len(tokens)
				if index < len(containerEnds) && containerEnds[index] > 0 {
					containerEnd = int(containerEnds[index])
				}
				valueEnd := min(
					containerEnd,
					javascriptLexInitializerSeparator(
						tokens, index+2, delimiters, typeScript,
					),
				)
				if owned, scopeEnd := javascriptLexInitializerScope(
					tokens, index+2, valueEnd-1, delimiters, positions,
				); owned {
					scopeOffset := tokens[index].startOffset()
					if decorated {
						scopeOffset = tokens[decoratorStart].startOffset()
					}
					scopeOffset = javascriptLexAttachedStart(
						source, scopeOffset, commentSpans,
					)
					definition.scopeStart, _ = positions.lineColumn(scopeOffset)
					definition.scopeEnd = max(line, scopeEnd)
					definition.ownsScope = true
				}
			}
			definitions = append(definitions, javascriptLexDefinition{definition: definition})
		}
	}
	return definitions
}

func javascriptLexPotentialDirectCallable(tokens []javascriptToken, start int) bool {
	if start < 0 || start >= len(tokens) {
		return false
	}
	cursor := start
	if tokens[cursor].text == "(" {
		return true
	}
	if tokens[cursor].text == "async" {
		cursor++
	}
	if cursor >= len(tokens) {
		return false
	}
	switch tokens[cursor].text {
	case "function", "class":
		return true
	default:
		return javascriptLexBindingName(tokens[cursor].text) &&
			cursor+1 < len(tokens) && tokens[cursor+1].text == "=>"
	}
}

func javascriptLexicalBraceContexts(
	source string,
	tokens []javascriptToken,
	delimiters javascriptDelimiterPairs,
	objectBraces []javascriptByteSpan,
) ([]uint8, []int32) {
	objectStarts := make(map[int]bool)
	if objectBraces == nil {
		objectBraces = scanJavaScriptFallback(source).objectBraces
	}
	for _, span := range objectBraces {
		objectStarts[span.start] = true
	}
	classStarts := javascriptLexClassBraceStarts(tokens, delimiters)
	contexts := make([]uint8, len(tokens))
	containerEnds := make([]int32, len(tokens))
	type delimiterContext struct {
		kind uint8
		end  int
	}
	stack := make([]delimiterContext, 0, 16)
	for index, token := range tokens {
		if len(stack) > 0 {
			contexts[index] = stack[len(stack)-1].kind
			containerEnds[index] = int32(stack[len(stack)-1].end)
		}
		_, opener, delimiter := javascriptDelimiterKind(token.text)
		if delimiter && opener {
			kind := javascriptLexicalBraceBlock
			if token.text == "{" {
				parentKind := javascriptLexicalBraceBlock
				if len(stack) > 0 {
					parentKind = stack[len(stack)-1].kind
				}
				switch {
				case classStarts[index]:
					kind = javascriptLexicalBraceClass
				case objectStarts[token.startOffset()] &&
					!javascriptLexBraceStartsStatementBlock(
						tokens, index, delimiters, parentKind,
					):
					kind = javascriptLexicalBraceObject
				}
			}
			end := len(tokens)
			if delimiters.at(index) > index {
				end = delimiters.at(index)
			}
			stack = append(stack, delimiterContext{kind: kind, end: end})
		} else if delimiter && !opener && len(stack) > 0 {
			stack = stack[:len(stack)-1]
		}
	}
	return contexts, containerEnds
}

func javascriptLexClassBraceStarts(
	tokens []javascriptToken,
	delimiters javascriptDelimiterPairs,
) map[int]bool {
	classStarts := make(map[int]bool)
	type activeDelimiter struct {
		end       int
		classBody bool
	}
	active := make([]activeDelimiter, 0, 16)
	for index, token := range tokens {
		for len(active) > 0 && active[len(active)-1].end < index {
			active = active[:len(active)-1]
		}
		directClassMember := len(active) > 0 && active[len(active)-1].classBody
		if token.text == "class" && javascriptLexClassExpressionCandidate(tokens, index) &&
			(!directClassMember || !javascriptLexClassMemberKeywordAt(tokens, index)) {
			bodyStart := javascriptLexClassBodyStart(tokens, index, delimiters, 0)
			if bodyStart >= 0 && delimiters.at(bodyStart) > bodyStart {
				classStarts[bodyStart] = true
			}
		}
		_, opener, delimiter := javascriptDelimiterKind(token.text)
		if delimiter && opener {
			if end, paired := delimiters.get(index); paired && end > index {
				active = append(active, activeDelimiter{
					end: end, classBody: token.text == "{" && classStarts[index],
				})
			}
		}
	}
	return classStarts
}

func javascriptLexClassMemberKeywordAt(tokens []javascriptToken, index int) bool {
	return index >= 0 && index < len(tokens) &&
		(tokens[index].text == "class" || tokens[index].text == "function") &&
		!javascriptLexKeywordStartsValue(tokens, index)
}

func javascriptLexClassExpressionCandidate(
	tokens []javascriptToken,
	index int,
) bool {
	if index < 0 || index+1 >= len(tokens) ||
		index > 0 && (tokens[index-1].text == "." || tokens[index-1].text == "?.") {
		return false
	}
	switch tokens[index+1].text {
	case "{", "extends":
		return true
	case "(", ":", ",", ";", "=", "}":
		return false
	default:
		return javascriptLexBindingName(tokens[index+1].text)
	}
}

func javascriptLexBraceStartsStatementBlock(
	tokens []javascriptToken,
	braceIndex int,
	delimiters javascriptDelimiterPairs,
	parentKind uint8,
) bool {
	if braceIndex < 2 || tokens[braceIndex-1].text != ":" {
		return false
	}
	nameIndex := braceIndex - 2
	if parentKind == javascriptLexicalBraceBlock &&
		javascriptSourceName(tokens[nameIndex].text) &&
		javascriptLexLabelStartsStatement(tokens, nameIndex, delimiters) {
		return true
	}
	for cursor := braceIndex - 2; cursor >= 0; cursor-- {
		if open, paired := delimiters.get(cursor); paired && open >= 0 && open < cursor {
			cursor = open
			continue
		}
		switch tokens[cursor].text {
		case "case", "default":
			return true
		case ";", "{", "}":
			return false
		}
	}
	return false
}

func javascriptLexLabelStartsStatement(
	tokens []javascriptToken,
	nameIndex int,
	delimiters javascriptDelimiterPairs,
) bool {
	for nameIndex >= 0 && javascriptSourceName(tokens[nameIndex].text) {
		if nameIndex == 0 || tokens[nameIndex].startsLine() {
			return true
		}
		previous := nameIndex - 1
		switch tokens[previous].text {
		case ";", "{", "}", "else", "do":
			return true
		case ")":
			if open, paired := delimiters.get(previous); paired &&
				javascriptLexControlOpen(tokens, open) {
				return true
			}
		case ":":
			nameIndex -= 2
			continue
		}
		return false
	}
	return false
}

func javascriptLexClassFieldName(tokens []javascriptToken, index int) bool {
	if index < 0 || index >= len(tokens) {
		return false
	}
	nextIsBoundary := index+1 >= len(tokens) || tokens[index+1].text == "=" ||
		tokens[index+1].text == ";" || tokens[index+1].text == "}" ||
		tokens[index+1].startsLine()
	if !nextIsBoundary {
		return false
	}
	memberStart := index
	prefix := index - 1
	for modifiers := 0; modifiers < 2 && prefix >= 0 &&
		(tokens[prefix].text == "static" || tokens[prefix].text == "accessor"); modifiers++ {
		memberStart = prefix
		prefix--
	}
	return prefix < 0 || javascriptMethodBoundaryToken(tokens[prefix].text) ||
		tokens[memberStart].startsLine() && javascriptTokenCanEnd(tokens[prefix])
}

func javascriptLexDirectCallable(
	tokens []javascriptToken,
	start, end int,
	delimiters javascriptDelimiterPairs,
	positions javascriptSourcePositions,
) (int, bool) {
	start = max(start, 0)
	end = min(end, len(tokens))
	for start < end && tokens[start].text == "(" && delimiters.at(start) == end-1 {
		start++
		end--
	}
	if start >= end {
		return 0, false
	}
	cursor := start
	if tokens[cursor].text == "async" {
		cursor++
	}
	if cursor >= end {
		return 0, false
	}
	direct := false
	callableEnd := -1
	switch tokens[cursor].text {
	case "function", "class":
		if tokens[cursor].text == "class" &&
			!javascriptLexClassExpressionCandidate(tokens, cursor) {
			return 0, false
		}
		direct = true
		if tokens[cursor].text == "class" {
			if bodyStart := javascriptLexClassBodyStart(tokens, cursor, delimiters, 0); bodyStart >= 0 {
				if bodyEnd, paired := delimiters.get(bodyStart); paired {
					callableEnd = bodyEnd + 1
				}
			}
		} else {
			parameterOpen := javascriptNextToken(tokens, cursor+1)
			if parameterClose, paired := delimiters.get(parameterOpen); paired {
				bodyStart := parameterClose + 1
				if bodyEnd, bodyPaired := delimiters.get(bodyStart); bodyPaired {
					callableEnd = bodyEnd + 1
				}
			}
		}
	default:
		arrowIndex := -1
		if javascriptLexBindingName(tokens[cursor].text) && cursor+1 < end &&
			tokens[cursor+1].text == "=>" {
			direct = true
			arrowIndex = cursor + 1
		} else if tokens[cursor].text == "(" && delimiters.at(cursor) > cursor &&
			delimiters.at(cursor)+1 < end && tokens[delimiters.at(cursor)+1].text == "=>" {
			direct = true
			arrowIndex = delimiters.at(cursor) + 1
		}
		if arrowIndex >= 0 {
			bodyStart := arrowIndex + 1
			if bodyStart < end && tokens[bodyStart].text == "{" {
				if bodyEnd, paired := delimiters.get(bodyStart); paired {
					callableEnd = bodyEnd + 1
				}
			} else {
				callableEnd = end
			}
		}
	}
	if !direct || callableEnd != end {
		return 0, false
	}
	owned, scopeEnd := javascriptLexInitializerScope(
		tokens, start, end-1, delimiters, positions,
	)
	return scopeEnd, owned
}

func javascriptLexCommonJSExportName(
	source string,
	tokens []javascriptToken,
	equalIndex int,
	delimiters javascriptDelimiterPairs,
) (string, int, bool) {
	if equalIndex <= 0 || equalIndex >= len(tokens) || tokens[equalIndex].text != "=" {
		return "", 0, false
	}
	parts := make([]struct {
		text  string
		start int
		index int
	}, 0, 4)
	cursor := equalIndex - 1
	wrapperOpens := make([]int, 0, 2)
	for cursor >= 0 {
		for cursor >= 0 && tokens[cursor].text == ")" {
			open, paired := delimiters.get(cursor)
			if !paired || open+1 >= cursor {
				return "", 0, false
			}
			wrapperOpens = append(wrapperOpens, open)
			cursor--
		}
		if cursor < 0 {
			return "", 0, false
		}
		part := struct {
			text  string
			start int
			index int
		}{}
		switch {
		case javascriptSourceName(tokens[cursor].text):
			part.text, part.start, part.index = tokens[cursor].text, tokens[cursor].startOffset(), cursor
			cursor--
		case tokens[cursor].text == "]":
			open, paired := delimiters.get(cursor)
			if !paired || open < 0 || open+2 != cursor ||
				!tokens[open+1].literal() {
				return "", 0, false
			}
			literal := tokens[open+1]
			literalStart, literalEnd := literal.startOffset(), literal.endOffset()
			if !literal.literal() || literalStart < 0 || literalEnd > len(source) ||
				literalEnd-literalStart < 2 ||
				(source[literalStart] != '\'' && source[literalStart] != '"') ||
				source[literalEnd-1] != source[literalStart] {
				return "", 0, false
			}
			part.text = source[literalStart+1 : literalEnd-1]
			part.start = literalStart + 1
			part.index = open
			if !javascriptSourceName(part.text) {
				return "", 0, false
			}
			cursor = open - 1
		default:
			return "", 0, false
		}
		parts = append(parts, part)
		if len(wrapperOpens) > 0 && cursor == wrapperOpens[len(wrapperOpens)-1] {
			cursor--
			wrapperOpens = wrapperOpens[:len(wrapperOpens)-1]
		}
		if cursor < 0 || tokens[cursor].text != "." {
			break
		}
		cursor--
	}
	if len(parts) == 0 {
		return "", 0, false
	}
	rootIndex := parts[len(parts)-1].index
	if cursor >= 0 && javascriptTokenCanEnd(tokens[cursor]) &&
		!tokens[rootIndex].startsLine() && tokens[cursor].text != "}" {
		return "", 0, false
	}
	for first, last := 0, len(parts)-1; first < last; first, last = first+1, last-1 {
		parts[first], parts[last] = parts[last], parts[first]
	}
	canonical := len(parts) >= 2 && parts[0].text == "exports" ||
		len(parts) >= 3 && parts[0].text == "module" && parts[1].text == "exports"
	if !canonical {
		return "", 0, false
	}
	name := parts[len(parts)-1]
	return name.text, name.start, true
}

func javascriptLexMethodAt(
	tokens []javascriptToken,
	nameIndex int,
	delimiters javascriptDelimiterPairs,
) bool {
	if nameIndex < 0 || nameIndex+1 >= len(tokens) || tokens[nameIndex+1].text != "(" {
		return false
	}
	parameterClose, paired := delimiters.get(nameIndex + 1)
	if !paired || parameterClose <= nameIndex+1 || parameterClose+1 >= len(tokens) {
		return false
	}
	return tokens[parameterClose+1].text == "{"
}

func javascriptLexPlausibleMethodParameters(
	tokens []javascriptToken,
	nameIndex int,
	delimiters javascriptDelimiterPairs,
) bool {
	if nameIndex < 0 || nameIndex+1 >= len(tokens) || tokens[nameIndex+1].text != "(" {
		return false
	}
	parameterEnd, paired := delimiters.get(nameIndex + 1)
	if !paired || parameterEnd <= nameIndex+1 {
		return false
	}
	for cursor := nameIndex + 2; cursor < parameterEnd; {
		for cursor < parameterEnd && tokens[cursor].text == "@" {
			cursor++
			if cursor >= parameterEnd || !javascriptLexBindingName(tokens[cursor].text) {
				return false
			}
			cursor++
			for cursor+1 < parameterEnd &&
				(tokens[cursor].text == "." || tokens[cursor].text == "?.") &&
				javascriptLexBindingName(tokens[cursor+1].text) {
				cursor += 2
			}
			if cursor < parameterEnd && tokens[cursor].text == "<" {
				end := typeScriptLexGenericArgumentEnd(tokens, cursor, delimiters)
				if end <= cursor || end >= parameterEnd {
					return false
				}
				cursor = end + 1
			}
			if cursor < parameterEnd && tokens[cursor].text == "(" {
				end, ok := delimiters.get(cursor)
				if !ok || end <= cursor || end >= parameterEnd {
					return false
				}
				cursor = end + 1
			}
		}
		for cursor < parameterEnd {
			switch tokens[cursor].text {
			case "public", "private", "protected", "readonly", "override":
				cursor++
			default:
				goto binding
			}
		}
	binding:
		if cursor < parameterEnd && tokens[cursor].text == "..." {
			cursor++
		}
		if cursor >= parameterEnd {
			return false
		}
		switch {
		case tokens[cursor].text == "{" || tokens[cursor].text == "[":
			end, ok := delimiters.get(cursor)
			if !ok || end <= cursor || end >= parameterEnd {
				return false
			}
			cursor = end + 1
		case javascriptLexBindingName(tokens[cursor].text):
			cursor++
		default:
			return false
		}
		for cursor < parameterEnd &&
			(tokens[cursor].text == "?" || tokens[cursor].text == "!") {
			cursor++
		}
		if cursor < parameterEnd && tokens[cursor].text == ":" {
			cursor++
			typeStart := cursor
			for cursor < parameterEnd && tokens[cursor].text != "," &&
				tokens[cursor].text != "=" {
				if (tokens[cursor].text == "(" || tokens[cursor].text == "[" ||
					tokens[cursor].text == "{") && delimiters.at(cursor) > cursor {
					cursor = delimiters.at(cursor) + 1
					continue
				}
				if tokens[cursor].text == "<" {
					if end := typeScriptLexGenericArgumentEnd(
						tokens, cursor, delimiters,
					); end > cursor {
						cursor = end + 1
						continue
					}
				}
				cursor++
			}
			if cursor == typeStart {
				return false
			}
		}
		if cursor < parameterEnd && tokens[cursor].text == "=" {
			cursor++
			for cursor < parameterEnd && tokens[cursor].text != "," {
				if (tokens[cursor].text == "(" || tokens[cursor].text == "[" ||
					tokens[cursor].text == "{") && delimiters.at(cursor) > cursor {
					cursor = delimiters.at(cursor) + 1
					continue
				}
				cursor++
			}
		}
		if cursor >= parameterEnd {
			return true
		}
		if tokens[cursor].text != "," {
			return false
		}
		cursor++
		if cursor == parameterEnd {
			return true
		}
	}
	return true
}

func javascriptLexMethodPrefixStart(tokens []javascriptToken, nameIndex int) int {
	start := nameIndex
	for start > 0 {
		switch tokens[start-1].text {
		case "async", "get", "set", "static", "*":
			if tokens[start].startsLine() && tokens[start-1].text != "*" {
				return start
			}
			start--
		default:
			return start
		}
	}
	return start
}

func javascriptMethodBoundaryToken(token string) bool {
	switch token {
	case "{", "}", ",", ";":
		return true
	default:
		return false
	}
}

func javascriptNextToken(tokens []javascriptToken, start int) int {
	limit := min(len(tokens), start+javascriptMaximumRecoveryLookahead)
	for index := start; index < limit; index++ {
		if tokens[index].text == "(" {
			return index
		}
		if tokens[index].text == ";" ||
			(index > start && tokens[index].startsLine() && javascriptHardDeclarationToken(tokens[index].text)) {
			return -1
		}
	}
	return -1
}

func javascriptLexBindingName(name string) bool {
	return javascriptSourceIdentifier(name) &&
		!javascriptReservedBinding[javascriptDecodedIdentifier(name)]
}

func javascriptLexPropertyName(name string) bool {
	return javascriptSourceName(name)
}

func javascriptHardDeclarationToken(token string) bool {
	switch token {
	case "function", "class", "const", "let", "var", "using", "import", "export":
		return true
	default:
		return false
	}
}

func javascriptExpressionCanEnd(token string) bool {
	if javascriptSourceName(token) || javascriptNumberStart(token, 0) {
		return true
	}
	switch token {
	case ")", "]", "}", "++", "--":
		return true
	default:
		return false
	}
}

func javascriptTokenCanEnd(token javascriptToken) bool {
	return token.literal() || javascriptExpressionCanEnd(token.text)
}

func javascriptLexDeclarationStart(
	tokens []javascriptToken,
	index int,
	delimiters javascriptDelimiterPairs,
) bool {
	if index < 0 || index >= len(tokens) {
		return false
	}
	if index > 0 {
		previous := tokens[index-1].text
		if previous == "." || previous == "?." || previous == "<" ||
			previous == "/" && index > 1 && tokens[index-2].text == "<" {
			return false
		}
		if javascriptLexicalBindingDeclarationToken(tokens[index].text) &&
			javascriptLexControlDeclarationPosition(tokens, index, delimiters) {
			return true
		}
		switch previous {
		case ";", "}", "else", "do":
			return true
		case "{":
			return !javascriptLexModuleSpecifierBrace(tokens, index-1)
		}
		if javascriptLexicalBindingDeclarationToken(tokens[index].text) &&
			!tokens[index].startsLine() && previous != "export" && previous != "await" &&
			previous != "}" &&
			javascriptTokenCanEnd(tokens[index-1]) {
			return false
		}
		if previous == ")" {
			if open, paired := delimiters.get(index - 1); paired && open > 0 &&
				javascriptLexControlOpen(tokens, open) {
				return true
			}
		}
	}
	if tokens[index].startsLine() && javascriptHardDeclarationToken(tokens[index].text) {
		return true
	}
	openSpecifier := false
	minimum := max(0, index-javascriptMaximumRecoveryLookahead)
	for cursor := index - 1; cursor >= minimum; cursor-- {
		switch tokens[cursor].text {
		case ";":
			return true
		case "}":
			// A completed object/block cannot open a module specifier around
			// the declaration keyword being considered.
			if !openSpecifier {
				return true
			}
		case "{":
			openSpecifier = true
		case "import", "export":
			return !openSpecifier
		}
		if tokens[index].startsLine() && tokens[cursor].startsLine() &&
			javascriptHardDeclarationToken(tokens[cursor].text) {
			return true
		}
	}
	return minimum == 0
}

func javascriptLexHardStatementStart(
	tokens []javascriptToken,
	index int,
	delimiters javascriptDelimiterPairs,
) bool {
	if index < 0 || index >= len(tokens) {
		return false
	}
	if (tokens[index].text == "function" || tokens[index].text == "class") &&
		javascriptLexKeywordStartsValue(tokens, index) {
		return false
	}
	return javascriptLexDeclarationStart(tokens, index, delimiters)
}

func javascriptLexControlDeclarationPosition(
	tokens []javascriptToken,
	index int,
	delimiters javascriptDelimiterPairs,
) bool {
	if index <= 0 {
		return false
	}
	previousIndex := index - 1
	switch tokens[previousIndex].text {
	case ")":
		open, paired := delimiters.get(previousIndex)
		return paired && javascriptLexControlOpen(tokens, open)
	case "(":
		return javascriptLexControlOpen(tokens, previousIndex)
	default:
		return false
	}
}

func javascriptLexControlOpen(tokens []javascriptToken, open int) bool {
	if open <= 0 || open >= len(tokens) {
		return false
	}
	keyword := open - 1
	if tokens[keyword].text == "await" && keyword > 0 {
		keyword--
	}
	return javascriptLexControlKeywordAt(tokens, keyword)
}

func javascriptLexControlKeywordAt(tokens []javascriptToken, index int) bool {
	if index < 0 || index >= len(tokens) ||
		index > 0 && (tokens[index-1].text == "." || tokens[index-1].text == "?.") {
		return false
	}
	return javascriptControlHeaderKeyword(
		javascriptDecodedIdentifier(tokens[index].text),
	)
}

func javascriptLexModuleSpecifierBrace(tokens []javascriptToken, braceIndex int) bool {
	minimum := max(0, braceIndex-javascriptMaximumRecoveryLookahead)
	for cursor := braceIndex - 1; cursor >= minimum; cursor-- {
		switch tokens[cursor].text {
		case "import", "export":
			return true
		case ";", "{", "}":
			return false
		}
	}
	return false
}

func javascriptLexicalBindingDeclarationToken(token string) bool {
	switch token {
	case "const", "let", "var", "using":
		return true
	default:
		return false
	}
}

var javascriptReservedBinding = func() map[string]bool {
	reserved := make(map[string]bool, len(javascriptHardKeywords)+8)
	for keyword := range javascriptHardKeywords {
		if keyword == "await" || keyword == "yield" || keyword == "let" {
			continue
		}
		reserved[keyword] = true
	}
	reserved["enum"] = true
	return reserved
}()

func javascriptLexImports(
	source string,
	tokens []javascriptToken,
	delimiters javascriptDelimiterPairs,
	boundaries javascriptLexBoundaries,
	commentSpans []javascriptByteSpan,
	jsxValues []javascriptByteSpan,
	objectBraces []javascriptByteSpan,
) []javascriptLineSpan {
	positions := javascriptSourcePositions{source: source, lineStarts: javascriptLineStarts(source)}
	imports := make([]javascriptLineSpan, 0)
	statementStarts := javascriptLexicalStatementStarts(
		source, tokens, delimiters, objectBraces, boundaries.forcedStarts,
	)
	functionBodies := javascriptLexicalFunctionBodies(
		source, tokens, delimiters, boundaries, objectBraces,
	)
	jsxOwners := javascriptLexicalJSXOwners(tokens, boundaries, jsxValues, statementStarts)
	executionOwners := javascriptLexicalExecutionOwners(
		tokens, delimiters, boundaries, statementStarts,
	)
	activeOwners := make([]javascriptLexExecutionOwner, 0, 8)
	nextOwner := 0
	activeControls := make([]javascriptTokenRange, 0, 8)
	activeControlHead := 0
	depth := 0
	for index, token := range tokens {
		for len(activeOwners) > 0 && activeOwners[len(activeOwners)-1].end < index {
			activeOwners = activeOwners[:len(activeOwners)-1]
		}
		for nextOwner < len(executionOwners) && executionOwners[nextOwner].start == index {
			activeOwners = append(activeOwners, executionOwners[nextOwner])
			nextOwner++
		}
		for activeControlHead < len(activeControls) &&
			activeControls[activeControlHead].end < index {
			activeControlHead++
		}
		if controlEnd := boundaries.controlEnd(index); controlEnd > index {
			activeControls = append(activeControls, javascriptTokenRange{
				start: index,
				end:   controlEnd,
			})
		}
		_, opener, delimiter := javascriptDelimiterKind(token.text)
		if delimiter && !opener && delimiters.at(index) < index && depth > 0 {
			depth--
		}
		topLevel := depth == 0
		include := false
		startIndex := index
		endIndex := -1
		switch token.text {
		case "import":
			include = topLevel &&
				(index == 0 || tokens[index-1].text != "." && tokens[index-1].text != "?.") &&
				(index+1 >= len(tokens) ||
					(tokens[index+1].text != "(" && tokens[index+1].text != "."))
		case "export":
			if !topLevel || index > 0 &&
				(tokens[index-1].text == "." || tokens[index-1].text == "?.") {
				break
			}
			include = javascriptLexExportHasSource(tokens, index, delimiters)
		case "require":
			if javascriptTokenInsideRanges(index, functionBodies) ||
				!javascriptLexicalRequireCall(
					source, tokens, index, delimiters, boundaries,
				) {
				continue
			}
			include = true
			startIndex = statementStarts[index]
			if ownerStart, ownerEnd, owned := javascriptLexJSXOwnerStatement(
				tokens, index, jsxOwners,
			); owned {
				startIndex, endIndex = ownerStart, ownerEnd
			}
			if len(activeOwners) > 0 {
				startIndex = activeOwners[0].ownerStart
				endIndex = activeOwners[0].ownerEnd
			}
			if activeControlHead < len(activeControls) {
				control := activeControls[activeControlHead]
				if endIndex < startIndex || control.start < startIndex ||
					control.start == startIndex && control.end > endIndex {
					startIndex = control.start
					endIndex = control.end
				}
			}
		}
		if !include {
			if delimiter && opener && delimiters.at(index) > index {
				depth++
			}
			continue
		}
		if endIndex < startIndex {
			endIndex = boundaries.statementEnd(startIndex)
		}
		startOffset := javascriptLexAttachedStart(
			source, tokens[startIndex].startOffset(), commentSpans,
		)
		startLine, _ := positions.lineColumn(startOffset)
		endLine := startLine
		if endIndex >= index && endIndex < len(tokens) {
			endLine, _ = positions.lineColumn(max(tokens[endIndex].endOffset()-1, 0))
		}
		imports = append(imports, javascriptLineSpan{start: startLine, end: endLine})
		if delimiter && opener && delimiters.at(index) > index {
			depth++
		}
	}
	return normalizeJavaScriptLineSpans(imports)
}

func javascriptLexExportHasSource(
	tokens []javascriptToken,
	exportIndex int,
	delimiters javascriptDelimiterPairs,
) bool {
	if exportIndex < 0 || exportIndex+1 >= len(tokens) ||
		tokens[exportIndex].text != "export" {
		return false
	}
	cursor := exportIndex + 1
	switch tokens[cursor].text {
	case "{":
		closingIndex, paired := delimiters.get(cursor)
		if !paired || closingIndex <= cursor {
			return false
		}
		cursor = closingIndex + 1
	case "*":
		cursor++
		if cursor < len(tokens) && tokens[cursor].text == "as" {
			cursor += 2
		}
	default:
		return false
	}
	return cursor < len(tokens) && tokens[cursor].text == "from"
}

type javascriptLexExecutionOwner struct {
	start      int
	end        int
	ownerStart int
	ownerEnd   int
}

func javascriptLexicalExecutionOwners(
	tokens []javascriptToken,
	delimiters javascriptDelimiterPairs,
	boundaries javascriptLexBoundaries,
	statementStarts []int,
) []javascriptLexExecutionOwner {
	owners := make([]javascriptLexExecutionOwner, 0)
	appendOwner := func(start, end, ownerStart int) {
		if start < 0 || end <= start || end >= len(tokens) ||
			ownerStart < 0 || ownerStart >= len(tokens) {
			return
		}
		ownerEnd := boundaries.statementEnd(end)
		if ownerEnd < end {
			ownerEnd = end
		}
		owners = append(owners, javascriptLexExecutionOwner{
			start: start, end: end, ownerStart: ownerStart, ownerEnd: ownerEnd,
		})
	}
	for index, token := range tokens {
		if token.text == "{" {
			if end, paired := delimiters.get(index); paired && end > index {
				appendOwner(index, end, statementStarts[index])
			}
		}
		if token.text != "class" || !javascriptLexClassExpressionCandidate(tokens, index) {
			continue
		}
		bodyStart := javascriptLexClassBodyStart(tokens, index, delimiters, 0)
		bodyEnd, paired := delimiters.get(bodyStart)
		if !paired || bodyEnd <= bodyStart {
			continue
		}
		prefixStart := javascriptLexDecoratorPrefixStart(tokens, index, delimiters)
		ownerStart := statementStarts[index]
		if prefixStart < index {
			ownerStart = statementStarts[prefixStart]
		} else if index > 0 && javascriptExpressionContinuationToken(tokens[index-1].text) {
			ownerStart = statementStarts[index-1]
		}
		appendOwner(prefixStart, bodyEnd, ownerStart)
	}
	sort.SliceStable(owners, func(first, second int) bool {
		if owners[first].start != owners[second].start {
			return owners[first].start < owners[second].start
		}
		return owners[first].end > owners[second].end
	})
	return owners
}

func javascriptLexDecoratorPrefixStart(
	tokens []javascriptToken,
	declarationStart int,
	delimiters javascriptDelimiterPairs,
) int {
	if declarationStart <= 0 || declarationStart >= len(tokens) {
		return max(declarationStart, 0)
	}
	prefixStart := declarationStart
	minimum := max(0, declarationStart-javascriptMaximumRecoveryLookahead)
	for cursor := declarationStart - 1; cursor >= minimum; cursor-- {
		if tokens[cursor].text == ")" || tokens[cursor].text == "]" {
			if open, paired := delimiters.get(cursor); paired && open >= minimum {
				cursor = open
				continue
			}
		}
		switch tokens[cursor].text {
		case "@":
			prefixStart = cursor
		case ";", "{", "}", "=":
			return prefixStart
		}
	}
	return prefixStart
}

type javascriptLexJSXOwner struct {
	spanStart  int
	spanEnd    int
	ownerStart int
	ownerEnd   int
}

func javascriptLexicalJSXOwners(
	tokens []javascriptToken,
	boundaries javascriptLexBoundaries,
	jsxValues []javascriptByteSpan,
	statementStarts []int,
) []javascriptLexJSXOwner {
	owners := make([]javascriptLexJSXOwner, 0, len(jsxValues))
	for _, span := range jsxValues {
		rootIndex := sort.Search(len(tokens), func(candidate int) bool {
			return tokens[candidate].startOffset() >= span.start
		})
		if rootIndex >= len(tokens) {
			continue
		}
		ownerEnd := sort.Search(len(tokens), func(candidate int) bool {
			return tokens[candidate].endOffset() >= span.end
		})
		if ownerEnd >= len(tokens) {
			continue
		}
		ownerStart := rootIndex
		if rootIndex < len(statementStarts) {
			ownerStart = statementStarts[rootIndex]
		}
		owners = append(owners, javascriptLexJSXOwner{
			spanStart:  span.start,
			spanEnd:    span.end,
			ownerStart: ownerStart,
			ownerEnd:   boundaries.statementEnd(ownerEnd),
		})
	}
	return owners
}

func javascriptLexJSXOwnerStatement(
	tokens []javascriptToken,
	index int,
	owners []javascriptLexJSXOwner,
) (int, int, bool) {
	if index < 0 || index >= len(tokens) || len(owners) == 0 {
		return 0, 0, false
	}
	offset := tokens[index].startOffset()
	ownerIndex := sort.Search(len(owners), func(candidate int) bool {
		return owners[candidate].spanEnd > offset
	})
	if ownerIndex >= len(owners) || owners[ownerIndex].spanStart > offset {
		return 0, 0, false
	}
	owner := owners[ownerIndex]
	return owner.ownerStart, owner.ownerEnd, true
}

func javascriptLexAttachedStart(
	source string,
	start int,
	commentSpans []javascriptByteSpan,
) int {
	candidateStart := start
	attachedStart := -1
	lastComment := sort.Search(len(commentSpans), func(index int) bool {
		return commentSpans[index].end > start
	}) - 1
	for index := lastComment; index >= 0; index-- {
		comment := commentSpans[index]
		if comment.end > candidateStart {
			continue
		}
		if !javascriptAttachmentGap(source, comment.end, candidateStart) {
			break
		}
		if comment.start < 0 || comment.end > len(source) {
			break
		}
		candidateStart = comment.start
		if strings.HasPrefix(source[comment.start:comment.end], "/**") {
			attachedStart = comment.start
		}
	}
	if attachedStart >= 0 {
		return attachedStart
	}
	return start
}

type javascriptTokenRange struct {
	start int
	end   int
}

func javascriptLexScopes(
	source string,
	tokens []javascriptToken,
	delimiters javascriptDelimiterPairs,
	boundaries javascriptLexBoundaries,
	commentSpans, objectBraces []javascriptByteSpan,
) []javascriptLineScope {
	if len(tokens) == 0 {
		return nil
	}
	positions := javascriptSourcePositions{
		source: source, lineStarts: javascriptLineStarts(source),
	}
	if objectBraces == nil {
		objectBraces = scanJavaScriptFallback(source).objectBraces
	}
	objectStarts := make(map[int]bool, len(objectBraces))
	for _, span := range objectBraces {
		objectStarts[span.start] = true
	}
	classStarts := javascriptLexClassBraceStarts(tokens, delimiters)
	scopes := make([]javascriptLineScope, 0)
	appendScope := func(startIndex, endIndex int, attached bool) {
		if startIndex < 0 || startIndex >= len(tokens) || endIndex < startIndex ||
			endIndex >= len(tokens) {
			return
		}
		startOffset := tokens[startIndex].startOffset()
		if attached {
			startOffset = javascriptLexAttachedStart(source, startOffset, commentSpans)
		}
		startLine, _ := positions.lineColumn(startOffset)
		endLine, _ := positions.lineColumn(max(tokens[endIndex].endOffset()-1, 0))
		if startLine > 0 && endLine >= startLine {
			scopes = append(scopes, javascriptLineScope{start: startLine, end: endLine})
		}
	}

	type javascriptActiveBrace struct {
		index int
		end   int
		kind  uint8
	}
	activeBraces := make([]javascriptActiveBrace, 0, 16)
	switchBodies := make(map[int]int)
	var expressionEnds []int
	braceDepth := 0
	for index, token := range tokens {
		for len(activeBraces) > 0 && activeBraces[len(activeBraces)-1].end < index {
			activeBraces = activeBraces[:len(activeBraces)-1]
		}
		if token.text == "}" && delimiters.at(index) < index && braceDepth > 0 {
			braceDepth--
		}
		if token.text == "{" {
			bodyEnd, paired := delimiters.get(index)
			if !paired || bodyEnd <= index {
				braceDepth++
				continue
			}
			parentKind := javascriptLexicalBraceBlock
			if len(activeBraces) > 0 {
				parentKind = activeBraces[len(activeBraces)-1].kind
			}
			if !objectStarts[token.startOffset()] {
				startIndex, attached, include, generic := javascriptLexBraceScopeOwner(
					source, tokens, index, delimiters, parentKind,
				)
				if include && (!generic || braceDepth == 0) {
					appendScope(startIndex, bodyEnd, attached)
				}
			}
			kind := javascriptLexicalBraceBlock
			switch {
			case classStarts[index]:
				kind = javascriptLexicalBraceClass
			case objectStarts[token.startOffset()] &&
				!javascriptLexBraceStartsStatementBlock(
					tokens, index, delimiters, parentKind,
				):
				kind = javascriptLexicalBraceObject
			}
			activeBraces = append(activeBraces, javascriptActiveBrace{
				index: index,
				end:   bodyEnd,
				kind:  kind,
			})
			braceDepth++
			continue
		}
		if token.text == "=>" && index+1 < len(tokens) && tokens[index+1].text != "{" {
			if expressionEnds == nil {
				expressionEnds = javascriptLexicalExpressionEnds(
					tokens, delimiters, boundaries.forcedStarts,
				)
			}
			end := expressionEnds[index]
			if end > index+1 {
				appendScope(javascriptLexArrowStart(tokens, index, delimiters), end-1, false)
			}
		}
		if javascriptLexPropertyName(token.text) && index+1 < len(tokens) &&
			tokens[index+1].text == ":" &&
			(len(activeBraces) == 0 ||
				activeBraces[len(activeBraces)-1].kind == javascriptLexicalBraceBlock) &&
			javascriptLexLabelStartsStatement(tokens, index, delimiters) {
			if end := javascriptLexLabeledStatementEnd(
				tokens, index, delimiters, boundaries,
			); end >= index {
				appendScope(index, end, false)
			}
		}
		if javascriptLexControlScopeToken(token.text) &&
			!javascriptLexTrailingDoWhileScope(tokens, index, delimiters, boundaries) {
			if end := boundaries.controlEnd(index); end > index {
				appendScope(index, end, false)
			}
		}
		if token.text == "switch" &&
			(len(activeBraces) == 0 ||
				activeBraces[len(activeBraces)-1].kind == javascriptLexicalBraceBlock) {
			if bodyStart, bodyEnd, ok := javascriptLexSwitchBody(tokens, index, delimiters); ok {
				switchBodies[bodyStart] = bodyEnd
			}
		}
		if (token.text == "case" || token.text == "default") && len(activeBraces) > 0 {
			bodyStart := activeBraces[len(activeBraces)-1].index
			bodyEnd, switchBody := switchBodies[bodyStart]
			if switchBody && javascriptLexSwitchClauseAt(
				tokens, index, bodyStart, bodyEnd, delimiters,
			) {
				end := javascriptLexCaseScopeEnd(
					tokens, index, bodyStart, bodyEnd, delimiters,
				)
				appendScope(index, end, false)
			}
		}
	}
	return normalizeJavaScriptScopes(scopes)
}

func javascriptLexLabeledStatementEnd(
	tokens []javascriptToken,
	labelIndex int,
	delimiters javascriptDelimiterPairs,
	boundaries javascriptLexBoundaries,
) int {
	bodyStart := labelIndex + 2
	if bodyStart >= len(tokens) || tokens[labelIndex+1].text != ":" {
		return -1
	}
	for bodyStart+1 < len(tokens) && javascriptLexPropertyName(tokens[bodyStart].text) &&
		tokens[bodyStart+1].text == ":" {
		bodyStart += 2
	}
	if bodyStart >= len(tokens) {
		return len(tokens) - 1
	}
	if tokens[bodyStart].text == ";" {
		return bodyStart
	}
	if tokens[bodyStart].text == "{" {
		if end, paired := delimiters.get(bodyStart); paired && end > bodyStart {
			return end
		}
	}
	if end := boundaries.controlEnd(bodyStart); end >= bodyStart {
		return end
	}
	return boundaries.statementEnd(bodyStart)
}

func javascriptLexBraceScopeOwner(
	source string,
	tokens []javascriptToken,
	braceIndex int,
	delimiters javascriptDelimiterPairs,
	parentKind uint8,
) (start int, attached, include, generic bool) {
	if braceIndex <= 0 {
		return braceIndex, false, true, true
	}
	previous := braceIndex - 1
	switch tokens[previous].text {
	case "=>":
		return javascriptLexArrowStart(tokens, previous, delimiters), false, true, false
	case "catch", "else", "try", "finally", "do":
		return previous, false, false, false
	case "static":
		return previous, true, true, false
	case ")":
		parameterOpen, paired := delimiters.get(previous)
		if !paired || parameterOpen <= 0 {
			return braceIndex, false, true, true
		}
		owner := parameterOpen - 1
		if tokens[owner].text == "await" && owner > 0 && tokens[owner-1].text == "for" {
			owner--
		}
		if parentKind == javascriptLexicalBraceObject ||
			parentKind == javascriptLexicalBraceClass {
			methodStart := owner
			if tokens[methodStart].text == "]" {
				if computedStart, computed := delimiters.get(methodStart); computed &&
					computedStart >= 0 {
					methodStart = computedStart
				}
			}
			if javascriptLexTokenPropertyName(source, tokens[methodStart]) ||
				tokens[methodStart].text == "[" {
				prefixStart := javascriptLexMethodPrefixStart(tokens, methodStart)
				prefixStart = javascriptLexDecoratorPrefixStart(
					tokens, prefixStart, delimiters,
				)
				return prefixStart, true, true, false
			}
		}
		word := javascriptDecodedIdentifier(tokens[owner].text)
		if javascriptLexControlKeywordAt(tokens, owner) ||
			word == "catch" && (owner == 0 ||
				tokens[owner-1].text != "." && tokens[owner-1].text != "?.") {
			return owner, false, false, false
		}
		if tokens[owner].text == "function" {
			return javascriptLexFunctionPrefixStart(tokens, owner), true, true, false
		}
		if owner > 0 && (tokens[owner-1].text == "function" ||
			tokens[owner-1].text == "*") {
			functionIndex := owner - 1
			if tokens[functionIndex].text == "*" && functionIndex > 0 &&
				tokens[functionIndex-1].text == "function" {
				functionIndex--
			}
			return javascriptLexFunctionPrefixStart(tokens, functionIndex), true, true, false
		}
		if javascriptLexTokenPropertyName(source, tokens[owner]) {
			prefixStart := javascriptLexMethodPrefixStart(tokens, owner)
			prefixStart = javascriptLexDecoratorPrefixStart(tokens, prefixStart, delimiters)
			if prefixStart > 0 &&
				(javascriptMethodBoundaryToken(tokens[prefixStart-1].text) ||
					tokens[prefixStart].startsLine() &&
						javascriptTokenCanEnd(tokens[prefixStart-1])) {
				return prefixStart, true, true, false
			}
		}
	}
	if start := javascriptLexNearestClassStart(tokens, braceIndex, delimiters); start >= 0 {
		return start, true, true, false
	}
	if braceIndex >= 2 && tokens[braceIndex-1].text == ":" &&
		javascriptSourceName(tokens[braceIndex-2].text) {
		return braceIndex - 2, false, true, false
	}
	return braceIndex, false, true, true
}

func javascriptLexTokenPropertyName(source string, token javascriptToken) bool {
	if !token.literal() {
		return javascriptLexPropertyName(token.text)
	}
	start, end := token.startOffset(), token.endOffset()
	if start < 0 || end > len(source) || end-start < 2 {
		return false
	}
	quote := source[start]
	if (quote != '\'' && quote != '"') || source[end-1] != quote {
		return false
	}
	return true
}

func javascriptLexFunctionPrefixStart(tokens []javascriptToken, functionIndex int) int {
	if functionIndex > 0 && tokens[functionIndex-1].text == "async" &&
		!tokens[functionIndex].startsLine() {
		return functionIndex - 1
	}
	return functionIndex
}

func javascriptLexArrowStart(
	tokens []javascriptToken,
	arrowIndex int,
	delimiters javascriptDelimiterPairs,
) int {
	if arrowIndex <= 0 {
		return max(arrowIndex, 0)
	}
	start := arrowIndex - 1
	if tokens[start].text == ")" {
		if open, paired := delimiters.get(start); paired && open >= 0 {
			start = open
		}
	}
	if start > 0 && tokens[start-1].text == "async" {
		start--
	}
	return start
}

func javascriptLexNearestClassStart(
	tokens []javascriptToken,
	braceIndex int,
	delimiters javascriptDelimiterPairs,
) int {
	minimum := max(0, braceIndex-javascriptMaximumRecoveryLookahead)
	for cursor := braceIndex - 1; cursor >= minimum; cursor-- {
		if open, paired := delimiters.get(cursor); paired && open >= minimum && open < cursor {
			cursor = open
			continue
		}
		switch tokens[cursor].text {
		case "class":
			if javascriptLexClassExpressionCandidate(tokens, cursor) {
				return cursor
			}
		case ";", "{", "}", ",", "=>":
			return -1
		}
	}
	return -1
}

func javascriptLexControlScopeToken(token string) bool {
	switch javascriptDecodedIdentifier(token) {
	case "if", "for", "while", "with", "switch", "catch", "else", "do",
		"try", "finally":
		return true
	default:
		return false
	}
}

func javascriptLexTrailingDoWhile(
	tokens []javascriptToken,
	whileIndex int,
	delimiters javascriptDelimiterPairs,
) bool {
	if whileIndex <= 0 || tokens[whileIndex].text != "while" {
		return false
	}
	previous := whileIndex - 1
	if tokens[previous].text != "}" {
		return false
	}
	bodyStart, paired := delimiters.get(previous)
	return paired && bodyStart > 0 && tokens[bodyStart-1].text == "do"
}

func javascriptLexTrailingDoWhileScope(
	tokens []javascriptToken,
	whileIndex int,
	delimiters javascriptDelimiterPairs,
	boundaries javascriptLexBoundaries,
) bool {
	if javascriptLexTrailingDoWhile(tokens, whileIndex, delimiters) {
		return true
	}
	if whileIndex <= 0 || whileIndex >= len(tokens) || tokens[whileIndex].text != "while" {
		return false
	}
	minimum := max(0, whileIndex-javascriptMaximumRecoveryLookahead)
	for index := whileIndex - 1; index >= minimum; index-- {
		if tokens[index].text == "do" && boundaries.controlEnd(index) >= whileIndex {
			return true
		}
	}
	return false
}

func javascriptLexControlContinuationAt(
	tokens []javascriptToken,
	index int,
	delimiters javascriptDelimiterPairs,
	contexts []uint8,
) bool {
	if index < 0 || index >= len(tokens) {
		return false
	}
	if index < len(contexts) &&
		(contexts[index] == javascriptLexicalBraceObject ||
			contexts[index] == javascriptLexicalBraceClass) &&
		javascriptLexMethodAt(tokens, index, delimiters) {
		return false
	}
	switch tokens[index].text {
	case "else", "catch", "finally":
		return true
	case "while":
		return javascriptLexTrailingDoWhile(tokens, index, delimiters)
	default:
		return false
	}
}

func javascriptLexCaseScopeEnd(
	tokens []javascriptToken,
	start, bodyStart, bodyEnd int,
	delimiters javascriptDelimiterPairs,
) int {
	for index := start + 1; index < bodyEnd && index < len(tokens); index++ {
		if (tokens[index].text == "(" || tokens[index].text == "[" ||
			tokens[index].text == "{") && delimiters.at(index) > index {
			index = delimiters.at(index)
			continue
		}
		if javascriptLexSwitchClauseAt(
			tokens, index, bodyStart, bodyEnd, delimiters,
		) {
			return max(start, index-1)
		}
	}
	return max(start, min(bodyEnd-1, len(tokens)-1))
}

func javascriptLexSwitchBody(
	tokens []javascriptToken,
	switchIndex int,
	delimiters javascriptDelimiterPairs,
) (int, int, bool) {
	if switchIndex < 0 || switchIndex+1 >= len(tokens) ||
		tokens[switchIndex+1].text != "(" ||
		switchIndex > 0 &&
			(tokens[switchIndex-1].text == "." || tokens[switchIndex-1].text == "?.") {
		return 0, 0, false
	}
	conditionEnd, paired := delimiters.get(switchIndex + 1)
	if !paired || conditionEnd+1 >= len(tokens) || tokens[conditionEnd+1].text != "{" {
		return 0, 0, false
	}
	bodyStart := conditionEnd + 1
	bodyEnd, paired := delimiters.get(bodyStart)
	return bodyStart, bodyEnd, paired && bodyEnd > bodyStart
}

func javascriptLexSwitchClauseAt(
	tokens []javascriptToken,
	index, bodyStart, bodyEnd int,
	delimiters javascriptDelimiterPairs,
) bool {
	if index <= bodyStart || index >= bodyEnd || index >= len(tokens) ||
		(tokens[index].text != "case" && tokens[index].text != "default") ||
		index > 0 && (tokens[index-1].text == "." || tokens[index-1].text == "?.") {
		return false
	}
	if tokens[index].text == "default" {
		return index+1 < bodyEnd && tokens[index+1].text == ":"
	}
	for cursor := index + 1; cursor < bodyEnd && cursor < len(tokens); cursor++ {
		if (tokens[cursor].text == "(" || tokens[cursor].text == "[" ||
			tokens[cursor].text == "{") && delimiters.at(cursor) > cursor {
			cursor = delimiters.at(cursor)
			continue
		}
		switch tokens[cursor].text {
		case ":":
			return true
		case ";", "case", "default", "}":
			return false
		}
	}
	return false
}

func javascriptLexicalFunctionBodies(
	source string,
	tokens []javascriptToken,
	delimiters javascriptDelimiterPairs,
	boundaries javascriptLexBoundaries,
	objectBraces []javascriptByteSpan,
) []javascriptTokenRange {
	ranges := make([]javascriptTokenRange, 0)
	contexts, _ := javascriptLexicalBraceContexts(source, tokens, delimiters, objectBraces)
	var expressionEnds []int
	for index, token := range tokens {
		if token.text == "=>" && index+1 < len(tokens) && tokens[index+1].text != "{" {
			expressionEnds = javascriptLexicalExpressionEnds(
				tokens, delimiters, boundaries.forcedStarts,
			)
			break
		}
	}
	for index, token := range tokens {
		bodyStart := -1
		deferredStart := -1
		parameterEnd := -1
		methodParameters := false
		switch token.text {
		case "function":
			parameterOpen := javascriptNextToken(tokens, index+1)
			if parameterClose, ok := delimiters.get(parameterOpen); ok &&
				parameterClose > parameterOpen && parameterClose+1 < len(tokens) &&
				tokens[parameterClose+1].text == "{" {
				deferredStart = parameterOpen
				bodyStart = parameterClose + 1
			}
		case "=>":
			if index+1 < len(tokens) && tokens[index+1].text == "{" {
				deferredStart = javascriptLexArrowStart(tokens, index, delimiters)
				bodyStart = index + 1
			} else if index+1 < len(tokens) && expressionEnds[index] > index+1 {
				ranges = append(ranges, javascriptTokenRange{
					start: javascriptLexArrowStart(tokens, index, delimiters),
					end:   expressionEnds[index],
				})
			}
		case "class":
			// Class heritage, decorators, and computed keys run while the class
			// is evaluated. Method bodies and field initializers are added as
			// narrower deferred ranges below.
		}
		if token.text == "(" && index < len(contexts) &&
			(contexts[index] == javascriptLexicalBraceObject ||
				contexts[index] == javascriptLexicalBraceClass) {
			if parameterClose, paired := delimiters.get(index); paired &&
				parameterClose > index && parameterClose+1 < len(tokens) &&
				tokens[parameterClose+1].text == "{" {
				deferredStart = index
				parameterEnd = parameterClose
				bodyStart = parameterClose + 1
				methodParameters = true
			}
		}
		if bodyEnd, ok := delimiters.get(bodyStart); ok && bodyEnd > bodyStart {
			if methodParameters {
				ranges = append(ranges, javascriptLexMethodDeferredRanges(
					max(deferredStart, 0), parameterEnd, bodyEnd, tokens, delimiters,
				)...)
			} else {
				ranges = append(ranges, javascriptTokenRange{
					start: max(deferredStart, 0),
					end:   bodyEnd,
				})
			}
		}
	}
	for index, token := range tokens {
		if token.text != "=" || index >= len(contexts) ||
			contexts[index] != javascriptLexicalBraceClass || index+1 >= len(tokens) ||
			javascriptLexStaticFieldInitializer(tokens, index, delimiters) {
			continue
		}
		end := boundaries.statementEnd(index)
		if end > index+1 {
			ranges = append(ranges, javascriptTokenRange{start: index, end: end})
		}
	}
	sort.Slice(ranges, func(first, second int) bool {
		if ranges[first].start != ranges[second].start {
			return ranges[first].start < ranges[second].start
		}
		return ranges[first].end < ranges[second].end
	})
	merged := ranges[:0]
	for _, tokenRange := range ranges {
		last := len(merged) - 1
		if last < 0 || tokenRange.start >= merged[last].end {
			merged = append(merged, tokenRange)
			continue
		}
		merged[last].end = max(merged[last].end, tokenRange.end)
	}
	return merged
}

func javascriptLexMethodDeferredRanges(
	parameterStart int,
	parameterEnd int,
	bodyEnd int,
	tokens []javascriptToken,
	delimiters javascriptDelimiterPairs,
) []javascriptTokenRange {
	if parameterStart < 0 || parameterEnd <= parameterStart || bodyEnd <= parameterEnd ||
		bodyEnd > len(tokens) {
		return nil
	}
	decorators := javascriptLexParameterDecoratorRanges(
		tokens, parameterStart, parameterEnd, delimiters,
	)
	if len(decorators) == 0 {
		return []javascriptTokenRange{{start: parameterStart, end: bodyEnd}}
	}
	ranges := make([]javascriptTokenRange, 0, len(decorators)+1)
	cursor := parameterStart
	for _, decorator := range decorators {
		if decorator.start > cursor {
			ranges = append(ranges, javascriptTokenRange{start: cursor, end: decorator.start})
		}
		cursor = max(cursor, decorator.end)
	}
	if cursor < bodyEnd {
		ranges = append(ranges, javascriptTokenRange{start: cursor, end: bodyEnd})
	}
	return ranges
}

func javascriptLexParameterDecoratorRanges(
	tokens []javascriptToken,
	parameterStart int,
	parameterEnd int,
	delimiters javascriptDelimiterPairs,
) []javascriptTokenRange {
	if parameterStart < 0 || parameterEnd <= parameterStart || parameterEnd >= len(tokens) {
		return nil
	}
	ranges := make([]javascriptTokenRange, 0)
	for cursor := parameterStart + 1; cursor < parameterEnd; {
		for cursor < parameterEnd && tokens[cursor].text == "@" {
			end := javascriptLexDecoratorExpressionEnd(
				tokens, cursor, parameterEnd, delimiters,
			)
			if end <= cursor {
				break
			}
			ranges = append(ranges, javascriptTokenRange{start: cursor, end: end})
			cursor = end
		}
		for cursor < parameterEnd && tokens[cursor].text != "," {
			if (tokens[cursor].text == "(" || tokens[cursor].text == "[" ||
				tokens[cursor].text == "{") && delimiters.at(cursor) > cursor {
				cursor = delimiters.at(cursor) + 1
				continue
			}
			if tokens[cursor].text == "<" {
				if end := typeScriptLexGenericArgumentEnd(
					tokens, cursor, delimiters,
				); end > cursor && end < parameterEnd {
					cursor = end + 1
					continue
				}
			}
			cursor++
		}
		if cursor < parameterEnd && tokens[cursor].text == "," {
			cursor++
		}
	}
	return ranges
}

func javascriptLexDecoratorExpressionEnd(
	tokens []javascriptToken,
	start int,
	limit int,
	delimiters javascriptDelimiterPairs,
) int {
	if start < 0 || start+1 >= limit || limit > len(tokens) || tokens[start].text != "@" {
		return -1
	}
	cursor := start + 1
	if tokens[cursor].text == "(" {
		if end, paired := delimiters.get(cursor); paired && end > cursor && end < limit {
			return end + 1
		}
		return -1
	}
	if !javascriptLexBindingName(tokens[cursor].text) {
		return -1
	}
	cursor++
	for cursor < limit {
		switch tokens[cursor].text {
		case ".", "?.":
			if cursor+1 >= limit || !javascriptLexBindingName(tokens[cursor+1].text) {
				return cursor
			}
			cursor += 2
		case "<":
			end := typeScriptLexGenericArgumentEnd(tokens, cursor, delimiters)
			if end <= cursor || end >= limit {
				return cursor
			}
			cursor = end + 1
		case "(":
			end, paired := delimiters.get(cursor)
			if !paired || end <= cursor || end >= limit {
				return cursor
			}
			cursor = end + 1
		default:
			return cursor
		}
	}
	return cursor
}

func javascriptLexStaticFieldInitializer(
	tokens []javascriptToken,
	equalIndex int,
	delimiters javascriptDelimiterPairs,
) bool {
	if equalIndex <= 0 || equalIndex >= len(tokens) || tokens[equalIndex].text != "=" {
		return false
	}
	fieldStart := equalIndex - 1
	if tokens[fieldStart].text == "]" {
		if open, paired := delimiters.get(fieldStart); !paired || open < 0 {
			return false
		} else {
			fieldStart = open
		}
	}
	if tokens[fieldStart].startsLine() {
		return false
	}
	prefix := fieldStart - 1
	if prefix >= 0 && tokens[prefix].text == "accessor" {
		prefix--
	}
	return prefix >= 0 && tokens[prefix].text == "static"
}

func javascriptLexicalExpressionEnds(
	tokens []javascriptToken,
	delimiters javascriptDelimiterPairs,
	forcedStarts []bool,
) []int {
	ends := make([]int, len(tokens))
	depths := make([]int, len(tokens))
	depth, maximumDepth := 0, 0
	for index, token := range tokens {
		depths[index] = depth
		_, opener, delimiter := javascriptDelimiterKind(token.text)
		if !delimiter {
			continue
		}
		if opener && delimiters.at(index) > index {
			depth++
			maximumDepth = max(maximumDepth, depth)
		} else if !opener && delimiters.at(index) < index && depth > 0 {
			depth--
		}
	}
	next := make([]int, maximumDepth+1)
	for index := range next {
		next[index] = len(tokens)
	}
	for index := len(tokens) - 1; index >= 0; index-- {
		depth = min(depths[index], len(next)-1)
		ends[index] = next[depth]
		_, opener, delimiter := javascriptDelimiterKind(tokens[index].text)
		boundary := tokens[index].text == "," || tokens[index].text == ";" ||
			delimiter && !opener ||
			index < len(forcedStarts) && forcedStarts[index] ||
			tokens[index].startsLine() && (javascriptHardDeclarationToken(tokens[index].text) &&
				javascriptLexHardStatementStart(tokens, index, delimiters) ||
				index > 0 && javascriptTokenCanEnd(tokens[index-1]) &&
					!javascriptTokenContinuesExpression(tokens[index]))
		if boundary {
			next[depth] = index
		}
	}
	return ends
}

func javascriptExpressionContinuationToken(token string) bool {
	switch token {
	case ".", "?.", "(", "[", "+", "-", "*", "/", "%", "**", "&&", "||", "??",
		"&", "|", "^", "<<", ">>", ">>>", "<", ">", "<=", ">=", "==", "!=", "===",
		"!==", "?", ":", "in", "instanceof", "of", "=", "+=", "-=", "*=", "/=", "%=",
		"&=", "|=", "^=", "&&=", "||=", "??=", "=>", ",":
		return true
	}
	return false
}

func javascriptTokenContinuesExpression(token javascriptToken) bool {
	return token.text == javascriptLexicalTemplateToken ||
		javascriptExpressionContinuationToken(token.text)
}

func javascriptTokenInsideRanges(index int, ranges []javascriptTokenRange) bool {
	position := sort.Search(len(ranges), func(rangeIndex int) bool {
		return ranges[rangeIndex].start >= index
	})
	if position == 0 {
		return false
	}
	return ranges[position-1].end > index
}

func javascriptLexicalRequireCall(
	source string,
	tokens []javascriptToken,
	requireIndex int,
	delimiters javascriptDelimiterPairs,
	boundaries javascriptLexBoundaries,
) bool {
	if requireIndex > 0 && (tokens[requireIndex-1].text == "." ||
		tokens[requireIndex-1].text == "?.") {
		return false
	}
	open := requireIndex + 1
	if open >= len(tokens) || tokens[open].text != "(" {
		return false
	}
	var end int
	if closeIndex, ok := delimiters.get(open); ok && closeIndex > open {
		end = closeIndex
	} else {
		end = boundaries.statementEnd(requireIndex) + 1
	}
	argumentCount := 0
	for index := open + 1; index < end && index < len(tokens); index++ {
		if tokens[index].text == "," && index == end-1 && argumentCount == 1 {
			continue
		}
		tokenStart := tokens[index].startOffset()
		if !tokens[index].literal() || tokenStart >= len(source) ||
			(source[tokenStart] != '\'' && source[tokenStart] != '"') {
			return false
		}
		argumentCount++
	}
	return argumentCount == 1
}

func javascriptLexicalStatementStarts(
	source string,
	tokens []javascriptToken,
	delimiters javascriptDelimiterPairs,
	objectBraces []javascriptByteSpan,
	forcedStarts []bool,
) []int {
	if objectBraces == nil {
		objectBraces = scanJavaScriptFallback(source).objectBraces
	}
	objectStarts := make(map[int]bool, len(objectBraces))
	for _, span := range objectBraces {
		objectStarts[span.start] = true
	}
	valueBraceStarts := javascriptLexicalValueBraceStarts(tokens, delimiters, objectStarts)
	contexts, _ := javascriptLexicalBraceContexts(source, tokens, delimiters, objectBraces)
	expressionBrace := func(index int) bool {
		if index < 0 || index >= len(tokens) || tokens[index].text != "{" {
			return false
		}
		if objectStarts[tokens[index].startOffset()] ||
			javascriptLexModuleSpecifierBrace(tokens, index) {
			return true
		}
		return index > 0 && javascriptLexicalBindingDeclarationToken(tokens[index-1].text)
	}
	starts := make([]int, len(tokens))
	valueBraceOuterStarts := make(map[int]int, len(valueBraceStarts))
	current := 0
	for index, token := range tokens {
		if index > 0 {
			previousIndex := index - 1
			if index < len(forcedStarts) && forcedStarts[index] {
				current = index
			}
			switch tokens[previousIndex].text {
			case ";":
				current = index
			case "{":
				if !expressionBrace(previousIndex) {
					current = index
				}
			case "}":
				open := delimiters.at(previousIndex)
				if outer, valueBrace := valueBraceOuterStarts[previousIndex]; valueBrace &&
					(javascriptTokenContinuesExpression(token) ||
						token.text == ")" || token.text == "]" || token.text == "}") {
					current = outer
				} else if !expressionBrace(open) &&
					!javascriptLexControlContinuationAt(tokens, index, delimiters, contexts) {
					current = index
				}
			}
			if token.startsLine() && token.text != ")" && token.text != "]" &&
				token.text != "}" && javascriptLexStatementCanEnd(tokens[previousIndex]) &&
				!javascriptTokenContinuesExpression(token) &&
				!javascriptLexControlContinuationAt(tokens, index, delimiters, contexts) &&
				(!javascriptHardDeclarationToken(token.text) ||
					!javascriptLexContinuesExportDeclaration(tokens, index)) {
				current = index
			}
		}
		if token.startsLine() && javascriptHardDeclarationToken(token.text) &&
			javascriptLexHardStatementStart(tokens, index, delimiters) &&
			!javascriptLexContinuesExportDeclaration(tokens, index) {
			current = index
		}
		starts[index] = current
		if valueBraceStarts[index] {
			if closingIndex, paired := delimiters.get(index); paired && closingIndex > index {
				valueBraceOuterStarts[closingIndex] = current
			}
		}
	}
	return starts
}

func javascriptLexContinuesExportDeclaration(tokens []javascriptToken, index int) bool {
	if index <= 0 || index >= len(tokens) {
		return false
	}
	if tokens[index-1].text == "export" {
		return true
	}
	return index > 1 && tokens[index-1].text == "default" &&
		tokens[index-2].text == "export"
}

func javascriptLexicalValueBraceStarts(
	tokens []javascriptToken,
	delimiters javascriptDelimiterPairs,
	objectStarts map[int]bool,
) map[int]bool {
	starts := make(map[int]bool, len(objectStarts))
	for index, token := range tokens {
		if token.text == "{" && objectStarts[token.startOffset()] {
			starts[index] = true
		}
		if token.text == "=>" && index+1 < len(tokens) && tokens[index+1].text == "{" {
			starts[index+1] = true
		}
		if token.text == "function" && javascriptLexKeywordStartsValue(tokens, index) {
			parameterOpen := javascriptNextToken(tokens, index+1)
			if parameterClose, paired := delimiters.get(parameterOpen); paired &&
				parameterClose+1 < len(tokens) && tokens[parameterClose+1].text == "{" {
				starts[parameterClose+1] = true
			}
		}
		if token.text == "class" && javascriptLexKeywordStartsValue(tokens, index) {
			if bodyStart := javascriptLexClassBodyStart(tokens, index, delimiters, 0); bodyStart >= 0 {
				starts[bodyStart] = true
			}
		}
	}
	return starts
}

func javascriptLexKeywordStartsValue(tokens []javascriptToken, index int) bool {
	if index < 0 || index >= len(tokens) ||
		index > 0 && (tokens[index-1].text == "." || tokens[index-1].text == "?.") {
		return false
	}
	start := index
	if start > 0 && tokens[start-1].text == "async" && !tokens[start].startsLine() {
		start--
	}
	if start <= 0 {
		return false
	}
	previous := tokens[start-1].text
	return javascriptFallbackKeywordStartsValue(
		previous, javascriptDecodedIdentifier(previous),
	)
}

func javascriptLexStatementCanEnd(token javascriptToken) bool {
	if !javascriptTokenCanEnd(token) {
		return false
	}
	switch javascriptDecodedIdentifier(token.text) {
	case "var", "let", "const", "using", "import", "export", "from", "as",
		"return", "throw", "yield", "await", "new", "typeof", "void", "delete",
		"instanceof", "in", "of", "extends", "else", "do", "case":
		return false
	default:
		return true
	}
}

func mergeJavaScriptDefinitions(
	lineCount int,
	tree *javascriptSyntaxTree,
	treeDefinitions []sourceDefinition,
	lexical []javascriptLexDefinition,
	recoveryLines map[int]bool,
) []sourceDefinition {
	definitions := append([]sourceDefinition(nil), treeDefinitions...)
	seen := make(map[javascriptDefinitionIdentity]int, len(definitions))
	for index, definition := range definitions {
		seen[javascriptDefinitionIdentity{
			symbol: definition.symbol,
			line:   definition.line,
			column: definition.column,
		}] = index
	}
	for _, candidate := range lexical {
		definition := normalizeJavaScriptDefinition(candidate.definition, lineCount)
		if definition.symbol == "" {
			continue
		}
		key := javascriptDefinitionIdentity{
			symbol: definition.symbol,
			line:   definition.line,
			column: definition.column,
		}
		if prior, exists := seen[key]; exists {
			if candidate.force && javascriptDefinitionHasWiderScope(
				definition, definitions[prior],
			) {
				definitions[prior] = definition
			}
			continue
		}
		if tree != nil && !candidate.force &&
			!javascriptDefinitionTouchesRecovery(definition, recoveryLines) {
			continue
		}
		seen[key] = len(definitions)
		definitions = append(definitions, definition)
	}
	return sortUniqueJavaScriptDefinitions(definitions)
}

func javascriptDefinitionTouchesRecovery(
	definition sourceDefinition,
	recoveryLines map[int]bool,
) bool {
	return recoveryLines[definition.line]
}

func mergeJavaScriptScopes(
	lineCount int,
	treeScopes, lexicalScopes []javascriptLineScope,
	definitions []sourceDefinition,
	lexicalOnly bool,
	recoveryLines map[int]bool,
) []javascriptLineScope {
	scopes := append([]javascriptLineScope(nil), treeScopes...)
	recoveryPrefix := javascriptRecoveryLinePrefix(lineCount, recoveryLines, lexicalOnly)
	for _, scope := range lexicalScopes {
		if scope.start < 1 || scope.end < scope.start || scope.end > lineCount {
			continue
		}
		if lexicalOnly || javascriptLineRangeTouchesRecovery(
			scope.start, scope.end, recoveryPrefix,
		) {
			scopes = append(scopes, scope)
		}
	}
	for _, definition := range definitions {
		if !definition.ownsScope {
			continue
		}
		scope := javascriptLineScope{start: definition.scopeStart, end: definition.scopeEnd}
		if scope.start < 1 || scope.end < scope.start || scope.end > lineCount {
			continue
		}
		scopes = append(scopes, scope)
	}
	return normalizeJavaScriptScopes(scopes)
}

func mergeJavaScriptImports(
	lineCount int,
	treeImports, lexical []javascriptLineSpan,
	lexicalOnly bool,
	recoveryLines map[int]bool,
) []javascriptLineSpan {
	imports := append([]javascriptLineSpan(nil), treeImports...)
	recoveryPrefix := javascriptRecoveryLinePrefix(lineCount, recoveryLines, lexicalOnly)
	for _, statement := range lexical {
		if statement.start < 1 || statement.end < statement.start || statement.end > lineCount {
			continue
		}
		if lexicalOnly || javascriptLineRangeTouchesRecovery(
			statement.start, statement.end, recoveryPrefix,
		) {
			imports = append(imports, statement)
		}
	}
	return normalizeJavaScriptLineSpans(imports)
}

func javascriptRecoveryLinePrefix(
	lineCount int,
	recoveryLines map[int]bool,
	lexicalOnly bool,
) []int {
	if lexicalOnly || lineCount < 1 || len(recoveryLines) == 0 {
		return nil
	}
	prefix := make([]int, lineCount+1)
	for line := 1; line <= lineCount; line++ {
		prefix[line] = prefix[line-1]
		if recoveryLines[line] {
			prefix[line]++
		}
	}
	return prefix
}

func javascriptLineRangeTouchesRecovery(start, end int, recoveryPrefix []int) bool {
	if start < 1 || end < start || end >= len(recoveryPrefix) {
		return false
	}
	return recoveryPrefix[end] > recoveryPrefix[start-1]
}
