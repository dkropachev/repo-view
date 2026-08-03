package repoview

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestJavaScriptDefinitionsCoverConcreteDeclarationsAndBindings(t *testing.T) {
	t.Parallel()

	const source = `#!/usr/bin/env node
import def, {x as y} from "pkg";
export * as ns from "other";
export async function* top(a = {}) { return a; }
export default class Named extends Base {
  #field = () => 1;
  static value = function inner() {};
  get item() { return this.#field; }
  set item(value) {}
  async *method() { await value; }
  [computed]() {}
}
const arrow = async (x) => x;
let expr = function local() {};
var Klass = class Inner {};
const object = { shorthand, method() {}, async *other() {}, prop: () => 1, wrapped: ((arg) => arg) };
exports.assigned = function () {};
module.exports = class {};
const loaded = require("dep");
async function caller() { const dynamic = await import("lazy"); }
`
	definitions := newJavaScriptLanguage("javascript").sourceDefinitions(
		javascriptTestLines(source),
	)
	got := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		got = append(got, definition.symbol)
	}
	want := []string{
		"top", "Named", "#field", "value", "inner", "item", "item", "method",
		"arrow", "expr", "local", "Klass", "Inner", "object", "method", "other",
		"prop", "wrapped", "assigned", "loaded", "caller", "dynamic",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("definitions = %#v, want %#v", got, want)
	}

	for _, symbol := range []string{
		"top", "Named", "#field", "value", "inner", "item", "method", "arrow",
		"expr", "local", "Klass", "Inner", "object", "other", "prop", "wrapped", "assigned",
		"caller",
	} {
		definition := javascriptDefinitionNamed(t, definitions, symbol)
		if !definition.ownsScope {
			t.Fatalf("definition %q = %#v, want owning scope", symbol, definition)
		}
	}
	for _, symbol := range []string{"loaded", "dynamic"} {
		definition := javascriptDefinitionNamed(t, definitions, symbol)
		if definition.ownsScope || definition.scopeStart != definition.line ||
			definition.scopeEnd != definition.line {
			t.Fatalf("definition %q = %#v, want non-owning physical line", symbol, definition)
		}
	}
	class := javascriptDefinitionNamed(t, definitions, "Named")
	if class.scopeStart != 5 || class.scopeEnd != 12 {
		t.Fatalf("class scope = %#v, want 5-12", class)
	}
}

func TestJavaScriptDefinitionsExcludeOpaqueAndNonBindingSyntax(t *testing.T) {
	t.Parallel()

	const source = `// function LineComment() {}
/* class BlockComment {} */
const text = "function InString() {}";
const pattern = /class InRegex \{\}/;
const template = ` + "`function InTemplate() {} ${realCall()}`" + `;
const view = <section title="class InAttribute {}">function InText() {} {jsxCall()}</section>;
function actual(parameter) {
  try {} catch (caught) {}
  label: for (const item of values) {
    item.run();
  }
}
actual();
if (ready) {}
const keywordTags = <section><const fake /><let fake /><class Fake /><function Fake /><using fake /></section>;
`
	definitions := newJavaScriptLanguage("javascript").sourceDefinitions(
		javascriptTestLines(source),
	)
	got := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		got = append(got, definition.symbol)
	}
	if want := []string{"text", "pattern", "template", "view", "actual", "item", "keywordTags"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("definitions = %#v, want %#v", got, want)
	}
}

func TestJavaScriptDefinitionsCoverDestructuringAndLoopBindings(t *testing.T) {
	t.Parallel()

	const source = `const {
  key: alias,
  short,
  nested: {deep = fallback},
  list: [first, , ...middle],
  ...rest
} = object, [head, ...tail] = values;
using resource = acquire();
await using asyncResource = acquireAsync();
for (let index = 0; index < limit; index++) {}
for (const {id, value: renamed} of items) {}
for (var [entry, ...remaining] in table) {}
`
	definitions := newJavaScriptLanguage("javascript").sourceDefinitions(
		javascriptTestLines(source),
	)
	got := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		got = append(got, definition.symbol)
		if definition.ownsScope || definition.scopeStart != definition.line ||
			definition.scopeEnd != definition.line {
			t.Fatalf("binding = %#v, want non-owning physical line", definition)
		}
	}
	want := []string{
		"alias", "short", "deep", "first", "middle", "rest", "head", "tail",
		"resource", "asyncResource", "index", "id", "renamed", "entry", "remaining",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("definitions = %#v, want %#v", got, want)
	}
}

func TestJavaScriptDefinitionsCoverStaticBracketExportsAndCommentedCallables(t *testing.T) {
	t.Parallel()

	const source = `exports["computed"] = function () {};
module.exports['nested'] = class {};
exports.dynamic = (/* keep transparent */ () => {});
const object = { handler: (/* keep transparent */ function () {}) };
exports[key] = function () {};
exports["not-a-name"] = class {};
`
	definitions := newJavaScriptLanguage("javascript").sourceDefinitions(
		javascriptTestLines(source),
	)
	if got, want := javascriptDefinitionSymbols(definitions),
		[]string{"computed", "nested", "dynamic", "object", "handler"}; !slices.Equal(got, want) {
		t.Fatalf("definitions = %#v, want %#v", got, want)
	}
	for _, symbol := range []string{"computed", "nested", "dynamic", "object", "handler"} {
		if definition := javascriptDefinitionNamed(t, definitions, symbol); !definition.ownsScope {
			t.Fatalf("definition %q = %#v, want owning scope", symbol, definition)
		}
	}
}

func TestJavaScriptImportsCoverStaticFormsReexportsAndCommonJS(t *testing.T) {
	t.Parallel()

	const source = `/** dependency group */
import "side-effect";
import value, {
  first as renamed,
  second,
} from "package" with { type: "json" };
export * as namespace from "other";
export {local as remote} from "remote";
export {local};
const loaded = require("common");
require("side-common");
require(variable, "not-canonical");
require("also-not-canonical", extra);
object.require("member");
require?.("optional");
const dynamic = import("lazy");
function nested() {
  return require("runtime");
}
`
	lines := javascriptTestLines(source)
	backend := newJavaScriptLanguage("javascript")
	analysis := backend.analysisForSource(strings.Join(lines, "\n"), len(lines))
	wantSpans := []javascriptLineSpan{
		{start: 1, end: 2},
		{start: 3, end: 6},
		{start: 7, end: 7},
		{start: 8, end: 8},
		{start: 10, end: 10},
		{start: 11, end: 11},
	}
	if !slices.Equal(analysis.imports, wantSpans) {
		t.Fatalf("imports = %#v, want %#v", analysis.imports, wantSpans)
	}
	start, end, ok := backend.importRange(lines)
	if !ok || start != 1 || end != 11 {
		t.Fatalf("import range = %d-%d, %v; want 1-11, true", start, end, ok)
	}
}

func TestJavaScriptImportsIgnoreDeferredClassAndConciseArrowRequires(t *testing.T) {
	t.Parallel()

	const source = `const loaded = require /* bundler hint */ ("dependency");
class Service {
  dependency = require("instance-time");
}
const callback = () => require("callback-time");
using = () => require("recovery-callback-time");
require("top-level");
run(() => require("nested-argument"), require("top-argument"));
`
	lines := javascriptTestLines(source)
	analysis := analyzeJavaScriptSource(strings.Join(lines, "\n"), len(lines))
	if len(analysis.recoverySpans) == 0 {
		t.Fatal("fixture did not exercise lexical recovery")
	}
	want := []javascriptLineSpan{{start: 1, end: 1}, {start: 7, end: 7}, {start: 8, end: 8}}
	if !slices.Equal(analysis.imports, want) {
		t.Fatalf("imports = %#v, want %#v", analysis.imports, want)
	}
}

func TestJavaScriptSearchMaskingDistinguishesExecutableSyntax(t *testing.T) {
	t.Parallel()

	const source = `const stringValue = "target // not comment";
const division = target / divisor;
const pattern = /target[//]{2}\/end/giu;
const template = ` + "`raw target ${outer(target, `nested ${inner(target)}`)} tail`" + `;
const view = <Panel title="target">target {render(target)}</Panel>;
// target in a line comment
/* target in a block comment */ target();
`
	lines := javascriptTestLines(source)
	backend := prepareLanguageBackend(newJavaScriptLanguage("javascript"), lines)
	masked := backend.searchLines(lines, true, true)
	if len(masked) != len(lines) || len(strings.Join(masked, "\n")) != len(strings.Join(lines, "\n")) {
		t.Fatalf("masked source changed coordinates: %#v", masked)
	}
	wantCounts := []int{0, 1, 0, 2, 1, 0, 1}
	for index, line := range masked {
		if got := backend.(symbolOccurrenceCounter).countSymbolOccurrences(line, "target"); got != wantCounts[index] {
			t.Fatalf("line %d target count = %d, want %d; masked=%q", index+1, got, wantCounts[index], line)
		}
	}
	commentsOnly := backend.searchLines(lines, true, false)
	if !strings.Contains(commentsOnly[0], "target // not comment") ||
		!strings.Contains(commentsOnly[2], "target[//]") ||
		strings.Contains(commentsOnly[5], "target") {
		t.Fatalf("comment masking confused literals and comments: %#v", commentsOnly)
	}
	stringsOnly := backend.searchLines(lines, false, true)
	if !strings.Contains(stringsOnly[5], "target in a line comment") ||
		!strings.Contains(stringsOnly[6], "target in a block comment") ||
		strings.Contains(stringsOnly[0], "target // not comment") {
		t.Fatalf("string masking confused comments and literals: %#v", stringsOnly)
	}
}

func TestJavaScriptScopesUseSmallestSemanticBlockAndNamedOwner(t *testing.T) {
	t.Parallel()

	const source = `/** outer docs */
export function outer(value) {
  if (value) {
    while (value.ready) {
      value.step();
    }
  } else {
    value.reset();
  }
}
const handler = () => {
  try {
    work();
  } catch (error) {
    recover(error);
  } finally {
    finish();
  }
};
`
	lines := javascriptTestLines(source)
	backend := prepareLanguageBackend(newJavaScriptLanguage("javascript"), lines)
	if start, end := backend.enclosingScope(lines, 5); start != 4 || end != 6 {
		t.Fatalf("smallest while scope = %d-%d, want 4-6", start, end)
	}
	resolver := backend.(navigationScopeResolver)
	if start, end := resolver.navigationScope(lines, 5); start != 1 || end != 10 {
		t.Fatalf("outer navigation scope = %d-%d, want 1-10", start, end)
	}
	if start, end := backend.enclosingScope(lines, 15); start != 14 || end != 16 {
		t.Fatalf("catch scope = %d-%d, want 14-16", start, end)
	}
	if start, end := resolver.navigationScope(lines, 15); start != 11 || end != 19 {
		t.Fatalf("handler navigation scope = %d-%d, want 11-19", start, end)
	}
}

func TestJavaScriptDecoratorsAndJSDocAttachToOwnedScopes(t *testing.T) {
	t.Parallel()

	const source = `@sealed
export class Service {
  /** method docs */
  @memo
  method() {
    work();
  }
}
`
	lines := javascriptTestLines(source)
	backend := prepareLanguageBackend(newJavaScriptLanguage("javascript"), lines)
	definitions := backend.sourceDefinitions(lines)
	service := javascriptDefinitionNamed(t, definitions, "Service")
	if service.line != 2 || service.scopeStart != 1 || service.scopeEnd != 8 ||
		!service.ownsScope {
		t.Fatalf("Service definition = %#v, want decorated scope 1-8", service)
	}
	method := javascriptDefinitionNamed(t, definitions, "method")
	if method.line != 5 || method.scopeStart != 3 || method.scopeEnd != 7 ||
		!method.ownsScope {
		t.Fatalf("method definition = %#v, want documented/decorated scope 3-7", method)
	}
	if start, end := backend.(navigationScopeResolver).navigationScope(lines, 6); start != 3 || end != 7 {
		t.Fatalf("method navigation scope = %d-%d, want 3-7", start, end)
	}
}

func TestJavaScriptFunctionValuedVariablesOwnWholeDocumentedDeclarations(t *testing.T) {
	t.Parallel()

	const source = `/** callback docs */
export const
  callback = () => {
    work();
  };
/** factory docs */
const factory =
  function named() {
    work();
  };
/** type docs */
const Type =
  class Inner {
    method() {}
  };
`
	lines := javascriptTestLines(source)
	backend := prepareLanguageBackend(newJavaScriptLanguage("javascript"), lines)
	definitions := backend.sourceDefinitions(lines)
	for _, test := range []struct {
		symbol     string
		start, end int
	}{
		{symbol: "callback", start: 1, end: 5},
		{symbol: "factory", start: 6, end: 10},
		{symbol: "Type", start: 11, end: 15},
	} {
		definition := javascriptDefinitionNamed(t, definitions, test.symbol)
		if !definition.ownsScope || definition.scopeStart != test.start ||
			definition.scopeEnd != test.end {
			t.Fatalf("definition %q = %#v, want owning scope %d-%d", test.symbol, definition, test.start, test.end)
		}
	}
	if start, end := backend.(navigationScopeResolver).navigationScope(lines, 4); start != 1 || end != 5 {
		t.Fatalf("callback navigation scope = %d-%d, want 1-5", start, end)
	}
}

func TestJavaScriptOccurrenceCounterUsesECMAScriptBoundaries(t *testing.T) {
	t.Parallel()

	counter := newJavaScriptLanguage("javascript")
	line := `foo $foo foo$bar foo\u200Cbar ` + "foo\u200Cbar foo\u200Dbar" +
		` #foo obj.foo foo`
	if got := counter.countSymbolOccurrences(line, "foo"); got != 3 {
		t.Fatalf("foo occurrences = %d, want 3", got)
	}
	if got := counter.countSymbolOccurrences(`const \u0061 = \u0061; u0061`, `\u0061`); got != 2 {
		t.Fatalf("escaped occurrences = %d, want 2", got)
	}
	if got := counter.countSymbolOccurrences(`const \u0061 = \u0061; u0061`, "u0061"); got != 1 {
		t.Fatalf("escape-fragment occurrences = %d, want 1", got)
	}
}

func javascriptTestLines(source string) []string {
	return strings.Split(strings.TrimSuffix(source, "\n"), "\n")
}

func javascriptDefinitionNamed(
	t *testing.T,
	definitions []sourceDefinition,
	symbol string,
) sourceDefinition {
	t.Helper()
	for _, definition := range definitions {
		if definition.symbol == symbol {
			return definition
		}
	}
	t.Fatalf("definition %q missing from %#v", symbol, definitions)
	return sourceDefinition{}
}
