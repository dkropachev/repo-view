# Tokenbench design

> Status: current process-planning foundation plus proposed execution architecture. Suite/model/source/Git verification, adapter binding, pair resolution, invocation and process parity, audit-plan serialization, fake/conformance support, and the external process bridge are current. Target-process execution, live MCP read-only verification, Codex, immutable evidence, replay, statistics, and reporting are planned.

## Goals

Tokenbench should make a paired token-efficiency result reproducible and falsifiable. It must:

- isolate one treatment: one read-only `repo_view` MCP registration;
- reject pairs with any other semantic input or configuration delta;
- support a planned Codex integration and other live harnesses through one narrow adapter boundary;
- preserve raw evidence immutably and derive reports offline;
- make usage semantics, quality checks, exclusions, and provenance explicit.

It is not a general agent tournament, prompt optimizer, or live dashboard.

## Terms

- **Run specification:** versioned, harness-neutral semantic inputs shared by both arms.
- **Registration:** the canonical `repo_view` launch metadata and declared read-only flag; the live server/tool surface is not captured yet.
- **Prepared suite:** private, non-serializable state retaining the verified source snapshot, resolved identity, tokenbench executable identity, and adapter capability.
- **Resolved invocation:** the common semantic harness input after adapter model/identity resolution; arms differ only by the registration field.
- **Process pair:** current wrapper-level process plans built once from common state, with candidate argv extended centrally by the MCP suffix.
- **Resolved plan:** serializable audit/transport data containing the verified process pair and its digest; it can validate in-document commitments but cannot build or execute processes.
- **Pair:** current private adapter-bound baseline/candidate invocations plus parity proof; it is the capability that can call `Build` or `Plan`.
- **Attempt pair:** future baseline/candidate executions for one task, repetition, repository state, and run specification.
- **Bundle:** immutable manifest plus content-addressed evidence objects.
- **Replay:** deterministic decoding and analysis of captured objects without a model call.

## Treatment invariant

The adapter resolves common identity during preparation; `Pair.Build` later re-resolves the same common request. Central code, not the adapter, derives the arms from one common invocation `c` and registration `m`:

```text
baseline  = clone(c)
candidate = clone(c)
baseline.mcp_servers  = []
candidate.mcp_servers = [m]
remove(candidate.mcp_servers[0]) == baseline

p = adapter.Build(baseline)             # exactly once, no MCP servers
baseline_process  = clone(p)
candidate_process = clone(p)
candidate_process.argv += adapter.MCPArguments(m)
```

The equality is deep and fail-closed. Baseline has no MCP registrations. Candidate contains exactly one registration in total, named `repo_view`. The current planner computes the selected executable digest and commits the complete code-owned registration digest in the resolved plan. There may be no shared server, replacement, duplicate, alias, or second candidate-only tool.

The MCP tool declarations learned from that registration are a direct consequence of the treatment. No separate prompt text may explain, recommend, or require the tool. Current code marks the registration required/read-only and pins its executable and code-owned argv; proving the live server identity, handshake, and read-only tool surface is planned.

Semantic equality includes, at minimum:

- exact system, developer, user, and task bytes in the same roles and order;
- requested model, expected and resolved `<model>@<immutable-revision>`, reasoning effort, sampling controls, context controls, and tokenizer/accounting contract;
- harness binary/version and adapter executable, control-configuration, effective child-configuration, and version identities;
- canonical tokenbench executable path and digest;
- non-registration arguments, feature flags, routing, and account class; a harness-native encoding of the exact registration is part of the one permitted delta;
- sandbox, permissions, network policy, timeout, limits, locale, clock policy, working directory, and model-visible repository path;
- repository tree/commit, standalone `.git` metadata digest, pinned Git executable/path/digest, and pre-run filesystem state;
- environment, including the complete `PATH` value;
- the native tool inventory; no common MCP registration is permitted.

Arm labels, pair IDs, timestamps, randomized order, and host scheduling facts are evidence metadata. They must not be injected into prompts, environment variables, paths, or other model-visible state.

Physical isolation must preserve semantic paths. A future runner should use fresh isomorphic sandboxes mounted at the same model-visible location, or sequentially restore the same location, rather than leak arm-specific temporary paths.

## Canonical run specification

The current `tokenbench.suite/v1` authors one common prompt, requested model, expected `<model>@<immutable-revision>`, harness executable/digest/kind, canonical Git executable path/digest, read-only permission profile, timeout, repetition count, seed, and full source/base/tree commitment. Source verification invokes only that pinned Git executable; it does not discover Git from ambient `PATH`. The suite deliberately contains no arm fields, opaque harness argument/environment escape hatch, or tool registry. Current code validates and commits the repetition and seed fields through the suite digest but does not schedule repetitions or derive an AB/BA order from them.

The target complete study specification contains no arbitrary arm override. Its logical sections are:

- schema and corpus versions;
- task and prompt object digests;
- immutable repository locator, tree/commit digest, standalone Git-metadata digest, and suite-authored Git-executable identity;
- harness adapter wrapper/child identities and pinned harness binary identity;
- requested model, resolved immutable model revision, and inference settings;
- sandbox, permissions, resource limits, and timeout;
- an allowlisted environment snapshot with secret references separated from values;
- the common native-tool contract, with no authored MCP registry;
- one canonical read-only `repo_view` registration;
- repetition, pairing, randomization, and quality policy;
- usage-decoder and pricing-table identities.

There is intentionally no `baseline_overrides`, `candidate_prompt`, `navigator_policy`, or arm-specific environment block.

Unknown fields are rejected by the current loader. Defaults must be resolved before parity and stored in future evidence so a later harness release cannot reinterpret an old specification.

The current CLI planning flow is `LoadSuite` → `PrepareSuite(adapter)` → `NewRepoViewTool` → `ResolvePair` → `Pair.Plan(ctx)` → `ResolvedPlan.Validate`. `PreparedSuite` retains its verified `source.Snapshot`, resolved `Identity`, tokenbench executable identity, and adapter capability. `Pair.Plan(ctx)` internally calls the bound pair's `Build` exactly once; that build re-resolves identity, renders/verifies the common process once, centrally clones/appends only `MCPArguments`, and then reverifies the tokenbench/harness/MCP executables plus source/Git identity. The plan stores the verified process pair and its digest. No target process is launched.

`ResolvedPlan` intentionally omits the adapter capability even though it contains rendered process data. `DecodePlan` performs strict audit validation only; a decoded plan cannot call `Pair.Build` and must never be accepted as execution authority. A future executor must reload and prepare the authored suite, then use the retained `Pair` capability.

## Component boundaries

```text
suite + prompt + source + adapter
              |
              v
   PrepareSuite: verify source/Git + resolve common identity
              |
              v
 ResolvePair: clone common invocation + add one registration
              |
              v
 Pair.Plan(ctx): call bound Pair.Build exactly once
              |
              v
 Pair.Build: re-resolve -> build once -> clone/append -> reverify
              |
              v
 ResolvedPlan: verified ProcessPair + digest (audit-only)
              |
              v
 target execution/CAS/replay/stats (planned)
```

### Validation core

Owns strict decoding, canonical serialization, digesting, and cross-field validation. It must not know harness command-line details.

### Harness adapter

`PrepareSuite` calls `Resolve` with common state only. `ResolvePair` then constructs and parity-checks both invocations. `Pair.Build` calls `Resolve` again and requires exact identity equality, calls `Build` once with the MCP-free common invocation, and calls `MCPArguments` only with the approved registration. Central code constructs candidate process argv; the adapter never builds candidate independently.

The current boundary is:

```go
type Adapter interface {
    Kind() string
    Resolve(context.Context, ResolveRequest) (Identity, error)
    MCPArguments(context.Context, MCPServer) ([]string, error)
    Build(context.Context, Invocation) (ProcessSpec, error)
    Decode(context.Context, RawExecution) (Observation, error)
}
```

`ResolveRequest` includes common harness/model settings, working directory, source/base/tree identities, pinned Git executable/metadata identities, and the canonical tokenbench executable path/digest, but no arm. `Build` must reject an invocation containing MCP servers. `MCPArguments` returns a nonempty suffix and has no task/prompt input. `Decode` normalizes raw execution supplied by a future target-process runner. The current external process bridge implements these calls; no checked-in adapter executes Codex.

### Parity gate

The current invocation gate validates the exact registration and deep equality after removing it, then commits common/baseline/candidate/prompt/registration digests. The current process gate verifies that adapter output preserves executable, stdin, full environment, working directory, and timeout, and that candidate is a clone with only the MCP argv suffix. Live child-effective configuration and MCP handshake/tool-surface verification remain planned. A harness that cannot expose them is unsupported for live results.

### Source and identity verifier

Current verification requires a clean standalone repository whose `.git` directory, object storage, index, HEAD, tracked paths/modes/raw bytes, local configuration, and absence of alternates/linked state are self-contained. It rejects unsafe index flags, symlinks/submodules, ignored/untracked files, local overrides, transient Git state, and external hard links. It records the source revision/base/tree digest, a stable digest over complete `.git` metadata, and the canonical Git executable path/digest. Preparation also resolves and hashes the canonical tokenbench executable. `Pair.Build` repeats source/Git verification and rehashes that executable after adapter control calls.

The suite names the requested model and expected immutable revision separately. Adapter `Identity.Model` must equal the requested model and `Identity.ModelRevision` must equal the exact expected `<model>@<immutable-revision>` both during preparation and build-time re-resolution.

### Plan authority

`Pair` is the current build authority because it retains the adapter capability in private state. `ResolvedPlan` is a defensive serializable audit view containing semantic invocations, the rendered process pair, and their commitments. Those commitments can be recomputed after decoding, but the plan cannot be converted back into a `Pair` or used to skip source, executable, adapter, or model re-verification.

### Target-process runner (planned)

Will launch the verified `ProcessPair` in fresh sessions, perform the live MCP identity/handshake/read-only surface checks, use a pre-recorded randomized AB/BA order, and record all outcomes. It must not retry silently; a retry is a new linked attempt.

### Capture and evidence store (planned)

Capture writes typed raw bytes to a content-addressed store. A small root manifest links the pair, configuration, events, responses, usage, exits, and checks. Publication is atomic: either the verified manifest becomes visible or the bundle is incomplete and not analyzed.

### Replay, checks, analysis, and reporting (planned)

Replay reads only captured objects, verifies digests, applies a pinned decoder, and writes a new derived bundle linked to its source. Quality checks consume responses and task fixtures without changing raw evidence. Analysis reports raw counters and financial estimates as separate metric families.

## State and side effects

The current verifier treats the worktree, standalone `.git` state, Git executable, and tokenbench executable as immutable inputs and detects drift again during `Pair.Build`. Future model sessions receive no prior transcript. Any harness scratch state must be isolated per run and initialized identically. The future live MCP gate must establish that exposed `repo_view` operations cannot edit the repository, Git metadata, host configuration, or external systems.

Credentials are supplied through the same non-model-visible mechanism to both arms. Secret values are never serialized into a run specification, resolved-configuration artifact, event object, or report.

## Failure model

Failures are data, not omissions:

- **spec failure:** invalid or non-canonical input;
- **parity failure:** any effective delta beyond the registration;
- **adapter failure:** harness unavailable, incompatible, or not auditable;
- **infrastructure failure:** timeout, launch, transport, or capture failure;
- **decoder failure:** raw usage cannot be interpreted by the pinned decoder;
- **quality failure:** response fails a predeclared check;
- **integrity failure:** missing object or digest mismatch.

No token-efficiency winner is declared from an invalid pair. Reports include attempt counts and reasons before any filtered analysis.

## Extension rules

New harnesses implement the same adapter contract; they do not weaken parity. New metrics derive from immutable raw evidence. Schema changes are versioned and old decoders remain available for replay. A new treatment requires a separately named experiment design rather than another candidate override in tokenbench.
