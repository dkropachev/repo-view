package repoview

import (
	"context"
	"time"

	treesitter "github.com/dcosson/treesitter-go"
	pythonlanguage "github.com/dcosson/treesitter-go/languages/python"
	pythonparser "github.com/dcosson/treesitter-go/parser"
)

const (
	pythonSyntaxParseTimeout = 2 * time.Second
	pythonSyntaxMinimumNodes = 1024
	pythonSyntaxNodesPerByte = 8
)

// pythonSyntaxNode is a position-safe copy of a tree-sitter node. Keeping only
// byte offsets prevents callers from accidentally relying on parser points or
// retaining parser-owned node values.
type pythonSyntaxNode struct {
	kind     string
	children []int

	startByte, endByte int
	parent             int
}

type pythonSyntaxTree struct {
	nodes []pythonSyntaxNode
	root  int
}

// parsePythonSyntax parses source with the pure-Go Python grammar and copies
// the complete visible tree through TreeCursor. A malformed parser result is
// rejected instead of exposing untrusted coordinates to the rest of repoview.
func parsePythonSyntax(source string) (syntaxTree *pythonSyntaxTree, ok bool) {
	defer func() {
		if recover() != nil {
			syntaxTree = nil
			ok = false
		}
	}()
	if uint64(len(source)) > uint64(^uint32(0)) {
		return nil, false
	}

	ctx, cancel := context.WithTimeout(context.Background(), pythonSyntaxParseTimeout)
	defer cancel()

	parser := pythonparser.NewParser()
	parser.SetLanguage(pythonlanguage.Language())
	tree := parser.ParseString(ctx, []byte(source))
	if tree == nil || ctx.Err() != nil {
		return nil, false
	}

	cursor := treesitter.NewTreeCursor(tree.RootNode())
	syntaxTree, ok = copyPythonSyntaxTree(ctx, &cursor, len(source))
	if !ok || !validatePythonSyntaxTree(syntaxTree, len(source)) {
		return nil, false
	}
	return syntaxTree, true
}

func copyPythonSyntaxTree(
	ctx context.Context,
	cursor *treesitter.TreeCursor,
	sourceLength int,
) (*pythonSyntaxTree, bool) {
	root, ok := pythonSyntaxNodeAtCursor(cursor, sourceLength, -1)
	if !ok {
		return nil, false
	}

	nodeLimit := pythonSyntaxNodeLimit(sourceLength)
	operationLimit := pythonSyntaxOperationLimit(nodeLimit)
	operations := 0
	syntaxTree := &pythonSyntaxTree{
		nodes: []pythonSyntaxNode{root},
		root:  0,
	}
	current := syntaxTree.root

	for {
		if !pythonSyntaxOperationAvailable(ctx, &operations, operationLimit) {
			return nil, false
		}
		if cursor.GotoFirstChild() {
			child, valid := pythonSyntaxNodeAtCursor(cursor, sourceLength, current)
			if !valid {
				return nil, false
			}
			if appendPythonSyntaxNode(syntaxTree, child, nodeLimit) {
				current = len(syntaxTree.nodes) - 1
				continue
			}
			realigned, appended := appendPythonSyntaxRealignedSibling(
				ctx,
				syntaxTree,
				child,
				current,
				nodeLimit,
				&operations,
				operationLimit,
			)
			if !appended {
				return nil, false
			}
			current = realigned
			continue
		}
		if !pythonSyntaxCursorMatches(cursor, syntaxTree.nodes[current], sourceLength) {
			return nil, false
		}

		for current != syntaxTree.root {
			parent := syntaxTree.nodes[current].parent
			if parent < 0 || parent >= len(syntaxTree.nodes) {
				return nil, false
			}

			if !pythonSyntaxOperationAvailable(ctx, &operations, operationLimit) {
				return nil, false
			}
			if cursor.GotoNextSibling() {
				sibling, valid := pythonSyntaxNodeAtCursor(cursor, sourceLength, parent)
				if !valid {
					return nil, false
				}
				if appendPythonSyntaxNode(syntaxTree, sibling, nodeLimit) {
					current = len(syntaxTree.nodes) - 1
					break
				}
				realigned, appended := appendPythonSyntaxRealignedSibling(
					ctx,
					syntaxTree,
					sibling,
					parent,
					nodeLimit,
					&operations,
					operationLimit,
				)
				if !appended {
					return nil, false
				}
				current = realigned
				break
			}

			// This cursor can expose a transparent grammar node after an
			// unsuccessful sibling move. It can also skip a downward-only alias
			// while moving upward. The resulting node must exactly match a
			// recorded strict ancestor; skipped aliases are thereby complete.
			if !pythonSyntaxOperationAvailable(ctx, &operations, operationLimit) ||
				!cursor.GotoParent() {
				return nil, false
			}
			ancestor, aligned := pythonSyntaxRecordedAncestorAtCursor(
				ctx,
				cursor,
				syntaxTree,
				current,
				sourceLength,
				&operations,
				operationLimit,
			)
			if !aligned {
				return nil, false
			}
			current = ancestor
		}

		if current != syntaxTree.root {
			continue
		}
		if !pythonSyntaxOperationAvailable(ctx, &operations, operationLimit) {
			return nil, false
		}
		if cursor.GotoParent() ||
			!pythonSyntaxCursorMatches(cursor, syntaxTree.nodes[syntaxTree.root], sourceLength) {
			return nil, false
		}
		return syntaxTree, true
	}
}

func pythonSyntaxNodeAtCursor(
	cursor *treesitter.TreeCursor,
	sourceLength int,
	parent int,
) (pythonSyntaxNode, bool) {
	node := cursor.CurrentNode()
	startByte := node.StartByte()
	endByte := node.EndByte()
	if uint64(startByte) > uint64(sourceLength) ||
		uint64(endByte) > uint64(sourceLength) ||
		startByte > endByte {
		return pythonSyntaxNode{}, false
	}
	kind := node.Type()
	if kind == "" {
		return pythonSyntaxNode{}, false
	}
	return pythonSyntaxNode{
		kind:      kind,
		startByte: int(startByte),
		endByte:   int(endByte),
		parent:    parent,
	}, true
}

func pythonSyntaxCursorMatches(
	cursor *treesitter.TreeCursor,
	want pythonSyntaxNode,
	sourceLength int,
) bool {
	got, ok := pythonSyntaxNodeAtCursor(cursor, sourceLength, want.parent)
	return ok &&
		got.kind == want.kind &&
		got.startByte == want.startByte &&
		got.endByte == want.endByte
}

func pythonSyntaxRecordedAncestorAtCursor(
	ctx context.Context,
	cursor *treesitter.TreeCursor,
	syntaxTree *pythonSyntaxTree,
	current int,
	sourceLength int,
	operations *int,
	operationLimit int,
) (int, bool) {
	got, ok := pythonSyntaxNodeAtCursor(cursor, sourceLength, -1)
	if !ok {
		return -1, false
	}
	return pythonSyntaxRecordedAncestor(
		ctx,
		syntaxTree,
		current,
		got,
		operations,
		operationLimit,
	)
}

func pythonSyntaxRecordedAncestor(
	ctx context.Context,
	syntaxTree *pythonSyntaxTree,
	current int,
	got pythonSyntaxNode,
	operations *int,
	operationLimit int,
) (int, bool) {
	if syntaxTree == nil || current <= syntaxTree.root || current >= len(syntaxTree.nodes) {
		return -1, false
	}

	previous := current
	for ancestor := syntaxTree.nodes[current].parent; ancestor >= 0; {
		if ancestor >= previous || ancestor >= len(syntaxTree.nodes) ||
			!pythonSyntaxOperationAvailable(ctx, operations, operationLimit) {
			return -1, false
		}
		want := syntaxTree.nodes[ancestor]
		if got.kind == want.kind &&
			got.startByte == want.startByte &&
			got.endByte == want.endByte {
			return ancestor, true
		}
		previous = ancestor
		ancestor = want.parent
	}
	return -1, false
}

func appendPythonSyntaxRealignedSibling(
	ctx context.Context,
	syntaxTree *pythonSyntaxTree,
	node pythonSyntaxNode,
	firstAncestor int,
	nodeLimit int,
	operations *int,
	operationLimit int,
) (int, bool) {
	if syntaxTree == nil || firstAncestor < 0 || firstAncestor >= len(syntaxTree.nodes) {
		return -1, false
	}

	previous := len(syntaxTree.nodes)
	for ancestor := firstAncestor; ancestor >= 0; {
		if ancestor >= previous || ancestor >= len(syntaxTree.nodes) ||
			!pythonSyntaxOperationAvailable(ctx, operations, operationLimit) {
			return -1, false
		}
		ancestorNode := syntaxTree.nodes[ancestor]
		parentIndex := ancestorNode.parent
		if parentIndex < 0 || parentIndex >= ancestor {
			return -1, false
		}
		parent := &syntaxTree.nodes[parentIndex]
		if len(parent.children) == 0 || parent.children[len(parent.children)-1] != ancestor {
			return -1, false
		}
		// Never repair a parser parent whose leading extra children precede
		// its reported start. That would weaken both monotonicity and the
		// containment invariant; the lexical analyzer is the safe fallback.
		if node.startByte < ancestorNode.endByte {
			return -1, false
		}
		if node.startByte >= parent.startByte && node.endByte <= parent.endByte {
			node.parent = parentIndex
			if !appendPythonSyntaxNode(syntaxTree, node, nodeLimit) {
				return -1, false
			}
			return len(syntaxTree.nodes) - 1, true
		}
		previous = ancestor
		ancestor = parentIndex
	}
	return -1, false
}

func appendPythonSyntaxNode(
	syntaxTree *pythonSyntaxTree,
	node pythonSyntaxNode,
	nodeLimit int,
) bool {
	if len(syntaxTree.nodes) >= nodeLimit ||
		node.parent < 0 ||
		node.parent >= len(syntaxTree.nodes) {
		return false
	}
	parent := &syntaxTree.nodes[node.parent]
	if node.startByte < parent.startByte || node.endByte > parent.endByte {
		return false
	}
	if len(parent.children) > 0 {
		previousIndex := parent.children[len(parent.children)-1]
		if previousIndex < 0 || previousIndex >= len(syntaxTree.nodes) ||
			node.startByte < syntaxTree.nodes[previousIndex].endByte {
			return false
		}
	}

	nodeIndex := len(syntaxTree.nodes)
	syntaxTree.nodes = append(syntaxTree.nodes, node)
	syntaxTree.nodes[node.parent].children = append(
		syntaxTree.nodes[node.parent].children,
		nodeIndex,
	)
	return true
}

func validatePythonSyntaxTree(syntaxTree *pythonSyntaxTree, sourceLength int) bool {
	if syntaxTree == nil ||
		len(syntaxTree.nodes) == 0 ||
		syntaxTree.root != 0 ||
		syntaxTree.nodes[syntaxTree.root].parent != -1 {
		return false
	}

	references := make([]int, len(syntaxTree.nodes))
	for nodeIndex := range syntaxTree.nodes {
		node := &syntaxTree.nodes[nodeIndex]
		if node.kind == "" ||
			node.startByte < 0 ||
			node.startByte > node.endByte ||
			node.endByte > sourceLength {
			return false
		}
		if nodeIndex != syntaxTree.root &&
			(node.parent < 0 || node.parent >= nodeIndex) {
			return false
		}

		previousEnd := node.startByte
		for _, childIndex := range node.children {
			if childIndex <= nodeIndex || childIndex >= len(syntaxTree.nodes) {
				return false
			}
			child := &syntaxTree.nodes[childIndex]
			if child.parent != nodeIndex ||
				child.startByte < node.startByte ||
				child.endByte > node.endByte ||
				child.startByte < previousEnd {
				return false
			}
			previousEnd = child.endByte
			references[childIndex]++
		}
	}

	if references[syntaxTree.root] != 0 {
		return false
	}
	for nodeIndex := 1; nodeIndex < len(references); nodeIndex++ {
		if references[nodeIndex] != 1 {
			return false
		}
	}
	return true
}

func pythonSyntaxNodeLimit(sourceLength int) int {
	maxInt := int(^uint(0) >> 1)
	if sourceLength > (maxInt-pythonSyntaxMinimumNodes)/pythonSyntaxNodesPerByte {
		return maxInt
	}
	return pythonSyntaxMinimumNodes + sourceLength*pythonSyntaxNodesPerByte
}

func pythonSyntaxOperationLimit(nodeLimit int) int {
	maxInt := int(^uint(0) >> 1)
	if nodeLimit > (maxInt-16)/4 {
		return maxInt
	}
	return nodeLimit*4 + 16
}

func pythonSyntaxOperationAvailable(
	ctx context.Context,
	operations *int,
	operationLimit int,
) bool {
	if *operations >= operationLimit || ctx.Err() != nil {
		return false
	}
	(*operations)++
	return true
}
