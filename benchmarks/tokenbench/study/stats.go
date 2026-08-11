package study

import (
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
	"sort"
)

const AnalysisSchemaVersion = "tokenbench.study-analysis/v3"

// TokenCounts contains native counters only. Cached input is a subset of
// input, and reasoning is a subset of output in the strict v1 accounting
// contract. PrimaryTotal is input plus output; a provider-reported total is an
// optional separate metric and is never substituted into PrimaryTotal.
type TokenCounts struct {
	ProviderTotalTokens   *int64 `json:"provider_total_tokens,omitempty"`
	InputTokens           int64  `json:"input_tokens"`
	CachedInputTokens     int64  `json:"cached_input_tokens"`
	CacheWriteInputTokens int64  `json:"cache_write_input_tokens"`
	OutputTokens          int64  `json:"output_tokens"`
	ReasoningTokens       int64  `json:"reasoning_tokens"`
}

type ArmObservation struct {
	Tokens              *TokenCounts `json:"tokens"`
	FailureReason       string       `json:"failure_reason"`
	MissingAnswerReason string       `json:"missing_answer_reason"`
	Completed           bool         `json:"completed"`
	AnswerPresent       bool         `json:"answer_present"`
}

type Exclusion struct {
	Code           string `json:"code"`
	Detail         string `json:"detail"`
	EvidenceSHA256 string `json:"evidence_sha256"`
}

// PairRecord must exist for every included task/repetition in policy order.
// A run that never occurred is represented with Attempted=false and an
// explicit reason; removing its record is rejected as corpus cherry-picking.
type PairRecord struct {
	Exclusion            *Exclusion            `json:"exclusion"`
	Quality              *PairedQuality        `json:"quality"`
	CodeQuality          *ObjectiveCodeQuality `json:"code_quality"`
	TaskID               string                `json:"task_id"`
	NotAttemptedReason   string                `json:"not_attempted_reason"`
	QualityMissingReason string                `json:"quality_missing_reason"`
	Baseline             ArmObservation        `json:"baseline"`
	Candidate            ArmObservation        `json:"candidate"`
	Repetition           int                   `json:"repetition"`
	Attempted            bool                  `json:"attempted"`
}

type AnalysisSeeds struct {
	Randomization []byte
	Bootstrap     []byte
}

// AnalysisReport declares counts before metrics in its canonical JSON shape.
// Statistics use only nonexcluded pairs with two completed token observations.
//
//nolint:govet,nolintlint // Field order is the byte-level canonical analysis-report wire order.
type AnalysisReport struct {
	SchemaVersion string              `json:"schema_version"`
	PolicySHA256  string              `json:"policy_sha256"`
	Counts        StudyCounts         `json:"counts"`
	Tokens        TokenReport         `json:"tokens"`
	Randomization RandomizationReport `json:"randomization"`
	Bootstrap     BootstrapReport     `json:"bootstrap"`
	Quality       QualityReport       `json:"quality"`
	InterRater    InterRaterReport    `json:"inter_rater"`
	Decision      DecisionReport      `json:"decision"`
	seal          [sha256.Size]byte
}

type StudyCounts struct {
	DeclaredTasks             int `json:"declared_tasks"`
	IncludedTasks             int `json:"included_tasks"`
	PredeclaredExcludedTasks  int `json:"predeclared_excluded_tasks"`
	PlannedPairs              int `json:"planned_pairs"`
	PredeclaredExcludedPairs  int `json:"predeclared_excluded_pairs"`
	AttemptedPairs            int `json:"attempted_pairs"`
	NotAttemptedPairs         int `json:"not_attempted_pairs"`
	FailedPairs               int `json:"failed_pairs"`
	FailedArms                int `json:"failed_arms"`
	ExcludedPairs             int `json:"excluded_pairs"`
	MissingAnswerPairs        int `json:"missing_answer_pairs"`
	QualityMissingPairs       int `json:"quality_missing_pairs"`
	CompleteTokenPairs        int `json:"complete_token_pairs"`
	CompleteTokenTasks        int `json:"complete_token_tasks"`
	QualityEvaluatedPairs     int `json:"quality_evaluated_pairs"`
	QualityEvaluatedTasks     int `json:"quality_evaluated_tasks"`
	JudgedQualityPairs        int `json:"judged_quality_pairs"`
	ObjectiveCodeQualityPairs int `json:"objective_code_quality_pairs"`
	AnalyzedPairs             int `json:"analyzed_pairs"`
}

type TokenReport struct {
	Input           MetricReport               `json:"input"`
	CachedInput     MetricReport               `json:"cached_input"`
	CacheWriteInput MetricReport               `json:"cache_write_input"`
	Output          MetricReport               `json:"output"`
	Reasoning       MetricReport               `json:"reasoning"`
	ProviderTotal   OptionalPairedMetricReport `json:"provider_total"`
	PrimaryTotal    MetricReport               `json:"primary_total"`
}

// OptionalPairedMetricReport computes a paired metric only for pairs where
// both arms reported the counter. BaselineMissingPairs and
// CandidateMissingPairs each include BothMissingPairs.
type OptionalPairedMetricReport struct {
	BothPresentPairs      int          `json:"both_present_pairs"`
	BaselineMissingPairs  int          `json:"baseline_missing_pairs"`
	CandidateMissingPairs int          `json:"candidate_missing_pairs"`
	BothMissingPairs      int          `json:"both_missing_pairs"`
	BothPresent           MetricReport `json:"both_present"`
}

type MetricReport struct {
	Baseline     Distribution `json:"baseline"`
	Candidate    Distribution `json:"candidate"`
	Delta        Distribution `json:"paired_delta"`
	Ratios       RatioReport  `json:"paired_ratio"`
	BaselineSum  int64        `json:"baseline_sum"`
	CandidateSum int64        `json:"candidate_sum"`
	RatioOfSums  Number       `json:"ratio_of_sums"`
}

type Distribution struct {
	Count   int    `json:"count"`
	Mean    Number `json:"mean"`
	Median  Number `json:"median"`
	Q25     Number `json:"q25"`
	Q75     Number `json:"q75"`
	Minimum Number `json:"minimum"`
	Maximum Number `json:"maximum"`
}

type RatioReport struct {
	Defined                       Distribution `json:"defined"`
	BaselineZeroCandidateZero     int          `json:"baseline_zero_candidate_zero"`
	BaselineZeroCandidatePositive int          `json:"baseline_zero_candidate_positive"`
}

type Number struct {
	Defined bool    `json:"defined"`
	Value   float64 `json:"value"`
}

type Interval struct {
	Defined bool    `json:"defined"`
	Lower   float64 `json:"lower"`
	Upper   float64 `json:"upper"`
}

//nolint:govet,nolintlint // Field order is the byte-level canonical randomization-report wire order.
type RandomizationReport struct {
	Method                                  string `json:"method"`
	Unit                                    string `json:"unit"`
	RepositoryClusterCount                  int    `json:"repository_cluster_count"`
	MinimumTokenReductionPPM                int64  `json:"minimum_token_reduction_ppm"`
	ObservedMarginAdjustedClusterMeanTokens Number `json:"observed_margin_adjusted_cluster_mean_tokens"`
	Assignments                             int    `json:"assignments"`
	ExtremeAssignments                      int    `json:"extreme_assignments"`
	TwoSidedPValue                          Number `json:"two_sided_p_value"`
	SeedCommitmentSHA256                    string `json:"seed_commitment_sha256"`
}

//nolint:govet,nolintlint // Field order is the byte-level canonical bootstrap-report wire order.
type BootstrapReport struct {
	Method                                    string   `json:"method"`
	Unit                                      string   `json:"unit"`
	RepositoryClusterCount                    int      `json:"repository_cluster_count"`
	Samples                                   int      `json:"samples"`
	ConfidenceLevelPPM                        int64    `json:"confidence_level_ppm"`
	MarginAdjustedClusterMeanTokensConfidence Interval `json:"margin_adjusted_cluster_mean_tokens_confidence"`
	SeedCommitmentSHA256                      string   `json:"seed_commitment_sha256"`
}

type QualityReport struct {
	EvaluatedPairs                          int          `json:"evaluated_pairs"`
	EvaluatedTasks                          int          `json:"evaluated_tasks"`
	EvaluatedRepositoryClusters             int          `json:"evaluated_repository_clusters"`
	BaselinePasses                          int          `json:"baseline_passes"`
	CandidatePasses                         int          `json:"candidate_passes"`
	CandidatePassRate                       Number       `json:"candidate_pass_rate"`
	BaselineScoresPPM                       Distribution `json:"baseline_scores_ppm"`
	CandidateScoresPPM                      Distribution `json:"candidate_scores_ppm"`
	PairedDeltaPPM                          Distribution `json:"paired_delta_ppm"`
	RepositoryClusterMeanDeltaPPMConfidence Interval     `json:"repository_cluster_mean_delta_ppm_confidence"`
	NoninferiorityMarginPPM                 int64        `json:"noninferiority_margin_ppm"`
	Noninferior                             bool         `json:"noninferior"`
}

// InterRaterReport discloses exact score agreement for the two independent
// blinded judgments. One item comparison is one answer/criterion combination;
// a pair disagrees if any such score differs.
type InterRaterReport struct {
	EvaluatorIDs          []string     `json:"evaluator_ids"`
	EvaluatedPairs        int          `json:"evaluated_pairs"`
	ItemComparisons       int          `json:"item_comparisons"`
	ExactAgreementItems   int          `json:"exact_agreement_items"`
	DisagreementItems     int          `json:"disagreement_items"`
	ExactAgreementPairs   int          `json:"exact_agreement_pairs"`
	DisagreementPairs     int          `json:"disagreement_pairs"`
	ExactAgreementRate    Number       `json:"exact_agreement_rate"`
	AbsoluteScoreDeltaPPM Distribution `json:"absolute_score_delta_ppm"`
}

type DecisionReport struct {
	Status         string       `json:"status"`
	Gates          []GateResult `json:"gates"`
	TokenEfficient bool         `json:"token_efficient"`
}

type GateResult struct {
	Name        string `json:"name"`
	Actual      string `json:"actual"`
	Requirement string `json:"requirement"`
	Passed      bool   `json:"passed"`
}

type pairedValues struct {
	taskID              string
	repositoryClusterID string
	baseline            TokenCounts
	candidate           TokenCounts
	primaryBaseline     int64
	primaryCandidate    int64
}

// Analyze validates the full preregistered record matrix before computing any
// statistic. It rejects missing, duplicate, extra, or reordered records.
func Analyze(policy Policy, records []PairRecord, seeds AnalysisSeeds) (AnalysisReport, error) {
	if err := policy.Validate(); err != nil {
		return AnalysisReport{}, err
	}
	if err := verifySeed(RandomizationSeedPurpose, seeds.Randomization, policy.Seeds.RandomizationSHA256); err != nil {
		return AnalysisReport{}, err
	}
	if err := verifySeed(BootstrapSeedPurpose, seeds.Bootstrap, policy.Seeds.BootstrapSHA256); err != nil {
		return AnalysisReport{}, err
	}
	policySHA, err := policy.SHA256()
	if err != nil {
		return AnalysisReport{}, err
	}
	counts := initialCounts(policy)
	expected := expectedRecordKeys(policy)
	if len(records) != len(expected) {
		return AnalysisReport{}, fmt.Errorf("record matrix has %d entries; preregistration requires %d", len(records), len(expected))
	}
	values := make([]pairedValues, 0, len(records))
	qualityValues := make([]qualityValue, 0, len(records))
	judgedQualities := make([]PairedQuality, 0, len(records))
	completeTasks := make(map[string]struct{})
	qualityTasks := make(map[string]struct{})
	for index, record := range records {
		if record.TaskID != expected[index].taskID || record.Repetition != expected[index].repetition {
			return AnalysisReport{}, fmt.Errorf("record %d is missing, extra, duplicate, or out of preregistered order", index)
		}
		if err := validateRecord(policy, policySHA, record); err != nil {
			return AnalysisReport{}, fmt.Errorf("record %s/%d: %w", record.TaskID, record.Repetition, err)
		}
		if !record.Attempted {
			counts.NotAttemptedPairs++
			continue
		}
		counts.AttemptedPairs++
		failedArms := 0
		if !record.Baseline.Completed {
			failedArms++
		}
		if !record.Candidate.Completed {
			failedArms++
		}
		if failedArms > 0 {
			counts.FailedPairs++
			counts.FailedArms += failedArms
		}
		if record.Exclusion != nil {
			counts.ExcludedPairs++
		}
		if (record.Baseline.Completed && !record.Baseline.AnswerPresent) ||
			(record.Candidate.Completed && !record.Candidate.AnswerPresent) {
			counts.MissingAnswerPairs++
		}
		if record.QualityMissingReason != "" {
			counts.QualityMissingPairs++
		}
		if record.Exclusion != nil || !record.Baseline.Completed || !record.Candidate.Completed {
			continue
		}
		baselineTotal, err := record.Baseline.Tokens.primaryTotal()
		if err != nil {
			return AnalysisReport{}, fmt.Errorf("baseline primary total: %w", err)
		}
		candidateTotal, err := record.Candidate.Tokens.primaryTotal()
		if err != nil {
			return AnalysisReport{}, fmt.Errorf("candidate primary total: %w", err)
		}
		values = append(values, pairedValues{
			taskID:              record.TaskID,
			repositoryClusterID: expected[index].repositoryClusterID,
			baseline:            cloneTokenCounts(*record.Baseline.Tokens),
			candidate:           cloneTokenCounts(*record.Candidate.Tokens),
			primaryBaseline:     baselineTotal,
			primaryCandidate:    candidateTotal,
		})
		counts.CompleteTokenPairs++
		counts.AnalyzedPairs++
		completeTasks[record.TaskID] = struct{}{}
		if record.Quality != nil {
			qualityValues = append(qualityValues, qualityValue{
				taskID:              record.TaskID,
				repositoryClusterID: expected[index].repositoryClusterID,
				baseline:            record.Quality.BaselineScorePPM,
				candidate:           record.Quality.CandidateScorePPM,
				baselinePass:        record.Quality.BaselinePass,
				candidatePass:       record.Quality.CandidatePass,
			})
			counts.QualityEvaluatedPairs++
			counts.JudgedQualityPairs++
			judgedQualities = append(judgedQualities, *record.Quality)
			qualityTasks[record.TaskID] = struct{}{}
		} else if record.CodeQuality != nil {
			qualityValues = append(qualityValues, qualityValue{
				taskID:              record.TaskID,
				repositoryClusterID: expected[index].repositoryClusterID,
				baseline:            record.CodeQuality.BaselineScorePPM,
				candidate:           record.CodeQuality.CandidateScorePPM,
				baselinePass:        record.CodeQuality.BaselinePass,
				candidatePass:       record.CodeQuality.CandidatePass,
			})
			counts.QualityEvaluatedPairs++
			counts.ObjectiveCodeQualityPairs++
			qualityTasks[record.TaskID] = struct{}{}
		}
	}
	counts.CompleteTokenTasks = len(completeTasks)
	counts.QualityEvaluatedTasks = len(qualityTasks)
	tokens, err := buildTokenReport(values)
	if err != nil {
		return AnalysisReport{}, err
	}
	primaryEffects, err := primaryRepositoryClusterEffects(policy, values)
	if err != nil {
		return AnalysisReport{}, err
	}
	randomization := randomizationTest(policy, primaryEffects, seeds.Randomization)
	bootstrap := bootstrapReport(policy, primaryEffects, seeds.Bootstrap)
	quality, err := buildQualityReport(policy, qualityValues, seeds.Bootstrap)
	if err != nil {
		return AnalysisReport{}, err
	}
	interRater, err := buildInterRaterReport(policy, judgedQualities)
	if err != nil {
		return AnalysisReport{}, err
	}
	decision := buildDecision(policy, counts, tokens.PrimaryTotal, randomization, quality)
	report := AnalysisReport{
		SchemaVersion: AnalysisSchemaVersion,
		PolicySHA256:  policySHA,
		Counts:        counts,
		Tokens:        tokens,
		Randomization: randomization,
		Bootstrap:     bootstrap,
		Quality:       quality,
		InterRater:    interRater,
		Decision:      decision,
	}
	reportSeal, err := canonicalPrivateSeal("analysis-report/v3", report)
	if err != nil {
		return AnalysisReport{}, fmt.Errorf("seal analysis report: %w", err)
	}
	report.seal = reportSeal
	return report, nil
}

func EncodeAnalysisReport(report AnalysisReport) ([]byte, error) {
	if report.SchemaVersion != AnalysisSchemaVersion || !validSHA256(report.PolicySHA256) {
		return nil, errors.New("analysis report was not produced by Analyze")
	}
	expectedSeal, err := canonicalPrivateSeal("analysis-report/v3", report)
	if err != nil {
		return nil, fmt.Errorf("verify analysis report seal: %w", err)
	}
	if report.seal != expectedSeal {
		return nil, errors.New("analysis report differs from the output produced by Analyze")
	}
	return canonicalJSON(report)
}

type recordKey struct {
	taskID              string
	repositoryClusterID string
	repetition          int
}

func expectedRecordKeys(policy Policy) []recordKey {
	keys := make([]recordKey, 0)
	for _, task := range policy.Tasks {
		if task.Status != TaskIncluded {
			continue
		}
		for repetition := range task.Repetitions {
			keys = append(keys, recordKey{
				taskID: task.TaskID, repositoryClusterID: task.RepositoryClusterID, repetition: repetition,
			})
		}
	}
	return keys
}

func initialCounts(policy Policy) StudyCounts {
	counts := StudyCounts{DeclaredTasks: len(policy.Tasks)}
	for _, task := range policy.Tasks {
		if task.Status == TaskIncluded {
			counts.IncludedTasks++
			counts.PlannedPairs += task.Repetitions
		} else {
			counts.PredeclaredExcludedTasks++
			counts.PredeclaredExcludedPairs += task.Repetitions
		}
	}
	return counts
}

func validateRecord(policy Policy, policySHA string, record PairRecord) error {
	task, included := includedTask(policy, record.TaskID)
	if !included {
		return errors.New("record task is not included in the policy")
	}
	if !record.Attempted {
		if !validBoundedText(record.NotAttemptedReason, 2_000) {
			return errors.New("not-attempted record requires an explicit reason")
		}
		if record.Exclusion != nil || record.Quality != nil || record.CodeQuality != nil || record.QualityMissingReason != "" ||
			record.Baseline != (ArmObservation{}) || record.Candidate != (ArmObservation{}) {
			return errors.New("not-attempted record contains attempt outcomes")
		}
		return nil
	}
	if record.NotAttemptedReason != "" {
		return errors.New("attempted record has a not-attempted reason")
	}
	if err := validateArmObservation(record.Baseline); err != nil {
		return fmt.Errorf("baseline: %w", err)
	}
	if err := validateArmObservation(record.Candidate); err != nil {
		return fmt.Errorf("candidate: %w", err)
	}
	if record.Exclusion != nil {
		if err := validateExclusion(policy, *record.Exclusion); err != nil {
			return err
		}
		if record.Quality != nil || record.CodeQuality != nil || record.QualityMissingReason != "" {
			return errors.New("excluded record must not contain a quality result or quality-missing reason")
		}
		return nil
	}
	bothAnswers := record.Baseline.Completed && record.Baseline.AnswerPresent &&
		record.Candidate.Completed && record.Candidate.AnswerPresent
	if !bothAnswers {
		if record.Quality != nil || record.CodeQuality != nil || record.QualityMissingReason != "" {
			return errors.New("record without two answers must not contain a quality result or separate quality reason")
		}
		return nil
	}
	if record.Quality == nil && record.CodeQuality == nil {
		if !validBoundedText(record.QualityMissingReason, 2_000) {
			return errors.New("two-answer record without verified quality requires an explicit reason")
		}
		return nil
	}
	if record.QualityMissingReason != "" {
		return errors.New("verified quality record also has a quality-missing reason")
	}
	if task.TaskFamily == CodeTaskFamily {
		if record.Quality != nil || record.CodeQuality == nil {
			return errors.New("code task must use only objective code quality")
		}
		return validateObjectiveCodeQuality(policy, policySHA, record.TaskID, record.Repetition, *record.CodeQuality)
	}
	if record.CodeQuality != nil || record.Quality == nil {
		return errors.New("review/explain task must use only two-judge quality")
	}
	return validatePairedQuality(policy, policySHA, record.TaskID, record.Repetition, *record.Quality)
}

func validateArmObservation(observation ArmObservation) error {
	if observation.Completed {
		if observation.FailureReason != "" {
			return errors.New("completed arm has a failure reason")
		}
		if observation.Tokens == nil {
			return errors.New("completed arm is missing native token counters")
		}
		if err := observation.Tokens.Validate(); err != nil {
			return err
		}
		if observation.AnswerPresent {
			if observation.MissingAnswerReason != "" {
				return errors.New("present answer has a missing-answer reason")
			}
		} else if !validBoundedText(observation.MissingAnswerReason, 2_000) {
			return errors.New("missing answer requires an explicit reason")
		}
		return nil
	}
	if !validBoundedText(observation.FailureReason, 2_000) {
		return errors.New("failed arm requires an explicit reason")
	}
	if observation.AnswerPresent || observation.MissingAnswerReason != "" {
		return errors.New("failed arm cannot contain answer state")
	}
	if observation.Tokens != nil {
		return observation.Tokens.Validate()
	}
	return nil
}

func validateExclusion(policy Policy, exclusion Exclusion) error {
	if !validBoundedText(exclusion.Detail, 2_000) || !validSHA256(exclusion.EvidenceSHA256) {
		return errors.New("exclusion requires detail and an evidence SHA-256")
	}
	for _, allowed := range policy.ExclusionReasons {
		if allowed.Code == exclusion.Code {
			return nil
		}
	}
	return fmt.Errorf("exclusion code %q was not preregistered", exclusion.Code)
}

func validatePairedQuality(policy Policy, policySHA, taskID string, repetition int, quality PairedQuality) error {
	expectedSeal, err := canonicalPrivateSeal("paired-quality/v3", quality)
	if err != nil {
		return fmt.Errorf("verify quality seal: %w", err)
	}
	if quality.seal != expectedSeal {
		return errors.New("verified quality differs from the output produced by VerifyEvaluations")
	}
	if quality.SchemaVersion != VerifiedQualitySchemaVersion ||
		quality.PolicySHA256 != policySHA || quality.TaskID != taskID || quality.Repetition != repetition ||
		!validSHA256(quality.PacketCommitment) {
		return errors.New("verified quality identity differs from the record and policy")
	}
	task, ok := includedTask(policy, taskID)
	if !ok || task.TaskFamily == CodeTaskFamily || quality.TaskFamily != task.TaskFamily {
		return errors.New("verified judged quality family differs from the task")
	}
	if quality.Aggregation != policy.Quality.ProseAggregation || len(quality.Judgments) != 2 {
		return errors.New("verified judged quality aggregation or judgment count differs from the policy")
	}
	criteria := criteriaForTask(task)
	maximum := int64(0)
	for _, criterion := range criteria {
		maximum += criterion.MaximumPoints
	}
	aggregateBaseline, aggregateCandidate := int64(0), int64(0)
	for index, judgment := range quality.Judgments {
		if judgment.EvaluatorID != policy.Quality.ProseEvaluatorIDs[index] || !validSHA256(judgment.OutputSHA256) {
			return errors.New("verified judgment identity or output lineage differs from the policy")
		}
		outputSHA256, err := canonicalDigest(judgment.Output)
		if err != nil || outputSHA256 != judgment.OutputSHA256 ||
			judgment.Output.EvaluatorID != judgment.EvaluatorID ||
			judgment.Output.Commitment != quality.PacketCommitment {
			return errors.New("verified judgment complete output differs from its lineage")
		}
		if len(judgment.Items) != len(criteria) {
			return errors.New("verified judgment item matrix is incomplete")
		}
		baselineTotal, candidateTotal := int64(0), int64(0)
		for itemIndex, item := range judgment.Items {
			criterion := criteria[itemIndex]
			if item.ItemID != criterion.ItemID || item.BaselineScore < 0 || item.BaselineScore > criterion.MaximumPoints ||
				item.CandidateScore < 0 || item.CandidateScore > criterion.MaximumPoints {
				return errors.New("verified judgment item matrix differs from the policy")
			}
			if criterion.Kind == FactCriterion &&
				((item.BaselineScore != 0 && item.BaselineScore != criterion.MaximumPoints) ||
					(item.CandidateScore != 0 && item.CandidateScore != criterion.MaximumPoints)) {
				return errors.New("verified objective fact judgment is not binary")
			}
			baselineTotal += item.BaselineScore
			candidateTotal += item.CandidateScore
		}
		if judgment.BaselineScorePPM != baselineTotal*PPM/maximum ||
			judgment.CandidateScorePPM != candidateTotal*PPM/maximum ||
			judgment.BaselinePass != (judgment.BaselineScorePPM >= policy.Quality.MinimumAnswerScorePPM) ||
			judgment.CandidatePass != (judgment.CandidateScorePPM >= policy.Quality.MinimumAnswerScorePPM) {
			return errors.New("verified individual judgment score or pass flag is invalid")
		}
		aggregateBaseline += baselineTotal
		aggregateCandidate += candidateTotal
	}
	denominator := int64(len(quality.Judgments)) * maximum
	if quality.BaselineScorePPM != aggregateBaseline*PPM/denominator ||
		quality.CandidateScorePPM != aggregateCandidate*PPM/denominator {
		return errors.New("verified aggregate quality differs from the preregistered arithmetic mean")
	}
	if quality.BaselinePass != (quality.BaselineScorePPM >= policy.Quality.MinimumAnswerScorePPM) ||
		quality.CandidatePass != (quality.CandidateScorePPM >= policy.Quality.MinimumAnswerScorePPM) {
		return errors.New("verified quality pass flags differ from the preregistered threshold")
	}
	return nil
}

func validateObjectiveCodeQuality(
	policy Policy,
	policySHA, taskID string,
	repetition int,
	quality ObjectiveCodeQuality,
) error {
	expectedSeal, err := canonicalPrivateSeal("objective-code-quality/v1", quality)
	if err != nil {
		return fmt.Errorf("verify objective code quality seal: %w", err)
	}
	if quality.seal != expectedSeal {
		return errors.New("objective code quality was not produced by an authenticated code outcome verifier")
	}
	task, ok := includedTask(policy, taskID)
	if !ok || task.TaskFamily != CodeTaskFamily || quality.TaskFamily != CodeTaskFamily ||
		quality.SchemaVersion != ObjectiveCodeQualitySchemaVersion || quality.PolicySHA256 != policySHA ||
		quality.TaskID != taskID || quality.Repetition != repetition ||
		!validSHA256(quality.BaselineOutcomeSHA256) || !validSHA256(quality.CandidateOutcomeSHA256) {
		return errors.New("objective code quality identity or outcome lineage differs from the record and policy")
	}
	if quality.BaselineScorePPM < 0 || quality.BaselineScorePPM > PPM ||
		quality.CandidateScorePPM < 0 || quality.CandidateScorePPM > PPM ||
		quality.BaselinePass != (quality.BaselineScorePPM >= policy.Quality.MinimumAnswerScorePPM) ||
		quality.CandidatePass != (quality.CandidateScorePPM >= policy.Quality.MinimumAnswerScorePPM) {
		return errors.New("objective code quality score or pass flag is invalid")
	}
	return nil
}

func (counts TokenCounts) Validate() error {
	if counts.InputTokens < 0 || counts.CachedInputTokens < 0 ||
		counts.CacheWriteInputTokens < 0 || counts.OutputTokens < 0 ||
		counts.ReasoningTokens < 0 ||
		(counts.ProviderTotalTokens != nil && *counts.ProviderTotalTokens < 0) {
		return errors.New("native token counters must be nonnegative")
	}
	if counts.CachedInputTokens > counts.InputTokens {
		return errors.New("cached input tokens exceed input tokens")
	}
	if counts.ReasoningTokens > counts.OutputTokens {
		return errors.New("reasoning tokens exceed output tokens under the v1 accounting contract")
	}
	_, err := counts.primaryTotal()
	return err
}

func cloneTokenCounts(source TokenCounts) TokenCounts {
	clone := source
	if source.ProviderTotalTokens != nil {
		providerTotal := *source.ProviderTotalTokens
		clone.ProviderTotalTokens = &providerTotal
	}
	return clone
}

func (counts TokenCounts) primaryTotal() (int64, error) {
	return checkedAdd(counts.InputTokens, counts.OutputTokens)
}

func buildTokenReport(values []pairedValues) (TokenReport, error) {
	type component func(pairedValues) (int64, int64)
	build := func(extract component) (MetricReport, error) {
		baseline := make([]int64, 0, len(values))
		candidate := make([]int64, 0, len(values))
		for _, value := range values {
			left, right := extract(value)
			baseline = append(baseline, left)
			candidate = append(candidate, right)
		}
		return buildMetric(baseline, candidate)
	}
	var report TokenReport
	var err error
	if report.Input, err = build(func(value pairedValues) (int64, int64) {
		return value.baseline.InputTokens, value.candidate.InputTokens
	}); err != nil {
		return TokenReport{}, err
	}
	if report.CachedInput, err = build(func(value pairedValues) (int64, int64) {
		return value.baseline.CachedInputTokens, value.candidate.CachedInputTokens
	}); err != nil {
		return TokenReport{}, err
	}
	if report.CacheWriteInput, err = build(func(value pairedValues) (int64, int64) {
		return value.baseline.CacheWriteInputTokens, value.candidate.CacheWriteInputTokens
	}); err != nil {
		return TokenReport{}, err
	}
	if report.Output, err = build(func(value pairedValues) (int64, int64) {
		return value.baseline.OutputTokens, value.candidate.OutputTokens
	}); err != nil {
		return TokenReport{}, err
	}
	if report.Reasoning, err = build(func(value pairedValues) (int64, int64) {
		return value.baseline.ReasoningTokens, value.candidate.ReasoningTokens
	}); err != nil {
		return TokenReport{}, err
	}
	if report.ProviderTotal, err = buildOptionalProviderTotalMetric(values); err != nil {
		return TokenReport{}, err
	}
	if report.PrimaryTotal, err = build(func(value pairedValues) (int64, int64) { return value.primaryBaseline, value.primaryCandidate }); err != nil {
		return TokenReport{}, err
	}
	return report, nil
}

func buildOptionalProviderTotalMetric(
	values []pairedValues,
) (OptionalPairedMetricReport, error) {
	baseline := make([]int64, 0, len(values))
	candidate := make([]int64, 0, len(values))
	report := OptionalPairedMetricReport{}
	for _, value := range values {
		left := value.baseline.ProviderTotalTokens
		right := value.candidate.ProviderTotalTokens
		if left == nil {
			report.BaselineMissingPairs++
		}
		if right == nil {
			report.CandidateMissingPairs++
		}
		if left == nil && right == nil {
			report.BothMissingPairs++
		}
		if left == nil || right == nil {
			continue
		}
		report.BothPresentPairs++
		baseline = append(baseline, *left)
		candidate = append(candidate, *right)
	}
	metric, err := buildMetric(baseline, candidate)
	if err != nil {
		return OptionalPairedMetricReport{}, err
	}
	report.BothPresent = metric
	return report, nil
}

func buildMetric(baseline, candidate []int64) (MetricReport, error) {
	if len(baseline) != len(candidate) {
		return MetricReport{}, errors.New("paired metric lengths differ")
	}
	deltas := make([]int64, len(baseline))
	ratios := make([]float64, 0, len(baseline))
	ratioReport := RatioReport{}
	for index := range baseline {
		delta, err := checkedSub(candidate[index], baseline[index])
		if err != nil {
			return MetricReport{}, fmt.Errorf("paired delta %d overflows: %w", index, err)
		}
		deltas[index] = delta
		switch {
		case baseline[index] > 0:
			ratios = append(ratios, float64(candidate[index])/float64(baseline[index]))
		case candidate[index] == 0:
			ratioReport.BaselineZeroCandidateZero++
		default:
			ratioReport.BaselineZeroCandidatePositive++
		}
	}
	baselineSum, err := checkedSum(baseline)
	if err != nil {
		return MetricReport{}, fmt.Errorf("baseline sum: %w", err)
	}
	candidateSum, err := checkedSum(candidate)
	if err != nil {
		return MetricReport{}, fmt.Errorf("candidate sum: %w", err)
	}
	ratioReport.Defined = distributionFloat(ratios)
	ratioOfSums := Number{}
	if baselineSum > 0 {
		ratioOfSums = Number{Defined: true, Value: float64(candidateSum) / float64(baselineSum)}
	}
	return MetricReport{
		Baseline: distributionInt(baseline), Candidate: distributionInt(candidate),
		Delta: distributionInt(deltas), Ratios: ratioReport,
		BaselineSum: baselineSum, CandidateSum: candidateSum, RatioOfSums: ratioOfSums,
	}, nil
}

func distributionInt(values []int64) Distribution {
	converted := make([]float64, len(values))
	for index, value := range values {
		converted[index] = float64(value)
	}
	return distributionFloat(converted)
}

func distributionFloat(values []float64) Distribution {
	if len(values) == 0 {
		return Distribution{}
	}
	sorted := append([]float64(nil), values...)
	sort.Float64s(sorted)
	sum := 0.0
	for _, value := range values {
		sum += value
	}
	return Distribution{
		Count: len(values), Mean: definedNumber(sum / float64(len(values))),
		Median: definedNumber(quantile(sorted, 0.5)), Q25: definedNumber(quantile(sorted, 0.25)),
		Q75: definedNumber(quantile(sorted, 0.75)), Minimum: definedNumber(sorted[0]),
		Maximum: definedNumber(sorted[len(sorted)-1]),
	}
}

func definedNumber(value float64) Number { return Number{Defined: true, Value: value} }

func quantile(sorted []float64, probability float64) float64 {
	if len(sorted) == 1 {
		return sorted[0]
	}
	position := probability * float64(len(sorted)-1)
	lower := int(math.Floor(position))
	upper := int(math.Ceil(position))
	if lower == upper {
		return sorted[lower]
	}
	fraction := position - float64(lower)
	return sorted[lower] + fraction*(sorted[upper]-sorted[lower])
}

type pairEffect struct {
	taskID              string
	repositoryClusterID string
	effect              float64
}

type repositoryClusterEffect struct {
	repositoryClusterID string
	effect              float64
}

type effectAccumulator struct {
	repositoryClusterID string
	sum                 float64
	compensation        float64
	count               int
}

func (accumulator *effectAccumulator) add(value float64) {
	adjusted := value - accumulator.compensation
	next := accumulator.sum + adjusted
	accumulator.compensation = (next - accumulator.sum) - adjusted
	accumulator.sum = next
	accumulator.count++
}

func (accumulator *effectAccumulator) mean() float64 {
	return accumulator.sum / float64(accumulator.count)
}

// repositoryClusterEffects averages repetitions within a task before giving
// every task equal weight within its repository cluster.
func repositoryClusterEffects(values []pairEffect) ([]repositoryClusterEffect, error) {
	byTask := make(map[string]effectAccumulator)
	taskOrder := make([]string, 0)
	for _, value := range values {
		current, exists := byTask[value.taskID]
		if exists && current.repositoryClusterID != value.repositoryClusterID {
			return nil, fmt.Errorf("task %s spans repository clusters", value.taskID)
		}
		if !exists {
			current.repositoryClusterID = value.repositoryClusterID
			taskOrder = append(taskOrder, value.taskID)
		}
		current.add(value.effect)
		byTask[value.taskID] = current
	}

	byRepositoryCluster := make(map[string]effectAccumulator)
	repositoryClusterOrder := make([]string, 0)
	for _, taskID := range taskOrder {
		task := byTask[taskID]
		current, exists := byRepositoryCluster[task.repositoryClusterID]
		if !exists {
			current.repositoryClusterID = task.repositoryClusterID
			repositoryClusterOrder = append(repositoryClusterOrder, task.repositoryClusterID)
		}
		current.add(task.mean())
		byRepositoryCluster[task.repositoryClusterID] = current
	}

	result := make([]repositoryClusterEffect, 0, len(repositoryClusterOrder))
	for _, repositoryClusterID := range repositoryClusterOrder {
		cluster := byRepositoryCluster[repositoryClusterID]
		result = append(result, repositoryClusterEffect{
			repositoryClusterID: repositoryClusterID,
			effect:              cluster.mean(),
		})
	}
	return result, nil
}

func primaryRepositoryClusterEffects(policy Policy, values []pairedValues) ([]repositoryClusterEffect, error) {
	// Nonpositive values mean the candidate met or exceeded the preregistered
	// reduction margin for the pair.
	requiredCandidateFraction := float64(PPM-policy.Analysis.MinimumTokenReductionPPM) / float64(PPM)
	pairs := make([]pairEffect, 0, len(values))
	for _, value := range values {
		pairs = append(pairs, pairEffect{
			taskID:              value.taskID,
			repositoryClusterID: value.repositoryClusterID,
			effect: float64(value.primaryCandidate) -
				requiredCandidateFraction*float64(value.primaryBaseline),
		})
	}
	return repositoryClusterEffects(pairs)
}

func randomizationTest(policy Policy, effects []repositoryClusterEffect, seed []byte) RandomizationReport {
	report := RandomizationReport{
		Method:                   "not_computed",
		Unit:                     "repository_cluster",
		RepositoryClusterCount:   len(effects),
		MinimumTokenReductionPPM: policy.Analysis.MinimumTokenReductionPPM,
		SeedCommitmentSHA256:     policy.Seeds.RandomizationSHA256,
	}
	if len(effects) > 0 {
		report.ObservedMarginAdjustedClusterMeanTokens = definedNumber(
			effectSum(effects, nil) / float64(len(effects)),
		)
	}
	if len(effects) < 2 {
		return report
	}
	observed := math.Abs(effectSum(effects, nil))
	extreme := 0
	if len(effects) <= policy.Analysis.ExactRandomizationMaxClusters {
		report.Method = "exact_repository_cluster_sign_flip"
		report.Assignments = 1 << len(effects)
		for assignment := range report.Assignments {
			if extremeEffect(effectSum(effects, func(index int) bool { return assignment&(1<<index) != 0 }), observed) {
				extreme++
			}
		}
	} else {
		report.Method = "monte_carlo_repository_cluster_sign_flip"
		report.Assignments = policy.Analysis.MonteCarloSamples
		for sample := range report.Assignments {
			if extremeEffect(effectSum(effects, func(index int) bool {
				return deterministicBit(seed, "repository-cluster-randomization", sample, index)
			}), observed) {
				extreme++
			}
		}
	}
	report.ExtremeAssignments = extreme
	if report.Method == "exact_repository_cluster_sign_flip" {
		report.TwoSidedPValue = definedNumber(float64(extreme) / float64(report.Assignments))
	} else {
		report.TwoSidedPValue = definedNumber(float64(extreme+1) / float64(report.Assignments+1))
	}
	return report
}

func effectSum(effects []repositoryClusterEffect, negative func(int) bool) float64 {
	sum, compensation := 0.0, 0.0
	for index, effect := range effects {
		value := effect.effect
		if negative != nil && negative(index) {
			value = -value
		}
		adjusted := value - compensation
		next := sum + adjusted
		compensation = (next - sum) - adjusted
		sum = next
	}
	return sum
}

func extremeEffect(value, observed float64) bool {
	tolerance := 1e-12 * math.Max(1, observed)
	return math.Abs(value)+tolerance >= observed
}

type clusterBootstrapResult struct {
	method     string
	samples    int
	confidence Interval
}

func bootstrapClusterMean(
	policy Policy,
	effects []repositoryClusterEffect,
	seed []byte,
	domain string,
) clusterBootstrapResult {
	result := clusterBootstrapResult{method: "not_computed"}
	if len(effects) < 2 {
		return result
	}
	samples := make([]float64, policy.Analysis.BootstrapSamples)
	for sample := range samples {
		var sampled effectAccumulator
		for draw := range effects {
			index := deterministicIndex(seed, domain, sample, draw, len(effects))
			sampled.add(effects[index].effect)
		}
		samples[sample] = sampled.mean()
	}
	sort.Float64s(samples)
	tail := float64(PPM-policy.Analysis.ConfidenceLevelPPM) / float64(2*PPM)
	result.method = "paired_task_then_repository_cluster_percentile"
	result.samples = len(samples)
	result.confidence = Interval{Defined: true, Lower: quantile(samples, tail), Upper: quantile(samples, 1-tail)}
	return result
}

func bootstrapReport(policy Policy, effects []repositoryClusterEffect, seed []byte) BootstrapReport {
	result := bootstrapClusterMean(policy, effects, seed, "primary-margin-adjusted-repository-cluster")
	report := BootstrapReport{
		Method:                 result.method,
		Unit:                   "repository_cluster",
		RepositoryClusterCount: len(effects),
		Samples:                result.samples,
		ConfidenceLevelPPM:     policy.Analysis.ConfidenceLevelPPM,
		MarginAdjustedClusterMeanTokensConfidence: result.confidence,
		SeedCommitmentSHA256:                      policy.Seeds.BootstrapSHA256,
	}
	return report
}

type qualityValue struct {
	taskID                      string
	repositoryClusterID         string
	baseline, candidate         int64
	baselinePass, candidatePass bool
}

func buildQualityReport(policy Policy, values []qualityValue, seed []byte) (QualityReport, error) {
	report := QualityReport{EvaluatedPairs: len(values), NoninferiorityMarginPPM: policy.Quality.NoninferiorityMarginPPM}
	baseline := make([]int64, 0, len(values))
	candidate := make([]int64, 0, len(values))
	delta := make([]int64, 0, len(values))
	pairEffects := make([]pairEffect, 0, len(values))
	tasks := make(map[string]struct{})
	for _, value := range values {
		baseline = append(baseline, value.baseline)
		candidate = append(candidate, value.candidate)
		difference, err := checkedSub(value.candidate, value.baseline)
		if err != nil {
			return QualityReport{}, err
		}
		delta = append(delta, difference)
		pairEffects = append(pairEffects, pairEffect{
			taskID: value.taskID, repositoryClusterID: value.repositoryClusterID, effect: float64(difference),
		})
		if value.baselinePass {
			report.BaselinePasses++
		}
		if value.candidatePass {
			report.CandidatePasses++
		}
		tasks[value.taskID] = struct{}{}
	}
	report.EvaluatedTasks = len(tasks)
	report.BaselineScoresPPM = distributionInt(baseline)
	report.CandidateScoresPPM = distributionInt(candidate)
	report.PairedDeltaPPM = distributionInt(delta)
	if len(values) > 0 {
		report.CandidatePassRate = definedNumber(float64(report.CandidatePasses) / float64(len(values)))
	}
	effects, err := repositoryClusterEffects(pairEffects)
	if err != nil {
		return QualityReport{}, err
	}
	report.EvaluatedRepositoryClusters = len(effects)
	bootstrap := bootstrapClusterMean(policy, effects, seed, "quality-repository-cluster")
	report.RepositoryClusterMeanDeltaPPMConfidence = bootstrap.confidence
	report.Noninferior = len(values) >= policy.Analysis.MinimumCompletePairs &&
		len(tasks) >= policy.Analysis.MinimumCompleteTasks &&
		len(effects) >= 2 && report.RepositoryClusterMeanDeltaPPMConfidence.Defined &&
		report.RepositoryClusterMeanDeltaPPMConfidence.Lower >= -float64(policy.Quality.NoninferiorityMarginPPM)
	return report, nil
}

func buildInterRaterReport(policy Policy, qualities []PairedQuality) (InterRaterReport, error) {
	evaluatorIDs := make([]string, len(policy.Quality.ProseEvaluatorIDs))
	copy(evaluatorIDs, policy.Quality.ProseEvaluatorIDs)
	report := InterRaterReport{
		EvaluatorIDs:   evaluatorIDs,
		EvaluatedPairs: len(qualities),
	}
	deltas := make([]int64, 0)
	for _, quality := range qualities {
		task, ok := includedTask(policy, quality.TaskID)
		if !ok || len(quality.Judgments) != 2 {
			return InterRaterReport{}, errors.New("inter-rater input is incomplete")
		}
		criteria := criteriaForTask(task)
		pairDisagreed := false
		for itemIndex, criterion := range criteria {
			left := quality.Judgments[0].Items[itemIndex]
			right := quality.Judgments[1].Items[itemIndex]
			for _, scores := range [][2]int64{
				{left.BaselineScore, right.BaselineScore},
				{left.CandidateScore, right.CandidateScore},
			} {
				difference := scores[0] - scores[1]
				if difference < 0 {
					difference = -difference
				}
				deltaPPM := difference * PPM / criterion.MaximumPoints
				deltas = append(deltas, deltaPPM)
				report.ItemComparisons++
				if difference == 0 {
					report.ExactAgreementItems++
				} else {
					report.DisagreementItems++
					pairDisagreed = true
				}
			}
		}
		if pairDisagreed {
			report.DisagreementPairs++
		} else {
			report.ExactAgreementPairs++
		}
	}
	if report.ItemComparisons > 0 {
		report.ExactAgreementRate = definedNumber(
			float64(report.ExactAgreementItems) / float64(report.ItemComparisons),
		)
	}
	report.AbsoluteScoreDeltaPPM = distributionInt(deltas)
	return report, nil
}

func buildDecision(policy Policy, counts StudyCounts, primary MetricReport, randomization RandomizationReport, quality QualityReport) DecisionReport {
	gates := []GateResult{
		integerGate("complete_pairs", counts.CompleteTokenPairs, policy.Analysis.MinimumCompletePairs, true),
		integerGate("complete_tasks", counts.CompleteTokenTasks, policy.Analysis.MinimumCompleteTasks, true),
		integerGate("failed_pairs", counts.FailedPairs, policy.Analysis.MaximumFailedPairs, false),
		integerGate("excluded_pairs", counts.ExcludedPairs, policy.Analysis.MaximumExcludedPairs, false),
		integerGate("not_attempted_pairs", counts.NotAttemptedPairs, policy.Analysis.MaximumNotAttemptedPairs, false),
		integerGate("missing_answer_pairs", counts.MissingAnswerPairs, policy.Analysis.MaximumMissingAnswerPairs, false),
		integerGate("quality_missing_pairs", counts.QualityMissingPairs, policy.Analysis.MaximumQualityMissingPairs, false),
		integerGate("quality_pairs", counts.QualityEvaluatedPairs, policy.Analysis.MinimumCompletePairs, true),
		integerGate("quality_tasks", counts.QualityEvaluatedTasks, policy.Analysis.MinimumCompleteTasks, true),
	}
	passRateRequirement := float64(policy.Quality.MinimumCandidatePassRatePPM) / float64(PPM)
	gates = append(gates, GateResult{Name: "candidate_quality_pass_rate", Passed: quality.CandidatePassRate.Defined && quality.CandidatePassRate.Value >= passRateRequirement, Actual: formatNumber(quality.CandidatePassRate), Requirement: fmt.Sprintf(">= %.6f", passRateRequirement)})
	gates = append(gates, GateResult{Name: "quality_noninferiority", Passed: quality.Noninferior, Actual: formatInterval(quality.RepositoryClusterMeanDeltaPPMConfidence), Requirement: fmt.Sprintf("repository-cluster lower >= -%d ppm", policy.Quality.NoninferiorityMarginPPM)})
	eligibilityGateCount := len(gates)
	gates = append(gates, GateResult{Name: "primary_baseline_nonzero", Passed: primary.BaselineSum > 0, Actual: fmt.Sprintf("%d", primary.BaselineSum), Requirement: "> 0"})
	observed := randomization.ObservedMarginAdjustedClusterMeanTokens
	gates = append(gates, GateResult{
		Name: "token_reduction", Passed: observed.Defined && observed.Value <= 0,
		Actual: formatNumber(observed),
		Requirement: fmt.Sprintf(
			"margin-adjusted repository-cluster mean <= 0 tokens at %d ppm reduction",
			policy.Analysis.MinimumTokenReductionPPM,
		),
	})
	alpha := float64(policy.Analysis.AlphaPPM) / float64(PPM)
	gates = append(gates, GateResult{Name: "randomization_p_value", Passed: randomization.TwoSidedPValue.Defined && randomization.TwoSidedPValue.Value <= alpha, Actual: formatNumber(randomization.TwoSidedPValue), Requirement: fmt.Sprintf("<= %.6f", alpha)})
	all, eligibility := true, true
	for index, gate := range gates {
		all = all && gate.Passed
		if index < eligibilityGateCount {
			eligibility = eligibility && gate.Passed
		}
	}
	status := "inconclusive"
	if all {
		status = "demonstrated"
	} else if eligibility {
		status = "not_demonstrated"
	}
	return DecisionReport{Status: status, TokenEfficient: all, Gates: gates}
}

func integerGate(name string, actual, threshold int, minimum bool) GateResult {
	passed, requirement := actual <= threshold, fmt.Sprintf("<= %d", threshold)
	if minimum {
		passed, requirement = actual >= threshold, fmt.Sprintf(">= %d", threshold)
	}
	return GateResult{Name: name, Passed: passed, Actual: fmt.Sprintf("%d", actual), Requirement: requirement}
}

func formatNumber(value Number) string {
	if !value.Defined {
		return "undefined"
	}
	return fmt.Sprintf("%.9g", value.Value)
}
func formatInterval(value Interval) string {
	if !value.Defined {
		return "undefined"
	}
	return fmt.Sprintf("[%.9g,%.9g]", value.Lower, value.Upper)
}

func checkedAdd(left, right int64) (int64, error) {
	if right > 0 && left > math.MaxInt64-right || right < 0 && left < math.MinInt64-right {
		return 0, errors.New("int64 addition overflow")
	}
	return left + right, nil
}
func checkedSub(left, right int64) (int64, error) {
	if right > 0 && left < math.MinInt64+right || right < 0 && left > math.MaxInt64+right {
		return 0, errors.New("int64 subtraction overflow")
	}
	return left - right, nil
}
func checkedSum(values []int64) (int64, error) {
	sum := int64(0)
	var err error
	for _, value := range values {
		sum, err = checkedAdd(sum, value)
		if err != nil {
			return 0, err
		}
	}
	return sum, nil
}

func deterministicBit(seed []byte, domain string, sample, index int) bool {
	digest := deterministicDigest(seed, domain, sample, index, 0)
	return digest[0]&1 == 1
}

func deterministicIndex(seed []byte, domain string, sample, draw, count int) int {
	limit := uint64(math.MaxUint64) - uint64(math.MaxUint64)%uint64(count)
	for retry := 0; ; retry++ {
		digest := deterministicDigest(seed, domain, sample, draw, retry)
		value := binary.BigEndian.Uint64(digest[:8])
		if value < limit {
			return int(value % uint64(count))
		}
	}
}

func deterministicDigest(seed []byte, domain string, values ...int) [sha256.Size]byte {
	hasher := sha256.New()
	hasher.Write([]byte("scopesifter/tokenbench/study-random/v2\x00"))
	writeCommitmentField(hasher, []byte(domain))
	writeCommitmentField(hasher, seed)
	var encoded [8]byte
	for _, value := range values {
		binary.BigEndian.PutUint64(encoded[:], uint64(value))
		hasher.Write(encoded[:])
	}
	var result [sha256.Size]byte
	copy(result[:], hasher.Sum(nil))
	return result
}
