package codex

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"unicode/utf8"

	"github.com/dkropachev/repo-view/benchmarks/tokenbench/harness"
)

const (
	// ResponsesTraceArtifactName is the sanitized provider-wire evidence artifact
	// required by Decode.
	ResponsesTraceArtifactName = "codex.responses_trace"
	// ResponsesTraceMediaType identifies the strict exported ResponsesTrace JSON
	// schema below.
	ResponsesTraceMediaType = "application/vnd.tokenbench.codex.responses-trace.v1+json"
	// PartialResponsesTraceArtifactName is emitted for an ordinary terminal arm
	// so all sanitized provider progress remains auditable even though Decode is
	// intentionally skipped.
	PartialResponsesTraceArtifactName = "codex.responses_trace.partial"
	// PartialResponsesTraceMediaType identifies the strict partial trace schema.
	PartialResponsesTraceMediaType = "application/vnd.tokenbench.codex.partial-responses-trace.v1+json"
	// EffectiveConfigArtifactName is the runner-exported effective Codex config.
	EffectiveConfigArtifactName = "codex.effective_config"
	// EffectiveConfigMediaType pins the parser profile used for the config bytes.
	EffectiveConfigMediaType = "application/toml;profile=codex-v0.144.0"
	// ResponsesTraceSchemaVersion is the sole accepted provider trace schema.
	ResponsesTraceSchemaVersion = "tokenbench.codex.responses-trace/v1"
	// PartialResponsesTraceSchemaVersion is separate because incomplete request
	// sequences are never valid decoder input.
	PartialResponsesTraceSchemaVersion = "tokenbench.codex.partial-responses-trace/v1"

	ToolKindCommand    = "command"
	ToolKindMCP        = "mcp"
	ToolKindMCPSupport = "mcp_support"

	// MaxResponsesTraceBytes is the runner and decoder byte limit for one trace.
	MaxResponsesTraceBytes = 8 << 20
	// MaxEffectiveConfigBytes is the runner and decoder byte limit for config.
	MaxEffectiveConfigBytes = 1 << 20

	maxTraceResponses = 4096
	maxTraceTools     = 4096
	maxTraceOutputs   = 100000
)

var allowedMCPTools = []string{"changed", "find", "inspect", "outline"}

var allowedNormalizedMCPCalls = map[string]struct{}{
	"repo_view.changed": {},
	"repo_view.find":    {},
	"repo_view.inspect": {},
	"repo_view.outline": {},
}

// Codex v0.144.0 adds these provider-visible helper tools whenever at least
// one MCP server is configured. They are Codex's generic MCP resource UI, not
// resource capabilities advertised by repo-view itself.
var allowedMCPSupportTools = []string{
	"list_mcp_resources",
	"list_mcp_resource_templates",
	"read_mcp_resource",
}

var allowedMCPSupportCalls = map[string]struct{}{
	"list_mcp_resources":          {},
	"list_mcp_resource_templates": {},
	"read_mcp_resource":           {},
}

// ResponsesTrace is the credential-free normalized evidence emitted by the
// runner's Codex Responses proxy. FirstRequest commits the first provider
// request with its tool list separated from the non-tool payload so pair-level
// validation can authorize only the repo_view declaration delta.
type ResponsesTrace struct {
	SchemaVersion string                   `json:"schema_version"`
	FirstRequest  *ResponsesRequestTrace   `json:"first_request"`
	Responses     []ResponsesResponseTrace `json:"responses"`
}

// PartialResponsesTrace retains completed, sanitized response entries and
// their original request sequence numbers after an ordinary process failure.
// It never masquerades as the complete decoder artifact.
type PartialResponsesTrace struct {
	SchemaVersion     string                   `json:"schema_version"`
	FirstRequest      *ResponsesRequestTrace   `json:"first_request"`
	Responses         []ResponsesResponseTrace `json:"responses"`
	ResponseSequences []int                    `json:"response_sequences"`
	RequestCount      int                      `json:"request_count"`
}

// ResponsesRequestTrace describes the sanitized first Responses request.
// NonToolPayloadSHA256 is the SHA-256 of the runner's canonical request after
// removing tools and normalizing runner-authorized transport identifiers.
type ResponsesRequestTrace struct {
	Model                string                     `json:"model"`
	NonToolPayloadSHA256 string                     `json:"non_tool_payload_sha256"`
	Tools                []ResponsesToolDeclaration `json:"tools"`
}

// ResponsesToolDeclaration is one normalized provider-visible tool schema.
// Description and input schema are represented only by canonical SHA-256
// commitments so the trace cannot accidentally retain repository content.
type ResponsesToolDeclaration struct {
	Kind              string `json:"kind"`
	Name              string `json:"name"`
	WireType          string `json:"wire_type"`
	Strict            *bool  `json:"strict"`
	DescriptionSHA256 string `json:"description_sha256"`
	InputSchemaSHA256 string `json:"input_schema_sha256"`
}

// ResponsesResponseTrace is one completed provider response in request order.
// Usage is a pointer so omission is distinguishable from a legitimate all-zero
// provider counter object.
type ResponsesResponseTrace struct {
	RequestModel                string                 `json:"request_model"`
	RequestNonToolPayloadSHA256 string                 `json:"request_non_tool_payload_sha256"`
	RequestToolsSHA256          string                 `json:"request_tools_sha256"`
	ResponseModel               string                 `json:"response_model"`
	ProviderModelHeader         string                 `json:"provider_model_header"`
	TurnStateSHA256             string                 `json:"turn_state_sha256"`
	Outputs                     []ResponsesOutputTrace `json:"outputs"`
	Usage                       *harness.Usage         `json:"usage"`
	TLSConnections              []TLSConnectionTrace   `json:"tls_connections,omitempty"`
	ProviderTotalTokens         *int64                 `json:"provider_total_tokens"`
	ReasoningIncluded           bool                   `json:"reasoning_included"`
}

// TLSConnectionTrace commits the authenticated production transport used for
// one provider request. DNSName is the exact verified SNI/hostname and each
// chain contains ordered SHA-256 fingerprints of the certificates' exact DER
// bytes (leaf first). Generic, nonpublishable loopback lifecycles omit it.
type TLSConnectionTrace struct {
	DNSName              string     `json:"dns_name"`
	VerifiedChainsSHA256 [][]string `json:"verified_chains_sha256"`
	TLSVersion           uint16     `json:"tls_version"`
}

const (
	OutputTypeAssistantMessage = "assistant_message"
	OutputTypeReasoning        = "reasoning"
	OutputTypeToolCall         = "tool_call"
)

// ResponsesOutputTrace is one provider output item in exact response order.
// WireSHA256 commits the entire canonical provider item. PayloadSHA256 commits
// the exact payload v0.144.0 exposes through exec JSONL; it is empty only for
// an encrypted reasoning item whose summary Codex intentionally omits.
type ResponsesOutputTrace struct {
	Type          string `json:"type"`
	WireSHA256    string `json:"wire_sha256"`
	PayloadSHA256 string `json:"payload_sha256"`
	Kind          string `json:"kind"`
	Name          string `json:"name"`
}

// ResponsesToolCall is the JSONL-visible identity of one provider-issued call.
// Name is the exact native command-tool name for Kind command and the
// normalized server.tool name for Kind mcp. ArgumentsSHA256 commits the
// canonical provider arguments after the exact v0.144.0 JSONL transformation.
type ResponsesToolCall struct {
	Kind            string `json:"kind"`
	Name            string `json:"name"`
	ArgumentsSHA256 string `json:"arguments_sha256"`
}

type decodedTrace struct {
	usage         harness.Usage
	model         string
	toolCalls     []ResponsesToolCall
	toolCallNames []string
	outputs       []ResponsesOutputTrace
}

// AllowedMCPTools returns the exact repo_view tool allowlist, without the
// normalized server prefix used by ResponsesTrace.
func AllowedMCPTools() []string {
	return append([]string(nil), allowedMCPTools...)
}

// AllowedMCPSupportTools returns the exact generic resource-tool delta Codex
// v0.144.0 exposes when the candidate registers repo-view.
func AllowedMCPSupportTools() []string {
	return append([]string(nil), allowedMCPSupportTools...)
}

// ParseResponsesTrace performs the same bounded, duplicate-key rejecting,
// unknown-field rejecting validation used by Decode. The runner can use this
// when comparing sanitized first requests between paired arms.
func ParseResponsesTrace(raw []byte) (ResponsesTrace, error) {
	trace, _, err := parseResponsesTrace(raw)
	return trace, err
}

// ParsePartialResponsesTrace validates the bounded failure-only trace envelope.
// Complete decoding deliberately does not accept this schema.
func ParsePartialResponsesTrace(raw []byte) (PartialResponsesTrace, error) {
	if len(raw) == 0 || len(raw) > MaxResponsesTraceBytes {
		return PartialResponsesTrace{}, errors.New(
			"Codex partial Responses trace is empty or exceeds its byte limit",
		)
	}
	var trace PartialResponsesTrace
	if err := strictUnmarshalJSON(raw, &trace); err != nil {
		return PartialResponsesTrace{}, fmt.Errorf(
			"decode Codex partial Responses trace: %w",
			err,
		)
	}
	if trace.SchemaVersion != PartialResponsesTraceSchemaVersion ||
		trace.RequestCount < 0 || trace.RequestCount > maxTraceResponses ||
		trace.Responses == nil || trace.ResponseSequences == nil ||
		len(trace.Responses) != len(trace.ResponseSequences) ||
		len(trace.Responses) > trace.RequestCount {
		return PartialResponsesTrace{}, errors.New(
			"Codex partial Responses trace envelope is invalid",
		)
	}
	if (trace.RequestCount == 0) != (trace.FirstRequest == nil) {
		return PartialResponsesTrace{}, errors.New(
			"Codex partial Responses trace request identity is inconsistent",
		)
	}
	prior := -1
	for _, sequence := range trace.ResponseSequences {
		if sequence <= prior || sequence < 0 || sequence >= trace.RequestCount {
			return PartialResponsesTrace{}, errors.New(
				"Codex partial Responses trace sequences are not canonical",
			)
		}
		prior = sequence
	}
	if trace.FirstRequest != nil {
		if !validText(trace.FirstRequest.Model) || trace.FirstRequest.Model == "" ||
			!validSHA256(trace.FirstRequest.NonToolPayloadSHA256) ||
			trace.FirstRequest.Tools == nil ||
			len(trace.FirstRequest.Tools) > maxTraceTools {
			return PartialResponsesTrace{}, errors.New(
				"Codex partial Responses first request is invalid",
			)
		}
	}
	return trace, nil
}

func decodeArtifacts(artifacts []harness.Artifact) (decodedTrace, error) {
	if err := harness.ValidateArtifacts(artifacts); err != nil {
		return decodedTrace{}, fmt.Errorf("Codex artifacts: %w", err)
	}
	if len(artifacts) != 2 {
		return decodedTrace{}, fmt.Errorf(
			"Codex execution requires exactly two artifacts, got %d",
			len(artifacts),
		)
	}
	var traceRaw, configRaw []byte
	for _, artifact := range artifacts {
		switch artifact.Name {
		case ResponsesTraceArtifactName:
			if artifact.MediaType != ResponsesTraceMediaType {
				return decodedTrace{}, fmt.Errorf(
					"artifact %q has media type %q, want %q",
					artifact.Name, artifact.MediaType, ResponsesTraceMediaType,
				)
			}
			traceRaw = artifact.Data
		case EffectiveConfigArtifactName:
			if artifact.MediaType != EffectiveConfigMediaType {
				return decodedTrace{}, fmt.Errorf(
					"artifact %q has media type %q, want %q",
					artifact.Name, artifact.MediaType, EffectiveConfigMediaType,
				)
			}
			configRaw = artifact.Data
		default:
			return decodedTrace{}, fmt.Errorf("unexpected Codex artifact %q", artifact.Name)
		}
	}
	if len(traceRaw) == 0 || len(traceRaw) > MaxResponsesTraceBytes {
		return decodedTrace{}, errors.New("Codex Responses trace is empty or exceeds its byte limit")
	}
	if len(configRaw) == 0 || len(configRaw) > MaxEffectiveConfigBytes {
		return decodedTrace{}, errors.New("Codex effective config is empty or exceeds its byte limit")
	}
	if !utf8.Valid(configRaw) || bytes.IndexByte(configRaw, 0) >= 0 {
		return decodedTrace{}, errors.New("Codex effective config contains invalid text")
	}

	_, decoded, err := parseResponsesTrace(traceRaw)
	return decoded, err
}

func parseResponsesTrace(raw []byte) (ResponsesTrace, decodedTrace, error) {
	if len(raw) == 0 || len(raw) > MaxResponsesTraceBytes {
		return ResponsesTrace{}, decodedTrace{}, errors.New(
			"Codex Responses trace is empty or exceeds its byte limit",
		)
	}
	var trace ResponsesTrace
	if err := strictUnmarshalJSON(raw, &trace); err != nil {
		return ResponsesTrace{}, decodedTrace{}, fmt.Errorf("decode Codex Responses trace: %w", err)
	}
	decoded, err := validateResponsesTrace(trace)
	if err != nil {
		return ResponsesTrace{}, decodedTrace{}, err
	}
	return trace, decoded, nil
}

func validateResponsesTrace(trace ResponsesTrace) (decodedTrace, error) {
	if trace.SchemaVersion != ResponsesTraceSchemaVersion {
		return decodedTrace{}, fmt.Errorf(
			"unsupported Codex Responses trace schema %q",
			trace.SchemaVersion,
		)
	}
	if trace.FirstRequest == nil {
		return decodedTrace{}, errors.New("Codex Responses trace omitted first_request")
	}
	if len(trace.Responses) == 0 || len(trace.Responses) > maxTraceResponses {
		return decodedTrace{}, errors.New("Codex Responses trace has invalid response cardinality")
	}
	first := trace.FirstRequest
	if !validText(first.Model) || first.Model == "" {
		return decodedTrace{}, errors.New("Codex first request model is invalid")
	}
	if !validSHA256(first.NonToolPayloadSHA256) {
		return decodedTrace{}, errors.New("Codex first request non-tool payload digest is invalid")
	}
	if first.Tools == nil || len(first.Tools) > maxTraceTools {
		return decodedTrace{}, errors.New("Codex first request tool list is not canonical")
	}
	declarations := make(map[string]ResponsesToolDeclaration, len(first.Tools))
	mcpDeclarations := make(map[string]struct{})
	mcpSupportDeclarations := make(map[string]struct{})
	commandDeclarations := 0
	for index, declaration := range first.Tools {
		if !validText(declaration.Name) || declaration.Name == "" {
			return decodedTrace{}, fmt.Errorf("Codex tool declaration %d has an invalid name", index)
		}
		switch declaration.WireType {
		case "function":
			if declaration.Strict == nil {
				return decodedTrace{}, fmt.Errorf("Codex function declaration %q omitted strict", declaration.Name)
			}
		case "custom":
			if declaration.Strict != nil {
				return decodedTrace{}, fmt.Errorf("Codex custom declaration %q contains strict", declaration.Name)
			}
		default:
			return decodedTrace{}, fmt.Errorf("Codex declaration %q has unsupported wire type %q", declaration.Name, declaration.WireType)
		}
		if !validSHA256(declaration.DescriptionSHA256) ||
			!validSHA256(declaration.InputSchemaSHA256) {
			return decodedTrace{}, fmt.Errorf("Codex tool declaration %q has an invalid digest", declaration.Name)
		}
		key := declaration.Kind + "\x00" + declaration.Name
		if _, exists := declarations[key]; exists {
			return decodedTrace{}, fmt.Errorf("duplicate Codex tool declaration %q", declaration.Name)
		}
		declarations[key] = declaration
		switch declaration.Kind {
		case ToolKindCommand:
			commandDeclarations++
		case ToolKindMCP:
			if declaration.WireType != "function" || declaration.Strict == nil || *declaration.Strict {
				return decodedTrace{}, fmt.Errorf("repo_view declaration %q has an unsupported wire shape", declaration.Name)
			}
			if _, ok := allowedNormalizedMCPCalls[declaration.Name]; !ok {
				return decodedTrace{}, fmt.Errorf("unsupported Codex MCP tool declaration %q", declaration.Name)
			}
			mcpDeclarations[declaration.Name] = struct{}{}
		case ToolKindMCPSupport:
			if declaration.WireType != "function" || declaration.Strict == nil || *declaration.Strict {
				return decodedTrace{}, fmt.Errorf("MCP support declaration %q has an unsupported wire shape", declaration.Name)
			}
			if _, ok := allowedMCPSupportCalls[declaration.Name]; !ok {
				return decodedTrace{}, fmt.Errorf("unsupported Codex MCP support declaration %q", declaration.Name)
			}
			mcpSupportDeclarations[declaration.Name] = struct{}{}
		default:
			return decodedTrace{}, fmt.Errorf("unsupported Codex tool kind %q", declaration.Kind)
		}
	}
	if commandDeclarations == 0 {
		return decodedTrace{}, errors.New("Codex first request omitted the pinned command tool")
	}
	if len(mcpDeclarations) != 0 && len(mcpDeclarations) != len(allowedNormalizedMCPCalls) {
		return decodedTrace{}, errors.New("Codex first request has a partial repo_view tool surface")
	}
	if len(mcpSupportDeclarations) != 0 &&
		len(mcpSupportDeclarations) != len(allowedMCPSupportCalls) {
		return decodedTrace{}, errors.New("Codex first request has a partial MCP support tool surface")
	}
	if (len(mcpDeclarations) == 0) != (len(mcpSupportDeclarations) == 0) {
		return decodedTrace{}, errors.New("Codex first request MCP and support surfaces are inconsistent")
	}

	result := decodedTrace{
		model:     first.Model,
		toolCalls: make([]ResponsesToolCall, 0),
		outputs:   make([]ResponsesOutputTrace, 0),
	}
	toolsRaw, err := json.Marshal(first.Tools)
	if err != nil {
		return decodedTrace{}, fmt.Errorf("encode Codex request tool commitment: %w", err)
	}
	requestToolsSHA256 := digest(toolsRaw)
	providerModel := ""
	tlsEvidenceObserved := false
	tlsEvidenceExpected := false
	for responseIndex, response := range trace.Responses {
		if response.RequestModel != first.Model {
			return decodedTrace{}, fmt.Errorf(
				"Codex response %d request model %q differs from first request model %q",
				responseIndex, response.RequestModel, first.Model,
			)
		}
		if !validSHA256(response.RequestNonToolPayloadSHA256) {
			return decodedTrace{}, fmt.Errorf(
				"Codex response %d request payload commitment is invalid",
				responseIndex,
			)
		}
		if responseIndex == 0 &&
			response.RequestNonToolPayloadSHA256 != first.NonToolPayloadSHA256 {
			return decodedTrace{}, errors.New(
				"first Codex response request commitment differs from first_request",
			)
		}
		if response.RequestToolsSHA256 != requestToolsSHA256 {
			return decodedTrace{}, fmt.Errorf(
				"Codex response %d request tool commitment differs from first_request",
				responseIndex,
			)
		}
		if !validText(response.ResponseModel) || response.ResponseModel == "" {
			return decodedTrace{}, fmt.Errorf("Codex response %d model is invalid", responseIndex)
		}
		if response.ProviderModelHeader != response.ResponseModel {
			return decodedTrace{}, fmt.Errorf(
				"Codex response %d provider model header differs from its body",
				responseIndex,
			)
		}
		if !validSHA256(response.TurnStateSHA256) {
			return decodedTrace{}, fmt.Errorf("Codex response %d turn-state commitment is invalid", responseIndex)
		}
		if _, ok := snapshotForProviderModel(first.Model, response.ResponseModel); !ok {
			return decodedTrace{}, fmt.Errorf(
				"Codex response %d reported unsupported model resolution %q -> %q",
				responseIndex, first.Model, response.ResponseModel,
			)
		}
		if providerModel == "" {
			providerModel = response.ResponseModel
		} else if providerModel != response.ResponseModel {
			return decodedTrace{}, errors.New("Codex provider model changed within one turn")
		}
		if response.Usage == nil {
			return decodedTrace{}, fmt.Errorf("Codex response %d omitted usage", responseIndex)
		}
		if response.ProviderTotalTokens != nil {
			total := *response.ProviderTotalTokens
			if total < 0 || total != response.Usage.InputTokens+response.Usage.OutputTokens {
				return decodedTrace{}, fmt.Errorf(
					"Codex response %d provider total_tokens is inconsistent",
					responseIndex,
				)
			}
		}
		if !tlsEvidenceObserved {
			tlsEvidenceObserved = true
			tlsEvidenceExpected = len(response.TLSConnections) != 0
		} else if tlsEvidenceExpected != (len(response.TLSConnections) != 0) {
			return decodedTrace{}, errors.New(
				"Codex production TLS evidence is incomplete within one arm",
			)
		}
		for connectionIndex, connection := range response.TLSConnections {
			if err := validateTLSConnectionTrace(connection); err != nil {
				return decodedTrace{}, fmt.Errorf(
					"Codex response %d TLS connection %d: %w",
					responseIndex,
					connectionIndex,
					err,
				)
			}
		}
		if err := harness.ValidateUsage(*response.Usage); err != nil {
			return decodedTrace{}, fmt.Errorf("Codex response %d usage: %w", responseIndex, err)
		}
		if response.Usage.CacheWriteInputTokens != 0 {
			return decodedTrace{}, errors.New("Codex Responses trace reported unsupported cache-write tokens")
		}
		if err := addUsage(&result.usage, *response.Usage); err != nil {
			return decodedTrace{}, fmt.Errorf("Codex Responses trace usage: %w", err)
		}
		if response.Outputs == nil {
			return decodedTrace{}, fmt.Errorf("Codex response %d outputs must be an array", responseIndex)
		}
		if len(result.outputs)+len(response.Outputs) > maxTraceOutputs {
			return decodedTrace{}, errors.New("Codex Responses trace exceeds its output-item limit")
		}
		for outputIndex, output := range response.Outputs {
			if !validSHA256(output.WireSHA256) {
				return decodedTrace{}, fmt.Errorf(
					"Codex response %d output %d has an invalid wire commitment",
					responseIndex, outputIndex,
				)
			}
			switch output.Type {
			case OutputTypeAssistantMessage:
				if !validSHA256(output.PayloadSHA256) || output.Kind != "" || output.Name != "" {
					return decodedTrace{}, fmt.Errorf(
						"Codex response %d assistant output %d is invalid",
						responseIndex, outputIndex,
					)
				}
			case OutputTypeReasoning:
				if output.PayloadSHA256 != "" && !validSHA256(output.PayloadSHA256) ||
					output.Kind != "" || output.Name != "" {
					return decodedTrace{}, fmt.Errorf(
						"Codex response %d reasoning output %d is invalid",
						responseIndex, outputIndex,
					)
				}
			case OutputTypeToolCall:
				call := ResponsesToolCall{
					Kind:            output.Kind,
					Name:            output.Name,
					ArgumentsSHA256: output.PayloadSHA256,
				}
				if err := validateToolCall(declarations, call); err != nil {
					return decodedTrace{}, fmt.Errorf(
						"Codex response %d output %d: %w",
						responseIndex, outputIndex, err,
					)
				}
				result.toolCalls = append(result.toolCalls, call)
				result.toolCallNames = append(result.toolCallNames, call.Name)
			default:
				return decodedTrace{}, fmt.Errorf(
					"Codex response %d output %d has unsupported type %q",
					responseIndex, outputIndex, output.Type,
				)
			}
			if output.PayloadSHA256 != "" {
				result.outputs = append(result.outputs, output)
			}
		}
	}
	return result, nil
}

func validateTLSConnectionTrace(connection TLSConnectionTrace) error {
	if connection.DNSName != "api.openai.com" || connection.TLSVersion < tls.VersionTLS12 {
		return errors.New("verified DNS name or TLS version is outside production policy")
	}
	if len(connection.VerifiedChainsSHA256) == 0 ||
		len(connection.VerifiedChainsSHA256) > 16 {
		return errors.New("verified chain cardinality is invalid")
	}
	for _, chain := range connection.VerifiedChainsSHA256 {
		if len(chain) == 0 || len(chain) > 16 {
			return errors.New("verified certificate cardinality is invalid")
		}
		for _, fingerprint := range chain {
			if !validSHA256(fingerprint) {
				return errors.New("verified certificate fingerprint is invalid")
			}
		}
	}
	return nil
}

func cloneTLSConnectionTrace(source TLSConnectionTrace) TLSConnectionTrace {
	clone := source
	clone.VerifiedChainsSHA256 = make([][]string, len(source.VerifiedChainsSHA256))
	for index, chain := range source.VerifiedChainsSHA256 {
		clone.VerifiedChainsSHA256[index] = append([]string(nil), chain...)
	}
	return clone
}

func validateToolCall(
	declarations map[string]ResponsesToolDeclaration,
	call ResponsesToolCall,
) error {
	if !validText(call.Name) || call.Name == "" {
		return errors.New("tool call has an invalid name")
	}
	if !validSHA256(call.ArgumentsSHA256) {
		return errors.New("tool call has an invalid argument commitment")
	}
	if call.Kind != ToolKindCommand && call.Kind != ToolKindMCP &&
		call.Kind != ToolKindMCPSupport {
		return fmt.Errorf("unsupported Codex tool-call kind %q", call.Kind)
	}
	if call.Kind == ToolKindCommand && call.Name != "exec_command" {
		return fmt.Errorf(
			"command call %q has no reviewed Codex JSONL argument mapping",
			call.Name,
		)
	}
	if call.Kind == ToolKindMCP {
		if _, ok := allowedNormalizedMCPCalls[call.Name]; !ok {
			return fmt.Errorf("unsupported Codex MCP call %q", call.Name)
		}
	}
	if call.Kind == ToolKindMCPSupport {
		if _, ok := allowedMCPSupportCalls[call.Name]; !ok {
			return fmt.Errorf("unsupported Codex MCP support call %q", call.Name)
		}
	}
	if _, declared := declarations[call.Kind+"\x00"+call.Name]; !declared {
		return fmt.Errorf("Codex called undeclared tool %q", call.Name)
	}
	return nil
}

func addUsage(total *harness.Usage, next harness.Usage) error {
	values := []struct {
		name   string
		target *int64
		value  int64
	}{
		{"input", &total.InputTokens, next.InputTokens},
		{"cached input", &total.CachedInputTokens, next.CachedInputTokens},
		{"cache-write input", &total.CacheWriteInputTokens, next.CacheWriteInputTokens},
		{"output", &total.OutputTokens, next.OutputTokens},
		{"reasoning", &total.ReasoningTokens, next.ReasoningTokens},
	}
	for _, value := range values {
		if value.value > math.MaxInt64-*value.target {
			return fmt.Errorf("%s token counter overflow", value.name)
		}
		*value.target += value.value
	}
	return harness.ValidateUsage(*total)
}

func strictUnmarshalJSON(raw []byte, destination any) error {
	if !utf8.Valid(raw) {
		return errors.New("JSON must be valid UTF-8")
	}
	if err := rejectDuplicateJSONKeys(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON documents are not allowed")
		}
		return err
	}
	return nil
}

func rejectDuplicateJSONKeys(raw []byte) error {
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
					return errors.New("JSON object key is not a string")
				}
				if _, exists := seen[key]; exists {
					return fmt.Errorf("duplicate JSON object key %q", key)
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
			return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
		}
	}
	if err := walk(); err != nil {
		return err
	}
	if decoder.More() {
		return errors.New("unexpected trailing JSON tokens")
	}
	return nil
}

func canonicalJSON(raw json.RawMessage) ([]byte, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return nil, errors.New("JSON value has trailing content")
	}
	return json.Marshal(value)
}
