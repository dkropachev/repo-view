# Evidence format

> Status: proposed versioned evidence format. No CAS writer, capture bundle, replay, statistics, or report schema exists yet. The current serializable `ResolvedPlan` is an audit artifact only and is not evidence or execution authority.

## Principles

Tokenbench evidence is intended to be:

- **immutable:** capture and derived analysis are never edited in place;
- **content-addressed:** every object is named and verified by a cryptographic digest;
- **self-describing:** manifests pin schemas, producers, adapters, decoders, and repository inputs;
- **replayable:** token decoding, quality checks, and reporting run without a live model;
- **complete about failure:** incomplete, invalid, excluded, and retried attempts remain visible;
- **secret-free:** credential values and unrestricted environment dumps are prohibited.

## Current audit artifact versus future evidence

Current `Pair.Plan(ctx)` serializes the suite/prompt digests, two semantic invocations, resolved model/adapter/source/tokenbench-executable identities, parity proof, rendered process pair, and rendered-process digest. `ResolvedPlan.Validate` recomputes parity and process commitments and checks the rendered sole-delta shape; `DecodePlan` also rejects invalid UTF-8, duplicate/unknown fields, and trailing JSON.

Validation does not reread the suite, source, `.git`, Git executable, tokenbench/harness/MCP executables, or adapter configuration. A decoded plan contains the verified `ProcessPair`, but not the retained adapter capability, so it cannot call `Pair.Build` or authorize execution. A future capture manifest may reference a plan digest, but must also prove fresh suite preparation, build-time re-resolution/reverification, actual process launch, and raw capture.

## Proposed bundle model

A bundle is a small canonical manifest that refers to immutable objects in a content-addressed store (CAS):

```text
bundle manifest
  -> canonical run specification
  -> corpus/task objects
  -> requested/resolved model and adapter wrapper/child identities
  -> source tree, standalone .git metadata, Git executable, and tokenbench executable identities
  -> pair/attempt manifests
       -> requested and effective configuration
       -> prompt and repository identity
       -> verified common process and candidate MCP argv suffix
       -> live MCP handshake/server/tool-surface record
       -> raw stdout/stderr/event streams
       -> response and exit record
       -> raw usage events
       -> parity and quality results
  -> optional derived analysis/report objects
```

Objects use a versioned media type, byte length, and a digest such as `sha256:<lowercase-hex>`. Canonical JSON should use one pinned serialization rule before hashing; opaque raw streams are hashed byte-for-byte.

A directory layout may cache objects as `objects/sha256/<prefix>/<remainder>` and keep named bundle manifests separately. Paths are an implementation detail; the digest is the identity.

## Bundle kinds and lineage

- **capture bundle:** raw outputs and execution metadata from paired attempts.
- **replay bundle:** normalized usage/events produced from one capture bundle by pinned decoders.
- **analysis bundle:** paired metrics and quality results derived from capture or replay.
- **report bundle:** rendered tables and narrative derived from an analysis bundle.
- **legacy-import bundle:** immutable preservation of historical artifacts and their limitations.

Every derived manifest lists parent bundle digests and producer identity. Changing a decoder, price table, quality rule, or report template creates a new derived bundle. A convenient mutable alias such as `latest` may exist outside evidence, but it is never a source of provenance.

## Root manifest fields

The planned root manifest records at least:

- schema name/version and bundle kind;
- bundle digest and creation timestamp;
- producer source revision plus canonical tokenbench executable path/digest;
- parent bundle digests;
- canonical run-spec and corpus digests;
- repository locator, full base/head IDs, tracked-tree digest, standalone `.git` metadata digest, canonical Git executable path/digest, and tokenbench executable identity;
- requested model and exact resolved `<model>@<immutable-revision>`;
- adapter executable, keyed wrapper-control, child effective-configuration, version, harness executable/version, decoder, quality-policy, and price-table identities as applicable;
- pair-manifest digests and declared attempt counts;
- integrity status and explicit limitations;
- classification such as `conformant`, `invalid`, or `legacy-nonconformant`.

Timestamps describe evidence creation, not object identity, unless a future schema explicitly includes them in canonical bytes.

## Pair and attempt records

A pair record links task, repetition, randomization seed/position, baseline attempt, candidate attempt, and any retry chain. Each attempt should record:

- arm and lifecycle state;
- exact prompt object digest and role sequence;
- repository/tree, standalone `.git`, pinned Git executable, tokenbench executable, and model-visible path identities;
- requested model, resolved immutable revision, reasoning, and canonical requested/effective configuration object digests;
- harness identity plus distinct adapter executable, wrapper-control, and child effective-config digests;
- the candidate registration digest, or its verified absence for baseline;
- common `ProcessSpec` digest and the exact candidate-only MCP argv suffix;
- live MCP handshake, server identity, and read-only tool-schema result;
- start/end observations and timeout policy;
- raw stdout, stderr, structured event, tool-handshake, response, and exit object digests;
- provider usage event digests, not only derived totals;
- parity, infrastructure, decoder, and quality outcomes;
- retry predecessor and exclusion reason when applicable.

Baseline and candidate prompt, model, adapter, source/Git, and protected configuration commitments must match. Baseline records an empty MCP array; candidate records exactly one `repo_view` element. Candidate process must be a central clone of the one adapter-built common process plus only the recorded MCP suffix. The future live record must show that this registration actually resolved to the expected read-only server/tool surface.

## Usage records

Normalized usage is derived, never substituted for raw events. A record should include value, unit, source event/object, decoder version, and overlap semantics for:

- input tokens;
- cached input tokens;
- output tokens;
- reasoning tokens;
- provider total tokens;
- any harness-specific counters.

A normalized total must identify the non-overlapping components used. Missing and ambiguous are distinct from zero. Financial cost points to a separate immutable price-table object and never overwrites token fields.

## Atomic publication and integrity

A planned writer should:

1. stream each object to staging while hashing;
2. verify length and digest;
3. make the object visible idempotently;
4. write and verify child manifests;
5. publish the root manifest atomically last.

Interrupted staging data is not a bundle. A validator walks every reference, rejects digest or media-type mismatch, checks schema versions/counts, and reruns strict arm/process parity from captured effective configuration. Execution cannot be reconstructed by trusting a decoded plan: the live runner must have reloaded/prepared the suite and preserved build-time and launch-time verification records. Corrupt source evidence is never repaired in place.

## Replay contract

Replay accepts a capture or prior derived bundle by digest. It must not access a live model, mutate the repository, depend on ambient user configuration, or overwrite its parent. It verifies all input digests, uses explicitly selected decoder/checker versions, and emits a new manifest even when derivation fails.

Determinism applies to the same input object set and producer versions. Presentation timestamps or local output paths should be excluded from canonical derived data.

## Redaction and sensitive data

Do not collect credential values, authorization headers, unrestricted process environments, adapter commitment keys, or unrelated home-directory configuration. Prefer allowlisted fields and value digests. A secret-bearing external-adapter control environment is represented by the wrapper's keyed HMAC-SHA-256 commitment, not by its values or key. If task content requires deterministic redaction, redact before CAS insertion, record the redaction policy identity, and classify the bundle accordingly; do not rewrite a published object.

Repository content and model responses may still be sensitive. Storage and publication policy are separate from cryptographic integrity and must honor the repository's access controls.

## Schema evolution

Schemas carry explicit versions and reject unknown incompatible fields. Additive extensions require documented canonicalization. Incompatible changes receive a new version and migration decoder; historical object bytes remain untouched. Reports must disclose when bundles with different accounting or parity schema versions are compared.
