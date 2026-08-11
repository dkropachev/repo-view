package navigator

import (
	"sort"
	"strings"
	"unicode/utf8"
)

type pythonLanguage struct {
	analysis *pythonSourceAnalysis
	languageDefinition
}

type pythonSourceAnalysis struct {
	tree *pythonSyntaxTree

	source      string
	lexed       pythonLexResult
	definitions []sourceDefinition
	treeScopes  []pythonLineScope
	treeImports []pythonLineSpan
	scopes      []pythonLineScope
	searchMask  []pythonByteSpan
	lines       []string
	lineCount   int
}

const pythonMaximumSyntaxUnwrapDepth = 64

func newPythonLanguage() pythonLanguage {
	return pythonLanguage{
		languageDefinition: newLanguageDefinition(
			"python",
			nil,
			nil,
			nil,
			commentStylePython,
			true,
		),
	}
}

func (p pythonLanguage) prepareSource(lines []string) languageBackend {
	if len(lines) == 0 {
		p.analysis = nil
		return p
	}
	p.analysis = analyzePythonSource(strings.Join(lines, "\n"), len(lines))
	p.analysis.lines = lines
	return p
}

func (p pythonLanguage) sourceAnalysis(lines []string) *pythonSourceAnalysis {
	if len(lines) == 0 {
		return nil
	}
	if p.analysis != nil && pythonSameLineStorage(p.analysis.lines, lines) {
		return p.analysis
	}
	return p.analysisForSource(strings.Join(lines, "\n"), len(lines))
}

func pythonSameLineStorage(first, second []string) bool {
	return len(first) == len(second) && len(first) > 0 && &first[0] == &second[0]
}

func (p pythonLanguage) analysisForSource(source string, lineCount int) *pythonSourceAnalysis {
	if p.analysis != nil && p.analysis.source == source && p.analysis.lineCount == lineCount {
		return p.analysis
	}
	return analyzePythonSource(source, lineCount)
}

func analyzePythonSource(source string, lineCount int) *pythonSourceAnalysis {
	analysis := &pythonSourceAnalysis{
		source:    source,
		lexed:     lexPython(source),
		lineCount: lineCount,
	}
	analysis.tree, _ = parsePythonSyntax(source)
	structuralMask := normalizePythonSpans(append(
		append([]pythonByteSpan(nil), analysis.lexed.commentSpans...),
		analysis.lexed.literalStringSpans...,
	))
	analysis.searchMask = normalizePythonSpans(append(
		append([]pythonByteSpan(nil), analysis.lexed.commentSpans...),
		analysis.lexed.stringSpans...,
	))
	analysis.definitions = pythonDefinitions(
		source,
		lineCount,
		analysis.lexed,
		analysis.tree,
		structuralMask,
	)
	analysis.treeScopes = pythonTreeScopesFromSyntax(source, analysis.tree, structuralMask)
	analysis.treeImports = pythonTreeImportsFromSyntax(source, analysis.tree, structuralMask)
	analysis.scopes = pythonCombinedScopes(
		lineCount,
		analysis.lexed.scopes,
		analysis.treeScopes,
		analysis.definitions,
	)
	return analysis
}

func (p pythonLanguage) definitionSymbol(line string) (string, bool) {
	for _, definition := range p.sourceDefinitions([]string{line}) {
		if definition.line == 1 {
			return definition.symbol, true
		}
	}
	return "", false
}

func (p pythonLanguage) sourceDefinitions(lines []string) []sourceDefinition {
	analysis := p.sourceAnalysis(lines)
	if analysis == nil {
		return nil
	}
	return analysis.definitions
}

func pythonDefinitions(
	source string,
	lineCount int,
	lexed pythonLexResult,
	tree *pythonSyntaxTree,
	excluded []pythonByteSpan,
) []sourceDefinition {
	definitions := make([]sourceDefinition, 0, len(lexed.definitions))
	definitions = append(
		definitions,
		pythonTreeDefinitionsFromSyntax(source, tree, excluded)...,
	)

	seen := make(map[pythonDefinitionIdentity]bool, len(definitions))
	for _, definition := range definitions {
		seen[pythonDefinitionKey(definition)] = true
	}
	for _, definition := range lexed.definitions {
		definition = normalizePythonDefinition(definition, lineCount)
		if definition.symbol == "" || seen[pythonDefinitionKey(definition)] {
			continue
		}
		seen[pythonDefinitionKey(definition)] = true
		definitions = append(definitions, definition)
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

func pythonTreeDefinitionsFromSyntax(
	source string,
	tree *pythonSyntaxTree,
	excluded []pythonByteSpan,
) []sourceDefinition {
	if tree == nil {
		return nil
	}
	positions := newPythonSourcePositions(source)
	definitions := make([]sourceDefinition, 0)
	for nodeIndex, node := range tree.nodes {
		ownsScope := node.kind == "function_definition" || node.kind == "class_definition"
		// The pinned grammar also classifies calls such as type(value).attr = ...
		// as type_alias_statement. Type aliases are extracted by the lexical pass,
		// which validates their complete leading form.
		if !ownsScope {
			continue
		}
		if pythonSyntaxHasErrorAncestor(tree, nodeIndex) {
			continue
		}
		nameIndex := pythonDefinitionNameNode(tree, nodeIndex)
		if nameIndex < 0 {
			continue
		}
		nameNode := tree.nodes[nameIndex]
		if nameNode.startByte < 0 || nameNode.endByte > len(source) ||
			nameNode.startByte >= nameNode.endByte ||
			pythonByteRangeExcluded(nameNode.startByte, nameNode.endByte, excluded) {
			continue
		}
		symbol := source[nameNode.startByte:nameNode.endByte]
		if !pythonIdentifier(symbol) {
			continue
		}

		scopeNode := node
		if node.parent >= 0 && tree.nodes[node.parent].kind == "decorated_definition" {
			scopeNode = tree.nodes[node.parent]
		}
		line, column := positions.lineColumn(nameNode.startByte)
		scopeStart, scopeEnd := positions.lineSpan(scopeNode.startByte, scopeNode.endByte)
		definitions = append(definitions, sourceDefinition{
			symbol:     symbol,
			line:       line,
			column:     column,
			scopeStart: scopeStart,
			scopeEnd:   scopeEnd,
			ownsScope:  ownsScope,
		})
	}
	return definitions
}

func pythonDefinitionNameNode(tree *pythonSyntaxTree, nodeIndex int) int {
	node := tree.nodes[nodeIndex]
	if node.kind == "function_definition" || node.kind == "class_definition" {
		for _, childIndex := range node.children {
			if tree.nodes[childIndex].kind == "identifier" {
				return childIndex
			}
		}
		return -1
	}

	stack := make([]int, 0, len(node.children))
	for index := len(node.children) - 1; index >= 0; index-- {
		stack = append(stack, node.children[index])
	}
	for len(stack) > 0 {
		last := len(stack) - 1
		candidateIndex := stack[last]
		stack = stack[:last]
		candidate := tree.nodes[candidateIndex]
		if candidate.kind == "identifier" {
			return candidateIndex
		}
		for index := len(candidate.children) - 1; index >= 0; index-- {
			stack = append(stack, candidate.children[index])
		}
	}
	return -1
}

type pythonDefinitionIdentity struct {
	symbol string
	line   int
	column int
}

func pythonDefinitionKey(definition sourceDefinition) pythonDefinitionIdentity {
	return pythonDefinitionIdentity{
		symbol: definition.symbol,
		line:   definition.line,
		column: definition.column,
	}
}

func normalizePythonDefinition(definition sourceDefinition, lineCount int) sourceDefinition {
	if definition.line < 1 || definition.line > lineCount {
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
	if definition.scopeEnd > lineCount {
		definition.scopeEnd = lineCount
	}
	return definition
}

func (p pythonLanguage) enclosingScope(lines []string, lineNo int) (int, int) {
	if lineNo < 1 || lineNo > len(lines) {
		return lineNo, lineNo
	}
	analysis := p.sourceAnalysis(lines)
	if analysis == nil {
		return lineNo, lineNo
	}

	bestStart, bestEnd := 0, 0
	for _, candidate := range analysis.scopes {
		if candidate.start < 1 || candidate.end < candidate.start ||
			candidate.end > len(lines) || lineNo < candidate.start || lineNo > candidate.end {
			continue
		}
		if bestStart == 0 || candidate.end-candidate.start < bestEnd-bestStart ||
			(candidate.end-candidate.start == bestEnd-bestStart && candidate.start < bestStart) {
			bestStart, bestEnd = candidate.start, candidate.end
		}
	}
	if bestStart > 0 {
		return bestStart, bestEnd
	}
	return lineNo, lineNo
}

func pythonCombinedScopes(
	lineCount int,
	lexical, concrete []pythonLineScope,
	definitions []sourceDefinition,
) []pythonLineScope {
	decoratedStarts := make(map[pythonLineScope]int)
	for _, definition := range definitions {
		if !definition.ownsScope || definition.scopeStart >= definition.line {
			continue
		}
		key := pythonLineScope{start: definition.line, end: definition.scopeEnd}
		if previous, exists := decoratedStarts[key]; !exists || definition.scopeStart < previous {
			decoratedStarts[key] = definition.scopeStart
		}
	}

	scopes := make([]pythonLineScope, 0, len(lexical)+len(concrete))
	for _, candidate := range append(append([]pythonLineScope(nil), lexical...), concrete...) {
		if decoratedStart, exists := decoratedStarts[candidate]; exists {
			candidate.start = decoratedStart
		}
		if candidate.start < 1 || candidate.end < candidate.start || candidate.end > lineCount {
			continue
		}
		scopes = append(scopes, candidate)
	}
	sort.Slice(scopes, func(left, right int) bool {
		if scopes[left].start != scopes[right].start {
			return scopes[left].start < scopes[right].start
		}
		return scopes[left].end < scopes[right].end
	})
	unique := scopes[:0]
	for _, scope := range scopes {
		if len(unique) == 0 || unique[len(unique)-1] != scope {
			unique = append(unique, scope)
		}
	}
	return unique
}

func pythonTreeScopesFromSyntax(
	source string,
	tree *pythonSyntaxTree,
	excluded []pythonByteSpan,
) []pythonLineScope {
	if tree == nil {
		return nil
	}
	positions := newPythonSourcePositions(source)
	scopes := make([]pythonLineScope, 0)
	for nodeIndex, node := range tree.nodes {
		if !pythonTreeScopeKind(node.kind) || pythonSyntaxHasErrorAncestor(tree, nodeIndex) ||
			pythonByteRangeExcluded(node.startByte, node.endByte, excluded) {
			continue
		}
		if (node.kind == "function_definition" || node.kind == "class_definition") &&
			node.parent >= 0 && tree.nodes[node.parent].kind == "decorated_definition" {
			continue
		}
		start, end := positions.lineSpan(node.startByte, node.endByte)
		scopes = append(scopes, pythonLineScope{start: start, end: end})
	}
	return scopes
}

func pythonTreeScopeKind(kind string) bool {
	switch kind {
	case "decorated_definition", "function_definition", "class_definition",
		"if_statement", "elif_clause", "else_clause", "for_statement",
		"while_statement", "try_statement", "except_clause", "except_group_clause",
		"finally_clause", "with_statement", "match_statement", "case_clause":
		return true
	default:
		return false
	}
}

func pythonSyntaxHasErrorAncestor(tree *pythonSyntaxTree, nodeIndex int) bool {
	for nodeIndex >= 0 {
		if nodeIndex >= len(tree.nodes) || tree.nodes[nodeIndex].kind == "ERROR" {
			return true
		}
		nodeIndex = tree.nodes[nodeIndex].parent
	}
	return false
}

func (p pythonLanguage) navigationScope(lines []string, lineNo int) (int, int) {
	analysis := p.sourceAnalysis(lines)
	if analysis == nil {
		return lineNo, lineNo
	}
	bestStart, bestEnd := 0, 0
	for _, definition := range analysis.definitions {
		if !definition.ownsScope || lineNo < definition.scopeStart || lineNo > definition.scopeEnd {
			continue
		}
		if bestStart == 0 || definition.scopeEnd-definition.scopeStart < bestEnd-bestStart {
			bestStart, bestEnd = definition.scopeStart, definition.scopeEnd
		}
	}
	if bestStart > 0 {
		return bestStart, bestEnd
	}
	return p.enclosingScope(lines, lineNo)
}

func (p pythonLanguage) importRange(lines []string) (int, int, bool) {
	analysis := p.sourceAnalysis(lines)
	if analysis == nil {
		return 0, 0, false
	}
	imports := append([]pythonLineSpan(nil), analysis.lexed.imports...)
	imports = append(imports, analysis.treeImports...)
	start, end := 0, 0
	for _, statement := range imports {
		if statement.start < 1 || statement.end < statement.start || statement.end > len(lines) {
			continue
		}
		if start == 0 || statement.start < start {
			start = statement.start
		}
		if statement.end > end {
			end = statement.end
		}
	}
	return start, end, start > 0 && end >= start
}

func pythonTreeImportsFromSyntax(
	source string,
	tree *pythonSyntaxTree,
	excluded []pythonByteSpan,
) []pythonLineSpan {
	if tree == nil {
		return nil
	}
	positions := newPythonSourcePositions(source)
	imports := make([]pythonLineSpan, 0)
	for nodeIndex, node := range tree.nodes {
		if node.kind != "import_statement" && node.kind != "import_from_statement" &&
			node.kind != "future_import_statement" {
			continue
		}
		if pythonSyntaxHasErrorAncestor(tree, nodeIndex) {
			continue
		}
		if pythonByteRangeExcluded(node.startByte, node.endByte, excluded) {
			continue
		}
		start, end := positions.lineSpan(node.startByte, node.endByte)
		imports = append(imports, pythonLineSpan{start: start, end: end})
	}
	return imports
}

func (p pythonLanguage) cleanSource(source string, dropComments, dropDocstrings bool) string {
	if !dropComments && !dropDocstrings {
		return source
	}
	lines := strings.Split(source, "\n")
	cleaned := p.cleanSourceLines(lines, dropComments, dropDocstrings)
	return dropBlankArtifactLines(strings.Join(cleaned, "\n"))
}

func (p pythonLanguage) cleanSourceLines(
	lines []string,
	dropComments, dropDocstrings bool,
) []string {
	if len(lines) == 0 {
		return nil
	}
	if !dropComments && !dropDocstrings {
		return append([]string(nil), lines...)
	}
	analysis := p.sourceAnalysis(lines)
	source := analysis.source
	spans := make(
		[]pythonByteSpan,
		0,
		len(analysis.lexed.commentSpans)+len(analysis.lexed.docstringSpans),
	)
	if dropComments {
		spans = append(spans, analysis.lexed.commentSpans...)
	}
	if dropDocstrings {
		spans = append(spans, analysis.lexed.docstringSpans...)
	}
	cleaned := strings.Split(pythonMaskByteSpans(source, spans), "\n")
	for index := range cleaned {
		cleaned[index] = strings.TrimRight(cleaned[index], " \t")
	}
	return cleaned
}

func (pythonLanguage) finalizeSourceSnippet(
	source string,
	dropComments, dropDocstrings bool,
) string {
	if !dropComments && !dropDocstrings {
		return source
	}
	return dropBlankArtifactLines(source)
}

func (p pythonLanguage) ignoredSearchLines(
	lines []string,
	dropComments, dropDocstrings bool,
) map[int]bool {
	ignored := map[int]bool{}
	if len(lines) == 0 || (!dropComments && !dropDocstrings) {
		return ignored
	}
	analysis := p.sourceAnalysis(lines)
	source := analysis.source
	spans := make(
		[]pythonByteSpan,
		0,
		len(analysis.lexed.commentSpans)+len(analysis.lexed.docstringSpans),
	)
	if dropComments {
		spans = append(spans, analysis.lexed.commentSpans...)
	}
	if dropDocstrings {
		spans = append(spans, analysis.lexed.docstringSpans...)
	}
	masked := strings.Split(pythonMaskByteSpans(source, spans), "\n")
	for lineIndex := range lines {
		if masked[lineIndex] != lines[lineIndex] && strings.TrimSpace(masked[lineIndex]) == "" {
			ignored[lineIndex+1] = true
		}
	}
	return ignored
}

func (p pythonLanguage) searchLines(
	lines []string,
	noComments, noStrings bool,
) []string {
	return p.searchSourceLines(lines, noComments, noStrings, false)
}

func (p pythonLanguage) searchSourceLines(
	lines []string,
	noComments, noStrings, dropDocstrings bool,
) []string {
	if len(lines) == 0 {
		return nil
	}
	analysis := p.sourceAnalysis(lines)
	source := analysis.source
	spans := make(
		[]pythonByteSpan,
		0,
		len(analysis.lexed.commentSpans)+
			len(analysis.lexed.stringSpans)+
			len(analysis.lexed.docstringSpans),
	)
	if noComments {
		spans = append(spans, analysis.lexed.commentSpans...)
	}
	if noStrings {
		spans = append(spans, analysis.lexed.stringSpans...)
	}
	if dropDocstrings {
		spans = append(spans, analysis.lexed.docstringSpans...)
	}
	masked := pythonMaskByteSpans(source, spans)
	return strings.Split(masked, "\n")
}

func (pythonLanguage) stripComment(line string) string {
	return strings.TrimRight(maskPythonSource(line, true, false), " \t")
}

func (p pythonLanguage) symbolOnLine(lines []string, lineNo int) (string, bool) {
	if lineNo < 1 || lineNo > len(lines) {
		return "", false
	}
	analysis := p.sourceAnalysis(lines)
	if analysis == nil {
		return "", false
	}
	for _, definition := range analysis.definitions {
		if definition.line == lineNo {
			return definition.symbol, true
		}
	}
	if symbol, ok := pythonTreeSymbolOnLineFromSyntax(
		analysis.source,
		analysis.tree,
		analysis.searchMask,
		lineNo,
	); ok {
		return symbol, true
	}
	searchable := p.searchLines(lines, true, true)
	if len(searchable) != len(lines) {
		return "", false
	}
	return pythonSymbolOnLine(searchable[lineNo-1]), true
}

func pythonTreeSymbolOnLineFromSyntax(
	source string,
	tree *pythonSyntaxTree,
	excluded []pythonByteSpan,
	lineNo int,
) (string, bool) {
	if tree == nil {
		return "", false
	}
	positions := newPythonSourcePositions(source)
	for nodeIndex, node := range tree.nodes {
		if node.kind != "call" || pythonSyntaxHasErrorAncestor(tree, nodeIndex) ||
			pythonByteRangeExcluded(node.startByte, node.endByte, excluded) {
			continue
		}
		identifierIndex := pythonCalledIdentifierNode(tree, nodeIndex)
		if pythonSyntaxIdentifierOnLine(tree, identifierIndex, positions, lineNo) {
			return pythonSyntaxIdentifierText(source, tree, identifierIndex), true
		}
	}
	for nodeIndex, node := range tree.nodes {
		if node.kind != "attribute" || pythonSyntaxHasErrorAncestor(tree, nodeIndex) ||
			pythonByteRangeExcluded(node.startByte, node.endByte, excluded) {
			continue
		}
		identifierIndex := pythonAttributeIdentifierNode(tree, nodeIndex)
		if pythonSyntaxIdentifierOnLine(tree, identifierIndex, positions, lineNo) {
			return pythonSyntaxIdentifierText(source, tree, identifierIndex), true
		}
	}
	for nodeIndex, node := range tree.nodes {
		if node.kind != "identifier" || pythonSyntaxHasErrorAncestor(tree, nodeIndex) ||
			pythonByteRangeExcluded(node.startByte, node.endByte, excluded) {
			continue
		}
		if !pythonSyntaxIdentifierOnLine(tree, nodeIndex, positions, lineNo) {
			continue
		}
		symbol := pythonSyntaxIdentifierText(source, tree, nodeIndex)
		if pythonSymbolCandidate(symbol) {
			return symbol, true
		}
	}
	return "", false
}

func pythonCalledIdentifierNode(tree *pythonSyntaxTree, callIndex int) int {
	return pythonCalledIdentifierNodeAtDepth(tree, callIndex, 0)
}

func pythonCalledIdentifierNodeAtDepth(
	tree *pythonSyntaxTree,
	callIndex, depth int,
) int {
	if callIndex < 0 || callIndex >= len(tree.nodes) || depth > pythonMaximumSyntaxUnwrapDepth {
		return -1
	}
	call := tree.nodes[callIndex]
	for _, childIndex := range call.children {
		child := tree.nodes[childIndex]
		if child.kind == "argument_list" || child.kind == "(" || child.kind == ")" {
			continue
		}
		return pythonUnwrapCalledIdentifier(tree, childIndex, depth+1)
	}
	return -1
}

func pythonUnwrapCalledIdentifier(tree *pythonSyntaxTree, nodeIndex, depth int) int {
	if nodeIndex < 0 || nodeIndex >= len(tree.nodes) || depth > pythonMaximumSyntaxUnwrapDepth {
		return -1
	}
	node := tree.nodes[nodeIndex]
	switch node.kind {
	case "identifier":
		return nodeIndex
	case "attribute":
		return pythonAttributeIdentifierNode(tree, nodeIndex)
	case "call":
		return pythonCalledIdentifierNodeAtDepth(tree, nodeIndex, depth+1)
	case "subscript", "parenthesized_expression":
		for _, childIndex := range node.children {
			child := tree.nodes[childIndex]
			if child.kind == "[" || child.kind == "]" || child.kind == "(" ||
				child.kind == ")" || child.kind == "slice" {
				continue
			}
			if identifierIndex := pythonUnwrapCalledIdentifier(tree, childIndex, depth+1); identifierIndex >= 0 {
				return identifierIndex
			}
		}
	}
	return -1
}

func pythonAttributeIdentifierNode(tree *pythonSyntaxTree, attributeIndex int) int {
	if attributeIndex < 0 || attributeIndex >= len(tree.nodes) {
		return -1
	}
	attribute := tree.nodes[attributeIndex]
	for index := len(attribute.children) - 1; index >= 0; index-- {
		childIndex := attribute.children[index]
		if tree.nodes[childIndex].kind == "identifier" {
			return childIndex
		}
	}
	return -1
}

func pythonSyntaxIdentifierOnLine(
	tree *pythonSyntaxTree,
	nodeIndex int,
	positions pythonSourcePositions,
	lineNo int,
) bool {
	if nodeIndex < 0 || nodeIndex >= len(tree.nodes) {
		return false
	}
	node := tree.nodes[nodeIndex]
	line, _ := positions.lineColumn(node.startByte)
	return node.kind == "identifier" && line == lineNo
}

func pythonSyntaxIdentifierText(source string, tree *pythonSyntaxTree, nodeIndex int) string {
	if nodeIndex < 0 || nodeIndex >= len(tree.nodes) {
		return ""
	}
	node := tree.nodes[nodeIndex]
	if node.startByte < 0 || node.endByte > len(source) || node.startByte >= node.endByte {
		return ""
	}
	return source[node.startByte:node.endByte]
}

type pythonIdentifierOccurrence struct {
	text       string
	start, end int
}

func pythonSymbolOnLine(line string) string {
	identifiers := pythonLineIdentifiers(line)
	for _, identifier := range identifiers {
		if !pythonSymbolCandidate(identifier.text) ||
			pythonSoftKeywordHeader(line, identifier) {
			continue
		}
		after := identifier.end
		for after < len(line) && (line[after] == ' ' || line[after] == '\t' || line[after] == '\r') {
			after++
		}
		if after < len(line) && (line[after] == '(' || line[after] == '[') {
			return identifier.text
		}
	}
	for index := len(identifiers) - 1; index >= 0; index-- {
		identifier := identifiers[index]
		if !pythonSymbolCandidate(identifier.text) ||
			pythonSoftKeywordHeader(line, identifier) {
			continue
		}
		before := identifier.start - 1
		for before >= 0 && (line[before] == ' ' || line[before] == '\t') {
			before--
		}
		if before >= 0 && line[before] == '.' {
			return identifier.text
		}
	}
	for _, identifier := range identifiers {
		if pythonSymbolCandidate(identifier.text) &&
			!pythonSoftKeywordHeader(line, identifier) {
			return identifier.text
		}
	}
	return ""
}

func pythonLineIdentifiers(line string) []pythonIdentifierOccurrence {
	identifiers := make([]pythonIdentifierOccurrence, 0)
	for offset := 0; offset < len(line); {
		r, size := utf8.DecodeRuneInString(line[offset:])
		if r == utf8.RuneError && size == 1 {
			offset++
			continue
		}
		if !pythonIdentifierStart(r) {
			offset += size
			continue
		}
		start := offset
		offset += size
		for offset < len(line) {
			r, size = utf8.DecodeRuneInString(line[offset:])
			if r == utf8.RuneError && size == 1 || !pythonIdentifierContinue(r) {
				break
			}
			offset += size
		}
		identifiers = append(identifiers, pythonIdentifierOccurrence{
			text:  line[start:offset],
			start: start,
			end:   offset,
		})
	}
	return identifiers
}

func pythonIdentifier(identifier string) bool {
	if identifier == "" || !utf8.ValidString(identifier) || pythonHardKeywords[identifier] {
		return false
	}
	first := true
	for _, r := range identifier {
		if first {
			if !pythonIdentifierStart(r) {
				return false
			}
			first = false
			continue
		}
		if !pythonIdentifierContinue(r) {
			return false
		}
	}
	return !first
}

func pythonSymbolCandidate(symbol string) bool {
	return symbol != "_" && !pythonHardKeywords[symbol]
}

func pythonSoftKeywordHeader(line string, identifier pythonIdentifierOccurrence) bool {
	if identifier.text != "match" && identifier.text != "case" {
		return false
	}
	if strings.TrimSpace(line[:identifier.start]) != "" {
		return false
	}
	subjectStart := identifier.end
	for subjectStart < len(line) && (line[subjectStart] == ' ' || line[subjectStart] == '\t') {
		subjectStart++
	}
	colon := pythonTopLevelColon(line, subjectStart, len(line))
	return colon > subjectStart
}

func pythonMaskByteSpans(source string, spans []pythonByteSpan) string {
	if len(spans) == 0 {
		return source
	}
	content := []byte(source)
	for _, span := range spans {
		start, end := span.start, span.end
		if start < 0 {
			start = 0
		}
		if end > len(content) {
			end = len(content)
		}
		if start >= end {
			continue
		}
		for offset := start; offset < end; offset++ {
			if content[offset] != '\n' && content[offset] != '\r' {
				content[offset] = ' '
			}
		}
	}
	return string(content)
}

func pythonByteRangeExcluded(start, end int, spans []pythonByteSpan) bool {
	index := sort.Search(len(spans), func(index int) bool {
		return spans[index].end > start
	})
	return index < len(spans) && spans[index].start <= start && end <= spans[index].end
}

type pythonSourcePositions struct {
	source     string
	lineStarts []int
}

func newPythonSourcePositions(source string) pythonSourcePositions {
	starts := []int{0}
	for offset := range len(source) {
		if source[offset] == '\n' {
			starts = append(starts, offset+1)
		}
	}
	return pythonSourcePositions{lineStarts: starts, source: source}
}

func (positions pythonSourcePositions) lineColumn(offset int) (int, int) {
	if offset < 0 {
		offset = 0
	}
	if offset > len(positions.source) {
		offset = len(positions.source)
	}
	lineIndex := sort.Search(len(positions.lineStarts), func(index int) bool {
		return positions.lineStarts[index] > offset
	}) - 1
	if lineIndex < 0 {
		lineIndex = 0
	}
	return lineIndex + 1, offset - positions.lineStarts[lineIndex] + 1
}

func (positions pythonSourcePositions) lineSpan(start, end int) (int, int) {
	startLine, _ := positions.lineColumn(start)
	endOffset := end
	for endOffset > start &&
		(positions.source[endOffset-1] == '\n' || positions.source[endOffset-1] == '\r') {
		endOffset--
	}
	if endOffset > start {
		endOffset--
	}
	endLine, _ := positions.lineColumn(endOffset)
	if endLine < startLine {
		endLine = startLine
	}
	return startLine, endLine
}
