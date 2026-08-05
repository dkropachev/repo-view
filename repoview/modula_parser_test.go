package repoview

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestModulaConcreteTreeExtractsDeclarationsScopesAndImports(t *testing.T) {
	t.Parallel()

	const source = `MODULE Concrete;
IMPORT IO;
FROM MathLib IMPORT Sum;

CONST Limit = 10;
TYPE
  Mode = (read, write);
  Entry = RECORD
    key: INTEGER;
    CASE mode: Mode OF
      read: input: INTEGER |
      write: output: INTEGER
    END
  END;
  Callback = PROCEDURE (INTEGER, VAR CARDINAL): BOOLEAN;
VAR current: Entry;

PROCEDURE Run(value: INTEGER);
VAR local: INTEGER;
BEGIN
  IF value > 0 THEN
    local := value
  END
END Run;

BEGIN
  Run(Limit)
END Concrete.
`
	lines := modulaTestLines(source)
	tree := modulaTreeTestParse(t, source)
	if spans := modulaSyntaxErrorSpans(tree, len(source)); len(spans) != 0 {
		t.Fatalf("valid concrete Modula-2 recovery spans = %#v, want none", spans)
	}

	definitions := modulaTreeDefinitions(source, len(lines), tree)
	want := []string{
		"Concrete", "Limit", "Mode", "read", "write", "Entry", "key", "mode",
		"input", "output", "Callback", "current", "Run", "local",
	}
	if got := modulaTestDefinitionSymbols(definitions); !slices.Equal(got, want) {
		t.Fatalf("concrete definitions =\n%#v\nwant\n%#v", got, want)
	}
	for _, forbidden := range []string{
		"IO", "MathLib", "Sum", "INTEGER", "CARDINAL", "BOOLEAN", "value",
	} {
		if slices.Contains(modulaTestDefinitionSymbols(definitions), forbidden) {
			t.Errorf("non-declaration %q became a concrete definition: %#v",
				forbidden, definitions)
		}
	}
	modulaTestAssertDefinitionCoordinates(t, lines, definitions)

	module := modulaTestFirstDefinition(t, definitions, "Concrete")
	if !module.ownsScope || module.scopeStart != 1 || module.scopeEnd != len(lines) {
		t.Fatalf("concrete module scope = %#v, want 1-%d", module, len(lines))
	}
	entry := modulaTestFirstDefinition(t, definitions, "Entry")
	entryStart := modulaTestLineContaining(t, lines, "Entry = RECORD")
	entryEnd := modulaTestLineAfter(t, lines, entryStart, "END;")
	if !entry.ownsScope || entry.scopeStart != entryStart || entry.scopeEnd != entryEnd {
		t.Fatalf("concrete record type = %#v, want %d-%d", entry, entryStart, entryEnd)
	}
	run := modulaTestFirstDefinition(t, definitions, "Run")
	runStart := modulaTestLineContaining(t, lines, "PROCEDURE Run")
	runEnd := modulaTestLineContaining(t, lines, "END Run")
	if !run.ownsScope || run.scopeStart != runStart || run.scopeEnd != runEnd {
		t.Fatalf("concrete procedure = %#v, want %d-%d", run, runStart, runEnd)
	}

	wantImports := []cLineSpan{{start: 2, end: 2}, {start: 3, end: 3}}
	if got := modulaTreeImports(source, len(lines), tree); !reflect.DeepEqual(got, wantImports) {
		t.Fatalf("concrete imports = %#v, want %#v", got, wantImports)
	}

	scopes := modulaTreeScopes(source, len(lines), tree)
	for _, wantScope := range []cLineScope{
		{start: 1, end: len(lines)},
		{start: entryStart, end: entryEnd},
		{start: runStart, end: runEnd},
		{
			start: modulaTestLineContaining(t, lines, "IF value > 0 THEN"),
			end:   modulaTestLineAfter(t, lines, runStart, "  END"),
		},
	} {
		if !slices.Contains(scopes, wantScope) {
			t.Errorf("concrete scopes do not contain %#v: %#v", wantScope, scopes)
		}
	}
}

func TestModulaConcreteTreeCoversEveryCompilationUnitForm(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		source string
		want   []string
	}{
		{
			name: "program module",
			source: `MODULE Program;
PROCEDURE Work;
BEGIN
END Work;
BEGIN
END Program.
`,
			want: []string{"Program", "Work"},
		},
		{
			name: "definition module",
			source: `DEFINITION MODULE FOR "POSIX" Contract;
TYPE Handle;
PROCEDURE Open(VAR handle: Handle);
END Contract.
`,
			want: []string{"Contract", "Handle", "Open"},
		},
		{
			name: "implementation module",
			source: `IMPLEMENTATION MODULE Contract[5];
TYPE Handle = POINTER TO INTEGER;
PROCEDURE Open(VAR handle: Handle);
BEGIN
END Open;
BEGIN
END Contract.
`,
			want: []string{"Contract", "Handle", "Open"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			tree := modulaTreeTestParse(t, test.source)
			if spans := modulaSyntaxErrorSpans(tree, len(test.source)); len(spans) != 0 {
				t.Fatalf("valid unit recovery spans = %#v, want none", spans)
			}
			lines := modulaTestLines(test.source)
			definitions := modulaTreeDefinitions(test.source, len(lines), tree)
			if got := modulaTestDefinitionSymbols(definitions); !slices.Equal(got, test.want) {
				t.Fatalf("concrete definitions = %#v, want %#v", got, test.want)
			}
			modulaTestAssertDefinitionCoordinates(t, lines, definitions)
		})
	}
}

func TestModulaConcreteTreeRetainsStrictDeclarationNodeKindsAndNameAnchors(t *testing.T) {
	t.Parallel()

	const source = `MODULE Nodes;
IMPORT IO;
CONST Limit = 1;
TYPE
  Mode = (off, on);
  Entry = RECORD
    field: INTEGER;
    CASE tag: Mode OF
      off: disabled: INTEGER |
      on: enabled: INTEGER
    END
  END;
VAR current: Entry;
PROCEDURE Work;
BEGIN
END Work;
BEGIN
END Nodes.
`
	tree := modulaTreeTestParse(t, source)
	if spans := modulaSyntaxErrorSpans(tree, len(source)); len(spans) != 0 {
		t.Fatalf("strict-node fixture recovery spans = %#v, want none", spans)
	}
	wantMinimum := map[string]int{
		"module_declaration":       1,
		"import_clause":            1,
		"constant_declaration":     1,
		"type_declaration":         1,
		"record_type_declaration":  1,
		"enum_member":              2,
		"record_field_declaration": 3,
		"variant_tag_declaration":  1,
		"variable_declaration":     1,
		"procedure_declaration":    1,
		"block":                    2,
	}
	counts := make(map[string]int, len(wantMinimum))
	anchoredKinds := map[string]bool{
		"module_declaration": true, "constant_declaration": true,
		"type_declaration": true, "record_type_declaration": true,
		"enum_member":              true,
		"record_field_declaration": true, "variant_tag_declaration": true,
		"variable_declaration": true, "procedure_declaration": true,
	}
	for nodeIndex, node := range tree.nodes {
		counts[node.kind]++
		if !anchoredKinds[node.kind] {
			continue
		}
		anchored := false
		for _, childIndex := range node.children {
			if childIndex < 0 || childIndex >= len(tree.nodes) {
				t.Fatalf("node %d has invalid child %d", nodeIndex, childIndex)
			}
			child := tree.nodes[childIndex]
			if child.kind != "identifier" {
				continue
			}
			if child.startByte < 0 || child.endByte <= child.startByte ||
				child.endByte > len(source) {
				t.Fatalf("node %d has invalid identifier anchor %#v", nodeIndex, child)
			}
			anchored = true
			break
		}
		if !anchored {
			t.Errorf("strict %s node %d has no direct identifier anchor: %#v",
				node.kind, nodeIndex, node)
		}
	}
	for kind, minimum := range wantMinimum {
		if counts[kind] < minimum {
			t.Errorf("strict tree has %d %s nodes, want at least %d: %#v",
				counts[kind], kind, minimum, counts)
		}
	}
}

func TestModulaConcreteLocalModuleAndProcedureKindsDoNotPromoteUses(t *testing.T) {
	t.Parallel()

	const source = `MODULE Owners;
TYPE Callback = PROCEDURE (INTEGER): BOOLEAN;
VAR callback: Callback;

MODULE Local;
EXPORT QUALIFIED Work;
PROCEDURE Work(value: INTEGER);
BEGIN
END Work;
BEGIN
  Work(1)
END Local;

PROCEDURE Forwarded(value: INTEGER); FORWARD;
PROCEDURE Forwarded(value: INTEGER);
BEGIN
  callback(value)
END Forwarded;

BEGIN
  Forwarded(1)
END Owners.
`
	lines := modulaTestLines(source)
	tree := modulaTreeTestParse(t, source)
	if spans := modulaSyntaxErrorSpans(tree, len(source)); len(spans) != 0 {
		t.Fatalf("valid owner fixture recovery spans = %#v, want none", spans)
	}
	definitions := modulaTreeDefinitions(source, len(lines), tree)
	if got, want := modulaTestDefinitionSymbols(definitions), []string{
		"Owners", "Callback", "callback", "Local", "Work", "Forwarded", "Forwarded",
	}; !slices.Equal(got, want) {
		t.Fatalf("owner definitions = %#v, want %#v", got, want)
	}
	if got := modulaTestOwningDefinitionCount(definitions, "Forwarded"); got != 1 {
		t.Fatalf("Forwarded owning definitions = %d, want implemented declaration only", got)
	}
	for _, forbidden := range []string{"INTEGER", "BOOLEAN", "value"} {
		if slices.Contains(modulaTestDefinitionSymbols(definitions), forbidden) {
			t.Errorf("procedure type/formal %q became definition: %#v", forbidden, definitions)
		}
	}
	modulaTestAssertDefinitionCoordinates(t, lines, definitions)
}

func TestModulaConcreteRecoveryRejectsMalformedHeadsAndKeepsIndependentTail(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		body      string
		forbidden []string
	}{
		{
			name: "constant missing name",
			body: `CONST = 1;
PROCEDURE Tail;
BEGIN
END Tail;`,
			forbidden: []string{"CONST"},
		},
		{
			name: "type missing equals",
			body: `TYPE Broken RECORD field: INTEGER END;
PROCEDURE Tail;
BEGIN
END Tail;`,
			forbidden: []string{"Broken", "field"},
		},
		{
			name: "variable missing type",
			body: `VAR Broken:;
PROCEDURE Tail;
BEGIN
END Tail;`,
			forbidden: []string{"Broken"},
		},
		{
			name: "procedure missing separator",
			body: `PROCEDURE Broken(value INTEGER);
BEGIN
END Broken;
PROCEDURE Tail;
BEGIN
END Tail;`,
			forbidden: []string{"Broken"},
		},
		{
			name: "forward declaration has payload",
			body: `PROCEDURE Broken;
FORWARD junk + more;
PROCEDURE Tail;
BEGIN
END Tail;`,
			forbidden: []string{"junk", "more"},
		},
		{
			name: "import missing module",
			body: `FROM IMPORT Hidden;
PROCEDURE Tail;
BEGIN
END Tail;`,
			forbidden: []string{"Hidden"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			source := "MODULE Recovery;\n" + test.body + "\nBEGIN\nEND Recovery.\n"
			tree := modulaTreeTestParseRecovery(t, source)
			if spans := modulaSyntaxErrorSpans(tree, len(source)); len(spans) == 0 {
				t.Fatal("malformed concrete fixture has no recovery span")
			}
			lines := modulaTestLines(source)
			definitions := modulaTreeDefinitions(source, len(lines), tree)
			symbols := modulaTestDefinitionSymbols(definitions)
			for _, required := range []string{"Recovery", "Tail"} {
				if !slices.Contains(symbols, required) {
					t.Errorf("concrete recovery lost %q: %#v", required, definitions)
				}
			}
			for _, forbidden := range test.forbidden {
				if slices.Contains(symbols, forbidden) {
					t.Errorf("malformed declaration promoted %q: %#v", forbidden, definitions)
				}
			}
			modulaTestAssertDefinitionCoordinates(t, lines, definitions)

			analysis := analyzeModulaSource(source, len(lines))
			if analysis == nil {
				t.Fatal("analyzeModulaSource returned nil")
			}
			merged := modulaTestDefinitionSymbols(analysis.definitions)
			if !slices.Contains(merged, "Tail") {
				t.Errorf("merged recovery lost Tail: %#v", analysis.definitions)
			}
			for _, forbidden := range test.forbidden {
				if slices.Contains(merged, forbidden) {
					t.Errorf("merged recovery promoted %q: %#v", forbidden, analysis.definitions)
				}
			}
		})
	}
}

func TestModulaStrictImportGrammarAndDefinitionPlacementRecovery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		source        string
		forbiddenDefs []string
	}{
		{
			name: "dotted import",
			source: `MODULE InvalidImport;
IMPORT Foo.Bar;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END InvalidImport.
`,
		},
		{
			name: "aliased import",
			source: `MODULE InvalidImport;
IMPORT Foo := Alias;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END InvalidImport.
`,
		},
		{
			name: "qualified import",
			source: `MODULE InvalidImport;
IMPORT QUALIFIED Foo;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END InvalidImport.
`,
		},
		{
			name: "dotted from module",
			source: `MODULE InvalidImport;
FROM Foo.Bar IMPORT Item;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END InvalidImport.
`,
		},
		{
			name: "import after declaration section",
			source: `MODULE LateImport;
CONST Value = 1;
IMPORT Hidden;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END LateImport.
`,
		},
		{
			name: "local module in definition module",
			source: `DEFINITION MODULE InvalidDefinition;
MODULE Local;
IMPORT Hidden;
END Local;
PROCEDURE Tail;
END InvalidDefinition.
`,
			forbiddenDefs: []string{"Local"},
		},
		{
			name: "begin block in definition module",
			source: `DEFINITION MODULE InvalidDefinition;
BEGIN
END;
PROCEDURE Tail;
END InvalidDefinition.
`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			tree := modulaTreeTestParseRecovery(t, test.source)
			if spans := modulaSyntaxErrorSpans(tree, len(test.source)); len(spans) == 0 {
				t.Fatal("invalid import/placement fixture has no concrete recovery evidence")
			}
			lines := modulaTestLines(test.source)
			if imports := modulaTreeImports(test.source, len(lines), tree); len(imports) != 0 {
				t.Errorf("invalid/late concrete imports = %#v, want none", imports)
			}
			analysis := analyzeModulaSource(test.source, len(lines))
			if analysis == nil {
				t.Fatal("analyzeModulaSource returned nil")
			}
			if len(analysis.imports) != 0 {
				t.Errorf("invalid/late merged imports = %#v, want none", analysis.imports)
			}
			symbols := modulaTestDefinitionSymbols(analysis.definitions)
			if !slices.Contains(symbols, "Tail") {
				t.Errorf("invalid import/placement recovery lost Tail: %#v", analysis.definitions)
			}
			for _, forbidden := range test.forbiddenDefs {
				if slices.Contains(symbols, forbidden) {
					t.Errorf("invalid placement promoted %q: %#v", forbidden, analysis.definitions)
				}
			}
			modulaTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
		})
	}
}

func TestModulaExportAllowedContextsAreRecoveryFree(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		source string
		want   []string
	}{
		{
			name: "definition module",
			source: `DEFINITION MODULE PublicAPI;
IMPORT IO;
EXPORT QUALIFIED PublicValue, Run;
CONST PublicValue = 1;
PROCEDURE Run;
END PublicAPI.
`,
			want: []string{"PublicAPI", "PublicValue", "Run"},
		},
		{
			name: "local module",
			source: `MODULE Container;
MODULE Local;
IMPORT IO;
EXPORT PublicValue, Run;
CONST PublicValue = 1;
PROCEDURE Run;
BEGIN
END Run;
BEGIN
END Local;
BEGIN
END Container.
`,
			want: []string{"Container", "Local", "PublicValue", "Run"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			tree := modulaTreeTestParse(t, test.source)
			if spans := modulaSyntaxErrorSpans(tree, len(test.source)); len(spans) != 0 {
				t.Fatalf("valid EXPORT recovery spans = %#v, want none", spans)
			}
			lines := modulaTestLines(test.source)
			for name, definitions := range map[string][]sourceDefinition{
				"concrete": modulaTreeDefinitions(test.source, len(lines), tree),
				"fallback": analyzeModulaLexically(test.source, len(lines)).definitions,
			} {
				if got := modulaTestDefinitionSymbols(definitions); !slices.Equal(got, test.want) {
					t.Errorf("%s EXPORT definitions = %#v, want %#v", name, got, test.want)
				}
				modulaTestAssertDefinitionCoordinates(t, lines, definitions)
			}
		})
	}
}

func TestModulaExportInvalidContextsAndPlacementRecoverTail(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		source string
	}{
		{
			name: "program module",
			source: `MODULE InvalidExport;
EXPORT QUALIFIED Tail;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END InvalidExport.
`,
		},
		{
			name: "implementation module",
			source: `IMPLEMENTATION MODULE InvalidExport;
EXPORT QUALIFIED Tail;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END InvalidExport.
`,
		},
		{
			name: "late definition export",
			source: `DEFINITION MODULE InvalidExport;
CONST Value = 1;
EXPORT QUALIFIED Tail;
PROCEDURE Tail;
END InvalidExport.
`,
		},
		{
			name: "repeated definition export",
			source: `DEFINITION MODULE InvalidExport;
EXPORT QUALIFIED Value;
EXPORT UNQUALIFIED Tail;
CONST Value = 1;
PROCEDURE Tail;
END InvalidExport.
`,
		},
		{
			name: "procedure export",
			source: `MODULE InvalidExport;
PROCEDURE Owner;
EXPORT QUALIFIED Hidden;
BEGIN
END Owner;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END InvalidExport.
`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			tree := modulaTreeTestParseRecovery(t, test.source)
			if spans := modulaSyntaxErrorSpans(tree, len(test.source)); len(spans) == 0 {
				t.Fatal("invalid EXPORT placement has no concrete recovery evidence")
			}
			lines := modulaTestLines(test.source)
			analysis := analyzeModulaSource(test.source, len(lines))
			if analysis == nil {
				t.Fatal("analyzeModulaSource returned nil")
			}
			if !slices.Contains(modulaTestDefinitionSymbols(analysis.definitions), "Tail") {
				t.Errorf("invalid EXPORT recovery lost Tail: %#v", analysis.definitions)
			}
			modulaTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
		})
	}
}

func TestModulaSyntaxErrorSpansCoverErrorAndMissingNodes(t *testing.T) {
	t.Parallel()

	tree := &modulaSyntaxTree{
		root: 0,
		nodes: []modulaSyntaxNode{
			{kind: "source_file", startByte: 0, endByte: 12, parent: -1, children: []int{1, 2}},
			{kind: "ERROR", startByte: 2, endByte: 5, parent: 0},
			{kind: "identifier", startByte: 9, endByte: 9, parent: 0},
		},
	}
	if !validateModulaSyntaxTree(tree, 12) {
		t.Fatal("synthetic Modula-2 syntax tree is invalid")
	}
	want := []cByteSpan{{start: 2, end: 5}, {start: 9, end: 10}}
	if got := modulaSyntaxErrorSpans(tree, 12); !reflect.DeepEqual(got, want) {
		t.Fatalf("recovery spans = %#v, want %#v", got, want)
	}
}

func TestModulaConcreteParserResourceGates(t *testing.T) {
	t.Parallel()

	const source = "MODULE Kept; END Kept.\n"
	for _, test := range []struct {
		name  string
		lexed modulaLexResult
	}{
		{
			name:  "ineligible",
			lexed: modulaLexResult{lexicalUnits: 5},
		},
		{
			name: "token limit",
			lexed: modulaLexResult{
				concreteEligible: true,
				lexicalUnits:     modulaMaximumConcreteTokens + 1,
			},
		},
		{
			name: "comment depth limit",
			lexed: modulaLexResult{
				concreteEligible:    true,
				maximumCommentDepth: modulaMaximumCommentDepth + 1,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if tree, ok := parseModulaSyntax(source, test.lexed); ok || tree != nil {
				t.Fatalf("over-frontier parse = %#v, %t; want nil, false", tree, ok)
			}
		})
	}
	overBytes := "MODULE Oversized; (*" +
		strings.Repeat(" ", modulaMaximumConcreteParseBytes) +
		"*) END Oversized."
	if tree, ok := parseModulaSyntax(
		overBytes,
		modulaLexResult{concreteEligible: true},
	); ok || tree != nil {
		t.Fatalf("over-byte parse = %#v, %t; want nil, false", tree, ok)
	}
}

func TestModulaConcreteTreeRejectsInvalidRootAndCoordinates(t *testing.T) {
	t.Parallel()

	invalid := []*modulaSyntaxTree{
		nil,
		{
			root: 0,
			nodes: []modulaSyntaxNode{
				{kind: "module_declaration", startByte: 0, endByte: 4, parent: -1},
			},
		},
		{
			root: 0,
			nodes: []modulaSyntaxNode{
				{kind: "source_file", startByte: 1, endByte: 4, parent: -1},
			},
		},
		{
			root: 0,
			nodes: []modulaSyntaxNode{
				{kind: "source_file", startByte: 0, endByte: 4, parent: -1, children: []int{1}},
				{kind: "identifier", startByte: 3, endByte: 5, parent: 0},
			},
		},
	}
	for index, tree := range invalid {
		if validateModulaSyntaxTree(tree, 4) {
			t.Errorf("invalid tree %d was accepted: %#v", index, tree)
		}
	}
}

func modulaTreeTestParse(t *testing.T, source string) *modulaSyntaxTree {
	t.Helper()
	lexed := lexModula(source)
	if !lexed.concreteEligible {
		t.Fatal("small valid Modula-2 fixture is not concrete-eligible")
	}
	tree, ok := parseModulaSyntax(source, lexed)
	if !ok || !validateModulaSyntaxTree(tree, len(source)) {
		t.Fatal("valid Modula-2 fixture did not produce a validated concrete tree")
	}
	return tree
}

func modulaTreeTestParseRecovery(t *testing.T, source string) *modulaSyntaxTree {
	t.Helper()
	lexed := lexModula(source)
	lexed.concreteEligible = true
	tree, ok := parseModulaSyntax(source, lexed)
	if !ok || !validateModulaSyntaxTree(tree, len(source)) {
		t.Fatal("Modula-2 recovery fixture did not produce a validated concrete tree")
	}
	return tree
}
