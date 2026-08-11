package codex

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/dkropachev/repo-view/benchmarks/tokenbench/harness"
)

func TestDecodeValidatesJSONLAgainstProviderEvidence(t *testing.T) {
	t.Parallel()
	observation, err := adapterFixture(t).Decode(context.Background(), executionFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	providerTotal := int64(15)
	want := harness.Observation{
		FinalAnswer: "The answer is 42.",
		Model:       "gpt-5.4",
		ToolCalls:   []string{"exec_command", "repo_view.inspect"},
		Usage: harness.Usage{
			InputTokens:         10,
			CachedInputTokens:   2,
			OutputTokens:        5,
			ReasoningTokens:     1,
			ProviderTotalTokens: &providerTotal,
		},
		Completed: true,
	}
	if !reflect.DeepEqual(observation, want) {
		t.Fatalf("unexpected observation:\n got: %+v\nwant: %+v", observation, want)
	}
}

func TestDecodeOmitsAggregateProviderTotalWhenAnyResponseOmitsIt(t *testing.T) {
	t.Parallel()
	execution := executionFixture(t)
	mutateTrace(t, &execution, func(trace *ResponsesTrace) {
		trace.Responses[1].ProviderTotalTokens = nil
	})
	observation, err := adapterFixture(t).Decode(context.Background(), execution)
	if err != nil {
		t.Fatal(err)
	}
	if observation.Usage.ProviderTotalTokens != nil {
		t.Fatalf("partial provider totals produced an aggregate: %+v", observation.Usage)
	}
	if observation.Usage.InputTokens != 10 || observation.Usage.OutputTokens != 5 {
		t.Fatalf("native components changed with provider-total missingness: %+v", observation.Usage)
	}
}

func TestDecodeRejectsProviderTotalDuplicatedInsideV3Usage(t *testing.T) {
	t.Parallel()
	for _, value := range []string{"null", "9"} {
		t.Run(value, func(t *testing.T) {
			t.Parallel()
			execution := executionFixture(t)
			execution.Artifacts[0].Data = replaceExactlyOnce(
				t,
				execution.Artifacts[0].Data,
				`"input_tokens": 6,`,
				`"provider_total_tokens": `+value+`, "input_tokens": 6,`,
			)
			if _, err := adapterFixture(t).Decode(context.Background(), execution); err == nil {
				t.Fatal("Responses trace v3 accepted a provider total inside its usage object")
			}
		})
	}
}

func TestPartialResponsesTraceIsStrictAndNeverDecoderInput(t *testing.T) {
	partial := PartialResponsesTrace{
		SchemaVersion:     PartialResponsesTraceSchemaVersion,
		Requests:          []ResponsesRequestSnapshot{},
		ResponseAttempts:  []ProviderResponseAttemptTrace{},
		Responses:         []ResponsesResponseTrace{},
		ResponseSequences: []int{},
		RequestCount:      0,
	}
	raw, err := json.Marshal(partial)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParsePartialResponsesTrace(raw)
	if err != nil || parsed.RequestCount != 0 || parsed.Responses == nil ||
		parsed.ResponseSequences == nil {
		t.Fatalf("ParsePartialResponsesTrace() = %#v, %v", parsed, err)
	}
	if _, err := ParseResponsesTrace(raw); err == nil {
		t.Fatal("partial trace was accepted as complete decoder evidence")
	}
	mutated := strings.Replace(
		string(raw),
		`"schema_version":`,
		`"unknown":true,"schema_version":`,
		1,
	)
	if _, err := ParsePartialResponsesTrace([]byte(mutated)); err == nil {
		t.Fatal("partial trace accepted an unknown field")
	}
}

func TestParsePartialResponsesTraceValidatesEveryRetainedResponse(t *testing.T) {
	t.Parallel()
	valid := partialResponsesFixture(t)
	raw, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParsePartialResponsesTrace(raw); err != nil {
		t.Fatalf("valid complete partial trace: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*PartialResponsesTrace)
	}{
		{"response model", func(trace *PartialResponsesTrace) {
			trace.Responses[0].ResponseModel = "gpt-5.4-2099-01-01"
			trace.Responses[0].ProviderModelHeader = "gpt-5.4-2099-01-01"
		}},
		{"usage", func(trace *PartialResponsesTrace) { trace.Responses[0].Usage = nil }},
		{"provider total", func(trace *PartialResponsesTrace) {
			total := int64(999)
			trace.Responses[0].ProviderTotalTokens = &total
		}},
		{"TLS binding", func(trace *PartialResponsesTrace) {
			trace.ResponseAttempts[0].TLSConnections = []TLSConnectionTrace{testTLSConnection()}
		}},
		{"nil outputs", func(trace *PartialResponsesTrace) { trace.Responses[0].Outputs = nil }},
		{"undeclared tool", func(trace *PartialResponsesTrace) {
			trace.Responses[0].Outputs[0].Name = "write_stdin"
		}},
		{"incomplete declaration", func(trace *PartialResponsesTrace) {
			trace.FirstRequest.Tools[0].Strict = nil
		}},
		{"partial MCP declarations", func(trace *PartialResponsesTrace) {
			trace.FirstRequest.Tools = trace.FirstRequest.Tools[:4]
		}},
		{"provider model header digest", func(trace *PartialResponsesTrace) {
			trace.ResponseAttempts[0].Headers.ProviderModelSHA256 = strings.Repeat("f", 64)
		}},
		{"sticky turn state", func(trace *PartialResponsesTrace) {
			trace.Responses[1].TurnStateSHA256 = strings.Repeat("f", 64)
		}},
		{"reasoning presence", func(trace *PartialResponsesTrace) {
			trace.Responses[0].ReasoningIncluded = false
		}},
		{"content type interpretation", func(trace *PartialResponsesTrace) {
			trace.Responses[0].Wire.RawContentType = "text/event-stream"
		}},
		{"completed response omitted", func(trace *PartialResponsesTrace) {
			trace.Responses = trace.Responses[:1]
			trace.ResponseSequences = trace.ResponseSequences[:1]
		}},
		{"failed attempt retained a response", func(trace *PartialResponsesTrace) {
			trace.ResponseAttempts[1].Stage = ResponseAttemptSemanticValidation
			trace.ResponseAttempts[1].ErrorClass = ResponseAttemptSemanticValidation + "_failure"
		}},
		{"failed attempt invalid TLS", func(trace *PartialResponsesTrace) {
			trace.Responses = trace.Responses[:1]
			trace.ResponseSequences = trace.ResponseSequences[:1]
			attempt := &trace.ResponseAttempts[1]
			attempt.Stage = ResponseAttemptSemanticValidation
			attempt.ErrorClass = ResponseAttemptSemanticValidation + "_failure"
			attempt.TLSConnections = []TLSConnectionTrace{{
				DNSName:              "attacker.example",
				VerifiedChainsSHA256: [][]string{{strings.Repeat("c", 64)}},
				TLSVersion:           0x0303,
			}}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			trace := clonePartialResponsesTrace(t, valid)
			test.mutate(&trace)
			raw, err := json.Marshal(trace)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := ParsePartialResponsesTrace(raw); err == nil {
				t.Fatal("ParsePartialResponsesTrace accepted invalid retained evidence")
			}
		})
	}
}

func TestParsePartialResponsesTraceAllowsSparseFailedAttempts(t *testing.T) {
	t.Parallel()
	partial := partialResponsesFixture(t)
	partial.Responses = partial.Responses[:1]
	partial.ResponseSequences = partial.ResponseSequences[:1]
	partial.ResponseAttempts[1].Stage = ResponseAttemptSemanticValidation
	partial.ResponseAttempts[1].ErrorClass = ResponseAttemptSemanticValidation + "_failure"
	raw, err := json.Marshal(partial)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParsePartialResponsesTrace(raw); err != nil {
		t.Fatalf("sparse failed attempt was rejected: %v", err)
	}

	partial = partialResponsesFixture(t)
	partial.Responses = partial.Responses[:1]
	partial.ResponseSequences = partial.ResponseSequences[:1]
	partial.ResponseAttempts[1].Stage = ResponseAttemptResponseBody
	partial.ResponseAttempts[1].ErrorClass = ResponseAttemptResponseBody + "_failure"
	raw, err = json.Marshal(partial)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParsePartialResponsesTrace(raw); err != nil {
		t.Fatalf("sparse response-body close failure was rejected: %v", err)
	}

	partial = partialResponsesFixture(t)
	partial.ResponseAttempts[1].Stage = ResponseAttemptDownstreamForward
	partial.ResponseAttempts[1].ErrorClass = ResponseAttemptDownstreamForward + "_failure"
	raw, err = json.Marshal(partial)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ParsePartialResponsesTrace(raw); err != nil {
		t.Fatalf("retained downstream-forward failure was rejected: %v", err)
	}
}

func TestParsePartialResponsesTraceRequiresProductionTLS(t *testing.T) {
	t.Parallel()
	partial := partialResponsesFixture(t)
	partial.TLSRequired = true
	connection := testTLSConnection()
	for index := range partial.ResponseAttempts {
		partial.ResponseAttempts[index].TLSConnections = []TLSConnectionTrace{connection}
	}
	for index := range partial.Responses {
		partial.Responses[index].TLSConnections = []TLSConnectionTrace{connection}
	}
	parsePartialFixture := func(trace PartialResponsesTrace) error {
		raw, err := json.Marshal(trace)
		if err != nil {
			return err
		}
		_, err = ParsePartialResponsesTrace(raw)
		return err
	}
	if err := parsePartialFixture(partial); err != nil {
		t.Fatalf("valid production TLS trace: %v", err)
	}

	removed := clonePartialResponsesTrace(t, partial)
	removed.ResponseAttempts[1].TLSConnections = []TLSConnectionTrace{}
	removed.Responses[1].TLSConnections = nil
	if err := parsePartialFixture(removed); err == nil {
		t.Fatal("production TLS removal was accepted")
	}

	preHandshake := clonePartialResponsesTrace(t, partial)
	preHandshake.Responses = preHandshake.Responses[:1]
	preHandshake.ResponseSequences = preHandshake.ResponseSequences[:1]
	preHandshake.ResponseAttempts[1] = ProviderResponseAttemptTrace{
		Sequence:       1,
		Stage:          ResponseAttemptTransport,
		ErrorClass:     ResponseAttemptTransport + "_failure",
		TLSConnections: []TLSConnectionTrace{},
	}
	if err := parsePartialFixture(preHandshake); err != nil {
		t.Fatalf("pre-handshake production transport failure: %v", err)
	}

	localWithTLS := partialResponsesFixture(t)
	localWithTLS.ResponseAttempts[0].TLSConnections = []TLSConnectionTrace{connection}
	if err := parsePartialFixture(localWithTLS); err == nil {
		t.Fatal("nonproduction trace accepted production TLS evidence")
	}
}

func TestProviderResponseAttemptStagesRequireReachableState(t *testing.T) {
	t.Parallel()
	completed := testCompletedAttempt("5")
	responseBody := completed
	responseBody.Stage = ResponseAttemptResponseBody
	responseBody.ErrorClass = ResponseAttemptResponseBody + "_failure"
	responseStatus := completed
	responseStatus.Stage = ResponseAttemptResponseStatus
	responseStatus.ErrorClass = ResponseAttemptResponseStatus + "_failure"
	responseStatus.StatusCode = 500
	responseHeaders := completed
	responseHeaders.Stage = ResponseAttemptResponseHeaders
	responseHeaders.ErrorClass = ResponseAttemptResponseHeaders + "_failure"
	semantic := completed
	semantic.Stage = ResponseAttemptSemanticValidation
	semantic.ErrorClass = ResponseAttemptSemanticValidation + "_failure"
	downstream := completed
	downstream.Stage = ResponseAttemptDownstreamForward
	downstream.ErrorClass = ResponseAttemptDownstreamForward + "_failure"
	transport := ProviderResponseAttemptTrace{
		Sequence:       0,
		Stage:          ResponseAttemptTransport,
		ErrorClass:     ResponseAttemptTransport + "_failure",
		TLSConnections: []TLSConnectionTrace{},
	}
	for name, attempt := range map[string]ProviderResponseAttemptTrace{
		"transport":           transport,
		"response body":       responseBody,
		"response status":     responseStatus,
		"response headers":    responseHeaders,
		"semantic validation": semantic,
		"downstream forward":  downstream,
		"completed":           completed,
	} {
		t.Run("valid "+name, func(t *testing.T) {
			t.Parallel()
			if err := validateProviderResponseAttempt(attempt, 0, false); err != nil {
				t.Fatalf("valid %s attempt: %v", name, err)
			}
		})
	}

	tests := []struct {
		name    string
		attempt ProviderResponseAttemptTrace
		mutate  func(*ProviderResponseAttemptTrace)
	}{
		{"transport with status but no headers", transport, func(value *ProviderResponseAttemptTrace) {
			value.StatusPresent = true
			value.StatusCode = 200
		}},
		{"transport with body", transport, func(value *ProviderResponseAttemptTrace) {
			value.BodyCaptured = true
			value.BodySHA256 = strings.Repeat("1", 64)
		}},
		{"response body without captured body", responseBody, func(value *ProviderResponseAttemptTrace) {
			value.BodyCaptured = false
			value.BodyComplete = false
			value.BodyBytes = 0
			value.BodySHA256 = ""
		}},
		{"response body without headers", responseBody, func(value *ProviderResponseAttemptTrace) {
			value.HeadersPresent = false
			value.Headers = ProviderResponseHeadersTrace{}
		}},
		{"response status with 200", responseStatus, func(value *ProviderResponseAttemptTrace) {
			value.StatusCode = 200
		}},
		{"response status with incomplete body", responseStatus, func(value *ProviderResponseAttemptTrace) {
			value.BodyComplete = false
		}},
		{"response headers with non-200", responseHeaders, func(value *ProviderResponseAttemptTrace) {
			value.StatusCode = 500
		}},
		{"response headers with incomplete body", responseHeaders, func(value *ProviderResponseAttemptTrace) {
			value.BodyComplete = false
		}},
		{"semantic response without content type", semantic, func(value *ProviderResponseAttemptTrace) {
			value.Headers.ContentTypePresent = false
			value.Headers.ContentTypeSHA256 = ""
		}},
		{"semantic first response without turn state", semantic, func(value *ProviderResponseAttemptTrace) {
			value.Headers.TurnStatePresent = false
			value.Headers.TurnStateSHA256 = ""
		}},
		{"semantic response with non-200", semantic, func(value *ProviderResponseAttemptTrace) {
			value.StatusCode = 500
		}},
		{"downstream response with incomplete body", downstream, func(value *ProviderResponseAttemptTrace) {
			value.BodyComplete = false
		}},
		{"completed response with non-200", completed, func(value *ProviderResponseAttemptTrace) {
			value.StatusCode = 500
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			attempt := test.attempt
			test.mutate(&attempt)
			if err := validateProviderResponseAttempt(attempt, 0, false); err == nil {
				t.Fatal("unreachable provider response attempt state was accepted")
			}
		})
	}
}

func partialResponsesFixture(t *testing.T) PartialResponsesTrace {
	t.Helper()
	complete, err := ParseResponsesTrace(readTestdata(t, "testdata/responses-trace-success.json"))
	if err != nil {
		t.Fatal(err)
	}
	return PartialResponsesTrace{
		SchemaVersion:      PartialResponsesTraceSchemaVersion,
		FirstRequest:       complete.FirstRequest,
		Requests:           complete.Requests,
		ResponseAttempts:   complete.ResponseAttempts,
		Responses:          complete.Responses,
		ResponseSequences:  []int{0, 1},
		RequestCount:       2,
		CaptureErrorSHA256: strings.Repeat("e", 64),
	}
}

func clonePartialResponsesTrace(
	t *testing.T,
	source PartialResponsesTrace,
) PartialResponsesTrace {
	t.Helper()
	raw, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	var clone PartialResponsesTrace
	if err := json.Unmarshal(raw, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func TestParseResponsesTraceUsesDecoderValidation(t *testing.T) {
	t.Parallel()
	raw := readTestdata(t, "testdata/responses-trace-success.json")
	trace, err := ParseResponsesTrace(raw)
	if err != nil {
		t.Fatal(err)
	}
	if trace.SchemaVersion != ResponsesTraceSchemaVersion || trace.FirstRequest == nil ||
		trace.FirstRequest.Model != "gpt-5.4" || len(trace.Responses) != 2 {
		t.Fatalf("unexpected parsed trace: %+v", trace)
	}
	corrupt := replaceExactlyOnce(t, raw,
		`"schema_version": "tokenbench.codex.responses-trace/v3",`,
		`"schema_version": "tokenbench.codex.responses-trace/v3", "unknown": true,`)
	if _, err := ParseResponsesTrace(corrupt); err == nil {
		t.Fatal("ParseResponsesTrace accepted an unknown field")
	}
}

func TestDecodeDoesNotRequireMCPUse(t *testing.T) {
	t.Parallel()
	providerTotal := int64(0)
	usage := ResponsesUsageTrace{}
	strict := true
	tools := []ResponsesToolDeclaration{{
		Kind:              ToolKindCommand,
		Name:              "exec_command",
		WireType:          "function",
		Strict:            &strict,
		DescriptionSHA256: strings.Repeat("1", 64),
		InputSchemaSHA256: strings.Repeat("2", 64),
	}}
	toolsJSON, err := json.Marshal(tools)
	if err != nil {
		t.Fatal(err)
	}
	trace := ResponsesTrace{
		SchemaVersion: ResponsesTraceSchemaVersion,
		FirstRequest: &ResponsesRequestTrace{
			Model:                               "gpt-5.4",
			ExactBodySHA256:                     strings.Repeat("4", 64),
			NonceNormalizedNonToolPayloadSHA256: strings.Repeat("0", 64),
			Headers:                             testRequestHeaders(false),
			DynamicFields:                       testDynamicFields(),
			Tools:                               tools,
		},
		Requests: []ResponsesRequestSnapshot{{
			Sequence:                            0,
			Model:                               "gpt-5.4",
			ExactBodySHA256:                     strings.Repeat("4", 64),
			NonceNormalizedNonToolPayloadSHA256: strings.Repeat("0", 64),
			ToolsSHA256:                         digest(toolsJSON),
			Headers:                             testRequestHeaders(false),
			DynamicFields:                       testDynamicFields(),
		}},
		ResponseAttempts: []ProviderResponseAttemptTrace{testCompletedAttempt("5")},
		Responses: []ResponsesResponseTrace{{
			ProviderTotalTokens:                        &providerTotal,
			RequestModel:                               "gpt-5.4",
			RequestExactBodySHA256:                     strings.Repeat("4", 64),
			RequestNonceNormalizedNonToolPayloadSHA256: strings.Repeat("0", 64),
			RequestHeadersSHA256:                       testRequestHeaders(false).ReviewedSemanticSHA256,
			RequestToolsSHA256:                         digest(toolsJSON),
			ResponseModel:                              "gpt-5.4-2026-03-05",
			ProviderModelHeader:                        "gpt-5.4-2026-03-05",
			TurnStateSHA256:                            strings.Repeat("1", 64),
			Outputs: []ResponsesOutputTrace{{
				Type:          OutputTypeAssistantMessage,
				WireSHA256:    strings.Repeat("3", 64),
				PayloadSHA256: digest([]byte("No tool needed.")),
			}},
			Usage:             &usage,
			ReasoningIncluded: true,
			Wire:              testJSONWire("5"),
		}},
	}
	traceJSON, err := json.Marshal(trace)
	if err != nil {
		t.Fatal(err)
	}
	execution := harness.RawExecution{
		Stdout: []byte(
			"{\"type\":\"thread.started\",\"thread_id\":\"thread-1\"}\n" +
				"{\"type\":\"turn.started\"}\n" +
				"{\"type\":\"item.completed\",\"item\":{\"id\":\"message-1\",\"type\":\"agent_message\",\"text\":\"No tool needed.\"}}\n" +
				"{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":0,\"cached_input_tokens\":0,\"output_tokens\":0,\"reasoning_output_tokens\":0}}\n",
		),
		Artifacts: []harness.Artifact{
			{Name: ResponsesTraceArtifactName, MediaType: ResponsesTraceMediaType, Data: traceJSON},
			{Name: EffectiveConfigArtifactName, MediaType: EffectiveConfigMediaType, Data: []byte("model = \"gpt-5.4\"\n")},
		},
	}
	observation, err := adapterFixture(t).Decode(context.Background(), execution)
	if err != nil {
		t.Fatal(err)
	}
	if !observation.Completed || observation.FinalAnswer != "No tool needed." ||
		observation.ToolCalls == nil || len(observation.ToolCalls) != 0 ||
		observation.Usage.ProviderTotalTokens == nil ||
		*observation.Usage.ProviderTotalTokens != 0 {
		t.Fatalf("unexpected baseline observation: %+v", observation)
	}
}

func testRequestHeaders(turnState bool) ProviderRequestHeadersTrace {
	result := ProviderRequestHeadersTrace{
		ReviewedSemanticSHA256: strings.Repeat("a", 64),
		ParityProjectionSHA256: strings.Repeat("b", 64),
		ContentTypeSHA256:      strings.Repeat("c", 64),
		UserAgentSHA256:        strings.Repeat("d", 64),
		AuthorizationClass:     RequestAuthorizationBearerCredential,
	}
	if turnState {
		result.ReviewedSemanticSHA256 = strings.Repeat("e", 64)
		result.TurnStatePresent = true
		result.TurnStateSHA256 = strings.Repeat("f", 64)
	}
	return result
}

func testDynamicFields() []ProviderRequestDynamicFieldTrace {
	return []ProviderRequestDynamicFieldTrace{
		{JSONPointer: "/client_metadata/session_id", Present: true, SHA256: strings.Repeat("1", 64), Classification: DynamicFieldFreshProcessNonce},
		{JSONPointer: "/client_metadata/x-codex-turn-metadata/turn_started_at_unix_ms", Present: true, SHA256: strings.Repeat("2", 64), Classification: DynamicFieldClockNonce},
		{JSONPointer: "/input/0/internal_chat_message_metadata_passthrough/turn_id", Present: true, SHA256: strings.Repeat("3", 64), Classification: DynamicFieldFreshProcessNonce},
		{JSONPointer: "/prompt_cache_key", Present: true, SHA256: strings.Repeat("4", 64), Classification: DynamicFieldProviderCacheRouting},
	}
}

func testJSONWire(digit string) ProviderResponseWireTrace {
	return ProviderResponseWireTrace{
		StatusCode:              200,
		RawContentType:          "application/json",
		MediaType:               "application/json",
		BodyBytes:               1,
		ExactBodySHA256:         strings.Repeat(digit, 64),
		CanonicalResponseSHA256: strings.Repeat("6", 64),
		SSEEvents:               []ResponsesSSEEventTrace{},
	}
}

func testCompletedAttempt(digit string) ProviderResponseAttemptTrace {
	return ProviderResponseAttemptTrace{
		Sequence:       0,
		Stage:          ResponseAttemptCompleted,
		StatusPresent:  true,
		StatusCode:     200,
		HeadersPresent: true,
		Headers: ProviderResponseHeadersTrace{
			ReviewedSemanticSHA256:   strings.Repeat("7", 64),
			ContentTypePresent:       true,
			ContentTypeSHA256:        testHeaderValueSHA256("application/json"),
			ProviderModelPresent:     true,
			ProviderModelSHA256:      testHeaderValueSHA256("gpt-5.4-2026-03-05"),
			TurnStatePresent:         true,
			TurnStateSHA256:          strings.Repeat("1", 64),
			ReasoningIncludedPresent: true,
			ReasoningIncludedSHA256:  strings.Repeat("b", 64),
		},
		TLSConnections: []TLSConnectionTrace{},
		BodyCaptured:   true,
		BodyComplete:   true,
		BodyBytes:      1,
		BodySHA256:     strings.Repeat(digit, 64),
	}
}

func testHeaderValueSHA256(value string) string {
	raw, err := json.Marshal([]string{value})
	if err != nil {
		panic(err)
	}
	return digest(raw)
}

func testTLSConnection() TLSConnectionTrace {
	return TLSConnectionTrace{
		DNSName:              "api.openai.com",
		VerifiedChainsSHA256: [][]string{{strings.Repeat("c", 64)}},
		TLSVersion:           0x0303,
	}
}

func TestDecodeAcceptsReportedMCPFailure(t *testing.T) {
	t.Parallel()
	execution := executionFixture(t)
	old := `"result":{"content":[],"_meta":null,"structured_content":{"text":"README"}},"error":null,"status":"completed"`
	replacement := `"result":null,"error":{"message":"tool failed"},"status":"failed"`
	execution.Stdout = replaceExactlyOnce(t, execution.Stdout, old, replacement)
	observation, err := adapterFixture(t).Decode(context.Background(), execution)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(observation.ToolCalls, []string{"exec_command", "repo_view.inspect"}) {
		t.Fatalf("failed provider-issued tool call was lost: %+v", observation)
	}
}

func TestDecodeAcceptsPinnedMCPResourceSupportCall(t *testing.T) {
	t.Parallel()
	execution := executionFixture(t)
	mutateTrace(t, &execution, func(trace *ResponsesTrace) {
		trace.Responses[0].Outputs = append(trace.Responses[0].Outputs, ResponsesOutputTrace{
			Type:          OutputTypeToolCall,
			WireSHA256:    strings.Repeat("8", 64),
			PayloadSHA256: digest([]byte("{}")),
			Kind:          ToolKindMCPSupport,
			Name:          "list_mcp_resources",
		})
	})
	started := `{"type":"item.started","item":{"id":"resource-1","type":"mcp_tool_call","server":"codex","tool":"list_mcp_resources","arguments":{},"result":null,"error":null,"status":"in_progress"}}` + "\n"
	completed := `{"type":"item.completed","item":{"id":"resource-1","type":"mcp_tool_call","server":"codex","tool":"list_mcp_resources","arguments":{},"result":{"content":[],"_meta":null,"structured_content":null},"error":null,"status":"completed"}}` + "\n"
	needle := `{"type":"item.completed","item":{"id":"mcp-1","type":"mcp_tool_call","server":"repo_view","tool":"inspect","arguments":{"line":1,"path":"README.md"},"result":{"content":[],"_meta":null,"structured_content":{"text":"README"}},"error":null,"status":"completed"}}` + "\n"
	execution.Stdout = replaceExactlyOnce(t, execution.Stdout, needle, needle+started+completed)

	observation, err := adapterFixture(t).Decode(context.Background(), execution)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"exec_command", "repo_view.inspect", "list_mcp_resources"}
	if !reflect.DeepEqual(observation.ToolCalls, want) {
		t.Fatalf("support tool calls = %q, want %q", observation.ToolCalls, want)
	}
}

func TestDecodeRejectsFailedCapture(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*harness.RawExecution)
	}{
		{"launch failed", func(value *harness.RawExecution) { value.LaunchFailed = true }},
		{"timeout", func(value *harness.RawExecution) { value.TimedOut = true }},
		{"cancelled", func(value *harness.RawExecution) { value.Cancelled = true }},
		{"stdout truncated", func(value *harness.RawExecution) { value.StdoutTruncated = true }},
		{"stderr truncated", func(value *harness.RawExecution) { value.StderrTruncated = true }},
		{"exit status", func(value *harness.RawExecution) { value.ExitCode = 1 }},
		{"stderr", func(value *harness.RawExecution) { value.Stderr = []byte("warning\n") }},
		{"empty stdout", func(value *harness.RawExecution) { value.Stdout = nil }},
		{"missing final newline", func(value *harness.RawExecution) { value.Stdout = value.Stdout[:len(value.Stdout)-1] }},
		{"invalid UTF-8", func(value *harness.RawExecution) { value.Stdout[0] = 0xff }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			execution := cloneExecution(executionFixture(t))
			test.mutate(&execution)
			if _, err := adapterFixture(t).Decode(context.Background(), execution); err == nil {
				t.Fatal("Decode accepted failed or incomplete capture")
			}
		})
	}
}

func TestDecodeRejectsInvalidArtifacts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*testing.T, *harness.RawExecution)
	}{
		{"missing", func(_ *testing.T, value *harness.RawExecution) { value.Artifacts = value.Artifacts[:1] }},
		{"extra", func(_ *testing.T, value *harness.RawExecution) {
			value.Artifacts = append(value.Artifacts, harness.Artifact{Name: "other", MediaType: "application/octet-stream"})
		}},
		{"duplicate", func(_ *testing.T, value *harness.RawExecution) { value.Artifacts[1].Name = ResponsesTraceArtifactName }},
		{"wrong trace media", func(_ *testing.T, value *harness.RawExecution) { value.Artifacts[0].MediaType = "application/json" }},
		{"wrong config media", func(_ *testing.T, value *harness.RawExecution) { value.Artifacts[1].MediaType = "text/plain" }},
		{"empty config", func(_ *testing.T, value *harness.RawExecution) { value.Artifacts[1].Data = nil }},
		{"config NUL", func(_ *testing.T, value *harness.RawExecution) { value.Artifacts[1].Data = []byte("bad\x00config") }},
		{"config invalid UTF-8", func(_ *testing.T, value *harness.RawExecution) { value.Artifacts[1].Data = []byte{0xff} }},
		{"trace duplicate key", func(t *testing.T, value *harness.RawExecution) {
			t.Helper()
			value.Artifacts[0].Data = replaceExactlyOnce(t, value.Artifacts[0].Data,
				`"schema_version": "tokenbench.codex.responses-trace/v3",`,
				`"schema_version": "tokenbench.codex.responses-trace/v3", "schema_version": "tokenbench.codex.responses-trace/v3",`)
		}},
		{"trace unknown field", func(t *testing.T, value *harness.RawExecution) {
			t.Helper()
			value.Artifacts[0].Data = replaceExactlyOnce(t, value.Artifacts[0].Data,
				`"schema_version": "tokenbench.codex.responses-trace/v3",`,
				`"schema_version": "tokenbench.codex.responses-trace/v3", "unknown": true,`)
		}},
		{"unsupported provider model", func(_ *testing.T, value *harness.RawExecution) {
			value.Artifacts[0].Data = []byte(strings.ReplaceAll(
				string(value.Artifacts[0].Data),
				"gpt-5.4-2026-03-05", "gpt-5.4-2099-01-01",
			))
		}},
		{"partial MCP surface", func(t *testing.T, value *harness.RawExecution) {
			t.Helper()
			mutateTrace(t, value, func(trace *ResponsesTrace) { trace.FirstRequest.Tools = trace.FirstRequest.Tools[:4] })
		}},
		{"undeclared call", func(t *testing.T, value *harness.RawExecution) {
			t.Helper()
			mutateTrace(t, value, func(trace *ResponsesTrace) { trace.Responses[0].Outputs[0].Name = "other_command" })
		}},
		{"nil outputs", func(t *testing.T, value *harness.RawExecution) {
			t.Helper()
			mutateTrace(t, value, func(trace *ResponsesTrace) { trace.Responses[1].Outputs = nil })
		}},
		{"request payload commitment", func(t *testing.T, value *harness.RawExecution) {
			t.Helper()
			mutateTrace(t, value, func(trace *ResponsesTrace) {
				trace.Responses[0].RequestNonceNormalizedNonToolPayloadSHA256 = strings.Repeat("f", 64)
			})
		}},
		{"request tool commitment", func(t *testing.T, value *harness.RawExecution) {
			t.Helper()
			mutateTrace(t, value, func(trace *ResponsesTrace) {
				trace.Responses[1].RequestToolsSHA256 = strings.Repeat("f", 64)
			})
		}},
		{"tool argument commitment", func(t *testing.T, value *harness.RawExecution) {
			t.Helper()
			mutateTrace(t, value, func(trace *ResponsesTrace) {
				trace.Responses[0].Outputs[1].PayloadSHA256 = strings.Repeat("f", 64)
			})
		}},
		{"assistant content commitment", func(t *testing.T, value *harness.RawExecution) {
			t.Helper()
			mutateTrace(t, value, func(trace *ResponsesTrace) {
				trace.Responses[1].Outputs[1].PayloadSHA256 = strings.Repeat("f", 64)
			})
		}},
		{"reordered provider outputs", func(t *testing.T, value *harness.RawExecution) {
			t.Helper()
			mutateTrace(t, value, func(trace *ResponsesTrace) {
				outputs := trace.Responses[0].Outputs
				outputs[0], outputs[1] = outputs[1], outputs[0]
			})
		}},
		{"cache write usage", func(t *testing.T, value *harness.RawExecution) {
			t.Helper()
			mutateTrace(t, value, func(trace *ResponsesTrace) { trace.Responses[0].Usage.CacheWriteInputTokens = 1 })
		}},
		{"provider model header digest uses raw instead of canonical values", func(t *testing.T, value *harness.RawExecution) {
			t.Helper()
			mutateTrace(t, value, func(trace *ResponsesTrace) {
				trace.ResponseAttempts[0].Headers.ProviderModelSHA256 = digest(
					[]byte(trace.Responses[0].ProviderModelHeader),
				)
			})
		}},
		{"sticky turn header commitment", func(t *testing.T, value *harness.RawExecution) {
			t.Helper()
			mutateTrace(t, value, func(trace *ResponsesTrace) {
				trace.ResponseAttempts[1].Headers.TurnStateSHA256 = strings.Repeat("f", 64)
			})
		}},
		{"sticky turn semantic commitment", func(t *testing.T, value *harness.RawExecution) {
			t.Helper()
			mutateTrace(t, value, func(trace *ResponsesTrace) {
				trace.Responses[1].TurnStateSHA256 = strings.Repeat("f", 64)
			})
		}},
		{"reasoning header presence", func(t *testing.T, value *harness.RawExecution) {
			t.Helper()
			mutateTrace(t, value, func(trace *ResponsesTrace) {
				trace.Responses[0].ReasoningIncluded = false
			})
		}},
		{"raw content type header commitment", func(t *testing.T, value *harness.RawExecution) {
			t.Helper()
			mutateTrace(t, value, func(trace *ResponsesTrace) {
				trace.Responses[0].Wire.RawContentType = "application/json; charset=utf-8"
			})
		}},
		{"raw content type media interpretation", func(t *testing.T, value *harness.RawExecution) {
			t.Helper()
			mutateTrace(t, value, func(trace *ResponsesTrace) {
				trace.Responses[0].Wire.RawContentType = "text/event-stream"
			})
		}},
		{"unexpected content encoding", func(t *testing.T, value *harness.RawExecution) {
			t.Helper()
			mutateTrace(t, value, func(trace *ResponsesTrace) {
				headers := &trace.ResponseAttempts[0].Headers
				headers.ContentEncodingPresent = true
				headers.ContentEncodingSHA256 = testHeaderValueSHA256("gzip")
			})
		}},
		{"provider total tokens", func(t *testing.T, value *harness.RawExecution) {
			t.Helper()
			mutateTrace(t, value, func(trace *ResponsesTrace) {
				total := int64(999)
				trace.Responses[0].ProviderTotalTokens = &total
			})
		}},
		{"attempt TLS differs from response", func(t *testing.T, value *harness.RawExecution) {
			t.Helper()
			mutateTrace(t, value, func(trace *ResponsesTrace) {
				trace.ResponseAttempts[0].TLSConnections = []TLSConnectionTrace{testTLSConnection()}
			})
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			execution := cloneExecution(executionFixture(t))
			test.mutate(t, &execution)
			if _, err := adapterFixture(t).Decode(context.Background(), execution); err == nil {
				t.Fatal("Decode accepted invalid artifacts")
			}
		})
	}
}

func TestDecodeRejectsInvalidJSONLLifecycleAndClaims(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mutate func(*testing.T, *harness.RawExecution)
	}{
		{"duplicate key", func(t *testing.T, value *harness.RawExecution) {
			t.Helper()
			value.Stdout = replaceExactlyOnce(t, value.Stdout,
				`{"type":"thread.started","thread_id":"thread-1"}`,
				`{"type":"thread.started","type":"thread.started","thread_id":"thread-1"}`)
		}},
		{"unknown event", func(t *testing.T, value *harness.RawExecution) {
			t.Helper()
			value.Stdout = replaceExactlyOnce(t, value.Stdout, `{"type":"turn.started"}`, `{"type":"turn.unknown"}`)
		}},
		{"unknown event field", func(t *testing.T, value *harness.RawExecution) {
			t.Helper()
			value.Stdout = replaceExactlyOnce(t, value.Stdout,
				`{"type":"turn.started"}`, `{"type":"turn.started","extra":true}`)
		}},
		{"wrong event order", func(t *testing.T, value *harness.RawExecution) {
			t.Helper()
			value.Stdout = replaceExactlyOnce(t, value.Stdout,
				`{"type":"turn.started"}`, `{"type":"thread.started","thread_id":"thread-2"}`)
		}},
		{"disabled item", func(t *testing.T, value *harness.RawExecution) {
			t.Helper()
			value.Stdout = replaceExactlyOnce(t, value.Stdout,
				`"type":"reasoning","text":"Checked the repository."`,
				`"type":"file_change","text":"Checked the repository."`)
		}},
		{"duplicate completed item", func(_ *testing.T, value *harness.RawExecution) {
			value.Stdout = []byte(strings.ReplaceAll(string(value.Stdout), "message-1", "reasoning-1"))
		}},
		{"active item at turn completion", func(t *testing.T, value *harness.RawExecution) {
			t.Helper()
			line := `{"type":"item.completed","item":{"id":"command-1","type":"command_execution","command":"/bin/zsh -c 'rg --files'","aggregated_output":"README.md\n","exit_code":0,"status":"completed"}}` + "\n"
			value.Stdout = replaceExactlyOnce(t, value.Stdout, line, "")
		}},
		{"unknown MCP result field", func(t *testing.T, value *harness.RawExecution) {
			t.Helper()
			value.Stdout = replaceExactlyOnce(t, value.Stdout,
				`"structured_content":{"text":"README"}`,
				`"structured_content":{"text":"README"},"unknown":true`)
		}},
		{"MCP identity changed", func(t *testing.T, value *harness.RawExecution) {
			t.Helper()
			value.Stdout = replaceExactlyOnce(t, value.Stdout,
				`"arguments":{"line":1,"path":"README.md"},"result":`,
				`"arguments":{"line":2,"path":"README.md"},"result":`)
		}},
		{"MCP arguments differ from provider", func(_ *testing.T, value *harness.RawExecution) {
			value.Stdout = []byte(strings.ReplaceAll(
				string(value.Stdout),
				`"path":"README.md"`,
				`"path":"OTHER.md"`,
			))
		}},
		{"command arguments differ from provider", func(_ *testing.T, value *harness.RawExecution) {
			value.Stdout = []byte(strings.ReplaceAll(
				string(value.Stdout),
				`/bin/zsh -c 'rg --files'`,
				`/bin/zsh -c 'rg --hidden'`,
			))
		}},
		{"final answer differs from provider", func(t *testing.T, value *harness.RawExecution) {
			t.Helper()
			value.Stdout = replaceExactlyOnce(
				t,
				value.Stdout,
				`"text":"The answer is 42."`,
				`"text":"The answer is 41."`,
			)
		}},
		{"JSONL tool claim differs", func(_ *testing.T, value *harness.RawExecution) {
			value.Stdout = []byte(strings.ReplaceAll(string(value.Stdout), `"tool":"inspect"`, `"tool":"outline"`))
		}},
		{"usage differs", func(t *testing.T, value *harness.RawExecution) {
			t.Helper()
			mutateTrace(t, value, func(trace *ResponsesTrace) { trace.Responses[1].Usage.OutputTokens++ })
		}},
		{"event after completion", func(_ *testing.T, value *harness.RawExecution) {
			value.Stdout = append(value.Stdout,
				[]byte(`{"type":"item.completed","item":{"id":"late","type":"agent_message","text":"late"}}`+"\n")...)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			execution := cloneExecution(executionFixture(t))
			test.mutate(t, &execution)
			if _, err := adapterFixture(t).Decode(context.Background(), execution); err == nil {
				t.Fatal("Decode accepted invalid Codex JSONL")
			}
		})
	}
}

func TestExecCommandArgumentsPinsCodexV0144Display(t *testing.T) {
	t.Parallel()
	want := `{"cmd":"rg --files"}`
	arguments, err := execCommandArguments(`/bin/zsh -c 'rg --files'`)
	if err != nil {
		t.Fatal(err)
	}
	if string(arguments) != want {
		t.Fatalf("exec arguments = %s, want %s", arguments, want)
	}
	for _, invalid := range []string{
		`rg --files`,
		`/bin/zsh -lc 'rg --files'`,
		`/bin/zsh -c "rg --files"`,
		`zsh -c 'rg --files'`,
	} {
		if _, err := execCommandArguments(invalid); err == nil {
			t.Fatalf("execCommandArguments accepted %q", invalid)
		}
	}
}

func TestDecodeHonorsCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := adapterFixture(t).Decode(ctx, executionFixture(t)); err == nil {
		t.Fatal("Decode ignored cancellation")
	}
}

func cloneExecution(source harness.RawExecution) harness.RawExecution {
	result := source
	result.Stdout = append([]byte(nil), source.Stdout...)
	result.Stderr = append([]byte(nil), source.Stderr...)
	result.Artifacts = make([]harness.Artifact, len(source.Artifacts))
	for index, artifact := range source.Artifacts {
		result.Artifacts[index] = artifact
		result.Artifacts[index].Data = append([]byte(nil), artifact.Data...)
	}
	return result
}

func mutateTrace(t *testing.T, execution *harness.RawExecution, mutate func(*ResponsesTrace)) {
	t.Helper()
	var trace ResponsesTrace
	if err := json.Unmarshal(execution.Artifacts[0].Data, &trace); err != nil {
		t.Fatal(err)
	}
	mutate(&trace)
	raw, err := json.Marshal(trace)
	if err != nil {
		t.Fatal(err)
	}
	execution.Artifacts[0].Data = raw
}

func replaceExactlyOnce(t *testing.T, source []byte, old, replacement string) []byte {
	t.Helper()
	if strings.Count(string(source), old) != 1 {
		t.Fatalf("fixture does not contain exactly one %q", old)
	}
	return []byte(strings.Replace(string(source), old, replacement, 1))
}
