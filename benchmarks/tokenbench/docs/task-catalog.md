# Task catalog v1

`tokenbench.task-catalog/v1` is the strict authoring contract for the ScopeSifter confirmatory corpus. It records task inputs and evaluator identities; it does not execute commands, materialize repositories, generate study policies, or make catalog bytes model-visible.

## Closed corpus

The catalog contains exactly 144 unique tasks. The validator requires the complete Cartesian product of twelve locked repositories, three families (`code`, `review`, `explain`), and four tiers (`small`, `medium`, `large`, `huge`). This produces exactly 12 tasks per repository, 24 per language, 48 per family, and 36 per tier.

Task IDs are derived, never independently named:

```text
<language>.<concise-repo-slug>.<family>.<tier>
```

The concise `scylla-driver` slug occurs in both Rust and Java and is disambiguated by the language. Every task also records the full `corpus-<language>-...` repository slug. Go validation locks each language, concise slug, full slug, upstream URL, and `https://github.com/scopesifter/<repository-slug>` source URL as one tuple.

| Language | Task slug | Corpus repository | Upstream |
|---|---|---|---|
| C++ | `fmt` | `corpus-cpp-fmt` | `fmtlib/fmt` |
| C++ | `seastar` | `corpus-cpp-seastar` | `scylladb/seastar` |
| Go | `chi` | `corpus-go-chi` | `go-chi/chi` |
| Go | `go-git` | `corpus-go-go-git` | `go-git/go-git` |
| Java | `commons-lang` | `corpus-java-commons-lang` | `apache/commons-lang` |
| Java | `scylla-driver` | `corpus-java-scylla-driver` | `scylladb/java-driver` |
| Python | `click` | `corpus-python-click` | `pallets/click` |
| Python | `scylla-ccm` | `corpus-python-scylla-ccm` | `scylladb/scylla-ccm` |
| Rust | `clap` | `corpus-rust-clap` | `clap-rs/clap` |
| Rust | `scylla-driver` | `corpus-rust-scylla-driver` | `scylladb/scylla-rust-driver` |
| TypeScript | `got` | `corpus-typescript-got` | `sindresorhus/got` |
| TypeScript | `kysely` | `corpus-typescript-kysely` | `kysely-org/kysely` |

## Task identity

Each task binds:

- upstream and corpus-copy URLs, a required base Git object ID, an explicitly nullable head object ID, and the source-tree SHA-256; code and review tasks require the comparison head, while explain tasks may omit it, and all IDs in a task use the same Git hash format;
- prompt, toolchain, and hidden evaluator bundle SHA-256 identities;
- one or more sorted direct argument vectors, each with a positive bounded timeout; code tasks require exactly one command ID named `build`, `fail-to-pass`, and `pass-to-pass`, and may add other uniquely named commands;
- the fixed navigation-file, changed-line, and component bounds for its tier;
- sorted objective facts and rubric items using the same blinded quality vocabulary as the study policy; every task has objective facts, review and explain require a bounded rubric, and code may use an empty rubric because its hidden checks are objective;
- sorted, preregistered exclusion conditions;
- for `code` tasks only, the hidden gold-patch SHA-256 and expected result-tree Git object ID.

The catalog contains identities, not hidden evaluator or patch contents. The companion [`task-bundle/v1`](task-bundle.md) contract binds those identities, prompts, and pinned toolchain manifests to bounded opaque CAS references while keeping hidden artifacts outside the model sandbox.

## Canonical encoding

`EncodeTaskCatalog` emits Go's sole compact field-order encoding. `DecodeTaskCatalog` rejects oversized data, invalid UTF-8, duplicate or unknown keys, omitted fields, extra JSON values, and any byte representation that is not exactly canonical. Arrays with semantic identity are required to be strictly sorted and duplicate-free; the encoder never sorts or repairs input.

The JSON Schema artifact documents the closed wire shape and simple bounds. Go validation remains authoritative for the exact corpus distributions, cross-field repository and task-ID mappings, sorted uniqueness, aggregate byte and point ceilings, base/head inequality, and the family-specific gold-patch union.
