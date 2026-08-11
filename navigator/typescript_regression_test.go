package navigator

import (
	"slices"
	"strings"
	"testing"
)

func TestTypeScriptMissingTokenRecoveryDoesNotTreatASIAsMalformed(t *testing.T) {
	t.Parallel()

	const source = `const first = 1
const second = 2
function run(): void {
  return
}`
	tree, ok := parseTypeScriptSyntax(source, false)
	if !ok {
		t.Fatal("valid ASI source did not parse")
	}
	if spans := typeScriptSyntaxMissingTokenSpans(tree, len(source)); len(spans) != 0 {
		t.Fatalf("valid ASI source produced missing-token recovery spans: %#v", spans)
	}
}

func TestTypeScriptContextualKeywordsRemainMemberNames(t *testing.T) {
	t.Parallel()

	const source = `interface InterfaceNames {
  abstract: boolean;
  abstract(): void;
  accessor: string;
}
type AliasNames = {
  abstract: false;
  accessor(): void;
};
class ClassNames {
  abstract: boolean = false;
  abstract() {}
  accessor: boolean = false;
  accessor() {}
}`
	definitions := newTypeScriptLanguage("typescript", false).sourceDefinitions(
		typeScriptTestLines(source),
	)
	want := []string{
		"InterfaceNames", "abstract", "abstract", "accessor",
		"AliasNames", "abstract", "accessor",
		"ClassNames", "abstract", "abstract", "accessor", "accessor",
	}
	if got := typeScriptDefinitionSymbols(definitions); !slices.Equal(got, want) {
		t.Fatalf("contextual member definitions = %#v, want %#v", got, want)
	}
	class := typeScriptFirstDefinition(t, definitions, "ClassNames")
	if class.scopeStart != 10 || class.scopeEnd != 15 {
		t.Fatalf("contextual class scope = %#v, want 10-15", class)
	}
}

func TestTypeScriptQuotedEnumMembersWithoutInitializersAreDefinitions(t *testing.T) {
	t.Parallel()

	const source = `enum Status { "ready", 'done', Plain }`
	definitions := newTypeScriptLanguage("typescript", false).sourceDefinitions(
		typeScriptTestLines(source),
	)
	want := []string{"Status", "ready", "done", "Plain"}
	if got := typeScriptDefinitionSymbols(definitions); !slices.Equal(got, want) {
		t.Fatalf("enum definitions = %#v, want %#v", got, want)
	}
}

func TestTypeScriptDefinitionsCoverNamespacesParameterPropertiesAndMemberNames(t *testing.T) {
	t.Parallel()

	const source = `export namespace Root.Middle.Leaf {
  export module Nested { export const value = 1; }
}
export default abstract class Service {
  constructor(
    public readonly id: string,
    private label?: string,
    protected override count = 0,
    ordinary: string = "not-a-field",
  ) {}
  accessor current: string;
  #private = 1;
  "quoted"(): void {}
}
interface Contract {
  "quotedProperty"?: string;
  "quotedMethod"(): void;
}
declare function ambient(value: string): void;
declare const configured: unique symbol;`
	definitions := newTypeScriptLanguage("typescript", false).sourceDefinitions(
		typeScriptTestLines(source),
	)
	got := typeScriptDefinitionSymbols(definitions)
	want := []string{
		"Root", "Middle", "Leaf", "Nested", "value", "Service", "constructor",
		"id", "label", "count", "current", "#private", "quoted", "Contract",
		"quotedProperty", "quotedMethod", "ambient", "configured",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("definitions = %#v, want %#v", got, want)
	}
	for _, symbol := range []string{"id", "label", "count", "current", "#private", "quotedProperty"} {
		definition := typeScriptFirstDefinition(t, definitions, symbol)
		if definition.ownsScope {
			t.Fatalf("definition %q unexpectedly owns scope: %#v", symbol, definition)
		}
	}
	if slices.Contains(got, "ordinary") {
		t.Fatalf("ordinary constructor parameter became field definition: %#v", got)
	}
}

func TestTypeScriptDeclarationMergingAndOverloadsPreserveDistinctLocations(t *testing.T) {
	t.Parallel()

	const source = `interface Merge { first: string }
interface Merge { second: number }
namespace Merge { export const value = 1 }
function call(value: string): string;
function call(value: number): number;
function call(value: unknown) { return value; }`
	definitions := newTypeScriptLanguage("typescript", false).sourceDefinitions(
		typeScriptTestLines(source),
	)
	mergeLines := make([]int, 0, 3)
	callLines := make([]int, 0, 3)
	for _, definition := range definitions {
		switch definition.symbol {
		case "Merge":
			mergeLines = append(mergeLines, definition.line)
		case "call":
			callLines = append(callLines, definition.line)
		}
	}
	if !slices.Equal(mergeLines, []int{1, 2, 3}) {
		t.Fatalf("Merge lines = %#v, want 1,2,3", mergeLines)
	}
	if !slices.Equal(callLines, []int{4, 5, 6}) {
		t.Fatalf("call lines = %#v, want 4,5,6", callLines)
	}
}

func TestTypeScriptQuotedDefinitionsRemainSearchableWithStringsMasked(t *testing.T) {
	t.Parallel()

	const source = `enum Status { "ready" = 1 }
interface Contract { "run"(): void; "value"?: string }
class Service { "method"(): void {} }`
	lines := typeScriptTestLines(source)
	backend := prepareLanguageBackend(newTypeScriptLanguage("typescript", false), lines)
	masked := backend.searchLines(lines, false, true)
	for _, symbol := range []string{"ready", "run", "value", "method"} {
		found := false
		for _, line := range masked {
			if backend.(symbolOccurrenceCounter).countSymbolOccurrences(line, symbol) > 0 {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("quoted definition %q was masked: %#v", symbol, masked)
		}
	}
}

func TestTypeScriptReferenceDirectiveRecognitionIsStrict(t *testing.T) {
	t.Parallel()

	tests := []struct {
		comment string
		want    bool
	}{
		{comment: `/// <reference path="./ambient.d.ts" />`, want: true},
		{comment: `/// <reference types = 'node' preserve="true" />`, want: true},
		{comment: `/// <reference lib="esnext.disposable"/>`, want: true},
		{comment: `/// <reference no-default-lib="true" />`, want: false},
		{comment: `// <reference path="fake" />`, want: false},
		{comment: `//// <reference path="fake" />`, want: false},
		{comment: `/// <referencepath="fake" />`, want: false},
		{comment: `/// <reference path="" />`, want: false},
		{comment: `/// <reference path="fake">`, want: false},
		{comment: `/// <reference path="fake" garbage />`, want: false},
		{comment: `/// <reference path="first" path="second" />`, want: false},
		{comment: `/// <reference path="unterminated />`, want: false},
	}
	for _, test := range tests {
		t.Run(test.comment, func(t *testing.T) {
			t.Parallel()
			if got := javascriptTypeScriptReferenceDirective(test.comment); got != test.want {
				t.Fatalf("directive = %v, want %v", got, test.want)
			}
		})
	}
}

func TestTypeScriptReferenceDirectivesOnlyCountBeforeFirstStatement(t *testing.T) {
	t.Parallel()

	const source = `// leading ordinary comment
/// <reference path="./leading.d.ts" />
const value = 1;
/// <reference types="too-late" />
function nested(): void {
  /// <reference lib="not-top-level" />
}`
	analysis := newTypeScriptLanguage("typescript", false).analysisForSource(source, 7)
	want := []javascriptLineSpan{{start: 2, end: 2}}
	if !slices.Equal(analysis.imports, want) {
		t.Fatalf("imports = %#v, want %#v", analysis.imports, want)
	}
}

func TestTypeScriptImportTypesCoverCanonicalOptionsAndAssertions(t *testing.T) {
	t.Parallel()

	const source = `type Direct = import("direct").Thing;
type WithOptions = import("json", { with: { type: "json" } }).Data;
type Query = typeof import("query");
const runtime = import("runtime");
const assertedRuntime = import("runtime-two") as Promise<unknown>;
const typed = value as import("cast").Thing;
const satisfied = value satisfies import("satisfied").Shape;
const actual = jest.requireActual<typeof import("node:child_process")>("node:child_process");
const spaced = callee < import("spaced") > ("value");
const relational = (callee < import("runtime-three")) > (arg);`
	analysis := newTypeScriptLanguage("typescript", false).analysisForSource(source, 10)
	want := []javascriptLineSpan{
		{start: 1, end: 1},
		{start: 2, end: 2},
		{start: 3, end: 3},
		{start: 6, end: 6},
		{start: 7, end: 7},
		{start: 8, end: 8},
		{start: 9, end: 9},
	}
	if !slices.Equal(analysis.imports, want) {
		t.Fatalf("imports = %#v, want %#v", analysis.imports, want)
	}
}

func TestTypeScriptPrimitiveStringTypeIsNotMaskedAsLiteral(t *testing.T) {
	t.Parallel()

	const source = `type Text = string;
interface Value { field: string }
const literal = "string";`
	lines := typeScriptTestLines(source)
	backend := prepareLanguageBackend(newTypeScriptLanguage("typescript", false), lines)
	masked := backend.searchLines(lines, false, true)
	if got := backend.(symbolOccurrenceCounter).countSymbolOccurrences(masked[0], "string"); got != 1 {
		t.Fatalf("type alias primitive count = %d, want 1; %q", got, masked[0])
	}
	if got := backend.(symbolOccurrenceCounter).countSymbolOccurrences(masked[1], "string"); got != 1 {
		t.Fatalf("property primitive count = %d, want 1; %q", got, masked[1])
	}
	if got := backend.(symbolOccurrenceCounter).countSymbolOccurrences(masked[2], "string"); got != 0 {
		t.Fatalf("literal count = %d, want 0; %q", got, masked[2])
	}
}

func TestTypeScriptModernUsingAccessorsAndNamespaceExportDefinitions(t *testing.T) {
	t.Parallel()

	const source = `using resource = existing, second = acquire(), third = other;
await using asyncResource = existingAsync, asyncSecond = acquireAsync(), asyncThird = otherAsync;
ordinary = first, ordinarySecond = second;
class Service {
  accessor value = 1;
  static accessor shared = 2;
}
export as namespace Library;`
	definitions := newTypeScriptLanguage("typescript", false).sourceDefinitions(
		typeScriptTestLines(source),
	)
	got := typeScriptDefinitionSymbols(definitions)
	want := []string{
		"resource", "second", "third", "asyncResource", "asyncSecond", "asyncThird",
		"Service", "value", "shared", "Library",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("definitions = %#v, want %#v", got, want)
	}
	paddedSource := source + strings.Repeat(" ", javascriptMaximumConcreteParseBytes+1)
	padded := newTypeScriptLanguage("typescript", false).analysisForSource(
		paddedSource, len(typeScriptTestLines(source)),
	)
	if padded.tree != nil {
		t.Fatal("padded modern-declaration fixture unexpectedly retained a concrete tree")
	}
	if paddedSymbols := typeScriptDefinitionSymbols(padded.definitions); !slices.Equal(
		paddedSymbols, want,
	) {
		t.Fatalf("fallback definitions = %#v, want %#v", paddedSymbols, want)
	}
	if slices.Contains(got, "accessor") {
		t.Fatalf("accessor modifier became definition: %#v", got)
	}
}

func TestTypeScriptExportImportRequireAndAMDDependencyImports(t *testing.T) {
	t.Parallel()

	const source = `/// <amd-dependency path="legacy" name="old" />
export import External = require("pkg");
import Internal = require("other");
export import Local = Namespace.Member;`
	analysis := newTypeScriptLanguage("typescript", false).analysisForSource(source, 4)
	want := []javascriptLineSpan{
		{start: 1, end: 1},
		{start: 2, end: 2},
		{start: 3, end: 3},
	}
	if !slices.Equal(analysis.imports, want) {
		t.Fatalf("imports = %#v, want %#v", analysis.imports, want)
	}
}
