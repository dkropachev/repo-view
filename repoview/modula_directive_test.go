package repoview

import (
	"fmt"
	"slices"
	"strings"
	"testing"
)

func TestModulaGNUDirectiveValidGrammarPlacements(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		source string
		want   []string
	}{
		{
			name: "type and variable alignments",
			source: `MODULE Alignments;
TYPE
  Eight = INTEGER <* bytealignment(8) *>;
  Sixteen = ARRAY Eight OF INTEGER <* bytealignment(8 + 8) *>;
VAR
  first: Eight <* bytealignment(8) *>;
  second[1 + 1], third: Sixteen <* bytealignment(16) *>;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END Alignments.
`,
			want: []string{"Alignments", "Eight", "Sixteen", "first", "second", "third", "Tail"},
		},
		{
			name: "record default field list and type alignment",
			source: `MODULE Records;
TYPE Entry = RECORD <* recordalignment(8) *>
  first, second: INTEGER <* volatile, fieldalignment(4), checked(1 + 2) *>;
  flag: BOOLEAN <* packed *>
END <* bytealignment(16) *>;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END Records.
`,
			want: []string{"Records", "Entry", "first", "second", "flag", "Tail"},
		},
		{
			name: "implementation procedure and normal formal attributes",
			source: `IMPLEMENTATION MODULE ImplementationAttributes;
PROCEDURE Work(
  input: Data <* unused *>;
  VAR output, status: Data <* addressable *>
): Result <* noreturn *>;
BEGIN
END Work;
PROCEDURE Plain <* leaf *>;
BEGIN
END Plain;
BEGIN
END ImplementationAttributes.
`,
			want: []string{"ImplementationAttributes", "Work", "Plain"},
		},
		{
			name: "definition procedure and normal formal attributes",
			source: `DEFINITION MODULE DefinitionAttributes;
PROCEDURE __INLINE__ Work(
  input: Data <* unused *>;
  VAR output: Data <* addressable *>
): Result <* noreturn *>;
PROCEDURE __BUILTIN__ Plain <* leaf *>;
END DefinitionAttributes.
`,
			want: []string{"DefinitionAttributes", "Work", "Plain"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := modulaDirectiveTestAnalyze(t, test.source, false)
			modulaDirectiveTestAssertDefinitions(t, fixture, test.want, nil)
			modulaDirectiveTestAssertAllBounds(t, fixture)
		})
	}
}

func TestModulaRecoveryProcedureAttributeDirectiveValidation(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		source string
		want   bool
	}{
		{name: "bare identifier", source: "<* leaf *>", want: true},
		{name: "keyword", source: "<* END *>", want: false},
		{name: "argument", source: "<* leaf(1) *>", want: false},
		{name: "unmatched", source: "<* leaf", want: false},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			tokens := lexModula(test.source).tokens
			if got := modulaRecoveryProcedureAttributeDirectiveValid(
				tokens, 0, len(tokens),
			); got != test.want {
				t.Fatalf("attribute directive %q valid = %t, want %t",
					test.source, got, test.want)
			}
		})
	}
}

func TestModulaGNUDirectiveInvalidPlacementsAndShapesRecoverTail(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		body      string
		forbidden []string
	}{
		{
			name:      "free declaration directive",
			body:      `<* free(1) *>`,
			forbidden: []string{"free"},
		},
		{
			name:      "alignment before type",
			body:      `TYPE Broken = <* bytealignment(8) *> INTEGER;`,
			forbidden: []string{"Broken", "bytealignment"},
		},
		{
			name:      "type alignment missing expression",
			body:      `TYPE Broken = INTEGER <* bytealignment *>;`,
			forbidden: []string{"Broken", "bytealignment"},
		},
		{
			name:      "type alignment is empty",
			body:      `TYPE Broken = INTEGER <* *>;`,
			forbidden: []string{"Broken"},
		},
		{
			name:      "type alignment has an empty expression",
			body:      `TYPE Broken = INTEGER <* bytealignment() *>;`,
			forbidden: []string{"Broken", "bytealignment"},
		},
		{
			name:      "type alignment has adjacent operands",
			body:      `TYPE Broken = INTEGER <* bytealignment(1 2) *>;`,
			forbidden: []string{"Broken", "bytealignment"},
		},
		{
			name:      "type alignment is a list",
			body:      `TYPE Broken = INTEGER <* bytealignment(8), packed(1) *>;`,
			forbidden: []string{"Broken", "bytealignment", "packed"},
		},
		{
			name:      "type alignment name is reserved",
			body:      `TYPE Broken = INTEGER <* END(8) *>;`,
			forbidden: []string{"Broken"},
		},
		{
			name:      "alignment before variable type",
			body:      `VAR Broken: <* bytealignment(8) *> INTEGER;`,
			forbidden: []string{"Broken", "bytealignment"},
		},
		{
			name:      "variable alignment has trailing tokens",
			body:      `VAR Broken: INTEGER <* bytealignment(8) extra *>;`,
			forbidden: []string{"Broken", "bytealignment", "extra"},
		},
		{
			name: "record default attribute is bare",
			body: `TYPE Broken = RECORD <* packed *>
  field: INTEGER
END;`,
			forbidden: []string{"Broken", "field", "packed"},
		},
		{
			name: "record default attribute is displaced",
			body: `TYPE Broken = RECORD
  first: INTEGER;
  <* recordalignment(8) *> second: INTEGER
END;`,
			forbidden: []string{"Broken", "first", "second", "recordalignment"},
		},
		{
			name: "field attribute precedes colon",
			body: `TYPE Broken = RECORD
  field <* volatile *>: INTEGER
END;`,
			forbidden: []string{"Broken", "field", "volatile"},
		},
		{
			name: "field attribute has empty expression",
			body: `TYPE Broken = RECORD
  field: INTEGER <* volatile() *>
END;`,
			forbidden: []string{"Broken", "field", "volatile"},
		},
		{
			name: "field attribute has adjacent operands",
			body: `TYPE Broken = RECORD
  field: INTEGER <* checked(1 2) *>
END;`,
			forbidden: []string{"Broken", "field", "checked"},
		},
		{
			name: "field attribute list misses comma",
			body: `TYPE Broken = RECORD
  field: INTEGER <* volatile fieldalignment(4) *>
END;`,
			forbidden: []string{"Broken", "field", "volatile", "fieldalignment"},
		},
		{
			name: "field attribute list has trailing comma",
			body: `TYPE Broken = RECORD
  field: INTEGER <* volatile, *>
END;`,
			forbidden: []string{"Broken", "field", "volatile"},
		},
		{
			name: "procedure attribute has expression",
			body: `PROCEDURE Broken <* noreturn(1) *>;
BEGIN
END Broken;`,
			forbidden: []string{"Broken", "noreturn"},
		},
		{
			name: "procedure attribute is a list",
			body: `PROCEDURE Broken <* noreturn, leaf *>;
BEGIN
END Broken;`,
			forbidden: []string{"Broken", "noreturn", "leaf"},
		},
		{
			name: "procedure attribute is empty",
			body: `PROCEDURE Broken <* *>;
BEGIN
END Broken;`,
			forbidden: []string{"Broken"},
		},
		{
			name: "procedure attribute is before name",
			body: `PROCEDURE <* noreturn *> Broken;
BEGIN
END Broken;`,
			forbidden: []string{"Broken", "noreturn"},
		},
		{
			name: "procedure attribute is before return type",
			body: `PROCEDURE Broken(): <* noreturn *> Result;
BEGIN
END Broken;`,
			forbidden: []string{"Broken", "noreturn"},
		},
		{
			name: "formal attribute has expression",
			body: `PROCEDURE Broken(value: Data <* unused(1) *>);
BEGIN
END Broken;`,
			forbidden: []string{"Broken", "unused"},
		},
		{
			name: "formal attribute is a list",
			body: `PROCEDURE Broken(value: Data <* unused, addressable *>);
BEGIN
END Broken;`,
			forbidden: []string{"Broken", "unused", "addressable"},
		},
		{
			name: "formal attribute precedes colon",
			body: `PROCEDURE Broken(value <* unused *>: Data);
BEGIN
END Broken;`,
			forbidden: []string{"Broken", "unused"},
		},
		{
			name: "optional formal has attribute",
			body: `PROCEDURE Broken([value: Data = 1 <* unused *>]);
BEGIN
END Broken;`,
			forbidden: []string{"Broken", "unused"},
		},
		{
			name:      "procedure type formal has attribute",
			body:      `TYPE Broken = PROCEDURE (Data <* unused *>);`,
			forbidden: []string{"Broken", "unused"},
		},
		{
			name:      "constant has alignment",
			body:      `CONST Broken = 1 <* bytealignment(8) *>;`,
			forbidden: []string{"Broken", "bytealignment"},
		},
		{
			name:      "import has directive",
			body:      `IMPORT Hidden <* importattribute(1) *>;`,
			forbidden: []string{"Hidden", "importattribute"},
		},
		{
			name:      "import directive precedes raw name",
			body:      `IMPORT <* importattribute(1) *> Ghost;`,
			forbidden: []string{"Ghost", "importattribute"},
		},
		{
			name:      "from import directive precedes raw source",
			body:      `FROM <* importattribute(1) *> Source IMPORT Ghost;`,
			forbidden: []string{"Source", "Ghost", "importattribute"},
		},
		{
			name: "local module directive precedes raw owner",
			body: `MODULE <* moduleattribute(1) *> Fake;
PROCEDURE Hidden;
BEGIN
END Hidden;
BEGIN
END Fake;`,
			forbidden: []string{"Fake", "Hidden", "moduleattribute"},
		},
		{
			name:      "export has directive",
			body:      `EXPORT Hidden <* exportattribute *>;`,
			forbidden: []string{"Hidden", "exportattribute"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			source := modulaDirectiveTestProgram(test.body)
			fixture := modulaDirectiveTestAnalyze(t, source, true)
			modulaDirectiveTestAssertDefinitions(
				t, fixture, []string{"DirectiveRecovery", "Tail"}, test.forbidden,
			)
			modulaDirectiveTestAssertNoImports(t, fixture)
			modulaDirectiveTestAssertAllBounds(t, fixture)
		})
	}
}

func TestModulaGNUDirectiveInvalidPriorityAndProcedureBodyPlacements(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		source    string
		want      []string
		forbidden []string
	}{
		{
			name: "module priority",
			source: `MODULE Priority[1 <* priorityattribute(1) *>];
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END Priority.
`,
			want:      []string{"Priority", "Tail"},
			forbidden: []string{"priorityattribute"},
		},
		{
			name: "procedure declaration region",
			source: `MODULE DeclarationRegion;
PROCEDURE Body;
<* declarationattribute(1) *>
BEGIN
END Body;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END DeclarationRegion.
`,
			want:      []string{"DeclarationRegion", "Body", "Tail"},
			forbidden: []string{"declarationattribute"},
		},
		{
			name: "procedure statement region",
			source: `MODULE StatementRegion;
PROCEDURE Body;
BEGIN
  <* statementattribute(1) *>;
END Body;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END StatementRegion.
`,
			want:      []string{"StatementRegion", "Body", "Tail"},
			forbidden: []string{"statementattribute"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := modulaDirectiveTestAnalyze(t, test.source, true)
			modulaDirectiveTestAssertDefinitions(
				t, fixture, test.want, test.forbidden,
			)
			modulaDirectiveTestAssertNoImports(t, fixture)
			modulaDirectiveTestAssertAllBounds(t, fixture)
		})
	}
}

func TestModulaContentGateTreatsPairedPriorityDirectiveAtomically(t *testing.T) {
	t.Parallel()

	const source = `MODULE PriorityPayload[1 <* illegal ] [ *> + 2];
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END PriorityPayload.
`
	lexed := lexModula(source)
	if !modulaContentGate(lexed) {
		t.Fatal("paired directive payload brackets escaped into the content gate")
	}
	lines := modulaTestLines(source)
	analysis := analyzeModulaSource(source, len(lines))
	if analysis == nil || !analysis.gated || analysis.tree == nil ||
		len(analysis.recoverySpans) == 0 {
		t.Fatalf("paired priority directive analysis = %#v, want gated concrete recovery",
			analysis)
	}
	if got := modulaTestDefinitionSymbols(analysis.definitions); !slices.Equal(
		got, []string{"PriorityPayload", "Tail"},
	) {
		t.Fatalf("paired priority directive definitions = %#v, want module/Tail", got)
	}
}

func TestModulaGNUPairedIllegalDirectivePayloadIsAtomic(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		body      string
		forbidden []string
	}{
		{
			name: "declaration level",
			body: `<* illegal
PROCEDURE Phantom;
BEGIN
END Phantom;
IMPORT Ghost;
MODULE Fake;
BEGIN
END Fake;
TYPE Fake = RECORD field: INTEGER END;
BEGIN IF condition THEN END;
END DirectiveRecovery.
*>`,
			forbidden: []string{"Phantom", "Fake", "field", "Ghost", "condition"},
		},
		{
			name: "type declaration",
			body: `TYPE Broken = INTEGER <* illegal
PROCEDURE Phantom;
IMPORT Ghost;
END DirectiveRecovery.
*>;`,
			forbidden: []string{"Broken", "Phantom", "Ghost"},
		},
		{
			name: "procedure heading",
			body: `PROCEDURE Broken <* illegal
PROCEDURE Phantom;
IMPORT Ghost;
END DirectiveRecovery.
*>;
BEGIN
END Broken;`,
			forbidden: []string{"Broken", "Phantom", "Ghost"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			source := modulaDirectiveTestProgram(test.body)
			fixture := modulaDirectiveTestAnalyze(t, source, true)
			modulaDirectiveTestAssertDefinitions(
				t, fixture, []string{"DirectiveRecovery", "Tail"}, test.forbidden,
			)
			modulaDirectiveTestAssertNoImports(t, fixture)
			modulaDirectiveTestAssertAllBounds(t, fixture)
		})
	}
}

func TestModulaGNUUnmatchedDirectiveContextsRetainIndependentTail(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		body      string
		forbidden []string
	}{
		{
			name:      "free declaration context",
			body:      `<* broken`,
			forbidden: []string{"broken"},
		},
		{
			name:      "type context",
			body:      `TYPE Broken = INTEGER <* broken`,
			forbidden: []string{"Broken", "broken"},
		},
		{
			name:      "variable context",
			body:      `VAR Broken: INTEGER <* broken`,
			forbidden: []string{"Broken", "broken"},
		},
		{
			name:      "procedure context",
			body:      `PROCEDURE Broken(value: Data) <* broken`,
			forbidden: []string{"Broken", "broken"},
		},
		{
			name:      "import context",
			body:      `IMPORT Hidden <* broken`,
			forbidden: []string{"Hidden", "broken"},
		},
		{
			name:      "export context",
			body:      `EXPORT Hidden <* broken`,
			forbidden: []string{"Hidden", "broken"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			source := modulaDirectiveTestProgram(test.body)
			fixture := modulaDirectiveTestAnalyze(t, source, true)
			modulaDirectiveTestAssertDefinitions(
				t, fixture, []string{"DirectiveRecovery", "Tail"}, test.forbidden,
			)
			modulaDirectiveTestAssertNoImports(t, fixture)
			modulaDirectiveTestAssertAllBounds(t, fixture)
		})
	}
}

func TestModulaGNUUnmatchedDirectiveAtEOFDoesNotPromotePayload(t *testing.T) {
	t.Parallel()

	const source = `MODULE UnmatchedEOF;
<* PROCEDURE Phantom; IMPORT Ghost; MODULE Fake;`
	fixture := modulaDirectiveTestAnalyze(t, source, true)
	modulaDirectiveTestAssertDefinitions(
		t, fixture, []string{"UnmatchedEOF"},
		[]string{"Phantom", "Ghost", "Fake"},
	)
	modulaDirectiveTestAssertNoImports(t, fixture)
	modulaDirectiveTestAssertAllBounds(t, fixture)
}

func TestModulaGNUDirectiveDelimiterShieldingUsesFirstVisibleClose(t *testing.T) {
	t.Parallel()

	const source = `MODULE Shielding;
<* illegal("*> PROCEDURE StringHidden;")
(* *> PROCEDURE CommentHidden; *)
# *> PROCEDURE MarkerHidden;
still_illegal *>
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END Shielding.
`
	fixture := modulaDirectiveTestAnalyze(t, source, true)
	openers, visibleCloses := 0, 0
	for _, token := range fixture.lexed.tokens {
		switch token.text {
		case "<*":
			openers++
			if !token.directiveClosed {
				t.Errorf("paired directive opener at byte %d is not marked closed", token.start)
			}
		case "*>":
			visibleCloses++
		}
	}
	if openers != 1 || visibleCloses != 1 {
		t.Fatalf("visible directive delimiters = %d/%d, want 1/1: %#v",
			openers, visibleCloses, fixture.lexed.tokens)
	}
	modulaDirectiveTestAssertDefinitions(
		t, fixture, []string{"Shielding", "Tail"},
		[]string{"StringHidden", "CommentHidden", "MarkerHidden", "still_illegal"},
	)
	modulaDirectiveTestAssertNoImports(t, fixture)
	modulaDirectiveTestAssertAllBounds(t, fixture)
}

func TestModulaGNUDirectiveStringTreatsCarriageReturnAsPayload(t *testing.T) {
	t.Parallel()

	const source = "MODULE CRShielding;\n" +
		"<* illegal(\"x\r*>\n" +
		"PROCEDURE Ghost;\n" +
		"BEGIN\n" +
		"END Ghost;\n" +
		"*>\n" +
		"PROCEDURE Tail;\n" +
		"BEGIN\n" +
		"END Tail;\n" +
		"BEGIN\n" +
		"END CRShielding.\n"
	lines := modulaTestLines(source)
	lexed := lexModula(source)
	if lexed.concreteEligible {
		t.Fatal("unterminated-to-LF directive string remained concrete-eligible")
	}
	openers, visibleCloses := 0, 0
	for _, token := range lexed.tokens {
		switch token.text {
		case "<*":
			openers++
			if !token.directiveClosed {
				t.Errorf("directive opener at byte %d is not marked closed", token.start)
			}
		case "*>":
			visibleCloses++
		}
	}
	if openers != 1 || visibleCloses != 1 {
		t.Fatalf("visible directive delimiters = %d/%d, want 1/1: %#v",
			openers, visibleCloses, lexed.tokens)
	}
	analysis := analyzeModulaSource(source, len(lines))
	if analysis == nil || !analysis.gated || analysis.tree != nil {
		t.Fatalf("CR-shielded analysis = %#v, want gated fallback", analysis)
	}
	for path, definitions := range map[string][]sourceDefinition{
		"fallback": analyzeModulaLexically(source, len(lines)).definitions,
		"merged":   analysis.definitions,
	} {
		if got, want := modulaTestDefinitionSymbols(definitions),
			[]string{"CRShielding", "Tail"}; !slices.Equal(got, want) {
			t.Errorf("%s CR-shielded definitions = %#v, want %#v", path, got, want)
		}
		modulaTestAssertDefinitionCoordinates(t, lines, definitions)
	}
}

func TestModulaGNULargePairedIllegalDirectiveStaysAtomicAcrossHeaderCap(t *testing.T) {
	t.Parallel()

	// Cross the bounded declaration-header frontier before any of the tokens
	// that would mutate overflow structure. If a paired directive is not kept
	// atomic, RECORD/CASE and the final unmatched delimiters leave the header
	// open and prevent the line-leading Tail from becoming a recovery restart.
	payload := strings.Repeat("noise ", modulaMaximumDeclarationTokens+64) +
		`; RECORD CASE END
( balanced ) [ balanced ] ( [
PROCEDURE Fake; IMPORT Ghost; END LargeAtomic;
`
	source := `MODULE LargeAtomic;
TYPE Broken = INTEGER <* ` + payload + `*>;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END LargeAtomic.
`
	fixture := modulaDirectiveTestAnalyze(t, source, true)
	if fixture.lexed.lexicalUnits <= modulaMaximumDeclarationTokens {
		t.Fatalf("large directive lexical units = %d, want > declaration cap %d",
			fixture.lexed.lexicalUnits, modulaMaximumDeclarationTokens)
	}
	if fixture.lexed.lexicalUnits > modulaMaximumConcreteTokens ||
		!fixture.lexed.concreteEligible {
		t.Fatalf("large directive crossed concrete frontier: units=%d eligible=%t",
			fixture.lexed.lexicalUnits, fixture.lexed.concreteEligible)
	}
	if len(fixture.lexed.tokens) > modulaMaximumRetainedTokens {
		t.Fatalf("retained tokens = %d, want <= %d",
			len(fixture.lexed.tokens), modulaMaximumRetainedTokens)
	}
	modulaDirectiveTestAssertDefinitions(
		t, fixture, []string{"LargeAtomic", "Tail"},
		[]string{"Broken", "noise", "balanced", "Fake", "Ghost"},
	)
	modulaDirectiveTestAssertNoImports(t, fixture)
	modulaDirectiveTestAssertAllBounds(t, fixture)
}

func TestModulaGNUPairedDirectiveDeclarationHeaderCapBoundary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		tokens int
	}{
		{name: "at cap", tokens: modulaMaximumDeclarationTokens},
		{name: "one over cap", tokens: modulaMaximumDeclarationTokens + 1},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			// Header tokens are Broken, =, INTEGER, <*, payload..., *>.
			payloadTokens := test.tokens - 5
			source := `MODULE HeaderBoundary;
TYPE Broken = INTEGER <* ` + strings.Repeat("noise ", payloadTokens) + `*>;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END HeaderBoundary.
`
			lexed := lexModula(source)
			typeIndex := slices.IndexFunc(lexed.tokens, func(token modulaToken) bool {
				return token.text == "TYPE"
			})
			semicolon := -1
			for index := typeIndex + 1; index < len(lexed.tokens); index++ {
				if lexed.tokens[index].text == ";" {
					semicolon = index
					break
				}
			}
			if typeIndex < 0 || semicolon < 0 {
				t.Fatalf("directive declaration boundary missing TYPE/semicolon: %#v",
					lexed.tokens)
			}
			if got := semicolon - typeIndex - 1; got != test.tokens {
				t.Fatalf("directive declaration header tokens = %d, want %d",
					got, test.tokens)
			}
			fixture := modulaDirectiveTestAnalyze(t, source, true)
			modulaDirectiveTestAssertDefinitions(
				t, fixture, []string{"HeaderBoundary", "Tail"},
				[]string{"Broken", "noise"},
			)
			modulaDirectiveTestAssertNoImports(t, fixture)
			modulaDirectiveTestAssertAllBounds(t, fixture)
		})
	}
}

func TestModulaGNUPairedDirectiveRawOwnerHeaderCapBoundary(t *testing.T) {
	t.Parallel()

	owners := []struct {
		name   string
		prefix string
	}{
		{name: "local module", prefix: "MODULE"},
		{name: "procedure", prefix: "PROCEDURE"},
	}
	boundaries := []struct {
		name   string
		tokens int
	}{
		{name: "at cap", tokens: modulaMaximumDeclarationTokens},
		{name: "one over cap", tokens: modulaMaximumDeclarationTokens + 1},
		{name: "payload over cap", tokens: modulaMaximumDeclarationTokens + 68},
	}
	for _, owner := range owners {
		for _, boundary := range boundaries {
			t.Run(owner.name+" "+boundary.name, func(t *testing.T) {
				t.Parallel()

				// Prefix, <*, payload, *>, and Fake together make the exact
				// declaration-header token count. At one-over, Fake is the
				// first streamed token after the retained frontier.
				payloadTokens := boundary.tokens - 4
				header := owner.prefix + " <* " +
					strings.Repeat("noise ", payloadTokens) + "*> Fake"
				headerTokens := lexModula(header + ";").tokens
				if got := len(headerTokens) - 1; got != boundary.tokens {
					t.Fatalf("raw-owner header tokens = %d, want %d", got, boundary.tokens)
				}

				source := `MODULE RawOwnerBoundary;
` + header + `;
PROCEDURE Hidden;
BEGIN
END Hidden;
BEGIN
END Fake;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END RawOwnerBoundary.
`
				lines := modulaTestLines(source)
				lexical := analyzeModulaLexically(source, len(lines))
				if got := modulaTestDefinitionSymbols(lexical.definitions); !slices.Equal(got, []string{"RawOwnerBoundary", "Tail"}) {
					t.Fatalf("raw-owner fallback definitions = %#v, want outer/Tail", got)
				}
				if len(lexical.imports) != 0 {
					t.Fatalf("raw-owner fallback imports = %#v, want none", lexical.imports)
				}
				modulaTestAssertDefinitionCoordinates(t, lines, lexical.definitions)
			})
		}
	}
}

func TestModulaGNUPairedDirectiveRawProcedurePrefixStreamsAcrossHeaderCap(
	t *testing.T,
) {
	t.Parallel()

	header := "PROCEDURE __ATTRIBUTE__ <* " +
		strings.Repeat("noise ", modulaMaximumDeclarationTokens+64) +
		"*> __BUILTIN__((foreign)) Fake"
	if units := lexModula(header + ";").lexicalUnits; units <= modulaMaximumDeclarationTokens {
		t.Fatalf("raw procedure prefix units = %d, want > %d",
			units, modulaMaximumDeclarationTokens)
	}
	source := `MODULE RawProcedurePrefix;
` + header + `;
PROCEDURE Hidden;
BEGIN
END Hidden;
BEGIN
END Fake;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END RawProcedurePrefix.
`
	lines := modulaTestLines(source)
	lexical := analyzeModulaLexically(source, len(lines))
	if got := modulaTestDefinitionSymbols(lexical.definitions); !slices.Equal(got, []string{"RawProcedurePrefix", "Tail"}) {
		t.Fatalf("raw procedure prefix fallback definitions = %#v, want outer/Tail", got)
	}
	modulaTestAssertDefinitionCoordinates(t, lines, lexical.definitions)
}

func TestModulaGNUOverflowOptionalDefaultExpressionValidation(t *testing.T) {
	t.Parallel()

	prefix := strings.Repeat("1 + ", modulaMaximumDeclarationTokens/2+64)
	tests := []struct {
		name      string
		malformed string
	}{
		{name: "adjacent operands", malformed: "1 2"},
		{name: "repeated additive operator", malformed: "1 + + 2"},
		{name: "mixed repeated operators", malformed: "1 + * 2"},
		{name: "adjacent operands in parentheses", malformed: "(1 2)"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			source := `MODULE DefaultBoundary;
PROCEDURE Broken([arg: T = ` + prefix + test.malformed + `]);
PROCEDURE Hidden;
BEGIN
END Hidden;
BEGIN
END Broken;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END DefaultBoundary.
`
			lines := modulaTestLines(source)
			lexed := lexModula(source)
			if lexed.lexicalUnits <= modulaMaximumDeclarationTokens {
				t.Fatalf("malformed default units = %d, want > %d",
					lexed.lexicalUnits, modulaMaximumDeclarationTokens)
			}
			lexical := analyzeModulaLexically(source, len(lines))
			if got := modulaTestDefinitionSymbols(lexical.definitions); !slices.Equal(got, []string{"DefaultBoundary", "Tail"}) {
				t.Fatalf("malformed default fallback definitions = %#v, want outer/Tail", got)
			}
			modulaTestAssertDefinitionCoordinates(t, lines, lexical.definitions)
		})
	}
}

func TestModulaGNUOverflowOptionalDefaultValidUnaryExpressions(t *testing.T) {
	t.Parallel()

	additive := strings.Repeat("1 + ", modulaMaximumDeclarationTokens/2+64)
	logical := strings.Repeat("flag AND ", modulaMaximumDeclarationTokens/2+64)
	tests := []struct {
		name       string
		expression string
	}{
		{name: "leading plus", expression: "+1 + " + additive + "1"},
		{name: "leading minus", expression: "-1 + " + additive + "1"},
		{name: "logical not", expression: "NOT flag AND " + logical + "flag"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			source := `MODULE ValidDefaultBoundary;
PROCEDURE Huge([arg: T = ` + test.expression + `]);
BEGIN
END Huge;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END ValidDefaultBoundary.
`
			lines := modulaTestLines(source)
			lexical := analyzeModulaLexically(source, len(lines))
			if got := modulaTestDefinitionSymbols(lexical.definitions); !slices.Equal(got, []string{"ValidDefaultBoundary", "Huge", "Tail"}) {
				t.Fatalf("valid unary default fallback definitions = %#v", got)
			}
			modulaTestAssertDefinitionCoordinates(t, lines, lexical.definitions)
		})
	}
}

func TestModulaGNUOverflowOptionalDefaultCompositeGrammar(t *testing.T) {
	t.Parallel()

	filler := strings.Repeat("1 + ", modulaMaximumDeclarationTokens/2+64)
	valid := []struct {
		name  string
		shape string
	}{
		{name: "empty constructor", shape: "{}"},
		{name: "constructor range repetition", shape: "{1, 2..N BY -2}"},
		{name: "typed empty constructor", shape: "T{}"},
		{name: "qualified typed constructor", shape: "Pkg.T{1..N BY Step}"},
		{name: "empty call", shape: "F()"},
		{name: "call arguments", shape: "F(a, b)"},
		{name: "general call argument designator", shape: "F(a.b[i, j]^)"},
		{name: "simple builtin attribute", shape: "__ATTRIBUTE__ __BUILTIN__((x))"},
		{
			name:  "qualified builtin attribute",
			shape: "__ATTRIBUTE__ __BUILTIN__((<Pkg.T, size>))",
		},
	}
	invalid := []struct {
		name  string
		shape string
	}{
		{name: "free repetition", shape: "1 BY 2"},
		{name: "free range", shape: "1..2"},
		{name: "repeated constructor repetition", shape: "{1 BY 2 BY 3}"},
		{name: "repeated constructor range", shape: "{1..2..3}"},
		{name: "chained relation", shape: "1 = 2 = 3"},
		{name: "grouping comma", shape: "(a, b)"},
		{name: "root indexed designator", shape: "a.b[i, j]^"},
		{name: "constructor indexed component", shape: "T{a[i]}"},
		{name: "leading empty call argument", shape: "F(, a)"},
		{name: "trailing empty call argument", shape: "F(a,)"},
		{name: "empty index", shape: "a[]"},
		{name: "bare builtin marker", shape: "__BUILTIN__((x))"},
		{
			name:  "misordered builtin attribute",
			shape: "__BUILTIN__ __ATTRIBUTE__((x))",
		},
	}
	run := func(t *testing.T, shape string, wantValid bool) {
		t.Helper()
		source := `MODULE CompositeDefault;
PROCEDURE Candidate([arg: T = ` + filler + shape + `]);
BEGIN
END Candidate;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END CompositeDefault.
`
		lines := modulaTestLines(source)
		lexical := analyzeModulaLexically(source, len(lines))
		want := []string{"CompositeDefault", "Tail"}
		if wantValid {
			want = []string{"CompositeDefault", "Candidate", "Tail"}
		}
		if got := modulaTestDefinitionSymbols(lexical.definitions); !slices.Equal(got, want) {
			t.Fatalf("composite default fallback definitions = %#v, want %#v", got, want)
		}
		modulaTestAssertDefinitionCoordinates(t, lines, lexical.definitions)
	}
	for _, test := range valid {
		t.Run("valid "+test.name, func(t *testing.T) {
			t.Parallel()
			run(t, test.shape, true)
		})
	}
	for _, test := range invalid {
		t.Run("invalid "+test.name, func(t *testing.T) {
			t.Parallel()
			run(t, test.shape, false)
		})
	}
}

func TestModulaGNUPairedDirectiveConcreteTokenCapBoundary(t *testing.T) {
	t.Parallel()

	const prefix = "MODULE ConcreteBoundary;\n<* "
	const suffix = `*>
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END ConcreteBoundary.
`
	baseUnits := lexModula(prefix + suffix).lexicalUnits
	if baseUnits >= modulaMaximumConcreteTokens {
		t.Fatalf("directive boundary frame units = %d, want < %d",
			baseUnits, modulaMaximumConcreteTokens)
	}
	for _, delta := range []int{0, 1} {
		name := "at cap"
		if delta == 1 {
			name = "one over cap"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			source := prefix + strings.Repeat(
				"noise ", modulaMaximumConcreteTokens-baseUnits+delta,
			) + suffix
			lines := modulaTestLines(source)
			lexed := lexModula(source)
			wantUnits := modulaMaximumConcreteTokens + delta
			if lexed.lexicalUnits != wantUnits {
				t.Fatalf("directive boundary lexical units = %d, want %d",
					lexed.lexicalUnits, wantUnits)
			}
			if lexed.concreteEligible != (delta == 0) {
				t.Fatalf("directive boundary concrete eligibility = %t at delta %d",
					lexed.concreteEligible, delta)
			}
			if delta == 0 {
				fixture := modulaDirectiveTestAnalyze(t, source, true)
				modulaDirectiveTestAssertDefinitions(
					t, fixture, []string{"ConcreteBoundary", "Tail"},
					[]string{"noise"},
				)
				modulaDirectiveTestAssertNoImports(t, fixture)
				modulaDirectiveTestAssertAllBounds(t, fixture)
				return
			}
			if tree, ok := parseModulaSyntax(source, lexed); ok || tree != nil {
				t.Fatalf("over-cap directive parse = %#v, %t; want nil, false", tree, ok)
			}
			lexical := analyzeModulaLexically(source, len(lines))
			analysis := analyzeModulaSource(source, len(lines))
			if analysis == nil || !analysis.gated || analysis.tree != nil {
				t.Fatalf("over-cap directive analysis = %#v, want fallback only", analysis)
			}
			for path, definitions := range map[string][]sourceDefinition{
				"fallback": lexical.definitions,
				"merged":   analysis.definitions,
			} {
				if got := modulaTestDefinitionSymbols(definitions); !slices.Equal(
					got, []string{"ConcreteBoundary", "Tail"},
				) {
					t.Errorf("%s over-cap definitions = %#v", path, got)
				}
				modulaTestAssertDefinitionCoordinates(t, lines, definitions)
			}
		})
	}
}

func TestModulaGNUOverConcreteDirectiveStreamsRetainTail(t *testing.T) {
	t.Parallel()

	filler := strings.Repeat("noise ", modulaMaximumConcreteTokens+64)
	tests := []struct {
		name      string
		source    string
		forbidden []string
	}{
		{
			name: "paired type payload",
			source: `MODULE OverPaired;
TYPE Broken = INTEGER <* ` + filler + `
; RECORD CASE END ( balanced ) [ balanced ] ( [
PROCEDURE Fake; IMPORT Ghost; END OverPaired;
*>;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END OverPaired.
`,
			forbidden: []string{"Broken", "noise", "balanced", "Fake", "Ghost"},
		},
		{
			name: "unmatched free payload",
			source: `MODULE OverUnmatched;
<* ` + filler + `
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END OverUnmatched.
`,
			forbidden: []string{"noise"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			lines := modulaTestLines(test.source)
			lexed := lexModula(test.source)
			if lexed.concreteEligible ||
				lexed.lexicalUnits <= modulaMaximumConcreteTokens {
				t.Fatalf("over-concrete directive remained eligible: units=%d eligible=%t",
					lexed.lexicalUnits, lexed.concreteEligible)
			}
			if len(lexed.tokens) > modulaMaximumRetainedTokens {
				t.Fatalf("over-concrete retained tokens = %d, want <= %d",
					len(lexed.tokens), modulaMaximumRetainedTokens)
			}
			if tree, ok := parseModulaSyntax(test.source, lexed); ok || tree != nil {
				t.Fatalf("over-concrete directive parse = %#v, %t; want nil, false",
					tree, ok)
			}
			lexical := analyzeModulaLexically(test.source, len(lines))
			analysis := analyzeModulaSource(test.source, len(lines))
			if analysis == nil || !analysis.gated || analysis.tree != nil {
				t.Fatalf("over-concrete analysis = %#v, want gated fallback only", analysis)
			}
			for path, definitions := range map[string][]sourceDefinition{
				"fallback": lexical.definitions,
				"merged":   analysis.definitions,
			} {
				symbols := modulaTestDefinitionSymbols(definitions)
				want := []string{"OverPaired", "Tail"}
				if test.name == "unmatched free payload" {
					want[0] = "OverUnmatched"
				}
				if !slices.Equal(symbols, want) {
					t.Errorf("%s over-concrete definitions = %#v, want %#v",
						path, symbols, want)
				}
				for _, forbidden := range test.forbidden {
					if slices.Contains(symbols, forbidden) {
						t.Errorf("%s over-concrete definitions promoted %q: %#v",
							path, forbidden, definitions)
					}
				}
				modulaTestAssertDefinitionCoordinates(t, lines, definitions)
			}
			if len(lexical.imports) != 0 || len(analysis.imports) != 0 {
				t.Errorf("over-concrete imports = fallback %#v merged %#v, want none",
					lexical.imports, analysis.imports)
			}
			for path, scopes := range map[string][]cLineScope{
				"fallback": lexical.scopes,
				"merged":   analysis.scopes,
			} {
				for _, scope := range scopes {
					if scope.start < 1 || scope.start > scope.end ||
						scope.end > len(lines) {
						t.Errorf("%s over-concrete scope out of bounds: %#v",
							path, scope)
					}
				}
			}
		})
	}
}

func TestModulaGNUValidHugeProcedureAndFormalAttributesCrossHeaderCap(t *testing.T) {
	t.Parallel()

	names := make([]string, modulaMaximumDeclarationTokens/2+64)
	for index := range names {
		names[index] = fmt.Sprintf("argument%d", index)
	}
	formal := "first: Data <* unused *>; " +
		strings.Join(names, ", ") + ": Data <* unused *>"
	formalNames := append([]string{"first"}, names...)
	tests := []struct {
		name   string
		source string
		want   []string
	}{
		{
			name: "implementation",
			source: `IMPLEMENTATION MODULE HugeImplementation;
PROCEDURE Massive(` + formal + `) <* noreturn *>;
BEGIN
END Massive;
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END HugeImplementation.
`,
			want: []string{"HugeImplementation", "Massive", "Tail"},
		},
		{
			name: "definition",
			source: `DEFINITION MODULE HugeDefinition;
PROCEDURE __INLINE__ Massive(` + formal + `) <* noreturn *>;
PROCEDURE Tail;
END HugeDefinition.
`,
			want: []string{"HugeDefinition", "Massive", "Tail"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			fixture := modulaDirectiveTestAnalyze(t, test.source, false)
			if fixture.lexed.lexicalUnits <= modulaMaximumDeclarationTokens {
				t.Fatalf("huge heading lexical units = %d, want > declaration cap %d",
					fixture.lexed.lexicalUnits, modulaMaximumDeclarationTokens)
			}
			if fixture.lexed.lexicalUnits > modulaMaximumConcreteTokens ||
				!fixture.lexed.concreteEligible {
				t.Fatalf("huge heading crossed concrete frontier: units=%d eligible=%t",
					fixture.lexed.lexicalUnits, fixture.lexed.concreteEligible)
			}
			modulaDirectiveTestAssertDefinitions(t, fixture, test.want, formalNames)
			modulaDirectiveTestAssertAllBounds(t, fixture)
		})
	}
}

type modulaDirectiveTestFixture struct {
	source   string
	lines    []string
	lexed    modulaLexResult
	tree     *modulaSyntaxTree
	lexical  modulaLexicalAnalysis
	analysis *modulaSourceAnalysis
}

func modulaDirectiveTestProgram(body string) string {
	return "MODULE DirectiveRecovery;\n" + body + `
PROCEDURE Tail;
BEGIN
END Tail;
BEGIN
END DirectiveRecovery.
`
}

func modulaDirectiveTestAnalyze(
	t *testing.T,
	source string,
	wantRecovery bool,
) modulaDirectiveTestFixture {
	t.Helper()
	lines := modulaTestLines(source)
	lexed := lexModula(source)
	if !lexed.concreteEligible {
		t.Fatalf("directive fixture is not concrete-eligible: bytes=%d units=%d",
			len(source), lexed.lexicalUnits)
	}
	tree, ok := parseModulaSyntax(source, lexed)
	if !ok || !validateModulaSyntaxTree(tree, len(source)) {
		t.Fatal("directive fixture did not produce a validated concrete tree")
	}
	spans := modulaSyntaxErrorSpans(tree, len(source))
	if wantRecovery && len(spans) == 0 {
		t.Fatal("invalid directive fixture produced no concrete recovery evidence")
	}
	if !wantRecovery && len(spans) != 0 {
		t.Fatalf("valid directive fixture recovery spans = %#v, want none", spans)
	}
	for _, span := range spans {
		if span.start < 0 || span.start >= span.end || span.end > len(source) {
			t.Errorf("recovery span out of bounds for %d bytes: %#v", len(source), span)
		}
	}
	analysis := analyzeModulaSource(source, len(lines))
	if analysis == nil || !analysis.gated {
		t.Fatalf("directive fixture analysis = %#v, want gated analysis", analysis)
	}
	return modulaDirectiveTestFixture{
		source: source, lines: lines, lexed: lexed, tree: tree,
		lexical: analyzeModulaLexically(source, len(lines)), analysis: analysis,
	}
}

func modulaDirectiveTestAssertDefinitions(
	t *testing.T,
	fixture modulaDirectiveTestFixture,
	want, forbidden []string,
) {
	t.Helper()
	paths := map[string][]sourceDefinition{
		"concrete": modulaTreeDefinitions(
			fixture.source, len(fixture.lines), fixture.tree,
		),
		"fallback": fixture.lexical.definitions,
		"merged":   fixture.analysis.definitions,
	}
	for path, definitions := range paths {
		symbols := modulaTestDefinitionSymbols(definitions)
		if !slices.Equal(symbols, want) {
			t.Errorf("%s directive definitions = %#v, want %#v", path, symbols, want)
		}
		for _, symbol := range forbidden {
			if slices.Contains(symbols, symbol) {
				t.Errorf("%s directive definitions promoted %q: %#v",
					path, symbol, definitions)
			}
		}
		modulaTestAssertDefinitionCoordinates(t, fixture.lines, definitions)
	}
}

func modulaDirectiveTestAssertNoImports(
	t *testing.T,
	fixture modulaDirectiveTestFixture,
) {
	t.Helper()
	paths := map[string][]cLineSpan{
		"concrete": modulaTreeImports(
			fixture.source, len(fixture.lines), fixture.tree,
		),
		"fallback": fixture.lexical.imports,
		"merged":   fixture.analysis.imports,
	}
	for path, imports := range paths {
		if len(imports) != 0 {
			t.Errorf("%s directive imports = %#v, want none", path, imports)
		}
	}
}

func modulaDirectiveTestAssertAllBounds(
	t *testing.T,
	fixture modulaDirectiveTestFixture,
) {
	t.Helper()
	lineCount := len(fixture.lines)
	scopePaths := map[string][]cLineScope{
		"concrete": modulaTreeScopes(fixture.source, lineCount, fixture.tree),
		"fallback": fixture.lexical.scopes,
		"merged":   fixture.analysis.scopes,
	}
	for path, scopes := range scopePaths {
		for _, scope := range scopes {
			if scope.start < 1 || scope.start > scope.end || scope.end > lineCount {
				t.Errorf("%s directive scope out of 1-%d bounds: %#v",
					path, lineCount, scope)
			}
		}
	}
	importPaths := map[string][]cLineSpan{
		"concrete": modulaTreeImports(fixture.source, lineCount, fixture.tree),
		"fallback": fixture.lexical.imports,
		"merged":   fixture.analysis.imports,
	}
	for path, imports := range importPaths {
		for _, span := range imports {
			if span.start < 1 || span.start > span.end || span.end > lineCount {
				t.Errorf("%s directive import out of 1-%d bounds: %#v",
					path, lineCount, span)
			}
		}
	}
}
