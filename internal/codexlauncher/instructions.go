package codexlauncher

import "strings"

const standardAnswerGuard = `Before answering a change task, check completeness against the changed response and any follow-up evidence. For an explanation, name every changed artifact, distinguish direct changes from downstream impact, and retain every relevant quantitative detail. For a review, label each finding and residual risk with severity, include exact file:line references, and state an explicit correctness-regression conclusion. State unresolved evidence gaps or truncation explicitly. Do not omit evidence merely to be concise.`

const terminalNavigationInstructions = `For a branch, commit, or PR task, changed is the only navigation command: when patch_truncated is false, answer directly from that response without git metadata commands, find, inspect, outline, git diff, rg, sed, cat, or nl.`

const adaptiveNavigationInstructions = `For a branch, commit, or PR task, start with changed, then treat it as the change map rather than complete repository evidence. When the task asks about unchanged contracts, callers, implementations, tests, or downstream behavior, continue with targeted scopesifter calls. Use find --include defs for contracts before searching references. For broad reference searches, request locations first, narrow with --path, then inspect only representative returned locations; use outline only when the structure of a known file is itself evidence. Every bounded response explicitly reports results_truncated: when true, narrow the query if omitted results matter, and when false, do not repeat the same search. Batch symbols or inspect locations only when they answer the same evidence question. The navigation bounds are wrapper requirements, not suggestions: every scopesifter command must use --limit {{changed_limit}} or less, --max-code-lines {{changed_max_code_lines}} or less, --context {{navigation_context_cap}} or less, and --max-patch-lines {{changed_max_patch_lines}} or less. A command exceeding a bound invalidates the run. Stop each caller category after the requested representative path is fully evidenced, and stop the investigation when every requested category is supported or explicitly unresolved. Do not follow adjacent constructors or make redundant calls merely to increase the count. Use a shell fallback only for evidence outside the repository or evidence scopesifter cannot represent.`

const baseDeveloperInstructions = `Use scopesifter as the primary code-navigation tool. Pick exactly one initial command from this table:
- Branch, commit, or PR task: scopesifter changed --root . --base <BASE> --return {{changed_return}} --context {{changed_context}} --limit {{changed_limit}} --max-code-lines {{changed_max_code_lines}} --max-patch-lines {{changed_max_patch_lines}} --json
- Known symbols: scopesifter find <SYMBOL>... --root . --include both --return scope --context {{navigation_context_cap}} --limit {{changed_limit}} --max-code-lines {{changed_max_code_lines}} --max-patch-lines {{changed_max_patch_lines}} --json
- Known source location(s): scopesifter inspect <PATH:LINE>... --root . --include scope --return scope --context {{navigation_context_cap}} --limit {{changed_limit}} --max-code-lines {{changed_max_code_lines}} --max-patch-lines {{changed_max_patch_lines}} --json. Use --include all only when imports and repository-wide related symbol results are both required.
- Known source file(s): scopesifter outline <PATH>... --root . --return scope --context {{navigation_context_cap}} --limit {{changed_limit}} --max-code-lines {{changed_max_code_lines}} --max-patch-lines {{changed_max_patch_lines}} --json
For <BASE>, use the revision stated by the task; do not substitute another revision. {{change_navigation_instructions}} For non-change tasks, batch symbols that answer the same question and do not raise the listed limits. Do not call collaboration, subagent, spawn-agent, or agent-wait tools. Do not read or invoke Codex skills, plugins, hooks, or marketplace resources; they are outside this navigation task and invalidate the run.
{{answer_guard_instructions}}`

func developerInstructions(c config) string {
	changeInstructions := terminalNavigationInstructions
	if c.navigationPolicy == "adaptive" {
		changeInstructions = adaptiveNavigationInstructions
	}
	answerInstructions := ""
	if c.answerGuard == "on" {
		answerInstructions = standardAnswerGuard
	}
	return expandInstructions(baseDeveloperInstructions, c, map[string]string{
		"{{change_navigation_instructions}}": changeInstructions,
		"{{answer_guard_instructions}}":      answerInstructions,
	})
}

func expandInstructions(template string, c config, additional map[string]string) string {
	for name, value := range additional {
		template = strings.ReplaceAll(template, name, value)
	}
	replacements := map[string]string{
		"{{changed_return}}":          c.changedReturn,
		"{{changed_context}}":         c.changedContext,
		"{{changed_limit}}":           c.changedLimit,
		"{{changed_max_code_lines}}":  c.changedMaxCodeLines,
		"{{changed_max_patch_lines}}": c.changedMaxPatchLines,
		"{{navigation_context_cap}}":  c.navigationContextCap,
	}
	for name, value := range replacements {
		template = strings.ReplaceAll(template, name, value)
	}
	return template
}
