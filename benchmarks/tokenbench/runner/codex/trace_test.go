package codex

import (
	"encoding/json"
	"strings"
	"testing"

	harnesscodex "github.com/yapless/scopesifter/benchmarks/tokenbench/harness/codex"
)

func TestParseCompletedResponseCommitsOrderedJSONLPayloads(t *testing.T) {
	t.Parallel()
	strict := true
	declarations := []harnesscodex.ResponsesToolDeclaration{{
		Kind:              harnesscodex.ToolKindCommand,
		Name:              "exec_command",
		WireType:          "function",
		Strict:            &strict,
		DescriptionSHA256: strings.Repeat("1", 64),
		InputSchemaSHA256: strings.Repeat("2", 64),
	}}
	request := harnesscodex.ResponsesRequestTrace{
		Model:                               "gpt-5.4",
		ExactBodySHA256:                     strings.Repeat("4", 64),
		NonceNormalizedNonToolPayloadSHA256: strings.Repeat("3", 64),
		Headers: harnesscodex.ProviderRequestHeadersTrace{
			ReviewedSemanticSHA256: strings.Repeat("5", 64),
		},
		Tools: declarations,
	}
	providerOutputs := []any{
		map[string]any{
			"type":      "function_call",
			"name":      "exec_command",
			"arguments": `{"cmd":"rg --files"}`,
			"call_id":   "call-command",
		},
		map[string]any{
			"type": "reasoning",
			"summary": []any{
				map[string]any{"type": "summary_text", "text": "first"},
				map[string]any{"type": "summary_text", "text": "second"},
			},
		},
		map[string]any{
			"type":  "message",
			"role":  "assistant",
			"phase": nil,
			"content": []any{
				map[string]any{"type": "output_text", "text": "answer"},
				map[string]any{"type": "input_text", "text": "!"},
			},
		},
	}
	response, err := parseCompletedResponse(
		map[string]any{
			"model":  "gpt-5.4-2026-03-05",
			"output": providerOutputs,
			"usage": map[string]any{
				"input_tokens":  json.Number("1"),
				"output_tokens": json.Number("1"),
			},
		},
		request,
		"gpt-5.4-2026-03-05",
		declarations,
	)
	if err != nil {
		t.Fatal(err)
	}
	wantPayloads := []string{
		bytesDigest([]byte(`{"cmd":"rg --files"}`)),
		bytesDigest([]byte("first\nsecond")),
		bytesDigest([]byte("answer!")),
	}
	wantTypes := []string{
		harnesscodex.OutputTypeToolCall,
		harnesscodex.OutputTypeReasoning,
		harnesscodex.OutputTypeAssistantMessage,
	}
	if len(response.Outputs) != len(wantTypes) {
		t.Fatalf("outputs = %#v", response.Outputs)
	}
	for index, output := range response.Outputs {
		if output.Type != wantTypes[index] || output.PayloadSHA256 != wantPayloads[index] {
			t.Fatalf("output %d = %#v", index, output)
		}
		wire, err := canonicalJSONDigest(providerOutputs[index])
		if err != nil {
			t.Fatal(err)
		}
		if output.WireSHA256 != wire {
			t.Fatalf("output %d wire commitment = %s, want %s", index, output.WireSHA256, wire)
		}
	}
	toolsSHA256, err := canonicalJSONDigest(declarations)
	if err != nil {
		t.Fatal(err)
	}
	if response.RequestNonceNormalizedNonToolPayloadSHA256 != request.NonceNormalizedNonToolPayloadSHA256 ||
		response.RequestExactBodySHA256 != request.ExactBodySHA256 ||
		response.RequestHeadersSHA256 != request.Headers.ReviewedSemanticSHA256 ||
		response.RequestToolsSHA256 != toolsSHA256 {
		t.Fatalf("request commitments = %#v", response)
	}
	if response.ProviderTotalTokens != nil {
		t.Fatal("omitted provider total_tokens lost its absence")
	}
	if response.Usage == nil {
		t.Fatal("response usage is nil")
	}
}

func TestParseUsagePreservesPresentZeroTotalTokens(t *testing.T) {
	usage, total, err := parseUsage(map[string]any{
		"input_tokens":  json.Number("0"),
		"output_tokens": json.Number("0"),
		"total_tokens":  json.Number("0"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if usage.InputTokens != 0 || usage.OutputTokens != 0 || total == nil || *total != 0 ||
		usage.ProviderTotalTokens == nil || *usage.ProviderTotalTokens != 0 {
		t.Fatalf("usage/total = %#v, %v", usage, total)
	}
	if usage.ProviderTotalTokens == total {
		t.Fatal("normalized usage aliases the provider trace field")
	}
}

func TestParseUsageRejectsOverflowingProviderTotalEquation(t *testing.T) {
	t.Parallel()
	if _, _, err := parseUsage(map[string]any{
		"input_tokens":  json.Number("9223372036854775807"),
		"output_tokens": json.Number("1"),
		"total_tokens":  json.Number("0"),
	}); err == nil {
		t.Fatal("overflowing total_tokens equation was accepted")
	}
}

func TestCloneResponseTraceDefensivelyClonesProviderTotals(t *testing.T) {
	t.Parallel()
	providerTotal := int64(3)
	source := harnesscodex.ResponsesResponseTrace{
		ProviderTotalTokens: &providerTotal,
		Usage: &harnesscodex.ResponsesUsageTrace{
			InputTokens: 1, OutputTokens: 2,
		},
	}
	clone := cloneResponseTrace(source)
	*clone.ProviderTotalTokens = 4
	clone.Usage.InputTokens = 5
	if *source.ProviderTotalTokens != 3 || source.Usage.InputTokens != 1 {
		t.Fatalf("response trace clone aliases source: source=%+v clone=%+v", source, clone)
	}
}

func TestTLSIdentitySetIncludesFailedResponseAttempts(t *testing.T) {
	t.Parallel()
	connection := harnesscodex.TLSConnectionTrace{
		DNSName:              "api.openai.com",
		VerifiedChainsSHA256: [][]string{{strings.Repeat("a", 64)}},
		TLSVersion:           0x0304,
	}
	trace := harnesscodex.ResponsesTrace{
		ResponseAttempts: []harnesscodex.ProviderResponseAttemptTrace{{
			TLSConnections: []harnesscodex.TLSConnectionTrace{connection},
		}},
	}
	identities, err := tlsIdentitySet(trace)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := json.Marshal(connection)
	if err != nil {
		t.Fatal(err)
	}
	want := bytesDigest(raw)
	if len(identities) != 1 || identities[0] != want {
		t.Fatalf("attempt TLS identities = %q, want [%q]", identities, want)
	}
	trace.Responses = []harnesscodex.ResponsesResponseTrace{{
		TLSConnections: []harnesscodex.TLSConnectionTrace{connection},
	}}
	identities, err = tlsIdentitySet(trace)
	if err != nil {
		t.Fatal(err)
	}
	if len(identities) != 1 || identities[0] != want {
		t.Fatalf("duplicate response TLS identity was not deduplicated: %q", identities)
	}
}

func TestComparePairSnapshotsRejectsTLSModeDrift(t *testing.T) {
	t.Parallel()
	baseline := armSnapshot{trace: harnesscodex.ResponsesTrace{TLSRequired: false}}
	candidate := armSnapshot{trace: harnesscodex.ResponsesTrace{TLSRequired: true}}
	if err := comparePairSnapshots(baseline, candidate); err == nil ||
		!strings.Contains(err.Error(), "TLS modes drifted") {
		t.Fatalf("TLS mode drift error = %v", err)
	}
}

func TestParseOutputItemCommitsCanonicalMCPArguments(t *testing.T) {
	t.Parallel()
	output, err := parseOutputItem("function_call", map[string]any{
		"type":      "function_call",
		"name":      "mcp__scopesifter__inspect",
		"arguments": `{"path":"README.md","line":1}`,
		"call_id":   "call-mcp",
	})
	if err != nil {
		t.Fatal(err)
	}
	want := bytesDigest([]byte(`{"line":1,"path":"README.md"}`))
	if output.Type != harnesscodex.OutputTypeToolCall ||
		output.Kind != harnesscodex.ToolKindMCP ||
		output.Name != "scopesifter.inspect" || output.PayloadSHA256 != want {
		t.Fatalf("MCP output = %#v", output)
	}
}

func TestParseOutputItemRetainsSuppressedReasoningWireCommitment(t *testing.T) {
	t.Parallel()
	item := map[string]any{
		"type":              "reasoning",
		"summary":           []any{},
		"encrypted_content": "opaque-provider-content",
	}
	output, err := parseOutputItem("reasoning", item)
	if err != nil {
		t.Fatal(err)
	}
	wire, err := canonicalJSONDigest(item)
	if err != nil {
		t.Fatal(err)
	}
	if output.Type != harnesscodex.OutputTypeReasoning ||
		output.PayloadSHA256 != "" || output.WireSHA256 != wire {
		t.Fatalf("suppressed reasoning output = %#v", output)
	}
}

func TestParseOutputItemRejectsUnmappedProviderWire(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		typeName string
		item     map[string]any
	}{
		{
			"unknown output type",
			"computer_call",
			map[string]any{"type": "computer_call"},
		},
		{
			"disabled custom call",
			"custom_tool_call",
			map[string]any{"type": "custom_tool_call"},
		},
		{
			"non-assistant message",
			"message",
			map[string]any{
				"type":    "message",
				"role":    "user",
				"content": []any{map[string]any{"type": "output_text", "text": "x"}},
			},
		},
		{
			"unsupported message content",
			"message",
			map[string]any{
				"type":    "message",
				"role":    "assistant",
				"content": []any{map[string]any{"type": "refusal", "text": "x"}},
			},
		},
		{
			"dropped exec option",
			"function_call",
			map[string]any{
				"type":      "function_call",
				"name":      "exec_command",
				"arguments": `{"cmd":"pwd","workdir":"/tmp"}`,
				"call_id":   "call-command",
			},
		},
		{
			"unmapped command tool",
			"function_call",
			map[string]any{
				"type":      "function_call",
				"name":      "write_stdin",
				"arguments": `{}`,
				"call_id":   "call-command",
			},
		},
		{
			"invalid reasoning summary",
			"reasoning",
			map[string]any{
				"type":    "reasoning",
				"summary": []any{map[string]any{"type": "output_text", "text": "x"}},
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := parseOutputItem(test.typeName, test.item); err == nil {
				t.Fatal("parseOutputItem accepted unmapped provider output")
			}
		})
	}
}
