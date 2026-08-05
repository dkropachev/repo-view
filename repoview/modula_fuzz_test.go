package repoview

import (
	"strings"
	"testing"
)

func FuzzModulaBackendMaintainsCoordinateContracts(f *testing.F) {
	for _, source := range []string{
		"",
		"MODULE Ready;\nVAR value: INTEGER;\nBEGIN\nEND Ready.\n",
		"DEFINITION MODULE FOR \"runtime\" API;\nTYPE Mode = (cold, hot);\nEND API.\n",
		"MODULE Records;\nTYPE Node = RECORD CASE tag OF 0: left: INTEGER | 1: right: INTEGER END;\nEND Records.\n",
		"MODULE Directives;\n<* illegal(\"*> hidden\") *>\nPROCEDURE Tail;\nBEGIN END Tail;\nBEGIN END Directives.\n",
		"MODULE Broken;\n(* unterminated\nPROCEDURE Hidden;\n",
		"MODULE Alternate;\nVAR p: POINTER TO INTEGER;\nBEGIN p@ := 1; IF ~ready THEN END END Alternate.\n",
		"MODULE CR;\r\nCONST text = \"left\rright\";\r\nEND CR.\r\n",
		string([]byte{'M', 'O', 'D', 'U', 'L', 'E', ' ', 0xff, ';', '\n'}),
	} {
		f.Add(source)
	}

	f.Fuzz(func(t *testing.T, source string) {
		if len(source) > 64<<10 {
			t.Skip()
		}
		lines := strings.Split(source, "\n")
		lexed := lexModula(source)
		if tree, ok := parseModulaSyntax(source, lexed); ok &&
			!validateModulaSyntaxTree(tree, len(source)) {
			t.Fatal("parser returned an invalid syntax tree")
		}
		analysis := analyzeModulaSource(source, len(lines))
		if analysis == nil {
			t.Fatal("analyzeModulaSource returned nil")
		}

		backend := prepareLanguageBackend(newModulaLanguage(), lines)
		modulaTestAssertDefinitionCoordinates(t, lines, backend.sourceDefinitions(lines))
		if start, end, ok := backend.importRange(lines); ok &&
			(start < 1 || end < start || end > len(lines)) {
			t.Fatalf("invalid import range: %d-%d of %d", start, end, len(lines))
		}
		for _, options := range [][2]bool{
			{false, false}, {true, false}, {false, true}, {true, true},
		} {
			searchable := backend.searchLines(lines, options[0], options[1])
			if analysis.gated {
				modulaTestAssertLineWidths(t, lines, searchable)
			} else if len(searchable) != len(lines) {
				t.Fatalf("fallback search lines = %d, want %d",
					len(searchable), len(lines))
			}
		}
		if cleaner, ok := backend.(linePreservingSourceCleaner); ok {
			for _, dropComments := range []bool{false, true} {
				if cleaned := cleaner.cleanSourceLines(
					lines, dropComments, false,
				); len(cleaned) != len(lines) {
					t.Fatalf("cleaned lines = %d, want %d", len(cleaned), len(lines))
				}
			}
		}
		for _, lineNo := range []int{1, (len(lines) + 1) / 2, len(lines)} {
			start, end := backend.enclosingScope(lines, lineNo)
			if start < 1 || start > lineNo || end < lineNo || end > len(lines) {
				t.Fatalf("invalid scope for line %d: %d-%d of %d",
					lineNo, start, end, len(lines))
			}
			_ = bestSymbolOnLine(lines, lineNo, backend)
		}
		_ = backend.cleanSource(source, true, false)
		for _, line := range lines {
			_, _ = backend.definitionSymbol(line)
			_ = backend.stripComment(line)
		}
	})
}
