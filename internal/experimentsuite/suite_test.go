package experimentsuite

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
)

type qualityRubric struct {
	Tasks map[string]struct {
		Criteria []qualityCriterion `json:"criteria"`
	} `json:"tasks"`
}

type qualityCriterion struct {
	ID     string   `json:"id"`
	AllOf  []string `json:"all_of"`
	NoneOf []string `json:"none_of"`
}

func TestValidateJudgeRunEnforcesNotWorseSemantics(t *testing.T) {
	valid := func() qualityJudgeRun {
		return qualityJudgeRun{
			Task: "review",
			Baseline: qualityJudgeRunBaseline{
				Name: "baseline-review", Correctness: 4, Completeness: 4,
				Grounding: 4, TaskAdherence: 4,
				CriticalOmissions: []string{"", "duplicate", "duplicate"},
				UnsupportedClaims: []string{},
			},
			Candidates: []qualityJudgeRunCandidate{{
				Name: "optimized-review", Correctness: 4, Completeness: 4,
				Grounding: 4, TaskAdherence: 4,
				CriticalOmissions: []string{}, UnsupportedClaims: []string{},
				CoreConclusionMatchesBaseline: true,
				NotWorseThanBaseline:          true,
				MaterialContradictions:        []string{},
				BaselineMaterialPointsOmitted: []string{},
				CandidateMaterialAdditions:    []string{"", "duplicate", "duplicate"},
				Rationale:                     "grounded",
			}},
		}
	}
	if err := validateJudgeRun(valid()); err != nil {
		t.Fatalf("producer-valid duplicate/empty array values rejected: %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*qualityJudgeRunCandidate)
	}{
		{"lower correctness", func(candidate *qualityJudgeRunCandidate) {
			candidate.Correctness = 3
		}},
		{"lower completeness", func(candidate *qualityJudgeRunCandidate) {
			candidate.Completeness = 3
		}},
		{"lower grounding", func(candidate *qualityJudgeRunCandidate) {
			candidate.Grounding = 3
		}},
		{"lower adherence", func(candidate *qualityJudgeRunCandidate) {
			candidate.TaskAdherence = 3
		}},
		{"critical omission", func(candidate *qualityJudgeRunCandidate) {
			candidate.CriticalOmissions = []string{"material"}
		}},
		{"unsupported claim", func(candidate *qualityJudgeRunCandidate) {
			candidate.UnsupportedClaims = []string{"material"}
		}},
		{"contradiction", func(candidate *qualityJudgeRunCandidate) {
			candidate.MaterialContradictions = []string{"material"}
		}},
		{"baseline omission", func(candidate *qualityJudgeRunCandidate) {
			candidate.BaselineMaterialPointsOmitted = []string{"material"}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			run := valid()
			test.mutate(&run.Candidates[0])
			if err := validateJudgeRun(run); err == nil ||
				!strings.Contains(err.Error(), "claims not-worse") {
				t.Fatalf("coherent not-worse mutation error = %v", err)
			}
		})
	}
}

func manifestCaseFixture(outcome string) map[string]any {
	digest := strings.Repeat("1", 64)
	return map[string]any{
		"id":                       "01-safe",
		"level":                    1,
		"complexity":               "fixture",
		"outcome":                  outcome,
		"evidence":                 "current/simple",
		"source_checksum_sha256":   digest,
		"quality_aggregate_sha256": digest,
		"description":              "fixture",
		"assertions": []any{map[string]any{
			"description": "fixture",
			"source":      "metrics.json",
			"field":       "schema_version",
			"operator":    "equals",
			"value":       2,
		}},
	}
}

func liveConfigFixture() map[string]any {
	return map[string]any{
		"task":          "explain",
		"profile":       "guarded-high",
		"baseline_from": "",
		"source":        "fixtures/source.git",
		"commit":        strings.Repeat("a", 40),
		"prompt_commit": strings.Repeat("a", 9),
		"base":          strings.Repeat("b", 40),
		"model_mode":    "router",
	}
}

func resolutionCaseFixture(status string) map[string]any {
	digest := strings.Repeat("1", 64)
	fixture := map[string]any{
		"id":                       "01-safe",
		"status":                   status,
		"root_cause":               "fixture",
		"fix":                      "fixture",
		"evidence":                 "current/simple",
		"source_checksum_sha256":   digest,
		"quality_aggregate_sha256": digest,
		"metric_cases":             []any{"baseline-explain"},
		"assertions": []any{map[string]any{
			"description": "fixture",
			"source":      "metrics.json",
			"field":       "schema_version",
			"operator":    "equals",
			"value":       2,
		}},
	}
	if status == "resolved" {
		fixture["repair"] = liveConfigFixture()
	}
	return fixture
}

func TestLoadManifestRejectsPlaceholderAndUnsafeFields(t *testing.T) {
	validDigest := strings.Repeat("1", 64)
	baseCase := func() map[string]any {
		return map[string]any{
			"id":                       "01-safe",
			"level":                    1,
			"complexity":               "fixture",
			"outcome":                  "accepted",
			"evidence":                 "current/simple",
			"source_checksum_sha256":   validDigest,
			"quality_aggregate_sha256": validDigest,
			"description":              "fixture",
			"assertions": []any{map[string]any{
				"description": "fixture",
				"source":      "metrics.json",
				"field":       "schema_version",
				"operator":    "equals",
				"value":       2,
			}},
		}
	}
	tests := []struct {
		name   string
		mutate func(map[string]any)
	}{
		{
			name: "placeholder source digest",
			mutate: func(current map[string]any) {
				current["source_checksum_sha256"] = strings.Repeat("0", 64)
			},
		},
		{
			name: "placeholder aggregate digest",
			mutate: func(current map[string]any) {
				current["quality_aggregate_sha256"] = strings.Repeat("0", 64)
			},
		},
		{
			name: "unsafe id",
			mutate: func(current map[string]any) {
				current["id"] = "../escape"
			},
		},
		{
			name: "unsafe evidence",
			mutate: func(current map[string]any) {
				current["evidence"] = "../escape"
			},
		},
		{
			name: "unsafe live configuration",
			mutate: func(current map[string]any) {
				current["live"] = map[string]any{
					"task":          "arbitrary",
					"profile":       "../profile",
					"baseline_from": "/outside",
				}
			},
		},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			current := baseCase()
			testCase.mutate(current)
			manifestPath := filepath.Join(t.TempDir(), "cases.json")
			writeJSON(t, manifestPath, map[string]any{
				"schema_version": 1,
				"cases":          []any{current},
			})
			if _, err := LoadManifest(manifestPath); err == nil {
				t.Fatal("LoadManifest accepted an unsafe or placeholder field")
			}
		})
	}
	current := baseCase()
	current["live"] = map[string]any{
		"task":          "explain",
		"profile":       "guarded-high",
		"baseline_from": "",
		"source":        "fixtures/source.git",
		"commit":        strings.Repeat("a", 40),
		"prompt_commit": strings.Repeat("a", 9),
		"base":          strings.Repeat("b", 40),
		"model_mode":    "router",
	}
	manifestPath := filepath.Join(t.TempDir(), "same-run-cases.json")
	writeJSON(t, manifestPath, map[string]any{
		"schema_version": 1,
		"cases":          []any{current},
	})
	if _, err := LoadManifest(manifestPath); err != nil {
		t.Fatalf("LoadManifest rejected same-run live configuration: %v", err)
	}
}

func TestManifestSnapshotsNormalizeProvenanceAndHashParsedBytes(t *testing.T) {
	manifestPath := filepath.Join(t.TempDir(), "cases.json")
	writeJSON(t, manifestPath, map[string]any{
		"schema_version": 1,
		"cases":          []any{manifestCaseFixture("accepted")},
	})
	parsedBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, digest, err := LoadManifestSnapshot(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Cases[0].QualityProvenance != "strict-current" {
		t.Fatalf(
			"normalized provenance = %q, want strict-current",
			manifest.Cases[0].QualityProvenance,
		)
	}
	if digest != sha256Bytes(parsedBytes) {
		t.Fatalf("snapshot digest = %q, want digest of parsed bytes", digest)
	}
	if err := os.WriteFile(manifestPath, append(parsedBytes, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	mutatedBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if digest == sha256Bytes(mutatedBytes) {
		t.Fatal("snapshot digest changed to match a later manifest rewrite")
	}
}

func TestAcceptedManifestCasesRequireStrictCurrentProvenance(t *testing.T) {
	for _, provenance := range []string{"legacy-unisolated-attested", "non-strict"} {
		t.Run(provenance, func(t *testing.T) {
			fixture := manifestCaseFixture("accepted")
			fixture["quality_provenance"] = provenance
			manifestPath := filepath.Join(t.TempDir(), "cases.json")
			writeJSON(t, manifestPath, map[string]any{
				"schema_version": 1,
				"cases":          []any{fixture},
			})
			if _, err := LoadManifest(manifestPath); err == nil ||
				!strings.Contains(err.Error(), "requires strict-current") {
				t.Fatalf("accepted %s provenance error = %v", provenance, err)
			}
		})
	}

	fixture := manifestCaseFixture("rejected")
	fixture["quality_provenance"] = "legacy-unisolated-attested"
	manifestPath := filepath.Join(t.TempDir(), "rejected-cases.json")
	writeJSON(t, manifestPath, map[string]any{
		"schema_version": 1,
		"cases":          []any{fixture},
	})
	if _, err := LoadManifest(manifestPath); err != nil {
		t.Fatalf("rejected historical provenance was rejected: %v", err)
	}
}

func TestResolutionSnapshotsRequireStrictCurrentAndHashParsedBytes(t *testing.T) {
	manifestPath := filepath.Join(t.TempDir(), "resolutions.json")
	writeJSON(t, manifestPath, map[string]any{
		"schema_version": 1,
		"cases":          []any{resolutionCaseFixture("accepted")},
	})
	parsedBytes, err := os.ReadFile(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	manifest, digest, err := LoadResolutionManifestSnapshot(manifestPath)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Cases[0].QualityProvenance != "strict-current" {
		t.Fatalf(
			"normalized resolution provenance = %q, want strict-current",
			manifest.Cases[0].QualityProvenance,
		)
	}
	if digest != sha256Bytes(parsedBytes) {
		t.Fatalf("resolution digest = %q, want digest of parsed bytes", digest)
	}

	for _, status := range []string{"accepted", "resolved"} {
		t.Run(status, func(t *testing.T) {
			fixture := resolutionCaseFixture(status)
			fixture["quality_provenance"] = "legacy-unisolated-attested"
			path := filepath.Join(t.TempDir(), "resolutions.json")
			writeJSON(t, path, map[string]any{
				"schema_version": 1,
				"cases":          []any{fixture},
			})
			if _, err := LoadResolutionManifest(path); err == nil ||
				!strings.Contains(err.Error(), "requires strict-current") {
				t.Fatalf("%s legacy provenance error = %v", status, err)
			}
		})
	}
}

func TestLoadManifestRejectsUnanchoredLiveConfiguration(t *testing.T) {
	validDigest := strings.Repeat("1", 64)
	baseLive := func() map[string]any {
		return map[string]any{
			"task":          "explain",
			"profile":       "guarded-high",
			"baseline_from": "",
			"source":        "fixtures/source.git",
			"commit":        strings.Repeat("a", 40),
			"prompt_commit": strings.Repeat("a", 9),
			"base":          strings.Repeat("b", 40),
			"model_mode":    "router",
		}
	}
	for _, testCase := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "missing source", mutate: func(live map[string]any) { delete(live, "source") }},
		{name: "missing commit", mutate: func(live map[string]any) { delete(live, "commit") }},
		{name: "prompt not commit prefix", mutate: func(live map[string]any) { live["prompt_commit"] = strings.Repeat("c", 9) }},
		{name: "same base", mutate: func(live map[string]any) { live["base"] = strings.Repeat("a", 40) }},
		{name: "unknown model mode", mutate: func(live map[string]any) { live["model_mode"] = "automatic" }},
		{name: "pinned without model", mutate: func(live map[string]any) { live["model_mode"] = "pinned" }},
		{name: "router with ignored model", mutate: func(live map[string]any) { live["model"] = "gpt-fixture" }},
		{name: "unsafe pinned model", mutate: func(live map[string]any) {
			live["model_mode"] = "pinned"
			live["model"] = "-option"
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			live := baseLive()
			testCase.mutate(live)
			manifestPath := filepath.Join(t.TempDir(), "cases.json")
			writeJSON(t, manifestPath, map[string]any{
				"schema_version": 1,
				"cases": []any{map[string]any{
					"id":                       "01-safe",
					"level":                    1,
					"complexity":               "fixture",
					"outcome":                  "accepted",
					"evidence":                 "current/simple",
					"source_checksum_sha256":   validDigest,
					"quality_aggregate_sha256": validDigest,
					"description":              "fixture",
					"live":                     live,
					"assertions": []any{map[string]any{
						"description": "fixture",
						"source":      "metrics.json",
						"field":       "schema_version",
						"operator":    "equals",
						"value":       2,
					}},
				}},
			})
			if _, err := LoadManifest(manifestPath); err == nil {
				t.Fatal("LoadManifest accepted unanchored live configuration")
			}
		})
	}
}

func TestValidateLiveIdentity(t *testing.T) {
	config := LiveConfig{
		Task:         "deep",
		Profile:      "investigative-verified-high",
		Source:       "fixtures/source.git",
		Commit:       strings.Repeat("a", 40),
		PromptCommit: strings.Repeat("a", 9),
		Base:         strings.Repeat("b", 40),
		ModelMode:    "router",
	}
	runDir := t.TempDir()
	manifestFixture := func(config LiveConfig, baselineFrom any) map[string]any {
		variant := "all"
		if baselineFrom != nil {
			variant = "optimized"
		}
		model := config.Model
		modelConfiguration := "pinned"
		if config.ModelMode == "router" {
			model = "router-selected"
			modelConfiguration = "none"
		}
		return map[string]any{
			"source_repo":         config.Source,
			"target_commit":       config.Commit,
			"prompt_commit":       config.PromptCommit,
			"base_commit":         config.Base,
			"task_selection":      config.Task,
			"variant_selection":   variant,
			"profiles":            []any{config.Profile},
			"baseline_from":       baselineFrom,
			"model":               model,
			"model_mode":          config.ModelMode,
			"model_configuration": modelConfiguration,
		}
	}
	writeJSON(
		t,
		filepath.Join(runDir, "manifest.json"),
		manifestFixture(config, nil),
	)
	if result := ValidateLiveIdentity(runDir, config); !result.Passed {
		t.Fatalf("ValidateLiveIdentity rejected matching identity: %+v", result)
	}

	for _, testCase := range []struct {
		name   string
		mutate func(map[string]any)
	}{
		{name: "task", mutate: func(manifest map[string]any) { manifest["task_selection"] = "review" }},
		{name: "profile", mutate: func(manifest map[string]any) { manifest["profiles"] = []any{"other-profile"} }},
		{name: "variant", mutate: func(manifest map[string]any) { manifest["variant_selection"] = "baseline" }},
		{name: "baseline", mutate: func(manifest map[string]any) { manifest["baseline_from"] = "/other/run" }},
		{name: "model", mutate: func(manifest map[string]any) { manifest["model"] = "ambient-model" }},
		{name: "model mode", mutate: func(manifest map[string]any) { manifest["model_mode"] = "pinned" }},
		{name: "model configuration", mutate: func(manifest map[string]any) { manifest["model_configuration"] = "pinned" }},
		{name: "missing field", mutate: func(manifest map[string]any) { delete(manifest, "task_selection") }},
	} {
		t.Run("rejects "+testCase.name+" mismatch", func(t *testing.T) {
			manifest := manifestFixture(config, nil)
			testCase.mutate(manifest)
			writeJSON(t, filepath.Join(runDir, "manifest.json"), manifest)
			if result := ValidateLiveIdentity(runDir, config); result.Passed {
				t.Fatalf("ValidateLiveIdentity accepted mismatched identity: %+v", result)
			}
		})
	}

	pinned := config
	pinned.ModelMode = "pinned"
	pinned.Model = "gpt-fixture"
	writeJSON(
		t,
		filepath.Join(runDir, "manifest.json"),
		manifestFixture(pinned, nil),
	)
	if result := ValidateLiveIdentity(runDir, pinned); !result.Passed {
		t.Fatalf("ValidateLiveIdentity rejected pinned model identity: %+v", result)
	}

	baseline := config
	baseline.BaselineFrom = "baselines/accepted"
	resolvedBaseline := filepath.Join(runDir, "resolved-baseline")
	writeJSON(
		t,
		filepath.Join(runDir, "manifest.json"),
		manifestFixture(baseline, resolvedBaseline),
	)
	if result := ValidateLiveIdentity(
		runDir,
		baseline,
		baseline.Source,
		resolvedBaseline,
	); !result.Passed {
		t.Fatalf("ValidateLiveIdentity rejected bound baseline identity: %+v", result)
	}
}

func TestLoadResolutionManifestRejectsUnsafeExecutionPaths(t *testing.T) {
	validDigest := strings.Repeat("1", 64)
	for _, packagePath := range []string{
		"example.com/remote/module",
		"../outside",
		"./../outside",
		`.\outside`,
	} {
		t.Run(packagePath, func(t *testing.T) {
			manifestPath := filepath.Join(t.TempDir(), "resolutions.json")
			writeJSON(t, manifestPath, map[string]any{
				"schema_version": 1,
				"cases": []any{map[string]any{
					"id":                       "01-safe",
					"status":                   "accepted",
					"root_cause":               "fixture",
					"fix":                      "fixture",
					"evidence":                 "current/simple",
					"source_checksum_sha256":   validDigest,
					"quality_aggregate_sha256": validDigest,
					"metric_cases":             []any{"baseline-explain"},
					"assertions": []any{map[string]any{
						"description": "fixture",
						"source":      "metrics.json",
						"field":       "schema_version",
						"operator":    "equals",
						"value":       2,
					}},
					"go_tests": []any{map[string]any{
						"description": "fixture",
						"package":     packagePath,
						"run":         "TestFixture",
					}},
				}},
			})
			if _, err := LoadResolutionManifest(manifestPath); err == nil {
				t.Fatal("LoadResolutionManifest accepted a non-local Go package")
			}
		})
	}
}

func strictGenerationConfigFixture(
	mechanicalNavigation bool,
) qualityGenerationConfig {
	config := qualityGenerationConfig{
		GenerationIsolation:          qualityGenerationIsolation,
		DeveloperInstructions:        qualityNoCollaboration,
		FeatureFlags:                 append([]string(nil), qualityFeatureFlags...),
		CodexIsolationFlags:          append([]string(nil), qualityCodexIsolationFlags...),
		CodexEnvironment:             append([]string(nil), qualityCodexEnvironment...),
		HostGoEnvironment:            append([]string(nil), qualityHostGoEnvironment...),
		ProfilesSnapshotPath:         "profiles-snapshot.tsv",
		ProfilesSnapshotSHA256:       strings.Repeat("1", 64),
		PromptFiles:                  map[string]string{"explain": "prompts/explain.txt"},
		PromptDigests:                map[string]string{"explain": strings.Repeat("2", 64)},
		CasePromptFiles:              map[string]string{"baseline-explain": "baseline-explain.user-prompt.txt"},
		CasePromptDigests:            map[string]string{"baseline-explain": strings.Repeat("3", 64)},
		MechanicalNavigationEnforced: mechanicalNavigation,
		AuthSourcePermission:         "deny-if-present",
	}
	config.MechanicalNavigationContract.RequiredRoot = "<worktree>"
	config.MechanicalNavigationContract.RequiredBaseCommit = "<resolved-base>"
	config.MechanicalNavigationContract.RequiredChangedReturn = "<profile-return>"
	config.MechanicalNavigationContract.RequiredChangedContext = "<profile-context>"
	config.MechanicalNavigationContract.RequireNavigation = "1"
	return config
}

func TestStrictGenerationConfigRequiresExactIsolation(t *testing.T) {
	if config := strictGenerationConfigFixture(true); !validStrictGenerationConfig(config, true) {
		t.Fatal("exact current strict generation config was rejected")
	}
	for _, testCase := range []struct {
		name   string
		mutate func(*qualityGenerationConfig)
	}{
		{
			name: "overriding codex config",
			mutate: func(config *qualityGenerationConfig) {
				config.CodexIsolationFlags = append(
					config.CodexIsolationFlags,
					"-c",
					`default_permissions="danger-full-access"`,
				)
			},
		},
		{
			name: "contradictory codex environment",
			mutate: func(config *qualityGenerationConfig) {
				config.CodexEnvironment = append(config.CodexEnvironment, "GOENV=/tmp/goenv")
			},
		},
		{
			name: "incomplete host Go environment",
			mutate: func(config *qualityGenerationConfig) {
				config.HostGoEnvironment = config.HostGoEnvironment[:len(config.HostGoEnvironment)-1]
			},
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			config := strictGenerationConfigFixture(true)
			testCase.mutate(&config)
			if validStrictGenerationConfig(config, true) {
				t.Fatal("strict generation validation accepted non-exact isolation")
			}
		})
	}
}

func TestStrictGenerationConfigMatchesRunScriptContract(t *testing.T) {
	manifest := map[string]any{
		"profiles_snapshot_sha256":                 strings.Repeat("1", 64),
		"mechanical_navigation_semantics_enforced": true,
		"prompt_files":                             map[string]string{"explain": "prompts/explain.txt"},
		"prompt_digests":                           map[string]string{"explain": strings.Repeat("2", 64)},
		"case_prompt_files":                        map[string]string{"baseline-explain": "baseline-explain.user-prompt.txt"},
		"case_prompt_digests":                      map[string]string{"baseline-explain": strings.Repeat("3", 64)},
	}
	currentBytes := runScriptGenerationConfig(t, manifest)
	var current qualityGenerationConfig
	if err := json.Unmarshal(currentBytes, &current); err != nil {
		t.Fatal(err)
	}
	if !validStrictGenerationConfig(current, true) {
		t.Fatal("suite strict isolation contract drifted from run.sh generation config")
	}

	manifest["mechanical_navigation_semantics_enforced"] = false
	baselineBytes := runScriptGenerationConfig(t, manifest)
	var baseline qualityGenerationConfig
	if err := json.Unmarshal(baselineBytes, &baseline); err != nil {
		t.Fatal(err)
	}
	if !validStrictGenerationConfig(baseline, false) {
		t.Fatal("suite baseline isolation contract drifted from run.sh generation config")
	}
	equivalent, err := equivalentGenerationConfigsExceptMechanicalNavigation(
		baselineBytes,
		currentBytes,
	)
	if err != nil {
		t.Fatal(err)
	}
	if !equivalent {
		t.Fatal("run.sh configs differ beyond their mechanical marker")
	}
}

func TestBaselineGenerationConfigMayDifferOnlyByMechanicalNavigation(t *testing.T) {
	current := strictGenerationConfigFixture(true)
	currentBytes, err := json.Marshal(current)
	if err != nil {
		t.Fatal(err)
	}
	for _, sourceMechanicalNavigation := range []bool{false, true} {
		t.Run(strconv.FormatBool(sourceMechanicalNavigation), func(t *testing.T) {
			baseline := strictGenerationConfigFixture(sourceMechanicalNavigation)
			baselineBytes, err := json.Marshal(baseline)
			if err != nil {
				t.Fatal(err)
			}
			equivalent, err := equivalentGenerationConfigsExceptMechanicalNavigation(
				baselineBytes,
				currentBytes,
			)
			if err != nil {
				t.Fatal(err)
			}
			if !equivalent {
				t.Fatal("source config was not equivalent after removing mechanical marker")
			}
			if !validStrictGenerationConfig(
				baseline,
				sourceMechanicalNavigation,
			) {
				t.Fatal("strict source generation config was rejected")
			}
		})
	}

	baseline := strictGenerationConfigFixture(false)
	baseline.CodexEnvironment = append(baseline.CodexEnvironment, "GOENV=/tmp/goenv")
	baselineBytes, err := json.Marshal(baseline)
	if err != nil {
		t.Fatal(err)
	}
	equivalent, err := equivalentGenerationConfigsExceptMechanicalNavigation(
		baselineBytes,
		currentBytes,
	)
	if err != nil {
		t.Fatal(err)
	}
	if equivalent {
		t.Fatal("baseline config drift beyond the mechanical marker was accepted")
	}
}

func loadRubricCriterion(t *testing.T, task, criterionID string) qualityCriterion {
	t.Helper()

	var rubric qualityRubric
	data, err := os.ReadFile(filepath.Join(
		"..", "..", "experiments", "lsp-replacement", "quality-rubric.json",
	))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, &rubric); err != nil {
		t.Fatal(err)
	}

	for _, criterion := range rubric.Tasks[task].Criteria {
		if criterion.ID == criterionID {
			return criterion
		}
	}
	t.Fatalf("%s %s criterion not found", task, criterionID)
	return qualityCriterion{}
}

func loadRubricPatterns(t *testing.T, task, criterionID string) []string {
	t.Helper()
	return loadRubricCriterion(t, task, criterionID).AllOf
}

func matchesAllPatterns(patterns []string, answer string) bool {
	for _, pattern := range patterns {
		if !regexp.MustCompile("(?is)" + pattern).MatchString(answer) {
			return false
		}
	}
	return true
}

func matchesCriterion(criterion qualityCriterion, answer string) bool {
	if !matchesAllPatterns(criterion.AllOf, answer) {
		return false
	}
	for _, pattern := range criterion.NoneOf {
		if regexp.MustCompile("(?is)" + pattern).MatchString(answer) {
			return false
		}
	}
	return true
}

func TestSimpleExplainThroughputRubricRequiresEveryMeasurement(t *testing.T) {
	criterion := loadRubricCriterion(t, "explain", "throughput_numbers")
	complete := "Activity improved 3.49%; write-admission improved 19.45%; read/drain improved 4.49%."
	if !matchesCriterion(criterion, complete) {
		t.Fatal("throughput rubric rejected all three measurements")
	}
	for _, omitted := range []string{"3.49%", "19.45%", "4.49%"} {
		incomplete := strings.ReplaceAll(complete, omitted, "an unreported amount")
		if matchesCriterion(criterion, incomplete) {
			t.Errorf("throughput rubric accepted answer without %s", omitted)
		}
	}
}

func TestSimpleExplainAllocationRubricAcceptsBothOrders(t *testing.T) {
	criterion := loadRubricCriterion(t, "explain", "allocation_numbers")
	for name, complete := range map[string]string{
		"count first": "The benchmark changed from 88 B/op and 2 allocations to 64 B/op and 1 allocation.",
		"label first": "Memory changed from 88 B/op to 64 B/op. Allocations: 2 to 1.",
	} {
		if !matchesCriterion(criterion, complete) {
			t.Errorf("allocation rubric rejected %s wording", name)
		}
	}
	for name, incomplete := range map[string]string{
		"old count": "Memory changed from 88 B/op to 64 B/op. Allocations fell to 1.",
		"new count": "Memory changed from 88 B/op to 64 B/op. Allocations started at 2.",
	} {
		if matchesCriterion(criterion, incomplete) {
			t.Errorf("allocation rubric accepted wording without %s", name)
		}
	}
}

func TestSimpleExplainWrapperAndConstructionRubric(t *testing.T) {
	wrapper := loadRubricCriterion(t, "explain", "wrapper_no_argument_semantics")
	construction := loadRubricCriterion(t, "explain", "concrete_construction_paths")
	semantics := `
The wrapper's no-argument Delay and Cancel methods previously consulted the
injected TimeSource, whereas the native underlying methods obtain time through
their own behavior. That semantic difference creates a compatibility risk.
`
	paths := `
Inspected construction paths include
common/persistence/client/health_request_rate_limiter.go,
service/matching/ratelimit_manager.go,
service/history/replication/stream_sender_flow_controller.go, and
service/worker/scheduler/fx.go.
`
	complete := semantics + paths
	if !matchesCriterion(wrapper, complete) {
		t.Fatal("wrapper rubric rejected injected-TimeSource versus native compatibility risk")
	}
	if !matchesCriterion(construction, complete) {
		t.Fatal("construction rubric rejected persistence health, matching, and replication paths")
	}
	labelOnlyPaths := `
Inspected construction paths include persistence health checks, matching,
replication flow control, and worker scheduling.
`
	if matchesCriterion(construction, semantics+labelOnlyPaths) {
		t.Fatal("construction rubric accepted category labels without concrete source paths")
	}

	if matchesCriterion(wrapper, paths) {
		t.Fatal("wrapper rubric accepted an answer that omitted wrapper clock semantics")
	}
	for name, omitted := range map[string]string{
		"persistence health": "common/persistence/client/health_request_rate_limiter.go",
		"matching":           "service/matching/ratelimit_manager.go",
		"replication":        "service/history/replication/stream_sender_flow_controller.go",
		"worker scheduling":  "service/worker/scheduler/fx.go",
	} {
		withoutCategory := strings.ReplaceAll(complete, omitted, "another subsystem")
		if matchesCriterion(construction, withoutCategory) {
			t.Errorf("construction rubric accepted an answer without %s", name)
		}
	}

	blanketEquivalence := complete + `
Delay and Cancel are always equivalent to the prior behavior.
`
	if !matchesAllPatterns(wrapper.AllOf, blanketEquivalence) {
		t.Fatal("blanket-equivalence fixture does not satisfy wrapper all_of patterns")
	}
	if matchesCriterion(wrapper, blanketEquivalence) {
		t.Fatal("wrapper rubric accepted a blanket Delay/Cancel equivalence claim")
	}
}

func TestSimpleReviewRubricRequiresScopedWrapperRisk(t *testing.T) {
	correctness := loadRubricCriterion(t, "review", "correctness_conclusion")
	wrapper := loadRubricCriterion(t, "review", "wrapper_clock_risk")
	construction := loadRubricCriterion(t, "review", "concrete_construction_paths")
	complete := `
Within the bounded, inspected production evidence, no demonstrated correctness
regression was found. A compatibility risk remains because the wrapper's Delay
and Cancel methods consulted the injected TimeSource while the native underlying
behavior obtains its own time. Concrete construction paths include
common/persistence/client/health_request_rate_limiter.go,
service/matching/ratelimit_manager.go,
service/history/replication/stream_sender_flow_controller.go, and
service/worker/scheduler/fx.go.
`
	for name, criterion := range map[string]qualityCriterion{
		"scoped correctness": correctness,
		"wrapper clock risk": wrapper,
		"construction paths": construction,
	} {
		if !matchesCriterion(criterion, complete) {
			t.Errorf("review rubric rejected complete %s evidence", name)
		}
	}

	oldBlanket := `
There is universally no correctness regression in production. Delay and Cancel
use the injected TimeSource, and their native underlying behavior is always equivalent,
so there is no compatibility risk. Construction paths include
persistence health checks, matching, replication flow control, and worker scheduling.
`
	if !matchesAllPatterns(wrapper.AllOf, oldBlanket) {
		t.Fatal("old blanket review fixture does not satisfy wrapper all_of patterns")
	}
	if matchesCriterion(wrapper, oldBlanket) {
		t.Fatal("review rubric accepted the old blanket universal equivalence conclusion")
	}
}

func TestSimpleRubricsRejectFalseCommandFreeClaim(t *testing.T) {
	explain := loadRubricCriterion(t, "explain", "affected_artifacts")
	explainAnswer := `
The RateLimiterImpl implementation, unit test, benchmark, and performance
documentation were inspected.
`
	if !matchesCriterion(explain, explainAnswer) {
		t.Fatal("explain rubric rejected a truthful artifact summary")
	}
	if !matchesCriterion(explain, explainAnswer+"\nNo tests were run.\n") {
		t.Fatal("explain rubric rejected a truthful tests-only disclaimer")
	}
	if !matchesCriterion(explain, strings.Replace(
		explainAnswer,
		"documentation were inspected",
		"documented results were inspected",
		1,
	)) {
		t.Fatal("explain rubric rejected documented-result adjective wording")
	}
	if matchesCriterion(explain, explainAnswer+"\nNo commands or tests were run.\n") {
		t.Fatal("explain rubric accepted a false command-free claim")
	}

	review := loadRubricCriterion(t, "review", "correctness_conclusion")
	reviewAnswer := `
Within the bounded inspected production evidence, a compatibility risk remains;
no demonstrated correctness regression was found.
`
	if !matchesCriterion(review, reviewAnswer) {
		t.Fatal("review rubric rejected a scoped correctness conclusion")
	}
	if !matchesCriterion(review, reviewAnswer+"\nNo tests were run.\n") {
		t.Fatal("review rubric rejected a truthful tests-only disclaimer")
	}
	if matchesCriterion(review, reviewAnswer+"\nNo commands were run.\n") {
		t.Fatal("review rubric accepted a false command-free claim")
	}
}

func TestSimpleRubricsRequireBoundedInterfacesOperationsAndConsumers(t *testing.T) {
	complete := `
The upstream native dependency source was not inspected, so the evidence
boundary cannot prove its exact clock calls. The public RateLimiter and
Reservation interface method sets and the NewRateLimiter and
NewDynamicRateLimiter constructor signatures remain unchanged. Allow and
AllowN, Wait and WaitN, recycling, rate and burst configuration, and TokensAt
token inspection remain unchanged. Proven consuming paths include scheduler
and per-namespace Reserve calls plus request adapter and persistence health
delegation to ReserveN.
`
	criteria := []string{
		"native_source_boundary",
		"interface_constructor_scope",
		"unchanged_operations",
		"concrete_consuming_paths",
	}
	for _, task := range []string{"explain", "review"} {
		for _, criterionID := range criteria {
			criterion := loadRubricCriterion(t, task, criterionID)
			if !matchesCriterion(criterion, complete) {
				t.Errorf("%s %s rubric rejected complete bounded evidence", task, criterionID)
			}
		}
	}

	omissions := map[string]string{
		"native_source_boundary":      "The upstream native dependency source was not inspected, so the evidence\nboundary cannot prove its exact clock calls.",
		"interface_constructor_scope": "The public RateLimiter and\nReservation interface method sets and the NewRateLimiter and\nNewDynamicRateLimiter constructor signatures remain unchanged.",
		"unchanged_operations":        "Allow and\nAllowN, Wait and WaitN, recycling, rate and burst configuration, and TokensAt\ntoken inspection remain unchanged.",
		"concrete_consuming_paths":    "Proven consuming paths include scheduler\nand per-namespace Reserve calls plus request adapter and persistence health\ndelegation to ReserveN.",
	}
	for criterionID, sentence := range omissions {
		withoutCategory := strings.ReplaceAll(complete, sentence, "")
		for _, task := range []string{"explain", "review"} {
			criterion := loadRubricCriterion(t, task, criterionID)
			if matchesCriterion(criterion, withoutCategory) {
				t.Errorf("%s %s rubric accepted omitted evidence", task, criterionID)
			}
		}
	}
}

func TestDeepExplainProductionPathsRubricAcceptsDirectPath(t *testing.T) {
	criterion := loadRubricCriterion(t, "deep-explain", "production_paths")
	complete := `
Exactly three production paths:
Direct RateLimiterImpl construction and consumption.
DynamicRateLimiterImpl construction, delegation, and consumption.
RequestRateLimiterAdapterImpl construction, delegation, and consumption.
`
	if !matchesCriterion(criterion, complete) {
		t.Fatal("complete direct/dynamic/adapter path evidence did not match")
	}
	incomplete := `
DynamicRateLimiterImpl construction, delegation, and consumption.
RequestRateLimiterAdapterImpl construction, delegation, and consumption.
`
	if matchesCriterion(criterion, incomplete) {
		t.Fatal("rubric accepted production paths without a direct path")
	}
	unaffected := complete + `
This selected adapter path does not receive the native reservation improvement.
`
	if matchesCriterion(criterion, unaffected) {
		t.Fatal("rubric accepted an explicitly unaffected selected adapter path")
	}
	unresolved := complete + `
The adapter's final constructor injection edge remains unresolved.
`
	if matchesCriterion(criterion, unresolved) {
		t.Fatal("rubric accepted a selected adapter path with an unresolved edge")
	}
	externalUnknown := complete + `
External consumers remain unresolved.
`
	if !matchesCriterion(criterion, externalUnknown) {
		t.Fatal("rubric confused external risk with an unresolved production edge")
	}
}

func TestDeepExplainPerformanceRubricAcceptsNounForms(t *testing.T) {
	patterns := loadRubricPatterns(t, "deep-explain", "performance_evidence")
	complete := `
Measurements: 88 B/op became 64 B/op; throughput changed by 3.49%,
19.45%, and 4.49%. Inference is limited because raw commands, samples,
variance, and confidence intervals are unavailable.
`
	if !matchesAllPatterns(patterns, complete) {
		t.Fatal("measurement and inference noun forms did not match")
	}
	withoutInference := `
Measurements: 88 B/op became 64 B/op; throughput changed by 3.49%,
19.45%, and 4.49%. Raw commands, samples, and variance are unavailable.
`
	if matchesAllPatterns(patterns, withoutInference) {
		t.Fatal("rubric accepted performance evidence without inference")
	}
}

func TestDeepClockSemanticsRubricRequiresPinnedVersionAndReserveClock(t *testing.T) {
	completeAnswers := []string{`
The time source is RealTimeSource.Now, which calls time.Now().UTC().
Explicit time remains under DelayFrom and CancelAt. UTC strips monotonic data.
The dependency pinned by go.mod is golang.org/x/time v0.14.0, so Reserve()
remains wall-clock based.
`, `
The time source is RealTimeSource.Now, which calls time.Now().UTC().
Explicit time remains under DelayFrom and CancelAt. UTC strips monotonic data.
The dependency pinned by go.mod is golang.org/x/time v0.14.0.
RateLimiterImpl.Reserve anchors through UTC, so later comparisons use wall time.
`, `
The time source is RealTimeSource.Now, which calls time.Now().UTC().
Explicit time remains under DelayFrom and CancelAt. UTC strips monotonic data.
Upstream ` + "`golang.org/x/time`" + ` is pinned to **v0.14.0** in go.mod.
RateLimiterImpl.Reserve anchors through UTC, so later comparisons use wall time.
`, `
The time source is RealTimeSource.Now, which calls time.Now().UTC().
Explicit time remains under DelayFrom and CancelAt. Time.UTC delegates to
setLoc; therefore UTC strips monotonic data.
The dependency pinned by go.mod is golang.org/x/time v0.14.0.
RateLimiterImpl.Reserve anchors through UTC, so later comparisons use wall time.
`, `
The time source is RealTimeSource.Now, which calls time.Now().UTC().
Explicit time remains under DelayFrom and CancelAt. Time.UTC calls setLoc,
setLoc calls stripMono, removing the monotonic reading.
The dependency pinned by go.mod is golang.org/x/time v0.14.0.
RateLimiterImpl.Reserve anchors through UTC, so later comparisons use wall time.
`, `
The time source is RealTimeSource.Now, which calls time.Now().UTC().
Explicit time remains under DelayFrom and CancelAt. Time.UTC delegates to
setLoc [time.go:217](/a/very/long/module/cache/path/that/represents/a/citation/to/the/standard/library/time/source/file/and/makes/the/evidence/chain/longer).
setLoc calls stripMono [time.go:226](/another/very/long/module/cache/path/that/represents/a/citation/to/the/standard/library/time/source/file/and/makes/the/evidence/chain/longer),
which removes the monotonic reading.
The dependency pinned by go.mod is golang.org/x/time v0.14.0.
RateLimiterImpl.Reserve anchors through UTC, so later comparisons use wall time.
`}
	for _, task := range []string{"deep-explain", "deep-review"} {
		criterion := loadRubricCriterion(t, task, map[string]string{
			"deep-explain": "clock_semantics",
			"deep-review":  "semantic_matrix",
		}[task])
		for _, complete := range completeAnswers {
			taskAnswer := complete
			if task == "deep-review" {
				taskAnswer += `
OK Delay DelayFrom Cancel CancelAt.
Rejected reservations return InfDuration and cancellation is a no-op.
`
			}
			if !matchesCriterion(criterion, taskAnswer) {
				t.Fatalf("%s clock semantics did not match complete evidence", task)
			}
			for name, incomplete := range map[string]string{
				"wrong dependency": strings.ReplaceAll(taskAnswer, "v0.14.0", "v0.12.0"),
				"missing manifest": strings.ReplaceAll(taskAnswer, "go.mod", "a cache path"),
				"wrong method": strings.NewReplacer(
					"Reserve()", "ReserveN(now, n)",
					".Reserve ", ".ReserveN ",
				).Replace(taskAnswer),
			} {
				if matchesCriterion(criterion, incomplete) {
					t.Errorf("%s rubric accepted %s evidence", task, name)
				}
			}
			soleTimestamp := taskAnswer + `
Explicit DelayFrom and CancelAt depend solely on the supplied timestamp.
`
			if matchesCriterion(criterion, soleTimestamp) {
				t.Errorf("%s rubric accepted sole-timestamp semantics", task)
			}
			correctNegation := taskAnswer + `
Explicit DelayFrom and CancelAt do not depend solely on the supplied timestamp.
`
			if !matchesCriterion(criterion, correctNegation) {
				t.Errorf("%s rubric rejected correct sole-timestamp negation", task)
			}
			wrongUTC := `
The time source is RealTimeSource.Now, which calls time.Now().UTC().
Explicit time remains under DelayFrom and CancelAt.
UTC changes location without removing monotonic data.
The dependency pinned by go.mod is golang.org/x/time v0.14.0.
RateLimiterImpl.Reserve anchors through UTC, so later comparisons use wall time.
`
			if task == "deep-review" {
				wrongUTC += `
OK Delay DelayFrom Cancel CancelAt.
Rejected reservations return InfDuration and cancellation is a no-op.
`
			}
			if matchesCriterion(criterion, wrongUTC) {
				t.Errorf("%s rubric accepted incorrect UTC monotonic semantics", task)
			}
		}
	}
}

func TestDeepExplainReservationRubricRequiresRejectedBehavior(t *testing.T) {
	criterion := loadRubricCriterion(
		t,
		"deep-explain",
		"reservation_contract_matrix",
	)
	complete := `
OK false makes Delay and DelayFrom return InfDuration. Cancel and CancelAt
are a no-op for a rejected reservation.
`
	if !matchesCriterion(criterion, complete) {
		t.Fatal("reservation rubric rejected complete rejected behavior")
	}
	for name, incomplete := range map[string]string{
		"missing infinite delay": strings.ReplaceAll(
			complete,
			"InfDuration",
			"a sentinel",
		),
		"missing no-op cancellation": strings.ReplaceAll(
			complete,
			"a no-op",
			"handled",
		),
	} {
		if matchesCriterion(criterion, incomplete) {
			t.Errorf("reservation rubric accepted %s", name)
		}
	}
}

func TestDeepReviewBehaviorRubricRejectsFalseReaderCoverageNegative(t *testing.T) {
	criterion := loadRubricCriterion(t, "deep-review", "behavior_test_matrix")
	complete := `
Immediate and rejected over-burst behavior, delayed behavior, cancellation
refund, explicit time through DelayFrom and CancelAt, and upstream x/time
rate_test.go coverage were reviewed.
Local fake-clock Wait/WaitN tests cover delayed waits, deadline and context
cancellation, timer advancement, and recycle/no-recycle behavior.
No changed-path test covers wall-clock jumps or the monotonic-time distinction.
`
	if !matchesCriterion(criterion, complete) {
		t.Fatal("complete behavior matrix did not match")
	}
	falseNegative := complete + `
The reader test path contained no Wait reference and no real affected adapter
path is tested end-to-end.
`
	if matchesCriterion(criterion, falseNegative) {
		t.Fatal("rubric accepted false reader affected-path coverage claim")
	}
	falseDynamic := complete + `
No selected direct-scheduler or dynamic-worker caller test hit.
`
	if matchesCriterion(criterion, falseDynamic) {
		t.Fatal("rubric accepted false dynamic-worker caller coverage claim")
	}
}

func TestDeepBehaviorRubricsAcceptDistributedTestEvidence(t *testing.T) {
	common := `
Existing event-clock coverage includes an immediate Wait and a delayed Wait
completed after EventTimeSource.Advance. A canceled Wait covers context
interruption, and a separate test covers deadline rejection. Wait recycling is
covered. WaitN is explicitly not recycling a single token and exits on cancellation.
No repository test covers the wall-clock versus monotonic distinction.
`
	explain := common + `
Zero delay and delayed behavior are covered, as are Cancel and refund paths.
Allocation regression is measured but not enforced by an assertion.
`
	if criterion := loadRubricCriterion(t, "deep-explain", "test_coverage_matrix"); !matchesCriterion(criterion, explain) {
		t.Fatal("deep-explain test matrix rejected distributed evidence")
	}
	review := common + `
Immediate and rejected over-burst behavior, delayed behavior, cancellation and
refund, and explicit time through DelayFrom and CancelAt were reviewed. The
upstream x/time rate_test.go coverage was also reviewed.
`
	if criterion := loadRubricCriterion(t, "deep-review", "behavior_test_matrix"); !matchesCriterion(criterion, review) {
		t.Fatal("deep-review behavior matrix rejected distributed evidence")
	}
}

func TestDeepReviewSemanticRubricAcceptsNaturalRejectedAndUTCWording(t *testing.T) {
	answer := `
OK, Delay, DelayFrom, Cancel, and CancelAt are in the semantic matrix. A
rejected reservation has InfDuration; Cancel and CancelAt do nothing. RealTimeSource is
the time source and uses real time.Now().UTC() through the wrapper, stripping
the monotonic component. Explicit time remains under DelayFrom and CancelAt.
go.mod pins golang.org/x/time v0.14.0. Reserve() starts from wall-only time.
`
	criterion := loadRubricCriterion(t, "deep-review", "semantic_matrix")
	if !matchesCriterion(criterion, answer) {
		t.Fatal("semantic matrix rejected natural rejected/UTC wording")
	}
}

func TestDeepRubricsRejectUnsupportedGoEnvFailure(t *testing.T) {
	for task, criterionID := range map[string]string{
		"deep-explain": "contract_sources",
		"deep-review":  "source_references",
	} {
		criterion := loadRubricCriterion(t, task, criterionID)
		claim := "One go env attempt tried toolchain verification and failed."
		matched := false
		for _, pattern := range criterion.NoneOf {
			if regexp.MustCompile("(?is)" + pattern).MatchString(claim) {
				matched = true
				break
			}
		}
		if !matched {
			t.Errorf("%s rubric did not reject unsupported go env failure", task)
		}
	}
}

func TestDeepReviewInterfaceRubricAcceptsMethodSetWording(t *testing.T) {
	criterion := loadRubricCriterion(t, "deep-review", "interface_conformance")
	complete := `
The producer returns Reservation, whose method set still satisfies the interface.
The concrete type changes to ClockedReservation, so external consumers using
type assertions retain an unresolved risk.
`
	if !matchesCriterion(criterion, complete) {
		t.Fatal("interface rubric rejected Go method-set wording")
	}
	withoutExternalRisk := strings.ReplaceAll(
		complete,
		"external consumers",
		"repository callers",
	)
	if matchesCriterion(criterion, withoutExternalRisk) {
		t.Fatal("interface rubric accepted evidence without external-consumer risk")
	}
}

func TestBatchedNavigationPolicyRequiresConnectedProductionPaths(t *testing.T) {
	policy, err := os.ReadFile(filepath.Join(
		"..", "..", "scripts", "codex-with-repo-view",
	))
	if err != nil {
		t.Fatal(err)
	}
	for _, requirement := range []string{
		"same-subsystem edge ledger",
		"exactly these symbols: RateLimiter, Reservation, ClockedReservation, RateLimiterImpl, ClockedRateLimiter, DynamicRateLimiterImpl, TimeSource, and RealTimeSource",
		"ClockedReservationImpl does not exist",
		"including a sibling method",
		"concrete production caller invocation",
		"whether tests exercise real direct, dynamic, and adapter or health paths end to end",
		"whether cancellation or refund tests assert restored quota or tokens",
		"Search tests for the concrete production caller selected in every path",
		"Never generalize from one subsystem test search",
		"at 12 or less, freeze production-path selection",
		"Target 26 or fewer repo-view invocations",
		"phase ceilings",
		"Do not use outline for test discovery",
		"TestClockedRateLimiter_Wait_Ok, TestClockedRateLimiter_Wait_Canceled",
		"TestClockedRateLimiter_Wait_DeadlineWouldExceed, TestClockedRateLimiter_Wait_Recycle, TestClockedRateLimiter_WaitN_NoRecycle",
		"Then search DelayFrom and CancelAt together",
		"Reserve two test-phase calls for reader_test.go",
		"Use the sixth test-phase call for AllocsPerRun, ReportAllocs, and BenchmarkRateLimiterReserve",
		"Never infer reader-path absence from a multi-path search",
		"Never say no dynamic-worker caller test was hit",
		"use this exact standalone sentence",
		"time.Time.UTC strips the monotonic reading: UTC delegates to setLoc, and setLoc calls stripMono.",
		"No changed-path test covers wall-clock jumps or the monotonic-time distinction.",
		"Local fake-clock Wait/WaitN tests cover delayed waits, deadline and context cancellation, timer advancement, and recycle/no-recycle behavior.",
		"never use 0 as a sentinel",
		"use --return locations instead of --max-code-lines 0",
		"at most this exact six-call adapter sequence",
		"Do not probe arbitrary line numbers",
		"search common/quotas constructors after reader_quotas.go already shows the nested adapter/dynamic construction",
		"do not defer either check",
		"Never connect scheduler_quotas.go construction to queue_scheduled.go readerRateLimiter",
		"Carry the manifest citation and exact selected version into the final answer",
		"both explanation and review must state that go.mod pins golang.org/x/time v0.14.0",
		"Never call evidence complete when a cited response still reports code_truncated or results_truncated",
		"do not describe it as completely retrieved unless a later response clears both truncation flags",
		"ReaderImpl.loadAndSubmitTasks -> ratelimiter.Wait -> MultiRequestRateLimiterImpl.Wait/Reserve",
		"ReaderImpl has no Wait method",
		"reject it immediately and switch paths",
		"local priority and multi-reservation tests for DelayFrom aggregation and CancelAt delegation",
		"never say their behavior depends solely on the supplied timestamp",
		"Delay returns InfDuration, and cancellation is a no-op",
		"Do not read or invoke Codex skills, plugins, hooks, or marketplace resources",
	} {
		if !strings.Contains(string(policy), requirement) {
			t.Errorf("batched navigation policy missing %q", requirement)
		}
	}
}

func TestQualityCheckUsesCurrentJudgeFormat(t *testing.T) {
	content, err := os.ReadFile(filepath.Join(
		"..", "..", "experiments", "lsp-replacement", "quality-check.sh",
	))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(content), "$criterion.none_of") {
		t.Fatal("quality check does not enforce rubric none_of patterns")
	}
	for _, requirement := range []string{
		"judge_attempt_limit=3",
		"judge_output_valid",
		"judge_input_digest",
		"judge_cache_valid",
		"retain_invalid_judge",
		`stem="judge-${task}-${repeat}"`,
		`"${parked_repeat}" =~ ^[1-9][0-9]*$`,
		"inputs.sha256",
		".task == $task",
		"never emit zero placeholder scores",
		"Never concatenate multiple evaluator inputs into one command output",
		"Read each baseline and candidate answer through EOF before scoring",
		"search the candidate answer directly for each supposedly missing concept",
		"A correct candidate correction of a baseline error is a candidate_material_addition, never a material_contradiction",
		"never against the baseline's length or exploratory breadth",
		"unless the shared task prompt or rubric expressly requires a proven consuming chain",
		"without equally strong substitute coverage",
		"schema_version: 5",
		"model_mode: $judge_model_mode",
		"judge_cache_schema=8",
		"result.sha256",
		"aggregate-manifest.json",
		"export LC_ALL=C",
		"--ignore-user-config",
		"--ignore-rules",
		"project_doc_max_bytes=0",
		"project_doc_fallback_filenames=[]",
		"mcp_servers={}",
		"apps._default.enabled=false",
		"--disable hooks",
		`default_permissions="quality-audit"`,
		`":root"="deny"`,
		`shell_environment_policy.inherit="none"`,
		`GOMODCACHE`,
		`canonical-auth>=deny`,
	} {
		if !strings.Contains(string(content), requirement) {
			t.Errorf("quality check missing judge input requirement %q", requirement)
		}
	}
	if strings.Contains(string(content), "mapfile -d") {
		t.Fatal("quality check requires Bash 4.4 mapfile -d")
	}
	for _, staleGlob := range []string{
		`-name "judge-*-*.json"`,
		`/quality/judge-*-*.jsonl`,
	} {
		if strings.Contains(string(content), staleGlob) {
			t.Errorf("quality check still aggregates stale judge artifacts with %q", staleGlob)
		}
	}
	if strings.Contains(
		string(content),
		"and (if $judge == null then true else $judge.all_core_conclusion_match end)",
	) {
		t.Fatal("quality gate still rejects correct baseline conclusion fixes")
	}
}

func TestDeepQualityRubricRequiresMonotonicTestGap(t *testing.T) {
	answers := map[string]string{
		"deep-explain": `
Immediate reservations have zero delay. Delayed behavior and cancel/refund are
covered upstream. WaitN indirectly calls Delay and Cancel. Allocation regression
is not enforced by an assertion.
Local fake-clock Wait/WaitN tests cover delayed waits, deadline and context
cancellation, timer advancement, and recycle/no-recycle behavior.
No changed-path test covers wall-clock jumps or the monotonic-time distinction.
`,
		"deep-review": `
Immediate and rejected over-burst behavior are covered. Delayed cancellation
and refund behavior use explicit time through DelayFrom and CancelAt. Upstream
x/time tests cover those operations.
Local fake-clock Wait/WaitN tests cover delayed waits, deadline and context
cancellation, timer advancement, and recycle/no-recycle behavior.
No changed-path test covers wall-clock jumps or the monotonic-time distinction.
`,
	}
	criteria := map[string]string{
		"deep-explain": "test_coverage_matrix",
		"deep-review":  "behavior_test_matrix",
	}
	for task, answer := range answers {
		criterion := loadRubricCriterion(t, task, criteria[task])
		if !matchesCriterion(criterion, answer) {
			t.Fatalf("%s rubric rejected explicit monotonic test gap", task)
		}
		withoutGap := strings.ReplaceAll(
			answer,
			"No changed-path test covers wall-clock jumps or the monotonic-time distinction.",
			"",
		)
		if matchesCriterion(criterion, withoutGap) {
			t.Fatalf("%s rubric accepted missing monotonic test gap", task)
		}
		withoutWrapperCoverage := strings.ReplaceAll(
			answer,
			"Local fake-clock Wait/WaitN tests cover delayed waits, deadline and context\ncancellation, timer advancement, and recycle/no-recycle behavior.",
			"",
		)
		if matchesCriterion(criterion, withoutWrapperCoverage) {
			t.Fatalf("%s rubric accepted missing local wrapper coverage", task)
		}
	}
}

func TestJudgeSchemaRejectsZeroPlaceholderScores(t *testing.T) {
	content, err := os.ReadFile(filepath.Join(
		"..", "..", "experiments", "lsp-replacement", "quality-output-schema.json",
	))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(content), `"minimum": 0`) {
		t.Fatal("judge schema still accepts zero placeholder scores")
	}
	if got := strings.Count(string(content), `"minimum": 1`); got != 8 {
		t.Fatalf("judge schema score minimum count = %d, want 8", got)
	}
}

func TestExperimentCodexInvocationsForbidPluginReads(t *testing.T) {
	requirement := "Do not read or invoke Codex skills, plugins, hooks, or marketplace resources"
	for _, path := range []string{
		filepath.Join("..", "..", "scripts", "codex-with-repo-view"),
		filepath.Join("..", "..", "experiments", "lsp-replacement", "run.sh"),
		filepath.Join("..", "..", "experiments", "lsp-replacement", "quality-check.sh"),
	} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(content), requirement) {
			t.Errorf("%s missing benchmark isolation requirement", path)
		}
	}
}

func TestValidateEvidence(t *testing.T) {
	runDir := t.TempDir()
	writeSourceChecksumFixture(t, runDir, "")
	for _, directory := range []string{
		"answers",
		"commands",
		"tool-stats",
		"call-graphs",
		"quality",
	} {
		if err := os.MkdirAll(filepath.Join(runDir, directory), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for relative, content := range map[string]string{
		"optimized-test.jsonl":        "{}\n",
		"optimized-test.exit-code":    "0\n",
		"answers/optimized-test.md":   "answer\n",
		"commands/optimized-test.txt": "command\n",
	} {
		if err := os.WriteFile(
			filepath.Join(runDir, filepath.FromSlash(relative)),
			[]byte(content),
			0o644,
		); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{
		"call-graphs/case.dot",
		"call-graphs/case.md",
	} {
		if err := os.WriteFile(filepath.Join(runDir, path), []byte("{}"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeJSON(t, filepath.Join(runDir, "tool-stats/case.json"), map[string]any{
		"total_tool_calls":            3,
		"temporal_edge_count":         2,
		"output_reference_edge_count": 0,
		"call_graph": map[string]any{
			"nodes": []any{map[string]any{}, map[string]any{}, map[string]any{}},
			"edges": []any{
				map[string]any{"kind": "next_tool_call"},
				map[string]any{"kind": "next_tool_call"},
			},
		},
	})
	writeJSON(t, filepath.Join(runDir, "metrics.json"), map[string]any{
		"schema_version": 2,
		"formula":        qualityMetricsFormula,
		"cases": []any{map[string]any{
			"name":                                     "optimized-test",
			"answer_file":                              "answers/optimized-test.md",
			"commands_file":                            "commands/optimized-test.txt",
			"tool_call_count":                          3,
			"repo_view_tool_call_count":                2,
			"other_tool_call_count":                    1,
			"temporal_tool_edge_count":                 2,
			"output_reference_edge_count":              0,
			"tool_call_accounting_valid":               true,
			"repo_view_invocation_accounting_valid":    true,
			"repo_view_tool_call_accounting_valid":     true,
			"repo_view_budget_accounting_valid":        true,
			"repo_view_command_shape_valid":            true,
			"repo_view_first_invocation_changed":       false,
			"repo_view_navigation_semantics_valid":     true,
			"mechanical_navigation_semantics_enforced": true,
			"repo_view_changed_invocation_count":       0,
			"repo_view_invocation_cap":                 0,
			"tool_stats_file":                          "tool-stats/case.json",
			"call_graph_dot_file":                      "call-graphs/case.dot",
			"call_graph_markdown_file":                 "call-graphs/case.md",
		}},
		"comparisons": []any{map[string]any{
			"task":                        "explain",
			"effective_reduction_percent": 25,
		}},
	})
	writeQualityAggregateManifest(t, runDir)

	checks := ValidateEvidence(runDir, []Assertion{{
		Description: "tokens improve",
		Source:      "metrics.comparison",
		Selector:    map[string]any{"task": "explain"},
		Field:       "effective_reduction_percent",
		Operator:    "gt",
		Value:       0,
	}})
	if !Passed(checks) {
		t.Fatalf("checks failed: %+v", checks)
	}

	markerDigest := setQualityAggregateStrictEvidence(t, runDir, false)
	checks = ValidateTrackedEvidence(runDir, nil, "", markerDigest)
	if Passed(checks) ||
		!checkResultsContainError(checks, `want "strict-current"`) {
		t.Fatalf("promoted non-strict aggregate checks = %+v", checks)
	}
	checks = ValidateRejectedTrackedEvidence(runDir, nil, "", markerDigest)
	if !Passed(checks) {
		t.Fatalf("rejected fixture did not accept non-strict aggregate: %+v", checks)
	}
}

func checkResultsContainError(checks []CheckResult, fragment string) bool {
	for _, check := range checks {
		if strings.Contains(check.Error, fragment) {
			return true
		}
	}
	return false
}

func TestValidateEvidenceDetectsAccountingMismatch(t *testing.T) {
	runDir := t.TempDir()
	writeSourceChecksumFixture(t, runDir, "")
	writeJSON(t, filepath.Join(runDir, "metrics.json"), map[string]any{
		"cases": []any{map[string]any{
			"name":                                     "bad",
			"tool_call_count":                          3,
			"repo_view_tool_call_count":                2,
			"other_tool_call_count":                    2,
			"temporal_tool_edge_count":                 2,
			"output_reference_edge_count":              0,
			"tool_call_accounting_valid":               false,
			"repo_view_invocation_accounting_valid":    true,
			"repo_view_tool_call_accounting_valid":     true,
			"repo_view_budget_accounting_valid":        true,
			"repo_view_command_shape_valid":            true,
			"repo_view_first_invocation_changed":       false,
			"repo_view_navigation_semantics_valid":     true,
			"mechanical_navigation_semantics_enforced": true,
			"repo_view_changed_invocation_count":       0,
			"repo_view_invocation_cap":                 0,
		}},
	})
	checks := validateToolAccounting(runDir)
	for _, check := range checks {
		if check.Description != "tool accounting and call graph: bad" {
			continue
		}
		if check.Passed {
			t.Fatalf("accounting mismatch check passed: %+v", check)
		}
		if !strings.Contains(check.Error, "total != repo-view + other") {
			t.Fatalf("accounting mismatch error = %q", check.Error)
		}
		return
	}
	t.Fatal("validateToolAccounting did not report the accounting check")
}

func TestSelectCases(t *testing.T) {
	manifest := Manifest{Cases: []Case{
		{ID: "simple", Level: 1},
		{ID: "deep", Level: 3},
	}}
	selected, err := SelectCases(manifest, nil, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected[0].ID != "simple" {
		t.Fatalf("selected = %+v", selected)
	}
}

func TestSelectResolutions(t *testing.T) {
	cases := []Case{
		{ID: "accepted", Outcome: "accepted"},
		{ID: "rejected", Outcome: "rejected"},
	}
	manifest := ResolutionManifest{Cases: []ResolutionCase{
		{ID: "rejected", Status: "resolved"},
		{ID: "accepted", Status: "accepted"},
	}}
	selected, err := SelectResolutions(manifest, cases)
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 2 ||
		selected[0].ID != "accepted" ||
		selected[1].ID != "rejected" {
		t.Fatalf("selected = %+v", selected)
	}

	manifest.Cases = manifest.Cases[:1]
	if _, err := SelectResolutions(manifest, cases); err == nil {
		t.Fatal("expected missing resolution error")
	}
}

func TestAnswerAndStaticCriterionSources(t *testing.T) {
	runDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(runDir, "answers"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(runDir, "quality"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(runDir, "answers", "candidate.md"),
		[]byte("pinned golang.org/x/time v0.14.0"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(runDir, "metrics.json"), map[string]any{
		"cases": []any{map[string]any{
			"name":        "candidate",
			"task":        "deep-review",
			"profile":     "verified",
			"answer_file": "answers/candidate.md",
		}},
	})
	writeJSON(t, filepath.Join(runDir, "quality", "static.json"), map[string]any{
		"cases": []any{map[string]any{
			"name":    "candidate",
			"task":    "deep-review",
			"profile": "verified",
			"variant": "optimized",
			"criteria": []any{map[string]any{
				"id":     "contract_sources",
				"passed": true,
			}},
		}},
	})

	answers, err := loadSource(runDir, "answer")
	if err != nil {
		t.Fatal(err)
	}
	actual, err := selectField(
		answers,
		map[string]any{"name": "candidate"},
		"content",
	)
	if err != nil || actual != "pinned golang.org/x/time v0.14.0" {
		t.Fatalf("answer = %v, err = %v", actual, err)
	}

	criteria, err := loadSource(runDir, "quality.static_criterion")
	if err != nil {
		t.Fatal(err)
	}
	actual, err = selectField(
		criteria,
		map[string]any{"name": "candidate", "id": "contract_sources"},
		"passed",
	)
	if err != nil || actual != true {
		t.Fatalf("criterion = %v, err = %v", actual, err)
	}
}

func TestSummarizeEvidence(t *testing.T) {
	runDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(runDir, "quality"), 0o755); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(runDir, "metrics.json"), map[string]any{
		"schema_version": 2,
		"formula":        qualityMetricsFormula,
		"cases": []any{map[string]any{
			"name":                        "candidate",
			"task":                        "deep-review",
			"variant":                     "optimized",
			"profile":                     "verified",
			"completed":                   false,
			"regular_input_tokens":        100,
			"cached_input_tokens":         200,
			"output_tokens":               30,
			"effective_tokens":            150,
			"tool_call_count":             4,
			"repo_view_tool_call_count":   3,
			"other_tool_call_count":       1,
			"repo_view_invocation_count":  3,
			"temporal_tool_edge_count":    3,
			"output_reference_edge_count": 2,
			"answer_file":                 "answers/candidate.md",
			"commands_file":               "commands/candidate.txt",
			"tool_stats_file":             "tool-stats/candidate.json",
			"call_graph_dot_file":         "call-graphs/candidate.dot",
			"call_graph_markdown_file":    "call-graphs/candidate.md",
			"tool_types": []any{map[string]any{
				"name":              "command_execution",
				"tool_calls":        4,
				"invocations":       4,
				"output_characters": 500,
			}},
			"operations": []any{map[string]any{
				"name":              "repo-view.find",
				"tool_calls":        3,
				"invocations":       3,
				"output_characters": 400,
			}},
		}},
		"comparisons": []any{map[string]any{
			"task":                        "deep-review",
			"profile":                     "verified",
			"baseline_effective_tokens":   300,
			"effective_reduction_percent": 50,
		}},
	})
	for relative, content := range map[string]string{
		"answers/candidate.md":      "candidate answer\n",
		"commands/candidate.txt":    "repo-view find symbol\n",
		"tool-stats/candidate.json": "{}\n",
		"call-graphs/candidate.dot": "digraph {}\n",
		"call-graphs/candidate.md":  "# graph\n",
		"candidate.jsonl":           "{}\n",
		"candidate.exit-code":       "0\n",
	} {
		fullPath := filepath.Join(runDir, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	writeJSON(t, filepath.Join(runDir, "quality", "static.json"), map[string]any{
		"schema_version": 1,
		"cases":          []any{},
		"comparisons":    []any{},
	})
	writeJSON(t, filepath.Join(runDir, "quality", "judges.json"), map[string]any{
		"provenance_status": "strict-current",
		"judge_runs":        []any{},
		"baselines":         []any{},
		"candidates":        []any{},
	})
	writeQualityAggregateManifest(t, runDir)

	summaries, err := SummarizeEvidence(runDir, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(summaries) != 1 {
		t.Fatalf("summaries = %+v", summaries)
	}
	got := summaries[0]
	if got.TotalToolCalls != 4 ||
		got.RepoViewToolCalls != 3 ||
		got.OtherToolCalls != 1 ||
		!got.HasComparison ||
		got.EffectiveReductionPercent != 50 ||
		got.StaticScorePercent != 0 ||
		got.JudgeStatus != "" ||
		got.JudgeCoreConclusionMatch {
		t.Fatalf("summary = %+v", got)
	}

	selected, err := SummarizeEvidence(runDir, []string{"candidate"})
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected[0].Name != "candidate" {
		t.Fatalf("selected summaries = %+v", selected)
	}

	markerPath := filepath.Join(runDir, "quality", "aggregate-manifest.json")
	trackedDigest, err := sha256File(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := SummarizeTrackedEvidence(
		runDir,
		nil,
		trackedDigest,
	); err != nil {
		t.Fatalf("tracked summary failed: %v", err)
	}
	metricsPath := filepath.Join(runDir, "metrics.json")
	metricsContent, err := os.ReadFile(metricsPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(metricsPath, append(metricsContent, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := SummarizeEvidence(runDir, nil); err == nil ||
		!strings.Contains(err.Error(), "does not match committed snapshot") {
		t.Fatalf("mutated input error = %v", err)
	}
	if err := os.WriteFile(metricsPath, metricsContent, 0o644); err != nil {
		t.Fatal(err)
	}
	generatorPath := filepath.Join(
		runDir,
		filepath.FromSlash(
			qualityGeneratorBundlePath(
				"experiments/lsp-replacement/quality-check.sh",
			),
		),
	)
	generatorContent, err := os.ReadFile(generatorPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(generatorPath, []byte("tampered\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := SummarizeEvidence(runDir, nil); err == nil ||
		!strings.Contains(err.Error(), "does not match committed snapshot") {
		t.Fatalf("mutated evaluator source error = %v", err)
	}
	if err := os.WriteFile(generatorPath, generatorContent, 0o644); err != nil {
		t.Fatal(err)
	}
	markerContent, err := os.ReadFile(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(markerPath, append(markerContent, '\n'), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := SummarizeTrackedEvidence(
		runDir,
		nil,
		trackedDigest,
	); err == nil || !strings.Contains(err.Error(), "tracked quality aggregate digest mismatch") {
		t.Fatalf("rewritten marker error = %v", err)
	}
}

func TestSummarizeEvidenceRejectsMixedQualityGeneration(t *testing.T) {
	runDir := t.TempDir()
	qualityDir := filepath.Join(runDir, "quality")
	if err := os.MkdirAll(qualityDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range qualityAggregateFiles {
		if err := os.WriteFile(
			filepath.Join(qualityDir, name),
			[]byte("{}\n"),
			0o644,
		); err != nil {
			t.Fatal(err)
		}
	}
	writeQualityAggregateManifest(t, runDir)
	if err := os.WriteFile(
		filepath.Join(qualityDir, "static.json"),
		[]byte("{\"new\":true}\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := SummarizeEvidence(runDir, nil); err == nil ||
		!strings.Contains(err.Error(), "committed generation") {
		t.Fatalf("SummarizeEvidence error = %v, want mixed-generation error", err)
	}
}

func TestSummarizeEvidenceRequiresQualityAggregateMarker(t *testing.T) {
	runDir := t.TempDir()
	writeJSON(t, filepath.Join(runDir, "metrics.json"), map[string]any{
		"schema_version": 2,
		"formula":        qualityMetricsFormula,
		"cases":          []any{},
		"comparisons":    []any{},
	})
	if _, err := SummarizeEvidence(runDir, nil); err == nil ||
		!strings.Contains(err.Error(), "quality aggregate commit marker is missing") {
		t.Fatalf("missing aggregate marker error = %v", err)
	}
	checks := ValidateEvidence(runDir, nil)
	for _, check := range checks {
		if check.Description ==
			"quality aggregate files match aggregate-manifest.json" {
			if check.Passed ||
				!strings.Contains(
					check.Error,
					"quality aggregate commit marker is missing",
				) {
				t.Fatalf("missing aggregate validation check = %+v", check)
			}
			return
		}
	}
	t.Fatal("ValidateEvidence omitted quality aggregate check")
}

func TestValidatePromotion(t *testing.T) {
	metrics := []EvidenceMetric{
		{
			Name:               "baseline",
			Task:               "deep-review",
			Variant:            "baseline",
			StaticScorePercent: 100,
		},
		{
			Name:                          "candidate",
			Task:                          "deep-review",
			Variant:                       "optimized",
			Profile:                       "investigative-verified-high",
			Completed:                     true,
			HasComparison:                 true,
			EffectiveReductionPercent:     25,
			StaticScorePercent:            100,
			StaticRequiredPass:            true,
			JudgeCount:                    2,
			JudgeNotWorse:                 true,
			JudgeCoreConclusionMatch:      true,
			RepoViewInvocations:           8,
			RepoViewChangedInvocations:    1,
			RepoViewFindInvocations:       2,
			RepoViewInspectInvocations:    4,
			RepoViewOutlineInvocations:    1,
			OtherToolCalls:                1,
			RepoViewDeepSequenceExact:     true,
			RepoViewDeepDependencyExact:   true,
			RepoViewFirstChanged:          true,
			RepoViewNavigationValid:       true,
			MechanicalNavigationEnforced:  true,
			RepoViewInvocationCap:         34,
			RepoViewInvocationCapExceeded: false,
		},
	}
	metrics[1].JudgeCount = 0
	metrics[1].JudgeNotWorse = false
	if checks := ValidatePromotion(metrics, 0); !Passed(checks) {
		t.Fatalf("pre-judge promotion checks failed: %+v", checks)
	}
	metrics[1].JudgeCount = 2
	metrics[1].JudgeNotWorse = true
	metrics[1].JudgeCoreConclusionMatch = false
	if checks := ValidatePromotion(metrics, 2); !Passed(checks) {
		t.Fatalf("correct baseline conclusion fix failed promotion: %+v", checks)
	}
	metrics[1].RepoViewDeepDependencyExact = false
	if checks := ValidatePromotion(metrics, 2); Passed(checks) {
		t.Fatal("inexact deep dependency command passed promotion")
	}
	metrics[1].RepoViewDeepDependencyExact = true

	metrics[1].EffectiveReductionPercent = -1
	metrics[1].JudgeNotWorse = false
	metrics[1].JudgeUnsupportedClaims = 1
	checks := ValidatePromotion(metrics, 2)
	if Passed(checks) {
		t.Fatal("expected token and judge regressions to fail promotion")
	}
}

func TestNormalizeQualityFixtureBindsEveryCasePrompt(t *testing.T) {
	runDir := t.TempDir()
	manifestPath := filepath.Join(runDir, "manifest.json")
	writeJSON(t, manifestPath, map[string]any{
		"schema_version":    1,
		"worktree":          runDir,
		"target_commit":     strings.Repeat("a", 40),
		"base_commit":       strings.Repeat("b", 40),
		"task_selection":    "all",
		"variant_selection": "all",
		"profiles":          []any{"default", "guarded-high"},
		"baseline_from":     nil,
	})

	type promptBindings struct {
		Files   map[string]string `json:"case_prompt_files"`
		Digests map[string]string `json:"case_prompt_digests"`
	}
	readBindings := func(path string) promptBindings {
		t.Helper()
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var bindings promptBindings
		if err := json.Unmarshal(content, &bindings); err != nil {
			t.Fatal(err)
		}
		return bindings
	}
	manifestBindings := readBindings(manifestPath)
	generationConfigPath := filepath.Join(runDir, "generation-config.json")
	generationBindings := readBindings(generationConfigPath)
	expected := []string{
		"baseline-explain",
		"optimized-explain",
		"optimized-guarded-high-explain",
		"baseline-review",
		"optimized-review",
		"optimized-guarded-high-review",
	}
	if len(manifestBindings.Files) != len(expected) ||
		len(manifestBindings.Digests) != len(expected) ||
		len(generationBindings.Files) != len(expected) ||
		len(generationBindings.Digests) != len(expected) {
		t.Fatalf(
			"case prompt binding sizes = manifest %d/%d, generation %d/%d, want %d",
			len(manifestBindings.Files),
			len(manifestBindings.Digests),
			len(generationBindings.Files),
			len(generationBindings.Digests),
			len(expected),
		)
	}
	for _, name := range expected {
		relative := name + ".user-prompt.txt"
		digest := manifestBindings.Digests[name]
		if manifestBindings.Files[name] != relative ||
			generationBindings.Files[name] != relative ||
			generationBindings.Digests[name] != digest ||
			!validSHA256Digest(digest) {
			t.Errorf(
				"case prompt %s binding = manifest %q/%q, generation %q/%q",
				name,
				manifestBindings.Files[name],
				digest,
				generationBindings.Files[name],
				generationBindings.Digests[name],
			)
			continue
		}
		content, err := os.ReadFile(filepath.Join(runDir, relative))
		if err != nil {
			t.Error(err)
			continue
		}
		if sha256Bytes(content) != digest {
			t.Errorf("case prompt %s content does not match its digest", name)
		}
	}
	configContent, err := os.ReadFile(generationConfigPath)
	if err != nil {
		t.Fatal(err)
	}
	var configFields map[string]json.RawMessage
	if err := json.Unmarshal(configContent, &configFields); err != nil {
		t.Fatal(err)
	}
	if len(configFields) != 15 {
		t.Fatalf("generation config field count = %d, want 15", len(configFields))
	}
}

func TestSharedQualityGenerationConfigIgnoresOnlyVariantBindings(t *testing.T) {
	baseline := qualityGenerationConfig{
		PromptFiles: map[string]string{
			"explain": "prompts/explain.txt",
		},
		PromptDigests: map[string]string{
			"explain": strings.Repeat("a", 64),
		},
		CasePromptFiles: map[string]string{
			"baseline-explain": "baseline-explain.user-prompt.txt",
		},
		CasePromptDigests: map[string]string{
			"baseline-explain": strings.Repeat("b", 64),
		},
		GenerationIsolation:          qualityGenerationIsolation,
		ProfilesSnapshotPath:         "profiles-snapshot.tsv",
		ProfilesSnapshotSHA256:       strings.Repeat("c", 64),
		AuthSourcePermission:         "deny-if-present",
		MechanicalNavigationEnforced: false,
	}
	candidate := baseline
	candidate.CasePromptFiles = map[string]string{
		"baseline-explain":  "baseline-explain.user-prompt.txt",
		"optimized-explain": "optimized-explain.user-prompt.txt",
	}
	candidate.CasePromptDigests = map[string]string{
		"baseline-explain":  strings.Repeat("b", 64),
		"optimized-explain": strings.Repeat("d", 64),
	}
	candidate.MechanicalNavigationEnforced = true
	if !reflect.DeepEqual(
		sharedQualityGenerationConfig(baseline),
		sharedQualityGenerationConfig(candidate),
	) {
		t.Fatal("variant-specific prompt bindings changed shared projection")
	}
	candidate.AuthSourcePermission = "different-policy"
	if reflect.DeepEqual(
		sharedQualityGenerationConfig(baseline),
		sharedQualityGenerationConfig(candidate),
	) {
		t.Fatal("shared policy change disappeared from projection")
	}
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	if root, ok := value.(map[string]any); ok {
		normalizeQualityFixture(t, path, root)
	}
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func normalizeQualityFixture(t *testing.T, filePath string, root map[string]any) {
	t.Helper()
	switch filepath.Base(filePath) {
	case "manifest.json":
		if _, ok := root["worktree"]; !ok {
			return
		}
		runDir := filepath.Dir(filePath)
		target, _ := root["target_commit"].(string)
		if _, ok := root["prompt_commit"]; !ok {
			root["prompt_commit"] = target
		}
		if _, ok := root["base_ref"]; !ok {
			root["base_ref"] = "HEAD"
		}
		if _, ok := root["model"]; !ok {
			root["model"] = "fixture-model"
		}
		if _, ok := root["model_mode"]; !ok {
			root["model_mode"] = "pinned"
		}
		if _, ok := root["model_configuration"]; !ok {
			root["model_configuration"] = "pinned"
		}
		if _, ok := root["codex_version"]; !ok {
			root["codex_version"] = "fixture-codex"
		}
		if _, ok := root["generation_isolation"]; !ok {
			root["generation_isolation"] = qualityGenerationIsolation
		}
		profilesSnapshot, err := os.ReadFile(filepath.Join(
			"..", "..", "experiments", "lsp-replacement", "profiles.tsv",
		))
		if err != nil {
			t.Fatal(err)
		}
		profilesDigest := sha256Bytes(profilesSnapshot)
		root["profiles_snapshot_path"] = "profiles-snapshot.tsv"
		root["profiles_snapshot_sha256"] = profilesDigest
		if err := os.WriteFile(
			filepath.Join(runDir, "profiles-snapshot.tsv"),
			profilesSnapshot,
			0o644,
		); err != nil {
			t.Fatal(err)
		}
		selectedTasks := make([]string, 0, 2)
		switch root["task_selection"] {
		case "all":
			selectedTasks = append(selectedTasks, "explain", "review")
		case "deep":
			selectedTasks = append(
				selectedTasks,
				"deep-explain",
				"deep-review",
			)
		default:
			task, _ := root["task_selection"].(string)
			selectedTasks = append(selectedTasks, task)
		}
		if err := os.MkdirAll(filepath.Join(runDir, "prompts"), 0o755); err != nil {
			t.Fatal(err)
		}
		promptFiles := make(map[string]any, len(selectedTasks))
		promptDigests := make(map[string]any, len(selectedTasks))
		for _, task := range selectedTasks {
			relative := "prompts/" + task + ".txt"
			content := []byte("fixture rendered prompt for " + task)
			if err := os.WriteFile(
				filepath.Join(runDir, filepath.FromSlash(relative)),
				content,
				0o644,
			); err != nil {
				t.Fatal(err)
			}
			promptFiles[task] = relative
			promptDigests[task] = sha256Bytes(content)
		}
		if _, ok := root["prompt_files"]; !ok {
			root["prompt_files"] = promptFiles
		}
		if _, ok := root["prompt_digests"]; !ok {
			root["prompt_digests"] = promptDigests
		}
		profiles := make([]string, 0)
		switch rawProfiles := root["profiles"].(type) {
		case []any:
			for _, rawProfile := range rawProfiles {
				profile, _ := rawProfile.(string)
				profiles = append(profiles, profile)
			}
		case []string:
			profiles = append(profiles, rawProfiles...)
		}
		casePromptFiles := make(map[string]any)
		casePromptDigests := make(map[string]any)
		writeCasePrompt := func(name string) {
			relative := name + ".user-prompt.txt"
			content := []byte("fixture rendered user prompt for " + name)
			if err := os.WriteFile(
				filepath.Join(runDir, filepath.FromSlash(relative)),
				content,
				0o644,
			); err != nil {
				t.Fatal(err)
			}
			casePromptFiles[name] = relative
			casePromptDigests[name] = sha256Bytes(content)
		}
		for _, task := range selectedTasks {
			writeCasePrompt("baseline-" + task)
			variantSelection, _ := root["variant_selection"].(string)
			if variantSelection != "baseline" {
				for _, profile := range profiles {
					name := "optimized-" + profile + "-" + task
					if profile == "default" {
						name = "optimized-" + task
					}
					writeCasePrompt(name)
				}
			}
		}
		if _, ok := root["case_prompt_files"]; !ok {
			root["case_prompt_files"] = casePromptFiles
		}
		if _, ok := root["case_prompt_digests"]; !ok {
			root["case_prompt_digests"] = casePromptDigests
		}
		generationConfig, err := json.Marshal(map[string]any{
			"generation_isolation":                     qualityGenerationIsolation,
			"baseline_developer_instructions":          "Do not call collaboration, subagent, spawn-agent, or agent-wait tools. Do not read or invoke Codex skills, plugins, hooks, or marketplace resources; they are outside this benchmark.",
			"feature_flags":                            append([]string(nil), qualityFeatureFlags...),
			"codex_isolation_flags":                    append([]string(nil), qualityCodexIsolationFlags...),
			"codex_environment":                        append([]string(nil), qualityCodexEnvironment...),
			"host_go_environment":                      append([]string(nil), qualityHostGoEnvironment...),
			"profiles_snapshot_path":                   "profiles-snapshot.tsv",
			"profiles_snapshot_sha256":                 profilesDigest,
			"prompt_files":                             root["prompt_files"],
			"prompt_digests":                           root["prompt_digests"],
			"case_prompt_files":                        root["case_prompt_files"],
			"case_prompt_digests":                      root["case_prompt_digests"],
			"mechanical_navigation_semantics_enforced": true,
			"mechanical_navigation_contract": map[string]any{
				"required_root":                "<worktree>",
				"required_base_commit":         "<resolved-base>",
				"required_changed_return":      "<profile-return>",
				"required_changed_context":     "<profile-context>",
				"require_navigation_semantics": "1",
			},
			"auth_source_permission": "deny-if-present",
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, ok := root["mechanical_navigation_semantics_enforced"]; !ok {
			root["mechanical_navigation_semantics_enforced"] = true
		}
		if _, ok := root["generation_config_sha256"]; !ok {
			root["generation_config_sha256"] = sha256Bytes(generationConfig)
		}
		if err := os.WriteFile(
			filepath.Join(runDir, "generation-config.json"),
			generationConfig,
			0o644,
		); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(
			filepath.Join(runDir, "run-complete.json"),
			[]byte(
				`{"schema_version":1,"state":"complete","outcome":"success","exit_code":0,"completed_at":"2026-01-01T00:00:00Z"}`,
			),
			0o644,
		); err != nil {
			t.Fatal(err)
		}
		if _, ok := root["go_version"]; !ok {
			root["go_version"] = "fixture-go"
		}
		metricsPath := filepath.Join(runDir, "metrics.json")
		if metricsContent, readErr := os.ReadFile(metricsPath); readErr == nil {
			var metrics map[string]any
			if err := json.Unmarshal(metricsContent, &metrics); err != nil {
				t.Fatal(err)
			}
			metrics["analysis_provenance"] = map[string]any{
				"profiles_source": "run-snapshot",
				"profiles_path":   "profiles-snapshot.tsv",
				"profiles_sha256": profilesDigest,
			}
			metricsContent, err = json.Marshal(metrics)
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(metricsPath, metricsContent, 0o644); err != nil {
				t.Fatal(err)
			}
		} else if !os.IsNotExist(readErr) {
			t.Fatal(readErr)
		}
	case "metrics.json":
		rawCases, ok := root["cases"].([]any)
		if !ok || len(rawCases) == 0 {
			return
		}
		first, ok := rawCases[0].(map[string]any)
		if !ok {
			return
		}
		if _, ok := first["answer_file"]; !ok {
			return
		}
		if _, ok := root["schema_version"]; !ok {
			root["schema_version"] = 2
		}
		if _, ok := root["formula"]; !ok {
			root["formula"] = qualityMetricsFormula
		}
		if _, ok := root["analysis_provenance"]; !ok {
			profilesContent, err := os.ReadFile(filepath.Join(
				filepath.Dir(filePath),
				"profiles-snapshot.tsv",
			))
			if err == nil {
				root["analysis_provenance"] = map[string]any{
					"profiles_source": "run-snapshot",
					"profiles_path":   "profiles-snapshot.tsv",
					"profiles_sha256": sha256Bytes(profilesContent),
				}
			} else if !os.IsNotExist(err) {
				t.Fatal(err)
			}
		}
		for _, rawCase := range rawCases {
			current, ok := rawCase.(map[string]any)
			if !ok {
				continue
			}
			changed, _ := number(
				current["repo_view_changed_invocation_count"],
			)
			if _, ok := current["repo_view_first_invocation_changed"]; !ok {
				current["repo_view_first_invocation_changed"] = changed == 1
			}
			if _, ok := current["repo_view_navigation_semantics_valid"]; !ok {
				current["repo_view_navigation_semantics_valid"] =
					current["variant"] != "optimized" || changed == 1
			}
			if _, ok :=
				current["mechanical_navigation_semantics_enforced"]; !ok {
				current["mechanical_navigation_semantics_enforced"] =
					current["variant"] == "optimized"
			}
		}
		sort.SliceStable(rawCases, func(i, j int) bool {
			left, _ := rawCases[i].(map[string]any)["name"].(string)
			right, _ := rawCases[j].(map[string]any)["name"].(string)
			return left < right
		})
		if _, ok := root["comparisons"]; !ok {
			root["comparisons"] = qualityFixtureComparisons(rawCases)
		}
	}
}

func qualityFixtureComparisons(rawCases []any) []any {
	comparisons := make([]any, 0)
	for _, rawOptimized := range rawCases {
		optimized, _ := rawOptimized.(map[string]any)
		if optimized["variant"] != "optimized" || optimized["completed"] != true {
			continue
		}
		for _, rawBaseline := range rawCases {
			baseline, _ := rawBaseline.(map[string]any)
			if baseline["variant"] != "baseline" ||
				baseline["completed"] != true ||
				baseline["task"] != optimized["task"] {
				continue
			}
			baselineEffective, _ := number(baseline["effective_tokens"])
			optimizedEffective, _ := number(optimized["effective_tokens"])
			baselineRaw, _ := number(baseline["raw_total_tokens"])
			optimizedRaw, _ := number(optimized["raw_total_tokens"])
			baselineRegular, _ := number(baseline["regular_input_tokens"])
			optimizedRegular, _ := number(optimized["regular_input_tokens"])
			baselineCachedPercent, _ := number(baseline["cached_input_percent"])
			optimizedCachedPercent, _ := number(optimized["cached_input_percent"])
			comparisons = append(comparisons, map[string]any{
				"task":                                        optimized["task"],
				"profile":                                     optimized["profile"],
				"baseline_effective_tokens":                   baseline["effective_tokens"],
				"optimized_effective_tokens":                  optimized["effective_tokens"],
				"effective_reduction_percent":                 (1 - optimizedEffective/baselineEffective) * 100,
				"baseline_raw_total_tokens":                   baseline["raw_total_tokens"],
				"optimized_raw_total_tokens":                  optimized["raw_total_tokens"],
				"raw_reduction_percent":                       (1 - optimizedRaw/baselineRaw) * 100,
				"baseline_regular_input_tokens":               baseline["regular_input_tokens"],
				"optimized_regular_input_tokens":              optimized["regular_input_tokens"],
				"regular_input_reduction_percent":             (1 - optimizedRegular/baselineRegular) * 100,
				"baseline_cached_input_tokens":                baseline["cached_input_tokens"],
				"optimized_cached_input_tokens":               optimized["cached_input_tokens"],
				"baseline_cached_input_percent":               baseline["cached_input_percent"],
				"optimized_cached_input_percent":              optimized["cached_input_percent"],
				"cached_input_percent_delta":                  optimizedCachedPercent - baselineCachedPercent,
				"baseline_output_tokens":                      baseline["output_tokens"],
				"optimized_output_tokens":                     optimized["output_tokens"],
				"baseline_tool_calls":                         baseline["tool_call_count"],
				"optimized_tool_calls":                        optimized["tool_call_count"],
				"baseline_other_tool_calls":                   baseline["other_tool_call_count"],
				"optimized_other_tool_calls":                  optimized["other_tool_call_count"],
				"baseline_repo_view_tool_calls":               baseline["repo_view_tool_call_count"],
				"optimized_repo_view_tool_calls":              optimized["repo_view_tool_call_count"],
				"baseline_repo_view_invocations":              baseline["repo_view_invocation_count"],
				"optimized_repo_view_invocations":             optimized["repo_view_invocation_count"],
				"optimized_repo_view_bound_violations":        optimized["repo_view_bound_violation_count"],
				"optimized_repo_view_invocation_cap":          optimized["repo_view_invocation_cap"],
				"optimized_repo_view_invocation_cap_exceeded": optimized["repo_view_invocation_cap_exceeded"],
				"optimized_repo_view_budget_tamper_commands":  optimized["repo_view_budget_tamper_command_count"],
			})
			break
		}
	}
	return comparisons
}

func writeQualityAggregateManifest(t *testing.T, runDir string) {
	t.Helper()
	qualityDir := filepath.Join(runDir, "quality")
	if err := os.MkdirAll(qualityDir, 0o755); err != nil {
		t.Fatal(err)
	}
	staticPath := filepath.Join(qualityDir, "static.json")
	if _, err := os.Stat(staticPath); os.IsNotExist(err) {
		writeJSON(t, staticPath, map[string]any{
			"schema_version": 1,
			"cases":          []any{},
			"comparisons":    []any{},
		})
	} else if err != nil {
		t.Fatal(err)
	}
	judgesPath := filepath.Join(qualityDir, "judges.json")
	if _, err := os.Stat(judgesPath); os.IsNotExist(err) {
		writeJSON(t, judgesPath, map[string]any{
			"provenance_status": "strict-current",
			"judge_runs":        []any{},
			"baselines":         []any{},
			"candidates":        []any{},
		})
	} else if err != nil {
		t.Fatal(err)
	}
	usagePath := filepath.Join(qualityDir, "judge-usage.json")
	if _, err := os.Stat(usagePath); os.IsNotExist(err) {
		writeJSON(t, usagePath, map[string]any{
			"formula": qualityMetricsFormula,
			"runs":    []any{},
			"totals": map[string]any{
				"run_count":                      0,
				"input_tokens":                   0,
				"regular_input_tokens":           0,
				"cached_input_tokens":            0,
				"cached_input_equivalent_tokens": 0,
				"output_tokens":                  0,
				"reasoning_output_tokens":        0,
				"raw_total_tokens":               0,
				"effective_tokens":               0,
			},
		})
	} else if err != nil {
		t.Fatal(err)
	}
	inputDigests := make(map[string]string)
	metricsPath := filepath.Join(runDir, "metrics.json")
	strictEvidence := false
	generationConfigDigest := strings.Repeat("3", 64)
	if metricsContent, err := os.ReadFile(metricsPath); err == nil {
		strictEvidence = true
		manifestPath := filepath.Join(runDir, "manifest.json")
		if _, err := os.Stat(manifestPath); os.IsNotExist(err) {
			writeJSON(t, manifestPath, map[string]any{
				"schema_version":    1,
				"worktree":          runDir,
				"target_commit":     strings.Repeat("a", 40),
				"base_commit":       strings.Repeat("a", 40),
				"task_selection":    "explain",
				"variant_selection": "baseline",
				"profiles":          []any{"default"},
				"baseline_from":     nil,
			})
		} else if err != nil {
			t.Fatal(err)
		}
		generationConfigPath := filepath.Join(runDir, "generation-config.json")
		generationConfigContent, err := os.ReadFile(generationConfigPath)
		if err != nil {
			t.Fatal(err)
		}
		generationConfigDigest = sha256Bytes(generationConfigContent)
		var metrics struct {
			Cases []struct {
				Name                  string `json:"name"`
				AnswerFile            string `json:"answer_file"`
				CommandsFile          string `json:"commands_file"`
				ToolStatsFile         string `json:"tool_stats_file"`
				CallGraphDOTFile      string `json:"call_graph_dot_file"`
				CallGraphMarkdownFile string `json:"call_graph_markdown_file"`
			} `json:"cases"`
		}
		if err := json.Unmarshal(metricsContent, &metrics); err != nil {
			t.Fatal(err)
		}
		inputPaths := []string{
			"metrics.json",
			"manifest.json",
			"generation-config.json",
			"run-complete.json",
			"profiles-snapshot.tsv",
		}
		var manifest struct {
			PromptFiles     map[string]string `json:"prompt_files"`
			CasePromptFiles map[string]string `json:"case_prompt_files"`
		}
		manifestContent, err := os.ReadFile(manifestPath)
		if err != nil {
			t.Fatal(err)
		}
		if err := json.Unmarshal(manifestContent, &manifest); err != nil {
			t.Fatal(err)
		}
		for _, relative := range manifest.PromptFiles {
			inputPaths = append(inputPaths, relative)
		}
		for _, relative := range manifest.CasePromptFiles {
			inputPaths = append(inputPaths, relative)
		}
		for _, current := range metrics.Cases {
			inputPaths = append(inputPaths,
				current.Name+".jsonl",
				current.Name+".exit-code",
				current.AnswerFile,
				current.CommandsFile,
				current.ToolStatsFile,
				current.CallGraphDOTFile,
				current.CallGraphMarkdownFile,
			)
		}
		for _, relative := range inputPaths {
			digest, err := sha256File(
				filepath.Join(runDir, filepath.FromSlash(relative)),
			)
			if err != nil {
				t.Fatal(err)
			}
			inputDigests[relative] = digest
		}
	} else if !os.IsNotExist(err) {
		t.Fatal(err)
	}
	status := "non-strict"
	if strictEvidence {
		status = "strict-current"
	}
	// Normalize the empty test aggregates to the exact current schemas. Tests
	// that need judge content create a complete aggregate explicitly.
	var judgesProbe struct {
		JudgeRuns []any `json:"judge_runs"`
	}
	if content, err := os.ReadFile(judgesPath); err == nil &&
		json.Unmarshal(content, &judgesProbe) == nil &&
		judgesProbe.JudgeRuns != nil {
		var normalizedJudges map[string]any
		if err := json.Unmarshal(content, &normalizedJudges); err != nil {
			t.Fatal(err)
		}
		normalizedJudges["provenance_status"] = status
		writeJSON(t, judgesPath, normalizedJudges)
		var staticDocument any
		var judgesDocument any
		var usageDocument any
		for path, destination := range map[string]*any{
			staticPath: &staticDocument,
			judgesPath: &judgesDocument,
			usagePath:  &usageDocument,
		} {
			content, err := os.ReadFile(path)
			if err != nil || json.Unmarshal(content, destination) != nil {
				t.Fatalf("decode aggregate fixture %s: %v", path, err)
			}
		}
		writeJSON(t, filepath.Join(qualityDir, "quality.json"), map[string]any{
			"schema_version":       5,
			"provenance_status":    status,
			"required_judge_count": 0,
			"evaluator": map[string]any{
				"model":         "router-selected",
				"model_mode":    "router",
				"codex_version": "codex-cli fixture",
				"cache_schema":  8,
				"environment":   qualityEvaluatorEnvironmentFixture(),
			},
			"static":      staticDocument,
			"judges":      judgesDocument,
			"judge_usage": usageDocument,
			"verdicts":    []any{},
		})
	}
	snapshotDigests := make(map[string]string)
	for relative, digest := range inputDigests {
		snapshotDigests[qualitySnapshotName(relative)] = digest
	}
	for _, snapshotName := range []string{
		"quality-rubric.json",
		"quality-output-schema.json",
	} {
		bundlePath := qualityEvaluatorBundlePath(snapshotName)
		content, err := os.ReadFile(filepath.Join(
			"..", "..", "experiments", "lsp-replacement", snapshotName,
		))
		if err != nil {
			t.Fatal(err)
		}
		fullPath := filepath.Join(runDir, filepath.FromSlash(bundlePath))
		if err := os.WriteFile(fullPath, content, 0o644); err != nil {
			t.Fatal(err)
		}
		digest := sha256Bytes(content)
		inputDigests[bundlePath] = digest
		snapshotDigests[snapshotName] = digest
	}
	generatorDigests := make(map[string]string, len(qualityGeneratorFiles))
	for _, name := range qualityGeneratorFiles {
		snapshotName := qualityGeneratorSnapshotName(name)
		bundlePath := qualityGeneratorBundlePath(name)
		content := []byte("fixture evaluator source: " + name + "\n")
		fullPath := filepath.Join(runDir, filepath.FromSlash(bundlePath))
		if err := os.WriteFile(fullPath, content, 0o644); err != nil {
			t.Fatal(err)
		}
		digest := sha256Bytes(content)
		inputDigests[bundlePath] = digest
		snapshotDigests[snapshotName] = digest
		generatorDigests[name] = digest
	}
	writeJSON(t, filepath.Join(qualityDir, "inputs.json"), map[string]any{
		"schema_version": 1,
		"validation": map[string]any{
			"strict_evidence":    strictEvidence,
			"enforce":            strictEvidence,
			"bind_legacy_judges": false,
			"skip_analyze":       false,
			"judge_repeats":      0,
			"aggregate_status": func() string {
				if strictEvidence {
					return "strict-current"
				}
				return "non-strict"
			}(),
			"metrics_schema_version":   2,
			"metrics_formula":          qualityMetricsFormula,
			"generation_isolation":     qualityGenerationIsolation,
			"generation_config_sha256": generationConfigDigest,
			"judge_cache_schema":       8,
		},
		"inputs":     inputDigests,
		"snapshots":  snapshotDigests,
		"generators": generatorDigests,
		"analysis_environment": map[string]any{
			"go_version": "go version go1.26.5 fixture/fixture",
			"GOENV":      "off",
			"GOWORK":     "off",
			"GOFLAGS":    "-mod=readonly",
		},
		"judge_environment_semantics": []any{
			"go-version=go version go1.26.5 fixture/fixture",
			"outer-environment=inherit-none;fixture=true",
		},
	})
	digests := make(map[string]string, len(qualityAggregateFiles))
	for _, name := range qualityAggregateFiles {
		path := filepath.Join(qualityDir, name)
		if _, err := os.Stat(path); os.IsNotExist(err) {
			if err := os.WriteFile(path, []byte("{}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		} else if err != nil {
			t.Fatal(err)
		}
		digest, err := sha256File(path)
		if err != nil {
			t.Fatal(err)
		}
		digests[name] = digest
	}
	writeJSON(t, filepath.Join(qualityDir, "aggregate-manifest.json"), map[string]any{
		"schema_version": 2,
		"files":          digests,
	})
}

func qualityEvaluatorEnvironmentFixture() map[string]any {
	return map[string]any{
		"go_version":         "go version go1.26.5 fixture/fixture",
		"permission_profile": "quality-audit",
		"filesystem": map[string]any{
			"root":                   "deny",
			"minimal_runtime":        "read",
			"judge_checkout":         "read",
			"quality_input_snapshot": "read",
			"goroot":                 "read",
			"gomodcache":             "read",
			"judge_tool_root":        "write",
			"codex_executable":       "read",
			"codex_home":             "deny",
			"canonical_auth":         "deny-when-present",
		},
		"network": "disabled",
		"outer_environment": map[string]any{
			"inherit": "none", "PATH": "/bin", "HOME": "<home>",
			"TMPDIR": "<tmp>", "LANG": "C", "LC_ALL": "C", "TZ": "UTC",
			"CODEX_HOME": "<codex-home>", "auth": "staged-auth-json-only",
		},
		"shell_environment": map[string]any{
			"inherit": "none", "PATH": "/bin", "HOME": "<home>",
			"TMPDIR": "<tmp>", "LANG": "C", "LC_ALL": "C", "TZ": "UTC",
			"GOROOT": "<goroot>", "GOPATH": "<gopath>",
			"GOMODCACHE": "<gomodcache>", "GOCACHE": "<gocache>",
			"GOENV": "off", "GOTOOLCHAIN": "local", "GOWORK": "off",
			"GOFLAGS": "-mod=readonly", "git_configuration": "hardened",
		},
	}
}

func setQualityAggregateStrictEvidence(
	t *testing.T,
	runDir string,
	strict bool,
) string {
	t.Helper()
	inputsPath := filepath.Join(runDir, "quality", "inputs.json")
	var inputs map[string]any
	content, err := os.ReadFile(inputsPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(content, &inputs); err != nil {
		t.Fatal(err)
	}
	validation, ok := inputs["validation"].(map[string]any)
	if !ok {
		t.Fatal("quality input fixture has no validation object")
	}
	validation["strict_evidence"] = strict
	validation["enforce"] = strict
	if strict {
		validation["aggregate_status"] = "strict-current"
	} else {
		validation["aggregate_status"] = "non-strict"
	}
	writeJSON(t, inputsPath, inputs)
	status := validation["aggregate_status"].(string)
	judgesPath := filepath.Join(runDir, "quality", "judges.json")
	var judges map[string]any
	content, err = os.ReadFile(judgesPath)
	if err != nil || json.Unmarshal(content, &judges) != nil {
		t.Fatalf("decode judges fixture: %v", err)
	}
	judges["provenance_status"] = status
	writeJSON(t, judgesPath, judges)
	qualityPath := filepath.Join(runDir, "quality", "quality.json")
	var quality map[string]any
	content, err = os.ReadFile(qualityPath)
	if err != nil || json.Unmarshal(content, &quality) != nil {
		t.Fatalf("decode quality fixture: %v", err)
	}
	quality["provenance_status"] = status
	quality["judges"] = judges
	writeJSON(t, qualityPath, quality)

	markerPath := filepath.Join(runDir, "quality", "aggregate-manifest.json")
	var marker map[string]any
	content, err = os.ReadFile(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(content, &marker); err != nil {
		t.Fatal(err)
	}
	files, ok := marker["files"].(map[string]any)
	if !ok {
		t.Fatal("quality aggregate fixture has no files object")
	}
	for name, path := range map[string]string{
		"inputs.json":  inputsPath,
		"judges.json":  judgesPath,
		"quality.json": qualityPath,
	} {
		digest, err := sha256File(path)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = digest
	}
	writeJSON(t, markerPath, marker)
	digest, err := sha256File(markerPath)
	if err != nil {
		t.Fatal(err)
	}
	return digest
}

func TestValidateSourceChecksumsAcceptsRelocatableManifests(t *testing.T) {
	tests := []struct {
		name         string
		recordedRoot string
	}{
		{name: "bundle-local"},
		{name: "legacy-absolute", recordedRoot: "/stale/evidence/run"},
		{name: "legacy-relative", recordedRoot: "stale/evidence/run"},
		{name: "legacy-relative-dot", recordedRoot: "./stale/evidence/run"},
		{name: "legacy-parent-relative", recordedRoot: "../outside/run"},
		{name: "legacy-normalized-parent", recordedRoot: "stale/../outside/run"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			runDir := t.TempDir()
			writeSourceChecksumFixture(t, runDir, test.recordedRoot)
			check := validateSourceChecksums(runDir)
			if !check.Passed {
				t.Fatalf("checksum validation failed: %+v", check)
			}
		})
	}
}

func TestValidateEvidenceDetectsTamperedSourceArtifact(t *testing.T) {
	runDir := t.TempDir()
	writeSourceChecksumFixture(t, runDir, "")
	if err := os.WriteFile(
		filepath.Join(runDir, "repo-view.bin"),
		[]byte("tampered"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	checks := ValidateEvidence(runDir, nil)
	for _, check := range checks {
		if check.Description != "source artifacts match source-SHA256SUMS" {
			continue
		}
		if check.Passed || !strings.Contains(check.Error, "checksum mismatch") {
			t.Fatalf("tampered artifact check = %+v", check)
		}
		return
	}
	t.Fatal("ValidateEvidence did not report a source checksum check")
}

func TestValidateTrackedEvidenceDetectsRewrittenArtifactAndChecksum(t *testing.T) {
	runDir := t.TempDir()
	writeSourceChecksumFixture(t, runDir, "")
	checksumPath := filepath.Join(runDir, "source-SHA256SUMS")
	trackedDigest, err := sha256File(checksumPath)
	if err != nil {
		t.Fatal(err)
	}

	artifactPath := filepath.Join(runDir, "repo-view.bin")
	if err := os.WriteFile(artifactPath, []byte("coordinated tamper"), 0o644); err != nil {
		t.Fatal(err)
	}
	artifactDigest, err := sha256File(artifactPath)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(checksumPath)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(content), "\n")
	for index, line := range lines {
		if strings.HasSuffix(line, "  repo-view.bin") {
			lines[index] = artifactDigest + "  repo-view.bin"
		}
	}
	if err := os.WriteFile(
		checksumPath,
		[]byte(strings.Join(lines, "\n")),
		0o644,
	); err != nil {
		t.Fatal(err)
	}

	checks := ValidateTrackedEvidence(runDir, nil, trackedDigest)
	for _, check := range checks {
		if check.Description != "source artifacts match source-SHA256SUMS" {
			continue
		}
		if check.Passed ||
			!strings.Contains(check.Error, "tracked source-SHA256SUMS digest mismatch") {
			t.Fatalf("coordinated tamper check = %+v", check)
		}
		return
	}
	t.Fatal("ValidateTrackedEvidence did not report a source checksum check")
}

func TestValidateSourceChecksumsRejectsUnexpectedArtifactNames(t *testing.T) {
	for _, recordedPath := range []string{
		"../unexpected.bin",
		"stale/evidence/../unexpected.tar.gz",
	} {
		t.Run(recordedPath, func(t *testing.T) {
			runDir := t.TempDir()
			writeSourceChecksumFixture(t, runDir, "")
			checksumPath := filepath.Join(runDir, "source-SHA256SUMS")
			content, err := os.ReadFile(checksumPath)
			if err != nil {
				t.Fatal(err)
			}
			content = []byte(strings.Replace(
				string(content),
				"  repo-view.bin\n",
				"  "+recordedPath+"\n",
				1,
			))
			if err := os.WriteFile(checksumPath, content, 0o644); err != nil {
				t.Fatal(err)
			}

			check := validateSourceChecksums(runDir)
			if check.Passed || !strings.Contains(check.Error, "unexpected source artifact") {
				t.Fatalf("unexpected checksum artifact check = %+v", check)
			}
		})
	}
}

func TestValidateSourceChecksumsBindsAuthenticatedDependencySnapshot(t *testing.T) {
	runDir := t.TempDir()
	writeSourceChecksumFixture(t, runDir, "")
	writeJSON(t, filepath.Join(runDir, "manifest.json"), map[string]any{
		"dependency_source": map[string]any{
			"manifest_path": "dependency-source/manifest.json",
		},
	})
	missingCheck := validateSourceChecksums(runDir)
	if missingCheck.Passed ||
		!strings.Contains(
			missingCheck.Error,
			"missing checksum entry for dependency-source/manifest.json",
		) {
		t.Fatalf("missing dependency source check = %+v", missingCheck)
	}
	checksumPath := filepath.Join(runDir, "source-SHA256SUMS")
	checksum, err := os.OpenFile(checksumPath, os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range dependencySourceChecksumArtifacts {
		path := filepath.Join(runDir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			checksum.Close()
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte("contents of "+name), 0o644); err != nil {
			checksum.Close()
			t.Fatal(err)
		}
		digest, err := sha256File(path)
		if err != nil {
			checksum.Close()
			t.Fatal(err)
		}
		if _, err := fmt.Fprintf(checksum, "%s  %s\n", digest, name); err != nil {
			checksum.Close()
			t.Fatal(err)
		}
	}
	if err := checksum.Close(); err != nil {
		t.Fatal(err)
	}
	if check := validateSourceChecksums(runDir); !check.Passed {
		t.Fatalf("dependency source checksum validation failed: %+v", check)
	}
	if err := os.WriteFile(
		filepath.Join(runDir, "dependency-source", "target-go.sum"),
		[]byte("tampered"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	check := validateSourceChecksums(runDir)
	if check.Passed || !strings.Contains(check.Error, "target-go.sum: checksum mismatch") {
		t.Fatalf("tampered dependency source check = %+v", check)
	}
}

func writeSourceChecksumFixture(t *testing.T, runDir, recordedRoot string) {
	t.Helper()
	var checksum string
	for _, name := range sourceChecksumArtifacts {
		path := filepath.Join(runDir, name)
		if err := os.WriteFile(path, []byte("contents of "+name), 0o644); err != nil {
			t.Fatal(err)
		}
		digest, err := sha256File(path)
		if err != nil {
			t.Fatal(err)
		}
		recordedPath := name
		if recordedRoot != "" {
			recordedPath = recordedRoot + "/" + name
		}
		checksum += digest + "  " + recordedPath + "\n"
	}
	if err := os.WriteFile(
		filepath.Join(runDir, "source-SHA256SUMS"),
		[]byte(checksum),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
}

func TestReadRegularSnapshotRejectsOversizedFilesBeforeReading(t *testing.T) {
	path := filepath.Join(t.TempDir(), "oversized")
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Truncate(maximumRegularSnapshotBytes + 1); err != nil {
		_ = file.Close()
		t.Skipf("sparse files unavailable: %v", err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := readRegularSnapshot(path); err == nil ||
		!strings.Contains(err.Error(), "regular snapshot exceeds") {
		t.Fatalf("oversized snapshot error = %v", err)
	}
}
