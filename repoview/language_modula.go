package repoview

import (
	"sort"
	"strings"
	"unicode/utf8"
)

// modulaLanguage keeps source-specific parsing results on an immutable
// prepared copy. The registry value remains stateless and safe to share.
type modulaLanguage struct {
	analysis *modulaSourceAnalysis
	languageDefinition
}

type modulaSourceAnalysis struct {
	tree          *modulaSyntaxTree
	scopeResolver *cPreparedFindScopeResolver
	source        string
	lineStorage   []string
	lineSnapshot  []string
	lineStarts    []int
	definitions   []sourceDefinition
	scopes        []cLineScope
	imports       []cLineSpan
	recoverySpans []cByteSpan
	lexed         modulaLexResult
	lineCount     int
	gated         bool
}

func newModulaLanguage() modulaLanguage {
	return modulaLanguage{languageDefinition: newLanguageDefinition(
		"mod", nil, nil, nil, commentStyleCLike, false,
	)}
}

func registerModulaLanguage(registry map[string]languageBackend) {
	registerLanguage(registry, newModulaLanguage(), ".mod", ".def")
}

func (modula modulaLanguage) prepareSource(lines []string) languageBackend {
	if len(lines) == 0 {
		modula.analysis = nil
		return modula
	}
	modula.analysis = analyzeModulaSource(strings.Join(lines, "\n"), len(lines))
	modula.analysis.lineStorage = lines
	modula.analysis.lineSnapshot = append([]string(nil), lines...)
	return modula
}

func (modula modulaLanguage) sourceAnalysis(lines []string) *modulaSourceAnalysis {
	if len(lines) == 0 {
		return nil
	}
	if modula.analysis != nil &&
		cSameLineStorage(modula.analysis.lineStorage, lines) &&
		cSameLines(modula.analysis.lineSnapshot, lines) {
		return modula.analysis
	}
	source := strings.Join(lines, "\n")
	if modula.analysis != nil && modula.analysis.source == source &&
		modula.analysis.lineCount == len(lines) {
		return modula.analysis
	}
	return analyzeModulaSource(source, len(lines))
}

func analyzeModulaSource(source string, lineCount int) *modulaSourceAnalysis {
	lineCount = max(lineCount, 1)
	analysis := &modulaSourceAnalysis{
		source: source, lineStarts: modulaLineStarts(source), lineCount: lineCount,
		lexed: lexModula(source),
	}
	analysis.gated = modulaContentGate(analysis.lexed)
	if !analysis.gated {
		analysis.scopeResolver = newCPreparedFindScopeResolver(nil, nil, lineCount)
		return analysis
	}

	analysis.tree, _ = parseModulaSyntax(source, analysis.lexed)
	analysis.recoverySpans = modulaSyntaxErrorSpans(analysis.tree, len(source))
	concreteDefinitions := modulaTreeDefinitions(
		source, lineCount, analysis.tree,
	)
	lexical := analyzeModulaLexically(source, lineCount)
	analysis.definitions = modulaCombinedDefinitions(
		lineCount,
		concreteDefinitions,
		lexical.definitions,
		analysis.tree != nil,
		analysis.recoverySpans,
		analysis.lineStarts,
	)
	analysis.scopes = cCombinedScopes(
		lineCount,
		modulaTreeScopes(source, lineCount, analysis.tree),
		lexical.scopes,
		analysis.definitions,
		analysis.tree != nil,
		analysis.recoverySpans,
		analysis.lineStarts,
	)
	analysis.imports = cCombinedImports(
		lineCount,
		modulaTreeImports(source, lineCount, analysis.tree),
		lexical.imports,
	)
	analysis.scopeResolver = newCPreparedFindScopeResolver(
		analysis.definitions, analysis.scopes, lineCount,
	)
	return analysis
}

func modulaCombinedDefinitions(
	lineCount int,
	concrete, lexical []sourceDefinition,
	hasTree bool,
	recoverySpans []cByteSpan,
	lineStarts []int,
) []sourceDefinition {
	definitions := make([]sourceDefinition, 0, len(concrete)+len(lexical))
	seen := make(map[cDefinitionIdentity]int, len(concrete)+len(lexical))
	appendDefinition := func(definition sourceDefinition) {
		definition = normalizeCDefinition(definition, lineCount)
		if !modulaDefinitionSymbolValid(definition.symbol) {
			return
		}
		key := cDefinitionKey(definition)
		if index, exists := seen[key]; exists {
			current := &definitions[index]
			if definition.ownsScope && !current.ownsScope {
				*current = definition
			} else if definition.ownsScope == current.ownsScope {
				current.scopeStart = min(current.scopeStart, definition.scopeStart)
				if definition.scopeEnd > current.scopeEnd {
					current.scopeEnd = definition.scopeEnd
					current.ownedEndColumn = definition.ownedEndColumn
				} else if definition.scopeEnd == current.scopeEnd &&
					current.ownedEndColumn == 0 {
					current.ownedEndColumn = definition.ownedEndColumn
				}
			}
			return
		}
		seen[key] = len(definitions)
		definitions = append(definitions, definition)
	}
	for _, definition := range concrete {
		appendDefinition(definition)
	}
	for _, definition := range lexical {
		definition = normalizeCDefinition(definition, lineCount)
		if !modulaDefinitionSymbolValid(definition.symbol) {
			continue
		}
		if _, duplicate := seen[cDefinitionKey(definition)]; duplicate {
			continue
		}
		if hasTree && !cDefinitionTouchesRecovery(
			definition, recoverySpans, lineStarts,
		) {
			continue
		}
		appendDefinition(definition)
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

func modulaDefinitionSymbolValid(symbol string) bool {
	return modulaIdentifierTextValid(symbol) && !modulaKeyword(symbol)
}

func modulaIdentifierTextValid(symbol string) bool {
	if symbol == "" || !utf8.ValidString(symbol) {
		return false
	}
	for offset := 0; offset < len(symbol); {
		r, size := utf8.DecodeRuneInString(symbol[offset:])
		valid := modulaIdentifierContinue(r)
		if offset == 0 {
			valid = modulaIdentifierStart(r)
		}
		if size < 1 || !valid {
			return false
		}
		offset += size
	}
	return true
}

func (modula modulaLanguage) definitionSymbol(line string) (string, bool) {
	lexed := lexModula(line)
	if len(lexed.tokens) > 0 {
		switch lexed.tokens[0].text {
		case "MODULE", "DEFINITION", "IMPLEMENTATION":
			if !modulaContentGate(lexed) {
				return "", false
			}
		}
	}
	return modulaLineDefinitionSymbol(lexed.tokens)
}

func modulaLineDefinitionSymbol(tokens []modulaToken) (string, bool) {
	if len(tokens) == 0 || tokens[0].gap {
		return "", false
	}
	if symbol, ok := modulaLineModuleSymbol(tokens); ok {
		return symbol, true
	}
	switch tokens[0].text {
	case "PROCEDURE":
		return modulaLineProcedureSymbol(tokens)
	case "CONST":
		return modulaLineSectionSymbol(tokens[1:], "=")
	case "TYPE":
		return modulaLineSectionSymbol(tokens[1:], "=", ";")
	case "VAR":
		return modulaLineVariableSymbol(tokens[1:])
	case "IMPORT", "FROM", "EXPORT", "END":
		return "", false
	}
	if tokens[0].kind != modulaTokenIdentifier ||
		!modulaDefinitionSymbolValid(tokens[0].text) {
		return "", false
	}
	if len(tokens) > 1 && tokens[1].text == "=" {
		return tokens[0].text, true
	}
	return modulaLineVariableSymbol(tokens)
}

func modulaLineModuleSymbol(tokens []modulaToken) (string, bool) {
	index := 0
	switch tokens[index].text {
	case "MODULE":
		index++
	case "DEFINITION", "IMPLEMENTATION":
		definition := tokens[index].text == "DEFINITION"
		index++
		if index >= len(tokens) || tokens[index].text != "MODULE" {
			return "", false
		}
		index++
		if definition && index < len(tokens) && tokens[index].text == "FOR" {
			index++
			if index >= len(tokens) || !modulaGNUStringToken(tokens[index]) {
				return "", false
			}
			index++
		}
	default:
		return "", false
	}
	if index >= len(tokens) || tokens[index].kind != modulaTokenIdentifier ||
		!modulaDefinitionSymbolValid(tokens[index].text) {
		return "", false
	}
	return tokens[index].text, true
}

func modulaLineProcedureSymbol(tokens []modulaToken) (string, bool) {
	depth := 0
	for _, token := range tokens[1:] {
		switch token.text {
		case "(", "[", "{":
			depth++
			continue
		case ")", "]", "}":
			depth = max(0, depth-1)
			continue
		}
		if depth != 0 || token.kind != modulaTokenIdentifier ||
			modulaProcedureMarker(token.text) ||
			!modulaDefinitionSymbolValid(token.text) {
			continue
		}
		return token.text, true
	}
	return "", false
}

func modulaLineSectionSymbol(tokens []modulaToken, terminators ...string) (string, bool) {
	if len(tokens) < 2 || tokens[0].kind != modulaTokenIdentifier ||
		!modulaDefinitionSymbolValid(tokens[0].text) {
		return "", false
	}
	for _, token := range tokens[1:] {
		for _, terminator := range terminators {
			if token.text == terminator {
				return tokens[0].text, true
			}
		}
		if token.text == ":=" {
			return "", false
		}
	}
	return "", false
}

func modulaLineVariableSymbol(tokens []modulaToken) (string, bool) {
	if len(tokens) < 2 || tokens[0].kind != modulaTokenIdentifier ||
		!modulaDefinitionSymbolValid(tokens[0].text) {
		return "", false
	}
	for _, token := range tokens[1:] {
		switch token.text {
		case ":":
			return tokens[0].text, true
		case ":=", "=", ";":
			return "", false
		}
	}
	return "", false
}

func (modula modulaLanguage) sourceDefinitions(lines []string) []sourceDefinition {
	analysis := modula.sourceAnalysis(lines)
	if analysis == nil || !analysis.gated {
		return nil
	}
	return append([]sourceDefinition(nil), analysis.definitions...)
}

func (modula modulaLanguage) prepareFindScopeResolver(
	lines []string,
) preparedFindScopeResolver {
	analysis := modula.sourceAnalysis(lines)
	if analysis == nil {
		return nil
	}
	return analysis.scopeResolver
}

func (modula modulaLanguage) enclosingScope(lines []string, lineNo int) (int, int) {
	if lineNo < 1 || lineNo > len(lines) {
		return lineNo, lineNo
	}
	analysis := modula.sourceAnalysis(lines)
	if analysis == nil || analysis.scopeResolver == nil {
		return lineNo, lineNo
	}
	return analysis.scopeResolver.enclosingScope(lineNo)
}

func (modula modulaLanguage) navigationScope(lines []string, lineNo int) (int, int) {
	if lineNo < 1 || lineNo > len(lines) {
		return lineNo, lineNo
	}
	analysis := modula.sourceAnalysis(lines)
	if analysis == nil || analysis.scopeResolver == nil {
		return lineNo, lineNo
	}
	return analysis.scopeResolver.navigationScope(lineNo)
}

func (modula modulaLanguage) scopeNameOnLine(
	lines []string,
	lineNo int,
) (string, bool) {
	if lineNo < 1 || lineNo > len(lines) {
		return "", false
	}
	analysis := modula.sourceAnalysis(lines)
	if analysis == nil || analysis.scopeResolver == nil {
		return "", false
	}
	return analysis.scopeResolver.scopeName(lineNo), true
}

func (modula modulaLanguage) importRange(lines []string) (int, int, bool) {
	analysis := modula.sourceAnalysis(lines)
	if analysis == nil || !analysis.gated {
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

func (modula modulaLanguage) compilationUnit() bool {
	return modula.analysis == nil || modula.analysis.gated
}

func (modula modulaLanguage) cleanSource(
	source string,
	dropComments, _ bool,
) string {
	if !dropComments {
		return source
	}
	if !modula.compilationUnit() {
		return modula.languageDefinition.cleanSource(source, true, false)
	}
	return dropBlankArtifactLines(maskModulaSearchSource(source, true, false))
}

func (modula modulaLanguage) stripComment(line string) string {
	if !modula.compilationUnit() {
		return modula.languageDefinition.stripComment(line)
	}
	return strings.TrimRight(maskModulaSearchSource(line, true, false), " \t")
}

func (modula modulaLanguage) cleanSourceLines(
	lines []string,
	dropComments, _ bool,
) []string {
	if len(lines) == 0 {
		return nil
	}
	if !dropComments {
		return append([]string(nil), lines...)
	}
	analysis := modula.sourceAnalysis(lines)
	if analysis == nil {
		return append([]string(nil), lines...)
	}
	if !analysis.gated {
		return modula.languageDefinition.searchLines(lines, true, false)
	}
	masked := strings.Split(maskModulaSearchSource(analysis.source, true, false), "\n")
	for index := range masked {
		masked[index] = strings.TrimRight(masked[index], " \t")
	}
	return masked
}

func (modulaLanguage) finalizeSourceSnippet(
	source string,
	dropComments, _ bool,
) string {
	if !dropComments {
		return source
	}
	return dropBlankArtifactLines(source)
}

func (modula modulaLanguage) ignoredSearchLines(
	lines []string,
	dropComments, _ bool,
) map[int]bool {
	if len(lines) == 0 || !dropComments {
		return map[int]bool{}
	}
	analysis := modula.sourceAnalysis(lines)
	if analysis == nil {
		return map[int]bool{}
	}
	if !analysis.gated {
		return modula.languageDefinition.ignoredSearchLines(lines, true, false)
	}
	ignored := make(map[int]bool)
	masked := strings.Split(maskModulaSearchSource(analysis.source, true, false), "\n")
	for index := range lines {
		if index < len(masked) && masked[index] != lines[index] &&
			strings.TrimSpace(masked[index]) == "" {
			ignored[index+1] = true
		}
	}
	return ignored
}

func (modula modulaLanguage) searchLines(
	lines []string,
	noComments, noStrings bool,
) []string {
	if len(lines) == 0 {
		return nil
	}
	if !noComments && !noStrings {
		return append([]string(nil), lines...)
	}
	analysis := modula.sourceAnalysis(lines)
	if analysis == nil {
		return append([]string(nil), lines...)
	}
	if !analysis.gated {
		return modula.languageDefinition.searchLines(lines, noComments, noStrings)
	}
	return strings.Split(maskModulaSearchSource(
		analysis.source, noComments, noStrings,
	), "\n")
}

func maskModulaSearchSource(source string, maskComments, maskStrings bool) string {
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
	sink := modulaLexicalSink{}
	if maskComments {
		sink.comment = mask
		sink.pragma = mask
	}
	if maskStrings {
		sink.literal = mask
	}
	walkModulaLexically(source, sink)
	return string(masked)
}

func (modula modulaLanguage) countSymbolOccurrences(line, symbol string) int {
	if line == "" || symbol == "" {
		return 0
	}
	if !modula.compilationUnit() || !modulaIdentifierTextValid(symbol) {
		return countSymbolOccurrences(line, symbol)
	}
	return modulaCountValidSymbolOccurrences(line, symbol)
}

func (modula modulaLanguage) symbolOccurrenceColumns(line, symbol string) []int {
	if line == "" || symbol == "" {
		return nil
	}
	if !modula.compilationUnit() || !modulaIdentifierTextValid(symbol) {
		return independentSymbolColumns(line, symbol)
	}
	var columns []int
	modulaWalkValidSymbolOccurrences(line, symbol, func(start int) {
		columns = append(columns, start+1)
	})
	return columns
}

// modulaCountValidSymbolOccurrences scans the line after search options have
// masked comments and/or strings. It deliberately does not lex those regions
// again: when callers leave them visible, identifiers inside them are part of
// the requested search surface, including middle lines of nested comments.
func modulaCountValidSymbolOccurrences(line, symbol string) int {
	return modulaWalkValidSymbolOccurrences(line, symbol, nil)
}

func modulaWalkValidSymbolOccurrences(
	line, symbol string,
	visit func(start int),
) int {
	count := 0
	for offset := 0; offset < len(line); {
		r, size := utf8.DecodeRuneInString(line[offset:])
		if size < 1 {
			size = 1
		}
		if modulaIdentifierStart(r) {
			start := offset
			offset += size
			for offset < len(line) {
				next, nextSize := utf8.DecodeRuneInString(line[offset:])
				if nextSize < 1 || !modulaIdentifierContinue(next) {
					break
				}
				offset += nextSize
			}
			if line[start:offset] == symbol {
				count++
				if visit != nil {
					visit(start)
				}
			}
			continue
		}
		if modulaASCIIDigit(r) {
			offset = modulaNumberEnd(line, offset)
			continue
		}
		if r == '.' && modulaLeadingDotNumber(line, offset) {
			offset = modulaLeadingDotRealEnd(line, offset)
			continue
		}
		offset += size
	}
	return count
}

func (modula modulaLanguage) prepareSymbolOccurrenceCounter(
	symbol string,
) func(string) int {
	return func(line string) int {
		return modula.countSymbolOccurrences(line, symbol)
	}
}

func (modula modulaLanguage) walkAdditionalSymbolOccurrences(
	lines []string,
	symbol string,
	visit func(lineNo, additionalCount int) bool,
) bool {
	return modula.walkAdditionalSymbolOccurrencesWithVisitor(lines, symbol, visit, nil)
}

func (modula modulaLanguage) walkAdditionalSymbolOccurrencesAt(
	lines []string,
	symbol string,
	visit func(
		lineNo, additionalCount int,
		addedColumns, removedColumns []int,
	) bool,
) bool {
	return modula.walkAdditionalSymbolOccurrencesWithVisitor(lines, symbol, nil, visit)
}

func (modula modulaLanguage) walkAdditionalSymbolOccurrencesWithVisitor(
	lines []string,
	symbol string,
	visit func(lineNo, additionalCount int) bool,
	visitAt func(
		lineNo, additionalCount int,
		addedColumns, removedColumns []int,
	) bool,
) bool {
	if visit == nil && visitAt == nil {
		return false
	}
	pattern, ok := modulaQualifiedOccurrencePattern(symbol)
	if !ok {
		return false
	}
	analysis := modula.sourceAnalysis(lines)
	if analysis == nil {
		return true
	}
	if !analysis.gated {
		return false
	}
	prefix := make([]int, len(pattern))
	for index, matched := 1, 0; index < len(pattern); index++ {
		for matched > 0 && pattern[index] != pattern[matched] {
			matched = prefix[matched-1]
		}
		if pattern[index] == pattern[matched] {
			matched++
		}
		prefix[index] = matched
	}

	// One start offset per query token recovers the beginning of the current
	// KMP suffix. Memory is proportional to the query, never the source.
	starts := make([]int, len(pattern))
	tokenCount := 0
	matched := 0
	nextLine := 1
	pendingAdjustment := 0
	var pendingAddedColumns []int
	visitorStopped := false
	matchLines := modulaOccurrenceLineCursor{starts: analysis.lineStarts}
	frontierLines := modulaOccurrenceLineCursor{starts: analysis.lineStarts}

	emitThrough := func(lastLine int) bool {
		lastLine = min(lastLine, len(lines))
		for nextLine <= lastLine {
			var keepGoing bool
			if visitAt != nil {
				keepGoing = visitAt(
					nextLine, pendingAdjustment, pendingAddedColumns, nil,
				)
			} else {
				keepGoing = visit(nextLine, pendingAdjustment)
			}
			if !keepGoing {
				visitorStopped = true
				return false
			}
			nextLine++
			pendingAdjustment = 0
			pendingAddedColumns = pendingAddedColumns[:0]
		}
		return true
	}
	record := func(lineNo, start int, additional bool) bool {
		if !additional {
			return true
		}
		if lineNo < nextLine {
			return true
		}
		if !emitThrough(lineNo - 1) {
			return false
		}
		if pendingAdjustment < int(^uint(0)>>1) {
			pendingAdjustment++
		}
		if visitAt != nil &&
			lineNo >= 1 && lineNo <= len(analysis.lineStarts) {
			pendingAddedColumns = append(
				pendingAddedColumns,
				start-analysis.lineStarts[lineNo-1]+1,
			)
		}
		return true
	}
	matchFrontier := func(fallback int) int {
		if matched == 0 {
			return fallback
		}
		return starts[(tokenCount-matched)%len(starts)]
	}
	emitBeforeFrontier := func(frontier int) bool {
		return emitThrough(frontierLines.lineAt(frontier) - 1)
	}

	walkModulaLexically(analysis.source, modulaLexicalSink{
		comment: func(span cByteSpan) bool {
			// Modula comments are lexical trivia, so a qualified name may
			// continue on the other side of one.
			return emitBeforeFrontier(matchFrontier(span.end))
		},
		pragma: func(span cByteSpan) bool {
			// Preprocessor line markers are opaque source-control material, not
			// trivia within a qualified designator. Top-level GNU directives are
			// ordinary tokens and therefore break a match naturally.
			matched = 0
			return emitBeforeFrontier(span.end)
		},
		token: func(token modulaToken) bool {
			starts[tokenCount%len(starts)] = token.start
			tokenCount++

			for matched > 0 && !modulaQualifiedOccurrenceTokenEqual(
				token, pattern[matched],
			) {
				matched = prefix[matched-1]
			}
			if modulaQualifiedOccurrenceTokenEqual(token, pattern[matched]) {
				matched++
			}
			if matched == len(pattern) {
				start := starts[(tokenCount-len(pattern))%len(starts)]
				additional := !modulaRawQualifiedOccurrenceCounted(
					analysis.source, symbol, start, token.end,
				)
				if !record(matchLines.lineAt(start), start, additional) {
					return false
				}
				matched = prefix[matched-1]
			}
			return emitBeforeFrontier(matchFrontier(token.end))
		},
	})
	if !visitorStopped {
		_ = emitThrough(len(lines))
	}
	return true
}

func modulaQualifiedOccurrencePattern(symbol string) ([]string, bool) {
	components := strings.Split(symbol, ".")
	if len(components) < 2 || len(components) > int(^uint(0)>>1)/2 {
		return nil, false
	}
	pattern := make([]string, len(components)*2-1)
	for index, component := range components {
		if !modulaIdentifierTextValid(component) || modulaKeyword(component) {
			return nil, false
		}
		pattern[index*2] = component
		if index+1 < len(components) {
			pattern[index*2+1] = "."
		}
	}
	return pattern, true
}

func modulaQualifiedOccurrenceTokenEqual(token modulaToken, expected string) bool {
	if expected == "." {
		return token.kind == modulaTokenPunctuation && token.text == expected
	}
	return token.kind == modulaTokenIdentifier && token.text == expected
}

func modulaRawQualifiedOccurrenceCounted(
	source, symbol string,
	start, end int,
) bool {
	if start < 0 || end < start || end > len(source) ||
		end-start != len(symbol) || source[start:end] != symbol {
		return false
	}
	before, _ := utf8.DecodeLastRuneInString(source[:start])
	after, _ := utf8.DecodeRuneInString(source[end:])
	return (start == 0 || !isIdent(before)) &&
		(end == len(source) || !isIdent(after))
}

type modulaOccurrenceLineCursor struct {
	starts []int
	index  int
	offset int
}

func (cursor *modulaOccurrenceLineCursor) lineAt(offset int) int {
	if cursor == nil || len(cursor.starts) == 0 {
		return 1
	}
	if offset < cursor.offset {
		cursor.index = 0
	}
	cursor.offset = max(0, offset)
	for cursor.index+1 < len(cursor.starts) &&
		cursor.starts[cursor.index+1] <= cursor.offset {
		cursor.index++
	}
	return cursor.index + 1
}

func (modula modulaLanguage) symbolOnLine(
	lines []string,
	lineNo int,
) (string, bool) {
	if lineNo < 1 || lineNo > len(lines) {
		return "", false
	}
	analysis := modula.sourceAnalysis(lines)
	if analysis == nil {
		return "", false
	}
	if !analysis.gated {
		return modulaInertSymbolOnLine(lines[lineNo-1])
	}
	for _, definition := range analysis.definitions {
		if definition.line == lineNo {
			return definition.symbol, true
		}
	}
	masked := modula.searchLines(lines, true, true)
	if lineNo > len(masked) {
		return "", false
	}
	return modulaSemanticSymbolOnLine(masked[lineNo-1])
}

func (modulaLanguage) authoritativeSymbolOnLine() {}

func modulaInertSymbolOnLine(line string) (string, bool) {
	line = strings.TrimSpace(stripSlashComment(line))
	fields := strings.Fields(line)
	if len(fields) == 0 {
		return "", false
	}
	trimToken := func(token string) string {
		return strings.Trim(token, "()[]{}\"'`,;")
	}
	if len(fields) > 1 {
		switch fields[0] {
		case "module", "require", "replace", "exclude":
			if symbol := trimToken(fields[1]); symbol != "" {
				return symbol, true
			}
		}
	}
	if symbol := trimToken(fields[0]); strings.ContainsAny(symbol, "./") {
		return symbol, true
	}
	lexed := lexModula(line)
	for _, token := range lexed.tokens {
		if token.kind == modulaTokenIdentifier {
			return token.text, true
		}
	}
	return "", false
}

func modulaSemanticSymbolOnLine(line string) (string, bool) {
	tokens := lexModula(line).tokens
	for index := len(tokens) - 1; index > 0; index-- {
		if tokens[index].kind == modulaTokenIdentifier &&
			modulaDefinitionSymbolValid(tokens[index].text) &&
			tokens[index-1].text == "." {
			return tokens[index].text, true
		}
	}
	for index := len(tokens) - 2; index >= 0; index-- {
		if tokens[index].kind == modulaTokenIdentifier &&
			modulaDefinitionSymbolValid(tokens[index].text) &&
			tokens[index+1].text == "(" {
			return tokens[index].text, true
		}
	}
	for _, token := range tokens {
		if token.kind == modulaTokenIdentifier &&
			modulaDefinitionSymbolValid(token.text) {
			return token.text, true
		}
	}
	return "", false
}
