package study

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"reflect"
	"strconv"
	"unicode/utf8"
)

const (
	EvaluationPacketSchemaVersion     = "tokenbench.blind-evaluation/v3"
	EvaluationOutputSchemaVersion     = "tokenbench.evaluator-output/v3"
	VerifiedQualitySchemaVersion      = "tokenbench.verified-quality/v3"
	ObjectiveCodeQualitySchemaVersion = "tokenbench.objective-code-quality/v1"

	maxAnswerBytes    = 16 << 20
	maxRationaleBytes = 16_000
)

type CriterionKind string

const (
	FactCriterion   CriterionKind = "fact"
	RubricCriterion CriterionKind = "rubric"
)

// BlindPairRequest contains the private association between final answers and
// treatments. Repetition is checked against the preregistration and is not
// exposed in EvaluationPacket.
type BlindPairRequest struct {
	TaskID          string
	BaselineAnswer  string
	CandidateAnswer string
	Repetition      int
}

// EvaluationPacket is the only value passed to an Evaluator. It contains final
// answer text and preregistered criteria, but no treatment, execution order,
// tool trace, token count, repetition, failure, or pair identifier metadata.
type EvaluationPacket struct {
	SchemaVersion string                `json:"schema_version"`
	PolicySHA256  string                `json:"policy_sha256"`
	Nonce         string                `json:"nonce"`
	Commitment    string                `json:"commitment"`
	TaskID        string                `json:"task_id"`
	Criteria      []EvaluationCriterion `json:"criteria"`
	Answers       []BlindAnswer         `json:"answers"`
}

type EvaluationCriterion struct {
	ItemID        string        `json:"item_id"`
	Kind          CriterionKind `json:"kind"`
	Requirement   string        `json:"requirement"`
	Expected      string        `json:"expected"`
	MaximumPoints int64         `json:"maximum_points"`
}

type BlindAnswer struct {
	Label string `json:"label"`
	Text  string `json:"text"`
}

// EvaluationOutput echoes the packet nonce and commitment. Results and items
// must retain packet order; accepting arbitrary reordering would make label
// transposition undetectable at this boundary.
type EvaluationOutput struct {
	SchemaVersion string             `json:"schema_version"`
	EvaluatorID   string             `json:"evaluator_id"`
	Nonce         string             `json:"nonce"`
	Commitment    string             `json:"commitment"`
	Answers       []AnswerEvaluation `json:"answers"`
}

type AnswerEvaluation struct {
	Label string           `json:"label"`
	Items []ItemEvaluation `json:"items"`
}

type ItemEvaluation struct {
	ItemID    string `json:"item_id"`
	Rationale string `json:"rationale"`
	Score     int64  `json:"score"`
}

// Evaluator is intentionally narrow. An implementation receives no run or
// observation object from which it could inspect treatment or usage metadata.
type Evaluator interface {
	EvaluatorID() string
	Evaluate(context.Context, EvaluationPacket) (EvaluationOutput, error)
}

// EvaluationKey is an opaque, in-memory association created with a packet. It
// is not JSON serializable and must never be given to an Evaluator.
type EvaluationKey struct {
	policySHA256   string
	taskID         string
	nonce          string
	commitment     string
	baselineLabel  string
	candidateLabel string
	repetition     int
	valid          bool
}

// PairedQuality is produced only by VerifyEvaluations. The private seal binds
// every exported field so mutation or reuse for another repetition is rejected.
//
//nolint:govet,nolintlint // Field order is the byte-level canonical verified-quality wire order.
type PairedQuality struct {
	SchemaVersion     string                 `json:"schema_version"`
	PolicySHA256      string                 `json:"policy_sha256"`
	TaskID            string                 `json:"task_id"`
	TaskFamily        TaskFamily             `json:"task_family"`
	Repetition        int                    `json:"repetition"`
	PacketCommitment  string                 `json:"packet_commitment"`
	Aggregation       ProseAggregationMethod `json:"aggregation"`
	Judgments         []IndividualJudgment   `json:"judgments"`
	BaselineScorePPM  int64                  `json:"baseline_score_ppm"`
	CandidateScorePPM int64                  `json:"candidate_score_ppm"`
	BaselinePass      bool                   `json:"baseline_pass"`
	CandidatePass     bool                   `json:"candidate_pass"`
	seal              [sha256.Size]byte
}

// IndividualJudgment preserves each evaluator's complete canonical output,
// its digest, and treatment-mapped scores. The two entries remain in
// preregistered evaluator-ID order.
type IndividualJudgment struct {
	EvaluatorID       string           `json:"evaluator_id"`
	OutputSHA256      string           `json:"output_sha256"`
	Output            EvaluationOutput `json:"output"`
	Items             []JudgedItem     `json:"items"`
	BaselineScorePPM  int64            `json:"baseline_score_ppm"`
	CandidateScorePPM int64            `json:"candidate_score_ppm"`
	BaselinePass      bool             `json:"baseline_pass"`
	CandidatePass     bool             `json:"candidate_pass"`
}

// JudgedItem discloses exact inter-rater agreement after unblinding without
// discarding the full evaluator output committed by OutputSHA256.
type JudgedItem struct {
	ItemID         string `json:"item_id"`
	BaselineScore  int64  `json:"baseline_score"`
	CandidateScore int64  `json:"candidate_score"`
}

// ObjectiveCodeQuality reserves the authenticated family-specific boundary for
// code-task patches and hidden-test outcomes. This milestone intentionally has
// no exported constructor: prose evaluators cannot create this capability, and
// a later objective code runner must bind both outcome digests before sealing
// it.
//
//nolint:govet,nolintlint // Field order is the canonical future code-quality wire order.
type ObjectiveCodeQuality struct {
	SchemaVersion          string     `json:"schema_version"`
	PolicySHA256           string     `json:"policy_sha256"`
	TaskID                 string     `json:"task_id"`
	TaskFamily             TaskFamily `json:"task_family"`
	Repetition             int        `json:"repetition"`
	BaselineOutcomeSHA256  string     `json:"baseline_outcome_sha256"`
	CandidateOutcomeSHA256 string     `json:"candidate_outcome_sha256"`
	BaselineScorePPM       int64      `json:"baseline_score_ppm"`
	CandidateScorePPM      int64      `json:"candidate_score_ppm"`
	BaselinePass           bool       `json:"baseline_pass"`
	CandidatePass          bool       `json:"candidate_pass"`
	seal                   [sha256.Size]byte
}

// BlindPair checks the committed secret seed, derives a deterministic nonce
// and assignment, and returns an evaluator-safe packet plus its private key.
func BlindPair(policy Policy, request BlindPairRequest, seed []byte) (EvaluationPacket, EvaluationKey, error) {
	if err := policy.Validate(); err != nil {
		return EvaluationPacket{}, EvaluationKey{}, err
	}
	if err := verifySeed(BlindingSeedPurpose, seed, policy.Seeds.BlindingSHA256); err != nil {
		return EvaluationPacket{}, EvaluationKey{}, err
	}
	if err := validateAnswer(request.BaselineAnswer); err != nil {
		return EvaluationPacket{}, EvaluationKey{}, fmt.Errorf("baseline answer: %w", err)
	}
	if err := validateAnswer(request.CandidateAnswer); err != nil {
		return EvaluationPacket{}, EvaluationKey{}, fmt.Errorf("candidate answer: %w", err)
	}
	task, ok := includedTask(policy, request.TaskID)
	if !ok {
		return EvaluationPacket{}, EvaluationKey{}, fmt.Errorf("task %q is not an included preregistered task", request.TaskID)
	}
	if task.TaskFamily == CodeTaskFamily {
		return EvaluationPacket{}, EvaluationKey{}, errors.New("code tasks require objective code outcomes, not blind prose evaluation")
	}
	if request.Repetition < 0 || request.Repetition >= task.Repetitions {
		return EvaluationPacket{}, EvaluationKey{}, fmt.Errorf(
			"repetition %d is outside preregistered task %q",
			request.Repetition,
			request.TaskID,
		)
	}
	policySHA, err := policy.SHA256()
	if err != nil {
		return EvaluationPacket{}, EvaluationKey{}, err
	}
	repetition := strconv.Itoa(request.Repetition)
	assignment := deriveSecret(seed, "assignment", policySHA, request.TaskID)
	nonceBytes := deriveSecret(seed, "nonce", policySHA, request.TaskID, repetition)
	nonce := hex.EncodeToString(nonceBytes)
	answers := []BlindAnswer{
		{Label: "answer_1", Text: request.BaselineAnswer},
		{Label: "answer_2", Text: request.CandidateAnswer},
	}
	baselineLabel, candidateLabel := "answer_1", "answer_2"
	candidateFirst := assignment[0]&1 == 1
	if request.Repetition%2 == 1 {
		candidateFirst = !candidateFirst
	}
	if candidateFirst {
		answers[0].Text, answers[1].Text = answers[1].Text, answers[0].Text
		baselineLabel, candidateLabel = candidateLabel, baselineLabel
	}
	packet := EvaluationPacket{
		SchemaVersion: EvaluationPacketSchemaVersion,
		PolicySHA256:  policySHA,
		Nonce:         nonce,
		TaskID:        request.TaskID,
		Criteria:      criteriaForTask(task),
		Answers:       answers,
	}
	packet.Commitment, err = evaluationPacketCommitment(packet)
	if err != nil {
		return EvaluationPacket{}, EvaluationKey{}, err
	}
	key := EvaluationKey{
		policySHA256:   policySHA,
		taskID:         request.TaskID,
		nonce:          nonce,
		commitment:     packet.Commitment,
		baselineLabel:  baselineLabel,
		candidateLabel: candidateLabel,
		repetition:     request.Repetition,
		valid:          true,
	}
	return packet, key, nil
}

// EvaluateAndVerify invokes exactly the two preregistered evaluators with
// separate defensive packet copies, then verifies and aggregates both outputs.
func EvaluateAndVerify(
	ctx context.Context,
	policy Policy,
	packet EvaluationPacket,
	key EvaluationKey,
	evaluators []Evaluator,
) (PairedQuality, error) {
	original := clonePacket(packet)
	if err := ValidateEvaluationPacket(policy, original); err != nil {
		return PairedQuality{}, err
	}
	if len(evaluators) != 2 {
		return PairedQuality{}, errors.New("exactly two evaluators are required")
	}
	outputs := make([]EvaluationOutput, len(evaluators))
	for index, evaluator := range evaluators {
		if evaluator == nil {
			return PairedQuality{}, fmt.Errorf("blind evaluator %d is required", index)
		}
		expectedID := policy.Quality.ProseEvaluatorIDs[index]
		if evaluator.EvaluatorID() != expectedID {
			return PairedQuality{}, fmt.Errorf("blind evaluator %d identity differs from preregistration", index)
		}
		output, err := evaluator.Evaluate(ctx, clonePacket(packet))
		if err != nil {
			return PairedQuality{}, fmt.Errorf("blind evaluator %q: %w", expectedID, err)
		}
		outputs[index] = output
	}
	return VerifyEvaluations(policy, original, key, outputs)
}

func EncodeEvaluationPacket(policy Policy, packet EvaluationPacket) ([]byte, error) {
	if err := ValidateEvaluationPacket(policy, packet); err != nil {
		return nil, err
	}
	return canonicalJSON(packet)
}

func DecodeEvaluationPacket(policy Policy, raw []byte) (EvaluationPacket, error) {
	var packet EvaluationPacket
	if err := decodeCanonical(raw, &packet); err != nil {
		return EvaluationPacket{}, fmt.Errorf("decode blind evaluation packet: %w", err)
	}
	if err := ValidateEvaluationPacket(policy, packet); err != nil {
		return EvaluationPacket{}, err
	}
	return packet, nil
}

func EncodeEvaluationOutput(packet EvaluationPacket, output EvaluationOutput) ([]byte, error) {
	if err := validateEvaluationOutput(packet, output, ""); err != nil {
		return nil, err
	}
	return canonicalJSON(output)
}

func DecodeEvaluationOutput(packet EvaluationPacket, raw []byte) (EvaluationOutput, error) {
	var output EvaluationOutput
	if err := decodeCanonical(raw, &output); err != nil {
		return EvaluationOutput{}, fmt.Errorf("decode evaluator output: %w", err)
	}
	if err := validateEvaluationOutput(packet, output, ""); err != nil {
		return EvaluationOutput{}, err
	}
	return output, nil
}

func ValidateEvaluationPacket(policy Policy, packet EvaluationPacket) error {
	if err := policy.Validate(); err != nil {
		return err
	}
	if packet.SchemaVersion != EvaluationPacketSchemaVersion {
		return fmt.Errorf("unexpected blind evaluation schema %q", packet.SchemaVersion)
	}
	policySHA, err := policy.SHA256()
	if err != nil {
		return err
	}
	if !sameSecretText(packet.PolicySHA256, policySHA) {
		return errors.New("blind packet policy commitment differs")
	}
	if !validSHA256(packet.Nonce) {
		return errors.New("blind packet nonce is invalid")
	}
	task, ok := includedTask(policy, packet.TaskID)
	if !ok {
		return fmt.Errorf("blind packet task %q is not included", packet.TaskID)
	}
	if task.TaskFamily == CodeTaskFamily {
		return errors.New("code tasks cannot produce blind prose evaluation packets")
	}
	if !reflect.DeepEqual(packet.Criteria, criteriaForTask(task)) {
		return errors.New("blind packet criteria differ from the preregistration")
	}
	if len(packet.Answers) != 2 || packet.Answers[0].Label != "answer_1" ||
		packet.Answers[1].Label != "answer_2" {
		return errors.New("blind packet must contain the two canonical answer labels")
	}
	for _, answer := range packet.Answers {
		if err := validateAnswer(answer.Text); err != nil {
			return fmt.Errorf("blind packet %s: %w", answer.Label, err)
		}
	}
	commitment, err := evaluationPacketCommitment(packet)
	if err != nil {
		return err
	}
	if !sameSecretText(packet.Commitment, commitment) {
		return errors.New("blind packet commitment differs from its payload")
	}
	return nil
}

// VerifyEvaluations binds exactly two distinct preregistered evaluator outputs
// to the randomized labels, preserves both, then applies the preregistered
// deterministic arithmetic-mean aggregation. Missing, duplicate, reordered,
// or identity-substituted judgments fail closed.
func VerifyEvaluations(
	policy Policy,
	packet EvaluationPacket,
	key EvaluationKey,
	outputs []EvaluationOutput,
) (PairedQuality, error) {
	if err := ValidateEvaluationPacket(policy, packet); err != nil {
		return PairedQuality{}, err
	}
	if !key.valid || !sameSecretText(key.policySHA256, packet.PolicySHA256) ||
		key.taskID != packet.TaskID || !sameSecretText(key.nonce, packet.Nonce) ||
		!sameSecretText(key.commitment, packet.Commitment) || key.repetition < 0 {
		return PairedQuality{}, errors.New("evaluation key does not bind the blind packet")
	}
	task, _ := includedTask(policy, packet.TaskID)
	if key.repetition >= task.Repetitions {
		return PairedQuality{}, errors.New("evaluation key repetition is outside the preregistration")
	}
	if key.baselineLabel == key.candidateLabel ||
		(key.baselineLabel != "answer_1" && key.baselineLabel != "answer_2") ||
		(key.candidateLabel != "answer_1" && key.candidateLabel != "answer_2") {
		return PairedQuality{}, errors.New("evaluation key has an invalid label assignment")
	}
	if len(outputs) != 2 || len(policy.Quality.ProseEvaluatorIDs) != 2 {
		return PairedQuality{}, errors.New("exactly two preregistered evaluator outputs are required")
	}
	if policy.Quality.ProseAggregation != ArithmeticMeanAggregation {
		return PairedQuality{}, errors.New("prose aggregation differs from the supported preregistration")
	}
	judgments := make([]IndividualJudgment, len(outputs))
	baselineTotal, candidateTotal, maximum := int64(0), int64(0), int64(0)
	for index, output := range outputs {
		expectedID := policy.Quality.ProseEvaluatorIDs[index]
		if err := validateEvaluationOutput(packet, output, expectedID); err != nil {
			return PairedQuality{}, fmt.Errorf("evaluator %q: %w", expectedID, err)
		}
		judgment, judgeBaseline, judgeCandidate, judgeMaximum, err := verifiedJudgment(packet, key, output, policy.Quality.MinimumAnswerScorePPM)
		if err != nil {
			return PairedQuality{}, err
		}
		if index == 0 {
			maximum = judgeMaximum
		} else if maximum != judgeMaximum {
			return PairedQuality{}, errors.New("evaluator score maxima differ")
		}
		baselineTotal += judgeBaseline
		candidateTotal += judgeCandidate
		judgments[index] = judgment
	}
	denominator := int64(len(judgments)) * maximum
	baselineScore := baselineTotal * PPM / denominator
	candidateScore := candidateTotal * PPM / denominator
	quality := PairedQuality{
		SchemaVersion:     VerifiedQualitySchemaVersion,
		PolicySHA256:      packet.PolicySHA256,
		TaskID:            packet.TaskID,
		TaskFamily:        task.TaskFamily,
		Repetition:        key.repetition,
		PacketCommitment:  packet.Commitment,
		Aggregation:       policy.Quality.ProseAggregation,
		Judgments:         judgments,
		BaselineScorePPM:  baselineScore,
		CandidateScorePPM: candidateScore,
		BaselinePass:      baselineScore >= policy.Quality.MinimumAnswerScorePPM,
		CandidatePass:     candidateScore >= policy.Quality.MinimumAnswerScorePPM,
	}
	seal, err := canonicalPrivateSeal("paired-quality/v3", quality)
	if err != nil {
		return PairedQuality{}, fmt.Errorf("seal verified quality: %w", err)
	}
	quality.seal = seal
	return quality, nil
}

func validateEvaluationOutput(packet EvaluationPacket, output EvaluationOutput, expectedEvaluatorID string) error {
	if output.SchemaVersion != EvaluationOutputSchemaVersion {
		return fmt.Errorf("unexpected evaluator output schema %q", output.SchemaVersion)
	}
	if !validID(output.EvaluatorID) {
		return errors.New("evaluator output identity is not canonical")
	}
	if expectedEvaluatorID != "" && output.EvaluatorID != expectedEvaluatorID {
		return errors.New("evaluator output identity differs from preregistration")
	}
	if !sameSecretText(output.Nonce, packet.Nonce) ||
		!sameSecretText(output.Commitment, packet.Commitment) {
		return errors.New("evaluator output does not echo the packet nonce and commitment")
	}
	if len(output.Answers) != len(packet.Answers) {
		return errors.New("evaluator output answer count differs from the packet")
	}
	for answerIndex, answer := range output.Answers {
		if answer.Label != packet.Answers[answerIndex].Label {
			return errors.New("evaluator output labels are missing, unknown, or reordered")
		}
		if len(answer.Items) != len(packet.Criteria) {
			return fmt.Errorf("evaluator output %s item count differs", answer.Label)
		}
		for itemIndex, item := range answer.Items {
			criterion := packet.Criteria[itemIndex]
			if item.ItemID != criterion.ItemID {
				return fmt.Errorf("evaluator output %s items are missing, unknown, or reordered", answer.Label)
			}
			if item.Score < 0 || item.Score > criterion.MaximumPoints {
				return fmt.Errorf("evaluator output %s item %s score is outside its range", answer.Label, item.ItemID)
			}
			if criterion.Kind == FactCriterion && item.Score != 0 &&
				item.Score != criterion.MaximumPoints {
				return fmt.Errorf("objective fact %s must receive zero or maximum points", item.ItemID)
			}
			if !validEvaluationText(item.Rationale, maxRationaleBytes) {
				return fmt.Errorf("evaluator output %s item %s rationale is invalid or contains forbidden metadata", answer.Label, item.ItemID)
			}
		}
	}
	return nil
}

func verifiedJudgment(
	packet EvaluationPacket,
	key EvaluationKey,
	output EvaluationOutput,
	minimumScorePPM int64,
) (IndividualJudgment, int64, int64, int64, error) {
	outputSHA256, err := canonicalDigest(output)
	if err != nil {
		return IndividualJudgment{}, 0, 0, 0, fmt.Errorf("digest evaluator output: %w", err)
	}
	byLabel := make(map[string]AnswerEvaluation, len(output.Answers))
	totals := make(map[string]int64, len(output.Answers))
	maximum := int64(0)
	for answerIndex, answer := range output.Answers {
		total := int64(0)
		answerMaximum := int64(0)
		for itemIndex, item := range answer.Items {
			criterion := packet.Criteria[itemIndex]
			if item.Score > maxQualityPoints-total || criterion.MaximumPoints > maxQualityPoints-answerMaximum {
				return IndividualJudgment{}, 0, 0, 0, errors.New("quality score total exceeds the v3 limit")
			}
			total += item.Score
			answerMaximum += criterion.MaximumPoints
		}
		if answerIndex == 0 {
			maximum = answerMaximum
		} else if maximum != answerMaximum {
			return IndividualJudgment{}, 0, 0, 0, errors.New("answer score maxima differ")
		}
		byLabel[answer.Label] = answer
		totals[answer.Label] = total
	}
	baselineTotal := totals[key.baselineLabel]
	candidateTotal := totals[key.candidateLabel]
	baselineScore := baselineTotal * PPM / maximum
	candidateScore := candidateTotal * PPM / maximum
	baselineItems := byLabel[key.baselineLabel].Items
	candidateItems := byLabel[key.candidateLabel].Items
	items := make([]JudgedItem, len(packet.Criteria))
	for index, criterion := range packet.Criteria {
		items[index] = JudgedItem{
			ItemID:         criterion.ItemID,
			BaselineScore:  baselineItems[index].Score,
			CandidateScore: candidateItems[index].Score,
		}
	}
	return IndividualJudgment{
		EvaluatorID:       output.EvaluatorID,
		OutputSHA256:      outputSHA256,
		Output:            cloneEvaluationOutput(output),
		Items:             items,
		BaselineScorePPM:  baselineScore,
		CandidateScorePPM: candidateScore,
		BaselinePass:      baselineScore >= minimumScorePPM,
		CandidatePass:     candidateScore >= minimumScorePPM,
	}, baselineTotal, candidateTotal, maximum, nil
}

func criteriaForTask(task TaskPolicy) []EvaluationCriterion {
	criteria := make([]EvaluationCriterion, 0, len(task.Facts)+len(task.Rubric))
	for _, fact := range task.Facts {
		criteria = append(criteria, EvaluationCriterion{
			ItemID:        fact.ItemID,
			Kind:          FactCriterion,
			Requirement:   fact.Requirement,
			Expected:      fact.Expected,
			MaximumPoints: fact.MaximumPoints,
		})
	}
	for _, rubric := range task.Rubric {
		criteria = append(criteria, EvaluationCriterion{
			ItemID:        rubric.ItemID,
			Kind:          RubricCriterion,
			Requirement:   rubric.Requirement,
			Expected:      "not_applicable",
			MaximumPoints: rubric.MaximumPoints,
		})
	}
	return criteria
}

func includedTask(policy Policy, taskID string) (TaskPolicy, bool) {
	for _, task := range policy.Tasks {
		if task.TaskID == taskID {
			return task, task.Status == TaskIncluded
		}
	}
	return TaskPolicy{}, false
}

type packetCommitmentPayload struct {
	SchemaVersion string                `json:"schema_version"`
	PolicySHA256  string                `json:"policy_sha256"`
	Nonce         string                `json:"nonce"`
	TaskID        string                `json:"task_id"`
	Criteria      []EvaluationCriterion `json:"criteria"`
	Answers       []BlindAnswer         `json:"answers"`
}

func evaluationPacketCommitment(packet EvaluationPacket) (string, error) {
	payload := packetCommitmentPayload{
		SchemaVersion: packet.SchemaVersion,
		PolicySHA256:  packet.PolicySHA256,
		Nonce:         packet.Nonce,
		TaskID:        packet.TaskID,
		Criteria:      packet.Criteria,
		Answers:       packet.Answers,
	}
	raw, err := canonicalJSON(payload)
	if err != nil {
		return "", fmt.Errorf("encode blind packet commitment payload: %w", err)
	}
	hasher := sha256.New()
	hasher.Write([]byte("scopesifter/tokenbench/blind-evaluation/v3\x00"))
	writeCommitmentField(hasher, raw)
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func deriveSecret(seed []byte, purpose string, fields ...string) []byte {
	hasher := sha256.New()
	hasher.Write([]byte("scopesifter/tokenbench/blind-derive/v2\x00"))
	writeCommitmentField(hasher, []byte(purpose))
	writeCommitmentField(hasher, seed)
	for _, field := range fields {
		writeCommitmentField(hasher, []byte(field))
	}
	return hasher.Sum(nil)
}

func sameSecretText(left, right string) bool {
	return len(left) == len(right) && subtle.ConstantTimeCompare([]byte(left), []byte(right)) == 1
}

func validateAnswer(answer string) error {
	if answer == "" {
		return errors.New("answer is missing")
	}
	if len(answer) > maxAnswerBytes {
		return fmt.Errorf("answer exceeds %d bytes", maxAnswerBytes)
	}
	if !utf8.ValidString(answer) || string([]byte(answer)) != answer {
		return errors.New("answer is not valid UTF-8")
	}
	for _, character := range answer {
		if character == '\x00' {
			return errors.New("answer contains NUL")
		}
	}
	return nil
}

func clonePacket(source EvaluationPacket) EvaluationPacket {
	clone := source
	clone.Criteria = append([]EvaluationCriterion(nil), source.Criteria...)
	clone.Answers = append([]BlindAnswer(nil), source.Answers...)
	return clone
}

func cloneEvaluationOutput(source EvaluationOutput) EvaluationOutput {
	clone := source
	clone.Answers = make([]AnswerEvaluation, len(source.Answers))
	for index, answer := range source.Answers {
		clone.Answers[index] = answer
		clone.Answers[index].Items = append([]ItemEvaluation(nil), answer.Items...)
	}
	return clone
}

func clonePairedQuality(source PairedQuality) PairedQuality {
	clone := source
	clone.Judgments = make([]IndividualJudgment, len(source.Judgments))
	for index, judgment := range source.Judgments {
		clone.Judgments[index] = judgment
		clone.Judgments[index].Output = cloneEvaluationOutput(judgment.Output)
		clone.Judgments[index].Items = append([]JudgedItem(nil), judgment.Items...)
	}
	return clone
}
