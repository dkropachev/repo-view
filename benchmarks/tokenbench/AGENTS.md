# Tokenbench coding instructions

This file applies to `benchmarks/tokenbench/` and every descendant. Read
`DESIGN.md` before modifying parity, authority, execution, capture, or evidence.

## Non-negotiable experiment invariant

Baseline and candidate must be identical in every semantic input and effective
configuration value except one:

- baseline has zero MCP registrations;
- candidate has exactly one required, read-only registration named `repo_view`.

The resulting candidate MCP declarations are part of that registration. No
other arm difference is allowed.

Do not add candidate-only prompt text, navigation hints, answer facts, tool-use
requirements, wrappers, executable paths, descriptors, environment variables,
`PATH` entries, permissions, timeouts, model/reasoning settings, feature flags,
source preparation, runtime directories, retry rules, or capture behavior. Do
not make repo-view available as a candidate-only shell executable. If a harness
needs any such difference, it is a different experiment and must not produce
tokenbench evidence.

## Authority rules

- Treat every exported struct and JSON document as audit data. It is not proof
  that a path, process, mount, kernel policy, or signature was verified live.
- Keep authority-bearing state private and unforgeable. Constructors must return
  capabilities with unexported state after completing the live checks.
- Never add an execution constructor from `ResolvedPlan`, decoded capture, raw
  `ExecutionInputs`, a boolean such as `Conformant`, or a caller implementation
  of an open interface.
- Keep the publishable path concrete: trusted artifact capability -> immutable
  snapshot authority -> built-in production adapter -> conformant runner ->
  closed execution boundary -> one-shot publication authority.
- Reverify mutable inputs before and after every external/control call. Recheck
  private capabilities at the point of use; do not rely only on preparation.
- A close method is a security boundary. Report closed only after descendants,
  listeners, credentials, mounts, cgroups, kernel programs, locks, and retained
  descriptors are actually gone. A previous close failure remains fail-closed.
- Attempt every ordered cleanup step and join errors. Never sign, publish, or
  delete diagnostic residue after an uncertain cleanup.

## Suite, parity, and adapter rules

- Keep the authored suite common and closed. Do not add arm blocks, a general
  MCP/tool registry, opaque argv, arbitrary environment, extension maps, or
  defaults that depend on ambient state.
- Reject unknown/duplicate/trailing JSON and noncanonical encodings where the
  schema requires canonical bytes. Bound every string, list, object, raw stream,
  recursion depth, and statistical work factor before allocation or execution.
- Central code alone creates the `repo_view` registration and candidate arm.
  `Adapter.Build` receives the MCP-free common invocation exactly once;
  candidate is a central deep clone plus only `MCPArguments(registration)`.
- `ResolveRequest` and `MCPArguments` must not carry an arm label, prompt/rubric
  unavailable to the other arm, order, repetition, token count, or answer key.
- Compare complete process specs and effective child/provider configuration,
  not selected command-line fields. Missing evidence is a failure, never proof
  of equality.
- An adapter extension is non-publishable until code owns and pins its concrete
  lifecycle, executable/model/config allowlists, effective-config capture,
  provider/tool-surface checks, and cleanup authority. Do not let extensions
  self-attest by returning a marker string or implementing another interface.

## Filesystem and executable rules

- Resolve and require absolute canonical non-root paths at CLI boundaries.
  Reject overlap among mutable outputs, credentials/trust files, source,
  artifact origins, immutable snapshots, and runtime state.
- Do not follow symlinks or cross a filesystem boundary while loading trusted
  inputs. Reject hard-linked authority files, special files, mount descendants,
  shared/slave propagation, dynamic ELF loaders, ABI mismatch, and path/digest
  drift.
- Use descriptor-relative/traversal-safe opens for security decisions. If a
  pathname is checked before an open, cross-check the opened inode and pathname
  again afterward. Retain inode descriptors until the operation that depends on
  them is durable.
- The model `PATH` is exactly the immutable toolbox directory. Never fall back
  to ambient `PATH`, shell discovery, `/usr/bin`, user home configuration, or a
  dynamic loader.
- Preserve the exact same source/toolbox/runtime paths for both arms. Do not
  encode arm name, randomized order, attempt ID, or repetition into anything
  model-visible.
- Failed snapshot/CAS cleanup must retain narrowly identified residue and emit
  recovery information. Never recursively remove a broad or unresolved path.

## Process, kernel, and network rules

- Launch target processes directly, without a shell wrapper. Use complete
  allowlisted environments; never append `os.Environ()` to a model or adapter
  process.
- Contain the entire descendant tree, not just the direct child. Preserve atomic
  cgroup placement, bounded ancestor/arm limits, PID namespace reaping,
  capability removal, Landlock, seccomp, and exact network enforcement.
- Do not weaken a kernel prerequisite into a skip in the privileged CI lane.
  `TOKENBENCH_REQUIRE_PRIVILEGED_TESTS=1` must turn an unavailable prerequisite
  into a test failure.
- Query both direct and effective cgroup BPF program chains. Any unowned
  ancestor/extra program is invalid even if it appears to be more restrictive.
- Production provider routing is code-owned. Reject ambient proxies, alternate
  API bases, custom CA variables, redirects, unexpected DNS/TLS identity, and
  unrecorded response attempts.
- Keep secret descriptors and upstream credentials outside model-visible state.
  Read the credential once after immutable preparation, close its source before
  launch, clear temporary byte slices, and never serialize/hash/log the value.

## Capture, evidence, and replay rules

- Capture raw bounded bodies, ordered SSE state, and the complete reviewed
  semantic header envelope before decoding. Do not claim raw HTTP/2 framing or
  header ordering. Preserve non-2xx, transport, redirect, partial, overflow,
  timeout, and cancellation attempts; do not retain only successful responses.
- Cross-check Codex JSONL, committed provider request/response semantics,
  effective config, model revision, tool declarations, and normalized
  observation.
- Ordinary arm failures are data. Integrity/parity/cleanup/capture failures
  invalidate publication. Never silently retry, select the favorable attempt,
  turn missing counters into zero, or drop a failed arm.
- CAS objects are immutable and content-addressed. Publish child objects first,
  the canonical subject next, and the signed root last. Verify every edge and
  directory sync before declaring completion.
- Do not rewrite a capture during replay. Authenticate the complete parent
  graph under an explicit out-of-band policy, decode offline, and publish a new
  signed child root.
- Never place credentials, authorization headers, signing seeds, trust-policy
  authority, unrelated host configuration, or unrestricted environments in
  evidence. Repository/answer content may still be sensitive; integrity does
  not grant permission to disclose it.
- Keep raw input, cached-input, output, reasoning, and provider totals separate.
  Do not invent “effective tokens” by discounting cached tokens. Pricing is a
  separate versioned derived input.

## Study and quality rules

- Predeclare the corpus, exclusions, repetition/stopping rules, quality items,
  thresholds, estimator, uncertainty procedure, and random seeds by commitment
  before looking at comparative outcomes.
- Blind evaluators to treatment, order, traces, token counts, failures, and pair
  identifiers. Randomize opaque answer labels from a committed secret and bind
  returned scores to the exact packet before unblinding.
- Prefer objective binary facts. Keep evaluator rationale bounded and preserve
  individual outputs. A shorter incorrect answer cannot pass the efficiency
  gate.
- Count attempted, not-attempted, excluded, failed, incomplete, quality-failed,
  and analyzed pairs explicitly. Enforce predeclared limits before reporting a
  winner.
- Analyze paired effects at task level and account for repeated task/repository
  clustering. Keep statistical significance, effect size, quality
  noninferiority, and practical token threshold as separate gates.

## Change discipline

- Preserve package boundaries: `source` verifies mutable origins; `snapshot`
  owns immutable filesystem authority; `harness` defines semantics; `runner`
  owns processes/kernel boundaries; `evidence` owns immutable publication and
  replay; `study` owns blinded quality/statistics; `cmd` only orchestrates.
- Use typed errors or concrete private state for security decisions. Error text
  is diagnostic, not authority. Avoid errors that reveal secret/path contents
  unnecessarily.
- Do not edit or regenerate `experiments/lsp-replacement` evidence to make it
  conform. Legacy replay must work on staging copies and preserve source bytes.
- Update schema, golden, design, migration, and adapter documentation in the
  same change as a contract modification. Label unsupported behavior plainly.
- Add positive, negative, corruption, aliasing, mutation-race, cleanup-failure,
  and serialization tests at the affected boundary. Tests must not require a
  live model or real credential.

## Required verification

At minimum for code changes:

```sh
go test ./benchmarks/tokenbench/... -count=1
go test -race ./benchmarks/tokenbench/... -count=1
go vet ./...
go build ./cmd/... ./benchmarks/tokenbench/cmd/...
golangci-lint run --config=.golangci.yml
golangci-lint run --config=.golangci-fieldalignment.yml
git diff --check
```

Run relevant GOOS/GOARCH compile checks for platform files. Changes to mounts,
fs-verity, cgroups, BPF, Landlock, seccomp, PID namespaces, capabilities, or
cleanup also require the fail-closed privileged lane:

```sh
benchmarks/tokenbench/scripts/privileged-linux-tests.sh
```

Do not merge a staged pull request until every required GitHub check is green.
