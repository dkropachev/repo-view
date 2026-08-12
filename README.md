# ScopeSifter

[![CI](https://github.com/yapless/scopesifter/actions/workflows/ci.yml/badge.svg)](https://github.com/yapless/scopesifter/actions/workflows/ci.yml)

`ScopeSifter` is a Go code navigation library and CLI. It finds where a function,
class, method, type, or other symbol is used and can return:

- `file:line`
- `file:start-end` for the enclosing function/class/block
- fenced code snippets

It can also drop comments and Python docstrings from returned snippets.

## Installation

Install the latest source with Go:

```console
go install github.com/yapless/scopesifter/cmd/scopesifter@latest
```

Version tags publish prebuilt Linux, macOS, and Windows binaries, raw static
Linux `taskctl` and `taskctl-launcher` executables for amd64 and arm64, plus a
`SHA256SUMS` file, on the
[GitHub Releases page](https://github.com/yapless/scopesifter/releases).
Before publication, the release workflow creates signed GitHub build-provenance
attestations for every artifact named by the checksum manifest and a separate
attestation for `SHA256SUMS` itself.

### Trusted taskctl launcher

`taskctl` generates and validates authenticated benchmark-task artifacts. Its
launcher is intentionally outside the writable repository at
`/usr/local/libexec/scopesifter/taskctl-launcher`; there is no repository-local
fallback and no compatibility path.

Provisioning is an administrator trust decision, not an automatic build step:

1. Download `SHA256SUMS` and the raw
   `scopesifter-taskctl-launcher_VERSION_linux_ARCH` artifact from one exact
   GitHub release tag.
2. Verify the artifact's signed provenance with `gh attestation verify`, bound
   to `yapless/scopesifter`, the exact release workflow, tag ref, and source
   commit; then verify its line in `SHA256SUMS` and record that lowercase
   SHA-256 independently.
3. Mark the already-verified raw artifact executable and authorize its narrow
   root-only installer with
   `sudo ./scopesifter-taskctl-launcher_VERSION_linux_ARCH install trusted-launcher SHA256`.
   This is installation convenience after the trust decision, not a verifier
   or trust bootstrap. It copies the executing bytes through pinned
   descriptors and creates the fixed pathname with owner and group
   `root:root`, mode exactly `0555`, one hard link, and no capabilities or
   set-ID bits.
4. Run the installed launcher directly in the trusted host mount namespace.
   It rejects non-initial user-ID mappings, a privilege transition, or any
   mismatch in pathname, inode, ownership, mode, link count, static ELF shape,
   or capabilities. Its in-process checks cannot authenticate the mount
   namespace itself.

The launcher opens `bin/taskctl`, copies the authenticated bytes to a sealed
anonymous executable, and executes that descriptor with a closed environment.
For every mutating role, provide the independently reviewed lowercase digest as
`TASKCTL_EXECUTABLE_SHA256`. `make taskctl-inspect-executable-sha256` prints the
digest through the installed launcher; it does not admit the digest on the
operator's behalf.

## Navigation CLI

```console
scopesifter find renderUser parseSession --root ./my-repo --include both --return scope --json
scopesifter inspect src/app.go:42 src/session.go:18 --root ./my-repo --include scope --return scope --json
scopesifter outline src/app.go src/session.go --root ./my-repo --return scope --json
scopesifter changed --root ./my-repo --base main --return context --json
```

Each command accepts one kind of input. Do not combine selectors:

| Input already known | Command | Returned evidence | Default `--return` |
| --- | --- | --- | --- |
| One or more symbol names | `find SYMBOL...` | Definitions and references | `scope` |
| One or more exact source locations | `inspect PATH:LINE...` | Enclosing scope; optional imports and related symbol results | `scope` |
| One or more source file paths | `outline PATH...` | Definitions in file order | `line` |
| Git base revision | `changed --base REF` | Commit metadata, exact patch, and changed-source context | `context` |

For `inspect --json` and `outline --json` batches, an invalid location or path
produces an `error` on that item's response while valid items are still
returned. The command exits successfully when at least one item is valid and
fails when every item is invalid. A single invalid item remains a hard error.

`--return` controls the `code` field without changing result identity:

- `locations`: locations and metadata only.
- `line`: the matching or defining line.
- `context`: the hit plus `--context N` lines on each side.
- `scope`: the enclosing function, method, class, or block.

Shared options:

- `--root DIR`: repository root. Default: `.`
- `--return locations|line|context|scope`: embedded source amount.
- `--context N`: lines around context hits. Default: `5`.
- `--limit N`: maximum results. Default: `50`.
- `--path FILTER`: include path filter; repeatable.
- `--exclude FILTER`: exclude path filter; repeatable.
- `--changed-only`: limit `find` or `inspect` to files changed from `--base`.
- `--base REF`: Git base revision for `changed` and `--changed-only`.
- `--drop-comments`: remove comments from embedded code.
- `--drop-docstrings`: remove Python docstrings from embedded code.
- `--no-comments`: ignore comment-only symbol matches.
- `--no-strings`: ignore string-only symbol matches.
- `--include-comments`: include comment-only matches; excluded by default.
- `--include-strings`: include string-only matches; excluded by default.
- `--max-code-lines N`: positive cap for each embedded snippet. Default: `80`;
  omitted when `--return locations` returns no code.
- `--max-patch-lines N`: positive cap for `changed` patch output. Default: `400`.
- `--json`: compact JSON output.
- `--pretty`: pretty JSON output.

JSON always includes `code_truncated`; it is `true` when `--max-code-lines`
cuts a returned scope or context. Truncated snippets remain centered on the
requested or matching line and include `code_start_line` and `code_end_line`
for the exact embedded range. Increase the cap only when omitted scope is
required. Those range fields are also included when an imports result embeds
only a line or adds context while retaining the full import range as its
identity.

Command-specific options and defaults:

Path filters accept a plain path substring (`service/matching`), a basename
glob (`*_test.go`), or a recursive directory prefix (`service/matching/**`).
Multiple includes are ORed; any matching exclude wins.

- `find`: `--include defs|refs|both`; default `both`. Default return is
  `scope`. Its `--limit` is shared across all supplied symbols. Exact text such
  as a dependency module path can be searched in `go.mod`, for example
  `scopesifter find golang.org/x/time --path go.mod --include refs --return line`.
- `inspect`: `--include symbol|scope|imports|defs|refs|both|all`; default `scope`.
  Use `all` only when the enclosing scope, imports, and repository-wide related
  symbol results are all needed in one response.
  Default return is `scope`. It accepts one or more locations and shares
  `--limit` fairly across them. JSON is one object for one location and an
  array in argument order for multiple locations.
- `outline`: no selector options; default return is `line`. It accepts one or
  more paths and shares `--limit` fairly across them. JSON is one object for
  one path and an array in argument order for multiple paths.
- `changed`: default return is `context`; `--base REF` compares `REF...HEAD`.
  Its source context comes from that immutable HEAD snapshot, even when the
  worktree is dirty. Without `--base`, it reports staged, unstaged, and
  untracked files in worktree coordinates. JSON always includes
  `patch_truncated`, plus `changed_lines` and the containing `scope` or `scopes`
  for each merged range. `patch_truncated` is `true` when
  `--max-patch-lines` cuts the exact patch or its 16 MiB safety ceiling is
  reached. The byte ceiling also bounds temporary snapshots used to diff
  untracked files.

## Codex Integration

Build the typed launcher, then use it to inject `scopesifter` into PATH and add
navigation instructions for one Codex invocation. It does not modify Codex
config, Codex source, or the target repository. The root Makefile exposes the
target from `make/launcher.mk`.

```console
$ make scopesifter-codex
$ bin/scopesifter-codex exec \
  -C /path/to/repository \
  -s read-only \
  --json \
  "Explain this branch compared with main."
```

The launcher defaults to a bounded navigation profile:

```text
SCOPESIFTER_CHANGED_RETURN=context
SCOPESIFTER_CHANGED_CONTEXT=4
SCOPESIFTER_CHANGED_LIMIT=20
SCOPESIFTER_CHANGED_MAX_CODE_LINES=60
SCOPESIFTER_CHANGED_MAX_PATCH_LINES=300
SCOPESIFTER_REASONING_EFFORT=high
SCOPESIFTER_ANSWER_GUARD=on
```

Each value can be overridden for one invocation. `SCOPESIFTER_ANSWER_GUARD`
accepts `on|off`; reasoning accepts `inherit|low|medium|high|xhigh|ultra`.
The launcher compiles the advertised navigation ceilings into every child
`scopesifter` binary: result limit `20`, context `20`, embedded code `60`, and
changed patch `300`. Environment values are fallback-only, so a command cannot
disable these ceilings with `env -u`. A command that requests more exits with
an explicit cap error and is counted as a navigation-bound violation by the
transcript validator.
`SCOPESIFTER_NAVIGATION_CONTEXT_CAP` overrides the default context ceiling; the
other ceilings follow their corresponding `SCOPESIFTER_CHANGED_*` values.
When both `SCOPESIFTER_NAVIGATION_COMMAND_CAP` and
`SCOPESIFTER_NAVIGATION_BUDGET_FILE` are set in a writable integration, JSON
responses include `navigation_budget` with `used`, `limit`, and `remaining`,
and scopesifter invocations after the limit are rejected. For read-only capped
Codex runs, the launcher requires `--json`, mirrors live events to an ignored
local transcript, and compiles its path and cap into scopesifter. Each
scopesifter CLI invocation is counted from started and completed navigation
events, so the model cannot bypass the cumulative budget by unsetting
environment variables. Total Codex tool calls are measured separately. The
transcript is removed when the launcher exits.

Reproducible comparisons belong in
[`tokenbench`](benchmarks/tokenbench/README.md), whose evidence and parity
contracts fail closed. Evidence created under a different product identity is
not accepted by the current contracts; historical material remains available
from Git history.

## Validation

```console
go run ./cmd/scopesifter-validate --cases 100
```

The validator builds an independent line-location index, selects deterministic
symbols per repository, and checks point locations, enclosing ranges, and
returned source for each symbol. By default it shallow-clones the repositories
in `testdata/validation-repos.tsv` under the ignored `validation-repos/`
directory. A matching existing clone is reused without fetching or checking
out another revision. A conflicting directory or `origin` fails closed.

Use `--repo-list FILE` to validate an existing list of checkout paths, or
`--repo-root DIR` to scan an existing directory instead of managing clones.
`--repo-spec` and `--clone-root` override the managed-clone defaults.

Output examples:

```text
src/app.go:42
```

```markdown
# src/app.go:38-45
~~~go
func view() string {
	return renderUser(currentUser)
}
~~~
```

## Go Library

```go
view, err := navigator.New("./my-repo")
if err != nil {
	return err
}

response, err := view.Find("renderUser", navigator.Options{
	Include:        navigator.IncludeRefs,
	Return:         navigator.ReturnScope,
	Limit:          20,
	MaxCodeLines:   60,
	DropComments:   true,
	DropDocstrings: true,
})
if err != nil {
	return err
}

for _, result := range response.Results {
	fmt.Printf("%s:%d\n", result.Path, result.Line)
	fmt.Println(result.Code)
}
```

Go uses the standard library parser and scanner, with a conservative fallback
for incomplete source. Python, Rust, JavaScript, JSX, TypeScript, TSX, Java, C,
C++, C#, Kotlin, and Swift use pure-Go Tree-sitter concrete parsers with
language-specific lexical recovery. The JavaScript backend covers `.js`,
`.mjs`, `.cjs`, and `.jsx`;
TypeScript covers `.ts`, `.tsx`, `.mts`, and `.cts`; and Java covers `.java`.
C covers `.c` and `.h`. C++ has a dedicated backend for common source, header,
template-implementation, and module-interface extensions. C# covers `.cs` and
script `.csx` sources. Kotlin covers `.kt` and script `.kts` sources. Swift
covers `.swift` sources, including Swift package manifests. Modula-2 uses a
dedicated first-party concrete parser and bounded lexical recovery for `.mod`
program or implementation modules and `.def` definition modules. Unknown
extensions fall back to the shared brace scanner.

## Language backends

Language-dependent navigation is isolated behind a backend interface. Each
backend owns its language name, definition recognition, scope selection,
import extraction, comment syntax, and docstring behavior. The Go backend uses
`go/parser` and `go/scanner` for declarations, scopes, imports, and lexical
comment/string handling. Python, Rust, JavaScript, JSX, TypeScript, TSX, Java,
C, C++, C#, Kotlin, Swift, and Modula-2 have dedicated concrete parsers,
coordinate-preserving lexical masks, and bounded recovery for malformed or
newer syntax. Unknown brace-based languages use the common brace scanner and
generic definition matcher.

The C++ backend covers declarations, templates, namespaces, special member and
operator functions, concepts, structured bindings, preprocessing definitions,
and C++20 module/import forms. Its concrete parser and full-source lexical
recovery use independent byte, token, delimiter, and ambiguity budgets so
malformed or very large inputs remain bounded.

The C# backend targets current C# 14 syntax, including records, primary and
partial declarations, extension blocks and operators, raw and interpolated
strings, file-scoped namespaces, global/alias usings, and file-app dependency
directives. Its pure-Go external scanner and full-source lexical recovery have
independent byte, token, delimiter, preprocessor, and retention limits.

The Kotlin backend covers source and script declarations, package/import
directives, classes and objects, functions, properties, type aliases,
constructors, enum entries, nested comments, string templates, and
multi-dollar strings. Its generated concrete grammar, bounded pure-Go external
scanner, and full-source lexical recovery have independent byte, token,
delimiter, structural, and retention limits.

The Swift backend covers source and package-manifest declarations, imports,
nominal types and extensions, functions, properties, initializers, subscripts,
macros, operators, compiler-condition branches, nested comments, raw and
multiline strings, interpolation, and regex literals. Its generated concrete
grammar, bounded pure-Go external scanner, and full-source lexical recovery
have independent byte, token, delimiter, directive, structural, and retention
limits.

The Modula-2 backend targets GNU's PIM and ISO grammar for program,
implementation, definition, and local modules, including common GNU lexer and
procedure extensions. It covers imports, constants, types, variables,
procedures, records, enumerations, nested comments, pragmas, and structured
statement scopes. An uppercase module-header content gate keeps non-Modula
files such as `go.mod` searchable without inventing declarations. Its
first-party concrete parser and independent lexical recovery have separate
byte, token, nesting, declaration, and retention limits.

The TypeScript backend covers runtime and type declarations, TypeScript module
forms, JSX, and coordinate-preserving recovery. The Java backend targets Java
SE 26, including primitive-pattern preview syntax, and covers named types and
members, records, modules, `requires`, and module imports, Javadoc ownership,
JLS Unicode-escape preprocessing, and Unicode 17 identifier rules. It also
recognizes legacy preview string templates for navigation and masking. The
C backend covers functions and prototypes, object and type declarations,
aggregate members, enumerators, macros, include/embed directives, complex C
declarators, preprocessor scopes, and bounded recovery for C23 and common
compiler-extension syntax outside the pinned concrete grammar. The
searchable-extension list is derived from the backend registry, so adding a
language requires registering its extensions in one place. Shared scanning,
path safety, filtering, result bounding, and Git change handling remain in the
common navigation engine.
