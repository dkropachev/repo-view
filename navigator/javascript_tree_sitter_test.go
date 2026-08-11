package navigator

import (
	"slices"
	"strings"
	"testing"
)

func TestParseJavaScriptSyntaxCopiesSafeTree(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		source    string
		wantKinds []string
	}{
		{name: "empty"},
		{
			name:   "unicode and crlf",
			source: "export function caf\u00e9(\u53c2\u6570) { return \u53c2\u6570; }\r\n",
			wantKinds: []string{
				"export_statement", "function_declaration",
			},
		},
		{
			name: "modern syntax",
			source: `#!/usr/bin/env node
import data from "package" with { type: "json" };
export default class Service {
  #field = 1;
  async *run(value = {}) {
    return value?.items?.map((item) => ({...item, ready: true})) ?? [];
  }
}
`,
			wantKinds: []string{
				"hash_bang_line", "import_statement", "class_declaration",
				"method_definition", "arrow_function",
			},
		},
		{
			name:   "jsx and nested templates",
			source: "const view = <Panel title=\"value\">text {render(`raw ${value}`)}</Panel>;\n",
			wantKinds: []string{
				"jsx_element", "jsx_text", "template_string", "template_substitution",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			tree, ok := parseJavaScriptSyntax(test.source)
			if !ok {
				t.Fatal("parseJavaScriptSyntax rejected JavaScript input")
			}
			if !validateJavaScriptSyntaxTree(tree, len(test.source)) {
				t.Fatal("parseJavaScriptSyntax returned an invalid copied tree")
			}
			if tree.nodes[tree.root].kind != "program" {
				t.Fatalf("root kind = %q, want program", tree.nodes[tree.root].kind)
			}
			found := make(map[string]bool)
			for _, node := range tree.nodes {
				_ = test.source[node.startByte:node.endByte]
				found[node.kind] = true
			}
			for _, kind := range test.wantKinds {
				if !found[kind] {
					t.Fatalf("copied tree is missing %q", kind)
				}
			}
		})
	}
}

func TestParseJavaScriptSyntaxMalformedInputNeverPanics(t *testing.T) {
	t.Parallel()

	for _, source := range []string{
		"function broken( {\nfunction after() {}\n",
		"const value = `unterminated ${expression\n",
		"class Broken { method(\n}\n",
		"\x00\xff\xfe\nfunction after() {}\n",
		"const value = /unterminated[regex/;\n",
	} {
		tree, ok := parseJavaScriptSyntax(source)
		if ok && !validateJavaScriptSyntaxTree(tree, len(source)) {
			t.Fatal("parseJavaScriptSyntax accepted an invalid copied tree")
		}
	}
}

func TestJavaScriptSyntaxErrorSpansKeepZeroWidthEOFErrors(t *testing.T) {
	t.Parallel()

	tree := &javascriptSyntaxTree{
		root: 0,
		nodes: []javascriptSyntaxNode{
			{kind: "program", startByte: 0, endByte: 4, parent: -1, children: []int{1}},
			{kind: "ERROR", startByte: 4, endByte: 4, parent: 0},
		},
	}
	if got := javascriptSyntaxErrorSpans(tree, 4); len(got) != 1 ||
		got[0] != (javascriptByteSpan{start: 3, end: 4}) {
		t.Fatalf("EOF error spans = %#v, want 3-4", got)
	}
}

func TestJavaScriptConcreteParserBudgetUsesLexicalFallbackForDenseSources(t *testing.T) {
	dense := `const loaded = require("dependency");` +
		strings.Repeat("a();", javascriptMaximumConcreteParseLexicalUnits/4+1) +
		`
exports.assigned = function () {};
const object = { handler: (() => {}) };
class Service { field = class {}; plain; method() {} }
const view = <Panel title="hidden">function Fake() {} require("fake") {target}</Panel>;
function after() {}`
	if javascriptConcreteSyntaxAllowed(dense) {
		t.Fatal("dense source exceeded lexical-unit budget but remained concrete-parser eligible")
	}
	analysis := analyzeJavaScriptSource(dense, 6)
	if analysis.tree != nil {
		t.Fatal("dense source unexpectedly retained a concrete syntax tree")
	}
	if got, want := javascriptDefinitionSymbols(analysis.definitions),
		[]string{"loaded", "assigned", "object", "handler", "Service", "field", "plain", "method", "view", "after"}; !slices.Equal(got, want) {
		t.Fatalf("fallback definitions = %#v, want %#v", got, want)
	}
	if want := []javascriptLineSpan{{start: 1, end: 1}}; !slices.Equal(analysis.imports, want) {
		t.Fatalf("fallback imports = %#v, want %#v", analysis.imports, want)
	}
	masked := maskJavaScriptSource(dense, analysis.stringSpans)
	if strings.Contains(masked, "Fake") || strings.Contains(masked, `require("fake")`) ||
		strings.Contains(masked, "hidden") || !strings.Contains(masked, "target") {
		t.Fatalf("fallback JSX search mask was incorrect")
	}

	sparse := "/*" + strings.Repeat("x", javascriptMaximumConcreteParseLexicalUnits+1024) +
		"*/\nconst sparse = 1;"
	if !javascriptConcreteSyntaxAllowed(sparse) {
		t.Fatal("large sparse source should remain concrete-parser eligible")
	}
	if tree, ok := parseJavaScriptSyntax(sparse); !ok || tree == nil {
		t.Fatal("large sparse source did not use concrete parser")
	}

	tooLarge := strings.Repeat(" ", javascriptMaximumConcreteParseBytes+1)
	if javascriptConcreteSyntaxAllowed(tooLarge) {
		t.Fatal("source beyond concrete-parser byte ceiling was accepted")
	}
}

func FuzzParseJavaScriptSyntaxNeverPanics(f *testing.F) {
	f.Add("")
	f.Add("function main() { console.log(\"hello\"); }\n")
	f.Add("const view = <Panel>{items.map((item) => `${item}`)}</Panel>;\n")
	f.Add("const value = `unterminated ${expression\n")
	f.Add("\x00\xff\xfe\nfunction after() {}\n")

	f.Fuzz(func(t *testing.T, source string) {
		tree, ok := parseJavaScriptSyntax(source)
		if ok && !validateJavaScriptSyntaxTree(tree, len(source)) {
			t.Fatal("parseJavaScriptSyntax accepted an invalid copied tree")
		}
	})
}
