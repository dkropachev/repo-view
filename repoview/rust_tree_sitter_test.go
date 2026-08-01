package repoview

import "testing"

func TestParseRustSyntaxCopiesSafeTree(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		source    string
		wantKinds []string
	}{
		{name: "empty"},
		{
			name:   "unicode and crlf",
			source: "fn caf\u00e9(\u53c2\u6570: &str) -> &str { \u53c2\u6570 }\r\n",
			wantKinds: []string{
				"function_item",
			},
		},
		{
			name: "modern declarations",
			source: `#![allow(dead_code)]
use std::{collections::HashMap, sync::Arc};

#[derive(Debug)]
pub struct Service<'a, T: ?Sized> {
    label: &'a str,
    inner: Arc<T>,
}

impl<'a, T: ?Sized> Service<'a, T> {
    pub async fn run<const N: usize>(&self, input: [u8; N]) -> Result<(), ()> {
        let text = r###"raw text"###;
        println!("{text}: {}", input.len());
        let _: HashMap<&str, usize> = HashMap::new();
        Ok(())
    }
}
`,
			wantKinds: []string{
				"struct_item",
				"impl_item",
				"function_item",
			},
		},
		{
			name: "macro token trees",
			source: `macro_rules! make {
    ($name:ident) => { fn $name() {} };
}
make!(generated);
`,
			wantKinds: []string{
				"macro_definition",
				"macro_invocation",
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			syntaxTree, ok := parseRustSyntax(test.source)
			if !ok {
				t.Fatal("parseRustSyntax rejected Rust input")
			}
			if !validateRustSyntaxTree(syntaxTree, len(test.source)) {
				t.Fatal("parseRustSyntax returned an invalid copied tree")
			}
			if syntaxTree.nodes[syntaxTree.root].kind != "source_file" {
				t.Fatalf(
					"root kind = %q, want source_file",
					syntaxTree.nodes[syntaxTree.root].kind,
				)
			}

			foundKinds := make(map[string]bool)
			for _, node := range syntaxTree.nodes {
				_ = test.source[node.startByte:node.endByte]
				foundKinds[node.kind] = true
			}
			for _, kind := range test.wantKinds {
				if !foundKinds[kind] {
					t.Fatalf("copied tree is missing %q", kind)
				}
			}
		})
	}
}

func TestParseRustSyntaxMalformedInputNeverPanics(t *testing.T) {
	t.Parallel()

	sources := []string{
		"fn broken<T(\n",
		"let value = r###\"unterminated\n",
		"macro_rules! broken { ($value:expr => { $value }\n",
		"\x00\xff\xfe\nfn after() {}\n",
		"impl<T> Service<T> where T: Send + {\n",
	}
	for _, source := range sources {
		syntaxTree, ok := parseRustSyntax(source)
		if ok && !validateRustSyntaxTree(syntaxTree, len(source)) {
			t.Fatal("parseRustSyntax accepted an invalid copied tree")
		}
	}
}

func FuzzParseRustSyntaxNeverPanics(f *testing.F) {
	f.Add("")
	f.Add("fn main() { println!(\"hello\"); }\n")
	f.Add("pub async fn run<'a, T: Send>(value: &'a T) -> impl Future + 'a { todo!() }\n")
	f.Add("macro_rules! make { ($name:ident) => { fn $name() {} }; }\n")
	f.Add("let value = r###\"unterminated\n")
	f.Add("\x00\xff\xfe\nfn after() {}\n")

	f.Fuzz(func(t *testing.T, source string) {
		syntaxTree, ok := parseRustSyntax(source)
		if ok && !validateRustSyntaxTree(syntaxTree, len(source)) {
			t.Fatal("parseRustSyntax accepted an invalid copied tree")
		}
	})
}
