package repoview

import (
	"context"
	"testing"
)

func TestParsePythonSyntaxCopiesSafeTree(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		source string
	}{
		{name: "empty"},
		{
			name:   "unicode and crlf",
			source: "# caf\u00e9\r\n@d\u00e9corateur\r\nasync def \u043f\u0440\u0438\u0432\u0435\u0442(\u0438\u043c\u044f):\r\n    return f\"bonjour {\u0438\u043c\u044f}\"\r\n",
		},
		{
			name:   "nested constructs",
			source: "class Service:\n    def run(self):\n        if self.ready:\n            return client.session.request()\n",
		},
		{
			name:   "downward-only as-pattern alias",
			source: "try:\n    risky()\nexcept ValueError as error:\n    handle(error)\n",
		},
		{
			name: "compound clause siblings",
			source: "if first:\n    one()\nelif second:\n    two()\nelse:\n    three()\n" +
				"try:\n    risky()\nexcept Error:\n    recover()\nfinally:\n    finish()\n",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			syntaxTree, ok := parsePythonSyntax(test.source)
			if !ok {
				t.Fatal("parsePythonSyntax rejected valid Python")
			}
			if !validatePythonSyntaxTree(syntaxTree, len(test.source)) {
				t.Fatal("parsePythonSyntax returned an invalid copied tree")
			}
			if syntaxTree.nodes[syntaxTree.root].kind != "module" {
				t.Fatalf(
					"root kind = %q, want module",
					syntaxTree.nodes[syntaxTree.root].kind,
				)
			}
			for _, node := range syntaxTree.nodes {
				_ = test.source[node.startByte:node.endByte]
			}
		})
	}
}

func TestParsePythonSyntaxRealignsCompoundClauses(t *testing.T) {
	t.Parallel()

	source := "if first:\n    one()\nelif second:\n    two()\nelse:\n    three()\n" +
		"try:\n    risky()\nexcept Error:\n    recover()\nfinally:\n    finish()\n"
	syntaxTree, ok := parsePythonSyntax(source)
	if !ok {
		t.Fatal("parsePythonSyntax rejected compound clause siblings")
	}
	wantParents := map[string]string{
		"elif_clause":    "if_statement",
		"else_clause":    "if_statement",
		"except_clause":  "try_statement",
		"finally_clause": "try_statement",
	}
	found := make(map[string]bool, len(wantParents))
	for nodeIndex, node := range syntaxTree.nodes {
		wantParent, tracked := wantParents[node.kind]
		if !tracked {
			continue
		}
		if node.parent < 0 || node.parent >= nodeIndex {
			t.Fatalf("%s has invalid parent %d", node.kind, node.parent)
		}
		if got := syntaxTree.nodes[node.parent].kind; got != wantParent {
			t.Fatalf("%s parent = %s, want %s", node.kind, got, wantParent)
		}
		found[node.kind] = true
	}
	for kind := range wantParents {
		if !found[kind] {
			t.Fatalf("copied tree is missing %s", kind)
		}
	}
}

func TestParsePythonSyntaxCopiesAsPatternAlias(t *testing.T) {
	t.Parallel()

	source := "try:\n    risky()\nexcept ValueError as error:\n    handle(error)\n"
	syntaxTree, ok := parsePythonSyntax(source)
	if !ok {
		t.Fatal("parsePythonSyntax rejected an as-pattern alias")
	}
	foundPattern := false
	foundTarget := false
	for _, node := range syntaxTree.nodes {
		switch node.kind {
		case "as_pattern":
			foundPattern = true
		case "as_pattern_target":
			foundTarget = true
		default:
		}
	}
	if !foundPattern || !foundTarget {
		t.Fatalf(
			"copied kinds missing alias structure: as_pattern=%v, as_pattern_target=%v",
			foundPattern,
			foundTarget,
		)
	}
}

func TestParsePythonSyntaxRejectsBackwardLeadingExtras(t *testing.T) {
	t.Parallel()

	source := "match value:\n    # first\n    # second\n    case None:\n        pass\n"
	if _, ok := parsePythonSyntax(source); ok {
		t.Fatal("parsePythonSyntax accepted a child before its parser-reported parent")
	}
}

func TestPythonSyntaxRecordedAncestor(t *testing.T) {
	t.Parallel()

	syntaxTree := &pythonSyntaxTree{
		root: 0,
		nodes: []pythonSyntaxNode{
			{kind: "module", startByte: 0, endByte: 20, parent: -1, children: []int{1}},
			{kind: "as_pattern", startByte: 5, endByte: 15, parent: 0, children: []int{2}},
			{kind: "as_pattern_target", startByte: 10, endByte: 15, parent: 1, children: []int{3}},
			{kind: "identifier", startByte: 10, endByte: 15, parent: 2},
		},
	}

	t.Run("skips alias to exact ancestor", func(t *testing.T) {
		t.Parallel()

		operations := 0
		ancestor, ok := pythonSyntaxRecordedAncestor(
			context.Background(),
			syntaxTree,
			3,
			syntaxTree.nodes[1],
			&operations,
			10,
		)
		if !ok || ancestor != 1 {
			t.Fatalf("ancestor = %d, %v; want 1, true", ancestor, ok)
		}
		if operations != 2 {
			t.Fatalf("operations = %d, want 2", operations)
		}
	})

	t.Run("rejects current node", func(t *testing.T) {
		t.Parallel()

		operations := 0
		if ancestor, ok := pythonSyntaxRecordedAncestor(
			context.Background(),
			syntaxTree,
			3,
			syntaxTree.nodes[3],
			&operations,
			10,
		); ok || ancestor != -1 {
			t.Fatalf("ancestor = %d, %v; want -1, false", ancestor, ok)
		}
	})

	t.Run("rejects span-only match", func(t *testing.T) {
		t.Parallel()

		operations := 0
		got := syntaxTree.nodes[1]
		got.kind = "different_alias"
		if ancestor, ok := pythonSyntaxRecordedAncestor(
			context.Background(),
			syntaxTree,
			3,
			got,
			&operations,
			10,
		); ok || ancestor != -1 {
			t.Fatalf("ancestor = %d, %v; want -1, false", ancestor, ok)
		}
	})

	t.Run("charges skipped levels", func(t *testing.T) {
		t.Parallel()

		operations := 0
		if ancestor, ok := pythonSyntaxRecordedAncestor(
			context.Background(),
			syntaxTree,
			3,
			syntaxTree.nodes[1],
			&operations,
			1,
		); ok || ancestor != -1 {
			t.Fatalf("ancestor = %d, %v; want -1, false", ancestor, ok)
		}
		if operations != 1 {
			t.Fatalf("operations = %d, want 1", operations)
		}
	})
}

func TestAppendPythonSyntaxRealignedSibling(t *testing.T) {
	t.Parallel()

	newTree := func() *pythonSyntaxTree {
		return &pythonSyntaxTree{
			root: 0,
			nodes: []pythonSyntaxNode{
				{kind: "module", startByte: 0, endByte: 150, parent: -1, children: []int{1}},
				{kind: "if_statement", startByte: 0, endByte: 100, parent: 0, children: []int{2}},
				{kind: "block", startByte: 10, endByte: 50, parent: 1, children: []int{3}},
				{kind: "expression_statement", startByte: 20, endByte: 30, parent: 2},
			},
		}
	}

	t.Run("nearest ancestor sibling", func(t *testing.T) {
		t.Parallel()

		syntaxTree := newTree()
		operations := 0
		node := pythonSyntaxNode{kind: "else_clause", startByte: 60, endByte: 80}
		appended, ok := appendPythonSyntaxRealignedSibling(
			context.Background(),
			syntaxTree,
			node,
			2,
			10,
			&operations,
			10,
		)
		if !ok || appended != 4 {
			t.Fatalf("appended = %d, %v; want 4, true", appended, ok)
		}
		if syntaxTree.nodes[appended].parent != 1 {
			t.Fatalf("parent = %d, want 1", syntaxTree.nodes[appended].parent)
		}
		if operations != 1 {
			t.Fatalf("operations = %d, want 1", operations)
		}
	})

	t.Run("climbs to outer sibling", func(t *testing.T) {
		t.Parallel()

		syntaxTree := newTree()
		operations := 0
		node := pythonSyntaxNode{kind: "function_definition", startByte: 110, endByte: 140}
		appended, ok := appendPythonSyntaxRealignedSibling(
			context.Background(),
			syntaxTree,
			node,
			2,
			10,
			&operations,
			10,
		)
		if !ok || appended != 4 {
			t.Fatalf("appended = %d, %v; want 4, true", appended, ok)
		}
		if syntaxTree.nodes[appended].parent != 0 {
			t.Fatalf("parent = %d, want 0", syntaxTree.nodes[appended].parent)
		}
		if operations != 2 {
			t.Fatalf("operations = %d, want 2", operations)
		}
	})

	t.Run("rejects backward move", func(t *testing.T) {
		t.Parallel()

		syntaxTree := newTree()
		operations := 0
		node := pythonSyntaxNode{kind: "else_clause", startByte: 40, endByte: 45}
		if appended, ok := appendPythonSyntaxRealignedSibling(
			context.Background(),
			syntaxTree,
			node,
			2,
			10,
			&operations,
			10,
		); ok || appended != -1 {
			t.Fatalf("appended = %d, %v; want -1, false", appended, ok)
		}
	})

	t.Run("rejects non-last ancestor", func(t *testing.T) {
		t.Parallel()

		syntaxTree := newTree()
		syntaxTree.nodes = append(syntaxTree.nodes, pythonSyntaxNode{
			kind:      "else_clause",
			startByte: 60,
			endByte:   70,
			parent:    1,
		})
		syntaxTree.nodes[1].children = append(syntaxTree.nodes[1].children, 4)
		operations := 0
		node := pythonSyntaxNode{kind: "finally_clause", startByte: 80, endByte: 90}
		if appended, ok := appendPythonSyntaxRealignedSibling(
			context.Background(),
			syntaxTree,
			node,
			2,
			10,
			&operations,
			10,
		); ok || appended != -1 {
			t.Fatalf("appended = %d, %v; want -1, false", appended, ok)
		}
	})

	t.Run("honors operation cap", func(t *testing.T) {
		t.Parallel()

		syntaxTree := newTree()
		operations := 0
		node := pythonSyntaxNode{kind: "function_definition", startByte: 110, endByte: 140}
		if appended, ok := appendPythonSyntaxRealignedSibling(
			context.Background(),
			syntaxTree,
			node,
			2,
			10,
			&operations,
			1,
		); ok || appended != -1 {
			t.Fatalf("appended = %d, %v; want -1, false", appended, ok)
		}
		if operations != 1 {
			t.Fatalf("operations = %d, want 1", operations)
		}
	})
}

func TestParsePythonSyntaxMalformedInputNeverPanics(t *testing.T) {
	t.Parallel()

	sources := []string{
		"def broken(\n",
		"value = '''unterminated\r\nstill text",
		"if (\n    def recovered():\n        pass\n",
		"\x00\xff\xfe\ndef after():\n    pass\n",
		"f'{value!r:{width}.{precision}}'",
	}
	for _, source := range sources {
		syntaxTree, ok := parsePythonSyntax(source)
		if ok && !validatePythonSyntaxTree(syntaxTree, len(source)) {
			t.Fatal("parsePythonSyntax accepted an invalid copied tree")
		}
	}
}

func TestValidatePythonSyntaxTreeRejectsInvalidRelationships(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		tree *pythonSyntaxTree
	}{
		{
			name: "child outside parent",
			tree: &pythonSyntaxTree{
				root: 0,
				nodes: []pythonSyntaxNode{
					{kind: "module", startByte: 0, endByte: 2, parent: -1, children: []int{1}},
					{kind: "identifier", startByte: 1, endByte: 3, parent: 0},
				},
			},
		},
		{
			name: "overlapping siblings",
			tree: &pythonSyntaxTree{
				root: 0,
				nodes: []pythonSyntaxNode{
					{kind: "module", startByte: 0, endByte: 4, parent: -1, children: []int{1, 2}},
					{kind: "identifier", startByte: 0, endByte: 3, parent: 0},
					{kind: "identifier", startByte: 2, endByte: 4, parent: 0},
				},
			},
		},
		{
			name: "multiply referenced child",
			tree: &pythonSyntaxTree{
				root: 0,
				nodes: []pythonSyntaxNode{
					{kind: "module", startByte: 0, endByte: 1, parent: -1, children: []int{1, 1}},
					{kind: "identifier", startByte: 0, endByte: 1, parent: 0},
				},
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if validatePythonSyntaxTree(test.tree, 4) {
				t.Fatal("validatePythonSyntaxTree accepted an invalid tree")
			}
		})
	}
}

func FuzzParsePythonSyntaxNeverPanics(f *testing.F) {
	f.Add("")
	f.Add("def valid(value):\n    return f'{value!r}'\n")
	f.Add("value = '''unterminated\r\nstill text")
	f.Add("\x00\xff\xfe\ndef after():\n    pass\n")
	f.Add("match value:\n    # leading extra\n    case None:\n        pass\n")
	f.Add("if first:\n    one()\nelse:\n    two()\n")

	f.Fuzz(func(t *testing.T, source string) {
		syntaxTree, ok := parsePythonSyntax(source)
		if ok && !validatePythonSyntaxTree(syntaxTree, len(source)) {
			t.Fatal("parsePythonSyntax accepted an invalid copied tree")
		}
	})
}
