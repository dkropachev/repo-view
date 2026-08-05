package repoview

import (
	"slices"
	"strings"
	"testing"
)

func TestModulaDefinitionForAcceptsGNUStringMacrosOnly(t *testing.T) {
	t.Parallel()

	for _, macro := range []string{"__FILE__", "__DATE__", "__FUNCTION__"} {
		t.Run(macro, func(t *testing.T) {
			t.Parallel()
			source := "DEFINITION MODULE FOR " + macro + " Foreign;\nEND Foreign.\n"
			lines := modulaTestLines(source)
			lexed := lexModula(source)
			if !modulaContentGate(lexed) || !lexed.concreteEligible {
				t.Fatalf("GNU string macro %s failed the concrete content gate", macro)
			}
			tree := modulaTreeTestParse(t, source)
			if spans := modulaSyntaxErrorSpans(tree, len(source)); len(spans) != 0 {
				t.Fatalf("GNU string macro %s recovery spans = %#v, want none",
					macro, spans)
			}
			analysis := analyzeModulaSource(source, len(lines))
			if analysis == nil || !analysis.gated || analysis.tree == nil {
				t.Fatalf("GNU string macro %s analysis = %#v", macro, analysis)
			}
			for path, definitions := range map[string][]sourceDefinition{
				"concrete": modulaTreeDefinitions(source, len(lines), tree),
				"fallback": analyzeModulaLexically(source, len(lines)).definitions,
				"merged":   analysis.definitions,
			} {
				if got, want := modulaTestDefinitionSymbols(definitions),
					[]string{"Foreign"}; !slices.Equal(got, want) {
					t.Errorf("%s %s definitions = %#v, want %#v", path, macro, got, want)
				}
				modulaTestAssertDefinitionCoordinates(t, lines, definitions)
			}
		})
	}

	for _, macro := range []string{"__LINE__", "__COLUMN__"} {
		t.Run(macro, func(t *testing.T) {
			t.Parallel()
			source := "DEFINITION MODULE FOR " + macro + " Foreign;\nEND Foreign.\n"
			lexed := lexModula(source)
			if modulaContentGate(lexed) {
				t.Fatalf("GNU integer macro %s passed a string-only content gate", macro)
			}
			analysis := analyzeModulaSource(source, len(modulaTestLines(source)))
			if analysis == nil || analysis.gated || len(analysis.definitions) != 0 {
				t.Fatalf("GNU integer macro %s analysis = %#v, want inert source",
					macro, analysis)
			}
		})
	}
}

func TestModulaRepeatScopeIncludesMultilineUntilCondition(t *testing.T) {
	t.Parallel()

	const source = `MODULE RepeatScope;
PROCEDURE Work;
BEGIN
  REPEAT
    Work
  UNTIL ready
    AND more
END Work;
BEGIN
END RepeatScope.
`
	lines := modulaTestLines(source)
	tree := modulaTreeTestParse(t, source)
	if spans := modulaSyntaxErrorSpans(tree, len(source)); len(spans) != 0 {
		t.Fatalf("multiline UNTIL recovery spans = %#v, want none", spans)
	}
	repeatLine := modulaTestLineContaining(t, lines, "REPEAT")
	conditionEnd := modulaTestLineContaining(t, lines, "AND more")
	want := cLineScope{start: repeatLine, end: conditionEnd}
	lexical := analyzeModulaLexically(source, len(lines))
	analysis := analyzeModulaSource(source, len(lines))
	if analysis == nil || !analysis.gated {
		t.Fatalf("multiline UNTIL analysis = %#v", analysis)
	}
	for path, scopes := range map[string][]cLineScope{
		"concrete": modulaTreeScopes(source, len(lines), tree),
		"fallback": lexical.scopes,
		"merged":   analysis.scopes,
	} {
		if !slices.Contains(scopes, want) {
			t.Errorf("%s scopes do not contain multiline UNTIL scope %#v: %#v",
				path, want, scopes)
		}
		if slices.Contains(scopes, cLineScope{start: repeatLine, end: conditionEnd - 1}) {
			t.Errorf("%s retained the prematurely closed UNTIL scope: %#v", path, scopes)
		}
	}
}

func TestModulaOfficialDeclarationGrammarConcreteAndFallback(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		source    string
		want      []string
		forbidden []string
	}{
		{
			name: "foreign definition and builtin procedures",
			source: `DEFINITION MODULE FOR "C" Foreign;
TYPE Address;
PROCEDURE __BUILTIN__ Size(value: Address): CARDINAL;
PROCEDURE __INLINE__ Fast(value: CARDINAL);
END Foreign.
`,
			want:      []string{"Foreign", "Address", "Size", "Fast"},
			forbidden: []string{"C", "value", "CARDINAL"},
		},
		{
			name: "attribute builtin implementation procedure",
			source: `IMPLEMENTATION MODULE Builtins;
PROCEDURE __ATTRIBUTE__ __BUILTIN__ ((__builtin_sqrt)) sqrt(x: REAL): REAL;
BEGIN
  RETURN x
END sqrt;
BEGIN
END Builtins.
`,
			want:      []string{"Builtins", "sqrt"},
			forbidden: []string{"__builtin_sqrt", "x", "REAL"},
		},
		{
			name: "module priority indexed variables and repeated sections",
			source: `MODULE Priority[1 + 2];
CONST First = 1;
VAR indexed[First], next: INTEGER;
TYPE Range = [1..10];
CONST Second = 2;
VAR last: Range;
TYPE Flags = PACKEDSET OF [0..15];
BEGIN
END Priority.
`,
			want: []string{"Priority", "First", "indexed", "next", "Range", "Second", "last", "Flags"},
		},
		{
			name: "record variants with optional tag forms",
			source: `MODULE Variants;
TYPE
  Tagged = RECORD
    CASE kind: BOOLEAN OF
      TRUE: yes: INTEGER |
      FALSE: no: INTEGER
    END
  END;
	  TypedOnly = RECORD
	    CASE : BOOLEAN OF
	      TRUE: enabled: INTEGER |
	      FALSE: disabled: INTEGER
	    END
	  END;
	  IdentifierOnly = RECORD
	    CASE state OF
	      0: inactive: INTEGER |
	      1: active: INTEGER
	    END
	  END;
	  EnumSet = SET OF (cold, hot);
	  Untagged = RECORD
    CASE OF
      0: zero: INTEGER |
      1: one: INTEGER
    END
  END;
BEGIN
END Variants.
`,
			want: []string{
				"Variants", "Tagged", "kind", "yes", "no", "TypedOnly", "enabled",
				"disabled", "IdentifierOnly", "state", "inactive", "active", "EnumSet",
				"cold", "hot", "Untagged", "zero", "one",
			},
			forbidden: []string{"BOOLEAN", "TRUE", "FALSE", "INTEGER"},
		},
		{
			name: "anonymous variable record and nested enumeration members",
			source: `MODULE Anonymous;
	VAR
	  item: RECORD
	    field: INTEGER;
	    CASE state OF
	      0: payload: SET OF (empty, full)
	    END
	  END;
	BEGIN
	END Anonymous.
	`,
			want: []string{
				"Anonymous", "item", "field", "state", "payload", "empty", "full",
			},
			forbidden: []string{"INTEGER"},
		},
		{
			name: "type field and procedure pragmas",
			source: `MODULE Attributes;
TYPE
  Entry = RECORD <* align(8) *>
    value: INTEGER <* volatile(1) *>
  END <* align(8) *>;
PROCEDURE __INLINE__ Work(value: Entry) <* checked *>;
BEGIN
END Work;
BEGIN
END Attributes.
`,
			want:      []string{"Attributes", "Entry", "value", "Work"},
			forbidden: []string{"align", "volatile", "inline", "checked", "INTEGER"},
		},
		{
			name: "local export unqualified and bodyless procedure",
			source: `MODULE Exports;
MODULE Local[3];
EXPORT
  UNQUALIFIED
  Stop, Value;
CONST Value = 1;
PROCEDURE Stop;
END Stop;
BEGIN
END Local;
BEGIN
END Exports.
`,
			want:      []string{"Exports", "Local", "Value", "Stop"},
			forbidden: []string{"UNQUALIFIED"},
		},
		{
			name: "extended signatures and remaining composite types",
			source: `DEFINITION MODULE Signatures;
TYPE
  Values = ARRAY [0..10] OF INTEGER;
  Flags = SET OF [0..31];
  Link = POINTER TO Values;
PROCEDURE Variadic(...);
PROCEDURE Optional([count: CARDINAL = 1]);
PROCEDURE Arrays(VAR data: ARRAY OF CHAR): [INTEGER];
END Signatures.
`,
			want:      []string{"Signatures", "Values", "Flags", "Link", "Variadic", "Optional", "Arrays"},
			forbidden: []string{"count", "data", "CARDINAL", "CHAR", "INTEGER"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			lines := modulaTestLines(test.source)
			tree := modulaTreeTestParse(t, test.source)
			if spans := modulaSyntaxErrorSpans(tree, len(test.source)); len(spans) != 0 {
				t.Fatalf("official syntax recovery spans = %#v, want none", spans)
			}

			concrete := modulaTreeDefinitions(test.source, len(lines), tree)
			if got := modulaTestDefinitionSymbols(concrete); !slices.Equal(got, test.want) {
				t.Fatalf("concrete definitions = %#v, want %#v", got, test.want)
			}
			fallback := analyzeModulaLexically(test.source, len(lines)).definitions
			if got := modulaTestDefinitionSymbols(fallback); !slices.Equal(got, test.want) {
				t.Fatalf("fallback definitions = %#v, want %#v", got, test.want)
			}
			for _, definitions := range [][]sourceDefinition{concrete, fallback} {
				for _, forbidden := range test.forbidden {
					if slices.Contains(modulaTestDefinitionSymbols(definitions), forbidden) {
						t.Errorf("non-declaration %q became definition: %#v", forbidden, definitions)
					}
				}
				modulaTestAssertDefinitionCoordinates(t, lines, definitions)
			}
		})
	}
}

func TestModulaOfficialNumericAndRawStringLexemes(t *testing.T) {
	t.Parallel()

	const source = `MODULE Lexemes;
CONST
  Exponent = 1.25E+3;
  Octal = 377B;
  Character = 141C;
  Hexadecimal = 0FFH;
  CommentOpen = "(* not a comment";
  CommentClose = '*) not a close';
  DirectiveOpen = "<* not a pragma *>";
TYPE Range = [1..10];
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END Lexemes.
`
	lines := modulaTestLines(source)
	lexed := lexModula(source)
	if !lexed.concreteEligible {
		t.Fatal("small official lexeme fixture is not concrete-eligible")
	}
	tree := modulaTreeTestParse(t, source)
	if spans := modulaSyntaxErrorSpans(tree, len(source)); len(spans) != 0 {
		t.Fatalf("official lexeme recovery spans = %#v, want none", spans)
	}
	want := []string{
		"Lexemes", "Exponent", "Octal", "Character", "Hexadecimal",
		"CommentOpen", "CommentClose", "DirectiveOpen", "Range", "Tail",
	}
	for name, definitions := range map[string][]sourceDefinition{
		"concrete": modulaTreeDefinitions(source, len(lines), tree),
		"fallback": analyzeModulaLexically(source, len(lines)).definitions,
	} {
		if got := modulaTestDefinitionSymbols(definitions); !slices.Equal(got, want) {
			t.Errorf("%s definitions = %#v, want %#v", name, got, want)
		}
		modulaTestAssertDefinitionCoordinates(t, lines, definitions)
	}
}

func TestModulaGNULeadingDotRealsRemainSingleNumericTokens(t *testing.T) {
	t.Parallel()

	const source = `MODULE Reals;
CONST
  LeadingFraction = .5;
  LeadingExponent = .E+2;
  IntegerExponent = 1.E+2;
TYPE ClosedRange = [1..10];
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END Reals.
`
	lexed := lexModula(source)
	if !lexed.concreteEligible {
		t.Fatal("small GNU-real fixture is not concrete-eligible")
	}
	wantNumbers := map[string]int{".5": 1, ".E+2": 1, "1.E+2": 1}
	for _, token := range lexed.tokens {
		if token.kind == modulaTokenNumber {
			if _, tracked := wantNumbers[token.text]; tracked {
				wantNumbers[token.text]--
			}
		}
		if token.kind == modulaTokenIdentifier && token.text == "E" {
			t.Errorf("exponent marker split into identifier token: %#v", token)
		}
	}
	for number, remaining := range wantNumbers {
		if remaining != 0 {
			t.Errorf("numeric token %q count delta = %d, want zero", number, remaining)
		}
	}
	if !modulaTestHasToken(lexed.tokens, "..", modulaTokenPunctuation) {
		t.Fatal("closed range lost its '..' punctuation token")
	}
	if !modulaTestHasToken(lexed.tokens, ".", modulaTokenPunctuation) {
		t.Fatal("module terminator lost its standalone '.' punctuation token")
	}

	lines := modulaTestLines(source)
	tree := modulaTreeTestParse(t, source)
	if spans := modulaSyntaxErrorSpans(tree, len(source)); len(spans) != 0 {
		t.Fatalf("GNU-real recovery spans = %#v, want none", spans)
	}
	want := []string{
		"Reals", "LeadingFraction", "LeadingExponent", "IntegerExponent",
		"ClosedRange", "Tail",
	}
	for name, definitions := range map[string][]sourceDefinition{
		"concrete": modulaTreeDefinitions(source, len(lines), tree),
		"fallback": analyzeModulaLexically(source, len(lines)).definitions,
	} {
		if got := modulaTestDefinitionSymbols(definitions); !slices.Equal(got, want) {
			t.Errorf("%s GNU-real definitions = %#v, want %#v", name, got, want)
		}
	}

	root := t.TempDir()
	writeFile(t, root, "Reals.mod", source)
	found, err := mustView(t, root).Find("E", Options{
		Include: IncludeRefs,
		Return:  ReturnLocations,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found.Results) != 0 {
		t.Fatalf("numeric exponent marker became searchable identifier: %#v", found.Results)
	}
}

func TestModulaGNUCPPLineMarkersAreLineLeadingTriviaOnly(t *testing.T) {
	t.Parallel()

	const source = `# arbitrary preprocessing text
# 1 "source.mod"
MODULE LineMarkers;
# 20 "source.mod" 2 3
CONST Different = 1 # 2;
# 30 "generated.mod" 1 3 4
PROCEDURE Tail;
BEGIN
  IF 1 # 2 THEN
    Tail
  END
END Tail;
BEGIN
END LineMarkers.
`
	lexed := lexModula(source)
	if !lexed.concreteEligible {
		t.Fatal("valid CPP-line-marker fixture is not concrete-eligible")
	}
	hashTokens := 0
	for _, token := range lexed.tokens {
		if token.text == "#" {
			hashTokens++
		}
	}
	if hashTokens != 2 {
		t.Fatalf("inline relation hash tokens = %d, want 2; line markers must be trivia", hashTokens)
	}
	if len(lexed.tokens) == 0 || lexed.tokens[0].text != "MODULE" {
		t.Fatalf("first logical token after line marker = %#v, want MODULE", lexed.tokens)
	}

	lines := modulaTestLines(source)
	tree := modulaTreeTestParse(t, source)
	if spans := modulaSyntaxErrorSpans(tree, len(source)); len(spans) != 0 {
		t.Fatalf("CPP-line-marker recovery spans = %#v, want none", spans)
	}
	want := []string{"LineMarkers", "Different", "Tail"}
	for name, definitions := range map[string][]sourceDefinition{
		"concrete": modulaTreeDefinitions(source, len(lines), tree),
		"fallback": analyzeModulaLexically(source, len(lines)).definitions,
	} {
		if got := modulaTestDefinitionSymbols(definitions); !slices.Equal(got, want) {
			t.Errorf("%s line-marker definitions = %#v, want %#v", name, got, want)
		}
		modulaTestAssertDefinitionCoordinates(t, lines, definitions)
	}
}

func TestModulaModuleFinallyWithoutBeginIsAValidBlock(t *testing.T) {
	t.Parallel()

	const source = `MODULE FinalOnly;
PROCEDURE Owner;
BEGIN
END Owner;
PROCEDURE Tail;
BEGIN
END Tail;
FINALLY
  Cleanup
END FinalOnly.
`
	lines := modulaTestLines(source)
	tree := modulaTreeTestParse(t, source)
	if spans := modulaSyntaxErrorSpans(tree, len(source)); len(spans) != 0 {
		t.Fatalf("FINALLY-only module recovery spans = %#v, want none", spans)
	}
	want := []string{"FinalOnly", "Owner", "Tail"}
	for name, definitions := range map[string][]sourceDefinition{
		"concrete": modulaTreeDefinitions(source, len(lines), tree),
		"fallback": analyzeModulaLexically(source, len(lines)).definitions,
	} {
		if got := modulaTestDefinitionSymbols(definitions); !slices.Equal(got, want) {
			t.Errorf("%s FINALLY-only definitions = %#v, want %#v", name, got, want)
		}
		if slices.Contains(modulaTestDefinitionSymbols(definitions), "Cleanup") {
			t.Errorf("%s FINALLY call became definition: %#v", name, definitions)
		}
	}
}

func TestModulaGNUAlternatePunctuationCanonicalizesConcreteAndFallback(t *testing.T) {
	t.Parallel()

	const source = `MODULE Alternate(!2!);
CONST
  Limit = 4;
  Negated = ~FALSE;
  Combined = TRUE & TRUE;
  SetValue = (: 1, 2 :);
TYPE
  NodePtr = POINTER TO Node;
  Node = RECORD
    value: INTEGER;
    CASE tag: BOOLEAN OF
      TRUE: next: NodePtr !
      FALSE: prior: NodePtr
    END
  END;
VAR indexed(!Limit!), current: Node;
BEGIN
  current@.value := Limit
END Alternate.
`
	lines := modulaTestLines(source)
	lexed := lexModula(source)
	if !lexed.concreteEligible {
		t.Fatal("small alternate-punctuation fixture is not concrete-eligible")
	}
	canonical := make(map[string]int)
	for _, token := range lexed.tokens {
		canonical[token.text]++
	}
	for _, want := range []string{"[", "]", "{", "}", "^", "|", "NOT", "&"} {
		if canonical[want] == 0 {
			t.Errorf("canonical token %q absent from alternate source: %#v", want, canonical)
		}
	}
	for _, raw := range []string{"(!", "!)", "(:", ":)", "@", "!", "~"} {
		if canonical[raw] != 0 {
			t.Errorf("raw alternate token %q survived canonicalization: %#v", raw, canonical)
		}
	}

	tree := modulaTreeTestParse(t, source)
	if spans := modulaSyntaxErrorSpans(tree, len(source)); len(spans) != 0 {
		t.Fatalf("alternate-punctuation recovery spans = %#v, want none", spans)
	}
	want := []string{
		"Alternate", "Limit", "Negated", "Combined", "SetValue", "NodePtr",
		"Node", "value", "tag", "next", "prior", "indexed", "current",
	}
	for name, definitions := range map[string][]sourceDefinition{
		"concrete": modulaTreeDefinitions(source, len(lines), tree),
		"fallback": analyzeModulaLexically(source, len(lines)).definitions,
	} {
		if got := modulaTestDefinitionSymbols(definitions); !slices.Equal(got, want) {
			t.Errorf("%s alternate definitions = %#v, want %#v", name, got, want)
		}
		for _, use := range []string{"TRUE", "FALSE", "INTEGER", "BOOLEAN"} {
			if slices.Contains(modulaTestDefinitionSymbols(definitions), use) {
				t.Errorf("%s alternate use %q became definition: %#v", name, use, definitions)
			}
		}
		modulaTestAssertDefinitionCoordinates(t, lines, definitions)
	}
}

func TestModulaRawStringsDoNotUseBackslashOrDoubledQuoteEscapes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		prefix string
	}{
		{name: "double quote after backslash", prefix: `CONST Text = "slash\";`},
		{name: "single quote after backslash", prefix: `CONST Text = 'slash\';`},
		{name: "adjacent double quoted strings", prefix: `CONST Text = "left""right";`},
		{name: "adjacent single quoted strings", prefix: `CONST Text = 'left''right';`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			source := "MODULE Raw;\n" + test.prefix + `
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END Raw.
`
			lines := modulaTestLines(source)
			lexed := lexModula(source)
			if len(lexed.stringSpans) == 0 {
				t.Fatal("raw string fixture has no string span")
			}
			definitions := analyzeModulaLexically(source, len(lines)).definitions
			if !slices.Contains(modulaTestDefinitionSymbols(definitions), "Tail") {
				t.Fatalf("raw quote handling swallowed independent Tail: %#v", definitions)
			}
			modulaTestAssertDefinitionCoordinates(t, lines, definitions)
		})
	}
}

func TestModulaTopLevelDirectivesRemainSyntaxTokens(t *testing.T) {
	t.Parallel()

	const paired = `MODULE Directives;
TYPE Aligned = INTEGER <* bytealignment(8) *>;
END Directives.
`
	lexed := lexModula(paired)
	if !lexed.concreteEligible {
		t.Fatal("small paired directive is not concrete-eligible")
	}
	if len(lexed.pragmaSpans) != 0 {
		t.Fatalf("top-level directive was classified as trivia: %#v", lexed.pragmaSpans)
	}
	for _, want := range []struct {
		text string
		kind modulaTokenKind
	}{
		{text: "<*", kind: modulaTokenPunctuation},
		{text: "bytealignment", kind: modulaTokenIdentifier},
		{text: "*>", kind: modulaTokenPunctuation},
	} {
		if !modulaTestHasToken(lexed.tokens, want.text, want.kind) {
			t.Errorf("directive token %q (%d) missing from %#v", want.text, want.kind, lexed.tokens)
		}
	}

	// GNU's depth-one comment-directive substate treats the inner (* as
	// payload and the first *) as recovery for the surrounding comment. The
	// final *> is therefore visible and closes the top-level directive.
	const commentSubstate = `<* outer (* <* (* *) *>`
	lexed = lexModula(commentSubstate)
	if len(lexed.tokens) == 0 || lexed.tokens[0].text != "<*" ||
		!lexed.tokens[0].directiveClosed ||
		!modulaTestHasToken(lexed.tokens, "*>", modulaTokenPunctuation) {
		t.Fatalf("comment-directive substate hid visible close: %#v", lexed.tokens)
	}

	const unmatched = `MODULE Open;
<* broken
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END Open.
`
	lexed = lexModula(unmatched)
	if !lexed.concreteEligible {
		t.Fatal("unmatched directive opener was treated as opaque-to-EOF")
	}
	if !modulaTestHasToken(lexed.tokens, "Tail", modulaTokenIdentifier) {
		t.Fatalf("unmatched directive opener swallowed later declarations: %#v", lexed.tokens)
	}
}

func TestModulaNestedCommentsAndDirectiveTokensUseIndependentStates(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		body      string
		forbidden []string
	}{
		{
			name: "nested comments contain well formed pragma",
			body: `(* outer
  (* inner <* payload *> PROCEDURE HiddenInner; *)
  PROCEDURE HiddenOuter;
*)`,
			forbidden: []string{"HiddenInner", "HiddenOuter", "payload"},
		},
		{
			name:      "malformed pragma closes comment at comment terminator",
			body:      `(* <* malformed *)`,
			forbidden: []string{"malformed"},
		},
		{
			name:      "comment markers inside pragma",
			body:      `<* text "(*" more "*)" *>`,
			forbidden: []string{"text", "more"},
		},
		{
			name:      "pragma close marker inside string",
			body:      `<* note("*> PROCEDURE Hidden;") *>`,
			forbidden: []string{"note", "Hidden"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			source := "MODULE States;\n" + test.body + `
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END States.
`
			lines := modulaTestLines(source)
			definitions := analyzeModulaLexically(source, len(lines)).definitions
			symbols := modulaTestDefinitionSymbols(definitions)
			for _, required := range []string{"States", "Tail"} {
				if !slices.Contains(symbols, required) {
					t.Errorf("opaque-state recovery lost %q: %#v", required, definitions)
				}
			}
			for _, forbidden := range test.forbidden {
				if slices.Contains(symbols, forbidden) {
					t.Errorf("opaque state promoted %q: %#v", forbidden, definitions)
				}
			}
			modulaTestAssertDefinitionCoordinates(t, lines, definitions)
		})
	}
}

func TestModulaProcedureExceptModuleFinallyAndAsmAreNotDeclarations(t *testing.T) {
	t.Parallel()

	const source = `MODULE Finalization;
PROCEDURE Guarded;
BEGIN
  ASM VOLATILE ("nop");
  Work
EXCEPT
  Recover
END Guarded;

BEGIN
  Guarded
FINALLY
  Cleanup
END Finalization.
`
	lines := modulaTestLines(source)
	tree := modulaTreeTestParse(t, source)
	if spans := modulaSyntaxErrorSpans(tree, len(source)); len(spans) != 0 {
		t.Fatalf("EXCEPT/FINALLY/ASM recovery spans = %#v, want none", spans)
	}
	want := []string{"Finalization", "Guarded"}
	for name, definitions := range map[string][]sourceDefinition{
		"concrete": modulaTreeDefinitions(source, len(lines), tree),
		"fallback": analyzeModulaLexically(source, len(lines)).definitions,
	} {
		if got := modulaTestDefinitionSymbols(definitions); !slices.Equal(got, want) {
			t.Errorf("%s definitions = %#v, want %#v", name, got, want)
		}
		for _, call := range []string{"ASM", "VOLATILE", "Work", "Recover", "Cleanup"} {
			if slices.Contains(modulaTestDefinitionSymbols(definitions), call) {
				t.Errorf("%s statement %q became definition: %#v", name, call, definitions)
			}
		}
	}
}

func TestModulaAllControlScopesAndNestedNamedEndsStayDisambiguated(t *testing.T) {
	t.Parallel()

	const source = `MODULE Nesting;
TYPE Choice = RECORD
  CASE tag: BOOLEAN OF
    TRUE: value: INTEGER |
    FALSE: other: INTEGER
  END
END;

MODULE Inner;
PROCEDURE Work(item: Choice);
BEGIN
  IF item.tag THEN
    FOR index := 1 TO 10 BY 2 DO
      WITH item DO
        WHILE value > 0 DO
          value := value - 1
        END
      END
    END
  END;
  REPEAT
    item.value := item.value + 1
  UNTIL item.value = 10;
  LOOP
    EXIT
  END
END Work;
BEGIN
END Inner;

BEGIN
END Nesting.
`
	lines := modulaTestLines(source)
	backend := prepareLanguageBackend(newModulaLanguage(), lines)
	definitions := backend.sourceDefinitions(lines)
	want := []string{"Nesting", "Choice", "tag", "value", "other", "Inner", "Work"}
	if got := modulaTestDefinitionSymbols(definitions); !slices.Equal(got, want) {
		t.Fatalf("nested definitions = %#v, want %#v", got, want)
	}
	for _, closingOrBinding := range []string{"index", "item"} {
		if slices.Contains(modulaTestDefinitionSymbols(definitions), closingOrBinding) {
			t.Errorf("control binding/close %q became definition: %#v", closingOrBinding, definitions)
		}
	}

	workStart := modulaTestLineContaining(t, lines, "PROCEDURE Work")
	workEnd := modulaTestLineContaining(t, lines, "END Work")
	forBody := modulaTestLineContaining(t, lines, "WITH item DO")
	resolver := backend.(navigationScopeResolver)
	if start, end := resolver.navigationScope(lines, forBody); start != workStart || end != workEnd {
		t.Fatalf("nested control navigation = %d-%d, want Work %d-%d",
			start, end, workStart, workEnd)
	}
	if got := scopeName(lines, forBody, backend); got != "Work" {
		t.Fatalf("nested control scope name = %q, want Work", got)
	}
	modulaTestAssertDefinitionCoordinates(t, lines, definitions)
}

func TestModulaMismatchedNamedEndsAreRecoveryNotDefinitions(t *testing.T) {
	t.Parallel()

	const source = `MODULE Names;
PROCEDURE Right;
BEGIN
END Wrong;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END Names.
`
	lines := modulaTestLines(source)
	tree := modulaTreeTestParseRecovery(t, source)
	if spans := modulaSyntaxErrorSpans(tree, len(source)); len(spans) == 0 {
		t.Fatal("mismatched END name has no recovery evidence")
	}
	for name, definitions := range map[string][]sourceDefinition{
		"concrete": modulaTreeDefinitions(source, len(lines), tree),
		"fallback": analyzeModulaLexically(source, len(lines)).definitions,
	} {
		symbols := modulaTestDefinitionSymbols(definitions)
		for _, want := range []string{"Names", "Right", "Tail"} {
			if !slices.Contains(symbols, want) {
				t.Errorf("%s mismatched close lost %q: %#v", name, want, definitions)
			}
		}
		if slices.Contains(symbols, "Wrong") {
			t.Errorf("%s closing name became definition: %#v", name, definitions)
		}
		right := modulaTestFirstDefinition(t, definitions, "Right")
		if right.ownsScope {
			t.Errorf("%s mismatched procedure has trusted owning scope: %#v", name, right)
		}
		tail := modulaTestFirstDefinition(t, definitions, "Tail")
		if !tail.ownsScope {
			t.Errorf("%s independent Tail lost owning scope: %#v", name, tail)
		}
		modulaTestAssertDefinitionCoordinates(t, lines, definitions)
	}
}

func TestModulaReservedBuiltinTokensCannotBecomeDeclarationNames(t *testing.T) {
	t.Parallel()

	const source = `MODULE Reserved;
CONST __BUILTIN__ = 1;
PROCEDURE __LINE__;
BEGIN
END __LINE__;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END Reserved.
`
	lines := modulaTestLines(source)
	tree := modulaTreeTestParseRecovery(t, source)
	if spans := modulaSyntaxErrorSpans(tree, len(source)); len(spans) == 0 {
		t.Fatal("reserved declaration names have no concrete recovery evidence")
	}
	for name, definitions := range map[string][]sourceDefinition{
		"concrete": modulaTreeDefinitions(source, len(lines), tree),
		"fallback": analyzeModulaLexically(source, len(lines)).definitions,
	} {
		symbols := modulaTestDefinitionSymbols(definitions)
		for _, want := range []string{"Reserved", "Tail"} {
			if !slices.Contains(symbols, want) {
				t.Errorf("%s reserved-name recovery lost %q: %#v", name, want, definitions)
			}
		}
		for _, forbidden := range []string{"__BUILTIN__", "__LINE__"} {
			if slices.Contains(symbols, forbidden) {
				t.Errorf("%s reserved token became definition %q: %#v",
					name, forbidden, definitions)
			}
		}
		modulaTestAssertDefinitionCoordinates(t, lines, definitions)
	}
}

func TestModulaNonASCIIDeclarationNamesRecoverToASCIITail(t *testing.T) {
	t.Parallel()

	const source = `MODULE ASCIIOnly;
CONST Δ = 1;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
  Tail
END ASCIIOnly.
`
	lines := modulaTestLines(source)
	tree := modulaTreeTestParseRecovery(t, source)
	if spans := modulaSyntaxErrorSpans(tree, len(source)); len(spans) == 0 {
		t.Fatal("non-ASCII declaration name has no concrete recovery evidence")
	}
	analysis := analyzeModulaSource(source, len(lines))
	if analysis == nil {
		t.Fatal("analyzeModulaSource returned nil")
	}
	symbols := modulaTestDefinitionSymbols(analysis.definitions)
	if slices.Contains(symbols, "Δ") {
		t.Fatalf("non-ASCII declaration became definition: %#v", analysis.definitions)
	}
	for _, want := range []string{"ASCIIOnly", "Tail"} {
		if !slices.Contains(symbols, want) {
			t.Errorf("non-ASCII recovery lost %q: %#v", want, analysis.definitions)
		}
	}
	modulaTestAssertDefinitionCoordinates(t, lines, analysis.definitions)

	root := t.TempDir()
	writeFile(t, root, "ASCIIOnly.mod", source)
	found, err := mustView(t, root).Find("Tail", Options{
		Include: IncludeBoth,
		Return:  ReturnLocations,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := modulaTestResultLines(found.Results), []int{3, 5, 7}; !slices.Equal(got, want) {
		t.Fatalf("ASCII Tail recovery/search lines = %#v, want %#v", got, want)
	}
}

func TestModulaMalformedUnmatchedControlTerminatorsKeepTail(t *testing.T) {
	t.Parallel()

	for _, terminator := range []string{"END;", "UNTIL ready;"} {
		t.Run(terminator, func(t *testing.T) {
			t.Parallel()
			source := `MODULE BrokenControl;
PROCEDURE Broken;
BEGIN
  ` + terminator + `
END Broken;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END BrokenControl.
`
			lines := modulaTestLines(source)
			analysis := analyzeModulaSource(source, len(lines))
			if analysis == nil {
				t.Fatal("analyzeModulaSource returned nil")
			}
			if !slices.Contains(modulaTestDefinitionSymbols(analysis.definitions), "Tail") {
				t.Fatalf("unmatched terminator swallowed Tail: %#v", analysis.definitions)
			}
			modulaTestAssertDefinitionCoordinates(t, lines, analysis.definitions)
		})
	}
}

func TestModulaLineTerminatorsBOMAndInvalidUTF8PreserveIndependentDeclarations(t *testing.T) {
	t.Parallel()

	base := "MODULE Encodings;\nCONST First = 1;\nPROCEDURE Tail;\nBEGIN\nEND Tail;\nBEGIN\nEND Encodings.\n"
	tests := []struct {
		name      string
		source    string
		lineCount int
	}{
		{name: "LF", source: base, lineCount: 7},
		{name: "CRLF", source: strings.ReplaceAll(base, "\n", "\r\n"), lineCount: 7},
		{name: "CR", source: strings.ReplaceAll(base, "\n", "\r"), lineCount: 1},
		{name: "initial BOM", source: "\ufeff" + base, lineCount: 7},
		{
			name: "invalid UTF-8 between declarations",
			source: "MODULE Encodings;\nCONST First = 1;\n(*" + string([]byte{0xff, 0xfe}) +
				"*)\nPROCEDURE Tail;\nBEGIN\nEND Tail;\nBEGIN\nEND Encodings.\n",
			lineCount: 8,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			analysis := analyzeModulaSource(test.source, test.lineCount)
			if analysis == nil {
				t.Fatal("analyzeModulaSource returned nil")
			}
			symbols := modulaTestDefinitionSymbols(analysis.definitions)
			for _, want := range []string{"Encodings", "First", "Tail"} {
				if !slices.Contains(symbols, want) {
					t.Errorf("encoding recovery lost %q: %#v", want, analysis.definitions)
				}
			}
			for _, definition := range analysis.definitions {
				if definition.line < 1 || definition.line > test.lineCount ||
					definition.scopeStart < 1 || definition.scopeEnd > test.lineCount {
					t.Errorf("definition outside %d-line source: %#v", test.lineCount, definition)
				}
			}
		})
	}
}

func TestModulaCommentsAreTokenSeparatorsNotIdentifierGlue(t *testing.T) {
	t.Parallel()

	const source = `MODULE Separators;
PROCEDURE(* between keyword and name *)Visible;
BEGIN
END Visible;
PRO(* cannot join a keyword *)CEDURE Phantom;
TYPE Na(* cannot join identifiers *)me = INTEGER;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END Separators.
`
	lines := modulaTestLines(source)
	definitions := analyzeModulaLexically(source, len(lines)).definitions
	symbols := modulaTestDefinitionSymbols(definitions)
	for _, want := range []string{"Separators", "Visible", "Tail"} {
		if !slices.Contains(symbols, want) {
			t.Errorf("comment-separator fixture lost %q: %#v", want, definitions)
		}
	}
	for _, forbidden := range []string{"Phantom", "Name", "Na", "me"} {
		if slices.Contains(symbols, forbidden) {
			t.Errorf("comment joined tokens into %q: %#v", forbidden, definitions)
		}
	}
	modulaTestAssertDefinitionCoordinates(t, lines, definitions)
}

func modulaTestHasToken(tokens []modulaToken, text string, kind modulaTokenKind) bool {
	for _, token := range tokens {
		if token.text == text && token.kind == kind {
			return true
		}
	}
	return false
}
