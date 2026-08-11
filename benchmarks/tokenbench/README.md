# Tokenbench

Tokenbench is the validity-first paired benchmark for measuring whether making
the read-only `scopesifter` MCP server available changes Codex token use without
reducing answer quality.

The invariant is deliberately narrow:

| Arm | MCP configuration |
| --- | --- |
| Baseline | no MCP registrations |
| Candidate | exactly one required registration named `scopesifter` |

Everything else is derived once and shared: exact prompt bytes and roles,
developer instructions, requested and provider-resolved model revision,
reasoning settings, Codex build, adapter, native tools, environment and `PATH`,
permissions, limits, proxy route, source/Git state, filesystem paths, and fresh
runtime layout. Tool declarations obtained from the candidate MCP handshake are
part of the treatment. A candidate-only hint, wrapper, executable, environment
value, or repository preparation is a parity failure, not a benchmark result.

## Current status

The checked-in implementation provides:

- strict `tokenbench.suite/v2`, artifact-manifest v2, plan v4, run v3,
  observation v2, capture v5, signed-root, trust-policy v2, and replay v3
  contracts;
- an audit-only planner plus one publishable live adapter for Codex CLI
  `0.144.0`, exact executable SHA-256
  `08b012d75651efb22b5162be253cd4d28752594082671098e123229b896ba77e`;
- one allowed model snapshot: requested `gpt-5.4`, immutable identity
  `gpt-5.4@gpt-5.4-2026-03-05`, provider value
  `gpt-5.4-2026-03-05`;
- immutable source, standalone `.git`, changed-state cache, Codex, scopesifter,
  Git, Bash, runner, and closed native-tool snapshots backed by fs-verity and a
  private read-only self-bind mount;
- fresh arm state, randomized counterbalanced order, complete process and
  effective-config parity checks, exact MCP tool-surface checks, raw Codex
  JSONL and Responses-wire capture, and provider usage decoding;
- Linux containment using a private PID namespace, delegated bounded cgroup v2,
  cgroup BPF network policy, Landlock ABI 6 or newer, seccomp, empty capability
  sets, and fail-closed process-tree cleanup;
- append-only content-addressed evidence, atomic publication, Ed25519
  attestations, out-of-band trust policy, graph verification, and offline replay;
- family-aware study policy, exactly two blinded judgments for review/explain
  tasks, deterministic preregistered aggregation, and inter-rater disclosure;
- a harness-neutral adapter interface, fake/conformance fixtures, and a generic
  non-publishable executor for extension work.

The live path intentionally refuses to run when a required kernel, artifact,
identity, cleanup, or capture proof is unavailable. It has no best-effort
publication mode. Study-level blinded quality evaluation and statistical
reporting are kept separate from raw capture and replay. Code-task quality has
a sealed objective-outcome boundary, but no code runner is implemented yet.

## Commands

From the repository root:

```sh
go run ./benchmarks/tokenbench/cmd/tokenbench help
```

The command surface is:

- `validate`: strictly load a suite and reverify its common source, model, and
  Codex planning inputs without launching a model;
- `plan`: emit a non-publishable audit plan; decoded plans never grant execution
  authority;
- `run`: execute exactly one explicit suite repetition and publish one signed
  capture root;
- `verify`: authenticate and transitively verify a capture or replay root under
  an explicit trust policy;
- `replay`: decode an authenticated capture offline and publish a new signed
  replay root without changing its parent.

Run every authored repetition explicitly. Outputs, state, CAS, and snapshot
paths are exclusive; tokenbench never selects or overwrites a prior result.

```sh
tokenbench run \
  --suite /absolute/study/suite.json \
  --artifact-bundle /absolute/artifacts \
  --snapshot-root /absolute/new-snapshot \
  --state-root /absolute/new-runtime \
  --cas /absolute/new-cas \
  --root-out /absolute/new-capture.root.json \
  --credential-fd 3 \
  --signing-key-file /absolute/secrets/capture.seed \
  --trust-policy /absolute/policy/trust.json \
  --repetition 0 \
  3</absolute/secrets/openai-api-key
```

The inherited credential descriptor must be in `3..255`. The parent process
passes it unchanged into a new private mount namespace; the child reads it once,
closes it before either arm launches, and never serializes it. Ambient provider
base URLs, proxies, or custom-CA variables are rejected.

Verify and replay by signed root, never by mutable directory convention:

```sh
tokenbench verify \
  --cas /absolute/cas \
  --root /absolute/capture.root.json \
  --trust-policy /absolute/policy/trust.json

tokenbench replay \
  --cas /absolute/cas \
  --root /absolute/capture.root.json \
  --trust-policy /absolute/policy/trust.json \
  --signing-key-file /absolute/secrets/replay.seed \
  --root-out /absolute/new-replay.root.json
```

`run` and `replay` read signing keys only after the data they will attest has
passed the applicable execution/authentication boundary. A signing-key file is
exactly one unpadded base64url-encoded 32-byte Ed25519 seed with no newline.
Trust-policy JSON is byte-canonical, independently distributed, and authorizes
sorted key IDs for `capture`, `replay`, or both roles.

## Building a publishable binary

Publishable execution uses a closed artifact bundle. Its fixed manifest name is
`tokenbench-artifacts-v2.json`; the exact compact JSON bytes must match
[`schemas/artifact-manifest-v2.schema.json`](schemas/artifact-manifest-v2.schema.json).
It names exact, single-link, native static ELF images for Codex, scopesifter,
verifier Git, Bash, and each allowlisted utility, with reproducible provenance.
No symlink, dynamic loader, multicall alias, unlisted executable, or digest drift
is accepted.

The suite and tokenbench binary must independently allow the same manifest
digest. Build tokenbench itself as a static executable and bind that digest at
link time:

```sh
manifest_sha256="$(sha256sum /absolute/artifacts/tokenbench-artifacts-v2.json | awk '{print $1}')"
CGO_ENABLED=0 go build -trimpath \
  -ldflags "-X github.com/scopesifter/scopesifter/benchmarks/tokenbench.trustedArtifactManifestSHA256=${manifest_sha256}" \
  -o /absolute/bin/tokenbench \
  ./benchmarks/tokenbench/cmd/tokenbench
```

The authored suite's `artifact_manifest_sha256`, `harness_executable`,
`harness_sha256`, `git_executable`, and `git_executable_sha256` must agree with
that same bundle. A normal `go run` or binary built without the link-time policy
can validate and produce audit-only plans, but cannot publish a live result.

## Host prerequisites

Publishable runs currently require Linux on a native architecture supported by
the bundled static ELF images, plus all of the following:

- a private mount namespace whose mount tree can be made recursively private;
- an absent snapshot path on an fs-verity-capable filesystem and permission to
  create a read-only `nosuid,nodev` self-bind mount;
- a writable delegated cgroup-v2 directory containing only tokenbench, with
  finite `pids.max` and `memory.max`, and `cpu`, `memory`, and `pids`
  controllers;
- cgroup BPF attach/query support with no inherited connect programs;
- Landlock ABI 6 or newer, seccomp, PID namespaces, and capability bounding;
- direct TLS access to `https://api.openai.com/v1` using system roots.

The mandatory privileged CI lane builds a pinned container by digest and turns
every unavailable prerequisite or skipped kernel test into a failure. For a
local kernel-boundary check, run:

```sh
benchmarks/tokenbench/scripts/privileged-linux-tests.sh
```

This command requires a Linux x86-64 host and a privileged Docker-compatible
container engine. It performs no model call.

## Repository layout

```text
benchmarks/tokenbench/
  AGENTS.md             coding rules for agents and contributors
  CONTRIBUTING.md       review and verification checklist
  DESIGN.md             security, authority, parity, and failure model
  cmd/tokenbench/       command-line boundary
  harness/              harness-neutral contract and Codex adapter
  runner/               containment and raw execution
  snapshot/             immutable source/tool image and changed-state cache
  cas/                  typed append-only content-addressed store
  evidence/             capture, attestation, verification, and replay
  source/               mutable-origin verification
  study/                separate methodology stage for blinded paired analysis
  schemas/              authored JSON contracts
  scripts/              fail-closed privileged validation
  docs/                 operations, methodology, evidence, and adapter guides
```

Read [DESIGN.md](DESIGN.md) before changing an authority boundary,
[AGENTS.md](AGENTS.md) before editing code, and
[docs/adapter-authoring.md](docs/adapter-authoring.md) before adding another
harness. Operators should follow the end-to-end
[artifact, signing, run, and recovery guide](docs/operations.md). Current
loaders accept only the ScopeSifter-bound contracts documented here;
pre-rename evidence remains available from Git history and is not pooled with
Tokenbench evidence.
