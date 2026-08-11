package navigator

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestTypeScriptConcreteDefinitionsCoverRuntimeAndTypeDeclarations(t *testing.T) {
	t.Parallel()

	const source = `/** Box documentation */
export declare abstract class Box<T> {
  abstract value: T;
  readonly field?: string;
  constructor(value: T);
  constructor(value: T) {}
  abstract run<U>(arg: U): Promise<T>;
  method<U>(arg: U): T { return this.value; }
  get item(): T { return this.value; }
  set item(value: T) {}
}
export interface Shape<T = string> {
  readonly prop?: T;
  method?<U>(arg: U): T;
}
export type Result<T> = { ok: true; value: T } | { ok: false };
export const enum Status { Ready, Busy = 2, "done" = 3 }
export namespace API.Inner {
  export const value = 1;
  export function run(): void;
}
declare module "virtual" { export interface Config {} }
declare global { interface Window { custom: string } }
export function overload(x: string): string;
export function overload(x: number): number;
export function overload(x: unknown) { return x; }`

	backend := newTypeScriptLanguage("typescript", false)
	analysis := backend.analysisForSource(source, len(typeScriptTestLines(source)))
	if analysis.tree == nil || javascriptSyntaxRootIsError(analysis.tree) ||
		len(analysis.recoverySpans) != 0 {
		t.Fatalf("fixture did not use clean concrete parser: tree=%v recovery=%#v", analysis.tree != nil, analysis.recoverySpans)
	}
	got := typeScriptDefinitionSymbols(analysis.definitions)
	want := []string{
		"Box", "value", "field", "constructor", "constructor", "run", "method",
		"item", "item", "Shape", "prop", "method", "Result", "ok", "value", "ok",
		"Status", "Ready", "Busy", "done", "API", "Inner", "value", "run", "Config",
		"Window", "custom", "overload", "overload", "overload",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("definitions = %#v, want %#v", got, want)
	}

	for _, symbol := range []string{
		"Box", "constructor", "run", "method", "item", "Shape", "Result", "Status",
		"API", "Inner", "Config", "Window", "overload",
	} {
		if !typeScriptHasOwningDefinition(analysis.definitions, symbol) {
			t.Fatalf("definition %q has no owning scope: %#v", symbol, analysis.definitions)
		}
	}
	for _, symbol := range []string{"field", "prop", "Ready", "Busy", "done", "custom"} {
		if definition := typeScriptFirstDefinition(t, analysis.definitions, symbol); definition.ownsScope || definition.scopeStart != definition.line ||
			definition.scopeEnd != definition.line {
			t.Fatalf("definition %q = %#v, want physical-line scope", symbol, definition)
		}
	}
	box := typeScriptFirstDefinition(t, analysis.definitions, "Box")
	if box.scopeStart != 1 || box.scopeEnd != 11 {
		t.Fatalf("Box scope = %#v, want 1-11", box)
	}
}

func TestTypeScriptConcreteImportsCoverTypeAndRuntimeForms(t *testing.T) {
	t.Parallel()

	const source = `/// <reference path="./ambient.d.ts" />
/// <reference types="node" />
import type { Foo as Bar } from "types";
import value = require("legacy");
export type { Thing } from "other";
export { runtime } from "runtime";
export import Alias = Namespace.Member;
type Query = typeof import("query");
const eager = require("eager");
class Service {
  field = require("instance-deferred");
  static shared = require("static-eager");
  [require("computed-eager")](): void {}
  method(): void { require("method-deferred"); }
}`

	lines := typeScriptTestLines(source)
	backend := newTypeScriptLanguage("typescript", false)
	analysis := backend.analysisForSource(source, len(lines))
	want := []javascriptLineSpan{
		{start: 1, end: 1},
		{start: 2, end: 2},
		{start: 3, end: 3},
		{start: 4, end: 4},
		{start: 5, end: 5},
		{start: 6, end: 6},
		{start: 8, end: 8},
		{start: 9, end: 9},
		{start: 10, end: 15},
	}
	if !slices.Equal(analysis.imports, want) {
		t.Fatalf("imports = %#v, want %#v", analysis.imports, want)
	}
	if start, end, ok := backend.importRange(lines); !ok || start != 1 || end != 15 {
		t.Fatalf("import range = %d-%d, %v; want 1-15, true", start, end, ok)
	}
}

func TestTypeScriptParameterDecoratorImportsAreEager(t *testing.T) {
	const source = `@sealed
class Service {
  constructor(
    @inject<Token>(require("constructor-eager")) dependency: Token,
    value = require("constructor-default-deferred"),
  ) {}
  method(
    @inject(require("method-eager")) dependency: Token,
    @inject(() => require("decorator-arrow-deferred")) lazy: Token,
    value = class { @dec(require("nested-class-deferred")) nested() {} },
  ) { require("method-body-deferred"); }
}

const eager = require("top-eager");`
	lines := typeScriptTestLines(source)
	want := []javascriptLineSpan{{start: 1, end: 12}, {start: 14, end: 14}}
	clean := newTypeScriptLanguage("typescript", false).analysisForSource(source, len(lines))
	if clean.tree == nil || javascriptSyntaxRootIsError(clean.tree) ||
		len(clean.recoverySpans) != 0 {
		t.Fatalf("parameter-decorator fixture did not parse cleanly: tree=%v recovery=%#v",
			clean.tree != nil, clean.recoverySpans)
	}
	if !slices.Equal(clean.imports, want) {
		t.Fatalf("concrete parameter-decorator imports = %#v, want %#v", clean.imports, want)
	}

	paddedSource := source + strings.Repeat(" ", javascriptMaximumConcreteParseBytes+1)
	padded := newTypeScriptLanguage("typescript", false).analysisForSource(
		paddedSource, len(lines),
	)
	if padded.tree != nil {
		t.Fatal("padded parameter-decorator fixture unexpectedly retained a concrete tree")
	}
	if !slices.Equal(padded.imports, want) {
		t.Fatalf("fallback parameter-decorator imports = %#v, want %#v", padded.imports, want)
	}
}

func TestTypeScriptAndTSXSelectDistinctConcreteGrammars(t *testing.T) {
	t.Parallel()

	const typeScriptSource = `const asserted = <Result<string>>value;
const generic = call<Result<string>>(value);`
	ts := newTypeScriptLanguage("typescript", false).analysisForSource(typeScriptSource, 2)
	if ts.tree == nil || len(ts.recoverySpans) != 0 || javascriptSyntaxRootIsError(ts.tree) {
		t.Fatalf("TypeScript angle assertions did not parse concretely: %#v", ts.recoverySpans)
	}

	const tsxSource = `type Props = { title: string };
const View = <T extends object,>(props: Props & T) => (
  <Panel title="hidden" value={props.title}>{render<T>(props)}</Panel>
);`
	tsx := newTypeScriptLanguage("tsx", true).analysisForSource(tsxSource, 4)
	if tsx.tree == nil || len(tsx.recoverySpans) != 0 || javascriptSyntaxRootIsError(tsx.tree) {
		t.Fatalf("TSX generic arrow/JSX did not parse concretely: %#v", tsx.recoverySpans)
	}
	if got, want := typeScriptDefinitionSymbols(tsx.definitions),
		[]string{"Props", "title", "View"}; !slices.Equal(got, want) {
		t.Fatalf("TSX definitions = %#v, want %#v", got, want)
	}
}

func TestTypeScriptSearchMaskingCoversTypeLiteralsDecoratorsAndTSX(t *testing.T) {
	t.Parallel()

	const source = `@decorate("target")
class Service {
  literal: "target" = "target";
  pattern = /target/u;
  render(): JSX.Element {
    return <Panel title="target">target {call(target)}</Panel>;
  }
}
// target comment
type Named = { "target"?: string; value: ` + "`target-${string}`" + ` };`
	lines := typeScriptTestLines(source)
	backend := prepareLanguageBackend(newTypeScriptLanguage("tsx", true), lines)
	masked := backend.searchLines(lines, true, true)
	if len(masked) != len(lines) || len(strings.Join(masked, "\n")) != len(source) {
		t.Fatalf("mask changed coordinates: %#v", masked)
	}
	wantCounts := []int{0, 0, 0, 0, 0, 1, 0, 0, 0, 1}
	for index, line := range masked {
		if got := backend.(symbolOccurrenceCounter).countSymbolOccurrences(line, "target"); got != wantCounts[index] {
			t.Fatalf("line %d target count = %d, want %d; masked=%q", index+1, got, wantCounts[index], line)
		}
	}
	if definitions := backend.sourceDefinitions(lines); !slices.Contains(
		typeScriptDefinitionSymbols(definitions), "target",
	) {
		t.Fatalf("quoted property definition lost: %#v", definitions)
	}
}

func TestTypeScriptScopesPreferSemanticBlockAndNamedOwner(t *testing.T) {
	t.Parallel()

	const source = `/** service docs */
export class Service<T> {
  method(value: T): T {
    if (value) {
      return value;
    }
    throw new Error();
  }
}
export interface Shape {
  method(
    value: string,
  ): string;
}
export namespace API {
  export function load(): void {
    while (ready) {
      work();
    }
  }
}`
	lines := typeScriptTestLines(source)
	backend := prepareLanguageBackend(newTypeScriptLanguage("typescript", false), lines)
	if start, end := backend.enclosingScope(lines, 5); start != 4 || end != 6 {
		t.Fatalf("enclosing if scope = %d-%d, want 4-6", start, end)
	}
	resolver := backend.(navigationScopeResolver)
	if start, end := resolver.navigationScope(lines, 5); start != 3 || end != 8 {
		t.Fatalf("method navigation scope = %d-%d, want 3-8", start, end)
	}
	if start, end := resolver.navigationScope(lines, 13); start != 11 || end != 13 {
		t.Fatalf("interface method scope = %d-%d, want 11-13", start, end)
	}
	if start, end := resolver.navigationScope(lines, 18); start != 16 || end != 20 {
		t.Fatalf("namespace function scope = %d-%d, want 16-20", start, end)
	}
}

func TestTypeScriptPreparedBackendIsImmutableAndFlavorPreserving(t *testing.T) {
	t.Parallel()

	first := typeScriptTestLines("interface First { value: string }\n")
	second := typeScriptTestLines("type Second = number;\n")
	prepared, ok := prepareLanguageBackend(
		newTypeScriptLanguage("typescript", false), first,
	).(typescriptLanguage)
	if !ok || prepared.flavor != javascriptSyntaxFlavorTypeScript {
		t.Fatalf("prepared backend = %#v", prepared)
	}
	if got := typeScriptDefinitionSymbols(prepared.sourceDefinitions(first)); !slices.Equal(got, []string{"First", "value"}) {
		t.Fatalf("first definitions = %#v", got)
	}
	if got := typeScriptDefinitionSymbols(prepared.sourceDefinitions(second)); !slices.Equal(got, []string{"Second"}) {
		t.Fatalf("second definitions = %#v", got)
	}
	if got := typeScriptDefinitionSymbols(prepared.sourceDefinitions(first)); !slices.Equal(got, []string{"First", "value"}) {
		t.Fatalf("cached first definitions changed = %#v", got)
	}
}

func TestTypeScriptExtensionResultsUseDedicatedLanguageNames(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeFile(t, root, "alpha.ts", "export interface Alpha {}\n")
	writeFile(t, root, "beta.tsx", "export const Beta = () => <div />;\n")
	writeFile(t, root, "gamma.mts", "export type Gamma = string;\n")
	writeFile(t, root, "delta.cts", "export enum Delta { Ready }\n")
	view, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	response, err := view.FindMany([]string{"Alpha", "Beta", "Gamma", "Delta"}, Options{
		Include: IncludeDefs,
		Return:  ReturnLine,
		Limit:   10,
	})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, item := range response {
		for _, result := range item.Results {
			got[result.Path] = result.Language
		}
	}
	want := map[string]string{
		"alpha.ts":  "typescript",
		"beta.tsx":  "tsx",
		"gamma.mts": "mts",
		"delta.cts": "cts",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("languages = %#v, want %#v", got, want)
	}
}

func typeScriptTestLines(source string) []string {
	return strings.Split(strings.TrimSuffix(source, "\n"), "\n")
}

func typeScriptDefinitionSymbols(definitions []sourceDefinition) []string {
	symbols := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		symbols = append(symbols, definition.symbol)
	}
	return symbols
}

func typeScriptFirstDefinition(
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
	t.Fatalf("missing definition %q in %#v", symbol, definitions)
	return sourceDefinition{}
}

func typeScriptHasOwningDefinition(definitions []sourceDefinition, symbol string) bool {
	for _, definition := range definitions {
		if definition.symbol == symbol && definition.ownsScope {
			return true
		}
	}
	return false
}
