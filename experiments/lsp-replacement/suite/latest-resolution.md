# Failed-Case Resolution Suite

- Started: `2026-08-09T02:16:26.524537658Z`
- Finished: `2026-08-09T02:17:06.143165288Z`
- Case manifest SHA-256: `9746a218ce972b89ffd62373dc4f1a56df9c61f1ab076169405363a83059e2e2`
- Resolution manifest SHA-256: `6b807c965197467ed7d9545a85e80512fa82698744a6e092d5d911fb13a0c7f6`
- Result: **PASS**
- Evidence analysis: `executed`
- Quality aggregation: `executed`
- Cases: **16/16 passed** (13 resolved, 3 accepted)
- Checks: **888/888 passed**

| Level | Case | Resolution | Result | Checks |
| ---: | --- | --- | --- | ---: |
| 1 | `01-simple-explain-accepted` | accepted | **PASS** | 50/50 |
| 2 | `02-simple-review-accepted` | accepted | **PASS** | 50/50 |
| 3 | `03-rejected-incomplete-deep-run` | resolved | **PASS** | 65/65 |
| 4 | `04-rejected-tool-contract-defects` | resolved | **PASS** | 58/58 |
| 5 | `05-rejected-bounded-cost-regression` | resolved | **PASS** | 59/59 |
| 6 | `06-rejected-soft-cap-and-quality` | resolved | **PASS** | 59/59 |
| 7 | `07-rejected-environment-budget-bypass` | resolved | **PASS** | 59/59 |
| 8 | `08-rejected-repo-view-not-used` | resolved | **PASS** | 56/56 |
| 9 | `09-rejected-read-only-budget-file` | resolved | **PASS** | 58/58 |
| 10 | `10-rejected-wrong-dependency-semantics` | resolved | **PASS** | 57/57 |
| 11 | `11-rejected-confirmed-soft-cap` | resolved | **PASS** | 59/59 |
| 12 | `12-rejected-instruction-only-cap` | resolved | **PASS** | 61/61 |
| 13 | `13-rejected-inherited-fd-budget` | resolved | **PASS** | 61/61 |
| 14 | `14-rejected-semantic-call-chain` | resolved | **PASS** | 60/60 |
| 15 | `15-rejected-incomplete-review-path` | resolved | **PASS** | 36/36 |
| 16 | `16-deep-verified-accepted` | accepted | **PASS** | 40/40 |

## Current accepted replacement

Evidence: `runs/router-token-performance-deep-v6-20260808` (`strict-current`).

| Task | Effective-token saving | Calls (total/repo-view/other) | Static quality | Judges C/C/G/A | Issues O/U/B/X |
| --- | ---: | ---: | ---: | ---: | ---: |
| deep-explain | 21.50% | 9/8/1 | 100% | 2 × 5/5/5/5 | 0/0/0/0 |
| deep-review | 28.73% | 9/8/1 | 100% | 2 × 5/5/5/5 | 0/0/0/0 |

The accepted run used router mode with no explicit model configuration. Its baseline was imported from the matching baseline-only source bundle; exact dependency and navigation commands, positive token savings, and two not-worse judges per task were all verified. Every historical bad-result category points to this replacement while its original rejected fixture remains preserved for replay.
