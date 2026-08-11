# Tokenbench study library

The `study` package currently implements the preregistered, harness-neutral analysis boundary. It does not launch runs, invoke a particular model harness, publish evidence, or provide a CLI command. Those integrations remain separate work.

## Current contracts

- `Policy` / `DecodePolicy` implement byte-canonical `tokenbench.study-policy/v1`. The policy fixes the complete sorted task corpus, repetitions, retained pilot/excluded tasks and reasons, objective facts, rubric items, thresholds, inference settings, and domain-separated seed commitments before comparative outcomes are analyzed.
- `InputManifest` / `LoadAuthenticatedCorpus` implement byte-canonical `tokenbench.study-inputs/v1`. Every included task/repetition is bound to an authenticated capture or replay root, or to one explicit preregistered not-attempted reason. Run identity, task, prompt, repetition, and common execution configuration are derived from verified evidence rather than caller-authored summaries.
- `BlindPair` accepts only the two final answers plus their preregistered task/repetition. It verifies the secret blinding seed, derives the label assignment and nonce deterministically, and emits `tokenbench.blind-evaluation/v1`. The packet contains no treatment, run order, repetition, pair ID, traces, token counters, or failure metadata.
- `Evaluator` receives only that packet. `VerifyEvaluation` requires exact label/item order and an echoed nonce and packet commitment before it returns a treatment-mapped `PairedQuality`. Objective facts are binary; rubric scores are bounded integral values. Analysis rejects scores not produced by this verifier.
- `BuildPairRecords` derives the complete ordered analysis matrix only from an authenticated corpus. Verified quality packets must commit the exact authenticated answers; exclusions, failures, counters, missing answers, and not-attempted slots cannot be supplied or overridden by report callers.
- `Analyze` requires exactly one ordered `PairRecord` for every included task/repetition. A missing attempt, failed arm, missing answer, unavailable judgment, or permitted post-attempt exclusion remains an explicit record with a reason. Omitted, duplicate, reordered, or extra records fail closed.
- `tokenbench.study-analysis/v1` starts with attempted/failure/exclusion/completeness counts. It then reports input, cached input, cache-write input, output, reasoning, primary input-plus-output, and optional provider-native totals independently. Provider totals include only both-present pairs and disclose missingness; they never replace the primary metric. Cached and reasoning subsets are never added again, and no price or cache-discount input exists.

## Statistical rules

Pair-level component reports include baseline/candidate distributions, candidate-minus-baseline deltas, ratio distributions, ratio of sums, mean, median, quartiles, and extrema. A positive value divided by a zero baseline and a zero/zero pair are counted separately; neither is assigned an invented finite paired ratio. A zero aggregate baseline makes the ratio of sums undefined.

Inference first averages repetitions within each task and then tasks within each preregistered repository cluster. Up to the preregistered small-sample limit, the two-sided repository-cluster sign-flip p-value enumerates every assignment. Above it, a deterministic SHA-256 counter stream drives the preregistered Monte Carlo sample count and the report uses the conservative plus-one p-value. The repository-cluster percentile bootstrap uses a separately committed seed and preregistered confidence/sample settings for primary-token and quality mean-difference intervals.

A `demonstrated` token-efficiency decision requires every preregistered gate: minimum complete pairs and tasks, maximum failures/exclusions/not-attempted/missing answers, enough verified quality pairs/tasks, candidate answer-threshold pass rate, quality noninferiority confidence bound, a nonzero baseline total, minimum token reduction, and the randomization p-value threshold. Otherwise the result is `not_demonstrated` only when all eligibility/quality gates passed, or `inconclusive` when they did not. Policies cannot authorize a one-pair or one-task claim.

The JSON schemas in `schemas/` document the five v1 wire objects. Go validation is stricter where JSON Schema cannot express sorted uniqueness, cross-field totals, packet-to-policy equality, committed-seed verification, authenticated evidence lineage, or evaluator label/item correspondence.
