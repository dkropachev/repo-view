package evidence

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/dkropachev/repo-view/benchmarks/tokenbench"
	"github.com/dkropachev/repo-view/benchmarks/tokenbench/cas"
	"github.com/dkropachev/repo-view/benchmarks/tokenbench/harness"
	harnesscodex "github.com/dkropachev/repo-view/benchmarks/tokenbench/harness/codex"
	"github.com/dkropachev/repo-view/benchmarks/tokenbench/internal/selfexec"
)

func TestCapturePublicationIsDeterministicAndRecursive(t *testing.T) {
	store := openTestStore(t)
	run := validRun(t)
	signer, verifier := testSignerAndVerifier(t, []BundleKind{CaptureBundle}, KeyActive)
	first := publishTestRun(t, store, run, signer)
	second := publishTestRun(t, store, run, signer)
	if first != second {
		t.Fatalf("identical captures differ: %+v %+v", first, second)
	}
	capture, err := LoadCapture(context.Background(), store, first, verifier)
	if err != nil {
		t.Fatal(err)
	}
	if string(capture.Run.Baseline.Raw.Artifacts[0].Data) != "trace" ||
		capture.Run.Plan.SuiteSHA256 != run.Plan.SuiteSHA256 {
		t.Fatalf("capture did not round trip: %+v", capture)
	}
}

func TestCallerConstructedRunCannotBePublishedAsLiveCapture(t *testing.T) {
	store := openTestStore(t)
	signer, verifier := testSignerAndVerifier(t, []BundleKind{CaptureBundle}, KeyActive)
	if _, err := PublishRun(
		context.Background(), store, validRun(t), signer, verifier,
	); err == nil {
		t.Fatal("caller-constructed audit run was granted capture authority")
	}
}

func TestReplayIsDeterministicAndDoesNotMutateParent(t *testing.T) {
	store := openTestStore(t)
	run := validRun(t)
	signer, verifier := testSignerAndVerifier(
		t, []BundleKind{CaptureBundle, ReplayBundle}, KeyActive,
	)
	parent := publishTestRun(t, store, run, signer)
	decoder := fixtureDecoder{identity: decoderIdentityForRun(run)}
	first, err := replayCaptureWithDecoder(
		context.Background(), store, parent, verifier, decoder, signer,
	)
	if err != nil {
		t.Fatal(err)
	}
	if first.State != PublicationComplete {
		t.Fatalf("first replay state = %q", first.State)
	}
	second, err := replayCaptureWithDecoder(
		context.Background(), store, parent, verifier, decoder, signer,
	)
	if err != nil {
		t.Fatal(err)
	}
	if second.State != PublicationComplete || first.IntendedRoot != second.IntendedRoot {
		t.Fatalf("identical replays differ: %+v %+v", first, second)
	}
	replay, err := LoadReplay(context.Background(), store, first.IntendedRoot, verifier)
	if err != nil {
		t.Fatal(err)
	}
	if replay.Manifest.Parent != parent || replay.Baseline == nil || replay.Candidate == nil {
		t.Fatalf("invalid replay: %+v", replay)
	}
	if _, err := LoadCapture(context.Background(), store, parent, verifier); err != nil {
		t.Fatalf("parent changed after replay: %v", err)
	}
}

func TestDecoderFailurePublishesNothingOverParent(t *testing.T) {
	store := openTestStore(t)
	run := validRun(t)
	signer, verifier := testSignerAndVerifier(
		t, []BundleKind{CaptureBundle, ReplayBundle}, KeyActive,
	)
	parent := publishTestRun(t, store, run, signer)
	decoder := fixtureDecoder{
		decodeError: errors.New("bad decoder"),
		identity:    decoderIdentityForRun(run),
	}
	if _, err := replayCaptureWithDecoder(
		context.Background(), store, parent, verifier, decoder, signer,
	); err == nil {
		t.Fatal("decoder failure was accepted")
	}
	if _, err := LoadCapture(context.Background(), store, parent, verifier); err != nil {
		t.Fatalf("failed replay damaged parent: %v", err)
	}
}

func TestReplayRejectsNondeterministicOrNoncanonicalDecoder(t *testing.T) {
	store := openTestStore(t)
	run := validRun(t)
	signer, verifier := testSignerAndVerifier(
		t, []BundleKind{CaptureBundle, ReplayBundle}, KeyActive,
	)
	parent := publishTestRun(t, store, run, signer)
	identity := decoderIdentityForRun(run)
	for _, test := range []struct {
		name    string
		decoder replayDecoder
	}{
		{"nondeterministic", &mutatingFixtureDecoder{identity: identity}},
		{"nil tool calls", nilToolCallsDecoder{identity: identity}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := replayCaptureWithDecoder(
				context.Background(),
				store,
				parent,
				verifier,
				test.decoder,
				signer,
			); err == nil {
				t.Fatal("invalid decoder output was accepted")
			}
			if _, err := LoadCapture(context.Background(), store, parent, verifier); err != nil {
				t.Fatalf("failed replay damaged parent: %v", err)
			}
		})
	}
}

func TestReplaySignerMustHaveActiveReplayRole(t *testing.T) {
	store := openTestStore(t)
	run := validRun(t)
	captureSigner := testSigner(t, "replay role capture signer")
	replaySigner := testSigner(t, "replay role candidate signer")
	parent := publishTestRun(t, store, run, captureSigner)
	decoder := fixtureDecoder{identity: decoderIdentityForRun(run)}
	for _, test := range []struct {
		name   string
		roles  []BundleKind
		status KeyStatus
		want   error
	}{
		{"wrong role", []BundleKind{CaptureBundle}, KeyActive, ErrUntrustedAttestation},
		{"retired", []BundleKind{ReplayBundle}, KeyRetired, ErrRetiredAttestation},
		{"revoked", []BundleKind{ReplayBundle}, KeyRevoked, ErrRevokedAttestation},
	} {
		t.Run(test.name, func(t *testing.T) {
			verifier := testVerifier(t,
				testTrustKey{
					signer: captureSigner,
					roles:  []BundleKind{CaptureBundle},
					status: KeyActive,
				},
				testTrustKey{
					signer: replaySigner,
					roles:  test.roles,
					status: test.status,
				},
			)
			result, err := replayCaptureWithDecoder(
				context.Background(), store, parent, verifier, decoder, replaySigner,
			)
			if !errors.Is(err, test.want) || result.IntendedRoot.Digest != "" ||
				result.State != PublicationRetryable {
				t.Fatalf("result=%+v err=%v, want %v", result, err, test.want)
			}
		})
	}
}

func TestLoadCaptureRejectsWrongRootType(t *testing.T) {
	store := openTestStore(t)
	signer, verifier := testSignerAndVerifier(t, []BundleKind{CaptureBundle}, KeyActive)
	root := publishTestRun(t, store, validRun(t), signer)
	root.MediaType = "application/octet-stream"
	if _, err := LoadCapture(context.Background(), store, root, verifier); err == nil {
		t.Fatal("wrong root media type was accepted")
	}
}

func TestRunScheduleTamperingIsRejected(t *testing.T) {
	run := validRun(t)
	run.Order[0], run.Order[1] = run.Order[1], run.Order[0]
	if err := run.Validate(); err == nil {
		t.Fatal("tampered arm order was accepted")
	}
	run = validRun(t)
	run.Repetition = run.Plan.Repetitions
	if err := run.Validate(); err == nil {
		t.Fatal("out-of-range repetition was accepted")
	}
}

func openTestStore(t *testing.T) *cas.Store {
	t.Helper()
	store, err := cas.Open(t.TempDir(), cas.Options{MaxObjectBytes: 4 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	return store
}

func publishTestRun(
	t *testing.T,
	store *cas.Store,
	run tokenbench.Run,
	signer *Ed25519Signer,
) cas.ObjectRef {
	t.Helper()
	publicationVerifier := testVerifier(t, testTrustKey{
		signer: signer,
		roles:  []BundleKind{CaptureBundle},
		status: KeyActive,
	})
	result, err := publishRun(
		context.Background(), store, run, signer, publicationVerifier,
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.State != PublicationComplete {
		t.Fatalf("unexpected publication state %q", result.State)
	}
	return result.IntendedRoot
}

func testSignerAndVerifier(
	t *testing.T,
	roles []BundleKind,
	status KeyStatus,
) (*Ed25519Signer, *Verifier) {
	t.Helper()
	seed := sha256.Sum256([]byte("tokenbench deterministic attestation test key"))
	signer, err := NewEd25519Signer(ed25519.NewKeyFromSeed(seed[:]))
	if err != nil {
		t.Fatal(err)
	}
	publicKey := signer.AttestationPublicKey()
	policy := TrustPolicy{
		SchemaVersion: TrustPolicySchemaVersion,
		Project:       AttestationProject,
		Keys: []TrustedKeyPolicy{{
			KeyID:     attestationKeyID(publicKey),
			PublicKey: base64.RawURLEncoding.EncodeToString(publicKey),
			Roles:     append([]BundleKind(nil), roles...),
			Status:    status,
		}},
	}
	raw, err := json.Marshal(policy)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := DecodeTrustPolicy(raw)
	if err != nil {
		t.Fatal(err)
	}
	return signer, verifier
}

type fixtureDecoder struct {
	decodeError error
	identity    DecoderIdentity
}

type mutatingFixtureDecoder struct {
	calls    int
	identity DecoderIdentity
}

func (decoder *mutatingFixtureDecoder) Identity() DecoderIdentity {
	return decoder.identity
}

func (decoder *mutatingFixtureDecoder) Decode(
	_ context.Context,
	raw harness.RawExecution,
) (harness.Observation, error) {
	observation, err := fixtureDecoder{}.Decode(context.Background(), raw)
	if err != nil {
		return harness.Observation{}, err
	}
	decoder.calls++
	observation.FinalAnswer += strings.Repeat("!", decoder.calls)
	return observation, nil
}

type nilToolCallsDecoder struct {
	identity DecoderIdentity
}

func (decoder nilToolCallsDecoder) Identity() DecoderIdentity {
	return decoder.identity
}

func (nilToolCallsDecoder) Decode(
	_ context.Context,
	raw harness.RawExecution,
) (harness.Observation, error) {
	observation, err := fixtureDecoder{}.Decode(context.Background(), raw)
	observation.ToolCalls = nil
	return observation, err
}

func (decoder fixtureDecoder) Identity() DecoderIdentity {
	return decoder.identity
}

func decoderIdentityForRun(run tokenbench.Run) DecoderIdentity {
	identity := run.Plan.Baseline.HarnessIdentity
	return DecoderIdentity{
		Kind:             "codex",
		Version:          identity.AdapterVersion,
		Schema:           identity.DecoderSchema,
		ExecutableSHA256: identity.AdapterExecutableSHA256,
		ConfigSHA256:     identity.AdapterConfigSHA256,
	}
}

func (decoder fixtureDecoder) Decode(
	_ context.Context,
	raw harness.RawExecution,
) (harness.Observation, error) {
	if decoder.decodeError != nil {
		return harness.Observation{}, decoder.decodeError
	}
	var observation harness.Observation
	decoderJSON := json.NewDecoder(bytes.NewReader(raw.Stdout))
	decoderJSON.DisallowUnknownFields()
	if err := decoderJSON.Decode(&observation); err != nil {
		return harness.Observation{}, err
	}
	return observation, nil
}

func validRun(t *testing.T) tokenbench.Run {
	t.Helper()
	runnerIdentity, err := selfexec.Current()
	if err != nil {
		t.Fatal(err)
	}
	layout := harnesscodex.RuntimeLayout{
		ProxyURL:             "http://127.0.0.1:43119/v1",
		Home:                 "/tokenbench/home",
		CodexHome:            "/tokenbench/codex-home",
		Temp:                 "/tokenbench/tmp",
		ConfigLock:           "/tokenbench/config-lock",
		LocalProxyCapability: harnesscodex.OfflineLocalProxyCapability,
	}
	adapter, err := harnesscodex.New(layout)
	if err != nil {
		t.Fatal(err)
	}
	common := harness.Invocation{
		Environment:            layout.Environment(),
		DeveloperInstructions:  "common instructions",
		PermissionProfile:      "read-only",
		SourceTreeSHA256:       strings.Repeat("b", 64),
		GitExecutable:          "/usr/bin/git",
		GitExecutableSHA256:    strings.Repeat("c", 64),
		GitMetadataSHA256:      strings.Repeat("d", 64),
		RunnerExecutable:       "/tools/tokenbench",
		RunnerExecutableSHA256: runnerIdentity.SHA256,
		Executable:             "/tools/codex",
		ExecutableSHA256:       "08b012d75651efb22b5162be253cd4d28752594082671098e123229b896ba77e",
		Model:                  "gpt-5.4",
		RequestedModel:         "gpt-5.4",
		ModelRevision:          "gpt-5.4@gpt-5.4-2026-03-05",
		ReasoningEffort:        "high",
		SourceBaseRevision:     strings.Repeat("0", 40),
		SourceRevision:         strings.Repeat("1", 40),
		WorkingDirectory:       "/source",
		Arguments:              []string{},
		MCPServers:             []harness.MCPServer{},
		Prompt:                 []byte("task\n"),
		TimeoutMillis:          30_000,
	}
	identity, err := adapter.Resolve(context.Background(), harness.ResolveRequest{
		Environment:            common.Environment,
		Executable:             common.Executable,
		ExecutableSHA256:       common.ExecutableSHA256,
		Model:                  common.RequestedModel,
		ExpectedModelRevision:  common.ModelRevision,
		ReasoningEffort:        common.ReasoningEffort,
		PermissionProfile:      common.PermissionProfile,
		DeveloperInstructions:  common.DeveloperInstructions,
		WorkingDirectory:       common.WorkingDirectory,
		SourceRevision:         common.SourceRevision,
		SourceBaseRevision:     common.SourceBaseRevision,
		SourceTreeSHA256:       common.SourceTreeSHA256,
		GitExecutable:          common.GitExecutable,
		GitExecutableSHA256:    common.GitExecutableSHA256,
		GitMetadataSHA256:      common.GitMetadataSHA256,
		RunnerExecutable:       common.RunnerExecutable,
		RunnerExecutableSHA256: common.RunnerExecutableSHA256,
		TimeoutMillis:          common.TimeoutMillis,
	})
	if err != nil {
		t.Fatal(err)
	}
	common.HarnessIdentity = identity
	baseline := common
	candidate := common
	candidate.MCPServers = []harness.MCPServer{{
		Environment:      map[string]string{},
		Name:             "repo_view",
		Command:          "/usr/bin/repo-view",
		ExecutableSHA256: tokenbench.SHA256([]byte("repo-view")),
		Arguments: []string{
			"mcp",
			"--root", "/source",
			"--base", strings.Repeat("0", 40),
			"--git", "/usr/bin/git",
			"--git-sha256", strings.Repeat("c", 64),
		},
		Required: true,
		ReadOnly: true,
	}}
	proof, err := tokenbench.ProveParity(baseline, candidate)
	if err != nil {
		t.Fatal(err)
	}
	baselineProcess, err := adapter.Build(context.Background(), baseline)
	if err != nil {
		t.Fatal(err)
	}
	suffix, err := harnesscodex.CanonicalMCPArguments(candidate.MCPServers[0])
	if err != nil {
		t.Fatal(err)
	}
	candidateProcess := baselineProcess
	candidateProcess.Argv = append(append([]string{}, baselineProcess.Argv...), suffix...)
	processes := tokenbench.ProcessPair{
		Baseline:              baselineProcess,
		Candidate:             candidateProcess,
		CandidateMCPArguments: suffix,
	}
	processDigest, err := tokenbench.JSONSHA256(processes)
	if err != nil {
		t.Fatal(err)
	}
	suite := tokenbench.Suite{
		SchemaVersion:         tokenbench.SuiteSchemaVersion,
		ID:                    "fixture",
		PromptFile:            "prompt.md",
		HarnessKind:           identity.Kind,
		HarnessExecutable:     common.Executable,
		HarnessSHA256:         common.ExecutableSHA256,
		GitExecutable:         common.GitExecutable,
		GitExecutableSHA256:   common.GitExecutableSHA256,
		Model:                 common.RequestedModel,
		ExpectedModelRevision: common.ModelRevision,
		ReasoningEffort:       common.ReasoningEffort,
		PermissionProfile:     common.PermissionProfile,
		DeveloperInstructions: common.DeveloperInstructions,
		SourceRoot:            common.WorkingDirectory,
		SourceRevision:        common.SourceRevision,
		SourceBaseRevision:    common.SourceBaseRevision,
		SourceTreeSHA256:      common.SourceTreeSHA256,
		TimeoutMillis:         common.TimeoutMillis,
		Repetitions:           1,
		Seed:                  7,
	}
	suiteJSON, err := json.Marshal(suite)
	if err != nil {
		t.Fatal(err)
	}
	resolvedSuite := suite
	resolvedSuite.PromptFile = "/benchmark/prompt.md"
	resolvedSuiteSHA256, err := tokenbench.JSONSHA256(resolvedSuite)
	if err != nil {
		t.Fatal(err)
	}
	plan := tokenbench.ResolvedPlan{
		Parity:                  proof,
		RenderedProcesses:       processes,
		RenderedProcessesSHA256: processDigest,
		SchemaVersion:           tokenbench.PlanSchemaVersion,
		SuiteID:                 "fixture",
		SuiteSHA256:             tokenbench.SHA256(suiteJSON),
		SuiteJSON:               suiteJSON,
		SuitePath:               "/benchmark/suite.json",
		ResolvedSuite:           resolvedSuite,
		ResolvedSuiteSHA256:     resolvedSuiteSHA256,
		PromptSHA256:            tokenbench.SHA256([]byte("task\n")),
		Seed:                    7,
		Repetitions:             1,
		Baseline:                baseline,
		Candidate:               candidate,
	}
	if err := plan.Validate(); err != nil {
		t.Fatal(err)
	}
	baselineObservation := harness.Observation{
		FinalAnswer: "baseline answer",
		Model:       identity.Model,
		ToolCalls:   []string{},
		Usage:       harness.Usage{InputTokens: 10, OutputTokens: 2},
		Completed:   true,
	}
	candidateObservation := harness.Observation{
		FinalAnswer: "candidate answer",
		Model:       identity.Model,
		ToolCalls:   []string{"repo_view.inspect"},
		Usage:       harness.Usage{InputTokens: 8, OutputTokens: 2},
		Completed:   true,
	}
	baselineRaw, _ := json.Marshal(baselineObservation)
	candidateRaw, _ := json.Marshal(candidateObservation)
	run := tokenbench.Run{
		Plan: plan,
		Baseline: tokenbench.ArmRun{
			Observation: &baselineObservation,
			Raw: harness.RawExecution{
				Stdout: baselineRaw,
				Stderr: []byte{},
				Artifacts: []harness.Artifact{{
					Name:      "trace",
					MediaType: "application/json",
					Data:      []byte("trace"),
				}},
				ExitCode: 0,
			},
			Arm: tokenbench.BaselineArm,
		},
		Candidate: tokenbench.ArmRun{
			Observation: &candidateObservation,
			Raw: harness.RawExecution{
				Stdout:    candidateRaw,
				Stderr:    []byte{},
				Artifacts: []harness.Artifact{},
				ExitCode:  0,
			},
			Arm: tokenbench.CandidateArm,
		},
		ExecutorIdentity: tokenbench.ExecutorIdentity{
			Kind:         "process",
			Version:      "tokenbench.process-executor/v1",
			ConfigSHA256: tokenbench.SHA256([]byte("executor")),
		},
		SchemaVersion: tokenbench.RunSchemaVersion,
		Repetition:    0,
	}
	run.Order, err = tokenbench.ScheduledOrder(plan.SuiteSHA256, plan.Seed, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := run.Validate(); err != nil {
		t.Fatal(err)
	}
	return run
}
