package repoview

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestSwiftBackendContractRegistrationAndPublicIntegration(t *testing.T) {
	t.Parallel()

	backend := newSwiftLanguage()
	if backend.name() != "swift" {
		t.Fatalf("language name = %q, want swift", backend.name())
	}
	contracts := []struct {
		name        string
		implemented bool
	}{
		{name: "sourceBackendPreparer", implemented: swiftTestImplements[sourceBackendPreparer](backend)},
		{name: "findScopeResolverPreparer", implemented: swiftTestImplements[findScopeResolverPreparer](backend)},
		{name: "linePreservingSourceCleaner", implemented: swiftTestImplements[linePreservingSourceCleaner](backend)},
		{name: "navigationScopeResolver", implemented: swiftTestImplements[navigationScopeResolver](backend)},
		{name: "sourceScopeNameResolver", implemented: swiftTestImplements[sourceScopeNameResolver](backend)},
		{name: "symbolOccurrenceCounter", implemented: swiftTestImplements[symbolOccurrenceCounter](backend)},
		{name: "sourceSymbolOccurrenceAugmenter", implemented: swiftTestImplements[sourceSymbolOccurrenceAugmenter](backend)},
		{name: "authoritativeSymbolOnLineResolver", implemented: swiftTestImplements[authoritativeSymbolOnLineResolver](backend)},
	}
	for _, contract := range contracts {
		if !contract.implemented {
			t.Errorf("Swift backend does not implement %s", contract.name)
		}
	}

	registered := languageForExtension(".swift")
	if registered.name() != "swift" {
		t.Fatalf("registered .swift language = %q, want swift", registered.name())
	}
	if _, generic := registered.(braceLanguage); generic {
		t.Fatal("registered .swift still uses generic braceLanguage")
	}
	if !defaultExtensions()[".swift"] {
		t.Fatal(".swift is absent from default source discovery")
	}

	const source = `import Foundation

final class Service {
    let value = 1

    func run() {
        Target()
    }
}
`
	root := t.TempDir()
	writeFile(t, root, "Service.swift", source)
	view := mustView(t, root)

	outline, err := view.Outline("Service.swift", Options{Return: ReturnLocations})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := swiftTestResultSymbols(outline.Results),
		[]string{"Service", "value", "run"}; !slices.Equal(got, want) {
		t.Fatalf("Swift outline symbols = %#v, want %#v", got, want)
	}
	for _, result := range outline.Results {
		if result.Kind != "def" || result.Language != "swift" || result.Path != "Service.swift" {
			t.Errorf("malformed Swift outline result: %#v", result)
		}
	}

	found, err := view.Find("Target", Options{
		Include: IncludeRefs,
		Return:  ReturnScope,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found.Results) != 1 || found.Results[0].Language != "swift" ||
		found.Results[0].Scope != "run" || found.Results[0].StartLine != 6 ||
		found.Results[0].EndLine != 8 {
		t.Fatalf("Target reference scope = %#v, want run at 6-8", found.Results)
	}

	inspected, err := view.Inspect(
		"Service.swift:7",
		Options{Include: IncludeScope, Return: ReturnScope},
	)
	if err != nil {
		t.Fatal(err)
	}
	if inspected.Symbol != "Target" || len(inspected.Results) != 1 ||
		inspected.Results[0].Scope != "run" || inspected.Results[0].StartLine != 6 ||
		inspected.Results[0].EndLine != 8 {
		t.Fatalf("Inspect Target = %#v, want Target in run at 6-8", inspected)
	}
}

func TestSwiftDefinitionSymbolRecognizesDeclarationsAndRejectsExpressions(t *testing.T) {
	t.Parallel()

	backend := newSwiftLanguage()
	tests := []struct {
		name string
		line string
		want string
		ok   bool
	}{
		{name: "class", line: `public final class Service<T>: Worker {`, want: "Service", ok: true},
		{name: "actor", line: `distributed actor Store {`, want: "Store", ok: true},
		{name: "protocol", line: `protocol Worker: Sendable {`, want: "Worker", ok: true},
		{name: "structure", line: `private struct Entry: Codable {`, want: "Entry", ok: true},
		{name: "enumeration", line: `indirect enum Tree<T> {`, want: "Tree", ok: true},
		{name: "type alias", line: `typealias Handler<T> = @Sendable (T) -> Void`, want: "Handler", ok: true},
		{name: "function", line: `nonisolated func load<T>(_ value: T) async throws -> T {`, want: "load", ok: true},
		{name: "property", line: `private(set) var value: Int = 0`, want: "value", ok: true},
		{name: "multiple bindings", line: `let first = 1, second = 2`, want: "first", ok: true},
		{name: "if", line: `if ready { Target() }`},
		{name: "guard", line: `guard let value else { return }`},
		{name: "for", line: `for item in values { Target(item) }`},
		{name: "switch case", line: `case .failed(let error): Target(error)`},
		{name: "call", line: `Target()`},
		{name: "qualified call", line: `service.Client.render<Result>()`},
		{name: "attribute", line: `@MainActor(unsafe)`},
		{name: "comment", line: `// func Hidden() {}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, ok := backend.definitionSymbol(test.line)
			if got != test.want || ok != test.ok {
				t.Fatalf("definitionSymbol(%q) = %q, %v; want %q, %v",
					test.line, got, ok, test.want, test.ok)
			}
		})
	}
}

func TestSwiftDefinitionsCoverNamedDeclarationsAndExcludeBindings(t *testing.T) {
	t.Parallel()

	const source = `/// A worker contract.
protocol Worker {
    associatedtype Output
    var output: Output { get }
    func work(_ input: Output)
}

@MainActor
final class Service<T>: Worker where T: Sendable {
    @Wrapper var value: T
    weak var delegate: Delegate?
    let first = 1, second = 2

    required init(_ value: T) {
        self.value = value
    }

    deinit {
        cleanup()
    }

    subscript<U>(_ key: U) -> T {
        get { value }
        set { value = newValue }
    }

    func fetch<each U>(_ values: repeat each U) async throws -> T {
        func localHelper() {}
        struct LocalType {}
        let localValue = values
        return value
    }

    enum State {
        case idle
        case failed(code: Int), waiting
    }

    struct Nested {}
}

extension Service {
    func extended() {}
}

typealias Handler<T> = @Sendable (T) async -> Void
`
	lines := swiftTestLines(source)
	definitions := newSwiftLanguage().sourceDefinitions(lines)
	want := []string{
		"Worker", "Output", "output", "work", "Service", "value", "delegate",
		"first", "second", "init", "deinit", "subscript", "fetch", "localHelper",
		"LocalType", "State", "idle", "failed", "waiting", "Nested", "Service",
		"extended", "Handler",
	}
	if got := swiftTestDefinitionSymbols(definitions); !slices.Equal(got, want) {
		t.Fatalf("Swift definitions =\n%#v\nwant\n%#v", got, want)
	}
	for _, forbidden := range []string{
		"T", "U", "input", "key", "values", "localValue", "code", "get", "set",
		"newValue", "cleanup", "Wrapper", "MainActor",
	} {
		if slices.Contains(swiftTestDefinitionSymbols(definitions), forbidden) {
			t.Errorf("non-outline binding or call %q became a definition: %#v",
				forbidden, definitions)
		}
	}
	swiftTestAssertDefinitionCoordinates(t, lines, definitions)

	for _, symbol := range []string{
		"Worker", "output", "work", "Service", "init", "deinit", "subscript",
		"fetch", "localHelper", "LocalType", "State", "Nested", "extended",
	} {
		if !swiftTestHasOwningDefinition(definitions, symbol) {
			t.Errorf("definition %q has no owning declaration: %#v", symbol, definitions)
		}
	}
	for _, symbol := range []string{
		"Output", "value", "delegate", "first", "second", "idle", "failed", "waiting",
	} {
		definition := swiftTestFirstDefinition(t, definitions, symbol)
		if definition.ownsScope || definition.scopeStart != definition.line ||
			definition.scopeEnd != definition.line {
			t.Errorf("non-owning definition %q has scope %#v", symbol, definition)
		}
	}
}

func TestSwiftImportsCoverAttributesAccessControlAndConditionalBranches(t *testing.T) {
	t.Parallel()

	const source = `#!/usr/bin/env swift
@preconcurrency import Foundation
@testable import DemoSupport
public import SwiftUI
package import struct Utilities.Identifier
#if canImport(Observation)
import Observation
#elseif canImport(Combine)
import Combine
#endif

let text = "import Hidden"
// import CommentOnly
func importThing() {}
`
	lines := swiftTestLines(source)
	backend := newSwiftLanguage()
	start, end, ok := backend.importRange(lines)
	if !ok || start != 2 || end != 9 {
		t.Fatalf("Swift import range = %d-%d, %v; want 2-9, true", start, end, ok)
	}

	root := t.TempDir()
	writeFile(t, root, "Imports.swift", source)
	response, err := mustView(t, root).Inspect(
		"Imports.swift:13",
		Options{Include: IncludeImports, Return: ReturnScope},
	)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, result := range response.Results {
		if result.Kind != "imports" {
			continue
		}
		found = true
		if result.Language != "swift" || result.StartLine != 2 || result.EndLine != 9 ||
			result.Code != strings.Join(lines[1:9], "\n") {
			t.Errorf("Swift import result = %#v, want exact lines 2-9", result)
		}
	}
	if !found {
		t.Fatalf("Inspect result has no imports entry: %#v", response.Results)
	}
}

func TestSwiftSmallestScopesAndNamedNavigationScopes(t *testing.T) {
	t.Parallel()

	const source = `final class Service {
    var value: Int {
        get {
            if ready {
                return Target()
            }
            return 0
        }
    }

    func run() {
        values.forEach { value in
            switch value {
            case .ready:
                Target()
            default:
                break
            }
        }
    }
}
`
	lines := swiftTestLines(source)
	backend := prepareLanguageBackend(newSwiftLanguage(), lines)
	if start, end := backend.enclosingScope(lines, 5); start != 4 || end != 6 {
		t.Fatalf("smallest getter if scope = %d-%d, want 4-6", start, end)
	}
	resolver := backend.(navigationScopeResolver)
	if start, end := resolver.navigationScope(lines, 5); start != 2 || end != 9 {
		t.Fatalf("property navigation scope = %d-%d, want 2-9", start, end)
	}
	if got := scopeName(lines, 5, backend); got != "value" {
		t.Fatalf("getter scope name = %q, want value", got)
	}
	if start, end := backend.enclosingScope(lines, 15); start != 14 || end != 16 {
		t.Fatalf("switch case scope = %d-%d, want 14-16", start, end)
	}
	if start, end := resolver.navigationScope(lines, 15); start != 11 || end != 20 {
		t.Fatalf("run navigation scope = %d-%d, want 11-20", start, end)
	}
	if got := scopeName(lines, 15, backend); got != "run" {
		t.Fatalf("closure/switch scope name = %q, want run", got)
	}
}

func swiftTestLines(source string) []string {
	return strings.Split(strings.TrimSuffix(source, "\n"), "\n")
}

func swiftTestDefinitionSymbols(definitions []sourceDefinition) []string {
	symbols := make([]string, len(definitions))
	for index, definition := range definitions {
		symbols[index] = definition.symbol
	}
	return symbols
}

func swiftTestResultSymbols(results []Result) []string {
	symbols := make([]string, len(results))
	for index, result := range results {
		symbols[index] = result.Symbol
	}
	return symbols
}

func swiftTestResultKinds(results []Result) []string {
	kinds := make([]string, len(results))
	for index, result := range results {
		kinds[index] = result.Kind
	}
	return kinds
}

func swiftTestResultLines(results []Result) []int {
	lines := make([]int, len(results))
	for index, result := range results {
		lines[index] = result.Line
	}
	return lines
}

func swiftTestFirstDefinition(
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
	t.Fatalf("missing Swift definition %q in %#v", symbol, definitions)
	return sourceDefinition{}
}

func swiftTestHasOwningDefinition(definitions []sourceDefinition, symbol string) bool {
	for _, definition := range definitions {
		if definition.symbol == symbol && definition.ownsScope {
			return true
		}
	}
	return false
}

func swiftTestLineContaining(t *testing.T, lines []string, marker string) int {
	t.Helper()
	for index, line := range lines {
		if strings.Contains(line, marker) {
			return index + 1
		}
	}
	t.Fatalf("marker %q is absent from source", marker)
	return 0
}

func swiftTestAssertDefinitionCoordinates(
	t *testing.T,
	lines []string,
	definitions []sourceDefinition,
) {
	t.Helper()
	for _, definition := range definitions {
		if definition.symbol == "" || definition.line < 1 || definition.line > len(lines) ||
			definition.column < 1 || definition.scopeStart < 1 ||
			definition.scopeStart > definition.line || definition.scopeEnd < definition.line ||
			definition.scopeEnd > len(lines) {
			t.Fatalf("invalid Swift definition coordinates: %#v (lines=%d)",
				definition, len(lines))
		}
		line := lines[definition.line-1]
		if definition.column > len(line) ||
			!strings.HasPrefix(line[definition.column-1:], definition.symbol) {
			t.Fatalf("Swift definition is not source-backed: %#v in %q", definition, line)
		}
	}
}

func swiftTestAssertLineWidths(t *testing.T, original, masked []string) {
	t.Helper()
	if len(masked) != len(original) {
		t.Fatalf("masked lines = %d, want %d", len(masked), len(original))
	}
	for index := range original {
		if len(masked[index]) != len(original[index]) {
			t.Fatalf("masked line %d width = %d, want %d: %q",
				index+1, len(masked[index]), len(original[index]), masked[index])
		}
	}
}

func swiftTestImplements[Contract any](value any) bool {
	_, ok := value.(Contract)
	return ok
}

func swiftTestAssertResultShape(t *testing.T, got []Result, wantLines []int, wantKinds []string) {
	t.Helper()
	if !reflect.DeepEqual(swiftTestResultLines(got), wantLines) ||
		!reflect.DeepEqual(swiftTestResultKinds(got), wantKinds) {
		t.Fatalf("results = lines %#v kinds %#v; want lines %#v kinds %#v",
			swiftTestResultLines(got), swiftTestResultKinds(got), wantLines, wantKinds)
	}
}
