package harness

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestValidateUsage(t *testing.T) {
	t.Parallel()
	providerTotal := int64(12)
	valid := Usage{
		InputTokens: 10, CachedInputTokens: 5, CacheWriteInputTokens: 3,
		OutputTokens: 2, ReasoningTokens: 1, ProviderTotalTokens: &providerTotal,
	}
	if err := ValidateUsage(valid); err != nil {
		t.Fatal(err)
	}
	tests := []Usage{
		{InputTokens: -1},
		{InputTokens: 1, CachedInputTokens: -1},
		{CacheWriteInputTokens: -1},
		{OutputTokens: -1},
		{ReasoningTokens: -1},
		{ProviderTotalTokens: int64Pointer(-1)},
		{InputTokens: 1, CachedInputTokens: 2},
	}
	for index, usage := range tests {
		if err := ValidateUsage(usage); err == nil {
			t.Fatalf("invalid usage %d was accepted", index)
		}
	}
}

func TestCloneObservationPreservesProviderTotalPresenceWithoutAliasing(t *testing.T) {
	t.Parallel()
	providerTotal := int64(0)
	source := Observation{
		ToolCalls: []string{"exec_command"},
		Usage:     Usage{ProviderTotalTokens: &providerTotal},
	}
	clone := CloneObservation(source)
	if clone.Usage.ProviderTotalTokens == nil || *clone.Usage.ProviderTotalTokens != 0 {
		t.Fatalf("present zero provider total was lost: %+v", clone.Usage)
	}
	if clone.Usage.ProviderTotalTokens == source.Usage.ProviderTotalTokens {
		t.Fatal("provider total pointer was aliased")
	}
	clone.ToolCalls[0] = "changed"
	*clone.Usage.ProviderTotalTokens = 1
	if source.ToolCalls[0] != "exec_command" || *source.Usage.ProviderTotalTokens != 0 {
		t.Fatalf("clone mutated source: %+v", source)
	}
	if !EqualUsageNativeComponents(Usage{InputTokens: 1}, Usage{
		InputTokens: 1, ProviderTotalTokens: int64Pointer(99),
	}) {
		t.Fatal("optional provider total affected native-component equality")
	}
	if empty := CloneObservation(Observation{ToolCalls: []string{}}); empty.ToolCalls == nil {
		t.Fatal("defensive clone changed a canonical empty tool-call list to nil")
	}
}

func TestUsageJSONDistinguishesMissingFromPresentZeroProviderTotal(t *testing.T) {
	t.Parallel()
	absent, err := json.Marshal(Usage{})
	if err != nil {
		t.Fatal(err)
	}
	present, err := json.Marshal(Usage{ProviderTotalTokens: int64Pointer(0)})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(absent), "provider_total_tokens") ||
		!strings.Contains(string(present), `"provider_total_tokens":0`) {
		t.Fatalf("provider-total JSON presence: absent=%s present=%s", absent, present)
	}
	var roundTrip Usage
	if err := json.Unmarshal(present, &roundTrip); err != nil {
		t.Fatal(err)
	}
	if roundTrip.ProviderTotalTokens == nil || *roundTrip.ProviderTotalTokens != 0 {
		t.Fatalf("present zero did not round trip: %+v", roundTrip)
	}
}

func int64Pointer(value int64) *int64 { return &value }

func TestValidateProcessSpecRejectsInvalidText(t *testing.T) {
	t.Parallel()
	valid := ProcessSpec{
		Environment:   map[string]string{"LANG": "C"},
		Directory:     "/source",
		Argv:          []string{"/bin/tool", "argument"},
		TimeoutMillis: 1,
	}
	for name, mutate := range map[string]func(*ProcessSpec){
		"argument NUL": func(process *ProcessSpec) {
			process.Argv[1] = "bad\x00argument"
		},
		"directory NUL": func(process *ProcessSpec) {
			process.Directory = "/bad\x00directory"
		},
		"environment key NUL": func(process *ProcessSpec) {
			process.Environment = map[string]string{"BAD\x00KEY": "value"}
		},
		"environment value NUL": func(process *ProcessSpec) {
			process.Environment = map[string]string{"KEY": "bad\x00value"}
		},
		"argument invalid UTF-8": func(process *ProcessSpec) {
			process.Argv[1] = string([]byte{0xff})
		},
		"timeout overflow": func(process *ProcessSpec) {
			process.TimeoutMillis = 1<<63 - 1
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			process := valid
			process.Environment = map[string]string{"LANG": "C"}
			process.Argv = append([]string(nil), valid.Argv...)
			mutate(&process)
			if err := ValidateProcessSpec(process); err == nil ||
				!strings.Contains(err.Error(), "invalid") {
				t.Fatalf("invalid process was accepted: %v", err)
			}
		})
	}
}

func TestValidateEnvironmentRejectsSecretsAndRuntimeInjection(t *testing.T) {
	t.Parallel()
	for name, environment := range map[string]map[string]string{
		"secret":     {"OPENAI_API_KEY": "real-secret"},
		"AWS secret": {"AWS_SECRET_ACCESS_KEY": "real-secret"},
		"opaque":     {"X": "real-secret"},
		"loader":     {"LD_PRELOAD": "/tmp/inject.so"},
		"proxy":      {"HTTPS_PROXY": "https://ambient.invalid"},
		"Git":        {"GIT_DIR": "/other/repository"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if err := ValidatePublishableEnvironment(environment); err == nil {
				t.Fatalf("unsafe environment was accepted: %v", environment)
			}
		})
	}
	if err := ValidatePublishableEnvironment(map[string]string{
		"CODEX_API_KEY": OfflineLocalProxyCapability,
		"LANG":          "C.UTF-8",
		"TZ":            "UTC",
	}); err != nil {
		t.Fatalf("fixed local proxy environment was rejected: %v", err)
	}
}
