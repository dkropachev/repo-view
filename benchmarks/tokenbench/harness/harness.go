// Package harness defines the harness-neutral boundary used by tokenbench.
//
// Adapters translate a resolved, parity-checked invocation into a process and
// decode the resulting raw bytes. They do not schedule pairs, execute the
// process, judge answers, or write benchmark evidence.
package harness

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// OfflineLocalProxyCapability is an inert marker used only by offline audit
// plans. Live runs commit the exact fresh runner-issued local capability in
// both arms; publication is gated until the listener is closed, so that value
// is an expired local capability by the time it enters signed evidence. It is
// never an upstream provider credential.
const OfflineLocalProxyCapability = "tokenbench-local-proxy/v1/offline-audit-only"

const localProxyCapabilityPrefix = "tokenbench-local-proxy/v1/"

// ValidLocalProxyCapability accepts either the explicit offline marker or one
// canonical 256-bit per-lifecycle capability. This is syntax validation only;
// publication authority separately proves the owning listener was closed.
func ValidLocalProxyCapability(value string) bool {
	if value == OfflineLocalProxyCapability {
		return true
	}
	if !strings.HasPrefix(value, localProxyCapabilityPrefix) {
		return false
	}
	encoded := strings.TrimPrefix(value, localProxyCapabilityPrefix)
	decoded, err := base64.RawURLEncoding.DecodeString(encoded)
	return err == nil && len(decoded) == sha256.Size &&
		base64.RawURLEncoding.EncodeToString(decoded) == encoded
}

const (
	// MaxRawStreamBytes is the schema-level bound for each captured stdout or
	// stderr stream, independent of runner configuration.
	MaxRawStreamBytes = 16 << 20
	// MaxArtifactCount and MaxArtifactBytes bound sanitized runner artifacts.
	MaxArtifactCount = 32
	MaxArtifactBytes = 64 << 20
	// MaxObservationToolCalls bounds normalized provider-issued tool calls.
	MaxObservationToolCalls = 100000
)

// MaxTimeoutMillis is the largest millisecond timeout that can be converted to
// time.Duration without overflow.
const MaxTimeoutMillis = int64((1<<63 - 1) / time.Millisecond)

// Adapter resolves harness identity, renders a process invocation, and decodes
// raw execution output. Implementations must be deterministic for equal input.
type Adapter interface {
	Kind() string
	Resolve(context.Context, ResolveRequest) (Identity, error)
	MCPArguments(context.Context, MCPServer) ([]string, error)
	Build(context.Context, Invocation) (ProcessSpec, error)
	Decode(context.Context, RawExecution) (Observation, error)
}

// CommonEnvironmentAdapter is an optional adapter capability for deriving the
// complete, non-inherited target-process environment from common inputs. The
// suite format cannot author this environment. Tokenbench calls the method once
// while preparing a suite and again immediately before process construction;
// the two results must be deeply equal. Adapters that do not implement this
// interface receive a canonical empty environment.
//
// Returned values must be safe to publish in benchmark plans and evidence.
// Live credentials belong in a runner-owned local proxy, never in this map.
type CommonEnvironmentAdapter interface {
	CommonEnvironment(context.Context, ResolveRequest) (map[string]string, error)
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

// Artifact is one sanitized runner-produced input made available to a decoder.
// Typical examples are a provider-wire trace or an exported effective-config
// lock. Artifacts must never contain credentials. Names are adapter contracts;
// media types make their bytes independently inspectable.
type Artifact struct {
	Name      string `json:"name"`
	MediaType string `json:"media_type"`
	Data      []byte `json:"data"`
}

// RawExecution is the exact output and sanitized runner evidence made
// available to a decoder. Limit flags are distinct from timeout/cancellation so
// failed attempts remain classifiable instead of looking like model output.
type RawExecution struct { //nolint:govet,nolintlint // Field order is the stable capture wire order.
	Stdout          []byte           `json:"stdout"`
	Stderr          []byte           `json:"stderr"`
	Artifacts       []Artifact       `json:"artifacts"`
	Resources       *ResourceOutcome `json:"resources"`
	ExitCode        int              `json:"exit_code"`
	LaunchFailed    bool             `json:"launch_failed"`
	TimedOut        bool             `json:"timed_out"`
	Cancelled       bool             `json:"cancelled"`
	StdoutTruncated bool             `json:"stdout_truncated"`
	StderrTruncated bool             `json:"stderr_truncated"`
}

// ResourceOutcome is the canonical cgroup-v2 accounting captured after an
// arm's complete process subtree is empty and before its cgroup is deleted.
// A nil pointer means the noncontained extension runner did not provide
// kernel accounting; conformant contained execution always supplies one.
type ResourceOutcome struct {
	Version            string            `json:"version"`
	CPUStat            []ResourceCounter `json:"cpu_stat"`
	MemoryEvents       []ResourceCounter `json:"memory_events"`
	MemoryEventsLocal  []ResourceCounter `json:"memory_events_local"`
	PIDsEvents         []ResourceCounter `json:"pids_events"`
	MemoryCurrentBytes uint64            `json:"memory_current_bytes"`
	MemoryPeakBytes    uint64            `json:"memory_peak_bytes"`
	PIDsCurrent        uint64            `json:"pids_current"`
	PIDsPeak           uint64            `json:"pids_peak"`
}

// ResourceCounter is one sorted, unique, unsigned kernel counter.
type ResourceCounter struct {
	Name  string `json:"name"`
	Value uint64 `json:"value"`
}

const ResourceOutcomeVersion = "tokenbench.cgroup-v2-resources/v1"

// ValidateResourceOutcome enforces the publication-neutral canonical shape.
// The runner additionally compares the exact counter key sets with the sets
// committed by its cgroup policy identity.
func ValidateResourceOutcome(outcome *ResourceOutcome) error {
	if outcome == nil {
		return errors.New("resource outcome is required")
	}
	if outcome.Version != ResourceOutcomeVersion {
		return fmt.Errorf("unexpected resource outcome version %q", outcome.Version)
	}
	for _, counters := range []struct {
		name     string
		values   []ResourceCounter
		required []string
	}{
		{"cpu.stat", outcome.CPUStat, []string{
			"nr_periods", "nr_throttled", "system_usec", "throttled_usec", "usage_usec", "user_usec",
		}},
		{"memory.events", outcome.MemoryEvents, []string{"high", "low", "max", "oom", "oom_kill"}},
		{"memory.events.local", outcome.MemoryEventsLocal, []string{"high", "low", "max", "oom", "oom_kill"}},
		{"pids.events", outcome.PIDsEvents, []string{"max"}},
	} {
		if err := validateResourceCounters(counters.name, counters.values, counters.required); err != nil {
			return err
		}
	}
	if !sameResourceCounterNames(outcome.MemoryEvents, outcome.MemoryEventsLocal) {
		return errors.New("memory.events and memory.events.local counter keys differ")
	}
	if outcome.MemoryPeakBytes < outcome.MemoryCurrentBytes {
		return errors.New("memory peak is below memory current")
	}
	if outcome.PIDsPeak < outcome.PIDsCurrent {
		return errors.New("pids peak is below pids current")
	}
	if outcome.PIDsCurrent != 0 {
		return errors.New("resource outcome was captured before the process subtree became empty")
	}
	return nil
}

func validateResourceCounters(name string, counters []ResourceCounter, required []string) error {
	if len(counters) == 0 || len(counters) > 64 {
		return fmt.Errorf("%s counter set is empty or oversized", name)
	}
	for index, counter := range counters {
		if len(counter.Name) == 0 || len(counter.Name) > 128 {
			return fmt.Errorf("%s contains an invalid counter name", name)
		}
		for _, character := range counter.Name {
			if character != '.' && character != '_' &&
				(character < 'a' || character > 'z') &&
				(character < '0' || character > '9') {
				return fmt.Errorf("%s contains an invalid counter name", name)
			}
		}
		if index != 0 && counters[index-1].Name >= counter.Name {
			return fmt.Errorf("%s counters are not strictly sorted and unique", name)
		}
	}
	for _, requiredName := range required {
		if _, ok := ResourceCounterValue(counters, requiredName); !ok {
			return fmt.Errorf("%s omitted required counter %s", name, requiredName)
		}
	}
	return nil
}

func sameResourceCounterNames(left, right []ResourceCounter) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index].Name != right[index].Name {
			return false
		}
	}
	return true
}

// ResourceCounterValue returns one value from a validated sorted counter set.
func ResourceCounterValue(counters []ResourceCounter, name string) (uint64, bool) {
	index := sort.Search(len(counters), func(index int) bool {
		return counters[index].Name >= name
	})
	if index == len(counters) || counters[index].Name != name {
		return 0, false
	}
	return counters[index].Value, true
}

// CloneResourceOutcome returns a defensive deep copy.
func CloneResourceOutcome(source *ResourceOutcome) *ResourceOutcome {
	if source == nil {
		return nil
	}
	clone := *source
	clone.CPUStat = append([]ResourceCounter(nil), source.CPUStat...)
	clone.MemoryEvents = append([]ResourceCounter(nil), source.MemoryEvents...)
	clone.MemoryEventsLocal = append([]ResourceCounter(nil), source.MemoryEventsLocal...)
	clone.PIDsEvents = append([]ResourceCounter(nil), source.PIDsEvents...)
	return &clone
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
	// ProviderTotalTokens is nil when the provider omitted its native total.
	// A nonnil zero therefore remains distinct from an omitted total.
	ProviderTotalTokens   *int64 `json:"provider_total_tokens,omitempty"`
	InputTokens           int64  `json:"input_tokens"`
	CachedInputTokens     int64  `json:"cached_input_tokens"`
	CacheWriteInputTokens int64  `json:"cache_write_input_tokens"`
	OutputTokens          int64  `json:"output_tokens"`
	ReasoningTokens       int64  `json:"reasoning_tokens"`
}

// CloneUsage returns a defensive copy, including optional native counters.
func CloneUsage(source Usage) Usage {
	clone := source
	if source.ProviderTotalTokens != nil {
		providerTotal := *source.ProviderTotalTokens
		clone.ProviderTotalTokens = &providerTotal
	}
	return clone
}

// CloneObservation returns a defensive copy of normalized observation data.
func CloneObservation(source Observation) Observation {
	clone := source
	if source.ToolCalls != nil {
		clone.ToolCalls = append(
			make([]string, 0, len(source.ToolCalls)),
			source.ToolCalls...,
		)
	}
	clone.Usage = CloneUsage(source.Usage)
	return clone
}

// EqualUsageNativeComponents compares the independently reported components
// shared by provider evidence and adapters whose JSONL omits a provider total.
func EqualUsageNativeComponents(left, right Usage) bool {
	return left.InputTokens == right.InputTokens &&
		left.CachedInputTokens == right.CachedInputTokens &&
		left.CacheWriteInputTokens == right.CacheWriteInputTokens &&
		left.OutputTokens == right.OutputTokens &&
		left.ReasoningTokens == right.ReasoningTokens
}

// ValidateIdentity checks the fields required to stratify benchmark results.
func ValidateIdentity(identity Identity) error {
	switch {
	case !validBoundedText(identity.Kind, 128):
		return errors.New("harness identity kind is required")
	case !validBoundedText(identity.AdapterVersion, 256):
		return errors.New("harness adapter version is required")
	case !validSHA256(identity.AdapterExecutableSHA256):
		return errors.New("harness adapter executable digest is invalid")
	case !validSHA256(identity.AdapterControlConfigSHA256):
		return errors.New("harness adapter control configuration digest is invalid")
	case !validSHA256(identity.AdapterConfigSHA256):
		return errors.New("harness adapter configuration digest is invalid")
	case !validSHA256(identity.ExecutableSHA256):
		return errors.New("harness executable digest is invalid")
	case !validBoundedText(identity.ExecutableVersion, 256):
		return errors.New("harness executable version is required")
	case !validBoundedText(identity.Model, 512):
		return errors.New("resolved model identity is required")
	case !validBoundedText(identity.ModelRevision, 512):
		return errors.New("resolved immutable model revision is required")
	case !validBoundedText(identity.ReasoningEffort, 128):
		return errors.New("resolved reasoning effort is required")
	case !validBoundedText(identity.DecoderSchema, 256):
		return errors.New("decoder schema is required")
	default:
		return nil
	}
}

func validBoundedText(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && validText(value) &&
		strings.TrimSpace(value) == value
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
		usage.CacheWriteInputTokens < 0 ||
		usage.OutputTokens < 0 || usage.ReasoningTokens < 0 ||
		(usage.ProviderTotalTokens != nil && *usage.ProviderTotalTokens < 0) {
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
	case process.TimeoutMillis > MaxTimeoutMillis:
		return errors.New("process timeout is invalid: exceeds the representable duration")
	}
	for index, argument := range process.Argv {
		if !validText(argument) {
			return fmt.Errorf("process argument %d contains invalid text", index)
		}
	}
	if process.Environment == nil {
		return errors.New("process environment must be a canonical object")
	}
	if err := ValidateEnvironment(process.Environment); err != nil {
		return err
	}
	return nil
}

// ValidateEnvironment validates a complete, non-inherited process
// environment. It does not permit secrets merely because their syntax is
// valid; the adapter contract additionally requires publishable values.
func ValidateEnvironment(environment map[string]string) error {
	if environment == nil {
		return errors.New("process environment must be a canonical object")
	}
	for key, value := range environment {
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

// ValidatePublishableEnvironment applies the closed semantic policy used by
// invocation plans. Unknown variables are rejected: syntax and a blacklist
// cannot prove that a value is secret-free or unable to inject runtime code.
func ValidatePublishableEnvironment(environment map[string]string) error {
	if err := ValidateEnvironment(environment); err != nil {
		return err
	}
	for key, value := range environment {
		switch key {
		case "LANG", "LC_ALL":
			if value != "C" && value != "C.UTF-8" {
				return fmt.Errorf("%s must be C or C.UTF-8", key)
			}
		case "TZ":
			if value != "UTC" {
				return errors.New("tz must be UTC")
			}
		case "USER", "LOGNAME":
			if value != "tokenbench" {
				return fmt.Errorf("%s must be tokenbench", key)
			}
		case "HOME", "CODEX_HOME", "CODEX_SQLITE_HOME", "TMPDIR":
			if !canonicalAbsolutePath(value) {
				return fmt.Errorf("%s must be an absolute canonical path", key)
			}
		case "PATH":
			if err := validatePathList(value); err != nil {
				return err
			}
		case "SHELL", "GIT_CONFIG_GLOBAL":
			if !canonicalAbsolutePath(value) {
				return fmt.Errorf("%s must be an absolute canonical path", key)
			}
		case "GIT_CONFIG_NOSYSTEM":
			if value != "1" {
				return errors.New("git_config_nosystem must be 1")
			}
		case "CODEX_API_KEY", "OPENAI_API_KEY", "ANTHROPIC_API_KEY", "GEMINI_API_KEY":
			if !ValidLocalProxyCapability(value) {
				return fmt.Errorf("%s must contain only a canonical local proxy capability", key)
			}
		default:
			return fmt.Errorf("process environment key %q is not in the closed publishable allowlist", key)
		}
	}
	return nil
}

func canonicalAbsolutePath(value string) bool {
	return filepath.IsAbs(value) && filepath.Clean(value) == value
}

func validatePathList(value string) error {
	if value == "" {
		return errors.New("path must not be empty")
	}
	for _, entry := range filepath.SplitList(value) {
		if entry == "" || !canonicalAbsolutePath(entry) {
			return fmt.Errorf("path entry %q is not an absolute canonical path", entry)
		}
	}
	if strings.Contains(value, string(os.PathListSeparator)+string(os.PathListSeparator)) {
		return errors.New("path contains an empty entry")
	}
	return nil
}

// ValidateArtifacts checks the generic artifact envelope. Adapters remain
// responsible for validating the schema and content of artifacts they use.
func ValidateArtifacts(artifacts []Artifact) error {
	if artifacts == nil {
		return errors.New("artifacts must be a canonical array")
	}
	if len(artifacts) > MaxArtifactCount {
		return errors.New("artifact count exceeds 32")
	}
	seen := make(map[string]struct{}, len(artifacts))
	total := 0
	for index, artifact := range artifacts {
		if !validText(artifact.Name) || artifact.Name == "" || len(artifact.Name) > 255 {
			return fmt.Errorf("artifact %d has an invalid name", index)
		}
		if !validText(artifact.MediaType) || artifact.MediaType == "" ||
			len(artifact.MediaType) > 255 {
			return fmt.Errorf("artifact %q has an invalid media type", artifact.Name)
		}
		if _, exists := seen[artifact.Name]; exists {
			return fmt.Errorf("duplicate artifact name %q", artifact.Name)
		}
		seen[artifact.Name] = struct{}{}
		if len(artifact.Data) > MaxArtifactBytes-total {
			return errors.New("artifact bytes exceed 64 MiB")
		}
		total += len(artifact.Data)
	}
	return nil
}

func validText(value string) bool {
	return utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}
