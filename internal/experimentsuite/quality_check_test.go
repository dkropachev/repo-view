package experimentsuite

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const qualityCheckGitFixtureContent = "tracked fixture\n"

func TestQualityCheckJudgeCacheAndArtifactSelection(t *testing.T) {
	bashPath := requireQualityCheckTools(t)
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	targetRoot, head := initializeQualityCheckGitTarget(t)
	if err := os.WriteFile(
		filepath.Join(targetRoot, "AGENTS.md"),
		[]byte("ignored evaluator override\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	runDir := t.TempDir()
	for _, directory := range []string{"answers", "quality"} {
		if err := os.MkdirAll(filepath.Join(runDir, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	writeJSON(t, filepath.Join(runDir, "metrics.json"), map[string]any{
		"cases": []any{
			qualityCheckMetricCase(
				"baseline-explain", "explain", "baseline", "baseline",
			),
			qualityCheckMetricCase(
				"optimized-explain", "explain", "optimized", "default",
			),
		},
	})
	writeJSON(t, filepath.Join(runDir, "changed-packet.json"), map[string]any{
		"root":        targetRoot,
		"head_commit": head,
		"base_commit": head,
	})
	writeJSON(t, filepath.Join(runDir, "manifest.json"), map[string]any{
		"schema_version":    1,
		"worktree":          targetRoot,
		"target_commit":     head,
		"base_commit":       head,
		"task_selection":    "explain",
		"variant_selection": "all",
		"profiles":          []any{"default"},
		"baseline_from":     nil,
	})
	writeQualityGenerationEvidence(
		t, runDir, "baseline-explain", "baseline answer",
	)
	writeQualityGenerationEvidence(
		t, runDir, "optimized-explain", "candidate answer v1",
	)

	legacyJudge := qualityCheckJudgeFixture("explain", false)
	writeJSON(
		t,
		filepath.Join(runDir, "quality", "judge-v4-explain-1.json"),
		legacyJudge,
	)
	writeQualityCheckUsage(t, filepath.Join(
		runDir, "quality", "judge-v4-explain-1.jsonl",
	))
	writeJSON(
		t,
		filepath.Join(runDir, "quality", "judge-explain-10.json"),
		legacyJudge,
	)
	writeQualityCheckUsage(t, filepath.Join(
		runDir, "quality", "judge-explain-10.jsonl",
	))
	if err := os.WriteFile(
		filepath.Join(runDir, "quality", "judge-explain-10.exit-code"),
		[]byte("0\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	writeJSON(
		t,
		filepath.Join(runDir, "quality", "judge-explain-extra.json"),
		legacyJudge,
	)
	writeQualityCheckUsage(t, filepath.Join(
		runDir, "quality", "judge-explain-extra.jsonl",
	))
	writeJSON(
		t,
		filepath.Join(runDir, "quality", "judge-explain-3.json"),
		qualityCheckJudgeFixture("review", false),
	)
	writeQualityCheckUsage(t, filepath.Join(
		runDir, "quality", "judge-explain-3.jsonl",
	))
	invalidJudges := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{
			name: "judge-explain-4",
			mutate: func(judge map[string]any) {
				judge["baseline"].(map[string]any)["correctness"] = 4.5
			},
		},
		{
			name: "judge-explain-5",
			mutate: func(judge map[string]any) {
				qualityCheckCandidate(judge)["not_worse_than_baseline"] = "false"
			},
		},
		{
			name: "judge-explain-6",
			mutate: func(judge map[string]any) {
				qualityCheckCandidate(judge)["unexpected"] = true
			},
		},
		{
			name: "judge-explain-7",
			mutate: func(judge map[string]any) {
				qualityCheckCandidate(judge)["critical_omissions"] = []any{1}
			},
		},
		{
			name: "judge-explain-8",
			mutate: func(judge map[string]any) {
				qualityCheckCandidate(judge)["rationale"] = 1
			},
		},
	}
	for _, invalid := range invalidJudges {
		judge := qualityCheckJudgeFixture("explain", true)
		invalid.mutate(judge)
		writeJSON(
			t,
			filepath.Join(runDir, "quality", invalid.name+".json"),
			judge,
		)
		writeQualityCheckUsage(
			t,
			filepath.Join(runDir, "quality", invalid.name+".jsonl"),
		)
	}
	multiDocumentJudge, err := json.Marshal(
		qualityCheckJudgeFixture("explain", true),
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(runDir, "quality", "judge-explain-9.json"),
		append(append(multiDocumentJudge, '\n'), append(multiDocumentJudge, '\n')...),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	writeQualityCheckUsage(t, filepath.Join(
		runDir, "quality", "judge-explain-9.jsonl",
	))
	if err := os.WriteFile(
		filepath.Join(runDir, "quality", "judge-explain-9.exit-code"),
		[]byte("0\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	fakeBin := filepath.Join(runDir, "bin")
	if err := os.Mkdir(fakeBin, 0o755); err != nil {
		t.Fatal(err)
	}
	countPath := filepath.Join(runDir, "codex-count")
	if err := os.WriteFile(countPath, []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	exitStatusPath := filepath.Join(runDir, "codex-exit-status")
	if err := os.WriteFile(exitStatusPath, []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	mutateControlPath := filepath.Join(runDir, "codex-mutate-control")
	if err := os.WriteFile(mutateControlPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	fakeCodex := `#!/bin/sh
set -eu
fixture_root="$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)"
if [ -n "${OPENAI_API_KEY+x}${CODEX_HOSTILE+x}${HTTPS_PROXY+x}${RUST_LOG+x}" ]; then
  exit 86
fi
if [ "${1:-}" = "--version" ]; then
  printf '%s\n' 'codex-cli 0.144.0'
  exit 0
fi
output=
checkout=
ignore_user_config=0
ignore_rules=0
hooks_disabled=0
project_docs_disabled=0
fallbacks_disabled=0
mcp_disabled=0
apps_disabled=0
permission_selected=0
permission_root_denied=0
permission_auth_denied=0
shell_inherit_none=0
shell_environment_pinned=0
legacy_sandbox=0
model_configured=0
while [ "$#" -gt 0 ]; do
  if [ "$1" = "-o" ]; then
    output="$2"
    shift 2
  elif [ "$1" = "-C" ]; then
    checkout="$2"
    shift 2
  elif [ "$1" = "--ignore-user-config" ]; then
    ignore_user_config=1
    shift
  elif [ "$1" = "--ignore-rules" ]; then
    ignore_rules=1
    shift
  elif [ "$1" = "--disable" ]; then
    if [ "${2:-}" = "hooks" ]; then hooks_disabled=1; fi
    shift 2
  elif [ "$1" = "-c" ]; then
    case "${2:-}" in
      model_*) model_configured=1 ;;
      default_permissions=\"quality-audit\") permission_selected=1 ;;
      permissions.quality-audit=*)
        case "$2" in *'":root"="deny"'*) permission_root_denied=1 ;; esac
        case "$2" in *'":root"="deny"'*'="deny"'*) permission_auth_denied=1 ;; esac
        ;;
      shell_environment_policy.inherit=\"none\") shell_inherit_none=1 ;;
      shell_environment_policy.set=*)
        case "$2" in
          *'GOMODCACHE='*'GOENV="off"'*'GIT_CONFIG_NOSYSTEM="1"'*)
            shell_environment_pinned=1
            ;;
        esac
        ;;
      project_doc_max_bytes=0) project_docs_disabled=1 ;;
      project_doc_fallback_filenames=\[\]) fallbacks_disabled=1 ;;
      mcp_servers=\{\}) mcp_disabled=1 ;;
      apps._default.enabled=false) apps_disabled=1 ;;
    esac
    shift 2
  elif [ "$1" = "-s" ] || [ "$1" = "--sandbox" ]; then
    legacy_sandbox=1
    shift 2
  elif [ "$1" = "-m" ] || [ "$1" = "--model" ]; then
    model_configured=1
    shift 2
  else
    shift
  fi
done
if [ "$ignore_user_config$ignore_rules$hooks_disabled$project_docs_disabled$fallbacks_disabled$mcp_disabled$apps_disabled" != "1111111" ]; then
  exit 89
fi
if [ "$permission_selected$permission_root_denied$permission_auth_denied$shell_inherit_none$shell_environment_pinned$legacy_sandbox" != "111110" ]; then
  exit 87
fi
if [ "$model_configured" != "0" ]; then
  exit 91
fi
case "${CODEX_HOME:-}" in
  */repo-view-quality-codex-home.*) ;;
  *) exit 90 ;;
esac
if [ -e "$checkout/AGENTS.md" ]; then
  exit 88
fi
count="$(sed -n '1p' "$fixture_root/codex-count")"
count=$((count + 1))
printf '%s\n' "$count" > "$fixture_root/codex-count"
judge_json='{"task":"explain","baseline":{"name":"baseline-explain","correctness":5,"completeness":5,"grounding":5,"task_adherence":5,"critical_omissions":[],"unsupported_claims":[]},"candidates":[{"name":"optimized-explain","correctness":5,"completeness":5,"grounding":5,"task_adherence":5,"critical_omissions":[],"unsupported_claims":[],"core_conclusion_matches_baseline":true,"material_contradictions":[],"baseline_material_points_omitted":[],"candidate_material_additions":[],"not_worse_than_baseline":true,"rationale":"complete"}]}'
printf '%s\n' "$judge_json" > "$output"
requested_status="$(sed -n '1p' "$fixture_root/codex-exit-status")"
if [ "$requested_status" -ne 0 ]; then
  exit "$requested_status"
fi
if [ -s "$fixture_root/codex-mutate-control" ]; then
  mutate_path="$(sed -n '1p' "$fixture_root/codex-mutate-control")"
  case "$mutate_path" in
    replace-lock:*)
      lock_path="${mutate_path#replace-lock:}"
      : > "$fixture_root/codex-mutate-control"
      mv -- "$lock_path" "$lock_path.owned"
      mkdir -- "$lock_path"
      printf '%s\n' 'replacement victim' > "$lock_path/victim"
      ;;
    replace-quality:*)
      quality_path="${mutate_path#replace-quality:}"
      : > "$fixture_root/codex-mutate-control"
      mv -- "$quality_path" "$quality_path.owned"
      mkdir -- "$quality_path"
      printf '%s\n' 'quality replacement victim' > "$quality_path/victim"
      ;;
    *)
      printf '%s\n' 'mutated during judging' > "$mutate_path"
      ;;
  esac
fi
printf '%s\n' '{"type":"thread.started","thread_id":"fixture"}'
printf '%s\n' '{"type":"turn.started"}'
jq -cn --arg text "$judge_json" \
  '{type:"item.completed",item:{type:"agent_message",text:$text}}'
printf '%s\n' '{"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":0,"output_tokens":2,"reasoning_output_tokens":1}}'
`
	fakeCodexPath := filepath.Join(fakeBin, "codex")
	if err := os.WriteFile(fakeCodexPath, []byte(fakeCodex), 0o755); err != nil {
		t.Fatal(err)
	}
	hooksDir := filepath.Join(runDir, "global-hooks")
	if err := os.Mkdir(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hookMarker := filepath.Join(runDir, "global-hook-fired")
	if err := os.WriteFile(
		filepath.Join(hooksDir, "post-checkout"),
		[]byte("#!/bin/sh\nprintf fired > \"$QUALITY_HOOK_MARKER\"\n"),
		0o755,
	); err != nil {
		t.Fatal(err)
	}
	globalGitConfig := filepath.Join(runDir, "global-gitconfig")
	configureHooks := exec.Command(
		"git", "config", "--file", globalGitConfig, "core.hooksPath", hooksDir,
	)
	if output, err := configureHooks.CombinedOutput(); err != nil {
		t.Fatalf("configure global hooks: %v\n%s", err, output)
	}
	fsmonitorMarker := filepath.Join(runDir, "local-fsmonitor-fired")
	fsmonitorPath := filepath.Join(runDir, "local-fsmonitor")
	if err := os.WriteFile(
		fsmonitorPath,
		[]byte("#!/bin/sh\nprintf '%s\\n' fired > \"$QUALITY_FSMONITOR_MARKER\"\nprintf '%s\\n' '{}'\n"),
		0o755,
	); err != nil {
		t.Fatal(err)
	}
	configureFSMonitor := exec.Command(
		"git",
		"-C",
		targetRoot,
		"config",
		"--local",
		"core.fsmonitor",
		fsmonitorPath,
	)
	if output, err := configureFSMonitor.CombinedOutput(); err != nil {
		t.Fatalf("configure local fsmonitor: %v\n%s", err, output)
	}

	qualityCheck := filepath.Join(
		repoRoot, "experiments", "lsp-replacement", "quality-check.sh",
	)
	commandEnvironment := make([]string, 0, len(os.Environ())+7)
	for _, variable := range os.Environ() {
		if strings.HasPrefix(variable, "PATH=") ||
			strings.HasPrefix(variable, "FAKE_CODEX_COUNT=") ||
			strings.HasPrefix(variable, "FAKE_CODEX_EXIT_STATUS=") ||
			strings.HasPrefix(variable, "FAKE_CODEX_MUTATE_CONTROL=") ||
			strings.HasPrefix(variable, "GIT_CONFIG_GLOBAL=") ||
			strings.HasPrefix(variable, "QUALITY_HOOK_MARKER=") ||
			strings.HasPrefix(variable, "QUALITY_FSMONITOR_MARKER=") ||
			strings.HasPrefix(variable, "LSP_JUDGE_MODEL_MODE=") {
			continue
		}
		commandEnvironment = append(commandEnvironment, variable)
	}
	commandEnvironment = append(
		commandEnvironment,
		"PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"FAKE_CODEX_COUNT="+countPath,
		"FAKE_CODEX_EXIT_STATUS="+exitStatusPath,
		"FAKE_CODEX_MUTATE_CONTROL="+mutateControlPath,
		"OPENAI_API_KEY=hostile-test-key",
		"CODEX_HOSTILE=must-not-inherit",
		"HTTPS_PROXY=http://hostile.invalid",
		"RUST_LOG=trace",
		"GIT_CONFIG_GLOBAL="+globalGitConfig,
		"QUALITY_HOOK_MARKER="+hookMarker,
		"QUALITY_FSMONITOR_MARKER="+fsmonitorMarker,
		"LSP_JUDGE_MODEL_MODE=pinned",
	)
	qualityCheckCommand := func(repeats string, extraArguments ...string) *exec.Cmd {
		t.Helper()
		arguments := []string{
			qualityCheck,
			runDir,
			"--judge-repeats",
			repeats,
			"--model-mode",
			"router",
		}
		arguments = append(arguments, extraArguments...)
		command := exec.Command(bashPath, arguments...)
		command.Env = commandEnvironment
		return command
	}
	runQualityCheck := func(repeats string, extraArguments ...string) {
		t.Helper()
		command := qualityCheckCommand(repeats, extraArguments...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("quality-check failed: %v\n%s", err, output)
		}
	}
	codexCount := func() string {
		t.Helper()
		content, err := os.ReadFile(countPath)
		if err != nil {
			t.Fatal(err)
		}
		return strings.TrimSpace(string(content))
	}

	command := qualityCheckCommand("1", "--enforce")
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(
		string(output),
		"pre-judge quality gate failed; live judges were not started",
	) {
		t.Fatalf("enforced static failure started judges: %v\n%s", err, output)
	}
	if got := codexCount(); got != "0" {
		t.Fatalf("pre-judge quality failure executed codex: count = %s", got)
	}

	runQualityCheck("1")
	if _, err := os.Stat(hookMarker); !os.IsNotExist(err) {
		t.Fatalf("global post-checkout hook ran in pristine clone: %v", err)
	}
	if _, err := os.Stat(fsmonitorMarker); !os.IsNotExist(err) {
		t.Fatalf("local fsmonitor ran during isolated Git checks: %v", err)
	}
	if got := codexCount(); got != "1" {
		t.Fatalf("codex executions after initial audit = %s, want 1", got)
	}
	assertQualityCheckJudgeAggregate(t, runDir, 1, 1, true)
	aggregateDigest, err := sha256File(
		filepath.Join(runDir, "quality", "aggregate-manifest.json"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SummarizeTrackedEvidence(
		runDir,
		nil,
		aggregateDigest,
	); err != nil {
		t.Fatalf("script aggregate is not consumable: %v", err)
	}

	runQualityCheck("1")
	if got := codexCount(); got != "1" {
		t.Fatalf("unchanged inputs executed codex again: count = %s", got)
	}
	runQualityCheck("01")
	if got := codexCount(); got != "1" {
		t.Fatalf("base-10 repeat count missed the cache: count = %s", got)
	}
	runQualityCheck("2")
	if got := codexCount(); got != "2" {
		t.Fatalf("second independent repeat reused another slot: count = %s", got)
	}
	firstDigest, err := os.ReadFile(filepath.Join(
		runDir, "quality", "judge-explain-1.inputs.sha256",
	))
	if err != nil {
		t.Fatal(err)
	}
	secondDigest, err := os.ReadFile(filepath.Join(
		runDir, "quality", "judge-explain-2.inputs.sha256",
	))
	if err != nil {
		t.Fatal(err)
	}
	if string(firstDigest) == string(secondDigest) {
		t.Fatal("independent repeat slots received the same input digest")
	}
	assertQualityCheckJudgeAggregate(t, runDir, 2, 2, true)
	for _, suffix := range []string{
		"json",
		"jsonl",
		"stderr",
		"exit-code",
		"inputs.sha256",
		"result.sha256",
	} {
		if err := os.Remove(filepath.Join(
			runDir,
			"quality",
			"judge-explain-2."+suffix,
		)); err != nil {
			t.Fatal(err)
		}
	}

	assertDirtyTargetRejected := func(name string, dirty func(), clean func()) {
		t.Helper()
		dirty()
		command := qualityCheckCommand("1")
		output, err := command.CombinedOutput()
		if err == nil ||
			!strings.Contains(string(output), "judge target checkout is dirty") {
			t.Fatalf("%s target result = %v\n%s", name, err, output)
		}
		if got := codexCount(); got != "2" {
			t.Fatalf("%s target executed cached judge: count = %s", name, got)
		}
		clean()
	}
	targetFixture := filepath.Join(targetRoot, "fixture.txt")
	assertDirtyTargetRejected(
		"modified",
		func() {
			if err := os.WriteFile(targetFixture, []byte("modified\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		func() {
			if err := os.WriteFile(
				targetFixture,
				[]byte(qualityCheckGitFixtureContent),
				0o644,
			); err != nil {
				t.Fatal(err)
			}
		},
	)
	untrackedFixture := filepath.Join(targetRoot, "untracked.txt")
	assertDirtyTargetRejected(
		"untracked",
		func() {
			if err := os.WriteFile(untrackedFixture, []byte("untracked\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		},
		func() {
			if err := os.Remove(untrackedFixture); err != nil {
				t.Fatal(err)
			}
		},
	)

	writeQualityGenerationEvidence(
		t, runDir, "optimized-explain", "candidate answer v2",
	)
	runQualityCheck("1")
	if got := codexCount(); got != "3" {
		t.Fatalf("changed answer reused cached judge: count = %s, want 3", got)
	}
	assertQualityCheckJudgeAggregate(t, runDir, 1, 1, true)

	runQualityCheck("0")
	if got := codexCount(); got != "3" {
		t.Fatalf("offline aggregation executed codex: count = %s", got)
	}
	assertQualityCheckJudgeAggregate(t, runDir, 1, 1, true)
	if _, err := os.Stat(filepath.Join(
		runDir,
		"quality",
		"judge-explain-10.inputs.sha256",
	)); !os.IsNotExist(err) {
		t.Fatalf("unbound judge unexpectedly has a digest: %v", err)
	}

	runQualityCheck("0", "--bind-legacy-judges")
	assertQualityCheckJudgeAggregate(t, runDir, 2, 2, false)
	if _, err := os.Stat(filepath.Join(
		runDir,
		"quality",
		"judge-explain-10.legacy-attestation.json",
	)); err != nil {
		t.Fatalf("legacy judge was not attested: %v", err)
	}
	if _, err := os.Stat(filepath.Join(
		runDir,
		"quality",
		"judge-explain-10.inputs.sha256",
	)); !os.IsNotExist(err) {
		t.Fatalf("legacy judge gained a current-cache digest: %v", err)
	}

	writeQualityGenerationEvidence(
		t, runDir, "optimized-explain", "candidate answer v3",
	)
	runQualityCheck("0")
	assertQualityCheckJudgeAggregate(t, runDir, 0, 0, false)
	command = qualityCheckCommand("0", "--bind-legacy-judges")
	output, err = command.CombinedOutput()
	if err == nil ||
		!strings.Contains(
			string(output),
			"refusing to overwrite mismatched legacy judge attestation",
		) {
		t.Fatalf("legacy binding overwrote stale attestation: %v\n%s", err, output)
	}
	writeQualityGenerationEvidence(
		t, runDir, "optimized-explain", "candidate answer v2",
	)
	runQualityCheck("0", "--bind-legacy-judges")
	assertQualityCheckJudgeAggregate(t, runDir, 2, 2, false)

	writeQualityGenerationEvidence(
		t, runDir, "optimized-explain", "candidate answer v4",
	)
	if err := os.WriteFile(exitStatusPath, []byte("42\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	command = qualityCheckCommand("1")
	output, err = command.CombinedOutput()
	if err == nil ||
		!strings.Contains(string(output), "judge output remained invalid") {
		t.Fatalf("failed judge process was accepted: %v\n%s", err, output)
	}
	if got := codexCount(); got != "6" {
		t.Fatalf("failed judge attempt count = %s, want 6", got)
	}
	if _, err := os.Stat(filepath.Join(
		runDir,
		"quality",
		"judge-explain-1.inputs.sha256",
	)); !os.IsNotExist(err) {
		t.Fatalf("failed judge published an input digest: %v", err)
	}
	if err := os.WriteFile(exitStatusPath, []byte("0\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runQualityCheck("1")
	if got := codexCount(); got != "7" {
		t.Fatalf("successful retry count = %s, want 7", got)
	}
	assertQualityCheckJudgeAggregate(t, runDir, 1, 1, true)

	aggregateNames := append(
		append([]string(nil), qualityAggregateFiles...),
		"aggregate-manifest.json",
	)
	aggregateBefore := make(map[string][]byte, len(aggregateNames))
	for _, name := range aggregateNames {
		content, err := os.ReadFile(filepath.Join(runDir, "quality", name))
		if err != nil {
			t.Fatal(err)
		}
		aggregateBefore[name] = content
	}
	writeQualityGenerationEvidence(
		t, runDir, "optimized-explain", "candidate answer v5",
	)
	if err := os.WriteFile(
		mutateControlPath,
		[]byte(filepath.Join(runDir, "answers", "baseline-explain.md")+"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	command = qualityCheckCommand("1")
	output, err = command.CombinedOutput()
	if err == nil ||
		!strings.Contains(string(output), "evaluator input changed during quality check") {
		t.Fatalf("mutated evaluator input was accepted: %v\n%s", err, output)
	}
	for _, name := range aggregateNames {
		content, readErr := os.ReadFile(filepath.Join(runDir, "quality", name))
		if readErr != nil {
			t.Fatal(readErr)
		}
		if string(content) != string(aggregateBefore[name]) {
			t.Errorf("failed quality check partially published %s", name)
		}
	}
	if err := os.WriteFile(mutateControlPath, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	writeQualityGenerationEvidence(
		t, runDir, "baseline-explain", "baseline answer",
	)
	writeJSON(t, filepath.Join(runDir, "manifest.json"), map[string]any{
		"schema_version":    1,
		"worktree":          targetRoot,
		"target_commit":     head,
		"base_commit":       "not-a-commit",
		"task_selection":    "explain",
		"variant_selection": "all",
		"profiles":          []any{"default"},
		"baseline_from":     nil,
	})
	writeJSON(t, filepath.Join(runDir, "changed-packet.json"), map[string]any{
		"root":        targetRoot,
		"head_commit": head,
		"base_commit": "not-a-commit",
	})
	command = qualityCheckCommand("1")
	output, err = command.CombinedOutput()
	if err == nil ||
		!strings.Contains(string(output), "missing or invalid quality manifest") {
		t.Fatalf("invalid base commit was accepted: %v\n%s", err, output)
	}

	for _, name := range []string{
		"judge-v4-explain-1.json",
		"judge-explain-extra.json",
		"judge-explain-3.json",
		"judge-explain-4.json",
		"judge-explain-5.json",
		"judge-explain-6.json",
		"judge-explain-7.json",
		"judge-explain-8.json",
		"judge-explain-9.json",
	} {
		if _, err := os.Stat(filepath.Join(runDir, "quality", name)); err != nil {
			t.Errorf("unselected artifact %s was not preserved: %v", name, err)
		}
	}

	writeJSON(t, filepath.Join(runDir, "manifest.json"), map[string]any{
		"schema_version":    1,
		"worktree":          targetRoot,
		"target_commit":     head,
		"base_commit":       head,
		"task_selection":    "explain",
		"variant_selection": "all",
		"profiles":          []any{"default"},
		"baseline_from":     nil,
	})
	writeJSON(t, filepath.Join(runDir, "changed-packet.json"), map[string]any{
		"root":        targetRoot,
		"head_commit": head,
		"base_commit": head,
	})
	writeQualityGenerationEvidence(
		t, runDir, "optimized-explain", "candidate answer quality replacement",
	)
	qualityPath := filepath.Join(runDir, "quality")
	if err := os.WriteFile(
		mutateControlPath,
		[]byte("replace-quality:"+qualityPath+"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	command = qualityCheckCommand("1")
	qualityReplacementOutput, qualityReplacementErr := command.CombinedOutput()
	if qualityReplacementErr == nil {
		t.Fatalf(
			"quality directory replacement unexpectedly succeeded:\n%s",
			qualityReplacementOutput,
		)
	}
	qualityVictim, err := os.ReadFile(filepath.Join(qualityPath, "victim"))
	if err != nil {
		entries, _ := os.ReadDir(runDir)
		t.Fatalf(
			"quality replacement victim was deleted: %v; entries=%v; output=%s",
			err,
			entries,
			qualityReplacementOutput,
		)
	}
	if string(qualityVictim) != "quality replacement victim\n" {
		t.Fatalf("quality replacement victim changed to %q", qualityVictim)
	}
	if err := os.Rename(qualityPath, qualityPath+".replacement"); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(qualityPath+".owned", qualityPath); err != nil {
		t.Fatal(err)
	}

	writeQualityGenerationEvidence(
		t, runDir, "optimized-explain", "candidate answer lock replacement",
	)
	lockPath := filepath.Join(runDir, ".quality-check.lock")
	if err := os.WriteFile(
		mutateControlPath,
		[]byte("replace-lock:"+lockPath+"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	command = qualityCheckCommand("1")
	if output, err := command.CombinedOutput(); err == nil {
		t.Fatalf("lock replacement unexpectedly succeeded:\n%s", output)
	}
	replacementVictim, err := os.ReadFile(filepath.Join(lockPath, "victim"))
	if err != nil {
		t.Fatalf("replacement lock victim was deleted: %v", err)
	}
	if string(replacementVictim) != "replacement victim\n" {
		t.Fatalf("replacement lock victim changed to %q", replacementVictim)
	}
}

func TestQualityCheckRejectsInvalidJudgeOptions(t *testing.T) {
	bashPath := requireQualityCheckTools(t)
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(
		bashPath,
		filepath.Join(
			repoRoot,
			"experiments",
			"lsp-replacement",
			"quality-check.sh",
		),
		t.TempDir(),
		"--judge-repeats",
		"invalid",
	)
	output, err := command.CombinedOutput()
	if err == nil {
		t.Fatalf("quality-check accepted invalid repeats:\n%s", output)
	}
	if got := string(output); got != "--judge-repeats must be a non-negative integer\n" {
		t.Fatalf("invalid repeats output = %q", got)
	}

	command = exec.Command(
		bashPath,
		filepath.Join(
			repoRoot,
			"experiments",
			"lsp-replacement",
			"quality-check.sh",
		),
		t.TempDir(),
		"--judge-repeats",
		"18446744073709551616",
	)
	output, err = command.CombinedOutput()
	if err == nil {
		t.Fatalf("quality-check accepted overflowing repeats:\n%s", output)
	}
	if got := string(output); got != "--judge-repeats must be between 0 and 100\n" {
		t.Fatalf("overflowing repeats output = %q", got)
	}

	command = exec.Command(
		bashPath,
		filepath.Join(
			repoRoot,
			"experiments",
			"lsp-replacement",
			"quality-check.sh",
		),
		t.TempDir(),
		"--judge-repeats",
	)
	output, err = command.CombinedOutput()
	if err == nil {
		t.Fatalf("quality-check accepted missing repeats value:\n%s", output)
	}
	if got := string(output); got != "--judge-repeats requires a value\n" {
		t.Fatalf("missing repeats output = %q", got)
	}

	command = exec.Command(
		bashPath,
		filepath.Join(
			repoRoot,
			"experiments",
			"lsp-replacement",
			"quality-check.sh",
		),
		t.TempDir(),
		"--judge-repeats",
		"1",
		"--bind-legacy-judges",
	)
	output, err = command.CombinedOutput()
	if err == nil {
		t.Fatalf("quality-check combined binding with live repeats:\n%s", output)
	}
	if got := string(output); got != "--bind-legacy-judges requires --judge-repeats 0\n" {
		t.Fatalf("binding with live repeats output = %q", got)
	}

	command = exec.Command(
		bashPath,
		filepath.Join(
			repoRoot,
			"experiments",
			"lsp-replacement",
			"quality-check.sh",
		),
		t.TempDir(),
		"--model-mode",
		"automatic",
	)
	output, err = command.CombinedOutput()
	if err == nil {
		t.Fatalf("quality-check accepted invalid model mode:\n%s", output)
	}
	if got := string(output); got != "invalid --model-mode: automatic\n" {
		t.Fatalf("invalid model mode output = %q", got)
	}
}

func TestQualityCheckRejectsSymlinkedQualityDirectory(t *testing.T) {
	bashPath := requireQualityCheckTools(t)
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	runDir := t.TempDir()
	victimDir := t.TempDir()
	sentinel := filepath.Join(victimDir, "static.json")
	if err := os.WriteFile(sentinel, []byte("victim\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victimDir, filepath.Join(runDir, "quality")); err != nil {
		t.Skipf("symlink creation unavailable: %v", err)
	}
	command := exec.Command(
		bashPath,
		filepath.Join(
			repoRoot,
			"experiments",
			"lsp-replacement",
			"quality-check.sh",
		),
		runDir,
		"--skip-analyze",
	)
	output, err := command.CombinedOutput()
	if err == nil ||
		!strings.Contains(string(output), "quality output path is not a real directory") {
		t.Fatalf("symlinked quality directory result = %v\n%s", err, output)
	}
	content, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "victim\n" {
		t.Fatalf("victim content changed to %q", content)
	}
}

func TestQualityCheckRejectsMalformedMetricsAndMissingComparator(t *testing.T) {
	bashPath := requireQualityCheckTools(t)
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	qualityCheck := filepath.Join(
		repoRoot,
		"experiments",
		"lsp-replacement",
		"quality-check.sh",
	)

	malformedRun := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(malformedRun, "metrics.json"),
		[]byte("{bad\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	command := exec.Command(bashPath, qualityCheck, malformedRun)
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "invalid metrics.json") {
		t.Fatalf("malformed metrics result = %v\n%s", err, output)
	}

	missingMetricsRun := t.TempDir()
	command = exec.Command(
		bashPath,
		qualityCheck,
		missingMetricsRun,
		"--skip-analyze",
	)
	output, err = command.CombinedOutput()
	if err == nil ||
		!strings.Contains(string(output), "metrics.json does not exist") {
		t.Fatalf("--skip-analyze regenerated missing metrics: %v\n%s", err, output)
	}

	lockedRun := t.TempDir()
	if err := os.Mkdir(
		filepath.Join(lockedRun, ".quality-check.lock"),
		0o755,
	); err != nil {
		t.Fatal(err)
	}
	command = exec.Command(bashPath, qualityCheck, lockedRun)
	output, err = command.CombinedOutput()
	if err == nil ||
		!strings.Contains(string(output), "quality check already running") {
		t.Fatalf("concurrent quality check was not rejected: %v\n%s", err, output)
	}

	missingBaselineRun := t.TempDir()
	if err := os.Mkdir(
		filepath.Join(missingBaselineRun, "answers"),
		0o755,
	); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(missingBaselineRun, "metrics.json"), map[string]any{
		"cases": []any{map[string]any{
			"name":        "optimized-explain",
			"task":        "explain",
			"variant":     "optimized",
			"profile":     "default",
			"completed":   true,
			"answer_file": "answers/optimized-explain.md",
		}},
	})
	if err := os.WriteFile(
		filepath.Join(missingBaselineRun, "answers", "optimized-explain.md"),
		[]byte("candidate\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	command = exec.Command(
		bashPath,
		qualityCheck,
		missingBaselineRun,
		"--enforce",
	)
	output, err = command.CombinedOutput()
	if err == nil {
		t.Fatalf("enforcement accepted candidate without baseline:\n%s", output)
	}
}

func TestQualityCheckRejectsVacuousAndMismatchedEvidence(t *testing.T) {
	bashPath := requireQualityCheckTools(t)
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	qualityCheck := filepath.Join(
		repoRoot, "experiments", "lsp-replacement", "quality-check.sh",
	)
	targetRoot, head := initializeQualityCheckGitTarget(t)

	t.Run("baseline only enforcement", func(t *testing.T) {
		runDir := t.TempDir()
		if err := os.Mkdir(filepath.Join(runDir, "answers"), 0o755); err != nil {
			t.Fatal(err)
		}
		writeJSON(t, filepath.Join(runDir, "metrics.json"), map[string]any{
			"cases": []any{qualityCheckMetricCase(
				"baseline-explain", "explain", "baseline", "baseline",
			)},
		})
		writeJSON(t, filepath.Join(runDir, "manifest.json"), map[string]any{
			"schema_version":    1,
			"worktree":          targetRoot,
			"target_commit":     head,
			"base_commit":       head,
			"task_selection":    "explain",
			"variant_selection": "baseline",
			"profiles":          []any{"default"},
			"baseline_from":     nil,
		})
		writeQualityGenerationEvidence(
			t, runDir, "baseline-explain", "baseline answer",
		)
		command := exec.Command(
			bashPath, qualityCheck, runDir, "--skip-analyze", "--enforce",
		)
		output, err := command.CombinedOutput()
		if err == nil ||
			!strings.Contains(string(output), "requires an optimized case") {
			t.Fatalf("baseline-only enforcement result = %v\n%s", err, output)
		}
	})

	t.Run("manifest case mismatch", func(t *testing.T) {
		runDir := t.TempDir()
		if err := os.Mkdir(filepath.Join(runDir, "answers"), 0o755); err != nil {
			t.Fatal(err)
		}
		writeJSON(t, filepath.Join(runDir, "metrics.json"), map[string]any{
			"cases": []any{qualityCheckMetricCase(
				"baseline-explain", "explain", "baseline", "baseline",
			)},
		})
		writeJSON(t, filepath.Join(runDir, "manifest.json"), map[string]any{
			"schema_version":    1,
			"worktree":          targetRoot,
			"target_commit":     head,
			"base_commit":       head,
			"task_selection":    "explain",
			"variant_selection": "all",
			"profiles":          []any{"default"},
			"baseline_from":     nil,
		})
		command := exec.Command(
			bashPath, qualityCheck, runDir, "--skip-analyze", "--enforce",
		)
		output, err := command.CombinedOutput()
		if err == nil ||
			!strings.Contains(string(output), "exactly match manifest") {
			t.Fatalf("mismatched matrix result = %v\n%s", err, output)
		}
	})

	t.Run("boolean optional counter", func(t *testing.T) {
		runDir := t.TempDir()
		current := qualityCheckMetricCase(
			"baseline-explain", "explain", "baseline", "baseline",
		)
		current["repo_view_invocation_count"] = false
		writeJSON(t, filepath.Join(runDir, "metrics.json"), map[string]any{
			"cases": []any{current},
		})
		command := exec.Command(
			bashPath, qualityCheck, runDir, "--skip-analyze",
		)
		output, err := command.CombinedOutput()
		if err == nil || !strings.Contains(string(output), "invalid metrics.json") {
			t.Fatalf("boolean counter result = %v\n%s", err, output)
		}
	})

	t.Run("incompatible metrics schema", func(t *testing.T) {
		runDir := t.TempDir()
		writeJSON(t, filepath.Join(runDir, "metrics.json"), map[string]any{
			"schema_version": 999,
			"cases": []any{qualityCheckMetricCase(
				"baseline-explain", "explain", "baseline", "baseline",
			)},
		})
		command := exec.Command(
			bashPath, qualityCheck, runDir, "--skip-analyze",
		)
		output, err := command.CombinedOutput()
		if err == nil || !strings.Contains(string(output), "invalid metrics.json") {
			t.Fatalf("incompatible schema result = %v\n%s", err, output)
		}
	})

	for _, testCase := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{
			name: "missing prompt commit",
			mutate: func(manifest map[string]any) {
				manifest["prompt_commit"] = nil
			},
		},
		{
			name: "missing base ref",
			mutate: func(manifest map[string]any) {
				manifest["base_ref"] = nil
			},
		},
		{
			name: "optimized only without baseline provenance",
			mutate: func(manifest map[string]any) {
				manifest["variant_selection"] = "optimized"
				manifest["baseline_from"] = nil
			},
		},
		{
			name: "legacy generation isolation",
			mutate: func(manifest map[string]any) {
				manifest["generation_isolation"] = "legacy-unisolated"
			},
		},
		{
			name: "wrong prompt digest set",
			mutate: func(manifest map[string]any) {
				manifest["prompt_digests"] = map[string]any{
					"review": strings.Repeat("1", 64),
				}
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			runDir := t.TempDir()
			writeJSON(t, filepath.Join(runDir, "metrics.json"), map[string]any{
				"cases": []any{
					qualityCheckMetricCase(
						"baseline-explain", "explain", "baseline", "baseline",
					),
					qualityCheckMetricCase(
						"optimized-explain", "explain", "optimized", "default",
					),
				},
			})
			manifest := map[string]any{
				"schema_version":    1,
				"worktree":          targetRoot,
				"target_commit":     head,
				"base_commit":       head,
				"task_selection":    "explain",
				"variant_selection": "all",
				"profiles":          []any{"default"},
				"baseline_from":     nil,
			}
			testCase.mutate(manifest)
			writeJSON(t, filepath.Join(runDir, "manifest.json"), manifest)
			command := exec.Command(
				bashPath, qualityCheck, runDir, "--skip-analyze", "--enforce",
			)
			output, err := command.CombinedOutput()
			if err == nil ||
				!strings.Contains(string(output), "missing or invalid quality manifest") {
				t.Fatalf("invalid provenance manifest result = %v\n%s", err, output)
			}
		})
	}

	newStrictRun := func(t *testing.T) string {
		t.Helper()
		runDir := t.TempDir()
		writeJSON(t, filepath.Join(runDir, "metrics.json"), map[string]any{
			"cases": []any{
				qualityCheckMetricCase(
					"baseline-explain", "explain", "baseline", "baseline",
				),
				qualityCheckMetricCase(
					"optimized-explain", "explain", "optimized", "default",
				),
			},
		})
		writeJSON(t, filepath.Join(runDir, "manifest.json"), map[string]any{
			"schema_version":    1,
			"worktree":          targetRoot,
			"target_commit":     head,
			"base_commit":       head,
			"task_selection":    "explain",
			"variant_selection": "all",
			"profiles":          []any{"default"},
			"baseline_from":     nil,
		})
		writeJSON(t, filepath.Join(runDir, "changed-packet.json"), map[string]any{
			"root":        targetRoot,
			"head_commit": head,
			"base_commit": head,
		})
		writeQualityGenerationEvidence(
			t, runDir, "baseline-explain", "baseline answer",
		)
		writeQualityGenerationEvidence(
			t, runDir, "optimized-explain", "candidate answer",
		)
		return runDir
	}
	t.Run("missing generation config", func(t *testing.T) {
		runDir := newStrictRun(t)
		if err := os.Remove(filepath.Join(runDir, "generation-config.json")); err != nil {
			t.Fatal(err)
		}
		command := exec.Command(
			bashPath, qualityCheck, runDir, "--skip-analyze", "--enforce",
		)
		output, err := command.CombinedOutput()
		if err == nil ||
			!strings.Contains(string(output), "generation config is missing") {
			t.Fatalf("missing generation config result = %v\n%s", err, output)
		}
	})
	t.Run("tampered generation config", func(t *testing.T) {
		runDir := newStrictRun(t)
		if err := os.WriteFile(
			filepath.Join(runDir, "generation-config.json"),
			[]byte(`{"mechanical_navigation_semantics_enforced":true}`),
			0o644,
		); err != nil {
			t.Fatal(err)
		}
		command := exec.Command(
			bashPath, qualityCheck, runDir, "--skip-analyze", "--enforce",
		)
		output, err := command.CombinedOutput()
		if err == nil ||
			!strings.Contains(string(output), "generation config is missing") {
			t.Fatalf("tampered generation config result = %v\n%s", err, output)
		}
	})
	t.Run("forged generation config digest", func(t *testing.T) {
		runDir := newStrictRun(t)
		configPath := filepath.Join(runDir, "generation-config.json")
		var config map[string]any
		content, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(content, &config); err != nil {
			t.Fatal(err)
		}
		config["forged"] = true
		content, err = json.Marshal(config)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(configPath, content, 0o644); err != nil {
			t.Fatal(err)
		}
		manifestPath := filepath.Join(runDir, "manifest.json")
		var manifest map[string]any
		manifestContent, err := os.ReadFile(manifestPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(manifestContent, &manifest); err != nil {
			t.Fatal(err)
		}
		manifest["generation_config_sha256"] = sha256Bytes(content)
		manifestContent, err = json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(manifestPath, manifestContent, 0o644); err != nil {
			t.Fatal(err)
		}
		command := exec.Command(
			bashPath, qualityCheck, runDir, "--skip-analyze", "--enforce",
		)
		output, err := command.CombinedOutput()
		if err == nil ||
			!strings.Contains(string(output), "generation config is missing") {
			t.Fatalf("forged generation config result = %v\n%s", err, output)
		}
	})
	t.Run("unsuccessful completion marker", func(t *testing.T) {
		runDir := newStrictRun(t)
		writeJSON(t, filepath.Join(runDir, "run-complete.json"), map[string]any{
			"schema_version": 1,
			"state":          "complete",
			"outcome":        "failed",
			"exit_code":      1,
			"completed_at":   "2026-01-01T00:00:00Z",
		})
		command := exec.Command(
			bashPath, qualityCheck, runDir, "--skip-analyze", "--enforce",
		)
		output, err := command.CombinedOutput()
		if err == nil ||
			!strings.Contains(string(output), "successful run-complete.json") {
			t.Fatalf("failed run completion result = %v\n%s", err, output)
		}
	})

	t.Run("legacy import without isolation provenance", func(t *testing.T) {
		runDir := t.TempDir()
		writeJSON(t, filepath.Join(runDir, "metrics.json"), map[string]any{
			"cases": []any{
				qualityCheckMetricCase(
					"baseline-explain", "explain", "baseline", "baseline",
				),
				qualityCheckMetricCase(
					"optimized-explain", "explain", "optimized", "default",
				),
			},
		})
		writeJSON(t, filepath.Join(runDir, "manifest.json"), map[string]any{
			"schema_version":    1,
			"worktree":          targetRoot,
			"target_commit":     head,
			"base_commit":       head,
			"task_selection":    "explain",
			"variant_selection": "optimized",
			"profiles":          []any{"default"},
			"baseline_from":     "legacy",
		})
		writeJSON(
			t,
			filepath.Join(runDir, "baseline-source-manifest.json"),
			map[string]any{
				"schema_version": 1,
				"target_commit":  head,
				"prompt_commit":  head,
				"base_commit":    head,
				"base_ref":       "HEAD",
			},
		)
		generationConfig, err := os.ReadFile(
			filepath.Join(runDir, "generation-config.json"),
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(
				runDir,
				"baseline-source-generation-config.json",
			),
			generationConfig,
			0o644,
		); err != nil {
			t.Fatal(err)
		}
		profilesSnapshot, err := os.ReadFile(
			filepath.Join(runDir, "profiles-snapshot.tsv"),
		)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(runDir, "baseline-source-profiles-snapshot.tsv"),
			profilesSnapshot,
			0o644,
		); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(
			filepath.Join(runDir, "baseline-source-prompts"),
			0o755,
		); err != nil {
			t.Fatal(err)
		}
		prompt, err := os.ReadFile(filepath.Join(runDir, "prompts", "explain.txt"))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(runDir, "baseline-source-prompts", "explain.txt"),
			prompt,
			0o644,
		); err != nil {
			t.Fatal(err)
		}
		command := exec.Command(
			bashPath, qualityCheck, runDir, "--skip-analyze", "--enforce",
		)
		output, err := command.CombinedOutput()
		if err == nil ||
			!strings.Contains(
				string(output),
				"imported baseline source manifest disagrees",
			) {
			t.Fatalf("legacy import provenance result = %v\n%s", err, output)
		}
	})
}

func TestQualityCheckDeepNavigationRequiresPositiveUnexceededCap(t *testing.T) {
	bashPath := requireQualityCheckTools(t)
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	qualityCheck := filepath.Join(
		repoRoot, "experiments", "lsp-replacement", "quality-check.sh",
	)
	targetRoot, head := initializeQualityCheckGitTarget(t)
	for _, testCase := range []struct {
		name        string
		cap         int
		capExceeded bool
	}{
		{name: "zero cap", cap: 0},
		{name: "reported exceeded", cap: 3, capExceeded: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			runDir := t.TempDir()
			if err := os.Mkdir(filepath.Join(runDir, "answers"), 0o755); err != nil {
				t.Fatal(err)
			}
			baseline := qualityCheckMetricCase(
				"baseline-deep-explain",
				"deep-explain",
				"baseline",
				"baseline",
			)
			candidate := qualityCheckMetricCase(
				"optimized-deep-explain",
				"deep-explain",
				"optimized",
				"default",
			)
			candidate["repo_view_invocation_count"] = 3
			candidate["repo_view_invocation_cap"] = testCase.cap
			candidate["repo_view_invocation_cap_exceeded"] = testCase.capExceeded
			candidate["repo_view_changed_invocation_count"] = 1
			candidate["repo_view_find_invocation_count"] = 1
			candidate["repo_view_inspect_invocation_count"] = 1
			writeJSON(t, filepath.Join(runDir, "metrics.json"), map[string]any{
				"cases": []any{baseline, candidate},
			})
			writeJSON(t, filepath.Join(runDir, "manifest.json"), map[string]any{
				"schema_version":    1,
				"worktree":          targetRoot,
				"target_commit":     head,
				"base_commit":       head,
				"task_selection":    "deep-explain",
				"variant_selection": "all",
				"profiles":          []any{"default"},
				"baseline_from":     nil,
			})
			writeJSON(t, filepath.Join(runDir, "changed-packet.json"), map[string]any{
				"root":        targetRoot,
				"head_commit": head,
				"base_commit": head,
			})
			writeQualityGenerationEvidence(
				t, runDir, "baseline-deep-explain", "baseline answer",
			)
			writeQualityGenerationEvidence(
				t, runDir, "optimized-deep-explain", "candidate answer",
			)
			command := exec.Command(
				bashPath, qualityCheck, runDir, "--skip-analyze",
			)
			output, err := command.CombinedOutput()
			if err == nil ||
				!strings.Contains(
					string(output),
					"metrics do not match independent raw evidence analysis",
				) {
				t.Fatalf("untrusted navigation accounting result = %v\n%s", err, output)
			}
		})
	}
}

func TestQualityCheckKeepsShallowAndDeepCandidatesSeparate(t *testing.T) {
	bashPath := requireQualityCheckTools(t)
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	targetRoot, head := initializeQualityCheckGitTarget(t)
	runDir := t.TempDir()
	for _, directory := range []string{"answers", "quality"} {
		if err := os.Mkdir(filepath.Join(runDir, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	var cases []any
	for _, task := range []string{"explain", "review"} {
		for _, variant := range []string{"baseline", "optimized"} {
			name := variant + "-" + task
			profile := "default"
			if variant == "baseline" {
				profile = "baseline"
			}
			cases = append(
				cases,
				qualityCheckMetricCase(name, task, variant, profile),
			)
			writeQualityGenerationEvidence(
				t, runDir, name, name+" answer",
			)
		}
	}
	writeJSON(t, filepath.Join(runDir, "metrics.json"), map[string]any{
		"cases": cases,
	})
	writeJSON(t, filepath.Join(runDir, "manifest.json"), map[string]any{
		"schema_version":    1,
		"worktree":          targetRoot,
		"target_commit":     head,
		"base_commit":       head,
		"task_selection":    "all",
		"variant_selection": "all",
		"profiles":          []any{"default"},
		"baseline_from":     nil,
	})
	writeJSON(t, filepath.Join(runDir, "changed-packet.json"), map[string]any{
		"root":        targetRoot,
		"head_commit": head,
		"base_commit": head,
	})
	qualityCheck := filepath.Join(
		repoRoot,
		"experiments",
		"lsp-replacement",
		"quality-check.sh",
	)
	command := exec.Command(
		bashPath,
		qualityCheck,
		runDir,
		"--judge-repeats",
		"0",
		"--bind-legacy-judges",
	)
	output, err := command.CombinedOutput()
	if err == nil ||
		!strings.Contains(string(output), "no eligible legacy judge artifacts") {
		t.Fatalf("empty legacy binding result = %v\n%s", err, output)
	}
	writeJSON(
		t,
		filepath.Join(runDir, "quality", "judge-explain-1.json"),
		func() map[string]any {
			judge := qualityCheckJudgeFixture("explain", true)
			qualityCheckCandidate(judge)["correctness"] = 4
			return judge
		}(),
	)
	writeQualityCheckUsage(t, filepath.Join(
		runDir, "quality", "judge-explain-1.jsonl",
	))
	if err := os.WriteFile(
		filepath.Join(runDir, "quality", "judge-explain-1.exit-code"),
		[]byte("0\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	command = exec.Command(
		bashPath,
		qualityCheck,
		runDir,
		"--judge-repeats",
		"0",
		"--bind-legacy-judges",
	)
	output, err = command.CombinedOutput()
	if err == nil ||
		!strings.Contains(string(output), "no eligible legacy judge artifacts") {
		t.Fatalf("inconsistent not-worse judge was accepted: %v\n%s", err, output)
	}
	writeJSON(
		t,
		filepath.Join(runDir, "quality", "judge-explain-1.json"),
		qualityCheckJudgeFixture("explain", true),
	)
	writeQualityCheckUsage(t, filepath.Join(
		runDir, "quality", "judge-explain-1.jsonl",
	))
	command = exec.Command(
		bashPath,
		qualityCheck,
		runDir,
		"--judge-repeats",
		"0",
		"--bind-legacy-judges",
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("quality-check failed: %v\n%s", err, output)
	}
	var judges struct {
		Candidates []struct {
			Name string `json:"name"`
		} `json:"candidates"`
	}
	content, err := os.ReadFile(filepath.Join(runDir, "quality", "judges.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(content, &judges); err != nil {
		t.Fatal(err)
	}
	if len(judges.Candidates) != 1 ||
		judges.Candidates[0].Name != "optimized-explain" {
		t.Fatalf("shallow judge candidates = %+v", judges.Candidates)
	}
}

func initializeQualityCheckGitTarget(t *testing.T) (string, string) {
	t.Helper()
	targetRoot := t.TempDir()
	commands := [][]string{
		{"init", "--quiet"},
		{"add", "fixture.txt", ".gitignore"},
		{
			"-c", "user.name=Quality Check Test",
			"-c", "user.email=quality-check-test@example.invalid",
			"-c", "commit.gpgSign=false",
			"-c", "core.hooksPath=/dev/null",
			"commit", "--quiet", "--no-gpg-sign", "--no-verify", "-m", "fixture",
		},
	}
	for index, arguments := range commands {
		if index == 1 {
			if err := os.WriteFile(
				filepath.Join(targetRoot, "fixture.txt"),
				[]byte(qualityCheckGitFixtureContent),
				0o644,
			); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(
				filepath.Join(targetRoot, ".gitignore"),
				[]byte("AGENTS.md\n"),
				0o644,
			); err != nil {
				t.Fatal(err)
			}
		}
		command := exec.Command("git", append([]string{"-C", targetRoot}, arguments...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s failed: %v\n%s", arguments[0], err, output)
		}
	}
	command := exec.Command("git", "-C", targetRoot, "rev-parse", "HEAD")
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	return targetRoot, strings.TrimSpace(string(output))
}

func qualityCheckJudgeFixture(task string, notWorse bool) map[string]any {
	return map[string]any{
		"task": task,
		"baseline": map[string]any{
			"name":               "baseline-explain",
			"correctness":        5,
			"completeness":       5,
			"grounding":          5,
			"task_adherence":     5,
			"critical_omissions": []any{},
			"unsupported_claims": []any{},
		},
		"candidates": []any{map[string]any{
			"name":                             "optimized-explain",
			"correctness":                      5,
			"completeness":                     5,
			"grounding":                        5,
			"task_adherence":                   5,
			"critical_omissions":               []any{},
			"unsupported_claims":               []any{},
			"core_conclusion_matches_baseline": true,
			"material_contradictions":          []any{},
			"baseline_material_points_omitted": []any{},
			"candidate_material_additions":     []any{},
			"not_worse_than_baseline":          notWorse,
			"rationale":                        "fixture",
		}},
	}
}

func qualityCheckCandidate(judge map[string]any) map[string]any {
	return judge["candidates"].([]any)[0].(map[string]any)
}

func qualityCheckMetricCase(
	name, task, variant, profile string,
) map[string]any {
	return map[string]any{
		"name":                                             name,
		"task":                                             task,
		"variant":                                          variant,
		"profile":                                          profile,
		"completed":                                        true,
		"exit_code":                                        0,
		"answer_file":                                      "answers/" + name + ".md",
		"commands_file":                                    "commands/" + name + ".txt",
		"tool_stats_file":                                  "tool-stats/" + name + ".json",
		"call_graph_dot_file":                              "call-graphs/" + name + ".dot",
		"call_graph_markdown_file":                         "call-graphs/" + name + ".md",
		"input_tokens":                                     10,
		"cached_input_tokens":                              2,
		"cached_input_equivalent_tokens":                   0.2,
		"cached_input_percent":                             20,
		"regular_input_tokens":                             8,
		"output_tokens":                                    3,
		"reasoning_output_tokens":                          1,
		"raw_total_tokens":                                 13,
		"effective_tokens":                                 11.2,
		"tool_call_count":                                  0,
		"command_execution_tool_call_count":                0,
		"other_tool_call_count":                            0,
		"repo_view_invocation_count":                       0,
		"repo_view_tool_call_count":                        0,
		"tool_call_accounting_valid":                       true,
		"repo_view_invocation_accounting_valid":            true,
		"repo_view_tool_call_accounting_valid":             true,
		"tool_types":                                       []any{},
		"operations":                                       []any{},
		"temporal_tool_edge_count":                         0,
		"output_reference_edge_count":                      0,
		"repo_view_invocation_cap":                         0,
		"repo_view_invocation_cap_exceeded":                false,
		"repo_view_tool_output_characters":                 0,
		"repo_view_budget_observed_used":                   0,
		"repo_view_budget_accounting_valid":                true,
		"repo_view_command_shape_valid":                    true,
		"repo_view_first_invocation_changed":               false,
		"repo_view_navigation_semantics_valid":             variant == "baseline",
		"mechanical_navigation_semantics_enforced":         variant == "optimized",
		"repo_view_navigation_semantic_violation_commands": []any{},
		"repo_view_budget_tamper_command_count":            0,
		"repo_view_budget_tamper_commands":                 []any{},
		"repo_view_bounds": map[string]any{
			"limit":           0,
			"context":         0,
			"max_code_lines":  0,
			"max_patch_lines": 0,
		},
		"repo_view_bound_violation_count":    0,
		"repo_view_bound_violation_commands": []any{},
		"repo_view_changed_invocation_count": 0,
		"repo_view_find_invocation_count":    0,
		"repo_view_inspect_invocation_count": 0,
		"repo_view_outline_invocation_count": 0,
		"tool_output_characters":             0,
	}
}

func writeQualityGenerationEvidence(
	t *testing.T,
	runDir, name, answer string,
) {
	t.Helper()
	for _, directory := range []string{
		"answers",
		"commands",
		"tool-stats",
		"call-graphs",
	} {
		if err := os.MkdirAll(filepath.Join(runDir, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(
		filepath.Join(runDir, "answers", name+".md"),
		[]byte(answer+"\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	events := []any{
		map[string]any{
			"type":      "thread.started",
			"thread_id": "fixture",
		},
		map[string]any{"type": "turn.started"},
		map[string]any{
			"type": "item.completed",
			"item": map[string]any{
				"type": "agent_message",
				"text": answer,
			},
		},
		map[string]any{
			"type": "turn.completed",
			"usage": map[string]any{
				"input_tokens":            10,
				"cached_input_tokens":     2,
				"output_tokens":           3,
				"reasoning_output_tokens": 1,
			},
		},
	}
	var transcript strings.Builder
	for _, event := range events {
		data, err := json.Marshal(event)
		if err != nil {
			t.Fatal(err)
		}
		transcript.Write(data)
		transcript.WriteByte('\n')
	}
	if err := os.WriteFile(
		filepath.Join(runDir, name+".jsonl"),
		[]byte(transcript.String()),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(runDir, name+".exit-code"),
		[]byte("0\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(runDir, "commands", name+".txt"),
		nil,
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(runDir, "tool-stats", name+".json"), map[string]any{
		"schema_version":                     1,
		"total_tool_calls":                   0,
		"command_execution_tool_calls":       0,
		"repo_view_tool_calls":               0,
		"other_tool_calls":                   0,
		"repo_view_invocations":              0,
		"repo_view_command_shape_valid":      true,
		"repo_view_command_shape_violations": []any{},
		"tool_types":                         []any{},
		"operations":                         []any{},
		"temporal_edge_count":                0,
		"output_reference_edge_count":        0,
		"calls":                              []any{},
		"call_graph": map[string]any{
			"dependency_model": "The transcript has no explicit tool-result dependency IDs. next_tool_call edges are temporal/model-context inferences; output_reference edges additionally prove literal reuse of a prior output value in a later command, but causal use remains inferred.",
			"nodes":            []any{},
			"edges":            []any{},
		},
	})
	if err := os.WriteFile(
		filepath.Join(runDir, "call-graphs", name+".dot"),
		[]byte("digraph tool_calls {\n  rankdir=\"LR\";\n}\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(runDir, "call-graphs", name+".md"),
		[]byte("# Tool Call Graph\n\nThe transcript has no explicit tool-result dependency IDs. next_tool_call edges are temporal/model-context inferences; output_reference edges additionally prove literal reuse of a prior output value in a later command, but causal use remains inferred.\n\n## Nodes\n\n| # | ID | Tool type | Primary operation | Operations | Output characters |\n| ---: | --- | --- | --- | --- | ---: |\n\n## Edges\n\n| From | To | Kind | Confidence | Evidence |\n| --- | --- | --- | --- | --- |\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
}

func requireQualityCheckTools(t *testing.T) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("quality-check integration requires a Unix shell environment")
	}
	var bashPath string
	for _, name := range []string{
		"bash",
		"git",
		"jq",
		"sha256sum",
		"awk",
		"basename",
		"date",
		"find",
		"go",
		"mktemp",
		"mv",
		"sed",
		"sort",
		"stat",
	} {
		path, err := exec.LookPath(name)
		if err != nil {
			t.Skipf("quality-check integration requires %s: %v", name, err)
		}
		if name == "bash" {
			bashPath = path
		}
	}
	return bashPath
}

func writeQualityCheckUsage(t *testing.T, path string) {
	t.Helper()
	outputPath := strings.TrimSuffix(path, "l")
	output, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	message, err := json.Marshal(map[string]any{
		"type": "item.completed",
		"item": map[string]any{
			"type": "agent_message",
			"text": strings.TrimSpace(string(output)),
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	content := strings.Join([]string{
		`{"type":"thread.started","thread_id":"fixture"}`,
		`{"type":"turn.started"}`,
		string(message),
		`{"type":"turn.completed","usage":{"input_tokens":10,"cached_input_tokens":0,"output_tokens":2,"reasoning_output_tokens":1}}`,
	}, "\n")
	if err := os.WriteFile(path, []byte(content+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func assertQualityCheckJudgeAggregate(
	t *testing.T,
	runDir string,
	wantJudges int,
	wantUsage int,
	wantNotWorse bool,
) {
	t.Helper()
	var judges struct {
		JudgeRuns  []json.RawMessage `json:"judge_runs"`
		Candidates []struct {
			JudgeCount  int  `json:"judge_count"`
			AllNotWorse bool `json:"all_not_worse"`
		} `json:"candidates"`
	}
	content, err := os.ReadFile(filepath.Join(runDir, "quality", "judges.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(content, &judges); err != nil {
		t.Fatal(err)
	}
	if len(judges.JudgeRuns) != wantJudges ||
		(wantJudges == 0 && len(judges.Candidates) != 0) ||
		(wantJudges > 0 &&
			(len(judges.Candidates) != 1 ||
				judges.Candidates[0].JudgeCount != wantJudges ||
				judges.Candidates[0].AllNotWorse != wantNotWorse)) {
		t.Fatalf("judge aggregate = %+v, runs = %d", judges.Candidates, len(judges.JudgeRuns))
	}

	var usage struct {
		Totals struct {
			RunCount int `json:"run_count"`
		} `json:"totals"`
	}
	content, err = os.ReadFile(filepath.Join(runDir, "quality", "judge-usage.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(content, &usage); err != nil {
		t.Fatal(err)
	}
	if usage.Totals.RunCount != wantUsage {
		t.Fatalf("judge usage run_count = %d, want %d", usage.Totals.RunCount, wantUsage)
	}
}
