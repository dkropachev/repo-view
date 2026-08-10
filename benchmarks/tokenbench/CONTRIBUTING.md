# Contributing to tokenbench

> Tokenbench currently validates suites, binds source/model/adapter/tokenbench-executable identity, and renders and commits verified process pairs through `Pair.Plan(ctx)`. Target-process execution, live MCP read-only verification, Codex, immutable evidence, replay, statistics, and reporting remain planned. Do not advertise a planned command without checked-in implementation and tests.

## Start with the invariant

A baseline/candidate pair must be identical in every semantic input and configuration value except that baseline has no MCP registrations and candidate has exactly one: a read-only registration named `repo_view`.

Do not introduce arm-specific prompts, policies, hints, wrappers, environment variables, `PATH` entries, permissions, timeouts, feature flags, repository setup, or model settings. If a useful experiment needs one of those deltas, it is a different experiment and should not be represented as tokenbench evidence.

## Change workflow

1. Identify the contract affected: corpus, schema, parity, adapter, evidence, decoder, quality, analysis, report, or migration.
2. Update the relevant design document before or with implementation.
3. Keep raw capture separate from derived interpretation.
4. Add a narrow fixture that demonstrates the behavior and a negative fixture for invalid state.
5. Run the checks appropriate to the implemented stage and inspect the complete diff.
6. State current limitations in the pull request; avoid roadmap language in release claims.

## Required review by change type

### Run specification or schema

- Reject unknown fields and ambiguous defaults.
- Require both the explicit requested model and exact expected `<model>@<immutable-revision>`.
- Require a canonical absolute Git executable path and matching SHA-256; source verification must not discover Git through ambient `PATH`.
- Preserve canonical serialization and stable digests.
- Version incompatible changes.
- Demonstrate that no general arm override can encode a second treatment.
- Include malformed, future-version, and round-trip fixtures.

### Parity

- Test a valid pair with only the canonical registration added.
- Test every protected category: prompt, requested/resolved model identity, adapter identity digests, non-registration arguments, environment, `PATH`, permissions, tools, timeouts, working path, source tree, `.git` metadata, Git executable, and tokenbench executable.
- Test duplicate, aliased, replaced, writable, and mismatched `repo_view` registrations.
- Emit actionable paths and both value digests without leaking secret values.
- Build the common process once, clone centrally, append only `MCPArguments`, and fail closed on any other rendered-process delta.
- Treat live handshake/tool-surface parity as planned until implemented.

### Harness adapter

Follow [docs/adapter-authoring.md](docs/adapter-authoring.md). `Resolve` is common and deterministic; `Build` accepts only the MCP-free common invocation; `MCPArguments` accepts only the approved registration. Central tokenbench rendering, not the adapter, clones candidate and appends the suffix. Changes to the process bridge must test protocol shape, deterministic cwd/environment, wrapper-owned and child-owned identity digests, executable drift, keyed control-environment commitment, timeout/held-pipe behavior, and platform-specific process cleanup.

### Source verification and authority

- Keep the source worktree, standalone `.git` metadata, Git executable identity, and tokenbench executable identity bound into the common invocation.
- Test dirty/ignored state, linked worktrees, alternates, unsafe index flags/configuration, symlinks/submodules, hard links, transient metadata, and mutation races.
- Preserve `PreparedSuite`/`Pair` as the private adapter-bound build authority.
- Never add an execution path from `ResolvedPlan` or `DecodePlan`; embedded rendered processes do not change the decoded plan's audit-only authority.

### Evidence or replay

Follow [docs/evidence-format.md](docs/evidence-format.md). Evidence/CAS/replay are planned. When implemented, test partial writes, digest corruption, missing parents, schema mismatch, deterministic replay, and preservation of unknown raw events. Never update a captured object in place or mistake an audit plan for captured evidence.

### Metrics or quality

Predeclare metric semantics and denominator changes. Keep provider-reported token components, normalized non-overlapping totals, and monetary cost separate. Quality checks must be arm-blind where possible and must not use candidate traces as an answer key.

### Documentation

Say whether behavior is current, planned, or historical. An example command must be marked proposed until the binary and integration test exist. Keep legacy results visibly classified as non-conformant unless strict parity is proven by their original evidence.

## Reproducibility checklist

Before accepting a live benchmark implementation or result, reviewers should be able to answer yes to all of these:

- Are prompt bytes and role ordering identical?
- Are requested model, resolved immutable revision, reasoning, harness, adapter wrapper/child identities, sandbox, permissions, timeout, environment, and full `PATH` identical?
- Are repository tree, standalone `.git` metadata, Git executable identity, tokenbench executable identity, and model-visible path identical?
- Does baseline have zero MCP registrations and candidate exactly one canonical read-only `repo_view` registration?
- Does removing that registration make the resolved configurations deeply equal?
- Was the common process rendered exactly once and candidate created only by central clone plus the approved MCP suffix?
- Was adapter identity re-resolved unchanged before rendering, then were tokenbench/harness/MCP executables and source/Git identity reverified after adapter control calls?
- Are both sessions fresh and their order randomized or counterbalanced?
- Are all attempts, exits, raw events, usage semantics, and quality outcomes preserved?
- Can analysis replay without credentials, a harness, a model, or the live repository?
- Are raw token counts reported without an arbitrary cache weight?
- Are pricing assumptions versioned separately?

## Verification

For documentation changes, use repository diff inspection, `git diff --check`, relative-link checks, and trailing-whitespace checks. For foundation code, run focused tokenbench tests plus the repository's normal Go suite. Live model calls must not be required for unit, protocol, source-verifier, or future replay tests.

Do not regenerate, delete, or rewrite legacy evidence as part of routine verification.
