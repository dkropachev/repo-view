# Token Optimization

## Goal

Reduce repository-reading round trips without increasing token cost or lowering
answer quality.

`repo-view` targets the common sequence:

```text
search -> locate file and line -> read context -> follow symbol -> read caller
```

It returns bounded source with structured locations so a model can batch those
steps.

## Current CLI

Choose the command from the input already known:

| Known input | Command |
| --- | --- |
| Symbol names | `repo-view find SYMBOL...` |
| Exact locations | `repo-view inspect PATH:LINE...` |
| Source files | `repo-view outline PATH...` |
| Git base or worktree changes | `repo-view changed` |

Choose one source return:

```text
--return locations
--return line
--return context --context N
--return scope
```

Batch related requests:

```bash
repo-view find Reserve ReserveN CancelAt \
  --include both \
  --return scope \
  --limit 20 \
  --max-code-lines 60 \
  --json

repo-view inspect path/a.go:42 path/b.go:18 \
  --include all \
  --return scope \
  --limit 20 \
  --max-code-lines 60 \
  --json
```

Use `--path` and `--exclude` to bound monorepo searches. Use
`--changed-only --base REF` when only changed files matter. Use
`--return locations` when source is not needed.

All numeric bounds must be positive. `--max-code-lines` is omitted when
`--return locations` returns no source. Wrapper-managed runs can compile
per-command bounds and a cumulative repo-view-invocation cap into the binary.

## Validation

Run deterministic cross-repository validation:

```bash
go run ./cmd/repo-view-validate --cases 100
```

The validator builds an independent line index, chooses deterministic symbols,
and verifies point locations, enclosing ranges, and returned source. It
shallow-clones `testdata/validation-repos.tsv` into the ignored
`validation-repos/` directory. Existing matching clones are reused without a
fetch or checkout; conflicting directories fail validation.

The former model-navigation shell suite is archived and its driver has been
removed. Treat the figures below as historical; run new comparisons through
[`tokenbench`](benchmarks/tokenbench/README.md).

Current measured reductions:

| Workload | Explain | Review |
| --- | ---: | ---: |
| Short changed packet | 80.57% | 88.74% |
| Deep navigation | 44.95% | 45.15% |

See
[`experiments/lsp-replacement.md`](experiments/lsp-replacement.md) for the
complete measurement definitions and evidence links.

## Remaining Work

1. Add a persistent parsed index with explicit invalidation.
2. Add a trace operation that returns a definition, selected callers, and
   observable consumption in one bounded result.
3. Add language/extension and per-file result limits.
4. Add a statement-level call-site return.
5. Add cursors and structural budgets for large Git and log results.
6. Expand the pinned workload corpus before changing default profiles.

Each change must pass the same token, call-accounting, and quality gates before
promotion.
