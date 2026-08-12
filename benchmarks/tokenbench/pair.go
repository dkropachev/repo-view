package tokenbench

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"unicode/utf8"

	"github.com/yapless/scopesifter/benchmarks/tokenbench/harness"
	"github.com/yapless/scopesifter/benchmarks/tokenbench/internal/selfexec"
	executionsnapshot "github.com/yapless/scopesifter/benchmarks/tokenbench/snapshot"
	"github.com/yapless/scopesifter/benchmarks/tokenbench/source"
)

const (
	// PlanSchemaVersion is the schema emitted after pair resolution.
	PlanSchemaVersion = "tokenbench.plan/v6"
	// MaximumPlanObjectBytes is the common pre-execution and evidence-store
	// ceiling for one canonical plan. Keeping the format limit here lets Pair
	// reject an oversized embedded snapshot before either arm is launched.
	MaximumPlanObjectBytes = 64 << 20
)

// ScopeSifterTool is the only treatment tokenbench can add. Its fields are
// private so task authors cannot add arguments, descriptions, environment
// variables, or other candidate-only instructions.
type ScopeSifterTool struct {
	executable string
	sha256     string
}

// NewScopeSifterTool verifies and pins the scopesifter MCP executable. The server
// argv is owned by ResolvePair and cannot be supplied by a suite.
func NewScopeSifterTool(executable string) (ScopeSifterTool, error) {
	if !filepath.IsAbs(executable) {
		return ScopeSifterTool{}, errors.New("scopesifter executable must be an absolute path")
	}
	if err := requireExecutableFile(executable, "scopesifter MCP"); err != nil {
		return ScopeSifterTool{}, err
	}
	digest, err := FileSHA256(executable)
	if err != nil {
		return ScopeSifterTool{}, err
	}
	if err := requireExecutableFile(executable, "scopesifter MCP"); err != nil {
		return ScopeSifterTool{}, err
	}
	return ScopeSifterTool{executable: filepath.Clean(executable), sha256: digest}, nil
}

// PreparedSuite is an unforgeable verified input state. Its fields are private;
// external callers can obtain it only through PrepareSuite.
type PreparedSuite struct {
	adapter                harness.Adapter
	environment            map[string]string
	identity               harness.Identity
	source                 source.Snapshot
	runnerExecutable       string
	runnerExecutableSHA256 string
	loaded                 LoadedSuite
}

// PrepareSuite verifies the pinned harness binary and clean self-contained
// source before pair construction. Execution must verify them again immediately
// before launching each arm.
func PrepareSuite(
	ctx context.Context,
	loaded LoadedSuite,
	adapter harness.Adapter,
) (PreparedSuite, error) {
	if err := loaded.suite.Validate(); err != nil {
		return PreparedSuite{}, err
	}
	if SHA256(loaded.prompt) != loaded.promptSHA256 {
		return PreparedSuite{}, errors.New("loaded prompt digest does not match prompt bytes")
	}
	if !ValidSHA256(loaded.sha256) {
		return PreparedSuite{}, errors.New("loaded suite digest is invalid")
	}
	if len(loaded.raw) == 0 || SHA256(loaded.raw) != loaded.sha256 {
		return PreparedSuite{}, errors.New("loaded suite digest does not match exact suite JSON bytes")
	}
	if err := requireExecutableFile(
		loaded.suite.HarnessExecutable,
		"harness",
	); err != nil {
		return PreparedSuite{}, err
	}
	harnessDigest, err := FileSHA256(loaded.suite.HarnessExecutable)
	if err != nil {
		return PreparedSuite{}, err
	}
	if harnessDigest != loaded.suite.HarnessSHA256 {
		return PreparedSuite{}, fmt.Errorf(
			"harness executable digest mismatch: got %s, want %s",
			harnessDigest,
			loaded.suite.HarnessSHA256,
		)
	}
	if err := requireExecutableFile(
		loaded.suite.HarnessExecutable,
		"harness",
	); err != nil {
		return PreparedSuite{}, err
	}
	sourceSnapshot, err := source.Verify(ctx, source.Expected{
		Root:                loaded.suite.SourceRoot,
		Revision:            loaded.suite.SourceRevision,
		Base:                loaded.suite.SourceBaseRevision,
		TreeSHA256:          loaded.suite.SourceTreeSHA256,
		GitExecutable:       loaded.suite.GitExecutable,
		GitExecutableSHA256: loaded.suite.GitExecutableSHA256,
	})
	if err != nil {
		return PreparedSuite{}, fmt.Errorf("verify source: %w", err)
	}
	runnerExecutable, runnerSHA256, err := currentRunnerIdentity()
	if err != nil {
		return PreparedSuite{}, err
	}
	if adapter == nil {
		return PreparedSuite{}, errors.New("harness adapter is required")
	}
	if adapter.Kind() != loaded.suite.HarnessKind {
		return PreparedSuite{}, fmt.Errorf(
			"adapter kind %q does not match suite harness kind %q",
			adapter.Kind(),
			loaded.suite.HarnessKind,
		)
	}
	request := harness.ResolveRequest{
		Environment:            make(map[string]string),
		Executable:             loaded.suite.HarnessExecutable,
		ExecutableSHA256:       loaded.suite.HarnessSHA256,
		Model:                  loaded.suite.Model,
		ExpectedModelRevision:  loaded.suite.ExpectedModelRevision,
		ReasoningEffort:        loaded.suite.ReasoningEffort,
		PermissionProfile:      loaded.suite.PermissionProfile,
		DeveloperInstructions:  loaded.suite.DeveloperInstructions,
		WorkingDirectory:       sourceSnapshot.Root,
		SourceRevision:         sourceSnapshot.Revision,
		SourceBaseRevision:     sourceSnapshot.Base,
		SourceTreeSHA256:       sourceSnapshot.TreeSHA256,
		GitExecutable:          sourceSnapshot.GitExecutable,
		GitExecutableSHA256:    sourceSnapshot.GitExecutableSHA256,
		GitMetadataSHA256:      sourceSnapshot.GitMetadataSHA256,
		RunnerExecutable:       runnerExecutable,
		RunnerExecutableSHA256: runnerSHA256,
		TimeoutMillis:          loaded.suite.TimeoutMillis,
	}
	environment, err := resolveCommonEnvironment(ctx, adapter, request)
	if err != nil {
		return PreparedSuite{}, fmt.Errorf("resolve common harness environment: %w", err)
	}
	request.Environment = environment
	identity, err := adapter.Resolve(ctx, request)
	if err != nil {
		return PreparedSuite{}, fmt.Errorf("resolve harness adapter: %w", err)
	}
	if err := validateResolvedIdentity(identity, loaded.suite); err != nil {
		return PreparedSuite{}, err
	}
	return PreparedSuite{
		identity:               identity,
		environment:            cloneMap(environment),
		loaded:                 loaded,
		adapter:                adapter,
		source:                 sourceSnapshot,
		runnerExecutable:       runnerExecutable,
		runnerExecutableSHA256: runnerSHA256,
	}, nil
}

// Difference is one authorized semantic difference between paired arms.
type Difference struct {
	JSONPointer     string `json:"json_pointer"`
	Authorization   string `json:"authorization"`
	BaselineSHA256  string `json:"baseline_sha256"`
	CandidateSHA256 string `json:"candidate_sha256"`
}

// ParityProof commits to the actual semantic invocations. A valid proof always
// contains exactly one authorized difference at /mcp_servers.
type ParityProof struct {
	SchemaVersion                 string       `json:"schema_version"`
	CommonInvocationSHA256        string       `json:"common_invocation_sha256"`
	BaselineInvocationSHA256      string       `json:"baseline_invocation_sha256"`
	CandidateInvocationSHA256     string       `json:"candidate_invocation_sha256"`
	PromptSHA256                  string       `json:"prompt_sha256"`
	ScopeSifterRegistrationSHA256 string       `json:"scopesifter_registration_sha256"`
	Differences                   []Difference `json:"differences"`
}

// ResolvedPlan is serializable audit evidence of the one authored suite and
// two runner-derived invocations. It is never an execution capability: a
// caller must reload and prepare the suite to obtain an adapter-bound Pair.
type ResolvedPlan struct {
	OriginInputs            *executionsnapshot.OriginInputs    `json:"origin_inputs"`
	ExecutionInputs         *executionsnapshot.ExecutionInputs `json:"execution_inputs"`
	ArtifactBundle          *ArtifactBundleAudit               `json:"artifact_bundle"`
	SuitePath               string                             `json:"suite_path"`
	PromptSHA256            string                             `json:"prompt_sha256"`
	ResolvedSuiteSHA256     string                             `json:"resolved_suite_sha256"`
	RenderedProcessesSHA256 string                             `json:"rendered_processes_sha256"`
	SchemaVersion           string                             `json:"schema_version"`
	SuiteID                 string                             `json:"suite_id"`
	SuiteSHA256             string                             `json:"suite_sha256"`
	Parity                  ParityProof                        `json:"parity"`
	SuiteJSON               []byte                             `json:"suite_json"`
	Baseline                harness.Invocation                 `json:"baseline"`
	Candidate               harness.Invocation                 `json:"candidate"`
	RenderedProcesses       ProcessPair                        `json:"rendered_processes"`
	ResolvedSuite           Suite                              `json:"resolved_suite"`
	Seed                    uint64                             `json:"seed"`
	Repetitions             int                                `json:"repetitions"`
	Publishable             bool                               `json:"publishable"`
}

// Pair holds unexported legs so normal callers cannot construct or replace one
// arm independently.
type Pair struct {
	adapter             harness.Adapter
	snapshotAuthority   *executionsnapshot.Authority
	executionInputs     executionsnapshot.ExecutionInputs
	artifactBundle      ArtifactBundleAudit
	originInputs        executionsnapshot.OriginInputs
	suiteID             string
	suiteSHA256         string
	suitePath           string
	resolvedSuiteSHA256 string
	promptSHA256        string
	parity              ParityProof
	suiteJSON           []byte
	candidate           harness.Invocation
	baseline            harness.Invocation
	resolvedSuite       Suite
	repetitions         int
	seed                uint64
	publishable         bool
}

// ResolvePair creates both arms from one loaded suite. It adds only the
// candidate scopesifter MCP registration and proves parity before returning.
func ResolvePair(prepared PreparedSuite, tool ScopeSifterTool) (Pair, error) {
	loaded := prepared.loaded
	if err := loaded.suite.Validate(); err != nil {
		return Pair{}, err
	}
	if SHA256(loaded.prompt) != loaded.promptSHA256 {
		return Pair{}, errors.New("loaded prompt digest does not match prompt bytes")
	}
	if !ValidSHA256(loaded.sha256) {
		return Pair{}, errors.New("loaded suite digest is invalid")
	}
	if len(loaded.raw) == 0 || SHA256(loaded.raw) != loaded.sha256 {
		return Pair{}, errors.New("loaded suite digest does not match exact suite JSON bytes")
	}
	if tool.executable == "" || !ValidSHA256(tool.sha256) {
		return Pair{}, errors.New("scopesifter tool was not constructed by NewScopeSifterTool")
	}
	if prepared.adapter == nil {
		return Pair{}, errors.New("prepared suite has no bound harness adapter")
	}

	if err := validateResolvedIdentity(prepared.identity, loaded.suite); err != nil {
		return Pair{}, err
	}
	common := invocationFromSuite(prepared)
	baseline := cloneInvocation(common)
	candidate := cloneInvocation(common)
	candidate.MCPServers = []harness.MCPServer{
		scopeSifterRegistration(tool, candidate),
	}
	proof, err := ProveParity(baseline, candidate)
	if err != nil {
		return Pair{}, err
	}
	resolvedSuiteSHA256, err := JSONSHA256(loaded.suite)
	if err != nil {
		return Pair{}, fmt.Errorf("digest resolved suite: %w", err)
	}
	return Pair{
		baseline:            baseline,
		candidate:           candidate,
		parity:              proof,
		suiteID:             loaded.suite.ID,
		suiteSHA256:         loaded.sha256,
		suiteJSON:           append([]byte(nil), loaded.raw...),
		suitePath:           loaded.path,
		resolvedSuite:       loaded.suite,
		resolvedSuiteSHA256: resolvedSuiteSHA256,
		promptSHA256:        loaded.promptSHA256,
		seed:                loaded.suite.Seed,
		repetitions:         loaded.suite.Repetitions,
		adapter:             prepared.adapter,
	}, nil
}

func requireExecutableFile(path, label string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("inspect %s executable: %w", label, err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return fmt.Errorf(
			"%s executable %s is not an executable regular file",
			label,
			path,
		)
	}
	if hasMultipleLinks(info) {
		return fmt.Errorf(
			"%s executable %s is hard-linked outside its pinned path",
			label,
			path,
		)
	}
	return nil
}

// Baseline returns a defensive copy.
func (pair Pair) Baseline() harness.Invocation {
	return cloneInvocation(pair.baseline)
}

// Candidate returns a defensive copy.
func (pair Pair) Candidate() harness.Invocation {
	return cloneInvocation(pair.candidate)
}

// Proof returns a defensive copy.
func (pair Pair) Proof() ParityProof {
	proof := pair.parity
	proof.Differences = append([]Difference(nil), pair.parity.Differences...)
	return proof
}

// Plan builds through the retained adapter capability and binds the rendered
// processes to a serializable audit plan. It does not accept caller-authored
// processes, and decoded plans remain audit-only.
func (pair Pair) Plan(ctx context.Context) (ResolvedPlan, error) {
	processes, err := pair.Build(ctx)
	if err != nil {
		return ResolvedPlan{}, err
	}
	return pair.plan(processes)
}

func (pair Pair) plan(processes ProcessPair) (ResolvedPlan, error) {
	baseline, candidate := pair.Baseline(), pair.Candidate()
	if err := validateProcessPair(baseline, candidate, processes); err != nil {
		return ResolvedPlan{}, err
	}
	processes = cloneProcessPair(processes)
	processDigest, err := JSONSHA256(processes)
	if err != nil {
		return ResolvedPlan{}, fmt.Errorf("digest rendered processes: %w", err)
	}
	plan := ResolvedPlan{
		Publishable:             pair.publishable,
		Baseline:                pair.Baseline(),
		Candidate:               pair.Candidate(),
		Parity:                  pair.Proof(),
		RenderedProcesses:       processes,
		RenderedProcessesSHA256: processDigest,
		SchemaVersion:           PlanSchemaVersion,
		SuiteID:                 pair.suiteID,
		SuiteSHA256:             pair.suiteSHA256,
		SuiteJSON:               append([]byte(nil), pair.suiteJSON...),
		SuitePath:               pair.suitePath,
		ResolvedSuite:           pair.resolvedSuite,
		ResolvedSuiteSHA256:     pair.resolvedSuiteSHA256,
		PromptSHA256:            pair.promptSHA256,
		Seed:                    pair.seed,
		Repetitions:             pair.repetitions,
	}
	if pair.publishable {
		origins := pair.originInputs
		execution := pair.executionInputs.Clone()
		artifacts := cloneArtifactBundleAudit(pair.artifactBundle)
		plan.OriginInputs = &origins
		plan.ExecutionInputs = &execution
		plan.ArtifactBundle = &artifacts
	}
	if err := plan.Validate(); err != nil {
		return ResolvedPlan{}, fmt.Errorf("validate resolved execution plan: %w", err)
	}
	return plan, nil
}

// Build repeats parity, source, executable, and resolved-identity checks, then
// renders both processes through the exact adapter capability retained by
// PrepareSuite. A decoded ResolvedPlan cannot call this method.
func (pair Pair) Build(ctx context.Context) (ProcessPair, error) {
	if pair.adapter == nil {
		return ProcessPair{}, errors.New("pair has no bound harness adapter")
	}
	baseline, candidate := pair.Baseline(), pair.Candidate()
	proof, err := ProveParity(baseline, candidate)
	if err != nil {
		return ProcessPair{}, err
	}
	if !equalJSON(proof, pair.parity) {
		return ProcessPair{}, errors.New("stored pair parity proof is invalid")
	}
	if pair.adapter.Kind() != baseline.HarnessIdentity.Kind {
		return ProcessPair{}, errors.New("bound adapter kind changed after preparation")
	}
	if err := pair.reverifyExecutionInputs(ctx, baseline, candidate); err != nil {
		return ProcessPair{}, err
	}
	request := resolveRequest(baseline)
	expectedEnvironment := cloneMap(request.Environment)
	request.Environment = make(map[string]string)
	resolvedEnvironment, err := resolveCommonEnvironment(ctx, pair.adapter, request)
	if err != nil {
		return ProcessPair{}, fmt.Errorf("re-resolve common harness environment: %w", err)
	}
	if !reflect.DeepEqual(resolvedEnvironment, expectedEnvironment) {
		return ProcessPair{}, errors.New("common harness environment changed after preparation")
	}
	if err := pair.reverifyExecutionInputs(ctx, baseline, candidate); err != nil {
		return ProcessPair{}, err
	}
	request.Environment = expectedEnvironment
	identity, err := pair.adapter.Resolve(ctx, request)
	if err != nil {
		return ProcessPair{}, fmt.Errorf("re-resolve harness adapter: %w", err)
	}
	if identity != baseline.HarnessIdentity {
		return ProcessPair{}, errors.New("resolved harness identity changed after preparation")
	}
	if err := pair.reverifyExecutionInputs(ctx, baseline, candidate); err != nil {
		return ProcessPair{}, err
	}
	processes, err := renderPair(ctx, pair.adapter, baseline, candidate)
	if err != nil {
		return ProcessPair{}, err
	}
	// Adapter control processes run before this check. Any source or executable
	// mutation they attempt is therefore observed before the processes escape.
	if err := pair.reverifyExecutionInputs(ctx, baseline, candidate); err != nil {
		return ProcessPair{}, err
	}
	return processes, nil
}

// Validate recomputes all plan commitments for audit or transport. Validation
// does not turn a decoded plan into execution authority.
func (plan ResolvedPlan) Validate() error {
	if err := validatePlanObjectSize(plan); err != nil {
		return err
	}
	switch {
	case plan.SchemaVersion != PlanSchemaVersion:
		return fmt.Errorf("unexpected plan schema %q", plan.SchemaVersion)
	case plan.SuiteID == "":
		return errors.New("plan suite_id is required")
	case !ValidSHA256(plan.SuiteSHA256):
		return errors.New("plan suite_sha256 is invalid")
	case len(plan.SuiteJSON) == 0:
		return errors.New("plan suite_json is required")
	case SHA256(plan.SuiteJSON) != plan.SuiteSHA256:
		return errors.New("plan suite digest does not match exact suite JSON bytes")
	case !filepath.IsAbs(plan.SuitePath) || filepath.Clean(plan.SuitePath) != plan.SuitePath:
		return errors.New("plan suite_path must be absolute and canonical")
	case !ValidSHA256(plan.ResolvedSuiteSHA256):
		return errors.New("plan resolved_suite_sha256 is invalid")
	case !ValidSHA256(plan.PromptSHA256):
		return errors.New("plan prompt_sha256 is invalid")
	case plan.Repetitions <= 0:
		return errors.New("plan repetitions must be positive")
	case !ValidSHA256(plan.RenderedProcessesSHA256):
		return errors.New("plan rendered_processes_sha256 is invalid")
	}
	if plan.Publishable {
		if plan.OriginInputs == nil || plan.ExecutionInputs == nil || plan.ArtifactBundle == nil {
			return errors.New("publishable v6 plan requires origin, execution, and artifact inputs")
		}
		if err := executionsnapshot.ValidateBinding(
			*plan.OriginInputs,
			*plan.ExecutionInputs,
		); err != nil {
			return fmt.Errorf("validate plan input binding: %w", err)
		}
		if err := plan.ArtifactBundle.validateBinding(*plan.OriginInputs); err != nil {
			return fmt.Errorf("validate plan artifact binding: %w", err)
		}
		policyDigest, err := artifactBuildPolicyDigest()
		if err != nil || policyDigest != plan.ArtifactBundle.ManifestSHA256 {
			return errors.Join(errors.New("plan artifacts differ from this tokenbench binary policy"), err)
		}
	} else if plan.OriginInputs != nil || plan.ExecutionInputs != nil || plan.ArtifactBundle != nil {
		return errors.New("nonpublishable v6 plan must not claim immutable input authority")
	}
	authoredSuite, err := decodeEmbeddedSuite(plan.SuiteJSON)
	if err != nil {
		return fmt.Errorf("decode plan suite_json: %w", err)
	}
	resolvedSuite := authoredSuite
	base := filepath.Dir(plan.SuitePath)
	resolvedSuite.PromptFile = resolveRelative(base, resolvedSuite.PromptFile)
	resolvedSuite.SourceRoot = resolveRelative(base, resolvedSuite.SourceRoot)
	resolvedSuiteDigest, err := JSONSHA256(resolvedSuite)
	if err != nil {
		return err
	}
	if resolvedSuiteDigest != plan.ResolvedSuiteSHA256 ||
		!reflect.DeepEqual(resolvedSuite, plan.ResolvedSuite) {
		return errors.New("plan resolved suite does not match its exact authored suite and origin")
	}
	if resolvedSuite.ID != plan.SuiteID || resolvedSuite.Seed != plan.Seed ||
		resolvedSuite.Repetitions != plan.Repetitions {
		return errors.New("plan schedule does not match exact embedded suite JSON")
	}
	if err := validatePlanSuiteBinding(resolvedSuite, plan); err != nil {
		return err
	}
	if err := ValidateTreatmentNeutrality(
		plan.Baseline.DeveloperInstructions,
		plan.Baseline.Prompt,
	); err != nil {
		return fmt.Errorf("plan treatment neutrality: %w", err)
	}
	proof, err := ProveParity(plan.Baseline, plan.Candidate)
	if err != nil {
		return err
	}
	if !equalJSON(proof, plan.Parity) {
		return errors.New("stored parity proof does not match resolved invocations")
	}
	if proof.PromptSHA256 != plan.PromptSHA256 {
		return errors.New("plan prompt digest does not match actual invocation prompt")
	}
	if err := validateProcessPair(plan.Baseline, plan.Candidate, plan.RenderedProcesses); err != nil {
		return err
	}
	processDigest, err := JSONSHA256(plan.RenderedProcesses)
	if err != nil {
		return err
	}
	if processDigest != plan.RenderedProcessesSHA256 {
		return errors.New("rendered process digest does not match stored processes")
	}
	return nil
}

func decodeEmbeddedSuite(raw []byte) (Suite, error) {
	if !utf8.Valid(raw) {
		return Suite{}, errors.New("suite JSON must be valid UTF-8")
	}
	if err := rejectDuplicateObjectKeys(raw); err != nil {
		return Suite{}, err
	}
	var suite Suite
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&suite); err != nil {
		return Suite{}, err
	}
	if err := requireJSONEOF(decoder); err != nil {
		return Suite{}, err
	}
	if err := requireSuiteFields(raw); err != nil {
		return Suite{}, err
	}
	if err := suite.Validate(); err != nil {
		return Suite{}, err
	}
	return suite, nil
}

func validatePlanSuiteBinding(suite Suite, plan ResolvedPlan) error {
	invocation := plan.Baseline
	harnessExecutable := suite.HarnessExecutable
	harnessSHA256 := suite.HarnessSHA256
	gitExecutable := suite.GitExecutable
	gitSHA256 := suite.GitExecutableSHA256
	sourceRoot := suite.SourceRoot
	if plan.Publishable {
		origins := plan.OriginInputs
		execution := plan.ExecutionInputs
		if origins == nil || execution == nil {
			return errors.New("publishable plan input commitments are missing")
		}
		switch {
		case suite.HarnessExecutable != origins.Codex.Path ||
			suite.HarnessSHA256 != origins.Codex.SHA256:
			return errors.New("embedded suite harness does not match its origin input")
		case suite.GitExecutable != origins.Git.Path ||
			suite.GitExecutableSHA256 != origins.Git.SHA256:
			return errors.New("embedded suite Git does not match its origin input")
		case suite.SourceRoot != origins.Source.Root:
			return errors.New("embedded suite source does not match its origin input")
		case plan.ArtifactBundle == nil ||
			suite.ArtifactManifestSHA256 != plan.ArtifactBundle.ManifestSHA256:
			return errors.New("embedded suite artifact manifest does not match the plan bundle")
		case invocation.Executable != execution.CodexExecutable:
			return errors.New("plan harness does not use the immutable execution path")
		case invocation.GitExecutable != execution.VerifierGitExecutable:
			return errors.New("plan verifier Git does not use the provenance execution path")
		case invocation.RunnerExecutable != execution.RunnerExecutable:
			return errors.New("plan runner does not use the immutable execution path")
		case invocation.WorkingDirectory != execution.SourceRoot:
			return errors.New("plan source does not use the immutable execution path")
		case invocation.ExecutableSHA256 != origins.Codex.SHA256 ||
			invocation.GitExecutableSHA256 != origins.Git.SHA256 ||
			invocation.RunnerExecutableSHA256 != origins.Runner.SHA256:
			return errors.New("plan execution digests differ from their origin inputs")
		case invocation.SourceRevision != execution.SourceRevision ||
			invocation.SourceBaseRevision != execution.SourceBaseRevision ||
			invocation.SourceTreeSHA256 != execution.SourceTreeSHA256 ||
			invocation.GitMetadataSHA256 != execution.GitMetadataSHA256:
			return errors.New("plan source identity differs from its execution input")
		case invocation.HarnessIdentity.AdapterExecutableSHA256 != origins.Runner.SHA256:
			return errors.New("plan adapter control identity differs from runner/arm-init")
		case len(plan.Candidate.MCPServers) != 1 ||
			!reflect.DeepEqual(
				plan.Candidate.MCPServers[0],
				scopeSifterCacheRegistration(*execution, origins.ScopeSifter),
			):
			return errors.New("publishable plan lacks the exact cache-only scopesifter registration")
		}
		harnessExecutable = execution.CodexExecutable
		gitExecutable = execution.VerifierGitExecutable
		sourceRoot = execution.SourceRoot
	}
	switch {
	case suite.HarnessKind != invocation.HarnessIdentity.Kind:
		return errors.New("embedded suite harness kind does not match plan")
	case harnessExecutable != invocation.Executable:
		return errors.New("embedded suite harness executable does not match plan")
	case harnessSHA256 != invocation.ExecutableSHA256:
		return errors.New("embedded suite harness digest does not match plan")
	case gitExecutable != invocation.GitExecutable:
		return errors.New("embedded suite Git executable does not match plan")
	case gitSHA256 != invocation.GitExecutableSHA256:
		return errors.New("embedded suite Git digest does not match plan")
	case suite.Model != invocation.RequestedModel:
		return errors.New("embedded suite model does not match plan")
	case suite.ExpectedModelRevision != invocation.ModelRevision:
		return errors.New("embedded suite model revision does not match plan")
	case suite.ReasoningEffort != invocation.ReasoningEffort:
		return errors.New("embedded suite reasoning effort does not match plan")
	case suite.PermissionProfile != invocation.PermissionProfile:
		return errors.New("embedded suite permission profile does not match plan")
	case suite.DeveloperInstructions != invocation.DeveloperInstructions:
		return errors.New("embedded suite developer instructions do not match plan")
	case sourceRoot != invocation.WorkingDirectory:
		return errors.New("embedded suite source root does not match plan")
	case suite.SourceRevision != invocation.SourceRevision:
		return errors.New("embedded suite source revision does not match plan")
	case suite.SourceBaseRevision != invocation.SourceBaseRevision:
		return errors.New("embedded suite source base does not match plan")
	case suite.SourceTreeSHA256 != invocation.SourceTreeSHA256:
		return errors.New("embedded suite source tree does not match plan")
	case suite.TimeoutMillis != invocation.TimeoutMillis:
		return errors.New("embedded suite timeout does not match plan")
	default:
		return nil
	}
}

// DecodePlan strictly decodes and validates one plan document.
func DecodePlan(raw []byte) (ResolvedPlan, error) {
	if err := validatePlanByteLength(len(raw)); err != nil {
		return ResolvedPlan{}, fmt.Errorf("decode plan: %w", err)
	}
	if !utf8.Valid(raw) {
		return ResolvedPlan{}, errors.New("decode plan: JSON must be valid UTF-8")
	}
	if err := rejectDuplicateObjectKeys(raw); err != nil {
		return ResolvedPlan{}, fmt.Errorf("decode plan: %w", err)
	}
	var plan ResolvedPlan
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&plan); err != nil {
		return ResolvedPlan{}, fmt.Errorf("decode plan: %w", err)
	}
	if err := requireJSONEOF(decoder); err != nil {
		return ResolvedPlan{}, fmt.Errorf("decode plan: %w", err)
	}
	if err := plan.Validate(); err != nil {
		return ResolvedPlan{}, err
	}
	return plan, nil
}

func validatePlanObjectSize(plan ResolvedPlan) error {
	raw, err := json.Marshal(plan)
	if err != nil {
		return fmt.Errorf("encode plan for size validation: %w", err)
	}
	if len(raw) > MaximumPlanObjectBytes {
		return validatePlanByteLength(len(raw))
	}
	return nil
}

func validatePlanByteLength(size int) error {
	if size < 0 || size > MaximumPlanObjectBytes {
		return fmt.Errorf("object exceeds %d bytes", MaximumPlanObjectBytes)
	}
	return nil
}

func invocationFromSuite(prepared PreparedSuite) harness.Invocation {
	loaded := prepared.loaded
	suite := loaded.suite
	return harness.Invocation{
		Environment:            cloneMap(prepared.environment),
		Arguments:              make([]string, 0),
		MCPServers:             make([]harness.MCPServer, 0),
		Prompt:                 append([]byte(nil), loaded.prompt...),
		HarnessIdentity:        prepared.identity,
		Executable:             suite.HarnessExecutable,
		ExecutableSHA256:       suite.HarnessSHA256,
		Model:                  prepared.identity.Model,
		RequestedModel:         suite.Model,
		ModelRevision:          prepared.identity.ModelRevision,
		ReasoningEffort:        suite.ReasoningEffort,
		PermissionProfile:      suite.PermissionProfile,
		DeveloperInstructions:  suite.DeveloperInstructions,
		WorkingDirectory:       suite.SourceRoot,
		SourceRevision:         suite.SourceRevision,
		SourceBaseRevision:     suite.SourceBaseRevision,
		SourceTreeSHA256:       suite.SourceTreeSHA256,
		GitExecutable:          prepared.source.GitExecutable,
		GitExecutableSHA256:    prepared.source.GitExecutableSHA256,
		GitMetadataSHA256:      prepared.source.GitMetadataSHA256,
		RunnerExecutable:       prepared.runnerExecutable,
		RunnerExecutableSHA256: prepared.runnerExecutableSHA256,
		TimeoutMillis:          suite.TimeoutMillis,
	}
}

func resolveCommonEnvironment(
	ctx context.Context,
	adapter harness.Adapter,
	request harness.ResolveRequest,
) (map[string]string, error) {
	request.Environment = make(map[string]string)
	provider, ok := adapter.(harness.CommonEnvironmentAdapter)
	if !ok {
		return make(map[string]string), nil
	}
	environment, err := provider.CommonEnvironment(ctx, request)
	if err != nil {
		return nil, err
	}
	if err := harness.ValidatePublishableEnvironment(environment); err != nil {
		return nil, err
	}
	return cloneMap(environment), nil
}

func resolveRequest(invocation harness.Invocation) harness.ResolveRequest {
	return harness.ResolveRequest{
		Environment:            cloneMap(invocation.Environment),
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
	}
}

func reverifyExecutionInputs(
	ctx context.Context,
	baseline, candidate harness.Invocation,
) error {
	if err := requireExecutableFile(baseline.Executable, "harness"); err != nil {
		return err
	}
	digest, err := FileSHA256(baseline.Executable)
	if err != nil {
		return err
	}
	if digest != baseline.ExecutableSHA256 {
		return errors.New("harness executable changed after pair resolution")
	}
	tool := candidate.MCPServers[0]
	if err := requireExecutableFile(tool.Command, "scopesifter MCP"); err != nil {
		return err
	}
	digest, err = FileSHA256(tool.Command)
	if err != nil {
		return err
	}
	if digest != tool.ExecutableSHA256 {
		return errors.New("scopesifter MCP executable changed after pair resolution")
	}
	snapshot, err := source.Verify(ctx, source.Expected{
		Root:                baseline.WorkingDirectory,
		Revision:            baseline.SourceRevision,
		Base:                baseline.SourceBaseRevision,
		TreeSHA256:          baseline.SourceTreeSHA256,
		GitExecutable:       baseline.GitExecutable,
		GitExecutableSHA256: baseline.GitExecutableSHA256,
	})
	if err != nil {
		return fmt.Errorf("reverify source: %w", err)
	}
	if snapshot.GitExecutable != baseline.GitExecutable ||
		snapshot.GitExecutableSHA256 != baseline.GitExecutableSHA256 ||
		snapshot.GitMetadataSHA256 != baseline.GitMetadataSHA256 {
		return errors.New("git verifier identity changed after pair resolution")
	}
	runnerIdentity, err := selfexec.Current()
	if err != nil {
		return fmt.Errorf("reverify running tokenbench executable: %w", err)
	}
	if runnerIdentity.Path != baseline.RunnerExecutable ||
		runnerIdentity.SHA256 != baseline.RunnerExecutableSHA256 {
		return errors.New("tokenbench runner executable changed after pair resolution")
	}
	return nil
}

func (pair Pair) reverifyExecutionInputs(
	ctx context.Context,
	baseline, candidate harness.Invocation,
) error {
	if !pair.publishable {
		return reverifyExecutionInputs(ctx, baseline, candidate)
	}
	if pair.snapshotAuthority == nil {
		return errors.New("publishable pair has no live execution snapshot authority")
	}
	inputs, err := pair.snapshotAuthority.Inputs()
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(inputs, pair.executionInputs) {
		return errors.New("live snapshot authority differs from the bound execution inputs")
	}
	return pair.snapshotAuthority.RequireConformant(ctx)
}

// Publishable reports whether this Pair retains live immutable snapshot
// authority. Audit-only PrepareSuite/ResolvePair pairs deliberately return false.
func (pair Pair) Publishable() bool {
	return pair.publishable && pair.snapshotAuthority != nil
}

// ExecutionInputs returns the policy commitment bound to a publishable Pair.
// It is audit data, not authority; live use must still pass ReverifySnapshot.
func (pair Pair) ExecutionInputs() (executionsnapshot.ExecutionInputs, error) {
	if !pair.Publishable() {
		return executionsnapshot.ExecutionInputs{}, errors.New("pair is not publishable")
	}
	return pair.executionInputs.Clone(), nil
}

// ReverifySnapshot proves that the live authority still matches the Pair.
func (pair Pair) ReverifySnapshot(ctx context.Context) error {
	return pair.reverifyExecutionInputs(ctx, pair.baseline, pair.candidate)
}

// CloseExecutionSnapshot releases the read-only self-bind after all arm
// processes and pinned runner lifecycle resources have been closed.
func (pair Pair) CloseExecutionSnapshot() error {
	if pair.snapshotAuthority == nil {
		return nil
	}
	return pair.snapshotAuthority.Close()
}

// ExecutionSnapshotClosed reports whether the live mount and all inode pins
// were released successfully. Publication readiness combines this with the
// executor/lifecycle boundary; either half remaining open prevents signing.
func (pair Pair) ExecutionSnapshotClosed() bool {
	return pair.snapshotAuthority != nil && pair.snapshotAuthority.Closed()
}

func currentRunnerIdentity() (string, string, error) {
	identity, err := selfexec.Current()
	if err != nil {
		return "", "", fmt.Errorf("pin running tokenbench executable: %w", err)
	}
	return identity.Path, identity.SHA256, nil
}

func validateResolvedIdentity(identity harness.Identity, suite Suite) error {
	if err := harness.ValidateIdentity(identity); err != nil {
		return fmt.Errorf("resolved harness identity: %w", err)
	}
	switch {
	case identity.Kind != suite.HarnessKind:
		return fmt.Errorf(
			"resolved harness kind %q does not match requested %q",
			identity.Kind,
			suite.HarnessKind,
		)
	case identity.ExecutableSHA256 != suite.HarnessSHA256:
		return errors.New("resolved harness executable digest does not match suite")
	case identity.Model != suite.Model:
		return fmt.Errorf(
			"resolved model %q does not match requested %q",
			identity.Model,
			suite.Model,
		)
	case identity.ModelRevision != suite.ExpectedModelRevision:
		return fmt.Errorf(
			"resolved model revision %q does not match expected %q",
			identity.ModelRevision,
			suite.ExpectedModelRevision,
		)
	case identity.ReasoningEffort != suite.ReasoningEffort:
		return fmt.Errorf(
			"resolved reasoning effort %q does not match requested %q",
			identity.ReasoningEffort,
			suite.ReasoningEffort,
		)
	default:
		return nil
	}
}

func scopeSifterRegistration(
	tool ScopeSifterTool,
	invocation harness.Invocation,
) harness.MCPServer {
	return harness.MCPServer{
		Environment: make(map[string]string),
		Arguments: []string{
			"mcp",
			"--root", invocation.WorkingDirectory,
			"--base", invocation.SourceBaseRevision,
			"--git", invocation.GitExecutable,
			"--git-sha256", invocation.GitExecutableSHA256,
		},
		Name:             "scopesifter",
		Command:          tool.executable,
		ExecutableSHA256: tool.sha256,
		Required:         true,
		ReadOnly:         true,
	}
}

func cloneInvocation(source harness.Invocation) harness.Invocation {
	clone := source
	clone.Environment = cloneMap(source.Environment)
	clone.Arguments = append(make([]string, 0, len(source.Arguments)), source.Arguments...)
	clone.Prompt = append([]byte(nil), source.Prompt...)
	clone.MCPServers = make([]harness.MCPServer, len(source.MCPServers))
	for index, server := range source.MCPServers {
		clone.MCPServers[index] = server
		clone.MCPServers[index].Environment = cloneMap(server.Environment)
		clone.MCPServers[index].Arguments = append([]string(nil), server.Arguments...)
	}
	return clone
}

func cloneMap(source map[string]string) map[string]string {
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func equalJSON(left, right any) bool {
	leftJSON, leftErr := json.Marshal(left)
	rightJSON, rightErr := json.Marshal(right)
	return leftErr == nil && rightErr == nil && bytes.Equal(leftJSON, rightJSON)
}
