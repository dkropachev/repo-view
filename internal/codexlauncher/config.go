package codexlauncher

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	defaultChangedReturn        = "context"
	defaultChangedContext       = "4"
	defaultChangedLimit         = "20"
	defaultChangedMaxCodeLines  = "60"
	defaultChangedMaxPatchLines = "300"
	defaultReasoningEffort      = "high"
	defaultAnswerGuard          = "on"
	defaultNavigationPolicy     = "terminal"
	defaultNavigationContextCap = "20"
	defaultNavigationCommandCap = "0"
)

var (
	decimalPattern  = regexp.MustCompile(`^[0-9]+$`)
	objectIDPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
)

type config struct {
	cacheDir                      string
	binDir                        string
	changedReturn                 string
	changedContext                string
	changedLimit                  string
	changedMaxCodeLines           string
	changedMaxPatchLines          string
	reasoningEffort               string
	answerGuard                   string
	navigationPolicy              string
	navigationContextCap          string
	navigationCommandCap          string
	requiredNavigationRoot        string
	requiredNavigationBaseCommit  string
	requiredChangedReturn         string
	requiredChangedContext        string
	requireNavigationSemantics    string
	navigationSemanticsConfigured bool
}

func loadConfig(root string, environment []string, arguments []string) (config, error) {
	env := environmentMap(environment)
	c := config{
		cacheDir:                     valueOrDefault(env, "SCOPESIFTER_CACHE_DIR", root+"/.cache/bin"),
		binDir:                       env["SCOPESIFTER_BIN_DIR"],
		changedReturn:                valueOrDefault(env, "SCOPESIFTER_CHANGED_RETURN", defaultChangedReturn),
		changedContext:               valueOrDefault(env, "SCOPESIFTER_CHANGED_CONTEXT", defaultChangedContext),
		changedLimit:                 valueOrDefault(env, "SCOPESIFTER_CHANGED_LIMIT", defaultChangedLimit),
		changedMaxCodeLines:          valueOrDefault(env, "SCOPESIFTER_CHANGED_MAX_CODE_LINES", defaultChangedMaxCodeLines),
		changedMaxPatchLines:         valueOrDefault(env, "SCOPESIFTER_CHANGED_MAX_PATCH_LINES", defaultChangedMaxPatchLines),
		reasoningEffort:              valueOrDefault(env, "SCOPESIFTER_REASONING_EFFORT", defaultReasoningEffort),
		answerGuard:                  valueOrDefault(env, "SCOPESIFTER_ANSWER_GUARD", defaultAnswerGuard),
		navigationPolicy:             valueOrDefault(env, "SCOPESIFTER_NAVIGATION_POLICY", defaultNavigationPolicy),
		navigationContextCap:         valueOrDefault(env, "SCOPESIFTER_NAVIGATION_CONTEXT_CAP", defaultNavigationContextCap),
		navigationCommandCap:         valueOrDefault(env, "SCOPESIFTER_NAVIGATION_COMMAND_CAP", defaultNavigationCommandCap),
		requiredNavigationRoot:       env["SCOPESIFTER_REQUIRED_ROOT"],
		requiredNavigationBaseCommit: env["SCOPESIFTER_REQUIRED_BASE_COMMIT"],
		requiredChangedReturn:        env["SCOPESIFTER_REQUIRED_CHANGED_RETURN"],
		requiredChangedContext:       env["SCOPESIFTER_REQUIRED_CHANGED_CONTEXT"],
		requireNavigationSemantics:   env["SCOPESIFTER_REQUIRE_NAVIGATION_SEMANTICS"],
	}

	if !oneOf(c.changedReturn, "locations", "line", "context", "scope") {
		return config{}, fmt.Errorf("invalid SCOPESIFTER_CHANGED_RETURN: %s", c.changedReturn)
	}
	numerics := []struct {
		name  string
		value string
	}{
		{"changed_context", c.changedContext},
		{"changed_limit", c.changedLimit},
		{"changed_max_code_lines", c.changedMaxCodeLines},
		{"changed_max_patch_lines", c.changedMaxPatchLines},
		{"navigation_context_cap", c.navigationContextCap},
		{"navigation_command_cap", c.navigationCommandCap},
	}
	for _, numeric := range numerics {
		if !decimalPattern.MatchString(numeric.value) {
			return config{}, fmt.Errorf("%s must be a non-negative integer: %s", numeric.name, numeric.value)
		}
	}
	positives := []struct {
		name  string
		value string
	}{
		{"changed_limit", c.changedLimit},
		{"changed_max_code_lines", c.changedMaxCodeLines},
		{"changed_max_patch_lines", c.changedMaxPatchLines},
	}
	for _, positive := range positives {
		if decimalIsZero(positive.value) {
			return config{}, fmt.Errorf("%s must be a positive integer: %s", positive.name, positive.value)
		}
	}
	if decimalGreaterThan(c.changedContext, c.navigationContextCap) {
		return config{}, fmt.Errorf(
			"changed_context %s exceeds navigation_context_cap %s",
			c.changedContext,
			c.navigationContextCap,
		)
	}
	if !oneOf(c.reasoningEffort, "", "inherit", "low", "medium", "high", "xhigh", "ultra") {
		return config{}, fmt.Errorf("invalid SCOPESIFTER_REASONING_EFFORT: %s", c.reasoningEffort)
	}
	if !oneOf(c.answerGuard, "on", "off") {
		return config{}, fmt.Errorf("invalid SCOPESIFTER_ANSWER_GUARD: %s", c.answerGuard)
	}
	if !oneOf(c.navigationPolicy, "terminal", "adaptive") {
		return config{}, fmt.Errorf("invalid SCOPESIFTER_NAVIGATION_POLICY: %s", c.navigationPolicy)
	}

	c.navigationSemanticsConfigured = c.requiredNavigationRoot != "" ||
		c.requiredNavigationBaseCommit != "" ||
		c.requiredChangedReturn != "" ||
		c.requiredChangedContext != "" ||
		c.requireNavigationSemantics != ""
	if c.navigationSemanticsConfigured {
		if c.requireNavigationSemantics != "1" ||
			c.requiredNavigationRoot == "" ||
			c.requiredNavigationBaseCommit == "" ||
			c.requiredChangedReturn == "" ||
			c.requiredChangedContext == "" {
			return config{}, fmt.Errorf("mechanical navigation semantics configuration is incomplete")
		}
		if decimalIsZero(c.navigationCommandCap) {
			return config{}, fmt.Errorf("mechanical navigation semantics require a positive command cap")
		}
		if !decimalPattern.MatchString(c.requiredChangedContext) {
			return config{}, fmt.Errorf(
				"SCOPESIFTER_REQUIRED_CHANGED_CONTEXT must be a non-negative integer: %s",
				c.requiredChangedContext,
			)
		}
		if !objectIDPattern.MatchString(c.requiredNavigationBaseCommit) {
			return config{}, fmt.Errorf(
				"SCOPESIFTER_REQUIRED_BASE_COMMIT must be a full lowercase object ID: %s",
				c.requiredNavigationBaseCommit,
			)
		}
		if !oneOf(c.requiredChangedReturn, "locations", "line", "context", "scope") {
			return config{}, fmt.Errorf(
				"invalid SCOPESIFTER_REQUIRED_CHANGED_RETURN: %s",
				c.requiredChangedReturn,
			)
		}
		if c.requiredChangedReturn != c.changedReturn {
			return config{}, fmt.Errorf(
				"SCOPESIFTER_REQUIRED_CHANGED_RETURN must match SCOPESIFTER_CHANGED_RETURN",
			)
		}
		if !decimalEqual(c.requiredChangedContext, c.changedContext) {
			return config{}, fmt.Errorf(
				"SCOPESIFTER_REQUIRED_CHANGED_CONTEXT must match SCOPESIFTER_CHANGED_CONTEXT",
			)
		}
		if decimalIsZero(c.requiredChangedContext) && c.requiredChangedReturn != "locations" {
			return config{}, fmt.Errorf(
				"mechanically enforced changed context must be positive unless return is locations",
			)
		}
	}

	if !decimalIsZero(c.navigationCommandCap) && !contains(arguments, "--json") {
		return config{}, fmt.Errorf("capped scopesifter navigation requires codex --json events")
	}
	return c, nil
}

func valueOrDefault(environment map[string]string, name string, fallback string) string {
	if value := environment[name]; value != "" {
		return value
	}
	return fallback
}

func environmentMap(environment []string) map[string]string {
	result := make(map[string]string, len(environment))
	for _, entry := range environment {
		name, value, ok := strings.Cut(entry, "=")
		if ok {
			result[name] = value
		}
	}
	return result
}

func normalizedDecimal(value string) string {
	normalized := strings.TrimLeft(value, "0")
	if normalized == "" {
		return "0"
	}
	return normalized
}

func decimalGreaterThan(left string, right string) bool {
	left = normalizedDecimal(left)
	right = normalizedDecimal(right)
	if len(left) != len(right) {
		return len(left) > len(right)
	}
	return left > right
}

func decimalEqual(left string, right string) bool {
	return !decimalGreaterThan(left, right) && !decimalGreaterThan(right, left)
}

func decimalIsZero(value string) bool {
	return normalizedDecimal(value) == "0"
}

func oneOf(value string, allowed ...string) bool {
	return contains(allowed, value)
}

func contains(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
