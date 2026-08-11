package navigator

import (
	"sort"
	"strings"
)

type javaLineScope struct {
	start int
	end   int
}

type javaLineSpan struct {
	start int
	end   int
}

type javaDefinitionIdentity struct {
	symbol string
	line   int
	column int
}

type javaSourcePositions struct {
	source     string
	lineStarts []int
}

// javaTreeDefinitions extracts only declarations that form useful Java
// navigation targets. In particular, variable_declarator is accepted only
// beneath a field or interface constant declaration. Local variables and all
// parameter-like bindings are deliberately left to neither the concrete nor
// lexical definition set.
func javaTreeDefinitions(
	source string,
	lineCount int,
	tree *javaSyntaxTree,
	attached []int,
	errorContext []bool,
) []sourceDefinition {
	if lineCount < 1 || !validateJavaSyntaxTree(tree, len(source)) {
		return nil
	}
	if len(attached) != len(tree.nodes) {
		attached = javaSyntaxAttachedStarts(source, tree)
	}
	if len(errorContext) != len(tree.nodes) {
		errorContext = javaSyntaxErrorContexts(tree)
	}

	positions := javaSourcePositions{source: source, lineStarts: javaLineStarts(source)}
	definitions := make([]sourceDefinition, 0)
	var moduleHeaderValidity map[int]bool
	moduleHeadersIndexed := false
	for nodeIndex := range tree.nodes {
		if javaSyntaxNodeInError(errorContext, nodeIndex) {
			continue
		}
		nameIndex, scopeIndex, ownsScope, qualified, ok := javaDefinitionDescriptor(
			tree, nodeIndex,
		)
		if !ok || !javaValidSyntaxNodeIndex(tree, nameIndex) ||
			!javaValidSyntaxNodeIndex(tree, scopeIndex) {
			continue
		}
		// The pinned grammar reports Java Unicode escapes in identifiers as
		// header-level ERROR siblings and exposes only a prefix or suffix as the
		// named identifier. Reject that partial concrete name; the lexical pass
		// can retain the complete raw spelling and coordinates.
		if javaSyntaxHasDirectError(tree, nodeIndex) ||
			scopeIndex != nodeIndex && javaSyntaxHasDirectError(tree, scopeIndex) {
			continue
		}

		nameNode := tree.nodes[nameIndex]
		if !javaSyntaxRangeValid(nameNode.startByte, nameNode.endByte, len(source)) {
			continue
		}
		if tree.nodes[nodeIndex].kind == "module_declaration" {
			if !moduleHeadersIndexed {
				moduleHeaderValidity = javaTreeModuleHeaderValidity(source)
				moduleHeadersIndexed = true
			}
			if !moduleHeaderValidity[nameNode.startByte] {
				continue
			}
		}
		symbol := source[nameNode.startByte:nameNode.endByte]
		nameStart := nameNode.startByte
		if qualified {
			var qualifiedOK bool
			symbol, nameStart, qualifiedOK = javaQualifiedSourceSymbol(
				source, nameNode.startByte, nameNode.endByte,
			)
			if !qualifiedOK {
				continue
			}
		} else if !javaIdentifierSourceName(source, nameNode.startByte, nameNode.endByte) {
			continue
		}

		line, column := positions.lineColumn(nameStart)
		scopeStart, scopeEnd := line, line
		ownedEndLine, ownedEndColumn := 0, 0
		if ownsScope {
			scopeNode := tree.nodes[scopeIndex]
			startOffset := javaSyntaxAttachedStart(tree, scopeIndex, attached)
			scopeStart, scopeEnd = positions.lineSpan(startOffset, scopeNode.endByte)
			ownedEndLine, ownedEndColumn = positions.lineColumn(scopeNode.endByte)
		}
		if !ownsScope || ownedEndLine != scopeEnd {
			ownedEndColumn = 0
		}
		definition := normalizeJavaTreeDefinition(sourceDefinition{
			symbol:         symbol,
			line:           line,
			column:         column,
			scopeStart:     scopeStart,
			scopeEnd:       scopeEnd,
			ownedEndColumn: ownedEndColumn,
			ownsScope:      ownsScope,
		}, lineCount)
		if definition.symbol != "" {
			definitions = append(definitions, definition)
		}
	}
	return sortUniqueJavaTreeDefinitions(definitions)
}

func javaDefinitionDescriptor(
	tree *javaSyntaxTree,
	nodeIndex int,
) (nameIndex, scopeIndex int, ownsScope, qualified, ok bool) {
	if !javaValidSyntaxNodeIndex(tree, nodeIndex) {
		return -1, -1, false, false, false
	}
	node := tree.nodes[nodeIndex]
	scopeIndex = nodeIndex
	switch node.kind {
	case "class_declaration", "enum_declaration", "interface_declaration",
		"record_declaration", "annotation_type_declaration":
		nameIndex = javaDirectChildOfKind(tree, nodeIndex, "identifier")
		ownsScope = true
	case "module_declaration":
		nameIndex = javaDirectChildOfKinds(
			tree, nodeIndex, "scoped_identifier", "identifier",
		)
		ownsScope, qualified = true, nameIndex >= 0 &&
			tree.nodes[nameIndex].kind == "scoped_identifier"
	case "method_declaration":
		nameIndex = javaDirectChildOfKind(tree, nodeIndex, "identifier")
		ownsScope = true
	case "constructor_declaration":
		nameIndex = javaDirectChildOfKind(tree, nodeIndex, "identifier")
		ownsScope = true
	case "compact_constructor_declaration":
		nameIndex = javaDirectChildOfKind(tree, nodeIndex, "identifier")
		ownsScope = true
	case "annotation_type_element_declaration":
		nameIndex = javaDirectChildOfKind(tree, nodeIndex, "identifier")
		ownsScope = true
	case "enum_constant":
		nameIndex = javaDirectChildOfKind(tree, nodeIndex, "identifier")
		ownsScope = javaSyntaxHasScopedInitializer(tree, nodeIndex)
	case "variable_declarator":
		parent := node.parent
		if !javaValidSyntaxNodeIndex(tree, parent) {
			return -1, -1, false, false, false
		}
		switch tree.nodes[parent].kind {
		case "field_declaration", "constant_declaration":
			nameIndex = javaDirectChildOfKind(tree, nodeIndex, "identifier")
			scopeIndex = parent
			ownsScope = javaSyntaxHasScopedInitializer(tree, nodeIndex)
		default:
			return -1, -1, false, false, false
		}
	case "formal_parameter":
		if !javaRecordComponentParameter(tree, nodeIndex) {
			return -1, -1, false, false, false
		}
		nameIndex = javaDirectChildOfKind(tree, nodeIndex, "identifier")
	case "spread_parameter":
		if !javaRecordComponentParameter(tree, nodeIndex) {
			return -1, -1, false, false, false
		}
		declarator := javaDirectChildOfKind(tree, nodeIndex, "variable_declarator")
		nameIndex = javaDirectChildOfKind(tree, declarator, "identifier")
	default:
		return -1, -1, false, false, false
	}
	return nameIndex, scopeIndex, ownsScope, qualified, nameIndex >= 0
}

func javaRecordComponentParameter(tree *javaSyntaxTree, nodeIndex int) bool {
	if !javaValidSyntaxNodeIndex(tree, nodeIndex) ||
		(tree.nodes[nodeIndex].kind != "formal_parameter" &&
			tree.nodes[nodeIndex].kind != "spread_parameter") {
		return false
	}
	parameters := tree.nodes[nodeIndex].parent
	if !javaValidSyntaxNodeIndex(tree, parameters) ||
		tree.nodes[parameters].kind != "formal_parameters" {
		return false
	}
	record := tree.nodes[parameters].parent
	return javaValidSyntaxNodeIndex(tree, record) &&
		tree.nodes[record].kind == "record_declaration"
}

func javaSyntaxHasScopedInitializer(tree *javaSyntaxTree, nodeIndex int) bool {
	if !javaValidSyntaxNodeIndex(tree, nodeIndex) {
		return false
	}
	stack := make([]int, 0, len(tree.nodes[nodeIndex].children))
	stack = append(stack, tree.nodes[nodeIndex].children...)
	operations, operationLimit := 0, len(tree.nodes)+1
	for len(stack) > 0 && operations < operationLimit {
		operations++
		last := len(stack) - 1
		candidateIndex := stack[last]
		stack = stack[:last]
		if !javaValidSyntaxNodeIndex(tree, candidateIndex) {
			return false
		}
		candidate := tree.nodes[candidateIndex]
		switch candidate.kind {
		case "lambda_expression", "switch_expression", "class_body":
			return true
		case "object_creation_expression":
			if javaSyntaxHasDirectChild(tree, candidateIndex, "class_body") {
				return true
			}
		}
		if len(stack) > len(tree.nodes)-len(candidate.children) {
			return false
		}
		stack = append(stack, candidate.children...)
	}
	return false
}

func normalizeJavaTreeDefinition(
	definition sourceDefinition,
	lineCount int,
) sourceDefinition {
	if definition.symbol == "" || definition.line < 1 || definition.line > lineCount {
		return sourceDefinition{}
	}
	definition.column = max(definition.column, 1)
	if definition.scopeStart < 1 || definition.scopeStart > definition.line {
		definition.scopeStart = definition.line
	}
	if definition.scopeEnd < definition.line {
		definition.scopeEnd = definition.line
	}
	definition.scopeEnd = min(definition.scopeEnd, lineCount)
	return definition
}

func sortUniqueJavaTreeDefinitions(definitions []sourceDefinition) []sourceDefinition {
	sort.SliceStable(definitions, func(first, second int) bool {
		if definitions[first].line != definitions[second].line {
			return definitions[first].line < definitions[second].line
		}
		if definitions[first].column != definitions[second].column {
			return definitions[first].column < definitions[second].column
		}
		return definitions[first].symbol < definitions[second].symbol
	})
	seen := make(map[javaDefinitionIdentity]bool, len(definitions))
	unique := definitions[:0]
	for _, definition := range definitions {
		identity := javaDefinitionIdentity{
			symbol: definition.symbol,
			line:   definition.line,
			column: definition.column,
		}
		if seen[identity] {
			continue
		}
		seen[identity] = true
		unique = append(unique, definition)
	}
	return unique
}

// javaTreeScopes returns concrete lexical scopes. A declaration body is
// represented by its declaration node, so its direct body node is suppressed;
// standalone blocks and nested control-flow constructs remain independently
// selectable scopes.
func javaTreeScopes(
	source string,
	lineCount int,
	tree *javaSyntaxTree,
	attached []int,
	errorContext []bool,
) []javaLineScope {
	if lineCount < 1 || !validateJavaSyntaxTree(tree, len(source)) {
		return nil
	}
	if len(attached) != len(tree.nodes) {
		attached = javaSyntaxAttachedStarts(source, tree)
	}
	if len(errorContext) != len(tree.nodes) {
		errorContext = javaSyntaxErrorContexts(tree)
	}

	positions := javaSourcePositions{source: source, lineStarts: javaLineStarts(source)}
	scopes := make([]javaLineScope, 0)
	for nodeIndex := range tree.nodes {
		if javaSyntaxNodeInError(errorContext, nodeIndex) {
			continue
		}
		scopeIndex, attachDeclaration, ok := javaTreeScopeDescriptor(tree, nodeIndex)
		if !ok || !javaValidSyntaxNodeIndex(tree, scopeIndex) {
			continue
		}
		scopeNode := tree.nodes[scopeIndex]
		startOffset := scopeNode.startByte
		if attachDeclaration {
			startOffset = javaSyntaxAttachedStart(tree, scopeIndex, attached)
		}
		start, end := positions.lineSpan(startOffset, scopeNode.endByte)
		start = max(start, 1)
		end = min(max(end, start), lineCount)
		if start <= end {
			scopes = append(scopes, javaLineScope{start: start, end: end})
		}
	}
	scopes = append(scopes, javaTreeBranchScopes(
		source, lineCount, tree, errorContext, positions,
	)...)
	return normalizeJavaLineScopes(scopes, lineCount)
}

func javaTreeBranchScopes(
	source string,
	lineCount int,
	tree *javaSyntaxTree,
	errorContext []bool,
	positions javaSourcePositions,
) []javaLineScope {
	if !validateJavaSyntaxTree(tree, len(source)) {
		return nil
	}
	scopes := make([]javaLineScope, 0)
	appendScope := func(start, end int) {
		startLine, endLine := positions.lineSpan(start, end)
		if startLine >= 1 && endLine >= startLine && endLine <= lineCount {
			scopes = append(scopes, javaLineScope{start: startLine, end: endLine})
		}
	}
	for nodeIndex, node := range tree.nodes {
		if javaSyntaxNodeInError(errorContext, nodeIndex) {
			continue
		}
		switch node.kind {
		case "if_statement":
			branchStart := node.startByte
			awaitingBody := false
			for _, childIndex := range node.children {
				if !javaValidSyntaxNodeIndex(tree, childIndex) {
					continue
				}
				child := tree.nodes[childIndex]
				if child.kind == "parenthesized_expression" {
					awaitingBody = true
					continue
				}
				if child.kind == "else" {
					branchStart = child.startByte
					awaitingBody = true
					continue
				}
				if awaitingBody && child.kind != "line_comment" &&
					child.kind != "block_comment" {
					appendScope(branchStart, child.endByte)
					awaitingBody = false
				}
			}
		case "try_statement", "try_with_resources_statement":
			for _, childIndex := range node.children {
				if !javaValidSyntaxNodeIndex(tree, childIndex) {
					continue
				}
				child := tree.nodes[childIndex]
				if child.kind == "block" {
					appendScope(node.startByte, child.endByte)
					break
				}
			}
		}
	}
	return scopes
}

func javaTreeScopeDescriptor(
	tree *javaSyntaxTree,
	nodeIndex int,
) (scopeIndex int, attachDeclaration, ok bool) {
	if !javaValidSyntaxNodeIndex(tree, nodeIndex) {
		return -1, false, false
	}
	node := tree.nodes[nodeIndex]
	switch node.kind {
	case "class_declaration", "enum_declaration", "interface_declaration",
		"record_declaration", "annotation_type_declaration", "module_declaration":
		return nodeIndex, true, true
	case "method_declaration", "constructor_declaration",
		"compact_constructor_declaration", "annotation_type_element_declaration":
		return nodeIndex, true, true
	case "enum_constant":
		return nodeIndex, true, javaSyntaxHasScopedInitializer(tree, nodeIndex)
	case "variable_declarator":
		parent := node.parent
		if !javaValidSyntaxNodeIndex(tree, parent) {
			return -1, false, false
		}
		switch tree.nodes[parent].kind {
		case "field_declaration", "constant_declaration":
			return parent, true, javaSyntaxHasScopedInitializer(tree, nodeIndex)
		default:
			return -1, false, false
		}
	case "static_initializer":
		return nodeIndex, true, true
	case "lambda_expression":
		return nodeIndex, false, true
	case "object_creation_expression":
		return nodeIndex, false, javaSyntaxHasDirectChild(tree, nodeIndex, "class_body")
	case "if_statement", "while_statement", "do_statement", "for_statement",
		"enhanced_for_statement", "synchronized_statement", "labeled_statement",
		"try_statement", "try_with_resources_statement", "catch_clause",
		"finally_clause", "switch_expression", "switch_block_statement_group",
		"switch_rule":
		return nodeIndex, false, true
	case "block", "constructor_body", "class_body", "enum_body", "interface_body",
		"annotation_type_body", "module_body", "switch_block":
		if javaTreeBodyOwnedByParent(tree, nodeIndex) {
			return -1, false, false
		}
		return nodeIndex, false, true
	default:
		return -1, false, false
	}
}

func javaTreeBodyOwnedByParent(tree *javaSyntaxTree, nodeIndex int) bool {
	if !javaValidSyntaxNodeIndex(tree, nodeIndex) {
		return false
	}
	parent := tree.nodes[nodeIndex].parent
	if !javaValidSyntaxNodeIndex(tree, parent) {
		return false
	}
	switch tree.nodes[nodeIndex].kind {
	case "constructor_body":
		return tree.nodes[parent].kind == "constructor_declaration"
	case "class_body":
		switch tree.nodes[parent].kind {
		case "class_declaration", "record_declaration", "enum_constant",
			"object_creation_expression":
			return true
		}
	case "enum_body":
		return tree.nodes[parent].kind == "enum_declaration"
	case "interface_body":
		return tree.nodes[parent].kind == "interface_declaration"
	case "annotation_type_body":
		return tree.nodes[parent].kind == "annotation_type_declaration"
	case "module_body":
		return tree.nodes[parent].kind == "module_declaration"
	case "switch_block":
		return tree.nodes[parent].kind == "switch_expression"
	case "block":
		switch tree.nodes[parent].kind {
		case "method_declaration", "compact_constructor_declaration",
			"static_initializer", "lambda_expression", "if_statement",
			"while_statement", "do_statement", "for_statement",
			"enhanced_for_statement", "synchronized_statement", "try_statement",
			"try_with_resources_statement", "catch_clause", "finally_clause",
			"switch_rule":
			return true
		}
	}
	return false
}

func normalizeJavaLineScopes(scopes []javaLineScope, lineCount int) []javaLineScope {
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

// javaTreeImports includes type, static, and single-module imports together
// with module-info requires directives. Other module directives describe the
// current module's API rather than a dependency import and are excluded.
func javaTreeImports(
	source string,
	lineCount int,
	tree *javaSyntaxTree,
	attached []int,
	errorContext []bool,
) []javaLineSpan {
	if lineCount < 1 || !validateJavaSyntaxTree(tree, len(source)) {
		return nil
	}
	if len(attached) != len(tree.nodes) {
		attached = javaSyntaxAttachedStarts(source, tree)
	}
	if len(errorContext) != len(tree.nodes) {
		errorContext = javaSyntaxErrorContexts(tree)
	}

	positions := javaSourcePositions{source: source, lineStarts: javaLineStarts(source)}
	imports := make([]javaLineSpan, 0)
	for nodeIndex, node := range tree.nodes {
		if !javaTreeImportKind(node.kind) || javaSyntaxNodeInError(errorContext, nodeIndex) {
			continue
		}
		if node.kind == "import_declaration" && javaSyntaxHasDirectError(tree, nodeIndex) {
			continue
		}
		startOffset := javaSyntaxAttachedStart(tree, nodeIndex, attached)
		start, end := positions.lineSpan(startOffset, node.endByte)
		if start >= 1 && end >= start && end <= lineCount {
			imports = append(imports, javaLineSpan{start: start, end: end})
		}
	}
	return normalizeJavaLineSpans(imports, lineCount)
}

func javaTreeImportKind(kind string) bool {
	switch kind {
	case "import_declaration", "requires_module_directive":
		return true
	default:
		return false
	}
}

func normalizeJavaLineSpans(spans []javaLineSpan, lineCount int) []javaLineSpan {
	sort.Slice(spans, func(first, second int) bool {
		if spans[first].start != spans[second].start {
			return spans[first].start < spans[second].start
		}
		return spans[first].end < spans[second].end
	})
	unique := spans[:0]
	for _, span := range spans {
		if span.start < 1 || span.end < span.start || span.end > lineCount {
			continue
		}
		if len(unique) == 0 || unique[len(unique)-1] != span {
			unique = append(unique, span)
		}
	}
	return unique
}

// javaSyntaxMasks returns coordinate-preserving comment and literal spans.
// A string template is split around each interpolation body: its delimiters
// and literal fragments remain opaque while executable Java stays searchable.
func javaSyntaxMasks(
	source string,
	tree *javaSyntaxTree,
) (comments, stringsAndCharacters []javaByteSpan) {
	if !validateJavaSyntaxTree(tree, len(source)) {
		return nil, nil
	}
	comments = make([]javaByteSpan, 0)
	stringsAndCharacters = make([]javaByteSpan, 0)
	for nodeIndex, node := range tree.nodes {
		if !javaSyntaxRangeValid(node.startByte, node.endByte, len(source)) {
			continue
		}
		switch node.kind {
		case "line_comment", "block_comment":
			comments = append(comments, javaByteSpan{start: node.startByte, end: node.endByte})
		case "character_literal":
			stringsAndCharacters = append(stringsAndCharacters, javaByteSpan{
				start: node.startByte,
				end:   node.endByte,
			})
		case "string_literal":
			if javaSyntaxHasDirectChild(tree, nodeIndex, "string_interpolation") {
				stringsAndCharacters = append(
					stringsAndCharacters,
					javaStringTemplateMaskSpans(source, tree, nodeIndex)...,
				)
			} else {
				stringsAndCharacters = append(stringsAndCharacters, javaByteSpan{
					start: node.startByte,
					end:   node.endByte,
				})
			}
		}
	}
	return normalizeJavaSpans(comments), normalizeJavaSpans(stringsAndCharacters)
}

func javaStringTemplateMaskSpans(
	source string,
	tree *javaSyntaxTree,
	literalIndex int,
) []javaByteSpan {
	if !javaValidSyntaxNodeIndex(tree, literalIndex) {
		return nil
	}
	literal := tree.nodes[literalIndex]
	if !javaSyntaxRangeValid(literal.startByte, literal.endByte, len(source)) {
		return nil
	}
	cursor := literal.startByte
	spans := make([]javaByteSpan, 0, 3)
	for _, childIndex := range literal.children {
		if !javaValidSyntaxNodeIndex(tree, childIndex) {
			return nil
		}
		child := tree.nodes[childIndex]
		if child.kind != "string_interpolation" || child.startByte < cursor ||
			child.endByte > literal.endByte {
			continue
		}
		bodyStart, bodyEnd := child.startByte, child.endByte
		if bodyStart+2 <= len(source) && source[bodyStart:bodyStart+2] == `\{` {
			bodyStart += 2
		}
		if bodyEnd > bodyStart && bodyEnd <= len(source) && source[bodyEnd-1] == '}' {
			bodyEnd--
		}
		if bodyStart < cursor || bodyEnd < bodyStart {
			continue
		}
		if cursor < bodyStart {
			spans = append(spans, javaByteSpan{start: cursor, end: bodyStart})
		}
		cursor = max(cursor, bodyEnd)
	}
	if cursor < literal.endByte {
		spans = append(spans, javaByteSpan{start: cursor, end: literal.endByte})
	}
	return spans
}

func javaSyntaxErrorContexts(tree *javaSyntaxTree) []bool {
	if tree == nil || len(tree.nodes) == 0 {
		return nil
	}
	contexts := make([]bool, len(tree.nodes))
	for nodeIndex, node := range tree.nodes {
		ownError := node.kind == "ERROR" &&
			(len(tree.nodes) <= 1 || !javaSyntaxWholeFileErrorRoot(tree, nodeIndex))
		contexts[nodeIndex] = ownError
		if node.parent < 0 {
			continue
		}
		if node.parent >= nodeIndex || node.parent >= len(tree.nodes) {
			contexts[nodeIndex] = true
			continue
		}
		contexts[nodeIndex] = contexts[nodeIndex] || contexts[node.parent]
	}
	return contexts
}

func javaSyntaxErrorSpans(
	tree *javaSyntaxTree,
	sourceLength int,
) []javaByteSpan {
	if !validateJavaSyntaxTree(tree, sourceLength) {
		return nil
	}
	spans := make([]javaByteSpan, 0)
	for nodeIndex, node := range tree.nodes {
		if node.kind != "ERROR" ||
			(len(tree.nodes) > 1 && javaSyntaxWholeFileErrorRoot(tree, nodeIndex)) {
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
			spans = append(spans, javaByteSpan{start: start, end: end})
		}
	}
	return normalizeJavaSpans(spans)
}

func javaSyntaxWholeFileErrorRoot(tree *javaSyntaxTree, nodeIndex int) bool {
	return javaValidSyntaxNodeIndex(tree, nodeIndex) && nodeIndex == tree.root &&
		tree.nodes[nodeIndex].parent < 0 && tree.nodes[nodeIndex].kind == "ERROR" &&
		tree.nodes[nodeIndex].startByte == 0
}

// javaSyntaxAttachedStarts associates an adjacent traditional or Markdown
// documentation group with the item that follows it. One physical line break
// is allowed between adjacent pieces; a blank line terminates the attachment.
// Ordinary comments are transparent only after a documentation group begins.
func javaSyntaxAttachedStarts(source string, tree *javaSyntaxTree) []int {
	if !validateJavaSyntaxTree(tree, len(source)) {
		return nil
	}
	starts := make([]int, len(tree.nodes))
	for nodeIndex, node := range tree.nodes {
		starts[nodeIndex] = node.startByte
	}
	for _, parent := range tree.nodes {
		pendingStart, previousEnd := -1, -1
		markdownRun := false
		for _, childIndex := range parent.children {
			if !javaValidSyntaxNodeIndex(tree, childIndex) {
				pendingStart, previousEnd = -1, -1
				markdownRun = false
				continue
			}
			child := tree.nodes[childIndex]
			adjacent := previousEnd >= 0 && previousEnd <= child.startByte &&
				javaAttachmentGap(source, previousEnd, child.startByte)
			if previousEnd >= 0 && !adjacent {
				pendingStart = -1
				markdownRun = false
			}

			comment := child.kind == "line_comment" || child.kind == "block_comment"
			javadoc := false
			if child.kind == "block_comment" &&
				javaSyntaxRangeValid(child.startByte, child.endByte, len(source)) {
				javadoc = strings.HasPrefix(source[child.startByte:child.endByte], "/**")
			}
			markdown := child.kind == "line_comment" &&
				javaMarkdownDocumentationLine(source, child)
			switch {
			case markdown:
				if !markdownRun {
					// An ordinary comment can remain transparent after a docs
					// group, but it ends the Markdown run. A closer run supersedes
					// the stale one for declaration ownership.
					pendingStart = child.startByte
				}
				markdownRun = true
			case javadoc:
				if pendingStart < 0 {
					pendingStart = child.startByte
				}
				markdownRun = false
			case comment && pendingStart >= 0:
				// Retain pending documentation through an adjacent ordinary comment.
				markdownRun = false
			default:
				if pendingStart >= 0 && adjacent {
					starts[childIndex] = pendingStart
				}
				pendingStart = -1
				markdownRun = false
			}
			previousEnd = child.endByte
		}
	}
	return starts
}

func javaMarkdownDocumentationLine(source string, node javaSyntaxNode) bool {
	if !javaSyntaxRangeValid(node.startByte, node.endByte, len(source)) ||
		!strings.HasPrefix(source[node.startByte:node.endByte], "///") {
		return false
	}
	lineStart := strings.LastIndexAny(source[:node.startByte], "\r\n") + 1
	for offset := lineStart; offset < node.startByte; offset++ {
		switch source[offset] {
		case ' ', '\t', '\f':
		default:
			return false
		}
	}
	return true
}

func javaSyntaxAttachedStart(tree *javaSyntaxTree, nodeIndex int, starts []int) int {
	if !javaValidSyntaxNodeIndex(tree, nodeIndex) {
		return 0
	}
	if nodeIndex < len(starts) && starts[nodeIndex] >= 0 &&
		starts[nodeIndex] <= tree.nodes[nodeIndex].startByte {
		return starts[nodeIndex]
	}
	return tree.nodes[nodeIndex].startByte
}

func javaAttachmentGap(source string, start, end int) bool {
	if start < 0 || end < start || end > len(source) ||
		strings.TrimSpace(source[start:end]) != "" {
		return false
	}
	lineBreaks := 0
	for offset := start; offset < end; offset++ {
		switch source[offset] {
		case '\n':
			lineBreaks++
		case '\r':
			lineBreaks++
			if offset+1 < end && source[offset+1] == '\n' {
				offset++
			}
		}
		if lineBreaks > 1 {
			return false
		}
	}
	return true
}

func javaDirectChildOfKind(tree *javaSyntaxTree, nodeIndex int, kind string) int {
	return javaDirectChildOfKinds(tree, nodeIndex, kind)
}

func javaDirectChildOfKinds(tree *javaSyntaxTree, nodeIndex int, kinds ...string) int {
	if !javaValidSyntaxNodeIndex(tree, nodeIndex) {
		return -1
	}
	for _, childIndex := range tree.nodes[nodeIndex].children {
		if !javaValidSyntaxNodeIndex(tree, childIndex) {
			return -1
		}
		for _, kind := range kinds {
			if tree.nodes[childIndex].kind == kind {
				return childIndex
			}
		}
	}
	return -1
}

func javaSyntaxHasDirectChild(tree *javaSyntaxTree, nodeIndex int, kind string) bool {
	return javaDirectChildOfKind(tree, nodeIndex, kind) >= 0
}

func javaSyntaxHasDirectError(tree *javaSyntaxTree, nodeIndex int) bool {
	return javaSyntaxHasDirectChild(tree, nodeIndex, "ERROR")
}

func javaValidSyntaxNodeIndex(tree *javaSyntaxTree, nodeIndex int) bool {
	return tree != nil && nodeIndex >= 0 && nodeIndex < len(tree.nodes)
}

func javaSyntaxNodeInError(errorContext []bool, nodeIndex int) bool {
	return nodeIndex < 0 || nodeIndex >= len(errorContext) || errorContext[nodeIndex]
}

func javaSyntaxRangeValid(start, end, sourceLength int) bool {
	return sourceLength >= 0 && start >= 0 && end > start && end <= sourceLength
}

func javaIdentifierSourceName(source string, start, end int) bool {
	if !javaSyntaxRangeValid(start, end, len(source)) {
		return false
	}
	token, next, ok := javaIdentifierToken(source, start, end)
	return ok && next == end && javaTokenIsSourceName(token)
}

func javaQualifiedSourceSymbol(source string, start, end int) (string, int, bool) {
	if !javaSyntaxRangeValid(start, end, len(source)) {
		return "", 0, false
	}
	lexed := lexJava(source[start:end])
	// A malformed scoped_identifier can span an opaque literal while still
	// exposing the identifiers and dots on either side as ordinary children.
	// Comments are valid trivia in a module name, but strings, character
	// literals, text blocks, and template fragments must break the qualified
	// name rather than being silently skipped by the lexer.
	if len(lexed.stringSpans) != 0 || len(lexed.tokens) == 0 ||
		len(lexed.tokens)%2 == 0 {
		return "", 0, false
	}
	var symbol strings.Builder
	for index, token := range lexed.tokens {
		if index%2 == 0 {
			if !javaTokenIsSourceName(token) {
				return "", 0, false
			}
			if index > 0 {
				symbol.WriteByte('.')
			}
			symbol.WriteString(token.text)
			continue
		}
		if token.value != "." {
			return "", 0, false
		}
	}
	return symbol.String(), start + lexed.tokens[0].start, true
}

// javaTreeModuleHeaderValidity applies the lexical header rule to all concrete
// module declarations in one source pass. Error recovery in the grammar can
// retain a clean module_declaration node while an illegal literal sits between
// `open` and `module`; matching names back to the full token stream ensures
// that such an ERROR sibling cannot be ignored without rescanning for every
// malformed declaration.
func javaTreeModuleHeaderValidity(source string) map[int]bool {
	valid := make(map[int]bool)
	lexed := lexJava(source)
	if lexed.truncated || len(lexed.tokens) == 0 {
		return valid
	}
	tokens := lexed.tokens
	delimiters := analyzeJavaDelimiters(tokens)
	moduleRestart := false
	for index := range tokens {
		if index < len(delimiters.braceOwner) && delimiters.braceOwner[index] < 0 &&
			(tokens[index].value == ";" || tokens[index].value == "{") {
			moduleRestart = false
		}
		start, headerNameStart, _, bodyOpen, candidate, ok := javaLexicalModuleHeader(
			tokens, delimiters, index, moduleRestart,
		)
		if candidate {
			moduleRestart = true
		}
		if !ok {
			continue
		}
		valid[tokens[headerNameStart].start] = !javaModuleHeaderHasIllegalOpaque(
			tokens, delimiters, start, index, bodyOpen, lexed.stringSpans,
		)
	}
	return valid
}

func javaLineStarts(source string) []int {
	starts := []int{0}
	for offset := range len(source) {
		if source[offset] == '\n' {
			starts = append(starts, offset+1)
		}
	}
	return starts
}

func (positions javaSourcePositions) lineColumn(offset int) (int, int) {
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

func (positions javaSourcePositions) lineSpan(start, end int) (int, int) {
	startLine, _ := positions.lineColumn(start)
	endOffset := end
	if endOffset > start {
		endOffset--
	}
	endLine, _ := positions.lineColumn(endOffset)
	return startLine, max(startLine, endLine)
}
