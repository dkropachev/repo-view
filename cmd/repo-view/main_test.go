package main

import (
	"encoding/json"
	"flag"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/dkropachev/repo-view/repoview"
)

func TestSplitPositionalsAllowsFlagsAfterSymbol(t *testing.T) {
	symbols, flags, ok := splitPositionals([]string{"helper", "--root", ".", "--return", "scope", "--include", "refs"})
	if !ok {
		t.Fatal("symbol not found")
	}
	if len(symbols) != 1 || symbols[0] != "helper" {
		t.Fatalf("symbols = %#v", symbols)
	}
	want := []string{"--root", ".", "--return", "scope", "--include", "refs"}
	if len(flags) != len(want) {
		t.Fatalf("flags = %#v", flags)
	}
	for i := range want {
		if flags[i] != want[i] {
			t.Fatalf("flags[%d] = %q, want %q", i, flags[i], want[i])
		}
	}
}

func TestReturnLocationsMapsToNoEmbeddedCode(t *testing.T) {
	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	common := addCommonFlags(flags, repoview.ReturnScope)
	if err := flags.Parse([]string{"--return", "locations"}); err != nil {
		t.Fatal(err)
	}
	options, err := common.buildOptions(repoview.IncludeBoth)
	if err != nil {
		t.Fatal(err)
	}
	if options.Return != repoview.ReturnLocations {
		t.Fatalf("return = %q", options.Return)
	}
}

func TestFenceLanguageCoversJavaScriptAndTypeScriptExtensions(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		path string
		want string
	}{
		{path: "script.js", want: "javascript"},
		{path: "module.mjs", want: "javascript"},
		{path: "common.cjs", want: "javascript"},
		{path: "view.jsx", want: "jsx"},
		{path: "source.ts", want: "typescript"},
		{path: "view.tsx", want: "tsx"},
		{path: "module.mts", want: "typescript"},
		{path: "common.cts", want: "typescript"},
	} {
		if got := fenceLanguage(test.path); got != test.want {
			t.Fatalf("fenceLanguage(%q) = %q, want %q", test.path, got, test.want)
		}
	}
}

func TestFenceLanguageCoversKotlinSourceAndScriptExtensions(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"source.kt", "build.gradle.kts"} {
		if got := fenceLanguage(path); got != "kotlin" {
			t.Fatalf("fenceLanguage(%q) = %q, want kotlin", path, got)
		}
	}
}

func TestFenceLanguageCoversSwiftSourceExtension(t *testing.T) {
	t.Parallel()

	if got := fenceLanguage("Sources/App/Service.swift"); got != "swift" {
		t.Fatalf("fenceLanguage(Service.swift) = %q, want swift", got)
	}
}

func TestFenceLanguageCoversModulaSourceExtensions(t *testing.T) {
	t.Parallel()

	for _, path := range []string{"src/Program.mod", "lib/Storage.def"} {
		if got := fenceLanguage(path); got != "modula-2" {
			t.Fatalf("fenceLanguage(%q) = %q, want modula-2", path, got)
		}
	}
}

func TestPrintLocationsUsesPointLine(t *testing.T) {
	output := captureStdout(t, func() {
		printResults([]repoview.Result{{
			Path:      "found.go",
			Line:      7,
			StartLine: 4,
			EndLine:   10,
		}}, repoview.ReturnLocations)
	})
	if output != "found.go:7\n" {
		t.Fatalf("output = %q", output)
	}
}

func TestPrintLocationsUsesRangeStartWithoutPointLine(t *testing.T) {
	output := captureStdout(t, func() {
		printResults([]repoview.Result{{
			Path:      "found.go",
			StartLine: 4,
			EndLine:   10,
		}}, repoview.ReturnLocations)
	})
	if output != "found.go:4\n" {
		t.Fatalf("output = %q", output)
	}
}

func TestReturnRejectsUnknownValues(t *testing.T) {
	for _, value := range []string{"invalid"} {
		t.Run(value, func(t *testing.T) {
			flags := flag.NewFlagSet("test", flag.ContinueOnError)
			common := addCommonFlags(flags, repoview.ReturnScope)
			if err := flags.Parse([]string{"--return", value}); err != nil {
				t.Fatal(err)
			}
			_, err := common.buildOptions(repoview.IncludeBoth)
			if err == nil || !strings.Contains(err.Error(), "--return must be one of") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestChangedRejectsPositionalArguments(t *testing.T) {
	if status := run([]string{
		"changed",
		"unexpected-positional",
		"--root", "/definitely/not/a/repository",
		"--json",
	}); status != 2 {
		t.Fatalf("changed status = %d, want 2", status)
	}
}

func TestNavigationCapsRejectOversizedOptions(t *testing.T) {
	t.Setenv("REPO_VIEW_LIMIT_CAP", "20")
	t.Setenv("REPO_VIEW_CONTEXT_CAP", "10")
	t.Setenv("REPO_VIEW_MAX_CODE_LINES_CAP", "60")

	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	common := addCommonFlags(flags, repoview.ReturnScope)
	if err := flags.Parse([]string{"--limit", "21", "--context", "11", "--max-code-lines", "61"}); err != nil {
		t.Fatal(err)
	}
	_, err := common.buildOptions(repoview.IncludeBoth)
	if err == nil || !strings.Contains(err.Error(), "--limit 21 exceeds REPO_VIEW_LIMIT_CAP 20") {
		t.Fatalf("error = %v", err)
	}
}

func TestNavigationRejectsZeroCodeAndPatchLimits(t *testing.T) {
	tests := []struct {
		option string
		want   string
	}{
		{"--max-code-lines", "use --return locations to omit code"},
		{"--max-patch-lines", "--max-patch-lines must be positive"},
	}
	for _, test := range tests {
		t.Run(test.option, func(t *testing.T) {
			flags := flag.NewFlagSet("test", flag.ContinueOnError)
			common := addCommonFlags(flags, repoview.ReturnScope)
			if err := flags.Parse([]string{test.option, "0"}); err != nil {
				t.Fatal(err)
			}
			_, err := common.buildOptions(repoview.IncludeBoth)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestLocationReturnIgnoresOmittedCodeLineCap(t *testing.T) {
	t.Setenv("REPO_VIEW_MAX_CODE_LINES_CAP", "60")

	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	common := addCommonFlags(flags, repoview.ReturnScope)
	if err := flags.Parse([]string{"--return", "locations"}); err != nil {
		t.Fatal(err)
	}
	options, err := common.buildOptions(repoview.IncludeBoth)
	if err != nil {
		t.Fatal(err)
	}
	if options.Return != repoview.ReturnLocations {
		t.Fatalf("return = %q", options.Return)
	}

	flags = flag.NewFlagSet("test", flag.ContinueOnError)
	common = addCommonFlags(flags, repoview.ReturnScope)
	if err := flags.Parse([]string{
		"--return", "locations",
		"--max-code-lines", "61",
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := common.buildOptions(repoview.IncludeBoth); err == nil ||
		!strings.Contains(err.Error(), "--max-code-lines 61 exceeds") {
		t.Fatalf("explicit code-line cap error = %v", err)
	}
}

func TestEnforcedOptionCapsCannotBeDisabledByEnvironment(t *testing.T) {
	tests := []struct {
		name        string
		option      string
		environment string
		enforced    *string
	}{
		{"limit", "--limit", "REPO_VIEW_LIMIT_CAP", &enforcedLimitCap},
		{"context", "--context", "REPO_VIEW_CONTEXT_CAP", &enforcedContextCap},
		{"code lines", "--max-code-lines", "REPO_VIEW_MAX_CODE_LINES_CAP", &enforcedMaxCodeLinesCap},
		{"patch lines", "--max-patch-lines", "REPO_VIEW_MAX_PATCH_LINES_CAP", &enforcedMaxPatchLinesCap},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			previous := *test.enforced
			*test.enforced = "10"
			t.Cleanup(func() {
				*test.enforced = previous
			})
			t.Setenv(test.environment, "999")

			err := enforceOptionCap(test.option, 11, test.environment)
			if err == nil || !strings.Contains(err.Error(), test.environment+" 10") {
				t.Fatalf("error = %v", err)
			}
		})
	}
}

func TestCompiledOptionCapSurvivesUnsetEnvironment(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "repo-view")
	build := exec.Command(
		"go",
		"build",
		"-ldflags",
		"-X main.enforcedLimitCap=2",
		"-o",
		binary,
		".",
	)
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build: %v\n%s", err, output)
	}

	root := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(root, "demo.go"),
		[]byte("package demo\n\nfunc Target() {}\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(
		binary,
		"find",
		"Target",
		"--root",
		root,
		"--include",
		"defs",
		"--return",
		"locations",
		"--limit",
		"3",
		"--json",
	)
	for _, item := range os.Environ() {
		if !strings.HasPrefix(item, "REPO_VIEW_LIMIT_CAP=") {
			command.Env = append(command.Env, item)
		}
	}
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(
		string(output),
		"--limit 3 exceeds REPO_VIEW_LIMIT_CAP 2",
	) {
		t.Fatalf("error = %v, output = %s", err, output)
	}
}

func TestCommonFlagsPreserveRepeatablePathFilters(t *testing.T) {
	flags := flag.NewFlagSet("test", flag.ContinueOnError)
	common := addCommonFlags(flags, repoview.ReturnScope)
	if err := flags.Parse([]string{
		"--path", "service/matching",
		"--path", "common/quotas/**",
		"--exclude", "*_test.go",
	}); err != nil {
		t.Fatal(err)
	}
	options, err := common.buildOptions(repoview.IncludeBoth)
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(options.PathGlobs, ","); got != "service/matching,common/quotas/**" {
		t.Fatalf("path globs = %q", got)
	}
	if got := strings.Join(options.ExcludeGlobs, ","); got != "*_test.go" {
		t.Fatalf("exclude globs = %q", got)
	}
}

func TestOptionCapRejectsInvalidEnvironment(t *testing.T) {
	t.Setenv("REPO_VIEW_LIMIT_CAP", "many")
	err := enforceOptionCap("--limit", 20, "REPO_VIEW_LIMIT_CAP")
	if err == nil || !strings.Contains(err.Error(), "must be a non-negative integer") {
		t.Fatalf("error = %v", err)
	}
}

func TestOptionCapRejectsZeroValueBypasses(t *testing.T) {
	t.Setenv("REPO_VIEW_LIMIT_CAP", "20")
	if err := enforceOptionCap("--limit", 0, "REPO_VIEW_LIMIT_CAP"); err == nil ||
		!strings.Contains(err.Error(), "disables the result limit") {
		t.Fatalf("limit error = %v", err)
	}

	t.Setenv("REPO_VIEW_MAX_CODE_LINES_CAP", "60")
	if err := enforceOptionCap("--max-code-lines", 0, "REPO_VIEW_MAX_CODE_LINES_CAP"); err == nil ||
		!strings.Contains(err.Error(), "use --return locations to omit code") {
		t.Fatalf("code error = %v", err)
	}
}

func TestNavigationBudgetPersistsAcrossCommands(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "budget")
	t.Setenv("REPO_VIEW_NAVIGATION_COMMAND_CAP", "2")
	t.Setenv("REPO_VIEW_NAVIGATION_BUDGET_FILE", statePath)

	first, err := consumeNavigationBudget()
	if err != nil {
		t.Fatal(err)
	}
	second, err := consumeNavigationBudget()
	if err != nil {
		t.Fatal(err)
	}
	if first.Used != 1 || first.Remaining != 1 || second.Used != 2 || second.Remaining != 0 {
		t.Fatalf("budgets = %#v, %#v", first, second)
	}
	if _, err := consumeNavigationBudget(); err == nil ||
		!strings.Contains(err.Error(), "budget exhausted: 2/2 used") {
		t.Fatalf("error = %v", err)
	}
}

func TestNavigationBudgetSerializesConcurrentCommands(t *testing.T) {
	const (
		limit    = 8
		attempts = 24
	)
	statePath := filepath.Join(t.TempDir(), "budget")
	t.Setenv("REPO_VIEW_NAVIGATION_COMMAND_CAP", "8")
	t.Setenv("REPO_VIEW_NAVIGATION_BUDGET_FILE", statePath)

	type result struct {
		budget *repoview.NavigationBudget
		err    error
	}
	start := make(chan struct{})
	results := make(chan result, attempts)
	var wait sync.WaitGroup
	for range attempts {
		wait.Add(1)
		go func() {
			defer wait.Done()
			<-start
			budget, err := consumeNavigationBudget()
			results <- result{budget: budget, err: err}
		}()
	}
	close(start)
	wait.Wait()
	close(results)

	seen := make([]bool, limit+1)
	successes := 0
	exhausted := 0
	for current := range results {
		if current.err != nil {
			if !strings.Contains(current.err.Error(), "budget exhausted: 8/8 used") {
				t.Fatalf("unexpected budget error: %v", current.err)
			}
			exhausted++
			continue
		}
		successes++
		if current.budget.Used < 1 ||
			current.budget.Used > limit ||
			seen[current.budget.Used] {
			t.Fatalf("duplicate or invalid budget: %#v", current.budget)
		}
		seen[current.budget.Used] = true
	}
	if successes != limit || exhausted != attempts-limit {
		t.Fatalf("successes = %d, exhausted = %d", successes, exhausted)
	}
	content, err := os.ReadFile(statePath)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "8\n" {
		t.Fatalf("budget state = %q", content)
	}
	if _, err := os.Lstat(statePath + ".lock"); !os.IsNotExist(err) {
		t.Fatalf("navigation lock was not removed: %v", err)
	}
}

func TestNavigationBudgetCountsLiveTranscript(t *testing.T) {
	transcript, err := os.CreateTemp(t.TempDir(), "transcript.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer transcript.Close()
	writeStarted := func(command string) {
		t.Helper()
		event := map[string]any{
			"type": "item.started",
			"item": map[string]any{
				"type":    "command_execution",
				"command": command,
			},
		}
		if err := json.NewEncoder(transcript).Encode(event); err != nil {
			t.Fatal(err)
		}
	}
	writeStarted("repo-view changed --root . --json")
	if err := json.NewEncoder(transcript).Encode(map[string]any{
		"type": "item.completed",
		"item": map[string]any{
			"type":    "command_execution",
			"command": "repo-view changed --root . --json",
		},
	}); err != nil {
		t.Fatal(err)
	}
	writeStarted("repo-view find First Second --root . --json")

	budget, err := consumeNavigationTranscriptBudget(transcript.Name(), 3)
	if err != nil {
		t.Fatal(err)
	}
	if budget.Used != 2 || budget.Remaining != 1 {
		t.Fatalf("budget = %#v", budget)
	}

	writeStarted("repo-view inspect first.go:1 --root . --json; repo-view outline first.go --root . --json")
	if _, err := consumeNavigationTranscriptBudget(transcript.Name(), 3); err == nil ||
		!strings.Contains(err.Error(), "unsafe repo-view command") {
		t.Fatalf("error = %v", err)
	}
}

func TestNavigationBudgetRejectsLoopedInvocation(t *testing.T) {
	transcript, err := os.CreateTemp(t.TempDir(), "transcript.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer transcript.Close()
	if err := json.NewEncoder(transcript).Encode(map[string]any{
		"type": "item.started",
		"item": map[string]any{
			"type": "command_execution",
			"command": "for name in A B C; do " +
				"repo-view find \"$name\" --root . --json; done",
		},
	}); err != nil {
		t.Fatal(err)
	}

	if _, err := consumeNavigationTranscriptBudget(
		transcript.Name(),
		1,
	); err == nil || !strings.Contains(err.Error(), "unsafe repo-view command") {
		t.Fatalf("error = %v", err)
	}
}

func TestEnforcedNavigationBudgetUsesReadOnlyTranscriptWithoutEnvironment(t *testing.T) {
	directory := t.TempDir()
	transcriptPath := filepath.Join(directory, "transcript.jsonl")
	transcript, err := os.Create(transcriptPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.NewEncoder(transcript).Encode(map[string]any{
		"type": "item.started",
		"item": map[string]any{
			"type":    "command_execution",
			"command": "repo-view changed --root . --json",
		},
	}); err != nil {
		t.Fatal(err)
	}
	if err := transcript.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(transcriptPath, 0o444); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(directory, 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(directory, 0o755)
		_ = os.Chmod(transcriptPath, 0o644)
	})

	previousCap := enforcedNavigationCommandCap
	previousPath := enforcedNavigationTranscriptPath
	enforcedNavigationCommandCap = "2"
	enforcedNavigationTranscriptPath = transcriptPath
	t.Cleanup(func() {
		enforcedNavigationCommandCap = previousCap
		enforcedNavigationTranscriptPath = previousPath
	})
	t.Setenv("REPO_VIEW_NAVIGATION_COMMAND_CAP", "0")
	t.Setenv("REPO_VIEW_NAVIGATION_BUDGET_FILE", filepath.Join(directory, "unwritable-state"))

	budget, err := consumeNavigationBudget()
	if err != nil {
		t.Fatal(err)
	}
	if budget.Used != 1 || budget.Limit != 2 || budget.Remaining != 1 {
		t.Fatalf("budget = %#v", budget)
	}
}

func TestNavigationBudgetWaitsForCurrentStartedEvent(t *testing.T) {
	transcript, err := os.CreateTemp(t.TempDir(), "transcript.jsonl")
	if err != nil {
		t.Fatal(err)
	}
	defer transcript.Close()

	writeDone := make(chan error, 1)
	go func() {
		time.Sleep(10 * time.Millisecond)
		writeDone <- json.NewEncoder(transcript).Encode(map[string]any{
			"type": "item.started",
			"item": map[string]any{
				"type":    "command_execution",
				"command": "repo-view changed --root . --json",
			},
		})
	}()

	budget, err := consumeNavigationTranscriptBudget(transcript.Name(), 2)
	if err != nil {
		t.Fatal(err)
	}
	if err := <-writeDone; err != nil {
		t.Fatal(err)
	}
	if budget.Used != 1 || budget.Remaining != 1 {
		t.Fatalf("budget = %#v", budget)
	}
}

func TestMechanicallyEnforcedNavigationSemantics(t *testing.T) {
	root := t.TempDir()
	base := strings.Repeat("a", 40)
	setMechanicalNavigationContract(t, root, base, "locations", "0")

	tests := []struct {
		name       string
		subcommand string
		args       []string
		wantError  string
	}{
		{
			name:       "changed exact profile with location context zero",
			subcommand: "changed",
			args: []string{
				"--root", root,
				"--base", base,
				"--return", "locations",
				"--context", "0",
				"--limit", "20",
				"--max-code-lines", "60",
				"--max-patch-lines", "300",
				"--json",
			},
		},
		{
			name:       "missing bounded option",
			subcommand: "changed",
			args: []string{
				"--root", root,
				"--base", base,
				"--return", "locations",
				"--context", "0",
				"--limit", "20",
				"--max-code-lines", "60",
				"--json",
			},
			wantError: "--max-patch-lines",
		},
		{
			name:       "symbolic base",
			subcommand: "changed",
			args: []string{
				"--root", root,
				"--base", "HEAD^",
				"--return", "locations",
				"--context", "0",
				"--limit", "20",
				"--max-code-lines", "60",
				"--max-patch-lines", "300",
				"--json",
			},
			wantError: "does not match enforced base commit",
		},
		{
			name:       "location followup permits zero context",
			subcommand: "find",
			args: []string{
				"--root", root,
				"--return", "locations",
				"--context", "0",
				"--limit", "10",
				"--max-code-lines", "40",
				"--max-patch-lines", "200",
				"--json",
			},
		},
		{
			name:       "scope followup rejects zero context",
			subcommand: "find",
			args: []string{
				"--root", root,
				"--return", "scope",
				"--context", "0",
				"--limit", "10",
				"--max-code-lines", "40",
				"--max-patch-lines", "200",
				"--json",
			},
			wantError: "positive --context unless --return locations",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			flags := flag.NewFlagSet(test.subcommand, flag.ContinueOnError)
			flags.SetOutput(io.Discard)
			common := addCommonFlags(flags, repoview.ReturnScope)
			if err := flags.Parse(test.args); err != nil {
				t.Fatal(err)
			}
			options, err := common.buildOptions(repoview.IncludeAll)
			if err == nil {
				err = common.enforceNavigationSemantics(
					test.subcommand,
					options,
				)
			}
			if test.wantError == "" {
				if err != nil {
					t.Fatal(err)
				}
			} else if err == nil || !strings.Contains(err.Error(), test.wantError) {
				t.Fatalf("error = %v, want %q", err, test.wantError)
			}
		})
	}
}

func TestMechanicallyEnforcedNavigationSequence(t *testing.T) {
	valid := [][]string{
		{"changed"},
		{"changed", "find"},
		{"changed", "find", "inspect", "outline"},
	}
	for _, subcommands := range valid {
		if err := validateNavigationSequence(
			subcommands,
			subcommands[len(subcommands)-1],
		); err != nil {
			t.Errorf("valid sequence %v: %v", subcommands, err)
		}
	}
	invalid := []struct {
		subcommands []string
		current     string
	}{
		{[]string{"find"}, "find"},
		{[]string{"changed", "find", "changed"}, "changed"},
		{[]string{"changed", "find"}, "inspect"},
	}
	for _, test := range invalid {
		if err := validateNavigationSequence(
			test.subcommands,
			test.current,
		); err == nil {
			t.Errorf("invalid sequence accepted: %v / %s", test.subcommands, test.current)
		}
	}
}

func TestMechanicallyEnforcedLiveTranscriptSequence(t *testing.T) {
	root := t.TempDir()
	base := strings.Repeat("a", 40)
	setMechanicalNavigationContract(t, root, base, "context", "4")
	transcriptPath := filepath.Join(root, "transcript.jsonl")
	transcript, err := os.Create(transcriptPath)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(transcript)
	writeEvent := func(eventType, command string) {
		t.Helper()
		if err := encoder.Encode(map[string]any{
			"type": eventType,
			"item": map[string]any{
				"type":    "command_execution",
				"command": command,
			},
		}); err != nil {
			t.Fatal(err)
		}
	}
	changed := "repo-view changed --root . --json"
	find := "repo-view find Symbol --root . --json"
	writeEvent("item.started", changed)
	if budget, err := consumeNavigationBudgetFor("changed"); err != nil {
		t.Fatal(err)
	} else if budget.Used != 1 {
		t.Fatalf("changed budget = %#v", budget)
	}
	writeEvent("item.completed", changed)
	writeEvent("item.started", find)
	if budget, err := consumeNavigationBudgetFor("find"); err != nil {
		t.Fatal(err)
	} else if budget.Used != 2 {
		t.Fatalf("find budget = %#v", budget)
	}
	writeEvent("item.completed", find)
	writeEvent("item.started", changed)
	if _, err := consumeNavigationBudgetFor("changed"); err == nil ||
		!strings.Contains(err.Error(), "exactly once") {
		t.Fatalf("repeated changed error = %v", err)
	}
	if err := transcript.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSplitPositionalsAcceptsSingleDashValueFlag(t *testing.T) {
	symbols, flags, ok := splitPositionals([]string{"helper", "-root", ".", "-return", "scope"})
	if !ok || len(symbols) != 1 || symbols[0] != "helper" {
		t.Fatalf("symbols = %#v", symbols)
	}
	if got := strings.Join(flags, " "); got != "-root . -return scope" {
		t.Fatalf("flags = %q", got)
	}
}

func TestValidateFindIncludeRejectsInspectOnlyValue(t *testing.T) {
	err := validateInclude("find", repoview.IncludeAll, repoview.IncludeDefs, repoview.IncludeRefs, repoview.IncludeBoth)
	if err == nil || !strings.Contains(err.Error(), "find --include") {
		t.Fatalf("error = %v", err)
	}
}

func TestFairResultLimitReservesBudgetForEverySymbol(t *testing.T) {
	remaining := 20
	var got []int
	for symbols := 6; symbols > 0; symbols-- {
		share := fairResultLimit(remaining, symbols)
		got = append(got, share)
		remaining -= share
	}
	want := []int{4, 4, 3, 3, 3, 3}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("shares = %#v, want %#v", got, want)
		}
	}
}

func TestInspectAcceptsMultipleLocations(t *testing.T) {
	root := t.TempDir()
	source := "package demo\n\nfunc first() {}\n\nfunc second() {}\n"
	if err := os.WriteFile(filepath.Join(root, "demo.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout := captureStdout(t, func() {
		if status := run([]string{
			"inspect",
			"demo.go:3",
			"demo.go:5",
			"--root", root,
			"--include", "scope",
			"--return", "line",
			"--limit", "2",
			"--json",
		}); status != 0 {
			t.Fatalf("status = %d", status)
		}
	})

	var responses []repoview.InspectResponse
	if err := json.Unmarshal([]byte(stdout), &responses); err != nil {
		t.Fatalf("decode response: %v\n%s", err, stdout)
	}
	if len(responses) != 2 {
		t.Fatalf("responses = %#v", responses)
	}
	if responses[0].Location != "demo.go:3" || responses[1].Location != "demo.go:5" {
		t.Fatalf("locations = %q, %q", responses[0].Location, responses[1].Location)
	}
	if len(responses[0].Results) != 1 || len(responses[1].Results) != 1 {
		t.Fatalf("results = %#v", responses)
	}
}

func TestInspectDefaultsToScopeOnly(t *testing.T) {
	root := t.TempDir()
	source := "package demo\n\nfunc helper() {}\n\nfunc caller() {\n\thelper()\n}\n"
	if err := os.WriteFile(filepath.Join(root, "demo.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout := captureStdout(t, func() {
		if status := run([]string{
			"inspect",
			"demo.go:6",
			"--root", root,
			"--return", "scope",
			"--limit", "20",
			"--json",
		}); status != 0 {
			t.Fatalf("status = %d", status)
		}
	})

	var response repoview.InspectResponse
	if err := json.Unmarshal([]byte(stdout), &response); err != nil {
		t.Fatalf("decode response: %v\n%s", err, stdout)
	}
	if len(response.Results) != 1 || response.Results[0].Kind != "scope" ||
		response.Results[0].Scope != "caller" {
		t.Fatalf("response = %#v", response)
	}
}

func TestInspectBatchRetainsValidLocationsWhenOneIsInvalid(t *testing.T) {
	root := t.TempDir()
	source := "package demo\n\nfunc first() {}\n\nfunc second() {}\n"
	if err := os.WriteFile(filepath.Join(root, "demo.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}

	var status int
	stdout := captureStdout(t, func() {
		status = run([]string{
			"inspect",
			"demo.go:3",
			"demo.go:99",
			"demo.go:5",
			"--root", root,
			"--include", "scope",
			"--return", "line",
			"--limit", "3",
			"--json",
		})
	})
	if status != 0 {
		t.Fatalf("status = %d", status)
	}

	var responses []repoview.InspectResponse
	if err := json.Unmarshal([]byte(stdout), &responses); err != nil {
		t.Fatalf("decode response: %v\n%s", err, stdout)
	}
	if len(responses) != 3 {
		t.Fatalf("responses = %#v", responses)
	}
	if len(responses[0].Results) != 1 || len(responses[2].Results) != 1 {
		t.Fatalf("valid results discarded: %#v", responses)
	}
	if responses[1].Location != "demo.go:99" ||
		!strings.Contains(responses[1].Error, "line 99 out of range") ||
		responses[1].Results == nil {
		t.Fatalf("invalid response = %#v", responses[1])
	}
}

func TestInspectBatchFailsWhenEveryLocationIsInvalid(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(root, "demo.go"),
		[]byte("package demo\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}

	var status int
	stdout := captureStdout(t, func() {
		status = run([]string{
			"inspect",
			"demo.go:2",
			"demo.go:3",
			"--root", root,
			"--include", "scope",
			"--return", "line",
			"--limit", "2",
			"--json",
		})
	})
	if status == 0 {
		t.Fatal("expected all-invalid batch to fail")
	}

	var responses []repoview.InspectResponse
	if err := json.Unmarshal([]byte(stdout), &responses); err != nil {
		t.Fatalf("decode response: %v\n%s", err, stdout)
	}
	if len(responses) != 2 || responses[0].Error == "" || responses[1].Error == "" {
		t.Fatalf("responses = %#v", responses)
	}
}

func TestFindHonorsPathFilterAndUsesEmptyArrays(t *testing.T) {
	root := t.TempDir()
	for path, source := range map[string]string{
		"first/one.go":          "package first\n\nfunc target() {}\nfunc caller() { target() }\n",
		"first/skipped_test.go": "package first\n\nfunc callerTest() { target() }\n",
		"second/two.go":         "package second\n\nfunc target() {}\nfunc caller() { target() }\n",
	} {
		absolute := filepath.Join(root, path)
		if err := os.MkdirAll(filepath.Dir(absolute), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(absolute, []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	stdout := captureStdout(t, func() {
		if status := run([]string{
			"find",
			"target",
			"missing",
			"--root", root,
			"--include", "refs",
			"--return", "locations",
			"--path", "first",
			"--exclude", "*_test.go",
			"--limit", "4",
			"--json",
		}); status != 0 {
			t.Fatalf("status = %d", status)
		}
	})

	var responses []repoview.FindResponse
	if err := json.Unmarshal([]byte(stdout), &responses); err != nil {
		t.Fatalf("decode response: %v\n%s", err, stdout)
	}
	if len(responses) != 2 || len(responses[0].Results) != 1 {
		t.Fatalf("responses = %#v", responses)
	}
	if got := responses[0].Results[0].Path; got != "first/one.go" {
		t.Fatalf("path = %q", got)
	}
	if responses[1].Results == nil || len(responses[1].Results) != 0 {
		t.Fatalf("missing results = %#v", responses[1].Results)
	}
	if !strings.Contains(stdout, `"results":[]`) {
		t.Fatalf("empty results are not an array: %s", stdout)
	}
}

func TestFindFairLimitReturnsEveryRequestedSymbol(t *testing.T) {
	root := t.TempDir()
	source := `package demo

func Alpha() {}
func Bravo() {}
func Charlie() {}
func Delta() {}
func Echo() {}
func Foxtrot() {}
`
	if err := os.WriteFile(filepath.Join(root, "demo.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}

	stdout := captureStdout(t, func() {
		if status := run([]string{
			"find",
			"Alpha", "Bravo", "Charlie", "Delta", "Echo", "Foxtrot",
			"--root", root,
			"--include", "defs",
			"--return", "locations",
			"--limit", "6",
			"--json",
		}); status != 0 {
			t.Fatalf("status = %d", status)
		}
	})

	var responses []repoview.FindResponse
	if err := json.Unmarshal([]byte(stdout), &responses); err != nil {
		t.Fatalf("decode response: %v\n%s", err, stdout)
	}
	if len(responses) != 6 {
		t.Fatalf("responses = %#v", responses)
	}
	for index, response := range responses {
		if len(response.Results) != 1 {
			t.Fatalf("responses[%d] = %#v", index, response)
		}
	}
}

func TestOutlineAcceptsMultiplePaths(t *testing.T) {
	root := t.TempDir()
	for path, source := range map[string]string{
		"first.go":  "package demo\n\nfunc first() {}\n",
		"second.go": "package demo\n\nfunc second() {}\n",
	} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	stdout := captureStdout(t, func() {
		if status := run([]string{
			"outline",
			"first.go",
			"second.go",
			"--root", root,
			"--return", "line",
			"--limit", "2",
			"--json",
		}); status != 0 {
			t.Fatalf("status = %d", status)
		}
	})

	var responses []repoview.OutlineResponse
	if err := json.Unmarshal([]byte(stdout), &responses); err != nil {
		t.Fatalf("decode response: %v\n%s", err, stdout)
	}
	if len(responses) != 2 || responses[0].Path != "first.go" || responses[1].Path != "second.go" {
		t.Fatalf("responses = %#v", responses)
	}
	if len(responses[0].Results) != 1 || len(responses[1].Results) != 1 {
		t.Fatalf("results = %#v", responses)
	}
}

func TestSwiftCLIOutlineFindAndInspectUseDedicatedBackend(t *testing.T) {
	root := t.TempDir()
	const source = `import Foundation

final class Service {
    let value = 1
    func run() {
        Target()
    }
}
`
	if err := os.WriteFile(filepath.Join(root, "Service.swift"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}

	outlineJSON := captureStdout(t, func() {
		if status := run([]string{
			"outline", "Service.swift",
			"--root", root,
			"--return", "locations",
			"--limit", "10",
			"--json",
		}); status != 0 {
			t.Fatalf("outline status = %d", status)
		}
	})
	var outline repoview.OutlineResponse
	if err := json.Unmarshal([]byte(outlineJSON), &outline); err != nil {
		t.Fatalf("decode Swift outline: %v\n%s", err, outlineJSON)
	}
	wantSymbols := []string{"Service", "value", "run"}
	if len(outline.Results) != len(wantSymbols) {
		t.Fatalf("Swift outline = %#v, want %v", outline.Results, wantSymbols)
	}
	for index, want := range wantSymbols {
		result := outline.Results[index]
		if result.Symbol != want || result.Kind != "def" || result.Language != "swift" {
			t.Errorf("Swift outline result %d = %#v, want %q definition", index, result, want)
		}
	}

	findJSON := captureStdout(t, func() {
		if status := run([]string{
			"find", "Target",
			"--root", root,
			"--include", "refs",
			"--return", "scope",
			"--limit", "10",
			"--json",
		}); status != 0 {
			t.Fatalf("find status = %d", status)
		}
	})
	var found []repoview.FindResponse
	if err := json.Unmarshal([]byte(findJSON), &found); err != nil {
		t.Fatalf("decode Swift find: %v\n%s", err, findJSON)
	}
	if len(found) != 1 || len(found[0].Results) != 1 ||
		found[0].Results[0].Path != "Service.swift" ||
		found[0].Results[0].Language != "swift" ||
		found[0].Results[0].Scope != "run" ||
		found[0].Results[0].StartLine != 5 || found[0].Results[0].EndLine != 7 {
		t.Fatalf("Swift find = %#v, want Target in run at 5-7", found)
	}

	inspectJSON := captureStdout(t, func() {
		if status := run([]string{
			"inspect", "Service.swift:6",
			"--root", root,
			"--include", "scope",
			"--return", "scope",
			"--limit", "10",
			"--json",
		}); status != 0 {
			t.Fatalf("inspect status = %d", status)
		}
	})
	var inspected repoview.InspectResponse
	if err := json.Unmarshal([]byte(inspectJSON), &inspected); err != nil {
		t.Fatalf("decode Swift inspect: %v\n%s", err, inspectJSON)
	}
	if inspected.Symbol != "Target" || len(inspected.Results) != 1 ||
		inspected.Results[0].Language != "swift" || inspected.Results[0].Scope != "run" {
		t.Fatalf("Swift inspect = %#v, want Target in run", inspected)
	}
}

func TestOutlineBatchRetainsValidPathsWhenOneIsInvalid(t *testing.T) {
	root := t.TempDir()
	for path, source := range map[string]string{
		"first.go":  "package demo\n\nfunc first() {}\n",
		"second.go": "package demo\n\nfunc second() {}\n",
	} {
		if err := os.WriteFile(filepath.Join(root, path), []byte(source), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var status int
	stdout := captureStdout(t, func() {
		status = run([]string{
			"outline",
			"first.go",
			"missing.go",
			"second.go",
			"--root", root,
			"--return", "line",
			"--limit", "3",
			"--json",
		})
	})
	if status != 0 {
		t.Fatalf("status = %d", status)
	}

	var responses []repoview.OutlineResponse
	if err := json.Unmarshal([]byte(stdout), &responses); err != nil {
		t.Fatalf("decode response: %v\n%s", err, stdout)
	}
	if len(responses) != 3 {
		t.Fatalf("responses = %#v", responses)
	}
	if len(responses[0].Results) != 1 || len(responses[2].Results) != 1 {
		t.Fatalf("valid results discarded: %#v", responses)
	}
	if responses[1].Path != "missing.go" ||
		!strings.Contains(responses[1].Error, "no such file") ||
		responses[1].Results == nil {
		t.Fatalf("invalid response = %#v", responses[1])
	}
}

func TestOutlineBatchFailsWhenEveryPathIsInvalid(t *testing.T) {
	root := t.TempDir()

	var status int
	stdout := captureStdout(t, func() {
		status = run([]string{
			"outline",
			"missing.go",
			"also-missing.go",
			"--root", root,
			"--return", "line",
			"--limit", "2",
			"--json",
		})
	})
	if status == 0 {
		t.Fatal("expected all-invalid batch to fail")
	}

	var responses []repoview.OutlineResponse
	if err := json.Unmarshal([]byte(stdout), &responses); err != nil {
		t.Fatalf("decode response: %v\n%s", err, stdout)
	}
	if len(responses) != 2 || responses[0].Error == "" || responses[1].Error == "" {
		t.Fatalf("responses = %#v", responses)
	}
}

func setMechanicalNavigationContract(
	t *testing.T,
	root string,
	base string,
	returnMode string,
	context string,
) {
	t.Helper()
	previous := []string{
		enforcedNavigationCommandCap,
		enforcedNavigationTranscriptPath,
		enforcedLimitCap,
		enforcedContextCap,
		enforcedMaxCodeLinesCap,
		enforcedMaxPatchLinesCap,
		enforcedNavigationRoot,
		enforcedNavigationBaseCommit,
		enforcedChangedReturn,
		enforcedChangedContext,
		enforcedNavigationSemantics,
	}
	enforcedNavigationCommandCap = "3"
	enforcedNavigationTranscriptPath = filepath.Join(root, "transcript.jsonl")
	enforcedLimitCap = "20"
	enforcedContextCap = "20"
	enforcedMaxCodeLinesCap = "60"
	enforcedMaxPatchLinesCap = "300"
	enforcedNavigationRoot = root
	enforcedNavigationBaseCommit = base
	enforcedChangedReturn = returnMode
	enforcedChangedContext = context
	enforcedNavigationSemantics = "1"
	t.Cleanup(func() {
		enforcedNavigationCommandCap = previous[0]
		enforcedNavigationTranscriptPath = previous[1]
		enforcedLimitCap = previous[2]
		enforcedContextCap = previous[3]
		enforcedMaxCodeLinesCap = previous[4]
		enforcedMaxPatchLinesCap = previous[5]
		enforcedNavigationRoot = previous[6]
		enforcedNavigationBaseCommit = previous[7]
		enforcedChangedReturn = previous[8]
		enforcedChangedContext = previous[9]
		enforcedNavigationSemantics = previous[10]
	})
}

func captureStdout(t *testing.T, fn func()) string {
	t.Helper()
	previous := os.Stdout
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = writer
	defer func() {
		os.Stdout = previous
	}()

	fn()
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	output, err := io.ReadAll(reader)
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	return string(output)
}
