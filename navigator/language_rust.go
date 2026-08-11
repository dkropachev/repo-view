package navigator

import (
	"sort"
	"strings"
	"unicode/utf8"
)

type rustLanguage struct {
	analysis *rustSourceAnalysis
	languageDefinition
}

type rustSourceAnalysis struct {
	tree *rustSyntaxTree

	source        string
	lexed         rustLexResult
	definitions   []sourceDefinition
	treeScopes    []rustLineScope
	treeImports   []rustLineSpan
	scopes        []rustLineScope
	searchMask    []rustByteSpan
	recoverySpans []rustByteSpan
	recoveryLines map[int]bool
	lines         []string
	lineSnapshot  []string
	lineCount     int
}

const rustMaximumSyntaxUnwrapDepth = 64

func newRustLanguage() rustLanguage {
	return rustLanguage{
		languageDefinition: newLanguageDefinition(
			"rust",
			nil,
			nil,
			nil,
			commentStyleCLike,
			false,
		),
	}
}

func (r rustLanguage) prepareSource(lines []string) languageBackend {
	if len(lines) == 0 {
		r.analysis = nil
		return r
	}
	r.analysis = analyzeRustSource(strings.Join(lines, "\n"), len(lines))
	r.analysis.lines = lines
	r.analysis.lineSnapshot = append([]string(nil), lines...)
	return r
}

func (r rustLanguage) sourceAnalysis(lines []string) *rustSourceAnalysis {
	if len(lines) == 0 {
		return nil
	}
	if r.analysis != nil && rustSameLineStorage(r.analysis.lines, lines) &&
		rustSameLines(r.analysis.lineSnapshot, lines) {
		return r.analysis
	}
	return r.analysisForSource(strings.Join(lines, "\n"), len(lines))
}

func rustSameLineStorage(first, second []string) bool {
	return len(first) == len(second) && len(first) > 0 && &first[0] == &second[0]
}

func rustSameLines(first, second []string) bool {
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

func (r rustLanguage) analysisForSource(source string, lineCount int) *rustSourceAnalysis {
	if r.analysis != nil && r.analysis.source == source && r.analysis.lineCount == lineCount {
		return r.analysis
	}
	return analyzeRustSource(source, lineCount)
}

func analyzeRustSource(source string, lineCount int) *rustSourceAnalysis {
	analysis := &rustSourceAnalysis{
		source:    source,
		lexed:     lexRust(source),
		lineCount: lineCount,
	}
	analysis.tree, _ = parseRustSyntax(source)
	attachedStarts := rustSyntaxAttachedStarts(source, analysis.tree)
	analysis.recoverySpans = rustSyntaxErrorSpans(analysis.tree, len(source))
	analysis.recoveryLines = rustRecoveryLines(
		source,
		analysis.recoverySpans,
	)
	analysis.searchMask = normalizeRustSpans(append(
		append([]rustByteSpan(nil), analysis.lexed.commentSpans...),
		analysis.lexed.stringSpans...,
	))
	syntaxExcluded := normalizeRustSpans(append(
		append([]rustByteSpan(nil), analysis.searchMask...),
		analysis.lexed.syntaxOpaqueSpans...,
	))
	analysis.definitions = rustDefinitions(
		source,
		lineCount,
		analysis.lexed,
		analysis.tree,
		syntaxExcluded,
		attachedStarts,
		analysis.recoverySpans,
		analysis.recoveryLines,
	)
	analysis.treeScopes = rustTreeScopesFromSyntax(
		source,
		analysis.tree,
		syntaxExcluded,
		attachedStarts,
	)
	analysis.treeImports = rustTreeImportsFromSyntax(
		source,
		analysis.tree,
		syntaxExcluded,
		attachedStarts,
	)
	analysis.scopes = rustCombinedScopes(
		lineCount,
		analysis.lexed.scopes,
		analysis.treeScopes,
		analysis.definitions,
		analysis.tree != nil,
	)
	return analysis
}

func (r rustLanguage) definitionSymbol(line string) (string, bool) {
	for _, definition := range r.sourceDefinitions([]string{line}) {
		if definition.line == 1 {
			return definition.symbol, true
		}
	}
	return "", false
}

func (r rustLanguage) sourceDefinitions(lines []string) []sourceDefinition {
	analysis := r.sourceAnalysis(lines)
	if analysis == nil {
		return nil
	}
	return analysis.definitions
}

func (r rustLanguage) prepareFindScopeResolver(
	lines []string,
) preparedFindScopeResolver {
	analysis := r.sourceAnalysis(lines)
	if analysis == nil {
		return nil
	}
	scopes := make([]cLineScope, 0, len(analysis.scopes))
	for _, scope := range analysis.scopes {
		scopes = append(scopes, cLineScope(scope))
	}
	return newCPreparedFindScopeResolver(analysis.definitions, scopes, len(lines))
}

func rustDefinitions(
	source string,
	lineCount int,
	lexed rustLexResult,
	tree *rustSyntaxTree,
	excluded []rustByteSpan,
	attachedStarts []int,
	recoverySpans []rustByteSpan,
	recoveryLines map[int]bool,
) []sourceDefinition {
	definitions := rustTreeDefinitionsFromSyntax(source, tree, lexed, excluded, attachedStarts)
	lineStarts := rustLineStarts(source)
	seen := make(map[rustDefinitionIdentity]int, len(definitions))
	for index, definition := range definitions {
		seen[rustDefinitionKey(definition)] = index
	}
	for _, lexical := range lexed.definitions {
		lexical = normalizeRustDefinition(lexical, lineCount)
		if lexical.symbol == "" {
			continue
		}
		key := rustDefinitionKey(lexical)
		if index, exists := seen[key]; exists {
			touchesRecovery := rustLexicalDefinitionTouchesRecovery(
				source, lineStarts, lexical, recoverySpans, recoveryLines,
			)
			switch {
			case lexed.recoveredDefinitions[key]:
				// A lexical hard resynchronization is direct boundary evidence.
				// It can be more precise than the concrete error span, which may
				// cover only the following keyword (or be hidden by an ERROR root).
				definitions[index].scopeStart = lexical.scopeStart
				definitions[index].scopeEnd = lexical.scopeEnd
				definitions[index].ownedEndColumn = lexical.ownedEndColumn
				definitions[index].ownsScope = lexical.ownsScope
			case lexed.trustedDefinitions[key] &&
				definitions[index].ownsScope == lexical.ownsScope:
				// A delimiter-balanced lexical item is a stronger boundary
				// oracle when the concrete grammar recovers across newer Rust
				// syntax such as `[const]` bounds or `unsafe extern` blocks.
				definitions[index].scopeStart = lexical.scopeStart
				definitions[index].scopeEnd = lexical.scopeEnd
				definitions[index].ownedEndColumn = lexical.ownedEndColumn
			case lexed.trustedDefinitions[key] && touchesRecovery &&
				lexical.scopeEnd < definitions[index].scopeEnd:
				definitions[index].scopeStart = lexical.scopeStart
				definitions[index].scopeEnd = lexical.scopeEnd
				definitions[index].ownedEndColumn = lexical.ownedEndColumn
				definitions[index].ownsScope = lexical.ownsScope
			default:
				definitions[index].scopeStart = min(
					definitions[index].scopeStart,
					lexical.scopeStart,
				)
			}
			continue
		}
		if tree != nil && !lexed.trustedDefinitions[key] &&
			!lexed.recoveredDefinitions[key] &&
			!rustLexicalDefinitionTouchesRecovery(
				source, lineStarts, lexical, recoverySpans, recoveryLines,
			) {
			continue
		}
		seen[key] = len(definitions)
		definitions = append(definitions, lexical)
	}

	normalized := definitions[:0]
	for _, definition := range definitions {
		definition = normalizeRustDefinition(definition, lineCount)
		if definition.symbol != "" {
			normalized = append(normalized, definition)
		}
	}
	definitions = normalized
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

func normalizeRustDefinition(definition sourceDefinition, lineCount int) sourceDefinition {
	if definition.line < 1 || definition.line > lineCount ||
		!rustSourceIdentifier(definition.symbol) {
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

func rustTreeDefinitionsFromSyntax(
	source string,
	tree *rustSyntaxTree,
	lexed rustLexResult,
	excluded []rustByteSpan,
	attachedStarts []int,
) []sourceDefinition {
	if tree == nil {
		return nil
	}
	positions := rustSourcePositions{source: source, lineStarts: rustLineStarts(source)}
	delimiters := lexed.delimiters
	if delimiters == nil {
		delimiters = rustMatchDelimiters(lexed.tokens)
	}
	errorContext, macroContext := rustSyntaxContexts(tree)
	definitions := make([]sourceDefinition, 0)
	for nodeIndex, node := range tree.nodes {
		if !rustTreeDefinitionKind(node.kind) ||
			errorContext[nodeIndex] || macroContext[nodeIndex] ||
			rustByteRangeExcluded(node.startByte, node.endByte, excluded) {
			continue
		}

		nameStart, nameEnd := -1, -1
		if node.kind == "impl_item" {
			if nameToken, ok := rustImplNameTokenForNode(node, lexed.tokens, delimiters); ok {
				nameStart, nameEnd = nameToken.start, nameToken.end
			}
		} else {
			nameIndex := rustDefinitionNameNode(tree, nodeIndex)
			if nameIndex >= 0 && nameIndex < len(tree.nodes) {
				nameStart = tree.nodes[nameIndex].startByte
				nameEnd = tree.nodes[nameIndex].endByte
			}
		}
		if nameStart < 0 || nameEnd < 0 {
			continue
		}
		if nameStart < 0 || nameEnd > len(source) || nameStart >= nameEnd ||
			!rustIdentifierRangeIsWhole(source, nameStart, nameEnd) ||
			rustByteRangeExcluded(nameStart, nameEnd, excluded) {
			continue
		}
		symbol := source[nameStart:nameEnd]
		if !rustSourceIdentifier(symbol) {
			continue
		}

		ownsScope := rustTreeDefinitionOwnsScope(tree, nodeIndex)
		scopeStartOffset := rustSyntaxAttachedStart(tree, nodeIndex, attachedStarts)
		line, column := positions.lineColumn(nameStart)
		scopeStart, scopeEnd := positions.lineSpan(scopeStartOffset, node.endByte)
		ownedEndColumn := 0
		if ownsScope {
			ownedEndLine, exactEndColumn := positions.lineColumn(node.endByte)
			if ownedEndLine == scopeEnd {
				ownedEndColumn = exactEndColumn
			}
		}
		definitions = append(definitions, sourceDefinition{
			symbol:         symbol,
			line:           line,
			column:         column,
			scopeStart:     scopeStart,
			scopeEnd:       scopeEnd,
			ownedEndColumn: ownedEndColumn,
			ownsScope:      ownsScope,
		})
	}
	return definitions
}

func rustTreeDefinitionKind(kind string) bool {
	switch kind {
	case "function_item", "function_signature_item", "struct_item", "union_item",
		"enum_item", "enum_variant", "trait_item", "impl_item", "type_item",
		"associated_type", "const_item", "static_item", "mod_item",
		"macro_definition":
		return true
	default:
		return false
	}
}

func rustDefinitionNameNode(tree *rustSyntaxTree, nodeIndex int) int {
	if nodeIndex < 0 || nodeIndex >= len(tree.nodes) {
		return -1
	}
	for _, childIndex := range tree.nodes[nodeIndex].children {
		switch tree.nodes[childIndex].kind {
		case "identifier", "type_identifier":
			return childIndex
		}
	}
	return -1
}

func rustImplNameTokenForNode(
	node rustSyntaxNode,
	tokens []rustToken,
	delimiters map[int]int,
) (rustToken, bool) {
	firstToken := sort.Search(len(tokens), func(index int) bool {
		return tokens[index].start >= node.startByte
	})
	implIndex := -1
	for index := firstToken; index < len(tokens) && tokens[index].start < node.endByte; index++ {
		if !tokens[index].raw && tokens[index].text == "impl" {
			implIndex = index
			break
		}
	}
	if implIndex < 0 {
		return rustToken{}, false
	}
	limit := sort.Search(len(tokens), func(index int) bool {
		return tokens[index].start >= node.endByte
	})
	if limit <= implIndex+1 {
		return rustToken{}, false
	}
	bodyIndex := rustImplBodyTokenWithin(tokens, implIndex, limit, delimiters)
	nameTokenIndex, ok := rustImplNameTokenWithin(
		tokens,
		implIndex,
		bodyIndex,
		limit-1,
		delimiters,
	)
	if !ok || nameTokenIndex < 0 || nameTokenIndex >= len(tokens) {
		return rustToken{}, false
	}
	nameToken := tokens[nameTokenIndex]
	if nameToken.start < node.startByte || nameToken.end > node.endByte {
		return rustToken{}, false
	}
	return nameToken, true
}

func rustTreeDefinitionOwnsScope(tree *rustSyntaxTree, nodeIndex int) bool {
	if nodeIndex < 0 || nodeIndex >= len(tree.nodes) {
		return false
	}
	switch tree.nodes[nodeIndex].kind {
	case "function_item", "struct_item", "union_item", "enum_item", "trait_item",
		"impl_item", "macro_definition":
		return true
	case "mod_item":
		return rustSyntaxHasDirectDeclarationList(tree, nodeIndex)
	case "const_item", "static_item":
		return rustSyntaxHasScopedDescendant(tree, nodeIndex)
	default:
		return false
	}
}

func rustSyntaxHasDirectDeclarationList(tree *rustSyntaxTree, nodeIndex int) bool {
	for _, childIndex := range tree.nodes[nodeIndex].children {
		if tree.nodes[childIndex].kind == "declaration_list" {
			return true
		}
	}
	return false
}

func rustSyntaxHasScopedDescendant(tree *rustSyntaxTree, nodeIndex int) bool {
	stack := append([]int(nil), tree.nodes[nodeIndex].children...)
	for len(stack) > 0 {
		last := len(stack) - 1
		candidateIndex := stack[last]
		stack = stack[:last]
		candidate := tree.nodes[candidateIndex]
		if rustTreeExpressionScopeKind(candidate.kind) || candidate.kind == "block" {
			return true
		}
		stack = append(stack, candidate.children...)
	}
	return false
}

func rustSyntaxAttachedStarts(source string, tree *rustSyntaxTree) []int {
	if tree == nil {
		return nil
	}
	starts := make([]int, len(tree.nodes))
	for index, node := range tree.nodes {
		starts[index] = node.startByte
	}
	for _, parent := range tree.nodes {
		pendingStart := -1
		previousEnd := -1
		for _, childIndex := range parent.children {
			if childIndex < 0 || childIndex >= len(tree.nodes) {
				pendingStart = -1
				previousEnd = -1
				continue
			}
			child := tree.nodes[childIndex]
			adjacent := previousEnd >= 0 && previousEnd <= child.startByte &&
				child.startByte <= len(source) && rustOnlyWhitespace(source[previousEnd:child.startByte])
			if previousEnd >= 0 && !adjacent {
				pendingStart = -1
			}

			comment := child.kind == "line_comment" || child.kind == "block_comment"
			commentText := ""
			if comment && child.startByte >= 0 && child.endByte <= len(source) {
				commentText = source[child.startByte:child.endByte]
			}
			attached := child.kind == "attribute_item" || rustOuterDocComment(commentText)
			switch {
			case attached:
				if pendingStart < 0 {
					pendingStart = child.startByte
				}
			case comment && !rustInnerDocComment(commentText):
				// Ordinary comments are transparent between semantic outer
				// attributes or docs and the item they annotate.
			default:
				if pendingStart >= 0 && adjacent {
					starts[childIndex] = pendingStart
				}
				pendingStart = -1
			}
			previousEnd = child.endByte
		}
	}
	return starts
}

func rustSyntaxAttachedStart(tree *rustSyntaxTree, nodeIndex int, starts []int) int {
	if tree == nil || nodeIndex < 0 || nodeIndex >= len(tree.nodes) {
		return 0
	}
	if nodeIndex < len(starts) && starts[nodeIndex] >= 0 &&
		starts[nodeIndex] <= tree.nodes[nodeIndex].startByte {
		return starts[nodeIndex]
	}
	return tree.nodes[nodeIndex].startByte
}

func rustSyntaxWholeFileErrorRoot(tree *rustSyntaxTree, nodeIndex int) bool {
	return tree != nil && nodeIndex == tree.root && nodeIndex >= 0 &&
		nodeIndex < len(tree.nodes) && tree.nodes[nodeIndex].parent < 0 &&
		tree.nodes[nodeIndex].kind == "ERROR" && tree.nodes[nodeIndex].startByte == 0
}

func rustSyntaxErrorSpans(tree *rustSyntaxTree, sourceLength int) []rustByteSpan {
	if tree == nil {
		return nil
	}
	spans := make([]rustByteSpan, 0)
	for nodeIndex, node := range tree.nodes {
		if node.kind != "ERROR" {
			continue
		}
		// The pure-Go parser can wrap a useful recovered CST in a full-file
		// ERROR root. Its narrower descendant ERROR nodes carry the actual
		// recovery evidence; treating the wrapper as one giant error would
		// admit every lexical guess in the file.
		if len(tree.nodes) > 1 && rustSyntaxWholeFileErrorRoot(tree, nodeIndex) {
			continue
		}
		start := max(0, min(node.startByte, sourceLength))
		end := max(start, min(node.endByte, sourceLength))
		if start == end {
			if end < sourceLength {
				end++
			} else if start > 0 {
				start--
			}
		}
		if start < end {
			spans = append(spans, rustByteSpan{start: start, end: end})
		}
	}
	return normalizeRustSpans(spans)
}

func rustLexicalDefinitionTouchesRecovery(
	source string,
	lineStarts []int,
	definition sourceDefinition,
	recoverySpans []rustByteSpan,
	recoveryLines map[int]bool,
) bool {
	if recoveryLines[definition.line] {
		return true
	}
	if definition.line < 1 || definition.line > len(lineStarts) || definition.column < 1 {
		return false
	}
	start := lineStarts[definition.line-1] + definition.column - 1
	end := start + len(definition.symbol)
	if start < 0 || end > len(source) || start >= end {
		return false
	}
	index := sort.Search(len(recoverySpans), func(index int) bool {
		return recoverySpans[index].end > start
	})
	return index < len(recoverySpans) && recoverySpans[index].start < end
}

func rustRecoveryLines(source string, spans []rustByteSpan) map[int]bool {
	result := make(map[int]bool)
	positions := rustSourcePositions{source: source, lineStarts: rustLineStarts(source)}
	for _, span := range spans {
		start, end := positions.lineSpan(span.start, span.end)
		for line := start; line <= end; line++ {
			result[line] = true
		}
	}
	return result
}

func rustLineSpanTouchesRecovery(span rustLineSpan, recoveryLines map[int]bool) bool {
	for line := span.start; line <= span.end; line++ {
		if recoveryLines[line] {
			return true
		}
	}
	return false
}

func rustSyntaxContexts(tree *rustSyntaxTree) (errorContext, macroContext []bool) {
	if tree == nil {
		return nil, nil
	}
	errorContext = make([]bool, len(tree.nodes))
	macroContext = make([]bool, len(tree.nodes))
	for nodeIndex, node := range tree.nodes {
		if node.kind == "ERROR" && !rustSyntaxWholeFileErrorRoot(tree, nodeIndex) {
			errorContext[nodeIndex] = true
		}
		parent := node.parent
		if parent < 0 {
			continue
		}
		if parent >= nodeIndex || parent >= len(tree.nodes) {
			errorContext[nodeIndex] = true
			macroContext[nodeIndex] = true
			continue
		}
		errorContext[nodeIndex] = errorContext[nodeIndex] || errorContext[parent]
		macroContext[nodeIndex] = macroContext[parent] || rustSyntaxMacroContainer(
			tree.nodes[parent].kind,
		)
	}
	return errorContext, macroContext
}

func rustSyntaxMacroContainer(kind string) bool {
	switch kind {
	case "macro_definition", "macro_invocation", "token_tree", "token_tree_pattern":
		return true
	default:
		return false
	}
}

func (r rustLanguage) enclosingScope(lines []string, lineNo int) (int, int) {
	if lineNo < 1 || lineNo > len(lines) {
		return lineNo, lineNo
	}
	analysis := r.sourceAnalysis(lines)
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

func (r rustLanguage) navigationScope(lines []string, lineNo int) (int, int) {
	analysis := r.sourceAnalysis(lines)
	if analysis == nil {
		return lineNo, lineNo
	}
	bestStart, bestEnd := 0, 0
	for _, definition := range analysis.definitions {
		if !definition.ownsScope || lineNo < definition.scopeStart || lineNo > definition.scopeEnd {
			continue
		}
		if bestStart == 0 || definition.scopeEnd-definition.scopeStart < bestEnd-bestStart ||
			(definition.scopeEnd-definition.scopeStart == bestEnd-bestStart &&
				definition.scopeStart > bestStart) {
			bestStart, bestEnd = definition.scopeStart, definition.scopeEnd
		}
	}
	if bestStart > 0 {
		return bestStart, bestEnd
	}
	return r.enclosingScope(lines, lineNo)
}

func rustCombinedScopes(
	lineCount int,
	lexical, concrete []rustLineScope,
	definitions []sourceDefinition,
	hasTree bool,
) []rustLineScope {
	capacity := len(concrete) + len(definitions)
	if !hasTree || len(concrete) == 0 {
		capacity += len(lexical)
	}
	scopes := make([]rustLineScope, 0, capacity)
	scopes = append(scopes, concrete...)
	if !hasTree || len(concrete) == 0 {
		scopes = append(scopes, lexical...)
	}
	for _, definition := range definitions {
		if definition.ownsScope {
			scopes = append(scopes, rustLineScope{
				start: definition.scopeStart,
				end:   definition.scopeEnd,
			})
		}
	}
	sort.Slice(scopes, func(left, right int) bool {
		if scopes[left].start != scopes[right].start {
			return scopes[left].start < scopes[right].start
		}
		return scopes[left].end < scopes[right].end
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

func rustTreeScopesFromSyntax(
	source string,
	tree *rustSyntaxTree,
	excluded []rustByteSpan,
	attachedStarts []int,
) []rustLineScope {
	if tree == nil {
		return nil
	}
	positions := rustSourcePositions{source: source, lineStarts: rustLineStarts(source)}
	errorContext, macroContext := rustSyntaxContexts(tree)
	scopes := make([]rustLineScope, 0)
	for nodeIndex, node := range tree.nodes {
		if !rustTreeScopeKind(node.kind) || errorContext[nodeIndex] || macroContext[nodeIndex] ||
			rustByteRangeExcluded(node.startByte, node.endByte, excluded) {
			continue
		}
		if node.kind == "block" && rustBlockOwnedByParent(tree, nodeIndex) {
			continue
		}
		startOffset := node.startByte
		if rustTreeDefinitionKind(node.kind) {
			startOffset = rustSyntaxAttachedStart(tree, nodeIndex, attachedStarts)
		}
		start, end := positions.lineSpan(startOffset, node.endByte)
		scopes = append(scopes, rustLineScope{start: start, end: end})
	}
	return scopes
}

func rustTreeScopeKind(kind string) bool {
	if rustTreeDefinitionKind(kind) {
		return kind != "function_signature_item" && kind != "associated_type" &&
			kind != "enum_variant" && kind != "type_item"
	}
	if rustTreeExpressionScopeKind(kind) {
		return true
	}
	switch kind {
	case "foreign_mod_item", "else_clause", "match_arm", "block":
		return true
	default:
		return false
	}
}

func rustTreeExpressionScopeKind(kind string) bool {
	switch kind {
	case "if_expression", "match_expression", "while_expression", "loop_expression",
		"for_expression", "const_block", "closure_expression", "unsafe_block",
		"async_block", "gen_block", "try_block":
		return true
	default:
		return false
	}
}

func rustBlockOwnedByParent(tree *rustSyntaxTree, nodeIndex int) bool {
	parentIndex := tree.nodes[nodeIndex].parent
	if parentIndex < 0 || parentIndex >= len(tree.nodes) {
		return false
	}
	parentKind := tree.nodes[parentIndex].kind
	return rustTreeScopeKind(parentKind) || parentKind == "match_arm"
}

func (r rustLanguage) importRange(lines []string) (int, int, bool) {
	analysis := r.sourceAnalysis(lines)
	if analysis == nil {
		return 0, 0, false
	}
	imports := make([]rustLineSpan, 0, len(analysis.lexed.imports)+len(analysis.treeImports))
	for _, statement := range analysis.lexed.imports {
		if analysis.tree == nil || rustLineSpanTouchesRecovery(statement, analysis.recoveryLines) {
			imports = append(imports, statement)
		}
	}
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

func rustTreeImportsFromSyntax(
	source string,
	tree *rustSyntaxTree,
	excluded []rustByteSpan,
	attachedStarts []int,
) []rustLineSpan {
	if tree == nil {
		return nil
	}
	positions := rustSourcePositions{source: source, lineStarts: rustLineStarts(source)}
	errorContext, macroContext := rustSyntaxContexts(tree)
	imports := make([]rustLineSpan, 0)
	for nodeIndex, node := range tree.nodes {
		if node.kind != "use_declaration" && node.kind != "extern_crate_declaration" {
			continue
		}
		if errorContext[nodeIndex] || macroContext[nodeIndex] ||
			rustByteRangeExcluded(node.startByte, node.endByte, excluded) {
			continue
		}
		startOffset := rustSyntaxAttachedStart(tree, nodeIndex, attachedStarts)
		start, end := positions.lineSpan(startOffset, node.endByte)
		imports = append(imports, rustLineSpan{start: start, end: end})
	}
	return imports
}

func (r rustLanguage) cleanSource(source string, dropComments, _ bool) string {
	if !dropComments {
		return source
	}
	lines := strings.Split(source, "\n")
	cleaned := r.cleanSourceLines(lines, true, false)
	return dropBlankArtifactLines(strings.Join(cleaned, "\n"))
}

func (r rustLanguage) cleanSourceLines(
	lines []string,
	dropComments, _ bool,
) []string {
	if len(lines) == 0 {
		return nil
	}
	if !dropComments {
		return append([]string(nil), lines...)
	}
	analysis := r.sourceAnalysis(lines)
	if analysis == nil {
		return append([]string(nil), lines...)
	}
	masked := strings.Split(maskRustSource(analysis.source, analysis.lexed.commentSpans), "\n")
	for index := range masked {
		if index < len(lines) && masked[index] != lines[index] {
			masked[index] = strings.TrimRight(masked[index], " \t")
		}
	}
	return masked
}

func (rustLanguage) finalizeSourceSnippet(source string, dropComments, _ bool) string {
	if !dropComments {
		return source
	}
	return dropBlankArtifactLines(source)
}

func (r rustLanguage) ignoredSearchLines(
	lines []string,
	dropComments, _ bool,
) map[int]bool {
	ignored := map[int]bool{}
	if len(lines) == 0 || !dropComments {
		return ignored
	}
	analysis := r.sourceAnalysis(lines)
	if analysis == nil {
		return ignored
	}
	masked := strings.Split(maskRustSource(analysis.source, analysis.lexed.commentSpans), "\n")
	for index := range lines {
		if index < len(masked) && masked[index] != lines[index] && strings.TrimSpace(masked[index]) == "" {
			ignored[index+1] = true
		}
	}
	return ignored
}

func (r rustLanguage) searchLines(lines []string, noComments, noStrings bool) []string {
	if len(lines) == 0 {
		return nil
	}
	if !noComments && !noStrings {
		return append([]string(nil), lines...)
	}
	analysis := r.sourceAnalysis(lines)
	if analysis == nil {
		return append([]string(nil), lines...)
	}
	spans := make([]rustByteSpan, 0, len(analysis.searchMask))
	if noComments {
		spans = append(spans, analysis.lexed.commentSpans...)
	}
	if noStrings {
		spans = append(spans, analysis.lexed.stringSpans...)
	}
	return strings.Split(maskRustSource(analysis.source, spans), "\n")
}

func (rustLanguage) stripComment(line string) string {
	lexed := lexRust(line)
	return strings.TrimRight(maskRustSource(line, lexed.commentSpans), " \t")
}

func (r rustLanguage) symbolOnLine(lines []string, lineNo int) (string, bool) {
	if lineNo < 1 || lineNo > len(lines) {
		return "", false
	}
	analysis := r.sourceAnalysis(lines)
	if analysis == nil {
		return "", false
	}
	for _, definition := range analysis.definitions {
		if definition.line == lineNo {
			return definition.symbol, true
		}
	}
	if symbol, ok := rustTreeSymbolOnLineFromSyntax(
		analysis.source,
		analysis.tree,
		analysis.searchMask,
		lineNo,
	); ok {
		return symbol, true
	}
	return rustLexicalSymbolOnLine(analysis, lineNo), true
}

func rustTreeSymbolOnLineFromSyntax(
	source string,
	tree *rustSyntaxTree,
	excluded []rustByteSpan,
	lineNo int,
) (string, bool) {
	if tree == nil {
		return "", false
	}
	positions := rustSourcePositions{source: source, lineStarts: rustLineStarts(source)}
	errorContext, _ := rustSyntaxContexts(tree)
	for nodeIndex, node := range tree.nodes {
		if node.kind != "call_expression" || errorContext[nodeIndex] ||
			rustByteRangeExcluded(node.startByte, node.endByte, excluded) {
			continue
		}
		identifierIndex := rustCalledIdentifierNode(tree, nodeIndex)
		if rustSyntaxIdentifierOnLine(tree, identifierIndex, positions, lineNo) {
			return rustSyntaxIdentifierText(source, tree, identifierIndex), true
		}
	}
	for nodeIndex, node := range tree.nodes {
		if node.kind != "macro_invocation" || errorContext[nodeIndex] ||
			rustByteRangeExcluded(node.startByte, node.endByte, excluded) {
			continue
		}
		identifierIndex := rustMacroIdentifierNode(tree, nodeIndex)
		if rustSyntaxIdentifierOnLine(tree, identifierIndex, positions, lineNo) {
			return rustSyntaxIdentifierText(source, tree, identifierIndex), true
		}
	}
	for nodeIndex, node := range tree.nodes {
		if node.kind != "field_expression" || errorContext[nodeIndex] ||
			rustByteRangeExcluded(node.startByte, node.endByte, excluded) {
			continue
		}
		identifierIndex := rustFieldIdentifierNode(tree, nodeIndex)
		if rustSyntaxIdentifierOnLine(tree, identifierIndex, positions, lineNo) {
			return rustSyntaxIdentifierText(source, tree, identifierIndex), true
		}
	}
	for nodeIndex, node := range tree.nodes {
		if node.kind != "scoped_identifier" && node.kind != "scoped_type_identifier" {
			continue
		}
		if errorContext[nodeIndex] ||
			rustByteRangeExcluded(node.startByte, node.endByte, excluded) {
			continue
		}
		identifierIndex := rustTerminalPathIdentifierNode(tree, nodeIndex, 0)
		if rustSyntaxIdentifierOnLine(tree, identifierIndex, positions, lineNo) {
			return rustSyntaxIdentifierText(source, tree, identifierIndex), true
		}
	}
	for nodeIndex, node := range tree.nodes {
		if !rustSyntaxIdentifierKind(node.kind) || errorContext[nodeIndex] ||
			rustByteRangeExcluded(node.startByte, node.endByte, excluded) ||
			rustSyntaxIdentifierIsPathPrefixOrLifetime(tree, nodeIndex) ||
			!rustSyntaxIdentifierOnLine(tree, nodeIndex, positions, lineNo) {
			continue
		}
		symbol := rustSyntaxIdentifierText(source, tree, nodeIndex)
		if rustSourceIdentifier(symbol) {
			return symbol, true
		}
	}
	return "", false
}

func rustCalledIdentifierNode(tree *rustSyntaxTree, callIndex int) int {
	return rustCalledIdentifierNodeAtDepth(tree, callIndex, 0)
}

func rustCalledIdentifierNodeAtDepth(tree *rustSyntaxTree, callIndex, depth int) int {
	if callIndex < 0 || callIndex >= len(tree.nodes) {
		return -1
	}
	if depth > rustMaximumSyntaxUnwrapDepth {
		return -1
	}
	for _, childIndex := range tree.nodes[callIndex].children {
		if tree.nodes[childIndex].kind == "arguments" {
			continue
		}
		if identifierIndex := rustCallableIdentifierNode(
			tree, childIndex, depth,
		); identifierIndex >= 0 {
			return identifierIndex
		}
	}
	return -1
}

func rustCallableIdentifierNode(tree *rustSyntaxTree, nodeIndex, depth int) int {
	if nodeIndex < 0 || nodeIndex >= len(tree.nodes) || depth > rustMaximumSyntaxUnwrapDepth {
		return -1
	}
	node := tree.nodes[nodeIndex]
	if rustSyntaxIdentifierKind(node.kind) {
		return nodeIndex
	}
	switch node.kind {
	case "field_expression":
		return rustFieldIdentifierNode(tree, nodeIndex)
	case "scoped_identifier", "scoped_type_identifier":
		return rustTerminalPathIdentifierNode(tree, nodeIndex, depth+1)
	case "call_expression":
		return rustCalledIdentifierNodeAtDepth(tree, nodeIndex, depth+1)
	case "macro_invocation":
		return rustMacroIdentifierNodeAtDepth(tree, nodeIndex, depth+1)
	case "generic_function", "index_expression", "parenthesized_expression",
		"reference_expression", "unary_expression", "try_expression":
		for _, childIndex := range node.children {
			childKind := tree.nodes[childIndex].kind
			if childKind == "type_arguments" || childKind == "arguments" {
				continue
			}
			if identifierIndex := rustCallableIdentifierNode(
				tree, childIndex, depth+1,
			); identifierIndex >= 0 {
				return identifierIndex
			}
		}
	}
	return -1
}

func rustMacroIdentifierNode(tree *rustSyntaxTree, macroIndex int) int {
	return rustMacroIdentifierNodeAtDepth(tree, macroIndex, 0)
}

func rustMacroIdentifierNodeAtDepth(tree *rustSyntaxTree, macroIndex, depth int) int {
	if macroIndex < 0 || macroIndex >= len(tree.nodes) || depth > rustMaximumSyntaxUnwrapDepth {
		return -1
	}
	for _, childIndex := range tree.nodes[macroIndex].children {
		childKind := tree.nodes[childIndex].kind
		if childKind == "token_tree" || childKind == "!" {
			break
		}
		if identifierIndex := rustCallableIdentifierNode(
			tree, childIndex, depth,
		); identifierIndex >= 0 {
			return identifierIndex
		}
	}
	return -1
}

func rustTerminalPathIdentifierNode(tree *rustSyntaxTree, nodeIndex, depth int) int {
	if nodeIndex < 0 || nodeIndex >= len(tree.nodes) || depth > rustMaximumSyntaxUnwrapDepth {
		return -1
	}
	node := tree.nodes[nodeIndex]
	if rustSyntaxIdentifierKind(node.kind) {
		return nodeIndex
	}
	for index := len(node.children) - 1; index >= 0; index-- {
		childIndex := node.children[index]
		childKind := tree.nodes[childIndex].kind
		if childKind == "type_arguments" || childKind == "generic_arguments" {
			continue
		}
		if identifierIndex := rustTerminalPathIdentifierNode(
			tree, childIndex, depth+1,
		); identifierIndex >= 0 {
			return identifierIndex
		}
	}
	return -1
}

func rustSyntaxIdentifierIsPathPrefixOrLifetime(tree *rustSyntaxTree, nodeIndex int) bool {
	if nodeIndex < 0 || nodeIndex >= len(tree.nodes) {
		return true
	}
	parentIndex := tree.nodes[nodeIndex].parent
	if parentIndex < 0 || parentIndex >= len(tree.nodes) {
		return false
	}
	switch tree.nodes[parentIndex].kind {
	case "label", "lifetime", "loop_label":
		return true
	case "scoped_identifier", "scoped_type_identifier":
		return rustTerminalPathIdentifierNode(tree, parentIndex, 0) != nodeIndex
	default:
		return false
	}
}

func rustFieldIdentifierNode(tree *rustSyntaxTree, nodeIndex int) int {
	if nodeIndex < 0 || nodeIndex >= len(tree.nodes) {
		return -1
	}
	for index := len(tree.nodes[nodeIndex].children) - 1; index >= 0; index-- {
		childIndex := tree.nodes[nodeIndex].children[index]
		if tree.nodes[childIndex].kind == "field_identifier" {
			return childIndex
		}
	}
	return -1
}

func rustSyntaxIdentifierKind(kind string) bool {
	return kind == "identifier" || kind == "type_identifier" || kind == "field_identifier"
}

func rustSyntaxIdentifierOnLine(
	tree *rustSyntaxTree,
	nodeIndex int,
	positions rustSourcePositions,
	lineNo int,
) bool {
	if nodeIndex < 0 || nodeIndex >= len(tree.nodes) ||
		!rustSyntaxIdentifierKind(tree.nodes[nodeIndex].kind) {
		return false
	}
	line, _ := positions.lineColumn(tree.nodes[nodeIndex].startByte)
	return line == lineNo
}

func rustSyntaxIdentifierText(source string, tree *rustSyntaxTree, nodeIndex int) string {
	if nodeIndex < 0 || nodeIndex >= len(tree.nodes) {
		return ""
	}
	node := tree.nodes[nodeIndex]
	if node.startByte < 0 || node.endByte > len(source) || node.startByte >= node.endByte {
		return ""
	}
	return source[node.startByte:node.endByte]
}

func rustLexicalSymbolOnLine(analysis *rustSourceAnalysis, lineNo int) string {
	if analysis == nil {
		return ""
	}
	positions := rustSourcePositions{
		source: analysis.source, lineStarts: rustLineStarts(analysis.source),
	}
	indices := make([]int, 0)
	for index, token := range analysis.lexed.tokens {
		line, _ := positions.lineColumn(token.start)
		if line == lineNo && rustLexicalSymbolCandidate(analysis.lexed.tokens, index) {
			indices = append(indices, index)
		}
	}
	for _, index := range indices {
		if rustNextTokenText(analysis.lexed.tokens, index+1) == "!" {
			return rustTokenSourceText(analysis.source, analysis.lexed.tokens[index])
		}
	}
	for _, index := range indices {
		next := rustNextTokenText(analysis.lexed.tokens, index+1)
		if next == "(" || next == "[" {
			return rustTokenSourceText(analysis.source, analysis.lexed.tokens[index])
		}
	}
	for index := len(indices) - 1; index >= 0; index-- {
		tokenIndex := indices[index]
		precededByPathSeparator := tokenIndex > 0 &&
			analysis.lexed.tokens[tokenIndex-1].text == "." ||
			tokenIndex > 1 && analysis.lexed.tokens[tokenIndex-2].text == ":" &&
				analysis.lexed.tokens[tokenIndex-1].text == ":"
		if precededByPathSeparator {
			return rustTokenSourceText(analysis.source, analysis.lexed.tokens[tokenIndex])
		}
	}
	if len(indices) > 0 {
		return rustTokenSourceText(analysis.source, analysis.lexed.tokens[indices[0]])
	}
	return ""
}

func rustLexicalSymbolCandidate(tokens []rustToken, index int) bool {
	if index < 0 || index >= len(tokens) || !rustTokenIdentifier(tokens[index]) ||
		(!tokens[index].raw && rustReservedIdentifier(tokens[index].text)) || tokens[index].text == "_" {
		return false
	}
	if index > 0 && tokens[index-1].text == "'" && tokens[index-1].end == tokens[index].start {
		return false
	}
	return true
}

func rustTokenSourceText(source string, token rustToken) string {
	if token.start < 0 || token.end > len(source) || token.start >= token.end {
		return ""
	}
	return source[token.start:token.end]
}

func (rustLanguage) countSymbolOccurrences(line, symbol string) int {
	return rustWalkSymbolOccurrences(line, symbol, nil)
}

func (rustLanguage) symbolOccurrenceColumns(line, symbol string) []int {
	var columns []int
	rustWalkSymbolOccurrences(line, symbol, func(start int) {
		columns = append(columns, start+1)
	})
	return columns
}

func rustWalkSymbolOccurrences(line, symbol string, visit func(start int)) int {
	if symbol == "" {
		return 0
	}
	count := 0
	for offset := 0; offset <= len(line)-len(symbol); {
		relative := strings.Index(line[offset:], symbol)
		if relative < 0 {
			break
		}
		position := offset + relative
		after := position + len(symbol)
		beforeRune, _ := utf8.DecodeLastRuneInString(line[:position])
		afterRune, _ := utf8.DecodeRuneInString(line[after:])
		beforeOK := position == 0 || !rustIdentifierContinue(beforeRune)
		afterOK := after >= len(line) || !rustIdentifierContinue(afterRune)
		if beforeOK && afterOK && rustOccurrenceIsWholeToken(line, symbol, position, after) {
			count++
			if visit != nil {
				visit(position)
			}
		}
		_, size := rustDecode(line, position)
		if size < 1 {
			size = 1
		}
		offset = position + size
	}
	return count
}

func rustOccurrenceIsWholeToken(line, symbol string, start, end int) bool {
	if !strings.HasPrefix(symbol, "r#") && start >= 2 && line[start-2:start] == "r#" {
		return false
	}
	if symbol == "r" && end < len(line) && line[end] == '#' {
		return false
	}
	if !strings.HasPrefix(symbol, "'") && start > 0 && line[start-1] == '\'' {
		if end >= len(line) || line[end] != '\'' {
			return false
		}
	}
	return true
}

func rustSourceIdentifier(identifier string) bool {
	if identifier == "" || !utf8.ValidString(identifier) {
		return false
	}
	raw := strings.HasPrefix(identifier, "r#")
	if raw {
		identifier = identifier[2:]
		if identifier == "" || identifier == "_" {
			return false
		}
		switch identifier {
		case "crate", "self", "super", "Self":
			return false
		}
	} else if identifier == "_" || rustReservedIdentifier(identifier) {
		return false
	}
	first := true
	for _, current := range identifier {
		if first {
			if !rustIdentifierStart(current) {
				return false
			}
			first = false
			continue
		}
		if !rustIdentifierContinue(current) {
			return false
		}
	}
	return !first
}

func rustIdentifierRangeIsWhole(source string, start, end int) bool {
	if start < 0 || end > len(source) || start >= end {
		return false
	}
	if start > 0 {
		previous, _ := utf8.DecodeLastRuneInString(source[:start])
		if rustIdentifierContinue(previous) {
			return false
		}
	}
	if end < len(source) {
		next, _ := utf8.DecodeRuneInString(source[end:])
		if rustIdentifierContinue(next) {
			return false
		}
	}
	identifier := source[start:end]
	if !strings.HasPrefix(identifier, "r#") && start >= 2 && source[start-2:start] == "r#" {
		return false
	}
	return identifier != "r" || end >= len(source) || source[end] != '#'
}

func rustByteRangeExcluded(start, end int, spans []rustByteSpan) bool {
	index := sort.Search(len(spans), func(index int) bool {
		return spans[index].end > start
	})
	return index < len(spans) && spans[index].start <= start && end <= spans[index].end
}

type rustSourcePositions struct {
	source     string
	lineStarts []int
}

func (positions rustSourcePositions) lineColumn(offset int) (int, int) {
	offset = max(0, min(offset, len(positions.source)))
	if len(positions.lineStarts) == 0 {
		positions.lineStarts = []int{0}
	}
	lineIndex := sort.Search(len(positions.lineStarts), func(index int) bool {
		return positions.lineStarts[index] > offset
	}) - 1
	lineIndex = max(lineIndex, 0)
	return lineIndex + 1, offset - positions.lineStarts[lineIndex] + 1
}

func (positions rustSourcePositions) lineSpan(start, end int) (int, int) {
	startLine, _ := positions.lineColumn(start)
	endOffset := end
	if endOffset > start {
		endOffset--
	}
	endLine, _ := positions.lineColumn(endOffset)
	return startLine, max(startLine, endLine)
}
