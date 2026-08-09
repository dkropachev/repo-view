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

func TestQualityCheckRequiresExactUntruncatedSimpleContract(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile(filepath.Join(
		repoRoot, "experiments", "lsp-replacement", "quality-check.sh",
	))
	if err != nil {
		t.Fatal(err)
	}
	body := string(script)
	for _, required := range []string{
		"repo_view_simple_changed_command_exact",
		"repo_view_simple_core_inspect_command_exact",
		"repo_view_simple_consumer_inspect_command_exact",
		"repo_view_simple_inspect_outputs_untruncated",
		"$simple_changed_exact",
		"$simple_core_exact",
		"$simple_consumer_exact",
		"$simple_untruncated",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("quality-check simple pre-gate is missing %q", required)
		}
	}
}

func TestQualityCheckRequiresExactVerifiedDeepCommandSequence(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile(filepath.Join(
		repoRoot, "experiments", "lsp-replacement", "quality-check.sh",
	))
	if err != nil {
		t.Fatal(err)
	}
	body := string(script)
	for _, required := range []string{
		"repo_view_deep_command_sequence_exact",
		"repo_view_deep_dependency_awk_exact",
		"$deep_sequence_exact",
		"$deep_dependency_exact",
		"$repo_view_calls == 8",
		"$find_calls == 2",
		"$inspect_calls == 4",
		"$outline_calls == 1",
	} {
		if !strings.Contains(body, required) {
			t.Errorf("quality-check deep pre-gate is missing %q", required)
		}
	}
}

func TestQualityCheckUsesJQMultilineRegexSemantics(t *testing.T) {
	jqPath, err := exec.LookPath("jq")
	if err != nil {
		t.Skipf("quality-check regex test requires jq: %v", err)
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	script, err := os.ReadFile(filepath.Join(
		repoRoot, "experiments", "lsp-replacement", "quality-check.sh",
	))
	if err != nil {
		t.Fatal(err)
	}
	const multilineCall = `test($pattern; "im")`
	if got := strings.Count(string(script), multilineCall); got != 2 {
		t.Fatalf("quality-check must use jq multiline matching for required and prohibited patterns; found %d calls", got)
	}
	if strings.Contains(string(script), `test($pattern; "is")`) {
		t.Fatal("quality-check still uses jq's single-line-anchor mode instead of multiline matching")
	}

	type criterion struct {
		ID     string   `json:"id"`
		AllOf  []string `json:"all_of"`
		NoneOf []string `json:"none_of"`
	}
	var rubric struct {
		Tasks map[string]struct {
			Criteria []criterion `json:"criteria"`
		} `json:"tasks"`
	}
	rubricJSON, err := os.ReadFile(filepath.Join(
		repoRoot, "experiments", "lsp-replacement", "quality-rubric.json",
	))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(rubricJSON, &rubric); err != nil {
		t.Fatal(err)
	}
	criterionByID := func(id string) criterion {
		t.Helper()
		for _, candidate := range rubric.Tasks["deep-explain"].Criteria {
			if candidate.ID == id {
				return candidate
			}
		}
		t.Fatalf("deep-explain rubric is missing criterion %q", id)
		return criterion{}
	}
	testCoverage := criterionByID("test_coverage_matrix")
	productionPaths := criterionByID("production_paths")
	clockSemantics := criterionByID("clock_semantics")
	concreteType := criterionByID("concrete_type_risk")
	if len(testCoverage.AllOf) == 0 || len(productionPaths.NoneOf) == 0 ||
		len(clockSemantics.NoneOf) == 0 || len(concreteType.AllOf) == 0 {
		t.Fatal("deep-explain rubric patterns required by this test are empty")
	}

	jqMatches := func(answer, pattern, flags string) bool {
		t.Helper()
		output, err := exec.Command(
			jqPath, "-n", "--arg", "answer", answer, "--arg", "pattern", pattern,
			"--arg", "flags", flags, `$answer | test($pattern; $flags)`,
		).CombinedOutput()
		if err != nil {
			t.Fatalf("jq regex evaluation failed: %v\n%s", err, output)
		}
		return strings.TrimSpace(string(output)) == "true"
	}

	tests := []struct {
		name          string
		answer        string
		pattern       string
		wantMultiline bool
		wantOldMode   bool
	}{
		{
			name:          "required pattern crosses a Markdown line break",
			answer:        "no explicit comparison was added\nbetween wall-clock and monotonic test coverage",
			pattern:       testCoverage.AllOf[len(testCoverage.AllOf)-1],
			wantMultiline: true,
			wantOldMode:   false,
		},
		{
			name: "current missing-coverage wording remains accepted",
			answer: "Missing coverage includes:\n\n" +
				"- a successful delayed RateLimiterImpl.ReserveN case;\n" +
				"- direct old-versus-new zero-argument Delay and Cancel " +
				"behavior with a monotonic-bearing ReserveN timestamp;",
			pattern:       testCoverage.AllOf[len(testCoverage.AllOf)-1],
			wantMultiline: true,
			wantOldMode:   false,
		},
		{
			name: "repository-external compatibility wording remains accepted",
			answer: "Repository-external compatibility risk is limited to callers " +
				"that inspect the runtime dynamic type through a type assertion.",
			pattern:       concreteType.AllOf[len(concreteType.AllOf)-1],
			wantMultiline: true,
			wantOldMode:   true,
		},
		{
			name:          "prohibited pattern crosses a Markdown line break",
			answer:        "selected adapter path\ntherefore does not receive native ReserveN",
			pattern:       productionPaths.NoneOf[0],
			wantMultiline: true,
			wantOldMode:   false,
		},
		{
			name:          "explicit newline bound remains enforced",
			answer:        "UTC\nwithout stripping monotonic data",
			pattern:       clockSemantics.NoneOf[len(clockSemantics.NoneOf)-1],
			wantMultiline: false,
			wantOldMode:   false,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := jqMatches(test.answer, test.pattern, "im"); got != test.wantMultiline {
				t.Fatalf("jq multiline result = %t, want %t", got, test.wantMultiline)
			}
			if got := jqMatches(test.answer, test.pattern, "is"); got != test.wantOldMode {
				t.Fatalf("jq old-mode result = %t, want %t", got, test.wantOldMode)
			}
		})
	}
}

func TestCanonicalJSONDigestMatchesJQForUnicodeSeparators(t *testing.T) {
	jqPath, err := exec.LookPath("jq")
	if err != nil {
		t.Skipf("canonical JSON digest test requires jq: %v", err)
	}
	const document = `{"root":"/tmp/source","separators":"line\u2028paragraph\u2029end","literal":"\\u2028 and \\u2029"}`
	const filter = `
def normalize_roots:
  if type == "object" then
    with_entries(.value |= normalize_roots)
    | if has("root") then .root = "<target-root>" else . end
  elif type == "array" then
    map(normalize_roots)
  else
    .
  end;
normalize_roots
`
	command := exec.Command(jqPath, "-cS", filter)
	command.Stdin = strings.NewReader(document)
	canonical, err := command.Output()
	if err != nil {
		t.Fatalf("jq canonicalization failed: %v", err)
	}
	for _, separator := range []string{"\u2028", "\u2029"} {
		if !strings.Contains(string(canonical), separator) {
			t.Fatalf("jq output omits literal separator %U: %q", []rune(separator)[0], canonical)
		}
	}
	if got, want := canonicalJSONDigest([]byte(document), true), sha256Bytes(canonical); got != want {
		t.Fatalf("Go canonical digest = %s, jq digest = %s; jq=%q", got, want, canonical)
	}
}

func TestQualityCheckJudgePromptAndTaskPromptDigestBinding(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	scriptBytes, err := os.ReadFile(filepath.Join(
		repoRoot, "experiments", "lsp-replacement", "quality-check.sh",
	))
	if err != nil {
		t.Fatal(err)
	}
	script := string(scriptBytes)
	for _, required := range []string{
		"judge_cache_schema=8",
		"legacy_judge_attestation_schema=2",
		"Read the shared task prompt at ${prompt_task_prompt}",
		"baseline's exact user prompt at ${prompt_baseline_user_prompt}",
		"each candidate's exact user prompt, answer, transcript, and changed packet",
		"Each case's exact user prompt governs only that case's task adherence",
		"Never apply an optimized profile's navigation constraints",
		"never against the baseline's length or exploratory breadth",
		"deeper call-chain tracing beyond an accurately stated evidence boundary",
		"unless the shared task prompt or rubric expressly requires a proven consuming chain",
	} {
		if !strings.Contains(script, required) {
			t.Fatalf("quality-check judge prompt is missing %q", required)
		}
	}

	functionBody := func(name, next string) string {
		t.Helper()
		startMarker := name + "() {"
		start := strings.Index(script, startMarker)
		if start < 0 {
			t.Fatalf("quality-check is missing %s", startMarker)
		}
		end := strings.Index(script[start+len(startMarker):], "\n"+next+"() {")
		if end < 0 {
			t.Fatalf("quality-check is missing function after %s: %s", name, next)
		}
		return script[start : start+len(startMarker)+end]
	}
	for _, function := range []struct {
		name string
		next string
	}{
		{name: "judge_input_digest", next: "legacy_judge_input_digest"},
		{name: "legacy_judge_input_digest", next: "judge_log_valid"},
	} {
		body := functionBody(function.name, function.next)
		for _, required := range []string{
			`local task_prompt_file="$9"`,
			`local baseline_user_prompt_file="${10}"`,
			`if (( $# % 4 != 0 )); then`,
			`input_hash="$(file_digest "${task_prompt_file}")"`,
			`printf 'file-input\0task-prompt\0%s\0' "${input_hash}"`,
			`input_hash="$(file_digest "${baseline_user_prompt_file}")"`,
			`printf 'file-input\0baseline-user-prompt\0%s\0' "${input_hash}"`,
			`input_hash="$(file_digest "${candidate_user_prompt_file}")"`,
			`printf 'file-input\0candidate-user-prompt-%s\0%s\0'`,
		} {
			if !strings.Contains(body, required) {
				t.Fatalf("%s does not bind the shared task prompt with %q", function.name, required)
			}
		}
	}
}

func TestQualityCheckLegacyJudgeBindingRequiresExactPromptArtifacts(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(
		repoRoot, "experiments", "lsp-replacement", "quality-check.sh",
	))
	if err != nil {
		t.Fatal(err)
	}
	script := string(content)
	for _, required := range []string{
		"legacy_prompt_bindings_valid=false",
		`if "${bind_legacy_judges}" &&`,
		"legacy judge binding requires exact prompt bindings",
		`if "${manifest_valid}" || "${legacy_prompt_bindings_valid}"; then`,
		"legacy shared prompt is missing or disagrees with manifest",
		"legacy case prompt is missing or disagrees with manifest",
	} {
		if !strings.Contains(script, required) {
			t.Errorf("quality-check legacy prompt binding is missing %q", required)
		}
	}
}

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
		"root":               targetRoot,
		"head_commit":        head,
		"base_commit":        head,
		"unicode_separators": "line\u2028paragraph\u2029end",
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
	invocationCountPath := filepath.Join(runDir, "codex-invocation-count")
	if err := os.WriteFile(invocationCountPath, []byte("0\n"), 0o644); err != nil {
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
invocation_count="$(sed -n '1p' "$fixture_root/codex-invocation-count")"
invocation_count=$((invocation_count + 1))
printf '%s\n' "$invocation_count" > "$fixture_root/codex-invocation-count"
if [ -n "${OPENAI_API_KEY+x}${CODEX_HOSTILE+x}${HTTPS_PROXY+x}${RUST_LOG+x}" ]; then
  exit 86
fi
if [ "${1:-}" = "--version" ]; then
  printf '%s\n' 'codex-cli 0.144.0'
  exit 0
fi
output=
checkout=
prompt=
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
    prompt="$1"
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
if [ -z "$prompt" ]; then
  exit 92
fi
printf '%s\n' "$prompt" > "$fixture_root/judge-prompt"
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
			strings.HasPrefix(variable, "LSP_JUDGE_MODEL=") ||
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
	rewriteTaskPrompt := func(content string) {
		t.Helper()
		promptContent := []byte(content)
		if err := os.WriteFile(
			filepath.Join(runDir, "prompts", "explain.txt"),
			promptContent,
			0o644,
		); err != nil {
			t.Fatal(err)
		}

		manifestPath := filepath.Join(runDir, "manifest.json")
		manifestContent, err := os.ReadFile(manifestPath)
		if err != nil {
			t.Fatal(err)
		}
		var manifest map[string]any
		if err := json.Unmarshal(manifestContent, &manifest); err != nil {
			t.Fatal(err)
		}
		promptDigests, ok := manifest["prompt_digests"].(map[string]any)
		if !ok {
			t.Fatal("fixture manifest is missing prompt_digests")
		}
		promptDigests["explain"] = sha256Bytes(promptContent)

		configPath := filepath.Join(runDir, "generation-config.json")
		configContent, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatal(err)
		}
		var config map[string]any
		if err := json.Unmarshal(configContent, &config); err != nil {
			t.Fatal(err)
		}
		config["prompt_digests"] = promptDigests
		configContent, err = json.Marshal(config)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(configPath, configContent, 0o644); err != nil {
			t.Fatal(err)
		}
		manifest["generation_config_sha256"] = sha256Bytes(configContent)
		manifestContent, err = json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(manifestPath, manifestContent, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	rewriteCasePrompt := func(caseName, content string) {
		t.Helper()
		manifestPath := filepath.Join(runDir, "manifest.json")
		manifestContent, err := os.ReadFile(manifestPath)
		if err != nil {
			t.Fatal(err)
		}
		var manifest map[string]any
		if err := json.Unmarshal(manifestContent, &manifest); err != nil {
			t.Fatal(err)
		}
		casePromptFiles, ok := manifest["case_prompt_files"].(map[string]any)
		if !ok {
			t.Fatal("fixture manifest is missing case_prompt_files")
		}
		relative, ok := casePromptFiles[caseName].(string)
		if !ok || relative == "" {
			t.Fatalf("fixture manifest is missing case prompt %q", caseName)
		}
		promptContent := []byte(content)
		if err := os.WriteFile(
			filepath.Join(runDir, relative), promptContent, 0o644,
		); err != nil {
			t.Fatal(err)
		}
		casePromptDigests, ok := manifest["case_prompt_digests"].(map[string]any)
		if !ok {
			t.Fatal("fixture manifest is missing case_prompt_digests")
		}
		casePromptDigests[caseName] = sha256Bytes(promptContent)

		configPath := filepath.Join(runDir, "generation-config.json")
		configContent, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatal(err)
		}
		var config map[string]any
		if err := json.Unmarshal(configContent, &config); err != nil {
			t.Fatal(err)
		}
		config["case_prompt_files"] = casePromptFiles
		config["case_prompt_digests"] = casePromptDigests
		configContent, err = json.Marshal(config)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(configPath, configContent, 0o644); err != nil {
			t.Fatal(err)
		}
		manifest["generation_config_sha256"] = sha256Bytes(configContent)
		manifestContent, err = json.Marshal(manifest)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(manifestPath, manifestContent, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	codexInvocationCount := func() string {
		t.Helper()
		content, err := os.ReadFile(invocationCountPath)
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
	renderedJudgePrompt, err := os.ReadFile(filepath.Join(runDir, "judge-prompt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"Read the shared task prompt at ",
		"/prompts/explain.txt",
		"/baseline-explain.user-prompt.txt",
		"/optimized-explain.user-prompt.txt",
		"baseline's exact user prompt",
		"Each case's exact user prompt governs only that case's task adherence",
		"never against the baseline's length or exploratory breadth",
	} {
		if !strings.Contains(string(renderedJudgePrompt), required) {
			t.Fatalf("rendered judge prompt is missing %q:\n%s", required, renderedJudgePrompt)
		}
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
	qualityDir := filepath.Join(runDir, "quality")
	inputsPath := filepath.Join(qualityDir, "inputs.json")
	aggregateQualityPath := filepath.Join(qualityDir, "quality.json")
	markerPath := filepath.Join(qualityDir, "aggregate-manifest.json")
	originalInputs, err := os.ReadFile(inputsPath)
	if err != nil {
		t.Fatal(err)
	}
	originalQuality, err := os.ReadFile(aggregateQualityPath)
	if err != nil {
		t.Fatal(err)
	}
	originalMarker, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	var emptyVerdictQuality map[string]any
	var emptyVerdictMarker map[string]any
	if json.Unmarshal(originalQuality, &emptyVerdictQuality) != nil ||
		json.Unmarshal(originalMarker, &emptyVerdictMarker) != nil {
		t.Fatal("decode verdict integrity fixture")
	}
	emptyVerdictQuality["verdicts"] = []any{}
	writeJSON(t, aggregateQualityPath, emptyVerdictQuality)
	emptyQualityDigest, err := sha256File(aggregateQualityPath)
	if err != nil {
		t.Fatal(err)
	}
	emptyVerdictMarker["files"].(map[string]any)["quality.json"] =
		emptyQualityDigest
	writeJSON(t, markerPath, emptyVerdictMarker)
	if _, err := SummarizeEvidence(runDir, nil); err == nil ||
		!strings.Contains(err.Error(), "verdicts disagree") {
		t.Fatalf("empty quality verdicts error = %v", err)
	}
	if err := os.WriteFile(aggregateQualityPath, originalQuality, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(markerPath, originalMarker, 0o644); err != nil {
		t.Fatal(err)
	}
	staticPath := filepath.Join(qualityDir, "static.json")
	originalStatic, err := os.ReadFile(staticPath)
	if err != nil {
		t.Fatal(err)
	}
	var flippedStatic map[string]any
	var flippedQuality map[string]any
	var flippedMarker map[string]any
	if json.Unmarshal(originalStatic, &flippedStatic) != nil ||
		json.Unmarshal(originalQuality, &flippedQuality) != nil ||
		json.Unmarshal(originalMarker, &flippedMarker) != nil {
		t.Fatal("decode coherent static integrity fixture")
	}
	for _, rawCase := range flippedStatic["cases"].([]any) {
		current := rawCase.(map[string]any)
		if current["variant"] == "optimized" {
			current["accounting_pass"] = false
			current["navigation_pass"] = false
			current["required_pass"] = false
		}
	}
	for _, rawComparison := range flippedStatic["comparisons"].([]any) {
		comparison := rawComparison.(map[string]any)
		comparison["accounting_pass"] = false
		comparison["navigation_pass"] = false
		comparison["required_pass"] = false
		comparison["static_not_worse"] = false
	}
	flippedQuality["static"] = flippedStatic
	for _, rawVerdict := range flippedQuality["verdicts"].([]any) {
		verdict := rawVerdict.(map[string]any)
		verdict["accounting_pass"] = false
		verdict["navigation_pass"] = false
		verdict["static_not_worse"] = false
		verdict["quality_pass"] = false
	}
	writeJSON(t, staticPath, flippedStatic)
	writeJSON(t, aggregateQualityPath, flippedQuality)
	for name, path := range map[string]string{
		"static.json": staticPath, "quality.json": aggregateQualityPath,
	} {
		digest, err := sha256File(path)
		if err != nil {
			t.Fatal(err)
		}
		flippedMarker["files"].(map[string]any)[name] = digest
	}
	writeJSON(t, markerPath, flippedMarker)
	if _, err := SummarizeEvidence(runDir, nil); err == nil ||
		!strings.Contains(err.Error(), "static.json cases disagree") {
		t.Fatalf("coherently flipped static aggregate error = %v", err)
	}
	for path, content := range map[string][]byte{
		staticPath: originalStatic, aggregateQualityPath: originalQuality,
		markerPath: originalMarker,
	} {
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var fabricatedInputs map[string]any
	var fabricatedQuality map[string]any
	var fabricatedMarker map[string]any
	if json.Unmarshal(originalInputs, &fabricatedInputs) != nil ||
		json.Unmarshal(originalQuality, &fabricatedQuality) != nil ||
		json.Unmarshal(originalMarker, &fabricatedMarker) != nil {
		t.Fatal("decode strict aggregate fixture")
	}
	fabricatedInputs["validation"].(map[string]any)["judge_repeats"] = 0
	fabricatedInputs["validation"].(map[string]any)["enforce"] = true
	fabricatedQuality["required_judge_count"] = 0
	for _, rawVerdict := range fabricatedQuality["verdicts"].([]any) {
		verdict := rawVerdict.(map[string]any)
		verdict["required_judge_count"] = 0
		verdict["judge_complete"] = true
	}
	writeJSON(t, inputsPath, fabricatedInputs)
	writeJSON(t, aggregateQualityPath, fabricatedQuality)
	files := fabricatedMarker["files"].(map[string]any)
	for name, path := range map[string]string{
		"inputs.json":  inputsPath,
		"quality.json": aggregateQualityPath,
	} {
		digest, err := sha256File(path)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = digest
	}
	writeJSON(t, markerPath, fabricatedMarker)
	if _, err := SummarizeEvidence(runDir, nil); err == nil ||
		!strings.Contains(err.Error(), "judge_repeats=0") {
		t.Fatalf("fabricated zero-repeat strict aggregate error = %v", err)
	}
	for path, content := range map[string][]byte{
		inputsPath: originalInputs, aggregateQualityPath: originalQuality, markerPath: originalMarker,
	} {
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	runQualityCheck("1")
	if got := codexCount(); got != "1" {
		t.Fatalf("unchanged inputs executed codex again: count = %s", got)
	}
	beforeReuseInvocations := codexInvocationCount()
	runQualityCheck("1", "--reuse-judges-only")
	if got := codexCount(); got != "1" {
		t.Fatalf("reuse-only executed codex: count = %s", got)
	}
	if got := codexInvocationCount(); got != beforeReuseInvocations {
		t.Fatalf("reuse-only invoked codex (including --version): %s -> %s", beforeReuseInvocations, got)
	}
	resultSidecar := filepath.Join(
		runDir,
		"quality",
		"judge-explain-1.result.sha256",
	)
	validResultSidecar, err := os.ReadFile(resultSidecar)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resultSidecar, []byte(strings.Repeat("0", 64)+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	command = qualityCheckCommand("1", "--reuse-judges-only")
	output, err = command.CombinedOutput()
	if err == nil || !strings.Contains(
		string(output),
		"required reusable judge artifact is missing or invalid",
	) {
		t.Fatalf("reuse-only accepted invalid sidecar: %v\n%s", err, output)
	}
	if got := codexCount(); got != "1" {
		t.Fatalf("failed reuse-only executed codex: count = %s", got)
	}
	if got := codexInvocationCount(); got != beforeReuseInvocations {
		t.Fatalf("failed reuse-only invoked codex: %s -> %s", beforeReuseInvocations, got)
	}
	if err := os.WriteFile(resultSidecar, validResultSidecar, 0o644); err != nil {
		t.Fatal(err)
	}
	runQualityCheck("01")
	if got := codexCount(); got != "1" {
		t.Fatalf("base-10 repeat count missed the cache: count = %s", got)
	}
	firstPromptDigest, err := os.ReadFile(filepath.Join(
		runDir, "quality", "judge-explain-1.inputs.sha256",
	))
	if err != nil {
		t.Fatal(err)
	}
	rewriteTaskPrompt("fixture rendered prompt for explain, revision 2")
	runQualityCheck("1")
	if got := codexCount(); got != "2" {
		t.Fatalf("changed task prompt reused cached judge: count = %s, want 2", got)
	}
	secondPromptDigest, err := os.ReadFile(filepath.Join(
		runDir, "quality", "judge-explain-1.inputs.sha256",
	))
	if err != nil {
		t.Fatal(err)
	}
	if string(firstPromptDigest) == string(secondPromptDigest) {
		t.Fatal("changed task prompt retained the same judge input digest")
	}
	runQualityCheck("1")
	if got := codexCount(); got != "2" {
		t.Fatalf("unchanged revised task prompt missed cache: count = %s", got)
	}
	runQualityCheck("2")
	if got := codexCount(); got != "3" {
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
		if got := codexCount(); got != "3" {
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
	if got := codexCount(); got != "4" {
		t.Fatalf("changed answer reused cached judge: count = %s, want 4", got)
	}
	assertQualityCheckJudgeAggregate(t, runDir, 1, 1, true)

	runQualityCheck("0")
	if got := codexCount(); got != "4" {
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
	if _, err := SummarizeEvidence(runDir, nil); err != nil {
		t.Fatalf("legacy attested aggregate is not consumable: %v", err)
	}
	legacyAttestationPath := filepath.Join(
		runDir,
		"quality",
		"judge-explain-10.legacy-attestation.json",
	)
	legacyInputsPath := filepath.Join(runDir, "quality", "inputs.json")
	legacyMarkerPath := filepath.Join(runDir, "quality", "aggregate-manifest.json")
	originalLegacyAttestation, err := os.ReadFile(legacyAttestationPath)
	if err != nil {
		t.Fatal(err)
	}
	originalLegacyInputs, err := os.ReadFile(legacyInputsPath)
	if err != nil {
		t.Fatal(err)
	}
	originalLegacyMarker, err := os.ReadFile(legacyMarkerPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyAttestationPath, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	var rewrittenLegacyInputs map[string]any
	var rewrittenLegacyMarker map[string]any
	if json.Unmarshal(originalLegacyInputs, &rewrittenLegacyInputs) != nil ||
		json.Unmarshal(originalLegacyMarker, &rewrittenLegacyMarker) != nil {
		t.Fatal("decode legacy aggregate commitments")
	}
	attestationDigest, err := sha256File(legacyAttestationPath)
	if err != nil {
		t.Fatal(err)
	}
	rewrittenLegacyInputs["inputs"].(map[string]any)["quality/judge-explain-10.legacy-attestation.json"] = attestationDigest
	rewrittenLegacyInputs["snapshots"].(map[string]any)["judges/judge-explain-10.legacy-attestation.json"] = attestationDigest
	writeJSON(t, legacyInputsPath, rewrittenLegacyInputs)
	rewrittenInputsDigest, err := sha256File(legacyInputsPath)
	if err != nil {
		t.Fatal(err)
	}
	rewrittenLegacyMarker["files"].(map[string]any)["inputs.json"] =
		rewrittenInputsDigest
	writeJSON(t, legacyMarkerPath, rewrittenLegacyMarker)
	if _, err := SummarizeEvidence(runDir, nil); err == nil ||
		!strings.Contains(err.Error(), "invalid attestation schema") {
		t.Fatalf("empty legacy attestation error = %v", err)
	}
	for path, content := range map[string][]byte{
		legacyAttestationPath: originalLegacyAttestation,
		legacyInputsPath:      originalLegacyInputs,
		legacyMarkerPath:      originalLegacyMarker,
	} {
		if err := os.WriteFile(path, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
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

	rewriteTaskPrompt("fixture rendered prompt for explain, revision 3")
	runQualityCheck("0")
	assertQualityCheckJudgeAggregate(t, runDir, 0, 0, false)
	command = qualityCheckCommand("0", "--bind-legacy-judges")
	output, err = command.CombinedOutput()
	if err == nil ||
		!strings.Contains(
			string(output),
			"refusing to overwrite mismatched legacy judge attestation",
		) {
		t.Fatalf("legacy binding ignored changed task prompt: %v\n%s", err, output)
	}
	rewriteTaskPrompt("fixture rendered prompt for explain, revision 2")
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
	if got := codexCount(); got != "7" {
		t.Fatalf("failed judge attempt count = %s, want 7", got)
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
	if got := codexCount(); got != "8" {
		t.Fatalf("successful retry count = %s, want 8", got)
	}
	assertQualityCheckJudgeAggregate(t, runDir, 1, 1, true)

	casePromptDigest, err := os.ReadFile(filepath.Join(
		runDir, "quality", "judge-explain-1.inputs.sha256",
	))
	if err != nil {
		t.Fatal(err)
	}
	rewriteCasePrompt("baseline-explain", "exact baseline prompt revision 2")
	runQualityCheck("1")
	if got := codexCount(); got != "9" {
		t.Fatalf("changed baseline prompt reused cached judge: count = %s, want 9", got)
	}
	baselinePromptDigest, err := os.ReadFile(filepath.Join(
		runDir, "quality", "judge-explain-1.inputs.sha256",
	))
	if err != nil {
		t.Fatal(err)
	}
	if string(casePromptDigest) == string(baselinePromptDigest) {
		t.Fatal("changed baseline prompt retained the same judge input digest")
	}
	rewriteCasePrompt("optimized-explain", "exact candidate prompt revision 2")
	runQualityCheck("1")
	if got := codexCount(); got != "10" {
		t.Fatalf("changed candidate prompt reused cached judge: count = %s, want 10", got)
	}
	candidatePromptDigest, err := os.ReadFile(filepath.Join(
		runDir, "quality", "judge-explain-1.inputs.sha256",
	))
	if err != nil {
		t.Fatal(err)
	}
	if string(baselinePromptDigest) == string(candidatePromptDigest) {
		t.Fatal("changed candidate prompt retained the same judge input digest")
	}
	runQualityCheck("1")
	if got := codexCount(); got != "10" {
		t.Fatalf("unchanged exact case prompts missed cache: count = %s", got)
	}

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
	t.Setenv("LSP_JUDGE_MODEL", "custom-judge")
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
		"router",
	)
	output, err = command.CombinedOutput()
	if err == nil {
		t.Fatalf("quality-check silently ignored a configured judge model:\n%s", output)
	}
	if got := string(output); got !=
		"LSP_JUDGE_MODEL requires --model-mode pinned; router mode configures no model\n" {
		t.Fatalf("router judge model output = %q", got)
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

	for _, testCase := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{
			name: "repository read bypass count",
			mutate: func(current map[string]any) {
				current["repository_read_bypass_command_count"] = 1
			},
		},
		{
			name: "repository read bypass provenance",
			mutate: func(current map[string]any) {
				current["repository_read_bypass_command_count"] = 1
				current["repository_read_bypass_commands"] = []any{"awk go.mod"}
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			runDir := t.TempDir()
			current := qualityCheckMetricCase(
				"baseline-explain", "explain", "baseline", "baseline",
			)
			testCase.mutate(current)
			writeJSON(t, filepath.Join(runDir, "metrics.json"), map[string]any{
				"cases": []any{current},
			})
			command := exec.Command(
				bashPath, qualityCheck, runDir, "--skip-analyze",
			)
			output, err := command.CombinedOutput()
			if err == nil || !strings.Contains(string(output), "invalid metrics.json") {
				t.Fatalf("bypass accounting result = %v\n%s", err, output)
			}
		})
	}

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
	t.Run("baseline-only provenance may feed optimized run", func(t *testing.T) {
		runDir := newStrictRun(t)
		manifestPath := filepath.Join(runDir, "manifest.json")
		var manifest map[string]any
		manifestContent, err := os.ReadFile(manifestPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(manifestContent, &manifest); err != nil {
			t.Fatal(err)
		}
		manifest["variant_selection"] = "optimized"
		manifest["baseline_from"] = "baseline-only"
		writeJSON(t, manifestPath, manifest)

		configPath := filepath.Join(runDir, "generation-config.json")
		configContent, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatal(err)
		}
		var sourceConfig map[string]any
		if err := json.Unmarshal(configContent, &sourceConfig); err != nil {
			t.Fatal(err)
		}
		sourceCaseFiles := map[string]any{}
		sourceCaseDigests := map[string]any{}
		for name, relative := range manifest["case_prompt_files"].(map[string]any) {
			if strings.HasPrefix(name, "baseline-") {
				sourceCaseFiles[name] = relative
			}
		}
		for name, digest := range manifest["case_prompt_digests"].(map[string]any) {
			if strings.HasPrefix(name, "baseline-") {
				sourceCaseDigests[name] = digest
			}
		}
		sourceConfig["case_prompt_files"] = sourceCaseFiles
		sourceConfig["case_prompt_digests"] = sourceCaseDigests
		sourceConfig["mechanical_navigation_semantics_enforced"] = false
		sourceConfigContent, err := json.Marshal(sourceConfig)
		if err != nil {
			t.Fatal(err)
		}
		sourceManifest := make(map[string]any, len(manifest))
		for key, value := range manifest {
			sourceManifest[key] = value
		}
		sourceManifest["variant_selection"] = "baseline"
		sourceManifest["baseline_from"] = nil
		sourceManifest["case_prompt_files"] = sourceCaseFiles
		sourceManifest["case_prompt_digests"] = sourceCaseDigests
		sourceManifest["mechanical_navigation_semantics_enforced"] = false
		sourceManifest["generation_config_sha256"] =
			sha256Bytes(sourceConfigContent)
		writeJSON(
			t,
			filepath.Join(runDir, "baseline-source-manifest.json"),
			sourceManifest,
		)
		if err := os.WriteFile(
			filepath.Join(runDir, "baseline-source-generation-config.json"),
			sourceConfigContent,
			0o644,
		); err != nil {
			t.Fatal(err)
		}
		copyEvidenceFile := func(source, destination string) {
			t.Helper()
			content, readErr := os.ReadFile(source)
			if readErr != nil {
				t.Fatal(readErr)
			}
			if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(destination, content, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		copyEvidenceFile(
			filepath.Join(runDir, "profiles-snapshot.tsv"),
			filepath.Join(runDir, "baseline-source-profiles-snapshot.tsv"),
		)
		copyEvidenceFile(
			filepath.Join(runDir, "prompts", "explain.txt"),
			filepath.Join(runDir, "baseline-source-prompts", "explain.txt"),
		)
		copyEvidenceFile(
			filepath.Join(runDir, "baseline-explain.user-prompt.txt"),
			filepath.Join(
				runDir,
				"baseline-source-baseline-explain.user-prompt.txt",
			),
		)

		command := exec.Command(
			bashPath, qualityCheck, runDir, "--skip-analyze", "--enforce",
		)
		output, err := command.CombinedOutput()
		if err == nil || !strings.Contains(string(output), "# Quality Confirmation") {
			t.Fatalf("optimized import result = %v\n%s", err, output)
		}
		if strings.Contains(string(output), "imported baseline") {
			t.Fatalf("baseline-only import rejected structurally:\n%s", output)
		}
	})
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
		baselineUserPrompt, err := os.ReadFile(filepath.Join(
			runDir, "baseline-explain.user-prompt.txt",
		))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(
				runDir,
				"baseline-source-baseline-explain.user-prompt.txt",
			),
			baselineUserPrompt,
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

	t.Run("baseline-only import permits mechanical marker delta", func(t *testing.T) {
		runDir := newStrictRun(t)
		manifestPath := filepath.Join(runDir, "manifest.json")
		manifestContent, err := os.ReadFile(manifestPath)
		if err != nil {
			t.Fatal(err)
		}
		var currentManifest map[string]any
		if err := json.Unmarshal(manifestContent, &currentManifest); err != nil {
			t.Fatal(err)
		}
		currentManifest["variant_selection"] = "optimized"
		currentManifest["baseline_from"] = "baseline-only"
		writeJSON(t, manifestPath, currentManifest)

		currentConfigPath := filepath.Join(runDir, "generation-config.json")
		currentConfigContent, err := os.ReadFile(currentConfigPath)
		if err != nil {
			t.Fatal(err)
		}
		var baselineConfig map[string]any
		if err := json.Unmarshal(currentConfigContent, &baselineConfig); err != nil {
			t.Fatal(err)
		}
		sourceCaseFiles := make(map[string]any)
		sourceCaseDigests := make(map[string]any)
		for name, relative := range currentManifest["case_prompt_files"].(map[string]any) {
			if strings.HasPrefix(name, "baseline-") {
				sourceCaseFiles[name] = relative
			}
		}
		for name, digest := range currentManifest["case_prompt_digests"].(map[string]any) {
			if strings.HasPrefix(name, "baseline-") {
				sourceCaseDigests[name] = digest
			}
		}
		baselineConfig["case_prompt_files"] = sourceCaseFiles
		baselineConfig["case_prompt_digests"] = sourceCaseDigests
		baselineConfig["mechanical_navigation_semantics_enforced"] = false
		baselineConfigContent, err := json.Marshal(baselineConfig)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(runDir, "baseline-source-generation-config.json"),
			baselineConfigContent,
			0o644,
		); err != nil {
			t.Fatal(err)
		}

		baselineManifestContent, err := json.Marshal(currentManifest)
		if err != nil {
			t.Fatal(err)
		}
		var baselineManifest map[string]any
		if err := json.Unmarshal(baselineManifestContent, &baselineManifest); err != nil {
			t.Fatal(err)
		}
		baselineManifest["variant_selection"] = "baseline"
		baselineManifest["baseline_from"] = nil
		baselineManifest["case_prompt_files"] = sourceCaseFiles
		baselineManifest["case_prompt_digests"] = sourceCaseDigests
		baselineManifest["mechanical_navigation_semantics_enforced"] = false
		baselineManifest["generation_config_sha256"] = sha256Bytes(
			baselineConfigContent,
		)
		writeJSON(
			t,
			filepath.Join(runDir, "baseline-source-manifest.json"),
			baselineManifest,
		)

		for source, destination := range map[string]string{
			"profiles-snapshot.tsv":            "baseline-source-profiles-snapshot.tsv",
			"prompts/explain.txt":              "baseline-source-prompts/explain.txt",
			"baseline-explain.user-prompt.txt": "baseline-source-baseline-explain.user-prompt.txt",
		} {
			content, err := os.ReadFile(filepath.Join(runDir, source))
			if err != nil {
				t.Fatal(err)
			}
			destinationPath := filepath.Join(runDir, destination)
			if err := os.MkdirAll(filepath.Dir(destinationPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(destinationPath, content, 0o644); err != nil {
				t.Fatal(err)
			}
		}

		command := exec.Command(
			bashPath,
			qualityCheck,
			runDir,
			"--skip-analyze",
			"--enforce",
			"--judge-repeats", "1",
		)
		output, err := command.CombinedOutput()
		if err == nil || !strings.Contains(
			string(output),
			"pre-judge quality gate failed",
		) {
			t.Fatalf("baseline-only import result = %v\n%s", err, output)
		}
		if strings.Contains(
			string(output),
			"imported baseline generation config disagrees",
		) {
			t.Fatalf("mechanical marker delta was rejected:\n%s", output)
		}

		baselineManifest["model"] = "router-selected"
		baselineManifest["model_mode"] = "router"
		baselineManifest["model_configuration"] = "none"
		writeJSON(
			t,
			filepath.Join(runDir, "baseline-source-manifest.json"),
			baselineManifest,
		)
		command = exec.Command(
			bashPath,
			qualityCheck,
			runDir,
			"--skip-analyze",
			"--enforce",
			"--judge-repeats", "1",
		)
		output, err = command.CombinedOutput()
		if err == nil || !strings.Contains(
			string(output),
			"imported baseline source manifest disagrees",
		) {
			t.Fatalf("model-routing mismatch result = %v\n%s", err, output)
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
	simpleContractValid := variant != "optimized" || strings.HasPrefix(task, "deep-")
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
		"repo_view_simple_changed_command_exact":           simpleContractValid,
		"repo_view_simple_core_inspect_command_exact":      simpleContractValid,
		"repo_view_simple_consumer_inspect_command_exact":  simpleContractValid,
		"repo_view_simple_inspect_outputs_untruncated":     simpleContractValid,
		"repo_view_deep_command_sequence_exact":            true,
		"repo_view_deep_dependency_awk_exact":              true,
		"repo_view_navigation_semantic_violation_commands": []any{},
		"repo_view_budget_tamper_command_count":            0,
		"repo_view_budget_tamper_commands":                 []any{},
		"repository_read_bypass_command_count":             0,
		"repository_read_bypass_commands":                  []any{},
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
	t.Setenv("LSP_JUDGE_MODEL", "")
	t.Setenv("LSP_JUDGE_MODEL_MODE", "")
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

	var quality struct {
		SchemaVersion int `json:"schema_version"`
		Evaluator     struct {
			ModelMode   string `json:"model_mode"`
			Environment struct {
				Filesystem struct {
					CodexExecutable string `json:"codex_executable"`
				} `json:"filesystem"`
			} `json:"environment"`
		} `json:"evaluator"`
	}
	content, err = os.ReadFile(filepath.Join(runDir, "quality", "quality.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(content, &quality); err != nil {
		t.Fatal(err)
	}
	if quality.SchemaVersion != 5 || quality.Evaluator.ModelMode != "router" ||
		quality.Evaluator.Environment.Filesystem.CodexExecutable != "read" {
		t.Fatalf("quality evaluator provenance = %+v", quality)
	}
}
