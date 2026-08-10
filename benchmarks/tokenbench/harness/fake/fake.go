// Package fake provides a deterministic harness adapter for offline runner and
// adapter conformance tests. It contains no Codex assumptions.
package fake

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"

	"github.com/dkropachev/repo-view/benchmarks/tokenbench/harness"
)

const adapterVersion = "tokenbench.fake-adapter/v1"

var adapterConfigSHA256 = digest([]byte("tokenbench.fake-config/v1"))
var adapterControlConfigSHA256 = digest([]byte("tokenbench.fake-control-config/v1"))

// Adapter is a deterministic harness.Adapter.
type Adapter struct{}

// Kind implements harness.Adapter.
func (Adapter) Kind() string {
	return "fake"
}

// MCPArguments implements harness.Adapter. Its input is only the approved
// registration, so task prompt or rubric content cannot enter the delta.
func (Adapter) MCPArguments(
	_ context.Context,
	server harness.MCPServer,
) ([]string, error) {
	return CanonicalMCPArguments(server)
}

// CanonicalMCPArguments is the independently verifiable fake-harness
// treatment encoding used by offline end-to-end tests.
func CanonicalMCPArguments(server harness.MCPServer) ([]string, error) {
	servers, err := json.Marshal([]harness.MCPServer{server})
	if err != nil {
		return nil, fmt.Errorf("encode MCP registry: %w", err)
	}
	return []string{
		"--mcp-servers-base64",
		base64.RawStdEncoding.EncodeToString(servers),
	}, nil
}

// Resolve implements harness.Adapter.
func (Adapter) Resolve(
	_ context.Context,
	request harness.ResolveRequest,
) (harness.Identity, error) {
	adapterDigest, err := currentExecutableSHA256()
	if err != nil {
		return harness.Identity{}, err
	}
	identity := harness.Identity{
		AdapterExecutableSHA256:    adapterDigest,
		AdapterControlConfigSHA256: adapterControlConfigSHA256,
		AdapterConfigSHA256:        adapterConfigSHA256,
		Kind:                       "fake",
		AdapterVersion:             adapterVersion,
		ExecutableSHA256:           request.ExecutableSHA256,
		ExecutableVersion:          "fake/v1",
		Model:                      request.Model,
		ModelRevision:              request.ExpectedModelRevision,
		ReasoningEffort:            request.ReasoningEffort,
		DecoderSchema:              "tokenbench.fake-output/v1",
	}
	if err := harness.ValidateIdentity(identity); err != nil {
		return harness.Identity{}, err
	}
	return identity, nil
}

func currentExecutableSHA256() (string, error) {
	path, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("resolve fake adapter executable: %w", err)
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read fake adapter executable: %w", err)
	}
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:]), nil
}

// Build implements harness.Adapter. The semantic MCP registry is encoded in a
// single candidate-only argument; every other rendered field is common.
func (Adapter) Build(
	_ context.Context,
	invocation harness.Invocation,
) (harness.ProcessSpec, error) {
	adapterDigest, err := currentExecutableSHA256()
	if err != nil {
		return harness.ProcessSpec{}, err
	}
	if invocation.HarnessIdentity.Kind != "fake" ||
		invocation.HarnessIdentity.AdapterExecutableSHA256 != adapterDigest ||
		invocation.HarnessIdentity.AdapterControlConfigSHA256 != adapterControlConfigSHA256 ||
		invocation.HarnessIdentity.AdapterConfigSHA256 != adapterConfigSHA256 {
		return harness.ProcessSpec{}, errors.New(
			"invocation identity was not resolved by this fake adapter",
		)
	}
	if len(invocation.Prompt) == 0 {
		return harness.ProcessSpec{}, errors.New("prompt must not be empty")
	}
	if len(invocation.MCPServers) != 0 {
		return harness.ProcessSpec{}, errors.New(
			"fake Build accepts only the common invocation; use MCPArguments for registration",
		)
	}
	return canonicalProcess(invocation)
}

// ValidateCanonicalProcess lets the offline plan verifier independently
// rederive the complete fake-harness common process. It does not trust a
// caller-authored common argv merely because both arms share it.
func ValidateCanonicalProcess(
	invocation harness.Invocation,
	observed harness.ProcessSpec,
) error {
	expected, err := canonicalProcess(invocation)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(observed, expected) {
		return errors.New("fake common process differs from its code-owned encoding")
	}
	return nil
}

func canonicalProcess(invocation harness.Invocation) (harness.ProcessSpec, error) {
	if len(invocation.MCPServers) != 0 {
		return harness.ProcessSpec{}, errors.New("fake common invocation contains MCP servers")
	}
	arguments := append([]string{invocation.Executable}, invocation.Arguments...)
	arguments = append(
		arguments,
		"--model", invocation.RequestedModel,
		"--reasoning-effort", invocation.ReasoningEffort,
		"--permission-profile", invocation.PermissionProfile,
		"--developer-instructions", invocation.DeveloperInstructions,
		"--source-revision", invocation.SourceRevision,
		"--source-base-revision", invocation.SourceBaseRevision,
		"--source-tree-sha256", invocation.SourceTreeSHA256,
	)
	process := harness.ProcessSpec{
		Environment:   cloneMap(invocation.Environment),
		Argv:          arguments,
		Stdin:         append([]byte(nil), invocation.Prompt...),
		Directory:     invocation.WorkingDirectory,
		TimeoutMillis: invocation.TimeoutMillis,
	}
	if err := harness.ValidateProcessSpec(process); err != nil {
		return harness.ProcessSpec{}, err
	}
	return process, nil
}

func digest(content []byte) string {
	value := sha256.Sum256(content)
	return hex.EncodeToString(value[:])
}

// Decode implements harness.Adapter.
func (Adapter) Decode(
	_ context.Context,
	execution harness.RawExecution,
) (harness.Observation, error) {
	if execution.TimedOut || execution.ExitCode != 0 {
		return harness.Observation{}, errors.New("fake harness execution did not complete")
	}
	if len(bytes.TrimSpace(execution.Stderr)) != 0 {
		return harness.Observation{}, errors.New("fake harness wrote to stderr")
	}
	var output struct {
		FinalAnswer string        `json:"final_answer"`
		Model       string        `json:"model"`
		ToolCalls   []string      `json:"tool_calls"`
		Usage       harness.Usage `json:"usage"`
		Completed   bool          `json:"completed"`
	}
	decoder := json.NewDecoder(bytes.NewReader(execution.Stdout))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&output); err != nil {
		return harness.Observation{}, fmt.Errorf("decode fake output: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return harness.Observation{}, errors.New("fake output contains multiple JSON values")
		}
		return harness.Observation{}, fmt.Errorf("decode fake output: %w", err)
	}
	if err := harness.ValidateUsage(output.Usage); err != nil {
		return harness.Observation{}, err
	}
	return harness.Observation{
		Usage:       output.Usage,
		FinalAnswer: output.FinalAnswer,
		Model:       output.Model,
		ToolCalls:   append([]string(nil), output.ToolCalls...),
		Completed:   output.Completed,
	}, nil
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

var _ harness.Adapter = Adapter{}
