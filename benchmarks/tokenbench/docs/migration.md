# Migration from legacy experiments

> Status: proposed migration plan. No importer, compatibility command, or conformant replacement dataset is implied by this document.

## Scope

The repository's `experiments/lsp-replacement` artifacts are valuable historical evidence. They should remain inspectable, but they were produced under evolving scripts and may include prompt, wrapper, environment, or navigation-policy differences that violate tokenbench's sole-delta rule.

Migration preserves those facts. It does not rewrite history or promote a legacy result into a conformant tokenbench result. Conformance requires baseline to have no MCP registrations and candidate exactly one `repo_view` registration, with every other semantic input/configuration identical. It also requires exact requested/resolved model revision, adapter wrapper/child identities, tokenbench executable identity, full source/tree/standalone-Git identity, build-once process parity, and future live read-only MCP proof.

## Classification

Imported evidence should receive one explicit classification:

- **legacy-unverified:** required raw artifacts or effective configuration are unavailable;
- **legacy-nonconformant:** evidence proves an arm delta beyond the MCP registration;
- **legacy-partially-replayable:** raw events can be decoded, but strict parity or another required contract cannot be proven;
- **conformant:** reserved for a run originally captured under the strict tokenbench contract and successfully validated.

An importer must not assign `conformant` merely because old files can be mapped to a new schema or decoded as `ResolvedPlan`. A plan is audit-only and cannot prove fresh preparation, verified process construction, execution, or capture. Missing evidence is unknown, not equality.

Candidate-only oracle facts, conclusions, commands, file locations, navigator appendices, tool policies, wrappers, `PATH` entries, or environment changes make an old pair non-conformant. Normalizing those fields away during import is prohibited.

## Migration stages

### 1. Inventory and freeze

Inventory legacy suite summaries, case metadata, prompts, raw event streams, usage counters, quality outputs, scripts, and source revisions. Hash the original bytes before interpretation. Do not edit existing evidence directories.

### 2. Map with provenance

Create a new legacy-import bundle that references copied or externally addressed original objects. Record original relative paths, file digests, producing commit when known, import tool/version, mapping decisions, and omissions.

Map fields mechanically where semantics are documented. Preserve unknown counters and raw JSON rather than guessing. Keep any historical “effective token” metric under a namespaced legacy field; do not treat it as raw token usage.

### 3. Audit parity and replayability

Run strict invocation and rendered-process parity checks against captured requested/effective configuration if available. Audit requested versus resolved immutable model identity, adapter executable/control/child-config commitments, tokenbench executable identity, tree/`.git`/Git-executable identity, and exact MCP argv suffix. Record every mismatch. Separately determine whether raw events support a pinned decoder and whether quality checks replay offline.

Parity failure does not prevent historical replay; it prevents comparative promotion.

### 4. Shadow reports

Produce a derived report that shows the legacy calculation beside the new decoder's output, with accounting differences and missing fields. The report title and manifest must retain the legacy classification. Do not combine legacy and conformant observations in one headline estimate.

### 5. Strict rerun

Create new corpus tasks from the user-facing intent only. Remove arm-specific oracle material from both arms rather than transferring it to candidate or hiding it in configuration. Pin full source and standalone Git state, requested model plus exact expected revision, tokenbench/harness/adapter identities, and canonical prompt, then execute a new paired study whose only delta is the live-verified read-only `repo_view` registration.

The strict rerun is a new evidence lineage, not a continuation that replaces old files. It may cite the legacy task as motivation while remaining statistically separate.

### 6. Compatibility transition

After the staged runner exists, an optional compatibility entry point may translate a limited old workflow into a new run specification. It must print the resulting classification and refuse configurations with additional arm deltas. Until implemented and tested, no compatibility command should be documented as available.

## Suggested field mapping

| Legacy concept | Tokenbench destination | Rule |
| --- | --- | --- |
| Case/task name | Corpus task identity | Preserve original and assign a versioned digest |
| Baseline/candidate prompt | Separate prompt objects | Compare exact bytes; never merge away differences |
| Raw JSONL/events | CAS raw stream object | Preserve original bytes and ordering |
| Usage summary | Namespaced legacy derived object | Link to raw source; record formula |
| Quality/judge output | Legacy quality object | Preserve rubric, judge identity, and failure state if known |
| Shell script/config | Provenance object | Hash exact bytes and source revision |
| Serialized plan | Current audit artifact | Validate internally; never treat as execution authority or capture proof |
| Suite summary/report | Legacy report object | Do not treat as raw authority |
| Retry/version suffix | Attempt lineage | Preserve every attempt rather than selecting a winner |

## Reporting rules

Legacy reports must state:

- the classification and why strict parity is or is not provable;
- all known prompt/configuration deltas;
- requested/resolved model, adapter configuration, tokenizer/accounting, and pricing uncertainty;
- whether source tree, standalone `.git`, Git executable, and tokenbench executable identities are provable;
- whether common-process build-once parity and live MCP read-only surface are provable;
- repository/task dependence and retry selection;
- which numbers were reconstructed from raw events;
- why the result is not pooled with conformant tokenbench evidence.

Do not preserve a favorable historical headline when the selected underlying evidence changed. Derived reports must resolve their case set from immutable manifest links.

## Completion criteria

Migration is complete when original bytes are hashed and retained, mappings and omissions are machine-readable, legacy calculations replay where possible, classifications are visible in every report, and a strict rerun can proceed without relying on a candidate-only instruction. Historical artifacts may then be archived from the primary workflow, but not deleted or silently rewritten.
