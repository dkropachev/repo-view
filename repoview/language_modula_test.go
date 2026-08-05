package repoview

import (
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestModulaBackendContractRegistrationAndGoModGate(t *testing.T) {
	t.Parallel()

	backend := newModulaLanguage()
	if backend.name() != "mod" {
		t.Fatalf("language name = %q, want mod", backend.name())
	}
	contracts := []struct {
		name        string
		implemented bool
	}{
		{name: "sourceBackendPreparer", implemented: modulaTestImplements[sourceBackendPreparer](backend)},
		{name: "findScopeResolverPreparer", implemented: modulaTestImplements[findScopeResolverPreparer](backend)},
		{name: "linePreservingSourceCleaner", implemented: modulaTestImplements[linePreservingSourceCleaner](backend)},
		{name: "navigationScopeResolver", implemented: modulaTestImplements[navigationScopeResolver](backend)},
		{name: "sourceScopeNameResolver", implemented: modulaTestImplements[sourceScopeNameResolver](backend)},
		{name: "symbolOccurrenceCounter", implemented: modulaTestImplements[symbolOccurrenceCounter](backend)},
		{name: "sourceSymbolOccurrenceAugmenter", implemented: modulaTestImplements[sourceSymbolOccurrenceAugmenter](backend)},
		{name: "authoritativeSymbolOnLineResolver", implemented: modulaTestImplements[authoritativeSymbolOnLineResolver](backend)},
	}
	for _, contract := range contracts {
		if !contract.implemented {
			t.Errorf("Modula-2 backend does not implement %s", contract.name)
		}
	}

	for _, extension := range []string{".mod", ".def"} {
		registered := languageForExtension(extension)
		if registered.name() != "mod" {
			t.Errorf("registered %s language = %q, want mod", extension, registered.name())
		}
		if _, generic := registered.(braceLanguage); generic {
			t.Errorf("registered %s still uses generic braceLanguage", extension)
		}
		if !defaultExtensions()[extension] {
			t.Errorf("%s is absent from default source discovery", extension)
		}
	}

	const goMod = `module example.com/application

go 1.26

require example.com/dependency v1.2.3 // indirect

replace example.com/dependency => ../dependency
`
	root := t.TempDir()
	writeFile(t, root, "go.mod", goMod)
	view := mustView(t, root)

	outline, err := view.Outline("go.mod", Options{Return: ReturnLocations})
	if err != nil {
		t.Fatal(err)
	}
	if len(outline.Results) != 0 {
		t.Fatalf("go.mod outline = %#v, want no Modula-2 definitions", outline.Results)
	}

	found, err := view.Find("example.com/dependency", Options{
		Include: IncludeRefs,
		Return:  ReturnLine,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := modulaTestResultLines(found.Results); !slices.Equal(got, []int{5, 7}) {
		t.Fatalf("go.mod dependency search lines = %#v, want [5 7]", got)
	}
	for _, result := range found.Results {
		if result.Path != "go.mod" || result.Language != "mod" || result.Kind != "ref" {
			t.Errorf("malformed go.mod search result: %#v", result)
		}
	}

	for _, inspectedLine := range []struct {
		line int
		want string
	}{
		{line: 1, want: "example.com/application"},
		{line: 5, want: "example.com/dependency"},
	} {
		inspected, inspectErr := view.Inspect(
			"go.mod:"+strconv.Itoa(inspectedLine.line),
			Options{Include: IncludeScope, Return: ReturnLine, NoComments: true},
		)
		if inspectErr != nil {
			t.Fatal(inspectErr)
		}
		if inspected.Symbol != inspectedLine.want || len(inspected.Results) != 1 ||
			inspected.Results[0].Line != inspectedLine.line ||
			!strings.Contains(inspected.Results[0].Code, inspectedLine.want) {
			t.Errorf("Inspect go.mod:%d = %#v, want symbol %q and original line",
				inspectedLine.line, inspected, inspectedLine.want)
		}
	}

	noComments, err := view.Find("example.com/dependency", Options{
		Include:    IncludeRefs,
		Return:     ReturnLine,
		NoComments: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := modulaTestResultLines(noComments.Results); !slices.Equal(got, []int{5, 7}) {
		t.Fatalf("NoComments dependency lines = %#v, want [5 7]", got)
	}
	commentOnly, err := view.Find("indirect", Options{
		Include:    IncludeRefs,
		Return:     ReturnLocations,
		NoComments: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(commentOnly.Results) != 0 {
		t.Fatalf("go.mod // suffix survived NoComments: %#v", commentOnly.Results)
	}

	if symbol, ok := backend.definitionSymbol("module example.com/application"); ok || symbol != "" {
		t.Fatalf("lowercase go.mod module became definition %q, %v", symbol, ok)
	}
}

func TestModulaNonCompilationUnitsAreInertButSearchable(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		path   string
		source string
		query  string
		line   int
	}{
		{
			name: "definition extension prose",
			path: "notes.def",
			source: `This is not a Modula-2 definition module.
PROCEDURE Phantom;
Needle remains searchable.
`,
			query: "Needle",
			line:  3,
		},
		{
			name: "lowercase module-like file",
			path: "lower.mod",
			source: `module lowercase;
CONST Phantom = 1;
Needle
`,
			query: "Needle",
			line:  3,
		},
		{
			name: "leading declaration without unit",
			path: "fragment.mod",
			source: `PROCEDURE Phantom;
Needle
`,
			query: "Needle",
			line:  2,
		},
		{
			name: "program prefix without header semicolon",
			path: "plausible.mod",
			source: `MODULE Foo prose PROCEDURE Phantom;
Needle
`,
			query: "Needle",
			line:  2,
		},
		{
			name: "definition prefix without header semicolon",
			path: "plausible.def",
			source: `DEFINITION MODULE Foo prose TYPE Phantom = INTEGER;
Needle
`,
			query: "Needle",
			line:  2,
		},
		{
			name: "reserved module name",
			path: "reserved.mod",
			source: `MODULE __FILE__;
PROCEDURE Phantom;
Needle
`,
			query: "Needle",
			line:  3,
		},
		{
			name: "non ASCII module name",
			path: "unicode.mod",
			source: `MODULE Módulo;
PROCEDURE Phantom;
Needle
`,
			query: "Needle",
			line:  3,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeFile(t, root, test.path, test.source)
			view := mustView(t, root)
			outline, err := view.Outline(test.path, Options{Return: ReturnLocations})
			if err != nil {
				t.Fatal(err)
			}
			if len(outline.Results) != 0 {
				t.Fatalf("inert source outline = %#v, want none", outline.Results)
			}
			found, err := view.Find(test.query, Options{
				Include: IncludeRefs,
				Return:  ReturnLocations,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(found.Results) != 1 || found.Results[0].Line != test.line ||
				found.Results[0].Path != test.path {
				t.Fatalf("inert source search = %#v, want %s:%d", found.Results, test.path, test.line)
			}
		})
	}
}

func TestModulaDefinitionSymbolRecognizesDeclarationsAndRejectsUses(t *testing.T) {
	t.Parallel()

	backend := newModulaLanguage()
	tests := []struct {
		name string
		line string
		want string
		ok   bool
	}{
		{name: "program module", line: "MODULE Catalogue;", want: "Catalogue", ok: true},
		{name: "definition module", line: "DEFINITION MODULE Catalogue;", want: "Catalogue", ok: true},
		{name: "definition module for", line: `DEFINITION MODULE FOR "POSIX" Catalogue;`, want: "Catalogue", ok: true},
		{name: "definition module for file macro", line: "DEFINITION MODULE FOR __FILE__ Catalogue;", want: "Catalogue", ok: true},
		{name: "definition module for date macro", line: "DEFINITION MODULE FOR __DATE__ Catalogue;", want: "Catalogue", ok: true},
		{name: "definition module for function macro", line: "DEFINITION MODULE FOR __FUNCTION__ Catalogue;", want: "Catalogue", ok: true},
		{name: "definition module for integer macro", line: "DEFINITION MODULE FOR __LINE__ Catalogue;"},
		{name: "implementation module", line: "IMPLEMENTATION MODULE Catalogue;", want: "Catalogue", ok: true},
		{name: "implementation module rejects for literal", line: `IMPLEMENTATION MODULE FOR "POSIX" Catalogue;`},
		{name: "implementation module rejects for macro", line: "IMPLEMENTATION MODULE FOR __FILE__ Catalogue;"},
		{name: "constant", line: "CONST Answer = 42;", want: "Answer", ok: true},
		{name: "type", line: "TYPE Index = [0..10];", want: "Index", ok: true},
		{name: "opaque type", line: "TYPE Handle;", want: "Handle", ok: true},
		{name: "variable", line: "VAR first, second: INTEGER;", want: "first", ok: true},
		{name: "procedure", line: "PROCEDURE Open(VAR handle: Handle);", want: "Open", ok: true},
		{name: "bare section constant", line: "Answer = 42;", want: "Answer", ok: true},
		{name: "bare section variable", line: "first, second: INTEGER;", want: "first", ok: true},
		{name: "import", line: "IMPORT IO, Math;"},
		{name: "from import", line: "FROM Storage IMPORT ALLOCATE;"},
		{name: "procedure call", line: "Open(handle);"},
		{name: "assignment", line: "Answer := Compute();"},
		{name: "closing procedure name", line: "END Open;"},
		{name: "closing module name", line: "END Catalogue."},
		{name: "control end", line: "END;"},
		{name: "comment", line: "(* PROCEDURE Hidden; *)"},
		{name: "string", line: `text := "PROCEDURE Hidden;";`},
		{name: "lowercase module", line: "module go.mod"},
		{name: "lowercase declaration", line: "procedure hidden;"},
		{name: "reserved module name", line: "MODULE __FILE__;"},
		{name: "reserved constant name", line: "CONST __BUILTIN__ = 1;"},
		{name: "reserved procedure name", line: "PROCEDURE __LINE__;"},
		{name: "non ASCII module name", line: "MODULE Módulo;"},
		{name: "non ASCII constant name", line: "CONST Δ = 1;"},
		{name: "CPP line marker", line: `# 1 "source.mod"`},
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

func TestModulaDefinitionsCoverDeclarationsAndExcludeTypeAndUseNames(t *testing.T) {
	t.Parallel()

	const source = `MODULE Catalogue;
IMPORT IO, Math;
FROM Storage IMPORT ALLOCATE, DEALLOCATE;

CONST
  Answer = 42;
  Greeting = "hello";
TYPE
  Color = (red, green, blue);
  Index = [0..10];
  ItemPtr = POINTER TO Item;
  Item = RECORD
    code: INTEGER;
    CASE kind: Color OF
      red: redValue: INTEGER |
      green, blue: otherValue: CARDINAL
    END
  END;
  Handler = PROCEDURE (INTEGER, VAR CARDINAL): BOOLEAN;
VAR
  first, second: INTEGER;

PROCEDURE DeclaredLater(value: INTEGER); FORWARD;

MODULE Local;
EXPORT QUALIFIED LocalValue;
CONST LocalValue = 1;
PROCEDURE LocalWork;
BEGIN
END LocalWork;
BEGIN
END Local;

PROCEDURE DeclaredLater(value: INTEGER);
VAR scratch: INTEGER;
BEGIN
  IF value > 0 THEN
    scratch := value
  END
END DeclaredLater;

BEGIN
  DeclaredLater(Answer)
END Catalogue.
`
	lines := modulaTestLines(source)
	definitions := newModulaLanguage().sourceDefinitions(lines)
	want := []string{
		"Catalogue", "Answer", "Greeting", "Color", "red", "green", "blue",
		"Index", "ItemPtr", "Item", "code", "kind", "redValue", "otherValue",
		"Handler", "first", "second", "DeclaredLater", "Local", "LocalValue",
		"LocalWork", "DeclaredLater", "scratch",
	}
	if got := modulaTestDefinitionSymbols(definitions); !slices.Equal(got, want) {
		t.Fatalf("Modula-2 definitions =\n%#v\nwant\n%#v", got, want)
	}
	for _, forbidden := range []string{
		"IO", "Math", "Storage", "ALLOCATE", "DEALLOCATE", "INTEGER", "CARDINAL",
		"BOOLEAN", "value", "Open", "Compute",
	} {
		if slices.Contains(modulaTestDefinitionSymbols(definitions), forbidden) {
			t.Errorf("non-declaration %q became a definition: %#v", forbidden, definitions)
		}
	}
	modulaTestAssertDefinitionCoordinates(t, lines, definitions)

	for _, symbol := range []string{"Catalogue", "Item", "Local", "LocalWork"} {
		if !modulaTestHasOwningDefinition(definitions, symbol) {
			t.Errorf("definition %q has no owning declaration: %#v", symbol, definitions)
		}
	}
	if got := modulaTestOwningDefinitionCount(definitions, "DeclaredLater"); got != 1 {
		t.Errorf("DeclaredLater owning definitions = %d, want body only", got)
	}
	for _, symbol := range []string{
		"Answer", "Greeting", "red", "green", "blue", "Index", "ItemPtr", "code",
		"kind", "redValue", "otherValue", "first", "second", "LocalValue",
		"Handler", "scratch",
	} {
		definition := modulaTestFirstDefinition(t, definitions, symbol)
		if definition.ownsScope || definition.scopeStart != definition.line ||
			definition.scopeEnd != definition.line {
			t.Errorf("non-owning definition %q has scope %#v", symbol, definition)
		}
	}
}

func TestModulaDefinitionAndImplementationUnits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		source     string
		want       []string
		owning     []string
		nonOwning  []string
		forbidden  []string
		wantImport cLineSpan
	}{
		{
			name: "definition",
			source: `DEFINITION MODULE Handles;
FROM SYSTEM IMPORT ADDRESS;
CONST NilValue = 0;
TYPE
  Handle;
  Mode = (read, write);
VAR DefaultHandle: Handle;
PROCEDURE Open(name: ARRAY OF CHAR; mode: Mode): Handle;
PROCEDURE Close(VAR handle: Handle);
END Handles.
`,
			want:       []string{"Handles", "NilValue", "Handle", "Mode", "read", "write", "DefaultHandle", "Open", "Close"},
			owning:     []string{"Handles", "Open", "Close"},
			nonOwning:  []string{"NilValue", "Handle", "Mode", "read", "write", "DefaultHandle"},
			forbidden:  []string{"SYSTEM", "ADDRESS", "name", "mode", "handle", "CHAR"},
			wantImport: cLineSpan{start: 2, end: 2},
		},
		{
			name: "implementation",
			source: `IMPLEMENTATION MODULE Handles;
FROM Storage IMPORT ALLOCATE;
TYPE Handle = POINTER TO Descriptor;
     Descriptor = RECORD value: INTEGER END;
PROCEDURE Open(name: ARRAY OF CHAR; mode: INTEGER): Handle;
VAR result: Handle;
BEGIN
  ALLOCATE(result, SIZE(Descriptor));
  RETURN result
END Open;
PROCEDURE Close(VAR handle: Handle);
BEGIN
END Close;
BEGIN
END Handles.
`,
			want:       []string{"Handles", "Handle", "Descriptor", "value", "Open", "result", "Close"},
			owning:     []string{"Handles", "Descriptor", "Open", "Close"},
			nonOwning:  []string{"Handle", "value", "result"},
			forbidden:  []string{"Storage", "ALLOCATE", "name", "mode", "handle", "SIZE"},
			wantImport: cLineSpan{start: 2, end: 2},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			lines := modulaTestLines(test.source)
			backend := prepareLanguageBackend(newModulaLanguage(), lines)
			definitions := backend.sourceDefinitions(lines)
			if got := modulaTestDefinitionSymbols(definitions); !slices.Equal(got, test.want) {
				t.Fatalf("definitions = %#v, want %#v", got, test.want)
			}
			for _, forbidden := range test.forbidden {
				if slices.Contains(modulaTestDefinitionSymbols(definitions), forbidden) {
					t.Errorf("non-declaration %q became definition: %#v", forbidden, definitions)
				}
			}
			for _, owning := range test.owning {
				if !modulaTestHasOwningDefinition(definitions, owning) {
					t.Errorf("%q has no owning definition: %#v", owning, definitions)
				}
			}
			for _, nonOwning := range test.nonOwning {
				definition := modulaTestFirstDefinition(t, definitions, nonOwning)
				if definition.ownsScope || definition.scopeStart != definition.line ||
					definition.scopeEnd != definition.line {
					t.Errorf("non-owning declaration %q has scope %#v", nonOwning, definition)
				}
			}
			modulaTestAssertDefinitionCoordinates(t, lines, definitions)
			start, end, ok := backend.importRange(lines)
			if !ok || start != test.wantImport.start || end != test.wantImport.end {
				t.Errorf("imports = %d-%d, %v; want %d-%d, true",
					start, end, ok, test.wantImport.start, test.wantImport.end)
			}
		})
	}
}

func TestModulaImportsAndNamedNavigationScopes(t *testing.T) {
	t.Parallel()

	const source = `MODULE Control;
IMPORT IO, Math;
(* imports may be separated by nested comments (* safely *) *)
FROM Storage IMPORT ALLOCATE, DEALLOCATE;
(* CHECKED *)
FROM SYSTEM IMPORT ADDRESS;

PROCEDURE Run(limit: INTEGER);
VAR index: INTEGER;
BEGIN
  IF limit > 0 THEN
    WHILE index < limit DO
      CASE index OF
        0: IO.WriteString("zero") |
        1..10: IO.WriteString("small")
      ELSE
        IO.WriteString("large")
      END
    END
  END;
  REPEAT
    index := index - 1
  UNTIL index = 0;
  LOOP
    EXIT
  END
END Run;

BEGIN
  Run(10)
END Control.
`
	lines := modulaTestLines(source)
	backend := prepareLanguageBackend(newModulaLanguage(), lines)
	if start, end, ok := backend.importRange(lines); !ok || start != 2 || end != 6 {
		t.Fatalf("import range = %d-%d, %v; want 2-6, true", start, end, ok)
	}

	caseBody := modulaTestLineContaining(t, lines, `IO.WriteString("small")`)
	caseStart := modulaTestLineContaining(t, lines, "CASE index OF")
	caseEnd := modulaTestLineAfter(t, lines, caseStart, "END")
	if start, end := backend.enclosingScope(lines, caseBody); start != caseStart || end != caseEnd {
		t.Fatalf("CASE scope = %d-%d, want %d-%d", start, end, caseStart, caseEnd)
	}

	repeatBody := modulaTestLineContaining(t, lines, "index := index - 1")
	repeatStart := modulaTestLineContaining(t, lines, "REPEAT")
	repeatEnd := modulaTestLineContaining(t, lines, "UNTIL index = 0")
	if start, end := backend.enclosingScope(lines, repeatBody); start != repeatStart || end != repeatEnd {
		t.Fatalf("REPEAT scope = %d-%d, want %d-%d", start, end, repeatStart, repeatEnd)
	}

	runStart := modulaTestLineContaining(t, lines, "PROCEDURE Run")
	runEnd := modulaTestLineContaining(t, lines, "END Run")
	resolver := backend.(navigationScopeResolver)
	if start, end := resolver.navigationScope(lines, caseBody); start != runStart || end != runEnd {
		t.Fatalf("CASE navigation scope = %d-%d, want Run %d-%d", start, end, runStart, runEnd)
	}
	if got := scopeName(lines, caseBody, backend); got != "Run" {
		t.Fatalf("CASE named scope = %q, want Run", got)
	}

	root := t.TempDir()
	writeFile(t, root, "Control.mod", source)
	inspected, err := mustView(t, root).Inspect(
		"Control.mod:"+strconv.Itoa(caseBody),
		Options{Include: IncludeScope, Return: ReturnScope},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(inspected.Results) != 1 || inspected.Results[0].Scope != "Run" ||
		inspected.Results[0].StartLine != runStart || inspected.Results[0].EndLine != runEnd {
		t.Fatalf("Inspect nested control scope = %#v, want Run %d-%d",
			inspected.Results, runStart, runEnd)
	}
}

func TestModulaMultilineImportsExcludeProcedureBodyRecovery(t *testing.T) {
	t.Parallel()

	const source = `MODULE Imports;
IMPORT
  IO,
  Math;
FROM
  Storage
IMPORT
  ALLOCATE,
  DEALLOCATE;

PROCEDURE Run;
BEGIN
  IMPORT Hidden;
END Run;

BEGIN
END Imports.
`
	lines := modulaTestLines(source)
	backend := prepareLanguageBackend(newModulaLanguage(), lines)
	if start, end, ok := backend.importRange(lines); !ok || start != 2 || end != 9 {
		t.Fatalf("multiline import range = %d-%d, %v; want 2-9, true", start, end, ok)
	}
	definitions := backend.sourceDefinitions(lines)
	if got, want := modulaTestDefinitionSymbols(definitions), []string{"Imports", "Run"}; !slices.Equal(got, want) {
		t.Fatalf("multiline-import definitions = %#v, want %#v", got, want)
	}
	if slices.Contains(modulaTestDefinitionSymbols(definitions), "Hidden") {
		t.Fatalf("body-level import binding became definition: %#v", definitions)
	}

	tree := modulaTreeTestParseRecovery(t, source)
	if spans := modulaSyntaxErrorSpans(tree, len(source)); len(spans) == 0 {
		t.Fatal("illegal procedure-body IMPORT has no recovery evidence")
	}
	wantSpans := []cLineSpan{{start: 2, end: 4}, {start: 5, end: 9}}
	if got := modulaTreeImports(source, len(lines), tree); !reflect.DeepEqual(got, wantSpans) {
		t.Fatalf("concrete multiline imports = %#v, want %#v", got, wantSpans)
	}
}

func TestModulaNestedCommentsStringsAndDirectiveTokensAreClassifiedLexically(t *testing.T) {
	t.Parallel()

	const source = `MODULE Lexical;
(* outer comment
   PROCEDURE HiddenInComment;
   (* nested TYPE Phantom = INTEGER; *)
*)
<* ASSERT("PROCEDURE HiddenInDirectiveString;") *>
CONST DoubleQuoted = "PROCEDURE HiddenDouble;";
      SingleQuoted = 'TYPE HiddenSingle = INTEGER;';
PROCEDURE Visible;
BEGIN
  Target;
  Target("Target in string");
  (* Target in comment (* Target nested *) *)
END Visible;
BEGIN
END Lexical.
`
	lines := modulaTestLines(source)
	backend := prepareLanguageBackend(newModulaLanguage(), lines)
	definitions := backend.sourceDefinitions(lines)
	if got, want := modulaTestDefinitionSymbols(definitions),
		[]string{"Lexical", "DoubleQuoted", "SingleQuoted", "Visible"}; !slices.Equal(got, want) {
		t.Fatalf("lexical definitions = %#v, want %#v", got, want)
	}
	for _, hidden := range []string{"HiddenInComment", "Phantom", "HiddenInDirectiveString", "HiddenDouble", "HiddenSingle"} {
		if slices.Contains(modulaTestDefinitionSymbols(definitions), hidden) {
			t.Errorf("opaque spelling %q became definition: %#v", hidden, definitions)
		}
	}

	directiveLines := []string{
		`TYPE Aligned = INTEGER <* bytealignment("HiddenDirectiveString") *>;`,
	}
	commentsMasked := newModulaLanguage().searchLines(directiveLines, true, false)
	if !strings.Contains(commentsMasked[0], "bytealignment") {
		t.Fatal("legal type directive identifier was hidden as comment trivia")
	}
	if !strings.Contains(commentsMasked[0], "HiddenDirectiveString") {
		t.Fatal("directive string was hidden without NoStrings")
	}
	codeOnlyDirective := newModulaLanguage().searchLines(directiveLines, true, true)
	if !strings.Contains(codeOnlyDirective[0], "bytealignment") {
		t.Fatal("NoComments hid a legal type directive identifier")
	}
	if strings.Contains(codeOnlyDirective[0], "HiddenDirectiveString") {
		t.Fatal("NoStrings retained a string inside a directive")
	}

	masked := backend.searchLines(lines, true, true)
	modulaTestAssertLineWidths(t, lines, masked)
	for index, line := range masked {
		if strings.Contains(line, "Target in string") || strings.Contains(line, "Target in comment") ||
			strings.Contains(line, "Target nested") {
			t.Errorf("opaque Target survived on masked line %d: %q", index+1, line)
		}
	}
	if !strings.Contains(masked[modulaTestLineContaining(t, lines, "Target;")-1], "Target") {
		t.Fatal("real code reference was masked")
	}
	if strings.Contains(
		masked[modulaTestLineContaining(t, lines, "HiddenInDirectiveString")-1],
		"HiddenInDirectiveString",
	) {
		t.Fatal("NoStrings retained a string inside a directive")
	}

	root := t.TempDir()
	writeFile(t, root, "Lexical.mod", source)
	found, err := mustView(t, root).Find("Target", Options{
		Include:    IncludeRefs,
		Return:     ReturnLocations,
		NoComments: true,
		NoStrings:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := modulaTestResultLines(found.Results), []int{11, 12}; !slices.Equal(got, want) {
		t.Fatalf("code-only Target lines = %#v, want %#v", got, want)
	}
}

func TestModulaFindUsesCaseSensitiveIdentifierBoundariesAndQualifiedNames(t *testing.T) {
	t.Parallel()

	const source = `MODULE Symbols;
PROCEDURE Target;
BEGIN
END Target;
PROCEDURE Caller;
VAR TargetExtra: INTEGER;
BEGIN
  Target;
  Module.Target;
  target;
  TargetExtra := 1;
  (* Target *)
  Message := "Target"
END Caller;
BEGIN
END Symbols.
`
	root := t.TempDir()
	writeFile(t, root, "Symbols.mod", source)
	view := mustView(t, root)
	found, err := view.Find("Target", Options{
		Include:    IncludeBoth,
		Return:     ReturnLocations,
		NoComments: true,
		NoStrings:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := modulaTestResultLines(found.Results), []int{2, 4, 8, 9}; !slices.Equal(got, want) {
		t.Fatalf("case-sensitive Target lines = %#v, want %#v", got, want)
	}
	if got, want := []string{
		found.Results[0].Kind, found.Results[1].Kind,
		found.Results[2].Kind, found.Results[3].Kind,
	}, []string{"def", "ref", "ref", "ref"}; !slices.Equal(got, want) {
		t.Fatalf("Target result kinds = %#v, want %#v", got, want)
	}

	inspected, err := view.Inspect(
		"Symbols.mod:9",
		Options{Include: IncludeScope, Return: ReturnScope},
	)
	if err != nil {
		t.Fatal(err)
	}
	if inspected.Symbol != "Target" || len(inspected.Results) != 1 ||
		inspected.Results[0].Scope != "Caller" {
		t.Fatalf("qualified Target inspect = %#v, want Target in Caller", inspected)
	}
}

func TestModulaFindHonorsCommentAndStringSearchOptionsAcrossLines(t *testing.T) {
	t.Parallel()

	const source = `MODULE SearchOptions;
BEGIN
  (* Target begins
     Target continues
     Target ends *)
  Message := "Target";
  Target
END SearchOptions.
`
	root := t.TempDir()
	writeFile(t, root, "SearchOptions.mod", source)
	view := mustView(t, root)
	for _, test := range []struct {
		name                  string
		noComments, noStrings bool
		want                  []int
	}{
		{name: "include both", want: []int{3, 4, 5, 6, 7}},
		{name: "exclude comments", noComments: true, want: []int{6, 7}},
		{name: "exclude strings", noStrings: true, want: []int{3, 4, 5, 7}},
		{name: "code only", noComments: true, noStrings: true, want: []int{7}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			found, err := view.Find("Target", Options{
				Include: IncludeRefs, Return: ReturnLocations,
				NoComments: test.noComments, NoStrings: test.noStrings,
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := modulaTestResultLines(found.Results); !slices.Equal(got, test.want) {
				t.Fatalf("Target lines = %#v, want %#v", got, test.want)
			}
		})
	}
}

func TestModulaQualifiedOccurrenceWalkerBridgesTriviaExactly(t *testing.T) {
	t.Parallel()

	const source = `MODULE Qualified;
BEGIN
  Pkg.Target;
  Pkg . Target;
  Pkg.
    Target;
  Pkg.(* bridge *)Target;
  Pkg.(* multi-line
         bridge *)Target;
  Chain.(* one *)Node.(* two *)Leaf;
  Repeat.(* one *)Repeat.(* two *)Repeat;
  Message := "Pkg.Target Pkg.(* hidden *)Target";
  (* Pkg.Target Pkg.(* hidden *)Target *)
END Qualified.
`
	lines := modulaTestLines(source)
	prepared := newModulaLanguage().prepareSource(lines).(modulaLanguage)
	collect := func(symbol string) (map[int]int, int, bool) {
		adjustments := make(map[int]int)
		visited := 0
		handled := prepared.walkAdditionalSymbolOccurrences(
			lines, symbol,
			func(lineNo, additional int) bool {
				visited++
				if additional != 0 {
					adjustments[lineNo] = additional
				}
				return true
			},
		)
		return adjustments, visited, handled
	}

	adjustments, visited, handled := collect("Pkg.Target")
	if !handled {
		t.Fatal("qualified Modula occurrence walk was not handled")
	}
	if visited != len(lines) {
		t.Fatalf("qualified occurrence walk visited %d lines, want %d",
			visited, len(lines))
	}
	if want := map[int]int{4: 1, 5: 1, 7: 1, 8: 1}; !reflect.DeepEqual(adjustments, want) {
		t.Fatalf("qualified occurrence adjustments = %#v, want %#v",
			adjustments, want)
	}
	if got, _, ok := collect("Chain.Node.Leaf"); !ok ||
		!reflect.DeepEqual(got, map[int]int{10: 1}) {
		t.Fatalf("multi-component occurrence adjustments = %#v, handled %v", got, ok)
	}
	if got, _, ok := collect("Repeat.Repeat"); !ok ||
		!reflect.DeepEqual(got, map[int]int{11: 2}) {
		t.Fatalf("overlapping occurrence adjustments = %#v, handled %v", got, ok)
	}

	root := t.TempDir()
	writeFile(t, root, "Qualified.mod", source)
	found, err := mustView(t, root).Find("Pkg.Target", Options{
		Include:    IncludeRefs,
		Return:     ReturnLocations,
		NoComments: true,
		NoStrings:  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := modulaTestResultLines(found.Results), []int{3, 4, 5, 7, 8}; !slices.Equal(got, want) {
		t.Fatalf("qualified trivia-spanning find lines = %#v, want %#v", got, want)
	}
}

func TestModulaQualifiedOccurrenceWalkerHonorsEarlyStop(t *testing.T) {
	var source strings.Builder
	source.WriteString("MODULE Streaming;\nBEGIN\n")
	for range 4096 {
		source.WriteString("  Pkg.(* bridge *)Target;\n")
	}
	source.WriteString("END Streaming.\n")
	lines := modulaTestLines(source.String())
	prepared := newModulaLanguage().prepareSource(lines).(modulaLanguage)

	visited := 0
	positiveLine := 0
	handled := prepared.walkAdditionalSymbolOccurrences(
		lines, "Pkg.Target",
		func(lineNo, additional int) bool {
			visited++
			if additional == 0 {
				return true
			}
			positiveLine = lineNo
			return false
		},
	)
	if !handled || positiveLine != 3 || visited != 3 {
		t.Fatalf("early-stop walk = handled %v, positive line %d, visits %d; want true, 3, 3",
			handled, positiveLine, visited)
	}

	allocations := testing.AllocsPerRun(3, func() {
		count := 0
		if !prepared.walkAdditionalSymbolOccurrences(
			lines, "Pkg.Target",
			func(_ int, additional int) bool {
				count += additional
				return true
			},
		) || count != 4096 {
			panic("streaming qualified occurrence walk lost matches")
		}
	})
	if allocations > 32 {
		t.Fatalf("streaming occurrence walk allocated %.0f objects, want at most 32",
			allocations)
	}
}

func modulaTestLines(source string) []string {
	return strings.Split(strings.TrimSuffix(source, "\n"), "\n")
}

func modulaTestDefinitionSymbols(definitions []sourceDefinition) []string {
	symbols := make([]string, len(definitions))
	for index, definition := range definitions {
		symbols[index] = definition.symbol
	}
	return symbols
}

func modulaTestResultLines(results []Result) []int {
	lines := make([]int, len(results))
	for index, result := range results {
		lines[index] = result.Line
	}
	return lines
}

func modulaTestFirstDefinition(
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
	t.Fatalf("missing Modula-2 definition %q in %#v", symbol, definitions)
	return sourceDefinition{}
}

func modulaTestHasOwningDefinition(definitions []sourceDefinition, symbol string) bool {
	return modulaTestOwningDefinitionCount(definitions, symbol) > 0
}

func modulaTestOwningDefinitionCount(definitions []sourceDefinition, symbol string) int {
	count := 0
	for _, definition := range definitions {
		if definition.symbol == symbol && definition.ownsScope {
			count++
		}
	}
	return count
}

func modulaTestLineContaining(t *testing.T, lines []string, marker string) int {
	t.Helper()
	for index, line := range lines {
		if strings.Contains(line, marker) {
			return index + 1
		}
	}
	t.Fatalf("marker %q is absent from source", marker)
	return 0
}

func modulaTestLineAfter(t *testing.T, lines []string, after int, marker string) int {
	t.Helper()
	for index := after; index < len(lines); index++ {
		if strings.Contains(lines[index], marker) {
			return index + 1
		}
	}
	t.Fatalf("marker %q is absent after line %d", marker, after)
	return 0
}

func modulaTestAssertDefinitionCoordinates(
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
			t.Fatalf("invalid Modula-2 definition coordinates: %#v (lines=%d)",
				definition, len(lines))
		}
		line := lines[definition.line-1]
		if definition.column > len(line) ||
			!strings.HasPrefix(line[definition.column-1:], definition.symbol) {
			t.Fatalf("Modula-2 definition is not source-backed: %#v in %q", definition, line)
		}
	}
}

func modulaTestAssertLineWidths(t *testing.T, original, masked []string) {
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

func modulaTestImplements[Contract any](value any) bool {
	_, ok := value.(Contract)
	return ok
}
