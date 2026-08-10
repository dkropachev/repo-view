package harness

import (
	"strings"
	"testing"
)

func TestValidateUsage(t *testing.T) {
	t.Parallel()
	valid := Usage{InputTokens: 10, CachedInputTokens: 5, OutputTokens: 2, ReasoningTokens: 1}
	if err := ValidateUsage(valid); err != nil {
		t.Fatal(err)
	}
	tests := []Usage{
		{InputTokens: -1},
		{InputTokens: 1, CachedInputTokens: -1},
		{OutputTokens: -1},
		{ReasoningTokens: -1},
		{InputTokens: 1, CachedInputTokens: 2},
	}
	for index, usage := range tests {
		if err := ValidateUsage(usage); err == nil {
			t.Fatalf("invalid usage %d was accepted", index)
		}
	}
}

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
