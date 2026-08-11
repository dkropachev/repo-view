package study

import (
	"bytes"
	"math"
	"reflect"
	"strings"
	"testing"
)

func TestAnalyzeReportsCountsComponentsAndDemonstratedDecision(t *testing.T) {
	policy, seeds := analysisPolicyFixture(t, 4)
	records := completeRecords(t, policy, seeds, TokenCounts{InputTokens: 80, CachedInputTokens: 20, CacheWriteInputTokens: 3, OutputTokens: 20, ReasoningTokens: 5, ProviderTotalTokens: tokenPointer(123)}, TokenCounts{InputTokens: 40, CachedInputTokens: 10, CacheWriteInputTokens: 1, OutputTokens: 10, ReasoningTokens: 2, ProviderTotalTokens: tokenPointer(41)})
	report, err := Analyze(policy, records, AnalysisSeeds{Randomization: seeds.randomization, Bootstrap: seeds.bootstrap})
	if err != nil {
		t.Fatal(err)
	}
	if report.Counts.AttemptedPairs != 4 || report.Counts.FailedPairs != 0 ||
		report.Counts.ExcludedPairs != 0 || report.Counts.AnalyzedPairs != 4 ||
		report.Counts.PredeclaredExcludedTasks != 1 {
		t.Fatalf("unexpected counts: %+v", report.Counts)
	}
	if report.Tokens.Input.Baseline.Mean.Value != 80 || report.Tokens.Output.Candidate.Mean.Value != 10 ||
		report.Tokens.CachedInput.Baseline.Mean.Value != 20 || report.Tokens.CacheWriteInput.Candidate.Mean.Value != 1 ||
		report.Tokens.Reasoning.Candidate.Mean.Value != 2 {
		t.Fatalf("raw components not reported separately: %+v", report.Tokens)
	}
	if report.Tokens.PrimaryTotal.RatioOfSums.Value != 0.5 ||
		report.Tokens.ProviderTotal.BothPresentPairs != 4 ||
		report.Tokens.ProviderTotal.BaselineMissingPairs != 0 ||
		report.Tokens.ProviderTotal.CandidateMissingPairs != 0 ||
		report.Tokens.ProviderTotal.BothMissingPairs != 0 ||
		report.Tokens.ProviderTotal.BothPresent.Baseline.Mean.Value != 123 ||
		report.Tokens.ProviderTotal.BothPresent.Candidate.Mean.Value != 41 ||
		report.Randomization.Method != "exact_repository_cluster_sign_flip" ||
		report.Randomization.Unit != "repository_cluster" ||
		report.Randomization.RepositoryClusterCount != 4 ||
		report.Randomization.MinimumTokenReductionPPM != 100_000 ||
		report.Randomization.ObservedMarginAdjustedClusterMeanTokens.Value != -40 ||
		report.Randomization.TwoSidedPValue.Value != 0.125 {
		t.Fatalf("unexpected paired inference: %+v %+v", report.Tokens.PrimaryTotal, report.Randomization)
	}
	if report.Bootstrap.Method != "paired_task_then_repository_cluster_percentile" ||
		report.Bootstrap.Unit != "repository_cluster" || report.Bootstrap.RepositoryClusterCount != 4 ||
		!report.Bootstrap.MarginAdjustedClusterMeanTokensConfidence.Defined ||
		report.Bootstrap.MarginAdjustedClusterMeanTokensConfidence.Lower != -40 ||
		report.Quality.EvaluatedRepositoryClusters != 4 || !report.Quality.Noninferior ||
		report.Decision.Status != "demonstrated" || !report.Decision.TokenEfficient {
		t.Fatalf("unexpected gates: bootstrap=%+v quality=%+v decision=%+v", report.Bootstrap, report.Quality, report.Decision)
	}
}

func TestProviderTotalMetricPreservesPresenceAndMissingness(t *testing.T) {
	policy, seeds := analysisPolicyFixture(t, 4)
	records := completeRecords(
		t, policy, seeds,
		TokenCounts{InputTokens: 20},
		TokenCounts{InputTokens: 10},
	)
	records[0].Baseline.Tokens.ProviderTotalTokens = tokenPointer(0)
	records[0].Candidate.Tokens.ProviderTotalTokens = tokenPointer(0)
	records[1].Candidate.Tokens.ProviderTotalTokens = tokenPointer(5)
	records[2].Baseline.Tokens.ProviderTotalTokens = tokenPointer(7)

	report, err := Analyze(policy, records, AnalysisSeeds{
		Randomization: seeds.randomization,
		Bootstrap:     seeds.bootstrap,
	})
	if err != nil {
		t.Fatal(err)
	}
	provider := report.Tokens.ProviderTotal
	if provider.BothPresentPairs != 1 || provider.BaselineMissingPairs != 2 ||
		provider.CandidateMissingPairs != 2 || provider.BothMissingPairs != 1 ||
		provider.BothPresent.Baseline.Count != 1 ||
		provider.BothPresent.Ratios.BaselineZeroCandidateZero != 1 {
		t.Fatalf("provider-total presence report = %+v", provider)
	}
	if report.Tokens.PrimaryTotal.Baseline.Mean.Value != 20 ||
		report.Tokens.PrimaryTotal.Candidate.Mean.Value != 10 {
		t.Fatalf("provider total changed primary accounting: %+v", report.Tokens.PrimaryTotal)
	}
}

func TestProviderTotalValidationAndClone(t *testing.T) {
	t.Parallel()
	if err := (TokenCounts{ProviderTotalTokens: tokenPointer(-1)}).Validate(); err == nil {
		t.Fatal("negative provider total was accepted")
	}
	source := TokenCounts{ProviderTotalTokens: tokenPointer(0)}
	clone := cloneTokenCounts(source)
	if clone.ProviderTotalTokens == nil || *clone.ProviderTotalTokens != 0 ||
		clone.ProviderTotalTokens == source.ProviderTotalTokens {
		t.Fatalf("provider-total clone = %#v from %#v", clone, source)
	}
	*clone.ProviderTotalTokens = 1
	if *source.ProviderTotalTokens != 0 {
		t.Fatal("token-count clone aliases its source")
	}
}

func TestAnalyzeRejectsCherryPickingAndUnregisteredExclusion(t *testing.T) {
	policy, seeds := analysisPolicyFixture(t, 3)
	records := completeRecords(t, policy, seeds, TokenCounts{InputTokens: 10, OutputTokens: 2}, TokenCounts{InputTokens: 8, OutputTokens: 2})
	analysisSeeds := AnalysisSeeds{Randomization: seeds.randomization, Bootstrap: seeds.bootstrap}
	if _, err := Analyze(policy, records[:2], analysisSeeds); err == nil {
		t.Fatal("omitted preregistered record accepted")
	}
	reordered := append([]PairRecord(nil), records...)
	reordered[0], reordered[1] = reordered[1], reordered[0]
	if _, err := Analyze(policy, reordered, analysisSeeds); err == nil {
		t.Fatal("reordered record matrix accepted")
	}
	excluded := append([]PairRecord(nil), records...)
	excluded[0].Exclusion = &Exclusion{Code: "favorable_only", Detail: "Remove this result.", EvidenceSHA256: strings64('a')}
	excluded[0].Quality = nil
	if _, err := Analyze(policy, excluded, analysisSeeds); err == nil {
		t.Fatal("unregistered post-outcome exclusion accepted")
	}
	excluded[0].Exclusion.Code = "integrity_failure"
	report, err := Analyze(policy, excluded, analysisSeeds)
	if err != nil {
		t.Fatal(err)
	}
	if report.Counts.ExcludedPairs != 1 || report.Decision.Status != "inconclusive" {
		t.Fatalf("explicit exclusion was hidden: %+v %+v", report.Counts, report.Decision)
	}
}

func TestAnalyzePreservesFailuresMissingAnswersAndNotAttempted(t *testing.T) {
	policy, seeds := analysisPolicyFixture(t, 4)
	records := completeRecords(t, policy, seeds, TokenCounts{InputTokens: 10, OutputTokens: 2}, TokenCounts{InputTokens: 8, OutputTokens: 2})
	records[0] = PairRecord{TaskID: "task-01", Repetition: 0, Attempted: false, NotAttemptedReason: "Provider maintenance window."}
	records[1].Candidate = ArmObservation{Completed: false, FailureReason: "Provider request failed."}
	records[1].Quality = nil
	records[2].Candidate.AnswerPresent = false
	records[2].Candidate.MissingAnswerReason = "No final response event was emitted."
	records[2].Quality = nil
	report, err := Analyze(policy, records, AnalysisSeeds{Randomization: seeds.randomization, Bootstrap: seeds.bootstrap})
	if err != nil {
		t.Fatal(err)
	}
	if report.Counts.NotAttemptedPairs != 1 || report.Counts.FailedPairs != 1 ||
		report.Counts.FailedArms != 1 || report.Counts.MissingAnswerPairs != 1 ||
		report.Counts.CompleteTokenPairs != 2 || report.Counts.QualityEvaluatedPairs != 1 {
		t.Fatalf("missingness/failures not preserved: %+v", report.Counts)
	}
	if report.Decision.Status != "inconclusive" || report.Decision.TokenEfficient {
		t.Fatalf("incomplete study made a claim: %+v", report.Decision)
	}
	records[0].NotAttemptedReason = ""
	if _, err := Analyze(policy, records, AnalysisSeeds{Randomization: seeds.randomization, Bootstrap: seeds.bootstrap}); err == nil {
		t.Fatal("not-attempted record without reason accepted")
	}
}

func TestZeroDenominatorRulesAreExplicit(t *testing.T) {
	policy, seeds := analysisPolicyFixture(t, 2)
	records := completeRecords(t, policy, seeds, TokenCounts{}, TokenCounts{})
	records[1].Candidate.Tokens = &TokenCounts{InputTokens: 1}
	report, err := Analyze(policy, records, AnalysisSeeds{Randomization: seeds.randomization, Bootstrap: seeds.bootstrap})
	if err != nil {
		t.Fatal(err)
	}
	metric := report.Tokens.PrimaryTotal
	if metric.Ratios.Defined.Count != 0 || metric.Ratios.BaselineZeroCandidateZero != 1 ||
		metric.Ratios.BaselineZeroCandidatePositive != 1 || metric.RatioOfSums.Defined {
		t.Fatalf("zero denominator rules were not explicit: %+v", metric)
	}
	if report.Decision.TokenEfficient || report.Decision.Status == "demonstrated" {
		t.Fatalf("zero baseline produced an efficiency claim: %+v", report.Decision)
	}
}

func TestAnalyzeRejectsCounterAndAggregateOverflow(t *testing.T) {
	if err := (TokenCounts{InputTokens: math.MaxInt64, OutputTokens: 1}).Validate(); err == nil {
		t.Fatal("primary total overflow accepted")
	}
	policy, seeds := analysisPolicyFixture(t, 2)
	records := completeRecords(t, policy, seeds, TokenCounts{InputTokens: math.MaxInt64/2 + 1}, TokenCounts{InputTokens: 1})
	if _, err := Analyze(policy, records, AnalysisSeeds{Randomization: seeds.randomization, Bootstrap: seeds.bootstrap}); err == nil {
		t.Fatal("aggregate distribution overflow accepted")
	}
	records = completeRecords(
		t, policy, seeds,
		TokenCounts{InputTokens: 1, ProviderTotalTokens: tokenPointer(math.MaxInt64/2 + 1)},
		TokenCounts{InputTokens: 1, ProviderTotalTokens: tokenPointer(1)},
	)
	if _, err := Analyze(policy, records, AnalysisSeeds{Randomization: seeds.randomization, Bootstrap: seeds.bootstrap}); err == nil {
		t.Fatal("provider-total aggregate overflow accepted")
	}
}

func TestMonteCarloAndBootstrapAreDeterministicAndSeedBound(t *testing.T) {
	policy, seeds := analysisPolicyFixture(t, 4)
	policy.Analysis.ExactRandomizationMaxClusters = 2
	records := completeRecords(t, policy, seeds, TokenCounts{InputTokens: 20, OutputTokens: 5}, TokenCounts{InputTokens: 10, OutputTokens: 5})
	analysisSeeds := AnalysisSeeds{Randomization: seeds.randomization, Bootstrap: seeds.bootstrap}
	first, err := Analyze(policy, records, analysisSeeds)
	if err != nil {
		t.Fatal(err)
	}
	second, err := Analyze(policy, records, analysisSeeds)
	if err != nil {
		t.Fatal(err)
	}
	firstRaw, err := EncodeAnalysisReport(first)
	if err != nil {
		t.Fatal(err)
	}
	secondRaw, err := EncodeAnalysisReport(second)
	if err != nil {
		t.Fatal(err)
	}
	if first.Randomization.Method != "monte_carlo_repository_cluster_sign_flip" ||
		!bytes.Equal(firstRaw, secondRaw) || !reflect.DeepEqual(first, second) {
		t.Fatal("committed statistical inputs produced nondeterministic output")
	}
	wrong := append([]byte(nil), seeds.randomization...)
	wrong[0] ^= 0xff
	if _, err := Analyze(policy, records, AnalysisSeeds{Randomization: wrong, Bootstrap: seeds.bootstrap}); err == nil {
		t.Fatal("wrong Monte Carlo seed accepted")
	}
}

func TestRepositoryClusterEffectsAverageRepetitionsThenTasks(t *testing.T) {
	effects, err := repositoryClusterEffects([]pairEffect{
		{taskID: "task-01", repositoryClusterID: "repository-a", effect: 0},
		{taskID: "task-01", repositoryClusterID: "repository-a", effect: 10},
		{taskID: "task-02", repositoryClusterID: "repository-a", effect: 15},
		{taskID: "task-03", repositoryClusterID: "repository-b", effect: 30},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(effects) != 2 || effects[0].repositoryClusterID != "repository-a" || effects[0].effect != 10 ||
		effects[1].repositoryClusterID != "repository-b" || effects[1].effect != 30 {
		t.Fatalf("unexpected task-then-repository effects: %+v", effects)
	}
}

func TestInferenceRequiresTwoRepositoryClusters(t *testing.T) {
	policy, seeds := policyFixture(t, 2)
	records := completeRecords(t, policy, seeds, TokenCounts{InputTokens: 20}, TokenCounts{InputTokens: 10})
	report, err := Analyze(policy, records, AnalysisSeeds{Randomization: seeds.randomization, Bootstrap: seeds.bootstrap})
	if err != nil {
		t.Fatal(err)
	}
	if report.Randomization.RepositoryClusterCount != 1 || report.Randomization.Method != "not_computed" ||
		report.Randomization.TwoSidedPValue.Defined || report.Bootstrap.RepositoryClusterCount != 1 ||
		report.Bootstrap.MarginAdjustedClusterMeanTokensConfidence.Defined ||
		report.Quality.EvaluatedRepositoryClusters != 1 || report.Quality.Noninferior ||
		report.Decision.TokenEfficient {
		t.Fatalf("single-repository study produced inferential evidence: %+v %+v %+v %+v", report.Randomization, report.Bootstrap, report.Quality, report.Decision)
	}
}

func TestRatioOfSumsWinCannotOverrideClusterEstimandFailure(t *testing.T) {
	policy, seeds := analysisPolicyFixture(t, 3)
	policy.Tasks[0].RepositoryClusterID = "repository-a"
	policy.Tasks[1].RepositoryClusterID = "repository-a"
	policy.Tasks[2].RepositoryClusterID = "repository-b"
	records := completeRecords(t, policy, seeds, TokenCounts{}, TokenCounts{})
	for index := range 2 {
		records[index].Baseline.Tokens = &TokenCounts{InputTokens: 1_000}
		records[index].Candidate.Tokens = &TokenCounts{InputTokens: 800}
	}
	records[2].Baseline.Tokens = &TokenCounts{InputTokens: 100}
	records[2].Candidate.Tokens = &TokenCounts{InputTokens: 200}

	report, err := Analyze(policy, records, AnalysisSeeds{Randomization: seeds.randomization, Bootstrap: seeds.bootstrap})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Tokens.PrimaryTotal.RatioOfSums.Defined ||
		report.Tokens.PrimaryTotal.RatioOfSums.Value > 0.9 {
		t.Fatalf("fixture does not have the intended descriptive ratio-of-sums win: %+v", report.Tokens.PrimaryTotal)
	}
	observed := report.Randomization.ObservedMarginAdjustedClusterMeanTokens
	if !observed.Defined || observed.Value <= 0 {
		t.Fatalf("cluster estimand unexpectedly passed the reduction margin: %+v", report.Randomization)
	}
	var reductionGate *GateResult
	for index := range report.Decision.Gates {
		if report.Decision.Gates[index].Name == "token_reduction" {
			reductionGate = &report.Decision.Gates[index]
			break
		}
	}
	if reductionGate == nil || reductionGate.Passed || report.Decision.TokenEfficient {
		t.Fatalf("descriptive ratio overrode the preregistered cluster estimand: gate=%+v decision=%+v", reductionGate, report.Decision)
	}
}

func TestForgedQualityAndQualityMissingnessRejected(t *testing.T) {
	policy, seeds := analysisPolicyFixture(t, 2)
	records := completeRecords(t, policy, seeds, TokenCounts{InputTokens: 10}, TokenCounts{InputTokens: 9})
	records[0].Quality.seal = [32]byte{}
	if _, err := Analyze(policy, records, AnalysisSeeds{Randomization: seeds.randomization, Bootstrap: seeds.bootstrap}); err == nil {
		t.Fatal("caller-authored quality result accepted")
	}
	records = completeRecords(t, policy, seeds, TokenCounts{InputTokens: 10}, TokenCounts{InputTokens: 9})
	records[0].Quality = nil
	if _, err := Analyze(policy, records, AnalysisSeeds{Randomization: seeds.randomization, Bootstrap: seeds.bootstrap}); err == nil {
		t.Fatal("silently missing quality result accepted")
	}
	records[0].QualityMissingReason = "Evaluator service unavailable."
	report, err := Analyze(policy, records, AnalysisSeeds{Randomization: seeds.randomization, Bootstrap: seeds.bootstrap})
	if err != nil {
		t.Fatal(err)
	}
	if report.Counts.QualityMissingPairs != 1 || report.Decision.Status != "inconclusive" {
		t.Fatalf("quality missingness was hidden: %+v %+v", report.Counts, report.Decision)
	}
}

func TestVerifiedQualitySealRejectsMutationAndCrossRepetitionReuse(t *testing.T) {
	policy, seeds := analysisPolicyFixture(t, 2)
	policy.Tasks[0].Repetitions = 2
	analysisSeeds := AnalysisSeeds{Randomization: seeds.randomization, Bootstrap: seeds.bootstrap}
	records := completeRecords(t, policy, seeds, TokenCounts{InputTokens: 10}, TokenCounts{InputTokens: 9})

	records[0].Quality.CandidateScorePPM = 900_000
	if _, err := Analyze(policy, records, analysisSeeds); err == nil || !strings.Contains(err.Error(), "differs from the output produced by VerifyEvaluation") {
		t.Fatalf("post-verification score mutation was not rejected by the seal: %v", err)
	}

	records = completeRecords(t, policy, seeds, TokenCounts{InputTokens: 10}, TokenCounts{InputTokens: 9})
	records[1].Quality = records[0].Quality
	if _, err := Analyze(policy, records, analysisSeeds); err == nil || !strings.Contains(err.Error(), "identity differs") {
		t.Fatalf("quality result reused for another repetition was not rejected: %v", err)
	}
	records[1].Quality.Repetition = records[1].Repetition
	if _, err := Analyze(policy, records, analysisSeeds); err == nil || !strings.Contains(err.Error(), "differs from the output produced by VerifyEvaluation") {
		t.Fatalf("repetition mutation used to disguise quality reuse was not rejected by the seal: %v", err)
	}
}

func TestAnalysisReportSealRejectsTopLevelAndNestedMutation(t *testing.T) {
	policy, seeds := analysisPolicyFixture(t, 4)
	records := completeRecords(t, policy, seeds, TokenCounts{InputTokens: 20}, TokenCounts{InputTokens: 10})
	report, err := Analyze(policy, records, AnalysisSeeds{Randomization: seeds.randomization, Bootstrap: seeds.bootstrap})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := EncodeAnalysisReport(report); err != nil {
		t.Fatalf("encode untouched report: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*AnalysisReport)
	}{
		{name: "top-level counts", mutate: func(changed *AnalysisReport) {
			changed.Counts.AttemptedPairs--
		}},
		{name: "nested metric", mutate: func(changed *AnalysisReport) {
			changed.Tokens.PrimaryTotal.CandidateSum++
		}},
		{name: "optional provider metric", mutate: func(changed *AnalysisReport) {
			changed.Tokens.ProviderTotal.BothMissingPairs++
		}},
		{name: "nested gate", mutate: func(changed *AnalysisReport) {
			changed.Decision.Gates = append([]GateResult(nil), changed.Decision.Gates...)
			changed.Decision.Gates[0].Passed = !changed.Decision.Gates[0].Passed
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			changed := report
			test.mutate(&changed)
			if _, err := EncodeAnalysisReport(changed); err == nil || !strings.Contains(err.Error(), "differs from the output produced by Analyze") {
				t.Fatalf("mutated report was not rejected by the seal: %v", err)
			}
		})
	}
}

func TestAnalysisReportCanonicalTopLevelOrderStartsWithCounts(t *testing.T) {
	policy, seeds := analysisPolicyFixture(t, 2)
	records := completeRecords(t, policy, seeds, TokenCounts{InputTokens: 20}, TokenCounts{InputTokens: 10})
	report, err := Analyze(policy, records, AnalysisSeeds{Randomization: seeds.randomization, Bootstrap: seeds.bootstrap})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := EncodeAnalysisReport(report)
	if err != nil {
		t.Fatal(err)
	}
	previous := -1
	for _, field := range []string{"schema_version", "policy_sha256", "counts", "tokens", "randomization", "bootstrap", "quality", "decision"} {
		position := strings.Index(string(raw), `"`+field+`":`)
		if position <= previous {
			t.Fatalf("top-level field %q is absent or out of canonical order: %s", field, raw)
		}
		previous = position
	}
	wire := string(raw)
	for _, required := range []string{
		`"repository_cluster_count":`, `"minimum_token_reduction_ppm":`,
		`"observed_margin_adjusted_cluster_mean_tokens":`,
		`"margin_adjusted_cluster_mean_tokens_confidence":`,
		`"repository_cluster_mean_delta_ppm_confidence":`,
		`"provider_total":`, `"both_present_pairs":`, `"both_missing_pairs":`,
	} {
		if !strings.Contains(wire, required) {
			t.Fatalf("analysis report is missing repository-cluster field %s: %s", required, raw)
		}
	}
	for _, stale := range []string{`"task_count":`, `"primary_mean_delta":`, `"mean_delta_confidence":`, `"task_cluster"`} {
		if strings.Contains(wire, stale) {
			t.Fatalf("analysis report retained stale task-cluster field %s: %s", stale, raw)
		}
	}
}

func completeRecords(t *testing.T, policy Policy, seeds testSeedSet, baseline, candidate TokenCounts) []PairRecord {
	t.Helper()
	records := make([]PairRecord, 0)
	for _, task := range policy.Tasks {
		if task.Status != TaskIncluded {
			continue
		}
		for repetition := range task.Repetitions {
			packet, key, err := BlindPair(policy, BlindPairRequest{
				TaskID: task.TaskID, Repetition: repetition,
				BaselineAnswer: "A complete first response.", CandidateAnswer: "A complete second response.",
			}, seeds.blinding)
			if err != nil {
				t.Fatal(err)
			}
			quality, err := VerifyEvaluation(policy, packet, key, maximumOutput(packet))
			if err != nil {
				t.Fatal(err)
			}
			baselineCopy, candidateCopy := baseline, candidate
			records = append(records, PairRecord{
				TaskID: task.TaskID, Repetition: repetition, Attempted: true,
				Baseline:  ArmObservation{Completed: true, AnswerPresent: true, Tokens: &baselineCopy},
				Candidate: ArmObservation{Completed: true, AnswerPresent: true, Tokens: &candidateCopy},
				Quality:   &quality,
			})
		}
	}
	return records
}

func analysisPolicyFixture(t *testing.T, includedTasks int) (Policy, testSeedSet) {
	t.Helper()
	policy, seeds := policyFixture(t, includedTasks)
	cluster := 0
	for index := range policy.Tasks {
		if policy.Tasks[index].Status != TaskIncluded {
			continue
		}
		cluster++
		policy.Tasks[index].RepositoryClusterID = "repository-" + twoDigits(cluster)
	}
	if err := policy.Validate(); err != nil {
		t.Fatalf("analysis policy fixture: %v", err)
	}
	return policy, seeds
}

func strings64(character byte) string { return string(bytes.Repeat([]byte{character}, 64)) }

func tokenPointer(value int64) *int64 { return &value }
