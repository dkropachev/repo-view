#!/usr/bin/env bash
set -euo pipefail

readonly upstream_commit="1852ea17b7f60fb3f9d84e0b1555d56b46b39fb1"
readonly parser_sha256="70f193db454cfb1315d17d2d85879619e4b62295325bc4cbd4fe0f9fb96098e1"
readonly scanner_sha256="8a300c7da25290d5de076605fb46cc6b53b188d99aa9e8f34e928dbb7191935f"
readonly generator_version="v0.1.0"

if [[ $# -ne 1 ]]; then
  echo "usage: $0 /path/to/tree-sitter-kotlin" >&2
  exit 2
fi

readonly source_root="$(cd "$1" && pwd -P)"
readonly script_root="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
readonly parser_source="$source_root/src/parser.c"
readonly scanner_source="$source_root/src/scanner.c"
readonly output="$script_root/language_generated.go"
readonly table_output="$script_root/language_tables.bin"

if [[ "$(git -C "$source_root" rev-parse HEAD)" != "$upstream_commit" ]]; then
  echo "tree-sitter-kotlin must be checked out at $upstream_commit" >&2
  exit 1
fi
if ! git -C "$source_root" diff --quiet -- src/parser.c src/scanner.c; then
  echo "tree-sitter-kotlin parser or scanner has local changes" >&2
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

generated="$(mktemp "${TMPDIR:-/tmp}/kotlin-language.XXXXXX.go")"
rewritten="$(mktemp "${TMPDIR:-/tmp}/kotlin-language-public.XXXXXX")"
tables="$(mktemp "${TMPDIR:-/tmp}/kotlin-language-tables.XXXXXX.bin")"
trap 'rm -f "$generated" "$rewritten" "$tables"' EXIT

go run "github.com/dcosson/treesitter-go/cmd/tsgo-generate@$generator_version" \
  -parser "$parser_source" \
  -package kotlingrammar \
  -output "$generated"

# Generated grammars normally live inside treesitter-go and import its
# internal core package. The public facade re-exports every generated type and
# constant, so point the generated table at that facade for use in this module.
sed \
  -e 's#core "github.com/dcosson/treesitter-go/internal/core"#core "github.com/dcosson/treesitter-go"#' \
  -e '/language "github.com\/dcosson\/treesitter-go\/language"/d' \
  -e 's/language\.Language/core.Language/g' \
  "$generated" > "$rewritten"

go run "$script_root/compact.go" "$rewritten" "$tables"
gofmt -w "$rewritten"
chmod 0644 "$rewritten" "$tables"
mv "$rewritten" "$output"
mv "$tables" "$table_output"
