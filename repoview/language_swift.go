package repoview

import (
	"sort"
	"strings"
	"unicode/utf8"
)

// swiftLanguage keeps the expensive lexical and concrete analysis attached to
// a prepared backend copy. Registry instances remain immutable and reusable.
type swiftLanguage struct {
	analysis *swiftSourceAnalysis
	languageDefinition
}

type swiftSourceAnalysis struct {
	tree          *swiftSyntaxTree
	scopeResolver *cPreparedFindScopeResolver
	source        string
	lineStarts    []int
	lineStorage   []string
	lineSnapshot  []string
	definitions   []sourceDefinition
	scopes        []cLineScope
	imports       []cLineSpan
	recoverySpans []cByteSpan
	lexed         swiftLexResult
	lineCount     int
}

func newSwiftLanguage() swiftLanguage {
	return swiftLanguage{languageDefinition: newLanguageDefinition(
		"swift", nil, nil, nil, commentStyleCLike, false,
	)}
}

func registerSwiftLanguage(registry map[string]languageBackend) {
	registerLanguage(registry, newSwiftLanguage(), ".swift")
}

func (swift swiftLanguage) prepareSource(lines []string) languageBackend {
	if len(lines) == 0 {
		swift.analysis = nil
		return swift
	}
	swift.analysis = analyzeSwiftSource(strings.Join(lines, "\n"), len(lines))
	swift.analysis.lineStorage = lines
	swift.analysis.lineSnapshot = append([]string(nil), lines...)
	return swift
}

func (swift swiftLanguage) sourceAnalysis(lines []string) *swiftSourceAnalysis {
	if len(lines) == 0 {
		return nil
	}
	if swift.analysis != nil && cSameLineStorage(swift.analysis.lineStorage, lines) &&
		cSameLines(swift.analysis.lineSnapshot, lines) {
		return swift.analysis
	}
	source := strings.Join(lines, "\n")
	if swift.analysis != nil && swift.analysis.source == source &&
		swift.analysis.lineCount == len(lines) {
		return swift.analysis
	}
	return analyzeSwiftSource(source, len(lines))
}

func analyzeSwiftSource(source string, lineCount int) *swiftSourceAnalysis {
	lineCount = max(lineCount, 1)
	analysis := &swiftSourceAnalysis{
		source: source, lineStarts: swiftLineStarts(source), lineCount: lineCount,
		lexed: lexSwift(source),
	}
	analysis.tree, _ = parseSwiftSyntax(source, analysis.lexed)
	analysis.recoverySpans = swiftSyntaxErrorSpans(analysis.tree, len(source))
	treeContext := newSwiftTreeAnalysisContext(source, analysis.tree, analysis.lexed)

	concreteDefinitions := swiftTreeDefinitionsWithContext(
		source, lineCount, analysis.tree, treeContext,
	)
	concreteDefinitions = swiftDefinitionsOutsideOpaqueSpans(
		concreteDefinitions,
		analysis.lineStarts,
		normalizeCSpans(append(
			append([]cByteSpan(nil), analysis.lexed.commentSpans...),
			analysis.lexed.stringSpans...,
		)),
	)
	lexical := analyzeSwiftLexically(source, lineCount)
	analysis.definitions = swiftMergeDefinitions(
		lineCount, concreteDefinitions, lexical.definitions,
	)
	analysis.scopes = swiftMergeScopes(
		lineCount,
		swiftTreeScopesWithContext(source, lineCount, analysis.tree, treeContext),
		lexical.scopes,
		analysis.definitions,
	)
	analysis.imports = cCombinedImports(
		lineCount,
		swiftTreeImportsWithContext(source, lineCount, analysis.tree, treeContext),
		lexical.imports,
	)
	analysis.scopeResolver = newCPreparedFindScopeResolver(
		analysis.definitions, analysis.scopes, lineCount,
	)
	return analysis
}

func swiftDefinitionsOutsideOpaqueSpans(
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

func swiftMergeDefinitions(
	lineCount int,
	groups ...[]sourceDefinition,
) []sourceDefinition {
	definitions := make([]sourceDefinition, 0)
	seen := make(map[swiftDefinitionKey]int)
	for _, group := range groups {
		for _, definition := range group {
			definition = normalizeCDefinition(definition, lineCount)
			if !swiftDefinitionSymbolValid(definition.symbol) {
				continue
			}
			key := swiftDefinitionKey{
				symbol: definition.symbol, line: definition.line, column: definition.column,
			}
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
	return swiftSortUniqueDefinitions(definitions, lineCount)
}

func swiftMergeScopes(
	lineCount int,
	concrete, lexical []cLineScope,
	definitions []sourceDefinition,
) []cLineScope {
	scopes := append(append([]cLineScope(nil), concrete...), lexical...)
	for _, definition := range definitions {
		if definition.ownsScope {
			scopes = append(scopes, cLineScope{
				start: definition.scopeStart, end: definition.scopeEnd,
			})
		}
	}
	return cNormalizeTreeLineScopes(scopes, lineCount)
}

func swiftDefinitionSymbolValid(symbol string) bool {
	if symbol == "" || !utf8.ValidString(symbol) || strings.ContainsAny(symbol, "\r\n") {
		return false
	}
	if symbol == "init" || symbol == "deinit" || symbol == "subscript" ||
		swiftOperatorSymbol(symbol) {
		return true
	}
	for offset := 0; offset < len(symbol); {
		_, end, _, ok := swiftIdentifierAt(symbol, offset)
		if !ok || end <= offset {
			return false
		}
		if end == len(symbol) {
			return true
		}
		switch {
		case symbol[end] == '.':
			offset = end + 1
		case strings.HasPrefix(symbol[end:], "::"):
			offset = end + 2
		default:
			return false
		}
		if offset >= len(symbol) || symbol[offset] == '.' || symbol[offset] == ':' {
			return false
		}
	}
	return false
}

func (swift swiftLanguage) definitionSymbol(line string) (string, bool) {
	for _, definition := range swift.sourceDefinitions([]string{line}) {
		if definition.line == 1 {
			return definition.symbol, true
		}
	}
	return "", false
}

func (swift swiftLanguage) sourceDefinitions(lines []string) []sourceDefinition {
	analysis := swift.sourceAnalysis(lines)
	if analysis == nil {
		return nil
	}
	return append([]sourceDefinition(nil), analysis.definitions...)
}

func (swift swiftLanguage) prepareFindScopeResolver(lines []string) preparedFindScopeResolver {
	analysis := swift.sourceAnalysis(lines)
	if analysis == nil {
		return nil
	}
	return analysis.scopeResolver
}

func (swift swiftLanguage) enclosingScope(lines []string, lineNo int) (int, int) {
	if lineNo < 1 || lineNo > len(lines) {
		return lineNo, lineNo
	}
	analysis := swift.sourceAnalysis(lines)
	if analysis == nil || analysis.scopeResolver == nil {
		return lineNo, lineNo
	}
	return analysis.scopeResolver.enclosingScope(lineNo)
}

func (swift swiftLanguage) navigationScope(lines []string, lineNo int) (int, int) {
	if lineNo < 1 || lineNo > len(lines) {
		return lineNo, lineNo
	}
	analysis := swift.sourceAnalysis(lines)
	if analysis == nil || analysis.scopeResolver == nil {
		return lineNo, lineNo
	}
	return analysis.scopeResolver.navigationScope(lineNo)
}

func (swift swiftLanguage) scopeNameOnLine(lines []string, lineNo int) (string, bool) {
	if lineNo < 1 || lineNo > len(lines) {
		return "", false
	}
	analysis := swift.sourceAnalysis(lines)
	if analysis == nil || analysis.scopeResolver == nil {
		return "", false
	}
	return analysis.scopeResolver.scopeName(lineNo), true
}

func (swift swiftLanguage) importRange(lines []string) (int, int, bool) {
	analysis := swift.sourceAnalysis(lines)
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

func (swift swiftLanguage) cleanSource(source string, dropComments, _ bool) string {
	if !dropComments {
		return source
	}
	return dropBlankArtifactLines(maskSwiftSearchSource(source, true, false))
}

func (swiftLanguage) stripComment(line string) string {
	return strings.TrimRight(maskSwiftSearchSource(line, true, false), " \t")
}

func (swift swiftLanguage) cleanSourceLines(
	lines []string,
	dropComments, _ bool,
) []string {
	if len(lines) == 0 {
		return nil
	}
	if !dropComments {
		return append([]string(nil), lines...)
	}
	analysis := swift.sourceAnalysis(lines)
	if analysis == nil {
		return append([]string(nil), lines...)
	}
	masked := strings.Split(maskSwiftSearchSource(analysis.source, true, false), "\n")
	for index := range masked {
		masked[index] = strings.TrimRight(masked[index], " \t")
	}
	return masked
}

func (swiftLanguage) finalizeSourceSnippet(source string, dropComments, _ bool) string {
	if !dropComments {
		return source
	}
	return dropBlankArtifactLines(source)
}

func (swift swiftLanguage) ignoredSearchLines(
	lines []string,
	dropComments, _ bool,
) map[int]bool {
	ignored := make(map[int]bool)
	if len(lines) == 0 || !dropComments {
		return ignored
	}
	analysis := swift.sourceAnalysis(lines)
	if analysis == nil {
		return ignored
	}
	masked := strings.Split(maskSwiftSearchSource(analysis.source, true, false), "\n")
	for index := range lines {
		if index < len(masked) && masked[index] != lines[index] &&
			strings.TrimSpace(masked[index]) == "" {
			ignored[index+1] = true
		}
	}
	return ignored
}

func (swift swiftLanguage) searchLines(
	lines []string,
	noComments, noStrings bool,
) []string {
	if len(lines) == 0 {
		return nil
	}
	if !noComments && !noStrings {
		return append([]string(nil), lines...)
	}
	analysis := swift.sourceAnalysis(lines)
	if analysis == nil {
		return append([]string(nil), lines...)
	}
	return strings.Split(maskSwiftSearchSource(
		analysis.source, noComments, noStrings,
	), "\n")
}

func maskSwiftSearchSource(source string, maskComments, maskStrings bool) string {
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
	sink := swiftLexicalSink{}
	if maskComments {
		sink.comment = mask
	}
	if maskStrings {
		sink.literal = mask
	} else if maskComments {
		// Enter interpolation expressions so comments there remain comments.
		sink.literal = func(cByteSpan) bool { return true }
	}
	walkSwiftLexically(source, sink)
	return string(masked)
}

func (swiftLanguage) countSymbolOccurrences(line, symbol string) int {
	if line == "" || symbol == "" {
		return 0
	}
	count := 0
	requireQuoted := swiftHardKeyword(symbol) && symbol != "init" &&
		symbol != "deinit" && symbol != "subscript"
	var previous swiftToken
	hasPrevious := false
	walkSwiftLexically(line, swiftLexicalSink{token: func(token swiftToken) bool {
		if token.text == symbol {
			specialMember := hasPrevious && (previous.text == "." || previous.text == "::") &&
				!token.quotedIdentifier && (symbol == "self" || symbol == "Self" ||
				symbol == "Type" || symbol == "Protocol")
			memberKeyword := requireQuoted && hasPrevious &&
				(previous.text == "." || previous.text == "::") &&
				symbol != "self" && symbol != "Self" && symbol != "Type" &&
				symbol != "Protocol"
			if token.kind == swiftTokenIdentifier && !specialMember &&
				(!requireQuoted || token.quotedIdentifier || memberKeyword) {
				count++
			} else if swiftOperatorSymbol(symbol) && token.kind == swiftTokenPunctuation {
				count++
			}
		}
		previous = token
		hasPrevious = true
		return true
	}})
	return count
}

func (swiftLanguage) prepareSymbolOccurrenceCounter(symbol string) func(string) int {
	return func(line string) int {
		return swiftLanguage{}.countSymbolOccurrences(line, symbol)
	}
}

func (swift swiftLanguage) walkAdditionalSymbolOccurrences(
	lines []string,
	_ string,
	visit func(lineNo, additionalCount int) bool,
) bool {
	if visit == nil {
		return false
	}
	for index := range lines {
		if !visit(index+1, 0) {
			break
		}
	}
	return true
}

func (swift swiftLanguage) symbolOnLine(lines []string, lineNo int) (string, bool) {
	if lineNo < 1 || lineNo > len(lines) {
		return "", false
	}
	analysis := swift.sourceAnalysis(lines)
	if analysis == nil {
		return "", false
	}
	maskedLines := swift.searchLines(lines, true, true)
	if lineNo <= len(maskedLines) {
		if symbol, ok := swiftSemanticSymbolOnLine(maskedLines[lineNo-1], true); ok {
			return symbol, true
		}
	}
	for _, definition := range analysis.definitions {
		if definition.line == lineNo {
			return definition.symbol, true
		}
	}
	if lineNo <= len(maskedLines) {
		return swiftSemanticSymbolOnLine(maskedLines[lineNo-1], false)
	}
	return "", false
}

func (swiftLanguage) authoritativeSymbolOnLine() {}

func swiftSemanticSymbolOnLine(line string, requireExpression bool) (string, bool) {
	tokens := make([]swiftToken, 0, 24)
	walkSwiftLexically(line, swiftLexicalSink{token: func(token swiftToken) bool {
		tokens = append(tokens, token)
		return true
	}})
	start := 0
	if requireExpression {
		start = swiftExpressionTokenStart(tokens)
		if start < 0 {
			return "", false
		}
	}

	// The terminal component of a member path, module selector, key path, or
	// #selector is the most useful symbol for Inspect.
	for index := len(tokens) - 1; index >= start; index-- {
		if !swiftSemanticNameToken(tokens, index) {
			continue
		}
		if index > start && (tokens[index-1].text == "." || tokens[index-1].text == "::") {
			return tokens[index].text, true
		}
	}
	// Calls (including freestanding macros) outrank ordinary value operands.
	for index := len(tokens) - 1; index >= start; index-- {
		if !swiftSemanticNameToken(tokens, index) {
			continue
		}
		if index+1 < len(tokens) && tokens[index+1].text == "(" {
			return tokens[index].text, true
		}
	}
	for index := start; index < len(tokens); index++ {
		if swiftSemanticNameToken(tokens, index) {
			return tokens[index].text, true
		}
		if swiftOperatorSymbol(tokens[index].text) {
			return tokens[index].text, true
		}
	}
	return "", false
}

func swiftSemanticNameToken(tokens []swiftToken, index int) bool {
	if index < 0 || index >= len(tokens) {
		return false
	}
	if index > 0 && !tokens[index].quotedIdentifier &&
		(tokens[index-1].text == "." || tokens[index-1].text == "::") &&
		(tokens[index].text == "self" || tokens[index].text == "Self" ||
			tokens[index].text == "Type" || tokens[index].text == "Protocol") {
		return false
	}
	if swiftNameToken(tokens[index]) {
		return true
	}
	if tokens[index].kind != swiftTokenIdentifier || tokens[index].quotedIdentifier ||
		!swiftHardKeyword(tokens[index].text) || index == 0 {
		return false
	}
	if tokens[index].text == "self" || tokens[index].text == "Self" ||
		tokens[index].text == "Type" || tokens[index].text == "Protocol" {
		return false
	}
	return tokens[index-1].text == "." || tokens[index-1].text == "::"
}

func swiftExpressionTokenStart(tokens []swiftToken) int {
	paren, bracket := 0, 0
	for index, token := range tokens {
		switch token.text {
		case "(":
			paren++
		case ")":
			paren = max(0, paren-1)
		case "[":
			bracket++
		case "]":
			bracket = max(0, bracket-1)
		case "=":
			if paren == 0 && bracket == 0 && index+1 < len(tokens) {
				return index + 1
			}
		}
	}
	return -1
}
