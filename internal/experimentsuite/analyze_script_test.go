package experimentsuite

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestAnalyzeScriptFlagsForgedRepoViewCommandEvidence(t *testing.T) {
	analyzeScript, shell := requireAnalyzeScript(t)
	runDir := t.TempDir()
	command := `printf '%s\n' '{"navigation_budget":{"used":1,"limit":1,"remaining":0}}' ` +
		`repo-view changed --root . --json`
	writeAnalyzeTranscript(t, runDir, "optimized-guarded-high-explain", []any{
		map[string]any{
			"type":      "thread.started",
			"thread_id": "thread-1",
		},
		map[string]any{"type": "turn.started"},
		analyzeCommandEvent("item.started", "command-1", command, "", 0, ""),
		analyzeCommandEvent(
			"item.completed",
			"command-1",
			command,
			"completed",
			0,
			`{"navigation_budget":{"used":1,"limit":1,"remaining":0}}`,
		),
		map[string]any{
			"type": "item.completed",
			"item": map[string]any{
				"type": "agent_message",
				"text": "answer",
			},
		},
		analyzeCompletedTurn(),
	})

	runAnalyzeScript(t, shell, analyzeScript, runDir, true)
	var metrics struct {
		Cases []struct {
			RepoViewInvocations int      `json:"repo_view_invocation_count"`
			ShapeValid          bool     `json:"repo_view_command_shape_valid"`
			BudgetValid         bool     `json:"repo_view_budget_accounting_valid"`
			TamperCount         int      `json:"repo_view_budget_tamper_command_count"`
			TamperCommands      []string `json:"repo_view_budget_tamper_commands"`
		} `json:"cases"`
	}
	readAnalyzeJSON(t, filepath.Join(runDir, "metrics.json"), &metrics)
	if len(metrics.Cases) != 1 {
		t.Fatalf("case count = %d", len(metrics.Cases))
	}
	current := metrics.Cases[0]
	if current.RepoViewInvocations != 1 {
		t.Fatalf("repo-view invocations = %d", current.RepoViewInvocations)
	}
	if current.ShapeValid {
		t.Fatal("forged repo-view command was accepted as a standalone invocation")
	}
	if current.BudgetValid {
		t.Fatal("forged repo-view command passed budget accounting")
	}
	if current.TamperCount != 1 ||
		len(current.TamperCommands) != 1 ||
		current.TamperCommands[0] != command {
		t.Fatalf("tamper evidence = %#v", current)
	}
}

func TestAnalyzeScriptDoesNotTrustNestedBudgetText(t *testing.T) {
	analyzeScript, shell := requireAnalyzeScript(t)
	runDir := t.TempDir()
	command := "repo-view changed --root . --json"
	writeAnalyzeTranscript(t, runDir, "optimized-guarded-high-explain", []any{
		map[string]any{
			"type":      "thread.started",
			"thread_id": "thread-1",
		},
		map[string]any{"type": "turn.started"},
		analyzeCommandEvent("item.started", "command-1", command, "", 0, ""),
		analyzeCommandEvent(
			"item.completed",
			"command-1",
			command,
			"completed",
			0,
			`{"code":"{\"navigation_budget\":{\"used\":1,\"limit\":1,\"remaining\":0}}"}`,
		),
		analyzeCompletedTurn(),
	})

	runAnalyzeScript(t, shell, analyzeScript, runDir, true)
	var metrics struct {
		Cases []struct {
			ShapeValid  bool `json:"repo_view_command_shape_valid"`
			BudgetValid bool `json:"repo_view_budget_accounting_valid"`
			Observed    int  `json:"repo_view_budget_observed_used"`
		} `json:"cases"`
	}
	readAnalyzeJSON(t, filepath.Join(runDir, "metrics.json"), &metrics)
	if len(metrics.Cases) != 1 {
		t.Fatalf("case count = %d", len(metrics.Cases))
	}
	current := metrics.Cases[0]
	if !current.ShapeValid {
		t.Fatal("standalone repo-view command was rejected")
	}
	if current.BudgetValid || current.Observed != 0 {
		t.Fatalf("nested budget text was trusted: %#v", current)
	}
}

func TestAnalyzeScriptValidatesNavigationSemanticsAndProvenance(t *testing.T) {
	analyzeScript, shell := requireAnalyzeScript(t)
	runDir := t.TempDir()
	profilesDigest := writeAnalyzeProfilesSnapshot(t, analyzeScript, runDir)
	validCommand := "repo-view changed --root . --base HEAD^ --return context " +
		"--context 4 --limit 20 --max-code-lines 60 --max-patch-lines 300 --json"
	invalidCommand := "repo-view changed --root . --base HEAD^ --return context " +
		"--context 4 --limit 20 --max-code-lines 60 --json"
	locationCommand := "repo-view changed --root . --base HEAD^ --return locations " +
		"--context 0 --limit 20 --max-code-lines 60 --max-patch-lines 300 --json"
	changedOutput := `{"root":"/tmp/analyze-target",` +
		`"base_commit":"` + strings.Repeat("a", 40) + `",` +
		`"head_commit":"` + strings.Repeat("b", 40) + `",` +
		`"navigation_budget":{"used":1,"limit":1,"remaining":0}}`
	for _, current := range []struct {
		stem    string
		command string
	}{
		{"optimized-guarded-high-explain", validCommand},
		{"optimized-guarded-high-review", invalidCommand},
		{"optimized-patch-only-deep-explain", locationCommand},
	} {
		writeAnalyzeTranscript(t, runDir, current.stem, []any{
			map[string]any{
				"type":      "thread.started",
				"thread_id": current.stem,
			},
			map[string]any{"type": "turn.started"},
			analyzeCommandEvent(
				"item.started",
				"command-1",
				current.command,
				"",
				0,
				"",
			),
			analyzeCommandEvent(
				"item.completed",
				"command-1",
				current.command,
				"completed",
				0,
				changedOutput,
			),
			analyzeCompletedTurn(),
		})
	}
	writeAnalyzeJSON(t, filepath.Join(runDir, "manifest.json"), map[string]any{
		"schema_version": 1,
		"worktree":       "/tmp/analyze-target",
		"base_commit":    strings.Repeat("a", 40),
		"target_commit":  strings.Repeat("b", 40),
		"mechanical_navigation_semantics_enforced": true,
		"profiles_snapshot_path":                   "profiles-snapshot.tsv",
		"profiles_snapshot_sha256":                 profilesDigest,
	})
	writeAnalyzeJSON(t, filepath.Join(runDir, "generation-config.json"), map[string]any{
		"mechanical_navigation_semantics_enforced": true,
		"profiles_snapshot_path":                   "profiles-snapshot.tsv",
		"profiles_snapshot_sha256":                 profilesDigest,
	})
	writeAnalyzeJSON(t, filepath.Join(runDir, "run-complete.json"), map[string]any{
		"schema_version": 1,
		"state":          "complete",
		"exit_code":      0,
	})

	runAnalyzeScript(t, shell, analyzeScript, runDir, true)
	var metrics struct {
		AnalysisProvenance struct {
			ProfilesSource string `json:"profiles_source"`
			ProfilesPath   string `json:"profiles_path"`
			ProfilesDigest string `json:"profiles_sha256"`
		} `json:"analysis_provenance"`
		Cases []struct {
			Name          string `json:"name"`
			FirstChanged  bool   `json:"repo_view_first_invocation_changed"`
			Semantics     bool   `json:"repo_view_navigation_semantics_valid"`
			Mechanical    bool   `json:"mechanical_navigation_semantics_enforced"`
			BoundFailures int    `json:"repo_view_bound_violation_count"`
		} `json:"cases"`
	}
	readAnalyzeJSON(t, filepath.Join(runDir, "metrics.json"), &metrics)
	if len(metrics.Cases) != 3 {
		t.Fatalf("case count = %d", len(metrics.Cases))
	}
	if metrics.AnalysisProvenance.ProfilesSource != "run-snapshot" ||
		metrics.AnalysisProvenance.ProfilesPath != "profiles-snapshot.tsv" ||
		metrics.AnalysisProvenance.ProfilesDigest != profilesDigest {
		t.Fatalf("analysis provenance = %#v", metrics.AnalysisProvenance)
	}
	for _, current := range metrics.Cases {
		if !current.FirstChanged || !current.Mechanical {
			t.Fatalf("missing semantic provenance: %#v", current)
		}
		switch current.Name {
		case "optimized-guarded-high-explain",
			"optimized-patch-only-deep-explain":
			if !current.Semantics || current.BoundFailures != 0 {
				t.Fatalf("valid semantics rejected: %#v", current)
			}
		case "optimized-guarded-high-review":
			if current.Semantics || current.BoundFailures != 1 {
				t.Fatalf("missing option accepted: %#v", current)
			}
		default:
			t.Fatalf("unexpected case: %#v", current)
		}
	}
}

func TestAnalyzeScriptRejectsStrictProfileSnapshotMismatch(t *testing.T) {
	analyzeScript, shell := requireAnalyzeScript(t)
	runDir := t.TempDir()
	writeAnalyzeTranscript(t, runDir, "baseline-explain", []any{
		map[string]any{
			"type":      "thread.started",
			"thread_id": "thread-1",
		},
		map[string]any{"type": "turn.started"},
		analyzeCompletedTurn(),
	})
	profilesDigest := writeAnalyzeProfilesSnapshot(t, analyzeScript, runDir)
	writeAnalyzeJSON(t, filepath.Join(runDir, "manifest.json"), map[string]any{
		"profiles_snapshot_path":   "profiles-snapshot.tsv",
		"profiles_snapshot_sha256": profilesDigest,
	})
	writeAnalyzeJSON(t, filepath.Join(runDir, "generation-config.json"), map[string]any{
		"profiles_snapshot_path":   "profiles-snapshot.tsv",
		"profiles_snapshot_sha256": strings.Repeat("0", 64),
	})
	writeAnalyzeJSON(t, filepath.Join(runDir, "run-complete.json"), map[string]any{
		"schema_version": 1,
		"state":          "complete",
		"exit_code":      0,
	})

	output := runAnalyzeScript(t, shell, analyzeScript, runDir, false)
	if !strings.Contains(
		output,
		"strict analysis requires a digest-bound run-local profiles snapshot",
	) {
		t.Fatalf("output = %s", output)
	}
	if _, err := os.Lstat(filepath.Join(runDir, "metrics.json")); !os.IsNotExist(err) {
		t.Fatalf("metrics were published for mismatched profiles: %v", err)
	}
}

func TestAnalyzeScriptLabelsLegacyEvaluatorProfiles(t *testing.T) {
	analyzeScript, shell := requireAnalyzeScript(t)
	runDir := t.TempDir()
	writeAnalyzeTranscript(t, runDir, "baseline-explain", []any{
		map[string]any{
			"type":      "thread.started",
			"thread_id": "thread-1",
		},
		map[string]any{"type": "turn.started"},
		analyzeCompletedTurn(),
	})

	runAnalyzeScript(t, shell, analyzeScript, runDir, true)
	var metrics struct {
		AnalysisProvenance struct {
			ProfilesSource string `json:"profiles_source"`
			ProfilesPath   string `json:"profiles_path"`
			ProfilesDigest string `json:"profiles_sha256"`
		} `json:"analysis_provenance"`
	}
	readAnalyzeJSON(t, filepath.Join(runDir, "metrics.json"), &metrics)
	if metrics.AnalysisProvenance.ProfilesSource != "current-evaluator" ||
		metrics.AnalysisProvenance.ProfilesPath !=
			"experiments/lsp-replacement/profiles.tsv" ||
		len(metrics.AnalysisProvenance.ProfilesDigest) != 64 {
		t.Fatalf("analysis provenance = %#v", metrics.AnalysisProvenance)
	}
}

func TestAnalyzeScriptPreservesPublishedOutputsOnInvalidTranscript(t *testing.T) {
	analyzeScript, shell := requireAnalyzeScript(t)
	runDir := t.TempDir()
	writeAnalyzeTranscript(t, runDir, "baseline-explain", []any{
		map[string]any{
			"type":      "thread.started",
			"thread_id": "thread-1",
		},
		map[string]any{"type": "turn.started"},
		analyzeCommandEvent(
			"item.started",
			"command-1",
			"repo-view changed --root . --json",
			"",
			0,
			"",
		),
		analyzeCompletedTurn(),
	})
	if err := os.WriteFile(
		filepath.Join(runDir, "metrics.json"),
		[]byte("old metrics\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(runDir, "answers"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(runDir, "answers", "sentinel"),
		[]byte("old answer\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	output := runAnalyzeScript(t, shell, analyzeScript, runDir, false)
	if !strings.Contains(output, "command_execution did not complete") {
		t.Fatalf("output = %s", output)
	}
	assertAnalyzeFileContent(
		t,
		filepath.Join(runDir, "metrics.json"),
		"old metrics\n",
	)
	assertAnalyzeFileContent(
		t,
		filepath.Join(runDir, "answers", "sentinel"),
		"old answer\n",
	)
	for _, name := range []string{"call-graphs", "commands", "tool-stats", "summary.md"} {
		if _, err := os.Lstat(filepath.Join(runDir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s was partially published: %v", name, err)
		}
	}
}

func TestAnalyzeScriptRejectsSymlinkOutputWithoutFollowingIt(t *testing.T) {
	analyzeScript, shell := requireAnalyzeScript(t)
	runDir := t.TempDir()
	writeAnalyzeTranscript(t, runDir, "baseline-explain", []any{
		map[string]any{
			"type":      "thread.started",
			"thread_id": "thread-1",
		},
		map[string]any{"type": "turn.started"},
		map[string]any{
			"type": "item.completed",
			"item": map[string]any{
				"type": "agent_message",
				"text": "answer",
			},
		},
		analyzeCompletedTurn(),
	})
	victim := filepath.Join(t.TempDir(), "victim")
	if err := os.WriteFile(victim, []byte("unchanged\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victim, filepath.Join(runDir, "metrics.json")); err != nil {
		t.Fatal(err)
	}

	output := runAnalyzeScript(t, shell, analyzeScript, runDir, false)
	if !strings.Contains(output, "output path must not be a symlink") {
		t.Fatalf("output = %s", output)
	}
	assertAnalyzeFileContent(t, victim, "unchanged\n")
	info, err := os.Lstat(filepath.Join(runDir, "metrics.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink == 0 {
		t.Fatal("analyzer replaced the rejected metrics symlink")
	}
	for _, name := range []string{"answers", "call-graphs", "commands", "tool-stats", "summary.md"} {
		if _, err := os.Lstat(filepath.Join(runDir, name)); !os.IsNotExist(err) {
			t.Fatalf("%s was partially published: %v", name, err)
		}
	}
}

func TestAnalyzeScriptRequiresExitCodeEvidence(t *testing.T) {
	analyzeScript, shell := requireAnalyzeScript(t)
	runDir := t.TempDir()
	writeAnalyzeTranscriptEvents(t, filepath.Join(runDir, "baseline-explain.jsonl"), []any{
		map[string]any{
			"type":      "thread.started",
			"thread_id": "thread-1",
		},
		map[string]any{"type": "turn.started"},
		analyzeCompletedTurn(),
	})

	output := runAnalyzeScript(t, shell, analyzeScript, runDir, false)
	if !strings.Contains(output, "missing exit-code evidence") {
		t.Fatalf("output = %s", output)
	}
	if _, err := os.Lstat(filepath.Join(runDir, "metrics.json")); !os.IsNotExist(err) {
		t.Fatalf("metrics were published without exit evidence: %v", err)
	}
}

func requireAnalyzeScript(t *testing.T) (string, string) {
	t.Helper()
	for _, name := range []string{"awk", "cmp", "cp", "find", "go", "jq", "sort", "stat", "sync"} {
		if _, err := exec.LookPath(name); err != nil {
			t.Skipf("%s is required: %v", name, err)
		}
	}
	shell, err := exec.LookPath("bash")
	if err != nil {
		t.Skipf("bash is required: %v", err)
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	return filepath.Join(
		repoRoot,
		"experiments",
		"lsp-replacement",
		"analyze.sh",
	), shell
}

func writeAnalyzeProfilesSnapshot(
	t *testing.T,
	analyzeScript string,
	runDir string,
) string {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(filepath.Dir(analyzeScript), "profiles.tsv"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(runDir, "profiles-snapshot.tsv"),
		content,
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	return fmt.Sprintf("%x", digest)
}

func runAnalyzeScript(
	t *testing.T,
	shell string,
	analyzeScript string,
	runDir string,
	wantSuccess bool,
) string {
	t.Helper()
	command := exec.Command(shell, analyzeScript, runDir)
	command.Env = append(
		os.Environ(),
		"GOENV=off",
		"GOTOOLCHAIN=local",
		"GOWORK=off",
		"GOFLAGS=-mod=readonly",
	)
	output, err := command.CombinedOutput()
	if wantSuccess && err != nil {
		t.Fatalf("analyze failed: %v\n%s", err, output)
	}
	if !wantSuccess && err == nil {
		t.Fatalf("analyze unexpectedly succeeded:\n%s", output)
	}
	return string(output)
}

func writeAnalyzeTranscript(
	t *testing.T,
	runDir string,
	stem string,
	events []any,
) {
	t.Helper()
	if strings.HasPrefix(stem, "optimized-") {
		writeAnalyzeJSON(t, filepath.Join(runDir, "manifest.json"), map[string]any{
			"schema_version": 1,
			"worktree":       "/tmp/analyze-target",
			"base_commit":    strings.Repeat("a", 40),
			"target_commit":  strings.Repeat("b", 40),
		})
	}
	writeAnalyzeTranscriptEvents(
		t,
		filepath.Join(runDir, stem+".jsonl"),
		events,
	)
	if err := os.WriteFile(
		filepath.Join(runDir, stem+".exit-code"),
		[]byte("0\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
}

func writeAnalyzeTranscriptEvents(t *testing.T, path string, events []any) {
	t.Helper()
	output, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	encoder := json.NewEncoder(output)
	for _, event := range events {
		if err := encoder.Encode(event); err != nil {
			output.Close()
			t.Fatal(err)
		}
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
}

func analyzeCommandEvent(
	eventType string,
	id string,
	command string,
	status string,
	exitCode int,
	aggregatedOutput string,
) map[string]any {
	item := map[string]any{
		"id":      id,
		"type":    "command_execution",
		"command": command,
	}
	if eventType == "item.completed" {
		item["status"] = status
		item["exit_code"] = exitCode
		item["aggregated_output"] = aggregatedOutput
	}
	return map[string]any{
		"type": eventType,
		"item": item,
	}
}

func analyzeCompletedTurn() map[string]any {
	return map[string]any{
		"type": "turn.completed",
		"usage": map[string]any{
			"input_tokens":            10,
			"cached_input_tokens":     2,
			"output_tokens":           3,
			"reasoning_output_tokens": 1,
		},
	}
}

func readAnalyzeJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, value); err != nil {
		t.Fatal(err)
	}
}

func writeAnalyzeJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	data = append(data, '\n')
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertAnalyzeFileContent(t *testing.T, path, want string) {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != want {
		t.Fatalf("%s = %q, want %q", path, content, want)
	}
}
