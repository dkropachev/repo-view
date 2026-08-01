# Failed-Case Resolution Suite

- Started: `2026-08-01T04:25:53.72740354Z`
- Finished: `2026-08-01T04:26:30.173416439Z`
- Case manifest SHA-256: `653527c6d4e91430b13f02f1018c179ab99b68d48836ed40fc35961c141e0dc5`
- Resolution manifest SHA-256: `6304fab206a3de317c670dc79fe95203331f9e1766124fb5c945d820d45acd3f`
- Result: **PASS**
- Evidence analysis: `executed`
- Quality aggregation: `executed`
- Cases: **16/16 passed** (13 resolved, 3 accepted)
- Checks: **795/795 passed**

| Level | Case | Resolution | Result | Checks |
| ---: | --- | --- | --- | ---: |
| 1 | `01-simple-explain-accepted` | accepted | **PASS** | 20/20 |
| 2 | `02-simple-review-accepted` | accepted | **PASS** | 20/20 |
| 3 | `03-rejected-incomplete-deep-run` | resolved | **PASS** | 63/63 |
| 4 | `04-rejected-tool-contract-defects` | resolved | **PASS** | 56/56 |
| 5 | `05-rejected-bounded-cost-regression` | resolved | **PASS** | 57/57 |
| 6 | `06-rejected-soft-cap-and-quality` | resolved | **PASS** | 57/57 |
| 7 | `07-rejected-environment-budget-bypass` | resolved | **PASS** | 57/57 |
| 8 | `08-rejected-repo-view-not-used` | resolved | **PASS** | 54/54 |
| 9 | `09-rejected-read-only-budget-file` | resolved | **PASS** | 56/56 |
| 10 | `10-rejected-wrong-dependency-semantics` | resolved | **PASS** | 55/55 |
| 11 | `11-rejected-confirmed-soft-cap` | resolved | **PASS** | 57/57 |
| 12 | `12-rejected-instruction-only-cap` | resolved | **PASS** | 59/59 |
| 13 | `13-rejected-inherited-fd-budget` | resolved | **PASS** | 59/59 |
| 14 | `14-rejected-semantic-call-chain` | resolved | **PASS** | 58/58 |
| 15 | `15-rejected-incomplete-review-path` | resolved | **PASS** | 35/35 |
| 16 | `16-deep-verified-accepted` | accepted | **PASS** | 32/32 |

## Current accepted replacement

Evidence: `runs/router-deep-eight-command-v5-20260801` (`strict-current`).

| Task | Effective-token saving | Calls (total/repo-view/other) | Static quality | Judges C/C/G/A | Issues O/U/B/X |
| --- | ---: | ---: | ---: | ---: | ---: |
| deep-explain | 14.80% | 9/8/1 | 100% | 5/5/5/5 | 0/0/0/0 |
| deep-review | 39.15% | 9/8/1 | 100% | 5/5/5/5 | 0/0/0/0 |

The accepted run used router mode with no model flag, model environment, model name, or reasoning-effort configuration. Every historical bad-result category is linked to this replacement in `resolutions.json`; the original rejected fixture remains preserved for replay.
