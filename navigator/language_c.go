package navigator

import (
	"sort"
	"strings"
	"unicode/utf8"
)

type cLanguage struct {
	analysis *cSourceAnalysis
	languageDefinition
}

type cSourceAnalysis struct {
	tree          *cSyntaxTree
	scopeResolver *cPreparedFindScopeResolver
	source        string
	lineStarts    []int
	lineStorage   []string
	lineSnapshot  []string
	recoverySpans []cByteSpan
	definitions   []sourceDefinition
	scopes        []cLineScope
	imports       []cLineSpan
	lexed         cLexResult
	lineCount     int
}

func newCLanguage() cLanguage {
	return cLanguage{languageDefinition: newLanguageDefinition(
		"c",
		nil,
		nil,
		nil,
		commentStyleCLike,
		false,
	)}
}

func registerCLanguage(registry map[string]languageBackend) {
	registerLanguage(registry, newCLanguage(), ".c", ".h")
}

func (c cLanguage) prepareSource(lines []string) languageBackend {
	if len(lines) == 0 {
		c.analysis = nil
		return c
	}
	c.analysis = analyzeCSource(strings.Join(lines, "\n"), len(lines))
	c.analysis.lineStorage = lines
	c.analysis.lineSnapshot = append([]string(nil), lines...)
	return c
}

func (c cLanguage) sourceAnalysis(lines []string) *cSourceAnalysis {
	if len(lines) == 0 {
		return nil
	}
	if c.analysis != nil && cSameLineStorage(c.analysis.lineStorage, lines) &&
		cSameLines(c.analysis.lineSnapshot, lines) {
		return c.analysis
	}
	source := strings.Join(lines, "\n")
	if c.analysis != nil && c.analysis.source == source && c.analysis.lineCount == len(lines) {
		return c.analysis
	}
	return analyzeCSource(source, len(lines))
}

func analyzeCSource(source string, lineCount int) *cSourceAnalysis {
	analysis := &cSourceAnalysis{
		source:     source,
		lineCount:  lineCount,
		lineStarts: cLineStarts(source),
		lexed:      lexC(source),
	}
	analysis.tree, _ = parseCSyntaxWithLexed(source, analysis.lexed)
	analysis.recoverySpans = cSyntaxErrorSpans(analysis.tree, len(source))
	concreteDefinitions := cTreeDefinitions(source, lineCount, analysis.tree)
	concreteDefinitions = cFilterConcreteSpliceFragments(
		concreteDefinitions,
		analysis.lineStarts,
		analysis.lexed.tokens,
	)
	analysis.definitions = cCombinedDefinitions(
		lineCount,
		concreteDefinitions,
		analysis.lexed.definitions,
		analysis.tree != nil,
		analysis.recoverySpans,
		analysis.lineStarts,
		analysis.lexed.trustedDefinitions,
		analysis.lexed.recoveredDefinitions,
	)
	analysis.scopes = cCombinedScopes(
		lineCount,
		cTreeScopes(source, analysis.tree),
		analysis.lexed.scopes,
		analysis.definitions,
		analysis.tree != nil,
		analysis.recoverySpans,
		analysis.lineStarts,
	)
	analysis.imports = cCombinedImports(
		lineCount,
		cTreeImports(source, analysis.tree),
		analysis.lexed.imports,
	)
	analysis.scopeResolver = newCPreparedFindScopeResolver(
		analysis.definitions,
		analysis.scopes,
		lineCount,
	)
	return analysis
}

func cSameLineStorage(first, second []string) bool {
	return len(first) == len(second) && len(first) > 0 && &first[0] == &second[0]
}

func cSameLines(first, second []string) bool {
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

func (c cLanguage) definitionSymbol(line string) (string, bool) {
	for _, definition := range c.sourceDefinitions([]string{line}) {
		if definition.line == 1 {
			return definition.symbol, true
		}
	}
	return "", false
}

func (c cLanguage) sourceDefinitions(lines []string) []sourceDefinition {
	analysis := c.sourceAnalysis(lines)
	if analysis == nil {
		return nil
	}
	return append([]sourceDefinition(nil), analysis.definitions...)
}

type cDefinitionIdentity struct {
	symbol       string
	line, column int
}

func cDefinitionKey(definition sourceDefinition) cDefinitionIdentity {
	return cDefinitionIdentity{
		symbol: definition.symbol,
		line:   definition.line,
		column: definition.column,
	}
}

func cCombinedDefinitions(
	lineCount int,
	concrete, lexical []sourceDefinition,
	hasTree bool,
	recoverySpans []cByteSpan,
	lineStarts []int,
	trusted, recovered map[cDefinitionIdentity]bool,
) []sourceDefinition {
	definitions := make([]sourceDefinition, 0, len(concrete)+len(lexical))
	seen := make(map[cDefinitionIdentity]int, len(concrete)+len(lexical))
	appendCandidate := func(candidate sourceDefinition) {
		candidate = normalizeCDefinition(candidate, lineCount)
		if candidate.symbol == "" || !cSourceIdentifier(candidate.symbol) {
			return
		}
		key := cDefinitionKey(candidate)
		if index, exists := seen[key]; exists {
			current := &definitions[index]
			if candidate.ownsScope && !current.ownsScope {
				current.scopeStart = candidate.scopeStart
				current.scopeEnd = candidate.scopeEnd
				current.ownedEndColumn = candidate.ownedEndColumn
				current.ownsScope = true
			} else if candidate.ownsScope == current.ownsScope {
				current.scopeStart = min(current.scopeStart, candidate.scopeStart)
				if candidate.scopeEnd > current.scopeEnd {
					current.scopeEnd = candidate.scopeEnd
					current.ownedEndColumn = candidate.ownedEndColumn
				} else if candidate.scopeEnd == current.scopeEnd &&
					current.ownedEndColumn == 0 {
					current.ownedEndColumn = candidate.ownedEndColumn
				}
			}
			return
		}
		seen[key] = len(definitions)
		definitions = append(definitions, candidate)
	}
	for _, candidate := range concrete {
		appendCandidate(candidate)
	}
	for _, candidate := range lexical {
		candidate = normalizeCDefinition(candidate, lineCount)
		if candidate.symbol == "" || !cSourceIdentifier(candidate.symbol) {
			continue
		}
		key := cDefinitionKey(candidate)
		if _, concreteDuplicate := seen[key]; concreteDuplicate {
			// The concrete parser owns metadata in clean syntax. In particular,
			// a lexical approximation must never widen a precise tree scope.
			continue
		}
		if hasTree && !trusted[key] && !recovered[key] &&
			!cDefinitionTouchesRecovery(candidate, recoverySpans, lineStarts) {
			continue
		}
		appendCandidate(candidate)
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

func cFilterConcreteSpliceFragments(
	definitions []sourceDefinition,
	lineStarts []int,
	tokens []cToken,
) []sourceDefinition {
	filtered := definitions[:0]
	for _, definition := range definitions {
		if definition.line < 1 || definition.line > len(lineStarts) || definition.column < 1 {
			continue
		}
		anchor := lineStarts[definition.line-1] + definition.column - 1
		tokenIndex := sort.Search(len(tokens), func(index int) bool {
			return tokens[index].end > anchor
		})
		if tokenIndex < len(tokens) && tokens[tokenIndex].start <= anchor {
			token := tokens[tokenIndex]
			if token.start != anchor || token.text != definition.symbol {
				continue
			}
		}
		filtered = append(filtered, definition)
	}
	return filtered
}

func cDefinitionTouchesRecovery(
	definition sourceDefinition,
	recoverySpans []cByteSpan,
	lineStarts []int,
) bool {
	startLine, endLine := definition.line, definition.line
	if !definition.ownsScope {
		startLine = min(startLine, definition.scopeStart)
		endLine = max(endLine, definition.scopeEnd)
	}
	return cLineSpanTouchesByteSpans(
		startLine,
		endLine,
		lineStarts,
		recoverySpans,
	)
}

func normalizeCDefinition(definition sourceDefinition, lineCount int) sourceDefinition {
	if lineCount < 1 || definition.line < 1 || definition.line > lineCount {
		return sourceDefinition{}
	}
	if definition.column < 1 {
		definition.column = 1
	}
	if definition.scopeStart < 1 || definition.scopeStart > definition.line {
		definition.scopeStart = definition.line
	}
	if definition.scopeEnd < definition.line {
		definition.scopeEnd = definition.line
	}
	definition.scopeEnd = min(definition.scopeEnd, lineCount)
	return definition
}

func cCombinedScopes(
	lineCount int,
	concrete, lexical []cLineScope,
	definitions []sourceDefinition,
	hasTree bool,
	recoverySpans []cByteSpan,
	lineStarts []int,
) []cLineScope {
	scopes := make([]cLineScope, 0, len(concrete)+len(lexical)+len(definitions))
	scopes = append(scopes, concrete...)
	for _, scope := range lexical {
		if !hasTree ||
			cLineSpanTouchesByteSpans(scope.start, scope.end, lineStarts, recoverySpans) {
			scopes = append(scopes, scope)
		}
	}
	for _, definition := range definitions {
		if definition.ownsScope {
			scopes = append(scopes, cLineScope{
				start: definition.scopeStart,
				end:   definition.scopeEnd,
			})
		}
	}
	sort.Slice(scopes, func(first, second int) bool {
		if scopes[first].start != scopes[second].start {
			return scopes[first].start < scopes[second].start
		}
		return scopes[first].end < scopes[second].end
	})
	unique := scopes[:0]
	for _, scope := range scopes {
		if scope.start < 1 || scope.end < scope.start || scope.end > lineCount {
			continue
		}
		if len(unique) == 0 || unique[len(unique)-1] != scope {
			unique = append(unique, scope)
		}
	}
	return unique
}

func cLineSpanTouchesByteSpans(
	startLine, endLine int,
	lineStarts []int,
	spans []cByteSpan,
) bool {
	if startLine < 1 || endLine < startLine || startLine > len(lineStarts) || len(spans) == 0 {
		return false
	}
	start := lineStarts[startLine-1]
	end := int(^uint(0) >> 1)
	if endLine < len(lineStarts) {
		end = lineStarts[endLine]
	}
	spanIndex := sort.Search(len(spans), func(index int) bool {
		return spans[index].end > start
	})
	return spanIndex < len(spans) && spans[spanIndex].start < end
}

func cCombinedImports(lineCount int, groups ...[]cLineSpan) []cLineSpan {
	imports := make([]cLineSpan, 0)
	for _, group := range groups {
		imports = append(imports, group...)
	}
	sort.Slice(imports, func(first, second int) bool {
		if imports[first].start != imports[second].start {
			return imports[first].start < imports[second].start
		}
		return imports[first].end < imports[second].end
	})
	unique := imports[:0]
	for _, span := range imports {
		if span.start < 1 || span.end < span.start || span.end > lineCount {
			continue
		}
		if len(unique) == 0 || unique[len(unique)-1] != span {
			unique = append(unique, span)
		}
	}
	return unique
}

func (c cLanguage) enclosingScope(lines []string, lineNo int) (int, int) {
	if lineNo < 1 || lineNo > len(lines) {
		return lineNo, lineNo
	}
	analysis := c.sourceAnalysis(lines)
	if analysis == nil || analysis.scopeResolver == nil {
		return lineNo, lineNo
	}
	return analysis.scopeResolver.enclosingScope(lineNo)
}

func (c cLanguage) navigationScope(lines []string, lineNo int) (int, int) {
	analysis := c.sourceAnalysis(lines)
	if analysis == nil || analysis.scopeResolver == nil {
		return lineNo, lineNo
	}
	return analysis.scopeResolver.navigationScope(lineNo)
}

func (c cLanguage) scopeNameOnLine(lines []string, lineNo int) (string, bool) {
	if lineNo < 1 || lineNo > len(lines) {
		return "", false
	}
	analysis := c.sourceAnalysis(lines)
	if analysis == nil || analysis.scopeResolver == nil {
		return "", false
	}
	return analysis.scopeResolver.scopeName(lineNo), true
}

func (c cLanguage) importRange(lines []string) (int, int, bool) {
	analysis := c.sourceAnalysis(lines)
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

func (c cLanguage) cleanSource(source string, dropComments, _ bool) string {
	if !dropComments {
		return source
	}
	lines := strings.Split(source, "\n")
	return dropBlankArtifactLines(strings.Join(c.cleanSourceLines(lines, true, false), "\n"))
}

func (c cLanguage) cleanSourceLines(
	lines []string,
	dropComments, _ bool,
) []string {
	if len(lines) == 0 {
		return nil
	}
	if !dropComments {
		return append([]string(nil), lines...)
	}
	analysis := c.sourceAnalysis(lines)
	if analysis == nil {
		return append([]string(nil), lines...)
	}
	masked := strings.Split(maskCSource(analysis.source, analysis.lexed.commentSpans), "\n")
	for index := range masked {
		if index < len(lines) && masked[index] != lines[index] {
			masked[index] = strings.TrimRight(masked[index], " \t")
		}
	}
	return masked
}

func (cLanguage) finalizeSourceSnippet(source string, dropComments, _ bool) string {
	if !dropComments {
		return source
	}
	return dropBlankArtifactLines(source)
}

func (c cLanguage) ignoredSearchLines(lines []string, dropComments, _ bool) map[int]bool {
	ignored := map[int]bool{}
	if len(lines) == 0 || !dropComments {
		return ignored
	}
	analysis := c.sourceAnalysis(lines)
	if analysis == nil {
		return ignored
	}
	masked := strings.Split(maskCSource(analysis.source, analysis.lexed.commentSpans), "\n")
	for index := range lines {
		if index < len(masked) && masked[index] != lines[index] &&
			strings.TrimSpace(masked[index]) == "" {
			ignored[index+1] = true
		}
	}
	return ignored
}

func (c cLanguage) searchLines(lines []string, noComments, noStrings bool) []string {
	if len(lines) == 0 {
		return nil
	}
	if !noComments && !noStrings {
		return append([]string(nil), lines...)
	}
	analysis := c.sourceAnalysis(lines)
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

func (cLanguage) stripComment(line string) string {
	lexed := lexC(line)
	return strings.TrimRight(maskCSource(line, lexed.commentSpans), " \t")
}

func (c cLanguage) symbolOnLine(lines []string, lineNo int) (string, bool) {
	if lineNo < 1 || lineNo > len(lines) {
		return "", false
	}
	analysis := c.sourceAnalysis(lines)
	if analysis == nil {
		return "", false
	}
	for _, definition := range analysis.definitions {
		if definition.line == lineNo {
			return definition.symbol, true
		}
	}
	return cStreamSymbolOnLine(analysis, lineNo)
}

func (cLanguage) authoritativeSymbolOnLine() {}

func (cLanguage) countSymbolOccurrences(line, symbol string) int {
	if line == "" || !cSourceIdentifier(symbol) {
		return 0
	}
	return cCountValidSymbolOccurrences(line, symbol)
}

func (cLanguage) symbolOccurrenceColumns(line, symbol string) []int {
	if line == "" || !cSourceIdentifier(symbol) {
		return nil
	}
	var columns []int
	cWalkValidSymbolOccurrences(line, symbol, func(start int) {
		columns = append(columns, start+1)
	})
	return columns
}

// cCountValidSymbolOccurrences counts a symbol that the caller has already
// validated. Streaming reconciliation uses this helper for each physical
// fragment without repeatedly rescanning a potentially long symbol.
func cCountValidSymbolOccurrences(line, symbol string) int {
	return cWalkValidSymbolOccurrences(line, symbol, nil)
}

func cWalkValidSymbolOccurrences(
	line, symbol string,
	visit func(start int),
) int {
	count := 0
	for offset := 0; offset < len(line); {
		if cPreprocessingNumberStart(line, offset) {
			offset = cPreprocessingNumberEnd(line, offset)
			continue
		}
		end, ok := cIdentifierUnit(line, offset, true)
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
			next, continues := cIdentifierUnit(line, offset, false)
			if !continues {
				break
			}
			offset = next
		}
		if line[start:offset] == symbol {
			count++
			if visit != nil {
				visit(start)
			}
		}
	}
	return count
}

func cIdentifierUnit(source string, offset int, first bool) (int, bool) {
	if character, end, ok := cIdentifierUCN(source, offset); ok {
		return end, cIdentifierRune(character, first)
	}
	if offset < 0 || offset >= len(source) {
		return offset, false
	}
	character, size := utf8.DecodeRuneInString(source[offset:])
	if character == utf8.RuneError && size == 1 {
		return offset + 1, false
	}
	return offset + size, cIdentifierRune(character, first)
}

func cPreprocessingNumberStart(source string, offset int) bool {
	if offset < 0 || offset >= len(source) {
		return false
	}
	if source[offset] >= '0' && source[offset] <= '9' {
		return true
	}
	return source[offset] == '.' && offset+1 < len(source) &&
		source[offset+1] >= '0' && source[offset+1] <= '9'
}

func cPreprocessingNumberEnd(source string, start int) int {
	offset := start
	previous := byte(0)
	for offset < len(source) {
		character := source[offset]
		if character >= '0' && character <= '9' ||
			character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character == '_' || character == '.' || character == '$' || character == '\'' ||
			(character == '+' || character == '-') &&
				(previous == 'e' || previous == 'E' || previous == 'p' || previous == 'P') {
			previous = character
			offset++
			continue
		}
		if end, ok := cIdentifierUnit(source, offset, false); ok {
			previous = 0
			offset = end
			continue
		}
		break
	}
	return max(start+1, offset)
}

func cSourceIdentifier(identifier string) bool {
	if identifier == "" {
		return false
	}
	for offset, first := 0, true; offset < len(identifier); first = false {
		if character, end, ok := cIdentifierUCN(identifier, offset); ok {
			if !cIdentifierRune(character, first) {
				return false
			}
			offset = end
			continue
		}
		r, size := utf8.DecodeRuneInString(identifier[offset:])
		if r == utf8.RuneError && size == 1 || !cIdentifierRune(r, first) {
			return false
		}
		offset += size
	}
	return true
}

func cIdentifierRune(character rune, first bool) bool {
	if character == '_' || character == '$' {
		return true
	}
	// C23 adopts the Unicode XID properties. Reuse the repository's generated
	// Unicode 17 tables so concrete extraction, lexical recovery, and Find all
	// make the same decision. Dollar is retained above as a common GNU/MSVC
	// extension.
	if first {
		return rustRuneInXIDRanges(character, rustXIDStartRanges[:])
	}
	return rustRuneInXIDRanges(character, rustXIDContinueRanges[:])
}

func cIdentifierUCN(source string, start int) (rune, int, bool) {
	if start < 0 || start+2 > len(source) || source[start] != '\\' ||
		(source[start+1] != 'u' && source[start+1] != 'U') {
		return 0, start, false
	}
	digitStart := start + 2
	digitEnd := digitStart + 4
	if source[start+1] == 'U' {
		digitEnd = digitStart + 8
	}
	if digitEnd > len(source) || digitStart >= digitEnd {
		return 0, start, false
	}
	value := rune(0)
	for _, digit := range source[digitStart:digitEnd] {
		value <<= 4
		switch {
		case digit >= '0' && digit <= '9':
			value += digit - '0'
		case digit >= 'a' && digit <= 'f':
			value += digit - 'a' + 10
		case digit >= 'A' && digit <= 'F':
			value += digit - 'A' + 10
		default:
			return 0, start, false
		}
	}
	if value > utf8.MaxRune || value >= 0xD800 && value <= 0xDFFF {
		return 0, start, false
	}
	return value, digitEnd, true
}

func cKeyword(identifier string) bool {
	_, exists := cKeywords[identifier]
	return exists
}

var cKeywords = map[string]struct{}{
	"alignas": {}, "alignof": {}, "auto": {}, "bool": {}, "break": {},
	"case": {}, "char": {}, "const": {}, "constexpr": {}, "continue": {},
	"default": {}, "do": {}, "double": {}, "else": {}, "enum": {},
	"extern": {}, "false": {}, "float": {}, "for": {}, "goto": {},
	"if": {}, "inline": {}, "int": {}, "long": {}, "nullptr": {},
	"register": {}, "restrict": {}, "return": {}, "short": {}, "signed": {},
	"sizeof": {}, "static": {}, "static_assert": {}, "struct": {},
	"switch": {}, "thread_local": {}, "true": {}, "typedef": {},
	"typeof": {}, "typeof_unqual": {}, "union": {}, "unsigned": {},
	"void": {}, "volatile": {}, "while": {},
	"_Alignas": {}, "_Alignof": {}, "_Atomic": {}, "_BitInt": {},
	"_Bool": {}, "_Complex": {}, "_Decimal128": {}, "_Decimal32": {},
	"_Decimal64": {}, "_Generic": {}, "_Imaginary": {}, "_Noreturn": {},
	"_Static_assert": {}, "_Thread_local": {},
}
