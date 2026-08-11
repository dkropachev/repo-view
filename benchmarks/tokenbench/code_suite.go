package tokenbench

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// CodeSuiteSchemaVersion identifies the separate authored contract for code
// tasks. It does not authorize a writable workspace by itself.
const CodeSuiteSchemaVersion = "tokenbench.suite/v3"

// CodeTaskBinding binds a code suite to one preregistered catalog task and its
// pinned language environment. Hidden evaluator and gold identities remain in
// the authenticated task bundle, outside the model-visible suite.
type CodeTaskBinding struct {
	TaskCatalogSHA256 string `json:"task_catalog_sha256"`
	TaskID            string `json:"task_id"`
	ToolchainSHA256   string `json:"toolchain_sha256"`
}

// CodeSuite is a single common code-task specification. Like Suite, it has no
// arm fields, tool registry, arbitrary environment, or caller-selected writable
// paths. Loading it produces audit data only; a future workspace authority must
// independently bind and authorize execution.
type CodeSuite struct {
	Model                  string          `json:"model"`
	ExpectedModelRevision  string          `json:"expected_model_revision"`
	DeveloperInstructions  string          `json:"developer_instructions"`
	ID                     string          `json:"id"`
	PromptFile             string          `json:"prompt_file"`
	HarnessKind            string          `json:"harness_kind"`
	HarnessExecutable      string          `json:"harness_executable"`
	HarnessSHA256          string          `json:"harness_sha256"`
	ArtifactManifestSHA256 string          `json:"artifact_manifest_sha256"`
	GitExecutable          string          `json:"git_executable"`
	GitExecutableSHA256    string          `json:"git_executable_sha256"`
	SourceTreeSHA256       string          `json:"source_tree_sha256"`
	SchemaVersion          string          `json:"schema_version"`
	ReasoningEffort        string          `json:"reasoning_effort"`
	PermissionProfile      string          `json:"permission_profile"`
	SourceRoot             string          `json:"source_root"`
	SourceRevision         string          `json:"source_revision"`
	SourceBaseRevision     string          `json:"source_base_revision"`
	CodeTask               CodeTaskBinding `json:"code_task"`
	TimeoutMillis          int64           `json:"timeout_millis"`
	Repetitions            int             `json:"repetitions"`
	Seed                   uint64          `json:"seed"`
}

// LoadedCodeSuite binds authored code-task configuration to its exact bytes,
// location, and prompt. Its accessors expose audit data, not workspace
// authority.
type LoadedCodeSuite struct {
	path         string
	raw          []byte
	sha256       string
	promptSHA256 string
	prompt       []byte
	suite        CodeSuite
}

// Suite returns a defensive copy of the authored common code specification.
func (loaded LoadedCodeSuite) Suite() CodeSuite { return loaded.suite }

// Path returns the absolute path loaded by LoadCodeSuite.
func (loaded LoadedCodeSuite) Path() string { return loaded.path }

// Digest returns the digest of the exact authored JSON bytes.
func (loaded LoadedCodeSuite) Digest() string { return loaded.sha256 }

// Raw returns a defensive copy of the exact authored suite JSON bytes.
func (loaded LoadedCodeSuite) Raw() []byte { return append([]byte(nil), loaded.raw...) }

// Prompt returns a defensive copy of the exact task bytes.
func (loaded LoadedCodeSuite) Prompt() []byte { return append([]byte(nil), loaded.prompt...) }

// PromptDigest returns the digest of the exact task bytes.
func (loaded LoadedCodeSuite) PromptDigest() string { return loaded.promptSHA256 }

// LoadCodeSuite strictly loads the separate v3 code-task contract. It does not
// construct an overlay, grant write access, or authorize execution.
func LoadCodeSuite(path string) (LoadedCodeSuite, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return LoadedCodeSuite{}, fmt.Errorf("resolve code suite path: %w", err)
	}
	if len(absPath) > maximumSuitePathBytes {
		return LoadedCodeSuite{}, fmt.Errorf(
			"code suite path exceeds %d bytes",
			maximumSuitePathBytes,
		)
	}
	raw, err := readStableRegularFileLimited(absPath, maximumSuiteBytes)
	if err != nil {
		return LoadedCodeSuite{}, fmt.Errorf("read code suite: %w", err)
	}
	if !utf8.Valid(raw) {
		return LoadedCodeSuite{}, errors.New("code suite JSON must be valid UTF-8")
	}
	if err := rejectDuplicateObjectKeys(raw); err != nil {
		return LoadedCodeSuite{}, fmt.Errorf("decode code suite: %w", err)
	}

	var suite CodeSuite
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&suite); err != nil {
		return LoadedCodeSuite{}, fmt.Errorf("decode code suite: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return LoadedCodeSuite{}, fmt.Errorf("decode code suite: %w", err)
	}
	if err := requireCodeSuiteFields(raw); err != nil {
		return LoadedCodeSuite{}, fmt.Errorf("decode code suite: %w", err)
	}
	if err := suite.Validate(); err != nil {
		return LoadedCodeSuite{}, err
	}

	base := filepath.Dir(absPath)
	suite.PromptFile = resolveRelative(base, suite.PromptFile)
	suite.SourceRoot = resolveRelative(base, suite.SourceRoot)
	if len(suite.PromptFile) > maximumSuitePathBytes ||
		len(suite.SourceRoot) > maximumSuitePathBytes {
		return LoadedCodeSuite{}, fmt.Errorf(
			"resolved code suite paths must not exceed %d bytes",
			maximumSuitePathBytes,
		)
	}
	prompt, err := readStableRegularFileLimited(suite.PromptFile, maximumPromptBytes)
	if err != nil {
		return LoadedCodeSuite{}, fmt.Errorf("snapshot code prompt: %w", err)
	}
	if len(prompt) == 0 {
		return LoadedCodeSuite{}, errors.New("code prompt must not be empty")
	}
	if !utf8.Valid(prompt) {
		return LoadedCodeSuite{}, errors.New("code prompt must be valid UTF-8")
	}
	if bytes.IndexByte(prompt, 0) >= 0 {
		return LoadedCodeSuite{}, errors.New("code prompt must not contain NUL")
	}
	if err := ValidateTreatmentNeutrality(suite.DeveloperInstructions, prompt); err != nil {
		return LoadedCodeSuite{}, err
	}

	return LoadedCodeSuite{
		suite:        suite,
		path:         absPath,
		raw:          append([]byte(nil), raw...),
		sha256:       SHA256(raw),
		prompt:       prompt,
		promptSHA256: SHA256(prompt),
	}, nil
}

func requireCodeSuiteFields(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	required := []string{
		"schema_version", "id", "prompt_file", "harness_kind",
		"harness_executable", "harness_sha256", "artifact_manifest_sha256",
		"git_executable", "git_executable_sha256", "model",
		"expected_model_revision", "reasoning_effort", "permission_profile",
		"developer_instructions", "source_root", "source_revision",
		"source_base_revision", "source_tree_sha256", "code_task",
		"timeout_millis", "repetitions", "seed",
	}
	if err := requireExactJSONFields(fields, required, "code suite"); err != nil {
		return err
	}
	var binding map[string]json.RawMessage
	if err := json.Unmarshal(fields["code_task"], &binding); err != nil {
		return fmt.Errorf("decode code_task: %w", err)
	}
	return requireExactJSONFields(
		binding,
		[]string{"task_catalog_sha256", "task_id", "toolchain_sha256"},
		"code_task",
	)
}

func requireExactJSONFields(
	fields map[string]json.RawMessage,
	required []string,
	role string,
) error {
	requiredSet := make(map[string]struct{}, len(required))
	for _, name := range required {
		requiredSet[name] = struct{}{}
		value, exists := fields[name]
		if !exists {
			return fmt.Errorf("required %s field %q is missing", role, name)
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("required %s field %q must not be null", role, name)
		}
	}
	for name := range fields {
		if _, exists := requiredSet[name]; !exists {
			return fmt.Errorf("%s field %q is not permitted", role, name)
		}
	}
	return nil
}

// Validate enforces the closed v3 code-task union without weakening Suite v2.
func (suite CodeSuite) Validate() error {
	bindingErr := suite.CodeTask.Validate()
	switch {
	case suite.SchemaVersion != CodeSuiteSchemaVersion:
		return fmt.Errorf("schema_version must be %q, got %q", CodeSuiteSchemaVersion, suite.SchemaVersion)
	case suite.PermissionProfile != "workspace-write":
		return errors.New("permission_profile must be workspace-write for a code suite")
	case suite.SourceRevision != suite.SourceBaseRevision:
		return errors.New("code suite source_base_revision must equal source_revision")
	case bindingErr != nil:
		return bindingErr
	}
	common := suite.readOnlyValidationView()
	return common.Validate()
}

// Validate enforces the closed catalog identity and digest binding.
func (binding CodeTaskBinding) Validate() error {
	switch {
	case !ValidSHA256(binding.TaskCatalogSHA256):
		return errors.New("code_task.task_catalog_sha256 must be a lowercase SHA-256 digest")
	case !ValidSHA256(binding.ToolchainSHA256):
		return errors.New("code_task.toolchain_sha256 must be a lowercase SHA-256 digest")
	case !validCodeTaskID(binding.TaskID):
		return errors.New("code_task.task_id must identify a locked code-family catalog cell")
	}
	return nil
}

func validCodeTaskID(taskID string) bool {
	parts := strings.Split(taskID, ".")
	if len(parts) != 4 || parts[2] != "code" {
		return false
	}
	repositories := map[string]map[string]struct{}{
		"cpp":        {"fmt": {}, "seastar": {}},
		"go":         {"chi": {}, "go-git": {}},
		"rust":       {"clap": {}, "scylla-driver": {}},
		"java":       {"commons-lang": {}, "scylla-driver": {}},
		"python":     {"click": {}, "scylla-ccm": {}},
		"typescript": {"got": {}, "kysely": {}},
	}
	repositorySet, exists := repositories[parts[0]]
	if !exists {
		return false
	}
	if _, exists := repositorySet[parts[1]]; !exists {
		return false
	}
	switch parts[3] {
	case "small", "medium", "large", "huge":
		return true
	default:
		return false
	}
}

func (suite CodeSuite) readOnlyValidationView() Suite {
	return Suite{
		Model: suite.Model, ExpectedModelRevision: suite.ExpectedModelRevision,
		DeveloperInstructions: suite.DeveloperInstructions, ID: suite.ID,
		PromptFile: suite.PromptFile, HarnessKind: suite.HarnessKind,
		HarnessExecutable: suite.HarnessExecutable, HarnessSHA256: suite.HarnessSHA256,
		ArtifactManifestSHA256: suite.ArtifactManifestSHA256,
		GitExecutable:          suite.GitExecutable, GitExecutableSHA256: suite.GitExecutableSHA256,
		SourceTreeSHA256: suite.SourceTreeSHA256, SchemaVersion: SuiteSchemaVersion,
		ReasoningEffort: suite.ReasoningEffort, PermissionProfile: "read-only",
		SourceRoot: suite.SourceRoot, SourceRevision: suite.SourceRevision,
		SourceBaseRevision: suite.SourceBaseRevision, TimeoutMillis: suite.TimeoutMillis,
		Repetitions: suite.Repetitions, Seed: suite.Seed,
	}
}
