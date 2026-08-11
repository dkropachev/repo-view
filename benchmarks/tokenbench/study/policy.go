// Package study implements harness-neutral, preregistered quality evaluation
// and paired statistical analysis for tokenbench. It deliberately accepts no
// harness process, tool-trace, arm-order, or price-table inputs.
package study

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	PolicySchemaVersion = "tokenbench.study-policy/v3"
	PPM                 = int64(1_000_000)

	BlindingSeedPurpose      = "blinding"
	RandomizationSeedPurpose = "randomization"
	BootstrapSeedPurpose     = "bootstrap"

	maxTasks            = 10_000
	maxRepetitions      = 1_000
	maxPlannedPairs     = 100_000
	maxQualityItems     = 256
	maxQualityPoints    = 1_000_000
	maxExclusionReasons = 256
	maxStatisticalWork  = 50_000_000
	minimumSeedBytes    = 32
)

var canonicalID = regexp.MustCompile(`^[a-z0-9][a-z0-9._-]{0,127}$`)

type TaskStatus string

type TaskFamily string

const (
	CodeTaskFamily    TaskFamily = "code"
	ReviewTaskFamily  TaskFamily = "review"
	ExplainTaskFamily TaskFamily = "explain"
)

type ProseAggregationMethod string

const (
	NoProseAggregation        ProseAggregationMethod = "not_applicable"
	ArithmeticMeanAggregation ProseAggregationMethod = "arithmetic_mean"
)

const (
	TaskIncluded TaskStatus = "included"
	TaskExcluded TaskStatus = "excluded"
)

// Policy is the complete v3 preregistration. Tasks and every nested item list
// are strictly sorted so equivalent corpora have one canonical encoding.
//
//nolint:govet,nolintlint // Field order is the byte-level canonical policy wire order.
type Policy struct {
	SchemaVersion    string                   `json:"schema_version"`
	StudyID          string                   `json:"study_id"`
	Tasks            []TaskPolicy             `json:"tasks"`
	ExclusionReasons []AllowedExclusionReason `json:"exclusion_reasons"`
	Quality          QualityPolicy            `json:"quality"`
	Analysis         AnalysisPolicy           `json:"analysis"`
	Seeds            SeedCommitments          `json:"seeds"`
}

//nolint:govet,nolintlint // Field order is the byte-level canonical task-policy wire order.
type TaskPolicy struct {
	TaskID              string       `json:"task_id"`
	SuiteSHA256         string       `json:"suite_sha256"`
	PromptSHA256        string       `json:"prompt_sha256"`
	RepositoryClusterID string       `json:"repository_cluster_id"`
	TaskFamily          TaskFamily   `json:"task_family"`
	Status              TaskStatus   `json:"status"`
	ExclusionReason     string       `json:"exclusion_reason"`
	Repetitions         int          `json:"repetitions"`
	Facts               []FactItem   `json:"facts"`
	Rubric              []RubricItem `json:"rubric"`
}

// FactItem is objectively scored: an evaluator may award either zero or all
// MaximumPoints, never an intermediate value.
type FactItem struct {
	ItemID        string `json:"item_id"`
	Requirement   string `json:"requirement"`
	Expected      string `json:"expected"`
	MaximumPoints int64  `json:"maximum_points"`
}

// RubricItem permits any integral score from zero through MaximumPoints.
type RubricItem struct {
	ItemID        string `json:"item_id"`
	Requirement   string `json:"requirement"`
	MaximumPoints int64  `json:"maximum_points"`
}

type AllowedExclusionReason struct {
	Code        string `json:"code"`
	Description string `json:"description"`
}

//nolint:govet,nolintlint // Field order is the byte-level canonical quality-policy wire order.
type QualityPolicy struct {
	MinimumAnswerScorePPM       int64                  `json:"minimum_answer_score_ppm"`
	MinimumCandidatePassRatePPM int64                  `json:"minimum_candidate_pass_rate_ppm"`
	NoninferiorityMarginPPM     int64                  `json:"noninferiority_margin_ppm"`
	ProseEvaluatorIDs           []string               `json:"prose_evaluator_ids"`
	ProseAggregation            ProseAggregationMethod `json:"prose_aggregation"`
}

type AnalysisPolicy struct {
	MinimumCompletePairs          int   `json:"minimum_complete_pairs"`
	MinimumCompleteTasks          int   `json:"minimum_complete_tasks"`
	MaximumFailedPairs            int   `json:"maximum_failed_pairs"`
	MaximumExcludedPairs          int   `json:"maximum_excluded_pairs"`
	MaximumNotAttemptedPairs      int   `json:"maximum_not_attempted_pairs"`
	MaximumMissingAnswerPairs     int   `json:"maximum_missing_answer_pairs"`
	MaximumQualityMissingPairs    int   `json:"maximum_quality_missing_pairs"`
	MinimumTokenReductionPPM      int64 `json:"minimum_token_reduction_ppm"`
	AlphaPPM                      int64 `json:"alpha_ppm"`
	ConfidenceLevelPPM            int64 `json:"confidence_level_ppm"`
	ExactRandomizationMaxClusters int   `json:"exact_randomization_max_clusters"`
	MonteCarloSamples             int   `json:"monte_carlo_samples"`
	BootstrapSamples              int   `json:"bootstrap_samples"`
}

type SeedCommitments struct {
	BlindingSHA256      string `json:"blinding_sha256"`
	RandomizationSHA256 string `json:"randomization_sha256"`
	BootstrapSHA256     string `json:"bootstrap_sha256"`
}

// DecodePolicy accepts only the byte-exact canonical required-field JSON form.
func DecodePolicy(raw []byte) (Policy, error) {
	var policy Policy
	if err := decodeCanonical(raw, &policy); err != nil {
		return Policy{}, fmt.Errorf("decode study policy: %w", err)
	}
	if err := policy.Validate(); err != nil {
		return Policy{}, err
	}
	return policy, nil
}

// EncodePolicy returns the sole canonical JSON representation of a valid v3
// policy. It never sorts or repairs caller input.
func EncodePolicy(policy Policy) ([]byte, error) {
	if err := policy.Validate(); err != nil {
		return nil, err
	}
	return canonicalJSON(policy)
}

// SHA256 returns the identity of the canonical preregistration.
func (policy Policy) SHA256() (string, error) {
	if err := policy.Validate(); err != nil {
		return "", err
	}
	return canonicalDigest(policy)
}

func (policy Policy) Validate() error {
	if policy.SchemaVersion != PolicySchemaVersion {
		return fmt.Errorf("unexpected study policy schema %q", policy.SchemaVersion)
	}
	if !validID(policy.StudyID) {
		return errors.New("study_id is not a canonical identifier")
	}
	if len(policy.Tasks) == 0 || len(policy.Tasks) > maxTasks {
		return fmt.Errorf("study policy must contain 1..%d tasks", maxTasks)
	}
	if policy.ExclusionReasons == nil {
		return errors.New("exclusion reasons must be a canonical array")
	}
	plannedPairs := 0
	includedTasks := 0
	hasProseTasks := false
	for index, task := range policy.Tasks {
		if index > 0 && policy.Tasks[index-1].TaskID >= task.TaskID {
			return errors.New("tasks are not strictly sorted by task_id")
		}
		if err := validateTask(task); err != nil {
			return fmt.Errorf("task %q: %w", task.TaskID, err)
		}
		if task.Status == TaskIncluded {
			if task.TaskFamily == ReviewTaskFamily || task.TaskFamily == ExplainTaskFamily {
				hasProseTasks = true
			}
			includedTasks++
			if task.Repetitions > maxPlannedPairs-plannedPairs {
				return fmt.Errorf("included task repetitions exceed %d planned pairs", maxPlannedPairs)
			}
			plannedPairs += task.Repetitions
		}
	}
	if plannedPairs == 0 {
		return errors.New("study policy contains no included task repetitions")
	}
	if len(policy.ExclusionReasons) > maxExclusionReasons {
		return fmt.Errorf("study policy has more than %d exclusion reasons", maxExclusionReasons)
	}
	for index, reason := range policy.ExclusionReasons {
		if index > 0 && policy.ExclusionReasons[index-1].Code >= reason.Code {
			return errors.New("exclusion reasons are not strictly sorted by code")
		}
		if !validID(reason.Code) || !validBoundedText(reason.Description, 2_000) {
			return fmt.Errorf("exclusion reason %d is invalid", index)
		}
	}
	if err := validateQualityPolicy(policy.Quality, hasProseTasks); err != nil {
		return err
	}
	if err := validateAnalysisPolicy(policy.Analysis, plannedPairs, includedTasks); err != nil {
		return err
	}
	if err := validateSeedCommitments(policy.Seeds); err != nil {
		return err
	}
	return nil
}

func validateTask(task TaskPolicy) error {
	if !validID(task.TaskID) {
		return errors.New("task_id is not a canonical identifier")
	}
	if !validSHA256(task.SuiteSHA256) {
		return errors.New("suite_sha256 is not a canonical SHA-256 digest")
	}
	if !validSHA256(task.PromptSHA256) {
		return errors.New("prompt_sha256 is not a canonical SHA-256 digest")
	}
	if !validID(task.RepositoryClusterID) {
		return errors.New("repository_cluster_id is not a canonical identifier")
	}
	switch task.TaskFamily {
	case CodeTaskFamily, ReviewTaskFamily, ExplainTaskFamily:
	default:
		return fmt.Errorf("unsupported task_family %q", task.TaskFamily)
	}
	if task.Repetitions <= 0 || task.Repetitions > maxRepetitions {
		return fmt.Errorf("repetitions must be in 1..%d", maxRepetitions)
	}
	if task.Facts == nil {
		return errors.New("fact items must be a canonical array")
	}
	if task.Rubric == nil {
		return errors.New("rubric items must be a canonical array")
	}
	switch task.Status {
	case TaskIncluded:
		if task.ExclusionReason != "" {
			return errors.New("included task has an exclusion reason")
		}
	case TaskExcluded:
		if !validBoundedText(task.ExclusionReason, 2_000) {
			return errors.New("excluded task requires an explicit reason")
		}
	default:
		return fmt.Errorf("invalid task status %q", task.Status)
	}
	qualityItems := len(task.Facts) + len(task.Rubric)
	if task.TaskFamily == CodeTaskFamily {
		if qualityItems != 0 {
			return errors.New("code tasks must use objective code outcomes, not prose quality items")
		}
		return nil
	}
	if qualityItems == 0 || qualityItems > maxQualityItems {
		return fmt.Errorf("review and explain tasks must contain 1..%d quality items", maxQualityItems)
	}
	seen := make(map[string]struct{}, len(task.Facts)+len(task.Rubric))
	points := int64(0)
	for index, item := range task.Facts {
		if index > 0 && task.Facts[index-1].ItemID >= item.ItemID {
			return errors.New("fact items are not strictly sorted by item_id")
		}
		if !validID(item.ItemID) ||
			!validEvaluationText(item.Requirement, 4_000) ||
			!validEvaluationText(item.Expected, 8_000) ||
			item.MaximumPoints <= 0 || item.MaximumPoints > maxQualityPoints {
			return fmt.Errorf("fact item %d is invalid", index)
		}
		seen[item.ItemID] = struct{}{}
		if item.MaximumPoints > maxQualityPoints-points {
			return errors.New("quality points exceed the v3 limit")
		}
		points += item.MaximumPoints
	}
	for index, item := range task.Rubric {
		if index > 0 && task.Rubric[index-1].ItemID >= item.ItemID {
			return errors.New("rubric items are not strictly sorted by item_id")
		}
		if !validID(item.ItemID) || !validEvaluationText(item.Requirement, 4_000) ||
			item.MaximumPoints <= 0 || item.MaximumPoints > maxQualityPoints {
			return fmt.Errorf("rubric item %d is invalid", index)
		}
		if _, exists := seen[item.ItemID]; exists {
			return fmt.Errorf("duplicate quality item_id %q", item.ItemID)
		}
		seen[item.ItemID] = struct{}{}
		if item.MaximumPoints > maxQualityPoints-points {
			return errors.New("quality points exceed the v3 limit")
		}
		points += item.MaximumPoints
	}
	return nil
}

func validateQualityPolicy(policy QualityPolicy, hasProseTasks bool) error {
	switch {
	case policy.MinimumAnswerScorePPM < 0 || policy.MinimumAnswerScorePPM > PPM:
		return errors.New("minimum answer score must be in 0..1000000 ppm")
	case policy.MinimumCandidatePassRatePPM < 0 || policy.MinimumCandidatePassRatePPM > PPM:
		return errors.New("minimum candidate pass rate must be in 0..1000000 ppm")
	case policy.NoninferiorityMarginPPM < 0 || policy.NoninferiorityMarginPPM > PPM:
		return errors.New("quality noninferiority margin must be in 0..1000000 ppm")
	}
	if policy.ProseEvaluatorIDs == nil {
		return errors.New("prose evaluator IDs must be a canonical array")
	}
	if !hasProseTasks {
		if len(policy.ProseEvaluatorIDs) != 0 || policy.ProseAggregation != NoProseAggregation {
			return errors.New("a code-only policy must not configure prose evaluators")
		}
		return nil
	}
	if len(policy.ProseEvaluatorIDs) != 2 {
		return errors.New("review and explain tasks require exactly two prose evaluator IDs")
	}
	for index, evaluatorID := range policy.ProseEvaluatorIDs {
		if !validID(evaluatorID) {
			return fmt.Errorf("prose evaluator ID %d is not canonical", index)
		}
		if index > 0 && policy.ProseEvaluatorIDs[index-1] >= evaluatorID {
			return errors.New("prose evaluator IDs must be distinct and strictly sorted")
		}
	}
	if policy.ProseAggregation != ArithmeticMeanAggregation {
		return fmt.Errorf("unsupported prose aggregation %q", policy.ProseAggregation)
	}
	return nil
}

func validateAnalysisPolicy(policy AnalysisPolicy, plannedPairs, taskCount int) error {
	switch {
	case policy.MinimumCompletePairs < 2 || policy.MinimumCompletePairs > plannedPairs:
		return errors.New("minimum complete pairs must be between 2 and the planned pair count")
	case policy.MinimumCompleteTasks < 2 || policy.MinimumCompleteTasks > taskCount:
		return errors.New("minimum complete tasks must be between 2 and the declared task count")
	case policy.MaximumFailedPairs < 0 || policy.MaximumFailedPairs > plannedPairs:
		return errors.New("maximum failed pairs is invalid")
	case policy.MaximumExcludedPairs < 0 || policy.MaximumExcludedPairs > plannedPairs:
		return errors.New("maximum excluded pairs is invalid")
	case policy.MaximumNotAttemptedPairs < 0 || policy.MaximumNotAttemptedPairs > plannedPairs:
		return errors.New("maximum not-attempted pairs is invalid")
	case policy.MaximumMissingAnswerPairs < 0 || policy.MaximumMissingAnswerPairs > plannedPairs:
		return errors.New("maximum missing-answer pairs is invalid")
	case policy.MaximumQualityMissingPairs < 0 || policy.MaximumQualityMissingPairs > plannedPairs:
		return errors.New("maximum quality-missing pairs is invalid")
	case policy.MinimumTokenReductionPPM < 0 || policy.MinimumTokenReductionPPM >= PPM:
		return errors.New("minimum token reduction must be in 0..999999 ppm")
	case policy.AlphaPPM <= 0 || policy.AlphaPPM >= PPM/2:
		return errors.New("alpha must be in 1..499999 ppm")
	case policy.ConfidenceLevelPPM <= PPM/2 || policy.ConfidenceLevelPPM >= PPM:
		return errors.New("confidence level must be in 500001..999999 ppm")
	case policy.ConfidenceLevelPPM != PPM-policy.AlphaPPM:
		return errors.New("confidence level must equal one minus alpha")
	case policy.ExactRandomizationMaxClusters <= 0 || policy.ExactRandomizationMaxClusters > 20:
		return errors.New("exact randomization maximum must be in 1..20 tasks")
	case policy.MonteCarloSamples < 1_000 || policy.MonteCarloSamples > 1_000_000:
		return errors.New("configured Monte Carlo samples must be in 1000..1000000")
	case policy.BootstrapSamples < 1_000 || policy.BootstrapSamples > 1_000_000:
		return errors.New("bootstrap samples must be in 1000..1000000")
	case policy.MonteCarloSamples > maxStatisticalWork/taskCount:
		return errors.New("configured Monte Carlo task-sample product exceeds the study work limit")
	case policy.BootstrapSamples > maxStatisticalWork/taskCount:
		return errors.New("bootstrap task-sample product exceeds the study work limit")
	default:
		return nil
	}
}

func validateSeedCommitments(seeds SeedCommitments) error {
	values := []string{seeds.BlindingSHA256, seeds.RandomizationSHA256, seeds.BootstrapSHA256}
	for _, value := range values {
		if !validSHA256(value) {
			return errors.New("study seed commitment is not a lowercase SHA-256 digest")
		}
	}
	if values[0] == values[1] || values[0] == values[2] || values[1] == values[2] {
		return errors.New("blinding, randomization, and bootstrap commitments must be distinct")
	}
	return nil
}

// CommitSeed creates a domain-separated commitment. The purpose must be one
// of the three exported purpose constants.
func CommitSeed(purpose string, seed []byte) (string, error) {
	if !validSeedPurpose(purpose) {
		return "", fmt.Errorf("invalid seed purpose %q", purpose)
	}
	if len(seed) < minimumSeedBytes {
		return "", fmt.Errorf("%s seed must contain at least %d bytes", purpose, minimumSeedBytes)
	}
	hasher := sha256.New()
	hasher.Write([]byte("scopesifter/tokenbench/study-seed/v2\x00"))
	writeCommitmentField(hasher, []byte(purpose))
	writeCommitmentField(hasher, seed)
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func verifySeed(purpose string, seed []byte, expected string) error {
	actual, err := CommitSeed(purpose, seed)
	if err != nil {
		return err
	}
	if actual != expected {
		return fmt.Errorf("%s seed does not match the preregistered commitment", purpose)
	}
	return nil
}

type byteWriter interface {
	Write([]byte) (int, error)
}

func writeCommitmentField(writer byteWriter, value []byte) {
	var length [8]byte
	binary.BigEndian.PutUint64(length[:], uint64(len(value)))
	_, _ = writer.Write(length[:])
	_, _ = writer.Write(value)
}

func validSeedPurpose(value string) bool {
	return value == BlindingSeedPurpose || value == RandomizationSeedPurpose ||
		value == BootstrapSeedPurpose
}

func validID(value string) bool {
	return canonicalID.MatchString(value)
}

func validBoundedText(value string, maximum int) bool {
	return value != "" && len(value) <= maximum && utf8.ValidString(value) &&
		!strings.ContainsRune(value, '\x00') && strings.TrimSpace(value) == value
}

func validEvaluationText(value string, maximum int) bool {
	if !validBoundedText(value, maximum) {
		return false
	}
	words := strings.FieldsFunc(strings.ToLower(value), func(character rune) bool {
		return (character < 'a' || character > 'z') &&
			(character < '0' || character > '9') && character != '_'
	})
	for _, word := range words {
		switch word {
		case "baseline", "candidate", "treatment", "arm", "order", "token", "tokens",
			"tool", "tools", "toolcall", "toolcalls", "mcp", "scopesifter", "navigator":
			return false
		}
	}
	return true
}
