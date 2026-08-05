package repoview

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
				current.scopeEnd = max(current.scopeEnd, definition.scopeEnd)
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
		wrapped := []string{"class __RepoViewLineHost__ {", line, "}", "}"}
		for _, definition := range csharp.sourceDefinitions(wrapped) {
			if definition.line == 2 && definition.symbol != "__RepoViewLineHost__" {
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
				}
				offset = end
				continue
			}
			_, size := utf8.DecodeRuneInString(line[offset:])
			offset += max(size, 1)
		}
		return count
	}
	return csharpCountCompositeOccurrences(line, symbol)
}

func (csharpLanguage) prepareSymbolOccurrenceCounter(
	symbol string,
) func(string) int {
	return func(line string) int {
		return csharpLanguage{}.countSymbolOccurrences(line, symbol)
	}
}

func csharpCountCompositeOccurrences(line, symbol string) int {
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
		}
		start = position + max(1, len(symbol))
	}
	return count
}

func (csharp csharpLanguage) walkAdditionalSymbolOccurrences(
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
