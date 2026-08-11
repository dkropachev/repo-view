package study

import (
	"context"
	"strings"
	"testing"

	"github.com/dkropachev/repo-view/benchmarks/tokenbench"
	"github.com/dkropachev/repo-view/benchmarks/tokenbench/evidence"
	"github.com/dkropachev/repo-view/benchmarks/tokenbench/harness"
)

func TestBuildPairRecordsDerivesAuthenticatedObservationsDefensively(t *testing.T) {
	policy, seeds := analysisPolicyFixture(t, 2)
	corpus := recordsCorpusFixture(t, policy)
	qualityInputs := recordsQualityInputs(t, policy, seeds, corpus)

	records, err := BuildPairRecords(policy, corpus, qualityInputs)
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != len(corpus.slots) || records[0].TaskID != corpus.slots[0].taskID ||
		records[1].TaskID != corpus.slots[1].taskID {
		t.Fatalf("record order differs from authenticated corpus: %+v", records)
	}
	baselineUsage := corpus.slots[0].baseline.observation.Usage
	baselineTokens := records[0].Baseline.Tokens
	if baselineTokens == nil || baselineTokens.InputTokens != baselineUsage.InputTokens ||
		baselineTokens.CachedInputTokens != baselineUsage.CachedInputTokens ||
		baselineTokens.CacheWriteInputTokens != baselineUsage.CacheWriteInputTokens ||
		baselineTokens.OutputTokens != baselineUsage.OutputTokens ||
		baselineTokens.ReasoningTokens != baselineUsage.ReasoningTokens ||
		baselineTokens.ProviderTotalTokens == nil || *baselineTokens.ProviderTotalTokens != 100 ||
		baselineTokens.ProviderTotalTokens == baselineUsage.ProviderTotalTokens {
		t.Fatalf("authenticated usage was not defensively preserved: %+v %+v", baselineTokens, baselineUsage)
	}
	if records[0].Candidate.Tokens == nil ||
		records[0].Candidate.Tokens.ProviderTotalTokens != nil {
		t.Fatal("omitted provider total was converted into an observed counter")
	}
	if records[1].Candidate.Tokens == nil ||
		records[1].Candidate.Tokens.ProviderTotalTokens == nil ||
		*records[1].Candidate.Tokens.ProviderTotalTokens != 0 {
		t.Fatal("present zero provider total was converted into an omitted counter")
	}
	if records[0].Quality == nil || records[0].Quality == qualityInputs[0].Quality {
		t.Fatal("verified quality was not defensively copied")
	}
	if _, err := Analyze(policy, records, AnalysisSeeds{
		Randomization: seeds.randomization,
		Bootstrap:     seeds.bootstrap,
	}); err != nil {
		t.Fatalf("derived records are not accepted by analysis: %v", err)
	}

	records[0].Baseline.Tokens.InputTokens = 999
	*records[0].Baseline.Tokens.ProviderTotalTokens = 999
	records[0].Quality.CandidateScorePPM = 1
	rebuilt, err := BuildPairRecords(policy, corpus, qualityInputs)
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt[0].Baseline.Tokens.InputTokens != baselineUsage.InputTokens ||
		*rebuilt[0].Baseline.Tokens.ProviderTotalTokens != 100 ||
		rebuilt[0].Quality.CandidateScorePPM != qualityInputs[0].Quality.CandidateScorePPM ||
		*corpus.slots[0].baseline.observation.Usage.ProviderTotalTokens != 100 ||
		qualityInputs[0].Quality.CandidateScorePPM == 1 {
		t.Fatal("returned pointer mutation altered authenticated or verified source state")
	}
}

func TestBuildPairRecordsRejectsAnswerAndQualitySubstitution(t *testing.T) {
	policy, seeds := analysisPolicyFixture(t, 2)
	corpus := recordsCorpusFixture(t, policy)
	valid := recordsQualityInputs(t, policy, seeds, corpus)

	t.Run("different authenticated answers", func(t *testing.T) {
		changed := clonePairQualityInputs(valid)
		changed[0] = recordsQualityInput(
			t,
			policy,
			seeds,
			corpus.slots[0].taskID,
			corpus.slots[0].repetition,
			"caller-authored baseline answer",
			"caller-authored candidate answer",
		)
		if _, err := BuildPairRecords(policy, corpus, changed); err == nil ||
			!strings.Contains(err.Error(), "scored different answers") {
			t.Fatalf("verified quality for substituted answers was accepted: %v", err)
		}
	})

	t.Run("mutated verified quality", func(t *testing.T) {
		changed := clonePairQualityInputs(valid)
		changed[0].Quality.CandidateScorePPM--
		if _, err := BuildPairRecords(policy, corpus, changed); err == nil ||
			!strings.Contains(err.Error(), "differs from the output produced by VerifyEvaluation") {
			t.Fatalf("mutated verified quality was accepted: %v", err)
		}
	})

	t.Run("packet mutation", func(t *testing.T) {
		changed := clonePairQualityInputs(valid)
		changed[0].Packet.Answers[0].Text = "caller-authored answer"
		if _, err := BuildPairRecords(policy, corpus, changed); err == nil {
			t.Fatal("quality packet mutation was accepted")
		}
	})

	t.Run("cross-task quality", func(t *testing.T) {
		changed := clonePairQualityInputs(valid)
		changed[0].Packet = clonePairQualityInputs(valid[1:2])[0].Packet
		changed[0].Quality = clonePairQualityInputs(valid[1:2])[0].Quality
		if _, err := BuildPairRecords(policy, corpus, changed); err == nil {
			t.Fatal("quality from a different authenticated task was accepted")
		}
	})
}

func TestBuildPairRecordsRejectsQualityMatrixCorruption(t *testing.T) {
	policy, seeds := analysisPolicyFixture(t, 2)
	corpus := recordsCorpusFixture(t, policy)
	valid := recordsQualityInputs(t, policy, seeds, corpus)
	tests := map[string]func([]PairQualityInput) []PairQualityInput{
		"missing": func(inputs []PairQualityInput) []PairQualityInput {
			return inputs[:1]
		},
		"extra": func(inputs []PairQualityInput) []PairQualityInput {
			return append(inputs, inputs[1])
		},
		"duplicate": func(inputs []PairQualityInput) []PairQualityInput {
			inputs[1] = inputs[0]
			return inputs
		},
		"reordered": func(inputs []PairQualityInput) []PairQualityInput {
			inputs[0], inputs[1] = inputs[1], inputs[0]
			return inputs
		},
		"wrong repetition": func(inputs []PairQualityInput) []PairQualityInput {
			inputs[0].Repetition++
			return inputs
		},
		"quality and reason": func(inputs []PairQualityInput) []PairQualityInput {
			inputs[0].QualityMissingReason = "Evaluator unavailable."
			return inputs
		},
		"quality without packet": func(inputs []PairQualityInput) []PairQualityInput {
			inputs[0].Packet = nil
			return inputs
		},
		"packet without quality": func(inputs []PairQualityInput) []PairQualityInput {
			inputs[0].Quality = nil
			inputs[0].QualityMissingReason = "Evaluator unavailable."
			return inputs
		},
		"neither quality nor reason": func(inputs []PairQualityInput) []PairQualityInput {
			inputs[0].Packet = nil
			inputs[0].Quality = nil
			return inputs
		},
		"oversized missing reason": func(inputs []PairQualityInput) []PairQualityInput {
			inputs[0].Packet = nil
			inputs[0].Quality = nil
			inputs[0].QualityMissingReason = strings.Repeat("x", 2_001)
			return inputs
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			changed := mutate(clonePairQualityInputs(valid))
			if _, err := BuildPairRecords(policy, corpus, changed); err == nil {
				t.Fatal("corrupt quality matrix was accepted")
			}
		})
	}

	missing := clonePairQualityInputs(valid)
	for index := range missing {
		missing[index].Packet = nil
		missing[index].Quality = nil
		missing[index].QualityMissingReason = "The blinded evaluator was unavailable."
	}
	records, err := BuildPairRecords(policy, corpus, missing)
	if err != nil {
		t.Fatal(err)
	}
	if records[0].Quality != nil || records[0].QualityMissingReason == "" ||
		records[1].Quality != nil || records[1].QualityMissingReason == "" {
		t.Fatalf("explicit quality missingness was not retained: %+v", records)
	}
}

func TestBuildPairRecordsRejectsZeroForgedAndMismatchedCorpus(t *testing.T) {
	policy, seeds := analysisPolicyFixture(t, 2)
	validCorpus := recordsCorpusFixture(t, policy)
	qualityInputs := recordsQualityInputs(t, policy, seeds, validCorpus)
	tests := map[string]func(*AuthenticatedCorpus){
		"zero": func(corpus *AuthenticatedCorpus) {
			*corpus = AuthenticatedCorpus{}
		},
		"forged valid flag": func(corpus *AuthenticatedCorpus) {
			*corpus = AuthenticatedCorpus{valid: true, policySHA256: corpus.policySHA256}
		},
		"wrong policy sha": func(corpus *AuthenticatedCorpus) {
			corpus.policySHA256 = strings.Repeat("0", 64)
		},
		"missing slot": func(corpus *AuthenticatedCorpus) {
			corpus.slots = corpus.slots[:1]
		},
		"extra slot": func(corpus *AuthenticatedCorpus) {
			corpus.slots = append(corpus.slots, corpus.slots[0])
		},
		"reordered slots": func(corpus *AuthenticatedCorpus) {
			corpus.slots[0], corpus.slots[1] = corpus.slots[1], corpus.slots[0]
		},
		"duplicate slot": func(corpus *AuthenticatedCorpus) {
			corpus.slots[1] = corpus.slots[0]
		},
		"wrong repetition": func(corpus *AuthenticatedCorpus) {
			corpus.slots[0].repetition++
		},
		"contradictory attempt": func(corpus *AuthenticatedCorpus) {
			corpus.slots[0].attempted = false
		},
		"reused evidence root": func(corpus *AuthenticatedCorpus) {
			root := *corpus.slots[0].root
			corpus.slots[1].root = &root
		},
		"missing common identity": func(corpus *AuthenticatedCorpus) {
			corpus.common = nil
		},
		"mutated common identity": func(corpus *AuthenticatedCorpus) {
			corpus.common.resolvedModel = "different-model"
		},
		"mutated authenticated counter": func(corpus *AuthenticatedCorpus) {
			corpus.slots[0].baseline.observation.Usage.InputTokens = -1
		},
		"missing arm authority": func(corpus *AuthenticatedCorpus) {
			corpus.slots[0].baseline = authenticatedArm{}
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			corpus := recordsCorpusFixture(t, policy)
			mutate(&corpus)
			if _, err := BuildPairRecords(policy, corpus, qualityInputs); err == nil {
				t.Fatal("zero, forged, or corrupt corpus was accepted")
			}
		})
	}

	t.Run("policy identity mismatch", func(t *testing.T) {
		changed := policy
		changed.StudyID = "different-study"
		if _, err := BuildPairRecords(changed, validCorpus, qualityInputs); err == nil {
			t.Fatal("corpus was reused under a different policy")
		}
	})
	t.Run("policy matrix mismatch", func(t *testing.T) {
		changed := policy
		changed.Tasks = append([]TaskPolicy(nil), policy.Tasks...)
		changed.Tasks[0].Repetitions++
		if _, err := BuildPairRecords(changed, validCorpus, qualityInputs); err == nil {
			t.Fatal("corpus was reused under a different policy matrix")
		}
	})
}

func TestBuildPairRecordsRetainsExclusionNotAttemptedAndFailure(t *testing.T) {
	policy, seeds := analysisPolicyFixture(t, 2)
	corpus := recordsCorpusFixture(t, policy)
	exclusionRoot := *corpus.slots[0].root
	corpus.slots[0].exclusion = &InputExclusion{
		EvidenceRoot: exclusionRoot,
		Code:         "integrity_failure",
		Detail:       "The preregistered integrity condition applies.",
	}
	recordsSetNotAttempted(&corpus.slots[1], "The preregistered slot was not started.")

	records, err := BuildPairRecords(policy, corpus, nil)
	if err != nil {
		t.Fatal(err)
	}
	if records[0].Exclusion == nil || records[0].Exclusion.Code != corpus.slots[0].exclusion.Code ||
		records[0].Exclusion.Detail != corpus.slots[0].exclusion.Detail ||
		records[0].Exclusion.EvidenceSHA256 != strings.TrimPrefix(exclusionRoot.Digest, "sha256:") ||
		records[0].Quality != nil || records[0].QualityMissingReason != "" {
		t.Fatalf("preregistered exclusion was not converted exactly: %+v", records[0])
	}
	if records[1].Attempted || records[1].NotAttemptedReason != corpus.slots[1].notAttemptedReason ||
		records[1].Baseline != (ArmObservation{}) || records[1].Candidate != (ArmObservation{}) {
		t.Fatalf("explicit not-attempted slot was not retained: %+v", records[1])
	}
	records[0].Exclusion.Detail = "caller mutation"
	rebuilt, err := BuildPairRecords(policy, corpus, nil)
	if err != nil {
		t.Fatal(err)
	}
	if rebuilt[0].Exclusion.Detail != corpus.slots[0].exclusion.Detail {
		t.Fatal("returned exclusion mutation altered the authenticated corpus")
	}

	failureCorpus := recordsCorpusFixture(t, policy)
	failureCorpus.slots[0].candidate.observation = nil
	failureCorpus.slots[0].candidate.failure = &tokenbench.AttemptFailure{
		Stage: "execute", Kind: "timeout", Message: "approved process timed out",
	}
	failureQuality := recordsQualityInputs(t, policy, seeds, failureCorpus)
	failureRecords, err := BuildPairRecords(policy, failureCorpus, failureQuality)
	if err != nil {
		t.Fatal(err)
	}
	if failureRecords[0].Candidate.Completed || failureRecords[0].Candidate.Tokens != nil ||
		failureRecords[0].Candidate.FailureReason != "execute/timeout: approved process timed out" ||
		failureRecords[0].Quality != nil || len(failureQuality) != 1 {
		t.Fatalf("authenticated arm failure was not retained: %+v", failureRecords[0])
	}

	manifest := inputManifestFixture(t, policy)
	for index := range manifest.Slots {
		manifest.Slots[index].Root = nil
		manifest.Slots[index].NotAttemptedReason =
			"The complete preregistered slot was explicitly not attempted."
	}
	allNotAttempted, err := LoadAuthenticatedCorpus(
		context.Background(),
		inputTestStore(t),
		inputTestVerifier(t),
		policy,
		manifest,
	)
	if err != nil {
		t.Fatal(err)
	}
	allRecords, err := BuildPairRecords(policy, allNotAttempted, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(allRecords) != allNotAttempted.SlotCount() || allRecords[0].Attempted ||
		allRecords[1].Attempted || allRecords[0].NotAttemptedReason == "" ||
		allRecords[1].NotAttemptedReason == "" {
		t.Fatalf("complete all-not-attempted corpus was not retained: %+v", allRecords)
	}
}

func recordsCorpusFixture(t *testing.T, policy Policy) AuthenticatedCorpus {
	t.Helper()
	policySHA256, err := policy.SHA256()
	if err != nil {
		t.Fatal(err)
	}
	manifest := inputManifestFixture(t, policy)
	var common corpusExecutionIdentity
	for _, task := range policy.Tasks {
		if task.Status != TaskIncluded {
			continue
		}
		run := bindingRunFixture(t, task, 0)
		common, err = validateAuthenticatedRunBinding(task, InputSlot{
			TaskID: task.TaskID, Repetition: 0,
		}, run)
		if err != nil {
			t.Fatal(err)
		}
		break
	}
	corpus := AuthenticatedCorpus{
		policySHA256: policySHA256,
		common:       &common,
		slots:        make([]authenticatedInputSlot, len(manifest.Slots)),
		valid:        true,
	}
	for index, inputSlot := range manifest.Slots {
		baselineTotal := int64(100 + index)
		baseline := recordsObservation(
			"Authenticated baseline answer for "+inputSlot.TaskID+".",
			[]string{"provider.search"},
			harness.Usage{
				ProviderTotalTokens: &baselineTotal,
				InputTokens:         int64(10 + index),
				CachedInputTokens:   2,
				OutputTokens:        4,
				ReasoningTokens:     1,
			},
		)
		candidateUsage := harness.Usage{
			InputTokens: int64(8 + index), CachedInputTokens: 1,
			CacheWriteInputTokens: 1, OutputTokens: 3, ReasoningTokens: 1,
		}
		if index%2 == 1 {
			candidateTotal := int64(0)
			candidateUsage.ProviderTotalTokens = &candidateTotal
		}
		candidate := recordsObservation(
			"Authenticated candidate answer for "+inputSlot.TaskID+".",
			[]string{"repo_view.find"},
			candidateUsage,
		)
		root := *inputSlot.Root
		corpus.slots[index] = authenticatedInputSlot{
			root:              &root,
			baseline:          authenticatedArm{observation: &baseline},
			candidate:         authenticatedArm{observation: &candidate},
			taskID:            inputSlot.TaskID,
			bundleKind:        evidence.CaptureBundle,
			keyID:             recordsAttestationKeyPrefix + strings.Repeat("d", 64),
			trustPolicySHA256: strings.Repeat("e", 64),
			repetition:        inputSlot.Repetition,
			attempted:         true,
		}
	}
	return corpus
}

func recordsObservation(answer string, calls []string, usage harness.Usage) harness.Observation {
	return harness.Observation{
		FinalAnswer: answer,
		Model:       "pinned-model",
		ToolCalls:   append([]string(nil), calls...),
		Usage:       harness.CloneUsage(usage),
		Completed:   true,
	}
}

func recordsQualityInputs(
	t *testing.T,
	policy Policy,
	seeds testSeedSet,
	corpus AuthenticatedCorpus,
) []PairQualityInput {
	t.Helper()
	inputs := make([]PairQualityInput, 0, len(corpus.slots))
	for _, slot := range corpus.slots {
		if !slot.attempted || slot.exclusion != nil || slot.baseline.observation == nil ||
			slot.candidate.observation == nil {
			continue
		}
		inputs = append(inputs, recordsQualityInput(
			t,
			policy,
			seeds,
			slot.taskID,
			slot.repetition,
			slot.baseline.observation.FinalAnswer,
			slot.candidate.observation.FinalAnswer,
		))
	}
	return inputs
}

func recordsQualityInput(
	t *testing.T,
	policy Policy,
	seeds testSeedSet,
	taskID string,
	repetition int,
	baselineAnswer string,
	candidateAnswer string,
) PairQualityInput {
	t.Helper()
	packet, key, err := BlindPair(policy, BlindPairRequest{
		TaskID:          taskID,
		BaselineAnswer:  baselineAnswer,
		CandidateAnswer: candidateAnswer,
		Repetition:      repetition,
	}, seeds.blinding)
	if err != nil {
		t.Fatal(err)
	}
	quality, err := VerifyEvaluation(policy, packet, key, recordsMaximumOutput(packet))
	if err != nil {
		t.Fatal(err)
	}
	return PairQualityInput{
		Packet:     &packet,
		Quality:    &quality,
		TaskID:     taskID,
		Repetition: repetition,
	}
}

func recordsMaximumOutput(packet EvaluationPacket) EvaluationOutput {
	output := EvaluationOutput{
		SchemaVersion: EvaluationOutputSchemaVersion,
		Nonce:         packet.Nonce,
		Commitment:    packet.Commitment,
		Answers:       make([]AnswerEvaluation, len(packet.Answers)),
	}
	for answerIndex, answer := range packet.Answers {
		output.Answers[answerIndex] = AnswerEvaluation{
			Label: answer.Label,
			Items: make([]ItemEvaluation, len(packet.Criteria)),
		}
		for itemIndex, criterion := range packet.Criteria {
			output.Answers[answerIndex].Items[itemIndex] = ItemEvaluation{
				ItemID:    criterion.ItemID,
				Rationale: "The response satisfies the stated requirement.",
				Score:     criterion.MaximumPoints,
			}
		}
	}
	return output
}

func recordsSetNotAttempted(slot *authenticatedInputSlot, reason string) {
	*slot = authenticatedInputSlot{
		taskID:             slot.taskID,
		repetition:         slot.repetition,
		notAttemptedReason: reason,
	}
}
