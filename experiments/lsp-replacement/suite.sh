#!/usr/bin/env bash
set -euo pipefail

export LC_ALL=C
export TZ=UTC

experiment_dir="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
repo_root="$(cd "${experiment_dir}/../.." && pwd)"

if [[ $# -eq 0 ]]; then
  set -- replay
fi

command="$1"
shift
cd "${repo_root}"
exec env \
  GOENV=off \
  GOTOOLCHAIN=go1.26.5 \
  GOWORK=off \
  GOFLAGS='-mod=readonly -trimpath -buildvcs=false' \
  go run ./cmd/repo-view-experiment-suite \
    "${command}" \
    --repo-root "${repo_root}" \
    "$@"
