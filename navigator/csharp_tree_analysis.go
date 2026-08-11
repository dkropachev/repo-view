package navigator

import (
	"sort"
	"strings"
)

type csharpSyntaxTree = treeSitterSyntaxTree

type csharpDefinitionIdentity struct {
	symbol       string
	line, column int
}

func csharpTreeDefinitions(
	source string,
	lineCount int,
	tree *csharpSyntaxTree,
) []sourceDefinition {
	if lineCount < 1 || !validateTreeSitterSyntaxTree(tree, len(source)) {
		return nil
	}
	errorContext := cSyntaxErrorContexts(tree)
	errorDescendant := cSyntaxErrorDescendants(tree)
	attached := cSyntaxAttachedStarts(source, tree)
	positions := cSourcePositions{source: source, lineStarts: csharpLineStarts(source)}
	definitions := make([]sourceDefinition, 0)

	appendDefinition := func(
		nameIndex, scopeIndex int,
		symbol string,
		nameStart int,
		ownsScope bool,
	) {
		if !cValidSyntaxNodeIndex(tree, nameIndex) ||
			!cValidSyntaxNodeIndex(tree, scopeIndex) || symbol == "" ||
			nameStart < 0 || nameStart >= len(source) {
			return
		}
		line, column := positions.lineColumn(nameStart)
		scopeStart, scopeEnd := line, line
		ownedEndLine, ownedEndColumn := 0, 0
		if ownsScope {
			scope := tree.nodes[scopeIndex]
			start := scope.startByte
			if scopeIndex < len(attached) {
				start = min(start, attached[scopeIndex])
			}
			scopeStart, scopeEnd = positions.lineSpan(start, scope.endByte)
			ownedEndLine, ownedEndColumn = positions.lineColumn(scope.endByte)
		}
		if !ownsScope || ownedEndLine != scopeEnd {
			ownedEndColumn = 0
		}
		definitions = append(definitions, sourceDefinition{
			symbol: symbol, line: line, column: column,
			scopeStart: scopeStart, scopeEnd: scopeEnd,
			ownedEndColumn: ownedEndColumn, ownsScope: ownsScope,
		})
	}

	for nodeIndex, node := range tree.nodes {
		if cSyntaxNodeInError(errorContext, nodeIndex) ||
			cSyntaxHasDirectRecovery(tree, nodeIndex) {
			continue
		}
		if csharpTreeDeclarationKind(node.kind) &&
			csharpTreeDeclarationSignatureHasRecovery(tree, nodeIndex, errorDescendant) {
			continue
		}
		switch node.kind {
		case "class_declaration", "struct_declaration", "interface_declaration",
			"enum_declaration", "record_declaration", "delegate_declaration",
			"method_declaration", "local_function_statement", "constructor_declaration",
			"event_declaration":
			// A user-defined return/property/event type can itself be a direct
			// identifier before the declaration's field(name). The generated
			// grammar always places the declared name last among direct
			// identifier children; nested base types, constraints, parameters,
			// and bodies do not participate here.
			nameIndex := csharpTreeLastDirectChildOfKind(tree, nodeIndex, "identifier")
			if !csharpTreeIdentifierNode(source, tree, nameIndex) {
				continue
			}
			name := tree.nodes[nameIndex]
			ownsScope := csharpTreeDeclarationOwnsScope(tree, nodeIndex)
			appendDefinition(
				nameIndex, nodeIndex, source[name.startByte:name.endByte],
				name.startByte, ownsScope,
			)

		case "property_declaration":
			nameIndex := csharpTreeLastDirectChildOfKindBefore(
				tree, nodeIndex, "identifier",
				"accessor_list", "arrow_expression_clause", "=",
			)
			if !csharpTreeIdentifierNode(source, tree, nameIndex) {
				continue
			}
			name := tree.nodes[nameIndex]
			appendDefinition(
				nameIndex, nodeIndex, source[name.startByte:name.endByte],
				name.startByte, csharpTreeDeclarationOwnsScope(tree, nodeIndex),
			)

		case "enum_member_declaration":
			nameIndex := csharpTreeLastDirectChildOfKindBefore(
				tree, nodeIndex, "identifier", "=",
			)
			if !csharpTreeIdentifierNode(source, tree, nameIndex) {
				continue
			}
			name := tree.nodes[nameIndex]
			appendDefinition(
				nameIndex, nodeIndex, source[name.startByte:name.endByte],
				name.startByte, false,
			)

		case "namespace_declaration", "file_scoped_namespace_declaration":
			nameIndex := cDirectChildOfKinds(
				tree, nodeIndex,
				"identifier", "qualified_name", "alias_qualified_name", "generic_name",
			)
			symbol, start, ok := csharpTreeQualifiedName(source, tree, nameIndex)
			if !ok {
				continue
			}
			appendDefinition(
				nameIndex, nodeIndex, symbol, start,
				node.kind == "file_scoped_namespace_declaration" ||
					csharpTreeDeclarationOwnsScope(tree, nodeIndex),
			)
			if node.kind == "file_scoped_namespace_declaration" && len(definitions) > 0 {
				definition := &definitions[len(definitions)-1]
				definition.scopeEnd = lineCount
				// A file-scoped namespace owns through EOF, not merely through
				// its declaration semicolon. Retarget the exact final-line
				// boundary after widening the tree node's scope.
				endLine, endColumn := positions.lineColumn(len(source))
				definition.ownedEndColumn = 0
				if endLine == lineCount {
					definition.ownedEndColumn = endColumn
				}
			}

		case "destructor_declaration":
			nameIndex := cDirectChildOfKind(tree, nodeIndex, "identifier")
			if !csharpTreeIdentifierNode(source, tree, nameIndex) {
				continue
			}
			name := tree.nodes[nameIndex]
			start := name.startByte
			if tilde := cDirectChildOfKind(tree, nodeIndex, "~"); cValidSyntaxNodeIndex(tree, tilde) {
				start = tree.nodes[tilde].startByte
			}
			appendDefinition(
				nameIndex, nodeIndex, "~"+source[name.startByte:name.endByte], start,
				csharpTreeDeclarationOwnsScope(tree, nodeIndex),
			)

		case "operator_declaration":
			symbol, start, anchor, ok := csharpTreeOperatorSymbol(tree, nodeIndex)
			if ok {
				appendDefinition(
					anchor, nodeIndex, symbol, start,
					csharpTreeDeclarationOwnsScope(tree, nodeIndex),
				)
			}

		case "conversion_operator_declaration":
			symbol, start, anchor, ok := csharpTreeConversionSymbol(source, tree, nodeIndex)
			if ok {
				appendDefinition(
					anchor, nodeIndex, symbol, start,
					csharpTreeDeclarationOwnsScope(tree, nodeIndex),
				)
			}

		case "indexer_declaration":
			thisIndex := cDirectChildOfKind(tree, nodeIndex, "this")
			if cValidSyntaxNodeIndex(tree, thisIndex) {
				thisNode := tree.nodes[thisIndex]
				appendDefinition(
					thisIndex, nodeIndex, "this", thisNode.startByte,
					csharpTreeDeclarationOwnsScope(tree, nodeIndex),
				)
			}

		case "field_declaration", "event_field_declaration":
			variableDeclaration := cDirectChildOfKind(
				tree, nodeIndex, "variable_declaration",
			)
			if !cValidSyntaxNodeIndex(tree, variableDeclaration) {
				continue
			}
			for _, declarator := range tree.nodes[variableDeclaration].children {
				if !cValidSyntaxNodeIndex(tree, declarator) ||
					tree.nodes[declarator].kind != "variable_declarator" {
					continue
				}
				nameIndex := cDirectChildOfKind(tree, declarator, "identifier")
				if !csharpTreeIdentifierNode(source, tree, nameIndex) {
					continue
				}
				name := tree.nodes[nameIndex]
				appendDefinition(
					nameIndex, declarator, source[name.startByte:name.endByte],
					name.startByte, false,
				)
			}

		case "using_directive":
			// Only an alias identifier is a direct child; namespace/type names
			// live under the directive's type child.
			nameIndex := cDirectChildOfKind(tree, nodeIndex, "identifier")
			if !csharpTreeIdentifierNode(source, tree, nameIndex) ||
				cDirectChildOfKind(tree, nodeIndex, "=") < 0 {
				continue
			}
			name := tree.nodes[nameIndex]
			appendDefinition(
				nameIndex, nodeIndex, source[name.startByte:name.endByte],
				name.startByte, false,
			)
		case "extern_alias_directive":
			nameIndex := cDirectChildOfKind(tree, nodeIndex, "identifier")
			if !csharpTreeIdentifierNode(source, tree, nameIndex) {
				continue
			}
			name := tree.nodes[nameIndex]
			appendDefinition(
				nameIndex, nodeIndex, source[name.startByte:name.endByte],
				name.startByte, false,
			)

		case "parameter":
			if !csharpTreeRecordParameter(tree, nodeIndex) {
				continue
			}
			// Generic/custom parameter types can be direct identifiers too. A
			// default expression can also be a direct identifier, so stop at '='.
			nameIndex := csharpTreeLastDirectChildOfKindBefore(
				tree, nodeIndex, "identifier", "=",
			)
			if !csharpTreeIdentifierNode(source, tree, nameIndex) {
				continue
			}
			name := tree.nodes[nameIndex]
			appendDefinition(
				nameIndex, nodeIndex, source[name.startByte:name.endByte],
				name.startByte, false,
			)

		case "parameter_list":
			recordIndex := node.parent
			if !cValidSyntaxNodeIndex(tree, recordIndex) ||
				tree.nodes[recordIndex].kind != "record_declaration" ||
				csharpTreeDeclarationSignatureHasRecovery(
					tree, recordIndex, errorDescendant,
				) {
				continue
			}
			for _, nameIndex := range csharpTreeRecordParameterArrayNames(tree, nodeIndex) {
				if !csharpTreeIdentifierNode(source, tree, nameIndex) {
					continue
				}
				name := tree.nodes[nameIndex]
				appendDefinition(
					nameIndex, nodeIndex, source[name.startByte:name.endByte],
					name.startByte, false,
				)
			}
		}
	}
	return csharpSortUniqueDefinitions(definitions, lineCount)
}

func csharpTreeDeclarationKind(kind string) bool {
	switch kind {
	case "class_declaration", "struct_declaration", "interface_declaration",
		"enum_declaration", "record_declaration", "delegate_declaration",
		"method_declaration", "local_function_statement", "constructor_declaration",
		"destructor_declaration", "property_declaration", "event_declaration",
		"enum_member_declaration", "operator_declaration",
		"conversion_operator_declaration", "indexer_declaration", "field_declaration",
		"event_field_declaration", "extension_declaration", "using_directive",
		"extern_alias_directive", "parameter",
		"namespace_declaration", "file_scoped_namespace_declaration":
		return true
	default:
		return false
	}
}

func csharpTreeDeclarationSignatureHasRecovery(
	tree *csharpSyntaxTree,
	nodeIndex int,
	errorDescendant []bool,
) bool {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return true
	}
	for _, childIndex := range tree.nodes[nodeIndex].children {
		if !cValidSyntaxNodeIndex(tree, childIndex) {
			return true
		}
		switch tree.nodes[childIndex].kind {
		case "declaration_list", "enum_member_declaration_list", "block",
			"arrow_expression_clause", "accessor_list", "extension_body":
			continue
		}
		if cSyntaxNodeFlagged(errorDescendant, childIndex) {
			return true
		}
	}
	return false
}

func csharpTreeDeclarationOwnsScope(tree *csharpSyntaxTree, nodeIndex int) bool {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return false
	}
	// A positional record declares synthesized properties even when its body is
	// only a semicolon. Treat that declaration line as the record's owning
	// scope; parameter lists on methods and delegates remain non-owning.
	if tree.nodes[nodeIndex].kind == "record_declaration" &&
		cDirectChildOfKind(tree, nodeIndex, "parameter_list") >= 0 {
		return true
	}
	for _, childIndex := range tree.nodes[nodeIndex].children {
		if !cValidSyntaxNodeIndex(tree, childIndex) {
			return false
		}
		switch tree.nodes[childIndex].kind {
		case "declaration_list", "enum_member_declaration_list", "block",
			"arrow_expression_clause", "accessor_list", "extension_body":
			return true
		}
	}
	return false
}

func csharpTreeIdentifierNode(
	source string,
	tree *csharpSyntaxTree,
	nodeIndex int,
) bool {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return false
	}
	node := tree.nodes[nodeIndex]
	return node.startByte >= 0 && node.endByte > node.startByte &&
		node.endByte <= len(source) &&
		csharpSourceIdentifier(source[node.startByte:node.endByte])
}

func csharpTreeLastDirectChildOfKind(
	tree *csharpSyntaxTree,
	nodeIndex int,
	kind string,
) int {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return -1
	}
	children := tree.nodes[nodeIndex].children
	for offset := len(children) - 1; offset >= 0; offset-- {
		childIndex := children[offset]
		if !cValidSyntaxNodeIndex(tree, childIndex) {
			return -1
		}
		if tree.nodes[childIndex].kind == kind {
			return childIndex
		}
	}
	return -1
}

func csharpTreeLastDirectChildOfKindBefore(
	tree *csharpSyntaxTree,
	nodeIndex int,
	kind string,
	boundaries ...string,
) int {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return -1
	}
	result := -1
	for _, childIndex := range tree.nodes[nodeIndex].children {
		if !cValidSyntaxNodeIndex(tree, childIndex) {
			return -1
		}
		childKind := tree.nodes[childIndex].kind
		for _, boundary := range boundaries {
			if childKind == boundary {
				return result
			}
		}
		if childKind == kind {
			result = childIndex
		}
	}
	return result
}

func csharpTreeQualifiedName(
	source string,
	tree *csharpSyntaxTree,
	nodeIndex int,
) (string, int, bool) {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return "", 0, false
	}
	node := tree.nodes[nodeIndex]
	if node.startByte < 0 || node.endByte <= node.startByte || node.endByte > len(source) {
		return "", 0, false
	}
	parts := make([]string, 0)
	start := -1
	walkCSharpLexically(source[node.startByte:node.endByte], csharpLexicalSink{
		token: func(token csharpToken) bool {
			if token.kind == csharpTokenIdentifier || token.text == "." || token.text == "::" {
				if start < 0 {
					start = node.startByte + token.start
				}
				parts = append(parts, token.text)
			}
			return true
		},
	})
	if start < 0 || len(parts) == 0 {
		return "", 0, false
	}
	symbol := strings.Join(parts, "")
	return symbol, start, csharpQualifiedSourceName(symbol)
}

func csharpQualifiedSourceName(symbol string) bool {
	if symbol == "" {
		return false
	}
	for _, part := range strings.FieldsFunc(symbol, func(r rune) bool { return r == '.' }) {
		if !csharpSourceIdentifier(strings.TrimSuffix(strings.TrimSuffix(part, ":"), ":")) {
			return false
		}
	}
	return true
}

func csharpTreeOperatorSymbol(
	tree *csharpSyntaxTree,
	nodeIndex int,
) (string, int, int, bool) {
	operatorIndex := cDirectChildOfKind(tree, nodeIndex, "operator")
	if !cValidSyntaxNodeIndex(tree, operatorIndex) {
		return "", 0, -1, false
	}
	operatorNode := tree.nodes[operatorIndex]
	operatorText := ""
	checked := false
	for _, childIndex := range tree.nodes[nodeIndex].children {
		if !cValidSyntaxNodeIndex(tree, childIndex) {
			return "", 0, -1, false
		}
		child := tree.nodes[childIndex]
		if child.startByte <= operatorNode.startByte {
			continue
		}
		if child.kind == "checked" {
			checked = true
			continue
		}
		if csharpOperatorPunctuation(child.kind) {
			operatorText = child.kind
			break
		}
	}
	if operatorText == "" {
		return "", 0, -1, false
	}
	symbol := "operator"
	if checked {
		symbol += " checked"
	}
	return symbol + operatorText, operatorNode.startByte, operatorIndex, true
}

func csharpOperatorPunctuation(value string) bool {
	switch value {
	case "+", "-", "*", "/", "%", "&", "|", "^", "!", "~", "++", "--",
		"true", "false", "==", "!=", "<", ">", "<=", ">=", "<<", ">>", ">>>",
		"+=", "-=", "*=", "/=", "%=", "&=", "|=", "^=", "<<=", ">>=", ">>>=":
		return true
	default:
		return false
	}
}

func csharpTreeConversionSymbol(
	source string,
	tree *csharpSyntaxTree,
	nodeIndex int,
) (string, int, int, bool) {
	operatorIndex := cDirectChildOfKind(tree, nodeIndex, "operator")
	if !cValidSyntaxNodeIndex(tree, operatorIndex) {
		return "", 0, -1, false
	}
	operatorNode := tree.nodes[operatorIndex]
	prefix := ""
	prefixStart := -1
	checked := false
	for _, childIndex := range tree.nodes[nodeIndex].children {
		if !cValidSyntaxNodeIndex(tree, childIndex) {
			return "", 0, -1, false
		}
		child := tree.nodes[childIndex]
		switch child.kind {
		case "implicit", "explicit":
			prefix = child.kind
			prefixStart = child.startByte
		case "checked":
			checked = true
		}
	}
	typeIndex := -1
	for _, childIndex := range tree.nodes[nodeIndex].children {
		if !cValidSyntaxNodeIndex(tree, childIndex) {
			return "", 0, -1, false
		}
		child := tree.nodes[childIndex]
		if child.startByte > operatorNode.endByte && csharpTreeTypeNode(child.kind) {
			typeIndex = childIndex
			break
		}
	}
	if !cValidSyntaxNodeIndex(tree, typeIndex) {
		return "", 0, -1, false
	}
	typeNode := tree.nodes[typeIndex]
	typeText := csharpCompactSource(source[typeNode.startByte:typeNode.endByte])
	if typeText == "" {
		return "", 0, -1, false
	}
	if prefix == "" || prefixStart < 0 {
		return "", 0, -1, false
	}
	symbol := prefix + " operator "
	if checked {
		symbol += "checked "
	}
	symbol += typeText
	return symbol, prefixStart, operatorIndex, true
}

func csharpTreeTypeNode(kind string) bool {
	switch kind {
	case "type", "identifier", "predefined_type", "nullable_type", "array_type",
		"pointer_type", "tuple_type", "generic_name", "qualified_name", "alias_qualified_name":
		return true
	default:
		return false
	}
}

func csharpCompactSource(source string) string {
	parts := make([]string, 0)
	walkCSharpLexically(source, csharpLexicalSink{token: func(token csharpToken) bool {
		parts = append(parts, token.text)
		return true
	}})
	return strings.Join(parts, "")
}

func csharpTreeRecordParameter(tree *csharpSyntaxTree, nodeIndex int) bool {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) || tree.nodes[nodeIndex].kind != "parameter" {
		return false
	}
	parameters := tree.nodes[nodeIndex].parent
	if !cValidSyntaxNodeIndex(tree, parameters) || tree.nodes[parameters].kind != "parameter_list" {
		return false
	}
	record := tree.nodes[parameters].parent
	return cValidSyntaxNodeIndex(tree, record) &&
		tree.nodes[record].kind == "record_declaration"
}

func csharpTreeRecordParameterArrayNames(
	tree *csharpSyntaxTree,
	parameterList int,
) []int {
	if !cValidSyntaxNodeIndex(tree, parameterList) ||
		tree.nodes[parameterList].kind != "parameter_list" {
		return nil
	}
	names := make([]int, 0, 1)
	candidate := -1
	insideParameterArray := false
	flush := func() {
		if insideParameterArray && candidate >= 0 {
			names = append(names, candidate)
		}
		candidate = -1
		insideParameterArray = false
	}
	for _, childIndex := range tree.nodes[parameterList].children {
		if !cValidSyntaxNodeIndex(tree, childIndex) {
			return nil
		}
		switch tree.nodes[childIndex].kind {
		case "params":
			flush()
			insideParameterArray = true
		case ",", ")":
			flush()
		case "identifier":
			if insideParameterArray {
				candidate = childIndex
			}
		}
	}
	flush()
	return names
}

func csharpTreeScopes(
	source string,
	lineCount int,
	tree *csharpSyntaxTree,
) []cLineScope {
	if lineCount < 1 || !validateTreeSitterSyntaxTree(tree, len(source)) {
		return nil
	}
	errorContext := cSyntaxErrorContexts(tree)
	errorDescendant := cSyntaxErrorDescendants(tree)
	attached := cSyntaxAttachedStarts(source, tree)
	positions := cSourcePositions{source: source, lineStarts: csharpLineStarts(source)}
	scopes := make([]cLineScope, 0)
	for nodeIndex, node := range tree.nodes {
		if cSyntaxNodeInError(errorContext, nodeIndex) ||
			csharpTreeDeclarationKind(node.kind) &&
				csharpTreeDeclarationSignatureHasRecovery(tree, nodeIndex, errorDescendant) {
			continue
		}
		if node.kind == "block" && csharpTreeBlockOwnedByParent(tree, nodeIndex) {
			continue
		}
		attach := false
		switch node.kind {
		case "class_declaration", "struct_declaration", "interface_declaration",
			"enum_declaration", "record_declaration", "namespace_declaration",
			"file_scoped_namespace_declaration", "method_declaration",
			"local_function_statement", "constructor_declaration", "destructor_declaration",
			"operator_declaration", "conversion_operator_declaration", "property_declaration",
			"indexer_declaration", "event_declaration", "extension_declaration":
			if !csharpTreeDeclarationOwnsScope(tree, nodeIndex) &&
				node.kind != "file_scoped_namespace_declaration" {
				continue
			}
			attach = true
		case "block", "switch_statement", "switch_section", "accessor_declaration", "lambda_expression",
			"anonymous_method_expression", "if_statement", "for_statement",
			"foreach_statement", "while_statement", "do_statement", "using_statement",
			"lock_statement", "fixed_statement", "checked_statement", "unsafe_statement",
			"try_statement", "catch_clause", "finally_clause":
		default:
			continue
		}
		start := node.startByte
		if attach && nodeIndex < len(attached) {
			start = min(start, attached[nodeIndex])
		}
		end := node.endByte
		if node.kind == "file_scoped_namespace_declaration" {
			end = len(source)
		}
		startLine, endLine := positions.lineSpan(start, end)
		if startLine >= 1 && endLine >= startLine && endLine <= lineCount {
			scopes = append(scopes, cLineScope{start: startLine, end: endLine})
		}
	}
	return cNormalizeTreeLineScopes(scopes, lineCount)
}

func csharpTreeBlockOwnedByParent(tree *csharpSyntaxTree, nodeIndex int) bool {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) || tree.nodes[nodeIndex].kind != "block" {
		return false
	}
	parent := tree.nodes[nodeIndex].parent
	if !cValidSyntaxNodeIndex(tree, parent) {
		return false
	}
	switch tree.nodes[parent].kind {
	case "method_declaration", "local_function_statement", "constructor_declaration",
		"destructor_declaration", "operator_declaration", "conversion_operator_declaration",
		"accessor_declaration", "lambda_expression", "anonymous_method_expression",
		"if_statement", "for_statement", "foreach_statement", "while_statement",
		"do_statement", "using_statement", "lock_statement", "fixed_statement",
		"checked_statement", "unsafe_statement", "try_statement", "catch_clause",
		"finally_clause":
		return true
	default:
		return false
	}
}

func csharpTreeImports(
	source string,
	lineCount int,
	tree *csharpSyntaxTree,
) []cLineSpan {
	if lineCount < 1 || !validateTreeSitterSyntaxTree(tree, len(source)) {
		return nil
	}
	errorContext := cSyntaxErrorContexts(tree)
	errorDescendant := cSyntaxErrorDescendants(tree)
	positions := cSourcePositions{source: source, lineStarts: csharpLineStarts(source)}
	imports := make([]cLineSpan, 0)
	for nodeIndex, node := range tree.nodes {
		if cSyntaxNodeInError(errorContext, nodeIndex) ||
			csharpTreeDeclarationKind(node.kind) &&
				csharpTreeDeclarationSignatureHasRecovery(tree, nodeIndex, errorDescendant) {
			continue
		}
		switch node.kind {
		case "using_directive", "extern_alias_directive":
			start, end := positions.lineSpan(node.startByte, node.endByte)
			imports = append(imports, cLineSpan{start: start, end: end})
		}
	}
	return cNormalizeTreeLineSpans(imports, lineCount)
}

func csharpSortUniqueDefinitions(
	definitions []sourceDefinition,
	lineCount int,
) []sourceDefinition {
	normalized := definitions[:0]
	for _, definition := range definitions {
		definition = normalizeCDefinition(definition, lineCount)
		if definition.symbol == "" || definition.line < 1 || definition.column < 1 {
			continue
		}
		normalized = append(normalized, definition)
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
	unique := definitions[:0]
	for _, definition := range definitions {
		if len(unique) == 0 {
			unique = append(unique, definition)
			continue
		}
		last := &unique[len(unique)-1]
		if (csharpDefinitionIdentity{last.symbol, last.line, last.column}) !=
			(csharpDefinitionIdentity{definition.symbol, definition.line, definition.column}) {
			unique = append(unique, definition)
			continue
		}
		if definition.ownsScope && !last.ownsScope || definition.ownsScope == last.ownsScope &&
			definition.scopeEnd-definition.scopeStart > last.scopeEnd-last.scopeStart {
			*last = definition
		}
	}
	return unique
}
