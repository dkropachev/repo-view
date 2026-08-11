package fake_test

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/scopesifter/scopesifter/benchmarks/tokenbench"
	"github.com/scopesifter/scopesifter/benchmarks/tokenbench/harness"
	"github.com/scopesifter/scopesifter/benchmarks/tokenbench/harness/conformance"
	"github.com/scopesifter/scopesifter/benchmarks/tokenbench/harness/fake"
)

func TestConformance(t *testing.T) {
	t.Parallel()
	invocation := invocationFixture()
	conformance.Run(t, conformance.Fixture{
		Adapter: fake.Adapter{},
		Resolve: harness.ResolveRequest{
			Environment:            invocation.Environment,
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
		},
		Invocation: invocation,
		Execution: harness.RawExecution{
			Stdout: []byte(`{
  "usage": {"input_tokens": 100, "cached_input_tokens": 40, "output_tokens": 20, "reasoning_tokens": 5},
  "final_answer": "answer",
  "model": "pinned-model",
  "tool_calls": [],
  "completed": true
}`),
			Stderr:   []byte{},
			ExitCode: 0,
		},
	})
}

func TestBuildPreservesPromptAndOnlyRendersMCPDelta(t *testing.T) {
	t.Parallel()
	adapter := fake.Adapter{}
	baseline := invocationFixture()
	candidate := cloneInvocation(t, baseline)
	candidate.MCPServers = []harness.MCPServer{{
		Environment:      map[string]string{},
		Arguments:        []string{"mcp", "--root", "/source", "--base", strings.Repeat("0", 40)},
		Name:             "scopesifter",
		Command:          "/tools/scopesifter",
		ExecutableSHA256: tokenbench.SHA256([]byte("scopesifter")),
		Required:         true,
		ReadOnly:         true,
	}}
	conformance.RunPair(t, adapter, baseline, candidate)
	baselineProcess, err := adapter.Build(context.Background(), baseline)
	if err != nil {
		t.Fatal(err)
	}
	mcpArguments, err := adapter.MCPArguments(
		context.Background(),
		candidate.MCPServers[0],
	)
	if err != nil {
		t.Fatal(err)
	}
	candidateArgv := append(append([]string(nil), baselineProcess.Argv...), mcpArguments...)
	if !bytes.Equal(baselineProcess.Stdin, baseline.Prompt) {
		t.Fatal("adapter changed prompt bytes")
	}
	if got := candidateArgv[len(baselineProcess.Argv):]; len(got) != 2 || got[0] != "--mcp-servers-base64" {
		t.Fatalf("unexpected rendered MCP delta: %q", got)
	}
	if _, err := adapter.Build(context.Background(), candidate); err == nil {
		t.Fatal("adapter Build accepted an arm-dependent invocation")
	}
}

func TestDecodeDoesNotRequireToolUse(t *testing.T) {
	t.Parallel()
	output := []byte(`{
  "usage": {"provider_total_tokens": 2, "input_tokens": 1, "cached_input_tokens": 0, "output_tokens": 1, "reasoning_tokens": 0},
  "final_answer": "valid without tool",
  "model": "pinned-model",
  "tool_calls": [],
  "completed": true
}`)
	observation, err := (fake.Adapter{}).Decode(context.Background(), harness.RawExecution{
		Stdout:   output,
		ExitCode: 0,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !observation.Completed || len(observation.ToolCalls) != 0 ||
		observation.Usage.ProviderTotalTokens == nil ||
		*observation.Usage.ProviderTotalTokens != 2 {
		t.Fatalf("unexpected observation: %+v", observation)
	}
}

func TestResolveAdvertisesV2ObservationContract(t *testing.T) {
	t.Parallel()
	identity := invocationFixture().HarnessIdentity
	if identity.AdapterVersion != "tokenbench.fake-adapter/v2" ||
		identity.DecoderSchema != "tokenbench.fake-output/v2" {
		t.Fatalf("fake observation contract = %+v", identity)
	}
}

func invocationFixture() harness.Invocation {
	invocation := harness.Invocation{
		Environment:            map[string]string{"LANG": "C.UTF-8"},
		Arguments:              []string{"exec", "--json"},
		MCPServers:             []harness.MCPServer{},
		Prompt:                 []byte("same prompt\n"),
		Executable:             "/tools/fake",
		ExecutableSHA256:       tokenbench.SHA256([]byte("fake")),
		Model:                  "pinned-model",
		RequestedModel:         "pinned-model",
		ModelRevision:          "pinned-model@2026-08-01",
		ReasoningEffort:        "medium",
		PermissionProfile:      "read-only",
		DeveloperInstructions:  "same instructions",
		WorkingDirectory:       "/source",
		SourceRevision:         strings.Repeat("1", 40),
		SourceBaseRevision:     strings.Repeat("0", 40),
		SourceTreeSHA256:       tokenbench.SHA256([]byte("tree")),
		GitExecutable:          "/usr/bin/git",
		GitExecutableSHA256:    tokenbench.SHA256([]byte("git")),
		GitMetadataSHA256:      tokenbench.SHA256([]byte("git-metadata")),
		RunnerExecutable:       "/tools/tokenbench",
		RunnerExecutableSHA256: tokenbench.SHA256([]byte("tokenbench")),
		TimeoutMillis:          30_000,
	}
	identity, err := (fake.Adapter{}).Resolve(context.Background(), harness.ResolveRequest{
		Environment:            invocation.Environment,
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
	})
	if err != nil {
		panic(err)
	}
	invocation.HarnessIdentity = identity
	return invocation
}

func cloneInvocation(t *testing.T, source harness.Invocation) harness.Invocation {
	t.Helper()
	raw, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	var clone harness.Invocation
	if err := json.Unmarshal(raw, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}
