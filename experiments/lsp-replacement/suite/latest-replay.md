# LSP-Replacement Regression Suite

- Command: `replay`
- Started: `2026-08-01T04:25:13.101064917Z`
- Finished: `2026-08-01T04:25:48.090528085Z`
- Manifest SHA-256: `653527c6d4e91430b13f02f1018c179ab99b68d48836ed40fc35961c141e0dc5`
- Result: **PASS**
- Evidence analysis: `executed`
- Quality aggregation: `executed`
- Cases: **16/16 passed**

A rejected case passes replay when its retained rejection signature is reproduced; it is not an accepted model result.

| Level | Case | Expected | Status | Checks |
| ---: | --- | --- | --- | ---: |
| 1 | `01-simple-explain-accepted` | accepted | **PASS** | 11/11 |
| 2 | `02-simple-review-accepted` | accepted | **PASS** | 11/11 |
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
| 16 | `16-deep-verified-accepted` | accepted | **PASS** | 18/18 |

## Accepted evidence

- Simple explanation: `runs/router-no-model-config-simple-explain-v2-20260731` (`strict-current`).
- Simple review: `runs/router-no-model-config-simple-review-final-20260731` (`strict-current`).
- Deep explanation/review: `runs/router-deep-eight-command-v5-20260801` (`strict-current`).
- Deep manifest: `model_mode=router`, `model_configuration=none`.

## Failures

None.
