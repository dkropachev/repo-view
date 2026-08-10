# Tokenbench contributor instructions

This file applies to `benchmarks/tokenbench/` and all descendants.

## Status

Tokenbench is a staged benchmark foundation. Current code validates authored suites, pins requested and expected immutable model identity plus the tokenbench executable identity, verifies the full source/Git state, binds one adapter into `PreparedSuite`, resolves a strict pair, and uses `Pair.Plan(ctx)` to render, verify, embed, and digest a process pair in an audit plan. It does not launch either target process or provide a live Codex adapter, MCP handshake/tool-surface verification, immutable evidence, replay, statistics, or reporting. Label all such roadmap behavior as planned.

## Non-negotiable A/B invariant

Every semantic input and configuration value visible to the harness, model, tools, and repository must be identical between a paired baseline and candidate run, with one exception:

- the baseline has no MCP server registrations;
- the candidate has exactly one MCP server registration in total: a read-only server named `repo_view`.

The registration and the MCP tool declarations that follow from it are the entire treatment. Do not add any candidate-only prompt, system/developer appendix, navigation policy, hint, answer template, wrapper behavior, executable search path, environment variable, feature flag, permission, timeout, or repository preparation. In particular, do not put a `repo_view` CLI on only one arm's `PATH`.

A change that cannot preserve this invariant is not a tokenbench-compatible benchmark change. Fail closed rather than producing a result.

## Documentation rules

- Use **current** for checked-in, verified behavior and **planned** or **proposed** for roadmap behavior.
- Keep the invariant above consistent across all documents.
- Separate raw token counters from price-derived cost. Never hide a cache multiplier inside a token total.
- Treat legacy `experiments/lsp-replacement` results as historical, non-conformant evidence unless a record proves strict parity.
- Do not present an illustrative schema or command line as an implemented contract.

## Adapter rules

Central pair resolution is the sole semantic arm-dependent step: it adds the canonical `repo_view` registration for candidate while leaving baseline with none. `Pair.Build` calls `Adapter.Build` once for the common process, clones that process centrally, and appends only the nonempty suffix returned by `Adapter.MCPArguments` for the approved registration. An adapter must not build candidate independently or introduce another delta. Requested model, resolved `<model>@<immutable-revision>`, adapter identity, executable, non-registration arguments, environment, permissions, working directory, source/Git identity, timeout, and task bytes remain identical.

`Resolve` receives only common state and must return a deterministic identity. `Build` accepts only the common invocation; `MCPArguments` accepts only the approved MCP registration. If a harness cannot satisfy those boundaries or cannot make effective configuration auditable, mark it unsupported.

## Authority and verification rules

- `PreparedSuite` retains the verified source snapshot, resolved adapter identity, and live adapter capability.
- `Pair.Build` must re-resolve identity, render the common process once, append centrally, and reverify the harness executable, MCP executable, tokenbench executable, source tree, standalone `.git` metadata, and pinned Git executable.
- `Pair.Plan(ctx)` must obtain its rendered processes only through the bound pair's `Build`; it never accepts caller-authored processes. The plan commits both the process pair and its digest.
- A serialized or decoded `ResolvedPlan`, including its rendered process data, is audit/transport data only. `Validate` or `DecodePlan` must never grant execution authority; starting from plan data alone requires reloading and preparing the suite to obtain a new adapter-bound `Pair`.
- The current `ReadOnly` field is a declaration. Live MCP server identity, handshake, and exposed tool-surface enforcement remain planned and must not be described as current.
- The external adapter protocol is `tokenbench.external-adapter/v1`; document its envelopes and identity-digest ownership exactly as implemented.

## Future evidence rules

- Evidence is append-only and content-addressed.
- Raw capture precedes analysis; derived results reference their source object digests.
- Replay never rewrites the source bundle and never invokes a live model.
- Record failures and excluded attempts instead of deleting them.
- Never store credentials or secret environment values in evidence.

## Changes and verification

Keep schema, source verification, parity, adapter, process rendering, evidence, and reporting responsibilities separate. Changes to parity or rendering require positive and negative tests; future evidence code also requires corruption tests. Documentation-only changes should pass `git diff --check`, link/whitespace checks, and review for accidental implementation claims.

Do not edit generated or legacy evidence to make old results conform. Migration must preserve provenance and limitations.
