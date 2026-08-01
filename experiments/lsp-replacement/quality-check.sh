#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C

experiment_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
run_dir=""
judge_repeats=0
judge_repeat_limit=100
enforce=false
bind_legacy_judges=false
skip_analyze=false
judge_model_mode="${LSP_JUDGE_MODEL_MODE:-router}"
judge_model=""
judge_codex_version="codex-cli 0.144.0"
judge_cache_schema=6
legacy_judge_attestation_schema=1
metrics_formula="effective = (input - cached_input) + 0.1 * cached_input + output"
required_generation_isolation="root-deny-explicit-read-inherit-none-go-env-v3"
no_collaboration='Do not call collaboration, subagent, spawn-agent, or agent-wait tools. Do not read or invoke Codex skills, plugins, hooks, or marketplace resources; they are outside this benchmark.'

usage() {
  cat <<'EOF'
Usage: experiments/lsp-replacement/quality-check.sh RUN_DIR [options]

Options:
  --judge-repeats N  Run N independent source-grounded Codex judges (0-100).
  --model-mode MODE  Judge routing mode: router or pinned. Router configures no
                     model and is the default.
  --bind-legacy-judges
                     Trust schema-valid numeric legacy judges and bind them to
                     the current inputs. Valid only with --judge-repeats 0.
  --skip-analyze     Require and reuse an existing metrics.json.
  --enforce          Require every optimized case to pass quality and show a
                     positive effective-token saving.
  -h, --help
EOF
}

if [[ $# -gt 0 && "$1" != -* ]]; then
  run_dir="$1"
  shift
fi
while [[ $# -gt 0 ]]; do
  case "$1" in
    --judge-repeats)
      if [[ $# -lt 2 ]]; then
        printf '%s\n' '--judge-repeats requires a value' >&2
        exit 2
      fi
      judge_repeats="$2"
      shift 2
      ;;
    --model-mode)
      if [[ $# -lt 2 ]]; then
        printf '%s\n' '--model-mode requires a value' >&2
        exit 2
      fi
      judge_model_mode="$2"
      shift 2
      ;;
    --enforce)
      enforce=true
      shift
      ;;
    --bind-legacy-judges)
      bind_legacy_judges=true
      shift
      ;;
    --skip-analyze)
      skip_analyze=true
      shift
      ;;
    -h|--help)
      usage
      exit 0
      ;;
    *)
      printf 'unknown option: %s\n' "$1" >&2
      usage >&2
      exit 2
      ;;
  esac
done

if [[ -z "${run_dir}" ]]; then
  usage >&2
  exit 2
fi
while [[ "${run_dir}" != "/" && "${run_dir}" == */ ]]; do
  run_dir="${run_dir%/}"
done
if [[ -L "${run_dir}" ]]; then
  printf 'run directory must not be a symlink: %s\n' "${run_dir}" >&2
  exit 2
fi
if [[ ! -d "${run_dir}" ]]; then
  printf 'run directory does not exist: %s\n' "${run_dir}" >&2
  exit 2
fi
run_dir="$(cd -P "${run_dir}" && pwd -P)"
run_dir_identity="$(stat -Lc '%d:%i' -- "${run_dir}")"
exec {run_dir_fd}<"${run_dir}"
run_dir_fd_path="/proc/self/fd/${run_dir_fd}"
if [[ "$(stat -Lc '%d:%i' -- "${run_dir_fd_path}")" != \
  "${run_dir_identity}" ]]; then
  printf 'failed to hold run directory identity: %s\n' "${run_dir}" >&2
  exit 1
fi

verify_run_directory() {
  [[ ! -L "${run_dir}" ]] &&
    [[ -d "${run_dir}" ]] &&
    [[ "$(stat -Lc '%d:%i' -- "${run_dir}")" == "${run_dir_identity}" ]] &&
    [[ "$(stat -Lc '%d:%i' -- "${run_dir_fd_path}")" == \
      "${run_dir_identity}" ]]
}

if [[ ! "${judge_repeats}" =~ ^[0-9]+$ ]]; then
  printf '%s\n' '--judge-repeats must be a non-negative integer' >&2
  exit 2
fi
if [[ ${#judge_repeats} -gt 18 ]]; then
  printf '%s\n' "--judge-repeats must be between 0 and ${judge_repeat_limit}" >&2
  exit 2
fi
judge_repeats="$((10#${judge_repeats}))"
if [[ "${judge_repeats}" -gt "${judge_repeat_limit}" ]]; then
  printf '%s\n' "--judge-repeats must be between 0 and ${judge_repeat_limit}" >&2
  exit 2
fi
if "${bind_legacy_judges}" && [[ "${judge_repeats}" -ne 0 ]]; then
  printf '%s\n' '--bind-legacy-judges requires --judge-repeats 0' >&2
  exit 2
fi
if "${bind_legacy_judges}" && "${enforce}"; then
  printf '%s\n' '--bind-legacy-judges cannot be combined with --enforce' >&2
  exit 2
fi
case "${judge_model_mode}" in
  pinned)
    judge_model="${LSP_JUDGE_MODEL:-gpt-5.6-sol}"
    judge_model_args=(-m "${judge_model}")
    judge_reasoning_args=(-c 'model_reasoning_effort="high"')
    judge_model_configuration="model=${judge_model};model-reasoning-effort=high"
    ;;
  router)
    judge_model="router-selected"
    judge_model_args=()
    judge_reasoning_args=()
    judge_model_configuration="routing=router-selected;model-configuration=none"
    ;;
  *)
    printf 'invalid --model-mode: %s\n' "${judge_model_mode}" >&2
    exit 2
    ;;
esac

quality_lock="${run_dir}/.quality-check.lock"
if ! verify_run_directory ||
  ! mkdir "${run_dir_fd_path}/.quality-check.lock" 2>/dev/null; then
  printf 'quality check already running or stale lock exists: %s\n' \
    "${quality_lock}" >&2
  exit 1
fi
quality_lock_identity="$(stat -Lc '%d:%i' "${quality_lock}")"
exec {quality_lock_fd}<"${quality_lock}"
quality_lock_fd_path="/proc/self/fd/${quality_lock_fd}"
if [[ "$(stat -Lc '%d:%i' "${quality_lock_fd_path}")" != \
  "${quality_lock_identity}" ]]; then
  printf 'quality lock identity changed while opening: %s\n' \
    "${quality_lock}" >&2
  exit 1
fi
quality_scratch="${quality_lock}/scratch"
mkdir "${quality_scratch}"
quality_scratch_identity="$(stat -Lc '%d:%i' "${quality_scratch}")"
exec {quality_scratch_fd}<"${quality_scratch}"
quality_scratch_fd_path="/proc/self/fd/${quality_scratch_fd}"
static_cases=""
judge_usage_cases=""
judge_checkout=""
judge_codex_home=""
snapshot_sources=()
snapshot_copies=()
snapshot_identities=()
cleanup() {
  local status="$?"
  trap - EXIT
  set +e
  if [[ -n "${quality_scratch_fd:-}" ]] &&
    [[ -d "${quality_scratch_fd_path}/." ]] &&
    [[ "$(stat -Lc '%d:%i' "${quality_scratch_fd_path}")" == \
      "${quality_scratch_identity}" ]]; then
    find -P "${quality_scratch_fd_path}/." -depth -mindepth 1 -delete \
      >/dev/null 2>&1
  fi
  if [[ -n "${quality_scratch_fd:-}" ]]; then
    exec {quality_scratch_fd}<&-
  fi
  if [[ ! -L "${quality_lock}" &&
    -d "${quality_lock}" &&
    "$(stat -Lc '%d:%i' "${quality_lock}")" == \
      "${quality_lock_identity}" &&
    ! -L "${quality_scratch}" &&
    -d "${quality_scratch}" &&
    "$(stat -Lc '%d:%i' "${quality_scratch}")" == \
      "${quality_scratch_identity}" ]]; then
    rmdir -- "${quality_scratch}" >/dev/null 2>&1
    rmdir -- "${quality_lock}" >/dev/null 2>&1
  fi
  if [[ -n "${quality_dir_fd:-}" ]]; then
    exec {quality_dir_fd}<&-
  fi
  if [[ -n "${quality_lock_fd:-}" ]]; then
    exec {quality_lock_fd}<&-
  fi
  if [[ -n "${run_dir_fd:-}" ]]; then
    exec {run_dir_fd}<&-
  fi
  exit "${status}"
}
trap cleanup EXIT

quality_dir="${run_dir}/quality"
quality_dir_anchored="${run_dir_fd_path}/quality"
if [[ -L "${quality_dir_anchored}" ]] ||
  { [[ -e "${quality_dir_anchored}" ]] &&
    [[ ! -d "${quality_dir_anchored}" ]]; }; then
  printf 'quality output path is not a real directory: %s\n' \
    "${quality_dir}" >&2
  exit 1
fi
if [[ ! -e "${quality_dir_anchored}" ]]; then
  mkdir "${quality_dir_anchored}"
fi
if [[ -L "${quality_dir_anchored}" || ! -d "${quality_dir_anchored}" ]]; then
  printf 'quality output path is not a real directory: %s\n' \
    "${quality_dir}" >&2
  exit 1
fi
quality_dir_identity="$(stat -Lc '%d:%i' "${quality_dir_anchored}")"
exec {quality_dir_fd}<"${quality_dir_anchored}"
quality_dir_fd_path="/proc/self/fd/${quality_dir_fd}"
if [[ "$(stat -Lc '%d:%i' "${quality_dir_fd_path}")" != \
  "${quality_dir_identity}" ]]; then
  printf 'quality output directory changed while opening: %s\n' \
    "${quality_dir}" >&2
  exit 1
fi

verify_quality_directory() {
  [[ ! -L "${quality_dir}" ]] &&
    [[ -d "${quality_dir}" ]] &&
    [[ "$(stat -Lc '%d:%i' "${quality_dir}")" == "${quality_dir_identity}" ]] &&
    [[ "$(stat -Lc '%d:%i' "${quality_dir_fd_path}")" == \
      "${quality_dir_identity}" ]]
}

if [[ ! -f "${run_dir_fd_path}/metrics.json" ]]; then
  if "${skip_analyze}"; then
    printf 'metrics.json does not exist: %s\n' "${run_dir}/metrics.json" >&2
    exit 1
  fi
  if ! verify_run_directory; then
    printf 'run directory identity changed before analysis: %s\n' \
      "${run_dir}" >&2
    exit 1
  fi
  "${experiment_dir}/analyze.sh" "${run_dir}"
  if ! verify_run_directory; then
    printf 'run directory identity changed during analysis: %s\n' \
      "${run_dir}" >&2
    exit 1
  fi
fi

input_snapshot="${quality_scratch}/inputs"
mkdir -p "${input_snapshot}"

snapshot_input() {
  local source="$1"
  local relative="$2"
  local destination="${input_snapshot}/${relative}"
  local source_fd source_fd_path source_identity fd_state_before fd_state_after

  if [[ ! -f "${source}" || -L "${source}" ]]; then
    printf 'evaluator input is missing, not regular, or a symlink: %s\n' \
      "${source}" >&2
    exit 1
  fi
  if ! exec {source_fd}<"${source}"; then
    printf 'failed to open evaluator input: %s\n' "${source}" >&2
    exit 1
  fi
  source_fd_path="/proc/self/fd/${source_fd}"
  source_identity="$(stat -Lc '%d:%i' "${source}")"
  if [[ -L "${source}" ||
    ! -f "${source}" ||
    ! -f "${source_fd_path}" ||
    "$(stat -Lc '%d:%i' "${source_fd_path}")" != "${source_identity}" ]]; then
    exec {source_fd}<&-
    printf 'evaluator input identity changed while opening: %s\n' \
      "${source}" >&2
    exit 1
  fi
  fd_state_before="$(stat -Lc '%d:%i:%s:%Y:%Z' "${source_fd_path}")"
  mkdir -p "$(dirname "${destination}")"
  cp -- "${source_fd_path}" "${destination}"
  chmod a-w "${destination}"
  fd_state_after="$(stat -Lc '%d:%i:%s:%Y:%Z' "${source_fd_path}")"
  if [[ "${fd_state_before}" != "${fd_state_after}" ||
    -L "${source}" ||
    ! -f "${source}" ||
    "$(stat -Lc '%d:%i' "${source}")" != "${source_identity}" ]] ||
    ! cmp -s "${source_fd_path}" "${destination}"; then
    exec {source_fd}<&-
    printf 'evaluator input changed while snapshotting: %s\n' \
      "${source}" >&2
    exit 1
  fi
  exec {source_fd}<&-
  snapshot_sources+=("${source}")
  snapshot_copies+=("${destination}")
  snapshot_identities+=("${source_identity}")
}

verify_input_snapshot() {
  local index source_fd source_fd_path fd_state_before fd_state_after
  for ((index = 0; index < ${#snapshot_sources[@]}; index++)); do
    if [[ ! -f "${snapshot_sources[index]}" ||
      -L "${snapshot_sources[index]}" ]]; then
      printf 'evaluator input changed during quality check: %s\n' \
        "${snapshot_sources[index]}" >&2
      return 1
    fi
    if ! exec {source_fd}<"${snapshot_sources[index]}"; then
      printf 'evaluator input changed during quality check: %s\n' \
        "${snapshot_sources[index]}" >&2
      return 1
    fi
    source_fd_path="/proc/self/fd/${source_fd}"
    fd_state_before="$(stat -Lc '%d:%i:%s:%Y:%Z' "${source_fd_path}")"
    if [[ "$(stat -Lc '%d:%i' "${source_fd_path}")" != \
      "${snapshot_identities[index]}" ||
      "$(stat -Lc '%d:%i' "${snapshot_sources[index]}")" != \
      "${snapshot_identities[index]}" ]] ||
      ! cmp -s "${source_fd_path}" "${snapshot_copies[index]}"; then
      exec {source_fd}<&-
      printf 'evaluator input changed during quality check: %s\n' \
        "${snapshot_sources[index]}" >&2
      return 1
    fi
    fd_state_after="$(stat -Lc '%d:%i:%s:%Y:%Z' "${source_fd_path}")"
    exec {source_fd}<&-
    if [[ "${fd_state_before}" != "${fd_state_after}" ||
      ! -f "${snapshot_sources[index]}" ||
      -L "${snapshot_sources[index]}" ||
      "$(stat -Lc '%d:%i' "${snapshot_sources[index]}")" != \
      "${snapshot_identities[index]}" ]]; then
      printf 'evaluator input changed during quality check: %s\n' \
        "${snapshot_sources[index]}" >&2
      return 1
    fi
  done
}

file_digest() {
  sha256sum < "$1" | awk '{print $1}'
}

snapshot_input "${run_dir_fd_path}/metrics.json" "metrics.json"
snapshot_input \
  "${experiment_dir}/quality-rubric.json" \
  "quality-rubric.json"
snapshot_input \
  "${experiment_dir}/quality-output-schema.json" \
  "quality-output-schema.json"
repo_root="$(cd "${experiment_dir}/../.." && pwd)"
snapshot_input \
  "${experiment_dir}/quality-check.sh" \
  "generators/quality-check.sh"
snapshot_input \
  "${experiment_dir}/analyze.sh" \
  "generators/analyze.sh"
snapshot_input \
  "${experiment_dir}/profiles.tsv" \
  "generators/profiles.tsv"
snapshot_input \
  "${repo_root}/cmd/repo-view-run-stats/main.go" \
  "generators/cmd-repo-view-run-stats-main.go"
snapshot_input \
  "${repo_root}/internal/runstats/runstats.go" \
  "generators/internal-runstats-runstats.go"
snapshot_input \
  "${repo_root}/go.mod" \
  "generators/go.mod"
evaluator_snapshot_paths=(
  "quality-rubric.json"
  "quality-output-schema.json"
  "generators/quality-check.sh"
  "generators/analyze.sh"
  "generators/profiles.tsv"
  "generators/cmd-repo-view-run-stats-main.go"
  "generators/internal-runstats-runstats.go"
  "generators/go.mod"
)
evaluator_bundle_paths=(
  "quality/evaluator-quality-rubric.json"
  "quality/evaluator-quality-output-schema.json"
  "quality/generator-quality-check.sh"
  "quality/generator-analyze.sh"
  "quality/generator-profiles.tsv"
  "quality/generator-cmd-repo-view-run-stats-main.go.source"
  "quality/generator-internal-runstats-runstats.go.source"
  "quality/generator-go.mod"
)
metrics="${input_snapshot}/metrics.json"
rubric="${input_snapshot}/quality-rubric.json"
output_schema="${input_snapshot}/quality-output-schema.json"

if ! jq -se '
  def valid_regex:
    . as $pattern
    | type == "string"
    and length > 0
    and (try ("" | test($pattern) | true) catch false);
  def valid_criterion:
    type == "object"
    and (
      (keys | sort)
      == (
        if has("none_of") then
          ["all_of", "id", "none_of", "required", "weight"]
        else
          ["all_of", "id", "required", "weight"]
        end
        | sort
      )
    )
    and (.id | type == "string" and test("^[a-z0-9][a-z0-9_-]*$"))
    and (.weight | type == "number" and floor == . and . > 0)
    and (.required | type == "boolean")
    and (
      .all_of
      | type == "array"
      and length > 0
      and all(.[]; valid_regex)
    )
    and (
      (.none_of // [])
      | type == "array"
      and all(.[]; valid_regex)
    );
  length == 1
  and (
    .[0]
    | type == "object"
    and (keys | sort) == ["schema_version", "tasks"]
    and .schema_version == 1
    and (
      .tasks
      | type == "object"
      and (keys | sort)
        == ["deep-explain", "deep-review", "explain", "review"]
      and all(
        .[];
        type == "object"
        and (keys == ["criteria"])
        and (
          .criteria
          | type == "array"
          and length > 0
          and all(.[]; valid_criterion)
          and ([.[].id] | unique | length) == length
          and (map(.weight) | add) > 0
        )
      )
    )
  )
' "${rubric}" >/dev/null 2>&1; then
  printf 'invalid quality rubric: %s\n' \
    "${experiment_dir}/quality-rubric.json" >&2
  exit 1
fi
if ! jq -se '
  length == 1 and (.[0] | type == "object")
' "${output_schema}" >/dev/null 2>&1; then
  printf 'invalid quality output schema: %s\n' \
    "${experiment_dir}/quality-output-schema.json" >&2
  exit 1
fi

if ! jq -se '
  def task:
    . == "explain"
    or . == "review"
    or . == "deep-explain"
    or . == "deep-review";
  def safe_name:
    type == "string"
    and test("^[a-z0-9][a-z0-9-]*$");
  def nonnegative_integer:
    type == "number" and floor == . and . >= 0;
  def optional_nonnegative_integer($field):
    (has($field) | not)
    or (.[$field] | nonnegative_integer);
  def optional_boolean($field):
    (has($field) | not)
    or (.[$field] | type == "boolean");
  def count_stat:
    type == "object"
    and (.name | type == "string" and length > 0)
    and (.tool_calls | nonnegative_integer)
    and (.invocations | nonnegative_integer)
    and (.output_characters | nonnegative_integer);
  def valid_case:
    type == "object"
    and (.name | safe_name)
    and (.task | type == "string" and task)
    and (.variant == "baseline" or .variant == "optimized")
    and (.profile | safe_name)
    and (
      (.variant == "baseline" and .profile == "baseline")
      or (.variant == "optimized" and .profile != "baseline")
    )
    and (.completed | type == "boolean")
    and (.exit_code | nonnegative_integer)
    and (.answer_file | type == "string")
    and (
      .name == (
        if .variant == "baseline" then
          "baseline-" + .task
        elif .profile == "default" then
          "optimized-" + .task
        else
          "optimized-" + .profile + "-" + .task
        end
      )
    )
    and (.answer_file == ("answers/" + .name + ".md"))
    and (.commands_file == ("commands/" + .name + ".txt"))
    and (.tool_stats_file == ("tool-stats/" + .name + ".json"))
    and (.call_graph_dot_file == ("call-graphs/" + .name + ".dot"))
    and (
      .call_graph_markdown_file
      == ("call-graphs/" + .name + ".md")
    )
    and (.tool_call_count | nonnegative_integer)
    and (.command_execution_tool_call_count | nonnegative_integer)
    and (.other_tool_call_count | nonnegative_integer)
    and (.repo_view_tool_call_count | nonnegative_integer)
    and (.repo_view_invocation_count | nonnegative_integer)
    and (.temporal_tool_edge_count | nonnegative_integer)
    and (.output_reference_edge_count | nonnegative_integer)
    and (.repo_view_invocation_cap | nonnegative_integer)
    and (.repo_view_tool_output_characters | nonnegative_integer)
    and (.repo_view_budget_observed_used | nonnegative_integer)
    and (.repo_view_budget_tamper_command_count | nonnegative_integer)
    and (.repo_view_bound_violation_count | nonnegative_integer)
    and (.repo_view_changed_invocation_count | nonnegative_integer)
    and (.repo_view_find_invocation_count | nonnegative_integer)
    and (.repo_view_inspect_invocation_count | nonnegative_integer)
    and (.repo_view_outline_invocation_count | nonnegative_integer)
    and (.tool_output_characters | nonnegative_integer)
    and (.tool_call_accounting_valid | type == "boolean")
    and (.repo_view_invocation_accounting_valid | type == "boolean")
    and (.repo_view_tool_call_accounting_valid | type == "boolean")
    and (.repo_view_budget_accounting_valid | type == "boolean")
    and (.repo_view_command_shape_valid | type == "boolean")
    and (.repo_view_first_invocation_changed | type == "boolean")
    and (.repo_view_navigation_semantics_valid | type == "boolean")
    and (.mechanical_navigation_semantics_enforced | type == "boolean")
    and (.repo_view_navigation_semantic_violation_commands | type == "array")
    and all(
      .repo_view_navigation_semantic_violation_commands[];
      type == "string"
    )
    and (.repo_view_invocation_cap_exceeded | type == "boolean")
    and (.tool_types | type == "array" and all(.[]; count_stat))
    and (.operations | type == "array" and all(.[]; count_stat))
    and (.repo_view_budget_tamper_commands | type == "array")
    and all(.repo_view_budget_tamper_commands[]; type == "string")
    and (.repo_view_bound_violation_commands | type == "array")
    and all(.repo_view_bound_violation_commands[]; type == "string")
    and (
      .repo_view_bounds
      | type == "object"
      and (.limit | nonnegative_integer)
      and (.context | nonnegative_integer)
      and (.max_code_lines | nonnegative_integer)
      and (.max_patch_lines | nonnegative_integer)
    )
    and (
      (.completed | not)
      or (
        .exit_code == 0
        and (.input_tokens | nonnegative_integer)
        and (.cached_input_tokens | nonnegative_integer)
        and (.output_tokens | nonnegative_integer)
        and (.reasoning_output_tokens | nonnegative_integer)
        and .cached_input_tokens <= .input_tokens
        and .regular_input_tokens
          == (.input_tokens - .cached_input_tokens)
        and .raw_total_tokens
          == (.input_tokens + .output_tokens)
        and .effective_tokens
          == (
            (.input_tokens - .cached_input_tokens)
            + (.cached_input_tokens * 0.1)
            + .output_tokens
          )
      )
    );

  length == 1
  and (
    .[0]
    | type == "object"
    and (.schema_version == 2)
    and (
      .formula
      == "effective = (input - cached_input) + 0.1 * cached_input + output"
    )
    and (
      .analysis_provenance
      | type == "object"
      and (keys | sort)
        == ["profiles_path", "profiles_sha256", "profiles_source"]
      and (
        .profiles_source == "run-snapshot"
        or .profiles_source == "current-evaluator"
      )
      and (.profiles_path | type == "string" and length > 0)
      and (
        .profiles_sha256
        | type == "string" and test("^[0-9a-f]{64}$")
      )
    )
    and (.cases | type == "array" and length > 0)
    and (.comparisons | type == "array")
    and all(.cases[]; valid_case)
    and (([.cases[].name] | unique | length) == (.cases | length))
    and (
      (
        [
          .cases[]
          | select(.completed)
          | [.task, .variant, .profile]
        ]
        | unique
        | length
      )
      == ([.cases[] | select(.completed)] | length)
    )
  )
' "${metrics}" >/dev/null 2>&1; then
  printf 'invalid metrics.json: %s\n' "${run_dir}/metrics.json" >&2
  exit 1
fi

static_cases="$(mktemp "${quality_scratch}/static-cases.XXXXXX")"
judge_usage_cases="$(mktemp "${quality_scratch}/judge-usage-cases.XXXXXX")"

require_strict_inputs=false
if "${enforce}" ||
  [[ "${judge_repeats}" -gt 0 ]]; then
  require_strict_inputs=true
fi
strict_evidence="${require_strict_inputs}"
aggregate_status="non-strict"
if "${strict_evidence}"; then
  aggregate_status="strict-current"
fi
if "${bind_legacy_judges}"; then
  strict_evidence=false
  aggregate_status="legacy-unisolated-attested"
fi

manifest_source="${run_dir_fd_path}/manifest.json"
manifest=""
manifest_valid=false
manifest_selection_valid=false
target_root=""
target_commit=""
target_base_commit=""
manifest_target_root=""
manifest_generation_isolation=""
manifest_generation_config_sha256=""
generation_config=""
run_complete=""
task_selection=""
variant_selection=""
selected_tasks=()
selected_profiles=()
if [[ -e "${manifest_source}" ]]; then
  snapshot_input "${manifest_source}" "manifest.json"
  manifest="${input_snapshot}/manifest.json"
  if jq -se \
    --arg generation_isolation "${required_generation_isolation}" \
    '
    def safe_name:
      type == "string"
      and test("^[a-z0-9][a-z0-9-]*$");
    length == 1
    and (
      .[0]
      | . as $manifest
      | type == "object"
      and (.schema_version == 1)
      and (.worktree | type == "string" and length > 0)
      and (
        .target_commit
        | type == "string"
        and test("^([0-9a-f]{40}|[0-9a-f]{64})$")
      )
      and (
        .prompt_commit
        | type == "string"
        and test("^[0-9a-f]{7,64}$")
      )
      and ($manifest.target_commit | startswith($manifest.prompt_commit))
      and (.model | type == "string" and length > 0)
      and (.codex_version | type == "string" and length > 0)
      and (
        .generation_isolation
        == $generation_isolation
      )
      and .mechanical_navigation_semantics_enforced == true
      and (
        .generation_config_sha256
        | type == "string"
        and test("^[0-9a-f]{64}$")
      )
      and .profiles_snapshot_path == "profiles-snapshot.tsv"
      and (
        .profiles_snapshot_sha256
        | type == "string"
        and test("^[0-9a-f]{64}$")
      )
      and (.go_version | type == "string" and length > 0)
      and (
        .base_commit
        | type == "string"
        and test("^([0-9a-f]{40}|[0-9a-f]{64})$")
      )
      and (.base_ref | type == "string" and length > 0)
      and has("baseline_from")
      and (
        .baseline_from == null
        or (.baseline_from | type == "string" and length > 0)
      )
      and (
        .task_selection == "explain"
        or .task_selection == "review"
        or .task_selection == "all"
        or .task_selection == "deep-explain"
        or .task_selection == "deep-review"
        or .task_selection == "deep"
      )
      and (
        .prompt_digests
        | type == "object"
        and (
          keys | sort
          == (
            if $manifest.task_selection == "all" then
              ["explain", "review"]
            elif $manifest.task_selection == "deep" then
              ["deep-explain", "deep-review"]
            else
              [$manifest.task_selection]
            end
            | sort
          )
        )
        and all(
          .[];
          type == "string" and test("^[0-9a-f]{64}$")
        )
      )
      and (
        .prompt_files
        | type == "object"
        and (keys | sort) == ($manifest.prompt_digests | keys | sort)
        and all(
          to_entries[];
          .value == ("prompts/" + .key + ".txt")
        )
      )
      and (
        .variant_selection == "baseline"
        or .variant_selection == "optimized"
        or .variant_selection == "all"
      )
      and (
        .variant_selection != "optimized"
        or (.baseline_from | type == "string" and length > 0)
      )
      and (.profiles | type == "array" and length > 0)
      and all(.profiles[]; safe_name)
      and ((.profiles | unique | length) == (.profiles | length))
    )
  ' "${manifest}" >/dev/null 2>&1; then
    manifest_valid=true
    manifest_selection_valid=true
    target_root="$(jq -r '.worktree' "${manifest}")"
    manifest_target_root="${target_root}"
    target_commit="$(jq -r '.target_commit' "${manifest}")"
    target_base_commit="$(jq -r '.base_commit' "${manifest}")"
    task_selection="$(jq -r '.task_selection' "${manifest}")"
    variant_selection="$(jq -r '.variant_selection' "${manifest}")"
    manifest_generation_isolation="$(
      jq -r '.generation_isolation' "${manifest}"
    )"
    manifest_generation_config_sha256="$(
      jq -r '.generation_config_sha256' "${manifest}"
    )"
    case "${task_selection}" in
      all)
        selected_tasks=(explain review)
        ;;
      deep)
        selected_tasks=(deep-explain deep-review)
        ;;
      *)
        selected_tasks=("${task_selection}")
        ;;
    esac
    mapfile -t selected_profiles < <(jq -r '.profiles[]' "${manifest}")
  fi
  if "${bind_legacy_judges}" &&
    ! "${manifest_selection_valid}" &&
    jq -se '
      def safe_name:
        type == "string"
        and test("^[a-z0-9][a-z0-9-]*$");
      def commit:
        type == "string"
        and test("^([0-9a-f]{40}|[0-9a-f]{64})$")
        and (test("^0+$") | not);
      length == 1
      and (
        .[0]
        | type == "object"
        and .schema_version == 1
        and (.worktree | type == "string" and length > 0)
        and (.target_commit | commit)
        and (
          .prompt_commit
          | type == "string" and test("^[0-9a-f]{7,64}$")
        )
        and (
          . as $legacy_manifest
          | ($legacy_manifest.target_commit
            | startswith($legacy_manifest.prompt_commit))
        )
        and (.base_commit | commit)
        and (.base_ref | type == "string" and length > 0)
        and has("baseline_from")
        and (
          .baseline_from == null
          or (.baseline_from | type == "string" and length > 0)
        )
        and (
          .task_selection == "explain"
          or .task_selection == "review"
          or .task_selection == "all"
          or .task_selection == "deep-explain"
          or .task_selection == "deep-review"
          or .task_selection == "deep"
        )
        and (
          .variant_selection == "baseline"
          or .variant_selection == "optimized"
          or .variant_selection == "all"
        )
        and (
          .variant_selection != "optimized"
          or (.baseline_from | type == "string" and length > 0)
        )
        and (.profiles | type == "array" and length > 0)
        and all(.profiles[]; safe_name)
        and ((.profiles | unique | length) == (.profiles | length))
      )
    ' "${manifest}" >/dev/null 2>&1; then
    manifest_selection_valid=true
    target_root="$(jq -r '.worktree' "${manifest}")"
    manifest_target_root="${target_root}"
    target_commit="$(jq -r '.target_commit' "${manifest}")"
    target_base_commit="$(jq -r '.base_commit' "${manifest}")"
    task_selection="$(jq -r '.task_selection' "${manifest}")"
    variant_selection="$(jq -r '.variant_selection' "${manifest}")"
    manifest_generation_isolation="$(
      jq -r '.generation_isolation // "legacy-unisolated"' "${manifest}"
    )"
    manifest_generation_config_sha256="$(
      jq -r '.generation_config_sha256 // ""' "${manifest}"
    )"
    case "${task_selection}" in
      all)
        selected_tasks=(explain review)
        ;;
      deep)
        selected_tasks=(deep-explain deep-review)
        ;;
      *)
        selected_tasks=("${task_selection}")
        ;;
    esac
    mapfile -t selected_profiles < <(jq -r '.profiles[]' "${manifest}")
  fi
fi

profiles_snapshot=""
profiles_snapshot_source="${run_dir_fd_path}/profiles-snapshot.tsv"
declare -A rendered_prompt_snapshot_for_task=()
if [[ -e "${profiles_snapshot_source}" ]]; then
  snapshot_input "${profiles_snapshot_source}" "profiles-snapshot.tsv"
  profiles_snapshot="${input_snapshot}/profiles-snapshot.tsv"
fi
if "${manifest_selection_valid}"; then
  for selected_task in "${selected_tasks[@]}"; do
    rendered_prompt_relative="$(
      jq -r \
        --arg task "${selected_task}" \
        '.prompt_files[$task] // empty' \
        "${manifest}"
    )"
    if [[ -n "${rendered_prompt_relative}" ]]; then
      snapshot_input \
        "${run_dir_fd_path}/${rendered_prompt_relative}" \
        "${rendered_prompt_relative}"
      rendered_prompt_snapshot_for_task["${selected_task}"]="$(
        printf '%s/%s' "${input_snapshot}" "${rendered_prompt_relative}"
      )"
    fi
  done
fi

if "${require_strict_inputs}" && ! "${manifest_valid}"; then
  printf 'missing or invalid quality manifest: %s\n' "${manifest_source}" >&2
  exit 1
fi
if "${require_strict_inputs}"; then
  if [[ -z "${profiles_snapshot}" ]] ||
    [[ "$(file_digest "${profiles_snapshot}")" != \
      "$(jq -r '.profiles_snapshot_sha256' "${manifest}")" ]]; then
    printf 'profiles snapshot is missing or disagrees with manifest: %s\n' \
      "${profiles_snapshot_source}" >&2
    exit 1
  fi
  if ! jq -e \
    --arg digest "$(file_digest "${profiles_snapshot}")" \
    '
      .analysis_provenance == {
        profiles_source: "run-snapshot",
        profiles_path: "profiles-snapshot.tsv",
        profiles_sha256: $digest
      }
    ' "${metrics}" >/dev/null; then
    printf 'metrics profile provenance disagrees with run snapshot\n' >&2
    exit 1
  fi
  for selected_task in "${selected_tasks[@]}"; do
    rendered_prompt="${rendered_prompt_snapshot_for_task[${selected_task}]:-}"
    if [[ -z "${rendered_prompt}" ]] ||
      [[ "$(file_digest "${rendered_prompt}")" != \
        "$(jq -r --arg task "${selected_task}" \
          '.prompt_digests[$task]' "${manifest}")" ]]; then
      printf 'rendered prompt is missing or disagrees with manifest: %s\n' \
        "${selected_task}" >&2
      exit 1
    fi
  done
fi

generation_config_source="${run_dir_fd_path}/generation-config.json"
if [[ -e "${generation_config_source}" ]]; then
  snapshot_input "${generation_config_source}" "generation-config.json"
  generation_config="${input_snapshot}/generation-config.json"
fi
run_complete_source="${run_dir_fd_path}/run-complete.json"
if [[ -e "${run_complete_source}" ]]; then
  snapshot_input "${run_complete_source}" "run-complete.json"
  run_complete="${input_snapshot}/run-complete.json"
fi
if "${require_strict_inputs}"; then
  if [[ -z "${generation_config}" ]] ||
    [[ "$(file_digest "${generation_config}")" != \
      "${manifest_generation_config_sha256}" ]] ||
    ! jq -e \
    --arg generation_isolation "${required_generation_isolation}" \
    --arg developer_instructions "${no_collaboration}" \
    --slurpfile run_manifest "${manifest}" \
    '
      type == "object"
      and (
        keys | sort
        == [
          "auth_source_permission",
          "baseline_developer_instructions",
          "codex_environment",
          "codex_isolation_flags",
          "feature_flags",
          "generation_isolation",
          "host_go_environment",
          "mechanical_navigation_contract",
          "mechanical_navigation_semantics_enforced",
          "profiles_snapshot_path",
          "profiles_snapshot_sha256",
          "prompt_digests",
          "prompt_files"
        ]
      )
      and .generation_isolation == $generation_isolation
      and .baseline_developer_instructions == $developer_instructions
      and .feature_flags == [
        "--disable",
        "multi_agent",
        "--disable",
        "multi_agent_v2",
        "--disable",
        "enable_fanout",
        "--disable",
        "collaboration_modes",
        "--disable",
        "hooks",
        "--disable",
        "tool_router",
        "--disable",
        "workflows",
        "--disable",
        "code_mode",
        "--disable",
        "code_mode_host",
        "--disable",
        "code_mode_only"
      ]
      and (
        .codex_isolation_flags
        | type == "array"
        and length > 0
        and all(.[]; type == "string")
        and index("--ignore-user-config") != null
        and index("--ignore-rules") != null
      )
      and (
        .codex_environment
        | type == "array"
        and length > 0
        and all(.[]; type == "string")
        and index("env") != null
        and index("-i") != null
        and index("GOENV=off") != null
        and index("GOTOOLCHAIN=local") != null
        and index("GOWORK=off") != null
      )
      and (
        .host_go_environment
        | type == "array"
        and length > 0
        and all(.[]; type == "string")
        and index("env") != null
        and index("GOENV=off") != null
        and index("GOTOOLCHAIN=local") != null
        and index("GOWORK=off") != null
      )
      and .profiles_snapshot_path == "profiles-snapshot.tsv"
      and .profiles_snapshot_path == $run_manifest[0].profiles_snapshot_path
      and (
        .profiles_snapshot_sha256
        == $run_manifest[0].profiles_snapshot_sha256
      )
      and .prompt_files == $run_manifest[0].prompt_files
      and .prompt_digests == $run_manifest[0].prompt_digests
      and .mechanical_navigation_semantics_enforced == true
      and .mechanical_navigation_contract == {
        required_root: "<worktree>",
        required_base_commit: "<resolved-base>",
        required_changed_return: "<profile-return>",
        required_changed_context: "<profile-context>",
        require_navigation_semantics: "1"
      }
      and .auth_source_permission == "deny-if-present"
    ' "${generation_config}" >/dev/null 2>&1; then
    printf 'generation config is missing or disagrees with manifest: %s\n' \
      "${generation_config_source}" >&2
    exit 1
  fi
  if [[ -z "${run_complete}" ]] ||
    ! jq -se '
      length == 1
      and (
        .[0]
        | type == "object"
        and .schema_version == 1
        and .state == "complete"
        and .outcome == "success"
        and .exit_code == 0
        and (.completed_at | type == "string" and length > 0)
      )
    ' "${run_complete}" >/dev/null 2>&1; then
    printf 'strict quality evidence requires a successful run-complete.json: %s\n' \
      "${run_complete_source}" >&2
    exit 1
  fi
fi

baseline_from=""
baseline_source_manifest_source="${run_dir_fd_path}/baseline-source-manifest.json"
if "${manifest_valid}"; then
  baseline_from="$(jq -r '.baseline_from // empty' "${manifest}")"
  if [[ -n "${baseline_from}" ]]; then
    if [[ ! -f "${baseline_source_manifest_source}" ||
      -L "${baseline_source_manifest_source}" ]]; then
      printf 'imported baseline source manifest is missing: %s\n' \
        "${baseline_source_manifest_source}" >&2
      exit 1
    fi
    snapshot_input \
      "${baseline_source_manifest_source}" \
      "baseline-source-manifest.json"
    baseline_source_manifest="${input_snapshot}/baseline-source-manifest.json"
    baseline_source_generation_config_source="$(
      printf '%s' "${run_dir_fd_path}/baseline-source-generation-config.json"
    )"
    if [[ ! -f "${baseline_source_generation_config_source}" ||
      -L "${baseline_source_generation_config_source}" ]]; then
      printf 'imported baseline generation config is missing: %s\n' \
        "${baseline_source_generation_config_source}" >&2
      exit 1
    fi
    snapshot_input \
      "${baseline_source_generation_config_source}" \
      "baseline-source-generation-config.json"
    baseline_source_generation_config="$(
      printf '%s' "${input_snapshot}/baseline-source-generation-config.json"
    )"
    baseline_source_profiles_source="$(
      printf '%s' \
        "${run_dir_fd_path}/baseline-source-profiles-snapshot.tsv"
    )"
    if [[ ! -f "${baseline_source_profiles_source}" ||
      -L "${baseline_source_profiles_source}" ]]; then
      printf 'imported baseline profiles snapshot is missing: %s\n' \
        "${baseline_source_profiles_source}" >&2
      exit 1
    fi
    snapshot_input \
      "${baseline_source_profiles_source}" \
      "baseline-source-profiles-snapshot.tsv"
    if ! cmp -s \
      "${input_snapshot}/baseline-source-profiles-snapshot.tsv" \
      "${profiles_snapshot}"; then
      printf 'imported baseline profiles snapshot disagrees with run snapshot\n' >&2
      exit 1
    fi
    for selected_task in "${selected_tasks[@]}"; do
      baseline_prompt_source="$(
        printf '%s/baseline-source-prompts/%s.txt' \
          "${run_dir_fd_path}" "${selected_task}"
      )"
      if [[ ! -f "${baseline_prompt_source}" ||
        -L "${baseline_prompt_source}" ]]; then
        printf 'imported baseline rendered prompt is missing: %s\n' \
          "${baseline_prompt_source}" >&2
        exit 1
      fi
      snapshot_input \
        "${baseline_prompt_source}" \
        "baseline-source-prompts/${selected_task}.txt"
      if ! cmp -s \
        "${input_snapshot}/baseline-source-prompts/${selected_task}.txt" \
        "${rendered_prompt_snapshot_for_task[${selected_task}]}"; then
        printf 'imported baseline rendered prompt disagrees for %s\n' \
          "${selected_task}" >&2
        exit 1
      fi
    done
    if ! jq -se \
      --slurpfile run_manifest "${manifest}" \
      '
        length == 1
        and (
          .[0]
          | type == "object"
          and .schema_version == 1
          and (
            .target_commit
            | type == "string"
            and test("^([0-9a-f]{40}|[0-9a-f]{64})$")
          )
          and (
            .prompt_commit
            | type == "string"
            and test("^[0-9a-f]{7,64}$")
          )
          and (
            .base_commit
            | type == "string"
            and test("^([0-9a-f]{40}|[0-9a-f]{64})$")
          )
          and (.base_ref | type == "string" and length > 0)
          and (.model | type == "string" and length > 0)
          and (.codex_version | type == "string" and length > 0)
          and (.generation_isolation | type == "string" and length > 0)
          and .mechanical_navigation_semantics_enforced == true
          and (
            .generation_config_sha256
            | type == "string"
            and test("^[0-9a-f]{64}$")
          )
          and .profiles_snapshot_path == "profiles-snapshot.tsv"
          and (
            .profiles_snapshot_sha256
            | type == "string"
            and test("^[0-9a-f]{64}$")
          )
          and (.go_version | type == "string" and length > 0)
          and (
            .prompt_files
            | type == "object"
            and all(
              to_entries[];
              .value == ("prompts/" + .key + ".txt")
            )
          )
          and (
            .prompt_digests
            | type == "object"
            and all(
              .[];
              type == "string" and test("^[0-9a-f]{64}$")
            )
          )
          and .target_commit == $run_manifest[0].target_commit
          and .prompt_commit == $run_manifest[0].prompt_commit
          and .base_commit == $run_manifest[0].base_commit
          and .base_ref == $run_manifest[0].base_ref
          and .model == $run_manifest[0].model
          and .codex_version == $run_manifest[0].codex_version
          and .generation_isolation == $run_manifest[0].generation_isolation
          and (
            .mechanical_navigation_semantics_enforced
            == $run_manifest[0].mechanical_navigation_semantics_enforced
          )
          and (
            .generation_config_sha256
            == $run_manifest[0].generation_config_sha256
          )
          and (
            .profiles_snapshot_path
            == $run_manifest[0].profiles_snapshot_path
          )
          and (
            .profiles_snapshot_sha256
            == $run_manifest[0].profiles_snapshot_sha256
          )
          and .go_version == $run_manifest[0].go_version
          and .prompt_files == $run_manifest[0].prompt_files
          and .prompt_digests == $run_manifest[0].prompt_digests
        )
      ' "${baseline_source_manifest}" >/dev/null 2>&1; then
      printf 'imported baseline source manifest disagrees with run manifest\n' >&2
      exit 1
    fi
    if [[ "$(file_digest "${baseline_source_generation_config}")" != \
      "${manifest_generation_config_sha256}" ]] ||
      ! cmp -s \
        "${baseline_source_generation_config}" \
        "${generation_config}"; then
      printf 'imported baseline generation config disagrees with run config\n' >&2
      exit 1
    fi
  elif [[ -e "${baseline_source_manifest_source}" ]]; then
    printf 'non-imported run contains a baseline source manifest: %s\n' \
      "${baseline_source_manifest_source}" >&2
    exit 1
  fi
fi
matrix_complete=false
expected_cases_json='[]'
optimized_expected_count=0
if "${manifest_selection_valid}"; then
  expected_cases_json="$(
    jq -cn \
      --arg task_selection "${task_selection}" \
      --arg variant_selection "${variant_selection}" \
      --argjson profiles "$(jq -c '.profiles' "${manifest}")" \
      '
        (
          if $task_selection == "all" then
            ["explain", "review"]
          elif $task_selection == "deep" then
            ["deep-explain", "deep-review"]
          else
            [$task_selection]
          end
        ) as $tasks
        | (
            $variant_selection == "optimized"
            or $variant_selection == "all"
          ) as $has_optimized
        | [
            $tasks[] as $task
            | {
                name: ("baseline-" + $task),
                task: $task,
                variant: "baseline",
                profile: "baseline"
              },
              (
                if $has_optimized then
                  $profiles[] as $profile
                  | {
                      name: (
                        if $profile == "default" then
                          "optimized-" + $task
                        else
                          "optimized-" + $profile + "-" + $task
                        end
                      ),
                      task: $task,
                      variant: "optimized",
                      profile: $profile
                    }
                else
                  empty
                end
              )
          ]
        | sort_by([.task, .variant, .profile, .name])
      '
  )"
  actual_cases_json="$(
    jq -c '
      [
        .cases[]
        | {
            name,
            task,
            variant,
            profile
          }
      ]
      | sort_by([.task, .variant, .profile, .name])
    ' "${metrics}"
  )"
  if [[ "${actual_cases_json}" == "${expected_cases_json}" ]]; then
    matrix_complete=true
  fi
  optimized_expected_count="$(
    jq '[.[] | select(.variant == "optimized")] | length' \
      <<< "${expected_cases_json}"
  )"
fi
if "${require_strict_inputs}" && ! "${matrix_complete}"; then
  printf 'metrics cases do not exactly match manifest selection: %s\n' \
    "${run_dir}/metrics.json" >&2
  exit 1
fi
if "${require_strict_inputs}" && [[ "${optimized_expected_count}" -eq 0 ]]; then
  printf 'quality enforcement or judging requires an optimized case\n' >&2
  exit 1
fi
if "${require_strict_inputs}" &&
  ! jq -e 'all(.cases[]; .completed)' "${metrics}" >/dev/null; then
  printf 'manifest-selected quality cases are incomplete: %s\n' \
    "${run_dir}/metrics.json" >&2
  exit 1
fi

generation_log_valid() {
  local log="$1"
  jq -se '
      def nonnegative_integer:
        type == "number" and isfinite and floor == . and . >= 0;
      def valid_usage:
        type == "object"
        and (.input_tokens | nonnegative_integer)
        and (.cached_input_tokens | nonnegative_integer)
        and (.output_tokens | nonnegative_integer)
        and (
          (.reasoning_output_tokens // 0)
          | nonnegative_integer
        )
        and .cached_input_tokens <= .input_tokens;
      . as $events
      | (
          [
            range(0; $events | length) as $index
            | select($events[$index].type == "thread.started")
            | $index
          ]
        ) as $thread_indexes
      | (
          [
            range(0; $events | length) as $index
            | select($events[$index].type == "turn.started")
            | $index
          ]
        ) as $turn_indexes
      | (
          [
            range(0; $events | length) as $index
            | select(
                $events[$index].type == "turn.completed"
                or $events[$index].type == "turn.failed"
              )
            | $index
          ]
        ) as $terminal_indexes
      | (
          [
            range(0; $events | length) as $index
            | select(
                $events[$index].type == "item.started"
                and $events[$index].item.type == "command_execution"
              )
            | {
                index: $index,
                id: $events[$index].item.id,
                command: $events[$index].item.command
              }
          ]
        ) as $command_starts
      | (
          [
            range(0; $events | length) as $index
            | select(
                $events[$index].type == "item.completed"
                and $events[$index].item.type == "command_execution"
              )
            | {
                index: $index,
                id: $events[$index].item.id,
                command: $events[$index].item.command,
                status: $events[$index].item.status,
                exit_code: $events[$index].item.exit_code
              }
          ]
        ) as $command_completions
      | ($events | length) > 0
      and all($events[]; type == "object" and (.type | type == "string"))
      and ($thread_indexes | length) == 1
      and ($turn_indexes | length) == 1
      and $thread_indexes[0] < $turn_indexes[0]
      and (
        $events[$thread_indexes[0]].thread_id
        | type == "string" and length > 0
      )
      and ($terminal_indexes | length) == 1
      and $terminal_indexes[0] == (($events | length) - 1)
      and $events[$terminal_indexes[0]].type == "turn.completed"
      and (
        [
          range(0; $events | length) as $index
          | if (
              ($events[$index].type | startswith("item."))
              and $events[$index].item.type == "command_execution"
            ) then
              $index > $turn_indexes[0]
              and $index < $terminal_indexes[0]
            else
              true
            end
        ]
        | all
      )
      and (
        [$command_starts[].id] as $ids
        | all(
            $command_starts[];
            (.id | type == "string" and length > 0)
            and (.command | type == "string" and length > 0)
          )
        and ($ids | unique | length) == ($ids | length)
      )
      and (
        [$command_completions[].id] as $ids
        | all(
            $command_completions[];
            (.id | type == "string" and length > 0)
            and (.command | type == "string" and length > 0)
            and (.exit_code | nonnegative_integer)
            and (
              (.status == "completed" and .exit_code == 0)
              or (.status == "failed" and .exit_code != 0)
            )
          )
        and ($ids | unique | length) == ($ids | length)
      )
      and all(
        $command_starts[];
        . as $start
        | (
            [
              $command_completions[]
              | select(.id == $start.id)
            ]
          ) as $matches
        | ($matches | length) == 1
        and $matches[0].index > $start.index
        and $matches[0].command == $start.command
      )
      and all(
        $command_completions[];
        . as $completion
        | (
            [
              $command_starts[]
              | select(.id == $completion.id)
            ]
          ) as $matches
        | ($matches | length) == 1
        and $matches[0].index < $completion.index
        and $matches[0].command == $completion.command
      )
      and (
        [
          $events[]
          | select(
              .type == "item.completed"
              and .item.type == "agent_message"
              and (.item.text | type == "string")
            )
        ]
        | length > 0
      )
      and (
        $events[$terminal_indexes[0]].usage
        | valid_usage
      )
    ' "${log}" >/dev/null 2>&1
}

metric_case_names="$(
  jq -r '.cases[].name' "${metrics}" | sort
)"
raw_log_names="$(
  find "${run_dir_fd_path}/." -maxdepth 1 -type f \
    \( -name 'baseline-*.jsonl' -o -name 'optimized-*.jsonl' \) \
    -printf '%f\n' |
    sed 's/\.jsonl$//' |
    sort
)"
raw_exit_names="$(
  find "${run_dir_fd_path}/." -maxdepth 1 -type f \
    \( -name 'baseline-*.exit-code' -o -name 'optimized-*.exit-code' \) \
    -printf '%f\n' |
    sed 's/\.exit-code$//' |
    sort
)"
answer_names="$(
  find "${run_dir_fd_path}/answers" -maxdepth 1 -type f -name '*.md' \
    -printf '%f\n' 2>/dev/null |
    sed 's/\.md$//' |
    sort
)"
if [[ "${raw_log_names}" != "${metric_case_names}" ||
  "${raw_exit_names}" != "${metric_case_names}" ||
  "${answer_names}" != "${metric_case_names}" ]]; then
  printf 'raw evidence cardinality disagrees with metrics cases: %s\n' \
    "${run_dir}" >&2
  exit 1
fi

while IFS=$'\t' read -r name completed metric_exit answer_file; do
  source_log="${run_dir_fd_path}/${name}.jsonl"
  source_exit="${run_dir_fd_path}/${name}.exit-code"
  source_answer="${run_dir_fd_path}/${answer_file}"
  snapshot_input "${source_log}" "${name}.jsonl"
  snapshot_input "${source_exit}" "${name}.exit-code"
  snapshot_input "${source_answer}" "${answer_file}"
  case_log="${input_snapshot}/${name}.jsonl"
  case_exit="${input_snapshot}/${name}.exit-code"
  case_answer="${input_snapshot}/${answer_file}"
  raw_exit="$(<"${case_exit}")"
  if [[ ! "${raw_exit}" =~ ^[0-9]+$ ||
    ${#raw_exit} -gt 18 ]]; then
    printf 'invalid generation exit code: %s\n' "${source_exit}" >&2
    exit 1
  fi
  raw_exit="$((10#${raw_exit}))"
  if [[ "${raw_exit}" -ne "${metric_exit}" ]]; then
    printf 'metrics exit code disagrees with raw evidence: %s\n' \
      "${name}" >&2
    exit 1
  fi
  if ! jq -se '
    length > 0
    and all(.[]; type == "object" and (.type | type == "string"))
  ' "${case_log}" >/dev/null 2>&1; then
    printf 'invalid generation transcript: %s\n' "${source_log}" >&2
    exit 1
  fi
  if [[ "${completed}" == "true" ]]; then
    if [[ "${raw_exit}" -ne 0 ]] ||
      ! generation_log_valid "${case_log}"; then
      printf 'completed case has invalid transcript or nonzero exit: %s\n' \
        "${name}" >&2
      exit 1
    fi
    expected_answer="${quality_scratch}/${name}.answer"
    jq -s -r '
      [
        .[]
        | select(
            .type == "item.completed"
            and .item.type == "agent_message"
          )
        | .item.text
      ]
      | last
    ' "${case_log}" > "${expected_answer}"
    if ! cmp -s "${case_answer}" "${expected_answer}"; then
      printf 'answer does not match final transcript message: %s\n' \
        "${source_answer}" >&2
      exit 1
    fi
    if ! jq -se \
      --arg name "${name}" \
      --argjson exit_code "${raw_exit}" \
      --slurpfile metrics_document "${metrics}" \
      '
        (
          [
            .[]
            | select(.type == "turn.completed")
            | .usage
          ][0]
        ) as $usage
        | (
            $metrics_document[0].cases[]
            | select(.name == $name)
          ) as $case
        | $case.completed
        and $case.exit_code == $exit_code
        and $case.input_tokens == $usage.input_tokens
        and $case.cached_input_tokens == $usage.cached_input_tokens
        and $case.output_tokens == $usage.output_tokens
        and (
          $case.reasoning_output_tokens
          == ($usage.reasoning_output_tokens // 0)
        )
      ' "${case_log}" >/dev/null 2>&1; then
      printf 'metrics usage disagrees with raw transcript: %s\n' \
        "${name}" >&2
      exit 1
    fi
  fi
done < <(
  jq -r '
    .cases[]
    | [
        .name,
        (.completed | tostring),
        (.exit_code | tostring),
        .answer_file
      ]
    | @tsv
  ' "${metrics}"
)

packet_inventory_before="$(
  find "${run_dir_fd_path}/." -maxdepth 1 -type f \
    -name 'changed-packet*.json' -printf '%f\n' |
    sort
)"
packet_files=()
while IFS= read -r -d '' packet_file; do
  packet_files+=("${packet_file}")
  packet_name="$(basename "${packet_file}")"
  snapshot_input "${packet_file}" "packets/${packet_name}"
done < <(
  find "${run_dir_fd_path}/." -maxdepth 1 -type f \
    -name 'changed-packet*.json' -print0 |
    sort -z
)

reanalyzed_dir="${quality_scratch}/reanalyzed"
mkdir "${reanalyzed_dir}"
while IFS= read -r name; do
  cp -- \
    "${input_snapshot}/${name}.jsonl" \
    "${reanalyzed_dir}/${name}.jsonl"
  cp -- \
    "${input_snapshot}/${name}.exit-code" \
    "${reanalyzed_dir}/${name}.exit-code"
done < <(jq -r '.cases[].name' "${metrics}")
for metadata_name in \
  manifest.json \
  generation-config.json \
  run-complete.json \
  profiles-snapshot.tsv \
  baseline-source-manifest.json \
  baseline-source-generation-config.json; do
  if [[ -f "${input_snapshot}/${metadata_name}" ]]; then
    cp -- \
      "${input_snapshot}/${metadata_name}" \
      "${reanalyzed_dir}/${metadata_name}"
  fi
done
for packet_file in "${packet_files[@]}"; do
  packet_name="$(basename "${packet_file}")"
  cp -- \
    "${input_snapshot}/packets/${packet_name}" \
    "${reanalyzed_dir}/${packet_name}"
done
if ! analysis_go_version="$(
  env \
    GOENV=off \
    GOTOOLCHAIN=local \
    GOWORK=off \
    GOFLAGS=-mod=readonly \
    go version
)"; then
  printf 'failed to resolve analyzer Go version\n' >&2
  exit 1
fi
if ! env \
  GOENV=off \
  GOTOOLCHAIN=local \
  GOWORK=off \
  GOFLAGS=-mod=readonly \
  "${experiment_dir}/analyze.sh" "${reanalyzed_dir}" \
  >/dev/null; then
  printf 'failed to independently analyze raw quality evidence\n' >&2
  exit 1
fi
snapshotted_metrics_json="$(jq -cS . "${metrics}")"
reanalyzed_metrics_json="$(jq -cS . "${reanalyzed_dir}/metrics.json")"
if [[ "${snapshotted_metrics_json}" != "${reanalyzed_metrics_json}" ]]; then
  printf 'metrics do not match independent raw evidence analysis: %s\n' \
    "${run_dir}/metrics.json" >&2
  exit 1
fi
while IFS=$'\t' read -r name answer_file; do
  if ! cmp -s \
    "${input_snapshot}/${answer_file}" \
    "${reanalyzed_dir}/${answer_file}"; then
    printf 'answer does not match independent raw evidence analysis: %s\n' \
      "${name}" >&2
    exit 1
  fi
done < <(jq -r '.cases[] | [.name, .answer_file] | @tsv' "${metrics}")
while IFS= read -r artifact_relative; do
  snapshot_input \
    "${run_dir_fd_path}/${artifact_relative}" \
    "${artifact_relative}"
  artifact_matches=false
  if [[ "${artifact_relative}" == *.json ]]; then
    snapshotted_artifact_json="$(
      jq -cS . "${input_snapshot}/${artifact_relative}"
    )"
    reanalyzed_artifact_json="$(
      jq -cS . "${reanalyzed_dir}/${artifact_relative}"
    )"
    if [[ "${snapshotted_artifact_json}" == "${reanalyzed_artifact_json}" ]]; then
      artifact_matches=true
    fi
  elif cmp -s \
    "${input_snapshot}/${artifact_relative}" \
    "${reanalyzed_dir}/${artifact_relative}"; then
    artifact_matches=true
  fi
  if ! "${artifact_matches}"; then
    printf 'derived artifact does not match independent raw analysis: %s\n' \
      "${artifact_relative}" >&2
    exit 1
  fi
done < <(
  jq -r '
    .cases[]
    | .commands_file,
      .tool_stats_file,
      .call_graph_dot_file,
      .call_graph_markdown_file
  ' "${metrics}" |
    sort -u
)

while IFS=$'\t' read -r name task variant profile answer_file repo_view_calls command_cap cap_exceeded budget_tamper changed_calls find_calls inspect_calls outline_calls bound_violations tool_accounting invocation_accounting repo_view_tool_accounting budget_accounting command_shape_valid first_invocation_changed navigation_semantics_valid mechanical_semantics_enforced; do
  answer_path="${input_snapshot}/${answer_file}"
  jq -n \
    --arg name "${name}" \
    --arg task "${task}" \
    --arg variant "${variant}" \
    --arg profile "${profile}" \
    --argjson repo_view_calls "${repo_view_calls}" \
    --argjson command_cap "${command_cap}" \
    --argjson cap_exceeded "${cap_exceeded}" \
    --argjson budget_tamper "${budget_tamper}" \
    --argjson changed_calls "${changed_calls}" \
    --argjson find_calls "${find_calls}" \
    --argjson inspect_calls "${inspect_calls}" \
    --argjson outline_calls "${outline_calls}" \
    --argjson bound_violations "${bound_violations}" \
    --argjson tool_accounting "${tool_accounting}" \
    --argjson invocation_accounting "${invocation_accounting}" \
    --argjson repo_view_tool_accounting "${repo_view_tool_accounting}" \
    --argjson budget_accounting "${budget_accounting}" \
    --argjson command_shape_valid "${command_shape_valid}" \
    --argjson first_invocation_changed "${first_invocation_changed}" \
    --argjson navigation_semantics_valid "${navigation_semantics_valid}" \
    --argjson mechanical_semantics_enforced "${mechanical_semantics_enforced}" \
    --rawfile answer "${answer_path}" \
    --slurpfile rubric "${rubric}" \
    '
      [$rubric[0].tasks[$task].criteria[] as $criterion
        | {
            id: $criterion.id,
            weight: $criterion.weight,
            required: $criterion.required,
            passed: (
              (
                $criterion.all_of
                | all(. as $pattern | $answer | test($pattern; "is"))
              )
              and (
                ($criterion.none_of // [])
                | all(. as $pattern | ($answer | test($pattern; "is") | not))
              )
            )
          }
      ] as $criteria
      | (
          ($variant != "optimized")
          or (
            $tool_accounting
            and $invocation_accounting
            and $repo_view_tool_accounting
            and $command_shape_valid
            and $mechanical_semantics_enforced
            and (
              $command_cap == 0
              or $budget_accounting
            )
          )
        ) as $accounting_pass
      | (
          ($variant != "optimized")
          or (
            $accounting_pass
            and $repo_view_calls >= 1
            and $changed_calls == 1
            and $first_invocation_changed
            and $navigation_semantics_valid
            and $bound_violations == 0
            and $budget_tamper == 0
            and (
              (($task | startswith("deep-")) | not)
              or (
                $find_calls >= 1
                and ($inspect_calls + $outline_calls) >= 1
                and $command_cap > 0
                and ($cap_exceeded | not)
                and $repo_view_calls <= $command_cap
              )
            )
          )
        ) as $navigation_pass
      | {
          name: $name,
          task: $task,
          variant: $variant,
          profile: $profile,
          navigation_required: (
            $variant == "optimized"
          ),
          accounting_pass: $accounting_pass,
          navigation_pass: $navigation_pass,
          navigation_calls: {
            total: $repo_view_calls,
            command_cap: $command_cap,
            command_cap_pass: (
              (
                (
                  $variant != "optimized"
                  or (($task | startswith("deep-")) | not)
                )
                and $command_cap == 0
              )
              or (
                $command_cap > 0
                and ($cap_exceeded | not)
                and $repo_view_calls <= $command_cap
              )
            ),
            command_cap_exceeded: $cap_exceeded,
            budget_tamper: $budget_tamper,
            changed: $changed_calls,
            find: $find_calls,
            inspect: $inspect_calls,
            outline: $outline_calls,
            bound_violations: $bound_violations
          },
          criteria: $criteria,
          passed_weight: ([$criteria[] | select(.passed) | .weight] | add // 0),
          total_weight: ([$criteria[].weight] | add // 0),
          score_percent: (
            (([$criteria[] | select(.passed) | .weight] | add // 0)
              / ([$criteria[].weight] | add)) * 100
          ),
          required_pass: (
            $accounting_pass
            and $navigation_pass
            and ([$criteria[] | select(.required and (.passed | not))] | length == 0)
          )
        }
    ' >> "${static_cases}"
done < <(
  jq -r '
    .cases[]
    | select(.completed)
    | [
        .name,
        .task,
        .variant,
        .profile,
        .answer_file,
        (.repo_view_invocation_count // 0),
        (.repo_view_invocation_cap // 0),
        (.repo_view_invocation_cap_exceeded // false),
        (.repo_view_budget_tamper_command_count // 0),
        (.repo_view_changed_invocation_count // 0),
        (.repo_view_find_invocation_count // 0),
        (.repo_view_inspect_invocation_count // 0),
        (.repo_view_outline_invocation_count // 0),
        (.repo_view_bound_violation_count // 0),
        .tool_call_accounting_valid,
        .repo_view_invocation_accounting_valid,
        .repo_view_tool_call_accounting_valid,
        .repo_view_budget_accounting_valid,
        .repo_view_command_shape_valid,
        .repo_view_first_invocation_changed,
        .repo_view_navigation_semantics_valid,
        .mechanical_navigation_semantics_enforced
      ]
    | @tsv
  ' "${metrics}"
)

static_output="${quality_scratch}/static.json"
jq -s '
  . as $cases
  | {
      schema_version: 1,
      cases: $cases,
      comparisons: [
        $cases[]
        | select(.variant == "optimized")
        | . as $candidate
        | ([
            $cases[]
            | select(.task == $candidate.task and .variant == "baseline")
          ] | first) as $baseline
        | select($baseline != null)
        | {
            task: $candidate.task,
            profile: $candidate.profile,
            baseline_score_percent: $baseline.score_percent,
            candidate_score_percent: $candidate.score_percent,
            navigation_required: $candidate.navigation_required,
            navigation_pass: $candidate.navigation_pass,
            accounting_pass: $candidate.accounting_pass,
            navigation_calls: $candidate.navigation_calls,
            required_pass: $candidate.required_pass,
            static_not_worse: (
              $candidate.required_pass
              and $candidate.score_percent >= $baseline.score_percent
            )
          }
      ]
  }
' "${static_cases}" > "${static_output}"

# Do not spend live judge calls on a candidate that already fails deterministic
# quality/navigation checks or has no positive effective-token saving. Judges
# cannot repair either condition, so continuing would only reproduce the
# expensive failed runs retained by the suite.
if "${enforce}" && [[ "${judge_repeats}" -gt 0 ]] &&
  ! jq -e \
    --slurpfile metrics "${metrics}" \
    '
      . as $static
      | [
          $static.cases[]
          | select(.variant == "optimized")
        ] as $candidates
      | ($candidates | length) > 0
        and all($static.comparisons[]; .static_not_worse)
        and all(
          $candidates[];
          .required_pass
        )
        and all(
          $candidates[];
          . as $candidate
          | [
              $metrics[0].comparisons[]
              | select(
                  .task == $candidate.task
                  and .profile == $candidate.profile
                )
            ] as $matching
          | ($matching | length) == 1
            and (
              $matching[0].effective_reduction_percent
              | type == "number" and . > 0
            )
        )
    ' "${static_output}" >/dev/null; then
  printf '%s\n' \
    'pre-judge quality gate failed; live judges were not started' >&2
  exit 1
fi

declare -A packet_for_profile=()
expected_packet_names=()
packet_set_valid=true
if "${manifest_selection_valid}"; then
  if [[ "${variant_selection}" == "optimized" ||
    "${variant_selection}" == "all" ]]; then
    for selected_profile in "${selected_profiles[@]}"; do
      if [[ "${selected_profile}" == "default" ]]; then
        default_packet_count=0
        for packet_name in \
          changed-packet-default.json \
          changed-packet.json; do
          if [[ -f "${run_dir_fd_path}/${packet_name}" ]]; then
            default_packet_count=$((default_packet_count + 1))
            packet_for_profile["${selected_profile}"]="${packet_name}"
            expected_packet_names+=("${packet_name}")
          fi
        done
        if [[ "${default_packet_count}" -ne 1 ]]; then
          packet_set_valid=false
        fi
      else
        packet_name="changed-packet-${selected_profile}.json"
        expected_packet_names+=("${packet_name}")
        packet_for_profile["${selected_profile}"]="${packet_name}"
        if [[ ! -f "${run_dir_fd_path}/${packet_name}" ]]; then
          packet_set_valid=false
        fi
      fi
    done
  fi
  expected_packet_inventory="$(
    printf '%s\n' "${expected_packet_names[@]}" |
      sed '/^$/d' |
      sort
  )"
  if [[ "${packet_inventory_before}" != "${expected_packet_inventory}" ]]; then
    packet_set_valid=false
  fi
  for packet_file in "${packet_files[@]}"; do
    packet_name="$(basename "${packet_file}")"
    packet_snapshot="${input_snapshot}/packets/${packet_name}"
    if ! jq -se \
      --arg root "${manifest_target_root}" \
      --arg head "${target_commit}" \
      --arg base "${target_base_commit}" \
      '
        length == 1
        and (
          .[0]
          | type == "object"
          and .root == $root
          and .head_commit == $head
          and .base_commit == $base
        )
      ' "${packet_snapshot}" >/dev/null 2>&1; then
      packet_set_valid=false
    fi
  done
fi
if "${require_strict_inputs}" && ! "${packet_set_valid}"; then
  printf 'changed packet set does not exactly match manifest profiles\n' >&2
  exit 1
fi

judge_source_root=""
isolated_git() {
  env \
    -u GIT_CONFIG \
    -u GIT_CONFIG_PARAMETERS \
    -u GIT_CONFIG_COUNT \
    -u GIT_DIR \
    -u GIT_WORK_TREE \
    -u GIT_INDEX_FILE \
    -u GIT_OBJECT_DIRECTORY \
    -u GIT_ALTERNATE_OBJECT_DIRECTORIES \
    -u GIT_COMMON_DIR \
    -u GIT_EXEC_PATH \
    -u GIT_EXTERNAL_DIFF \
    -u GIT_DIFF_OPTS \
    -u GIT_PAGER \
    -u GIT_SSH \
    -u GIT_SSH_COMMAND \
    -u GIT_SSH_VARIANT \
    -u GIT_ASKPASS \
    -u SSH_ASKPASS \
    -u GIT_PROXY_COMMAND \
    -u GIT_NAMESPACE \
    -u GIT_REPLACE_REF_BASE \
    -u GIT_CEILING_DIRECTORIES \
    -u GIT_DISCOVERY_ACROSS_FILESYSTEM \
    -u GIT_OPTIONAL_LOCKS \
    GIT_CONFIG_NOSYSTEM=1 \
    GIT_CONFIG_GLOBAL=/dev/null \
    GIT_ATTR_NOSYSTEM=1 \
    GIT_NO_REPLACE_OBJECTS=1 \
    GIT_TERMINAL_PROMPT=0 \
    GIT_OPTIONAL_LOCKS=0 \
    git \
      -c core.hooksPath=/dev/null \
      -c core.attributesFile=/dev/null \
      -c core.excludesFile=/dev/null \
      -c core.autocrlf=false \
      -c core.eol=lf \
      -c core.safecrlf=false \
      -c core.fsmonitor=false \
      -c core.untrackedCache=false \
      -c core.sparseCheckout=false \
      "$@"
}

if [[ "${judge_repeats}" -gt 0 ]]; then
  if [[ -z "${target_root}" ]] ||
    ! isolated_git -C "${target_root}" \
      rev-parse --is-inside-work-tree >/dev/null 2>&1; then
    printf 'judge target checkout does not exist: %s\n' "${target_root:-missing}" >&2
    exit 1
  fi
  target_root="$(cd "${target_root}" && pwd -P)"
  target_top_level="$(
    isolated_git -C "${target_root}" rev-parse --show-toplevel
  )"
  target_top_level="$(cd "${target_top_level}" && pwd -P)"
  if [[ "${target_root}" != "${target_top_level}" ]]; then
    printf 'judge target must be the Git top level: %s != %s\n' \
      "${target_root}" "${target_top_level}" >&2
    exit 1
  fi
  target_status="$(
    isolated_git -C "${target_root}" status \
      --porcelain=v1 \
      --untracked-files=all \
      --ignore-submodules=none
  )"
  if [[ -n "${target_status}" ]]; then
    printf 'judge target checkout is dirty: %s\n%s\n' \
      "${target_root}" "${target_status}" >&2
    exit 1
  fi
  actual_head="$(
    isolated_git -C "${target_root}" rev-parse --verify 'HEAD^{commit}'
  )"
  if [[ "${actual_head}" != "${target_commit}" ]]; then
    printf 'judge target HEAD mismatch: %s != %s\n' "${actual_head}" "${target_commit}" >&2
    exit 1
  fi
  resolved_base="$(
    isolated_git -C "${target_root}" \
      rev-parse --verify "${target_base_commit}^{commit}" 2>/dev/null ||
      true
  )"
  if [[ -z "${resolved_base}" ||
    "${resolved_base}" != "${target_base_commit}" ]]; then
    printf 'judge base commit is invalid or not canonical: %s\n' \
      "${target_base_commit}" >&2
    exit 1
  fi
  if ! isolated_git -C "${target_root}" \
    merge-base --is-ancestor "${resolved_base}" "${actual_head}"; then
    printf 'judge base commit is not an ancestor of target: %s !<= %s\n' \
      "${resolved_base}" "${actual_head}" >&2
    exit 1
  fi
  if isolated_git -C "${target_root}" ls-files --stage |
    awk '$1 == "160000" {found = 1} END {exit !found}'; then
    printf 'judge target contains submodules that cannot be materialized reproducibly\n' >&2
    exit 1
  fi
  codex_executable="$(command -v codex || true)"
  if [[ -n "${codex_executable}" ]]; then
    codex_executable="$(realpath -- "${codex_executable}")"
    actual_codex_version="$(
      env -i \
        PATH="/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin" \
        LANG=C \
        LC_ALL=C \
        TZ=UTC \
        "${codex_executable}" --version 2>/dev/null ||
        true
    )"
  else
    actual_codex_version=""
  fi
  if [[ "${actual_codex_version}" != "${judge_codex_version}" ]]; then
    printf 'judge Codex version mismatch: %s != %s\n' \
      "${actual_codex_version:-missing}" "${judge_codex_version}" >&2
    exit 1
  fi
  judge_codex_home="$(
    mktemp -d "${quality_scratch}/repo-view-quality-codex-home.XXXXXX"
  )"
  if [[ -n "${CODEX_HOME:-}" ]]; then
    codex_auth_source="${CODEX_HOME}/auth.json"
  elif [[ -n "${HOME:-}" ]]; then
    codex_auth_source="${HOME}/.codex/auth.json"
  else
    codex_auth_source=""
  fi
  codex_auth_canonical=""
  if [[ -n "${codex_auth_source}" &&
    -f "${codex_auth_source}" &&
    ! -L "${codex_auth_source}" ]]; then
    codex_auth_canonical="$(realpath -- "${codex_auth_source}")"
    ln -s -- "${codex_auth_source}" "${judge_codex_home}/auth.json"
  fi
  judge_checkout="$(mktemp -d "${quality_scratch}/judge-checkout.XXXXXX")"
  if ! isolated_git clone --quiet --no-hardlinks --no-checkout \
    "${target_root}" "${judge_checkout}"; then
    printf 'failed to create pristine judge checkout from %s\n' \
      "${target_root}" >&2
    exit 1
  fi
  isolated_git -C "${judge_checkout}" \
    -c advice.detachedHead=false \
    checkout --quiet --detach "${target_commit}"
  checkout_head="$(
    isolated_git -C "${judge_checkout}" rev-parse --verify 'HEAD^{commit}'
  )"
  checkout_status="$(
    isolated_git -C "${judge_checkout}" status \
      --porcelain=v1 \
      --untracked-files=all \
      --ignore-submodules=none
  )"
  if [[ "${checkout_head}" != "${target_commit}" ||
    -n "${checkout_status}" ]]; then
    printf 'pristine judge checkout verification failed: %s\n' \
      "${judge_checkout}" >&2
    exit 1
  fi
  judge_source_root="${judge_checkout}"
fi

verify_judge_checkout() {
  local current_head current_status
  [[ -n "${judge_source_root}" ]] || return 0
  current_head="$(
    isolated_git -C "${judge_source_root}" rev-parse --verify 'HEAD^{commit}'
  )"
  current_status="$(
    isolated_git -C "${judge_source_root}" status \
      --porcelain=v1 \
      --untracked-files=all \
      --ignore-submodules=none
  )"
  if [[ "${current_head}" != "${target_commit}" ||
    -n "${current_status}" ]]; then
    printf 'pristine judge checkout changed during quality check: %s\n' \
      "${judge_source_root}" >&2
    return 1
  fi
}

feature_flags=(
  --disable multi_agent
  --disable multi_agent_v2
  --disable enable_fanout
  --disable collaboration_modes
  --disable hooks
  --disable tool_router
  --disable workflows
  --disable code_mode
  --disable code_mode_host
  --disable code_mode_only
)
judge_tool_path="/usr/local/go/bin:/usr/local/bin:/usr/bin:/bin"
if ! actual_judge_go_version="$(
  env \
    PATH="${judge_tool_path}" \
    GOENV=off \
    GOTOOLCHAIN=local \
    GOWORK=off \
    GOFLAGS='-mod=readonly -trimpath -buildvcs=false' \
    go version
)"; then
  printf 'failed to resolve judge Go version\n' >&2
  exit 1
fi
if ! judge_go_environment="$(
  env \
    PATH="${judge_tool_path}" \
    GOENV=off \
    GOTOOLCHAIN=local \
    GOWORK=off \
    GOFLAGS='-mod=readonly -trimpath -buildvcs=false' \
    go env -json GOROOT GOMODCACHE
)"; then
  printf 'failed to resolve judge Go environment\n' >&2
  exit 1
fi
judge_go_root="$(
  jq -er '.GOROOT | select(type == "string" and length > 0)' \
    <<< "${judge_go_environment}"
)"
judge_go_mod_cache="$(
  jq -er '.GOMODCACHE | select(type == "string" and length > 0)' \
    <<< "${judge_go_environment}"
)"
judge_tool_root="${quality_scratch}/judge-tools"
judge_shell_home="${judge_tool_root}/home"
judge_go_path="${judge_tool_root}/gopath"
judge_go_cache="${judge_tool_root}/gocache"
judge_tmpdir="${judge_tool_root}/tmp"
mkdir -p \
  "${judge_shell_home}" \
  "${judge_go_path}" \
  "${judge_go_cache}" \
  "${judge_tmpdir}"

toml_string() {
  jq -Rn --arg value "$1" '$value'
}

permission_checkout_root="${judge_source_root:-${input_snapshot}}"
permission_checkout_root_toml="$(toml_string "${permission_checkout_root}")"
permission_input_root_toml="$(toml_string "${input_snapshot}")"
permission_go_root_toml="$(toml_string "${judge_go_root}")"
permission_go_mod_cache_toml="$(toml_string "${judge_go_mod_cache}")"
permission_tool_root_toml="$(toml_string "${judge_tool_root}")"
permission_codex_executable_toml="$(
  toml_string "${codex_executable:-${quality_scratch}/no-codex-executable}"
)"
permission_codex_home_toml="$(toml_string "${judge_codex_home:-${quality_scratch}/no-codex-home}")"
if [[ -n "${codex_auth_canonical:-}" ]]; then
  permission_auth_toml="$(toml_string "${codex_auth_canonical}")"
  judge_permissions_config="$(
    printf \
      'permissions.quality-audit={extends=":read-only", filesystem={":root"="deny", ":minimal"="read", %s="read", %s="read", %s="read", %s="read", %s="write", %s="read", %s="deny", %s="deny"}}' \
      "${permission_checkout_root_toml}" \
      "${permission_input_root_toml}" \
      "${permission_go_root_toml}" \
      "${permission_go_mod_cache_toml}" \
      "${permission_tool_root_toml}" \
      "${permission_codex_executable_toml}" \
      "${permission_codex_home_toml}" \
      "${permission_auth_toml}"
  )"
else
  judge_permissions_config="$(
    printf \
      'permissions.quality-audit={extends=":read-only", filesystem={":root"="deny", ":minimal"="read", %s="read", %s="read", %s="read", %s="read", %s="write", %s="read", %s="deny"}}' \
      "${permission_checkout_root_toml}" \
      "${permission_input_root_toml}" \
      "${permission_go_root_toml}" \
      "${permission_go_mod_cache_toml}" \
      "${permission_tool_root_toml}" \
      "${permission_codex_executable_toml}" \
      "${permission_codex_home_toml}"
  )"
fi
judge_path_toml="$(toml_string "${judge_tool_path}")"
judge_shell_home_toml="$(toml_string "${judge_shell_home}")"
judge_go_path_toml="$(toml_string "${judge_go_path}")"
judge_go_cache_toml="$(toml_string "${judge_go_cache}")"
judge_tmpdir_toml="$(toml_string "${judge_tmpdir}")"
judge_shell_environment_config="$(
  printf '%s' \
    "shell_environment_policy.set={PATH=${judge_path_toml}," \
    "HOME=${judge_shell_home_toml},TMPDIR=${judge_tmpdir_toml}," \
    'LANG="C",LC_ALL="C",TZ="UTC",' \
    "GOROOT=${permission_go_root_toml}," \
    "GOPATH=${judge_go_path_toml}," \
    "GOMODCACHE=${permission_go_mod_cache_toml}," \
    "GOCACHE=${judge_go_cache_toml}," \
    'GOENV="off",GOTOOLCHAIN="local",' \
    'GOWORK="off",GOFLAGS="-mod=readonly -trimpath -buildvcs=false",' \
    'GIT_CONFIG_NOSYSTEM="1",GIT_CONFIG_GLOBAL="/dev/null",' \
    'GIT_ATTR_NOSYSTEM="1",GIT_CONFIG_COUNT="10",' \
    'GIT_CONFIG_KEY_0="core.hooksPath",GIT_CONFIG_VALUE_0="/dev/null",' \
    'GIT_CONFIG_KEY_1="core.attributesFile",GIT_CONFIG_VALUE_1="/dev/null",' \
    'GIT_CONFIG_KEY_2="core.excludesFile",GIT_CONFIG_VALUE_2="/dev/null",' \
    'GIT_CONFIG_KEY_3="core.autocrlf",GIT_CONFIG_VALUE_3="false",' \
    'GIT_CONFIG_KEY_4="core.eol",GIT_CONFIG_VALUE_4="lf",' \
    'GIT_CONFIG_KEY_5="core.safecrlf",GIT_CONFIG_VALUE_5="false",' \
    'GIT_CONFIG_KEY_6="core.fsmonitor",GIT_CONFIG_VALUE_6="false",' \
    'GIT_CONFIG_KEY_7="core.untrackedCache",GIT_CONFIG_VALUE_7="false",' \
    'GIT_CONFIG_KEY_8="core.sparseCheckout",GIT_CONFIG_VALUE_8="false",' \
    'GIT_CONFIG_KEY_9="core.filemode",GIT_CONFIG_VALUE_9="true",' \
    'GIT_TERMINAL_PROMPT="0",GIT_PAGER="cat",PAGER="cat"}'
)"
codex_isolation_flags=(
  --ignore-user-config
  --ignore-rules
  -c 'default_permissions="quality-audit"'
  -c "${judge_permissions_config}"
  -c 'shell_environment_policy.inherit="none"'
  -c 'shell_environment_policy.ignore_default_excludes=false'
  -c 'shell_environment_policy.experimental_use_profile=false'
  -c "${judge_shell_environment_config}"
  -c 'project_doc_max_bytes=0'
  -c 'project_doc_fallback_filenames=[]'
  -c 'mcp_servers={}'
  -c 'apps._default.enabled=false'
)
codex_isolation_semantics=(
  'ignore-user-config=true'
  'ignore-rules=true'
  'default-permissions=quality-audit'
  'filesystem=:root=deny;:minimal=read;<judge-checkout>=read;<quality-input-snapshot>=read;<goroot>=read;<gomodcache>=read;<judge-tool-root>=write;<codex-executable>=read;<codex-home>=deny;<canonical-auth>=deny-when-present'
  'network=disabled'
  "outer-environment=inherit-none;PATH=${judge_tool_path};HOME=<judge-tool-root>/home;TMPDIR=<judge-tool-root>/tmp;LANG=C;LC_ALL=C;TZ=UTC;CODEX_HOME=<private-codex-home>;auth=staged-auth-json-only"
  'shell-environment-inherit=none'
  "shell-PATH=${judge_tool_path}"
  'shell-HOME=<judge-tool-root>/home'
  'shell-TMPDIR=<judge-tool-root>/tmp'
  'shell-LANG=C;LC_ALL=C;TZ=UTC'
  'shell-GOENV=off;GOTOOLCHAIN=local;GOWORK=off;GOFLAGS=-mod=readonly -trimpath -buildvcs=false'
  'shell-GOROOT=<goroot>;GOPATH=<judge-tool-root>/gopath;GOMODCACHE=<gomodcache>;GOCACHE=<judge-tool-root>/gocache'
  'shell-Git=system/global/attributes disabled;hooks/attributes/excludes/fsmonitor/untracked-cache/sparse-checkout disabled;terminal prompt disabled'
  "go-version=${actual_judge_go_version}"
)
judge_environment_metadata="${quality_scratch}/judge-environment.json"
jq -n \
  --arg go_version "${actual_judge_go_version}" \
  --arg path "${judge_tool_path}" \
  '{
    go_version: $go_version,
    permission_profile: "quality-audit",
    filesystem: {
      root: "deny",
      minimal_runtime: "read",
      judge_checkout: "read",
      quality_input_snapshot: "read",
      goroot: "read",
      gomodcache: "read",
      judge_tool_root: "write",
      codex_home: "deny",
      canonical_auth: "deny-when-present"
    },
    network: "disabled",
    outer_environment: {
      inherit: "none",
      PATH: $path,
      HOME: "<judge-tool-root>/home",
      TMPDIR: "<judge-tool-root>/tmp",
      LANG: "C",
      LC_ALL: "C",
      TZ: "UTC",
      CODEX_HOME: "<private-codex-home>",
      auth: "staged-auth-json-only"
    },
    shell_environment: {
      inherit: "none",
      PATH: $path,
      HOME: "<judge-tool-root>/home",
      TMPDIR: "<judge-tool-root>/tmp",
      LANG: "C",
      LC_ALL: "C",
      TZ: "UTC",
      GOROOT: "<goroot>",
      GOPATH: "<judge-tool-root>/gopath",
      GOMODCACHE: "<gomodcache>",
      GOCACHE: "<judge-tool-root>/gocache",
      GOENV: "off",
      GOTOOLCHAIN: "local",
      GOWORK: "off",
      GOFLAGS: "-mod=readonly -trimpath -buildvcs=false",
      git_configuration: "hardened"
    }
  }' > "${judge_environment_metadata}"
judge_attempt_limit=3

judge_output_valid() {
  local output="$1"
  local expected_task="$2"
  local expected_baseline="$3"
  local expected_candidates="$4"

  [[ -s "${output}" ]] &&
    jq -se \
      --arg task "${expected_task}" \
      --arg baseline "${expected_baseline}" \
      --argjson candidates "${expected_candidates}" \
      '
        def exact_object($expected_keys):
          type == "object"
          and ((keys | sort) == ($expected_keys | sort));
        def integer_score:
          type == "number"
          and floor == .
          and . >= 1
          and . <= 5;
        def string_array:
          type == "array"
          and all(.[]; type == "string");
        def score:
          exact_object([
            "name",
            "correctness",
            "completeness",
            "grounding",
            "task_adherence",
            "critical_omissions",
            "unsupported_claims"
          ])
          and (.name | type == "string")
          and (.correctness | integer_score)
          and (.completeness | integer_score)
          and (.grounding | integer_score)
          and (.task_adherence | integer_score)
          and (.critical_omissions | string_array)
          and (.unsupported_claims | string_array);
        def candidate:
          exact_object([
            "name",
            "correctness",
            "completeness",
            "grounding",
            "task_adherence",
            "critical_omissions",
            "unsupported_claims",
            "core_conclusion_matches_baseline",
            "material_contradictions",
            "baseline_material_points_omitted",
            "candidate_material_additions",
            "not_worse_than_baseline",
            "rationale"
          ])
          and (.name | type == "string")
          and (.correctness | integer_score)
          and (.completeness | integer_score)
          and (.grounding | integer_score)
          and (.task_adherence | integer_score)
          and (.critical_omissions | string_array)
          and (.unsupported_claims | string_array)
          and (.core_conclusion_matches_baseline | type == "boolean")
          and (.material_contradictions | string_array)
          and (.baseline_material_points_omitted | string_array)
          and (.candidate_material_additions | string_array)
          and (.not_worse_than_baseline | type == "boolean")
          and (.rationale | type == "string" and length > 0);

        length == 1
        and (
          .[0]
          | . as $document
          | try (
              exact_object(["task", "baseline", "candidates"])
              and (.task | type == "string")
              and (.task == $task)
              and (
                $task == "explain"
                or $task == "review"
                or $task == "deep-explain"
                or $task == "deep-review"
              )
              and (.baseline | score)
              and (.baseline.name == $baseline)
              and (.candidates | type == "array")
              and all(
                .candidates[];
                candidate
                and (
                  (.not_worse_than_baseline == false)
                  or (
                    .correctness >= $document.baseline.correctness
                    and .completeness >= $document.baseline.completeness
                    and .grounding >= $document.baseline.grounding
                    and .task_adherence >= $document.baseline.task_adherence
                    and (.critical_omissions | length) == 0
                    and (.unsupported_claims | length) == 0
                    and (.material_contradictions | length) == 0
                    and (.baseline_material_points_omitted | length) == 0
                  )
                )
              )
              and ([.candidates[].name] | sort) == ($candidates | sort)
            ) catch false
        )
      ' \
      "${output}" >/dev/null 2>&1
}

render_judge_prompt() {
  local prompt_target_root="$1"
  local prompt_target_commit="$2"
  local prompt_rubric="$3"
  local prompt_baseline_answer="$4"
  local prompt_baseline_transcript="$5"
  local prompt_candidate_list="$6"
  local prompt_task="$7"
  local prompt_baseline_name="$8"
  local prompt_candidate_names="$9"

  cat <<EOF
Act as an independent code-review quality evaluator. The authoritative source checkout is ${prompt_target_root} at commit ${prompt_target_commit}. Read the task rubric at ${prompt_rubric}, the baseline answer at ${prompt_baseline_answer}, its raw transcript at ${prompt_baseline_transcript}, and each candidate's answer, transcript, and changed packet:
${prompt_candidate_list}
Independently inspect any source in the authoritative checkout needed to verify claims. Each changed packet is that candidate profile's navigation output, not the evaluator's sole ground truth. A claim supported by the source or its answer's raw transcript is grounded even when it is absent from its changed packet. Validate claims about executed commands, tests, or sandbox failures from that answer's transcript. Do not treat a different checkout prefix as broken when the linked file exists at the same commit.

Mandatory evaluator input protocol:
1. Read the rubric, baseline answer, every candidate changed packet, each candidate answer, and transcripts in separate commands. Never concatenate multiple evaluator inputs into one command output. Issue exactly one shell command at a time and wait for its completed result before issuing the next; parallel command execution invalidates the audit.
2. Read each baseline and candidate answer through EOF before scoring. Use a line count and bounded chunks when needed, and verify the final chunk was seen.
3. If any command output is truncated, issue narrower reads for the missing content before drawing a conclusion.
4. Before reporting a critical omission, unsupported negative claim, material contradiction, or baseline point omitted, search the candidate answer directly for each supposedly missing concept and read the matching section. Do not infer omission from an earlier truncated read.

Score every answer against the authoritative source and requested ${prompt_task} task. The baseline is only a comparator, not ground truth. Do not reward verbosity. Penalize factual errors, genuinely unsupported claims, missed required behavior or findings, and failure to answer the task. For each candidate, also compare behavior to baseline: core_conclusion_matches_baseline is true when the main technical conclusion and finding set align; material_contradictions contains only candidate claims contradicted by authoritative source or by the candidate's own answer/transcript; baseline_material_points_omitted contains only correct material baseline content the candidate loses without equally strong substitute coverage; candidate_material_additions lists material correct content the candidate adds. A correct candidate correction of a baseline error is a candidate_material_addition, never a material_contradiction. Set not_worse_than_baseline true only when the candidate has no material correctness, completeness, grounding, or task-adherence regression. Output task exactly ${prompt_task}, baseline name exactly ${prompt_baseline_name}, and exactly these candidate names: ${prompt_candidate_names}. Every score must be an integer from 1 through 5; never emit zero placeholder scores or omit a candidate. Return JSON matching the provided schema. Read only and do not modify files.
EOF
}

json_digest() {
  jq -cS . "$1" | sha256sum | awk '{print $1}'
}

packet_digest() {
  jq -cS '
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
  ' "$1" | sha256sum | awk '{print $1}'
}

judge_input_digest() {
  local judge_identity="$1"
  local prompt_semantics="$2"
  local target_commit="$3"
  local expected_task="$4"
  local expected_baseline="$5"
  local expected_candidates="$6"
  local rubric_file="$7"
  local schema_file="$8"
  local baseline_answer_file="$9"
  local baseline_transcript_file="${10}"
  shift 10
  local answer_file candidate_index input_hash packet_file transcript_file

  if (( $# % 3 != 0 )); then
    return 1
  fi

  {
    printf 'judge-cache-schema\0%s\0' "${judge_cache_schema}"
    printf 'judge-identity\0%s\0' "${judge_identity}"
    printf 'target-commit\0%s\0' "${target_commit}"
    printf 'task\0%s\0' "${expected_task}"
    printf 'baseline\0%s\0' "${expected_baseline}"
    printf 'candidate-identities\0%s\0' "${expected_candidates}"
    printf 'developer-instructions\0%s\0' "${no_collaboration}"
    printf 'evaluator-config\0%s\0' \
      "codex-exec;private-codex-home=auth-only-and-tool-denied;${judge_model_configuration};codex-version=${judge_codex_version};permissions=quality-audit;ephemeral=true;json=true;output-schema=true"
    printf 'prompt-semantics\0%s\0' "${prompt_semantics}"
    for input_hash in "${feature_flags[@]}"; do
      printf 'feature-flag\0%s\0' "${input_hash}"
    done
    for input_hash in "${codex_isolation_semantics[@]}"; do
      printf 'isolation-semantics\0%s\0' "${input_hash}"
    done

    input_hash="$(json_digest "${rubric_file}")"
    printf 'json-input\0quality-rubric\0%s\0' "${input_hash}"
    input_hash="$(json_digest "${schema_file}")"
    printf 'json-input\0quality-output-schema\0%s\0' "${input_hash}"
    input_hash="$(file_digest "${baseline_answer_file}")"
    printf 'file-input\0baseline-answer\0%s\0' "${input_hash}"
    input_hash="$(file_digest "${baseline_transcript_file}")"
    printf 'file-input\0baseline-transcript\0%s\0' "${input_hash}"

    candidate_index=0
    while [[ $# -gt 0 ]]; do
      answer_file="$1"
      transcript_file="$2"
      packet_file="$3"
      shift 3
      input_hash="$(file_digest "${answer_file}")"
      printf 'file-input\0candidate-answer-%s\0%s\0' \
        "${candidate_index}" "${input_hash}"
      input_hash="$(file_digest "${transcript_file}")"
      printf 'file-input\0candidate-transcript-%s\0%s\0' \
        "${candidate_index}" "${input_hash}"
      input_hash="$(packet_digest "${packet_file}")"
      printf 'json-input\0candidate-packet-%s\0%s\0' \
        "${candidate_index}" "${input_hash}"
      candidate_index=$((candidate_index + 1))
    done
  } | sha256sum | awk '{print $1}'
}

legacy_judge_input_digest() {
  local artifact_identity="$1"
  local prompt_semantics="$2"
  local target_commit="$3"
  local expected_task="$4"
  local expected_baseline="$5"
  local expected_candidates="$6"
  local rubric_file="$7"
  local schema_file="$8"
  local baseline_answer_file="$9"
  local baseline_transcript_file="${10}"
  shift 10
  local answer_file candidate_index input_hash packet_file transcript_file

  if (( $# % 3 != 0 )); then
    return 1
  fi

  {
    printf 'legacy-judge-attestation-schema\0%s\0' \
      "${legacy_judge_attestation_schema}"
    printf 'status\0legacy-unisolated-attested\0'
    printf 'artifact-identity\0%s\0' "${artifact_identity}"
    printf 'model\0unknown\0'
    printf 'codex-version\0unknown\0'
    printf 'isolation\0legacy-unisolated\0'
    printf 'target-commit\0%s\0' "${target_commit}"
    printf 'task\0%s\0' "${expected_task}"
    printf 'baseline\0%s\0' "${expected_baseline}"
    printf 'candidate-identities\0%s\0' "${expected_candidates}"
    printf 'prompt-semantics\0%s\0' "${prompt_semantics}"

    input_hash="$(json_digest "${rubric_file}")"
    printf 'json-input\0quality-rubric\0%s\0' "${input_hash}"
    input_hash="$(json_digest "${schema_file}")"
    printf 'json-input\0quality-output-schema\0%s\0' "${input_hash}"
    input_hash="$(file_digest "${baseline_answer_file}")"
    printf 'file-input\0baseline-answer\0%s\0' "${input_hash}"
    input_hash="$(file_digest "${baseline_transcript_file}")"
    printf 'file-input\0baseline-transcript\0%s\0' "${input_hash}"

    candidate_index=0
    while [[ $# -gt 0 ]]; do
      answer_file="$1"
      transcript_file="$2"
      packet_file="$3"
      shift 3
      input_hash="$(file_digest "${answer_file}")"
      printf 'file-input\0candidate-answer-%s\0%s\0' \
        "${candidate_index}" "${input_hash}"
      input_hash="$(file_digest "${transcript_file}")"
      printf 'file-input\0candidate-transcript-%s\0%s\0' \
        "${candidate_index}" "${input_hash}"
      input_hash="$(packet_digest "${packet_file}")"
      printf 'json-input\0candidate-packet-%s\0%s\0' \
        "${candidate_index}" "${input_hash}"
      candidate_index=$((candidate_index + 1))
    done
  } | sha256sum | awk '{print $1}'
}

judge_log_valid() {
  generation_log_valid "$1"
}

judge_output_matches_log() {
  local output="$1"
  local log="$2"
  jq -se \
    --slurpfile output_document "${output}" \
    '
      (
        [
          .[]
          | select(
              .type == "item.completed"
              and .item.type == "agent_message"
              and (.item.text | type == "string")
            )
          | .item.text
        ]
        | last
        | fromjson
      ) == $output_document[0]
    ' "${log}" >/dev/null 2>&1
}

judge_result_digest() {
  local judge_identity="$1"
  local input_digest="$2"
  local output="$3"
  local log="$4"
  local exit_code_file="$5"
  local output_hash log_hash exit_code

  output_hash="$(file_digest "${output}")"
  log_hash="$(file_digest "${log}")"
  exit_code="$(<"${exit_code_file}")"
  {
    printf 'judge-cache-schema\0%s\0' "${judge_cache_schema}"
    printf 'judge-identity\0%s\0' "${judge_identity}"
    printf 'input-digest\0%s\0' "${input_digest}"
    printf 'output-sha256\0%s\0' "${output_hash}"
    printf 'jsonl-sha256\0%s\0' "${log_hash}"
    printf 'exit-code\0%s\0' "${exit_code}"
  } | sha256sum | awk '{print $1}'
}

judge_cache_valid() {
  local judge_identity="$1"
  local output="$2"
  local log="$3"
  local digest_file="$4"
  local result_digest_file="$5"
  local exit_code_file="$6"
  local expected_digest="$7"
  local expected_task="$8"
  local expected_baseline="$9"
  local expected_candidates="${10}"
  local stored_digest stored_result expected_result

  [[ -s "${exit_code_file}" ]] || return 1
  [[ "$(<"${exit_code_file}")" == "0" ]] || return 1
  [[ -s "${digest_file}" ]] || return 1
  [[ -s "${result_digest_file}" ]] || return 1
  stored_digest="$(<"${digest_file}")"
  [[ "${stored_digest}" == "${expected_digest}" ]] || return 1
  judge_output_valid \
    "${output}" \
    "${expected_task}" \
    "${expected_baseline}" \
    "${expected_candidates}" || return 1
  judge_log_valid "${log}" || return 1
  judge_output_matches_log "${output}" "${log}" || return 1
  expected_result="$(
    judge_result_digest \
      "${judge_identity}" \
      "${expected_digest}" \
      "${output}" \
      "${log}" \
      "${exit_code_file}"
  )"
  stored_result="$(<"${result_digest_file}")"
  [[ "${stored_result}" == "${expected_result}" ]]
}

legacy_attestation_valid() {
  local attestation="$1"
  local artifact_identity="$2"
  local input_digest="$3"
  local output="$4"
  local log="$5"
  local exit_code_file="$6"
  local output_hash log_hash exit_code_hash

  [[ -s "${attestation}" ]] || return 1
  output_hash="$(file_digest "${output}")"
  log_hash="$(file_digest "${log}")"
  exit_code_hash="$(file_digest "${exit_code_file}")"
  jq -se \
    --argjson schema_version "${legacy_judge_attestation_schema}" \
    --arg artifact_identity "${artifact_identity}" \
    --arg input_sha256 "${input_digest}" \
    --arg output_sha256 "${output_hash}" \
    --arg transcript_sha256 "${log_hash}" \
    --arg exit_code_sha256 "${exit_code_hash}" \
    '
      length == 1
      and (
        .[0]
        | type == "object"
        and (
          (keys | sort)
          == ([
            "artifact_identity",
            "exit_code",
            "exit_code_sha256",
            "input_sha256",
            "provenance",
            "schema_version",
            "status",
            "transcript_sha256",
            "transcript_validation",
            "output_sha256"
          ] | sort)
        )
        and .schema_version == $schema_version
        and .status == "legacy-unisolated-attested"
        and .artifact_identity == $artifact_identity
        and .input_sha256 == $input_sha256
        and .output_sha256 == $output_sha256
        and .transcript_sha256 == $transcript_sha256
        and .exit_code_sha256 == $exit_code_sha256
        and .exit_code == 0
        and (
          .provenance
          | type == "object"
          and (keys | sort)
            == ["codex_version", "isolation", "model", "operator_action"]
          and .model == "unknown"
          and .codex_version == "unknown"
          and .isolation == "legacy-unisolated"
          and .operator_action == "--bind-legacy-judges"
        )
        and (
          .transcript_validation
          | type == "object"
          and (keys | sort)
            == [
              "contract",
              "output_matches_final_agent_message",
              "schema_valid_numeric_output"
            ]
          and .contract
            == "ordered-lifecycle-and-command-pairing-v1"
          and .output_matches_final_agent_message == true
          and .schema_valid_numeric_output == true
        )
      )
    ' "${attestation}" >/dev/null 2>&1
}

write_legacy_attestation() {
  local destination="$1"
  local artifact_identity="$2"
  local input_digest="$3"
  local output="$4"
  local log="$5"
  local exit_code_file="$6"
  local destination_name temporary

  destination_name="$(basename "${destination}")"
  temporary="$(
    mktemp "${quality_dir_fd_path}/${destination_name}.tmp.XXXXXX"
  )"
  jq -n \
    --argjson schema_version "${legacy_judge_attestation_schema}" \
    --arg artifact_identity "${artifact_identity}" \
    --arg input_sha256 "${input_digest}" \
    --arg output_sha256 "$(file_digest "${output}")" \
    --arg transcript_sha256 "$(file_digest "${log}")" \
    --arg exit_code_sha256 "$(file_digest "${exit_code_file}")" \
    '{
      schema_version: $schema_version,
      status: "legacy-unisolated-attested",
      artifact_identity: $artifact_identity,
      provenance: {
        model: "unknown",
        codex_version: "unknown",
        isolation: "legacy-unisolated",
        operator_action: "--bind-legacy-judges"
      },
      input_sha256: $input_sha256,
      output_sha256: $output_sha256,
      transcript_sha256: $transcript_sha256,
      exit_code: 0,
      exit_code_sha256: $exit_code_sha256,
      transcript_validation: {
        contract: "ordered-lifecycle-and-command-pairing-v1",
        output_matches_final_agent_message: true,
        schema_valid_numeric_output: true
      }
    }' > "${temporary}"
  mv -T -- "${temporary}" "${quality_dir_fd_path}/${destination_name}"
}

retain_invalid_judge() {
  local stem="$1"
  local source_path destination_path stamp
  stamp="$(date -u '+%Y%m%dT%H%M%S.%NZ')"
  for suffix in \
    json \
    jsonl \
    stderr \
    exit-code \
    inputs.sha256 \
    result.sha256 \
    legacy-attestation.json; do
    source_path="${quality_dir_fd_path}/${stem}.${suffix}"
    destination_path="$(
      printf '%s/rejected-%s-%s.%s' \
        "${quality_dir_fd_path}" "${stem}" "${stamp}" "${suffix}"
    )"
    if [[ -e "${source_path}" ]]; then
      mv -T -- \
        "${source_path}" \
        "${destination_path}"
    fi
  done
}

mapfile -t evaluated_tasks < <(
  jq -r '.cases[] | select(.completed) | .task' "${metrics}" | sort -u
)
judge_files=()
judge_logs=()
binding_eligible_count=0

write_atomic_line() {
  local destination="$1"
  local value="$2"
  local destination_name temporary
  destination_name="$(basename "${destination}")"
  temporary="$(
    mktemp "${quality_dir_fd_path}/${destination_name}.tmp.XXXXXX"
  )"
  printf '%s\n' "${value}" > "${temporary}"
  mv -T -- "${temporary}" "${quality_dir_fd_path}/${destination_name}"
}

snapshot_judge_run() {
  local stem="$1"
  local provenance="$2"
  local suffix source
  local suffixes=(json jsonl exit-code)
  if [[ "${provenance}" == "current" ]]; then
    suffixes+=(inputs.sha256 result.sha256)
  elif [[ "${provenance}" == "legacy-unisolated-attested" ]]; then
    suffixes+=(legacy-attestation.json)
  else
    printf 'unknown judge provenance for %s: %s\n' \
      "${stem}" "${provenance}" >&2
    exit 1
  fi
  for suffix in "${suffixes[@]}"; do
    source="${quality_dir_fd_path}/${stem}.${suffix}"
    snapshot_input "${source}" "judges/${stem}.${suffix}"
  done
  judge_files+=("${input_snapshot}/judges/${stem}.json")
  judge_logs+=("${input_snapshot}/judges/${stem}.jsonl")
}

for task in "${evaluated_tasks[@]}"; do
  baseline_answer="${input_snapshot}/answers/baseline-${task}.md"
  [[ -f "${baseline_answer}" ]] || continue
  baseline_transcript="${input_snapshot}/baseline-${task}.jsonl"
  judge_inputs_complete=true
  if ! "${manifest_selection_valid}" ||
    ! "${matrix_complete}" ||
    ! "${packet_set_valid}"; then
    judge_inputs_complete=false
  fi
  if [[ ! -s "${baseline_transcript}" ]]; then
    judge_inputs_complete=false
  fi
  if { [[ "${judge_repeats}" -gt 0 ]] || "${bind_legacy_judges}"; } &&
    [[ ! -s "${baseline_transcript}" ]]; then
    printf 'missing baseline transcript: %s\n' "${baseline_transcript}" >&2
    exit 1
  fi
  mapfile -t candidate_records < <(
    jq -r \
      --arg task "${task}" \
      '
        .cases[]
        | select(
            .completed
            and .variant == "optimized"
            and .task == $task
          )
        | [.name, .profile, .answer_file]
        | @tsv
      ' "${metrics}" |
      sort
  )
  [[ ${#candidate_records[@]} -gt 0 ]] || continue

  candidate_list=""
  candidate_semantic_list=""
  candidate_names=()
  candidate_input_files=()
  candidate_index=0
  for candidate_record in "${candidate_records[@]}"; do
    IFS=$'\t' read -r candidate_name candidate_profile answer_file \
      <<< "${candidate_record}"
    answer="${input_snapshot}/${answer_file}"
    candidate_names+=("${candidate_name}")
    candidate_transcript="${input_snapshot}/${candidate_name}.jsonl"
    candidate_packet_name="${packet_for_profile[${candidate_profile}]:-}"
    candidate_packet=""
    if [[ -n "${candidate_packet_name}" ]]; then
      candidate_packet="${input_snapshot}/packets/${candidate_packet_name}"
    fi
    if [[ ! -s "${answer}" ||
      ! -s "${candidate_transcript}" ||
      -z "${candidate_packet}" ]]; then
      judge_inputs_complete=false
    fi
    if { [[ "${judge_repeats}" -gt 0 ]] || "${bind_legacy_judges}"; } &&
      [[ ! -s "${answer}" ]]; then
      printf 'missing candidate answer: %s\n' "${answer}" >&2
      exit 1
    fi
    if { [[ "${judge_repeats}" -gt 0 ]] || "${bind_legacy_judges}"; } &&
      [[ ! -s "${candidate_transcript}" ]]; then
      printf 'missing candidate transcript: %s\n' \
        "${candidate_transcript}" >&2
      exit 1
    fi
    if { [[ "${judge_repeats}" -gt 0 ]] || "${bind_legacy_judges}"; } &&
      [[ -z "${candidate_packet}" ]]; then
      printf 'missing changed packet for candidate %s (profile %s)\n' \
        "${candidate_name}" "${candidate_profile}" >&2
      exit 1
    fi
    candidate_input_files+=(
      "${answer}"
      "${candidate_transcript}"
      "${candidate_packet}"
    )
    candidate_list+="- ${candidate_name}: answer=${answer}; transcript=${candidate_transcript}; changed_packet=${candidate_packet}"$'\n'
    candidate_semantic_list+="- ${candidate_name}: answer=<candidate-answer-${candidate_index}>; transcript=<candidate-transcript-${candidate_index}>; changed_packet=<candidate-packet-${candidate_index}>"$'\n'
    candidate_index=$((candidate_index + 1))
  done
  candidate_names_json="$(
    printf '%s\n' "${candidate_names[@]}" | jq -R . | jq -cs .
  )"
  prompt=""
  prompt_semantics=""
  if "${judge_inputs_complete}"; then
    prompt="$(
      render_judge_prompt \
        "${judge_source_root:-${target_root}}" \
        "${target_commit}" \
        "${rubric}" \
        "${baseline_answer}" \
        "${baseline_transcript}" \
        "${candidate_list}" \
        "${task}" \
        "baseline-${task}" \
        "${candidate_names[*]}"
    )"
    prompt_semantics="$(
      render_judge_prompt \
        "<target-root>" \
        "${target_commit}" \
        "<quality-rubric>" \
        "<baseline-answer>" \
        "<baseline-transcript>" \
        "${candidate_semantic_list}" \
        "${task}" \
        "baseline-${task}" \
        "${candidate_names[*]}"
    )"
  fi

  if [[ "${judge_repeats}" -eq 0 ]]; then
    if ! "${judge_inputs_complete}"; then
      continue
    fi
    parked_judge_outputs=()
    while IFS= read -r -d '' parked_judge_output; do
      parked_judge_outputs+=("${parked_judge_output}")
    done < <(
      find "${quality_dir_fd_path}/." -maxdepth 1 -type f \
        -name "judge-${task}-*.json" -print0 |
        sort -z
    )
    for parked_judge_output in "${parked_judge_outputs[@]}"; do
      parked_judge_name="$(basename "${parked_judge_output}" .json)"
      parked_repeat="${parked_judge_name#"judge-${task}-"}"
      [[ "${parked_repeat}" =~ ^[1-9][0-9]*$ ]] || continue
      parked_judge_digest="${quality_dir_fd_path}/${parked_judge_name}.inputs.sha256"
      parked_judge_result="${quality_dir_fd_path}/${parked_judge_name}.result.sha256"
      parked_judge_exit="${quality_dir_fd_path}/${parked_judge_name}.exit-code"
      parked_judge_log="${quality_dir_fd_path}/${parked_judge_name}.jsonl"
      parked_legacy_attestation="${quality_dir_fd_path}/${parked_judge_name}.legacy-attestation.json"
      expected_judge_digest="$(
        judge_input_digest \
          "${parked_judge_name}" \
          "${prompt_semantics}" \
          "${target_commit}" \
          "${task}" \
          "baseline-${task}" \
          "${candidate_names_json}" \
          "${rubric}" \
          "${output_schema}" \
          "${baseline_answer}" \
          "${baseline_transcript}" \
          "${candidate_input_files[@]}"
      )"
      if "${bind_legacy_judges}"; then
        if judge_cache_valid \
          "${parked_judge_name}" \
          "${parked_judge_output}" \
          "${parked_judge_log}" \
          "${parked_judge_digest}" \
          "${parked_judge_result}" \
          "${parked_judge_exit}" \
          "${expected_judge_digest}" \
          "${task}" \
          "baseline-${task}" \
          "${candidate_names_json}"; then
          snapshot_judge_run "${parked_judge_name}" "current"
          continue
        fi
        if [[ -e "${parked_judge_digest}" ||
          -e "${parked_judge_result}" ]]; then
          printf 'legacy judge has incompatible current-cache sidecars; leaving unchanged: %s\n' \
            "${parked_judge_name}" >&2
          continue
        fi
        [[ -s "${parked_judge_exit}" ]] || continue
        [[ "$(<"${parked_judge_exit}")" == "0" ]] || continue
        judge_log_valid "${parked_judge_log}" || continue
        judge_output_matches_log \
          "${parked_judge_output}" \
          "${parked_judge_log}" || continue
        judge_output_valid \
          "${parked_judge_output}" \
          "${task}" \
          "baseline-${task}" \
          "${candidate_names_json}" || continue
        expected_legacy_digest="$(
          legacy_judge_input_digest \
            "${parked_judge_name}" \
            "${prompt_semantics}" \
            "${target_commit}" \
            "${task}" \
            "baseline-${task}" \
            "${candidate_names_json}" \
            "${rubric}" \
            "${output_schema}" \
            "${baseline_answer}" \
            "${baseline_transcript}" \
            "${candidate_input_files[@]}"
        )"
        binding_eligible_count=$((binding_eligible_count + 1))
        if [[ -e "${parked_legacy_attestation}" ]]; then
          if ! legacy_attestation_valid \
            "${parked_legacy_attestation}" \
            "${parked_judge_name}" \
            "${expected_legacy_digest}" \
            "${parked_judge_output}" \
            "${parked_judge_log}" \
            "${parked_judge_exit}"; then
            printf 'refusing to overwrite mismatched legacy judge attestation: %s\n' \
              "${parked_legacy_attestation}" >&2
            exit 1
          fi
        else
          write_legacy_attestation \
            "${parked_legacy_attestation}" \
            "${parked_judge_name}" \
            "${expected_legacy_digest}" \
            "${parked_judge_output}" \
            "${parked_judge_log}" \
            "${parked_judge_exit}"
          printf 'wrote legacy judge attestation: %s\n' \
            "${parked_legacy_attestation}" >&2
        fi
        snapshot_judge_run \
          "${parked_judge_name}" \
          "legacy-unisolated-attested"
        continue
      elif ! judge_cache_valid \
        "${parked_judge_name}" \
        "${parked_judge_output}" \
        "${parked_judge_log}" \
        "${parked_judge_digest}" \
        "${parked_judge_result}" \
        "${parked_judge_exit}" \
        "${expected_judge_digest}" \
        "${task}" \
        "baseline-${task}" \
        "${candidate_names_json}"; then
        continue
      fi
      snapshot_judge_run "${parked_judge_name}" "current"
    done
    continue
  fi

  for ((repeat = 1; repeat <= judge_repeats; repeat++)); do
    verify_input_snapshot
    verify_judge_checkout
    stem="judge-${task}-${repeat}"
    judge_output="${quality_dir_fd_path}/${stem}.json"
    judge_log="${quality_dir_fd_path}/${stem}.jsonl"
    judge_digest_file="${quality_dir_fd_path}/${stem}.inputs.sha256"
    judge_result_file="${quality_dir_fd_path}/${stem}.result.sha256"
    judge_exit_file="${quality_dir_fd_path}/${stem}.exit-code"
    expected_judge_digest="$(
      judge_input_digest \
        "${stem}" \
        "${prompt_semantics}" \
        "${target_commit}" \
        "${task}" \
        "baseline-${task}" \
        "${candidate_names_json}" \
        "${rubric}" \
        "${output_schema}" \
        "${baseline_answer}" \
        "${baseline_transcript}" \
        "${candidate_input_files[@]}"
    )"
    if ! judge_cache_valid \
      "${stem}" \
      "${judge_output}" \
      "${judge_log}" \
      "${judge_digest_file}" \
      "${judge_result_file}" \
      "${judge_exit_file}" \
      "${expected_judge_digest}" \
      "${task}" \
      "baseline-${task}" \
      "${candidate_names_json}"; then
      retain_invalid_judge "${stem}"
      for ((judge_attempt = 1; judge_attempt <= judge_attempt_limit; judge_attempt++)); do
        status=0
        if env -i \
          PATH="${judge_tool_path}" \
          HOME="${judge_shell_home}" \
          TMPDIR="${judge_tmpdir}" \
          LANG=C \
          LC_ALL=C \
          TZ=UTC \
          CODEX_HOME="${judge_codex_home}" \
          "${codex_executable}" exec \
          -c "developer_instructions=\"${no_collaboration}\"" \
          "${judge_reasoning_args[@]}" \
          "${judge_model_args[@]}" \
          "${feature_flags[@]}" \
          "${codex_isolation_flags[@]}" \
          -C "${judge_source_root}" \
          --ephemeral \
          --json \
          --output-schema "${output_schema}" \
          -o "${judge_output}" \
          "${prompt}" \
          </dev/null \
          > "${judge_log}" \
          2> "${quality_dir_fd_path}/${stem}.stderr"; then
          printf '0\n' > "${quality_dir_fd_path}/${stem}.exit-code"
        else
          status=$?
          printf '%s\n' "${status}" > "${quality_dir_fd_path}/${stem}.exit-code"
        fi
        verify_input_snapshot
        verify_judge_checkout
        if [[ "${status}" -eq 0 ]] &&
          judge_output_valid \
          "${judge_output}" \
          "${task}" \
          "baseline-${task}" \
          "${candidate_names_json}" &&
          judge_log_valid "${judge_log}" &&
          judge_output_matches_log "${judge_output}" "${judge_log}"; then
          break
        fi
        retain_invalid_judge "${stem}"
      done
      if [[ ! -s "${judge_exit_file}" ||
        "$(<"${judge_exit_file}")" != "0" ]] ||
        ! judge_output_valid \
        "${judge_output}" \
        "${task}" \
        "baseline-${task}" \
        "${candidate_names_json}" ||
        ! judge_log_valid "${judge_log}" ||
        ! judge_output_matches_log "${judge_output}" "${judge_log}"; then
        printf 'judge output remained invalid after %s attempts: %s\n' \
          "${judge_attempt_limit}" "${stem}" >&2
        exit 1
      fi
      write_atomic_line "${judge_digest_file}" "${expected_judge_digest}"
      expected_judge_result="$(
        judge_result_digest \
          "${stem}" \
          "${expected_judge_digest}" \
          "${judge_output}" \
          "${judge_log}" \
          "${judge_exit_file}"
      )"
      write_atomic_line "${judge_result_file}" "${expected_judge_result}"
    fi

    snapshot_judge_run "${stem}" "current"
  done
done

if "${bind_legacy_judges}" && [[ "${binding_eligible_count}" -eq 0 ]]; then
  printf 'no eligible legacy judge artifacts found\n' >&2
  exit 1
fi

judges_output="${quality_scratch}/judges.json"
if [[ ${#judge_files[@]} -gt 0 ]]; then
  jq -s --arg provenance_status "${aggregate_status}" '
    {
      provenance_status: $provenance_status,
      judge_runs: .,
      baselines: (
        [
          .[] as $run
          | $run.baseline + {task: $run.task}
        ]
        | group_by([.task, .name])
        | map({
            task: .[0].task,
            name: .[0].name,
            judge_count: length,
            average_correctness: (map(.correctness) | add / length),
            average_completeness: (map(.completeness) | add / length),
            average_grounding: (map(.grounding) | add / length),
            average_task_adherence: (map(.task_adherence) | add / length),
            critical_omissions: (map(.critical_omissions[]) | unique),
            unsupported_claims: (map(.unsupported_claims[]) | unique)
          })
      ),
      candidates: (
        [
          .[] as $run
          | $run.candidates[]
          | . + {task: $run.task}
        ]
        | group_by([.task, .name])
        | map({
            task: .[0].task,
            name: .[0].name,
            judge_count: length,
            all_not_worse: all(.not_worse_than_baseline),
            average_correctness: (map(.correctness) | add / length),
            average_completeness: (map(.completeness) | add / length),
            average_grounding: (map(.grounding) | add / length),
            average_task_adherence: (map(.task_adherence) | add / length),
            critical_omissions: (map(.critical_omissions[]) | unique),
            unsupported_claims: (map(.unsupported_claims[]) | unique),
            all_core_conclusion_match: all(.core_conclusion_matches_baseline),
            material_contradictions: (map(.material_contradictions[]) | unique),
            baseline_material_points_omitted: (map(.baseline_material_points_omitted[]) | unique),
            candidate_material_additions: (map(.candidate_material_additions[]) | unique)
          })
      )
    }
  ' "${judge_files[@]}" > "${judges_output}"
else
  jq -n --arg provenance_status "${aggregate_status}" '{
      provenance_status: $provenance_status,
      judge_runs: [],
      baselines: [],
      candidates: []
    }' > "${judges_output}"
fi

if [[ ${#judge_logs[@]} -gt 0 ]]; then
  for log in "${judge_logs[@]}"; do
    if ! judge_log_valid "${log}"; then
      printf 'invalid selected judge transcript: %s\n' "${log}" >&2
      exit 1
    fi
    jq -s \
      --arg name "$(basename "${log}" .jsonl)" \
      '
        ([.[] | select(.type == "turn.completed")] | last | .usage // null) as $usage
        | select($usage != null)
        | {
            name: $name,
            input_tokens: $usage.input_tokens,
            regular_input_tokens: ($usage.input_tokens - $usage.cached_input_tokens),
            cached_input_tokens: $usage.cached_input_tokens,
            cached_input_equivalent_tokens: ($usage.cached_input_tokens * 0.1),
            output_tokens: $usage.output_tokens,
            reasoning_output_tokens: ($usage.reasoning_output_tokens // 0),
            raw_total_tokens: ($usage.input_tokens + $usage.output_tokens),
            effective_tokens: (
              ($usage.input_tokens - $usage.cached_input_tokens)
              + ($usage.cached_input_tokens * 0.1)
              + $usage.output_tokens
            )
          }
      ' "${log}" >> "${judge_usage_cases}"
  done
fi

judge_usage_output="${quality_scratch}/judge-usage.json"
jq -s '
  . as $runs
  | {
      formula: "effective = (input - cached_input) + 0.1 * cached_input + output",
      runs: $runs,
      totals: {
        run_count: ($runs | length),
        input_tokens: ([$runs[].input_tokens] | add // 0),
        regular_input_tokens: ([$runs[].regular_input_tokens] | add // 0),
        cached_input_tokens: ([$runs[].cached_input_tokens] | add // 0),
        cached_input_equivalent_tokens: ([$runs[].cached_input_equivalent_tokens] | add // 0),
        output_tokens: ([$runs[].output_tokens] | add // 0),
        reasoning_output_tokens: ([$runs[].reasoning_output_tokens] | add // 0),
        raw_total_tokens: ([$runs[].raw_total_tokens] | add // 0),
        effective_tokens: ([$runs[].effective_tokens] | add // 0)
      }
    }
' "${judge_usage_cases}" > "${judge_usage_output}"

quality_output="${quality_scratch}/quality.json"
jq -n \
  --slurpfile static "${static_output}" \
  --slurpfile judges "${judges_output}" \
  --slurpfile judge_usage "${judge_usage_output}" \
  --argjson required_judges "${judge_repeats}" \
  --arg judge_model "${judge_model}" \
  --arg judge_codex_version "${judge_codex_version}" \
  --arg provenance_status "${aggregate_status}" \
  --argjson judge_cache_schema "${judge_cache_schema}" \
  --slurpfile judge_environment "${judge_environment_metadata}" \
  '
    {
      schema_version: 4,
      provenance_status: $provenance_status,
      required_judge_count: $required_judges,
      evaluator: {
        model: $judge_model,
        codex_version: $judge_codex_version,
        cache_schema: $judge_cache_schema,
        environment: $judge_environment[0]
      },
      static: $static[0],
      judges: $judges[0],
      judge_usage: $judge_usage[0],
      verdicts: [
        $static[0].comparisons[] as $comparison
        | ([
            $judges[0].candidates[]
            | select(
                .task == $comparison.task
                and (
                  .name == ("optimized-" + $comparison.profile + "-" + $comparison.task)
                  or (
                    $comparison.profile == "default"
                    and .name == ("optimized-" + $comparison.task)
                  )
                )
              )
          ] | first) as $judge
        | {
            task: $comparison.task,
            profile: $comparison.profile,
            navigation_required: $comparison.navigation_required,
            navigation_pass: $comparison.navigation_pass,
            accounting_pass: $comparison.accounting_pass,
            navigation_calls: $comparison.navigation_calls,
            static_not_worse: $comparison.static_not_worse,
            judge_evaluated: ($judge != null),
            judge_count: (if $judge == null then 0 else $judge.judge_count end),
            required_judge_count: $required_judges,
            judge_complete: (
              $required_judges == 0
              or ($judge != null and $judge.judge_count >= $required_judges)
            ),
            judges_not_worse: (
              if $judge == null then null else $judge.all_not_worse end
            ),
            core_conclusion_match: (
              if $judge == null then null else $judge.all_core_conclusion_match end
            ),
            quality_pass: (
              $comparison.static_not_worse
              and (
                $required_judges == 0
                or ($judge != null and $judge.judge_count >= $required_judges)
              )
              and (if $judge == null then true else $judge.all_not_worse end)
            )
          }
      ]
    }
  ' > "${quality_output}"

summary_output="${quality_scratch}/summary.md"
{
  printf '# Quality Confirmation\n\n'
  printf 'Provenance status: `%s`\n\n' "${aggregate_status}"
  printf '| Task | Profile | Static score | Required facts | Navigation | Judge runs | Judge complete | Core match | Judge not worse | Pass |\n'
  printf '| --- | --- | ---: | --- | --- | ---: | --- | --- | --- | --- |\n'
  jq -r '
    . as $quality
    | (
        (
          $quality.static.cases[]
          | select(.variant == "baseline")
          | . as $case
          | ([
              $quality.judges.baselines[]
              | select(.task == $case.task)
            ] | first) as $judge
          | (($judge.judge_count // 0)) as $judge_count
          | (
              $quality.required_judge_count == 0
              or $judge_count >= $quality.required_judge_count
            ) as $judge_complete
          | {
              task: $case.task,
              profile: "baseline",
              score: $case.score_percent,
              required: $case.required_pass,
              navigation: "n/a",
              judge_count: $judge_count,
              judge_complete: $judge_complete,
              core_match: "reference",
              not_worse: "reference",
              pass: ($case.required_pass and $judge_complete)
            }
        ),
        (
          $quality.verdicts[] as $verdict
          | (
              $quality.static.comparisons[]
              | select(.task == $verdict.task and .profile == $verdict.profile)
            ) as $comparison
          | {
              task: $verdict.task,
              profile: $verdict.profile,
              score: $comparison.candidate_score_percent,
              required: $comparison.required_pass,
              navigation: (
                if $comparison.navigation_required
                then (
                  ($comparison.navigation_pass | tostring)
                  + " ("
                  + ($comparison.navigation_calls.total | tostring)
                  + "/"
                  + (
                      if $comparison.navigation_calls.command_cap > 0
                      then ($comparison.navigation_calls.command_cap | tostring)
                      else "unbounded"
                      end
                    )
                  + (
                      if $comparison.navigation_calls.budget_tamper > 0
                      then (
                        "; tamper="
                        + ($comparison.navigation_calls.budget_tamper | tostring)
                      )
                      else ""
                      end
                    )
                  + ")"
                )
                else "n/a"
                end
              ),
              judge_count: $verdict.judge_count,
              judge_complete: $verdict.judge_complete,
              core_match: (
                if $verdict.core_conclusion_match == null
                then "n/a"
                else $verdict.core_conclusion_match
                end
              ),
              not_worse: (
                if $verdict.judges_not_worse == null
                then "n/a"
                else $verdict.judges_not_worse
                end
              ),
              pass: $verdict.quality_pass
            }
        )
      )
    | [
        .task,
        .profile,
        (.score | tostring),
        (.required | tostring),
        (.navigation | tostring),
        (.judge_count | tostring),
        (.judge_complete | tostring),
        (.core_match | tostring),
        (.not_worse | tostring),
        (.pass | tostring)
      ]
    | @tsv
  ' "${quality_output}" |
    while IFS=$'\t' read -r task profile score required navigation judge_count judge_complete core_match not_worse pass; do
      printf '| %s | %s | %s%% | %s | %s | %s | %s | %s | %s | %s |\n' \
        "${task}" "${profile}" "${score}" "${required}" "${navigation}" "${judge_count}" \
        "${judge_complete}" "${core_match}" "${not_worse}" "${pass}"
    done
  if jq -e '(.judges.baselines + .judges.candidates) | length > 0' "${quality_output}" >/dev/null; then
    printf '\n## Judge Signals\n\n'
    printf '| Task | Profile | Judges | Correctness | Completeness | Grounding | Adherence | Critical omissions | Unsupported claims | Contradictions | Baseline points omitted | Candidate additions |\n'
    printf '| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |\n'
    jq -r '
      . as $quality
      | [
          (
            $quality.judges.baselines[]
            | . + {profile: "baseline"}
          ),
          (
            $quality.verdicts[] as $verdict
            | (
                if $verdict.profile == "default"
                then "optimized-" + $verdict.task
                else "optimized-" + $verdict.profile + "-" + $verdict.task
                end
              ) as $name
            | $quality.judges.candidates[]
            | select(.task == $verdict.task and .name == $name)
            | . + {profile: $verdict.profile}
          )
        ][]
      | [
          .task,
          .profile,
          .judge_count,
          (.average_correctness | tostring),
          (.average_completeness | tostring),
          (.average_grounding | tostring),
          (.average_task_adherence | tostring),
          (.critical_omissions | length),
          (.unsupported_claims | length),
          ((.material_contradictions // []) | length),
          ((.baseline_material_points_omitted // []) | length),
          ((.candidate_material_additions // []) | length)
        ]
      | @tsv
    ' "${quality_output}" |
      while IFS=$'\t' read -r task profile judges correctness completeness grounding adherence omissions unsupported contradictions baseline_omissions additions; do
        printf '| %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s |\n' \
          "${task}" "${profile}" "${judges}" "${correctness}" "${completeness}" \
          "${grounding}" "${adherence}" "${omissions}" "${unsupported}" \
          "${contradictions}" "${baseline_omissions}" "${additions}"
      done
  fi
  if jq -e '.judge_usage.totals.run_count > 0' "${quality_output}" >/dev/null; then
    printf '\n## Judge Token Usage\n\n'
    printf '| Runs | Input | Regular input | Cached input | Cached @0.1 | Output | Reasoning | Raw total | Effective |\n'
    printf '| ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |\n'
    jq -r '
      .judge_usage.totals
      | [
          .run_count,
          .input_tokens,
          .regular_input_tokens,
          .cached_input_tokens,
          (.cached_input_equivalent_tokens | tostring),
          .output_tokens,
          .reasoning_output_tokens,
          .raw_total_tokens,
          (.effective_tokens | tostring)
        ]
      | @tsv
    ' "${quality_output}" |
      while IFS=$'\t' read -r runs input regular cached cached_cost output reasoning raw_total effective; do
        printf '| %s | %s | %s | %s | %s | %s | %s | %s | %s |\n' \
          "${runs}" "${input}" "${regular}" "${cached}" "${cached_cost}" \
          "${output}" "${reasoning}" "${raw_total}" "${effective}"
      done
  fi
} > "${summary_output}"

enforcement_pass=true
if "${enforce}" &&
  ! jq -e \
  --slurpfile metrics "${metrics}" \
  '
    . as $quality
    | [
        $quality.static.cases[]
        | select(.variant == "optimized")
      ] as $candidates
    | (
        all(
          $candidates[];
          . as $candidate
          | (
              [
                $quality.static.cases[]
                | select(
                    .variant == "baseline"
                    and .task == $candidate.task
                  )
              ]
              | length
            ) == 1
        )
        and all(
          $candidates[];
          . as $candidate
          | (
              [
                $metrics[0].comparisons[]
                | select(
                    .task == $candidate.task
                    and .profile == $candidate.profile
                  )
              ] as $matching
              | ($matching | length) == 1
              and (
                $matching[0].effective_reduction_percent
                | type == "number" and . > 0
              )
            )
        )
        and ($quality.verdicts | length) == ($candidates | length)
        and all($quality.verdicts[]; .quality_pass)
      )
  ' "${quality_output}" >/dev/null; then
  enforcement_pass=false
fi

if ! verify_input_snapshot; then
  exit 1
fi
if ! verify_judge_checkout; then
  exit 1
fi
packet_inventory_after="$(
  find "${run_dir_fd_path}/." -maxdepth 1 -type f \
    -name 'changed-packet*.json' -printf '%f\n' |
    sort
)"
raw_log_names_after="$(
  find "${run_dir_fd_path}/." -maxdepth 1 -type f \
    \( -name 'baseline-*.jsonl' -o -name 'optimized-*.jsonl' \) \
    -printf '%f\n' |
    sed 's/\.jsonl$//' |
    sort
)"
raw_exit_names_after="$(
  find "${run_dir_fd_path}/." -maxdepth 1 -type f \
    \( -name 'baseline-*.exit-code' -o -name 'optimized-*.exit-code' \) \
    -printf '%f\n' |
    sed 's/\.exit-code$//' |
    sort
)"
answer_names_after="$(
  find "${run_dir_fd_path}/answers" -maxdepth 1 -type f -name '*.md' \
    -printf '%f\n' 2>/dev/null |
    sed 's/\.md$//' |
    sort
)"
if [[ "${packet_inventory_after}" != "${packet_inventory_before}" ||
  "${raw_log_names_after}" != "${raw_log_names}" ||
  "${raw_exit_names_after}" != "${raw_exit_names}" ||
  "${answer_names_after}" != "${answer_names}" ]]; then
  printf 'evaluator input inventory changed during quality check\n' >&2
  exit 1
fi

bundle_input_entries="${quality_scratch}/bundle-input-entries.jsonl"
snapshot_entries="${quality_scratch}/snapshot-entries.jsonl"
: > "${bundle_input_entries}"
: > "${snapshot_entries}"

record_bundle_input() {
  local snapshot_relative="$1"
  local bundle_relative="$2"
  jq -cn \
    --arg path "${bundle_relative}" \
    --arg digest "$(file_digest "${input_snapshot}/${snapshot_relative}")" \
    '{key: $path, value: $digest}' >> "${bundle_input_entries}"
}

record_bundle_input "metrics.json" "metrics.json"
if [[ -f "${input_snapshot}/manifest.json" ]]; then
  record_bundle_input "manifest.json" "manifest.json"
fi
if [[ -f "${input_snapshot}/generation-config.json" ]]; then
  record_bundle_input "generation-config.json" "generation-config.json"
fi
if [[ -f "${input_snapshot}/run-complete.json" ]]; then
  record_bundle_input "run-complete.json" "run-complete.json"
fi
if [[ -f "${input_snapshot}/profiles-snapshot.tsv" ]]; then
  record_bundle_input "profiles-snapshot.tsv" "profiles-snapshot.tsv"
fi
for selected_task in "${selected_tasks[@]}"; do
  rendered_prompt_relative="$(
    jq -r \
      --arg task "${selected_task}" \
      '.prompt_files[$task] // empty' \
      "${manifest}"
  )"
  if [[ -n "${rendered_prompt_relative}" &&
    -f "${input_snapshot}/${rendered_prompt_relative}" ]]; then
    record_bundle_input \
      "${rendered_prompt_relative}" \
      "${rendered_prompt_relative}"
  fi
done
if [[ -f "${input_snapshot}/baseline-source-manifest.json" ]]; then
  record_bundle_input \
    "baseline-source-manifest.json" \
    "baseline-source-manifest.json"
fi
if [[ -f "${input_snapshot}/baseline-source-generation-config.json" ]]; then
  record_bundle_input \
    "baseline-source-generation-config.json" \
    "baseline-source-generation-config.json"
fi
if [[ -f "${input_snapshot}/baseline-source-profiles-snapshot.tsv" ]]; then
  record_bundle_input \
    "baseline-source-profiles-snapshot.tsv" \
    "baseline-source-profiles-snapshot.tsv"
fi
for selected_task in "${selected_tasks[@]}"; do
  if [[ -f \
    "${input_snapshot}/baseline-source-prompts/${selected_task}.txt" ]]; then
    record_bundle_input \
      "baseline-source-prompts/${selected_task}.txt" \
      "baseline-source-prompts/${selected_task}.txt"
  fi
done
while IFS= read -r name; do
  record_bundle_input "${name}.jsonl" "${name}.jsonl"
  record_bundle_input "${name}.exit-code" "${name}.exit-code"
done < <(jq -r '.cases[].name' "${metrics}")
while IFS= read -r artifact_relative; do
  record_bundle_input "${artifact_relative}" "${artifact_relative}"
done < <(
  jq -r '
    .cases[]
    | .answer_file,
      .commands_file,
      .tool_stats_file,
      .call_graph_dot_file,
      .call_graph_markdown_file
  ' "${metrics}" |
    sort -u
)
while IFS= read -r packet_name; do
  [[ -n "${packet_name}" ]] || continue
  record_bundle_input "packets/${packet_name}" "${packet_name}"
done <<< "${packet_inventory_before}"
while IFS= read -r judge_snapshot; do
  [[ -n "${judge_snapshot}" ]] || continue
  judge_relative="${judge_snapshot#${input_snapshot}/judges/}"
  record_bundle_input \
    "judges/${judge_relative}" \
    "quality/${judge_relative}"
done < <(
  find "${input_snapshot}/judges" -maxdepth 1 -type f -print 2>/dev/null |
    LC_ALL=C sort
)
for ((index = 0; index < ${#evaluator_snapshot_paths[@]}; index++)); do
  record_bundle_input \
    "${evaluator_snapshot_paths[index]}" \
    "${evaluator_bundle_paths[index]}"
done

while IFS= read -r -d '' snapshot_file; do
  snapshot_relative="${snapshot_file#${input_snapshot}/}"
  jq -cn \
    --arg path "${snapshot_relative}" \
    --arg digest "$(file_digest "${snapshot_file}")" \
    '{key: $path, value: $digest}' >> "${snapshot_entries}"
done < <(
  find "${input_snapshot}" -type f -print0 |
    LC_ALL=C sort -z
)

judge_semantics_json="$(
  printf '%s\n' "${codex_isolation_semantics[@]}" |
    jq -Rsc 'split("\n") | map(select(length > 0))'
)"
inputs_output="${quality_scratch}/inputs.json"
jq -n \
  --slurpfile bundle_entries "${bundle_input_entries}" \
  --slurpfile snapshots "${snapshot_entries}" \
  --argjson strict_evidence "${strict_evidence}" \
  --arg aggregate_status "${aggregate_status}" \
  --argjson enforce "${enforce}" \
  --argjson bind_legacy_judges "${bind_legacy_judges}" \
  --argjson skip_analyze "${skip_analyze}" \
  --argjson judge_repeats "${judge_repeats}" \
  --argjson judge_cache_schema "${judge_cache_schema}" \
  --arg metrics_formula "${metrics_formula}" \
  --arg generation_isolation "${manifest_generation_isolation}" \
  --arg generation_config_sha256 "${manifest_generation_config_sha256}" \
  --arg analyzer_go_version "${analysis_go_version}" \
  --argjson judge_semantics "${judge_semantics_json}" \
  --arg quality_check_sha256 "$(
    file_digest "${input_snapshot}/generators/quality-check.sh"
  )" \
  --arg analyze_sha256 "$(
    file_digest "${input_snapshot}/generators/analyze.sh"
  )" \
  --arg profiles_sha256 "$(
    file_digest "${input_snapshot}/generators/profiles.tsv"
  )" \
  --arg stats_main_sha256 "$(
    file_digest \
      "${input_snapshot}/generators/cmd-repo-view-run-stats-main.go"
  )" \
  --arg runstats_sha256 "$(
    file_digest \
      "${input_snapshot}/generators/internal-runstats-runstats.go"
  )" \
  --arg go_mod_sha256 "$(
    file_digest "${input_snapshot}/generators/go.mod"
  )" \
  '
    def object_from_entries($entries):
      reduce $entries[] as $entry ({}; .[$entry.key] = $entry.value);
    {
      schema_version: 1,
      validation: {
        strict_evidence: $strict_evidence,
        aggregate_status: $aggregate_status,
        enforce: $enforce,
        bind_legacy_judges: $bind_legacy_judges,
        skip_analyze: $skip_analyze,
        judge_repeats: $judge_repeats,
        metrics_schema_version: 2,
        metrics_formula: $metrics_formula,
        generation_isolation: $generation_isolation,
        generation_config_sha256: $generation_config_sha256,
        judge_cache_schema: $judge_cache_schema
      },
      inputs: object_from_entries($bundle_entries),
      snapshots: object_from_entries($snapshots),
      generators: {
        "experiments/lsp-replacement/quality-check.sh": $quality_check_sha256,
        "experiments/lsp-replacement/analyze.sh": $analyze_sha256,
        "experiments/lsp-replacement/profiles.tsv": $profiles_sha256,
        "cmd/repo-view-run-stats/main.go": $stats_main_sha256,
        "internal/runstats/runstats.go": $runstats_sha256,
        "go.mod": $go_mod_sha256
      },
      analysis_environment: {
        go_version: $analyzer_go_version,
        GOENV: "off",
        GOWORK: "off",
        GOFLAGS: "-mod=readonly"
      },
      judge_environment_semantics: $judge_semantics
    }
  ' > "${inputs_output}"

aggregate_manifest="${quality_scratch}/aggregate-manifest.json"
jq -n \
  --arg static "$(file_digest "${static_output}")" \
  --arg judges "$(file_digest "${judges_output}")" \
  --arg judge_usage "$(file_digest "${judge_usage_output}")" \
  --arg quality "$(file_digest "${quality_output}")" \
  --arg summary "$(file_digest "${summary_output}")" \
  --arg inputs "$(file_digest "${inputs_output}")" \
  '{
    schema_version: 2,
    files: {
      "static.json": $static,
      "judges.json": $judges,
      "judge-usage.json": $judge_usage,
      "quality.json": $quality,
      "summary.md": $summary,
      "inputs.json": $inputs
    }
  }' > "${aggregate_manifest}"

# Publish through the already-open directory descriptor so replacing the public
# quality pathname cannot redirect a write. The checksum marker remains last;
# readers reject every interrupted or mixed generation.
publish_quality_file() {
  local source="$1"
  local name="$2"
  local public_path="${quality_dir}/${name}"
  local descriptor_path="${quality_dir_fd_path}/${name}"

  if ! verify_quality_directory; then
    printf 'quality output directory changed during quality check: %s\n' \
      "${quality_dir}" >&2
    return 1
  fi
  if [[ -e "${public_path}" || -L "${public_path}" ]] &&
    { [[ -L "${public_path}" ]] || [[ ! -f "${public_path}" ]]; }; then
    printf 'quality output is not a replaceable regular file: %s\n' \
      "${public_path}" >&2
    return 1
  fi
  if ! mv -T -- "${source}" "${descriptor_path}"; then
    printf 'failed to publish quality output: %s\n' "${public_path}" >&2
    return 1
  fi
  if ! verify_quality_directory ||
    [[ -L "${public_path}" ]] ||
    [[ ! -f "${public_path}" ]]; then
    printf 'quality output directory changed during publication: %s\n' \
      "${quality_dir}" >&2
    return 1
  fi
}

for ((index = 0; index < ${#evaluator_snapshot_paths[@]}; index++)); do
  publish_quality_file \
    "${input_snapshot}/${evaluator_snapshot_paths[index]}" \
    "${evaluator_bundle_paths[index]#quality/}"
done
publish_quality_file "${static_output}" "static.json"
publish_quality_file "${judges_output}" "judges.json"
publish_quality_file "${judge_usage_output}" "judge-usage.json"
publish_quality_file "${quality_output}" "quality.json"
publish_quality_file "${summary_output}" "summary.md"
publish_quality_file "${inputs_output}" "inputs.json"
publish_quality_file "${aggregate_manifest}" "aggregate-manifest.json"

if ! verify_quality_directory; then
  printf 'quality output directory changed after publication: %s\n' \
    "${quality_dir}" >&2
  exit 1
fi
cat "${quality_dir_fd_path}/summary.md"

if "${enforce}" && ! "${enforcement_pass}"; then
  exit 1
fi
