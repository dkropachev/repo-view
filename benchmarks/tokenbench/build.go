package tokenbench

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/yapless/scopesifter/benchmarks/tokenbench/harness"
	harnesscodex "github.com/yapless/scopesifter/benchmarks/tokenbench/harness/codex"
	"github.com/yapless/scopesifter/benchmarks/tokenbench/harness/fake"
)

// ProcessPair contains adapter-built target processes after the second parity
// gate. The only actual-process difference is a candidate argv suffix that
// encodes the already-approved MCP registration.
type ProcessPair struct {
	CandidateMCPArguments []string            `json:"candidate_mcp_arguments"`
	Baseline              harness.ProcessSpec `json:"baseline"`
	Candidate             harness.ProcessSpec `json:"candidate"`
}

// renderPair is deliberately package-private. Official process construction
// is available only through Pair.Build, which retains the adapter capability
// that resolved the suite and repeats all filesystem and identity checks.
func renderPair(
	ctx context.Context,
	adapter harness.Adapter,
	baseline, candidate harness.Invocation,
) (ProcessPair, error) {
	if adapter == nil {
		return ProcessPair{}, errors.New("adapter is required")
	}
	if len(baseline.MCPServers) != 0 || len(candidate.MCPServers) != 1 {
		return ProcessPair{}, errors.New("invalid MCP registries at adapter handoff")
	}
	baselineCommon := cloneInvocation(baseline)
	candidateCommon := cloneInvocation(candidate)
	baselineCommon.MCPServers = nil
	candidateCommon.MCPServers = nil
	if !reflect.DeepEqual(baselineCommon, candidateCommon) {
		return ProcessPair{}, errors.New("adapter handoff invocations are not otherwise identical")
	}

	baselineProcess, err := adapter.Build(ctx, cloneInvocation(baseline))
	if err != nil {
		return ProcessPair{}, err
	}
	if err := harness.ValidateProcessSpec(baselineProcess); err != nil {
		return ProcessPair{}, err
	}
	if len(baselineProcess.Argv) == 0 ||
		baselineProcess.Argv[0] != baseline.Executable ||
		!reflect.DeepEqual(baselineProcess.Stdin, baseline.Prompt) ||
		!reflect.DeepEqual(baselineProcess.Environment, baseline.Environment) ||
		baselineProcess.Directory != baseline.WorkingDirectory ||
		baselineProcess.TimeoutMillis != baseline.TimeoutMillis {
		return ProcessPair{}, errors.New(
			"adapter changed the approved common process configuration",
		)
	}
	registration := candidate.MCPServers[0]
	registration.Environment = cloneMap(registration.Environment)
	registration.Arguments = append(
		make([]string, 0, len(registration.Arguments)),
		registration.Arguments...,
	)
	expectedMCPArguments, err := adapter.MCPArguments(ctx, registration)
	if err != nil {
		return ProcessPair{}, err
	}
	if len(expectedMCPArguments) == 0 {
		return ProcessPair{}, errors.New(
			"adapter returned an empty MCP encoding",
		)
	}
	if err := validateTreatmentEncoding(
		baseline.HarnessIdentity.Kind,
		registration,
		expectedMCPArguments,
	); err != nil {
		return ProcessPair{}, err
	}
	candidateProcess := cloneProcessSpec(baselineProcess)
	candidateProcess.Argv = append(candidateProcess.Argv, expectedMCPArguments...)
	if err := harness.ValidateProcessSpec(candidateProcess); err != nil {
		return ProcessPair{}, err
	}
	processes := ProcessPair{
		Baseline:              cloneProcessSpec(baselineProcess),
		Candidate:             cloneProcessSpec(candidateProcess),
		CandidateMCPArguments: append([]string(nil), expectedMCPArguments...),
	}
	if err := validateProcessPair(baseline, candidate, processes); err != nil {
		return ProcessPair{}, err
	}
	return processes, nil
}

func validateProcessPair(
	baseline, candidate harness.Invocation,
	processes ProcessPair,
) error {
	if err := harness.ValidateProcessSpec(processes.Baseline); err != nil {
		return fmt.Errorf("baseline process: %w", err)
	}
	if err := harness.ValidateProcessSpec(processes.Candidate); err != nil {
		return fmt.Errorf("candidate process: %w", err)
	}
	if processes.Baseline.Argv[0] != baseline.Executable ||
		!reflect.DeepEqual(processes.Baseline.Stdin, baseline.Prompt) ||
		!reflect.DeepEqual(processes.Baseline.Environment, baseline.Environment) ||
		processes.Baseline.Directory != baseline.WorkingDirectory ||
		processes.Baseline.TimeoutMillis != baseline.TimeoutMillis {
		return errors.New("rendered baseline does not match the approved common invocation")
	}
	if err := validateCommonProcessEncoding(
		baseline.HarnessIdentity.Kind,
		baseline,
		processes.Baseline,
	); err != nil {
		return err
	}
	if !reflect.DeepEqual(processes.Baseline.Stdin, processes.Candidate.Stdin) ||
		!reflect.DeepEqual(processes.Baseline.Environment, processes.Candidate.Environment) ||
		processes.Baseline.Directory != processes.Candidate.Directory ||
		processes.Baseline.TimeoutMillis != processes.Candidate.TimeoutMillis {
		return errors.New("rendered processes differ outside candidate argv")
	}
	if len(processes.CandidateMCPArguments) == 0 ||
		len(processes.Candidate.Argv) <= len(processes.Baseline.Argv) ||
		!reflect.DeepEqual(
			processes.Candidate.Argv[:len(processes.Baseline.Argv)],
			processes.Baseline.Argv,
		) ||
		!reflect.DeepEqual(
			processes.Candidate.Argv[len(processes.Baseline.Argv):],
			processes.CandidateMCPArguments,
		) {
		return errors.New("rendered candidate is not common argv plus the committed MCP encoding")
	}
	if len(baseline.MCPServers) != 0 || len(candidate.MCPServers) != 1 {
		return errors.New("rendered pair does not correspond to approved MCP cardinality")
	}
	if err := validateTreatmentEncoding(
		baseline.HarnessIdentity.Kind,
		candidate.MCPServers[0],
		processes.CandidateMCPArguments,
	); err != nil {
		return err
	}
	return nil
}

func validateCommonProcessEncoding(
	harnessKind string,
	invocation harness.Invocation,
	process harness.ProcessSpec,
) error {
	switch harnessKind {
	case "codex":
		if err := harnesscodex.ValidateCanonicalProcess(invocation, process); err != nil {
			return fmt.Errorf("validate code-owned Codex common process: %w", err)
		}
	case "fake":
		if err := fake.ValidateCanonicalProcess(invocation, process); err != nil {
			return fmt.Errorf("validate code-owned fake common process: %w", err)
		}
	default:
		return fmt.Errorf(
			"harness kind %q has no code-owned common-process validator",
			harnessKind,
		)
	}
	return nil
}

func validateTreatmentEncoding(
	harnessKind string,
	server harness.MCPServer,
	observed []string,
) error {
	var (
		expected []string
		err      error
	)
	switch harnessKind {
	case "codex":
		expected, err = harnesscodex.CanonicalMCPArguments(server)
	case "fake":
		expected, err = fake.CanonicalMCPArguments(server)
	default:
		return fmt.Errorf(
			"harness kind %q has no code-owned scopesifter treatment encoder",
			harnessKind,
		)
	}
	if err != nil {
		return fmt.Errorf("validate code-owned scopesifter treatment encoding: %w", err)
	}
	if !reflect.DeepEqual(observed, expected) {
		return errors.New(
			"candidate argv suffix differs from the code-owned scopesifter treatment encoding",
		)
	}
	return nil
}

func cloneProcessSpec(source harness.ProcessSpec) harness.ProcessSpec {
	clone := source
	clone.Environment = cloneMap(source.Environment)
	clone.Argv = append([]string(nil), source.Argv...)
	clone.Stdin = append([]byte(nil), source.Stdin...)
	return clone
}

func cloneProcessPair(source ProcessPair) ProcessPair {
	return ProcessPair{
		Baseline:  cloneProcessSpec(source.Baseline),
		Candidate: cloneProcessSpec(source.Candidate),
		CandidateMCPArguments: append(
			[]string(nil),
			source.CandidateMCPArguments...,
		),
	}
}
