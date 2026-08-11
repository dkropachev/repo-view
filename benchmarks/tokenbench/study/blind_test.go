package study

import (
	"context"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

func TestBlindPacketDeterministicAndEvaluatorSafe(t *testing.T) {
	policy, seeds := policyFixture(t, 2)
	request := BlindPairRequest{
		TaskID: "task-01", Repetition: 0,
		BaselineAnswer: "The first response.", CandidateAnswer: "The second response.",
	}
	first, firstKey, err := BlindPair(policy, request, seeds.blinding)
	if err != nil {
		t.Fatal(err)
	}
	second, secondKey, err := BlindPair(policy, request, seeds.blinding)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(first, second) || !reflect.DeepEqual(firstKey, secondKey) {
		t.Fatal("equal committed inputs produced nondeterministic blinding")
	}
	raw, err := EncodeEvaluationPacket(policy, first)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"arm", "order", "tool_calls", "input_tokens", "output_tokens",
		"baseline", "candidate", "pairing_key", "repetition",
	} {
		if _, exists := object[forbidden]; exists {
			t.Fatalf("blind packet exposed %q", forbidden)
		}
	}
	changed := append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,"treatment":"candidate"}`)...)
	if _, err := DecodeEvaluationPacket(policy, changed); err == nil {
		t.Fatal("unblinding metadata was accepted")
	}

	varied := policy
	varied.Tasks = append([]TaskPolicy(nil), policy.Tasks...)
	varied.Tasks[0].Repetitions = 64
	firstBaseline, firstCandidate := 0, 0
	for index := range 64 {
		packet, _, err := BlindPair(varied, BlindPairRequest{
			TaskID: "task-01", Repetition: index,
			BaselineAnswer: "The first response.", CandidateAnswer: "The second response.",
		}, seeds.blinding)
		if err != nil {
			t.Fatal(err)
		}
		if packet.Answers[0].Text == "The first response." {
			firstBaseline++
		} else {
			firstCandidate++
		}
	}
	if firstBaseline != firstCandidate {
		t.Fatalf(
			"committed assignment is not exactly counterbalanced: baseline=%d candidate=%d",
			firstBaseline,
			firstCandidate,
		)
	}
}

func TestBlindPacketRejectsWrongSeedAndPayloadMutation(t *testing.T) {
	policy, seeds := policyFixture(t, 2)
	request := BlindPairRequest{TaskID: "task-01", Repetition: 0, BaselineAnswer: "One.", CandidateAnswer: "Two."}
	wrong := append([]byte(nil), seeds.blinding...)
	wrong[0] ^= 0xff
	if _, _, err := BlindPair(policy, request, wrong); err == nil {
		t.Fatal("wrong blinding seed accepted")
	}
	packet, key, err := BlindPair(policy, request, seeds.blinding)
	if err != nil {
		t.Fatal(err)
	}
	outputs := maximumOutputs(packet)
	outputs[0].Nonce = strings.Repeat("0", 64)
	if _, err := VerifyEvaluations(policy, packet, key, outputs); err == nil {
		t.Fatal("wrong evaluator nonce accepted")
	}
	outputs = maximumOutputs(packet)
	packet.Answers[0].Text = "Mutated."
	if _, err := VerifyEvaluations(policy, packet, key, outputs); err == nil {
		t.Fatal("mutated blind payload accepted")
	}
}

func TestEvaluatorOutputRejectsLabelAndItemSwaps(t *testing.T) {
	policy, seeds := policyFixture(t, 2)
	packet, key, err := BlindPair(policy, BlindPairRequest{
		TaskID: "task-01", Repetition: 0, BaselineAnswer: "One.", CandidateAnswer: "Two.",
	}, seeds.blinding)
	if err != nil {
		t.Fatal(err)
	}
	outputs := maximumOutputs(packet)
	outputs[0].Answers[0], outputs[0].Answers[1] = outputs[0].Answers[1], outputs[0].Answers[0]
	if _, err := VerifyEvaluations(policy, packet, key, outputs); err == nil {
		t.Fatal("label-swapped evaluator output accepted")
	}
	outputs = maximumOutputs(packet)
	outputs[0].Answers[0].Items[0], outputs[0].Answers[0].Items[1] = outputs[0].Answers[0].Items[1], outputs[0].Answers[0].Items[0]
	if _, err := VerifyEvaluations(policy, packet, key, outputs); err == nil {
		t.Fatal("item-swapped evaluator output accepted")
	}
	outputs = maximumOutputs(packet)
	outputs[0].Answers[0].Items[0].Score = 1
	if _, err := VerifyEvaluations(policy, packet, key, outputs); err == nil {
		t.Fatal("partially scored objective fact accepted")
	}
}

func TestEvaluatorOutputV2IsStrictCanonicalAndIdentityBound(t *testing.T) {
	policy, seeds := policyFixture(t, 2)
	packet, _, err := BlindPair(policy, BlindPairRequest{
		TaskID: "task-01", Repetition: 0, BaselineAnswer: "One.", CandidateAnswer: "Two.",
	}, seeds.blinding)
	if err != nil {
		t.Fatal(err)
	}
	output := maximumOutput(packet, "judge-alpha")
	raw, err := EncodeEvaluationOutput(packet, output)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeEvaluationOutput(packet, raw)
	if err != nil || !reflect.DeepEqual(decoded, output) {
		t.Fatalf("canonical evaluator output did not round trip: %+v %v", decoded, err)
	}
	for name, changed := range map[string][]byte{
		"missing evaluator ID": []byte(strings.Replace(string(raw), `"evaluator_id":"judge-alpha",`, "", 1)),
		"noncanonical ID":      []byte(strings.Replace(string(raw), "judge-alpha", "Judge Alpha", 1)),
		"trailing newline":     append(append([]byte(nil), raw...), '\n'),
		"unknown field":        append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,"extra":true}`)...),
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeEvaluationOutput(packet, changed); err == nil {
				t.Fatal("noncanonical evaluator output accepted")
			}
		})
	}
}

func TestEvaluationMapsRandomLabelsBackToTreatments(t *testing.T) {
	policy, seeds := policyFixture(t, 2)
	packet, key, err := BlindPair(policy, BlindPairRequest{
		TaskID: "task-01", Repetition: 0,
		BaselineAnswer: "Lower quality response.", CandidateAnswer: "Higher quality response.",
	}, seeds.blinding)
	if err != nil {
		t.Fatal(err)
	}
	outputs := maximumOutputs(packet)
	for outputIndex := range outputs {
		for answerIndex := range outputs[outputIndex].Answers {
			if packet.Answers[answerIndex].Text == "Lower quality response." {
				outputs[outputIndex].Answers[answerIndex].Items[0].Score = 0
				outputs[outputIndex].Answers[answerIndex].Items[1].Score = 1
			}
		}
	}
	quality, err := VerifyEvaluations(policy, packet, key, outputs)
	if err != nil {
		t.Fatal(err)
	}
	if quality.BaselineScorePPM != 200_000 || quality.CandidateScorePPM != 1_000_000 ||
		quality.BaselinePass || !quality.CandidatePass {
		t.Fatalf("random label mapping produced wrong treatment scores: %+v", quality)
	}
}

func TestEvaluateAndVerifyPassesOnlyDefensivePacket(t *testing.T) {
	policy, seeds := policyFixture(t, 2)
	packet, key, err := BlindPair(policy, BlindPairRequest{
		TaskID: "task-01", Repetition: 0, BaselineAnswer: "One.", CandidateAnswer: "Two.",
	}, seeds.blinding)
	if err != nil {
		t.Fatal(err)
	}
	evaluator := func(id string) Evaluator {
		return evaluatorFunc{id: id, evaluate: func(_ context.Context, input EvaluationPacket) (EvaluationOutput, error) {
			input.Answers[0].Text = "Evaluator mutation."
			return maximumOutput(packet, id), nil
		}}
	}
	if _, err := EvaluateAndVerify(context.Background(), policy, packet, key, []Evaluator{
		evaluator("judge-alpha"), evaluator("judge-beta"),
	}); err != nil {
		t.Fatal(err)
	}
	if packet.Answers[0].Text == "Evaluator mutation." {
		t.Fatal("evaluator mutated caller packet")
	}
}

func TestTwoJudgmentsRequireCompleteDistinctPreregisteredIdentities(t *testing.T) {
	policy, seeds := policyFixture(t, 2)
	packet, key, err := BlindPair(policy, BlindPairRequest{
		TaskID: "task-01", Repetition: 0, BaselineAnswer: "One.", CandidateAnswer: "Two.",
	}, seeds.blinding)
	if err != nil {
		t.Fatal(err)
	}
	valid := maximumOutputs(packet)
	for name, outputs := range map[string][]EvaluationOutput{
		"missing": valid[:1],
		"extra":   append(append([]EvaluationOutput(nil), valid...), maximumOutput(packet, "judge-gamma")),
		"duplicate": {
			maximumOutput(packet, "judge-alpha"), maximumOutput(packet, "judge-alpha"),
		},
		"reordered": {valid[1], valid[0]},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := VerifyEvaluations(policy, packet, key, outputs); err == nil {
				t.Fatal("incomplete or identity-substituted judgment matrix accepted")
			}
		})
	}
}

func TestTwoJudgmentsUsePreregisteredDeterministicMeanAndPreserveLineage(t *testing.T) {
	policy, seeds := policyFixture(t, 2)
	packet, key, err := BlindPair(policy, BlindPairRequest{
		TaskID: "task-01", Repetition: 0, BaselineAnswer: "One.", CandidateAnswer: "Two.",
	}, seeds.blinding)
	if err != nil {
		t.Fatal(err)
	}
	outputs := maximumOutputs(packet)
	for answerIndex := range outputs[1].Answers {
		for itemIndex := range outputs[1].Answers[answerIndex].Items {
			outputs[1].Answers[answerIndex].Items[itemIndex].Score = 0
			outputs[1].Answers[answerIndex].Items[itemIndex].Rationale = "The response does not satisfy this requirement."
		}
	}
	quality, err := VerifyEvaluations(policy, packet, key, outputs)
	if err != nil {
		t.Fatal(err)
	}
	if quality.Aggregation != ArithmeticMeanAggregation || quality.BaselineScorePPM != 500_000 ||
		quality.CandidateScorePPM != 500_000 || quality.BaselinePass || quality.CandidatePass {
		t.Fatalf("unexpected deterministic aggregate: %+v", quality)
	}
	if len(quality.Judgments) != 2 || quality.Judgments[0].EvaluatorID != "judge-alpha" ||
		quality.Judgments[1].EvaluatorID != "judge-beta" || quality.Judgments[0].OutputSHA256 == quality.Judgments[1].OutputSHA256 {
		t.Fatalf("individual evaluator identity or output lineage was not preserved: %+v", quality.Judgments)
	}
	repeated, err := VerifyEvaluations(policy, packet, key, outputs)
	if err != nil || !reflect.DeepEqual(quality, repeated) {
		t.Fatalf("equal outputs produced nondeterministic aggregation: %+v %v", repeated, err)
	}
}

func TestBlindPairRejectsCodeFamily(t *testing.T) {
	policy, seeds := policyFixture(t, 2)
	policy.Tasks[0].TaskFamily = CodeTaskFamily
	policy.Tasks[0].Facts = []FactItem{}
	policy.Tasks[0].Rubric = []RubricItem{}
	if _, _, err := BlindPair(policy, BlindPairRequest{
		TaskID: "task-01", Repetition: 0, BaselineAnswer: "One.", CandidateAnswer: "Two.",
	}, seeds.blinding); err == nil || !strings.Contains(err.Error(), "objective code outcomes") {
		t.Fatalf("code task entered prose judge boundary: %v", err)
	}
}

type evaluatorFunc struct {
	id       string
	evaluate func(context.Context, EvaluationPacket) (EvaluationOutput, error)
}

func (function evaluatorFunc) EvaluatorID() string { return function.id }

func (function evaluatorFunc) Evaluate(ctx context.Context, packet EvaluationPacket) (EvaluationOutput, error) {
	return function.evaluate(ctx, packet)
}

func maximumOutput(packet EvaluationPacket, evaluatorID ...string) EvaluationOutput {
	id := "judge-alpha"
	if len(evaluatorID) == 1 {
		id = evaluatorID[0]
	}
	output := EvaluationOutput{
		SchemaVersion: EvaluationOutputSchemaVersion,
		EvaluatorID:   id,
		Nonce:         packet.Nonce, Commitment: packet.Commitment,
		Answers: make([]AnswerEvaluation, len(packet.Answers)),
	}
	for answerIndex, answer := range packet.Answers {
		output.Answers[answerIndex] = AnswerEvaluation{Label: answer.Label, Items: make([]ItemEvaluation, len(packet.Criteria))}
		for itemIndex, criterion := range packet.Criteria {
			output.Answers[answerIndex].Items[itemIndex] = ItemEvaluation{
				ItemID: criterion.ItemID, Score: criterion.MaximumPoints,
				Rationale: "The response satisfies the stated requirement.",
			}
		}
	}
	return output
}

func maximumOutputs(packet EvaluationPacket) []EvaluationOutput {
	return []EvaluationOutput{
		maximumOutput(packet, "judge-alpha"),
		maximumOutput(packet, "judge-beta"),
	}
}
