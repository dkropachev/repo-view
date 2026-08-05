#!/usr/bin/env bash
set -euo pipefail

readonly upstream_commit="9150f7d56bb47f1a809fa23623f1ba1413e93fa9"
readonly parser_sha256="2549deeed0c8aeb84f42f9ccd3cf9de047a0c609387075a97784fddb2d1770cd"
readonly scanner_sha256="2ee1241a6a275e72a06838f5df927700bd405c16b48f986e2c33d1264cae4818"
readonly generator_version="v0.1.0"

if [[ $# -ne 1 ]]; then
  echo "usage: $0 /path/to/tree-sitter-c-sharp" >&2
  exit 2
fi

readonly source_root="$(cd "$1" && pwd -P)"
readonly script_root="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
readonly parser_source="$source_root/src/parser.c"
readonly scanner_source="$source_root/src/scanner.c"
readonly output="$script_root/language_generated.go"
readonly table_output="$script_root/language_tables.bin"

if [[ "$(git -C "$source_root" rev-parse HEAD)" != "$upstream_commit" ]]; then
  echo "tree-sitter-c-sharp must be checked out at $upstream_commit" >&2
  exit 1
fi
if ! git -C "$source_root" diff --quiet -- src/parser.c src/scanner.c; then
  echo "tree-sitter-c-sharp parser or scanner has local changes" >&2
  exit 1
fi

hash_file() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum "$1" | awk '{print $1}'
    return
  fi
  if command -v shasum >/dev/null 2>&1; then
    shasum -a 256 "$1" | awk '{print $1}'
    return
  fi
  echo "sha256sum or shasum is required" >&2
  return 1
}

if [[ "$(hash_file "$parser_source")" != "$parser_sha256" ]]; then
  echo "unexpected parser.c checksum" >&2
  exit 1
fi
if [[ "$(hash_file "$scanner_source")" != "$scanner_sha256" ]]; then
  echo "unexpected scanner.c checksum" >&2
  exit 1
fi

generated="$(mktemp "${TMPDIR:-/tmp}/csharp-language.XXXXXX.go")"
rewritten="$(mktemp "${TMPDIR:-/tmp}/csharp-language-public.XXXXXX")"
tables="$(mktemp "${TMPDIR:-/tmp}/csharp-language-tables.XXXXXX.bin")"
trap 'rm -f "$generated" "$rewritten" "$tables"' EXIT

go run "github.com/dcosson/treesitter-go/cmd/tsgo-generate@$generator_version" \
  -parser "$parser_source" \
  -package csharpgrammar \
  -output "$generated"

# Generated grammars normally live inside treesitter-go and import its
# internal core package. The public facade re-exports every generated type and
# constant, so point the generated table at that facade for use in this module.
# Correct the generator's legacy ABI metadata constant to the pinned parser's
# LANGUAGE_VERSION while doing the deterministic import rewrite.
sed \
  -e 's#core "github.com/dcosson/treesitter-go/internal/core"#core "github.com/dcosson/treesitter-go"#' \
  -e '/language "github.com\/dcosson\/treesitter-go\/language"/d' \
  -e 's/language\.Language/core.Language/g' \
  -e 's/Version:                14,/Version:                15,/' \
  "$generated" > "$rewritten"

go run "$script_root/compact.go" "$rewritten" "$tables"
gofmt -w "$rewritten"
chmod 0644 "$rewritten" "$tables"
mv "$rewritten" "$output"
mv "$tables" "$table_output"
