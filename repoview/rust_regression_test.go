package repoview

import (
	"strconv"
	"strings"
	"testing"
)

func TestRustRegressionMacro20OpacityAndUnaryNotBlockItem(t *testing.T) {
	t.Parallel()

	const source = `macro declare($name:ident) {
    fn hidden_in_macro_body() {}
    use hidden::MacroImport;
}

fn outer() {
    let _ = !{
        fn visible_local_item() {}
        visible_local_item();
        true
    };
}
`
	lines := rustTestLines(source)
	backend := newRustLanguage()
	if got, want := strings.Join(
		rustDefinitionSymbols(backend.sourceDefinitions(lines)), ",",
	), "declare,outer,visible_local_item"; got != want {
		t.Fatalf("definitions = %q, want %q", got, want)
	}
	if start, end, ok := backend.importRange(lines); ok {
		t.Fatalf("macro-body import escaped opacity: %d-%d", start, end)
	}
}

func TestRustRegressionLegacyTryMacroOpacity(t *testing.T) {
	t.Parallel()

	const source = `fn try() -> Result<(), ()> { Ok(()) }

fn legacy() -> Result<(), ()> {
	    let _ = try!({
	        fn hidden_in_try_macro() {}
	        use hidden::LegacyImport;
	        Ok::<(), ()>(())
	    });
	    Ok(())
}

fn caller() {
    let _ = try();
}
`
	// Plain try and try! are valid legacy syntax in Rust 2015 even though the
	// bundled edition-agnostic grammar reports them through recovery nodes.
	backend := newRustLanguage()
	lines := rustTestLines(source)
	if got, want := strings.Join(
		rustDefinitionSymbols(backend.sourceDefinitions(lines)), ",",
	), "try,legacy,caller"; got != want {
		t.Fatalf("legacy try macro definitions = %q, want %q", got, want)
	}
	if start, end, ok := backend.importRange(lines); ok {
		t.Fatalf("legacy try macro import escaped opacity: %d-%d", start, end)
	}

	root := t.TempDir()
	writeFile(t, root, "fixture.rs", source)
	lineNo := rustLineContaining(t, lines, "let _ = try();")
	response, err := mustView(t, root).Inspect(
		"fixture.rs:"+strconv.Itoa(lineNo),
		Options{Include: IncludeScope, Return: ReturnScope},
	)
	if err != nil {
		t.Fatal(err)
	}
	if response.Symbol != "try" {
		t.Fatalf("legacy try call Inspect symbol = %q, want try", response.Symbol)
	}
}

func TestRustRegressionMacroInvocationInItemHeaderDoesNotOwnScope(t *testing.T) {
	t.Parallel()

	const source = `macro_rules! ty { () => { u8 } }
macro_rules! target { () => { Wrapper } }
struct Wrapper;
fn expanded_return_type() -> ty! {} {
    body();
}
impl target!() {
    fn method(&self) {}
}
`
	rustAssertConcreteSyntax(t, source)
	function := rustRegressionDefinitionOnLine(
		t,
		newRustLanguage().sourceDefinitions(rustTestLines(source)),
		"expanded_return_type",
		4,
	)
	if function.scopeStart != 4 || function.scopeEnd != 6 || !function.ownsScope {
		t.Fatalf("function with macro return type = %#v; want owning scope 4-6", function)
	}
	implementation := rustRegressionDefinitionOnLine(
		t,
		newRustLanguage().sourceDefinitions(rustTestLines(source)),
		"target",
		7,
	)
	if implementation.scopeStart != 7 || implementation.scopeEnd != 9 ||
		!implementation.ownsScope {
		t.Fatalf("impl with macro target = %#v; want owning scope 7-9", implementation)
	}
}

func TestRustRegressionMalformedDefinitionOwnershipMerge(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		first  string
		source string
	}{
		{
			name:  "broken function parameters",
			first: "broken",
			source: `fn broken(
fn after() {
    target();
}
`,
		},
		{
			name:  "type missing semicolon",
			first: "Alias",
			source: `type Alias = usize
fn after() {
    target();
}
`,
		},
		{
			name:  "const missing semicolon",
			first: "VALUE",
			source: `const VALUE: usize = 1
fn after() {
    target();
}
`,
		},
		{
			name:  "module missing semicolon",
			first: "missing",
			source: `mod missing
fn after() {
    target();
}
`,
		},
		{
			name:  "bodyless function missing semicolon",
			first: "declaration",
			source: `fn declaration()
fn after() {
    target();
}
`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			lines := rustTestLines(test.source)
			definitions := newRustLanguage().sourceDefinitions(lines)
			first := rustRegressionDefinitionOnLine(
				t,
				definitions,
				test.first,
				1,
			)
			if first.scopeStart != 1 || first.scopeEnd != 1 || first.ownsScope {
				t.Fatalf("recovered declaration = %#v; want non-owning line 1", first)
			}
			after := rustRegressionDefinitionOnLine(t, definitions, "after", 2)
			if after.scopeStart != 2 || after.scopeEnd != 4 || !after.ownsScope {
				t.Fatalf("following function = %#v; want owning scope 2-4", after)
			}

			resolver := any(newRustLanguage()).(navigationScopeResolver)
			if start, end := resolver.navigationScope(lines, 3); start != 2 || end != 4 {
				t.Fatalf("following function navigation = %d-%d, want 2-4", start, end)
			}
		})
	}
}

func TestRustRegressionParenthesizedImplAndDynPrincipalNames(t *testing.T) {
	t.Parallel()

	const source = `trait Service {}
struct Target;

impl (Target) {}
impl dyn Service + Send {}
`
	rustAssertConcreteSyntax(t, source)
	lines := rustTestLines(source)
	definitions := newRustLanguage().sourceDefinitions(lines)
	for _, test := range []struct {
		fragment string
		want     string
	}{
		{fragment: "impl (Target)", want: "Target"},
		{fragment: "impl dyn Service", want: "Service"},
	} {
		lineNo := rustLineContaining(t, lines, test.fragment)
		_ = rustRegressionDefinitionOnLine(t, definitions, test.want, lineNo)
	}
}

func TestRustRegressionInspectExpressionAndLifetimeCases(t *testing.T) {
	t.Parallel()

	const source = `fn caller<'life>(handlers: &[fn()]) {
    crate::logging::emit!("ready");
    (crate::factory::build)();
    handlers[index()]();
    crate::service::run();
    consume::<'life>(value);
    'retry: loop { tick(); break 'retry; }
}
`
	rustAssertConcreteSyntax(t, source)
	lines := rustTestLines(source)
	root := t.TempDir()
	writeFile(t, root, "fixture.rs", source)
	view := mustView(t, root)
	for _, test := range []struct {
		fragment string
		want     string
	}{
		{fragment: "logging::emit!", want: "emit"},
		{fragment: "factory::build", want: "build"},
		{fragment: "handlers[index", want: "handlers"},
		{fragment: "service::run", want: "run"},
		{fragment: "consume::<'life>", want: "consume"},
		{fragment: "'retry: loop", want: "tick"},
	} {
		lineNo := rustLineContaining(t, lines, test.fragment)
		response, err := view.Inspect(
			"fixture.rs:"+strconv.Itoa(lineNo),
			Options{Include: IncludeScope, Return: ReturnScope},
		)
		if err != nil {
			t.Fatal(err)
		}
		if response.Symbol != test.want {
			t.Errorf("Inspect(%q) symbol = %q, want %q", test.fragment, response.Symbol, test.want)
		}
	}
}

func TestRustRegressionLexicalInspectFallbackPriorities(t *testing.T) {
	t.Parallel()

	const source = `crate::logging::emit!(argument, other);
let _ = crate::factory::build(argument);
let _ = crate::service::VALUE;
let _ = source_value;
'retry: loop { break 'retry; }
r#match!(value);
`
	lines := rustTestLines(source)
	analysis := analyzeRustSource(strings.Join(lines, "\n"), len(lines))
	// Force the same bounded fallback used when the concrete parser times out or
	// rejects a malformed tree, without relying on machine timing in the test.
	analysis.tree = nil
	analysis.definitions = nil
	backend := newRustLanguage()
	backend.analysis = analysis

	for _, test := range []struct {
		line int
		want string
	}{
		{line: 1, want: "emit"},
		{line: 2, want: "build"},
		{line: 3, want: "VALUE"},
		{line: 4, want: "source_value"},
		{line: 5, want: ""},
		{line: 6, want: "r#match"},
	} {
		got, ok := backend.symbolOnLine(lines, test.line)
		if !ok || got != test.want {
			t.Errorf("lexical symbol on line %d = %q, %v; want %q, true", test.line, got, ok, test.want)
		}
	}

	for _, token := range []rustToken{
		{start: -1, end: 1},
		{start: 0, end: len(source) + 1},
		{start: 1, end: 1},
	} {
		if got := rustTokenSourceText(source, token); got != "" {
			t.Errorf("invalid token %#v mapped to %q", token, got)
		}
	}
}

func TestRustRegressionDeepCallInspectionIsBounded(t *testing.T) {
	t.Parallel()

	expression := "factory" + strings.Repeat("()", rustMaximumSyntaxUnwrapDepth+128)
	source := "fn caller() {\n    " + expression + ";\n}\n"
	rustAssertConcreteSyntax(t, source)
	tree, ok := parseRustSyntax(source)
	if !ok || tree == nil {
		t.Fatal("parseRustSyntax rejected deep-call fixture")
	}
	outermost, widest := -1, -1
	for index, node := range tree.nodes {
		if node.kind == "call_expression" && node.endByte-node.startByte > widest {
			outermost = index
			widest = node.endByte - node.startByte
		}
	}
	if outermost < 0 {
		t.Fatal("deep-call fixture has no call expression")
	}
	if identifier := rustCalledIdentifierNode(tree, outermost); identifier != -1 {
		t.Fatalf("outer deep call unwrapped past depth cap to node %d", identifier)
	}

	lines := rustTestLines(source)
	if symbol, found := newRustLanguage().symbolOnLine(lines, 2); !found || symbol != "factory" {
		t.Fatalf("bounded deep-call symbol = %q, %v; want factory, true", symbol, found)
	}
}

func TestRustRegressionBOMAndShebangMasking(t *testing.T) {
	t.Parallel()

	const bom = "\uFEFF"
	shebangSource := bom + "#!/usr/bin/env -S cargo +nightly -Zscript\nfn visible() {}\n"
	shebangLexed := lexRust(shebangSource)
	if len(shebangLexed.commentSpans) != 1 ||
		shebangLexed.commentSpans[0] != (rustByteSpan{
			start: len(bom),
			end:   strings.IndexByte(shebangSource, '\n'),
		}) {
		t.Fatalf("BOM shebang spans = %#v", shebangLexed.commentSpans)
	}
	masked := maskRustSource(shebangSource, shebangLexed.commentSpans)
	if len(masked) != len(shebangSource) || strings.Contains(masked, "cargo") ||
		!strings.Contains(masked, "fn visible() {}") {
		t.Fatalf("masked BOM shebang = %q", masked)
	}
	if got := rustDefinitionSymbols(
		newRustLanguage().sourceDefinitions(rustTestLines(shebangSource)),
	); len(got) != 1 || got[0] != "visible" {
		t.Fatalf("BOM shebang definitions = %#v, want visible", got)
	}

	normalSource := bom + "fn normal_code() {}\n"
	normalLexed := lexRust(normalSource)
	if len(normalLexed.commentSpans) != 0 {
		t.Fatalf("BOM before normal code produced comments: %#v", normalLexed.commentSpans)
	}
	if got := rustDefinitionSymbols(
		newRustLanguage().sourceDefinitions(rustTestLines(normalSource)),
	); len(got) != 1 || got[0] != "normal_code" {
		t.Fatalf("BOM normal-code definitions = %#v, want normal_code", got)
	}
}

func TestRustRegressionPatternWhitespaceAttachment(t *testing.T) {
	t.Parallel()

	const source = "/** definition docs */\u200e\n" +
		"#[inline]\u2028fn attached() {}\n" +
		"\n" +
		"/** import docs */\u0085\n" +
		"#[allow(unused_imports)]\u200fuse crate::Item;\n"
	// rustc accepts the complete set even though the bundled concrete grammar
	// reports some members through its recovery nodes.
	lines := rustTestLines(source)
	backend := newRustLanguage()
	definition := rustRegressionDefinitionOnLine(
		t,
		backend.sourceDefinitions(lines),
		"attached",
		2,
	)
	if definition.scopeStart != 1 || definition.scopeEnd != 2 {
		t.Fatalf("Pattern_White_Space definition attachment = %#v; want scope 1-2", definition)
	}
	if start, end, ok := backend.importRange(lines); !ok || start != 4 || end != 5 {
		t.Fatalf("Pattern_White_Space import attachment = %d-%d, %v; want 4-5, true", start, end, ok)
	}

	for _, r := range []rune{
		'\u0009', '\u000A', '\u000B', '\u000C', '\u000D', '\u0020', '\u0085',
		'\u200E', '\u200F', '\u2028', '\u2029',
	} {
		if !rustSpace(r) || !rustOnlyWhitespace(string(r)) {
			t.Errorf("Rust Pattern_White_Space U+%04X was rejected", r)
		}
	}
	for _, r := range []rune{'\u00A0', '\u1680', '\u200B', '\u3000'} {
		if rustSpace(r) || rustOnlyWhitespace(string(r)) {
			t.Errorf("non-Pattern_White_Space U+%04X was accepted", r)
		}
	}
}

func TestRustRegressionMalformedGroupedImportResynchronizes(t *testing.T) {
	t.Parallel()

	const source = `use crate::{
    First,
    Second
use recovered::Item;
fn after() {}
`
	lexed := lexRust(source)
	if len(lexed.imports) != 2 ||
		lexed.imports[0] != (rustLineSpan{start: 1, end: 3}) ||
		lexed.imports[1] != (rustLineSpan{start: 4, end: 4}) {
		t.Fatalf("lexical imports = %#v, want 1-3 and 4-4", lexed.imports)
	}
	lines := rustTestLines(source)
	backend := newRustLanguage()
	if start, end, ok := backend.importRange(lines); !ok || start != 1 || end != 4 {
		t.Fatalf("import range = %d-%d, %v; want 1-4, true", start, end, ok)
	}
	_ = rustRegressionDefinitionOnLine(t, backend.sourceDefinitions(lines), "after", 5)

	for _, test := range []struct {
		name   string
		source string
	}{
		{
			name: "missing semicolon",
			source: `use crate::Item
fn after() {}
`,
		},
		{
			name: "stray closer",
			source: `use crate::Item)
fn after() {}
`,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			caseLines := rustTestLines(test.source)
			caseBackend := newRustLanguage()
			if start, end, ok := caseBackend.importRange(caseLines); !ok || start != 1 || end != 1 {
				t.Fatalf("import range = %d-%d, %v; want 1-1, true", start, end, ok)
			}
			_ = rustRegressionDefinitionOnLine(
				t,
				caseBackend.sourceDefinitions(caseLines),
				"after",
				2,
			)
		})
	}
}

func TestRustRegressionInvalidEscapedCharactersDoNotConsumeNames(t *testing.T) {
	t.Parallel()

	const source = `fn borrow<'life>(value: &'life str) {
    let invalid_character = '\q';
    let invalid_byte_character = b'\q';
    let invalid_multicharacter = 'ab';
    consume::<'life>(value);
}
`
	lexed := lexRust(source)
	masked := maskRustSource(source, lexed.stringSpans)
	for _, retained := range []string{"'life", "'ab'", "consume", "value"} {
		if !strings.Contains(masked, retained) {
			t.Fatalf("literal masking consumed %q:\n%s", retained, masked)
		}
	}
	for _, removed := range []string{"'\\q'", "b'\\q'"} {
		if strings.Contains(masked, removed) {
			t.Fatalf("literal masking retained invalid escaped literal %q:\n%s", removed, masked)
		}
	}
	searchable := strings.Join(
		newRustLanguage().searchLines(rustTestLines(source), false, true),
		"\n",
	)
	if !strings.Contains(searchable, "'life") || !strings.Contains(searchable, "'ab'") ||
		strings.Contains(searchable, "'\\q'") || strings.Contains(searchable, "b'\\q'") {
		t.Fatalf("search masking disagrees with lexer:\n%s", searchable)
	}
}

func TestRustRegressionMalformedEnumRecoversLaterVariant(t *testing.T) {
	t.Parallel()

	const source = `enum Broken {
    First(u8
    Later,
}
`
	lines := rustTestLines(source)
	definitions := newRustLanguage().sourceDefinitions(lines)
	for _, test := range []struct {
		name string
		line int
	}{
		{name: "Broken", line: 1},
		{name: "First", line: 2},
		{name: "Later", line: 3},
	} {
		_ = rustRegressionDefinitionOnLine(t, definitions, test.name, test.line)
	}
}

func TestRustRegressionConcreteAttachmentAcrossOrdinaryComments(t *testing.T) {
	t.Parallel()

	const source = `/// Function documentation.
// ordinary function comment
#[cfg(any())]
/* ordinary function block */
fn documented() {}

/// Import documentation.
// ordinary import comment
#[cfg(any())]
/* ordinary import block */
use crate::Item;

enum Choice {
    /// Variant documentation.
    // ordinary variant comment
    #[cfg(any())]
    /* ordinary variant block */
    Selected,
}

mod boundary {
    //! Inner module documentation.
    // ordinary boundary comment
    fn not_attached_to_inner_doc() {}
}
`
	rustAssertConcreteSyntax(t, source)
	tree, ok := parseRustSyntax(source)
	if !ok || tree == nil {
		t.Fatal("parseRustSyntax rejected attachment fixture")
	}
	lexed := lexRust(source)
	excluded := make([]rustByteSpan, 0,
		len(lexed.commentSpans)+len(lexed.stringSpans)+len(lexed.syntaxOpaqueSpans))
	excluded = append(excluded, lexed.commentSpans...)
	excluded = append(excluded, lexed.stringSpans...)
	excluded = append(excluded, lexed.syntaxOpaqueSpans...)
	excluded = normalizeRustSpans(excluded)
	attachedStarts := rustSyntaxAttachedStarts(source, tree)
	definitions := rustTreeDefinitionsFromSyntax(
		source,
		tree,
		lexed,
		excluded,
		attachedStarts,
	)
	lines := rustTestLines(source)
	for _, test := range []struct {
		name      string
		item      string
		wantStart string
	}{
		{name: "documented", item: "fn documented", wantStart: "Function documentation"},
		{name: "Selected", item: "Selected,", wantStart: "Variant documentation"},
		{
			name:      "not_attached_to_inner_doc",
			item:      "fn not_attached_to_inner_doc",
			wantStart: "fn not_attached_to_inner_doc",
		},
	} {
		definition := rustRegressionDefinitionOnLine(
			t,
			definitions,
			test.name,
			rustLineContaining(t, lines, test.item),
		)
		wantStart := rustLineContaining(t, lines, test.wantStart)
		if definition.scopeStart != wantStart {
			t.Errorf("%s concrete scope start = %d, want %d", test.name, definition.scopeStart, wantStart)
		}
	}

	imports := rustTreeImportsFromSyntax(source, tree, excluded, attachedStarts)
	if len(imports) != 1 {
		t.Fatalf("concrete imports = %#v, want one", imports)
	}
	wantImportStart := rustLineContaining(t, lines, "Import documentation")
	wantImportEnd := rustLineContaining(t, lines, "use crate::Item")
	if imports[0] != (rustLineSpan{start: wantImportStart, end: wantImportEnd}) {
		t.Fatalf(
			"concrete import = %#v, want %d-%d",
			imports[0],
			wantImportStart,
			wantImportEnd,
		)
	}
}

func rustRegressionDefinitionOnLine(
	t *testing.T,
	definitions []sourceDefinition,
	symbol string,
	line int,
) sourceDefinition {
	t.Helper()
	for _, definition := range definitions {
		if definition.symbol == symbol && definition.line == line {
			return definition
		}
	}
	t.Fatalf("missing definition %q on line %d: %#v", symbol, line, definitions)
	return sourceDefinition{}
}
