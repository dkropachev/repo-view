package codexlauncher

const navigationInstructions = `ScopeSifter CLI is available: find an exact symbol/path with scopesifter find, read PATH:LINE with scopesifter inspect, or map changes with scopesifter changed. Use it only when it replaces shell navigation, not as a required first action.`

const answerGuardInstructions = `Before answering a change task, check completeness against the changed response and follow-up evidence. Explanations must name every changed artifact, distinguish direct changes from downstream impact, and preserve all relevant quantitative detail. Reviews must give each finding and residual risk a severity, exact file:line reference, and explicit correctness-regression conclusion. State unresolved evidence gaps or truncation; never omit evidence for concision.`

func developerInstructions(c config) string {
	if c.answerGuard == "on" {
		return navigationInstructions + "\n" + answerGuardInstructions
	}
	return navigationInstructions
}
