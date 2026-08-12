package tokenbench

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/yapless/scopesifter/benchmarks/tokenbench/harness"
	"github.com/yapless/scopesifter/benchmarks/tokenbench/harness/fake"
	"github.com/yapless/scopesifter/benchmarks/tokenbench/runner"
	executionsnapshot "github.com/yapless/scopesifter/benchmarks/tokenbench/snapshot"
)

type recordingExecutor struct {
	mutateSource     bool
	integrityArm     Arm
	invalidArtifacts bool
	terminalArm      Arm
	failArm          Arm
	mu               sync.Mutex
	requests         []executionRequest
}

func (*recordingExecutor) Identity() ExecutorIdentity {
	return ExecutorIdentity{
		Kind:         "test",
		Version:      "test/v1",
		ConfigSHA256: SHA256([]byte("test executor config")),
	}
}

func (executor *recordingExecutor) Prepare(
	_ context.Context,
	request executionRequest,
) (preparedExecution, error) {
	return &recordingPrepared{executor: executor, request: request}, nil
}

type recordingPrepared struct {
	executor *recordingExecutor
	request  executionRequest
}

func (prepared *recordingPrepared) Abort(context.Context) error { return nil }

func (prepared *recordingPrepared) Execute(
	context.Context,
) (harness.RawExecution, error) {
	executor, request := prepared.executor, prepared.request
	executor.mu.Lock()
	executor.requests = append(executor.requests, request)
	executor.mu.Unlock()
	if executor.mutateSource {
		path := filepath.Join(request.Invocation.WorkingDirectory, "file.txt")
		if err := os.WriteFile(path, []byte("mutated\n"), 0o600); err != nil {
			return harness.RawExecution{}, err
		}
	}
	if executor.failArm == request.Arm {
		return harness.RawExecution{
			Artifacts:    []harness.Artifact{},
			ExitCode:     -1,
			LaunchFailed: true,
		}, errors.New("injected execution failure")
	}
	if executor.integrityArm == request.Arm {
		return harness.RawExecution{Artifacts: []harness.Artifact{{
				Name: "partial", MediaType: "application/octet-stream", Data: []byte("retained"),
			}}}, runner.NewIntegrityError(
				"fixture reset",
				errors.New("injected integrity failure"),
			)
	}
	toolCalls := []string{}
	if request.Arm == CandidateArm {
		toolCalls = []string{"scopesifter.inspect"}
	}
	stdout, err := json.Marshal(struct {
		FinalAnswer string        `json:"final_answer"`
		Model       string        `json:"model"`
		ToolCalls   []string      `json:"tool_calls"`
		Usage       harness.Usage `json:"usage"`
		Completed   bool          `json:"completed"`
	}{
		FinalAnswer: "answer",
		Model:       request.Invocation.Model,
		ToolCalls:   toolCalls,
		Usage: harness.Usage{
			InputTokens:  10,
			OutputTokens: 2,
		},
		Completed: true,
	})
	if err != nil {
		return harness.RawExecution{}, err
	}
	raw := harness.RawExecution{
		Stdout:    stdout,
		Stderr:    []byte{},
		Artifacts: []harness.Artifact{},
		ExitCode:  0,
	}
	if executor.invalidArtifacts {
		raw.Artifacts = []harness.Artifact{{Name: "duplicate", MediaType: "application/json"}, {Name: "duplicate", MediaType: "application/json"}}
	}
	if executor.terminalArm == request.Arm {
		raw.ExitCode = 9
	}
	return raw, nil
}

func TestPairExecuteRunsBothArmsAndPreservesSoleDelta(t *testing.T) {
	pair, _ := buildReadyPair(t, fake.Adapter{})
	executor := &recordingExecutor{}
	run, err := pair.executeWithBackend(context.Background(), executor, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Validate(); err != nil {
		t.Fatalf("validate run: %v", err)
	}
	if run.SchemaVersion != "tokenbench.run/v5" {
		t.Fatalf("run schema = %q", run.SchemaVersion)
	}
	oldVersion := run
	oldVersion.SchemaVersion = "tokenbench.run/v4"
	if err := oldVersion.Validate(); err == nil {
		t.Fatal("run/v4 was accepted as run/v5")
	}
	if run.Baseline.Observation == nil || run.Candidate.Observation == nil {
		t.Fatalf("missing observations: %+v", run)
	}
	if len(executor.requests) != 2 {
		t.Fatalf("got %d execution requests", len(executor.requests))
	}
	var baseline, candidate executionRequest
	for _, request := range executor.requests {
		if request.Arm == CandidateArm {
			candidate = request
		} else {
			baseline = request
		}
	}
	baselineInvocation := cloneInvocation(baseline.Invocation)
	candidateInvocation := cloneInvocation(candidate.Invocation)
	baselineInvocation.MCPServers = nil
	candidateInvocation.MCPServers = nil
	if !reflect.DeepEqual(baselineInvocation, candidateInvocation) {
		t.Fatal("executor received non-MCP semantic drift")
	}
	if !reflect.DeepEqual(baseline.Process.Stdin, candidate.Process.Stdin) ||
		!reflect.DeepEqual(baseline.Process.Environment, candidate.Process.Environment) ||
		!reflect.DeepEqual(
			candidate.Process.Argv[:len(baseline.Process.Argv)],
			baseline.Process.Argv,
		) {
		t.Fatal("executor received process drift outside the candidate suffix")
	}
}

func TestLivePublicationUsesOwnedSingleUseSnapshot(t *testing.T) {
	pair, _ := buildReadyPair(t, fake.Adapter{})
	run, err := pair.executeWithBackend(context.Background(), &recordingExecutor{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := sealRun(&run); err != nil {
		t.Fatal(err)
	}
	wantAnswer := run.Baseline.Observation.FinalAnswer
	wantStdout := append([]byte(nil), run.Baseline.Raw.Stdout...)

	// Every mutation below targets caller-owned public data. None of it may
	// alter the private snapshot later leased to evidence publication.
	run.Baseline.Observation.FinalAnswer = "caller mutation"
	run.Baseline.Raw.Stdout[0] ^= 0xff
	run.Plan.Baseline.Environment["MUTATED"] = "yes"
	run.Plan.Candidate.MCPServers[0].Arguments[0] = "mutated"

	snapshot, finish, err := AcquireLiveCapture(run)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Baseline.Observation.FinalAnswer != wantAnswer ||
		!reflect.DeepEqual(snapshot.Baseline.Raw.Stdout, wantStdout) {
		t.Fatal("caller mutation reached the owned publication snapshot")
	}
	if _, exists := snapshot.Plan.Baseline.Environment["MUTATED"]; exists ||
		snapshot.Plan.Candidate.MCPServers[0].Arguments[0] == "mutated" {
		t.Fatal("nested caller mutation reached the owned publication snapshot")
	}
	if err := snapshot.Validate(); err != nil {
		t.Fatalf("owned snapshot is invalid: %v", err)
	}
	finish(false)
	_, finish, err = AcquireLiveCapture(run)
	if err != nil {
		t.Fatalf("failed publication did not release authority: %v", err)
	}
	finish(true)
	if _, _, err := AcquireLiveCapture(run); err == nil {
		t.Fatal("consumed publication authority was reusable")
	}
}

func TestLivePublicationAuthorityWaitsForClosedExecutionBoundary(t *testing.T) {
	pair, _ := buildReadyPair(t, fake.Adapter{})
	run, err := pair.executeWithBackend(context.Background(), &recordingExecutor{}, 0)
	if err != nil {
		t.Fatal(err)
	}
	closed := false
	if err := sealRun(&run, func() bool { return closed }); err != nil {
		t.Fatal(err)
	}
	if _, _, err := AcquireLiveCapture(run); err == nil ||
		!strings.Contains(err.Error(), "publication boundary") {
		t.Fatalf("live capture was obtainable before Close: %v", err)
	}
	closed = true
	_, finish, err := AcquireLiveCapture(run)
	if err != nil {
		t.Fatalf("closed boundary did not release publication authority: %v", err)
	}
	finish(true)
}

func TestPairExecuteOrderIsCounterbalanced(t *testing.T) {
	pair, _ := buildReadyPair(t, fake.Adapter{})
	first := pair.executionOrder(0)
	second := pair.executionOrder(1)
	if first[0] == second[0] || first[1] == second[1] {
		t.Fatalf("orders are not counterbalanced: %v %v", first, second)
	}
}

func TestPairExecuteRetainsFailureAndRunsOtherArm(t *testing.T) {
	pair, _ := buildReadyPair(t, fake.Adapter{})
	executor := &recordingExecutor{failArm: CandidateArm}
	run, err := pair.executeWithBackend(context.Background(), executor, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(executor.requests) != 2 {
		t.Fatalf("ordinary failure stopped pair after %d arm(s)", len(executor.requests))
	}
	if run.Candidate.Failure == nil || run.Candidate.Observation != nil {
		t.Fatalf("candidate failure was not retained: %+v", run.Candidate)
	}
	if run.Baseline.Observation == nil {
		t.Fatalf("baseline did not run: %+v", run.Baseline)
	}
	if err := run.Validate(); err != nil {
		t.Fatalf("validate failed run record: %v", err)
	}
}

func TestPairExecuteStopsOnSourceMutation(t *testing.T) {
	pair, _ := buildReadyPair(t, fake.Adapter{})
	executor := &recordingExecutor{mutateSource: true}
	run, err := pair.executeWithBackend(context.Background(), executor, 0)
	if err == nil {
		t.Fatal("source mutation was accepted")
	}
	if run.SchemaVersion != RunSchemaVersion {
		t.Fatal("partial integrity-failed run was not returned")
	}
}

func TestPairExecuteClassifiesFailedTerminationBeforeDecode(t *testing.T) {
	pair, _ := buildReadyPair(t, fake.Adapter{})
	executor := &recordingExecutor{terminalArm: CandidateArm}
	run, err := pair.executeWithBackend(context.Background(), executor, 0)
	if err != nil {
		t.Fatal(err)
	}
	if run.Candidate.Failure == nil || run.Candidate.Failure.Kind != "nonzero_exit" ||
		run.Candidate.Observation != nil {
		t.Fatalf("failed termination was accepted: %+v", run.Candidate)
	}
	if err := run.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestClassifyTerminationUsesCgroupLimitCounters(t *testing.T) {
	tests := []struct {
		name     string
		set      func(*harness.ResourceOutcome)
		exitCode int
		kind     string
	}{
		{
			name: "oom kill",
			set: func(outcome *harness.ResourceOutcome) {
				setResourceCounter(outcome.MemoryEventsLocal, "oom_kill", 1)
			},
			kind: "memory_oom_kill",
		},
		{
			name: "oom allocation",
			set: func(outcome *harness.ResourceOutcome) {
				setResourceCounter(outcome.MemoryEventsLocal, "oom", 1)
			},
			exitCode: 7,
			kind:     "memory_oom",
		},
		{
			name: "pids max",
			set: func(outcome *harness.ResourceOutcome) {
				setResourceCounter(outcome.PIDsEvents, "max", 1)
			},
			exitCode: 7,
			kind:     "pids_limit",
		},
		{
			name: "memory max",
			set: func(outcome *harness.ResourceOutcome) {
				setResourceCounter(outcome.MemoryEventsLocal, "max", 1)
			},
			exitCode: 7,
			kind:     "memory_limit",
		},
		{
			name: "cpu throttling is evidence only",
			set: func(outcome *harness.ResourceOutcome) {
				setResourceCounter(outcome.CPUStat, "nr_throttled", 1)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			resources := resourceOutcomeFixture()
			test.set(resources)
			failure, err := classifyTermination(harness.RawExecution{
				Resources: resources,
				ExitCode:  test.exitCode,
			})
			if err != nil {
				t.Fatal(err)
			}
			if test.kind == "" {
				if failure != nil {
					t.Fatalf("classification = %+v, want clean execution", failure)
				}
				return
			}
			if failure == nil || failure.Kind != test.kind {
				t.Fatalf("classification = %+v, want %s", failure, test.kind)
			}
		})
	}
}

func resourceOutcomeFixture() *harness.ResourceOutcome {
	return &harness.ResourceOutcome{
		Version: harness.ResourceOutcomeVersion,
		CPUStat: []harness.ResourceCounter{
			{Name: "nr_periods"},
			{Name: "nr_throttled"},
			{Name: "system_usec"},
			{Name: "throttled_usec"},
			{Name: "usage_usec"},
			{Name: "user_usec"},
		},
		MemoryEvents: []harness.ResourceCounter{
			{Name: "high"},
			{Name: "low"},
			{Name: "max"},
			{Name: "oom"},
			{Name: "oom_kill"},
		},
		MemoryEventsLocal: []harness.ResourceCounter{
			{Name: "high"},
			{Name: "low"},
			{Name: "max"},
			{Name: "oom"},
			{Name: "oom_kill"},
		},
		PIDsEvents: []harness.ResourceCounter{{Name: "max"}},
	}
}

func setResourceCounter(counters []harness.ResourceCounter, name string, value uint64) {
	for index := range counters {
		if counters[index].Name == name {
			counters[index].Value = value
			return
		}
	}
}

func TestPairExecuteAbortsOnExecutorIntegrityFailure(t *testing.T) {
	pair, _ := buildReadyPair(t, fake.Adapter{})
	first := pair.executionOrder(0)[0]
	executor := &recordingExecutor{integrityArm: first}
	run, err := pair.executeWithBackend(context.Background(), executor, 0)
	if err == nil {
		t.Fatal("typed executor integrity failure was treated as ordinary")
	}
	failed := run.Baseline
	if first == CandidateArm {
		failed = run.Candidate
	}
	if len(failed.Raw.Artifacts) != 1 || string(failed.Raw.Artifacts[0].Data) != "retained" {
		t.Fatalf("integrity failure dropped partial evidence: %+v", failed)
	}
	if len(executor.requests) != 1 {
		t.Fatalf("other arm ran after integrity failure: %d requests", len(executor.requests))
	}
}

func TestPairExecuteAbortsOnInvalidArtifacts(t *testing.T) {
	pair, _ := buildReadyPair(t, fake.Adapter{})
	executor := &recordingExecutor{invalidArtifacts: true}
	if _, err := pair.executeWithBackend(context.Background(), executor, 0); err == nil {
		t.Fatal("invalid runner artifacts were retained as an ordinary failure")
	}
}

func TestPairExecuteRejectsOpenRunnerConstruction(t *testing.T) {
	pair, _ := buildReadyPair(t, fake.Adapter{})
	executor, err := runner.New(runner.Config{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pair.Execute(context.Background(), executor, 0); err == nil ||
		!strings.Contains(err.Error(), "not the conformant built-in Codex runner") {
		t.Fatalf("generic runner reached publication execution: %v", err)
	}
	if _, err := pair.Execute(context.Background(), nil, 0); err == nil {
		t.Fatal("nil runner was accepted")
	}
}

func TestExecutorPolicyMustExactlyMatchImmutableExecutionInputs(t *testing.T) {
	pair := Pair{
		executionInputs: executionsnapshot.ExecutionInputs{
			ReadOnlyPaths:         []string{"/snapshot/source", "/snapshot/cache"},
			ExecutablePaths:       []string{"/snapshot/bin/scopesifter", "/snapshot/bin/runner"},
			ScopeSifterExecutable: "/snapshot/bin/scopesifter",
		},
		originInputs: executionsnapshot.OriginInputs{
			ScopeSifter: executionsnapshot.FileOrigin{SHA256: strings.Repeat("a", 64)},
			Runner:      executionsnapshot.FileOrigin{SHA256: strings.Repeat("b", 64)},
		},
	}
	want := runner.ConformancePolicyIdentity{
		SchemaVersion: runner.ConformancePolicySchemaVersion,
		ReadOnlyPaths: []string{
			"/snapshot/cache",
			"/snapshot/source",
		},
		ExecutablePaths: []string{
			"/snapshot/bin/runner",
			"/snapshot/bin/scopesifter",
		},
		CommonMCPExecutable:       "/snapshot/bin/scopesifter",
		CommonMCPExecutableSHA256: strings.Repeat("a", 64),
		ArmInitExecutableSHA256:   strings.Repeat("b", 64),
	}
	if err := pair.validateExecutorPolicyIdentity(want); err != nil {
		t.Fatalf("exact policy rejected: %v", err)
	}
	if !reflect.DeepEqual(
		pair.executionInputs.ReadOnlyPaths,
		[]string{"/snapshot/source", "/snapshot/cache"},
	) {
		t.Fatal("policy validation mutated execution inputs while sorting")
	}

	tests := []struct {
		name   string
		mutate func(*runner.ConformancePolicyIdentity)
	}{
		{"schema", func(value *runner.ConformancePolicyIdentity) { value.SchemaVersion = "forged" }},
		{"extra read root", func(value *runner.ConformancePolicyIdentity) {
			value.ReadOnlyPaths = append(value.ReadOnlyPaths, "/host")
		}},
		{"missing read root", func(value *runner.ConformancePolicyIdentity) {
			value.ReadOnlyPaths = value.ReadOnlyPaths[:1]
		}},
		{"extra executable", func(value *runner.ConformancePolicyIdentity) {
			value.ExecutablePaths = append(value.ExecutablePaths, "/bin/sh")
		}},
		{"missing executable", func(value *runner.ConformancePolicyIdentity) {
			value.ExecutablePaths = value.ExecutablePaths[:1]
		}},
		{"MCP path", func(value *runner.ConformancePolicyIdentity) { value.CommonMCPExecutable = "/other" }},
		{"MCP digest", func(value *runner.ConformancePolicyIdentity) {
			value.CommonMCPExecutableSHA256 = strings.Repeat("c", 64)
		}},
		{"arm-init digest", func(value *runner.ConformancePolicyIdentity) { value.ArmInitExecutableSHA256 = strings.Repeat("d", 64) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			forged := want
			forged.ReadOnlyPaths = append([]string(nil), want.ReadOnlyPaths...)
			forged.ExecutablePaths = append([]string(nil), want.ExecutablePaths...)
			test.mutate(&forged)
			if err := pair.validateExecutorPolicyIdentity(forged); err == nil {
				t.Fatal("mismatched runner policy was accepted")
			}
		})
	}
}
