#!/usr/bin/env bash
set -euo pipefail
export LC_ALL=C

experiment_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "${experiment_dir}/../.." && pwd)"
source "${experiment_dir}/config.env"

task="all"
variant="all"
order="baseline-first"
profiles="guarded-high"
profiles_explicit=false
prompt_commit_explicit=false
baseline_from=""
dry_run=false
run_id="$(date -u +%Y%m%dT%H%M%SZ)"
source_repo="${LSP_SOURCE_REPO}"
target_commit="${LSP_TARGET_COMMIT}"
base_ref="${LSP_BASE_REF}"
worktree="${repo_root}/.cache/experiments/lsp-replacement/target"
evidence_root="${experiment_dir}/evidence/runs"
prompt_commit="${LSP_PROMPT_COMMIT}"
generation_model="${LSP_MODEL:-}"
generation_model_mode="${LSP_MODEL_MODE}"
expected_codex_version="${LSP_CODEX_VERSION}"
expected_go_version="${LSP_GO_VERSION}"
generation_isolation="root-deny-explicit-read-inherit-none-go-env-v3"

cd "${repo_root}"

usage() {
  cat <<'EOF'
Usage: experiments/lsp-replacement/run.sh [options]

Requires Bash 4+ on Linux, including realpath with -m support.

Options:
  --task explain|review|all|deep-explain|deep-review|deep
  --variant baseline|optimized|all
  --profile NAME[,NAME...]|all
  --baseline-from RUN_DIR
  --order baseline-first|optimized-first
  --run-id ID
  --source PATH_OR_URL
  --commit FULL_SHA
  --prompt-commit ID
  --model MODEL
  --model-mode pinned|router
  --codex-version VERSION
  --go-version VERSION
  --base REF
  --worktree DIR
  --evidence-root DIR
  --dry-run
  -h, --help
EOF
}

require_option_value() {
  local option="$1"
  if [[ $# -lt 2 || -z "${2-}" || "${2-}" == -* ]]; then
    printf 'missing value for %s\n' "${option}" >&2
    usage >&2
    exit 2
  fi
}

is_safe_run_id() {
  local LC_ALL=C
  [[ "$1" =~ ^[A-Za-z0-9][A-Za-z0-9._-]{0,127}$ ]]
}

is_safe_profile_name() {
  local LC_ALL=C
  [[ "$1" =~ ^[a-z0-9][a-z0-9-]*$ ]]
}

is_full_git_object_id() {
  local LC_ALL=C
  [[ "$1" =~ ^[0-9a-f]{40}$ || "$1" =~ ^[0-9a-f]{64}$ ]]
}

is_target_relative_base_ref() {
  local LC_ALL=C
  local suffix
  if [[ "$1" != HEAD* ]]; then
    return 1
  fi
  suffix="${1#HEAD}"
  [[ "${suffix}" =~ ^([\^~][0-9]*)*$ ]]
}

while [[ $# -gt 0 ]]; do
  case "$1" in
    --task)
      require_option_value "$@"
      task="$2"
      shift 2
      ;;
    --variant)
      require_option_value "$@"
      variant="$2"
      shift 2
      ;;
    --profile)
      require_option_value "$@"
      profiles="$2"
      profiles_explicit=true
      shift 2
      ;;
    --baseline-from)
      require_option_value "$@"
      baseline_from="$2"
      shift 2
      ;;
    --order)
      require_option_value "$@"
      order="$2"
      shift 2
      ;;
    --run-id)
      require_option_value "$@"
      run_id="$2"
      shift 2
      ;;
    --source)
      require_option_value "$@"
      source_repo="$2"
      shift 2
      ;;
    --commit)
      require_option_value "$@"
      target_commit="$2"
      shift 2
      ;;
    --prompt-commit)
      require_option_value "$@"
      prompt_commit="$2"
      prompt_commit_explicit=true
      shift 2
      ;;
    --model)
      require_option_value "$@"
      generation_model="$2"
      shift 2
      ;;
    --model-mode)
      require_option_value "$@"
      generation_model_mode="$2"
      shift 2
      ;;
    --codex-version)
      require_option_value "$@"
      expected_codex_version="$2"
      shift 2
      ;;
    --go-version)
      require_option_value "$@"
      expected_go_version="$2"
      shift 2
      ;;
    --base)
      require_option_value "$@"
      base_ref="$2"
      shift 2
      ;;
    --worktree)
      require_option_value "$@"
      worktree="$2"
      shift 2
      ;;
    --evidence-root)
      require_option_value "$@"
      evidence_root="$2"
      shift 2
      ;;
    --dry-run)
      dry_run=true
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

if ! is_safe_run_id "${run_id}"; then
  printf 'invalid --run-id: %s (use 1-128 ASCII letters, digits, dots, underscores, or hyphens; start with a letter or digit)\n' \
    "${run_id}" >&2
  exit 2
fi
if ! is_full_git_object_id "${target_commit}"; then
  printf 'invalid --commit: %s (use a full lowercase 40- or 64-hex Git commit ID)\n' \
    "${target_commit}" >&2
  exit 2
fi
if [[ ! "${prompt_commit}" =~ ^[0-9a-f]{7,64}$ ||
      "${target_commit}" != "${prompt_commit}"* ]]; then
  if "${prompt_commit_explicit}"; then
    printf 'invalid --prompt-commit: %s (use a lowercase hex prefix of --commit)\n' \
      "${prompt_commit}" >&2
    exit 2
  fi
  prompt_commit="${target_commit:0:9}"
fi

case "${task}" in
  explain|review|all|deep-explain|deep-review|deep) ;;
  *)
    printf 'invalid --task: %s\n' "${task}" >&2
    exit 2
    ;;
esac
case "${generation_model_mode}" in
  pinned)
    generation_model="${generation_model:-gpt-5.6-sol}"
    generation_model_args=(-m "${generation_model}")
    ;;
  router)
    generation_model="router-selected"
    generation_model_args=()
    ;;
  *)
    printf 'invalid --model-mode: %s\n' "${generation_model_mode}" >&2
    exit 2
    ;;
esac
if [[ "${task}" == "deep" || "${task}" == deep-* ]]; then
  if ! "${profiles_explicit}"; then
    profiles="investigative-verified-high"
  fi
fi
case "${task}" in
  all)
    selected_tasks=(explain review)
    ;;
  deep)
    selected_tasks=(deep-explain deep-review)
    ;;
  *)
    selected_tasks=("${task}")
    ;;
esac
case "${variant}" in
  baseline|optimized|all) ;;
  *)
    printf 'invalid --variant: %s\n' "${variant}" >&2
    exit 2
    ;;
esac
if [[ "${variant}" == "optimized" && -z "${baseline_from}" ]]; then
  printf '%s\n' \
    '--variant optimized requires --baseline-from for a comparable run' >&2
  exit 2
fi
case "${order}" in
  baseline-first|optimized-first) ;;
  *)
    printf 'invalid --order: %s\n' "${order}" >&2
    exit 2
    ;;
esac

profile_file="${experiment_dir}/profiles.tsv"
if [[ ! -f "${profile_file}" ]]; then
  printf 'profile file does not exist: %s\n' "${profile_file}" >&2
  exit 1
fi
if [[ "${profiles}" == "all" ]]; then
  mapfile -t selected_profiles < <(awk -F $'\t' '!/^#/ && NF > 0 {print $1}' "${profile_file}")
else
  if [[ "${profiles}" == ,* || "${profiles}" == *, || "${profiles}" == *,,* ]]; then
    printf 'invalid --profile: empty profile name in %s\n' "${profiles}" >&2
    exit 2
  fi
  IFS=',' read -r -a selected_profiles <<< "${profiles}"
fi
if [[ ${#selected_profiles[@]} -eq 0 ]]; then
  printf 'no profiles selected\n' >&2
  exit 2
fi

load_profile() {
  local requested="$1"
  local line
  line="$(awk -F $'\t' -v requested="${requested}" '!/^#/ && $1 == requested {print; exit}' "${profile_file}")"
  if [[ -z "${line}" ]]; then
    printf 'unknown profile: %s\n' "${requested}" >&2
    exit 2
  fi
  IFS=$'\t' read -r profile_name profile_return profile_context profile_limit \
    profile_max_code profile_max_patch profile_reasoning profile_answer_guard \
    profile_navigation_policy profile_navigation_command_cap \
    profile_description <<< "${line}"
}

effective_profile_reasoning() {
  if [[ "${generation_model_mode}" == "router" ]]; then
    printf '%s' inherit
  else
    printf '%s' "${profile_reasoning}"
  fi
}

declare -A seen_profiles=()
for selected_profile in "${selected_profiles[@]}"; do
  if ! is_safe_profile_name "${selected_profile}"; then
    printf 'invalid profile name: %s\n' "${selected_profile}" >&2
    exit 2
  fi
  if [[ -n "${seen_profiles[${selected_profile}]+x}" ]]; then
    printf 'duplicate profile: %s\n' "${selected_profile}" >&2
    exit 2
  fi
  seen_profiles["${selected_profile}"]=1
  load_profile "${selected_profile}"
done

for required in awk cmp find git go codex gzip jq mktemp sort stat tar sha256sum realpath tee; do
  if ! command -v "${required}" >/dev/null 2>&1; then
    printf 'missing required command: %s\n' "${required}" >&2
    exit 1
  fi
done

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
    GIT_TERMINAL_PROMPT=0 \
    GIT_NO_REPLACE_OBJECTS=1 \
    GIT_OPTIONAL_LOCKS=0 \
    GIT_DISCOVERY_ACROSS_FILESYSTEM=0 \
    git \
      -c core.hooksPath=/dev/null \
      -c core.attributesFile=/dev/null \
      -c core.excludesFile=/dev/null \
      -c core.autocrlf=false \
      -c core.eol=lf \
      -c core.safecrlf=false \
      -c core.filemode=true \
      -c core.fsmonitor=false \
      -c core.untrackedCache=false \
      -c core.sparseCheckout=false \
      "$@"
}

run_dir="${evidence_root}/${run_id}"
if "${dry_run}" && { [[ -e "${run_dir}" ]] || [[ -L "${run_dir}" ]]; }; then
  printf 'run already exists: %s\n' "${run_dir}" >&2
  exit 1
fi
if [[ -n "${baseline_from}" ]]; then
  if [[ "${baseline_from}" != /* ]]; then
    if [[ ! -d "${baseline_from}" ]]; then
      printf 'baseline run does not exist: %s\n' "${baseline_from}" >&2
      exit 2
    fi
    baseline_from="$(cd "${baseline_from}" && pwd)"
  fi
  if [[ ! -d "${baseline_from}" ]]; then
    printf 'baseline run does not exist: %s\n' "${baseline_from}" >&2
    exit 2
  fi
fi

render_prompt() {
  local prompt_file="$1"
  local rendered_commit="$2"
  local rendered_base="$3"
  local prompt
  prompt="$(<"${prompt_file}")"
  prompt="${prompt//\{\{COMMIT_SHORT\}\}/${rendered_commit}}"
  prompt="${prompt//\{\{BASE\}\}/${rendered_base}}"
  printf '%s' "${prompt}"
}

declare -A rendered_prompts=()
declare -A rendered_prompt_digests=()
prompt_digests_json='{}'
prompt_files_json='{}'
prepare_rendered_prompts() {
  local selected_task
  local rendered
  local digest_output
  local digest
  local prompt_relative

  mkdir -m 700 -- "${run_dir}/prompts"

  for selected_task in "${selected_tasks[@]}"; do
    rendered="$(
      render_prompt \
        "${runtime_experiment_dir}/prompts/${selected_task}.txt" \
        "${prompt_commit}" "${resolved_base}"
    )"
    prompt_relative="prompts/${selected_task}.txt"
    printf '%s' "${rendered}" > "${run_dir}/${prompt_relative}"
    digest_output="$(sha256sum -- "${run_dir}/${prompt_relative}")"
    digest="${digest_output%% *}"
    rendered_prompts["${selected_task}"]="${rendered}"
    rendered_prompt_digests["${selected_task}"]="${digest}"
    prompt_digests_json="$(
      jq -cn \
        --argjson current "${prompt_digests_json}" \
        --arg task "${selected_task}" \
        --arg digest "${digest}" \
        '$current + {($task): $digest}'
    )"
    prompt_files_json="$(
      jq -cn \
        --argjson current "${prompt_files_json}" \
        --arg task "${selected_task}" \
        --arg path "${prompt_relative}" \
        '$current + {($task): $path}'
    )"
  done
}

verify_generation_input_snapshots() {
  local digest_output
  local digest
  local selected_task
  local prompt_path

  if [[ ! -f "${run_dir}/profiles-snapshot.tsv" ||
        -L "${run_dir}/profiles-snapshot.tsv" ]]; then
    printf 'generation profile snapshot is missing or unsafe\n' >&2
    return 1
  fi
  digest_output="$(sha256sum -- "${run_dir}/profiles-snapshot.tsv")"
  digest="${digest_output%% *}"
  if [[ "${digest}" != "${profiles_snapshot_sha256}" ]]; then
    printf 'generation profile snapshot changed during run\n' >&2
    return 1
  fi
  for selected_task in "${selected_tasks[@]}"; do
    prompt_path="${run_dir}/prompts/${selected_task}.txt"
    if [[ ! -f "${prompt_path}" || -L "${prompt_path}" ]]; then
      printf 'rendered prompt snapshot is missing or unsafe: %s\n' \
        "${selected_task}" >&2
      return 1
    fi
    digest_output="$(sha256sum -- "${prompt_path}")"
    digest="${digest_output%% *}"
    if [[ "${digest}" != \
      "${rendered_prompt_digests[${selected_task}]}" ]]; then
      printf 'rendered prompt snapshot changed during run: %s\n' \
        "${selected_task}" >&2
      return 1
    fi
  done
}

validate_baseline_manifest_field() {
  local manifest="$1"
  local field="$2"
  local expected="$3"
  local actual
  if ! actual="$(
    jq -er -s --arg field "${field}" \
      '.[0][$field] | select(type == "string")' \
      "${manifest}"
  )"; then
    printf 'baseline manifest is missing string field %s: %s\n' \
      "${field}" "${manifest}" >&2
    exit 1
  fi
  if [[ "${actual}" != "${expected}" ]]; then
    printf 'baseline manifest %s mismatch: %s != %s\n' \
      "${field}" "${actual}" "${expected}" >&2
    exit 1
  fi
}

validate_baseline_prompt_digest() {
  local manifest="$1"
  local imported_task="$2"
  local expected="${rendered_prompt_digests[${imported_task}]}"
  local actual

  if ! actual="$(
    jq -er -s --arg task "${imported_task}" \
      '.[0].prompt_digests[$task]
        | select(type == "string" and test("^[0-9a-f]{64}$"))' \
      "${manifest}"
  )"; then
    printf 'baseline manifest is missing prompt digest for %s: %s\n' \
      "${imported_task}" "${manifest}" >&2
    exit 1
  fi
  if [[ "${actual}" != "${expected}" ]]; then
    printf 'baseline manifest prompt digest mismatch for %s: %s != %s\n' \
      "${imported_task}" "${actual}" "${expected}" >&2
    exit 1
  fi
}

validate_baseline_prompt_snapshot() {
  local baseline_dir="$1"
  local imported_task="$2"
  local manifest="${baseline_dir}/manifest.json"
  local expected_relative="prompts/${imported_task}.txt"
  local actual_relative

  if ! actual_relative="$(
    jq -er -s --arg task "${imported_task}" \
      '.[0].prompt_files[$task] | select(type == "string")' \
      "${manifest}"
  )" ||
    [[ "${actual_relative}" != "${expected_relative}" ]]; then
    printf 'baseline manifest prompt file mismatch for %s: %s\n' \
      "${imported_task}" "${manifest}" >&2
    exit 1
  fi
  if [[ ! -f "${baseline_dir}/${expected_relative}" ||
        -L "${baseline_dir}/${expected_relative}" ]] ||
    ! cmp -s -- \
      "${baseline_dir}/${expected_relative}" \
      "${run_dir}/${expected_relative}"; then
    printf 'baseline rendered prompt bytes mismatch for %s: %s\n' \
      "${imported_task}" "${baseline_dir}/${expected_relative}" >&2
    exit 1
  fi
}

jsonl_lifecycle_matches_exit_code() {
  local jsonl="$1"
  local exit_code="$2"

  if [[ ! "${exit_code}" =~ ^(0|[1-9][0-9]{0,2})$ ]] ||
    ((10#${exit_code} > 255)); then
    return 1
  fi
  jq -e -s --argjson exit_code "${exit_code}" \
    '
      def nonnegative_integer:
        type == "number" and isfinite and floor == . and . >= 0;
      . as $events
      | (
          [range(0; $events | length) as $index
            | select($events[$index].type == "thread.started")
            | $index]
        ) as $thread_indexes
      | (
          [range(0; $events | length) as $index
            | select($events[$index].type == "turn.started")
            | $index]
        ) as $turn_indexes
      | length > 0
      and all($events[]; type == "object" and (.type | type == "string"))
      and ($thread_indexes | length) == 1
      and ($turn_indexes | length) == 1
      and $thread_indexes[0] < $turn_indexes[0]
      and (
        $events[$thread_indexes[0]].thread_id
        | type == "string" and length > 0
      )
      and (
        [
          range(0; $events | length) as $index
          | if (
              ($events[$index].type | startswith("item."))
              and $events[$index].item.type == "command_execution"
            ) then
              $index > $turn_indexes[0]
            else
              true
            end
        ]
        | all
      )
      and (
        [$events[] | select(
          .type == "turn.completed" or .type == "turn.failed"
        )]
        | length <= 1
      )
      and (
        [
          range(0; $events | length) as $index
          | select(
              $events[$index].type == "turn.completed"
              or $events[$index].type == "turn.failed"
            )
          | $index
        ] as $terminal_indexes
        | ($terminal_indexes | length) == 0
          or $terminal_indexes[0] == (($events | length) - 1)
      )
      and all(
        $events[];
        if .type == "turn.completed" then
          (.usage | type == "object")
          and (.usage.input_tokens | nonnegative_integer)
          and (.usage.cached_input_tokens | nonnegative_integer)
          and (.usage.output_tokens | nonnegative_integer)
          and (
            (.usage.reasoning_output_tokens // 0)
            | nonnegative_integer
          )
          and .usage.cached_input_tokens <= .usage.input_tokens
        else
          true
        end
      )
      and (
        (
          [$events[] | select(.type == "turn.completed")] | length
        ) as $completed_count
        | if $exit_code == 0 then
            $completed_count == 1
          else
            $completed_count == 0
          end
        )
    ' \
    "${jsonl}" >/dev/null
}

completed_jsonl_is_valid() {
  jsonl_lifecycle_matches_exit_code "$1" 0
}

validate_baseline_case() {
  local baseline_dir="$1"
  local imported_task="$2"
  local stem="baseline-${imported_task}"
  local jsonl="${baseline_dir}/${stem}.jsonl"
  local exit_code_file="${baseline_dir}/${stem}.exit-code"
  local imported_exit_code

  if [[ ! -f "${jsonl}" ]]; then
    printf 'baseline JSONL missing for %s: %s\n' "${imported_task}" "${jsonl}" >&2
    exit 1
  fi
  if ! completed_jsonl_is_valid "${jsonl}"; then
    printf 'baseline JSONL is invalid or incomplete for %s: %s\n' \
      "${imported_task}" "${jsonl}" >&2
    exit 1
  fi
  if [[ ! -f "${exit_code_file}" ]]; then
    printf 'baseline exit code missing for %s: %s\n' \
      "${imported_task}" "${exit_code_file}" >&2
    exit 1
  fi
  imported_exit_code="$(<"${exit_code_file}")"
  if [[ "${imported_exit_code}" != "0" ]]; then
    printf 'baseline exit code is not zero for %s: %s\n' \
      "${imported_task}" "${imported_exit_code}" >&2
    exit 1
  fi
}

validate_baseline_run() {
  local baseline_dir="$1"
  local manifest="${baseline_dir}/manifest.json"
  local generation_config="${baseline_dir}/generation-config.json"
  local generation_config_digest_output
  local generation_config_digest
  local imported_task

  if [[ ! -f "${manifest}" ]]; then
    printf 'baseline manifest missing: %s\n' "${manifest}" >&2
    exit 1
  fi
  if ! jq -e -s \
    'length == 1 and (.[0] | type == "object")' \
    "${manifest}" >/dev/null; then
    printf 'baseline manifest is invalid: %s\n' "${manifest}" >&2
    exit 1
  fi
  validate_baseline_manifest_field "${manifest}" target_commit "${resolved_target}"
  validate_baseline_manifest_field "${manifest}" prompt_commit "${prompt_commit}"
  validate_baseline_manifest_field "${manifest}" base_commit "${resolved_base}"
  validate_baseline_manifest_field "${manifest}" base_ref "${base_ref}"
  validate_baseline_manifest_field "${manifest}" model "${generation_model}"
  validate_baseline_manifest_field \
    "${manifest}" codex_version "${expected_codex_version}"
  validate_baseline_manifest_field \
    "${manifest}" generation_isolation "${generation_isolation}"
  validate_baseline_manifest_field \
    "${manifest}" go_version "${actual_go_version}"
  validate_baseline_manifest_field \
    "${manifest}" generation_config_sha256 "${generation_config_sha256}"
  validate_baseline_manifest_field \
    "${manifest}" profiles_snapshot_path "profiles-snapshot.tsv"
  validate_baseline_manifest_field \
    "${manifest}" profiles_snapshot_sha256 "${profiles_snapshot_sha256}"
  if [[ ! -f "${generation_config}" || -L "${generation_config}" ]]; then
    printf 'baseline generation config missing: %s\n' \
      "${generation_config}" >&2
    exit 1
  fi
  generation_config_digest_output="$(
    sha256sum -- "${generation_config}"
  )"
  generation_config_digest="${generation_config_digest_output%% *}"
  if [[ "${generation_config_digest}" != "${generation_config_sha256}" ]]; then
    printf 'baseline generation config digest mismatch: %s != %s\n' \
      "${generation_config_digest}" "${generation_config_sha256}" >&2
    exit 1
  fi
  if ! cmp -s -- \
    "${generation_config}" "${run_dir}/generation-config.json"; then
    printf 'baseline generation config bytes mismatch: %s\n' \
      "${generation_config}" >&2
    exit 1
  fi
  if [[ ! -f "${baseline_dir}/profiles-snapshot.tsv" ||
        -L "${baseline_dir}/profiles-snapshot.tsv" ]] ||
    ! cmp -s -- \
      "${baseline_dir}/profiles-snapshot.tsv" \
      "${run_dir}/profiles-snapshot.tsv"; then
    printf 'baseline profile snapshot bytes mismatch: %s\n' \
      "${baseline_dir}/profiles-snapshot.tsv" >&2
    exit 1
  fi

  for imported_task in "${selected_tasks[@]}"; do
    validate_baseline_prompt_digest "${manifest}" "${imported_task}"
    validate_baseline_prompt_snapshot "${baseline_dir}" "${imported_task}"
    validate_baseline_case "${baseline_dir}" "${imported_task}"
  done
}

baseline_snapshot_checksums() {
  local baseline_dir="$1"
  local imported_task
  local stem
  local suffix
  local -a files=(
    manifest.json
    generation-config.json
    profiles-snapshot.tsv
  )

  for imported_task in "${selected_tasks[@]}"; do
    files+=("prompts/${imported_task}.txt")
    stem="baseline-${imported_task}"
    files+=("${stem}.jsonl" "${stem}.exit-code")
    for suffix in stderr invocation started-at finished-at duration-seconds; do
      if [[ -f "${baseline_dir}/${stem}.${suffix}" ]]; then
        files+=("${stem}.${suffix}")
      fi
    done
  done
  (
    cd "${baseline_dir}"
    sha256sum -- "${files[@]}"
  )
}

snapshot_baseline_file() {
  local baseline_root_fd="$1"
  local relative_path="$2"
  local destination="$3"
  local source_path="/proc/self/fd/${baseline_root_fd}/${relative_path}"
  local source_fd
  local source_fd_path
  local baseline_root_canonical
  local source_canonical
  local source_identity
  local path_identity
  local source_stat_before
  local source_stat_after
  local source_digest_output
  local source_digest_before
  local source_digest_after
  local destination_digest_output
  local destination_digest
  local link_count

  if [[ -z "${relative_path}" || "${relative_path}" == */* ||
        "${relative_path}" == "." || "${relative_path}" == ".." ]]; then
    printf 'invalid baseline evidence path: %s\n' "${relative_path}" >&2
    return 1
  fi
  if [[ ! -e "${source_path}" && ! -L "${source_path}" ]]; then
    return 2
  fi
  if [[ -L "${source_path}" || ! -f "${source_path}" ]]; then
    printf 'baseline evidence is not a regular file: %s\n' \
      "${baseline_from}/${relative_path}" >&2
    return 1
  fi
  if ! exec {source_fd}<"${source_path}"; then
    printf 'failed to open baseline evidence snapshot: %s\n' \
      "${baseline_from}/${relative_path}" >&2
    return 1
  fi
  source_fd_path="/proc/self/fd/${source_fd}"
  if [[ ! -f "${source_fd_path}" ]] ||
    ! baseline_root_canonical="$(
      realpath -- "/proc/self/fd/${baseline_root_fd}"
    )" ||
    ! source_canonical="$(realpath -- "${source_fd_path}")" ||
    [[ "${source_canonical}" != \
      "${baseline_root_canonical}/${relative_path}" ]] ||
    ! source_identity="$(stat -Lc '%d:%i' -- "${source_fd_path}")" ||
    ! path_identity="$(stat -Lc '%d:%i' -- "${source_path}")" ||
    [[ "${source_identity}" != "${path_identity}" ]] ||
    ! link_count="$(stat -Lc '%h' -- "${source_fd_path}")" ||
    [[ "${link_count}" != "1" ]]; then
    printf 'baseline evidence changed while opening snapshot: %s\n' \
      "${baseline_from}/${relative_path}" >&2
    exec {source_fd}<&-
    return 1
  fi
  source_stat_before="$(
    stat -Lc '%d:%i:%h:%f:%s:%y:%z' -- "${source_fd_path}"
  )"
  source_digest_output="$(sha256sum -- "${source_fd_path}")"
  source_digest_before="${source_digest_output%% *}"
  if ! cp --preserve=mode,timestamps -- \
    "${source_fd_path}" "${destination}"; then
    printf 'failed to snapshot baseline evidence: %s\n' \
      "${baseline_from}/${relative_path}" >&2
    exec {source_fd}<&-
    return 1
  fi
  source_stat_after="$(
    stat -Lc '%d:%i:%h:%f:%s:%y:%z' -- "${source_fd_path}"
  )"
  source_digest_output="$(sha256sum -- "${source_fd_path}")"
  source_digest_after="${source_digest_output%% *}"
  destination_digest_output="$(sha256sum -- "${destination}")"
  destination_digest="${destination_digest_output%% *}"
  if [[ "${source_stat_before}" != "${source_stat_after}" ||
        "${source_digest_before}" != "${source_digest_after}" ||
        "${source_digest_before}" != "${destination_digest}" ]]; then
    printf 'baseline evidence changed while snapshotting: %s\n' \
      "${baseline_from}/${relative_path}" >&2
    exec {source_fd}<&-
    return 1
  fi
  exec {source_fd}<&-
}

prepare_baseline_import() {
  local stage_dir="${run_dir}/.baseline-import"
  local baseline_root_fd
  local baseline_root_canonical
  local baseline_prompts_fd
  local baseline_prompts_canonical
  local imported_task
  local stem
  local suffix
  local snapshot_status

  if [[ -L "${baseline_from}" || ! -d "${baseline_from}" ]] ||
    ! exec {baseline_root_fd}<"${baseline_from}" ||
    ! baseline_root_canonical="$(
      realpath -- "/proc/self/fd/${baseline_root_fd}"
    )" ||
    [[ "${baseline_root_canonical}" != "${baseline_from}" ]]; then
    printf 'baseline run changed before import: %s\n' "${baseline_from}" >&2
    exit 1
  fi

  if ! mkdir -m 700 -- "${stage_dir}"; then
    printf 'failed to create private baseline import stage: %s\n' \
      "${stage_dir}" >&2
    exit 1
  fi
  mkdir -m 700 -- "${stage_dir}/prompts"
  snapshot_status=0
  snapshot_baseline_file \
    "${baseline_root_fd}" manifest.json "${stage_dir}/manifest.json" ||
    snapshot_status=$?
  if [[ "${snapshot_status}" -eq 2 ]]; then
    printf 'baseline manifest missing: %s\n' \
      "${baseline_from}/manifest.json" >&2
    exit 1
  elif [[ "${snapshot_status}" -ne 0 ]]; then
    exit 1
  fi
  snapshot_status=0
  snapshot_baseline_file \
    "${baseline_root_fd}" generation-config.json \
    "${stage_dir}/generation-config.json" ||
    snapshot_status=$?
  if [[ "${snapshot_status}" -eq 2 ]]; then
    printf 'baseline generation config missing: %s\n' \
      "${baseline_from}/generation-config.json" >&2
    exit 1
  elif [[ "${snapshot_status}" -ne 0 ]]; then
    exit 1
  fi
  snapshot_status=0
  snapshot_baseline_file \
    "${baseline_root_fd}" profiles-snapshot.tsv \
    "${stage_dir}/profiles-snapshot.tsv" ||
    snapshot_status=$?
  if [[ "${snapshot_status}" -eq 2 ]]; then
    printf 'baseline profile snapshot missing: %s\n' \
      "${baseline_from}/profiles-snapshot.tsv" >&2
    exit 1
  elif [[ "${snapshot_status}" -ne 0 ]]; then
    exit 1
  fi
  if [[ -L "/proc/self/fd/${baseline_root_fd}/prompts" ||
        ! -d "/proc/self/fd/${baseline_root_fd}/prompts" ]] ||
    ! exec {baseline_prompts_fd}< \
      "/proc/self/fd/${baseline_root_fd}/prompts" ||
    ! baseline_prompts_canonical="$(
      realpath -- "/proc/self/fd/${baseline_prompts_fd}"
    )" ||
    [[ "${baseline_prompts_canonical}" != \
      "${baseline_root_canonical}/prompts" ]]; then
    printf 'baseline rendered prompt directory missing: %s\n' \
      "${baseline_from}/prompts" >&2
    exit 1
  fi
  for imported_task in "${selected_tasks[@]}"; do
    stem="baseline-${imported_task}"
    snapshot_status=0
    snapshot_baseline_file \
      "${baseline_prompts_fd}" "${imported_task}.txt" \
      "${stage_dir}/prompts/${imported_task}.txt" ||
      snapshot_status=$?
    if [[ "${snapshot_status}" -eq 2 ]]; then
      printf 'baseline rendered prompt missing for %s: %s\n' \
        "${imported_task}" \
        "${baseline_from}/prompts/${imported_task}.txt" >&2
      exit 1
    elif [[ "${snapshot_status}" -ne 0 ]]; then
      exit 1
    fi
    for suffix in jsonl exit-code; do
      snapshot_status=0
      snapshot_baseline_file \
        "${baseline_root_fd}" "${stem}.${suffix}" \
        "${stage_dir}/${stem}.${suffix}" ||
        snapshot_status=$?
      if [[ "${snapshot_status}" -eq 2 ]]; then
        if [[ "${suffix}" == "jsonl" ]]; then
          printf 'baseline JSONL missing for %s: %s\n' \
            "${imported_task}" "${baseline_from}/${stem}.${suffix}" >&2
        else
          printf 'baseline exit code missing for %s: %s\n' \
            "${imported_task}" "${baseline_from}/${stem}.${suffix}" >&2
        fi
        exit 1
      elif [[ "${snapshot_status}" -ne 0 ]]; then
        exit 1
      fi
    done
    for suffix in stderr invocation started-at finished-at duration-seconds; do
      snapshot_status=0
      snapshot_baseline_file \
        "${baseline_root_fd}" "${stem}.${suffix}" \
        "${stage_dir}/${stem}.${suffix}" ||
        snapshot_status=$?
      if [[ "${snapshot_status}" -ne 0 && "${snapshot_status}" -ne 2 ]]; then
        exit 1
      fi
    done
  done
  exec {baseline_prompts_fd}<&-
  exec {baseline_root_fd}<&-

  validate_baseline_run "${stage_dir}"

  mv -- "${stage_dir}/manifest.json" \
    "${run_dir}/baseline-source-manifest.json"
  mv -- "${stage_dir}/generation-config.json" \
    "${run_dir}/baseline-source-generation-config.json"
  mv -- "${stage_dir}/profiles-snapshot.tsv" \
    "${run_dir}/baseline-source-profiles-snapshot.tsv"
  mkdir -m 700 -- "${run_dir}/baseline-source-prompts"
  for imported_task in "${selected_tasks[@]}"; do
    mv -- "${stage_dir}/prompts/${imported_task}.txt" \
      "${run_dir}/baseline-source-prompts/${imported_task}.txt"
  done
  rmdir -- "${stage_dir}/prompts"
  for imported_task in "${selected_tasks[@]}"; do
    stem="baseline-${imported_task}"
    for suffix in jsonl stderr exit-code invocation started-at finished-at duration-seconds; do
      if [[ -f "${stage_dir}/${stem}.${suffix}" ]]; then
        mv -- "${stage_dir}/${stem}.${suffix}" \
          "${run_dir}/${stem}.${suffix}"
      fi
    done
  done
  rmdir -- "${stage_dir}"
}

path_is_within_repo_root() {
  local candidate="$1"
  [[ "${candidate}" == "${repo_root}" || "${candidate}" == "${repo_root}/"* ]]
}

require_generated_path_excluded() {
  local candidate="$1"
  local option_name="$2"
  local relative

  if ! path_is_within_repo_root "${candidate}"; then
    return 0
  fi
  if [[ "${candidate}" == "${repo_root}" ]]; then
    printf '%s must not be the repo-view source root: %s\n' \
      "${option_name}" "${candidate}" >&2
    exit 2
  fi
  relative="${candidate#${repo_root}/}"
  if ! isolated_git -C "${repo_root}" check-ignore -q -- "${relative}"; then
    printf '%s inside the repo-view source must be ignored by Git: %s\n' \
      "${option_name}" "${candidate}" >&2
    exit 2
  fi
}

list_repo_view_source_files() {
  local source_root="$1"
  (
    cd "${source_root}"
    isolated_git ls-files -co --exclude-standard -z -- |
      while IFS= read -r -d '' source_path; do
        if [[ -e "${source_path}" || -L "${source_path}" ]]; then
          printf '%s\0' "${source_path}"
        fi
      done
  )
}

reject_repo_view_source_non_regular_files() {
  local source_root="$1"
  local source_list="$2"
  local source_path
  local link_count

  while IFS= read -r -d '' source_path; do
    if [[ -L "${source_root}/${source_path}" ||
          ! -f "${source_root}/${source_path}" ]]; then
      printf 'repo-view source contains non-regular file: %s\n' \
        "${source_path}" >&2
      return 1
    fi
    if ! link_count="$(
      stat -Lc '%h' -- "${source_root}/${source_path}"
    )" || [[ "${link_count}" != "1" ]]; then
      printf 'repo-view source contains multiply-linked file: %s\n' \
        "${source_path}" >&2
      return 1
    fi
  done < "${source_list}"
}

capture_repo_view_source_state() {
  local destination="$1"
  local source_root="$2"
  if ! list_repo_view_source_files "${source_root}" > "${destination}.files"; then
    printf 'failed to capture repo-view source state\n' >&2
    return 1
  fi
  reject_repo_view_source_non_regular_files \
    "${source_root}" "${destination}.files"
  if ! isolated_git -C "${source_root}" rev-parse HEAD > "${destination}.head" ||
    ! isolated_git -C "${source_root}" status \
      --porcelain=v1 --untracked-files=all --ignore-submodules=none \
      > "${destination}.status" ||
    ! isolated_git -C "${source_root}" diff \
      --binary --no-ext-diff --no-textconv HEAD \
      > "${destination}.patch"; then
    printf 'failed to capture repo-view source state\n' >&2
    return 1
  fi
}

snapshot_repo_view_source_file() {
  local source_root_fd="$1"
  local source_root_canonical="$2"
  local source_path="$3"
  local snapshot_root="$4"
  local remaining="${source_path}"
  local current_fd="${source_root_fd}"
  local current_fd_owned=false
  local next_fd
  local component
  local logical_parent=""
  local entry_path
  local expected_canonical
  local actual_canonical
  local opened_identity
  local path_identity
  local source_fd
  local source_fd_path
  local link_count
  local source_stat_before
  local source_stat_after
  local source_digest_output
  local source_digest_before
  local source_digest_after
  local destination="${snapshot_root}/${source_path}"
  local destination_digest_output
  local destination_digest

  if [[ -z "${source_path}" || "${source_path}" == /* ||
        "${source_path}" == */ || "${source_path}" == *//* ||
        "${source_path}" == "." || "${source_path}" == ".." ||
        "${source_path}" == ./* || "${source_path}" == ../* ||
        "${source_path}" == */./* || "${source_path}" == */../* ]]; then
    printf 'repo-view source contains unsafe path: %s\n' \
      "${source_path}" >&2
    return 1
  fi

  while [[ "${remaining}" == */* ]]; do
    component="${remaining%%/*}"
    remaining="${remaining#*/}"
    if [[ -z "${logical_parent}" ]]; then
      logical_parent="${component}"
    else
      logical_parent="${logical_parent}/${component}"
    fi
    entry_path="/proc/self/fd/${current_fd}/${component}"
    if [[ -L "${entry_path}" || ! -d "${entry_path}" ]] ||
      ! exec {next_fd}<"${entry_path}"; then
      printf 'repo-view source path changed while snapshotting: %s\n' \
        "${source_path}" >&2
      return 1
    fi
    expected_canonical="${source_root_canonical}/${logical_parent}"
    if ! actual_canonical="$(
      realpath -- "/proc/self/fd/${next_fd}"
    )" ||
      [[ "${actual_canonical}" != "${expected_canonical}" ]] ||
      ! opened_identity="$(
        stat -Lc '%d:%i' -- "/proc/self/fd/${next_fd}"
      )" ||
      ! path_identity="$(stat -Lc '%d:%i' -- "${entry_path}")" ||
      [[ "${opened_identity}" != "${path_identity}" ]]; then
      printf 'repo-view source path changed while snapshotting: %s\n' \
        "${source_path}" >&2
      exec {next_fd}<&-
      return 1
    fi
    if "${current_fd_owned}"; then
      exec {current_fd}<&-
    fi
    current_fd="${next_fd}"
    current_fd_owned=true
  done

  entry_path="/proc/self/fd/${current_fd}/${remaining}"
  if [[ -L "${entry_path}" || ! -f "${entry_path}" ]] ||
    ! exec {source_fd}<"${entry_path}"; then
    printf 'repo-view source contains non-regular file: %s\n' \
      "${source_path}" >&2
    if "${current_fd_owned}"; then
      exec {current_fd}<&-
    fi
    return 1
  fi
  source_fd_path="/proc/self/fd/${source_fd}"
  expected_canonical="${source_root_canonical}/${source_path}"
  if [[ ! -f "${source_fd_path}" ]] ||
    ! actual_canonical="$(realpath -- "${source_fd_path}")" ||
    [[ "${actual_canonical}" != "${expected_canonical}" ]] ||
    ! opened_identity="$(stat -Lc '%d:%i' -- "${source_fd_path}")" ||
    ! path_identity="$(stat -Lc '%d:%i' -- "${entry_path}")" ||
    [[ "${opened_identity}" != "${path_identity}" ]] ||
    ! link_count="$(stat -Lc '%h' -- "${source_fd_path}")" ||
    [[ "${link_count}" != "1" ]]; then
    printf 'repo-view source path changed while snapshotting: %s\n' \
      "${source_path}" >&2
    exec {source_fd}<&-
    if "${current_fd_owned}"; then
      exec {current_fd}<&-
    fi
    return 1
  fi

  mkdir -p -- "$(dirname "${destination}")"
  source_stat_before="$(
    stat -Lc '%d:%i:%h:%f:%s:%y:%z' -- "${source_fd_path}"
  )"
  source_digest_output="$(sha256sum -- "${source_fd_path}")"
  source_digest_before="${source_digest_output%% *}"
  if ! cp --preserve=mode,timestamps -- \
    "${source_fd_path}" "${destination}"; then
    printf 'failed to copy repo-view source snapshot: %s\n' \
      "${source_path}" >&2
    exec {source_fd}<&-
    if "${current_fd_owned}"; then
      exec {current_fd}<&-
    fi
    return 1
  fi
  source_stat_after="$(
    stat -Lc '%d:%i:%h:%f:%s:%y:%z' -- "${source_fd_path}"
  )"
  source_digest_output="$(sha256sum -- "${source_fd_path}")"
  source_digest_after="${source_digest_output%% *}"
  destination_digest_output="$(sha256sum -- "${destination}")"
  destination_digest="${destination_digest_output%% *}"
  if [[ "${source_stat_before}" != "${source_stat_after}" ||
        "${source_digest_before}" != "${source_digest_after}" ||
        "${source_digest_before}" != "${destination_digest}" ]] ||
    ! path_identity="$(stat -Lc '%d:%i' -- "${entry_path}")" ||
    [[ "${opened_identity}" != "${path_identity}" ]]; then
    printf 'repo-view source changed while snapshotting: %s\n' \
      "${repo_root}" >&2
    exec {source_fd}<&-
    if "${current_fd_owned}"; then
      exec {current_fd}<&-
    fi
    return 1
  fi
  exec {source_fd}<&-
  if "${current_fd_owned}"; then
    exec {current_fd}<&-
  fi
}

snapshot_repo_view_source_files() {
  local source_root_fd="$1"
  local source_root_canonical="$2"
  local source_list="$3"
  local snapshot_root="$4"
  local source_path

  while IFS= read -r -d '' source_path; do
    snapshot_repo_view_source_file \
      "${source_root_fd}" "${source_root_canonical}" \
      "${source_path}" "${snapshot_root}"
  done < "${source_list}"
}

prepare_repo_view_source_snapshot() {
  local snapshot_dir="${run_dir}/.source-snapshot"
  local before="${snapshot_dir}/before"
  local staged="${snapshot_dir}/staged"
  local after="${snapshot_dir}/after"
  local source_root_fd
  local source_root_canonical
  local source_root_path
  local staged_non_regular
  local staged_hardlink

  mkdir -m 700 -- "${snapshot_dir}"
  exec {source_snapshot_fd}<"${snapshot_dir}"
  source_snapshot_fd_open=true
  source_snapshot_identity="$(
    stat -Lc '%d:%i' -- "/proc/self/fd/${source_snapshot_fd}"
  )"
  mkdir -m 700 -- "${snapshot_dir}/root"
  if [[ -L "${repo_root}" || ! -d "${repo_root}" ]] ||
    ! exec {source_root_fd}<"${repo_root}" ||
    ! source_root_canonical="$(
      realpath -- "/proc/self/fd/${source_root_fd}"
    )" ||
    [[ "${source_root_canonical}" != "$(realpath -- "${repo_root}")" ]]; then
    printf 'repo-view source root changed before snapshot: %s\n' \
      "${repo_root}" >&2
    exit 1
  fi
  source_root_path="/proc/self/fd/${source_root_fd}"
  capture_repo_view_source_state "${before}" "${source_root_path}"
  snapshot_repo_view_source_files \
    "${source_root_fd}" "${source_root_canonical}" \
    "${before}.files" "${snapshot_dir}/root"
  staged_non_regular="$(
    find -P "${snapshot_dir}/root" -mindepth 1 \
      ! -type d ! -type f -print -quit
  )"
  if [[ -n "${staged_non_regular}" ]]; then
    printf 'repo-view source snapshot contains non-regular file: %s\n' \
      "${staged_non_regular#${snapshot_dir}/root/}" >&2
    exit 1
  fi
  staged_hardlink="$(
    find -P "${snapshot_dir}/root" -type f -links +1 -print -quit
  )"
  if [[ -n "${staged_hardlink}" ]]; then
    printf 'repo-view source snapshot contains multiply-linked file: %s\n' \
      "${staged_hardlink#${snapshot_dir}/root/}" >&2
    exit 1
  fi
  (
    cd "${snapshot_dir}/root"
    find . -mindepth 1 -print0 | sort -z > "${staged}.entries"
  )
  (
    cd "${snapshot_dir}/root"
    tar --null --files-from="${before}.files" -cf "${staged}.tar"
  )
  capture_repo_view_source_state "${after}" "${source_root_path}"
  exec {source_root_fd}<&-

  if ! cmp -s "${before}.head" "${after}.head" ||
    ! cmp -s "${before}.status" "${after}.status" ||
    ! cmp -s "${before}.patch" "${after}.patch" ||
    ! cmp -s "${before}.files" "${after}.files"; then
    printf 'repo-view source changed while snapshotting: %s\n' \
      "${repo_root}" >&2
    exit 1
  fi

  cp -- "${before}.status" "${run_dir}/repo-view-status.txt"
  cp -- "${before}.patch" "${run_dir}/repo-view.patch"
  gzip -n -c "${staged}.tar" > "${run_dir}/repo-view-source.tar.gz"
  repo_view_head="$(<"${before}.head")"
  repo_view_dirty=false
  if [[ -s "${before}.status" ]]; then
    repo_view_dirty=true
  fi
  source_snapshot_dir="${snapshot_dir}"
  source_snapshot_files="${before}.files"
  source_snapshot_tar="${staged}.tar"
  source_snapshot_entries="${staged}.entries"
  runner_source_root="${snapshot_dir}/root"
  runtime_experiment_dir="${runner_source_root}/experiments/lsp-replacement"
}

verify_repo_view_source_snapshot() {
  local verification_tar="${source_snapshot_dir}/verified.tar"
  local verification_entries="${source_snapshot_dir}/verified.entries"
  (
    cd "${runner_source_root}"
    find . -mindepth 1 -print0 | sort -z > "${verification_entries}"
    tar --null --files-from="${source_snapshot_files}" \
      -cf "${verification_tar}"
  )
  if ! cmp -s "${source_snapshot_entries}" "${verification_entries}" ||
    ! cmp -s "${source_snapshot_tar}" "${verification_tar}"; then
    printf 'staged repo-view source changed after snapshot: %s\n' \
      "${runner_source_root}" >&2
    exit 1
  fi
}

resolve_base_commit() {
  local remote_output
  local remote_oid=""
  local remote_ref=""
  local candidate_oid
  local candidate_ref
  local pattern
  local fetched_oid
  local worktree_oid
  local resolved
  local match_count=0
  local -a patterns=()

  if is_full_git_object_id "${base_ref}"; then
    if ! isolated_git -C "${source_verifier}" fetch --no-tags \
      "${source_repo}" "${base_ref}"; then
      printf 'failed to fetch base commit %s from %s\n' \
        "${base_ref}" "${source_repo}" >&2
      return 1
    fi
    fetched_oid="$(
      isolated_git -C "${source_verifier}" rev-parse --verify FETCH_HEAD
    )"
    if [[ "${fetched_oid}" != "${base_ref}" ]] ||
      [[ "$(
        isolated_git -C "${source_verifier}" cat-file -t FETCH_HEAD
      )" != "commit" ]]; then
      printf 'fetched base is not the requested commit: %s\n' \
        "${base_ref}" >&2
      return 1
    fi
    isolated_git -C "${worktree}" fetch --no-tags \
      "${source_verifier}" "${fetched_oid}"
    worktree_oid="$(
      isolated_git -C "${worktree}" rev-parse --verify FETCH_HEAD
    )"
    if [[ "${worktree_oid}" != "${fetched_oid}" ]]; then
      printf 'worktree base mismatch: %s != %s\n' \
        "${worktree_oid}" "${fetched_oid}" >&2
      return 1
    fi
    printf '%s\n' "${fetched_oid}"
    return 0
  fi

  if is_target_relative_base_ref "${base_ref}"; then
    if ! resolved="$(
      isolated_git -C "${worktree}" rev-parse --verify \
        "${base_ref}^{commit}" 2>/dev/null
    )"; then
      printf 'base does not resolve to a commit: %s\n' \
        "${base_ref}" >&2
      return 1
    fi
    printf '%s\n' "${resolved}"
    return 0
  fi

  case "${base_ref}" in
    refs/heads/*|refs/tags/*)
      if ! isolated_git check-ref-format "${base_ref}" >/dev/null 2>&1; then
        printf 'base does not resolve to a commit: %s\n' \
          "${base_ref}" >&2
        return 1
      fi
      patterns=("${base_ref}")
      ;;
    *)
      if ! isolated_git check-ref-format \
        "refs/heads/${base_ref}" >/dev/null 2>&1; then
        printf 'base does not resolve to a commit: %s\n' \
          "${base_ref}" >&2
        return 1
      fi
      patterns=("refs/heads/${base_ref}" "refs/tags/${base_ref}")
      ;;
  esac
  if ! remote_output="$(
    isolated_git ls-remote --refs "${source_repo}" "${patterns[@]}"
  )"; then
    printf 'failed to resolve base ref %s from %s\n' \
      "${base_ref}" "${source_repo}" >&2
    return 1
  fi
  while IFS=$'\t' read -r candidate_oid candidate_ref; do
    if [[ -z "${candidate_oid}" || -z "${candidate_ref}" ]]; then
      continue
    fi
    for pattern in "${patterns[@]}"; do
      if [[ "${candidate_ref}" == "${pattern}" ]]; then
        remote_oid="${candidate_oid}"
        remote_ref="${candidate_ref}"
        match_count=$((match_count + 1))
      fi
    done
  done <<< "${remote_output}"
  if [[ "${match_count}" -ne 1 ]]; then
    printf 'base ref must identify exactly one source head or tag: %s\n' \
      "${base_ref}" >&2
    return 1
  fi
  if ! isolated_git -C "${source_verifier}" fetch --no-tags \
    "${source_repo}" "${remote_ref}"; then
    printf 'failed to fetch base ref %s from %s\n' \
      "${remote_ref}" "${source_repo}" >&2
    return 1
  fi
  fetched_oid="$(
    isolated_git -C "${source_verifier}" rev-parse --verify FETCH_HEAD
  )"
  if [[ "${fetched_oid}" != "${remote_oid}" ]]; then
    printf 'fetched base ref changed while resolving: %s\n' \
      "${remote_ref}" >&2
    return 1
  fi
  isolated_git -C "${worktree}" fetch --no-tags \
    "${source_verifier}" "${fetched_oid}"
  worktree_oid="$(
    isolated_git -C "${worktree}" rev-parse --verify FETCH_HEAD
  )"
  if [[ "${worktree_oid}" != "${fetched_oid}" ]]; then
    printf 'worktree base ref mismatch: %s != %s\n' \
      "${worktree_oid}" "${fetched_oid}" >&2
    return 1
  fi
  if ! resolved="$(
    isolated_git -C "${source_verifier}" rev-parse --verify \
      'FETCH_HEAD^{commit}' 2>/dev/null
  )"; then
    printf 'base ref does not resolve to a commit: %s\n' \
      "${remote_ref}" >&2
    return 1
  fi
  printf '%s\n' "${resolved}"
}

validate_checkout_metadata() {
  local checkout="$1"
  local checkout_label="$2"
  local top_level
  local git_dir
  local dangerous_config
  local local_exclude
  local unsafe_index_entry

  if ! top_level="$(
    isolated_git -C "${checkout}" rev-parse --show-toplevel
  )"; then
    printf '%s is not a Git checkout: %s\n' \
      "${checkout_label}" "${checkout}" >&2
    exit 1
  fi
  top_level="$(realpath -m -- "${top_level}")"
  if [[ "${top_level}" != "${checkout}" ]]; then
    printf '%s resolves to a different Git checkout: %s != %s\n' \
      "${checkout_label}" "${top_level}" "${checkout}" >&2
    exit 1
  fi

  if ! dangerous_config="$(
    isolated_git -C "${checkout}" config \
      --local --includes --name-only --list |
      LC_ALL=C awk '
        {
          name = tolower($0)
          if (name ~ /^filter\..*\.(clean|smudge|process|required)$/ ||
              name == "diff.external" ||
              name ~ /^diff\..*\.(command|textconv)$/ ||
              name == "core.fsmonitor" ||
              name == "core.untrackedcache" ||
              name == "core.sparsecheckout" ||
              name == "core.attributesfile" ||
              name == "core.excludesfile" ||
              name == "include.path" ||
              name ~ /^includeif\..*\.path$/ ||
              name ~ /^url\..*\.(insteadof|pushinsteadof)$/ ||
              name ~ /^remote\..*\.uploadpack$/) {
            print
          }
        }
      '
  )"; then
    printf 'failed to inspect %s configuration: %s\n' \
      "${checkout_label}" "${checkout}" >&2
    exit 1
  fi
  if [[ -n "${dangerous_config}" ]]; then
    printf '%s has unsafe local Git configuration: %s\n' \
      "${checkout_label}" "${dangerous_config//$'\n'/, }" >&2
    exit 1
  fi

  git_dir="$(
    isolated_git -C "${checkout}" rev-parse --absolute-git-dir
  )"
  if [[ -e "${git_dir}/info/attributes" ||
        -L "${git_dir}/info/attributes" ]]; then
    printf '%s has local Git attributes: %s\n' \
      "${checkout_label}" "${git_dir}/info/attributes" >&2
    exit 1
  fi
  if [[ -L "${git_dir}/info/exclude" ]]; then
    printf '%s has a symlinked local Git exclude file: %s\n' \
      "${checkout_label}" "${git_dir}/info/exclude" >&2
    exit 1
  fi
  if [[ -f "${git_dir}/info/exclude" ]]; then
    local_exclude="$(
      LC_ALL=C awk '
        /^[[:space:]]*($|#)/ { next }
        { print; exit }
      ' "${git_dir}/info/exclude"
    )"
    if [[ -n "${local_exclude}" ]]; then
      printf '%s has local Git exclude patterns: %s\n' \
        "${checkout_label}" "${local_exclude}" >&2
      exit 1
    fi
  fi

  unsafe_index_entry="$(
    isolated_git -C "${checkout}" ls-files -v |
      LC_ALL=C awk '
        $1 != "H" && !found { first = $0; found = 1 }
        END { if (found) print first }
      '
  )"
  if [[ -n "${unsafe_index_entry}" ]]; then
    printf '%s has non-default index flags: %s\n' \
      "${checkout_label}" "${unsafe_index_entry}" >&2
    exit 1
  fi
}

validate_reused_worktree() {
  validate_checkout_metadata "${worktree}" "experiment worktree"
}

require_clean_worktree() {
  local status_output
  local clean_candidates

  status_output="$(
    isolated_git -C "${worktree}" status \
      --porcelain=v1 \
      --untracked-files=all \
      --ignore-submodules=none
  )"
  clean_candidates="$(
    isolated_git -C "${worktree}" clean -ndx --
  )"
  if [[ -n "${status_output}" || -n "${clean_candidates}" ]]; then
    printf 'experiment worktree is dirty: %s\n' "${worktree}" >&2
    if [[ -n "${clean_candidates}" ]]; then
      printf 'untracked or ignored worktree paths would survive checkout:\n%s\n' \
        "${clean_candidates}" >&2
    fi
    exit 1
  fi
}

verify_target_checkout() {
  local current_target

  validate_checkout_metadata "${worktree}" "experiment worktree"
  current_target="$(
    isolated_git -C "${worktree}" rev-parse --verify 'HEAD^{commit}'
  )"
  if [[ "${current_target}" != "${resolved_target}" ]]; then
    printf 'experiment worktree target changed during run: %s != %s\n' \
      "${current_target}" "${resolved_target}" >&2
    exit 1
  fi
  require_clean_worktree
}

require_final_run_absent() {
  if [[ -e "${final_run_dir}" || -L "${final_run_dir}" ]]; then
    printf 'run already exists: %s\n' "${final_run_dir}" >&2
    exit 1
  fi
}

print_configuration() {
  printf 'run_id=%s\n' "${run_id}"
  printf 'source=%s\n' "${source_repo}"
  printf 'worktree=%s\n' "${worktree}"
  printf 'commit=%s\n' "${target_commit}"
  printf 'prompt_commit=%s\n' "${prompt_commit}"
  printf 'model=%s\n' "${generation_model}"
  printf 'model_mode=%s\n' "${generation_model_mode}"
  printf 'codex_version=%s\n' "${expected_codex_version}"
  printf 'go_version=%s\n' "${expected_go_version}"
  printf 'base=%s\n' "${base_ref}"
  printf 'task=%s\n' "${task}"
  printf 'variant=%s\n' "${variant}"
  printf 'profiles=%s\n' "$(IFS=,; printf '%s' "${selected_profiles[*]}")"
  printf 'baseline_from=%s\n' "${baseline_from:-none}"
  printf 'order=%s\n' "${order}"
  printf 'evidence=%s\n' "${run_dir}"
}

if "${dry_run}"; then
  print_configuration
  for selected_task in "${selected_tasks[@]}"; do
    printf '\n%s prompt:\n%s\n' "${selected_task}" \
      "$(render_prompt \
        "${experiment_dir}/prompts/${selected_task}.txt" \
        "${prompt_commit}" "${base_ref}")"
  done
  printf '\nProfiles:\n'
  for selected_profile in "${selected_profiles[@]}"; do
    load_profile "${selected_profile}"
    printf '%s return=%s context=%s reasoning=%s answer_guard=%s navigation=%s navigation_command_cap=%s\n' \
      "${profile_name}" "${profile_return}" "${profile_context}" \
      "$(effective_profile_reasoning)" "${profile_answer_guard}" \
      "${profile_navigation_policy}" "${profile_navigation_command_cap}"
  done
  exit 0
fi

mkdir -p -- "$(dirname "${worktree}")" "${evidence_root}"
worktree="$(realpath -m -- "${worktree}")"
evidence_root="$(realpath -m -- "${evidence_root}")"
final_run_dir="${evidence_root}/${run_id}"
validate_checkout_metadata "${repo_root}" "repo-view source"
require_generated_path_excluded "${worktree}" "--worktree"
require_generated_path_excluded "${worktree}.lock" "--worktree lock"
require_generated_path_excluded "${evidence_root}" "--evidence-root"

worktree_lock="${worktree}.lock"
run_claim_lock="${evidence_root}/.${run_id}.lock"
run_stage=""
run_published=false
worktree_lock_owned=false
run_claim_lock_owned=false
worktree_lock_identity=""
run_claim_lock_identity=""
run_stage_identity=""
run_stage_fd=""
run_stage_fd_open=false
source_verifier_fd=""
source_verifier_fd_open=false
source_verifier_identity=""
source_snapshot_fd=""
source_snapshot_fd_open=false
source_snapshot_identity=""
generation_codex_home_fd=""
generation_codex_home_fd_open=false
generation_codex_home_identity=""
generation_shell_home_fd=""
generation_shell_home_fd_open=false
generation_shell_home_identity=""
generation_repo_view_cache_fd=""
generation_repo_view_cache_fd_open=false
generation_repo_view_cache_identity=""
owned_directory_matches_identity() {
  local directory_path="$1"
  local expected_identity="$2"
  local actual_identity

  [[ -n "${expected_identity}" ]] &&
    actual_identity="$(
      stat -Lc '%d:%i' -- "${directory_path}" 2>/dev/null
    )" &&
    [[ "${actual_identity}" == "${expected_identity}" ]] &&
    [[ -d "${directory_path}" && ! -L "${directory_path}" ]]
}
directory_fd_matches_identity() {
  local directory_fd="$1"
  local expected_identity="$2"
  local actual_identity

  [[ -n "${directory_fd}" && -n "${expected_identity}" ]] &&
    [[ -d "/proc/self/fd/${directory_fd}" ]] &&
    actual_identity="$(
      stat -Lc '%d:%i' -- "/proc/self/fd/${directory_fd}" 2>/dev/null
    )" &&
    [[ "${actual_identity}" == "${expected_identity}" ]]
}
close_directory_fd() {
  local directory_fd="$1"
  exec {directory_fd}<&-
}
retire_owned_directory_through_fd() {
  local directory_fd="$1"
  local directory_path="$2"
  local expected_identity="$3"

  if ! directory_fd_matches_identity \
    "${directory_fd}" "${expected_identity}"; then
    printf 'owned directory descriptor identity changed: %s\n' \
      "${directory_path}" >&2
    return 1
  fi
  find -P "/proc/self/fd/${directory_fd}/." \
    -depth -mindepth 1 -delete
  if owned_directory_matches_identity \
    "${directory_path}" "${expected_identity}"; then
    rmdir -- "${directory_path}"
  fi
}
release_owned_lock_directory() {
  local lock_path="$1"
  local expected_identity="$2"

  if ! owned_directory_matches_identity \
    "${lock_path}" "${expected_identity}"; then
    return 0
  fi
  rmdir -- "${lock_path}" 2>/dev/null || true
}
cleanup_runner() {
  local exit_status=$?
  if ! "${run_published}" && "${run_stage_fd_open}" &&
    [[ -n "${run_stage}" ]] &&
    [[ "${run_stage}" == "${evidence_root}/.${run_id}.partial."* ]]; then
    retire_owned_directory_through_fd \
      "${run_stage_fd}" "${run_stage}" "${run_stage_identity}" || true
  fi
  if "${generation_repo_view_cache_fd_open}"; then
    close_directory_fd "${generation_repo_view_cache_fd}"
  fi
  if "${generation_shell_home_fd_open}"; then
    close_directory_fd "${generation_shell_home_fd}"
  fi
  if "${generation_codex_home_fd_open}"; then
    close_directory_fd "${generation_codex_home_fd}"
  fi
  if "${source_snapshot_fd_open}"; then
    close_directory_fd "${source_snapshot_fd}"
  fi
  if "${source_verifier_fd_open}"; then
    close_directory_fd "${source_verifier_fd}"
  fi
  if "${run_stage_fd_open}"; then
    close_directory_fd "${run_stage_fd}"
  fi
  if "${run_claim_lock_owned}"; then
    release_owned_lock_directory \
      "${run_claim_lock}" "${run_claim_lock_identity}"
  fi
  if "${worktree_lock_owned}"; then
    release_owned_lock_directory \
      "${worktree_lock}" "${worktree_lock_identity}"
  fi
  return "${exit_status}"
}
trap cleanup_runner EXIT

if ! mkdir -m 700 -- "${worktree_lock}" 2>/dev/null; then
  printf 'experiment worktree is already in use: %s\n' "${worktree}" >&2
  exit 1
fi
worktree_lock_identity="$(stat -Lc '%d:%i' -- "${worktree_lock}")"
worktree_lock_owned=true
require_final_run_absent

if ! mkdir -m 700 -- "${run_claim_lock}" 2>/dev/null; then
  printf 'run is already in progress: %s\n' "${final_run_dir}" >&2
  exit 1
fi
run_claim_lock_identity="$(stat -Lc '%d:%i' -- "${run_claim_lock}")"
run_claim_lock_owned=true
require_final_run_absent

run_stage="$(mktemp -d "${evidence_root}/.${run_id}.partial.XXXXXX")"
exec {run_stage_fd}<"${run_stage}"
run_stage_fd_open=true
run_stage_identity="$(
  stat -Lc '%d:%i' -- "/proc/self/fd/${run_stage_fd}"
)"
run_dir="${run_stage}"

source_verifier="${run_dir}/.source-verifier.git"
if [[ "${#target_commit}" -eq 64 ]]; then
  isolated_git init --quiet --bare --object-format=sha256 "${source_verifier}"
  expected_target_object_format=sha256
else
  isolated_git init --quiet --bare --object-format=sha1 "${source_verifier}"
  expected_target_object_format=sha1
fi
exec {source_verifier_fd}<"${source_verifier}"
source_verifier_fd_open=true
source_verifier_identity="$(
  stat -Lc '%d:%i' -- "/proc/self/fd/${source_verifier_fd}"
)"
codex_executable="$(command -v codex)"
codex_executable="$(realpath -- "${codex_executable}")"
codex_bin_dir="$(dirname "${codex_executable}")"
actual_codex_version="$(
  "${codex_executable}" --version 2>/dev/null || true
)"
if [[ "${actual_codex_version}" != "${expected_codex_version}" ]]; then
  printf 'Codex version mismatch: %s != %s\n' \
    "${actual_codex_version:-missing}" "${expected_codex_version}" >&2
  exit 1
fi
pinned_host_go_environment=(
  env
  -u GOOS
  -u GOARCH
  -u GO386
  -u GOAMD64
  -u GOARM
  -u GOARM64
  -u GOMIPS
  -u GOMIPS64
  -u GOPPC64
  -u GORISCV64
  -u GOWASM
  -u CGO_ENABLED
  -u CC
  -u CXX
  -u CGO_CFLAGS
  -u CGO_CPPFLAGS
  -u CGO_CXXFLAGS
  -u CGO_LDFLAGS
  -u PKG_CONFIG
  -u GOROOT
  -u GOPATH
  -u GOMODCACHE
  -u GOCACHE
  -u GOEXPERIMENT
  -u GODEBUG
  "GO111MODULE=on"
  "GOENV=off"
  "GOTOOLCHAIN=local"
  "GOWORK=off"
  "GOFLAGS=-mod=readonly -trimpath -buildvcs=false"
  "GOPROXY=https://proxy.golang.org,direct"
  "GONOPROXY="
  "GOPRIVATE="
  "GONOSUMDB="
  "GOSUMDB=sum.golang.org"
  "GOINSECURE="
  "GOVCS=public:git|hg,private:all"
  "GOAUTH=off"
)
actual_go_version="$(
  "${pinned_host_go_environment[@]}" go version 2>/dev/null || true
)"
pinned_host_go_execution_environment=("${pinned_host_go_environment[@]}")
if [[ "${actual_go_version}" != "${expected_go_version}" ]]; then
  expected_go_toolchain="${expected_go_version#go version }"
  expected_go_toolchain="${expected_go_toolchain%% *}"
  if [[ ! "${expected_go_toolchain}" =~ ^go[0-9]+\.[0-9]+(\.[0-9]+)?$ ]]; then
    printf 'cannot select expected Go toolchain from version: %s\n' \
      "${expected_go_version}" >&2
    exit 1
  fi
  toolchain_selection_environment=("${pinned_host_go_environment[@]}")
  for index in "${!toolchain_selection_environment[@]}"; do
    if [[ "${toolchain_selection_environment[index]}" == "GOTOOLCHAIN=local" ]]; then
      toolchain_selection_environment[index]="GOTOOLCHAIN=${expected_go_toolchain}"
    fi
  done
  selected_go_root="$(
    "${toolchain_selection_environment[@]}" go env GOROOT 2>/dev/null || true
  )"
  if [[ -z "${selected_go_root}" || ! -x "${selected_go_root}/bin/go" ]]; then
    printf 'failed to resolve expected Go toolchain: %s\n' \
      "${expected_go_toolchain}" >&2
    exit 1
  fi
  pinned_host_go_execution_environment+=(
    "PATH=${selected_go_root}/bin:/usr/local/bin:/usr/bin:/bin"
  )
  actual_go_version="$(
    "${pinned_host_go_execution_environment[@]}" go version 2>/dev/null || true
  )"
fi
if [[ "${actual_go_version}" != "${expected_go_version}" ]]; then
  printf 'Go version mismatch: %s != %s\n' \
    "${actual_go_version:-missing}" "${expected_go_version}" >&2
  exit 1
fi
if ! generation_go_environment_json="$(
  "${pinned_host_go_execution_environment[@]}" \
    go env -json GOROOT GOPATH GOMODCACHE GOCACHE
)"; then
  printf 'failed to resolve pinned Go environment\n' >&2
  exit 1
fi
generation_go_root="$(
  jq -er '.GOROOT | select(type == "string" and length > 0)' \
    <<< "${generation_go_environment_json}"
)"
generation_go_path="$(
  jq -er '.GOPATH | select(type == "string" and length > 0)' \
    <<< "${generation_go_environment_json}"
)"
generation_go_mod_cache="$(
  jq -er '.GOMODCACHE | select(type == "string" and length > 0)' \
    <<< "${generation_go_environment_json}"
)"
generation_go_cache="$(
  jq -er '.GOCACHE | select(type == "string" and length > 0)' \
    <<< "${generation_go_environment_json}"
)"
generation_codex_home="${run_dir}/.codex-home"
mkdir -m 700 -- "${generation_codex_home}"
exec {generation_codex_home_fd}<"${generation_codex_home}"
generation_codex_home_fd_open=true
generation_codex_home_identity="$(
  stat -Lc '%d:%i' -- "/proc/self/fd/${generation_codex_home_fd}"
)"
generation_shell_home="${run_dir}/.shell-home"
mkdir -m 700 -- "${generation_shell_home}"
exec {generation_shell_home_fd}<"${generation_shell_home}"
generation_shell_home_fd_open=true
generation_shell_home_identity="$(
  stat -Lc '%d:%i' -- "/proc/self/fd/${generation_shell_home_fd}"
)"
generation_repo_view_cache="${run_dir}/.repo-view-cache"
mkdir -m 700 -- "${generation_repo_view_cache}"
exec {generation_repo_view_cache_fd}<"${generation_repo_view_cache}"
generation_repo_view_cache_fd_open=true
generation_repo_view_cache_identity="$(
  stat -Lc '%d:%i' -- "/proc/self/fd/${generation_repo_view_cache_fd}"
)"
codex_auth_source="${CODEX_HOME:-${HOME}/.codex}/auth.json"
codex_auth_canonical=""
if [[ -f "${codex_auth_source}" && ! -L "${codex_auth_source}" ]]; then
  codex_auth_canonical="$(realpath -- "${codex_auth_source}")"
  ln -s -- "${codex_auth_source}" "${generation_codex_home}/auth.json"
fi
generation_codex_home_toml="$(
  jq -Rn --arg value "${generation_codex_home}" '$value'
)"
generation_shell_home_toml="$(
  jq -Rn --arg value "${generation_shell_home}" '$value'
)"
generation_shell_path="${generation_repo_view_cache}/bin:${codex_bin_dir}:${generation_go_root}/bin:/usr/local/bin:/usr/bin:/bin"
generation_path_toml="$(
  jq -Rn --arg value "${generation_shell_path}" '$value'
)"
generation_worktree_toml="$(
  jq -Rn --arg value "${worktree}" '$value'
)"
generation_repo_view_cache_toml="$(
  jq -Rn --arg value "${generation_repo_view_cache}" '$value'
)"
generation_go_root_toml="$(
  jq -Rn --arg value "${generation_go_root}" '$value'
)"
generation_go_path_toml="$(
  jq -Rn --arg value "${generation_go_path}" '$value'
)"
generation_go_mod_cache_toml="$(
  jq -Rn --arg value "${generation_go_mod_cache}" '$value'
)"
generation_go_cache_toml="$(
  jq -Rn --arg value "${generation_go_cache}" '$value'
)"
benchmark_filesystem_config='":root"="deny", ":minimal"="read"'
declare -A benchmark_permission_paths=()
append_benchmark_permission() {
  local path="$1"
  local access="$2"
  local path_toml

  if [[ -n "${benchmark_permission_paths[${path}]+x}" ]]; then
    return 0
  fi
  benchmark_permission_paths["${path}"]="${access}"
  path_toml="$(jq -Rn --arg value "${path}" '$value')"
  benchmark_filesystem_config+=", ${path_toml}=\"${access}\""
}
append_benchmark_permission "${worktree}" read
append_benchmark_permission "${generation_go_root}" read
append_benchmark_permission "${generation_go_mod_cache}" read
append_benchmark_permission "${generation_go_cache}" read
append_benchmark_permission "${generation_repo_view_cache}" read
append_benchmark_permission "${generation_shell_home}" read
append_benchmark_permission "${codex_executable}" read
append_benchmark_permission "${generation_codex_home}" deny
if [[ -n "${codex_auth_canonical}" ]]; then
  append_benchmark_permission "${codex_auth_canonical}" deny
fi
benchmark_permissions_config="$(
  printf 'permissions.benchmark={extends=":read-only", filesystem={%s}}' \
    "${benchmark_filesystem_config}"
)"
shell_environment_set_config="$(
  printf '%s' \
    "shell_environment_policy.set={PATH=${generation_path_toml}," \
    "HOME=${generation_shell_home_toml}," \
    'LANG="C",LC_ALL="C",TZ="UTC",' \
    "GOROOT=${generation_go_root_toml}," \
    "GOPATH=${generation_go_path_toml}," \
    "GOMODCACHE=${generation_go_mod_cache_toml}," \
    "GOCACHE=${generation_go_cache_toml}," \
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
    'GIT_TERMINAL_PROMPT="0",GIT_NO_REPLACE_OBJECTS="1",' \
    'GIT_OPTIONAL_LOCKS="0",' \
    'GIT_DISCOVERY_ACROSS_FILESYSTEM="0",GIT_PAGER="cat",PAGER="cat"}'
)"

no_collaboration='Do not call collaboration, subagent, spawn-agent, or agent-wait tools. Do not read or invoke Codex skills, plugins, hooks, or marketplace resources; they are outside this benchmark.'
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
codex_isolation_flags=(
  --ignore-user-config
  --ignore-rules
  -c 'default_permissions="benchmark"'
  -c "${benchmark_permissions_config}"
  -c 'shell_environment_policy.inherit="none"'
  -c 'shell_environment_policy.ignore_default_excludes=false'
  -c 'shell_environment_policy.experimental_use_profile=false'
  -c "${shell_environment_set_config}"
  -c 'project_doc_max_bytes=0'
  -c 'project_doc_fallback_filenames=[]'
  -c 'mcp_servers={}'
  -c 'apps._default.enabled=false'
)
safe_git_environment=(
  env
  -u GIT_CONFIG
  -u GIT_CONFIG_PARAMETERS
  -u GIT_DIR
  -u GIT_WORK_TREE
  -u GIT_INDEX_FILE
  -u GIT_OBJECT_DIRECTORY
  -u GIT_ALTERNATE_OBJECT_DIRECTORIES
  -u GIT_COMMON_DIR
  -u GIT_EXEC_PATH
  -u GIT_EXTERNAL_DIFF
  -u GIT_SSH
  -u GIT_SSH_COMMAND
  -u GIT_SSH_VARIANT
  -u GIT_ASKPASS
  -u SSH_ASKPASS
  -u GIT_PROXY_COMMAND
  -u GIT_NAMESPACE
  -u GIT_REPLACE_REF_BASE
  -u GIT_CEILING_DIRECTORIES
  -u GIT_DISCOVERY_ACROSS_FILESYSTEM
  -u GIT_OPTIONAL_LOCKS
  "GIT_CONFIG_NOSYSTEM=1"
  "GIT_CONFIG_GLOBAL=/dev/null"
  "GIT_ATTR_NOSYSTEM=1"
  "GIT_CONFIG_COUNT=10"
  "GIT_CONFIG_KEY_0=core.hooksPath"
  "GIT_CONFIG_VALUE_0=/dev/null"
  "GIT_CONFIG_KEY_1=core.attributesFile"
  "GIT_CONFIG_VALUE_1=/dev/null"
  "GIT_CONFIG_KEY_2=core.excludesFile"
  "GIT_CONFIG_VALUE_2=/dev/null"
  "GIT_CONFIG_KEY_3=core.autocrlf"
  "GIT_CONFIG_VALUE_3=false"
  "GIT_CONFIG_KEY_4=core.eol"
  "GIT_CONFIG_VALUE_4=lf"
  "GIT_CONFIG_KEY_5=core.safecrlf"
  "GIT_CONFIG_VALUE_5=false"
  "GIT_CONFIG_KEY_6=core.fsmonitor"
  "GIT_CONFIG_VALUE_6=false"
  "GIT_CONFIG_KEY_7=core.untrackedCache"
  "GIT_CONFIG_VALUE_7=false"
  "GIT_CONFIG_KEY_8=core.sparseCheckout"
  "GIT_CONFIG_VALUE_8=false"
  "GIT_CONFIG_KEY_9=core.filemode"
  "GIT_CONFIG_VALUE_9=true"
  "GIT_TERMINAL_PROMPT=0"
  "GIT_NO_REPLACE_OBJECTS=1"
  "GIT_OPTIONAL_LOCKS=0"
  "GIT_DISCOVERY_ACROSS_FILESYSTEM=0"
  "GIT_PAGER=cat"
)
codex_environment=(
  env -i
  "PATH=${generation_shell_path}"
  "HOME=${generation_shell_home}"
  "CODEX_HOME=${generation_codex_home}"
  "LANG=C"
  "LC_ALL=C"
  "TZ=UTC"
  "GOROOT=${generation_go_root}"
  "GOPATH=${generation_go_path}"
  "GOMODCACHE=${generation_go_mod_cache}"
  "GOCACHE=${generation_go_cache}"
  "GO111MODULE=on"
  "GOENV=off"
  "GOTOOLCHAIN=local"
  "GOWORK=off"
  "GOFLAGS=-mod=readonly -trimpath -buildvcs=false"
  "GOPROXY=https://proxy.golang.org,direct"
  "GONOPROXY="
  "GOPRIVATE="
  "GONOSUMDB="
  "GOSUMDB=sum.golang.org"
  "GOINSECURE="
  "GOVCS=public:git|hg,private:all"
  "GOAUTH=off"
  "GIT_CONFIG_NOSYSTEM=1"
  "GIT_CONFIG_GLOBAL=/dev/null"
  "GIT_ATTR_NOSYSTEM=1"
  "GIT_CONFIG_COUNT=10"
  "GIT_CONFIG_KEY_0=core.hooksPath"
  "GIT_CONFIG_VALUE_0=/dev/null"
  "GIT_CONFIG_KEY_1=core.attributesFile"
  "GIT_CONFIG_VALUE_1=/dev/null"
  "GIT_CONFIG_KEY_2=core.excludesFile"
  "GIT_CONFIG_VALUE_2=/dev/null"
  "GIT_CONFIG_KEY_3=core.autocrlf"
  "GIT_CONFIG_VALUE_3=false"
  "GIT_CONFIG_KEY_4=core.eol"
  "GIT_CONFIG_VALUE_4=lf"
  "GIT_CONFIG_KEY_5=core.safecrlf"
  "GIT_CONFIG_VALUE_5=false"
  "GIT_CONFIG_KEY_6=core.fsmonitor"
  "GIT_CONFIG_VALUE_6=false"
  "GIT_CONFIG_KEY_7=core.untrackedCache"
  "GIT_CONFIG_VALUE_7=false"
  "GIT_CONFIG_KEY_8=core.sparseCheckout"
  "GIT_CONFIG_VALUE_8=false"
  "GIT_CONFIG_KEY_9=core.filemode"
  "GIT_CONFIG_VALUE_9=true"
  "GIT_TERMINAL_PROMPT=0"
  "GIT_NO_REPLACE_OBJECTS=1"
  "GIT_OPTIONAL_LOCKS=0"
  "GIT_DISCOVERY_ACROSS_FILESYSTEM=0"
  "GIT_PAGER=cat"
  "PAGER=cat"
)

normalized_permissions='permissions.benchmark={extends=":read-only", filesystem={":root"="deny", ":minimal"="read", "<worktree>"="read", "<go-root>"="read", "<go-mod-cache>"="read", "<go-cache>"="read", "<repo-view-cache>"="read", "<shell-home>"="read", "<codex-executable>"="read", "<codex-home>"="deny"}}'
normalized_shell_environment='shell_environment_policy.set={PATH="<repo-view-cache>/bin:<codex-bin>:<go-root>/bin:/usr/local/bin:/usr/bin:/bin",HOME="<shell-home>",LANG="C",LC_ALL="C",TZ="UTC",GOROOT="<go-root>",GOPATH="<go-path>",GOMODCACHE="<go-mod-cache>",GOCACHE="<go-cache>",GOENV="off",GOTOOLCHAIN="local",GOWORK="off",GOFLAGS="-mod=readonly -trimpath -buildvcs=false",GIT_CONFIG_NOSYSTEM="1",GIT_CONFIG_GLOBAL="/dev/null",GIT_ATTR_NOSYSTEM="1",GIT_CONFIG_COUNT="10",GIT_CONFIG_KEY_0="core.hooksPath",GIT_CONFIG_VALUE_0="/dev/null",GIT_CONFIG_KEY_1="core.attributesFile",GIT_CONFIG_VALUE_1="/dev/null",GIT_CONFIG_KEY_2="core.excludesFile",GIT_CONFIG_VALUE_2="/dev/null",GIT_CONFIG_KEY_3="core.autocrlf",GIT_CONFIG_VALUE_3="false",GIT_CONFIG_KEY_4="core.eol",GIT_CONFIG_VALUE_4="lf",GIT_CONFIG_KEY_5="core.safecrlf",GIT_CONFIG_VALUE_5="false",GIT_CONFIG_KEY_6="core.fsmonitor",GIT_CONFIG_VALUE_6="false",GIT_CONFIG_KEY_7="core.untrackedCache",GIT_CONFIG_VALUE_7="false",GIT_CONFIG_KEY_8="core.sparseCheckout",GIT_CONFIG_VALUE_8="false",GIT_CONFIG_KEY_9="core.filemode",GIT_CONFIG_VALUE_9="true",GIT_TERMINAL_PROMPT="0",GIT_NO_REPLACE_OBJECTS="1",GIT_OPTIONAL_LOCKS="0",GIT_DISCOVERY_ACROSS_FILESYSTEM="0",GIT_PAGER="cat",PAGER="cat"}'

normalized_codex_isolation_flags=()
for generation_flag in "${codex_isolation_flags[@]}"; do
  if [[ "${generation_flag}" == "${benchmark_permissions_config}" ]]; then
    generation_flag="${normalized_permissions}"
  elif [[ "${generation_flag}" == "${shell_environment_set_config}" ]]; then
    generation_flag="${normalized_shell_environment}"
  fi
  normalized_codex_isolation_flags+=("${generation_flag}")
done
normalized_codex_environment=()
for generation_setting in "${codex_environment[@]}"; do
  case "${generation_setting}" in
    PATH=*) generation_setting='PATH=<generation-path>' ;;
    HOME=*) generation_setting='HOME=<shell-home>' ;;
    CODEX_HOME=*) generation_setting='CODEX_HOME=<codex-home>' ;;
    GOROOT=*) generation_setting='GOROOT=<go-root>' ;;
    GOPATH=*) generation_setting='GOPATH=<go-path>' ;;
    GOMODCACHE=*) generation_setting='GOMODCACHE=<go-mod-cache>' ;;
    GOCACHE=*) generation_setting='GOCACHE=<go-cache>' ;;
  esac
  normalized_codex_environment+=("${generation_setting}")
done
feature_flags_json="$(
  jq -cn --args '$ARGS.positional' -- "${feature_flags[@]}"
)"
codex_isolation_flags_json="$(
  jq -cn --args '$ARGS.positional' -- "${normalized_codex_isolation_flags[@]}"
)"
codex_environment_json="$(
  jq -cn --args '$ARGS.positional' -- "${normalized_codex_environment[@]}"
)"
host_go_environment_json="$(
  jq -cn --args '$ARGS.positional' -- "${pinned_host_go_environment[@]}"
)"
mechanical_navigation_semantics_enforced=false
if [[ "${variant}" != "baseline" ]]; then
  mechanical_navigation_semantics_enforced=true
fi
build_generation_config() {
  generation_config_json="$(
    jq -cSn \
      --arg generation_isolation "${generation_isolation}" \
      --arg developer_instructions "${no_collaboration}" \
      --argjson feature_flags "${feature_flags_json}" \
      --argjson codex_isolation_flags "${codex_isolation_flags_json}" \
      --argjson codex_environment "${codex_environment_json}" \
      --argjson host_go_environment "${host_go_environment_json}" \
      --arg profiles_snapshot_path "profiles-snapshot.tsv" \
      --arg profiles_snapshot_sha256 "${profiles_snapshot_sha256}" \
      --argjson prompt_files "${prompt_files_json}" \
      --argjson prompt_digests "${prompt_digests_json}" \
      --argjson mechanical_navigation_semantics_enforced \
        "${mechanical_navigation_semantics_enforced}" \
      '{
        generation_isolation: $generation_isolation,
        baseline_developer_instructions: $developer_instructions,
        feature_flags: $feature_flags,
        codex_isolation_flags: $codex_isolation_flags,
        codex_environment: $codex_environment,
        host_go_environment: $host_go_environment,
        profiles_snapshot_path: $profiles_snapshot_path,
        profiles_snapshot_sha256: $profiles_snapshot_sha256,
        prompt_files: $prompt_files,
        prompt_digests: $prompt_digests,
        mechanical_navigation_semantics_enforced:
          $mechanical_navigation_semantics_enforced,
        mechanical_navigation_contract: {
          required_root: "<worktree>",
          required_base_commit: "<resolved-base>",
          required_changed_return: "<profile-return>",
          required_changed_context: "<profile-context>",
          require_navigation_semantics: "1"
        },
        auth_source_permission: "deny-if-present"
      }'
  )"
  generation_config_digest_output="$(
    printf '%s' "${generation_config_json}" | sha256sum
  )"
  generation_config_sha256="${generation_config_digest_output%% *}"
}

new_clone=false
if [[ ! -e "${worktree}/.git" && ! -L "${worktree}/.git" ]]; then
  isolated_git clone --no-hardlinks --no-checkout \
    "${source_repo}" "${worktree}"
  new_clone=true
else
  validate_reused_worktree
fi

if ! "${new_clone}"; then
  require_clean_worktree
fi
actual_target_object_format="$(
  isolated_git -C "${worktree}" rev-parse --show-object-format
)"
if [[ "${actual_target_object_format}" != "${expected_target_object_format}" ]]; then
  printf 'experiment worktree object format mismatch: %s != %s\n' \
    "${actual_target_object_format}" "${expected_target_object_format}" >&2
  exit 1
fi
if ! isolated_git -C "${source_verifier}" fetch --no-tags \
  "${source_repo}" "${target_commit}"; then
  printf 'failed to fetch target commit %s from %s\n' \
    "${target_commit}" "${source_repo}" >&2
  exit 1
fi
expected_target="$(
  isolated_git -C "${source_verifier}" rev-parse --verify 'FETCH_HEAD^{commit}'
)"
if [[ "${expected_target}" != "${target_commit}" ]]; then
  printf 'fetched target mismatch: %s != %s\n' \
    "${expected_target}" "${target_commit}" >&2
  exit 1
fi
isolated_git -C "${worktree}" fetch --no-tags \
  "${source_verifier}" "${expected_target}"
worktree_fetched_target="$(
  isolated_git -C "${worktree}" rev-parse --verify 'FETCH_HEAD^{commit}'
)"
if [[ "${worktree_fetched_target}" != "${expected_target}" ]]; then
  printf 'worktree target mismatch: %s != %s\n' \
    "${worktree_fetched_target}" "${expected_target}" >&2
  exit 1
fi
isolated_git -C "${worktree}" -c advice.detachedHead=false \
  checkout --quiet --detach "${expected_target}"
validate_checkout_metadata "${worktree}" "experiment worktree"
require_clean_worktree

resolved_target="$(
  isolated_git -C "${worktree}" rev-parse --verify 'HEAD^{commit}'
)"
if [[ "${resolved_target}" != "${expected_target}" ]]; then
  printf 'resolved target mismatch: %s != %s\n' \
    "${resolved_target}" "${expected_target}" >&2
  exit 1
fi
submodule_entry="$(
  isolated_git -C "${worktree}" ls-files --stage |
    LC_ALL=C awk '
      $1 == "160000" && !found { first = $0; found = 1 }
      END { if (found) print first }
    '
)"
if [[ -n "${submodule_entry}" ]]; then
  printf 'target contains submodules that are not materialized reproducibly: %s\n' \
    "${submodule_entry}" >&2
  exit 1
fi
if ! resolved_base="$(resolve_base_commit)"; then
  exit 1
fi
if ! isolated_git -C "${worktree}" merge-base --is-ancestor \
  "${resolved_base}" "${resolved_target}"; then
  printf 'base commit is not an ancestor of target: %s !<= %s\n' \
    "${resolved_base}" "${resolved_target}" >&2
  exit 1
fi
prepare_repo_view_source_snapshot
cp -- "${runtime_experiment_dir}/profiles.tsv" \
  "${run_dir}/profiles-snapshot.tsv"
profiles_snapshot_digest_output="$(
  sha256sum -- "${run_dir}/profiles-snapshot.tsv"
)"
profiles_snapshot_sha256="${profiles_snapshot_digest_output%% *}"
profile_file="${run_dir}/profiles-snapshot.tsv"
for selected_profile in "${selected_profiles[@]}"; do
  load_profile "${selected_profile}"
done
prepare_rendered_prompts
build_generation_config
printf '%s' "${generation_config_json}" \
  > "${run_dir}/generation-config.json"
verify_generation_input_snapshots
if [[ -n "${baseline_from}" ]]; then
  prepare_baseline_import
fi
(
  cd "${runner_source_root}"
  "${pinned_host_go_execution_environment[@]}" \
    go build -o "${run_dir}/repo-view.bin" ./cmd/repo-view
)
(
  cd "${run_dir}"
  sha256sum repo-view.bin repo-view-source.tar.gz > source-SHA256SUMS
)

{
  date -u '+created_at=%Y-%m-%dT%H:%M:%SZ'
  isolated_git --version
  "${pinned_host_go_execution_environment[@]}" go version
  codex --version
  jq --version
  uname -a
} > "${run_dir}/environment.txt"

jq -n \
  --arg run_id "${run_id}" \
  --arg created_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --arg source_repo "${source_repo}" \
  --arg worktree "${worktree}" \
  --arg target_commit "${resolved_target}" \
  --arg prompt_commit "${prompt_commit}" \
  --arg model "${generation_model}" \
  --arg model_mode "${generation_model_mode}" \
  --arg model_configuration "$([[ "${generation_model_mode}" == "router" ]] && printf none || printf pinned)" \
  --arg codex_version "${actual_codex_version}" \
  --arg generation_isolation "${generation_isolation}" \
  --arg generation_config_sha256 "${generation_config_sha256}" \
  --arg profiles_snapshot_path "profiles-snapshot.tsv" \
  --arg profiles_snapshot_sha256 "${profiles_snapshot_sha256}" \
  --arg go_version "${actual_go_version}" \
  --arg go_root "${generation_go_root}" \
  --arg go_path "${generation_go_path}" \
  --arg go_mod_cache "${generation_go_mod_cache}" \
  --arg go_cache "${generation_go_cache}" \
  --arg base_ref "${base_ref}" \
  --arg base_commit "${resolved_base}" \
  --arg task "${task}" \
  --arg variant "${variant}" \
  --arg profiles "$(IFS=,; printf '%s' "${selected_profiles[*]}")" \
  --arg baseline_from "${baseline_from}" \
  --arg order "${order}" \
  --arg repo_view_head "${repo_view_head}" \
  --argjson repo_view_dirty "${repo_view_dirty}" \
  --argjson prompt_digests "${prompt_digests_json}" \
  --argjson prompt_files "${prompt_files_json}" \
  --argjson mechanical_navigation_semantics_enforced \
    "${mechanical_navigation_semantics_enforced}" \
  '{
    schema_version: 1,
    run_id: $run_id,
    created_at: $created_at,
    source_repo: $source_repo,
    worktree: $worktree,
    target_commit: $target_commit,
    prompt_commit: $prompt_commit,
    model: $model,
    model_mode: $model_mode,
    model_configuration: $model_configuration,
    codex_version: $codex_version,
    generation_isolation: $generation_isolation,
    generation_config_sha256: $generation_config_sha256,
    profiles_snapshot_path: $profiles_snapshot_path,
    profiles_snapshot_sha256: $profiles_snapshot_sha256,
    go_version: $go_version,
    go_root: $go_root,
    go_path: $go_path,
    go_mod_cache: $go_mod_cache,
    go_cache: $go_cache,
    base_ref: $base_ref,
    base_commit: $base_commit,
    task_selection: $task,
    variant_selection: $variant,
    profiles: ($profiles | split(",")),
    baseline_from: (if $baseline_from == "" then null else $baseline_from end),
    order: $order,
    repo_view_head: $repo_view_head,
    repo_view_dirty: $repo_view_dirty,
    prompt_files: $prompt_files,
    prompt_digests: $prompt_digests,
    mechanical_navigation_semantics_enforced:
      $mechanical_navigation_semantics_enforced,
    cached_input_weight: 0.1
  }' > "${run_dir}/manifest.json"

overall_status=0
transcript_validation_failed=false

should_run() {
  local requested="$1"
  local selected="$2"
  [[ "${selected}" == "all" || "${selected}" == "${requested}" ]]
}

run_case() {
  local case_variant="$1"
  local case_profile="$2"
  local case_task="$3"
  local prompt="$4"
  local stem
  local packet
  local started_seconds
  local ended_seconds
  local status
  local optimized_prompt
  local navigation_prompt_guard
  local -a command

  if [[ "${case_variant}" == "baseline" ]]; then
    stem="baseline-${case_task}"
    command=(
      "${codex_environment[@]}"
      codex exec
      "${generation_model_args[@]}"
      -c "developer_instructions=\"${no_collaboration}\""
      "${feature_flags[@]}"
      "${codex_isolation_flags[@]}"
      -C "${worktree}"
      --ephemeral
      --json
      "${prompt}"
    )
  else
    load_profile "${case_profile}"
    stem="optimized-${case_profile}-${case_task}"
    navigation_prompt_guard="$(cat <<EOF
MANDATORY experiment navigation protocol: your first repository command must be exactly:
repo-view changed --root . --base ${resolved_base} --return ${profile_return} --context ${profile_context} --limit ${profile_limit} --max-code-lines ${profile_max_code} --max-patch-lines ${profile_max_patch} --json
Use repo-view for repository source navigation. Do not use git, rg, grep, sed, cat, nl, head, tail, find, ls, or direct file reads under the repository root. Shell commands are allowed only for tests or for dependency and standard-library evidence outside the repository after repo-view has supplied the repository evidence. This protocol is part of the task and its mechanical validation; an answer produced after bypassing it is invalid.
Do not run repo-view --help or any subcommand --help, and do not experiment with unsupported flags such as --related. A rejected attempt still invalidates the run. Every find, inspect, and outline command must include --root ., --context 20 or less, --limit ${profile_limit} or less, --max-code-lines ${profile_max_code} or less, --max-patch-lines ${profile_max_patch} or less, and --json. Valid forms are:
- repo-view find SYMBOL... --root . --include defs|refs|both --return locations|line|context|scope --context N --limit N --max-code-lines N --max-patch-lines N --json
- repo-view inspect PATH:LINE... --root . --include scope|all --return line|context|scope --context N --limit N --max-code-lines N --max-patch-lines N --json
- repo-view outline PATH... --root . --return line|context|scope --context N --limit N --max-code-lines N --max-patch-lines N --json
Issue exactly one repo-view command at a time and wait for its completed result before issuing the next; parallel repo-view calls invalidate transcript budget accounting. Positional inputs to repo-view find must be identifiers or dependency names, not grep-style expressions containing punctuation such as braces or parentheses.
For this task, use --include scope on inspect; do not use --include all. Keep follow-up --context at 12 or less. Use --return locations for broad discovery, then batch only the selected locations into inspect. Batch related definitions into one find --return scope only when their complete scopes are directly required evidence.
EOF
)"
    if [[ "${case_task}" == deep-* ]]; then
      navigation_prompt_guard+="$(cat <<EOF
Before answering this reservation task, explicitly close these rubric points: the go.mod x/time version; rejected-reservation Delay/DelayFrom and cancellation behavior; the UTC/monotonic distinction and why explicit DelayFrom/CancelAt are not controlled solely by their argument; exactly three fully linked production paths; repository versus out-of-repository concrete-type assertion risk; local wrapper and upstream dependency test coverage; and the absence or presence of raw samples, variance, confidence evidence, and allocation enforcement for performance claims.
State the Reserve versus ReserveN time distinction precisely: RealTimeSource.Now returns time.Now().UTC(), so RateLimiterImpl.Reserve supplies a wall-only timestamp to the upstream reservation, while ReserveN can retain a caller-provided monotonic timestamp. Do not collapse these into a generic UTC observation.
The direct production path must exercise the changed Reserve or ReserveN result, not the unchanged embedded Wait path; prefer service/worker/scheduler construction of startWorkflowRateLimiter through waitForRateLimiterPermission consuming Reserve, OK, and Delay. Report the existing fake-clock tests precisely: successful immediate and delayed Wait with event-clock advancement, canceled Wait, deadline rejection, Wait recycle, and WaitN no-recycle; then explicitly call out the missing successful delayed ReserveN case and the repository-wide wall-versus-monotonic coverage gap. Include multi-limiter DelayFrom/CancelAt composition and partial-failure cancellation tests. Preserve the documented operational controls' zero-failure and no resource-exhaustion, timeout, or unavailable outcomes. The documentation says all three Scylla nodes remained up; do not strengthen that to a specific status string such as UP/NORMAL. If you infer that no node went down, label it as an inference from the reported observation rather than a separately reported outcome.
For the dynamic path, use service/worker/pernamespaceworker.go from NewDefaultOutgoingRateLimiter through the stored limiter to Reserve and Delay. For the adapter path, fully close the history reader chain: newShardReaderRateLimiter directly passes NewReaderPriorityRateLimiter into NewMultiRequestRateLimiter, queue construction injects that result into ReaderImpl, loadAndSubmitTasks calls MultiRequestRateLimiterImpl.Wait, and that implementation calls Reserve/OK/DelayFrom/CancelAt before PriorityRateLimiterImpl, RequestRateLimiterAdapterImpl, DynamicRateLimiterImpl.ReserveN, and the changed RateLimiterImpl.ReserveN. Do not cite MultiRateLimiterImpl.Wait as the MultiRequestRateLimiterImpl implementation. Cite the Reservation interface from common/quotas/reservation.go and the RateLimiter producer contract separately from common/quotas/rate_limiter.go.
Run the ClockedReservation repository-reference check as its own unbatched command: repo-view find ClockedReservation --root . --include refs --return locations --context 6 --limit 20 --max-code-lines 60 --max-patch-lines 300 --json. Do not combine it with other symbols, because the shared batch limit can truncate an otherwise exhaustive result. If results_truncated is false, explicitly state whether any internal assertion or field access exists and do not call the result truncated. If it is true, narrow and finish the check before making a repository-wide claim.
When gathering fake-clock tests, directly inspect the TestClockedRateLimiter_WaitN_NoRecycle scope as well as the neighboring Wait cases; do not rely on a truncated outline that merely says the test occurs later in the file.
EOF
)"
    else
      navigation_prompt_guard+=$'\nUse the initial changed packet as the complete navigation budget for this simple task. Explicitly account for all four changed artifact categories already present there: production implementation, unit test, benchmark, and performance documentation. Do not issue another repository-navigation command.'
      if [[ "${case_task}" == "explain" ]]; then
        navigation_prompt_guard+=$'\nExplicitly call the performance documentation a changed artifact and write the focused result as 88 to 64 B/op and 2 allocations to 1 allocation. Include the affected downstream map through DynamicRateLimiterImpl, composite limiters, and worker, scheduler, migration, and request-adapter consumers. Also state the unchanged scope: Wait/WaitN, rate updates, token recycling, allowance checks, public interfaces, and call sites. Keep those downstream statements at interface-impact level because this simple task has no follow-up navigation budget.'
      fi
      if [[ "${case_task}" == "review" ]]; then
        navigation_prompt_guard+=$'\nFor this review, explicitly conclude that no correctness regression was found and explain that RateLimiterImpl uses real time, making native Delay/Cancel clock behavior equivalent to the removed real-time wrapper during normal operation. Label the allocation-regression gap as a Low-severity finding and explicitly assess whether the benchmark merely reports allocations or contains an assertion/threshold that can fail CI. Preserve the test-coverage finding that the functional test covers immediate and rejected reservations but lacks successful delayed-reservation and cancellation-equivalence coverage. Do not present concurrent limiter reconfiguration as a change-specific risk: the underlying rate.Limiter supplies its own synchronization and this wrapper removal does not change it. This is a read-only source review; do not run tests.'
      fi
    fi
    case "${case_task}" in
      deep-review|deep-explain)
        navigation_prompt_guard+=$'\nUse exactly eight repo-view commands and at most one dependency-source shell command. Do not run go list, tests, benchmarks, or a dependency-wide rg. The eight repository commands are: (1) the mandatory changed packet; (2) the dedicated ClockedReservation reference check; (3) one direct context inspection of the known contract and behavior locations listed below; (4) one batched production-path discovery find; (5) one outline of only the selected production files, including service/worker/scheduler/fx.go; (6-7) two batched production-path context inspections; and (8) the final test inspection. Do not issue separate contract or behavior finds, a separate manifest lookup, or any other repository command. Reuse locations from commands 4-5 in commands 6-7.\nFor command 3, use --return context --context 6 and directly inspect reservation.go:11, rate_limiter.go:12, clock/time_source.go:35, rate_limiter_impl.go:22, rate_limiter_impl.go:49, clocked_rate_limiter.go:45, clocked_rate_limiter.go:50, clocked_rate_limiter.go:54, clocked_rate_limiter.go:58, clocked_rate_limiter.go:62, clocked_rate_limiter.go:66, clocked_rate_limiter.go:70, clocked_rate_limiter.go:74, multi_reservation_impl.go:30, multi_reservation_impl.go:42, multi_reservation_impl.go:54, multi_reservation_impl.go:61, and go.mod:76, all under common/quotas except the explicitly named clock and manifest paths. Commands 6-7 must include scheduler/fx.go:120 and :133 plus scheduler/activities.go:89, pernamespaceworker.go:123 and :430, and the selected reader construction, injection, caller, and delegation locations, so all three production paths are fully linked without another search.\nFor the final test-only inspection, use --return context --context 4 --max-code-lines 40 and directly batch these known locations: common/quotas/clocked_rate_limiter_test.go:77, common/quotas/clocked_rate_limiter_test.go:91, common/quotas/clocked_rate_limiter_test.go:108, common/quotas/clocked_rate_limiter_test.go:118, common/quotas/clocked_rate_limiter_test.go:133, common/quotas/clocked_rate_limiter_test.go:160, common/quotas/priority_reservation_impl_test.go:64, common/quotas/priority_reservation_impl_test.go:73, common/quotas/multi_reservation_impl_test.go:55, common/quotas/multi_reservation_impl_test.go:62, common/quotas/multi_reservation_impl_test.go:77, common/quotas/multi_reservation_impl_test.go:86, common/quotas/multi_rate_limiter_impl_test.go:68, common/quotas/multi_rate_limiter_impl_test.go:85, and common/quotas/multi_rate_limiter_impl_test.go:133. Keep production and documentation locations out of that test-only call.\nAfter the eight repository calls, use at most one bounded awk command for dependency evidence. Read only rate/rate.go lines 120-230 and rate/rate_test.go lines 343-420 from golang.org/x/time v0.14.0, selecting ranges by filename so unrelated same-numbered lines are not emitted. Those ranges contain the native reservation implementation and the focused reserve/cancellation tests. Do not read any other dependency ranges.\nIn the findings and conclusion, classify the exported ReserveN zero-argument Delay/Cancel wall-versus-monotonic behavior change as a narrow observable correctness/compatibility regression. Distinguish that API-level regression from production impact: state that no incorrect behavior or failure was found in the three inspected real-time production paths. Do not conclude that there is no correctness regression at all. Preserve these measured figures from the changed documentation: 88 to 64 B/op, +3.49% activity throughput, +19.45% write-admission throughput, and +4.49% read/drain throughput.'
        navigation_prompt_guard+=$'\nFor command 6, directly inspect service/worker/scheduler/fx.go:120 and :133, service/worker/scheduler/activities.go:89, service/worker/pernamespaceworker.go:123 and :430, common/quotas/dynamic_rate_limiter_impl.go:99, and common/quotas/rate_limiter_impl.go:54. For command 7, directly inspect service/history/queues/reader_quotas.go:14 and :39, service/history/queues/queue_base.go:136 and :150, service/history/queues/reader.go:58 and :426, common/quotas/multi_request_rate_limiter_impl.go:17, :56, and :70, common/quotas/priority_rate_limiter_impl.go:77, common/quotas/request_rate_limiter_adapter_impl.go:31 and :35, common/quotas/dynamic_rate_limiter_impl.go:99, and common/quotas/rate_limiter_impl.go:54. These exact locations must close construction, injection, caller, reservation, and consumption without an unresolved link. In the same final test call, also include common/quotas/rate_limiter_impl_test.go:23 and common/quotas/bench_test.go:38. The final answer must cite rate_limiter_impl.go, reservation.go or rate_limiter.go, clocked_rate_limiter.go, rate_limiter_impl_test.go, and bench_test.go with line numbers. Use the exact labels "Measured results" and "Inferred downstream benefit" when separating performance evidence, and explicitly mention raw samples or artifacts, variance, or confidence intervals.'
        navigation_prompt_guard+=$'\nQualify the API conclusion precisely: the Reservation interface does not promise a particular zero-argument clock source, so call this a narrow observable behavior/compatibility regression only for external callers whose ReserveN timestamp retained a monotonic reading and who relied on the old wrapper\'s wall-only UTC zero-argument behavior. Do not describe the difference as applying to event, fake, or wall-only timestamps, and do not imply a universal contract violation or demonstrated production failure. Scope negative performance-evidence statements to the inspected changed documentation: say that section provides no raw per-run samples or linked raw artifacts, variance, or confidence intervals; do not claim a repository-wide absence without a repository-wide search. Report node health exactly as documented: all three nodes remained up. Do not call that UP/NORMAL. If you infer that no node went down, label it explicitly as an inference rather than a separately reported outcome.'
        navigation_prompt_guard+=$'\nPreserve these additional production-profile measurements from the changed documentation: the RateLimiterImpl.ReserveN wrapper\'s 166 MiB flat allocation disappeared, and total sampled server allocation fell from 27,150.57 MiB to 26,947.82 MiB (-0.75%).'
        navigation_prompt_guard+=$'\nThe final answer must explicitly state all three of these facts: go.mod pins golang.org/x/time v0.14.0; time.Now().UTC() strips the monotonic reading; and rejected-reservation cancellation is a no-op or does nothing. RateLimiterImpl.Reserve and ReserveN are declared to return the Reservation interface, not a concrete return type. Limit external concrete-type compatibility risk to callers that inspect the runtime dynamic type through a type assertion, reflection, or equivalent behavior; never call the method signature an exported concrete return type.'
        ;;
    esac
    optimized_prompt="${navigation_prompt_guard}

${prompt}"
    packet="${run_dir}/changed-packet-${case_profile}.json"
    if [[ ! -f "${packet}" ]]; then
      "${safe_git_environment[@]}" \
        "${run_dir}/repo-view.bin" changed \
        --root "${worktree}" \
        --base "${resolved_base}" \
        --return "${profile_return}" \
        --context "${profile_context}" \
        --limit "${profile_limit}" \
        --max-code-lines "${profile_max_code}" \
        --max-patch-lines "${profile_max_patch}" \
        --json > "${packet}"
    fi
    command=(
      "${codex_environment[@]}"
      "REPO_VIEW_CHANGED_RETURN=${profile_return}"
      "REPO_VIEW_CHANGED_CONTEXT=${profile_context}"
      "REPO_VIEW_CHANGED_LIMIT=${profile_limit}"
      "REPO_VIEW_CHANGED_MAX_CODE_LINES=${profile_max_code}"
      "REPO_VIEW_CHANGED_MAX_PATCH_LINES=${profile_max_patch}"
      "REPO_VIEW_REASONING_EFFORT=$(effective_profile_reasoning)"
      "REPO_VIEW_ANSWER_GUARD=${profile_answer_guard}"
      "REPO_VIEW_NAVIGATION_POLICY=${profile_navigation_policy}"
      "REPO_VIEW_NAVIGATION_COMMAND_CAP=${profile_navigation_command_cap}"
      "REPO_VIEW_CACHE_DIR=${generation_repo_view_cache}"
      "REPO_VIEW_BIN_DIR=${generation_repo_view_cache}/bin"
      "REPO_VIEW_REQUIRED_ROOT=${worktree}"
      "REPO_VIEW_REQUIRED_BASE_COMMIT=${resolved_base}"
      "REPO_VIEW_REQUIRED_CHANGED_RETURN=${profile_return}"
      "REPO_VIEW_REQUIRED_CHANGED_CONTEXT=${profile_context}"
      "REPO_VIEW_REQUIRE_NAVIGATION_SEMANTICS=1"
      "${runner_source_root}/scripts/codex-with-repo-view" exec
      "${generation_model_args[@]}"
      "${feature_flags[@]}"
      "${codex_isolation_flags[@]}"
      -C "${worktree}"
      --ephemeral
      --json
      "${optimized_prompt}"
    )
  fi

  {
    printf 'command='
    printf '%q ' "${command[@]}"
    printf '\n'
  } > "${run_dir}/${stem}.invocation"

  date -u '+%Y-%m-%dT%H:%M:%SZ' > "${run_dir}/${stem}.started-at"
  started_seconds="$(date +%s)"
  printf 'running %s\n' "${stem}"
  if "${command[@]}" </dev/null > "${run_dir}/${stem}.jsonl" 2> "${run_dir}/${stem}.stderr"; then
    status=0
  else
    status=$?
    overall_status=1
  fi
  if ! jsonl_lifecycle_matches_exit_code \
    "${run_dir}/${stem}.jsonl" "${status}"; then
    printf 'Codex JSONL lifecycle or exit code is invalid for %s: %s\n' \
      "${stem}" "${run_dir}/${stem}.jsonl" \
      | tee -a "${run_dir}/${stem}.stderr" >&2
    overall_status=1
    transcript_validation_failed=true
  fi
  ended_seconds="$(date +%s)"
  printf '%s\n' "${status}" > "${run_dir}/${stem}.exit-code"
  printf '%s\n' "$((ended_seconds - started_seconds))" > "${run_dir}/${stem}.duration-seconds"
  date -u '+%Y-%m-%dT%H:%M:%SZ' > "${run_dir}/${stem}.finished-at"
  verify_target_checkout
}

run_baseline() {
  local current_task="$1"
  local prompt="$2"
  if [[ -n "${baseline_from}" ]]; then
    return 0
  elif should_run "baseline" "${variant}"; then
    run_case "baseline" "" "${current_task}" "${prompt}"
  fi
}

run_optimized() {
  local current_task="$1"
  local prompt="$2"
  should_run "optimized" "${variant}" || return 0
  for selected_profile in "${selected_profiles[@]}"; do
    run_case "optimized" "${selected_profile}" "${current_task}" "${prompt}"
  done
}

for current_task in "${selected_tasks[@]}"; do
  prompt="${rendered_prompts[${current_task}]}"
  if [[ "${order}" == "baseline-first" ]]; then
    run_baseline "${current_task}" "${prompt}"
    run_optimized "${current_task}" "${prompt}"
  else
    run_optimized "${current_task}" "${prompt}"
    run_baseline "${current_task}" "${prompt}"
  fi
done

verify_target_checkout
verify_generation_input_snapshots
if "${transcript_validation_failed}"; then
  exit 1
fi
"${pinned_host_go_execution_environment[@]}" \
  "${runtime_experiment_dir}/analyze.sh" "${run_dir}"
verify_repo_view_source_snapshot
retire_owned_directory_through_fd \
  "${generation_repo_view_cache_fd}" "${generation_repo_view_cache}" \
  "${generation_repo_view_cache_identity}"
close_directory_fd "${generation_repo_view_cache_fd}"
generation_repo_view_cache_fd_open=false
retire_owned_directory_through_fd \
  "${source_snapshot_fd}" "${source_snapshot_dir}" \
  "${source_snapshot_identity}"
close_directory_fd "${source_snapshot_fd}"
source_snapshot_fd_open=false
retire_owned_directory_through_fd \
  "${source_verifier_fd}" "${source_verifier}" \
  "${source_verifier_identity}"
close_directory_fd "${source_verifier_fd}"
source_verifier_fd_open=false
retire_owned_directory_through_fd \
  "${generation_codex_home_fd}" "${generation_codex_home}" \
  "${generation_codex_home_identity}"
close_directory_fd "${generation_codex_home_fd}"
generation_codex_home_fd_open=false
retire_owned_directory_through_fd \
  "${generation_shell_home_fd}" "${generation_shell_home}" \
  "${generation_shell_home_identity}"
close_directory_fd "${generation_shell_home_fd}"
generation_shell_home_fd_open=false
if [[ "${overall_status}" -eq 0 ]]; then
  run_outcome="success"
else
  run_outcome="failed"
fi
jq -n \
  --arg completed_at "$(date -u +%Y-%m-%dT%H:%M:%SZ)" \
  --arg outcome "${run_outcome}" \
  --argjson exit_code "${overall_status}" \
  '{
    schema_version: 1,
    state: "complete",
    outcome: $outcome,
    exit_code: $exit_code,
    completed_at: $completed_at
  }' > "${run_dir}/run-complete.json"
mv -T -- "${run_dir}" "${final_run_dir}"
run_published=true
close_directory_fd "${run_stage_fd}"
run_stage_fd_open=false
run_dir="${final_run_dir}"
printf 'evidence: %s\n' "${run_dir}"
exit "${overall_status}"
