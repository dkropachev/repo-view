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
	want := harness.Observation{
		FinalAnswer: "The answer is 42.",
		Model:       "gpt-5.4",
		ToolCalls:   []string{"exec_command", "repo_view.inspect"},
		Usage: harness.Usage{
			InputTokens:       10,
			CachedInputTokens: 2,
			OutputTokens:      5,
			ReasoningTokens:   1,
		},
		Completed: true,
	}
	if !reflect.DeepEqual(observation, want) {
		t.Fatalf("unexpected observation:\n got: %+v\nwant: %+v", observation, want)
	}
}

func TestPartialResponsesTraceIsStrictAndNeverDecoderInput(t *testing.T) {
	partial := PartialResponsesTrace{
		SchemaVersion:     PartialResponsesTraceSchemaVersion,
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
		`"schema_version": "tokenbench.codex.responses-trace/v1",`,
		`"schema_version": "tokenbench.codex.responses-trace/v1", "unknown": true,`)
	if _, err := ParseResponsesTrace(corrupt); err == nil {
		t.Fatal("ParseResponsesTrace accepted an unknown field")
	}
}

func TestDecodeDoesNotRequireMCPUse(t *testing.T) {
	t.Parallel()
	usage := harness.Usage{InputTokens: 1, OutputTokens: 1}
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
			Model:                "gpt-5.4",
			NonToolPayloadSHA256: strings.Repeat("0", 64),
			Tools:                tools,
		},
		Responses: []ResponsesResponseTrace{{
			RequestModel:                "gpt-5.4",
			RequestNonToolPayloadSHA256: strings.Repeat("0", 64),
			RequestToolsSHA256:          digest(toolsJSON),
			ResponseModel:               "gpt-5.4-2026-03-05",
			ProviderModelHeader:         "gpt-5.4-2026-03-05",
			TurnStateSHA256:             strings.Repeat("1", 64),
			Outputs: []ResponsesOutputTrace{{
				Type:          OutputTypeAssistantMessage,
				WireSHA256:    strings.Repeat("3", 64),
				PayloadSHA256: digest([]byte("No tool needed.")),
			}},
			Usage:             &usage,
			ReasoningIncluded: true,
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
				"{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":1,\"cached_input_tokens\":0,\"output_tokens\":1,\"reasoning_output_tokens\":0}}\n",
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
		observation.ToolCalls == nil || len(observation.ToolCalls) != 0 {
		t.Fatalf("unexpected baseline observation: %+v", observation)
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
			value.Artifacts[0].Data = replaceExactlyOnce(t, value.Artifacts[0].Data,
				`"schema_version": "tokenbench.codex.responses-trace/v1",`,
				`"schema_version": "tokenbench.codex.responses-trace/v1", "schema_version": "tokenbench.codex.responses-trace/v1",`)
		}},
		{"trace unknown field", func(t *testing.T, value *harness.RawExecution) {
			value.Artifacts[0].Data = replaceExactlyOnce(t, value.Artifacts[0].Data,
				`"schema_version": "tokenbench.codex.responses-trace/v1",`,
				`"schema_version": "tokenbench.codex.responses-trace/v1", "unknown": true,`)
		}},
		{"unsupported provider model", func(_ *testing.T, value *harness.RawExecution) {
			value.Artifacts[0].Data = []byte(strings.ReplaceAll(
				string(value.Artifacts[0].Data),
				"gpt-5.4-2026-03-05", "gpt-5.4-2099-01-01",
			))
		}},
		{"partial MCP surface", func(t *testing.T, value *harness.RawExecution) {
			mutateTrace(t, value, func(trace *ResponsesTrace) { trace.FirstRequest.Tools = trace.FirstRequest.Tools[:4] })
		}},
		{"undeclared call", func(t *testing.T, value *harness.RawExecution) {
			mutateTrace(t, value, func(trace *ResponsesTrace) { trace.Responses[0].Outputs[0].Name = "other_command" })
		}},
		{"nil outputs", func(t *testing.T, value *harness.RawExecution) {
			mutateTrace(t, value, func(trace *ResponsesTrace) { trace.Responses[1].Outputs = nil })
		}},
		{"request payload commitment", func(t *testing.T, value *harness.RawExecution) {
			mutateTrace(t, value, func(trace *ResponsesTrace) {
				trace.Responses[0].RequestNonToolPayloadSHA256 = strings.Repeat("f", 64)
			})
		}},
		{"request tool commitment", func(t *testing.T, value *harness.RawExecution) {
			mutateTrace(t, value, func(trace *ResponsesTrace) {
				trace.Responses[1].RequestToolsSHA256 = strings.Repeat("f", 64)
			})
		}},
		{"tool argument commitment", func(t *testing.T, value *harness.RawExecution) {
			mutateTrace(t, value, func(trace *ResponsesTrace) {
				trace.Responses[0].Outputs[1].PayloadSHA256 = strings.Repeat("f", 64)
			})
		}},
		{"assistant content commitment", func(t *testing.T, value *harness.RawExecution) {
			mutateTrace(t, value, func(trace *ResponsesTrace) {
				trace.Responses[1].Outputs[1].PayloadSHA256 = strings.Repeat("f", 64)
			})
		}},
		{"reordered provider outputs", func(t *testing.T, value *harness.RawExecution) {
			mutateTrace(t, value, func(trace *ResponsesTrace) {
				outputs := trace.Responses[0].Outputs
				outputs[0], outputs[1] = outputs[1], outputs[0]
			})
		}},
		{"cache write usage", func(t *testing.T, value *harness.RawExecution) {
			mutateTrace(t, value, func(trace *ResponsesTrace) { trace.Responses[0].Usage.CacheWriteInputTokens = 1 })
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
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
			value.Stdout = replaceExactlyOnce(t, value.Stdout,
				`{"type":"thread.started","thread_id":"thread-1"}`,
				`{"type":"thread.started","type":"thread.started","thread_id":"thread-1"}`)
		}},
		{"unknown event", func(t *testing.T, value *harness.RawExecution) {
			value.Stdout = replaceExactlyOnce(t, value.Stdout, `{"type":"turn.started"}`, `{"type":"turn.unknown"}`)
		}},
		{"unknown event field", func(t *testing.T, value *harness.RawExecution) {
			value.Stdout = replaceExactlyOnce(t, value.Stdout,
				`{"type":"turn.started"}`, `{"type":"turn.started","extra":true}`)
		}},
		{"wrong event order", func(t *testing.T, value *harness.RawExecution) {
			value.Stdout = replaceExactlyOnce(t, value.Stdout,
				`{"type":"turn.started"}`, `{"type":"thread.started","thread_id":"thread-2"}`)
		}},
		{"disabled item", func(t *testing.T, value *harness.RawExecution) {
			value.Stdout = replaceExactlyOnce(t, value.Stdout,
				`"type":"reasoning","text":"Checked the repository."`,
				`"type":"file_change","text":"Checked the repository."`)
		}},
		{"duplicate completed item", func(_ *testing.T, value *harness.RawExecution) {
			value.Stdout = []byte(strings.ReplaceAll(string(value.Stdout), "message-1", "reasoning-1"))
		}},
		{"active item at turn completion", func(t *testing.T, value *harness.RawExecution) {
			line := `{"type":"item.completed","item":{"id":"command-1","type":"command_execution","command":"/bin/zsh -c 'rg --files'","aggregated_output":"README.md\n","exit_code":0,"status":"completed"}}` + "\n"
			value.Stdout = replaceExactlyOnce(t, value.Stdout, line, "")
		}},
		{"unknown MCP result field", func(t *testing.T, value *harness.RawExecution) {
			value.Stdout = replaceExactlyOnce(t, value.Stdout,
				`"structured_content":{"text":"README"}`,
				`"structured_content":{"text":"README"},"unknown":true`)
		}},
		{"MCP identity changed", func(t *testing.T, value *harness.RawExecution) {
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
			mutateTrace(t, value, func(trace *ResponsesTrace) { trace.Responses[1].Usage.OutputTokens++ })
		}},
		{"event after completion", func(_ *testing.T, value *harness.RawExecution) {
			value.Stdout = append(value.Stdout,
				[]byte(`{"type":"item.completed","item":{"id":"late","type":"agent_message","text":"late"}}`+"\n")...)
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
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
