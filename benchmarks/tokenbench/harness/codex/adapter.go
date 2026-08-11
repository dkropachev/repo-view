// Package codex implements the built-in, offline-verifiable tokenbench adapter
// for the Codex CLI v0.144.0 exec JSONL protocol.
package codex

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/scopesifter/scopesifter/benchmarks/tokenbench/harness"
	"github.com/scopesifter/scopesifter/benchmarks/tokenbench/internal/selfexec"
)

const (
	adapterKind       = "codex"
	adapterVersion    = "tokenbench.codex-adapter/codex-cli-v0.144.0/v4"
	executableVersion = "codex-cli 0.144.0"
	decoderSchema     = "codex.exec-jsonl/v0.144.0+responses-trace/v4+observation/v2"
	configSchema      = "tokenbench.codex-config/v0.144.0/v3"
	layoutSchema      = "tokenbench.codex-runtime-layout/v4"
	productionAdapter = "tokenbench.codex-production-adapter/v2"

	// OfflineLocalProxyCapability is the inert capability marker used for
	// offline audit plans. Live adapters receive and commit the exact fresh
	// lifecycle capability; signing is gated until that capability is expired.
	OfflineLocalProxyCapability = harness.OfflineLocalProxyCapability
)

// Snapshot is one exact requested-model/provider-model resolution supported by
// this version-pinned adapter. ModelRevision is the suite-facing immutable
// identity and ProviderModel is the value that must appear on provider wire.
type Snapshot struct {
	RequestedModel string `json:"requested_model"`
	ModelRevision  string `json:"model_revision"`
	ProviderModel  string `json:"provider_model"`
}

var snapshotAllowlist = []Snapshot{
	{
		RequestedModel: "gpt-5.4",
		ModelRevision:  "gpt-5.4@gpt-5.4-2026-03-05",
		ProviderModel:  "gpt-5.4-2026-03-05",
	},
}

// executableSHA256Allowlist binds the static version claim to exact release
// bytes. New platform/build artifacts require an audited adapter update.
var executableSHA256Allowlist = []string{
	"08b012d75651efb22b5162be253cd4d28752594082671098e123229b896ba77e",
}

// These are all v0.144.0 feature gates whose state can change model-visible
// instructions, tools, execution behavior, or provider requests. shell_tool
// and unified_exec are the only enabled gates and are listed separately.
var disabledFeatures = []string{
	"apply_patch_freeform",
	"apply_patch_streaming_events",
	"apps",
	"apps_mcp_path_override",
	"artifact",
	"auth_elicitation",
	"browser_use",
	"browser_use_external",
	"browser_use_full_cdp_access",
	"chronicle",
	"code_mode",
	"code_mode_host",
	"code_mode_only",
	"codex_git_commit",
	"codex_hooks",
	"collab",
	"collaboration_modes",
	"computer_use",
	"concurrent_reasoning_summaries",
	"connectors",
	"current_time_reminder",
	"default_mode_request_user_input",
	"deferred_executor",
	"elevated_windows_sandbox",
	"enable_experimental_windows_sandbox",
	"enable_fanout",
	"enable_mcp_apps",
	"enable_request_compression",
	"exec_permission_approvals",
	"experimental_windows_sandbox",
	"external_migration",
	"fast_mode",
	"goals",
	"guardian_approval",
	"hooks",
	"image_detail_original",
	"image_generation",
	"imagegenext",
	"in_app_browser",
	"item_ids",
	"js_repl",
	"js_repl_tools_only",
	"local_thread_store_compression",
	"memories",
	"memory_tool",
	"mentions_v2",
	"multi_agent",
	"multi_agent_mode",
	"multi_agent_v2",
	"network_proxy",
	"non_prefixed_mcp_tool_names",
	"personality",
	"plugin_hooks",
	"plugin_sharing",
	"plugins",
	"prevent_idle_sleep",
	"realtime_conversation",
	"remote_compaction_v2",
	"remote_control",
	"remote_models",
	"remote_plugin",
	"request_permissions",
	"request_permissions_tool",
	"request_rule",
	"resize_all_images",
	"respect_system_proxy",
	"responses_websockets",
	"responses_websockets_v2",
	"rollout_budget",
	"runtime_metrics",
	"search_tool",
	"secret_auth_storage",
	"shell_snapshot",
	"shell_zsh_fork",
	"skill_env_var_dependency_prompt",
	"skill_mcp_dependency_install",
	"sqlite",
	"standalone_web_search",
	"steer",
	"telepathy",
	"terminal_resize_reflow",
	"terminal_visualization_instructions",
	"token_budget",
	"tool_call_mcp_elicitation",
	"tool_search",
	"tool_search_always_defer_mcp_tools",
	"tool_suggest",
	"tui_app_server",
	"unavailable_dummy_tools",
	"undo",
	"unified_exec_zsh_fork",
	"use_agent_identity",
	"use_legacy_landlock",
	"use_linux_sandbox_bwrap",
	"web_search",
	"web_search_cached",
	"web_search_request",
	"workspace_dependencies",
	"workspace_owner_usage_nudge",
}

var enabledFeatures = []string{"shell_tool", "unified_exec"}

var allowedReasoningEfforts = map[string]map[string]struct{}{
	"gpt-5.4": reasoningEfforts("low", "medium", "high", "xhigh"),
}

func reasoningEfforts(values ...string) map[string]struct{} {
	result := make(map[string]struct{}, len(values))
	for _, value := range values {
		result[value] = struct{}{}
	}
	return result
}

// RuntimeLayout mirrors the runner-owned immutable layout without introducing
// a package cycle. ToolboxRoot is the sole PATH directory and is disjoint from
// every writable runtime directory. All paths and the loopback proxy are
// public, committed process inputs. LocalProxyCapability is identical in both
// arms and never an upstream credential. For live evidence it is expired before
// signing.
type RuntimeLayout struct {
	ProxyURL             string `json:"proxy_url"`
	Home                 string `json:"home"`
	CodexHome            string `json:"codex_home"`
	Temp                 string `json:"temp"`
	ConfigLock           string `json:"config_lock"`
	ToolboxRoot          string `json:"toolbox_root"`
	LocalProxyCapability string `json:"local_proxy_capability"`
}

// Validate checks the exact v0.144.0 runner-layout contract.
func (layout RuntimeLayout) Validate() error {
	return validateRuntimeLayout(layout)
}

// Environment returns the exact complete environment committed into both
// arm ProcessSpecs.
func (layout RuntimeLayout) Environment() map[string]string {
	return runtimeEnvironment(layout)
}

// ConfigAssignments returns the exact common Codex -c assignments owned by
// the runtime layout. Callers interleave each with a preceding "-c".
func (layout RuntimeLayout) ConfigAssignments() []string {
	proxyURL, _ := tomlString(layout.ProxyURL)
	configLock, _ := tomlString(layout.ConfigLock)
	toolboxRoot, _ := tomlString(layout.ToolboxRoot)
	return []string{
		"openai_base_url=" + proxyURL,
		"debug.config_lockfile.export_dir=" + configLock,
		"debug.config_lockfile.save_fields_resolved_from_model_catalog=true",
		"shell_environment_policy.set={PATH=" + toolboxRoot + "}",
	}
}

// Commitment returns the same deterministic public layout commitment used by
// the runner package, without importing it and creating a package cycle.
func (layout RuntimeLayout) Commitment() (string, error) {
	if err := layout.Validate(); err != nil {
		return "", err
	}
	canonical := struct {
		Schema string        `json:"schema"`
		Layout RuntimeLayout `json:"layout"`
	}{Schema: layoutSchema, Layout: layout}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("encode Codex runtime layout: %w", err)
	}
	return digest(raw), nil
}

// Adapter is a deterministic built-in harness.Adapter. Its runtime layout is
// constructor-bound and immutable. It never executes the Codex executable
// while resolving, building, or decoding.
type Adapter struct {
	layout        RuntimeLayout
	controlSHA256 string
	production    bool
}

// New constructs a Codex adapter bound to one runner-owned immutable layout.
func New(layout RuntimeLayout) (*Adapter, error) {
	return newAdapter(layout, false)
}

// NewProduction constructs the exact built-in adapter capability eligible for
// live publication. Offline planning, validation, and replay use New; live
// execution must additionally pair this exact concrete type with the
// production lifecycle authority.
func NewProduction(layout RuntimeLayout) (*Adapter, error) {
	return newAdapter(layout, true)
}

func newAdapter(layout RuntimeLayout, production bool) (*Adapter, error) {
	if err := layout.Validate(); err != nil {
		return nil, err
	}
	controlSHA256, err := controlConfigSHA256(layout)
	if err != nil {
		return nil, err
	}
	return &Adapter{
		layout:        layout,
		controlSHA256: controlSHA256,
		production:    production,
	}, nil
}

// ProductionIdentity reports a stable nonsecret marker carried only by an
// exact adapter returned by NewProduction.
func (adapter *Adapter) ProductionIdentity() (string, bool) {
	if adapter == nil || !adapter.production || adapter.validateConfigured() != nil {
		return "", false
	}
	return productionAdapter + "/sha256:" + adapter.controlSHA256, true
}

// BuiltInProductionIdentity rejects wrappers even when they embed an Adapter
// and promote its methods. Pair execution uses this exact concrete-type gate
// before granting live publication authority.
func BuiltInProductionIdentity(adapter harness.Adapter) (string, bool) {
	builtIn, ok := adapter.(*Adapter)
	if !ok {
		return "", false
	}
	return builtIn.ProductionIdentity()
}

func validateRuntimeLayout(layout RuntimeLayout) error {
	if !harness.ValidLocalProxyCapability(layout.LocalProxyCapability) {
		return errors.New("codex runtime layout must use one canonical local proxy capability")
	}
	parsed, err := url.Parse(layout.ProxyURL)
	if err != nil {
		return fmt.Errorf("parse Codex proxy URL: %w", err)
	}
	port, err := strconv.Atoi(parsed.Port())
	if err != nil || port < 1 || port > 65535 ||
		parsed.Scheme != "http" || parsed.Hostname() != "127.0.0.1" ||
		parsed.Host != "127.0.0.1:"+strconv.Itoa(port) || parsed.Path != "/v1" ||
		parsed.RawPath != "" || parsed.RawQuery != "" || parsed.Fragment != "" ||
		parsed.User != nil || parsed.Opaque != "" || parsed.String() != layout.ProxyURL {
		return errors.New("codex proxy URL must be canonical http://127.0.0.1:PORT/v1")
	}
	directories := []struct {
		name  string
		value string
	}{
		{"home", layout.Home},
		{"Codex home", layout.CodexHome},
		{"temporary", layout.Temp},
		{"config-lock", layout.ConfigLock},
		{"PATH toolbox", layout.ToolboxRoot},
	}
	for _, directory := range directories {
		if !validText(directory.value) || !filepath.IsAbs(directory.value) ||
			filepath.Clean(directory.value) != directory.value || isFilesystemRoot(directory.value) {
			return fmt.Errorf("codex runtime %s directory must be absolute, canonical, and non-root", directory.name)
		}
	}
	if strings.ContainsRune(layout.ToolboxRoot, os.PathListSeparator) {
		return errors.New("codex runtime PATH toolbox must be exactly one directory")
	}
	for left := range directories {
		for right := left + 1; right < len(directories); right++ {
			if pathsOverlap(directories[left].value, directories[right].value) {
				return fmt.Errorf(
					"codex runtime %s and %s directories must be disjoint",
					directories[left].name,
					directories[right].name,
				)
			}
		}
	}
	if err := harness.ValidatePublishableEnvironment(runtimeEnvironment(layout)); err != nil {
		return fmt.Errorf("codex runtime environment: %w", err)
	}
	return nil
}

func pathsOverlap(left, right string) bool {
	return pathContains(left, right) || pathContains(right, left)
}

func pathContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	if err != nil {
		return true
	}
	return relative == "." || relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func isFilesystemRoot(path string) bool {
	volume := filepath.VolumeName(path)
	return filepath.Clean(path) == filepath.Join(volume, string(filepath.Separator))
}

func runtimeEnvironment(layout RuntimeLayout) map[string]string {
	return map[string]string{
		"HOME":              layout.Home,
		"CODEX_HOME":        layout.CodexHome,
		"CODEX_SQLITE_HOME": filepath.Join(layout.CodexHome, "sqlite"),
		"TMPDIR":            layout.Temp,
		"PATH":              layout.ToolboxRoot,
		"CODEX_API_KEY":     layout.LocalProxyCapability,
	}
}

func controlConfigSHA256(layout RuntimeLayout) (string, error) {
	manifest := struct {
		ReasoningEfforts        map[string]map[string]struct{} `json:"reasoning_efforts"`
		CommonEnvironment       map[string]string              `json:"common_environment"`
		RuntimeLayout           RuntimeLayout                  `json:"runtime_layout"`
		AdapterVersion          string                         `json:"adapter_version"`
		ExecutableVersion       string                         `json:"executable_version"`
		DecoderSchema           string                         `json:"decoder_schema"`
		ResponsesTraceArtifact  string                         `json:"responses_trace_artifact"`
		EffectiveConfigArtifact string                         `json:"effective_config_artifact"`
		Schema                  string                         `json:"schema"`
		Snapshots               []Snapshot                     `json:"snapshots"`
		ExecutableSHA256        []string                       `json:"executable_sha256_allowlist"`
		MCPTools                []string                       `json:"mcp_tools"`
		EnabledFeatures         []string                       `json:"enabled_features"`
		DisabledFeatures        []string                       `json:"disabled_features"`
	}{
		Schema:                  configSchema,
		AdapterVersion:          adapterVersion,
		ExecutableVersion:       executableVersion,
		DecoderSchema:           decoderSchema,
		ResponsesTraceArtifact:  ResponsesTraceArtifactName,
		EffectiveConfigArtifact: EffectiveConfigArtifactName,
		Snapshots:               snapshotAllowlist,
		DisabledFeatures:        disabledFeatures,
		EnabledFeatures:         enabledFeatures,
		MCPTools:                allowedMCPTools,
		ExecutableSHA256:        executableSHA256Allowlist,
		RuntimeLayout:           layout,
		CommonEnvironment:       runtimeEnvironment(layout),
		ReasoningEfforts:        allowedReasoningEfforts,
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return "", fmt.Errorf("encode Codex control configuration: %w", err)
	}
	return digest(raw), nil
}

func (adapter *Adapter) validateConfigured() error {
	if adapter == nil {
		return errors.New("codex adapter is nil")
	}
	if err := validateRuntimeLayout(adapter.layout); err != nil {
		return fmt.Errorf("codex adapter runtime layout: %w", err)
	}
	expected, err := controlConfigSHA256(adapter.layout)
	if err != nil {
		return err
	}
	if adapter.controlSHA256 != expected {
		return errors.New("codex adapter control configuration is invalid")
	}
	return nil
}

func (adapter *Adapter) commonEnvironment() map[string]string {
	return runtimeEnvironment(adapter.layout)
}

// Kind implements harness.Adapter.
func (*Adapter) Kind() string { return adapterKind }

// RuntimeLayout returns the adapter's immutable runner layout by value.
func (adapter *Adapter) RuntimeLayout() (RuntimeLayout, error) {
	if err := adapter.validateConfigured(); err != nil {
		return RuntimeLayout{}, err
	}
	return adapter.layout, nil
}

// Snapshots returns a defensive copy of the adapter's exact model allowlist.
func Snapshots() []Snapshot {
	return append([]Snapshot(nil), snapshotAllowlist...)
}

// ExecutableSHA256Allowlist returns the exact Codex v0.144.0 release digests
// this adapter is permitted to identify.
func ExecutableSHA256Allowlist() []string {
	return append([]string(nil), executableSHA256Allowlist...)
}

// DisabledFeatures returns the exact v0.144.0 feature-disable manifest.
func DisabledFeatures() []string {
	return append([]string(nil), disabledFeatures...)
}

// EnabledFeatures returns the exact v0.144.0 feature-enable manifest.
func EnabledFeatures() []string {
	return append([]string(nil), enabledFeatures...)
}

// CommonEnvironment returns the complete adapter-owned process environment.
// It contains only a public dummy key; runner-scoped proxy routing and isolated
// state paths are supplied by the committed execution session, not ambient
// process state or authored suite data.
func (adapter *Adapter) CommonEnvironment(
	ctx context.Context,
	request harness.ResolveRequest,
) (map[string]string, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if err := adapter.validateConfigured(); err != nil {
		return nil, err
	}
	if request.Environment == nil || len(request.Environment) != 0 {
		return nil, errors.New("codex common environment resolution requires an empty input environment")
	}
	environment := adapter.commonEnvironment()
	request.Environment = cloneMap(environment)
	if err := adapter.validateResolveRequest(request); err != nil {
		return nil, err
	}
	return environment, nil
}

// Resolve implements harness.Adapter using only pinned inputs and the bytes of
// the already-running tokenbench executable. It does not probe Codex or read
// user configuration.
func (adapter *Adapter) Resolve(
	ctx context.Context,
	request harness.ResolveRequest,
) (harness.Identity, error) {
	if err := contextError(ctx); err != nil {
		return harness.Identity{}, err
	}
	if err := adapter.validateConfigured(); err != nil {
		return harness.Identity{}, err
	}
	if err := adapter.validateResolveRequest(request); err != nil {
		return harness.Identity{}, err
	}
	adapterDigest, err := currentExecutableSHA256()
	if err != nil {
		return harness.Identity{}, err
	}
	configDigest, err := adapter.resolvedConfigSHA256(request)
	if err != nil {
		return harness.Identity{}, err
	}
	identity := harness.Identity{
		AdapterExecutableSHA256:    adapterDigest,
		AdapterControlConfigSHA256: adapter.controlSHA256,
		AdapterConfigSHA256:        configDigest,
		Kind:                       adapterKind,
		AdapterVersion:             adapterVersion,
		ExecutableSHA256:           request.ExecutableSHA256,
		ExecutableVersion:          executableVersion,
		Model:                      request.Model,
		ModelRevision:              request.ExpectedModelRevision,
		ReasoningEffort:            request.ReasoningEffort,
		DecoderSchema:              decoderSchema,
	}
	if err := harness.ValidateIdentity(identity); err != nil {
		return harness.Identity{}, fmt.Errorf("resolve Codex identity: %w", err)
	}
	return identity, nil
}

// Build implements harness.Adapter. It accepts only the arm-common invocation;
// tokenbench appends the candidate-only MCPArguments result centrally.
func (adapter *Adapter) Build(
	ctx context.Context,
	invocation harness.Invocation,
) (harness.ProcessSpec, error) {
	if err := contextError(ctx); err != nil {
		return harness.ProcessSpec{}, err
	}
	if len(invocation.MCPServers) != 0 {
		return harness.ProcessSpec{}, errors.New(
			"codex Build accepts only the common invocation; use MCPArguments for registration",
		)
	}
	if len(invocation.Arguments) != 0 {
		return harness.ProcessSpec{}, errors.New("codex invocation arguments must be empty")
	}
	if len(invocation.Prompt) == 0 {
		return harness.ProcessSpec{}, errors.New("codex prompt must not be empty")
	}
	if !utf8.Valid(invocation.Prompt) {
		return harness.ProcessSpec{}, errors.New("codex prompt must be valid UTF-8")
	}
	if invocation.Model != invocation.RequestedModel {
		return harness.ProcessSpec{}, errors.New(
			"resolved and requested Codex model must be identical",
		)
	}
	request := resolveRequest(invocation)
	expectedIdentity, err := adapter.Resolve(ctx, request)
	if err != nil {
		return harness.ProcessSpec{}, err
	}
	if invocation.HarnessIdentity != expectedIdentity {
		return harness.ProcessSpec{}, errors.New(
			"invocation identity was not resolved by this Codex adapter",
		)
	}
	arguments, err := adapter.baseArguments(request)
	if err != nil {
		return harness.ProcessSpec{}, err
	}
	process := harness.ProcessSpec{
		Environment:   cloneMap(invocation.Environment),
		Directory:     invocation.WorkingDirectory,
		Argv:          append([]string{invocation.Executable}, arguments...),
		Stdin:         append([]byte(nil), invocation.Prompt...),
		TimeoutMillis: invocation.TimeoutMillis,
	}
	if err := harness.ValidateProcessSpec(process); err != nil {
		return harness.ProcessSpec{}, fmt.Errorf("build Codex process: %w", err)
	}
	return process, nil
}

// ValidateCanonicalProcess reconstructs the exact runtime layout committed in
// a Codex ProcessSpec, re-runs the built-in adapter, and requires full equality.
// Offline plan validation therefore rejects shared hidden flags/config rather
// than accepting them merely because both arms contain the same override.
func ValidateCanonicalProcess(
	invocation harness.Invocation,
	observed harness.ProcessSpec,
) error {
	_, err := AdapterForCanonicalProcess(invocation, observed)
	return err
}

// AdapterForCanonicalProcess reconstructs a built-in offline adapter only
// after the observed common process passes the same strict layout parsing and
// full rederivation check as ValidateCanonicalProcess. It is intended for
// replay of an already authenticated capture; no relaxed or caller-authored
// adapter capability is returned.
func AdapterForCanonicalProcess(
	invocation harness.Invocation,
	observed harness.ProcessSpec,
) (*Adapter, error) {
	if len(invocation.MCPServers) != 0 {
		return nil, errors.New("codex common invocation contains MCP servers")
	}
	layout, err := runtimeLayoutFromProcess(observed)
	if err != nil {
		return nil, err
	}
	adapter, err := New(layout)
	if err != nil {
		return nil, fmt.Errorf("reconstruct Codex adapter: %w", err)
	}
	expected, err := adapter.Build(context.Background(), invocation)
	if err != nil {
		return nil, fmt.Errorf("rederive Codex common process: %w", err)
	}
	if !reflect.DeepEqual(observed, expected) {
		return nil, errors.New("codex common process differs from its code-owned encoding")
	}
	return adapter, nil
}

func runtimeLayoutFromProcess(process harness.ProcessSpec) (RuntimeLayout, error) {
	proxyURL, err := exactQuotedConfigValue(process.Argv, "openai_base_url=")
	if err != nil {
		return RuntimeLayout{}, err
	}
	configLock, err := exactQuotedConfigValue(
		process.Argv,
		"debug.config_lockfile.export_dir=",
	)
	if err != nil {
		return RuntimeLayout{}, err
	}
	return RuntimeLayout{
		ProxyURL:             proxyURL,
		Home:                 process.Environment["HOME"],
		CodexHome:            process.Environment["CODEX_HOME"],
		Temp:                 process.Environment["TMPDIR"],
		ConfigLock:           configLock,
		ToolboxRoot:          process.Environment["PATH"],
		LocalProxyCapability: process.Environment["CODEX_API_KEY"],
	}, nil
}

func exactQuotedConfigValue(arguments []string, prefix string) (string, error) {
	encoded := ""
	count := 0
	for index := 0; index < len(arguments); index++ {
		if arguments[index] != "-c" {
			continue
		}
		if index+1 == len(arguments) {
			return "", errors.New("codex common argv ends with a bare -c")
		}
		assignment := arguments[index+1]
		if strings.HasPrefix(assignment, prefix) {
			count++
			encoded = strings.TrimPrefix(assignment, prefix)
		}
		index++
	}
	if count != 1 {
		return "", fmt.Errorf("codex common argv contains %d %s assignments", count, prefix)
	}
	var value string
	if err := json.Unmarshal([]byte(encoded), &value); err != nil {
		return "", fmt.Errorf("decode Codex %s assignment: %w", prefix, err)
	}
	canonical, err := tomlString(value)
	if err != nil || canonical != encoded {
		return "", fmt.Errorf("codex %s assignment is not canonical", prefix)
	}
	return value, nil
}

// MCPArguments renders the sole authorized treatment delta as strict Codex
// -c TOML assignments. No MCP environment value is permitted.
func (adapter *Adapter) MCPArguments(
	ctx context.Context,
	server harness.MCPServer,
) ([]string, error) {
	if err := contextError(ctx); err != nil {
		return nil, err
	}
	if err := adapter.validateConfigured(); err != nil {
		return nil, err
	}
	return CanonicalMCPArguments(server)
}

// CanonicalMCPArguments is the code-owned Codex v0.144 encoding of the sole
// permitted treatment. The tokenbench parity gate calls this independently of
// Adapter.MCPArguments, so a process adapter cannot smuggle unrelated config
// overrides into the candidate suffix while claiming to encode scopesifter.
func CanonicalMCPArguments(server harness.MCPServer) ([]string, error) {
	if err := validateMCPServer(server); err != nil {
		return nil, err
	}
	command, err := tomlString(server.Command)
	if err != nil {
		return nil, fmt.Errorf("encode scopesifter command: %w", err)
	}
	arguments, err := tomlStringArray(server.Arguments)
	if err != nil {
		return nil, fmt.Errorf("encode scopesifter arguments: %w", err)
	}
	tools, err := tomlStringArray(allowedMCPTools)
	if err != nil {
		return nil, fmt.Errorf("encode scopesifter tool allowlist: %w", err)
	}
	assignments := []string{
		"mcp_servers.scopesifter.command=" + command,
		"mcp_servers.scopesifter.args=" + arguments,
		"mcp_servers.scopesifter.env={}",
		"mcp_servers.scopesifter.env_vars=[]",
		"mcp_servers.scopesifter.enabled=true",
		"mcp_servers.scopesifter.required=true",
		"mcp_servers.scopesifter.enabled_tools=" + tools,
		"mcp_servers.scopesifter.disabled_tools=[]",
		"mcp_servers.scopesifter.default_tools_approval_mode=\"auto\"",
		"mcp_servers.scopesifter.startup_timeout_sec=10",
		"mcp_servers.scopesifter.tool_timeout_sec=60",
	}
	return configArguments(assignments), nil
}

func (adapter *Adapter) baseArguments(request harness.ResolveRequest) ([]string, error) {
	model, err := tomlString(request.Model)
	if err != nil {
		return nil, fmt.Errorf("encode model: %w", err)
	}
	reasoning, err := tomlString(request.ReasoningEffort)
	if err != nil {
		return nil, fmt.Errorf("encode reasoning effort: %w", err)
	}
	developerInstructions, err := tomlString(request.DeveloperInstructions)
	if err != nil {
		return nil, fmt.Errorf("encode developer instructions: %w", err)
	}
	arguments := []string{
		"exec",
		"--json",
		"--ephemeral",
		"--ignore-user-config",
		"--ignore-rules",
		"--strict-config",
		"--color", "never",
		"--sandbox", "read-only",
		"--model", request.Model,
		"--cd", request.WorkingDirectory,
	}
	for _, feature := range disabledFeatures {
		arguments = append(arguments, "--disable", feature)
	}
	for _, feature := range enabledFeatures {
		arguments = append(arguments, "--enable", feature)
	}
	assignments := []string{"model=" + model}
	assignments = append(assignments, adapter.layout.ConfigAssignments()...)
	assignments = append(assignments,
		"approval_policy=\"never\"",
		"developer_instructions="+developerInstructions,
		"model_reasoning_effort="+reasoning,
		"model_reasoning_summary=\"none\"",
		"model_verbosity=\"medium\"",
		"service_tier=\"default\"",
		"cli_auth_credentials_store=\"ephemeral\"",
		"allow_login_shell=false",
		"shell_environment_policy.inherit=\"none\"",
		"shell_environment_policy.ignore_default_excludes=false",
		"shell_environment_policy.experimental_use_profile=false",
		"project_doc_max_bytes=0",
		"project_doc_fallback_filenames=[]",
		"skills.bundled.enabled=false",
		"skills.include_instructions=false",
		"include_apps_instructions=false",
		"include_collaboration_mode_instructions=false",
		"include_environment_context=false",
		"include_permissions_instructions=false",
		"web_search=\"disabled\"",
		"apps._default.enabled=false",
		"mcp_servers={}",
		"history.persistence=\"none\"",
		"check_for_update_on_startup=false",
		"analytics.enabled=false",
		"feedback.enabled=false",
		"otel.exporter=\"none\"",
		"otel.trace_exporter=\"none\"",
		"otel.metrics_exporter=\"none\"",
		"otel.log_user_prompt=false",
	)
	return append(arguments, configArguments(assignments)...), nil
}

func configArguments(assignments []string) []string {
	arguments := make([]string, 0, len(assignments)*2)
	for _, assignment := range assignments {
		arguments = append(arguments, "-c", assignment)
	}
	return arguments
}

func (adapter *Adapter) validateResolveRequest(request harness.ResolveRequest) error {
	if err := harness.ValidatePublishableEnvironment(request.Environment); err != nil {
		return fmt.Errorf("codex environment: %w", err)
	}
	if !equalStringMap(request.Environment, adapter.commonEnvironment()) {
		return errors.New("codex environment does not match the pinned runtime layout")
	}
	if request.Environment["PATH"] != adapter.layout.ToolboxRoot {
		return errors.New("codex PATH differs from the immutable toolbox root")
	}
	efforts, modelKnown := allowedReasoningEfforts[request.Model]
	if _, ok := efforts[request.ReasoningEffort]; !modelKnown || !ok {
		return fmt.Errorf("unsupported Codex reasoning effort %q", request.ReasoningEffort)
	}
	if _, ok := findSnapshot(request.Model, request.ExpectedModelRevision); !ok {
		return fmt.Errorf(
			"unsupported Codex model snapshot %q",
			request.ExpectedModelRevision,
		)
	}
	if request.PermissionProfile != "read-only" {
		return errors.New("codex permission profile must be read-only")
	}
	if !validText(request.DeveloperInstructions) {
		return errors.New("codex developer instructions contain invalid text")
	}
	paths := []struct {
		name  string
		value string
	}{
		{"executable", request.Executable},
		{"working directory", request.WorkingDirectory},
		{"Git executable", request.GitExecutable},
		{"runner executable", request.RunnerExecutable},
	}
	for _, path := range paths {
		if !validText(path.value) || !filepath.IsAbs(path.value) ||
			filepath.Clean(path.value) != path.value {
			return fmt.Errorf("codex %s must be an absolute canonical path", path.name)
		}
	}
	digests := []struct {
		name  string
		value string
	}{
		{"executable", request.ExecutableSHA256},
		{"source tree", request.SourceTreeSHA256},
		{"Git executable", request.GitExecutableSHA256},
		{"Git metadata", request.GitMetadataSHA256},
		{"runner executable", request.RunnerExecutableSHA256},
	}
	for _, digest := range digests {
		if !validSHA256(digest.value) {
			return fmt.Errorf("codex %s digest is invalid", digest.name)
		}
	}
	if !containsString(executableSHA256Allowlist, request.ExecutableSHA256) {
		return errors.New("codex executable digest is not in the v0.144.0 allowlist")
	}
	if !validGitObjectID(request.SourceRevision) {
		return errors.New("codex source revision is invalid")
	}
	if !validGitObjectID(request.SourceBaseRevision) {
		return errors.New("codex source base revision is invalid")
	}
	if request.TimeoutMillis <= 0 {
		return errors.New("codex timeout must be positive")
	}
	return nil
}

func validateMCPServer(server harness.MCPServer) error {
	if server.Name != "scopesifter" {
		return fmt.Errorf("unsupported MCP server %q", server.Name)
	}
	if !server.Required || !server.ReadOnly {
		return errors.New("scopesifter MCP server must be required and read-only")
	}
	if !validText(server.Command) || !filepath.IsAbs(server.Command) ||
		filepath.Clean(server.Command) != server.Command {
		return errors.New("scopesifter MCP command must be an absolute canonical path")
	}
	if !validSHA256(server.ExecutableSHA256) {
		return errors.New("scopesifter MCP executable digest is invalid")
	}
	if server.Environment == nil || len(server.Environment) != 0 {
		return errors.New("scopesifter MCP environment must be a canonical empty object")
	}
	if len(server.Arguments) != 9 && len(server.Arguments) != 11 ||
		server.Arguments[0] != "mcp" ||
		server.Arguments[1] != "--root" ||
		server.Arguments[3] != "--base" {
		return errors.New("scopesifter MCP arguments do not match the canonical registration")
	}
	if !validText(server.Arguments[2]) ||
		!filepath.IsAbs(server.Arguments[2]) ||
		filepath.Clean(server.Arguments[2]) != server.Arguments[2] {
		return errors.New("scopesifter MCP root must be an absolute canonical path")
	}
	if !validGitObjectID(server.Arguments[4]) {
		return errors.New("scopesifter MCP base revision is invalid")
	}
	if len(server.Arguments) == 9 {
		if server.Arguments[5] != "--git" || server.Arguments[7] != "--git-sha256" {
			return errors.New("scopesifter MCP Git arguments are not canonical")
		}
		if !validText(server.Arguments[6]) ||
			!filepath.IsAbs(server.Arguments[6]) ||
			filepath.Clean(server.Arguments[6]) != server.Arguments[6] {
			return errors.New("scopesifter MCP Git executable must be an absolute canonical path")
		}
		if !validSHA256(server.Arguments[8]) {
			return errors.New("scopesifter MCP Git executable digest is invalid")
		}
		return nil
	}
	if server.Arguments[5] != "--head" ||
		server.Arguments[7] != "--changed-state-cache" ||
		server.Arguments[9] != "--changed-state-cache-sha256" {
		return errors.New("scopesifter MCP cache arguments are not canonical")
	}
	if !validGitObjectID(server.Arguments[6]) {
		return errors.New("scopesifter MCP cache head revision is invalid")
	}
	if !validText(server.Arguments[8]) ||
		!filepath.IsAbs(server.Arguments[8]) ||
		filepath.Clean(server.Arguments[8]) != server.Arguments[8] {
		return errors.New("scopesifter MCP cache must be an absolute canonical path")
	}
	if !validSHA256(server.Arguments[10]) {
		return errors.New("scopesifter MCP cache digest is invalid")
	}
	return nil
}

func findSnapshot(model, revision string) (Snapshot, bool) {
	for _, snapshot := range snapshotAllowlist {
		if snapshot.RequestedModel == model && snapshot.ModelRevision == revision {
			return snapshot, true
		}
	}
	return Snapshot{}, false
}

func snapshotForProviderModel(requested, provider string) (Snapshot, bool) {
	for _, snapshot := range snapshotAllowlist {
		if snapshot.RequestedModel == requested && snapshot.ProviderModel == provider {
			return snapshot, true
		}
	}
	return Snapshot{}, false
}

func (adapter *Adapter) resolvedConfigSHA256(request harness.ResolveRequest) (string, error) {
	arguments, err := adapter.baseArguments(request)
	if err != nil {
		return "", err
	}
	manifest := struct {
		Schema                string            `json:"schema"`
		Executable            string            `json:"executable"`
		ExecutableSHA256      string            `json:"executable_sha256"`
		ExpectedModelRevision string            `json:"expected_model_revision"`
		Environment           map[string]string `json:"environment"`
		Directory             string            `json:"directory"`
		Arguments             []string          `json:"arguments"`
		TimeoutMillis         int64             `json:"timeout_millis"`
	}{
		Schema:                configSchema,
		Executable:            request.Executable,
		ExecutableSHA256:      request.ExecutableSHA256,
		ExpectedModelRevision: request.ExpectedModelRevision,
		Environment:           cloneMap(request.Environment),
		Directory:             request.WorkingDirectory,
		Arguments:             arguments,
		TimeoutMillis:         request.TimeoutMillis,
	}
	raw, err := json.Marshal(manifest)
	if err != nil {
		return "", fmt.Errorf("encode resolved Codex configuration: %w", err)
	}
	return digest(raw), nil
}

func resolveRequest(invocation harness.Invocation) harness.ResolveRequest {
	return harness.ResolveRequest{
		Environment:            cloneMap(invocation.Environment),
		Executable:             invocation.Executable,
		ExecutableSHA256:       invocation.ExecutableSHA256,
		Model:                  invocation.RequestedModel,
		ExpectedModelRevision:  invocation.ModelRevision,
		ReasoningEffort:        invocation.ReasoningEffort,
		PermissionProfile:      invocation.PermissionProfile,
		DeveloperInstructions:  invocation.DeveloperInstructions,
		WorkingDirectory:       invocation.WorkingDirectory,
		SourceRevision:         invocation.SourceRevision,
		SourceBaseRevision:     invocation.SourceBaseRevision,
		SourceTreeSHA256:       invocation.SourceTreeSHA256,
		GitExecutable:          invocation.GitExecutable,
		GitExecutableSHA256:    invocation.GitExecutableSHA256,
		GitMetadataSHA256:      invocation.GitMetadataSHA256,
		RunnerExecutable:       invocation.RunnerExecutable,
		RunnerExecutableSHA256: invocation.RunnerExecutableSHA256,
		TimeoutMillis:          invocation.TimeoutMillis,
	}
}

func currentExecutableSHA256() (string, error) {
	identity, err := selfexec.Current()
	if err != nil {
		return "", fmt.Errorf("pin built-in Codex adapter executable: %w", err)
	}
	return identity.SHA256, nil
}

func digest(content []byte) string {
	value := sha256.Sum256(content)
	return hex.EncodeToString(value[:])
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && value == hex.EncodeToString(decoded)
}

func validGitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && value == hex.EncodeToString(decoded)
}

func validText(value string) bool {
	return utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func equalStringMap(left, right map[string]string) bool {
	if len(left) != len(right) {
		return false
	}
	for key, value := range left {
		if right[key] != value {
			return false
		}
	}
	return true
}

func cloneMap(source map[string]string) map[string]string {
	keys := make([]string, 0, len(source))
	for key := range source {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	clone := make(map[string]string, len(source))
	for _, key := range keys {
		clone[key] = source[key]
	}
	return clone
}

func contextError(ctx context.Context) error {
	if ctx == nil {
		return errors.New("context is required")
	}
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("codex adapter context: %w", err)
	}
	return nil
}

var _ harness.Adapter = (*Adapter)(nil)
var _ harness.CommonEnvironmentAdapter = (*Adapter)(nil)
