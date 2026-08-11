package tokenbench

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/dkropachev/repo-view/benchmarks/tokenbench/harness"
	"github.com/dkropachev/repo-view/benchmarks/tokenbench/runner"
)

// RunSchemaVersion is the current reconstructed run schema.
const RunSchemaVersion = "tokenbench.run/v2"

// Arm identifies one side of a paired attempt.
type Arm string

const (
	BaselineArm  Arm = "baseline"
	CandidateArm Arm = "candidate"
)

// ExecutorIdentity pins the runner implementation and all non-secret runner
// configuration. ConfigSHA256 must commit output limits, cleanup policy,
// capture-proxy behavior, reset policy, and any containment configuration.
type ExecutorIdentity struct {
	Kind         string `json:"kind"`
	Version      string `json:"version"`
	ConfigSHA256 string `json:"config_sha256"`
}

// executionRequest is issued only by an adapter-bound Pair. Process and
// Invocation are defensive copies of the already parity-checked arm.
type executionRequest struct { //nolint:govet,nolintlint // Preserve the stable request field order.
	Arm        Arm                 `json:"arm"`
	Repetition int                 `json:"repetition"`
	Invocation harness.Invocation  `json:"invocation"`
	Process    harness.ProcessSpec `json:"process"`
}

// preparedExecution is an opaque, single-use execution capability bound to one
// approved request. Abort is idempotent and releases partially initialized or
// completed arm-local state without using live model credentials.
type preparedExecution interface {
	Execute(context.Context) (harness.RawExecution, error)
	Abort(context.Context) error
}

// executionBackend is private test scaffolding. Production publication accepts
// only the concrete runner.Executor below, so another package cannot fabricate
// execution provenance by implementing an open interface.
type executionBackend interface {
	Identity() ExecutorIdentity
	Prepare(context.Context, executionRequest) (preparedExecution, error)
}

type conformantRunnerBridge struct {
	executor *runner.Executor
}

func (bridge conformantRunnerBridge) Identity() ExecutorIdentity {
	identity := bridge.executor.Identity()
	return ExecutorIdentity{
		Kind:         identity.Kind,
		Version:      identity.Version,
		ConfigSHA256: identity.ConfigSHA256,
	}
}

func (bridge conformantRunnerBridge) Prepare(
	ctx context.Context,
	request executionRequest,
) (preparedExecution, error) {
	arm := runner.BaselineArm
	if request.Arm == CandidateArm {
		arm = runner.CandidateArm
	}
	return bridge.executor.Prepare(ctx, runner.ExecutionRequest{
		Arm:        arm,
		Repetition: request.Repetition,
		Invocation: cloneInvocation(request.Invocation),
		Process:    cloneProcessSpec(request.Process),
	})
}

// AttemptFailure classifies an ordinary arm failure. Integrity failures are
// returned from Pair.Execute and stop the pair instead of being recorded here.
type AttemptFailure struct {
	Stage   string `json:"stage"`
	Kind    string `json:"kind"`
	Message string `json:"message"`
}

// ArmRun contains raw capture and a normalized observation when decoding
// succeeds. Failed arms are retained; they are never silently dropped.
type ArmRun struct { //nolint:govet,nolintlint // Field order defines tokenbench.run/v2 JSON.
	Failure     *AttemptFailure      `json:"failure,omitempty"`
	Observation *harness.Observation `json:"observation,omitempty"`
	Raw         harness.RawExecution `json:"raw"`
	Arm         Arm                  `json:"arm"`
}

// Run is one randomized, paired execution. The embedded plan is audit data;
// it still cannot be used as an execution capability.
type Run struct { //nolint:govet,nolintlint // Field order defines tokenbench.run/v2 JSON.
	Plan             ResolvedPlan     `json:"plan"`
	Baseline         ArmRun           `json:"baseline"`
	Candidate        ArmRun           `json:"candidate"`
	ExecutorIdentity ExecutorIdentity `json:"executor_identity"`
	Order            [2]Arm           `json:"order"`
	SchemaVersion    string           `json:"schema_version"`
	Repetition       int              `json:"repetition"`

	publication *publicationAuthority
}

type publicationAuthority struct {
	snapshot Run
	ready    func() bool
	digest   string
	mu       sync.Mutex
	state    publicationState
}

type publicationState uint8

const (
	publicationReady publicationState = iota
	publicationInProgress
	publicationConsumed
)

// Execute builds the pair exactly once, chooses the precommitted
// counterbalanced order, reverifies every immutable input around each external
// call, and continues the other arm after an ordinary execution/decode failure.
// Any integrity failure aborts immediately and returns a partial run plus an
// error; such a run must never be analyzed as a valid pair.
func (pair Pair) Execute(
	ctx context.Context,
	executor *runner.Executor,
	repetition int,
) (Run, error) {
	if executor == nil {
		return Run{}, errors.New("executor is required")
	}
	if !executor.Publishable() || !executor.Conformant() {
		return Run{}, errors.New("executor is not the conformant built-in Codex runner")
	}
	if !pair.Publishable() {
		return Run{}, errors.New("pair has no live immutable execution snapshot authority")
	}
	if err := pair.validateExecutorPolicy(executor); err != nil {
		return Run{}, err
	}
	run, err := pair.executeWithBackend(
		ctx,
		conformantRunnerBridge{executor: executor},
		repetition,
	)
	if err != nil {
		return run, err
	}
	if err := pair.validateExecutorPolicy(executor); err != nil {
		return run, err
	}
	if err := sealRun(&run, func() bool {
		return executor.PublicationBoundaryClosed() && pair.ExecutionSnapshotClosed()
	}); err != nil {
		return Run{}, err
	}
	return run, nil
}

func (pair Pair) validateExecutorPolicy(executor *runner.Executor) error {
	policy, err := executor.ConformancePolicy()
	if err != nil {
		return fmt.Errorf("read conformant executor policy: %w", err)
	}
	return pair.validateExecutorPolicyIdentity(policy)
}

func (pair Pair) validateExecutorPolicyIdentity(policy runner.ConformancePolicyIdentity) error {
	readOnly := append([]string(nil), pair.executionInputs.ReadOnlyPaths...)
	executable := append([]string(nil), pair.executionInputs.ExecutablePaths...)
	sort.Strings(readOnly)
	sort.Strings(executable)
	expected := runner.ConformancePolicyIdentity{
		SchemaVersion:             runner.ConformancePolicySchemaVersion,
		ReadOnlyPaths:             readOnly,
		ExecutablePaths:           executable,
		CommonMCPExecutable:       pair.executionInputs.RepoViewExecutable,
		CommonMCPExecutableSHA256: pair.originInputs.RepoView.SHA256,
		ArmInitExecutableSHA256:   pair.originInputs.Runner.SHA256,
	}
	if !reflect.DeepEqual(policy, expected) {
		return errors.New("conformant executor policy differs from the pair execution snapshot")
	}
	return nil
}

func (pair Pair) executeWithBackend(
	ctx context.Context,
	executor executionBackend,
	repetition int,
) (Run, error) {
	identity := executor.Identity()
	if err := validateExecutorIdentity(identity); err != nil {
		return Run{}, err
	}
	if repetition < 0 || repetition >= pair.repetitions {
		return Run{}, fmt.Errorf(
			"repetition %d is outside [0,%d)",
			repetition,
			pair.repetitions,
		)
	}
	processes, err := pair.Build(ctx)
	if err != nil {
		return Run{}, fmt.Errorf("build execution pair: %w", err)
	}
	plan, err := pair.plan(processes)
	if err != nil {
		return Run{}, fmt.Errorf("bind execution plan: %w", err)
	}
	order := pair.executionOrder(repetition)
	run := Run{
		Plan:             plan,
		Baseline:         ArmRun{Arm: BaselineArm},
		Candidate:        ArmRun{Arm: CandidateArm},
		ExecutorIdentity: identity,
		Order:            order,
		SchemaVersion:    RunSchemaVersion,
		Repetition:       repetition,
	}
	for _, arm := range order {
		if err := pair.reverifyExecutionInputs(ctx, pair.baseline, pair.candidate); err != nil {
			return run, fmt.Errorf("integrity check before %s: %w", arm, err)
		}
		invocation, process := pair.armInputs(arm, processes)
		prepared, prepareErr := executor.Prepare(ctx, executionRequest{
			Arm:        arm,
			Repetition: repetition,
			Invocation: invocation,
			Process:    process,
		})
		if prepareErr != nil {
			cleanupErr := abortPrepared(prepared)
			return run, errors.Join(
				fmt.Errorf("prepare %s execution: %w", arm, prepareErr),
				cleanupErr,
			)
		}
		if prepared == nil {
			return run, fmt.Errorf("prepare %s execution returned no capability", arm)
		}
		if err := pair.reverifyExecutionInputs(ctx, pair.baseline, pair.candidate); err != nil {
			return run, errors.Join(
				fmt.Errorf("integrity check after %s preparation: %w", arm, err),
				abortPrepared(prepared),
			)
		}
		raw, executeErr := prepared.Execute(ctx)
		cleanupErr := abortPrepared(prepared)
		if err := pair.reverifyExecutionInputs(ctx, pair.baseline, pair.candidate); err != nil {
			return run, errors.Join(
				fmt.Errorf("integrity check after %s execution: %w", arm, err),
				cleanupErr,
			)
		}
		if cleanupErr != nil {
			return run, fmt.Errorf("cleanup %s execution: %w", arm, cleanupErr)
		}
		if err := ctx.Err(); err != nil {
			return run, fmt.Errorf("paired execution context ended during %s: %w", arm, err)
		}
		armRun := ArmRun{Arm: arm, Raw: cloneRawExecution(raw)}
		if err := harness.ValidateArtifacts(raw.Artifacts); err != nil {
			return run, fmt.Errorf("invalid %s capture artifacts: %w", arm, err)
		}
		if executeErr != nil {
			var integrityError *runner.IntegrityError
			if errors.As(executeErr, &integrityError) {
				setArmRun(&run, armRun)
				return run, fmt.Errorf("%s executor integrity failure: %w", arm, executeErr)
			}
			terminal, terminalErr := classifyTermination(raw)
			if terminalErr != nil || terminal == nil || terminal.Kind != "launch_failed" {
				return run, errors.Join(
					fmt.Errorf("%s executor returned an unclassified ordinary error", arm),
					terminalErr,
				)
			}
			armRun.Failure = terminal
			setArmRun(&run, armRun)
			continue
		}
		terminal, terminalErr := classifyTermination(raw)
		if terminalErr != nil {
			return run, fmt.Errorf("invalid %s termination state: %w", arm, terminalErr)
		}
		if terminal != nil {
			armRun.Failure = terminal
			setArmRun(&run, armRun)
			continue
		}
		observation, decodeErr := pair.adapter.Decode(ctx, cloneRawExecution(raw))
		if err := pair.reverifyExecutionInputs(ctx, pair.baseline, pair.candidate); err != nil {
			return run, fmt.Errorf("integrity check after %s decode: %w", arm, err)
		}
		if decodeErr != nil {
			armRun.Failure = fixedFailure(
				"decode",
				"decoder_error",
				"pinned adapter decoder rejected the captured output",
			)
			setArmRun(&run, armRun)
			continue
		}
		if observation.ToolCalls == nil {
			observation.ToolCalls = []string{}
		}
		if err := validateObservation(arm, invocation, observation); err != nil {
			armRun.Failure = fixedFailure(
				"normalize",
				"invalid_observation",
				"decoded observation violated the pinned normalization contract",
			)
			setArmRun(&run, armRun)
			continue
		}
		observation = harness.CloneObservation(observation)
		armRun.Observation = &observation
		setArmRun(&run, armRun)
	}
	return run, nil
}

func (pair Pair) armInputs(
	arm Arm,
	processes ProcessPair,
) (harness.Invocation, harness.ProcessSpec) {
	if arm == CandidateArm {
		return pair.Candidate(), cloneProcessSpec(processes.Candidate)
	}
	return pair.Baseline(), cloneProcessSpec(processes.Baseline)
}

func (pair Pair) executionOrder(repetition int) [2]Arm {
	return scheduledOrder(pair.suiteSHA256, pair.seed, repetition)
}

// ScheduledOrder recomputes the deterministic counterbalanced order committed
// by a plan. It is used by offline evidence auditors.
func ScheduledOrder(
	suiteSHA256 string,
	seed uint64,
	repetition int,
) ([2]Arm, error) {
	if !ValidSHA256(suiteSHA256) {
		return [2]Arm{}, errors.New("schedule suite digest is invalid")
	}
	if repetition < 0 {
		return [2]Arm{}, errors.New("schedule repetition must be nonnegative")
	}
	return scheduledOrder(suiteSHA256, seed, repetition), nil
}

func scheduledOrder(suiteSHA256 string, seed uint64, repetition int) [2]Arm {
	seedBytes := make([]byte, 8)
	binary.BigEndian.PutUint64(seedBytes, seed)
	digest := sha256.Sum256(append(append([]byte(suiteSHA256), 0), seedBytes...))
	candidateFirst := digest[0]&1 == 1
	if repetition%2 == 1 {
		candidateFirst = !candidateFirst
	}
	if candidateFirst {
		return [2]Arm{CandidateArm, BaselineArm}
	}
	return [2]Arm{BaselineArm, CandidateArm}
}

func validateExecutorIdentity(identity ExecutorIdentity) error {
	switch {
	case !validPublicText(identity.Kind, 128):
		return errors.New("executor identity kind is required")
	case strings.TrimSpace(identity.Kind) != identity.Kind:
		return errors.New("executor identity kind must be canonical")
	case !validPublicText(identity.Version, 256):
		return errors.New("executor identity version is required")
	case strings.TrimSpace(identity.Version) != identity.Version:
		return errors.New("executor identity version must be canonical")
	case !ValidSHA256(identity.ConfigSHA256):
		return errors.New("executor configuration digest is invalid")
	default:
		return nil
	}
}

func validateObservation(
	arm Arm,
	invocation harness.Invocation,
	observation harness.Observation,
) error {
	if err := harness.ValidateUsage(observation.Usage); err != nil {
		return err
	}
	switch {
	case !observation.Completed:
		return errors.New("harness did not report a completed turn")
	case !validPublicText(observation.Model, 512):
		return errors.New("observed model contains invalid text")
	case observation.Model != invocation.Model:
		return fmt.Errorf(
			"observed model %q does not match pinned model %q",
			observation.Model,
			invocation.Model,
		)
	case !validPublicText(observation.FinalAnswer, 16<<20):
		return errors.New("completed turn final answer contains invalid text")
	case strings.TrimSpace(observation.FinalAnswer) == "":
		return errors.New("completed turn has no final answer")
	case observation.ToolCalls == nil:
		return errors.New("tool calls must be a canonical array")
	case len(observation.ToolCalls) > harness.MaxObservationToolCalls:
		return errors.New("observation tool-call count exceeds its schema limit")
	}
	allowedRepoView := map[string]struct{}{
		"repo_view.changed": {},
		"repo_view.find":    {},
		"repo_view.inspect": {},
		"repo_view.outline": {},
	}
	for _, call := range observation.ToolCalls {
		if !validPublicText(call, 512) {
			return errors.New("observation tool call contains invalid text")
		}
		if strings.HasPrefix(call, "repo_view.") {
			if arm == BaselineArm {
				return errors.New("baseline observation contains a repo_view tool call")
			}
			if _, ok := allowedRepoView[call]; !ok {
				return fmt.Errorf("candidate observation contains unsupported repo_view call %q", call)
			}
		}
	}
	return nil
}

func fixedFailure(stage, kind, message string) *AttemptFailure {
	return &AttemptFailure{Stage: stage, Kind: kind, Message: message}
}

func classifyTermination(raw harness.RawExecution) (*AttemptFailure, error) {
	if raw.Resources != nil {
		if err := harness.ValidateResourceOutcome(raw.Resources); err != nil {
			return nil, fmt.Errorf("invalid resource outcome: %w", err)
		}
		if failure := classifyDestructiveResourceOutcome(raw.Resources); failure != nil {
			return failure, nil
		}
	}
	if raw.LaunchFailed {
		if raw.ExitCode != -1 || raw.TimedOut || raw.Cancelled ||
			raw.StdoutTruncated || raw.StderrTruncated {
			return nil, errors.New("launch_failed conflicts with process termination flags")
		}
		return fixedFailure(
			"execute",
			"launch_failed",
			"approved process could not be launched",
		), nil
	}
	if raw.TimedOut && (raw.Cancelled || raw.StdoutTruncated || raw.StderrTruncated) {
		return nil, errors.New("timeout conflicts with cancellation or output-limit flags")
	}
	if raw.Cancelled && (raw.StdoutTruncated || raw.StderrTruncated) {
		return nil, errors.New("cancellation conflicts with output-limit flags")
	}
	if raw.TimedOut {
		return fixedFailure("execute", "timeout", "approved process timed out"), nil
	}
	if raw.Cancelled {
		return fixedFailure("execute", "cancelled", "approved process was cancelled"), nil
	}
	if raw.StdoutTruncated || raw.StderrTruncated {
		return fixedFailure(
			"capture",
			"output_limit",
			"captured process output exceeded its configured limit",
		), nil
	}
	if raw.ExitCode != 0 {
		if raw.Resources != nil {
			if failure := classifyFailedResourceOutcome(raw.Resources); failure != nil {
				return failure, nil
			}
		}
		return fixedFailure(
			"execute",
			"nonzero_exit",
			fmt.Sprintf("approved process exited with code %d", raw.ExitCode),
		), nil
	}
	return nil, nil
}

func classifyDestructiveResourceOutcome(resources *harness.ResourceOutcome) *AttemptFailure {
	memory := resources.MemoryEventsLocal
	if value, ok := harness.ResourceCounterValue(memory, "oom_group_kill"); ok && value != 0 {
		return fixedFailure(
			"execute",
			"memory_oom_group_kill",
			"the arm cgroup was terminated by an out-of-memory group kill",
		)
	}
	if value, _ := harness.ResourceCounterValue(memory, "oom_kill"); value != 0 {
		return fixedFailure(
			"execute",
			"memory_oom_kill",
			"the arm cgroup terminated a process after an out-of-memory condition",
		)
	}
	return nil
}

func classifyFailedResourceOutcome(resources *harness.ResourceOutcome) *AttemptFailure {
	memory := resources.MemoryEventsLocal
	if value, _ := harness.ResourceCounterValue(memory, "oom"); value != 0 {
		return fixedFailure(
			"execute",
			"memory_oom",
			"the arm cgroup encountered an out-of-memory allocation failure",
		)
	}
	if value, _ := harness.ResourceCounterValue(resources.PIDsEvents, "max"); value != 0 {
		return fixedFailure(
			"execute",
			"pids_limit",
			"the arm cgroup reached its process limit",
		)
	}
	if value, _ := harness.ResourceCounterValue(memory, "max"); value != 0 {
		return fixedFailure(
			"execute",
			"memory_limit",
			"the arm cgroup reached its memory limit",
		)
	}
	// cpu.stat throttling, memory.high, and recovered max/oom/pids events are
	// evidence, not terminal failures. They refine classification only when the
	// approved process itself also failed.
	return nil
}

func validPublicText(value string, maximum int) bool {
	if value == "" {
		return false
	}
	return len(value) <= maximum && utf8.ValidString(value) &&
		!strings.ContainsRune(value, '\x00')
}

func validateAttemptFailure(
	failure *AttemptFailure,
	raw harness.RawExecution,
) error {
	if failure == nil {
		return nil
	}
	if !validPublicText(failure.Stage, 64) ||
		!validPublicText(failure.Kind, 64) ||
		!validPublicText(failure.Message, 1024) {
		return errors.New("attempt failure contains invalid public text")
	}
	terminal, err := classifyTermination(raw)
	if err != nil {
		return err
	}
	if terminal != nil {
		if *failure != *terminal {
			return errors.New("attempt failure does not match raw termination state")
		}
		return nil
	}
	allowed := []*AttemptFailure{
		fixedFailure("decode", "decoder_error", "pinned adapter decoder rejected the captured output"),
		fixedFailure("normalize", "invalid_observation", "decoded observation violated the pinned normalization contract"),
	}
	for _, expected := range allowed {
		if *failure == *expected {
			return nil
		}
	}
	return errors.New("clean raw execution has an unsupported failure classification")
}

func setArmRun(run *Run, armRun ArmRun) {
	if armRun.Arm == CandidateArm {
		run.Candidate = armRun
		return
	}
	run.Baseline = armRun
}

func abortPrepared(prepared preparedExecution) error {
	if prepared == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return prepared.Abort(ctx)
}

func cloneRawExecution(source harness.RawExecution) harness.RawExecution {
	clone := source
	clone.Stdout = append([]byte(nil), source.Stdout...)
	clone.Stderr = append([]byte(nil), source.Stderr...)
	clone.Artifacts = make([]harness.Artifact, len(source.Artifacts))
	for index, artifact := range source.Artifacts {
		clone.Artifacts[index] = artifact
		clone.Artifacts[index].Data = append([]byte(nil), artifact.Data...)
	}
	clone.Resources = harness.CloneResourceOutcome(source.Resources)
	return clone
}

// Validate recomputes all local run invariants. It does not replay a decoder;
// replay requires the pinned adapter implementation and publishes new evidence.
func (run Run) Validate() error {
	if run.SchemaVersion != RunSchemaVersion {
		return fmt.Errorf("unexpected run schema %q", run.SchemaVersion)
	}
	if err := validateExecutorIdentity(run.ExecutorIdentity); err != nil {
		return err
	}
	if err := run.Plan.Validate(); err != nil {
		return err
	}
	if run.Repetition < 0 || run.Repetition >= run.Plan.Repetitions {
		return fmt.Errorf(
			"run repetition %d is outside [0,%d)",
			run.Repetition,
			run.Plan.Repetitions,
		)
	}
	expectedOrder := scheduledOrder(
		run.Plan.SuiteSHA256,
		run.Plan.Seed,
		run.Repetition,
	)
	if run.Order != expectedOrder {
		return fmt.Errorf("run order %v does not match committed order %v", run.Order, expectedOrder)
	}
	for expected, armRun := range map[Arm]ArmRun{
		BaselineArm:  run.Baseline,
		CandidateArm: run.Candidate,
	} {
		if armRun.Arm != expected {
			return fmt.Errorf("%s run has arm %q", expected, armRun.Arm)
		}
		if len(armRun.Raw.Stdout) > harness.MaxRawStreamBytes ||
			len(armRun.Raw.Stderr) > harness.MaxRawStreamBytes {
			return fmt.Errorf("%s raw stream exceeds its schema byte limit", expected)
		}
		if err := harness.ValidateArtifacts(armRun.Raw.Artifacts); err != nil {
			return err
		}
		if armRun.Raw.Resources != nil {
			if err := harness.ValidateResourceOutcome(armRun.Raw.Resources); err != nil {
				return fmt.Errorf("%s raw resources: %w", expected, err)
			}
		}
		if (armRun.Failure == nil) == (armRun.Observation == nil) {
			return fmt.Errorf("%s must contain exactly one observation or failure", expected)
		}
		if err := validateAttemptFailure(armRun.Failure, armRun.Raw); err != nil {
			return fmt.Errorf("%s failure: %w", expected, err)
		}
		if armRun.Observation != nil {
			terminal, err := classifyTermination(armRun.Raw)
			if err != nil {
				return err
			}
			if terminal != nil {
				return fmt.Errorf("%s successful observation has failed termination state", expected)
			}
			invocation := run.Plan.Baseline
			if expected == CandidateArm {
				invocation = run.Plan.Candidate
			}
			if err := validateObservation(expected, invocation, *armRun.Observation); err != nil {
				return err
			}
		}
	}
	return nil
}

func sealRun(run *Run, readiness ...func() bool) error {
	if len(readiness) > 1 {
		return errors.New("run publication readiness gate is ambiguous")
	}
	if err := run.Validate(); err != nil {
		return err
	}
	snapshot, err := cloneRunForPublication(*run)
	if err != nil {
		return err
	}
	if err := snapshot.Validate(); err != nil {
		return err
	}
	digest, err := JSONSHA256(snapshot)
	if err != nil {
		return err
	}
	var ready func() bool
	if len(readiness) == 1 {
		if readiness[0] == nil {
			return errors.New("run publication readiness gate is nil")
		}
		ready = readiness[0]
	}
	run.publication = &publicationAuthority{
		snapshot: snapshot,
		digest:   digest,
		ready:    ready,
		state:    publicationReady,
	}
	return nil
}

func cloneRunForPublication(source Run) (Run, error) {
	source.publication = nil
	raw, err := json.Marshal(source)
	if err != nil {
		return Run{}, fmt.Errorf("encode publication snapshot: %w", err)
	}
	var clone Run
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&clone); err != nil {
		return Run{}, fmt.Errorf("decode publication snapshot: %w", err)
	}
	return clone, nil
}

// AcquireLiveCapture leases the immutable, runner-owned snapshot sealed by a
// successful Pair.Execute. The returned Run shares no maps, slices, pointers,
// or artifact bytes with the caller's public Run value. finish(true) consumes
// the authority after publication; finish(false) permits a safe retry.
func AcquireLiveCapture(run Run) (snapshot Run, finish func(bool), err error) {
	authority := run.publication
	if authority == nil {
		return Run{}, nil, errors.New("run is audit-only and has no live capture authority")
	}
	authority.mu.Lock()
	if authority.ready != nil && !authority.ready() {
		authority.mu.Unlock()
		return Run{}, nil, errors.New(
			"live capture publication boundary is not fully closed",
		)
	}
	switch authority.state {
	case publicationReady:
		authority.state = publicationInProgress
	case publicationInProgress:
		authority.mu.Unlock()
		return Run{}, nil, errors.New("live capture publication is already in progress")
	case publicationConsumed:
		authority.mu.Unlock()
		return Run{}, nil, errors.New("live capture publication authority was already consumed")
	default:
		authority.mu.Unlock()
		return Run{}, nil, errors.New("live capture publication authority is invalid")
	}
	if !ValidSHA256(authority.digest) {
		authority.state = publicationConsumed
		authority.mu.Unlock()
		return Run{}, nil, errors.New("live capture publication authority is corrupt")
	}
	digest, digestErr := JSONSHA256(authority.snapshot)
	if digestErr != nil || digest != authority.digest {
		authority.state = publicationConsumed
		authority.mu.Unlock()
		return Run{}, nil, errors.New("live capture publication snapshot is corrupt")
	}
	snapshot, cloneErr := cloneRunForPublication(authority.snapshot)
	if cloneErr != nil {
		authority.state = publicationReady
		authority.mu.Unlock()
		return Run{}, nil, cloneErr
	}
	authority.mu.Unlock()

	var once sync.Once
	finish = func(published bool) {
		once.Do(func() {
			authority.mu.Lock()
			if authority.state == publicationInProgress {
				if published {
					authority.state = publicationConsumed
				} else {
					authority.state = publicationReady
				}
			}
			authority.mu.Unlock()
		})
	}
	return snapshot, finish, nil
}

// ValidateLiveCapture proves that this value is the unchanged result of a
// completed live Pair.Execute call. Decoded or caller-constructed Run values
// are audit-only and cannot be republished as fresh capture evidence.
func (run Run) ValidateLiveCapture() error {
	if run.publication == nil {
		return errors.New("run is audit-only and has no live capture authority")
	}
	if err := run.Validate(); err != nil {
		return err
	}
	digest, err := JSONSHA256(run)
	if err != nil {
		return err
	}
	run.publication.mu.Lock()
	want := run.publication.digest
	state := run.publication.state
	run.publication.mu.Unlock()
	if digest != want || state == publicationConsumed {
		return errors.New("live run changed after capture")
	}
	return nil
}
