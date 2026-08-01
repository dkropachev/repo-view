package repoview

import (
	"context"

	pythonlanguage "github.com/dcosson/treesitter-go/languages/python"
)

// These aliases preserve the Python backend's established internal API while
// the guarded TreeCursor copy is shared by every pure-Go grammar adapter.
type pythonSyntaxNode = treeSitterSyntaxNode
type pythonSyntaxTree = treeSitterSyntaxTree

func parsePythonSyntax(source string) (*pythonSyntaxTree, bool) {
	return parseTreeSitterSyntax(source, pythonlanguage.Language())
}

//nolint:unparam // Retains the focused compatibility seam used by parser invariant tests.
func pythonSyntaxRecordedAncestor(
	ctx context.Context,
	syntaxTree *pythonSyntaxTree,
	current int,
	got pythonSyntaxNode,
	operations *int,
	operationLimit int,
) (int, bool) {
	return treeSitterSyntaxRecordedAncestor(
		ctx,
		syntaxTree,
		current,
		got,
		operations,
		operationLimit,
	)
}

//nolint:unparam // Retains the focused compatibility seam used by parser invariant tests.
func appendPythonSyntaxRealignedSibling(
	ctx context.Context,
	syntaxTree *pythonSyntaxTree,
	node pythonSyntaxNode,
	firstAncestor int,
	nodeLimit int,
	operations *int,
	operationLimit int,
) (int, bool) {
	return appendTreeSitterSyntaxRealignedSibling(
		ctx,
		syntaxTree,
		node,
		firstAncestor,
		nodeLimit,
		operations,
		operationLimit,
	)
}

func validatePythonSyntaxTree(syntaxTree *pythonSyntaxTree, sourceLength int) bool {
	return validateTreeSitterSyntaxTree(syntaxTree, sourceLength)
}
