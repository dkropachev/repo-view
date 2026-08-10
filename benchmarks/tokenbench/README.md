# Tokenbench

> Status: validation and process-planning foundation. This revision validates suites, pins model/source/adapter/tokenbench-executable identity, resolves the sole-delta pair, renders a verified process pair, and provides `validate`/`plan`. It does not launch model runs or provide live MCP handshake/tool-surface verification, a Codex adapter, immutable evidence, replay, statistics, or reporting commands.

Tokenbench is the proposed reproducible benchmark for measuring whether read-only `repo_view` MCP access changes model token use while preserving answer quality. It is intended to replace one-off shell experiments with an auditable paired design, immutable evidence, harness adapters, and offline replay.

## The experiment

Each attempt is a fresh pair of runs over the same immutable repository state and the same task:

| Arm | Configuration |
| --- | --- |
| Baseline | Canonical run specification, with no MCP server registrations |
| Candidate | The identical specification, with exactly one read-only MCP registration named `repo_view` |

That registration is the sole treatment. The model may receive the tool declarations naturally produced by the MCP handshake. It must not receive candidate-only instructions about navigation or tool use.

The following must remain identical: prompt bytes and roles, requested model, resolved immutable model revision, reasoning settings, harness, adapter, and tokenbench executable identities, permissions and sandbox, working directory and complete source/Git state, executable `PATH`, environment, timeouts, native tool inventory, account/routing settings, feature flags, and all other effective configuration. Baseline has no MCP registrations; candidate's `repo_view` entry is the only one. Run identifiers, timestamps, and randomized order are evidence metadata and are not model-visible inputs.

The current planner proves invocation parity, asks the bound adapter to build the common process exactly once, clones that process, and centrally appends only the adapter's native encoding of the approved registration to candidate argv. It then reverifies the tokenbench, harness, and MCP executables plus source, `.git`, and Git-executable inputs, and commits the rendered pair and its digest in the audit plan. Live MCP handshake, server identity, and read-only tool-surface verification remain planned.

## Intended lifecycle

1. Validate a versioned corpus and canonical run specification.
2. Centrally derive baseline and candidate invocations and prove that their sole semantic difference is the MCP registration.
3. Render the approved common process once, centrally clone/append the MCP suffix, and prove wrapper-level process parity.
4. Run fresh sessions in randomized, counterbalanced order.
5. Capture raw events, usage, responses, effective configuration, and checks into immutable content-addressed evidence.
6. Replay decoding and analysis offline into a new derived bundle.
7. Report token components, quality, uncertainty, failures, and provenance together.

The current foundation covers common suite validation plus steps 2–3 through verified process construction, but it has no versioned corpus and does not launch those processes. Corpus support, steps 4–7, and live harness/MCP verification are target capabilities, not current command guarantees.

## Current foundation

The checked-in Go foundation currently provides:

- `tokenbench.suite/v1` decoding with unknown/duplicate-field rejection and schema documentation;
- an explicit requested model plus an expected resolved identity in `<model>@<immutable-revision>` form;
- prompt, harness executable, canonical tokenbench executable, full source and base revisions, raw tracked-tree and standalone `.git` metadata commitments, plus suite-authored canonical Git executable path/SHA-256;
- rejection of ambiguous model aliases, authored opaque harness arguments/environment, dirty or ignored source files, linked worktrees, Git alternates, unsafe index state, and local Git overrides;
- `PrepareSuite` retention of the verified source snapshot, resolved adapter identity, and adapter capability, plus `NewRepoViewTool` pinning of the actual MCP executable;
- code-owned construction of the sole `repo_view` registration, marked required and read-only in the plan; live server identity/tool-surface verification remains planned;
- defensive pair resolution, deep common-invocation comparison, and a serializable parity proof;
- `Pair.Build`, which re-resolves adapter identity, builds the common process once, centrally clones/appends `MCPArguments`, and reverifies tokenbench/harness/MCP executables plus source/Git identity before returning `ProcessPair`;
- `Pair.Plan(ctx)`, which obtains that pair only through the retained adapter-bound capability and stores both the rendered processes and their digest in `ResolvedPlan`;
- `validate` for suite validation plus suite/prompt digest output, and `plan` for full preparation, verified process rendering, and exclusive audit-plan creation;
- the `Kind`/`Resolve`/`MCPArguments`/`Build`/`Decode` adapter interface, deterministic fake, shared conformance helper, and external process bridge.

Neither subcommand launches the planned target processes. A generated or decoded `ResolvedPlan` contains the verified process specifications, but remains audit/transport data rather than benchmark evidence, a benchmark result, or execution authority. Code that still retains the original adapter-bound `Pair` may build again; the plan alone cannot. Starting from only serialized plan data requires reloading and preparing the suite to obtain fresh authority.

From the repository root, the implemented command surface is:

```sh
go run ./benchmarks/tokenbench/cmd/tokenbench validate --suite SUITE.json
go run ./benchmarks/tokenbench/cmd/tokenbench plan \
  --suite SUITE.json \
  --repo-view-mcp /absolute/path/to/repo-view \
  --out PLAN.json
```

`plan` requires pinned harness/MCP/Git executables, the exact expected model revision, and a clean self-contained source repository with standalone `.git`. It calls `Pair.Plan(ctx)`, which internally builds the pair once and commits the rendered processes before emitting the audit plan. Omitting `--out` writes JSON to standard output; when `--out` names a file, that path must not already exist. No `run`, `replay`, `analyze`, `stats`, or `report` subcommand exists yet.

The built-in `fake` adapter supports deterministic offline planning/tests. Any other `harness_kind` requires `--adapter-command /absolute/path/to/adapter`; the command must implement `tokenbench.external-adapter/v1` as documented in [docs/adapter-authoring.md](docs/adapter-authoring.md). Codex is not yet a built-in adapter.

## Current and planned structure

```text
benchmarks/tokenbench/
  AGENTS.md
  README.md
  DESIGN.md
  CONTRIBUTING.md
  cmd/tokenbench/       # current validate/plan CLI
  harness/              # current interface, fake, conformance, process bridge
  source/               # current immutable-source verification
  docs/
    methodology.md
    adapter-authoring.md
    evidence-format.md
    migration.md
  corpus/              # planned versioned tasks and expected facts
  schemas/             # current suite schema; evidence schemas planned
  suites/smoke/        # current placeholder; corpus planned
  testdata/            # current process-adapter fixtures; replay fixtures planned
```

The current CLI name and package locations above are established. Directories labeled planned are roadmap, not available workflow surfaces.

## Roadmap

- **Stage 0 — contracts (current):** methodology, design, evidence, adapter, and migration documentation.
- **Stage 1 — validation/process-planning core (current):** strict types/schema, full source/Git verification, adapter/model identity binding, sole-delta pair construction, process rendering, parity proofs, `validate`/`plan`, fake/conformance support, and the external process bridge.
- **Stage 2 — live execution and evidence (planned):** target-process runner, MCP handshake/read-only tool-surface enforcement, raw capture, and immutable CAS publication.
- **Stage 3 — first live/model-backed harness (planned):** Codex adapter, provider event/usage decoding, and live conformance fixtures.
- **Stage 4 — replay and study workflow (planned):** offline replay, reviewed corpus, blinded quality checks, paired statistics, and report generation.
- **Stage 5 — legacy transition (planned):** provenance-preserving import and an optional compatibility entry point for old experiment workflows.

A stage is complete only after its implementation and tests are checked in. Later-stage prose is directional.

## Reading guide

- [DESIGN.md](DESIGN.md) defines boundaries, parity, and failure behavior.
- [docs/methodology.md](docs/methodology.md) defines the experimental estimand and analysis.
- [docs/adapter-authoring.md](docs/adapter-authoring.md) defines harness conformance.
- [docs/evidence-format.md](docs/evidence-format.md) defines the proposed immutable bundle model.
- [docs/migration.md](docs/migration.md) explains how legacy evidence remains available without being mistaken for conformant evidence.
- [CONTRIBUTING.md](CONTRIBUTING.md) contains change and review expectations.

## Non-goals

Tokenbench does not tune prompts separately for each arm, prove that every task benefits from repository navigation, convert price into “effective tokens,” or retroactively make legacy oracle-assisted experiments comparable. It is designed to make a narrow treatment and its evidence inspectable.
