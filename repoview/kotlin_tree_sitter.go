package repoview

import kotlingrammar "github.com/dkropachev/repo-view/internal/kotlingrammar"

type kotlinSyntaxNode = treeSitterSyntaxNode
type kotlinSyntaxTree = treeSitterSyntaxTree

const (
	kotlinMaximumConcreteParseBytes     = 8 << 20
	kotlinMaximumConcreteTokens         = 64 << 10
	kotlinMaximumConcreteDelimiterDepth = 128
)

// parseKotlinSyntax admits the generated grammar only after the independent
// lexical pass has bounded the parser's byte, token, and delimiter frontiers.
// Rejected inputs remain available to Kotlin's full-source lexical recovery.
func parseKotlinSyntax(
	source string,
	lexed kotlinLexResult,
) (*kotlinSyntaxTree, bool) {
	if !lexed.concreteEligible ||
		len(source) > kotlinMaximumConcreteParseBytes ||
		lexed.lexicalUnits > kotlinMaximumConcreteTokens ||
		lexed.maximumDelimiterDepth > kotlinMaximumConcreteDelimiterDepth {
		return nil, false
	}
	return parseTreeSitterSyntax(source, kotlingrammar.Language())
}

func validateKotlinSyntaxTree(tree *kotlinSyntaxTree, sourceLength int) bool {
	if !validateTreeSitterSyntaxTree(tree, sourceLength) {
		return false
	}
	root := tree.nodes[tree.root]
	return root.kind == "source_file" && root.startByte == 0 &&
		root.endByte == sourceLength
}
