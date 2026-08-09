# Local Evidence Store

This directory is the durable local store for LSP-replacement experiments.
Everything except this README and `.gitignore` is intentionally ignored by
Git because raw Codex JSONL can be large and can contain local paths or source
snippets.

Layout:

```text
current/NAME/
  manifest.json
  run-complete.json
  environment.txt
  repo-view.bin
  repo-view-source.tar.gz
  source-SHA256SUMS
  repo-view.patch
  baseline-explain.jsonl
  baseline-explain.stderr
  baseline-review.jsonl
  optimized-explain.jsonl
  optimized-review.jsonl
  answers/
  commands/
  tool-stats/
    CASE.json
  call-graphs/
    CASE.md
    CASE.dot
  metrics.json
  summary.md
  quality/
    static.json
    judge-TASK-N.json
    judge-TASK-N.jsonl
    judge-TASK-N.stderr
    judge-TASK-N.exit-code
    judge-TASK-N.inputs.sha256
    judge-TASK-N.result.sha256
    judge-TASK-N.legacy-attestation.json
    judges.json
    judge-usage.json
    quality.json
    summary.md
    inputs.json
    aggregate-manifest.json

runs/RUN_ID/
  fresh experiment using the same layout

fixtures/CASE_ID/
  complete rejected run
  raw artifacts required by a regression fixture

repairs/CASE_ID/ATTEMPT_ID/
  failed-case replacement attempt
  complete run artifacts

suites/SUITE_ID/
  results.json
  summary.md
  go-tests/
```

`current/` contains the accepted evidence used by the suite. `runs/` contains
fresh experiments understood by `../analyze.sh` and `../quality-check.sh`.
`fixtures/` contains the exact rejected evidence required by regression cases.
The current tracked conclusion is in `../../lsp-replacement.md`; the accepted
case mapping is in `../suite/latest-resolution.md`. The older
`../optimization-results.md` and `../deep-navigation-results.md` files are
historical pinned measurement snapshots.

Fresh runs are prepared in private ignored sibling directories and renamed to
`runs/RUN_ID` only after reaching a terminal state. `run-complete.json`
records `state`, `outcome`, the runner exit code, and completion time. Its
presence therefore distinguishes an atomically published run from abandoned
or externally assembled evidence.

`repairs/` contains per-case replacement attempts. The repair suite
first checks completion, token savings, static quality, navigation accounting,
and hard bounds; it spends source-judge tokens only after those checks pass.
Accepted evidence is promoted to `current/` and referenced by
`../suite/resolutions.json`.

`suites/` stores replay, resolution, or live-suite results. The tracked case
manifest is `../suite/cases.json`; replay validates both accepted outcomes and
the retained failure signature for each rejected run. The tracked
`../suite/resolutions.json` maps every case to its current cause, fix, named Go
regressions, accepted evidence, and quality assertions.

`source-SHA256SUMS` records bundle-relative names for `repo-view.bin` and
`repo-view-source.tar.gz`. Replay and resolution validate both artifacts, and
the manifest can also be checked manually from its evidence directory with
`sha256sum -c source-SHA256SUMS`.

The source archive is copied to a private snapshot and compared with
before/staged/after captures before use. The retained `repo-view.bin`, the
optimized-run wrapper, and analysis tooling all run from that snapshot under
read-only module settings; a final comparison rejects any source mutation
during build or analysis. The optimized wrapper uses a run-private cache
outside the snapshot so even transient build directories cannot alter the
captured tree.

Generation manifests bind the explicit model and Codex CLI version. Fresh
baseline and optimized sessions run with the same private auth-only Codex home
and isolation flags. A custom read-only permission profile denies filesystem
access at the root, then reopens only minimal runtime paths, the target
checkout, the repo-view cache, and recorded Go source/cache paths; the private
Codex home and canonical auth source stay denied. A pinned child environment
allowlist excludes credentials and ambient Git state. User config and rules,
project instructions, hooks, MCP servers, apps, and collaboration are
disabled. Imported baselines must carry matching model, CLI, Go-version, and
generation-isolation pins.

Quality enforcement additionally requires canonical full target/base object
IDs, a prompt commit that is a lowercase prefix of the target, and a nonempty
base ref. An optimized-only run must name an imported baseline, and its copied
source manifest must match the target, prompt, base, model, Codex, Go, and
generation-isolation fields exactly, including the selected-task rendered
prompt digests and normalized generation-configuration digest. Retained
manifests that predate those fields are legacy, unisolated evidence; they are
not silently accepted by `--enforce` or represented as newly isolated.

Judge filenames use `judge-TASK-REPEAT.*`. Each output must satisfy
`../quality-output-schema.json`. Live judging records a path-independent
evaluator-input digest in `judge-TASK-REPEAT.inputs.sha256`. It binds the prompt
semantics, repeat slot, target commit, pinned evaluator model and Codex CLI
version, rubric, output schema, each candidate's normalized changed packet,
baseline inputs, ordered candidate identities and inputs, the pinned Go
version, and the effective permission/shell environment. Live judges run from
a hook-free pristine checkout of the canonical manifest commit. Their custom
permission profile denies the filesystem root, explicitly reopens only the
checkout, immutable evaluator inputs, minimal tool runtime, `GOROOT`, and
`GOMODCACHE`, gives a private scratch/cache root the only write access, and
explicitly denies both the private Codex home and canonical auth file. The
child shell inherits no ambient environment and receives fixed Go and
hardened-Git variables. User rules, project instructions, hooks, MCP servers,
apps, and collaboration are disabled and included in the input digest.
Relocating an unchanged evidence bundle does not invalidate it.

Live cache reuse and offline aggregation both require a matching digest
sidecar, a recorded zero exit code, a strict single-session JSONL transcript,
and `judge-TASK-REPEAT.result.sha256`, which binds the JSON output, transcript,
and exit status. The JSON output must equal the final agent message
semantically. Offline aggregation ignores unbound, mismatched, malformed,
multi-document, nonnumeric, failed, and legacy
`judge-vN-*` artifacts. `quality/quality.json` records the pinned evaluator
model, Codex CLI version, cache-schema version, and relocatable evaluator
environment semantics used to interpret those sidecars.

Before aggregation, quality checking independently regenerates schema-2
metrics, answers, command logs, tool statistics, and call graphs from the
snapshotted raw JSONL and requires an exact semantic match. It rejects a
symlinked/replaced `quality/` directory and publishes aggregate files only
after all snapshotted inputs remain unchanged. `quality/inputs.json` commits
every bundle input consumed by the evaluator, every private evaluator
snapshot, the rubric/schema, the quality/analyzer sources, run-stats sources,
profiles, `go.mod`, and the analysis/judge semantics. The schema-2
`aggregate-manifest.json` is published last and commits `inputs.json` plus
`static.json`, `judges.json`, `judge-usage.json`, `quality.json`, and
`summary.md`.

Suite manifests independently record the SHA-256 of the exact
`aggregate-manifest.json` bytes. Consumers open each marker, output, and
committed input once, verify its opened file identity and digest, and perform
summaries, assertions, and tool-accounting checks from those immutable byte
snapshots. A missing marker, rewritten marker, changed input, symlink, or mixed
generation therefore fails closed.

To migrate a current-format judge created before sidecars existed, first
independently verify that it belongs to the bundle's current inputs and
completed successfully, then explicitly bind it once:

```bash
experiments/lsp-replacement/quality-check.sh RUN_DIR \
  --judge-repeats 0 \
  --bind-legacy-judges
```

Binding is an operator trust decision: it writes a distinct deterministic
`legacy-attestation.json` only for single-document, schema-valid numeric judge
outputs whose strict lifecycle transcript matches the output and whose
recorded exit code is zero. It fails when no eligible artifact exists, never
overwrites a mismatched attestation or current-cache sidecar, and cannot be
combined with live judge repeats. The attestation records unknown model and
Codex version plus `legacy-unisolated` generation; it never creates current
`inputs.sha256` or `result.sha256` cache sidecars.

Binding attests only those retained judge bytes against the current evaluator
inputs. It does not upgrade the generation run to the current isolation
contract. Aggregates containing these judges are visibly marked
`legacy-unisolated-attested` and cannot satisfy strict enforcement.
