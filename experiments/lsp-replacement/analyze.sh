#!/usr/bin/env bash
set -euo pipefail

export LC_ALL=C
export TZ=UTC

usage() {
  printf 'usage: %s RUN_DIR\n' "$0" >&2
}

if [[ $# -ne 1 ]]; then
  usage
  exit 2
fi

for required_command in awk cmp cp env find go jq mkdir mktemp mv sha256sum sort stat sync; do
  if ! command -v "${required_command}" >/dev/null 2>&1; then
    printf 'missing required command: %s\n' "${required_command}" >&2
    exit 1
  fi
done

experiment_dir="$(cd -P "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd -P "${experiment_dir}/../.." && pwd)"
requested_run_dir="$1"
while [[ "${requested_run_dir}" != "/" && "${requested_run_dir}" == */ ]]; do
  requested_run_dir="${requested_run_dir%/}"
done
if [[ "${requested_run_dir}" != /* && ! -e "${requested_run_dir}" && ! -L "${requested_run_dir}" ]]; then
  requested_run_dir="${experiment_dir}/${requested_run_dir}"
fi
if [[ -L "${requested_run_dir}" ]]; then
  printf 'run directory must not be a symlink: %s\n' "${requested_run_dir}" >&2
  exit 2
fi
if [[ ! -d "${requested_run_dir}" ]]; then
  printf 'run directory does not exist: %s\n' "${requested_run_dir}" >&2
  exit 2
fi
run_dir="$(cd -P "${requested_run_dir}" && pwd)"
run_identity="$(stat -Lc '%d:%i' -- "${run_dir}")"
exec {run_dir_fd}<"${run_dir}"
if [[ "$(stat -Lc '%d:%i' -- "/proc/self/fd/${run_dir_fd}")" != "${run_identity}" ]]; then
  printf 'failed to hold run directory identity: %s\n' "${run_dir}" >&2
  exit 1
fi

analysis_stage=""
analysis_stage_identity=""
analysis_stage_fd=""
lock_dir="${run_dir}/.analysis.lock"
lock_identity=""
lock_fd=""
lock_acquired=false
publication_started=false
publication_committed=false
declare -a installed_outputs=()
declare -a backed_up_outputs=()

rollback_publication() {
  local index name target staged
  if ! verify_run_identity; then
    return
  fi
  for ((index=${#installed_outputs[@]} - 1; index >= 0; index--)); do
    name="${installed_outputs[index]}"
    target="${run_dir}/${name}"
    staged="${analysis_stage}/generated/${name}"
    if [[ -e "${target}" || -L "${target}" ]]; then
      mv -T -- "${target}" "${staged}" >/dev/null 2>&1 || true
    fi
  done
  for ((index=${#backed_up_outputs[@]} - 1; index >= 0; index--)); do
    name="${backed_up_outputs[index]}"
    target="${run_dir}/${name}"
    staged="${analysis_stage}/previous/${name}"
    if [[ ! -e "${target}" && ! -L "${target}" && (-e "${staged}" || -L "${staged}") ]]; then
      mv -T -- "${staged}" "${target}" >/dev/null 2>&1 || true
    fi
  done
}

cleanup() {
  local status=$?
  trap - EXIT
  set +e
  if "${publication_started}" && ! "${publication_committed}"; then
    rollback_publication
  fi
  if [[ -n "${analysis_stage_fd}" &&
    "$(stat -Lc '%d:%i' -- "/proc/self/fd/${analysis_stage_fd}" 2>/dev/null)" == "${analysis_stage_identity}" ]]; then
    find -P "/proc/self/fd/${analysis_stage_fd}/." \
      -depth -mindepth 1 -delete >/dev/null 2>&1 || true
  fi
  if [[ -n "${analysis_stage}" &&
    "${analysis_stage}" == "${run_dir}"/.analysis-stage.* &&
    -d "${analysis_stage}" &&
    ! -L "${analysis_stage}" &&
    "$(stat -Lc '%d:%i' -- "${analysis_stage}")" == "${analysis_stage_identity}" ]]; then
    rmdir -- "${analysis_stage}" >/dev/null 2>&1 || true
  fi
  if "${lock_acquired}" &&
    [[ -d "${lock_dir}" &&
      ! -L "${lock_dir}" &&
      "$(stat -Lc '%d:%i' -- "${lock_dir}")" == "${lock_identity}" &&
      "$(stat -Lc '%d:%i' -- "/proc/self/fd/${lock_fd}" 2>/dev/null)" == "${lock_identity}" ]]; then
    rmdir -- "${lock_dir}" >/dev/null 2>&1 || true
  fi
  exit "${status}"
}
trap cleanup EXIT
trap 'exit 129' HUP
trap 'exit 130' INT
trap 'exit 143' TERM

if ! mkdir -m 0700 -- "${lock_dir}"; then
  printf 'analysis is already running or lock path is unsafe: %s\n' "${lock_dir}" >&2
  exit 1
fi
lock_acquired=true
lock_identity="$(stat -Lc '%d:%i' -- "${lock_dir}")"
exec {lock_fd}<"${lock_dir}"
if [[ "$(stat -Lc '%d:%i' -- "/proc/self/fd/${lock_fd}")" != "${lock_identity}" ]]; then
  printf 'failed to hold analysis lock identity: %s\n' "${lock_dir}" >&2
  exit 1
fi

analysis_stage="$(mktemp -d "${run_dir}/.analysis-stage.XXXXXX")"
chmod 0700 "${analysis_stage}"
analysis_stage_identity="$(stat -Lc '%d:%i' -- "${analysis_stage}")"
exec {analysis_stage_fd}<"${analysis_stage}"
if [[ "$(stat -Lc '%d:%i' -- "/proc/self/fd/${analysis_stage_fd}")" != "${analysis_stage_identity}" ]]; then
  printf 'failed to hold analysis stage identity: %s\n' "${analysis_stage}" >&2
  exit 1
fi
inputs_dir="${analysis_stage}/inputs"
source_dir="${analysis_stage}/analyzer-source"
generated_dir="${analysis_stage}/generated"
mkdir -m 0700 -- \
  "${inputs_dir}" \
  "${source_dir}" \
  "${generated_dir}" \
  "${generated_dir}/answers" \
  "${generated_dir}/call-graphs" \
  "${generated_dir}/commands" \
  "${generated_dir}/tool-stats" \
  "${analysis_stage}/bin" \
  "${analysis_stage}/go-tmp" \
  "${analysis_stage}/previous"

declare -a snapshotted_sources=()
declare -A snapshot_copies=()
declare -A snapshot_states=()
declare -A snapshot_fds=()

file_state() {
  stat -Lc '%d:%i:%s:%y:%z' -- "$1"
}

verify_run_identity() {
  [[ -d "${run_dir}" &&
    ! -L "${run_dir}" &&
    "$(stat -Lc '%d:%i' -- "${run_dir}")" == "${run_identity}" &&
    "$(stat -Lc '%d:%i' -- "/proc/self/fd/${run_dir_fd}")" == "${run_identity}" ]]
}

snapshot_file() {
  local source=$1
  local destination=$2
  local before after source_fd fd_path
  if [[ -L "${source}" || ! -f "${source}" ]]; then
    printf 'analysis input must be a regular non-symlink file: %s\n' "${source}" >&2
    exit 1
  fi
  before="$(file_state "${source}")"
  exec {source_fd}<"${source}"
  fd_path="/proc/self/fd/${source_fd}"
  if [[ ! -f "${fd_path}" ||
    "$(file_state "${fd_path}")" != "${before}" ||
    -L "${source}" ||
    "$(file_state "${source}")" != "${before}" ]]; then
    printf 'analysis input changed before it could be opened safely: %s\n' \
      "${source}" >&2
    exit 1
  fi
  cp -- "${fd_path}" "${destination}"
  after="$(file_state "${source}")"
  if [[ "${before}" != "${after}" ]] ||
    [[ "$(file_state "${fd_path}")" != "${before}" ]] ||
    ! cmp -s -- "${fd_path}" "${destination}"; then
    printf 'analysis input changed while it was snapshotted: %s\n' "${source}" >&2
    exit 1
  fi
  snapshotted_sources+=("${source}")
  snapshot_copies["${source}"]="${destination}"
  snapshot_states["${source}"]="${after}"
  snapshot_fds["${source}"]="${source_fd}"
}

input_names="${analysis_stage}/input-names.txt"
: > "${input_names}"
shopt -s nullglob
for source_input in "${run_dir}"/*.jsonl "${run_dir}"/*.exit-code; do
  input_name="$(basename "${source_input}")"
  if [[ ! "${input_name}" =~ ^[A-Za-z0-9._-]+$ ]]; then
    printf 'unsafe analysis input name: %s\n' "${input_name}" >&2
    exit 1
  fi
  snapshot_file "${source_input}" "${inputs_dir}/${input_name}"
  printf '%s\n' "${input_name}" >> "${input_names}"
done
for metadata_name in \
  manifest.json \
  generation-config.json \
  run-complete.json \
  profiles-snapshot.tsv; do
  if [[ -e "${run_dir}/${metadata_name}" || -L "${run_dir}/${metadata_name}" ]]; then
    snapshot_file \
      "${run_dir}/${metadata_name}" \
      "${inputs_dir}/${metadata_name}"
    printf '%s\n' "${metadata_name}" >> "${input_names}"
  fi
done
for dependency_input_spec in \
  'dependency-source/manifest.json:dependency-source-manifest.json' \
  'dependency-source/target-go.mod:dependency-source-target-go.mod' \
  'dependency-source/target-go.sum:dependency-source-target-go.sum' \
  'dependency-source/golang.org/x/time@v0.14.0/rate/rate.go:dependency-source-rate.go' \
  'dependency-source/golang.org/x/time@v0.14.0/rate/rate_test.go:dependency-source-rate_test.go'; do
  dependency_relative="${dependency_input_spec%%:*}"
  dependency_input_name="${dependency_input_spec##*:}"
  if [[ -e "${run_dir}/${dependency_relative}" ||
    -L "${run_dir}/${dependency_relative}" ]]; then
    snapshot_file \
      "${run_dir}/${dependency_relative}" \
      "${inputs_dir}/${dependency_input_name}"
    printf '%s\n' "${dependency_input_name}" >> "${input_names}"
  fi
done
for source_input in "${run_dir}"/changed-packet*.json; do
  input_name="$(basename "${source_input}")"
  if [[ ! "${input_name}" =~ ^changed-packet(-[a-z0-9][a-z0-9-]*)?\.json$ ]]; then
    printf 'unsafe changed-packet input name: %s\n' "${input_name}" >&2
    exit 1
  fi
  snapshot_file "${source_input}" "${inputs_dir}/${input_name}"
  printf '%s\n' "${input_name}" >> "${input_names}"
done
sort -u -o "${input_names}" "${input_names}"

mkdir -p -m 0700 -- \
  "${source_dir}/cmd/repo-view-run-stats" \
  "${source_dir}/internal/runstats" \
  "${source_dir}/experiments/lsp-replacement"
snapshot_file "${repo_root}/go.mod" "${source_dir}/go.mod"
snapshot_file \
  "${repo_root}/cmd/repo-view-run-stats/main.go" \
  "${source_dir}/cmd/repo-view-run-stats/main.go"
snapshot_file \
  "${repo_root}/internal/runstats/runstats.go" \
  "${source_dir}/internal/runstats/runstats.go"
profiles_provenance="current-evaluator"
profiles_path="experiments/lsp-replacement/profiles.tsv"
if [[ -f "${inputs_dir}/profiles-snapshot.tsv" ]]; then
  cp -- \
    "${inputs_dir}/profiles-snapshot.tsv" \
    "${source_dir}/experiments/lsp-replacement/profiles.tsv"
  profiles_provenance="run-snapshot"
  profiles_path="profiles-snapshot.tsv"
else
  snapshot_file \
    "${experiment_dir}/profiles.tsv" \
    "${source_dir}/experiments/lsp-replacement/profiles.tsv"
fi
snapshot_file \
  "${experiment_dir}/analyze.sh" \
  "${source_dir}/experiments/lsp-replacement/analyze.sh"
profiles_sha256="$(
  sha256sum "${source_dir}/experiments/lsp-replacement/profiles.tsv"
)"
profiles_sha256="${profiles_sha256%% *}"
manifest_profiles_path=""
manifest_profiles_sha256=""
generation_profiles_path=""
generation_profiles_sha256=""
if [[ -f "${inputs_dir}/manifest.json" ]]; then
  if ! jq -e '
    type == "object"
    and (
      has("profiles_snapshot_path")
      == has("profiles_snapshot_sha256")
    )
    and (
      if has("profiles_snapshot_path") then
        .profiles_snapshot_path == "profiles-snapshot.tsv"
        and (
          .profiles_snapshot_sha256
          | type == "string"
          and test("^[0-9a-f]{64}$")
        )
      else
        true
      end
    )
  ' "${inputs_dir}/manifest.json" >/dev/null; then
    printf 'invalid profiles snapshot binding in manifest: %s\n' \
      "${run_dir}/manifest.json" >&2
    exit 1
  fi
  manifest_profiles_path="$(
    jq -r '.profiles_snapshot_path // ""' "${inputs_dir}/manifest.json"
  )"
  manifest_profiles_sha256="$(
    jq -r '.profiles_snapshot_sha256 // ""' "${inputs_dir}/manifest.json"
  )"
fi
if [[ -f "${inputs_dir}/generation-config.json" ]]; then
  if ! jq -e '
    type == "object"
    and (
      has("profiles_snapshot_path")
      == has("profiles_snapshot_sha256")
    )
    and (
      if has("profiles_snapshot_path") then
        .profiles_snapshot_path == "profiles-snapshot.tsv"
        and (
          .profiles_snapshot_sha256
          | type == "string"
          and test("^[0-9a-f]{64}$")
        )
      else
        true
      end
    )
  ' "${inputs_dir}/generation-config.json" >/dev/null; then
    printf 'invalid profiles snapshot binding in generation configuration: %s\n' \
      "${run_dir}/generation-config.json" >&2
    exit 1
  fi
  generation_profiles_path="$(
    jq -r '.profiles_snapshot_path // ""' \
      "${inputs_dir}/generation-config.json"
  )"
  generation_profiles_sha256="$(
    jq -r '.profiles_snapshot_sha256 // ""' \
      "${inputs_dir}/generation-config.json"
  )"
fi
strict_profiles=false
if [[ -f "${inputs_dir}/run-complete.json" ||
  "${profiles_provenance}" == "run-snapshot" ||
  -n "${manifest_profiles_path}" ||
  -n "${generation_profiles_path}" ]]; then
  strict_profiles=true
fi
if "${strict_profiles}"; then
  if [[ "${profiles_provenance}" != "run-snapshot" ||
    "${manifest_profiles_path}" != "profiles-snapshot.tsv" ||
    "${generation_profiles_path}" != "profiles-snapshot.tsv" ||
    "${manifest_profiles_sha256}" != "${generation_profiles_sha256}" ||
    "${manifest_profiles_sha256}" != "${profiles_sha256}" ]]; then
    printf 'strict analysis requires a digest-bound run-local profiles snapshot\n' >&2
    exit 1
  fi
elif [[ "${profiles_provenance}" != "current-evaluator" ]]; then
  printf 'legacy analysis profile provenance is invalid\n' >&2
  exit 1
fi

(
  cd "${source_dir}"
  env -u GOOS -u GOARCH -u CGO_ENABLED \
    GOENV=off \
    GOTOOLCHAIN=local \
    GOWORK=off \
    GOFLAGS='-mod=readonly -trimpath -buildvcs=false' \
    TMPDIR="${analysis_stage}/go-tmp" \
    GOTMPDIR="${analysis_stage}/go-tmp" \
    go build \
      -o "${analysis_stage}/bin/repo-view-run-stats" \
      ./cmd/repo-view-run-stats
)

profiles_file="${source_dir}/experiments/lsp-replacement/profiles.tsv"
cases_file="${analysis_stage}/cases.jsonl"
: > "${cases_file}"
recognized_cases=0
manifest_worktree=""
manifest_base_commit=""
manifest_target_commit=""
manifest_go_mod_cache=""
mechanical_navigation_semantics_enforced=false
simple_core_inspect_command='repo-view inspect common/quotas/rate_limiter.go:12 common/quotas/reservation.go:11 common/quotas/rate_limiter_impl.go:13 common/quotas/rate_limiter_impl.go:26 common/quotas/rate_limiter_impl.go:80 common/quotas/rate_limiter_impl.go:114 common/quotas/dynamic_rate_limiter_impl.go:28 common/quotas/dynamic_rate_limiter_impl.go:57 common/quotas/clocked_rate_limiter.go:54 common/quotas/clocked_rate_limiter.go:62 common/quotas/clocked_rate_limiter.go:70 common/clock/time_source.go:38 --root . --include scope --return scope --context 4 --limit 20 --max-code-lines 60 --max-patch-lines 300 --json'
simple_consumer_inspect_command='repo-view inspect service/worker/scheduler/fx.go:120 service/worker/scheduler/activities.go:95 service/worker/pernamespaceworker.go:123 service/worker/pernamespaceworker.go:430 common/quotas/request_rate_limiter_adapter_impl.go:16 common/quotas/request_rate_limiter_adapter_impl.go:31 service/frontend/configs/quotas.go:346 common/persistence/client/health_request_rate_limiter.go:56 common/persistence/client/health_request_rate_limiter.go:82 service/matching/ratelimit_manager.go:81 service/history/replication/stream_sender_flow_controller.go:59 --root . --include scope --return context --context 4 --limit 20 --max-code-lines 60 --max-patch-lines 300 --json'
deep_reference_find_command='repo-view find ClockedReservation --root . --include refs --return locations --context 6 --limit 20 --max-code-lines 60 --max-patch-lines 300 --json'
deep_contract_inspect_command='repo-view inspect common/quotas/reservation.go:11 common/quotas/rate_limiter.go:12 common/clock/time_source.go:35 common/quotas/rate_limiter_impl.go:22 common/quotas/rate_limiter_impl.go:49 common/quotas/clocked_rate_limiter.go:45 common/quotas/clocked_rate_limiter.go:50 common/quotas/clocked_rate_limiter.go:54 common/quotas/clocked_rate_limiter.go:58 common/quotas/clocked_rate_limiter.go:62 common/quotas/clocked_rate_limiter.go:66 common/quotas/clocked_rate_limiter.go:70 common/quotas/clocked_rate_limiter.go:74 common/quotas/multi_reservation_impl.go:30 common/quotas/multi_reservation_impl.go:42 common/quotas/multi_reservation_impl.go:54 common/quotas/multi_reservation_impl.go:61 go.mod:76 --root . --include scope --return context --context 6 --limit 20 --max-code-lines 60 --max-patch-lines 300 --json'
deep_path_find_command='repo-view find startWorkflowRateLimiter NewDefaultOutgoingRateLimiter newShardReaderRateLimiter ReaderImpl loadAndSubmitTasks MultiRequestRateLimiterImpl --root . --include both --return locations --context 8 --limit 20 --max-code-lines 60 --max-patch-lines 300 --json'
deep_path_outline_command='repo-view outline service/worker/scheduler/fx.go service/worker/scheduler/activities.go service/worker/pernamespaceworker.go common/quotas/dynamic_rate_limiter_impl.go common/quotas/rate_limiter_impl.go service/history/queues/reader_quotas.go service/history/queues/queue_base.go service/history/queues/reader.go common/quotas/multi_request_rate_limiter_impl.go common/quotas/priority_rate_limiter_impl.go common/quotas/request_rate_limiter_adapter_impl.go --root . --return locations --context 8 --limit 20 --max-code-lines 60 --max-patch-lines 300 --json'
deep_worker_inspect_command='repo-view inspect service/worker/scheduler/fx.go:120 service/worker/scheduler/fx.go:133 service/worker/scheduler/activities.go:89 service/worker/pernamespaceworker.go:123 service/worker/pernamespaceworker.go:430 common/quotas/dynamic_rate_limiter_impl.go:99 common/quotas/rate_limiter_impl.go:54 --root . --include scope --return context --context 8 --limit 20 --max-code-lines 60 --max-patch-lines 300 --json'
deep_reader_inspect_command='repo-view inspect service/history/queues/reader_quotas.go:14 service/history/queues/reader_quotas.go:39 service/history/queues/queue_base.go:136 service/history/queues/queue_base.go:150 service/history/queues/reader.go:58 service/history/queues/reader.go:426 common/quotas/multi_request_rate_limiter_impl.go:17 common/quotas/multi_request_rate_limiter_impl.go:56 common/quotas/multi_request_rate_limiter_impl.go:70 common/quotas/priority_rate_limiter_impl.go:77 common/quotas/request_rate_limiter_adapter_impl.go:31 common/quotas/request_rate_limiter_adapter_impl.go:35 common/quotas/dynamic_rate_limiter_impl.go:99 common/quotas/rate_limiter_impl.go:54 --root . --include scope --return context --context 8 --limit 20 --max-code-lines 60 --max-patch-lines 300 --json'
deep_test_inspect_command='repo-view inspect common/quotas/clocked_rate_limiter_test.go:77 common/quotas/clocked_rate_limiter_test.go:91 common/quotas/clocked_rate_limiter_test.go:108 common/quotas/clocked_rate_limiter_test.go:118 common/quotas/clocked_rate_limiter_test.go:133 common/quotas/clocked_rate_limiter_test.go:160 common/quotas/priority_reservation_impl_test.go:64 common/quotas/priority_reservation_impl_test.go:73 common/quotas/multi_reservation_impl_test.go:55 common/quotas/multi_reservation_impl_test.go:62 common/quotas/multi_reservation_impl_test.go:77 common/quotas/multi_reservation_impl_test.go:86 common/quotas/multi_rate_limiter_impl_test.go:68 common/quotas/multi_rate_limiter_impl_test.go:85 common/quotas/multi_rate_limiter_impl_test.go:133 common/quotas/rate_limiter_impl_test.go:23 common/quotas/bench_test.go:38 --root . --include scope --return context --context 4 --limit 20 --max-code-lines 40 --max-patch-lines 300 --json'
optimized_inputs=("${inputs_dir}"/optimized-*.jsonl)
if ((${#optimized_inputs[@]} > 0)); then
  if [[ ! -f "${inputs_dir}/manifest.json" ]]; then
    printf 'optimized analysis requires manifest.json: %s\n' "${run_dir}" >&2
    exit 1
  fi
  if ! jq -e '
    type == "object"
    and (.worktree | type == "string" and startswith("/"))
    and (
      .base_commit
      | type == "string"
      and test("^([0-9a-f]{40}|[0-9a-f]{64})$")
    )
    and (
      .target_commit
      | type == "string"
      and test("^([0-9a-f]{40}|[0-9a-f]{64})$")
    )
    and (
      (has("go_mod_cache") | not)
      or (
        .go_mod_cache
        | type == "string"
        and test("^/[A-Za-z0-9._/-]+$")
      )
    )
    and (
      (has("mechanical_navigation_semantics_enforced") | not)
      or (.mechanical_navigation_semantics_enforced | type == "boolean")
    )
  ' "${inputs_dir}/manifest.json" >/dev/null; then
    printf 'invalid analysis manifest: %s\n' "${run_dir}/manifest.json" >&2
    exit 1
  fi
  manifest_worktree="$(jq -r '.worktree' "${inputs_dir}/manifest.json")"
  manifest_base_commit="$(jq -r '.base_commit' "${inputs_dir}/manifest.json")"
  manifest_target_commit="$(jq -r '.target_commit' "${inputs_dir}/manifest.json")"
  manifest_go_mod_cache="$(
    jq -r '.go_mod_cache // ""' "${inputs_dir}/manifest.json"
  )"
  manifest_mechanical="$(
    jq -r '.mechanical_navigation_semantics_enforced // false' \
      "${inputs_dir}/manifest.json"
  )"
  generation_mechanical=false
  if [[ -f "${inputs_dir}/generation-config.json" ]]; then
    if ! jq -e '
      type == "object"
      and (
        (has("mechanical_navigation_semantics_enforced") | not)
        or (.mechanical_navigation_semantics_enforced | type == "boolean")
      )
    ' "${inputs_dir}/generation-config.json" >/dev/null; then
      printf 'invalid generation configuration: %s\n' \
        "${run_dir}/generation-config.json" >&2
      exit 1
    fi
    generation_mechanical="$(
      jq -r '.mechanical_navigation_semantics_enforced // false' \
        "${inputs_dir}/generation-config.json"
    )"
  fi
  if [[ "${manifest_mechanical}" != "${generation_mechanical}" ]]; then
    printf 'mechanical navigation provenance disagrees between manifest and generation configuration\n' >&2
    exit 1
  fi
  if [[ -f "${inputs_dir}/run-complete.json" &&
    ("${manifest_mechanical}" != "true" ||
      ! -f "${inputs_dir}/generation-config.json") ]]; then
    printf 'completed strict evidence lacks mechanical navigation provenance\n' >&2
    exit 1
  fi
  if [[ "${manifest_mechanical}" == "true" ]]; then
    if ! "${strict_profiles}"; then
      printf 'mechanically enforced evidence lacks run-local profile provenance\n' >&2
      exit 1
    fi
    mechanical_navigation_semantics_enforced=true
  fi
fi
deep_dependency_snapshot_verified=false
deep_dependency_command_root='$HOME/dependencies/golang.org/x/time@v0.14.0'
deep_dependency_awk_command='awk -v OFS=: "((FILENAME == ARGV[1]) && FNR >= 120 && FNR <= 230) || ((FILENAME == ARGV[2]) && FNR >= 343 && FNR <= 420) { print FILENAME, FNR; print }" "$HOME/dependencies/golang.org/x/time@v0.14.0/rate/rate.go" "$HOME/dependencies/golang.org/x/time@v0.14.0/rate/rate_test.go"'
dependency_snapshot_inputs=(
  dependency-source-manifest.json
  dependency-source-target-go.mod
  dependency-source-target-go.sum
  dependency-source-rate.go
  dependency-source-rate_test.go
)
dependency_snapshot_complete=true
for dependency_input_name in "${dependency_snapshot_inputs[@]}"; do
  if [[ ! -f "${inputs_dir}/${dependency_input_name}" ]]; then
    dependency_snapshot_complete=false
  fi
done
if "${dependency_snapshot_complete}" &&
  [[ -f "${inputs_dir}/manifest.json" ]]; then
  dependency_manifest_digest_output="$(
    sha256sum -- "${inputs_dir}/dependency-source-manifest.json"
  )"
  dependency_manifest_digest="${dependency_manifest_digest_output%% *}"
  dependency_target_go_mod_digest_output="$(
    sha256sum -- "${inputs_dir}/dependency-source-target-go.mod"
  )"
  dependency_target_go_mod_digest="${dependency_target_go_mod_digest_output%% *}"
  dependency_target_go_sum_digest_output="$(
    sha256sum -- "${inputs_dir}/dependency-source-target-go.sum"
  )"
  dependency_target_go_sum_digest="${dependency_target_go_sum_digest_output%% *}"
  dependency_rate_go_digest_output="$(
    sha256sum -- "${inputs_dir}/dependency-source-rate.go"
  )"
  dependency_rate_go_digest="${dependency_rate_go_digest_output%% *}"
  dependency_rate_test_go_digest_output="$(
    sha256sum -- "${inputs_dir}/dependency-source-rate_test.go"
  )"
  dependency_rate_test_go_digest="${dependency_rate_test_go_digest_output%% *}"
  if jq -e \
    --arg command_root "${deep_dependency_command_root}" \
    --arg target_go_mod_sha256 "${dependency_target_go_mod_digest}" \
    --arg target_go_sum_sha256 "${dependency_target_go_sum_digest}" \
    --arg rate_go_sha256 "${dependency_rate_go_digest}" \
    --arg rate_test_go_sha256 "${dependency_rate_test_go_digest}" '
      type == "object"
      and .schema_version == 1
      and .module == "golang.org/x/time"
      and .version == "v0.14.0"
      and (.module_sum | type == "string" and test("^h1:[A-Za-z0-9+/]+=$"))
      and (.go_mod_sum | type == "string" and test("^h1:[A-Za-z0-9+/]+=$"))
      and .authentication
        == "fresh-private-gomodcache-go-mod-download-target-go.sum"
      and .command_root == $command_root
      and .source_root == "dependency-source"
      and .files == {
        "target-go.mod": $target_go_mod_sha256,
        "target-go.sum": $target_go_sum_sha256,
        "golang.org/x/time@v0.14.0/rate/rate.go": $rate_go_sha256,
        "golang.org/x/time@v0.14.0/rate/rate_test.go":
          $rate_test_go_sha256
      }
    ' "${inputs_dir}/dependency-source-manifest.json" >/dev/null &&
    jq -e -s \
      --arg manifest_sha256 "${dependency_manifest_digest}" '
        length == 2
        and .[1].dependency_source == {
          manifest_path: "dependency-source/manifest.json",
          manifest_sha256: $manifest_sha256,
          module: "golang.org/x/time",
          version: "v0.14.0",
          sum: .[0].module_sum
        }
      ' \
      "${inputs_dir}/dependency-source-manifest.json" \
      "${inputs_dir}/manifest.json" >/dev/null; then
    deep_dependency_module_sum="$(
      jq -r '.module_sum' "${inputs_dir}/dependency-source-manifest.json"
    )"
    deep_dependency_go_mod_sum="$(
      jq -r '.go_mod_sum' "${inputs_dir}/dependency-source-manifest.json"
    )"
    dependency_go_mod_requirement_count="$(LC_ALL=C awk '
        $1 == "golang.org/x/time" && $2 == "v0.14.0" { matches++ }
        END { print matches + 0 }
      ' "${inputs_dir}/dependency-source-target-go.mod")"
    dependency_module_sum_count="$(LC_ALL=C awk \
      -v expected="golang.org/x/time v0.14.0 ${deep_dependency_module_sum}" '
        $0 == expected { matches++ }
        END { print matches + 0 }
      ' "${inputs_dir}/dependency-source-target-go.sum")"
    dependency_go_mod_sum_count="$(LC_ALL=C awk \
      -v expected="golang.org/x/time v0.14.0/go.mod ${deep_dependency_go_mod_sum}" '
        $0 == expected { matches++ }
        END { print matches + 0 }
      ' "${inputs_dir}/dependency-source-target-go.sum")"
    if [[ "${dependency_go_mod_requirement_count}" == 1 &&
      "${dependency_module_sum_count}" == 1 &&
      "${dependency_go_mod_sum_count}" == 1 ]]; then
      deep_dependency_snapshot_verified=true
    fi
  fi
fi
for packet_input in "${inputs_dir}"/changed-packet*.json; do
  if ! jq -e 'type == "object"' "${packet_input}" >/dev/null; then
    printf 'invalid changed-packet input: %s\n' \
      "${run_dir}/$(basename "${packet_input}")" >&2
    exit 1
  fi
done

for log in "${inputs_dir}"/*.jsonl; do
  stem="$(basename "${log}" .jsonl)"
  task=""
  variant=""
  profile=""
  for known_task in deep-explain deep-review explain review; do
    suffix="-${known_task}"
    if [[ "${stem}" == "baseline-${known_task}" ]]; then
      task="${known_task}"
      variant="baseline"
      profile="baseline"
      break
    fi
    if [[ "${stem}" != *"${suffix}" ]]; then
      continue
    fi
    optimized_prefix="${stem%${suffix}}"
    if [[ "${optimized_prefix}" == "optimized" ]]; then
      task="${known_task}"
      variant="optimized"
      profile="default"
      break
    fi
    if [[ "${optimized_prefix}" =~ ^optimized-([a-z0-9][a-z0-9-]*)$ ]]; then
      task="${known_task}"
      variant="optimized"
      profile="${BASH_REMATCH[1]}"
      break
    fi
  done
  if [[ -z "${task}" ]]; then
    continue
  fi
  recognized_cases=$((recognized_cases + 1))

  if ! jq -se '
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
      [$events[] | select(.type == "turn.completed" or .type == "turn.failed")]
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
  ' "${log}" >/dev/null 2>&1; then
    printf 'invalid transcript lifecycle: %s\n' "${run_dir}/${stem}.jsonl" >&2
    exit 1
  fi

  exit_code_file="${inputs_dir}/${stem}.exit-code"
  if [[ ! -f "${exit_code_file}" || -L "${exit_code_file}" ]]; then
    printf 'missing exit-code evidence: %s\n' "${run_dir}/${stem}.exit-code" >&2
    exit 1
  fi
  exit_code="$(<"${exit_code_file}")"
  if [[ ! "${exit_code}" =~ ^(0|[1-9][0-9]{0,2})$ ]] ||
    ((10#${exit_code} > 255)); then
    printf 'invalid exit-code evidence: %s\n' "${run_dir}/${stem}.exit-code" >&2
    exit 1
  fi
  turn_completed_count="$(
    jq -sr '[.[] | select(.type == "turn.completed")] | length' "${log}"
  )"
  if { ((10#${exit_code} == 0)) && ((turn_completed_count != 1)); } ||
    { ((10#${exit_code} != 0)) && ((turn_completed_count != 0)); }; then
    printf 'exit-code evidence disagrees with transcript completion: %s\n' \
      "${run_dir}/${stem}.exit-code" >&2
    exit 1
  fi

  tool_stats_file="${generated_dir}/tool-stats/${stem}.json"
  call_graph_dot="${generated_dir}/call-graphs/${stem}.dot"
  call_graph_markdown="${generated_dir}/call-graphs/${stem}.md"
  "${analysis_stage}/bin/repo-view-run-stats" \
    --input "${log}" \
    --output "${tool_stats_file}" \
    --dot-output "${call_graph_dot}" \
    --markdown-output "${call_graph_markdown}"

  limit_cap=0
  context_cap=0
  max_code_lines_cap=0
  max_patch_lines_cap=0
  navigation_command_cap=0
  profile_return=""
  profile_changed_context=0
  if [[ "${variant}" == "optimized" && "${profile}" != "default" ]]; then
    profile_matches="$(
      awk -F $'\t' -v requested="${profile}" \
        '!/^#/ && $1 == requested {count++; line=$0} END {
          if (count == 1) print line
          else if (count > 1) exit 3
        }' \
        "${profiles_file}"
    )" || {
      printf 'duplicate profile definition: %s\n' "${profile}" >&2
      exit 1
    }
    if [[ -z "${profile_matches}" ]]; then
      printf 'unknown profile in analysis input: %s\n' "${profile}" >&2
      exit 1
    fi
    IFS=$'\t' read -r _ profile_return profile_context profile_limit profile_max_code \
      profile_max_patch _ _ profile_navigation_policy \
      profile_navigation_command_cap _ <<< "${profile_matches}"
    for profile_integer in \
      "${profile_context}" \
      "${profile_limit}" \
      "${profile_max_code}" \
      "${profile_max_patch}" \
      "${profile_navigation_command_cap}"; do
      if [[ ! "${profile_integer}" =~ ^(0|[1-9][0-9]*)$ ]]; then
        printf 'invalid numeric profile field for %s\n' "${profile}" >&2
        exit 1
      fi
    done
    limit_cap="${profile_limit}"
    context_cap="${profile_context}"
    profile_changed_context="${profile_context}"
    max_code_lines_cap="${profile_max_code}"
    max_patch_lines_cap="${profile_max_patch}"
    navigation_command_cap="${profile_navigation_command_cap}"
    if [[ "${profile_navigation_policy}" == "adaptive" ||
      "${profile_navigation_policy}" == "batched" ]]; then
      context_cap=20
    elif [[ "${profile_navigation_policy}" != "terminal" ]]; then
      printf 'invalid navigation policy for profile %s\n' "${profile}" >&2
      exit 1
    fi
  fi
  simple_changed_command=""
  deep_changed_command=""
  if [[ "${variant}" == "optimized" ]] &&
    [[ "${task}" == "explain" || "${task}" == "review" ]]; then
    simple_changed_command="repo-view changed --root . --base ${manifest_base_commit} --return ${profile_return} --context ${profile_changed_context} --limit ${limit_cap} --max-code-lines ${max_code_lines_cap} --max-patch-lines ${max_patch_lines_cap} --json"
  fi
  if [[ "${variant}" == "optimized" ]] &&
    [[ "${task}" == "deep-explain" || "${task}" == "deep-review" ]] &&
    [[ "${profile}" == "investigative-verified-high" ]]; then
    deep_changed_command="repo-view changed --root . --base ${manifest_base_commit} --return ${profile_return} --context ${profile_changed_context} --limit ${limit_cap} --max-code-lines ${max_code_lines_cap} --max-patch-lines ${max_patch_lines_cap} --json"
  fi

  jq -s \
    --arg name "${stem}" \
    --arg task "${task}" \
    --arg variant "${variant}" \
    --arg profile "${profile}" \
    --arg answer_file "answers/${stem}.md" \
    --arg commands_file "commands/${stem}.txt" \
    --arg tool_stats_file "tool-stats/${stem}.json" \
    --arg call_graph_dot_file "call-graphs/${stem}.dot" \
    --arg call_graph_markdown_file "call-graphs/${stem}.md" \
    --slurpfile tool_stats "${tool_stats_file}" \
    --argjson exit_code "${exit_code}" \
    --argjson limit_cap "${limit_cap}" \
    --argjson context_cap "${context_cap}" \
    --argjson max_code_lines_cap "${max_code_lines_cap}" \
    --argjson max_patch_lines_cap "${max_patch_lines_cap}" \
    --argjson navigation_command_cap "${navigation_command_cap}" \
    --arg profile_return "${profile_return}" \
    --argjson profile_changed_context "${profile_changed_context}" \
    --arg manifest_worktree "${manifest_worktree}" \
    --arg manifest_base_commit "${manifest_base_commit}" \
    --arg manifest_target_commit "${manifest_target_commit}" \
    --arg simple_changed_command "${simple_changed_command}" \
    --arg simple_core_inspect_command "${simple_core_inspect_command}" \
    --arg simple_consumer_inspect_command "${simple_consumer_inspect_command}" \
    --arg deep_changed_command "${deep_changed_command}" \
    --arg deep_reference_find_command "${deep_reference_find_command}" \
    --arg deep_contract_inspect_command "${deep_contract_inspect_command}" \
    --arg deep_path_find_command "${deep_path_find_command}" \
    --arg deep_path_outline_command "${deep_path_outline_command}" \
    --arg deep_worker_inspect_command "${deep_worker_inspect_command}" \
    --arg deep_reader_inspect_command "${deep_reader_inspect_command}" \
    --arg deep_test_inspect_command "${deep_test_inspect_command}" \
    --arg deep_dependency_awk_command "${deep_dependency_awk_command}" \
    --argjson deep_dependency_snapshot_verified \
      "${deep_dependency_snapshot_verified}" \
    --argjson mechanical_navigation_semantics_enforced \
      "${mechanical_navigation_semantics_enforced}" \
    '
      def nonnegative_integer:
        type == "number" and isfinite and floor == . and . >= 0;
      def option_exceeds($command; $option; $cap):
        if $cap <= 0 then false
        else (
          [
            $command
            | scan("--" + $option + "(?:=|\\s+)([0-9]+)")
            | .[0]
            | tonumber
          ]
          | any(
              . > $cap
              or (. == 0 and $option == "limit")
              or (. == 0 and $option == "max-code-lines" and 80 > $cap)
              or (. == 0 and $option == "max-patch-lines" and 400 > $cap)
            )
        )
        end;
      def repo_view_invocations($command; $subcommand):
        [
          $command
          | scan(
              "repo-view(?:\\.bin)?\\s+"
              + $subcommand
              + "(?:\\s|$)"
            )
        ] | length;
      def all_repo_view_invocations($command):
        repo_view_invocations(
          $command;
          "(?:changed|find|inspect|outline)"
        );
      def repo_view_command_payload($command):
        def normalize_binary:
          if startswith("repo-view ") then
            .
          elif startswith("repo-view.bin ") then
            "repo-view " + ltrimstr("repo-view.bin ")
          else
            null
          end;
        if (
          ($command | startswith("repo-view "))
          or ($command | startswith("repo-view.bin "))
        ) then
          ($command | normalize_binary)
        else
          (
            [
              "/usr/bin/zsh -lc ",
              "/bin/zsh -lc ",
              "/usr/bin/bash -lc ",
              "/bin/bash -lc ",
              "/bin/sh -lc ",
              "zsh -lc ",
              "bash -lc ",
              "sh -lc "
            ]
            | map(. as $candidate_prefix | select(
                $command | startswith($candidate_prefix)
              ))
            | first // null
          ) as $prefix
          | if $prefix == null then
              null
            else
              ($command | ltrimstr($prefix)) as $quoted
              | if (
                  ($quoted | length) >= 2
                  and (
                    (
                      ($quoted | .[0:1]) == "\u0027"
                      and ($quoted | endswith("\u0027"))
                    )
                    or (
                      ($quoted | .[0:1]) == "\u0022"
                      and ($quoted | endswith("\u0022"))
                    )
                  )
                ) then
                  ($quoted | .[1:-1] | normalize_binary)
                else
                  null
                end
            end
        end;
      def shell_command_payload($command):
        if (
          ($command | startswith("repo-view "))
          or ($command | startswith("repo-view.bin "))
          or ($command | startswith("awk "))
        ) then
          $command
        else
          (
            [
              "/usr/bin/zsh -lc ",
              "/bin/zsh -lc ",
              "/usr/bin/bash -lc ",
              "/bin/bash -lc ",
              "/bin/sh -lc ",
              "zsh -lc ",
              "bash -lc ",
              "sh -lc "
            ]
            | map(. as $candidate_prefix | select(
                $command | startswith($candidate_prefix)
              ))
            | first // null
          ) as $prefix
          | if $prefix == null then
              null
            else
              ($command | ltrimstr($prefix)) as $quoted
              | if (
                  ($quoted | length) >= 2
                  and (
                    (
                      ($quoted | .[0:1]) == "\u0027"
                      and ($quoted | endswith("\u0027"))
                    )
                    or (
                      ($quoted | .[0:1]) == "\u0022"
                      and ($quoted | endswith("\u0022"))
                    )
                  )
                ) then
                  ($quoted | .[1:-1])
                else
                  null
                end
            end
        end;
      def inspect_output_untruncated($execution; $expected_locations):
        (
          if (($execution.aggregated_output // null) | type) == "string" then
            try ($execution.aggregated_output | fromjson) catch null
          else
            null
          end
        ) as $output
        | ($output | type) == "array"
          and ($output | length) == $expected_locations
          and all(
            $output[];
            type == "object"
            and .results_truncated == false
            and (.results | type) == "array"
            and (.results | length) > 0
            and all(
              .results[];
              type == "object" and .code_truncated == false
            )
          );
      def option_occurrences($command; $option):
        [
          $command
          | scan(
              "(?:^|[\\t ])--"
              + $option
              + "(?:=|[\\t ])"
            )
        ] | length;
      def option_values($command; $option):
        [
          $command
          | scan(
              "(?:^|[\\t ])--"
              + $option
              + "(?:=|[\\t ]+)([^\\t \u0027\u0022;|&<>]+)"
            )
          | .[0]
        ];
      def flag_occurrences($command; $option):
        [
          $command
          | scan(
              "(?:^|[\\t ])--"
              + $option
              + "(?:[\\t \u0027\u0022]|$)"
            )
        ] | length;
      def bounded_numeric_option(
        $command;
        $option;
        $minimum;
        $cap
      ):
        (option_values($command; $option)) as $values
        | option_occurrences($command; $option) == 1
          and ($values | length) == 1
          and ($values[0] | test("^(0|[1-9][0-9]*)$"))
          and ($values[0] | tonumber) >= $minimum
          and ($values[0] | tonumber) <= $cap;
      def exact_numeric_option($command; $option; $expected):
        (option_values($command; $option)) as $values
        | option_occurrences($command; $option) == 1
          and ($values | length) == 1
          and ($values[0] | test("^(0|[1-9][0-9]*)$"))
          and ($values[0] | tonumber) == $expected;
      def return_value($command):
        (option_values($command; "return")) as $values
        | if (
            option_occurrences($command; "return") == 1
            and ($values | length) == 1
            and (
              $values[0] == "locations"
              or $values[0] == "line"
              or $values[0] == "context"
              or $values[0] == "scope"
            )
          ) then
            $values[0]
          else
            null
          end;
      def common_navigation_options_valid($command):
        (return_value($command)) as $return
        | (option_values($command; "root")) as $roots
        | $profile_return != ""
          and flag_occurrences($command; "json") == 1
          and option_occurrences($command; "root") == 1
          and ($roots | length) == 1
          and $roots[0] == "."
          and $return != null
          and bounded_numeric_option(
            $command;
            "limit";
            1;
            $limit_cap
          )
          and bounded_numeric_option(
            $command;
            "context";
            (if $return == "locations" then 0 else 1 end);
            $context_cap
          )
          and bounded_numeric_option(
            $command;
            "max-code-lines";
            1;
            $max_code_lines_cap
          )
          and bounded_numeric_option(
            $command;
            "max-patch-lines";
            1;
            $max_patch_lines_cap
          );
      def changed_navigation_options_valid($command):
        common_navigation_options_valid($command)
        and option_occurrences($command; "base") == 1
        and return_value($command) == $profile_return
        and exact_numeric_option(
          $command;
          "context";
          $profile_changed_context
        )
        and exact_numeric_option($command; "limit"; $limit_cap)
        and exact_numeric_option(
          $command;
          "max-code-lines";
          $max_code_lines_cap
        )
        and exact_numeric_option(
          $command;
          "max-patch-lines";
          $max_patch_lines_cap
        );
      def changed_output_valid($execution):
        (
          if (($execution.aggregated_output // null) | type) == "string" then
            try ($execution.aggregated_output | fromjson) catch null
          else
            null
          end
        ) as $output
        | ($output | type) == "object"
          and $output.root == $manifest_worktree
          and $output.base_commit == $manifest_base_commit
          and $output.head_commit == $manifest_target_commit;
      def valid_budget:
        type == "object"
        and (keys | sort) == ["limit", "remaining", "used"]
        and (.used | nonnegative_integer)
        and (.limit | nonnegative_integer)
        and (.remaining | nonnegative_integer)
        and .used <= .limit
        and .remaining == (.limit - .used);
      def budget_record($output):
        (
          if ($output | type) == "string" then
            try ($output | fromjson) catch null
          else
            null
          end
        ) as $parsed
        | if ($parsed | type) == "object" then
            if (
              ($parsed | has("navigation_budget"))
              and ($parsed.navigation_budget | valid_budget)
            ) then
              $parsed.navigation_budget
            else
              null
            end
          elif ($parsed | type) == "array" and ($parsed | length) > 0 then
            if all(
              $parsed[];
              type == "object"
              and has("navigation_budget")
              and (.navigation_budget | valid_budget)
            ) then
              (
                [$parsed[].navigation_budget]
                | unique
                | if length == 1 then .[0] else null end
              )
            else
              null
            end
          else
            null
          end;
      ($tool_stats[0]) as $tools
      | ([.[] | select(.type == "turn.completed")] | first) as $turn
      | ($turn.usage // null) as $usage
      | ([
          .[]
          | select(.type == "item.completed" and .item.type == "command_execution")
          | select(all_repo_view_invocations(.item.command // "") > 0)
          | .item
        ]) as $repo_view_executions
      | ([
          .[]
          | select(.type == "item.started" and .item.type == "command_execution")
          | select(all_repo_view_invocations(.item.command // "") > 0)
          | .item.command
        ]) as $repo_view_started_commands
      | (
          $variant == "optimized"
          and ($task == "explain" or $task == "review")
        ) as $optimized_simple
      | (
          $variant == "optimized"
          and ($task == "deep-explain" or $task == "deep-review")
          and $profile == "investigative-verified-high"
        ) as $optimized_verified_deep
      | (
          $repo_view_started_commands
          | map(repo_view_command_payload(.))
        ) as $repo_view_started_payloads
      | (
          $repo_view_executions
          | map(repo_view_command_payload(.command))
        ) as $repo_view_completed_payloads
      | (
          if ($optimized_simple | not) then
            true
          else
            $simple_changed_command != ""
            and ($repo_view_started_payloads | length) == 3
            and ($repo_view_completed_payloads | length) == 3
            and $repo_view_started_payloads[0] == $simple_changed_command
            and $repo_view_completed_payloads[0] == $simple_changed_command
          end
        ) as $simple_changed_command_exact
      | (
          if ($optimized_simple | not) then
            true
          else
            ($repo_view_started_payloads | length) == 3
            and ($repo_view_completed_payloads | length) == 3
            and $repo_view_started_payloads[1] == $simple_core_inspect_command
            and $repo_view_completed_payloads[1] == $simple_core_inspect_command
          end
        ) as $simple_core_inspect_command_exact
      | (
          if ($optimized_simple | not) then
            true
          else
            ($repo_view_started_payloads | length) == 3
            and ($repo_view_completed_payloads | length) == 3
            and $repo_view_started_payloads[2]
              == $simple_consumer_inspect_command
            and $repo_view_completed_payloads[2]
              == $simple_consumer_inspect_command
          end
        ) as $simple_consumer_inspect_command_exact
      | (
          if ($optimized_simple | not) then
            true
          else
            ($repo_view_executions | length) >= 3
            and inspect_output_untruncated($repo_view_executions[1]; 12)
            and inspect_output_untruncated($repo_view_executions[2]; 11)
          end
        ) as $simple_inspect_outputs_untruncated
      | (
          if ($optimized_verified_deep | not) then
            true
          else
            ($repo_view_started_payloads | length) == 8
            and ($repo_view_completed_payloads | length) == 8
            and $repo_view_started_payloads == [
              $deep_changed_command,
              $deep_reference_find_command,
              $deep_contract_inspect_command,
              $deep_path_find_command,
              $deep_path_outline_command,
              $deep_worker_inspect_command,
              $deep_reader_inspect_command,
              $deep_test_inspect_command
            ]
            and $repo_view_completed_payloads == $repo_view_started_payloads
          end
        ) as $deep_command_sequence_exact
      | (
          if ($optimized_verified_deep | not) then
            true
          else
            $deep_dependency_snapshot_verified
            and $tools.total_tool_calls == 9
            and $tools.command_execution_tool_calls == 9
            and $tools.repo_view_tool_calls == 8
            and $tools.other_tool_calls == 1
            and ($tools.calls | length) == 9
            and (
              $tools.calls[8]
              | .index == 9
              and .tool_type == "command_execution"
              and .primary_operation == "awk"
              and .operations == ["awk"]
              and .status == "completed"
              and .exit_code == 0
              and (
                shell_command_payload(.command)
                == $deep_dependency_awk_command
              )
            )
          end
        ) as $deep_dependency_awk_exact
      | ([
          $repo_view_executions[]
          | select(repo_view_invocations(.command; "changed") > 0)
        ]) as $changed_executions
      | (
          ($repo_view_started_commands | length) > 0
          and (
            repo_view_invocations(
              $repo_view_started_commands[0];
              "changed"
            ) == 1
          )
        ) as $first_invocation_changed
      | ([
          $repo_view_executions[]
          | .command as $command
          | select(
              if repo_view_invocations($command; "changed") > 0 then
                (changed_navigation_options_valid($command) | not)
              else
                (common_navigation_options_valid($command) | not)
              end
            )
          | $command
        ] | unique) as $navigation_option_violations
      | (
          ($changed_executions | length) == 1
          and changed_output_valid($changed_executions[0])
        ) as $changed_output_semantics_valid
      | ([
          .[]
          | select(.type == "item.completed" and .item.type == "command_execution")
          | (.item.command // "")
          | select(
              test(
                "REPO_VIEW_NAVIGATION_(?:BUDGET_FILE|COMMAND_CAP)"
                + "|env\\s+-u\\s+REPO_VIEW_"
                + "|unset\\s+REPO_VIEW_"
              )
            )
        ] + ($tools.repo_view_command_shape_violations // []) | unique) as $budget_tamper_commands
      | (
          if $variant == "optimized" then
            $navigation_option_violations
          else
            [
              $repo_view_executions[]
              | .command
              | select(
                  option_exceeds(.; "limit"; $limit_cap)
                  or option_exceeds(.; "context"; $context_cap)
                  or option_exceeds(.; "max-code-lines"; $max_code_lines_cap)
                  or option_exceeds(.; "max-patch-lines"; $max_patch_lines_cap)
                )
            ]
          end
        ) as $bound_violations
      | ([
          $repo_view_executions[]
          | all_repo_view_invocations(.command)
        ] | add // 0) as $repo_view_invocation_count
      | ([
          $repo_view_executions[]
          | budget_record(.aggregated_output // "")
          | select(. != null)
        ]) as $budget_records
      | (
          $variant != "optimized"
          or (
            $first_invocation_changed
            and ($changed_executions | length) == 1
            and ($navigation_option_violations | length) == 0
            and $changed_output_semantics_valid
            and $deep_command_sequence_exact
            and $deep_dependency_awk_exact
          )
        ) as $navigation_semantics_valid
      | {
          name: $name,
          task: $task,
          variant: $variant,
          profile: $profile,
          exit_code: $exit_code,
          completed: ($usage != null and $exit_code == 0),
          input_tokens: ($usage.input_tokens // null),
          regular_input_tokens: (
            if $usage == null then null
            else ($usage.input_tokens - $usage.cached_input_tokens)
            end
          ),
          cached_input_tokens: ($usage.cached_input_tokens // null),
          cached_input_equivalent_tokens: (
            if $usage == null then null
            else ($usage.cached_input_tokens * 0.1)
            end
          ),
          cached_input_percent: (
            if $usage == null or $usage.input_tokens == 0 then null
            else (($usage.cached_input_tokens / $usage.input_tokens) * 100)
            end
          ),
          output_tokens: ($usage.output_tokens // null),
          reasoning_output_tokens: (
            if $usage == null then null
            else ($usage.reasoning_output_tokens // 0)
            end
          ),
          raw_total_tokens: (
            if $usage == null then null
            else ($usage.input_tokens + $usage.output_tokens)
            end
          ),
          effective_tokens: (
            if $usage == null then null
            else (($usage.input_tokens - $usage.cached_input_tokens)
              + ($usage.cached_input_tokens * 0.1)
              + $usage.output_tokens)
            end
          ),
          tool_call_count: $tools.total_tool_calls,
          command_execution_tool_call_count: $tools.command_execution_tool_calls,
          other_tool_call_count: $tools.other_tool_calls,
          repo_view_invocation_count: $tools.repo_view_invocations,
          repo_view_tool_call_count: $tools.repo_view_tool_calls,
          tool_call_accounting_valid: (
            $tools.total_tool_calls
            == ($tools.repo_view_tool_calls + $tools.other_tool_calls)
          ),
          repo_view_invocation_accounting_valid: (
            $repo_view_invocation_count == $tools.repo_view_invocations
          ),
          repo_view_tool_call_accounting_valid: (
            ($repo_view_executions | length) == $tools.repo_view_tool_calls
          ),
          repo_view_command_shape_valid: $tools.repo_view_command_shape_valid,
          repo_view_first_invocation_changed: $first_invocation_changed,
          repo_view_navigation_semantics_valid: $navigation_semantics_valid,
          repo_view_simple_changed_command_exact:
            $simple_changed_command_exact,
          repo_view_simple_core_inspect_command_exact:
            $simple_core_inspect_command_exact,
          repo_view_simple_consumer_inspect_command_exact:
            $simple_consumer_inspect_command_exact,
          repo_view_simple_inspect_outputs_untruncated:
            $simple_inspect_outputs_untruncated,
          repo_view_deep_command_sequence_exact:
            $deep_command_sequence_exact,
          repo_view_deep_dependency_awk_exact:
            $deep_dependency_awk_exact,
          mechanical_navigation_semantics_enforced: (
            $variant == "optimized"
            and $mechanical_navigation_semantics_enforced
          ),
          repo_view_navigation_semantic_violation_commands: (
            (
              $navigation_option_violations
              + (
                  if (
                    $variant == "optimized"
                    and (
                      ($first_invocation_changed | not)
                      or ($changed_executions | length) != 1
                      or ($changed_output_semantics_valid | not)
                    )
                  ) then
                    [$repo_view_started_commands[], $changed_executions[].command]
                  else
                    []
                  end
                )
            )
            | unique
          ),
          tool_types: $tools.tool_types,
          operations: $tools.operations,
          temporal_tool_edge_count: $tools.temporal_edge_count,
          output_reference_edge_count: $tools.output_reference_edge_count,
          repo_view_invocation_cap: $navigation_command_cap,
          repo_view_invocation_cap_exceeded: (
            $navigation_command_cap > 0
            and $repo_view_invocation_count > $navigation_command_cap
          ),
          repo_view_tool_output_characters: ([
            $repo_view_executions[]
            | ((.aggregated_output // "") | length)
          ] | add // 0),
          repo_view_budget_observed_used: ([
            $budget_records[].used
          ] | max // 0),
          repo_view_budget_accounting_valid: (
            $navigation_command_cap == 0
            or (
              $tools.repo_view_command_shape_valid
              and ($budget_records | length) == $repo_view_invocation_count
              and (
                [
                  range(0; $repo_view_invocation_count) as $index
                  | $budget_records[$index].used == ($index + 1)
                    and $budget_records[$index].limit == $navigation_command_cap
                    and $budget_records[$index].remaining
                      == ($navigation_command_cap - $index - 1)
                ]
                | all
              )
            )
          ),
          repo_view_budget_tamper_command_count: ($budget_tamper_commands | length),
          repo_view_budget_tamper_commands: $budget_tamper_commands,
          repo_view_bounds: {
            limit: $limit_cap,
            context: $context_cap,
            max_code_lines: $max_code_lines_cap,
            max_patch_lines: $max_patch_lines_cap
          },
          repo_view_bound_violation_count: ($bound_violations | length),
          repo_view_bound_violation_commands: $bound_violations,
          repo_view_changed_invocation_count: ([
            $repo_view_executions[]
            | repo_view_invocations(.command; "changed")
          ] | add // 0),
          repo_view_find_invocation_count: ([
            $repo_view_executions[]
            | repo_view_invocations(.command; "find")
          ] | add // 0),
          repo_view_inspect_invocation_count: ([
            $repo_view_executions[]
            | repo_view_invocations(.command; "inspect")
          ] | add // 0),
          repo_view_outline_invocation_count: ([
            $repo_view_executions[]
            | repo_view_invocations(.command; "outline")
          ] | add // 0),
          tool_output_characters: ([
            .[]
            | select(.type == "item.completed" and .item.type == "command_execution")
            | ((.item.aggregated_output // "") | length)
          ] | add // 0),
          answer_file: $answer_file,
          commands_file: $commands_file,
          tool_stats_file: $tool_stats_file,
          call_graph_dot_file: $call_graph_dot_file,
          call_graph_markdown_file: $call_graph_markdown_file
        }
    ' "${log}" >> "${cases_file}"

  jq -s -r \
    '[.[] | select(.type == "item.completed" and .item.type == "agent_message") | .item.text] | last // ""' \
    "${log}" > "${generated_dir}/answers/${stem}.md"
  jq -r \
    'select(.type == "item.completed" and .item.type == "command_execution") | .item.command' \
    "${log}" > "${generated_dir}/commands/${stem}.txt"
done

if ((recognized_cases == 0)) || [[ ! -s "${cases_file}" ]]; then
  printf 'no baseline/optimized JSONL files found in %s\n' "${run_dir}" >&2
  exit 1
fi

jq -s \
  --arg profiles_source "${profiles_provenance}" \
  --arg profiles_path "${profiles_path}" \
  --arg profiles_sha256 "${profiles_sha256}" \
  '
  . as $cases
  | {
      schema_version: 2,
      formula: "effective = (input - cached_input) + 0.1 * cached_input + output",
      analysis_provenance: {
        profiles_source: $profiles_source,
        profiles_path: $profiles_path,
        profiles_sha256: $profiles_sha256
      },
      cases: $cases,
      comparisons: (
        [
          $cases[]
          | select(.variant == "optimized" and .completed)
          | . as $optimized
          | ([
              $cases[]
              | select(.task == $optimized.task and .variant == "baseline" and .completed)
            ] | first) as $baseline
          | select($baseline != null)
          | {
              task: $optimized.task,
              profile: $optimized.profile,
              baseline_effective_tokens: $baseline.effective_tokens,
              optimized_effective_tokens: $optimized.effective_tokens,
              effective_reduction_percent: (
                if $baseline.effective_tokens == 0 then null
                else (1 - ($optimized.effective_tokens / $baseline.effective_tokens)) * 100
                end
              ),
              baseline_raw_total_tokens: $baseline.raw_total_tokens,
              optimized_raw_total_tokens: $optimized.raw_total_tokens,
              raw_reduction_percent: (
                if $baseline.raw_total_tokens == 0 then null
                else (1 - ($optimized.raw_total_tokens / $baseline.raw_total_tokens)) * 100
                end
              ),
              baseline_regular_input_tokens: $baseline.regular_input_tokens,
              optimized_regular_input_tokens: $optimized.regular_input_tokens,
              regular_input_reduction_percent: (
                if $baseline.regular_input_tokens == 0 then null
                else (1 - ($optimized.regular_input_tokens / $baseline.regular_input_tokens)) * 100
                end
              ),
              baseline_cached_input_tokens: $baseline.cached_input_tokens,
              optimized_cached_input_tokens: $optimized.cached_input_tokens,
              baseline_cached_input_percent: $baseline.cached_input_percent,
              optimized_cached_input_percent: $optimized.cached_input_percent,
              cached_input_percent_delta: (
                if (
                  $baseline.cached_input_percent == null
                  or $optimized.cached_input_percent == null
                ) then null
                else (
                  $optimized.cached_input_percent
                  - $baseline.cached_input_percent
                )
                end
              ),
              baseline_output_tokens: $baseline.output_tokens,
              optimized_output_tokens: $optimized.output_tokens,
              baseline_tool_calls: $baseline.tool_call_count,
              optimized_tool_calls: $optimized.tool_call_count,
              baseline_other_tool_calls: $baseline.other_tool_call_count,
              optimized_other_tool_calls: $optimized.other_tool_call_count,
              baseline_repo_view_tool_calls: $baseline.repo_view_tool_call_count,
              optimized_repo_view_tool_calls: $optimized.repo_view_tool_call_count,
              baseline_repo_view_invocations: $baseline.repo_view_invocation_count,
              optimized_repo_view_invocations: $optimized.repo_view_invocation_count,
              optimized_repo_view_bound_violations: $optimized.repo_view_bound_violation_count,
              optimized_repo_view_invocation_cap: $optimized.repo_view_invocation_cap,
              optimized_repo_view_invocation_cap_exceeded: $optimized.repo_view_invocation_cap_exceeded,
              optimized_repo_view_budget_tamper_commands: $optimized.repo_view_budget_tamper_command_count
            }
        ]
      )
    }
' "${cases_file}" > "${generated_dir}/metrics.json"

{
  printf '# LSP-Replacement Run\n\n'
  if [[ -f "${inputs_dir}/manifest.json" ]]; then
    jq -r '"- Run: `" + .run_id + "`\n- Created: `" + .created_at + "`\n- Target: `" + .target_commit + "`\n- Base: `" + .base_ref + "` (`" + .base_commit + "`)"' \
      "${inputs_dir}/manifest.json"
    printf '\n'
  fi
  printf '%s\n\n' 'Effective cost: `(input - cached_input) + 0.1 * cached_input + output`'
  printf '| Task | Profile | Input | Regular input | Cached input | Cached @0.1 | Output | Reasoning | Raw total | Effective | Tool calls | repo-view tool calls | Other tool calls | repo-view invocations | cap | observed budget | tamper | changed | find | inspect | outline | bound violations |\n'
  printf '| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |\n'
  jq -r '
    .cases[]
    | [
        .task,
        .profile,
        (.input_tokens // 0),
        (.regular_input_tokens // 0),
        (.cached_input_tokens // 0),
        ((.cached_input_equivalent_tokens // 0) | tostring),
        (.output_tokens // 0),
        (.reasoning_output_tokens // 0),
        (.raw_total_tokens // 0),
        ((.effective_tokens // 0) | tostring),
        .tool_call_count,
        .repo_view_tool_call_count,
        .other_tool_call_count,
        .repo_view_invocation_count,
        (if .repo_view_invocation_cap > 0 then .repo_view_invocation_cap else "n/a" end),
        .repo_view_budget_observed_used,
        .repo_view_budget_tamper_command_count,
        .repo_view_changed_invocation_count,
        .repo_view_find_invocation_count,
        .repo_view_inspect_invocation_count,
        .repo_view_outline_invocation_count,
        .repo_view_bound_violation_count
      ]
    | @tsv
  ' "${generated_dir}/metrics.json" |
    while IFS=$'\t' read -r task profile input regular cached cached_cost output reasoning raw_total effective tool_calls repo_view_tool_calls other_tool_calls repo_view_invocations command_cap observed_budget tamper changed find inspect outline bound_violations; do
      printf '| %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s | %s |\n' \
        "${task}" "${profile}" "${input}" "${regular}" "${cached}" "${cached_cost}" \
        "${output}" "${reasoning}" "${raw_total}" "${effective}" "${tool_calls}" \
        "${repo_view_tool_calls}" "${other_tool_calls}" "${repo_view_invocations}" \
        "${command_cap}" "${observed_budget}" "${tamper}" "${changed}" "${find}" \
        "${inspect}" "${outline}" "${bound_violations}"
    done
  printf '\n## Comparisons\n\n'
  jq -r '
    .comparisons[]
    | "- " + (.task | ascii_upcase) + " / " + .profile
      + ": " + (.effective_reduction_percent | tostring)
      + "% effective-token reduction; "
      + (.raw_reduction_percent | tostring)
      + "% raw-token reduction; cached-input rate "
      + (.baseline_cached_input_percent | tostring) + "% -> "
      + (.optimized_cached_input_percent | tostring) + "% ("
      + (
          if .cached_input_percent_delta == null then "n/a"
          elif .cached_input_percent_delta >= 0 then
            "+" + (.cached_input_percent_delta | tostring)
          else (.cached_input_percent_delta | tostring)
          end
        )
      + " percentage points); "
      + (.regular_input_reduction_percent | tostring)
      + "% regular-input reduction; "
      + (.baseline_tool_calls | tostring) + " -> "
      + (.optimized_tool_calls | tostring) + " tool calls; "
      + (.baseline_repo_view_tool_calls | tostring) + " -> "
      + (.optimized_repo_view_tool_calls | tostring) + " repo-view tool calls; "
      + (.baseline_other_tool_calls | tostring) + " -> "
      + (.optimized_other_tool_calls | tostring) + " other tool calls; "
      + (.baseline_repo_view_invocations | tostring) + " -> "
      + (.optimized_repo_view_invocations | tostring) + " repo-view invocations."
  ' "${generated_dir}/metrics.json"
  printf '\n## Per-Tool Stats\n\n'
  printf '| Case | Layer | Tool | Tool calls | Observed invocations | Output characters |\n'
  printf '| --- | --- | --- | ---: | ---: | ---: |\n'
  jq -r '
    .cases[] as $case
    | (
        $case.tool_types[]
        | [$case.name, "Codex tool type", .name, .tool_calls, .invocations, .output_characters]
      ),
      (
        $case.operations[]
        | [$case.name, "Command operation", .name, .tool_calls, .invocations, .output_characters]
      )
    | @tsv
  ' "${generated_dir}/metrics.json" |
    while IFS=$'\t' read -r case_name layer tool tool_calls invocations output_characters; do
      printf '| %s | %s | `%s` | %s | %s | %s |\n' \
        "${case_name}" "${layer}" "${tool}" "${tool_calls}" "${invocations}" \
        "${output_characters}"
    done
  printf '\n## Call Graphs\n\n'
  printf '%s\n\n' \
    'Temporal edges are inferred from event order. Output-reference edges additionally require a literal path, location, or symbol from an earlier result to appear in a later command; the transcript does not expose explicit causal dependency IDs.'
  printf '| Case | Nodes | Temporal edges | Output-reference edges | Graph | DOT | Stats JSON |\n'
  printf '| --- | ---: | ---: | ---: | --- | --- | --- |\n'
  jq -r '
    .cases[]
    | [
        .name,
        .tool_call_count,
        .temporal_tool_edge_count,
        .output_reference_edge_count,
        .call_graph_markdown_file,
        .call_graph_dot_file,
        .tool_stats_file
      ]
    | @tsv
  ' "${generated_dir}/metrics.json" |
    while IFS=$'\t' read -r case_name nodes temporal_edges reference_edges graph_file dot_file stats_file; do
      printf '| %s | %s | %s | %s | [%s](%s) | [DOT](%s) | [JSON](%s) |\n' \
        "${case_name}" "${nodes}" "${temporal_edges}" "${reference_edges}" \
        "${case_name}" "${graph_file}" "${dot_file}" "${stats_file}"
    done
  printf '\n## Evidence\n\n'
  printf '%s\n' \
    '- Final answers: `answers/`' \
    '- Executed commands: `commands/`' \
    '- Per-call tool stats: `tool-stats/`' \
    '- Call graphs: `call-graphs/`' \
    '- Raw events: `*.jsonl`' \
    '- Diagnostics: `*.stderr`'
} > "${generated_dir}/summary.md"

current_input_names="${analysis_stage}/current-input-names.txt"
: > "${current_input_names}"
for current_input in "${run_dir}"/*.jsonl "${run_dir}"/*.exit-code; do
  current_name="$(basename "${current_input}")"
  if [[ ! "${current_name}" =~ ^[A-Za-z0-9._-]+$ ]]; then
    printf 'unsafe analysis input name appeared during analysis: %s\n' "${current_name}" >&2
    exit 1
  fi
  printf '%s\n' "${current_name}" >> "${current_input_names}"
done
for metadata_name in \
  manifest.json \
  generation-config.json \
  run-complete.json \
  profiles-snapshot.tsv; do
  if [[ -e "${run_dir}/${metadata_name}" || -L "${run_dir}/${metadata_name}" ]]; then
    printf '%s\n' "${metadata_name}" >> "${current_input_names}"
  fi
done
for dependency_input_spec in \
  'dependency-source/manifest.json:dependency-source-manifest.json' \
  'dependency-source/target-go.mod:dependency-source-target-go.mod' \
  'dependency-source/target-go.sum:dependency-source-target-go.sum' \
  'dependency-source/golang.org/x/time@v0.14.0/rate/rate.go:dependency-source-rate.go' \
  'dependency-source/golang.org/x/time@v0.14.0/rate/rate_test.go:dependency-source-rate_test.go'; do
  dependency_relative="${dependency_input_spec%%:*}"
  dependency_input_name="${dependency_input_spec##*:}"
  if [[ -e "${run_dir}/${dependency_relative}" ||
    -L "${run_dir}/${dependency_relative}" ]]; then
    printf '%s\n' "${dependency_input_name}" >> "${current_input_names}"
  fi
done
for current_input in "${run_dir}"/changed-packet*.json; do
  current_name="$(basename "${current_input}")"
  if [[ ! "${current_name}" =~ ^changed-packet(-[a-z0-9][a-z0-9-]*)?\.json$ ]]; then
    printf 'unsafe changed-packet input name appeared during analysis: %s\n' \
      "${current_name}" >&2
    exit 1
  fi
  printf '%s\n' "${current_name}" >> "${current_input_names}"
done
sort -u -o "${current_input_names}" "${current_input_names}"
if ! cmp -s -- "${input_names}" "${current_input_names}"; then
  printf 'analysis input set changed during analysis: %s\n' "${run_dir}" >&2
  exit 1
fi
if ! verify_run_identity; then
  printf 'run directory changed identity during analysis: %s\n' "${run_dir}" >&2
  exit 1
fi
for source_input in "${snapshotted_sources[@]}"; do
  snapshot_fd_path="/proc/self/fd/${snapshot_fds["${source_input}"]}"
  if [[ -L "${source_input}" ||
    ! -f "${source_input}" ||
    ! -f "${snapshot_fd_path}" ||
    "$(file_state "${source_input}")" != "${snapshot_states["${source_input}"]}" ||
    "$(file_state "${snapshot_fd_path}")" != "${snapshot_states["${source_input}"]}" ]] ||
    ! cmp -s \
      -- "${snapshot_fd_path}" "${snapshot_copies["${source_input}"]}"; then
    printf 'analysis source changed during analysis: %s\n' "${source_input}" >&2
    exit 1
  fi
done

while IFS= read -r generated_file; do
  sync -f -- "${generated_file}"
done < <(find "${generated_dir}" -type f -print)
sync -f -- "${generated_dir}"

outputs=(answers call-graphs commands tool-stats metrics.json summary.md)
if ! verify_run_identity; then
  printf 'run directory changed identity before output validation: %s\n' \
    "${run_dir}" >&2
  exit 1
fi
for output_name in "${outputs[@]}"; do
  output_path="${run_dir}/${output_name}"
  if [[ -L "${output_path}" ]]; then
    printf 'analysis output path must not be a symlink: %s\n' "${output_path}" >&2
    exit 1
  fi
  if [[ -e "${output_path}" ]]; then
    if [[ "${output_name}" == "metrics.json" || "${output_name}" == "summary.md" ]]; then
      if [[ ! -f "${output_path}" ]]; then
        printf 'analysis output path is not a regular file: %s\n' "${output_path}" >&2
        exit 1
      fi
    elif [[ ! -d "${output_path}" ]]; then
      printf 'analysis output path is not a directory: %s\n' "${output_path}" >&2
      exit 1
    fi
  fi
done

publication_started=true
if ! verify_run_identity; then
  printf 'run directory changed identity before output backup: %s\n' \
    "${run_dir}" >&2
  exit 1
fi
for output_name in "${outputs[@]}"; do
  output_path="${run_dir}/${output_name}"
  if [[ -e "${output_path}" || -L "${output_path}" ]]; then
    mv -T -- "${output_path}" "${analysis_stage}/previous/${output_name}"
    backed_up_outputs+=("${output_name}")
  fi
done
if ! verify_run_identity; then
  printf 'run directory changed identity before output publication: %s\n' \
    "${run_dir}" >&2
  exit 1
fi
for output_name in "${outputs[@]}"; do
  mv -T -- "${generated_dir}/${output_name}" "${run_dir}/${output_name}"
  installed_outputs+=("${output_name}")
done
if ! verify_run_identity; then
  printf 'run directory changed identity after output publication: %s\n' \
    "${run_dir}" >&2
  exit 1
fi
sync -f -- "${run_dir}"
publication_committed=true

printf 'analysis written to %s\n' "${run_dir}"
