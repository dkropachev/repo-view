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

func analyzeSimpleInspectOutput(
	t *testing.T,
	used int,
	expectedLocations int,
	resultsTruncated bool,
	codeTruncated bool,
) string {
	t.Helper()
	entries := make([]any, 0, expectedLocations)
	for index := range expectedLocations {
		entries = append(entries, map[string]any{
			"navigation_budget": map[string]any{
				"used":      used,
				"limit":     3,
				"remaining": 3 - used,
			},
			"location":          fmt.Sprintf("fixture.go:%d", index+1),
			"results_truncated": resultsTruncated && index == expectedLocations-1,
			"results": []any{map[string]any{
				"path":           "fixture.go",
				"code_truncated": codeTruncated && index == expectedLocations-1,
			}},
		})
	}
	encoded, err := json.Marshal(entries)
	if err != nil {
		t.Fatal(err)
	}
	return string(encoded)
}

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
	for _, current := range []struct {
		stem    string
		command string
	}{
		{"optimized-guarded-high-explain", validCommand},
		{"optimized-guarded-high-review", invalidCommand},
		{"optimized-patch-only-deep-explain", locationCommand},
	} {
		budgetLimit := 1
		if strings.HasPrefix(current.stem, "optimized-guarded-high-") {
			budgetLimit = 3
		}
		changedOutput := `{"root":"/tmp/analyze-target",` +
			`"base_commit":"` + strings.Repeat("a", 40) + `",` +
			`"head_commit":"` + strings.Repeat("b", 40) + `",` +
			fmt.Sprintf(
				`"navigation_budget":{"used":1,"limit":%d,"remaining":%d}}`,
				budgetLimit,
				budgetLimit-1,
			)
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
		"go_mod_cache":   "/tmp/go/pkg/mod",
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
			Name              string `json:"name"`
			FirstChanged      bool   `json:"repo_view_first_invocation_changed"`
			Semantics         bool   `json:"repo_view_navigation_semantics_valid"`
			Mechanical        bool   `json:"mechanical_navigation_semantics_enforced"`
			BoundFailures     int    `json:"repo_view_bound_violation_count"`
			BudgetValid       bool   `json:"repo_view_budget_accounting_valid"`
			BudgetCap         int    `json:"repo_view_invocation_cap"`
			SimpleChanged     bool   `json:"repo_view_simple_changed_command_exact"`
			SimpleCore        bool   `json:"repo_view_simple_core_inspect_command_exact"`
			SimpleConsumer    bool   `json:"repo_view_simple_consumer_inspect_command_exact"`
			SimpleUntruncated bool   `json:"repo_view_simple_inspect_outputs_untruncated"`
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
		if !current.FirstChanged || !current.Mechanical || !current.BudgetValid {
			t.Fatalf("missing semantic provenance: %#v", current)
		}
		switch current.Name {
		case "optimized-guarded-high-explain":
			if !current.Semantics || current.BoundFailures != 0 || current.BudgetCap != 3 {
				t.Fatalf("valid guarded semantics rejected: %#v", current)
			}
		case "optimized-patch-only-deep-explain":
			if !current.Semantics || current.BoundFailures != 0 || current.BudgetCap != 1 {
				t.Fatalf("valid semantics rejected: %#v", current)
			}
			if !current.SimpleChanged || !current.SimpleCore ||
				!current.SimpleConsumer || !current.SimpleUntruncated {
				t.Fatalf("simple-only checks changed deep behavior: %#v", current)
			}
		case "optimized-guarded-high-review":
			if current.Semantics || current.BoundFailures != 1 || current.BudgetCap != 3 {
				t.Fatalf("missing option accepted: %#v", current)
			}
		default:
			t.Fatalf("unexpected case: %#v", current)
		}
	}
}

func TestAnalyzeScriptEnforcesGuardedHighThreeCommandBudget(t *testing.T) {
	analyzeScript, shell := requireAnalyzeScript(t)
	runDir := t.TempDir()
	profilesDigest := writeAnalyzeProfilesSnapshot(t, analyzeScript, runDir)
	commands := []string{
		"repo-view changed --root . --base " + strings.Repeat("a", 40) +
			" --return context " +
			"--context 4 --limit 20 --max-code-lines 60 --max-patch-lines 300 --json",
		"repo-view inspect common/quotas/rate_limiter.go:12 " +
			"common/quotas/reservation.go:11 common/quotas/rate_limiter_impl.go:13 " +
			"common/quotas/rate_limiter_impl.go:26 common/quotas/rate_limiter_impl.go:80 " +
			"common/quotas/rate_limiter_impl.go:114 " +
			"common/quotas/dynamic_rate_limiter_impl.go:28 " +
			"common/quotas/dynamic_rate_limiter_impl.go:57 " +
			"common/quotas/clocked_rate_limiter.go:54 " +
			"common/quotas/clocked_rate_limiter.go:62 " +
			"common/quotas/clocked_rate_limiter.go:70 common/clock/time_source.go:38 " +
			"--root . --include scope --return scope --context 4 --limit 20 " +
			"--max-code-lines 60 --max-patch-lines 300 --json",
		"repo-view inspect service/worker/scheduler/fx.go:120 " +
			"service/worker/scheduler/activities.go:95 " +
			"service/worker/pernamespaceworker.go:123 service/worker/pernamespaceworker.go:430 " +
			"common/quotas/request_rate_limiter_adapter_impl.go:16 " +
			"common/quotas/request_rate_limiter_adapter_impl.go:31 " +
			"service/frontend/configs/quotas.go:346 " +
			"common/persistence/client/health_request_rate_limiter.go:56 " +
			"common/persistence/client/health_request_rate_limiter.go:82 " +
			"service/matching/ratelimit_manager.go:81 " +
			"service/history/replication/stream_sender_flow_controller.go:59 " +
			"--root . --include scope --return context --context 4 --limit 20 " +
			"--max-code-lines 60 --max-patch-lines 300 --json",
		"repo-view outline common/quotas/rate_limiter_impl.go --root . --return scope " +
			"--context 4 --limit 20 --max-code-lines 60 --max-patch-lines 300 --json",
	}
	for _, current := range []struct {
		stem         string
		commandCount int
	}{
		{stem: "optimized-guarded-high-explain", commandCount: 3},
		{stem: "optimized-guarded-high-review", commandCount: 4},
	} {
		events := []any{
			map[string]any{
				"type":      "thread.started",
				"thread_id": current.stem,
			},
			map[string]any{"type": "turn.started"},
		}
		for index, command := range commands[:current.commandCount] {
			used := index + 1
			if used > 3 {
				// A mechanically enforced run cannot produce a valid fourth budget
				// record. Repeating the exhausted record models forged evidence.
				used = 3
			}
			output := fmt.Sprintf(
				`{"navigation_budget":{"used":%d,"limit":3,"remaining":%d}}`,
				used,
				3-used,
			)
			switch index {
			case 0:
				output = `{"root":"/tmp/analyze-target",` +
					`"base_commit":"` + strings.Repeat("a", 40) + `",` +
					`"head_commit":"` + strings.Repeat("b", 40) + `",` +
					fmt.Sprintf(
						`"navigation_budget":{"used":%d,"limit":3,"remaining":%d}}`,
						used,
						3-used,
					)
			case 1:
				output = analyzeSimpleInspectOutput(t, used, 12, false, false)
			case 2:
				output = analyzeSimpleInspectOutput(t, used, 11, false, false)
			}
			id := fmt.Sprintf("command-%d", index+1)
			events = append(
				events,
				analyzeCommandEvent("item.started", id, command, "", 0, ""),
				analyzeCommandEvent("item.completed", id, command, "completed", 0, output),
			)
		}
		events = append(events, analyzeCompletedTurn())
		writeAnalyzeTranscript(t, runDir, current.stem, events)
	}
	writeAnalyzeJSON(t, filepath.Join(runDir, "manifest.json"), map[string]any{
		"schema_version": 1,
		"worktree":       "/tmp/analyze-target",
		"go_mod_cache":   "/tmp/go/pkg/mod",
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
		Cases []struct {
			Name        string `json:"name"`
			Invocations int    `json:"repo_view_invocation_count"`
			Operations  []struct {
				Name        string `json:"name"`
				Invocations int    `json:"invocations"`
			} `json:"operations"`
			Semantics     bool `json:"repo_view_navigation_semantics_valid"`
			BudgetCap     int  `json:"repo_view_invocation_cap"`
			CapExceeded   bool `json:"repo_view_invocation_cap_exceeded"`
			ObservedUsed  int  `json:"repo_view_budget_observed_used"`
			BudgetValid   bool `json:"repo_view_budget_accounting_valid"`
			ShapeValid    bool `json:"repo_view_command_shape_valid"`
			BoundFailures int  `json:"repo_view_bound_violation_count"`
			ChangedExact  bool `json:"repo_view_simple_changed_command_exact"`
			CoreExact     bool `json:"repo_view_simple_core_inspect_command_exact"`
			ConsumerExact bool `json:"repo_view_simple_consumer_inspect_command_exact"`
			Untruncated   bool `json:"repo_view_simple_inspect_outputs_untruncated"`
		} `json:"cases"`
	}
	readAnalyzeJSON(t, filepath.Join(runDir, "metrics.json"), &metrics)
	if len(metrics.Cases) != 2 {
		t.Fatalf("case count = %d", len(metrics.Cases))
	}
	for _, current := range metrics.Cases {
		if !current.Semantics || !current.ShapeValid || current.BudgetCap != 3 ||
			current.ObservedUsed != 3 || current.BoundFailures != 0 {
			t.Fatalf("invalid guarded-high contract metrics: %#v", current)
		}
		switch current.Name {
		case "optimized-guarded-high-explain":
			if current.Invocations != 3 || current.CapExceeded || !current.BudgetValid {
				t.Fatalf("three-call budget rejected: %#v", current)
			}
			if !current.ChangedExact || !current.CoreExact ||
				!current.ConsumerExact || !current.Untruncated {
				t.Fatalf("exact simple contract rejected: %#v", current)
			}
			operationCounts := make(map[string]int, len(current.Operations))
			for _, operation := range current.Operations {
				operationCounts[operation.Name] = operation.Invocations
			}
			if len(operationCounts) != 2 ||
				operationCounts["repo-view.changed"] != 1 ||
				operationCounts["repo-view.inspect"] != 2 ||
				operationCounts["repo-view.find"] != 0 {
				t.Fatalf("three-call operation mix = %#v", current.Operations)
			}
		case "optimized-guarded-high-review":
			if current.Invocations != 4 || !current.CapExceeded || current.BudgetValid {
				t.Fatalf("fourth call was not rejected: %#v", current)
			}
			if current.ChangedExact || current.CoreExact || current.ConsumerExact ||
				!current.Untruncated {
				t.Fatalf("four-call exact contract metrics = %#v", current)
			}
		default:
			t.Fatalf("unexpected case: %#v", current)
		}
	}
}

func TestAnalyzeScriptRejectsSimpleCommandDriftAndTruncatedInspects(t *testing.T) {
	analyzeScript, shell := requireAnalyzeScript(t)
	baseCommit := strings.Repeat("a", 40)
	targetCommit := strings.Repeat("b", 40)
	changedCommand := "repo-view changed --root . --base " + baseCommit +
		" --return context --context 4 --limit 20 --max-code-lines 60 " +
		"--max-patch-lines 300 --json"
	consumerCommand := simpleConsumerInspectCommand()
	for _, required := range []string{
		"service/matching/ratelimit_manager.go:81",
		"service/history/replication/stream_sender_flow_controller.go:59",
	} {
		if !strings.Contains(consumerCommand, required) {
			t.Fatalf("simple consumer command is missing %q", required)
		}
	}

	for _, testCase := range []struct {
		name                     string
		consumerCommand          string
		wrapperStyle             string
		coreCodeTruncated        bool
		consumerResultsTruncated bool
		wantConsumerExact        bool
		wantUntruncated          bool
	}{
		{
			name:              "double quoted absolute shell wrapper",
			consumerCommand:   consumerCommand,
			wrapperStyle:      "double",
			wantConsumerExact: true,
			wantUntruncated:   true,
		},
		{
			name:              "bare shell wrapper",
			consumerCommand:   consumerCommand,
			wrapperStyle:      "bare",
			wantConsumerExact: true,
			wantUntruncated:   true,
		},
		{
			name:              "direct command",
			consumerCommand:   consumerCommand,
			wrapperStyle:      "direct",
			wantConsumerExact: true,
			wantUntruncated:   true,
		},
		{
			name:              "direct binary command",
			consumerCommand:   consumerCommand,
			wrapperStyle:      "direct-binary",
			wantConsumerExact: true,
			wantUntruncated:   true,
		},
		{
			name: "consumer command drift",
			consumerCommand: strings.Replace(
				consumerCommand,
				"service/matching/ratelimit_manager.go:81",
				"service/matching/ratelimit_manager.go:82",
				1,
			),
			wantConsumerExact: false,
			wantUntruncated:   true,
		},
		{
			name:              "core code truncated",
			consumerCommand:   consumerCommand,
			coreCodeTruncated: true,
			wantConsumerExact: true,
			wantUntruncated:   false,
		},
		{
			name:                     "consumer results truncated",
			consumerCommand:          consumerCommand,
			consumerResultsTruncated: true,
			wantConsumerExact:        true,
			wantUntruncated:          false,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			runDir := t.TempDir()
			profilesDigest := writeAnalyzeProfilesSnapshot(t, analyzeScript, runDir)
			commands := []string{
				changedCommand,
				simpleCoreInspectCommand(),
				testCase.consumerCommand,
			}
			events := []any{
				map[string]any{
					"type":      "thread.started",
					"thread_id": testCase.name,
				},
				map[string]any{"type": "turn.started"},
			}
			for index, command := range commands {
				used := index + 1
				var wrapped string
				switch testCase.wrapperStyle {
				case "double":
					wrapped = `/usr/bin/bash -lc "` + command + `"`
				case "bare":
					wrapped = "sh -lc '" + command + "'"
				case "direct":
					wrapped = command
				case "direct-binary":
					wrapped = strings.Replace(
						command,
						"repo-view ",
						"repo-view.bin ",
						1,
					)
				default:
					wrapped = "/usr/bin/zsh -lc '" + command + "'"
				}
				var output string
				switch index {
				case 0:
					output = `{"root":"/tmp/analyze-target",` +
						`"base_commit":"` + baseCommit + `",` +
						`"head_commit":"` + targetCommit + `",` +
						`"navigation_budget":{"used":1,"limit":3,"remaining":2}}`
				case 1:
					output = analyzeSimpleInspectOutput(
						t, used, 12, false, testCase.coreCodeTruncated,
					)
				case 2:
					output = analyzeSimpleInspectOutput(
						t,
						used,
						11,
						testCase.consumerResultsTruncated,
						false,
					)
				}
				id := fmt.Sprintf("command-%d", used)
				events = append(
					events,
					analyzeCommandEvent("item.started", id, wrapped, "", 0, ""),
					analyzeCommandEvent(
						"item.completed", id, wrapped, "completed", 0, output,
					),
				)
			}
			events = append(events, analyzeCompletedTurn())
			writeAnalyzeTranscript(
				t, runDir, "optimized-guarded-high-explain", events,
			)
			writeAnalyzeJSON(t, filepath.Join(runDir, "manifest.json"), map[string]any{
				"schema_version": 1,
				"worktree":       "/tmp/analyze-target",
				"go_mod_cache":   "/tmp/go/pkg/mod",
				"base_commit":    baseCommit,
				"target_commit":  targetCommit,
				"mechanical_navigation_semantics_enforced": true,
				"profiles_snapshot_path":                   "profiles-snapshot.tsv",
				"profiles_snapshot_sha256":                 profilesDigest,
			})
			writeAnalyzeJSON(
				t,
				filepath.Join(runDir, "generation-config.json"),
				map[string]any{
					"mechanical_navigation_semantics_enforced": true,
					"profiles_snapshot_path":                   "profiles-snapshot.tsv",
					"profiles_snapshot_sha256":                 profilesDigest,
				},
			)
			writeAnalyzeJSON(t, filepath.Join(runDir, "run-complete.json"), map[string]any{
				"schema_version": 1,
				"state":          "complete",
				"exit_code":      0,
			})
			runAnalyzeScript(t, shell, analyzeScript, runDir, true)
			var metrics struct {
				Cases []struct {
					NavigationValid bool `json:"repo_view_navigation_semantics_valid"`
					ChangedExact    bool `json:"repo_view_simple_changed_command_exact"`
					CoreExact       bool `json:"repo_view_simple_core_inspect_command_exact"`
					ConsumerExact   bool `json:"repo_view_simple_consumer_inspect_command_exact"`
					Untruncated     bool `json:"repo_view_simple_inspect_outputs_untruncated"`
				} `json:"cases"`
			}
			readAnalyzeJSON(t, filepath.Join(runDir, "metrics.json"), &metrics)
			if len(metrics.Cases) != 1 {
				t.Fatalf("case count = %d", len(metrics.Cases))
			}
			current := metrics.Cases[0]
			if !current.NavigationValid || !current.ChangedExact || !current.CoreExact ||
				current.ConsumerExact != testCase.wantConsumerExact ||
				current.Untruncated != testCase.wantUntruncated {
				t.Fatalf("simple exact/truncation metrics = %#v", current)
			}
		})
	}
}

func TestAnalyzeScriptEnforcesVerifiedDeepCommandContract(t *testing.T) {
	analyzeScript, shell := requireAnalyzeScript(t)
	baseCommit := strings.Repeat("a", 40)
	targetCommit := strings.Repeat("b", 40)
	worktree := "/tmp/analyze-target"

	for _, testCase := range []struct {
		name                string
		driftRepoCommand    bool
		driftAWKCommand     bool
		wrapAWKCommand      bool
		interleaveExtraTool bool
		omitSnapshot        bool
		wantSequenceExact   bool
		wantDependencyExact bool
		wantNavigationValid bool
		wantToolCalls       int
	}{
		{
			name:                "exact contract",
			wantSequenceExact:   true,
			wantDependencyExact: true,
			wantNavigationValid: true,
			wantToolCalls:       9,
		},
		{
			name:                "shell-wrapped exact contract",
			wrapAWKCommand:      true,
			wantSequenceExact:   true,
			wantDependencyExact: true,
			wantNavigationValid: true,
			wantToolCalls:       9,
		},
		{
			name:                "repo command drift",
			driftRepoCommand:    true,
			wantDependencyExact: true,
			wantToolCalls:       9,
		},
		{
			name:              "awk command drift",
			driftAWKCommand:   true,
			wantSequenceExact: true,
			wantToolCalls:     9,
		},
		{
			name:                "extra interleaved tool",
			interleaveExtraTool: true,
			wantSequenceExact:   true,
			wantToolCalls:       10,
		},
		{
			name:              "missing authenticated snapshot",
			omitSnapshot:      true,
			wantSequenceExact: true,
			wantToolCalls:     9,
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			runDir := t.TempDir()
			profilesDigest := writeAnalyzeProfilesSnapshot(t, analyzeScript, runDir)
			repoCommands := []string{
				"repo-view changed --root . --base " + baseCommit +
					" --return context --context 4 --limit 20 --max-code-lines 60 " +
					"--max-patch-lines 300 --json",
				"repo-view find ClockedReservation --root . --include refs " +
					"--return locations --context 6 --limit 20 --max-code-lines 60 " +
					"--max-patch-lines 300 --json",
				"repo-view inspect common/quotas/reservation.go:11 " +
					"common/quotas/rate_limiter.go:12 common/clock/time_source.go:35 " +
					"common/quotas/rate_limiter_impl.go:22 " +
					"common/quotas/rate_limiter_impl.go:49 " +
					"common/quotas/clocked_rate_limiter.go:45 " +
					"common/quotas/clocked_rate_limiter.go:50 " +
					"common/quotas/clocked_rate_limiter.go:54 " +
					"common/quotas/clocked_rate_limiter.go:58 " +
					"common/quotas/clocked_rate_limiter.go:62 " +
					"common/quotas/clocked_rate_limiter.go:66 " +
					"common/quotas/clocked_rate_limiter.go:70 " +
					"common/quotas/clocked_rate_limiter.go:74 " +
					"common/quotas/multi_reservation_impl.go:30 " +
					"common/quotas/multi_reservation_impl.go:42 " +
					"common/quotas/multi_reservation_impl.go:54 " +
					"common/quotas/multi_reservation_impl.go:61 go.mod:76 --root . " +
					"--include scope --return context --context 6 --limit 20 " +
					"--max-code-lines 60 --max-patch-lines 300 --json",
				"repo-view find startWorkflowRateLimiter NewDefaultOutgoingRateLimiter " +
					"newShardReaderRateLimiter ReaderImpl loadAndSubmitTasks " +
					"MultiRequestRateLimiterImpl --root . --include both " +
					"--return locations --context 8 --limit 20 --max-code-lines 60 " +
					"--max-patch-lines 300 --json",
				"repo-view outline service/worker/scheduler/fx.go " +
					"service/worker/scheduler/activities.go " +
					"service/worker/pernamespaceworker.go " +
					"common/quotas/dynamic_rate_limiter_impl.go " +
					"common/quotas/rate_limiter_impl.go " +
					"service/history/queues/reader_quotas.go " +
					"service/history/queues/queue_base.go " +
					"service/history/queues/reader.go " +
					"common/quotas/multi_request_rate_limiter_impl.go " +
					"common/quotas/priority_rate_limiter_impl.go " +
					"common/quotas/request_rate_limiter_adapter_impl.go --root . " +
					"--return locations --context 8 --limit 20 --max-code-lines 60 " +
					"--max-patch-lines 300 --json",
				"repo-view inspect service/worker/scheduler/fx.go:120 " +
					"service/worker/scheduler/fx.go:133 " +
					"service/worker/scheduler/activities.go:89 " +
					"service/worker/pernamespaceworker.go:123 " +
					"service/worker/pernamespaceworker.go:430 " +
					"common/quotas/dynamic_rate_limiter_impl.go:99 " +
					"common/quotas/rate_limiter_impl.go:54 --root . --include scope " +
					"--return context --context 8 --limit 20 --max-code-lines 60 " +
					"--max-patch-lines 300 --json",
				"repo-view inspect service/history/queues/reader_quotas.go:14 " +
					"service/history/queues/reader_quotas.go:39 " +
					"service/history/queues/queue_base.go:136 " +
					"service/history/queues/queue_base.go:150 " +
					"service/history/queues/reader.go:58 " +
					"service/history/queues/reader.go:426 " +
					"common/quotas/multi_request_rate_limiter_impl.go:17 " +
					"common/quotas/multi_request_rate_limiter_impl.go:56 " +
					"common/quotas/multi_request_rate_limiter_impl.go:70 " +
					"common/quotas/priority_rate_limiter_impl.go:77 " +
					"common/quotas/request_rate_limiter_adapter_impl.go:31 " +
					"common/quotas/request_rate_limiter_adapter_impl.go:35 " +
					"common/quotas/dynamic_rate_limiter_impl.go:99 " +
					"common/quotas/rate_limiter_impl.go:54 --root . --include scope " +
					"--return context --context 8 --limit 20 --max-code-lines 60 " +
					"--max-patch-lines 300 --json",
				"repo-view inspect common/quotas/clocked_rate_limiter_test.go:77 " +
					"common/quotas/clocked_rate_limiter_test.go:91 " +
					"common/quotas/clocked_rate_limiter_test.go:108 " +
					"common/quotas/clocked_rate_limiter_test.go:118 " +
					"common/quotas/clocked_rate_limiter_test.go:133 " +
					"common/quotas/clocked_rate_limiter_test.go:160 " +
					"common/quotas/priority_reservation_impl_test.go:64 " +
					"common/quotas/priority_reservation_impl_test.go:73 " +
					"common/quotas/multi_reservation_impl_test.go:55 " +
					"common/quotas/multi_reservation_impl_test.go:62 " +
					"common/quotas/multi_reservation_impl_test.go:77 " +
					"common/quotas/multi_reservation_impl_test.go:86 " +
					"common/quotas/multi_rate_limiter_impl_test.go:68 " +
					"common/quotas/multi_rate_limiter_impl_test.go:85 " +
					"common/quotas/multi_rate_limiter_impl_test.go:133 " +
					"common/quotas/rate_limiter_impl_test.go:23 " +
					"common/quotas/bench_test.go:38 --root . --include scope " +
					"--return context --context 4 --limit 20 --max-code-lines 40 " +
					"--max-patch-lines 300 --json",
			}
			if testCase.driftRepoCommand {
				repoCommands[1] = strings.Replace(
					repoCommands[1],
					"ClockedReservation",
					"ClockedReservationImpl",
					1,
				)
			}
			awkCommand := `awk -v OFS=: "((FILENAME == ARGV[1]) && FNR >= 120 && FNR <= 230) || ((FILENAME == ARGV[2]) && FNR >= 343 && FNR <= 420) { print FILENAME, FNR; print }" "$HOME/dependencies/golang.org/x/time@v0.14.0/rate/rate.go" "$HOME/dependencies/golang.org/x/time@v0.14.0/rate/rate_test.go"`
			if testCase.driftAWKCommand {
				awkCommand = strings.Replace(awkCommand, "FNR <= 230", "FNR <= 231", 1)
			}
			if testCase.wrapAWKCommand {
				awkCommand = "/usr/bin/zsh -lc '" + awkCommand + "'"
			}

			events := []any{
				map[string]any{
					"type":      "thread.started",
					"thread_id": testCase.name,
				},
				map[string]any{"type": "turn.started"},
			}
			toolIndex := 0
			appendCommand := func(command string, output string) {
				toolIndex++
				id := fmt.Sprintf("command-%d", toolIndex)
				events = append(
					events,
					analyzeCommandEvent("item.started", id, command, "", 0, ""),
					analyzeCommandEvent(
						"item.completed", id, command, "completed", 0, output,
					),
				)
			}
			for index, command := range repoCommands {
				if testCase.interleaveExtraTool && index == 4 {
					appendCommand("pwd", worktree+"\n")
				}
				used := index + 1
				output := fmt.Sprintf(
					`{"navigation_budget":{"used":%d,"limit":34,"remaining":%d}}`,
					used,
					34-used,
				)
				if index == 0 {
					output = `{"root":"` + worktree + `",` +
						`"base_commit":"` + baseCommit + `",` +
						`"head_commit":"` + targetCommit + `",` +
						`"navigation_budget":{"used":1,"limit":34,"remaining":33}}`
				}
				appendCommand(command, output)
			}
			appendCommand(awkCommand, "dependency source\n")
			events = append(events, analyzeCompletedTurn())

			writeAnalyzeTranscript(
				t,
				runDir,
				"optimized-investigative-verified-high-deep-explain",
				events,
			)
			dependencySource := any(nil)
			if !testCase.omitSnapshot {
				dependencySource = writeAnalyzeDependencySnapshot(t, runDir)
			}
			writeAnalyzeJSON(t, filepath.Join(runDir, "manifest.json"), map[string]any{
				"schema_version":    1,
				"worktree":          worktree,
				"base_commit":       baseCommit,
				"target_commit":     targetCommit,
				"dependency_source": dependencySource,
				"mechanical_navigation_semantics_enforced": true,
				"profiles_snapshot_path":                   "profiles-snapshot.tsv",
				"profiles_snapshot_sha256":                 profilesDigest,
			})
			writeAnalyzeJSON(
				t,
				filepath.Join(runDir, "generation-config.json"),
				map[string]any{
					"mechanical_navigation_semantics_enforced": true,
					"profiles_snapshot_path":                   "profiles-snapshot.tsv",
					"profiles_snapshot_sha256":                 profilesDigest,
				},
			)
			writeAnalyzeJSON(t, filepath.Join(runDir, "run-complete.json"), map[string]any{
				"schema_version": 1,
				"state":          "complete",
				"exit_code":      0,
			})
			runAnalyzeScript(t, shell, analyzeScript, runDir, true)
			var metrics struct {
				Cases []struct {
					SequenceExact   bool `json:"repo_view_deep_command_sequence_exact"`
					DependencyExact bool `json:"repo_view_deep_dependency_awk_exact"`
					NavigationValid bool `json:"repo_view_navigation_semantics_valid"`
					Mechanical      bool `json:"mechanical_navigation_semantics_enforced"`
					ShapeValid      bool `json:"repo_view_command_shape_valid"`
					BudgetValid     bool `json:"repo_view_budget_accounting_valid"`
					BudgetObserved  int  `json:"repo_view_budget_observed_used"`
					BudgetCap       int  `json:"repo_view_invocation_cap"`
					RepoViewCalls   int  `json:"repo_view_invocation_count"`
					ToolCalls       int  `json:"tool_call_count"`
				} `json:"cases"`
			}
			readAnalyzeJSON(t, filepath.Join(runDir, "metrics.json"), &metrics)
			if len(metrics.Cases) != 1 {
				t.Fatalf("case count = %d", len(metrics.Cases))
			}
			current := metrics.Cases[0]
			if current.SequenceExact != testCase.wantSequenceExact ||
				current.DependencyExact != testCase.wantDependencyExact ||
				current.NavigationValid != testCase.wantNavigationValid {
				t.Fatalf("deep contract metrics = %#v", current)
			}
			if !current.Mechanical || !current.ShapeValid || !current.BudgetValid ||
				current.BudgetObserved != 8 || current.BudgetCap != 34 ||
				current.RepoViewCalls != 8 || current.ToolCalls != testCase.wantToolCalls {
				t.Fatalf("deep navigation evidence = %#v", current)
			}
		})
	}
}

func TestSimpleSuiteManifestsRequireExactCommandsAndUntruncatedInspects(t *testing.T) {
	expectedFields := map[string]bool{
		"repo_view_simple_changed_command_exact":          true,
		"repo_view_simple_core_inspect_command_exact":     true,
		"repo_view_simple_consumer_inspect_command_exact": true,
		"repo_view_simple_inspect_outputs_untruncated":    true,
	}
	for _, relative := range []string{
		filepath.Join("..", "..", "experiments", "lsp-replacement", "suite", "cases.json"),
		filepath.Join("..", "..", "experiments", "lsp-replacement", "suite", "resolutions.json"),
	} {
		content, err := os.ReadFile(relative)
		if err != nil {
			t.Fatal(err)
		}
		var document struct {
			Cases []struct {
				ID         string `json:"id"`
				Assertions []struct {
					Field    string `json:"field"`
					Operator string `json:"operator"`
					Value    any    `json:"value"`
				} `json:"assertions"`
			} `json:"cases"`
		}
		if err := json.Unmarshal(content, &document); err != nil {
			t.Fatal(err)
		}
		for _, caseID := range []string{
			"01-simple-explain-accepted",
			"02-simple-review-accepted",
		} {
			var assertions []struct {
				Field    string `json:"field"`
				Operator string `json:"operator"`
				Value    any    `json:"value"`
			}
			for _, current := range document.Cases {
				if current.ID == caseID {
					assertions = current.Assertions
					break
				}
			}
			if assertions == nil {
				t.Fatalf("%s has no %s", relative, caseID)
			}
			found := make(map[string]bool)
			for _, assertion := range assertions {
				if !expectedFields[assertion.Field] {
					continue
				}
				value, ok := assertion.Value.(bool)
				if assertion.Operator != "eq" || !ok || !value {
					t.Errorf(
						"%s %s assertion %s = %q/%#v",
						relative,
						caseID,
						assertion.Field,
						assertion.Operator,
						assertion.Value,
					)
				}
				found[assertion.Field] = true
			}
			for field := range expectedFields {
				if !found[field] {
					t.Errorf("%s %s is missing %s", relative, caseID, field)
				}
			}
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

func TestAnalyzeScriptReportsRawAndCacheComparisonDiagnostics(t *testing.T) {
	analyzeScript, shell := requireAnalyzeScript(t)
	runDir := t.TempDir()
	for _, current := range []struct {
		stem   string
		input  int
		cached int
	}{
		{"baseline-explain", 100, 80},
		{"optimized-explain", 50, 0},
		{"baseline-review", 100, 20},
		{"optimized-review", 50, 45},
	} {
		writeAnalyzeTranscript(t, runDir, current.stem, []any{
			map[string]any{
				"type":      "thread.started",
				"thread_id": current.stem,
			},
			map[string]any{"type": "turn.started"},
			analyzeCompletedTurnWithUsage(current.input, current.cached, 0, 0),
		})
	}

	runAnalyzeScript(t, shell, analyzeScript, runDir, true)
	type comparisonDiagnostics struct {
		Task                        string  `json:"task"`
		EffectiveReductionPercent   float64 `json:"effective_reduction_percent"`
		BaselineRawTotalTokens      int     `json:"baseline_raw_total_tokens"`
		OptimizedRawTotalTokens     int     `json:"optimized_raw_total_tokens"`
		RawReductionPercent         float64 `json:"raw_reduction_percent"`
		BaselineCachedInputPercent  float64 `json:"baseline_cached_input_percent"`
		OptimizedCachedInputPercent float64 `json:"optimized_cached_input_percent"`
		CachedInputPercentDelta     float64 `json:"cached_input_percent_delta"`
	}
	var metrics struct {
		Comparisons []comparisonDiagnostics `json:"comparisons"`
	}
	readAnalyzeJSON(t, filepath.Join(runDir, "metrics.json"), &metrics)
	if len(metrics.Comparisons) != 2 {
		t.Fatalf("comparison count = %d", len(metrics.Comparisons))
	}
	comparisons := make(map[string]comparisonDiagnostics, len(metrics.Comparisons))
	for _, comparison := range metrics.Comparisons {
		comparisons[comparison.Task] = comparison
	}
	explain, ok := comparisons["explain"]
	if !ok {
		t.Fatal("missing explain comparison")
	}
	if explain.EffectiveReductionPercent >= 0 ||
		explain.BaselineRawTotalTokens != 100 ||
		explain.OptimizedRawTotalTokens != 50 ||
		explain.RawReductionPercent != 50 ||
		explain.BaselineCachedInputPercent != 80 ||
		explain.OptimizedCachedInputPercent != 0 ||
		explain.CachedInputPercentDelta != -80 {
		t.Fatalf("explain comparison = %#v", explain)
	}
	review, ok := comparisons["review"]
	if !ok {
		t.Fatal("missing review comparison")
	}
	if review.BaselineRawTotalTokens != 100 ||
		review.OptimizedRawTotalTokens != 50 ||
		review.RawReductionPercent != 50 ||
		review.BaselineCachedInputPercent != 20 ||
		review.OptimizedCachedInputPercent != 90 ||
		review.CachedInputPercentDelta != 70 {
		t.Fatalf("review comparison = %#v", review)
	}

	summary, err := os.ReadFile(filepath.Join(runDir, "summary.md"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"50% raw-token reduction; cached-input rate 80% -> 0% (-80 percentage points)",
		"50% raw-token reduction; cached-input rate 20% -> 90% (+70 percentage points)",
	} {
		if !strings.Contains(string(summary), want) {
			t.Fatalf("summary is missing %q:\n%s", want, summary)
		}
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

func writeAnalyzeDependencySnapshot(t *testing.T, runDir string) map[string]any {
	t.Helper()
	const (
		moduleSum = "h1:MRx4UaLrDotUKUdCIqzPC48t1Y9hANFKIRpNx+Te8PI="
		goModSum  = "h1:eL/Oa2bBBK0TkX57Fyni+NgnyQQN4LitPmob2Hjnqw4="
	)
	files := map[string][]byte{
		"target-go.mod": []byte(
			"module example.invalid/analyze\n\n" +
				"go 1.26\n\nrequire (\n\tgolang.org/x/time v0.14.0\n)\n",
		),
		"target-go.sum": []byte(
			"golang.org/x/time v0.14.0 " + moduleSum + "\n" +
				"golang.org/x/time v0.14.0/go.mod " + goModSum + "\n",
		),
		"golang.org/x/time@v0.14.0/rate/rate.go":      []byte("package rate\n"),
		"golang.org/x/time@v0.14.0/rate/rate_test.go": []byte("package rate\n"),
	}
	root := filepath.Join(runDir, "dependency-source")
	digests := make(map[string]string, len(files))
	for relative, content := range files {
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatal(err)
		}
		digests[relative] = sha256Bytes(content)
	}
	dependencyManifest := map[string]any{
		"schema_version": 1,
		"module":         "golang.org/x/time",
		"version":        "v0.14.0",
		"module_sum":     moduleSum,
		"go_mod_sum":     goModSum,
		"authentication": "fresh-private-gomodcache-go-mod-download-target-go.sum",
		"command_root":   "$HOME/dependencies/golang.org/x/time@v0.14.0",
		"source_root":    "dependency-source",
		"files":          digests,
	}
	manifestPath := filepath.Join(root, "manifest.json")
	writeAnalyzeJSON(t, manifestPath, dependencyManifest)
	manifestContent, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	return map[string]any{
		"manifest_path":   "dependency-source/manifest.json",
		"manifest_sha256": sha256Bytes(manifestContent),
		"module":          "golang.org/x/time",
		"version":         "v0.14.0",
		"sum":             moduleSum,
	}
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
			"go_mod_cache":   "/tmp/go/pkg/mod",
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
	return analyzeCompletedTurnWithUsage(10, 2, 3, 1)
}

func analyzeCompletedTurnWithUsage(
	inputTokens int,
	cachedInputTokens int,
	outputTokens int,
	reasoningOutputTokens int,
) map[string]any {
	return map[string]any{
		"type": "turn.completed",
		"usage": map[string]any{
			"input_tokens":            inputTokens,
			"cached_input_tokens":     cachedInputTokens,
			"output_tokens":           outputTokens,
			"reasoning_output_tokens": reasoningOutputTokens,
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
