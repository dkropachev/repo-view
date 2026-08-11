package tokenbench

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestLoadCodeSuiteSnapshotsExactCommonPrompt(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	prompt := []byte("Implement the bounded change.\n")
	if err := os.WriteFile(filepath.Join(directory, "prompt.md"), prompt, 0o600); err != nil {
		t.Fatal(err)
	}
	suite := validCodeSuite()
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
	loaded, err := LoadCodeSuite(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(loaded.Prompt(), prompt) ||
		loaded.PromptDigest() != SHA256(prompt) || loaded.Digest() != SHA256(raw) {
		t.Fatal("loaded code suite did not bind exact authored bytes")
	}
	returned := loaded.Raw()
	returned[0] ^= 1
	if !reflect.DeepEqual(loaded.Raw(), raw) {
		t.Fatal("Raw returned mutable internal bytes")
	}
}

func TestSuiteVersionsRemainDisjoint(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "prompt.md"), []byte("task\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	code := validCodeSuite()
	code.PromptFile = "prompt.md"
	code.SourceRoot = "source"
	codeRaw, err := json.Marshal(code)
	if err != nil {
		t.Fatal(err)
	}
	codePath := filepath.Join(directory, "code.json")
	if err := os.WriteFile(codePath, codeRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadSuite(codePath); err == nil {
		t.Fatal("v2 loader accepted a v3 code suite")
	}

	readOnly := validLoadedSuite().suite
	readOnly.PromptFile = "prompt.md"
	readOnly.SourceRoot = "source"
	readOnlyRaw, err := json.Marshal(readOnly)
	if err != nil {
		t.Fatal(err)
	}
	readOnlyPath := filepath.Join(directory, "read-only.json")
	if err := os.WriteFile(readOnlyPath, readOnlyRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCodeSuite(readOnlyPath); err == nil {
		t.Fatal("v3 code loader accepted a v2 read-only suite")
	}
	if _, err := LoadSuite(readOnlyPath); err != nil {
		t.Fatalf("v2 loader behavior changed: %v", err)
	}
}

func TestLoadCodeSuiteRejectsVariantAndMalformedBindings(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "prompt.md"), []byte("task\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	suite := validCodeSuite()
	suite.PromptFile = "prompt.md"
	suite.SourceRoot = "source"
	raw, err := json.Marshal(suite)
	if err != nil {
		t.Fatal(err)
	}
	var nullFields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &nullFields); err != nil {
		t.Fatal(err)
	}
	nullFields["code_task"] = json.RawMessage("null")
	nullBinding, err := json.Marshal(nullFields)
	if err != nil {
		t.Fatal(err)
	}
	addField := func(field string) []byte {
		content := append([]byte(nil), raw[:len(raw)-1]...)
		return append(content, []byte(","+field+"}")...)
	}
	replaceOnce := func(old, replacement string) []byte {
		t.Helper()
		if strings.Count(string(raw), old) != 1 {
			t.Fatalf("test fixture does not contain exactly one %q", old)
		}
		return []byte(strings.Replace(string(raw), old, replacement, 1))
	}
	type malformedCase struct {
		content []byte
		want    string
	}
	tests := map[string]malformedCase{
		"arm":            {addField(`"baseline":{"model":"other"}`), `unknown field "baseline"`},
		"case alias":     {addField(`"SEED":43`), `code suite field "SEED" is not permitted`},
		"trailing value": {append(append([]byte(nil), raw...), []byte(` {}`)...), "multiple JSON documents"},
		"writable path":  {addField(`"writable_paths":["/workspace"]`), `unknown field "writable_paths"`},
		"null binding":   {nullBinding, `required code suite field "code_task" must not be null`},
		"nested unknown": {replaceOnce(
			`"toolchain_sha256":"`+suite.CodeTask.ToolchainSHA256+`"`,
			`"toolchain_sha256":"`+suite.CodeTask.ToolchainSHA256+`","environment":{}`,
		), `unknown field "environment"`},
		"nested case alias": {replaceOnce(
			`"task_id":"`+suite.CodeTask.TaskID+`"`,
			`"task_id":"`+suite.CodeTask.TaskID+`","TASK_ID":"cpp.fmt.code.huge"`,
		), `code_task field "TASK_ID" is not permitted`},
		"nested duplicate": {replaceOnce(
			`"task_id":"`+suite.CodeTask.TaskID+`"`,
			`"task_id":"`+suite.CodeTask.TaskID+`","task_id":"cpp.fmt.code.huge"`,
		), `duplicate object key "task_id"`},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(directory, strings.ReplaceAll(name, " ", "-")+".json")
			if err := os.WriteFile(path, test.content, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := LoadCodeSuite(path); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("invalid code suite error = %v, want containing %q", err, test.want)
			}
		})
	}
}

func TestCodeSuiteRequiresClosedCodeIdentity(t *testing.T) {
	t.Parallel()
	tests := map[string]func(*CodeSuite){
		"schema":           func(suite *CodeSuite) { suite.SchemaVersion = SuiteSchemaVersion },
		"permission":       func(suite *CodeSuite) { suite.PermissionProfile = "read-only" },
		"different base":   func(suite *CodeSuite) { suite.SourceBaseRevision = strings.Repeat("2", 40) },
		"prose task":       func(suite *CodeSuite) { suite.CodeTask.TaskID = "cpp.fmt.review.small" },
		"unknown repo":     func(suite *CodeSuite) { suite.CodeTask.TaskID = "go.unknown.code.small" },
		"catalog digest":   func(suite *CodeSuite) { suite.CodeTask.TaskCatalogSHA256 = "bad" },
		"toolchain digest": func(suite *CodeSuite) { suite.CodeTask.ToolchainSHA256 = "bad" },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			suite := validCodeSuite()
			mutate(&suite)
			if err := suite.Validate(); err == nil {
				t.Fatal("invalid code suite was accepted")
			}
		})
	}
}

func TestCodeSuiteSchemaMatchesClosedType(t *testing.T) {
	t.Parallel()
	typeInfo := reflect.TypeFor[CodeSuite]()
	forbidden := map[string]bool{
		"baseline": true, "candidate": true, "optimized": true,
		"tools": true, "mcp_servers": true, "environment": true,
		"arguments": true, "writable_paths": true, "workspace_root": true,
		"gold_patch": true, "evaluator": true, "hidden_evaluator": true,
	}
	for index := range typeInfo.NumField() {
		name := strings.Split(typeInfo.Field(index).Tag.Get("json"), ",")[0]
		if forbidden[name] {
			t.Fatalf("CodeSuite exposes forbidden field %q", name)
		}
	}

	schema, err := os.ReadFile(filepath.Join("schemas", "suite-v3.schema.json"))
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
	required := make(map[string]struct{}, len(document.Required))
	for _, name := range document.Required {
		required[name] = struct{}{}
	}
	if len(document.Properties) != typeInfo.NumField() || len(required) != typeInfo.NumField() {
		t.Fatalf("schema/type field mismatch: fields=%d properties=%d required=%d", typeInfo.NumField(), len(document.Properties), len(required))
	}
	for index := range typeInfo.NumField() {
		name := strings.Split(typeInfo.Field(index).Tag.Get("json"), ",")[0]
		if _, exists := document.Properties[name]; !exists {
			t.Errorf("CodeSuite field %q is absent from schema", name)
		}
		if _, exists := required[name]; !exists {
			t.Errorf("CodeSuite field %q is not required", name)
		}
	}
	for name := range forbidden {
		if _, exists := document.Properties[name]; exists {
			t.Errorf("v3 schema exposes forbidden field %q", name)
		}
	}

	var bindingSchema struct {
		Properties           map[string]json.RawMessage `json:"properties"`
		Required             []string                   `json:"required"`
		AdditionalProperties bool                       `json:"additionalProperties"`
	}
	if err := json.Unmarshal(document.Properties["code_task"], &bindingSchema); err != nil {
		t.Fatal(err)
	}
	bindingType := reflect.TypeFor[CodeTaskBinding]()
	bindingRequired := make(map[string]struct{}, len(bindingSchema.Required))
	for _, name := range bindingSchema.Required {
		bindingRequired[name] = struct{}{}
	}
	if bindingSchema.AdditionalProperties ||
		len(bindingSchema.Properties) != bindingType.NumField() ||
		len(bindingRequired) != bindingType.NumField() {
		t.Fatal("code_task schema is not the exact closed CodeTaskBinding shape")
	}
	for index := range bindingType.NumField() {
		name := strings.Split(bindingType.Field(index).Tag.Get("json"), ",")[0]
		if _, exists := bindingSchema.Properties[name]; !exists {
			t.Errorf("CodeTaskBinding field %q is absent from schema", name)
		}
		if _, exists := bindingRequired[name]; !exists {
			t.Errorf("CodeTaskBinding field %q is not required", name)
		}
	}
}

func validCodeSuite() CodeSuite {
	common := validLoadedSuite().suite
	return CodeSuite{
		Model: common.Model, ExpectedModelRevision: common.ExpectedModelRevision,
		DeveloperInstructions: common.DeveloperInstructions, ID: common.ID,
		PromptFile: common.PromptFile, HarnessKind: common.HarnessKind,
		HarnessExecutable: common.HarnessExecutable, HarnessSHA256: common.HarnessSHA256,
		ArtifactManifestSHA256: common.ArtifactManifestSHA256,
		GitExecutable:          common.GitExecutable, GitExecutableSHA256: common.GitExecutableSHA256,
		SourceTreeSHA256: common.SourceTreeSHA256, SchemaVersion: CodeSuiteSchemaVersion,
		ReasoningEffort: common.ReasoningEffort, PermissionProfile: "workspace-write",
		SourceRoot: common.SourceRoot, SourceRevision: common.SourceRevision,
		SourceBaseRevision: common.SourceRevision,
		CodeTask: CodeTaskBinding{
			TaskCatalogSHA256: SHA256([]byte("catalog")),
			TaskID:            "cpp.fmt.code.small",
			ToolchainSHA256:   SHA256([]byte("toolchain")),
		},
		TimeoutMillis: common.TimeoutMillis, Repetitions: common.Repetitions, Seed: common.Seed,
	}
}
