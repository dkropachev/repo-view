package repoview

import (
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestCPPTreeStructuredBindingWidthBudgetRetainsBoundedPrefix(t *testing.T) {
	t.Parallel()

	// A structured-binding subtree contains its root, two brackets, one node
	// per name, and one comma between names: 2*n+2 visited nodes in total.
	if cppMaximumNameSearchOperations < 4 || cppMaximumNameSearchOperations%2 != 0 {
		t.Fatalf("name-search operation budget %d cannot exercise this boundary",
			cppMaximumNameSearchOperations)
	}
	atLimit := (cppMaximumNameSearchOperations - 2) / 2

	for _, test := range []struct {
		name       string
		bindings   int
		wantPrefix int
		wantVisits int
	}{
		{
			name:       "at limit",
			bindings:   atLimit,
			wantPrefix: atLimit,
			wantVisits: cppMaximumNameSearchOperations,
		},
		{
			name:       "over limit",
			bindings:   atLimit + 1,
			wantPrefix: atLimit,
			wantVisits: cppMaximumNameSearchOperations + 2,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			source, names := cppStructuredBindingFixture(test.bindings)
			tree, ok := parseCPPSyntax(source)
			if !ok {
				t.Fatal("parseCPPSyntax rejected structured-binding fixture")
			}
			binding := cppFirstDescendantOfKinds(
				tree, tree.root, "structured_binding_declarator",
			)
			if binding < 0 {
				t.Fatal("structured-binding declarator was not found")
			}
			if got := cppTreeSubtreeSize(tree, binding); got != test.wantVisits {
				t.Fatalf("structured-binding subtree visits = %d, want %d",
					got, test.wantVisits)
			}

			definitions := cppTreeDefinitions(source, 1, tree)
			if got, want := cppDefinitionSymbols(definitions), names[:test.wantPrefix]; !reflect.DeepEqual(got, want) {
				t.Fatalf("definitions = %#v, want bounded prefix %#v", got, want)
			}
		})
	}
}

func cppStructuredBindingFixture(count int) (string, []string) {
	var source strings.Builder
	source.WriteString("auto [")
	names := make([]string, 0, count)
	for index := range count {
		if index > 0 {
			source.WriteString(", ")
		}
		name := fmt.Sprintf("binding%d", index)
		names = append(names, name)
		source.WriteString(name)
	}
	source.WriteString("] = values;\n")
	return source.String(), names
}

func cppTreeSubtreeSize(tree *cppSyntaxTree, nodeIndex int) int {
	if !cValidSyntaxNodeIndex(tree, nodeIndex) {
		return 0
	}
	size := 0
	stack := []int{nodeIndex}
	for len(stack) > 0 {
		last := len(stack) - 1
		current := stack[last]
		stack = stack[:last]
		if !cValidSyntaxNodeIndex(tree, current) {
			return 0
		}
		size++
		stack = append(stack, tree.nodes[current].children...)
	}
	return size
}
