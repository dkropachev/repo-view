package repoview

import (
	"sort"
	"strings"
	"unicode/utf8"
)

type cppLanguage struct {
	analysis *cppSourceAnalysis
	languageDefinition
}

type cppSourceAnalysis struct {
	tree          *cppSyntaxTree
	scopeResolver *cPreparedFindScopeResolver
	source        string
	lineStarts    []int
	lineStorage   []string
	lineSnapshot  []string
	recoverySpans []cByteSpan
	definitions   []sourceDefinition
	scopes        []cLineScope
	imports       []cLineSpan
	lexed         cppLexResult
	lineCount     int
}

func newCPPLanguage() cppLanguage {
	return cppLanguage{languageDefinition: newLanguageDefinition(
		"cpp", nil, nil, nil, commentStyleCLike, false,
	)}
}

func registerCPPLanguage(registry map[string]languageBackend) {
	registerLanguage(
		registry,
		newCPPLanguage(),
		".C", ".CC", ".CPP", ".CXX",
		".cc", ".cpp", ".cxx", ".c++", ".ii",
		".H", ".HH", ".HPP", ".HXX",
		".hpp", ".hh", ".hxx", ".h++",
		".ipp", ".tpp", ".tcc", ".inl",
		".ixx", ".cppm", ".mpp", ".ccm", ".cxxm", ".txx",
	)
}

func (cpp cppLanguage) prepareSource(lines []string) languageBackend {
	if len(lines) == 0 {
		cpp.analysis = nil
		return cpp
	}
	cpp.analysis = analyzeCPPSource(strings.Join(lines, "\n"), len(lines))
	cpp.analysis.lineStorage = lines
	cpp.analysis.lineSnapshot = append([]string(nil), lines...)
	return cpp
}

func (cpp cppLanguage) sourceAnalysis(lines []string) *cppSourceAnalysis {
	if len(lines) == 0 {
		return nil
	}
	if cpp.analysis != nil && cSameLineStorage(cpp.analysis.lineStorage, lines) &&
		cSameLines(cpp.analysis.lineSnapshot, lines) {
		return cpp.analysis
	}
	source := strings.Join(lines, "\n")
	if cpp.analysis != nil && cpp.analysis.source == source &&
		cpp.analysis.lineCount == len(lines) {
		return cpp.analysis
	}
	return analyzeCPPSource(source, len(lines))
}

func analyzeCPPSource(source string, lineCount int) *cppSourceAnalysis {
	analysis := &cppSourceAnalysis{
		source:     source,
		lineStarts: cLineStarts(source),
		lineCount:  lineCount,
		lexed:      lexCPP(source),
	}
	analysis.tree, _ = parseCPPSyntax(source)
	analysis.recoverySpans = cSyntaxErrorSpans(analysis.tree, len(source))

	concreteDefinitions := cppTreeDefinitions(
		source, lineCount, analysis.tree,
	)
	concreteDefinitions = cppFilterOpaqueDefinitions(
		concreteDefinitions,
		analysis.lineStarts,
		analysis.lexed.opaqueSpans,
	)
	concreteDefinitions = cppFilterImportPhantoms(
		concreteDefinitions, analysis.lineStarts, analysis.lexed.moduleSpans,
	)
	fallbackDefinitions := analysis.lexed.fallbackDefinitions
	if analysis.tree != nil {
		filtered := fallbackDefinitions[:0]
		for _, definition := range fallbackDefinitions {
			if cppDefinitionUsesLogicalSplice(
				definition, source, analysis.lineStarts,
			) || cppDefinitionTouchesRecovery(
				definition, analysis.recoverySpans, analysis.lineStarts,
			) {
				filtered = append(filtered, definition)
			}
		}
		fallbackDefinitions = filtered
	}
	analysis.definitions = cppCombinedDefinitions(
		lineCount,
		concreteDefinitions,
		analysis.lexed.trustedDefinitions,
		fallbackDefinitions,
	)
	analysis.definitions = cppFilterSourceBackedDefinitions(
		analysis.definitions, source, analysis.lineStarts, analysis.lexed.moduleSpans,
	)

	concreteScopes := cppTreeScopes(source, analysis.tree)
	lexicalScopes := analysis.lexed.scopes
	if analysis.tree != nil {
		filtered := lexicalScopes[:0]
		for _, scope := range lexicalScopes {
			if cLineSpanTouchesByteSpans(
				scope.start, scope.end,
				analysis.lineStarts, analysis.recoverySpans,
			) {
				filtered = append(filtered, scope)
			}
		}
		lexicalScopes = filtered
	}
	analysis.scopes = cCombinedScopes(
		lineCount,
		concreteScopes,
		lexicalScopes,
		analysis.definitions,
		false,
		nil,
		nil,
	)
	analysis.imports = cCombinedImports(
		lineCount,
		cppTreeImports(source, analysis.tree),
		analysis.lexed.imports,
	)
	analysis.scopeResolver = newCPreparedFindScopeResolver(
		analysis.definitions, analysis.scopes, lineCount,
	)
	return analysis
}

func cppDefinitionUsesLogicalSplice(
	definition sourceDefinition,
	source string,
	lineStarts []int,
) bool {
	if definition.line < 1 || definition.line > len(lineStarts) ||
		definition.column < 1 || !cppSourceIdentifier(definition.symbol) {
		return false
	}
	start := lineStarts[definition.line-1] + definition.column - 1
	if start < 0 || start >= len(source) {
		return false
	}
	end := cppLogicalIdentifierEnd(source, start)
	return end > start && end <= len(source) && end-start != len(definition.symbol) &&
		cLogicalText(source, start, end) == definition.symbol
}

func cppFilterOpaqueDefinitions(
	definitions []sourceDefinition,
	lineStarts []int,
	opaque []cByteSpan,
) []sourceDefinition {
	if len(definitions) == 0 || len(opaque) == 0 {
		return definitions
	}
	filtered := definitions[:0]
	for _, definition := range definitions {
		if definition.line < 1 || definition.line > len(lineStarts) || definition.column < 1 {
			continue
		}
		anchor := lineStarts[definition.line-1] + definition.column - 1
		spanIndex := sort.Search(len(opaque), func(index int) bool {
			return opaque[index].end > anchor
		})
		if spanIndex < len(opaque) && opaque[spanIndex].start <= anchor {
			continue
		}
		filtered = append(filtered, definition)
	}
	return filtered
}

func cppFilterImportPhantoms(
	definitions []sourceDefinition,
	lineStarts []int,
	imports []cByteSpan,
) []sourceDefinition {
	if len(imports) == 0 {
		return definitions
	}
	filtered := definitions[:0]
	for _, definition := range definitions {
		if definition.line < 1 || definition.line > len(lineStarts) || definition.column < 1 {
			continue
		}
		anchor := lineStarts[definition.line-1] + definition.column - 1
		spanIndex := sort.Search(len(imports), func(index int) bool {
			return imports[index].end > anchor
		})
		phantom := spanIndex < len(imports) && imports[spanIndex].start <= anchor
		if !phantom {
			filtered = append(filtered, definition)
		}
	}
	return filtered
}

func cppFilterSourceBackedDefinitions(
	definitions []sourceDefinition,
	source string,
	lineStarts []int,
	moduleSpans []cByteSpan,
) []sourceDefinition {
	filtered := definitions[:0]
	for _, definition := range definitions {
		if definition.line < 1 || definition.line > len(lineStarts) ||
			definition.column < 1 || definition.symbol == "" {
			continue
		}
		start := lineStarts[definition.line-1] + definition.column - 1
		end := start + len(definition.symbol)
		lineEnd := len(source)
		if definition.line < len(lineStarts) {
			lineEnd = lineStarts[definition.line] - 1
		}
		if start < 0 || start >= len(source) {
			continue
		}
		physicalMatch := end <= lineEnd && end <= len(source) &&
			source[start:end] == definition.symbol
		logicalIdentifierMatch := false
		if cppSourceIdentifier(definition.symbol) {
			logicalEnd := cppLogicalIdentifierEnd(source, start)
			logicalIdentifierMatch = logicalEnd > start && logicalEnd <= len(source) &&
				cLogicalText(source, start, logicalEnd) == definition.symbol
		}
		moduleMatch := false
		if spanIndex := sort.Search(len(moduleSpans), func(index int) bool {
			return moduleSpans[index].end > start
		}); spanIndex < len(moduleSpans) {
			moduleMatch = moduleSpans[spanIndex].start <= start &&
				cppNavigationSymbol(definition.symbol)
		}
		if !physicalMatch && !logicalIdentifierMatch && !moduleMatch {
			continue
		}
		filtered = append(filtered, definition)
	}
	return filtered
}

func cppDefinitionTouchesRecovery(
	definition sourceDefinition,
	recoverySpans []cByteSpan,
	lineStarts []int,
) bool {
	start, end := definition.line, definition.line
	if definition.ownsScope {
		start = min(start, definition.scopeStart)
		end = max(end, definition.scopeEnd)
	}
	return cLineSpanTouchesByteSpans(start, end, lineStarts, recoverySpans)
}

func cppCombinedDefinitions(
	lineCount int,
	groups ...[]sourceDefinition,
) []sourceDefinition {
	definitions := make([]sourceDefinition, 0)
	seen := make(map[cDefinitionIdentity]int)
	for _, group := range groups {
		for _, definition := range group {
			definition = normalizeCDefinition(definition, lineCount)
			if definition.symbol == "" || !cppNavigationSymbol(definition.symbol) {
				continue
			}
			key := cDefinitionKey(definition)
			if index, exists := seen[key]; exists {
				current := &definitions[index]
				if definition.ownsScope && !current.ownsScope {
					*current = definition
				} else if definition.ownsScope == current.ownsScope {
					current.scopeStart = min(current.scopeStart, definition.scopeStart)
					current.scopeEnd = max(current.scopeEnd, definition.scopeEnd)
				}
				continue
			}
			seen[key] = len(definitions)
			definitions = append(definitions, definition)
		}
	}
	sort.SliceStable(definitions, func(first, second int) bool {
		if definitions[first].line != definitions[second].line {
			return definitions[first].line < definitions[second].line
		}
		if definitions[first].column != definitions[second].column {
			return definitions[first].column < definitions[second].column
		}
		return definitions[first].symbol < definitions[second].symbol
	})
	return definitions
}

func (cpp cppLanguage) definitionSymbol(line string) (string, bool) {
	for _, definition := range cpp.sourceDefinitions([]string{line}) {
		if definition.line == 1 {
			return definition.symbol, true
		}
	}
	return "", false
}

func (cpp cppLanguage) sourceDefinitions(lines []string) []sourceDefinition {
	analysis := cpp.sourceAnalysis(lines)
	if analysis == nil {
		return nil
	}
	return append([]sourceDefinition(nil), analysis.definitions...)
}

func (cpp cppLanguage) enclosingScope(lines []string, lineNo int) (int, int) {
	if lineNo < 1 || lineNo > len(lines) {
		return lineNo, lineNo
	}
	analysis := cpp.sourceAnalysis(lines)
	if analysis == nil || analysis.scopeResolver == nil {
		return lineNo, lineNo
	}
	return analysis.scopeResolver.enclosingScope(lineNo)
}

func (cpp cppLanguage) navigationScope(lines []string, lineNo int) (int, int) {
	analysis := cpp.sourceAnalysis(lines)
	if analysis == nil || analysis.scopeResolver == nil {
		return lineNo, lineNo
	}
	return analysis.scopeResolver.navigationScope(lineNo)
}

func (cpp cppLanguage) scopeNameOnLine(lines []string, lineNo int) (string, bool) {
	if lineNo < 1 || lineNo > len(lines) {
		return "", false
	}
	analysis := cpp.sourceAnalysis(lines)
	if analysis == nil || analysis.scopeResolver == nil {
		return "", false
	}
	return analysis.scopeResolver.scopeName(lineNo), true
}

func (cpp cppLanguage) prepareFindScopeResolver(lines []string) preparedFindScopeResolver {
	analysis := cpp.sourceAnalysis(lines)
	if analysis == nil {
		return nil
	}
	return analysis.scopeResolver
}

func (cpp cppLanguage) importRange(lines []string) (int, int, bool) {
	analysis := cpp.sourceAnalysis(lines)
	if analysis == nil {
		return 0, 0, false
	}
	start, end := 0, 0
	for _, statement := range analysis.imports {
		if start == 0 || statement.start < start {
			start = statement.start
		}
		end = max(end, statement.end)
	}
	return start, end, start > 0 && end >= start
}

func (cpp cppLanguage) cleanSource(source string, dropComments, _ bool) string {
	if !dropComments {
		return source
	}
	lines := strings.Split(source, "\n")
	return dropBlankArtifactLines(strings.Join(
		cpp.cleanSourceLines(lines, true, false), "\n",
	))
}

func (cpp cppLanguage) cleanSourceLines(
	lines []string,
	dropComments, _ bool,
) []string {
	if len(lines) == 0 {
		return nil
	}
	if !dropComments {
		return append([]string(nil), lines...)
	}
	analysis := cpp.sourceAnalysis(lines)
	if analysis == nil {
		return append([]string(nil), lines...)
	}
	masked := strings.Split(maskCSource(
		analysis.source, analysis.lexed.commentSpans,
	), "\n")
	for index := range masked {
		if index < len(lines) && masked[index] != lines[index] {
			masked[index] = strings.TrimRight(masked[index], " \t")
		}
	}
	return masked
}

func (cppLanguage) finalizeSourceSnippet(source string, dropComments, _ bool) string {
	if !dropComments {
		return source
	}
	return dropBlankArtifactLines(source)
}

func (cpp cppLanguage) ignoredSearchLines(
	lines []string,
	dropComments, _ bool,
) map[int]bool {
	ignored := map[int]bool{}
	if len(lines) == 0 || !dropComments {
		return ignored
	}
	analysis := cpp.sourceAnalysis(lines)
	if analysis == nil {
		return ignored
	}
	masked := strings.Split(maskCSource(
		analysis.source, analysis.lexed.commentSpans,
	), "\n")
	for index := range lines {
		if index < len(masked) && masked[index] != lines[index] &&
			strings.TrimSpace(masked[index]) == "" {
			ignored[index+1] = true
		}
	}
	return ignored
}

func (cpp cppLanguage) searchLines(
	lines []string,
	noComments, noStrings bool,
) []string {
	if len(lines) == 0 {
		return nil
	}
	if !noComments && !noStrings {
		return append([]string(nil), lines...)
	}
	analysis := cpp.sourceAnalysis(lines)
	if analysis == nil {
		return append([]string(nil), lines...)
	}
	spans := make([]cByteSpan, 0,
		len(analysis.lexed.commentSpans)+len(analysis.lexed.stringSpans))
	if noComments {
		spans = append(spans, analysis.lexed.commentSpans...)
	}
	if noStrings {
		spans = append(spans, analysis.lexed.stringSpans...)
	}
	return strings.Split(maskCSource(analysis.source, spans), "\n")
}

func (cppLanguage) stripComment(line string) string {
	lexed := lexCPP(line)
	return strings.TrimRight(maskCSource(line, lexed.commentSpans), " \t")
}

func (cppLanguage) countSymbolOccurrences(line, symbol string) int {
	if line == "" || !cppNavigationSymbol(symbol) {
		return 0
	}
	if cppSourceIdentifier(symbol) {
		return cppCountIdentifierOccurrences(line, symbol)
	}
	return cppCountCompositeOccurrences(line, symbol)
}

func cppCountIdentifierOccurrences(line, symbol string) int {
	count := 0
	for offset := 0; offset < len(line); {
		if cPreprocessingNumberStart(line, offset) {
			offset = cppLogicalNumberEnd(line, offset)
			continue
		}
		end, ok := cppLogicalIdentifierUnit(line, offset, true)
		if !ok {
			_, size := utf8.DecodeRuneInString(line[offset:])
			if size < 1 {
				size = 1
			}
			offset += size
			continue
		}
		start := offset
		offset = end
		for offset < len(line) {
			next, continues := cppLogicalIdentifierUnit(line, offset, false)
			if !continues {
				break
			}
			offset = next
		}
		if line[start:offset] == symbol {
			count++
		}
	}
	return count
}

func cppCountCompositeOccurrences(line, symbol string) int {
	count := 0
	for offset := 0; offset <= len(line)-len(symbol); {
		relative := strings.Index(line[offset:], symbol)
		if relative < 0 {
			break
		}
		start := offset + relative
		end := start + len(symbol)
		if cppCompositeBoundaries(line, start, end, symbol) {
			count++
		}
		offset = max(start+1, end)
	}
	return count
}

func cppCompositeBoundaries(line string, start, end int, symbol string) bool {
	firstIdentifier := strings.IndexFunc(symbol, func(character rune) bool {
		return cIdentifierRune(character, true)
	})
	lastIdentifier := strings.LastIndexFunc(symbol, func(character rune) bool {
		return cIdentifierRune(character, false)
	})
	if firstIdentifier >= 0 && start > 0 {
		previous, _ := utf8.DecodeLastRuneInString(line[:start+firstIdentifier])
		if cIdentifierRune(previous, false) {
			return false
		}
	}
	if lastIdentifier >= 0 && end < len(line) {
		next, _ := utf8.DecodeRuneInString(line[end:])
		if cIdentifierRune(next, false) {
			return false
		}
	}
	if strings.HasPrefix(symbol, "operator") && end < len(line) {
		suffix := strings.TrimSpace(strings.TrimPrefix(symbol, "operator"))
		if suffix != "" && strings.ContainsAny(suffix, "+-<>=*/%&|^!") &&
			strings.ContainsRune("+-<>=*/%&|^!", rune(line[end])) {
			return false
		}
		if suffix == "new" || suffix == "delete" {
			if strings.HasPrefix(line[end:], "[]") {
				return false
			}
		}
	}
	return true
}

func (cpp cppLanguage) walkAdditionalSymbolOccurrences(
	lines []string,
	symbol string,
	visit func(lineNo, additionalCount int) bool,
) bool {
	if visit == nil || !cppNavigationSymbol(symbol) || len(lines) == 0 {
		return false
	}
	if cppSourceIdentifier(symbol) {
		return cppWalkIdentifierOccurrenceAdjustments(
			cpp.sourceAnalysis(lines), lines, symbol, visit,
		)
	}
	return cpp.walkCompositeSymbolOccurrences(lines, symbol, visit)
}

// cppWalkIdentifierOccurrenceAdjustments reconciles the physical-line search
// with translation-phase line splicing. A logical identifier or pp-number can
// cover several physical lines: the complete identifier belongs on its first
// line, while every physical fragment must be removed with a signed
// adjustment. Streaming the full source also keeps this correction exact past
// the retained concrete-token window.
func cppWalkIdentifierOccurrenceAdjustments(
	analysis *cppSourceAnalysis,
	lines []string,
	symbol string,
	visit func(lineNo, additionalCount int) bool,
) bool {
	if analysis == nil || len(lines) == 0 {
		return true
	}

	nextLine := 1
	pendingAdjustment := 0
	stopped := false
	emitThrough := func(lastLine int) bool {
		lastLine = min(lastLine, len(lines))
		for nextLine <= lastLine {
			if !visit(nextLine, pendingAdjustment) {
				stopped = true
				return false
			}
			nextLine++
			pendingAdjustment = 0
		}
		return true
	}
	record := func(lineNo, adjustment int) bool {
		if lineNo < nextLine {
			return true
		}
		if !emitThrough(lineNo - 1) {
			return false
		}
		pendingAdjustment += adjustment
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
	recordToken := func(start, end int, text string, identifier bool) bool {
		if start < 0 || end <= start || end > len(analysis.source) ||
			!cPhysicalRangeContainsNewline(analysis.source, start, end) {
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
			if lineStart < segmentEnd {
				adjustment = -cppCountIdentifierOccurrences(
					analysis.source[lineStart:segmentEnd], symbol,
				)
			}
			if identifier && lineNo == firstLine && text == symbol {
				adjustment++
			}
			if !record(lineNo, adjustment) {
				return false
			}
			if end <= lineEnd || lineNo == len(lines) {
				break
			}
		}
		return true
	}

	opaqueIndex := 0
	for offset := 0; offset < len(analysis.source) && !stopped; {
		currentLine := lineAt(offset)
		if !emitThrough(currentLine - 1) {
			break
		}
		for opaqueIndex < len(analysis.lexed.opaqueSpans) &&
			analysis.lexed.opaqueSpans[opaqueIndex].end <= offset {
			opaqueIndex++
		}
		if opaqueIndex < len(analysis.lexed.opaqueSpans) {
			span := analysis.lexed.opaqueSpans[opaqueIndex]
			if span.start <= offset && offset < span.end {
				offset = min(span.end, len(analysis.source))
				continue
			}
		}
		if splice := cSpliceLength(analysis.source, offset); splice > 0 {
			offset += splice
			continue
		}
		if end := cppLogicalIdentifierEnd(analysis.source, offset); end > offset {
			if !recordToken(
				offset, end, cLogicalText(analysis.source, offset, end), true,
			) {
				break
			}
			offset = end
			continue
		}
		if cLogicalNumberStart(analysis.source, offset) {
			end := cppLogicalNumberEnd(analysis.source, offset)
			if !recordToken(offset, end, "", false) {
				break
			}
			offset = end
			continue
		}
		_, end := cppPunctuationAt(analysis.source, offset)
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

// Composite C++ navigation symbols keep their token-sequence reconciliation.
// In particular, qualified names, destructors, and operator spellings must not
// be reduced to the identifier-only streaming policy above.
func (cpp cppLanguage) walkCompositeSymbolOccurrences(
	lines []string,
	symbol string,
	visit func(lineNo, additionalCount int) bool,
) bool {
	analysis := cpp.sourceAnalysis(lines)
	if analysis == nil || analysis.lexed.truncated {
		return false
	}
	want := cppSymbolTokenTexts(symbol)
	anchored := make(map[int]int)
	if len(want) > 0 {
		for index := 0; index+len(want) <= len(analysis.lexed.tokens); index++ {
			matches := true
			for part := range want {
				if analysis.lexed.tokens[index+part].text != want[part] {
					matches = false
					break
				}
			}
			if !matches {
				continue
			}
			line := cppLineAt(analysis.lineStarts, analysis.lexed.tokens[index].start)
			anchored[line]++
		}
	}
	for lineNo, line := range lines {
		additional := anchored[lineNo+1] - cppLanguage{}.countSymbolOccurrences(line, symbol)
		if additional < 0 {
			additional = 0
		}
		if !visit(lineNo+1, additional) {
			return true
		}
	}
	return true
}

func cppSymbolTokenTexts(symbol string) []string {
	lexed := lexCPP(symbol)
	texts := make([]string, 0, len(lexed.tokens))
	for _, token := range lexed.tokens {
		if token.kind != cTokenLiteral {
			texts = append(texts, token.text)
		}
	}
	return texts
}

func cppLineAt(lineStarts []int, offset int) int {
	return sort.Search(len(lineStarts), func(index int) bool {
		return lineStarts[index] > offset
	})
}

func (cpp cppLanguage) symbolOnLine(
	lines []string,
	lineNo int,
) (string, bool) {
	if lineNo < 1 || lineNo > len(lines) {
		return "", false
	}
	analysis := cpp.sourceAnalysis(lines)
	if analysis == nil {
		return "", false
	}
	for _, definition := range analysis.definitions {
		if definition.line == lineNo {
			return definition.symbol, true
		}
	}
	if symbol := cppSemanticSymbolOnLine(analysis, lines[lineNo-1], lineNo); symbol != "" {
		return symbol, true
	}
	return "", false
}

func (cppLanguage) authoritativeSymbolOnLine() {}

func cppSemanticSymbolOnLine(
	analysis *cppSourceAnalysis,
	_ string,
	lineNo int,
) string {
	symbol, _ := cppStreamSymbolOnLine(analysis, lineNo)
	return symbol
}

// cppWalkLogicalCodeTokens streams the complete C++ preprocessing-token view.
// Comments disappear, while literals remain adjacency barriers. Unlike the
// bounded parser token view, directives are intentionally present so Inspect
// can still select macro and conditional operands.
func cppWalkLogicalCodeTokens(
	analysis *cppSourceAnalysis,
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
		if end := stringEnd(offset); end > offset {
			if !visit(cToken{
				text: analysis.source[offset:end], start: offset, end: end,
				kind: cTokenLiteral,
			}) {
				return
			}
			offset = end
			continue
		}
		if splice := cSpliceLength(analysis.source, offset); splice > 0 {
			offset += splice
			continue
		}
		if offset == 0 && strings.HasPrefix(analysis.source, "\uFEFF") {
			offset += len("\uFEFF")
			continue
		}
		switch analysis.source[offset] {
		case ' ', '\t', '\v', '\f', '\r', '\n':
			offset++
			continue
		}

		token := cToken{start: offset}
		if end := cppLogicalIdentifierEnd(analysis.source, offset); end > offset {
			token.text = cLogicalText(analysis.source, offset, end)
			token.end = end
			token.kind = cTokenIdentifier
		} else if cLogicalNumberStart(analysis.source, offset) {
			token.end = cppLogicalNumberEnd(analysis.source, offset)
			token.text = cLogicalText(analysis.source, offset, token.end)
			token.kind = cTokenNumber
		} else {
			token.text, token.end = cppPunctuationAt(analysis.source, offset)
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

func cppStreamSymbolOnLine(
	analysis *cppSourceAnalysis,
	lineNo int,
) (string, bool) {
	if analysis == nil || lineNo < 1 || lineNo > analysis.lineCount ||
		lineNo > len(analysis.lineStarts) {
		return "", false
	}
	lineStart := analysis.lineStarts[lineNo-1]
	lineEnd := len(analysis.source)
	if lineNo < len(analysis.lineStarts) {
		lineEnd = analysis.lineStarts[lineNo]
	}

	const (
		cppInspectTokenWindow    = 48
		cppInspectLookaheadBytes = 64 << 10
	)
	window := make([]cToken, 0, cppInspectTokenWindow)
	best, member, templateHead, call := "", "", "", ""
	hasLineCandidate := false
	afterLineTokens := 0

	appendWindow := func(token cToken) {
		if len(window) == cppInspectTokenWindow {
			copy(window, window[1:])
			window = window[:len(window)-1]
		}
		window = append(window, token)
	}
	cppWalkLogicalCodeTokens(analysis, func(offset int) bool {
		if call != "" {
			return false
		}
		if offset < lineEnd {
			return true
		}
		return hasLineCandidate && afterLineTokens < cppInspectTokenWindow &&
			offset-lineEnd <= cppInspectLookaheadBytes
	}, func(token cToken) bool {
		touches := cppTokenTouchesLine(token, lineStart, lineEnd)
		appendWindow(token)
		if token.start >= lineEnd {
			afterLineTokens++
		}

		if token.kind == cTokenIdentifier && !cppKeyword(token.text) &&
			token.text != "defined" && touches {
			best = token.text
			hasLineCandidate = true
			if len(window) >= 2 {
				previous := window[len(window)-2].text
				if previous == "." || previous == "->" {
					member = token.text
				}
			}
		}

		if token.text == ">" || token.text == ">>" {
			if head := cppTemplateHeadBefore(window, len(window)-1); head >= 0 &&
				cppTokenTouchesLine(window[head], lineStart, lineEnd) {
				templateHead = window[head].text
				hasLineCandidate = true
			}
		}
		if token.text == "(" {
			nameIndex, symbol := cppNavigationCallableBefore(
				analysis.source, window, len(window)-1,
			)
			if nameIndex >= 0 && symbol != "" &&
				cppTokenTouchesLine(window[nameIndex], lineStart, lineEnd) {
				call = symbol
				return false
			}
		}
		return true
	})

	switch {
	case call != "":
		return call, true
	case templateHead != "":
		return templateHead, true
	case member != "":
		return member, true
	case best != "":
		return best, true
	default:
		return "", false
	}
}

func cppTokenTouchesLine(token cToken, lineStart, lineEnd int) bool {
	return token.start < lineEnd && token.end > lineStart
}

func cppTemplateHeadBefore(tokens []cToken, closeIndex int) int {
	if closeIndex <= 0 || closeIndex >= len(tokens) {
		return -1
	}
	var depth int
	switch tokens[closeIndex].text {
	case ">":
		depth = 1
	case ">>":
		depth = 2
	default:
		return -1
	}
	for index := closeIndex - 1; index >= 0; index-- {
		switch tokens[index].text {
		case ">":
			depth++
		case ">>":
			depth += 2
		case "<":
			depth--
			if depth == 0 {
				head := index - 1
				if head >= 0 && tokens[head].kind == cTokenIdentifier &&
					!cppKeyword(tokens[head].text) {
					return head
				}
				return -1
			}
		case ";", "{", "}":
			return -1
		}
	}
	return -1
}

func cppNavigationCallableBefore(
	source string,
	tokens []cToken,
	open int,
) (int, string) {
	if open <= 0 || open >= len(tokens) {
		return -1, ""
	}

	// Member operator calls are expressions, so the declaration extractor's
	// deliberate member rejection does not apply here.
	for start := max(0, open-12); start < open; start++ {
		if tokens[start].text != "operator" ||
			!cppOperatorIDBefore(tokens, start, open) {
			continue
		}
		symbolStart, symbolEnd := tokens[start].start, tokens[open-1].end
		if symbolStart < 0 || symbolEnd <= symbolStart || symbolEnd > len(source) ||
			strings.ContainsAny(source[symbolStart:symbolEnd], "\r\n") {
			continue
		}
		return start, strings.TrimRight(
			source[symbolStart:symbolEnd], " \t\v\f",
		)
	}

	previous := open - 1
	if tokens[previous].text == ">" || tokens[previous].text == ">>" {
		if head := cppTemplateHeadBefore(tokens, previous); head >= 0 {
			return head, tokens[head].text
		}
	}
	if tokens[previous].kind != cTokenIdentifier ||
		cppKeyword(tokens[previous].text) || tokens[previous].text == "defined" {
		return -1, ""
	}
	if previous > 0 && tokens[previous-1].text == "~" {
		start, end := tokens[previous-1].start, tokens[previous].end
		if start >= 0 && end > start && end <= len(source) {
			return previous, source[start:end]
		}
		return previous, "~" + tokens[previous].text
	}
	return previous, tokens[previous].text
}
