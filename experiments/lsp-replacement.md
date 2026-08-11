# LSP-Replacement Experiment

## Purpose

Measure whether `repo-view` can replace shell-driven source navigation without
increasing token cost or reducing answer quality.

Two workloads are retained:

- short explain/review: one bounded `changed` packet;
- deep explain/review: multi-stage contracts, callers, tests, dependency
  semantics, and performance evidence.

## Setup

- target: `.cache/experiments/lsp-replacement/target`
- commit: `17a4e282574ee9392732f6886a331d561e13c008`
- base: `f472ef766bae61664675a3a66c36f9a06a939996`
- Codex: `0.144.0`
- sandbox: read-only
- sessions: ephemeral
- collaboration and fanout: disabled
- baseline: normal Codex CLI
- candidate: `scripts/codex-with-repo-view`

Raw runs remain under the ignored
`experiments/lsp-replacement/evidence/` directory. No external repository-list
file was read or modified.

```text
effective = regular input + 0.1 * cached input + output
```

Reasoning is included in output. Calls are `total/repo-view/other`. Static
quality is deterministic weighted task-rubric coverage; source judges
separately compare correctness, completeness, grounding, and task adherence.

## Short Workload

Use `guarded-high`:

```text
changed return=context, context=4, limit=20
max code lines=60, max patch lines=300
reasoning=high, answer guard=on
```

| Task | Baseline calls | Candidate calls | Baseline effective | Candidate effective | Saved | Candidate quality |
| --- | ---: | ---: | ---: | ---: | ---: | --- |
| Explain | 15/0/15 | 1/1/0 | 130,463.8 | 25,354.2 | 80.57% | static 100%; 2 x 5/5/5/5 |
| Review | 16/0/16 | 1/1/0 | 227,566.0 | 25,633.2 | 88.74% | static 100%; 2 x 5/5/5/5 |

The exact packet matches the authoritative Git diff and reports explicit patch,
code, and result truncation. Detailed token and judge costs are in
[`lsp-replacement/optimization-results.md`](lsp-replacement/optimization-results.md).

## Deep Workload

Use `investigative-verified-high` with a hard ceiling of 34 repo-view CLI
invocations:

| Task | Baseline calls | Candidate calls | Baseline effective | Candidate effective | Saved | Candidate quality |
| --- | ---: | ---: | ---: | ---: | ---: | --- |
| Deep explain | 40/0/40 | 28/22/6 | 477,263.4 | 262,711.8 | 44.95% | static 100%; 2 x 5/5/5/5 |
| Deep review | 30/0/30 | 32/24/8 | 499,473.0 | 273,981.6 | 45.15% | static 100%; 2 x 5/5/5/5 |

The deep-review candidate found a narrow exported `ReserveN`
wall-clock/monotonic behavior regression that the baseline underclassified.
Both source judges accepted the correction as better than baseline, not a
candidate regression.

Specialized cases retain positive effective savings:

- case 14 explain: 40.25%; review: 48.76%;
- case 15 review: 52.77%.

Detailed tokens, judge costs, failed regression fixtures, and call graphs are
in
[`lsp-replacement/deep-navigation-results.md`](lsp-replacement/deep-navigation-results.md).

## Regression Suite

The suite contains 16 ordered cases: 3 accepted controls and 13 rejected
fixtures. A rejected fixture passes replay only when its expected failure
signature is reproduced. Resolution then requires current code and replacement
evidence to pass all gates.

Current result:

- resolution: 16/16 cases, 673/673 checks;
- replay: 16/16 cases;
- every replacement has positive effective-token savings;
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
[`lsp-replacement/README.md`](lsp-replacement/README.md).
