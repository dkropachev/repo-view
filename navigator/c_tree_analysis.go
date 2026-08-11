package navigator

import (
	"sort"
	"strings"
)

const (
	cMaximumDeclaratorUnwrapDepth      = 64
	cMaximumDeclaratorUnwrapOperations = 256
	cMaximumAttachedScopeDepth         = 64
)

type cSourcePositions struct {
	source     string
	lineStarts []int
}

type cDeclaratorName struct {
	bindingKind string
	node        int
	root        int
}

type cTreeDefinitionIdentity struct {
	symbol string
	line   int
	column int
}

// cTreeDefinitions extracts declarations that are useful C navigation
// targets. Ordinary block variables and parameters are intentionally omitted;
// local typedefs, local tags, and GNU nested functions remain definitions.
func cTreeDefinitions(
	source string,
	lineCount int,
	tree *cSyntaxTree,
) []sourceDefinition {
	if lineCount < 1 || !validateCSyntaxTree(tree, len(source)) {
		return nil
	}

	errorContext := cSyntaxErrorContexts(tree)
	recoveryDescendant := cSyntaxErrorDescendants(tree)
	recoveryBeforeEnumerator, _ := cSyntaxRecoveryBeforeNextSiblingFlags(
		tree, "enumerator",
	)
	attachedStarts := cSyntaxAttachedStarts(source, tree)
	positions := cSourcePositions{
		source:     source,
		lineStarts: cTreeLineStarts(source),
	}
	definitions := make([]sourceDefinition, 0)

	appendDefinition := func(
		nameIndex, scopeIndex int,
		ownsScope, attachDocumentation bool,
	) {
		if !cValidSyntaxNodeIndex(tree, nameIndex) ||
			!cValidSyntaxNodeIndex(tree, scopeIndex) {
			return
		}
		name := tree.nodes[nameIndex]
		if !cTreeIdentifierSourceName(source, name.startByte, name.endByte) {
			return
		}
		line, column := positions.lineColumn(name.startByte)
		scope := tree.nodes[scopeIndex]
		startOffset := scope.startByte
		if ownsScope && attachDocumentation {
			startOffset = cSyntaxAttachedScopeStart(
				tree, scopeIndex, attachedStarts,
			)
		}
		scopeStart, scopeEnd := positions.lineSpan(startOffset, scope.endByte)
		ownedEndLine, ownedEndColumn := positions.lineColumn(scope.endByte)
		if !ownsScope || ownedEndLine != scopeEnd {
			ownedEndColumn = 0
		}
		definitions = append(definitions, sourceDefinition{
			symbol:         source[name.startByte:name.endByte],
			line:           line,
			column:         column,
			scopeStart:     scopeStart,
			scopeEnd:       scopeEnd,
			ownedEndColumn: ownedEndColumn,
			ownsScope:      ownsScope,
		})
	}

	for nodeIndex, node := range tree.nodes {
		if cSyntaxNodeInError(errorContext, nodeIndex) {
			continue
		}

		switch node.kind {
		case "function_definition":
			if !cFunctionDefinitionContextValid(tree, nodeIndex) ||
				cSyntaxHasDirectRecovery(tree, nodeIndex) {
				continue
			}
			for _, declarator := range cDirectDeclaratorNames(
				tree, nodeIndex, "identifier",
			) {
				if cSyntaxNodeFlagged(recoveryDescendant, declarator.root) {
					continue
				}
				appendDefinition(declarator.node, nodeIndex, true, true)
				break
			}

		case "declaration":
			if cSyntaxHasDirectRecovery(tree, nodeIndex) {
				continue
			}
			if parent := node.parent; cValidSyntaxNodeIndex(tree, parent) &&
				tree.nodes[parent].kind == "function_definition" {
				// Old-style function definitions carry K&R parameter
				// declarations directly before their compound body.
				continue
			}
			fileScope := cDeclarationAtFileScope(tree, nodeIndex)
			anonymousAggregate := -1
			if fileScope {
				anonymousAggregate = cDirectAnonymousAggregateBody(tree, nodeIndex)
			}
			for _, declarator := range cDirectDeclaratorNames(
				tree, nodeIndex, "identifier",
			) {
				if cSyntaxNodeFlagged(recoveryDescendant, declarator.root) {
					continue
				}
				// Block-scope function declarations are useful prototypes.
				// All other block declarators are ordinary locals, including
				// static/extern objects and function-pointer variables.
				if !fileScope && declarator.bindingKind != "function_declarator" {
					continue
				}
				ownsScope := anonymousAggregate >= 0
				appendDefinition(
					declarator.node, nodeIndex, ownsScope, ownsScope,
				)
			}

		case "type_definition":
			if !cTypeDefinitionContextValid(tree, nodeIndex) ||
				cSyntaxHasDirectRecovery(tree, nodeIndex) {
				continue
			}
			anonymousAggregate := cDirectAnonymousAggregateBody(tree, nodeIndex)
			for _, declarator := range cTypeDefinitionDeclaratorNames(
				tree, nodeIndex,
			) {
				if cSyntaxNodeFlagged(recoveryDescendant, declarator.root) {
					continue
				}
				ownsScope := anonymousAggregate >= 0
				appendDefinition(
					declarator.node, nodeIndex, ownsScope, ownsScope,
				)
			}

		case "struct_specifier", "union_specifier", "enum_specifier":
			if cSyntaxHasDirectRecovery(tree, nodeIndex) {
				continue
			}
			bodyIndex := cAggregateBodyNode(tree, nodeIndex)
			if bodyIndex < 0 && !cStandaloneTagDeclaration(tree, nodeIndex) {
				continue
			}
			nameIndex := cDirectChildOfKind(tree, nodeIndex, "type_identifier")
			if nameIndex >= 0 {
				appendDefinition(
					nameIndex, nodeIndex, bodyIndex >= 0, bodyIndex >= 0,
				)
			}

		case "enumerator":
			if cSyntaxHasDirectRecovery(tree, nodeIndex) ||
				cSyntaxNodeFlagged(recoveryBeforeEnumerator, nodeIndex) {
				continue
			}
			nameIndex := cDirectChildOfKind(tree, nodeIndex, "identifier")
			if nameIndex >= 0 {
				appendDefinition(nameIndex, nodeIndex, false, false)
			}

		case "field_declaration":
			if cSyntaxHasDirectRecovery(tree, nodeIndex) {
				continue
			}
			anonymousAggregate := cDirectAnonymousAggregateBody(tree, nodeIndex)
			for _, declarator := range cDirectDeclaratorNames(
				tree, nodeIndex, "field_identifier",
			) {
				if cSyntaxNodeFlagged(recoveryDescendant, declarator.root) {
					continue
				}
				ownsScope := anonymousAggregate >= 0
				appendDefinition(
					declarator.node, nodeIndex, ownsScope, ownsScope,
				)
			}

		case "preproc_def", "preproc_function_def":
			if cSyntaxHasDirectRecovery(tree, nodeIndex) {
				continue
			}
			nameIndex := cDirectChildOfKind(tree, nodeIndex, "identifier")
			if nameIndex < 0 {
				continue
			}
			startLine, endLine := positions.lineSpan(node.startByte, node.endByte)
			appendDefinition(
				nameIndex, nodeIndex, endLine > startLine, false,
			)
		}
	}

	return cSortUniqueTreeDefinitions(definitions, lineCount)
}

func cDirectDeclaratorNames(
	tree *cSyntaxTree,
	nodeIndex int,
	leafKind string,
) []cDeclaratorName {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return nil
	}
	names := make([]cDeclaratorName, 0)
	for _, childIndex := range tree.nodes[nodeIndex].children {
		if !cValidSyntaxNodeIndex(tree, childIndex) ||
			!cDeclaratorCanContainLeaf(tree.nodes[childIndex].kind, leafKind) {
			continue
		}
		if name, ok := cUnwrapDeclaratorName(tree, childIndex, leafKind); ok {
			names = append(names, name)
		}
	}
	return names
}

func cTypeDefinitionDeclaratorNames(
	tree *cSyntaxTree,
	nodeIndex int,
) []cDeclaratorName {
	names := cDirectDeclaratorNames(tree, nodeIndex, "type_identifier")
	if len(names) == 0 {
		return nil
	}

	// In 'typedef Existing Alias', the underlying named type and the aliases
	// are all direct type_identifier children because the safe shared adapter
	// intentionally does not retain field labels. An explicit type specifier
	// removes that ambiguity; otherwise the first bare type_identifier is the
	// underlying type rather than a declared alias.
	if !cTypeDefinitionHasExplicitTypeSpecifier(tree, nodeIndex) {
		for nameIndex, name := range names {
			if tree.nodes[name.node].parent == nodeIndex {
				return append(names[:nameIndex], names[nameIndex+1:]...)
			}
		}
	}
	return names
}

func cTypeDefinitionHasExplicitTypeSpecifier(
	tree *cSyntaxTree,
	nodeIndex int,
) bool {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return false
	}
	for _, childIndex := range tree.nodes[nodeIndex].children {
		if !cValidSyntaxNodeIndex(tree, childIndex) {
			continue
		}
		switch tree.nodes[childIndex].kind {
		case "primitive_type", "sized_type_specifier", "struct_specifier",
			"union_specifier", "enum_specifier", "macro_type_specifier":
			return true
		}
	}
	return false
}

func cUnwrapDeclaratorName(
	tree *cSyntaxTree,
	nodeIndex int,
	leafKind string,
) (cDeclaratorName, bool) {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return cDeclaratorName{}, false
	}
	current := nodeIndex
	root := nodeIndex
	bindingKind := ""
	operations := 0

	for range cMaximumDeclaratorUnwrapDepth {
		if !cValidSyntaxNodeIndex(tree, current) {
			return cDeclaratorName{}, false
		}
		node := tree.nodes[current]
		if node.kind == leafKind {
			if node.startByte >= node.endByte {
				return cDeclaratorName{}, false
			}
			return cDeclaratorName{
				node: current, root: root, bindingKind: bindingKind,
			}, true
		}
		if !cDeclaratorCanContainLeaf(node.kind, leafKind) {
			return cDeclaratorName{}, false
		}
		if node.kind != "init_declarator" &&
			cSyntaxHasDirectRecovery(tree, current) {
			return cDeclaratorName{}, false
		}
		switch node.kind {
		case "pointer_declarator", "function_declarator", "array_declarator":
			// Traversal is outside-in, so the final recorded constructor is
			// nearest the declared identifier. It distinguishes 'f()' from
			// '(*f)()' without implementing the C declarator grammar again.
			bindingKind = node.kind
		}

		next := -1
		for _, childIndex := range node.children {
			operations++
			if operations > cMaximumDeclaratorUnwrapOperations {
				return cDeclaratorName{}, false
			}
			if !cValidSyntaxNodeIndex(tree, childIndex) {
				return cDeclaratorName{}, false
			}
			if cDeclaratorCanContainLeaf(tree.nodes[childIndex].kind, leafKind) {
				// The declarator precedes an initializer or array bound. Those
				// expressions can contain identifiers too, so only the first
				// eligible direct child is the structural declarator path.
				next = childIndex
				break
			}
		}
		if next < 0 || next == current {
			return cDeclaratorName{}, false
		}
		current = next
	}
	return cDeclaratorName{}, false
}

func cDeclaratorCanContainLeaf(kind, leafKind string) bool {
	if kind == leafKind {
		return true
	}
	switch kind {
	case "init_declarator", "parenthesized_declarator",
		"attributed_declarator", "pointer_declarator",
		"function_declarator", "array_declarator":
		return true
	default:
		return false
	}
}

func cDirectAnonymousAggregateBody(tree *cSyntaxTree, nodeIndex int) int {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return -1
	}
	for _, childIndex := range tree.nodes[nodeIndex].children {
		if !cValidSyntaxNodeIndex(tree, childIndex) {
			continue
		}
		switch tree.nodes[childIndex].kind {
		case "struct_specifier", "union_specifier", "enum_specifier":
			if cAggregateBodyNode(tree, childIndex) >= 0 &&
				cDirectChildOfKind(tree, childIndex, "type_identifier") < 0 {
				return childIndex
			}
		}
	}
	return -1
}

func cAggregateBodyNode(tree *cSyntaxTree, nodeIndex int) int {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return -1
	}
	switch tree.nodes[nodeIndex].kind {
	case "struct_specifier", "union_specifier":
		return cDirectChildOfKind(tree, nodeIndex, "field_declaration_list")
	case "enum_specifier":
		return cDirectChildOfKind(tree, nodeIndex, "enumerator_list")
	default:
		return -1
	}
}

func cFunctionDefinitionContextValid(tree *cSyntaxTree, nodeIndex int) bool {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return false
	}
	for parent := tree.nodes[nodeIndex].parent; parent >= 0; {
		if !cValidSyntaxNodeIndex(tree, parent) {
			return false
		}
		switch tree.nodes[parent].kind {
		case "field_declaration", "field_declaration_list", "enumerator_list",
			"parameter_declaration", "parameter_list", "type_descriptor":
			return false
		case "compound_statement", "translation_unit":
			return true
		case "ERROR":
			if !cSyntaxWholeFileErrorRoot(tree, parent) {
				return false
			}
		}
		next := tree.nodes[parent].parent
		if next < 0 {
			return true
		}
		parent = next
	}
	return true
}

func cDeclarationAtFileScope(tree *cSyntaxTree, nodeIndex int) bool {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return false
	}
	for parent := tree.nodes[nodeIndex].parent; parent >= 0; {
		if !cValidSyntaxNodeIndex(tree, parent) {
			return false
		}
		switch tree.nodes[parent].kind {
		case "translation_unit":
			return true
		case "compound_statement", "function_definition", "field_declaration",
			"field_declaration_list", "enumerator_list", "parameter_declaration",
			"parameter_list", "type_descriptor":
			return false
		case "ERROR":
			if !cSyntaxWholeFileErrorRoot(tree, parent) {
				return false
			}
		}
		next := tree.nodes[parent].parent
		if next < 0 {
			return true
		}
		parent = next
	}
	return true
}

func cTypeDefinitionContextValid(tree *cSyntaxTree, nodeIndex int) bool {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return false
	}
	for parent := tree.nodes[nodeIndex].parent; parent >= 0; {
		if !cValidSyntaxNodeIndex(tree, parent) {
			return false
		}
		switch tree.nodes[parent].kind {
		case "field_declaration", "field_declaration_list", "enumerator_list",
			"parameter_declaration", "parameter_list", "type_descriptor":
			return false
		case "translation_unit", "compound_statement":
			return true
		case "ERROR":
			if !cSyntaxWholeFileErrorRoot(tree, parent) {
				return false
			}
		}
		next := tree.nodes[parent].parent
		if next < 0 {
			return true
		}
		parent = next
	}
	return true
}

func cStandaloneTagDeclaration(tree *cSyntaxTree, nodeIndex int) bool {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return false
	}
	parent := tree.nodes[nodeIndex].parent
	if parent < 0 {
		return true
	}
	if !cValidSyntaxNodeIndex(tree, parent) {
		return false
	}
	switch tree.nodes[parent].kind {
	case "translation_unit", "compound_statement", "declaration_list",
		"linkage_specification", "preproc_if", "preproc_ifdef",
		"preproc_else", "preproc_elif", "preproc_elifdef":
		return true
	case "declaration":
		// Some grammar recovery paths wrap a tag-only forward declaration
		// in declaration. A real declarator makes the tag a type use rather
		// than the standalone navigation target handled here.
		return !cHasDirectDeclaratorRoot(tree, parent, "identifier")
	case "ERROR":
		return cSyntaxWholeFileErrorRoot(tree, parent)
	default:
		return false
	}
}

func cHasDirectDeclaratorRoot(
	tree *cSyntaxTree,
	nodeIndex int,
	leafKind string,
) bool {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return false
	}
	for _, childIndex := range tree.nodes[nodeIndex].children {
		if cValidSyntaxNodeIndex(tree, childIndex) &&
			cDeclaratorCanContainLeaf(tree.nodes[childIndex].kind, leafKind) {
			return true
		}
	}
	return false
}

// cTreeScopes returns concrete navigation ranges for declaration bodies,
// control statements, standalone compounds, preprocessor branches, and SEH
// extension statements.
func cTreeScopes(source string, tree *cSyntaxTree) []cLineScope {
	if !validateCSyntaxTree(tree, len(source)) {
		return nil
	}
	lineCount := len(cTreeLineStarts(source))
	positions := cSourcePositions{
		source:     source,
		lineStarts: cTreeLineStarts(source),
	}
	errorContext := cSyntaxErrorContexts(tree)
	attachedStarts := cSyntaxAttachedStarts(source, tree)
	scopes := make([]cLineScope, 0)

	for nodeIndex, node := range tree.nodes {
		if cSyntaxNodeInError(errorContext, nodeIndex) ||
			!cTreeScopeNode(tree, nodeIndex, positions) {
			continue
		}
		if node.kind == "compound_statement" &&
			cCompoundScopeOwnedByParent(tree, nodeIndex) {
			continue
		}
		startOffset := node.startByte
		if node.kind == "function_definition" ||
			cAggregateBodyNode(tree, nodeIndex) >= 0 {
			startOffset = cSyntaxAttachedScopeStart(
				tree, nodeIndex, attachedStarts,
			)
		}
		start, end := positions.lineSpan(startOffset, node.endByte)
		scopes = append(scopes, cLineScope{start: start, end: end})
	}
	return cNormalizeTreeLineScopes(scopes, lineCount)
}

func cTreeScopeNode(
	tree *cSyntaxTree,
	nodeIndex int,
	positions cSourcePositions,
) bool {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return false
	}
	if cSyntaxHasDirectRecovery(tree, nodeIndex) {
		return false
	}
	node := tree.nodes[nodeIndex]
	switch node.kind {
	case "function_definition":
		return cFunctionDefinitionContextValid(tree, nodeIndex)
	case "struct_specifier", "union_specifier", "enum_specifier":
		return cAggregateBodyNode(tree, nodeIndex) >= 0
	case "preproc_def", "preproc_function_def":
		start, end := positions.lineSpan(node.startByte, node.endByte)
		return end > start
	case "compound_statement", "if_statement", "else_clause",
		"switch_statement", "case_statement", "while_statement",
		"do_statement", "for_statement", "preproc_if", "preproc_ifdef",
		"preproc_else", "preproc_elif", "preproc_elifdef",
		"seh_try_statement", "seh_except_clause", "seh_finally_clause":
		return true
	default:
		return false
	}
}

func cCompoundScopeOwnedByParent(tree *cSyntaxTree, nodeIndex int) bool {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return false
	}
	node := tree.nodes[nodeIndex]
	parent := node.parent
	if !cValidSyntaxNodeIndex(tree, parent) {
		return false
	}
	parentNode := tree.nodes[parent]
	if parentNode.kind == "function_definition" {
		return true
	}
	if node.endByte != parentNode.endByte {
		return false
	}
	switch parentNode.kind {
	case "if_statement", "else_clause", "switch_statement",
		"case_statement", "while_statement", "for_statement",
		"seh_try_statement", "seh_except_clause", "seh_finally_clause":
		return true
	default:
		return false
	}
}

// cTreeImports returns every concrete #include span, including includes nested
// in preprocessor conditionals. GNU #include_next is recovered by the lexical
// pass because the pinned grammar exposes it only as preproc_call.
func cTreeImports(source string, tree *cSyntaxTree) []cLineSpan {
	if !validateCSyntaxTree(tree, len(source)) {
		return nil
	}
	lineCount := len(cTreeLineStarts(source))
	positions := cSourcePositions{
		source:     source,
		lineStarts: cTreeLineStarts(source),
	}
	errorContext := cSyntaxErrorContexts(tree)
	errorDescendant := cSyntaxErrorDescendants(tree)
	imports := make([]cLineSpan, 0)

	for nodeIndex, node := range tree.nodes {
		if node.kind != "preproc_include" ||
			cSyntaxNodeInError(errorContext, nodeIndex) ||
			nodeIndex >= len(errorDescendant) || errorDescendant[nodeIndex] {
			continue
		}
		pathIndex := cDirectChildOfKinds(
			tree, nodeIndex, "system_lib_string", "string_literal", "identifier",
			"call_expression",
		)
		if !cValidSyntaxNodeIndex(tree, pathIndex) {
			continue
		}
		path := tree.nodes[pathIndex]
		if path.startByte >= path.endByte {
			continue
		}
		start, end := positions.lineSpan(node.startByte, node.endByte)
		imports = append(imports, cLineSpan{start: start, end: end})
	}
	return cNormalizeTreeLineSpans(imports, lineCount)
}

func cSyntaxErrorContexts(tree *cSyntaxTree) []bool {
	if tree == nil || len(tree.nodes) == 0 {
		return nil
	}
	rootRecoveryStart, rootHasRecovery := cSyntaxWholeRootRecoveryStart(tree)
	contexts := make([]bool, len(tree.nodes))
	for nodeIndex, node := range tree.nodes {
		ownError := node.kind == "ERROR" &&
			(len(tree.nodes) <= 1 || !cSyntaxWholeFileErrorRoot(tree, nodeIndex))
		afterRootRecovery := rootHasRecovery && nodeIndex != tree.root &&
			node.startByte >= rootRecoveryStart
		contexts[nodeIndex] = ownError || afterRootRecovery
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

func cSyntaxErrorDescendants(tree *cSyntaxTree) []bool {
	if tree == nil || len(tree.nodes) == 0 {
		return nil
	}
	descendants := make([]bool, len(tree.nodes))
	for nodeIndex := len(tree.nodes) - 1; nodeIndex >= 0; nodeIndex-- {
		node := tree.nodes[nodeIndex]
		if cSyntaxRecoveryNode(tree, nodeIndex) &&
			(len(tree.nodes) <= 1 || !cSyntaxWholeFileErrorRoot(tree, nodeIndex)) {
			descendants[nodeIndex] = true
		}
		if descendants[nodeIndex] && node.parent >= 0 &&
			node.parent < nodeIndex && node.parent < len(tree.nodes) {
			descendants[node.parent] = true
		}
	}
	return descendants
}

func cSyntaxErrorSpans(
	tree *cSyntaxTree,
	sourceLength int,
) []cByteSpan {
	if !validateCSyntaxTree(tree, sourceLength) {
		return nil
	}
	spans := make([]cByteSpan, 0)
	if start, ok := cSyntaxWholeRootRecoveryStart(tree); ok {
		root := tree.nodes[tree.root]
		end := max(start, min(root.endByte, sourceLength))
		if start < end {
			spans = append(spans, cByteSpan{start: start, end: end})
		}
	}
	for nodeIndex, node := range tree.nodes {
		if !cSyntaxRecoveryNode(tree, nodeIndex) ||
			len(tree.nodes) > 1 && cSyntaxWholeFileErrorRoot(tree, nodeIndex) {
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

func cSyntaxWholeRootRecoveryStart(tree *cSyntaxTree) (int, bool) {
	if tree == nil || len(tree.nodes) <= 1 ||
		!cSyntaxWholeFileErrorRoot(tree, tree.root) {
		return 0, false
	}
	start, found := 0, false
	record := func(candidate int) {
		if candidate < 0 {
			return
		}
		if !found || candidate < start {
			start, found = candidate, true
		}
	}
	for nodeIndex := range tree.nodes {
		if nodeIndex != tree.root && cSyntaxRecoveryNode(tree, nodeIndex) {
			record(tree.nodes[nodeIndex].startByte)
		}
	}
	for _, childIndex := range tree.nodes[tree.root].children {
		if !cValidSyntaxNodeIndex(tree, childIndex) {
			continue
		}
		if !cSyntaxWholeRootItemKind(tree.nodes[childIndex].kind) {
			record(tree.nodes[childIndex].startByte)
			break
		}
	}
	return start, found
}

func cSyntaxWholeRootItemKind(kind string) bool {
	switch kind {
	case "comment", ";", "declaration", "type_definition",
		"function_definition", "struct_specifier", "union_specifier",
		"enum_specifier", "linkage_specification", "attribute_declaration",
		"preproc_include", "preproc_def", "preproc_function_def",
		"preproc_call", "preproc_if", "preproc_ifdef":
		return true
	default:
		return false
	}
}

func cSyntaxRecoveryNode(tree *cSyntaxTree, nodeIndex int) bool {
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
	// The pinned grammar represents a valid unnamed bit-field as a missing
	// field_identifier beside its bitfield_clause. It is not parser recovery.
	if node.kind == "field_identifier" {
		parent := node.parent
		if cValidSyntaxNodeIndex(tree, parent) &&
			tree.nodes[parent].kind == "field_declaration" &&
			cDirectChildOfKind(tree, parent, "bitfield_clause") >= 0 {
			return false
		}
	}
	return true
}

func cSyntaxWholeFileErrorRoot(tree *cSyntaxTree, nodeIndex int) bool {
	return cValidSyntaxNodeIndex(tree, nodeIndex) && nodeIndex == tree.root &&
		tree.nodes[nodeIndex].parent < 0 &&
		tree.nodes[nodeIndex].kind == "ERROR" &&
		tree.nodes[nodeIndex].startByte == 0
}

func cSyntaxNodeInError(errorContext []bool, nodeIndex int) bool {
	return nodeIndex < 0 || nodeIndex >= len(errorContext) || errorContext[nodeIndex]
}

func cSyntaxNodeFlagged(flags []bool, nodeIndex int) bool {
	return nodeIndex < 0 || nodeIndex >= len(flags) || flags[nodeIndex]
}

func cSyntaxHasDirectRecovery(tree *cSyntaxTree, nodeIndex int) bool {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return true
	}
	for _, childIndex := range tree.nodes[nodeIndex].children {
		if !cValidSyntaxNodeIndex(tree, childIndex) ||
			cSyntaxRecoveryNode(tree, childIndex) {
			return true
		}
	}
	return false
}

// cSyntaxRecoveryBeforeNextSiblingFlags classifies every sibling of kind in
// one pass over the tree's child edges. A recovery node belongs to the
// preceding sibling's segment and stops mattering at the next sibling of kind.
// The visit count makes the linear frontier deterministic in resource tests.
func cSyntaxRecoveryBeforeNextSiblingFlags(
	tree *cSyntaxTree,
	siblingKind string,
) ([]bool, int) {
	if tree == nil || len(tree.nodes) == 0 || siblingKind == "" {
		return nil, 0
	}
	flags := make([]bool, len(tree.nodes))
	childVisits := 0
	for _, parent := range tree.nodes {
		previousSibling := -1
		recoveryAfterPrevious := false
		invalidSeen := false
		for _, siblingIndex := range parent.children {
			childVisits++
			if !cValidSyntaxNodeIndex(tree, siblingIndex) {
				// The validated parser tree cannot take this path. Preserve the
				// former conservative behavior for malformed synthetic trees: an
				// invalid earlier child poisons every later sibling lookup.
				invalidSeen = true
				recoveryAfterPrevious = true
				continue
			}
			if tree.nodes[siblingIndex].kind == siblingKind {
				if previousSibling >= 0 && recoveryAfterPrevious {
					flags[previousSibling] = true
				}
				if invalidSeen {
					flags[siblingIndex] = true
				}
				previousSibling = siblingIndex
				recoveryAfterPrevious = false
				continue
			}
			if cSyntaxRecoveryNode(tree, siblingIndex) {
				recoveryAfterPrevious = true
			}
		}
		if previousSibling >= 0 && recoveryAfterPrevious {
			flags[previousSibling] = true
		}
	}
	return flags, childVisits
}

func cDirectChildOfKind(tree *cSyntaxTree, nodeIndex int, kind string) int {
	return cDirectChildOfKinds(tree, nodeIndex, kind)
}

func cDirectChildOfKinds(
	tree *cSyntaxTree,
	nodeIndex int,
	kinds ...string,
) int {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return -1
	}
	for _, childIndex := range tree.nodes[nodeIndex].children {
		if !cValidSyntaxNodeIndex(tree, childIndex) {
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

func cValidSyntaxNodeIndex(tree *cSyntaxTree, nodeIndex int) bool {
	return tree != nil && nodeIndex >= 0 && nodeIndex < len(tree.nodes)
}

func cSyntaxAttachedStarts(source string, tree *cSyntaxTree) []int {
	if !validateCSyntaxTree(tree, len(source)) {
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
			adjacent := previousEnd >= 0 &&
				cDocumentationAttachmentGap(source, previousEnd, child.startByte)
			if previousEnd >= 0 && !adjacent {
				pendingStart = -1
			}

			if cForwardDocumentationComment(source, child) {
				if pendingStart < 0 {
					pendingStart = child.startByte
				}
				previousEnd = child.endByte
				continue
			}
			if child.kind == "comment" {
				// Ordinary comments are not declaration documentation.
				pendingStart = -1
				previousEnd = child.endByte
				continue
			}
			if pendingStart >= 0 && adjacent {
				starts[childIndex] = pendingStart
			}
			pendingStart = -1
			previousEnd = child.endByte
		}
	}
	return starts
}

func cSyntaxAttachedScopeStart(
	tree *cSyntaxTree,
	nodeIndex int,
	starts []int,
) int {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return 0
	}
	start := tree.nodes[nodeIndex].startByte
	current := nodeIndex
	for depth := 0; depth < cMaximumAttachedScopeDepth &&
		cValidSyntaxNodeIndex(tree, current); depth++ {
		if current < len(starts) && starts[current] >= 0 &&
			starts[current] <= tree.nodes[current].startByte {
			start = min(start, starts[current])
		}
		parent := tree.nodes[current].parent
		if !cValidSyntaxNodeIndex(tree, parent) {
			break
		}
		switch tree.nodes[parent].kind {
		case "declaration", "type_definition", "field_declaration",
			"attributed_statement":
			current = parent
		default:
			return start
		}
	}
	return start
}

func cDocumentationComment(source string, node cSyntaxNode) bool {
	if node.kind != "comment" || node.startByte < 0 ||
		node.endByte <= node.startByte || node.endByte > len(source) {
		return false
	}
	comment := source[node.startByte:node.endByte]
	return strings.HasPrefix(comment, "/**") ||
		strings.HasPrefix(comment, "/*!") ||
		strings.HasPrefix(comment, "///") ||
		strings.HasPrefix(comment, "//!")
}

func cForwardDocumentationComment(source string, node cSyntaxNode) bool {
	if !cDocumentationComment(source, node) {
		return false
	}
	comment := source[node.startByte:node.endByte]
	for _, prefix := range []string{"///<", "//!<", "/**<", "/*!<"} {
		if strings.HasPrefix(comment, prefix) {
			return false
		}
	}
	lineStart := node.startByte
	for lineStart > 0 {
		if previous := source[lineStart-1]; previous == '\n' || previous == '\r' {
			break
		}
		lineStart--
	}
	prefix := source[lineStart:node.startByte]
	if lineStart == 0 {
		prefix = strings.TrimPrefix(prefix, "\uFEFF")
	}
	return strings.TrimSpace(prefix) == ""
}

func cDocumentationAttachmentGap(source string, start, end int) bool {
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

func cTreeLineStarts(source string) []int {
	starts := []int{0}
	for offset := range len(source) {
		if source[offset] == '\n' {
			starts = append(starts, offset+1)
		}
	}
	return starts
}

func (positions cSourcePositions) lineColumn(offset int) (int, int) {
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

func (positions cSourcePositions) lineSpan(start, end int) (int, int) {
	startLine, _ := positions.lineColumn(start)
	endOffset := end
	if endOffset > start {
		endOffset--
	}
	endLine, _ := positions.lineColumn(endOffset)
	return startLine, max(startLine, endLine)
}

func cTreeIdentifierSourceName(source string, start, end int) bool {
	return start >= 0 && end > start && end <= len(source) &&
		cSourceIdentifier(source[start:end])
}

func cSortUniqueTreeDefinitions(
	definitions []sourceDefinition,
	lineCount int,
) []sourceDefinition {
	normalized := definitions[:0]
	for _, definition := range definitions {
		if definition.symbol == "" || definition.line < 1 ||
			definition.line > lineCount || definition.column < 1 {
			continue
		}
		if definition.scopeStart < 1 || definition.scopeStart > definition.line {
			definition.scopeStart = definition.line
		}
		definition.scopeEnd = min(
			lineCount, max(definition.line, definition.scopeEnd),
		)
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
		if (cTreeDefinitionIdentity{
			symbol: last.symbol, line: last.line, column: last.column,
		}) != (cTreeDefinitionIdentity{
			symbol: definition.symbol, line: definition.line, column: definition.column,
		}) {
			unique = append(unique, definition)
			continue
		}
		if definition.ownsScope && !last.ownsScope ||
			definition.ownsScope == last.ownsScope &&
				definition.scopeEnd-definition.scopeStart >
					last.scopeEnd-last.scopeStart {
			*last = definition
		}
	}
	return unique
}

func cNormalizeTreeLineScopes(
	scopes []cLineScope,
	lineCount int,
) []cLineScope {
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

func cNormalizeTreeLineSpans(
	spans []cLineSpan,
	lineCount int,
) []cLineSpan {
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

func cNormalizeSyntaxByteSpans(spans []cByteSpan) []cByteSpan {
	sort.Slice(spans, func(first, second int) bool {
		if spans[first].start != spans[second].start {
			return spans[first].start < spans[second].start
		}
		return spans[first].end < spans[second].end
	})
	normalized := spans[:0]
	for _, span := range spans {
		if span.end <= span.start {
			continue
		}
		if len(normalized) == 0 || span.start > normalized[len(normalized)-1].end {
			normalized = append(normalized, span)
			continue
		}
		normalized[len(normalized)-1].end = max(
			normalized[len(normalized)-1].end, span.end,
		)
	}
	return normalized
}
