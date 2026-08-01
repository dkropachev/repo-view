# Short LSP-Replacement Results

## Decision

Use `guarded-high` for the pinned short explain/review workload:

```text
changed return=context, context=4, limit=20
max code lines=60, max patch lines=300
reasoning=high, answer guard=on
```

One terminal `repo-view changed` call returns the exact patch, changed
locations, and bounded source context. The answer guard requires coverage of
every changed artifact, measurement, review finding, source reference, and
residual risk.

This result applies to the pinned task. It does not establish a universal
profile for other repositories or prompts.

## Metrics

All runs use target `17a4e282574ee9392732f6886a331d561e13c008`,
base `f472ef766bae61664675a3a66c36f9a06a939996`, Codex `0.144.0`, read-only
ephemeral sessions, and disabled collaboration/fanout.

```text
effective = regular input + 0.1 * cached input + output
```

Reasoning is included in output. Calls are `total/repo-view/other`.

| Task | Variant | Calls | Regular | Cached | Output | Effective | Saved |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: |
| Explain | Baseline | 15/0/15 | 70,215 | 525,568 | 7,692 | 130,463.8 | n/a |
| Explain | Guarded-high | 1/1/0 | 22,022 | 17,152 | 1,617 | 25,354.2 | **80.57%** |
| Review | Baseline | 16/0/16 | 130,269 | 866,560 | 10,641 | 227,566.0 | n/a |
| Review | Guarded-high | 1/1/0 | 21,758 | 17,152 | 2,160 | 25,633.2 | **88.74%** |

## Quality

Static quality is deterministic weighted task-rubric coverage. Source judges
separately score correctness, completeness, grounding, and task adherence.

| Task | Variant | Static | Judges | Correctness | Completeness | Grounding | Adherence | Verdict |
| --- | --- | ---: | ---: | ---: | ---: | ---: | ---: | --- |
| Explain | Baseline | 100% | 2 | 5 | 5 | 5 | 5 | Reference |
| Explain | Guarded-high | 100% | 2 | 5 | 5 | 5 | 5 | Not worse |
| Review | Baseline | 81.8% | 2 | 5 | 4 | 4 | 5 | Reference |
| Review | Guarded-high | 100% | 2 | 5 | 5 | 5 | 5 | Not worse |

The review candidate preserved the baseline conclusion and added missing
delayed-reservation and cancellation test coverage. No candidate had a
critical omission, unsupported claim, omitted baseline point, or material
contradiction.

Judge cost is verification overhead and is not part of candidate inference:

| Task checked | Judge runs | Regular | Cached | Output | Reasoning | Raw total | Effective |
| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: |
| Explain | 2 | 153,232 | 1,080,576 | 11,771 | 5,905 | 1,245,579 | 273,060.6 |
| Review | 2 | 144,089 | 1,690,880 | 16,474 | 9,179 | 1,851,443 | 329,651.0 |
| Total | 4 | 297,321 | 2,771,456 | 28,245 | 15,084 | 3,097,022 | 602,711.6 |

## Reproduce

Rerun both accepted short cases with fresh model and judge sessions:

```bash
experiments/lsp-replacement/suite.sh live \
  --case 01-simple-explain-accepted,02-simple-review-accepted \
  --judge-repeats 2
```

Recheck stored evidence without new model calls:

```bash
experiments/lsp-replacement/suite.sh replay \
  --case 01-simple-explain-accepted,02-simple-review-accepted
```

The tracked resolution report contains call graphs and per-tool operation
counts: [`suite/latest-resolution.md`](suite/latest-resolution.md).
