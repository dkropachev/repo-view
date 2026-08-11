package study

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type testSeedSet struct {
	blinding      []byte
	randomization []byte
	bootstrap     []byte
}

func policyFixture(t *testing.T, includedTasks int) (Policy, testSeedSet) {
	t.Helper()
	if includedTasks < 2 {
		t.Fatal("policy fixture requires at least two included tasks")
	}
	seeds := testSeedSet{
		blinding:      bytes.Repeat([]byte{0x11}, minimumSeedBytes),
		randomization: bytes.Repeat([]byte{0x22}, minimumSeedBytes),
		bootstrap:     bytes.Repeat([]byte{0x33}, minimumSeedBytes),
	}
	commit := func(purpose string, seed []byte) string {
		t.Helper()
		value, err := CommitSeed(purpose, seed)
		if err != nil {
			t.Fatal(err)
		}
		return value
	}
	tasks := make([]TaskPolicy, 0, includedTasks+1)
	for index := 1; index <= includedTasks; index++ {
		tasks = append(tasks, taskFixture("task-"+twoDigits(index), TaskIncluded))
	}
	tasks = append(tasks, taskFixture("zz-pilot", TaskExcluded))
	policy := Policy{
		SchemaVersion: PolicySchemaVersion,
		StudyID:       "quality-study",
		Tasks:         tasks,
		ExclusionReasons: []AllowedExclusionReason{{
			Code:        "integrity_failure",
			Description: "An independently recorded integrity check failed.",
		}},
		Quality: QualityPolicy{
			MinimumAnswerScorePPM:       800_000,
			MinimumCandidatePassRatePPM: 1_000_000,
			NoninferiorityMarginPPM:     50_000,
			ProseEvaluatorIDs:           []string{"judge-alpha", "judge-beta"},
			ProseAggregation:            ArithmeticMeanAggregation,
		},
		Analysis: AnalysisPolicy{
			MinimumCompletePairs:          2,
			MinimumCompleteTasks:          2,
			MaximumFailedPairs:            0,
			MaximumExcludedPairs:          0,
			MaximumNotAttemptedPairs:      0,
			MaximumMissingAnswerPairs:     0,
			MaximumQualityMissingPairs:    0,
			MinimumTokenReductionPPM:      100_000,
			AlphaPPM:                      200_000,
			ConfidenceLevelPPM:            800_000,
			ExactRandomizationMaxClusters: 8,
			MonteCarloSamples:             1_000,
			BootstrapSamples:              1_000,
		},
		Seeds: SeedCommitments{
			BlindingSHA256:      commit(BlindingSeedPurpose, seeds.blinding),
			RandomizationSHA256: commit(RandomizationSeedPurpose, seeds.randomization),
			BootstrapSHA256:     commit(BootstrapSeedPurpose, seeds.bootstrap),
		},
	}
	if err := policy.Validate(); err != nil {
		t.Fatalf("fixture policy: %v", err)
	}
	return policy, seeds
}

func taskFixture(taskID string, status TaskStatus) TaskPolicy {
	reason := ""
	if status == TaskExcluded {
		reason = "Pilot task retained outside confirmatory analysis."
	}
	return TaskPolicy{
		TaskID:              taskID,
		SuiteSHA256:         strings.Repeat("a", 64),
		PromptSHA256:        strings.Repeat("b", 64),
		RepositoryClusterID: "repository-main",
		TaskFamily:          ExplainTaskFamily,
		Status:              status,
		ExclusionReason:     reason,
		Repetitions:         1,
		Facts: []FactItem{{
			ItemID:        "fact.location",
			Requirement:   "States the defined location.",
			Expected:      "The location is internal/config.",
			MaximumPoints: 2,
		}},
		Rubric: []RubricItem{{
			ItemID:        "rubric.explanation",
			Requirement:   "Explains the result with repository-grounded evidence.",
			MaximumPoints: 3,
		}},
	}
}

func twoDigits(value int) string {
	return string([]byte{'0' + byte(value/10), '0' + byte(value%10)})
}

func TestPolicyCanonicalGolden(t *testing.T) {
	policy, _ := policyFixture(t, 2)
	raw, err := EncodePolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "study-policy-v3.json"))
	if err != nil {
		t.Fatal(err)
	}
	want = bytes.TrimSuffix(want, []byte{'\n'})
	if !bytes.Equal(raw, want) {
		t.Fatalf("canonical policy differs from golden\n got: %s\nwant: %s", raw, want)
	}
	decoded, err := DecodePolicy(want)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := decoded.SHA256()
	if err != nil || !validSHA256(digest) {
		t.Fatalf("policy digest: %q, %v", digest, err)
	}
}

func TestPolicyStrictCanonicalDecode(t *testing.T) {
	policy, _ := policyFixture(t, 2)
	raw, err := EncodePolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{
		"trailing newline": append(append([]byte(nil), raw...), '\n'),
		"unknown field":    append(append([]byte(nil), raw[:len(raw)-1]...), []byte(`,"surprise":true}`)...),
		"duplicate field":  []byte(strings.Replace(string(raw), `"study_id":"quality-study"`, `"study_id":"quality-study","study_id":"quality-study"`, 1)),
		"invalid UTF-8":    append(append([]byte(nil), raw...), 0xff),
	}
	for name, changed := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodePolicy(changed); err == nil {
				t.Fatal("noncanonical policy accepted")
			}
		})
	}
}

func TestPolicyRejectsUnblindingCriteriaAndOneRunClaims(t *testing.T) {
	policy, _ := policyFixture(t, 2)
	policy.Tasks[0].Rubric[0].Requirement = "Prefer the candidate arm because it used fewer tokens."
	if err := policy.Validate(); err == nil {
		t.Fatal("treatment and token metadata entered evaluator-facing criteria")
	}
	policy, _ = policyFixture(t, 2)
	policy.Analysis.MinimumCompletePairs = 1
	if err := policy.Validate(); err == nil {
		t.Fatal("one-pair decision policy accepted")
	}
	policy, _ = policyFixture(t, 2)
	policy.Analysis.MinimumCompleteTasks = 1
	if err := policy.Validate(); err == nil {
		t.Fatal("one-task decision policy accepted")
	}
}

func TestPolicyRequiresExactlyTwoCanonicalDistinctProseEvaluators(t *testing.T) {
	mutations := map[string]func(*Policy){
		"nil": func(policy *Policy) { policy.Quality.ProseEvaluatorIDs = nil },
		"one": func(policy *Policy) { policy.Quality.ProseEvaluatorIDs = []string{"judge-alpha"} },
		"three": func(policy *Policy) {
			policy.Quality.ProseEvaluatorIDs = []string{"judge-alpha", "judge-beta", "judge-gamma"}
		},
		"duplicate": func(policy *Policy) {
			policy.Quality.ProseEvaluatorIDs = []string{"judge-alpha", "judge-alpha"}
		},
		"reordered": func(policy *Policy) {
			policy.Quality.ProseEvaluatorIDs = []string{"judge-beta", "judge-alpha"}
		},
		"noncanonical": func(policy *Policy) {
			policy.Quality.ProseEvaluatorIDs = []string{"Judge Alpha", "judge-beta"}
		},
		"unregistered aggregation": func(policy *Policy) {
			policy.Quality.ProseAggregation = "pick_first"
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			policy, _ := policyFixture(t, 2)
			mutate(&policy)
			if err := policy.Validate(); err == nil {
				t.Fatal("invalid two-evaluator preregistration accepted")
			}
		})
	}
}

func TestPolicySeparatesCodeFromJudgedTaskFamilies(t *testing.T) {
	policy, _ := policyFixture(t, 2)
	policy.Tasks[0].TaskFamily = CodeTaskFamily
	policy.Tasks[0].Facts = []FactItem{}
	policy.Tasks[0].Rubric = []RubricItem{}
	if err := policy.Validate(); err != nil {
		t.Fatalf("mixed family policy rejected: %v", err)
	}

	policy.Tasks[0].Facts = []FactItem{{
		ItemID: "fact.patch", Requirement: "Checks the patch.", Expected: "The patch passes.", MaximumPoints: 1,
	}}
	if err := policy.Validate(); err == nil {
		t.Fatal("code task was forced through prose criteria")
	}

	policy, _ = policyFixture(t, 2)
	policy.Tasks[0].Facts = []FactItem{}
	policy.Tasks[0].Rubric = []RubricItem{}
	if err := policy.Validate(); err == nil {
		t.Fatal("explain task without judged criteria accepted")
	}

	policy, _ = policyFixture(t, 2)
	for index := range policy.Tasks {
		policy.Tasks[index].TaskFamily = CodeTaskFamily
		policy.Tasks[index].Facts = []FactItem{}
		policy.Tasks[index].Rubric = []RubricItem{}
	}
	policy.Quality.ProseEvaluatorIDs = []string{}
	policy.Quality.ProseAggregation = NoProseAggregation
	if err := policy.Validate(); err != nil {
		t.Fatalf("code-only policy without prose judges rejected: %v", err)
	}
}

func TestPolicyRequiresSortedPredeclaredCorpus(t *testing.T) {
	policy, _ := policyFixture(t, 2)
	policy.Tasks[0], policy.Tasks[1] = policy.Tasks[1], policy.Tasks[0]
	if err := policy.Validate(); err == nil {
		t.Fatal("reordered corpus accepted")
	}
	policy, _ = policyFixture(t, 2)
	policy.Tasks[0].Status = TaskExcluded
	policy.Tasks[0].ExclusionReason = ""
	if err := policy.Validate(); err == nil {
		t.Fatal("predeclared exclusion without a reason accepted")
	}
}

func TestPolicyRequiresTaskEvidenceBindingsAndStrata(t *testing.T) {
	tests := map[string]func(*TaskPolicy){
		"suite digest missing": func(task *TaskPolicy) {
			task.SuiteSHA256 = ""
		},
		"suite digest noncanonical": func(task *TaskPolicy) {
			task.SuiteSHA256 = strings.Repeat("A", 64)
		},
		"prompt digest missing": func(task *TaskPolicy) {
			task.PromptSHA256 = ""
		},
		"prompt digest noncanonical": func(task *TaskPolicy) {
			task.PromptSHA256 = strings.Repeat("B", 64)
		},
		"repository cluster missing": func(task *TaskPolicy) {
			task.RepositoryClusterID = ""
		},
		"repository cluster noncanonical": func(task *TaskPolicy) {
			task.RepositoryClusterID = "Repository/Main"
		},
		"task family missing": func(task *TaskPolicy) {
			task.TaskFamily = ""
		},
		"task family noncanonical": func(task *TaskPolicy) {
			task.TaskFamily = "Repository Location"
		},
	}
	for _, fixture := range []struct {
		name      string
		taskIndex int
	}{
		{name: "included", taskIndex: 0},
		{name: "excluded", taskIndex: 2},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			for name, mutate := range tests {
				t.Run(name, func(t *testing.T) {
					policy, _ := policyFixture(t, 2)
					mutate(&policy.Tasks[fixture.taskIndex])
					if err := policy.Validate(); err == nil {
						t.Fatal("task without canonical evidence binding or stratum was accepted")
					}
					if _, err := EncodePolicy(policy); err == nil {
						t.Fatal("task without canonical evidence binding or stratum was encoded")
					}
				})
			}
		})
	}
}

func TestPolicyRejectsNilCanonicalArrays(t *testing.T) {
	tests := map[string]func(*Policy){
		"exclusion reasons": func(policy *Policy) {
			policy.ExclusionReasons = nil
		},
		"fact items": func(policy *Policy) {
			policy.Tasks[0].Facts = nil
		},
		"rubric items": func(policy *Policy) {
			policy.Tasks[0].Rubric = nil
		},
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			policy, _ := policyFixture(t, 2)
			mutate(&policy)
			if err := policy.Validate(); err == nil {
				t.Fatal("nil canonical array was accepted")
			}
			if _, err := EncodePolicy(policy); err == nil {
				t.Fatal("nil canonical array was encoded")
			}
			raw, err := canonicalJSON(policy)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := DecodePolicy(raw); err == nil {
				t.Fatal("canonical JSON containing null array was decoded")
			}
		})
	}

	policy, _ := policyFixture(t, 2)
	policy.ExclusionReasons = []AllowedExclusionReason{}
	policy.Tasks[0].Facts = []FactItem{}
	policy.Tasks[1].Rubric = []RubricItem{}
	if err := policy.Validate(); err != nil {
		t.Fatalf("explicit empty canonical arrays were rejected: %v", err)
	}
	raw, err := EncodePolicy(policy)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(":null")) {
		t.Fatalf("explicit empty canonical array encoded as null: %s", raw)
	}
}

func TestPolicyRequiresComplementaryAlphaAndConfidence(t *testing.T) {
	policy, _ := policyFixture(t, 2)
	policy.Analysis.ConfidenceLevelPPM++
	if err := policy.Validate(); err == nil {
		t.Fatal("confidence level inconsistent with alpha was accepted")
	}
	if _, err := EncodePolicy(policy); err == nil {
		t.Fatal("confidence level inconsistent with alpha was encoded")
	}

	policy, _ = policyFixture(t, 2)
	policy.Analysis.AlphaPPM = 50_000
	policy.Analysis.ConfidenceLevelPPM = 950_000
	if err := policy.Validate(); err != nil {
		t.Fatalf("complementary alpha and confidence were rejected: %v", err)
	}
}

func TestSeedCommitmentRejectsWeakWrongAndReusedSeeds(t *testing.T) {
	if _, err := CommitSeed(BlindingSeedPurpose, []byte("short")); err == nil {
		t.Fatal("short seed accepted")
	}
	policy, _ := policyFixture(t, 2)
	policy.Seeds.BootstrapSHA256 = policy.Seeds.RandomizationSHA256
	if err := policy.Validate(); err == nil {
		t.Fatal("reused cross-purpose seed commitment accepted")
	}
}

func TestVersionedSchemasAreJSONDocuments(t *testing.T) {
	entries, err := os.ReadDir("schemas")
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 9 {
		t.Fatalf("schema count = %d, want 9", len(entries))
	}
	want := map[string]bool{
		"blind-evaluation-v3.schema.json":       true,
		"evaluator-output-v3.schema.json":       true,
		"objective-code-quality-v1.schema.json": true,
		"study-analysis-v3.schema.json":         true,
		"study-inputs-v2.schema.json":           true,
		"study-policy-v3.schema.json":           true,
		"task-bundle-v1.schema.json":            true,
		"task-catalog-v1.schema.json":           true,
		"verified-quality-v3.schema.json":       true,
	}
	for _, entry := range entries {
		t.Run(entry.Name(), func(t *testing.T) {
			if !want[entry.Name()] {
				t.Fatal("stale or unversioned schema")
			}
			raw, err := os.ReadFile(filepath.Join("schemas", entry.Name()))
			if err != nil {
				t.Fatal(err)
			}
			var schema map[string]any
			if err := json.Unmarshal(raw, &schema); err != nil {
				t.Fatalf("invalid JSON schema: %v", err)
			}
			if schema["$schema"] != "https://json-schema.org/draft/2020-12/schema" ||
				schema["$id"] == "" || schema["additionalProperties"] != false {
				t.Fatal("schema lacks a versioned strict top-level contract")
			}
			assertSchemaReferencesAreLocal(t, schema)
		})
	}
}

func assertSchemaReferencesAreLocal(t *testing.T, value any) {
	t.Helper()
	switch typed := value.(type) {
	case map[string]any:
		for key, child := range typed {
			if key == "$ref" {
				reference, ok := child.(string)
				if !ok || !strings.HasPrefix(reference, "#/") {
					t.Fatalf("schema has a nonlocal reference %v", child)
				}
			}
			assertSchemaReferencesAreLocal(t, child)
		}
	case []any:
		for _, child := range typed {
			assertSchemaReferencesAreLocal(t, child)
		}
	}
}
