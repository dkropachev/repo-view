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
		c.changedReturn != "context" ||
		c.changedContext != "4" ||
		c.changedLimit != "20" ||
		c.changedMaxCodeLines != "60" ||
		c.changedMaxPatchLines != "300" ||
		c.reasoningEffort != "high" ||
		c.answerGuard != "on" ||
		c.navigationPolicy != "terminal" ||
		c.navigationContextCap != "20" ||
		c.navigationCommandCap != "0" {
		t.Fatalf("unexpected defaults: %#v", c)
	}
}

func TestLoadConfigComparesArbitrarilyLargeDecimals(t *testing.T) {
	t.Parallel()
	huge := strings.Repeat("9", 10_000)
	c, err := loadConfig("/source", []string{
		"SCOPESIFTER_CHANGED_CONTEXT=000" + huge,
		"SCOPESIFTER_NAVIGATION_CONTEXT_CAP=" + huge,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if c.changedContext != "000"+huge {
		t.Fatal("the original decimal spelling was not preserved")
	}
	_, err = loadConfig("/source", []string{
		"SCOPESIFTER_CHANGED_CONTEXT=1" + huge,
		"SCOPESIFTER_NAVIGATION_CONTEXT_CAP=" + huge,
	}, nil)
	if err == nil || !strings.Contains(err.Error(), "exceeds navigation_context_cap") {
		t.Fatalf("error = %v", err)
	}
}

func TestLoadConfigRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()
	mechanical := []string{
		"SCOPESIFTER_NAVIGATION_COMMAND_CAP=1",
		"SCOPESIFTER_REQUIRE_NAVIGATION_SEMANTICS=1",
		"SCOPESIFTER_REQUIRED_ROOT=/tmp",
		"SCOPESIFTER_REQUIRED_BASE_COMMIT=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"SCOPESIFTER_REQUIRED_CHANGED_RETURN=context",
		"SCOPESIFTER_REQUIRED_CHANGED_CONTEXT=4",
	}
	tests := []struct {
		name        string
		environment []string
		arguments   []string
		want        string
	}{
		{"invalid return", []string{"SCOPESIFTER_CHANGED_RETURN=all"}, nil, "invalid SCOPESIFTER_CHANGED_RETURN"},
		{"negative context", []string{"SCOPESIFTER_CHANGED_CONTEXT=-1"}, nil, "must be a non-negative integer"},
		{"empty defaults", []string{"SCOPESIFTER_CHANGED_LIMIT="}, nil, ""},
		{"zero limit", []string{"SCOPESIFTER_CHANGED_LIMIT=000"}, nil, "changed_limit must be a positive integer"},
		{"zero code lines", []string{"SCOPESIFTER_CHANGED_MAX_CODE_LINES=0"}, nil, "changed_max_code_lines must be a positive integer"},
		{"zero patch lines", []string{"SCOPESIFTER_CHANGED_MAX_PATCH_LINES=0"}, nil, "changed_max_patch_lines must be a positive integer"},
		{"invalid reasoning", []string{"SCOPESIFTER_REASONING_EFFORT=max"}, nil, "invalid SCOPESIFTER_REASONING_EFFORT"},
		{"invalid guard", []string{"SCOPESIFTER_ANSWER_GUARD=yes"}, nil, "invalid SCOPESIFTER_ANSWER_GUARD"},
		{"invalid policy", []string{"SCOPESIFTER_NAVIGATION_POLICY=unbounded"}, nil, "invalid SCOPESIFTER_NAVIGATION_POLICY"},
		{"incomplete semantics", []string{"SCOPESIFTER_REQUIRE_NAVIGATION_SEMANTICS=1"}, nil, "configuration is incomplete"},
		{"semantics zero commands", replaceEnv(mechanical, "SCOPESIFTER_NAVIGATION_COMMAND_CAP=0"), nil, "require a positive command cap"},
		{"invalid required context", replaceEnv(mechanical, "SCOPESIFTER_REQUIRED_CHANGED_CONTEXT=many"), nil, "REQUIRED_CHANGED_CONTEXT must be a non-negative integer"},
		{"short object ID", replaceEnv(mechanical, "SCOPESIFTER_REQUIRED_BASE_COMMIT=aaaa"), nil, "must be a full lowercase object ID"},
		{"uppercase object ID", replaceEnv(mechanical, "SCOPESIFTER_REQUIRED_BASE_COMMIT=AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"), nil, "must be a full lowercase object ID"},
		{"invalid required return", replaceEnv(mechanical, "SCOPESIFTER_REQUIRED_CHANGED_RETURN=all"), nil, "invalid SCOPESIFTER_REQUIRED_CHANGED_RETURN"},
		{"return mismatch", replaceEnv(mechanical, "SCOPESIFTER_REQUIRED_CHANGED_RETURN=locations"), nil, "must match SCOPESIFTER_CHANGED_RETURN"},
		{"context mismatch", replaceEnv(mechanical, "SCOPESIFTER_REQUIRED_CHANGED_CONTEXT=5"), nil, "REQUIRED_CHANGED_CONTEXT must match"},
		{"zero semantic code context", replaceEnv(
			replaceEnv(mechanical, "SCOPESIFTER_CHANGED_CONTEXT=0"),
			"SCOPESIFTER_REQUIRED_CHANGED_CONTEXT=0",
		), nil, "must be positive unless return is locations"},
		{"capped without JSON", mechanical, []string{"exec"}, "requires codex --json events"},
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

func TestLoadConfigAcceptsCompleteMechanicalSemantics(t *testing.T) {
	t.Parallel()
	c, err := loadConfig("/source", []string{
		"SCOPESIFTER_CHANGED_RETURN=locations",
		"SCOPESIFTER_CHANGED_CONTEXT=000",
		"SCOPESIFTER_NAVIGATION_COMMAND_CAP=0002",
		"SCOPESIFTER_REQUIRE_NAVIGATION_SEMANTICS=1",
		"SCOPESIFTER_REQUIRED_ROOT=/tmp/source root",
		"SCOPESIFTER_REQUIRED_BASE_COMMIT=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"SCOPESIFTER_REQUIRED_CHANGED_RETURN=locations",
		"SCOPESIFTER_REQUIRED_CHANGED_CONTEXT=0",
	}, []string{"exec", "--json"})
	if err != nil {
		t.Fatal(err)
	}
	if !c.navigationSemanticsConfigured {
		t.Fatal("mechanical semantics were not enabled")
	}
}

func replaceEnv(environment []string, replacement string) []string {
	name, _, _ := strings.Cut(replacement, "=")
	result := make([]string, 0, len(environment)+1)
	for _, entry := range environment {
		entryName, _, _ := strings.Cut(entry, "=")
		if entryName != name {
			result = append(result, entry)
		}
	}
	return append(result, replacement)
}
