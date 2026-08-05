package repoview

import (
	"strings"
	"testing"
)

func TestCPPSyntaxParserCoversConcreteLanguageNodes(t *testing.T) {
	t.Parallel()

	const source = `namespace demo {
template <class T>
class Box {
public:
    explicit Box(T value) : value_(value) {}
    T get() const { return value_; }
private:
    T value_;
};
const char *raw = R"tag(/* text, not a comment */ "quoted")tag";
}
`
	tree, ok := parseCPPSyntax(source)
	if !ok || tree == nil || !validateCPPSyntaxTree(tree, len(source)) {
		t.Fatal("valid C++ source did not produce a validated syntax tree")
	}
	wantKinds := map[string]bool{
		"namespace_definition": false,
		"template_declaration": false,
		"class_specifier":      false,
		"function_definition":  false,
		"raw_string_literal":   false,
	}
	for _, node := range tree.nodes {
		if node.kind == "ERROR" {
			t.Fatalf("valid C++ source produced ERROR at bytes %d-%d", node.startByte, node.endByte)
		}
		if _, wanted := wantKinds[node.kind]; wanted {
			wantKinds[node.kind] = true
		}
	}
	for kind, found := range wantKinds {
		if !found {
			t.Errorf("concrete C++ tree omitted %s", kind)
		}
	}
}

func TestCPPSyntaxPreflightEnforcesIndependentByteAndSourceIdentity(t *testing.T) {
	t.Parallel()

	atCap := strings.Repeat(" ", cppMaximumConcreteParseBytes)
	preflight := preflightCPPSyntax(atCap)
	if !preflight.concreteEligible ||
		!validCPPSyntaxPreflight(atCap, preflight) {
		t.Fatal("source at C++ byte cap was rejected")
	}

	overCap := atCap + " "
	over := preflightCPPSyntax(overCap)
	if over.concreteEligible || validCPPSyntaxPreflight(overCap, over) {
		t.Fatal("source above C++ byte cap remained eligible")
	}
	if tree, ok := parseCPPSyntaxWithPreflight(overCap, preflight); ok || tree != nil {
		t.Fatal("preflight for a different source bypassed C++ parser gate")
	}
	if tree, ok := parseCPPSyntax(overCap); ok || tree != nil {
		t.Fatal("source above C++ byte cap reached concrete parser")
	}
}

func TestCPPSyntaxPreflightCapsLexicalAndStructuralFrontiers(t *testing.T) {
	t.Parallel()

	lexicalAtCap := strings.Repeat(";", cppMaximumConcreteLexicalUnits)
	if preflight := preflightCPPSyntax(lexicalAtCap); !preflight.concreteEligible ||
		preflight.lexicalUnits != cppMaximumConcreteLexicalUnits {
		t.Fatalf("lexical cap eligibility = %v, units = %d",
			preflight.concreteEligible, preflight.lexicalUnits)
	}
	if preflight := preflightCPPSyntax(lexicalAtCap + ";"); preflight.concreteEligible {
		t.Fatal("source above lexical-unit cap remained eligible")
	}

	depthAtCap := strings.Repeat("(", cppMaximumConcreteDelimiterDepth) + "0" +
		strings.Repeat(")", cppMaximumConcreteDelimiterDepth)
	if preflight := preflightCPPSyntax(depthAtCap); !preflight.concreteEligible ||
		preflight.delimiterDepth != cppMaximumConcreteDelimiterDepth {
		t.Fatalf("delimiter cap eligibility = %v, depth = %d",
			preflight.concreteEligible, preflight.delimiterDepth)
	}
	depthOverCap := "(" + depthAtCap + ")"
	if preflight := preflightCPPSyntax(depthOverCap); preflight.concreteEligible {
		t.Fatal("source above delimiter-depth cap remained eligible")
	}

	angleAtCap := strings.Repeat("<", cppMaximumConcreteAngleFrontier)
	if preflight := preflightCPPSyntax(angleAtCap); !preflight.concreteEligible ||
		preflight.angleFrontier != cppMaximumConcreteAngleFrontier {
		t.Fatalf("angle cap eligibility = %v, frontier = %d",
			preflight.concreteEligible, preflight.angleFrontier)
	}
	if preflight := preflightCPPSyntax(angleAtCap + "<"); preflight.concreteEligible {
		t.Fatal("source above angle frontier remained eligible")
	}
}

func TestCPPSyntaxPreflightTreatsRawStringBodiesAsOpaque(t *testing.T) {
	t.Parallel()

	body := strings.Repeat("({< /* not a comment */ ", cppMaximumConcreteDelimiterDepth+1)
	source := `const char *value = R"edge(` + body + `)edge";`
	preflight := preflightCPPSyntax(source)
	if !preflight.concreteEligible {
		t.Fatalf("raw-string body inflated structural frontier: %#v", preflight)
	}
	if preflight.delimiterDepth != 0 || preflight.angleFrontier != 0 {
		t.Fatalf("raw-string structural counters = %#v, want zero", preflight)
	}

	tree, ok := parseCPPSyntaxWithPreflight(source, preflight)
	if !ok || tree == nil || !validateCPPSyntaxTree(tree, len(source)) {
		t.Fatal("raw-string fixture did not produce a validated tree")
	}
	foundRaw := false
	for _, node := range tree.nodes {
		foundRaw = foundRaw || node.kind == "raw_string_literal"
	}
	if !foundRaw {
		t.Fatal("raw-string fixture did not produce raw_string_literal")
	}
}

func TestValidateCPPSyntaxTreeRejectsNil(t *testing.T) {
	t.Parallel()

	if validateCPPSyntaxTree(nil, 0) {
		t.Fatal("C++ syntax validator accepted nil tree")
	}
}
