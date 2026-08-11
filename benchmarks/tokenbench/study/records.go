package study

import (
	"errors"
	"fmt"
	"strings"

	"github.com/scopesifter/scopesifter/benchmarks/tokenbench"
	"github.com/scopesifter/scopesifter/benchmarks/tokenbench/evidence"
	"github.com/scopesifter/scopesifter/benchmarks/tokenbench/harness"
)

const recordsAttestationKeyPrefix = "ed25519-sha256:"

// PairQualityInput is the only caller-supplied data accepted while converting
// an authenticated corpus into analysis records. Entries exist, in corpus
// order, only for nonexcluded attempted pairs with two authenticated answers.
//
// Packet is required with Quality because judged PairedQuality commits to a
// packet, not directly to answer text. CodeQuality is the disjoint objective
// code-family capability; this package intentionally cannot construct one yet.
type PairQualityInput struct {
	Packet               *EvaluationPacket
	Quality              *PairedQuality
	CodeQuality          *ObjectiveCodeQuality
	TaskID               string
	QualityMissingReason string
	Repetition           int
}

// BuildPairRecords derives the complete canonical analysis matrix from an
// opaque authenticated corpus. Callers cannot provide observations, answers,
// failures, or token counters at this boundary.
func BuildPairRecords(
	policy Policy,
	corpus AuthenticatedCorpus,
	qualityInputs []PairQualityInput,
) ([]PairRecord, error) {
	policySnapshot, policySHA256, err := snapshotInputPolicy(policy)
	if err != nil {
		return nil, err
	}
	if err := validateCorpusForRecords(policySnapshot, policySHA256, corpus); err != nil {
		return nil, err
	}
	qualityInputs = clonePairQualityInputs(qualityInputs)
	records := make([]PairRecord, len(corpus.slots))
	qualityIndex := 0
	for index, slot := range corpus.slots {
		record := PairRecord{TaskID: slot.taskID, Repetition: slot.repetition}
		if !slot.attempted {
			record.NotAttemptedReason = slot.notAttemptedReason
		} else {
			record.Attempted = true
			record.Baseline, err = buildAuthenticatedArmObservation(
				slot.baseline,
				*corpus.common,
				true,
			)
			if err != nil {
				return nil, fmt.Errorf("record %s/%d baseline: %w", slot.taskID, slot.repetition, err)
			}
			record.Candidate, err = buildAuthenticatedArmObservation(
				slot.candidate,
				*corpus.common,
				false,
			)
			if err != nil {
				return nil, fmt.Errorf("record %s/%d candidate: %w", slot.taskID, slot.repetition, err)
			}
			if slot.exclusion != nil {
				record.Exclusion = &Exclusion{
					Code:           slot.exclusion.Code,
					Detail:         slot.exclusion.Detail,
					EvidenceSHA256: strings.TrimPrefix(slot.exclusion.EvidenceRoot.Digest, "sha256:"),
				}
			}
			if pairRequiresQuality(record) {
				if qualityIndex >= len(qualityInputs) {
					return nil, fmt.Errorf(
						"record %s/%d requires verified quality or an explicit missing reason",
						slot.taskID,
						slot.repetition,
					)
				}
				if err := applyPairQualityInput(
					policySnapshot,
					policySHA256,
					slot,
					qualityInputs[qualityIndex],
					&record,
				); err != nil {
					return nil, err
				}
				qualityIndex++
			}
		}
		if err := validateRecord(policySnapshot, policySHA256, record); err != nil {
			return nil, fmt.Errorf(
				"derive authenticated record %s/%d: %w",
				record.TaskID,
				record.Repetition,
				err,
			)
		}
		records[index] = record
	}
	if qualityIndex != len(qualityInputs) {
		return nil, fmt.Errorf(
			"quality input matrix has %d entries; authenticated answer matrix requires %d",
			len(qualityInputs),
			qualityIndex,
		)
	}
	return records, nil
}

func validateCorpusForRecords(
	policy Policy,
	policySHA256 string,
	corpus AuthenticatedCorpus,
) error {
	if !corpus.valid {
		return errors.New("authenticated corpus is invalid")
	}
	if corpus.policySHA256 != policySHA256 {
		return errors.New("authenticated corpus policy commitment differs")
	}
	expected := expectedRecordKeys(policy)
	if len(corpus.slots) != len(expected) {
		return fmt.Errorf(
			"authenticated corpus has %d slots; preregistration requires %d",
			len(corpus.slots),
			len(expected),
		)
	}
	manifest := InputManifest{
		SchemaVersion: InputManifestSchemaVersion,
		PolicySHA256:  policySHA256,
		Slots:         make([]InputSlot, len(corpus.slots)),
	}
	attempted := 0
	for index, slot := range corpus.slots {
		if slot.taskID != expected[index].taskID || slot.repetition != expected[index].repetition {
			return fmt.Errorf(
				"authenticated corpus slot %d is missing, extra, duplicate, or out of preregistered order",
				index,
			)
		}
		if slot.attempted != (slot.root != nil) {
			return fmt.Errorf(
				"authenticated corpus slot %s/%d has contradictory attempt authority",
				slot.taskID,
				slot.repetition,
			)
		}
		manifest.Slots[index] = InputSlot{
			TaskID:             slot.taskID,
			Repetition:         slot.repetition,
			Root:               cloneObjectRef(slot.root),
			NotAttemptedReason: slot.notAttemptedReason,
			Exclusion:          cloneInputExclusion(slot.exclusion),
		}
		if !slot.attempted {
			if slot.parent != nil || slot.bundleKind != "" || slot.keyID != "" ||
				slot.trustPolicySHA256 != "" || slot.baseline != (authenticatedArm{}) ||
				slot.candidate != (authenticatedArm{}) {
				return fmt.Errorf(
					"not-attempted corpus slot %s/%d contains authenticated outcomes",
					slot.taskID,
					slot.repetition,
				)
			}
			continue
		}
		attempted++
		if err := validateAttemptedCorpusSlot(slot, corpus.common); err != nil {
			return fmt.Errorf(
				"authenticated corpus slot %s/%d: %w",
				slot.taskID,
				slot.repetition,
				err,
			)
		}
	}
	if err := validateInputManifest(policy, policySHA256, manifest); err != nil {
		return fmt.Errorf("authenticated corpus matrix: %w", err)
	}
	if attempted == 0 {
		if corpus.common != nil {
			return errors.New("all-not-attempted corpus contains an execution identity")
		}
		return nil
	}
	if corpus.common == nil {
		return errors.New("attempted corpus is missing its common execution identity")
	}
	return validateCorpusIdentity(*corpus.common)
}

func validateAttemptedCorpusSlot(
	slot authenticatedInputSlot,
	common *corpusExecutionIdentity,
) error {
	if common == nil {
		return errors.New("attempted slot is missing the corpus execution identity")
	}
	if !validRecordsAttestationKeyID(slot.keyID) {
		return errors.New("attestation key identity is invalid")
	}
	if !validSHA256(slot.trustPolicySHA256) {
		return errors.New("attestation trust-policy identity is invalid")
	}
	switch slot.bundleKind {
	case evidence.CaptureBundle:
		if slot.parent != nil {
			return errors.New("capture slot unexpectedly contains a replay parent")
		}
	case evidence.ReplayBundle:
		if slot.parent == nil {
			return errors.New("replay slot is missing its authenticated parent")
		}
		if err := slot.parent.Validate(); err != nil {
			return fmt.Errorf("replay parent: %w", err)
		}
		if slot.parent.MediaType != attestationMediaType || slot.parent.Size <= 0 ||
			slot.parent.Size > maxInputAttestationBytes {
			return errors.New("replay parent is not a bounded signed attestation")
		}
	default:
		return errors.New("authenticated slot has an invalid evidence bundle kind")
	}
	if err := validateAuthenticatedArmForRecords(slot.baseline, *common, true); err != nil {
		return fmt.Errorf("baseline: %w", err)
	}
	if err := validateAuthenticatedArmForRecords(slot.candidate, *common, false); err != nil {
		return fmt.Errorf("candidate: %w", err)
	}
	return nil
}

func validateCorpusIdentity(identity corpusExecutionIdentity) error {
	if err := harness.ValidateIdentity(identity.harnessIdentity); err != nil {
		return fmt.Errorf("common harness identity: %w", err)
	}
	if identity.harnessIdentity.Model != identity.resolvedModel ||
		identity.harnessIdentity.ModelRevision != identity.modelRevision ||
		identity.harnessIdentity.ReasoningEffort != identity.reasoningEffort ||
		identity.harnessIdentity.DecoderSchema != identity.decoderSchema {
		return errors.New("common execution identity fields are inconsistent")
	}
	if !validBoundedText(identity.requestedModel, 512) ||
		!validBoundedText(identity.resolvedModel, 512) ||
		!validBoundedText(identity.modelRevision, 512) ||
		!validBoundedText(identity.reasoningEffort, 128) ||
		!validBoundedText(identity.decoderSchema, 256) {
		return errors.New("common model identity is invalid")
	}
	if !validBoundedText(identity.executorIdentity.Kind, 128) ||
		!validBoundedText(identity.executorIdentity.Version, 256) ||
		!validSHA256(identity.executorIdentity.ConfigSHA256) {
		return errors.New("common executor identity is invalid")
	}
	return nil
}

func validateAuthenticatedArmForRecords(
	arm authenticatedArm,
	common corpusExecutionIdentity,
	baseline bool,
) error {
	if (arm.observation == nil) == (arm.failure == nil) {
		return errors.New("arm must contain exactly one authenticated observation or failure")
	}
	if arm.failure != nil {
		_, err := authenticatedFailureReason(arm.failure)
		return err
	}
	observation := arm.observation
	if !observation.Completed {
		return errors.New("authenticated observation is not completed")
	}
	if observation.Model != common.resolvedModel {
		return errors.New("authenticated observation model differs from the corpus identity")
	}
	if err := validateAnswer(observation.FinalAnswer); err != nil {
		return fmt.Errorf("authenticated answer: %w", err)
	}
	if observation.ToolCalls == nil || len(observation.ToolCalls) > harness.MaxObservationToolCalls {
		return errors.New("authenticated observation tool-call array is invalid")
	}
	for _, call := range observation.ToolCalls {
		if !validBoundedText(call, 512) {
			return errors.New("authenticated observation tool call is invalid")
		}
		if strings.HasPrefix(call, "scopesifter.") {
			if baseline {
				return errors.New("baseline observation contains a scopesifter tool call")
			}
			switch call {
			case "scopesifter.changed", "scopesifter.find", "scopesifter.inspect", "scopesifter.outline":
			default:
				return errors.New("candidate observation contains an unsupported scopesifter tool call")
			}
		}
	}
	if err := harness.ValidateUsage(observation.Usage); err != nil {
		return fmt.Errorf("authenticated usage: %w", err)
	}
	return nil
}

func buildAuthenticatedArmObservation(
	arm authenticatedArm,
	common corpusExecutionIdentity,
	baseline bool,
) (ArmObservation, error) {
	if err := validateAuthenticatedArmForRecords(arm, common, baseline); err != nil {
		return ArmObservation{}, err
	}
	if arm.failure != nil {
		reason, err := authenticatedFailureReason(arm.failure)
		if err != nil {
			return ArmObservation{}, err
		}
		return ArmObservation{FailureReason: reason}, nil
	}
	usage := harness.CloneUsage(arm.observation.Usage)
	tokens := TokenCounts{
		ProviderTotalTokens:   usage.ProviderTotalTokens,
		InputTokens:           usage.InputTokens,
		CachedInputTokens:     usage.CachedInputTokens,
		CacheWriteInputTokens: usage.CacheWriteInputTokens,
		OutputTokens:          usage.OutputTokens,
		ReasoningTokens:       usage.ReasoningTokens,
	}
	if err := tokens.Validate(); err != nil {
		return ArmObservation{}, fmt.Errorf("authenticated token counters: %w", err)
	}
	return ArmObservation{
		Tokens:        &tokens,
		Completed:     true,
		AnswerPresent: true,
	}, nil
}

func authenticatedFailureReason(failure *tokenbench.AttemptFailure) (string, error) {
	if failure == nil {
		return "", errors.New("authenticated failure is missing")
	}
	if !validBoundedText(failure.Stage, 64) || !validBoundedText(failure.Kind, 64) ||
		!validBoundedText(failure.Message, 1_024) {
		return "", errors.New("authenticated failure classification is invalid")
	}
	reason := failure.Stage + "/" + failure.Kind + ": " + failure.Message
	if !validBoundedText(reason, 2_000) {
		return "", errors.New("derived authenticated failure reason is invalid")
	}
	return reason, nil
}

func pairRequiresQuality(record PairRecord) bool {
	return record.Attempted && record.Exclusion == nil &&
		record.Baseline.Completed && record.Baseline.AnswerPresent &&
		record.Candidate.Completed && record.Candidate.AnswerPresent
}

func applyPairQualityInput(
	policy Policy,
	policySHA256 string,
	slot authenticatedInputSlot,
	input PairQualityInput,
	record *PairRecord,
) error {
	if input.TaskID != slot.taskID || input.Repetition != slot.repetition {
		return fmt.Errorf(
			"quality input for %s/%d is missing, extra, duplicate, or out of authenticated order",
			slot.taskID,
			slot.repetition,
		)
	}
	if input.Quality == nil {
		if input.CodeQuality == nil {
			if input.Packet != nil {
				return fmt.Errorf("quality input %s/%d has a packet without verified judged quality", slot.taskID, slot.repetition)
			}
			if !validBoundedText(input.QualityMissingReason, 2_000) {
				return fmt.Errorf(
					"quality input %s/%d requires family-appropriate verified quality or a bounded explicit missing reason",
					slot.taskID,
					slot.repetition,
				)
			}
			record.QualityMissingReason = input.QualityMissingReason
			return nil
		}
	}
	task, ok := includedTask(policy, slot.taskID)
	if !ok {
		return fmt.Errorf("quality input %s/%d task is not included", slot.taskID, slot.repetition)
	}
	if input.QualityMissingReason != "" {
		return fmt.Errorf("quality input %s/%d has both quality and a missing reason", slot.taskID, slot.repetition)
	}
	if task.TaskFamily == CodeTaskFamily {
		if input.Quality != nil || input.Packet != nil || input.CodeQuality == nil {
			return fmt.Errorf("quality input %s/%d code task must use only an objective code outcome", slot.taskID, slot.repetition)
		}
		quality := *input.CodeQuality
		if err := validateObjectiveCodeQuality(policy, policySHA256, slot.taskID, slot.repetition, quality); err != nil {
			return fmt.Errorf("quality input %s/%d: %w", slot.taskID, slot.repetition, err)
		}
		record.CodeQuality = &quality
		return nil
	}
	if input.CodeQuality != nil {
		return fmt.Errorf("quality input %s/%d review/explain task cannot use an objective code outcome", slot.taskID, slot.repetition)
	}
	if input.Packet == nil {
		return fmt.Errorf("quality input %s/%d is missing its committed packet", slot.taskID, slot.repetition)
	}
	if err := ValidateEvaluationPacket(policy, *input.Packet); err != nil {
		return fmt.Errorf("quality input %s/%d packet: %w", slot.taskID, slot.repetition, err)
	}
	quality := *input.Quality
	if err := validatePairedQuality(policy, policySHA256, slot.taskID, slot.repetition, quality); err != nil {
		return fmt.Errorf("quality input %s/%d: %w", slot.taskID, slot.repetition, err)
	}
	for index, judgment := range quality.Judgments {
		if err := validateEvaluationOutput(*input.Packet, judgment.Output, policy.Quality.ProseEvaluatorIDs[index]); err != nil {
			return fmt.Errorf("quality input %s/%d preserved evaluator output: %w", slot.taskID, slot.repetition, err)
		}
	}
	if quality.PacketCommitment != input.Packet.Commitment {
		return fmt.Errorf("quality input %s/%d differs from its committed packet", slot.taskID, slot.repetition)
	}
	baselineAnswer := slot.baseline.observation.FinalAnswer
	candidateAnswer := slot.candidate.observation.FinalAnswer
	first := input.Packet.Answers[0].Text
	second := input.Packet.Answers[1].Text
	answersMatch := (first == baselineAnswer && second == candidateAnswer) ||
		(first == candidateAnswer && second == baselineAnswer)
	if !answersMatch {
		return fmt.Errorf("quality input %s/%d scored different answers", slot.taskID, slot.repetition)
	}
	record.Quality = &quality
	return nil
}

func clonePairQualityInputs(source []PairQualityInput) []PairQualityInput {
	if source == nil {
		return nil
	}
	clone := make([]PairQualityInput, len(source))
	for index, input := range source {
		clone[index] = input
		if input.Packet != nil {
			packet := clonePacket(*input.Packet)
			clone[index].Packet = &packet
		}
		if input.Quality != nil {
			quality := clonePairedQuality(*input.Quality)
			clone[index].Quality = &quality
		}
		if input.CodeQuality != nil {
			quality := *input.CodeQuality
			clone[index].CodeQuality = &quality
		}
	}
	return clone
}

func validRecordsAttestationKeyID(value string) bool {
	return strings.HasPrefix(value, recordsAttestationKeyPrefix) &&
		validSHA256(strings.TrimPrefix(value, recordsAttestationKeyPrefix))
}
