#!/usr/bin/env bash
set -euo pipefail

readonly upstream_commit="8d02b7ff390a17a43ce90c4e987c49315cfc4be6"
readonly grammar_sha256="081ee7a9601afc12869659d407729a4024e5c8e1c21cc46aed3387502d430156"
readonly scanner_sha256="380edc27e2020e5ba2d6415c9f6c0065965771d60138ae53372858e7b1f92e3b"
readonly grammar_js_sha256="ca35fad5fa249e836f86127dd3daf0a6d7b647a5dd16a0f45a366fe03a794a6c"
readonly package_lock_sha256="1f05b21cc01d5a506ae8ccdf79aba0a5223137b636da03cbb41aa740dbe0c75f"
readonly license_sha256="3533cec129bb4bba015c0d61d86dd7c3b7e82110e4d2ff7837a01eff5bad5ccc"
readonly parser_sha256="9df63e0b6680f0b6cf1f1df613aaff2a7a4a3d9c9eb573b28b5d5c33fdaf7494"
readonly raw_go_sha256="2ac2fa39c03d62e84b4e602366506573c54889ed39054b5476ba7b2ba6ff8e4e"
readonly final_go_sha256="4745d5b74c074b322e6bf49330b321ffa9c41b93e851a4cde5a3f1a04d9c1534"
readonly table_sha256="1c17f16cf9d32b9ac851816c940736d04c0ce16b1242883bd4ed758e8245e50b"
readonly tree_sitter_version="0.23.0"
readonly generator_version="v0.1.0"
readonly -a corpus_files=(
  annotations.txt
  classes.txt
  comments.txt
  emojis.txt
  expressions.txt
  functions.txt
  literals.txt
  macros.txt
  statements.txt
  types.txt
)
readonly -a corpus_hashes=(
  dd7a6cf376847fe826f37299751a0845192b8c624aa21c1724ff150de0d32b03
  981c302c6d218d6c3b57087bcd5c5dafe08a6006d2f989a8198d64064c7fe658
  7c2083b6d6973dc44e12928a46f0b9356fc16f71da197c40895a0cb6303da80b
  257fba8196000b6d395e4bacd559d6e609c1cd726230c65dcbe0e853725d5e3f
  d408e6a48bf57a8f171860051db310cd30eea811225258f34a3274e7b862cf1d
  32ff2908246ff5598e0a73cb64e8e6f3384b2889636d689d2d69e36f768be0d8
  fb6dfedfd30a487698af0759af15c7b08f0ca2b4ae47efe5228c6180fbe5df2a
  625248a1fc204b36e52dd882404f9b8f20aff68ae6c923f94ba679453ae4fb42
  073bbe4d3c2edbd57119938eb04d5885013a9032d27825aee5f19fb160ab0553
  3f8040771bf413c420730688136c863f99514c97cc0372f6b8ff18a6d7006a25
)

if [[ $# -ne 1 ]]; then
  echo "usage: $0 /path/to/tree-sitter-swift" >&2
  exit 2
fi

readonly source_root="$(cd "$1" && pwd -P)"
readonly script_root="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd -P)"
readonly grammar_source="$source_root/src/grammar.json"
readonly scanner_source="$source_root/src/scanner.c"
readonly corpus_source="$source_root/test/corpus"
readonly output="$script_root/language_generated.go"
readonly table_output="$script_root/language_tables.bin"
readonly corpus_output="$script_root/testdata/tree-sitter-swift-corpus"

if [[ "$(git -C "$source_root" rev-parse HEAD)" != "$upstream_commit" ]]; then
  echo "tree-sitter-swift must be checked out at $upstream_commit" >&2
  exit 1
fi
if ! git -C "$source_root" diff --quiet -- \
  src/grammar.json src/scanner.c grammar.js package-lock.json LICENSE test/corpus; then
  echo "tree-sitter-swift pinned inputs have local changes" >&2
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

check_hash() {
  local path="$1"
  local expected="$2"
  local label="$3"
  if [[ "$(hash_file "$path")" != "$expected" ]]; then
    echo "unexpected $label checksum" >&2
    return 1
  fi
}

check_hash "$grammar_source" "$grammar_sha256" "grammar.json"
check_hash "$scanner_source" "$scanner_sha256" "scanner.c"
check_hash "$source_root/grammar.js" "$grammar_js_sha256" "grammar.js"
check_hash "$source_root/package-lock.json" "$package_lock_sha256" "package-lock.json"
check_hash "$source_root/LICENSE" "$license_sha256" "LICENSE"
for index in "${!corpus_files[@]}"; do
  check_hash \
    "$corpus_source/${corpus_files[index]}" \
    "${corpus_hashes[index]}" \
    "corpus/${corpus_files[index]}"
done

work_root="$(mktemp -d "${TMPDIR:-/tmp}/swift-language.XXXXXX")"
trap 'rm -rf "$work_root"' EXIT
mkdir -p "$work_root/grammar/src"
cp "$grammar_source" "$work_root/grammar/src/grammar.json"

(
  cd "$work_root/grammar"
  npm exec --yes --package="tree-sitter-cli@$tree_sitter_version" -- \
    tree-sitter generate --abi 14 --no-bindings src/grammar.json
)
readonly parser_source="$work_root/grammar/src/parser.c"
check_hash "$parser_source" "$parser_sha256" "generated parser.c"

readonly generated="$work_root/language.raw.go"
readonly rewritten="$work_root/language.rewritten"
readonly split_output="$work_root/language.split"
readonly tables="$work_root/language_tables.bin"

go run "github.com/dcosson/treesitter-go/cmd/tsgo-generate@$generator_version" \
  -parser "$parser_source" \
  -package swiftgrammar \
  -output "$generated"
check_hash "$generated" "$raw_go_sha256" "raw generated Go"

# Generated grammars normally live inside treesitter-go and import its
# internal core package. The public facade re-exports every generated type and
# constant, so point the generated grammar at that facade for this module.
sed \
  -e 's#core "github.com/dcosson/treesitter-go/internal/core"#core "github.com/dcosson/treesitter-go"#' \
  -e '/language "github.com\/dcosson\/treesitter-go\/language"/d' \
  -e 's/language\.Language/core.Language/g' \
  "$generated" > "$rewritten"

go run "$script_root/compact.go" "$rewritten" "$tables"
go run "$script_root/split.go" "$rewritten" "$split_output"
gofmt -w "$split_output"
check_hash "$split_output" "$final_go_sha256" "final generated Go"
check_hash "$tables" "$table_sha256" "generated table asset"
chmod 0644 "$split_output" "$tables"
mv "$split_output" "$output"
mv "$tables" "$table_output"
mkdir -p "$corpus_output"
for file in "${corpus_files[@]}"; do
  cp "$corpus_source/$file" "$corpus_output/$file"
  chmod 0644 "$corpus_output/$file"
done
