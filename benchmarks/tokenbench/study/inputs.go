package study

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/yapless/scopesifter/benchmarks/tokenbench"
	"github.com/yapless/scopesifter/benchmarks/tokenbench/cas"
	"github.com/yapless/scopesifter/benchmarks/tokenbench/evidence"
	"github.com/yapless/scopesifter/benchmarks/tokenbench/harness"
)

const (
	InputManifestSchemaVersion = "tokenbench.study-inputs/v2"

	maxInputManifestBytes    = 256 << 20
	maxInputReasonBytes      = 2_000
	maxInputAttestationBytes = 64 << 10
	attestationMediaType     = "application/vnd.tokenbench.attestation.v2+json"
)

// InputManifest assigns exactly one authenticated evidence root or explicit
// not-attempted state to every included preregistered task/repetition.
//
//nolint:govet,nolintlint // Field order is the byte-level canonical input-manifest wire order.
type InputManifest struct {
	SchemaVersion string      `json:"schema_version"`
	PolicySHA256  string      `json:"policy_sha256"`
	Slots         []InputSlot `json:"slots"`
}

// InputSlot is a closed union. Root is non-nil only for an attempted slot;
// NotAttemptedReason is nonempty only when Root is nil. Exclusion is optional
// for an attempted slot and must bind the same exact root.
//
//nolint:govet,nolintlint // Field order is the byte-level canonical input-slot wire order.
type InputSlot struct {
	TaskID             string          `json:"task_id"`
	Repetition         int             `json:"repetition"`
	Root               *cas.ObjectRef  `json:"root"`
	NotAttemptedReason string          `json:"not_attempted_reason"`
	Exclusion          *InputExclusion `json:"exclusion"`
}

// InputExclusion applies one preregistered reason to an authenticated slot.
// EvidenceRoot is repeated deliberately so an exclusion cannot be detached
// from the root whose condition it describes.
type InputExclusion struct {
	EvidenceRoot cas.ObjectRef `json:"evidence_root"`
	Code         string        `json:"code"`
	Detail       string        `json:"detail"`
}

// DecodeInputManifest accepts only the byte-exact canonical required-field
// representation and validates it against the exact preregistered policy.
func DecodeInputManifest(policy Policy, raw []byte) (InputManifest, error) {
	if len(raw) == 0 || len(raw) > maxInputManifestBytes {
		return InputManifest{}, errors.New("study input manifest size is invalid")
	}
	var manifest InputManifest
	if err := decodeCanonical(raw, &manifest); err != nil {
		return InputManifest{}, fmt.Errorf("decode study input manifest: %w", err)
	}
	policySnapshot, policySHA256, err := snapshotInputPolicy(policy)
	if err != nil {
		return InputManifest{}, err
	}
	manifest = cloneInputManifest(manifest)
	if err := validateInputManifest(policySnapshot, policySHA256, manifest); err != nil {
		return InputManifest{}, err
	}
	return manifest, nil
}

// EncodeInputManifest returns the sole canonical JSON representation of a
// valid input matrix. Caller-owned pointers and slices are copied first.
func EncodeInputManifest(policy Policy, manifest InputManifest) ([]byte, error) {
	policySnapshot, policySHA256, err := snapshotInputPolicy(policy)
	if err != nil {
		return nil, err
	}
	manifest = cloneInputManifest(manifest)
	if err := validateInputManifest(policySnapshot, policySHA256, manifest); err != nil {
		return nil, err
	}
	raw, err := canonicalJSON(manifest)
	if err != nil {
		return nil, fmt.Errorf("encode study input manifest: %w", err)
	}
	if len(raw) == 0 || len(raw) > maxInputManifestBytes {
		return nil, errors.New("study input manifest size is invalid")
	}
	return raw, nil
}

// Validate checks the complete policy-bound matrix without authenticating its
// roots. LoadAuthenticatedCorpus is the authority-producing boundary.
func (manifest InputManifest) Validate(policy Policy) error {
	policySnapshot, policySHA256, err := snapshotInputPolicy(policy)
	if err != nil {
		return err
	}
	return validateInputManifest(
		policySnapshot,
		policySHA256,
		cloneInputManifest(manifest),
	)
}

func snapshotInputPolicy(policy Policy) (Policy, string, error) {
	raw, err := EncodePolicy(policy)
	if err != nil {
		return Policy{}, "", err
	}
	snapshot, err := DecodePolicy(raw)
	if err != nil {
		return Policy{}, "", err
	}
	digest, err := snapshot.SHA256()
	if err != nil {
		return Policy{}, "", err
	}
	return snapshot, digest, nil
}

func validateInputManifest(policy Policy, policySHA256 string, manifest InputManifest) error {
	if manifest.SchemaVersion != InputManifestSchemaVersion {
		return fmt.Errorf("unexpected study input manifest schema %q", manifest.SchemaVersion)
	}
	if manifest.PolicySHA256 != policySHA256 {
		return errors.New("study input manifest policy commitment differs")
	}
	if manifest.Slots == nil {
		return errors.New("study input slots must be a canonical array")
	}
	expectedSlots := 0
	for _, task := range policy.Tasks {
		if task.Status == TaskIncluded {
			expectedSlots += task.Repetitions
		}
	}
	if len(manifest.Slots) != expectedSlots {
		return fmt.Errorf(
			"study input manifest has %d slots; preregistration requires %d",
			len(manifest.Slots),
			expectedSlots,
		)
	}
	seenRoots := make(map[string]struct{}, len(manifest.Slots))
	index := 0
	for _, task := range policy.Tasks {
		if task.Status != TaskIncluded {
			continue
		}
		for repetition := range task.Repetitions {
			slot := manifest.Slots[index]
			if slot.TaskID != task.TaskID || slot.Repetition != repetition {
				return fmt.Errorf(
					"study input slot %d is missing, extra, duplicate, or out of preregistered order",
					index,
				)
			}
			if err := validateInputSlot(policy, slot, seenRoots); err != nil {
				return fmt.Errorf("study input slot %s/%d: %w", slot.TaskID, slot.Repetition, err)
			}
			index++
		}
	}
	return nil
}

func validateInputSlot(
	policy Policy,
	slot InputSlot,
	seenRoots map[string]struct{},
) error {
	if slot.Root == nil {
		if !validBoundedText(slot.NotAttemptedReason, maxInputReasonBytes) {
			return errors.New("slot without a root requires an explicit not-attempted reason")
		}
		if slot.Exclusion != nil {
			return errors.New("not-attempted slot cannot contain an exclusion")
		}
		return nil
	}
	if slot.NotAttemptedReason != "" {
		return errors.New("attempted slot has a not-attempted reason")
	}
	if err := slot.Root.Validate(); err != nil {
		return fmt.Errorf("evidence root: %w", err)
	}
	if slot.Root.MediaType != attestationMediaType {
		return errors.New("evidence root is not a signed tokenbench attestation")
	}
	if slot.Root.Size <= 0 || slot.Root.Size > maxInputAttestationBytes {
		return errors.New("evidence root size is outside the attestation limit")
	}
	if _, duplicate := seenRoots[slot.Root.Digest]; duplicate {
		return errors.New("evidence root is reused by more than one slot")
	}
	seenRoots[slot.Root.Digest] = struct{}{}
	if slot.Exclusion == nil {
		return nil
	}
	if slot.Exclusion.EvidenceRoot != *slot.Root {
		return errors.New("exclusion does not bind the slot evidence root")
	}
	if !validBoundedText(slot.Exclusion.Detail, maxInputReasonBytes) {
		return errors.New("exclusion detail is invalid")
	}
	for _, allowed := range policy.ExclusionReasons {
		if slot.Exclusion.Code == allowed.Code {
			return nil
		}
	}
	return fmt.Errorf("exclusion code %q was not preregistered", slot.Exclusion.Code)
}

// AuthenticatedCorpus is an opaque snapshot of a complete, policy-bound input
// matrix. Its unexported observations and failures can be consumed only by
// later study stages in this package; callers receive defensive summaries.
type AuthenticatedCorpus struct {
	policySHA256 string
	common       *corpusExecutionIdentity
	slots        []authenticatedInputSlot
	valid        bool
}

type authenticatedInputSlot struct {
	root               *cas.ObjectRef
	parent             *cas.ObjectRef
	exclusion          *InputExclusion
	baseline           authenticatedArm
	candidate          authenticatedArm
	taskID             string
	notAttemptedReason string
	bundleKind         evidence.BundleKind
	keyID              string
	trustPolicySHA256  string
	repetition         int
	attempted          bool
}

type authenticatedArm struct {
	observation *harness.Observation
	failure     *tokenbench.AttemptFailure
}

type corpusExecutionIdentity struct {
	harnessIdentity  harness.Identity
	executorIdentity tokenbench.ExecutorIdentity
	requestedModel   string
	resolvedModel    string
	modelRevision    string
	reasoningEffort  string
	decoderSchema    string
}

// AuthenticatedSlotSummary contains no answer text, token counter, trace, or
// mutable corpus authority.
type AuthenticatedSlotSummary struct {
	Root               *cas.ObjectRef
	Parent             *cas.ObjectRef
	Exclusion          *InputExclusion
	TaskID             string
	NotAttemptedReason string
	BundleKind         evidence.BundleKind
	KeyID              string
	TrustPolicySHA256  string
	Repetition         int
	Attempted          bool
	BaselineCompleted  bool
	CandidateCompleted bool
}

// AuthenticatedCorpusIdentity is the common execution identity proved across
// every attempted slot.
type AuthenticatedCorpusIdentity struct {
	HarnessIdentity  harness.Identity
	ExecutorIdentity tokenbench.ExecutorIdentity
	RequestedModel   string
	ResolvedModel    string
	ModelRevision    string
	ReasoningEffort  string
	DecoderSchema    string
}

// LoadAuthenticatedCorpus authenticates every selected root and binds its
// normalized run to the preregistered slot. It never accepts caller-authored
// observations, counters, failures, or quality results.
func LoadAuthenticatedCorpus(
	ctx context.Context,
	store *cas.Store,
	verifier *evidence.Verifier,
	policy Policy,
	manifest InputManifest,
) (AuthenticatedCorpus, error) {
	if ctx == nil {
		return AuthenticatedCorpus{}, errors.New("study input context is required")
	}
	if store == nil {
		return AuthenticatedCorpus{}, errors.New("study input evidence store is required")
	}
	if verifier == nil {
		return AuthenticatedCorpus{}, errors.New("study input attestation verifier is required")
	}
	if !validSHA256(verifier.PolicySHA256()) {
		return AuthenticatedCorpus{}, errors.New("study input attestation verifier is invalid")
	}
	policySnapshot, policySHA256, err := snapshotInputPolicy(policy)
	if err != nil {
		return AuthenticatedCorpus{}, err
	}
	manifest = cloneInputManifest(manifest)
	if err := validateInputManifest(policySnapshot, policySHA256, manifest); err != nil {
		return AuthenticatedCorpus{}, err
	}
	tasks := make(map[string]TaskPolicy, len(policySnapshot.Tasks))
	for _, task := range policySnapshot.Tasks {
		if task.Status == TaskIncluded {
			tasks[task.TaskID] = task
		}
	}
	corpus := AuthenticatedCorpus{
		slots:        make([]authenticatedInputSlot, 0, len(manifest.Slots)),
		policySHA256: policySHA256,
	}
	for _, slot := range manifest.Slots {
		if err := ctx.Err(); err != nil {
			return AuthenticatedCorpus{}, err
		}
		if slot.Root == nil {
			corpus.slots = append(corpus.slots, authenticatedInputSlot{
				taskID:             slot.TaskID,
				repetition:         slot.Repetition,
				notAttemptedReason: slot.NotAttemptedReason,
			})
			continue
		}
		verified, err := evidence.VerifyEvidence(ctx, store, *slot.Root, verifier)
		if err != nil {
			return AuthenticatedCorpus{}, fmt.Errorf(
				"authenticate study input %s/%d: %w",
				slot.TaskID,
				slot.Repetition,
				err,
			)
		}
		if verified.Root != *slot.Root || verified.TrustPolicySHA256 != verifier.PolicySHA256() {
			return AuthenticatedCorpus{}, errors.New("authenticated study input identity differs")
		}
		run, bundleKind, parent, err := normalizeVerifiedRun(verified)
		if err != nil {
			return AuthenticatedCorpus{}, fmt.Errorf(
				"normalize study input %s/%d: %w",
				slot.TaskID,
				slot.Repetition,
				err,
			)
		}
		if err := run.Validate(); err != nil {
			return AuthenticatedCorpus{}, fmt.Errorf(
				"validate authenticated run %s/%d: %w",
				slot.TaskID,
				slot.Repetition,
				err,
			)
		}
		identity, err := validateAuthenticatedRunBinding(tasks[slot.TaskID], slot, run)
		if err != nil {
			return AuthenticatedCorpus{}, fmt.Errorf(
				"bind authenticated run %s/%d: %w",
				slot.TaskID,
				slot.Repetition,
				err,
			)
		}
		if corpus.common == nil {
			common := identity
			corpus.common = &common
		} else if err := validateCommonExecutionIdentity(*corpus.common, identity); err != nil {
			return AuthenticatedCorpus{}, err
		}
		root := *slot.Root
		corpus.slots = append(corpus.slots, authenticatedInputSlot{
			root:              &root,
			parent:            cloneObjectRef(parent),
			exclusion:         cloneInputExclusion(slot.Exclusion),
			baseline:          authenticatedArmFromRun(run.Baseline),
			candidate:         authenticatedArmFromRun(run.Candidate),
			taskID:            slot.TaskID,
			bundleKind:        bundleKind,
			keyID:             verified.KeyID,
			trustPolicySHA256: verified.TrustPolicySHA256,
			repetition:        slot.Repetition,
			attempted:         true,
		})
	}
	corpus.valid = true
	return corpus, nil
}

func validateCommonExecutionIdentity(
	expected, actual corpusExecutionIdentity,
) error {
	if expected != actual {
		return errors.New("attempted study slots do not share one execution identity")
	}
	return nil
}

func validateAuthenticatedRunBinding(
	task TaskPolicy,
	slot InputSlot,
	run tokenbench.Run,
) (corpusExecutionIdentity, error) {
	if !run.Plan.Publishable {
		return corpusExecutionIdentity{}, errors.New("authenticated run plan is not publishable")
	}
	if run.Plan.SuiteID != task.TaskID || slot.TaskID != task.TaskID {
		return corpusExecutionIdentity{}, errors.New("authenticated run suite differs from the task")
	}
	if run.Plan.SuiteSHA256 != task.SuiteSHA256 {
		return corpusExecutionIdentity{}, errors.New("authenticated run suite digest differs from the policy")
	}
	if run.Plan.PromptSHA256 != task.PromptSHA256 {
		return corpusExecutionIdentity{}, errors.New("authenticated run prompt digest differs from the policy")
	}
	if run.Plan.Repetitions != task.Repetitions {
		return corpusExecutionIdentity{}, errors.New("authenticated run repetitions differ from the policy")
	}
	if run.Repetition != slot.Repetition {
		return corpusExecutionIdentity{}, errors.New("authenticated run repetition differs from the slot")
	}
	expectedOrder, err := tokenbench.ScheduledOrder(
		run.Plan.SuiteSHA256,
		run.Plan.Seed,
		run.Repetition,
	)
	if err != nil || run.Order != expectedOrder {
		return corpusExecutionIdentity{}, errors.Join(
			errors.New("authenticated run order differs from its committed schedule"),
			err,
		)
	}
	baseline := run.Plan.Baseline
	candidate := run.Plan.Candidate
	if baseline.HarnessIdentity != candidate.HarnessIdentity ||
		baseline.RequestedModel != candidate.RequestedModel ||
		baseline.Model != candidate.Model ||
		baseline.ModelRevision != candidate.ModelRevision ||
		baseline.ReasoningEffort != candidate.ReasoningEffort {
		return corpusExecutionIdentity{}, errors.New(
			"authenticated run arms do not share one model and harness identity",
		)
	}
	identity := corpusExecutionIdentity{
		harnessIdentity:  baseline.HarnessIdentity,
		executorIdentity: run.ExecutorIdentity,
		requestedModel:   baseline.RequestedModel,
		resolvedModel:    baseline.Model,
		modelRevision:    baseline.ModelRevision,
		reasoningEffort:  baseline.ReasoningEffort,
		decoderSchema:    baseline.HarnessIdentity.DecoderSchema,
	}
	if identity.harnessIdentity.Model != identity.resolvedModel ||
		identity.harnessIdentity.ModelRevision != identity.modelRevision ||
		identity.harnessIdentity.ReasoningEffort != identity.reasoningEffort ||
		identity.decoderSchema == "" {
		return corpusExecutionIdentity{}, errors.New(
			"authenticated run model or decoder identity is internally inconsistent",
		)
	}
	return identity, nil
}

func normalizeVerifiedRun(
	verified evidence.VerifiedEvidence,
) (tokenbench.Run, evidence.BundleKind, *cas.ObjectRef, error) {
	switch verified.BundleKind {
	case evidence.CaptureBundle:
		if verified.Capture == nil || verified.Replay != nil {
			return tokenbench.Run{}, "", nil, errors.New("capture evidence payload is ambiguous")
		}
		run, err := cloneEvidenceRun(verified.Capture.Run)
		return run, evidence.CaptureBundle, nil, err
	case evidence.ReplayBundle:
		if verified.Replay == nil || verified.Capture != nil {
			return tokenbench.Run{}, "", nil, errors.New("replay evidence payload is ambiguous")
		}
		replay := verified.Replay
		run, err := cloneEvidenceRun(replay.Capture.Run)
		if err != nil {
			return tokenbench.Run{}, "", nil, err
		}
		if err := applyReplayObservation(&run.Baseline, replay.Baseline); err != nil {
			return tokenbench.Run{}, "", nil, fmt.Errorf("baseline replay: %w", err)
		}
		if err := applyReplayObservation(&run.Candidate, replay.Candidate); err != nil {
			return tokenbench.Run{}, "", nil, fmt.Errorf("candidate replay: %w", err)
		}
		parent := replay.Manifest.Parent
		return run, evidence.ReplayBundle, &parent, nil
	default:
		return tokenbench.Run{}, "", nil, errors.New("study input is not a capture or replay")
	}
}

func cloneEvidenceRun(run tokenbench.Run) (tokenbench.Run, error) {
	raw, err := json.Marshal(run)
	if err != nil {
		return tokenbench.Run{}, fmt.Errorf("clone authenticated run: %w", err)
	}
	var clone tokenbench.Run
	if err := json.Unmarshal(raw, &clone); err != nil {
		return tokenbench.Run{}, fmt.Errorf("clone authenticated run: %w", err)
	}
	return clone, nil
}

func applyReplayObservation(
	arm *tokenbench.ArmRun,
	observation *harness.Observation,
) error {
	if arm == nil {
		return errors.New("replay arm is missing")
	}
	replayable := arm.Failure == nil || arm.Failure.Kind == "decoder_error" ||
		arm.Failure.Kind == "invalid_observation"
	if !replayable {
		if observation != nil {
			return errors.New("ordinary execution failure has a replay observation")
		}
		return nil
	}
	if observation == nil {
		return errors.New("replayable arm is missing its decoded observation")
	}
	clone := cloneObservation(*observation)
	arm.Failure = nil
	arm.Observation = &clone
	return nil
}

func authenticatedArmFromRun(arm tokenbench.ArmRun) authenticatedArm {
	result := authenticatedArm{}
	if arm.Observation != nil {
		observation := cloneObservation(*arm.Observation)
		result.observation = &observation
	}
	if arm.Failure != nil {
		failure := *arm.Failure
		result.failure = &failure
	}
	return result
}

func cloneObservation(source harness.Observation) harness.Observation {
	return harness.CloneObservation(source)
}

func cloneInputManifest(source InputManifest) InputManifest {
	clone := source
	clone.Slots = make([]InputSlot, len(source.Slots))
	for index, slot := range source.Slots {
		clone.Slots[index] = slot
		clone.Slots[index].Root = cloneObjectRef(slot.Root)
		clone.Slots[index].Exclusion = cloneInputExclusion(slot.Exclusion)
	}
	if source.Slots == nil {
		clone.Slots = nil
	}
	return clone
}

func cloneInputExclusion(source *InputExclusion) *InputExclusion {
	if source == nil {
		return nil
	}
	clone := *source
	return &clone
}

func cloneObjectRef(source *cas.ObjectRef) *cas.ObjectRef {
	if source == nil {
		return nil
	}
	clone := *source
	return &clone
}

// PolicySHA256 returns the exact preregistration identity, or an empty string
// for a zero-value corpus.
func (corpus AuthenticatedCorpus) PolicySHA256() string {
	if !corpus.valid {
		return ""
	}
	return corpus.policySHA256
}

// SlotCount returns the complete number of preregistered included slots.
func (corpus AuthenticatedCorpus) SlotCount() int {
	if !corpus.valid {
		return 0
	}
	return len(corpus.slots)
}

// AttemptedCount returns the number of slots backed by authenticated roots.
func (corpus AuthenticatedCorpus) AttemptedCount() int {
	count := 0
	if corpus.valid {
		for _, slot := range corpus.slots {
			if slot.attempted {
				count++
			}
		}
	}
	return count
}

// NotAttemptedCount returns the number of explicit slots without roots.
func (corpus AuthenticatedCorpus) NotAttemptedCount() int {
	if !corpus.valid {
		return 0
	}
	return len(corpus.slots) - corpus.AttemptedCount()
}

// SlotSummaries returns a defensive, answer-free copy in policy order.
func (corpus AuthenticatedCorpus) SlotSummaries() []AuthenticatedSlotSummary {
	if !corpus.valid {
		return nil
	}
	result := make([]AuthenticatedSlotSummary, len(corpus.slots))
	for index, slot := range corpus.slots {
		result[index] = AuthenticatedSlotSummary{
			Root:               cloneObjectRef(slot.root),
			Parent:             cloneObjectRef(slot.parent),
			Exclusion:          cloneInputExclusion(slot.exclusion),
			TaskID:             slot.taskID,
			NotAttemptedReason: slot.notAttemptedReason,
			BundleKind:         slot.bundleKind,
			KeyID:              slot.keyID,
			TrustPolicySHA256:  slot.trustPolicySHA256,
			Repetition:         slot.repetition,
			Attempted:          slot.attempted,
			BaselineCompleted:  slot.baseline.observation != nil,
			CandidateCompleted: slot.candidate.observation != nil,
		}
	}
	return result
}

// CommonIdentity returns the execution identity shared by every attempted
// slot. The boolean is false when the corpus is invalid or has no attempts.
func (corpus AuthenticatedCorpus) CommonIdentity() (AuthenticatedCorpusIdentity, bool) {
	if !corpus.valid || corpus.common == nil {
		return AuthenticatedCorpusIdentity{}, false
	}
	return AuthenticatedCorpusIdentity{
		HarnessIdentity:  corpus.common.harnessIdentity,
		ExecutorIdentity: corpus.common.executorIdentity,
		RequestedModel:   corpus.common.requestedModel,
		ResolvedModel:    corpus.common.resolvedModel,
		ModelRevision:    corpus.common.modelRevision,
		ReasoningEffort:  corpus.common.reasoningEffort,
		DecoderSchema:    corpus.common.decoderSchema,
	}, true
}
