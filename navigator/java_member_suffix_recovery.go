package navigator

import "sort"

const javaMaximumMemberSuffixCandidateLines = 16

type javaMemberSuffixCandidate struct {
	index int
	line  int
	owner int
}

// javaRecoverMemberSuffixes performs a bounded declaration resynchronization
// after an incomplete physical member line. The ordinary lexical pass remains
// the source of truth before that point. A candidate whose raw brace owner is
// the containing type is an unambiguous sibling. Candidates underneath a
// brace that may have consumed the type's closing brace are staged until a
// callable or constructor establishes a new member boundary.
func javaRecoverMemberSuffixes(
	source string,
	lineCount int,
	lexed javaLexResult,
	positions javaSourcePositions,
	delimiters javaDelimiterAnalysis,
	typeBodies map[int]javaTypeBody,
) ([]sourceDefinition, map[int]struct{}) {
	tokens := lexed.tokens
	if len(tokens) == 0 || len(typeBodies) == 0 {
		return nil, nil
	}

	tokenLines := javaMemberSuffixTokenLines(tokens, positions.lineStarts)
	enclosingTypes := javaMemberSuffixEnclosingTypes(tokens, delimiters, typeBodies)
	tokensByType := javaMemberSuffixTokensByType(enclosingTypes)
	switchRuleExpressions := javaMemberSuffixSwitchRuleExpressions(tokens, delimiters)
	expressionAnchors := javaMemberSuffixExpressionAnchors(delimiters, switchRuleExpressions)
	definitions := make([]sourceDefinition, 0)
	invalidHeaders := make(map[int]struct{})

	for bodyOpen, body := range typeBodies {
		bodyClose := javaDelimiterMatch(delimiters, bodyOpen)
		bodyMatched := bodyClose > bodyOpen
		limit := len(tokens)
		if bodyMatched {
			limit = bodyClose
		}

		recovering := false
		recoveredOwner := -1
		line := 0
		lineBoundary := true
		restartCandidate := false
		skipThrough := -1
		candidates := make([]javaMemberSuffixCandidate, 0,
			javaMaximumMemberSuffixCandidateLines)
		staged := make(map[int][]sourceDefinition)

		for _, index := range tokensByType[bodyOpen] {
			if index <= bodyOpen || index >= limit {
				continue
			}
			if index <= skipThrough || enclosingTypes[index] != bodyOpen {
				continue
			}
			direct := index < len(delimiters.braceOwner) &&
				delimiters.braceOwner[index] == bodyOpen
			if bodyMatched && !direct {
				continue
			}

			tokenLine := tokenLines[index]
			lineChanged := line != 0 && tokenLine != line
			if lineChanged && !lineBoundary {
				recovering = true
			}
			if line == 0 || lineChanged {
				line = tokenLine
				lineBoundary = false
				restartCandidate = true
			}
			if recovering && restartCandidate &&
				javaMemberSuffixMayStart(tokens[index]) {
				javaAppendMemberSuffixCandidate(&candidates, javaMemberSuffixCandidate{
					index: index, line: tokenLine,
					owner: delimiters.braceOwner[index],
				})
			}
			restartCandidate = false

			switch tokens[index].value {
			case ";":
				if recovering {
					candidateDefinitions, strong, candidate, ok :=
						javaRecoverMemberSuffixBoundary(
							source, lineCount, lexed, positions, tokens,
							candidates, index+1, body,
						)
					if ok {
						strong = strong || javaMemberSuffixExpressionOwnerAnchor(
							expressionAnchors, candidate,
							bodyOpen, recoveredOwner,
						)
						definitions, recoveredOwner =
							javaCommitMemberSuffixCandidate(
								definitions, staged, candidateDefinitions,
								candidate.owner, bodyOpen, strong, recoveredOwner,
							)
					}
				}
				candidates = candidates[:0]
				lineBoundary = true
				restartCandidate = true

			case "{":
				closeIndex := javaDelimiterMatch(delimiters, index)
				initializer := direct && javaBraceStartsInitializer(
					tokens, delimiters, bodyOpen, index,
				)
				if !bodyMatched && initializer && closeIndex > index {
					definitions = append(definitions, javaRecoverMemberInitializerPrefix(
						lineCount, lexed, positions, delimiters, index, closeIndex,
					)...)
				}
				if recovering && closeIndex > index {
					candidateDefinitions, strong, candidate, ok :=
						javaRecoverMemberSuffixBoundary(
							source, lineCount, lexed, positions, tokens,
							candidates, closeIndex+1, body,
						)
					if ok {
						strong = strong || javaMemberSuffixExpressionOwnerAnchor(
							expressionAnchors, candidate,
							bodyOpen, recoveredOwner,
						)
					}
					if ok && strong {
						definitions, recoveredOwner =
							javaCommitMemberSuffixCandidate(
								definitions, staged, candidateDefinitions,
								candidate.owner, bodyOpen, true, recoveredOwner,
							)
						candidates = candidates[:0]
						lineBoundary = true
						restartCandidate = true
						skipThrough = closeIndex
						continue
					}
				}
				if closeIndex > index && recoveredOwner >= 0 &&
					delimiters.braceOwner[index] == recoveredOwner {
					// Once a stolen sibling owner is established, a complete
					// child body belongs to that sibling. Skip it so locals in
					// initializer, lambda, and anonymous bodies cannot inherit
					// the recovered member authority.
					skipThrough = closeIndex
					if !javaMemberSuffixCandidateHasInitializer(tokens, candidates, index) {
						candidates = candidates[:0]
						lineBoundary = true
						restartCandidate = true
					}
					continue
				}
				if bodyMatched && direct && closeIndex > index && !initializer {
					lineBoundary = true
					skipThrough = closeIndex
					candidates = candidates[:0]
					restartCandidate = true
				}
			}
		}
		javaRejectUnanchoredMemberSuffixes(staged, positions.lineStarts, invalidHeaders)
	}

	return sortUniqueJavaTreeDefinitions(definitions), invalidHeaders
}

// A weak declaration beneath a stolen raw brace owner remains staged until a
// callable proves that the parser crossed back into the containing type. If
// no such anchor arrives, the concrete parser must not independently promote
// the staged declaration: it can still be a local following a local type.
func javaRejectUnanchoredMemberSuffixes(
	staged map[int][]sourceDefinition,
	lineStarts []int,
	invalidHeaders map[int]struct{},
) {
	for _, definitions := range staged {
		for _, definition := range definitions {
			if definition.line < 1 || definition.line > len(lineStarts) ||
				definition.column < 1 {
				continue
			}
			invalidHeaders[lineStarts[definition.line-1]+definition.column-1] = struct{}{}
		}
	}
}

func javaMemberSuffixTokenLines(tokens []javaToken, lineStarts []int) []int {
	lines := make([]int, len(tokens))
	lineIndex := 0
	for index, token := range tokens {
		for lineIndex+1 < len(lineStarts) && lineStarts[lineIndex+1] <= token.start {
			lineIndex++
		}
		lines[index] = lineIndex + 1
	}
	return lines
}

func javaMemberSuffixEnclosingTypes(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	typeBodies map[int]javaTypeBody,
) []int {
	enclosing := make([]int, len(tokens))
	for index := range enclosing {
		enclosing[index] = -1
		owner := -1
		if index < len(delimiters.braceOwner) {
			owner = delimiters.braceOwner[index]
		}
		if _, ok := typeBodies[owner]; ok {
			enclosing[index] = owner
		} else if owner >= 0 && owner < index {
			enclosing[index] = enclosing[owner]
		}
	}
	return enclosing
}

func javaMemberSuffixTokensByType(enclosingTypes []int) map[int][]int {
	tokensByType := make(map[int][]int)
	for index, bodyOpen := range enclosingTypes {
		if bodyOpen >= 0 {
			tokensByType[bodyOpen] = append(tokensByType[bodyOpen], index)
		}
	}
	return tokensByType
}

func javaMemberSuffixSwitchRuleExpressions(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
) []bool {
	switchBodies := make([]bool, len(tokens))
	for brace := range tokens {
		if tokens[brace].value != "{" || brace == 0 || tokens[brace-1].value != ")" {
			continue
		}
		condition := javaDelimiterMatch(delimiters, brace-1)
		switchBodies[brace] = condition > 0 && tokens[condition-1].value == "switch"
	}

	expression := make([]bool, len(tokens))
	active := make([]bool, len(tokens))
	pendingLabel := make([]bool, len(tokens))
	parenthesisDepth := make([]int, len(tokens))
	bracketDepth := make([]int, len(tokens))
	conditionalDepth := make([]int, len(tokens))
	for index, token := range tokens {
		if index >= len(delimiters.braceOwner) {
			break
		}
		owner := delimiters.braceOwner[index]
		if owner < 0 || owner >= len(tokens) || !switchBodies[owner] {
			continue
		}
		topLevel := parenthesisDepth[owner] == 0 && bracketDepth[owner] == 0
		switch token.value {
		case "(":
			parenthesisDepth[owner]++
		case ")":
			parenthesisDepth[owner] = max(0, parenthesisDepth[owner]-1)
		case "[":
			bracketDepth[owner]++
		case "]":
			bracketDepth[owner] = max(0, bracketDepth[owner]-1)
		case "case", "default":
			if !topLevel {
				continue
			}
			active[owner] = false
			pendingLabel[owner] = true
			conditionalDepth[owner] = 0
		case "?":
			if topLevel && pendingLabel[owner] &&
				!javaMemberSuffixQuestionIsWildcard(tokens, delimiters, index) {
				conditionalDepth[owner]++
			}
		case ":":
			if topLevel && pendingLabel[owner] {
				if conditionalDepth[owner] > 0 {
					conditionalDepth[owner]--
				} else {
					active[owner] = false
					pendingLabel[owner] = false
				}
			}
		case "->":
			if topLevel && pendingLabel[owner] {
				active[owner] = true
				pendingLabel[owner] = false
				conditionalDepth[owner] = 0
			}
		case ";":
			if topLevel {
				pendingLabel[owner] = false
				conditionalDepth[owner] = 0
			}
		default:
			expression[index] = topLevel && active[owner]
		}
	}
	return expression
}

func javaMemberSuffixQuestionIsWildcard(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	question int,
) bool {
	previous := question - 1
	for previous >= 0 {
		annotationStart, annotation := javaAnnotationStartBefore(
			tokens, delimiters, previous,
		)
		if !annotation {
			break
		}
		previous = annotationStart - 1
	}
	return previous >= 0 &&
		(tokens[previous].value == "<" || tokens[previous].value == ",")
}

// expressionAnchors collapses array-initializer and genuine switch-rule owner
// chains once. Candidate checks stay O(1), including deeply nested arrays.
func javaMemberSuffixExpressionAnchors(
	delimiters javaDelimiterAnalysis,
	switchRuleExpressions []bool,
) []int {
	anchors := make([]int, len(delimiters.braceOwner))
	for index := range anchors {
		anchors[index] = -1
		owner := delimiters.braceOwner[index]
		if owner < 0 || owner >= index {
			continue
		}
		arrayExpression := owner < len(delimiters.arrayInitializer) &&
			delimiters.arrayInitializer[owner]
		switchExpression := index < len(switchRuleExpressions) &&
			switchRuleExpressions[index]
		if arrayExpression || switchExpression {
			anchors[index] = anchors[owner]
			if anchors[index] < 0 {
				anchors[index] = delimiters.braceOwner[owner]
			}
		}
	}
	return anchors
}

func javaMemberSuffixExpressionOwnerAnchor(
	expressionAnchors []int,
	candidate javaMemberSuffixCandidate,
	bodyOpen, recoveredOwner int,
) bool {
	if candidate.index < 0 || candidate.index >= len(expressionAnchors) {
		return false
	}
	anchor := expressionAnchors[candidate.index]
	return anchor >= 0 && (anchor == bodyOpen ||
		recoveredOwner >= 0 && anchor == recoveredOwner)
}

func javaMemberSuffixMayStart(token javaToken) bool {
	if javaTokenIsSourceName(token) || javaPrimitiveOrVoid(token.value) {
		return true
	}
	switch token.value {
	case "@", "<", "public", "protected", "private", "static", "final",
		"abstract", "native", "synchronized", "strictfp", "default", "volatile",
		"transient",
		"sealed", "non-sealed", "class", "interface", "enum", "record":
		return true
	default:
		return false
	}
}

func javaAppendMemberSuffixCandidate(
	candidates *[]javaMemberSuffixCandidate,
	candidate javaMemberSuffixCandidate,
) {
	if len(*candidates) == javaMaximumMemberSuffixCandidateLines {
		copy(*candidates, (*candidates)[1:])
		*candidates = (*candidates)[:len(*candidates)-1]
	}
	*candidates = append(*candidates, candidate)
}

func javaMemberSuffixCandidateHasInitializer(
	tokens []javaToken,
	candidates []javaMemberSuffixCandidate,
	brace int,
) bool {
	if len(candidates) == 0 || brace < 0 || brace > len(tokens) {
		return false
	}
	start := candidates[len(candidates)-1].index
	for index := max(0, start); index < brace; index++ {
		switch tokens[index].value {
		case "=", "->", "new", "default":
			return true
		}
	}
	return false
}

func javaRecoverMemberSuffixBoundary(
	source string,
	lineCount int,
	lexed javaLexResult,
	positions javaSourcePositions,
	tokens []javaToken,
	candidates []javaMemberSuffixCandidate,
	end int,
	body javaTypeBody,
) ([]sourceDefinition, bool, javaMemberSuffixCandidate, bool) {
	for index := len(candidates) - 1; index >= 0; index-- {
		candidate := candidates[index]
		if candidate.index < 0 || candidate.index >= end || end > len(tokens) ||
			end-candidate.index > javaMaximumRecoveryHeaderTokens {
			continue
		}
		definitions, strong := javaAnalyzeMemberSuffixWindow(
			source, lineCount, lexed, positions,
			candidate.index, end, candidate.line, body,
		)
		if len(definitions) > 0 {
			return definitions, strong, candidate, true
		}
	}
	return nil, false, javaMemberSuffixCandidate{}, false
}

func javaAnalyzeMemberSuffixWindow(
	source string,
	lineCount int,
	lexed javaLexResult,
	positions javaSourcePositions,
	start, end, startLine int,
	body javaTypeBody,
) ([]sourceDefinition, bool) {
	if start < 0 || start >= end || end > len(lexed.tokens) {
		return nil, false
	}
	lineStart := lexed.tokens[start].start
	if startLine > 0 && startLine <= len(positions.lineStarts) {
		lineStart = positions.lineStarts[startLine-1]
	}
	endOffset := lexed.tokens[end-1].end
	window := lexed
	window.tokens = lexed.tokens[start:end]
	window.commentSpans = javaMemberSuffixWindowSpans(
		lexed.commentSpans, lineStart, endOffset,
	)
	window.stringSpans = javaMemberSuffixWindowSpans(
		lexed.stringSpans, lineStart, endOffset,
	)
	window.truncated = false

	local := analyzeJavaLexicallyWithPositionsMode(
		source, lineCount, window, positions, false,
	)
	definitions := append([]sourceDefinition(nil), local.definitions...)

	windowDelimiters := analyzeJavaDelimiters(window.tokens)
	invalidHeaders := make(map[int]struct{})
	javaAppendLexicalCallables(
		source, lineCount, window.commentSpans, window.input,
		window.tokens, windowDelimiters, positions,
		-1, len(window.tokens), body,
		javaTopLevelRecoveryMembers(window.tokens, windowDelimiters),
		window.stringSpans, &definitions, invalidHeaders,
	)
	definitions = sortUniqueJavaTreeDefinitions(definitions)

	filtered := definitions[:0]
	strong := false
	for _, definition := range definitions {
		if definition.line < 1 || definition.line > len(positions.lineStarts) {
			continue
		}
		offset := positions.lineStarts[definition.line-1] + definition.column - 1
		if offset < lexed.tokens[start].start || offset >= endOffset {
			continue
		}
		tokenIndex := sort.Search(len(window.tokens), func(index int) bool {
			return window.tokens[index].start >= offset
		})
		if tokenIndex >= len(window.tokens) ||
			window.tokens[tokenIndex].start != offset ||
			windowDelimiters.braceOwner[tokenIndex] >= 0 {
			continue
		}
		filtered = append(filtered, definition)
		if javaMemberSuffixDefinitionIsStrong(window.tokens, definition, offset, body) {
			strong = true
		}
	}
	return filtered, strong
}

func javaMemberSuffixWindowSpans(
	spans []javaByteSpan,
	start, end int,
) []javaByteSpan {
	first := sort.Search(len(spans), func(index int) bool {
		return spans[index].end > start
	})
	last := first
	for last < len(spans) && spans[last].start < end {
		last++
	}
	return spans[first:last]
}

func javaRecoverMemberInitializerPrefix(
	lineCount int,
	lexed javaLexResult,
	positions javaSourcePositions,
	delimiters javaDelimiterAnalysis,
	brace, closeBrace int,
) []sourceDefinition {
	if brace <= 0 || brace >= len(lexed.tokens) || closeBrace <= brace ||
		closeBrace >= len(lexed.tokens) {
		return nil
	}
	start := javaDeclarationPrefixStart(lexed.tokens, delimiters, brace)
	if start < 0 || start >= brace || brace-start > javaMaximumRecoveryHeaderTokens ||
		!javaHeaderContains(lexed.tokens, delimiters, start, brace, "=") {
		return nil
	}

	windowTokens := make([]javaToken, brace-start+1)
	copy(windowTokens, lexed.tokens[start:brace])
	windowTokens[len(windowTokens)-1] = javaToken{
		text: ";", value: ";",
		start: lexed.tokens[brace].start,
		end:   lexed.tokens[brace].start,
	}
	windowDelimiters := analyzeJavaDelimiters(windowTokens)
	windowStart := lexed.tokens[start].start
	windowEnd := lexed.tokens[brace].start
	definitions := make([]sourceDefinition, 0, 2)
	invalidHeaders := make(map[int]struct{})
	javaAppendLexicalFields(
		lineCount,
		javaMemberSuffixWindowSpans(lexed.commentSpans, windowStart, windowEnd),
		lexed.input, windowTokens, windowDelimiters, positions,
		-1, len(windowTokens), javaTypeBody{kind: "compact"},
		javaTopLevelRecoveryMembers(windowTokens, windowDelimiters),
		javaMemberSuffixWindowSpans(lexed.stringSpans, windowStart, windowEnd),
		&definitions, invalidHeaders,
	)
	if len(definitions) == 0 {
		return nil
	}
	if brace >= len(delimiters.arrayInitializer) ||
		!delimiters.arrayInitializer[brace] {
		last := len(definitions) - 1
		startOffset := javaLexicalAttachedStart(
			lexed.input, lexed.commentSpans, lexed.tokens[start].start,
		)
		scopeStart, scopeEnd := positions.lineSpan(
			startOffset, lexed.tokens[closeBrace].end,
		)
		definitions[last].ownsScope = true
		definitions[last].scopeStart = scopeStart
		definitions[last].scopeEnd = scopeEnd
		definitions[last].ownedEndColumn = 0
		// A following declaration-shaped token makes this closing brace a
		// recovered sibling boundary. Expression continuations such as
		// `}.method()` still belong to the field through its semicolon.
		next := closeBrace + 1
		siblingBoundary := next < len(lexed.tokens) &&
			javaMemberSuffixMayStart(lexed.tokens[next])
		if next < len(lexed.tokens) && lexed.tokens[next].value == "{" &&
			brace < len(delimiters.braceOwner) &&
			next < len(delimiters.braceOwner) &&
			delimiters.braceOwner[next] == delimiters.braceOwner[brace] {
			siblingBoundary = true
		}
		if siblingBoundary {
			definitions[last].ownedEndColumn = javaExactOwnedEndColumn(
				positions,
				scopeEnd,
				lexed.tokens[closeBrace].end,
				javaTokenIsExactPunctuation(
					lexed.input, lexed.tokens[closeBrace], '}',
				),
			)
		}
		definitions[last] = normalizeJavaTreeDefinition(definitions[last], lineCount)
	}
	return definitions
}

func javaMemberSuffixDefinitionIsStrong(
	tokens []javaToken,
	definition sourceDefinition,
	offset int,
	body javaTypeBody,
) bool {
	for index := range tokens {
		if tokens[index].start != offset || tokens[index].text != definition.symbol {
			continue
		}
		// A record name is followed by its component list, but that list is not
		// a callable boundary. Treating it as one would let a local record turn
		// later locals into recovered members of the enclosing type.
		if index > 0 && tokens[index-1].value == "record" {
			continue
		}
		if index+1 < len(tokens) && tokens[index+1].value == "(" {
			return true
		}
		if body.kind == "record" && definition.symbol == body.name &&
			index+1 < len(tokens) && tokens[index+1].value == "{" &&
			(index == 0 || !javaTypeDeclarationKeyword(tokens[index-1].value)) {
			return true
		}
	}
	return false
}

func javaCommitMemberSuffixCandidate(
	definitions []sourceDefinition,
	staged map[int][]sourceDefinition,
	candidate []sourceDefinition,
	owner, bodyOpen int,
	strong bool,
	recoveredOwner int,
) ([]sourceDefinition, int) {
	if owner == bodyOpen || owner == recoveredOwner {
		definitions = append(definitions, candidate...)
		return definitions, recoveredOwner
	}
	if strong {
		definitions = append(definitions, staged[owner]...)
		definitions = append(definitions, candidate...)
		delete(staged, owner)
		return definitions, owner
	}
	staged[owner] = append(staged[owner], candidate...)
	return definitions, recoveredOwner
}
