package codex

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"mime"
	"net/http"
	"reflect"
	"strings"
	"unicode/utf8"

	"github.com/scopesifter/scopesifter/benchmarks/tokenbench/harness"
)

const (
	// ResponsesTraceArtifactName is the sanitized provider-wire evidence artifact
	// required by Decode.
	ResponsesTraceArtifactName = "codex.responses_trace"
	// ResponsesTraceMediaType identifies the strict exported ResponsesTrace JSON
	// schema below.
	ResponsesTraceMediaType = "application/vnd.tokenbench.codex.responses-trace.v4+json"
	// PartialResponsesTraceArtifactName is emitted for an ordinary terminal arm
	// so all sanitized provider progress remains auditable even though Decode is
	// intentionally skipped.
	PartialResponsesTraceArtifactName = "codex.responses_trace.partial"
	// PartialResponsesTraceMediaType identifies the strict partial trace schema.
	PartialResponsesTraceMediaType = "application/vnd.tokenbench.codex.partial-responses-trace.v4+json"
	// EffectiveConfigArtifactName is the runner-exported effective Codex config.
	EffectiveConfigArtifactName = "codex.effective_config"
	// EffectiveConfigMediaType pins the parser profile used for the config bytes.
	EffectiveConfigMediaType = "application/toml;profile=codex-v0.144.0"
	// ResponsesTraceSchemaVersion is the sole accepted provider trace schema.
	ResponsesTraceSchemaVersion = "tokenbench.codex.responses-trace/v4"
	// PartialResponsesTraceSchemaVersion is separate because incomplete request
	// sequences are never valid decoder input.
	PartialResponsesTraceSchemaVersion = "tokenbench.codex.partial-responses-trace/v4"

	ToolKindCommand    = "command"
	ToolKindMCP        = "mcp"
	ToolKindMCPSupport = "mcp_support"

	// MaxResponsesTraceBytes is the runner and decoder byte limit for one trace.
	MaxResponsesTraceBytes = 8 << 20
	// MaxEffectiveConfigBytes is the runner and decoder byte limit for config.
	MaxEffectiveConfigBytes = 1 << 20
	// MaxProviderResponseBodyBytes is the largest complete provider response
	// body whose exact bytes may be committed by a v4 trace.
	MaxProviderResponseBodyBytes = 32 << 20
	// MaxResponsesTraceRequests bounds ordered requests, attempts, and complete
	// responses in one v4 trace.
	MaxResponsesTraceRequests = 4096
	// MaxResponsesTraceSSEEvents bounds dispatched provider SSE data events.
	MaxResponsesTraceSSEEvents = 100000
	// MaxResponsesSSEEventBytes bounds one SSE event before normalized capture.
	// An event cannot validly exceed the enclosing provider response body.
	MaxResponsesSSEEventBytes = MaxProviderResponseBodyBytes

	maxTraceResponses = MaxResponsesTraceRequests
	maxTraceTools     = 4096
	maxTraceOutputs   = 100000
	maxDynamicFields  = 100000
	maxTraceSSEEvents = MaxResponsesTraceSSEEvents
)

const (
	DynamicFieldProviderCacheRouting = "provider_cache_routing_identifier"
	DynamicFieldFreshProcessNonce    = "fresh_process_nonce"
	DynamicFieldClockNonce           = "clock_nonce"

	RequestAuthorizationBearerCredential = "bearer_credential_secret"

	SSEMappingForwardedUnmapped = "forwarded_unmapped"
	SSEMappingCompletedResponse = "completed_response"
	SSEMappingStreamDone        = "stream_done"

	ResponseAttemptCompleted          = "completed"
	ResponseAttemptTransport          = "transport"
	ResponseAttemptResponseBody       = "response_body"
	ResponseAttemptResponseStatus     = "response_status"
	ResponseAttemptResponseHeaders    = "response_headers"
	ResponseAttemptSemanticValidation = "semantic_validation"
	ResponseAttemptDownstreamForward  = "downstream_forward"
)

var allowedMCPTools = []string{"changed", "find", "inspect", "outline"}

var allowedNormalizedMCPCalls = map[string]struct{}{
	"scopesifter.changed": {},
	"scopesifter.find":    {},
	"scopesifter.inspect": {},
	"scopesifter.outline": {},
}

// Codex v0.144.0 adds these provider-visible helper tools whenever at least
// one MCP server is configured. They are Codex's generic MCP resource UI, not
// resource capabilities advertised by scopesifter itself.
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
// validation can authorize only the scopesifter declaration delta.
type ResponsesTrace struct {
	SchemaVersion    string                         `json:"schema_version"`
	FirstRequest     *ResponsesRequestTrace         `json:"first_request"`
	Requests         []ResponsesRequestSnapshot     `json:"requests"`
	ResponseAttempts []ProviderResponseAttemptTrace `json:"response_attempts"`
	Responses        []ResponsesResponseTrace       `json:"responses"`
	TLSRequired      bool                           `json:"production_tls_required"`
}

// PartialResponsesTrace retains completed, sanitized response entries and
// their original request sequence numbers after an ordinary process failure.
// It never masquerades as the complete decoder artifact.
type PartialResponsesTrace struct {
	FirstRequest       *ResponsesRequestTrace         `json:"first_request"`
	SchemaVersion      string                         `json:"schema_version"`
	CaptureErrorSHA256 string                         `json:"capture_error_sha256"`
	Requests           []ResponsesRequestSnapshot     `json:"requests"`
	ResponseAttempts   []ProviderResponseAttemptTrace `json:"response_attempts"`
	Responses          []ResponsesResponseTrace       `json:"responses"`
	ResponseSequences  []int                          `json:"response_sequences"`
	RequestCount       int                            `json:"request_count"`
	TLSRequired        bool                           `json:"production_tls_required"`
}

// ResponsesRequestTrace describes the sanitized first Responses request.
// NonceNormalizedNonToolPayloadSHA256 is only a reviewed parity projection:
// it removes tools and substitutes validated nonce fields. It is never an
// exact provider-input commitment; ExactBodySHA256 and Headers carry that
// application-wire evidence.
type ResponsesRequestTrace struct {
	Model                               string                             `json:"model"`
	ExactBodySHA256                     string                             `json:"exact_body_sha256"`
	NonceNormalizedNonToolPayloadSHA256 string                             `json:"nonce_normalized_non_tool_payload_sha256"`
	Headers                             ProviderRequestHeadersTrace        `json:"headers"`
	DynamicFields                       []ProviderRequestDynamicFieldTrace `json:"dynamic_fields"`
	Tools                               []ResponsesToolDeclaration         `json:"tools"`
}

// ResponsesRequestSnapshot commits every provider request, including one for
// which transport or process failure prevented a completed response. Tools are
// represented by their canonical digest to keep the ordered list bounded.
type ResponsesRequestSnapshot struct {
	Headers                             ProviderRequestHeadersTrace        `json:"headers"`
	Model                               string                             `json:"model"`
	ExactBodySHA256                     string                             `json:"exact_body_sha256"`
	NonceNormalizedNonToolPayloadSHA256 string                             `json:"nonce_normalized_non_tool_payload_sha256"`
	ToolsSHA256                         string                             `json:"tools_sha256"`
	DynamicFields                       []ProviderRequestDynamicFieldTrace `json:"dynamic_fields"`
	Sequence                            int                                `json:"sequence"`
}

// ProviderRequestHeadersTrace commits the complete reviewed semantic envelope
// reconstructed for the upstream request. It is not a raw HTTP/2 wire-header
// capture: transport framing and the reusable Authorization value are never
// hashed or exported. Authorization presence is explicitly classified, and
// the parity projection normalizes only the validated sticky turn-state nonce.
type ProviderRequestHeadersTrace struct {
	ReviewedSemanticSHA256 string `json:"reviewed_semantic_sha256"`
	ParityProjectionSHA256 string `json:"parity_projection_sha256"`
	ContentTypeSHA256      string `json:"content_type_sha256"`
	UserAgentSHA256        string `json:"user_agent_sha256"`
	AcceptSHA256           string `json:"accept_sha256"`
	OpenAIBetaSHA256       string `json:"openai_beta_sha256"`
	TurnStateSHA256        string `json:"turn_state_sha256"`
	AuthorizationClass     string `json:"authorization_class"`
	AcceptPresent          bool   `json:"accept_present"`
	OpenAIBetaPresent      bool   `json:"openai_beta_present"`
	TurnStatePresent       bool   `json:"turn_state_present"`
}

// ProviderRequestDynamicFieldTrace preserves exact presence and value
// commitments for fields intentionally projected during pair comparison.
// Classification distinguishes behaviorally significant cache routing from
// fresh-process and clock nonces.
type ProviderRequestDynamicFieldTrace struct {
	JSONPointer    string `json:"json_pointer"`
	SHA256         string `json:"sha256"`
	Classification string `json:"classification"`
	Present        bool   `json:"present"`
}

// ProviderResponseAttemptTrace is the ordered, bounded provider outcome for
// every forwarded request. It exists even when no completed response could be
// semantically mapped. BodySHA256 commits the complete body when BodyComplete
// is true and the exact bounded prefix otherwise.
type ProviderResponseAttemptTrace struct {
	Stage          string                       `json:"stage"`
	ErrorClass     string                       `json:"error_class"`
	BodySHA256     string                       `json:"body_sha256"`
	Headers        ProviderResponseHeadersTrace `json:"headers"`
	TLSConnections []TLSConnectionTrace         `json:"tls_connections"`
	Sequence       int                          `json:"sequence"`
	StatusCode     int                          `json:"status_code"`
	BodyBytes      int                          `json:"body_bytes"`
	StatusPresent  bool                         `json:"status_present"`
	HeadersPresent bool                         `json:"headers_present"`
	BodyCaptured   bool                         `json:"body_captured"`
	BodyComplete   bool                         `json:"body_complete"`
}

// ProviderResponseHeadersTrace commits the complete reviewed semantic header
// projection without retaining provider identifiers or state. It is not a raw
// HTTP/2 wire-header capture; unconsumed response headers are intentionally
// outside this behaviorally relevant projection.
type ProviderResponseHeadersTrace struct {
	ReviewedSemanticSHA256   string `json:"reviewed_semantic_sha256"`
	ContentTypeSHA256        string `json:"content_type_sha256"`
	ContentEncodingSHA256    string `json:"content_encoding_sha256"`
	ProviderModelSHA256      string `json:"provider_model_sha256"`
	TurnStateSHA256          string `json:"turn_state_sha256"`
	ReasoningIncludedSHA256  string `json:"reasoning_included_sha256"`
	ContentTypePresent       bool   `json:"content_type_present"`
	ContentEncodingPresent   bool   `json:"content_encoding_present"`
	ProviderModelPresent     bool   `json:"provider_model_present"`
	TurnStatePresent         bool   `json:"turn_state_present"`
	ReasoningIncludedPresent bool   `json:"reasoning_included_present"`
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
	ProviderTotalTokens                        *int64                    `json:"provider_total_tokens"`
	Usage                                      *ResponsesUsageTrace      `json:"usage"`
	ProviderModelHeader                        string                    `json:"provider_model_header"`
	RequestHeadersSHA256                       string                    `json:"request_headers_sha256"`
	RequestToolsSHA256                         string                    `json:"request_tools_sha256"`
	ResponseModel                              string                    `json:"response_model"`
	TurnStateSHA256                            string                    `json:"turn_state_sha256"`
	RequestModel                               string                    `json:"request_model"`
	RequestNonceNormalizedNonToolPayloadSHA256 string                    `json:"request_nonce_normalized_non_tool_payload_sha256"`
	RequestExactBodySHA256                     string                    `json:"request_exact_body_sha256"`
	Outputs                                    []ResponsesOutputTrace    `json:"outputs"`
	TLSConnections                             []TLSConnectionTrace      `json:"tls_connections,omitempty"`
	Wire                                       ProviderResponseWireTrace `json:"wire"`
	ReasoningIncluded                          bool                      `json:"reasoning_included"`
}

// ResponsesUsageTrace is the frozen component-only usage object in Responses
// trace v4. Provider total presence belongs only to the enclosing response's
// pre-existing provider_total_tokens field.
type ResponsesUsageTrace struct {
	InputTokens           int64 `json:"input_tokens"`
	CachedInputTokens     int64 `json:"cached_input_tokens"`
	CacheWriteInputTokens int64 `json:"cache_write_input_tokens"`
	OutputTokens          int64 `json:"output_tokens"`
	ReasoningTokens       int64 `json:"reasoning_tokens"`
}

func (usage ResponsesUsageTrace) normalized() harness.Usage {
	return harness.Usage{
		InputTokens:           usage.InputTokens,
		CachedInputTokens:     usage.CachedInputTokens,
		CacheWriteInputTokens: usage.CacheWriteInputTokens,
		OutputTokens:          usage.OutputTokens,
		ReasoningTokens:       usage.ReasoningTokens,
	}
}

// ProviderResponseWireTrace commits the exact bounded bytes forwarded to the
// Codex child and the ordered SSE-to-completed-response mapping used by the
// decoder. JSON responses use an empty, non-nil SSEEvents list.
type ProviderResponseWireTrace struct {
	CompletedEventSequence  *int                     `json:"completed_event_sequence"`
	RawContentType          string                   `json:"raw_content_type"`
	MediaType               string                   `json:"media_type"`
	ExactBodySHA256         string                   `json:"exact_body_sha256"`
	CanonicalResponseSHA256 string                   `json:"canonical_response_sha256"`
	SSEEvents               []ResponsesSSEEventTrace `json:"sse_events"`
	StatusCode              int                      `json:"status_code"`
	BodyBytes               int                      `json:"body_bytes"`
}

// ResponsesSSEEventTrace retains every dispatched data event in exact order.
// Exact body framing/comments remain committed by ExactBodySHA256.
type ResponsesSSEEventTrace struct {
	Event                string `json:"event"`
	Type                 string `json:"type"`
	DataSHA256           string `json:"data_sha256"`
	CanonicalJSONSHA256  string `json:"canonical_json_sha256"`
	MappedResponseSHA256 string `json:"mapped_response_sha256"`
	Mapping              string `json:"mapping"`
	Sequence             int    `json:"sequence"`
	EventPresent         bool   `json:"event_present"`
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
	model         string
	toolCalls     []ResponsesToolCall
	toolCallNames []string
	outputs       []ResponsesOutputTrace
	usage         harness.Usage
}

// AllowedMCPTools returns the exact scopesifter tool allowlist, without the
// normalized server prefix used by ResponsesTrace.
func AllowedMCPTools() []string {
	return append([]string(nil), allowedMCPTools...)
}

// AllowedMCPSupportTools returns the exact generic resource-tool delta Codex
// v0.144.0 exposes when the candidate registers scopesifter.
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
			"codex partial Responses trace is empty or exceeds its byte limit",
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
		trace.Requests == nil || len(trace.Requests) != trace.RequestCount ||
		trace.ResponseAttempts == nil || len(trace.ResponseAttempts) != trace.RequestCount ||
		trace.Responses == nil || trace.ResponseSequences == nil ||
		len(trace.Responses) != len(trace.ResponseSequences) ||
		len(trace.Responses) > trace.RequestCount ||
		trace.CaptureErrorSHA256 != "" && !validSHA256(trace.CaptureErrorSHA256) {
		return PartialResponsesTrace{}, errors.New(
			"codex partial Responses trace envelope is invalid",
		)
	}
	if (trace.RequestCount == 0) != (trace.FirstRequest == nil) {
		return PartialResponsesTrace{}, errors.New(
			"codex partial Responses trace request identity is inconsistent",
		)
	}
	if trace.RequestCount == 0 {
		return trace, nil
	}
	if _, err := validateTraceContents(
		trace.FirstRequest,
		trace.Requests,
		trace.ResponseAttempts,
		trace.Responses,
		trace.ResponseSequences,
		trace.TLSRequired,
		false,
	); err != nil {
		return PartialResponsesTrace{}, fmt.Errorf(
			"validate Codex partial Responses trace: %w",
			err,
		)
	}
	return trace, nil
}

func decodeArtifacts(artifacts []harness.Artifact) (decodedTrace, error) {
	if err := harness.ValidateArtifacts(artifacts); err != nil {
		return decodedTrace{}, fmt.Errorf("codex artifacts: %w", err)
	}
	if len(artifacts) != 2 {
		return decodedTrace{}, fmt.Errorf(
			"codex execution requires exactly two artifacts, got %d",
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
		return decodedTrace{}, errors.New("codex Responses trace is empty or exceeds its byte limit")
	}
	if len(configRaw) == 0 || len(configRaw) > MaxEffectiveConfigBytes {
		return decodedTrace{}, errors.New("codex effective config is empty or exceeds its byte limit")
	}
	if !utf8.Valid(configRaw) || bytes.IndexByte(configRaw, 0) >= 0 {
		return decodedTrace{}, errors.New("codex effective config contains invalid text")
	}

	_, decoded, err := parseResponsesTrace(traceRaw)
	return decoded, err
}

func parseResponsesTrace(raw []byte) (ResponsesTrace, decodedTrace, error) {
	if len(raw) == 0 || len(raw) > MaxResponsesTraceBytes {
		return ResponsesTrace{}, decodedTrace{}, errors.New(
			"codex Responses trace is empty or exceeds its byte limit",
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
		return decodedTrace{}, errors.New("codex Responses trace omitted first_request")
	}
	if len(trace.Responses) == 0 || len(trace.Responses) > maxTraceResponses {
		return decodedTrace{}, errors.New("codex Responses trace has invalid response cardinality")
	}
	if trace.Requests == nil || len(trace.Requests) != len(trace.Responses) {
		return decodedTrace{}, errors.New("codex Responses trace request snapshots are incomplete")
	}
	if trace.ResponseAttempts == nil || len(trace.ResponseAttempts) != len(trace.Requests) {
		return decodedTrace{}, errors.New("codex Responses trace response attempts are incomplete")
	}
	sequences := make([]int, len(trace.Responses))
	for index := range sequences {
		sequences[index] = index
	}
	return validateTraceContents(
		trace.FirstRequest,
		trace.Requests,
		trace.ResponseAttempts,
		trace.Responses,
		sequences,
		trace.TLSRequired,
		true,
	)
}

type traceResponseValidation struct {
	firstRequest           *ResponsesRequestTrace
	declarations           map[string]ResponsesToolDeclaration
	requestToolsSHA256     string
	providerModel          string
	turnStateSHA256        string
	result                 decodedTrace
	providerTotalSum       int64
	responseCount          int
	outputCount            int
	tlsModeObserved        bool
	tlsExpected            bool
	providerTotalsComplete bool
}

func validateTraceContents(
	first *ResponsesRequestTrace,
	requests []ResponsesRequestSnapshot,
	attempts []ProviderResponseAttemptTrace,
	responses []ResponsesResponseTrace,
	responseSequences []int,
	tlsRequired bool,
	requireCompleted bool,
) (decodedTrace, error) {
	declarations, requestToolsSHA256, err := validateFirstRequest(first)
	if err != nil {
		return decodedTrace{}, err
	}
	if len(requests) == 0 || len(requests) > maxTraceResponses ||
		len(attempts) != len(requests) || len(responses) != len(responseSequences) ||
		len(responses) > len(requests) {
		return decodedTrace{}, errors.New("codex Responses trace content cardinality is invalid")
	}
	for index, request := range requests {
		if err := validateRequestSnapshot(request, index); err != nil {
			return decodedTrace{}, fmt.Errorf("codex request %d: %w", index, err)
		}
		if request.Model != first.Model {
			return decodedTrace{}, fmt.Errorf("codex request %d changed the requested model", index)
		}
		if request.ToolsSHA256 != requestToolsSHA256 {
			return decodedTrace{}, fmt.Errorf(
				"codex request %d tool commitment drifted: got %s, want %s",
				index,
				request.ToolsSHA256,
				requestToolsSHA256,
			)
		}
	}
	if err := requestTraceMatchesSnapshot(first, requests[0], requestToolsSHA256); err != nil {
		return decodedTrace{}, fmt.Errorf("codex first request: %w", err)
	}
	for index, attempt := range attempts {
		if err := validateProviderResponseAttempt(attempt, index, requireCompleted); err != nil {
			return decodedTrace{}, fmt.Errorf("codex response attempt %d: %w", index, err)
		}
	}
	if err := validateTraceTLSPolicy(tlsRequired, attempts, responses); err != nil {
		return decodedTrace{}, err
	}

	state := traceResponseValidation{
		firstRequest:           first,
		declarations:           declarations,
		requestToolsSHA256:     requestToolsSHA256,
		providerTotalsComplete: true,
		result: decodedTrace{
			model:     first.Model,
			toolCalls: make([]ResponsesToolCall, 0),
			outputs:   make([]ResponsesOutputTrace, 0),
		},
	}
	retained := make([]bool, len(requests))
	priorSequence := -1
	for responseIndex, response := range responses {
		sequence := responseSequences[responseIndex]
		if sequence <= priorSequence || sequence < 0 || sequence >= len(requests) {
			return decodedTrace{}, errors.New("codex retained response sequences are not canonical")
		}
		retained[sequence] = true
		priorSequence = sequence
		if err := state.validateResponse(
			response,
			requests[sequence],
			attempts[sequence],
			sequence,
		); err != nil {
			return decodedTrace{}, fmt.Errorf("codex response %d: %w", sequence, err)
		}
	}
	for sequence, attempt := range attempts {
		requiresResponse := attempt.Stage == ResponseAttemptCompleted ||
			attempt.Stage == ResponseAttemptDownstreamForward
		if retained[sequence] != requiresResponse {
			return decodedTrace{}, fmt.Errorf(
				"codex response %d retention differs from its provider attempt stage",
				sequence,
			)
		}
	}
	if state.responseCount != 0 && state.providerTotalsComplete {
		providerTotal := state.providerTotalSum
		state.result.usage.ProviderTotalTokens = &providerTotal
	}
	return state.result, nil
}

// validateTraceTLSPolicy makes production TLS mode explicit so removing every
// connection cannot downgrade a production trace into a local one. A provider
// transport may fail before a TLS handshake produces verified connection
// evidence; that sole exception is represented by a transport-stage attempt
// with no observed status, headers, or body.
func validateTraceTLSPolicy(
	required bool,
	attempts []ProviderResponseAttemptTrace,
	responses []ResponsesResponseTrace,
) error {
	if !required {
		for _, attempt := range attempts {
			if len(attempt.TLSConnections) != 0 {
				return errors.New("nonproduction trace contains provider TLS evidence")
			}
		}
		for _, response := range responses {
			if len(response.TLSConnections) != 0 {
				return errors.New("nonproduction response contains provider TLS evidence")
			}
		}
		return nil
	}
	for sequence, attempt := range attempts {
		if len(attempt.TLSConnections) != 0 {
			continue
		}
		preHandshakeTransportFailure := attempt.Stage == ResponseAttemptTransport &&
			!attempt.StatusPresent && !attempt.HeadersPresent && !attempt.BodyCaptured
		if !preHandshakeTransportFailure {
			return fmt.Errorf(
				"production response attempt %d omitted required TLS evidence",
				sequence,
			)
		}
	}
	for responseIndex, response := range responses {
		if len(response.TLSConnections) == 0 {
			return fmt.Errorf(
				"production response %d omitted required TLS evidence",
				responseIndex,
			)
		}
	}
	return nil
}

func validateFirstRequest(
	first *ResponsesRequestTrace,
) (map[string]ResponsesToolDeclaration, string, error) {
	if first == nil || !validText(first.Model) || first.Model == "" {
		return nil, "", errors.New("codex first request model is invalid")
	}
	if !validSHA256(first.ExactBodySHA256) ||
		!validSHA256(first.NonceNormalizedNonToolPayloadSHA256) {
		return nil, "", errors.New("codex first request body commitment is invalid")
	}
	if err := validateProviderRequestHeaders(first.Headers); err != nil {
		return nil, "", fmt.Errorf("codex first request headers: %w", err)
	}
	if err := validateDynamicFields(first.DynamicFields); err != nil {
		return nil, "", fmt.Errorf("codex first request dynamic fields: %w", err)
	}
	if first.Tools == nil || len(first.Tools) > maxTraceTools {
		return nil, "", errors.New("codex first request tool list is not canonical")
	}
	declarations := make(map[string]ResponsesToolDeclaration, len(first.Tools))
	mcpDeclarations := make(map[string]struct{})
	mcpSupportDeclarations := make(map[string]struct{})
	commandDeclarations := 0
	for index, declaration := range first.Tools {
		if !validText(declaration.Name) || declaration.Name == "" {
			return nil, "", fmt.Errorf("codex tool declaration %d has an invalid name", index)
		}
		switch declaration.WireType {
		case "function":
			if declaration.Strict == nil {
				return nil, "", fmt.Errorf("codex function declaration %q omitted strict", declaration.Name)
			}
		case "custom":
			if declaration.Strict != nil {
				return nil, "", fmt.Errorf("codex custom declaration %q contains strict", declaration.Name)
			}
		default:
			return nil, "", fmt.Errorf(
				"codex declaration %q has unsupported wire type %q",
				declaration.Name,
				declaration.WireType,
			)
		}
		if !validSHA256(declaration.DescriptionSHA256) ||
			!validSHA256(declaration.InputSchemaSHA256) {
			return nil, "", fmt.Errorf("codex tool declaration %q has an invalid digest", declaration.Name)
		}
		key := declaration.Kind + "\x00" + declaration.Name
		if _, exists := declarations[key]; exists {
			return nil, "", fmt.Errorf("duplicate Codex tool declaration %q", declaration.Name)
		}
		declarations[key] = declaration
		switch declaration.Kind {
		case ToolKindCommand:
			commandDeclarations++
		case ToolKindMCP:
			if declaration.WireType != "function" || declaration.Strict == nil || *declaration.Strict {
				return nil, "", fmt.Errorf(
					"scopesifter declaration %q has an unsupported wire shape",
					declaration.Name,
				)
			}
			if _, ok := allowedNormalizedMCPCalls[declaration.Name]; !ok {
				return nil, "", fmt.Errorf("unsupported Codex MCP tool declaration %q", declaration.Name)
			}
			mcpDeclarations[declaration.Name] = struct{}{}
		case ToolKindMCPSupport:
			if declaration.WireType != "function" || declaration.Strict == nil || *declaration.Strict {
				return nil, "", fmt.Errorf(
					"mcp support declaration %q has an unsupported wire shape",
					declaration.Name,
				)
			}
			if _, ok := allowedMCPSupportCalls[declaration.Name]; !ok {
				return nil, "", fmt.Errorf("unsupported Codex MCP support declaration %q", declaration.Name)
			}
			mcpSupportDeclarations[declaration.Name] = struct{}{}
		default:
			return nil, "", fmt.Errorf("unsupported Codex tool kind %q", declaration.Kind)
		}
	}
	if commandDeclarations == 0 {
		return nil, "", errors.New("codex first request omitted the pinned command tool")
	}
	if len(mcpDeclarations) != 0 && len(mcpDeclarations) != len(allowedNormalizedMCPCalls) {
		return nil, "", errors.New("codex first request has a partial scopesifter tool surface")
	}
	if len(mcpSupportDeclarations) != 0 &&
		len(mcpSupportDeclarations) != len(allowedMCPSupportCalls) {
		return nil, "", errors.New("codex first request has a partial MCP support tool surface")
	}
	if (len(mcpDeclarations) == 0) != (len(mcpSupportDeclarations) == 0) {
		return nil, "", errors.New("codex first request MCP and support surfaces are inconsistent")
	}
	toolsRaw, err := json.Marshal(first.Tools)
	if err != nil {
		return nil, "", fmt.Errorf("encode Codex request tool commitment: %w", err)
	}
	return declarations, digest(toolsRaw), nil
}

func (state *traceResponseValidation) validateResponse(
	response ResponsesResponseTrace,
	request ResponsesRequestSnapshot,
	attempt ProviderResponseAttemptTrace,
	sequence int,
) error {
	if err := validateResponseRequestReference(response, request); err != nil {
		return err
	}
	if response.RequestModel != state.firstRequest.Model ||
		response.RequestToolsSHA256 != state.requestToolsSHA256 {
		return errors.New("request model or tool commitment differs from first request")
	}
	if err := validateProviderResponseWire(response.Wire); err != nil {
		return fmt.Errorf("wire: %w", err)
	}
	if err := responseMatchesAttempt(response.Wire, attempt); err != nil {
		return fmt.Errorf("attempt: %w", err)
	}
	if err := state.validateResponseHeaders(response, attempt, sequence); err != nil {
		return fmt.Errorf("headers: %w", err)
	}
	if !validText(response.ResponseModel) || response.ResponseModel == "" ||
		response.ProviderModelHeader != response.ResponseModel {
		return errors.New("provider model header differs from its valid response model")
	}
	if _, ok := snapshotForProviderModel(state.firstRequest.Model, response.ResponseModel); !ok {
		return fmt.Errorf(
			"reported unsupported model resolution %q -> %q",
			state.firstRequest.Model,
			response.ResponseModel,
		)
	}
	if state.providerModel == "" {
		state.providerModel = response.ResponseModel
	} else if state.providerModel != response.ResponseModel {
		return errors.New("codex provider model changed within one turn")
	}
	if response.Usage == nil {
		return errors.New("usage is omitted")
	}
	usage := response.Usage.normalized()
	if err := harness.ValidateUsage(usage); err != nil {
		return fmt.Errorf("usage: %w", err)
	}
	if usage.CacheWriteInputTokens != 0 {
		return errors.New("codex Responses trace reported unsupported cache-write tokens")
	}
	state.responseCount++
	if response.ProviderTotalTokens != nil {
		total := *response.ProviderTotalTokens
		if total < 0 ||
			usage.InputTokens > math.MaxInt64-usage.OutputTokens ||
			total != usage.InputTokens+usage.OutputTokens {
			return errors.New("provider total_tokens is inconsistent")
		}
		if total > math.MaxInt64-state.providerTotalSum {
			return errors.New("provider total token counter overflow")
		}
		state.providerTotalSum += total
	} else {
		state.providerTotalsComplete = false
	}
	if err := addUsage(&state.result.usage, usage); err != nil {
		return fmt.Errorf("codex Responses trace usage: %w", err)
	}
	if !state.tlsModeObserved {
		state.tlsModeObserved = true
		state.tlsExpected = len(response.TLSConnections) != 0
	} else if state.tlsExpected != (len(response.TLSConnections) != 0) {
		return errors.New("codex production TLS evidence is incomplete within one arm")
	}
	for connectionIndex, connection := range response.TLSConnections {
		if err := validateTLSConnectionTrace(connection); err != nil {
			return fmt.Errorf("TLS connection %d: %w", connectionIndex, err)
		}
	}
	if !equalTLSConnectionTraces(response.TLSConnections, attempt.TLSConnections) {
		return errors.New("response TLS evidence differs from its provider attempt")
	}
	if response.Outputs == nil {
		return errors.New("outputs must be an array")
	}
	if len(response.Outputs) > maxTraceOutputs-state.outputCount {
		return errors.New("codex Responses trace exceeds its output-item limit")
	}
	state.outputCount += len(response.Outputs)
	for outputIndex, output := range response.Outputs {
		if err := state.validateOutput(output); err != nil {
			return fmt.Errorf("output %d: %w", outputIndex, err)
		}
	}
	return nil
}

func (state *traceResponseValidation) validateResponseHeaders(
	response ResponsesResponseTrace,
	attempt ProviderResponseAttemptTrace,
	sequence int,
) error {
	headers := attempt.Headers
	if !headers.ContentTypePresent || headers.ContentEncodingPresent ||
		!headers.ProviderModelPresent {
		return errors.New("required reviewed response-header presence is invalid")
	}
	contentTypeSHA256, err := canonicalStringValuesSHA256([]string{response.Wire.RawContentType})
	if err != nil || headers.ContentTypeSHA256 != contentTypeSHA256 {
		return errors.New("raw content type differs from its reviewed header commitment")
	}
	providerModelSHA256, err := canonicalStringValuesSHA256([]string{response.ProviderModelHeader})
	if err != nil || headers.ProviderModelSHA256 != providerModelSHA256 {
		return errors.New("provider model differs from its reviewed header commitment")
	}
	if headers.ReasoningIncludedPresent != response.ReasoningIncluded {
		return errors.New("reasoning-included presence differs from its semantic claim")
	}
	if !validSHA256(response.TurnStateSHA256) {
		return errors.New("turn-state commitment is invalid")
	}
	if state.turnStateSHA256 == "" {
		if sequence != 0 || !headers.TurnStatePresent ||
			response.TurnStateSHA256 != headers.TurnStateSHA256 {
			return errors.New("initial sticky turn state is not bound to its response header")
		}
		state.turnStateSHA256 = response.TurnStateSHA256
		return nil
	}
	if response.TurnStateSHA256 != state.turnStateSHA256 ||
		headers.TurnStatePresent && headers.TurnStateSHA256 != state.turnStateSHA256 {
		return errors.New("sticky turn-state commitment changed within one turn")
	}
	return nil
}

func (state *traceResponseValidation) validateOutput(output ResponsesOutputTrace) error {
	if !validSHA256(output.WireSHA256) {
		return errors.New("wire commitment is invalid")
	}
	switch output.Type {
	case OutputTypeAssistantMessage:
		if !validSHA256(output.PayloadSHA256) || output.Kind != "" || output.Name != "" {
			return errors.New("assistant output is invalid")
		}
	case OutputTypeReasoning:
		if output.PayloadSHA256 != "" && !validSHA256(output.PayloadSHA256) ||
			output.Kind != "" || output.Name != "" {
			return errors.New("reasoning output is invalid")
		}
	case OutputTypeToolCall:
		call := ResponsesToolCall{
			Kind:            output.Kind,
			Name:            output.Name,
			ArgumentsSHA256: output.PayloadSHA256,
		}
		if err := validateToolCall(state.declarations, call); err != nil {
			return err
		}
		state.result.toolCalls = append(state.result.toolCalls, call)
		state.result.toolCallNames = append(state.result.toolCallNames, call.Name)
	default:
		return fmt.Errorf("unsupported type %q", output.Type)
	}
	if output.PayloadSHA256 != "" {
		state.result.outputs = append(state.result.outputs, output)
	}
	return nil
}

func canonicalStringValuesSHA256(values []string) (string, error) {
	raw, err := json.Marshal(values)
	if err != nil {
		return "", err
	}
	return digest(raw), nil
}

func equalTLSConnectionTraces(left, right []TLSConnectionTrace) bool {
	if len(left) == 0 && len(right) == 0 {
		return true
	}
	return reflect.DeepEqual(left, right)
}

func validateProviderRequestHeaders(headers ProviderRequestHeadersTrace) error {
	for name, value := range map[string]string{
		"reviewed semantic": headers.ReviewedSemanticSHA256,
		"parity projection": headers.ParityProjectionSHA256,
		"content type":      headers.ContentTypeSHA256,
		"user agent":        headers.UserAgentSHA256,
	} {
		if !validSHA256(value) {
			return fmt.Errorf("%s commitment is invalid", name)
		}
	}
	for name, field := range map[string]struct {
		digest  string
		present bool
	}{
		"Accept":      {digest: headers.AcceptSHA256, present: headers.AcceptPresent},
		"OpenAI-Beta": {digest: headers.OpenAIBetaSHA256, present: headers.OpenAIBetaPresent},
		"turn state":  {digest: headers.TurnStateSHA256, present: headers.TurnStatePresent},
	} {
		if field.present != validSHA256(field.digest) {
			return fmt.Errorf("%s presence and commitment are inconsistent", name)
		}
	}
	if headers.AuthorizationClass != RequestAuthorizationBearerCredential {
		return errors.New("authorization classification is invalid")
	}
	return nil
}

func validateDynamicFields(fields []ProviderRequestDynamicFieldTrace) error {
	if len(fields) == 0 || len(fields) > maxDynamicFields {
		return errors.New("dynamic field list is empty or oversized")
	}
	prior := ""
	hasCacheRouting := false
	hasFreshNonce := false
	hasClockNonce := false
	hasInputTurnNonce := false
	for index, field := range fields {
		if !field.Present || !validText(field.JSONPointer) ||
			field.JSONPointer == "" || len(field.JSONPointer) > 2048 ||
			!validSHA256(field.SHA256) || index != 0 && field.JSONPointer <= prior {
			return errors.New("dynamic fields are not present, valid, ordered, and unique")
		}
		switch field.Classification {
		case DynamicFieldProviderCacheRouting:
			if field.JSONPointer != "/prompt_cache_key" {
				return errors.New("cache-routing classification is used on an unexpected field")
			}
			hasCacheRouting = true
		case DynamicFieldFreshProcessNonce:
			hasFreshNonce = true
			if strings.HasPrefix(field.JSONPointer, "/input/") &&
				strings.HasSuffix(field.JSONPointer, "/internal_chat_message_metadata_passthrough/turn_id") {
				hasInputTurnNonce = true
			}
		case DynamicFieldClockNonce:
			if field.JSONPointer != "/client_metadata/x-codex-turn-metadata/turn_started_at_unix_ms" {
				return errors.New("clock classification is used on an unexpected field")
			}
			hasClockNonce = true
		default:
			return errors.New("dynamic field classification is unsupported")
		}
		prior = field.JSONPointer
	}
	if !hasCacheRouting || !hasFreshNonce || !hasClockNonce || !hasInputTurnNonce {
		return errors.New("dynamic field classifications are incomplete")
	}
	return nil
}

func validateRequestSnapshot(request ResponsesRequestSnapshot, sequence int) error {
	if request.Sequence != sequence || !validText(request.Model) || request.Model == "" ||
		!validSHA256(request.ExactBodySHA256) ||
		!validSHA256(request.NonceNormalizedNonToolPayloadSHA256) ||
		!validSHA256(request.ToolsSHA256) {
		return errors.New("request identity or commitment is invalid")
	}
	if err := validateProviderRequestHeaders(request.Headers); err != nil {
		return fmt.Errorf("headers: %w", err)
	}
	if err := validateDynamicFields(request.DynamicFields); err != nil {
		return fmt.Errorf("dynamic fields: %w", err)
	}
	return nil
}

func requestTraceMatchesSnapshot(
	first *ResponsesRequestTrace,
	snapshot ResponsesRequestSnapshot,
	toolsSHA256 string,
) error {
	if first == nil || snapshot.Sequence != 0 || first.Model != snapshot.Model ||
		first.ExactBodySHA256 != snapshot.ExactBodySHA256 ||
		first.NonceNormalizedNonToolPayloadSHA256 != snapshot.NonceNormalizedNonToolPayloadSHA256 ||
		toolsSHA256 != snapshot.ToolsSHA256 ||
		!reflect.DeepEqual(first.Headers, snapshot.Headers) ||
		!reflect.DeepEqual(first.DynamicFields, snapshot.DynamicFields) {
		return errors.New("detailed trace differs from ordered snapshot")
	}
	return nil
}

func validateResponseRequestReference(
	response ResponsesResponseTrace,
	request ResponsesRequestSnapshot,
) error {
	if response.RequestModel != request.Model ||
		response.RequestExactBodySHA256 != request.ExactBodySHA256 ||
		response.RequestNonceNormalizedNonToolPayloadSHA256 != request.NonceNormalizedNonToolPayloadSHA256 ||
		response.RequestToolsSHA256 != request.ToolsSHA256 ||
		response.RequestHeadersSHA256 != request.Headers.ReviewedSemanticSHA256 {
		return errors.New("request reference differs from its ordered snapshot")
	}
	return nil
}

func validateProviderResponseWire(wire ProviderResponseWireTrace) error {
	if wire.StatusCode != http.StatusOK || wire.BodyBytes <= 0 ||
		wire.BodyBytes > MaxProviderResponseBodyBytes || !validSHA256(wire.ExactBodySHA256) ||
		!validSHA256(wire.CanonicalResponseSHA256) || wire.SSEEvents == nil ||
		len(wire.SSEEvents) > maxTraceSSEEvents || !validText(wire.RawContentType) ||
		wire.RawContentType == "" || len(wire.RawContentType) > 1024 {
		return errors.New("response wire envelope is invalid")
	}
	parsedMediaType, _, err := mime.ParseMediaType(wire.RawContentType)
	if err != nil || parsedMediaType != wire.MediaType {
		return errors.New("response raw content type does not map to its media type")
	}
	switch wire.MediaType {
	case "application/json":
		if len(wire.SSEEvents) != 0 || wire.CompletedEventSequence != nil {
			return errors.New("json response contains an SSE mapping")
		}
	case "text/event-stream":
		if len(wire.SSEEvents) == 0 || wire.CompletedEventSequence == nil ||
			*wire.CompletedEventSequence < 0 || *wire.CompletedEventSequence >= len(wire.SSEEvents) {
			return errors.New("sse response omitted its completed-event mapping")
		}
		completed := 0
		for index, event := range wire.SSEEvents {
			if event.Sequence != index || !validText(event.Event) || !validText(event.Type) ||
				event.Type == "" || !validSHA256(event.DataSHA256) ||
				event.EventPresent != (event.Event != "") {
				return errors.New("sse event identity is invalid")
			}
			switch event.Mapping {
			case SSEMappingForwardedUnmapped:
				if !validSHA256(event.CanonicalJSONSHA256) || event.MappedResponseSHA256 != "" {
					return errors.New("unmapped SSE event commitment is invalid")
				}
			case SSEMappingCompletedResponse:
				completed++
				if index != *wire.CompletedEventSequence || event.Type != "response.completed" ||
					!validSHA256(event.CanonicalJSONSHA256) ||
					event.MappedResponseSHA256 != wire.CanonicalResponseSHA256 {
					return errors.New("completed SSE mapping is invalid")
				}
			case SSEMappingStreamDone:
				if event.Type != "[DONE]" || event.EventPresent ||
					event.CanonicalJSONSHA256 != "" || event.MappedResponseSHA256 != "" {
					return errors.New("sse done mapping is invalid")
				}
			default:
				return errors.New("sse mapping is unsupported")
			}
		}
		if completed != 1 {
			return errors.New("sse response does not map exactly one completed response")
		}
	default:
		return errors.New("response wire media type is unsupported")
	}
	return nil
}

func validateProviderResponseAttempt(
	attempt ProviderResponseAttemptTrace,
	sequence int,
	requireCompleted bool,
) error {
	if attempt.Sequence != sequence ||
		attempt.StatusPresent != (attempt.StatusCode != 0) ||
		attempt.StatusPresent && (attempt.StatusCode < 100 || attempt.StatusCode > 599) ||
		attempt.HeadersPresent != (attempt.Headers.ReviewedSemanticSHA256 != "") ||
		attempt.BodyComplete && !attempt.BodyCaptured ||
		attempt.BodyCaptured != validSHA256(attempt.BodySHA256) ||
		!attempt.BodyCaptured && (attempt.BodyBytes != 0 || attempt.BodySHA256 != "") ||
		attempt.BodyBytes < 0 || attempt.BodyBytes > MaxProviderResponseBodyBytes+1 ||
		attempt.BodyComplete && attempt.BodyBytes > MaxProviderResponseBodyBytes ||
		attempt.TLSConnections == nil || len(attempt.TLSConnections) > 16 {
		return errors.New("response attempt envelope is invalid")
	}
	for connectionIndex, connection := range attempt.TLSConnections {
		if err := validateTLSConnectionTrace(connection); err != nil {
			return fmt.Errorf("TLS connection %d: %w", connectionIndex, err)
		}
	}
	if attempt.HeadersPresent {
		if err := validateProviderResponseHeaders(attempt.Headers); err != nil {
			return err
		}
	} else if !reflect.DeepEqual(attempt.Headers, ProviderResponseHeadersTrace{}) {
		return errors.New("absent response headers retain commitments")
	}
	validStage := false
	for _, stage := range []string{
		ResponseAttemptCompleted,
		ResponseAttemptTransport,
		ResponseAttemptResponseBody,
		ResponseAttemptResponseStatus,
		ResponseAttemptResponseHeaders,
		ResponseAttemptSemanticValidation,
		ResponseAttemptDownstreamForward,
	} {
		if attempt.Stage == stage {
			validStage = true
			break
		}
	}
	if !validStage {
		return errors.New("response attempt stage is unsupported")
	}
	if attempt.Stage == ResponseAttemptCompleted {
		if attempt.ErrorClass != "" {
			return errors.New("completed response attempt contains an error class")
		}
	} else if attempt.ErrorClass != attempt.Stage+"_failure" {
		return errors.New("failed response attempt error class is inconsistent")
	}
	observedResponse := attempt.StatusPresent && attempt.HeadersPresent
	completeBody := attempt.BodyCaptured && attempt.BodyComplete
	semanticHeaders := attempt.Headers.ContentTypePresent &&
		!attempt.Headers.ContentEncodingPresent &&
		attempt.Headers.ProviderModelPresent &&
		(sequence != 0 || attempt.Headers.TurnStatePresent)
	switch attempt.Stage {
	case ResponseAttemptTransport:
		if attempt.BodyCaptured || attempt.StatusPresent != attempt.HeadersPresent {
			return errors.New("transport-stage response attempt contains unreachable state")
		}
	case ResponseAttemptResponseBody:
		if !observedResponse || !attempt.BodyCaptured {
			return errors.New("response-body attempt omitted its status, headers, or body prefix")
		}
	case ResponseAttemptResponseStatus:
		if !observedResponse || !completeBody || attempt.StatusCode == http.StatusOK {
			return errors.New("response-status attempt does not contain a complete non-200 response")
		}
	case ResponseAttemptResponseHeaders:
		if !observedResponse || !completeBody || attempt.StatusCode != http.StatusOK {
			return errors.New("response-headers attempt omitted its complete 200 response")
		}
	case ResponseAttemptSemanticValidation:
		if !observedResponse || !completeBody || attempt.StatusCode != http.StatusOK ||
			!semanticHeaders {
			return errors.New("semantic-validation attempt omitted its validated header/body prerequisites")
		}
	case ResponseAttemptDownstreamForward, ResponseAttemptCompleted:
		if !observedResponse || !completeBody || attempt.StatusCode != http.StatusOK ||
			!semanticHeaders {
			return errors.New("completed semantic response attempt is incomplete")
		}
	}
	if requireCompleted && attempt.Stage != ResponseAttemptCompleted {
		return errors.New("complete trace contains a failed response attempt")
	}
	return nil
}

func validateProviderResponseHeaders(headers ProviderResponseHeadersTrace) error {
	if !validSHA256(headers.ReviewedSemanticSHA256) {
		return errors.New("response header aggregate commitment is invalid")
	}
	for name, field := range map[string]struct {
		digest  string
		present bool
	}{
		"content type":       {digest: headers.ContentTypeSHA256, present: headers.ContentTypePresent},
		"content encoding":   {digest: headers.ContentEncodingSHA256, present: headers.ContentEncodingPresent},
		"provider model":     {digest: headers.ProviderModelSHA256, present: headers.ProviderModelPresent},
		"turn state":         {digest: headers.TurnStateSHA256, present: headers.TurnStatePresent},
		"reasoning included": {digest: headers.ReasoningIncludedSHA256, present: headers.ReasoningIncludedPresent},
	} {
		if field.present != validSHA256(field.digest) {
			return fmt.Errorf("response %s presence and commitment are inconsistent", name)
		}
	}
	return nil
}

func responseMatchesAttempt(
	wire ProviderResponseWireTrace,
	attempt ProviderResponseAttemptTrace,
) error {
	if attempt.Stage != ResponseAttemptCompleted &&
		attempt.Stage != ResponseAttemptDownstreamForward || !attempt.BodyComplete ||
		attempt.StatusCode != wire.StatusCode || attempt.BodyBytes != wire.BodyBytes ||
		attempt.BodySHA256 != wire.ExactBodySHA256 {
		return errors.New("completed response wire differs from its provider attempt")
	}
	return nil
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
		return fmt.Errorf("codex called undeclared tool %q", call.Name)
	}
	return nil
}

func addUsage(total *harness.Usage, next harness.Usage) error {
	values := []struct {
		target *int64
		name   string
		value  int64
	}{
		{name: "input", target: &total.InputTokens, value: next.InputTokens},
		{name: "cached input", target: &total.CachedInputTokens, value: next.CachedInputTokens},
		{name: "cache-write input", target: &total.CacheWriteInputTokens, value: next.CacheWriteInputTokens},
		{name: "output", target: &total.OutputTokens, value: next.OutputTokens},
		{name: "reasoning", target: &total.ReasoningTokens, value: next.ReasoningTokens},
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
		return errors.New("json must be valid UTF-8")
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
					return errors.New("json object key is not a string")
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
		return nil, errors.New("json value has trailing content")
	}
	return json.Marshal(value)
}
