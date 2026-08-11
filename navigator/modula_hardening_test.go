package navigator

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestModulaMalformedContextsDoNotLeakDefinitionsOrImports(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		source string
		want   []string
	}{
		{
			name: "stray separator closes import phase",
			source: `MODULE M;
;
IMPORT Late;
BEGIN
END M.
`,
			want: []string{"M"},
		},
		{
			name: "nested implementation with local terminator",
			source: `MODULE M;
IMPLEMENTATION MODULE Fake;
CONST Hidden = 1;
END Fake;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END M.
`,
			want: []string{"M", "Tail"},
		},
		{
			name: "nested implementation with unit terminator",
			source: `MODULE M;
IMPLEMENTATION MODULE Fake;
CONST Hidden = 1;
END Fake.
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END M.
`,
			want: []string{"M", "Tail"},
		},
		{
			name: "nested definition unit",
			source: `MODULE M;
DEFINITION MODULE Fake;
CONST Hidden = 1;
END Fake.
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END M.
`,
			want: []string{"M", "Tail"},
		},
		{
			name: "nested definition invalid body exits at named owner",
			source: `MODULE M;
DEFINITION MODULE Fake;
BEGIN
CONST Hidden = 1;
END Fake.
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END M.
`,
			want: []string{"M", "Tail"},
		},
		{
			name: "nested definition except exits at named owner",
			source: `MODULE M;
DEFINITION MODULE Fake;
EXCEPT
CONST Hidden = 1;
END Fake.
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END M.
`,
			want: []string{"M", "Tail"},
		},
		{
			name: "nested definition finally exits at named owner",
			source: `MODULE M;
DEFINITION MODULE Fake;
FINALLY
CONST Hidden = 1;
END Fake.
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END M.
`,
			want: []string{"M", "Tail"},
		},
		{
			name: "local module in definition suppresses its contents",
			source: `DEFINITION MODULE D;
MODULE Bad;
CONST Hidden = 1;
PROCEDURE Ghost;
END Ghost;
END Bad;
PROCEDURE Tail;
END D.
`,
			want: []string{"D", "Tail"},
		},
		{
			name: "export closes import phase",
			source: `DEFINITION MODULE D;
EXPORT QUALIFIED X;
IMPORT Late;
PROCEDURE Tail;
END D.
`,
			want: []string{"D", "Tail"},
		},
		{
			name: "tokens after completed unit are inert",
			source: `MODULE M;
END M.
IMPORT Late;
CONST Outside = 1;
`,
			want: []string{"M"},
		},
		{
			name: "malformed header closes import phase",
			source: `MODULE M;
Bogus;
IMPORT Late;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END M.
`,
			want: []string{"M", "Tail"},
		},
		{
			name: "unterminated malformed header closes import phase",
			source: `MODULE M;
Bogus
IMPORT Late;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END M.
`,
			want: []string{"M", "Tail"},
		},
		{
			name: "illegal definition begin suppresses body declarations",
			source: `DEFINITION MODULE D;
BEGIN
CONST Hidden = 1;
PROCEDURE Ghost;
END;
PROCEDURE Tail;
END D.
`,
			want: []string{"D", "Tail"},
		},
		{
			name: "illegal definition finally suppresses body declarations",
			source: `DEFINITION MODULE D;
FINALLY
CONST Hidden = 1;
END;
PROCEDURE Tail;
END D.
`,
			want: []string{"D", "Tail"},
		},
		{
			name: "illegal procedure finally suppresses body declarations",
			source: `MODULE M;
PROCEDURE P;
FINALLY
CONST Hidden = 1;
END;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END M.
`,
			want: []string{"M", "P", "Tail"},
		},
		{
			name: "illegal procedure finally accepts named owner resync",
			source: `MODULE M;
PROCEDURE P;
FINALLY
CONST Hidden = 1;
END P;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END M.
`,
			want: []string{"M", "P", "Tail"},
		},
		{
			name: "illegal leading procedure except suppresses declarations",
			source: `MODULE M;
PROCEDURE P;
EXCEPT
CONST Hidden = 1;
END P;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END M.
`,
			want: []string{"M", "P", "Tail"},
		},
		{
			name: "illegal definition body tracks unterminated control separator",
			source: `DEFINITION MODULE D;
BEGIN
IF ready THEN
END
END;
PROCEDURE Tail;
END D.
`,
			want: []string{"D", "Tail"},
		},
		{
			name: "illegal definition body suppresses nested named owner",
			source: `DEFINITION MODULE D;
BEGIN
PROCEDURE Ghost;
BEGIN
END Ghost;
END;
PROCEDURE Tail;
END D.
`,
			want: []string{"D", "Tail"},
		},
		{
			name: "illegal definition UNTIL cannot close IF",
			source: `DEFINITION MODULE D;
BEGIN
IF ready THEN
UNTIL ready;
END;
PROCEDURE Hidden;
END;
PROCEDURE Tail;
END D.
`,
			want: []string{"D", "Tail"},
		},
		{
			name: "reserved from module is not an import",
			source: `MODULE M;
FROM MODULE IMPORT Hidden;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END M.
`,
			want: []string{"M", "Tail"},
		},
		{
			name: "reserved direct import name is not an import",
			source: `MODULE M;
IMPORT MODULE;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END M.
`,
			want: []string{"M", "Tail"},
		},
		{
			name: "reserved from import name is not an import",
			source: `MODULE M;
FROM Good IMPORT MODULE;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END M.
`,
			want: []string{"M", "Tail"},
		},
		{
			name: "reserved malformed procedure name cannot own tail",
			source: `MODULE M;
PROCEDURE MODULE;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END M.
`,
			want: []string{"M", "Tail"},
		},
		{
			name: "malformed import closes import phase",
			source: `MODULE M;
IMPORT MODULE;
IMPORT Leaked;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END M.
`,
			want: []string{"M", "Tail"},
		},
		{
			name: "malformed procedure closes import phase",
			source: `MODULE M;
PROCEDURE ;
IMPORT Leaked;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END M.
`,
			want: []string{"M", "Tail"},
		},
		{
			name: "malformed local module closes import phase",
			source: `MODULE M;
MODULE ;
IMPORT Leaked;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END M.
`,
			want: []string{"M", "Tail"},
		},
		{
			name: "missing procedure terminator keeps following declaration",
			source: `MODULE M;
PROCEDURE P;
BEGIN
END P
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END M.
`,
			want: []string{"M", "P", "Tail"},
		},
		{
			name: "bare procedure terminator keeps following declaration",
			source: `MODULE M;
PROCEDURE P;
BEGIN
END;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END M.
`,
			want: []string{"M", "P", "Tail"},
		},
		{
			name: "unterminated bare procedure END keeps following declaration",
			source: `MODULE M;
PROCEDURE P;
BEGIN
END
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END M.
`,
			want: []string{"M", "P", "Tail"},
		},
		{
			name: "same-line bare procedure END keeps following declaration",
			source: `MODULE M;
PROCEDURE P;
BEGIN
END; PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END M.
`,
			want: []string{"M", "P", "Tail"},
		},
		{
			name: "constant CASE token does not open a type block",
			source: `MODULE M;
CONST Broken = CASE;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END M.
`,
			want: []string{"M", "Tail"},
		},
		{
			name: "reserved RECORD name does not open a type block",
			source: `MODULE M;
TYPE RECORD = INTEGER;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END M.
`,
			want: []string{"M", "Tail"},
		},
		{
			name: "constant missing separator keeps following procedure",
			source: `DEFINITION MODULE D;
CONST Broken = 1
PROCEDURE Tail;
END D.
`,
			want: []string{"D", "Tail"},
		},
		{
			name: "type missing separator keeps following procedure",
			source: `DEFINITION MODULE D;
TYPE Broken = INTEGER
PROCEDURE Tail;
END D.
`,
			want: []string{"D", "Tail"},
		},
		{
			name: "variable missing separator keeps following procedure",
			source: `DEFINITION MODULE D;
VAR Broken: INTEGER
PROCEDURE Tail;
END D.
`,
			want: []string{"D", "Tail"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			lines := modulaTestLines(test.source)
			tree := modulaTreeTestParseRecovery(t, test.source)
			if spans := modulaSyntaxErrorSpans(tree, len(test.source)); len(spans) == 0 {
				t.Fatal("malformed context produced no concrete recovery evidence")
			}
			lexical := analyzeModulaLexically(test.source, len(lines))
			analysis := analyzeModulaSource(test.source, len(lines))
			if analysis == nil {
				t.Fatal("analyzeModulaSource returned nil")
			}
			definitionPaths := map[string][]sourceDefinition{
				"concrete": modulaTreeDefinitions(test.source, len(lines), tree),
				"fallback": lexical.definitions,
				"merged":   analysis.definitions,
			}
			for path, definitions := range definitionPaths {
				if got := modulaTestDefinitionSymbols(definitions); !slices.Equal(got, test.want) {
					t.Errorf("%s definitions = %#v, want %#v", path, got, test.want)
				}
				modulaTestAssertDefinitionCoordinates(t, lines, definitions)
			}
			importPaths := map[string][]cLineSpan{
				"concrete": modulaTreeImports(test.source, len(lines), tree),
				"fallback": lexical.imports,
				"merged":   analysis.imports,
			}
			for path, imports := range importPaths {
				if len(imports) != 0 {
					t.Errorf("%s imports = %#v, want none", path, imports)
				}
			}
		})
	}
}

func TestModulaLineStartProcedureTypeContinuationIsContextual(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		prefix string
	}{
		{name: "direct type", prefix: "TYPE Callback ="},
		{name: "pointer type", prefix: "TYPE Callback = POINTER TO"},
		{name: "variable type", prefix: "VAR Callback:"},
		{name: "array type", prefix: "TYPE Callback = ARRAY T OF"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			source := "DEFINITION MODULE D;\n" + test.prefix +
				"\n  PROCEDURE(T): R;\nPROCEDURE Tail;\nEND D.\n"
			lines := modulaTestLines(source)
			tree := modulaTreeTestParse(t, source)
			if spans := modulaSyntaxErrorSpans(tree, len(source)); len(spans) != 0 {
				t.Fatalf("valid procedure type recovery spans = %#v, want none", spans)
			}
			want := []string{"D", "Callback", "Tail"}
			for path, definitions := range map[string][]sourceDefinition{
				"concrete": modulaTreeDefinitions(source, len(lines), tree),
				"fallback": analyzeModulaLexically(source, len(lines)).definitions,
			} {
				if got := modulaTestDefinitionSymbols(definitions); !slices.Equal(got, want) {
					t.Errorf("%s procedure-type definitions = %#v, want %#v",
						path, got, want)
				}
			}
		})
	}
}

func TestModulaFallbackRetainsExactMultilineImportSpans(t *testing.T) {
	t.Parallel()

	const source = `MODULE M;
IMPORT
  A,
  B;
FROM Source
IMPORT C;
BEGIN
END M.
`
	lines := modulaTestLines(source)
	tree := modulaTreeTestParse(t, source)
	if spans := modulaSyntaxErrorSpans(tree, len(source)); len(spans) != 0 {
		t.Fatalf("valid multiline imports recovery spans = %#v, want none", spans)
	}
	want := []cLineSpan{{start: 2, end: 4}, {start: 5, end: 6}}
	analysis := analyzeModulaSource(source, len(lines))
	if analysis == nil {
		t.Fatal("analyzeModulaSource returned nil")
	}
	for path, imports := range map[string][]cLineSpan{
		"concrete": modulaTreeImports(source, len(lines), tree),
		"fallback": analyzeModulaLexically(source, len(lines)).imports,
		"merged":   analysis.imports,
	} {
		if !reflect.DeepEqual(imports, want) {
			t.Errorf("%s multiline imports = %#v, want %#v", path, imports, want)
		}
	}
}

func TestModulaProcedureHeadingGrammarIsContextExact(t *testing.T) {
	t.Parallel()

	valid := []struct {
		name   string
		source string
		want   []string
	}{
		{
			name: "implementation GNU attribute and inline forms",
			source: `MODULE M;
PROCEDURE __ATTRIBUTE__ __BUILTIN__ ((binding)) Attribute(x: T): R;
BEGIN
END Attribute;
PROCEDURE __INLINE__ Inline(...);
BEGIN
END Inline;
PROCEDURE Optional([count: CARDINAL]);
BEGIN
END Optional;
BEGIN
END M.
`,
			want: []string{"M", "Attribute", "Inline", "Optional"},
		},
		{
			name: "definition builtin inline optional and bracketed return",
			source: `DEFINITION MODULE D;
PROCEDURE __BUILTIN__ Builtin;
PROCEDURE __INLINE__ Inline;
PROCEDURE Optional([count: CARDINAL = 1]);
PROCEDURE Result(): [Pkg.T];
END D.
`,
			want: []string{"D", "Builtin", "Inline", "Optional", "Result"},
		},
		{
			name: "SYSTEM pseudo notation is comment opaque",
			source: `DEFINITION MODULE D;
(*
PROCEDURE ADR(VAR v: <anytype>): ADDRESS;
PROCEDURE CAST(<targettype>; value: <anytype>): <targettype>;
PROCEDURE ROTATE(value: <a packedset type>): <type of first parameter>;
*)
PROCEDURE Arrays(value: ARRAY OF ARRAY OF Pkg.T): [Pkg.R];
END D.
`,
			want: []string{"D", "Arrays"},
		},
	}
	for _, test := range valid {
		t.Run("valid/"+test.name, func(t *testing.T) {
			t.Parallel()
			lines := modulaTestLines(test.source)
			tree := modulaTreeTestParse(t, test.source)
			if spans := modulaSyntaxErrorSpans(tree, len(test.source)); len(spans) != 0 {
				t.Fatalf("valid heading recovery spans = %#v, want none", spans)
			}
			for path, definitions := range map[string][]sourceDefinition{
				"concrete": modulaTreeDefinitions(test.source, len(lines), tree),
				"fallback": analyzeModulaLexically(test.source, len(lines)).definitions,
			} {
				if got := modulaTestDefinitionSymbols(definitions); !slices.Equal(got, test.want) {
					t.Errorf("%s definitions = %#v, want %#v", path, got, test.want)
				}
			}
		})
	}

	invalid := []struct {
		name       string
		definition bool
		heading    string
	}{
		{name: "arbitrary parenthesized prefix", heading: "(Junk) Fake"},
		{name: "attribute without builtin payload", heading: "__ATTRIBUTE__ Fake"},
		{name: "stacked builtin markers", heading: "__BUILTIN__ __INLINE__ Fake"},
		{name: "trailing return garbage", heading: "Fake(): INTEGER Garbage"},
		{name: "extended parameter followed by section", heading: "Fake(a: T; ...; b: T)"},
		{name: "optional argument cannot be VAR", heading: "Fake([VAR a: T])"},
		{name: "optional argument has one name", heading: "Fake([a, b: T])"},
		{name: "optional argument rejects formal attribute", heading: "Fake([a: T <* unused *>])"},
		{name: "reserved formal type", heading: "Fake(a: MODULE)"},
		{name: "reserved return type", heading: "Fake(): MODULE"},
		{name: "indexed formal name", heading: "Fake(a[0]: T)"},
		{name: "documentary pseudo formal type", heading: "Fake(a: <anytype>)"},
		{name: "documentary pseudo return type", heading: "Fake(): <type>"},
		{
			name:       "definition attribute form",
			definition: true,
			heading:    "__ATTRIBUTE__ __BUILTIN__((foreign)) Fake",
		},
		{
			name:       "definition optional argument without default",
			definition: true,
			heading:    "Fake([count: CARDINAL])",
		},
		{
			name:       "definition optional argument rejects formal attribute",
			definition: true,
			heading:    "Fake([count: CARDINAL <* unused *> = 1])",
		},
	}
	for _, test := range invalid {
		t.Run("invalid/"+test.name, func(t *testing.T) {
			t.Parallel()
			owner, terminator := "MODULE M;", "END M."
			body := "\nBEGIN\nEND Fake;"
			want := []string{"M", "Tail"}
			if test.definition {
				owner, terminator = "DEFINITION MODULE D;", "END D."
				body = ""
				want = []string{"D", "Tail"}
			}
			source := owner + "\nPROCEDURE " + test.heading + ";" + body +
				"\nPROCEDURE Tail;\n"
			if !test.definition {
				source += "BEGIN\nEND Tail;\nBEGIN\n"
			}
			source += terminator + "\n"
			lines := modulaTestLines(source)
			tree := modulaTreeTestParseRecovery(t, source)
			if spans := modulaSyntaxErrorSpans(tree, len(source)); len(spans) == 0 {
				t.Fatal("invalid heading produced no concrete recovery evidence")
			}
			analysis := analyzeModulaSource(source, len(lines))
			if analysis == nil {
				t.Fatal("analyzeModulaSource returned nil")
			}
			for path, definitions := range map[string][]sourceDefinition{
				"concrete": modulaTreeDefinitions(source, len(lines), tree),
				"fallback": analyzeModulaLexically(source, len(lines)).definitions,
				"merged":   analysis.definitions,
			} {
				if got := modulaTestDefinitionSymbols(definitions); !slices.Equal(got, want) {
					t.Errorf("%s definitions = %#v, want %#v", path, got, want)
				}
			}
		})
	}
}

func TestModulaConcreteStatementsValidateSeparatorsAndDelimiters(t *testing.T) {
	t.Parallel()

	valid := `MODULE Statements;
CONST template = "nop";
BEGIN
  INC(x);
	Empty();
	Pkg.obj^[i].Run(a, b);
	Pkg.obj^[i] := T{1, 2..N BY -Step};
  value := ((2 + 4) DIV 2);
  values := {1, 2, 3};
	IF ready THEN END;
	WHILE ready DO END;
	FOR i := 1 TO 2 DO END;
	FOR i := f(a[1]) TO limit BY -Step DO END;
	FOR i := 1 TO 2 BY F(a[i]) DO END;
	WITH Pkg.item^[i].field DO END;
	CASE x OF END;
	CASE x OF ELSE Work END;
	CASE x OF 1: Work | 2: Work ELSE Work END;
	LOOP END;
	REPEAT Work UNTIL ready;
	value := a DIV
		b;
	RETURN
		result;
	RETURN;
	RETURN f(a[1]);
	ASM (template);
	ASM (template : : "r" (x));
	ASM (template : : : );
	ASM (template : , "r" (x));
	ASM (template : : : , "cc");
	ASM VOLATILE (template : [out] "=r" (value) : [in] "r" (x) : "cc", "memory")
EXCEPT
	Recover
FINALLY
	Cleanup
END Statements.
`
	if spans := modulaSyntaxErrorSpans(
		modulaTreeTestParse(t, valid), len(valid),
	); len(spans) != 0 {
		t.Fatalf("valid statements recovery spans = %#v, want none", spans)
	}

	invalid := []struct {
		name string
		body string
	}{
		{
			name: "missing statement separator",
			body: `WHILE x < y DO
    INC(x)
    INC(x)
  END`,
		},
		{name: "missing separator between ordinary statements", body: "Work\nCleanup"},
		{name: "missing separator after control", body: "IF ready THEN END\nWork"},
		{name: "missing right parenthesis", body: "value := ((2 + 4) DIV 2"},
		{name: "mismatched delimiter", body: "value := (items[1))"},
		{name: "missing constructor brace", body: "values := {1, 2, 3"},
		{name: "missing separator before control", body: "Work IF ready THEN END"},
		{name: "illegal label", body: "Work: IF ready THEN END"},
		{name: "standalone else", body: "ELSE Work"},
		{name: "standalone until", body: "UNTIL ready"},
		{name: "if missing then", body: "IF ready Work END"},
		{name: "while missing do", body: "WHILE ready Work END"},
		{name: "case missing of", body: "CASE x 1: Work END"},
		{name: "repeat missing condition", body: "REPEAT Work UNTIL END"},
		{name: "nested begin is not a statement", body: "BEGIN Work END"},
		{name: "literal is not a statement", body: "42"},
		{name: "operator is not an ordinary statement", body: "x + y"},
		{name: "malformed call argument", body: "f(+)"},
		{name: "assignment requires exact designator", body: "x + y := z"},
		{name: "assignment requires expression", body: "x :="},
		{name: "return rejects assignment", body: "RETURN := x"},
		{name: "exit has no operand", body: "EXIT + x"},
		{name: "retry has no operand", body: "RETRY + x"},
		{name: "for missing assignment and range", body: "FOR i DO Work END"},
		{name: "for requires bare control identifier", body: "FOR i + j := 1 TO 2 DO END"},
		{name: "for requires lower expression", body: "FOR i := + TO 2 DO END"},
		{name: "for requires upper expression", body: "FOR i := 1 TO + DO END"},
		{name: "for by is constant expression", body: "FOR i := 1 TO 2 BY + DO END"},
		{name: "for by rejects constant root index", body: "FOR i := 1 TO 2 BY a[i] DO END"},
		{name: "with requires a designator", body: "WITH 42 DO Work END"},
		{name: "with rejects expression", body: "WITH x + y DO Work END"},
		{name: "with rejects call", body: "WITH x() DO Work END"},
		{name: "assignment in if condition", body: "IF ready := TRUE THEN Work END"},
		{name: "assignment in case selector", body: "CASE value := 1 OF END"},
		{name: "asm requires parentheses", body: `ASM "mov"`},
		{name: "asm rejects dangling template operator", body: `ASM ("nop" +)`},
		{name: "asm operand requires expression parentheses", body: `ASM ("nop" : "r")`},
		{name: "asm operand requires expression", body: `ASM ("nop" : "r" ())`},
		{name: "asm named operand requires identifier", body: `ASM ("nop" : [] "r" (x))`},
		{name: "asm trash is constant expression", body: `ASM ("nop" : : : value[i])`},
		{name: "asm list rejects second leading comma", body: `ASM ("nop" : , , "r" (x))`},
		{name: "asm list rejects trailing comma", body: `ASM ("nop" : "r" (x),)`},
		{
			name: "asm has at most three colon groups",
			body: `ASM ("mov" : "r" (x) : "m" (y) : "cc" : "extra")`,
		},
		{name: "missing separator before return", body: "RETURN x\nRETURN y"},
		{name: "standalone then", body: "THEN Work"},
		{name: "standalone branch", body: "| Work"},
		{
			name: "repeated except",
			body: "Work\nEXCEPT\nRecover\nEXCEPT\nAgain",
		},
		{
			name: "finally inside control",
			body: "IF ready THEN\nWork\nFINALLY\nCleanup\nEND",
		},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			source := "MODULE Invalid;\nBEGIN\n  " + test.body + "\nEND Invalid.\n"
			tree := modulaTreeTestParseRecovery(t, source)
			if spans := modulaSyntaxErrorSpans(tree, len(source)); len(spans) == 0 {
				t.Fatal("malformed statement produced no concrete recovery evidence")
			}
			lines := modulaTestLines(source)
			analysis := analyzeModulaSource(source, len(lines))
			if analysis == nil || !slices.Equal(
				modulaTestDefinitionSymbols(analysis.definitions), []string{"Invalid"},
			) {
				t.Fatalf("malformed statement analysis = %#v, want only Invalid", analysis)
			}
		})
	}
}

func TestModulaControlHeaderErrorsPointAtOffendingToken(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name, body string
	}{
		{name: "for", body: "FOR i + j := 1 TO 2 DO END"},
		{name: "with", body: "WITH x + y DO END"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			source := "MODULE M;\nBEGIN\n" + test.body + "\nEND M.\n"
			offending := strings.Index(source, "+")
			if offending < 0 {
				t.Fatal("fixture has no offending plus token")
			}
			spans := modulaSyntaxErrorSpans(
				modulaTreeTestParseRecovery(t, source), len(source),
			)
			if !slices.Contains(spans, (cByteSpan{
				start: offending, end: offending + 1,
			})) {
				t.Fatalf("control recovery spans = %#v, want exact plus span %d-%d",
					spans, offending, offending+1)
			}
		})
	}
}

func TestModulaConcreteRejectsMalformedDeclarationAndExpressionGrammar(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		source    string
		forbidden []string
		wantTail  bool
	}{
		{
			name: "constant missing operand",
			source: `MODULE M;
CONST Broken = +;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END M.
`,
			forbidden: []string{"Broken"}, wantTail: true,
		},
		{
			name: "constant adjacent operands",
			source: `MODULE M;
CONST Broken = 1 2;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END M.
`,
			forbidden: []string{"Broken"}, wantTail: true,
		},
		{
			name: "constant repeated additive operator",
			source: `MODULE M;
CONST Broken = 1 + + 2;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END M.
`,
			forbidden: []string{"Broken"}, wantTail: true,
		},
		{
			name: "type is not punctuation",
			source: `MODULE M;
TYPE Fake = +;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END M.
`,
			forbidden: []string{"Fake"}, wantTail: true,
		},
		{
			name: "type reference has trailing token",
			source: `MODULE M;
TYPE Broken = INTEGER Garbage;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END M.
`,
			forbidden: []string{"Broken"}, wantTail: true,
		},
		{
			name: "subrange requires both bounds",
			source: `MODULE M;
TYPE Broken = [1];
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END M.
`,
			forbidden: []string{"Broken"}, wantTail: true,
		},
		{
			name: "qualified subrange requires both bounds",
			source: `MODULE M;
TYPE Broken = INTEGER[0];
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END M.
`,
			forbidden: []string{"Broken"}, wantTail: true,
		},
		{
			name: "array requires a simple first index",
			source: `MODULE M;
TYPE Broken = ARRAY + OF INTEGER;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END M.
`,
			forbidden: []string{"Broken"}, wantTail: true,
		},
		{
			name: "array requires every index to be simple",
			source: `MODULE M;
TYPE Broken = ARRAY INTEGER, + OF INTEGER;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END M.
`,
			forbidden: []string{"Broken"}, wantTail: true,
		},
		{
			name: "procedure type parameters are comma separated types",
			source: `MODULE M;
TYPE Broken = PROCEDURE (x x);
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END M.
`,
			forbidden: []string{"Broken"}, wantTail: true,
		},
		{
			name: "set element type has no trailing token",
			source: `MODULE M;
TYPE Broken = SET OF INTEGER Garbage;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END M.
`,
			forbidden: []string{"Broken"}, wantTail: true,
		},
		{
			name: "variable type has no trailing token",
			source: `MODULE M;
VAR broken: INTEGER Garbage;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END M.
`,
			forbidden: []string{"broken"}, wantTail: true,
		},
		{
			name: "variable index and type require expressions",
			source: `MODULE M;
VAR bogus[]: +;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END M.
`,
			forbidden: []string{"bogus"}, wantTail: true,
		},
		{
			name: "record fields use identifier lists",
			source: `MODULE M;
TYPE R = RECORD field[0]: INTEGER END;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END M.
`,
			forbidden: []string{"R", "field"}, wantTail: true,
		},
		{
			name: "record field requires a type",
			source: `MODULE M;
TYPE R = RECORD field: + END;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END M.
`,
			forbidden: []string{"R", "field"}, wantTail: true,
		},
		{
			name: "record field type has no trailing token",
			source: `MODULE M;
TYPE R = RECORD field: INTEGER Garbage END;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END M.
`,
			forbidden: []string{"R", "field"}, wantTail: true,
		},
		{
			name: "priority needs a constant expression",
			source: `MODULE M[+];
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END M.
`,
			wantTail: true,
		},
		{
			name: "assignment missing operand",
			source: `MODULE M;
BEGIN
x := +
END M.
`,
		},
		{
			name: "return missing operand",
			source: `MODULE M;
BEGIN
RETURN +
END M.
`,
		},
		{
			name: "if missing operand",
			source: `MODULE M;
BEGIN
IF + THEN END
END M.
`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			tree := modulaTreeTestParseRecovery(t, test.source)
			if spans := modulaSyntaxErrorSpans(tree, len(test.source)); len(spans) == 0 {
				t.Fatal("malformed grammar produced no concrete recovery evidence")
			}
			lines := modulaTestLines(test.source)
			paths := map[string][]sourceDefinition{
				"concrete": modulaTreeDefinitions(test.source, len(lines), tree),
			}
			if test.wantTail {
				paths["fallback"] = analyzeModulaLexically(
					test.source, len(lines),
				).definitions
				analysis := analyzeModulaSource(test.source, len(lines))
				if analysis == nil {
					t.Fatal("analyzeModulaSource returned nil")
				}
				paths["merged"] = analysis.definitions
			}
			for path, definitions := range paths {
				symbols := modulaTestDefinitionSymbols(definitions)
				if !slices.Contains(symbols, "M") {
					t.Errorf("%s malformed grammar lost module definition: %#v",
						path, definitions)
				}
				if test.wantTail && !slices.Contains(symbols, "Tail") {
					t.Errorf("%s malformed grammar lost independent Tail: %#v",
						path, definitions)
				}
				for _, forbidden := range test.forbidden {
					if slices.Contains(symbols, forbidden) {
						t.Errorf("%s malformed grammar promoted %q: %#v",
							path, forbidden, definitions)
					}
				}
				modulaTestAssertDefinitionCoordinates(t, lines, definitions)
			}
		})
	}
}

func TestModulaConstantExpressionGrammarConcreteAndSlices(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		expression string
		valid      bool
	}{
		{name: "empty constructor", expression: "{}", valid: true},
		{name: "constructor range and repetition", expression: "{1, 2..N BY -2}", valid: true},
		{name: "typed constructor", expression: "Pkg.T{1..N BY Step}", valid: true},
		{name: "empty call", expression: "F()", valid: true},
		{name: "call argument designator", expression: "F(a.b[i, j]^)", valid: true},
		{name: "builtin attribute", expression: "__ATTRIBUTE__ __BUILTIN__((x))", valid: true},
		{name: "qualified builtin attribute", expression: "__ATTRIBUTE__ __BUILTIN__((<Pkg.T, size>))", valid: true},
		{name: "free repetition", expression: "1 BY 2"},
		{name: "free range", expression: "1..2"},
		{name: "repeated constructor repetition", expression: "{1 BY 2 BY 3}"},
		{name: "repeated constructor range", expression: "{1..2..3}"},
		{name: "chained relation", expression: "1 = 2 = 3"},
		{name: "grouping comma", expression: "(a, b)"},
		{name: "root indexed designator", expression: "a.b[i, j]^"},
		{name: "constructor indexed component", expression: "T{a[i]}"},
		{name: "leading empty call argument", expression: "F(, a)"},
		{name: "trailing empty call argument", expression: "F(a,)"},
		{name: "empty index", expression: "F(a[])"},
		{name: "bare builtin marker", expression: "__BUILTIN__((x))"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			expressionTokens := lexModula(test.expression).tokens
			if got := modulaConstantExpressionRangeValid(
				expressionTokens, 0, len(expressionTokens),
			); got != test.valid {
				t.Errorf("constant expression slice validity = %t, want %t", got, test.valid)
			}
			directiveTokens := lexModula("<* alignment(" + test.expression + ") *>").tokens
			if got := modulaDefaultAttributeRangeValid(
				directiveTokens, 0, len(directiveTokens),
			); got != test.valid {
				t.Errorf("directive expression slice validity = %t, want %t", got, test.valid)
			}

			source := "MODULE Expressions;\nCONST Candidate = " + test.expression +
				";\nPROCEDURE Tail;\nBEGIN\nEND Tail;\nBEGIN\nEND Expressions.\n"
			lines := modulaTestLines(source)
			tree := modulaTreeTestParseRecovery(t, source)
			spans := modulaSyntaxErrorSpans(tree, len(source))
			if test.valid && len(spans) != 0 {
				t.Errorf("valid expression recovery spans = %#v", spans)
			}
			if !test.valid && len(spans) == 0 {
				t.Error("invalid expression produced no concrete recovery evidence")
			}
			analysis := analyzeModulaSource(source, len(lines))
			if analysis == nil {
				t.Fatal("analyzeModulaSource returned nil")
			}
			paths := map[string][]sourceDefinition{
				"concrete": modulaTreeDefinitions(source, len(lines), tree),
				"fallback": analyzeModulaLexically(source, len(lines)).definitions,
				"merged":   analysis.definitions,
			}
			want := []string{"Expressions", "Tail"}
			if test.valid {
				want = []string{"Expressions", "Candidate", "Tail"}
			}
			for path, definitions := range paths {
				if got := modulaTestDefinitionSymbols(definitions); !slices.Equal(got, want) {
					t.Errorf("%s definitions = %#v, want %#v", path, got, want)
				}
				modulaTestAssertDefinitionCoordinates(t, lines, definitions)
			}
		})
	}
}

func TestModulaRecordVariantsAllowEmptyAlternatives(t *testing.T) {
	t.Parallel()

	const source = `MODULE EmptyVariants;
TYPE Choice = RECORD
  CASE tag: BOOLEAN OF
  || TRUE: first: INTEGER |||
  FALSE: second: INTEGER |
  ELSE
  END;
  final: CARDINAL
END;
BEGIN
END EmptyVariants.
`
	lines := modulaTestLines(source)
	tree := modulaTreeTestParse(t, source)
	if spans := modulaSyntaxErrorSpans(tree, len(source)); len(spans) != 0 {
		t.Fatalf("empty record variants recovery spans = %#v", spans)
	}
	want := []string{"EmptyVariants", "Choice", "tag", "first", "second", "final"}
	for path, definitions := range map[string][]sourceDefinition{
		"concrete": modulaTreeDefinitions(source, len(lines), tree),
		"fallback": analyzeModulaLexically(source, len(lines)).definitions,
	} {
		if got := modulaTestDefinitionSymbols(definitions); !slices.Equal(got, want) {
			t.Errorf("%s empty-variant definitions = %#v, want %#v", path, got, want)
		}
		modulaTestAssertDefinitionCoordinates(t, lines, definitions)
	}
}

func TestModulaProcedureBlockRejectsFinally(t *testing.T) {
	t.Parallel()

	const source = `MODULE M;
PROCEDURE P;
BEGIN
  Work
FINALLY
  Cleanup
END P;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END M.
`
	tree := modulaTreeTestParseRecovery(t, source)
	if spans := modulaSyntaxErrorSpans(tree, len(source)); len(spans) == 0 {
		t.Fatal("procedure FINALLY produced no concrete recovery evidence")
	}
	lines := modulaTestLines(source)
	analysis := analyzeModulaSource(source, len(lines))
	if analysis == nil {
		t.Fatal("analyzeModulaSource returned nil")
	}
	want := []string{"M", "P", "Tail"}
	if got := modulaTestDefinitionSymbols(analysis.definitions); !slices.Equal(got, want) {
		t.Fatalf("procedure FINALLY definitions = %#v, want %#v", got, want)
	}
}

func TestModulaLocalFinallyAndFallbackControlScopesAreExact(t *testing.T) {
	t.Parallel()

	const source = `MODULE Outer;
MODULE Local;
FINALLY
  IF ready THEN
  END
END Local;
BEGIN
  REPEAT
    IF ready THEN
    END
  UNTIL done
END Outer.
`
	lines := modulaTestLines(source)
	tree := modulaTreeTestParse(t, source)
	if spans := modulaSyntaxErrorSpans(tree, len(source)); len(spans) != 0 {
		t.Fatalf("valid FINALLY/control recovery spans = %#v, want none", spans)
	}
	wantDefinitions := []string{"Outer", "Local"}
	for path, definitions := range map[string][]sourceDefinition{
		"concrete": modulaTreeDefinitions(source, len(lines), tree),
		"fallback": analyzeModulaLexically(source, len(lines)).definitions,
	} {
		if got := modulaTestDefinitionSymbols(definitions); !slices.Equal(got, wantDefinitions) {
			t.Errorf("%s definitions = %#v, want %#v", path, got, wantDefinitions)
		}
	}
	wantFallback := []cLineScope{
		{start: 1, end: 12},
		{start: 2, end: 6},
		{start: 3, end: 6},
		{start: 4, end: 5},
		{start: 7, end: 12},
		{start: 8, end: 11},
		{start: 9, end: 10},
	}
	if got := analyzeModulaLexically(source, len(lines)).scopes; !reflect.DeepEqual(got, wantFallback) {
		t.Fatalf("fallback scopes = %#v, want %#v", got, wantFallback)
	}
}

func TestModulaLexingUsesExactGNUASCIIAndNumericBoundaries(t *testing.T) {
	t.Parallel()

	lexed := lexModula("01A 377B 141C 0FFH .5 .E+2 1.E+2 1.25E+3 1..10 123ABC 12foo 0GH 1.2e3 1E3 1. 1.E+")
	want := []string{
		"01A", "377B", "141C", "0FFH", ".5", ".E+2", "1.E+2", "1.25E+3",
		"1", "..", "10", "123", "ABC", "12", "foo", "0", "GH", "1.2", "e3",
		"1", "E3", "1", ".", "1", ".", "E", "+",
	}
	got := make([]string, 0, len(lexed.tokens))
	for _, token := range lexed.tokens {
		if !token.gap {
			got = append(got, token.text)
		}
	}
	if !slices.Equal(got, want) {
		t.Fatalf("GNU numeric tokens = %#v, want %#v", got, want)
	}

	for _, separator := range []string{"\u00a0", "\f", "\v"} {
		source := "MODULE" + separator + "M; END M."
		if modulaContentGate(lexModula(source)) {
			t.Errorf("non-GNU separator %q made a valid module header", separator)
		}
	}
}

func TestModulaHeaderOverflowAndWrongOwnerTerminatorKeepIndependentTail(t *testing.T) {
	t.Parallel()

	overflow := "MODULE M;\nCONST Huge = " +
		strings.Repeat("1 + ", modulaMaximumDeclarationTokens+8) + "1\n" +
		"PROCEDURE Tail;\nBEGIN\nEND Tail;\nBEGIN\nEND M.\n"
	overflowLines := modulaTestLines(overflow)
	overflowAnalysis := analyzeModulaLexically(overflow, len(overflowLines))
	if got := modulaTestDefinitionSymbols(overflowAnalysis.definitions); !slices.Equal(got, []string{"M", "Tail"}) {
		t.Fatalf("overflow recovery definitions = %#v, want M and Tail", got)
	}

	wrongTerminator := `MODULE M;
PROCEDURE P;
BEGIN
END P.
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END M.
`
	lines := modulaTestLines(wrongTerminator)
	definitions := analyzeModulaLexically(wrongTerminator, len(lines)).definitions
	if got := modulaTestDefinitionSymbols(definitions); !slices.Equal(got, []string{"M", "P", "Tail"}) {
		t.Fatalf("wrong-terminator definitions = %#v, want M/P/Tail", got)
	}
	if modulaTestHasOwningDefinition(definitions, "P") {
		t.Fatalf("wrong-terminator procedure became owning: %#v", definitions)
	}
	if !modulaTestHasOwningDefinition(definitions, "Tail") {
		t.Fatalf("independent Tail is not owning: %#v", definitions)
	}

	largeOwnerTests := []struct {
		name   string
		source string
	}{
		{
			name: "procedure heading over concrete frontier",
			source: "MODULE M;\nPROCEDURE Huge(" +
				strings.Repeat("a: T; ", modulaMaximumConcreteTokens/4+16) +
				"z: T);\nPROCEDURE Inner;\nBEGIN\nEND Inner;\n" +
				"BEGIN\nEND Huge;\n" +
				"PROCEDURE Tail;\nBEGIN\nEND Tail;\nBEGIN\nEND M.\n",
		},
		{
			name: "local module priority over concrete frontier",
			source: "MODULE M;\nMODULE Huge[" +
				strings.Repeat("1 + ", modulaMaximumConcreteTokens/2+16) +
				"1];\nBEGIN\nEND Huge;\n" +
				"PROCEDURE Tail;\nBEGIN\nEND Tail;\nBEGIN\nEND M.\n",
		},
	}
	for _, test := range largeOwnerTests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			lines := modulaTestLines(test.source)
			analysis := analyzeModulaSource(test.source, len(lines))
			if analysis == nil {
				t.Fatal("analyzeModulaSource returned nil")
			}
			if analysis.tree != nil {
				t.Fatal("over-concrete owner unexpectedly retained a concrete tree")
			}
			definitions := analysis.definitions
			if !slices.Contains(modulaTestDefinitionSymbols(definitions), "Tail") {
				t.Fatalf("over-concrete owner lost independent Tail among %d definitions",
					len(definitions))
			}
			if !modulaTestHasOwningDefinition(definitions, "M") ||
				!modulaTestHasOwningDefinition(definitions, "Tail") {
				t.Fatalf("over-concrete owner damaged M/Tail scope ownership: %#v",
					definitions)
			}
			if test.name == "procedure heading over concrete frontier" {
				for _, owner := range []string{"Huge", "Inner"} {
					if !slices.Contains(modulaTestDefinitionSymbols(definitions), owner) ||
						!modulaTestHasOwningDefinition(definitions, owner) {
						t.Errorf("over-concrete procedure lost %s ownership: %#v",
							owner, definitions)
					}
				}
			}
		})
	}

	topLevelOwner := "MODULE Huge[" +
		strings.Repeat("1 + ", modulaMaximumConcreteTokens/2+16) +
		"1];\nPROCEDURE Tail;\nBEGIN\nEND Tail;\nBEGIN\nEND Huge.\n"
	topLevelLines := modulaTestLines(topLevelOwner)
	topLevelAnalysis := analyzeModulaSource(topLevelOwner, len(topLevelLines))
	if topLevelAnalysis == nil {
		t.Fatal("top-level overflow module analysis is nil")
	}
	if topLevelAnalysis.tree != nil {
		t.Fatal("top-level overflow module unexpectedly retained concrete tree")
	}
	topLevelDefinitions := topLevelAnalysis.definitions
	if got := modulaTestDefinitionSymbols(topLevelDefinitions); !slices.Equal(
		got,
		[]string{"Huge", "Tail"},
	) {
		t.Fatalf("top-level overflow module definitions = %#v, want Huge/Tail", got)
	}
	for _, owner := range []string{"Huge", "Tail"} {
		if !modulaTestHasOwningDefinition(topLevelDefinitions, owner) {
			t.Errorf("top-level overflow module lost %s ownership: %#v",
				owner, topLevelDefinitions)
		}
	}

	definitionOwner := "DEFINITION MODULE D;\nPROCEDURE Huge(" +
		strings.Repeat("a: T; ", modulaMaximumConcreteTokens/4+16) +
		"z: T);\nPROCEDURE Tail;\nEND D.\n"
	definitionLines := modulaTestLines(definitionOwner)
	definitionAnalysis := analyzeModulaSource(
		definitionOwner,
		len(definitionLines),
	)
	if definitionAnalysis == nil || definitionAnalysis.tree != nil {
		t.Fatalf("definition overflow procedure analysis/tree = %#v, want non-concrete analysis",
			definitionAnalysis)
	}
	if got := modulaTestDefinitionSymbols(definitionAnalysis.definitions); !slices.Equal(
		got,
		[]string{"D", "Huge", "Tail"},
	) {
		t.Fatalf("definition overflow procedure definitions = %#v, want D/Huge/Tail", got)
	}

	invalidProcedure := "MODULE M;\nPROCEDURE Huge(" +
		strings.Repeat("a: T; ", modulaMaximumConcreteTokens/4+16) +
		"z[0]);\nBEGIN\nEND Huge;\n" +
		"PROCEDURE Tail;\nBEGIN\nEND Tail;\nBEGIN\nEND M.\n"
	invalidLines := modulaTestLines(invalidProcedure)
	invalidAnalysis := analyzeModulaSource(invalidProcedure, len(invalidLines))
	if invalidAnalysis == nil || invalidAnalysis.tree != nil {
		t.Fatalf("invalid overflow procedure analysis/tree = %#v, want non-concrete analysis",
			invalidAnalysis)
	}
	if got := modulaTestDefinitionSymbols(invalidAnalysis.definitions); !slices.Equal(
		got,
		[]string{"M", "Tail"},
	) {
		t.Fatalf("invalid overflow procedure definitions = %#v, want M/Tail", got)
	}

	var singleSection strings.Builder
	singleSection.WriteString("MODULE M;\nPROCEDURE Huge(")
	for index := range modulaMaximumConcreteTokens/2 + 16 {
		fmt.Fprintf(&singleSection, "arg%d, ", index)
	}
	singleSection.WriteString("last: T);\nPROCEDURE Inner;\nBEGIN\nEND Inner;\n")
	singleSection.WriteString("BEGIN\nEND Huge;\nPROCEDURE Tail;\nBEGIN\nEND Tail;\n")
	singleSection.WriteString("BEGIN\nEND M.\n")
	singleSectionSource := singleSection.String()
	singleSectionLines := modulaTestLines(singleSectionSource)
	singleSectionAnalysis := analyzeModulaSource(
		singleSectionSource,
		len(singleSectionLines),
	)
	if singleSectionAnalysis == nil || singleSectionAnalysis.tree != nil {
		t.Fatalf("single-section overflow procedure analysis/tree = %#v, want non-concrete analysis",
			singleSectionAnalysis)
	}
	if got := modulaTestDefinitionSymbols(singleSectionAnalysis.definitions); !slices.Equal(
		got,
		[]string{"M", "Huge", "Inner", "Tail"},
	) {
		t.Fatalf("single-section overflow definitions = %#v, want M/Huge/Inner/Tail", got)
	}

	hugeReturn := "MODULE M;\nPROCEDURE Huge(): " +
		strings.Repeat("Pkg.", modulaMaximumConcreteTokens/2+16) +
		"Result;\nPROCEDURE Inner;\nBEGIN\nEND Inner;\n" +
		"BEGIN\nEND Huge;\nPROCEDURE Tail;\nBEGIN\nEND Tail;\nBEGIN\nEND M.\n"
	hugeReturnLines := modulaTestLines(hugeReturn)
	hugeReturnAnalysis := analyzeModulaSource(hugeReturn, len(hugeReturnLines))
	if hugeReturnAnalysis == nil || hugeReturnAnalysis.tree != nil {
		t.Fatalf("overflow return analysis/tree = %#v, want non-concrete analysis",
			hugeReturnAnalysis)
	}
	if got := modulaTestDefinitionSymbols(hugeReturnAnalysis.definitions); !slices.Equal(
		got,
		[]string{"M", "Huge", "Inner", "Tail"},
	) {
		t.Fatalf("overflow return definitions = %#v, want M/Huge/Inner/Tail", got)
	}

	hugeDefault := "MODULE M;\nPROCEDURE Huge([arg: T = " +
		strings.Repeat("1 + ", modulaMaximumConcreteTokens/2+16) +
		"1]);\nBEGIN\nEND Huge;\n" +
		"PROCEDURE Tail;\nBEGIN\nEND Tail;\nBEGIN\nEND M.\n"
	hugeDefaultLines := modulaTestLines(hugeDefault)
	hugeDefaultAnalysis := analyzeModulaSource(hugeDefault, len(hugeDefaultLines))
	if hugeDefaultAnalysis == nil || hugeDefaultAnalysis.tree != nil {
		t.Fatalf("overflow optional default analysis/tree = %#v, want non-concrete analysis",
			hugeDefaultAnalysis)
	}
	if got := modulaTestDefinitionSymbols(hugeDefaultAnalysis.definitions); !slices.Equal(
		got,
		[]string{"M", "Huge", "Tail"},
	) {
		t.Fatalf("overflow optional default definitions = %#v, want M/Huge/Tail", got)
	}

	for _, importPrefix := range []string{"IMPORT ", "FROM Source IMPORT "} {
		hugeImport := "MODULE M;\n" + importPrefix +
			strings.Repeat("Item, ", modulaMaximumConcreteTokens/2+16) +
			"Last;\nPROCEDURE Tail;\nBEGIN\nEND Tail;\nBEGIN\nEND M.\n"
		importLines := modulaTestLines(hugeImport)
		importAnalysis := analyzeModulaSource(hugeImport, len(importLines))
		if importAnalysis == nil || importAnalysis.tree != nil {
			t.Fatalf("overflow import %q analysis/tree = %#v, want non-concrete analysis",
				importPrefix, importAnalysis)
		}
		wantImports := []cLineSpan{{start: 2, end: 2}}
		for path, imports := range map[string][]cLineSpan{
			"fallback": analyzeModulaLexically(hugeImport, len(importLines)).imports,
			"merged":   importAnalysis.imports,
		} {
			if !reflect.DeepEqual(imports, wantImports) {
				t.Errorf("%s overflow import %q spans = %#v, want %#v",
					path, importPrefix, imports, wantImports)
			}
		}
	}

	lateRecord := "MODULE M;\nVAR " +
		strings.Repeat("item, ", modulaMaximumConcreteTokens/2+16) +
		"last: RECORD\nfield: T;\n" +
		"PROCEDURE Leaked;\nBEGIN\nEND Leaked;\nEND;\n" +
		"PROCEDURE Tail;\nBEGIN\nEND Tail;\nBEGIN\nEND M.\n"
	lateRecordLines := modulaTestLines(lateRecord)
	lateRecordDefinitions := analyzeModulaSource(
		lateRecord,
		len(lateRecordLines),
	).definitions
	lateRecordSymbols := modulaTestDefinitionSymbols(lateRecordDefinitions)
	if slices.Contains(lateRecordSymbols, "Leaked") ||
		!slices.Contains(lateRecordSymbols, "Tail") {
		t.Fatalf("late overflow RECORD definitions = %#v, want Tail without Leaked",
			lateRecordSymbols)
	}

	lineStartMarkers := "MODULE M;\nPROCEDURE Huge(\n" +
		strings.Repeat("VAR value: T;\n", modulaMaximumDeclarationTokens/4+32) +
		"last: T);\nBEGIN\nEND Huge;\n" +
		"PROCEDURE Tail;\nBEGIN\nEND Tail;\nBEGIN\nEND M.\n"
	markerLines := modulaTestLines(lineStartMarkers)
	markerDefinitions := analyzeModulaLexically(
		lineStartMarkers,
		len(markerLines),
	).definitions
	if got := modulaTestDefinitionSymbols(markerDefinitions); !slices.Equal(
		got,
		[]string{"M", "Huge", "Tail"},
	) {
		t.Fatalf("overflow header line-start markers definitions = %#v, want M/Huge/Tail", got)
	}
	if !modulaTestHasOwningDefinition(markerDefinitions, "M") ||
		!modulaTestHasOwningDefinition(markerDefinitions, "Huge") ||
		!modulaTestHasOwningDefinition(markerDefinitions, "Tail") {
		t.Fatalf("overflow header line-start markers damaged owners: %#v",
			markerDefinitions)
	}

	contextlessCase := "MODULE M;\nTYPE Broken = " +
		strings.Repeat("T + ", modulaMaximumDeclarationTokens+8) +
		"CASE\nPROCEDURE Tail;\nBEGIN\nEND Tail;\nBEGIN\nEND M.\n"
	caseLines := modulaTestLines(contextlessCase)
	caseDefinitions := analyzeModulaLexically(
		contextlessCase,
		len(caseLines),
	).definitions
	if got := modulaTestDefinitionSymbols(caseDefinitions); !slices.Equal(
		got,
		[]string{"M", "Tail"},
	) {
		t.Fatalf("contextless overflow CASE definitions = %#v, want M/Tail", got)
	}
}

func TestModulaOverflowOwnersAndControlsRetainBoundedNestingState(t *testing.T) {
	t.Parallel()

	var owners strings.Builder
	owners.WriteString("MODULE M;\n")
	const retainedProcedures = modulaMaximumStructuralDepth - 2
	for index := range retainedProcedures {
		fmt.Fprintf(&owners, "PROCEDURE Owner%d;\n", index)
	}
	owners.WriteString("PROCEDURE Forward;\nFORWARD;\n")
	owners.WriteString("PROCEDURE Tail;\nBEGIN\nEND Tail;\n")
	for index := retainedProcedures - 1; index >= 0; index-- {
		fmt.Fprintf(&owners, "BEGIN\nEND Owner%d;\n", index)
	}
	owners.WriteString("BEGIN\nEND M.\n")
	ownerSource := owners.String()
	ownerLines := modulaTestLines(ownerSource)
	ownerAnalysis := analyzeModulaLexically(ownerSource, len(ownerLines))
	ownerSymbols := modulaTestDefinitionSymbols(ownerAnalysis.definitions)
	for _, want := range []string{"Forward", "Tail"} {
		if !slices.Contains(ownerSymbols, want) {
			t.Errorf("overflow/forward recovery lost %q among %d definitions",
				want, len(ownerSymbols))
		}
	}
	modulaTestAssertDefinitionCoordinates(t, ownerLines, ownerAnalysis.definitions)

	var strayForward strings.Builder
	strayForward.WriteString("MODULE M;\n")
	for index := range retainedProcedures {
		fmt.Fprintf(&strayForward, "PROCEDURE Parent%d;\n", index)
	}
	strayForward.WriteString("MODULE Overflow;\nFORWARD;\nEND Overflow;\n")
	strayForward.WriteString("PROCEDURE Tail;\nBEGIN\nEND Tail;\n")
	for index := retainedProcedures - 1; index >= 0; index-- {
		fmt.Fprintf(&strayForward, "BEGIN\nEND Parent%d;\n", index)
	}
	strayForward.WriteString("BEGIN\nEND M.\n")
	straySource := strayForward.String()
	strayLines := modulaTestLines(straySource)
	strayDefinitions := analyzeModulaLexically(straySource, len(strayLines)).definitions
	if !slices.Contains(modulaTestDefinitionSymbols(strayDefinitions), "Tail") {
		t.Fatalf("stray overflow FORWARD lost Tail among %d definitions",
			len(strayDefinitions))
	}
	if !modulaTestHasOwningDefinition(
		strayDefinitions,
		fmt.Sprintf("Parent%d", retainedProcedures-1),
	) {
		t.Fatalf("stray overflow FORWARD unwound retained parent among %d definitions",
			len(strayDefinitions))
	}

	var interveningForward strings.Builder
	interveningForward.WriteString("MODULE M;\n")
	for index := range retainedProcedures {
		fmt.Fprintf(&interveningForward, "PROCEDURE Parent%d;\n", index)
	}
	interveningForward.WriteString(
		"PROCEDURE Overflow;\nCONST X = 1;\nFORWARD;\nEND Overflow;\n",
	)
	interveningForward.WriteString("PROCEDURE Tail;\nBEGIN\nEND Tail;\n")
	for index := retainedProcedures - 1; index >= 0; index-- {
		fmt.Fprintf(&interveningForward, "BEGIN\nEND Parent%d;\n", index)
	}
	interveningForward.WriteString("BEGIN\nEND M.\n")
	interveningSource := interveningForward.String()
	interveningLines := modulaTestLines(interveningSource)
	interveningDefinitions := analyzeModulaLexically(
		interveningSource,
		len(interveningLines),
	).definitions
	if !slices.Contains(modulaTestDefinitionSymbols(interveningDefinitions), "Tail") {
		t.Fatalf("intervening overflow declaration lost Tail among %d definitions",
			len(interveningDefinitions))
	}
	for _, owner := range []string{
		"M", fmt.Sprintf("Parent%d", retainedProcedures-1),
	} {
		if !modulaTestHasOwningDefinition(interveningDefinitions, owner) {
			t.Errorf("intervening overflow FORWARD unwound %s among %d definitions",
				owner, len(interveningDefinitions))
		}
	}

	for _, incomplete := range []string{"PROCEDURE Phantom", "MODULE Phantom"} {
		var eofOverflow strings.Builder
		eofOverflow.WriteString("MODULE M;\n")
		for index := range retainedProcedures {
			fmt.Fprintf(&eofOverflow, "PROCEDURE Parent%d;\n", index)
		}
		eofOverflow.WriteString("PROCEDURE Overflow;\n")
		eofOverflow.WriteString(incomplete)
		eofSource := eofOverflow.String()
		eofLines := modulaTestLines(eofSource)
		eofSymbols := modulaTestDefinitionSymbols(analyzeModulaLexically(
			eofSource,
			len(eofLines),
		).definitions)
		if slices.Contains(eofSymbols, "Phantom") {
			t.Errorf("owner-overflow EOF %q leaked Phantom: %#v", incomplete, eofSymbols)
		}
		for _, want := range []string{
			"M", fmt.Sprintf("Parent%d", retainedProcedures-1), "Overflow",
		} {
			if !slices.Contains(eofSymbols, want) {
				t.Errorf("owner-overflow EOF %q lost %s among %d definitions",
					incomplete, want, len(eofSymbols))
			}
		}
	}

	var controls strings.Builder
	controls.WriteString("MODULE M;\nBEGIN\n")
	for range modulaMaximumStructuralDepth + 1 {
		controls.WriteString("IF TRUE THEN\n")
	}
	for range modulaMaximumStructuralDepth + 1 {
		controls.WriteString("END;\n")
	}
	controls.WriteString("END M.\n")
	controlSource := controls.String()
	controlLines := modulaTestLines(controlSource)
	controlScopes := analyzeModulaLexically(controlSource, len(controlLines)).scopes
	for _, want := range []cLineScope{
		{start: 3, end: 2*modulaMaximumStructuralDepth + 4},
		{start: modulaMaximumStructuralDepth + 2, end: modulaMaximumStructuralDepth + 5},
	} {
		if !slices.Contains(controlScopes, want) {
			t.Errorf("bounded control scopes do not contain %#v: %#v", want, controlScopes)
		}
	}
	for _, wrong := range []cLineScope{
		{start: 3, end: 2*modulaMaximumStructuralDepth + 3},
		{start: modulaMaximumStructuralDepth + 2, end: modulaMaximumStructuralDepth + 4},
	} {
		if slices.Contains(controlScopes, wrong) {
			t.Errorf("overflow END prematurely closed retained scope %#v", wrong)
		}
	}

	var repeats strings.Builder
	repeats.WriteString("MODULE M;\nBEGIN\n")
	for range modulaMaximumStructuralDepth + 1 {
		repeats.WriteString("REPEAT\n")
	}
	for range modulaMaximumStructuralDepth + 1 {
		repeats.WriteString("UNTIL TRUE\n")
	}
	repeats.WriteString("END M.\n")
	repeatSource := repeats.String()
	repeatLines := modulaTestLines(repeatSource)
	repeatScopes := analyzeModulaLexically(repeatSource, len(repeatLines)).scopes
	for _, want := range []cLineScope{
		{start: 3, end: 2*modulaMaximumStructuralDepth + 4},
		{start: modulaMaximumStructuralDepth + 2, end: modulaMaximumStructuralDepth + 5},
	} {
		if !slices.Contains(repeatScopes, want) {
			t.Errorf("bounded REPEAT scopes do not contain %#v: %#v", want, repeatScopes)
		}
	}

	var contaminated strings.Builder
	contaminated.WriteString("MODULE M;\nPROCEDURE Broken;\nBEGIN\n")
	for range modulaMaximumStructuralDepth + 1 {
		contaminated.WriteString("IF TRUE THEN\n")
	}
	contaminated.WriteString("END Broken;\n")
	contaminated.WriteString("PROCEDURE Tail;\nBEGIN\nIF TRUE THEN\nEND;\nEND Tail;\n")
	contaminated.WriteString("BEGIN\nEND M.\n")
	contaminatedSource := contaminated.String()
	contaminatedLines := modulaTestLines(contaminatedSource)
	contaminatedScopes := analyzeModulaLexically(
		contaminatedSource,
		len(contaminatedLines),
	).scopes
	tailIfLine := modulaMaximumStructuralDepth + 8
	wantTailIf := cLineScope{start: tailIfLine, end: tailIfLine + 1}
	if !slices.Contains(contaminatedScopes, wantTailIf) {
		t.Errorf("stale control overflow contaminated Tail scope %#v: %#v",
			wantTailIf, contaminatedScopes)
	}
}
