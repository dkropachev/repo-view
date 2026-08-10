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
	"fmt"
	"io"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
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
	response.Body.Close()
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
	first.Body.Close()
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first proxy status = %d", first.StatusCode)
	}
	second := proxyPostWithTurnState(
		t,
		layout,
		requestJSON(genericrunner.BaselineArm, "second prompt", "command"),
		"fixture-sticky-turn-state",
	)
	second.Body.Close()
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
	if trace.Responses[0].RequestNonToolPayloadSHA256 != trace.FirstRequest.NonToolPayloadSHA256 {
		t.Fatal("first response lost its first-request commitment")
	}
	if trace.Responses[1].RequestNonToolPayloadSHA256 == trace.Responses[0].RequestNonToolPayloadSHA256 {
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
		fmt.Fprintf(writer, "event: response.created\ndata: {\"type\":\"response.created\"}\n\n")
		fmt.Fprintf(writer, "event: response.completed\ndata: %s\n\n", completed)
		fmt.Fprint(writer, "data: [DONE]\n\n")
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
				return proxyPost(t, layout, bytes.Repeat([]byte("x"), 64))
			},
		},
		{
			name: "missing candidate tool",
			issue: func(t *testing.T, layout RuntimeLayout) *http.Response {
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
			response.Body.Close()
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
	first.Body.Close()
	if first.StatusCode != http.StatusOK {
		t.Fatalf("first proxy status = %d", first.StatusCode)
	}
	second := proxyPost(t, layout, requestJSON(genericrunner.BaselineArm, "second", "command-v2"))
	second.Body.Close()
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
			response.Body.Close()
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
	if baseline.NonToolPayloadSHA256 != candidate.NonToolPayloadSHA256 {
		t.Fatal("fresh request identifiers changed the non-tool commitment")
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
			if trace.NonToolPayloadSHA256 == baseline.NonToolPayloadSHA256 {
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
		strings.Contains(fmt.Sprint(values), fakeCredential) {
		t.Fatalf("child environment contains the wrong credential: %#v", values)
	}
	resolved, err := resolveConfig(Config{
		StateRoot:          filepath.Dir(layout.Home),
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
	unauthenticated.Body.Close()
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
	wrongRouteResponse.Body.Close()
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
	valid.Body.Close()
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
		value, existed := os.LookupEnv(name)
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			if existed {
				_ = os.Setenv(name, value)
			} else {
				_ = os.Unsetenv(name)
			}
		})
	}
	root := filepath.Join(t.TempDir(), "production-runtime")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	lifecycle, err := NewProduction(ProductionConfig{
		StateRoot:          root,
		UpstreamCredential: fakeCredential,
		UpstreamTimeout:    42 * time.Second,
	})
	if err != nil {
		t.Fatalf("NewProduction(): %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
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
		if pinned == nil || len(pinned.Subjects()) == 0 {
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
	response.Body.Close()
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
			artifacts, err := session.Finish(context.Background(), request, test.raw)
			if err != nil {
				t.Fatalf("Finish(): %v", err)
			}
			if len(artifacts) != 1 ||
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
	response.Body.Close()
	if response.StatusCode != http.StatusOK {
		t.Fatalf("proxy status = %d", response.StatusCode)
	}
	artifacts, err := session.Finish(
		context.Background(),
		request,
		harness.RawExecution{ExitCode: 7},
	)
	if err != nil || len(artifacts) != 1 {
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
	response.Body.Close()
	if response.StatusCode == http.StatusOK {
		t.Fatal("proxy accepted an invalid provider request")
	}
	if artifacts, err := session.Finish(
		context.Background(),
		request,
		harness.RawExecution{ExitCode: 1},
	); err == nil || artifacts != nil {
		t.Fatalf("Finish() terminal proxy failure = (%#v, %v), want fail closed", artifacts, err)
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
	if artifacts, err := session.Finish(
		context.Background(),
		request,
		harness.RawExecution{},
	); err == nil || artifacts != nil {
		t.Fatalf("Finish() clean incomplete capture = (%#v, %v), want fail closed", artifacts, err)
	}
	assertEmptyTemplate(t, layout)
}

func TestEffectiveConfigRejectsSymlinkHardlinkAndMultiplicity(t *testing.T) {
	mutations := []struct {
		name   string
		create func(*testing.T, RuntimeLayout)
	}{
		{
			"symlink",
			func(t *testing.T, layout RuntimeLayout) {
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
			response.Body.Close()
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
	config.UpstreamURL = upstreamURL
	config.UpstreamCredential = fakeCredential
	lifecycle, err := New(config)
	if err != nil {
		t.Fatalf("New(): %v", err)
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
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
	writer.Header().Set(openAIModelHeader, "gpt-5.4-2026-03-05")
	writer.Header().Set(turnStateHeader, "fixture-sticky-turn-state")
	writer.Header().Set(reasoningIncludedHeader, "true")
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
	response.Body.Close()
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
		request.Header.Set(turnStateHeader, turnState)
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
	response.Body.Close()
	if response.StatusCode == http.StatusOK {
		t.Fatal("proxy accepted bounded capture overflow")
	}
	if _, err := session.Finish(context.Background(), request, harness.RawExecution{}); err == nil {
		t.Fatal("Finish() accepted bounded capture overflow")
	}
}
