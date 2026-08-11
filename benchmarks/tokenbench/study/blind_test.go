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
	output := maximumOutput(packet)
	output.Nonce = strings.Repeat("0", 64)
	if _, err := VerifyEvaluation(policy, packet, key, output); err == nil {
		t.Fatal("wrong evaluator nonce accepted")
	}
	output = maximumOutput(packet)
	packet.Answers[0].Text = "Mutated."
	if _, err := VerifyEvaluation(policy, packet, key, output); err == nil {
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
	output := maximumOutput(packet)
	output.Answers[0], output.Answers[1] = output.Answers[1], output.Answers[0]
	if _, err := VerifyEvaluation(policy, packet, key, output); err == nil {
		t.Fatal("label-swapped evaluator output accepted")
	}
	output = maximumOutput(packet)
	output.Answers[0].Items[0], output.Answers[0].Items[1] = output.Answers[0].Items[1], output.Answers[0].Items[0]
	if _, err := VerifyEvaluation(policy, packet, key, output); err == nil {
		t.Fatal("item-swapped evaluator output accepted")
	}
	output = maximumOutput(packet)
	output.Answers[0].Items[0].Score = 1
	if _, err := VerifyEvaluation(policy, packet, key, output); err == nil {
		t.Fatal("partially scored objective fact accepted")
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
	output := maximumOutput(packet)
	for answerIndex := range output.Answers {
		if packet.Answers[answerIndex].Text == "Lower quality response." {
			output.Answers[answerIndex].Items[0].Score = 0
			output.Answers[answerIndex].Items[1].Score = 1
		}
	}
	quality, err := VerifyEvaluation(policy, packet, key, output)
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
	evaluator := evaluatorFunc(func(_ context.Context, input EvaluationPacket) (EvaluationOutput, error) {
		input.Answers[0].Text = "Evaluator mutation."
		return maximumOutput(packet), nil
	})
	if _, err := EvaluateAndVerify(context.Background(), policy, packet, key, evaluator); err != nil {
		t.Fatal(err)
	}
	if packet.Answers[0].Text == "Evaluator mutation." {
		t.Fatal("evaluator mutated caller packet")
	}
}

type evaluatorFunc func(context.Context, EvaluationPacket) (EvaluationOutput, error)

func (function evaluatorFunc) Evaluate(ctx context.Context, packet EvaluationPacket) (EvaluationOutput, error) {
	return function(ctx, packet)
}

func maximumOutput(packet EvaluationPacket) EvaluationOutput {
	output := EvaluationOutput{
		SchemaVersion: EvaluationOutputSchemaVersion,
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
