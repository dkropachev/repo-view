# Reproducible LSP-Replacement Experiment

This harness reruns the controlled Codex baseline versus `repo-view` comparison
and keeps all evidence in this repository without committing raw session data.

## Regression Suite

Replay every accepted and rejected case from local evidence, regenerate its
metrics, call graphs, and deterministic quality results, re-aggregate parked
judge outputs, and check the retained outcome:

```bash
experiments/lsp-replacement/suite.sh replay
```

Verify that every retained rejection is addressed by the current Go
implementation and accepted replacement evidence:

```bash
experiments/lsp-replacement/suite.sh resolve
```

Generate and judge a fresh replacement for one failed case:

```bash
experiments/lsp-replacement/suite.sh repair \
  --case 05-rejected-bounded-cost-regression \
  --judge-repeats 2
```

`replay` proves each fixture failure signature remains reproducible. `resolve`
instead runs named Go regression tests, checks exact current quality criteria,
and publishes current token, per-tool, and call-graph statistics for all cases.
`repair` stages deterministic and source-judge gates, retains failed attempts,
and rejects any candidate with non-positive token savings or worse quality.

The default suite is ordered from the one-call explanation through every
rejected deep fixture and the accepted verified deep workload. A rejected
case passes replay when its fixture rejection signature is reproduced; it is
not reclassified as an accepted result.

List cases or run a subset:

```bash
experiments/lsp-replacement/suite.sh list
experiments/lsp-replacement/suite.sh replay --max-level 6
experiments/lsp-replacement/suite.sh replay \
  --case 10-rejected-wrong-dependency-semantics,16-deep-verified-accepted
```

Live reruns are enabled only for accepted cases. They invoke Codex and two
source-grounded judges per task by default:

```bash
experiments/lsp-replacement/suite.sh live \
  --case 01-simple-explain-accepted
experiments/lsp-replacement/suite.sh live \
  --case 16-deep-verified-accepted \
  --judge-repeats 2
```

Each live case pins `source`, full target `commit`, `prompt_commit`, full
`base`, and `model_mode`. The suite passes those values explicitly and rejects
the result unless its manifest records the same source repository, target,
prompt, base, and routing mode.

Suite definitions and machine-checkable expectations are tracked in
`suite/cases.json`; current causes, fixes, and proof obligations are tracked in
`suite/resolutions.json`. Results are stored locally under
`evidence/suites/RUN_ID/` as `results.json` and `summary.md`.

## Run

Prerequisites:

- authenticated `codex` CLI
- Bash 4+ on Linux, with `awk`, `cmp`, `find`, `git`, `go`, `gzip`, `jq`,
  `mktemp`, `realpath -m`, `sha256sum`, `sort`, `stat`, `tar`, `tee`, and
  `unzip`
- the configured source repository and pinned commit

Run the baseline and recommended `guarded-high` profile for both tasks:

```bash
experiments/lsp-replacement/run.sh
```

Run the robust multi-stage LSP-replacement workload. Deep tasks default to the
quality-confirmed `investigative-verified-high` profile:

```bash
experiments/lsp-replacement/run.sh \
  --task deep \
  --variant all \
  --run-id deep-navigation-retry-01
```

Run only one cohort:

```bash
experiments/lsp-replacement/run.sh \
  --task explain \
  --variant all \
  --profile guarded-high \
  --run-id explain-retry-01
```

Inspect the exact configuration and rendered prompts without cloning or
starting Codex:

```bash
experiments/lsp-replacement/run.sh --dry-run
```

Useful overrides:

```text
--task explain|review|all|deep-explain|deep-review|deep
--variant baseline|optimized|all
--profile NAME[,NAME...]|all
--baseline-from RUN_DIR
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
--order baseline-first|optimized-first
--run-id ID
--dry-run
```

Source, commit, and executable-version defaults are pinned in `config.env`.
Model selection defaults to router/default mode. Prompt text is stored in `prompts/`, and
all packet/reasoning combinations are named in `profiles.tsv`. Normal tasks
default to `guarded-high`; deep tasks default to
`investigative-verified-high`. The latter uses a mechanically enforced
34-repo-view-invocation cap and requires Codex `--json` events. The current
deep reservation protocol uses exactly eight targeted `repo-view` calls plus
exactly one bounded dependency-source `awk` read. Analyzer, quality, and
promotion gates require the ordered command sequence and the exact dependency
command; prompt-only compliance is not accepted. Before generation, the
runner creates a fresh private `GOMODCACHE`, authenticates the exact
`golang.org/x/time v0.14.0` module archive against the target commit's
`go.sum`, and extracts only the two permitted rate-source files. Command 9
reads those files through the stable `$HOME/dependencies/...` path, never the
ambient module cache. The target `go.mod`/`go.sum`, extracted bytes, and a
provenance manifest are retained under `dependency-source/` and bound by
`source-SHA256SUMS`.

`--commit` must be a full lowercase 40- or 64-hex object ID. The runner
verifies it from a fresh fetch against `--source`, even when reusing an
existing worktree. `--base` may be a full commit ID, a target-relative
`HEAD` expression such as `HEAD^`, or one exact head or tag from `--source`;
the resolved base must be an ancestor of the target. `--prompt-commit` must
be a lowercase hexadecimal target prefix of at least seven characters;
otherwise a nine-character prefix is derived when the value came from
configuration. Runtime prompts use the canonical resolved base commit rather
than a symbolic ref. Generated
worktree and evidence paths inside this repository must be ignored by Git so
they cannot enter the captured source archive; the sibling worktree lock path
must be ignored too. Source symlinks, FIFOs, devices, sockets, and
multiply-linked files are rejected before extraction or building. Reused
worktrees are accepted only when their checkout, index, local
attributes/configuration, and ignored paths cannot conceal state, and targets
containing unmaterialized submodules are rejected.

Generation defaults to router mode, which omits both the Codex `-m` flag and
any model configuration so the router/default selection is authoritative.
Pinned mode is available only through the explicit `--model-mode pinned`
override. Codex and Go versions remain pinned by `LSP_CODEX_VERSION` and
`LSP_GO_VERSION` (or the matching CLI overrides), recorded in
`manifest.json`, and required to match imported baselines together with the
generation-isolation schema. The manifest also binds each selected task's
exact rendered prompt digest and a normalized digest of the effective
generation flags/environment; imported baselines must match both. The
normalized input is retained as `generation-config.json`, while absolute Go
paths remain audit metadata rather than relocation-sensitive identity. Host
Go commands and optimized wrapper builds use the recorded pinned Go
environment, and Codex itself starts from an explicit `env -i` allowlist.
Both variants use a private auth-only Codex home and a custom read-only
permission profile. Filesystem access starts denied at
the root and reopens only minimal runtime paths, the target checkout,
repo-view cache, and recorded Go source/cache paths; the Codex home and
canonical auth source remain denied. Subprocesses start from a pinned
allowlist environment rather than inheriting credentials or ambient Git
variables. User config and rules, project instructions, hooks, MCP servers,
apps, and collaboration are disabled. In pinned mode the model is an explicit
input; in router mode the deliberate absence of model configuration is the
recorded input. Codex and Go versions, permissions, and the child environment
remain explicit rather than machine-local defaults.

Each run is assembled in a private sibling directory. After transcript
validation and analysis finish, `run-complete.json` is written and the whole
directory is renamed atomically to `runs/RUN_ID`. Setup or analysis failures
remove the private stage; completed Codex failures are published atomically
with a `failed` outcome for diagnosis. Worktree and run claims use atomic
private lock directories; cleanup checks their device and inode so a symlink
or path replacement is never truncated or removed.

Until a new isolated canonical baseline is explicitly promoted, generate
baseline and optimized cohorts in the same run:

```bash
experiments/lsp-replacement/run.sh \
  --task all \
  --variant all \
  --profile guarded-high \
  --run-id guarded-high-retry-01
```

`--baseline-from` accepts only a completed baseline whose target, prompt,
base, model mode/configuration, Codex version, Go version, and
generation-isolation pins match
the new run. Selected-task prompt digests and the normalized generation
configuration's shared environment projection must match too; baseline-only
case-prompt bindings and the navigation-enforcement marker are self-bound to
their source run and may differ from the optimized run. Legacy
`evidence/current/simple` and `current/deep` manifests do not carry those pins
and are intentionally not reusable.

`--variant optimized` requires `--baseline-from`; use `--variant all` to
generate a live same-run comparison. Optimized invocations pass the canonical
root, resolved base commit, and profile return contract into the wrapper, and
both the manifest and normalized generation config record whether those
mechanical navigation semantics are enforced.

Treat cache-adjusted effective-token results from different runs as directly
comparable only when their `task_selection`, `variant_selection`, `order`, and
baseline-import schedule match. Prompt-cache warmth depends on that execution
schedule. Historical comparisons must therefore report raw-token change and
the baseline and optimized cached-input percentages (including their delta)
alongside effective-token change. A colder cache with lower raw token use is
not, by itself, evidence of a navigation regression.

## Quality

Run the deterministic required-fact checks:

```bash
experiments/lsp-replacement/quality-check.sh \
  experiments/lsp-replacement/evidence/runs/RUN_ID \
  --enforce
```

Add two independent source-grounded Codex judges:

```bash
experiments/lsp-replacement/quality-check.sh \
  experiments/lsp-replacement/evidence/runs/RUN_ID \
  --judge-repeats 2 \
  --enforce
```

With `--enforce` and live judges requested, the script first requires every
optimized candidate to pass all static criteria without scoring below its
baseline and to show positive effective-token savings. If that deterministic
pre-gate fails, no judge process is started.

Source-grounded judges inspect the pinned source checkout, packet, baseline and
candidate answers, and raw transcripts. This prevents packet-only evidence from
biasing the comparison, allows a supported candidate to correct a baseline
conclusion, and validates claims about commands, tests, or sandbox failures.
Valid judge outputs are reused when no new judge calls are requested.
Judge token usage is written separately to `quality/judge-usage.json` and
`quality/summary.md`; it is not mixed into candidate token totals.

The quality gate accepts only schema-2 metrics using the recorded effective
token formula, then independently rebuilds metrics, answers, tool statistics,
and call graphs from immutable raw-transcript snapshots. It rejects forged
accounting, incomplete optimized-only provenance, unsafe local Git
configuration, and symlinked quality output directories. Judges use a
root-deny permission profile with explicit read access for the pristine
checkout, evaluator inputs, `GOROOT`, and `GOMODCACHE`; their auth home remains
unreadable to model-issued tools and their shell receives a fixed,
credential-free Go/Git environment.

## Evidence

Each run is stored at:

```text
experiments/lsp-replacement/evidence/runs/RUN_ID/
```

The runner captures:

- resolved target/base revisions and execution settings
- toolchain versions
- exact repository source snapshot, binary, patch, status, and checksums
- Codex JSONL and stderr for every case
- invocation, exit code, and elapsed time
- extracted final answers and executed commands
- machine-readable `metrics.json` with regular, cached, output, reasoning, raw
  total, cache-adjusted effective tokens, total tool calls, `repo-view` tool
  calls, other tool calls, and actual `repo-view` CLI invocations
- per-tool and per-command-operation counts plus the JSON call graph under
  `tool-stats/`
- rendered temporal and literal-output-reference call graphs under
  `call-graphs/`, in Markdown and Graphviz DOT formats
- human-readable `summary.md`
- deterministic and optional source-grounded quality evidence under `quality/`
- a schema-2 quality aggregate whose `inputs.json` binds raw/derived evaluator
  inputs, rubric/schema, generator sources, and evaluator semantics

Raw evidence is ignored by Git. To publish a run, scrub local paths and source
snippets first, then copy only the intended summary or sanitized artifacts into
a tracked location.

Reports write call counts as `total/repo-view/other`. `Tool calls` counts
completed Codex tool items, including command execution, planning/todo, MCP,
web, and other recognized call events. `repo-view tool calls` is the subset of
command-execution calls whose shell command contains `repo-view`; `other tool
calls` is total minus that subset. `repo-view invocations` parses those
commands and counts every CLI invocation separately, including multiple
invocations inside one tool call.

Static quality is deterministic weighted task-rubric coverage:
`passed weight / total weight`. It catches missing required facts before judge
tokens are spent, but does not prove factual correctness. Source judges remain
the final quality comparison.

Per-tool stats preserve two layers. Codex tool types count model/tool
round-trips. Command operations report executables observed inside each shell
call, so one compound tool call can contain `go`, `rg`, and `sed`. Operation
output characters are attributed to every operation present in that call and
therefore must not be summed across operations.

Codex JSONL does not expose explicit result-dependency IDs. Call graphs label
the edge from one completed result to the next model-issued call as an inferred
temporal/model-context edge. A separate inferred output-reference edge is added
only when a later command literally reuses a path, location, or symbol emitted
by an earlier result.

Rejected deep runs are retained under `evidence/fixtures/CASE_ID`; accepted
evidence remains under `evidence/current/`. The local evidence README
describes the complete per-run layout.

Rebuild metrics from an existing run:

```bash
experiments/lsp-replacement/analyze.sh \
  experiments/lsp-replacement/evidence/runs/RUN_ID
```

The current measurement summary is in
[`../lsp-replacement.md`](../lsp-replacement.md). Short-workload details are in
[`optimization-results.md`](optimization-results.md). Deep measurements,
failed regression cases, and reproduction commands are in
[`deep-navigation-results.md`](deep-navigation-results.md).
