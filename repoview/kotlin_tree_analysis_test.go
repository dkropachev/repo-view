package repoview

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

func TestKotlinTreeAnalysisExtractsDefinitionsScopesImportsAndKDoc(t *testing.T) {
	t.Parallel()

	const source = `package demo

import kotlin.collections.List
import demo.model.Entry as ModelEntry

/** Service docs. */
class Service(val id: Int, label: String) {
    enum class State {
        Idle,
        Active {
            fun code() = 1
        },
    }

    companion object Factory {
        fun create() = Service(1, "")
    }

    object Nested {
        val answer = 42
    }

    typealias Name = String

    constructor() : this(0, "") {
        println(id)
    }

    val value: Int
        get() = id

    fun run() {
        val localValue = 1
        fun local() = ModelEntry()
        if (id > 0) {
            local()
        }
    }
}

typealias Alias = List<String>
fun ` + "`when value`" + `() = Service(0, "")
val topProperty = 1
`
	lines := kotlinTreeTestLines(source)
	tree := kotlinTreeTestParse(t, source)
	if spans := kotlinSyntaxErrorSpans(tree, len(source)); len(spans) != 0 {
		t.Fatalf("valid Kotlin recovery spans = %#v, want none", spans)
	}

	definitions := kotlinTreeDefinitions(source, len(lines), tree)
	wantSymbols := []string{
		"Service", "id", "State", "Idle", "Active", "code", "Factory",
		"create", "Nested", "answer", "Name", "constructor", "value", "run",
		"local", "Alias", "when value", "topProperty",
	}
	if got := kotlinTreeDefinitionSymbols(definitions); !slices.Equal(got, wantSymbols) {
		t.Fatalf("definition symbols = %#v, want %#v", got, wantSymbols)
	}
	for _, forbidden := range []string{
		"ModelEntry", "label", "localValue", "println", "get",
	} {
		if slices.Contains(kotlinTreeDefinitionSymbols(definitions), forbidden) {
			t.Errorf("non-outline binding %q became a definition: %#v", forbidden, definitions)
		}
	}

	service := kotlinTreeDefinitionNamed(t, definitions, "Service")
	serviceDocLine := kotlinTreeLineContaining(t, lines, "/** Service docs. */")
	serviceEndLine := kotlinTreeLineContaining(t, lines, "typealias Alias") - 2
	if !service.ownsScope || service.scopeStart != serviceDocLine ||
		service.scopeEnd != serviceEndLine {
		t.Fatalf(
			"Service scope = %#v, want attached KDoc line %d through %d",
			service,
			serviceDocLine,
			serviceEndLine,
		)
	}
	id := kotlinTreeDefinitionNamed(t, definitions, "id")
	if id.ownsScope || id.line != kotlinTreeLineContaining(t, lines, "class Service") {
		t.Fatalf("constructor property = %#v, want non-owning Service-line definition", id)
	}
	backtick := kotlinTreeDefinitionNamed(t, definitions, "when value")
	backtickLine := lines[backtick.line-1]
	if got := backtickLine[backtick.column-1:]; !strings.HasPrefix(got, "when value`") {
		t.Fatalf("backtick definition column points at %q, want inner identifier", got)
	}

	wantImports := []cLineSpan{
		{start: 3, end: 3},
		{start: 4, end: 4},
	}
	if got := kotlinTreeImports(source, len(lines), tree); !reflect.DeepEqual(got, wantImports) {
		t.Fatalf("imports = %#v, want %#v", got, wantImports)
	}

	scopes := kotlinTreeScopes(source, len(lines), tree)
	for _, want := range []cLineScope{
		{start: serviceDocLine, end: serviceEndLine},
		{
			start: kotlinTreeLineContaining(t, lines, "fun run()"),
			end:   kotlinTreeLineContaining(t, lines, "typealias Alias") - 3,
		},
		{
			start: kotlinTreeLineContaining(t, lines, "if (id > 0)"),
			end:   kotlinTreeLineContaining(t, lines, "if (id > 0)") + 2,
		},
	} {
		if !slices.Contains(scopes, want) {
			t.Errorf("scopes do not contain %#v: %#v", want, scopes)
		}
	}
}

func TestKotlinTreeRecoveryRejectsHeadersButRetainsBodyDeclarations(t *testing.T) {
	t.Parallel()

	headerSource := "class Broken ? { }"
	headerTree := kotlinSyntheticClassTree(headerSource, 13, 14)
	if got := kotlinTreeDefinitions(headerSource, 1, headerTree); len(got) != 0 {
		t.Fatalf("header-recovery definitions = %#v, want none", got)
	}

	bodySource := "class Broken { ? }"
	bodyTree := kotlinSyntheticClassTree(bodySource, 15, 16)
	definitions := kotlinTreeDefinitions(bodySource, 1, bodyTree)
	if got := kotlinTreeDefinitionSymbols(definitions); !slices.Equal(got, []string{"Broken"}) {
		t.Fatalf("body-recovery definitions = %#v, want Broken", got)
	}
	if !definitions[0].ownsScope {
		t.Fatalf("body-recovery definition does not own its class scope: %#v", definitions[0])
	}
}

func TestKotlinSyntaxErrorSpansDistinguishAutomaticSemicolons(t *testing.T) {
	t.Parallel()

	tree := &kotlinSyntaxTree{
		root: 0,
		nodes: []kotlinSyntaxNode{
			{kind: "source_file", startByte: 0, endByte: 8, parent: -1, children: []int{1, 2, 3}},
			{kind: "ERROR", startByte: 1, endByte: 3, parent: 0},
			{kind: "simple_identifier", startByte: 5, endByte: 5, parent: 0},
			{kind: "_automatic_semicolon", startByte: 7, endByte: 7, parent: 0},
		},
	}
	if !validateKotlinSyntaxTree(tree, 8) {
		t.Fatal("synthetic Kotlin recovery tree is invalid")
	}
	wantSpans := []cByteSpan{{start: 1, end: 3}, {start: 5, end: 6}}
	if got := kotlinSyntaxErrorSpans(tree, 8); !reflect.DeepEqual(got, wantSpans) {
		t.Fatalf("recovery spans = %#v, want %#v", got, wantSpans)
	}
	if got, want := kotlinSyntaxErrorContexts(tree), []bool{false, true, true, false}; !reflect.DeepEqual(got, want) {
		t.Fatalf("error contexts = %#v, want %#v", got, want)
	}
}

func TestKotlinConcreteParserResourceGates(t *testing.T) {
	t.Parallel()

	const source = "fun kept() = Unit\n"
	for _, test := range []struct {
		name  string
		lexed kotlinLexResult
	}{
		{
			name: "ineligible",
			lexed: kotlinLexResult{
				lexicalUnits: 4,
			},
		},
		{
			name: "token limit",
			lexed: kotlinLexResult{
				concreteEligible: true,
				lexicalUnits:     kotlinMaximumConcreteTokens + 1,
			},
		},
		{
			name: "delimiter limit",
			lexed: kotlinLexResult{
				concreteEligible:      true,
				maximumDelimiterDepth: kotlinMaximumConcreteDelimiterDepth + 1,
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if tree, ok := parseKotlinSyntax(source, test.lexed); ok || tree != nil {
				t.Fatalf("over-frontier parse = %#v, %t; want nil, false", tree, ok)
			}
		})
	}
	overBytes := strings.Repeat(" ", kotlinMaximumConcreteParseBytes+1)
	if tree, ok := parseKotlinSyntax(overBytes, kotlinLexResult{concreteEligible: true}); ok || tree != nil {
		t.Fatalf("over-byte parse = %#v, %t; want nil, false", tree, ok)
	}
}

func TestKotlinTreeAssociatesSiblingAccessorsAndBodylessInterfaceProperties(t *testing.T) {
	t.Parallel()

	const source = `interface Shape {
    val area: Double
}

class Service {
    /** Title documentation. */
    @Deprecated("old")
    var title: String = ""
        private set
}
`
	lines := kotlinTreeTestLines(source)
	definitions := kotlinTreeDefinitions(
		source, len(lines), kotlinTreeTestParse(t, source),
	)
	area := kotlinTreeDefinitionNamed(t, definitions, "area")
	areaLine := kotlinTreeLineContaining(t, lines, "val area")
	if !area.ownsScope || area.scopeStart != areaLine || area.scopeEnd != areaLine {
		t.Fatalf("bodyless interface property = %#v, want owning declaration line", area)
	}
	title := kotlinTreeDefinitionNamed(t, definitions, "title")
	docLine := kotlinTreeLineContaining(t, lines, "/** Title documentation. */")
	setterLine := kotlinTreeLineContaining(t, lines, "private set")
	if !title.ownsScope || title.scopeStart != docLine || title.scopeEnd != setterLine {
		t.Fatalf(
			"sibling-accessor property = %#v, want documented scope %d-%d",
			title, docLine, setterLine,
		)
	}
}

func TestKotlinTreeRecoversAllTargetClassExtent(t *testing.T) {
	t.Parallel()

	const source = `/** Service documentation. */
@Deprecated("old")
class Service(
    @all:Tracked
    val dependency: Dependency,
) {
    /** Value documentation. */
    @get:Tracked
    @set:Tracked
    var value: String by delegate()
        private set

    fun run() = Unit
}
`
	lines := kotlinTreeTestLines(source)
	tree := kotlinTreeTestParse(t, source)
	if spans := kotlinSyntaxErrorSpans(tree, len(source)); len(spans) == 0 {
		t.Fatal("pinned grammar unexpectedly accepted @all without recovery")
	}
	service := kotlinTreeDefinitionNamed(
		t, kotlinTreeDefinitions(source, len(lines), tree), "Service",
	)
	if !service.ownsScope || service.scopeStart != 1 || service.scopeEnd != len(lines) {
		t.Fatalf("recovered @all class = %#v, want documented full extent", service)
	}
	value := kotlinTreeDefinitionNamed(
		t, kotlinTreeDefinitions(source, len(lines), tree), "value",
	)
	valueDocLine := kotlinTreeLineContaining(t, lines, "/** Value documentation. */")
	setterLine := kotlinTreeLineContaining(t, lines, "private set")
	if !value.ownsScope || value.scopeStart != valueDocLine ||
		value.scopeEnd != setterLine {
		t.Fatalf(
			"recovered property = %#v, want attached scope %d-%d",
			value, valueDocLine, setterLine,
		)
	}
}

func TestKotlinKDocAttachmentCrossesAnnotationSiblings(t *testing.T) {
	t.Parallel()

	const source = "/** docs */\n@Mark\nclass C"
	commentEnd := strings.IndexByte(source, '\n')
	annotationStart := commentEnd + 1
	annotationEnd := annotationStart + len("@Mark")
	classStart := strings.Index(source, "class C")
	tree := &kotlinSyntaxTree{
		root: 0,
		nodes: []kotlinSyntaxNode{
			{kind: "source_file", startByte: 0, endByte: len(source), parent: -1, children: []int{1, 2, 3}},
			{kind: "multiline_comment", startByte: 0, endByte: commentEnd, parent: 0},
			{kind: "annotation", startByte: annotationStart, endByte: annotationEnd, parent: 0},
			{kind: "class_declaration", startByte: classStart, endByte: len(source), parent: 0},
		},
	}
	starts := kotlinSyntaxAttachedStarts(source, tree)
	if len(starts) != len(tree.nodes) || starts[3] != 0 {
		t.Fatalf("annotation-bridged KDoc starts = %#v, want class start 0", starts)
	}
}

func kotlinTreeTestParse(t *testing.T, source string) *kotlinSyntaxTree {
	t.Helper()
	lexed := lexKotlin(source)
	if !lexed.concreteEligible {
		t.Fatal("small valid Kotlin fixture is not concrete-eligible")
	}
	tree, ok := parseKotlinSyntax(source, lexed)
	if !ok || !validateKotlinSyntaxTree(tree, len(source)) {
		t.Fatal("valid Kotlin fixture did not produce a validated concrete tree")
	}
	return tree
}

func kotlinTreeTestLines(source string) []string {
	return strings.Split(strings.TrimSuffix(source, "\n"), "\n")
}

func kotlinTreeDefinitionSymbols(definitions []sourceDefinition) []string {
	result := make([]string, len(definitions))
	for index, definition := range definitions {
		result[index] = definition.symbol
	}
	return result
}

func kotlinTreeDefinitionNamed(
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
	t.Fatalf("definition %q missing from %#v", symbol, definitions)
	return sourceDefinition{}
}

func kotlinTreeLineContaining(t *testing.T, lines []string, marker string) int {
	t.Helper()
	for index, line := range lines {
		if strings.Contains(line, marker) {
			return index + 1
		}
	}
	t.Fatalf("marker %q missing from source", marker)
	return 0
}

func kotlinSyntheticClassTree(
	source string,
	errorStart, errorEnd int,
) *kotlinSyntaxTree {
	bodyStart := strings.Index(source, "{")
	if errorStart < bodyStart {
		return &kotlinSyntaxTree{
			root: 0,
			nodes: []kotlinSyntaxNode{
				{
					kind: "source_file", startByte: 0, endByte: len(source), parent: -1,
					children: []int{1},
				},
				{
					kind: "class_declaration", startByte: 0, endByte: len(source), parent: 0,
					children: []int{2, 3, 4},
				},
				{kind: "type_identifier", startByte: 6, endByte: 12, parent: 1},
				{kind: "ERROR", startByte: errorStart, endByte: errorEnd, parent: 1},
				{kind: "class_body", startByte: bodyStart, endByte: len(source), parent: 1},
			},
		}
	}
	return &kotlinSyntaxTree{
		root: 0,
		nodes: []kotlinSyntaxNode{
			{
				kind: "source_file", startByte: 0, endByte: len(source), parent: -1,
				children: []int{1},
			},
			{
				kind: "class_declaration", startByte: 0, endByte: len(source), parent: 0,
				children: []int{2, 3},
			},
			{kind: "type_identifier", startByte: 6, endByte: 12, parent: 1},
			{
				kind: "class_body", startByte: bodyStart, endByte: len(source), parent: 1,
				children: []int{4},
			},
			{kind: "ERROR", startByte: errorStart, endByte: errorEnd, parent: 3},
		},
	}
}
