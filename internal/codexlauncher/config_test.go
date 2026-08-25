package codexlauncher

import (
	"strings"
	"testing"
)

func TestLoadConfigDefaults(t *testing.T) {
	t.Parallel()
	c, err := loadConfig("/source", []string{"PATH=/usr/bin"}, []string{"exec"})
	if err != nil {
		t.Fatal(err)
	}
	if c.cacheDir != "/source/.cache/bin" ||
		c.limitCap != "20" ||
		c.contextCap != "20" ||
		c.maxCodeLinesCap != "60" ||
		c.maxPatchLinesCap != "300" ||
		c.reasoningEffort != "high" ||
		c.answerGuard != "on" ||
		c.navigationCommandCap != "0" {
		t.Fatalf("unexpected defaults: %#v", c)
	}
}

func TestLoadConfigRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		environment []string
		arguments   []string
		want        string
	}{
		{"negative context", []string{"SCOPESIFTER_CONTEXT_CAP=-1"}, nil, "must be a non-negative integer"},
		{"empty uses default", []string{"SCOPESIFTER_LIMIT_CAP="}, nil, ""},
		{"zero limit", []string{"SCOPESIFTER_LIMIT_CAP=000"}, nil, "must be a positive integer"},
		{"zero code lines", []string{"SCOPESIFTER_MAX_CODE_LINES_CAP=0"}, nil, "must be a positive integer"},
		{"zero patch lines", []string{"SCOPESIFTER_MAX_PATCH_LINES_CAP=0"}, nil, "must be a positive integer"},
		{"invalid reasoning", []string{"SCOPESIFTER_REASONING_EFFORT=max"}, nil, "invalid SCOPESIFTER_REASONING_EFFORT"},
		{"invalid guard", []string{"SCOPESIFTER_ANSWER_GUARD=yes"}, nil, "invalid SCOPESIFTER_ANSWER_GUARD"},
		{"capped without JSON", []string{"SCOPESIFTER_NAVIGATION_COMMAND_CAP=1"}, []string{"exec"}, "requires codex --json events"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := loadConfig("/source", test.environment, test.arguments)
			if test.want == "" {
				if err != nil {
					t.Fatal(err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestLoadConfigAcceptsOverrides(t *testing.T) {
	t.Parallel()
	c, err := loadConfig("/source", []string{
		"SCOPESIFTER_LIMIT_CAP=21",
		"SCOPESIFTER_CONTEXT_CAP=0",
		"SCOPESIFTER_MAX_CODE_LINES_CAP=61",
		"SCOPESIFTER_MAX_PATCH_LINES_CAP=301",
		"SCOPESIFTER_REASONING_EFFORT=inherit",
		"SCOPESIFTER_ANSWER_GUARD=off",
		"SCOPESIFTER_NAVIGATION_COMMAND_CAP=2",
	}, []string{"exec", "--json"})
	if err != nil {
		t.Fatal(err)
	}
	if c.limitCap != "21" || c.contextCap != "0" ||
		c.maxCodeLinesCap != "61" || c.maxPatchLinesCap != "301" ||
		c.reasoningEffort != "inherit" || c.answerGuard != "off" ||
		c.navigationCommandCap != "2" {
		t.Fatalf("overrides = %#v", c)
	}
}
