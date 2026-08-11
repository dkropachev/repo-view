package navigator

import (
	"sort"
	"strings"
	"unicode/utf8"
)

type csharpLanguage struct {
	analysis *csharpSourceAnalysis
	languageDefinition
}

type csharpSourceAnalysis struct {
	tree          *csharpSyntaxTree
	scopeResolver *cPreparedFindScopeResolver
	source        string
	lineStarts    []int
	lineStorage   []string
	lineSnapshot  []string
	definitions   []sourceDefinition
	scopes        []cLineScope
	imports       []cLineSpan
	recoverySpans []cByteSpan
	lexed         csharpLexResult
	lineCount     int
}

func newCSharpLanguage() csharpLanguage {
	return csharpLanguage{languageDefinition: newLanguageDefinition(
		"cs", nil, nil, nil, commentStyleCLike, false,
	)}
}

func registerCSharpLanguage(registry map[string]languageBackend) {
	registerLanguage(registry, newCSharpLanguage(), ".cs", ".csx")
}

func (csharp csharpLanguage) prepareSource(lines []string) languageBackend {
	if len(lines) == 0 {
		csharp.analysis = nil
		return csharp
	}
	csharp.analysis = analyzeCSharpSource(strings.Join(lines, "\n"), len(lines))
	csharp.analysis.lineStorage = lines
	csharp.analysis.lineSnapshot = append([]string(nil), lines...)
	return csharp
}

func (csharp csharpLanguage) sourceAnalysis(lines []string) *csharpSourceAnalysis {
	if len(lines) == 0 {
		return nil
	}
	if csharp.analysis != nil && cSameLineStorage(csharp.analysis.lineStorage, lines) &&
		cSameLines(csharp.analysis.lineSnapshot, lines) {
		return csharp.analysis
	}
	source := strings.Join(lines, "\n")
	if csharp.analysis != nil && csharp.analysis.source == source &&
		csharp.analysis.lineCount == len(lines) {
		return csharp.analysis
	}
	return analyzeCSharpSource(source, len(lines))
}

func analyzeCSharpSource(source string, lineCount int) *csharpSourceAnalysis {
	lineCount = max(lineCount, 1)
	analysis := &csharpSourceAnalysis{
		source:     source,
		lineStarts: csharpLineStarts(source),
		lineCount:  lineCount,
		lexed:      lexCSharp(source),
	}
	analysis.tree, _ = parseCSharpSyntax(source, analysis.lexed)
	analysis.recoverySpans = cSyntaxErrorSpans(analysis.tree, len(source))

	concreteDefinitions := csharpTreeDefinitions(
		source, lineCount, analysis.tree,
	)
	concreteDefinitions = csharpDefinitionsOutsideOpaqueSpans(
		concreteDefinitions, analysis.lineStarts,
		normalizeCSpans(append(
			append([]cByteSpan(nil), analysis.lexed.commentSpans...),
			analysis.lexed.stringSpans...,
		)),
	)
	concreteScopes := csharpTreeScopes(source, lineCount, analysis.tree)
	concreteImports := csharpTreeImports(source, lineCount, analysis.tree)
	lexical := analyzeCSharpLexically(source, lineCount)

	analysis.definitions = csharpMergeDefinitions(
		lineCount,
		concreteDefinitions,
		lexical.definitions,
		analysis.tree != nil,
		analysis.recoverySpans,
		analysis.lineStarts,
		source,
	)
	analysis.scopes = csharpMergeScopes(
		lineCount,
		concreteScopes,
		lexical.scopes,
		analysis.definitions,
		analysis.tree != nil,
		analysis.recoverySpans,
		analysis.lineStarts,
	)
	// Lexical import recognition is context-aware and streams the complete
	// source, so it remains useful even for a clean concrete tree (notably for
	// .NET file-app directives that are newer than the grammar).
	analysis.imports = cCombinedImports(
		lineCount, concreteImports, lexical.imports,
	)
	analysis.scopeResolver = newCPreparedFindScopeResolver(
		analysis.definitions, analysis.scopes, lineCount,
	)
	return analysis
}

func csharpDefinitionsOutsideOpaqueSpans(
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

func csharpMergeDefinitions(
	lineCount int,
	concrete, lexical []sourceDefinition,
	hasTree bool,
	recoverySpans []cByteSpan,
	lineStarts []int,
	source string,
) []sourceDefinition {
	definitions := make([]sourceDefinition, 0, len(concrete)+len(lexical))
	seen := make(map[csharpDefinitionIdentity]int, len(concrete)+len(lexical))
	appendDefinition := func(definition sourceDefinition) {
		definition = normalizeCDefinition(definition, lineCount)
		if definition.symbol == "" || !csharpDefinitionSymbolValid(definition.symbol) {
			return
		}
		identity := csharpDefinitionIdentity{
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
					current.ownedEndColumn == 0 {
					current.ownedEndColumn = definition.ownedEndColumn
				}
			}
			return
		}
		seen[identity] = len(definitions)
		definitions = append(definitions, definition)
	}
	for _, definition := range concrete {
		// csharpTreeDefinitions already rejects declarations whose signatures
		// contain parser recovery. Body-only ERROR nodes must not erase an
		// otherwise valid owning declaration.
		appendDefinition(definition)
	}
	for _, definition := range lexical {
		identity := csharpDefinitionIdentity{
			symbol: definition.symbol, line: definition.line, column: definition.column,
		}
		if _, duplicate := seen[identity]; duplicate {
			continue
		}
		if hasTree && !csharpTrustedLexicalDefinition(source, lineStarts, definition) &&
			!csharpLexicalDefinitionMatchesPhysicalHeader(source, lineStarts, definition) &&
			!csharpDefinitionTouchesRecovery(definition, recoverySpans, lineStarts) {
			continue
		}
		appendDefinition(definition)
	}
	return csharpSortUniqueDefinitions(definitions, lineCount)
}

func csharpLexicalDefinitionMatchesPhysicalHeader(
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
	symbol, ok := csharpLineDeclarationSymbol(source[start:end])
	return ok && symbol == definition.symbol
}

func csharpDefinitionTouchesRecovery(
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

func csharpTrustedLexicalDefinition(
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
	trimmed := strings.TrimSpace(source[start:end])
	return strings.HasPrefix(trimmed, "#define ") ||
		strings.HasPrefix(trimmed, "extern alias ")
}

func csharpDefinitionSymbolValid(symbol string) bool {
	if csharpSourceIdentifier(symbol) || csharpQualifiedSourceName(symbol) || symbol == "this" {
		return true
	}
	if strings.HasPrefix(symbol, "~") {
		return csharpSourceIdentifier(strings.TrimPrefix(symbol, "~"))
	}
	return strings.HasPrefix(symbol, "operator") ||
		strings.HasPrefix(symbol, "implicit operator ") ||
		strings.HasPrefix(symbol, "explicit operator ")
}

func csharpMergeScopes(
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

func (csharp csharpLanguage) definitionSymbol(line string) (string, bool) {
	if symbol, ok := csharpLineDeclarationSymbol(line); ok {
		return symbol, true
	}
	// Most line-local probes are member declarations. Give them a valid type
	// context first so calls/control statements are not mistaken for methods
	// and fields/properties/events retain their normal grammar shape.
	if strings.Contains(line, "operator") || strings.Contains(line, "~") {
		wrapped := []string{"class __ViewLineHost__ {", line, "}", "}"}
		for _, definition := range csharp.sourceDefinitions(wrapped) {
			if definition.line == 2 && definition.symbol != "__ViewLineHost__" {
				return definition.symbol, true
			}
		}
	}
	// Namespace and using directives are illegal inside a type, so retry the
	// physical line as a compilation-unit fragment.
	for _, definition := range csharp.sourceDefinitions([]string{line}) {
		if definition.line == 1 {
			return definition.symbol, true
		}
	}
	return "", false
}

func csharpLineDeclarationSymbol(line string) (string, bool) {
	tokens := make([]csharpToken, 0, 32)
	walkCSharpLexically(line, csharpLexicalSink{token: func(token csharpToken) bool {
		tokens = append(tokens, token)
		return len(tokens) <= csharpMaximumHeaderTokens
	}})
	if len(tokens) == 0 {
		return "", false
	}
	if descriptor, ok := csharpRecoveryNamespaceDescriptor(tokens); ok {
		return descriptor.symbol, true
	}
	if descriptor, ok := csharpRecoveryTypeDescriptor(tokens); ok {
		return descriptor.symbol, true
	}
	if descriptor, ok := csharpRecoveryDelegateDescriptor(tokens); ok {
		return descriptor.symbol, true
	}
	start := csharpRecoveryDeclarationPrefix(tokens)
	if start >= len(tokens) || csharpRecoveryControlHeader(tokens[start:]) {
		return "", false
	}
	if tokens[start].text == "global" && start+1 < len(tokens) &&
		tokens[start+1].text == "using" {
		start++
	}
	if tokens[start].text == "using" {
		if alias, hasAlias, directive := csharpRecoveryUsingDirective(tokens, start); directive && hasAlias {
			return alias.text, true
		}
		return "", false
	}
	if tokens[start].text == "extern" && start+2 < len(tokens) &&
		tokens[start+1].text == "alias" && csharpRecoveryIdentifier(tokens[start+2]) {
		return tokens[start+2].text, true
	}
	frame := &csharpRecoveryFrame{kind: csharpRecoveryType}
	if callable, ok := csharpRecoveryCallableDescriptor(tokens, frame); ok {
		return callable.name.text, true
	}
	declarationEnd := len(tokens)
	for _, terminator := range []string{"{", "=>", ";"} {
		if index := csharpRecoveryTopLevelToken(tokens[:declarationEnd], terminator); index >= 0 {
			declarationEnd = min(declarationEnd, index)
		}
	}
	if declarationEnd <= start {
		return "", false
	}
	declaration := tokens[start:declarationEnd]
	if equals := csharpRecoveryTopLevelToken(declaration, "="); equals >= 0 {
		declaration = declaration[:equals]
	}
	for _, token := range declaration {
		if token.text == "this" {
			return "this", true
		}
	}
	name, ok := csharpRecoveryLastIdentifier(declaration)
	if !ok || !csharpRecoveryFieldHasType(declaration, name) ||
		csharpRecoveryCallAfterName(declaration, name) {
		return "", false
	}
	return name.text, true
}

func (csharp csharpLanguage) sourceDefinitions(lines []string) []sourceDefinition {
	analysis := csharp.sourceAnalysis(lines)
	if analysis == nil {
		return nil
	}
	return append([]sourceDefinition(nil), analysis.definitions...)
}

func (csharp csharpLanguage) prepareFindScopeResolver(
	lines []string,
) preparedFindScopeResolver {
	analysis := csharp.sourceAnalysis(lines)
	if analysis == nil {
		return nil
	}
	return analysis.scopeResolver
}

func (csharp csharpLanguage) enclosingScope(lines []string, lineNo int) (int, int) {
	if lineNo < 1 || lineNo > len(lines) {
		return lineNo, lineNo
	}
	analysis := csharp.sourceAnalysis(lines)
	if analysis == nil || analysis.scopeResolver == nil {
		return lineNo, lineNo
	}
	return analysis.scopeResolver.enclosingScope(lineNo)
}

func (csharp csharpLanguage) navigationScope(lines []string, lineNo int) (int, int) {
	if lineNo < 1 || lineNo > len(lines) {
		return lineNo, lineNo
	}
	analysis := csharp.sourceAnalysis(lines)
	if analysis == nil || analysis.scopeResolver == nil {
		return lineNo, lineNo
	}
	return analysis.scopeResolver.navigationScope(lineNo)
}

func (csharp csharpLanguage) scopeNameOnLine(
	lines []string,
	lineNo int,
) (string, bool) {
	if lineNo < 1 || lineNo > len(lines) {
		return "", false
	}
	analysis := csharp.sourceAnalysis(lines)
	if analysis == nil || analysis.scopeResolver == nil {
		return "", false
	}
	return analysis.scopeResolver.scopeName(lineNo), true
}

func (csharp csharpLanguage) importRange(lines []string) (int, int, bool) {
	analysis := csharp.sourceAnalysis(lines)
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

func (csharp csharpLanguage) cleanSource(
	source string,
	dropComments, _ bool,
) string {
	if !dropComments {
		return source
	}
	return dropBlankArtifactLines(maskCSharpSearchSource(source, true, false))
}

func (csharpLanguage) stripComment(line string) string {
	return strings.TrimRight(maskCSharpSearchSource(line, true, false), " \t")
}

func (csharp csharpLanguage) cleanSourceLines(
	lines []string,
	dropComments, _ bool,
) []string {
	if len(lines) == 0 {
		return nil
	}
	if !dropComments {
		return append([]string(nil), lines...)
	}
	analysis := csharp.sourceAnalysis(lines)
	if analysis == nil {
		return append([]string(nil), lines...)
	}
	masked := strings.Split(maskCSharpSearchSource(analysis.source, true, false), "\n")
	for index := range masked {
		masked[index] = strings.TrimRight(masked[index], " \t")
	}
	return masked
}

func (csharpLanguage) finalizeSourceSnippet(
	source string,
	dropComments, _ bool,
) string {
	if !dropComments {
		return source
	}
	return dropBlankArtifactLines(source)
}

func (csharp csharpLanguage) ignoredSearchLines(
	lines []string,
	dropComments, _ bool,
) map[int]bool {
	ignored := make(map[int]bool)
	if len(lines) == 0 || !dropComments {
		return ignored
	}
	analysis := csharp.sourceAnalysis(lines)
	if analysis == nil {
		return ignored
	}
	masked := strings.Split(maskCSharpSearchSource(analysis.source, true, false), "\n")
	for index := range lines {
		if index < len(masked) && masked[index] != lines[index] &&
			strings.TrimSpace(masked[index]) == "" {
			ignored[index+1] = true
		}
	}
	return ignored
}

func (csharp csharpLanguage) searchLines(
	lines []string,
	noComments, noStrings bool,
) []string {
	if len(lines) == 0 {
		return nil
	}
	if !noComments && !noStrings {
		return append([]string(nil), lines...)
	}
	analysis := csharp.sourceAnalysis(lines)
	if analysis == nil {
		return append([]string(nil), lines...)
	}
	return strings.Split(maskCSharpSearchSource(
		analysis.source, noComments, noStrings,
	), "\n")
}

func maskCSharpSearchSource(
	source string,
	maskComments, maskStrings bool,
) string {
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
	sink := csharpLexicalSink{}
	if maskComments {
		sink.comment = mask
	}
	if maskStrings {
		sink.literal = mask
	}
	walkCSharpLexically(source, sink)
	return string(masked)
}

func (csharpLanguage) countSymbolOccurrences(line, symbol string) int {
	if line == "" || symbol == "" || len(symbol) > len(line) {
		return 0
	}
	return csharpWalkSymbolOccurrences(line, symbol, nil)
}

func (csharpLanguage) symbolOccurrenceColumns(line, symbol string) []int {
	if line == "" || symbol == "" || len(symbol) > len(line) {
		return nil
	}
	var columns []int
	csharpWalkSymbolOccurrences(line, symbol, func(start int) {
		columns = append(columns, start+1)
	})
	return columns
}

func csharpWalkSymbolOccurrences(
	line, symbol string,
	visit func(start int),
) int {
	if csharpSourceIdentifier(symbol) {
		count := 0
		for offset := 0; offset < len(line); {
			if csharpNumberStart(line, offset) {
				offset = csharpNumberEnd(line, offset)
				continue
			}
			if end := csharpIdentifierEnd(line, offset); end > offset {
				if line[offset:end] == symbol {
					count++
					if visit != nil {
						visit(offset)
					}
				}
				offset = end
				continue
			}
			_, size := utf8.DecodeRuneInString(line[offset:])
			offset += max(size, 1)
		}
		return count
	}
	return csharpWalkCompositeOccurrences(line, symbol, visit)
}

func (csharpLanguage) prepareSymbolOccurrenceCounter(
	symbol string,
) func(string) int {
	return func(line string) int {
		return csharpLanguage{}.countSymbolOccurrences(line, symbol)
	}
}

func csharpWalkCompositeOccurrences(
	line, symbol string,
	visit func(start int),
) int {
	count := 0
	for start := 0; start+len(symbol) <= len(line); {
		relative := strings.Index(line[start:], symbol)
		if relative < 0 {
			break
		}
		position := start + relative
		end := position + len(symbol)
		before, _ := utf8.DecodeLastRuneInString(line[:position])
		after, _ := utf8.DecodeRuneInString(line[end:])
		if (position == 0 || !csharpIdentifierContinueRune(before)) &&
			(end == len(line) || !csharpIdentifierContinueRune(after)) {
			count++
			if visit != nil {
				visit(position)
			}
		}
		start = position + max(1, len(symbol))
	}
	return count
}

func (csharp csharpLanguage) walkAdditionalSymbolOccurrences(
	lines []string,
	symbol string,
	visit func(lineNo, additionalCount int) bool,
) bool {
	if visit == nil {
		return false
	}
	return csharp.walkAdditionalSymbolOccurrencesAt(
		lines, symbol,
		func(lineNo, additionalCount int, _, _ []int) bool {
			return visit(lineNo, additionalCount)
		},
	)
}

func (csharp csharpLanguage) walkAdditionalSymbolOccurrencesAt(
	lines []string,
	symbol string,
	visit func(
		lineNo, additionalCount int,
		addedColumns, removedColumns []int,
	) bool,
) bool {
	if visit == nil {
		return false
	}
	if csharpSourceIdentifier(symbol) {
		for index := range lines {
			if !visit(index+1, 0, nil, nil) {
				break
			}
		}
		return true
	}

	pattern, ok := csharpCompositeOccurrencePattern(symbol)
	if !ok {
		return false
	}
	analysis := csharp.sourceAnalysis(lines)
	if analysis == nil || len(lines) == 0 {
		return true
	}
	// Comments are trivia between C# tokens, while literals must remain
	// adjacency barriers. Interpolation expressions stay as code so qualified
	// references inside them receive the same reconciliation as ordinary code.
	code := csharpCompositeOccurrenceSource(analysis.source)
	prefix := csharpCompositeOccurrencePrefix(pattern)
	starts := make([]int, len(pattern))
	tokenCount := 0
	matched := 0
	nextLine := 1
	pendingLogicalColumns := make([]int, 0, 1)
	visitorStopped := false
	matchLines := csharpOccurrenceLineCursor{starts: analysis.lineStarts}
	frontierLines := csharpOccurrenceLineCursor{starts: analysis.lineStarts}

	emitThrough := func(lastLine int) bool {
		lastLine = min(lastLine, len(lines))
		for nextLine <= lastLine {
			lineIndex := nextLine - 1
			lineStart, lineEnd := 0, 0
			if lineIndex < len(analysis.lineStarts) {
				lineStart = analysis.lineStarts[lineIndex]
				lineEnd = len(code)
				if lineIndex+1 < len(analysis.lineStarts) {
					lineEnd = max(lineStart, analysis.lineStarts[lineIndex+1]-1)
				}
			}

			logicalIndex := 0
			physicalCount := 0
			var addedColumns, removedColumns []int
			if lineStart <= lineEnd && lineEnd <= len(code) {
				physicalCount = csharpWalkCompositeOccurrences(
					code[lineStart:lineEnd], symbol,
					func(start int) {
						column := start + 1
						for logicalIndex < len(pendingLogicalColumns) &&
							pendingLogicalColumns[logicalIndex] < column {
							addedColumns = append(
								addedColumns, pendingLogicalColumns[logicalIndex],
							)
							logicalIndex++
						}
						if logicalIndex < len(pendingLogicalColumns) &&
							pendingLogicalColumns[logicalIndex] == column {
							logicalIndex++
							return
						}
						removedColumns = append(removedColumns, column)
					},
				)
			}
			if logicalIndex == 0 && physicalCount == 0 {
				addedColumns = pendingLogicalColumns
			} else {
				addedColumns = append(
					addedColumns, pendingLogicalColumns[logicalIndex:]...,
				)
			}
			if !visit(
				nextLine,
				len(pendingLogicalColumns)-physicalCount,
				addedColumns, removedColumns,
			) {
				visitorStopped = true
				return false
			}
			nextLine++
			pendingLogicalColumns = pendingLogicalColumns[:0]
		}
		return true
	}
	record := func(lineNo, start int) bool {
		if lineNo < nextLine {
			return true
		}
		if !emitThrough(lineNo - 1) {
			return false
		}
		if lineNo <= len(analysis.lineStarts) {
			pendingLogicalColumns = append(
				pendingLogicalColumns,
				start-analysis.lineStarts[lineNo-1]+1,
			)
		}
		return true
	}
	walkCSharpLexically(code, csharpLexicalSink{token: func(token csharpToken) bool {
		starts[tokenCount%len(starts)] = token.start
		tokenCount++
		for matched > 0 && token.text != pattern[matched] {
			matched = prefix[matched-1]
		}
		if token.text == pattern[matched] {
			matched++
		}
		if matched == len(pattern) {
			start := starts[(tokenCount-len(pattern))%len(starts)]
			lineNo := matchLines.lineAt(start)
			column := 0
			if lineNo >= 1 && lineNo <= len(analysis.lineStarts) {
				column = start - analysis.lineStarts[lineNo-1] + 1
			}
			if csharpCompositeOccurrenceAllowedAt(
				analysis.scopeResolver, symbol, lineNo, column,
			) && !record(lineNo, start) {
				return false
			}
			matched = prefix[matched-1]
		}
		frontier := token.end
		if matched > 0 {
			frontier = starts[(tokenCount-matched)%len(starts)]
		}
		return emitThrough(frontierLines.lineAt(frontier) - 1)
	}})
	if !visitorStopped {
		_ = emitThrough(len(lines))
	}
	return true
}

func csharpCompositeOccurrencePattern(symbol string) ([]string, bool) {
	if symbol == "" || csharpSourceIdentifier(symbol) ||
		!csharpDefinitionSymbolValid(symbol) {
		return nil, false
	}
	pattern := make([]string, 0, 8)
	walkCSharpLexically(symbol, csharpLexicalSink{token: func(token csharpToken) bool {
		pattern = append(pattern, token.text)
		return true
	}})
	return pattern, len(pattern) > 1
}

func csharpCompositeOccurrenceAllowedAt(
	resolver *cPreparedFindScopeResolver,
	symbol string,
	lineNo, column int,
) bool {
	if !strings.HasPrefix(symbol, "~") {
		return true
	}
	if resolver == nil || lineNo < 1 || column < 1 {
		return false
	}
	for _, definitionColumn := range resolver.definitionColumns(lineNo, symbol) {
		if definitionColumn == column {
			return true
		}
	}
	return false
}

func csharpCompositeOccurrencePrefix(pattern []string) []int {
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
	return prefix
}

type csharpOccurrenceLineCursor struct {
	starts []int
	index  int
	offset int
}

func (cursor *csharpOccurrenceLineCursor) lineAt(offset int) int {
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

func csharpCompositeOccurrenceSource(source string) string {
	masked := []byte(source)
	mask := func(span cByteSpan, barrier bool) bool {
		start := max(0, min(span.start, len(masked)))
		end := max(start, min(span.end, len(masked)))
		barrierAt := -1
		for index := start; index < end; index++ {
			if masked[index] == '\n' || masked[index] == '\r' {
				continue
			}
			if barrierAt < 0 {
				barrierAt = index
			}
			masked[index] = ' '
		}
		if barrier && barrierAt >= 0 {
			masked[barrierAt] = 0
		}
		return true
	}
	walkCSharpLexically(source, csharpLexicalSink{
		comment: func(span cByteSpan) bool { return mask(span, false) },
		literal: func(span cByteSpan) bool { return mask(span, true) },
	})
	return string(masked)
}

func (csharp csharpLanguage) symbolOnLine(
	lines []string,
	lineNo int,
) (string, bool) {
	if lineNo < 1 || lineNo > len(lines) {
		return "", false
	}
	analysis := csharp.sourceAnalysis(lines)
	if analysis == nil {
		return "", false
	}
	for _, definition := range analysis.definitions {
		if definition.line == lineNo {
			return definition.symbol, true
		}
	}
	masked := csharp.searchLines(lines, true, true)[lineNo-1]
	return csharpSemanticSymbolOnLine(masked)
}

func (csharpLanguage) authoritativeSymbolOnLine() {}

func csharpSemanticSymbolOnLine(line string) (string, bool) {
	tokens := make([]csharpToken, 0)
	walkCSharpLexically(line, csharpLexicalSink{token: func(token csharpToken) bool {
		tokens = append(tokens, token)
		return true
	}})
	for index, token := range tokens {
		if token.kind != csharpTokenIdentifier || csharpKeywordToken(token) {
			continue
		}
		if index > 0 && (tokens[index-1].text == "." || tokens[index-1].text == "?." ||
			tokens[index-1].text == "::") {
			return token.text, true
		}
	}
	for index, token := range tokens {
		if token.kind != csharpTokenIdentifier || csharpKeywordToken(token) {
			continue
		}
		next := index + 1
		if next < len(tokens) && tokens[next].text == "<" {
			depth := 0
			for next < len(tokens) {
				switch tokens[next].text {
				case "<":
					depth++
				case ">":
					depth--
				case ">>":
					depth -= 2
				}
				next++
				if depth <= 0 {
					break
				}
			}
		}
		if next < len(tokens) && tokens[next].text == "(" {
			return token.text, true
		}
	}
	for _, token := range tokens {
		if token.kind == csharpTokenIdentifier && !csharpKeywordToken(token) {
			return token.text, true
		}
	}
	return "", false
}
