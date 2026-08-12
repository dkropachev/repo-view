# Contributing to tokenbench

Start with the invariant: a paired run is identical except that candidate has
one `scopesifter` MCP registration and baseline has none. Read [DESIGN.md](DESIGN.md)
and [AGENTS.md](AGENTS.md) before changing code.

## Change workflow

1. Name the boundary being changed: authored policy, origin verification,
   immutable snapshot, parity, adapter, process containment, capture, evidence,
   replay, quality, or statistics.
2. Open or link a focused issue. Keep foundational, live-execution,
   methodology and reporting changes in reviewable stages.
3. Update the relevant schema/design text with the implementation. Never
   advertise a command or guarantee that the checked-in code does not provide.
4. Add a positive fixture and adversarial tests for malformed, aliased,
   drifting, interrupted, and cleanup-failure states at that boundary.
5. Run the focused tests, full Go suite, race/vet/lint/build checks, and any
   required privileged kernel lane.
6. Inspect the complete diff, including generated schema/golden changes and
   unrelated worktree modifications. Preserve user-owned changes.
7. Keep the pull request draft until the implementation, documentation, and
   required checks are green. Merge only the exact reviewed commit.

## Review questions

Every runtime/evidence change should answer these explicitly:

- Can caller-authored JSON, an exported struct, an interface implementation, or
  a boolean forge execution/publication authority?
- Can any mutable pathname change between verification and use, escape through
  a symlink/mount/hard link, or share an inode with another role?
- Are baseline and candidate prompt, model, source, process, environment,
  native tools, permissions, resource policy, routing, and model-visible paths
  exact matches after removing only the MCP registration?
- Is common state built once and candidate derived centrally rather than by an
  independent adapter branch?
- Does the live effective configuration and provider tool surface prove the
  same sole delta that the requested plan claims?
- Does containment cover every descendant and prove its own kernel state?
- Can any failure, cancellation, overflow, response attempt, cleanup error, or
  missing counter disappear from the evidence?
- Are credentials, signing material, and ambient configuration excluded from
  child state and serialized output?
- Is publication atomic, durable, authenticated out of band, graph-verified,
  and incapable of rewriting a parent during replay?
- Can all non-model behavior be tested deterministically without a live API
  credential?

## Contract-specific expectations

### Schemas and canonical data

Reject unknown and duplicate fields, trailing JSON, invalid UTF-8, ambiguous
null/default values, noncanonical ordering/encoding where required, and inputs
above explicit limits. Incompatible changes get new schema/media versions;
historical decoders and bytes remain unchanged.

### Parity and adapters

Test every protected field, empty-versus-nil collections, duplicate/aliased
registrations, prompt treatment hints, common build count, suffix-only process
derivation, adapter identity drift, and requested/effective/provider config
agreement. Follow [docs/adapter-authoring.md](docs/adapter-authoring.md).

### Filesystems and executables

Test dirty/ignored/transient Git state, alternates and linked worktrees,
symlinks, hard links, mount descendants and propagation, cross-device copy,
static/native ELF enforcement, fs-verity measurement, same-content replacement,
mutation races, failed unmount/descriptor close, and retained residue.

### Process and kernel boundaries

Test the exact allowed and denied filesystem/network/syscall sets, cgroup direct
and effective BPF programs, finite resource controls, atomic placement, process
tree kill/reap, PID namespace, capability sets, x32/architecture validation,
and transient cleanup retry. A mandatory privileged test may not silently skip.

### Evidence and replay

Test object collision/corruption, partial write, inode reuse, concurrent
publication, directory-sync uncertainty, missing/transposed references,
signature/trust-role failures, one-shot authority, recovery output, offline
decoder binding, and parent immutability.

### Quality and statistics

Test preregistration canonicality, secret commitments, answer-label
randomization, evaluator isolation, swapped/tampered output, objective fact
scoring, missing/failure/exclusion accounting, paired estimators, exact and
Monte Carlo paths, deterministic bootstrap, thresholds, and small/adversarial
samples. Report raw paired observations with uncertainty and quality gates.

### Product identity

Current contracts are bound to ScopeSifter. Pre-rename evidence is preserved
in Git history, but current loaders do not decode, translate, or republish it.

## Verification matrix

Run the narrow package while developing, then before review:

```sh
go test ./... -count=1
go test -race ./benchmarks/tokenbench/... -count=1
go vet ./...
go build ./cmd/... ./benchmarks/tokenbench/cmd/...
golangci-lint run --config=.golangci.yml
golangci-lint run --config=.golangci-fieldalignment.yml
git diff --check
```

Also validate tracked JSON files and compile platform-specific files for their
supported GOOS/GOARCH combinations. Kernel-boundary changes require:

```sh
make -f make/tokenbench.mk tokenbench-privileged-linux
```

Live model calls are never a unit-test prerequisite and should not run in
ordinary CI. A green privileged lane proves kernel mechanics, not a production
model result.
