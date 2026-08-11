# Smoke suites

This directory intentionally contains no checked-in publishable model-backed
suite or credential. The live runner, immutable capture, verification, and
offline replay paths are implemented, but a benchmark result is valid only for
an explicitly preregistered corpus, exact clean repository state, trusted
artifact bundle, signing policy, and supported Codex/model snapshot.

Unit and conformance fixtures live beside their Go packages and do not make a
model call. Add a smoke suite here only when it:

- uses `tokenbench.suite/v2` and a treatment-neutral prompt;
- pins full source base/head/tree, standalone Git, Codex, artifact-manifest, and
  model-revision identities;
- contains no arm fields, tool hints, opaque config, or secrets;
- has an accompanying preregistered study/quality policy and expected failure
  handling;
- is clearly labeled local/private unless its repository and artifact inputs
  are reproducibly available.

A `plan` file is audit-only. A signed capture/replay root proves integrity and
provenance for its exact inputs; it is not, by itself, a general claim that
scopesifter saves tokens.
