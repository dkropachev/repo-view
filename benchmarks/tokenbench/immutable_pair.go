package tokenbench

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/dkropachev/repo-view/benchmarks/tokenbench/harness"
	harnesscodex "github.com/dkropachev/repo-view/benchmarks/tokenbench/harness/codex"
	executionsnapshot "github.com/dkropachev/repo-view/benchmarks/tokenbench/snapshot"
	"github.com/dkropachev/repo-view/benchmarks/tokenbench/source"
)

// PreparedOrigins is the adapter-free first phase. Its private fields make it
// impossible to claim verified origin inputs by decoding audit JSON.
type PreparedOrigins struct {
	artifactBundle  preparedArtifactBundle
	origins         executionsnapshot.OriginInputs
	artifactOrigins artifactOriginSet
	loaded          LoadedSuite
}

// PrepareOrigins verifies all mutable origins before any adapter control
// process runs. Every executable role is hashed across two stable-file checks.
func PrepareOrigins(
	ctx context.Context,
	loaded LoadedSuite,
	bundle preparedArtifactBundle,
) (PreparedOrigins, error) {
	if ctx == nil {
		return PreparedOrigins{}, errors.New("origin preparation context is required")
	}
	if err := validateLoadedForPreparation(loaded); err != nil {
		return PreparedOrigins{}, err
	}
	artifacts, err := bundle.reverify(loaded)
	if err != nil {
		return PreparedOrigins{}, err
	}
	verified, err := source.Verify(ctx, source.Expected{
		Root:                loaded.suite.SourceRoot,
		Revision:            loaded.suite.SourceRevision,
		Base:                loaded.suite.SourceBaseRevision,
		TreeSHA256:          loaded.suite.SourceTreeSHA256,
		GitExecutable:       loaded.suite.GitExecutable,
		GitExecutableSHA256: loaded.suite.GitExecutableSHA256,
	})
	if err != nil {
		return PreparedOrigins{}, fmt.Errorf("verify source origin: %w", err)
	}
	runnerPath, runnerDigest, err := currentRunnerIdentity()
	if err != nil {
		return PreparedOrigins{}, err
	}
	runner, err := identifyExecutableOrigin(runnerPath, runnerDigest, "runner/arm-init")
	if err != nil {
		return PreparedOrigins{}, err
	}
	origins, err := executionsnapshot.NewOriginInputs(
		executionsnapshot.SourceOrigin{
			Root: verified.Root, Revision: verified.Revision, Base: verified.Base,
			TreeSHA256:        verified.TreeSHA256,
			GitMetadataSHA256: verified.GitMetadataSHA256,
		},
		artifacts.Codex,
		artifacts.RepoView,
		artifacts.Git,
		artifacts.Bash,
		artifacts.Utilities,
		runner,
	)
	if err != nil {
		return PreparedOrigins{}, fmt.Errorf("commit origin inputs: %w", err)
	}
	return PreparedOrigins{
		loaded: loaded, origins: origins,
		artifactBundle: bundle, artifactOrigins: artifacts,
	}, nil
}

func validateLoadedForPreparation(loaded LoadedSuite) error {
	if err := loaded.suite.Validate(); err != nil {
		return err
	}
	switch {
	case SHA256(loaded.prompt) != loaded.promptSHA256:
		return errors.New("loaded prompt digest does not match prompt bytes")
	case !ValidSHA256(loaded.sha256):
		return errors.New("loaded suite digest is invalid")
	case len(loaded.raw) == 0 || SHA256(loaded.raw) != loaded.sha256:
		return errors.New("loaded suite digest does not match exact suite JSON bytes")
	}
	return ValidateTreatmentNeutrality(loaded.suite.DeveloperInstructions, loaded.prompt)
}

func identifyExecutableOrigin(path, expectedDigest, label string) (executionsnapshot.FileOrigin, error) {
	if err := requireExecutableFile(path, label); err != nil {
		return executionsnapshot.FileOrigin{}, err
	}
	digestValue, err := FileSHA256(path)
	if err != nil {
		return executionsnapshot.FileOrigin{}, err
	}
	if expectedDigest != "" && digestValue != expectedDigest {
		return executionsnapshot.FileOrigin{}, fmt.Errorf("%s executable digest mismatch", label)
	}
	if err := requireExecutableFile(path, label); err != nil {
		return executionsnapshot.FileOrigin{}, err
	}
	second, err := FileSHA256(path)
	if err != nil || second != digestValue {
		return executionsnapshot.FileOrigin{}, errors.Join(
			fmt.Errorf("%s executable changed during origin preparation", label), err,
		)
	}
	return executionsnapshot.FileOrigin{Path: path, SHA256: digestValue}, nil
}

// PreparedExecution owns a live immutable self-bind until BindAdapter
// transfers it to a Pair or Close releases it.
type PreparedExecution struct {
	authority *executionsnapshot.Authority
	inputs    executionsnapshot.ExecutionInputs
	artifacts ArtifactBundleAudit
	origins   executionsnapshot.OriginInputs
	loaded    LoadedSuite
}

// Inputs returns a defensive audit commitment while retaining ownership of
// the live snapshot authority. The authority is checked both before and after
// reading so CLI construction cannot proceed from stale or decoded data.
func (prepared *PreparedExecution) Inputs(
	ctx context.Context,
) (executionsnapshot.ExecutionInputs, error) {
	if ctx == nil {
		return executionsnapshot.ExecutionInputs{}, errors.New("prepared execution context is required")
	}
	if prepared == nil || prepared.authority == nil {
		return executionsnapshot.ExecutionInputs{}, errors.New("live prepared execution is required")
	}
	if err := prepared.authority.RequireConformant(ctx); err != nil {
		return executionsnapshot.ExecutionInputs{}, err
	}
	live, err := prepared.authority.Inputs()
	if err != nil {
		return executionsnapshot.ExecutionInputs{}, err
	}
	result, err := cloneExactPreparedInputs(prepared.inputs, live)
	if err != nil {
		return executionsnapshot.ExecutionInputs{}, err
	}
	if err := prepared.authority.RequireConformant(ctx); err != nil {
		return executionsnapshot.ExecutionInputs{}, err
	}
	return result, nil
}

func cloneExactPreparedInputs(
	expected, live executionsnapshot.ExecutionInputs,
) (executionsnapshot.ExecutionInputs, error) {
	if !reflect.DeepEqual(live, expected) {
		return executionsnapshot.ExecutionInputs{}, errors.New(
			"live snapshot authority differs from prepared execution inputs",
		)
	}
	return live.Clone(), nil
}

func BuildExecutionSnapshot(
	ctx context.Context,
	prepared PreparedOrigins,
	root string,
) (PreparedExecution, error) {
	if err := prepared.origins.Validate(); err != nil {
		return PreparedExecution{}, errors.New("origins were not produced by PrepareOrigins")
	}
	artifacts, err := prepared.artifactBundle.reverify(prepared.loaded)
	if err != nil || !reflect.DeepEqual(artifacts, prepared.artifactOrigins) {
		return PreparedExecution{}, errors.Join(
			errors.New("execution artifact bundle changed after origin preparation"), err,
		)
	}
	authority, err := executionsnapshot.Build(ctx, executionsnapshot.BuildRequest{
		Root: root, Origins: prepared.origins,
	})
	if err != nil {
		return PreparedExecution{}, err
	}
	inputs, err := authority.Inputs()
	if err != nil {
		return PreparedExecution{}, errors.Join(err, authority.Close())
	}
	return PreparedExecution{
		loaded: prepared.loaded, origins: prepared.origins,
		inputs: inputs, authority: authority,
		artifacts: cloneArtifactBundleAudit(prepared.artifactBundle.state.audit),
	}, nil
}

func (prepared *PreparedExecution) Close() error {
	if prepared == nil || prepared.authority == nil {
		return nil
	}
	err := prepared.authority.Close()
	if err == nil {
		prepared.authority = nil
	}
	return err
}

// BindAdapter is the only publishable Pair constructor. It accepts exactly
// the built-in production Codex adapter and transfers snapshot ownership on
// success.
func BindAdapter(
	ctx context.Context,
	prepared *PreparedExecution,
	adapter harness.Adapter,
) (Pair, error) {
	if prepared == nil || prepared.authority == nil {
		return Pair{}, errors.New("live prepared execution is required")
	}
	if _, ok := harnesscodex.BuiltInProductionIdentity(adapter); !ok {
		return Pair{}, errors.New("publishable pair requires the built-in production Codex adapter")
	}
	if prepared.loaded.suite.HarnessKind != "codex" {
		return Pair{}, errors.New("publishable pair requires a Codex suite")
	}
	if err := prepared.authority.RequireConformant(ctx); err != nil {
		return Pair{}, err
	}
	liveInputs, err := prepared.authority.Inputs()
	if err != nil || !reflect.DeepEqual(liveInputs, prepared.inputs) {
		return Pair{}, errors.Join(errors.New("prepared execution authority changed"), err)
	}
	request := resolveRequestFromExecution(prepared.loaded.suite, prepared.inputs, prepared.origins)
	environment, err := resolveCommonEnvironment(ctx, adapter, request)
	if err != nil {
		return Pair{}, fmt.Errorf("resolve common harness environment: %w", err)
	}
	if err := prepared.authority.RequireConformant(ctx); err != nil {
		return Pair{}, err
	}
	request.Environment = environment
	identity, err := adapter.Resolve(ctx, request)
	if err != nil {
		return Pair{}, fmt.Errorf("resolve harness adapter: %w", err)
	}
	if err := validateResolvedIdentity(identity, prepared.loaded.suite); err != nil {
		return Pair{}, err
	}
	if identity.AdapterExecutableSHA256 != prepared.origins.Runner.SHA256 {
		return Pair{}, errors.New("adapter control executable differs from runner/arm-init origin")
	}
	common := invocationFromExecution(
		prepared.loaded, prepared.inputs, prepared.origins, identity, environment,
	)
	baseline := cloneInvocation(common)
	candidate := cloneInvocation(common)
	candidate.MCPServers = []harness.MCPServer{
		repoViewCacheRegistration(prepared.inputs, prepared.origins.RepoView),
	}
	proof, err := ProveParity(baseline, candidate)
	if err != nil {
		return Pair{}, err
	}
	resolvedSuiteSHA256, err := JSONSHA256(prepared.loaded.suite)
	if err != nil {
		return Pair{}, err
	}
	pair := Pair{
		adapter: adapter, suiteID: prepared.loaded.suite.ID,
		suiteSHA256: prepared.loaded.sha256,
		suiteJSON:   append([]byte(nil), prepared.loaded.raw...),
		suitePath:   prepared.loaded.path, resolvedSuite: prepared.loaded.suite,
		resolvedSuiteSHA256: resolvedSuiteSHA256,
		promptSHA256:        prepared.loaded.promptSHA256,
		seed:                prepared.loaded.suite.Seed, repetitions: prepared.loaded.suite.Repetitions,
		parity: proof, baseline: baseline, candidate: candidate,
		publishable: true, originInputs: prepared.origins,
		executionInputs: prepared.inputs.Clone(), snapshotAuthority: prepared.authority,
		artifactBundle: cloneArtifactBundleAudit(prepared.artifacts),
	}
	if err := pair.ReverifySnapshot(ctx); err != nil {
		return Pair{}, err
	}
	prepared.authority = nil
	return pair, nil
}

func resolveRequestFromExecution(
	suite Suite,
	inputs executionsnapshot.ExecutionInputs,
	origins executionsnapshot.OriginInputs,
) harness.ResolveRequest {
	return harness.ResolveRequest{
		Environment: make(map[string]string), Executable: inputs.CodexExecutable,
		ExecutableSHA256: origins.Codex.SHA256, Model: suite.Model,
		ExpectedModelRevision: suite.ExpectedModelRevision,
		ReasoningEffort:       suite.ReasoningEffort, PermissionProfile: suite.PermissionProfile,
		DeveloperInstructions: suite.DeveloperInstructions, WorkingDirectory: inputs.SourceRoot,
		SourceRevision: inputs.SourceRevision, SourceBaseRevision: inputs.SourceBaseRevision,
		SourceTreeSHA256: inputs.SourceTreeSHA256,
		GitExecutable:    inputs.VerifierGitExecutable, GitExecutableSHA256: origins.Git.SHA256,
		GitMetadataSHA256: inputs.GitMetadataSHA256,
		RunnerExecutable:  inputs.RunnerExecutable, RunnerExecutableSHA256: origins.Runner.SHA256,
		TimeoutMillis: suite.TimeoutMillis,
	}
}

func invocationFromExecution(
	loaded LoadedSuite,
	inputs executionsnapshot.ExecutionInputs,
	origins executionsnapshot.OriginInputs,
	identity harness.Identity,
	environment map[string]string,
) harness.Invocation {
	request := resolveRequestFromExecution(loaded.suite, inputs, origins)
	return harness.Invocation{
		Environment: cloneMap(environment), HarnessIdentity: identity,
		DeveloperInstructions: request.DeveloperInstructions,
		PermissionProfile:     request.PermissionProfile,
		SourceTreeSHA256:      request.SourceTreeSHA256,
		GitExecutable:         request.GitExecutable, GitExecutableSHA256: request.GitExecutableSHA256,
		GitMetadataSHA256: request.GitMetadataSHA256,
		RunnerExecutable:  request.RunnerExecutable, RunnerExecutableSHA256: request.RunnerExecutableSHA256,
		Executable: request.Executable, ExecutableSHA256: request.ExecutableSHA256,
		Model: identity.Model, RequestedModel: request.Model,
		ModelRevision: identity.ModelRevision, ReasoningEffort: request.ReasoningEffort,
		SourceBaseRevision: request.SourceBaseRevision, SourceRevision: request.SourceRevision,
		WorkingDirectory: request.WorkingDirectory, Arguments: []string{}, MCPServers: []harness.MCPServer{},
		Prompt: append([]byte(nil), loaded.prompt...), TimeoutMillis: request.TimeoutMillis,
	}
}

func repoViewCacheRegistration(
	inputs executionsnapshot.ExecutionInputs,
	origin executionsnapshot.FileOrigin,
) harness.MCPServer {
	return harness.MCPServer{
		Environment: map[string]string{}, Name: "repo_view",
		Command: inputs.RepoViewExecutable, ExecutableSHA256: origin.SHA256,
		Arguments: []string{
			"mcp", "--root", inputs.SourceRoot,
			"--base", inputs.SourceBaseRevision,
			"--head", inputs.SourceRevision,
			"--changed-state-cache", inputs.ChangedState.Path,
			"--changed-state-cache-sha256", inputs.ChangedState.SHA256,
		},
		Required: true, ReadOnly: true,
	}
}
