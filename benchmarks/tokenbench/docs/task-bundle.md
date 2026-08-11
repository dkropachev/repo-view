# Task bundle v1

`tokenbench.task-bundle/v1` is the strict artifact-reference companion to a
decoded `tokenbench.task-catalog/v1`. It binds the catalog's canonical SHA-256
and all 144 task IDs to the exact authored inputs needed by later execution
work. The wire contract does not read a filesystem, open a CAS object, or
execute a toolchain or evaluator. `LoadAuthenticatedTaskBundle` is the
separate, capability-based object-loading boundary described below.

## Closed artifact roles

Every task has exactly one lexically sorted reference for each of these roles:

- `prompt`, bound to the task's `prompt_sha256`;
- `toolchain_manifest`, bound to `toolchain_sha256` and intended to describe a
  fully pinned build/test environment;
- `hidden_evaluator_bundle`, bound to
  `hidden_evaluator_bundle_sha256` and kept outside the model sandbox. The
  catalog digest is the SHA-256 of these canonical JSON bundle-descriptor
  bytes, identified by
  `application/vnd.tokenbench.hidden-evaluator-bundle.v1+json`; it is not a
  digest of an unstated executable or archive payload;
- `gold_patch`, bound to `gold_patch.patch_sha256`, present if and only if the
  catalog task family is `code`.

The complete manifest therefore has 144 task entries and 480 references: three
per task plus one for each of the 48 code tasks. Task IDs must equal the supplied
catalog in exact sorted order. The bundle also commits the canonical catalog
digest, so the same artifact list cannot be replayed against another valid
catalog revision.

## Opaque object identity

Each role contains a `cas.ObjectRef` with `sha256:<64 lowercase hex>`, a positive
bounded byte size, and the role's fixed vendor media type. References contain no
pathname, URI, environment variable, or loader option. Repeated digests must
carry identical size and media-type metadata, while different roles cannot
silently alias one digest under conflicting metadata. Manifest validation
authenticates only this description; artifact bytes are not trusted until the
loader verifies them.

Role-specific object ceilings are 1 MiB for prompts, 4 MiB for pinned toolchain
manifests, 64 MiB for hidden evaluator bundle descriptors, and 16 MiB for gold patches.
The encoded bundle is capped at 1 MiB and all references together at 8 GiB.

## Canonical encoding

`EncodeTaskBundle` validates against the supplied catalog and emits the sole
compact field-order encoding. `DecodeTaskBundle` rejects empty or oversized
input, invalid UTF-8, unknown or duplicate keys, omitted values, JSON `null`,
extra values, alternate whitespace or field order, unsorted or duplicate task
and role identities, family-role drift, and any catalog/object digest mismatch.
It never sorts or repairs author input.

The versioned JSON Schema documents the closed shape, role media types, simple
size limits, and code/non-code role union. Go validation remains authoritative
for equality to the external catalog, canonical order, exact aggregate counts,
cross-document digests, repeated-object metadata, and aggregate byte limits.

## Authenticated loading boundary

`LoadAuthenticatedTaskBundle` accepts a validated catalog/bundle value and one
caller-supplied `TaskArtifactAuthority`. The authority exposes only
`Copy(context.Context, cas.ObjectRef, io.Writer)`: there is no path, URI,
environment variable, archive name, or command input. `*cas.Store` implements
that capability using its rooted, immutable object checks.

Loading first canonical-encodes and decodes the complete catalog/bundle pair,
creating an independently owned deep snapshot before the first reentrant
authority call. It then visits snapshot tasks and roles in canonical order and
streams every distinct object to a hashing sink,
independently checking the exact SHA-256 and byte count even if a different
authority implementation is supplied. The role's catalog digest, fixed media
type, positive size, per-role ceiling, and aggregate ceiling were already
checked by `TaskBundle.Validate`. Exact repeated references in the same role are
authenticated once; conflicting metadata or cross-role reuse fails closed.
Cancellation, a missing/corrupt object, short or oversized output, an authority
error after partial output, or any later context error returns no loaded bundle.

The successful result retains typed object identities, not artifact bytes.
Task lookup accepts only an exact validated task ID, roles remain the closed
family-specific set, and returned task/role lists and identities are defensive
copies. `AuthenticatedTaskArtifact.Verify` streams an object to a discard sink.
`AuthenticatedTaskArtifact.Read` reopens and reauthenticates the exact object
before returning a fresh caller-owned byte slice, so replacement between load
and use is detected and corrupt partial bytes never escape. A read allocates at
most one role-bounded object (64 MiB maximum), rather than retaining the
bundle's possible 8 GiB of references.

This boundary deliberately treats prompt text, toolchain JSON, hidden evaluator
JSON, and gold-patch text as opaque bytes. It does not parse, unpack, mount,
execute, or place any of them in a model sandbox. Callers must keep hidden
evaluator and gold artifacts outside that sandbox and add format-specific
validation at a later, separately reviewed boundary.
