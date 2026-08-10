package tokenbench

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/dkropachev/repo-view/benchmarks/tokenbench/harness"
	"github.com/dkropachev/repo-view/benchmarks/tokenbench/harness/fake"
	"github.com/dkropachev/repo-view/benchmarks/tokenbench/internal/selfexec"
	executionsnapshot "github.com/dkropachev/repo-view/benchmarks/tokenbench/snapshot"
	"github.com/dkropachev/repo-view/benchmarks/tokenbench/source"
)

func TestCurrentRunnerIdentityUsesPinnedRunningImage(t *testing.T) {
	want, err := selfexec.Current()
	if err != nil {
		t.Fatal(err)
	}
	path, digest, err := currentRunnerIdentity()
	if err != nil {
		t.Fatal(err)
	}
	if path != want.Path || digest != want.SHA256 {
		t.Fatalf("currentRunnerIdentity() = (%q, %q), want (%q, %q)", path, digest, want.Path, want.SHA256)
	}
}

func TestPreparedExecutionInputsRequiresLiveAuthority(t *testing.T) {
	if _, err := (*PreparedExecution)(nil).Inputs(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "live prepared execution") {
		t.Fatalf("nil PreparedExecution.Inputs() = %v", err)
	}
	prepared := &PreparedExecution{}
	//nolint:staticcheck // This assertion deliberately exercises the nil-context rejection boundary.
	if _, err := prepared.Inputs(nil); err == nil || !strings.Contains(err.Error(), "context") {
		t.Fatalf("PreparedExecution.Inputs(nil) = %v", err)
	}
}

func TestCloneExactPreparedInputsRejectsDriftAndDefensivelyClones(t *testing.T) {
	expected := executionsnapshot.ExecutionInputs{
		ReadOnlyPaths:   []string{"/snapshot/source"},
		ExecutablePaths: []string{"/snapshot/tool"},
		ChangedStateCache: executionsnapshot.ChangedStateCache{
			ChangedFiles: []executionsnapshot.ChangedFileState{},
		},
		Manifest: []executionsnapshot.ManifestEntry{{
			SnapshotPath: "/snapshot/tool",
			ELF:          &executionsnapshot.ELFIdentity{Needed: []string{}},
		}},
	}
	live := expected.Clone()
	result, err := cloneExactPreparedInputs(expected, live)
	if err != nil {
		t.Fatal(err)
	}
	result.ReadOnlyPaths[0] = "/changed"
	result.Manifest[0].ELF.Needed = append(result.Manifest[0].ELF.Needed, "changed")
	if expected.ReadOnlyPaths[0] == "/changed" || len(expected.Manifest[0].ELF.Needed) != 0 {
		t.Fatal("prepared execution inputs share mutable storage")
	}
	live.ExecutablePaths[0] = "/different"
	if _, err := cloneExactPreparedInputs(expected, live); err == nil ||
		!strings.Contains(err.Error(), "differs") {
		t.Fatalf("drifted live inputs = %v", err)
	}
}

func TestPlanByteCeilingIsSharedByConstructionAndDecode(t *testing.T) {
	if err := validatePlanByteLength(MaximumPlanObjectBytes); err != nil {
		t.Fatalf("exact plan ceiling rejected: %v", err)
	}
	if err := validatePlanByteLength(MaximumPlanObjectBytes + 1); err == nil {
		t.Fatal("oversized plan length was accepted")
	}
	if _, err := DecodePlan(make([]byte, MaximumPlanObjectBytes+1)); err == nil ||
		!strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized encoded plan was not rejected before decoding: %v", err)
	}
}

func TestResolvePairDiffIsExactlyRepoViewMCP(t *testing.T) {
	t.Parallel()
	loaded := validLoadedSuite()
	tool := repoViewToolFixture()
	pair, err := ResolvePair(preparedSuiteFixture(loaded), tool)
	if err != nil {
		t.Fatal(err)
	}
	baseline := pair.Baseline()
	candidate := pair.Candidate()

	if !reflect.DeepEqual(baseline.Prompt, candidate.Prompt) {
		t.Fatal("paired prompt bytes differ")
	}
	if len(baseline.MCPServers) != 0 || len(candidate.MCPServers) != 1 {
		t.Fatalf(
			"unexpected MCP registries: baseline=%d candidate=%d",
			len(baseline.MCPServers),
			len(candidate.MCPServers),
		)
	}
	candidate.MCPServers = nil
	baseline.MCPServers = nil
	if !reflect.DeepEqual(baseline, candidate) {
		t.Fatal("paired invocations differ after removing candidate MCP registration")
	}
	proof := pair.Proof()
	if len(proof.Differences) != 1 ||
		proof.Differences[0].JSONPointer != "/mcp_servers" {
		t.Fatalf("unexpected parity differences: %+v", proof.Differences)
	}
	if proof.PromptSHA256 != SHA256(loaded.prompt) {
		t.Fatal("parity proof does not bind actual prompt bytes")
	}
}

func TestNewRepoViewToolPinsActualExecutable(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "repo-view")
	content := []byte("executable fixture")
	if err := os.WriteFile(path, content, 0o700); err != nil {
		t.Fatal(err)
	}
	tool, err := NewRepoViewTool(path)
	if err != nil {
		t.Fatal(err)
	}
	if tool.executable != path || tool.sha256 != SHA256(content) {
		t.Fatalf("unexpected tool binding: %+v", tool)
	}
	link := filepath.Join(t.TempDir(), "repo-view-link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRepoViewTool(link); err == nil {
		t.Fatal("symlinked repo-view executable was accepted")
	}
	hardlink := filepath.Join(t.TempDir(), "repo-view-hardlink")
	if err := os.Link(path, hardlink); err != nil {
		t.Fatal(err)
	}
	if _, err := NewRepoViewTool(path); err == nil {
		t.Fatal("hard-linked repo-view executable was accepted")
	}
}

func TestPrepareSuiteRejectsUncheckedHarness(t *testing.T) {
	t.Parallel()
	loaded := validLoadedSuite()
	path := filepath.Join(t.TempDir(), "harness")
	if err := os.WriteFile(path, []byte("actual"), 0o700); err != nil {
		t.Fatal(err)
	}
	loaded.suite.HarnessExecutable = path
	loaded.suite.HarnessSHA256 = SHA256([]byte("claimed"))
	loaded = bindFixtureSuiteJSON(t, loaded)
	if _, err := PrepareSuite(context.Background(), loaded, fake.Adapter{}); err == nil ||
		!strings.Contains(err.Error(), "digest mismatch") {
		t.Fatalf("expected harness digest error, got %v", err)
	}
}

func TestResolvedIdentityMustMatchEveryRequestedField(t *testing.T) {
	t.Parallel()
	suite := validLoadedSuite().suite
	identity := preparedSuiteFixture(validLoadedSuite()).identity
	mutations := map[string]func(*harness.Identity){
		"kind":           func(value *harness.Identity) { value.Kind = "other" },
		"digest":         func(value *harness.Identity) { value.ExecutableSHA256 = SHA256([]byte("other")) },
		"model":          func(value *harness.Identity) { value.Model = "other" },
		"model revision": func(value *harness.Identity) { value.ModelRevision = "other-revision/v1" },
		"reasoning":      func(value *harness.Identity) { value.ReasoningEffort = "other" },
		"version":        func(value *harness.Identity) { value.ExecutableVersion = "" },
		"adapter":        func(value *harness.Identity) { value.AdapterVersion = "" },
		"adapter digest": func(value *harness.Identity) { value.AdapterExecutableSHA256 = "" },
		"decoder":        func(value *harness.Identity) { value.DecoderSchema = "" },
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			changed := identity
			mutate(&changed)
			if err := validateResolvedIdentity(changed, suite); err == nil {
				t.Fatal("mismatched resolved identity was accepted")
			}
		})
	}
}

func TestResolvePairRejectsZeroPreparedSuite(t *testing.T) {
	t.Parallel()
	if _, err := ResolvePair(PreparedSuite{}, repoViewToolFixture()); err == nil {
		t.Fatal("zero prepared suite was accepted")
	}
}

func TestPairReturnsDefensiveCopies(t *testing.T) {
	t.Parallel()
	pair := mustPair(t)
	baseline := pair.Baseline()
	candidate := pair.Candidate()
	baseline.Prompt[0] = 'X'
	baseline.Environment["NEW"] = "value"
	candidate.MCPServers[0].Arguments[0] = "answer-key"

	if string(pair.Baseline().Prompt) != string(validLoadedSuite().prompt) {
		t.Fatal("baseline prompt mutation escaped defensive copy")
	}
	if _, exists := pair.Baseline().Environment["NEW"]; exists {
		t.Fatal("environment mutation escaped defensive copy")
	}
	if pair.Candidate().MCPServers[0].Arguments[0] != "mcp" {
		t.Fatal("MCP argument mutation escaped defensive copy")
	}
}

func TestParityRejectsEveryNonToolDifference(t *testing.T) {
	t.Parallel()
	pair := mustPair(t)
	tests := map[string]func(*harness.Invocation){
		"prompt": func(value *harness.Invocation) { value.Prompt = []byte("different") },
		"argument": func(value *harness.Invocation) {
			value.Arguments = append(value.Arguments, "--candidate")
		},
		"environment":       func(value *harness.Invocation) { value.Environment["ONLY"] = "candidate" },
		"executable":        func(value *harness.Invocation) { value.Executable = "/different" },
		"executable digest": func(value *harness.Invocation) { value.ExecutableSHA256 = SHA256([]byte("different")) },
		"model":             func(value *harness.Invocation) { value.Model = "other-model" },
		"requested model":   func(value *harness.Invocation) { value.RequestedModel = "other-request" },
		"model revision":    func(value *harness.Invocation) { value.ModelRevision = "other-revision/v1" },
		"reasoning":         func(value *harness.Invocation) { value.ReasoningEffort = "high" },
		"resolved identity": func(value *harness.Invocation) { value.HarnessIdentity.Model = "other" },
		"identity adapter executable": func(value *harness.Invocation) {
			value.HarnessIdentity.AdapterExecutableSHA256 = SHA256([]byte("other-adapter"))
		},
		"identity control config": func(value *harness.Invocation) {
			value.HarnessIdentity.AdapterControlConfigSHA256 = SHA256([]byte("other-control"))
		},
		"identity effective config": func(value *harness.Invocation) {
			value.HarnessIdentity.AdapterConfigSHA256 = SHA256([]byte("other-config"))
		},
		"identity kind": func(value *harness.Invocation) {
			value.HarnessIdentity.Kind = "other-kind"
		},
		"identity adapter version": func(value *harness.Invocation) {
			value.HarnessIdentity.AdapterVersion = "other-adapter/v1"
		},
		"identity executable version": func(value *harness.Invocation) {
			value.HarnessIdentity.ExecutableVersion = "other-harness/v1"
		},
		"identity harness digest": func(value *harness.Invocation) {
			value.HarnessIdentity.ExecutableSHA256 = SHA256([]byte("other-harness"))
		},
		"identity model revision": func(value *harness.Invocation) {
			value.HarnessIdentity.ModelRevision = "other-model@2026-08-01"
		},
		"identity reasoning": func(value *harness.Invocation) {
			value.HarnessIdentity.ReasoningEffort = "other"
		},
		"identity decoder": func(value *harness.Invocation) {
			value.HarnessIdentity.DecoderSchema = "other-output/v1"
		},
		"permission":             func(value *harness.Invocation) { value.PermissionProfile = "other" },
		"developer instructions": func(value *harness.Invocation) { value.DeveloperInstructions = "hint" },
		"working directory":      func(value *harness.Invocation) { value.WorkingDirectory = "/other" },
		"source revision":        func(value *harness.Invocation) { value.SourceRevision = strings.Repeat("2", 40) },
		"source base":            func(value *harness.Invocation) { value.SourceBaseRevision = strings.Repeat("3", 40) },
		"source tree":            func(value *harness.Invocation) { value.SourceTreeSHA256 = SHA256([]byte("other")) },
		"Git executable":         func(value *harness.Invocation) { value.GitExecutable = "/other/git" },
		"Git executable digest":  func(value *harness.Invocation) { value.GitExecutableSHA256 = SHA256([]byte("other-git")) },
		"Git metadata digest":    func(value *harness.Invocation) { value.GitMetadataSHA256 = SHA256([]byte("other-metadata")) },
		"runner executable":      func(value *harness.Invocation) { value.RunnerExecutable = "/other/tokenbench" },
		"runner executable digest": func(value *harness.Invocation) {
			value.RunnerExecutableSHA256 = SHA256([]byte("other-runner"))
		},
		"timeout": func(value *harness.Invocation) { value.TimeoutMillis++ },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			candidate := pair.Candidate()
			mutate(&candidate)
			if _, err := ProveParity(pair.Baseline(), candidate); err == nil {
				t.Fatal("forbidden difference was accepted")
			}
		})
	}
}

func TestParityRejectsWrongMCPCardinality(t *testing.T) {
	t.Parallel()
	pair := mustPair(t)
	baseline, candidate := pair.Baseline(), pair.Candidate()
	baseline.MCPServers = append([]harness.MCPServer(nil), candidate.MCPServers...)
	if _, err := ProveParity(baseline, candidate); err == nil {
		t.Fatal("baseline MCP registration was accepted")
	}

	baseline = pair.Baseline()
	candidate = pair.Candidate()
	candidate.MCPServers = []harness.MCPServer{}
	if _, err := ProveParity(baseline, candidate); err == nil {
		t.Fatal("candidate without repo_view MCP was accepted")
	}

	candidate = pair.Candidate()
	candidate.MCPServers = append(candidate.MCPServers, candidate.MCPServers[0])
	if _, err := ProveParity(baseline, candidate); err == nil {
		t.Fatal("candidate with multiple MCP registrations was accepted")
	}
}

func TestParityRejectsOpaqueCommonEscapeHatches(t *testing.T) {
	t.Parallel()
	pair := mustPair(t)
	tests := map[string]func(*harness.Invocation, *harness.Invocation){
		"arguments": func(baseline, candidate *harness.Invocation) {
			baseline.Arguments = []string{"--config", "mcp_servers.oracle=..."}
			candidate.Arguments = append([]string(nil), baseline.Arguments...)
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			baseline, candidate := pair.Baseline(), pair.Candidate()
			mutate(&baseline, &candidate)
			if _, err := ProveParity(baseline, candidate); err == nil {
				t.Fatal("opaque common escape hatch was accepted")
			}
		})
	}
}

func TestParityRejectsNonGenericRepoViewRegistration(t *testing.T) {
	t.Parallel()
	pair := mustPair(t)
	tests := map[string]func(*harness.MCPServer){
		"name":             func(server *harness.MCPServer) { server.Name = "oracle" },
		"command":          func(server *harness.MCPServer) { server.Command = "relative" },
		"digest":           func(server *harness.MCPServer) { server.ExecutableSHA256 = "bad" },
		"required":         func(server *harness.MCPServer) { server.Required = false },
		"read only":        func(server *harness.MCPServer) { server.ReadOnly = false },
		"environment hint": func(server *harness.MCPServer) { server.Environment["ANSWER"] = "yes" },
		"argument hint":    func(server *harness.MCPServer) { server.Arguments = append(server.Arguments, "--symbol", "Answer") },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			candidate := pair.Candidate()
			mutate(&candidate.MCPServers[0])
			if _, err := ProveParity(pair.Baseline(), candidate); err == nil {
				t.Fatal("invalid repo-view registration was accepted")
			}
		})
	}
}

func TestResolvedPlanDetectsTampering(t *testing.T) {
	t.Parallel()
	plan := mustPlan(t)
	if err := plan.Validate(); err != nil {
		t.Fatalf("valid plan: %v", err)
	}
	plan.Candidate.Model = "candidate-only-model"
	if err := plan.Validate(); err == nil {
		t.Fatal("tampered plan was accepted")
	}
}

func TestResolvedPlanRejectsSharedHiddenCommonArguments(t *testing.T) {
	t.Parallel()
	plan := mustPlan(t)
	plan.RenderedProcesses.Baseline.Argv = append(
		plan.RenderedProcesses.Baseline.Argv,
		"--hidden-common-override",
	)
	plan.RenderedProcesses.Candidate.Argv = append(
		append(
			[]string(nil),
			plan.RenderedProcesses.Baseline.Argv...,
		),
		plan.RenderedProcesses.CandidateMCPArguments...,
	)
	plan.RenderedProcessesSHA256 = mustJSONSHA256(t, plan.RenderedProcesses)
	if err := plan.Validate(); err == nil ||
		!strings.Contains(err.Error(), "code-owned fake common process") {
		t.Fatalf("shared hidden common argv was accepted: %v", err)
	}
}

func TestResolvedPlanRejectsRequestedResolvedModelMismatch(t *testing.T) {
	t.Parallel()
	plan := mustPlan(t)
	plan.Baseline.RequestedModel = "other-model"
	plan.Candidate.RequestedModel = "other-model"
	recommitPlan(t, &plan)
	if err := plan.Validate(); err == nil ||
		!strings.Contains(err.Error(), "does not match plan") {
		t.Fatalf("requested/resolved model mismatch was accepted: %v", err)
	}
}

func TestResolvedPlanBindsRelativePathsToSuiteOrigin(t *testing.T) {
	t.Parallel()
	plan := mustPlan(t)
	authored, err := decodeEmbeddedSuite(plan.SuiteJSON)
	if err != nil {
		t.Fatal(err)
	}
	origin := filepath.Dir(plan.SuitePath)
	authored.PromptFile, err = filepath.Rel(origin, plan.ResolvedSuite.PromptFile)
	if err != nil {
		t.Fatal(err)
	}
	authored.SourceRoot, err = filepath.Rel(origin, plan.ResolvedSuite.SourceRoot)
	if err != nil {
		t.Fatal(err)
	}
	plan.SuiteJSON, err = json.Marshal(authored)
	if err != nil {
		t.Fatal(err)
	}
	plan.SuiteSHA256 = SHA256(plan.SuiteJSON)
	if err := plan.Validate(); err != nil {
		t.Fatalf("relative-path plan is invalid before rebinding: %v", err)
	}
	plan.SuitePath = filepath.Join(filepath.Dir(plan.SuitePath), "moved", "suite.json")
	if err := plan.Validate(); err == nil || !strings.Contains(err.Error(), "resolved suite") {
		t.Fatalf("suite-origin rebinding was accepted: %v", err)
	}

	plan = mustPlan(t)
	plan.ResolvedSuite.SourceRoot = "/different-source"
	digest, err := JSONSHA256(plan.ResolvedSuite)
	if err != nil {
		t.Fatal(err)
	}
	plan.ResolvedSuiteSHA256 = digest
	if err := plan.Validate(); err == nil || !strings.Contains(err.Error(), "authored suite") {
		t.Fatalf("resolved source rebinding was accepted: %v", err)
	}
}

func TestDecodePlanRejectsNonUTF8Prompt(t *testing.T) {
	t.Parallel()
	plan := mustPlan(t)
	invalidPrompt := []byte{0xff}
	plan.Baseline.Prompt = append([]byte(nil), invalidPrompt...)
	plan.Candidate.Prompt = append([]byte(nil), invalidPrompt...)
	plan.RenderedProcesses.Baseline.Stdin = append([]byte(nil), invalidPrompt...)
	plan.RenderedProcesses.Candidate.Stdin = append([]byte(nil), invalidPrompt...)
	recommitPlan(t, &plan)
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodePlan(raw); err == nil ||
		!strings.Contains(err.Error(), "valid UTF-8") {
		t.Fatalf("non-UTF-8 prompt was accepted: %v", err)
	}
}

func TestRenderPairRejectsAdapterChangedPrompt(t *testing.T) {
	t.Parallel()
	pair := mustPair(t)
	if _, err := renderPair(
		context.Background(),
		maliciousAdapter{Adapter: fake.Adapter{}},
		pair.Baseline(),
		pair.Candidate(),
	); err == nil {
		t.Fatal("adapter-introduced prompt drift was accepted")
	}
}

type maliciousAdapter struct {
	fake.Adapter
}

func (adapter maliciousAdapter) Build(
	ctx context.Context,
	invocation harness.Invocation,
) (harness.ProcessSpec, error) {
	process, err := adapter.Adapter.Build(ctx, invocation)
	if err == nil {
		process.Stdin = []byte("adapter-injected answer key")
	}
	return process, err
}

func TestDecodePlanIsStrict(t *testing.T) {
	t.Parallel()
	plan := mustPlan(t)
	raw, err := json.Marshal(plan)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodePlan(raw); err != nil {
		t.Fatalf("decode valid plan: %v", err)
	}

	withUnknown := append([]byte(nil), raw[:len(raw)-1]...)
	withUnknown = append(withUnknown, []byte(`,"baseline_prompt":"different"}`)...)
	if _, err := DecodePlan(withUnknown); err == nil {
		t.Fatal("unknown plan field was accepted")
	}
}

func TestPairBuildReverifiesInputsAndUsesBoundAdapter(t *testing.T) {
	t.Parallel()
	pair, toolPath := buildReadyPair(t, fake.Adapter{})
	processes, err := pair.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(processes.CandidateMCPArguments) == 0 ||
		len(processes.Candidate.Argv) <= len(processes.Baseline.Argv) ||
		!reflect.DeepEqual(
			processes.Candidate.Argv[:len(processes.Baseline.Argv)],
			processes.Baseline.Argv,
		) {
		t.Fatalf("invalid centrally rendered process pair: %+v", processes)
	}
	if err := os.WriteFile(toolPath, []byte("changed"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := pair.Build(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "changed") {
		t.Fatalf("changed repo-view executable was accepted: %v", err)
	}
}

func TestPairBuildRejectsResolvedAdapterIdentityDrift(t *testing.T) {
	t.Parallel()
	adapter := &driftingIdentityAdapter{}
	pair, _ := buildReadyPair(t, adapter)
	if _, err := pair.Build(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "identity changed") {
		t.Fatalf("adapter identity drift was accepted: %v", err)
	}
}

func TestAdapterOwnedCommonEnvironmentIsEqualAcrossArms(t *testing.T) {
	t.Parallel()
	adapter := &environmentAdapter{environment: map[string]string{
		"LANG": "C.UTF-8",
		"TZ":   "UTC",
	}}
	pair, _ := buildReadyPair(t, adapter)
	processes, err := pair.Build(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(processes.Baseline.Environment, adapter.environment) ||
		!reflect.DeepEqual(processes.Baseline.Environment, processes.Candidate.Environment) {
		t.Fatalf("common environment was not preserved: %+v", processes)
	}
}

func TestPairBuildRejectsCommonEnvironmentDrift(t *testing.T) {
	t.Parallel()
	adapter := &environmentAdapter{
		environment: map[string]string{"LANG": "C.UTF-8"},
		drift:       true,
	}
	pair, _ := buildReadyPair(t, adapter)
	if _, err := pair.Build(context.Background()); err == nil ||
		!strings.Contains(err.Error(), "environment changed") {
		t.Fatalf("adapter environment drift was accepted: %v", err)
	}
}

type environmentAdapter struct {
	fake.Adapter
	environment map[string]string
	calls       int
	drift       bool
}

func (adapter *environmentAdapter) CommonEnvironment(
	_ context.Context,
	_ harness.ResolveRequest,
) (map[string]string, error) {
	adapter.calls++
	result := cloneMap(adapter.environment)
	if adapter.drift && adapter.calls > 1 {
		result["TZ"] = "UTC"
	}
	return result, nil
}

type driftingIdentityAdapter struct {
	fake.Adapter
	resolveCount int
}

func (adapter *driftingIdentityAdapter) Resolve(
	ctx context.Context,
	request harness.ResolveRequest,
) (harness.Identity, error) {
	identity, err := adapter.Adapter.Resolve(ctx, request)
	adapter.resolveCount++
	if adapter.resolveCount > 1 {
		identity.AdapterConfigSHA256 = SHA256([]byte("changed effective config"))
	}
	return identity, err
}

func buildReadyPair(t *testing.T, adapter harness.Adapter) (Pair, string) {
	t.Helper()
	directory := t.TempDir()
	root := filepath.Join(directory, "source")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	pairTestGit(t, root, "init", "--quiet")
	tracked := filepath.Join(root, "file.txt")
	if err := os.WriteFile(tracked, []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pairTestGit(t, root, "add", "file.txt")
	pairTestCommit(t, root, "base")
	base := strings.TrimSpace(pairTestGit(t, root, "rev-parse", "HEAD"))
	if err := os.WriteFile(tracked, []byte("head\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	pairTestGit(t, root, "add", "file.txt")
	pairTestCommit(t, root, "head")
	revision := strings.TrimSpace(pairTestGit(t, root, "rev-parse", "HEAD"))
	treeSHA256, err := source.TreeDigest(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	gitExecutable, gitSHA256 := pairTestGitIdentity(t)
	harnessPath := filepath.Join(directory, "harness")
	if err := os.WriteFile(harnessPath, []byte("harness"), 0o700); err != nil {
		t.Fatal(err)
	}
	toolPath := filepath.Join(directory, "repo-view")
	if err := os.WriteFile(toolPath, []byte("repo-view"), 0o700); err != nil {
		t.Fatal(err)
	}
	prompt := []byte("Explain the change.\n")
	loaded := LoadedSuite{
		suite: Suite{
			SchemaVersion:          SuiteSchemaVersion,
			ID:                     "build-fixture",
			PromptFile:             filepath.Join(directory, "prompt.md"),
			HarnessKind:            adapter.Kind(),
			HarnessExecutable:      harnessPath,
			HarnessSHA256:          SHA256([]byte("harness")),
			ArtifactManifestSHA256: SHA256([]byte("artifact-manifest")),
			GitExecutable:          gitExecutable,
			GitExecutableSHA256:    gitSHA256,
			Model:                  "fixed-model",
			ExpectedModelRevision:  "fixed-model@2026-08-01",
			ReasoningEffort:        "medium",
			PermissionProfile:      "read-only",
			DeveloperInstructions:  "common instructions",
			SourceRoot:             root,
			SourceRevision:         revision,
			SourceBaseRevision:     base,
			SourceTreeSHA256:       treeSHA256,
			TimeoutMillis:          30_000,
			Repetitions:            2,
			Seed:                   1,
		},
		path:         filepath.Join(directory, "suite.json"),
		prompt:       prompt,
		promptSHA256: SHA256(prompt),
	}
	loaded = bindFixtureSuiteJSON(t, loaded)
	prepared, err := PrepareSuite(context.Background(), loaded, adapter)
	if err != nil {
		t.Fatal(err)
	}
	tool, err := NewRepoViewTool(toolPath)
	if err != nil {
		t.Fatal(err)
	}
	pair, err := ResolvePair(prepared, tool)
	if err != nil {
		t.Fatal(err)
	}
	return pair, toolPath
}

func pairTestCommit(t *testing.T, root, message string) {
	t.Helper()
	pairTestGit(
		t,
		root,
		"-c", "user.name=Tokenbench Test",
		"-c", "user.email=tokenbench@example.invalid",
		"commit", "--quiet", "-m", message,
	)
}

func pairTestGit(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	command.Env = append(
		os.Environ(),
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_SYSTEM="+os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
	return string(output)
}

func pairTestGitIdentity(t *testing.T) (string, string) {
	t.Helper()
	path, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	path, err = filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := FileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, digest
}

func mustPair(t *testing.T) Pair {
	t.Helper()
	pair, err := ResolvePair(
		preparedSuiteFixture(validLoadedSuite()),
		repoViewToolFixture(),
	)
	if err != nil {
		t.Fatal(err)
	}
	return pair
}

func mustPlan(t *testing.T) ResolvedPlan {
	t.Helper()
	pair := mustPair(t)
	processes, err := renderPair(
		context.Background(),
		pair.adapter,
		pair.Baseline(),
		pair.Candidate(),
	)
	if err != nil {
		t.Fatal(err)
	}
	plan, err := pair.plan(processes)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func recommitPlan(t *testing.T, plan *ResolvedPlan) {
	t.Helper()
	common := cloneInvocation(plan.Baseline)
	common.MCPServers = nil
	plan.Parity.CommonInvocationSHA256 = mustJSONSHA256(t, common)
	plan.Parity.BaselineInvocationSHA256 = mustJSONSHA256(t, plan.Baseline)
	plan.Parity.CandidateInvocationSHA256 = mustJSONSHA256(t, plan.Candidate)
	plan.Parity.PromptSHA256 = SHA256(plan.Baseline.Prompt)
	plan.Parity.RepoViewRegistrationSHA256 = mustJSONSHA256(
		t,
		plan.Candidate.MCPServers[0],
	)
	plan.Parity.Differences[0].BaselineSHA256 = mustJSONSHA256(
		t,
		plan.Baseline.MCPServers,
	)
	plan.Parity.Differences[0].CandidateSHA256 = mustJSONSHA256(
		t,
		plan.Candidate.MCPServers,
	)
	plan.PromptSHA256 = SHA256(plan.Baseline.Prompt)
	plan.RenderedProcessesSHA256 = mustJSONSHA256(t, plan.RenderedProcesses)
}

func mustJSONSHA256(t *testing.T, value any) string {
	t.Helper()
	digest, err := JSONSHA256(value)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func preparedSuiteFixture(loaded LoadedSuite) PreparedSuite {
	adapter := fake.Adapter{}
	runnerExecutable, runnerSHA256, err := currentRunnerIdentity()
	if err != nil {
		panic(err)
	}
	identity, err := adapter.Resolve(context.Background(), harness.ResolveRequest{
		Environment:            map[string]string{},
		Executable:             loaded.suite.HarnessExecutable,
		ExecutableSHA256:       loaded.suite.HarnessSHA256,
		Model:                  loaded.suite.Model,
		ExpectedModelRevision:  loaded.suite.ExpectedModelRevision,
		ReasoningEffort:        loaded.suite.ReasoningEffort,
		PermissionProfile:      loaded.suite.PermissionProfile,
		DeveloperInstructions:  loaded.suite.DeveloperInstructions,
		WorkingDirectory:       loaded.suite.SourceRoot,
		SourceRevision:         loaded.suite.SourceRevision,
		SourceBaseRevision:     loaded.suite.SourceBaseRevision,
		SourceTreeSHA256:       loaded.suite.SourceTreeSHA256,
		GitExecutable:          "/usr/bin/git",
		GitExecutableSHA256:    SHA256([]byte("git")),
		GitMetadataSHA256:      SHA256([]byte("git-metadata")),
		RunnerExecutable:       runnerExecutable,
		RunnerExecutableSHA256: runnerSHA256,
		TimeoutMillis:          loaded.suite.TimeoutMillis,
	})
	if err != nil {
		panic(err)
	}
	return PreparedSuite{
		identity:    identity,
		environment: map[string]string{},
		loaded:      loaded,
		adapter:     adapter,
		source: source.Snapshot{
			GitExecutable:       "/usr/bin/git",
			GitExecutableSHA256: SHA256([]byte("git")),
			GitMetadataSHA256:   SHA256([]byte("git-metadata")),
		},
		runnerExecutable:       runnerExecutable,
		runnerExecutableSHA256: runnerSHA256,
	}
}

func repoViewToolFixture() RepoViewTool {
	return RepoViewTool{
		executable: "/tools/repo-view",
		sha256:     SHA256([]byte("tool")),
	}
}

func validLoadedSuite() LoadedSuite {
	prompt := []byte("Explain the change.\n")
	loaded := LoadedSuite{
		suite: Suite{
			SchemaVersion:          SuiteSchemaVersion,
			ID:                     "fixture",
			PromptFile:             "/suite/prompt.md",
			HarnessKind:            "fake",
			HarnessExecutable:      "/tools/fake-harness",
			HarnessSHA256:          SHA256([]byte("harness")),
			ArtifactManifestSHA256: SHA256([]byte("artifact-manifest")),
			GitExecutable:          "/usr/bin/git",
			GitExecutableSHA256:    SHA256([]byte("git")),
			Model:                  "gpt-5.2-codex",
			ExpectedModelRevision:  "gpt-5.2-codex@2026-08-01",
			ReasoningEffort:        "medium",
			PermissionProfile:      "read-only",
			DeveloperInstructions:  "Answer using repository evidence.",
			SourceRoot:             "/source",
			SourceRevision:         strings.Repeat("1", 40),
			SourceBaseRevision:     strings.Repeat("0", 40),
			SourceTreeSHA256:       SHA256([]byte("tree")),
			TimeoutMillis:          60_000,
			Repetitions:            10,
			Seed:                   42,
		},
		path:         "/suite/suite.json",
		prompt:       prompt,
		promptSHA256: SHA256(prompt),
	}
	raw, err := json.Marshal(loaded.suite)
	if err != nil {
		panic(err)
	}
	loaded.raw = raw
	loaded.sha256 = SHA256(raw)
	return loaded
}

func bindFixtureSuiteJSON(t *testing.T, loaded LoadedSuite) LoadedSuite {
	t.Helper()
	raw, err := json.Marshal(loaded.suite)
	if err != nil {
		t.Fatal(err)
	}
	loaded.raw = raw
	loaded.sha256 = SHA256(raw)
	return loaded
}
