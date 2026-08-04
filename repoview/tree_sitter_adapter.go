package repoview

import (
	"context"
	"time"

	treesitter "github.com/dcosson/treesitter-go"
	treesitterparser "github.com/dcosson/treesitter-go/parser"
)

const (
	treeSitterSyntaxParseTimeout = 2 * time.Second
	treeSitterSyntaxMinimumNodes = 1024
	treeSitterSyntaxNodesPerByte = 8
)

// treeSitterSyntaxNode is a position-safe copy of a Tree-sitter node. Keeping
// only byte offsets prevents callers from retaining parser-owned node values or
// accidentally relying on parser points.
type treeSitterSyntaxNode struct {
	kind     string
	children []int

	startByte, endByte int
	parent             int
}

type treeSitterSyntaxTree struct {
	nodes []treeSitterSyntaxNode
	root  int
}

// parseTreeSitterSyntax parses source with a pure-Go grammar and copies the
// complete visible tree through TreeCursor. Malformed parser results are
// rejected instead of exposing untrusted coordinates to language backends.
func parseTreeSitterSyntax(
	source string,
	language *treesitter.Language,
) (syntaxTree *treeSitterSyntaxTree, ok bool) {
	ctx, cancel := context.WithTimeout(context.Background(), treeSitterSyntaxParseTimeout)
	defer cancel()
	return parseTreeSitterSyntaxContext(ctx, source, language)
}

func parseTreeSitterSyntaxContext(
	ctx context.Context,
	source string,
	language *treesitter.Language,
) (syntaxTree *treeSitterSyntaxTree, ok bool) {
	defer func() {
		if recover() != nil {
			syntaxTree = nil
			ok = false
		}
	}()
	if ctx == nil || ctx.Err() != nil || language == nil ||
		uint64(len(source)) > uint64(^uint32(0)) {
		return nil, false
	}

	parser := treesitterparser.NewParser()
	parser.SetLanguage(language)
	tree := parser.ParseString(ctx, []byte(source))
	if tree == nil || ctx.Err() != nil {
		return nil, false
	}

	cursor := treesitter.NewTreeCursor(tree.RootNode())
	syntaxTree, ok = copyTreeSitterSyntaxTree(ctx, &cursor, len(source))
	if !ok || !validateTreeSitterSyntaxTree(syntaxTree, len(source)) {
		return nil, false
	}
	return syntaxTree, true
}

func copyTreeSitterSyntaxTree(
	ctx context.Context,
	cursor *treesitter.TreeCursor,
	sourceLength int,
) (*treeSitterSyntaxTree, bool) {
	root, ok := treeSitterSyntaxNodeAtCursor(cursor, sourceLength, -1)
	if !ok {
		return nil, false
	}

	nodeLimit := treeSitterSyntaxNodeLimit(sourceLength)
	operationLimit := treeSitterSyntaxOperationLimit(nodeLimit)
	operations := 0
	syntaxTree := &treeSitterSyntaxTree{
		nodes: []treeSitterSyntaxNode{root},
		root:  0,
	}
	current := syntaxTree.root

	for {
		if !treeSitterSyntaxOperationAvailable(ctx, &operations, operationLimit) {
			return nil, false
		}
		if cursor.GotoFirstChild() {
			child, valid := treeSitterSyntaxNodeAtCursor(cursor, sourceLength, current)
			if !valid {
				return nil, false
			}
			if appendTreeSitterSyntaxNode(syntaxTree, child, nodeLimit) {
				current = len(syntaxTree.nodes) - 1
				continue
			}
			realigned, appended := appendTreeSitterSyntaxRealignedSibling(
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
		if !treeSitterSyntaxCursorMatches(cursor, syntaxTree.nodes[current], sourceLength) {
			return nil, false
		}

		for current != syntaxTree.root {
			parent := syntaxTree.nodes[current].parent
			if parent < 0 || parent >= len(syntaxTree.nodes) {
				return nil, false
			}

			if !treeSitterSyntaxOperationAvailable(ctx, &operations, operationLimit) {
				return nil, false
			}
			if cursor.GotoNextSibling() {
				sibling, valid := treeSitterSyntaxNodeAtCursor(cursor, sourceLength, parent)
				if !valid {
					return nil, false
				}
				if appendTreeSitterSyntaxNode(syntaxTree, sibling, nodeLimit) {
					current = len(syntaxTree.nodes) - 1
					break
				}
				realigned, appended := appendTreeSitterSyntaxRealignedSibling(
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
			if !treeSitterSyntaxOperationAvailable(ctx, &operations, operationLimit) ||
				!cursor.GotoParent() {
				return nil, false
			}
			ancestor, aligned := treeSitterSyntaxRecordedAncestorAtCursor(
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
		if !treeSitterSyntaxOperationAvailable(ctx, &operations, operationLimit) {
			return nil, false
		}
		if cursor.GotoParent() ||
			!treeSitterSyntaxCursorMatches(cursor, syntaxTree.nodes[syntaxTree.root], sourceLength) {
			return nil, false
		}
		return syntaxTree, true
	}
}

func treeSitterSyntaxNodeAtCursor(
	cursor *treesitter.TreeCursor,
	sourceLength int,
	parent int,
) (treeSitterSyntaxNode, bool) {
	if cursor == nil || sourceLength < 0 {
		return treeSitterSyntaxNode{}, false
	}
	node := cursor.CurrentNode()
	startByte := node.StartByte()
	endByte := node.EndByte()
	if uint64(startByte) > uint64(sourceLength) ||
		uint64(endByte) > uint64(sourceLength) ||
		startByte > endByte {
		return treeSitterSyntaxNode{}, false
	}
	kind := node.Type()
	if kind == "" {
		return treeSitterSyntaxNode{}, false
	}
	return treeSitterSyntaxNode{
		kind:      kind,
		startByte: int(startByte),
		endByte:   int(endByte),
		parent:    parent,
	}, true
}

func treeSitterSyntaxCursorMatches(
	cursor *treesitter.TreeCursor,
	want treeSitterSyntaxNode,
	sourceLength int,
) bool {
	got, ok := treeSitterSyntaxNodeAtCursor(cursor, sourceLength, want.parent)
	return ok &&
		got.kind == want.kind &&
		got.startByte == want.startByte &&
		got.endByte == want.endByte
}

func treeSitterSyntaxRecordedAncestorAtCursor(
	ctx context.Context,
	cursor *treesitter.TreeCursor,
	syntaxTree *treeSitterSyntaxTree,
	current int,
	sourceLength int,
	operations *int,
	operationLimit int,
) (int, bool) {
	got, ok := treeSitterSyntaxNodeAtCursor(cursor, sourceLength, -1)
	if !ok {
		return -1, false
	}
	return treeSitterSyntaxRecordedAncestor(
		ctx,
		syntaxTree,
		current,
		got,
		operations,
		operationLimit,
	)
}

func treeSitterSyntaxRecordedAncestor(
	ctx context.Context,
	syntaxTree *treeSitterSyntaxTree,
	current int,
	got treeSitterSyntaxNode,
	operations *int,
	operationLimit int,
) (int, bool) {
	if syntaxTree == nil || current <= syntaxTree.root || current >= len(syntaxTree.nodes) {
		return -1, false
	}

	previous := current
	for ancestor := syntaxTree.nodes[current].parent; ancestor >= 0; {
		if ancestor >= previous || ancestor >= len(syntaxTree.nodes) ||
			!treeSitterSyntaxOperationAvailable(ctx, operations, operationLimit) {
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

func appendTreeSitterSyntaxRealignedSibling(
	ctx context.Context,
	syntaxTree *treeSitterSyntaxTree,
	node treeSitterSyntaxNode,
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
			!treeSitterSyntaxOperationAvailable(ctx, operations, operationLimit) {
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
		// containment invariant; language-specific analysis is the safe fallback.
		if node.startByte < ancestorNode.endByte {
			return -1, false
		}
		if node.startByte >= parent.startByte && node.endByte <= parent.endByte {
			node.parent = parentIndex
			if !appendTreeSitterSyntaxNode(syntaxTree, node, nodeLimit) {
				return -1, false
			}
			return len(syntaxTree.nodes) - 1, true
		}
		previous = ancestor
		ancestor = parentIndex
	}
	return -1, false
}

func appendTreeSitterSyntaxNode(
	syntaxTree *treeSitterSyntaxTree,
	node treeSitterSyntaxNode,
	nodeLimit int,
) bool {
	if syntaxTree == nil || len(syntaxTree.nodes) >= nodeLimit ||
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

func validateTreeSitterSyntaxTree(syntaxTree *treeSitterSyntaxTree, sourceLength int) bool {
	if syntaxTree == nil || sourceLength < 0 ||
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

func treeSitterSyntaxNodeLimit(sourceLength int) int {
	if sourceLength < 0 {
		return 0
	}
	maxInt := int(^uint(0) >> 1)
	if sourceLength > (maxInt-treeSitterSyntaxMinimumNodes)/treeSitterSyntaxNodesPerByte {
		return maxInt
	}
	return treeSitterSyntaxMinimumNodes + sourceLength*treeSitterSyntaxNodesPerByte
}

func treeSitterSyntaxOperationLimit(nodeLimit int) int {
	if nodeLimit < 0 {
		return 0
	}
	maxInt := int(^uint(0) >> 1)
	if nodeLimit > (maxInt-16)/4 {
		return maxInt
	}
	return nodeLimit*4 + 16
}

func treeSitterSyntaxOperationAvailable(
	ctx context.Context,
	operations *int,
	operationLimit int,
) bool {
	if ctx == nil || operations == nil || *operations < 0 ||
		*operations >= operationLimit || ctx.Err() != nil {
		return false
	}
	(*operations)++
	return true
}
