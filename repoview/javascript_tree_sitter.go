package repoview

type javascriptSyntaxNode = treeSitterSyntaxNode
type javascriptSyntaxTree = treeSitterSyntaxTree

const (
	// The pure-Go parser retains every parse-arena allocation until parsing
	// finishes. Dense generated bundles can otherwise amplify a sub-megabyte
	// source into hundreds of megabytes before the shared adapter can apply its
	// copied-node bound. The lexical backend remains complete and bounded for
	// sources outside this concrete-parser budget.
	javascriptMaximumConcreteParseBytes        = 8 << 20
	javascriptMaximumConcreteParseLexicalUnits = 64 << 10
)

// parseJavaScriptSyntax parses JavaScript and JSX with the pinned pure-Go
// grammar and exposes only the adapter's validated, position-safe tree copy.
func parseJavaScriptSyntax(source string) (*javascriptSyntaxTree, bool) {
	return parseJavaScriptSyntaxFlavor(source, javascriptSyntaxFlavorJavaScript)
}

func javascriptConcreteSyntaxAllowed(source string) bool {
	return javascriptConcreteSyntaxAllowedFlavor(source, javascriptSyntaxFlavorJavaScript)
}

func javascriptConcreteSyntaxAllowedFlavor(
	source string,
	flavor javascriptSyntaxFlavor,
) bool {
	if len(source) > javascriptMaximumConcreteParseBytes {
		return false
	}
	if len(source) <= javascriptMaximumConcreteParseLexicalUnits {
		return true
	}
	scanner := &javascriptFallbackScanner{
		source:                source,
		expressionAllowed:     true,
		logicalLineWhitespace: true,
		concreteUnitLimit:     javascriptMaximumConcreteParseLexicalUnits,
		jsxDisabled:           !flavor.permitsJSX(),
	}
	scanner.scan()
	return !scanner.concreteBudgetExceeded
}

func validateJavaScriptSyntaxTree(tree *javascriptSyntaxTree, sourceLength int) bool {
	return validateTreeSitterSyntaxTree(tree, sourceLength)
}
