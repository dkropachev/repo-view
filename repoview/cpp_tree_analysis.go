package repoview

import "strings"

const (
	cppMaximumDeclaratorDepth      = 64
	cppMaximumDeclaratorOperations = 256
	cppMaximumNameSearchDepth      = 64
	cppMaximumNameSearchOperations = 256
	cppMaximumSpecialNameBytes     = 4096
)

type cppTreeDeclaratorName struct {
	bindingKind string
	node        int
	root        int
	start       int
	end         int
}

// cppTreeDefinitions extracts concrete C++ declarations while deliberately
// omitting parameters, lambdas, and ordinary block variables. The shared
// syntax copy does not retain Tree-sitter field labels, so all name walks are
// bounded and follow only declarator-shaped children.
func cppTreeDefinitions(
	source string,
	lineCount int,
	tree *cppSyntaxTree,
) []sourceDefinition {
	if lineCount < 1 || !validateTreeSitterSyntaxTree(tree, len(source)) {
		return nil
	}

	errorContext := cppSyntaxErrorContexts(tree)
	recoveryDescendant := cppSyntaxRecoveryDescendants(tree)
	recoveryBeforeEnumerator, _ := cSyntaxRecoveryBeforeNextSiblingFlags(
		tree, "enumerator",
	)
	attachedStarts := cSyntaxAttachedStarts(source, tree)
	positions := cSourcePositions{
		source: source, lineStarts: cTreeLineStarts(source),
	}
	definitions := make([]sourceDefinition, 0)

	appendDefinition := func(
		name cppTreeDeclaratorName,
		scopeIndex int,
		ownsScope bool,
		attachDocumentation bool,
	) {
		name = cppNormalizeTreeName(source, tree, name)
		if !cValidSyntaxNodeIndex(tree, name.node) ||
			!cValidSyntaxNodeIndex(tree, scopeIndex) ||
			!cppTreeDefinitionNameValid(source, name) {
			return
		}
		line, column := positions.lineColumn(name.start)
		scope := tree.nodes[scopeIndex]
		startOffset := scope.startByte
		if ownsScope && attachDocumentation {
			startOffset = cppSyntaxAttachedScopeStart(
				tree, scopeIndex, attachedStarts,
			)
		}
		scopeStart, scopeEnd := positions.lineSpan(startOffset, scope.endByte)
		ownedEndLine, ownedEndColumn := positions.lineColumn(scope.endByte)
		if !ownsScope || ownedEndLine != scopeEnd {
			ownedEndColumn = 0
		}
		definitions = append(definitions, sourceDefinition{
			symbol:         source[name.start:name.end],
			line:           line,
			column:         column,
			scopeStart:     scopeStart,
			scopeEnd:       scopeEnd,
			ownedEndColumn: ownedEndColumn,
			ownsScope:      ownsScope,
		})
	}

	for nodeIndex, node := range tree.nodes {
		if cppSyntaxNodeInError(errorContext, nodeIndex) {
			continue
		}

		switch node.kind {
		case "namespace_definition":
			if cppSyntaxHasDirectRecovery(tree, nodeIndex) {
				continue
			}
			body := cDirectChildOfKind(tree, nodeIndex, "declaration_list")
			if body < 0 {
				continue
			}
			for _, name := range cppNamespaceDefinitionNames(source, tree, nodeIndex) {
				appendDefinition(name, nodeIndex, true, true)
			}

		case "namespace_alias_definition":
			if cppSyntaxHasDirectRecovery(tree, nodeIndex) {
				continue
			}
			nameIndex := cDirectChildOfKind(
				tree, nodeIndex, "namespace_identifier",
			)
			if name, ok := cppOrdinaryTreeName(tree, nameIndex, nodeIndex); ok {
				appendDefinition(name, nodeIndex, false, false)
			}

		case "alias_declaration":
			if cppSyntaxHasDirectRecovery(tree, nodeIndex) ||
				!cppTypeDefinitionContextValid(tree, nodeIndex) {
				continue
			}
			nameIndex := cDirectChildOfKind(tree, nodeIndex, "type_identifier")
			if name, ok := cppOrdinaryTreeName(tree, nameIndex, nodeIndex); ok {
				appendDefinition(name, nodeIndex, false, false)
			}

		case "concept_definition":
			if cppSyntaxHasDirectRecovery(tree, nodeIndex) ||
				!cppNamespaceDeclarationContext(tree, nodeIndex) {
				continue
			}
			nameIndex := cDirectChildOfKind(tree, nodeIndex, "identifier")
			if name, ok := cppOrdinaryTreeName(tree, nameIndex, nodeIndex); ok {
				appendDefinition(name, nodeIndex, false, false)
			}

		case "friend_declaration":
			if cppSyntaxHasDirectRecovery(tree, nodeIndex) {
				continue
			}
			friendKind := cDirectChildOfKinds(
				tree, nodeIndex, "class", "struct", "union", "enum",
			)
			nameIndex := cDirectChildOfKind(tree, nodeIndex, "type_identifier")
			if friendKind >= 0 {
				if name, ok := cppOrdinaryTreeName(tree, nameIndex, nodeIndex); ok {
					appendDefinition(name, nodeIndex, false, false)
				}
			}

		case "function_definition":
			if cppSyntaxHasDirectRecovery(tree, nodeIndex) ||
				!cppFunctionDefinitionContextValid(tree, nodeIndex) {
				continue
			}
			for _, name := range cppDirectDeclaratorNames(
				tree, nodeIndex, false,
			) {
				if cppSyntaxNodeFlagged(recoveryDescendant, name.root) {
					continue
				}
				ownsScope := cppFunctionBodyNode(tree, nodeIndex) >= 0
				appendDefinition(name, nodeIndex, ownsScope, ownsScope)
				break
			}

		case "declaration":
			if cppSyntaxHasDirectRecovery(tree, nodeIndex) {
				continue
			}
			namespaceScope := cppNamespaceDeclarationContext(tree, nodeIndex)
			memberScope := cppClassDeclarationContext(tree, nodeIndex)
			// Block declarations are intentionally absent from navigation. Apart
			// from ordinary locals, Tree-sitter must parse an ambiguous direct
			// initialization such as `Box<T> value(argument);` as a function
			// declarator (the most-vexing parse), so its node shape cannot safely
			// distinguish a useful prototype here.
			if !namespaceScope && !memberScope {
				continue
			}
			// The pinned grammar predates C++20 modules and parses a simple
			// `import name;` as a variable declaration. Module declarations and
			// imports are handled by the bounded lexical module scanner instead.
			if cppDeclarationStartsWithModuleKeyword(source, tree, nodeIndex) {
				continue
			}
			anonymousAggregate := cppDirectAnonymousAggregateBody(tree, nodeIndex)
			for _, name := range cppDirectDeclaratorNames(
				tree, nodeIndex, false,
			) {
				if cppSyntaxNodeFlagged(recoveryDescendant, name.root) {
					continue
				}
				ownsScope := anonymousAggregate >= 0
				appendDefinition(name, nodeIndex, ownsScope, ownsScope)
			}

		case "type_definition":
			if cppSyntaxHasDirectRecovery(tree, nodeIndex) ||
				!cppTypeDefinitionContextValid(tree, nodeIndex) {
				continue
			}
			anonymousAggregate := cppDirectAnonymousAggregateBody(tree, nodeIndex)
			for _, name := range cppTypeDefinitionDeclaratorNames(tree, nodeIndex) {
				if cppSyntaxNodeFlagged(recoveryDescendant, name.root) {
					continue
				}
				ownsScope := anonymousAggregate >= 0
				appendDefinition(name, nodeIndex, ownsScope, ownsScope)
			}

		case "class_specifier", "struct_specifier", "union_specifier",
			"enum_specifier":
			if cppSyntaxHasDirectRecovery(tree, nodeIndex) {
				continue
			}
			body := cppAggregateBodyNode(tree, nodeIndex)
			if body < 0 && !cppStandaloneAggregateDeclaration(tree, nodeIndex) {
				continue
			}
			if name, ok := cppAggregateName(tree, nodeIndex); ok {
				appendDefinition(name, nodeIndex, body >= 0, body >= 0)
			}

		case "enumerator":
			if cppSyntaxHasDirectRecovery(tree, nodeIndex) ||
				cppSyntaxNodeFlagged(recoveryBeforeEnumerator, nodeIndex) {
				continue
			}
			nameIndex := cDirectChildOfKind(tree, nodeIndex, "identifier")
			if name, ok := cppOrdinaryTreeName(tree, nameIndex, nodeIndex); ok {
				appendDefinition(name, nodeIndex, false, false)
			}

		case "field_declaration":
			if cppSyntaxHasDirectRecovery(tree, nodeIndex) {
				continue
			}
			anonymousAggregate := cppDirectAnonymousAggregateBody(tree, nodeIndex)
			for _, name := range cppDirectDeclaratorNames(
				tree, nodeIndex, false,
			) {
				if cppSyntaxNodeFlagged(recoveryDescendant, name.root) {
					continue
				}
				ownsScope := anonymousAggregate >= 0
				appendDefinition(name, nodeIndex, ownsScope, ownsScope)
			}

		case "preproc_def", "preproc_function_def":
			if cppSyntaxHasDirectRecovery(tree, nodeIndex) {
				continue
			}
			nameIndex := cDirectChildOfKind(tree, nodeIndex, "identifier")
			name, ok := cppOrdinaryTreeName(tree, nameIndex, nodeIndex)
			if !ok {
				continue
			}
			startLine, endLine := positions.lineSpan(node.startByte, node.endByte)
			appendDefinition(name, nodeIndex, endLine > startLine, false)
		}
	}

	return cSortUniqueTreeDefinitions(definitions, lineCount)
}

func cppDeclarationStartsWithModuleKeyword(
	source string,
	tree *cppSyntaxTree,
	nodeIndex int,
) bool {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return false
	}
	seenExport := false
	for _, childIndex := range tree.nodes[nodeIndex].children {
		if !cValidSyntaxNodeIndex(tree, childIndex) {
			return false
		}
		child := tree.nodes[childIndex]
		if child.startByte < 0 || child.endByte <= child.startByte ||
			child.endByte > len(source) {
			continue
		}
		spelling := source[child.startByte:child.endByte]
		switch spelling {
		case "module", "import":
			return true
		case "export":
			seenExport = true
			continue
		}
		return false
	}
	return seenExport
}

func cppTreeDefinitionNameValid(source string, name cppTreeDeclaratorName) bool {
	if name.start < 0 || name.end <= name.start || name.end > len(source) ||
		name.node < 0 || strings.ContainsAny(source[name.start:name.end], "\r\n") {
		return false
	}
	// The caller validated name.node against its tree. Ordinary names have the
	// same span as their identifier node; special names deliberately expand to
	// include '~' or the complete operator spelling.
	if name.end-name.start > cppMaximumSpecialNameBytes {
		return false
	}
	spelling := source[name.start:name.end]
	if strings.HasPrefix(spelling, "~") {
		return cppTreeDestructorNameEnd(source, name.start, name.end) == name.end
	}
	if strings.HasPrefix(spelling, "operator") {
		return len(spelling) > len("operator")
	}
	if strings.Contains(spelling, "::") {
		return cppQualifiedDeclarationSymbol(spelling)
	}
	return cppTreeIdentifierSourceName(source, name.start, name.end)
}

func cppNormalizeTreeName(
	source string,
	tree *cppSyntaxTree,
	name cppTreeDeclaratorName,
) cppTreeDeclaratorName {
	if name.start < 0 || name.end <= name.start || name.end > len(source) {
		return name
	}
	spelling := source[name.start:name.end]
	if strings.HasPrefix(spelling, "~") {
		if destructorEnd := cppTreeDestructorNameEnd(
			source, name.start, name.end,
		); destructorEnd > name.start {
			name.end = destructorEnd
		}
		return name
	}
	if strings.HasPrefix(spelling, "operator") {
		limit := name.end
		if cValidSyntaxNodeIndex(tree, name.root) {
			limit = min(len(source), max(limit, tree.nodes[name.root].endByte))
		}
		if operatorEnd := cppTreeOperatorNameEnd(source, name.start, limit); operatorEnd > name.start {
			name.end = operatorEnd
		}
		for name.end > name.start {
			switch source[name.end-1] {
			case ' ', '\t', '\v', '\f':
				name.end--
			default:
				return name
			}
		}
	}
	return name
}

func cppTreeOperatorNameEnd(source string, start, limit int) int {
	if start < 0 || limit > len(source) || start+len("operator") > limit ||
		source[start:start+len("operator")] != "operator" {
		return start
	}
	offset := cppTreeSpecialNameTriviaEnd(
		source, start+len("operator"), limit,
	)
	operandStart := offset
	// Allocation operators permit horizontal trivia between the keyword and
	// the array brackets.  Match them before the fixed-token table so
	// `operator new []` is not truncated to `operator new`.
	for _, allocation := range []string{"new", "delete"} {
		keywordEnd := operandStart + len(allocation)
		if keywordEnd > limit || source[operandStart:keywordEnd] != allocation ||
			cppLogicalIdentifierEnd(source, operandStart) != keywordEnd {
			continue
		}
		arrayStart := cppTreeSpecialNameTriviaEnd(source, keywordEnd, limit)
		if arrayStart+2 <= limit && source[arrayStart:arrayStart+2] == "[]" {
			return arrayStart + 2
		}
		return keywordEnd
	}
	for _, fixed := range []string{
		"co_await", "<=>", "->*",
		">>=", "<<=", "()", "[]", "->", "++", "--", "<<", ">>", "<=",
		">=", "==", "!=", "&&", "||", "+=", "-=", "*=", "/=", "%=",
		"^=", "&=", "|=", "+", "-", "*", "/", "%", "^", "&", "|",
		"~", "!", "=", "<", ">", ",",
	} {
		if strings.HasPrefix(source[operandStart:limit], fixed) {
			if fixed == "co_await" &&
				cppLogicalIdentifierEnd(source, operandStart) != operandStart+len(fixed) {
				continue
			}
			return operandStart + len(fixed)
		}
	}
	if strings.HasPrefix(source[operandStart:limit], `""`) {
		identifierStart := cppTreeSpecialNameTriviaEnd(
			source, operandStart+2, limit,
		)
		if identifierEnd := cppLogicalIdentifierEnd(source, identifierStart); identifierEnd > identifierStart {
			return min(identifierEnd, limit)
		}
		return identifierStart
	}

	// Conversion functions use a type-id rather than an operator token. Its
	// first top-level '(' begins the function parameter list. Angle and square
	// nesting cover qualified template types and attributes without allowing a
	// malformed grammar alias to consume a following declaration or body.
	angleDepth, squareDepth := 0, 0
	for offset < limit {
		if triviaEnd := cppTreeSpecialNameTriviaEnd(
			source, offset, limit,
		); triviaEnd > offset {
			offset = triviaEnd
			continue
		}
		switch source[offset] {
		case '<':
			angleDepth++
		case '>':
			angleDepth = max(0, angleDepth-1)
		case '[':
			squareDepth++
		case ']':
			squareDepth = max(0, squareDepth-1)
		case '(':
			if angleDepth == 0 && squareDepth == 0 {
				if decltypeEnd, ok := cppTreeDecltypeOperandEnd(
					source, operandStart, offset, limit,
				); ok {
					offset = decltypeEnd
					continue
				}
				return cppTrimTreeOperatorEnd(source, operandStart, offset)
			}
		case ';', '{', '=', '\r', '\n':
			if angleDepth == 0 && squareDepth == 0 {
				return cppTrimTreeOperatorEnd(source, operandStart, offset)
			}
		}
		offset++
	}
	return cppTrimTreeOperatorEnd(source, operandStart, limit)
}

func cppTreeSpecialNameTriviaEnd(source string, start, limit int) int {
	if start < 0 || start > limit || limit > len(source) {
		return start
	}
	offset := start
	for offset < limit {
		if strings.ContainsRune(" \t\v\f", rune(source[offset])) {
			offset++
			continue
		}
		if splice := cSpliceLength(source, offset); splice > 0 && offset+splice <= limit {
			offset += splice
			continue
		}
		if end, _, ok := cCommentEnd(source, offset, limit); ok && end > offset {
			offset = end
			continue
		}
		break
	}
	return offset
}

func cppTreeDestructorNameEnd(source string, start, limit int) int {
	if start < 0 || start >= limit || limit > len(source) || source[start] != '~' {
		return start
	}
	identifierStart := cppTreeSpecialNameTriviaEnd(source, start+1, limit)
	identifierEnd := cppLogicalIdentifierEnd(source, identifierStart)
	if identifierEnd <= identifierStart || identifierEnd > limit ||
		!cppTreeIdentifierSourceName(source, identifierStart, identifierEnd) {
		return start
	}
	return identifierEnd
}

func cppTreeDecltypeOperandEnd(
	source string,
	operandStart int,
	open int,
	limit int,
) (int, bool) {
	wordStart := -1
	searchStart := max(operandStart, open-cppMaximumSpecialNameBytes)
	for candidate := searchStart; candidate+len("decltype") <= open; candidate++ {
		wordEnd := candidate + len("decltype")
		if source[candidate:wordEnd] == "decltype" &&
			cppLogicalIdentifierEnd(source, candidate) == wordEnd &&
			cppTreeSpecialNameTriviaEnd(source, wordEnd, open) == open {
			wordStart = candidate
		}
	}
	if wordStart < operandStart {
		return open, false
	}

	depth := 0
	for offset := open; offset < limit; {
		if splice := cSpliceLength(source, offset); splice > 0 {
			offset += splice
			continue
		}
		if end, _, ok := cCommentEnd(source, offset, limit); ok {
			offset = end
			continue
		}
		if end, ok := cppSyntaxRawStringEnd(source, offset, limit); ok {
			offset = end
			continue
		}
		if end, ok := cLiteralEnd(source, offset, limit); ok {
			offset = end
			continue
		}
		switch source[offset] {
		case '(':
			depth++
			if depth > cppMaximumConcreteDelimiterDepth {
				return open, false
			}
		case ')':
			depth--
			if depth == 0 {
				return offset + 1, true
			}
			if depth < 0 {
				return open, false
			}
		}
		offset++
	}
	return open, false
}

func cppTrimTreeOperatorEnd(source string, minimum, end int) int {
	for end > minimum && strings.ContainsRune(" \t\v\f", rune(source[end-1])) {
		end--
	}
	return end
}

func cppOrdinaryTreeName(
	tree *cppSyntaxTree,
	nodeIndex int,
	root int,
) (cppTreeDeclaratorName, bool) {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return cppTreeDeclaratorName{}, false
	}
	node := tree.nodes[nodeIndex]
	if node.startByte >= node.endByte {
		return cppTreeDeclaratorName{}, false
	}
	return cppTreeDeclaratorName{
		node: nodeIndex, root: root, start: node.startByte, end: node.endByte,
	}, true
}

func cppNamespaceDefinitionNames(
	source string,
	tree *cppSyntaxTree,
	nodeIndex int,
) []cppTreeDeclaratorName {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return nil
	}
	for _, childIndex := range tree.nodes[nodeIndex].children {
		if !cValidSyntaxNodeIndex(tree, childIndex) {
			return nil
		}
		switch tree.nodes[childIndex].kind {
		case "namespace_identifier":
			if name, ok := cppOrdinaryTreeName(tree, childIndex, nodeIndex); ok {
				return []cppTreeDeclaratorName{name}
			}
		case "nested_namespace_specifier":
			nested := tree.nodes[childIndex]
			if nested.startByte < nested.endByte && nested.endByte <= len(source) &&
				cppQualifiedDeclarationSymbol(source[nested.startByte:nested.endByte]) {
				return []cppTreeDeclaratorName{{
					node: childIndex, root: nodeIndex,
					start: nested.startByte, end: nested.endByte,
				}}
			}
			names := cppDescendantNamesOfKind(
				tree, childIndex, nodeIndex, "namespace_identifier",
			)
			if len(names) > 0 {
				return []cppTreeDeclaratorName{names[len(names)-1]}
			}
			return nil
		case "declaration_list":
			return nil
		}
	}
	return nil
}

func cppQualifiedDeclarationSymbol(symbol string) bool {
	parts := strings.FieldsFunc(symbol, func(character rune) bool {
		return character == ':'
	})
	if len(parts) == 0 {
		return false
	}
	for _, part := range parts {
		if !cppSourceIdentifier(part) || cppKeyword(part) {
			return false
		}
	}
	return true
}

func cppAggregateName(
	tree *cppSyntaxTree,
	nodeIndex int,
) (cppTreeDeclaratorName, bool) {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return cppTreeDeclaratorName{}, false
	}
	for _, childIndex := range tree.nodes[nodeIndex].children {
		if !cValidSyntaxNodeIndex(tree, childIndex) {
			return cppTreeDeclaratorName{}, false
		}
		kind := tree.nodes[childIndex].kind
		switch kind {
		case "type_identifier":
			return cppOrdinaryTreeName(tree, childIndex, nodeIndex)
		case "template_type":
			nameIndex := cppFirstDescendantOfKinds(
				tree, childIndex, "type_identifier",
			)
			return cppOrdinaryTreeName(tree, nameIndex, nodeIndex)
		case "qualified_identifier":
			if name, ok := cppQualifiedIdentifierName(
				tree, childIndex, nodeIndex, true,
			); ok {
				return name, true
			}
		case "field_declaration_list", "enumerator_list":
			return cppTreeDeclaratorName{}, false
		}
	}
	return cppTreeDeclaratorName{}, false
}

func cppDirectDeclaratorNames(
	tree *cppSyntaxTree,
	nodeIndex int,
	typeNames bool,
) []cppTreeDeclaratorName {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return nil
	}
	names := make([]cppTreeDeclaratorName, 0)
	children := tree.nodes[nodeIndex].children
	for childPosition, childIndex := range children {
		if !cValidSyntaxNodeIndex(tree, childIndex) {
			return nil
		}
		kind := tree.nodes[childIndex].kind
		if !cppDeclaratorCanContainName(kind, typeNames) {
			continue
		}
		if !typeNames && (kind == "qualified_identifier" ||
			kind == "template_function" || kind == "template_method") &&
			!cppDirectDeclaratorHasTypeEvidence(tree, children, childPosition) {
			continue
		}
		if !typeNames {
			if binding := cppDeclaratorPathNode(
				tree, childIndex, "structured_binding_declarator",
			); binding >= 0 {
				for _, name := range cppDescendantNamesOfKind(
					tree, binding, childIndex, "identifier",
				) {
					name.root = childIndex
					name.bindingKind = "structured_binding_declarator"
					names = append(names, name)
				}
				continue
			}
		}
		if name, ok := cppUnwrapDeclaratorName(
			tree, childIndex, typeNames,
		); ok {
			names = append(names, name)
		}
	}
	return names
}

func cppTypeDefinitionDeclaratorNames(
	tree *cppSyntaxTree,
	nodeIndex int,
) []cppTreeDeclaratorName {
	names := cppDirectDeclaratorNames(tree, nodeIndex, true)
	if len(names) == 0 || cppTypeDefinitionHasExplicitType(tree, nodeIndex) {
		return names
	}
	// With a bare named underlying type, the first direct type_identifier is
	// the type use and every later declarator is an alias.
	for index, name := range names {
		if tree.nodes[name.node].parent == nodeIndex {
			return append(names[:index], names[index+1:]...)
		}
	}
	return names
}

func cppUnwrapDeclaratorName(
	tree *cppSyntaxTree,
	nodeIndex int,
	typeNames bool,
) (cppTreeDeclaratorName, bool) {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return cppTreeDeclaratorName{}, false
	}
	current := nodeIndex
	root := nodeIndex
	bindingKind := ""
	operations := 0

	for range cppMaximumDeclaratorDepth {
		if !cValidSyntaxNodeIndex(tree, current) {
			return cppTreeDeclaratorName{}, false
		}
		node := tree.nodes[current]
		if typeNames && node.kind == "type_identifier" ||
			!typeNames && (node.kind == "identifier" ||
				node.kind == "field_identifier") {
			name, ok := cppOrdinaryTreeName(tree, current, root)
			name.bindingKind = bindingKind
			return name, ok
		}
		if !typeNames && node.kind == "function_declarator" {
			for _, childIndex := range node.children {
				if cValidSyntaxNodeIndex(tree, childIndex) &&
					tree.nodes[childIndex].kind == "type_identifier" {
					name, ok := cppOrdinaryTreeName(tree, childIndex, root)
					name.bindingKind = "function_declarator"
					return name, ok
				}
			}
		}
		switch node.kind {
		case "destructor_name":
			name, ok := cppSpecialTreeName(tree, current, root, false)
			name.bindingKind = "function_declarator"
			return name, ok
		case "operator_name":
			name, ok := cppSpecialTreeName(tree, current, root, false)
			name.bindingKind = "function_declarator"
			return name, ok
		case "operator_cast":
			name, ok := cppSpecialTreeName(tree, current, root, true)
			name.bindingKind = "function_declarator"
			return name, ok
		}
		if !cppDeclaratorCanContainName(node.kind, typeNames) ||
			node.kind != "init_declarator" &&
				cppSyntaxHasDirectRecovery(tree, current) {
			return cppTreeDeclaratorName{}, false
		}
		switch node.kind {
		case "function_declarator":
			bindingKind = "function_declarator"
		case "pointer_declarator", "reference_declarator", "array_declarator",
			"structured_binding_declarator":
			bindingKind = node.kind
		}

		next := -1
		if node.kind == "qualified_identifier" {
			for childPosition := len(node.children) - 1; childPosition >= 0; childPosition-- {
				operations++
				if operations > cppMaximumDeclaratorOperations {
					return cppTreeDeclaratorName{}, false
				}
				childIndex := node.children[childPosition]
				if !cValidSyntaxNodeIndex(tree, childIndex) {
					return cppTreeDeclaratorName{}, false
				}
				if cppDeclaratorCanContainName(tree.nodes[childIndex].kind, typeNames) {
					next = childIndex
					break
				}
			}
		} else {
			for _, childIndex := range node.children {
				operations++
				if operations > cppMaximumDeclaratorOperations ||
					!cValidSyntaxNodeIndex(tree, childIndex) {
					return cppTreeDeclaratorName{}, false
				}
				if cppDeclaratorCanContainName(tree.nodes[childIndex].kind, typeNames) {
					next = childIndex
					break
				}
			}
		}
		if next < 0 || next == current {
			return cppTreeDeclaratorName{}, false
		}
		current = next
	}
	return cppTreeDeclaratorName{}, false
}

func cppSpecialTreeName(
	tree *cppSyntaxTree,
	nodeIndex int,
	root int,
	conversion bool,
) (cppTreeDeclaratorName, bool) {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return cppTreeDeclaratorName{}, false
	}
	node := tree.nodes[nodeIndex]
	start, end := node.startByte, node.endByte
	if conversion {
		parameters := cppFirstDescendantOfKinds(tree, nodeIndex, "parameter_list")
		if !cValidSyntaxNodeIndex(tree, parameters) {
			return cppTreeDeclaratorName{}, false
		}
		end = tree.nodes[parameters].startByte
	}
	if start < 0 || end <= start {
		return cppTreeDeclaratorName{}, false
	}
	return cppTreeDeclaratorName{
		node: nodeIndex, root: root, start: start, end: end,
	}, true
}

func cppQualifiedIdentifierName(
	tree *cppSyntaxTree,
	nodeIndex int,
	root int,
	allowType bool,
) (cppTreeDeclaratorName, bool) {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return cppTreeDeclaratorName{}, false
	}
	children := tree.nodes[nodeIndex].children
	for childPosition := len(children) - 1; childPosition >= 0; childPosition-- {
		childIndex := children[childPosition]
		if !cValidSyntaxNodeIndex(tree, childIndex) {
			return cppTreeDeclaratorName{}, false
		}
		kind := tree.nodes[childIndex].kind
		if kind == "identifier" || kind == "field_identifier" ||
			allowType && kind == "type_identifier" {
			return cppOrdinaryTreeName(tree, childIndex, root)
		}
		if kind == "destructor_name" || kind == "operator_name" {
			return cppSpecialTreeName(tree, childIndex, root, false)
		}
		if kind == "operator_cast" {
			return cppSpecialTreeName(tree, childIndex, root, true)
		}
		if allowType && kind == "template_type" {
			nameIndex := cDirectChildOfKind(tree, childIndex, "type_identifier")
			if nameIndex < 0 {
				nameIndex = cppFirstDescendantOfKinds(
					tree, childIndex, "type_identifier",
				)
			}
			if name, ok := cppOrdinaryTreeName(tree, nameIndex, root); ok {
				return name, true
			}
		}
		if kind == "template_function" || kind == "template_method" ||
			kind == "template_type" || kind == "qualified_identifier" {
			if name, ok := cppUnwrapDeclaratorName(
				tree, childIndex, allowType,
			); ok {
				name.root = root
				return name, true
			}
		}
	}
	return cppTreeDeclaratorName{}, false
}

func cppDeclaratorCanContainName(kind string, typeNames bool) bool {
	if typeNames && kind == "type_identifier" ||
		!typeNames && (kind == "identifier" || kind == "field_identifier") {
		return true
	}
	switch kind {
	case "init_declarator", "parenthesized_declarator",
		"attributed_declarator", "pointer_declarator", "reference_declarator",
		"function_declarator", "array_declarator", "variadic_declarator":
		return true
	case "qualified_identifier", "template_function", "template_method":
		return !typeNames
	case "destructor_name", "operator_name", "operator_cast",
		"structured_binding_declarator":
		return !typeNames
	default:
		return false
	}
}

func cppDirectDeclaratorHasTypeEvidence(
	tree *cppSyntaxTree,
	children []int,
	declaratorPosition int,
) bool {
	for position := range declaratorPosition {
		childIndex := children[position]
		if !cValidSyntaxNodeIndex(tree, childIndex) {
			return false
		}
		switch tree.nodes[childIndex].kind {
		case "primitive_type", "sized_type_specifier", "type_identifier",
			"template_type", "qualified_identifier", "decltype",
			"placeholder_type_specifier", "dependent_type", "class_specifier",
			"struct_specifier", "union_specifier", "enum_specifier":
			return true
		}
	}
	return false
}

func cppDeclaratorPathNode(
	tree *cppSyntaxTree,
	nodeIndex int,
	wantedKind string,
) int {
	current := nodeIndex
	operations := 0
	for range cppMaximumDeclaratorDepth {
		if !cValidSyntaxNodeIndex(tree, current) {
			return -1
		}
		node := tree.nodes[current]
		if node.kind == wantedKind {
			return current
		}
		next := -1
		for _, childIndex := range node.children {
			operations++
			if operations > cppMaximumDeclaratorOperations ||
				!cValidSyntaxNodeIndex(tree, childIndex) {
				return -1
			}
			if cppDeclaratorCanContainName(tree.nodes[childIndex].kind, false) {
				next = childIndex
				break
			}
		}
		if next < 0 {
			return -1
		}
		current = next
	}
	return -1
}

func cppDescendantNamesOfKind(
	tree *cppSyntaxTree,
	nodeIndex int,
	root int,
	kind string,
) []cppTreeDeclaratorName {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) || kind == "" {
		return nil
	}
	type entry struct{ node, depth int }
	stack := []entry{{node: nodeIndex}}
	operations := 0
	names := make([]cppTreeDeclaratorName, 0)
	for len(stack) > 0 {
		last := len(stack) - 1
		current := stack[last]
		stack = stack[:last]
		operations++
		if operations > cppMaximumNameSearchOperations {
			// Wide structured bindings can exceed the traversal budget even
			// though every name visited so far is complete and valid. Preserve
			// that bounded prefix instead of turning one extra sibling into an
			// all-or-nothing extraction cliff.
			return names
		}
		if current.depth > cppMaximumNameSearchDepth ||
			!cValidSyntaxNodeIndex(tree, current.node) {
			return nil
		}
		node := tree.nodes[current.node]
		if node.kind == kind {
			if name, ok := cppOrdinaryTreeName(tree, current.node, root); ok {
				names = append(names, name)
			}
		}
		for childPosition := len(node.children) - 1; childPosition >= 0; childPosition-- {
			stack = append(stack, entry{
				node: node.children[childPosition], depth: current.depth + 1,
			})
		}
	}
	return names
}

func cppFirstDescendantOfKinds(
	tree *cppSyntaxTree,
	nodeIndex int,
	kinds ...string,
) int {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return -1
	}
	type entry struct{ node, depth int }
	stack := []entry{{node: nodeIndex}}
	operations := 0
	for len(stack) > 0 {
		last := len(stack) - 1
		current := stack[last]
		stack = stack[:last]
		operations++
		if operations > cppMaximumNameSearchOperations ||
			current.depth > cppMaximumNameSearchDepth ||
			!cValidSyntaxNodeIndex(tree, current.node) {
			return -1
		}
		if current.node != nodeIndex {
			for _, kind := range kinds {
				if tree.nodes[current.node].kind == kind {
					return current.node
				}
			}
		}
		children := tree.nodes[current.node].children
		for childPosition := len(children) - 1; childPosition >= 0; childPosition-- {
			stack = append(stack, entry{
				node: children[childPosition], depth: current.depth + 1,
			})
		}
	}
	return -1
}

func cppTypeDefinitionHasExplicitType(
	tree *cppSyntaxTree,
	nodeIndex int,
) bool {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return false
	}
	for _, childIndex := range tree.nodes[nodeIndex].children {
		if !cValidSyntaxNodeIndex(tree, childIndex) {
			return false
		}
		switch tree.nodes[childIndex].kind {
		case "primitive_type", "sized_type_specifier", "class_specifier",
			"struct_specifier", "union_specifier", "enum_specifier", "template_type",
			"qualified_identifier", "decltype", "placeholder_type_specifier",
			"dependent_type", "macro_type_specifier":
			return true
		}
	}
	return false
}

func cppDirectAnonymousAggregateBody(tree *cppSyntaxTree, nodeIndex int) int {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return -1
	}
	for _, childIndex := range tree.nodes[nodeIndex].children {
		if !cValidSyntaxNodeIndex(tree, childIndex) {
			return -1
		}
		switch tree.nodes[childIndex].kind {
		case "class_specifier", "struct_specifier", "union_specifier",
			"enum_specifier":
			if cppAggregateBodyNode(tree, childIndex) >= 0 {
				if _, named := cppAggregateName(tree, childIndex); !named {
					return childIndex
				}
			}
		}
	}
	return -1
}

func cppAggregateBodyNode(tree *cppSyntaxTree, nodeIndex int) int {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return -1
	}
	switch tree.nodes[nodeIndex].kind {
	case "class_specifier", "struct_specifier", "union_specifier":
		return cDirectChildOfKind(tree, nodeIndex, "field_declaration_list")
	case "enum_specifier":
		return cDirectChildOfKind(tree, nodeIndex, "enumerator_list")
	default:
		return -1
	}
}

func cppFunctionBodyNode(tree *cppSyntaxTree, nodeIndex int) int {
	return cDirectChildOfKinds(
		tree, nodeIndex, "compound_statement", "try_statement",
	)
}

func cppFunctionDefinitionContextValid(tree *cppSyntaxTree, nodeIndex int) bool {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return false
	}
	for parent := tree.nodes[nodeIndex].parent; parent >= 0; {
		if !cValidSyntaxNodeIndex(tree, parent) {
			return false
		}
		switch tree.nodes[parent].kind {
		case "translation_unit", "declaration_list", "field_declaration_list",
			"linkage_specification":
			return true
		case "compound_statement", "lambda_expression", "parameter_declaration",
			"parameter_list", "type_descriptor", "initializer_list":
			return false
		case "ERROR":
			if !cSyntaxWholeFileErrorRoot(tree, parent) {
				return false
			}
		}
		parent = tree.nodes[parent].parent
	}
	return true
}

func cppNamespaceDeclarationContext(tree *cppSyntaxTree, nodeIndex int) bool {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return false
	}
	for parent := tree.nodes[nodeIndex].parent; parent >= 0; {
		if !cValidSyntaxNodeIndex(tree, parent) {
			return false
		}
		switch tree.nodes[parent].kind {
		case "translation_unit", "declaration_list", "linkage_specification":
			return true
		case "compound_statement", "function_definition", "lambda_expression",
			"field_declaration", "field_declaration_list", "parameter_declaration",
			"parameter_list", "type_descriptor", "initializer_list":
			return false
		case "ERROR":
			if !cSyntaxWholeFileErrorRoot(tree, parent) {
				return false
			}
		}
		parent = tree.nodes[parent].parent
	}
	return true
}

func cppClassDeclarationContext(tree *cppSyntaxTree, nodeIndex int) bool {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return false
	}
	for parent := tree.nodes[nodeIndex].parent; parent >= 0; {
		if !cValidSyntaxNodeIndex(tree, parent) {
			return false
		}
		switch tree.nodes[parent].kind {
		case "field_declaration_list":
			return true
		case "translation_unit", "declaration_list", "compound_statement",
			"function_definition", "lambda_expression", "parameter_declaration",
			"parameter_list", "type_descriptor", "initializer_list":
			return false
		case "ERROR":
			if !cSyntaxWholeFileErrorRoot(tree, parent) {
				return false
			}
		}
		parent = tree.nodes[parent].parent
	}
	return false
}

func cppTypeDefinitionContextValid(tree *cppSyntaxTree, nodeIndex int) bool {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return false
	}
	for parent := tree.nodes[nodeIndex].parent; parent >= 0; {
		if !cValidSyntaxNodeIndex(tree, parent) {
			return false
		}
		switch tree.nodes[parent].kind {
		case "translation_unit", "declaration_list", "field_declaration_list",
			"compound_statement", "linkage_specification":
			return true
		case "parameter_declaration", "parameter_list", "type_descriptor",
			"initializer_list", "lambda_expression":
			return false
		case "ERROR":
			if !cSyntaxWholeFileErrorRoot(tree, parent) {
				return false
			}
		}
		parent = tree.nodes[parent].parent
	}
	return true
}

func cppStandaloneAggregateDeclaration(tree *cppSyntaxTree, nodeIndex int) bool {
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
	case "translation_unit", "declaration_list", "field_declaration_list",
		"compound_statement", "linkage_specification", "template_declaration",
		"friend_declaration",
		"preproc_if", "preproc_ifdef", "preproc_else", "preproc_elif",
		"preproc_elifdef":
		return true
	case "declaration":
		return len(cppDirectDeclaratorNames(tree, parent, false)) == 0
	case "field_declaration":
		return len(cppDirectDeclaratorNames(tree, parent, false)) == 0
	case "ERROR":
		return cSyntaxWholeFileErrorRoot(tree, parent)
	default:
		return false
	}
}

// cppTreeScopes returns declaration bodies, lambdas, control-flow bodies,
// linkage blocks, and preprocessor branches as concrete line scopes.
func cppTreeScopes(source string, tree *cppSyntaxTree) []cLineScope {
	if !validateTreeSitterSyntaxTree(tree, len(source)) {
		return nil
	}
	lineStarts := cTreeLineStarts(source)
	positions := cSourcePositions{source: source, lineStarts: lineStarts}
	errorContext := cppSyntaxErrorContexts(tree)
	attachedStarts := cSyntaxAttachedStarts(source, tree)
	scopes := make([]cLineScope, 0)

	for nodeIndex, node := range tree.nodes {
		if cppSyntaxNodeInError(errorContext, nodeIndex) ||
			!cppTreeScopeNode(tree, nodeIndex, positions) {
			continue
		}
		if node.kind == "compound_statement" &&
			cppCompoundScopeOwnedByParent(tree, nodeIndex) {
			continue
		}
		startOffset := node.startByte
		if node.kind == "function_definition" ||
			node.kind == "namespace_definition" ||
			cppAggregateBodyNode(tree, nodeIndex) >= 0 {
			startOffset = cppSyntaxAttachedScopeStart(
				tree, nodeIndex, attachedStarts,
			)
		}
		start, end := positions.lineSpan(startOffset, node.endByte)
		scopes = append(scopes, cLineScope{start: start, end: end})
	}
	return cNormalizeTreeLineScopes(scopes, len(lineStarts))
}

func cppTreeScopeNode(
	tree *cppSyntaxTree,
	nodeIndex int,
	positions cSourcePositions,
) bool {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) ||
		cppSyntaxHasDirectRecovery(tree, nodeIndex) {
		return false
	}
	node := tree.nodes[nodeIndex]
	switch node.kind {
	case "function_definition":
		return cppFunctionDefinitionContextValid(tree, nodeIndex) &&
			cppFunctionBodyNode(tree, nodeIndex) >= 0
	case "namespace_definition":
		return cDirectChildOfKind(tree, nodeIndex, "declaration_list") >= 0
	case "class_specifier", "struct_specifier", "union_specifier",
		"enum_specifier":
		return cppAggregateBodyNode(tree, nodeIndex) >= 0
	case "linkage_specification":
		return cDirectChildOfKind(tree, nodeIndex, "declaration_list") >= 0
	case "preproc_def", "preproc_function_def":
		start, end := positions.lineSpan(node.startByte, node.endByte)
		return end > start
	case "lambda_expression":
		return cDirectChildOfKind(tree, nodeIndex, "compound_statement") >= 0
	case "compound_statement", "if_statement", "else_clause",
		"switch_statement", "case_statement", "while_statement", "do_statement",
		"for_statement", "for_range_loop", "try_statement", "catch_clause",
		"preproc_if", "preproc_ifdef", "preproc_else", "preproc_elif",
		"preproc_elifdef", "seh_try_statement", "seh_except_clause",
		"seh_finally_clause":
		return true
	default:
		return false
	}
}

func cppCompoundScopeOwnedByParent(tree *cppSyntaxTree, nodeIndex int) bool {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return false
	}
	node := tree.nodes[nodeIndex]
	parent := node.parent
	if !cValidSyntaxNodeIndex(tree, parent) {
		return false
	}
	parentNode := tree.nodes[parent]
	if parentNode.kind == "function_definition" ||
		parentNode.kind == "lambda_expression" {
		return true
	}
	if node.endByte != parentNode.endByte {
		return false
	}
	switch parentNode.kind {
	case "if_statement", "else_clause", "switch_statement", "case_statement",
		"while_statement", "do_statement", "for_statement", "for_range_loop",
		"catch_clause", "seh_try_statement", "seh_except_clause",
		"seh_finally_clause":
		return true
	default:
		return false
	}
}

// cppTreeImports returns concrete #include spans. C++20 import/module syntax
// is absent from the pinned v0.23.4 grammar and remains a lexical responsibility.
func cppTreeImports(source string, tree *cppSyntaxTree) []cLineSpan {
	if !validateTreeSitterSyntaxTree(tree, len(source)) {
		return nil
	}
	lineStarts := cTreeLineStarts(source)
	positions := cSourcePositions{source: source, lineStarts: lineStarts}
	errorContext := cppSyntaxErrorContexts(tree)
	errorDescendant := cppSyntaxRecoveryDescendants(tree)
	imports := make([]cLineSpan, 0)

	for nodeIndex, node := range tree.nodes {
		if node.kind != "preproc_include" ||
			cppSyntaxNodeInError(errorContext, nodeIndex) ||
			cppSyntaxNodeFlagged(errorDescendant, nodeIndex) {
			continue
		}
		pathIndex := cDirectChildOfKinds(
			tree, nodeIndex, "system_lib_string", "string_literal", "identifier",
			"call_expression",
		)
		if !cValidSyntaxNodeIndex(tree, pathIndex) ||
			tree.nodes[pathIndex].startByte >= tree.nodes[pathIndex].endByte {
			continue
		}
		start, end := positions.lineSpan(node.startByte, node.endByte)
		imports = append(imports, cLineSpan{start: start, end: end})
	}
	return cNormalizeTreeLineSpans(imports, len(lineStarts))
}

func cppSyntaxAttachedScopeStart(
	tree *cppSyntaxTree,
	nodeIndex int,
	starts []int,
) int {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return 0
	}
	start := tree.nodes[nodeIndex].startByte
	current := nodeIndex
	for depth := 0; depth < cppMaximumNameSearchDepth &&
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
		case "template_declaration", "declaration", "type_definition",
			"field_declaration", "attributed_statement", "friend_declaration":
			current = parent
		default:
			return start
		}
	}
	return start
}

func cppSyntaxErrorContexts(tree *cppSyntaxTree) []bool {
	if tree == nil || len(tree.nodes) == 0 {
		return nil
	}
	rootRecoveryStart, rootHasRecovery := cppSyntaxWholeRootRecoveryStart(tree)
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

func cppSyntaxRecoveryDescendants(tree *cppSyntaxTree) []bool {
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

func cppSyntaxWholeRootRecoveryStart(tree *cppSyntaxTree) (int, bool) {
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
		if !cppSyntaxWholeRootItemKind(tree.nodes[childIndex].kind) {
			record(tree.nodes[childIndex].startByte)
			break
		}
	}
	return start, found
}

func cppSyntaxWholeRootItemKind(kind string) bool {
	if cSyntaxWholeRootItemKind(kind) {
		return true
	}
	switch kind {
	case "namespace_definition", "namespace_alias_definition", "using_declaration",
		"alias_declaration", "static_assert_declaration", "concept_definition",
		"template_declaration", "template_instantiation", "class_specifier",
		"friend_declaration":
		return true
	default:
		return false
	}
}

func cppSyntaxHasDirectRecovery(tree *cppSyntaxTree, nodeIndex int) bool {
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

func cppSyntaxNodeInError(errorContext []bool, nodeIndex int) bool {
	return nodeIndex < 0 || nodeIndex >= len(errorContext) || errorContext[nodeIndex]
}

func cppSyntaxNodeFlagged(flags []bool, nodeIndex int) bool {
	return nodeIndex < 0 || nodeIndex >= len(flags) || flags[nodeIndex]
}
