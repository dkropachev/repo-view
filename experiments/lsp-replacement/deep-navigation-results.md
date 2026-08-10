# Deep LSP-Replacement Navigation Results

## Decision

Use `investigative-verified-high` for this pinned deep-navigation workload:

```text
changed return=context, context=4, limit=20
max code lines=60, max patch lines=300
reasoning=high, answer guard=on
navigation=batched, hard repo-view invocation cap=34
```

Promotion requires both deep explain and deep review to:

- use multi-stage repo-view navigation under hard command and output bounds;
- save effective tokens against the matching baseline;
- pass every deterministic required criterion;
- receive two complete, source-grounded, not-worse judgments; and
- report commands and test execution consistently with the raw transcript.

Current general evidence is `evidence/current/deep`.
It resolves cases 03-13 and supplies accepted control case 16. Cases 14 and 15
use `evidence/current/semantic-call-chain` and `evidence/current/review-path`.

## Scope And Metrics

The pinned target is Temporal commit
`17a4e282574ee9392732f6886a331d561e13c008` against first parent
`f472ef766bae61664675a3a66c36f9a06a939996`. The local checkout is
`.cache/experiments/lsp-replacement/target`; source was copied from a local
Temporal checkout. No external repository-list file was read or
modified.

The prompts require changed behavior, a five-method reservation contract
matrix, complete production paths, concrete-type assumptions, direct and
indirect tests, pinned dependency behavior, and qualified performance claims.

Token accounting is:

```text
effective = regular input + 0.1 * cached input + output
```

Reasoning tokens are already part of output. Calls are
`total/repo-view/other`; repo-view invocations count CLI processes, so one tool
call can contain more than one invocation. Static quality is deterministic
weighted rubric coverage: `passed weight / total weight`. Judges separately
score correctness, completeness, grounding, and task adherence.

## Current Measurements

| Task | Variant | Regular | Cached | Output | Reasoning | Raw total | Effective | Calls | repo-view invocations |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Deep explain | Baseline | 155,681 | 2,918,144 | 29,768 | 19,359 | 3,103,593 | 477,263.4 | 40/0/40 | 0 |
| Deep explain | Verified | 101,983 | 1,420,288 | 18,700 | 9,300 | 1,540,971 | 262,711.8 | 28/22/6 | 22 |
| Deep review | Baseline | 170,159 | 3,059,200 | 23,394 | 15,099 | 3,252,753 | 499,473.0 | 30/0/30 | 0 |
| Deep review | Verified | 137,563 | 1,234,176 | 13,001 | 5,727 | 1,384,740 | 273,981.6 | 32/24/8 | 24 |

| Task | Regular saved | Cached saved | Output saved | Raw saved | Effective saved |
| --- | ---: | ---: | ---: | ---: | ---: |
| Deep explain | 34.49% | 51.33% | 37.18% | 50.35% | **44.95%** |
| Deep review | 19.16% | 59.66% | 44.43% | 57.43% | **45.15%** |

Navigation stayed below the 34-invocation cap:

| Task | Used | changed | find | inspect | outline | Bound violations | Tamper attempts |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Deep explain | 22 | 1 | 15 | 6 | 0 | 0 | 0 |
| Deep review | 24 | 1 | 17 | 6 | 0 | 0 | 0 |

Call-graph edges are inferred because Codex JSONL has no explicit dependency
IDs. Temporal edges connect successive tool results; output-reference edges
also require literal reuse of an earlier path, location, or symbol.

| Task | Variant | Temporal edges | Output-reference edges |
| --- | --- | ---: | ---: |
| Deep explain | Baseline | 39 | 40 |
| Deep explain | Verified | 27 | 39 |
| Deep review | Baseline | 29 | 43 |
| Deep review | Verified | 31 | 46 |

Specialized cases also passed:

| Case | Task | Calls | Effective | Saved | Static | Candidate judges |
| --- | --- | ---: | ---: | ---: | ---: | --- |
| 14 semantic call chain | Deep explain | 34/31/3 | 285,146.4 | 40.25% | 100% | 2 x 5/5/5/5 |
| 14 semantic call chain | Deep review | 33/25/8 | 255,939.2 | 48.76% | 100% | 2 x 5/5/5/5 |
| 15 review path | Deep review | 27/23/4 | 235,918.2 | 52.77% | 100% | 2 x 5/5/5/5 |

## Quality Confirmation

| Task | Variant | Static | Judges | Correctness | Completeness | Grounding | Adherence | Verdict |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| Deep explain | Baseline | 85% | 2 | 4 | 4 | 4 | 5 | Reference |
| Deep explain | Verified | 100% | 2 | 5 | 5 | 5 | 5 | Not worse |
| Deep review | Baseline | 70% | 2 | 4 | 4 | 4 | 5 | Reference |
| Deep review | Verified | 100% | 2 | 5 | 5 | 5 | 5 | Not worse |

The explain candidate corrected the baseline's false claim that no reader test
reached the real priority-to-adapter-to-dynamic limiter chain.

The review candidate found a narrow exported `ReserveN` behavior
regression. A monotonic-bearing timestamp can now remain monotonic in native
reservation delay and cancellation calculations, while the removed wrapper
forced wall time. The baseline noticed related clock behavior but did not
classify its wall-clock-jump impact as a finding. A source-supported candidate
conclusion may correct the baseline; differing conclusions remain visible.

The review also completed fake-clock `Wait`/`WaitN` coverage and correctly
reported that no tests or benchmarks ran. The baseline had claimed an
unsupported sandbox-blocked test.

Judge cost is promotion overhead and is not added to candidate inference:

| Task checked | Judge runs | Regular | Cached | Output | Reasoning | Raw total | Effective |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Deep explain | 2 | 220,128 | 2,308,352 | 18,781 | 10,012 | 2,547,261 | 469,744.2 |
| Deep review | 2 | 309,416 | 2,879,744 | 20,475 | 11,243 | 3,209,635 | 617,865.4 |
| Total | 4 | 529,544 | 5,188,096 | 39,256 | 21,255 | 5,756,896 | 1,087,609.6 |

## Failed Regression Cases

Every failed run remains under `evidence/fixtures/`; every failed repair attempt
remains under `evidence/repairs/`. The tracked
[`suite/latest-resolution.md`](suite/latest-resolution.md) contains exact
per-case calls, token splits, quality signals, per-tool counts, and call
graphs.

| Case | Fixture failure |
| --- | --- |
| 03 incomplete deep run | Unbounded exploration; review did not complete. |
| 04 tool contract defects | Exposed lost path filters, rejected batching, unfair limits, wrong `_` symbol selection, and output-bound violations. |
| 05 bounded cost regression | Token cost regressed 14.60% for explain and 46.79% for review; Go scope extraction stopped at an inner block. |
| 06 soft cap and quality | Tokens fell, but explain quality regressed and a prose-only 20-repo-view-invocation cap failed. |
| 07 environment budget bypass | `env -u` bypassed an environment-only budget. |
| 08 repo-view not used | Both candidates ignored repo-view. |
| 09 read-only budget file | A writable budget file failed in the read-only sandbox. |
| 10 wrong dependency semantics | Used the wrong cached `x/time` version and made incorrect clock claims. |
| 11 confirmed soft cap | Dependency semantics improved, but a prose-only 26-repo-view-invocation cap failed and explain omitted external type-assertion risk. |
| 12 instruction-only cap | Static content reached 100%, but instruction-only limits still allowed 49 and 40 repo-view invocations. |
| 13 inherited FD budget | An inherited descriptor was closed or reused across Codex `exec`, so first calls falsely saw an exhausted budget. |
| 14 semantic call chain | Transcript enforcement worked, but call-chain and indirect-test claims were incomplete or wrong. |
| 15 incomplete review path | Review saved tokens but omitted explicit contract and complete local wrapper-test proof. |

Token regressions occurred in cases 04 and 05. Quality regressions occurred in
04 explain, 05 review, 06 explain, 10 explain/review, 14 explain, and 15
review. These cases are intentionally retained.

## Current Guarantees

The suite verifies:

- batched symbols, locations, and files with fair shared limits;
- repeatable path filters and deterministic deduplication before limiting;
- full enclosing Go declarations and hit-centered truncation;
- exact dependency-manifest search, including `go.mod`;
- positive CLI bounds, with `--return locations` omitting code-line bounds;
- wrapper-compiled per-command and cumulative transcript limits;
- complete contract, production-path, local wrapper-test, and upstream-test
  quality gates; and
- regular/cached/output/effective tokens, call triples, per-operation stats,
  inferred call graphs, and retained judge evidence.

The evidence confirms that batching and bounded source reduce ping-pong, hard
transcript limits work in a read-only model sandbox, and deterministic gates
and source judges catch different failures.

The evidence rejects four assumptions: more calls imply better quality; prose
or mutable environment budgets enforce limits; static keyword coverage proves
correctness; any cached dependency version is acceptable evidence.

## Archived validation

The shell suite driver used to replay and resolve this historical evidence has
been removed. Use `tokenbench` for new reproducible paired studies.

The historical fresh-run command was:

```bash
experiments/lsp-replacement/run.sh \
  --task deep \
  --variant optimized \
  --profile investigative-verified-high \
  --baseline-from experiments/lsp-replacement/evidence/current/deep \
  --run-id deep-navigation-retry-N

experiments/lsp-replacement/quality-check.sh \
  experiments/lsp-replacement/evidence/runs/deep-navigation-retry-N \
  --judge-repeats 2 \
  --enforce
```

Regenerate metrics without rerunning Codex:

```bash
experiments/lsp-replacement/analyze.sh \
  experiments/lsp-replacement/evidence/current/deep
```

Raw evidence includes source and binary checksums, prompts, invocations, JSONL,
stderr, answers, commands, metrics, static criteria, judge outputs, judge token
usage, and call graphs. It remains local and ignored by Git.
