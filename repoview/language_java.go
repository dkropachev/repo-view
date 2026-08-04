package repoview

import (
	"sort"
	"strings"
	"unicode/utf8"
)

type javaLanguage struct {
	analysis *javaSourceAnalysis
	languageDefinition
}

type javaSourceAnalysis struct {
	tree *javaSyntaxTree

	source         string
	definitions    []sourceDefinition
	scopes         []javaLineScope
	imports        []javaLineSpan
	commentSpans   []javaByteSpan
	stringSpans    []javaByteSpan
	recoverySpans  []javaByteSpan
	recoveryPrefix []int
	lineStarts     []int
	lines          []string
	lineSnapshot   []string
	lexed          javaLexResult
	lineCount      int
}

func newJavaLanguage() javaLanguage {
	return javaLanguage{languageDefinition: newLanguageDefinition(
		"java", nil, nil, nil, commentStyleCLike, false,
	)}
}

func registerJavaLanguage(registry map[string]languageBackend) {
	registerLanguage(registry, newJavaLanguage(), ".java")
}

func (j javaLanguage) prepareSource(lines []string) languageBackend {
	if len(lines) == 0 {
		j.analysis = nil
		return j
	}
	j.analysis = analyzeJavaSource(strings.Join(lines, "\n"), len(lines))
	j.analysis.lines = lines
	j.analysis.lineSnapshot = append([]string(nil), lines...)
	return j
}

func (j javaLanguage) sourceAnalysis(lines []string) *javaSourceAnalysis {
	if len(lines) == 0 {
		return nil
	}
	if j.analysis != nil && javaSameLineStorage(j.analysis.lines, lines) &&
		javaSameLines(j.analysis.lineSnapshot, lines) {
		return j.analysis
	}
	return j.analysisForSource(strings.Join(lines, "\n"), len(lines))
}

func (j javaLanguage) analysisForSource(source string, lineCount int) *javaSourceAnalysis {
	if j.analysis != nil && j.analysis.source == source && j.analysis.lineCount == lineCount {
		return j.analysis
	}
	return analyzeJavaSource(source, lineCount)
}

func javaSameLineStorage(first, second []string) bool {
	return len(first) == len(second) && len(first) > 0 && &first[0] == &second[0]
}

func javaSameLines(first, second []string) bool {
	if len(first) != len(second) {
		return false
	}
	for index := range first {
		if first[index] != second[index] {
			return false
		}
	}
	return true
}

func analyzeJavaSource(source string, lineCount int) *javaSourceAnalysis {
	analysis := &javaSourceAnalysis{
		source:     source,
		lexed:      lexJava(source),
		lineStarts: javaLineStarts(source),
		lineCount:  max(lineCount, 1),
	}
	if !analysis.lexed.truncated && !analysis.lexed.translatedEscapes &&
		len(source) <= javaMaximumConcreteParseBytes &&
		analysis.lexed.lexicalUnits <= javaMaximumConcreteTokens &&
		javaConcreteDelimiterDepthEligible(analysis.lexed.tokens) {
		analysis.tree, _ = parseJavaSyntaxWithinBudget(source)
	}

	attached := javaSyntaxAttachedStarts(source, analysis.tree)
	errorContext := javaSyntaxErrorContexts(analysis.tree)
	analysis.recoverySpans = javaSyntaxErrorSpans(analysis.tree, len(source))
	analysis.recoveryPrefix = javaRecoveryLinePrefix(
		source, analysis.lineStarts, analysis.lineCount, analysis.recoverySpans,
	)

	treeDefinitions := javaTreeDefinitions(
		source, analysis.lineCount, analysis.tree, attached, errorContext,
	)
	treeScopes := javaTreeScopes(
		source, analysis.lineCount, analysis.tree, attached, errorContext,
	)
	treeImports := javaTreeImports(
		source, analysis.lineCount, analysis.tree, attached, errorContext,
	)
	lexical := analyzeJavaLexically(source, analysis.lineCount, analysis.lexed)
	if analysis.lexed.truncated {
		gap := analyzeJavaStreamedGap(
			source, analysis.lineCount, analysis.lexed,
		)
		lexical.definitions = sortUniqueJavaTreeDefinitions(append(
			gap.definitions, lexical.definitions...,
		))
		if len(gap.scopes) > 0 {
			correctedEnds := make(map[int]int, len(gap.scopes))
			for _, scope := range gap.scopes {
				if end, exists := correctedEnds[scope.start]; !exists || scope.end < end {
					correctedEnds[scope.start] = scope.end
				}
			}
			uncorrected := lexical.scopes[:0]
			for _, scope := range lexical.scopes {
				if correctedEnd, corrected := correctedEnds[scope.start]; !corrected || scope.end <= correctedEnd {
					uncorrected = append(uncorrected, scope)
				}
			}
			lexical.scopes = uncorrected
		}
		lexical.scopes = normalizeJavaLineScopes(append(
			lexical.scopes, gap.scopes...,
		), analysis.lineCount)
		lexical.imports = normalizeJavaLineSpans(append(
			lexical.imports, gap.imports...,
		), analysis.lineCount)
	}
	if analysis.tree != nil {
		opaqueSpans := normalizeJavaSpans(append(
			append([]javaByteSpan(nil), analysis.lexed.commentSpans...),
			analysis.lexed.stringSpans...,
		))
		treeDefinitions = filterJavaDefinitionsOutsideSpans(
			source, analysis.lineStarts, treeDefinitions, opaqueSpans,
		)
		treeDefinitions = filterJavaDefinitionsWithInvalidHeaders(
			analysis.lineStarts, treeDefinitions, lexical.invalidDefinitionHeader,
		)
		treeScopes = filterJavaScopesOutsideSpans(
			source, analysis.lineStarts, treeScopes, opaqueSpans,
		)
		treeImports = filterJavaImportsOutsideSpans(
			source, analysis.lineStarts, treeImports, opaqueSpans,
		)
		treeImports = filterJavaImportsWithInvalidHeaders(
			treeImports, lexical.invalidImportHeaders,
		)
	}
	lexicalAuthoritative := analysis.tree == nil || analysis.lexed.translatedEscapes
	analysis.definitions = mergeJavaDefinitions(
		treeDefinitions, lexical.definitions, lexicalAuthoritative, analysis.recoveryPrefix,
	)
	authoritativeScopes := make([]javaLineScope, 0)
	definitionSeen := make(map[javaDefinitionIdentity]struct{}, len(analysis.definitions))
	for _, definition := range analysis.definitions {
		definitionSeen[javaDefinitionIdentity{
			symbol: definition.symbol, line: definition.line, column: definition.column,
		}] = struct{}{}
	}
	for _, definition := range lexical.authoritativeDefinitions {
		identity := javaDefinitionIdentity{
			symbol: definition.symbol, line: definition.line, column: definition.column,
		}
		if _, exists := definitionSeen[identity]; exists {
			continue
		}
		definitionSeen[identity] = struct{}{}
		analysis.definitions = append(analysis.definitions, definition)
		if definition.ownsScope {
			authoritativeScopes = append(authoritativeScopes, javaLineScope{
				start: definition.scopeStart, end: definition.scopeEnd,
			})
		}
	}
	analysis.definitions = sortUniqueJavaTreeDefinitions(analysis.definitions)
	analysis.scopes = mergeJavaScopes(
		treeScopes, lexical.scopes, lexicalAuthoritative, analysis.recoveryPrefix,
		analysis.lineCount,
	)
	analysis.scopes = normalizeJavaLineScopes(append(
		analysis.scopes, authoritativeScopes...,
	), analysis.lineCount)
	analysis.imports = mergeJavaImports(
		treeImports, lexical.imports, lexicalAuthoritative, analysis.recoveryPrefix,
		analysis.lineCount,
	)

	treeComments, treeStrings := javaSyntaxMasks(source, analysis.tree)
	if analysis.tree == nil {
		// Tree-free analyses own their lexical result, so the final masks can
		// safely reuse its immutable spans without duplicating large fallback
		// inputs. Unicode translation also always takes this path because the
		// concrete parser sees only raw escape spellings.
		analysis.commentSpans = analysis.lexed.commentSpans
		analysis.stringSpans = analysis.lexed.stringSpans
	} else {
		analysis.commentSpans = normalizeJavaSpans(append(
			append([]javaByteSpan(nil), treeComments...),
			analysis.lexed.commentSpans...,
		))
		if analysis.tree != nil && len(analysis.recoverySpans) == 0 {
			analysis.stringSpans = treeStrings
		} else {
			analysis.stringSpans = normalizeJavaSpans(append(
				append([]javaByteSpan(nil), treeStrings...),
				analysis.lexed.stringSpans...,
			))
		}
	}
	return analysis
}

func (j javaLanguage) definitionSymbol(line string) (string, bool) {
	for _, definition := range j.sourceDefinitions([]string{line}) {
		if definition.line == 1 {
			return definition.symbol, true
		}
	}
	return "", false
}

func (j javaLanguage) sourceDefinitions(lines []string) []sourceDefinition {
	analysis := j.sourceAnalysis(lines)
	if analysis == nil {
		return nil
	}
	// Definitions are cached as part of the immutable prepared analysis. Do not
	// let an internal caller retain and mutate that cache through the returned
	// slice.
	return append([]sourceDefinition(nil), analysis.definitions...)
}

func mergeJavaDefinitions(
	treeDefinitions, lexicalDefinitions []sourceDefinition,
	lexicalOnly bool,
	recoveryPrefix []int,
) []sourceDefinition {
	if lexicalOnly {
		return sortUniqueJavaTreeDefinitions(append(
			[]sourceDefinition(nil), lexicalDefinitions...,
		))
	}
	definitions := append([]sourceDefinition(nil), treeDefinitions...)
	seen := make(map[javaDefinitionIdentity]int, len(definitions))
	for index, definition := range definitions {
		seen[javaDefinitionIdentity{
			symbol: definition.symbol, line: definition.line, column: definition.column,
		}] = index
	}
	for _, lexical := range lexicalDefinitions {
		identity := javaDefinitionIdentity{
			symbol: lexical.symbol, line: lexical.line, column: lexical.column,
		}
		if index, exists := seen[identity]; exists {
			if javaDefinitionTouchesRecovery(lexical, recoveryPrefix) {
				definitions[index] = lexical
			}
			continue
		}
		if !javaDefinitionTouchesRecovery(lexical, recoveryPrefix) {
			continue
		}
		seen[identity] = len(definitions)
		definitions = append(definitions, lexical)
	}
	return sortUniqueJavaTreeDefinitions(definitions)
}

func javaDefinitionTouchesRecovery(
	definition sourceDefinition,
	recoveryPrefix []int,
) bool {
	if javaLineRangeTouchesRecovery(definition.line, definition.line, recoveryPrefix) {
		return true
	}
	return definition.ownsScope && javaLineRangeTouchesRecovery(
		definition.scopeStart, definition.scopeEnd, recoveryPrefix,
	)
}

func mergeJavaScopes(
	treeScopes, lexicalScopes []javaLineScope,
	lexicalOnly bool,
	recoveryPrefix []int,
	lineCount int,
) []javaLineScope {
	if lexicalOnly {
		return normalizeJavaLineScopes(append([]javaLineScope(nil), lexicalScopes...), lineCount)
	}
	scopes := append([]javaLineScope(nil), treeScopes...)
	for _, scope := range lexicalScopes {
		if javaLineRangeTouchesRecovery(scope.start, scope.end, recoveryPrefix) {
			scopes = append(scopes, scope)
		}
	}
	return normalizeJavaLineScopes(scopes, lineCount)
}

func mergeJavaImports(
	treeImports, lexicalImports []javaLineSpan,
	lexicalOnly bool,
	recoveryPrefix []int,
	lineCount int,
) []javaLineSpan {
	if lexicalOnly {
		return normalizeJavaLineSpans(append([]javaLineSpan(nil), lexicalImports...), lineCount)
	}
	imports := append([]javaLineSpan(nil), treeImports...)
	for _, span := range lexicalImports {
		if javaLineRangeTouchesRecovery(span.start, span.end, recoveryPrefix) {
			imports = append(imports, span)
		}
	}
	return normalizeJavaLineSpans(imports, lineCount)
}

func javaLineRangeTouchesRecovery(start, end int, recoveryPrefix []int) bool {
	if start < 1 || end < start || end >= len(recoveryPrefix) {
		return false
	}
	return recoveryPrefix[end]-recoveryPrefix[start-1] > 0
}

func javaRecoveryLinePrefix(
	source string,
	lineStarts []int,
	lineCount int,
	spans []javaByteSpan,
) []int {
	if len(spans) == 0 {
		return nil
	}
	lineCount = max(lineCount, 1)
	prefix := make([]int, lineCount+2)
	positions := javaSourcePositions{source: source, lineStarts: lineStarts}
	for _, span := range spans {
		start, end := positions.lineSpan(span.start, span.end)
		start = max(1, min(start, lineCount))
		end = max(start, min(end, lineCount))
		prefix[start]++
		if end+1 < len(prefix) {
			prefix[end+1]--
		}
	}
	active := 0
	for line := 1; line <= lineCount; line++ {
		active += prefix[line]
		prefix[line] = prefix[line-1]
		if active > 0 {
			prefix[line]++
		}
	}
	return prefix[:lineCount+1]
}

func filterJavaDefinitionsOutsideSpans(
	source string,
	lineStarts []int,
	definitions []sourceDefinition,
	opaque []javaByteSpan,
) []sourceDefinition {
	filtered := definitions[:0]
	for _, definition := range definitions {
		if definition.line < 1 || definition.line > len(lineStarts) || definition.column < 1 {
			continue
		}
		start := lineStarts[definition.line-1] + definition.column - 1
		// A normalized qualified module symbol may omit comments that occur
		// between its source tokens, so its display length is not a source byte
		// range. The first byte is sufficient to reject tree artifacts whose
		// declaration name begins inside a lexical comment or literal.
		end := min(start+1, len(source))
		if start < 0 || end > len(source) || start >= end ||
			javaByteRangeIntersects(start, end, opaque) {
			continue
		}
		filtered = append(filtered, definition)
	}
	return filtered
}

// filterJavaDefinitionsWithInvalidHeaders applies the lexical header barrier
// to concrete declarations recovered beside or above a nested ERROR node. A
// false entry is keyed by the declaration name's raw byte offset, so malformed
// bodies remain irrelevant and valid literal initializers stay visible.
func filterJavaDefinitionsWithInvalidHeaders(
	lineStarts []int,
	definitions []sourceDefinition,
	invalid map[int]struct{},
) []sourceDefinition {
	if len(invalid) == 0 {
		return definitions
	}
	filtered := definitions[:0]
	for _, definition := range definitions {
		if definition.line < 1 || definition.line > len(lineStarts) || definition.column < 1 {
			continue
		}
		offset := lineStarts[definition.line-1] + definition.column - 1
		if _, rejected := invalid[offset]; rejected {
			continue
		}
		filtered = append(filtered, definition)
	}
	return filtered
}

func filterJavaScopesOutsideSpans(
	source string,
	lineStarts []int,
	scopes []javaLineScope,
	opaque []javaByteSpan,
) []javaLineScope {
	filtered := scopes[:0]
	for _, scope := range scopes {
		start, end, ok := javaPhysicalLineByteRange(source, lineStarts, scope.start, scope.end)
		if !ok || javaByteRangeCovered(start, end, opaque) {
			continue
		}
		filtered = append(filtered, scope)
	}
	return filtered
}

func filterJavaImportsOutsideSpans(
	source string,
	lineStarts []int,
	imports []javaLineSpan,
	opaque []javaByteSpan,
) []javaLineSpan {
	filtered := imports[:0]
	for _, span := range imports {
		start, end, ok := javaPhysicalLineByteRange(source, lineStarts, span.start, span.end)
		if !ok || javaByteRangeCovered(start, end, opaque) {
			continue
		}
		filtered = append(filtered, span)
	}
	return filtered
}

func filterJavaImportsWithInvalidHeaders(
	imports []javaLineSpan,
	invalid []javaLineSpan,
) []javaLineSpan {
	if len(invalid) == 0 {
		return imports
	}
	filtered := imports[:0]
	invalidIndex := 0
	for _, span := range imports {
		for invalidIndex < len(invalid) && invalid[invalidIndex].end < span.start {
			invalidIndex++
		}
		rejected := invalidIndex < len(invalid) &&
			invalid[invalidIndex].start <= span.end
		if !rejected {
			filtered = append(filtered, span)
		}
	}
	return filtered
}

func javaPhysicalLineByteRange(
	source string,
	lineStarts []int,
	startLine, endLine int,
) (int, int, bool) {
	if startLine < 1 || endLine < startLine || startLine > len(lineStarts) ||
		endLine > len(lineStarts) {
		return 0, 0, false
	}
	start := lineStarts[startLine-1]
	end := len(source)
	if endLine < len(lineStarts) {
		end = lineStarts[endLine]
	}
	return start, max(start, end), true
}

func javaByteRangeIntersects(start, end int, spans []javaByteSpan) bool {
	index := sort.Search(len(spans), func(index int) bool { return spans[index].end > start })
	return index < len(spans) && spans[index].start < end
}

func javaByteRangeCovered(start, end int, spans []javaByteSpan) bool {
	if end <= start {
		return false
	}
	index := sort.Search(len(spans), func(index int) bool { return spans[index].end > start })
	return index < len(spans) && spans[index].start <= start && spans[index].end >= end
}

func (j javaLanguage) enclosingScope(lines []string, lineNo int) (int, int) {
	if lineNo < 1 || lineNo > len(lines) {
		return lineNo, lineNo
	}
	analysis := j.sourceAnalysis(lines)
	if analysis == nil {
		return lineNo, lineNo
	}
	bestStart, bestEnd := 0, 0
	for _, scope := range analysis.scopes {
		if lineNo < scope.start || lineNo > scope.end {
			continue
		}
		if bestStart == 0 || scope.end-scope.start < bestEnd-bestStart ||
			scope.end-scope.start == bestEnd-bestStart && scope.start > bestStart {
			bestStart, bestEnd = scope.start, scope.end
		}
	}
	if bestStart == 0 {
		return lineNo, lineNo
	}
	return bestStart, bestEnd
}

func (j javaLanguage) navigationScope(lines []string, lineNo int) (int, int) {
	if lineNo < 1 || lineNo > len(lines) {
		return lineNo, lineNo
	}
	analysis := j.sourceAnalysis(lines)
	if analysis == nil {
		return lineNo, lineNo
	}
	bestStart, bestEnd, bestLine := 0, 0, 0
	bestBefore := false
	for _, definition := range analysis.definitions {
		if !definition.ownsScope || lineNo < definition.scopeStart || lineNo > definition.scopeEnd {
			continue
		}
		size := definition.scopeEnd - definition.scopeStart
		before := definition.line <= lineNo
		if bestStart == 0 || before && !bestBefore || before == bestBefore &&
			(size < bestEnd-bestStart || size == bestEnd-bestStart &&
				(before && definition.line > bestLine || !before && definition.line < bestLine)) {
			bestStart, bestEnd, bestLine = definition.scopeStart, definition.scopeEnd, definition.line
			bestBefore = before
		}
	}
	if bestStart > 0 {
		return bestStart, bestEnd
	}
	return j.enclosingScope(lines, lineNo)
}

func (j javaLanguage) importRange(lines []string) (int, int, bool) {
	analysis := j.sourceAnalysis(lines)
	if analysis == nil {
		return 0, 0, false
	}
	start, end := 0, 0
	for _, span := range analysis.imports {
		if span.start < 1 || span.end < span.start || span.end > len(lines) {
			continue
		}
		if start == 0 || span.start < start {
			start = span.start
		}
		end = max(end, span.end)
	}
	return start, end, start > 0 && end >= start
}

func (j javaLanguage) cleanSource(source string, dropComments, _ bool) string {
	if !dropComments {
		return source
	}
	lines := strings.Split(source, "\n")
	return dropBlankArtifactLines(strings.Join(j.cleanSourceLines(lines, true, false), "\n"))
}

func (javaLanguage) stripComment(line string) string {
	input := newJavaUnicodeInput(line)
	return strings.TrimRight(
		maskJavaSearchSource(line, &input, true, false), " \t",
	)
}

func (j javaLanguage) cleanSourceLines(
	lines []string,
	dropComments, _ bool,
) []string {
	if len(lines) == 0 {
		return nil
	}
	if !dropComments {
		return append([]string(nil), lines...)
	}
	analysis := j.sourceAnalysis(lines)
	if analysis == nil {
		return append([]string(nil), lines...)
	}
	masked := strings.Split(maskJavaSearchSource(
		analysis.source, analysis.lexed.input, true, false,
	), "\n")
	for index := range masked {
		masked[index] = strings.TrimRight(masked[index], " \t")
	}
	return masked
}

func (javaLanguage) finalizeSourceSnippet(source string, dropComments, _ bool) string {
	if !dropComments {
		return source
	}
	return dropBlankArtifactLines(source)
}

func (j javaLanguage) ignoredSearchLines(
	lines []string,
	dropComments, _ bool,
) map[int]bool {
	ignored := make(map[int]bool)
	if len(lines) == 0 || !dropComments {
		return ignored
	}
	analysis := j.sourceAnalysis(lines)
	if analysis == nil {
		return ignored
	}
	masked := strings.Split(maskJavaSearchSource(
		analysis.source, analysis.lexed.input, true, false,
	), "\n")
	for index := range lines {
		if index < len(masked) && masked[index] != lines[index] &&
			strings.TrimSpace(masked[index]) == "" {
			ignored[index+1] = true
		}
	}
	return ignored
}

func (j javaLanguage) searchLines(lines []string, noComments, noStrings bool) []string {
	if len(lines) == 0 {
		return nil
	}
	if !noComments && !noStrings {
		return append([]string(nil), lines...)
	}
	analysis := j.sourceAnalysis(lines)
	if analysis == nil {
		return append([]string(nil), lines...)
	}
	return strings.Split(maskJavaSearchSource(
		analysis.source, analysis.lexed.input, noComments, noStrings,
	), "\n")
}

// maskJavaSearchSource masks exact streamed lexical regions instead of the
// bounded retained span slices used by structural recovery. This matters for
// Find options: after pathological numbers of comments or literals, a
// conservative overflow span must not hide executable code between them.
func maskJavaSearchSource(
	source string,
	input *javaUnicodeInput,
	maskComments, maskStrings bool,
) string {
	if source == "" || !maskComments && !maskStrings {
		return source
	}
	if input == nil || input.source != source {
		prepared := newJavaUnicodeInput(source)
		input = &prepared
	}
	masked := []byte(source)
	mask := func(span javaByteSpan) {
		start := max(0, min(span.start, len(masked)))
		end := max(start, min(span.end, len(masked)))
		for index := start; index < end; index++ {
			if masked[index] != '\n' && masked[index] != '\r' {
				masked[index] = ' '
			}
		}
	}
	streamJavaLexicalEventsFromInput(
		input,
		javaLexicalStreamOptions{comments: maskComments},
		func(event javaLexicalStreamEvent) bool {
			switch event.kind {
			case javaLexicalStreamToken:
			case javaLexicalStreamComment:
				if maskComments {
					mask(event.span)
				}
			case javaLexicalStreamOpaque:
				if maskStrings {
					mask(event.span)
				}
			}
			return true
		},
	)
	return string(masked)
}

func maskJavaSource(source string, spans []javaByteSpan) string {
	if len(source) == 0 || len(spans) == 0 {
		return source
	}
	masked := []byte(source)
	for _, span := range spans {
		start := max(0, min(span.start, len(masked)))
		end := max(start, min(span.end, len(masked)))
		for index := start; index < end; index++ {
			if masked[index] != '\n' && masked[index] != '\r' {
				masked[index] = ' '
			}
		}
	}
	return string(masked)
}

func (javaLanguage) countSymbolOccurrences(line, symbol string) int {
	if symbol == "" || len(symbol) > len(line) {
		return 0
	}
	// KMP keeps overlapping raw spellings observable (for example, "a.a"
	// occurs twice in "a.a.a") without repeatedly comparing a long identifier
	// against every byte of one larger identifier.
	var inlinePrefix [64]int
	var prefix []int
	if len(symbol) <= len(inlinePrefix) {
		prefix = inlinePrefix[:len(symbol)]
	} else {
		prefix = make([]int, len(symbol))
	}
	javaBuildRawSymbolPrefix(symbol, prefix)
	return javaCountSymbolOccurrencesWithPrefix(line, symbol, prefix)
}

func (javaLanguage) prepareSymbolOccurrenceCounter(symbol string) func(string) int {
	prefix := make([]int, len(symbol))
	javaBuildRawSymbolPrefix(symbol, prefix)
	return func(line string) int {
		return javaCountSymbolOccurrencesWithPrefix(line, symbol, prefix)
	}
}

func javaBuildRawSymbolPrefix(symbol string, prefix []int) {
	if len(prefix) < len(symbol) {
		return
	}
	for index, matched := 1, 0; index < len(symbol); index++ {
		for matched > 0 && symbol[index] != symbol[matched] {
			matched = prefix[matched-1]
		}
		if symbol[index] == symbol[matched] {
			matched++
		}
		prefix[index] = matched
	}
}

func javaCountSymbolOccurrencesWithPrefix(line, symbol string, prefix []int) int {
	if symbol == "" || len(symbol) > len(line) || len(prefix) < len(symbol) {
		return 0
	}
	translated := strings.Contains(line, `\u`)
	var startBoundaries, endBoundaries javaTranslatedBoundaryCursor
	if translated {
		input := newJavaUnicodeInput(line)
		startBoundaries = newJavaTranslatedBoundaryCursor(&input)
		endBoundaries = newJavaTranslatedBoundaryCursor(&input)
	}

	count := 0
	matched := 0
	for offset := range len(line) {
		for matched > 0 && line[offset] != symbol[matched] {
			matched = prefix[matched-1]
		}
		if line[offset] != symbol[matched] {
			continue
		}
		matched++
		if matched != len(symbol) {
			continue
		}

		position := offset + 1 - len(symbol)
		after := position + len(symbol)
		var beforeOK, afterOK bool
		if translated {
			start := startBoundaries.at(position)
			finish := endBoundaries.at(after)
			beforeOK = start.valid &&
				(position == 0 || !javaIdentifierContinueRune(start.before))
			afterOK = finish.valid &&
				(after == len(line) || !javaIdentifierContinueRune(finish.after))
		} else {
			before, _ := utf8.DecodeLastRuneInString(line[:position])
			beforeOK = position == 0 || !javaIdentifierContinueRune(before)
			afterRune, _ := utf8.DecodeRuneInString(line[after:])
			afterOK = after == len(line) || !javaIdentifierContinueRune(afterRune)
		}
		if beforeOK && afterOK {
			count++
		}
		matched = prefix[matched-1]
	}
	return count
}

type javaTranslatedBoundary struct {
	before rune
	after  rune
	valid  bool
}

type javaTranslatedBoundaryCursor struct {
	input        *javaUnicodeInput
	cursor       javaInputCursor
	current      rune
	previous     rune
	currentStart int
	currentEnd   int
	previousEnd  int
	currentOK    bool
}

func newJavaTranslatedBoundaryCursor(
	input *javaUnicodeInput,
) javaTranslatedBoundaryCursor {
	boundary := javaTranslatedBoundaryCursor{input: input}
	if input == nil {
		return boundary
	}
	boundary.cursor = input.cursor(0, len(input.source))
	boundary.current, boundary.currentStart, boundary.currentEnd, _, boundary.currentOK =
		boundary.cursor.nextCodePoint()
	return boundary
}

func (boundary *javaTranslatedBoundaryCursor) at(offset int) javaTranslatedBoundary {
	if boundary == nil || boundary.input == nil || offset < 0 ||
		offset > len(boundary.input.source) {
		return javaTranslatedBoundary{}
	}
	for boundary.currentOK && boundary.currentEnd <= offset {
		boundary.previous = boundary.current
		boundary.previousEnd = boundary.currentEnd
		boundary.current, boundary.currentStart, boundary.currentEnd, _, boundary.currentOK =
			boundary.cursor.nextCodePoint()
	}
	beforeValid := offset == 0 || boundary.previousEnd == offset
	afterValid := offset == len(boundary.input.source) ||
		boundary.currentOK && boundary.currentStart == offset
	if !beforeValid || !afterValid {
		return javaTranslatedBoundary{}
	}
	return javaTranslatedBoundary{
		before: boundary.previous,
		after:  boundary.current,
		valid:  true,
	}
}

func (j javaLanguage) symbolOnLine(lines []string, lineNo int) (string, bool) {
	analysis := j.sourceAnalysis(lines)
	if analysis == nil || lineNo < 1 || lineNo > analysis.lineCount {
		return "", false
	}
	for _, definition := range analysis.definitions {
		if definition.line == lineNo {
			return definition.symbol, true
		}
	}
	recoveryLine := javaLineRangeTouchesRecovery(lineNo, lineNo, analysis.recoveryPrefix)
	if recoveryLine {
		if symbol, ok := javaLexicalSymbolOnLine(analysis, lineNo); ok {
			return symbol, true
		}
	}
	if symbol, ok := javaTreeSymbolOnLine(analysis, lineNo); ok {
		return symbol, true
	}
	if recoveryLine {
		return "", false
	}
	return javaLexicalSymbolOnLine(analysis, lineNo)
}

func (javaLanguage) authoritativeSymbolOnLine() {}

func javaTreeSymbolOnLine(
	analysis *javaSourceAnalysis,
	lineNo int,
) (string, bool) {
	if analysis == nil || !validateJavaSyntaxTree(analysis.tree, len(analysis.source)) {
		return "", false
	}
	lineStart, lineEnd, ok := javaPhysicalLineByteRange(
		analysis.source, analysis.lineStarts, lineNo, lineNo,
	)
	if !ok {
		return "", false
	}
	tree := analysis.tree
	errorContext := javaSyntaxErrorContexts(tree)

	for nodeIndex, node := range tree.nodes {
		if javaSyntaxNodeInError(errorContext, nodeIndex) {
			continue
		}
		var nameIndex int
		switch node.kind {
		case "method_invocation":
			nameIndex = javaLastDirectSyntaxChildOfKinds(tree, nodeIndex, "identifier")
		case "object_creation_expression":
			nameIndex = javaObjectCreationSyntaxName(tree, nodeIndex)
		default:
			continue
		}
		if symbol, found := javaSyntaxSymbolOnPhysicalLine(
			analysis, errorContext, nameIndex, lineStart, lineEnd,
		); found {
			return symbol, true
		}
	}

	for nodeIndex, node := range tree.nodes {
		if node.kind != "method_reference" ||
			javaSyntaxNodeInError(errorContext, nodeIndex) {
			continue
		}
		nameIndex := javaLastDirectSyntaxChildOfKinds(
			tree, nodeIndex, "identifier", "type_identifier",
		)
		if symbol, found := javaSyntaxSymbolOnPhysicalLine(
			analysis, errorContext, nameIndex, lineStart, lineEnd,
		); found {
			return symbol, true
		}
		for child := len(node.children) - 1; child >= 0; child-- {
			nameIndex = javaTypeSyntaxTerminalName(tree, node.children[child], 0)
			if symbol, found := javaSyntaxSymbolOnPhysicalLine(
				analysis, errorContext, nameIndex, lineStart, lineEnd,
			); found {
				return symbol, true
			}
		}
	}

	for nodeIndex, node := range tree.nodes {
		if javaSyntaxNodeInError(errorContext, nodeIndex) {
			continue
		}
		switch node.kind {
		case "field_access", "scoped_identifier", "scoped_type_identifier":
		default:
			continue
		}
		nameIndex := javaLastDirectSyntaxChildOfKinds(
			tree, nodeIndex, "identifier", "type_identifier",
		)
		if symbol, found := javaSyntaxSymbolOnPhysicalLine(
			analysis, errorContext, nameIndex, lineStart, lineEnd,
		); found {
			return symbol, true
		}
	}

	guardContext := javaSyntaxGuardContexts(tree, errorContext)
	for nodeIndex, node := range tree.nodes {
		if node.kind != "identifier" || nodeIndex >= len(guardContext) ||
			!guardContext[nodeIndex] {
			continue
		}
		if symbol, found := javaSyntaxSymbolOnPhysicalLine(
			analysis, errorContext, nodeIndex, lineStart, lineEnd,
		); found {
			return symbol, true
		}
	}

	for nodeIndex, node := range tree.nodes {
		if node.kind != "variable_declarator" ||
			javaSyntaxNodeInError(errorContext, nodeIndex) ||
			!javaValidSyntaxNodeIndex(tree, node.parent) ||
			tree.nodes[node.parent].kind != "local_variable_declaration" {
			continue
		}
		nameIndex := javaDirectChildOfKind(tree, nodeIndex, "identifier")
		if symbol, found := javaSyntaxSymbolOnPhysicalLine(
			analysis, errorContext, nameIndex, lineStart, lineEnd,
		); found {
			return symbol, true
		}
	}

	for nodeIndex, node := range tree.nodes {
		if node.kind != "identifier" {
			continue
		}
		if symbol, found := javaSyntaxSymbolOnPhysicalLine(
			analysis, errorContext, nodeIndex, lineStart, lineEnd,
		); found {
			return symbol, true
		}
	}
	return "", false
}

// javaSyntaxGuardContexts marks guard descendants in one preorder pass. The
// copied syntax tree guarantees that a node's parent precedes it, so nested
// switch guards do not require rescanning overlapping descendant subtrees.
func javaSyntaxGuardContexts(tree *javaSyntaxTree, errorContext []bool) []bool {
	if tree == nil || len(tree.nodes) == 0 {
		return nil
	}
	hasGuard := false
	for nodeIndex, node := range tree.nodes {
		if node.kind == "guard" && !javaSyntaxNodeInError(errorContext, nodeIndex) {
			hasGuard = true
			break
		}
	}
	if !hasGuard {
		return nil
	}
	contexts := make([]bool, len(tree.nodes))
	for nodeIndex, node := range tree.nodes {
		if !javaSyntaxNodeInError(errorContext, nodeIndex) && node.kind == "guard" {
			contexts[nodeIndex] = true
			continue
		}
		if javaValidSyntaxNodeIndex(tree, node.parent) {
			contexts[nodeIndex] = contexts[node.parent]
		}
	}
	return contexts
}

func javaLastDirectSyntaxChildOfKinds(
	tree *javaSyntaxTree,
	nodeIndex int,
	kinds ...string,
) int {
	if !javaValidSyntaxNodeIndex(tree, nodeIndex) {
		return -1
	}
	children := tree.nodes[nodeIndex].children
	for child := len(children) - 1; child >= 0; child-- {
		childIndex := children[child]
		if !javaValidSyntaxNodeIndex(tree, childIndex) {
			return -1
		}
		for _, kind := range kinds {
			if tree.nodes[childIndex].kind == kind {
				return childIndex
			}
		}
	}
	return -1
}

const javaMaximumSymbolSyntaxDepth = 64

func javaObjectCreationSyntaxName(tree *javaSyntaxTree, nodeIndex int) int {
	if !javaValidSyntaxNodeIndex(tree, nodeIndex) {
		return -1
	}
	nameIndex := -1
	for _, childIndex := range tree.nodes[nodeIndex].children {
		if !javaValidSyntaxNodeIndex(tree, childIndex) {
			return -1
		}
		switch tree.nodes[childIndex].kind {
		case "argument_list", "class_body":
			return nameIndex
		case "identifier", "type_identifier", "scoped_type_identifier",
			"generic_type", "annotated_type":
			if candidate := javaTypeSyntaxTerminalName(tree, childIndex, 0); candidate >= 0 {
				nameIndex = candidate
			}
		}
	}
	return nameIndex
}

func javaTypeSyntaxTerminalName(tree *javaSyntaxTree, nodeIndex, depth int) int {
	if !javaValidSyntaxNodeIndex(tree, nodeIndex) || depth > javaMaximumSymbolSyntaxDepth {
		return -1
	}
	node := tree.nodes[nodeIndex]
	if node.kind == "identifier" || node.kind == "type_identifier" {
		return nodeIndex
	}
	children := node.children
	for child := len(children) - 1; child >= 0; child-- {
		childIndex := children[child]
		if !javaValidSyntaxNodeIndex(tree, childIndex) {
			return -1
		}
		childKind := tree.nodes[childIndex].kind
		if childKind == "type_arguments" || childKind == "annotation_argument_list" {
			continue
		}
		if candidate := javaTypeSyntaxTerminalName(tree, childIndex, depth+1); candidate >= 0 {
			return candidate
		}
	}
	return -1
}

func javaSyntaxSymbolOnPhysicalLine(
	analysis *javaSourceAnalysis,
	errorContext []bool,
	nodeIndex, lineStart, lineEnd int,
) (string, bool) {
	if analysis == nil || !javaValidSyntaxNodeIndex(analysis.tree, nodeIndex) ||
		javaSyntaxNodeInError(errorContext, nodeIndex) {
		return "", false
	}
	node := analysis.tree.nodes[nodeIndex]
	if node.startByte < lineStart || node.startByte >= lineEnd ||
		!javaSyntaxRangeValid(node.startByte, node.endByte, len(analysis.source)) ||
		javaByteRangeIntersects(node.startByte, node.endByte, analysis.commentSpans) ||
		javaByteRangeIntersects(node.startByte, node.endByte, analysis.stringSpans) ||
		!javaIdentifierSourceName(analysis.source, node.startByte, node.endByte) {
		return "", false
	}
	return analysis.source[node.startByte:node.endByte], true
}

func javaLexicalSymbolOnLine(
	analysis *javaSourceAnalysis,
	lineNo int,
) (string, bool) {
	if analysis == nil || lineNo < 1 || lineNo > analysis.lineCount {
		return "", false
	}
	lineStart, lineEnd, ok := javaPhysicalLineByteRange(
		analysis.source, analysis.lineStarts, lineNo, lineNo,
	)
	if !ok {
		return "", false
	}
	tokens := analysis.lexed.tokens
	if javaStoredTokenGapIntersectsLine(tokens, lineStart, lineEnd) {
		// The retained lexer keeps the beginning and end of very large inputs.
		// Inspect must still distinguish real identifiers from numeric or opaque
		// spelling on a physical line in the omitted middle. Re-stream the source
		// with bounded local storage and reuse the ordinary lexical selector.
		tokens = javaStreamedTokensAroundLine(
			analysis.lexed.input, lineStart, lineEnd,
		)
		if symbol, ok := javaStreamedCallOnLine(
			analysis.lexed.input, lineStart, lineEnd,
		); ok {
			return symbol, true
		}
		if symbol, ok := javaStreamedNonCallSymbolOnLine(
			analysis.lexed.input, lineStart, lineEnd,
		); ok {
			return symbol, true
		}
	}
	delimiters := analyzeJavaDelimiters(tokens)
	for index, token := range tokens {
		if !javaTokenStartsOnPhysicalLine(token, lineStart, lineEnd) ||
			!javaTokenIsSourceName(token) || index+1 >= len(tokens) ||
			tokens[index+1].value != "(" ||
			javaLexicalAnnotationName(tokens, index) ||
			javaLexicalTokenIsNonCallKeyword(tokens, delimiters, index) ||
			javaLexicalNameBeginsPattern(tokens, delimiters, index) {
			continue
		}
		closeIndex := javaDelimiterMatch(delimiters, index+1)
		if closeIndex > index+1 {
			chainedCall := javaLexicalLastInvokedMemberOnLine(
				tokens, delimiters, closeIndex, lineStart, lineEnd,
			)
			if chainedCall >= 0 {
				return tokens[chainedCall].text, true
			}
		}
		return token.text, true
	}

	for index, token := range tokens {
		if token.value != "::" {
			continue
		}
		nameIndex := javaTokenAfterMethodReference(tokens, delimiters, index)
		if nameIndex >= 0 && javaTokenStartsOnPhysicalLine(
			tokens[nameIndex], lineStart, lineEnd,
		) && javaTokenIsSourceName(tokens[nameIndex]) {
			return tokens[nameIndex].text, true
		}
		if nameIndex >= 0 && tokens[nameIndex].value == "new" {
			qualifier := javaLexicalConstructorReferenceNameOnLine(
				tokens, delimiters, index, nameIndex, lineStart, lineEnd,
			)
			if qualifier >= 0 {
				return tokens[qualifier].text, true
			}
		}
	}

	for index, token := range tokens {
		if index == 0 || tokens[index-1].value != "." ||
			!javaTokenStartsOnPhysicalLine(token, lineStart, lineEnd) ||
			!javaTokenIsSourceName(token) ||
			javaLexicalMemberContinuesOnLine(
				tokens, delimiters, index, lineStart, lineEnd,
			) {
			continue
		}
		return token.text, true
	}

	for index, token := range tokens {
		if token.value != "when" || !javaLexicalCaseGuardKeyword(tokens, index) {
			continue
		}
		for candidate := index + 1; candidate < len(tokens) &&
			tokens[candidate].value != "->"; candidate++ {
			if javaTokenStartsOnPhysicalLine(
				tokens[candidate], lineStart, lineEnd,
			) && javaTokenIsSourceName(tokens[candidate]) &&
				!javaLexicalSymbolContextualKeyword(tokens, delimiters, candidate) {
				return tokens[candidate].text, true
			}
		}
	}

	for index, token := range tokens {
		if token.value == "var" && index+1 < len(tokens) &&
			javaTokenStartsOnPhysicalLine(token, lineStart, lineEnd) &&
			javaTokenStartsOnPhysicalLine(tokens[index+1], lineStart, lineEnd) &&
			javaTokenIsSourceName(tokens[index+1]) {
			return tokens[index+1].text, true
		}
	}

	for index, token := range tokens {
		if javaTokenStartsOnPhysicalLine(token, lineStart, lineEnd) &&
			javaTokenIsSourceName(token) &&
			!javaLexicalSymbolContextualKeyword(tokens, delimiters, index) {
			return token.text, true
		}
	}
	return "", false
}

type javaStreamedCallPhase uint8

const (
	javaStreamedCallArguments javaStreamedCallPhase = iota
	javaStreamedCallAfterInvocation
	javaStreamedCallArrayIndex
	javaStreamedCallAfterDot
	javaStreamedCallTypeArguments
	javaStreamedCallAfterMember
)

type javaStreamedCallState struct {
	result          javaToken
	member          javaToken
	depth           int
	annotationDepth int
	phase           javaStreamedCallPhase
	annotationState uint8
	active          bool
	started         bool
}

// javaStreamedCallOnLine preserves the selector's highest lexical priority on
// a physical line that intersects the retained-token gap. It never stores an
// argument list: once the first eligible invocation is found, scalar nesting
// tracks its closing delimiter and any invoked members in the following
// chain. The bounded history exists only to classify the invocation name.
func javaStreamedCallOnLine(
	input *javaUnicodeInput,
	lineStart, lineEnd int,
) (string, bool) {
	if input == nil || lineStart < 0 || lineEnd < lineStart ||
		lineEnd > len(input.source) {
		return "", false
	}

	const historyLimit = javaMaximumRecoveryHeaderTokens
	history := make([]javaToken, 0, historyLimit)
	historyNext := 0
	delimiterStack := make([]byte, 0, 32)
	activeWrappers := make([]byte, 0, 32)
	lineStarted := false
	state := javaStreamedCallState{}
	result := ""
	found := false
	annotationState := uint8(0) // 1: expect name, 2: name may continue or take args
	casePattern := false
	instanceofPending := false
	instanceofAngleDepth := 0
	instancePatternDepth := 0
	instanceAnnotationDepth := 0
	previousPatternName := false
	previousInstanceTypeName := false
	previousNonCallName := false
	previousToken := javaToken{}
	previousTokenValid := false
	statementStart := true
	appendHistory := func(token javaToken) {
		if len(history) < cap(history) {
			history = append(history, token)
			return
		}
		history[historyNext] = token
		historyNext = (historyNext + 1) % len(history)
	}
	historyTokens := func(extra javaToken) []javaToken {
		window := make([]javaToken, 0, len(history)+1)
		if len(history) < cap(history) || historyNext == 0 {
			window = append(window, history...)
		} else {
			window = append(window, history[historyNext:]...)
			window = append(window, history[:historyNext]...)
		}
		return append(window, extra)
	}
	finish := func() bool {
		result = state.result.text
		found = result != ""
		state.active = false
		return false
	}
	updateDelimiters := func(stack []byte, token javaToken) []byte {
		var open byte
		switch token.value {
		case "(":
			open = '('
		case "[":
			open = '['
		case "{":
			open = '{'
		}
		if open != 0 {
			if len(stack) < javaMaximumRecoveryHeaderTokens {
				stack = append(stack, open)
			}
			return stack
		}
		if len(stack) == 0 {
			return stack
		}
		matching := byte(0)
		switch token.value {
		case ")":
			matching = '('
		case "]":
			matching = '['
		case "}":
			matching = '{'
		}
		if matching == stack[len(stack)-1] {
			stack = stack[:len(stack)-1]
		}
		return stack
	}
	consumeActive := func(token javaToken) bool {
		if state.phase == javaStreamedCallAfterInvocation && len(activeWrappers) > 0 {
			activeWrappers = updateDelimiters(activeWrappers, token)
			return true
		}
		switch state.phase {
		case javaStreamedCallArguments:
			switch token.value {
			case "(":
				state.depth++
			case ")":
				state.depth--
				if state.depth <= 0 {
					state.phase = javaStreamedCallAfterInvocation
					state.depth = 0
				}
			}
			return true
		case javaStreamedCallAfterInvocation:
			switch token.value {
			case "[":
				state.phase = javaStreamedCallArrayIndex
				state.depth = 1
				return true
			case ".":
				state.phase = javaStreamedCallAfterDot
				return true
			default:
				return finish()
			}
		case javaStreamedCallArrayIndex:
			switch token.value {
			case "[":
				state.depth++
			case "]":
				state.depth--
				if state.depth <= 0 {
					state.phase = javaStreamedCallAfterInvocation
					state.depth = 0
				}
			}
			return true
		case javaStreamedCallAfterDot:
			if token.value == "<" {
				state.phase = javaStreamedCallTypeArguments
				state.depth = 1
				state.annotationState = 0
				state.annotationDepth = 0
				return true
			}
			if javaTokenIsSourceName(token) {
				state.member = token
				state.phase = javaStreamedCallAfterMember
				return true
			}
			return finish()
		case javaStreamedCallTypeArguments:
			annotationName := token.value == "(" && state.annotationState == 2
			switch {
			case state.annotationDepth > 0:
				switch token.value {
				case "(":
					state.annotationDepth++
				case ")":
					state.annotationDepth--
				}
			case annotationName:
				state.annotationDepth = 1
			default:
				switch token.value {
				case "<":
					state.depth++
				case ">", ">>", ">>>":
					state.depth -= strings.Count(token.value, ">")
					if state.depth <= 0 {
						state.phase = javaStreamedCallAfterDot
						state.depth = 0
					}
				}
			}
			switch {
			case token.value == "@":
				state.annotationState = 1
			case javaTokenIsSourceName(token) && state.annotationState == 1:
				state.annotationState = 2
			case token.value == "." && state.annotationState == 2:
				state.annotationState = 1
			default:
				state.annotationState = 0
			}
			return true
		case javaStreamedCallAfterMember:
			switch token.value {
			case "(":
				if javaTokenStartsOnPhysicalLine(state.member, lineStart, lineEnd) {
					state.result = state.member
				}
				state.phase = javaStreamedCallArguments
				state.depth = 1
				return true
			case ".":
				state.phase = javaStreamedCallAfterDot
				return true
			case "[":
				state.phase = javaStreamedCallArrayIndex
				state.depth = 1
				return true
			default:
				return finish()
			}
		}
		return finish()
	}

	streamJavaLexicalEventsFromInput(
		input, javaLexicalStreamOptions{},
		func(event javaLexicalStreamEvent) bool {
			if event.kind == javaLexicalStreamOpaque {
				if state.active && state.phase == javaStreamedCallTypeArguments &&
					state.annotationDepth > 0 {
					return true
				}
				if state.active && len(activeWrappers) == 0 &&
					state.phase != javaStreamedCallArguments &&
					state.phase != javaStreamedCallArrayIndex {
					return finish()
				}
				history = history[:0]
				historyNext = 0
				annotationState = 0
				previousPatternName = false
				previousInstanceTypeName = false
				previousNonCallName = false
				previousTokenValid = false
				instanceAnnotationDepth = 0
				return true
			}
			if event.kind != javaLexicalStreamToken {
				return true
			}
			token := event.token
			if !lineStarted && token.start >= lineStart {
				delimiterStack = delimiterStack[:0]
				lineStarted = true
			}
			if state.active {
				if !consumeActive(token) {
					return false
				}
				appendHistory(token)
				return true
			}
			if token.start >= lineEnd {
				return false
			}
			annotationName := token.value == "(" && annotationState == 2
			candidateExcluded := annotationName || previousPatternName ||
				previousNonCallName
			if token.value == "(" && len(history) > 0 && !candidateExcluded {
				window := historyTokens(token)
				candidate := len(window) - 2
				if candidate >= 0 &&
					javaTokenStartsOnPhysicalLine(
						window[candidate], lineStart, lineEnd,
					) && javaTokenIsSourceName(window[candidate]) {
					delimiters := analyzeJavaDelimiters(window)
					if !javaLexicalAnnotationName(window, candidate) &&
						!javaLexicalTokenIsNonCallKeyword(
							window, delimiters, candidate,
						) && !javaStreamedNameBeginsPattern(window, candidate) {
						state = javaStreamedCallState{
							result: window[candidate], phase: javaStreamedCallArguments,
							depth: 1, active: true, started: true,
						}
						activeWrappers = append(activeWrappers[:0], delimiterStack...)
					}
				}
			}

			currentPatternName := false
			currentInstanceTypeName := false
			currentNonCallName := false
			instanceAnnotationToken := instanceAnnotationDepth > 0 ||
				annotationName && instanceofPending
			switch {
			case instanceAnnotationDepth > 0:
				switch token.value {
				case "(":
					instanceAnnotationDepth++
				case ")":
					instanceAnnotationDepth--
				}
			case annotationName && instanceofPending:
				instanceAnnotationDepth = 1
			case instancePatternDepth > 0:
				currentPatternName = javaTokenIsSourceName(token)
				switch token.value {
				case "(":
					instancePatternDepth++
				case ")":
					instancePatternDepth--
				}
			case token.value == "(" && previousInstanceTypeName:
				instancePatternDepth = 1
				instanceofPending = false
				instanceofAngleDepth = 0
			}
			if token.value == "instanceof" {
				instanceofPending = true
				instanceofAngleDepth = 0
			} else if instanceofPending && instancePatternDepth == 0 &&
				!instanceAnnotationToken {
				switch {
				case javaTokenIsSourceName(token):
					currentInstanceTypeName = true
					currentPatternName = true
				case token.value == "<":
					instanceofAngleDepth++
				case token.value == ">" || token.value == ">>" || token.value == ">>>":
					instanceofAngleDepth = max(
						0, instanceofAngleDepth-strings.Count(token.value, ">"),
					)
				case token.value == "?" && instanceofAngleDepth > 0,
					token.value == ".", token.value == "@", token.value == "[",
					token.value == "]", token.value == ",", token.value == "extends",
					token.value == "super", token.value == "&":
				default:
					instanceofPending = false
					instanceofAngleDepth = 0
				}
			}
			if javaTokenIsSourceName(token) && casePattern {
				currentPatternName = true
			}
			if token.value == "when" && casePattern ||
				token.value == "yield" && statementStart &&
					(!previousTokenValid || previousToken.value != ".") {
				currentNonCallName = true
			}
			switch token.value {
			case "case":
				casePattern = true
			case "when", "->", ":", ";", "{", "}":
				casePattern = false
			}
			switch token.value {
			case "{", "}", ";", "->", ":", "else", "do":
				statementStart = true
			default:
				statementStart = false
			}
			switch {
			case token.value == "@":
				annotationState = 1
			case javaTokenIsSourceName(token) && annotationState == 1:
				annotationState = 2
			case token.value == "." && annotationState == 2:
				annotationState = 1
			default:
				annotationState = 0
			}
			previousPatternName = currentPatternName
			previousInstanceTypeName = currentInstanceTypeName
			previousNonCallName = currentNonCallName
			previousToken = token
			previousTokenValid = true
			if !state.active {
				delimiterStack = updateDelimiters(delimiterStack, token)
			}
			appendHistory(token)
			return true
		},
	)
	if !found && state.active && state.started {
		result = state.result.text
		found = result != ""
	}
	return result, found
}

func javaStreamedNameBeginsPattern(tokens []javaToken, index int) bool {
	if index < 0 || index >= len(tokens) {
		return false
	}
	minimum := max(0, index-javaMaximumRecoveryHeaderTokens)
	for cursor := index - 1; cursor >= minimum; cursor-- {
		switch tokens[cursor].value {
		case "case":
			return true
		case "when", "->", ":", ";", "{", "}", "&&", "||":
			cursor = minimum
		}
	}
	instanceof := -1
	for cursor := index - 1; cursor >= minimum; cursor-- {
		switch tokens[cursor].value {
		case "instanceof":
			instanceof = cursor
			cursor = minimum
		case "when", "->", ":", ";", "{", "}", "&&", "||", ")", "=":
			return false
		}
	}
	if instanceof < 0 {
		return false
	}
	angleDepth := 0
	for cursor := instanceof + 1; cursor < index; cursor++ {
		token := tokens[cursor]
		if javaTokenIsSourceName(token) {
			continue
		}
		switch token.value {
		case "@", ".", "[", "]", ",", "extends", "super", "&":
		case "<":
			angleDepth++
		case ">", ">>", ">>>":
			angleDepth = max(0, angleDepth-strings.Count(token.value, ">"))
		case "?":
			if angleDepth == 0 {
				return false
			}
		default:
			return false
		}
	}
	return true
}

type javaStreamedMemberPhase uint8

const (
	javaStreamedMemberIdle javaStreamedMemberPhase = iota
	javaStreamedMemberAfterName
	javaStreamedMemberTypeArguments
	javaStreamedMemberAfterDot
)

// javaStreamedNonCallSymbolOnLine collects the lower lexical priorities in a
// single pass. Each category retains at most its first candidate; a terminal
// member and a generic method-reference argument use scalar state rather than
// a token window, so a candidate may sit arbitrarily far from both line
// flanks.
func javaStreamedNonCallSymbolOnLine(
	input *javaUnicodeInput,
	lineStart, lineEnd int,
) (string, bool) {
	if input == nil || lineStart < 0 || lineEnd < lineStart ||
		lineEnd > len(input.source) {
		return "", false
	}

	var methodReference, member, guard, variable, generic javaToken
	previous := javaToken{}
	previousValid := false
	qualifierSource := javaToken{}
	qualifierTypeArgumentDepth := 0
	caseActive := false
	guardActive := false
	varPending := false
	statementStart := true
	parenDepth := 0
	controlPending := false
	controlParens := make([]int, 0, 8)
	genericPending := javaToken{}
	genericPendingKind := uint8(0)
	resolveGenericPending := func(next javaToken, hasNext bool) {
		if generic.text != "" || genericPending.text == "" {
			genericPending = javaToken{}
			genericPendingKind = 0
			return
		}
		contextual := false
		if hasNext {
			switch genericPendingKind {
			case 1:
				contextual = javaTokenIsSourceName(next)
			case 2:
				contextual = next.value == "class" || next.value == "interface"
			case 3:
				switch next.value {
				case "=", "+=", "-=", "*=", "/=", "%=", "&=", "|=", "^=",
					"<<=", ">>=", ">>>=", "++", "--", "[", ".", "::", ";":
				default:
					contextual = true
				}
			case 4:
				contextual = javaTokenIsSourceName(next) ||
					next.value == "static" || next.value == "transitive"
			}
		}
		if !contextual {
			generic = genericPending
		}
		genericPending = javaToken{}
		genericPendingKind = 0
	}

	memberPhase := javaStreamedMemberIdle
	memberPending := javaToken{}
	memberAngleDepth := 0
	finishMember := func() {
		if member.text == "" && memberPending.text != "" {
			member = memberPending
		}
		memberPending = javaToken{}
		memberPhase = javaStreamedMemberIdle
		memberAngleDepth = 0
	}

	referenceActive := false
	referenceTypeArguments := false
	referenceAngleDepth := 0
	referenceAnnotationState := uint8(0)
	referenceAnnotationDepth := 0
	referenceQualifier := javaToken{}
	consumeReference := func(token javaToken) bool {
		if !referenceActive {
			return false
		}
		if referenceTypeArguments {
			annotationName := token.value == "(" && referenceAnnotationState == 2
			switch {
			case referenceAnnotationDepth > 0:
				switch token.value {
				case "(":
					referenceAnnotationDepth++
				case ")":
					referenceAnnotationDepth--
				}
			case annotationName:
				referenceAnnotationDepth = 1
			default:
				switch token.value {
				case "<":
					referenceAngleDepth++
				case ">", ">>", ">>>":
					referenceAngleDepth -= strings.Count(token.value, ">")
					if referenceAngleDepth <= 0 {
						referenceTypeArguments = false
						referenceAngleDepth = 0
					}
				}
			}
			switch {
			case token.value == "@":
				referenceAnnotationState = 1
			case javaTokenIsSourceName(token) && referenceAnnotationState == 1:
				referenceAnnotationState = 2
			case token.value == "." && referenceAnnotationState == 2:
				referenceAnnotationState = 1
			default:
				referenceAnnotationState = 0
			}
			return true
		}
		if token.value == "<" {
			referenceTypeArguments = true
			referenceAngleDepth = 1
			return true
		}
		if methodReference.text == "" {
			if javaTokenIsSourceName(token) &&
				javaTokenStartsOnPhysicalLine(token, lineStart, lineEnd) {
				methodReference = token
			} else if token.value == "new" &&
				javaTokenStartsOnPhysicalLine(
					referenceQualifier, lineStart, lineEnd,
				) {
				methodReference = referenceQualifier
			}
		}
		referenceActive = false
		referenceAnnotationState = 0
		referenceAnnotationDepth = 0
		return true
	}

	streamJavaLexicalEventsFromInput(
		input, javaLexicalStreamOptions{},
		func(event javaLexicalStreamEvent) bool {
			if event.kind == javaLexicalStreamOpaque {
				if referenceActive && referenceTypeArguments &&
					referenceAnnotationDepth > 0 {
					return true
				}
				finishMember()
				resolveGenericPending(javaToken{}, false)
				referenceActive = false
				referenceTypeArguments = false
				varPending = false
				previousValid = false
				return true
			}
			if event.kind != javaLexicalStreamToken {
				return true
			}
			token := event.token
			resolveGenericPending(token, true)
			if token.start >= lineEnd {
				finishMember()
				return false
			}

			memberConsumed := false
			switch memberPhase {
			case javaStreamedMemberIdle:
			case javaStreamedMemberAfterName:
				switch token.value {
				case "<":
					memberPhase = javaStreamedMemberTypeArguments
					memberAngleDepth = 1
					memberConsumed = true
				case ".":
					memberPhase = javaStreamedMemberAfterDot
					memberConsumed = true
				default:
					finishMember()
				}
			case javaStreamedMemberTypeArguments:
				switch token.value {
				case "<":
					memberAngleDepth++
				case ">", ">>", ">>>":
					memberAngleDepth -= strings.Count(token.value, ">")
					if memberAngleDepth <= 0 {
						memberPhase = javaStreamedMemberAfterName
						memberAngleDepth = 0
					}
				}
				memberConsumed = true
			case javaStreamedMemberAfterDot:
				if javaTokenIsSourceName(token) &&
					javaTokenStartsOnPhysicalLine(token, lineStart, lineEnd) {
					memberPending = token
					memberPhase = javaStreamedMemberAfterName
				} else {
					finishMember()
				}
				memberConsumed = true
			}

			if consumeReference(token) {
				previous = token
				previousValid = true
				return true
			}

			onLine := javaTokenStartsOnPhysicalLine(token, lineStart, lineEnd)
			isName := javaTokenIsSourceName(token)
			if varPending {
				if variable.text == "" && onLine && isName {
					variable = token
				}
				varPending = false
			}
			if guardActive && guard.text == "" && onLine && isName &&
				token.value != "when" && token.value != "yield" {
				guard = token
			}
			if generic.text == "" && genericPending.text == "" && onLine && isName &&
				(token.value != "when" || !caseActive) {
				switch token.value {
				case "var", "module", "open", "exports", "opens", "to", "uses",
					"provides", "with", "record", "permits":
					genericPending = token
					genericPendingKind = 1
				case "sealed":
					genericPending = token
					genericPendingKind = 2
				case "yield":
					if statementStart && (!previousValid || previous.value != ".") {
						genericPending = token
						genericPendingKind = 3
					} else {
						generic = token
					}
				case "requires", "transitive":
					genericPending = token
					genericPendingKind = 4
				default:
					generic = token
				}
			}

			if token.value == "::" {
				referenceActive = true
				referenceQualifier = qualifierSource
				referenceAnnotationState = 0
				referenceAnnotationDepth = 0
			}
			if !memberConsumed && isName && onLine && previousValid &&
				previous.value == "." {
				memberPending = token
				memberPhase = javaStreamedMemberAfterName
			}
			if token.value == "var" && onLine {
				varPending = true
			}
			switch token.value {
			case "case":
				caseActive = true
				guardActive = false
			case "when":
				if caseActive {
					guardActive = true
				}
				caseActive = false
			case "->":
				caseActive = false
				guardActive = false
			case ";", "{", "}":
				caseActive = false
				guardActive = false
			}
			controlClosed := false
			switch token.value {
			case "if", "while", "for", "synchronized":
				controlPending = true
			case "(":
				parenDepth++
				if controlPending {
					if len(controlParens) < javaMaximumRecoveryHeaderTokens {
						controlParens = append(controlParens, parenDepth)
					}
					controlPending = false
				}
			case ")":
				if len(controlParens) > 0 &&
					controlParens[len(controlParens)-1] == parenDepth {
					controlParens = controlParens[:len(controlParens)-1]
					controlClosed = true
				}
				parenDepth = max(0, parenDepth-1)
			default:
				controlPending = false
			}
			switch token.value {
			case "{", "}", ";", "->", ":", "else", "do":
				statementStart = true
			case ")":
				statementStart = controlClosed
			default:
				statementStart = false
			}

			previous = token
			previousValid = true
			switch token.value {
			case "<":
				if qualifierSource.text != "" {
					qualifierTypeArgumentDepth++
				}
			case ">", ">>", ">>>":
				qualifierTypeArgumentDepth = max(
					0, qualifierTypeArgumentDepth-strings.Count(token.value, ">"),
				)
			}
			if isName {
				if qualifierTypeArgumentDepth == 0 {
					qualifierSource = token
				}
			}
			return true
		},
	)
	finishMember()
	resolveGenericPending(javaToken{}, false)
	for _, candidate := range []javaToken{
		methodReference, member, guard, variable, generic,
	} {
		if candidate.text != "" {
			return candidate.text, true
		}
	}
	return "", false
}

func javaStoredTokenGapIntersectsLine(
	tokens []javaToken,
	lineStart, lineEnd int,
) bool {
	for index, token := range tokens {
		if !token.gap {
			continue
		}
		gapStart := 0
		if index > 0 {
			gapStart = tokens[index-1].end
		}
		return gapStart < lineEnd && token.start > lineStart
	}
	return false
}

func javaStreamedTokensAroundLine(
	input *javaUnicodeInput,
	lineStart, lineEnd int,
) []javaToken {
	if input == nil || lineStart < 0 || lineEnd < lineStart ||
		lineEnd > len(input.source) {
		return nil
	}

	const flankLimit = javaMaximumRecoveryHeaderTokens
	prefix := make([]javaToken, 0, flankLimit)
	prefixNext := 0
	tokens := make([]javaToken, 0, flankLimit*4+1)
	lineTail := make([]javaToken, 0, flankLimit)
	lineTailNext := 0
	prefixAdded := false
	lineTokenCount := 0
	afterCount := 0
	addLineTail := func() {
		if len(lineTail) == 0 {
			return
		}
		earliest := 0
		if len(lineTail) == cap(lineTail) {
			earliest = lineTailNext
		}
		tokens = append(tokens, javaToken{
			text: ";", value: ";", start: lineTail[earliest].start,
			end: lineTail[earliest].start, gap: true,
		})
		tokens = append(tokens, lineTail[earliest:]...)
		tokens = append(tokens, lineTail[:earliest]...)
		lineTail = nil
	}
	addPrefix := func() {
		if prefixAdded {
			return
		}
		prefixAdded = true
		if len(prefix) < cap(prefix) || prefixNext == 0 {
			tokens = append(tokens, prefix...)
			return
		}
		tokens = append(tokens, prefix[prefixNext:]...)
		tokens = append(tokens, prefix[:prefixNext]...)
	}
	streamJavaLexicalEventsFromInput(
		input, javaLexicalStreamOptions{},
		func(event javaLexicalStreamEvent) bool {
			if event.kind != javaLexicalStreamToken {
				return true
			}
			token := event.token
			if token.start < lineStart {
				if len(prefix) < cap(prefix) {
					prefix = append(prefix, token)
				} else {
					prefix[prefixNext] = token
					prefixNext = (prefixNext + 1) % len(prefix)
				}
				return true
			}
			addPrefix()
			if token.start < lineEnd {
				lineTokenCount++
				if lineTokenCount <= flankLimit {
					tokens = append(tokens, token)
					return true
				}
				if len(lineTail) < cap(lineTail) {
					lineTail = append(lineTail, token)
				} else {
					lineTail[lineTailNext] = token
					lineTailNext = (lineTailNext + 1) % len(lineTail)
				}
				return true
			}
			addLineTail()
			if afterCount >= flankLimit {
				return false
			}
			tokens = append(tokens, token)
			afterCount++
			return true
		},
	)
	addPrefix()
	addLineTail()
	return tokens
}

func javaLexicalConstructorReferenceNameOnLine(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	reference, nameIndex, lineStart, lineEnd int,
) int {
	if reference <= 0 || reference >= len(tokens) || nameIndex <= reference ||
		nameIndex >= len(tokens) || tokens[reference].value != "::" ||
		tokens[nameIndex].value != "new" {
		return -1
	}
	// The complete reference precedes a later physical line; none of its type
	// names can be selected there. This is the common adversarial case for many
	// constructor references followed by a symbol-free line.
	if tokens[nameIndex].end <= lineStart {
		return -1
	}

	cursor := javaTokenBeforeTypeArguments(tokens, delimiters, reference-1)
	minimum := max(0, cursor-javaMaximumRecoveryHeaderTokens+1)
	for cursor >= minimum {
		token := tokens[cursor]
		if javaLexicalConstructorReferenceBoundary(token.value) {
			return -1
		}
		if javaTokenStartsOnPhysicalLine(token, lineStart, lineEnd) &&
			javaTokenIsSourceName(token) {
			return cursor
		}
		cursor--
	}
	return -1
}

func javaLexicalConstructorReferenceBoundary(value string) bool {
	switch value {
	case ";", ",", "=", "+=", "-=", "*=", "/=", "%=", "&=", "|=", "^=",
		"<<=", ">>=", ">>>=", "->", "?", ":", "{", "}", "(",
		"return", "yield":
		return true
	default:
		return false
	}
}

func javaTokenStartsOnPhysicalLine(token javaToken, lineStart, lineEnd int) bool {
	return token.start >= lineStart && token.start < lineEnd
}

func javaLexicalAnnotationName(tokens []javaToken, index int) bool {
	if index < 0 || index >= len(tokens) || !javaTokenIsSourceName(tokens[index]) {
		return false
	}
	for cursor := index; cursor > 0; {
		if tokens[cursor-1].value == "@" {
			return true
		}
		if cursor < 2 || tokens[cursor-1].value != "." ||
			!javaTokenIsSourceName(tokens[cursor-2]) {
			return false
		}
		cursor -= 2
	}
	return false
}

func javaLexicalTokenIsNonCallKeyword(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	index int,
) bool {
	if index < 0 || index >= len(tokens) {
		return true
	}
	member := index > 0 && (tokens[index-1].value == "." || tokens[index-1].value == "::")
	if member {
		return false
	}
	switch tokens[index].value {
	case "yield":
		return javaLexicalStatementStart(tokens, delimiters, index)
	case "when":
		return javaLexicalCaseGuardKeyword(tokens, index)
	default:
		return false
	}
}

func javaLexicalNameBeginsPattern(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	index int,
) bool {
	minimum := max(0, index-javaMaximumRecoveryHeaderTokens)

casePattern:
	for cursor := index - 1; cursor >= minimum; cursor-- {
		switch tokens[cursor].value {
		case "case":
			return true
		case "when", "->", ":", ";", "{", "}", "&&", "||":
			break casePattern
		}
	}
	for cursor := index - 1; cursor >= minimum; cursor-- {
		if tokens[cursor].value != "instanceof" {
			continue
		}
		patternStart, closeIndex, ok := javaLexicalInstanceofRecordPattern(
			tokens, delimiters, cursor,
		)
		return ok && index >= patternStart && index < closeIndex
	}
	return false
}

func javaLexicalInstanceofRecordPattern(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	instanceof int,
) (int, int, bool) {
	if instanceof < 0 || instanceof >= len(tokens) ||
		tokens[instanceof].value != "instanceof" {
		return 0, 0, false
	}
	cursor := instanceof + 1
	limit := min(len(tokens), cursor+javaMaximumRecoveryHeaderTokens)
	for cursor < limit && tokens[cursor].value == "@" {
		next, annotation := javaAnnotationEnd(tokens, delimiters, cursor, limit)
		if !annotation || next <= cursor {
			return 0, 0, false
		}
		cursor = next
	}
	if cursor >= limit || !javaTokenIsSourceName(tokens[cursor]) {
		return 0, 0, false
	}
	patternStart := cursor
	cursor++
	for cursor < limit {
		switch {
		case tokens[cursor].value == "<":
			next := javaTokenAfterTypeArguments(tokens, delimiters, cursor)
			if next <= cursor || next > limit {
				return 0, 0, false
			}
			cursor = next
		case cursor+1 < limit && tokens[cursor].value == "." &&
			javaTokenIsSourceName(tokens[cursor+1]):
			cursor += 2
		default:
			goto typeComplete
		}
	}

typeComplete:
	if cursor >= limit || tokens[cursor].value != "(" {
		return 0, 0, false
	}
	closeIndex := javaDelimiterMatch(delimiters, cursor)
	if closeIndex <= cursor {
		return 0, 0, false
	}
	return patternStart, closeIndex, true
}

func javaLexicalLastInvokedMemberOnLine(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	closeIndex, lineStart, lineEnd int,
) int {
	cursor := closeIndex + 1
	last := -1
	for cursor < len(tokens) {
		switch tokens[cursor].value {
		case "[":
			nextClose := javaDelimiterMatch(delimiters, cursor)
			if nextClose <= cursor {
				return last
			}
			cursor = nextClose + 1
		case ".":
			cursor++
			if cursor < len(tokens) && tokens[cursor].value == "<" {
				cursor = javaTokenAfterTypeArguments(tokens, delimiters, cursor)
			}
			if cursor >= len(tokens) || !javaTokenIsSourceName(tokens[cursor]) {
				return last
			}
			nameIndex := cursor
			cursor++
			if cursor >= len(tokens) || tokens[cursor].value != "(" {
				continue
			}
			if javaTokenStartsOnPhysicalLine(tokens[nameIndex], lineStart, lineEnd) {
				last = nameIndex
			}
			nextClose := javaDelimiterMatch(delimiters, cursor)
			if nextClose <= cursor {
				return last
			}
			cursor = nextClose + 1
		default:
			return last
		}
	}
	return last
}

func javaTokenAfterMethodReference(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	reference int,
) int {
	if reference < 0 || reference+1 >= len(tokens) {
		return -1
	}
	cursor := reference + 1
	if tokens[cursor].value == "<" {
		cursor = javaTokenAfterTypeArguments(tokens, delimiters, cursor)
	}
	if cursor < 0 || cursor >= len(tokens) {
		return -1
	}
	return cursor
}

func javaTokenAfterTypeArguments(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	open int,
) int {
	if open < 0 || open >= len(tokens) || tokens[open].value != "<" {
		return open
	}
	limit := min(len(tokens), open+javaMaximumRecoveryHeaderTokens)
	if closeIndex := javaExpressionTypeArgumentClose(
		tokens, delimiters, open, limit,
	); closeIndex > open {
		return closeIndex + 1
	}
	return limit
}

func javaTokenBeforeTypeArguments(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	closeIndex int,
) int {
	if closeIndex < 0 || closeIndex >= len(tokens) ||
		!strings.Contains(tokens[closeIndex].value, ">") {
		return closeIndex
	}
	minimum := max(0, closeIndex-javaMaximumRecoveryHeaderTokens+1)
	limit := min(len(tokens), closeIndex+2)
	for cursor := minimum; cursor < closeIndex; cursor++ {
		if tokens[cursor].value == "<" && javaExpressionTypeArgumentClose(
			tokens, delimiters, cursor, limit,
		) == closeIndex {
			return cursor - 1
		}
	}
	return closeIndex
}

func javaLexicalMemberContinuesOnLine(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	index, lineStart, lineEnd int,
) bool {
	cursor := index + 1
	if cursor < len(tokens) && tokens[cursor].value == "<" {
		cursor = javaTokenAfterTypeArguments(tokens, delimiters, cursor)
	}
	return cursor+1 < len(tokens) && tokens[cursor].value == "." &&
		javaTokenIsSourceName(tokens[cursor+1]) &&
		javaTokenStartsOnPhysicalLine(tokens[cursor+1], lineStart, lineEnd)
}

func javaLexicalCaseGuardKeyword(tokens []javaToken, index int) bool {
	if index < 0 || index >= len(tokens) || tokens[index].value != "when" {
		return false
	}
	minimum := max(0, index-javaMaximumRecoveryHeaderTokens)
	for cursor := index - 1; cursor >= minimum; cursor-- {
		switch tokens[cursor].value {
		case "case":
			return true
		case "when", "->", ";", "{", "}":
			return false
		}
	}
	return false
}

func javaLexicalStatementStart(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	index int,
) bool {
	if index <= 0 {
		return true
	}
	previous := index - 1
	switch tokens[previous].value {
	case "{", "}", ";", "->", ":":
		return true
	case "else", "do":
		return true
	case ")":
		openIndex := javaDelimiterMatch(delimiters, previous)
		if openIndex <= 0 {
			return false
		}
		switch tokens[openIndex-1].value {
		case "if", "while", "for", "synchronized":
			return true
		}
		return false
	default:
		return false
	}
}

func javaLexicalSymbolContextualKeyword(
	tokens []javaToken,
	delimiters javaDelimiterAnalysis,
	index int,
) bool {
	if index < 0 || index >= len(tokens) || index > 0 &&
		(tokens[index-1].value == "." || tokens[index-1].value == "::") {
		return false
	}
	value := tokens[index].value
	if value == "when" && javaLexicalCaseGuardKeyword(tokens, index) {
		return true
	}
	if value == "yield" && javaLexicalStatementStart(tokens, delimiters, index) {
		if index+1 >= len(tokens) {
			return false
		}
		switch tokens[index+1].value {
		case "=", "+=", "-=", "*=", "/=", "%=", "&=", "|=", "^=",
			"<<=", ">>=", ">>>=", "++", "--", "[", ".", "::", ";":
			return false
		default:
			return true
		}
	}
	if value == "requires" {
		return javaLexicalRequiresDirectiveName(tokens, index) >= 0
	}
	if value == "transitive" {
		cursor := index - 1
		for cursor >= 0 &&
			(tokens[cursor].value == "static" || tokens[cursor].value == "transitive") {
			cursor--
		}
		return cursor >= 0 && tokens[cursor].value == "requires" &&
			javaLexicalRequiresDirectiveName(tokens, cursor) >= 0
	}
	contextual := false
	switch value {
	case "var", "module", "open", "exports", "opens",
		"to", "uses", "provides", "with", "record", "permits":
		contextual = true
	case "sealed":
		return index+1 < len(tokens) &&
			(tokens[index+1].value == "class" || tokens[index+1].value == "interface")
	}
	return contextual && index+1 < len(tokens) && javaTokenIsSourceName(tokens[index+1])
}

func javaLexicalRequiresDirectiveName(tokens []javaToken, requires int) int {
	if requires < 0 || requires >= len(tokens) || tokens[requires].value != "requires" {
		return -1
	}
	cursor := requires + 1
	for cursor < len(tokens) &&
		(tokens[cursor].value == "static" || tokens[cursor].value == "transitive") {
		cursor++
	}
	if cursor >= len(tokens) || !javaTokenIsSourceName(tokens[cursor]) {
		return -1
	}
	return cursor
}
