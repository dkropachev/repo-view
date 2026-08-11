package navigator

import (
	"sort"
	"strings"
)

type kotlinLanguage struct {
	analysis *kotlinSourceAnalysis
	languageDefinition
}

type kotlinSourceAnalysis struct {
	tree          *kotlinSyntaxTree
	scopeResolver *cPreparedFindScopeResolver
	source        string
	lineStarts    []int
	lineStorage   []string
	lineSnapshot  []string
	definitions   []sourceDefinition
	scopes        []cLineScope
	imports       []cLineSpan
	recoverySpans []cByteSpan
	lexed         kotlinLexResult
	lineCount     int
}

func newKotlinLanguage() kotlinLanguage {
	return kotlinLanguage{languageDefinition: newLanguageDefinition(
		"kt", nil, nil, nil, commentStyleCLike, false,
	)}
}

func registerKotlinLanguage(registry map[string]languageBackend) {
	registerLanguage(registry, newKotlinLanguage(), ".kt", ".kts")
}

func (kotlin kotlinLanguage) prepareSource(lines []string) languageBackend {
	if len(lines) == 0 {
		kotlin.analysis = nil
		return kotlin
	}
	kotlin.analysis = analyzeKotlinSource(strings.Join(lines, "\n"), len(lines))
	kotlin.analysis.lineStorage = lines
	kotlin.analysis.lineSnapshot = append([]string(nil), lines...)
	return kotlin
}

func (kotlin kotlinLanguage) sourceAnalysis(lines []string) *kotlinSourceAnalysis {
	if len(lines) == 0 {
		return nil
	}
	if kotlin.analysis != nil && cSameLineStorage(kotlin.analysis.lineStorage, lines) &&
		cSameLines(kotlin.analysis.lineSnapshot, lines) {
		return kotlin.analysis
	}
	source := strings.Join(lines, "\n")
	if kotlin.analysis != nil && kotlin.analysis.source == source &&
		kotlin.analysis.lineCount == len(lines) {
		return kotlin.analysis
	}
	return analyzeKotlinSource(source, len(lines))
}

func analyzeKotlinSource(source string, lineCount int) *kotlinSourceAnalysis {
	lineCount = max(lineCount, 1)
	analysis := &kotlinSourceAnalysis{
		source: source, lineStarts: kotlinLineStarts(source), lineCount: lineCount,
		lexed: lexKotlin(source),
	}
	analysis.tree, _ = parseKotlinSyntax(source, analysis.lexed)
	analysis.recoverySpans = kotlinSyntaxErrorSpans(analysis.tree, len(source))

	concreteDefinitions := kotlinTreeDefinitions(source, lineCount, analysis.tree)
	concreteDefinitions = kotlinDefinitionsOutsideOpaqueSpans(
		concreteDefinitions,
		analysis.lineStarts,
		normalizeCSpans(append(
			append([]cByteSpan(nil), analysis.lexed.commentSpans...),
			analysis.lexed.stringSpans...,
		)),
	)
	concreteScopes := kotlinTreeScopes(source, lineCount, analysis.tree)
	concreteImports := kotlinTreeImports(source, lineCount, analysis.tree)
	lexical := analyzeKotlinLexically(source, lineCount)

	analysis.definitions = kotlinMergeDefinitions(
		lineCount,
		concreteDefinitions,
		lexical.definitions,
		analysis.tree != nil,
		analysis.recoverySpans,
		analysis.lineStarts,
		source,
	)
	analysis.scopes = kotlinMergeScopes(
		lineCount,
		concreteScopes,
		lexical.scopes,
		analysis.definitions,
		analysis.tree != nil,
		analysis.recoverySpans,
		analysis.lineStarts,
	)
	analysis.imports = cCombinedImports(lineCount, concreteImports, lexical.imports)
	analysis.scopeResolver = newCPreparedFindScopeResolver(
		analysis.definitions, analysis.scopes, lineCount,
	)
	return analysis
}

func kotlinDefinitionsOutsideOpaqueSpans(
	definitions []sourceDefinition,
	lineStarts []int,
	opaque []cByteSpan,
) []sourceDefinition {
	if len(definitions) == 0 || len(opaque) == 0 {
		return definitions
	}
	filtered := definitions[:0]
	for _, definition := range definitions {
		if definition.line < 1 || definition.line > len(lineStarts) ||
			definition.column < 1 {
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

func kotlinMergeDefinitions(
	lineCount int,
	concrete, lexical []sourceDefinition,
	hasTree bool,
	recoverySpans []cByteSpan,
	lineStarts []int,
	source string,
) []sourceDefinition {
	definitions := make([]sourceDefinition, 0, len(concrete)+len(lexical))
	seen := make(map[kotlinDefinitionKey]int, len(concrete)+len(lexical))
	physicalHeaderSymbols := make(map[int]map[kotlinDefinitionKey]struct{})
	appendDefinition := func(definition sourceDefinition, authoritativeEnd bool) {
		definition = normalizeCDefinition(definition, lineCount)
		if definition.symbol == "" {
			return
		}
		identity := kotlinDefinitionKey{
			symbol: definition.symbol, line: definition.line, column: definition.column,
		}
		if index, exists := seen[identity]; exists {
			current := &definitions[index]
			if definition.ownsScope && !current.ownsScope {
				*current = definition
			} else if definition.ownsScope == current.ownsScope {
				current.scopeStart = min(current.scopeStart, definition.scopeStart)
				if definition.scopeEnd > current.scopeEnd {
					current.scopeEnd = definition.scopeEnd
					current.ownedEndColumn = definition.ownedEndColumn
				} else if definition.scopeEnd == current.scopeEnd &&
					(authoritativeEnd || current.ownedEndColumn == 0) {
					current.ownedEndColumn = definition.ownedEndColumn
				}
			}
			return
		}
		seen[identity] = len(definitions)
		definitions = append(definitions, definition)
	}
	for _, definition := range concrete {
		appendDefinition(definition, false)
	}
	for _, definition := range lexical {
		identity := kotlinDefinitionKey{
			symbol: definition.symbol, line: definition.line, column: definition.column,
		}
		trusted := kotlinTrustedLexicalDefinition(source, lineStarts, definition)
		touchesRecovery := kotlinDefinitionTouchesRecovery(
			definition, recoverySpans, lineStarts,
		)
		if _, duplicate := seen[identity]; duplicate {
			if !hasTree || trusted || touchesRecovery {
				appendDefinition(definition, true)
			}
			continue
		}
		if hasTree && !trusted && !touchesRecovery &&
			!kotlinLexicalDefinitionMatchesPhysicalHeader(
				source, lineStarts, definition, physicalHeaderSymbols,
			) {
			continue
		}
		appendDefinition(definition, true)
	}
	return kotlinSortUniqueDefinitions(definitions, lineCount)
}

func kotlinLexicalDefinitionMatchesPhysicalHeader(
	source string,
	lineStarts []int,
	definition sourceDefinition,
	cache map[int]map[kotlinDefinitionKey]struct{},
) bool {
	if definition.line < 1 || definition.line > len(lineStarts) {
		return false
	}
	symbols, cached := cache[definition.line]
	if !cached {
		start := lineStarts[definition.line-1]
		end := len(source)
		if definition.line < len(lineStarts) {
			end = lineStarts[definition.line]
		}
		symbols = make(map[kotlinDefinitionKey]struct{})
		for _, candidate := range analyzeKotlinLexically(source[start:end], 1).definitions {
			if candidate.symbol != "" {
				symbols[kotlinDefinitionKey{
					symbol: candidate.symbol, column: candidate.column,
				}] = struct{}{}
			}
		}
		cache[definition.line] = symbols
	}
	_, matched := symbols[kotlinDefinitionKey{
		symbol: definition.symbol, column: definition.column,
	}]
	return matched
}

func kotlinDefinitionTouchesRecovery(
	definition sourceDefinition,
	recoverySpans []cByteSpan,
	lineStarts []int,
) bool {
	startLine, endLine := definition.line, definition.line
	if definition.ownsScope {
		startLine = min(startLine, definition.scopeStart)
		endLine = max(endLine, definition.scopeEnd)
	}
	return cLineSpanTouchesByteSpans(startLine, endLine, lineStarts, recoverySpans)
}

func kotlinTrustedLexicalDefinition(
	source string,
	lineStarts []int,
	definition sourceDefinition,
) bool {
	if definition.line < 1 || definition.line > len(lineStarts) {
		return false
	}
	start := lineStarts[definition.line-1]
	end := len(source)
	if definition.line < len(lineStarts) {
		end = lineStarts[definition.line]
	}
	line := source[start:end]
	return strings.Contains(line, "context(") || strings.Contains(line, "context (") ||
		strings.Contains(line, "@all:") || strings.TrimSpace(line) == "field"
}

func kotlinMergeScopes(
	lineCount int,
	concrete, lexical []cLineScope,
	definitions []sourceDefinition,
	hasTree bool,
	recoverySpans []cByteSpan,
	lineStarts []int,
) []cLineScope {
	scopes := append([]cLineScope(nil), concrete...)
	for _, scope := range lexical {
		if !hasTree || cLineSpanTouchesByteSpans(
			scope.start, scope.end, lineStarts, recoverySpans,
		) {
			scopes = append(scopes, scope)
		}
	}
	for _, definition := range definitions {
		if definition.ownsScope {
			scopes = append(scopes, cLineScope{
				start: definition.scopeStart, end: definition.scopeEnd,
			})
		}
	}
	return cNormalizeTreeLineScopes(scopes, lineCount)
}

func (kotlin kotlinLanguage) definitionSymbol(line string) (string, bool) {
	for _, definition := range kotlin.sourceDefinitions([]string{line}) {
		if definition.line == 1 {
			return definition.symbol, true
		}
	}
	if strings.Contains(line, "constructor") {
		wrapped := []string{"class __ViewLineHost__ {", line, "}"}
		for _, definition := range kotlin.sourceDefinitions(wrapped) {
			if definition.line == 2 && definition.symbol != "__ViewLineHost__" {
				return definition.symbol, true
			}
		}
	}
	return "", false
}

func (kotlin kotlinLanguage) sourceDefinitions(lines []string) []sourceDefinition {
	analysis := kotlin.sourceAnalysis(lines)
	if analysis == nil {
		return nil
	}
	return append([]sourceDefinition(nil), analysis.definitions...)
}

func (kotlin kotlinLanguage) prepareFindScopeResolver(
	lines []string,
) preparedFindScopeResolver {
	analysis := kotlin.sourceAnalysis(lines)
	if analysis == nil {
		return nil
	}
	return analysis.scopeResolver
}

func (kotlin kotlinLanguage) enclosingScope(lines []string, lineNo int) (int, int) {
	if lineNo < 1 || lineNo > len(lines) {
		return lineNo, lineNo
	}
	analysis := kotlin.sourceAnalysis(lines)
	if analysis == nil || analysis.scopeResolver == nil {
		return lineNo, lineNo
	}
	return analysis.scopeResolver.enclosingScope(lineNo)
}

func (kotlin kotlinLanguage) navigationScope(lines []string, lineNo int) (int, int) {
	if lineNo < 1 || lineNo > len(lines) {
		return lineNo, lineNo
	}
	analysis := kotlin.sourceAnalysis(lines)
	if analysis == nil || analysis.scopeResolver == nil {
		return lineNo, lineNo
	}
	return analysis.scopeResolver.navigationScope(lineNo)
}

func (kotlin kotlinLanguage) scopeNameOnLine(
	lines []string,
	lineNo int,
) (string, bool) {
	if lineNo < 1 || lineNo > len(lines) {
		return "", false
	}
	analysis := kotlin.sourceAnalysis(lines)
	if analysis == nil || analysis.scopeResolver == nil {
		return "", false
	}
	return analysis.scopeResolver.scopeName(lineNo), true
}

func (kotlin kotlinLanguage) importRange(lines []string) (int, int, bool) {
	analysis := kotlin.sourceAnalysis(lines)
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

func (kotlin kotlinLanguage) cleanSource(source string, dropComments, _ bool) string {
	if !dropComments {
		return source
	}
	return dropBlankArtifactLines(maskKotlinSearchSource(source, true, false))
}

func (kotlinLanguage) stripComment(line string) string {
	return strings.TrimRight(maskKotlinSearchSource(line, true, false), " \t")
}

func (kotlin kotlinLanguage) cleanSourceLines(
	lines []string,
	dropComments, _ bool,
) []string {
	if len(lines) == 0 {
		return nil
	}
	if !dropComments {
		return append([]string(nil), lines...)
	}
	analysis := kotlin.sourceAnalysis(lines)
	if analysis == nil {
		return append([]string(nil), lines...)
	}
	masked := strings.Split(maskKotlinSearchSource(analysis.source, true, false), "\n")
	for index := range masked {
		masked[index] = strings.TrimRight(masked[index], " \t")
	}
	return masked
}

func (kotlinLanguage) finalizeSourceSnippet(
	source string,
	dropComments, _ bool,
) string {
	if !dropComments {
		return source
	}
	return dropBlankArtifactLines(source)
}

func (kotlin kotlinLanguage) ignoredSearchLines(
	lines []string,
	dropComments, _ bool,
) map[int]bool {
	ignored := make(map[int]bool)
	if len(lines) == 0 || !dropComments {
		return ignored
	}
	analysis := kotlin.sourceAnalysis(lines)
	if analysis == nil {
		return ignored
	}
	masked := strings.Split(maskKotlinSearchSource(analysis.source, true, false), "\n")
	for index := range lines {
		if index < len(masked) && masked[index] != lines[index] &&
			strings.TrimSpace(masked[index]) == "" {
			ignored[index+1] = true
		}
	}
	return ignored
}

func (kotlin kotlinLanguage) searchLines(
	lines []string,
	noComments, noStrings bool,
) []string {
	if len(lines) == 0 {
		return nil
	}
	if !noComments && !noStrings {
		return append([]string(nil), lines...)
	}
	analysis := kotlin.sourceAnalysis(lines)
	if analysis == nil {
		return append([]string(nil), lines...)
	}
	return strings.Split(maskKotlinSearchSource(
		analysis.source, noComments, noStrings,
	), "\n")
}

func maskKotlinSearchSource(source string, maskComments, maskStrings bool) string {
	if source == "" || !maskComments && !maskStrings {
		return source
	}
	masked := []byte(source)
	mask := func(span cByteSpan) bool {
		start := max(0, min(span.start, len(masked)))
		end := max(start, min(span.end, len(masked)))
		for index := start; index < end; index++ {
			if masked[index] != '\n' && masked[index] != '\r' {
				masked[index] = ' '
			}
		}
		return true
	}
	sink := kotlinLexicalSink{}
	if maskComments {
		sink.comment = mask
	}
	if maskStrings {
		sink.literal = mask
	} else if maskComments {
		// A non-nil literal callback makes the walker enter interpolation
		// expressions, where comments are real Kotlin comments.
		sink.literal = func(cByteSpan) bool { return true }
	}
	walkKotlinLexically(source, sink)
	return string(masked)
}

func (kotlinLanguage) countSymbolOccurrences(line, symbol string) int {
	if line == "" || symbol == "" {
		return 0
	}
	return kotlinWalkSymbolOccurrences(line, symbol, nil)
}

func (kotlinLanguage) symbolOccurrenceColumns(line, symbol string) []int {
	if line == "" || symbol == "" {
		return nil
	}
	var columns []int
	kotlinWalkSymbolOccurrences(line, symbol, func(start int) {
		columns = append(columns, start+1)
	})
	return columns
}

func kotlinWalkSymbolOccurrences(
	line, symbol string,
	visit func(start int),
) int {
	count := 0
	requireQuoted := kotlinHardKeyword(symbol)
	walkKotlinLexically(line, kotlinLexicalSink{token: func(token kotlinToken) bool {
		if token.kind == kotlinTokenIdentifier && token.text == symbol &&
			(!requireQuoted || token.quotedIdentifier) {
			count++
			if visit != nil {
				visit(token.nameStart)
			}
		}
		return true
	}})
	return count
}

func (kotlinLanguage) prepareSymbolOccurrenceCounter(symbol string) func(string) int {
	return func(line string) int {
		return kotlinLanguage{}.countSymbolOccurrences(line, symbol)
	}
}

func (kotlin kotlinLanguage) symbolOnLine(
	lines []string,
	lineNo int,
) (string, bool) {
	if lineNo < 1 || lineNo > len(lines) {
		return "", false
	}
	analysis := kotlin.sourceAnalysis(lines)
	if analysis == nil {
		return "", false
	}
	for _, definition := range analysis.definitions {
		if definition.line == lineNo {
			return definition.symbol, true
		}
	}
	masked := kotlin.searchLines(lines, true, true)[lineNo-1]
	return kotlinSemanticSymbolOnLine(masked)
}

func (kotlinLanguage) authoritativeSymbolOnLine() {}

func kotlinSemanticSymbolOnLine(line string) (string, bool) {
	tokens := make([]kotlinToken, 0, 16)
	walkKotlinLexically(line, kotlinLexicalSink{token: func(token kotlinToken) bool {
		tokens = append(tokens, token)
		return true
	}})
	for index, token := range tokens {
		if token.kind != kotlinTokenIdentifier || kotlinKeywordToken(token) {
			continue
		}
		if index > 0 && (tokens[index-1].text == "." ||
			tokens[index-1].text == "?." || tokens[index-1].text == "::") {
			return token.text, true
		}
	}
	for index, token := range tokens {
		if token.kind != kotlinTokenIdentifier || kotlinKeywordToken(token) {
			continue
		}
		if index+1 < len(tokens) && tokens[index+1].text == "(" {
			return token.text, true
		}
	}
	for _, token := range tokens {
		if token.kind == kotlinTokenIdentifier && !kotlinKeywordToken(token) {
			return token.text, true
		}
	}
	return "", false
}
