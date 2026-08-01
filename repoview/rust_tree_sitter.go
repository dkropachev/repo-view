package repoview

import rustlanguage "github.com/dcosson/treesitter-go/languages/rust"

type rustSyntaxNode = treeSitterSyntaxNode
type rustSyntaxTree = treeSitterSyntaxTree

// parseRustSyntax parses Rust with the pure-Go grammar and returns only the
// adapter's validated, position-safe tree copy.
func parseRustSyntax(source string) (*rustSyntaxTree, bool) {
	return parseTreeSitterSyntax(source, rustlanguage.Language())
}

func validateRustSyntaxTree(syntaxTree *rustSyntaxTree, sourceLength int) bool {
	return validateTreeSitterSyntaxTree(syntaxTree, sourceLength)
}
