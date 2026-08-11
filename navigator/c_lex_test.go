package navigator

import (
	"slices"
	"strconv"
	"strings"
	"testing"
)

func TestCLexicalDeclarationsRecoverDecoratedAndTerminalLabelFunctions(t *testing.T) {
	t.Parallel()

	const source = `__declspec(dllexport) int ms_export(void) {}
__attribute__((cold)) int gnu_export(void) {}
int terminal_label(int ready)
{
done:
    if (ready) goto done;
last:
}
`
	lexed := lexC(source)
	got := make([]string, 0, len(lexed.definitions))
	for _, definition := range lexed.definitions {
		got = append(got, definition.symbol)
	}
	want := []string{"ms_export", "gnu_export", "terminal_label"}
	if !slices.Equal(got, want) {
		t.Fatalf("lexical definitions = %#v, want %#v; tokens=%#v", got, want, lexed.tokens)
	}
	for _, definition := range lexed.definitions {
		if !lexed.trustedDefinitions[cDefinitionKey(definition)] {
			t.Errorf("definition %q is not trusted", definition.symbol)
		}
	}
}

func TestCLexDirectiveCommentAndHeaderMasks(t *testing.T) {
	t.Parallel()

	const source = `#define X /* open
int hidden(void);
*/
#include <dir/*literal*/x.h>
#include <dir//literal/x.h>
int visible(void);
`
	lexed := lexC(source)
	maskedComments := maskCSource(source, lexed.commentSpans)
	if strings.Contains(maskedComments, "hidden") {
		t.Fatalf("directive block comment leaked: %q", maskedComments)
	}
	if !strings.Contains(maskedComments, "<dir/*literal*/x.h>") ||
		!strings.Contains(maskedComments, "<dir//literal/x.h>") {
		t.Fatalf("header-name bytes were classified as comments: %q", maskedComments)
	}
	if got := maskCSource(source, lexed.stringSpans); strings.Contains(got, "literal") {
		t.Fatalf("header-name spans were not string-like: %q", got)
	}
}

func TestCLexBlockCommentNewlineRestoresDirectiveStart(t *testing.T) {
	t.Parallel()

	const source = "int value; /* comment\n*/ #define NEXT 1\n"
	lexed := lexC(source)
	if len(lexed.directives) != 1 || lexed.directives[0].kind != "define" {
		t.Fatalf("directives = %#v, want define", lexed.directives)
	}
	if len(lexed.definitions) == 0 || lexed.definitions[len(lexed.definitions)-1].symbol != "NEXT" {
		t.Fatalf("definitions = %#v, want NEXT", lexed.definitions)
	}
}

func TestCLexTrustAndRecoveryStayCandidateLocal(t *testing.T) {
	t.Parallel()

	const source = `#define VALUE 1
int object;
int caller(void) { foo(bar); }
)
int tail(void) {}
`
	lexed := lexC(source)
	symbols := make([]string, 0, len(lexed.definitions))
	byName := make(map[string]sourceDefinition)
	for _, definition := range lexed.definitions {
		symbols = append(symbols, definition.symbol)
		byName[definition.symbol] = definition
	}
	if !slices.Equal(symbols, []string{"VALUE", "object", "caller", "tail"}) {
		t.Fatalf("definitions = %#v; foo(bar) must remain an expression", symbols)
	}
	if !lexed.trustedDefinitions[cDefinitionKey(byName["VALUE"])] {
		t.Fatal("line-start macro is not trusted")
	}
	objectKey := cDefinitionKey(byName["object"])
	if lexed.trustedDefinitions[objectKey] || lexed.recoveredDefinitions[objectKey] {
		t.Fatalf("distant mismatch widened object trust/recovery: trusted=%v recovered=%v",
			lexed.trustedDefinitions[objectKey], lexed.recoveredDefinitions[objectKey])
	}
}

func TestCLexLogicalUCNAndPPNumberBoundaries(t *testing.T) {
	t.Parallel()

	source := "int r\\u00\\\nE9(void) {}\nint tail = 1é;\n"
	lexed := lexC(source)
	if len(lexed.definitions) < 2 || lexed.definitions[0].symbol != `r\u00E9` ||
		lexed.definitions[1].symbol != "tail" {
		t.Fatalf("definitions = %#v, want logical UCN and tail", lexed.definitions)
	}
	foundNumber := false
	for _, token := range lexed.tokens {
		if token.kind == cTokenNumber && token.text == "1é" {
			foundNumber = true
		}
	}
	if !foundNumber {
		t.Fatalf("direct Unicode did not remain inside pp-number: %#v", lexed.tokens)
	}
}

func TestCLexLogicalUCNRejectsBracedIdentifierEscape(t *testing.T) {
	t.Parallel()

	const braced = `\u{0061}`
	if _, _, ok := cLogicalUCN(braced, 0); ok {
		t.Fatal("nonstandard braced UCN was accepted in a logical identifier")
	}
	for _, token := range lexC(braced + `target;`).tokens {
		if token.kind == cTokenIdentifier && token.text == braced+"target" {
			t.Fatalf("braced escape swallowed the following identifier: %#v", token)
		}
	}
}

func TestCLexInvalidIdentifierRuneCannotBridgeADeclaration(t *testing.T) {
	t.Parallel()

	lexed := lexC("int \u037abad(void);\nint good(void);\n")
	for _, definition := range lexed.definitions {
		if definition.symbol == "bad" {
			t.Fatalf("invalid identifier-start punctuation bridged into %#v", definition)
		}
	}
}

func TestCLexEscapedSpliceConsumesNextLogicalLiteralCharacter(t *testing.T) {
	t.Parallel()

	source := "const char *text = \"" + "\\\\\n" +
		"\"; int hidden(void);\nint visible(void);\n"
	lexed := lexC(source)
	masked := maskCSource(source, lexed.stringSpans)
	if strings.Contains(masked, "hidden") {
		t.Fatalf("escaped quote after splice terminated literal early: %q", masked)
	}
	if !strings.Contains(masked, "visible") {
		t.Fatalf("unspliced newline failed to recover literal tail: %q", masked)
	}
}

func TestCLexDirectiveStructureHasIndependentResourceGates(t *testing.T) {
	t.Parallel()

	conditional := "#if " + strings.Repeat("!", cMaximumConcreteExpressionPrefix+1) +
		"READY\n#endif\n"
	if lexC(conditional).concreteEligible {
		t.Fatal("over-cap #if expression remained concrete-parser eligible")
	}
	parameters := "#define F(" + strings.Repeat("(", cMaximumConcreteDelimiterDepth+1) +
		"x" + strings.Repeat(")", cMaximumConcreteDelimiterDepth+1) + ") 0\n"
	if lexC(parameters).concreteEligible {
		t.Fatal("over-cap macro parameter nesting remained parser eligible")
	}
	replacement := "#define F(x) " + strings.Repeat("(", 1024) + "x\n"
	if !lexC(replacement).concreteEligible {
		t.Fatal("replacement-list punctuation incorrectly entered resource gates")
	}
}

func TestCLexDirectiveStructureExactBoundaries(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name string
		at   string
		over string
	}{
		{
			name: "if prefixes",
			at:   "#if " + strings.Repeat("!", cMaximumConcreteExpressionPrefix) + "READY\n",
			over: "#if " + strings.Repeat("!", cMaximumConcreteExpressionPrefix+1) + "READY\n",
		},
		{
			name: "if groups",
			at:   "#if " + strings.Repeat("(READY) && ", cMaximumConcreteGroupsPerSegment) + "1\n",
			over: "#if " + strings.Repeat("(READY) && ", cMaximumConcreteGroupsPerSegment+1) + "1\n",
		},
		{
			name: "if ternaries",
			at: "#if " + strings.Repeat("READY ? ", cMaximumConcreteExpressionPrefix) + "0" +
				strings.Repeat(" : 0", cMaximumConcreteExpressionPrefix) + "\n",
			over: "#if " + strings.Repeat("READY ? ", cMaximumConcreteExpressionPrefix+1) + "0" +
				strings.Repeat(" : 0", cMaximumConcreteExpressionPrefix+1) + "\n",
		},
		{
			name: "macro nesting",
			at: "#define F(" + strings.Repeat("(", cMaximumConcreteDelimiterDepth-1) + "x" +
				strings.Repeat(")", cMaximumConcreteDelimiterDepth-1) + ") 0\n",
			over: "#define F(" + strings.Repeat("(", cMaximumConcreteDelimiterDepth) + "x" +
				strings.Repeat(")", cMaximumConcreteDelimiterDepth) + ") 0\n",
		},
		{
			name: "macro parameters",
			at:   cMacroWithParameters(cMaximumConcreteGroupsPerSegment),
			over: cMacroWithParameters(cMaximumConcreteGroupsPerSegment + 1),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if !lexC(test.at).concreteEligible {
				t.Fatal("source at cap was rejected")
			}
			if lexC(test.over).concreteEligible {
				t.Fatal("source over cap remained eligible")
			}
		})
	}
}

func cMacroWithParameters(count int) string {
	var source strings.Builder
	source.WriteString("#define F(")
	for index := range count {
		if index > 0 {
			source.WriteByte(',')
		}
		source.WriteByte('p')
		source.WriteString(strconv.Itoa(index))
	}
	source.WriteString(") 0\n")
	return source.String()
}

func TestCLexPreprocessorAlternativesCannotPairDelimiters(t *testing.T) {
	t.Parallel()

	const source = `int outer(void) {
#if A
{
#elifdef B
}
#endif
return 0;
}
int tail(void) {}
`
	lexed := lexC(source)
	symbols := make([]string, 0, len(lexed.definitions))
	for _, definition := range lexed.definitions {
		symbols = append(symbols, definition.symbol)
	}
	if !slices.Contains(symbols, "outer") || !slices.Contains(symbols, "tail") {
		t.Fatalf("branch-local mismatch stole surrounding definition: %#v", symbols)
	}
}

func TestCLexDeepBalancedFallbackHasBoundedResolverStack(t *testing.T) {
	open := strings.Repeat("{", 20_000)
	closing := strings.Repeat("}", 20_000)
	lexed := lexC(open + closing + "\nint tail(void) {}\n")
	if lexed.concreteEligible {
		t.Fatal("deep fallback fixture unexpectedly parser eligible")
	}
	if len(lexed.definitions) == 0 ||
		lexed.definitions[len(lexed.definitions)-1].symbol != "tail" {
		t.Fatalf("bounded deep fallback lost tail: %#v", lexed.definitions)
	}
}

func TestCLexStreamingRecoveryRetainsDroppedMiddleDefinition(t *testing.T) {
	var source strings.Builder
	for range 100_000 {
		source.WriteString("0;")
	}
	source.WriteString("int middle_definition;\n")
	for range 100_000 {
		source.WriteString("0;")
	}
	lexed := lexC(source.String())
	if !lexed.truncated || len(lexed.tokens) > cMaximumRetainedLexicalUnits {
		t.Fatalf("retention metadata = truncated %v, tokens %d", lexed.truncated, len(lexed.tokens))
	}
	for _, definition := range lexed.definitions {
		if definition.symbol == "middle_definition" {
			return
		}
	}
	t.Fatalf("dropped-middle definition missing from %#v", lexed.definitions)
}

func TestCLexStreamingRecoveryKeepsNestedDefinitionPolicy(t *testing.T) {
	var source strings.Builder
	for range 100_000 {
		source.WriteString("0;")
	}
	source.WriteString(`struct Mid {
    int field;
};
enum Choice { CHOICE_A, CHOICE_B = 2 };
int outer(void) {
    typedef int local_type;
    int local_prototype(int);
    struct Local { int local_field; };
    int nested(int value) { return value; }
    int ordinary_local;
    return 0;
}
`)
	for range 100_000 {
		source.WriteString("0;")
	}
	lexed := lexC(source.String())
	symbols := make([]string, 0, len(lexed.definitions))
	for _, definition := range lexed.definitions {
		symbols = append(symbols, definition.symbol)
	}
	want := []string{
		"Mid", "field", "Choice", "CHOICE_A", "CHOICE_B", "outer",
		"local_type", "local_prototype", "Local", "local_field", "nested",
	}
	if !slices.Equal(symbols, want) {
		t.Fatalf("streamed nested definitions = %#v, want %#v", symbols, want)
	}
	if slices.Contains(symbols, "ordinary_local") || slices.Contains(symbols, "value") {
		t.Fatalf("streamed policy promoted local/parameter: %#v", symbols)
	}
}
