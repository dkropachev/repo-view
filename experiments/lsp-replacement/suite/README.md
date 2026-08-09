# LSP-Replacement Regression Suite

`cases.json` is the tracked source of truth for the parked experiment suite. It
orders cases from a bounded three-call explanation through the accepted multi-stage
navigation workload and includes every rejected deep fixture.

## Commands

List the manifest:

```bash
experiments/lsp-replacement/suite.sh list
```

Replay all local evidence:

```bash
experiments/lsp-replacement/suite.sh replay
```

Verify all retained failed cases against the current implementation:

```bash
experiments/lsp-replacement/suite.sh resolve
```

Run fresh failed-case repairs one at a time:

```bash
experiments/lsp-replacement/suite.sh repair \
  --case 05-rejected-bounded-cost-regression \
  --judge-repeats 2
```

Re-audit a retained attempt without rerunning model generation:

```bash
experiments/lsp-replacement/suite.sh repair \
  --case 05-rejected-bounded-cost-regression \
  --attempt experiments/lsp-replacement/evidence/repairs/05-rejected-bounded-cost-regression/ATTEMPT_ID \
  --judge-repeats 2
```

Run a bounded subset:

```bash
experiments/lsp-replacement/suite.sh replay --max-level 6
experiments/lsp-replacement/suite.sh replay \
  --case 10-rejected-wrong-dependency-semantics,16-deep-verified-accepted
```

Rerun an accepted case with Codex and two source-grounded judges:

```bash
experiments/lsp-replacement/suite.sh live \
  --case 16-deep-verified-accepted \
  --judge-repeats 2
```

With no `--case`, `live` selects the three live-enabled accepted cases and does
not rerun rejected fixtures.

Live and repair definitions must pin a safe repository-relative `source`, a
full target commit, prompt-commit prefix, distinct full base commit, and
`model_mode`. The suite passes every identity input explicitly and rejects a
completed run whose manifest does not match. Router mode is the default and
configures no model or reasoning effort; pinned mode is accepted only when the
definition explicitly requests it.

## Semantics

Replay first regenerates `metrics.json`, per-tool stats, and call graphs from
raw JSONL. It also reruns deterministic quality checks and re-aggregates
existing source-judge outputs without making new judge calls. It then checks:

- the tracked case manifest anchors the SHA-256 of each evidence bundle's
  `source-SHA256SUMS`, so coordinated artifact/checksum rewrites are rejected
- the tracked case and resolution manifests also anchor the exact
  `quality/aggregate-manifest.json`; the aggregate binds all quality outputs,
  raw/derived input snapshots, and evaluator/generator semantics
- aggregate files and committed inputs are opened once and consumed from the
  verified byte snapshots, and missing markers or symlinked/mixed generations
  fail closed
- total tool calls equal `repo-view` plus other tool calls
- `repo-view` tool-call and invocation counts match fixture expectations
- graph node and edge counts agree with metrics
- accepted cases retain completion, token, cap, static-quality, and judge
  expectations
- rejected cases retain the specific failure that caused rejection

A rejected case reports `PASS` when its expected rejection signature is still
present. Rejected fixtures are replay-only because current tool and wrapper
behavior intentionally prevents those failures. Their exact source snapshot,
binary, transcript, answer, and quality evidence remain in the ignored local
`evidence/fixtures/` store.

Metric notation is consistent across repair and resolution reports:

- calls: `total/repo-view/other`
- repo-view operations: `C/F/I/O` means `changed/find/inspect/outline`
- tokens: `regular/cached/output/effective`
- judge scores: `C/C/G/A` means
  correctness/completeness/grounding/adherence
- judge issues: `O/U/B/X` means critical omissions, unsupported claims,
  baseline points omitted, and material contradictions

Static quality is deterministic weighted required-criterion coverage:
`passed weight / total weight`. It is a fast task-specific content gate, not a
model judgment. Source-grounded judges independently compare correctness,
completeness, grounding, task adherence, and material conclusions against the
pinned source and raw transcripts.

Resolve uses `resolutions.json` as a separate source of truth. It preserves the
fixture outcome, then verifies the current status by:

- regenerating and reporting each failed run's own baseline/candidate token,
  tool, operation, quality, and call-graph statistics
- running the named Go regression tests for deterministic CLI and enforcement
  behavior
- regenerating analyzer, per-tool, token, and call-graph artifacts for the
  current accepted evidence
- checking exact static-quality criteria and answer facts that address fixture
  semantic omissions
- re-aggregating two parked source-grounded judges per task and requiring no
  candidate regression

The resolve summary has one status row for every case, a root-cause/fix table,
fixture baseline/candidate statistics for every failed task, all
total/repo-view/other tool calls, every observed tool operation, temporal and
literal-output-reference graph edges, regular/cached/output/effective token
counts, savings, and quality signals. Current replacement statistics are
reported separately and are not substituted for failed-run statistics.

Repair uses each resolution entry's `repair` configuration to create fresh
evidence under `evidence/repairs/CASE_ID/ATTEMPT_ID/`. It runs deterministic
quality before spending judge tokens, then requires every optimized task to:

- complete and use repo-view under a mechanically enforced cap
- save effective tokens relative to its matching baseline
- pass every required static criterion without scoring below baseline
- receive the requested number of complete, source-grounded, not-worse judges
- have no critical omission or unsupported claim; a conclusion that differs
  from baseline must be source-supported and identified by the judges
- have no output-bound violation, cap violation, or budget-tamper command

Every attempt is retained, including failed repairs. Accept an attempt by
placing it under a stable `evidence/current/` path, referencing that path from
the applicable tracked case or resolution, recording the SHA-256 of its
`source-SHA256SUMS` in `source_checksum_sha256`, recording the SHA-256 of its
exact `quality/aggregate-manifest.json` bytes in
`quality_aggregate_sha256`, and rerunning `resolve`.
Multiple failed cases may
share one verified replacement when that run satisfies every case's proof
obligations; each rejected fixture and its statistics remain distinct.

## Results

Each execution writes:

```text
evidence/suites/RUN_ID/
  results.json
  summary.md
  go-tests/
```

Replay and resolution summaries record the actual evidence-analysis and
quality-aggregation paths. A stage is `executed`, `reused`, or `not run`; it is
`mixed` when selected evidence directories used more than one of those paths.
This lets a tracked snapshot identify the verification path that produced it,
including allow-missing and upstream-failure runs.

The latest tracked replay snapshot is
[`latest-replay.md`](latest-replay.md). The latest failed-case audit is
[`latest-resolution.md`](latest-resolution.md). Raw evidence and per-execution
suite results remain ignored because they include local paths and source
content.
