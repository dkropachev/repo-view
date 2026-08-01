package repoview

import (
	"reflect"
	"strconv"
	"strings"
	"testing"
)

type rustDefinitionSummary struct {
	symbol string
	line   int
}

func TestRustDefinitionsCoverConcreteItems(t *testing.T) {
	t.Parallel()

	const source = `#![allow(dead_code)]

/// Service documentation.
#[derive(Clone)]
pub struct Service<T> {
    value: T,
}

pub union Packet {
    integer: u32,
    bytes: [u8; 4],
}

pub enum State {
    Ready,
    Failed {
        code: i32,
    },
    Tuple(u8),
}

pub trait Worker {
    type Output;
    const LIMIT: usize;
    fn work(&self) -> Self::Output;
}

impl<T> Worker for Service<T> {
    type Output = T;
    const LIMIT: usize = 1;
    fn work(&self) -> Self::Output {
        todo!()
    }
}

impl<T> Service<T> {
    pub fn new(value: T) -> Self {
        Self { value }
    }
}

extern "C" {
    fn foreign(input: i32) -> i32;
    static FOREIGN: i32;
}

pub type Alias<T> = Service<T>;
pub const DEFAULT: usize = 8;
pub static GLOBAL: usize = DEFAULT;

pub mod nested {
    pub fn child() {
        fn local() {}
        struct LocalType;
        static LOCAL_STATIC: usize = 1;
        let local_value = 1;
        let _ = local_value;
    }
}

macro_rules! generated {
    ($name:ident) => { fn $name() {} };
}
generated!(NotAnOutlineItem);

pub fn r#type() {}
`
	rustAssertConcreteSyntax(t, source)
	lines := rustTestLines(source)
	backend := newRustLanguage()
	definitions := backend.sourceDefinitions(lines)
	got := rustDefinitionSummaries(definitions)
	want := []rustDefinitionSummary{
		{symbol: "Service", line: rustLineContaining(t, lines, "pub struct Service")},
		{symbol: "Packet", line: rustLineContaining(t, lines, "pub union Packet")},
		{symbol: "State", line: rustLineContaining(t, lines, "pub enum State")},
		{symbol: "Ready", line: rustLineContaining(t, lines, "Ready,")},
		{symbol: "Failed", line: rustLineContaining(t, lines, "Failed {")},
		{symbol: "Tuple", line: rustLineContaining(t, lines, "Tuple(u8)")},
		{symbol: "Worker", line: rustLineContaining(t, lines, "pub trait Worker")},
		{symbol: "Output", line: rustLineContaining(t, lines, "type Output;")},
		{symbol: "LIMIT", line: rustLineContaining(t, lines, "const LIMIT: usize;")},
		{symbol: "work", line: rustLineContaining(t, lines, "fn work(&self) -> Self::Output;")},
		// Trait impls use the implemented trait as their pseudo-symbol.
		{symbol: "Worker", line: rustLineContaining(t, lines, "impl<T> Worker for Service")},
		{symbol: "Output", line: rustLineContaining(t, lines, "type Output = T")},
		{symbol: "LIMIT", line: rustLineContaining(t, lines, "const LIMIT: usize = 1")},
		{symbol: "work", line: rustLineContaining(t, lines, "fn work(&self) -> Self::Output {")},
		// Inherent impls use the target type as their pseudo-symbol.
		{symbol: "Service", line: rustLineContaining(t, lines, "impl<T> Service<T>")},
		{symbol: "new", line: rustLineContaining(t, lines, "pub fn new")},
		{symbol: "foreign", line: rustLineContaining(t, lines, "fn foreign")},
		{symbol: "FOREIGN", line: rustLineContaining(t, lines, "static FOREIGN")},
		{symbol: "Alias", line: rustLineContaining(t, lines, "pub type Alias")},
		{symbol: "DEFAULT", line: rustLineContaining(t, lines, "pub const DEFAULT")},
		{symbol: "GLOBAL", line: rustLineContaining(t, lines, "pub static GLOBAL")},
		{symbol: "nested", line: rustLineContaining(t, lines, "pub mod nested")},
		{symbol: "child", line: rustLineContaining(t, lines, "pub fn child")},
		{symbol: "local", line: rustLineContaining(t, lines, "fn local")},
		{symbol: "LocalType", line: rustLineContaining(t, lines, "struct LocalType")},
		{symbol: "LOCAL_STATIC", line: rustLineContaining(t, lines, "static LOCAL_STATIC")},
		{symbol: "generated", line: rustLineContaining(t, lines, "macro_rules! generated")},
		{symbol: "r#type", line: rustLineContaining(t, lines, "pub fn r#type")},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("definitions = %#v, want %#v", got, want)
	}

	root := t.TempDir()
	writeFile(t, root, "fixture.rs", source)
	outline, err := mustView(t, root).Outline("fixture.rs", Options{Return: ReturnLocations})
	if err != nil {
		t.Fatal(err)
	}
	outlineDefinitions := make([]rustDefinitionSummary, 0, len(outline.Results))
	for _, result := range outline.Results {
		outlineDefinitions = append(outlineDefinitions, rustDefinitionSummary{
			symbol: result.Symbol,
			line:   result.Line,
		})
		if result.Kind != "def" || result.Language != "rust" {
			t.Fatalf("malformed outline result: %#v", result)
		}
	}
	if !reflect.DeepEqual(outlineDefinitions, want) {
		t.Fatalf("outline = %#v, want %#v", outlineDefinitions, want)
	}
}

func TestRustDefinitionsExcludeNonItemsAndMacroTokenTrees(t *testing.T) {
	t.Parallel()

	const source = `struct Holder {
    field: usize,
}

fn outer(parameter: usize) {
    let local_binding = parameter;
    let closure_parameter = |argument: usize| argument;
    let _ = (local_binding, closure_parameter);
}

use crate::module::Item as ImportAlias;

macro_rules! declare {
    ($name:ident) => {
        fn MacroGenerated() {
            macro_body_target();
        }
        struct TokenTreeType;
    };
} // declare macro
declare!(ExpandedName);
`
	rustAssertConcreteSyntax(t, source)
	lines := rustTestLines(source)
	backend := newRustLanguage()
	definitions := backend.sourceDefinitions(lines)
	if got, want := rustDefinitionSummaries(definitions), []rustDefinitionSummary{
		{symbol: "Holder", line: 1},
		{symbol: "outer", line: 5},
		{symbol: "declare", line: 13},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("definitions = %#v, want %#v", got, want)
	}
	macroBodyLine := rustLineContaining(t, lines, "macro_body_target")
	wantStart := rustLineContaining(t, lines, "macro_rules! declare")
	wantEnd := rustLineContaining(t, lines, "declare macro")
	if start, end := backend.enclosingScope(lines, macroBodyLine); start != wantStart || end != wantEnd {
		t.Fatalf("macro token-tree scope = %d-%d, want %d-%d", start, end, wantStart, wantEnd)
	}
	resolver, ok := any(backend).(navigationScopeResolver)
	if !ok {
		t.Fatal("Rust backend does not provide named navigation scopes")
	}
	if start, end := resolver.navigationScope(lines, macroBodyLine); start != wantStart || end != wantEnd {
		t.Fatalf("macro token-tree navigation = %d-%d, want %d-%d", start, end, wantStart, wantEnd)
	}
}

func TestRustKeepsMultipleItemsOnOneLineInSourceOrder(t *testing.T) {
	t.Parallel()

	const source = `fn first() {} struct Second; const THIRD: usize = 3; fn r#type() {}`
	rustAssertConcreteSyntax(t, source)
	definitions := newRustLanguage().sourceDefinitions([]string{source})
	if got, want := rustDefinitionSymbols(definitions), []string{"first", "Second", "THIRD", "r#type"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("definitions = %#v, want %#v", got, want)
	}
	for index := 1; index < len(definitions); index++ {
		if definitions[index-1].column >= definitions[index].column {
			t.Fatalf("definitions are not in column order: %#v", definitions)
		}
	}
}

func TestRustImportsCoverConcreteFormsAndRecovery(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		source    string
		wantStart string
		wantEnd   string
		valid     bool
	}{
		{
			name: "extern crate, visibility, groups, and local use",
			source: `#![no_std]
extern crate alloc as heap;

#[cfg(feature = "std")]
pub(crate) use std::{
    collections::{BTreeMap, HashMap},
    sync::Arc,
};

use crate::module::{self, Item as Renamed};

fn load() {
    use super::Nested;
    let _ = Nested::new();
}

const TEXT: &str = "use fake::StringItem;";
// use fake::CommentItem;
macro_rules! fake_import {
    () => { use fake::MacroItem; };
}
`,
			wantStart: "extern crate alloc",
			wantEnd:   "use super::Nested",
			valid:     true,
		},
		{
			name: "incomplete grouped use",
			source: `use crate::{
    First,
    Second as Alias,
`,
			wantStart: "use crate::{",
			wantEnd:   "Second as Alias",
		},
		{
			name: "attached attribute belongs to import evidence",
			source: `#[cfg(any(unix, windows))]
pub use crate::{
    First,
    Second,
};
`,
			wantStart: "#[cfg",
			wantEnd:   "};",
			valid:     true,
		},
		{
			name: "resynchronizes after malformed item",
			source: `fn broken(
    value: usize,
use crate::{
    First,
    Second,
};
`,
			wantStart: "use crate::{",
			wantEnd:   "};",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if test.valid {
				rustAssertConcreteSyntax(t, test.source)
			}
			lines := rustTestLines(test.source)
			start, end, ok := newRustLanguage().importRange(lines)
			wantStart := rustLineContaining(t, lines, test.wantStart)
			wantEnd := rustLineContaining(t, lines, test.wantEnd)
			if !ok || start != wantStart || end != wantEnd {
				t.Fatalf(
					"imports = %d-%d, %v; want %d-%d, true",
					start,
					end,
					ok,
					wantStart,
					wantEnd,
				)
			}
		})
	}

	const fakeOnly = `const TEXT: &str = "extern crate fake; use fake::Item;";
// use comment::Only;
macro_rules! fake_import { () => { use macro_only::Item; }; }
`
	if start, end, ok := newRustLanguage().importRange(rustTestLines(fakeOnly)); ok {
		t.Fatalf("fake-only imports = %d-%d, true; want none", start, end)
	}
}

func TestRustScopesUseSmallestSemanticBlockAndNamedNavigationOwner(t *testing.T) {
	t.Parallel()

	const source = `/// Outer documentation.
#[inline]
pub fn outer(value: Option<i32>) {
    let closure = |input: i32| {
        if input > 0 {
            deep_target();
        } // inner if
        closure_target(input);
    };
    match value {
        Some(item) => {
            arm_target(item);
        } // match arm
        None => {}
    } // match
    let misleading = r###"} {"###;
    /* misleading braces: } { */
    after_target();
} // outer

impl Service {
    /// Run documentation.
    fn run(&self) {
        if ready() {
            method_target();
        } // method if
    } // run
} // impl
`
	rustAssertConcreteSyntax(t, source)
	lines := rustTestLines(source)
	backend := newRustLanguage()
	for _, test := range []struct {
		fragment string
		start    string
		end      string
	}{
		{fragment: "deep_target", start: "if input > 0", end: "inner if"},
		{fragment: "closure_target", start: "let closure", end: "};"},
		{fragment: "arm_target", start: "Some(item) =>", end: "match arm"},
		{fragment: "after_target", start: "Outer documentation", end: "} // outer"},
		{fragment: "method_target", start: "if ready()", end: "method if"},
	} {
		lineNo := rustLineContaining(t, lines, test.fragment)
		start, end := backend.enclosingScope(lines, lineNo)
		wantStart := rustLineContaining(t, lines, test.start)
		wantEnd := rustLineContaining(t, lines, test.end)
		if start != wantStart || end != wantEnd {
			t.Errorf(
				"%s enclosing scope = %d-%d, want %d-%d",
				test.fragment,
				start,
				end,
				wantStart,
				wantEnd,
			)
		}
	}

	resolver, ok := any(backend).(navigationScopeResolver)
	if !ok {
		t.Fatal("Rust backend does not provide named navigation scopes")
	}
	for _, test := range []struct {
		fragment string
		start    string
		end      string
	}{
		{fragment: "deep_target", start: "Outer documentation", end: "} // outer"},
		{fragment: "closure_target", start: "Outer documentation", end: "} // outer"},
		{fragment: "arm_target", start: "Outer documentation", end: "} // outer"},
		{fragment: "after_target", start: "Outer documentation", end: "} // outer"},
		{fragment: "method_target", start: "Run documentation", end: "} // run"},
	} {
		lineNo := rustLineContaining(t, lines, test.fragment)
		start, end := resolver.navigationScope(lines, lineNo)
		wantStart := rustLineContaining(t, lines, test.start)
		wantEnd := rustLineContaining(t, lines, test.end)
		if start != wantStart || end != wantEnd {
			t.Errorf(
				"%s navigation scope = %d-%d, want %d-%d",
				test.fragment,
				start,
				end,
				wantStart,
				wantEnd,
			)
		}
	}

	root := t.TempDir()
	writeFile(t, root, "fixture.rs", source)
	view := mustView(t, root)
	for _, test := range []struct {
		fragment  string
		wantScope string
		start     string
		end       string
	}{
		{fragment: "deep_target", wantScope: "outer", start: "Outer documentation", end: "} // outer"},
		{fragment: "method_target", wantScope: "run", start: "Run documentation", end: "} // run"},
	} {
		lineNo := rustLineContaining(t, lines, test.fragment)
		response, err := view.Inspect(
			"fixture.rs:"+strconv.Itoa(lineNo),
			Options{Include: IncludeScope, Return: ReturnScope},
		)
		if err != nil {
			t.Fatal(err)
		}
		if len(response.Results) != 1 {
			t.Fatalf("Inspect(%s) results = %#v", test.fragment, response.Results)
		}
		result := response.Results[0]
		wantStart := rustLineContaining(t, lines, test.start)
		wantEnd := rustLineContaining(t, lines, test.end)
		if result.Scope != test.wantScope || result.StartLine != wantStart || result.EndLine != wantEnd {
			t.Fatalf(
				"Inspect(%s) = %#v; want %q at %d-%d",
				test.fragment,
				result,
				test.wantScope,
				wantStart,
				wantEnd,
			)
		}
	}
}

func TestRustSearchAndCleaningUnderstandEveryLiteralAndNestedComment(t *testing.T) {
	t.Parallel()

	const source = `#!/usr/bin/env rust-script target
fn caller<'a>(value: &'a str) {
    let normal = "target // literal";
    let multiline = "first target
second target";
    let raw = r###"target /* raw */"###;
    let bytes = b"target // bytes";
    let raw_bytes = br##"target /* raw bytes */"##;
    let c_text = c"target // c string";
    let raw_c = cr##"target /* raw c */"##;
    let slash = '/';
    let byte_slash = b'/';
    /* outer target
       /* nested target */
       trailing target */
    // line target
    let _ = value; /* inline target */ target(); // trailing target
}
`
	rustAssertConcreteSyntax(t, source)
	lines := rustTestLines(source)
	backend := newRustLanguage()
	searchable := backend.searchLines(lines, true, true)
	if len(searchable) != len(lines) {
		t.Fatalf("search lines = %d, want %d", len(searchable), len(lines))
	}
	if got, want := rustLinesContainingSymbol(searchable, "target"), []int{
		rustLineContaining(t, lines, "target();"),
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("target lines = %#v, want %#v; masked = %#v", got, want, searchable)
	}
	if strings.Count(searchable[1], "'a") != 2 ||
		!strings.Contains(searchable[1], "value") {
		t.Fatalf("string masking consumed Rust lifetimes: %q", searchable[1])
	}

	cleaned := backend.cleanSource(source, true, false)
	for _, literal := range []string{
		"target // literal",
		"first target\nsecond target",
		"target /* raw */",
		"target // bytes",
		"target /* raw bytes */",
		"target // c string",
		"target /* raw c */",
	} {
		if !strings.Contains(cleaned, literal) {
			t.Fatalf("cleaned source lost literal %q:\n%s", literal, cleaned)
		}
	}
	for _, comment := range []string{
		"rust-script target",
		"outer target",
		"nested target",
		"trailing target */",
		"line target",
		"inline target",
	} {
		if strings.Contains(cleaned, comment) {
			t.Fatalf("cleaned source retained comment %q:\n%s", comment, cleaned)
		}
	}
	if !strings.Contains(cleaned, "target();") {
		t.Fatalf("cleaned source lost executable code:\n%s", cleaned)
	}

	cleaner, ok := any(backend).(linePreservingSourceCleaner)
	if !ok {
		t.Fatal("Rust backend does not provide line-preserving cleaning")
	}
	cleanedLines := cleaner.cleanSourceLines(lines, true, false)
	if len(cleanedLines) != len(lines) {
		t.Fatalf("cleaned lines = %d, want %d", len(cleanedLines), len(lines))
	}

	line := `let url = "https://example.test/a//b"; target(); // remove me`
	stripped := backend.stripComment(line)
	if !strings.Contains(stripped, `"https://example.test/a//b"`) ||
		!strings.Contains(stripped, "target();") || strings.Contains(stripped, "remove me") {
		t.Fatalf("stripComment(%q) = %q", line, stripped)
	}
}

func TestRustStringMaskingDistinguishesLifetimesLabelsAndCharacters(t *testing.T) {
	t.Parallel()

	const source = `fn borrow<'a, 'b>(first: &'a str, second: &'b str) -> (&'a str, &'b str) {
    'search: loop {
        let character = 'x';
        let byte = b'y';
        break 'search (first, second);
    }
}
`
	rustAssertConcreteSyntax(t, source)
	lexed := lexRust(source)
	masked := maskRustSource(source, lexed.stringSpans)
	for _, retained := range []string{"'a", "'b", "'search", "first", "second"} {
		if !strings.Contains(masked, retained) {
			t.Fatalf("literal masking consumed %q:\n%s", retained, masked)
		}
	}
	for _, removed := range []string{"'x'", "b'y'"} {
		if strings.Contains(masked, removed) {
			t.Fatalf("literal masking retained character literal %q:\n%s", removed, masked)
		}
	}

	searchable := newRustLanguage().searchLines(rustTestLines(source), true, true)
	joined := strings.Join(searchable, "\n")
	for _, retained := range []string{"'a", "'b", "'search", "first", "second"} {
		if !strings.Contains(joined, retained) {
			t.Fatalf("backend search masking consumed %q:\n%s", retained, joined)
		}
	}
}

func TestRustInspectSelectsConcreteExpressionSymbols(t *testing.T) {
	t.Parallel()

	const source = `fn r#type() {}

fn caller<T>(argument: T) {
    let first = client.transport.request::<T>(argument);
    let second = Factory::<T>::build();
    let third = mapping.get("key");
    let fourth = "Wrong()"; right();
    tracing::info!("ready");
    let fifth = r#type();
    let field = object.final_field;
    let chain = (
        client
            .transport
            .request()
    );
    tracing::debug!(
        target: module::TARGET,
        "{}",
        argument,
    );
    tracing::warn!(target: module::TARGET, "{}", argument);
    let constant = module::CONSTANT;
    let _ = (first, second, third, fourth, fifth, field, chain, constant);
}
`
	rustAssertConcreteSyntax(t, source)
	lines := rustTestLines(source)
	root := t.TempDir()
	writeFile(t, root, "fixture.rs", source)
	view := mustView(t, root)
	tests := []struct {
		fragment string
		want     string
	}{
		{fragment: "fn r#type", want: "r#type"},
		{fragment: "fn caller", want: "caller"},
		{fragment: "client.transport.request", want: "request"},
		{fragment: "Factory::<T>::build", want: "build"},
		{fragment: "mapping.get", want: "get"},
		{fragment: `"Wrong()"; right`, want: "right"},
		{fragment: "tracing::info!", want: "info"},
		{fragment: "r#type();", want: "r#type"},
		{fragment: "object.final_field", want: "final_field"},
		{fragment: "        client", want: "client"},
		{fragment: "            .transport", want: "transport"},
		{fragment: "            .request()", want: "request"},
		{fragment: "tracing::debug!", want: "debug"},
		{fragment: "tracing::warn!", want: "warn"},
		{fragment: "module::CONSTANT;", want: "CONSTANT"},
	}
	for _, test := range tests {
		lineNo := rustLineContaining(t, lines, test.fragment)
		response, err := view.Inspect(
			"fixture.rs:"+strconv.Itoa(lineNo),
			Options{Include: IncludeScope, Return: ReturnScope},
		)
		if err != nil {
			t.Fatal(err)
		}
		if response.Symbol != test.want {
			t.Fatalf("Inspect(%q) symbol = %q, want %q", test.fragment, response.Symbol, test.want)
		}
		if lineNo != 1 && len(response.Results) == 1 && response.Results[0].Scope != "caller" {
			t.Fatalf("Inspect(%q) scope = %#v, want caller", test.fragment, response.Results[0])
		}
	}
}

func TestRustPreparedBackendRejectsStaleAnalysis(t *testing.T) {
	t.Parallel()

	preparer, ok := any(newRustLanguage()).(sourceBackendPreparer)
	if !ok {
		t.Fatal("Rust backend does not support prepared source analysis")
	}
	first := []string{"fn first() {}"}
	prepared := preparer.prepareSource(first)
	if definitions := prepared.sourceDefinitions(first); len(definitions) != 1 ||
		definitions[0].symbol != "first" {
		t.Fatalf("prepared definitions = %#v, want first", definitions)
	}

	second := []string{"fn second() {}"}
	if definitions := prepared.sourceDefinitions(second); len(definitions) != 1 ||
		definitions[0].symbol != "second" {
		t.Fatalf("stale prepared definitions = %#v, want second", definitions)
	}

	first[0] = "fn mutated() {}"
	if definitions := prepared.sourceDefinitions(first); len(definitions) != 1 ||
		definitions[0].symbol != "mutated" {
		t.Fatalf("mutated-source definitions = %#v, want mutated", definitions)
	}

	empty := preparer.prepareSource(nil)
	if definitions := empty.sourceDefinitions(nil); len(definitions) != 0 {
		t.Fatalf("empty prepared definitions = %#v, want none", definitions)
	}
}

func TestRustUsesUnicode17XIDAndPreservesRawIdentifierSpelling(t *testing.T) {
	t.Parallel()

	for name, ranges := range map[string][]rustXIDRange{
		"start":    rustXIDStartRanges[:],
		"continue": rustXIDContinueRanges[:],
	} {
		for index, span := range ranges {
			if span.first > span.last {
				t.Fatalf("%s range %d is reversed: %#v", name, index, span)
			}
			if index > 0 && ranges[index-1].last >= span.first {
				t.Fatalf("%s ranges %d and %d overlap", name, index-1, index)
			}
		}
	}
	for _, span := range rustXIDStartRanges {
		if !rustIdentifierContinue(span.first) || !rustIdentifierContinue(span.last) {
			t.Fatalf("XID_Start range is not contained in XID_Continue: %#v", span)
		}
	}

	const unicode17 = "\U00011DB0"
	if !rustIdentifierStart([]rune(unicode17)[0]) {
		t.Fatalf("Unicode 17 identifier start %U was rejected", []rune(unicode17)[0])
	}
	if rustIdentifierStart('\u037a') {
		t.Fatal("non-XID U+037A was accepted as an identifier start")
	}
	if !rustIdentifierContinue('\u00b7') {
		t.Fatal("XID_Continue U+00B7 was rejected")
	}

	source := "fn r#type() {}\n" +
		"fn " + unicode17 + "suffix() {}\n" +
		"fn a\u00b7b() {}\n" +
		"fn caller() { r#type(); " + unicode17 + "suffix(); a\u00b7b(); }\n" +
		"fn type() {}\n" +
		"fn \u037a() {}\n" +
		"fn _() {}\n" +
		"fn r#_() {}\n" +
		"fn r#self() {}\n" +
		"fn r#Self() {}\n" +
		"fn r#super() {}\n" +
		"fn r#crate() {}\n"
	lines := rustTestLines(source)
	definitions := newRustLanguage().sourceDefinitions(lines)
	if got, want := rustDefinitionSummaries(definitions), []rustDefinitionSummary{
		{symbol: "r#type", line: 1},
		{symbol: unicode17 + "suffix", line: 2},
		{symbol: "a\u00b7b", line: 3},
		{symbol: "caller", line: 4},
	}; !reflect.DeepEqual(got, want) {
		t.Fatalf("definitions = %#v, want %#v", got, want)
	}

	root := t.TempDir()
	writeFile(t, root, "fixture.rs", source)
	view := mustView(t, root)
	for _, test := range []struct {
		symbol string
		lines  []int
	}{
		{symbol: "r#type", lines: []int{1, 4}},
		{symbol: unicode17 + "suffix", lines: []int{2, 4}},
		{symbol: "a\u00b7b", lines: []int{3, 4}},
	} {
		response, err := view.Find(test.symbol, Options{
			Include:    IncludeBoth,
			Return:     ReturnLocations,
			NoComments: true,
			NoStrings:  true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if got := resultLines(response.Results); !reflect.DeepEqual(got, test.lines) {
			t.Fatalf("Find(%q) lines = %#v, want %#v", test.symbol, got, test.lines)
		}
		if len(response.Results) != 2 || response.Results[0].Kind != "def" ||
			response.Results[1].Kind != "ref" {
			t.Fatalf("Find(%q) results = %#v", test.symbol, response.Results)
		}
	}

	partial, err := view.Find("suffix", Options{
		Include: IncludeBoth,
		Return:  ReturnLocations,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(partial.Results) != 0 {
		t.Fatalf("partial Unicode 17 identifier matched: %#v", partial.Results)
	}
}

func TestRustCRLFCoordinatesRemainPhysical(t *testing.T) {
	t.Parallel()

	source := strings.Join([]string{
		"use std::sync::Arc;",
		"/// docs",
		"pub fn crlf<T>(",
		"    value: T,",
		") {",
		"    if ready() {",
		"        target();",
		"    }",
		"}",
	}, "\r\n")
	rustAssertConcreteSyntax(t, source)
	lines := strings.Split(source, "\n")
	backend := newRustLanguage()
	definitions := backend.sourceDefinitions(lines)
	if len(definitions) != 1 || definitions[0].symbol != "crlf" ||
		definitions[0].line != 3 || definitions[0].scopeStart != 2 ||
		definitions[0].scopeEnd != 9 {
		t.Fatalf("CRLF definitions = %#v", definitions)
	}
	if start, end, ok := backend.importRange(lines); !ok || start != 1 || end != 1 {
		t.Fatalf("CRLF imports = %d-%d, %v; want 1-1, true", start, end, ok)
	}
	if start, end := backend.enclosingScope(lines, 7); start != 6 || end != 8 {
		t.Fatalf("CRLF scope = %d-%d, want 6-8", start, end)
	}
}

func TestRustMalformedAndInvalidSourcesRecoverWithoutPanics(t *testing.T) {
	t.Parallel()

	const incomplete = `fn good() {}
fn broken(
    value: i32,
fn later() {}
let payload = r###"unterminated
fn hidden() {}
`
	backend := newRustLanguage()
	definitions := backend.sourceDefinitions(rustTestLines(incomplete))
	if got, want := rustDefinitionSymbols(definitions), []string{"good", "broken", "later"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("incomplete definitions = %#v, want %#v", got, want)
	}
	const unterminatedComment = `fn visible() {}
/* unterminated comment
fn hidden() {}
`
	if got, want := rustDefinitionSymbols(
		backend.sourceDefinitions(rustTestLines(unterminatedComment)),
	), []string{"visible"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("unterminated-comment definitions = %#v, want %#v", got, want)
	}

	invalidUTF8 := "fn before() {}\nlet payload = \"" + string([]byte{0xff, 0xfe}) +
		"\";\nfn after() {}\n// " + string([]byte{0xc0}) + "\n"
	invalidLines := rustTestLines(invalidUTF8)
	invalidDefinitions := backend.sourceDefinitions(invalidLines)
	if got, want := rustDefinitionSymbols(invalidDefinitions), []string{"before", "after"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("invalid-UTF-8 definitions = %#v, want %#v", got, want)
	}

	corpus := []string{
		"",
		"fn open<T(\n",
		"use crate::{\n    Item,\n",
		"/* unterminated\nfn hidden() {}\n",
		"let value = r###\"unterminated\nfn hidden() {}\n",
		"macro_rules! broken { ($name:ident => { fn hidden() {} }\n",
		invalidUTF8,
	}
	for index, source := range corpus {
		t.Run("case_"+strconv.Itoa(index), func(t *testing.T) {
			t.Parallel()
			lines := strings.Split(source, "\n")
			prepared := prepareLanguageBackend(backend, lines)
			_ = prepared.sourceDefinitions(lines)
			_, _, _ = prepared.importRange(lines)
			searchable := prepared.searchLines(lines, true, true)
			if len(searchable) != len(lines) {
				t.Fatalf("search lines = %d, want %d", len(searchable), len(lines))
			}
			_ = prepared.ignoredSearchLines(lines, true, false)
			_ = prepared.cleanSource(source, true, false)
			_, _ = prepared.enclosingScope(lines, 1)
			_, _ = prepared.enclosingScope(lines, len(lines))
			for _, line := range lines {
				_, _ = prepared.definitionSymbol(line)
				_ = prepared.stripComment(line)
			}
		})
	}

	root := t.TempDir()
	writeFile(t, root, "fixture.rs", invalidUTF8)
	outline, err := mustView(t, root).Outline("fixture.rs", Options{Return: ReturnLocations})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := rustResultSymbols(outline.Results), []string{"before", "after"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("invalid-UTF-8 outline = %#v, want %#v", got, want)
	}
}

func rustTestLines(source string) []string {
	return strings.Split(strings.TrimSuffix(source, "\n"), "\n")
}

func rustDefinitionSummaries(definitions []sourceDefinition) []rustDefinitionSummary {
	result := make([]rustDefinitionSummary, 0, len(definitions))
	for _, definition := range definitions {
		result = append(result, rustDefinitionSummary{
			symbol: definition.symbol,
			line:   definition.line,
		})
	}
	return result
}

func rustDefinitionSymbols(definitions []sourceDefinition) []string {
	result := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		result = append(result, definition.symbol)
	}
	return result
}

func rustResultSymbols(results []Result) []string {
	result := make([]string, 0, len(results))
	for _, item := range results {
		result = append(result, item.Symbol)
	}
	return result
}

func rustLineContaining(t *testing.T, lines []string, fragment string) int {
	t.Helper()
	for index, line := range lines {
		if strings.Contains(line, fragment) {
			return index + 1
		}
	}
	t.Fatalf("fixture does not contain %q", fragment)
	return 0
}

func rustLinesContainingSymbol(lines []string, symbol string) []int {
	result := make([]int, 0)
	for index, line := range lines {
		if countSymbolOccurrences(line, symbol) > 0 {
			result = append(result, index+1)
		}
	}
	return result
}

func rustAssertConcreteSyntax(t *testing.T, source string) {
	t.Helper()
	tree, ok := parseRustSyntax(source)
	if !ok || tree == nil {
		t.Fatal("parseRustSyntax rejected valid Rust source")
	}
	for _, node := range tree.nodes {
		if node.kind == "ERROR" {
			start, end := node.startByte, node.endByte
			if start < 0 || start > len(source) {
				start = 0
			}
			if end < start || end > len(source) {
				end = start
			}
			t.Fatalf("valid Rust source contains ERROR node at %d:%d: %q", start, end, source[start:end])
		}
	}
}

func FuzzRustLanguageNeverPanics(f *testing.F) {
	for _, source := range []string{
		"",
		"fn valid<T>(value: T) -> T { value }\n",
		"fn broken(\nuse crate::{\n    Item,\n",
		"let value = r###\"unterminated\nfn hidden() {}\n",
		"/* outer /* nested */ unterminated\nfn hidden() {}\n",
		"fn r#type() {}\r\nfn \U00011DB0() {}\r\n",
		"fn nested() { factory" + strings.Repeat("()", rustMaximumSyntaxUnwrapDepth+2) + "; }\n",
		string([]byte{'f', 'n', ' ', 0xff, '(', ')', ' ', '{', '}', '\n'}),
	} {
		f.Add(source)
	}

	f.Fuzz(func(t *testing.T, source string) {
		if len(source) > 256*1024 {
			t.Skip()
		}
		lines := strings.Split(source, "\n")
		backend := prepareLanguageBackend(newRustLanguage(), lines)
		definitions := backend.sourceDefinitions(lines)
		for _, definition := range definitions {
			if definition.symbol == "" || definition.line < 1 || definition.line > len(lines) ||
				definition.column < 1 || definition.scopeStart < 1 ||
				definition.scopeStart > definition.line || definition.scopeEnd < definition.line ||
				definition.scopeEnd > len(lines) {
				t.Fatalf("invalid definition: %#v", definition)
			}
		}
		if start, end, ok := backend.importRange(lines); ok &&
			(start < 1 || end < start || end > len(lines)) {
			t.Fatalf("invalid import range: %d-%d", start, end)
		}
		for _, options := range [][2]bool{{false, false}, {true, false}, {false, true}, {true, true}} {
			searchable := backend.searchLines(lines, options[0], options[1])
			if len(searchable) != len(lines) {
				t.Fatalf("search lines = %d, want %d", len(searchable), len(lines))
			}
		}
		if cleaner, ok := backend.(linePreservingSourceCleaner); ok {
			cleaned := cleaner.cleanSourceLines(lines, true, false)
			if len(cleaned) != len(lines) {
				t.Fatalf("clean lines = %d, want %d", len(cleaned), len(lines))
			}
		}
		for _, lineNo := range []int{1, len(lines)} {
			start, end := backend.enclosingScope(lines, lineNo)
			if start < 1 || end < start || end > len(lines) {
				t.Fatalf("invalid scope for line %d: %d-%d", lineNo, start, end)
			}
			_ = bestSymbolOnLine(lines, lineNo, backend)
		}
		lexed := lexRust(source)
		spans := append([]rustByteSpan(nil), lexed.commentSpans...)
		spans = append(spans, lexed.stringSpans...)
		masked := maskRustSource(source, spans)
		if len(masked) != len(source) {
			t.Fatalf("mask length = %d, want %d", len(masked), len(source))
		}
	})
}
