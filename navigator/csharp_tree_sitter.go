package navigator

import csharpgrammar "github.com/scopesifter/scopesifter/internal/csharpgrammar"

// parseCSharpSyntax gates the official C# grammar behind the lexical resource
// frontiers. Inputs outside those frontiers are handled by full-source bounded
// recovery without allocating a concrete parse arena.
func parseCSharpSyntax(
	source string,
	lexed csharpLexResult,
) (*csharpSyntaxTree, bool) {
	if !lexed.concreteEligible || len(source) > csharpMaximumConcreteParseBytes ||
		lexed.lexicalUnits > csharpMaximumConcreteTokens {
		return nil, false
	}
	return parseTreeSitterSyntax(source, csharpgrammar.Language())
}
