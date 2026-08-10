package tokenbench

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadSuiteRejectsUnknownDuplicateAndVariantFields(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	promptPath := filepath.Join(directory, "prompt.md")
	if err := os.WriteFile(promptPath, []byte("task\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	suite := validLoadedSuite().suite
	suite.PromptFile = "prompt.md"
	suite.SourceRoot = "source"
	raw, err := json.Marshal(suite)
	if err != nil {
		t.Fatal(err)
	}

	tests := map[string][]byte{
		"unknown": append(raw[:len(raw)-1], []byte(`,"candidate_prompt":"answer key"}`)...),
		"duplicate": []byte(strings.Replace(
			string(raw),
			`"id":"fixture"`,
			`"id":"fixture","id":"other"`,
			1,
		)),
		"arm override":  append(raw[:len(raw)-1], []byte(`,"baseline":{"model":"other"}}`)...),
		"tool registry": append(raw[:len(raw)-1], []byte(`,"mcp_servers":[]}`)...),
	}
	for name, content := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(directory, strings.ReplaceAll(name, " ", "-")+".json")
			if err := os.WriteFile(path, content, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadSuite(path); err == nil {
				t.Fatal("invalid suite was accepted")
			}
		})
	}
}

func TestLoadSuiteSnapshotsExactPrompt(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	prompt := []byte("byte exact prompt\n")
	if err := os.WriteFile(filepath.Join(directory, "prompt.md"), prompt, 0o600); err != nil {
		t.Fatal(err)
	}
	suite := validLoadedSuite().suite
	suite.PromptFile = "prompt.md"
	suite.SourceRoot = "source"
	raw, err := json.Marshal(suite)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(directory, "suite.json")
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadSuite(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.Prompt(), prompt) {
		t.Fatalf("prompt bytes changed: got %q, want %q", loaded.Prompt(), prompt)
	}
	if loaded.PromptDigest() != SHA256(prompt) || loaded.Digest() != SHA256(raw) {
		t.Fatal("loaded suite did not bind exact source bytes")
	}
	returnedPrompt := loaded.Prompt()
	returnedSuite := loaded.Suite()
	returnedPrompt[0] = 'X'
	returnedSuite.ID = "mutated"
	if !reflect.DeepEqual(loaded.Prompt(), prompt) {
		t.Fatal("Prompt returned mutable internal bytes")
	}
	if loaded.Suite().ID == "mutated" {
		t.Fatal("Suite returned mutable internal state")
	}
}

func TestLoadSuiteRequiresEverySchemaFieldAndRejectsNull(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(directory, "prompt.md"),
		[]byte("task\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	suite := validLoadedSuite().suite
	suite.PromptFile = "prompt.md"
	suite.SourceRoot = "source"
	raw, err := json.Marshal(suite)
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	suiteType := reflect.TypeFor[Suite]()
	for index := range suiteType.NumField() {
		name := strings.Split(suiteType.Field(index).Tag.Get("json"), ",")[0]
		copyFields := make(map[string]json.RawMessage, len(fields))
		for key, value := range fields {
			copyFields[key] = value
		}
		delete(copyFields, name)
		content, marshalErr := json.Marshal(copyFields)
		if marshalErr != nil {
			t.Fatal(marshalErr)
		}
		path := filepath.Join(directory, "missing-"+name+".json")
		if err := os.WriteFile(path, content, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := LoadSuite(path); err == nil ||
			!strings.Contains(err.Error(), "required field") {
			t.Fatalf("missing field %q was not rejected: %v", name, err)
		}
	}

	fields["developer_instructions"] = json.RawMessage("null")
	nullRaw, err := json.Marshal(fields)
	if err != nil {
		t.Fatal(err)
	}
	nullPath := filepath.Join(directory, "null.json")
	if err := os.WriteFile(nullPath, nullRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSuite(nullPath); err == nil ||
		!strings.Contains(err.Error(), "must not be null") {
		t.Fatalf("null field was not rejected: %v", err)
	}
}

func TestLoadSuiteRejectsInvalidUTF8(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "suite.json")
	if err := os.WriteFile(path, []byte{'{', '"', 'i', 'd', '"', ':', '"', 0xff, '"', '}'}, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSuite(path); err == nil ||
		!strings.Contains(err.Error(), "UTF-8") {
		t.Fatalf("invalid UTF-8 was not rejected: %v", err)
	}
}

func TestSuiteRejectsAmbiguousModelAliases(t *testing.T) {
	t.Parallel()
	models := []string{
		"auto",
		"default",
		"router-selected",
		"model-latest",
		"model-*",
		"stable",
		"production-model",
		" model-v1",
		"model-v1 ",
	}
	for index, model := range models {
		suite := validLoadedSuite().suite
		suite.Model = model
		suite.ExpectedModelRevision = model + "@2026-08-01"
		if err := suite.Validate(); err == nil {
			t.Fatalf("ambiguous model %d (%q) was accepted", index, model)
		}
	}
}

func TestSuiteRequiresStructuredImmutableModelRevision(t *testing.T) {
	t.Parallel()
	for _, revision := range []string{
		"production1",
		"gpt-5.2-codex@latest1",
		"gpt-5.2-codex@stable1",
		"gpt-5.2-codex@revision",
		"other-model@2026-08-01",
		" gpt-5.2-codex@2026-08-01",
	} {
		suite := validLoadedSuite().suite
		suite.ExpectedModelRevision = revision
		if err := suite.Validate(); err == nil {
			t.Fatalf("ambiguous model revision %q was accepted", revision)
		}
	}
}

func TestSuiteRejectsTimeoutDurationOverflow(t *testing.T) {
	t.Parallel()
	suite := validLoadedSuite().suite
	suite.TimeoutMillis = 1<<63 - 1
	if err := suite.Validate(); err == nil {
		t.Fatal("overflowing timeout was accepted")
	}
}

func TestSuiteRejectsPracticalResourceBounds(t *testing.T) {
	t.Parallel()
	tests := map[string]func(*Suite){
		"repetitions": func(suite *Suite) { suite.Repetitions = maxRepetitions + 1 },
		"timeout":     func(suite *Suite) { suite.TimeoutMillis = maximumTimeoutMillis + 1 },
		"id":          func(suite *Suite) { suite.ID = strings.Repeat("a", maximumSuiteIDBytes+1) },
		"prompt path": func(suite *Suite) {
			suite.PromptFile = strings.Repeat("p", maximumSuitePathBytes+1)
		},
		"harness kind": func(suite *Suite) {
			suite.HarnessKind = strings.Repeat("h", maximumHarnessKindBytes+1)
		},
		"harness path": func(suite *Suite) {
			suite.HarnessExecutable = "/" + strings.Repeat("h", maximumSuitePathBytes)
		},
		"git path": func(suite *Suite) {
			suite.GitExecutable = "/" + strings.Repeat("g", maximumSuitePathBytes)
		},
		"model": func(suite *Suite) {
			suite.Model = strings.Repeat("m", maximumModelBytes+1)
			suite.ExpectedModelRevision = suite.Model + "@2026-08-01"
		},
		"model revision": func(suite *Suite) {
			suite.ExpectedModelRevision = suite.Model + "@" +
				strings.Repeat("2", maximumModelRevisionBytes)
		},
		"reasoning effort": func(suite *Suite) {
			suite.ReasoningEffort = strings.Repeat("r", maximumReasoningEffortBytes+1)
		},
		"developer instructions": func(suite *Suite) {
			suite.DeveloperInstructions = strings.Repeat("d", maximumInstructionsBytes+1)
		},
		"developer NUL": func(suite *Suite) {
			suite.DeveloperInstructions = "instruction\x00suffix"
		},
		"source root": func(suite *Suite) {
			suite.SourceRoot = strings.Repeat("s", maximumSuitePathBytes+1)
		},
	}
	for name, mutate := range tests {
		name, mutate := name, mutate
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			suite := validLoadedSuite().suite
			mutate(&suite)
			if err := suite.Validate(); err == nil {
				t.Fatal("suite beyond practical bound was accepted")
			}
		})
	}
}

func TestLoadSuiteRejectsOversizedSuiteAndPromptBeforeAllocation(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	oversizedSuite := filepath.Join(directory, "oversized-suite.json")
	file, err := os.OpenFile(oversizedSuite, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maximumSuiteBytes + 1); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSuite(oversizedSuite); err == nil ||
		!strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized suite error = %v, want byte limit", err)
	}

	promptPath := filepath.Join(directory, "prompt.md")
	prompt, err := os.OpenFile(promptPath, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := prompt.Truncate(maximumPromptBytes + 1); err != nil {
		_ = prompt.Close()
		t.Fatal(err)
	}
	if err := prompt.Close(); err != nil {
		t.Fatal(err)
	}
	suite := validLoadedSuite().suite
	suite.PromptFile = "prompt.md"
	suite.SourceRoot = "source"
	raw, err := json.Marshal(suite)
	if err != nil {
		t.Fatal(err)
	}
	suitePath := filepath.Join(directory, "suite.json")
	if err := os.WriteFile(suitePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSuite(suitePath); err == nil ||
		!strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized prompt error = %v, want byte limit", err)
	}
}

func TestSuiteSchemaHasNoArmOrToolConfiguration(t *testing.T) {
	t.Parallel()
	typeInfo := reflect.TypeFor[Suite]()
	forbidden := map[string]bool{
		"baseline":    true,
		"candidate":   true,
		"optimized":   true,
		"tools":       true,
		"mcp_servers": true,
		"environment": true,
		"arguments":   true,
	}
	for index := range typeInfo.NumField() {
		name := strings.Split(typeInfo.Field(index).Tag.Get("json"), ",")[0]
		if forbidden[name] {
			t.Fatalf("Suite exposes forbidden variant field %q", name)
		}
	}

	schema, err := os.ReadFile(filepath.Join("schemas", "suite-v1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	var document struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(schema, &document); err != nil {
		t.Fatal(err)
	}
	for name := range forbidden {
		if _, exists := document.Properties[name]; exists {
			t.Fatalf("suite schema exposes forbidden variant field %q", name)
		}
	}
	required := make(map[string]struct{}, len(document.Required))
	for _, name := range document.Required {
		required[name] = struct{}{}
	}
	if len(document.Properties) != typeInfo.NumField() ||
		len(required) != typeInfo.NumField() {
		t.Fatalf(
			"schema fields/properties do not match Suite: fields=%d properties=%d required=%d",
			typeInfo.NumField(),
			len(document.Properties),
			len(required),
		)
	}
	for index := range typeInfo.NumField() {
		name := strings.Split(typeInfo.Field(index).Tag.Get("json"), ",")[0]
		if _, exists := document.Properties[name]; !exists {
			t.Errorf("Suite field %q is missing from schema properties", name)
		}
		if _, exists := required[name]; !exists {
			t.Errorf("Suite field %q is not required by schema", name)
		}
	}
}
