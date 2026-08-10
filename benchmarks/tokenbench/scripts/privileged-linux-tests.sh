#!/usr/bin/env bash

set -Eeuo pipefail

readonly required_environment="TOKENBENCH_REQUIRE_PRIVILEGED_TESTS"
readonly pinned_image_default="golang:1.26.5-bookworm@sha256:6c5605ab3a9a9fb3c4eafe5b3d63cdbf3881caf113262b67862547b54a9db599"

fail() {
  printf 'tokenbench privileged tests: %s\n' "$*" >&2
  exit 1
}

require_command() {
  command -v "$1" >/dev/null 2>&1 || fail "required command is unavailable: $1"
}

script_directory="$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd -P)"
repository_root="$(cd -- "${script_directory}/../../.." && pwd -P)"
host_binary_directory=""
container_fsverity_image=""
container_fsverity_root=""

host_main() {
  [[ "$(uname -s)" == "Linux" ]] || fail "the privileged lane requires Linux"
  [[ "$(uname -m)" == "x86_64" ]] || fail "the x32 seccomp probe requires an x86_64 runner"
  require_command go

  local container_engine="${TOKENBENCH_CONTAINER_ENGINE:-docker}"
  require_command "${container_engine}"
  local image="${TOKENBENCH_PRIVILEGED_IMAGE:-${pinned_image_default}}"
  [[ "${image}" == *@sha256:* ]] || fail "container image must be pinned by digest"

  host_binary_directory="$(mktemp -d)"
  cleanup_host() {
    rm -f -- \
      "${host_binary_directory}/runner.test" \
      "${host_binary_directory}/snapshot.test" \
      "${host_binary_directory}/tokenbench-command.test"
    rmdir -- "${host_binary_directory}" 2>/dev/null || true
  }
  trap cleanup_host EXIT

  (
    cd -- "${repository_root}"
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -mod=readonly -c \
      -o "${host_binary_directory}/runner.test" \
      ./benchmarks/tokenbench/runner
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -mod=readonly -c \
      -o "${host_binary_directory}/snapshot.test" \
      ./benchmarks/tokenbench/snapshot
    CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -mod=readonly -c \
      -o "${host_binary_directory}/tokenbench-command.test" \
      ./benchmarks/tokenbench/cmd/tokenbench
  )

  local host_mount_namespace host_cgroup_namespace
  host_mount_namespace="$(readlink /proc/self/ns/mnt)"
  host_cgroup_namespace="$(readlink /proc/self/ns/cgroup)"

  "${container_engine}" run --rm \
    --privileged \
    --cgroupns=private \
    --network=none \
    --platform=linux/amd64 \
    --tmpfs /tmp:rw,nosuid,nodev,exec,size=1g \
    --mount "type=bind,src=${repository_root},dst=/workspace,readonly" \
    --mount "type=bind,src=${host_binary_directory},dst=/tokenbench-tests,readonly" \
    --env TOKENBENCH_PRIVILEGED_CONTAINER=1 \
    --env "TOKENBENCH_HOST_MOUNT_NAMESPACE=${host_mount_namespace}" \
    --env "TOKENBENCH_HOST_CGROUP_NAMESPACE=${host_cgroup_namespace}" \
    "${image}" \
    bash /workspace/benchmarks/tokenbench/scripts/privileged-linux-tests.sh
}

assert_distinct_namespace() {
  local kind="$1" inherited="$2" observed
  observed="$(readlink "/proc/self/ns/${kind}")"
  [[ -n "${inherited}" ]] || fail "host ${kind} namespace identity was not supplied"
  [[ "${observed}" != "${inherited}" ]] || fail "container did not receive a private ${kind} namespace"
}

assert_passed_tests() {
  local output="$1"
  shift
  grep -q -- '^PASS$' <<<"${output}" || fail "test binary omitted its PASS marker"
  if grep -q -- '^--- SKIP:' <<<"${output}"; then
    fail "required privileged test was skipped"
  fi
  local test_name
  for test_name in "$@"; do
    grep -q -- "^--- PASS: ${test_name} " <<<"${output}" || \
      fail "required test did not run: ${test_name}"
  done
}

run_in_delegation() {
  local delegation="$1" binary="$2" expression="$3"
  shift 3
  local output
  if ! output="$(
    bash -c '
      set -Eeuo pipefail
      delegation="$1"
      shift
      printf "%d\n" "$$" >"${delegation}/cgroup.procs"
      exec "$@"
    ' tokenbench-cgroup-entry "${delegation}" \
      env "${required_environment}=1" \
      "${binary}" \
      -test.run="${expression}" \
      -test.v \
      -test.count=1 \
      -test.timeout=8m 2>&1
  )"; then
    printf '%s\n' "${output}" >&2
    fail "runner privileged test binary failed"
  fi
  printf '%s\n' "${output}"
  assert_passed_tests "${output}" "$@"
}

run_snapshot_tests() {
  local fsverity_root="$1"
  local expression
  expression='^(TestImmutableFileHasMeasuredFSVerity|TestFSVerityMerkleBlockSizeIsPageCompatible|TestReadOnlySelfBindFailsClosedWithoutAuthority|TestPrivilegedMountedAuthorityCloseReleasesKernelBoundary)$'
  local output
  if ! output="$(
    env \
      "${required_environment}=1" \
      TOKENBENCH_FSVERITY_TEST_ROOT="${fsverity_root}" \
      TMPDIR="${fsverity_root}/tmp" \
      /tokenbench-tests/snapshot.test \
      -test.run="${expression}" \
      -test.v \
      -test.count=1 \
      -test.timeout=2m 2>&1
  )"; then
    printf '%s\n' "${output}" >&2
    fail "snapshot privileged test binary failed"
  fi
  printf '%s\n' "${output}"
  assert_passed_tests \
    "${output}" \
    TestImmutableFileHasMeasuredFSVerity \
    TestFSVerityMerkleBlockSizeIsPageCompatible \
    TestReadOnlySelfBindFailsClosedWithoutAuthority \
    TestPrivilegedMountedAuthorityCloseReleasesKernelBoundary
}

run_command_tests() {
  local expression='^TestPhysicalPathSeparationRejectsBindMountAliases$'
  local output
  if ! output="$(
    env \
      "${required_environment}=1" \
      /tokenbench-tests/tokenbench-command.test \
      -test.run="${expression}" \
      -test.v \
      -test.count=1 \
      -test.timeout=2m 2>&1
  )"; then
    printf '%s\n' "${output}" >&2
    fail "tokenbench command privileged test binary failed"
  fi
  printf '%s\n' "${output}"
  assert_passed_tests \
    "${output}" \
    TestPhysicalPathSeparationRejectsBindMountAliases
}

container_main() {
  [[ "$(id -u)" == "0" ]] || fail "privileged container must run as root"
  assert_distinct_namespace mnt "${TOKENBENCH_HOST_MOUNT_NAMESPACE:-}"
  assert_distinct_namespace cgroup "${TOKENBENCH_HOST_CGROUP_NAMESPACE:-}"
  require_command mkfs.ext4
  require_command mount
  require_command mountpoint
  require_command umount
  mount --make-rprivate /

  container_fsverity_image=/tmp/tokenbench-fsverity.ext4
  container_fsverity_root=/tokenbench-fsverity
  local fsverity_image="${container_fsverity_image}"
  local fsverity_root="${container_fsverity_root}"
  truncate -s 512M "${fsverity_image}"
  mkfs.ext4 -q -F -O verity "${fsverity_image}"
  mkdir "${fsverity_root}"
  mount -t ext4 -o loop,nosuid,nodev "${fsverity_image}" "${fsverity_root}"
  cleanup_container() {
    if mountpoint -q "${container_fsverity_root}"; then
      umount "${container_fsverity_root}" || true
    fi
    rmdir "${container_fsverity_root}" 2>/dev/null || true
    rm -f -- "${container_fsverity_image}"
  }
  trap cleanup_container EXIT
  mkdir "${fsverity_root}/tmp"

  local cgroup_root=/sys/fs/cgroup
  [[ "$(stat -fc %T "${cgroup_root}")" == "cgroup2fs" ]] || \
    fail "unified cgroup v2 is unavailable"
  [[ -w "${cgroup_root}/cgroup.procs" ]] || fail "cgroup-v2 mount is not writable"
  local controller
  for controller in cpu memory pids; do
    grep -qw -- "${controller}" "${cgroup_root}/cgroup.controllers" || \
      fail "cgroup-v2 controller is unavailable: ${controller}"
  done

  local driver="${cgroup_root}/tokenbench-ci-driver-v1"
  local delegation="${cgroup_root}/tokenbench-ci-delegation-v1"
  [[ ! -e "${driver}" && ! -e "${delegation}" ]] || fail "stale tokenbench CI cgroup exists"
  mkdir "${driver}"
  printf '%d\n' "$$" >"${driver}/cgroup.procs"
  grep -qx -- "$$" "${driver}/cgroup.procs" || fail "could not isolate the CI driver cgroup"
  printf '%s\n' '+cpu +memory +pids' >"${cgroup_root}/cgroup.subtree_control"
  mkdir "${delegation}"
  printf '%s\n' '4096' >"${delegation}/pids.max"
  printf '%s\n' "$((32 << 30))" >"${delegation}/memory.max"
  mkdir "${delegation}/writable-probe"
  rmdir "${delegation}/writable-probe"

  local runner_expression
  runner_expression='^(TestCgroupManagerAppliesExactArmLimitsAndReusesStablePath|TestArmCleanupRetriesTransientRmdirWithinDeadline|TestLandlockBlocksCgroupEscapeAndAllowsOnlyPinnedWritableRoots|TestLandlockFullPolicyDeniesHostReadsExecutablesAndLoaderBypass|TestPrivilegedExactConnectKernelBoundary|TestPrivilegedExactConnectRejectsAncestorProgram|TestPrivilegedArmInitPIDNamespaceBoundary|TestProcessInspectionSeccompKillsX32SyscallTable)$'
  run_in_delegation \
    "${delegation}" \
    /tokenbench-tests/runner.test \
    "${runner_expression}" \
    TestCgroupManagerAppliesExactArmLimitsAndReusesStablePath \
    TestArmCleanupRetriesTransientRmdirWithinDeadline \
    TestLandlockBlocksCgroupEscapeAndAllowsOnlyPinnedWritableRoots \
    TestLandlockFullPolicyDeniesHostReadsExecutablesAndLoaderBypass \
    TestPrivilegedExactConnectKernelBoundary \
    TestPrivilegedExactConnectRejectsAncestorProgram \
    TestPrivilegedArmInitPIDNamespaceBoundary \
    TestProcessInspectionSeccompKillsX32SyscallTable

  [[ -z "$(cat "${delegation}/cgroup.procs")" ]] || fail "runner left the delegated cgroup populated"
  local entry
  for entry in "${delegation}"/*; do
    [[ ! -d "${entry}" ]] || fail "runner left a child cgroup behind: ${entry}"
  done
  rmdir "${delegation}"
  printf '%s\n' '-cpu -memory -pids' >"${cgroup_root}/cgroup.subtree_control"
  printf '%d\n' "$$" >"${cgroup_root}/cgroup.procs"
  rmdir "${driver}"

  run_command_tests
  run_snapshot_tests "${fsverity_root}"
}

if [[ "${TOKENBENCH_PRIVILEGED_CONTAINER:-}" == "1" ]]; then
  container_main
else
  host_main
fi
