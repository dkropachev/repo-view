package repoview

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRequestContextCancelsNavigation(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "found.go", "package demo\nfunc Helper() {}\n")
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := mustView(t, root).WithContext(ctx).Find(
		"Helper",
		Options{Return: ReturnLocations},
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Find error = %v, want context cancellation", err)
	}
}

func TestSourceFileByteLimitAppliesToWalkAndDirectRead(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "oversized.go")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maximumSourceFileBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}

	view := mustView(t, root)
	if _, err := view.Find("Helper", Options{Return: ReturnLocations}); err == nil ||
		!strings.Contains(err.Error(), "source file exceeds") {
		t.Fatalf("Find error = %v, want source byte limit", err)
	}
	if _, err := view.Outline("oversized.go", Options{}); err == nil ||
		!strings.Contains(err.Error(), "source file exceeds") {
		t.Fatalf("Outline error = %v, want source byte limit", err)
	}
}

func TestFindReturnsLocations(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "found.rs", "fn helper() {}\n\nfn caller() {\n    helper();\n}\n")

	view := mustView(t, root)
	response, err := view.Find("helper", Options{Return: ReturnLocations})
	if err != nil {
		t.Fatal(err)
	}

	assertLocations(t, response.Results, []string{"found.rs:1", "found.rs:4"})
}

func TestFindAcceptsMinifiedJavaScriptLineOverOneMiB(t *testing.T) {
	root := t.TempDir()
	body := `const payload = "` + strings.Repeat("x", 1300<<10) +
		`"; const longLineNeedle = 1;` + "\n"
	writeFile(t, root, "minified.js", body)

	view := mustView(t, root)
	response, err := view.Find("longLineNeedle", Options{Return: ReturnLocations})
	if err != nil {
		t.Fatal(err)
	}

	assertLocations(t, response.Results, []string{"minified.js:1"})
}

func TestFindReturnsEnclosingRustFunction(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "found.rs", "fn helper() {}\n\nfn caller() {\n    helper();\n}\n")

	view := mustView(t, root)
	response, err := view.Find("helper", Options{Return: ReturnScope})
	if err != nil {
		t.Fatal(err)
	}

	if got := resultLocation(response.Results[1]); got != "found.rs:3-5" {
		t.Fatalf("location = %q", got)
	}
	if got := response.Results[1].Code; got != "fn caller() {\n    helper();\n}" {
		t.Fatalf("code = %q", got)
	}
}

func TestFindReturnsEnclosingGoFunction(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "found.go", "package demo\n\nfunc helper() {}\n\nfunc caller() {\n\thelper()\n}\n")

	view := mustView(t, root)
	response, err := view.Find("helper", Options{Return: ReturnScope})
	if err != nil {
		t.Fatal(err)
	}

	assertLocations(t, response.Results, []string{"found.go:3-3", "found.go:5-7"})
}

func TestFindCanDropCommentsAndDocstrings(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "found.py", "def caller():\n    \"\"\"Call helper.\"\"\"\n    # noisy\n    return helper()  # trailing\n")

	view := mustView(t, root)
	response, err := view.Find("helper", Options{
		Return:         ReturnScope,
		DropComments:   true,
		DropDocstrings: true,
	})
	if err != nil {
		t.Fatal(err)
	}

	if got := resultLocation(response.Results[0]); got != "found.py:1-4" {
		t.Fatalf("location = %q", got)
	}
	if got := response.Results[0].Code; got != "def caller():\n    return helper()" {
		t.Fatalf("code = %q", got)
	}
}

func TestIgnoresPartialIdentifierMatches(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "found.go", "package demo\n\nfunc main() {\n\thelperish()\n\thelper()\n}\n")

	view := mustView(t, root)
	response, err := view.Find("helper", Options{Return: ReturnLocations})
	if err != nil {
		t.Fatal(err)
	}

	assertLocations(t, response.Results, []string{"found.go:5"})
}

func TestFindExcludesRepositoryCache(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "found.go", "package demo\n\nfunc helper() {}\n")
	writeFile(t, root, ".cache/hidden.go", "package hidden\n\nfunc helper() {}\n")

	view := mustView(t, root)
	response, err := view.Find("helper", Options{Return: ReturnLocations})
	if err != nil {
		t.Fatal(err)
	}

	assertLocations(t, response.Results, []string{"found.go:3"})
}

func TestCanSkipLikelyDefinitions(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "found.rs", "fn helper() {}\nfn caller() {\n    helper();\n}\n")

	view := mustView(t, root)
	response, err := view.Find("helper", Options{
		Include: IncludeRefs,
		Return:  ReturnLocations,
	})
	if err != nil {
		t.Fatal(err)
	}

	assertLocations(t, response.Results, []string{"found.rs:3"})
}

func TestFindDedupesMultipleHitsInSameScope(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "found.rs", "fn caller() {\n    helper();\n    helper();\n}\n")

	view := mustView(t, root)
	response, err := view.Find("helper", Options{Return: ReturnScope})
	if err != nil {
		t.Fatal(err)
	}

	assertLocations(t, response.Results, []string{"found.rs:1-4"})
}

func TestScopeForCommentBetweenBraceBlocksContainsHitLine(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "found.c", "void previous(void)\n{\n}\n\n/* exact hash comment */\nvoid next(void)\n{\n}\n")

	view := mustView(t, root)
	response, err := view.Find("hash", Options{Return: ReturnScope})
	if err != nil {
		t.Fatal(err)
	}

	assertLocations(t, response.Results, []string{"found.c:5-5"})
}

func TestInspectGenericBraceFallbackPreservesQuotedCommentText(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "fixture.xyz", `function run() {
  const block = "/* not a comment */";
  const line = "https://example.test/path";
	const joined = left/* separator */right;
  // remove this comment
	return block + line + joined;
}
`)

	view := mustView(t, root)
	response, err := view.Inspect("fixture.xyz:2", Options{
		Include:      IncludeScope,
		Return:       ReturnScope,
		DropComments: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 {
		t.Fatalf("results = %#v", response.Results)
	}
	code := response.Results[0].Code
	if !strings.Contains(code, `"/* not a comment */"`) ||
		!strings.Contains(code, `"https://example.test/path"`) ||
		!strings.Contains(code, "left right") ||
		strings.Contains(code, "remove this comment") {
		t.Fatalf("cleaned generic scope = %q", code)
	}
}

func TestGenericCommentCleanerPreservesBacktickText(t *testing.T) {
	source := "const template = `/* template text */`; // remove this"
	if got := dropCLikeComments(source); got != "const template = `/* template text */`; " {
		t.Fatalf("cleaned backtick source = %q", got)
	}
}

func TestGenericCommentCleanerResynchronizesUnterminatedLineQuotes(t *testing.T) {
	source := "const broken = \"unterminated\n// remove this\nconst visible = true;"
	want := "const broken = \"unterminated\n\nconst visible = true;"
	if got := dropCLikeComments(source); got != want {
		t.Fatalf("cleaned unterminated quoted source = %q, want %q", got, want)
	}
}

func TestInspectGenericBraceFallbackIgnoresCommentBraces(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "fixture.xyz", `function run() {
  /* } is not syntax */
  target();
  // { is not syntax either
}
`)

	view := mustView(t, root)
	response, err := view.Inspect(
		"fixture.xyz:3",
		Options{Include: IncludeScope, Return: ReturnScope},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Results) != 1 {
		t.Fatalf("results = %#v", response.Results)
	}
	result := response.Results[0]
	if result.StartLine != 1 || result.EndLine != 5 ||
		!strings.Contains(result.Code, "function run()") ||
		!strings.Contains(result.Code, "target()") {
		t.Fatalf("generic comment-brace scope = %#v", result)
	}
}

func TestInspectGenericBraceFallbackSkipsClosedPrecedingBlock(t *testing.T) {
	root := t.TempDir()
	writeFile(t, root, "fixture.xyz", `function run() {
  if (enabled) {
    helper();
  }
  target();
}
`)

	view := mustView(t, root)
	for _, lineNo := range []int{5, 6} {
		response, err := view.Inspect(
			fmt.Sprintf("fixture.xyz:%d", lineNo),
			Options{Include: IncludeScope, Return: ReturnScope},
		)
		if err != nil {
			t.Fatal(err)
		}
		if len(response.Results) != 1 {
			t.Fatalf("line %d results = %#v", lineNo, response.Results)
		}
		result := response.Results[0]
		if result.StartLine != 1 || result.EndLine != 6 ||
			!strings.Contains(result.Code, "function run()") ||
			!strings.Contains(result.Code, "target()") {
			t.Fatalf("line %d generic scope = %#v", lineNo, result)
		}
	}
}

func TestInspectGenericBraceFallbackIgnoresBacktickLiteralBraces(t *testing.T) {
	tests := []struct {
		name       string
		source     string
		targetLine int
		wantEnd    int
	}{
		{
			name: "single line",
			source: "function run() {\n" +
				"  const text = `}`;\n" +
				"  target();\n" +
				"}\n",
			targetLine: 3,
			wantEnd:    4,
		},
		{
			name: "multiline",
			source: "function run() {\n" +
				"  const text = `\n" +
				"    {\n" +
				"  `;\n" +
				"  target();\n" +
				"}\n",
			targetLine: 5,
			wantEnd:    6,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			writeFile(t, root, "fixture.xyz", test.source)

			view := mustView(t, root)
			response, err := view.Inspect(
				fmt.Sprintf("fixture.xyz:%d", test.targetLine),
				Options{Include: IncludeScope, Return: ReturnScope},
			)
			if err != nil {
				t.Fatal(err)
			}
			if len(response.Results) != 1 {
				t.Fatalf("results = %#v", response.Results)
			}
			result := response.Results[0]
			if result.StartLine != 1 || result.EndLine != test.wantEnd ||
				!strings.Contains(result.Code, "function run()") ||
				!strings.Contains(result.Code, "target()") {
				t.Fatalf("generic backtick-brace scope = %#v", result)
			}
		})
	}
}

func TestPreparedGenericBraceScopeReusesStructuralSource(t *testing.T) {
	lines := strings.Split("function run() {\n  /* } */\n  target();\n}\n", "\n")
	backend := prepareLanguageBackend(newBraceLanguage("xyz"), lines)
	prepared, ok := backend.(braceLanguage)
	if !ok || len(prepared.structuralLines) != len(lines) {
		t.Fatalf("prepared generic backend = %#v", backend)
	}
	var start, end int
	allocations := testing.AllocsPerRun(100, func() {
		start, end = prepared.enclosingScope(lines, 3)
	})
	if start != 1 || end != 4 {
		t.Fatalf("prepared generic scope = %d-%d, want 1-4", start, end)
	}
	if allocations != 0 {
		t.Fatalf("prepared generic scope allocated %.2f objects per hit", allocations)
	}
}

func mustView(t *testing.T, root string) *RepoView {
	t.Helper()
	view, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	return view
}

func writeFile(t *testing.T, root, name, body string) {
	t.Helper()
	path := filepath.Join(root, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertLocations(t *testing.T, results []Result, want []string) {
	t.Helper()
	if len(results) != len(want) {
		t.Fatalf("len = %d, want %d: %#v", len(results), len(want), results)
	}
	for i := range results {
		got := fmt.Sprintf("%s:%d", results[i].Path, results[i].Line)
		if strings.Contains(want[i], "-") {
			got = resultLocation(results[i])
		}
		if got != want[i] {
			t.Fatalf("locations[%d] = %q, want %q", i, got, want[i])
		}
	}
}

func resultLocation(result Result) string {
	if result.StartLine > 0 && result.EndLine > 0 {
		return fmt.Sprintf("%s:%d-%d", result.Path, result.StartLine, result.EndLine)
	}
	return fmt.Sprintf("%s:%d", result.Path, result.Line)
}
