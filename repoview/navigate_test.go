package repoview

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
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
	if err := os.Symlink(
		filepath.Join(parent, "outside.go"),
		filepath.Join(root, "linked.go"),
	); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}

	view := mustView(t, root)
	for name, location := range map[string]string{
		"parent traversal": "../outside.go:3",
		"file symlink":     "linked.go:3",
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
		"parent traversal": "../outside.go",
		"file symlink":     "linked.go",
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
	runGit(t, root, "config", "user.email", "repo-view@example.test")
	runGit(t, root, "config", "user.name", "repo-view test")
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

func TestChangedRejectsOptionLikeBase(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "repo-view@example.test")
	runGit(t, root, "config", "user.name", "repo-view test")
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
	runGit(t, root, "config", "user.email", "repo-view@example.test")
	runGit(t, root, "config", "user.name", "repo-view test")
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
	runGit(t, root, "config", "user.email", "repo-view@example.test")
	runGit(t, root, "config", "user.name", "repo-view test")
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
	runGit(t, root, "config", "user.email", "repo-view@example.test")
	runGit(t, root, "config", "user.name", "repo-view test")
	writeFile(t, root, "found.go", "package demo\n\nfunc before() {}\n")
	runGit(t, root, "add", "found.go")
	runGit(t, root, "commit", "-m", "initial")
	writeFile(t, root, "found.go", "package demo\n\nfunc after() {}\n")

	other := t.TempDir()
	runGit(t, other, "init")
	runGit(t, other, "config", "user.email", "repo-view@example.test")
	runGit(t, other, "config", "user.name", "repo-view test")
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

func TestChangedDoesNotExecuteConfiguredFilesystemMonitor(t *testing.T) {
	root := t.TempDir()
	runGit(t, root, "init")
	runGit(t, root, "config", "user.email", "repo-view@example.test")
	runGit(t, root, "config", "user.name", "repo-view test")
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
	runGit(t, root, "config", "user.email", "repo-view@example.test")
	runGit(t, root, "config", "user.name", "repo-view test")
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
	runGit(t, root, "config", "user.email", "repo-view@example.test")
	runGit(t, root, "config", "user.name", "repo-view test")
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
