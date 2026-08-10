// Package harness defines the harness-neutral boundary used by tokenbench.
//
// Adapters translate a resolved, parity-checked invocation into a process and
// decode the resulting raw bytes. They do not schedule pairs, execute the
// process, judge answers, or write benchmark evidence.
package harness

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// Adapter resolves harness identity, renders a process invocation, and decodes
// raw execution output. Implementations must be deterministic for equal input.
type Adapter interface {
	Kind() string
	Resolve(context.Context, ResolveRequest) (Identity, error)
	MCPArguments(context.Context, MCPServer) ([]string, error)
	Build(context.Context, Invocation) (ProcessSpec, error)
	Decode(context.Context, RawExecution) (Observation, error)
}

// ResolveRequest contains only common harness settings. There is no arm in the
// request, so an adapter cannot resolve different model settings per arm.
type ResolveRequest struct {
	Environment            map[string]string `json:"environment"`
	Executable             string            `json:"executable"`
	ExecutableSHA256       string            `json:"executable_sha256"`
	Model                  string            `json:"model"`
	ExpectedModelRevision  string            `json:"expected_model_revision"`
	ReasoningEffort        string            `json:"reasoning_effort"`
	PermissionProfile      string            `json:"permission_profile"`
	DeveloperInstructions  string            `json:"developer_instructions"`
	WorkingDirectory       string            `json:"working_directory"`
	SourceRevision         string            `json:"source_revision"`
	SourceBaseRevision     string            `json:"source_base_revision"`
	SourceTreeSHA256       string            `json:"source_tree_sha256"`
	GitExecutable          string            `json:"git_executable"`
	GitExecutableSHA256    string            `json:"git_executable_sha256"`
	GitMetadataSHA256      string            `json:"git_metadata_sha256"`
	RunnerExecutable       string            `json:"runner_executable"`
	RunnerExecutableSHA256 string            `json:"runner_executable_sha256"`
	TimeoutMillis          int64             `json:"timeout_millis"`
}

// Identity pins the implementation that rendered and decoded a run.
type Identity struct {
	AdapterExecutableSHA256    string `json:"adapter_executable_sha256"`
	AdapterControlConfigSHA256 string `json:"adapter_control_config_sha256"`
	AdapterConfigSHA256        string `json:"adapter_config_sha256"`
	Kind                       string `json:"kind"`
	AdapterVersion             string `json:"adapter_version"`
	ExecutableSHA256           string `json:"executable_sha256"`
	ExecutableVersion          string `json:"executable_version"`
	Model                      string `json:"model"`
	ModelRevision              string `json:"model_revision"`
	ReasoningEffort            string `json:"reasoning_effort"`
	DecoderSchema              string `json:"decoder_schema"`
}

// Invocation is the complete semantic input to a harness. Baseline and
// candidate invocations must be equal except for MCPServers.
type Invocation struct {
	Environment            map[string]string `json:"environment"`
	HarnessIdentity        Identity          `json:"harness_identity"`
	DeveloperInstructions  string            `json:"developer_instructions"`
	PermissionProfile      string            `json:"permission_profile"`
	SourceTreeSHA256       string            `json:"source_tree_sha256"`
	GitExecutable          string            `json:"git_executable"`
	GitExecutableSHA256    string            `json:"git_executable_sha256"`
	GitMetadataSHA256      string            `json:"git_metadata_sha256"`
	RunnerExecutable       string            `json:"runner_executable"`
	RunnerExecutableSHA256 string            `json:"runner_executable_sha256"`
	Executable             string            `json:"executable"`
	ExecutableSHA256       string            `json:"executable_sha256"`
	Model                  string            `json:"model"`
	RequestedModel         string            `json:"requested_model"`
	ModelRevision          string            `json:"model_revision"`
	ReasoningEffort        string            `json:"reasoning_effort"`
	SourceBaseRevision     string            `json:"source_base_revision"`
	SourceRevision         string            `json:"source_revision"`
	WorkingDirectory       string            `json:"working_directory"`
	Arguments              []string          `json:"arguments"`
	MCPServers             []MCPServer       `json:"mcp_servers"`
	Prompt                 []byte            `json:"prompt"`
	TimeoutMillis          int64             `json:"timeout_millis"`
}

// MCPServer is one semantic stdio MCP registration. Environment values are
// private to the server process and never enter the agent process environment.
type MCPServer struct {
	Environment      map[string]string `json:"environment"`
	Name             string            `json:"name"`
	Command          string            `json:"command"`
	ExecutableSHA256 string            `json:"executable_sha256"`
	Arguments        []string          `json:"arguments"`
	Required         bool              `json:"required"`
	ReadOnly         bool              `json:"read_only"`
}

// ProcessSpec is an argv-based target process. Env is complete: executors must
// not inherit ambient variables.
type ProcessSpec struct {
	Environment   map[string]string `json:"environment"`
	Directory     string            `json:"directory"`
	Argv          []string          `json:"argv"`
	Stdin         []byte            `json:"stdin"`
	TimeoutMillis int64             `json:"timeout_millis"`
}

// RawExecution is the exact output made available to a decoder.
type RawExecution struct {
	Stdout   []byte `json:"stdout"`
	Stderr   []byte `json:"stderr"`
	ExitCode int    `json:"exit_code"`
	TimedOut bool   `json:"timed_out"`
}

// Observation is the harness-neutral normalized result.
type Observation struct {
	FinalAnswer string   `json:"final_answer"`
	Model       string   `json:"model"`
	ToolCalls   []string `json:"tool_calls"`
	Usage       Usage    `json:"usage"`
	Completed   bool     `json:"completed"`
}

// Usage preserves provider-reported categories. InputTokens includes cached
// input when that is how the provider reports it; no pricing weights are used.
type Usage struct {
	InputTokens       int64 `json:"input_tokens"`
	CachedInputTokens int64 `json:"cached_input_tokens"`
	OutputTokens      int64 `json:"output_tokens"`
	ReasoningTokens   int64 `json:"reasoning_tokens"`
}

// ValidateIdentity checks the fields required to stratify benchmark results.
func ValidateIdentity(identity Identity) error {
	switch {
	case identity.Kind == "":
		return errors.New("harness identity kind is required")
	case identity.AdapterVersion == "":
		return errors.New("harness adapter version is required")
	case !validSHA256(identity.AdapterExecutableSHA256):
		return errors.New("harness adapter executable digest is invalid")
	case !validSHA256(identity.AdapterControlConfigSHA256):
		return errors.New("harness adapter control configuration digest is invalid")
	case !validSHA256(identity.AdapterConfigSHA256):
		return errors.New("harness adapter configuration digest is invalid")
	case !validSHA256(identity.ExecutableSHA256):
		return errors.New("harness executable digest is invalid")
	case identity.ExecutableVersion == "":
		return errors.New("harness executable version is required")
	case identity.Model == "":
		return errors.New("resolved model identity is required")
	case identity.ModelRevision == "":
		return errors.New("resolved immutable model revision is required")
	case identity.ReasoningEffort == "":
		return errors.New("resolved reasoning effort is required")
	case identity.DecoderSchema == "":
		return errors.New("decoder schema is required")
	default:
		return nil
	}
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && value == hex.EncodeToString(decoded)
}

// ValidateUsage rejects arithmetic inputs that cannot be provider usage.
func ValidateUsage(usage Usage) error {
	if usage.InputTokens < 0 || usage.CachedInputTokens < 0 ||
		usage.OutputTokens < 0 || usage.ReasoningTokens < 0 {
		return errors.New("usage counters must be nonnegative")
	}
	if usage.CachedInputTokens > usage.InputTokens {
		return fmt.Errorf(
			"cached input tokens %d exceed input tokens %d",
			usage.CachedInputTokens,
			usage.InputTokens,
		)
	}
	return nil
}

// ValidateProcessSpec checks the executor contract shared by every adapter.
func ValidateProcessSpec(process ProcessSpec) error {
	switch {
	case len(process.Argv) == 0:
		return errors.New("process argv must not be empty")
	case !validText(process.Directory):
		return errors.New("process directory contains invalid text")
	case !filepath.IsAbs(process.Argv[0]):
		return errors.New("process argv[0] must be an absolute path")
	case !filepath.IsAbs(process.Directory):
		return errors.New("process directory must be an absolute path")
	case process.TimeoutMillis <= 0:
		return errors.New("process timeout must be positive")
	}
	for index, argument := range process.Argv {
		if !validText(argument) {
			return fmt.Errorf("process argument %d contains invalid text", index)
		}
	}
	for key, value := range process.Environment {
		if key == "" || strings.ContainsRune(key, '=') || !validText(key) {
			return fmt.Errorf("invalid process environment key %q", key)
		}
		if !validText(value) {
			return fmt.Errorf(
				"process environment value for %q contains invalid text",
				key,
			)
		}
	}
	return nil
}

func validText(value string) bool {
	return utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}
