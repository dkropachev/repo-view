// Package tokenbench provides the strict paired-run model used by the
// scopesifter token benchmark.
package tokenbench

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"github.com/scopesifter/scopesifter/benchmarks/tokenbench/harness"
)

// SuiteSchemaVersion is the only authored suite schema accepted by this
// implementation. Publishable preparation requires v2's artifact-manifest
// commitment.
const SuiteSchemaVersion = "tokenbench.suite/v2"

const (
	maxRepetitions              = 100
	maximumSuiteBytes           = 1 << 20
	maximumPromptBytes          = 1 << 20
	maximumSuiteIDBytes         = 128
	maximumModelBytes           = 128
	maximumModelRevisionBytes   = 256
	maximumHarnessKindBytes     = 64
	maximumReasoningEffortBytes = 64
	maximumInstructionsBytes    = 64 << 10
	maximumSuitePathBytes       = 4_096
	maximumTimeoutMillis        = int64(2 * 60 * 60 * 1_000)
)

// Suite is deliberately a single common specification. It has no arm fields,
// no tool registry, and no arbitrary per-arm extension map.
type Suite struct {
	Model                  string `json:"model"`
	ExpectedModelRevision  string `json:"expected_model_revision"`
	DeveloperInstructions  string `json:"developer_instructions"`
	ID                     string `json:"id"`
	PromptFile             string `json:"prompt_file"`
	HarnessKind            string `json:"harness_kind"`
	HarnessExecutable      string `json:"harness_executable"`
	HarnessSHA256          string `json:"harness_sha256"`
	ArtifactManifestSHA256 string `json:"artifact_manifest_sha256"`
	GitExecutable          string `json:"git_executable"`
	GitExecutableSHA256    string `json:"git_executable_sha256"`
	SourceTreeSHA256       string `json:"source_tree_sha256"`
	SchemaVersion          string `json:"schema_version"`
	ReasoningEffort        string `json:"reasoning_effort"`
	PermissionProfile      string `json:"permission_profile"`
	SourceRoot             string `json:"source_root"`
	SourceRevision         string `json:"source_revision"`
	SourceBaseRevision     string `json:"source_base_revision"`
	TimeoutMillis          int64  `json:"timeout_millis"`
	Repetitions            int    `json:"repetitions"`
	Seed                   uint64 `json:"seed"`
}

// LoadedSuite binds authored configuration to its exact bytes and location.
// The raw digest makes later plans commit to what was actually parsed.
type LoadedSuite struct {
	path         string
	raw          []byte
	sha256       string
	promptSHA256 string
	prompt       []byte
	suite        Suite
}

// Suite returns a defensive copy of the authored common specification.
func (loaded LoadedSuite) Suite() Suite {
	return loaded.suite
}

// Path returns the absolute path loaded by LoadSuite.
func (loaded LoadedSuite) Path() string {
	return loaded.path
}

// Digest returns the digest of the exact authored JSON bytes.
func (loaded LoadedSuite) Digest() string {
	return loaded.sha256
}

// Raw returns a defensive copy of the exact authored suite JSON bytes.
func (loaded LoadedSuite) Raw() []byte {
	return append([]byte(nil), loaded.raw...)
}

// Prompt returns a defensive copy of the exact task bytes.
func (loaded LoadedSuite) Prompt() []byte {
	return append([]byte(nil), loaded.prompt...)
}

// PromptDigest returns the digest of the exact task bytes.
func (loaded LoadedSuite) PromptDigest() string {
	return loaded.promptSHA256
}

// LoadSuite strictly decodes one JSON document, resolves filesystem paths
// relative to that document, and snapshots the prompt bytes without rewriting
// the source files.
func LoadSuite(path string) (LoadedSuite, error) {
	absPath, err := filepath.Abs(path)
	if err != nil {
		return LoadedSuite{}, fmt.Errorf("resolve suite path: %w", err)
	}
	if len(absPath) > maximumSuitePathBytes {
		return LoadedSuite{}, fmt.Errorf(
			"suite path exceeds %d bytes",
			maximumSuitePathBytes,
		)
	}
	raw, err := readStableRegularFileLimited(absPath, maximumSuiteBytes)
	if err != nil {
		return LoadedSuite{}, fmt.Errorf("read suite: %w", err)
	}
	if !utf8.Valid(raw) {
		return LoadedSuite{}, errors.New("suite JSON must be valid UTF-8")
	}
	if err := rejectDuplicateObjectKeys(raw); err != nil {
		return LoadedSuite{}, fmt.Errorf("decode suite: %w", err)
	}

	var suite Suite
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&suite); err != nil {
		return LoadedSuite{}, fmt.Errorf("decode suite: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return LoadedSuite{}, fmt.Errorf("decode suite: %w", err)
	}
	if err := requireSuiteFields(raw); err != nil {
		return LoadedSuite{}, fmt.Errorf("decode suite: %w", err)
	}
	if err := suite.Validate(); err != nil {
		return LoadedSuite{}, err
	}

	base := filepath.Dir(absPath)
	suite.PromptFile = resolveRelative(base, suite.PromptFile)
	suite.SourceRoot = resolveRelative(base, suite.SourceRoot)
	if len(suite.PromptFile) > maximumSuitePathBytes ||
		len(suite.SourceRoot) > maximumSuitePathBytes {
		return LoadedSuite{}, fmt.Errorf(
			"resolved suite paths must not exceed %d bytes",
			maximumSuitePathBytes,
		)
	}
	prompt, err := readStableRegularFileLimited(
		suite.PromptFile,
		maximumPromptBytes,
	)
	if err != nil {
		return LoadedSuite{}, fmt.Errorf("snapshot prompt: %w", err)
	}
	if len(prompt) == 0 {
		return LoadedSuite{}, errors.New("prompt must not be empty")
	}
	if !utf8.Valid(prompt) {
		return LoadedSuite{}, errors.New("prompt must be valid UTF-8")
	}
	if bytes.IndexByte(prompt, 0) >= 0 {
		return LoadedSuite{}, errors.New("prompt must not contain NUL")
	}
	if err := ValidateTreatmentNeutrality(
		suite.DeveloperInstructions,
		prompt,
	); err != nil {
		return LoadedSuite{}, err
	}

	return LoadedSuite{
		suite:        suite,
		path:         absPath,
		raw:          append([]byte(nil), raw...),
		sha256:       SHA256(raw),
		prompt:       prompt,
		promptSHA256: SHA256(prompt),
	}, nil
}

func requireSuiteFields(raw []byte) error {
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return err
	}
	required := []string{
		"schema_version",
		"id",
		"prompt_file",
		"harness_kind",
		"harness_executable",
		"harness_sha256",
		"artifact_manifest_sha256",
		"git_executable",
		"git_executable_sha256",
		"model",
		"expected_model_revision",
		"reasoning_effort",
		"permission_profile",
		"developer_instructions",
		"source_root",
		"source_revision",
		"source_base_revision",
		"source_tree_sha256",
		"timeout_millis",
		"repetitions",
		"seed",
	}
	for _, name := range required {
		value, exists := fields[name]
		if !exists {
			return fmt.Errorf("required field %q is missing", name)
		}
		if bytes.Equal(bytes.TrimSpace(value), []byte("null")) {
			return fmt.Errorf("required field %q must not be null", name)
		}
	}
	return nil
}

// Validate enforces fields that must be known before pair resolution.
func (suite Suite) Validate() error {
	developerNeutralityErr := validateNeutralText(
		"developer_instructions",
		suite.DeveloperInstructions,
	)
	switch {
	case suite.SchemaVersion != SuiteSchemaVersion:
		return fmt.Errorf(
			"schema_version must be %q, got %q",
			SuiteSchemaVersion,
			suite.SchemaVersion,
		)
	case strings.TrimSpace(suite.ID) == "":
		return errors.New("suite id is required")
	case strings.TrimSpace(suite.ID) != suite.ID:
		return errors.New("suite id must not have leading or trailing whitespace")
	case !validIdentifier(suite.ID, true):
		return errors.New("suite id contains invalid characters")
	case len(suite.ID) > maximumSuiteIDBytes:
		return fmt.Errorf("suite id must not exceed %d bytes", maximumSuiteIDBytes)
	case suite.PromptFile == "":
		return errors.New("prompt_file is required")
	case len(suite.PromptFile) > maximumSuitePathBytes:
		return fmt.Errorf("prompt_file must not exceed %d bytes", maximumSuitePathBytes)
	case suite.HarnessKind == "":
		return errors.New("harness_kind is required")
	case len(suite.HarnessKind) > maximumHarnessKindBytes:
		return fmt.Errorf("harness_kind must not exceed %d bytes", maximumHarnessKindBytes)
	case suite.HarnessExecutable == "":
		return errors.New("harness_executable is required")
	case len(suite.HarnessExecutable) > maximumSuitePathBytes:
		return fmt.Errorf("harness_executable must not exceed %d bytes", maximumSuitePathBytes)
	case !filepath.IsAbs(suite.HarnessExecutable):
		return errors.New("harness_executable must be an absolute path")
	case filepath.Clean(suite.HarnessExecutable) != suite.HarnessExecutable:
		return errors.New("harness_executable must be a canonical path")
	case !ValidSHA256(suite.HarnessSHA256):
		return errors.New("harness_sha256 must be a lowercase SHA-256 digest")
	case !ValidSHA256(suite.ArtifactManifestSHA256):
		return errors.New("artifact_manifest_sha256 must be a lowercase SHA-256 digest")
	case len(suite.GitExecutable) > maximumSuitePathBytes:
		return fmt.Errorf("git_executable must not exceed %d bytes", maximumSuitePathBytes)
	case !filepath.IsAbs(suite.GitExecutable):
		return errors.New("git_executable must be an absolute path")
	case filepath.Clean(suite.GitExecutable) != suite.GitExecutable:
		return errors.New("git_executable must be a canonical path")
	case !ValidSHA256(suite.GitExecutableSHA256):
		return errors.New("git_executable_sha256 must be a lowercase SHA-256 digest")
	case !isPinnedModel(suite.Model):
		return fmt.Errorf("model %q is not an explicit model selection", suite.Model)
	case len(suite.Model) > maximumModelBytes:
		return fmt.Errorf("model must not exceed %d bytes", maximumModelBytes)
	case !validModelRevision(suite.ExpectedModelRevision):
		return errors.New("expected_model_revision must be an immutable model identity")
	case len(suite.ExpectedModelRevision) > maximumModelRevisionBytes:
		return fmt.Errorf(
			"expected_model_revision must not exceed %d bytes",
			maximumModelRevisionBytes,
		)
	case !strings.HasPrefix(suite.ExpectedModelRevision, suite.Model+"@"):
		return errors.New("expected_model_revision must begin with model followed by @")
	case suite.ReasoningEffort == "":
		return errors.New("reasoning_effort is required")
	case len(suite.ReasoningEffort) > maximumReasoningEffortBytes:
		return fmt.Errorf(
			"reasoning_effort must not exceed %d bytes",
			maximumReasoningEffortBytes,
		)
	case suite.PermissionProfile != "read-only":
		return errors.New("permission_profile must be read-only")
	case len(suite.DeveloperInstructions) > maximumInstructionsBytes:
		return fmt.Errorf(
			"developer_instructions must not exceed %d bytes",
			maximumInstructionsBytes,
		)
	case strings.ContainsRune(suite.DeveloperInstructions, '\x00'):
		return errors.New("developer_instructions must not contain NUL")
	case developerNeutralityErr != nil:
		return developerNeutralityErr
	case suite.SourceRoot == "":
		return errors.New("source_root is required")
	case len(suite.SourceRoot) > maximumSuitePathBytes:
		return fmt.Errorf("source_root must not exceed %d bytes", maximumSuitePathBytes)
	case !validGitObjectID(suite.SourceRevision):
		return errors.New("source_revision must be a full lowercase Git object id")
	case !validGitObjectID(suite.SourceBaseRevision):
		return errors.New("source_base_revision must be a full lowercase Git object id")
	case !ValidSHA256(suite.SourceTreeSHA256):
		return errors.New("source_tree_sha256 must be a lowercase SHA-256 digest")
	case suite.TimeoutMillis <= 0:
		return errors.New("timeout_millis must be positive")
	case suite.TimeoutMillis > harness.MaxTimeoutMillis:
		return errors.New("timeout_millis exceeds the representable duration")
	case suite.TimeoutMillis > maximumTimeoutMillis:
		return fmt.Errorf("timeout_millis must not exceed %d", maximumTimeoutMillis)
	case suite.Repetitions <= 0:
		return errors.New("repetitions must be positive")
	case suite.Repetitions > maxRepetitions:
		return fmt.Errorf("repetitions must not exceed %d", maxRepetitions)
	}
	return nil
}

func validModelRevision(value string) bool {
	if value == "" || strings.TrimSpace(value) != value ||
		strings.Count(value, "@") != 1 {
		return false
	}
	model, revision, _ := strings.Cut(value, "@")
	if !isPinnedModel(model) || len(revision) < 4 ||
		!validIdentifier(revision, false) {
		return false
	}
	hasDigit := false
	for _, character := range revision {
		if character >= '0' && character <= '9' {
			hasDigit = true
			break
		}
	}
	lower := strings.ToLower(revision)
	return hasDigit && lower != "default" &&
		!strings.Contains(lower, "latest") &&
		!strings.Contains(lower, "router") &&
		!strings.Contains(lower, "current") &&
		!strings.Contains(lower, "stable") &&
		!strings.Contains(lower, "production")
}

func isPinnedModel(model string) bool {
	if strings.TrimSpace(model) != model || !validIdentifier(model, true) {
		return false
	}
	normalized := strings.ToLower(model)
	return normalized != "auto" &&
		normalized != "default" && normalized != "router" &&
		normalized != "router-selected" &&
		!strings.Contains(normalized, "router") &&
		!strings.Contains(normalized, "latest") &&
		!strings.Contains(normalized, "current") &&
		!strings.Contains(normalized, "stable") &&
		!strings.Contains(normalized, "production") &&
		!strings.ContainsRune(normalized, '*')
}

func validIdentifier(value string, allowSlash bool) bool {
	if value == "" {
		return false
	}
	for index, character := range value {
		alphanumeric := character >= 'a' && character <= 'z' ||
			character >= 'A' && character <= 'Z' ||
			character >= '0' && character <= '9'
		if index == 0 && !alphanumeric {
			return false
		}
		if alphanumeric {
			continue
		}
		if strings.ContainsRune("._:+-", character) ||
			allowSlash && character == '/' {
			continue
		}
		return false
	}
	return true
}

func validGitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
}

func resolveRelative(base, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Clean(filepath.Join(base, path))
}

func readStableRegularFileLimited(
	path string,
	maximumBytes int64,
) (content []byte, resultErr error) {
	if maximumBytes < 0 {
		return nil, errors.New("regular file byte limit must be nonnegative")
	}
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() {
		return nil, fmt.Errorf("%s is not a regular file", path)
	}
	if before.Size() < 0 || before.Size() > maximumBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", path, maximumBytes)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil {
			resultErr = errors.Join(
				resultErr,
				fmt.Errorf("close %s: %w", path, closeErr),
			)
			content = nil
		}
	}()
	opened, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		return nil, fmt.Errorf("%s changed before it was opened", path)
	}
	content, err = io.ReadAll(io.LimitReader(file, maximumBytes+1))
	if err != nil {
		return nil, err
	}
	if int64(len(content)) > maximumBytes {
		return nil, fmt.Errorf("%s exceeds %d bytes", path, maximumBytes)
	}
	openedAfter, err := file.Stat()
	if err != nil {
		return nil, err
	}
	after, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !os.SameFile(before, openedAfter) || !os.SameFile(before, after) ||
		before.Size() != openedAfter.Size() || before.Size() != after.Size() ||
		before.Mode() != openedAfter.Mode() || before.Mode() != after.Mode() ||
		!before.ModTime().Equal(openedAfter.ModTime()) ||
		!before.ModTime().Equal(after.ModTime()) {
		return nil, fmt.Errorf("%s changed while it was read", path)
	}
	return content, nil
}

func requireJSONEOF(decoder *json.Decoder) error {
	var trailing any
	err := decoder.Decode(&trailing)
	if errors.Is(err, io.EOF) {
		return nil
	}
	if err == nil {
		return errors.New("multiple JSON documents are not allowed")
	}
	return err
}

func rejectDuplicateObjectKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("object key is not a string")
				}
				if _, exists := seen[key]; exists {
					return fmt.Errorf("duplicate object key %q", key)
				}
				seen[key] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
			_, err := decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err := decoder.Token()
			return err
		default:
			return fmt.Errorf("unexpected delimiter %q", delimiter)
		}
	}
	return walk()
}
