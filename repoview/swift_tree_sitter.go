package repoview

import swiftgrammar "github.com/dkropachev/repo-view/internal/swiftgrammar"

type swiftSyntaxNode = treeSitterSyntaxNode
type swiftSyntaxTree = treeSitterSyntaxTree

const (
	swiftMaximumConcreteParseBytes     = 8 << 20
	swiftMaximumConcreteTokens         = 64 << 10
	swiftMaximumConcreteDelimiterDepth = 128
)

// parseSwiftSyntax admits the generated grammar only after the independent
// lexical pass has bounded the parser's byte, token, delimiter, and directive
// frontiers. Rejected inputs remain available to Swift's full-source recovery.
func parseSwiftSyntax(source string, lexed swiftLexResult) (*swiftSyntaxTree, bool) {
	if !lexed.concreteEligible ||
		len(source) > swiftMaximumConcreteParseBytes ||
		lexed.lexicalUnits > swiftMaximumConcreteTokens ||
		lexed.maximumDelimiterDepth > swiftMaximumConcreteDelimiterDepth {
		return nil, false
	}
	return parseTreeSitterSyntax(source, swiftgrammar.Language())
}

func validateSwiftSyntaxTree(tree *swiftSyntaxTree, sourceLength int) bool {
	if !validateTreeSitterSyntaxTree(tree, sourceLength) {
		return false
	}
	root := tree.nodes[tree.root]
	return root.kind == "source_file" && root.startByte == 0 &&
		root.endByte == sourceLength
}
