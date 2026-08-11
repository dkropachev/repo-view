package navigator

import clanguage "github.com/dcosson/treesitter-go/languages/c"

type cSyntaxNode = treeSitterSyntaxNode
type cSyntaxTree = treeSitterSyntaxTree

const (
	cMaximumConcreteParseBytes         = 8 << 20
	cMaximumConcreteLexicalUnits       = 64 << 10
	cMaximumConcreteDelimiterDepth     = 128
	cMaximumConcretePreprocessorDepth  = 128
	cMaximumConcreteAdjacentAttributes = 32
	cMaximumConcreteGroupsPerSegment   = 256
	cMaximumConcreteExpressionPrefix   = 256
	cMaximumRetainedLexicalUnits       = 256 << 10
)

// parseCSyntax parses source with the pinned pure-Go C grammar. The lexical
// preflight rejects known high-amplification GLR frontiers before the shared
// parser allocates its arena; the lexical backend remains authoritative for
// sources outside these concrete-parser budgets.
func parseCSyntax(source string) (*cSyntaxTree, bool) {
	if len(source) > cMaximumConcreteParseBytes {
		return nil, false
	}
	return parseCSyntaxWithLexed(source, lexC(source))
}

func parseCSyntaxWithLexed(source string, lexed cLexResult) (*cSyntaxTree, bool) {
	if len(source) > cMaximumConcreteParseBytes || !lexed.concreteEligible {
		return nil, false
	}
	return parseTreeSitterSyntax(source, clanguage.Language())
}

func validateCSyntaxTree(tree *cSyntaxTree, sourceLength int) bool {
	return validateTreeSitterSyntaxTree(tree, sourceLength)
}
