# Evidence format

Tokenbench currently publishes two immutable bundle kinds: a raw `capture` and
an offline-derived `replay`. Both have an Ed25519-signed attestation envelope as
their public root. A plan by itself is audit data, not evidence or execution
authority.

## Object references and CAS

Every object is identified by:

```json
{"digest":"sha256:<64 lowercase hex>","size":123,"media_type":"type/subtype"}
```

The digest covers exact bytes. Media types are lowercase and parameter-free at
the CAS layer; an adapter artifact's original parameterized media type is stored
inside its semantic envelope.

The CAS is append-only and bounded. It accepts an absolute, canonical, real
directory on one filesystem. A transaction:

1. creates an exclusive private staging directory and lease;
2. writes each object through a retained inode pin while hashing and bounding
   bytes;
3. verifies mode, link count, size, inode identity, and digest;
4. publishes with atomic no-replace semantics, accepting an existing path only
   when its exact bytes and metadata verify;
5. syncs objects and directories before publishing the designated root last;
6. reopens and verifies the complete expected object graph;
7. removes and syncs private staging state.

Stale recovery is lease-aware and validates every owned name/inode. Publication
states distinguish `retryable`, `durable`, `visible`, and `indeterminate`; a
later sync/cleanup failure cannot be collapsed into success. When visibility is
uncertain, the CLI writes an exclusive typed recovery record instead of a
canonical root file. Cleanup residue is never accepted as evidence.

## Signed root

The public root references
`application/vnd.tokenbench.attestation.v1+json`. Its canonical
envelope contains:

- project context;
- signer key ID;
- bundle kind (`capture` or `replay`);
- exact subject `ObjectRef`;
- sorted parent signed-root references;
- an unpadded base64url Ed25519 signature over a domain-separated canonical
  statement.

No public key is embedded as authority. `verify`, `run`, and `replay` require an
independently supplied canonical `tokenbench.trust-policy/v1`. The policy binds
each `ed25519-sha256:<public-key-digest>` ID to an active/retired/revoked status
and sorted allowed roles. Current verification rejects retired and revoked keys
because v1 intentionally has no trusted timestamp.

The root reference file emitted by the CLI is itself the exact compact canonical
JSON encoding of the attestation `ObjectRef`; it is not an alias or filename
lookup.

## Capture v3 graph

The signed capture subject media type is
`application/vnd.tokenbench.capture.v3+json`:

```text
signed capture root
  -> capture v3 subject
       -> plan v3
            -> exact suite/prompt commitments
            -> sole-delta invocations and rendered processes
            -> trusted artifact manifest/provenance
            -> origin and immutable execution inputs
            -> parity and process commitments
       -> baseline attempt
            -> stdout bytes
            -> stderr bytes
            -> sanitized Codex artifacts
            -> optional normalized observation OR ordinary failure
            -> exact termination/truncation/resource state
       -> candidate attempt (same shape)
       -> executor identity, randomized order, repetition
```

The plan is bounded to 64 MiB before either arm starts and uses
`application/vnd.tokenbench.plan.v3+json`. It is marked publishable only when it
contains the trusted artifact audit and immutable origin/execution inputs
created by live private authority. `DecodePlan` strictly validates all
commitments but never recreates that authority.

Raw process streams are stored byte-for-byte. Adapter artifacts include the
Codex effective configuration and either a complete or sanitized partial
Responses trace. The trace commits ordered provider request/response attempts,
exact body digests, bounded SSE events, canonical reviewed-semantic-header
digests, TLS identities, dynamic fields, provider model, usage, tool
declarations/calls, and capture errors within fixed limits. It does not claim
to retain raw HTTP/2 framing, header order, or header-name casing.
Authorization headers and upstream credentials are excluded.

Complete traces use
`application/vnd.tokenbench.codex.responses-trace.v3+json`; ordinary terminal
process failures use
`application/vnd.tokenbench.codex.partial-responses-trace.v3+json`. A partial
trace preserves bounded audit evidence but does not turn a provider-routing,
capture, parity, or cleanup failure into publication authority.

An attempt has exactly one of these outcomes:

- successful decoding with one canonical normalized observation;
- a recorded ordinary execution/decode failure plus its raw/partial evidence.

An integrity failure never becomes a signed capture. Capture loading verifies
the signature and trust role, every object ref and exact media type, plan parity,
built-in Codex executable/model/adapter allowlists, common process shape,
attempt/arm/order consistency, normalized observations, and the full transitive
graph.

## Replay v1 graph

The signed replay subject media type is
`application/vnd.tokenbench.replay.v1+json`:

```text
signed replay root
  -> replay v1 subject
       -> parent signed capture root
       -> exact built-in decoder identity
       -> baseline decoded observation or retained failure
       -> candidate decoded observation or retained failure
```

Replay first authenticates and reconstructs the full parent. It binds the exact
Codex adapter/decoder configuration from the capture plan, decodes only
successful raw attempts, preserves ordinary failures, validates the
reconstructed run, and authenticates the parent again immediately before
staging the child. Parent run identity must be byte-stable across decoding.

Replay receives no credential, model client, live repository, clock, random
source, or writable parent capability. Any decoder failure publishes nothing.
The derived root names the parent signed root, not an unsigned subject. Loading a
replay recursively authenticates both lineages and validates the observations
again.

## Token accounting

Normalized observations preserve provider-native counters separately:

- input tokens;
- cached input tokens (a subset of input);
- output tokens;
- reasoning tokens (with their documented containment semantics);
- provider-reported total when present.

Missing is distinct from zero. Codex JSONL usage, provider body usage, provider
header/model state, and raw trace must agree. Tokenbench does not add cached
input twice or create a discounted “effective token” total. Pricing, exchange
rates, and cost formulas are separate versioned derived inputs and are not part
of capture/replay v1.

## Publication authority and signing order

`PublishRun` accepts only a `Run` sealed by a live publishable `Pair.Execute`.
The seal snapshots and digests the run and carries a one-shot private state
machine. Acquisition fails unless all of these remain true:

- exact live run bytes equal the sealed snapshot;
- the conformant executor has killed/removed all arm state and closed;
- the Codex proxy/lifecycle has closed its listener, state, sessions, and
  upstream credential;
- the immutable snapshot authority has unmounted and closed every descriptor.

The signing key is loaded only after that boundary. Publication consumes the
authority exactly once; failed/incomplete publication cannot be retried with a
mutated run object.

`ReplayCapture` likewise authenticates its signer against the replay role before
publication and signs only after the full parent/decoder checks. Signer seeds
and trust policy are not CAS objects.

## Sensitive data

Never store:

- upstream API credentials or secret-source descriptors;
- authorization/cookie headers;
- signing seeds/private keys;
- an ambient or unrestricted process environment;
- unrelated user home/Codex configuration;
- credential-bearing URLs or provenance.

Exact prompt, repository content, stdout/stderr, model answers, and sanitized
tool traces may still be sensitive. Cryptographic integrity does not authorize
publication; storage and disclosure must follow the repository/data owner’s
policy.

## Evolution and derived studies

Schemas and media types are explicit and incompatible changes receive new
versions. Historical object bytes and decoders remain immutable. Blinded quality
results, paired statistics, cost tables, and rendered reports must be published
as new typed descendants with their own policy/producer identities; they may
reference a replay/capture lineage but must never edit it or hide failed pairs.
