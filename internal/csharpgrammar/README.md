# Generated C# grammar

`language_generated.go` is generated from
[`tree-sitter-c-sharp`](https://github.com/tree-sitter/tree-sitter-c-sharp)
commit `9150f7d56bb47f1a809fa23623f1ba1413e93fa9` (2026-07-15) with
`github.com/dcosson/treesitter-go/cmd/tsgo-generate@v0.1.0`.

The pinned inputs are:

- `src/parser.c`: `2549deeed0c8aeb84f42f9ccd3cf9de047a0c609387075a97784fddb2d1770cd`
- `src/scanner.c`: `2ee1241a6a275e72a06838f5df927700bd405c16b48f986e2c33d1264cae4818`

Regenerate from a clean checkout at that commit through the Make entrypoint:

```text
make -f make/grammar.mk generate-csharp-grammar GRAMMAR_SOURCE=/path/to/tree-sitter-c-sharp
```

The generator normally targets a package inside `treesitter-go` and imports
its `internal/core` package. The Go generator deterministically redirects those
references to the runtime's public facade, which re-exports the same types and
constants, and records the source grammar's ABI 15 metadata. It moves the two
largest numeric literals into the deterministic, little-endian
`language_tables.bin` asset; `tables.go` validates and decodes that trusted
asset once when the cached language is first used. This keeps Go analysis of
the generated package within practical memory limits. The external scanner in
`scanner.go` is a bounded pure-Go port of the pinned `src/scanner.c`.

The upstream grammar and generated tables are MIT licensed; see
`LICENSE.tree-sitter-c-sharp` and the repository's `THIRD_PARTY_NOTICES.md`.
