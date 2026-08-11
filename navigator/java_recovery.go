package navigator

import (
	"sort"
	"strings"
)

const javaMaximumRecoveryHeaderTokens = 4096

type javaDelimiterAnalysis struct {
	pairs            []int
	braceOwner       []int
	arrayInitializer []bool
}

type javaTypeBody struct {
	name string
	kind string
}

type javaLexicalAnalysis struct {
	definitions              []sourceDefinition
	scopes                   []javaLineScope
	imports                  []javaLineSpan
	authoritativeDefinitions []sourceDefinition
	invalidDefinitionHeader  map[int]struct{}
	invalidImportHeaders     []javaLineSpan
}

func analyzeJavaLexically(
	source string,
	lineCount int,
	lexed javaLexResult,
) javaLexicalAnalysis {
	if lineCount < 1 || len(lexed.tokens) == 0 {
		return javaLexicalAnalysis{}
	}
	return analyzeJavaLexicallyWithPositionsMode(
		source, lineCount, lexed,
		javaSourcePositions{source: source, lineStarts: javaLineStarts(source)},
		true,
	)
}

func analyzeJavaLexicallyWithPositionsMode(
	source string,
	lineCount int,
	lexed javaLexResult,
	positions javaSourcePositions,
	recoverMemberSuffix bool,
) javaLexicalAnalysis {
	if lineCount < 1 || len(lexed.tokens) == 0 {
		return javaLexicalAnalysis{}
	}
	if positions.source != source || len(positions.lineStarts) == 0 {
		positions = javaSourcePositions{
			source: source, lineStarts: javaLineStarts(source),
		}
	}
	tokens := lexed.tokens
	delimiters := analyzeJavaDelimiters(tokens)
	typeBodies := make(map[int]javaTypeBody)
	moduleBodies := make(map[int]struct{})
	definitions := make([]sourceDefinition, 0)
	invalidDefinitionHeaders := make(map[int]struct{})
	invalidImportHeaders := make([]javaLineSpan, 0)

	appendDefinition := func(
		name javaToken,
		startOffset, endOffset int,
		ownsScope, exactEnd bool,
	) {
		if !javaTokenIsSourceName(name) || name.start < 0 || name.end > len(source) {
			return
		}
		line, column := positions.lineColumn(name.start)
		scopeStart, scopeEnd := line, line
		ownedEndColumn := 0
		if ownsScope {
			startOffset = javaLexicalAttachedStart(lexed.input, lexed.commentSpans, startOffset)
			scopeStart, scopeEnd = positions.lineSpan(
				max(0, min(startOffset, len(source))),
				max(0, min(endOffset, len(source))),
			)
			ownedEndColumn = javaExactOwnedEndColumn(
				positions, scopeEnd, endOffset, exactEnd,
			)
		}
		definition := normalizeJavaTreeDefinition(sourceDefinition{
			symbol:         name.text,
			line:           line,
			column:         column,
			scopeStart:     scopeStart,
			scopeEnd:       scopeEnd,
			ownedEndColumn: ownedEndColumn,
			ownsScope:      ownsScope,
		}, lineCount)
		if definition.symbol != "" {
			definitions = append(definitions, definition)
		}
	}

	for index := range tokens {
		keyword := tokens[index].value
		if !javaTypeDeclarationKeyword(keyword) ||
			keyword == "class" && index > 0 && tokens[index-1].value == "." &&
				!javaClassDeclarationRestartsIncompleteTopLevelHeader(
					lexed.input, tokens, delimiters, index,
				) {
			continue
		}
		nameIndex := index + 1
		if nameIndex >= len(tokens) || !javaTokenIsSourceName(tokens[nameIndex]) {
			continue
		}
		bodyOpen := javaDeclarationBody(tokens, delimiters, nameIndex+1)
		if bodyOpen < 0 {
			continue
		}
		bodyEnd, exactEnd := javaBraceEndOffsetExact(
			source, lexed.input, tokens, delimiters, bodyOpen,
		)
		declarationStart := index
		if keyword == "interface" && index > 0 && tokens[index-1].value == "@" {
			declarationStart = index - 1
		}
		startIndex := javaDeclarationPrefixStart(tokens, delimiters, declarationStart)
		startOffset := tokens[startIndex].start
		headerInvalid := javaDeclarationHeaderHasIllegalOpaque(
			tokens, delimiters, startIndex, bodyOpen,
			javaByteSpan{
				start: javaDeclarationSegmentStartOffset(tokens, delimiters, startIndex),
				end:   tokens[bodyOpen].start,
			},
			lexed.stringSpans, nil, true,
		)
		if headerInvalid {
			invalidDefinitionHeaders[tokens[nameIndex].start] = struct{}{}
		}
		if keyword == "record" {
			javaAppendLexicalRecordComponents(
				tokens, delimiters, positions, nameIndex, bodyOpen, lineCount,
				&definitions, invalidDefinitionHeaders, !headerInvalid,
			)
		}
		if headerInvalid {
			continue
		}
		typeBodies[bodyOpen] = javaTypeBody{name: tokens[nameIndex].value, kind: keyword}
		appendDefinition(tokens[nameIndex], startOffset, bodyEnd, true, exactEnd)
	}
	javaRegisterLexicalAnonymousBodies(tokens, delimiters, typeBodies)
	enumConstantBodies := javaRegisterLexicalEnumConstantBodies(
		tokens, delimiters, typeBodies,
	)

	moduleRestart := false
	for index := range tokens {
		if index < len(delimiters.braceOwner) && delimiters.braceOwner[index] < 0 &&
			(tokens[index].value == ";" || tokens[index].value == "{") {
			moduleRestart = false
		}
		startIndex, nameStart, nameEnd, bodyOpen, candidate, ok := javaLexicalModuleHeader(
			tokens, delimiters, index, moduleRestart,
		)
		if candidate {
			moduleRestart = true
		}
		if !ok {
			continue
		}
		if javaModuleHeaderHasIllegalOpaque(
			tokens, delimiters, startIndex, index, bodyOpen, lexed.stringSpans,
		) {
			continue
		}
		moduleBodies[bodyOpen] = struct{}{}
		nameParts := make([]string, 0, (nameEnd-nameStart)/2+1)
		for cursor := nameStart; cursor <= nameEnd; cursor += 2 {
			nameParts = append(nameParts, tokens[cursor].text)
		}
		name := javaToken{
			text:       strings.Join(nameParts, "."),
			value:      strings.Join(nameParts, "."),
			start:      tokens[nameStart].start,
			end:        tokens[nameEnd].end,
			identifier: true,
		}
		bodyEnd, exactEnd := javaBraceEndOffsetExact(
			source, lexed.input, tokens, delimiters, bodyOpen,
		)
		line, column := positions.lineColumn(name.start)
		scopeStart, scopeEnd := positions.lineSpan(
			javaLexicalAttachedStart(lexed.input, lexed.commentSpans, tokens[startIndex].start),
			bodyEnd,
		)
		ownedEndColumn := javaExactOwnedEndColumn(
			positions, scopeEnd, bodyEnd, exactEnd,
		)
		definitions = append(definitions, normalizeJavaTreeDefinition(sourceDefinition{
			symbol:         name.text,
			line:           line,
			column:         column,
			scopeStart:     scopeStart,
			scopeEnd:       scopeEnd,
			ownedEndColumn: ownedEndColumn,
			ownsScope:      true,
		}, lineCount))
	}
	javaAppendLexicalCompactCallables(
		source, lineCount, lexed.commentSpans, lexed.input, tokens, delimiters, positions,
		lexed.stringSpans, &definitions, invalidDefinitionHeaders,
	)
	javaAppendLexicalCompactFields(
		lineCount, lexed.commentSpans, lexed.input, tokens, delimiters, positions,
		lexed.stringSpans, &definitions, invalidDefinitionHeaders,
	)

	directMembers := javaIndexDirectTypeMembers(tokens, delimiters, typeBodies)
	for bodyOpen, body := range typeBodies {
		bodyClose := javaDelimiterMatch(delimiters, bodyOpen)
		if bodyClose < 0 {
			bodyClose = len(tokens)
		}
		javaAppendLexicalCallables(
			source, lineCount, lexed.commentSpans, lexed.input, tokens, delimiters, positions,
			bodyOpen, bodyClose, body, directMembers[bodyOpen], lexed.stringSpans,
			&definitions, invalidDefinitionHeaders,
		)
		javaAppendLexicalFields(
			lineCount, lexed.commentSpans, lexed.input, tokens, delimiters, positions,
			bodyOpen, bodyClose, body, directMembers[bodyOpen], lexed.stringSpans,
			&definitions, invalidDefinitionHeaders,
		)
		if body.kind == "enum" {
			javaAppendLexicalEnumConstants(
				source, lineCount, lexed.commentSpans, lexed.input,
				tokens, delimiters, positions,
				bodyOpen, bodyClose, directMembers[bodyOpen], lexed.stringSpans,
				&definitions, invalidDefinitionHeaders,
			)
		}
	}

	definitions = sortUniqueJavaTreeDefinitions(definitions)
	scopes := javaLexicalScopes(
		source, lineCount, lexed.commentSpans, lexed.input, tokens, delimiters,
		enumConstantBodies, positions,
	)
	for _, definition := range definitions {
		if definition.ownsScope && definition.scopeStart >= 1 &&
			definition.scopeEnd >= definition.scopeStart && definition.scopeEnd <= lineCount {
			scopes = append(scopes, javaLineScope{
				start: definition.scopeStart,
				end:   definition.scopeEnd,
			})
		}
	}

	imports := javaLexicalImports(
		lineCount, lexed.commentSpans, lexed.input, tokens, delimiters, positions,
		moduleBodies, lexed.stringSpans, &invalidImportHeaders,
	)
	authoritativeDefinitions := []sourceDefinition(nil)
	if recoverMemberSuffix && !lexed.truncated {
		var invalidMemberSuffixHeaders map[int]struct{}
		authoritativeDefinitions, invalidMemberSuffixHeaders = javaRecoverMemberSuffixes(
			source, lineCount, lexed, positions, delimiters, typeBodies,
		)
		for offset := range invalidMemberSuffixHeaders {
			invalidDefinitionHeaders[offset] = struct{}{}
		}
	}
	return javaLexicalAnalysis{
		definitions:              definitions,
		scopes:                   normalizeJavaLineScopes(scopes, lineCount),
		imports:                  imports,
		authoritativeDefinitions: authoritativeDefinitions,
		invalidDefinitionHeader:  invalidDefinitionHeaders,
		invalidImportHeaders:     normalizeJavaLineSpans(invalidImportHeaders, lineCount),
	}
}

func analyzeJavaDelimiters(tokens []javaToken) javaDelimiterAnalysis {
	analysis := javaDelimiterAnalysis{
		pairs:            make([]int, len(tokens)),
		braceOwner:       make([]int, len(tokens)),
		arrayInitializer: make([]bool, len(tokens)),
	}
	for index := range analysis.pairs {
		analysis.pairs[index] = -1
		analysis.braceOwner[index] = -1
	}
	type opener struct {
		index        int
		previous     int
		previousKind int
		kind         byte
	}
	stack := make([]opener, 0, 32)
	braceStack := make([]int, 0, 16)
	top := -1
	tops := [3]int{-1, -1, -1}
	delimiterKind := func(value string) int {
		switch value {
		case "(", ")":
			return 0
		case "[", "]":
			return 1
		case "{", "}":
			return 2
		default:
			return -1
		}
	}
	for index, token := range tokens {
		if token.gap {
			top = -1
			tops = [3]int{-1, -1, -1}
			braceStack = braceStack[:0]
			continue
		}
		if len(braceStack) > 0 {
			analysis.braceOwner[index] = braceStack[len(braceStack)-1]
		}
		switch token.value {
		case "(", "[", "{":
			kind := delimiterKind(token.value)
			stack = append(stack, opener{
				index: index, previous: top, previousKind: tops[kind], kind: byte(kind),
			})
			top = len(stack) - 1
			tops[kind] = top
			if token.value == "{" {
				braceStack = append(braceStack, index)
			}
		case ")", "]", "}":
			kind := delimiterKind(token.value)
			match := tops[kind]
			if match >= 0 {
				openIndex := stack[match].index
				analysis.pairs[openIndex], analysis.pairs[index] = index, openIndex
				for top >= match {
					popped := stack[top]
					tops[popped.kind] = popped.previousKind
					top = popped.previous
				}
				if token.value == "}" {
					for len(braceStack) > 0 {
						last := braceStack[len(braceStack)-1]
						braceStack = braceStack[:len(braceStack)-1]
						if last == openIndex {
							break
						}
					}
				}
			}
		}
	}
	for index := range tokens {
		if tokens[index].value != "{" {
			continue
		}
		previous := index - 1
		if previous < 0 {
			continue
		}
		switch tokens[previous].value {
		case "=", "default":
			analysis.arrayInitializer[index] = true
		case "(":
			analysis.arrayInitializer[index] = javaAnnotationArgumentOpen(tokens, previous)
		case "]":
			analysis.arrayInitializer[index] = !javaBraceFollowsCallableDims(
				tokens, analysis, index,
			)
		case "{", ",":
			owner := analysis.braceOwner[index]
			analysis.arrayInitializer[index] = owner >= 0 &&
				owner < len(analysis.arrayInitializer) && analysis.arrayInitializer[owner]
		}
	}
	return analysis
}

func javaAnnotationArgumentOpen(tokens []javaToken, open int) bool {
	cursor := open - 1
	if cursor < 0 || !javaTokenIsSourceName(tokens[cursor]) {
		return false
	}
	for cursor >= 2 && tokens[cursor-1].value == "." &&
		javaTokenIsSourceName(tokens[cursor-2]) {
		cursor -= 2
	}
	return cursor > 0 && tokens[cursor-1].value == "@"
}

func javaBraceFollowsCallableDims(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	brace int,
) bool {
	cursor := brace - 1
	sawDims := false
	for cursor >= 0 && tokens[cursor].value == "]" {
		openIndex := javaDelimiterMatch(delimiters, cursor)
		if openIndex < 0 || openIndex+1 != cursor {
			return false
		}
		sawDims = true
		cursor = openIndex - 1
		for {
			annotationStart, annotation := javaAnnotationStartBefore(
				tokens, delimiters, cursor,
			)
			if !annotation {
				break
			}
			cursor = annotationStart - 1
		}
	}
	if !sawDims || cursor < 0 || tokens[cursor].value != ")" {
		return false
	}
	parameters := javaDelimiterMatch(delimiters, cursor)
	nameIndex := parameters - 1
	return parameters >= 0 && parameters < cursor && nameIndex >= 0 &&
		javaTokenIsSourceName(tokens[nameIndex]) &&
		(nameIndex == 0 || tokens[nameIndex-1].value != "@" &&
			tokens[nameIndex-1].value != ".")
}

func javaAnnotationStartBefore(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	end int,
) (int, bool) {
	if end < 0 || end >= len(tokens) {
		return 0, false
	}
	cursor := end
	if tokens[cursor].value == ")" {
		openIndex := javaDelimiterMatch(delimiters, cursor)
		if openIndex <= 0 || openIndex >= cursor {
			return 0, false
		}
		cursor = openIndex - 1
	}
	if !javaTokenIsSourceName(tokens[cursor]) {
		return 0, false
	}
	for cursor >= 2 && tokens[cursor-1].value == "." &&
		javaTokenIsSourceName(tokens[cursor-2]) {
		cursor -= 2
	}
	if cursor <= 0 || tokens[cursor-1].value != "@" {
		return 0, false
	}
	return cursor - 1, true
}

func javaIndexDirectTypeMembers(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	typeBodies map[int]javaTypeBody,
) map[int][]int {
	members := make(map[int][]int, len(typeBodies))
	for index := range tokens {
		if index >= len(delimiters.braceOwner) {
			break
		}
		owner := delimiters.braceOwner[index]
		if _, ok := typeBodies[owner]; ok {
			members[owner] = append(members[owner], index)
		}
	}
	return members
}

func javaRegisterLexicalAnonymousBodies(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	typeBodies map[int]javaTypeBody,
) {
	for index := range tokens {
		if tokens[index].value != "new" || index >= len(delimiters.braceOwner) {
			continue
		}
		owner := delimiters.braceOwner[index]
		limit := min(len(tokens), index+javaMaximumRecoveryHeaderTokens)
		for cursor := index + 1; cursor < limit; cursor++ {
			if next, annotation := javaAnnotationEnd(tokens, delimiters, cursor, limit); annotation {
				cursor = next - 1
				continue
			}
			switch tokens[cursor].value {
			case "new":
				// A later allocation starts a fresh recovery candidate. The last
				// candidate still sees its constructor body, while malformed runs
				// no longer rescan the same bounded suffix for every `new`.
				cursor = limit
			case "(":
				closeIndex := javaDelimiterMatch(delimiters, cursor)
				if closeIndex <= cursor || closeIndex+1 >= len(tokens) {
					cursor = limit
					continue
				}
				bodyOpen := closeIndex + 1
				if tokens[bodyOpen].value == "{" &&
					delimiters.braceOwner[bodyOpen] == owner &&
					!delimiters.arrayInitializer[bodyOpen] {
					typeBodies[bodyOpen] = javaTypeBody{kind: "anonymous"}
				}
				cursor = limit
			case "[", "{", ";", "=", "->":
				cursor = limit
			}
		}
	}
}

func javaRegisterLexicalEnumConstantBodies(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	typeBodies map[int]javaTypeBody,
) map[int]struct{} {
	constantBodies := make(map[int]struct{})
	enumBodies := make([]int, 0)
	for bodyOpen, body := range typeBodies {
		if body.kind == "enum" {
			enumBodies = append(enumBodies, bodyOpen)
		}
	}
	for _, bodyOpen := range enumBodies {
		bodyClose := javaDelimiterMatch(delimiters, bodyOpen)
		if bodyClose < 0 {
			bodyClose = len(tokens)
		}
		cursor := bodyOpen + 1
		for cursor < bodyClose {
			for cursor < bodyClose {
				if next, annotation := javaAnnotationEnd(
					tokens, delimiters, cursor, bodyClose,
				); annotation {
					cursor = next
					continue
				}
				break
			}
			if cursor >= bodyClose || tokens[cursor].value == ";" {
				break
			}
			if !javaTokenIsSourceName(tokens[cursor]) {
				cursor++
				continue
			}
			cursor++
			if cursor < bodyClose && tokens[cursor].value == "(" {
				closeIndex := javaDelimiterMatch(delimiters, cursor)
				if closeIndex <= cursor || closeIndex >= bodyClose {
					break
				}
				cursor = closeIndex + 1
			}
			if cursor < bodyClose && tokens[cursor].value == "{" &&
				delimiters.braceOwner[cursor] == bodyOpen &&
				!delimiters.arrayInitializer[cursor] {
				typeBodies[cursor] = javaTypeBody{kind: "anonymous"}
				constantBodies[cursor] = struct{}{}
				if closeIndex := javaDelimiterMatch(delimiters, cursor); closeIndex > cursor {
					cursor = closeIndex + 1
				} else {
					break
				}
			}
			for cursor < bodyClose {
				switch tokens[cursor].value {
				case "(", "[", "{":
					if closeIndex := javaDelimiterMatch(delimiters, cursor); closeIndex > cursor {
						cursor = closeIndex + 1
						continue
					}
				case ",":
					cursor++
					goto nextConstant
				case ";":
					cursor = bodyClose
					goto nextConstant
				}
				cursor++
			}
		nextConstant:
		}
	}
	return constantBodies
}

func javaAppendLexicalCompactCallables(
	source string,
	lineCount int,
	comments []javaByteSpan,
	input *javaUnicodeInput,
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	positions javaSourcePositions,
	opaqueSpans []javaByteSpan,
	definitions *[]sourceDefinition,
	invalidDefinitionHeaders map[int]struct{},
) {
	directMembers := javaTopLevelRecoveryMembers(tokens, delimiters)
	javaAppendLexicalCallables(
		source, lineCount, comments, input, tokens, delimiters, positions,
		-1, len(tokens), javaTypeBody{kind: "compact"}, directMembers, opaqueSpans,
		definitions, invalidDefinitionHeaders,
	)
}

func javaAppendLexicalCompactFields(
	lineCount int,
	comments []javaByteSpan,
	input *javaUnicodeInput,
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	positions javaSourcePositions,
	opaqueSpans []javaByteSpan,
	definitions *[]sourceDefinition,
	invalidDefinitionHeaders map[int]struct{},
) {
	javaAppendLexicalFields(
		lineCount, comments, input, tokens, delimiters, positions,
		-1, len(tokens), javaTypeBody{kind: "compact"},
		javaTopLevelRecoveryMembers(tokens, delimiters), opaqueSpans,
		definitions, invalidDefinitionHeaders,
	)
}

func javaTopLevelRecoveryMembers(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
) []int {
	directMembers := make([]int, 0)
	for index := range tokens {
		if index < len(delimiters.braceOwner) && delimiters.braceOwner[index] < 0 {
			directMembers = append(directMembers, index)
		}
	}
	return directMembers
}

func javaDelimiterMatch(delimiters javaDelimiterAnalysis, index int) int {
	if index < 0 || index >= len(delimiters.pairs) {
		return -1
	}
	return delimiters.pairs[index]
}

func javaDeclarationBody(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	start int,
) int {
	limit := min(len(tokens), start+javaMaximumRecoveryHeaderTokens)
	for index := start; index < limit; index++ {
		switch tokens[index].value {
		case "(", "[":
			if closeIndex := javaDelimiterMatch(delimiters, index); closeIndex > index {
				index = closeIndex
			}
		case "{":
			return index
		case ";", "=", "->":
			return -1
		}
		if javaTypeDeclarationKeyword(tokens[index].value) &&
			(index == 0 || tokens[index-1].value != ".") {
			// A new declaration header cannot belong to the preceding malformed
			// one. Stopping here keeps repeated incomplete headers linear rather
			// than rescanning the bounded lookahead for every keyword.
			return -1
		}
	}
	return -1
}

func javaTypeDeclarationKeyword(value string) bool {
	switch value {
	case "class", "interface", "enum", "record":
		return true
	default:
		return false
	}
}

// javaClassDeclarationRestartsIncompleteTopLevelHeader distinguishes a class
// declaration on the line after a malformed package/import name from the
// `class` token in an ordinary class literal. Tokens normally make `.` and
// `class` look identical in both cases. A logical line break after the trailing
// header dot plus a top-level run beginning at package/import is the narrow
// synchronization evidence; legal `Type\n.class` and `Type.\nclass` expressions
// do not satisfy both conditions.
func javaClassDeclarationRestartsIncompleteTopLevelHeader(
	input *javaUnicodeInput,
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	classIndex int,
) bool {
	if input == nil || classIndex <= 0 || classIndex >= len(tokens) ||
		tokens[classIndex].value != "class" || tokens[classIndex-1].value != "." ||
		classIndex >= len(delimiters.braceOwner) ||
		delimiters.braceOwner[classIndex] >= 0 {
		return false
	}
	dot := tokens[classIndex-1]
	whitespace, lineBreaks := javaTranslatedWhitespaceGap(
		input, dot.end, tokens[classIndex].start,
	)
	if !whitespace || lineBreaks == 0 {
		return false
	}
	start := javaDeclarationPrefixStart(tokens, delimiters, classIndex)
	if start < 0 || start >= classIndex {
		return false
	}
	return tokens[start].value == "package" || tokens[start].value == "import"
}

func javaDeclarationPrefixStart(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	index int,
) int {
	start := index
	minimum := max(0, index-javaMaximumRecoveryHeaderTokens)
	owner := -1
	if index < len(delimiters.braceOwner) {
		owner = delimiters.braceOwner[index]
	}
	if owner >= 0 {
		// No declaration prefix can precede the brace that owns it. Besides
		// being more precise on malformed input, this keeps deeply nested
		// scopes and types from repeatedly walking through their ancestors.
		minimum = max(minimum, owner+1)
	}
	for cursor := index - 1; cursor >= minimum; cursor-- {
		if cursor < len(delimiters.braceOwner) && delimiters.braceOwner[cursor] != owner {
			continue
		}
		switch tokens[cursor].value {
		case ";", "}":
			return start
		case "{":
			if cursor >= len(delimiters.arrayInitializer) ||
				!delimiters.arrayInitializer[cursor] {
				return start
			}
		}
		start = cursor
	}
	return start
}

func javaLexicalModuleHeader(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	moduleIndex int,
	restart bool,
) (start, nameStart, nameEnd, bodyOpen int, candidate, ok bool) {
	if moduleIndex < 0 || moduleIndex >= len(tokens) || tokens[moduleIndex].value != "module" ||
		moduleIndex >= len(delimiters.braceOwner) || delimiters.braceOwner[moduleIndex] >= 0 {
		return 0, 0, 0, 0, false, false
	}
	if restart {
		start = moduleIndex
		if moduleIndex > 0 && tokens[moduleIndex-1].value == "open" &&
			delimiters.braceOwner[moduleIndex-1] < 0 {
			start = moduleIndex - 1
		}
	} else {
		start = javaDeclarationPrefixStart(tokens, delimiters, moduleIndex)
		prefix := moduleIndex
		if prefix > 0 && tokens[prefix-1].value == "open" {
			prefix--
		}
		for prefix > 0 {
			annotationStart, annotation := javaAnnotationStartBefore(
				tokens, delimiters, prefix-1,
			)
			if !annotation {
				break
			}
			prefix = annotationStart
		}
		start = min(start, prefix)
	}
	cursor := start
	for cursor < moduleIndex {
		if next, annotation := javaAnnotationEnd(tokens, delimiters, cursor, moduleIndex); annotation {
			cursor = next
			continue
		}
		break
	}
	if cursor < moduleIndex && tokens[cursor].value == "open" {
		cursor++
	}
	if cursor != moduleIndex {
		return 0, 0, 0, 0, false, false
	}

	nameStart = moduleIndex + 1
	if nameStart >= len(tokens) || !javaTokenIsSourceName(tokens[nameStart]) {
		return 0, 0, 0, 0, false, false
	}
	nameEnd = nameStart
	for nameEnd+2 < len(tokens) && tokens[nameEnd+1].value == "." &&
		javaTokenIsSourceName(tokens[nameEnd+2]) {
		nameEnd += 2
	}
	bodyOpen = nameEnd + 1
	if bodyOpen >= len(tokens) || tokens[bodyOpen].value != "{" ||
		bodyOpen >= len(delimiters.braceOwner) || delimiters.braceOwner[bodyOpen] >= 0 {
		return 0, 0, 0, 0, true, false
	}
	return start, nameStart, nameEnd, bodyOpen, true, true
}

// javaModuleHeaderHasIllegalOpaque reports literals that disappeared from the
// token stream and would otherwise make a malformed module header look
// contiguous. Literal values are legal inside a recognized annotation
// argument list; everywhere else from the first header token through the body
// brace they invalidate the recovered declaration. Comments remain ordinary
// Java trivia and are intentionally not part of opaqueSpans.
func javaModuleHeaderHasIllegalOpaque(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	start, moduleIndex, bodyOpen int,
	opaqueSpans []javaByteSpan,
) bool {
	if start < 0 || start >= len(tokens) || moduleIndex < start ||
		moduleIndex >= len(tokens) || bodyOpen <= moduleIndex ||
		bodyOpen >= len(tokens) {
		return true
	}
	return javaDeclarationHeaderHasIllegalOpaque(
		tokens, delimiters, start, bodyOpen,
		javaByteSpan{
			start: javaDeclarationSegmentStartOffset(tokens, delimiters, start),
			end:   tokens[bodyOpen].start,
		},
		opaqueSpans, nil, true,
	)
}

// javaDeclarationHeaderHasIllegalOpaque reports literal fragments that the
// retained token stream omitted from one candidate declaration header. Only
// spans intersecting the bounded header are inspected. Annotation argument
// lists and caller-supplied expression ranges are the only places where an
// opaque literal can legally occur without separating declaration syntax.
func javaDeclarationHeaderHasIllegalOpaque(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	tokenStart, tokenEnd int,
	header javaByteSpan,
	opaqueSpans []javaByteSpan,
	extraAllowed []javaByteSpan,
	allowAnnotations bool,
) bool {
	if tokenStart < 0 || tokenStart > tokenEnd || tokenEnd > len(tokens) ||
		header.start < 0 || header.end < header.start {
		return true
	}
	firstOpaque := sort.Search(len(opaqueSpans), func(index int) bool {
		return opaqueSpans[index].end > header.start
	})
	if firstOpaque >= len(opaqueSpans) || opaqueSpans[firstOpaque].start >= header.end {
		return false
	}

	allowed := make([]javaByteSpan, 0, len(extraAllowed)+2)
	allowed = append(allowed, extraAllowed...)
	if allowAnnotations {
		for cursor := tokenStart; cursor < tokenEnd; cursor++ {
			argument, ok := javaAnnotationArgumentSpan(
				tokens, delimiters, cursor, tokenEnd,
			)
			if !ok {
				continue
			}
			allowed = append(allowed, argument)
			if next, annotation := javaAnnotationEnd(
				tokens, delimiters, cursor, tokenEnd,
			); annotation && next > cursor {
				cursor = next - 1
			}
		}
	}
	allowed = normalizeJavaSpans(allowed)

	allowedIndex := 0
	for opaqueIndex := firstOpaque; opaqueIndex < len(opaqueSpans); opaqueIndex++ {
		opaque := opaqueSpans[opaqueIndex]
		if opaque.start >= header.end {
			break
		}
		for allowedIndex < len(allowed) && allowed[allowedIndex].end <= opaque.start {
			allowedIndex++
		}
		if allowedIndex < len(allowed) && allowed[allowedIndex].start <= opaque.start &&
			opaque.end <= allowed[allowedIndex].end {
			continue
		}
		return true
	}
	return false
}

// javaDeclarationSegmentStartOffset includes an opaque fragment immediately
// after a real declaration boundary even when no retained token represents
// that fragment. Synthetic recovery restarts inside a malformed token run use
// their first token as the new boundary.
func javaDeclarationSegmentStartOffset(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	start int,
) int {
	if start <= 0 {
		return 0
	}
	if start >= len(tokens) {
		return tokens[len(tokens)-1].end
	}
	owner := -1
	if start < len(delimiters.braceOwner) {
		owner = delimiters.braceOwner[start]
	}
	previous := start - 1
	if owner >= 0 && previous == owner {
		return tokens[previous].end
	}
	if previous < len(delimiters.braceOwner) &&
		delimiters.braceOwner[previous] == owner {
		switch tokens[previous].value {
		case ";", "}":
			return tokens[previous].end
		case "{":
			if previous >= len(delimiters.arrayInitializer) ||
				!delimiters.arrayInitializer[previous] {
				return tokens[previous].end
			}
		}
	}
	return tokens[start].start
}

func javaAnnotationArgumentSpan(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	start, limit int,
) (javaByteSpan, bool) {
	if start < 0 || start >= limit || limit > len(tokens) || tokens[start].value != "@" {
		return javaByteSpan{}, false
	}
	cursor := start + 1
	if cursor >= limit || !javaTokenIsSourceName(tokens[cursor]) {
		return javaByteSpan{}, false
	}
	cursor++
	for cursor+1 < limit && tokens[cursor].value == "." &&
		javaTokenIsSourceName(tokens[cursor+1]) {
		cursor += 2
	}
	if cursor >= limit || tokens[cursor].value != "(" {
		return javaByteSpan{}, false
	}
	closeIndex := javaDelimiterMatch(delimiters, cursor)
	if closeIndex <= cursor || closeIndex >= limit {
		return javaByteSpan{}, false
	}
	return javaByteSpan{
		start: tokens[cursor].end,
		end:   tokens[closeIndex].start,
	}, true
}

// javaAnnotationEnd returns the first token after an annotation that begins at
// start. Annotation arguments are skipped as one delimiter-bounded group so
// their calls and assignments cannot be mistaken for members.
func javaAnnotationEnd(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	start, limit int,
) (int, bool) {
	if start < 0 || start >= limit || start >= len(tokens) || tokens[start].value != "@" {
		return start, false
	}
	cursor := start + 1
	if cursor >= limit || cursor >= len(tokens) || !javaTokenIsSourceName(tokens[cursor]) {
		return start, false
	}
	cursor++
	for cursor+1 < limit && cursor+1 < len(tokens) && tokens[cursor].value == "." &&
		javaTokenIsSourceName(tokens[cursor+1]) {
		cursor += 2
	}
	if cursor < limit && cursor < len(tokens) && tokens[cursor].value == "(" {
		closeIndex := javaDelimiterMatch(delimiters, cursor)
		if closeIndex <= cursor || closeIndex >= limit {
			return limit, true
		}
		cursor = closeIndex + 1
	}
	return cursor, true
}

func javaBraceEndOffset(
	source string,
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	open int,
) int {
	if closeIndex := javaDelimiterMatch(delimiters, open); closeIndex > open &&
		closeIndex < len(tokens) {
		return tokens[closeIndex].end
	}
	return len(source)
}

func javaBraceEndOffsetExact(
	source string,
	input *javaUnicodeInput,
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	open int,
) (int, bool) {
	closeIndex := javaDelimiterMatch(delimiters, open)
	if closeIndex <= open || closeIndex >= len(tokens) {
		return len(source), false
	}
	closing := tokens[closeIndex]
	return closing.end, javaTokenIsExactPunctuation(input, closing, '}')
}

func javaTokenIsExactPunctuation(
	input *javaUnicodeInput,
	token javaToken,
	want rune,
) bool {
	if input == nil || token.start < 0 || token.start >= token.end ||
		token.end > len(input.source) {
		return false
	}
	cursor := input.cursor(token.start, token.end)
	unit, ok := cursor.next()
	if !ok || unit.start != token.start || unit.end != token.end || unit.value != want {
		return false
	}
	_, extra := cursor.next()
	return !extra
}

func javaExactOwnedEndColumn(
	positions javaSourcePositions,
	scopeEnd, endOffset int,
	exact bool,
) int {
	if !exact {
		return 0
	}
	endLine, endColumn := positions.lineColumn(endOffset)
	if endLine != scopeEnd {
		return 0
	}
	return endColumn
}

func javaAppendLexicalRecordComponents(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	positions javaSourcePositions,
	nameIndex, bodyOpen, lineCount int,
	definitions *[]sourceDefinition,
	invalidDefinitionHeaders map[int]struct{},
	headerValid bool,
) {
	parameters := nameIndex + 1
	if parameters < bodyOpen && tokens[parameters].value == "<" {
		closeIndex := javaAngleGroupEnd(tokens, delimiters, parameters, bodyOpen)
		if closeIndex < parameters {
			return
		}
		parameters = closeIndex + 1
	}
	if parameters >= bodyOpen || tokens[parameters].value != "(" {
		return
	}
	closeIndex := javaDelimiterMatch(delimiters, parameters)
	if closeIndex <= parameters || closeIndex > bodyOpen {
		return
	}
	segmentStart := parameters + 1
	angleDepth := 0
	for index := segmentStart; index <= closeIndex; index++ {
		if index < closeIndex {
			switch tokens[index].value {
			case "<":
				angleDepth++
			case ">", ">>", ">>>":
				angleDepth = max(
					0, angleDepth-strings.Count(tokens[index].value, ">"),
				)
			}
		}
		if index < closeIndex && (tokens[index].value != "," || angleDepth != 0) {
			if (tokens[index].value == "(" || tokens[index].value == "[") &&
				javaDelimiterMatch(delimiters, index) > index {
				index = javaDelimiterMatch(delimiters, index)
			}
			continue
		}
		nameIndex := -1
		for cursor := segmentStart; cursor < index; cursor++ {
			if javaTokenIsSourceName(tokens[cursor]) {
				nameIndex = cursor
			}
		}
		if nameIndex >= 0 {
			if !headerValid {
				invalidDefinitionHeaders[tokens[nameIndex].start] = struct{}{}
				segmentStart = index + 1
				continue
			}
			line, column := positions.lineColumn(tokens[nameIndex].start)
			*definitions = append(*definitions, normalizeJavaTreeDefinition(sourceDefinition{
				symbol: tokens[nameIndex].text, line: line, column: column,
				scopeStart: line, scopeEnd: line,
			}, lineCount))
		}
		segmentStart = index + 1
	}
}

func javaAppendLexicalCallables(
	source string,
	lineCount int,
	comments []javaByteSpan,
	input *javaUnicodeInput,
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	positions javaSourcePositions,
	bodyOpen, bodyClose int,
	body javaTypeBody,
	directMembers []int,
	opaqueSpans []javaByteSpan,
	definitions *[]sourceDefinition,
	invalidDefinitionHeaders map[int]struct{},
) {
	enumConstantsEnd := -1
	if body.kind == "enum" {
		enumConstantsEnd = bodyClose
		for _, index := range directMembers {
			if index > bodyOpen && index < bodyClose && index < len(tokens) &&
				tokens[index].value == ";" {
				enumConstantsEnd = index
				break
			}
		}
	}
	recoveryStart := max(0, bodyOpen+1)
	restartAfterParameters := false
	expressionTail := false
	for memberCursor := 0; memberCursor < len(directMembers); memberCursor++ {
		nameIndex := directMembers[memberCursor]
		if nameIndex <= bodyOpen || nameIndex >= bodyClose || nameIndex >= len(tokens) {
			continue
		}
		switch {
		case restartAfterParameters && tokens[nameIndex].value == "default":
			restartAfterParameters = false
			expressionTail = true
		case expressionTail && tokens[nameIndex].value == "void" &&
			(nameIndex == 0 || tokens[nameIndex-1].value != ".") &&
			nameIndex+2 < bodyClose && javaTokenIsSourceName(tokens[nameIndex+1]) &&
			tokens[nameIndex+2].value == "(":
			// `void` cannot occur in an initializer expression except as the
			// `void.class` literal excluded above. It is therefore a safe narrow
			// synchronization point after a missing field semicolon.
			recoveryStart = nameIndex
			restartAfterParameters = false
			expressionTail = false
		case !expressionTail && (restartAfterParameters || recoveryStart < 0):
			recoveryStart = nameIndex
			restartAfterParameters = false
		}
		if annotationEnd, ok := javaAnnotationEnd(
			tokens, delimiters, nameIndex, bodyClose,
		); ok {
			for memberCursor+1 < len(directMembers) &&
				directMembers[memberCursor+1] < annotationEnd {
				memberCursor++
			}
			continue
		}
		switch tokens[nameIndex].value {
		case ";":
			recoveryStart = -1
			restartAfterParameters = false
			expressionTail = false
			continue
		case "=", "->":
			// A callable-shaped token after an initializer expression remains
			// part of that member until its terminating boundary. In particular,
			// a matched invocation followed by an operator must not restart method
			// recovery in the middle of `field = first() + second();`.
			restartAfterParameters = false
			expressionTail = true
			continue
		case "{":
			if !expressionTail && (nameIndex >= len(delimiters.arrayInitializer) ||
				!delimiters.arrayInitializer[nameIndex]) {
				recoveryStart = -1
			}
			continue
		case ")":
			if !expressionTail && javaDelimiterMatch(delimiters, nameIndex) >= 0 {
				restartAfterParameters = true
			}
			continue
		}
		name := tokens[nameIndex]
		if !javaTokenIsSourceName(name) {
			continue
		}
		if nameIndex > 0 && tokens[nameIndex-1].value == "." {
			continue
		}
		open := nameIndex + 1
		if open >= bodyClose || tokens[open].value != "(" {
			if body.kind == "record" && name.value == body.name &&
				open < bodyClose && tokens[open].value == "{" {
				start := javaDeclarationPrefixStart(tokens, delimiters, nameIndex)
				headerInvalid := javaDeclarationHeaderHasIllegalOpaque(
					tokens, delimiters, start, open,
					javaByteSpan{
						start: javaDeclarationSegmentStartOffset(tokens, delimiters, start),
						end:   tokens[open].start,
					},
					opaqueSpans, nil, true,
				)
				if headerInvalid {
					invalidDefinitionHeaders[name.start] = struct{}{}
					continue
				}
				endOffset, exactEnd := javaBraceEndOffsetExact(
					source, input, tokens, delimiters, open,
				)
				javaAppendOwnedLexicalDefinition(
					lineCount, comments, input, positions, name,
					tokens[start].start, endOffset, exactEnd,
					definitions,
				)
			}
			continue
		}
		closeIndex := javaDelimiterMatch(delimiters, open)
		if closeIndex <= open || closeIndex >= bodyClose {
			continue
		}
		start := max(recoveryStart, nameIndex-javaMaximumRecoveryHeaderTokens)
		if start == nameIndex && name.value != body.name {
			continue
		}
		if javaHeaderContains(tokens, delimiters, start, nameIndex, "=", "->", "new") ||
			javaHeaderContainsEarlierParameterList(
				tokens, delimiters, start, nameIndex,
			) ||
			javaHeaderContainsControl(tokens, delimiters, start, nameIndex) {
			continue
		}
		if body.kind == "compact" && !javaCompactCallableDeclarationPrefix(
			tokens, delimiters, start, nameIndex,
		) {
			continue
		}
		endToken, bodyToken := javaCallableEnd(
			tokens, delimiters, closeIndex, bodyClose, bodyOpen,
		)
		if endToken < 0 {
			continue
		}
		// A record declaration header and enum constant argument list are not
		// callable members.
		if nameIndex > 0 && tokens[nameIndex-1].value == "record" ||
			body.kind == "enum" && nameIndex < enumConstantsEnd {
			continue
		}
		extraAllowed := make([]javaByteSpan, 0, 1)
		for cursor := closeIndex + 1; cursor < endToken; cursor++ {
			if delimiters.braceOwner[cursor] == bodyOpen &&
				tokens[cursor].value == "default" {
				extraAllowed = append(extraAllowed, javaByteSpan{
					start: tokens[cursor].end,
					end:   tokens[endToken].start,
				})
				break
			}
		}
		headerInvalid := javaDeclarationHeaderHasIllegalOpaque(
			tokens, delimiters, start, endToken,
			javaByteSpan{
				start: javaDeclarationSegmentStartOffset(tokens, delimiters, start),
				end:   tokens[endToken].start,
			},
			opaqueSpans, extraAllowed, true,
		)
		if headerInvalid {
			invalidDefinitionHeaders[name.start] = struct{}{}
			continue
		}
		endOffset := tokens[endToken].end
		exactEnd := javaTokenIsExactPunctuation(input, tokens[endToken], ';')
		if bodyToken >= 0 {
			endOffset, exactEnd = javaBraceEndOffsetExact(
				source, input, tokens, delimiters, bodyToken,
			)
		}
		javaAppendOwnedLexicalDefinition(
			lineCount, comments, input, positions, name,
			tokens[start].start, endOffset, exactEnd, definitions,
		)
	}
}

func javaCallableEnd(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	start, limit, owner int,
) (endToken, bodyToken int) {
	maximum := min(limit, start+javaMaximumRecoveryHeaderTokens)
	annotationDefault := false
	for index := start + 1; index < maximum; index++ {
		if delimiters.braceOwner[index] != owner {
			continue
		}
		if next, annotation := javaAnnotationEnd(
			tokens, delimiters, index, maximum,
		); annotation {
			index = next - 1
			continue
		}
		if javaTokenIsSourceName(tokens[index]) && !annotationDefault &&
			(index == 0 || tokens[index-1].value != ".") &&
			index+1 < maximum && tokens[index+1].value == "(" &&
			javaDelimiterMatch(delimiters, index+1) > index+1 {
			// A second parameter-list-shaped header restarts malformed member
			// recovery. It cannot be part of a Java method declaration header.
			return -1, -1
		}
		switch tokens[index].value {
		case "default":
			annotationDefault = true
		case "(", "[":
			if closeIndex := javaDelimiterMatch(delimiters, index); closeIndex > index {
				index = closeIndex
			}
		case "{":
			if index < len(delimiters.arrayInitializer) &&
				delimiters.arrayInitializer[index] {
				if closeIndex := javaDelimiterMatch(delimiters, index); closeIndex > index {
					index = closeIndex
					continue
				}
			}
			return index, index
		case ";":
			return index, -1
		case "=", "->":
			return -1, -1
		}
	}
	return -1, -1
}

func javaAppendOwnedLexicalDefinition(
	lineCount int,
	comments []javaByteSpan,
	input *javaUnicodeInput,
	positions javaSourcePositions,
	name javaToken,
	startOffset, endOffset int,
	exactEnd bool,
	definitions *[]sourceDefinition,
) {
	if !javaTokenIsSourceName(name) {
		return
	}
	line, column := positions.lineColumn(name.start)
	scopeStart, scopeEnd := positions.lineSpan(
		javaLexicalAttachedStart(input, comments, startOffset), endOffset,
	)
	ownedEndColumn := javaExactOwnedEndColumn(
		positions, scopeEnd, endOffset, exactEnd,
	)
	*definitions = append(*definitions, normalizeJavaTreeDefinition(sourceDefinition{
		symbol: name.text, line: line, column: column,
		scopeStart: scopeStart, scopeEnd: scopeEnd,
		ownedEndColumn: ownedEndColumn, ownsScope: true,
	}, lineCount))
}

func javaHeaderContains(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	start, end int,
	values ...string,
) bool {
	for index := max(start, 0); index < end && index < len(tokens); index++ {
		if next, annotation := javaAnnotationEnd(tokens, delimiters, index, end); annotation {
			index = next - 1
			continue
		}
		for _, value := range values {
			if tokens[index].value == value {
				return true
			}
		}
	}
	return false
}

func javaHeaderContainsControl(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	start, end int,
) bool {
	for index := max(start, 0); index < end && index < len(tokens); index++ {
		if next, annotation := javaAnnotationEnd(tokens, delimiters, index, end); annotation {
			index = next - 1
			continue
		}
		switch tokens[index].value {
		case "synchronized":
			if index+1 < end && tokens[index+1].value == "(" {
				return true
			}
		case "super":
			if index == 0 || tokens[index-1].value != "?" {
				return true
			}
		case "if", "for", "while", "switch", "catch", "return", "throw",
			"assert", "case", "this":
			return true
		}
	}
	return false
}

func javaHeaderContainsEarlierParameterList(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	start, end int,
) bool {
	for index := max(start, 0); index < end && index < len(tokens); index++ {
		if next, annotation := javaAnnotationEnd(tokens, delimiters, index, end); annotation {
			index = next - 1
			continue
		}
		if tokens[index].value != "(" {
			continue
		}
		if closeIndex := javaDelimiterMatch(delimiters, index); closeIndex > index &&
			closeIndex < end {
			return true
		}
	}
	return false
}

func javaCompactCallableDeclarationPrefix(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	start, nameIndex int,
) bool {
	cursor := max(start, 0)
	for cursor < nameIndex {
		if next, annotation := javaAnnotationEnd(
			tokens, delimiters, cursor, nameIndex,
		); annotation {
			cursor = next
			continue
		}
		switch tokens[cursor].value {
		case "public", "protected", "private", "static", "final", "abstract", "native",
			"synchronized", "strictfp", "default":
			cursor++
			continue
		}
		break
	}
	if cursor < nameIndex && tokens[cursor].value == "<" {
		closeIndex := javaAngleGroupEnd(tokens, delimiters, cursor, nameIndex)
		if closeIndex < 0 {
			return false
		}
		cursor = closeIndex + 1
	}
	if cursor >= nameIndex {
		return false
	}
	seenTypeName := false
	for index := cursor; index < nameIndex; index++ {
		if next, annotation := javaAnnotationEnd(
			tokens, delimiters, index, nameIndex,
		); annotation {
			index = next - 1
			continue
		}
		if tokens[index].value == ":" ||
			(tokens[index].value == "." && index+1 < nameIndex &&
				tokens[index+1].value == "<") {
			return false
		}
		if javaTokenIsSourceName(tokens[index]) || javaPrimitiveOrVoid(tokens[index].value) {
			seenTypeName = true
			continue
		}
		switch tokens[index].value {
		case ".", "<", ">", ">>", ">>>", "[", "]", "?", ",", "extends", "super", "&":
			continue
		default:
			return false
		}
	}
	return seenTypeName
}

func javaPrimitiveOrVoid(value string) bool {
	switch value {
	case "boolean", "byte", "char", "double", "float", "int", "long", "short", "void":
		return true
	default:
		return false
	}
}

func javaAngleGroupEnd(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	start, limit int,
) int {
	depth := 0
	for index := start; index < limit; index++ {
		if next, annotation := javaAnnotationEnd(
			tokens, delimiters, index, limit,
		); annotation {
			index = next - 1
			continue
		}
		switch tokens[index].value {
		case "<":
			depth++
		case ">", ">>", ">>>":
			depth -= strings.Count(tokens[index].value, ">")
			if depth <= 0 {
				return index
			}
		}
	}
	return -1
}

func javaAppendLexicalFields(
	lineCount int,
	comments []javaByteSpan,
	input *javaUnicodeInput,
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	positions javaSourcePositions,
	bodyOpen, bodyClose int,
	body javaTypeBody,
	directMembers []int,
	opaqueSpans []javaByteSpan,
	definitions *[]sourceDefinition,
	invalidDefinitionHeaders map[int]struct{},
) {
	segmentStarts := javaDirectMemberSegmentStarts(
		tokens, delimiters, bodyOpen, bodyClose, directMembers,
	)
	for _, semicolon := range directMembers {
		if semicolon <= bodyOpen || semicolon >= bodyClose || semicolon >= len(tokens) ||
			tokens[semicolon].value != ";" {
			continue
		}
		start, candidate := segmentStarts[semicolon]
		if !candidate {
			continue
		}
		headerEnd := javaFieldHeaderEnd(tokens, delimiters, start, semicolon)
		if start >= semicolon || javaSegmentIsCallable(tokens, delimiters, start, semicolon) ||
			javaHeaderContains(
				tokens, delimiters, start, headerEnd,
				"class", "interface", "enum", "record",
			) {
			continue
		}
		typeArguments := javaAnalyzeExpressionTypeArguments(
			tokens, delimiters, start, semicolon,
		)
		if javaSegmentContainsMalformedAllocation(
			tokens, delimiters, start, semicolon, typeArguments,
		) {
			continue
		}
		if body.kind == "compact" && javaHeaderContains(
			tokens, delimiters, start, semicolon,
			"package", "import", "module",
		) {
			continue
		}
		if body.kind == "enum" && javaSegmentContainsEnumConstants(
			tokens, delimiters, bodyOpen, start, semicolon,
		) {
			continue
		}
		declarators := javaFieldDeclarators(
			tokens, delimiters, start, semicolon, typeArguments,
		)
		if len(declarators) == 0 {
			continue
		}
		startOffset := tokens[start].start
		endOffset := tokens[semicolon].end
		exactEnd := javaTokenIsExactPunctuation(input, tokens[semicolon], ';')
		ownedScopeStart, ownedScopeEnd := 0, 0
		ownedScopeReady := false
		firstHeaderInvalid := false
		for index, declarator := range declarators {
			nameIndex := declarator.name
			name := tokens[nameIndex]
			headerEnd := javaVariableDeclaratorIDEnd(
				tokens, delimiters, nameIndex, semicolon,
			)
			headerInvalid := firstHeaderInvalid ||
				javaDeclarationHeaderHasIllegalOpaque(
					tokens, delimiters, declarator.headerStart, headerEnd,
					javaByteSpan{
						start: javaDeclarationSegmentStartOffset(
							tokens, delimiters, declarator.headerStart,
						),
						end: tokens[headerEnd].start,
					},
					opaqueSpans, nil, true,
				)
			if index == 0 {
				firstHeaderInvalid = headerInvalid
			}
			if headerInvalid {
				invalidDefinitionHeaders[name.start] = struct{}{}
				continue
			}
			line, column := positions.lineColumn(name.start)
			scopeStart, scopeEnd := line, line
			declaratorEnd := semicolon
			if index+1 < len(declarators) {
				declaratorEnd = declarators[index+1].name
			}
			ownsScope := javaSegmentHasScopedInitializer(
				tokens, delimiters, nameIndex, declaratorEnd,
			)
			if ownsScope {
				if !ownedScopeReady {
					ownedScopeStart, ownedScopeEnd = positions.lineSpan(
						javaLexicalAttachedStart(input, comments, startOffset), endOffset,
					)
					ownedScopeReady = true
				}
				scopeStart, scopeEnd = ownedScopeStart, ownedScopeEnd
			}
			ownedEndColumn := 0
			if ownsScope {
				ownedEndColumn = javaExactOwnedEndColumn(
					positions, scopeEnd, endOffset, exactEnd,
				)
			}
			*definitions = append(*definitions, normalizeJavaTreeDefinition(sourceDefinition{
				symbol: name.text, line: line, column: column,
				scopeStart: scopeStart, scopeEnd: scopeEnd,
				ownedEndColumn: ownedEndColumn, ownsScope: ownsScope,
			}, lineCount))
		}
	}
}

func javaSegmentContainsMalformedAllocation(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	start, end int,
	typeArguments javaExpressionTypeArgumentAnalysis,
) bool {
	pendingAllocation := false
	methodReference := false
	for index := max(start, 0); index < end && index < len(tokens); index++ {
		if tokens[index].gap {
			return pendingAllocation
		}
		if next, annotation := javaAnnotationEnd(tokens, delimiters, index, end); annotation {
			index = next - 1
			continue
		}
		if methodReference {
			if tokens[index].value == "<" {
				if closeIndex := typeArguments.close(index); closeIndex > index {
					index = closeIndex
					continue
				}
			}
			if tokens[index].value == "new" || javaTokenIsSourceName(tokens[index]) {
				methodReference = false
				continue
			}
			methodReference = false
		}
		if !pendingAllocation {
			switch tokens[index].value {
			case "::":
				methodReference = true
			case "new":
				pendingAllocation = true
			}
			continue
		}
		switch tokens[index].value {
		case "new", ";", "=", "->", "{":
			return true
		case "<":
			if closeIndex := typeArguments.close(index); closeIndex > index {
				index = closeIndex
			}
		case "(", "[":
			closeIndex := javaDelimiterMatch(delimiters, index)
			if closeIndex <= index || closeIndex >= end {
				return true
			}
			pendingAllocation = false
			index = closeIndex
		}
	}
	return pendingAllocation
}

func javaFieldHeaderEnd(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	start, end int,
) int {
	for index := max(start, 0); index < end && index < len(tokens); index++ {
		if next, annotation := javaAnnotationEnd(tokens, delimiters, index, end); annotation {
			index = next - 1
			continue
		}
		switch tokens[index].value {
		case "(", "[", "{":
			if closeIndex := javaDelimiterMatch(delimiters, index); closeIndex > index &&
				closeIndex < end {
				index = closeIndex
			}
		case "=":
			return index
		}
	}
	return end
}

func javaDirectMemberSegmentStarts(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	bodyOpen, bodyClose int,
	directMembers []int,
) map[int]int {
	starts := make(map[int]int)
	segmentStart := max(0, bodyOpen+1)
	skipThrough := -1
	assignment := false
	for _, index := range directMembers {
		if index <= bodyOpen || index >= bodyClose || index >= len(tokens) {
			continue
		}
		if index <= skipThrough {
			continue
		}
		switch tokens[index].value {
		case "(", "[":
			if closeIndex := javaDelimiterMatch(delimiters, index); closeIndex > index &&
				closeIndex < bodyClose {
				skipThrough = closeIndex
			}
		case "=":
			assignment = true
		case "{":
			closeIndex := javaDelimiterMatch(delimiters, index)
			arrayInitializer := index < len(delimiters.arrayInitializer) &&
				delimiters.arrayInitializer[index]
			if !assignment && !arrayInitializer {
				if closeIndex > index {
					segmentStart = closeIndex + 1
				} else {
					segmentStart = index + 1
				}
				assignment = false
			}
			if closeIndex > index && closeIndex < bodyClose {
				skipThrough = closeIndex
			}
		case ";":
			starts[index] = segmentStart
			segmentStart = index + 1
			assignment = false
		}
	}
	return starts
}

func javaBraceStartsInitializer(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	bodyOpen, brace int,
) bool {
	start := javaDeclarationPrefixStart(tokens, delimiters, brace)
	return (brace >= 0 && brace < len(delimiters.arrayInitializer) &&
		delimiters.arrayInitializer[brace] ||
		javaHeaderContains(tokens, delimiters, start, brace, "=", "->", "new")) &&
		delimiters.braceOwner[brace] == bodyOpen
}

func javaSegmentIsCallable(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	start, end int,
) bool {
	assignment := false
	for index := start; index < end; index++ {
		if next, annotation := javaAnnotationEnd(tokens, delimiters, index, end); annotation {
			index = next - 1
			continue
		}
		if tokens[index].value == "=" {
			assignment = true
		}
		if tokens[index].value == "(" && !assignment {
			closeIndex := javaDelimiterMatch(delimiters, index)
			if closeIndex > index && closeIndex <= end {
				return true
			}
		}
	}
	return false
}

type javaFieldDeclarator struct {
	name        int
	headerStart int
}

func javaFieldDeclaratorNames(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	start, end int,
	typeArguments javaExpressionTypeArgumentAnalysis,
) []int {
	declarators := javaFieldDeclarators(tokens, delimiters, start, end, typeArguments)
	names := make([]int, 0, len(declarators))
	for _, declarator := range declarators {
		names = append(names, declarator.name)
	}
	return names
}

func javaFieldDeclarators(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	start, end int,
	typeArguments javaExpressionTypeArgumentAnalysis,
) []javaFieldDeclarator {
	if start < 0 || start >= end || end > len(tokens) {
		return nil
	}
	first := -1
	angleDepth := 0
	for index := start; index < end; index++ {
		if next, annotation := javaAnnotationEnd(tokens, delimiters, index, end); annotation {
			index = next - 1
			continue
		}
		switch tokens[index].value {
		case "@":
			continue
		case "<":
			angleDepth++
		case ">", ">>", ">>>":
			angleDepth = max(0, angleDepth-strings.Count(tokens[index].value, ">"))
		case "=", ",":
			if angleDepth == 0 {
				index = end
			}
		}
		if index >= end || angleDepth != 0 || !javaTokenIsSourceName(tokens[index]) {
			continue
		}
		next := javaVariableDeclaratorIDEnd(tokens, delimiters, index, end)
		if next == end || tokens[next].value == "=" || tokens[next].value == "," {
			first = index
			break
		}
	}
	if first < 0 || first == start {
		return nil
	}
	declarators := []javaFieldDeclarator{{name: first, headerStart: start}}
	parenDepth, bracketDepth, braceDepth := 0, 0, 0
	pendingDeclarator := false
	pendingHeaderStart := start
	for index := first + 1; index < end; index++ {
		if next, annotation := javaAnnotationEnd(
			tokens, delimiters, index, end,
		); annotation {
			index = next - 1
			continue
		}
		switch tokens[index].value {
		case "(":
			parenDepth++
		case ")":
			parenDepth = max(0, parenDepth-1)
		case "[":
			bracketDepth++
		case "]":
			bracketDepth = max(0, bracketDepth-1)
		case "{":
			braceDepth++
		case "}":
			braceDepth = max(0, braceDepth-1)
		case "<":
			if closeIndex := typeArguments.close(index); closeIndex > index {
				index = closeIndex
			}
		case "=":
			if parenDepth == 0 && bracketDepth == 0 && braceDepth == 0 {
				pendingDeclarator = false
			}
		case ",":
			if parenDepth == 0 && bracketDepth == 0 && braceDepth == 0 {
				pendingDeclarator = true
				pendingHeaderStart = index
			}
		}
		if pendingDeclarator && parenDepth == 0 && bracketDepth == 0 && braceDepth == 0 &&
			javaTokenIsSourceName(tokens[index]) &&
			javaFieldDeclaratorCandidate(tokens, delimiters, index, end) {
			declarators = append(declarators, javaFieldDeclarator{
				name: index, headerStart: pendingHeaderStart,
			})
			pendingDeclarator = false
		}
	}
	return declarators
}

// javaExpressionTypeArgumentAnalysis classifies every expression-level '<' in
// one forward pass. Recovery callers often inspect many relational operators
// in the same malformed field; rescanning the suffix from each one would make
// that recovery quadratic.
type javaExpressionTypeArgumentAnalysis struct {
	closes []int // Encoded as close index + 1; zero means no valid close.
	start  int
}

func (analysis javaExpressionTypeArgumentAnalysis) close(index int) int {
	index -= analysis.start
	if index < 0 || index >= len(analysis.closes) {
		return -1
	}
	return analysis.closes[index] - 1
}

func javaAnalyzeExpressionTypeArguments(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	start, end int,
) javaExpressionTypeArgumentAnalysis {
	start = max(start, 0)
	end = min(end, len(tokens))
	if start >= end {
		return javaExpressionTypeArgumentAnalysis{start: start}
	}
	analysis := javaExpressionTypeArgumentAnalysis{start: start}
	var open []int
	for index := start; index < end; index++ {
		if tokens[index].gap {
			open = open[:0]
			continue
		}
		if next, annotation := javaAnnotationEnd(
			tokens, delimiters, index, end,
		); annotation {
			index = next - 1
			continue
		}
		switch tokens[index].value {
		case "<":
			if analysis.closes == nil {
				analysis.closes = make([]int, end-start)
			}
			open = append(open, index)
		case ">", ">>", ">>>":
			remaining := strings.Count(tokens[index].value, ">")
			for remaining > 0 && len(open) > 0 {
				openIndex := open[len(open)-1]
				open = open[:len(open)-1]
				if javaExpressionTypeArgumentCloseValid(
					tokens, delimiters, openIndex, index, end,
				) {
					analysis.closes[openIndex-start] = index + 1
				}
				remaining--
			}
		case ";", "=", "->":
			open = open[:0]
		}
	}
	return analysis
}

func javaExpressionTypeArgumentCloseValid(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	start, closeIndex, end int,
) bool {
	next := closeIndex + 1
	if next >= end {
		return false
	}
	switch tokens[next].value {
	case "(", ".", "[", "::":
		return true
	}
	if javaExpressionTypeArgumentsFollowInstanceof(tokens, delimiters, start) {
		return true
	}
	if javaTokenIsSourceName(tokens[next]) && next+1 < end &&
		tokens[next+1].value == "(" {
		return true
	}
	return start > 0 && tokens[start-1].value == "::" &&
		(tokens[next].value == "new" || javaTokenIsSourceName(tokens[next]))
}

func javaExpressionTypeArgumentClose(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	start, end int,
) int {
	if start < 0 || start >= end || end > len(tokens) || tokens[start].value != "<" {
		return -1
	}
	methodReference := start > 0 && tokens[start-1].value == "::"
	instanceofType := javaExpressionTypeArgumentsFollowInstanceof(tokens, delimiters, start)
	depth := 0
	for index := start; index < end; index++ {
		if tokens[index].gap {
			return -1
		}
		if next, annotation := javaAnnotationEnd(tokens, delimiters, index, end); annotation {
			index = next - 1
			continue
		}
		switch tokens[index].value {
		case "<":
			depth++
		case ">", ">>", ">>>":
			depth -= strings.Count(tokens[index].value, ">")
			if depth > 0 {
				continue
			}
			next := index + 1
			if next >= end {
				return -1
			}
			switch tokens[next].value {
			case "(", ".", "[", "::":
				return index
			}
			if instanceofType {
				return index
			}
			if javaTokenIsSourceName(tokens[next]) && next+1 < end &&
				tokens[next+1].value == "(" {
				return index
			}
			if methodReference &&
				(tokens[next].value == "new" || javaTokenIsSourceName(tokens[next])) {
				return index
			}
			return -1
		case ";", "=", "->":
			return -1
		}
	}
	return -1
}

func javaExpressionTypeArgumentsFollowInstanceof(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	start int,
) bool {
	cursor := start - 1
	if cursor < 0 || !javaTokenIsSourceName(tokens[cursor]) {
		return false
	}
	for cursor >= 2 && tokens[cursor-1].value == "." &&
		javaTokenIsSourceName(tokens[cursor-2]) {
		cursor -= 2
	}
	for cursor > 0 {
		if tokens[cursor-1].value == "instanceof" {
			return true
		}
		annotationStart, annotation := javaAnnotationStartBefore(
			tokens, delimiters, cursor-1,
		)
		if !annotation {
			return false
		}
		cursor = annotationStart
	}
	return false
}

func javaFieldDeclaratorCandidate(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	candidate, end int,
) bool {
	if candidate < 0 || candidate >= end || end > len(tokens) {
		return false
	}
	next := javaVariableDeclaratorIDEnd(tokens, delimiters, candidate, end)
	return next == end || tokens[next].value == "=" || tokens[next].value == ","
}

func javaVariableDeclaratorIDEnd(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	name, end int,
) int {
	cursor := name + 1
	for cursor < end {
		if next, annotation := javaAnnotationEnd(
			tokens, delimiters, cursor, end,
		); annotation {
			cursor = next
			continue
		}
		if cursor+1 >= end || tokens[cursor].value != "[" ||
			tokens[cursor+1].value != "]" {
			break
		}
		cursor += 2
	}
	return cursor
}

func javaSegmentHasScopedInitializer(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	start, end int,
) bool {
	assignment := false
	for index := start; index < end; index++ {
		if next, annotation := javaAnnotationEnd(tokens, delimiters, index, end); annotation {
			index = next - 1
			continue
		}
		if tokens[index].value == "=" {
			assignment = true
			continue
		}
		if !assignment {
			continue
		}
		if tokens[index].value == "->" || tokens[index].value == "switch" {
			return true
		}
		if tokens[index].value == "{" &&
			(index >= len(delimiters.arrayInitializer) || !delimiters.arrayInitializer[index]) &&
			javaBraceStartsInitializer(
				tokens, delimiters, delimiters.braceOwner[index], index,
			) {
			return true
		}
	}
	return false
}

func javaAppendLexicalEnumConstants(
	source string,
	lineCount int,
	comments []javaByteSpan,
	input *javaUnicodeInput,
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	positions javaSourcePositions,
	bodyOpen, bodyClose int,
	directMembers []int,
	opaqueSpans []javaByteSpan,
	definitions *[]sourceDefinition,
	invalidDefinitionHeaders map[int]struct{},
) {
	segmentStart := bodyOpen + 1
	skipThrough := -1
	for _, index := range directMembers {
		if index < segmentStart || index > bodyClose || index >= len(tokens) {
			continue
		}
		if index <= skipThrough {
			continue
		}
		switch tokens[index].value {
		case "(", "[", "{":
			if closeIndex := javaDelimiterMatch(delimiters, index); closeIndex > index {
				skipThrough = closeIndex
				continue
			}
		}
		separator := index == bodyClose || tokens[index].value == "," || tokens[index].value == ";"
		if !separator {
			continue
		}
		nameIndex := -1
		classBody := -1
		for cursor := segmentStart; cursor < index; cursor++ {
			if delimiters.braceOwner[cursor] != bodyOpen {
				continue
			}
			if tokens[cursor].value == "{" {
				classBody = cursor
				break
			}
			if javaTokenIsSourceName(tokens[cursor]) {
				nameIndex = cursor
			}
			if (tokens[cursor].value == "(" || tokens[cursor].value == "[") &&
				javaDelimiterMatch(delimiters, cursor) > cursor {
				cursor = javaDelimiterMatch(delimiters, cursor)
			}
		}
		if nameIndex >= 0 {
			headerEnd := index
			if classBody >= 0 {
				headerEnd = classBody
			}
			extraAllowed := make([]javaByteSpan, 0, 1)
			argumentOpen := nameIndex + 1
			if argumentOpen < headerEnd && tokens[argumentOpen].value == "(" {
				if closeIndex := javaDelimiterMatch(delimiters, argumentOpen); closeIndex > argumentOpen &&
					closeIndex < headerEnd {
					extraAllowed = append(extraAllowed, javaByteSpan{
						start: tokens[argumentOpen].end,
						end:   tokens[closeIndex].start,
					})
				}
			}
			headerInvalid := javaDeclarationHeaderHasIllegalOpaque(
				tokens, delimiters, segmentStart, headerEnd,
				javaByteSpan{
					start: javaDeclarationSegmentStartOffset(
						tokens, delimiters, segmentStart,
					),
					end: tokens[headerEnd].start,
				},
				opaqueSpans, extraAllowed, true,
			)
			if headerInvalid {
				invalidDefinitionHeaders[tokens[nameIndex].start] = struct{}{}
				segmentStart = index + 1
				continue
			}
			line, column := positions.lineColumn(tokens[nameIndex].start)
			scopeStart, scopeEnd := line, line
			ownedEndColumn := 0
			ownsScope := classBody >= 0
			if ownsScope {
				endOffset, exactEnd := javaBraceEndOffsetExact(
					source, input, tokens, delimiters, classBody,
				)
				scopeStart, scopeEnd = positions.lineSpan(
					javaLexicalAttachedStart(
						input, comments, tokens[segmentStart].start,
					),
					endOffset,
				)
				ownedEndColumn = javaExactOwnedEndColumn(
					positions, scopeEnd, endOffset, exactEnd,
				)
			}
			*definitions = append(*definitions, normalizeJavaTreeDefinition(sourceDefinition{
				symbol: tokens[nameIndex].text, line: line, column: column,
				scopeStart: scopeStart, scopeEnd: scopeEnd,
				ownedEndColumn: ownedEndColumn, ownsScope: ownsScope,
			}, lineCount))
		}
		segmentStart = index + 1
		if index < len(tokens) && tokens[index].value == ";" {
			return
		}
	}
}

func javaEnumConstantPosition(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	bodyOpen, nameIndex int,
) bool {
	for index := bodyOpen + 1; index < nameIndex; index++ {
		if delimiters.braceOwner[index] == bodyOpen && tokens[index].value == ";" {
			return false
		}
	}
	return true
}

func javaSegmentContainsEnumConstants(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	bodyOpen, start, end int,
) bool {
	if start != bodyOpen+1 {
		return false
	}
	for index := start; index < end; index++ {
		if delimiters.braceOwner[index] == bodyOpen && tokens[index].value == "," {
			return true
		}
	}
	return javaEnumConstantPosition(tokens, delimiters, bodyOpen, end)
}

func javaLexicalScopes(
	source string,
	lineCount int,
	comments []javaByteSpan,
	input *javaUnicodeInput,
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	enumConstantBodies map[int]struct{},
	positions javaSourcePositions,
) []javaLineScope {
	scopes := make([]javaLineScope, 0)
	statements := newJavaLexicalStatementResolver(tokens, delimiters)
	switchLabels := javaIndexLexicalSwitchLabels(tokens, delimiters)
	statementScopes, switchArrows, statementColons := javaLexicalStatementScopes(
		lineCount, tokens, delimiters, positions, statements, switchLabels,
	)
	for open, token := range tokens {
		_, enumConstantBody := enumConstantBodies[open]
		if token.value != "{" || open < len(delimiters.arrayInitializer) &&
			delimiters.arrayInitializer[open] || statements.doBodies[open] ||
			statements.switchRuleBodies[open] || enumConstantBody {
			continue
		}
		start := javaDeclarationPrefixStart(tokens, delimiters, open)
		start = javaLexicalBraceScopeAnchor(
			tokens, delimiters, statementColons, start, open,
		)
		startOffset := token.start
		if start >= 0 && start < open {
			startOffset = tokens[start].start
		}
		if javaLexicalScopeAttachesJavadoc(tokens, delimiters, start, open) {
			startOffset = javaLexicalAttachedStart(input, comments, startOffset)
		}
		scopeStart, scopeEnd := positions.lineSpan(
			startOffset, javaBraceEndOffset(source, tokens, delimiters, open),
		)
		if scopeStart >= 1 && scopeEnd >= scopeStart && scopeEnd <= lineCount {
			scopes = append(scopes, javaLineScope{start: scopeStart, end: scopeEnd})
		}
	}
	scopes = append(scopes, statementScopes...)
	scopes = javaAppendLexicalExpressionLambdaScopes(
		lineCount, tokens, delimiters, positions, switchArrows, scopes,
	)
	return normalizeJavaLineScopes(scopes, lineCount)
}

func javaLexicalBraceScopeAnchor(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	statementColons map[int]bool,
	start, open int,
) int {
	if start < 0 || start >= open || open > len(tokens) {
		return start
	}
	owner := delimiters.braceOwner[open]
	anchor := start
	for index := start; index < open; index++ {
		if delimiters.braceOwner[index] != owner {
			continue
		}
		if next, annotation := javaAnnotationEnd(
			tokens, delimiters, index, open,
		); annotation {
			index = next - 1
			continue
		}
		switch tokens[index].value {
		case "(", "[":
			if closeIndex := javaDelimiterMatch(delimiters, index); closeIndex > index &&
				closeIndex < open {
				index = closeIndex
			}
		case ":":
			if statementColons[index] {
				anchor = index + 1
			}
		case "new", "switch":
			anchor = index
		case "->":
			anchor = index
			if index > start && tokens[index-1].value == ")" {
				if parameters := javaDelimiterMatch(delimiters, index-1); parameters >= start {
					anchor = parameters
				}
			} else if index > start && javaTokenIsSourceName(tokens[index-1]) {
				anchor = index - 1
			}
		}
	}
	return anchor
}

const (
	javaMaximumRecoveryStatementDepth = 256
	javaUnknownStatementEnd           = -2
)

type javaLexicalStatementResolver struct {
	doBodies         map[int]bool
	doWhiles         map[int]bool
	labelStarts      []bool
	switchRuleBodies map[int]bool
	tokens           []javaToken
	delimiters       javaDelimiterAnalysis
	ends             []int
	states           []uint8
	depthLimited     bool
}

func newJavaLexicalStatementResolver(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
) *javaLexicalStatementResolver {
	ends := make([]int, len(tokens))
	for index := range ends {
		ends[index] = javaUnknownStatementEnd
	}
	return &javaLexicalStatementResolver{
		tokens:           tokens,
		delimiters:       delimiters,
		ends:             ends,
		states:           make([]uint8, len(tokens)),
		doBodies:         make(map[int]bool),
		doWhiles:         make(map[int]bool),
		labelStarts:      javaIndexLexicalLabelStarts(tokens, delimiters),
		switchRuleBodies: make(map[int]bool),
	}
}

func (resolver *javaLexicalStatementResolver) end(start int) int {
	if resolver == nil {
		return -1
	}
	resolver.depthLimited = false
	return resolver.endAtDepth(start, 0)
}

func (resolver *javaLexicalStatementResolver) endAtDepth(start, depth int) int {
	if resolver == nil || start < 0 || start >= len(resolver.tokens) ||
		resolver.tokens[start].gap {
		return -1
	}
	if resolver.ends[start] != javaUnknownStatementEnd {
		return resolver.ends[start]
	}
	if depth >= javaMaximumRecoveryStatementDepth {
		resolver.depthLimited = true
		return -1
	}
	if resolver.states[start] == 1 {
		return -1
	}
	resolver.states[start] = 1
	end := resolver.uncachedEnd(start, depth)
	if resolver.depthLimited {
		resolver.states[start] = 0
		return end
	}
	resolver.states[start] = 2
	resolver.ends[start] = end
	return end
}

func (resolver *javaLexicalStatementResolver) resolveStructuralStatements() {
	if resolver == nil {
		return
	}
	for index := len(resolver.tokens) - 1; index >= 0; index-- {
		switch resolver.tokens[index].value {
		case "if", "while", "for", "synchronized", "do", "switch", "try":
			resolver.end(index)
		default:
			if resolver.labelStart(index) {
				resolver.end(index)
			}
		}
	}
}

func (resolver *javaLexicalStatementResolver) uncachedEnd(start, depth int) int {
	tokens := resolver.tokens
	switch tokens[start].value {
	case "{":
		return resolver.boundedMatch(start, start)
	case ";":
		return start
	case "if":
		_, _, end, ok := resolver.ifParts(start, depth)
		if ok {
			return end
		}
		return -1
	case "while", "for", "synchronized":
		return resolver.conditionStatementEnd(start, depth)
	case "do":
		return resolver.doStatementEnd(start, depth)
	case "switch":
		_, closeIndex, ok := resolver.switchBody(start)
		if ok {
			return closeIndex
		}
		return -1
	case "try":
		return resolver.tryStatementEnd(start)
	}
	if resolver.labelStart(start) {
		return resolver.nestedEnd(start, start+2, depth)
	}
	return resolver.simpleEnd(start)
}

func (resolver *javaLexicalStatementResolver) ifParts(
	start, depth int,
) (thenEnd, elseIndex, end int, ok bool) {
	thenStart := resolver.conditionBodyStart(start)
	if thenStart < 0 {
		return 0, 0, 0, false
	}
	thenEnd = resolver.nestedEnd(start, thenStart, depth)
	if thenEnd < thenStart {
		return 0, 0, 0, false
	}
	end = thenEnd
	elseIndex = -1
	if thenEnd+1 < len(resolver.tokens) && resolver.tokens[thenEnd+1].value == "else" {
		elseIndex = thenEnd + 1
		elseEnd := resolver.nestedEnd(start, elseIndex+1, depth)
		if elseEnd < elseIndex+1 {
			return 0, 0, 0, false
		}
		end = elseEnd
	}
	return thenEnd, elseIndex, end, true
}

func (resolver *javaLexicalStatementResolver) conditionStatementEnd(
	start, depth int,
) int {
	bodyStart := resolver.conditionBodyStart(start)
	if bodyStart < 0 {
		return -1
	}
	return resolver.nestedEnd(start, bodyStart, depth)
}

func (resolver *javaLexicalStatementResolver) conditionBodyStart(start int) int {
	openIndex := start + 1
	if openIndex >= len(resolver.tokens) || resolver.tokens[openIndex].value != "(" {
		return -1
	}
	closeIndex := javaDelimiterMatch(resolver.delimiters, openIndex)
	if closeIndex <= openIndex || closeIndex+1 >= len(resolver.tokens) {
		return -1
	}
	return closeIndex + 1
}

func (resolver *javaLexicalStatementResolver) nestedEnd(
	parentStart, nestedStart, depth int,
) int {
	end := resolver.endAtDepth(nestedStart, depth+1)
	limit := min(len(resolver.tokens), parentStart+javaMaximumRecoveryHeaderTokens)
	if end < nestedStart || end >= limit {
		return -1
	}
	return end
}

func (resolver *javaLexicalStatementResolver) doStatementEnd(start, depth int) int {
	bodyStart := start + 1
	bodyEnd := resolver.nestedEnd(start, bodyStart, depth)
	if bodyEnd < bodyStart || bodyEnd+1 >= len(resolver.tokens) ||
		resolver.tokens[bodyEnd+1].value != "while" {
		return -1
	}
	whileIndex := bodyEnd + 1
	conditionOpen := whileIndex + 1
	if conditionOpen >= len(resolver.tokens) ||
		resolver.tokens[conditionOpen].value != "(" {
		return -1
	}
	conditionClose := javaDelimiterMatch(resolver.delimiters, conditionOpen)
	limit := min(len(resolver.tokens), start+javaMaximumRecoveryHeaderTokens)
	if conditionClose <= conditionOpen || conditionClose+1 >= limit ||
		resolver.tokens[conditionClose+1].value != ";" {
		return -1
	}
	if resolver.tokens[bodyStart].value == "{" {
		resolver.doBodies[bodyStart] = true
	}
	resolver.doWhiles[whileIndex] = true
	return conditionClose + 1
}

func (resolver *javaLexicalStatementResolver) switchBody(start int) (int, int, bool) {
	conditionOpen := start + 1
	if conditionOpen >= len(resolver.tokens) ||
		resolver.tokens[conditionOpen].value != "(" {
		return 0, 0, false
	}
	conditionClose := javaDelimiterMatch(resolver.delimiters, conditionOpen)
	bodyOpen := conditionClose + 1
	if conditionClose <= conditionOpen || bodyOpen >= len(resolver.tokens) ||
		resolver.tokens[bodyOpen].value != "{" {
		return 0, 0, false
	}
	bodyClose := resolver.boundedMatch(start, bodyOpen)
	if bodyClose <= bodyOpen {
		return 0, 0, false
	}
	return bodyOpen, bodyClose, true
}

func (resolver *javaLexicalStatementResolver) tryStatementEnd(start int) int {
	limit := min(len(resolver.tokens), start+javaMaximumRecoveryHeaderTokens)
	cursor := start + 1
	if cursor < limit && resolver.tokens[cursor].value == "(" {
		closeIndex := javaDelimiterMatch(resolver.delimiters, cursor)
		if closeIndex <= cursor || closeIndex+1 >= limit {
			return -1
		}
		cursor = closeIndex + 1
	}
	if cursor >= limit || resolver.tokens[cursor].value != "{" {
		return -1
	}
	end := resolver.boundedMatch(start, cursor)
	if end <= cursor {
		return -1
	}
	cursor = end + 1
	for cursor < limit {
		switch resolver.tokens[cursor].value {
		case "catch":
			conditionOpen := cursor + 1
			if conditionOpen >= limit || resolver.tokens[conditionOpen].value != "(" {
				return end
			}
			conditionClose := javaDelimiterMatch(resolver.delimiters, conditionOpen)
			bodyOpen := conditionClose + 1
			if conditionClose <= conditionOpen || bodyOpen >= limit ||
				resolver.tokens[bodyOpen].value != "{" {
				return end
			}
			bodyClose := resolver.boundedMatch(start, bodyOpen)
			if bodyClose <= bodyOpen {
				return end
			}
			end = bodyClose
			cursor = end + 1
		case "finally":
			bodyOpen := cursor + 1
			if bodyOpen >= limit || resolver.tokens[bodyOpen].value != "{" {
				return end
			}
			bodyClose := resolver.boundedMatch(start, bodyOpen)
			if bodyClose > bodyOpen {
				end = bodyClose
			}
			return end
		default:
			return end
		}
	}
	return end
}

func (resolver *javaLexicalStatementResolver) simpleEnd(start int) int {
	limit := min(len(resolver.tokens), start+javaMaximumRecoveryHeaderTokens)
	owner := resolver.delimiters.braceOwner[start]
	scopedExpression := false
	for index := start; index < limit; index++ {
		if resolver.tokens[index].gap {
			return -1
		}
		if resolver.delimiters.braceOwner[index] != owner {
			continue
		}
		switch resolver.tokens[index].value {
		case "=", "->", "new", "switch", "return", "throw", "yield":
			scopedExpression = true
		case "(", "[":
			closeIndex := javaDelimiterMatch(resolver.delimiters, index)
			if closeIndex <= index || closeIndex >= limit {
				return -1
			}
			index = closeIndex
		case "{":
			closeIndex := javaDelimiterMatch(resolver.delimiters, index)
			if closeIndex <= index || closeIndex >= limit {
				return -1
			}
			if index == start || !scopedExpression &&
				(index >= len(resolver.delimiters.arrayInitializer) ||
					!resolver.delimiters.arrayInitializer[index]) {
				return closeIndex
			}
			index = closeIndex
		case ";":
			return index
		}
	}
	return -1
}

func (resolver *javaLexicalStatementResolver) boundedMatch(start, open int) int {
	closeIndex := javaDelimiterMatch(resolver.delimiters, open)
	if closeIndex <= open || closeIndex >= len(resolver.tokens) ||
		closeIndex >= start+javaMaximumRecoveryHeaderTokens {
		return -1
	}
	return closeIndex
}

func javaIndexLexicalLabelStarts(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
) []bool {
	starts := make([]bool, len(tokens))
	for index := range tokens {
		if index+2 >= len(tokens) || !javaTokenIsSourceName(tokens[index]) ||
			tokens[index+1].value != ":" {
			continue
		}
		if index == 0 {
			starts[index] = true
			continue
		}
		previous := index - 1
		if tokens[previous].value == ":" {
			preceding := index - 2
			starts[index] = preceding >= 0 && starts[preceding] &&
				javaTokenIsSourceName(tokens[preceding])
			continue
		}
		switch tokens[previous].value {
		case "{", ";", "}", "else", "do":
			starts[index] = true
		case ")":
			openIndex := javaDelimiterMatch(delimiters, previous)
			if openIndex <= 0 {
				continue
			}
			switch tokens[openIndex-1].value {
			case "if", "while", "for", "synchronized":
				starts[index] = true
			}
		}
	}
	return starts
}

func (resolver *javaLexicalStatementResolver) labelStart(index int) bool {
	return resolver != nil && index >= 0 && index < len(resolver.labelStarts) &&
		resolver.labelStarts[index]
}

func javaLexicalStatementScopes(
	lineCount int,
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	positions javaSourcePositions,
	resolver *javaLexicalStatementResolver,
	switchLabels map[int][]int,
) ([]javaLineScope, map[int]bool, map[int]bool) {
	scopes := make([]javaLineScope, 0)
	switchArrows := make(map[int]bool)
	statementColons := make(map[int]bool)
	resolver.resolveStructuralStatements()
	appendScope := func(start, end int) {
		if start < 0 || end < start || end >= len(tokens) {
			return
		}
		startLine, endLine := positions.lineSpan(tokens[start].start, tokens[end].end)
		if startLine >= 1 && endLine >= startLine && endLine <= lineCount {
			scopes = append(scopes, javaLineScope{start: startLine, end: endLine})
		}
	}
	for index := range tokens {
		switch tokens[index].value {
		case "if":
			thenEnd, elseIndex, end, ok := resolver.ifParts(index, 0)
			if !ok {
				continue
			}
			appendScope(index, end)
			appendScope(index, thenEnd)
			if elseIndex >= 0 {
				appendScope(elseIndex, end)
			}
		case "while":
			if resolver.doWhiles[index] {
				continue
			}
			appendScope(index, resolver.end(index))
		case "for", "synchronized", "do", "try":
			appendScope(index, resolver.end(index))
		case "switch":
			bodyOpen, bodyClose, ok := resolver.switchBody(index)
			if !ok {
				continue
			}
			appendScope(index, bodyClose)
			javaAppendLexicalSwitchScopes(
				tokens, delimiters, resolver, positions, lineCount,
				bodyOpen, bodyClose, switchLabels[bodyOpen], &scopes,
				switchArrows, statementColons,
			)
		default:
			if resolver.labelStart(index) {
				statementColons[index+1] = true
				appendScope(index, resolver.end(index))
			}
		}
	}
	return scopes, switchArrows, statementColons
}

func javaIndexLexicalSwitchLabels(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
) map[int][]int {
	labels := make(map[int][]int)
	for index, token := range tokens {
		if token.value != "case" && token.value != "default" ||
			index >= len(delimiters.braceOwner) {
			continue
		}
		owner := delimiters.braceOwner[index]
		if owner < 0 || owner >= len(tokens) || tokens[owner].value != "{" {
			continue
		}
		if token.value == "default" && index > 0 && tokens[index-1].value == "," &&
			delimiters.braceOwner[index-1] == owner {
			continue
		}
		labels[owner] = append(labels[owner], index)
	}
	return labels
}

func javaAppendLexicalSwitchScopes(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	resolver *javaLexicalStatementResolver,
	positions javaSourcePositions,
	lineCount, bodyOpen, bodyClose int,
	labels []int,
	scopes *[]javaLineScope,
	switchArrows map[int]bool,
	statementColons map[int]bool,
) {
	appendScope := func(start, end int) {
		if start < 0 || end < start || end >= len(tokens) {
			return
		}
		startLine, endLine := positions.lineSpan(tokens[start].start, tokens[end].end)
		if startLine >= 1 && endLine >= startLine && endLine <= lineCount {
			*scopes = append(*scopes, javaLineScope{start: startLine, end: endLine})
		}
	}
	for labelCursor, label := range labels {
		limit := bodyClose
		if labelCursor+1 < len(labels) {
			limit = labels[labelCursor+1]
		}
		colon, arrow := -1, -1
		for index := label + 1; index < limit; index++ {
			if delimiters.braceOwner[index] != bodyOpen {
				continue
			}
			switch tokens[index].value {
			case "(", "[":
				if closeIndex := javaDelimiterMatch(delimiters, index); closeIndex > index &&
					closeIndex < limit {
					index = closeIndex
				}
			case ":":
				if colon < 0 {
					colon = index
				}
			case "->":
				arrow = index
				index = limit
			}
		}
		if arrow >= 0 {
			switchArrows[arrow] = true
			bodyStart := arrow + 1
			end := resolver.end(bodyStart)
			if end >= bodyStart && end < limit {
				appendScope(label, end)
				if tokens[bodyStart].value == "{" {
					resolver.switchRuleBodies[bodyStart] = true
				}
			}
			continue
		}
		if colon >= 0 {
			statementColons[colon] = true
			appendScope(label, max(colon, limit-1))
		}
	}
}

func javaAppendLexicalExpressionLambdaScopes(
	lineCount int,
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	positions javaSourcePositions,
	switchArrows map[int]bool,
	scopes []javaLineScope,
) []javaLineScope {
	for index, token := range tokens {
		if token.value != "->" || switchArrows[index] || index+1 >= len(tokens) ||
			tokens[index+1].value == "{" {
			continue
		}
		end := javaLexicalExpressionEnd(tokens, delimiters, index+1)
		if end < index+1 {
			continue
		}
		start := index
		if index > 0 && tokens[index-1].value == ")" {
			if parameters := javaDelimiterMatch(delimiters, index-1); parameters >= 0 {
				start = parameters
			}
		} else if index > 0 && javaTokenIsSourceName(tokens[index-1]) {
			start = index - 1
		}
		startLine, endLine := positions.lineSpan(tokens[start].start, tokens[end].end)
		if startLine >= 1 && endLine >= startLine && endLine <= lineCount {
			scopes = append(scopes, javaLineScope{start: startLine, end: endLine})
		}
	}
	return scopes
}

func javaLexicalExpressionEnd(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	start int,
) int {
	limit := min(len(tokens), start+javaMaximumRecoveryHeaderTokens)
	for index := start; index < limit; index++ {
		if tokens[index].gap {
			return -1
		}
		switch tokens[index].value {
		case "(", "[", "{":
			if closeIndex := javaDelimiterMatch(delimiters, index); closeIndex > index {
				index = closeIndex
			}
		case "<":
			if closeIndex := javaExpressionTypeArgumentClose(
				tokens, delimiters, index, limit,
			); closeIndex > index {
				index = closeIndex
			}
		case ";":
			return index - 1
		case ",":
			return index - 1
		case ")", "]", "}":
			if javaDelimiterMatch(delimiters, index) < start {
				return index - 1
			}
		}
	}
	return -1
}

func javaLexicalScopeAttachesJavadoc(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	start, open int,
) bool {
	if start < 0 || start >= open || open > len(tokens) {
		return false
	}
	for index := start; index < open; index++ {
		if next, annotation := javaAnnotationEnd(tokens, delimiters, index, open); annotation {
			index = next - 1
			continue
		}
		switch tokens[index].value {
		case "if", "else", "for", "while", "do", "switch", "case", "try", "catch",
			"finally", "synchronized", ":":
			return false
		}
	}
	return true
}

func javaLexicalImports(
	lineCount int,
	comments []javaByteSpan,
	input *javaUnicodeInput,
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	positions javaSourcePositions,
	moduleBodies map[int]struct{},
	opaqueSpans []javaByteSpan,
	invalidHeaders *[]javaLineSpan,
) []javaLineSpan {
	spans := make([]javaLineSpan, 0)
	requiresRestart := make(map[int]bool)
	for index, token := range tokens {
		owner := -1
		if index < len(delimiters.braceOwner) {
			owner = delimiters.braceOwner[index]
		}
		if token.value == ";" || token.value == "{" &&
			(index >= len(delimiters.arrayInitializer) ||
				!delimiters.arrayInitializer[index]) {
			delete(requiresRestart, owner)
		}
		ordinary := token.value == "import" && delimiters.braceOwner[index] < 0
		requires := token.value == "requires" &&
			javaLexicalRequiresDirective(
				tokens, delimiters, moduleBodies, index, requiresRestart[owner],
			)
		if token.value == "requires" {
			if _, moduleBody := moduleBodies[owner]; moduleBody {
				requiresRestart[owner] = true
			}
		}
		if !ordinary && !requires {
			continue
		}
		endIndex := -1
		limit := min(len(tokens), index+javaMaximumRecoveryHeaderTokens)
		for cursor := index + 1; cursor < limit; cursor++ {
			if tokens[cursor].value == token.value &&
				delimiters.braceOwner[cursor] == owner {
				break
			}
			if tokens[cursor].value == ";" &&
				delimiters.braceOwner[cursor] == owner {
				endIndex = cursor
				break
			}
			if ordinary && tokens[cursor].value == "{" {
				break
			}
		}
		headerTokenStart := javaDeclarationPrefixStart(tokens, delimiters, index)
		headerEnd := len(positions.source)
		var headerTokenEnd int
		if endIndex >= 0 {
			headerEnd = tokens[endIndex].start
			headerTokenEnd = endIndex
		} else {
			line, _ := positions.lineColumn(token.start)
			if line < len(positions.lineStarts) {
				headerEnd = positions.lineStarts[line] - 1
			}
			headerTokenEnd = sort.Search(len(tokens), func(candidate int) bool {
				return tokens[candidate].start >= headerEnd
			})
		}
		headerSpan := javaByteSpan{
			start: javaDeclarationSegmentStartOffset(
				tokens, delimiters, headerTokenStart,
			),
			end: headerEnd,
		}
		if javaDeclarationHeaderHasIllegalOpaque(
			tokens, delimiters, headerTokenStart, headerTokenEnd,
			headerSpan,
			opaqueSpans, nil, false,
		) {
			if invalidHeaders != nil {
				startLine, endLine := positions.lineSpan(headerSpan.start, headerSpan.end)
				*invalidHeaders = append(*invalidHeaders, javaLineSpan{
					start: startLine, end: endLine,
				})
			}
			continue
		}
		if endIndex < 0 {
			// A visibly incomplete declaration remains useful recovery evidence,
			// but it must stop at its physical line.
			line, _ := positions.lineColumn(token.start)
			spans = append(spans, javaLineSpan{start: line, end: line})
			continue
		}
		startOffset := javaLexicalAttachedStart(input, comments, token.start)
		startLine, endLine := positions.lineSpan(startOffset, tokens[endIndex].end)
		spans = append(spans, javaLineSpan{start: startLine, end: endLine})
	}
	return normalizeJavaLineSpans(spans, lineCount)
}

func javaLexicalRequiresDirective(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	moduleBodies map[int]struct{},
	index int,
	restart bool,
) bool {
	if index < 0 || index >= len(tokens) || index >= len(delimiters.braceOwner) {
		return false
	}
	bodyOpen := delimiters.braceOwner[index]
	if bodyOpen < 0 || bodyOpen >= len(tokens) || tokens[bodyOpen].value != "{" {
		return false
	}
	if _, ok := moduleBodies[bodyOpen]; !ok {
		return false
	}
	if !restart && javaDeclarationPrefixStart(tokens, delimiters, index) != index {
		return false
	}
	cursor := index + 1
	for cursor < len(tokens) &&
		(tokens[cursor].value == "static" || tokens[cursor].value == "transitive") {
		cursor++
	}
	if cursor >= len(tokens) || !javaTokenIsSourceName(tokens[cursor]) ||
		delimiters.braceOwner[cursor] != bodyOpen {
		return false
	}
	cursor++
	for cursor+1 < len(tokens) && tokens[cursor].value == "." &&
		javaTokenIsSourceName(tokens[cursor+1]) &&
		delimiters.braceOwner[cursor+1] == bodyOpen {
		cursor += 2
	}
	return cursor >= len(tokens) || tokens[cursor].value == ";" || tokens[cursor].gap ||
		tokens[cursor].value == "requires" && delimiters.braceOwner[cursor] == bodyOpen
}

func javaLexicalAttachedStart(
	input *javaUnicodeInput,
	comments []javaByteSpan,
	start int,
) int {
	if input == nil || start < 0 || start > len(input.source) || len(comments) == 0 {
		return start
	}
	index := sort.Search(len(comments), func(index int) bool {
		return comments[index].end > start
	}) - 1
	if index < 0 {
		return start
	}
	pending := start
	attached := start
	markdownGroup := false
	markdownSeparated := false
	for index >= 0 {
		comment := comments[index]
		if comment.start < 0 || comment.end > pending || comment.end > len(input.source) {
			break
		}
		parts := javaTranslatedCommentParts(input, comment)
		if len(parts) == 0 {
			parts = []javaByteSpan{comment}
		}
		for partIndex := len(parts) - 1; partIndex >= 0; partIndex-- {
			part := parts[partIndex]
			whitespace, lineBreaks := javaTranslatedWhitespaceGap(input, part.end, pending)
			if !whitespace || lineBreaks > 1 {
				return attached
			}
			markdown := javaTranslatedMarkdownJavadoc(input, part)
			switch {
			case markdown:
				if markdownSeparated {
					return attached
				}
				attached = part.start
				markdownGroup = true
			case javaTranslatedJavadoc(input, part):
				if markdownGroup {
					return attached
				}
				attached = part.start
			case markdownGroup:
				// Ordinary comments remain transparent between the closest doc
				// group and its declaration, but they separate an older Markdown
				// `///` series from that closest group.
				markdownSeparated = true
			}
			pending = part.start
		}
		index--
	}
	return attached
}

func javaTranslatedCommentParts(
	input *javaUnicodeInput,
	comment javaByteSpan,
) []javaByteSpan {
	if input == nil || comment.start < 0 || comment.end > len(input.source) ||
		comment.start >= comment.end {
		return nil
	}
	cursor := input.cursor(comment.start, comment.end)
	parts := make([]javaByteSpan, 0, 2)
	for {
		first, firstOK := cursor.next()
		second, secondOK := cursor.next()
		if !firstOK || !secondOK || first.value != '/' {
			return parts
		}
		switch second.value {
		case '/':
			return append(parts, javaByteSpan{start: first.start, end: comment.end})
		case '*':
			end := comment.end
			for {
				unit, ok := cursor.next()
				if !ok {
					break
				}
				if unit.value != '*' {
					continue
				}
				closing, ok := cursor.peek()
				if !ok || closing.value != '/' {
					continue
				}
				closing, _ = cursor.next()
				end = closing.end
				break
			}
			parts = append(parts, javaByteSpan{start: first.start, end: end})
			if end >= comment.end {
				return parts
			}
		default:
			return parts
		}
	}
}

func javaTranslatedJavadoc(input *javaUnicodeInput, comment javaByteSpan) bool {
	if input == nil || comment.start < 0 || comment.end > len(input.source) ||
		comment.start >= comment.end {
		return false
	}
	cursor := input.cursor(comment.start, comment.end)
	first, firstOK := cursor.next()
	second, secondOK := cursor.next()
	third, thirdOK := cursor.next()
	return firstOK && secondOK && thirdOK && first.value == '/' &&
		second.value == '*' && third.value == '*'
}

func javaTranslatedMarkdownJavadoc(input *javaUnicodeInput, comment javaByteSpan) bool {
	if input == nil || comment.start < 0 || comment.end > len(input.source) ||
		comment.start >= comment.end {
		return false
	}
	cursor := input.cursor(comment.start, comment.end)
	first, firstOK := cursor.next()
	second, secondOK := cursor.next()
	third, thirdOK := cursor.next()
	return firstOK && secondOK && thirdOK && first.value == '/' &&
		second.value == '/' && third.value == '/' &&
		javaTranslatedLinePrefixWhitespace(input, first.start)
}

func javaTranslatedWhitespaceGap(
	input *javaUnicodeInput,
	start, end int,
) (bool, int) {
	if input == nil || start < 0 || end < start || end > len(input.source) {
		return false, 0
	}
	cursor := input.cursor(start, end)
	lineBreaks := 0
	previousCR := false
	for {
		unit, ok := cursor.next()
		if !ok {
			return true, lineBreaks
		}
		switch unit.value {
		case '\r':
			lineBreaks++
			previousCR = true
		case '\n':
			if !previousCR {
				lineBreaks++
			}
			previousCR = false
		case ' ', '\t', '\f':
			previousCR = false
		default:
			return false, lineBreaks
		}
	}
}

func javaTokenIsSourceName(token javaToken) bool {
	if !token.identifier || token.text == "" || token.value == "_" {
		return false
	}
	return !javaHardKeywords[token.value]
}

var javaHardKeywords = map[string]bool{
	"abstract": true, "assert": true, "boolean": true, "break": true,
	"byte": true, "case": true, "catch": true, "char": true,
	"class": true, "const": true, "continue": true, "default": true,
	"do": true, "double": true, "else": true, "enum": true,
	"extends": true, "final": true, "finally": true, "float": true,
	"for": true, "goto": true, "if": true, "implements": true,
	"import": true, "instanceof": true, "int": true, "interface": true,
	"long": true, "native": true, "new": true, "package": true,
	"private": true, "protected": true, "public": true, "return": true,
	"short": true, "static": true, "strictfp": true, "super": true,
	"switch": true, "synchronized": true, "this": true, "throw": true,
	"throws": true, "transient": true, "try": true, "void": true,
	"volatile": true, "while": true, "true": true, "false": true,
	"null": true,
}
