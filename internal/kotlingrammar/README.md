# Generated Kotlin grammar

`language_generated.go` is generated from
[`tree-sitter-kotlin`](https://github.com/fwcd/tree-sitter-kotlin)
commit `1852ea17b7f60fb3f9d84e0b1555d56b46b39fb1` (2026-08-01) with
`github.com/dcosson/treesitter-go/cmd/tsgo-generate@v0.1.0`.

The pinned inputs are:

- `src/parser.c`: `70f193db454cfb1315d17d2d85879619e4b62295325bc4cbd4fe0f9fb96098e1`
- `src/scanner.c`: `8a300c7da25290d5de076605fb46cc6b53b188d99aa9e8f34e928dbb7191935f`

Regenerate from a clean checkout at that commit through the Make entrypoint:

```text
make -f make/grammar.mk generate-kotlin-grammar GRAMMAR_SOURCE=/path/to/tree-sitter-kotlin
```

The generator normally targets a package inside `treesitter-go` and imports
its `internal/core` package. The Go generator deterministically redirects those
references to the runtime's public facade, which re-exports the same types and
constants. It moves the two largest numeric literals into the deterministic,
little-endian `language_tables.bin` asset; `tables.go` validates and decodes
that trusted asset once when the cached language is first used. This keeps Go
analysis of the generated package within practical memory limits. The external
scanner in `scanner.go` is a bounded pure-Go port of the pinned `src/scanner.c`.

The upstream grammar and generated tables are MIT licensed; see
`LICENSE.tree-sitter-kotlin` and the repository's `THIRD_PARTY_NOTICES.md`.
