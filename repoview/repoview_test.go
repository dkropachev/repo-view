package repoview

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

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
