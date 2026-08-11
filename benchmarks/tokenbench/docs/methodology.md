# Methodology

> Status: the canonical study policy, blind paired-evaluation boundary, completeness checks, and paired analysis described below are current in `benchmarks/tokenbench/study`. Integration of that study layer with the CLI/evidence workflow and a published conformant dataset remain planned. The remaining protocol text states requirements for conformant studies, not a claim that such a dataset exists.

## Implemented study and analysis boundary

The harness-neutral `study` package implements five versioned v1 objects: the study policy, authenticated-input manifest, blind evaluation packet, evaluator output, and analysis report. Policy JSON is accepted only in its byte-canonical required-field form, with duplicate, unknown, reordered-equivalent, and trailing content rejected. Tasks, fact items, rubric items, and permitted exclusion codes are predeclared and strictly ordered. Included tasks fix repetitions; excluded or pilot tasks remain in the corpus with explicit reasons. Policies commit separate blinding, randomization, and bootstrap seeds and cannot authorize a decision from only one pair or one repository cluster.

`BlindPair` derives the anonymous answer assignment and nonce from the committed secret seed plus the predeclared task/repetition. The evaluator receives only final answer text, objective facts, rubric criteria, anonymous labels, and packet commitments. Treatment names, execution order, repetition, pair/run identity, traces, tool metadata, failure metadata, and token counters are absent. Evaluator output must echo the nonce and commitment and retain exact answer/item order. Verification maps scores back to treatments only after these checks; unverified scores are not accepted by analysis.

`Analyze` requires one ordered record for every included task/repetition. Never-attempted pairs, arm failures, missing final answers, missing quality judgments, and permitted exclusions require explicit reasons; omitting a declared record is an error. Only nonexcluded pairs with complete token observations for both arms enter token statistics. Missing answers still remain visible and prevent quality/decision gates from silently passing.

The v1 primary token total is raw input plus raw output. Cached input, cache-write input, output, reasoning, and an optional provider-native total are reported independently; cached and reasoning subsets are not added twice, and the provider total never replaces the preregistered primary metric. Provider-total paired summaries include only pairs where both arms reported that counter and disclose missingness explicitly. The API accepts no prices or cache weights. Each component reports paired candidate-minus-baseline differences and ratios, with zero/zero and positive-over-zero denominators counted separately rather than assigned invented finite ratios.

Pair-level reports include sums, means, medians, quartiles, extrema, deltas, and ratios. Inference first averages repetitions within each task, then averages tasks within each preregistered repository cluster. It uses exhaustive two-sided repository-cluster sign flips through the predeclared small-corpus limit, otherwise deterministic committed-seed Monte Carlo with a plus-one p-value. A separately committed-seed repository-cluster percentile bootstrap supplies primary-token and quality mean-difference intervals. A token-efficiency decision is produced only if every predeclared coverage, failure, exclusion, missingness, absolute-quality, quality-noninferiority, token-reduction, nonzero-baseline, and p-value gate passes; otherwise the result is explicitly not demonstrated or inconclusive.

## Research question

For a fixed repository-understanding task, model, harness, and execution configuration, what is the paired change in model token use and answer quality when the model is offered one read-only `repo_view` MCP server?

The treatment is availability of that MCP registration, not a prompt that tells the model how to navigate. Tool selection and non-use are legitimate candidate outcomes.

## Experimental unit and estimand

The experimental unit is a task/repository-state/repetition pair. Its baseline and candidate runs start fresh and share one canonical run specification.

The primary token estimand is preregistered as the paired change in raw input plus raw output under one pinned provider accounting contract. A provider-reported total is a separate optional paired metric when both arms report it; it is never inferred from components or substituted for the primary estimand.

Always preserve and report the native components:

- reported input tokens;
- reported cached input tokens and whether they are a subset of input;
- reported output tokens;
- reported reasoning tokens and whether they are a subset of output;
- provider-reported total, if present;
- missing or unknown counters.

Do not add cached or reasoning subsets twice. Do not apply a cache discount to create “effective tokens.” Monetary cost is a secondary derived metric computed from a separately versioned model price table.

Recommended paired summaries are absolute difference, percent change relative to baseline, ratio of sums, median paired ratio, and a confidence interval over paired task-level effects. State the primary summary before inspecting results.

## Sole-delta protocol

Baseline and candidate must have byte-identical prompts in the same roles and order. They must use the same requested model, exact resolved `<model>@<immutable-revision>`, reasoning settings, harness identity, adapter executable/control/child-configuration identities, canonical tokenbench executable identity, sandbox, permissions, executable paths, full environment including `PATH`, timeout, working path, immutable repository tree and standalone `.git` state, suite-authored pinned Git executable path/digest, native tool inventory, feature settings, routing/account class, and pre-run state. Baseline has no MCP registrations.

The candidate has exactly one MCP registration in total: the canonical read-only server named `repo_view`. No navigator appendix, tool-use policy, hint, wrapper, environment variable, or candidate-only executable is allowed. The tool declarations resulting from the MCP handshake are part of that one treatment.

A pair is eligible only if a fail-closed comparison proves that removing the registration from candidate produces baseline exactly. The current foundation proves this for semantic invocations and wrapper-level process plans by building common state once and centrally appending only the registration encoding. The current built-in live Codex path additionally fail-closes on MCP handshake/server identity, the read-only tool surface, and child-effective configuration; any alternate live integration needs equivalent proof before treating a pair as conformant. Unavoidable facts such as attempt ID, timestamp, and randomized position are evidence metadata and must remain invisible to the model.

## Corpus construction

Tasks should be selected before live runs and stored with immutable repository and prompt digests. The corpus should span realistic repository-understanding work, for example locating behavior, tracing a call path, explaining a subsystem, and reviewing a bounded change. It should not disclose oracle facts to one arm.

Each task record should contain:

- repository locator, full base/head object IDs, tracked-tree digest, standalone `.git` metadata digest, and Git executable identity;
- prompt bytes and role placement;
- task family and declared difficulty strata;
- machine-checkable expected facts where feasible;
- a versioned arm-blind rubric for judgments that need one;
- exclusions known before execution.

Rejected or pilot tasks must remain listed with reasons. Several prompts against one commit improve coverage, but they are not independent repository samples; analysis must retain task and repository clustering.

## Pairing, order, and repetition

Use new model sessions for every arm and repetition. Do not reuse a baseline across candidate retries and do not share transcripts or summaries between arms.

Generate pair order from a recorded seed before execution. Counterbalance AB and BA positions within task strata to reduce time and warm-state effects. Restore and reverify the same repository, `.git`, Git executable, tokenbench executable, harness/MCP executables, adapter identity, and harness preconditions immediately before every arm. If an infrastructure failure is retried, record a new linked attempt; never replace the failed record.

The repetition count, stopping rule, and any minimum quality threshold must be fixed before examining comparative token outcomes. Sequential additions after observing a favorable result require a new declared study phase.

## Quality

Token reduction is interpretable only alongside answer quality. Prefer deterministic factual checks and repository-grounded expected facts. When human or model judgment is necessary:

- remove arm labels and randomize answer order;
- use the same rubric and judge configuration for both arms;
- do not expose tool traces, token counts, or source-arm metadata;
- preserve individual judgments and disagreements;
- report quality pass rate and score difference independently of tokens.

A suggested analysis reports all valid pairs, then a preregistered quality-qualified subset. It must not silently discard quality failures. A candidate cannot be called more efficient merely because it produced a shorter wrong answer.

## Usage and cost interpretation

Usage decoders are versioned by harness event schema, adapter decoder identity, requested model, resolved immutable model revision, and provider accounting contract. Raw events remain the authority. A decoder that encounters an unknown event or ambiguous counter records an unknown value or fails the derived analysis; it does not guess.

Cost reports pin currency, region if relevant, model price version, effective date, and rules for cached input and reasoning. Because prices change, replay with a new price table creates a new derived bundle without modifying token evidence.

## Statistical reporting

Report task-level paired observations, not only aggregate percentages. Include:

- attempted, parity-valid, completed, quality-passing, and analyzed pair counts;
- the complete failure and exclusion table;
- baseline and candidate token-component distributions;
- paired effects with uncertainty;
- order, task-family, requested/resolved model revision, harness/adapter identity, and repository strata;
- sensitivity to preregistered quality thresholds;
- cost results separately from token results.

For multiple tasks per repository or repeated runs per task, use a cluster-aware bootstrap or a hierarchical model rather than treating every run as independent. With a small corpus, emphasize intervals and raw paired results over significance labels.

## Validity threats

Known threats include model nondeterminism, provider-side caching or routing, hidden model-revision drift, tokenbench/harness/adapter updates, repository or `.git` drift, order effects, task selection, judge error, and MCP-server warm state. Pin and capture what can be controlled; stratify or disclose the rest. Wrapper-level process parity alone does not prove live child-effective parity. The strict checks reduce confounding but do not turn one repository or model revision into a universal conclusion.

## Result language

Conclusions should name the corpus, repository/tree/Git identities, tokenbench executable identity, requested and resolved model revision, harness plus adapter wrapper/child identities, period, quality rule, and evidence bundle. Prefer “under this preregistered configuration” to broad claims that repository navigation always saves tokens.
