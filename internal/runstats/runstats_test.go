package runstats

import (
	"os"
	"slices"
	"strings"
	"testing"
)

func analyzeFixture(t *testing.T, name string) Stats {
	t.Helper()
	input, err := os.Open("testdata/" + name + ".jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	stats, err := Analyze(input)
	if err != nil {
		t.Fatal(err)
	}
	return stats
}

func TestAnalyzeSimple(t *testing.T) {
	stats := analyzeFixture(t, "simple")
	if stats.TotalToolCalls != 1 || stats.ScopeSifterToolCalls != 1 ||
		stats.OtherToolCalls != 0 || stats.ScopeSifterInvocations != 1 {
		t.Fatalf("unexpected totals: %+v", stats)
	}
	if !stats.ScopeSifterCommandShapeValid ||
		len(stats.ScopeSifterShapeViolations) != 0 {
		t.Fatalf("unexpected command-shape result: %+v", stats)
	}
	if got := stats.Calls[0].PrimaryOperation; got != "scopesifter.changed" {
		t.Fatalf("primary operation = %q", got)
	}
	if count := findCount(stats.Operations, "find"); count.ToolCalls != 0 {
		t.Fatalf("scopesifter subcommand counted as shell find: %+v", count)
	}
}

func TestAnalyzeChainedCallGraph(t *testing.T) {
	stats := analyzeFixture(t, "chained")
	if stats.TotalToolCalls != 4 || stats.ScopeSifterToolCalls != 2 ||
		stats.OtherToolCalls != 2 || stats.ScopeSifterInvocations != 2 {
		t.Fatalf("unexpected totals: %+v", stats)
	}
	if stats.TemporalEdgeCount != 3 {
		t.Fatalf("temporal edges = %d", stats.TemporalEdgeCount)
	}
	if stats.OutputReferenceEdgeCount < 2 {
		t.Fatalf("output-reference edges = %d", stats.OutputReferenceEdgeCount)
	}
	var found bool
	for _, edge := range stats.CallGraph.Edges {
		if edge.From == "find-1" && edge.To == "inspect-2" &&
			edge.Kind == "output_reference" {
			found = slices.Contains(edge.Evidence, "path:pkg/session.go")
		}
	}
	if !found {
		t.Fatalf("missing literal path dependency: %+v", stats.CallGraph.Edges)
	}
}

func TestAnalyzeBatchedInvocations(t *testing.T) {
	stats := analyzeFixture(t, "batched")
	if stats.TotalToolCalls != 1 || stats.ScopeSifterToolCalls != 1 ||
		stats.ScopeSifterInvocations != 2 {
		t.Fatalf("unexpected batched totals: %+v", stats)
	}
	if stats.ScopeSifterCommandShapeValid ||
		len(stats.ScopeSifterShapeViolations) != 1 {
		t.Fatalf("compound navigation was not rejected: %+v", stats)
	}
	count := findCount(stats.Operations, "scopesifter.find")
	if count.ToolCalls != 1 || count.Invocations != 2 {
		t.Fatalf("scopesifter.find count = %+v", count)
	}
}

func TestAnalyzeCompoundAndNonCommandTools(t *testing.T) {
	stats := analyzeFixture(t, "compound")
	if stats.TotalToolCalls != 2 || stats.CommandExecutionToolCalls != 1 ||
		stats.OtherToolCalls != 2 {
		t.Fatalf("unexpected totals: %+v", stats)
	}
	operations := stats.Calls[0].Operations
	for _, expected := range []string{"go", "rg", "sed"} {
		if !slices.Contains(operations, expected) {
			t.Fatalf("operations %v do not contain %q", operations, expected)
		}
	}
	if stats.Calls[0].PrimaryOperation != "compound-shell" {
		t.Fatalf("primary operation = %q", stats.Calls[0].PrimaryOperation)
	}
	if stats.Calls[1].PrimaryOperation != "search" {
		t.Fatalf("non-command operation = %q", stats.Calls[1].PrimaryOperation)
	}
}

func TestAnalyzeOrdersOverlappingToolsByStartEvent(t *testing.T) {
	stats := analyzeFixture(t, "overlap")
	if stats.TotalToolCalls != 3 || stats.OtherToolCalls != 3 {
		t.Fatalf("unexpected totals: %+v", stats)
	}
	if stats.Calls[0].ToolType != "todo_list" {
		t.Fatalf("first call = %+v", stats.Calls[0])
	}
	if stats.TemporalEdgeCount != 1 {
		t.Fatalf("temporal edges = %d, want 1", stats.TemporalEdgeCount)
	}
	edge := stats.CallGraph.Edges[0]
	if edge.From != "command-1" || edge.To != "command-2" {
		t.Fatalf("unexpected temporal edge: %+v", edge)
	}
}

func TestAnalyzeRejectsInvalidJSON(t *testing.T) {
	_, err := Analyze(strings.NewReader("{"))
	if err == nil {
		t.Fatal("expected invalid JSON error")
	}
}

func TestAnalyzeRejectsInvalidCommandLifecycle(t *testing.T) {
	tests := map[string]string{
		"missing start":      `{"type":"item.completed","item":{"id":"c1","type":"command_execution","command":"scopesifter find X --json","aggregated_output":"[]","exit_code":0,"status":"completed"}}`,
		"missing completion": `{"type":"item.started","item":{"id":"c1","type":"command_execution","command":"scopesifter find X --json"}}`,
		"changed command": strings.Join([]string{
			`{"type":"item.started","item":{"id":"c1","type":"command_execution","command":"scopesifter find X --json"}}`,
			`{"type":"item.completed","item":{"id":"c1","type":"command_execution","command":"scopesifter find Y --json","aggregated_output":"[]","exit_code":0,"status":"completed"}}`,
		}, "\n"),
		"duplicate ID": strings.Join([]string{
			`{"type":"item.started","item":{"id":"c1","type":"command_execution","command":"scopesifter find X --json"}}`,
			`{"type":"item.started","item":{"id":"c1","type":"command_execution","command":"scopesifter find X --json"}}`,
		}, "\n"),
		"inconsistent status": strings.Join([]string{
			`{"type":"item.started","item":{"id":"c1","type":"command_execution","command":"scopesifter find X --json"}}`,
			`{"type":"item.completed","item":{"id":"c1","type":"command_execution","command":"scopesifter find X --json","aggregated_output":"[]","exit_code":1,"status":"completed"}}`,
		}, "\n"),
	}
	for name, transcript := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := Analyze(strings.NewReader(transcript)); err == nil {
				t.Fatal("expected command lifecycle error")
			}
		})
	}
}

func TestValidateScopeSifterCommand(t *testing.T) {
	valid := []string{
		"scopesifter find Symbol --root . --json",
		"/usr/bin/zsh -lc 'scopesifter changed --root . --base HEAD''^ --json'",
		`/usr/bin/zsh -lc "scopesifter inspect pkg/file.go:12 --root . --json"`,
	}
	for _, command := range valid {
		count, err := ValidateScopeSifterCommand(command)
		if err != nil || count != 1 {
			t.Errorf("ValidateScopeSifterCommand(%q) = %d, %v", command, count, err)
		}
		subcommand, err := ValidatedScopeSifterSubcommand(command)
		if err != nil || subcommand == "" {
			t.Errorf(
				"ValidatedScopeSifterSubcommand(%q) = %q, %v",
				command,
				subcommand,
				err,
			)
		}
	}

	invalid := []string{
		"printf fake scopesifter find Symbol --root . --json",
		"/usr/bin/zsh -lc 'for x in A B; do scopesifter find $x --root . --json; done'",
		"/usr/bin/zsh -lc 'scopesifter find A --root . --json && scopesifter find B --root . --json'",
		"/usr/bin/zsh -lc 'scopesifter find A --root . --json > /dev/null'",
		"/usr/bin/zsh -lc 'scopesifter find A --root .'",
		`scopesifter find "$SYMBOL" --root . --json`,
		`scopesifter find "$@" --root . --json`,
		`scopesifter find "$?" --root . --json`,
		`scopesifter find Symbol* --root . --json`,
		`scopesifter inspect pkg/\{one,two\}.go:1 --root . --json`,
		`scopesifter inspect pkg/file\ name.go:1 --root . --json`,
	}
	for _, command := range invalid {
		count, err := ValidateScopeSifterCommand(command)
		if err == nil || count < 1 {
			t.Errorf("ValidateScopeSifterCommand(%q) = %d, %v", command, count, err)
		}
	}
	if subcommand, err := ValidatedScopeSifterSubcommand("go test ./..."); err != nil ||
		subcommand != "" {
		t.Fatalf("non-navigation subcommand = %q, %v", subcommand, err)
	}
	if count, err := ValidateScopeSifterCommand(
		"notscopesifter find Symbol --json",
	); err != nil || count != 0 {
		t.Fatalf("suffix command count = %d, error = %v", count, err)
	}
}

func TestAnalyzeDoesNotCountExecutableSuffixAsScopeSifter(t *testing.T) {
	transcript := strings.Join([]string{
		`{"type":"item.started","item":{"id":"c1","type":"command_execution","command":"notscopesifter find Symbol --json"}}`,
		`{"type":"item.completed","item":{"id":"c1","type":"command_execution","command":"notscopesifter find Symbol --json","aggregated_output":"not found","exit_code":127,"status":"failed"}}`,
	}, "\n")
	stats, err := Analyze(strings.NewReader(transcript))
	if err != nil {
		t.Fatal(err)
	}
	if stats.ScopeSifterToolCalls != 0 || stats.ScopeSifterInvocations != 0 ||
		!stats.ScopeSifterCommandShapeValid {
		t.Fatalf("stats = %#v", stats)
	}
}

func TestAnalyzeDoesNotCountAssignmentValueAsScopeSifter(t *testing.T) {
	const command = `/bin/bash -lc 'X=/usr/bin/scopesifter find Symbol --json'`
	transcript := strings.Join([]string{
		`{"type":"item.started","item":{"id":"c1","type":"command_execution","command":"` + command + `"}}`,
		`{"type":"item.completed","item":{"id":"c1","type":"command_execution","command":"` + command + `","aggregated_output":"","exit_code":0,"status":"completed"}}`,
	}, "\n")
	stats, err := Analyze(strings.NewReader(transcript))
	if err != nil {
		t.Fatal(err)
	}
	if stats.ScopeSifterToolCalls != 0 || stats.ScopeSifterInvocations != 0 ||
		!stats.ScopeSifterCommandShapeValid {
		t.Fatalf("stats = %#v", stats)
	}
	if got := stats.Calls[0].PrimaryOperation; got != "find" {
		t.Fatalf("primary operation = %q, want find", got)
	}
}

func TestWriteDOT(t *testing.T) {
	stats := analyzeFixture(t, "chained")
	var output strings.Builder
	if err := WriteDOT(&output, stats); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), `"find-1" -> "inspect-2"`) {
		t.Fatalf("DOT output missing edge:\n%s", output.String())
	}
}

func TestWriteMarkdown(t *testing.T) {
	stats := analyzeFixture(t, "chained")
	var output strings.Builder
	if err := WriteMarkdown(&output, stats); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(output.String(), "| `find-1` | `inspect-2` | `output_reference` |") {
		t.Fatalf("Markdown output missing edge:\n%s", output.String())
	}
}

func findCount(counts []Count, name string) Count {
	for _, count := range counts {
		if count.Name == name {
			return count
		}
	}
	return Count{}
}
