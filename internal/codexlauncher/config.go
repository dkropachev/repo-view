package codexlauncher

import (
	"fmt"
	"regexp"
	"strings"
)

const (
	defaultLimitCap             = "20"
	defaultContextCap           = "20"
	defaultMaxCodeLinesCap      = "60"
	defaultMaxPatchLinesCap     = "300"
	defaultReasoningEffort      = "high"
	defaultAnswerGuard          = "on"
	defaultNavigationCommandCap = "0"
)

var decimalPattern = regexp.MustCompile(`^[0-9]+$`)

type config struct {
	cacheDir             string
	binDir               string
	limitCap             string
	contextCap           string
	maxCodeLinesCap      string
	maxPatchLinesCap     string
	reasoningEffort      string
	answerGuard          string
	navigationCommandCap string
}

func loadConfig(root string, environment []string, arguments []string) (config, error) {
	env := environmentMap(environment)
	c := config{
		cacheDir:             valueOrDefault(env, "SCOPESIFTER_CACHE_DIR", root+"/.cache/bin"),
		binDir:               env["SCOPESIFTER_BIN_DIR"],
		limitCap:             valueOrDefault(env, "SCOPESIFTER_LIMIT_CAP", defaultLimitCap),
		contextCap:           valueOrDefault(env, "SCOPESIFTER_CONTEXT_CAP", defaultContextCap),
		maxCodeLinesCap:      valueOrDefault(env, "SCOPESIFTER_MAX_CODE_LINES_CAP", defaultMaxCodeLinesCap),
		maxPatchLinesCap:     valueOrDefault(env, "SCOPESIFTER_MAX_PATCH_LINES_CAP", defaultMaxPatchLinesCap),
		reasoningEffort:      valueOrDefault(env, "SCOPESIFTER_REASONING_EFFORT", defaultReasoningEffort),
		answerGuard:          valueOrDefault(env, "SCOPESIFTER_ANSWER_GUARD", defaultAnswerGuard),
		navigationCommandCap: valueOrDefault(env, "SCOPESIFTER_NAVIGATION_COMMAND_CAP", defaultNavigationCommandCap),
	}

	numerics := []struct {
		name  string
		value string
	}{
		{"SCOPESIFTER_LIMIT_CAP", c.limitCap},
		{"SCOPESIFTER_CONTEXT_CAP", c.contextCap},
		{"SCOPESIFTER_MAX_CODE_LINES_CAP", c.maxCodeLinesCap},
		{"SCOPESIFTER_MAX_PATCH_LINES_CAP", c.maxPatchLinesCap},
		{"SCOPESIFTER_NAVIGATION_COMMAND_CAP", c.navigationCommandCap},
	}
	for _, numeric := range numerics {
		if !decimalPattern.MatchString(numeric.value) {
			return config{}, fmt.Errorf("%s must be a non-negative integer: %s", numeric.name, numeric.value)
		}
	}
	for _, positive := range []struct {
		name  string
		value string
	}{
		{"SCOPESIFTER_LIMIT_CAP", c.limitCap},
		{"SCOPESIFTER_MAX_CODE_LINES_CAP", c.maxCodeLinesCap},
		{"SCOPESIFTER_MAX_PATCH_LINES_CAP", c.maxPatchLinesCap},
	} {
		if decimalIsZero(positive.value) {
			return config{}, fmt.Errorf("%s must be a positive integer: %s", positive.name, positive.value)
		}
	}
	if !oneOf(c.reasoningEffort, "", "inherit", "low", "medium", "high", "xhigh", "ultra") {
		return config{}, fmt.Errorf("invalid SCOPESIFTER_REASONING_EFFORT: %s", c.reasoningEffort)
	}
	if !oneOf(c.answerGuard, "on", "off") {
		return config{}, fmt.Errorf("invalid SCOPESIFTER_ANSWER_GUARD: %s", c.answerGuard)
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

func decimalIsZero(value string) bool {
	return strings.Trim(value, "0") == ""
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
