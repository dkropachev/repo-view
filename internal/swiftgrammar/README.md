# Generated Swift grammar

`language_generated.go` is generated from
[`tree-sitter-swift`](https://github.com/alex-pinkus/tree-sitter-swift)
commit `8d02b7ff390a17a43ce90c4e987c49315cfc4be6` (2026-08-02).
The upstream repository deliberately omits `parser.c`, so generation has two
pinned stages:

1. `tree-sitter-cli@0.23.0` generates an ABI 14 `parser.c` from the tracked
   `src/grammar.json`.
2. `github.com/dcosson/treesitter-go/cmd/tsgo-generate@v0.1.0` converts that
   parser to the pure-Go runtime representation.

The pinned upstream inputs are:

- `src/grammar.json`: `081ee7a9601afc12869659d407729a4024e5c8e1c21cc46aed3387502d430156`
- `src/scanner.c`: `380edc27e2020e5ba2d6415c9f6c0065965771d60138ae53372858e7b1f92e3b`
- `grammar.js`: `ca35fad5fa249e836f86127dd3daf0a6d7b647a5dd16a0f45a366fe03a794a6c`
- `package-lock.json`: `1f05b21cc01d5a506ae8ccdf79aba0a5223137b636da03cbb41aa740dbe0c75f`
- `LICENSE`: `3533cec129bb4bba015c0d61d86dd7c3b7e82110e4d2ff7837a01eff5bad5ccc`

The generated ABI 14 `parser.c` has SHA-256
`9df63e0b6680f0b6cf1f1df613aaff2a7a4a3d9c9eb573b28b5d5c33fdaf7494`;
this is byte-for-byte identical to the artifact published by upstream's
successful GitHub Actions run for the pinned commit. The raw generated Go file
has SHA-256
`2ac2fa39c03d62e84b4e602366506573c54889ed39054b5476ba7b2ba6ff8e4e`.
After deterministic rewriting, compaction, and lexer splitting, the committed
outputs have these SHA-256 digests:

- `language_generated.go`: `4745d5b74c074b322e6bf49330b321ffa9c41b93e851a4cde5a3f1a04d9c1534`
- `language_tables.bin`: `1c17f16cf9d32b9ac851816c940736d04c0ce16b1242883bd4ed758e8245e50b`

Regenerate from a clean checkout at that commit through the Make entrypoint:

```text
make -f make/grammar.mk generate-swift-grammar GRAMMAR_SOURCE=/path/to/tree-sitter-swift
```

The generator normally imports the runtime's internal packages. The Go tool
deterministically redirects those references to the public facade. It moves
the two largest numeric literals into `language_tables.bin`, then splits the
2,089-state generated lexer into small helpers. Splitting is semantic-preserving
and prevents ordinary Go optimization of one enormous function from consuming
multiple gigabytes of memory. `tables.go` validates and decodes the trusted,
little-endian table asset once when the cached language is first used.

The external scanner in `scanner.go` is a bounded pure-Go port of the pinned
`src/scanner.c`. The upstream grammar and generated tables are MIT licensed;
see `LICENSE.tree-sitter-swift`.

The generator also copies the ten checksum-pinned upstream corpus files into
`testdata/tree-sitter-swift-corpus`. `TestSwiftPinnedUpstreamCorpus` parses all
242 examples with the generated pure-Go grammar and compares their normalized
concrete trees with the upstream expectations, including the expected recovery
trees. This exercises the generated tables and external-scanner port together.
