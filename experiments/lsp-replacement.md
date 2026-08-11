# LSP-Replacement Experiment

## Purpose

Measure whether `repo-view` can replace shell-driven source navigation without
increasing token cost or reducing answer quality.

Two current workloads are retained:

- short explain/review: one bounded `changed` packet followed by two fixed,
  bounded `inspect` calls; and
- deep explain/review: eight ordered `repo-view` calls followed by one bounded
  dependency-source `awk` read, covering contracts, callers, tests, dependency
  semantics, and performance evidence.

## Current Accepted Evidence

The accepted cases use Temporal target
`74c8455d4338c131d021b5d77799237b2c3dcf1e` and base
`d682c10e7420867124fef9f029c48f0611b653ec`, router model selection,
read-only ephemeral sessions, and disabled collaboration/fanout.

| Workload | Evidence | Calls (total/repo-view/other) | Effective-token saving | Static quality | Source judges |
| --- | --- | ---: | ---: | ---: | --- |
| Short explain | `router-token-performance-simple-explain-v3-20260808` | 3/3/0 | positive | 100% | not worse |
| Short review | `router-token-performance-simple-review-v2-20260808` | 3/3/0 | positive | 100% | not worse |
| Deep explain | `router-token-performance-deep-v6-20260808` | 9/8/1 | 21.50% | 100% | 2 × 5/5/5/5 |
| Deep review | `router-token-performance-deep-v6-20260808` | 9/8/1 | 28.73% | 100% | 2 × 5/5/5/5 |

The suite verifies positive savings for the short cases without duplicating
their exact percentages in this tracked summary. The deep figures come from
the tracked resolution report; accepted evidence identifiers come from the
tracked case manifest and resolution report.

Token accounting is:

```text
effective = regular input + 0.1 * cached input + output
```

Reasoning is included in output. Static quality is deterministic weighted
task-rubric coverage; source judges separately compare correctness,
completeness, grounding, and task adherence.

## Profiles

Short tasks use `guarded-high`, a mechanically enforced three-call profile:

```text
changed return=context, context=4, limit=20
max code lines=60, max patch lines=300
reasoning=high, answer guard=on
navigation=adaptive, hard repo-view invocation cap=3
```

Deep tasks use `investigative-verified-high`. Its hard ceiling is 34
`repo-view` invocations, while the accepted command contract uses exactly
eight. The ninth and final tool call is the exact bounded dependency-source
`awk` command. Analyzer, quality, and promotion gates require the ordered
sequence; prompt-only compliance is not accepted.

## Regression Suite

The suite contains 16 ordered cases: 3 accepted controls and 13 rejected
fixtures. A rejected fixture passes replay only when its expected failure
signature is reproduced. Resolution then requires current code and replacement
evidence to pass all gates.

Current result:

- resolution: 16/16 cases, 888/888 checks;
- replay: 16/16 cases;
- every replacement has positive effective-token savings; and
- every replacement has static quality 100% and two not-worse judges.

Tracked reports:

- [`lsp-replacement/suite/latest-resolution.md`](lsp-replacement/suite/latest-resolution.md)
- [`lsp-replacement/suite/latest-replay.md`](lsp-replacement/suite/latest-replay.md)

## Archived workflow

The shell suite driver has been removed. These reports remain historical
artifacts and must not be presented as a current reproducible benchmark. Use
[`tokenbench`](../benchmarks/tokenbench/README.md) for new paired runs and
signed evidence.

Harness details and evidence layout are in
[`lsp-replacement/README.md`](lsp-replacement/README.md). The older pinned
measurement snapshots remain in
[`lsp-replacement/optimization-results.md`](lsp-replacement/optimization-results.md)
and
[`lsp-replacement/deep-navigation-results.md`](lsp-replacement/deep-navigation-results.md);
their historical call counts are not the current accepted protocol.
