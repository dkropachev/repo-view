# Migration from the legacy experiment suite

The repository now has two deliberately separate command surfaces:

| Command surface | Purpose | Conformance status |
| --- | --- | --- |
| [`experiments/lsp-replacement/suite.sh`](../../../experiments/lsp-replacement/suite.sh) | Inspect, regenerate, and audit historical experiment fixtures | Legacy and non-conformant |
| [`tokenbench`](../README.md#commands) | Create, verify, and replay signed tokenbench evidence | Conformant only after every live check succeeds |

The legacy commands do not import evidence into tokenbench, create a tokenbench
plan, or publish a signed capture. A successful legacy `replay`, `resolve`,
`live`, or `repair` result remains historical evidence under its recorded
provenance classification.

## Why `reply`, `replay`, and `resolve` failed

`reply` was never a command; it was a common misspelling of `replay`. The Go
suite driver now accepts `reply` as a compatibility alias, emits a warning, and
records the normalized command as `replay`. New automation should spell the
command `replay`.

The more serious risk was in evidence preparation. The old `replay` and
`resolve` flow validated a tracked quality aggregate and then ran `analyze.sh`
and `quality-check.sh` inside that same canonical evidence directory. If either
script or a later validation failed, already-written derived files could remain
in place. A subsequent `resolve` could then read regenerated inputs rather than
the tracked bytes it was meant to audit; that kind of in-place mutation can
appear as zero, missing, or inconsistent metrics.

Preparation now works on an isolated copy:

1. snapshot the bounded, symlink-free canonical evidence tree;
2. verify the tracked quality aggregate with bounded regular-file reads before
   running either script;
3. copy each descendant regular file byte-for-byte and preserve descendant
   permission bits in a private staging directory below the new suite-output
   directory;
4. run analysis and quality regeneration only in staging;
5. reconcile completed analysis and quality output against per-tree and
   suite-wide staging budgets;
6. validate the regenerated aggregate and replay configuration in staging;
7. validate the case from staged results, remove staging, and re-snapshot the
   canonical source before, during, and after cleanup and immediately before
   publishing the report.

A preparation or cleanup error fails the suite. Failed staging removal is
attempted immediately; a cleanup failure poisons the shared budget and prevents
later staging. The budget rejects oversized inputs before launching a script
and rejects completed analysis or quality output before the next stage. It is
not a filesystem quota while a repository script is running, so use a bounded
filesystem or host quota if those scripts are not trusted. Staged results are
never copied back into canonical evidence. With
`--skip-analyze` and `--skip-quality`, no staging copy is needed and the existing
canonical derived files are read and validated in place without regeneration.

These checks close mutations made by the suite and detect concurrent changes at
each publication checkpoint. They cannot make a writable legacy directory
cryptographically immutable between the last check and the final write. Run
legacy audits with exclusive access or on a read-only snapshot when another
process could modify canonical evidence.

`resolve` is not a more permissive replay. It compares both the retained
fixture and the current resolution evidence, executes the resolution's pinned
Go regression selectors, checks provenance and assertions, and applies any
promotion gate. It should fail when any of those inputs is missing, changed, or
still unresolved.

## Safe legacy use

Run from the repository root and always choose a new output directory:

```sh
./experiments/lsp-replacement/suite.sh replay \
  --output /absolute/new-legacy-replay-report

./experiments/lsp-replacement/suite.sh resolve \
  --output /absolute/new-legacy-resolution-report
```

Use `--skip-analyze` or `--skip-quality` only to require the corresponding
tracked derived files to be reused. `--allow-missing` marks absent local
evidence as skipped; it does not make that evidence valid. The default command
when `suite.sh` receives no arguments is `replay`.

Do not point `--output` at an existing result directory, an evidence directory,
or a symlinked path. Preserve a failed output directory and diagnostics until
the failure is understood. Never copy staged derived files into tracked legacy
evidence to make a check pass.

## Why legacy results are non-conformant

The old experiment evolved while it was being used. Its retained pairs can
contain candidate-only navigator text, exact answer facts, commands and paths,
different wrappers or tool policies, environment and `PATH` differences,
unproven model routing, asymmetric evaluation, selected retries, and one-run
estimates. Its candidate used a shell-visible repo-view executable rather than
tokenbench's one live-verified read-only MCP registration.

Those are treatment differences, not metadata omissions. Mapping old files to
a newer JSON shape cannot remove the confounding. In particular, neither a
legacy suite summary nor a decoded tokenbench plan proves:

- identical baseline and candidate inputs except for the MCP registration;
- exact requested and provider-resolved model identity;
- immutable source, standalone `.git`, trusted executable, and native-tool
  state;
- effective Codex configuration and MCP handshake/tool-surface parity;
- containment, complete process-tree removal, and signed append-only capture.

See [DESIGN.md](../DESIGN.md) for the live authority chain and
[evidence-format.md](evidence-format.md) for what a signed capture proves.

## Historical classification

Every derived legacy report should retain one explicit classification:

- **legacy-unverified:** required raw artifacts or effective configuration are
  unavailable;
- **legacy-nonconformant:** evidence shows an arm delta outside the intended
  treatment;
- **legacy-partially-replayable:** raw events can be decoded, but strict parity
  or another required contract cannot be proved;
- **conformant:** reserved for evidence originally captured by the strict
  tokenbench live path and successfully authenticated.

Missing evidence means unknown, not equal. Candidate-only oracle facts,
conclusions, commands, paths, navigation policies, wrappers, `PATH` entries, or
environment changes must remain visible in the classification. An importer may
not normalize them away.

## Migration procedure

1. **Inventory and freeze.** Hash original suite summaries, case metadata,
   prompts, raw events, usage counters, judge outputs, scripts, and source
   revisions before interpretation. Do not edit evidence directories.
2. **Map with provenance.** Record every original path and digest, producing
   commit when known, importer identity, mapping decision, omission, and unknown
   field. Keep historical “effective token” calculations namespaced as legacy.
3. **Audit parity and replayability.** Record all prompt, process, model,
   source, Git, tool, and evaluator mismatches. Separately determine whether a
   pinned decoder can reconstruct raw observations.
4. **Publish shadow reports.** Show the historical calculation beside the new
   decoder output. Keep failures and accounting differences; do not mix legacy
   and conformant observations in one headline estimate.
5. **Perform a strict rerun.** Reauthor the task from user-facing intent only,
   preregister the study, author suite v2, and run every repetition through the
   publishable tokenbench path described in [operations.md](operations.md).

The strict rerun starts a new signed evidence lineage. It may cite the legacy
task as motivation, but it must not replace, select over, or statistically pool
the historical pair.

## Suggested field mapping

| Legacy concept | Destination | Rule |
| --- | --- | --- |
| Case/task name | Corpus task identity | Preserve the original and assign a versioned digest |
| Baseline/candidate prompt | Separate legacy prompt objects | Compare exact bytes; never merge away differences |
| Raw JSONL/events | CAS raw-stream object in a typed importer | Preserve original bytes and ordering |
| Usage summary | Namespaced legacy derived object | Link to raw input and record the formula |
| Quality/judge output | Legacy quality object | Preserve rubric, judge identity, isolation, and failure state |
| Shell script/config | Provenance object | Hash exact bytes and producing revision |
| Serialized plan | Audit artifact | Validate internally; never treat it as execution authority |
| Suite summary/report | Legacy report object | Do not treat a derived summary as raw authority |
| Retry/version suffix | Attempt lineage | Preserve every attempt rather than selecting a winner |

Migration is complete only when original bytes remain retained, mappings and
omissions are machine-readable, replayable calculations can be reproduced, the
classification is visible in every report, and any strict rerun is independent
of candidate-only instructions. Historical artifacts may be archived from the
primary workflow, but not deleted or silently rewritten.
