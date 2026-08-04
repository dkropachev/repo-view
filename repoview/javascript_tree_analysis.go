package repoview

import (
	"sort"
	"strings"
	"unicode/utf8"
)

func javascriptTreeDefinitionsFromSyntax(
	source string,
	lineCount int,
	tree *javascriptSyntaxTree,
	excluded []javascriptByteSpan,
	attachedStarts []int,
	errorContext []bool,
) []sourceDefinition {
	if tree == nil {
		return nil
	}
	positions := javascriptSourcePositions{source: source, lineStarts: javascriptLineStarts(source)}
	scopedSubtree := javascriptScopedSubtrees(tree)
	earlierSiblingErrors := javascriptEarlierSiblingErrors(tree)
	definitions := make([]sourceDefinition, 0)
	for nodeIndex, node := range tree.nodes {
		if nodeIndex < len(errorContext) && errorContext[nodeIndex] &&
			!javascriptTypeScriptAnonymousDefaultSignatureDefinition(
				source, tree, nodeIndex,
			) {
			continue
		}
		nameNodes := make([]int, 0, 2)
		opaqueNameNode := -1
		ownsScope := false
		scopeIndex := javascriptDefinitionScopeNode(tree, nodeIndex)
		switch node.kind {
		case "function_declaration", "generator_function_declaration", "class_declaration",
			"abstract_class_declaration", "function_expression", "generator_function", "class":
			if nameIndex := javascriptDirectNameNode(tree, nodeIndex); nameIndex >= 0 {
				nameNodes = append(nameNodes, nameIndex)
			}
			ownsScope = true
		case "function_signature":
			if nameIndex := javascriptDirectNameNode(tree, nodeIndex); nameIndex >= 0 {
				nameNodes = append(nameNodes, nameIndex)
			}
			ownsScope = true
		case "interface_declaration", "type_alias_declaration", "enum_declaration":
			if nameIndex := javascriptDirectTypeNameNode(tree, nodeIndex); nameIndex >= 0 {
				nameNodes = append(nameNodes, nameIndex)
			}
			ownsScope = true
		case "internal_module", "module":
			nameNodes = append(nameNodes, javascriptTypeScriptModuleNameNodes(tree, nodeIndex)...)
			ownsScope = true
		case "method_definition", "method_signature", "abstract_method_signature":
			if nameIndex := javascriptDirectPropertyNameNode(tree, nodeIndex); nameIndex >= 0 {
				nameNodes = append(nameNodes, nameIndex)
			}
			ownsScope = true
		case "variable_declarator":
			if nodeIndex < len(earlierSiblingErrors) && earlierSiblingErrors[nodeIndex] ||
				!javascriptVariableDeclaratorHasDeclarationBoundary(tree, nodeIndex) {
				continue
			}
			if patternIndex := javascriptDeclaratorPatternNode(tree, nodeIndex); patternIndex >= 0 {
				nameNodes = append(nameNodes, javascriptBindingNameNodes(tree, patternIndex)...)
			}
			ownsScope = javascriptDefinitionHasScopedValue(tree, nodeIndex, scopedSubtree)
		case "for_in_statement":
			if patternIndex := javascriptForBindingPatternNode(tree, nodeIndex); patternIndex >= 0 {
				nameNodes = append(nameNodes, javascriptBindingNameNodes(tree, patternIndex)...)
			}
		case "field_definition":
			if nameIndex := javascriptDirectPropertyNameNode(tree, nodeIndex); nameIndex >= 0 {
				nameNodes = append(nameNodes, nameIndex)
			}
			ownsScope = javascriptDefinitionHasScopedValue(tree, nodeIndex, scopedSubtree)
		case "public_field_definition":
			if nameIndex := javascriptTypeScriptPublicFieldNameNode(
				source, tree, nodeIndex,
			); nameIndex >= 0 {
				nameNodes = append(nameNodes, nameIndex)
			}
			ownsScope = javascriptDefinitionHasScopedValue(tree, nodeIndex, scopedSubtree)
		case "property_signature":
			if nameIndex := javascriptDirectPropertyNameNode(tree, nodeIndex); nameIndex >= 0 {
				nameNodes = append(nameNodes, nameIndex)
			}
			ownsScope = javascriptDefinitionHasScopedValue(tree, nodeIndex, scopedSubtree)
		case "required_parameter", "optional_parameter":
			if javascriptTypeScriptParameterProperty(source, tree, nodeIndex) {
				if nameIndex := javascriptDirectNameNode(tree, nodeIndex); nameIndex >= 0 {
					nameNodes = append(nameNodes, nameIndex)
				}
			}
		case "enum_assignment":
			if nameIndex := javascriptDirectPropertyNameNode(tree, nodeIndex); nameIndex >= 0 {
				nameNodes = append(nameNodes, nameIndex)
			}
		case "property_identifier":
			if javascriptDirectChildOfKind(tree, nodeIndex, "enum_body") {
				nameNodes = append(nameNodes, nodeIndex)
			}
		case "string":
			if javascriptDirectChildOfKind(tree, nodeIndex, "enum_body") {
				if nameIndex := javascriptStaticStringFragmentNode(tree, nodeIndex); nameIndex >= 0 {
					nameNodes = append(nameNodes, nameIndex)
				}
			}
		case "pair":
			if javascriptDefinitionHasDirectScopedValue(tree, nodeIndex) {
				if nameIndex := javascriptDirectPropertyNameNode(tree, nodeIndex); nameIndex >= 0 {
					nameNodes = append(nameNodes, nameIndex)
				}
				ownsScope = true
			}
		case "assignment_expression":
			if nameIndex := javascriptTypeScriptUsingBindingNode(tree, nodeIndex); nameIndex >= 0 {
				nameNodes = append(nameNodes, nameIndex)
				ownsScope = javascriptDefinitionHasScopedValue(tree, nodeIndex, scopedSubtree)
			} else if javascriptDefinitionHasDirectScopedValue(tree, nodeIndex) {
				if nameIndex := javascriptCommonJSExportNameNode(source, tree, nodeIndex); nameIndex >= 0 {
					nameNodes = append(nameNodes, nameIndex)
					ownsScope = true
				}
			}
		case "export_statement":
			if nameIndex := javascriptTypeScriptNamespaceExportNameNode(tree, nodeIndex); nameIndex >= 0 {
				nameNodes = append(nameNodes, nameIndex)
				ownsScope = true
			}
		}
		if len(nameNodes) == 0 || scopeIndex < 0 || scopeIndex >= len(tree.nodes) {
			continue
		}
		scopeNode := tree.nodes[scopeIndex]
		scopeStartOffset := javascriptSyntaxAttachedStart(tree, scopeIndex, attachedStarts)
		scopeStart, scopeEnd := positions.lineSpan(scopeStartOffset, scopeNode.endByte)
		for _, nameIndex := range nameNodes {
			if nameIndex < 0 || nameIndex >= len(tree.nodes) {
				continue
			}
			nameNode := tree.nodes[nameIndex]
			if nameNode.kind == "string_fragment" {
				opaqueNameNode = nameIndex
			}
			if nameNode.startByte < 0 || nameNode.endByte > len(source) ||
				nameNode.startByte >= nameNode.endByte ||
				nameIndex != opaqueNameNode &&
					javascriptByteRangeExcluded(nameNode.startByte, nameNode.endByte, excluded) {
				continue
			}
			symbol := source[nameNode.startByte:nameNode.endByte]
			if !javascriptSourceName(symbol) {
				continue
			}
			line, column := positions.lineColumn(nameNode.startByte)
			definitionScopeStart, definitionScopeEnd := scopeStart, scopeEnd
			if !ownsScope {
				definitionScopeStart, definitionScopeEnd = line, line
			}
			definition := normalizeJavaScriptDefinition(sourceDefinition{
				symbol:     symbol,
				line:       line,
				column:     column,
				scopeStart: definitionScopeStart,
				scopeEnd:   definitionScopeEnd,
				ownsScope:  ownsScope,
			}, lineCount)
			if definition.symbol != "" {
				definitions = append(definitions, definition)
			}
		}
	}
	return sortUniqueJavaScriptDefinitions(definitions)
}

func javascriptVariableDeclaratorHasDeclarationBoundary(
	tree *javascriptSyntaxTree,
	nodeIndex int,
) bool {
	if tree == nil || nodeIndex < 0 || nodeIndex >= len(tree.nodes) {
		return false
	}
	parentIndex := tree.nodes[nodeIndex].parent
	if parentIndex < 0 || parentIndex >= len(tree.nodes) {
		return false
	}
	for _, childIndex := range tree.nodes[nodeIndex].children {
		if childIndex < 0 || childIndex >= len(tree.nodes) {
			return false
		}
		if tree.nodes[childIndex].kind == "type_annotation" &&
			!javascriptTypeScriptValidTypeAnnotation(tree, childIndex) {
			return false
		}
	}
	switch tree.nodes[parentIndex].kind {
	case "lexical_declaration", "variable_declaration", "using_declaration":
	default:
		return false
	}
	previousKind := ""
	for _, childIndex := range tree.nodes[parentIndex].children {
		if childIndex < 0 || childIndex >= len(tree.nodes) {
			return false
		}
		if childIndex == nodeIndex {
			switch previousKind {
			case "const", "let", "var", "using", ",":
				return true
			default:
				return false
			}
		}
		if tree.nodes[childIndex].kind != "comment" {
			previousKind = tree.nodes[childIndex].kind
		}
	}
	return false
}

func javascriptTypeScriptValidTypeAnnotation(
	tree *javascriptSyntaxTree,
	nodeIndex int,
) bool {
	if tree == nil || nodeIndex < 0 || nodeIndex >= len(tree.nodes) ||
		tree.nodes[nodeIndex].kind != "type_annotation" {
		return false
	}
	typeSeen := false
	for _, childIndex := range tree.nodes[nodeIndex].children {
		if childIndex < 0 || childIndex >= len(tree.nodes) {
			return false
		}
		switch tree.nodes[childIndex].kind {
		case ":", "comment":
		case "predefined_type", "type_identifier", "nested_type_identifier", "generic_type",
			"object_type", "tuple_type", "array_type", "readonly_type", "union_type",
			"intersection_type", "lookup_type", "index_type_query", "conditional_type",
			"infer_type", "literal_type", "template_literal_type", "function_type",
			"constructor_type", "parenthesized_type", "type_query", "this_type",
			"unique_type", "optional_type", "rest_type":
			if typeSeen {
				return false
			}
			typeSeen = true
		default:
			return false
		}
	}
	return typeSeen
}

func javascriptTypeScriptAnonymousDefaultSignatureDefinition(
	source string,
	tree *javascriptSyntaxTree,
	nodeIndex int,
) bool {
	if tree == nil || nodeIndex < 0 || nodeIndex >= len(tree.nodes) {
		return false
	}
	switch tree.nodes[nodeIndex].kind {
	case "property_signature", "method_signature", "abstract_method_signature":
	default:
		return false
	}
	errorIndex := -1
	for ancestor, depth := tree.nodes[nodeIndex].parent, 0; ancestor >= 0 &&
		depth < javascriptMaximumSyntaxUnwrapDepth; depth++ {
		if ancestor >= len(tree.nodes) {
			return false
		}
		switch tree.nodes[ancestor].kind {
		case "ERROR":
			errorIndex = ancestor
			ancestor = -1
		case "statement_block", "class_body", "interface_body":
			return false
		default:
			ancestor = tree.nodes[ancestor].parent
		}
	}
	if errorIndex < 0 {
		return false
	}
	errorNode := tree.nodes[errorIndex]
	if errorNode.parent < 0 || errorNode.parent >= len(tree.nodes) ||
		tree.nodes[errorNode.parent].kind != "expression_statement" ||
		errorNode.startByte < 0 || errorNode.endByte > len(source) {
		return false
	}
	defaultSeen, functionSeen, parametersSeen, returnTypeSeen := false, false, false, false
	for _, childIndex := range errorNode.children {
		if childIndex < 0 || childIndex >= len(tree.nodes) {
			return false
		}
		switch tree.nodes[childIndex].kind {
		case "comment":
		case "default":
			defaultSeen = true
		case "function":
			functionSeen = true
		case "formal_parameters":
			parametersSeen = true
		case "type_annotation":
			returnTypeSeen = true
		default:
			return false
		}
	}
	if !defaultSeen || !functionSeen || !parametersSeen || !returnTypeSeen {
		return false
	}
	statement := tree.nodes[errorNode.parent]
	for _, childIndex := range statement.children {
		if childIndex < 0 || childIndex >= len(tree.nodes) {
			return false
		}
		child := tree.nodes[childIndex]
		if childIndex == errorIndex || child.kind == ";" || child.kind == "comment" {
			continue
		}
		if child.kind != "identifier" || child.startByte < 0 || child.endByte > len(source) ||
			source[child.startByte:child.endByte] != "export" {
			return false
		}
	}
	return true
}

func javascriptTypeScriptPublicFieldNameNode(
	source string,
	tree *javascriptSyntaxTree,
	nodeIndex int,
) int {
	nameIndex := javascriptDirectPropertyNameNode(tree, nodeIndex)
	if nameIndex < 0 || javascriptNodeText(source, tree, nameIndex) != "accessor" {
		return nameIndex
	}
	equalSeen := false
	for _, childIndex := range tree.nodes[nodeIndex].children {
		child := tree.nodes[childIndex]
		if child.kind == "=" {
			equalSeen = true
		}
		if equalSeen || child.kind != "ERROR" {
			continue
		}
		for _, errorChildIndex := range child.children {
			if tree.nodes[errorChildIndex].kind == "identifier" {
				return errorChildIndex
			}
		}
	}
	return nameIndex
}

func javascriptTypeScriptUsingBindingNode(
	tree *javascriptSyntaxTree,
	nodeIndex int,
) int {
	if tree == nil || nodeIndex < 0 || nodeIndex >= len(tree.nodes) ||
		tree.nodes[nodeIndex].kind != "assignment_expression" {
		return -1
	}
	if bindingIndex, using := javascriptTypeScriptAssignmentBindingNode(
		tree, nodeIndex,
	); using {
		return bindingIndex
	}
	parentIndex := tree.nodes[nodeIndex].parent
	if parentIndex < 0 || parentIndex >= len(tree.nodes) ||
		tree.nodes[parentIndex].kind != "sequence_expression" ||
		tree.nodes[parentIndex].parent < 0 ||
		tree.nodes[parentIndex].parent >= len(tree.nodes) ||
		tree.nodes[tree.nodes[parentIndex].parent].kind != "expression_statement" {
		return -1
	}
	usingFirst := false
	commaSeen := false
	for _, childIndex := range tree.nodes[parentIndex].children {
		if childIndex < 0 || childIndex >= len(tree.nodes) {
			return -1
		}
		if childIndex == nodeIndex {
			if !usingFirst || !commaSeen {
				return -1
			}
			bindingIndex, using := javascriptTypeScriptAssignmentBindingNode(tree, nodeIndex)
			if using {
				return -1
			}
			return bindingIndex
		}
		switch tree.nodes[childIndex].kind {
		case "comment":
		case ",":
			commaSeen = usingFirst
		default:
			if !usingFirst {
				usingFirst = javascriptTypeScriptUsingAssignmentNode(tree, childIndex)
				commaSeen = false
			} else {
				commaSeen = false
			}
		}
	}
	return -1
}

func javascriptTypeScriptAssignmentBindingNode(
	tree *javascriptSyntaxTree,
	nodeIndex int,
) (int, bool) {
	if tree == nil || nodeIndex < 0 || nodeIndex >= len(tree.nodes) ||
		tree.nodes[nodeIndex].kind != "assignment_expression" {
		return -1, false
	}
	usingSeen := false
	bindingIndex := -1
	equalSeen := false
	for _, childIndex := range tree.nodes[nodeIndex].children {
		if childIndex < 0 || childIndex >= len(tree.nodes) {
			return -1, false
		}
		if equalSeen {
			continue
		}
		switch tree.nodes[childIndex].kind {
		case "comment":
		case "using":
			if usingSeen || bindingIndex >= 0 {
				return -1, false
			}
			usingSeen = true
		case "identifier":
			if bindingIndex >= 0 {
				return -1, false
			}
			bindingIndex = childIndex
		case "=":
			if bindingIndex < 0 {
				return -1, false
			}
			equalSeen = true
		default:
			return -1, false
		}
	}
	if bindingIndex < 0 || !equalSeen {
		return -1, false
	}
	return bindingIndex, usingSeen
}

func javascriptTypeScriptUsingAssignmentNode(
	tree *javascriptSyntaxTree,
	nodeIndex int,
) bool {
	if tree == nil || nodeIndex < 0 || nodeIndex >= len(tree.nodes) {
		return false
	}
	if _, using := javascriptTypeScriptAssignmentBindingNode(tree, nodeIndex); using {
		return true
	}
	if tree.nodes[nodeIndex].kind != "await_expression" {
		return false
	}
	assignmentSeen := false
	for _, childIndex := range tree.nodes[nodeIndex].children {
		if childIndex < 0 || childIndex >= len(tree.nodes) {
			return false
		}
		switch tree.nodes[childIndex].kind {
		case "await", "comment":
		case "assignment_expression":
			if assignmentSeen {
				return false
			}
			_, using := javascriptTypeScriptAssignmentBindingNode(tree, childIndex)
			if !using {
				return false
			}
			assignmentSeen = true
		default:
			return false
		}
	}
	return assignmentSeen
}

func javascriptTypeScriptNamespaceExportNameNode(
	tree *javascriptSyntaxTree,
	nodeIndex int,
) int {
	if tree == nil || nodeIndex < 0 || nodeIndex >= len(tree.nodes) ||
		tree.nodes[nodeIndex].kind != "export_statement" {
		return -1
	}
	asSeen, namespaceSeen := false, false
	nameIndex := -1
	for _, childIndex := range tree.nodes[nodeIndex].children {
		switch tree.nodes[childIndex].kind {
		case "export", ";", "comment":
		case "as":
			if asSeen || namespaceSeen || nameIndex >= 0 {
				return -1
			}
			asSeen = true
		case "namespace":
			if !asSeen || namespaceSeen || nameIndex >= 0 {
				return -1
			}
			namespaceSeen = true
		case "identifier":
			if !namespaceSeen || nameIndex >= 0 {
				return -1
			}
			nameIndex = childIndex
		default:
			return -1
		}
	}
	if !asSeen || !namespaceSeen {
		return -1
	}
	return nameIndex
}

func normalizeJavaScriptDefinition(
	definition sourceDefinition,
	lineCount int,
) sourceDefinition {
	if definition.line < 1 || definition.line > lineCount ||
		!javascriptSourceName(definition.symbol) {
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

func sortUniqueJavaScriptDefinitions(definitions []sourceDefinition) []sourceDefinition {
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
	seen := make(map[javascriptDefinitionIdentity]bool, len(definitions))
	for _, definition := range definitions {
		key := javascriptDefinitionIdentity{
			symbol: definition.symbol,
			line:   definition.line,
			column: definition.column,
		}
		if definition.symbol == "" || seen[key] {
			continue
		}
		seen[key] = true
		unique = append(unique, definition)
	}
	return unique
}

func javascriptDirectNameNode(tree *javascriptSyntaxTree, nodeIndex int) int {
	if tree == nil || nodeIndex < 0 || nodeIndex >= len(tree.nodes) {
		return -1
	}
	for _, childIndex := range tree.nodes[nodeIndex].children {
		if tree.nodes[childIndex].kind == "identifier" ||
			tree.nodes[childIndex].kind == "type_identifier" {
			return childIndex
		}
	}
	return -1
}

func javascriptDirectTypeNameNode(tree *javascriptSyntaxTree, nodeIndex int) int {
	if tree == nil || nodeIndex < 0 || nodeIndex >= len(tree.nodes) {
		return -1
	}
	for _, childIndex := range tree.nodes[nodeIndex].children {
		switch tree.nodes[childIndex].kind {
		case "type_identifier", "identifier":
			return childIndex
		}
	}
	return -1
}

func javascriptTypeScriptModuleNameNodes(
	tree *javascriptSyntaxTree,
	nodeIndex int,
) []int {
	if tree == nil || nodeIndex < 0 || nodeIndex >= len(tree.nodes) {
		return nil
	}
	for _, childIndex := range tree.nodes[nodeIndex].children {
		child := tree.nodes[childIndex]
		switch child.kind {
		case "identifier", "type_identifier":
			return []int{childIndex}
		case "nested_identifier", "nested_type_identifier":
			names := javascriptTypeScriptNestedNameNodes(tree, childIndex)
			return names
		}
	}
	return nil
}

func javascriptTypeScriptNestedNameNodes(
	tree *javascriptSyntaxTree,
	nodeIndex int,
) []int {
	if tree == nil || nodeIndex < 0 || nodeIndex >= len(tree.nodes) {
		return nil
	}
	pending := []int{nodeIndex}
	names := make([]int, 0, 3)
	for len(pending) > 0 {
		last := len(pending) - 1
		current := pending[last]
		pending = pending[:last]
		if current < 0 || current >= len(tree.nodes) {
			continue
		}
		for _, childIndex := range tree.nodes[current].children {
			switch tree.nodes[childIndex].kind {
			case "identifier", "type_identifier", "property_identifier":
				names = append(names, childIndex)
			default:
				pending = append(pending, childIndex)
			}
		}
	}
	sort.Slice(names, func(first, second int) bool {
		return tree.nodes[names[first]].startByte < tree.nodes[names[second]].startByte
	})
	return names
}

func javascriptTypeScriptParameterProperty(
	source string,
	tree *javascriptSyntaxTree,
	nodeIndex int,
) bool {
	if tree == nil || nodeIndex < 0 || nodeIndex >= len(tree.nodes) {
		return false
	}
	modifier := false
	for _, childIndex := range tree.nodes[nodeIndex].children {
		switch tree.nodes[childIndex].kind {
		case "accessibility_modifier", "readonly", "override":
			modifier = true
		}
	}
	if !modifier {
		return false
	}
	parametersIndex := tree.nodes[nodeIndex].parent
	if parametersIndex < 0 || parametersIndex >= len(tree.nodes) ||
		tree.nodes[parametersIndex].kind != "formal_parameters" {
		return false
	}
	ownerIndex := tree.nodes[parametersIndex].parent
	if ownerIndex < 0 || ownerIndex >= len(tree.nodes) ||
		(tree.nodes[ownerIndex].kind != "method_definition" &&
			tree.nodes[ownerIndex].kind != "method_signature") {
		return false
	}
	nameIndex := javascriptDirectPropertyNameNode(tree, ownerIndex)
	return nameIndex >= 0 && tree.nodes[nameIndex].kind == "property_identifier" &&
		javascriptNodeText(source, tree, nameIndex) == "constructor"
}

func javascriptDirectChildOfKind(
	tree *javascriptSyntaxTree,
	nodeIndex int,
	parentKind string,
) bool {
	if tree == nil || nodeIndex < 0 || nodeIndex >= len(tree.nodes) {
		return false
	}
	parentIndex := tree.nodes[nodeIndex].parent
	return parentIndex >= 0 && parentIndex < len(tree.nodes) &&
		tree.nodes[parentIndex].kind == parentKind
}

func javascriptDirectPropertyNameNode(tree *javascriptSyntaxTree, nodeIndex int) int {
	if tree == nil || nodeIndex < 0 || nodeIndex >= len(tree.nodes) {
		return -1
	}
	for _, childIndex := range tree.nodes[nodeIndex].children {
		switch tree.nodes[childIndex].kind {
		case "identifier", "property_identifier", "private_property_identifier",
			"shorthand_property_identifier", "shorthand_property_identifier_pattern":
			return childIndex
		case "string":
			return javascriptStaticStringFragmentNode(tree, childIndex)
		case "computed_property_name", "=", "formal_parameters", "statement_block":
			return -1
		}
	}
	return -1
}

func javascriptDeclaratorPatternNode(tree *javascriptSyntaxTree, nodeIndex int) int {
	if tree == nil || nodeIndex < 0 || nodeIndex >= len(tree.nodes) {
		return -1
	}
	for _, childIndex := range tree.nodes[nodeIndex].children {
		switch tree.nodes[childIndex].kind {
		case "identifier", "object_pattern", "array_pattern", "assignment_pattern",
			"object_assignment_pattern", "rest_pattern", "pattern",
			"shorthand_property_identifier_pattern":
			return childIndex
		}
	}
	return -1
}

func javascriptForBindingPatternNode(tree *javascriptSyntaxTree, nodeIndex int) int {
	if tree == nil || nodeIndex < 0 || nodeIndex >= len(tree.nodes) {
		return -1
	}
	bindingKeyword := false
	for _, childIndex := range tree.nodes[nodeIndex].children {
		kind := tree.nodes[childIndex].kind
		if kind == "var" || kind == "let" || kind == "const" || kind == "using" {
			bindingKeyword = true
			continue
		}
		if bindingKeyword && javascriptPatternChildKind(kind) {
			return childIndex
		}
		if kind == "in" || kind == "of" {
			break
		}
	}
	return -1
}

func javascriptBindingNameNodes(tree *javascriptSyntaxTree, patternIndex int) []int {
	if tree == nil || patternIndex < 0 || patternIndex >= len(tree.nodes) {
		return nil
	}
	stack := []int{patternIndex}
	seen := make(map[int]bool)
	names := make([]int, 0)
	for len(stack) > 0 {
		last := len(stack) - 1
		nodeIndex := stack[last]
		stack = stack[:last]
		if nodeIndex < 0 || nodeIndex >= len(tree.nodes) || seen[nodeIndex] {
			continue
		}
		seen[nodeIndex] = true
		node := tree.nodes[nodeIndex]
		switch node.kind {
		case "identifier", "shorthand_property_identifier_pattern":
			names = append(names, nodeIndex)
			continue
		case "assignment_pattern", "object_assignment_pattern":
			for _, childIndex := range node.children {
				if tree.nodes[childIndex].kind == "=" {
					break
				}
				if javascriptPatternChildKind(tree.nodes[childIndex].kind) {
					stack = append(stack, childIndex)
					break
				}
			}
			continue
		case "pair_pattern":
			colonSeen := false
			for _, childIndex := range node.children {
				childKind := tree.nodes[childIndex].kind
				if childKind == ":" {
					colonSeen = true
					continue
				}
				if colonSeen && javascriptPatternChildKind(childKind) {
					stack = append(stack, childIndex)
				}
			}
			continue
		case "object_pattern", "array_pattern", "rest_pattern", "pattern":
		default:
			continue
		}
		for index := len(node.children) - 1; index >= 0; index-- {
			childIndex := node.children[index]
			if javascriptPatternChildKind(tree.nodes[childIndex].kind) {
				stack = append(stack, childIndex)
			}
		}
	}
	sort.Slice(names, func(first, second int) bool {
		return tree.nodes[names[first]].startByte < tree.nodes[names[second]].startByte
	})
	return names
}

func javascriptPatternChildKind(kind string) bool {
	switch kind {
	case "identifier", "object_pattern", "array_pattern", "assignment_pattern",
		"object_assignment_pattern", "rest_pattern", "pattern", "pair_pattern",
		"shorthand_property_identifier_pattern":
		return true
	default:
		return false
	}
}

func javascriptScopedSubtrees(tree *javascriptSyntaxTree) []bool {
	if tree == nil {
		return nil
	}
	scoped := make([]bool, len(tree.nodes))
	for nodeIndex := len(tree.nodes) - 1; nodeIndex >= 0; nodeIndex-- {
		node := tree.nodes[nodeIndex]
		scoped[nodeIndex] = javascriptDirectScopeKind(node.kind)
		for _, childIndex := range node.children {
			if childIndex >= 0 && childIndex < len(scoped) && scoped[childIndex] {
				scoped[nodeIndex] = true
				break
			}
		}
	}
	return scoped
}

func javascriptDirectScopeKind(kind string) bool {
	switch kind {
	case "function_declaration", "generator_function_declaration", "function_expression",
		"generator_function", "arrow_function", "class_declaration",
		"abstract_class_declaration", "class", "method_definition", "class_static_block",
		"function_signature", "method_signature", "abstract_method_signature",
		"interface_declaration", "enum_declaration", "type_alias_declaration",
		"internal_module", "module":
		return true
	default:
		return false
	}
}

func javascriptDefinitionHasScopedValue(
	tree *javascriptSyntaxTree,
	nodeIndex int,
	scopedSubtree []bool,
) bool {
	if tree == nil || nodeIndex < 0 || nodeIndex >= len(tree.nodes) {
		return false
	}
	valueSeen := false
	for _, childIndex := range tree.nodes[nodeIndex].children {
		child := tree.nodes[childIndex]
		if child.kind == "=" || child.kind == ":" {
			valueSeen = true
			continue
		}
		if valueSeen && childIndex < len(scopedSubtree) && scopedSubtree[childIndex] {
			return true
		}
	}
	return false
}

func javascriptDefinitionHasDirectScopedValue(tree *javascriptSyntaxTree, nodeIndex int) bool {
	if tree == nil || nodeIndex < 0 || nodeIndex >= len(tree.nodes) {
		return false
	}
	valueSeen := false
	for _, childIndex := range tree.nodes[nodeIndex].children {
		child := tree.nodes[childIndex]
		if child.kind == "=" || child.kind == ":" {
			valueSeen = true
			continue
		}
		if valueSeen && javascriptTransparentScopedValue(tree, childIndex, 0) {
			return true
		}
	}
	return false
}

func javascriptTransparentScopedValue(
	tree *javascriptSyntaxTree,
	nodeIndex, depth int,
) bool {
	if tree == nil || nodeIndex < 0 || nodeIndex >= len(tree.nodes) ||
		depth > javascriptMaximumSyntaxUnwrapDepth {
		return false
	}
	node := tree.nodes[nodeIndex]
	if javascriptDirectScopeKind(node.kind) {
		return true
	}
	if node.kind != "parenthesized_expression" {
		return false
	}
	for _, childIndex := range node.children {
		switch tree.nodes[childIndex].kind {
		case "(", ")", "comment":
			continue
		default:
			return javascriptTransparentScopedValue(tree, childIndex, depth+1)
		}
	}
	return false
}

func javascriptEarlierSiblingErrors(tree *javascriptSyntaxTree) []bool {
	if tree == nil {
		return nil
	}
	result := make([]bool, len(tree.nodes))
	for _, parent := range tree.nodes {
		errorSeen := false
		for _, childIndex := range parent.children {
			if childIndex < 0 || childIndex >= len(tree.nodes) {
				continue
			}
			result[childIndex] = errorSeen
			errorSeen = errorSeen || tree.nodes[childIndex].kind == "ERROR"
		}
	}
	return result
}

func javascriptDefinitionScopeNode(tree *javascriptSyntaxTree, nodeIndex int) int {
	if tree == nil || nodeIndex < 0 || nodeIndex >= len(tree.nodes) {
		return -1
	}
	scopeIndex := nodeIndex
	node := tree.nodes[nodeIndex]
	if node.kind == "variable_declarator" && node.parent >= 0 {
		parentKind := tree.nodes[node.parent].kind
		if parentKind == "lexical_declaration" || parentKind == "variable_declaration" ||
			parentKind == "using_declaration" {
			scopeIndex = node.parent
		}
	}
	if node.kind == "assignment_expression" {
		if statementIndex := javascriptTypeScriptUsingStatementNode(
			tree, nodeIndex,
		); statementIndex >= 0 {
			scopeIndex = statementIndex
		} else if node.parent >= 0 && tree.nodes[node.parent].kind == "expression_statement" {
			scopeIndex = node.parent
		}
	}
	for tree.nodes[scopeIndex].parent >= 0 {
		parentIndex := tree.nodes[scopeIndex].parent
		if tree.nodes[parentIndex].kind != "export_statement" &&
			tree.nodes[parentIndex].kind != "ambient_declaration" {
			break
		}
		scopeIndex = parentIndex
	}
	return scopeIndex
}

func javascriptTypeScriptUsingStatementNode(
	tree *javascriptSyntaxTree,
	nodeIndex int,
) int {
	if javascriptTypeScriptUsingBindingNode(tree, nodeIndex) < 0 {
		return -1
	}
	for candidate, depth := nodeIndex, 0; candidate >= 0 &&
		candidate < len(tree.nodes) && depth < 4; depth++ {
		if tree.nodes[candidate].kind == "expression_statement" {
			return candidate
		}
		candidate = tree.nodes[candidate].parent
	}
	return -1
}

func javascriptCommonJSExportNameNode(
	source string,
	tree *javascriptSyntaxTree,
	assignmentIndex int,
) int {
	if tree == nil || assignmentIndex < 0 || assignmentIndex >= len(tree.nodes) {
		return -1
	}
	leftIndex := -1
	for _, childIndex := range tree.nodes[assignmentIndex].children {
		if tree.nodes[childIndex].kind == "=" {
			break
		}
		if tree.nodes[childIndex].kind == "member_expression" ||
			tree.nodes[childIndex].kind == "subscript_expression" {
			leftIndex = childIndex
		}
	}
	if leftIndex < 0 {
		return -1
	}
	parts, valid := javascriptMemberNameNodes(tree, leftIndex)
	if !valid || len(parts) < 2 {
		return -1
	}
	text := func(index int) string {
		node := tree.nodes[index]
		if node.startByte < 0 || node.endByte > len(source) || node.startByte >= node.endByte {
			return ""
		}
		return source[node.startByte:node.endByte]
	}
	if text(parts[0]) == "exports" {
		return parts[len(parts)-1]
	}
	if len(parts) >= 3 && text(parts[0]) == "module" && text(parts[1]) == "exports" {
		return parts[len(parts)-1]
	}
	return -1
}

func javascriptMemberNameNodes(tree *javascriptSyntaxTree, nodeIndex int) ([]int, bool) {
	if tree == nil || nodeIndex < 0 || nodeIndex >= len(tree.nodes) {
		return nil, false
	}
	node := tree.nodes[nodeIndex]
	switch node.kind {
	case "identifier", "property_identifier":
		return []int{nodeIndex}, true
	case "parenthesized_expression":
		expressionIndex := -1
		for _, childIndex := range node.children {
			switch tree.nodes[childIndex].kind {
			case "(", ")", "comment":
				continue
			default:
				if expressionIndex >= 0 {
					return nil, false
				}
				expressionIndex = childIndex
			}
		}
		return javascriptMemberNameNodes(tree, expressionIndex)
	case "member_expression":
		objectIndex, propertyIndex := -1, -1
		for _, childIndex := range node.children {
			kind := tree.nodes[childIndex].kind
			switch kind {
			case ".", "comment":
				continue
			case "property_identifier":
				if objectIndex < 0 || propertyIndex >= 0 {
					return nil, false
				}
				propertyIndex = childIndex
			default:
				if objectIndex >= 0 {
					return nil, false
				}
				objectIndex = childIndex
			}
		}
		objectParts, ok := javascriptMemberNameNodes(tree, objectIndex)
		if !ok || propertyIndex < 0 {
			return nil, false
		}
		return append(objectParts, propertyIndex), true
	case "subscript_expression":
		objectIndex, keyIndex := -1, -1
		for _, childIndex := range node.children {
			kind := tree.nodes[childIndex].kind
			switch kind {
			case "[", "]", "comment":
				continue
			case "string":
				if objectIndex < 0 || keyIndex >= 0 {
					return nil, false
				}
				keyIndex = javascriptStaticStringFragmentNode(tree, childIndex)
			default:
				if objectIndex >= 0 {
					return nil, false
				}
				objectIndex = childIndex
			}
		}
		objectParts, ok := javascriptMemberNameNodes(tree, objectIndex)
		if !ok || keyIndex < 0 {
			return nil, false
		}
		return append(objectParts, keyIndex), true
	default:
		return nil, false
	}
}

func javascriptStaticStringFragmentNode(tree *javascriptSyntaxTree, stringIndex int) int {
	if tree == nil || stringIndex < 0 || stringIndex >= len(tree.nodes) ||
		tree.nodes[stringIndex].kind != "string" {
		return -1
	}
	fragmentIndex := -1
	for _, childIndex := range tree.nodes[stringIndex].children {
		switch tree.nodes[childIndex].kind {
		case "\"", "'":
			continue
		case "string_fragment":
			if fragmentIndex >= 0 {
				return -1
			}
			fragmentIndex = childIndex
		default:
			return -1
		}
	}
	return fragmentIndex
}

func javascriptTreeScopesFromSyntax(
	source string,
	lineCount int,
	tree *javascriptSyntaxTree,
	excluded []javascriptByteSpan,
	attachedStarts []int,
	errorContext []bool,
) []javascriptLineScope {
	if tree == nil {
		return nil
	}
	positions := javascriptSourcePositions{source: source, lineStarts: javascriptLineStarts(source)}
	scopes := make([]javascriptLineScope, 0)
	for nodeIndex, node := range tree.nodes {
		if !javascriptTreeScopeKind(node.kind) ||
			(node.kind == "class" && len(node.children) == 0) ||
			(nodeIndex < len(errorContext) && errorContext[nodeIndex]) ||
			javascriptByteRangeExcluded(node.startByte, node.endByte, excluded) {
			continue
		}
		if node.kind == "statement_block" && javascriptBlockOwnedByParent(tree, nodeIndex) {
			continue
		}
		startOffset := node.startByte
		if javascriptTreeDefinitionOwnerKind(node.kind) {
			startOffset = javascriptSyntaxAttachedStart(tree, nodeIndex, attachedStarts)
		}
		start, end := positions.lineSpan(startOffset, node.endByte)
		start, end = max(start, 1), min(max(end, start), lineCount)
		if start <= end {
			scopes = append(scopes, javascriptLineScope{start: start, end: end})
		}
	}
	return normalizeJavaScriptScopes(scopes)
}

func javascriptTreeScopeKind(kind string) bool {
	if javascriptDirectScopeKind(kind) {
		return true
	}
	switch kind {
	case "if_statement", "else_clause", "switch_statement", "switch_case",
		"switch_default", "for_statement", "for_in_statement", "while_statement",
		"do_statement", "try_statement", "catch_clause", "finally_clause",
		"with_statement", "labeled_statement", "statement_block", "ambient_declaration",
		"property_signature", "call_signature", "construct_signature", "index_signature":
		return true
	default:
		return false
	}
}

func javascriptTreeDefinitionOwnerKind(kind string) bool {
	switch kind {
	case "function_declaration", "generator_function_declaration", "class_declaration",
		"abstract_class_declaration", "function_expression", "generator_function", "class",
		"method_definition", "function_signature", "method_signature",
		"abstract_method_signature", "interface_declaration", "enum_declaration",
		"type_alias_declaration", "internal_module", "module":
		return true
	default:
		return false
	}
}

func javascriptBlockOwnedByParent(tree *javascriptSyntaxTree, nodeIndex int) bool {
	if tree == nil || nodeIndex < 0 || nodeIndex >= len(tree.nodes) {
		return false
	}
	parentIndex := tree.nodes[nodeIndex].parent
	return parentIndex >= 0 && parentIndex < len(tree.nodes) &&
		javascriptTreeScopeKind(tree.nodes[parentIndex].kind)
}

func normalizeJavaScriptScopes(scopes []javascriptLineScope) []javascriptLineScope {
	sort.Slice(scopes, func(first, second int) bool {
		if scopes[first].start != scopes[second].start {
			return scopes[first].start < scopes[second].start
		}
		return scopes[first].end < scopes[second].end
	})
	unique := scopes[:0]
	for _, scope := range scopes {
		if scope.start < 1 || scope.end < scope.start {
			continue
		}
		if len(unique) == 0 || unique[len(unique)-1] != scope {
			unique = append(unique, scope)
		}
	}
	return unique
}

func (j javascriptLanguage) enclosingScope(lines []string, lineNo int) (int, int) {
	if lineNo < 1 || lineNo > len(lines) {
		return lineNo, lineNo
	}
	analysis := j.sourceAnalysis(lines)
	if analysis == nil {
		return lineNo, lineNo
	}
	bestStart, bestEnd := 0, 0
	for _, scope := range analysis.scopes {
		if lineNo < scope.start || lineNo > scope.end {
			continue
		}
		if bestStart == 0 || scope.end-scope.start < bestEnd-bestStart ||
			scope.end-scope.start == bestEnd-bestStart && scope.start > bestStart {
			bestStart, bestEnd = scope.start, scope.end
		}
	}
	if bestStart == 0 {
		return lineNo, lineNo
	}
	return bestStart, bestEnd
}

func (j javascriptLanguage) navigationScope(lines []string, lineNo int) (int, int) {
	if lineNo < 1 || lineNo > len(lines) {
		return lineNo, lineNo
	}
	analysis := j.sourceAnalysis(lines)
	if analysis == nil {
		return lineNo, lineNo
	}
	bestStart, bestEnd := 0, 0
	bestLine := 0
	bestBefore := false
	for _, definition := range analysis.definitions {
		if !definition.ownsScope || lineNo < definition.scopeStart || lineNo > definition.scopeEnd {
			continue
		}
		size := definition.scopeEnd - definition.scopeStart
		before := definition.line <= lineNo
		if bestStart == 0 || before && !bestBefore || before == bestBefore &&
			(size < bestEnd-bestStart || size == bestEnd-bestStart &&
				(before && definition.line > bestLine || !before && definition.line < bestLine)) {
			bestStart, bestEnd = definition.scopeStart, definition.scopeEnd
			bestLine = definition.line
			bestBefore = before
		}
	}
	if bestStart > 0 {
		return bestStart, bestEnd
	}
	return j.enclosingScope(lines, lineNo)
}

func javascriptTreeImportsFromSyntaxFlavor(
	source string,
	lineCount int,
	tree *javascriptSyntaxTree,
	excluded []javascriptByteSpan,
	attachedStarts []int,
	errorContext []bool,
	typeScript bool,
) []javascriptLineSpan {
	if tree == nil {
		return nil
	}
	positions := javascriptSourcePositions{source: source, lineStarts: javascriptLineStarts(source)}
	imports := make([]javascriptLineSpan, 0)
	var typeScriptReferences map[int]bool
	if typeScript {
		typeScriptReferences = javascriptTypeScriptLeadingReferenceNodes(source, tree, true)
	}
	for nodeIndex, node := range tree.nodes {
		if typeScript && typeScriptReferences[nodeIndex] {
			start, end := positions.lineSpan(node.startByte, node.endByte)
			if start >= 1 && end >= start && end <= lineCount {
				imports = append(imports, javascriptLineSpan{start: start, end: end})
			}
			continue
		}
		if nodeIndex < len(errorContext) && errorContext[nodeIndex] ||
			javascriptByteRangeExcluded(node.startByte, node.endByte, excluded) {
			continue
		}
		spanNode := -1
		switch node.kind {
		case "import_statement":
			spanNode = nodeIndex
		case "export_statement":
			if javascriptExportHasSource(tree, nodeIndex) {
				spanNode = nodeIndex
			} else if typeScript {
				if endOffset, ok := javascriptTypeScriptExportImportRequireEnd(
					source, tree, nodeIndex,
				); ok {
					startOffset := javascriptSyntaxAttachedStart(tree, nodeIndex, attachedStarts)
					start, end := positions.lineSpan(startOffset, endOffset)
					if start >= 1 && end >= start && end <= lineCount {
						imports = append(imports, javascriptLineSpan{start: start, end: end})
					}
					continue
				}
			}
		case "call_expression":
			if javascriptRequireCall(source, tree, nodeIndex) {
				spanNode = javascriptTopLevelStatementNode(tree, nodeIndex)
			} else if typeScript && javascriptTypeScriptImportTypeCall(source, tree, nodeIndex) {
				spanNode = nodeIndex
			}
		}
		if spanNode < 0 || spanNode >= len(tree.nodes) {
			continue
		}
		span := tree.nodes[spanNode]
		startOffset := javascriptSyntaxAttachedStart(tree, spanNode, attachedStarts)
		start, end := positions.lineSpan(startOffset, span.endByte)
		if start >= 1 && end >= start && end <= lineCount {
			imports = append(imports, javascriptLineSpan{start: start, end: end})
		}
	}
	return normalizeJavaScriptLineSpans(imports)
}

func javascriptTypeScriptExportImportRequireEnd(
	source string,
	tree *javascriptSyntaxTree,
	nodeIndex int,
) (int, bool) {
	if tree == nil || nodeIndex < 0 || nodeIndex >= len(tree.nodes) ||
		tree.nodes[nodeIndex].kind != "export_statement" {
		return 0, false
	}
	requireEnd := -1
	for _, childIndex := range tree.nodes[nodeIndex].children {
		if tree.nodes[childIndex].kind != "import_alias" {
			continue
		}
		equalSeen := false
		for _, aliasChildIndex := range tree.nodes[childIndex].children {
			aliasChild := tree.nodes[aliasChildIndex]
			switch aliasChild.kind {
			case "=":
				equalSeen = true
			case "identifier":
				if equalSeen && javascriptNodeText(source, tree, aliasChildIndex) == "require" {
					requireEnd = aliasChild.endByte
				}
			}
		}
	}
	if requireEnd < 0 || requireEnd != tree.nodes[nodeIndex].endByte {
		return 0, false
	}
	offset := javascriptTypeScriptSkipSpaceAndComments(source, requireEnd)
	if offset >= len(source) || source[offset] != '(' {
		return 0, false
	}
	offset = javascriptTypeScriptSkipSpaceAndComments(source, offset+1)
	if offset >= len(source) || source[offset] != '\'' && source[offset] != '"' {
		return 0, false
	}
	quote := source[offset]
	literalEnd := javascriptQuotedLiteralEnd(source, offset, quote)
	if literalEnd <= offset+1 || literalEnd > len(source) || source[literalEnd-1] != quote {
		return 0, false
	}
	offset = javascriptTypeScriptSkipSpaceAndComments(source, literalEnd)
	if offset >= len(source) || source[offset] != ')' {
		return 0, false
	}
	offset++
	for offset < len(source) && (source[offset] == ' ' || source[offset] == '\t') {
		offset++
	}
	if offset < len(source) && source[offset] == ';' {
		offset++
	}
	return offset, true
}

func javascriptTypeScriptSkipSpaceAndComments(source string, offset int) int {
	for offset >= 0 && offset < len(source) {
		if size := javascriptWhitespaceSize(source, offset); size > 0 {
			offset += size
			continue
		}
		if strings.HasPrefix(source[offset:], "//") {
			offset = javascriptLineTerminatorOffset(source, offset+2)
			continue
		}
		if strings.HasPrefix(source[offset:], "/*") {
			end := strings.Index(source[offset+2:], "*/")
			if end < 0 {
				return len(source)
			}
			offset += end + 4
			continue
		}
		break
	}
	return max(offset, 0)
}

func javascriptTypeScriptLeadingReferenceNodes(
	source string,
	tree *javascriptSyntaxTree,
	typeScript bool,
) map[int]bool {
	references := make(map[int]bool)
	if !typeScript || tree == nil || tree.root < 0 || tree.root >= len(tree.nodes) {
		return references
	}
	for _, childIndex := range tree.nodes[tree.root].children {
		if childIndex < 0 || childIndex >= len(tree.nodes) {
			break
		}
		child := tree.nodes[childIndex]
		switch child.kind {
		case "comment":
			comment := javascriptNodeText(source, tree, childIndex)
			if javascriptTypeScriptReferenceDirective(comment) ||
				javascriptTypeScriptAMDDependencyDirective(comment) {
				references[childIndex] = true
			}
		case "hash_bang_line":
		default:
			return references
		}
	}
	return references
}

func javascriptTypeScriptReferenceDirective(comment string) bool {
	attributes, ok := javascriptTypeScriptDirectiveAttributes(comment, "reference")
	if !ok {
		return false
	}
	for _, attribute := range []string{"path", "types", "lib"} {
		if attributes[attribute] != "" {
			return true
		}
	}
	return false
}

func javascriptTypeScriptAMDDependencyDirective(comment string) bool {
	attributes, ok := javascriptTypeScriptDirectiveAttributes(comment, "amd-dependency")
	return ok && attributes["path"] != ""
}

func javascriptTypeScriptDirectiveAttributes(
	comment string,
	tag string,
) (map[string]string, bool) {
	comment = strings.TrimSpace(comment)
	if !strings.HasPrefix(comment, "///") {
		return nil, false
	}
	directive := strings.TrimSpace(strings.TrimPrefix(comment, "///"))
	prefix := "<" + tag
	if !strings.HasPrefix(directive, prefix) || !strings.HasSuffix(directive, "/>") {
		return nil, false
	}
	if len(directive) > len(prefix) {
		next := directive[len(prefix)]
		if next != ' ' && next != '\t' && next != '\r' && next != '\n' {
			return nil, false
		}
	}
	attributeSource := strings.TrimSpace(directive[len(prefix) : len(directive)-len("/>")])
	if attributeSource == "" {
		return nil, false
	}
	attributes := make(map[string]string)
	for offset := 0; offset < len(attributeSource); {
		for offset < len(attributeSource) && (attributeSource[offset] == ' ' ||
			attributeSource[offset] == '\t' || attributeSource[offset] == '\r' ||
			attributeSource[offset] == '\n') {
			offset++
		}
		if offset == len(attributeSource) {
			break
		}
		nameStart := offset
		for offset < len(attributeSource) && (attributeSource[offset] == '-' ||
			attributeSource[offset] >= 'a' && attributeSource[offset] <= 'z') {
			offset++
		}
		if nameStart == offset {
			return nil, false
		}
		name := attributeSource[nameStart:offset]
		if _, exists := attributes[name]; exists {
			return nil, false
		}
		for offset < len(attributeSource) && (attributeSource[offset] == ' ' ||
			attributeSource[offset] == '\t') {
			offset++
		}
		if offset >= len(attributeSource) || attributeSource[offset] != '=' {
			return nil, false
		}
		offset++
		for offset < len(attributeSource) && (attributeSource[offset] == ' ' ||
			attributeSource[offset] == '\t') {
			offset++
		}
		if offset >= len(attributeSource) ||
			attributeSource[offset] != '\'' && attributeSource[offset] != '"' {
			return nil, false
		}
		quote := attributeSource[offset]
		offset++
		valueStart := offset
		for offset < len(attributeSource) && attributeSource[offset] != quote {
			offset++
		}
		if offset >= len(attributeSource) {
			return nil, false
		}
		value := attributeSource[valueStart:offset]
		offset++
		if value == "" {
			return nil, false
		}
		attributes[name] = value
	}
	return attributes, len(attributes) > 0
}

func javascriptTypeScriptImportTypeCall(
	source string,
	tree *javascriptSyntaxTree,
	nodeIndex int,
) bool {
	if tree == nil || nodeIndex < 0 || nodeIndex >= len(tree.nodes) ||
		!javascriptTypeScriptTypePosition(tree, nodeIndex) {
		return false
	}
	calleeSeen := false
	for _, childIndex := range tree.nodes[nodeIndex].children {
		child := tree.nodes[childIndex]
		if child.kind == "comment" {
			continue
		}
		if !calleeSeen {
			if child.kind != "import" || javascriptNodeText(source, tree, childIndex) != "import" {
				return false
			}
			calleeSeen = true
			continue
		}
		if child.kind != "arguments" {
			return false
		}
		arguments := make([]string, 0, 2)
		for _, argumentIndex := range child.children {
			switch tree.nodes[argumentIndex].kind {
			case "(", ")", ",", "comment":
				continue
			default:
				arguments = append(arguments, tree.nodes[argumentIndex].kind)
			}
		}
		return len(arguments) == 1 && arguments[0] == "string" ||
			len(arguments) == 2 && arguments[0] == "string" && arguments[1] == "object"
	}
	return false
}

func javascriptTypeScriptTypePosition(tree *javascriptSyntaxTree, nodeIndex int) bool {
	if tree == nil || nodeIndex < 0 || nodeIndex >= len(tree.nodes) {
		return false
	}
	childIndex := nodeIndex
	for parentIndex := tree.nodes[nodeIndex].parent; parentIndex >= 0; {
		if parentIndex >= len(tree.nodes) || parentIndex >= childIndex {
			return false
		}
		switch tree.nodes[parentIndex].kind {
		case "type_query", "type_alias_declaration", "type_annotation", "type_arguments",
			"extends_type_clause", "implements_clause", "constraint", "default_type",
			"optional_type", "rest_type", "constructor_type", "function_type",
			"conditional_type", "generic_type", "mapped_type_clause", "literal_type",
			"template_literal_type", "object_type", "tuple_type", "array_type",
			"readonly_type", "union_type", "intersection_type", "lookup_type",
			"index_type_query", "infer_type", "type_predicate",
			"type_predicate_annotation", "asserts_annotation", "parenthesized_type":
			return true
		case "as_expression", "satisfies_expression":
			return javascriptChildFollowsTypeOperator(tree, parentIndex, childIndex)
		}
		childIndex = parentIndex
		nodeIndex = parentIndex
		parentIndex = tree.nodes[nodeIndex].parent
	}
	return false
}

func javascriptChildFollowsTypeOperator(
	tree *javascriptSyntaxTree,
	parentIndex int,
	childIndex int,
) bool {
	if tree == nil || parentIndex < 0 || parentIndex >= len(tree.nodes) ||
		childIndex < 0 || childIndex >= len(tree.nodes) {
		return false
	}
	operatorSeen := false
	for _, siblingIndex := range tree.nodes[parentIndex].children {
		if siblingIndex == childIndex {
			return operatorSeen
		}
		switch tree.nodes[siblingIndex].kind {
		case "as", "satisfies":
			operatorSeen = true
		}
	}
	return false
}

func javascriptExportHasSource(tree *javascriptSyntaxTree, nodeIndex int) bool {
	if tree == nil || nodeIndex < 0 || nodeIndex >= len(tree.nodes) {
		return false
	}
	for _, childIndex := range tree.nodes[nodeIndex].children {
		if tree.nodes[childIndex].kind == "from" {
			return true
		}
	}
	return false
}

func javascriptRequireCall(source string, tree *javascriptSyntaxTree, nodeIndex int) bool {
	if tree == nil || nodeIndex < 0 || nodeIndex >= len(tree.nodes) {
		return false
	}
	calleeSeen := false
	for _, childIndex := range tree.nodes[nodeIndex].children {
		child := tree.nodes[childIndex]
		if child.kind == "comment" {
			continue
		}
		if !calleeSeen {
			if child.kind != "identifier" || child.startByte < 0 || child.endByte > len(source) ||
				source[child.startByte:child.endByte] != "require" {
				return false
			}
			calleeSeen = true
			continue
		}
		if child.kind != "arguments" {
			return false
		}
		argumentCount := 0
		for _, argumentIndex := range child.children {
			switch tree.nodes[argumentIndex].kind {
			case "(", ")", ",", "comment":
				continue
			case "string":
				argumentCount++
			default:
				return false
			}
		}
		return argumentCount == 1
	}
	return false
}

func javascriptTopLevelStatementNode(tree *javascriptSyntaxTree, nodeIndex int) int {
	if tree == nil || nodeIndex < 0 || nodeIndex >= len(tree.nodes) {
		return -1
	}
	candidate := nodeIndex
	for tree.nodes[candidate].parent >= 0 {
		parentIndex := tree.nodes[candidate].parent
		if tree.nodes[parentIndex].kind == "program" {
			return candidate
		}
		if javascriptDeferredExecutionBoundary(tree, parentIndex, candidate, nodeIndex) {
			return -1
		}
		candidate = parentIndex
	}
	return -1
}

func javascriptDeferredExecutionBoundary(
	tree *javascriptSyntaxTree,
	parentIndex, childIndex, descendantIndex int,
) bool {
	if tree == nil || parentIndex < 0 || parentIndex >= len(tree.nodes) ||
		childIndex < 0 || childIndex >= len(tree.nodes) {
		return true
	}
	switch tree.nodes[parentIndex].kind {
	case "function_declaration", "generator_function_declaration", "function_expression",
		"generator_function", "arrow_function":
		return true
	case "method_definition":
		// Decorators and computed property names run while their containing
		// object or class is evaluated. A legacy parameter decorator is likewise
		// evaluated with the class; other parameter expressions and the method
		// body wait until the method is called.
		switch tree.nodes[childIndex].kind {
		case "formal_parameters":
			return !javascriptTypeScriptParameterDecoratorDescendant(
				tree, descendantIndex, childIndex,
			)
		case "statement_block":
			return true
		default:
			return false
		}
	case "field_definition", "public_field_definition":
		// Computed names and decorators are eager for every field. Initializers
		// are eager only for static fields; instance initializers run later for
		// each constructed object.
		initializer := false
		static := false
		for _, siblingIndex := range tree.nodes[parentIndex].children {
			if siblingIndex < 0 || siblingIndex >= len(tree.nodes) {
				continue
			}
			if siblingIndex == childIndex {
				return initializer && !static
			}
			switch tree.nodes[siblingIndex].kind {
			case "static":
				static = true
			case "=":
				initializer = true
			}
		}
		return initializer && !static
	default:
		return false
	}
}

func javascriptTypeScriptParameterDecoratorDescendant(
	tree *javascriptSyntaxTree,
	descendantIndex int,
	parametersIndex int,
) bool {
	if tree == nil || descendantIndex < 0 || descendantIndex >= len(tree.nodes) ||
		parametersIndex < 0 || parametersIndex >= len(tree.nodes) ||
		tree.nodes[parametersIndex].kind != "formal_parameters" {
		return false
	}
	for candidate, depth := descendantIndex, 0; candidate >= 0 &&
		candidate < len(tree.nodes) && depth < javascriptMaximumSyntaxUnwrapDepth; depth++ {
		if candidate == parametersIndex {
			return false
		}
		if tree.nodes[candidate].kind == "decorator" {
			parameterIndex := tree.nodes[candidate].parent
			if parameterIndex >= 0 && parameterIndex < len(tree.nodes) &&
				(tree.nodes[parameterIndex].kind == "required_parameter" ||
					tree.nodes[parameterIndex].kind == "optional_parameter") &&
				tree.nodes[parameterIndex].parent == parametersIndex {
				return true
			}
		}
		candidate = tree.nodes[candidate].parent
	}
	return false
}

func normalizeJavaScriptLineSpans(spans []javascriptLineSpan) []javascriptLineSpan {
	sort.Slice(spans, func(first, second int) bool {
		if spans[first].start != spans[second].start {
			return spans[first].start < spans[second].start
		}
		return spans[first].end < spans[second].end
	})
	unique := spans[:0]
	for _, span := range spans {
		if span.start < 1 || span.end < span.start {
			continue
		}
		if len(unique) == 0 || unique[len(unique)-1] != span {
			unique = append(unique, span)
		}
	}
	return unique
}

func (j javascriptLanguage) importRange(lines []string) (int, int, bool) {
	analysis := j.sourceAnalysis(lines)
	if analysis == nil {
		return 0, 0, false
	}
	start, end := 0, 0
	for _, statement := range analysis.imports {
		if statement.start < 1 || statement.end < statement.start || statement.end > len(lines) {
			continue
		}
		if start == 0 || statement.start < start {
			start = statement.start
		}
		end = max(end, statement.end)
	}
	return start, end, start > 0 && end >= start
}

func (j javascriptLanguage) cleanSource(
	source string,
	dropComments, _ bool,
) string {
	if !dropComments {
		return source
	}
	lines := strings.Split(source, "\n")
	return dropBlankArtifactLines(strings.Join(j.cleanSourceLines(lines, true, false), "\n"))
}

func (j javascriptLanguage) cleanSourceLines(
	lines []string,
	dropComments, _ bool,
) []string {
	if len(lines) == 0 {
		return nil
	}
	if !dropComments {
		return append([]string(nil), lines...)
	}
	analysis := j.sourceAnalysis(lines)
	if analysis == nil {
		return append([]string(nil), lines...)
	}
	masked := strings.Split(maskJavaScriptSource(analysis.source, analysis.commentSpans), "\n")
	for index := range masked {
		masked[index] = strings.TrimRight(masked[index], " \t")
	}
	return masked
}

func (javascriptLanguage) finalizeSourceSnippet(
	source string,
	dropComments, _ bool,
) string {
	if !dropComments {
		return source
	}
	return dropBlankArtifactLines(source)
}

func (j javascriptLanguage) ignoredSearchLines(
	lines []string,
	dropComments, _ bool,
) map[int]bool {
	ignored := make(map[int]bool)
	if len(lines) == 0 || !dropComments {
		return ignored
	}
	analysis := j.sourceAnalysis(lines)
	if analysis == nil {
		return ignored
	}
	masked := strings.Split(maskJavaScriptSource(analysis.source, analysis.commentSpans), "\n")
	for index := range lines {
		if index < len(masked) && masked[index] != lines[index] &&
			strings.TrimSpace(masked[index]) == "" {
			ignored[index+1] = true
		}
	}
	return ignored
}

func (j javascriptLanguage) searchLines(
	lines []string,
	noComments, noStrings bool,
) []string {
	if len(lines) == 0 {
		return nil
	}
	if !noComments && !noStrings {
		return append([]string(nil), lines...)
	}
	analysis := j.sourceAnalysis(lines)
	if analysis == nil {
		return append([]string(nil), lines...)
	}
	spans := make([]javascriptByteSpan, 0, len(analysis.opaqueSpans))
	if noComments {
		spans = append(spans, analysis.commentSpans...)
	}
	if noStrings {
		spans = append(spans, analysis.stringSpans...)
	}
	masked := []byte(maskJavaScriptSource(analysis.source, spans))
	if noStrings {
		for _, definition := range analysis.definitions {
			if definition.line < 1 || definition.line > len(analysis.lineStarts) ||
				definition.column < 1 {
				continue
			}
			start := analysis.lineStarts[definition.line-1] + definition.column - 1
			end := start + len(definition.symbol)
			if start < 0 || end > len(analysis.source) ||
				analysis.source[start:end] != definition.symbol ||
				!javascriptByteRangeExcluded(start, end, analysis.stringSpans) {
				continue
			}
			copy(masked[start:end], analysis.source[start:end])
		}
	}
	return strings.Split(string(masked), "\n")
}

func (j javascriptLanguage) stripComment(line string) string {
	tree, _ := parseJavaScriptSyntaxFlavor(line, j.flavor)
	comments, _ := javascriptSyntaxMasks(line, tree)
	if tree == nil {
		comments = scanJavaScriptFallbackFlavor(line, j.flavor).comments
	}
	return strings.TrimRight(maskJavaScriptSource(line, comments), " \t")
}

func (javascriptLanguage) countSymbolOccurrences(line, symbol string) int {
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
		before, _ := utf8.DecodeLastRuneInString(line[:position])
		beforeOK := position == 0 || !javascriptOccurrenceBoundaryRune(before)
		afterIndex := position + len(symbol)
		after, _ := utf8.DecodeRuneInString(line[afterIndex:])
		afterOK := afterIndex >= len(line) || !javascriptOccurrenceBoundaryRune(after)
		if beforeOK && afterOK {
			count++
		}
		_, size := utf8.DecodeRuneInString(line[position:])
		if size < 1 {
			size = 1
		}
		offset = position + size
	}
	return count
}

func javascriptOccurrenceBoundaryRune(r rune) bool {
	return javascriptIdentifierContinueRune(r) || r == '#' || r == '\\'
}

func (j javascriptLanguage) symbolOnLine(lines []string, lineNo int) (string, bool) {
	if lineNo < 1 || lineNo > len(lines) {
		return "", false
	}
	analysis := j.sourceAnalysis(lines)
	if analysis == nil {
		return "", false
	}
	for _, definition := range analysis.definitions {
		if definition.line == lineNo {
			return definition.symbol, true
		}
	}
	if symbol, found := javascriptTreeSymbolOnLine(
		analysis.source,
		analysis.tree,
		analysis.opaqueSpans,
		lineNo,
	); found {
		return symbol, true
	}
	return javascriptLexicalSymbolOnLine(analysis, lineNo)
}

func javascriptLexicalSymbolOnLine(
	analysis *javascriptSourceAnalysis,
	lineNo int,
) (string, bool) {
	if analysis == nil || lineNo < 1 || lineNo > analysis.lineCount {
		return "", false
	}
	tokens := analysis.lexed.tokens
	positions := javascriptSourcePositions{
		source: analysis.source, lineStarts: analysis.lineStarts,
	}
	bestName := -1
	bestCallEnd := -1
	for index, token := range tokens {
		line, _ := positions.lineColumn(token.startOffset())
		if line != lineNo || !javascriptSourceName(token.text) {
			continue
		}
		memberName := index > 0 &&
			(tokens[index-1].text == "." || tokens[index-1].text == "?.")
		if !memberName && javascriptHardKeywords[javascriptDecodedIdentifier(token.text)] {
			continue
		}
		open := javascriptLexicalCallOpen(tokens, index, analysis.lexed.delimiters)
		if open < 0 {
			continue
		}
		callEnd := open
		if closeIndex, ok := analysis.lexed.delimiters.get(open); ok && closeIndex > open {
			callEnd = closeIndex
		}
		if callEnd > bestCallEnd {
			bestName, bestCallEnd = index, callEnd
		}
	}
	if bestName >= 0 {
		return tokens[bestName].text, true
	}
	for index, token := range tokens {
		line, _ := positions.lineColumn(token.startOffset())
		if line != lineNo || !javascriptSourceName(token.text) {
			continue
		}
		if index > 0 && (tokens[index-1].text == "." || tokens[index-1].text == "?.") {
			return token.text, true
		}
	}
	for _, token := range tokens {
		line, _ := positions.lineColumn(token.startOffset())
		if line == lineNo && javascriptSourceName(token.text) &&
			!javascriptHardKeywords[javascriptDecodedIdentifier(token.text)] {
			return token.text, true
		}
	}
	return "", false
}

func javascriptLexicalCallOpen(
	tokens []javascriptToken,
	nameIndex int,
	delimiters javascriptDelimiterPairs,
) int {
	if nameIndex < 0 || nameIndex >= len(tokens) {
		return -1
	}
	cursor := nameIndex + 1
	if cursor < len(tokens) && tokens[cursor].text == "[" {
		closeIndex, ok := delimiters.get(cursor)
		if !ok || closeIndex <= cursor {
			return -1
		}
		cursor = closeIndex + 1
	}
	if cursor < len(tokens) && tokens[cursor].text == "?." {
		cursor++
	}
	if cursor < len(tokens) && tokens[cursor].text == "(" {
		return cursor
	}
	return -1
}

func javascriptTreeSymbolOnLine(
	source string,
	tree *javascriptSyntaxTree,
	excluded []javascriptByteSpan,
	lineNo int,
) (string, bool) {
	if tree == nil {
		return "", false
	}
	positions := javascriptSourcePositions{source: source, lineStarts: javascriptLineStarts(source)}
	errorContext := javascriptSyntaxErrorContexts(tree)
	for nodeIndex, node := range tree.nodes {
		if node.kind != "call_expression" && node.kind != "new_expression" {
			continue
		}
		if nodeIndex < len(errorContext) && errorContext[nodeIndex] {
			continue
		}
		identifierIndex := javascriptCalledIdentifierNode(tree, nodeIndex, 0)
		if javascriptIdentifierNodeOnLine(
			source, tree, identifierIndex, positions, excluded, lineNo,
		) {
			return javascriptNodeText(source, tree, identifierIndex), true
		}
	}
	for nodeIndex, node := range tree.nodes {
		if node.kind != "member_expression" && node.kind != "subscript_expression" {
			continue
		}
		if nodeIndex < len(errorContext) && errorContext[nodeIndex] {
			continue
		}
		identifierIndex := javascriptMemberIdentifierNode(tree, nodeIndex)
		if javascriptIdentifierNodeOnLine(
			source, tree, identifierIndex, positions, excluded, lineNo,
		) {
			return javascriptNodeText(source, tree, identifierIndex), true
		}
	}
	for nodeIndex, node := range tree.nodes {
		if !javascriptIdentifierNodeKind(node.kind) ||
			(nodeIndex < len(errorContext) && errorContext[nodeIndex]) ||
			!javascriptIdentifierNodeOnLine(
				source, tree, nodeIndex, positions, excluded, lineNo,
			) {
			continue
		}
		symbol := javascriptNodeText(source, tree, nodeIndex)
		if javascriptInspectSymbolCandidate(symbol, node.kind) {
			return symbol, true
		}
	}
	return "", false
}

func javascriptCalledIdentifierNode(
	tree *javascriptSyntaxTree,
	nodeIndex, depth int,
) int {
	if tree == nil || nodeIndex < 0 || nodeIndex >= len(tree.nodes) ||
		depth > javascriptMaximumSyntaxUnwrapDepth {
		return -1
	}
	for _, childIndex := range tree.nodes[nodeIndex].children {
		childKind := tree.nodes[childIndex].kind
		if childKind == "arguments" || childKind == "new" || childKind == "optional_chain" {
			continue
		}
		return javascriptUnwrapExpressionIdentifier(tree, childIndex, depth+1)
	}
	return -1
}

func javascriptUnwrapExpressionIdentifier(
	tree *javascriptSyntaxTree,
	nodeIndex, depth int,
) int {
	if tree == nil || nodeIndex < 0 || nodeIndex >= len(tree.nodes) ||
		depth > javascriptMaximumSyntaxUnwrapDepth {
		return -1
	}
	node := tree.nodes[nodeIndex]
	if javascriptIdentifierNodeKind(node.kind) {
		return nodeIndex
	}
	switch node.kind {
	case "member_expression", "subscript_expression":
		return javascriptMemberIdentifierNode(tree, nodeIndex)
	case "call_expression", "new_expression":
		return javascriptCalledIdentifierNode(tree, nodeIndex, depth+1)
	case "parenthesized_expression", "await_expression":
		for _, childIndex := range node.children {
			if identifierIndex := javascriptUnwrapExpressionIdentifier(
				tree, childIndex, depth+1,
			); identifierIndex >= 0 {
				return identifierIndex
			}
		}
	}
	return -1
}

func javascriptMemberIdentifierNode(tree *javascriptSyntaxTree, nodeIndex int) int {
	if tree == nil || nodeIndex < 0 || nodeIndex >= len(tree.nodes) {
		return -1
	}
	node := tree.nodes[nodeIndex]
	if node.kind == "subscript_expression" {
		for _, childIndex := range node.children {
			childKind := tree.nodes[childIndex].kind
			if childKind == "[" || childKind == "]" {
				continue
			}
			if identifier := javascriptUnwrapExpressionIdentifier(tree, childIndex, 1); identifier >= 0 {
				return identifier
			}
		}
		return -1
	}
	for index := len(node.children) - 1; index >= 0; index-- {
		childIndex := node.children[index]
		if javascriptIdentifierNodeKind(tree.nodes[childIndex].kind) {
			return childIndex
		}
	}
	return -1
}

func javascriptIdentifierNodeKind(kind string) bool {
	switch kind {
	case "identifier", "property_identifier", "private_property_identifier",
		"shorthand_property_identifier", "shorthand_property_identifier_pattern",
		"type_identifier":
		return true
	default:
		return false
	}
}

func javascriptIdentifierNodeOnLine(
	source string,
	tree *javascriptSyntaxTree,
	nodeIndex int,
	positions javascriptSourcePositions,
	excluded []javascriptByteSpan,
	lineNo int,
) bool {
	if tree == nil || nodeIndex < 0 || nodeIndex >= len(tree.nodes) {
		return false
	}
	node := tree.nodes[nodeIndex]
	if !javascriptIdentifierNodeKind(node.kind) ||
		javascriptByteRangeExcluded(node.startByte, node.endByte, excluded) {
		return false
	}
	line, _ := positions.lineColumn(node.startByte)
	return line == lineNo && javascriptNodeText(source, tree, nodeIndex) != ""
}

func javascriptNodeText(source string, tree *javascriptSyntaxTree, nodeIndex int) string {
	if tree == nil || nodeIndex < 0 || nodeIndex >= len(tree.nodes) {
		return ""
	}
	node := tree.nodes[nodeIndex]
	if node.startByte < 0 || node.endByte > len(source) || node.startByte >= node.endByte {
		return ""
	}
	return source[node.startByte:node.endByte]
}

func javascriptInspectSymbolCandidate(symbol, nodeKind string) bool {
	if !javascriptSourceName(symbol) {
		return false
	}
	if nodeKind == "property_identifier" || nodeKind == "private_property_identifier" {
		return true
	}
	return !javascriptHardKeywords[javascriptDecodedIdentifier(symbol)]
}

func javascriptDecodedIdentifier(identifier string) string {
	identifier = strings.TrimPrefix(identifier, "#")
	if !strings.Contains(identifier, `\`) {
		return identifier
	}
	var decoded strings.Builder
	decoded.Grow(len(identifier))
	for offset := 0; offset < len(identifier); {
		r, size, ok := javascriptIdentifierRune(identifier, offset)
		if !ok || size < 1 {
			return identifier
		}
		decoded.WriteRune(r)
		offset += size
	}
	return decoded.String()
}

var javascriptHardKeywords = map[string]bool{
	"await": true, "break": true, "case": true, "catch": true, "class": true,
	"const": true, "continue": true, "debugger": true, "default": true, "delete": true,
	"do": true, "else": true, "export": true, "extends": true, "false": true,
	"finally": true, "for": true, "function": true, "if": true, "import": true,
	"in": true, "instanceof": true, "let": true, "new": true, "null": true,
	"return": true, "super": true, "switch": true, "this": true, "throw": true,
	"true": true, "try": true, "typeof": true, "var": true, "void": true,
	"while": true, "with": true, "yield": true,
}
