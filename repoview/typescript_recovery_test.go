package repoview

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestTypeScriptNestedCallableTypeImportsRemainImports(t *testing.T) {
	const source = `function load(
  input: import("parameter").Input,
): import("return").Output {
  type Local = import("local").Value;
  const adapt = (
    value: import("arrow-parameter").Input,
  ): import("arrow-return").Output => value;
  return adapt(input);
}
const factory = async (importOriginal: ImportOriginal) => {
  const actual = await importOriginal<typeof import("mocked")>();
	const actualJSON = await importOriginal<typeof import("mocked-json", { with: { type: "json" } })>();
  return actual;
};`

	analysis := newTypeScriptLanguage("typescript", false).analysisForSource(source, 14)
	want := []javascriptLineSpan{
		{start: 2, end: 2},
		{start: 3, end: 3},
		{start: 4, end: 4},
		{start: 6, end: 6},
		{start: 7, end: 7},
		{start: 11, end: 11},
		{start: 12, end: 12},
	}
	if !slices.Equal(analysis.imports, want) {
		t.Fatalf("nested type imports = %#v, want %#v", analysis.imports, want)
	}
	if got, wantDefinitions := typeScriptDefinitionSymbols(analysis.definitions),
		[]string{"load", "Local", "adapt", "factory", "actual", "actualJSON"}; !slices.Equal(got, wantDefinitions) {
		t.Fatalf("nested type-import definitions = %#v, want %#v", got, wantDefinitions)
	}
}

func TestTypeScriptRecoveryRejectsObjectKeysAfterGenericTypeImports(t *testing.T) {
	const source = `const factory = async (importOriginal: ImportOriginal) => {
  const actual = await importOriginal<typeof import("mocked")>();
  return {
    ...actual,
    getWorkflowAuthoringCapabilities: vi.fn(),
    inspectPublishedWorkflowDefinition: vi.fn(),
  };
};`
	padded := source + strings.Repeat(" ", javascriptMaximumConcreteParseBytes+1)
	analysis := newTypeScriptLanguage("typescript", false).analysisForSource(
		padded, len(typeScriptTestLines(source)),
	)
	if analysis.tree != nil {
		t.Fatal("padded generic type-import fixture unexpectedly retained a concrete tree")
	}
	if got, want := typeScriptDefinitionSymbols(analysis.definitions),
		[]string{"factory", "actual"}; !slices.Equal(got, want) {
		t.Fatalf("padded generic type-import definitions = %#v, want %#v", got, want)
	}
}

func TestTypeScriptRootErrorDoesNotSynthesizeObjectKeyBindings(t *testing.T) {
	const source = `vi.mock("pkg", async (importOriginal) => {
  const actual = await importOriginal<typeof import("pkg")>()
  return {
    ...actual,
    targetObjectKey: vi.fn(),
  }
})
describe("suite", () => {
  it("case", () => {
    render(<Component target={{ kind: "value" }} />)
  })
})`
	analysis := newTypeScriptLanguage("tsx", true).analysisForSource(
		source, len(typeScriptTestLines(source)),
	)
	if !javascriptSyntaxRootIsError(analysis.tree) {
		t.Fatal("adversarial TSX fixture did not exercise whole-file error recovery")
	}
	if got := typeScriptDefinitionSymbols(analysis.definitions); slices.Contains(
		got, "targetObjectKey",
	) {
		t.Fatalf("whole-file recovery synthesized object-key binding: %#v", got)
	}
}

func TestTypeScriptRecoversAnonymousDefaultFunctionReturnMembers(t *testing.T) {
	const source = `export default function (): {
  localeError: (issue: unknown) => string;
};`
	analysis := newTypeScriptLanguage("typescript", false).analysisForSource(source, 3)
	if len(analysis.recoverySpans) == 0 {
		t.Fatal("anonymous default function signature did not exercise grammar recovery")
	}
	if got, want := typeScriptDefinitionSymbols(analysis.definitions),
		[]string{"localeError"}; !slices.Equal(got, want) {
		t.Fatalf("anonymous default return definitions = %#v, want %#v", got, want)
	}
}

func TestTypeScriptFallbackPreservesSemicolonlessAliasScopes(t *testing.T) {
	const source = `type WorkflowRunSecretValues = Record<string, string>

interface WorkflowRunSubmission {
  workflowId: string
}
type JobDraft = Partial<Job>
type StepDraft = Partial<Step>
interface Following {
  value: string
}`
	lines := typeScriptTestLines(source)
	clean := newTypeScriptLanguage("typescript", false).analysisForSource(source, len(lines))
	paddedSource := source + strings.Repeat(" ", javascriptMaximumConcreteParseBytes+1)
	padded := newTypeScriptLanguage("typescript", false).analysisForSource(
		paddedSource, len(lines),
	)
	if padded.tree != nil {
		t.Fatal("padded alias fixture unexpectedly retained a concrete tree")
	}
	if !slices.Equal(padded.definitions, clean.definitions) {
		t.Fatalf("padded alias definitions = %#v, want concrete %#v",
			padded.definitions, clean.definitions)
	}
}

func TestTypeScriptFallbackDoesNotPromoteGenericArgumentsToBindings(t *testing.T) {
	const source = `const fields = operation.fields as Record<string, unknown>;
const mutation = useMutation<
  WorkflowDevelopmentTestResult,
  Error,
  WorkflowTriggerExecutionSubmission
>({ onSuccess() {} });`
	for _, test := range []struct {
		name   string
		flavor javascriptSyntaxFlavor
	}{
		{name: "typescript", flavor: javascriptSyntaxFlavorTypeScript},
		{name: "tsx", flavor: javascriptSyntaxFlavorTSX},
	} {
		t.Run(test.name, func(t *testing.T) {
			padded := source + strings.Repeat(" ", javascriptMaximumConcreteParseBytes+1)
			analysis := analyzeJavaScriptSourceFlavor(
				padded, len(typeScriptTestLines(source)), test.flavor,
			)
			if analysis.tree != nil {
				t.Fatal("padded generic fixture unexpectedly retained a concrete tree")
			}
			if got, want := typeScriptDefinitionSymbols(analysis.definitions),
				[]string{"fields", "mutation", "onSuccess"}; !slices.Equal(got, want) {
				t.Fatalf("padded generic definitions = %#v, want %#v", got, want)
			}
		})
	}
}

func TestTypeScriptOversizedFallbackPreservesDeclarationsAndCallableRequires(t *testing.T) {
	const prefix = `interface OversizedShape { field: string }
type OversizedAlias = { nested: number };
enum OversizedStatus { Ready, Busy }
namespace OversizedAPI { export class Service {} }
const eager: unknown = require("eager");
const deferred: () => unknown = () => require("deferred");
function typed(): void { require("function-deferred"); }
class Loaders {
  static eagerField: unknown = require("static-eager");
  instanceField: unknown = require("instance-deferred");
  method(): void { require("method-deferred"); }
}
`
	source := prefix + strings.Repeat(" ", javascriptMaximumConcreteParseBytes+1)
	lines := strings.Split(source, "\n")
	analysis := newTypeScriptLanguage("typescript", false).analysisForSource(source, len(lines))
	if analysis.tree != nil {
		t.Fatal("oversized fixture unexpectedly used concrete syntax tree")
	}

	wantDefinitions := []string{
		"OversizedShape", "field", "OversizedAlias", "nested", "OversizedStatus",
		"Ready", "Busy", "OversizedAPI", "Service", "eager", "deferred", "typed",
		"Loaders", "eagerField", "instanceField", "method",
	}
	if got := typeScriptDefinitionSymbols(analysis.definitions); !slices.Equal(got, wantDefinitions) {
		t.Errorf("oversized definitions = %#v, want %#v", got, wantDefinitions)
	}
	wantImports := []javascriptLineSpan{{start: 5, end: 5}, {start: 8, end: 12}}
	if !slices.Equal(analysis.imports, wantImports) {
		t.Errorf("oversized imports = %#v, want %#v", analysis.imports, wantImports)
	}
}

func TestTypeScriptDenseFallbackPreservesModifierMatrix(t *testing.T) {
	const declarationCount = 14_000
	var source strings.Builder
	source.WriteString(`abstract class Service {
  public method(): void {}
  private field!: string;
  protected readonly optional?: number;
  static accessor shared = 1;
  abstract run<T>(): T;
  override execute(): void {}
  declare ambient: string;
}
`)
	for index := range declarationCount {
		fmt.Fprintf(&source, "type Pad%d = number;\n", index)
	}

	text := source.String()
	if javascriptConcreteSyntaxAllowedFlavor(text, javascriptSyntaxFlavorTypeScript) {
		t.Fatal("dense TypeScript fixture did not exceed the whole-file parser budget")
	}
	analysis := newTypeScriptLanguage("typescript", false).analysisForSource(
		text, len(strings.Split(text, "\n")),
	)
	if analysis.tree != nil {
		t.Fatal("dense TypeScript fixture unexpectedly retained a whole-file tree")
	}
	symbols := typeScriptDefinitionSymbols(analysis.definitions)
	wantPrefix := []string{
		"Service", "method", "field", "optional", "shared", "run", "execute", "ambient",
	}
	last := ""
	if len(symbols) > 0 {
		last = symbols[len(symbols)-1]
	}
	if len(symbols) != len(wantPrefix)+declarationCount ||
		!slices.Equal(symbols[:len(wantPrefix)], wantPrefix) ||
		last != fmt.Sprintf("Pad%d", declarationCount-1) {
		t.Fatalf("dense definitions = %d, prefix=%#v, last=%q", len(symbols),
			symbols[:min(len(symbols), len(wantPrefix))], last)
	}
}

func TestTypeScriptFallbackMatchesConcreteForComplexMemberBoundaries(t *testing.T) {
	const source = `interface Contract<T extends { id: string }> {
  "event": Event;
  method<U, V>(value: U, other: V): Promise<T>;
  (input: T, options?: unknown): T;
  new (input: T): Contract<T>;
}
type Alias<T extends { id: string }> = { "value": T };
@sealed()
class Service {
  static {}
  @logged()
  method(): { id: string } { return { id: "value" }; }
  protected "quoted"(): void {}
}`
	lines := typeScriptTestLines(source)
	clean := newTypeScriptLanguage("typescript", false).analysisForSource(source, len(lines))
	paddedSource := source + strings.Repeat(" ", javascriptMaximumConcreteParseBytes+1)
	padded := newTypeScriptLanguage("typescript", false).analysisForSource(
		paddedSource, len(lines),
	)
	if padded.tree != nil {
		t.Fatal("padded member-boundary fixture unexpectedly retained a concrete tree")
	}
	if got, want := padded.definitions, clean.definitions; !slices.Equal(got, want) {
		t.Fatalf("padded definitions = %#v, want concrete %#v", got, want)
	}
	if !slices.Equal(padded.imports, clean.imports) {
		t.Fatalf("padded imports = %#v, want concrete %#v", padded.imports, clean.imports)
	}
}

func TestTypeScriptMalformedInterfaceRecoveryRejectsPhantomKeywords(t *testing.T) {
	const source = `interface Good { ok: string }
interface Broken { field: string
const recovered = () => 1;
type Also = { value: number };
import type { X } from "x";
/* unterminated`
	analysis := newTypeScriptLanguage("typescript", false).analysisForSource(source, 6)
	if len(analysis.recoverySpans) == 0 {
		t.Fatal("malformed fixture did not exercise concrete recovery")
	}

	got := typeScriptDefinitionSymbols(analysis.definitions)
	want := []string{"Good", "ok", "Broken", "field", "recovered", "Also", "value"}
	if !slices.Equal(got, want) {
		t.Errorf("recovered definitions = %#v, want %#v", got, want)
	}
	for _, phantom := range []string{"const", "type", "interface", "import"} {
		if slices.Contains(got, phantom) {
			t.Errorf("malformed interface produced phantom definition %q: %#v", phantom, got)
		}
	}
	if wantImports := []javascriptLineSpan{{start: 5, end: 5}}; !slices.Equal(
		analysis.imports, wantImports,
	) {
		t.Errorf("recovered imports = %#v, want %#v", analysis.imports, wantImports)
	}
}

func TestTypeScriptTypeLevelArrowDoesNotDeferImportsOrPromoteRuntimeRequires(t *testing.T) {
	const source = `type Handler = (
  value: import("handler-input").Input,
) => import("handler-output").Output;
const callback: Handler = () => require("runtime-deferred");
const eager: Handler = require("runtime-eager");`
	analysis := newTypeScriptLanguage("typescript", false).analysisForSource(source, 5)
	want := []javascriptLineSpan{
		{start: 2, end: 2},
		{start: 3, end: 3},
		{start: 5, end: 5},
	}
	if !slices.Equal(analysis.imports, want) {
		t.Fatalf("type-level arrow imports = %#v, want %#v", analysis.imports, want)
	}
}

func TestTSXOversizedFallbackDistinguishesGenericArrowsAndGenericJSX(t *testing.T) {
	const prefix = `const Generic = <T,>(value: T) => value;
const View = <T extends object,>(props: T) => (
  <Panel<Result<string>> title="hidden">
    hidden
    {render<Result<string>>(props)}
  </Panel>
);
const eager = require("eager");
`
	source := prefix + strings.Repeat(" ", javascriptMaximumConcreteParseBytes+1)
	lines := strings.Split(source, "\n")
	analysis := newTypeScriptLanguage("tsx", true).analysisForSource(source, len(lines))
	if analysis.tree != nil {
		t.Fatal("oversized TSX fixture unexpectedly used concrete syntax tree")
	}

	wantDefinitions := []string{"Generic", "View", "eager"}
	if got := typeScriptDefinitionSymbols(analysis.definitions); !slices.Equal(got, wantDefinitions) {
		t.Errorf("TSX fallback definitions = %#v, want %#v", got, wantDefinitions)
	}
	if wantImports := []javascriptLineSpan{{start: 8, end: 8}}; !slices.Equal(
		analysis.imports, wantImports,
	) {
		t.Errorf("TSX fallback imports = %#v, want %#v", analysis.imports, wantImports)
	}

	masked := strings.Split(maskJavaScriptSource(source, analysis.opaqueSpans), "\n")
	if strings.Contains(masked[2], "hidden") || strings.Contains(masked[3], "hidden") {
		t.Errorf("generic JSX literal content remained searchable: %#v", masked[2:4])
	}
	if !strings.Contains(masked[4], "render<Result<string>>(props)") {
		t.Errorf("generic JSX expression was masked: %q", masked[4])
	}
}

func TestTypeScriptFallbackRejectsControlConditionsAsMethods(t *testing.T) {
	tests := []struct {
		parameters string
		want       bool
	}{
		{parameters: "value", want: true},
		{parameters: "value: Result, options?: Options", want: true},
		{parameters: "value = make()", want: true},
		{parameters: "!res.ok"},
		{parameters: "res.ok"},
		{parameters: "res.status === 409"},
		{parameters: `payload != null && typeof payload === "object"`},
		{parameters: `workflowJSONContentType(res.headers.get("Content-Type"))`},
	}
	for _, test := range tests {
		source := "class Container { if(" + test.parameters + ") {} }"
		fallback := scanJavaScriptFallbackFlavor(source, javascriptSyntaxFlavorTypeScript)
		tokens := tokenizeJavaScriptWithReplacements(
			source,
			fallback.comments,
			fallback.lexicalLiterals,
			fallback.lexicalReplacements,
			fallback.tokenCapacity,
		)
		delimiters := javascriptMatchDelimiters(tokens)
		if got := javascriptLexPlausibleMethodParameters(tokens, 3, delimiters); got != test.want {
			t.Fatalf("parameters %q plausible = %v, want %v; tokens=%#v",
				test.parameters, got, test.want, tokens)
		}
	}
}

func TestTypeScriptRecoveryWindowSelectionReservesTailRescues(t *testing.T) {
	broad := make([]typeScriptRecoveryTokenWindow, 10)
	rescue := make([]typeScriptRecoveryTokenWindow, 10)
	for index := range 10 {
		broad[index] = typeScriptRecoveryTokenWindow{start: index, end: index}
		rescue[index] = typeScriptRecoveryTokenWindow{
			start:  100 + index,
			end:    100 + index,
			rescue: true,
		}
	}
	got := typeScriptSelectRecoveryTokenWindows(broad, rescue, 4)
	want := []typeScriptRecoveryTokenWindow{
		{start: 0, end: 0},
		{start: 9, end: 9},
		{start: 100, end: 100, rescue: true},
		{start: 109, end: 109, rescue: true},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("selected recovery windows = %#v, want %#v", got, want)
	}
}

func TestTypeScriptLexicalFallbackPreservesTypedMultiDeclarators(t *testing.T) {
	const source = `const first: number = 1, second: Pair<Key, Value> = make(), third = 3;`
	fallback := scanJavaScriptFallbackFlavor(source, javascriptSyntaxFlavorTypeScript)
	tokens := tokenizeJavaScriptWithReplacements(
		source,
		fallback.comments,
		fallback.lexicalLiterals,
		fallback.lexicalReplacements,
		fallback.tokenCapacity,
	)
	delimiters := javascriptMatchDelimiters(tokens)
	definitions := javascriptLexVariableDefinitions(
		tokens,
		0,
		delimiters,
		javascriptSourcePositions{source: source, lineStarts: javascriptLineStarts(source)},
		true,
	)
	got := make([]string, 0, len(definitions))
	for _, definition := range definitions {
		got = append(got, definition.definition.symbol)
	}
	if want := []string{"first", "second", "third"}; !slices.Equal(got, want) {
		t.Fatalf("typed multi-declarator definitions = %#v, want %#v", got, want)
	}
}

func TestTypeScriptRecoveryRejectsMultilineContextualMemberTypes(t *testing.T) {
	const source = `interface I {
  abstract:
    boolean;
}`
	analysis := newTypeScriptLanguage("typescript", false).analysisForSource(source, 4)
	if len(analysis.recoverySpans) == 0 || !analysis.recoveryLines[2] {
		t.Fatalf("contextual member fixture did not exercise line-two recovery: %#v",
			analysis.recoverySpans)
	}
	if got, want := typeScriptDefinitionSymbols(analysis.definitions),
		[]string{"I", "abstract"}; !slices.Equal(got, want) {
		t.Fatalf("multiline contextual definitions = %#v, want %#v", got, want)
	}
	abstract := typeScriptFirstDefinition(t, analysis.definitions, "abstract")
	if abstract.ownsScope || abstract.scopeStart != 2 || abstract.scopeEnd != 2 {
		t.Fatalf("recovered abstract member scope = %#v, want physical line", abstract)
	}
}
