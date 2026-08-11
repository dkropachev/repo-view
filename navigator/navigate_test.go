package navigator

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestFindReturnsScope(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "found.go", "package demo\n\nfunc helper() {}\n\nfunc caller() {\n\thelper()\n}\n")

	view := mustView(t, root)
	response, err := view.Find("helper", Options{Include: IncludeRefs, Return: ReturnScope})
	if err != nil {
		t.Fatal(err)
	}

	if len(response.Results) != 1 {
		t.Fatalf("results = %#v", response.Results)
	}
	got := response.Results[0]
	if got.Kind != "ref" || got.Path != "found.go" || got.StartLine != 5 || got.EndLine != 7 {
		t.Fatalf("result = %#v", got)
	}
	if !strings.Contains(got.Code, "func caller()") || !strings.Contains(got.Code, "helper()") {
		t.Fatalf("code = %q", got.Code)
	}
}

func TestFindDeduplicatesBeforeLimitAndReportsTruncation(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "found.go", "package demo\n\nfunc helper() {}\n\nfunc first() {\n\thelper()\n\thelper()\n}\n\nfunc second() {\n\thelper()\n}\n")

	view := mustView(t, root)
	response, err := view.Find("helper", Options{
		Include: IncludeRefs,
		Return:  ReturnScope,
		Limit:   1,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].Scope != "first" {
		t.Fatalf("results = %#v", response.Results)
	}
	if !response.ResultsTruncated {
		t.Fatalf("response = %#v", response)
	}
}

func TestFindManySharesLimitAndPreservesEverySymbol(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "found.go", `package demo

func Alpha() {}
func Bravo() {}

func first() { Alpha() }
func second() { Alpha() }
func third() { Bravo() }
`)

	view := mustView(t, root)
	responses, err := view.FindMany(
		[]string{"Alpha", "Bravo", "Missing"},
		Options{Include: IncludeRefs, Return: ReturnLocations, Limit: 3},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(responses) != 3 {
		t.Fatalf("responses = %#v", responses)
	}
	if responses[0].Symbol != "Alpha" || len(responses[0].Results) != 1 ||
		!responses[0].ResultsTruncated {
		t.Fatalf("Alpha response = %#v", responses[0])
	}
	if responses[1].Symbol != "Bravo" || len(responses[1].Results) != 1 ||
		responses[1].ResultsTruncated {
		t.Fatalf("Bravo response = %#v", responses[1])
	}
	if responses[2].Symbol != "Missing" || responses[2].Results == nil ||
		len(responses[2].Results) != 0 || responses[2].ResultsTruncated {
		t.Fatalf("Missing response = %#v", responses[2])
	}

	exhausted, err := view.FindMany(
		[]string{"Alpha", "Bravo", "Missing"},
		Options{Include: IncludeRefs, Return: ReturnLocations, Limit: 1},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(exhausted) != 1 || exhausted[0].Symbol != "Alpha" ||
		len(exhausted[0].Results) != 1 || !exhausted[0].ResultsTruncated {
		t.Fatalf("exhausted responses = %#v", exhausted)
	}
}

func TestFindManyMaximumLimitDoesNotOverflow(t *testing.T) {
	view := mustView(t, t.TempDir())
	maxInt := int(^uint(0) >> 1)
	responses, err := view.FindMany(
		[]string{"Alpha", "Bravo"},
		Options{Return: ReturnLocations, Limit: maxInt},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(responses) != 2 {
		t.Fatalf("responses = %#v", responses)
	}
	for _, response := range responses {
		if response.Results == nil || len(response.Results) != 0 || response.ResultsTruncated {
			t.Fatalf("response = %#v", response)
		}
	}
}

func TestFindMaximumContextDoesNotOverflow(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "found.java", "class Target {}\n")

	view := mustView(t, root)
	maxInt := int(^uint(0) >> 1)
	response, err := view.Find(
		"Target",
		Options{Include: IncludeDefs, Return: ReturnContext, Context: maxInt},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 {
		t.Fatalf("results = %#v", response.Results)
	}
	result := response.Results[0]
	if result.StartLine != 1 || result.EndLine != 1 || result.Code != "class Target {}" {
		t.Fatalf("result = %#v", result)
	}
}

func TestInspectReturnsScopeAndRelatedSymbolResults(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "found.go", "package demo\n\nfunc helper() {}\n\nfunc caller() {\n\thelper()\n}\n")

	view := mustView(t, root)
	response, err := view.Inspect("found.go:6", Options{Include: IncludeBoth, Return: ReturnContext, Context: 1})
	if err != nil {
		t.Fatal(err)
	}

	if response.Symbol != "helper" {
		t.Fatalf("symbol = %q", response.Symbol)
	}
	if len(response.Results) < 2 {
		t.Fatalf("results = %#v", response.Results)
	}
	if response.Results[0].Kind != "scope" {
		t.Fatalf("first result = %#v", response.Results[0])
	}
}

func TestInspectHonorsTotalLimitAndSignalsCodeTruncation(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "found.go", "package demo\n\nfunc helper() {}\n\nfunc caller() {\n\thelper()\n\thelper()\n\thelper()\n}\n")

	view := mustView(t, root)
	response, err := view.Inspect("found.go:6", Options{
		Include:      IncludeAll,
		Return:       ReturnScope,
		Limit:        2,
		MaxCodeLines: 2,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(response.Results) != 2 {
		t.Fatalf("results = %#v", response.Results)
	}
	if !response.ResultsTruncated {
		t.Fatalf("response = %#v", response)
	}
	if !response.Results[0].CodeTruncated {
		t.Fatalf("scope result = %#v", response.Results[0])
	}
}

func TestInspectExactLimitOnlySignalsRealRelatedTruncation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		path          string
		source        string
		wantSymbol    string
		wantTruncated bool
	}{
		{
			name:       "Go without related references",
			path:       "solo.go",
			source:     "package demo\nfunc solo() {}\n",
			wantSymbol: "solo",
		},
		{
			name:       "Java without related references",
			path:       "Solo.java",
			source:     "class Solo {\n void alone() {}\n}\n",
			wantSymbol: "alone",
		},
		{
			name:          "Go with a related reference",
			path:          "used.go",
			source:        "package demo\nfunc used() {}\nfunc caller() { used() }\n",
			wantSymbol:    "used",
			wantTruncated: true,
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			writeFile(t, root, testCase.path, testCase.source)

			response, err := mustView(t, root).Inspect(
				testCase.path+":2",
				Options{Include: IncludeRefs, Return: ReturnLocations, Limit: 1},
			)
			if err != nil {
				t.Fatal(err)
			}
			if response.Symbol != testCase.wantSymbol {
				t.Fatalf("symbol = %q, want %q", response.Symbol, testCase.wantSymbol)
			}
			if len(response.Results) != 1 || response.Results[0].Kind != "scope" {
				t.Fatalf("results = %#v", response.Results)
			}
			if response.ResultsTruncated != testCase.wantTruncated {
				t.Fatalf(
					"results_truncated = %v, want %v; response = %#v",
					response.ResultsTruncated, testCase.wantTruncated, response,
				)
			}
		})
	}
}

func TestInspectHonorsRequestedFileFilters(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "scopesifter@example.test")
	runGit(t, root, "config", "user.name", "scopesifter test")
	writeFile(t, root, "selected.go", "package demo\n\nfunc selected() {}\n")
	writeFile(t, root, "other.go", "package demo\n\nfunc other() {}\n")
	runGit(t, root, "add", "selected.go", "other.go")
	runGit(t, root, "commit", "-m", "initial")

	view := mustView(t, root)
	for name, options := range map[string]Options{
		"include": {
			PathGlobs: []string{"other.go"},
		},
		"exclude": {
			ExcludeGlobs: []string{"selected.go"},
		},
		"changed only": {
			ChangedOnly: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			options.Include = IncludeScope
			options.Return = ReturnLocations
			if _, err := view.Inspect("selected.go:3", options); err == nil {
				t.Fatalf("Inspect unexpectedly accepted filtered path with %#v", options)
			}
		})
	}

	writeFile(t, root, "selected.go", "package demo\n\nfunc changed() {}\n")
	if _, err := view.Inspect("selected.go:3", Options{
		Include: IncludeScope, Return: ReturnLocations, ChangedOnly: true,
	}); err != nil {
		t.Fatalf("Inspect rejected changed selected path: %v", err)
	}
}

func TestInspectImportsHonorReturnCleaningAndCodeLimit(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "found.go", `package demo

import (
	"fmt"
	// dependency comment
	"os"
	"strings"
)

func run() { fmt.Println(os.Args, strings.Builder{}) }
`)
	view := mustView(t, root)

	locations, err := view.Inspect("found.go:10", Options{
		Include: IncludeImports, Return: ReturnLocations, MaxCodeLines: 1,
		DropComments: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, result := range locations.Results {
		if result.Code != "" || result.CodeStartLine != 0 || result.CodeEndLine != 0 ||
			result.CodeTruncated {
			t.Fatalf("locations result embedded import code: %#v", result)
		}
	}

	lined, err := view.Inspect("found.go:10", Options{
		Include: IncludeImports, Return: ReturnLine,
	})
	if err != nil {
		t.Fatal(err)
	}
	var linedImports *Result
	for index := range lined.Results {
		if lined.Results[index].Kind == "imports" {
			linedImports = &lined.Results[index]
			break
		}
	}
	if linedImports == nil || linedImports.Code != "import (" ||
		linedImports.CodeStartLine != 3 || linedImports.CodeEndLine != 3 {
		t.Fatalf("line-mode imports = %#v", linedImports)
	}

	contextual, err := view.Inspect("found.go:10", Options{
		Include: IncludeImports, Return: ReturnContext, Context: 1, ContextSet: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var contextualImports *Result
	for index := range contextual.Results {
		if contextual.Results[index].Kind == "imports" {
			contextualImports = &contextual.Results[index]
			break
		}
	}
	if contextualImports == nil || contextualImports.CodeStartLine != 2 ||
		contextualImports.CodeEndLine != 9 {
		t.Fatalf("context-mode imports = %#v", contextualImports)
	}

	wideContext, err := view.Inspect("found.go:10", Options{
		Include: IncludeImports, Return: ReturnContext,
		Context: math.MaxInt, ContextSet: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var wideImports *Result
	for index := range wideContext.Results {
		if wideContext.Results[index].Kind == "imports" {
			wideImports = &wideContext.Results[index]
			break
		}
	}
	if wideImports == nil || wideImports.CodeStartLine != 1 ||
		wideImports.CodeEndLine != 10 {
		t.Fatalf("maximum-context imports = %#v", wideImports)
	}

	scoped, err := view.Inspect("found.go:10", Options{
		Include: IncludeImports, Return: ReturnScope, MaxCodeLines: 4,
		DropComments: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	var imports *Result
	for index := range scoped.Results {
		if scoped.Results[index].Kind == "imports" {
			imports = &scoped.Results[index]
			break
		}
	}
	if imports == nil {
		t.Fatalf("Inspect result has no imports: %#v", scoped.Results)
	}
	if !imports.CodeTruncated || imports.CodeStartLine == 0 || imports.CodeEndLine == 0 ||
		imports.CodeEndLine-imports.CodeStartLine+1 > 4 ||
		strings.Contains(imports.Code, "dependency comment") {
		t.Fatalf("bounded cleaned imports = %#v", *imports)
	}
}

func TestInspectPreservesExplicitZeroContext(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "found.go", `package demo

func run() {
	println("before")
	target()
	println("after")
}
`)
	view := mustView(t, root)
	response, err := view.Inspect("found.go:5", Options{
		Include:    IncludeScope,
		Return:     ReturnContext,
		Context:    0,
		ContextSet: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 {
		t.Fatalf("results = %#v", response.Results)
	}
	result := response.Results[0]
	if result.StartLine != 5 || result.EndLine != 5 || result.Code != "\ttarget()" {
		t.Fatalf("zero-context result = %#v", result)
	}
}

func TestInspectDefaultsOmittedContextToFive(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "found.go", `package demo

func run() {
	println("one")
	println("two")
	println("three")
	target()
	println("four")
	println("five")
	println("six")
}
`)
	response, err := mustView(t, root).Inspect("found.go:7", Options{
		Include: IncludeScope,
		Return:  ReturnContext,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 {
		t.Fatalf("results = %#v", response.Results)
	}
	result := response.Results[0]
	if result.StartLine != 2 || result.EndLine != 11 {
		t.Fatalf("default context result = %#v", result)
	}
}

func TestFindUsesSameLineNestedDefinitionAtHitPosition(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		path   string
		source string
		want   string
	}{
		{
			name: "Java", path: "One.java",
			source: "class C { void first() {} void second() { target(); } }\n",
			want:   "second",
		},
		{
			name: "Java first owner", path: "First.java",
			source: "class C { void first() { target(); } void second() {} }\n",
			want:   "first",
		},
		{
			name: "Java translated multiline owner", path: "Translated.java",
			source: "class C \\u007b void first() \\u007b target();\n" +
				"\\u007d \\u007d\n",
			want: "first",
		},
		{
			name: "C digraph multiline owner", path: "Digraph.c",
			source: "void first(void) <% target();\n" +
				"%>\n",
			want: "first",
		},
		{
			name: "C sharp", path: "One.cs",
			source: "class C { void First() {} void Second() { target(); } }\n",
			want:   "Second",
		},
		{
			name: "C sharp file-scoped namespace", path: "Namespace.cs",
			source: "namespace N; [target] class D {}\n",
			want:   "N",
		},
		{
			name: "C plus plus", path: "One.cpp",
			source: "class C { void first() {} void second() { target(); } };\n",
			want:   "second",
		},
		{
			name: "C plus plus digraph multiline owner", path: "Digraph.cpp",
			source: "void first() <% target();\n" +
				"%>\n",
			want: "first",
		},
		{
			name: "Kotlin", path: "One.kt",
			source: "class C { fun first() {} fun second() { target() } }\n",
			want:   "second",
		},
		{
			name: "Swift", path: "One.swift",
			source: "class C { func first() {} func second() { target() } }\n",
			want:   "second",
		},
		{
			name: "Modula", path: "One.mod",
			source: "MODULE C; PROCEDURE First; BEGIN END First; PROCEDURE Second; BEGIN target END Second; BEGIN END C.\n",
			want:   "Second",
		},
		{
			name: "Modula multiline owner", path: "Multiline.mod",
			source: "MODULE C; PROCEDURE First; BEGIN target;\n" +
				"END First; BEGIN END C.\n",
			want: "First",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, root, testCase.path, testCase.source)
			response, err := mustView(t, root).Find("target", Options{
				Include: IncludeRefs,
				Return:  ReturnScope,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(response.Results) != 1 || response.Results[0].Scope != testCase.want {
				t.Fatalf("same-line nested result = %#v, want scope %q", response.Results, testCase.want)
			}
		})
	}
}

func TestFindUsesQualifiedLogicalOccurrencePositionOnDenseLine(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		path   string
		source string
		symbol string
		want   string
	}{
		{
			name:   "Java",
			path:   "One.java",
			source: "class C { void first() {} void second() { Widget /*x*/ . run(); } }\n",
			symbol: "Widget.run",
			want:   "second",
		},
		{
			name:   "Java keeps earlier physical occurrence",
			path:   "Earlier.java",
			source: "class C { void first() { Widget.run(); } void second() { Widget /*x*/ . run(); } }\n",
			symbol: "Widget.run",
			want:   "first",
		},
		{
			name:   "Java removes rejected numeric composite",
			path:   "Numeric.java",
			source: "class C { void first() { double n = 0x1.deadp0.foo; } void second() { Object x = deadp0.foo; } }\n",
			symbol: "deadp0.foo",
			want:   "second",
		},
		{
			name:   "Java removes rejected numeric before hidden reference",
			path:   "NumericHidden.java",
			source: "class C { void first() { double n = 0x1.deadp0.foo; } void second() { Object x = deadp0 /*x*/ . foo; } }\n",
			symbol: "deadp0.foo",
			want:   "second",
		},
		{
			name: "Java reference after multiline method closes",
			path: "Closed.java",
			source: "class C {\n" +
				"  void first() {\n" +
				"  } Object field = Widget /*x*/ . run();\n" +
				"}\n",
			symbol: "Widget.run",
			want:   "C",
		},
		{
			name:   "C sharp",
			path:   "One.cs",
			source: "class C { void First() {} void Second() { Widget /*x*/ . Run(); } }\n",
			symbol: "Widget.Run",
			want:   "Second",
		},
		{
			name: "C sharp reference before multiline method closes",
			path: "BeforeClosed.cs",
			source: "class C {\n" +
				"  void First() {\n" +
				"    Widget /*x*/ . Run(); } object field = null;\n" +
				"}\n",
			symbol: "Widget.Run",
			want:   "First",
		},
		{
			name: "C sharp reference after multiline method closes",
			path: "AfterClosed.cs",
			source: "class C {\n" +
				"  void First() {\n" +
				"  } object field = Widget /*x*/ . Run();\n" +
				"}\n",
			symbol: "Widget.Run",
			want:   "C",
		},
		{
			name: "C sharp equal-span reference before method closes",
			path: "EqualBeforeClosed.cs",
			source: "class C { void First() {\n" +
				"  Widget /*x*/ . Run(); } object field = null; }\n",
			symbol: "Widget.Run",
			want:   "First",
		},
		{
			name: "C sharp equal-span reference after method closes",
			path: "EqualAfterClosed.cs",
			source: "class C { void First() {\n" +
				"  } object field = Widget /*x*/ . Run(); }\n",
			symbol: "Widget.Run",
			want:   "C",
		},
		{
			name:   "C sharp keeps earlier physical occurrence",
			path:   "Earlier.cs",
			source: "class C { void First() { Widget.Run(); } void Second() { Widget /*x*/ . Run(); } }\n",
			symbol: "Widget.Run",
			want:   "First",
		},
		{
			name:   "C sharp reference after qualified definition",
			path:   "DefinitionFirst.cs",
			source: "namespace Widget /*d*/ . Run { class First {} class Second { object x = Widget /*r*/ . Run; } }\n",
			symbol: "Widget.Run",
			want:   "Second",
		},
		{
			name:   "C sharp removes verbatim-prefix substring",
			path:   "VerbatimPrefix.cs",
			source: "class C { void First() { @Widget.Run(); } void Second() { Widget.Run(); } }\n",
			symbol: "Widget.Run",
			want:   "Second",
		},
		{
			name:   "Kotlin",
			path:   "One.kt",
			source: "class C { fun first() {} fun second() { Widget /*x*/ . run() } }\n",
			symbol: "Widget.run",
			want:   "second",
		},
		{
			name: "Kotlin reference before multiline function closes",
			path: "BeforeClosed.kt",
			source: "class C {\n" +
				"  fun first() {\n" +
				"    Widget /*x*/ . run() }; val field = 0\n" +
				"}\n",
			symbol: "Widget.run",
			want:   "first",
		},
		{
			name: "Kotlin reference after multiline function closes",
			path: "AfterClosed.kt",
			source: "class C {\n" +
				"  fun first() {\n" +
				"  }; val field = Widget /*x*/ . run()\n" +
				"}\n",
			symbol: "Widget.run",
			want:   "C",
		},
		{
			name: "Kotlin equal-span reference before function closes",
			path: "EqualBeforeClosed.kt",
			source: "class C { fun first() {\n" +
				"  Widget /*x*/ . run() }; val field = 0 }\n",
			symbol: "Widget.run",
			want:   "first",
		},
		{
			name: "Kotlin equal-span reference after function closes",
			path: "EqualAfterClosed.kt",
			source: "class C { fun first() {\n" +
				"  }; val field = Widget /*x*/ . run() }\n",
			symbol: "Widget.run",
			want:   "C",
		},
		{
			name:   "Kotlin keeps earlier physical occurrence",
			path:   "Earlier.kt",
			source: "class C { fun first() { Widget.run() } fun second() { Widget /*x*/ . run() } }\n",
			symbol: "Widget.run",
			want:   "first",
		},
		{
			name:   "Swift",
			path:   "One.swift",
			source: "class C { func first() {} func second() { Widget /*x*/ . run() } }\n",
			symbol: "Widget.run",
			want:   "second",
		},
		{
			name: "Swift reference before multiline function closes",
			path: "BeforeClosed.swift",
			source: "class C {\n" +
				"  func first() {\n" +
				"    Widget /*x*/ . run() }; let field = 0\n" +
				"}\n",
			symbol: "Widget.run",
			want:   "first",
		},
		{
			name: "Swift reference after multiline function closes",
			path: "AfterClosed.swift",
			source: "class C {\n" +
				"  func first() {\n" +
				"  }; let field = Widget /*x*/ . run()\n" +
				"}\n",
			symbol: "Widget.run",
			want:   "C",
		},
		{
			name: "Swift equal-span reference before function closes",
			path: "EqualBeforeClosed.swift",
			source: "class C { func first() {\n" +
				"  Widget /*x*/ . run() }; let field = 0 }\n",
			symbol: "Widget.run",
			want:   "first",
		},
		{
			name: "Swift equal-span reference after function closes",
			path: "EqualAfterClosed.swift",
			source: "class C { func first() {\n" +
				"  }; let field = Widget /*x*/ . run() }\n",
			symbol: "Widget.run",
			want:   "C",
		},
		{
			name:   "Swift keeps earlier physical occurrence",
			path:   "Earlier.swift",
			source: "class C { func first() { Widget.run() } func second() { Widget /*x*/ . run() } }\n",
			symbol: "Widget.run",
			want:   "first",
		},
		{
			name:   "Swift quoted qualified definition precedes reference",
			path:   "Quoted.swift",
			source: "extension `Outer`.`Inner` { func first() {} func second() { `Outer`.`Inner`() } }\n",
			symbol: "Outer.Inner",
			want:   "second",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, root, testCase.path, testCase.source)
			response, err := mustView(t, root).Find(testCase.symbol, Options{
				Include: IncludeRefs,
				Return:  ReturnScope,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(response.Results) != 1 || response.Results[0].Scope != testCase.want {
				t.Fatalf("qualified same-line result = %#v, want scope %q",
					response.Results, testCase.want)
			}
		})
	}
}

func TestFindSameLinePositionDoesNotReuseClosedOrDefinitionScope(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		path   string
		source string
		want   string
	}{
		{
			name:   "after closed method",
			path:   "One.java",
			source: "class C { void first() {} int field = target(); void second() {} }\n",
			want:   "C",
		},
		{
			name: "after closed Java method declared with multiline class",
			path: "Multiline.java",
			source: "class C { void first() {} int field = target();\n" +
				"}\n",
			want: "C",
		},
		{
			name: "after closed C sharp method declared with multiline class",
			path: "Multiline.cs",
			source: "class C { void First() {} object field = target();\n" +
				"}\n",
			want: "C",
		},
		{
			name: "after closed Kotlin function declared with multiline class",
			path: "Multiline.kt",
			source: "class C { fun first() {}; val field = target()\n" +
				"}\n",
			want: "C",
		},
		{
			name: "after closed Swift function declared with multiline class",
			path: "Multiline.swift",
			source: "class C { func first() {}; let field = target()\n" +
				"}\n",
			want: "C",
		},
		{
			name:   "reference after same-line definition",
			path:   "One.java",
			source: "class C { void target() {} void second() { target(); } }\n",
			want:   "second",
		},
		{
			name: "closed method inside multiline class",
			path: "One.java",
			source: "class C {\n" +
				"  void first() {} int field = target();\n" +
				"}\n",
			want: "C",
		},
		{
			name:   "Java identifier boundary",
			path:   "One.java",
			source: "class C { void first() { int $target = 0; } void second() { target(); } }\n",
			want:   "second",
		},
		{
			name:   "C sharp identifier boundary",
			path:   "One.cs",
			source: "class C { void First() { int @target = 0; } void Second() { target(); } }\n",
			want:   "Second",
		},
		{
			name:   "C sharp expression body",
			path:   "One.cs",
			source: "class C { int First() => target(); int Second() => 0; }\n",
			want:   "First",
		},
		{
			name:   "after C sharp expression body",
			path:   "One.cs",
			source: "class C { int First() => 0; int field = target(); }\n",
			want:   "C",
		},
		{
			name:   "Kotlin expression body",
			path:   "One.kt",
			source: "class C { fun first() = target(); fun second() = 0 }\n",
			want:   "first",
		},
		{
			name:   "after Kotlin expression body",
			path:   "One.kt",
			source: "class C { fun first() = 0; val field = target() }\n",
			want:   "C",
		},
		{
			name:   "Java Unicode escape body delimiters",
			path:   "One.java",
			source: `class C { void first() \u007b target(); \u007d }` + "\n",
			want:   "first",
		},
		{
			name:   "Modula set constructor is not a scope",
			path:   "One.mod",
			source: "MODULE C; PROCEDURE First; BEGIN END First; BEGIN x := {target}; END C.\n",
			want:   "C",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, root, testCase.path, testCase.source)
			response, err := mustView(t, root).Find("target", Options{
				Include: IncludeRefs,
				Return:  ReturnScope,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(response.Results) != 1 || response.Results[0].Scope != testCase.want {
				t.Fatalf("same-line reference = %#v, want scope %q", response.Results, testCase.want)
			}
		})
	}
}

func TestInspectSelectsMemberCallInsteadOfAssignmentTarget(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "found.go", "package demo\n\nfunc caller() {\n\t_ = limiter.ReserveN(now, 1)\n}\n")

	view := mustView(t, root)
	response, err := view.Inspect("found.go:4", Options{
		Include: IncludeBoth,
		Return:  ReturnScope,
	})
	if err != nil {
		t.Fatal(err)
	}
	if response.Symbol != "ReserveN" {
		t.Fatalf("symbol = %q", response.Symbol)
	}
}

func TestInspectGoScopeIncludesCodeAfterNestedBlock(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "found.go", `package demo

func use(reservation Reservation) {
	if !reservation.OK() {
		return
	}
	delay := reservation.Delay()
	_ = delay
}
`)

	view := mustView(t, root)
	response, err := view.Inspect("found.go:4", Options{
		Include:      IncludeScope,
		Return:       ReturnScope,
		MaxCodeLines: 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 {
		t.Fatalf("results = %#v", response.Results)
	}
	result := response.Results[0]
	if result.StartLine != 3 || result.EndLine != 9 {
		t.Fatalf("scope = %d-%d", result.StartLine, result.EndLine)
	}
	if !strings.Contains(result.Code, "reservation.Delay()") {
		t.Fatalf("scope omitted code after nested block:\n%s", result.Code)
	}
}

func TestInspectTruncatedScopeIncludesRequestedLine(t *testing.T) {
	root := t.TempDir()
	source := "package demo\n\nfunc caller() {\n" +
		strings.Repeat("\tprintln(\"padding\")\n", 70) +
		"\thelper()\n}\n"
	writeFile(t, root, "found.go", source)

	view := mustView(t, root)
	response, err := view.Inspect("found.go:74", Options{
		Include:      IncludeScope,
		Return:       ReturnScope,
		MaxCodeLines: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 {
		t.Fatalf("results = %#v", response.Results)
	}
	result := response.Results[0]
	if result.StartLine != 3 || result.EndLine != 75 || !result.CodeTruncated {
		t.Fatalf("scope = %#v", result)
	}
	if result.CodeStartLine > 74 || result.CodeEndLine < 74 {
		t.Fatalf("code range %d-%d omits requested line", result.CodeStartLine, result.CodeEndLine)
	}
	if !strings.Contains(result.Code, "helper()") {
		t.Fatalf("code omitted requested line:\n%s", result.Code)
	}
}

func TestInspectDefsDoesNotReturnReferences(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "found.go", "package demo\n\nfunc helper() {}\n\nfunc caller() {\n\thelper()\n}\n")

	view := mustView(t, root)
	response, err := view.Inspect("found.go:3", Options{
		Include: IncludeDefs,
		Return:  ReturnLine,
	})
	if err != nil {
		t.Fatal(err)
	}

	for _, result := range response.Results {
		if result.Kind == "ref" {
			t.Fatalf("results = %#v", response.Results)
		}
	}
}

func TestOutlineReturnsDefinitions(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "found.go", "package demo\n\ntype Service struct{}\n\nfunc (s Service) Run() {}\n")

	view := mustView(t, root)
	response, err := view.Outline("found.go", Options{Return: ReturnLine})
	if err != nil {
		t.Fatal(err)
	}

	if len(response.Results) != 2 {
		t.Fatalf("results = %#v", response.Results)
	}
	if response.Results[0].Symbol != "Service" || response.Results[1].Symbol != "Run" {
		t.Fatalf("results = %#v", response.Results)
	}
}

func TestOutlineReportsResultTruncation(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "found.go", "package demo\n\nfunc first() {}\nfunc second() {}\n")

	view := mustView(t, root)
	response, err := view.Outline("found.go", Options{Return: ReturnLine, Limit: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || !response.ResultsTruncated {
		t.Fatalf("response = %#v", response)
	}
}

func TestOutlineFindsGoGroupedTypeDefinition(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "found.go", "package demo\n\ntype (\n\tRateLimiter interface{}\n\tRateLimiterImpl struct{}\n)\n")

	view := mustView(t, root)
	response, err := view.Outline("found.go", Options{Return: ReturnLine})
	if err != nil {
		t.Fatal(err)
	}

	if len(response.Results) != 2 {
		t.Fatalf("results = %#v", response.Results)
	}
	if got := response.Results[1]; got.Symbol != "RateLimiterImpl" || got.Scope != "RateLimiterImpl" {
		t.Fatalf("grouped type result = %#v", got)
	}
}

func TestExplicitPathsCannotEscapeRepositoryRoot(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "repo")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, parent, "outside.go", "package secret\n\nfunc OutsideSecret() {}\n")
	writeFile(t, parent, "outside/outside.go", "package secret\n\nfunc DirectorySecret() {}\n")
	if err := os.Symlink(
		filepath.Join(parent, "outside.go"),
		filepath.Join(root, "linked.go"),
	); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := os.Symlink(
		filepath.Join(parent, "outside"),
		filepath.Join(root, "linked-dir"),
	); err != nil {
		t.Skipf("directory symlinks unavailable: %v", err)
	}

	view := mustView(t, root)
	for name, location := range map[string]string{
		"parent traversal":  "../outside.go:3",
		"file symlink":      "linked.go:3",
		"directory symlink": "linked-dir/outside.go:3",
	} {
		t.Run("inspect/"+name, func(t *testing.T) {
			if _, err := view.Inspect(location, Options{
				Include: IncludeScope,
				Return:  ReturnLine,
			}); err == nil {
				t.Fatalf("Inspect(%q) unexpectedly succeeded", location)
			}
		})
	}
	for name, path := range map[string]string{
		"parent traversal":  "../outside.go",
		"file symlink":      "linked.go",
		"directory symlink": "linked-dir/outside.go",
	} {
		t.Run("outline/"+name, func(t *testing.T) {
			if _, err := view.Outline(path, Options{
				Return: ReturnLine,
			}); err == nil {
				t.Fatalf("Outline(%q) unexpectedly succeeded", path)
			}
		})
	}
	response, err := view.Find("OutsideSecret", Options{
		Include: IncludeBoth,
		Return:  ReturnLine,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 0 {
		t.Fatalf("Find followed an out-of-root symlink: %#v", response.Results)
	}
}

func TestExplicitPathsRejectReplacedRepositoryRoot(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "repo")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "found.go", "package original\n")
	view := mustView(t, root)

	if err := os.Rename(root, filepath.Join(parent, "original")); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "found.go", "package replacement\n")

	if _, err := view.Inspect("found.go:1", Options{
		Include: IncludeScope,
		Return:  ReturnLine,
	}); err == nil || !strings.Contains(err.Error(), "repository root changed") {
		t.Fatalf("Inspect after root replacement error = %v", err)
	}
	if _, err := view.Changed(Options{MaxPatchLines: 100}); err == nil ||
		!strings.Contains(err.Error(), "repository root changed") {
		t.Fatalf("Changed after root replacement error = %v", err)
	}
	if _, err := view.Find("replacement", Options{
		Include: IncludeBoth,
		Return:  ReturnLine,
	}); err == nil || !strings.Contains(err.Error(), "repository root changed") {
		t.Fatalf("Find after root replacement error = %v", err)
	}
	if _, err := view.Find("replacement", Options{
		Include:     IncludeBoth,
		Return:      ReturnLine,
		ChangedOnly: true,
	}); err == nil || !strings.Contains(err.Error(), "repository root changed") {
		t.Fatalf("changed-only Find after root replacement error = %v", err)
	}
}

func TestUnixBackslashFilenameRemainsAddressable(t *testing.T) {
	if filepath.Separator == '\\' {
		t.Skip("backslash is a path separator on this platform")
	}
	root := t.TempDir()
	const name = `weird\name.go`
	writeFile(t, root, name, "package demo\n\nfunc BackslashTarget() {}\n")
	view := mustView(t, root)

	found, err := view.Find("BackslashTarget", Options{
		Include: IncludeDefs,
		Return:  ReturnLocations,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(found.Results) != 1 || found.Results[0].Path != name ||
		found.Results[0].Line != 3 {
		t.Fatalf("Find results = %#v", found.Results)
	}

	outlined, err := view.Outline(name, Options{Return: ReturnLocations})
	if err != nil {
		t.Fatal(err)
	}
	if len(outlined.Results) != 1 || outlined.Results[0].Path != name {
		t.Fatalf("Outline results = %#v", outlined.Results)
	}

	inspected, err := view.Inspect(name+":3", Options{
		Include: IncludeSymbol,
		Return:  ReturnLocations,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(inspected.Results) != 1 || inspected.Results[0].Path != name {
		t.Fatalf("Inspect results = %#v", inspected.Results)
	}
}

func TestUnixInvalidUTF8DirectoryRemainsAddressable(t *testing.T) {
	if filepath.Separator == '\\' {
		t.Skip("invalid UTF-8 filenames are not portable to this platform")
	}
	root := t.TempDir()
	directory := "invalid-" + string([]byte{0xff})
	if err := os.Mkdir(filepath.Join(root, directory), 0o700); err != nil {
		t.Skipf("invalid UTF-8 filenames unavailable: %v", err)
	}
	writeFile(
		t,
		root,
		path.Join(directory, "found.go"),
		"package demo\n\nfunc InvalidUTF8Target() {}\n",
	)

	found, err := mustView(t, root).Find("InvalidUTF8Target", Options{
		Include: IncludeDefs,
		Return:  ReturnLocations,
	})
	if err != nil {
		t.Fatal(err)
	}
	wantPath := path.Join(directory, "found.go")
	if len(found.Results) != 1 || found.Results[0].Path != wantPath ||
		found.Results[0].Line != 3 {
		t.Fatalf("Find results = %#v", found.Results)
	}
}

func TestFindCanIgnoreCommentAndStringOnlyMatches(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "found.go", "package demo\n\n// helper documents this function.\nfunc caller() {\n\t_ = \"helper\"\n\thelper()\n}\n")

	view := mustView(t, root)
	response, err := view.Find("helper", Options{
		Include:    IncludeRefs,
		Return:     ReturnLine,
		NoComments: true,
		NoStrings:  true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(response.Results) != 1 || response.Results[0].Line != 6 {
		t.Fatalf("results = %#v", response.Results)
	}
}

func TestFindSearchesGoModuleManifest(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "go.mod", "module example.test/demo\n\nrequire golang.org/x/time v0.14.0\n")

	view := mustView(t, root)
	response, err := view.Find("golang.org/x/time", Options{
		Include:   IncludeRefs,
		Return:    ReturnLine,
		PathGlobs: []string{"go.mod"},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(response.Results) != 1 {
		t.Fatalf("results = %#v", response.Results)
	}
	if got := response.Results[0]; got.Path != "go.mod" || got.Line != 3 || !strings.Contains(got.Code, "v0.14.0") {
		t.Fatalf("result = %#v", got)
	}
}

func TestChangedIncludesAndTruncatesExactPatch(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "scopesifter@example.test")
	runGit(t, root, "config", "user.name", "scopesifter test")
	writeFile(t, root, "found.go", "package demo\n\nfunc old() {}\n")
	runGit(t, root, "add", "found.go")
	runGit(t, root, "commit", "-m", "initial")
	writeFile(t, root, "found.go", "package demo\n\nfunc changed() {\n\tprintln(\"changed\")\n}\n")
	writeFile(t, root, "new.go", "package demo\n\nfunc added() {}\n")

	view := mustView(t, root)
	full, err := view.Changed(Options{MaxPatchLines: 100})
	if err != nil {
		t.Fatal(err)
	}
	if full.PatchTruncated || !strings.Contains(full.Patch, `println("changed")`) {
		t.Fatalf("full patch = %q, truncated = %v", full.Patch, full.PatchTruncated)
	}
	if !strings.Contains(full.Patch, "func added()") {
		t.Fatalf("untracked patch = %q", full.Patch)
	}
	if full.HeadCommit == "" || full.HeadSubject != "initial" {
		t.Fatalf("metadata = %#v", full)
	}
	encoded, err := json.Marshal(full)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(encoded), `"patch_truncated":false`) ||
		!strings.Contains(string(encoded), `"code_truncated":false`) ||
		!strings.Contains(string(encoded), `"results_truncated":false`) {
		t.Fatalf("truncation state is not explicit: %s", encoded)
	}

	locations, err := view.Changed(Options{Return: ReturnLocations, MaxPatchLines: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(locations.Results) >= len(parseChangedLineNumbers(full.Patch)) {
		t.Fatalf("location results were not aggregated: %#v", locations.Results)
	}
	for _, result := range locations.Results {
		if result.Code != "" {
			t.Fatalf("locations result embeds code: %#v", result)
		}
	}

	truncated, err := view.Changed(Options{MaxPatchLines: 3})
	if err != nil {
		t.Fatal(err)
	}
	if !truncated.PatchTruncated || len(strings.Split(truncated.Patch, "\n")) != 3 {
		t.Fatalf("truncated patch = %q, truncated = %v", truncated.Patch, truncated.PatchTruncated)
	}
}

func TestPatchOutputCollectorPreservesExactPrefixWithinLimits(t *testing.T) {
	tests := []struct {
		name       string
		input      string
		maxLines   int
		maxBytes   int
		want       string
		truncated  bool
		commitTail bool
	}{
		{
			name: "line limit", input: "one\ntwo\nthree\n",
			maxLines: 2, maxBytes: 100, want: "one\ntwo", truncated: true,
		},
		{
			name: "trailing newlines", input: "one\ntwo\n\n",
			maxLines: 2, maxBytes: 100, want: "one\ntwo",
		},
		{
			name: "empty prefix lines", input: "\n\nthree",
			maxLines: 2, maxBytes: 100, want: "\n", truncated: true,
			commitTail: true,
		},
		{
			name: "byte limit", input: "abcdef",
			maxLines: 10, maxBytes: 4, want: "abcd", truncated: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			collector := newPatchOutputCollector(test.maxLines, test.maxBytes)
			collector.consume([]byte(test.input))
			if test.commitTail {
				collector.commitPendingNewlines()
			}
			if got := string(collector.output); got != test.want ||
				collector.truncated != test.truncated {
				t.Fatalf(
					"output = %q, truncated = %v; want %q, %v",
					got,
					collector.truncated,
					test.want,
					test.truncated,
				)
			}
		})
	}
}

func TestPatchOutputCollectorMatchesLineTruncationSemantics(t *testing.T) {
	const alphabet = "a\n"
	for length := range 11 {
		combinations := 1 << length
		for bits := range combinations {
			input := make([]byte, length)
			for index := range input {
				input[index] = alphabet[(bits>>index)&1]
			}
			for maxLines := 1; maxLines <= 4; maxLines++ {
				normalized := strings.TrimRight(string(input), "\n")
				want := normalized
				wantTruncated := false
				parts := strings.Split(normalized, "\n")
				if len(parts) > maxLines {
					want = strings.Join(parts[:maxLines], "\n")
					wantTruncated = true
				}

				collector := newPatchOutputCollector(maxLines, 1<<20)
				collector.consume(input)
				if collector.truncated {
					collector.commitPendingNewlines()
				}
				if got := string(collector.output); got != want ||
					collector.truncated != wantTruncated {
					t.Fatalf(
						"input %q, max lines %d: got %q, %v; want %q, %v",
						input,
						maxLines,
						got,
						collector.truncated,
						want,
						wantTruncated,
					)
				}
			}
		}
	}
}

func TestPatchOutputCollectorMatchesByteAndLinePrefix(t *testing.T) {
	const alphabet = "a\n"
	for length := range 10 {
		combinations := 1 << length
		for bits := range combinations {
			input := make([]byte, length)
			for index := range input {
				input[index] = alphabet[(bits>>index)&1]
			}
			for maxLines := 1; maxLines <= 3; maxLines++ {
				for maxBytes := 1; maxBytes <= 6; maxBytes++ {
					normalized := strings.TrimRight(string(input), "\n")
					if len(input)-len(normalized) > maxBytes+1 {
						// The raw-input safety probe intentionally terminates an
						// otherwise unbounded terminal-newline stream.
						continue
					}
					want := normalized
					wantTruncated := false
					parts := strings.Split(normalized, "\n")
					if len(parts) > maxLines {
						want = strings.Join(parts[:maxLines], "\n")
						wantTruncated = true
					}
					if len(want) > maxBytes {
						want = want[:maxBytes]
						wantTruncated = true
					}

					collector := newPatchOutputCollector(maxLines, maxBytes)
					collector.consume(input)
					if collector.truncated {
						collector.commitPendingNewlines()
					}
					if got := string(collector.output); got != want ||
						collector.truncated != wantTruncated {
						t.Fatalf(
							"input %q, limits %d/%d: got %q, %v; want %q, %v",
							input,
							maxLines,
							maxBytes,
							got,
							collector.truncated,
							want,
							wantTruncated,
						)
					}
				}
			}
		}
	}
}

func TestPatchOutputCollectorEmergencyProbeHonorsLineLimit(t *testing.T) {
	collector := newPatchOutputCollector(2, 4)
	collector.pendingNewlines = collector.maxBytes + 1
	collector.consume([]byte{'\n'})
	if got := string(collector.output); got != "\n" || !collector.truncated {
		t.Fatalf("output = %q, truncated = %v; want one newline, true", got, collector.truncated)
	}
}

func TestBoundedPatchCommandOutputStopsAtLineLimit(t *testing.T) {
	root := t.TempDir()
	var source strings.Builder
	for line := range 10_000 {
		fmt.Fprintf(&source, "line %d\n", line)
	}
	writeFile(t, root, "large.txt", source.String())

	command := exec.Command(
		"git", "diff", "--no-index", "--no-color", "--", os.DevNull, "large.txt",
	)
	command.Dir = root
	output, truncated, err := boundedPatchCommandOutput(command, 3, 1<<20, true)
	if err != nil {
		t.Fatal(err)
	}
	if !truncated || len(strings.Split(string(output), "\n")) != 3 {
		t.Fatalf("output lines = %d, truncated = %v: %q", len(strings.Split(string(output), "\n")), truncated, output)
	}
}

func TestChangedBoundsUntrackedSnapshotBytes(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "scopesifter@example.test")
	runGit(t, root, "config", "user.name", "scopesifter test")
	writeFile(t, root, "seed.go", "package demo\n")
	runGit(t, root, "add", "seed.go")
	runGit(t, root, "commit", "-m", "initial")

	large, err := os.Create(filepath.Join(root, "large.go"))
	if err != nil {
		t.Fatal(err)
	}
	if err := large.Truncate(maximumPatchBytes + 1); err != nil {
		large.Close()
		t.Fatal(err)
	}
	if err := large.Close(); err != nil {
		t.Fatal(err)
	}

	patch, truncated, err := mustView(t, root).changedPatch("", "", []string{"large.go"}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if patch != "" || !truncated {
		t.Fatalf("patch length = %d, truncated = %v", len(patch), truncated)
	}
}

func TestChangedCountsApplicableAttributesAgainstSnapshotBudget(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "scopesifter@example.test")
	runGit(t, root, "config", "user.name", "scopesifter test")
	writeFile(t, root, "seed.go", "package demo\n")
	runGit(t, root, "add", "seed.go")
	runGit(t, root, "commit", "-m", "initial")
	writeFile(t, root, "new.go", "package demo\n")

	attributes, err := os.Create(filepath.Join(root, ".gitattributes"))
	if err != nil {
		t.Fatal(err)
	}
	if err := attributes.Truncate(maximumPatchBytes); err != nil {
		attributes.Close()
		t.Fatal(err)
	}
	if err := attributes.Close(); err != nil {
		t.Fatal(err)
	}

	patch, truncated, err := mustView(t, root).changedPatch("", "", []string{"new.go"}, 100)
	if err != nil {
		t.Fatal(err)
	}
	if patch != "" || !truncated {
		t.Fatalf("patch length = %d, truncated = %v", len(patch), truncated)
	}
}

func TestChangedDeduplicatesBeforeApplyingResultLimit(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "scopesifter@example.test")
	runGit(t, root, "config", "user.name", "scopesifter test")
	writeFile(t, root, "found.go", "package demo\n\nfunc first() {\n\tprintln(1)\n\tprintln(2)\n\tprintln(3)\n}\n")
	runGit(t, root, "add", "found.go")
	runGit(t, root, "commit", "-m", "initial")
	writeFile(t, root, "found.go", "package demo\n\nfunc first() {\n\tprintln(4)\n\tprintln(5)\n\tprintln(6)\n}\n")

	view := mustView(t, root)
	lines, err := view.Changed(Options{
		Return: ReturnLine, Limit: 1, MaxPatchLines: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(lines.Results) != 1 || !lines.ResultsTruncated {
		t.Fatalf("line response = %#v", lines)
	}

	scopes, err := view.Changed(Options{
		Return: ReturnScope, Limit: 1, MaxPatchLines: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(scopes.Results) != 1 || scopes.ResultsTruncated ||
		scopes.Results[0].Scope != "first" {
		t.Fatalf("scope response = %#v", scopes)
	}
}

func TestChangedReportsEveryUntrackedSourceLine(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "scopesifter@example.test")
	runGit(t, root, "config", "user.name", "scopesifter test")
	writeFile(t, root, "seed.go", "package demo\n")
	runGit(t, root, "add", "seed.go")
	runGit(t, root, "commit", "-m", "initial")
	writeFile(t, root, "new.go", "package demo\n\nfunc added() {\n\tprintln(\"new\")\n}\n")

	view := mustView(t, root)
	if changed, err := view.changedLines(
		"new.go", "", mustGitText(t, view, "rev-parse", "HEAD"), 5,
	); err != nil {
		t.Fatal(err)
	} else if !slices.Equal(changed, []int{1, 2, 3, 4, 5}) {
		t.Fatalf("untracked changed lines = %#v", changed)
	}
	response, err := view.Changed(Options{
		Return: ReturnLocations, PathGlobs: []string{"new.go"}, MaxPatchLines: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 {
		t.Fatalf("results = %#v", response.Results)
	}
	result := response.Results[0]
	if result.StartLine != 1 || result.EndLine != 5 ||
		len(result.ChangedLines) != 5 || result.ChangedLines[4] != 5 {
		t.Fatalf("untracked changed range = %#v", result)
	}
}

func TestChangedLineDetectionStreamsOversizedDeletedLines(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "scopesifter@example.test")
	runGit(t, root, "config", "user.name", "scopesifter test")
	writeFile(t, root, "found.go", "package demo\n"+strings.Repeat("x", 256<<10)+"\n")
	runGit(t, root, "add", "found.go")
	runGit(t, root, "commit", "-m", "initial")
	writeFile(t, root, "found.go", "package demo\n")

	view := mustView(t, root)
	changed, err := view.changedLines(
		"found.go", "", mustGitText(t, view, "rev-parse", "HEAD"), 1,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(changed, []int{1}) {
		t.Fatalf("changed lines = %#v, want [1]", changed)
	}
}

func TestChangedUntrackedSnapshotPreservesPatchPathAndMode(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "scopesifter@example.test")
	runGit(t, root, "config", "user.name", "scopesifter test")
	writeFile(t, root, "seed.go", "package demo\n")
	runGit(t, root, "add", "seed.go")
	runGit(t, root, "commit", "-m", "initial")
	writeFile(t, root, "scripts/new.sh", "#!/bin/sh\necho safe\n")
	if err := os.Chmod(filepath.Join(root, "scripts/new.sh"), 0o755); err != nil {
		t.Fatal(err)
	}

	response, err := mustView(t, root).Changed(Options{
		PathGlobs: []string{"scripts/new.sh"}, MaxPatchLines: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(
		response.Patch,
		"diff --git a/scripts/new.sh b/scripts/new.sh",
	) || !strings.Contains(response.Patch, "new file mode 100755") {
		t.Fatalf("untracked snapshot patch = %q", response.Patch)
	}
}

func TestChangedUntrackedSnapshotPreservesGitAttributes(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "scopesifter@example.test")
	runGit(t, root, "config", "user.name", "scopesifter test")
	writeFile(t, root, ".gitattributes", "assets/*.dat binary\n")
	runGit(t, root, "add", ".gitattributes")
	runGit(t, root, "commit", "-m", "attributes")
	if err := os.WriteFile(
		filepath.Join(root, ".git", "info", "attributes"),
		[]byte("*.info binary\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	writeFile(t, root, "assets/new.dat", "attribute controlled text\n")
	writeFile(t, root, "new.info", "metadata controlled text\n")

	response, err := mustView(t, root).Changed(Options{MaxPatchLines: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"assets/new.dat", "new.info"} {
		if !strings.Contains(
			response.Patch,
			"Binary files /dev/null and b/"+path+" differ",
		) {
			t.Fatalf("untracked attribute patch for %s = %q", path, response.Patch)
		}
	}
	if strings.Contains(response.Patch, "+attribute controlled text") ||
		strings.Contains(response.Patch, "+metadata controlled text") {
		t.Fatalf("untracked attributes were ignored: %q", response.Patch)
	}
}

func TestChangedUntrackedSnapshotUsesLinkedWorktreeAttributes(t *testing.T) {
	container := t.TempDir()
	mainRoot := filepath.Join(container, "main")
	if err := os.Mkdir(mainRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, mainRoot, "init")
	runGit(t, mainRoot, "config", "user.email", "scopesifter@example.test")
	runGit(t, mainRoot, "config", "user.name", "scopesifter test")
	writeFile(t, mainRoot, "seed.go", "package demo\n")
	runGit(t, mainRoot, "add", "seed.go")
	runGit(t, mainRoot, "commit", "-m", "initial")
	linkedRoot := filepath.Join(container, "linked")
	runGit(t, mainRoot, "worktree", "add", "-b", "linked-test", linkedRoot)
	if err := os.WriteFile(
		filepath.Join(mainRoot, ".git", "info", "attributes"),
		[]byte("*.info binary\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	writeFile(t, linkedRoot, "linked.info", "linked worktree text\n")

	response, err := mustView(t, linkedRoot).Changed(Options{MaxPatchLines: 100})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(
		response.Patch,
		"Binary files /dev/null and b/linked.info differ",
	) {
		t.Fatalf("linked-worktree attribute patch = %q", response.Patch)
	}
}

func TestChangedUsesWorktreeCoordinatesForStagedAndUnstagedChanges(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "scopesifter@example.test")
	runGit(t, root, "config", "user.name", "scopesifter test")
	writeFile(t, root, "mixed.go", "package demo\n\nfunc run() {\n\tprintln(\"before\")\n}\n")
	runGit(t, root, "add", "mixed.go")
	runGit(t, root, "commit", "-m", "initial")
	writeFile(t, root, "mixed.go", "package demo\n\nfunc run() {\n\tprintln(\"staged\")\n}\n")
	runGit(t, root, "add", "mixed.go")
	writeFile(t, root, "mixed.go", "package demo\n// unstaged insertion\n\nfunc run() {\n\tprintln(\"staged\")\n}\n")

	view := mustView(t, root)
	response, err := view.Changed(Options{
		Return: ReturnLocations, PathGlobs: []string{"mixed.go"}, MaxPatchLines: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	var changed []int
	for _, result := range response.Results {
		changed = append(changed, result.ChangedLines...)
	}
	if !slices.Equal(changed, []int{2, 5}) {
		t.Fatalf("worktree changed lines = %#v, want [2 5]", changed)
	}
}

func TestChangedReportsStagedEditCanceledInWorktree(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "scopesifter@example.test")
	runGit(t, root, "config", "user.name", "scopesifter test")
	original := "package demo\n\nfunc run() {\n\tprintln(\"before\")\n}\n"
	writeFile(t, root, "mixed.go", original)
	runGit(t, root, "add", "mixed.go")
	runGit(t, root, "commit", "-m", "initial")
	writeFile(t, root, "mixed.go", "package demo\n\nfunc run() {\n\tprintln(\"staged\")\n}\n")
	runGit(t, root, "add", "mixed.go")
	writeFile(t, root, "mixed.go", original)

	response, err := mustView(t, root).Changed(Options{
		Return: ReturnLocations, MaxPatchLines: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].Kind != "file" ||
		response.Results[0].Path != "mixed.go" {
		t.Fatalf("canceled staged edit results = %#v, want one file result", response.Results)
	}
	forward := strings.Index(response.Patch, "-\tprintln(\"before\")\n+\tprintln(\"staged\")")
	reverse := strings.Index(response.Patch, "-\tprintln(\"staged\")\n+\tprintln(\"before\")")
	if forward < 0 || reverse <= forward {
		t.Fatalf("staged/worktree patch order = %q", response.Patch)
	}
}

func TestChangedOrdersUnbornIndexAndWorktreePatches(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "scopesifter@example.test")
	runGit(t, root, "config", "user.name", "scopesifter test")
	writeFile(t, root, "new.go", "package demo\n\nfunc staged() {}\n")
	runGit(t, root, "add", "new.go")
	writeFile(t, root, "new.go", "package demo\n\nfunc worktree() {}\n")

	response, err := mustView(t, root).Changed(Options{
		Return: ReturnLocations, MaxPatchLines: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	staged := strings.Index(response.Patch, "+func staged() {}")
	worktree := strings.Index(response.Patch, "+func worktree() {}")
	if staged < 0 || worktree <= staged {
		t.Fatalf("unborn index/worktree patch order = %q", response.Patch)
	}
	if len(response.Results) != 1 || response.Results[0].Path != "new.go" ||
		!slices.Equal(response.Results[0].ChangedLines, []int{1, 2, 3}) {
		t.Fatalf("unborn changed results = %#v", response.Results)
	}
}

func TestChangedTreatsSelectedFilenamesAsLiteralGitPathspecs(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "scopesifter@example.test")
	runGit(t, root, "config", "user.name", "scopesifter test")
	magic := ":(glob)*.go"
	writeFile(t, root, magic, "package magic\n\nfunc before() {}\n")
	writeFile(t, root, "other.go", "package other\n\nfunc before() {}\n")
	runGit(t, root, "add", magic, "other.go")
	runGit(t, root, "commit", "-m", "initial")
	writeFile(t, root, magic, "package magic\n\nfunc selected() {}\n")
	writeFile(t, root, "other.go", "package other\n\nfunc leaked() {}\n")

	view := mustView(t, root)
	response, err := view.Changed(Options{
		Return: ReturnLocations, PathGlobs: []string{magic}, MaxPatchLines: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(response.Patch, "leaked") || !strings.Contains(response.Patch, "selected") {
		t.Fatalf("literal-path patch = %q", response.Patch)
	}
	for _, result := range response.Results {
		if result.Path != magic || result.StartLine < 1 || result.EndLine < result.StartLine ||
			result.EndLine > 3 {
			t.Fatalf("literal-path result = %#v", result)
		}
	}
}

func TestChangedRejectsOptionLikeBase(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "scopesifter@example.test")
	runGit(t, root, "config", "user.name", "scopesifter test")
	writeFile(t, root, "found.go", "package demo\n")
	runGit(t, root, "add", "found.go")
	runGit(t, root, "commit", "-m", "initial")

	output := filepath.Join(t.TempDir(), "git-output")
	view := mustView(t, root)
	if _, err := view.Changed(Options{
		Base:          "--output=" + output,
		MaxPatchLines: 100,
	}); err == nil {
		t.Fatal("option-like base unexpectedly succeeded")
	}
	if _, err := os.Lstat(output + "...HEAD"); !os.IsNotExist(err) {
		t.Fatalf("Git option injection wrote an output file: %v", err)
	}
}

func TestChangedHandlesNewlineInTrackedPath(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "scopesifter@example.test")
	runGit(t, root, "config", "user.name", "scopesifter test")
	name := "line\nbreak.go"
	writeFile(t, root, name, "package demo\n\nfunc before() {}\n")
	runGit(t, root, "add", name)
	runGit(t, root, "commit", "-m", "initial")
	writeFile(t, root, name, "package demo\n\nfunc after() {}\n")

	view := mustView(t, root)
	response, err := view.Changed(Options{MaxPatchLines: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 || response.Results[0].Path != name {
		t.Fatalf("newline path was not preserved: %#v", response.Results)
	}
	if !strings.Contains(response.Patch, "func after()") {
		t.Fatalf("patch omitted newline-named file: %q", response.Patch)
	}
}

func TestChangedDisablesConfiguredGitColor(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "scopesifter@example.test")
	runGit(t, root, "config", "user.name", "scopesifter test")
	runGit(t, root, "config", "color.ui", "always")
	writeFile(t, root, "found.go", "package demo\n\nfunc before() {}\n")
	runGit(t, root, "add", "found.go")
	runGit(t, root, "commit", "-m", "initial")
	writeFile(t, root, "found.go", "package demo\n\nfunc after() {}\n")

	view := mustView(t, root)
	response, err := view.Changed(Options{MaxPatchLines: 100})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(response.Patch, "\x1b[") {
		t.Fatalf("patch contains terminal color escapes: %q", response.Patch)
	}
}

func TestChangedIgnoresAmbientGitRepositoryOverrides(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "scopesifter@example.test")
	runGit(t, root, "config", "user.name", "scopesifter test")
	writeFile(t, root, "found.go", "package demo\n\nfunc before() {}\n")
	runGit(t, root, "add", "found.go")
	runGit(t, root, "commit", "-m", "initial")
	writeFile(t, root, "found.go", "package demo\n\nfunc after() {}\n")

	other := t.TempDir()
	runGit(t, other, "init")
	runGit(t, other, "config", "user.email", "scopesifter@example.test")
	runGit(t, other, "config", "user.name", "scopesifter test")
	writeFile(t, other, "other.go", "package other\n")
	runGit(t, other, "add", "other.go")
	runGit(t, other, "commit", "-m", "other")
	t.Setenv("GIT_DIR", filepath.Join(other, ".git"))
	t.Setenv("GIT_WORK_TREE", other)
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "color.ui")
	t.Setenv("GIT_CONFIG_VALUE_0", "always")

	view := mustView(t, root)
	response, err := view.Changed(Options{MaxPatchLines: 100})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(response.Patch, "func after()") {
		t.Fatalf("ambient Git overrides redirected Changed: %#v", response)
	}
	if strings.Contains(response.Patch, "\x1b[") {
		t.Fatalf("ambient Git config added color escapes: %q", response.Patch)
	}
}

func TestIsolatedGitEnvironmentFiltersOverridesCaseInsensitively(t *testing.T) {
	t.Setenv("git_dir", "attacker-git-dir")
	t.Setenv("Git_Work_Tree", "attacker-work-tree")

	for _, entry := range isolatedGitEnvironment() {
		name, value, _ := strings.Cut(entry, "=")
		if (strings.EqualFold(name, "GIT_DIR") && value == "attacker-git-dir") ||
			(strings.EqualFold(name, "GIT_WORK_TREE") && value == "attacker-work-tree") {
			t.Fatalf("ambient Git override survived isolation: %q", entry)
		}
	}
}

func TestChangedDoesNotExecuteConfiguredFilesystemMonitor(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "scopesifter@example.test")
	runGit(t, root, "config", "user.name", "scopesifter test")
	writeFile(t, root, "found.go", "package demo\n\nfunc before() {}\n")
	runGit(t, root, "add", "found.go")
	runGit(t, root, "commit", "-m", "initial")

	monitor := filepath.Join(root, "fsmonitor-test")
	writeFile(t, root, "fsmonitor-test", "#!/bin/sh\n: > \"$0.marker\"\n")
	if err := os.Chmod(monitor, 0o700); err != nil {
		t.Fatal(err)
	}
	runGit(t, root, "config", "core.fsmonitor", monitor)
	writeFile(t, root, "found.go", "package demo\n\nfunc after() {}\n")

	view := mustView(t, root)
	response, err := view.Changed(Options{MaxPatchLines: 100})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(response.Patch, "func after()") {
		t.Fatalf("configured fsmonitor disrupted Changed: %#v", response)
	}
	if _, err := os.Lstat(monitor + ".marker"); !os.IsNotExist(err) {
		t.Fatalf("configured fsmonitor executed: %v", err)
	}
}

func TestChangedDoesNotReadUntrackedNamedPipe(t *testing.T) {
	mkfifo, err := exec.LookPath("mkfifo")
	if err != nil {
		t.Skip("mkfifo is unavailable")
	}
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "scopesifter@example.test")
	runGit(t, root, "config", "user.name", "scopesifter test")
	writeFile(t, root, "found.go", "package demo\n")
	runGit(t, root, "add", "found.go")
	runGit(t, root, "commit", "-m", "initial")
	pipePath := filepath.Join(root, "blocked.go")
	if output, err := exec.Command(mkfifo, pipePath).CombinedOutput(); err != nil {
		t.Skipf("cannot create named pipe: %v\n%s", err, output)
	}

	view := mustView(t, root)
	response, err := view.Changed(Options{MaxPatchLines: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 0 {
		t.Fatalf("named pipe was reported as source: %#v", response.Results)
	}
	if response.Patch != "" {
		t.Fatalf("named pipe unexpectedly contributed patch content: %q", response.Patch)
	}
}

func TestGlobMatchSupportsDocumentedPathForms(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		path    string
	}{
		{"substring", "service/matching", "service/matching/forwarder.go"},
		{"basename glob", "*_test.go", "common/quotas/rate_limiter_impl_test.go"},
		{"recursive prefix", "service/matching/**", "service/matching/forwarder.go"},
		{"recursive basename", "**/*_test.go", "common/quotas/rate_limiter_impl_test.go"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if !globMatch(test.pattern, test.path) {
				t.Fatalf("globMatch(%q, %q) = false", test.pattern, test.path)
			}
		})
	}
	if globMatch("service/matching/**", "common/quotas/rate_limiter.go") {
		t.Fatal("recursive prefix matched an unrelated path")
	}
}

func TestChangedRangeReportsChangedScopesOnly(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "scopesifter@example.test")
	runGit(t, root, "config", "user.name", "scopesifter test")
	writeFile(t, root, "found.go", "package demo\n\nfunc previous() {}\n\nfunc first() {}\n\nfunc second() {}\n")
	runGit(t, root, "add", "found.go")
	runGit(t, root, "commit", "-m", "initial")
	writeFile(t, root, "found.go", "package demo\n\nfunc previous() {}\n\n// first changed.\nfunc first() { println(\"first\") }\n\nfunc second() { println(\"second\") }\n")

	view := mustView(t, root)
	response, err := view.Changed(Options{Return: ReturnContext, Context: 2, MaxPatchLines: 100})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 {
		t.Fatalf("results = %#v", response.Results)
	}
	got := response.Results[0]
	if got.Scope != "" || strings.Join(got.Scopes, ",") != "first,second" {
		t.Fatalf("changed scopes = %#v", got)
	}
	if len(got.ChangedLines) == 0 || got.ChangedLines[0] != 5 {
		t.Fatalf("changed lines = %#v", got.ChangedLines)
	}
}

func TestScopeNameDoesNotBorrowPreviousDeclaration(t *testing.T) {
	lines := strings.Split("package demo\n\nfunc previous() {}\n\n// next documents next.\nfunc next() {\n\tprintln(\"next\")\n}\n", "\n")
	goBackend := languageForExtension(".go")
	if got := scopeName(lines, 5, goBackend); got != "next" {
		t.Fatalf("comment scope = %q, want next", got)
	}
	if got := scopeName(lines, 7, goBackend); got != "next" {
		t.Fatalf("body scope = %q, want next", got)
	}
}

func TestParseChangedLineNumbersExpandsAddedRange(t *testing.T) {
	patch := "@@ -2,2 +4,3 @@ heading\n@@ -10 +12,0 @@ deleted\n"
	got := parseChangedLineNumbers(patch)
	want := []int{4, 5, 6, 12}
	if len(got) != len(want) {
		t.Fatalf("lines = %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("lines[%d] = %d, want %d", i, got[i], want[i])
		}
	}
}

func TestMergeContextRangesCombinesOverlappingHunks(t *testing.T) {
	got := mergeContextRanges(100, []int{10, 13, 40}, 5)
	want := [][2]int{{5, 18}, {35, 45}}
	if len(got) != len(want) {
		t.Fatalf("ranges = %#v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("ranges[%d] = %#v, want %#v", i, got[i], want[i])
		}
	}
}

func TestInspectAllIncludesImports(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "found.go", "package demo\n\nimport (\n\t\"fmt\"\n)\n\nfunc run() {\n\tfmt.Println(\"ok\")\n}\n")

	view := mustView(t, root)
	response, err := view.Inspect("found.go:8", Options{Include: IncludeAll, Return: ReturnContext, Context: 1})
	if err != nil {
		t.Fatal(err)
	}

	foundImports := false
	for _, result := range response.Results {
		if result.Kind == "imports" && strings.Contains(result.Code, "\"fmt\"") {
			foundImports = true
		}
	}
	if !foundImports {
		t.Fatalf("results = %#v", response.Results)
	}
}

func TestFindRejectsUnknownReturn(t *testing.T) {
	view := mustView(t, t.TempDir())
	_, err := view.Find("helper", Options{Return: "everything"})
	if err == nil {
		t.Fatal("expected invalid return error")
	}
}

func runGit(t *testing.T, root string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = root
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}
