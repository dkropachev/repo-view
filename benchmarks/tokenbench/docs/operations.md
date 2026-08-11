# Tokenbench operator guide

This guide covers the publishable built-in Codex path. It does not turn a
prototype adapter or evidence created under another identity into conformant evidence.
Read [DESIGN.md](../DESIGN.md) before operating a live run and
[evidence-format.md](evidence-format.md) before moving or disclosing evidence.

## 1. Author immutable inputs

Start with a preregistered task and a clean standalone Git checkout. The source
root must be the repository top level, have a real `.git` directory with no
alternates, contain no modified, untracked, or ignored files, and have an index
that exactly matches `HEAD`. Record full base and head object IDs.

Compute `source_tree_sha256` with the exported
`source.TreeDigest(context.Context, root)` API from
[`source/verify.go`](../source/verify.go). It is a framed digest of checked
tracked paths, modes, directories, and bytes; a Git tree ID or archive hash is
not interchangeable. A trusted suite-authoring helper should call this API and
then recheck that the repository is still clean.

Author one strict [`tokenbench.suite/v2`](../schemas/suite-v2.schema.json)
document. Important bindings are:

- `prompt_file` and `source_root` resolve relative to the suite file when they
  are not absolute;
- `harness_kind` is `codex` for the publishable path;
- `harness_executable` and `git_executable` are canonical absolute paths to the
  exact Codex and static Git files below the artifact-bundle root;
- their SHA-256 values match those files and the artifact manifest;
- `artifact_manifest_sha256` is the digest of the exact canonical manifest
  bytes;
- the supported model/revision and Codex digest are the values documented in
  [README.md](../README.md#current-status);
- prompt and developer-instruction bytes are treatment-neutral and contain no
  scopesifter/tool-use hint.

Unknown, duplicate, null, and trailing fields are rejected. The suite's exact
bytes and prompt bytes are committed to evidence, even though suite JSON does
not have the manifest's compact-encoding requirement.

Code-task authors may separately validate a
[`tokenbench.suite/v3`](../schemas/suite-v3.schema.json) document with the Go
`LoadCodeSuite` API. V3 requires `workspace-write`, an identical visible source
and base revision, and a closed `code_task` binding containing the exact catalog
digest, locked code-family task ID, and toolchain digest. It deliberately has no
writable path, environment, arm, evaluator, or gold-patch field. This is a
load-only authoring boundary: the current CLI and runner do not accept it for
execution, and decoded workspace records cannot grant mount or write authority.

## 2. Build the trusted artifact bundle

The artifact build is a separate reproducible supply-chain step. Build all
images from the provenance named in the manifest, then place this closed set
below one canonical absolute directory:

- Codex, scopesifter, static Git, and static Bash;
- distinct `rg`, `sed`, `awk`, `find`, `head`, `tail`, `wc`, `sort`, `cut`,
  `tr`, `cat`, `ls`, `grep`, and `xargs` images.

Every listed role must be a native static ELF regular file with execute
permission, exactly one filesystem link, a unique relative path, and a unique
SHA-256 digest. Symlinks, hard links, dynamic loaders/dependencies, multicall
applets, and cross-role byte aliases are rejected. Only listed roles enter the
immutable snapshot; an extra bundle file grants no executable authority and
should be excluded from a reproducible bundle.

Write the fixed file `tokenbench-artifacts-v2.json` at the bundle root. Its
shape is defined by
[`artifact-manifest-v2.schema.json`](../schemas/artifact-manifest-v2.schema.json),
but schema validation alone is insufficient: the loader requires the exact
compact bytes produced by Go `json.Marshal(tokenbench.ArtifactManifest)`, with
no trailing newline. The exported `ArtifactManifest.Validate` method checks the
closed roles, relative paths, distinct identities, and provenance before a
trusted authoring helper writes those bytes.

The provenance object records a credential-free absolute source URI, immutable
source revision, SHA-256 of the complete build recipe, and
`sha256:<64-lowercase-hex>` builder-image digest. Artifact paths are
bundle-relative forward-slash paths; each `{path,sha256}` pair names exact file
bytes. Do not edit or reformat the manifest after computing its digest.

## 3. Build the policy-pinned tokenbench executable

Build tokenbench at its final canonical path. A normal development build or
`go run` has no artifact policy and cannot publish a live result.

```sh
manifest=/absolute/artifacts/tokenbench-artifacts-v2.json
manifest_sha256="$(sha256sum "${manifest}" | awk '{print $1}')"

CGO_ENABLED=0 go build -trimpath \
  -ldflags "-X github.com/scopesifter/scopesifter/benchmarks/tokenbench.trustedArtifactManifestSHA256=${manifest_sha256}" \
  -o /absolute/bin/tokenbench \
  ./benchmarks/tokenbench/cmd/tokenbench
```

The executable itself becomes the runner/arm-init image in the immutable
snapshot. It must therefore be a native static ELF executable at one real,
single-link path. Do not replace, relink, or move that file between build and
run. The suite, manifest bytes, and embedded link-time digest must all agree.

## 4. Provision signing and trust files

Provision Ed25519 material outside the repository with an audited key-management
tool. A signing-key file contains exactly the unpadded base64url encoding of one
32-byte Ed25519 seed: no whitespace and no newline. It must be a caller-owned,
single-link regular file with mode `0600` or stricter and must not be a symlink.
Use separate capture and replay keys unless the study policy explicitly
authorizes one key for both roles.

Distribute the public trust policy independently of the CAS and signed root.
It is exact compact JSON, with no newline, in this field order:

```text
schema_version, project, keys[]
key: key_id, public_key, roles, status
```

Its fixed context is:

```text
schema_version = tokenbench.trust-policy/v2
project        = github.com/scopesifter/scopesifter/benchmarks/tokenbench
```

For each key, `public_key` is the unpadded base64url encoding of the raw 32-byte
Ed25519 public key and `key_id` is
`ed25519-sha256:` followed by the lowercase SHA-256 of those public-key bytes.
Keys are strictly sorted by key ID; roles are strictly sorted and drawn from
`capture`, `replay`; status is `active`, `retired`, or `revoked`. Only an active
key with the required role verifies under policy v2. Generate canonical policy
bytes with Go `json.Marshal(evidence.TrustPolicy)` and authenticate those exact
bytes out of band.

The capture signer must already be authorized by the policy passed to `run`.
The policy passed to `replay` must authorize both the parent capture signer and
the new replay signer.

## 5. Preflight paths and host support

Run `tokenbench validate --suite /absolute/study/suite.json` as an early suite,
source, model, and Codex-planning check. It is not artifact loading, immutable
snapshot construction, or execution authority; only `run` performs those live
checks.

For every repetition, choose absent, canonical absolute paths for
`--snapshot-root`, `--state-root`, `--cas`, and `--root-out`. Their real parent
directories must already exist. Those mutable/output paths, the artifact bundle,
source, signing key, trust policy, and recovery path must satisfy the CLI's
disjointness checks. Never reuse a state, snapshot, CAS, or root-output path for
a second repetition.

The provider credential is printable non-whitespace ASCII read exactly once
from inherited descriptor `3..255`. A regular credential source must also be a
caller-owned, single-link, non-symlink file with mode `0600` or stricter and no
newline. Ambient proxy, provider-base-URL, and custom-CA overrides are rejected.

Confirm the Linux prerequisites in [README.md](../README.md#host-prerequisites).
The privileged test script proves kernel mechanics without making a model call:

```sh
benchmarks/tokenbench/scripts/privileged-linux-tests.sh
```

## 6. Run one explicit repetition

```sh
/absolute/bin/tokenbench run \
  --suite /absolute/study/suite.json \
  --artifact-bundle /absolute/artifacts \
  --snapshot-root /absolute/new-snapshot-r0 \
  --state-root /absolute/new-runtime-r0 \
  --cas /absolute/new-cas-r0 \
  --root-out /absolute/new-capture-r0.root.json \
  --credential-fd 3 \
  --signing-key-file /absolute/secrets/capture.seed \
  --trust-policy /absolute/policy/trust.json \
  --repetition 0 \
  3</absolute/secrets/openai-api-key
```

Invoke every authored repetition separately with its explicit zero-based index
and fresh paths. A zero exit plus a canonical root file is the operational
success boundary. Do not treat stdout, a plan, CAS object visibility, or an
answer file as a result.

## 7. Verify and replay safely

Keep the root file byte-exact; adding a newline or reordering fields invalidates
it. Verify it against the same CAS and an independently authenticated policy:

```sh
/absolute/bin/tokenbench verify \
  --cas /absolute/new-cas-r0 \
  --root /absolute/new-capture-r0.root.json \
  --trust-policy /absolute/policy/trust.json
```

Replay is offline. It authenticates the entire parent capture, uses the pinned
built-in decoder, preserves ordinary failures, adds an immutable replay lineage
to the existing CAS, and writes a new exclusive root:

```sh
/absolute/bin/tokenbench replay \
  --cas /absolute/new-cas-r0 \
  --root /absolute/new-capture-r0.root.json \
  --trust-policy /absolute/policy/trust.json \
  --signing-key-file /absolute/secrets/replay.seed \
  --root-out /absolute/new-replay-r0.root.json
```

Verify the replay root with `tokenbench verify`. Replay accepts an original
capture root, not a replay root, and never mutates the parent root or capture
objects.

## 8. Failure and residue handling

Tokenbench fails closed. On any nonzero exit:

- do not publish or analyze the attempted pair as conformant;
- preserve stderr, the exact paths used, and narrowly identified diagnostic
  residue;
- do not recursively delete a snapshot that might still be mounted;
- do not edit, rename, or synthesize a canonical signed-root file;
- do not remove visible CAS objects merely because the root was not finalized.

If `<root-out>.recovery.json` exists, it is a typed recovery record, not
evidence. Preserve it with the CAS and diagnostics. Its state distinguishes an
incomplete publication from a fully published root whose external root file
could not be finalized. There is no best-effort promotion: resolve the storage
or cleanup failure under reviewed recovery procedures, then authenticate the
intended root before making any claim.

For a fresh retry, use a new repetition attempt lineage and entirely new state,
snapshot, CAS, and output paths. Never overwrite or silently select over the
failed attempt.
