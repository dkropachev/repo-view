package study

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dkropachev/repo-view/benchmarks/tokenbench"
	"github.com/dkropachev/repo-view/benchmarks/tokenbench/cas"
	"github.com/dkropachev/repo-view/benchmarks/tokenbench/evidence"
	"github.com/dkropachev/repo-view/benchmarks/tokenbench/harness"
)

func TestInputManifestCanonicalGolden(t *testing.T) {
	policy, _ := policyFixture(t, 2)
	manifest := inputManifestFixture(t, policy)
	manifest.Slots[1].Root = nil
	manifest.Slots[1].NotAttemptedReason = "The preregistered slot was not started."
	raw, err := EncodeInputManifest(policy, manifest)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "study-inputs-v1.json"))
	if err != nil {
		t.Fatal(err)
	}
	want = bytes.TrimSuffix(want, []byte{'\n'})
	if !bytes.Equal(raw, want) {
		t.Fatalf("canonical input manifest differs from golden\n got: %s\nwant: %s", raw, want)
	}
	decoded, err := DecodeInputManifest(policy, want)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.Slots[0].Root == nil || decoded.Slots[1].Root != nil ||
		decoded.Slots[1].NotAttemptedReason == "" {
		t.Fatalf("decoded input states differ: %+v", decoded.Slots)
	}
}

func TestInputManifestStrictCanonicalDecode(t *testing.T) {
	policy, _ := policyFixture(t, 2)
	manifest := inputManifestFixture(t, policy)
	raw, err := EncodeInputManifest(policy, manifest)
	if err != nil {
		t.Fatal(err)
	}
	nullSlots := manifest
	nullSlots.Slots = nil
	nullRaw, err := canonicalJSON(nullSlots)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{
		"trailing newline": append(append([]byte(nil), raw...), '\n'),
		"unknown field": append(
			append([]byte(nil), raw[:len(raw)-1]...),
			[]byte(`,"surprise":true}`)...,
		),
		"duplicate field": []byte(strings.Replace(
			string(raw),
			`"policy_sha256":"`,
			`"policy_sha256":"`+manifest.PolicySHA256+`","policy_sha256":"`,
			1,
		)),
		"null slots": nullRaw,
	}
	for name, changed := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeInputManifest(policy, changed); err == nil {
				t.Fatal("noncanonical input manifest was accepted")
			}
		})
	}
}

func TestInputManifestRejectsMatrixCorruptionAndRootReuse(t *testing.T) {
	policy, _ := policyFixture(t, 2)
	tests := map[string]func(*InputManifest){
		"wrong policy": func(manifest *InputManifest) {
			manifest.PolicySHA256 = strings.Repeat("f", 64)
		},
		"nil slots": func(manifest *InputManifest) {
			manifest.Slots = nil
		},
		"missing slot": func(manifest *InputManifest) {
			manifest.Slots = manifest.Slots[:1]
		},
		"extra slot": func(manifest *InputManifest) {
			manifest.Slots = append(manifest.Slots, manifest.Slots[1])
		},
		"reordered slots": func(manifest *InputManifest) {
			manifest.Slots[0], manifest.Slots[1] = manifest.Slots[1], manifest.Slots[0]
		},
		"wrong repetition": func(manifest *InputManifest) {
			manifest.Slots[0].Repetition = 1
		},
		"duplicate root": func(manifest *InputManifest) {
			manifest.Slots[1].Root = cloneObjectRef(manifest.Slots[0].Root)
		},
		"duplicate digest with changed metadata": func(manifest *InputManifest) {
			manifest.Slots[1].Root.Digest = manifest.Slots[0].Root.Digest
			manifest.Slots[1].Root.Size++
		},
		"attempted with missing root": func(manifest *InputManifest) {
			manifest.Slots[0].Root = nil
		},
		"attempted with not-attempted reason": func(manifest *InputManifest) {
			manifest.Slots[0].NotAttemptedReason = "Contradictory state."
		},
		"wrong root media type": func(manifest *InputManifest) {
			manifest.Slots[0].Root.MediaType = "application/json"
		},
		"empty root": func(manifest *InputManifest) {
			manifest.Slots[0].Root.Size = 0
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			manifest := inputManifestFixture(t, policy)
			mutate(&manifest)
			if err := manifest.Validate(policy); err == nil {
				t.Fatal("corrupt input matrix was accepted")
			}
			if _, err := EncodeInputManifest(policy, manifest); err == nil {
				t.Fatal("corrupt input matrix was encoded")
			}
		})
	}
}

func TestInputManifestExclusionIsPreregisteredAndRootBound(t *testing.T) {
	policy, _ := policyFixture(t, 2)
	manifest := inputManifestFixture(t, policy)
	manifest.Slots[0].Exclusion = &InputExclusion{
		EvidenceRoot: *manifest.Slots[0].Root,
		Code:         "integrity_failure",
		Detail:       "The independently recorded integrity condition applies.",
	}
	if err := manifest.Validate(policy); err != nil {
		t.Fatal(err)
	}

	tests := map[string]func(*InputManifest){
		"unregistered code": func(value *InputManifest) {
			value.Slots[0].Exclusion.Code = "favorable_only"
		},
		"blank detail": func(value *InputManifest) {
			value.Slots[0].Exclusion.Detail = ""
		},
		"different root": func(value *InputManifest) {
			value.Slots[0].Exclusion.EvidenceRoot = *value.Slots[1].Root
		},
		"not attempted with exclusion": func(value *InputManifest) {
			value.Slots[0].Root = nil
			value.Slots[0].NotAttemptedReason = "The slot was not started."
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			value := cloneInputManifest(manifest)
			mutate(&value)
			if err := value.Validate(policy); err == nil {
				t.Fatal("invalid exclusion was accepted")
			}
		})
	}
}

func TestAuthenticatedRunBindingRejectsSubstitutionAndDrift(t *testing.T) {
	policy, _ := policyFixture(t, 2)
	slot := inputManifestFixture(t, policy).Slots[0]
	task := policy.Tasks[0]
	valid := bindingRunFixture(t, task, slot.Repetition)
	if _, err := validateAuthenticatedRunBinding(task, slot, valid); err != nil {
		t.Fatal(err)
	}

	tests := map[string]func(*tokenbench.Run){
		"nonpublishable": func(run *tokenbench.Run) {
			run.Plan.Publishable = false
		},
		"suite id": func(run *tokenbench.Run) {
			run.Plan.SuiteID = "task-02"
		},
		"suite digest": func(run *tokenbench.Run) {
			run.Plan.SuiteSHA256 = strings.Repeat("d", 64)
		},
		"prompt digest": func(run *tokenbench.Run) {
			run.Plan.PromptSHA256 = strings.Repeat("e", 64)
		},
		"repetitions": func(run *tokenbench.Run) {
			run.Plan.Repetitions++
		},
		"repetition": func(run *tokenbench.Run) {
			run.Repetition++
		},
		"schedule": func(run *tokenbench.Run) {
			run.Order[0], run.Order[1] = run.Order[1], run.Order[0]
		},
		"arm harness identity": func(run *tokenbench.Run) {
			run.Plan.Candidate.HarnessIdentity.DecoderSchema = "different/v1"
		},
		"arm requested model": func(run *tokenbench.Run) {
			run.Plan.Candidate.RequestedModel = "different-model"
		},
		"resolved model identity": func(run *tokenbench.Run) {
			run.Plan.Baseline.HarnessIdentity.Model = "different-model"
			run.Plan.Candidate.HarnessIdentity.Model = "different-model"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			run := bindingRunFixture(t, task, slot.Repetition)
			mutate(&run)
			if _, err := validateAuthenticatedRunBinding(task, slot, run); err == nil {
				t.Fatal("substituted or drifting run binding was accepted")
			}
		})
	}
}

func TestAuthenticatedRunCommonIdentityIsExact(t *testing.T) {
	policy, _ := policyFixture(t, 2)
	manifest := inputManifestFixture(t, policy)
	run := bindingRunFixture(t, policy.Tasks[0], manifest.Slots[0].Repetition)
	expected, err := validateAuthenticatedRunBinding(policy.Tasks[0], manifest.Slots[0], run)
	if err != nil {
		t.Fatal(err)
	}
	tests := map[string]func(*corpusExecutionIdentity){
		"harness": func(identity *corpusExecutionIdentity) {
			identity.harnessIdentity.AdapterConfigSHA256 = strings.Repeat("f", 64)
		},
		"executor": func(identity *corpusExecutionIdentity) {
			identity.executorIdentity.ConfigSHA256 = strings.Repeat("e", 64)
		},
		"requested model": func(identity *corpusExecutionIdentity) {
			identity.requestedModel = "different-model"
		},
		"resolved model": func(identity *corpusExecutionIdentity) {
			identity.resolvedModel = "different-model"
		},
		"model revision": func(identity *corpusExecutionIdentity) {
			identity.modelRevision = "pinned-model@different"
		},
		"reasoning": func(identity *corpusExecutionIdentity) {
			identity.reasoningEffort = "high"
		},
		"decoder": func(identity *corpusExecutionIdentity) {
			identity.decoderSchema = "different/v1"
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			actual := expected
			mutate(&actual)
			if err := validateCommonExecutionIdentity(expected, actual); err == nil {
				t.Fatal("cross-slot execution drift was accepted")
			}
		})
	}
}

func TestNormalizeVerifiedReplayUsesDerivedObservationsDefensively(t *testing.T) {
	oldBaseline := inputObservation("old baseline", "old_call")
	newBaseline := inputObservation("new baseline", "new_call")
	newCandidate := inputObservation("new candidate", "new_candidate_call")
	parent := cas.ObjectRef{
		Digest: "sha256:" + strings.Repeat("c", 64),
		Size:   1, MediaType: attestationMediaType,
	}
	run := tokenbench.Run{
		Baseline: tokenbench.ArmRun{
			Arm: tokenbench.BaselineArm, Observation: &oldBaseline,
		},
		Candidate: tokenbench.ArmRun{
			Arm: tokenbench.CandidateArm,
			Failure: &tokenbench.AttemptFailure{
				Stage: "decode", Kind: "decoder_error", Message: "fixed failure",
			},
		},
	}
	verified := evidence.VerifiedEvidence{
		BundleKind: evidence.ReplayBundle,
		Replay: &evidence.Replay{
			Baseline:  &newBaseline,
			Candidate: &newCandidate,
			Capture:   evidence.Capture{Run: run},
			Manifest:  evidence.ReplayManifest{Parent: parent},
		},
	}
	normalized, kind, gotParent, err := normalizeVerifiedRun(verified)
	if err != nil {
		t.Fatal(err)
	}
	if kind != evidence.ReplayBundle || gotParent == nil || *gotParent != parent ||
		normalized.Baseline.Observation.FinalAnswer != "new baseline" ||
		normalized.Candidate.Observation.FinalAnswer != "new candidate" ||
		normalized.Candidate.Failure != nil {
		t.Fatalf("unexpected normalized replay: %+v", normalized)
	}
	normalized.Baseline.Observation.ToolCalls[0] = "mutated"
	if verified.Replay.Baseline.ToolCalls[0] != "new_call" ||
		verified.Replay.Capture.Run.Baseline.Observation.ToolCalls[0] != "old_call" {
		t.Fatal("normalized replay aliases evidence-owned observations")
	}

	nonreplayable := verified
	nonreplayable.Replay = &evidence.Replay{
		Baseline: &newBaseline,
		Capture: evidence.Capture{Run: tokenbench.Run{Baseline: tokenbench.ArmRun{
			Failure: &tokenbench.AttemptFailure{
				Stage: "execute", Kind: "nonzero_exit", Message: "fixed failure",
			},
		}}},
	}
	if _, _, _, err := normalizeVerifiedRun(nonreplayable); err == nil {
		t.Fatal("replay observation over an ordinary execution failure was accepted")
	}
}

func TestLoadAuthenticatedCorpusPreservesExplicitNotAttemptedSlots(t *testing.T) {
	policy, _ := policyFixture(t, 2)
	manifest := inputManifestFixture(t, policy)
	for index := range manifest.Slots {
		manifest.Slots[index].Root = nil
		manifest.Slots[index].NotAttemptedReason = "The preregistered slot was not started."
	}
	store := inputTestStore(t)
	verifier := inputTestVerifier(t)
	corpus, err := LoadAuthenticatedCorpus(
		context.Background(), store, verifier, policy, manifest,
	)
	if err != nil {
		t.Fatal(err)
	}
	if corpus.PolicySHA256() != manifest.PolicySHA256 || corpus.SlotCount() != 2 ||
		corpus.AttemptedCount() != 0 || corpus.NotAttemptedCount() != 2 {
		t.Fatalf("unexpected corpus counts: %+v", corpus)
	}
	if _, ok := corpus.CommonIdentity(); ok {
		t.Fatal("all-not-attempted corpus claimed a common execution identity")
	}
	summaries := corpus.SlotSummaries()
	if len(summaries) != 2 || summaries[0].Attempted || summaries[0].NotAttemptedReason == "" {
		t.Fatalf("not-attempted state was not preserved: %+v", summaries)
	}
	summaries[0].NotAttemptedReason = "mutated"
	if corpus.SlotSummaries()[0].NotAttemptedReason == "mutated" {
		t.Fatal("slot summaries alias opaque corpus state")
	}

	withMissingRoot := inputManifestFixture(t, policy)
	if _, err := LoadAuthenticatedCorpus(
		context.Background(), store, verifier, policy, withMissingRoot,
	); err == nil {
		t.Fatal("absent evidence objects produced an authenticated corpus")
	}
}

func inputManifestFixture(t *testing.T, policy Policy) InputManifest {
	t.Helper()
	policySHA256, err := policy.SHA256()
	if err != nil {
		t.Fatal(err)
	}
	digits := "cdef0123456789ab"
	slots := make([]InputSlot, 0)
	for _, task := range policy.Tasks {
		if task.Status != TaskIncluded {
			continue
		}
		for repetition := range task.Repetitions {
			digit := string(digits[len(slots)%len(digits)])
			root := cas.ObjectRef{
				Digest:    "sha256:" + strings.Repeat(digit, 64),
				Size:      128,
				MediaType: attestationMediaType,
			}
			slots = append(slots, InputSlot{
				TaskID: task.TaskID, Repetition: repetition, Root: &root,
			})
		}
	}
	return InputManifest{
		SchemaVersion: InputManifestSchemaVersion,
		PolicySHA256:  policySHA256,
		Slots:         slots,
	}
}

func bindingRunFixture(t *testing.T, task TaskPolicy, repetition int) tokenbench.Run {
	t.Helper()
	identity := harness.Identity{
		AdapterExecutableSHA256:    strings.Repeat("1", 64),
		AdapterControlConfigSHA256: strings.Repeat("2", 64),
		AdapterConfigSHA256:        strings.Repeat("3", 64),
		Kind:                       "codex",
		AdapterVersion:             "tokenbench.codex-adapter/v2",
		ExecutableSHA256:           strings.Repeat("4", 64),
		ExecutableVersion:          "codex-cli/0.144.0",
		Model:                      "pinned-model",
		ModelRevision:              "pinned-model@2026-08-01",
		ReasoningEffort:            "medium",
		DecoderSchema:              "tokenbench.codex.responses-trace/v3",
	}
	invocation := harness.Invocation{
		HarnessIdentity: identity,
		Model:           identity.Model,
		RequestedModel:  identity.Model,
		ModelRevision:   identity.ModelRevision,
		ReasoningEffort: identity.ReasoningEffort,
	}
	plan := tokenbench.ResolvedPlan{
		SuiteID:      task.TaskID,
		SuiteSHA256:  task.SuiteSHA256,
		PromptSHA256: task.PromptSHA256,
		Baseline:     invocation,
		Candidate:    invocation,
		Seed:         42,
		Repetitions:  task.Repetitions,
		Publishable:  true,
	}
	order, err := tokenbench.ScheduledOrder(plan.SuiteSHA256, plan.Seed, repetition)
	if err != nil {
		t.Fatal(err)
	}
	return tokenbench.Run{
		Plan: plan,
		ExecutorIdentity: tokenbench.ExecutorIdentity{
			Kind: "process", Version: "tokenbench.process-executor/v2",
			ConfigSHA256: strings.Repeat("5", 64),
		},
		Order: order, Repetition: repetition,
	}
}

func inputObservation(answer, call string) harness.Observation {
	return harness.Observation{
		FinalAnswer: answer,
		Model:       "pinned-model",
		ToolCalls:   []string{call},
		Usage:       harness.Usage{InputTokens: 2, OutputTokens: 1},
		Completed:   true,
	}
}

func inputTestStore(t *testing.T) *cas.Store {
	t.Helper()
	root := filepath.Join(t.TempDir(), "cas")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := cas.Open(root, cas.Options{MaxObjectBytes: 1 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close input-test CAS: %v", err)
		}
	})
	return store
}

func inputTestVerifier(t *testing.T) *evidence.Verifier {
	t.Helper()
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{0x42}, ed25519.SeedSize))
	publicKey := privateKey.Public().(ed25519.PublicKey)
	digest := sha256.Sum256(publicKey)
	keyID := "ed25519-sha256:" + hex.EncodeToString(digest[:])
	raw, err := json.Marshal(evidence.TrustPolicy{
		SchemaVersion: evidence.TrustPolicySchemaVersion,
		Project:       evidence.AttestationProject,
		Keys: []evidence.TrustedKeyPolicy{{
			KeyID:     keyID,
			PublicKey: base64.RawURLEncoding.EncodeToString(publicKey),
			Roles:     []evidence.BundleKind{evidence.CaptureBundle, evidence.ReplayBundle},
			Status:    evidence.KeyActive,
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := evidence.DecodeTrustPolicy(raw)
	if err != nil {
		t.Fatal(err)
	}
	return verifier
}
