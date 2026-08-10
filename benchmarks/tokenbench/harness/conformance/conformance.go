// Package conformance supplies shared tests for harness adapters.
package conformance

import (
	"context"
	"reflect"
	"testing"

	"github.com/dkropachev/repo-view/benchmarks/tokenbench/harness"
)

// Fixture is one deterministic adapter exercise supplied by an adapter test.
type Fixture struct {
	Adapter    harness.Adapter
	Resolve    harness.ResolveRequest
	Invocation harness.Invocation
	Execution  harness.RawExecution
}

// Run checks the harness-neutral adapter contract. Adapter packages should call
// this from their own TestConformance.
func Run(t *testing.T, fixture Fixture) {
	t.Helper()
	ctx := context.Background()
	invocation := fixture.Invocation
	request := fixture.Resolve
	if !reflect.DeepEqual(invocation.Environment, request.Environment) ||
		invocation.Executable != request.Executable ||
		invocation.ExecutableSHA256 != request.ExecutableSHA256 ||
		invocation.RequestedModel != request.Model ||
		invocation.ModelRevision != request.ExpectedModelRevision ||
		invocation.ReasoningEffort != request.ReasoningEffort ||
		invocation.PermissionProfile != request.PermissionProfile ||
		invocation.DeveloperInstructions != request.DeveloperInstructions ||
		invocation.WorkingDirectory != request.WorkingDirectory ||
		invocation.SourceRevision != request.SourceRevision ||
		invocation.SourceBaseRevision != request.SourceBaseRevision ||
		invocation.SourceTreeSHA256 != request.SourceTreeSHA256 ||
		invocation.GitExecutable != request.GitExecutable ||
		invocation.GitExecutableSHA256 != request.GitExecutableSHA256 ||
		invocation.GitMetadataSHA256 != request.GitMetadataSHA256 ||
		invocation.RunnerExecutable != request.RunnerExecutable ||
		invocation.RunnerExecutableSHA256 != request.RunnerExecutableSHA256 ||
		invocation.TimeoutMillis != request.TimeoutMillis {
		t.Fatal("fixture invocation does not bind the complete ResolveRequest")
	}
	if fixture.Adapter.Kind() == "" {
		t.Fatal("adapter Kind is empty")
	}
	firstIdentity, err := fixture.Adapter.Resolve(ctx, fixture.Resolve)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	secondIdentity, err := fixture.Adapter.Resolve(ctx, fixture.Resolve)
	if err != nil {
		t.Fatalf("second Resolve: %v", err)
	}
	if !reflect.DeepEqual(firstIdentity, secondIdentity) {
		t.Fatal("Resolve is not deterministic")
	}
	if firstIdentity.Kind != fixture.Adapter.Kind() {
		t.Fatalf("identity kind %q != adapter kind %q", firstIdentity.Kind, fixture.Adapter.Kind())
	}
	if firstIdentity.ExecutableSHA256 != fixture.Resolve.ExecutableSHA256 ||
		firstIdentity.Model != fixture.Resolve.Model ||
		firstIdentity.ModelRevision != fixture.Resolve.ExpectedModelRevision ||
		firstIdentity.ReasoningEffort != fixture.Resolve.ReasoningEffort {
		t.Fatal("resolved identity does not bind the requested harness/model settings")
	}
	if err := harness.ValidateIdentity(firstIdentity); err != nil {
		t.Fatalf("identity: %v", err)
	}

	firstProcess, err := fixture.Adapter.Build(ctx, fixture.Invocation)
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	secondProcess, err := fixture.Adapter.Build(ctx, fixture.Invocation)
	if err != nil {
		t.Fatalf("second Build: %v", err)
	}
	if !reflect.DeepEqual(firstProcess, secondProcess) {
		t.Fatal("Build is not deterministic")
	}
	if err := harness.ValidateProcessSpec(firstProcess); err != nil {
		t.Fatalf("process: %v", err)
	}

	firstObservation, err := fixture.Adapter.Decode(ctx, fixture.Execution)
	if err != nil {
		t.Fatalf("Decode: %v", err)
	}
	secondObservation, err := fixture.Adapter.Decode(ctx, fixture.Execution)
	if err != nil {
		t.Fatalf("second Decode: %v", err)
	}
	if !reflect.DeepEqual(firstObservation, secondObservation) {
		t.Fatal("Decode is not deterministic")
	}
	if err := harness.ValidateUsage(firstObservation.Usage); err != nil {
		t.Fatalf("usage: %v", err)
	}
}

// RunPair checks that adapter rendering preserves byte-identical process input
// and common process configuration. The candidate may only append a nonempty
// harness-native encoding of its already-approved MCP registration.
func RunPair(
	t *testing.T,
	adapter harness.Adapter,
	baseline, candidate harness.Invocation,
) {
	t.Helper()
	if len(baseline.MCPServers) != 0 || len(candidate.MCPServers) != 1 {
		t.Fatalf(
			"invalid fixture MCP cardinality: baseline=%d candidate=%d",
			len(baseline.MCPServers),
			len(candidate.MCPServers),
		)
	}
	baselineCommon := baseline
	candidateCommon := candidate
	baselineCommon.MCPServers = nil
	candidateCommon.MCPServers = nil
	if !reflect.DeepEqual(baselineCommon, candidateCommon) {
		t.Fatal("fixture invocations differ outside MCP registration")
	}
	ctx := context.Background()
	baselineProcess, err := adapter.Build(ctx, baseline)
	if err != nil {
		t.Fatalf("Build baseline: %v", err)
	}
	if err := harness.ValidateProcessSpec(baselineProcess); err != nil {
		t.Fatalf("baseline process: %v", err)
	}
	if len(baselineProcess.Argv) == 0 ||
		baselineProcess.Argv[0] != baseline.Executable ||
		!reflect.DeepEqual(baselineProcess.Stdin, baseline.Prompt) ||
		!reflect.DeepEqual(baselineProcess.Environment, baseline.Environment) ||
		baselineProcess.Directory != baseline.WorkingDirectory ||
		baselineProcess.TimeoutMillis != baseline.TimeoutMillis {
		t.Fatal("adapter changed the approved common process configuration")
	}
	expected, err := adapter.MCPArguments(ctx, candidate.MCPServers[0])
	if err != nil {
		t.Fatalf("MCPArguments: %v", err)
	}
	if len(expected) == 0 {
		t.Fatal("adapter returned an empty MCP suffix")
	}
	secondExpected, err := adapter.MCPArguments(ctx, candidate.MCPServers[0])
	if err != nil {
		t.Fatalf("second MCPArguments: %v", err)
	}
	if !reflect.DeepEqual(expected, secondExpected) {
		t.Fatal("MCPArguments is not deterministic")
	}
	candidateArgv := append(append([]string(nil), baselineProcess.Argv...), expected...)
	if !reflect.DeepEqual(candidateArgv[:len(baselineProcess.Argv)], baselineProcess.Argv) {
		t.Fatal("runner construction changed the common argv")
	}
	candidateProcess := baselineProcess
	candidateProcess.Argv = candidateArgv
	if err := harness.ValidateProcessSpec(candidateProcess); err != nil {
		t.Fatalf("candidate process: %v", err)
	}
}
