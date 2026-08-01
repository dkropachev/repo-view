package repoview

import (
	"context"
	"testing"

	treesitter "github.com/dcosson/treesitter-go"
	rustlanguage "github.com/dcosson/treesitter-go/languages/rust"
	treesitterparser "github.com/dcosson/treesitter-go/parser"
)

func TestParseTreeSitterSyntaxRejectsNilLanguage(t *testing.T) {
	t.Parallel()

	if syntaxTree, ok := parseTreeSitterSyntax("fn main() {}", nil); ok || syntaxTree != nil {
		t.Fatalf("parseTreeSitterSyntax returned %#v, %v for a nil language", syntaxTree, ok)
	}
}

func TestCopyTreeSitterSyntaxTreeHonorsCancelledContext(t *testing.T) {
	t.Parallel()

	const source = "fn main() {}\n"
	parser := treesitterparser.NewParser()
	parser.SetLanguage(rustlanguage.Language())
	tree := parser.ParseString(context.Background(), []byte(source))
	if tree == nil {
		t.Fatal("Rust parser returned a nil tree")
	}
	cursor := treesitter.NewTreeCursor(tree.RootNode())
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if syntaxTree, ok := copyTreeSitterSyntaxTree(ctx, &cursor, len(source)); ok || syntaxTree != nil {
		t.Fatalf("copyTreeSitterSyntaxTree returned %#v, %v after cancellation", syntaxTree, ok)
	}
}

func TestTreeSitterSyntaxLimitsRemainBounded(t *testing.T) {
	t.Parallel()

	if got := treeSitterSyntaxNodeLimit(-1); got != 0 {
		t.Fatalf("negative source node limit = %d, want 0", got)
	}
	if got := treeSitterSyntaxNodeLimit(0); got != treeSitterSyntaxMinimumNodes {
		t.Fatalf("empty source node limit = %d, want %d", got, treeSitterSyntaxMinimumNodes)
	}
	maxInt := int(^uint(0) >> 1)
	if got := treeSitterSyntaxNodeLimit(maxInt); got != maxInt {
		t.Fatalf("saturated node limit = %d, want %d", got, maxInt)
	}
	if got := treeSitterSyntaxOperationLimit(-1); got != 0 {
		t.Fatalf("negative operation limit = %d, want 0", got)
	}
	if got := treeSitterSyntaxOperationLimit(maxInt); got != maxInt {
		t.Fatalf("saturated operation limit = %d, want %d", got, maxInt)
	}

	operations := 0
	if !treeSitterSyntaxOperationAvailable(context.Background(), &operations, 1) {
		t.Fatal("first bounded operation was rejected")
	}
	if operations != 1 {
		t.Fatalf("operations = %d, want 1", operations)
	}
	if treeSitterSyntaxOperationAvailable(context.Background(), &operations, 1) {
		t.Fatal("operation past the limit was accepted")
	}
	if operations != 1 {
		t.Fatalf("rejected operation changed count to %d", operations)
	}
}

func TestValidateTreeSitterSyntaxTreeRejectsInvalidBounds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		tree         *treeSitterSyntaxTree
		sourceLength int
	}{
		{name: "nil tree", sourceLength: 1},
		{
			name: "negative source length",
			tree: &treeSitterSyntaxTree{
				root:  0,
				nodes: []treeSitterSyntaxNode{{kind: "source_file", parent: -1}},
			},
			sourceLength: -1,
		},
		{
			name: "child outside parent",
			tree: &treeSitterSyntaxTree{
				root: 0,
				nodes: []treeSitterSyntaxNode{
					{kind: "source_file", startByte: 0, endByte: 2, parent: -1, children: []int{1}},
					{kind: "identifier", startByte: 1, endByte: 3, parent: 0},
				},
			},
			sourceLength: 3,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if validateTreeSitterSyntaxTree(test.tree, test.sourceLength) {
				t.Fatal("validator accepted malformed bounds")
			}
		})
	}
}
