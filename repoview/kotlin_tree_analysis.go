package repoview

import (
	"sort"
	"strings"
)

// kotlinTreeDefinitions extracts source-level and member declarations useful
// to navigation. Local functions and local named types remain useful targets;
// ordinary local variables and parameter bindings are deliberately omitted.
func kotlinTreeDefinitions(
	source string,
	lineCount int,
	tree *kotlinSyntaxTree,
) []sourceDefinition {
	if lineCount < 1 || !validateKotlinSyntaxTree(tree, len(source)) {
		return nil
	}

	errorContext := kotlinSyntaxErrorContexts(tree)
	recoveryDescendant := kotlinSyntaxErrorDescendants(tree)
	attachedStarts := kotlinSyntaxAttachedStarts(source, tree)
	positions := cSourcePositions{
		source:     source,
		lineStarts: cTreeLineStarts(source),
	}
	definitions := make([]sourceDefinition, 0)

	appendDefinition := func(
		nameIndex, scopeIndex int,
		symbol string,
		nameStart int,
		ownsScope bool,
	) {
		if !cValidSyntaxNodeIndex(tree, nameIndex) ||
			!cValidSyntaxNodeIndex(tree, scopeIndex) ||
			symbol == "" || nameStart < 0 || nameStart >= len(source) {
			return
		}
		line, column := positions.lineColumn(nameStart)
		scopeStart, scopeEnd := line, line
		if ownsScope {
			scope := tree.nodes[scopeIndex]
			start := kotlinSyntaxAttachedScopeStart(
				tree, scopeIndex, attachedStarts,
			)
			end := kotlinTreeDeclarationScopeEnd(source, tree, scopeIndex)
			if end < scope.endByte {
				end = scope.endByte
			}
			scopeStart, scopeEnd = positions.lineSpan(start, end)
		}
		definitions = append(definitions, sourceDefinition{
			symbol: symbol, line: line, column: column,
			scopeStart: scopeStart, scopeEnd: scopeEnd, ownsScope: ownsScope,
		})
	}

	appendIdentifierDefinition := func(nameIndex, scopeIndex int, ownsScope bool) {
		symbol, nameStart, ok := kotlinTreeIdentifierSourceName(
			source, tree, nameIndex,
		)
		if !ok {
			return
		}
		appendDefinition(
			nameIndex, scopeIndex, symbol, nameStart, ownsScope,
		)
	}

	for nodeIndex, node := range tree.nodes {
		if kotlinSyntaxNodeInError(errorContext, nodeIndex) {
			continue
		}
		if kotlinTreeDeclarationKind(node.kind) &&
			kotlinTreeDeclarationSignatureHasRecovery(
				tree, nodeIndex, recoveryDescendant,
			) && !kotlinTreeRecoverableTruncatedDeclaration(
			source, tree, nodeIndex,
		) {
			continue
		}

		switch node.kind {
		case "class_declaration", "object_declaration", "companion_object":
			nameIndex := cDirectChildOfKind(tree, nodeIndex, "type_identifier")
			// An unnamed companion object has the implicit runtime name
			// Companion, but there is no source spelling or byte coordinate to
			// expose as an outline definition.
			appendIdentifierDefinition(
				nameIndex, nodeIndex,
				kotlinTreeDeclarationOwnsScope(tree, nodeIndex),
			)

		case "function_declaration":
			nameIndex := cDirectChildOfKind(tree, nodeIndex, "simple_identifier")
			appendIdentifierDefinition(
				nameIndex, nodeIndex,
				kotlinTreeDeclarationOwnsScope(tree, nodeIndex),
			)

		case "property_declaration":
			if !kotlinTreeOutlineProperty(tree, nodeIndex) {
				continue
			}
			ownsScope := kotlinTreeDeclarationOwnsScope(tree, nodeIndex)
			for _, nameIndex := range kotlinTreePropertyNameNodes(tree, nodeIndex) {
				appendIdentifierDefinition(nameIndex, nodeIndex, ownsScope)
			}

		case "type_alias":
			if !kotlinTreeOutlineTypeAlias(tree, nodeIndex) {
				continue
			}
			nameIndex := cDirectChildOfKind(tree, nodeIndex, "type_identifier")
			appendIdentifierDefinition(nameIndex, nodeIndex, false)

		case "class_parameter":
			if !kotlinTreeConstructorProperty(tree, nodeIndex) {
				continue
			}
			nameIndex := cDirectChildOfKind(tree, nodeIndex, "simple_identifier")
			appendIdentifierDefinition(nameIndex, nodeIndex, false)

		case "enum_entry":
			if parent := node.parent; !cValidSyntaxNodeIndex(tree, parent) ||
				tree.nodes[parent].kind != "enum_class_body" {
				continue
			}
			nameIndex := cDirectChildOfKind(tree, nodeIndex, "simple_identifier")
			appendIdentifierDefinition(
				nameIndex, nodeIndex,
				kotlinTreeDeclarationOwnsScope(tree, nodeIndex),
			)

		case "secondary_constructor":
			nameIndex := cDirectChildOfKind(tree, nodeIndex, "constructor")
			if !kotlinTreeExactSourceToken(source, tree, nameIndex, "constructor") {
				continue
			}
			name := tree.nodes[nameIndex]
			appendDefinition(
				nameIndex, nodeIndex, "constructor", name.startByte,
				kotlinTreeDeclarationOwnsScope(tree, nodeIndex),
			)
		}
	}

	return cSortUniqueTreeDefinitions(definitions, lineCount)
}

func kotlinTreeDeclarationKind(kind string) bool {
	switch kind {
	case "class_declaration", "object_declaration", "companion_object",
		"function_declaration", "property_declaration", "type_alias",
		"class_parameter", "enum_entry", "secondary_constructor",
		"import_header":
		return true
	default:
		return false
	}
}

func kotlinTreeDeclarationSignatureHasRecovery(
	tree *kotlinSyntaxTree,
	nodeIndex int,
	recoveryDescendant []bool,
) bool {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return true
	}
	boundary := kotlinTreeDeclarationBodyStart(tree, nodeIndex)
	for _, childIndex := range tree.nodes[nodeIndex].children {
		if !cValidSyntaxNodeIndex(tree, childIndex) {
			return true
		}
		child := tree.nodes[childIndex]
		if boundary >= 0 && child.startByte >= boundary {
			continue
		}
		if kotlinSyntaxNodeFlagged(recoveryDescendant, childIndex) {
			return true
		}
	}
	return false
}

func kotlinTreeDeclarationBodyStart(tree *kotlinSyntaxTree, nodeIndex int) int {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return -1
	}
	node := tree.nodes[nodeIndex]
	for _, childIndex := range node.children {
		if !cValidSyntaxNodeIndex(tree, childIndex) {
			return -1
		}
		child := tree.nodes[childIndex]
		body := false
		switch node.kind {
		case "class_declaration":
			body = child.kind == "class_body" || child.kind == "enum_class_body"
		case "object_declaration", "companion_object", "enum_entry":
			body = child.kind == "class_body"
		case "function_declaration":
			body = child.kind == "function_body"
		case "property_declaration":
			body = child.kind == "=" || child.kind == "property_delegate" ||
				child.kind == "getter" || child.kind == "setter"
		case "secondary_constructor":
			body = child.kind == "{" || child.kind == "statements"
		}
		if body {
			return child.startByte
		}
	}
	return -1
}

func kotlinTreeDeclarationOwnsScope(tree *kotlinSyntaxTree, nodeIndex int) bool {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return false
	}
	node := tree.nodes[nodeIndex]
	switch node.kind {
	case "class_declaration", "object_declaration", "companion_object",
		"function_declaration", "secondary_constructor":
		// Kotlin declarations without bodies (expect/abstract members and
		// bodyless local types) still form useful one-line named owners, matching
		// the Java backend's declaration policy.
		return true
	case "property_declaration":
		return cDirectChildOfKind(tree, nodeIndex, "property_delegate") >= 0 ||
			cDirectChildOfKind(tree, nodeIndex, "getter") >= 0 ||
			cDirectChildOfKind(tree, nodeIndex, "setter") >= 0 ||
			kotlinTreePropertyHasSiblingAccessor(tree, nodeIndex) ||
			kotlinTreePropertyInInterface(tree, nodeIndex)
	case "enum_entry":
		return cDirectChildOfKind(tree, nodeIndex, "class_body") >= 0
	default:
		return false
	}
}

// The pinned grammar accepts accessors as class-body declarations because
// automatic semicolon insertion cannot yet distinguish every accessor
// newline. Associate an immediately following getter/setter with its property
// so navigation sees the source declaration as one named scope.
func kotlinTreePropertyHasSiblingAccessor(
	tree *kotlinSyntaxTree,
	nodeIndex int,
) bool {
	_, ok := kotlinTreePropertySiblingAccessorEnd(tree, nodeIndex)
	return ok
}

func kotlinTreePropertySiblingAccessorEnd(
	tree *kotlinSyntaxTree,
	nodeIndex int,
) (int, bool) {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) ||
		tree.nodes[nodeIndex].kind != "property_declaration" {
		return 0, false
	}
	parentIndex := tree.nodes[nodeIndex].parent
	if !cValidSyntaxNodeIndex(tree, parentIndex) {
		return 0, false
	}
	children := tree.nodes[parentIndex].children
	propertyPosition := -1
	for position, childIndex := range children {
		if childIndex == nodeIndex {
			propertyPosition = position
			break
		}
	}
	if propertyPosition < 0 {
		return 0, false
	}

	end := tree.nodes[nodeIndex].endByte
	found := false
	for _, childIndex := range children[propertyPosition+1:] {
		if !cValidSyntaxNodeIndex(tree, childIndex) {
			break
		}
		child := tree.nodes[childIndex]
		if child.kind != "getter" && child.kind != "setter" {
			break
		}
		found = true
		end = max(end, child.endByte)
	}
	return end, found
}

func kotlinTreePropertyInInterface(
	tree *kotlinSyntaxTree,
	nodeIndex int,
) bool {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) ||
		tree.nodes[nodeIndex].kind != "property_declaration" {
		return false
	}
	bodyIndex := tree.nodes[nodeIndex].parent
	if !cValidSyntaxNodeIndex(tree, bodyIndex) ||
		tree.nodes[bodyIndex].kind != "class_body" {
		return false
	}
	declarationIndex := tree.nodes[bodyIndex].parent
	return cValidSyntaxNodeIndex(tree, declarationIndex) &&
		tree.nodes[declarationIndex].kind == "class_declaration" &&
		cDirectChildOfKind(tree, declarationIndex, "interface") >= 0
}

func kotlinTreeDeclarationScopeEnd(
	source string,
	tree *kotlinSyntaxTree,
	nodeIndex int,
) int {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return 0
	}
	end := tree.nodes[nodeIndex].endByte
	if tree.nodes[nodeIndex].kind == "property_declaration" {
		if accessorEnd, ok := kotlinTreePropertySiblingAccessorEnd(
			tree, nodeIndex,
		); ok {
			end = max(end, accessorEnd)
		}
	}
	if recoveredEnd, ok := kotlinTreeRecoveredDeclarationBodyEnd(
		source, tree, nodeIndex,
	); ok {
		end = max(end, recoveredEnd)
	}
	return end
}

func kotlinTreeRecoverableTruncatedDeclaration(
	source string,
	tree *kotlinSyntaxTree,
	nodeIndex int,
) bool {
	_, ok := kotlinTreeRecoveredDeclarationBodyEnd(source, tree, nodeIndex)
	return ok
}

func kotlinTreeRecoveredDeclarationBodyEnd(
	source string,
	tree *kotlinSyntaxTree,
	nodeIndex int,
) (int, bool) {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return 0, false
	}
	node := tree.nodes[nodeIndex]
	if node.kind != "class_declaration" ||
		cDirectChildOfKind(tree, nodeIndex, "class_body") >= 0 ||
		cDirectChildOfKind(tree, nodeIndex, "enum_class_body") >= 0 ||
		node.startByte < 0 || node.endByte <= node.startByte ||
		node.endByte > len(source) ||
		!strings.Contains(source[node.startByte:node.endByte], "@all:") {
		return 0, false
	}
	return kotlinTreeLexicalBodyEnd(source, node.startByte)
}

func kotlinTreeLexicalBodyEnd(source string, declarationStart int) (int, bool) {
	if declarationStart < 0 || declarationStart >= len(source) {
		return 0, false
	}
	parentheses, brackets, braces := 0, 0, 0
	bodyStarted := false
	end := 0
	walkKotlinLexically(source, kotlinLexicalSink{token: func(token kotlinToken) bool {
		if token.end <= declarationStart {
			return true
		}
		switch token.text {
		case "(":
			parentheses++
		case ")":
			if parentheses > 0 {
				parentheses--
			}
		case "[":
			brackets++
		case "]":
			if brackets > 0 {
				brackets--
			}
		case "{":
			if bodyStarted {
				braces++
			} else if parentheses == 0 && brackets == 0 {
				bodyStarted = true
				braces = 1
			}
		case "}":
			if bodyStarted {
				braces--
				if braces == 0 {
					end = token.end
					return false
				}
			}
		}
		return true
	}})
	return end, end > declarationStart
}

func kotlinTreeOutlineProperty(tree *kotlinSyntaxTree, nodeIndex int) bool {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return false
	}
	parent := tree.nodes[nodeIndex].parent
	if !cValidSyntaxNodeIndex(tree, parent) {
		return false
	}
	switch tree.nodes[parent].kind {
	case "source_file", "class_body", "enum_class_body":
		return true
	default:
		return false
	}
}

func kotlinTreeOutlineTypeAlias(tree *kotlinSyntaxTree, nodeIndex int) bool {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return false
	}
	parent := tree.nodes[nodeIndex].parent
	if !cValidSyntaxNodeIndex(tree, parent) {
		return false
	}
	switch tree.nodes[parent].kind {
	case "source_file", "class_body", "enum_class_body":
		return true
	default:
		return false
	}
}

func kotlinTreeConstructorProperty(tree *kotlinSyntaxTree, nodeIndex int) bool {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) ||
		cDirectChildOfKind(tree, nodeIndex, "binding_pattern_kind") < 0 {
		return false
	}
	constructor := tree.nodes[nodeIndex].parent
	if !cValidSyntaxNodeIndex(tree, constructor) ||
		tree.nodes[constructor].kind != "primary_constructor" {
		return false
	}
	declaration := tree.nodes[constructor].parent
	return cValidSyntaxNodeIndex(tree, declaration) &&
		tree.nodes[declaration].kind == "class_declaration"
}

func kotlinTreePropertyNameNodes(
	tree *kotlinSyntaxTree,
	nodeIndex int,
) []int {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return nil
	}
	result := make([]int, 0, 1)
	for _, childIndex := range tree.nodes[nodeIndex].children {
		if !cValidSyntaxNodeIndex(tree, childIndex) {
			return nil
		}
		switch tree.nodes[childIndex].kind {
		case "variable_declaration":
			if name := cDirectChildOfKind(tree, childIndex, "simple_identifier"); name >= 0 {
				result = append(result, name)
			}
		case "multi_variable_declaration":
			for _, variableIndex := range tree.nodes[childIndex].children {
				if !cValidSyntaxNodeIndex(tree, variableIndex) ||
					tree.nodes[variableIndex].kind != "variable_declaration" {
					continue
				}
				if name := cDirectChildOfKind(
					tree, variableIndex, "simple_identifier",
				); name >= 0 {
					result = append(result, name)
				}
			}
		}
	}
	return result
}

func kotlinTreeIdentifierSourceName(
	source string,
	tree *kotlinSyntaxTree,
	nodeIndex int,
) (symbol string, nameStart int, ok bool) {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return "", 0, false
	}
	node := tree.nodes[nodeIndex]
	if node.startByte < 0 || node.endByte <= node.startByte ||
		node.endByte > len(source) {
		return "", 0, false
	}
	raw := source[node.startByte:node.endByte]
	text, end, relativeNameStart, _, valid := kotlinIdentifierAt(raw, 0)
	if !valid || text == "" || end != len(raw) {
		return "", 0, false
	}
	return text, node.startByte + relativeNameStart, true
}

func kotlinTreeExactSourceToken(
	source string,
	tree *kotlinSyntaxTree,
	nodeIndex int,
	want string,
) bool {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return false
	}
	node := tree.nodes[nodeIndex]
	return node.startByte >= 0 && node.endByte > node.startByte &&
		node.endByte <= len(source) && source[node.startByte:node.endByte] == want
}

func kotlinTreeScopes(
	source string,
	lineCount int,
	tree *kotlinSyntaxTree,
) []cLineScope {
	if lineCount < 1 || !validateKotlinSyntaxTree(tree, len(source)) {
		return nil
	}
	errorContext := kotlinSyntaxErrorContexts(tree)
	recoveryDescendant := kotlinSyntaxErrorDescendants(tree)
	attachedStarts := kotlinSyntaxAttachedStarts(source, tree)
	positions := cSourcePositions{
		source:     source,
		lineStarts: cTreeLineStarts(source),
	}
	scopes := make([]cLineScope, 0)
	for nodeIndex := range tree.nodes {
		if kotlinSyntaxNodeInError(errorContext, nodeIndex) {
			continue
		}
		if kotlinTreeDeclarationKind(tree.nodes[nodeIndex].kind) &&
			kotlinTreeDeclarationSignatureHasRecovery(
				tree, nodeIndex, recoveryDescendant,
			) && !kotlinTreeRecoverableTruncatedDeclaration(
			source, tree, nodeIndex,
		) {
			continue
		}
		scopeIndex, attachDeclaration, ok := kotlinTreeScopeDescriptor(tree, nodeIndex)
		if !ok || !cValidSyntaxNodeIndex(tree, scopeIndex) {
			continue
		}
		scope := tree.nodes[scopeIndex]
		start := scope.startByte
		if attachDeclaration {
			start = kotlinSyntaxAttachedScopeStart(
				tree, scopeIndex, attachedStarts,
			)
		}
		end := kotlinTreeDeclarationScopeEnd(source, tree, scopeIndex)
		if end < scope.endByte {
			end = scope.endByte
		}
		startLine, endLine := positions.lineSpan(start, end)
		scopes = append(scopes, cLineScope{start: startLine, end: endLine})
	}
	return cNormalizeTreeLineScopes(scopes, lineCount)
}

func kotlinTreeScopeDescriptor(
	tree *kotlinSyntaxTree,
	nodeIndex int,
) (scopeIndex int, attachDeclaration, ok bool) {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return -1, false, false
	}
	node := tree.nodes[nodeIndex]
	switch node.kind {
	case "class_declaration", "object_declaration", "companion_object",
		"function_declaration", "enum_entry",
		"secondary_constructor":
		return nodeIndex, true, kotlinTreeDeclarationOwnsScope(tree, nodeIndex)
	case "property_declaration":
		return nodeIndex, true, kotlinTreeOutlineProperty(tree, nodeIndex) &&
			kotlinTreeDeclarationOwnsScope(tree, nodeIndex)

	case "anonymous_initializer", "getter", "setter", "lambda_literal",
		"object_literal", "if_expression", "when_expression", "when_entry",
		"for_statement", "while_statement", "do_while_statement",
		"try_expression", "catch_block", "finally_block":
		return nodeIndex, false, true

	case "class_body", "enum_class_body", "function_body", "statements",
		"control_structure_body":
		if kotlinTreeBodyOwnedByParent(tree, nodeIndex) {
			return -1, false, false
		}
		return nodeIndex, false, true

	default:
		return -1, false, false
	}
}

func kotlinTreeBodyOwnedByParent(tree *kotlinSyntaxTree, nodeIndex int) bool {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return false
	}
	parent := tree.nodes[nodeIndex].parent
	if !cValidSyntaxNodeIndex(tree, parent) {
		return false
	}
	parentKind := tree.nodes[parent].kind
	switch parentKind {
	case "class_declaration", "object_declaration", "companion_object",
		"function_declaration", "property_declaration", "enum_entry",
		"secondary_constructor", "anonymous_initializer", "getter", "setter",
		"lambda_literal", "object_literal", "catch_block", "finally_block":
		return true
	case "control_structure_body":
		return tree.nodes[nodeIndex].kind == "statements"
	case "when_entry":
		return tree.nodes[nodeIndex].kind == "control_structure_body"
	default:
		return false
	}
}

func kotlinTreeImports(
	source string,
	lineCount int,
	tree *kotlinSyntaxTree,
) []cLineSpan {
	if lineCount < 1 || !validateKotlinSyntaxTree(tree, len(source)) {
		return nil
	}
	errorContext := kotlinSyntaxErrorContexts(tree)
	recoveryDescendant := kotlinSyntaxErrorDescendants(tree)
	positions := cSourcePositions{
		source:     source,
		lineStarts: cTreeLineStarts(source),
	}
	imports := make([]cLineSpan, 0)
	for nodeIndex, node := range tree.nodes {
		if node.kind != "import_header" ||
			kotlinSyntaxNodeInError(errorContext, nodeIndex) ||
			kotlinTreeDeclarationSignatureHasRecovery(
				tree, nodeIndex, recoveryDescendant,
			) {
			continue
		}
		start, end := positions.lineSpan(node.startByte, node.endByte)
		imports = append(imports, cLineSpan{start: start, end: end})
	}
	return cNormalizeTreeLineSpans(imports, lineCount)
}

func kotlinSyntaxErrorContexts(tree *kotlinSyntaxTree) []bool {
	if tree == nil || len(tree.nodes) == 0 {
		return nil
	}
	contexts := make([]bool, len(tree.nodes))
	for nodeIndex, node := range tree.nodes {
		contexts[nodeIndex] = kotlinSyntaxRecoveryNode(tree, nodeIndex)
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

func kotlinSyntaxErrorDescendants(tree *kotlinSyntaxTree) []bool {
	if tree == nil || len(tree.nodes) == 0 {
		return nil
	}
	descendants := make([]bool, len(tree.nodes))
	for nodeIndex := len(tree.nodes) - 1; nodeIndex >= 0; nodeIndex-- {
		node := tree.nodes[nodeIndex]
		if kotlinSyntaxRecoveryNode(tree, nodeIndex) {
			descendants[nodeIndex] = true
		}
		if descendants[nodeIndex] && node.parent >= 0 &&
			node.parent < nodeIndex && node.parent < len(tree.nodes) {
			descendants[node.parent] = true
		}
	}
	return descendants
}

func kotlinSyntaxErrorSpans(
	tree *kotlinSyntaxTree,
	sourceLength int,
) []cByteSpan {
	if !validateKotlinSyntaxTree(tree, sourceLength) {
		return nil
	}
	spans := make([]cByteSpan, 0)
	for nodeIndex, node := range tree.nodes {
		if !kotlinSyntaxRecoveryNode(tree, nodeIndex) {
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
			spans = append(spans, cByteSpan{start: start, end: end})
		}
	}
	return cNormalizeSyntaxByteSpans(spans)
}

func kotlinSyntaxRecoveryNode(tree *kotlinSyntaxTree, nodeIndex int) bool {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return false
	}
	node := tree.nodes[nodeIndex]
	if node.kind == "ERROR" {
		return true
	}
	if nodeIndex == tree.root || node.startByte != node.endByte {
		return false
	}
	// Kotlin's external scanner may represent automatic semicolon insertion
	// with a zero-width hidden token. It is syntax, not a missing parser node.
	switch node.kind {
	case ";", "_automatic_semicolon", "automatic_semicolon":
		return false
	default:
		return true
	}
}

func kotlinSyntaxNodeInError(errorContext []bool, nodeIndex int) bool {
	return nodeIndex < 0 || nodeIndex >= len(errorContext) || errorContext[nodeIndex]
}

func kotlinSyntaxNodeFlagged(flags []bool, nodeIndex int) bool {
	return nodeIndex < 0 || nodeIndex >= len(flags) || flags[nodeIndex]
}

func kotlinSyntaxAttachedStarts(
	source string,
	tree *kotlinSyntaxTree,
) []int {
	if !validateKotlinSyntaxTree(tree, len(source)) {
		return nil
	}
	starts := make([]int, len(tree.nodes))
	for nodeIndex, node := range tree.nodes {
		starts[nodeIndex] = node.startByte
	}
	for _, parent := range tree.nodes {
		pendingStart, previousEnd := -1, -1
		for _, childIndex := range parent.children {
			if !cValidSyntaxNodeIndex(tree, childIndex) {
				pendingStart, previousEnd = -1, -1
				continue
			}
			child := tree.nodes[childIndex]
			adjacent := previousEnd >= 0 && previousEnd <= child.startByte &&
				kotlinAttachmentGap(source, previousEnd, child.startByte)
			if previousEnd >= 0 && !adjacent {
				pendingStart = -1
			}
			switch {
			case kotlinKDocComment(source, child):
				if pendingStart < 0 {
					pendingStart = child.startByte
				}
			case child.kind == "line_comment" || child.kind == "multiline_comment":
				pendingStart = -1
			case kotlinSyntaxAttachmentBridge(child.kind):
				if pendingStart >= 0 && adjacent {
					starts[childIndex] = pendingStart
				}
			default:
				if pendingStart >= 0 && adjacent {
					starts[childIndex] = pendingStart
				}
				pendingStart = -1
			}
			previousEnd = child.endByte
		}
	}

	// Unsupported syntax can make tree-sitter wrap the opening brace and an
	// immediately following KDoc in a recovery node. The comment then has no
	// concrete comment node to participate in the sibling walk above. Recover
	// only the same strict, whitespace-adjacent attachment from the lexical
	// comment stream; annotations included in a declaration naturally remain
	// part of that declaration's start offset.
	targets := make([]int, 0)
	for nodeIndex, node := range tree.nodes {
		if kotlinSyntaxDocumentationTarget(node.kind) {
			targets = append(targets, nodeIndex)
		}
	}
	sort.Slice(targets, func(left, right int) bool {
		leftNode, rightNode := tree.nodes[targets[left]], tree.nodes[targets[right]]
		if leftNode.startByte != rightNode.startByte {
			return leftNode.startByte < rightNode.startByte
		}
		return targets[left] < targets[right]
	})
	walkKotlinLexically(source, kotlinLexicalSink{
		comment: func(span cByteSpan) bool {
			if !kotlinKDocSpan(source, span.start, span.end) {
				return true
			}
			position := sort.Search(len(targets), func(position int) bool {
				return tree.nodes[targets[position]].startByte >= span.end
			})
			if position >= len(targets) {
				return true
			}
			targetIndex := targets[position]
			target := tree.nodes[targetIndex]
			if kotlinAttachmentGap(source, span.end, target.startByte) {
				starts[targetIndex] = min(starts[targetIndex], span.start)
			}
			return true
		},
	})
	return starts
}

func kotlinSyntaxDocumentationTarget(kind string) bool {
	switch kind {
	case "class_declaration", "object_declaration", "companion_object",
		"function_declaration", "property_declaration", "type_alias",
		"enum_entry", "secondary_constructor":
		return true
	default:
		return false
	}
}

func kotlinSyntaxAttachmentBridge(kind string) bool {
	return kind == "annotation" || kind == "annotation_set" ||
		kind == "modifiers" || strings.HasSuffix(kind, "_modifier")
}

func kotlinSyntaxAttachedScopeStart(
	tree *kotlinSyntaxTree,
	nodeIndex int,
	starts []int,
) int {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return 0
	}
	start := tree.nodes[nodeIndex].startByte
	if nodeIndex < len(starts) && starts[nodeIndex] >= 0 &&
		starts[nodeIndex] <= tree.nodes[nodeIndex].startByte {
		start = min(start, starts[nodeIndex])
	}
	return start
}

func kotlinKDocComment(source string, node kotlinSyntaxNode) bool {
	return node.kind == "multiline_comment" &&
		kotlinKDocSpan(source, node.startByte, node.endByte)
}

func kotlinKDocSpan(source string, start, end int) bool {
	if start < 0 || end <= start || end > len(source) {
		return false
	}
	comment := source[start:end]
	if !strings.HasPrefix(comment, "/**") {
		return false
	}
	lineStart := strings.LastIndexAny(source[:start], "\r\n") + 1
	if lineStart == 0 {
		prefix := strings.TrimPrefix(source[:start], "\uFEFF")
		return strings.TrimSpace(prefix) == ""
	}
	return strings.TrimSpace(source[lineStart:start]) == ""
}

func kotlinAttachmentGap(source string, start, end int) bool {
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
