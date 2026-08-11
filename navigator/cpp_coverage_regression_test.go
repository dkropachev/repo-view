package navigator

import (
	"slices"
	"testing"
)

func TestCPPQualifiedAggregateDefinitionsRemainSourceBacked(t *testing.T) {
	t.Parallel()

	const source = `namespace model {
struct Record;
}
struct model::Record {
    int value;
    static int shared;
};
int model::Record::shared = 1;
`
	lines := cppTestLines(source)
	definitions := newCPPLanguage().sourceDefinitions(lines)
	want := []string{"model", "Record", "Record", "value", "shared", "shared"}
	if got := cppDefinitionSymbols(definitions); !slices.Equal(got, want) {
		t.Fatalf("qualified aggregate definitions = %#v, want %#v", got, want)
	}
	for _, definition := range definitions {
		cppAssertDefinitionCoordinate(t, lines, definition)
	}
}

func TestCPPQualifiedTemplateAggregateDefinitionIsNotDropped(t *testing.T) {
	t.Parallel()

	const source = `namespace model {
template<class T> struct Box;
}
template<class T> struct model::Box<T> {
    T payload;
};
`
	lines := cppTestLines(source)
	definitions := newCPPLanguage().sourceDefinitions(lines)
	want := []string{"model", "Box", "Box", "payload"}
	if got := cppDefinitionSymbols(definitions); !slices.Equal(got, want) {
		t.Fatalf("qualified template aggregate definitions = %#v, want %#v", got, want)
	}
	for _, definition := range definitions {
		cppAssertDefinitionCoordinate(t, lines, definition)
	}
}

func TestCPPWholeFileErrorWrapperPreservesRecognizedRootDeclarations(t *testing.T) {
	t.Parallel()

	const source = `namespace api { int run(); }
namespace alias = api;
using api::run;
using Count = unsigned long;
static_assert(sizeof(Count) > 0);
template<class T> concept Number = requires(T value) { value + value; };
template<class T> T identity(T value);
template int identity<int>(int value);
class Forward;
`
	tree, ok := parseCPPSyntax(source)
	if !ok {
		t.Fatal("parseCPPSyntax rejected recognized root declarations")
	}
	tree.nodes = append([]treeSitterSyntaxNode(nil), tree.nodes...)
	tree.nodes[tree.root].kind = "ERROR"

	definitions := cppTreeDefinitions(
		source, len(cppTestLines(source)), tree,
	)
	want := []string{
		"api", "run", "alias", "Count", "Number", "identity", "Forward",
	}
	if got := cppDefinitionSymbols(definitions); !slices.Equal(got, want) {
		t.Fatalf("whole-file wrapper definitions = %#v, want %#v", got, want)
	}
	if contexts := cppSyntaxErrorContexts(tree); contexts[tree.root] {
		t.Fatalf("whole-file parser wrapper became an error context: %#v", contexts)
	}
}

func TestCPPFindDropCommentsPreservesRawLiteralsAndLineCoordinates(t *testing.T) {
	t.Parallel()

	const source = `int target();
int caller()
{
    /* target in
       a block comment */
    const char *raw = R"tag(/* raw stays */ // raw stays)tag";
    return target(); // trailing comment
}
`
	root := t.TempDir()
	writeFile(t, root, "comments.cpp", source)
	response, err := mustView(t, root).Find("target", Options{
		Include:      IncludeRefs,
		Return:       ReturnScope,
		DropComments: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 {
		t.Fatalf("Find target results = %#v, want one reference", response.Results)
	}
	result := response.Results[0]
	wantCode := `int caller()
{
    const char *raw = R"tag(/* raw stays */ // raw stays)tag";
    return target();
}`
	if result.StartLine != 2 || result.EndLine != 8 || result.Line != 7 ||
		result.Code != wantCode {
		t.Fatalf("comment-cleaned scope = %#v, want lines 2-8 with code %q", result, wantCode)
	}
}

func TestCPPStripCommentHonorsLiterals(t *testing.T) {
	t.Parallel()

	backend := newCPPLanguage()
	tests := []struct {
		name   string
		source string
		want   string
	}{
		{
			name:   "literal markers",
			source: `const char *value = "// literal"; /* comment */`,
			want:   `const char *value = "// literal";`,
		},
		{
			name:   "raw literal markers",
			source: `auto value = R"tag(/* literal */ // literal)tag"; // comment`,
			want:   `auto value = R"tag(/* literal */ // literal)tag";`,
		},
		{
			name:   "line comment",
			source: "int value = 1; // comment",
			want:   "int value = 1;",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := backend.stripComment(test.source); got != test.want {
				t.Fatalf("stripComment(%q) = %q, want %q", test.source, got, test.want)
			}
		})
	}
}

func TestCPPWholeFileErrorWrapperStopsAtInvalidRootStatement(t *testing.T) {
	t.Parallel()

	const source = `namespace kept { int before(); }
return;
namespace untrusted { int after(); }
`
	tree, ok := parseCPPSyntax(source)
	if !ok {
		t.Fatal("parseCPPSyntax rejected malformed root-statement fixture")
	}
	tree.nodes = append([]treeSitterSyntaxNode(nil), tree.nodes...)
	tree.nodes[tree.root].kind = "ERROR"

	definitions := cppTreeDefinitions(
		source, len(cppTestLines(source)), tree,
	)
	if got, want := cppDefinitionSymbols(definitions), []string{"kept", "before"}; !slices.Equal(got, want) {
		t.Fatalf("definitions across invalid root statement = %#v, want %#v", got, want)
	}
}
