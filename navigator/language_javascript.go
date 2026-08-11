package navigator

import (
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
)

type javascriptLanguage struct {
	analysis *javascriptSourceAnalysis
	languageDefinition
	flavor javascriptSyntaxFlavor
}

type javascriptSourceAnalysis struct {
	tree *javascriptSyntaxTree

	source        string
	lexed         javascriptLexResult
	definitions   []sourceDefinition
	scopes        []javascriptLineScope
	imports       []javascriptLineSpan
	commentSpans  []javascriptByteSpan
	stringSpans   []javascriptByteSpan
	opaqueSpans   []javascriptByteSpan
	semanticSpans []javascriptByteSpan
	recoverySpans []javascriptByteSpan
	recoveryLines map[int]bool
	lines         []string
	lineSnapshot  []string
	lineStarts    []int
	lineCount     int
}

type javascriptByteSpan struct {
	start int
	end   int
}

type javascriptLineScope struct {
	start int
	end   int
}

type javascriptLineSpan struct {
	start int
	end   int
}

type javascriptDefinitionIdentity struct {
	symbol string
	line   int
	column int
}

const javascriptMaximumSyntaxUnwrapDepth = 64

func newJavaScriptLanguage(name string) javascriptLanguage {
	return javascriptLanguage{
		flavor: javascriptSyntaxFlavorJavaScript,
		languageDefinition: newLanguageDefinition(
			name,
			nil,
			nil,
			nil,
			commentStyleCLike,
			false,
		),
	}
}

func registerJavaScriptLanguages(registry map[string]languageBackend) {
	registerLanguage(registry, newJavaScriptLanguage("javascript"), ".js")
	registerLanguage(registry, newJavaScriptLanguage("mjs"), ".mjs")
	registerLanguage(registry, newJavaScriptLanguage("cjs"), ".cjs")
	registerLanguage(registry, newJavaScriptLanguage("jsx"), ".jsx")
}

func (javascriptLanguage) authoritativeSymbolOnLine() {}

func (j javascriptLanguage) prepareSource(lines []string) languageBackend {
	if len(lines) == 0 {
		j.analysis = nil
		return j
	}
	j.analysis = j.analyzeSource(strings.Join(lines, "\n"), len(lines))
	j.analysis.lines = lines
	j.analysis.lineSnapshot = append([]string(nil), lines...)
	return j
}

func (j javascriptLanguage) sourceAnalysis(lines []string) *javascriptSourceAnalysis {
	if len(lines) == 0 {
		return nil
	}
	if j.analysis != nil && javascriptSameLineStorage(j.analysis.lines, lines) &&
		javascriptSameLines(j.analysis.lineSnapshot, lines) {
		return j.analysis
	}
	return j.analysisForSource(strings.Join(lines, "\n"), len(lines))
}

func javascriptSameLineStorage(first, second []string) bool {
	return len(first) == len(second) && len(first) > 0 && &first[0] == &second[0]
}

func javascriptSameLines(first, second []string) bool {
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

func (j javascriptLanguage) analysisForSource(
	source string,
	lineCount int,
) *javascriptSourceAnalysis {
	if j.analysis != nil && j.analysis.source == source && j.analysis.lineCount == lineCount {
		return j.analysis
	}
	return j.analyzeSource(source, lineCount)
}

func analyzeJavaScriptSource(source string, lineCount int) *javascriptSourceAnalysis {
	return analyzeJavaScriptSourceFlavor(
		source, lineCount, javascriptSyntaxFlavorJavaScript,
	)
}

func (j javascriptLanguage) analyzeSource(
	source string,
	lineCount int,
) *javascriptSourceAnalysis {
	return analyzeJavaScriptSourceFlavor(source, lineCount, j.flavor)
}

func analyzeJavaScriptSourceFlavor(
	source string,
	lineCount int,
	flavor javascriptSyntaxFlavor,
) *javascriptSourceAnalysis {
	analysis := &javascriptSourceAnalysis{
		source:     source,
		lineStarts: javascriptLineStarts(source),
		lineCount:  lineCount,
	}
	analysis.tree, _ = parseJavaScriptSyntaxFlavor(source, flavor)
	analysis.commentSpans, analysis.stringSpans = javascriptSyntaxMasks(source, analysis.tree)
	semanticLiterals := javascriptSyntaxSemanticLiterals(source, analysis.tree)
	analysis.recoverySpans = javascriptSyntaxErrorSpans(analysis.tree, len(source))
	if flavor.isTypeScript() {
		analysis.recoverySpans = normalizeJavaScriptSpans(append(
			analysis.recoverySpans,
			typeScriptSyntaxMissingTokenSpans(analysis.tree, len(source))...,
		))
	}
	var fallback javascriptFallbackResult
	if analysis.tree == nil {
		fallback = scanJavaScriptFallbackFlavor(source, flavor)
		analysis.commentSpans = fallback.comments
		analysis.stringSpans = fallback.literals
		semanticLiterals = fallback.literals
	} else if len(analysis.recoverySpans) > 0 || javascriptSyntaxRootIsError(analysis.tree) {
		fallback = scanJavaScriptFallbackFlavor(source, flavor)
		analysis.commentSpans = append(
			analysis.commentSpans,
			javascriptRecoveryMaskSpans(fallback.comments, analysis.recoverySpans)...,
		)
		analysis.stringSpans = append(
			analysis.stringSpans,
			javascriptRecoveryMaskSpans(fallback.literals, analysis.recoverySpans)...,
		)
		semanticLiterals = append(
			semanticLiterals,
			javascriptRecoveryMaskSpans(fallback.literals, analysis.recoverySpans)...,
		)
	}
	analysis.commentSpans = normalizeJavaScriptSpans(analysis.commentSpans)
	analysis.stringSpans = normalizeJavaScriptSpans(analysis.stringSpans)
	analysis.opaqueSpans = normalizeJavaScriptSpans(append(
		append([]javascriptByteSpan(nil), analysis.commentSpans...),
		analysis.stringSpans...,
	))
	analysis.semanticSpans = normalizeJavaScriptSpans(append(
		append([]javascriptByteSpan(nil), analysis.commentSpans...),
		semanticLiterals...,
	))
	analysis.recoveryLines = javascriptRecoveryLines(source, analysis.recoverySpans)
	needsLexicalRecovery := analysis.tree == nil || len(analysis.recoverySpans) > 0 ||
		javascriptSyntaxRootIsError(analysis.tree)
	var lexicalRecoveryLines map[int]bool
	if analysis.tree != nil && !javascriptSyntaxRootIsError(analysis.tree) {
		lexicalRecoveryLines = analysis.recoveryLines
	}
	analysis.lexed = lexJavaScriptWithHintsFlavor(
		source,
		analysis.commentSpans,
		analysis.stringSpans,
		needsLexicalRecovery,
		lexicalRecoveryLines,
		fallback,
		flavor,
	)
	attachedStarts := javascriptSyntaxAttachedStarts(source, analysis.tree)
	errorContext := javascriptSyntaxErrorContexts(analysis.tree)
	analysis.definitions = javascriptTreeDefinitionsFromSyntax(
		source,
		lineCount,
		analysis.tree,
		analysis.semanticSpans,
		attachedStarts,
		errorContext,
	)
	if flavor.isTypeScript() {
		analysis.definitions = filterTypeScriptRecoveryDefinitions(
			source,
			analysis.definitions,
			analysis.recoveryLines,
			analysis.lineStarts,
		)
	}
	analysis.definitions = mergeJavaScriptDefinitions(
		lineCount,
		analysis.tree,
		analysis.definitions,
		analysis.lexed.definitions,
		analysis.recoveryLines,
	)
	analysis.scopes = javascriptTreeScopesFromSyntax(
		source,
		lineCount,
		analysis.tree,
		analysis.semanticSpans,
		attachedStarts,
		errorContext,
	)
	analysis.scopes = mergeJavaScriptScopes(
		lineCount,
		analysis.scopes,
		analysis.lexed.scopes,
		analysis.definitions,
		analysis.tree == nil,
		analysis.recoveryLines,
	)
	analysis.imports = javascriptTreeImportsFromSyntaxFlavor(
		source,
		lineCount,
		analysis.tree,
		analysis.semanticSpans,
		attachedStarts,
		errorContext,
		flavor.isTypeScript(),
	)
	if flavor.isTypeScript() {
		genericCallImports := typeScriptLexGenericCallTypeImports(
			source, analysis.lexed.tokens, analysis.lexed.delimiters,
		)
		if len(analysis.lexed.tokens) == 0 {
			genericCallImports = typeScriptLexGenericCallImportsFromSource(
				source, analysis.commentSpans, analysis.stringSpans,
			)
		}
		analysis.imports = normalizeJavaScriptLineSpans(append(
			analysis.imports,
			genericCallImports...,
		))
	}
	analysis.imports = mergeJavaScriptImports(
		lineCount,
		analysis.imports,
		analysis.lexed.imports,
		analysis.tree == nil,
		analysis.recoveryLines,
	)
	return analysis
}

func (j javascriptLanguage) definitionSymbol(line string) (string, bool) {
	for _, definition := range j.sourceDefinitions([]string{line}) {
		if definition.line == 1 {
			return definition.symbol, true
		}
	}
	return "", false
}

func (j javascriptLanguage) sourceDefinitions(lines []string) []sourceDefinition {
	analysis := j.sourceAnalysis(lines)
	if analysis == nil {
		return nil
	}
	return analysis.definitions
}

func javascriptLineStarts(source string) []int {
	starts := []int{0}
	for index := range len(source) {
		if source[index] == '\n' {
			starts = append(starts, index+1)
		}
	}
	return starts
}

type javascriptSourcePositions struct {
	source     string
	lineStarts []int
}

func (positions javascriptSourcePositions) lineColumn(offset int) (int, int) {
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

func (positions javascriptSourcePositions) lineSpan(start, end int) (int, int) {
	startLine, _ := positions.lineColumn(start)
	endOffset := end
	if endOffset > start {
		endOffset--
	}
	endLine, _ := positions.lineColumn(endOffset)
	return startLine, max(startLine, endLine)
}

func normalizeJavaScriptSpans(spans []javascriptByteSpan) []javascriptByteSpan {
	if len(spans) == 0 {
		return nil
	}
	sort.Slice(spans, func(first, second int) bool {
		if spans[first].start != spans[second].start {
			return spans[first].start < spans[second].start
		}
		return spans[first].end < spans[second].end
	})
	normalized := make([]javascriptByteSpan, 0, len(spans))
	for _, span := range spans {
		if span.start < 0 || span.end <= span.start {
			continue
		}
		if len(normalized) == 0 || span.start > normalized[len(normalized)-1].end {
			normalized = append(normalized, span)
			continue
		}
		if span.end > normalized[len(normalized)-1].end {
			normalized[len(normalized)-1].end = span.end
		}
	}
	return normalized
}

func maskJavaScriptSource(source string, spans []javascriptByteSpan) string {
	content := []byte(source)
	spans = normalizeJavaScriptSpans(append([]javascriptByteSpan(nil), spans...))
	for _, span := range spans {
		start, end := max(span.start, 0), min(span.end, len(content))
		for offset := start; offset < end; offset++ {
			if content[offset] != '\r' && content[offset] != '\n' {
				content[offset] = ' '
			}
		}
	}
	return string(content)
}

func javascriptByteRangeExcluded(start, end int, spans []javascriptByteSpan) bool {
	index := sort.Search(len(spans), func(index int) bool {
		return spans[index].end > start
	})
	return index < len(spans) && spans[index].start <= start && end <= spans[index].end
}

func javascriptSyntaxMasks(
	source string,
	tree *javascriptSyntaxTree,
) ([]javascriptByteSpan, []javascriptByteSpan) {
	if tree == nil {
		return nil, nil
	}
	comments := make([]javascriptByteSpan, 0)
	stringsAndRegex := make([]javascriptByteSpan, 0)
	for nodeIndex, node := range tree.nodes {
		switch node.kind {
		case "comment", "html_comment", "hash_bang_line":
			comments = append(comments, javascriptByteSpan{start: node.startByte, end: node.endByte})
		case "string":
			if !javascriptSyntaxQuotedString(source, node) {
				continue
			}
			stringsAndRegex = append(
				stringsAndRegex,
				javascriptByteSpan{start: node.startByte, end: node.endByte},
			)
		case "regex", "jsx_text":
			stringsAndRegex = append(
				stringsAndRegex,
				javascriptByteSpan{start: node.startByte, end: node.endByte},
			)
		case "template_string", "template_literal_type":
			stringsAndRegex = append(
				stringsAndRegex,
				javascriptTemplateLiteralSpans(source, tree, nodeIndex)...,
			)
		case "jsx_attribute":
			stringsAndRegex = append(
				stringsAndRegex,
				javascriptJSXAttributeSpans(source, tree, nodeIndex)...,
			)
		}
	}
	return normalizeJavaScriptSpans(comments), normalizeJavaScriptSpans(stringsAndRegex)
}

// javascriptSyntaxSemanticLiterals keeps JSX attribute values opaque to tree
// analysis while carving out every executable JSX expression body, including
// expressions nested inside JSX-valued attributes. Public search masking keeps
// using javascriptSyntaxMasks and therefore remains unchanged.
func javascriptSyntaxSemanticLiterals(
	source string,
	tree *javascriptSyntaxTree,
) []javascriptByteSpan {
	if tree == nil {
		return nil
	}
	jsxExpressions := javascriptJSXSemanticExpressionSpans(source, tree)
	spans := make([]javascriptByteSpan, 0)
	for nodeIndex, node := range tree.nodes {
		switch node.kind {
		case "string":
			if javascriptSyntaxQuotedString(source, node) {
				spans = append(spans, javascriptByteSpan{start: node.startByte, end: node.endByte})
			}
		case "regex", "jsx_text":
			spans = append(spans, javascriptByteSpan{start: node.startByte, end: node.endByte})
		case "template_string", "template_literal_type":
			spans = append(spans, javascriptTemplateLiteralSpans(source, tree, nodeIndex)...)
		case "jsx_attribute":
			spans = append(spans, javascriptJSXAttributeSemanticSpans(
				tree, nodeIndex, jsxExpressions,
			)...)
		}
	}
	return normalizeJavaScriptSpans(spans)
}

func javascriptSyntaxQuotedString(source string, node javascriptSyntaxNode) bool {
	if node.startByte < 0 || node.endByte > len(source) || node.startByte >= node.endByte {
		return false
	}
	return source[node.startByte] == '\'' || source[node.startByte] == '"'
}

func javascriptJSXAttributeSpans(
	source string,
	tree *javascriptSyntaxTree,
	attributeIndex int,
) []javascriptByteSpan {
	if tree == nil || attributeIndex < 0 || attributeIndex >= len(tree.nodes) {
		return nil
	}
	attribute := tree.nodes[attributeIndex]
	cursor := attribute.startByte
	spans := make([]javascriptByteSpan, 0, 2)
	for _, childIndex := range attribute.children {
		child := tree.nodes[childIndex]
		if child.kind != "jsx_expression" || child.startByte < cursor ||
			child.endByte > attribute.endByte {
			continue
		}
		bodyStart, bodyEnd := child.startByte, child.endByte
		if bodyStart < len(source) && source[bodyStart] == '{' {
			bodyStart++
		}
		if bodyEnd > bodyStart && bodyEnd <= len(source) && source[bodyEnd-1] == '}' {
			bodyEnd--
		}
		if cursor < bodyStart {
			spans = append(spans, javascriptByteSpan{start: cursor, end: bodyStart})
		}
		cursor = max(cursor, bodyEnd)
	}
	if cursor < attribute.endByte {
		spans = append(spans, javascriptByteSpan{start: cursor, end: attribute.endByte})
	}
	return spans
}

func javascriptJSXAttributeSemanticSpans(
	tree *javascriptSyntaxTree,
	attributeIndex int,
	visible []javascriptByteSpan,
) []javascriptByteSpan {
	if tree == nil || attributeIndex < 0 || attributeIndex >= len(tree.nodes) {
		return nil
	}
	attribute := tree.nodes[attributeIndex]
	visibleStart := sort.Search(len(visible), func(index int) bool {
		return visible[index].end > attribute.startByte
	})
	spans := make([]javascriptByteSpan, 0, 3)
	cursor := attribute.startByte
	for index := visibleStart; index < len(visible); index++ {
		span := visible[index]
		if span.start >= attribute.endByte {
			break
		}
		start := max(span.start, attribute.startByte)
		end := min(span.end, attribute.endByte)
		if start >= end || end <= cursor {
			continue
		}
		if cursor < start {
			spans = append(spans, javascriptByteSpan{start: cursor, end: start})
		}
		cursor = max(cursor, end)
	}
	if cursor < attribute.endByte {
		spans = append(spans, javascriptByteSpan{start: cursor, end: attribute.endByte})
	}
	return spans
}

func javascriptJSXSemanticExpressionSpans(
	source string,
	tree *javascriptSyntaxTree,
) []javascriptByteSpan {
	if tree == nil {
		return nil
	}
	visible := make([]javascriptByteSpan, 0)
	for nodeIndex, node := range tree.nodes {
		if node.kind != "jsx_expression" {
			continue
		}
		visible = append(visible, javascriptJSXExpressionSemanticSpans(
			source, tree, nodeIndex,
		)...)
	}
	return normalizeJavaScriptSpans(visible)
}

func javascriptJSXExpressionSemanticSpans(
	source string,
	tree *javascriptSyntaxTree,
	expressionIndex int,
) []javascriptByteSpan {
	if tree == nil || expressionIndex < 0 || expressionIndex >= len(tree.nodes) {
		return nil
	}
	expression := tree.nodes[expressionIndex]
	start, end := expression.startByte, expression.endByte
	if start < len(source) && source[start] == '{' {
		start++
	}
	if end > start && end <= len(source) && source[end-1] == '}' {
		end--
	}
	if start >= end {
		return nil
	}
	nestedJSX := make([]javascriptByteSpan, 0, 1)
	stack := append([]int(nil), expression.children...)
	for len(stack) > 0 {
		last := len(stack) - 1
		nodeIndex := stack[last]
		stack = stack[:last]
		if nodeIndex < 0 || nodeIndex >= len(tree.nodes) {
			continue
		}
		node := tree.nodes[nodeIndex]
		switch node.kind {
		case "jsx_element", "jsx_self_closing_element", "jsx_fragment":
			nestedJSX = append(nestedJSX, javascriptByteSpan{
				start: node.startByte, end: node.endByte,
			})
			continue
		}
		stack = append(stack, node.children...)
	}
	nestedJSX = normalizeJavaScriptSpans(nestedJSX)
	visible := make([]javascriptByteSpan, 0, len(nestedJSX)+1)
	cursor := start
	for _, span := range nestedJSX {
		if span.end <= cursor || span.start >= end {
			continue
		}
		if cursor < span.start {
			visible = append(visible, javascriptByteSpan{
				start: cursor, end: min(span.start, end),
			})
		}
		cursor = max(cursor, min(span.end, end))
	}
	if cursor < end {
		visible = append(visible, javascriptByteSpan{start: cursor, end: end})
	}
	return visible
}

func javascriptTemplateLiteralSpans(
	source string,
	tree *javascriptSyntaxTree,
	templateIndex int,
) []javascriptByteSpan {
	if tree == nil || templateIndex < 0 || templateIndex >= len(tree.nodes) {
		return nil
	}
	template := tree.nodes[templateIndex]
	cursor := template.startByte
	spans := make([]javascriptByteSpan, 0)
	for _, childIndex := range template.children {
		child := tree.nodes[childIndex]
		if child.kind != "template_substitution" && child.kind != "template_type" ||
			child.startByte < cursor ||
			child.endByte > template.endByte {
			continue
		}
		bodyStart, bodyEnd := child.startByte, child.endByte
		if bodyStart+2 <= len(source) && source[bodyStart:bodyStart+2] == "${" {
			bodyStart += 2
		}
		if bodyEnd > bodyStart && bodyEnd <= len(source) && source[bodyEnd-1] == '}' {
			bodyEnd--
		}
		if cursor < bodyStart {
			spans = append(spans, javascriptByteSpan{start: cursor, end: bodyStart})
		}
		cursor = max(cursor, bodyEnd)
	}
	if cursor < template.endByte {
		spans = append(spans, javascriptByteSpan{start: cursor, end: template.endByte})
	}
	return spans
}

func javascriptSyntaxWholeFileErrorRoot(tree *javascriptSyntaxTree, nodeIndex int) bool {
	return tree != nil && nodeIndex == tree.root && nodeIndex >= 0 &&
		nodeIndex < len(tree.nodes) && tree.nodes[nodeIndex].parent < 0 &&
		tree.nodes[nodeIndex].kind == "ERROR" && tree.nodes[nodeIndex].startByte == 0
}

func javascriptSyntaxRootIsError(tree *javascriptSyntaxTree) bool {
	return tree != nil && tree.root >= 0 && tree.root < len(tree.nodes) &&
		tree.nodes[tree.root].kind == "ERROR"
}

func javascriptRecoveryMaskSpans(
	candidates, recovery []javascriptByteSpan,
) []javascriptByteSpan {
	if len(candidates) == 0 {
		return nil
	}
	if len(recovery) == 0 {
		return candidates
	}
	recovery = normalizeJavaScriptSpans(append([]javascriptByteSpan(nil), recovery...))
	matched := make([]javascriptByteSpan, 0, len(candidates))
	for _, candidate := range candidates {
		index := sort.Search(len(recovery), func(index int) bool {
			return recovery[index].end > candidate.start
		})
		if index < len(recovery) && recovery[index].start < candidate.end {
			matched = append(matched, candidate)
		}
	}
	return matched
}

func javascriptSyntaxErrorContexts(tree *javascriptSyntaxTree) []bool {
	if tree == nil {
		return nil
	}
	contexts := make([]bool, len(tree.nodes))
	for nodeIndex, node := range tree.nodes {
		ownError := node.kind == "ERROR" &&
			(len(tree.nodes) <= 1 || !javascriptSyntaxWholeFileErrorRoot(tree, nodeIndex))
		contexts[nodeIndex] = ownError
		if node.parent >= 0 && node.parent < nodeIndex && contexts[node.parent] {
			contexts[nodeIndex] = true
		}
	}
	return contexts
}

func javascriptSyntaxErrorSpans(
	tree *javascriptSyntaxTree,
	sourceLength int,
) []javascriptByteSpan {
	if tree == nil {
		return nil
	}
	spans := make([]javascriptByteSpan, 0)
	for nodeIndex, node := range tree.nodes {
		if node.kind != "ERROR" ||
			(len(tree.nodes) > 1 && javascriptSyntaxWholeFileErrorRoot(tree, nodeIndex)) {
			continue
		}
		start, end := max(node.startByte, 0), min(node.endByte, sourceLength)
		if end == start && end < sourceLength {
			end++
		} else if end == start && start == sourceLength && start > 0 {
			start--
		}
		if end > start {
			spans = append(spans, javascriptByteSpan{start: start, end: end})
		}
	}
	return normalizeJavaScriptSpans(spans)
}

func javascriptRecoveryLines(source string, spans []javascriptByteSpan) map[int]bool {
	lines := make(map[int]bool)
	positions := javascriptSourcePositions{source: source, lineStarts: javascriptLineStarts(source)}
	for _, span := range spans {
		start, end := positions.lineSpan(span.start, span.end)
		for line := start; line <= end; line++ {
			lines[line] = true
		}
	}
	return lines
}

func javascriptSyntaxAttachedStarts(
	source string,
	tree *javascriptSyntaxTree,
) []int {
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
				javascriptAttachmentGap(source, previousEnd, child.startByte)
			if previousEnd >= 0 && !adjacent {
				pendingStart = -1
			}
			comment := child.kind == "comment"
			jsDoc := comment && child.startByte >= 0 && child.endByte <= len(source) &&
				strings.HasPrefix(source[child.startByte:child.endByte], "/**")
			switch {
			case jsDoc || child.kind == "decorator":
				if pendingStart < 0 {
					pendingStart = child.startByte
				}
			case comment && pendingStart >= 0:
				// Ordinary comments are transparent inside an attached JSDoc or
				// decorator group.
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

func javascriptAttachmentGap(source string, start, end int) bool {
	if start < 0 || end < start || end > len(source) {
		return false
	}
	gap := source[start:end]
	return strings.TrimSpace(gap) == "" && strings.Count(gap, "\n") <= 1
}

func javascriptSyntaxAttachedStart(
	tree *javascriptSyntaxTree,
	nodeIndex int,
	starts []int,
) int {
	if tree == nil || nodeIndex < 0 || nodeIndex >= len(tree.nodes) {
		return 0
	}
	if nodeIndex < len(starts) && starts[nodeIndex] >= 0 &&
		starts[nodeIndex] <= tree.nodes[nodeIndex].startByte {
		return starts[nodeIndex]
	}
	return tree.nodes[nodeIndex].startByte
}

func javascriptIdentifierStartRune(r rune) bool {
	if r == '\u2E2F' {
		// Unicode 17 removed VERTICAL TILDE from ID_Start.
		return false
	}
	return r == '$' || r == '_' || unicode.IsLetter(r) || unicode.In(
		r,
		unicode.Nl,
		unicode.Other_ID_Start,
	) || javascriptRuneInIdentifierRanges(r, javascriptUnicode17IDStartDelta[:])
}

func javascriptIdentifierContinueRune(r rune) bool {
	if r == '\u2E2F' {
		return false
	}
	return javascriptIdentifierStartRune(r) || r == '\u200C' || r == '\u200D' || unicode.In(
		r,
		unicode.Mn,
		unicode.Mc,
		unicode.Nd,
		unicode.Pc,
		unicode.Other_ID_Continue,
	) || javascriptRuneInIdentifierRanges(r, javascriptUnicode17IDContinueDelta[:])
}

func javascriptSourceIdentifier(identifier string) bool {
	if identifier == "" {
		return false
	}
	offset := 0
	r, size, ok := javascriptIdentifierRune(identifier, offset)
	if !ok || !javascriptIdentifierStartRune(r) {
		return false
	}
	offset += size
	for offset < len(identifier) {
		r, size, ok = javascriptIdentifierRune(identifier, offset)
		if !ok || !javascriptIdentifierContinueRune(r) {
			return false
		}
		offset += size
	}
	return true
}

func javascriptSourceName(name string) bool {
	if strings.HasPrefix(name, "#") {
		return len(name) > 1 && javascriptSourceIdentifier(name[1:])
	}
	return javascriptSourceIdentifier(name)
}

func javascriptIdentifierRune(source string, offset int) (rune, int, bool) {
	if offset < 0 || offset >= len(source) {
		return utf8.RuneError, 0, false
	}
	if source[offset] != '\\' {
		r, size := utf8.DecodeRuneInString(source[offset:])
		return r, size, r != utf8.RuneError || size != 1
	}
	if offset+2 > len(source) || source[offset+1] != 'u' {
		return utf8.RuneError, 0, false
	}
	if offset+2 < len(source) && source[offset+2] == '{' {
		value := rune(0)
		digits := 0
		for index := offset + 3; index < len(source); index++ {
			if source[index] == '}' {
				valid := digits > 0 && value <= utf8.MaxRune &&
					(value < 0xD800 || value > 0xDFFF)
				return value, index - offset + 1, valid
			}
			digit, ok := javascriptHexDigit(source[index])
			if !ok {
				return utf8.RuneError, 0, false
			}
			if value > (utf8.MaxRune-rune(digit))/16 {
				return utf8.RuneError, 0, false
			}
			value = value*16 + rune(digit)
			digits++
		}
		return utf8.RuneError, 0, false
	}
	if offset+6 > len(source) {
		return utf8.RuneError, 0, false
	}
	value := rune(0)
	for index := offset + 2; index < offset+6; index++ {
		digit, ok := javascriptHexDigit(source[index])
		if !ok {
			return utf8.RuneError, 0, false
		}
		value = value*16 + rune(digit)
	}
	return value, 6, value < 0xD800 || value > 0xDFFF
}

func javascriptHexDigit(value byte) (int, bool) {
	switch {
	case value >= '0' && value <= '9':
		return int(value - '0'), true
	case value >= 'a' && value <= 'f':
		return int(value-'a') + 10, true
	case value >= 'A' && value <= 'F':
		return int(value-'A') + 10, true
	default:
		return 0, false
	}
}
