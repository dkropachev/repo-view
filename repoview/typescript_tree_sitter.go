package repoview

import (
	javascriptlanguage "github.com/dcosson/treesitter-go/languages/javascript"
	tsxlanguage "github.com/dcosson/treesitter-go/languages/tsx"
	typescriptlanguage "github.com/dcosson/treesitter-go/languages/typescript"
)

type javascriptSyntaxFlavor uint8

const (
	javascriptSyntaxFlavorJavaScript javascriptSyntaxFlavor = iota
	javascriptSyntaxFlavorTypeScript
	javascriptSyntaxFlavorTSX
)

func (flavor javascriptSyntaxFlavor) isTypeScript() bool {
	return flavor == javascriptSyntaxFlavorTypeScript || flavor == javascriptSyntaxFlavorTSX
}

func (flavor javascriptSyntaxFlavor) permitsJSX() bool {
	return flavor != javascriptSyntaxFlavorTypeScript
}

func parseTypeScriptSyntax(source string, tsx bool) (*javascriptSyntaxTree, bool) {
	flavor := javascriptSyntaxFlavorTypeScript
	if tsx {
		flavor = javascriptSyntaxFlavorTSX
	}
	return parseJavaScriptSyntaxFlavor(source, flavor)
}

func parseJavaScriptSyntaxFlavor(
	source string,
	flavor javascriptSyntaxFlavor,
) (*javascriptSyntaxTree, bool) {
	if !javascriptConcreteSyntaxAllowedFlavor(source, flavor) {
		return nil, false
	}
	switch flavor {
	case javascriptSyntaxFlavorTypeScript:
		return parseTreeSitterSyntax(source, typescriptlanguage.Language())
	case javascriptSyntaxFlavorTSX:
		return parseTreeSitterSyntax(source, tsxlanguage.Language())
	case javascriptSyntaxFlavorJavaScript:
		return parseTreeSitterSyntax(source, javascriptlanguage.Language())
	default:
		return nil, false
	}
}

func typeScriptSyntaxMissingTokenSpans(
	tree *javascriptSyntaxTree,
	sourceLength int,
) []javascriptByteSpan {
	if tree == nil || sourceLength < 1 {
		return nil
	}
	spans := make([]javascriptByteSpan, 0)
	for _, node := range tree.nodes {
		if node.startByte != node.endByte || node.startByte < 0 ||
			node.startByte > sourceLength {
			continue
		}
		switch node.kind {
		case ")", "]", "}", ">", ";":
		default:
			continue
		}
		start := max(0, min(node.startByte, sourceLength)-1)
		end := min(sourceLength, max(node.endByte+1, start+1))
		if start < end {
			spans = append(spans, javascriptByteSpan{start: start, end: end})
		}
	}
	return normalizeJavaScriptSpans(spans)
}
