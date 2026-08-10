# Harness adapter authoring

> Status: current adapter and external-process protocol contract. The interface, fake adapter, conformance helper, process bridge, and wrapper-level process parity are implemented. Target-process execution, live MCP handshake/read-only tool-surface verification, raw capture, and Codex are planned.

## Treatment ownership

An adapter resolves and renders common harness state. It does not construct or independently render experimental arms.

Current central code owns the only semantic arm difference:

- baseline has an empty, non-nil MCP registration array;
- candidate has one array element, the code-owned `repo_view` registration;
- removing candidate element zero makes the semantic invocations deeply equal.

`Pair.Build` calls `Build` exactly once with the MCP-free common invocation. It clones the returned `ProcessSpec` and appends only the nonempty argv suffix returned by `MCPArguments(candidate.MCPServers[0])`. The adapter never receives candidate prompt, rubric, or arm label through `MCPArguments`.

No adapter may append a navigator prompt, add a tool-use policy, choose an arm-only wrapper, alter `PATH` or another environment value, change permissions, or inject a second candidate-only setting.

## Current Go interface

```go
type Adapter interface {
    Kind() string
    Resolve(context.Context, ResolveRequest) (Identity, error)
    MCPArguments(context.Context, MCPServer) ([]string, error)
    Build(context.Context, Invocation) (ProcessSpec, error)
    Decode(context.Context, RawExecution) (Observation, error)
}
```

The methods are deterministic for equal input.

- `Kind` returns the suite-selectable adapter kind.
- `Resolve` binds common requested state to a concrete adapter, harness, model, immutable model revision, reasoning effort, and decoder identity.
- `MCPArguments` serializes only one already-approved registration into a nonempty harness-native argv suffix.
- `Build` renders only the common invocation and must reject nonempty `MCPServers`.
- `Decode` normalizes raw execution supplied by a future runner; it does not execute the target process.

## Current preparation and build sequence

1. `PrepareSuite` verifies the harness executable and full source/Git snapshot, resolves and hashes the canonical tokenbench executable, checks adapter kind, and calls `Resolve` with an arm-free `ResolveRequest`.
2. The returned `Identity` must match requested harness digest, model, expected immutable model revision, and reasoning effort. `PreparedSuite` privately retains that identity, source snapshot, tokenbench executable identity, and exact adapter capability.
3. `ResolvePair` clones one common invocation, adds the sole candidate registration centrally, and proves semantic parity.
4. `Pair.Build` recomputes parity, calls `Resolve` again with the same common request, and requires exact `Identity` equality.
5. `Pair.Build` calls `Build` once, verifies the returned executable, prompt stdin bytes, complete environment, working directory, and timeout against common input, then centrally clones the process and appends `MCPArguments`.
6. After all adapter control calls, `Pair.Build` reverifies the tokenbench, harness, and `repo_view` executables, source tree, standalone `.git` metadata, and pinned Git executable before returning `ProcessPair`.

The current `plan` CLI calls `Pair.Plan(ctx)`. That method calls the bound pair's `Build` exactly once, stores the returned process pair and its digest in `ResolvedPlan`, and does not launch either target process.

A serialized or decoded plan is audit/transport data only. It contains rendered process specifications but has no adapter capability; `DecodePlan` cannot grant build or execution authority.

## Resolve request

`ResolveRequest` contains no arm field. It binds:

- the complete common environment;
- harness executable path and SHA-256;
- requested model and exact expected `<model>@<immutable-revision>`;
- reasoning effort, permission profile, and common developer instructions;
- working directory;
- source head, base, and tracked-tree digest;
- suite-authored canonical Git executable path/digest and verified standalone `.git` metadata digest;
- canonical tokenbench executable path and SHA-256;
- timeout.

The current suite path passes an empty environment map and no opaque authored harness arguments. Future harness-specific configuration belongs behind a pinned adapter identity, never in an arm override.

## Identity and digest ownership

Every valid `Identity` includes:

- `kind` and `adapter_version`;
- `adapter_executable_sha256`;
- `adapter_control_config_sha256`;
- `adapter_config_sha256`;
- harness `executable_sha256` and `executable_version`;
- resolved `model` and immutable `model_revision`;
- `reasoning_effort`;
- `decoder_schema`.

For an in-process adapter, the adapter supplies all three adapter commitments. For the external process bridge:

- the wrapper computes and overwrites `adapter_executable_sha256` from the exact external adapter executable bytes;
- the wrapper computes and overwrites `adapter_control_config_sha256` from its private launch/control configuration;
- the child supplies `adapter_config_sha256`, which must commit its effective child-side adapter configuration;
- the child supplies the remaining identity fields, which the wrapper and tokenbench validators check.

The suite's requested model and expected revision are separate inputs. Resolved `Identity.Model` must equal the requested model, and `Identity.ModelRevision` must equal the exact expected `<model>@<immutable-revision>`. `Pair.Build` repeats resolution and requires the whole identity to be unchanged.

## Process requirements

`Build` receives the already-approved common `Invocation` with zero MCP registrations. It must return a `ProcessSpec` whose:

- `argv[0]` is the approved absolute harness executable;
- `stdin` is the exact prompt byte sequence;
- environment is complete and exactly equal to the common environment, with no ambient inheritance;
- directory is the approved absolute working directory;
- timeout is positive and exactly equal to the common timeout.

Every string must be valid UTF-8 and contain no NUL. `MCPArguments` must return at least one valid argument. Central code appends that suffix without changing any common argv element.

The current gate proves wrapper-level process equality. A future live adapter/runner must additionally expose and verify child-effective defaults, routing, feature state, native tool inventory, and other model-visible configuration.

## External adapter protocol v1

The current bridge protocol is exactly `tokenbench.external-adapter/v1`.

### Process transport

For every method call, the bridge:

1. reverifies the external adapter before and after the call as the same non-hard-linked executable regular file and digest;
2. launches the absolute executable directly, without a shell;
3. sets cwd to the directory containing that executable;
4. sets the child environment to exactly `process.Config.Environment`, without inheriting the parent environment;
5. writes one compact JSON request value to stdin;
6. requires exit code zero and zero bytes on stderr;
7. reads one UTF-8 JSON response value from stdout.

The default limit is 8 MiB independently for stdout and stderr; configuration may choose another positive limit. The configured control-process timeout applies to every call. A fixed 250 ms `WaitDelay` bounds post-exit waits for inherited stdout/stderr pipes. On Unix, the bridge places the adapter in its own process group and kills that group on cancellation and after the call so descendants cannot escape control; other platforms retain direct-child cancellation plus `WaitDelay`. Output overflow, timeout, nonzero exit, any stderr, invalid UTF-8, duplicate keys, unknown fields, protocol drift, a descendant holding pipes open, or a second/trailing JSON value fails the call.

Every request carries `"protocol_version":"tokenbench.external-adapter/v1"`, an `"operation"`, and exactly one corresponding operation payload. Every response carries the protocol version but no operation field. After decoding, a successful response must have exactly one nonempty/non-null operation result and an empty/omitted `error`:

| Operation | Request payload | Required decoded success result |
| --- | --- | --- |
| `resolve` | `"resolve": ResolveRequest` | non-null `"identity": Identity` |
| `mcp_arguments` | `"mcp_server": MCPServer` | nonempty `"arguments": [string, ...]` |
| `build` | `"invocation": Invocation` | non-null `"process": ProcessSpec` |
| `decode` | `"execution": RawExecution` | non-null `"observation": Observation` |

A failure response carries the exact protocol version and a nonempty `"error"`; every success payload must decode as empty. The bridge validates that exclusive error shape before returning the child error. Because shape validation happens after JSON decoding, absent or `null` pointer payloads and absent, `null`, or empty `arguments` all count as empty; producers should omit them canonically.

The nested field names are the JSON tags on the current harness types. Go `[]byte` fields are standard JSON base64 strings:

- `Invocation.prompt`;
- `ProcessSpec.stdin`;
- `RawExecution.stdout`;
- `RawExecution.stderr`.

Non-nil byte slices use padded RFC 4648 standard base64 as produced by Go `encoding/json`; a nil slice encodes as JSON `null`. Adapters must not use arrays of byte integers or raw embedded text.

### External process control commitment

The canonical control record pins:

- absolute adapter command and its executable SHA-256;
- adapter kind;
- fixed adapter argv;
- complete adapter control environment;
- bridge and wire-protocol versions;
- per-call timeout;
- output limit;
- the bridge's fixed process `WaitDelay`.

The bridge canonicalizes those values and commits them as `adapter_control_config_sha256`. If `CommitmentKey` is present, the commitment is HMAC-SHA-256; a supplied key must be at least 32 bytes. Any nonempty control environment requires such a secret key, preventing secret environment values from being recoverable by dictionary attack against an unkeyed digest. With an empty environment and no key, the bridge uses ordinary SHA-256.

The key is control-plane secret material, not evidence. Changing the command, executable bytes, argv, kind, environment, bridge/protocol version, timeout, output limit, or committed wait delay changes the commitment. The current CLI constructs its external adapter with an empty control environment and deterministic 30-second bridge timeout.

### Per-operation rules

For `resolve`, the child returns its child-owned effective configuration digest and identity. The bridge overwrites wrapper-owned executable/control commitments, validates all identity fields, and requires returned kind to match configured kind.

For `build`, the bridge rejects nonempty MCP registrations before invoking the child and verifies that invocation wrapper commitments match this bridge instance. The child returns the common `ProcessSpec`; central tokenbench code performs the final common-input equality check.

For `mcp_arguments`, the child sees only `MCPServer` and returns a nonempty suffix. It cannot receive task prompt, developer instructions, quality rubric, or arm metadata through this operation.

For `decode`, the child receives exact raw stdout/stderr bytes, exit code, and timeout flag and returns `Observation`. Token counters must be nonnegative and cached input cannot exceed input.

## Registration and read-only status

Current central code creates exactly:

```text
name: repo_view
command: pinned absolute repo-view executable
arguments: ["mcp", "--root", working_directory, "--base", source_base_revision]
environment: {}
required: true
read_only: true
```

The executable and full registration are digest-bound, and no shared MCP registration is permitted. The current `read_only` value is a declaration, not live proof. A future runner must verify the actual MCP handshake, server identity, declared tool schemas, and absence of mutating operations before publishing a conformant result.

## Conformance tests

The current shared conformance helpers repeat equal-input `Resolve`, `Build`, `Decode`, and `MCPArguments` calls to check determinism. They also check complete request-to-invocation binding, identity validity, process validity, zero-versus-one registration cardinality, one common rendering path, and nonempty MCP encoding. Those repeated conformance calls do not change the runtime contract: one `Pair.Build` calls `Build` and `MCPArguments` once each. The fake and external process adapters exercise that contract without live credentials.

Adapter tests should additionally cover:

- requested versus resolved model/revision mismatch;
- adapter executable, wrapper control, and child effective-config digest drift;
- source/Git identity or working-directory drift;
- candidate passed incorrectly to `Build`;
- empty, invalid, or nondeterministic MCP suffix;
- external protocol version/shape/UTF-8/duplicate/trailing/stderr failures;
- deterministic external-adapter cwd and complete environment;
- timeout, descendant-held-pipe, and Unix process-group cleanup behavior;
- keyed control-environment changes and executable mutation;
- raw usage errors and interrupted output.

## Codex and other live harnesses

Codex is the first planned live/model-backed adapter. It must implement the same interface and sole-delta construction, expose exact resolved model revision and effective configuration, use native MCP registration without prompt or environment assistance, and preserve raw events for later capture.

A live harness is unsupported if it requires a candidate-only prompt, wrapper, `PATH`, environment variable, permission, hidden default, or any rendering path beyond the central MCP argv suffix.

## Review checklist

- `ResolveRequest` is arm-free and complete.
- Requested and resolved immutable model identities match.
- Adapter executable, control, and child configuration commitments are distinct and stable.
- `Build` accepts only common state and is called once.
- `MCPArguments` accepts only the approved registration and returns a nonempty suffix.
- Central clone-plus-append is the only process delta.
- Source, `.git`, Git executable, tokenbench executable, harness, MCP executable, and adapter identity are reverified.
- Serialized plans remain audit-only.
- Live read-only MCP verification is not claimed before implementation.
