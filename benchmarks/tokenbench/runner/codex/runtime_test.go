package codex

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/dkropachev/repo-view/benchmarks/tokenbench/harness"
	harnesscodex "github.com/dkropachev/repo-view/benchmarks/tokenbench/harness/codex"
	genericrunner "github.com/dkropachev/repo-view/benchmarks/tokenbench/runner"
)

const (
	fakeCredential = "real-upstream-test-credential"
	baselineConfig = "version = 1\n" +
		"codex_version = \"0.144.0\"\n" +
		"[config]\n" +
		"model = \"gpt-5.4\"\n" +
		"sandbox_mode = \"read-only\"\n" +
		"[config.mcp_servers]\n"
	candidateConfig = baselineConfig +
		"[config.mcp_servers.repo_view]\n" +
		"command = \"/repo-view\"\n" +
		"args = [\"mcp\", \"--root\", \"/source\", \"--base\", \"0000000000000000000000000000000000000000\", \"--git\", \"/usr/bin/git\", \"--git-sha256\", \"0000000000000000000000000000000000000000000000000000000000000000\"]\n" +
		"env = {}\n" +
		"environment_id = \"local\"\n" +
		"enabled = true\n" +
		"required = true\n" +
		"startup_timeout_sec = 10.0\n" +
		"tool_timeout_sec = 60.0\n" +
		"default_tools_approval_mode = \"auto\"\n" +
		"enabled_tools = [\"changed\", \"find\", \"inspect\", \"outline\"]\n" +
		"disabled_tools = []\n"
)

func TestLifecycleCapturesAndComparesOfflinePair(t *testing.T) {
	var upstreamRequests int
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		upstreamRequests++
		if request.Method != http.MethodPost || request.URL.Path != responsesPath || request.URL.RawQuery != "" {
			t.Errorf("unexpected upstream endpoint %s %s", request.Method, request.URL.String())
		}
		if got := request.Header.Get("Authorization"); got != "Bearer "+fakeCredential {
			t.Errorf("upstream Authorization = %q", got)
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Errorf("read upstream request: %v", err)
		}
		object, err := decodeJSONObject(body)
		if err != nil {
			t.Errorf("decode upstream request: %v", err)
		}
		toolCall := ""
		tools, _ := object["tools"].([]any)
		if len(tools) == 8 {
			toolCall = "mcp__repo_view__inspect"
		}
		setSuccessfulResponseHeaders(writer, "application/json")
		_, _ = writer.Write(responseJSON(toolCall))
	}))
	defer upstream.Close()

	lifecycle := newTestLifecycle(t, upstream.URL, Config{})
	layout := lifecycle.RuntimeLayout()
	if err := layout.Validate(); err != nil {
		t.Fatalf("RuntimeLayout.Validate(): %v", err)
	}
	proxyURL := layout.ProxyURL + "/responses"

	baselineArtifacts := executeArm(
		t,
		lifecycle,
		testRequest(layout, genericrunner.BaselineArm, 0),
		requestJSON(genericrunner.BaselineArm, "same prompt", "run commands"),
		[]byte(baselineConfig),
	)
	if lifecycle.RuntimeLayout().ProxyURL != layout.ProxyURL {
		t.Fatal("proxy URL changed between arms")
	}
	candidateArtifacts := executeArm(
		t,
		lifecycle,
		testRequest(layout, genericrunner.CandidateArm, 0),
		requestJSON(genericrunner.CandidateArm, "same prompt", "run commands"),
		[]byte(candidateConfig),
	)
	if upstreamRequests != 2 {
		t.Fatalf("upstream request count = %d, want 2", upstreamRequests)
	}
	if len(baselineArtifacts) != 2 || len(candidateArtifacts) != 2 {
		t.Fatalf("artifact counts = %d/%d, want 2/2", len(baselineArtifacts), len(candidateArtifacts))
	}
	if count := bytes.Count(
		candidateArtifacts[0].Data,
		[]byte(`"provider_total_tokens"`),
	); count != 1 {
		t.Fatalf("Responses trace v3 provider-total field count = %d, want 1", count)
	}
	assertArtifactContract(t, baselineArtifacts, baselineConfig)
	assertArtifactContract(t, candidateArtifacts, candidateConfig)

	candidateTrace, err := harnesscodex.ParseResponsesTrace(candidateArtifacts[0].Data)
	if err != nil {
		t.Fatalf("ParseResponsesTrace(candidate): %v", err)
	}
	if len(candidateTrace.FirstRequest.Tools) != 8 ||
		len(candidateTrace.Responses) != 1 ||
		len(candidateTrace.Responses[0].Outputs) != 2 ||
		candidateTrace.Responses[0].Outputs[1].Type != harnesscodex.OutputTypeToolCall ||
		candidateTrace.Responses[0].Outputs[1].Name != "repo_view.inspect" {
		t.Fatalf("unexpected candidate trace: %#v", candidateTrace)
	}
	if total := candidateTrace.Responses[0].ProviderTotalTokens; total == nil || *total != 14 {
		t.Fatalf("provider total_tokens presence/value = %v", total)
	}
	for _, artifact := range append(baselineArtifacts, candidateArtifacts...) {
		if bytes.Contains(artifact.Data, []byte(fakeCredential)) {
			t.Fatalf("artifact %q contains the real credential", artifact.Name)
		}
	}
	response, err := http.Post(proxyURL, "application/json", bytes.NewReader([]byte("{}")))
	if err != nil {
		t.Fatalf("call inactive proxy: %v", err)
	}
	closeResponseBody(t, response)
	if response.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated inactive proxy status = %d, want 401", response.StatusCode)
	}
}

func TestLifecycleSupportsCandidateFirst(t *testing.T) {
	upstream := newJSONUpstream(t, "")
	defer upstream.Close()
	lifecycle := newTestLifecycle(t, upstream.URL, Config{})
	layout := lifecycle.RuntimeLayout()

	executeArm(
		t,
		lifecycle,
		testRequest(layout, genericrunner.CandidateArm, 1),
		requestJSON(genericrunner.CandidateArm, "same", "command"),
		[]byte(candidateConfig),
	)
	executeArm(
		t,
		lifecycle,
		testRequest(layout, genericrunner.BaselineArm, 1),
		requestJSON(genericrunner.BaselineArm, "same", "command"),
		[]byte(baselineConfig),
	)
}

func TestLifecycleCommitsEveryResponsesRequest(t *testing.T) {
	upstream := newJSONUpstream(t, "")
	defer upstream.Close()
	lifecycle := newTestLifecycle(t, upstream.URL, Config{})
	layout := lifecycle.RuntimeLayout()
	request := testRequest(layout, genericrunner.BaselineArm, 202)
	session, err := lifecycle.BeginArm(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	first := proxyPost(t, layout, requestJSON(genericrunner.BaselineArm, "first prompt", "command"))
	closeResponseBody(t, first)
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first proxy status = %d", first.StatusCode)
	}
	second := proxyPostWithTurnState(
		t,
		layout,
		requestJSON(genericrunner.BaselineArm, "second prompt", "command"),
		"fixture-sticky-turn-state",
	)
	closeResponseBody(t, second)
	if second.StatusCode != http.StatusOK {
		t.Fatalf("second proxy status = %d", second.StatusCode)
	}
	writeConfigLock(t, layout, []byte(baselineConfig))
	artifacts, err := session.Finish(context.Background(), request, harness.RawExecution{})
	if err != nil {
		t.Fatal(err)
	}
	trace, err := harnesscodex.ParseResponsesTrace(artifacts[0].Data)
	if err != nil {
		t.Fatal(err)
	}
	if len(trace.Responses) != 2 {
		t.Fatalf("response commitments = %d, want 2", len(trace.Responses))
	}
	if trace.Responses[0].RequestNonceNormalizedNonToolPayloadSHA256 != trace.FirstRequest.NonceNormalizedNonToolPayloadSHA256 {
		t.Fatal("first response lost its first-request commitment")
	}
	if trace.Responses[1].RequestNonceNormalizedNonToolPayloadSHA256 == trace.Responses[0].RequestNonceNormalizedNonToolPayloadSHA256 {
		t.Fatal("second response reused the first request commitment")
	}
	if trace.Responses[0].RequestToolsSHA256 == "" ||
		trace.Responses[1].RequestToolsSHA256 != trace.Responses[0].RequestToolsSHA256 {
		t.Fatal("per-response tool declaration commitments are incomplete")
	}
}

func TestPinnedTreatmentSchemasMatchCodexV0144(t *testing.T) {
	declarations, err := pinnedTreatmentDeclarations()
	if err != nil {
		t.Fatal(err)
	}
	wantSupport := map[string][2]string{
		"list_mcp_resources": {
			"1aa9c29d996c24678f1b5874625e38a88ff3a55ec9045e962c45966ad0a2f819",
			"2e13a90ce5126025c0f59b1b2ccd17135f584b8fc6b40de68c05e363de7e0655",
		},
		"list_mcp_resource_templates": {
			"d526b412c36fa13f2086fc3445afc5d0551b6034149b81df611fe0981512ea07",
			"7366bd91463e8b5d749de5e1d4f2c6a28508eb445ba6b55e80e14c910a2b2704",
		},
		"read_mcp_resource": {
			"cd9ae972c351ecec00064fb69205c739bcee77f7c6569e033f0f55112c1ba576",
			"c4557459d69e290cec0ceab72c6c59da2dd0f1f05ad77f80cf0bfcf164f714a2",
		},
	}
	for name, want := range wantSupport {
		got, ok := declarations[name]
		if !ok || got.DescriptionSHA256 != want[0] || got.InputSchemaSHA256 != want[1] {
			t.Fatalf("pinned %s commitments = %+v, want %q", name, got, want)
		}
	}
	for _, spec := range pinnedTreatmentSpecs() {
		if spec.kind != harnesscodex.ToolKindMCP {
			continue
		}
		wire, err := providerVisibleTreatmentSchema(spec)
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(wire)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{
			`"default"`, `"minimum"`, `"maximum"`, `"minLength"`,
			`"maxLength"`, `"maxItems"`,
		} {
			if bytes.Contains(raw, []byte(forbidden)) {
				t.Fatalf("provider schema %s retained unsupported keyword %s", spec.name, forbidden)
			}
		}
	}
}

func TestResolveLimitsRejectsTraceV3EvidenceOverflow(t *testing.T) {
	maximum := Config{
		MaxResponseBytes: harnesscodex.MaxProviderResponseBodyBytes,
		MaxRequests:      harnesscodex.MaxResponsesTraceRequests,
		MaxEvents:        harnesscodex.MaxResponsesTraceSSEEvents,
		MaxSSEEventBytes: harnesscodex.MaxResponsesSSEEventBytes,
	}
	if _, err := resolveLimits(maximum); err != nil {
		t.Fatalf("resolveLimits rejected exact evidence bounds: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*Config)
	}{
		{"response body", func(config *Config) { config.MaxResponseBytes++ }},
		{"request count", func(config *Config) { config.MaxRequests++ }},
		{"SSE event count", func(config *Config) { config.MaxEvents++ }},
		{"SSE event bytes", func(config *Config) { config.MaxSSEEventBytes++ }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			config := maximum
			test.mutate(&config)
			if _, err := resolveLimits(config); err == nil ||
				!strings.Contains(err.Error(), "trace v3 evidence bounds") {
				t.Fatalf("resolveLimits() = %v, want evidence-bound rejection", err)
			}
		})
	}
}

func TestLifecycleCapturesBoundedSSE(t *testing.T) {
	completed, err := json.Marshal(map[string]any{
		"type":     "response.completed",
		"response": responseObject(""),
	})
	if err != nil {
		t.Fatal(err)
	}
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		setSuccessfulResponseHeaders(writer, "text/event-stream")
		if _, err := fmt.Fprintf(writer, "event: response.created\ndata: {\"type\":\"response.created\"}\n\n"); err != nil {
			t.Errorf("write response.created event: %v", err)
		}
		if _, err := fmt.Fprintf(writer, "event: response.completed\ndata: %s\n\n", completed); err != nil {
			t.Errorf("write response.completed event: %v", err)
		}
		if _, err := fmt.Fprint(writer, "data: [DONE]\n\n"); err != nil {
			t.Errorf("write response terminator: %v", err)
		}
	}))
	defer upstream.Close()
	lifecycle := newTestLifecycle(t, upstream.URL, Config{MaxEvents: 4, MaxSSEEventBytes: 4096})
	layout := lifecycle.RuntimeLayout()
	artifacts := executeArm(
		t,
		lifecycle,
		testRequest(layout, genericrunner.BaselineArm, 2),
		requestJSON(genericrunner.BaselineArm, "sse", "command"),
		[]byte(baselineConfig),
	)
	trace, err := harnesscodex.ParseResponsesTrace(artifacts[0].Data)
	if err != nil || len(trace.Responses) != 1 {
		t.Fatalf("SSE trace = %#v, error %v", trace, err)
	}
	wire := trace.Responses[0].Wire
	if wire.MediaType != "text/event-stream" || len(wire.SSEEvents) != 3 ||
		wire.CompletedEventSequence == nil || *wire.CompletedEventSequence != 1 ||
		wire.SSEEvents[0].Mapping != harnesscodex.SSEMappingForwardedUnmapped ||
		wire.SSEEvents[1].Mapping != harnesscodex.SSEMappingCompletedResponse ||
		wire.SSEEvents[2].Mapping != harnesscodex.SSEMappingStreamDone {
		t.Fatalf("ordered SSE wire evidence = %#v", wire)
	}
}

func TestIntermediateSSEAndRequestHeadersChangeExactEvidence(t *testing.T) {
	completed, err := json.Marshal(map[string]any{
		"type":     "response.completed",
		"response": responseObject(""),
	})
	if err != nil {
		t.Fatal(err)
	}
	request, declarations, err := parseResponsesRequest(
		requestJSON(genericrunner.BaselineArm, "same", "command"),
		testRequest(RuntimeLayout{}, genericrunner.BaselineArm, 0),
	)
	if err != nil {
		t.Fatal(err)
	}
	parse := func(marker int) harnesscodex.ResponsesResponseTrace {
		body := []byte(fmt.Sprintf(
			"event: response.created\ndata: {\"type\":\"response.created\",\"marker\":%d}\n\n"+
				"event: response.completed\ndata: %s\n\ndata: [DONE]\n\n",
			marker, completed,
		))
		trace, err := parseResponsesBody(
			body, "text/event-stream", request, "gpt-5.4-2026-03-05",
			declarations, 4096, 4,
		)
		if err != nil {
			t.Fatal(err)
		}
		return trace
	}
	first, second := parse(1), parse(2)
	if !reflect.DeepEqual(first.Outputs, second.Outputs) ||
		first.Wire.ExactBodySHA256 == second.Wire.ExactBodySHA256 ||
		first.Wire.SSEEvents[0].DataSHA256 == second.Wire.SSEEvents[0].DataSHA256 {
		t.Fatal("intermediate SSE mutation did not alter exact ordered wire evidence")
	}

	base, err := providerRequestHeadersTrace(100, "application/json", true, "", false, "")
	if err != nil {
		t.Fatal(err)
	}
	acceptDrift, _ := providerRequestHeadersTrace(100, "text/event-stream", true, "", false, "")
	betaDrift, _ := providerRequestHeadersTrace(100, "application/json", true, "responses=v2", true, "")
	if base.ExactApplicationSHA256 == acceptDrift.ExactApplicationSHA256 ||
		base.ParityProjectionSHA256 == acceptDrift.ParityProjectionSHA256 ||
		base.ExactApplicationSHA256 == betaDrift.ExactApplicationSHA256 ||
		base.ParityProjectionSHA256 == betaDrift.ParityProjectionSHA256 {
		t.Fatal("Accept/OpenAI-Beta mutation did not alter request-header evidence")
	}
	duplicate := http.Header{"Accept": {"application/json", "text/event-stream"}}
	if _, _, err := reviewedOptionalRequestHeader(duplicate, "Accept"); err == nil {
		t.Fatal("duplicate forwarded Accept header was silently collapsed")
	}
}

func TestProxyRejectsEndpointModelToolAndBodyDrift(t *testing.T) {
	tests := []struct {
		name   string
		config Config
		issue  func(*testing.T, RuntimeLayout) *http.Response
	}{
		{
			name: "endpoint",
			issue: func(t *testing.T, layout RuntimeLayout) *http.Response {
				t.Helper()
				request, err := http.NewRequest(http.MethodPost, layout.ProxyURL+"/chat/completions", strings.NewReader("{}"))
				if err != nil {
					t.Fatal(err)
				}
				request.Header.Set("Authorization", "Bearer "+layout.LocalProxyCapability)
				request.Header.Set("Content-Type", "application/json")
				return doRequest(t, request)
			},
		},
		{
			name: "model",
			issue: func(t *testing.T, layout RuntimeLayout) *http.Response {
				t.Helper()
				body := requestJSON(genericrunner.BaselineArm, "drift", "command")
				var object map[string]any
				if err := json.Unmarshal(body, &object); err != nil {
					t.Fatal(err)
				}
				object["model"] = "gpt-5.6"
				body, _ = json.Marshal(object)
				return proxyPost(t, layout, body)
			},
		},
		{
			name:   "request body limit",
			config: Config{MaxRequestBytes: 32},
			issue: func(t *testing.T, layout RuntimeLayout) *http.Response {
				t.Helper()
				return proxyPost(t, layout, bytes.Repeat([]byte("x"), 64))
			},
		},
		{
			name: "missing candidate tool",
			issue: func(t *testing.T, layout RuntimeLayout) *http.Response {
				t.Helper()
				body := requestJSON(genericrunner.CandidateArm, "drift", "command")
				var object map[string]any
				if err := json.Unmarshal(body, &object); err != nil {
					t.Fatal(err)
				}
				tools := object["tools"].([]any)
				object["tools"] = tools[:len(tools)-1]
				body, _ = json.Marshal(object)
				return proxyPost(t, layout, body)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			upstream := newJSONUpstream(t, "")
			defer upstream.Close()
			lifecycle := newTestLifecycle(t, upstream.URL, test.config)
			layout := lifecycle.RuntimeLayout()
			arm := genericrunner.BaselineArm
			if test.name == "missing candidate tool" {
				arm = genericrunner.CandidateArm
			}
			session, err := lifecycle.BeginArm(context.Background(), testRequest(layout, arm, 3))
			if err != nil {
				t.Fatalf("BeginArm(): %v", err)
			}
			response := test.issue(t, layout)
			closeResponseBody(t, response)
			if response.StatusCode == http.StatusOK {
				t.Fatalf("proxy accepted %s drift", test.name)
			}
			if _, err := session.Finish(context.Background(), testRequest(layout, arm, 3), harness.RawExecution{}); err == nil {
				t.Fatalf("Finish() accepted %s drift", test.name)
			}
		})
	}
}

func TestProxyRejectsIntraArmToolDrift(t *testing.T) {
	upstream := newJSONUpstream(t, "")
	defer upstream.Close()
	lifecycle := newTestLifecycle(t, upstream.URL, Config{})
	layout := lifecycle.RuntimeLayout()
	request := testRequest(layout, genericrunner.BaselineArm, 4)
	session, err := lifecycle.BeginArm(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	first := proxyPost(t, layout, requestJSON(genericrunner.BaselineArm, "first", "command-v1"))
	closeResponseBody(t, first)
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first proxy status = %d", first.StatusCode)
	}
	second := proxyPost(t, layout, requestJSON(genericrunner.BaselineArm, "second", "command-v2"))
	closeResponseBody(t, second)
	if second.StatusCode == http.StatusOK {
		t.Fatal("proxy accepted tool schema drift")
	}
	if _, err := session.Finish(context.Background(), request, harness.RawExecution{}); err == nil {
		t.Fatal("Finish() accepted intra-arm tool drift")
	}
}

func TestPairComparisonRejectsPayloadAndConfigDrift(t *testing.T) {
	tests := []struct {
		name            string
		candidatePrompt string
		candidateConfig string
	}{
		{"non-tool request", "different", candidateConfig},
		{"effective config", "same", strings.Replace(candidateConfig, "read-only", "workspace-write", 1)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			upstream := newJSONUpstream(t, "")
			defer upstream.Close()
			lifecycle := newTestLifecycle(t, upstream.URL, Config{})
			layout := lifecycle.RuntimeLayout()
			executeArm(
				t,
				lifecycle,
				testRequest(layout, genericrunner.BaselineArm, 5),
				requestJSON(genericrunner.BaselineArm, "same", "command"),
				[]byte(baselineConfig),
			)
			request := testRequest(layout, genericrunner.CandidateArm, 5)
			session, err := lifecycle.BeginArm(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			response := proxyPost(
				t,
				layout,
				requestJSON(genericrunner.CandidateArm, test.candidatePrompt, "command"),
			)
			closeResponseBody(t, response)
			if response.StatusCode != http.StatusOK {
				t.Fatalf("proxy status = %d", response.StatusCode)
			}
			writeConfigLock(t, layout, []byte(test.candidateConfig))
			if _, err := session.Finish(context.Background(), request, harness.RawExecution{}); err == nil {
				t.Fatalf("Finish() accepted paired %s drift", test.name)
			}
		})
	}
}

func TestWorkspaceMetadataRemainsInNonToolCommitment(t *testing.T) {
	baseline, _, err := parseResponsesRequest(
		requestJSON(genericrunner.BaselineArm, "same", "command"),
		testRequest(RuntimeLayout{}, genericrunner.BaselineArm, 0),
	)
	if err != nil {
		t.Fatal(err)
	}
	candidateRaw := requestJSON(genericrunner.CandidateArm, "same", "command")
	candidate, _, err := parseResponsesRequest(
		candidateRaw,
		testRequest(RuntimeLayout{}, genericrunner.CandidateArm, 0),
	)
	if err != nil {
		t.Fatal(err)
	}
	if baseline.NonceNormalizedNonToolPayloadSHA256 != candidate.NonceNormalizedNonToolPayloadSHA256 {
		t.Fatal("fresh request identifiers changed the non-tool commitment")
	}
	if baseline.ExactBodySHA256 == candidate.ExactBodySHA256 ||
		reflect.DeepEqual(baseline.DynamicFields, candidate.DynamicFields) {
		t.Fatal("exact provider identifiers were lost behind the nonce-normalized parity projection")
	}
	if field := candidate.DynamicFields[len(candidate.DynamicFields)-1]; field.JSONPointer != "/prompt_cache_key" ||
		field.Classification != harnesscodex.DynamicFieldProviderCacheRouting ||
		len(field.SHA256) != 64 {
		t.Fatalf("prompt cache routing commitment = %#v", field)
	}

	for _, test := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{"remote URL", func(metadata map[string]any) {
			workspace := metadata["workspaces"].(map[string]any)["/source"].(map[string]any)
			workspace["associated_remote_urls"].(map[string]any)["origin"] =
				"https://mirror.invalid/repo.git"
		}},
		{"remote presence", func(metadata map[string]any) {
			workspace := metadata["workspaces"].(map[string]any)["/source"].(map[string]any)
			delete(workspace, "associated_remote_urls")
		}},
		{"workspace presence", func(metadata map[string]any) {
			delete(metadata, "workspaces")
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			mutated := mutateTurnMetadata(t, candidateRaw, test.mutate)
			trace, _, err := parseResponsesRequest(
				mutated,
				testRequest(RuntimeLayout{}, genericrunner.CandidateArm, 0),
			)
			if err != nil {
				t.Fatalf("valid workspace mutation was rejected: %v", err)
			}
			if trace.NonceNormalizedNonToolPayloadSHA256 == baseline.NonceNormalizedNonToolPayloadSHA256 {
				t.Fatal("workspace drift was normalized out of the non-tool commitment")
			}
		})
	}
}

func TestEffectiveConfigEnvelopeAndRepoViewShapeAreExact(t *testing.T) {
	t.Parallel()
	registration := testRequest(
		RuntimeLayout{},
		genericrunner.CandidateArm,
		0,
	).Invocation.MCPServers[0]
	if _, delta, err := normalizeEffectiveConfig(
		[]byte(candidateConfig),
		genericrunner.CandidateArm,
		&registration,
	); err != nil || len(delta) == 0 {
		t.Fatalf("valid candidate config was rejected: delta=%q err=%v", delta, err)
	}
	reorderedEnvelope := strings.Replace(
		baselineConfig,
		"version = 1\ncodex_version = \"0.144.0\"\n",
		"codex_version = \"0.144.0\"\nversion = 1\n",
		1,
	)
	if _, delta, err := normalizeEffectiveConfig(
		[]byte(reorderedEnvelope),
		genericrunner.BaselineArm,
		nil,
	); err != nil || len(delta) != 0 {
		t.Fatalf("semantically reordered baseline config was rejected: %v", err)
	}

	for _, test := range []struct {
		name string
		raw  string
		arm  genericrunner.Arm
		reg  *harness.MCPServer
	}{
		{"wrong envelope version", strings.Replace(baselineConfig, "version = 1", "version = 2", 1), genericrunner.BaselineArm, nil},
		{"wrong Codex version", strings.Replace(baselineConfig, "0.144.0", "0.145.0", 1), genericrunner.BaselineArm, nil},
		{"extra envelope field", "extra = true\n" + baselineConfig, genericrunner.BaselineArm, nil},
		{"old top-level registry", strings.Replace(baselineConfig, "[config.mcp_servers]", "[mcp_servers]", 1), genericrunner.BaselineArm, nil},
		{"baseline treatment", candidateConfig, genericrunner.BaselineArm, nil},
		{"candidate omission", baselineConfig, genericrunner.CandidateArm, &registration},
		{"extra repo field", candidateConfig + "unexpected = true\n", genericrunner.CandidateArm, &registration},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, _, err := normalizeEffectiveConfig(
				[]byte(test.raw),
				test.arm,
				test.reg,
			); err == nil {
				t.Fatal("invalid effective config was accepted")
			}
		})
	}
}

func TestCredentialIsProxyOnlyAndIdentityIsSecretIndependent(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Authorization") != "Bearer "+fakeCredential {
			t.Error("real credential did not reach fake upstream")
		}
		setSuccessfulResponseHeaders(writer, "application/json")
		_, _ = writer.Write(responseJSON(""))
	}))
	defer upstream.Close()
	lifecycle := newTestLifecycle(t, upstream.URL, Config{})
	if strings.Contains(lifecycle.Identity(), fakeCredential) {
		t.Fatal("lifecycle identity contains the real credential")
	}
	layout := lifecycle.RuntimeLayout()
	if values := layout.Environment(); values["CODEX_API_KEY"] != layout.LocalProxyCapability ||
		values["PATH"] != layout.ToolboxRoot ||
		strings.Contains(fmt.Sprint(values), fakeCredential) {
		t.Fatalf("child environment contains the wrong credential: %#v", values)
	}
	resolved, err := resolveConfig(Config{
		StateRoot:          filepath.Dir(layout.Home),
		ToolboxRoot:        layout.ToolboxRoot,
		ListenAddress:      "127.0.0.1:1",
		UpstreamURL:        upstream.URL,
		UpstreamCredential: "different-real-credential",
	})
	if err != nil {
		t.Fatal(err)
	}
	identity, err := lifecycleIdentity(layout, resolved.upstreamURL, lifecycle.limits, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if identity != lifecycle.Identity() {
		t.Fatal("real credential changed lifecycle identity")
	}
	changedLayout := layout
	changedLayout.ToolboxRoot = filepath.Join(t.TempDir(), "other-toolbox")
	changedIdentity, err := lifecycleIdentity(changedLayout, resolved.upstreamURL, lifecycle.limits, "", "")
	if err != nil {
		t.Fatal(err)
	}
	if changedIdentity == lifecycle.Identity() {
		t.Fatal("toolbox root was absent from lifecycle identity")
	}
}

func TestConfigAndRequestsRequireExactToolboxPATH(t *testing.T) {
	stateRoot := filepath.Join(t.TempDir(), "runtime")
	toolboxRoot := filepath.Join(t.TempDir(), "toolbox")
	base := Config{
		StateRoot:          stateRoot,
		ToolboxRoot:        toolboxRoot,
		UpstreamURL:        "https://example.invalid",
		UpstreamCredential: fakeCredential,
	}
	mutations := []struct {
		name   string
		mutate func(*Config)
	}{
		{"missing", func(config *Config) { config.ToolboxRoot = "" }},
		{"relative", func(config *Config) { config.ToolboxRoot = "toolbox" }},
		{"unclean", func(config *Config) { config.ToolboxRoot = toolboxRoot + "/../toolbox" }},
		{"PATH list", func(config *Config) { config.ToolboxRoot = toolboxRoot + string(os.PathListSeparator) + "/usr/bin" }},
		{"inside state", func(config *Config) { config.ToolboxRoot = filepath.Join(stateRoot, "toolbox") }},
		{"contains state", func(config *Config) { config.ToolboxRoot = filepath.Dir(stateRoot) }},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			config := base
			test.mutate(&config)
			if _, err := resolveConfig(config); err == nil {
				t.Fatal("resolveConfig accepted an invalid toolbox root")
			}
		})
	}

	upstream := newJSONUpstream(t, "")
	defer upstream.Close()
	t.Setenv("PATH", "/ambient/attacker-bin")
	lifecycle := newTestLifecycle(t, upstream.URL, Config{})
	layout := lifecycle.RuntimeLayout()
	if layout.Environment()["PATH"] != layout.ToolboxRoot ||
		layout.Environment()["PATH"] == os.Getenv("PATH") {
		t.Fatal("lifecycle inherited ambient PATH")
	}
	for _, mutate := range []func(*genericrunner.ExecutionRequest){
		func(request *genericrunner.ExecutionRequest) {
			request.Invocation.Environment["PATH"] = "/ambient/attacker-bin"
		},
		func(request *genericrunner.ExecutionRequest) {
			request.Process.Environment["PATH"] = "/ambient/attacker-bin"
		},
	} {
		request := testRequest(layout, genericrunner.BaselineArm, 90)
		mutate(&request)
		if _, err := lifecycle.BeginArm(context.Background(), request); err == nil {
			t.Fatal("BeginArm accepted PATH that differs from the toolbox root")
		}
	}
}

func TestRuntimeLayoutToolboxContractMatchesAdapter(t *testing.T) {
	layout := RuntimeLayout{
		ProxyURL:             "http://127.0.0.1:43119/v1",
		Home:                 "/tokenbench/home",
		CodexHome:            "/tokenbench/codex-home",
		Temp:                 "/tokenbench/tmp",
		ConfigLock:           "/tokenbench/config-lock",
		ToolboxRoot:          "/snapshot/toolbox",
		LocalProxyCapability: harness.OfflineLocalProxyCapability,
	}
	adapterLayout := harnesscodex.RuntimeLayout{
		ProxyURL:             layout.ProxyURL,
		Home:                 layout.Home,
		CodexHome:            layout.CodexHome,
		Temp:                 layout.Temp,
		ConfigLock:           layout.ConfigLock,
		ToolboxRoot:          layout.ToolboxRoot,
		LocalProxyCapability: layout.LocalProxyCapability,
	}
	if err := layout.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := adapterLayout.Validate(); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(layout.Environment(), adapterLayout.Environment()) ||
		!reflect.DeepEqual(layout.ConfigAssignments(), adapterLayout.ConfigAssignments()) {
		t.Fatal("runner and adapter toolbox launch contracts diverged")
	}
	runnerCommitment, err := layout.Commitment()
	if err != nil {
		t.Fatal(err)
	}
	adapterCommitment, err := adapterLayout.Commitment()
	if err != nil {
		t.Fatal(err)
	}
	if runnerCommitment != adapterCommitment {
		t.Fatalf("layout commitments differ: runner=%s adapter=%s", runnerCommitment, adapterCommitment)
	}
}

func TestLifecycleUsesFreshCommonCapabilityAndExpiresItBeforePublication(t *testing.T) {
	upstream := newJSONUpstream(t, "")
	defer upstream.Close()
	first := newTestLifecycle(t, upstream.URL, Config{})
	second := newTestLifecycle(t, upstream.URL, Config{})
	firstLayout := first.RuntimeLayout()
	secondLayout := second.RuntimeLayout()
	if !harness.ValidLocalProxyCapability(firstLayout.LocalProxyCapability) ||
		firstLayout.LocalProxyCapability == harness.OfflineLocalProxyCapability ||
		firstLayout.LocalProxyCapability == fakeCredential ||
		firstLayout.LocalProxyCapability == secondLayout.LocalProxyCapability {
		t.Fatalf(
			"local capabilities are not fresh and distinct: %q %q",
			firstLayout.LocalProxyCapability,
			secondLayout.LocalProxyCapability,
		)
	}
	if _, err := resolveConfig(Config{
		StateRoot:          filepath.Dir(firstLayout.Home),
		ToolboxRoot:        firstLayout.ToolboxRoot,
		ListenAddress:      "127.0.0.1:1",
		UpstreamURL:        upstream.URL,
		UpstreamCredential: harness.OfflineLocalProxyCapability,
	}); err == nil {
		t.Fatal("upstream credential accepted the local capability namespace")
	}
	baseline := testRequest(firstLayout, genericrunner.BaselineArm, 31)
	candidate := testRequest(firstLayout, genericrunner.CandidateArm, 31)
	if baseline.Process.Environment["CODEX_API_KEY"] != firstLayout.LocalProxyCapability ||
		candidate.Process.Environment["CODEX_API_KEY"] != firstLayout.LocalProxyCapability ||
		!reflect.DeepEqual(baseline.Process.Environment, candidate.Process.Environment) {
		t.Fatal("paired arms did not commit one byte-identical local capability")
	}
	if first.PublicationBoundaryClosed() {
		t.Fatal("live listener reported an expired publication capability")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := first.Close(ctx); err != nil {
		t.Fatal(err)
	}
	if !first.PublicationBoundaryClosed() {
		t.Fatal("successful lifecycle Close did not expire the local capability")
	}
}

func TestLifecycleClosesReusedIdleUpstreamBeforePublicationBoundary(t *testing.T) {
	const wantLifecycleVersion = "tokenbench.codex-runner/codex-cli-v0.144.0/v2"
	if lifecycleVersion != wantLifecycleVersion ||
		stateMarkerName != ".tokenbench-codex-runtime-v2" ||
		stateMarkerData != wantLifecycleVersion+"\n" {
		t.Fatalf(
			"lifecycle version/marker = %q %q %q",
			lifecycleVersion,
			stateMarkerName,
			stateMarkerData,
		)
	}
	idleConnections := make(chan net.Conn, 2)
	closedConnections := make(chan net.Conn, 1)
	upstream := httptest.NewUnstartedServer(http.HandlerFunc(func(
		writer http.ResponseWriter,
		request *http.Request,
	) {
		if got := request.Header.Get("Authorization"); got != "Bearer "+fakeCredential {
			t.Errorf("upstream Authorization = %q", got)
		}
		if got := request.UserAgent(); got != wantLifecycleVersion {
			t.Errorf("upstream User-Agent = %q", got)
		}
		setSuccessfulResponseHeaders(writer, "application/json")
		_, _ = writer.Write(responseJSON(""))
	}))
	upstream.Config.ConnState = func(connection net.Conn, state http.ConnState) {
		switch state {
		case http.StateIdle:
			select {
			case idleConnections <- connection:
			default:
			}
		case http.StateClosed:
			select {
			case closedConnections <- connection:
			default:
			}
		case http.StateNew, http.StateActive, http.StateHijacked:
		}
	}
	upstream.Start()
	defer upstream.Close()

	lifecycle := newTestLifecycle(t, upstream.URL, Config{})
	if !strings.HasPrefix(lifecycle.Identity(), wantLifecycleVersion+"/sha256:") {
		t.Fatalf("lifecycle identity = %q", lifecycle.Identity())
	}
	transport, ok := lifecycle.client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("upstream transport type = %T, want *http.Transport", lifecycle.client.Transport)
	}
	clientSocketClosed := make(chan struct{}, 1)
	releaseSocketClose := make(chan struct{})
	released := false
	release := func() {
		if !released {
			close(releaseSocketClose)
			released = true
		}
	}
	defer release()
	dialer := &net.Dialer{}
	transport.DialContext = func(
		ctx context.Context,
		network string,
		address string,
	) (net.Conn, error) {
		connection, err := dialer.DialContext(ctx, network, address)
		if err != nil {
			return nil, err
		}
		return &boundaryObservedConn{
			Conn:    connection,
			closed:  clientSocketClosed,
			release: releaseSocketClose,
		}, nil
	}

	layout := lifecycle.RuntimeLayout()
	request := testRequest(layout, genericrunner.BaselineArm, 33)
	if _, err := lifecycle.BeginArm(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	firstResponse := proxyPost(
		t,
		layout,
		requestJSON(genericrunner.BaselineArm, "first", "command"),
	)
	closeResponseBody(t, firstResponse)
	if firstResponse.StatusCode != http.StatusOK {
		t.Fatalf("first proxy status = %d", firstResponse.StatusCode)
	}
	firstIdle := waitForUpstreamConnection(t, idleConnections, "first idle connection")

	secondResponse := proxyPostWithTurnState(
		t,
		layout,
		requestJSON(genericrunner.BaselineArm, "second", "command"),
		"fixture-sticky-turn-state",
	)
	closeResponseBody(t, secondResponse)
	if secondResponse.StatusCode != http.StatusOK {
		t.Fatalf("second proxy status = %d", secondResponse.StatusCode)
	}
	secondIdle := waitForUpstreamConnection(t, idleConnections, "second idle connection")
	if firstIdle != secondIdle {
		t.Fatal("upstream transport did not reuse its idle connection")
	}
	select {
	case <-clientSocketClosed:
		t.Fatal("upstream transport closed the reusable socket before lifecycle Close")
	default:
	}

	closeResult := make(chan error, 1)
	go func() {
		closeResult <- lifecycle.Close(context.Background())
	}()
	select {
	case <-clientSocketClosed:
	case <-time.After(5 * time.Second):
		t.Fatal("lifecycle Close did not close the idle upstream socket")
	}
	closedUpstream := waitForUpstreamConnection(t, closedConnections, "closed connection")
	if closedUpstream != firstIdle {
		t.Fatal("lifecycle closed a different upstream connection")
	}
	if lifecycle.PublicationBoundaryClosed() {
		t.Fatal("publication boundary closed before upstream socket cleanup completed")
	}
	release()
	if err := <-closeResult; err != nil {
		t.Fatalf("Close(): %v", err)
	}
	if !lifecycle.PublicationBoundaryClosed() {
		t.Fatal("publication boundary remained open after upstream socket cleanup")
	}
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	if lifecycle.client != nil {
		t.Fatal("lifecycle retained ownership of its closed upstream client")
	}
}

func TestLifecycleCloseRetriesAfterActiveHandlerOutlivesContext(t *testing.T) {
	upstreamEntered := make(chan struct{}, 1)
	releaseUpstream := make(chan struct{})
	released := false
	release := func() {
		if !released {
			close(releaseUpstream)
			released = true
		}
	}
	defer release()
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		upstreamEntered <- struct{}{}
		<-releaseUpstream
		setSuccessfulResponseHeaders(writer, "application/json")
		_, _ = writer.Write(responseJSON(""))
	}))
	defer upstream.Close()

	lifecycle := newTestLifecycle(t, upstream.URL, Config{})
	layout := lifecycle.RuntimeLayout()
	sessionValue, err := lifecycle.BeginArm(
		context.Background(),
		testRequest(layout, genericrunner.BaselineArm, 34),
	)
	if err != nil {
		t.Fatal(err)
	}
	session := sessionValue.(*ArmSession)
	proxyResult := startAsyncProxyPost(
		t,
		layout,
		requestJSON(genericrunner.BaselineArm, "blocked", "command"),
	)
	waitForSignal(t, upstreamEntered, "blocked upstream request")

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := lifecycle.Close(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("first Close() = %v, want context cancellation", err)
	}
	if lifecycle.PublicationBoundaryClosed() {
		t.Fatal("failed Close opened the publication boundary")
	}
	lifecycle.mu.Lock()
	retainedAuthority := lifecycle.client != nil &&
		lifecycle.credential == fakeCredential &&
		lifecycle.finalizing == session && lifecycle.active == nil
	lifecycle.mu.Unlock()
	if !retainedAuthority {
		t.Fatal("failed Close discarded provider authority or its closing session")
	}

	release()
	result := waitForAsyncHTTPResult(t, proxyResult)
	if result.err != nil {
		t.Fatalf("proxy request after failed Close: %v", result.err)
	}
	closeResponseBody(t, result.response)
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer closeCancel()
	if err := lifecycle.Close(closeCtx); err != nil {
		t.Fatalf("retry Close(): %v", err)
	}
	if !lifecycle.PublicationBoundaryClosed() {
		t.Fatal("retry did not close the publication boundary")
	}
}

func TestLifecycleCloseWaitsForFinishAndReadsPairsAfterDrain(t *testing.T) {
	var upstreamRequests atomic.Int32
	upstreamEntered := make(chan struct{}, 1)
	releaseUpstream := make(chan struct{})
	released := false
	release := func() {
		if !released {
			close(releaseUpstream)
			released = true
		}
	}
	defer release()
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		if upstreamRequests.Add(1) == 2 {
			upstreamEntered <- struct{}{}
			<-releaseUpstream
		}
		setSuccessfulResponseHeaders(writer, "application/json")
		_, _ = writer.Write(responseJSON(""))
	}))
	defer upstream.Close()

	lifecycle := newTestLifecycle(t, upstream.URL, Config{})
	layout := lifecycle.RuntimeLayout()
	executeArm(
		t,
		lifecycle,
		testRequest(layout, genericrunner.CandidateArm, 35),
		requestJSON(genericrunner.CandidateArm, "same", "command"),
		[]byte(candidateConfig),
	)
	request := testRequest(layout, genericrunner.BaselineArm, 35)
	sessionValue, err := lifecycle.BeginArm(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	session := sessionValue.(*ArmSession)
	proxyResult := startAsyncProxyPost(
		t,
		layout,
		requestJSON(genericrunner.BaselineArm, "same", "command"),
	)
	waitForSignal(t, upstreamEntered, "second upstream request")
	writeConfigLock(t, layout, []byte(baselineConfig))
	finishResult := make(chan error, 1)
	go func() {
		_, finishErr := session.Finish(context.Background(), request, harness.RawExecution{})
		finishResult <- finishErr
	}()
	waitForFinalizingSession(t, lifecycle, session, sessionFinishing)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := lifecycle.Close(cancelled); !errors.Is(err, context.Canceled) {
		t.Fatalf("Close during Finish = %v, want context cancellation", err)
	}
	if lifecycle.PublicationBoundaryClosed() {
		t.Fatal("Close published while Finish was still registered")
	}
	lifecycle.mu.Lock()
	authorityRetained := lifecycle.client != nil &&
		lifecycle.credential == fakeCredential && lifecycle.finalizing == session
	lifecycle.mu.Unlock()
	if !authorityRetained {
		t.Fatal("Close discarded authority while Finish was still running")
	}

	release()
	result := waitForAsyncHTTPResult(t, proxyResult)
	if result.err != nil {
		t.Fatalf("proxy request during Finish: %v", result.err)
	}
	closeResponseBody(t, result.response)
	if result.response.StatusCode != http.StatusOK {
		t.Fatalf("proxy status during Finish = %d", result.response.StatusCode)
	}
	select {
	case err := <-finishResult:
		if err != nil {
			t.Fatalf("Finish(): %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for Finish")
	}
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer closeCancel()
	if err := lifecycle.Close(closeCtx); err != nil {
		t.Fatalf("retry Close(): %v", err)
	}
	if !lifecycle.PublicationBoundaryClosed() {
		t.Fatal("retry Close did not observe the reconciled pair")
	}
}

func TestLifecycleCloseRejectsRetainedTerminalActiveSession(t *testing.T) {
	upstream := newJSONUpstream(t, "")
	defer upstream.Close()
	lifecycle := newTestLifecycle(t, upstream.URL, Config{})
	sessionValue, err := lifecycle.BeginArm(
		context.Background(),
		testRequest(lifecycle.RuntimeLayout(), genericrunner.BaselineArm, 36),
	)
	if err != nil {
		t.Fatal(err)
	}
	session := sessionValue.(*ArmSession)
	lifecycle.mu.Lock()
	session.mu.Lock()
	session.state = sessionAborted
	session.mu.Unlock()
	lifecycle.mu.Unlock()
	if err := lifecycle.Close(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "retained an active or finalizing arm") {
		t.Fatalf("Close() = %v, want retained-session rejection", err)
	}
	if lifecycle.PublicationBoundaryClosed() {
		t.Fatal("retained terminal session acquired publication authority")
	}
	lifecycle.mu.Lock()
	lifecycle.active = nil
	lifecycle.mu.Unlock()
	if err := lifecycle.Close(context.Background()); err != nil {
		t.Fatalf("Close() after invariant repair: %v", err)
	}
}

func TestLifecycleClosePersistsUnexpectedServeFailure(t *testing.T) {
	upstream := newJSONUpstream(t, "")
	defer upstream.Close()
	lifecycle := newTestLifecycle(t, upstream.URL, Config{})
	serveFailure := errors.New("fixture unexpected Serve failure")
	lifecycle.mu.Lock()
	lifecycle.serveErr = serveFailure
	lifecycle.mu.Unlock()
	if err := lifecycle.Close(context.Background()); !errors.Is(err, serveFailure) {
		t.Fatalf("Close() = %v, want persisted Serve failure", err)
	}
	if lifecycle.PublicationBoundaryClosed() {
		t.Fatal("unexpected Serve failure acquired publication authority")
	}
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	if lifecycle.client != nil || lifecycle.credential != "" {
		t.Fatal("Serve integrity failure prevented otherwise-safe resource cleanup")
	}
}

func TestUnauthenticatedLoopbackTrafficCannotPoisonAnArm(t *testing.T) {
	upstream := newJSONUpstream(t, "")
	defer upstream.Close()
	lifecycle := newTestLifecycle(t, upstream.URL, Config{})
	layout := lifecycle.RuntimeLayout()
	request := testRequest(layout, genericrunner.BaselineArm, 32)
	sessionValue, err := lifecycle.BeginArm(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	session := sessionValue.(*ArmSession)

	unauthenticated, err := http.Post(
		layout.ProxyURL+"/responses",
		"application/json",
		strings.NewReader("{}"),
	)
	if err != nil {
		t.Fatal(err)
	}
	closeResponseBody(t, unauthenticated)
	if unauthenticated.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthenticated status = %d", unauthenticated.StatusCode)
	}
	wrongRoute, err := http.NewRequest(
		http.MethodPost,
		layout.ProxyURL+"/chat/completions",
		strings.NewReader("{}"),
	)
	if err != nil {
		t.Fatal(err)
	}
	wrongRoute.Header.Set("Authorization", "Bearer "+layout.LocalProxyCapability)
	wrongRoute.Header.Set("Content-Type", "application/json")
	wrongRouteResponse := doRequest(t, wrongRoute)
	closeResponseBody(t, wrongRouteResponse)
	if wrongRouteResponse.StatusCode != http.StatusUnauthorized {
		t.Fatalf("wrong-route status = %d", wrongRouteResponse.StatusCode)
	}

	session.mu.Lock()
	fatal, requestCount := session.fatal, session.requestCount
	session.mu.Unlock()
	if fatal != nil || requestCount != 0 {
		t.Fatalf("host traffic reached arm state: fatal=%v requests=%d", fatal, requestCount)
	}
	valid := proxyPost(t, layout, requestJSON(genericrunner.BaselineArm, "valid", "command"))
	closeResponseBody(t, valid)
	if valid.StatusCode != http.StatusOK {
		t.Fatalf("valid request status = %d", valid.StatusCode)
	}
	writeConfigLock(t, layout, []byte(baselineConfig))
	if _, err := session.Finish(context.Background(), request, harness.RawExecution{}); err != nil {
		t.Fatalf("unauthenticated traffic poisoned later Finish: %v", err)
	}
}

func TestProductionTLSStateCommitsExactVerifiedDERChains(t *testing.T) {
	_, rootKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	rootTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "TokenBench Test Root"},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	rootDER, err := x509.CreateCertificate(
		rand.Reader, rootTemplate, rootTemplate, rootKey.Public(), rootKey,
	)
	if err != nil {
		t.Fatal(err)
	}
	rootCertificate, err := x509.ParseCertificate(rootDER)
	if err != nil {
		t.Fatal(err)
	}
	_, leafKey, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: productionUpstreamDNS},
		DNSNames:     []string{productionUpstreamDNS},
		NotBefore:    now.Add(-time.Hour),
		NotAfter:     now.Add(time.Hour),
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	leafDER, err := x509.CreateCertificate(
		rand.Reader, leafTemplate, rootCertificate, leafKey.Public(), rootKey,
	)
	if err != nil {
		t.Fatal(err)
	}
	leafCertificate, err := x509.ParseCertificate(leafDER)
	if err != nil {
		t.Fatal(err)
	}
	state := tls.ConnectionState{
		Version:           tls.VersionTLS13,
		HandshakeComplete: true,
		ServerName:        productionUpstreamDNS,
		PeerCertificates:  []*x509.Certificate{leafCertificate, rootCertificate},
		VerifiedChains:    [][]*x509.Certificate{{leafCertificate, rootCertificate}},
	}
	trace, err := productionTLSStateTrace(state)
	if err != nil {
		t.Fatal(err)
	}
	leafDigest := sha256.Sum256(leafDER)
	rootDigest := sha256.Sum256(rootDER)
	wantChain := []string{hex.EncodeToString(leafDigest[:]), hex.EncodeToString(rootDigest[:])}
	if trace.DNSName != productionUpstreamDNS || trace.TLSVersion != tls.VersionTLS13 ||
		!reflect.DeepEqual(trace.VerifiedChainsSHA256, [][]string{wantChain}) {
		t.Fatalf("TLS trace = %#v", trace)
	}

	wrongDNS := state
	wrongDNS.ServerName = "example.com"
	if _, err := productionTLSStateTrace(wrongDNS); err == nil {
		t.Fatal("TLS trace accepted an unverified production DNS name")
	}
	missingChain := state
	missingChain.VerifiedChains = nil
	if _, err := productionTLSStateTrace(missingChain); err == nil {
		t.Fatal("TLS trace accepted an absent verified chain")
	}
	oldTLS := state
	oldTLS.Version = tls.VersionTLS11
	if _, err := productionTLSStateTrace(oldTLS); err == nil {
		t.Fatal("TLS trace accepted an obsolete TLS version")
	}
}

func TestProductionConstructionPinsRouteAuthority(t *testing.T) {
	for _, name := range productionNetworkOverrideEnvironment {
		value := os.Getenv(name)
		t.Setenv(name, value)
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	root := filepath.Join(t.TempDir(), "production-runtime")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	lifecycle, err := NewProduction(ProductionConfig{
		StateRoot:          root,
		ToolboxRoot:        filepath.Join(t.TempDir(), "production-toolbox"),
		UpstreamCredential: fakeCredential,
		UpstreamTimeout:    42 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewProduction(): %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		lifecycle.mu.Lock()
		alreadyClosed := lifecycle.closed
		lifecycle.mu.Unlock()
		if alreadyClosed {
			return
		}
		// Most focused lifecycle tests intentionally exercise one arm. Explicit
		// Close reconciliation behavior has dedicated tests below; discard those
		// test-only snapshots before generic resource cleanup.
		lifecycle.mu.Lock()
		lifecycle.pairs = make(map[int]map[genericrunner.Arm]armSnapshot)
		lifecycle.mu.Unlock()
		if err := lifecycle.Close(ctx); err != nil {
			t.Errorf("Close(): %v", err)
		}
	})
	if route, ok := lifecycle.ProductionRouteIdentity(); !ok || route != ProductionRoute {
		t.Fatalf("production route = %q, %t", route, ok)
	}
	if policy, ok := lifecycle.ProductionNetworkPolicyIdentity(); !ok || !ValidProductionNetworkPolicyIdentity(policy) {
		t.Fatalf("production network policy = %q, %t", policy, ok)
	}
	transport, ok := lifecycle.client.Transport.(*http.Transport)
	if !ok || transport.Proxy != nil || transport.TLSClientConfig == nil ||
		transport.TLSClientConfig.RootCAs == nil ||
		transport.TLSClientConfig.MinVersion != tls.VersionTLS12 {
		t.Fatalf("production transport did not pin roots/TLS/no-proxy: %#v", lifecycle.client.Transport)
	}
	if lifecycle.upstreamURL.String() != productionUpstreamOrigin {
		t.Fatalf("production upstream = %q", lifecycle.upstreamURL)
	}
	writable := lifecycle.FilesystemWritePaths()
	wantWritable := []string{
		lifecycle.layout.Home,
		lifecycle.layout.CodexHome,
		lifecycle.layout.Temp,
		lifecycle.layout.ConfigLock,
	}
	slices.Sort(wantWritable)
	if !reflect.DeepEqual(writable, wantWritable) {
		t.Fatalf("filesystem write paths = %q, want %q", writable, wantWritable)
	}
	writable[0] = "/attacker-write"
	if reflect.DeepEqual(writable, lifecycle.FilesystemWritePaths()) {
		t.Fatal("FilesystemWritePaths did not return a defensive slice")
	}

	nonProductionRoot := filepath.Join(t.TempDir(), "generic-runtime")
	if err := os.Mkdir(nonProductionRoot, 0o700); err != nil {
		t.Fatal(err)
	}
	nonProduction, err := New(Config{
		StateRoot:          nonProductionRoot,
		ToolboxRoot:        filepath.Join(t.TempDir(), "generic-toolbox"),
		UpstreamURL:        productionUpstreamOrigin,
		UpstreamCredential: fakeCredential,
	})
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := nonProduction.Close(ctx); err != nil {
			t.Errorf("Close(): %v", err)
		}
	})
	if route, ok := nonProduction.ProductionRouteIdentity(); ok || route != "" {
		t.Fatalf("generic route authority = %q, %t", route, ok)
	}
	if policy, ok := nonProduction.ProductionNetworkPolicyIdentity(); ok || policy != "" {
		t.Fatalf("generic network policy authority = %q, %t", policy, ok)
	}
	if lifecycle.Identity() == nonProduction.Identity() {
		t.Fatal("production authority was absent from lifecycle identity")
	}

	mismatch := testRequest(lifecycle.RuntimeLayout(), genericrunner.BaselineArm, 0)
	mismatch.Invocation.TimeoutMillis = 41_000
	mismatch.Process.TimeoutMillis = 41_000
	if _, err := lifecycle.BeginArm(context.Background(), mismatch); err == nil ||
		!strings.Contains(err.Error(), "timeout") {
		t.Fatalf("BeginArm accepted mismatched production timeout: %v", err)
	}
	matched := testRequest(lifecycle.RuntimeLayout(), genericrunner.BaselineArm, 0)
	matched.Invocation.TimeoutMillis = 42_000
	matched.Process.TimeoutMillis = 42_000
	session, err := lifecycle.BeginArm(context.Background(), matched)
	if err != nil {
		t.Fatalf("BeginArm rejected matching production timeout: %v", err)
	}
	if err := session.Abort(context.Background()); err != nil {
		t.Fatalf("Abort(): %v", err)
	}

	t.Run("post-construction environment cannot change snapshot", func(t *testing.T) {
		before, ok := lifecycle.ProductionNetworkPolicyIdentity()
		if !ok {
			t.Fatal("production lifecycle lost network identity")
		}
		t.Setenv("SSL_CERT_FILE", filepath.Join(t.TempDir(), "attacker-roots.pem"))
		after, ok := lifecycle.ProductionNetworkPolicyIdentity()
		if !ok || after != before || lifecycle.Identity() == "" {
			t.Fatalf("production trust snapshot changed: before=%q after=%q", before, after)
		}
		pinned := lifecycle.client.Transport.(*http.Transport).TLSClientConfig.RootCAs
		if pinned == nil || pinned.Equal(x509.NewCertPool()) {
			t.Fatal("production transport lost its pinned root pool")
		}
	})

	for _, name := range productionNetworkOverrideEnvironment {
		t.Run("reject "+name, func(t *testing.T) {
			t.Setenv(name, "sentinel-override")
			if err := ValidateProductionEnvironment(); err == nil ||
				!strings.Contains(err.Error(), name) {
				t.Fatalf("production environment accepted %s: %v", name, err)
			}
			if _, err := NewProduction(ProductionConfig{}); err == nil ||
				!strings.Contains(err.Error(), name) {
				t.Fatalf("NewProduction did not reject %s before config: %v", name, err)
			}
		})
	}
}

func TestProxyRejectsReflectedCredential(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
		setSuccessfulResponseHeaders(writer, "application/json")
		fmt.Fprintf(writer, `{"model":"%s","secret":"%s","output":[],"usage":{"input_tokens":1,"output_tokens":1}}`,
			"gpt-5.4-2026-03-05", fakeCredential)
	}))
	defer upstream.Close()
	lifecycle := newTestLifecycle(t, upstream.URL, Config{})
	layout := lifecycle.RuntimeLayout()
	request := testRequest(layout, genericrunner.BaselineArm, 6)
	session, err := lifecycle.BeginArm(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	response := proxyPost(t, layout, requestJSON(genericrunner.BaselineArm, "secret", "command"))
	body, _ := io.ReadAll(response.Body)
	closeResponseBody(t, response)
	if response.StatusCode == http.StatusOK || bytes.Contains(body, []byte(fakeCredential)) {
		t.Fatalf("proxy emitted reflected credential: status=%d body=%q", response.StatusCode, body)
	}
	if _, err := session.Finish(context.Background(), request, harness.RawExecution{}); err == nil {
		t.Fatal("Finish() accepted reflected credential")
	}
}

func TestBeginAndAbortResetIdenticalEmptyTemplates(t *testing.T) {
	upstream := newJSONUpstream(t, "")
	defer upstream.Close()
	lifecycle := newTestLifecycle(t, upstream.URL, Config{})
	layout := lifecycle.RuntimeLayout()
	for _, directory := range []string{layout.Home, layout.CodexHome, layout.Temp, layout.ConfigLock} {
		if err := os.WriteFile(filepath.Join(directory, "residue"), []byte("state"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	session, err := lifecycle.BeginArm(
		context.Background(),
		testRequest(layout, genericrunner.BaselineArm, 7),
	)
	if err != nil {
		t.Fatal(err)
	}
	assertEmptyTemplate(t, layout)
	if err := os.WriteFile(filepath.Join(layout.Home, "arm-state"), []byte("state"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := session.Abort(context.Background()); err != nil {
		t.Fatalf("Abort(): %v", err)
	}
	assertEmptyTemplate(t, layout)
	if err := session.Abort(context.Background()); err != nil {
		t.Fatalf("second Abort(): %v", err)
	}
}

func TestFinishDefersOrdinaryProcessTerminationToRunner(t *testing.T) {
	tests := []struct {
		name string
		raw  harness.RawExecution
	}{
		{"launch failed", harness.RawExecution{ExitCode: -1, LaunchFailed: true}},
		{"timeout", harness.RawExecution{ExitCode: -1, TimedOut: true}},
		{"cancelled", harness.RawExecution{ExitCode: -1, Cancelled: true}},
		{"stdout truncated", harness.RawExecution{StdoutTruncated: true}},
		{"stderr truncated", harness.RawExecution{StderrTruncated: true}},
		{"nonzero exit", harness.RawExecution{ExitCode: 7}},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			upstream := newJSONUpstream(t, "")
			defer upstream.Close()
			lifecycle := newTestLifecycle(t, upstream.URL, Config{})
			layout := lifecycle.RuntimeLayout()
			request := testRequest(layout, genericrunner.BaselineArm, 100+index)
			session, err := lifecycle.BeginArm(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(
				filepath.Join(layout.Home, "arm-state"),
				[]byte("state"),
				0o600,
			); err != nil {
				t.Fatal(err)
			}
			writeConfigLock(t, layout, []byte(baselineConfig))
			artifacts, err := session.Finish(context.Background(), request, test.raw)
			if err != nil {
				t.Fatalf("Finish(): %v", err)
			}
			if len(artifacts) != 2 ||
				artifacts[0].Name != harnesscodex.PartialResponsesTraceArtifactName ||
				artifacts[0].MediaType != harnesscodex.PartialResponsesTraceMediaType {
				t.Fatalf("terminal partial artifacts = %#v", artifacts)
			}
			partial, err := harnesscodex.ParsePartialResponsesTrace(artifacts[0].Data)
			if err != nil || partial.RequestCount != 0 || partial.FirstRequest != nil ||
				len(partial.Responses) != 0 || partial.Responses == nil ||
				partial.ResponseSequences == nil {
				t.Fatalf("empty terminal partial trace = %#v, %v", partial, err)
			}
			assertEmptyTemplate(t, layout)
			if _, err := session.Finish(context.Background(), request, test.raw); err == nil {
				t.Fatal("Finish() accepted a consumed terminal session")
			}
		})
	}
}

func TestFinishPreservesCompletedProviderProgressOnOrdinaryFailure(t *testing.T) {
	upstream := newJSONUpstream(t, "")
	defer upstream.Close()
	lifecycle := newTestLifecycle(t, upstream.URL, Config{})
	layout := lifecycle.RuntimeLayout()
	request := testRequest(layout, genericrunner.BaselineArm, 199)
	session, err := lifecycle.BeginArm(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	response := proxyPost(
		t,
		layout,
		requestJSON(genericrunner.BaselineArm, "partial", "command"),
	)
	closeResponseBody(t, response)
	if response.StatusCode != http.StatusOK {
		t.Fatalf("proxy status = %d", response.StatusCode)
	}
	writeConfigLock(t, layout, []byte(baselineConfig))
	artifacts, err := session.Finish(
		context.Background(),
		request,
		harness.RawExecution{ExitCode: 7},
	)
	if err != nil || len(artifacts) != 2 {
		t.Fatalf("Finish() partial artifacts = %#v, %v", artifacts, err)
	}
	partial, err := harnesscodex.ParsePartialResponsesTrace(artifacts[0].Data)
	if err != nil || partial.RequestCount != 1 ||
		!reflect.DeepEqual(partial.ResponseSequences, []int{0}) ||
		len(partial.Responses) != 1 || partial.FirstRequest == nil {
		t.Fatalf("completed partial trace = %#v, %v", partial, err)
	}
	if total := partial.Responses[0].ProviderTotalTokens; total == nil || *total != 14 {
		t.Fatalf("partial trace total_tokens = %v", total)
	}
}

func TestFinishTerminalExecutionStillFailsClosedOnProxyIntegrityError(t *testing.T) {
	upstream := newJSONUpstream(t, "")
	defer upstream.Close()
	lifecycle := newTestLifecycle(t, upstream.URL, Config{})
	layout := lifecycle.RuntimeLayout()
	request := testRequest(layout, genericrunner.BaselineArm, 200)
	session, err := lifecycle.BeginArm(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	response := proxyPost(t, layout, []byte(`{}`))
	closeResponseBody(t, response)
	if response.StatusCode == http.StatusOK {
		t.Fatal("proxy accepted an invalid provider request")
	}
	writeConfigLock(t, layout, []byte(baselineConfig))
	artifacts, err := session.Finish(
		context.Background(),
		request,
		harness.RawExecution{ExitCode: 1},
	)
	if err != nil || len(artifacts) != 2 {
		t.Fatalf("Finish() terminal proxy failure = (%#v, %v), want retained evidence", artifacts, err)
	}
	partial, parseErr := harnesscodex.ParsePartialResponsesTrace(artifacts[0].Data)
	if parseErr != nil || len(partial.CaptureErrorSHA256) != 64 {
		t.Fatalf("terminal proxy failure evidence = (%#v, %v)", partial, parseErr)
	}
	assertEmptyTemplate(t, layout)
}

func TestFinishCleanExitStillRequiresCompleteProviderEvidence(t *testing.T) {
	upstream := newJSONUpstream(t, "")
	defer upstream.Close()
	lifecycle := newTestLifecycle(t, upstream.URL, Config{})
	layout := lifecycle.RuntimeLayout()
	request := testRequest(layout, genericrunner.BaselineArm, 201)
	session, err := lifecycle.BeginArm(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	writeConfigLock(t, layout, []byte(baselineConfig))
	if artifacts, err := session.Finish(
		context.Background(),
		request,
		harness.RawExecution{},
	); err == nil || len(artifacts) != 2 {
		t.Fatalf("Finish() clean incomplete capture = (%#v, %v), want fail closed", artifacts, err)
	}
	assertEmptyTemplate(t, layout)
}

func TestFailureSnapshotsReconcileAndRejectAsymmetry(t *testing.T) {
	tests := []struct {
		name      string
		firstArm  genericrunner.Arm
		firstRaw  harness.RawExecution
		secondRaw harness.RawExecution
	}{
		{"baseline failure then candidate success", genericrunner.BaselineArm, harness.RawExecution{ExitCode: 7}, harness.RawExecution{}},
		{"candidate failure then baseline success", genericrunner.CandidateArm, harness.RawExecution{ExitCode: 7}, harness.RawExecution{}},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			upstream := newJSONUpstream(t, "")
			defer upstream.Close()
			lifecycle := newTestLifecycle(t, upstream.URL, Config{})
			layout := lifecycle.RuntimeLayout()
			secondArm := genericrunner.CandidateArm
			if test.firstArm == genericrunner.CandidateArm {
				secondArm = genericrunner.BaselineArm
			}
			finish := func(arm genericrunner.Arm, raw harness.RawExecution) ([]harness.Artifact, error) {
				request := testRequest(layout, arm, 300+index)
				session, err := lifecycle.BeginArm(context.Background(), request)
				if err != nil {
					t.Fatal(err)
				}
				response := proxyPost(t, layout, requestJSON(arm, "same", "command"))
				closeResponseBody(t, response)
				if response.StatusCode != http.StatusOK {
					t.Fatalf("proxy status = %d", response.StatusCode)
				}
				config := []byte(baselineConfig)
				if arm == genericrunner.CandidateArm {
					config = []byte(candidateConfig)
				}
				writeConfigLock(t, layout, config)
				return session.Finish(context.Background(), request, raw)
			}
			firstArtifacts, err := finish(test.firstArm, test.firstRaw)
			if err != nil || len(firstArtifacts) != 2 {
				t.Fatalf("first Finish() = (%d artifacts, %v)", len(firstArtifacts), err)
			}
			secondArtifacts, err := finish(secondArm, test.secondRaw)
			if err == nil || len(secondArtifacts) != 2 ||
				!strings.Contains(err.Error(), "terminal states are asymmetric") {
				t.Fatalf("second Finish() = (%d artifacts, %v), want asymmetric rejection", len(secondArtifacts), err)
			}
		})
	}
}

func TestFailureSnapshotsRejectProviderRequestAndConfigDrift(t *testing.T) {
	tests := []struct {
		name            string
		issueCandidate  bool
		candidateConfig string
	}{
		{"request count", true, candidateConfig},
		{"effective config", false, strings.Replace(candidateConfig, `model = "gpt-5.4"`, `model = "gpt-5.6"`, 1)},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			upstream := newJSONUpstream(t, "")
			defer upstream.Close()
			lifecycle := newTestLifecycle(t, upstream.URL, Config{})
			layout := lifecycle.RuntimeLayout()
			finish := func(arm genericrunner.Arm, issue bool, config string) ([]harness.Artifact, error) {
				request := testRequest(layout, arm, 320+index)
				session, err := lifecycle.BeginArm(context.Background(), request)
				if err != nil {
					t.Fatal(err)
				}
				if issue {
					response := proxyPost(t, layout, requestJSON(arm, "same", "command"))
					if err := response.Body.Close(); err != nil {
						t.Fatalf("close response body: %v", err)
					}
				}
				writeConfigLock(t, layout, []byte(config))
				return session.Finish(context.Background(), request, harness.RawExecution{ExitCode: 7})
			}
			if _, err := finish(genericrunner.BaselineArm, false, baselineConfig); err != nil {
				t.Fatal(err)
			}
			artifacts, err := finish(genericrunner.CandidateArm, test.issueCandidate, test.candidateConfig)
			if err == nil || len(artifacts) != 2 {
				t.Fatalf("candidate failure reconciliation = (%d artifacts, %v)", len(artifacts), err)
			}
		})
	}
}

func TestLifecycleCloseRejectsUnmatchedFailureSnapshot(t *testing.T) {
	upstream := newJSONUpstream(t, "")
	defer upstream.Close()
	lifecycle := newTestLifecycle(t, upstream.URL, Config{})
	layout := lifecycle.RuntimeLayout()
	request := testRequest(layout, genericrunner.BaselineArm, 340)
	session, err := lifecycle.BeginArm(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	writeConfigLock(t, layout, []byte(baselineConfig))
	if _, err := session.Finish(
		context.Background(), request, harness.RawExecution{ExitCode: 7},
	); err != nil {
		t.Fatal(err)
	}
	idleCloseAttempted := make(chan struct{}, 1)
	lifecycle.client.Transport = &closeIdleObservedTransport{
		RoundTripper: lifecycle.client.Transport,
		closed:       idleCloseAttempted,
	}
	if err := lifecycle.Close(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "unmatched repetition snapshot") {
		t.Fatalf("Close() = %v, want unmatched snapshot rejection", err)
	}
	select {
	case <-idleCloseAttempted:
	default:
		t.Fatal("Close did not attempt upstream idle cleanup after an earlier error")
	}
	lifecycle.mu.Lock()
	clientRetained := lifecycle.client != nil
	lifecycle.mu.Unlock()
	if clientRetained {
		t.Fatal("failed Close retained ownership of its upstream client")
	}
	if lifecycle.PublicationBoundaryClosed() {
		t.Fatal("failed Close opened the publication boundary")
	}
	if err := lifecycle.Close(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "unmatched repetition snapshot") {
		t.Fatalf("second Close() = %v, want persistent unmatched snapshot rejection", err)
	}
}

func TestPartialTraceCommitsEveryProviderResponseAttemptOutcome(t *testing.T) {
	tests := []struct {
		name       string
		config     Config
		upstream   func(*testing.T) (*httptest.Server, string)
		wantStage  string
		wantStatus int
		wantBytes  int
		complete   bool
	}{
		{
			name: "non-200",
			upstream: func(t *testing.T) (*httptest.Server, string) {
				t.Helper()
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
					writer.Header().Set("Content-Type", "application/json")
					writer.Header().Set("X-Codex-Turn-State", "rejected-state")
					writer.WriteHeader(http.StatusTooManyRequests)
					_, _ = io.WriteString(writer, "rate limited")
				}))
				return server, server.URL
			},
			wantStage:  harnesscodex.ResponseAttemptResponseStatus,
			wantStatus: http.StatusTooManyRequests,
			wantBytes:  len("rate limited"),
			complete:   true,
		},
		{
			name: "transport",
			upstream: func(t *testing.T) (*httptest.Server, string) {
				t.Helper()
				server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
				url := server.URL
				server.Close()
				return server, url
			},
			wantStage: harnesscodex.ResponseAttemptTransport,
		},
		{
			name: "redirect rejection retains response envelope",
			upstream: func(t *testing.T) (*httptest.Server, string) {
				t.Helper()
				var server *httptest.Server
				server = httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
					writer.Header().Set("Location", server.URL+"/redirected")
					writer.WriteHeader(http.StatusFound)
				}))
				return server, server.URL
			},
			wantStage:  harnesscodex.ResponseAttemptTransport,
			wantStatus: http.StatusFound,
		},
		{
			name:   "bounded body prefix",
			config: Config{MaxResponseBytes: 8},
			upstream: func(t *testing.T) (*httptest.Server, string) {
				t.Helper()
				server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
					setSuccessfulResponseHeaders(writer, "application/json")
					_, _ = io.WriteString(writer, "0123456789abcdef")
				}))
				return server, server.URL
			},
			wantStage:  harnesscodex.ResponseAttemptResponseBody,
			wantStatus: http.StatusOK,
			wantBytes:  9,
			complete:   false,
		},
	}
	for index, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server, upstreamURL := test.upstream(t)
			if server != nil {
				defer server.Close()
			}
			lifecycle := newTestLifecycle(t, upstreamURL, test.config)
			layout := lifecycle.RuntimeLayout()
			request := testRequest(layout, genericrunner.BaselineArm, 360+index)
			session, err := lifecycle.BeginArm(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			response := proxyPost(t, layout, requestJSON(genericrunner.BaselineArm, "attempt", "command"))
			closeResponseBody(t, response)
			if response.StatusCode == http.StatusOK {
				t.Fatal("proxy unexpectedly accepted failed provider attempt")
			}
			writeConfigLock(t, layout, []byte(baselineConfig))
			artifacts, err := session.Finish(
				context.Background(), request, harness.RawExecution{ExitCode: 1},
			)
			if err != nil || len(artifacts) != 2 {
				t.Fatalf("Finish() = (%d artifacts, %v)", len(artifacts), err)
			}
			partial, err := harnesscodex.ParsePartialResponsesTrace(artifacts[0].Data)
			if err != nil || len(partial.ResponseAttempts) != 1 {
				t.Fatalf("partial response attempts = %#v, %v", partial.ResponseAttempts, err)
			}
			attempt := partial.ResponseAttempts[0]
			if attempt.Stage != test.wantStage || attempt.StatusCode != test.wantStatus ||
				attempt.BodyBytes != test.wantBytes || attempt.BodyComplete != test.complete ||
				attempt.ErrorClass != test.wantStage+"_failure" {
				t.Fatalf("provider attempt = %#v", attempt)
			}
			if attempt.StatusPresent != (test.wantStatus != 0) ||
				attempt.HeadersPresent != (test.wantStatus != 0) {
				t.Fatalf("provider response envelope presence = %#v", attempt)
			}
			if test.wantBytes != 0 && len(attempt.BodySHA256) != 64 {
				t.Fatal("provider attempt omitted exact body/prefix commitment")
			}
		})
	}
}

func TestEffectiveConfigRejectsSymlinkHardlinkAndMultiplicity(t *testing.T) {
	mutations := []struct {
		name   string
		create func(*testing.T, RuntimeLayout)
	}{
		{
			"symlink",
			func(t *testing.T, layout RuntimeLayout) {
				t.Helper()
				target := filepath.Join(filepath.Dir(filepath.Dir(layout.ConfigLock)), "outside-config")
				if err := os.WriteFile(target, []byte(baselineConfig), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(target, filepath.Join(layout.ConfigLock, "config.toml")); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			"hardlink",
			func(t *testing.T, layout RuntimeLayout) {
				t.Helper()
				target := filepath.Join(filepath.Dir(filepath.Dir(layout.ConfigLock)), "outside-config")
				if err := os.WriteFile(target, []byte(baselineConfig), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.Link(target, filepath.Join(layout.ConfigLock, "config.toml")); err != nil {
					t.Skipf("hard links unavailable: %v", err)
				}
			},
		},
		{
			"multiple files",
			func(t *testing.T, layout RuntimeLayout) {
				t.Helper()
				if err := os.WriteFile(filepath.Join(layout.ConfigLock, "one.toml"), []byte(baselineConfig), 0o600); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(filepath.Join(layout.ConfigLock, "two.toml"), []byte(baselineConfig), 0o600); err != nil {
					t.Fatal(err)
				}
			},
		},
	}
	for _, mutation := range mutations {
		t.Run(mutation.name, func(t *testing.T) {
			upstream := newJSONUpstream(t, "")
			defer upstream.Close()
			lifecycle := newTestLifecycle(t, upstream.URL, Config{})
			layout := lifecycle.RuntimeLayout()
			request := testRequest(layout, genericrunner.BaselineArm, 8)
			session, err := lifecycle.BeginArm(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			response := proxyPost(t, layout, requestJSON(genericrunner.BaselineArm, "config", "command"))
			closeResponseBody(t, response)
			if response.StatusCode != http.StatusOK {
				t.Fatalf("proxy status = %d", response.StatusCode)
			}
			mutation.create(t, layout)
			if _, err := session.Finish(context.Background(), request, harness.RawExecution{}); err == nil {
				t.Fatalf("Finish() accepted %s config", mutation.name)
			}
		})
	}
}

func TestResponseAndSSELimitsFailClosed(t *testing.T) {
	t.Run("response body", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			setSuccessfulResponseHeaders(writer, "application/json")
			_, _ = writer.Write(bytes.Repeat([]byte("x"), 1024))
		}))
		defer upstream.Close()
		lifecycle := newTestLifecycle(t, upstream.URL, Config{MaxResponseBytes: 128})
		assertProxyCaptureFailure(t, lifecycle, genericrunner.BaselineArm, 9)
	})

	t.Run("SSE event", func(t *testing.T) {
		upstream := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, _ *http.Request) {
			setSuccessfulResponseHeaders(writer, "text/event-stream")
			fmt.Fprintf(writer, "data: {\"type\":\"response.output_text.delta\",\"delta\":\"%s\"}\n\n", strings.Repeat("x", 512))
		}))
		defer upstream.Close()
		lifecycle := newTestLifecycle(t, upstream.URL, Config{MaxSSEEventBytes: 128})
		assertProxyCaptureFailure(t, lifecycle, genericrunner.BaselineArm, 10)
	})
}

func newTestLifecycle(t *testing.T, upstreamURL string, override Config) *Lifecycle {
	t.Helper()
	root := filepath.Join(t.TempDir(), "runtime")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	config := override
	config.StateRoot = root
	if config.ToolboxRoot == "" {
		config.ToolboxRoot = filepath.Join(t.TempDir(), "toolbox")
	}
	config.UpstreamURL = upstreamURL
	config.UpstreamCredential = fakeCredential
	lifecycle, err := New(config)
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		lifecycle.mu.Lock()
		if lifecycle.closed {
			lifecycle.mu.Unlock()
			return
		}
		lifecycle.pairs = make(map[int]map[genericrunner.Arm]armSnapshot)
		lifecycle.mu.Unlock()
		if err := lifecycle.Close(ctx); err != nil {
			t.Errorf("Close(): %v", err)
		}
	})
	return lifecycle
}

func newJSONUpstream(t *testing.T, toolCall string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		if got := request.Header.Get("Authorization"); got != "Bearer "+fakeCredential {
			t.Errorf("Authorization = %q", got)
		}
		setSuccessfulResponseHeaders(writer, "application/json")
		_, _ = writer.Write(responseJSON(toolCall))
	}))
}

func setSuccessfulResponseHeaders(writer http.ResponseWriter, contentType string) {
	writer.Header().Set("Content-Type", contentType)
	writer.Header()[openAIModelHeader] = []string{"gpt-5.4-2026-03-05"}
	writer.Header()[turnStateHeader] = []string{"fixture-sticky-turn-state"}
	writer.Header()[reasoningIncludedHeader] = []string{"true"}
}

func executeArm(
	t *testing.T,
	lifecycle *Lifecycle,
	request genericrunner.ExecutionRequest,
	body []byte,
	config []byte,
) []harness.Artifact {
	t.Helper()
	session, err := lifecycle.BeginArm(context.Background(), request)
	if err != nil {
		t.Fatalf("BeginArm(%s): %v", request.Arm, err)
	}
	response := proxyPost(t, lifecycle.RuntimeLayout(), body)
	responseBody, err := io.ReadAll(response.Body)
	closeResponseBody(t, response)
	if err != nil {
		t.Fatalf("read proxy response: %v", err)
	}
	if response.StatusCode != http.StatusOK {
		t.Fatalf("proxy status = %d, body=%q", response.StatusCode, responseBody)
	}
	writeConfigLock(t, lifecycle.RuntimeLayout(), config)
	artifacts, err := session.Finish(context.Background(), request, harness.RawExecution{})
	if err != nil {
		t.Fatalf("Finish(%s): %v", request.Arm, err)
	}
	return artifacts
}

func proxyPost(t *testing.T, layout RuntimeLayout, body []byte) *http.Response {
	t.Helper()
	return proxyPostWithTurnState(t, layout, body, "")
}

func proxyPostWithTurnState(
	t *testing.T,
	layout RuntimeLayout,
	body []byte,
	turnState string,
) *http.Response {
	t.Helper()
	request, err := http.NewRequest(
		http.MethodPost,
		layout.ProxyURL+"/responses",
		bytes.NewReader(body),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+layout.LocalProxyCapability)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if turnState != "" {
		request.Header[turnStateHeader] = []string{turnState}
	}
	return doRequest(t, request)
}

func doRequest(t *testing.T, request *http.Request) *http.Response {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{Proxy: nil}}
	response, err := client.Do(request)
	if err != nil {
		t.Fatalf("proxy request: %v", err)
	}
	return response
}

func closeResponseBody(t *testing.T, response *http.Response) {
	t.Helper()
	if err := response.Body.Close(); err != nil {
		t.Fatalf("close response body: %v", err)
	}
}

type asyncHTTPResult struct {
	response *http.Response
	err      error
}

func startAsyncProxyPost(
	t *testing.T,
	layout RuntimeLayout,
	body []byte,
) <-chan asyncHTTPResult {
	t.Helper()
	request, err := http.NewRequest(
		http.MethodPost,
		layout.ProxyURL+"/responses",
		bytes.NewReader(body),
	)
	if err != nil {
		t.Fatal(err)
	}
	request.Header.Set("Authorization", "Bearer "+layout.LocalProxyCapability)
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	client := &http.Client{Transport: &http.Transport{
		Proxy:             nil,
		DisableKeepAlives: true,
	}}
	result := make(chan asyncHTTPResult, 1)
	go func() {
		// The receiving test owns and closes every non-nil response body.
		response, requestErr := client.Do(request) //nolint:bodyclose
		result <- asyncHTTPResult{response: response, err: requestErr}
	}()
	return result
}

func waitForAsyncHTTPResult(
	t *testing.T,
	result <-chan asyncHTTPResult,
) asyncHTTPResult {
	t.Helper()
	select {
	case observed := <-result:
		return observed
	case <-time.After(5 * time.Second):
		t.Fatal("timed out waiting for asynchronous proxy request")
		return asyncHTTPResult{}
	}
}

func waitForSignal(t *testing.T, signal <-chan struct{}, description string) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
	}
}

func waitForFinalizingSession(
	t *testing.T,
	lifecycle *Lifecycle,
	session *ArmSession,
	wantState sessionState,
) {
	t.Helper()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	timeout := time.NewTimer(5 * time.Second)
	defer timeout.Stop()
	for {
		lifecycle.mu.Lock()
		finalizing := lifecycle.finalizing
		session.mu.Lock()
		state := session.state
		session.mu.Unlock()
		lifecycle.mu.Unlock()
		if finalizing == session && state == wantState {
			return
		}
		select {
		case <-ticker.C:
		case <-timeout.C:
			t.Fatalf(
				"timed out waiting for finalizing session state %d; got finalizer=%p state=%d",
				wantState,
				finalizing,
				state,
			)
		}
	}
}

type boundaryObservedConn struct {
	net.Conn
	closed  chan<- struct{}
	release <-chan struct{}
}

type closeIdleObservedTransport struct {
	http.RoundTripper
	closed chan<- struct{}
}

func (transport *closeIdleObservedTransport) CloseIdleConnections() {
	if underlying, ok := transport.RoundTripper.(interface{ CloseIdleConnections() }); ok {
		underlying.CloseIdleConnections()
	}
	select {
	case transport.closed <- struct{}{}:
	default:
	}
}

func (connection *boundaryObservedConn) Close() error {
	err := connection.Conn.Close()
	select {
	case connection.closed <- struct{}{}:
	default:
	}
	<-connection.release
	return err
}

func waitForUpstreamConnection(
	t *testing.T,
	connections <-chan net.Conn,
	description string,
) net.Conn {
	t.Helper()
	select {
	case connection := <-connections:
		return connection
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for %s", description)
		return nil
	}
}

func requestJSON(arm genericrunner.Arm, prompt, commandDescription string) []byte {
	tools := []any{map[string]any{
		"type":        "function",
		"name":        "exec_command",
		"description": commandDescription,
		"parameters": map[string]any{
			"type":       "object",
			"properties": map[string]any{"cmd": map[string]any{"type": "string"}},
		},
		"strict": true,
	}}
	if arm == genericrunner.CandidateArm {
		for _, spec := range pinnedTreatmentSpecs() {
			schema, err := providerVisibleTreatmentSchema(spec)
			if err != nil {
				panic(err)
			}
			name := spec.name
			if spec.kind == harnesscodex.ToolKindMCP {
				name = "mcp__repo_view__" + strings.TrimPrefix(name, "repo_view.")
			}
			tools = append(tools, map[string]any{
				"type":        "function",
				"name":        name,
				"description": spec.description,
				"parameters":  schema,
				"strict":      false,
			})
		}
	}
	installationID := "11111111-1111-4111-8111-111111111111"
	threadID := "33333333-3333-7333-8333-333333333333"
	turnID := "55555555-5555-7555-8555-555555555555"
	if arm == genericrunner.CandidateArm {
		installationID = "22222222-2222-4222-8222-222222222222"
		threadID = "44444444-4444-7444-8444-444444444444"
		turnID = "66666666-6666-7666-8666-666666666666"
	}
	turnMetadata := map[string]any{
		"installation_id":         installationID,
		"session_id":              threadID,
		"thread_id":               threadID,
		"turn_id":                 turnID,
		"window_id":               threadID + ":0",
		"request_kind":            "turn",
		"thread_source":           "user",
		"sandbox":                 "seccomp",
		"turn_started_at_unix_ms": 1,
	}
	turnMetadata["workspaces"] = map[string]any{
		"/source": map[string]any{
			"latest_git_commit_hash": strings.Repeat("0", 40),
			"has_changes":            false,
			"associated_remote_urls": map[string]any{
				"origin": "https://example.invalid/repo.git",
			},
		},
	}
	turnMetadataRaw, err := json.Marshal(turnMetadata)
	if err != nil {
		panic(err)
	}
	raw, err := json.Marshal(map[string]any{
		"model":        "gpt-5.4",
		"instructions": prompt,
		"input": []any{map[string]any{
			"role":    "user",
			"content": "task",
			"internal_chat_message_metadata_passthrough": map[string]any{
				"turn_id": turnID,
			},
		}},
		"stream":           true,
		"tools":            tools,
		"prompt_cache_key": threadID,
		"client_metadata": map[string]any{
			"x-codex-installation-id": installationID,
			"session_id":              threadID,
			"thread_id":               threadID,
			"x-codex-window-id":       threadID + ":0",
			"turn_id":                 turnID,
			"x-codex-turn-metadata":   string(turnMetadataRaw),
		},
	})
	if err != nil {
		panic(err)
	}
	return raw
}

func mutateTurnMetadata(
	t *testing.T,
	raw []byte,
	mutate func(map[string]any),
) []byte {
	t.Helper()
	var request map[string]any
	if err := json.Unmarshal(raw, &request); err != nil {
		t.Fatal(err)
	}
	client, ok := request["client_metadata"].(map[string]any)
	if !ok {
		t.Fatal("request fixture client_metadata is not an object")
	}
	encoded, ok := client["x-codex-turn-metadata"].(string)
	if !ok {
		t.Fatal("request fixture turn metadata is not a string")
	}
	var metadata map[string]any
	if err := json.Unmarshal([]byte(encoded), &metadata); err != nil {
		t.Fatal(err)
	}
	mutate(metadata)
	encodedMetadata, err := json.Marshal(metadata)
	if err != nil {
		t.Fatal(err)
	}
	client["x-codex-turn-metadata"] = string(encodedMetadata)
	result, err := json.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func responseJSON(toolCall string) []byte {
	raw, err := json.Marshal(responseObject(toolCall))
	if err != nil {
		panic(err)
	}
	return raw
}

func responseObject(toolCall string) map[string]any {
	output := []any{map[string]any{
		"type": "message",
		"role": "assistant",
		"content": []any{map[string]any{
			"type": "output_text",
			"text": "answer",
		}},
	}}
	if toolCall != "" {
		output = append(output, map[string]any{
			"type":      "function_call",
			"name":      toolCall,
			"arguments": `{}`,
			"call_id":   "call-1",
		})
	}
	return map[string]any{
		"model":  "gpt-5.4-2026-03-05",
		"output": output,
		"usage": map[string]any{
			"input_tokens":          10,
			"input_tokens_details":  map[string]any{"cached_tokens": 2},
			"output_tokens":         4,
			"output_tokens_details": map[string]any{"reasoning_tokens": 1},
			"total_tokens":          14,
		},
	}
}

func testRequest(layout RuntimeLayout, arm genericrunner.Arm, repetition int) genericrunner.ExecutionRequest {
	arguments := []string{"/bin/true"}
	for _, assignment := range layout.ConfigAssignments() {
		arguments = append(arguments, "-c", assignment)
	}
	servers := make([]harness.MCPServer, 0)
	if arm == genericrunner.CandidateArm {
		servers = append(servers, harness.MCPServer{
			Environment:      map[string]string{},
			Name:             "repo_view",
			Command:          "/repo-view",
			ExecutableSHA256: strings.Repeat("0", 64),
			Arguments: []string{
				"mcp",
				"--root", "/source",
				"--base", strings.Repeat("0", 40),
				"--git", "/usr/bin/git",
				"--git-sha256", strings.Repeat("0", 64),
			},
			Required: true,
			ReadOnly: true,
		})
	}
	environment := layout.Environment()
	return genericrunner.ExecutionRequest{
		Arm:        arm,
		Repetition: repetition,
		Invocation: harness.Invocation{
			Environment:        cloneStringMap(environment),
			MCPServers:         servers,
			Model:              "gpt-5.4",
			RequestedModel:     "gpt-5.4",
			ModelRevision:      "gpt-5.4@gpt-5.4-2026-03-05",
			WorkingDirectory:   "/source",
			SourceRevision:     strings.Repeat("0", 40),
			SourceBaseRevision: strings.Repeat("0", 40),
			SourceTreeSHA256:   strings.Repeat("0", 64),
		},
		Process: harness.ProcessSpec{
			Environment: cloneStringMap(environment),
			Argv:        arguments,
		},
	}
}

func writeConfigLock(t *testing.T, layout RuntimeLayout, content []byte) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(layout.ConfigLock, "effective.toml"), content, 0o600); err != nil {
		t.Fatalf("write config lock: %v", err)
	}
}

func assertArtifactContract(t *testing.T, artifacts []harness.Artifact, config string) {
	t.Helper()
	if len(artifacts) != 2 {
		t.Fatalf("artifact count = %d", len(artifacts))
	}
	if artifacts[0].Name != harnesscodex.ResponsesTraceArtifactName ||
		artifacts[0].MediaType != harnesscodex.ResponsesTraceMediaType ||
		artifacts[1].Name != harnesscodex.EffectiveConfigArtifactName ||
		artifacts[1].MediaType != harnesscodex.EffectiveConfigMediaType ||
		string(artifacts[1].Data) != config {
		t.Fatalf("unexpected artifacts: %#v", artifacts)
	}
}

func assertEmptyTemplate(t *testing.T, layout RuntimeLayout) {
	t.Helper()
	wanted := map[string][]string{
		layout.Home:       {},
		layout.CodexHome:  {"sqlite"},
		layout.Temp:       {},
		layout.ConfigLock: {},
	}
	for path, wantedNames := range wanted {
		entries, err := os.ReadDir(path)
		if err != nil {
			t.Fatalf("ReadDir(%s): %v", path, err)
		}
		names := make([]string, len(entries))
		for index, entry := range entries {
			names[index] = entry.Name()
		}
		if !reflect.DeepEqual(names, wantedNames) {
			t.Fatalf("template %s entries = %v, want %v", path, names, wantedNames)
		}
	}
}

func assertProxyCaptureFailure(
	t *testing.T,
	lifecycle *Lifecycle,
	arm genericrunner.Arm,
	repetition int,
) {
	t.Helper()
	layout := lifecycle.RuntimeLayout()
	request := testRequest(layout, arm, repetition)
	session, err := lifecycle.BeginArm(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	response := proxyPost(t, layout, requestJSON(arm, "limit", "command"))
	closeResponseBody(t, response)
	if response.StatusCode == http.StatusOK {
		t.Fatal("proxy accepted bounded capture overflow")
	}
	if _, err := session.Finish(context.Background(), request, harness.RawExecution{}); err == nil {
		t.Fatal("Finish() accepted bounded capture overflow")
	}
}
