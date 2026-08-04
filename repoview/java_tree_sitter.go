package repoview

import javalanguage "github.com/dcosson/treesitter-go/languages/java"

type javaSyntaxNode = treeSitterSyntaxNode
type javaSyntaxTree = treeSitterSyntaxTree

const (
	javaMaximumConcreteParseBytes = 8 << 20
	javaMaximumConcreteTokens     = 64 << 10
	// Deep malformed delimiter, generic-angle, and switch-label nests make the
	// GLR recovery frontier grow quadratically. Keep that work bounded and let
	// the lexical analyzer, which has its own depth caps, recover the source.
	javaMaximumConcreteDelimiterDepth = 128
	javaMaximumConcreteGenericDepth   = 128
	javaMaximumConcreteLabelsPerBrace = 128
)

type javaConcreteDelimiterFrame struct {
	openerIndex int
	labels      int
	angles      int
	delimiter   byte
	switchBody  bool
}

// parseJavaSyntax parses Java with the pinned pure-Go grammar and exposes only
// the adapter's validated, position-safe tree copy. Token and delimiter
// budgets keep adversarial recovery frontiers from expanding into an
// unbounded tree.
func parseJavaSyntax(source string) (*javaSyntaxTree, bool) {
	if len(source) > javaMaximumConcreteParseBytes ||
		javaLexicalTokenCount(source, javaMaximumConcreteTokens+1) > javaMaximumConcreteTokens {
		return nil, false
	}
	lexed := lexJava(source)
	if lexed.truncated || !javaConcreteDelimiterDepthEligible(lexed.tokens) {
		return nil, false
	}
	return parseJavaSyntaxWithinBudget(source)
}

func javaConcreteDelimiterDepthEligible(tokens []javaToken) bool {
	var stack [javaMaximumConcreteDelimiterDepth]javaConcreteDelimiterFrame
	depth := 0
	genericDepth := 0
	genericBaseDepth := 0
	globalAngles := 0
	previousClosedParen := -1
	for index, token := range tokens {
		if token.gap {
			return false
		}

		// Bound every potential label under a stable brace owner. The owner
		// deliberately sends giant valid switches (and very large groups
		// of malformed labels) to lexical analysis without guessing whether a
		// colon or arrow belongs to a ternary, lambda, or the switch grammar. A
		// recognized switch body remains the owner below nested recovery blocks;
		// otherwise the nearest brace is the conservative fallback owner.
		if token.value == "case" || token.value == "default" {
			owner := -1
			for frameIndex := depth - 1; frameIndex >= 0; frameIndex-- {
				if stack[frameIndex].switchBody {
					owner = frameIndex
					break
				}
			}
			if owner < 0 {
				owner = javaConcreteNearestBrace(&stack, depth)
			}
			if owner >= 0 {
				stack[owner].labels++
				if stack[owner].labels > javaMaximumConcreteLabelsPerBrace {
					return false
				}
			}
		}

		matchedParen := -1
		switch token.value {
		case "(", "[", "{":
			if depth == len(stack) {
				return false
			}
			switchBody := token.value == "{" && index > 0 &&
				tokens[index-1].value == ")" && previousClosedParen > 0 &&
				tokens[previousClosedParen-1].value == "switch"
			stack[depth] = javaConcreteDelimiterFrame{
				openerIndex: index,
				delimiter:   token.value[0],
				switchBody:  switchBody,
			}
			depth++
		case ")":
			if depth > 0 && stack[depth-1].delimiter == '(' {
				matchedParen = stack[depth-1].openerIndex
				depth--
			}
		case "]":
			if depth > 0 && stack[depth-1].delimiter == '[' {
				depth--
			}
		case "}":
			if depth > 0 && stack[depth-1].delimiter == '{' {
				depth--
			}
		case "<":
			if genericDepth == 0 {
				genericBaseDepth = depth
			}
			if genericDepth == javaMaximumConcreteGenericDepth {
				return false
			}
			genericDepth++
			owner := javaConcreteNearestBrace(&stack, depth)
			if owner >= 0 {
				if stack[owner].angles == javaMaximumConcreteGenericDepth {
					return false
				}
				stack[owner].angles++
			} else {
				if globalAngles == javaMaximumConcreteGenericDepth {
					return false
				}
				globalAngles++
			}
		case ">", ">>", ">>>":
			genericDepth = max(0, genericDepth-len(token.value))
		}
		if genericDepth > 0 && depth < genericBaseDepth {
			genericDepth = 0
		}
		if genericDepth > 0 && depth <= genericBaseDepth &&
			javaConcreteGenericFrontierBoundary(token.value) {
			genericDepth = 0
		}
		if token.value == ";" {
			owner := javaConcreteNearestBrace(&stack, depth)
			if owner >= 0 {
				stack[owner].angles = 0
			} else {
				globalAngles = 0
			}
		} else if (token.value == "{" && depth == 1) ||
			(token.value == "}" && depth == 0) {
			globalAngles = 0
		}
		previousClosedParen = matchedParen
	}
	return true
}

func javaConcreteNearestBrace(
	stack *[javaMaximumConcreteDelimiterDepth]javaConcreteDelimiterFrame,
	depth int,
) int {
	for frameIndex := min(depth, len(stack)) - 1; frameIndex >= 0; frameIndex-- {
		if stack[frameIndex].delimiter == '{' {
			return frameIndex
		}
	}
	return -1
}

func javaConcreteGenericFrontierBoundary(value string) bool {
	switch value {
	case ";", "=", "+=", "-=", "*=", "/=", "%=", "&=", "|=", "^=",
		"<<=", ">>=", ">>>=", "||", "&&", "==", "!=", "<=", ">=",
		"+", "-", "*", "/", "%", "|", "^", "!", "~", "++", "--", "->":
		return true
	default:
		return false
	}
}

func parseJavaSyntaxWithinBudget(source string) (*javaSyntaxTree, bool) {
	return parseTreeSitterSyntax(source, javalanguage.Language())
}

func validateJavaSyntaxTree(tree *javaSyntaxTree, sourceLength int) bool {
	return validateTreeSitterSyntaxTree(tree, sourceLength)
}
