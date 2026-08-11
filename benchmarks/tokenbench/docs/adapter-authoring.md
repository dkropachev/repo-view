# Harness adapter authoring

Tokenbench separates harness translation from experiment ownership. An adapter
resolves one common semantic request, renders one common process, encodes one
already-approved MCP registration, and decodes bounded raw execution. It does
not create arms, choose order, launch processes, retry, judge answers, or publish
evidence.

The fake adapter, shared conformance helpers, external-process bridge, and
generic runner make a new harness easy to prototype without weakening the
publishable Codex path. New adapters are non-publishable by default.

Study `task-bundle/v1` references are authoring and loader inputs, not adapter
inputs. An adapter receives the same resolved prompt and common toolchain in
both arms; it must never receive a hidden evaluator bundle descriptor, gold patch,
bundle role, CAS pathname, or catalog-only answer material.

## Interface

```go
type Adapter interface {
    Kind() string
    Resolve(context.Context, ResolveRequest) (Identity, error)
    MCPArguments(context.Context, MCPServer) ([]string, error)
    Build(context.Context, Invocation) (ProcessSpec, error)
    Decode(context.Context, RawExecution) (Observation, error)
}

type CommonEnvironmentAdapter interface {
    CommonEnvironment(context.Context, ResolveRequest) (map[string]string, error)
}
```

Equal inputs must produce deeply equal outputs.

- `Kind` is the suite-facing harness name.
- `CommonEnvironment`, when implemented, derives the complete child environment
  from common inputs. It is called during preparation and immediately before
  rendering; results must match. Without it, the canonical environment is `{}`.
- `Resolve` binds common requested settings to exact adapter, harness, model,
  immutable model revision, reasoning, and decoder identities.
- `Build` receives only a common invocation with an empty MCP list and returns
  one complete `ProcessSpec`.
- `MCPArguments` receives only the approved `MCPServer` and returns a nonempty
  harness-native argv suffix.
- `Decode` maps bounded `RawExecution` to one harness-neutral `Observation`; it
  performs no launch or network call.

`ResolveRequest` has no arm, order, repetition, prompt answer key, token count,
or quality result. It binds the complete common environment, harness executable
and digest, requested/expected model identity, reasoning, permissions,
developer instructions, working directory, source base/head/tree, standalone
Git metadata, verifier Git path/digest, runner path/digest, and timeout.

## Sole-delta construction

Central tokenbench code, never the adapter, performs:

```text
common     = resolve one authored suite
baseline   = deep clone common; mcp_servers = []
candidate  = deep clone common; mcp_servers = [approved scopesifter]
proof      = remove candidate registration and require deep equality

process    = adapter.Build(common-with-no-MCP)       // once
suffix     = adapter.MCPArguments(approved server)   // treatment only
baseline_p = deep clone process
candidate_p= deep clone process; argv += suffix
```

`Build` must reject a nonempty MCP list. `MCPArguments` must not receive task
prompt, developer instructions, rubric, order, or arm label. Do not implement a
second candidate rendering branch or inject a prompt, `PATH`, wrapper, feature,
permission, timeout, environment value, or executable alongside the suffix.

Current live central code constructs this exact registration from the immutable
snapshot:

```text
name: scopesifter
command: <immutable tools/scopesifter>
arguments:
  mcp --root <immutable source/worktree>
      --base <full base object id>
      --head <full head object id>
      --changed-state-cache <immutable canonical cache>
      --changed-state-cache-sha256 <exact digest>
environment: {}
required: true
read_only: true
```

The cache-only scopesifter backend cannot fall back to Git. Baseline inherits the
same pinned scopesifter image at the same fixed descriptor and has the same
filesystem/execute policy, but has no MCP configuration that references it.

## Identity ownership

Every `Identity` includes:

- adapter version and exact adapter executable SHA-256;
- adapter control-config and effective adapter-config SHA-256;
- harness executable digest/version;
- resolved model and immutable model revision;
- reasoning effort and decoder schema.

Resolved model must equal the requested model, and revision must exactly equal
the authored `<model>@<immutable-revision>`. Tokenbench calls `Resolve` again
before rendering and requires the whole identity to remain equal.

For an in-process adapter, the implementation owns all adapter commitments. For
the external bridge:

- the wrapper overwrites `adapter_executable_sha256` from the opened adapter
  program;
- the wrapper overwrites `adapter_control_config_sha256` from its exact command,
  argv, environment, protocol, timeout, limits, and wait policy;
- the child supplies `adapter_config_sha256` for its effective internal config
  and supplies the remaining semantic identity fields.

When a control environment is nonempty, the wrapper requires a private key of
at least 32 bytes and uses HMAC-SHA-256 so evidence cannot recover low-entropy
secret values by dictionary attack. The key and values are never evidence.

## Process and decode requirements

`Build` returns a complete process whose:

- `argv[0]` is the exact approved absolute harness executable;
- stdin is the exact prompt bytes;
- environment is non-nil, complete, and exactly common (no ambient inheritance);
- cwd is the exact common immutable source path;
- timeout exactly matches the invocation;
- strings are valid UTF-8 and contain no NUL.

Central code checks all fields before appending the candidate suffix and
reverifies source, Git, runner, harness, scopesifter, artifact bundle, and snapshot
authority around adapter calls.

`Decode` must reject malformed/ambiguous raw state. Counters are nonnegative,
cached input cannot exceed input, tool-call count is bounded, and a completed
observation requires a bounded final answer. Missing data is not zero. A decoder
may receive sanitized artifacts such as provider wire trace and effective config
and should cross-check them rather than trusting stdout alone.

## External adapter protocol v2

Use `harness/process` when the adapter should be an independently versioned
executable. The protocol is `tokenbench.external-adapter/v2`. Version 2 adds
the optional `usage.provider_total_tokens` observation field; missing and a
present zero are distinct.

The test-only in-process fake advertises `tokenbench.fake-adapter/v2` and
`tokenbench.fake-output/v2` for the same observation change. Old v1 protocol
and decoder identities are rejected rather than reinterpreted as v2.

For every method call the bridge:

1. opens and hashes an absolute, executable, single-link regular file;
2. launches it directly without a shell, in its containing directory;
3. supplies exactly the configured control environment, never ambient values;
4. writes one compact JSON request to stdin;
5. requires exit zero, empty stderr, bounded valid UTF-8 stdout, and exactly one
   strict JSON response;
6. kills the process group/descendants on timeout or after output completion;
7. reopens and rehashes the executable after the call.

Default stdout/stderr limits are 8 MiB each. Timeout and a fixed 250 ms inherited
pipe wait are committed control inputs. Overflow, invalid UTF-8, duplicate or
unknown fields, trailing JSON, nonzero exit, any stderr, descendant-held pipes,
timeout, or executable drift fails the call.

Every request has:

```json
{"protocol_version":"tokenbench.external-adapter/v2","operation":"<name>","<payload>":{}}
```

Every response has the protocol version and either exactly one success payload
or a nonempty `error`. It has no `operation` field.

| Operation | Request member | Success member |
| --- | --- | --- |
| `resolve` | `resolve: ResolveRequest` | `identity: Identity` |
| `mcp_arguments` | `mcp_server: MCPServer` | `arguments: [string, ...]` |
| `build` | `invocation: Invocation` | `process: ProcessSpec` |
| `decode` | `execution: RawExecution` | `observation: Observation` |

Go `[]byte` fields (`Invocation.prompt`, `ProcessSpec.stdin`, stdout, stderr,
artifact data) use standard padded base64 as emitted by `encoding/json`; nil is
JSON `null`. Do not encode byte arrays as integer arrays or embedded raw text.

The bridge validates exclusive response shape after strict decoding. A failure
response must leave every success payload empty. A success response must leave
`error` empty. Empty/null arguments are not a valid MCP suffix.

## Testing a new adapter

Start with `harness/conformance`. Add deterministic fixtures for:

- repeated equal-input `CommonEnvironment`, `Resolve`, `Build`,
  `MCPArguments`, and `Decode` calls;
- complete request-to-identity/invocation binding;
- requested/resolved model and revision mismatch;
- adapter executable, control config, and effective config drift;
- common source/Git/runner/cwd/environment/timeout drift;
- rejection of candidate/nonempty MCP input to `Build`;
- empty, invalid, nondeterministic, or extra-setting MCP suffixes;
- raw success, ordinary failure, partial output, usage ambiguity, and bounds;
- external protocol version/shape/UTF-8/duplicate/trailing/stderr/timeout and
  descendant cleanup failures;
- executable replacement/hard-link races and keyed control-environment changes.

`runner.New` can exercise a generic adapter/lifecycle and optional containment,
but its results are intentionally non-publishable. This is the correct path for
development and private comparisons.

## Promoting another harness to publishable

Adding an adapter implementation is not enough. A separate reviewed change must
add all of the following as code-owned, unforgeable policy:

- exact harness executable/version and model-revision allowlists;
- a production constructor distinguishable from test/offline constructors by
  private concrete state;
- one fresh common runtime layout and exact environment/argv/config policy;
- a local credential-isolating capture boundary and pinned provider route;
- effective child config capture and normalization proving the sole delta;
- exact native and MCP tool declaration/handshake checks;
- raw provider/event capture and a pinned decoder that cross-check each other;
- full descendant containment, kernel policy identity, resource accounting, and
  fail-closed cleanup;
- publication validation that recognizes only that exact implementation;
- ordinary, race, adversarial, and mandatory privileged tests plus current
  documentation.

Do not expose a `Publishable() bool` knob, registration hook, caller interface,
or serialized certificate that lets extension code grant itself authority. If a
harness cannot expose its effective configuration and tool/provider state, it
remains useful for non-publishable experiments but is unsupported for conformant
tokenbench evidence.
