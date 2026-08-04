package repoview

import (
	"context"
	"sort"
	"strings"

	tsxlanguage "github.com/dcosson/treesitter-go/languages/tsx"
	typescriptlanguage "github.com/dcosson/treesitter-go/languages/typescript"
)

const (
	typeScriptRecoveryChunkTokenLimit = 16 << 10
	typeScriptRecoveryWindowLimit     = 256
	typeScriptRecoveryParsedByteLimit = javascriptMaximumConcreteParseBytes
)

type typeScriptLexRecoveryResult struct {
	definitions []javascriptLexDefinition
	imports     []javascriptLineSpan
	scopes      []javascriptLineScope
	covered     []javascriptLineSpan
}

type typeScriptRecoveryWindow struct {
	start  int
	end    int
	rescue bool
}

type typeScriptRecoveryTokenWindow struct {
	start  int
	end    int
	rescue bool
}

// typeScriptLexRecovery supplements the JavaScript-compatible lexer with
// bounded concrete parses of statement-sized windows. This retains the exact
// TypeScript grammar for generated sources that exceed the whole-file parser
// budget, while malformed windows still fall back to conservative lexical
// declaration headers below.
func typeScriptLexRecovery(
	source string,
	flavor javascriptSyntaxFlavor,
	tokens []javascriptToken,
	delimiters javascriptDelimiterPairs,
	boundaries javascriptLexBoundaries,
	commentSpans []javascriptByteSpan,
	recoveryLines map[int]bool,
) typeScriptLexRecoveryResult {
	if !flavor.isTypeScript() {
		return typeScriptLexRecoveryResult{}
	}
	positions := javascriptSourcePositions{
		source: source, lineStarts: javascriptLineStarts(source),
	}
	result := typeScriptLexRecoveryResult{
		definitions: typeScriptLexDeclarationHeaders(
			source, tokens, delimiters, boundaries, commentSpans, recoveryLines,
		),
		imports: typeScriptLexReferenceImports(source, tokens, commentSpans),
	}
	result.imports = append(
		result.imports,
		typeScriptLexGenericCallTypeImports(source, tokens, delimiters)...,
	)

	windows := typeScriptRecoveryWindows(
		source, tokens, delimiters, boundaries, positions, commentSpans,
	)
	if len(windows) == 0 {
		result.definitions = javascriptLexUniqueDefinitions(result.definitions)
		result.imports = normalizeJavaScriptLineSpans(result.imports)
		return result
	}

	ctx, cancel := context.WithTimeout(context.Background(), treeSitterSyntaxParseTimeout)
	defer cancel()
	language := typescriptlanguage.Language()
	if flavor == javascriptSyntaxFlavorTSX {
		language = tsxlanguage.Language()
	}
	parsedBytes := 0
	reservedRescueBytes := 0
	for _, window := range windows {
		if window.rescue {
			reservedRescueBytes += min(
				window.end-window.start, typeScriptRecoveryParsedByteLimit/2,
			)
			reservedRescueBytes = min(
				reservedRescueBytes, typeScriptRecoveryParsedByteLimit/2,
			)
		}
	}
	broadByteLimit := typeScriptRecoveryParsedByteLimit - reservedRescueBytes
	cleanWindows := make([]typeScriptRecoveryWindow, 0)
	for _, window := range windows {
		if ctx.Err() != nil || window.start < 0 || window.end <= window.start ||
			window.end > len(source) {
			break
		}
		if typeScriptRecoveryWindowContained(window, cleanWindows) {
			continue
		}
		if window.end-window.start > javascriptMaximumConcreteParseBytes ||
			!window.rescue && parsedBytes > broadByteLimit-(window.end-window.start) ||
			parsedBytes > typeScriptRecoveryParsedByteLimit-(window.end-window.start) {
			continue
		}
		chunk := source[window.start:window.end]
		parsedBytes += len(chunk)
		tree, ok := parseTreeSitterSyntaxContext(ctx, chunk, language)
		if !ok || javascriptSyntaxRootIsError(tree) ||
			len(typeScriptSyntaxMissingTokenSpans(tree, len(chunk))) != 0 {
			continue
		}
		chunkLines := len(javascriptLineStarts(chunk))
		syntaxErrors := javascriptSyntaxErrorSpans(tree, len(chunk))
		if len(syntaxErrors) == 0 {
			cleanWindows = append(cleanWindows, window)
		}
		result.covered = append(result.covered, typeScriptRecoveryCoveredLines(
			source, window, positions, syntaxErrors,
		)...)
		comments, _ := javascriptSyntaxMasks(chunk, tree)
		semantic := javascriptSyntaxSemanticLiterals(chunk, tree)
		excluded := normalizeJavaScriptSpans(append(
			append([]javascriptByteSpan(nil), comments...), semantic...,
		))
		attached := javascriptSyntaxAttachedStarts(chunk, tree)
		errorContext := javascriptSyntaxErrorContexts(tree)
		definitions := javascriptTreeDefinitionsFromSyntax(
			chunk,
			chunkLines,
			tree,
			excluded,
			attached,
			errorContext,
		)
		definitions = filterTypeScriptRecoveryDefinitions(
			chunk,
			definitions,
			javascriptRecoveryLines(chunk, syntaxErrors),
			javascriptLineStarts(chunk),
		)
		line, _ := positions.lineColumn(window.start)
		lineOffset := line - 1
		for _, definition := range definitions {
			definition.line += lineOffset
			definition.scopeStart += lineOffset
			definition.scopeEnd += lineOffset
			result.definitions = append(result.definitions, javascriptLexDefinition{
				definition: definition,
				strong:     true,
				force:      true,
			})
		}
		for _, scope := range javascriptTreeScopesFromSyntax(
			chunk,
			chunkLines,
			tree,
			excluded,
			attached,
			errorContext,
		) {
			scope.start += lineOffset
			scope.end += lineOffset
			result.scopes = append(result.scopes, scope)
		}
		for _, statement := range javascriptTreeImportsFromSyntaxFlavor(
			chunk,
			chunkLines,
			tree,
			excluded,
			attached,
			errorContext,
			true,
		) {
			statement.start += lineOffset
			statement.end += lineOffset
			result.imports = append(result.imports, statement)
		}
	}
	result.definitions = javascriptLexUniqueDefinitions(result.definitions)
	result.imports = normalizeJavaScriptLineSpans(result.imports)
	result.scopes = normalizeJavaScriptScopes(result.scopes)
	result.covered = normalizeJavaScriptLineSpans(result.covered)
	return result
}

func typeScriptRecoveryWindowContained(
	window typeScriptRecoveryWindow,
	containers []typeScriptRecoveryWindow,
) bool {
	for _, container := range containers {
		if container.start <= window.start && container.end >= window.end {
			return true
		}
	}
	return false
}

func typeScriptRecoveryCoveredLines(
	source string,
	window typeScriptRecoveryWindow,
	positions javascriptSourcePositions,
	chunkErrors []javascriptByteSpan,
) []javascriptLineSpan {
	if window.start < 0 || window.end <= window.start || window.end > len(source) {
		return nil
	}
	startLine, endLine := positions.lineSpan(window.start, window.end)
	if startLine < 1 || startLine > len(positions.lineStarts) ||
		positions.lineStarts[startLine-1] != window.start {
		startLine++
	}
	lineEnd := len(source)
	if endLine < len(positions.lineStarts) {
		lineEnd = positions.lineStarts[endLine]
	}
	if window.end < lineEnd && !typeScriptRecoveryTrailingTriviaOnly(
		source, window.end, lineEnd,
	) {
		endLine--
	}
	if endLine < startLine {
		return nil
	}
	chunkRecovery := javascriptRecoveryLines(source[window.start:window.end], chunkErrors)
	covered := make([]javascriptLineSpan, 0, 1)
	spanStart := 0
	for line := startLine; line <= endLine; line++ {
		chunkLine := line - startLine + 1
		if chunkRecovery[chunkLine] {
			if spanStart > 0 {
				covered = append(covered, javascriptLineSpan{start: spanStart, end: line - 1})
				spanStart = 0
			}
			continue
		}
		if spanStart == 0 {
			spanStart = line
		}
	}
	if spanStart > 0 {
		covered = append(covered, javascriptLineSpan{start: spanStart, end: endLine})
	}
	return covered
}

func typeScriptRecoveryTrailingTriviaOnly(source string, start, end int) bool {
	if start < 0 || end < start || end > len(source) {
		return false
	}
	for offset := start; offset < end; {
		if size := javascriptWhitespaceSize(source, offset); size > 0 {
			offset += size
			continue
		}
		if strings.HasPrefix(source[offset:end], "//") {
			return true
		}
		if strings.HasPrefix(source[offset:end], "/*") {
			closeOffset := strings.Index(source[offset+2:end], "*/")
			if closeOffset < 0 {
				return false
			}
			offset += closeOffset + 4
			continue
		}
		return false
	}
	return true
}

func typeScriptDefinitionsOutsideRecoveryCoverage(
	definitions []javascriptLexDefinition,
	covered []javascriptLineSpan,
) []javascriptLexDefinition {
	if len(definitions) == 0 || len(covered) == 0 {
		return definitions
	}
	filtered := definitions[:0]
	for _, definition := range definitions {
		if typeScriptLineRangeCovered(
			definition.definition.line, definition.definition.line, covered,
		) {
			continue
		}
		filtered = append(filtered, definition)
	}
	return filtered
}

func typeScriptScopesOutsideRecoveryCoverage(
	scopes []javascriptLineScope,
	covered []javascriptLineSpan,
) []javascriptLineScope {
	if len(scopes) == 0 || len(covered) == 0 {
		return scopes
	}
	filtered := scopes[:0]
	for _, scope := range scopes {
		if typeScriptLineRangeCovered(scope.start, scope.end, covered) {
			continue
		}
		filtered = append(filtered, scope)
	}
	return filtered
}

func typeScriptImportsOutsideRecoveryCoverage(
	imports []javascriptLineSpan,
	covered []javascriptLineSpan,
) []javascriptLineSpan {
	if len(imports) == 0 || len(covered) == 0 {
		return imports
	}
	filtered := imports[:0]
	for _, statement := range imports {
		if typeScriptLineRangeCovered(statement.start, statement.end, covered) {
			continue
		}
		filtered = append(filtered, statement)
	}
	return filtered
}

func typeScriptLineRangeCovered(start, end int, covered []javascriptLineSpan) bool {
	if start < 1 || end < start || len(covered) == 0 {
		return false
	}
	index := sort.Search(len(covered), func(index int) bool {
		return covered[index].end >= end
	})
	return index < len(covered) && covered[index].start <= start
}

func typeScriptRecoveryWindows(
	source string,
	tokens []javascriptToken,
	delimiters javascriptDelimiterPairs,
	boundaries javascriptLexBoundaries,
	positions javascriptSourcePositions,
	commentSpans []javascriptByteSpan,
) []typeScriptRecoveryWindow {
	if len(tokens) == 0 {
		return nil
	}
	statements := make([]typeScriptRecoveryTokenWindow, 0, min(len(tokens), 256))
	for start := 0; start < len(tokens); {
		end := boundaries.statementEnd(start)
		if end < start || end >= len(tokens) {
			end = typeScriptLexicalSemicolonEnd(tokens, start)
		}
		if end < start || end >= len(tokens) {
			end = len(tokens) - 1
		}
		statements = append(statements, typeScriptRecoveryTokenWindow{start: start, end: end})
		start = end + 1
	}

	broadWindows := make([]typeScriptRecoveryTokenWindow, 0, len(statements))
	for index := 0; index < len(statements); {
		start := statements[index].start
		end := statements[index].end
		if end-start+1 > typeScriptRecoveryChunkTokenLimit {
			index++
			continue
		}
		index++
		for index < len(statements) &&
			statements[index].end-start+1 <= typeScriptRecoveryChunkTokenLimit {
			end = statements[index].end
			index++
		}
		broadWindows = append(broadWindows, typeScriptRecoveryTokenWindow{
			start: start, end: end,
		})
	}

	// A malformed unmatched container can make the broad batch invalid. Add
	// declaration-line windows so later clean declarations remain recoverable.
	rescueWindows := make([]typeScriptRecoveryTokenWindow, 0)
	for index, token := range tokens {
		if !token.startsLine() || !typeScriptRecoveryDeclarationToken(token.text) {
			continue
		}
		end := boundaries.statementEnd(index)
		if end < index || end >= len(tokens) ||
			end-index+1 > typeScriptRecoveryChunkTokenLimit {
			end = typeScriptLexicalSemicolonEnd(tokens, index)
		}
		if end >= index && end < len(tokens) &&
			end-index+1 <= typeScriptRecoveryChunkTokenLimit &&
			!typeScriptRecoveryCutsGenericArguments(tokens, index, end, delimiters) {
			rescueWindows = append(rescueWindows, typeScriptRecoveryTokenWindow{
				start: index, end: end, rescue: true,
			})
		}
	}
	tokenWindows := typeScriptSelectRecoveryTokenWindows(
		broadWindows, rescueWindows, typeScriptRecoveryWindowLimit,
	)

	windows := make([]typeScriptRecoveryWindow, 0, min(len(tokenWindows), typeScriptRecoveryWindowLimit))
	seen := make(map[[2]int]bool)
	for _, tokenWindow := range tokenWindows {
		if tokenWindow.start < 0 || tokenWindow.end < tokenWindow.start ||
			tokenWindow.end >= len(tokens) {
			continue
		}
		startOffset := javascriptLexAttachedStart(
			source, tokens[tokenWindow.start].startOffset(), commentSpans,
		)
		line, _ := positions.lineColumn(startOffset)
		if line >= 1 && line <= len(positions.lineStarts) {
			startOffset = positions.lineStarts[line-1]
		}
		window := typeScriptRecoveryWindow{
			start:  startOffset,
			end:    tokens[tokenWindow.end].endOffset(),
			rescue: tokenWindow.rescue,
		}
		key := [2]int{window.start, window.end}
		if window.end <= window.start || seen[key] {
			continue
		}
		seen[key] = true
		windows = append(windows, window)
	}
	return windows
}

func typeScriptSelectRecoveryTokenWindows(
	broad []typeScriptRecoveryTokenWindow,
	rescue []typeScriptRecoveryTokenWindow,
	limit int,
) []typeScriptRecoveryTokenWindow {
	if limit <= 0 || len(broad) == 0 && len(rescue) == 0 {
		return nil
	}
	rescueCount := min(len(rescue), limit/2)
	if len(rescue) > 0 && rescueCount == 0 {
		rescueCount = 1
	}
	broadCount := min(len(broad), limit-rescueCount)
	rescueCount = min(len(rescue), limit-broadCount)
	if remaining := limit - broadCount - rescueCount; remaining > 0 {
		additionalBroad := min(len(broad)-broadCount, remaining)
		broadCount += additionalBroad
		remaining -= additionalBroad
		rescueCount += min(len(rescue)-rescueCount, remaining)
	}
	selected := make([]typeScriptRecoveryTokenWindow, 0, broadCount+rescueCount)
	selected = append(selected, typeScriptSampleRecoveryTokenWindows(broad, broadCount)...)
	selected = append(selected, typeScriptSampleRecoveryTokenWindows(rescue, rescueCount)...)
	return selected
}

func typeScriptSampleRecoveryTokenWindows(
	windows []typeScriptRecoveryTokenWindow,
	count int,
) []typeScriptRecoveryTokenWindow {
	if count <= 0 || len(windows) == 0 {
		return nil
	}
	if count >= len(windows) {
		return append([]typeScriptRecoveryTokenWindow(nil), windows...)
	}
	if count == 1 {
		return []typeScriptRecoveryTokenWindow{windows[len(windows)-1]}
	}
	selected := make([]typeScriptRecoveryTokenWindow, 0, count)
	for index := range count {
		windowIndex := index * (len(windows) - 1) / (count - 1)
		selected = append(selected, windows[windowIndex])
	}
	return selected
}

func typeScriptRecoveryCutsGenericArguments(
	tokens []javascriptToken,
	start int,
	end int,
	delimiters javascriptDelimiterPairs,
) bool {
	if start < 0 || end < start || end >= len(tokens) {
		return false
	}
	for index := start; index <= end; index++ {
		if !typeScriptLexGenericArgumentStart(tokens, index) {
			continue
		}
		closeIndex := typeScriptLexGenericArgumentEnd(tokens, index, delimiters)
		if closeIndex < 0 || closeIndex > end {
			return true
		}
		index = closeIndex
	}
	return false
}

func typeScriptLexicalSemicolonEnd(tokens []javascriptToken, start int) int {
	limit := min(len(tokens), start+typeScriptRecoveryChunkTokenLimit)
	for index := start; index < limit; index++ {
		if tokens[index].text == ";" {
			return index
		}
	}
	return -1
}

func typeScriptRecoveryDeclarationToken(token string) bool {
	switch token {
	case "abstract", "class", "const", "declare", "enum", "export", "function",
		"import", "interface", "let", "module", "namespace", "type", "using", "var":
		return true
	default:
		return false
	}
}

func typeScriptLexDeclarationHeaders(
	source string,
	tokens []javascriptToken,
	delimiters javascriptDelimiterPairs,
	boundaries javascriptLexBoundaries,
	commentSpans []javascriptByteSpan,
	recoveryLines map[int]bool,
) []javascriptLexDefinition {
	positions := javascriptSourcePositions{source: source, lineStarts: javascriptLineStarts(source)}
	definitions := make([]javascriptLexDefinition, 0)
	for index, token := range tokens {
		kind := token.text
		if kind != "interface" && kind != "type" && kind != "enum" &&
			kind != "namespace" && kind != "module" {
			continue
		}
		if !typeScriptLexPlausibleDeclarationStart(tokens, index) || index+1 >= len(tokens) {
			continue
		}
		nameIndex := index + 1
		if kind == "module" && tokens[nameIndex].literal() {
			continue
		}
		if !javascriptLexBindingName(tokens[nameIndex].text) {
			continue
		}
		if kind == "type" && !typeScriptLexTypeAliasHasEquals(tokens, nameIndex+1) {
			continue
		}
		bodyStart := -1
		if kind != "type" {
			bodyStart = typeScriptLexDeclarationBody(tokens, nameIndex+1, delimiters)
			if bodyStart < 0 {
				continue
			}
		}
		endIndex := boundaries.statementEnd(index)
		force := false
		if bodyStart >= 0 {
			if bodyEnd, paired := delimiters.get(bodyStart); paired && bodyEnd > bodyStart {
				endIndex = bodyEnd
				force = typeScriptLexBodyTouchesRecovery(
					tokens, bodyStart, bodyEnd, positions, recoveryLines,
				)
			}
		}
		if endIndex < nameIndex || endIndex >= len(tokens) {
			endIndex = nameIndex
		}
		line, column := positions.lineColumn(tokens[nameIndex].startOffset())
		startOffset := javascriptLexAttachedStart(source, token.startOffset(), commentSpans)
		scopeStart, _ := positions.lineColumn(startOffset)
		scopeEnd, _ := positions.lineColumn(max(tokens[endIndex].endOffset()-1, 0))
		definition := sourceDefinition{
			symbol: tokens[nameIndex].text, line: line, column: column,
			scopeStart: scopeStart, scopeEnd: max(line, scopeEnd), ownsScope: true,
		}
		definitions = append(definitions, javascriptLexDefinition{
			definition: definition,
			strong:     true,
			force:      force,
		})

		if kind != "namespace" && kind != "module" {
			continue
		}
		cursor := nameIndex + 1
		for cursor+1 < bodyStart && tokens[cursor].text == "." &&
			javascriptLexBindingName(tokens[cursor+1].text) {
			cursor++
			partLine, partColumn := positions.lineColumn(tokens[cursor].startOffset())
			definitions = append(definitions, javascriptLexDefinition{
				definition: sourceDefinition{
					symbol: tokens[cursor].text, line: partLine, column: partColumn,
					scopeStart: scopeStart, scopeEnd: max(partLine, scopeEnd), ownsScope: true,
				},
				strong: true,
				force:  force,
			})
			cursor++
		}
	}
	definitions = append(definitions, typeScriptLexMemberDefinitions(
		source, tokens, delimiters, boundaries, commentSpans, recoveryLines,
	)...)
	return definitions
}

func typeScriptLexMemberDefinitions(
	source string,
	tokens []javascriptToken,
	delimiters javascriptDelimiterPairs,
	boundaries javascriptLexBoundaries,
	commentSpans []javascriptByteSpan,
	recoveryLines map[int]bool,
) []javascriptLexDefinition {
	positions := javascriptSourcePositions{source: source, lineStarts: javascriptLineStarts(source)}
	definitions := make([]javascriptLexDefinition, 0)
	for index, token := range tokens {
		bodyStart := -1
		switch token.text {
		case "class":
			bodyStart = javascriptLexClassBodyStart(tokens, index, delimiters, 0)
		case "interface":
			if typeScriptLexPlausibleDeclarationStart(tokens, index) && index+2 < len(tokens) {
				bodyStart = typeScriptLexDeclarationBody(tokens, index+2, delimiters)
			}
		case "type":
			if typeScriptLexPlausibleDeclarationStart(tokens, index) && index+2 < len(tokens) &&
				typeScriptLexTypeAliasHasEquals(tokens, index+2) {
				bodyStart = typeScriptLexTypeAliasBody(
					tokens, index+2, boundaries.statementEnd(index), delimiters,
				)
			}
		}
		bodyEnd, paired := delimiters.get(bodyStart)
		if bodyStart < 0 || !paired || bodyEnd <= bodyStart {
			continue
		}
		force := typeScriptLexBodyTouchesRecovery(
			tokens, bodyStart, bodyEnd, positions, recoveryLines,
		)
		if token.text == "class" && force {
			if definition, ok := javascriptLexClassDefinition(
				tokens, index, delimiters, positions,
			); ok {
				definitions = append(definitions, javascriptLexDefinition{
					definition: definition,
					strong:     true,
					force:      true,
				})
			}
		}
		definitions = append(definitions, typeScriptLexMembersInBody(
			source,
			tokens,
			delimiters,
			bodyStart,
			bodyEnd,
			positions,
			commentSpans,
			force,
		)...)
	}
	return definitions
}

func typeScriptLexTypeAliasBody(
	tokens []javascriptToken,
	start int,
	end int,
	delimiters javascriptDelimiterPairs,
) int {
	if end < start || end >= len(tokens) {
		end = min(len(tokens)-1, start+javascriptMaximumRecoveryLookahead)
	}
	equalSeen := false
	for index := start; index <= end && index < len(tokens); index++ {
		if tokens[index].text == "=" {
			equalSeen = true
			continue
		}
		if equalSeen && tokens[index].text == "{" {
			if bodyEnd, paired := delimiters.get(index); paired && bodyEnd > index {
				return index
			}
			return -1
		}
	}
	return -1
}

func typeScriptLexMembersInBody(
	source string,
	tokens []javascriptToken,
	delimiters javascriptDelimiterPairs,
	bodyStart int,
	bodyEnd int,
	positions javascriptSourcePositions,
	commentSpans []javascriptByteSpan,
	force bool,
) []javascriptLexDefinition {
	definitions := make([]javascriptLexDefinition, 0)
	for index := bodyStart + 1; index < bodyEnd; index++ {
		name := tokens[index].text
		if name != "abstract" && name != "accessor" ||
			!typeScriptLexContextualMemberStart(tokens, index, bodyStart) ||
			!typeScriptLexContextualMemberShape(tokens, index, bodyEnd) {
			if _, opener, delimiter := javascriptDelimiterKind(tokens[index].text); delimiter &&
				opener {
				if end, paired := delimiters.get(index); paired && end > index && end < bodyEnd {
					index = end
				}
			}
			continue
		}
		memberEnd, ownsScope := typeScriptLexContextualMemberScope(
			tokens, index, bodyEnd, delimiters,
		)
		memberStart := index
		for memberStart > bodyStart+1 && typeScriptLexMemberPrefix(tokens[memberStart-1].text) {
			memberStart--
		}
		line, column := positions.lineColumn(tokens[index].startOffset())
		scopeStartOffset := javascriptLexAttachedStart(
			source, tokens[memberStart].startOffset(), commentSpans,
		)
		scopeStart, _ := positions.lineColumn(scopeStartOffset)
		scopeEnd := line
		if ownsScope {
			scopeEnd, _ = positions.lineColumn(max(tokens[memberEnd].endOffset()-1, 0))
		}
		definitions = append(definitions, javascriptLexDefinition{
			definition: sourceDefinition{
				symbol: name, line: line, column: column,
				scopeStart: scopeStart, scopeEnd: max(line, scopeEnd), ownsScope: ownsScope,
			},
			strong: true,
			force:  force,
		})
	}
	return definitions
}

func typeScriptLexContextualMemberScope(
	tokens []javascriptToken,
	nameIndex int,
	bodyEnd int,
	delimiters javascriptDelimiterPairs,
) (int, bool) {
	if nameIndex < 0 || nameIndex >= len(tokens) || bodyEnd <= nameIndex ||
		bodyEnd >= len(tokens) {
		return nameIndex, false
	}
	cursor := nameIndex + 1
	for cursor < bodyEnd && (tokens[cursor].text == "?" || tokens[cursor].text == "!") {
		cursor++
	}
	if cursor < bodyEnd && tokens[cursor].text == "<" {
		genericEnd := typeScriptLexGenericArgumentEnd(tokens, cursor, delimiters)
		if genericEnd <= cursor || genericEnd >= bodyEnd {
			return nameIndex, false
		}
		cursor = genericEnd + 1
	}
	if cursor >= bodyEnd || tokens[cursor].text != "(" {
		return nameIndex, false
	}
	parameterEnd, paired := delimiters.get(cursor)
	if !paired || parameterEnd <= cursor || parameterEnd >= bodyEnd {
		return nameIndex, false
	}
	cursor = parameterEnd + 1
	if cursor < bodyEnd && tokens[cursor].text == "{" {
		if methodEnd, ok := delimiters.get(cursor); ok && methodEnd > cursor &&
			methodEnd <= bodyEnd {
			return methodEnd, true
		}
		return nameIndex, false
	}
	returnType := false
	for cursor < bodyEnd && cursor < nameIndex+javascriptMaximumRecoveryLookahead {
		switch tokens[cursor].text {
		case ":":
			returnType = true
		case ";", ",":
			return cursor, true
		case "{":
			methodEnd, ok := delimiters.get(cursor)
			if !ok || methodEnd <= cursor || methodEnd > bodyEnd {
				return nameIndex, false
			}
			previous := ""
			if cursor > 0 {
				previous = tokens[cursor-1].text
			}
			if !returnType || previous != ":" && previous != "|" && previous != "&" &&
				previous != "=>" && previous != "(" && previous != "," && previous != "<" {
				return methodEnd, true
			}
			cursor = methodEnd
		case "(", "[":
			if end, ok := delimiters.get(cursor); ok && end > cursor && end < bodyEnd {
				cursor = end
			}
		}
		cursor++
	}
	return nameIndex, false
}

func typeScriptLexBodyTouchesRecovery(
	tokens []javascriptToken,
	bodyStart int,
	bodyEnd int,
	positions javascriptSourcePositions,
	recoveryLines map[int]bool,
) bool {
	if len(recoveryLines) == 0 || bodyStart < 0 || bodyEnd <= bodyStart ||
		bodyEnd >= len(tokens) {
		return false
	}
	start, end := positions.lineSpan(
		tokens[bodyStart].startOffset(), tokens[bodyEnd].endOffset(),
	)
	for line := start; line <= end; line++ {
		if recoveryLines[line] {
			return true
		}
	}
	return false
}

func typeScriptLexContextualMemberShape(tokens []javascriptToken, nameIndex, bodyEnd int) bool {
	cursor := nameIndex + 1
	for cursor < bodyEnd && (tokens[cursor].text == "?" || tokens[cursor].text == "!") {
		cursor++
	}
	if cursor >= bodyEnd {
		return true
	}
	switch tokens[cursor].text {
	case ":", "=", ";", "(", "<":
		return true
	default:
		return tokens[cursor].startsLine()
	}
}

func typeScriptLexContextualMemberStart(
	tokens []javascriptToken,
	index int,
	bodyStart int,
) bool {
	if index <= bodyStart || index >= len(tokens) {
		return false
	}
	previous := tokens[index-1].text
	switch previous {
	case ":", "=", "=>", "|", "&", "<", ",", "(", "[", ".", "?.", "?",
		"extends", "implements", "return", "as", "satisfies":
		return false
	case "{", "}", ";":
		return true
	}
	return tokens[index].startsLine() || typeScriptLexMemberPrefix(previous)
}

func typeScriptLexMemberPrefix(token string) bool {
	switch token {
	case "abstract", "accessor", "async", "declare", "get", "override", "private",
		"protected", "public", "readonly", "set", "static":
		return true
	default:
		return false
	}
}

func typeScriptLexPlausibleDeclarationStart(tokens []javascriptToken, index int) bool {
	if index < 0 || index >= len(tokens) {
		return false
	}
	if index == 0 || tokens[index].startsLine() {
		return true
	}
	switch tokens[index-1].text {
	case ";", "{", "}", "declare", "default", "export", "global":
		return true
	default:
		return false
	}
}

func typeScriptLexTypeAliasHasEquals(tokens []javascriptToken, start int) bool {
	limit := min(len(tokens), start+javascriptMaximumRecoveryLookahead)
	angleDepth := 0
	for index := start; index < limit; index++ {
		switch tokens[index].text {
		case "<":
			angleDepth++
		case ">":
			angleDepth = max(0, angleDepth-1)
		case ">>":
			angleDepth = max(0, angleDepth-2)
		case ">>>":
			angleDepth = max(0, angleDepth-3)
		case "=":
			if angleDepth == 0 {
				return true
			}
		case ";", "{", "}":
			if angleDepth == 0 {
				return false
			}
		}
	}
	return false
}

func typeScriptLexDeclarationBody(
	tokens []javascriptToken,
	start int,
	delimiters javascriptDelimiterPairs,
) int {
	limit := min(len(tokens), start+javascriptMaximumRecoveryLookahead)
	angleDepth := 0
	for index := start; index < limit; index++ {
		switch tokens[index].text {
		case "<":
			angleDepth++
		case ">":
			angleDepth = max(0, angleDepth-1)
		case ">>":
			angleDepth = max(0, angleDepth-2)
		case ">>>":
			angleDepth = max(0, angleDepth-3)
		case "{":
			if angleDepth > 0 {
				if end, paired := delimiters.get(index); paired && end > index {
					index = end
				}
				continue
			}
			if end, paired := delimiters.get(index); paired && end > index {
				return index
			}
			return -1
		case ";":
			if angleDepth == 0 {
				return -1
			}
		}
	}
	return -1
}

func typeScriptLexReferenceImports(
	source string,
	tokens []javascriptToken,
	commentSpans []javascriptByteSpan,
) []javascriptLineSpan {
	firstToken := len(source)
	if len(tokens) > 0 {
		firstToken = tokens[0].startOffset()
	}
	positions := javascriptSourcePositions{source: source, lineStarts: javascriptLineStarts(source)}
	imports := make([]javascriptLineSpan, 0)
	for _, span := range commentSpans {
		if span.start >= firstToken || span.start < 0 || span.end > len(source) {
			break
		}
		comment := source[span.start:span.end]
		if !javascriptTypeScriptReferenceDirective(comment) &&
			!javascriptTypeScriptAMDDependencyDirective(comment) {
			continue
		}
		start, end := positions.lineSpan(span.start, span.end)
		imports = append(imports, javascriptLineSpan{start: start, end: end})
	}
	return imports
}

func typeScriptLexGenericCallTypeImports(
	source string,
	tokens []javascriptToken,
	delimiters javascriptDelimiterPairs,
) []javascriptLineSpan {
	positions := javascriptSourcePositions{source: source, lineStarts: javascriptLineStarts(source)}
	imports := make([]javascriptLineSpan, 0)
	for index, token := range tokens {
		if token.text != "import" || !typeScriptLexImportInsideGenericCall(
			tokens, index, delimiters,
		) {
			continue
		}
		endIndex, ok := typeScriptLexStaticImportCall(tokens, index, delimiters)
		if !ok {
			continue
		}
		start, _ := positions.lineColumn(token.startOffset())
		end, _ := positions.lineColumn(max(tokens[endIndex].endOffset()-1, token.startOffset()))
		imports = append(imports, javascriptLineSpan{start: start, end: end})
	}
	return imports
}

func typeScriptLexGenericCallImportsFromSource(
	source string,
	commentSpans []javascriptByteSpan,
	literalSpans []javascriptByteSpan,
) []javascriptLineSpan {
	tokens := tokenizeJavaScript(source, commentSpans, literalSpans, 0)
	if len(tokens) == 0 {
		return nil
	}
	return typeScriptLexGenericCallTypeImports(
		source, tokens, javascriptMatchDelimiters(tokens),
	)
}

func typeScriptLexStaticImportCall(
	tokens []javascriptToken,
	index int,
	delimiters javascriptDelimiterPairs,
) (int, bool) {
	if index < 0 || index+2 >= len(tokens) || tokens[index].text != "import" ||
		tokens[index+1].text != "(" {
		return 0, false
	}
	closeIndex, paired := delimiters.get(index + 1)
	if !paired || closeIndex <= index+1 || closeIndex >= len(tokens) ||
		!tokens[index+2].literal() {
		return 0, false
	}
	if closeIndex == index+3 {
		return closeIndex, true
	}
	if index+4 >= closeIndex || tokens[index+3].text != "," ||
		tokens[index+4].text != "{" {
		return 0, false
	}
	objectEnd, objectPaired := delimiters.get(index + 4)
	return closeIndex, objectPaired && objectEnd == closeIndex-1
}

func typeScriptLexImportInsideGenericCall(
	tokens []javascriptToken,
	importIndex int,
	delimiters javascriptDelimiterPairs,
) bool {
	minimum := max(0, importIndex-javascriptMaximumRecoveryLookahead)
	for open := importIndex - 1; open >= minimum; open-- {
		if tokens[open].text == ";" || tokens[open].text == "{" || tokens[open].text == "}" {
			return false
		}
		if tokens[open].text != "<" || open == 0 ||
			!typeScriptLexGenericCalleeEnd(tokens[open-1].text) {
			continue
		}
		depth := 0
		limit := min(len(tokens), open+javascriptMaximumRecoveryLookahead*2)
		for index := open; index < limit; index++ {
			if _, opener, delimiter := javascriptDelimiterKind(tokens[index].text); delimiter &&
				!opener && delimiters.at(index) < open {
				return false
			}
			switch tokens[index].text {
			case "<":
				depth++
			case ">":
				depth--
			case ">>":
				depth -= 2
			case ">>>":
				depth -= 3
			case ";":
				if depth > 0 {
					return false
				}
			}
			if depth != 0 {
				continue
			}
			next := index + 1
			if next < len(tokens) && tokens[next].text == "?." {
				next++
			}
			return importIndex > open && importIndex < index &&
				next < len(tokens) && tokens[next].text == "("
		}
	}
	return false
}

func typeScriptLexVariableTypeEnd(
	tokens []javascriptToken,
	start int,
	delimiters javascriptDelimiterPairs,
) int {
	if start < 0 || start >= len(tokens) {
		return min(max(start, 0), len(tokens))
	}
	limit := min(len(tokens), start+typeScriptRecoveryChunkTokenLimit)
	for index := start; index < limit; index++ {
		if (tokens[index].text == "(" || tokens[index].text == "[" ||
			tokens[index].text == "{") && delimiters.at(index) > index {
			index = delimiters.at(index)
			continue
		}
		if tokens[index].text == "<" {
			if end := typeScriptLexGenericArgumentEnd(tokens, index, delimiters); end > index {
				index = end
				continue
			}
		}
		switch tokens[index].text {
		case "=", ",", ";", ")", "]", "}":
			return index
		}
		if index > start && tokens[index].startsLine() &&
			javascriptHardDeclarationToken(tokens[index].text) {
			return index
		}
	}
	return limit
}

func typeScriptLexGenericCalleeEnd(token string) bool {
	return javascriptSourceName(token) || token == ")" || token == "]" ||
		token == ">" || token == ">>" || token == ">>>"
}

func typeScriptLexGenericArgumentEnd(
	tokens []javascriptToken,
	start int,
	delimiters javascriptDelimiterPairs,
) int {
	if !typeScriptLexGenericArgumentStart(tokens, start) {
		return -1
	}
	depth := 1
	limit := min(len(tokens), start+typeScriptRecoveryChunkTokenLimit)
	for index := start + 1; index < limit; index++ {
		if (tokens[index].text == "(" || tokens[index].text == "[" ||
			tokens[index].text == "{") && delimiters.at(index) > index {
			index = delimiters.at(index)
			continue
		}
		switch tokens[index].text {
		case "<":
			depth++
		case ">":
			depth--
		case ">>":
			depth -= 2
		case ">>>":
			depth -= 3
		case "=", ";":
			if depth == 1 {
				return -1
			}
		}
		if depth <= 0 {
			return index
		}
	}
	return -1
}

func typeScriptLexGenericArgumentStart(tokens []javascriptToken, start int) bool {
	if start <= 0 || start >= len(tokens) || tokens[start].text != "<" {
		return false
	}
	calleeIndex := start - 1
	if tokens[calleeIndex].text == "?." {
		calleeIndex--
	}
	return calleeIndex >= 0 && typeScriptLexGenericCalleeEnd(tokens[calleeIndex].text)
}

func filterTypeScriptRecoveryDefinitions(
	source string,
	definitions []sourceDefinition,
	recoveryLines map[int]bool,
	lineStarts []int,
) []sourceDefinition {
	if len(definitions) == 0 || len(recoveryLines) == 0 {
		return definitions
	}
	filtered := definitions[:0]
	for _, definition := range definitions {
		onRecoveryLine := recoveryLines[definition.line]
		if colonOffset := typeScriptDefinitionContextualMemberColonOffset(
			source, definition, lineStarts,
		); colonOffset >= 0 {
			colonLine := sort.Search(len(lineStarts), func(index int) bool {
				return lineStarts[index] > colonOffset
			})
			if onRecoveryLine || recoveryLines[colonLine] {
				continue
			}
		}
		if !onRecoveryLine ||
			!typeScriptRecoveryPhantomKeyword(definition.symbol) ||
			definition.line < 1 || definition.line > len(lineStarts) || definition.column < 1 {
			filtered = append(filtered, definition)
			continue
		}
		start := lineStarts[definition.line-1] + definition.column - 1
		end := start + len(definition.symbol)
		if start < 0 || end > len(source) || source[start:end] != definition.symbol {
			filtered = append(filtered, definition)
		}
	}
	return filtered
}

func typeScriptDefinitionContextualMemberColonOffset(
	source string,
	definition sourceDefinition,
	lineStarts []int,
) int {
	if definition.line < 1 || definition.line > len(lineStarts) || definition.column < 1 {
		return -1
	}
	start := lineStarts[definition.line-1] + definition.column - 1
	if start < 0 || start > len(source) {
		return -1
	}
	cursor := start
	for cursor > 0 && typeScriptRecoveryWhitespace(source[cursor-1]) {
		cursor--
	}
	if cursor == 0 || source[cursor-1] != ':' {
		return -1
	}
	colonOffset := cursor - 1
	cursor = colonOffset
	for cursor > 0 && typeScriptRecoveryWhitespace(source[cursor-1]) {
		cursor--
	}
	wordEnd := cursor
	for cursor > 0 && source[cursor-1] >= 'a' && source[cursor-1] <= 'z' {
		cursor--
	}
	word := source[cursor:wordEnd]
	if word == "abstract" || word == "accessor" {
		return colonOffset
	}
	return -1
}

func typeScriptRecoveryWhitespace(character byte) bool {
	return character == ' ' || character == '\t' || character == '\r' || character == '\n'
}

func typeScriptRecoveryPhantomKeyword(symbol string) bool {
	if strings.HasPrefix(symbol, "#") {
		return false
	}
	switch symbol {
	case "abstract", "accessor", "class", "const", "declare", "enum", "export",
		"function", "import", "interface", "let", "module", "namespace", "override",
		"private", "protected", "public", "readonly", "static", "type", "using", "var":
		return true
	default:
		return false
	}
}
