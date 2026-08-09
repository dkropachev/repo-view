# LSP-Replacement Regression Suite

- Command: `replay`
- Started: `2026-08-09T02:15:38.049487284Z`
- Finished: `2026-08-09T02:16:18.454675807Z`
- Manifest SHA-256: `9746a218ce972b89ffd62373dc4f1a56df9c61f1ab076169405363a83059e2e2`
- Result: **PASS**
- Evidence analysis: `executed`
- Quality aggregation: `executed`
- Cases: **16/16 passed**

A rejected case passes replay when its retained rejection signature is reproduced; it is not an accepted model result.

| Level | Case | Expected | Status | Checks |
| ---: | --- | --- | --- | ---: |
| 1 | `01-simple-explain-accepted` | accepted | **PASS** | 25/25 |
| 2 | `02-simple-review-accepted` | accepted | **PASS** | 25/25 |
| 3 | `03-rejected-incomplete-deep-run` | rejected | **PASS** | 7/7 |
| 4 | `04-rejected-tool-contract-defects` | rejected | **PASS** | 7/7 |
| 5 | `05-rejected-bounded-cost-regression` | rejected | **PASS** | 8/8 |
| 6 | `06-rejected-soft-cap-and-quality` | rejected | **PASS** | 8/8 |
| 7 | `07-rejected-environment-budget-bypass` | rejected | **PASS** | 7/7 |
| 8 | `08-rejected-repo-view-not-used` | rejected | **PASS** | 8/8 |
| 9 | `09-rejected-read-only-budget-file` | rejected | **PASS** | 7/7 |
| 10 | `10-rejected-wrong-dependency-semantics` | rejected | **PASS** | 7/7 |
| 11 | `11-rejected-confirmed-soft-cap` | rejected | **PASS** | 8/8 |
| 12 | `12-rejected-instruction-only-cap` | rejected | **PASS** | 8/8 |
| 13 | `13-rejected-inherited-fd-budget` | rejected | **PASS** | 8/8 |
| 14 | `14-rejected-semantic-call-chain` | rejected | **PASS** | 8/8 |
| 15 | `15-rejected-incomplete-review-path` | rejected | **PASS** | 5/5 |
| 16 | `16-deep-verified-accepted` | accepted | **PASS** | 22/22 |

## Accepted evidence

- Simple explanation: `runs/router-token-performance-simple-explain-v3-20260808` (`strict-current`).
- Simple review: `runs/router-token-performance-simple-review-v2-20260808` (`strict-current`).
- Deep explanation/review: `runs/router-token-performance-deep-v6-20260808` (`strict-current`).
- Deep generation used router mode, a baseline-first imported control, exact verified navigation, and two judges per task.

## Failures

None.
