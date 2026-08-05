package repoview

import (
	"strings"

	cpplanguage "github.com/dcosson/treesitter-go/languages/cpp"
)

type cppSyntaxTree = treeSitterSyntaxTree

// C++ has a substantially larger and more ambiguous grammar than C. Keep its
// parser budgets independent so later C++-specific frontier gates can evolve
// without silently changing C behavior. The initial byte budget is
// deliberately conservative; sources outside it remain eligible for the
// lexical backend.
const (
	cppMaximumConcreteParseBytes        = 512 << 10
	cppMaximumConcreteLexicalUnits      = 32 << 10
	cppMaximumConcreteDelimiterDepth    = 96
	cppMaximumConcreteAngleFrontier     = 128
	cppMaximumConcretePreprocessorDepth = 128
	cppMaximumRawStringDelimiterLength  = 16
)

// cppSyntaxPreflight is tied to the exact source used to construct it. That
// identity check prevents a result for a cheap source from being reused to
// bypass the resource gate for a different source.
type cppSyntaxPreflight struct {
	source            string
	lexicalUnits      int
	delimiterDepth    int
	angleFrontier     int
	preprocessorDepth int
	concreteEligible  bool
}

func parseCPPSyntax(source string) (*cppSyntaxTree, bool) {
	return parseCPPSyntaxWithPreflight(source, preflightCPPSyntax(source))
}

func parseCPPSyntaxWithPreflight(
	source string,
	preflight cppSyntaxPreflight,
) (*cppSyntaxTree, bool) {
	if !validCPPSyntaxPreflight(source, preflight) {
		return nil, false
	}
	return parseTreeSitterSyntax(source, cpplanguage.Language())
}

func validateCPPSyntaxTree(tree *cppSyntaxTree, sourceLength int) bool {
	return validateTreeSitterSyntaxTree(tree, sourceLength)
}

func validCPPSyntaxPreflight(source string, preflight cppSyntaxPreflight) bool {
	return preflight.concreteEligible && preflight.source == source &&
		len(source) <= cppMaximumConcreteParseBytes &&
		preflight.lexicalUnits >= 0 &&
		preflight.lexicalUnits <= cppMaximumConcreteLexicalUnits &&
		preflight.delimiterDepth >= 0 &&
		preflight.delimiterDepth <= cppMaximumConcreteDelimiterDepth &&
		preflight.angleFrontier >= 0 &&
		preflight.angleFrontier <= cppMaximumConcreteAngleFrontier &&
		preflight.preprocessorDepth >= 0 &&
		preflight.preprocessorDepth <= cppMaximumConcretePreprocessorDepth
}

// preflightCPPSyntax performs a bounded, allocation-light token pass before
// entering the GLR parser. It is intentionally syntax-agnostic: its job is to
// reject known amplification frontiers, not to decide whether a program is
// valid C++. Comments and literals are opaque so attacker-controlled text
// inside them cannot trip structural counters.
func preflightCPPSyntax(source string) cppSyntaxPreflight {
	preflight := cppSyntaxPreflight{source: source}
	if len(source) > cppMaximumConcreteParseBytes {
		return preflight
	}
	preflight.concreteEligible = true

	var delimiters [cppMaximumConcreteDelimiterDepth]byte
	delimiterDepth := 0
	angleDepth := 0
	preprocessorDepth := 0
	logicalLineStart := true
	directiveKeywordPending := false

	for offset := 0; offset < len(source); {
		if splice := cSpliceLength(source, offset); splice > 0 {
			offset += splice
			continue
		}
		if offset == 0 && strings.HasPrefix(source, "\uFEFF") {
			offset += len("\uFEFF")
			continue
		}
		switch source[offset] {
		case ' ', '\t', '\v', '\f':
			offset++
			continue
		case '\r':
			offset++
			if offset < len(source) && source[offset] == '\n' {
				offset++
			}
			logicalLineStart = true
			directiveKeywordPending = false
			continue
		case '\n':
			offset++
			logicalLineStart = true
			directiveKeywordPending = false
			continue
		}
		if end, _, ok := cCommentEnd(source, offset, len(source)); ok {
			if cContainsUnsplicedNewline(source, offset, end) {
				logicalLineStart = true
				directiveKeywordPending = false
			}
			offset = end
			continue
		}
		if end, ok := cppSyntaxRawStringEnd(source, offset, len(source)); ok {
			offset = end
			preflight.lexicalUnits++
			logicalLineStart = false
			directiveKeywordPending = false
		} else if end, ok := cLiteralEnd(source, offset, len(source)); ok {
			offset = end
			preflight.lexicalUnits++
			logicalLineStart = false
			directiveKeywordPending = false
		} else if end := cLogicalIdentifierEnd(source, offset); end > offset {
			if directiveKeywordPending {
				switch cLogicalText(source, offset, end) {
				case "if", "ifdef", "ifndef":
					preprocessorDepth++
					preflight.preprocessorDepth = max(
						preflight.preprocessorDepth,
						preprocessorDepth,
					)
					if preprocessorDepth > cppMaximumConcretePreprocessorDepth {
						preflight.concreteEligible = false
						return preflight
					}
				case "endif":
					preprocessorDepth = max(0, preprocessorDepth-1)
				}
			}
			offset = end
			preflight.lexicalUnits++
			logicalLineStart = false
			directiveKeywordPending = false
		} else if cLogicalNumberStart(source, offset) {
			offset = cLogicalNumberEnd(source, offset)
			preflight.lexicalUnits++
			logicalLineStart = false
			directiveKeywordPending = false
		} else {
			var token string
			token, offset = cppSyntaxPunctuationAt(source, offset)
			preflight.lexicalUnits++
			directiveKeywordPending = logicalLineStart && token == "#"
			logicalLineStart = false

			switch token {
			case "(", "[", "{":
				if delimiterDepth >= len(delimiters) {
					preflight.concreteEligible = false
					return preflight
				}
				delimiters[delimiterDepth] = token[0]
				delimiterDepth++
				preflight.delimiterDepth = max(
					preflight.delimiterDepth,
					delimiterDepth,
				)
			case ")", "]", "}":
				if delimiterDepth == 0 ||
					!cDelimiterCloses(delimiters[delimiterDepth-1], token[0]) {
					preflight.concreteEligible = false
					return preflight
				}
				delimiterDepth--
			}

			switch token {
			case "<":
				angleDepth++
			case "<<":
				angleDepth += 2
			case ">":
				angleDepth = max(0, angleDepth-1)
			case ">>":
				angleDepth = max(0, angleDepth-2)
			case ";", "{", "}":
				angleDepth = 0
			}
			preflight.angleFrontier = max(preflight.angleFrontier, angleDepth)
			if preflight.angleFrontier > cppMaximumConcreteAngleFrontier {
				preflight.concreteEligible = false
				return preflight
			}
		}

		if preflight.lexicalUnits > cppMaximumConcreteLexicalUnits {
			preflight.concreteEligible = false
			return preflight
		}
	}
	if delimiterDepth != 0 {
		preflight.concreteEligible = false
	}
	return preflight
}

func cppSyntaxPunctuationAt(source string, start int) (string, int) {
	for _, punctuation := range []string{"->*", "<=>", "::", ".*"} {
		if end, ok := cMatchLogical(source, start, punctuation, len(source)); ok {
			return punctuation, end
		}
	}
	return cPunctuationAt(source, start)
}

// cppSyntaxRawStringEnd recognizes the C++ raw-string envelope without
// interpreting its body. A missing terminator consumes the remaining source;
// the concrete grammar can then report recovery without structural bytes in
// the body inflating the preflight frontier.
func cppSyntaxRawStringEnd(source string, start, limit int) (int, bool) {
	if start < 0 || start >= limit || limit > len(source) {
		return start, false
	}
	prefixEnd := -1
	for _, prefix := range []string{`u8R"`, `LR"`, `uR"`, `UR"`, `R"`} {
		if strings.HasPrefix(source[start:limit], prefix) {
			prefixEnd = start + len(prefix)
			break
		}
	}
	if prefixEnd < 0 {
		return start, false
	}

	delimiterEnd := prefixEnd
	for delimiterEnd < limit && source[delimiterEnd] != '(' {
		if delimiterEnd-prefixEnd >= cppMaximumRawStringDelimiterLength ||
			!cppSyntaxRawDelimiterByte(source[delimiterEnd]) {
			return start, false
		}
		delimiterEnd++
	}
	if delimiterEnd >= limit {
		return limit, true
	}

	delimiter := source[prefixEnd:delimiterEnd]
	bodyStart := delimiterEnd + 1
	terminator := ")" + delimiter + `"`
	relative := strings.Index(source[bodyStart:limit], terminator)
	if relative < 0 {
		return limit, true
	}
	return bodyStart + relative + len(terminator), true
}

func cppSyntaxRawDelimiterByte(character byte) bool {
	return character >= 0x21 && character <= 0x7e &&
		character != '(' && character != ')' && character != '\\'
}
