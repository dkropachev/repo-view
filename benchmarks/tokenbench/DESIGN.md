# Tokenbench design

Tokenbench treats benchmark validity as an authority problem. Serializable JSON
can describe what should have happened, but only private capabilities returned
by successful live verification may authorize execution or publication.

## Security and validity objective

For one task, repository state, and repetition:

```text
baseline  = common configuration with mcp_servers = []
candidate = the same configuration with mcp_servers = [scopesifter]
```

Removing candidate's one registration must produce baseline exactly. The
candidate receives the MCP declarations that Codex derives from that
registration; no other candidate-only input is authorized. Run order and fresh
random values are committed evidence metadata and are not placed in model-visible
paths, prompts, environment, or configuration.

A run is not publishable merely because two objects compare equal. Tokenbench
must also prove that the exact configuration reached the child and provider,
that the repository/tools stayed immutable, that both process trees were
contained and removed, and that evidence publication completed durably.

## Authority flow

```text
suite v2 + prompt + mutable source + trusted artifact bundle
                            |
                            v
            LoadArtifactBundle / PrepareOrigins
                            |
                            v
          BuildExecutionSnapshot (fs-verity + read-only mount)
                            |
                            v
            PreparedExecution (live private authority)
                            |
                            v
     BindAdapter (built-in production Codex capability only)
                            |
                            v
              Pair (sole-delta + snapshot authority)
                            |
                            v
       Pair.Execute (conformant runner + fresh arm sessions)
                            |
                            v
 runner closed -> lifecycle closed -> snapshot unmounted/closed
                            |
                            v
     signed CAS publication (private one-shot run authority)
```

`ResolvedPlan`, `Run`, capture JSON, replay JSON, and filesystem paths are audit
data. Decoding or constructing them never recreates a private authority.
Publication consumes a one-shot marker sealed around the exact completed run;
the marker becomes ready only after executor, lifecycle, and immutable snapshot
closure predicates all hold.

## Authored suite

`tokenbench.suite/v2` is one common specification. It has no baseline/candidate
blocks, open tool registry, arbitrary environment, opaque harness arguments, or
arm override. It binds:

- exact suite and prompt bytes;
- requested model and expected immutable model revision;
- common developer instructions, reasoning effort, read-only permissions,
  timeout, repetitions, and order seed;
- exact Codex and verifier-Git paths/digests;
- exact trusted artifact-manifest digest;
- clean standalone source root, full base/head object IDs, and tracked-tree
  digest.

Unknown and duplicate fields are rejected. Treatment-neutrality checks reject
prompt/instruction text that names or hints at the treatment. Paths resolve
before execution and every mutable input is reverified across external calls.

## Study authoring contracts

The confirmatory study catalog is a closed 144-task matrix over twelve locked
repositories, three task families, and four size tiers. Its canonical digest
commits source revisions, prompt/toolchain/evaluator digests, commands, bounds,
quality items, exclusions, and code-only gold-patch identities before outcomes
are observed.

`task-bundle/v1` is the catalog's non-executing artifact-reference companion.
It commits that catalog digest and the exact sorted task IDs to opaque
content-addressed prompt, pinned-toolchain, hidden-evaluator, and code-only
gold-patch objects. The value contract accepts no filesystem paths or loader
options. A future integration must separately open and verify those objects,
keep hidden roles outside both model sandboxes, and convert verified inputs into
the same common authored suite for both arms before it can authorize execution.

`tokenbench.suite/v3` is a separate, load-only code-task authoring contract. It
retains the common suite fields, requires `workspace-write`, requires the source
base to equal the visible revision, and binds one locked code-family task ID to
the exact task-catalog and toolchain digests. It contains no workspace root,
writable path list, environment, arm field, evaluator identity, or gold object.
The v2 loader still accepts only v2 read-only suites, and the v3 loader does not
produce execution authority.

The `workspace` package defines bounded input and outcome audit records and a
Linux-only live mount authority. `workspace.Prepare` accepts only a live,
reverified immutable snapshot whose visible revision equals its base and whose
changed-state cache is empty. It borrows an existing empty mode-`0700`
mountpoint without creating, renaming, or removing it, attaches a byte-bounded
and model-inode-bounded detached tmpfs, and retains the exact mount namespace,
containing mount, mountpoint descriptor, mount, inode, snapshot, and policy
identities. Under the snapshot-authority lock it duplicates the already-pinned
source root, verifier Git, and Git object-store inodes; workspace code never
reopens those audit paths. The fixed construction and minimum capture allowance
is consumed by code-retained anonymous inodes before an arm is exposed, leaving
exactly `MaximumEntries` inodes shared by writable worktree changes and the
model-visible cache. Audit path strings alone never recreate this authority.

Each `PairAuthority.BeginArm` creates the same fixed layout and a fresh detached
OverlayFS over the immutable source. The model-visible worktree and cache paths
are stable across arms; upper, work, and capture directories remain private
runner state. A whiteout hides `.git`, cache and capture start empty, and the
merged tree is hashed and compared with the committed source manifest before a
future runner may launch a child. Both arm and pair cleanup reject descendant
mounts, use retained mount IDs and normal unmounts only, remove claimed content
descriptor-relatively within finite bounds, and report success only after the
exact mounts and descriptors are released and the borrowed directory descriptor
is again empty. Cleanup follows that exact mount and borrowed inode across
caller relocation; it never mutates a replacement pathname. The borrowed
mountpoint itself remains caller-owned. If a just-created private directory
cannot be assigned an exact inode claim, construction failure closes the whole
pair by normally unmounting its code-owned tmpfs.

After a future runner has synchronously proved the arm cgroup empty, the live
arm authority can capture exactly once. Capture syncs the merged filesystem,
changes the retained overlay mount from writable to read-only without changing
its identity or other mount options, and rejects symlinks, hard links, special
files, extended attributes, noncanonical modes, unrepresentable empty
directories, and every configured tree-entry, per-file, change-count, and patch
limit. The writable upper is independently bounded by its physical tmpfs; bytes
from the immutable lower do not consume that budget. Only after the read-only
transition does capture release the anonymous
infrastructure inode reserve, so private capture scratch cannot consume the
model-and-cache budget during execution. It invokes only the fs-verity-pinned
verifier Git and source object store
through retained descriptors, writes into private bounded tmpfs scratch, emits
a deterministic binary-capable full-index patch, and proves that applying the
patch to a fresh base index produces the exact scanned result tree. The
one-shot outcome distinguishes captured, no-change, invalid-tree, and
limit-exceeded states; capture integrity failures remain errors. No code runner,
signed workspace/CAS edge, or objective evaluator is wired to this authority
yet.

## Trusted artifacts and immutable execution image

The fixed artifact-manifest v4 contains a closed set of exact executable roles:
Codex, scopesifter, static verifier Git, and 11 native utilities. It
also contains bounded source/recipe/builder provenance. The manifest is accepted
only when three commitments agree: authored suite, exact manifest bytes, and an
unexported digest embedded in the tokenbench executable at link time.

Linux loading uses traversal-safe `openat2`, rejects symlinks, hard links,
dynamic ELF interpreters/dependencies, nonnative ABI, oversized files, role
aliasing, and byte drift. A private loader capability, not an exported manifest
value, is required for origin preparation.

The command dispatcher is not a bundle artifact. Snapshot construction copies
the already pinned static Go tokenbench runner image to the closed toolbox and
pure validation requires that copy's digest to equal the runner/arm-init image.
Codex CLI v0.144.0 has no configurable default-shell path and recognizes only a
fixed set of basenames. Its toolbox pathname therefore retains the basename it
searches first on Linux, but the executable at that path is the Go dispatcher;
no Bash image is loaded or executed. The dispatcher accepts only the pinned
non-login `-c COMMAND` form. Its closed, Go-owned grammar is exactly one
pipeline of literal argv commands. Quotes and backslashes only construct argv
bytes. Variables, expansion, source/dot/eval, loops, conditions, functions,
command lists, backgrounding, redirection, builtins, executable paths, and
script images are rejected. Each stage resolves one approved native utility in
a closed environment, and delegation or file-mutation options are rejected.
Evidence commits `tokenbench.command-runner/go-argv-pipeline-v1/v6`; this is not
a shell or scripting-language compatibility claim.

This discovery constraint was audited against the peeled
`rust-v0.144.0` source commit
`767822446c7a594caa19609ca435281a9ec67e0d`: `shell_detect.rs` obtains the Unix
default from `getpwuid_r`, then searches the fixed Bash/Zsh/sh names and absolute
fallbacks; the production CLI provides no user-shell override. `SHELL` controls
child environment policy but not this selection. The pinned discovery pathname
is therefore narrower than adding a Codex fork, a synthetic passwd database,
or a private mount over an ambient absolute shell path. It is version-scoped
and must be deleted as soon as the pinned Codex release no longer requires
fixed-basename discovery.

The snapshot builder:

1. verifies a clean, standalone source and `.git` graph with pinned Git;
2. copies the source and closed executable set without crossing filesystems,
   following links, or accepting special files;
3. derives a bounded, canonical base-to-head changed-state cache using the same
   diff contract as live Git-backed scopesifter;
4. enables and measures fs-verity for every regular file;
5. creates a self-bind mount, makes it read-only, `nosuid`, and `nodev`, and
   verifies its mount namespace, parent propagation state, filesystem root,
   inode, options, and lack of descendant mounts;
6. returns an `Authority` that continuously reverifies the mount and all
   committed inputs.

Both arms use the same model-visible source path and toolbox. The snapshot
authority cannot report closed until unmount and every retained descriptor close
succeeds. Cleanup failure preserves residue and blocks signing.

## Pair and process construction

Central code derives both invocations from one common value. Baseline has a
non-nil empty MCP list; candidate receives one private, code-owned registration:

```text
name: scopesifter
command: immutable snapshot scopesifter path
arguments: mcp, immutable root/base/head, immutable changed-state cache + digest
environment: {}
required: true
read_only: true
```

The adapter is called once to build the common MCP-free process. Candidate is a
deep clone with only the adapter's encoding of the approved registration
appended. Parity covers prompt, model, identity digests, cwd, complete
environment, stdin, timeouts, source/Git identities, runner, artifact inputs,
and every rendered common argument. The complete plan is size-bounded before
the first arm.

The built-in Codex adapter pins CLI v0.144.0, its exact executable digest,
feature allow/deny state, model/reasoning allowlist, command line, environment,
config-lock export, decoder, provider model, and MCP suffix. It never consults
ambient Codex configuration. An offline adapter can create audit-only plans;
only the exact production constructor can bind a publishable pair.

## Fresh runtime and containment

One production lifecycle reserves a loopback capture-proxy address and creates
one common layout before arm order is applied. Before each arm it deletes and
recreates the same `HOME`, `CODEX_HOME`, temporary, SQLite, and config-lock
paths. It retains no transcript or harness state between arms. The local proxy
capability and paths are identical; the upstream credential is never placed in
the child environment.

The runner creates a fresh bounded cgroup for each arm and atomically launches
the child into it. The arm-init process establishes:

- a private PID namespace with a reaping PID 1;
- verified empty effective/permitted/inheritable/ambient/bounding capability
  sets and `no_new_privs`;
- Landlock read/write/execute policy over exactly the immutable image, four
  writable runtime paths, and `/dev/null`;
- seccomp architecture validation, x32 rejection on amd64, process-inspection,
  namespace, kernel attack-surface, listening, Unix-socket, and mutation syscall
  denial;
- cgroup BPF allowing the model process to connect only to the exact loopback
  capture-proxy port; code-owned proxy logic separately pins the upstream
  production route and TLS policy.

The model process inherits the same read-only scopesifter image at fixed FD 5 in
both arms. Candidate configuration references it; baseline configuration does
not. This keeps executable availability, descriptors, and sandbox policy common
while preserving the one intended configuration delta.

Containment identity commits finite ancestor and arm limits, controller state,
kernel program IDs, network ports, Landlock ABI, seccomp version, executable
digests, and cleanup rules. Unexpected ancestor BPF programs, extra cgroup
processes, stale children, capability drift, inability to kill the whole tree,
or inability to remove the arm cgroup is an integrity error.

## Provider and effective-config parity

The capture proxy accepts requests only from the active arm and forwards only
to the code-owned production endpoint using a proxy-free HTTP transport and
system TLS roots. Ambient proxy/base-URL/custom-CA variables are forbidden.
Every verified DNS name and DER certificate chain is committed in the trace.

For every request and response, capture retains the exact body digest, bounded
SSE order, and a canonical digest of the complete reviewed semantic header
envelope. It does not claim to preserve raw HTTP/2 framing, header order, or
header-name casing. The trace also binds dynamic request fields,
response-attempt status (including transport/non-2xx/overflow failures), TLS
identity, usage, provider model, and tool output. Codex JSONL, provider body,
reviewed provider headers, and decoder claims must agree.

The trace explicitly commits whether production TLS is required. In that mode,
each observed response and every attempt that reached the provider carries its
verified TLS identity; only a transport failure before any response or TLS
handshake may have an empty TLS list.

After each arm, tokenbench reads the exact exported effective config. The
normalized common config must match across arms; baseline must have no MCP
entry, and candidate must have exactly the approved entry. Provider requests
must have the same nonce-normalized non-tool payload and native tool
declarations. Baseline must expose no MCP declarations; candidate must expose
exactly the four scopesifter operations and the expected Codex MCP support tools.
Missing capture, request-count asymmetry, provider model drift, tool drift, or
an unmatched arm blocks publication.

## Failure semantics

Failures are separated into two classes:

- ordinary arm outcomes, such as timeout, cancellation, nonzero exit, or bounded
  stdout/stderr truncation, are retained as failed attempts with sanitized
  partial capture;
- parity, identity, containment, cleanup, capture, evidence-integrity, and
  authority failures invalidate the pair and cannot acquire publication
  authority.

The second arm may still run after an ordinary first-arm failure so failure
rates remain observable. Tokenbench never retries silently; a retry is a new
explicit repetition or study record. It never converts missing data to zero or
drops failed attempts from evidence.

## Evidence and replay

Raw execution is published into a typed append-only CAS. Objects are created
exclusively, hashed while written, re-opened and verified, and linked by a
canonical capture manifest. Publication makes the signed Ed25519 attestation
root visible last. Inode pins, per-store/process locks, directory sync, recovery
records, and final graph verification distinguish complete, retryable, visible,
and indeterminate outcomes. An uncertain publication is never reported as
complete.

Trust is out of band. Evidence does not embed a key that can authorize itself.
`verify` authenticates the signer role and walks the full graph. `replay` first
authenticates a capture, reconstructs the exact pinned decoder from its plan,
decodes offline, and publishes a new signed child root. It never invokes Codex,
reads a credential, mutates the source capture, or overwrites a root.

Provider-reported input, cached-input, output, reasoning, and total counters are
preserved separately. Cached input remains a subset rather than an invented
discounted token total. Price calculations, quality evaluation, and statistical
reports are derived layers with their own versioned policy and lineage.

## Family-aware quality boundary

Study policy v3 classifies every task as `code`, `review`, or `explain`.
Review/explain tasks preregister exactly two distinct canonical evaluator IDs
and deterministic arithmetic-mean aggregation. Both evaluators independently
receive defensive copies of the same v3 blind packet. The packet contains final
answers, anonymous labels, and preregistered criteria, but no treatment, order,
repetition, trace, token, failure, or other-evaluator metadata.

Verification requires both outputs in preregistered evaluator order, exact
answer/item order, and matching packet nonce and commitment. It preserves each
complete canonical output, its SHA-256, and its treatment-mapped item matrix
before computing the aggregate. Analysis v3 discloses
evaluator identities, exact agreement and disagreement at pair and answer/item
levels, agreement rate, and normalized absolute score differences. Distinct
identifiers enforce protocol completeness and non-aliasing; operational judge
independence remains a study-procedure obligation and must be documented.
Post hoc adjudication is not accepted by v3; an explicit-adjudication design
would require a separately preregistered, versioned protocol.

Code tasks cannot enter the blind prose-evaluator path or contain prose quality
items. A disjoint private-seal `ObjectiveCodeQuality` shape reserves lineage for
future per-arm patch/test outcome digests. This version deliberately exposes no
production constructor and implements no code runner, so unavailable code
quality remains explicit missingness rather than a caller assertion.

## Harness extension boundary

The harness-neutral interface is:

```go
type Adapter interface {
    Kind() string
    Resolve(context.Context, ResolveRequest) (Identity, error)
    MCPArguments(context.Context, MCPServer) ([]string, error)
    Build(context.Context, Invocation) (ProcessSpec, error)
    Decode(context.Context, RawExecution) (Observation, error)
}
```

The fake adapter, shared conformance suite, external-process bridge, and generic
executor let another harness implement and test the boundary without importing
Codex. Such an extension is intentionally non-publishable until reviewed code
adds a concrete production lifecycle, exact effective-config/tool-surface
verification, executable/model allowlists, and an unforgeable constructor. A
caller-defined interface implementation cannot self-declare conformance.

See [docs/adapter-authoring.md](docs/adapter-authoring.md) for the wire protocol
and checklist.
