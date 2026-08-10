package experimentsuite

import (
	"bufio"
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

type Manifest struct {
	Cases         []Case `json:"cases"`
	SchemaVersion int    `json:"schema_version"`
}

type ResolutionManifest struct {
	Cases         []ResolutionCase `json:"cases"`
	SchemaVersion int              `json:"schema_version"`
}

type Case struct {
	Live                   *LiveConfig `json:"live,omitempty"`
	ID                     string      `json:"id"`
	Complexity             string      `json:"complexity"`
	Outcome                string      `json:"outcome"`
	Evidence               string      `json:"evidence"`
	SourceChecksumSHA256   string      `json:"source_checksum_sha256"`
	QualityAggregateSHA256 string      `json:"quality_aggregate_sha256"`
	QualityProvenance      string      `json:"quality_provenance,omitempty"`
	Description            string      `json:"description"`
	Assertions             []Assertion `json:"assertions"`
	Level                  int         `json:"level"`
}

type ResolutionCase struct {
	ID                     string      `json:"id"`
	Status                 string      `json:"status"`
	RootCause              string      `json:"root_cause"`
	Fix                    string      `json:"fix"`
	Evidence               string      `json:"evidence"`
	SourceChecksumSHA256   string      `json:"source_checksum_sha256"`
	QualityAggregateSHA256 string      `json:"quality_aggregate_sha256"`
	QualityProvenance      string      `json:"quality_provenance,omitempty"`
	Repair                 *LiveConfig `json:"repair,omitempty"`
	FixtureMetricCases     []string    `json:"fixture_metric_cases,omitempty"`
	MetricCases            []string    `json:"metric_cases"`
	GoTests                []GoTest    `json:"go_tests,omitempty"`
	Assertions             []Assertion `json:"assertions"`
}

type GoTest struct {
	Description string `json:"description"`
	Package     string `json:"package"`
	Run         string `json:"run"`
}

type LiveConfig struct {
	Task         string `json:"task"`
	Profile      string `json:"profile"`
	BaselineFrom string `json:"baseline_from"`
	Source       string `json:"source"`
	Commit       string `json:"commit"`
	PromptCommit string `json:"prompt_commit"`
	Base         string `json:"base"`
	ModelMode    string `json:"model_mode"`
	Model        string `json:"model,omitempty"`
}

type Assertion struct {
	Value       any            `json:"value"`
	Selector    map[string]any `json:"selector,omitempty"`
	Description string         `json:"description"`
	Source      string         `json:"source"`
	Field       string         `json:"field"`
	Operator    string         `json:"operator"`
}

type CheckResult struct {
	Actual      any    `json:"actual,omitempty"`
	Expected    any    `json:"expected,omitempty"`
	Description string `json:"description"`
	Error       string `json:"error,omitempty"`
	Passed      bool   `json:"passed"`
}

type ToolStat struct {
	Name             string `json:"name"`
	ToolCalls        int    `json:"tool_calls"`
	Invocations      int    `json:"invocations"`
	OutputCharacters int    `json:"output_characters"`
}

type EvidenceMetric struct {
	Task                          string     `json:"task"`
	Variant                       string     `json:"variant"`
	Profile                       string     `json:"profile"`
	JudgeStatus                   string     `json:"judge_status"`
	Name                          string     `json:"name"`
	CallGraphMarkdownFile         string     `json:"call_graph_markdown_file"`
	CallGraphDOTFile              string     `json:"call_graph_dot_file"`
	ToolStatsFile                 string     `json:"tool_stats_file"`
	ToolTypes                     []ToolStat `json:"tool_types"`
	Operations                    []ToolStat `json:"operations"`
	StaticScorePercent            float64    `json:"static_score_percent"`
	RepoViewFindInvocations       int        `json:"repo_view_find_invocations"`
	RepoViewInvocations           int        `json:"repo_view_invocations"`
	TemporalEdges                 int        `json:"temporal_edges"`
	OutputReferenceEdges          int        `json:"output_reference_edges"`
	RepoViewInvocationCap         int        `json:"repo_view_invocation_cap"`
	AverageTaskAdherence          float64    `json:"average_task_adherence"`
	RepoViewBoundViolations       int        `json:"repo_view_bound_violations"`
	RepoViewBudgetTamperCommands  int        `json:"repo_view_budget_tamper_commands"`
	RepoViewChangedInvocations    int        `json:"repo_view_changed_invocations"`
	OtherToolCalls                int        `json:"other_tool_calls"`
	AverageGrounding              float64    `json:"average_grounding"`
	AverageCompleteness           float64    `json:"average_completeness"`
	JudgeCriticalOmissions        int        `json:"judge_critical_omissions"`
	RepoViewInspectInvocations    int        `json:"repo_view_inspect_invocations"`
	RepoViewOutlineInvocations    int        `json:"repo_view_outline_invocations"`
	RepoViewToolCalls             int        `json:"repo_view_tool_calls"`
	TotalToolCalls                int        `json:"total_tool_calls"`
	EffectiveTokens               float64    `json:"effective_tokens"`
	OutputTokens                  int64      `json:"output_tokens"`
	CachedInputTokens             int64      `json:"cached_input_tokens"`
	AverageCorrectness            float64    `json:"average_correctness"`
	EffectiveReductionPercent     float64    `json:"effective_reduction_percent"`
	RegularInputTokens            int64      `json:"regular_input_tokens"`
	JudgeMaterialContradictions   int        `json:"judge_material_contradictions"`
	JudgeCount                    int        `json:"judge_count"`
	JudgeBaselinePointsOmitted    int        `json:"judge_baseline_points_omitted"`
	JudgeUnsupportedClaims        int        `json:"judge_unsupported_claims"`
	RepoViewFirstChanged          bool       `json:"repo_view_first_invocation_changed"`
	RepoViewDeepSequenceExact     bool       `json:"repo_view_deep_command_sequence_exact"`
	RepoViewDeepDependencyExact   bool       `json:"repo_view_deep_dependency_awk_exact"`
	JudgeCoreConclusionMatch      bool       `json:"judge_core_conclusion_match"`
	JudgeNotWorse                 bool       `json:"judge_not_worse"`
	Completed                     bool       `json:"completed"`
	StaticRequiredPass            bool       `json:"static_required_pass"`
	HasComparison                 bool       `json:"has_comparison"`
	MechanicalNavigationEnforced  bool       `json:"mechanical_navigation_semantics_enforced"`
	RepoViewNavigationValid       bool       `json:"repo_view_navigation_semantics_valid"`
	RepoViewInvocationCapExceeded bool       `json:"repo_view_invocation_cap_exceeded"`
}

func LoadManifest(path string) (Manifest, error) {
	manifest, _, err := LoadManifestSnapshot(path)
	return manifest, err
}

// LoadManifestSnapshot returns the digest of the same bytes that were parsed
// and validated. Callers can therefore record the executed manifest without a
// second, potentially different read of the path.
func LoadManifestSnapshot(path string) (Manifest, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, "", err
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, "", fmt.Errorf("decode suite manifest: %w", err)
	}
	if manifest.SchemaVersion != 1 {
		return Manifest{}, "", fmt.Errorf("unsupported suite schema version %d", manifest.SchemaVersion)
	}
	seen := make(map[string]bool)
	for index := range manifest.Cases {
		testCase := &manifest.Cases[index]
		if !validSuiteSlug(testCase.ID) || seen[testCase.ID] {
			return Manifest{}, "", fmt.Errorf("missing or duplicate suite case ID %q", testCase.ID)
		}
		seen[testCase.ID] = true
		if testCase.Level < 1 ||
			!validSuiteRelativePath(testCase.Evidence) ||
			len(testCase.Assertions) == 0 {
			return Manifest{}, "", fmt.Errorf(
				"case %s requires a positive level, safe evidence path, and assertions",
				testCase.ID,
			)
		}
		if !validSHA256Digest(testCase.SourceChecksumSHA256) {
			return Manifest{}, "", fmt.Errorf(
				"case %s requires a lowercase source checksum SHA-256 digest",
				testCase.ID,
			)
		}
		if !validSHA256Digest(testCase.QualityAggregateSHA256) {
			return Manifest{}, "", fmt.Errorf(
				"case %s requires a lowercase quality aggregate SHA-256 digest",
				testCase.ID,
			)
		}
		if testCase.QualityProvenance == "" {
			testCase.QualityProvenance = "strict-current"
		}
		if !validQualityProvenance(testCase.QualityProvenance) {
			return Manifest{}, "", fmt.Errorf(
				"case %s has invalid quality provenance %q",
				testCase.ID,
				testCase.QualityProvenance,
			)
		}
		if testCase.Outcome != "accepted" && testCase.Outcome != "rejected" {
			return Manifest{}, "", fmt.Errorf("case %s has invalid outcome %q", testCase.ID, testCase.Outcome)
		}
		if testCase.Outcome == "accepted" &&
			testCase.QualityProvenance != "strict-current" {
			return Manifest{}, "", fmt.Errorf(
				"accepted case %s requires strict-current quality provenance",
				testCase.ID,
			)
		}
		if testCase.Live != nil {
			if testCase.Outcome != "accepted" {
				return Manifest{}, "", fmt.Errorf("rejected case %s cannot be live-enabled", testCase.ID)
			}
			if !validLiveConfig(*testCase.Live) {
				return Manifest{}, "", fmt.Errorf("live case %s has incomplete configuration", testCase.ID)
			}
		}
	}
	sort.SliceStable(manifest.Cases, func(i, j int) bool {
		if manifest.Cases[i].Level == manifest.Cases[j].Level {
			return manifest.Cases[i].ID < manifest.Cases[j].ID
		}
		return manifest.Cases[i].Level < manifest.Cases[j].Level
	})
	return manifest, sha256Bytes(data), nil
}

func LoadResolutionManifest(path string) (ResolutionManifest, error) {
	manifest, _, err := LoadResolutionManifestSnapshot(path)
	return manifest, err
}

// LoadResolutionManifestSnapshot is the resolution-manifest counterpart of
// LoadManifestSnapshot.
func LoadResolutionManifestSnapshot(path string) (ResolutionManifest, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ResolutionManifest{}, "", err
	}
	var manifest ResolutionManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return ResolutionManifest{}, "", fmt.Errorf("decode resolution manifest: %w", err)
	}
	if manifest.SchemaVersion != 1 {
		return ResolutionManifest{}, "", fmt.Errorf(
			"unsupported resolution schema version %d",
			manifest.SchemaVersion,
		)
	}
	seen := make(map[string]bool)
	for index := range manifest.Cases {
		resolution := &manifest.Cases[index]
		if !validSuiteSlug(resolution.ID) || seen[resolution.ID] {
			return ResolutionManifest{}, "", fmt.Errorf(
				"missing or duplicate resolution case ID %q",
				resolution.ID,
			)
		}
		seen[resolution.ID] = true
		if resolution.Status != "accepted" && resolution.Status != "resolved" {
			return ResolutionManifest{}, "", fmt.Errorf(
				"resolution %s has invalid status %q",
				resolution.ID,
				resolution.Status,
			)
		}
		if resolution.QualityProvenance == "" {
			resolution.QualityProvenance = "strict-current"
		}
		if resolution.QualityProvenance != "strict-current" {
			return ResolutionManifest{}, "", fmt.Errorf(
				"resolution %s requires strict-current quality provenance",
				resolution.ID,
			)
		}
		if resolution.RootCause == "" ||
			resolution.Fix == "" ||
			!validSuiteRelativePath(resolution.Evidence) ||
			len(resolution.MetricCases) == 0 ||
			len(resolution.Assertions) == 0 {
			return ResolutionManifest{}, "", fmt.Errorf(
				"resolution %s requires cause, fix, safe evidence, metric cases, and assertions",
				resolution.ID,
			)
		}
		if !validSHA256Digest(resolution.SourceChecksumSHA256) {
			return ResolutionManifest{}, "", fmt.Errorf(
				"resolution %s requires a lowercase source checksum SHA-256 digest",
				resolution.ID,
			)
		}
		if !validSHA256Digest(resolution.QualityAggregateSHA256) {
			return ResolutionManifest{}, "", fmt.Errorf(
				"resolution %s requires a lowercase quality aggregate SHA-256 digest",
				resolution.ID,
			)
		}
		if resolution.Status == "resolved" && resolution.Repair == nil {
			return ResolutionManifest{}, "", fmt.Errorf(
				"resolved case %s requires a repair configuration",
				resolution.ID,
			)
		}
		if resolution.Repair != nil && !validLiveConfig(*resolution.Repair) {
			return ResolutionManifest{}, "", fmt.Errorf(
				"resolution %s has an incomplete repair configuration",
				resolution.ID,
			)
		}
		for _, goTest := range resolution.GoTests {
			if goTest.Description == "" ||
				!validLocalGoPackage(goTest.Package) ||
				goTest.Run == "" {
				return ResolutionManifest{}, "", fmt.Errorf(
					"resolution %s has an incomplete Go test",
					resolution.ID,
				)
			}
		}
	}
	return manifest, sha256Bytes(data), nil
}

func SelectResolutions(
	manifest ResolutionManifest,
	cases []Case,
) ([]ResolutionCase, error) {
	byID := make(map[string]ResolutionCase, len(manifest.Cases))
	for _, resolution := range manifest.Cases {
		byID[resolution.ID] = resolution
	}
	selected := make([]ResolutionCase, 0, len(cases))
	for _, testCase := range cases {
		resolution, ok := byID[testCase.ID]
		if !ok {
			return nil, fmt.Errorf("case %s has no resolution entry", testCase.ID)
		}
		if testCase.Outcome == "accepted" && resolution.Status != "accepted" {
			return nil, fmt.Errorf(
				"accepted case %s cannot resolve as %s",
				testCase.ID,
				resolution.Status,
			)
		}
		if testCase.Outcome == "rejected" && resolution.Status != "resolved" {
			return nil, fmt.Errorf(
				"rejected case %s must resolve as resolved",
				testCase.ID,
			)
		}
		selected = append(selected, resolution)
	}
	return selected, nil
}

func SelectCases(manifest Manifest, requested map[string]bool, maxLevel int) ([]Case, error) {
	var selected []Case
	found := make(map[string]bool)
	for _, testCase := range manifest.Cases {
		if len(requested) > 0 && !requested[testCase.ID] {
			continue
		}
		if maxLevel > 0 && testCase.Level > maxLevel {
			continue
		}
		selected = append(selected, testCase)
		found[testCase.ID] = true
	}
	for id := range requested {
		if !found[id] {
			return nil, fmt.Errorf("unknown or filtered suite case %q", id)
		}
	}
	if len(selected) == 0 {
		return nil, fmt.Errorf("no suite cases selected")
	}
	return selected, nil
}

func ValidateEvidence(runDir string, assertions []Assertion) []CheckResult {
	return validateEvidence(runDir, assertions, "", "", "")
}

// ValidateTrackedEvidence validates evidence whose source bundle is named by a
// tracked manifest. The expected digest anchors source-SHA256SUMS outside the
// ignored evidence directory, so rewriting both an artifact and its adjacent
// checksum file is detected.
func ValidateTrackedEvidence(
	runDir string,
	assertions []Assertion,
	expectedSourceChecksumSHA256 string,
	expectedQualityAggregateSHA256 ...string,
) []CheckResult {
	return validateTrackedEvidence(
		runDir,
		assertions,
		expectedSourceChecksumSHA256,
		true,
		expectedQualityAggregateSHA256...,
	)
}

func ValidateTrackedEvidenceProvenance(
	runDir string,
	assertions []Assertion,
	expectedSourceChecksumSHA256 string,
	expectedQualityAggregateSHA256 string,
	expectedQualityProvenance string,
) []CheckResult {
	if expectedQualityProvenance == "" {
		expectedQualityProvenance = "strict-current"
	}
	return validateEvidence(
		runDir,
		assertions,
		expectedSourceChecksumSHA256,
		expectedQualityAggregateSHA256,
		expectedQualityProvenance,
	)
}

// ValidateRejectedTrackedEvidence permits a historical non-strict aggregate
// only for a fixture whose suite outcome remains rejected.
func ValidateRejectedTrackedEvidence(
	runDir string,
	assertions []Assertion,
	expectedSourceChecksumSHA256 string,
	expectedQualityAggregateSHA256 ...string,
) []CheckResult {
	return validateTrackedEvidence(
		runDir,
		assertions,
		expectedSourceChecksumSHA256,
		false,
		expectedQualityAggregateSHA256...,
	)
}

func validateTrackedEvidence(
	runDir string,
	assertions []Assertion,
	expectedSourceChecksumSHA256 string,
	requireStrict bool,
	expectedQualityAggregateSHA256 ...string,
) []CheckResult {
	qualityDigest := ""
	if len(expectedQualityAggregateSHA256) > 0 {
		qualityDigest = expectedQualityAggregateSHA256[0]
	}
	expectedProvenance := "non-strict"
	if requireStrict {
		expectedProvenance = "strict-current"
	}
	return validateEvidence(
		runDir,
		assertions,
		expectedSourceChecksumSHA256,
		qualityDigest,
		expectedProvenance,
	)
}

func validateEvidence(
	runDir string,
	assertions []Assertion,
	expectedSourceChecksumSHA256 string,
	expectedQualityAggregateSHA256 string,
	expectedQualityProvenance string,
) []CheckResult {
	aggregate, aggregateErr := readQualityAggregate(
		runDir,
		expectedQualityAggregateSHA256,
	)
	if aggregateErr == nil && expectedQualityProvenance != "" &&
		aggregate.validation.AggregateStatus != expectedQualityProvenance {
		aggregateErr = fmt.Errorf(
			"quality aggregate provenance is %q, want %q",
			aggregate.validation.AggregateStatus,
			expectedQualityProvenance,
		)
	}
	var results []CheckResult
	if aggregateErr == nil {
		results = validateToolAccounting(runDir, aggregate)
	} else {
		results = append(results, CheckResult{
			Description: "metrics and tool accounting use committed quality inputs",
			Passed:      false,
			Error:       aggregateErr.Error(),
		})
	}
	results = append(
		results,
		validateSourceChecksums(runDir, expectedSourceChecksumSHA256),
	)
	aggregateResult := CheckResult{
		Description: "quality aggregate files match aggregate-manifest.json",
		Expected:    append([]string(nil), qualityAggregateFiles...),
	}
	if aggregateErr != nil {
		aggregateResult.Error = aggregateErr.Error()
	} else {
		aggregateResult.Passed = true
	}
	results = append(results, aggregateResult)
	documents := make(map[string]any)
	documentErrors := make(map[string]error)

	for _, assertion := range assertions {
		if aggregateErr != nil {
			results = append(results, CheckResult{
				Description: assertion.Description,
				Passed:      false,
				Expected:    assertion.Value,
				Error:       aggregateErr.Error(),
			})
			continue
		}
		document, ok := documents[assertion.Source]
		if !ok {
			var err error
			document, err = loadSourceSnapshot(runDir, assertion.Source, aggregate)
			documents[assertion.Source] = document
			documentErrors[assertion.Source] = err
		}
		if err := documentErrors[assertion.Source]; err != nil {
			results = append(results, CheckResult{
				Description: assertion.Description,
				Passed:      false,
				Expected:    assertion.Value,
				Error:       err.Error(),
			})
			continue
		}
		actual, err := selectField(document, assertion.Selector, assertion.Field)
		if err != nil {
			results = append(results, CheckResult{
				Description: assertion.Description,
				Passed:      false,
				Expected:    assertion.Value,
				Error:       err.Error(),
			})
			continue
		}
		passed, err := compare(actual, assertion.Operator, assertion.Value)
		result := CheckResult{
			Description: assertion.Description,
			Passed:      passed && err == nil,
			Actual:      actual,
			Expected:    assertion.Value,
		}
		if err != nil {
			result.Error = err.Error()
		}
		results = append(results, result)
	}
	return results
}

var sourceChecksumArtifacts = []string{
	"repo-view.bin",
	"repo-view-source.tar.gz",
}

var dependencySourceChecksumArtifacts = []string{
	"dependency-source/manifest.json",
	"dependency-source/target-go.mod",
	"dependency-source/target-go.sum",
	"dependency-source/golang.org/x/time@v0.14.0/rate/rate.go",
	"dependency-source/golang.org/x/time@v0.14.0/rate/rate_test.go",
}

func validateSourceChecksums(runDir string, trackedDigest ...string) CheckResult {
	result := CheckResult{
		Description: "source artifacts match source-SHA256SUMS",
	}
	checksumPath := filepath.Join(runDir, "source-SHA256SUMS")
	checksumBytes, err := readRegularSnapshot(checksumPath)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	if len(trackedDigest) > 0 && trackedDigest[0] != "" {
		actualManifestDigest := sha256Bytes(checksumBytes)
		if actualManifestDigest != trackedDigest[0] {
			result.Actual = actualManifestDigest
			result.Expected = trackedDigest[0]
			result.Error = "tracked source-SHA256SUMS digest mismatch"
			return result
		}
	}

	expected := make(map[string]string, len(sourceChecksumArtifacts)+len(dependencySourceChecksumArtifacts))
	var problems []string
	scanner := bufio.NewScanner(strings.NewReader(string(checksumBytes)))
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" {
			continue
		}
		digest, recordedPath, err := parseSHA256SumLine(line)
		if err != nil {
			problems = append(
				problems,
				fmt.Sprintf("source-SHA256SUMS:%d: %v", lineNumber, err),
			)
			continue
		}
		name, err := sourceArtifactName(recordedPath)
		if err != nil {
			problems = append(
				problems,
				fmt.Sprintf("source-SHA256SUMS:%d: %v", lineNumber, err),
			)
			continue
		}
		if _, duplicate := expected[name]; duplicate {
			problems = append(problems, "duplicate checksum entry for "+name)
			continue
		}
		expected[name] = digest
	}
	if err := scanner.Err(); err != nil {
		problems = append(problems, "read source-SHA256SUMS: "+err.Error())
	}

	requiredArtifacts := append([]string(nil), sourceChecksumArtifacts...)
	dependencySnapshotDeclared := false
	if manifestBytes, err := readRegularSnapshot(filepath.Join(runDir, "manifest.json")); err == nil {
		var manifestFields map[string]json.RawMessage
		if json.Unmarshal(manifestBytes, &manifestFields) == nil {
			if raw, ok := manifestFields["dependency_source"]; ok &&
				string(bytes.TrimSpace(raw)) != "null" {
				dependencySnapshotDeclared = true
			}
		}
	}
	for _, name := range dependencySourceChecksumArtifacts {
		if _, ok := expected[name]; ok {
			dependencySnapshotDeclared = true
		}
	}
	if dependencySnapshotDeclared {
		requiredArtifacts = append(requiredArtifacts, dependencySourceChecksumArtifacts...)
	}
	result.Expected = append([]string(nil), requiredArtifacts...)

	actual := make(map[string]string, len(requiredArtifacts))
	for _, name := range requiredArtifacts {
		expectedDigest, ok := expected[name]
		if !ok {
			problems = append(problems, "missing checksum entry for "+name)
			continue
		}
		artifactPath := filepath.Join(runDir, name)
		artifactBytes, err := readRegularSnapshot(artifactPath)
		if err != nil {
			problems = append(problems, name+": "+err.Error())
			continue
		}
		actualDigest := sha256Bytes(artifactBytes)
		actual[name] = actualDigest
		if !strings.EqualFold(actualDigest, expectedDigest) {
			problems = append(problems, name+": checksum mismatch")
		}
	}

	result.Passed = len(problems) == 0
	result.Actual = actual
	result.Error = strings.Join(problems, "; ")
	return result
}

func validSHA256Digest(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	_, err := hex.DecodeString(value)
	return err == nil &&
		value == strings.ToLower(value) &&
		value != strings.Repeat("0", sha256.Size*2)
}

func validSuiteSlug(value string) bool {
	if value == "" {
		return false
	}
	for index, character := range value {
		if (character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') ||
			(index > 0 && character == '-') {
			continue
		}
		return false
	}
	return true
}

func validSuiteTask(value string) bool {
	switch value {
	case "explain", "review", "all", "deep-explain", "deep-review", "deep":
		return true
	default:
		return false
	}
}

func validLiveConfig(config LiveConfig) bool {
	if !validSuiteTask(config.Task) ||
		!validSuiteSlug(config.Profile) ||
		!validOptionalSuiteRelativePath(config.BaselineFrom) ||
		!validSuiteRelativePath(config.Source) ||
		!validGitCommit(config.Commit) ||
		!validPromptCommit(config.PromptCommit) ||
		!strings.HasPrefix(config.Commit, config.PromptCommit) ||
		!validGitCommit(config.Base) ||
		config.Base == config.Commit {
		return false
	}
	switch config.ModelMode {
	case "router":
		return config.Model == ""
	case "pinned":
		return validModelIdentity(config.Model)
	default:
		return false
	}
}

func validModelIdentity(value string) bool {
	if value == "" || len(value) > 256 || value[0] == '-' {
		return false
	}
	for index := range len(value) {
		if value[index] < 0x21 || value[index] > 0x7e {
			return false
		}
	}
	return true
}

func validGenerationModelIdentity(model, mode, configuration string) bool {
	switch mode {
	case "router":
		return model == "router-selected" && configuration == "none"
	case "pinned":
		return validModelIdentity(model) && configuration == "pinned"
	default:
		return false
	}
}

//nolint:govet,nolintlint // Keep fields in the canonical manifest order used by audit output.
type liveRunIdentity struct {
	SourceRepo         string   `json:"source_repo"`
	TargetCommit       string   `json:"target_commit"`
	PromptCommit       string   `json:"prompt_commit"`
	BaseCommit         string   `json:"base_commit"`
	TaskSelection      string   `json:"task_selection"`
	VariantSelection   string   `json:"variant_selection"`
	Profiles           []string `json:"profiles"`
	BaselineFrom       *string  `json:"baseline_from"`
	Model              string   `json:"model"`
	ModelMode          string   `json:"model_mode"`
	ModelConfiguration string   `json:"model_configuration"`
}

func expectedLiveProfiles(runDir, selection string) ([]string, error) {
	if selection != "all" {
		return []string{selection}, nil
	}
	data, err := readRegularSnapshot(filepath.Join(runDir, "profiles-snapshot.tsv"))
	if err != nil {
		return nil, fmt.Errorf("read live profile snapshot: %w", err)
	}
	var profiles []string
	seen := make(map[string]bool)
	scanner := bufio.NewScanner(bytes.NewReader(data))
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		profile := strings.SplitN(line, "\t", 2)[0]
		if !validSuiteSlug(profile) || seen[profile] {
			return nil, fmt.Errorf("invalid live profile snapshot entry %q", profile)
		}
		seen[profile] = true
		profiles = append(profiles, profile)
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("scan live profile snapshot: %w", err)
	}
	if len(profiles) == 0 {
		return nil, fmt.Errorf("live profile snapshot selects no profiles")
	}
	return profiles, nil
}

// ValidateLiveIdentity proves that a live or repair run used the repository,
// workload, baseline, and model identity declared by its suite manifest.
func ValidateLiveIdentity(
	runDir string,
	config LiveConfig,
	resolvedPaths ...string,
) CheckResult {
	result := CheckResult{
		Description: "live run matches manifest repository, workload, and routing identity",
	}
	if !validLiveConfig(config) {
		result.Error = "suite live configuration is invalid"
		return result
	}
	if len(resolvedPaths) > 2 {
		result.Error = "too many resolved live identity paths"
		return result
	}
	expectedSource := config.Source
	if len(resolvedPaths) > 0 {
		expectedSource = resolvedPaths[0]
	}
	expectedProfiles, err := expectedLiveProfiles(runDir, config.Profile)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	variant := "all"
	var baselineFrom *string
	if config.BaselineFrom != "" {
		variant = "optimized"
		resolvedBaseline := config.BaselineFrom
		if len(resolvedPaths) > 1 {
			resolvedBaseline = resolvedPaths[1]
		}
		baselineFrom = &resolvedBaseline
	} else if len(resolvedPaths) > 1 {
		result.Error = "resolved baseline supplied for a same-run live configuration"
		return result
	}
	model := config.Model
	modelConfiguration := "pinned"
	if config.ModelMode == "router" {
		model = "router-selected"
		modelConfiguration = "none"
	}
	expected := liveRunIdentity{
		SourceRepo:         expectedSource,
		TargetCommit:       config.Commit,
		PromptCommit:       config.PromptCommit,
		BaseCommit:         config.Base,
		TaskSelection:      config.Task,
		VariantSelection:   variant,
		Profiles:           expectedProfiles,
		BaselineFrom:       baselineFrom,
		Model:              model,
		ModelMode:          config.ModelMode,
		ModelConfiguration: modelConfiguration,
	}
	result.Expected = expected
	data, err := os.ReadFile(filepath.Join(runDir, "manifest.json"))
	if err != nil {
		result.Error = err.Error()
		return result
	}
	var actual liveRunIdentity
	if err := json.Unmarshal(data, &actual); err != nil {
		result.Error = err.Error()
		return result
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(data, &fields); err != nil {
		result.Error = err.Error()
		return result
	}
	for _, field := range []string{
		"source_repo",
		"target_commit",
		"prompt_commit",
		"base_commit",
		"task_selection",
		"variant_selection",
		"profiles",
		"baseline_from",
		"model",
		"model_mode",
		"model_configuration",
	} {
		if _, ok := fields[field]; !ok {
			result.Actual = actual
			result.Error = fmt.Sprintf("run manifest omits live identity field %s", field)
			return result
		}
	}
	result.Actual = actual
	if !reflect.DeepEqual(actual, expected) {
		result.Error = "run identity differs from suite manifest"
		return result
	}
	result.Passed = true
	return result
}

func validSuiteRelativePath(value string) bool {
	return value != "" &&
		!strings.Contains(value, `\`) &&
		!path.IsAbs(value) &&
		path.Clean(value) == value &&
		value != "." &&
		!strings.HasPrefix(value, "../")
}

func validOptionalSuiteRelativePath(value string) bool {
	return value == "" || validSuiteRelativePath(value)
}

func validQualityProvenance(value string) bool {
	switch value {
	case "strict-current", "legacy-unisolated-attested", "non-strict":
		return true
	default:
		return false
	}
}

func validLocalGoPackage(value string) bool {
	if !strings.HasPrefix(value, "./") {
		return false
	}
	relative := strings.TrimPrefix(value, "./")
	if !validSuiteRelativePath(relative) {
		return false
	}
	for _, character := range relative {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			character == '-' ||
			character == '_' ||
			character == '.' ||
			character == '/' {
			continue
		}
		return false
	}
	return true
}

func parseSHA256SumLine(line string) (string, string, error) {
	if len(line) < sha256.Size*2+3 {
		return "", "", fmt.Errorf("malformed checksum entry")
	}
	digest := line[:sha256.Size*2]
	if _, err := hex.DecodeString(digest); err != nil {
		return "", "", fmt.Errorf("invalid SHA-256 digest")
	}
	if line[sha256.Size*2] != ' ' ||
		(line[sha256.Size*2+1] != ' ' && line[sha256.Size*2+1] != '*') {
		return "", "", fmt.Errorf("malformed checksum separator")
	}
	recordedPath := line[sha256.Size*2+2:]
	if recordedPath == "" {
		return "", "", fmt.Errorf("checksum path is empty")
	}
	return digest, recordedPath, nil
}

func sourceArtifactName(recordedPath string) (string, error) {
	path := filepath.FromSlash(recordedPath)
	clean := filepath.Clean(path)
	// Older evidence recorded either the source run's absolute path or a path
	// beneath a relative --evidence-root, including parent-relative roots.
	// Promotion relocates the files. The recorded path is never opened: resolve
	// core artifacts by basename and dependency artifacts by an exact allowed
	// suffix, then open only that fixed name inside runDir.
	path = filepath.Base(clean)
	for _, allowed := range sourceChecksumArtifacts {
		if path == allowed {
			return path, nil
		}
	}
	cleanSlash := filepath.ToSlash(clean)
	for _, allowed := range dependencySourceChecksumArtifacts {
		if cleanSlash == allowed || strings.HasSuffix(cleanSlash, "/"+allowed) {
			return allowed, nil
		}
	}
	return "", fmt.Errorf("unexpected source artifact %q", recordedPath)
}

func sha256File(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		_ = file.Close()
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", hasher.Sum(nil)), nil
}

var qualityAggregateFiles = []string{
	"static.json",
	"judges.json",
	"judge-usage.json",
	"quality.json",
	"summary.md",
	"inputs.json",
}

var qualityGeneratorFiles = []string{
	"experiments/lsp-replacement/quality-check.sh",
	"experiments/lsp-replacement/analyze.sh",
	"experiments/lsp-replacement/profiles.tsv",
	"cmd/repo-view-run-stats/main.go",
	"internal/runstats/runstats.go",
	"go.mod",
}

const qualityMetricsFormula = "effective = (input - cached_input) + 0.1 * cached_input + output"
const qualityGenerationIsolation = "root-deny-explicit-read-inherit-none-go-env-v3"
const qualityNoCollaboration = "Do not call collaboration, subagent, spawn-agent, or agent-wait tools. Do not read or invoke Codex skills, plugins, hooks, or marketplace resources; they are outside this benchmark."

var qualityFeatureFlags = []string{
	"--disable", "multi_agent",
	"--disable", "multi_agent_v2",
	"--disable", "enable_fanout",
	"--disable", "collaboration_modes",
	"--disable", "hooks",
	"--disable", "tool_router",
	"--disable", "workflows",
	"--disable", "code_mode",
	"--disable", "code_mode_host",
	"--disable", "code_mode_only",
}

var qualityCodexEnvironment = []string{
	"env",
	"-i",
	"PATH=<generation-path>",
	"HOME=<shell-home>",
	"CODEX_HOME=<codex-home>",
	"LANG=C",
	"LC_ALL=C",
	"TZ=UTC",
	"GOROOT=<go-root>",
	"GOPATH=<go-path>",
	"GOMODCACHE=<go-mod-cache>",
	"GOCACHE=<go-cache>",
	"GO111MODULE=on",
	"GOENV=off",
	"GOTOOLCHAIN=local",
	"GOWORK=off",
	"GOFLAGS=-mod=readonly -trimpath -buildvcs=false",
	"GOPROXY=https://proxy.golang.org,direct",
	"GONOPROXY=",
	"GOPRIVATE=",
	"GONOSUMDB=",
	"GOSUMDB=sum.golang.org",
	"GOINSECURE=",
	"GOVCS=public:git|hg,private:all",
	"GOAUTH=off",
	"GIT_CONFIG_NOSYSTEM=1",
	"GIT_CONFIG_GLOBAL=/dev/null",
	"GIT_ATTR_NOSYSTEM=1",
	"GIT_CONFIG_COUNT=10",
	"GIT_CONFIG_KEY_0=core.hooksPath",
	"GIT_CONFIG_VALUE_0=/dev/null",
	"GIT_CONFIG_KEY_1=core.attributesFile",
	"GIT_CONFIG_VALUE_1=/dev/null",
	"GIT_CONFIG_KEY_2=core.excludesFile",
	"GIT_CONFIG_VALUE_2=/dev/null",
	"GIT_CONFIG_KEY_3=core.autocrlf",
	"GIT_CONFIG_VALUE_3=false",
	"GIT_CONFIG_KEY_4=core.eol",
	"GIT_CONFIG_VALUE_4=lf",
	"GIT_CONFIG_KEY_5=core.safecrlf",
	"GIT_CONFIG_VALUE_5=false",
	"GIT_CONFIG_KEY_6=core.fsmonitor",
	"GIT_CONFIG_VALUE_6=false",
	"GIT_CONFIG_KEY_7=core.untrackedCache",
	"GIT_CONFIG_VALUE_7=false",
	"GIT_CONFIG_KEY_8=core.sparseCheckout",
	"GIT_CONFIG_VALUE_8=false",
	"GIT_CONFIG_KEY_9=core.filemode",
	"GIT_CONFIG_VALUE_9=true",
	"GIT_TERMINAL_PROMPT=0",
	"GIT_NO_REPLACE_OBJECTS=1",
	"GIT_OPTIONAL_LOCKS=0",
	"GIT_DISCOVERY_ACROSS_FILESYSTEM=0",
	"GIT_PAGER=cat",
	"PAGER=cat",
}

var qualityCodexIsolationFlags = []string{
	"--ignore-user-config",
	"--ignore-rules",
	"-c",
	`default_permissions="benchmark"`,
	"-c",
	`permissions.benchmark={extends=":read-only", filesystem={` +
		`":root"="deny", ":minimal"="read", ` +
		`"<worktree>"="read", "<go-root>"="read", ` +
		`"<go-mod-cache>"="read", "<go-cache>"="read", ` +
		`"<repo-view-cache>"="read", "<shell-home>"="read", ` +
		`"<codex-executable>"="read", ` +
		`"<codex-home>"="deny"}}`,
	"-c",
	`shell_environment_policy.inherit="none"`,
	"-c",
	"shell_environment_policy.ignore_default_excludes=false",
	"-c",
	"shell_environment_policy.experimental_use_profile=false",
	"-c",
	`shell_environment_policy.set={` +
		`PATH="<repo-view-cache>/bin:<codex-bin>:` +
		`<go-root>/bin:/usr/local/bin:/usr/bin:/bin",` +
		`HOME="<shell-home>",LANG="C",LC_ALL="C",TZ="UTC",` +
		`GOROOT="<go-root>",GOPATH="<go-path>",` +
		`GOMODCACHE="<go-mod-cache>",GOCACHE="<go-cache>",` +
		`GOENV="off",GOTOOLCHAIN="local",GOWORK="off",` +
		`GOFLAGS="-mod=readonly -trimpath -buildvcs=false",` +
		`GIT_CONFIG_NOSYSTEM="1",` +
		`GIT_CONFIG_GLOBAL="/dev/null",` +
		`GIT_ATTR_NOSYSTEM="1",GIT_CONFIG_COUNT="10",` +
		`GIT_CONFIG_KEY_0="core.hooksPath",` +
		`GIT_CONFIG_VALUE_0="/dev/null",` +
		`GIT_CONFIG_KEY_1="core.attributesFile",` +
		`GIT_CONFIG_VALUE_1="/dev/null",` +
		`GIT_CONFIG_KEY_2="core.excludesFile",` +
		`GIT_CONFIG_VALUE_2="/dev/null",` +
		`GIT_CONFIG_KEY_3="core.autocrlf",` +
		`GIT_CONFIG_VALUE_3="false",` +
		`GIT_CONFIG_KEY_4="core.eol",GIT_CONFIG_VALUE_4="lf",` +
		`GIT_CONFIG_KEY_5="core.safecrlf",` +
		`GIT_CONFIG_VALUE_5="false",` +
		`GIT_CONFIG_KEY_6="core.fsmonitor",` +
		`GIT_CONFIG_VALUE_6="false",` +
		`GIT_CONFIG_KEY_7="core.untrackedCache",` +
		`GIT_CONFIG_VALUE_7="false",` +
		`GIT_CONFIG_KEY_8="core.sparseCheckout",` +
		`GIT_CONFIG_VALUE_8="false",` +
		`GIT_CONFIG_KEY_9="core.filemode",` +
		`GIT_CONFIG_VALUE_9="true",` +
		`GIT_TERMINAL_PROMPT="0",GIT_NO_REPLACE_OBJECTS="1",` +
		`GIT_OPTIONAL_LOCKS="0",` +
		`GIT_DISCOVERY_ACROSS_FILESYSTEM="0",` +
		`GIT_PAGER="cat",PAGER="cat"}`,
	"-c",
	"project_doc_max_bytes=0",
	"-c",
	"project_doc_fallback_filenames=[]",
	"-c",
	"mcp_servers={}",
	"-c",
	"apps._default.enabled=false",
}

var qualityHostGoEnvironment = []string{
	"env",
	"-u", "GOOS",
	"-u", "GOARCH",
	"-u", "GO386",
	"-u", "GOAMD64",
	"-u", "GOARM",
	"-u", "GOARM64",
	"-u", "GOMIPS",
	"-u", "GOMIPS64",
	"-u", "GOPPC64",
	"-u", "GORISCV64",
	"-u", "GOWASM",
	"-u", "CGO_ENABLED",
	"-u", "CC",
	"-u", "CXX",
	"-u", "CGO_CFLAGS",
	"-u", "CGO_CPPFLAGS",
	"-u", "CGO_CXXFLAGS",
	"-u", "CGO_LDFLAGS",
	"-u", "PKG_CONFIG",
	"-u", "GOROOT",
	"-u", "GOPATH",
	"-u", "GOMODCACHE",
	"-u", "GOCACHE",
	"-u", "GOEXPERIMENT",
	"-u", "GODEBUG",
	"GO111MODULE=on",
	"GOENV=off",
	"GOTOOLCHAIN=local",
	"GOWORK=off",
	"GOFLAGS=-mod=readonly -trimpath -buildvcs=false",
	"GOPROXY=https://proxy.golang.org,direct",
	"GONOPROXY=",
	"GOPRIVATE=",
	"GONOSUMDB=",
	"GOSUMDB=sum.golang.org",
	"GOINSECURE=",
	"GOVCS=public:git|hg,private:all",
	"GOAUTH=off",
}

type qualityAggregateSnapshot struct {
	manifestDigest string
	outputs        map[string][]byte
	inputs         map[string][]byte
	validation     qualityValidation
}

// QualityReplayConfig is derived exclusively from a digest-verified aggregate
// snapshot. Callers use it to reproduce quality aggregation without trusting
// mutable public JSON files for judge selection or model configuration.
type QualityReplayConfig struct {
	AggregateSHA256 string
	Provenance      string
	JudgeModelMode  string
	JudgeModel      string
	JudgeRepeats    int
	Enforce         bool
}

// ReadQualityReplayConfig verifies the complete committed aggregate before
// returning any replay control recorded by it.
func ReadQualityReplayConfig(
	runDir string,
	expectedManifestDigest string,
	expectedProvenance string,
) (QualityReplayConfig, error) {
	aggregate, err := readQualityAggregate(runDir, expectedManifestDigest)
	if err != nil {
		return QualityReplayConfig{}, err
	}
	if aggregate.validation.AggregateStatus != expectedProvenance {
		return QualityReplayConfig{}, fmt.Errorf(
			"quality aggregate provenance is %q, want %q",
			aggregate.validation.AggregateStatus,
			expectedProvenance,
		)
	}
	quality, err := decodeQualityDocument(aggregate.outputs["quality.json"])
	if err != nil {
		return QualityReplayConfig{}, err
	}
	return QualityReplayConfig{
		AggregateSHA256: aggregate.manifestDigest,
		Provenance:      quality.ProvenanceStatus,
		JudgeRepeats:    quality.RequiredJudgeCount,
		JudgeModelMode:  quality.Evaluator.ModelMode,
		JudgeModel:      quality.Evaluator.Model,
		Enforce:         aggregate.validation.Enforce,
	}, nil
}

type qualityInputCommitment struct {
	Inputs                    map[string]string          `json:"inputs"`
	Snapshots                 map[string]string          `json:"snapshots"`
	Generators                map[string]string          `json:"generators"`
	AnalysisEnvironment       qualityAnalysisEnvironment `json:"analysis_environment"`
	JudgeEnvironmentSemantics []string                   `json:"judge_environment_semantics"`
	Validation                qualityValidation          `json:"validation"`
	SchemaVersion             int                        `json:"schema_version"`
}

type qualityValidation struct {
	AggregateStatus        string `json:"aggregate_status"`
	MetricsFormula         string `json:"metrics_formula"`
	GenerationIsolation    string `json:"generation_isolation"`
	GenerationConfigSHA256 string `json:"generation_config_sha256"`
	JudgeRepeats           int    `json:"judge_repeats"`
	MetricsSchemaVersion   int    `json:"metrics_schema_version"`
	JudgeCacheSchema       int    `json:"judge_cache_schema"`
	StrictEvidence         bool   `json:"strict_evidence"`
	Enforce                bool   `json:"enforce"`
	BindLegacyJudges       bool   `json:"bind_legacy_judges"`
	SkipAnalyze            bool   `json:"skip_analyze"`
}

type qualityAnalysisEnvironment struct {
	GoVersion string `json:"go_version"`
	GOENV     string `json:"GOENV"`
	GOWORK    string `json:"GOWORK"`
	GOFLAGS   string `json:"GOFLAGS"`
}

type qualityDocument struct {
	Evaluator          qualityEvaluator `json:"evaluator"`
	ProvenanceStatus   string           `json:"provenance_status"`
	Static             json.RawMessage  `json:"static"`
	Judges             json.RawMessage  `json:"judges"`
	JudgeUsage         json.RawMessage  `json:"judge_usage"`
	Verdicts           []qualityVerdict `json:"verdicts"`
	SchemaVersion      int              `json:"schema_version"`
	RequiredJudgeCount int              `json:"required_judge_count"`
}

type qualityEvaluator struct {
	Environment  qualityEvaluatorEnvironment `json:"environment"`
	Model        string                      `json:"model"`
	ModelMode    string                      `json:"model_mode,omitempty"`
	CodexVersion string                      `json:"codex_version"`
	CacheSchema  int                         `json:"cache_schema"`
}

type qualityEvaluatorEnvironment struct {
	GoVersion         string `json:"go_version"`
	PermissionProfile string `json:"permission_profile"`
	Filesystem        struct {
		Root                 string `json:"root"`
		MinimalRuntime       string `json:"minimal_runtime"`
		JudgeCheckout        string `json:"judge_checkout"`
		QualityInputSnapshot string `json:"quality_input_snapshot"`
		GOROOT               string `json:"goroot"`
		GOMODCACHE           string `json:"gomodcache"`
		JudgeToolRoot        string `json:"judge_tool_root"`
		CodexExecutable      string `json:"codex_executable"`
		CodexHome            string `json:"codex_home"`
		CanonicalAuth        string `json:"canonical_auth"`
	} `json:"filesystem"`
	Network          string `json:"network"`
	OuterEnvironment struct {
		Inherit   string `json:"inherit"`
		PATH      string `json:"PATH"`
		HOME      string `json:"HOME"`
		TMPDIR    string `json:"TMPDIR"`
		LANG      string `json:"LANG"`
		LCALL     string `json:"LC_ALL"`
		TZ        string `json:"TZ"`
		CODEXHOME string `json:"CODEX_HOME"`
		Auth      string `json:"auth"`
	} `json:"outer_environment"`
	ShellEnvironment struct {
		Inherit          string `json:"inherit"`
		PATH             string `json:"PATH"`
		HOME             string `json:"HOME"`
		TMPDIR           string `json:"TMPDIR"`
		LANG             string `json:"LANG"`
		LCALL            string `json:"LC_ALL"`
		TZ               string `json:"TZ"`
		GOROOT           string `json:"GOROOT"`
		GOPATH           string `json:"GOPATH"`
		GOMODCACHE       string `json:"GOMODCACHE"`
		GOCACHE          string `json:"GOCACHE"`
		GOENV            string `json:"GOENV"`
		GOTOOLCHAIN      string `json:"GOTOOLCHAIN"`
		GOWORK           string `json:"GOWORK"`
		GOFLAGS          string `json:"GOFLAGS"`
		GitConfiguration string `json:"git_configuration"`
	} `json:"shell_environment"`
}

type qualityVerdict struct {
	CoreConclusionMatch *bool                 `json:"core_conclusion_match"`
	JudgesNotWorse      *bool                 `json:"judges_not_worse"`
	Profile             string                `json:"profile"`
	Task                string                `json:"task"`
	NavigationCalls     staticNavigationCalls `json:"navigation_calls"`
	JudgeCount          int                   `json:"judge_count"`
	RequiredJudgeCount  int                   `json:"required_judge_count"`
	NavigationPass      bool                  `json:"navigation_pass"`
	JudgeEvaluated      bool                  `json:"judge_evaluated"`
	StaticNotWorse      bool                  `json:"static_not_worse"`
	JudgeComplete       bool                  `json:"judge_complete"`
	AccountingPass      bool                  `json:"accounting_pass"`
	NavigationRequired  bool                  `json:"navigation_required"`
	QualityPass         bool                  `json:"quality_pass"`
}

type staticDocument struct {
	Cases         []staticCase       `json:"cases"`
	Comparisons   []staticComparison `json:"comparisons"`
	SchemaVersion int                `json:"schema_version"`
}

type staticCase struct {
	Task               string                `json:"task"`
	Variant            string                `json:"variant"`
	Profile            string                `json:"profile"`
	Name               string                `json:"name"`
	Criteria           []staticCriterion     `json:"criteria"`
	NavigationCalls    staticNavigationCalls `json:"navigation_calls"`
	PassedWeight       int                   `json:"passed_weight"`
	TotalWeight        int                   `json:"total_weight"`
	ScorePercent       float64               `json:"score_percent"`
	NavigationPass     bool                  `json:"navigation_pass"`
	AccountingPass     bool                  `json:"accounting_pass"`
	NavigationRequired bool                  `json:"navigation_required"`
	RequiredPass       bool                  `json:"required_pass"`
}

type staticNavigationCalls struct {
	Total              int                  `json:"total"`
	CommandCap         int                  `json:"command_cap"`
	BudgetTamper       int                  `json:"budget_tamper"`
	Changed            int                  `json:"changed"`
	Find               int                  `json:"find"`
	Inspect            int                  `json:"inspect"`
	Outline            int                  `json:"outline"`
	BoundViolations    int                  `json:"bound_violations"`
	SimpleContract     staticSimpleContract `json:"simple_contract"`
	DeepContract       staticDeepContract   `json:"deep_contract"`
	CommandCapPass     bool                 `json:"command_cap_pass"`
	CommandCapExceeded bool                 `json:"command_cap_exceeded"`
}

type staticSimpleContract struct {
	ChangedCommandExact         bool `json:"changed_command_exact"`
	CoreInspectCommandExact     bool `json:"core_inspect_command_exact"`
	ConsumerInspectCommandExact bool `json:"consumer_inspect_command_exact"`
	InspectOutputsUntruncated   bool `json:"inspect_outputs_untruncated"`
}

type staticDeepContract struct {
	CommandSequenceExact bool `json:"command_sequence_exact"`
	DependencyAWKExact   bool `json:"dependency_awk_exact"`
}

type staticCriterion struct {
	ID       string `json:"id"`
	Weight   int    `json:"weight"`
	Required bool   `json:"required"`
	Passed   bool   `json:"passed"`
}

type staticComparison struct {
	Task                  string                `json:"task"`
	Profile               string                `json:"profile"`
	NavigationCalls       staticNavigationCalls `json:"navigation_calls"`
	BaselineScorePercent  float64               `json:"baseline_score_percent"`
	CandidateScorePercent float64               `json:"candidate_score_percent"`
	NavigationRequired    bool                  `json:"navigation_required"`
	NavigationPass        bool                  `json:"navigation_pass"`
	AccountingPass        bool                  `json:"accounting_pass"`
	RequiredPass          bool                  `json:"required_pass"`
	StaticNotWorse        bool                  `json:"static_not_worse"`
}

type qualityJudgesDocument struct {
	ProvenanceStatus string                  `json:"provenance_status"`
	JudgeRuns        []qualityJudgeRun       `json:"judge_runs"`
	Baselines        []qualityJudgeBaseline  `json:"baselines"`
	Candidates       []qualityJudgeCandidate `json:"candidates"`
}

type qualityJudgeRun struct {
	Task       string                     `json:"task"`
	Candidates []qualityJudgeRunCandidate `json:"candidates"`
	Baseline   qualityJudgeRunBaseline    `json:"baseline"`
}

type qualityJudgeRunBaseline struct {
	Name              string   `json:"name"`
	CriticalOmissions []string `json:"critical_omissions"`
	UnsupportedClaims []string `json:"unsupported_claims"`
	Correctness       int      `json:"correctness"`
	Completeness      int      `json:"completeness"`
	Grounding         int      `json:"grounding"`
	TaskAdherence     int      `json:"task_adherence"`
}

type qualityJudgeRunCandidate struct {
	Name                          string   `json:"name"`
	Rationale                     string   `json:"rationale"`
	MaterialContradictions        []string `json:"material_contradictions"`
	CriticalOmissions             []string `json:"critical_omissions"`
	UnsupportedClaims             []string `json:"unsupported_claims"`
	BaselineMaterialPointsOmitted []string `json:"baseline_material_points_omitted"`
	CandidateMaterialAdditions    []string `json:"candidate_material_additions"`
	Grounding                     int      `json:"grounding"`
	TaskAdherence                 int      `json:"task_adherence"`
	Completeness                  int      `json:"completeness"`
	Correctness                   int      `json:"correctness"`
	CoreConclusionMatchesBaseline bool     `json:"core_conclusion_matches_baseline"`
	NotWorseThanBaseline          bool     `json:"not_worse_than_baseline"`
}

type qualityJudgeBaseline struct {
	Task                 string   `json:"task"`
	Name                 string   `json:"name"`
	CriticalOmissions    []string `json:"critical_omissions"`
	UnsupportedClaims    []string `json:"unsupported_claims"`
	JudgeCount           int      `json:"judge_count"`
	AverageCorrectness   float64  `json:"average_correctness"`
	AverageCompleteness  float64  `json:"average_completeness"`
	AverageGrounding     float64  `json:"average_grounding"`
	AverageTaskAdherence float64  `json:"average_task_adherence"`
}

type qualityJudgeCandidate struct {
	Name                          string   `json:"name"`
	Task                          string   `json:"task"`
	CriticalOmissions             []string `json:"critical_omissions"`
	CandidateMaterialAdditions    []string `json:"candidate_material_additions"`
	BaselineMaterialPointsOmitted []string `json:"baseline_material_points_omitted"`
	MaterialContradictions        []string `json:"material_contradictions"`
	UnsupportedClaims             []string `json:"unsupported_claims"`
	AverageGrounding              float64  `json:"average_grounding"`
	AverageTaskAdherence          float64  `json:"average_task_adherence"`
	AverageCompleteness           float64  `json:"average_completeness"`
	AverageCorrectness            float64  `json:"average_correctness"`
	JudgeCount                    int      `json:"judge_count"`
	AllCoreConclusionMatch        bool     `json:"all_core_conclusion_match"`
	AllNotWorse                   bool     `json:"all_not_worse"`
}

type qualityJudgeUsageDocument struct {
	Formula string                 `json:"formula"`
	Runs    []qualityJudgeUsageRun `json:"runs"`
	Totals  qualityJudgeUsageTotal `json:"totals"`
}

type qualityJudgeUsageRun struct {
	Name                        string  `json:"name"`
	InputTokens                 int64   `json:"input_tokens"`
	RegularInputTokens          int64   `json:"regular_input_tokens"`
	CachedInputTokens           int64   `json:"cached_input_tokens"`
	CachedInputEquivalentTokens float64 `json:"cached_input_equivalent_tokens"`
	OutputTokens                int64   `json:"output_tokens"`
	ReasoningOutputTokens       int64   `json:"reasoning_output_tokens"`
	RawTotalTokens              int64   `json:"raw_total_tokens"`
	EffectiveTokens             float64 `json:"effective_tokens"`
}

type qualityJudgeUsageTotal struct {
	RunCount                    int     `json:"run_count"`
	InputTokens                 int64   `json:"input_tokens"`
	RegularInputTokens          int64   `json:"regular_input_tokens"`
	CachedInputTokens           int64   `json:"cached_input_tokens"`
	CachedInputEquivalentTokens float64 `json:"cached_input_equivalent_tokens"`
	OutputTokens                int64   `json:"output_tokens"`
	ReasoningOutputTokens       int64   `json:"reasoning_output_tokens"`
	RawTotalTokens              int64   `json:"raw_total_tokens"`
	EffectiveTokens             float64 `json:"effective_tokens"`
}

type qualityGenerationConfig struct {
	PromptFiles                  map[string]string `json:"prompt_files"`
	PromptDigests                map[string]string `json:"prompt_digests"`
	CasePromptFiles              map[string]string `json:"case_prompt_files"`
	CasePromptDigests            map[string]string `json:"case_prompt_digests"`
	MechanicalNavigationContract struct {
		RequiredRoot           string `json:"required_root"`
		RequiredBaseCommit     string `json:"required_base_commit"`
		RequiredChangedReturn  string `json:"required_changed_return"`
		RequiredChangedContext string `json:"required_changed_context"`
		RequireNavigation      string `json:"require_navigation_semantics"`
	} `json:"mechanical_navigation_contract"`
	ProfilesSnapshotPath         string   `json:"profiles_snapshot_path"`
	GenerationIsolation          string   `json:"generation_isolation"`
	ProfilesSnapshotSHA256       string   `json:"profiles_snapshot_sha256"`
	DeveloperInstructions        string   `json:"baseline_developer_instructions"`
	AuthSourcePermission         string   `json:"auth_source_permission"`
	CodexEnvironment             []string `json:"codex_environment"`
	HostGoEnvironment            []string `json:"host_go_environment"`
	CodexIsolationFlags          []string `json:"codex_isolation_flags"`
	FeatureFlags                 []string `json:"feature_flags"`
	MechanicalNavigationEnforced bool     `json:"mechanical_navigation_semantics_enforced"`
}

func sharedQualityGenerationConfig(
	config qualityGenerationConfig,
) qualityGenerationConfig {
	config.CasePromptFiles = nil
	config.CasePromptDigests = nil
	config.MechanicalNavigationEnforced = false
	return config
}

func validStrictGenerationConfig(
	config qualityGenerationConfig,
	expectedMechanicalNavigation bool,
) bool {
	return config.GenerationIsolation == qualityGenerationIsolation &&
		config.DeveloperInstructions == qualityNoCollaboration &&
		reflect.DeepEqual(config.FeatureFlags, qualityFeatureFlags) &&
		reflect.DeepEqual(
			config.CodexIsolationFlags,
			qualityCodexIsolationFlags,
		) &&
		reflect.DeepEqual(config.CodexEnvironment, qualityCodexEnvironment) &&
		reflect.DeepEqual(config.HostGoEnvironment, qualityHostGoEnvironment) &&
		config.ProfilesSnapshotPath == "profiles-snapshot.tsv" &&
		validSHA256Digest(config.ProfilesSnapshotSHA256) &&
		len(config.PromptFiles) > 0 &&
		len(config.PromptFiles) == len(config.PromptDigests) &&
		len(config.CasePromptFiles) > 0 &&
		len(config.CasePromptFiles) == len(config.CasePromptDigests) &&
		config.MechanicalNavigationEnforced == expectedMechanicalNavigation &&
		config.MechanicalNavigationContract.RequiredRoot == "<worktree>" &&
		config.MechanicalNavigationContract.RequiredBaseCommit == "<resolved-base>" &&
		config.MechanicalNavigationContract.RequiredChangedReturn == "<profile-return>" &&
		config.MechanicalNavigationContract.RequiredChangedContext == "<profile-context>" &&
		config.MechanicalNavigationContract.RequireNavigation == "1" &&
		config.AuthSourcePermission == "deny-if-present"
}

func equivalentGenerationConfigsExceptMechanicalNavigation(
	left, right []byte,
) (bool, error) {
	decode := func(data []byte) (map[string]any, error) {
		decoder := json.NewDecoder(bytes.NewReader(data))
		decoder.UseNumber()
		var config map[string]any
		if err := decoder.Decode(&config); err != nil {
			return nil, err
		}
		var trailing any
		if err := decoder.Decode(&trailing); err != io.EOF {
			if err == nil {
				return nil, fmt.Errorf("multiple generation config values")
			}
			return nil, err
		}
		delete(config, "mechanical_navigation_semantics_enforced")
		return config, nil
	}
	leftConfig, err := decode(left)
	if err != nil {
		return false, err
	}
	rightConfig, err := decode(right)
	if err != nil {
		return false, err
	}
	return reflect.DeepEqual(leftConfig, rightConfig), nil
}

func decodeStrictJSON(content []byte, destination any) error {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func decodeQualityDocument(content []byte) (qualityDocument, error) {
	var document qualityDocument
	if err := decodeStrictJSON(content, &document); err != nil {
		return qualityDocument{}, fmt.Errorf("decode quality.json: %w", err)
	}
	if document.SchemaVersion != 4 && document.SchemaVersion != 5 {
		return qualityDocument{}, fmt.Errorf(
			"quality.json schema is %d, want 4 or 5",
			document.SchemaVersion,
		)
	}
	if document.SchemaVersion == 4 {
		if document.Evaluator.ModelMode != "" {
			return qualityDocument{}, fmt.Errorf(
				"quality.json schema 4 unexpectedly records model_mode",
			)
		}
		if document.Evaluator.Model == "router-selected" {
			document.Evaluator.ModelMode = "router"
		} else {
			document.Evaluator.ModelMode = "pinned"
		}
	} else if document.Evaluator.ModelMode == "" {
		return qualityDocument{}, fmt.Errorf(
			"quality.json schema 5 omits evaluator model_mode",
		)
	}
	if document.RequiredJudgeCount < 0 || document.RequiredJudgeCount > 100 ||
		document.Evaluator.CacheSchema != 8 ||
		document.Evaluator.CodexVersion == "" {
		return qualityDocument{}, fmt.Errorf(
			"quality.json has incompatible evaluator semantics",
		)
	}
	switch document.Evaluator.ModelMode {
	case "router":
		if document.Evaluator.Model != "router-selected" {
			return qualityDocument{}, fmt.Errorf(
				"quality.json has inconsistent router evaluator identity",
			)
		}
	case "pinned":
		if !validQualityModelIdentity(document.Evaluator.Model) {
			return qualityDocument{}, fmt.Errorf(
				"quality.json has invalid pinned evaluator identity",
			)
		}
	default:
		return qualityDocument{}, fmt.Errorf(
			"quality.json has invalid evaluator model mode %q",
			document.Evaluator.ModelMode,
		)
	}
	if document.Evaluator.Environment.GoVersion == "" ||
		document.Evaluator.Environment.PermissionProfile != "quality-audit" ||
		document.Evaluator.Environment.Network != "disabled" ||
		document.Evaluator.Environment.Filesystem.Root != "deny" ||
		document.Evaluator.Environment.Filesystem.JudgeCheckout != "read" ||
		document.Evaluator.Environment.OuterEnvironment.Inherit != "none" ||
		document.Evaluator.Environment.ShellEnvironment.Inherit != "none" {
		return qualityDocument{}, fmt.Errorf(
			"quality.json has incompatible evaluator environment",
		)
	}
	if document.Verdicts == nil {
		return qualityDocument{}, fmt.Errorf("quality.json omits verdicts")
	}
	return document, nil
}

func validQualityModelIdentity(value string) bool {
	if value == "" || len(value) > 256 || value[0] == '-' {
		return false
	}
	for index := range len(value) {
		if value[index] < 0x21 || value[index] > 0x7e {
			return false
		}
	}
	return true
}

func semanticJSONEqual(left, right []byte) bool {
	var leftValue any
	var rightValue any
	leftDecoder := json.NewDecoder(bytes.NewReader(left))
	leftDecoder.UseNumber()
	rightDecoder := json.NewDecoder(bytes.NewReader(right))
	rightDecoder.UseNumber()
	return leftDecoder.Decode(&leftValue) == nil &&
		rightDecoder.Decode(&rightValue) == nil &&
		reflect.DeepEqual(leftValue, rightValue)
}

func requireJSONObjectKeys(
	content []byte,
	expected ...string,
) (map[string]json.RawMessage, error) {
	var object map[string]json.RawMessage
	if err := json.Unmarshal(content, &object); err != nil {
		return nil, err
	}
	if len(object) != len(expected) {
		return nil, fmt.Errorf("unexpected object field set")
	}
	for _, key := range expected {
		if _, ok := object[key]; !ok {
			return nil, fmt.Errorf("object omits %s", key)
		}
	}
	return object, nil
}

func validJudgeScore(score int) bool {
	return score >= 1 && score <= 5
}

func validStringSet(values []string) bool {
	return values != nil
}

func validateJudgeRun(run qualityJudgeRun) error {
	if run.Task == "" || run.Baseline.Name != "baseline-"+run.Task ||
		!validJudgeScore(run.Baseline.Correctness) ||
		!validJudgeScore(run.Baseline.Completeness) ||
		!validJudgeScore(run.Baseline.Grounding) ||
		!validJudgeScore(run.Baseline.TaskAdherence) ||
		!validStringSet(run.Baseline.CriticalOmissions) ||
		!validStringSet(run.Baseline.UnsupportedClaims) ||
		run.Candidates == nil || len(run.Candidates) == 0 {
		return fmt.Errorf("judge run for %q has an invalid baseline or score schema", run.Task)
	}
	seen := make(map[string]bool, len(run.Candidates))
	for _, candidate := range run.Candidates {
		if candidate.Name == "" || seen[candidate.Name] ||
			!validJudgeScore(candidate.Correctness) ||
			!validJudgeScore(candidate.Completeness) ||
			!validJudgeScore(candidate.Grounding) ||
			!validJudgeScore(candidate.TaskAdherence) ||
			!validStringSet(candidate.CriticalOmissions) ||
			!validStringSet(candidate.UnsupportedClaims) ||
			!validStringSet(candidate.MaterialContradictions) ||
			!validStringSet(candidate.BaselineMaterialPointsOmitted) ||
			!validStringSet(candidate.CandidateMaterialAdditions) ||
			candidate.Rationale == "" {
			return fmt.Errorf("judge run for %q has an invalid candidate schema", run.Task)
		}
		if candidate.NotWorseThanBaseline &&
			(candidate.Correctness < run.Baseline.Correctness ||
				candidate.Completeness < run.Baseline.Completeness ||
				candidate.Grounding < run.Baseline.Grounding ||
				candidate.TaskAdherence < run.Baseline.TaskAdherence ||
				len(candidate.CriticalOmissions) != 0 ||
				len(candidate.UnsupportedClaims) != 0 ||
				len(candidate.MaterialContradictions) != 0 ||
				len(candidate.BaselineMaterialPointsOmitted) != 0) {
			return fmt.Errorf(
				"judge run for %q claims not-worse with a material regression",
				run.Task,
			)
		}
		seen[candidate.Name] = true
	}
	return nil
}

func validateJudgeUsageRun(run qualityJudgeUsageRun) error {
	if run.Name == "" || run.InputTokens < 0 || run.RegularInputTokens < 0 ||
		run.CachedInputTokens < 0 || run.OutputTokens < 0 ||
		run.ReasoningOutputTokens < 0 ||
		run.CachedInputTokens > run.InputTokens ||
		run.RegularInputTokens != run.InputTokens-run.CachedInputTokens ||
		run.RawTotalTokens != run.InputTokens+run.OutputTokens ||
		!equalFloat(
			run.CachedInputEquivalentTokens,
			float64(run.CachedInputTokens)*0.1,
		) ||
		!equalFloat(
			run.EffectiveTokens,
			float64(run.RegularInputTokens)+
				run.CachedInputEquivalentTokens+
				float64(run.OutputTokens),
		) {
		return fmt.Errorf("judge usage run %q has inconsistent token accounting", run.Name)
	}
	return nil
}

func equalFloat(left, right float64) bool {
	return !math.IsNaN(left) && !math.IsInf(left, 0) && left == right
}

func judgeTaskAndRepeat(name string) (string, int, bool) {
	if !strings.HasPrefix(name, "judge-") {
		return "", 0, false
	}
	remainder := strings.TrimPrefix(name, "judge-")
	separator := strings.LastIndexByte(remainder, '-')
	if separator <= 0 {
		return "", 0, false
	}
	repeat, err := strconv.Atoi(remainder[separator+1:])
	if err != nil || repeat < 1 || repeat > 100 {
		return "", 0, false
	}
	task := remainder[:separator]
	switch task {
	case "explain", "review", "deep-explain", "deep-review":
		return task, repeat, true
	default:
		return "", 0, false
	}
}

func validateQualityOutputSemantics(
	snapshot *qualityAggregateSnapshot,
	commitment qualityInputCommitment,
) error {
	qualityObject, err := requireJSONObjectKeys(
		snapshot.outputs["quality.json"],
		"schema_version",
		"provenance_status",
		"required_judge_count",
		"evaluator",
		"static",
		"judges",
		"judge_usage",
		"verdicts",
	)
	if err != nil {
		return fmt.Errorf("quality.json has an invalid exact schema: %w", err)
	}
	quality, err := decodeQualityDocument(snapshot.outputs["quality.json"])
	if err != nil {
		return err
	}
	evaluatorKeys := []string{
		"model", "codex_version", "cache_schema", "environment",
	}
	if quality.SchemaVersion == 5 {
		evaluatorKeys = append(evaluatorKeys, "model_mode")
	}
	if _, err := requireJSONObjectKeys(
		qualityObject["evaluator"],
		evaluatorKeys...,
	); err != nil {
		return fmt.Errorf("quality.json evaluator has an invalid exact schema: %w", err)
	}
	if quality.ProvenanceStatus != commitment.Validation.AggregateStatus ||
		quality.RequiredJudgeCount != commitment.Validation.JudgeRepeats ||
		quality.Evaluator.CacheSchema != commitment.Validation.JudgeCacheSchema {
		return fmt.Errorf("quality.json disagrees with committed validation semantics")
	}

	if _, err := requireJSONObjectKeys(
		snapshot.outputs["judges.json"],
		"provenance_status", "judge_runs", "baselines", "candidates",
	); err != nil {
		return fmt.Errorf("judges.json has an invalid exact schema: %w", err)
	}
	var judges qualityJudgesDocument
	if err := decodeStrictJSON(snapshot.outputs["judges.json"], &judges); err != nil {
		return fmt.Errorf("decode judges.json: %w", err)
	}
	if judges.ProvenanceStatus != commitment.Validation.AggregateStatus ||
		judges.JudgeRuns == nil || judges.Baselines == nil || judges.Candidates == nil {
		return fmt.Errorf("judges.json has inconsistent provenance or collections")
	}
	for _, run := range judges.JudgeRuns {
		if err := validateJudgeRun(run); err != nil {
			return err
		}
	}
	if err := validateJudgeMembership(snapshot.inputs["metrics.json"], judges, commitment); err != nil {
		return err
	}

	if _, err := requireJSONObjectKeys(
		snapshot.outputs["judge-usage.json"],
		"formula", "runs", "totals",
	); err != nil {
		return fmt.Errorf("judge-usage.json has an invalid exact schema: %w", err)
	}
	var usage qualityJudgeUsageDocument
	if err := decodeStrictJSON(snapshot.outputs["judge-usage.json"], &usage); err != nil {
		return fmt.Errorf("decode judge-usage.json: %w", err)
	}
	if usage.Formula != qualityMetricsFormula || usage.Runs == nil ||
		usage.Totals.RunCount != len(usage.Runs) ||
		len(usage.Runs) != len(judges.JudgeRuns) {
		return fmt.Errorf("judge-usage.json has incompatible run accounting")
	}
	total := qualityJudgeUsageTotal{RunCount: len(usage.Runs)}
	seenUsage := make(map[string]bool, len(usage.Runs))
	for _, run := range usage.Runs {
		if seenUsage[run.Name] {
			return fmt.Errorf("judge-usage.json repeats run %q", run.Name)
		}
		seenUsage[run.Name] = true
		if err := validateJudgeUsageRun(run); err != nil {
			return err
		}
		total.InputTokens += run.InputTokens
		total.RegularInputTokens += run.RegularInputTokens
		total.CachedInputTokens += run.CachedInputTokens
		total.CachedInputEquivalentTokens += run.CachedInputEquivalentTokens
		total.OutputTokens += run.OutputTokens
		total.ReasoningOutputTokens += run.ReasoningOutputTokens
		total.RawTotalTokens += run.RawTotalTokens
		total.EffectiveTokens += run.EffectiveTokens
	}
	if !reflect.DeepEqual(total, usage.Totals) {
		return fmt.Errorf("judge-usage.json totals disagree with its runs")
	}

	if !semanticJSONEqual(quality.Judges, snapshot.outputs["judges.json"]) ||
		!semanticJSONEqual(quality.JudgeUsage, snapshot.outputs["judge-usage.json"]) ||
		!semanticJSONEqual(quality.Static, snapshot.outputs["static.json"]) {
		return fmt.Errorf("quality.json embeds outputs from a different aggregation")
	}
	if err := validateStaticAndVerdicts(snapshot, quality, judges); err != nil {
		return err
	}
	if commitment.Validation.StrictEvidence {
		if commitment.Validation.JudgeRepeats == 0 && len(judges.JudgeRuns) != 0 {
			return fmt.Errorf(
				"strict aggregate records judge_repeats=0 but contains judge runs",
			)
		}
		if len(judges.JudgeRuns) == 0 && commitment.Validation.JudgeRepeats != 0 {
			return fmt.Errorf("strict judges.json omits required judge runs")
		}
		if len(judges.JudgeRuns) != len(usage.Runs) {
			return fmt.Errorf("strict judge run and usage counts disagree")
		}
		for _, verdict := range quality.Verdicts {
			if verdict.RequiredJudgeCount != commitment.Validation.JudgeRepeats ||
				verdict.JudgeCount < 0 ||
				verdict.JudgeComplete !=
					(commitment.Validation.JudgeRepeats == 0 ||
						verdict.JudgeCount >= commitment.Validation.JudgeRepeats) {
				return fmt.Errorf("quality.json verdict has inconsistent judge counts")
			}
		}
	}
	if err := validateCommittedJudgeSidecars(
		snapshot,
		commitment,
		quality,
		judges,
		usage,
	); err != nil {
		return err
	}
	return nil
}

func validateStaticAndVerdicts(
	snapshot *qualityAggregateSnapshot,
	quality qualityDocument,
	judges qualityJudgesDocument,
) error {
	if _, err := requireJSONObjectKeys(
		snapshot.outputs["static.json"],
		"schema_version", "cases", "comparisons",
	); err != nil {
		return fmt.Errorf("static.json has an invalid exact schema: %w", err)
	}
	var static staticDocument
	if err := decodeStrictJSON(snapshot.outputs["static.json"], &static); err != nil {
		return fmt.Errorf("decode static.json: %w", err)
	}
	if static.SchemaVersion != 1 || static.Cases == nil || static.Comparisons == nil {
		return fmt.Errorf("static.json has incompatible schema or collections")
	}
	type metricCase struct {
		AnswerFile                   string `json:"answer_file"`
		Task                         string `json:"task"`
		Variant                      string `json:"variant"`
		Profile                      string `json:"profile"`
		Name                         string `json:"name"`
		BoundViolations              int    `json:"repo_view_bound_violation_count"`
		OutlineCalls                 int    `json:"repo_view_outline_invocation_count"`
		CommandCap                   int    `json:"repo_view_invocation_cap"`
		RepoViewCalls                int    `json:"repo_view_invocation_count"`
		BudgetTamper                 int    `json:"repo_view_budget_tamper_command_count"`
		ChangedCalls                 int    `json:"repo_view_changed_invocation_count"`
		FindCalls                    int    `json:"repo_view_find_invocation_count"`
		InspectCalls                 int    `json:"repo_view_inspect_invocation_count"`
		SimpleCoreInspectExact       bool   `json:"repo_view_simple_core_inspect_command_exact"`
		DeepDependencyAWKExact       bool   `json:"repo_view_deep_dependency_awk_exact"`
		SimpleChangedCommandExact    bool   `json:"repo_view_simple_changed_command_exact"`
		CommandCapExceeded           bool   `json:"repo_view_invocation_cap_exceeded"`
		SimpleConsumerInspectExact   bool   `json:"repo_view_simple_consumer_inspect_command_exact"`
		SimpleOutputsUntruncated     bool   `json:"repo_view_simple_inspect_outputs_untruncated"`
		DeepCommandSequenceExact     bool   `json:"repo_view_deep_command_sequence_exact"`
		Completed                    bool   `json:"completed"`
		ToolAccounting               bool   `json:"tool_call_accounting_valid"`
		InvocationAccounting         bool   `json:"repo_view_invocation_accounting_valid"`
		RepoViewToolAccounting       bool   `json:"repo_view_tool_call_accounting_valid"`
		BudgetAccounting             bool   `json:"repo_view_budget_accounting_valid"`
		CommandShapeValid            bool   `json:"repo_view_command_shape_valid"`
		FirstInvocationChanged       bool   `json:"repo_view_first_invocation_changed"`
		NavigationSemanticsValid     bool   `json:"repo_view_navigation_semantics_valid"`
		MechanicalNavigationEnforced bool   `json:"mechanical_navigation_semantics_enforced"`
	}
	var metrics struct {
		Cases []metricCase `json:"cases"`
	}
	if err := json.Unmarshal(snapshot.inputs["metrics.json"], &metrics); err != nil {
		return fmt.Errorf("decode metrics for static validation: %w", err)
	}
	var rubric struct {
		Tasks map[string]struct {
			Criteria []struct {
				ID       string   `json:"id"`
				AllOf    []string `json:"all_of"`
				NoneOf   []string `json:"none_of"`
				Weight   int      `json:"weight"`
				Required bool     `json:"required"`
			} `json:"criteria"`
		} `json:"tasks"`
	}
	rubricBytes := snapshot.inputs[qualityEvaluatorBundlePath("quality-rubric.json")]
	if err := json.Unmarshal(rubricBytes, &rubric); err != nil || len(rubric.Tasks) == 0 {
		return fmt.Errorf("decode committed quality rubric for static validation")
	}
	expectedCases := make([]staticCase, 0, len(metrics.Cases))
	for _, metric := range metrics.Cases {
		if !metric.Completed {
			continue
		}
		answer, ok := snapshot.inputs[metric.AnswerFile]
		if !ok {
			return fmt.Errorf("static case %s omits its committed answer", metric.Name)
		}
		rubricTask, ok := rubric.Tasks[metric.Task]
		if !ok || len(rubricTask.Criteria) == 0 {
			return fmt.Errorf("static case %s has no task rubric", metric.Name)
		}
		criteria := make([]staticCriterion, len(rubricTask.Criteria))
		passedWeight := 0
		totalWeight := 0
		for index, criterion := range rubricTask.Criteria {
			passed := true
			for _, pattern := range criterion.AllOf {
				matched, err := regexp.MatchString("(?is)"+pattern, string(answer))
				if err != nil || !matched {
					passed = false
					break
				}
			}
			if passed {
				for _, pattern := range criterion.NoneOf {
					matched, err := regexp.MatchString("(?is)"+pattern, string(answer))
					if err != nil || matched {
						passed = false
						break
					}
				}
			}
			criteria[index] = staticCriterion{
				ID: criterion.ID, Weight: criterion.Weight,
				Required: criterion.Required, Passed: passed,
			}
			totalWeight += criterion.Weight
			if passed {
				passedWeight += criterion.Weight
			}
		}
		optimized := metric.Variant == "optimized"
		accountingPass := !optimized ||
			(metric.ToolAccounting && metric.InvocationAccounting &&
				metric.RepoViewToolAccounting && metric.CommandShapeValid &&
				metric.MechanicalNavigationEnforced &&
				(metric.CommandCap == 0 || metric.BudgetAccounting))
		deepTask := strings.HasPrefix(metric.Task, "deep-")
		navigationPass := !optimized ||
			(accountingPass && metric.RepoViewCalls >= 1 &&
				metric.ChangedCalls == 1 && metric.FirstInvocationChanged &&
				metric.NavigationSemanticsValid && metric.BoundViolations == 0 &&
				metric.BudgetTamper == 0 &&
				(!deepTask ||
					(metric.FindCalls >= 1 &&
						metric.InspectCalls+metric.OutlineCalls >= 1 &&
						metric.CommandCap > 0 && !metric.CommandCapExceeded &&
						metric.RepoViewCalls <= metric.CommandCap)))
		commandCapPass := ((!optimized || !deepTask) && metric.CommandCap == 0) ||
			(metric.CommandCap > 0 && !metric.CommandCapExceeded &&
				metric.RepoViewCalls <= metric.CommandCap)
		navigationCalls := staticNavigationCalls{
			Total: metric.RepoViewCalls, CommandCap: metric.CommandCap,
			CommandCapPass:     commandCapPass,
			CommandCapExceeded: metric.CommandCapExceeded,
			BudgetTamper:       metric.BudgetTamper, Changed: metric.ChangedCalls,
			Find: metric.FindCalls, Inspect: metric.InspectCalls,
			Outline: metric.OutlineCalls, BoundViolations: metric.BoundViolations,
			SimpleContract: staticSimpleContract{
				ChangedCommandExact:         metric.SimpleChangedCommandExact,
				CoreInspectCommandExact:     metric.SimpleCoreInspectExact,
				ConsumerInspectCommandExact: metric.SimpleConsumerInspectExact,
				InspectOutputsUntruncated:   metric.SimpleOutputsUntruncated,
			},
			DeepContract: staticDeepContract{
				CommandSequenceExact: metric.DeepCommandSequenceExact,
				DependencyAWKExact:   metric.DeepDependencyAWKExact,
			},
		}
		requiredCriteriaPass := true
		for _, criterion := range criteria {
			if criterion.Required && !criterion.Passed {
				requiredCriteriaPass = false
			}
		}
		score := float64(passedWeight) / float64(totalWeight) * 100
		expectedCases = append(expectedCases, staticCase{
			Name: metric.Name, Task: metric.Task, Variant: metric.Variant,
			Profile: metric.Profile, NavigationRequired: optimized,
			AccountingPass: accountingPass, NavigationPass: navigationPass,
			NavigationCalls: navigationCalls, Criteria: criteria,
			PassedWeight: passedWeight, TotalWeight: totalWeight,
			ScorePercent: score,
			RequiredPass: accountingPass && navigationPass && requiredCriteriaPass,
		})
	}
	if !reflect.DeepEqual(static.Cases, expectedCases) {
		return fmt.Errorf("static.json cases disagree with metrics, answers, or rubric")
	}
	baselineByTask := make(map[string]staticCase)
	for _, current := range expectedCases {
		if current.Variant == "baseline" {
			if _, exists := baselineByTask[current.Task]; !exists {
				baselineByTask[current.Task] = current
			}
		}
	}
	var expectedComparisons []staticComparison
	for _, current := range expectedCases {
		if current.Variant != "optimized" {
			continue
		}
		baseline, ok := baselineByTask[current.Task]
		if !ok {
			continue
		}
		expectedComparisons = append(expectedComparisons, staticComparison{
			Task: current.Task, Profile: current.Profile,
			BaselineScorePercent:  baseline.ScorePercent,
			CandidateScorePercent: current.ScorePercent,
			NavigationRequired:    current.NavigationRequired,
			NavigationPass:        current.NavigationPass,
			AccountingPass:        current.AccountingPass,
			NavigationCalls:       current.NavigationCalls,
			RequiredPass:          current.RequiredPass,
			StaticNotWorse: current.RequiredPass &&
				current.ScorePercent >= baseline.ScorePercent,
		})
	}
	if expectedComparisons == nil {
		expectedComparisons = []staticComparison{}
	}
	if !reflect.DeepEqual(static.Comparisons, expectedComparisons) {
		return fmt.Errorf("static.json comparisons disagree with static cases")
	}
	judgeByName := make(map[string]qualityJudgeCandidate)
	for _, judge := range judges.Candidates {
		judgeByName[judge.Task+"\x00"+judge.Name] = judge
	}
	expectedVerdicts := make([]qualityVerdict, 0, len(expectedComparisons))
	for _, comparison := range expectedComparisons {
		candidateName := "optimized-" + comparison.Profile + "-" + comparison.Task
		if comparison.Profile == "default" {
			candidateName = "optimized-" + comparison.Task
		}
		judge, evaluated := judgeByName[comparison.Task+"\x00"+candidateName]
		judgeCount := 0
		var judgesNotWorse *bool
		var coreMatch *bool
		if evaluated {
			judgeCount = judge.JudgeCount
			notWorse := judge.AllNotWorse
			core := judge.AllCoreConclusionMatch
			judgesNotWorse = &notWorse
			coreMatch = &core
		}
		judgeComplete := quality.RequiredJudgeCount == 0 ||
			(evaluated && judgeCount >= quality.RequiredJudgeCount)
		expectedVerdicts = append(expectedVerdicts, qualityVerdict{
			Task: comparison.Task, Profile: comparison.Profile,
			NavigationRequired: comparison.NavigationRequired,
			NavigationPass:     comparison.NavigationPass,
			AccountingPass:     comparison.AccountingPass,
			NavigationCalls:    comparison.NavigationCalls,
			StaticNotWorse:     comparison.StaticNotWorse,
			JudgeEvaluated:     evaluated, JudgeCount: judgeCount,
			RequiredJudgeCount: quality.RequiredJudgeCount,
			JudgeComplete:      judgeComplete, JudgesNotWorse: judgesNotWorse,
			CoreConclusionMatch: coreMatch,
			QualityPass: comparison.StaticNotWorse && judgeComplete &&
				(!evaluated || judge.AllNotWorse),
		})
	}
	if !reflect.DeepEqual(quality.Verdicts, expectedVerdicts) {
		return fmt.Errorf("quality.json verdicts disagree with static and judge outputs")
	}
	return nil
}

func validateJudgeMembership(
	metricsBytes []byte,
	judges qualityJudgesDocument,
	commitment qualityInputCommitment,
) error {
	var metrics struct {
		Cases []struct {
			Name      string `json:"name"`
			Task      string `json:"task"`
			Variant   string `json:"variant"`
			Completed bool   `json:"completed"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(metricsBytes, &metrics); err != nil {
		return fmt.Errorf("decode metrics for judge membership: %w", err)
	}
	baselines := make(map[string]bool)
	candidates := make(map[string]map[string]bool)
	for _, current := range metrics.Cases {
		if !current.Completed {
			continue
		}
		switch current.Variant {
		case "baseline":
			baselines[current.Task] = current.Name == "baseline-"+current.Task
		case "optimized":
			if candidates[current.Task] == nil {
				candidates[current.Task] = make(map[string]bool)
			}
			candidates[current.Task][current.Name] = true
		}
	}
	runCounts := make(map[string]int)
	for _, run := range judges.JudgeRuns {
		expected := candidates[run.Task]
		if !baselines[run.Task] || len(expected) == 0 ||
			len(run.Candidates) != len(expected) {
			return fmt.Errorf("judge run task %s is not a completed metrics matrix", run.Task)
		}
		for _, candidate := range run.Candidates {
			if !expected[candidate.Name] {
				return fmt.Errorf(
					"judge run task %s contains unknown candidate %s",
					run.Task,
					candidate.Name,
				)
			}
		}
		runCounts[run.Task]++
	}
	if commitment.Validation.StrictEvidence && commitment.Validation.JudgeRepeats > 0 {
		for task, expected := range candidates {
			if baselines[task] && len(expected) > 0 &&
				runCounts[task] != commitment.Validation.JudgeRepeats {
				return fmt.Errorf(
					"strict judge task %s has %d runs, want %d",
					task,
					runCounts[task],
					commitment.Validation.JudgeRepeats,
				)
			}
		}
	}
	return nil
}

func validateCommittedJudgeSidecars(
	snapshot *qualityAggregateSnapshot,
	commitment qualityInputCommitment,
	quality qualityDocument,
	judges qualityJudgesDocument,
	usage qualityJudgeUsageDocument,
) error {
	expectedPaths := make(map[string]bool)
	strictRepeats := make(map[string]map[int]bool)
	for index, usageRun := range usage.Runs {
		task, repeat, ok := judgeTaskAndRepeat(usageRun.Name)
		if !ok || judges.JudgeRuns[index].Task != task {
			return fmt.Errorf(
				"judge usage run %q is not bound to its judges.json run",
				usageRun.Name,
			)
		}
		if strictRepeats[task] == nil {
			strictRepeats[task] = make(map[int]bool)
		}
		if strictRepeats[task][repeat] {
			return fmt.Errorf("judge task %s repeats slot %d", task, repeat)
		}
		strictRepeats[task][repeat] = true

		currentSuffixes := []string{
			"json", "jsonl", "exit-code", "inputs.sha256", "result.sha256",
		}
		legacySuffixes := []string{
			"json", "jsonl", "exit-code", "legacy-attestation.json",
		}
		currentComplete := completeJudgeSidecarSet(
			snapshot.inputs,
			usageRun.Name,
			currentSuffixes,
		)
		legacyComplete := completeJudgeSidecarSet(
			snapshot.inputs,
			usageRun.Name,
			legacySuffixes,
		)
		if commitment.Validation.StrictEvidence && !currentComplete {
			return fmt.Errorf(
				"strict judge %s omits a current-cache sidecar",
				usageRun.Name,
			)
		}
		if !currentComplete && !legacyComplete {
			return fmt.Errorf("judge %s has an incomplete sidecar set", usageRun.Name)
		}
		selectedSuffixes := currentSuffixes
		if !currentComplete {
			selectedSuffixes = legacySuffixes
		}
		for _, suffix := range selectedSuffixes {
			expectedPaths["quality/"+usageRun.Name+"."+suffix] = true
		}
		output := snapshot.inputs["quality/"+usageRun.Name+".json"]
		log := snapshot.inputs["quality/"+usageRun.Name+".jsonl"]
		exitCode := snapshot.inputs["quality/"+usageRun.Name+".exit-code"]
		if strings.TrimSpace(string(exitCode)) != "0" ||
			!semanticJSONEqual(output, mustMarshalJSON(judges.JudgeRuns[index])) {
			return fmt.Errorf("judge %s output or exit code is not bound to judges.json", usageRun.Name)
		}
		logUsage, finalOutput, err := readCommittedJudgeLog(log)
		if err != nil {
			return fmt.Errorf("judge %s transcript: %w", usageRun.Name, err)
		}
		logUsage.Name = usageRun.Name
		if !semanticJSONEqual(output, finalOutput) ||
			!reflect.DeepEqual(logUsage, usageRun) {
			return fmt.Errorf(
				"judge %s transcript is not bound to its output and usage",
				usageRun.Name,
			)
		}
		if currentComplete {
			inputDigest := strings.TrimSpace(string(
				snapshot.inputs["quality/"+usageRun.Name+".inputs.sha256"],
			))
			expectedInputDigest, err := expectedJudgeInputDigest(
				snapshot,
				commitment,
				quality,
				usageRun.Name,
			)
			if err != nil {
				return err
			}
			storedResult := strings.TrimSpace(string(
				snapshot.inputs["quality/"+usageRun.Name+".result.sha256"],
			))
			expectedResult := committedJudgeResultDigest(
				usageRun.Name,
				inputDigest,
				output,
				log,
				"0",
			)
			if !validSHA256Digest(inputDigest) ||
				inputDigest != expectedInputDigest ||
				storedResult != expectedResult {
				return fmt.Errorf(
					"judge %s has an invalid input/result binding",
					usageRun.Name,
				)
			}
		} else {
			attestation := snapshot.inputs["quality/"+usageRun.Name+".legacy-attestation.json"]
			expectedInputDigest, err := expectedLegacyJudgeInputDigest(
				snapshot,
				usageRun.Name,
			)
			if err != nil {
				return err
			}
			if err := validateLegacyJudgeAttestation(
				attestation,
				usageRun.Name,
				expectedInputDigest,
				output,
				log,
				exitCode,
			); err != nil {
				return err
			}
		}
	}
	if commitment.Validation.StrictEvidence {
		if commitment.Validation.JudgeRepeats == 0 && len(usage.Runs) != 0 {
			return fmt.Errorf(
				"strict aggregate records judge_repeats=0 but contains judge runs",
			)
		}
		for task, repeats := range strictRepeats {
			if len(repeats) != commitment.Validation.JudgeRepeats {
				return fmt.Errorf(
					"strict judge task %s has %d runs, want %d",
					task,
					len(repeats),
					commitment.Validation.JudgeRepeats,
				)
			}
			for repeat := 1; repeat <= commitment.Validation.JudgeRepeats; repeat++ {
				if !repeats[repeat] {
					return fmt.Errorf(
						"strict judge task %s omits repeat %d",
						task,
						repeat,
					)
				}
			}
		}
	}
	for relative := range commitment.Inputs {
		if strings.HasPrefix(relative, "quality/judge-") &&
			!expectedPaths[relative] {
			return fmt.Errorf("quality input commits unselected judge sidecar %s", relative)
		}
	}
	return validateJudgeAggregates(judges)
}

func completeJudgeSidecarSet(
	inputs map[string][]byte,
	name string,
	suffixes []string,
) bool {
	for _, suffix := range suffixes {
		if _, ok := inputs["quality/"+name+"."+suffix]; !ok {
			return false
		}
	}
	return true
}

func mustMarshalJSON(value any) []byte {
	content, _ := json.Marshal(value)
	return content
}

func committedJudgeResultDigest(
	name string,
	inputDigest string,
	output []byte,
	log []byte,
	exitCode string,
) string {
	var binding bytes.Buffer
	fmt.Fprintf(&binding, "judge-cache-schema\x008\x00")
	fmt.Fprintf(&binding, "judge-identity\x00%s\x00", name)
	fmt.Fprintf(&binding, "input-digest\x00%s\x00", inputDigest)
	fmt.Fprintf(&binding, "output-sha256\x00%s\x00", sha256Bytes(output))
	fmt.Fprintf(&binding, "jsonl-sha256\x00%s\x00", sha256Bytes(log))
	fmt.Fprintf(&binding, "exit-code\x00%s\x00", exitCode)
	return sha256Bytes(binding.Bytes())
}

func expectedJudgeInputDigest(
	snapshot *qualityAggregateSnapshot,
	commitment qualityInputCommitment,
	quality qualityDocument,
	judgeName string,
) (string, error) {
	task, _, ok := judgeTaskAndRepeat(judgeName)
	if !ok {
		return "", fmt.Errorf("invalid committed judge identity %q", judgeName)
	}
	var manifest struct {
		PromptFiles     map[string]string `json:"prompt_files"`
		CasePromptFiles map[string]string `json:"case_prompt_files"`
		TargetCommit    string            `json:"target_commit"`
	}
	if err := json.Unmarshal(snapshot.inputs["manifest.json"], &manifest); err != nil ||
		!validGitCommit(manifest.TargetCommit) {
		return "", fmt.Errorf("committed judge %s has no valid target commit", judgeName)
	}
	var metrics struct {
		Cases []struct {
			Name       string `json:"name"`
			Task       string `json:"task"`
			Variant    string `json:"variant"`
			Profile    string `json:"profile"`
			AnswerFile string `json:"answer_file"`
			Completed  bool   `json:"completed"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(snapshot.inputs["metrics.json"], &metrics); err != nil {
		return "", fmt.Errorf("decode committed judge metrics: %w", err)
	}
	type candidateInput struct {
		name       string
		profile    string
		userPrompt []byte
		answer     []byte
		transcript []byte
		packet     []byte
	}
	var candidates []candidateInput
	for _, current := range metrics.Cases {
		if !current.Completed || current.Task != task || current.Variant != "optimized" {
			continue
		}
		packetPath := "changed-packet-" + current.Profile + ".json"
		if current.Profile == "default" {
			_, oldName := snapshot.inputs["changed-packet.json"]
			_, explicitName := snapshot.inputs[packetPath]
			if oldName == explicitName {
				return "", fmt.Errorf(
					"committed judge %s has ambiguous default packet",
					judgeName,
				)
			}
			if oldName {
				packetPath = "changed-packet.json"
			}
		}
		userPromptPath := manifest.CasePromptFiles[current.Name]
		userPrompt, userPromptOK := snapshot.inputs[userPromptPath]
		answer, answerOK := snapshot.inputs[current.AnswerFile]
		transcript, transcriptOK := snapshot.inputs[current.Name+".jsonl"]
		packet, packetOK := snapshot.inputs[packetPath]
		if userPromptPath == "" || !userPromptOK || !answerOK ||
			!transcriptOK || !packetOK {
			return "", fmt.Errorf("committed judge %s omits candidate inputs", judgeName)
		}
		candidates = append(candidates, candidateInput{
			name:       current.Name,
			profile:    current.Profile,
			userPrompt: userPrompt,
			answer:     answer,
			transcript: transcript,
			packet:     packet,
		})
	}
	sort.Slice(candidates, func(left, right int) bool {
		leftRecord := candidates[left].name + "\t" + candidates[left].profile
		rightRecord := candidates[right].name + "\t" + candidates[right].profile
		return leftRecord < rightRecord
	})
	if len(candidates) == 0 {
		return "", fmt.Errorf("committed judge %s has no candidates", judgeName)
	}
	candidateNames := make([]string, len(candidates))
	var candidateSemantics strings.Builder
	for index, candidate := range candidates {
		candidateNames[index] = candidate.name
		fmt.Fprintf(
			&candidateSemantics,
			"- %s: exact_user_prompt=<candidate-user-prompt-%d>; answer=<candidate-answer-%d>; transcript=<candidate-transcript-%d>; changed_packet=<candidate-packet-%d>\n",
			candidate.name,
			index,
			index,
			index,
			index,
		)
	}
	candidateNamesJSON, _ := json.Marshal(candidateNames)
	promptSemantics := renderCommittedJudgePromptSemantics(
		manifest.TargetCommit,
		task,
		"baseline-"+task,
		strings.Join(candidateNames, " "),
		candidateSemantics.String(),
	)
	modelConfiguration := "routing=router-selected;model-configuration=none"
	if quality.Evaluator.ModelMode == "pinned" {
		modelConfiguration = "model=" + quality.Evaluator.Model +
			";model-reasoning-effort=high"
	}
	rubric := snapshot.inputs[qualityEvaluatorBundlePath("quality-rubric.json")]
	schema := snapshot.inputs[qualityEvaluatorBundlePath("quality-output-schema.json")]
	taskPromptPath := manifest.PromptFiles[task]
	baselineUserPromptPath := manifest.CasePromptFiles["baseline-"+task]
	taskPrompt := snapshot.inputs[taskPromptPath]
	baselineUserPrompt := snapshot.inputs[baselineUserPromptPath]
	baselineAnswer := snapshot.inputs["answers/baseline-"+task+".md"]
	baselineTranscript := snapshot.inputs["baseline-"+task+".jsonl"]
	if rubric == nil || schema == nil || taskPromptPath == "" || taskPrompt == nil ||
		baselineUserPromptPath == "" || baselineUserPrompt == nil ||
		baselineAnswer == nil || baselineTranscript == nil {
		return "", fmt.Errorf("committed judge %s omits baseline evaluator inputs", judgeName)
	}
	var binding bytes.Buffer
	writeJudgeDigestField(&binding, "judge-cache-schema", "8")
	writeJudgeDigestField(&binding, "judge-identity", judgeName)
	writeJudgeDigestField(&binding, "target-commit", manifest.TargetCommit)
	writeJudgeDigestField(&binding, "task", task)
	writeJudgeDigestField(&binding, "baseline", "baseline-"+task)
	writeJudgeDigestField(&binding, "candidate-identities", string(candidateNamesJSON))
	writeJudgeDigestField(&binding, "developer-instructions", qualityNoCollaboration)
	writeJudgeDigestField(
		&binding,
		"evaluator-config",
		"codex-exec;private-codex-home=auth-only-and-tool-denied;"+
			modelConfiguration+";codex-version="+quality.Evaluator.CodexVersion+
			";permissions=quality-audit;ephemeral=true;json=true;output-schema=true",
	)
	writeJudgeDigestField(&binding, "prompt-semantics", promptSemantics)
	for _, featureFlag := range qualityFeatureFlags {
		writeJudgeDigestField(&binding, "feature-flag", featureFlag)
	}
	for _, semantic := range commitment.JudgeEnvironmentSemantics {
		writeJudgeDigestField(&binding, "isolation-semantics", semantic)
	}
	writeJudgeDigestInput(&binding, "json-input", "quality-rubric", canonicalJSONDigest(rubric, false))
	writeJudgeDigestInput(&binding, "json-input", "quality-output-schema", canonicalJSONDigest(schema, false))
	writeJudgeDigestInput(&binding, "file-input", "task-prompt", sha256Bytes(taskPrompt))
	writeJudgeDigestInput(&binding, "file-input", "baseline-user-prompt", sha256Bytes(baselineUserPrompt))
	writeJudgeDigestInput(&binding, "file-input", "baseline-answer", sha256Bytes(baselineAnswer))
	writeJudgeDigestInput(&binding, "file-input", "baseline-transcript", sha256Bytes(baselineTranscript))
	for index, candidate := range candidates {
		writeJudgeDigestInput(
			&binding,
			"file-input",
			fmt.Sprintf("candidate-user-prompt-%d", index),
			sha256Bytes(candidate.userPrompt),
		)
		writeJudgeDigestInput(
			&binding,
			"file-input",
			fmt.Sprintf("candidate-answer-%d", index),
			sha256Bytes(candidate.answer),
		)
		writeJudgeDigestInput(
			&binding,
			"file-input",
			fmt.Sprintf("candidate-transcript-%d", index),
			sha256Bytes(candidate.transcript),
		)
		writeJudgeDigestInput(
			&binding,
			"json-input",
			fmt.Sprintf("candidate-packet-%d", index),
			canonicalJSONDigest(candidate.packet, true),
		)
	}
	return sha256Bytes(binding.Bytes()), nil
}

type legacyJudgeAttestation struct {
	Provenance struct {
		Model          string `json:"model"`
		CodexVersion   string `json:"codex_version"`
		Isolation      string `json:"isolation"`
		OperatorAction string `json:"operator_action"`
	} `json:"provenance"`
	Status           string `json:"status"`
	ArtifactIdentity string `json:"artifact_identity"`
	InputSHA256      string `json:"input_sha256"`
	OutputSHA256     string `json:"output_sha256"`
	TranscriptSHA256 string `json:"transcript_sha256"`
	ExitCodeSHA256   string `json:"exit_code_sha256"`
	Transcript       struct {
		Contract                       string `json:"contract"`
		OutputMatchesFinalAgentMessage bool   `json:"output_matches_final_agent_message"`
		SchemaValidNumericOutput       bool   `json:"schema_valid_numeric_output"`
	} `json:"transcript_validation"`
	SchemaVersion int `json:"schema_version"`
	ExitCode      int `json:"exit_code"`
}

func validateLegacyJudgeAttestation(
	content []byte,
	artifactIdentity string,
	expectedInputDigest string,
	output []byte,
	transcript []byte,
	exitCode []byte,
) error {
	if _, err := requireJSONObjectKeys(
		content,
		"schema_version",
		"status",
		"artifact_identity",
		"provenance",
		"input_sha256",
		"output_sha256",
		"transcript_sha256",
		"exit_code",
		"exit_code_sha256",
		"transcript_validation",
	); err != nil {
		return fmt.Errorf("legacy judge %s has an invalid attestation schema: %w", artifactIdentity, err)
	}
	var attestation legacyJudgeAttestation
	if err := decodeStrictJSON(content, &attestation); err != nil {
		return fmt.Errorf("decode legacy judge %s attestation: %w", artifactIdentity, err)
	}
	if attestation.SchemaVersion != 2 ||
		attestation.Status != "legacy-unisolated-attested" ||
		attestation.ArtifactIdentity != artifactIdentity ||
		attestation.Provenance.Model != "unknown" ||
		attestation.Provenance.CodexVersion != "unknown" ||
		attestation.Provenance.Isolation != "legacy-unisolated" ||
		attestation.Provenance.OperatorAction != "--bind-legacy-judges" ||
		attestation.InputSHA256 != expectedInputDigest ||
		attestation.OutputSHA256 != sha256Bytes(output) ||
		attestation.TranscriptSHA256 != sha256Bytes(transcript) ||
		attestation.ExitCode != 0 ||
		attestation.ExitCodeSHA256 != sha256Bytes(exitCode) ||
		attestation.Transcript.Contract !=
			"ordered-lifecycle-and-command-pairing-v1" ||
		!attestation.Transcript.OutputMatchesFinalAgentMessage ||
		!attestation.Transcript.SchemaValidNumericOutput {
		return fmt.Errorf("legacy judge %s has an invalid attestation binding", artifactIdentity)
	}
	return nil
}

func expectedLegacyJudgeInputDigest(
	snapshot *qualityAggregateSnapshot,
	judgeName string,
) (string, error) {
	task, _, ok := judgeTaskAndRepeat(judgeName)
	if !ok {
		return "", fmt.Errorf("invalid legacy judge identity %q", judgeName)
	}
	var manifest struct {
		PromptFiles     map[string]string `json:"prompt_files"`
		CasePromptFiles map[string]string `json:"case_prompt_files"`
		TargetCommit    string            `json:"target_commit"`
	}
	if err := json.Unmarshal(snapshot.inputs["manifest.json"], &manifest); err != nil ||
		!validGitCommit(manifest.TargetCommit) {
		return "", fmt.Errorf("legacy judge %s has no valid target commit", judgeName)
	}
	type candidate struct {
		name       string
		profile    string
		userPrompt []byte
		answer     []byte
		transcript []byte
		packet     []byte
	}
	var metrics struct {
		Cases []struct {
			Name       string `json:"name"`
			Task       string `json:"task"`
			Variant    string `json:"variant"`
			Profile    string `json:"profile"`
			AnswerFile string `json:"answer_file"`
			Completed  bool   `json:"completed"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(snapshot.inputs["metrics.json"], &metrics); err != nil {
		return "", fmt.Errorf("decode legacy judge metrics: %w", err)
	}
	var candidates []candidate
	for _, current := range metrics.Cases {
		if !current.Completed || current.Task != task || current.Variant != "optimized" {
			continue
		}
		packetPath := "changed-packet-" + current.Profile + ".json"
		if current.Profile == "default" {
			if _, ok := snapshot.inputs["changed-packet.json"]; ok {
				packetPath = "changed-packet.json"
			}
		}
		userPromptPath := manifest.CasePromptFiles[current.Name]
		userPrompt, userPromptOK := snapshot.inputs[userPromptPath]
		answer, answerOK := snapshot.inputs[current.AnswerFile]
		transcript, transcriptOK := snapshot.inputs[current.Name+".jsonl"]
		packet, packetOK := snapshot.inputs[packetPath]
		if userPromptPath == "" || !userPromptOK || !answerOK ||
			!transcriptOK || !packetOK {
			return "", fmt.Errorf("legacy judge %s omits candidate inputs", judgeName)
		}
		candidates = append(candidates, candidate{
			name: current.Name, profile: current.Profile,
			userPrompt: userPrompt, answer: answer,
			transcript: transcript, packet: packet,
		})
	}
	sort.Slice(candidates, func(left, right int) bool {
		return candidates[left].name+"\t"+candidates[left].profile <
			candidates[right].name+"\t"+candidates[right].profile
	})
	if len(candidates) == 0 {
		return "", fmt.Errorf("legacy judge %s has no candidates", judgeName)
	}
	names := make([]string, len(candidates))
	var semanticList strings.Builder
	for index, current := range candidates {
		names[index] = current.name
		fmt.Fprintf(
			&semanticList,
			"- %s: exact_user_prompt=<candidate-user-prompt-%d>; answer=<candidate-answer-%d>; transcript=<candidate-transcript-%d>; changed_packet=<candidate-packet-%d>\n",
			current.name, index, index, index, index,
		)
	}
	namesJSON, _ := json.Marshal(names)
	prompt := renderCommittedJudgePromptSemantics(
		manifest.TargetCommit,
		task,
		"baseline-"+task,
		strings.Join(names, " "),
		semanticList.String(),
	)
	rubric := snapshot.inputs[qualityEvaluatorBundlePath("quality-rubric.json")]
	schema := snapshot.inputs[qualityEvaluatorBundlePath("quality-output-schema.json")]
	taskPromptPath := manifest.PromptFiles[task]
	baselineUserPromptPath := manifest.CasePromptFiles["baseline-"+task]
	taskPrompt := snapshot.inputs[taskPromptPath]
	baselineUserPrompt := snapshot.inputs[baselineUserPromptPath]
	baselineAnswer := snapshot.inputs["answers/baseline-"+task+".md"]
	baselineTranscript := snapshot.inputs["baseline-"+task+".jsonl"]
	if rubric == nil || schema == nil || taskPromptPath == "" || taskPrompt == nil ||
		baselineUserPromptPath == "" || baselineUserPrompt == nil ||
		baselineAnswer == nil || baselineTranscript == nil {
		return "", fmt.Errorf("legacy judge %s omits baseline evaluator inputs", judgeName)
	}
	var binding bytes.Buffer
	writeJudgeDigestField(&binding, "legacy-judge-attestation-schema", "2")
	writeJudgeDigestField(&binding, "status", "legacy-unisolated-attested")
	writeJudgeDigestField(&binding, "artifact-identity", judgeName)
	writeJudgeDigestField(&binding, "model", "unknown")
	writeJudgeDigestField(&binding, "codex-version", "unknown")
	writeJudgeDigestField(&binding, "isolation", "legacy-unisolated")
	writeJudgeDigestField(&binding, "target-commit", manifest.TargetCommit)
	writeJudgeDigestField(&binding, "task", task)
	writeJudgeDigestField(&binding, "baseline", "baseline-"+task)
	writeJudgeDigestField(&binding, "candidate-identities", string(namesJSON))
	writeJudgeDigestField(&binding, "prompt-semantics", prompt)
	writeJudgeDigestInput(&binding, "json-input", "quality-rubric", canonicalJSONDigest(rubric, false))
	writeJudgeDigestInput(&binding, "json-input", "quality-output-schema", canonicalJSONDigest(schema, false))
	writeJudgeDigestInput(&binding, "file-input", "task-prompt", sha256Bytes(taskPrompt))
	writeJudgeDigestInput(&binding, "file-input", "baseline-user-prompt", sha256Bytes(baselineUserPrompt))
	writeJudgeDigestInput(&binding, "file-input", "baseline-answer", sha256Bytes(baselineAnswer))
	writeJudgeDigestInput(&binding, "file-input", "baseline-transcript", sha256Bytes(baselineTranscript))
	for index, current := range candidates {
		writeJudgeDigestInput(&binding, "file-input", fmt.Sprintf("candidate-user-prompt-%d", index), sha256Bytes(current.userPrompt))
		writeJudgeDigestInput(&binding, "file-input", fmt.Sprintf("candidate-answer-%d", index), sha256Bytes(current.answer))
		writeJudgeDigestInput(&binding, "file-input", fmt.Sprintf("candidate-transcript-%d", index), sha256Bytes(current.transcript))
		writeJudgeDigestInput(&binding, "json-input", fmt.Sprintf("candidate-packet-%d", index), canonicalJSONDigest(current.packet, true))
	}
	return sha256Bytes(binding.Bytes()), nil
}

func writeJudgeDigestField(buffer *bytes.Buffer, name, value string) {
	fmt.Fprintf(buffer, "%s\x00%s\x00", name, value)
}

func writeJudgeDigestInput(
	buffer *bytes.Buffer,
	kind, name, digest string,
) {
	fmt.Fprintf(buffer, "%s\x00%s\x00%s\x00", kind, name, digest)
}

func canonicalJSONDigest(content []byte, normalizeRoots bool) string {
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	var value any
	if decoder.Decode(&value) != nil {
		return ""
	}
	if normalizeRoots {
		value = normalizeJSONRoots(value)
	}
	var canonical bytes.Buffer
	encoder := json.NewEncoder(&canonical)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return ""
	}
	return sha256Bytes(jqCompatibleUnicodeSeparators(canonical.Bytes()))
}

// encoding/json always escapes U+2028 and U+2029, even when HTML escaping is
// disabled. jq -cS writes both separators as literal UTF-8. Convert only the
// genuine JSON escapes that encoding/json emitted; an even run of backslashes
// represents literal text such as `\u2028` and must remain escaped.
func jqCompatibleUnicodeSeparators(content []byte) []byte {
	result := make([]byte, 0, len(content))
	for index := 0; index < len(content); {
		if content[index] != '\\' {
			result = append(result, content[index])
			index++
			continue
		}
		end := index
		for end < len(content) && content[end] == '\\' {
			end++
		}
		if (end-index)%2 == 1 && end+5 <= len(content) &&
			content[end] == 'u' &&
			(string(content[end+1:end+5]) == "2028" ||
				string(content[end+1:end+5]) == "2029") {
			result = append(result, content[index:end-1]...)
			result = append(result, 0xe2, 0x80)
			if content[end+4] == '8' {
				result = append(result, 0xa8)
			} else {
				result = append(result, 0xa9)
			}
			index = end + 5
			continue
		}
		result = append(result, content[index:end]...)
		index = end
	}
	return result
}

func normalizeJSONRoots(value any) any {
	switch current := value.(type) {
	case map[string]any:
		for key, nested := range current {
			current[key] = normalizeJSONRoots(nested)
		}
		if _, ok := current["root"]; ok {
			current["root"] = "<target-root>"
		}
		return current
	case []any:
		for index := range current {
			current[index] = normalizeJSONRoots(current[index])
		}
		return current
	default:
		return value
	}
}

func renderCommittedJudgePromptSemantics(
	targetCommit, task, baselineName, candidateNames, candidates string,
) string {
	return fmt.Sprintf(`Act as an independent code-review quality evaluator. The authoritative source checkout is <target-root> at commit %s. Read the shared task prompt at <task-prompt>, the task rubric at <quality-rubric>, the baseline's exact user prompt at <baseline-user-prompt>, the baseline answer at <baseline-answer>, its raw transcript at <baseline-transcript>, and each candidate's exact user prompt, answer, transcript, and changed packet:
%s
Independently inspect any source in the authoritative checkout needed to verify claims. Each changed packet is that candidate profile's navigation output, not the evaluator's sole ground truth. A claim supported by the source or its answer's raw transcript is grounded even when it is absent from its changed packet. Validate claims about executed commands, tests, or sandbox failures from that answer's transcript. Do not treat a different checkout prefix as broken when the linked file exists at the same commit.

Mandatory evaluator input protocol:
1. Read the shared task prompt, rubric, baseline exact user prompt, baseline answer, every candidate exact user prompt, changed packet, answer, and transcript in separate commands. Never concatenate multiple evaluator inputs into one command output. Issue exactly one shell command at a time and wait for its completed result before issuing the next; parallel command execution invalidates the audit.
2. Read each baseline and candidate answer through EOF before scoring. Use a line count and bounded chunks when needed, and verify the final chunk was seen.
3. If any command output is truncated, issue narrower reads for the missing content before drawing a conclusion.
4. Before reporting a critical omission, unsupported negative claim, material contradiction, or baseline point omitted, search the candidate answer directly for each supposedly missing concept and read the matching section. Do not infer omission from an earlier truncated read.

The shared task prompt and rubric define common answer scope. Each case's exact user prompt governs only that case's task adherence and claims about what was requested. Never apply an optimized profile's navigation constraints or answer instructions to the baseline or to another profile.

Score every answer absolutely against the authoritative source, shared task prompt, rubric, and its own exact user prompt for the requested %s task. Assign correctness, completeness, grounding, and task-adherence scores against those requirements, never against the baseline's length or exploratory breadth. The baseline is only a comparator, not ground truth. Do not reward verbosity. A shorter answer can receive the same completeness score when it fully covers the requested scope. Penalize factual errors, genuinely unsupported claims, missed required behavior or findings, and failure to answer the task.

For each candidate, also compare behavior to baseline: core_conclusion_matches_baseline is true when the main technical conclusion and finding set align; material_contradictions contains only candidate claims contradicted by authoritative source or by the candidate's own answer/transcript; baseline_material_points_omitted contains only correct baseline content that is required by the shared task prompt or rubric, or is necessary to make the candidate's own core conclusion correct and adequately grounded, and that the candidate loses without equally strong substitute coverage; candidate_material_additions lists material correct content the candidate adds. Do not count extra examples, optional breadth, exhaustive unaffected-method lists, or deeper call-chain tracing beyond an accurately stated evidence boundary as baseline material points omitted. Treat an explicit and accurate construction-only limitation as satisfying a construction-versus-consumption distinction unless the shared task prompt or rubric expressly requires a proven consuming chain. A correct candidate correction of a baseline error is a candidate_material_addition, never a material_contradiction. Set not_worse_than_baseline true only when the candidate has no material correctness, completeness, grounding, or task-adherence regression. Output task exactly %s, baseline name exactly %s, and exactly these candidate names: %s. Every score must be an integer from 1 through 5; never emit zero placeholder scores or omit a candidate. Return JSON matching the provided schema. Read only and do not modify files.`, targetCommit, candidates, task, task, baselineName, candidateNames)
}

func readCommittedJudgeLog(
	content []byte,
) (qualityJudgeUsageRun, []byte, error) {
	var usage qualityJudgeUsageRun
	var hasUsage bool
	var finalOutput []byte
	scanner := bufio.NewScanner(bytes.NewReader(content))
	buffer := make([]byte, 64*1024)
	scanner.Buffer(buffer, 16*1024*1024)
	for scanner.Scan() {
		var event struct {
			Usage *struct {
				InputTokens           int64 `json:"input_tokens"`
				CachedInputTokens     int64 `json:"cached_input_tokens"`
				OutputTokens          int64 `json:"output_tokens"`
				ReasoningOutputTokens int64 `json:"reasoning_output_tokens"`
			} `json:"usage"`
			Item struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"item"`
			Type string `json:"type"`
		}
		if err := json.Unmarshal(scanner.Bytes(), &event); err != nil {
			return qualityJudgeUsageRun{}, nil, fmt.Errorf("invalid JSONL event: %w", err)
		}
		if event.Type == "item.completed" && event.Item.Type == "agent_message" {
			var document any
			if json.Unmarshal([]byte(event.Item.Text), &document) == nil {
				finalOutput = []byte(event.Item.Text)
			}
		}
		if event.Type == "turn.completed" && event.Usage != nil {
			hasUsage = true
			usage.InputTokens = event.Usage.InputTokens
			usage.CachedInputTokens = event.Usage.CachedInputTokens
			usage.RegularInputTokens = usage.InputTokens - usage.CachedInputTokens
			usage.CachedInputEquivalentTokens = float64(usage.CachedInputTokens) * 0.1
			usage.OutputTokens = event.Usage.OutputTokens
			usage.ReasoningOutputTokens = event.Usage.ReasoningOutputTokens
			usage.RawTotalTokens = usage.InputTokens + usage.OutputTokens
			usage.EffectiveTokens = float64(usage.RegularInputTokens) +
				usage.CachedInputEquivalentTokens + float64(usage.OutputTokens)
		}
	}
	if err := scanner.Err(); err != nil {
		return qualityJudgeUsageRun{}, nil, err
	}
	if !hasUsage || len(finalOutput) == 0 {
		return qualityJudgeUsageRun{}, nil, fmt.Errorf(
			"missing final agent output or completed-turn usage",
		)
	}
	return usage, finalOutput, nil
}

func validateJudgeAggregates(judges qualityJudgesDocument) error {
	type group struct {
		candidates map[string][]qualityJudgeRunCandidate
		baseline   []qualityJudgeRunBaseline
	}
	groups := make(map[string]*group)
	for _, run := range judges.JudgeRuns {
		current := groups[run.Task]
		if current == nil {
			current = &group{candidates: make(map[string][]qualityJudgeRunCandidate)}
			groups[run.Task] = current
		}
		current.baseline = append(current.baseline, run.Baseline)
		for _, candidate := range run.Candidates {
			current.candidates[candidate.Name] = append(
				current.candidates[candidate.Name],
				candidate,
			)
		}
	}
	seenBaselines := make(map[string]bool)
	for _, baseline := range judges.Baselines {
		key := baseline.Task + "\x00" + baseline.Name
		current := groups[baseline.Task]
		if seenBaselines[key] || current == nil ||
			baseline.Name != "baseline-"+baseline.Task ||
			baseline.JudgeCount != len(current.baseline) ||
			baseline.JudgeCount == 0 ||
			!validBaselineAggregate(baseline, current.baseline) {
			return fmt.Errorf("judges.json has an invalid baseline aggregate")
		}
		seenBaselines[key] = true
	}
	if len(seenBaselines) != len(groups) {
		return fmt.Errorf("judges.json omits a baseline aggregate")
	}
	seenCandidates := make(map[string]bool)
	expectedCandidateCount := 0
	for _, current := range groups {
		expectedCandidateCount += len(current.candidates)
	}
	for _, candidate := range judges.Candidates {
		key := candidate.Task + "\x00" + candidate.Name
		current := groups[candidate.Task]
		if seenCandidates[key] || current == nil ||
			candidate.JudgeCount != len(current.candidates[candidate.Name]) ||
			candidate.JudgeCount == 0 ||
			!validCandidateAggregate(
				candidate,
				current.candidates[candidate.Name],
			) {
			return fmt.Errorf("judges.json has an invalid candidate aggregate")
		}
		seenCandidates[key] = true
	}
	if len(seenCandidates) != expectedCandidateCount {
		return fmt.Errorf("judges.json omits a candidate aggregate")
	}
	return nil
}

func validBaselineAggregate(
	aggregate qualityJudgeBaseline,
	runs []qualityJudgeRunBaseline,
) bool {
	var correctness, completeness, grounding, adherence int
	var omissions, unsupported []string
	for _, run := range runs {
		correctness += run.Correctness
		completeness += run.Completeness
		grounding += run.Grounding
		adherence += run.TaskAdherence
		omissions = append(omissions, run.CriticalOmissions...)
		unsupported = append(unsupported, run.UnsupportedClaims...)
	}
	count := float64(len(runs))
	return aggregate.AverageCorrectness == float64(correctness)/count &&
		aggregate.AverageCompleteness == float64(completeness)/count &&
		aggregate.AverageGrounding == float64(grounding)/count &&
		aggregate.AverageTaskAdherence == float64(adherence)/count &&
		reflect.DeepEqual(aggregate.CriticalOmissions, uniqueSortedStrings(omissions)) &&
		reflect.DeepEqual(aggregate.UnsupportedClaims, uniqueSortedStrings(unsupported))
}

func validCandidateAggregate(
	aggregate qualityJudgeCandidate,
	runs []qualityJudgeRunCandidate,
) bool {
	var correctness, completeness, grounding, adherence int
	allNotWorse := true
	allCore := true
	var omissions, unsupported, contradictions, baselineOmissions, additions []string
	for _, run := range runs {
		correctness += run.Correctness
		completeness += run.Completeness
		grounding += run.Grounding
		adherence += run.TaskAdherence
		allNotWorse = allNotWorse && run.NotWorseThanBaseline
		allCore = allCore && run.CoreConclusionMatchesBaseline
		omissions = append(omissions, run.CriticalOmissions...)
		unsupported = append(unsupported, run.UnsupportedClaims...)
		contradictions = append(contradictions, run.MaterialContradictions...)
		baselineOmissions = append(
			baselineOmissions,
			run.BaselineMaterialPointsOmitted...,
		)
		additions = append(additions, run.CandidateMaterialAdditions...)
	}
	count := float64(len(runs))
	return aggregate.AllNotWorse == allNotWorse &&
		aggregate.AllCoreConclusionMatch == allCore &&
		aggregate.AverageCorrectness == float64(correctness)/count &&
		aggregate.AverageCompleteness == float64(completeness)/count &&
		aggregate.AverageGrounding == float64(grounding)/count &&
		aggregate.AverageTaskAdherence == float64(adherence)/count &&
		reflect.DeepEqual(aggregate.CriticalOmissions, uniqueSortedStrings(omissions)) &&
		reflect.DeepEqual(aggregate.UnsupportedClaims, uniqueSortedStrings(unsupported)) &&
		reflect.DeepEqual(
			aggregate.MaterialContradictions,
			uniqueSortedStrings(contradictions),
		) &&
		reflect.DeepEqual(
			aggregate.BaselineMaterialPointsOmitted,
			uniqueSortedStrings(baselineOmissions),
		) &&
		reflect.DeepEqual(
			aggregate.CandidateMaterialAdditions,
			uniqueSortedStrings(additions),
		)
}

func uniqueSortedStrings(values []string) []string {
	seen := make(map[string]bool, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		if !seen[value] {
			seen[value] = true
			result = append(result, value)
		}
	}
	sort.Strings(result)
	if result == nil {
		return []string{}
	}
	return result
}

func readQualityAggregate(
	runDir string,
	expectedManifestDigest string,
) (*qualityAggregateSnapshot, error) {
	qualityDir := filepath.Join(runDir, "quality")
	var qualityDirInfo os.FileInfo
	if info, err := os.Lstat(qualityDir); err == nil {
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("quality aggregate directory is not a real directory")
		}
		qualityDirInfo = info
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect quality aggregate directory: %w", err)
	}
	manifestPath := filepath.Join(qualityDir, "aggregate-manifest.json")
	aggregatePresent := false
	for _, name := range qualityAggregateFiles {
		if _, err := os.Lstat(filepath.Join(qualityDir, name)); err == nil {
			aggregatePresent = true
			break
		} else if !os.IsNotExist(err) {
			return nil, fmt.Errorf("inspect quality aggregate %s: %w", name, err)
		}
	}
	if _, err := os.Lstat(manifestPath); err == nil {
		aggregatePresent = true
	} else if !os.IsNotExist(err) {
		return nil, fmt.Errorf("inspect quality aggregate manifest: %w", err)
	}
	if !aggregatePresent {
		return nil, fmt.Errorf("quality aggregate commit marker is missing")
	}

	manifestBytes, err := readRegularSnapshot(manifestPath)
	if err != nil {
		return nil, fmt.Errorf("read quality aggregate commit marker: %w", err)
	}
	manifestDigest := sha256Bytes(manifestBytes)
	if expectedManifestDigest != "" && manifestDigest != expectedManifestDigest {
		return nil, fmt.Errorf(
			"tracked quality aggregate digest mismatch: got %s, want %s",
			manifestDigest,
			expectedManifestDigest,
		)
	}
	var manifest struct {
		Files         map[string]string `json:"files"`
		SchemaVersion int               `json:"schema_version"`
	}
	if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
		return nil, fmt.Errorf("decode quality aggregate commit marker: %w", err)
	}
	if manifest.SchemaVersion != 2 {
		return nil, fmt.Errorf(
			"quality aggregate commit marker schema is %d, want 2",
			manifest.SchemaVersion,
		)
	}
	if len(manifest.Files) != len(qualityAggregateFiles) {
		return nil, fmt.Errorf("quality aggregate commit marker has an unexpected file set")
	}
	snapshot := &qualityAggregateSnapshot{
		manifestDigest: manifestDigest,
		outputs:        make(map[string][]byte, len(qualityAggregateFiles)),
	}
	for _, name := range qualityAggregateFiles {
		expected, ok := manifest.Files[name]
		if !ok {
			return nil, fmt.Errorf("quality aggregate commit marker omits %s", name)
		}
		if !validSHA256Digest(expected) {
			return nil, fmt.Errorf(
				"quality aggregate commit marker has invalid digest for %s",
				name,
			)
		}
		path := filepath.Join(qualityDir, name)
		content, err := readRegularSnapshot(path)
		if err != nil {
			return nil, fmt.Errorf("read quality aggregate %s: %w", name, err)
		}
		actual := sha256Bytes(content)
		if actual != expected {
			return nil, fmt.Errorf(
				"quality aggregate %s does not match committed generation",
				name,
			)
		}
		snapshot.outputs[name] = content
	}

	var commitment qualityInputCommitment
	if err := json.Unmarshal(snapshot.outputs["inputs.json"], &commitment); err != nil {
		return nil, fmt.Errorf("decode quality input commitment: %w", err)
	}
	if err := validateQualityInputCommitment(commitment); err != nil {
		return nil, err
	}
	snapshot.validation = commitment.Validation
	snapshot.inputs = make(map[string][]byte, len(commitment.Inputs))
	for relative, expected := range commitment.Inputs {
		if err := validQualityBundlePath(relative); err != nil {
			return nil, err
		}
		content, err := readRegularSnapshot(
			filepath.Join(runDir, filepath.FromSlash(relative)),
		)
		if err != nil {
			return nil, fmt.Errorf("read committed quality input %s: %w", relative, err)
		}
		if actual := sha256Bytes(content); actual != expected {
			return nil, fmt.Errorf(
				"quality input %s does not match committed snapshot",
				relative,
			)
		}
		snapshot.inputs[relative] = content
		snapshotName := qualitySnapshotName(relative)
		if recorded, ok := commitment.Snapshots[snapshotName]; !ok ||
			recorded != expected {
			return nil, fmt.Errorf(
				"quality input %s is not bound to its evaluator snapshot",
				relative,
			)
		}
	}
	if err := validateMaterialQualityInputs(snapshot, commitment); err != nil {
		return nil, err
	}
	if err := validateQualityOutputSemantics(snapshot, commitment); err != nil {
		return nil, err
	}
	currentQualityDirInfo, err := os.Lstat(qualityDir)
	if err != nil ||
		!currentQualityDirInfo.IsDir() ||
		currentQualityDirInfo.Mode()&os.ModeSymlink != 0 ||
		qualityDirInfo == nil ||
		!os.SameFile(qualityDirInfo, currentQualityDirInfo) {
		return nil, fmt.Errorf("quality aggregate directory identity changed while reading")
	}
	return snapshot, nil
}

func readRegularSnapshot(path string) ([]byte, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("not a regular file")
	}
	if before.Size() < 0 || before.Size() > maximumRegularSnapshotBytes {
		return nil, fmt.Errorf(
			"regular snapshot exceeds %d bytes",
			maximumRegularSnapshotBytes,
		)
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(before, opened) ||
		opened.Size() != before.Size() || opened.Mode() != before.Mode() ||
		!opened.ModTime().Equal(before.ModTime()) {
		_ = file.Close()
		return nil, fmt.Errorf("file identity changed while opening")
	}
	content, err := io.ReadAll(io.LimitReader(file, before.Size()+1))
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if int64(len(content)) != before.Size() {
		_ = file.Close()
		return nil, fmt.Errorf("file size changed while reading")
	}
	finished, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !os.SameFile(opened, finished) ||
		opened.Size() != finished.Size() ||
		!opened.ModTime().Equal(finished.ModTime()) ||
		opened.Mode() != finished.Mode() {
		_ = file.Close()
		return nil, fmt.Errorf("file changed while reading")
	}
	after, err := os.Lstat(path)
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !after.Mode().IsRegular() ||
		after.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(opened, after) ||
		after.Size() != opened.Size() || after.Mode() != opened.Mode() ||
		!after.ModTime().Equal(opened.ModTime()) {
		_ = file.Close()
		return nil, fmt.Errorf("file identity changed while reading")
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	return content, nil
}

const maximumRegularSnapshotBytes = int64(64 << 20)

func sha256Bytes(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func validateQualityInputCommitment(commitment qualityInputCommitment) error {
	if commitment.SchemaVersion != 1 {
		return fmt.Errorf(
			"quality input commitment schema is %d, want 1",
			commitment.SchemaVersion,
		)
	}
	if commitment.Validation.MetricsSchemaVersion != 2 ||
		commitment.Validation.MetricsFormula != qualityMetricsFormula ||
		commitment.Validation.JudgeCacheSchema != 8 ||
		commitment.Validation.JudgeRepeats < 0 {
		return fmt.Errorf("quality input commitment has incompatible validation semantics")
	}
	switch commitment.Validation.AggregateStatus {
	case "strict-current":
		if !commitment.Validation.StrictEvidence ||
			commitment.Validation.BindLegacyJudges ||
			(commitment.Validation.JudgeRepeats == 0 &&
				!commitment.Validation.Enforce) {
			return fmt.Errorf(
				"quality input commitment has inconsistent strict provenance",
			)
		}
	case "legacy-unisolated-attested":
		if commitment.Validation.StrictEvidence ||
			!commitment.Validation.BindLegacyJudges ||
			commitment.Validation.Enforce ||
			commitment.Validation.JudgeRepeats != 0 {
			return fmt.Errorf(
				"quality input commitment has inconsistent legacy provenance",
			)
		}
	case "non-strict":
		if commitment.Validation.StrictEvidence ||
			commitment.Validation.BindLegacyJudges ||
			commitment.Validation.Enforce {
			return fmt.Errorf(
				"quality input commitment has inconsistent non-strict provenance",
			)
		}
	default:
		return fmt.Errorf("quality input commitment has unknown aggregate provenance")
	}
	if commitment.Validation.StrictEvidence &&
		commitment.Validation.GenerationIsolation != qualityGenerationIsolation {
		return fmt.Errorf(
			"strict quality input commitment has incompatible generation isolation",
		)
	}
	if commitment.Validation.StrictEvidence &&
		!validSHA256Digest(commitment.Validation.GenerationConfigSHA256) {
		return fmt.Errorf(
			"strict quality input commitment omits generation config digest",
		)
	}
	if commitment.AnalysisEnvironment.GoVersion == "" ||
		commitment.AnalysisEnvironment.GOENV != "off" ||
		commitment.AnalysisEnvironment.GOWORK != "off" ||
		commitment.AnalysisEnvironment.GOFLAGS != "-mod=readonly" {
		return fmt.Errorf("quality input commitment omits the pinned analysis environment")
	}
	if len(commitment.JudgeEnvironmentSemantics) == 0 {
		return fmt.Errorf("quality input commitment omits judge environment semantics")
	}
	hasJudgeGoVersion := false
	hasCleanOuterJudgeEnvironment := false
	for _, semantic := range commitment.JudgeEnvironmentSemantics {
		if strings.HasPrefix(semantic, "go-version=go version go") {
			hasJudgeGoVersion = true
		}
		if strings.HasPrefix(
			semantic,
			"outer-environment=inherit-none;",
		) {
			hasCleanOuterJudgeEnvironment = true
		}
	}
	if !hasJudgeGoVersion {
		return fmt.Errorf("quality input commitment omits the judge Go version")
	}
	if commitment.Validation.StrictEvidence && !hasCleanOuterJudgeEnvironment {
		return fmt.Errorf(
			"strict quality input commitment omits the clean outer judge environment",
		)
	}
	if len(commitment.Inputs) == 0 || len(commitment.Snapshots) == 0 {
		return fmt.Errorf("quality input commitment is empty")
	}
	for name, digest := range commitment.Snapshots {
		if name == "" || !validSHA256Digest(digest) {
			return fmt.Errorf("quality snapshot %q has an invalid digest", name)
		}
	}
	if len(commitment.Generators) != len(qualityGeneratorFiles) {
		return fmt.Errorf("quality input commitment has an unexpected generator set")
	}
	for _, name := range qualityGeneratorFiles {
		digest := commitment.Generators[name]
		if !validSHA256Digest(digest) {
			return fmt.Errorf("quality generator %s has an invalid digest", name)
		}
		snapshotName := qualityGeneratorSnapshotName(name)
		if commitment.Snapshots[snapshotName] != digest {
			return fmt.Errorf(
				"quality generator %s is not bound to its immutable snapshot",
				name,
			)
		}
		bundlePath := qualityGeneratorBundlePath(name)
		if commitment.Inputs[bundlePath] != digest {
			return fmt.Errorf(
				"quality generator %s bytes are not retained in the run bundle",
				name,
			)
		}
	}
	for _, snapshotName := range []string{
		"quality-rubric.json",
		"quality-output-schema.json",
	} {
		bundlePath := qualityEvaluatorBundlePath(snapshotName)
		if commitment.Inputs[bundlePath] != commitment.Snapshots[snapshotName] ||
			!validSHA256Digest(commitment.Inputs[bundlePath]) {
			return fmt.Errorf(
				"evaluator input %s bytes are not retained in the run bundle",
				snapshotName,
			)
		}
	}
	return nil
}

func qualityGeneratorSnapshotName(name string) string {
	switch name {
	case "experiments/lsp-replacement/quality-check.sh":
		return "generators/quality-check.sh"
	case "experiments/lsp-replacement/analyze.sh":
		return "generators/analyze.sh"
	case "experiments/lsp-replacement/profiles.tsv":
		return "generators/profiles.tsv"
	case "cmd/repo-view-run-stats/main.go":
		return "generators/cmd-repo-view-run-stats-main.go"
	case "internal/runstats/runstats.go":
		return "generators/internal-runstats-runstats.go"
	case "go.mod":
		return "generators/go.mod"
	default:
		return ""
	}
}

func qualityGeneratorBundlePath(name string) string {
	switch name {
	case "experiments/lsp-replacement/quality-check.sh":
		return "quality/generator-quality-check.sh"
	case "experiments/lsp-replacement/analyze.sh":
		return "quality/generator-analyze.sh"
	case "experiments/lsp-replacement/profiles.tsv":
		return "quality/generator-profiles.tsv"
	case "cmd/repo-view-run-stats/main.go":
		return "quality/generator-cmd-repo-view-run-stats-main.go.source"
	case "internal/runstats/runstats.go":
		return "quality/generator-internal-runstats-runstats.go.source"
	case "go.mod":
		return "quality/generator-go.mod"
	default:
		return ""
	}
}

func qualityEvaluatorBundlePath(snapshotName string) string {
	switch snapshotName {
	case "quality-rubric.json":
		return "quality/evaluator-quality-rubric.json"
	case "quality-output-schema.json":
		return "quality/evaluator-quality-output-schema.json"
	default:
		for _, generator := range qualityGeneratorFiles {
			if snapshotName == qualityGeneratorSnapshotName(generator) {
				return qualityGeneratorBundlePath(generator)
			}
		}
		return ""
	}
}

func validQualityBundlePath(relative string) error {
	if relative == "" ||
		strings.Contains(relative, `\`) ||
		path.IsAbs(relative) ||
		path.Clean(relative) != relative ||
		relative == "." ||
		strings.HasPrefix(relative, "../") {
		return fmt.Errorf("quality input has unsafe bundle path %q", relative)
	}
	if relative == "quality/aggregate-manifest.json" {
		return fmt.Errorf("quality input cannot commit its own marker")
	}
	for _, output := range qualityAggregateFiles {
		if relative == "quality/"+output {
			return fmt.Errorf("quality input cannot commit aggregate output %s", relative)
		}
	}
	return nil
}

func qualitySnapshotName(relative string) string {
	for _, snapshotName := range []string{
		"quality-rubric.json",
		"quality-output-schema.json",
	} {
		if relative == qualityEvaluatorBundlePath(snapshotName) {
			return snapshotName
		}
	}
	for _, generator := range qualityGeneratorFiles {
		if relative == qualityGeneratorBundlePath(generator) {
			return qualityGeneratorSnapshotName(generator)
		}
	}
	switch {
	case strings.HasPrefix(relative, "quality/"):
		return "judges/" + strings.TrimPrefix(relative, "quality/")
	case strings.HasPrefix(relative, "changed-packet"):
		return "packets/" + relative
	default:
		return relative
	}
}

func validateMaterialQualityInputs(
	snapshot *qualityAggregateSnapshot,
	commitment qualityInputCommitment,
) error {
	metricsBytes, ok := snapshot.inputs["metrics.json"]
	if !ok {
		return fmt.Errorf("quality input commitment omits metrics.json")
	}
	var metrics struct {
		AnalysisProvenance struct {
			ProfilesSource string `json:"profiles_source"`
			ProfilesPath   string `json:"profiles_path"`
			ProfilesSHA256 string `json:"profiles_sha256"`
		} `json:"analysis_provenance"`
		Formula string `json:"formula"`
		Cases   []struct {
			Name                  string `json:"name"`
			AnswerFile            string `json:"answer_file"`
			CommandsFile          string `json:"commands_file"`
			ToolStatsFile         string `json:"tool_stats_file"`
			CallGraphDOTFile      string `json:"call_graph_dot_file"`
			CallGraphMarkdownFile string `json:"call_graph_markdown_file"`
		} `json:"cases"`
		SchemaVersion int `json:"schema_version"`
	}
	if err := json.Unmarshal(metricsBytes, &metrics); err != nil {
		return fmt.Errorf("decode snapshotted metrics: %w", err)
	}
	if metrics.SchemaVersion != 2 || metrics.Formula != qualityMetricsFormula ||
		len(metrics.Cases) == 0 {
		return fmt.Errorf("snapshotted metrics has incompatible schema or formula")
	}
	if metrics.AnalysisProvenance.ProfilesSource == "" ||
		metrics.AnalysisProvenance.ProfilesPath == "" ||
		!validSHA256Digest(metrics.AnalysisProvenance.ProfilesSHA256) {
		return fmt.Errorf("snapshotted metrics omits analysis provenance")
	}
	required := map[string]bool{"metrics.json": true}
	for _, current := range metrics.Cases {
		if current.Name == "" {
			return fmt.Errorf("snapshotted metrics has an unnamed case")
		}
		for _, relative := range []string{
			current.Name + ".jsonl",
			current.Name + ".exit-code",
			current.AnswerFile,
			current.CommandsFile,
			current.ToolStatsFile,
			current.CallGraphDOTFile,
			current.CallGraphMarkdownFile,
		} {
			if err := validQualityBundlePath(relative); err != nil {
				return err
			}
			required[relative] = true
		}
	}
	for relative := range required {
		if _, ok := snapshot.inputs[relative]; !ok {
			return fmt.Errorf("quality input commitment omits material input %s", relative)
		}
	}
	for _, generator := range qualityGeneratorFiles {
		relative := qualityGeneratorBundlePath(generator)
		if _, ok := snapshot.inputs[relative]; !ok {
			return fmt.Errorf(
				"quality input commitment omits evaluator source bytes %s",
				relative,
			)
		}
	}
	for _, evaluator := range []string{
		"quality-rubric.json",
		"quality-output-schema.json",
	} {
		relative := qualityEvaluatorBundlePath(evaluator)
		if _, ok := snapshot.inputs[relative]; !ok {
			return fmt.Errorf(
				"quality input commitment omits evaluator input bytes %s",
				relative,
			)
		}
	}
	manifestBytes, hasManifest := snapshot.inputs["manifest.json"]
	if commitment.Validation.StrictEvidence && !hasManifest {
		return fmt.Errorf("strict quality input commitment omits manifest.json")
	}
	generationConfigBytes, hasGenerationConfig :=
		snapshot.inputs["generation-config.json"]
	var profilesSnapshot []byte
	var generationConfig qualityGenerationConfig
	if commitment.Validation.StrictEvidence {
		if !hasGenerationConfig {
			return fmt.Errorf(
				"strict quality input commitment omits generation-config.json",
			)
		}
		if sha256Bytes(generationConfigBytes) !=
			commitment.Validation.GenerationConfigSHA256 {
			return fmt.Errorf(
				"generation-config.json disagrees with the committed digest",
			)
		}
		var generationConfigFields map[string]json.RawMessage
		if err := json.Unmarshal(
			generationConfigBytes,
			&generationConfig,
		); err != nil {
			return fmt.Errorf("decode generation-config.json: %w", err)
		}
		if err := json.Unmarshal(
			generationConfigBytes,
			&generationConfigFields,
		); err != nil {
			return fmt.Errorf("decode generation-config.json fields: %w", err)
		}
		if len(generationConfigFields) != 15 ||
			!validStrictGenerationConfig(generationConfig, true) {
			return fmt.Errorf(
				"generation-config.json has incompatible strict generation semantics",
			)
		}
		var profilesFound bool
		profilesSnapshot, profilesFound =
			snapshot.inputs["profiles-snapshot.tsv"]
		if !profilesFound ||
			sha256Bytes(profilesSnapshot) !=
				generationConfig.ProfilesSnapshotSHA256 {
			return fmt.Errorf(
				"strict quality input omits the bound profiles snapshot",
			)
		}
		if metrics.AnalysisProvenance.ProfilesSource != "run-snapshot" ||
			metrics.AnalysisProvenance.ProfilesPath !=
				generationConfig.ProfilesSnapshotPath ||
			metrics.AnalysisProvenance.ProfilesSHA256 !=
				generationConfig.ProfilesSnapshotSHA256 {
			return fmt.Errorf(
				"snapshotted metrics disagrees with the bound profiles snapshot",
			)
		}
		for task, relative := range generationConfig.PromptFiles {
			if relative != "prompts/"+task+".txt" ||
				!validSuiteSlug(task) ||
				!validSHA256Digest(generationConfig.PromptDigests[task]) {
				return fmt.Errorf(
					"generation-config.json has an invalid rendered prompt binding",
				)
			}
			content, ok := snapshot.inputs[relative]
			if !ok ||
				sha256Bytes(content) != generationConfig.PromptDigests[task] {
				return fmt.Errorf(
					"strict quality input omits rendered prompt %s",
					task,
				)
			}
		}
		for name, relative := range generationConfig.CasePromptFiles {
			if relative != name+".user-prompt.txt" ||
				!validSuiteSlug(name) ||
				!validSHA256Digest(generationConfig.CasePromptDigests[name]) {
				return fmt.Errorf(
					"generation-config.json has an invalid case prompt binding",
				)
			}
			content, ok := snapshot.inputs[relative]
			if !ok ||
				sha256Bytes(content) != generationConfig.CasePromptDigests[name] {
				return fmt.Errorf(
					"strict quality input omits case prompt %s",
					name,
				)
			}
		}
		runCompleteBytes, ok := snapshot.inputs["run-complete.json"]
		if !ok {
			return fmt.Errorf(
				"strict quality input commitment omits run-complete.json",
			)
		}
		var completion struct {
			State         string `json:"state"`
			Outcome       string `json:"outcome"`
			CompletedAt   string `json:"completed_at"`
			SchemaVersion int    `json:"schema_version"`
			ExitCode      int    `json:"exit_code"`
		}
		if err := json.Unmarshal(runCompleteBytes, &completion); err != nil {
			return fmt.Errorf("decode run-complete.json: %w", err)
		}
		if completion.SchemaVersion != 1 ||
			completion.State != "complete" ||
			completion.Outcome != "success" ||
			completion.ExitCode != 0 ||
			completion.CompletedAt == "" {
			return fmt.Errorf(
				"strict quality input has an unsuccessful run-complete.json",
			)
		}
	}
	if hasManifest {
		var manifest struct {
			BaselineFrom           *string           `json:"baseline_from"`
			PromptDigests          map[string]string `json:"prompt_digests"`
			PromptFiles            map[string]string `json:"prompt_files"`
			CasePromptDigests      map[string]string `json:"case_prompt_digests"`
			CasePromptFiles        map[string]string `json:"case_prompt_files"`
			GoVersion              string            `json:"go_version"`
			TargetCommit           string            `json:"target_commit"`
			GenerationIsolation    string            `json:"generation_isolation"`
			GenerationConfigSHA256 string            `json:"generation_config_sha256"`
			ProfilesSnapshotPath   string            `json:"profiles_snapshot_path"`
			ProfilesSnapshotSHA256 string            `json:"profiles_snapshot_sha256"`
			CodexVersion           string            `json:"codex_version"`
			Model                  string            `json:"model"`
			TaskSelection          string            `json:"task_selection"`
			VariantSelection       string            `json:"variant_selection"`
			PromptCommit           string            `json:"prompt_commit"`
			BaseCommit             string            `json:"base_commit"`
			BaseRef                string            `json:"base_ref"`
			ModelConfiguration     string            `json:"model_configuration"`
			ModelMode              string            `json:"model_mode"`
			Profiles               []string          `json:"profiles"`
			SchemaVersion          int               `json:"schema_version"`
			MechanicalNavigation   bool              `json:"mechanical_navigation_semantics_enforced"`
		}
		if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
			return fmt.Errorf("decode snapshotted manifest: %w", err)
		}
		if manifest.SchemaVersion != 1 {
			return fmt.Errorf("snapshotted manifest has incompatible schema")
		}
		if commitment.Validation.StrictEvidence {
			var qualitySchema struct {
				SchemaVersion int `json:"schema_version"`
			}
			if err := json.Unmarshal(
				snapshot.outputs["quality.json"],
				&qualitySchema,
			); err != nil {
				return fmt.Errorf("decode quality schema for manifest identity: %w", err)
			}
			generationModelValid := validGenerationModelIdentity(
				manifest.Model,
				manifest.ModelMode,
				manifest.ModelConfiguration,
			)
			if qualitySchema.SchemaVersion == 4 &&
				manifest.Model == "router-selected" &&
				manifest.ModelMode == "" &&
				manifest.ModelConfiguration == "" {
				generationModelValid = true
			}
			if !validGitCommit(manifest.TargetCommit) ||
				!validPromptCommit(manifest.PromptCommit) ||
				!strings.HasPrefix(
					manifest.TargetCommit,
					manifest.PromptCommit,
				) ||
				!validGitCommit(manifest.BaseCommit) ||
				manifest.BaseRef == "" ||
				!generationModelValid ||
				manifest.CodexVersion == "" ||
				manifest.GoVersion == "" {
				return fmt.Errorf(
					"snapshotted manifest omits strict generation provenance",
				)
			}
			if !manifest.MechanicalNavigation {
				return fmt.Errorf(
					"snapshotted manifest omits mechanical navigation enforcement",
				)
			}
			if manifest.ProfilesSnapshotPath !=
				generationConfig.ProfilesSnapshotPath ||
				manifest.ProfilesSnapshotSHA256 !=
					generationConfig.ProfilesSnapshotSHA256 ||
				!reflect.DeepEqual(
					manifest.PromptFiles,
					generationConfig.PromptFiles,
				) ||
				!reflect.DeepEqual(
					manifest.PromptDigests,
					generationConfig.PromptDigests,
				) ||
				!reflect.DeepEqual(
					manifest.CasePromptFiles,
					generationConfig.CasePromptFiles,
				) ||
				!reflect.DeepEqual(
					manifest.CasePromptDigests,
					generationConfig.CasePromptDigests,
				) {
				return fmt.Errorf(
					"snapshotted manifest disagrees with bound profile or prompt inputs",
				)
			}
			expectedPromptTasks, ok :=
				qualityManifestTasks(manifest.TaskSelection)
			if !ok ||
				(manifest.VariantSelection != "baseline" &&
					manifest.VariantSelection != "optimized" &&
					manifest.VariantSelection != "all") ||
				len(manifest.Profiles) == 0 {
				return fmt.Errorf(
					"snapshotted manifest has an invalid generation selection",
				)
			}
			profiles := make(map[string]bool, len(manifest.Profiles))
			for _, profile := range manifest.Profiles {
				if !validSuiteSlug(profile) || profiles[profile] {
					return fmt.Errorf(
						"snapshotted manifest has an invalid profile selection",
					)
				}
				profiles[profile] = true
			}
			if len(manifest.PromptDigests) != len(expectedPromptTasks) {
				return fmt.Errorf(
					"snapshotted manifest has the wrong prompt digest set",
				)
			}
			for _, task := range expectedPromptTasks {
				if !validSHA256Digest(manifest.PromptDigests[task]) {
					return fmt.Errorf(
						"snapshotted manifest has the wrong prompt digest set",
					)
				}
			}
			expectedCasePrompts := make(map[string]bool)
			for _, task := range expectedPromptTasks {
				expectedCasePrompts["baseline-"+task] = true
				if manifest.VariantSelection != "baseline" {
					for _, profile := range manifest.Profiles {
						name := "optimized-" + profile + "-" + task
						if profile == "default" {
							name = "optimized-" + task
						}
						expectedCasePrompts[name] = true
					}
				}
			}
			if len(manifest.CasePromptFiles) != len(expectedCasePrompts) ||
				len(manifest.CasePromptDigests) != len(expectedCasePrompts) {
				return fmt.Errorf(
					"snapshotted manifest has the wrong case prompt set",
				)
			}
			for name := range expectedCasePrompts {
				if manifest.CasePromptFiles[name] != name+".user-prompt.txt" ||
					!validSHA256Digest(manifest.CasePromptDigests[name]) {
					return fmt.Errorf(
						"snapshotted manifest has the wrong case prompt set",
					)
				}
			}
		}
		if commitment.Validation.StrictEvidence &&
			manifest.GenerationIsolation != qualityGenerationIsolation {
			return fmt.Errorf("snapshotted manifest has incompatible generation isolation")
		}
		if commitment.Validation.StrictEvidence &&
			manifest.GenerationConfigSHA256 !=
				commitment.Validation.GenerationConfigSHA256 {
			return fmt.Errorf(
				"snapshotted manifest disagrees with generation config digest",
			)
		}
		if commitment.Validation.StrictEvidence && len(manifest.PromptDigests) == 0 {
			return fmt.Errorf("snapshotted manifest omits prompt digests")
		}
		for task, digest := range manifest.PromptDigests {
			if task == "" || !validSHA256Digest(digest) {
				return fmt.Errorf("snapshotted manifest has an invalid prompt digest")
			}
		}
		if manifest.VariantSelection == "optimized" {
			if manifest.BaselineFrom == nil || *manifest.BaselineFrom == "" {
				return fmt.Errorf("optimized-only quality input omits baseline provenance")
			}
		}
		if commitment.Validation.StrictEvidence &&
			manifest.BaselineFrom != nil && *manifest.BaselineFrom != "" {
			baselineManifestBytes, ok :=
				snapshot.inputs["baseline-source-manifest.json"]
			if !ok {
				return fmt.Errorf(
					"imported quality input omits baseline-source-manifest.json",
				)
			}
			var baselineManifest struct {
				PromptFiles            map[string]string `json:"prompt_files"`
				PromptDigests          map[string]string `json:"prompt_digests"`
				CasePromptFiles        map[string]string `json:"case_prompt_files"`
				CasePromptDigests      map[string]string `json:"case_prompt_digests"`
				ProfilesSnapshotPath   string            `json:"profiles_snapshot_path"`
				ProfilesSnapshotSHA256 string            `json:"profiles_snapshot_sha256"`
				GenerationConfigSHA256 string            `json:"generation_config_sha256"`
				GenerationIsolation    string            `json:"generation_isolation"`
				TargetCommit           string            `json:"target_commit"`
				PromptCommit           string            `json:"prompt_commit"`
				BaseCommit             string            `json:"base_commit"`
				BaseRef                string            `json:"base_ref"`
				Model                  string            `json:"model"`
				ModelMode              string            `json:"model_mode"`
				ModelConfiguration     string            `json:"model_configuration"`
				CodexVersion           string            `json:"codex_version"`
				GoVersion              string            `json:"go_version"`
				SchemaVersion          int               `json:"schema_version"`
				MechanicalNavigation   bool              `json:"mechanical_navigation_semantics_enforced"`
			}
			if err := json.Unmarshal(
				baselineManifestBytes,
				&baselineManifest,
			); err != nil {
				return fmt.Errorf("decode baseline source manifest: %w", err)
			}
			var baselineManifestFields map[string]json.RawMessage
			if err := json.Unmarshal(
				baselineManifestBytes,
				&baselineManifestFields,
			); err != nil {
				return fmt.Errorf("decode baseline source manifest fields: %w", err)
			}
			mechanicalMarker, ok := baselineManifestFields["mechanical_navigation_semantics_enforced"]
			var baselineMechanicalNavigation *bool
			if !ok || json.Unmarshal(
				mechanicalMarker,
				&baselineMechanicalNavigation,
			) != nil || baselineMechanicalNavigation == nil {
				return fmt.Errorf(
					"baseline source manifest has an invalid mechanical navigation marker",
				)
			}
			if baselineManifest.SchemaVersion != 1 ||
				baselineManifest.GenerationIsolation !=
					manifest.GenerationIsolation ||
				!validSHA256Digest(
					baselineManifest.GenerationConfigSHA256,
				) ||
				baselineManifest.ProfilesSnapshotPath !=
					manifest.ProfilesSnapshotPath ||
				baselineManifest.ProfilesSnapshotSHA256 !=
					manifest.ProfilesSnapshotSHA256 ||
				!reflect.DeepEqual(
					baselineManifest.PromptFiles,
					manifest.PromptFiles,
				) ||
				!reflect.DeepEqual(
					baselineManifest.PromptDigests,
					manifest.PromptDigests,
				) ||
				baselineManifest.TargetCommit != manifest.TargetCommit ||
				baselineManifest.PromptCommit != manifest.PromptCommit ||
				baselineManifest.BaseCommit != manifest.BaseCommit ||
				baselineManifest.BaseRef != manifest.BaseRef ||
				baselineManifest.Model != manifest.Model ||
				baselineManifest.ModelMode != manifest.ModelMode ||
				baselineManifest.ModelConfiguration !=
					manifest.ModelConfiguration ||
				baselineManifest.CodexVersion != manifest.CodexVersion ||
				baselineManifest.GoVersion != manifest.GoVersion {
				return fmt.Errorf(
					"baseline source manifest disagrees with quality manifest",
				)
			}
			expectedBaselinePromptFiles := make(map[string]string)
			expectedBaselinePromptDigests := make(map[string]string)
			for name, relative := range manifest.CasePromptFiles {
				if !strings.HasPrefix(name, "baseline-") {
					continue
				}
				expectedBaselinePromptFiles[name] = relative
				expectedBaselinePromptDigests[name] =
					manifest.CasePromptDigests[name]
			}
			if !reflect.DeepEqual(
				baselineManifest.CasePromptFiles,
				expectedBaselinePromptFiles,
			) || !reflect.DeepEqual(
				baselineManifest.CasePromptDigests,
				expectedBaselinePromptDigests,
			) {
				return fmt.Errorf(
					"baseline source case prompt set disagrees with quality run",
				)
			}
			baselineConfigBytes, ok :=
				snapshot.inputs["baseline-source-generation-config.json"]
			if !ok {
				return fmt.Errorf(
					"imported quality input omits baseline source generation config",
				)
			}
			if sha256Bytes(baselineConfigBytes) !=
				baselineManifest.GenerationConfigSHA256 {
				return fmt.Errorf(
					"baseline source generation config disagrees with its manifest digest",
				)
			}
			var baselineConfig qualityGenerationConfig
			var baselineConfigFields map[string]json.RawMessage
			if err := json.Unmarshal(baselineConfigBytes, &baselineConfig); err != nil {
				return fmt.Errorf("decode baseline source generation config: %w", err)
			}
			if err := json.Unmarshal(
				baselineConfigBytes,
				&baselineConfigFields,
			); err != nil {
				return fmt.Errorf(
					"decode baseline source generation config fields: %w",
					err,
				)
			}
			if len(baselineConfigFields) != 15 ||
				!validStrictGenerationConfig(
					baselineConfig,
					baselineManifest.MechanicalNavigation,
				) {
				return fmt.Errorf(
					"baseline source generation config has incompatible strict generation semantics",
				)
			}
			if baselineConfig.GenerationIsolation !=
				baselineManifest.GenerationIsolation ||
				baselineConfig.ProfilesSnapshotPath !=
					baselineManifest.ProfilesSnapshotPath ||
				baselineConfig.ProfilesSnapshotSHA256 !=
					baselineManifest.ProfilesSnapshotSHA256 ||
				!reflect.DeepEqual(
					baselineConfig.PromptFiles,
					baselineManifest.PromptFiles,
				) ||
				!reflect.DeepEqual(
					baselineConfig.PromptDigests,
					baselineManifest.PromptDigests,
				) ||
				!reflect.DeepEqual(
					baselineConfig.CasePromptFiles,
					baselineManifest.CasePromptFiles,
				) ||
				!reflect.DeepEqual(
					baselineConfig.CasePromptDigests,
					baselineManifest.CasePromptDigests,
				) ||
				baselineConfig.MechanicalNavigationEnforced !=
					baselineManifest.MechanicalNavigation {
				return fmt.Errorf(
					"baseline source generation config disagrees with quality run",
				)
			}
			if !reflect.DeepEqual(
				sharedQualityGenerationConfig(baselineConfig),
				sharedQualityGenerationConfig(generationConfig),
			) {
				return fmt.Errorf(
					"baseline source generation config differs beyond mechanical navigation enforcement",
				)
			}
			baselineProfilesBytes, ok :=
				snapshot.inputs["baseline-source-profiles-snapshot.tsv"]
			if !ok ||
				!bytes.Equal(baselineProfilesBytes, profilesSnapshot) {
				return fmt.Errorf(
					"baseline source profiles snapshot disagrees with quality run",
				)
			}
			for task, relative := range manifest.PromptFiles {
				baselineRelative :=
					"baseline-source-prompts/" + task + ".txt"
				baselinePromptBytes, ok := snapshot.inputs[baselineRelative]
				if !ok ||
					!bytes.Equal(
						baselinePromptBytes,
						snapshot.inputs[relative],
					) {
					return fmt.Errorf(
						"baseline source rendered prompt disagrees for %s",
						task,
					)
				}
			}
			for name, relative := range manifest.CasePromptFiles {
				if !strings.HasPrefix(name, "baseline-") {
					continue
				}
				baselineRelative := "baseline-source-" + relative
				baselinePromptBytes, ok := snapshot.inputs[baselineRelative]
				if !ok ||
					!bytes.Equal(
						baselinePromptBytes,
						snapshot.inputs[relative],
					) {
					return fmt.Errorf(
						"baseline source case prompt disagrees for %s",
						name,
					)
				}
			}
		}
		if manifest.VariantSelection == "optimized" ||
			manifest.VariantSelection == "all" {
			for _, profile := range manifest.Profiles {
				if profile == "default" {
					_, oldName := snapshot.inputs["changed-packet.json"]
					_, explicitName := snapshot.inputs["changed-packet-default.json"]
					if oldName == explicitName {
						return fmt.Errorf(
							"quality input must bind exactly one default changed packet",
						)
					}
					continue
				}
				packet := "changed-packet-" + profile + ".json"
				if _, ok := snapshot.inputs[packet]; !ok {
					return fmt.Errorf("quality input commitment omits %s", packet)
				}
			}
		}
	}
	for relative, digest := range commitment.Inputs {
		if !validSHA256Digest(digest) {
			return fmt.Errorf("quality input %s has an invalid digest", relative)
		}
	}
	for _, requiredSnapshot := range []string{
		"quality-rubric.json",
		"quality-output-schema.json",
		"generators/quality-check.sh",
		"generators/analyze.sh",
		"generators/profiles.tsv",
		"generators/cmd-repo-view-run-stats-main.go",
		"generators/internal-runstats-runstats.go",
		"generators/go.mod",
	} {
		if !validSHA256Digest(commitment.Snapshots[requiredSnapshot]) {
			return fmt.Errorf(
				"quality input commitment omits evaluator snapshot %s",
				requiredSnapshot,
			)
		}
	}
	return nil
}

func validGitCommit(value string) bool {
	return (len(value) == 40 || len(value) == 64) &&
		validLowerHex(value) &&
		value != strings.Repeat("0", len(value))
}

func validPromptCommit(value string) bool {
	return len(value) >= 7 &&
		len(value) <= 64 &&
		validLowerHex(value) &&
		value != strings.Repeat("0", len(value))
}

func validLowerHex(value string) bool {
	for _, character := range value {
		if (character >= '0' && character <= '9') ||
			(character >= 'a' && character <= 'f') {
			continue
		}
		return false
	}
	return value != ""
}

func qualityManifestTasks(selection string) ([]string, bool) {
	switch selection {
	case "explain", "review", "deep-explain", "deep-review":
		return []string{selection}, true
	case "all":
		return []string{"explain", "review"}, true
	case "deep":
		return []string{"deep-explain", "deep-review"}, true
	default:
		return nil, false
	}
}

func SummarizeEvidence(runDir string, names []string) ([]EvidenceMetric, error) {
	return summarizeEvidenceWithProvenance(runDir, names, "", "")
}

func SummarizeTrackedEvidence(
	runDir string,
	names []string,
	expectedQualityAggregateSHA256 string,
) ([]EvidenceMetric, error) {
	return summarizeEvidence(runDir, names, expectedQualityAggregateSHA256, true)
}

func SummarizeTrackedEvidenceProvenance(
	runDir string,
	names []string,
	expectedQualityAggregateSHA256 string,
	expectedQualityProvenance string,
) ([]EvidenceMetric, error) {
	if expectedQualityProvenance == "" {
		expectedQualityProvenance = "strict-current"
	}
	return summarizeEvidenceWithProvenance(
		runDir,
		names,
		expectedQualityAggregateSHA256,
		expectedQualityProvenance,
	)
}

// SummarizeRejectedTrackedEvidence permits historical non-strict aggregate
// metrics only while evaluating a fixture that remains rejected.
func SummarizeRejectedTrackedEvidence(
	runDir string,
	names []string,
	expectedQualityAggregateSHA256 string,
) ([]EvidenceMetric, error) {
	return summarizeEvidence(runDir, names, expectedQualityAggregateSHA256, false)
}

func summarizeEvidence(
	runDir string,
	names []string,
	expectedQualityAggregateSHA256 string,
	requireStrict bool,
) ([]EvidenceMetric, error) {
	expectedProvenance := "non-strict"
	if requireStrict {
		expectedProvenance = "strict-current"
	}
	return summarizeEvidenceWithProvenance(
		runDir,
		names,
		expectedQualityAggregateSHA256,
		expectedProvenance,
	)
}

func summarizeEvidenceWithProvenance(
	runDir string,
	names []string,
	expectedQualityAggregateSHA256 string,
	expectedQualityProvenance string,
) ([]EvidenceMetric, error) {
	aggregate, err := readQualityAggregate(
		runDir,
		expectedQualityAggregateSHA256,
	)
	if err != nil {
		return nil, err
	}
	if expectedQualityProvenance != "" &&
		aggregate.validation.AggregateStatus != expectedQualityProvenance {
		return nil, fmt.Errorf(
			"quality aggregate provenance is %q, want %q",
			aggregate.validation.AggregateStatus,
			expectedQualityProvenance,
		)
	}
	var metrics struct {
		Cases []struct {
			Name                             string     `json:"name"`
			Task                             string     `json:"task"`
			Variant                          string     `json:"variant"`
			Profile                          string     `json:"profile"`
			CallGraphMarkdownFile            string     `json:"call_graph_markdown_file"`
			CallGraphDOTFile                 string     `json:"call_graph_dot_file"`
			ToolStatsFile                    string     `json:"tool_stats_file"`
			Operations                       []ToolStat `json:"operations"`
			ToolTypes                        []ToolStat `json:"tool_types"`
			OutputReferenceEdgeCount         int        `json:"output_reference_edge_count"`
			RepoViewOutlineInvocationCount   int        `json:"repo_view_outline_invocation_count"`
			OtherToolCallCount               int        `json:"other_tool_call_count"`
			RepoViewInvocationCount          int        `json:"repo_view_invocation_count"`
			TemporalToolEdgeCount            int        `json:"temporal_tool_edge_count"`
			ToolCallCount                    int        `json:"tool_call_count"`
			RepoViewInvocationCap            int        `json:"repo_view_invocation_cap"`
			RegularInputTokens               int64      `json:"regular_input_tokens"`
			RepoViewBoundViolationCount      int        `json:"repo_view_bound_violation_count"`
			RepoViewBudgetTamperCommandCount int        `json:"repo_view_budget_tamper_command_count"`
			RepoViewChangedInvocationCount   int        `json:"repo_view_changed_invocation_count"`
			CachedInputTokens                int64      `json:"cached_input_tokens"`
			OutputTokens                     int64      `json:"output_tokens"`
			EffectiveTokens                  float64    `json:"effective_tokens"`
			RepoViewFindInvocationCount      int        `json:"repo_view_find_invocation_count"`
			RepoViewInspectInvocationCount   int        `json:"repo_view_inspect_invocation_count"`
			RepoViewToolCallCount            int        `json:"repo_view_tool_call_count"`
			MechanicalNavigationEnforced     bool       `json:"mechanical_navigation_semantics_enforced"`
			RepoViewNavigationSemanticsValid bool       `json:"repo_view_navigation_semantics_valid"`
			RepoViewFirstInvocationChanged   bool       `json:"repo_view_first_invocation_changed"`
			RepoViewDeepSequenceExact        bool       `json:"repo_view_deep_command_sequence_exact"`
			RepoViewDeepDependencyExact      bool       `json:"repo_view_deep_dependency_awk_exact"`
			RepoViewInvocationCapExceeded    bool       `json:"repo_view_invocation_cap_exceeded"`
			Completed                        bool       `json:"completed"`
		} `json:"cases"`
		Comparisons []struct {
			Task                      string  `json:"task"`
			Profile                   string  `json:"profile"`
			EffectiveReductionPercent float64 `json:"effective_reduction_percent"`
		} `json:"comparisons"`
	}
	if err := decodeAggregateJSON(
		aggregate,
		"metrics.json",
		filepath.Join(runDir, "metrics.json"),
		&metrics,
	); err != nil {
		return nil, err
	}

	var static struct {
		Cases []struct {
			Name         string  `json:"name"`
			ScorePercent float64 `json:"score_percent"`
			RequiredPass bool    `json:"required_pass"`
		} `json:"cases"`
	}
	if err := decodeAggregateOutputJSON(
		aggregate,
		"static.json",
		filepath.Join(runDir, "quality", "static.json"),
		&static,
	); err != nil {
		return nil, err
	}

	var judges struct {
		Baselines []struct {
			Name                 string  `json:"name"`
			JudgeCount           int     `json:"judge_count"`
			AverageCorrectness   float64 `json:"average_correctness"`
			AverageCompleteness  float64 `json:"average_completeness"`
			AverageGrounding     float64 `json:"average_grounding"`
			AverageTaskAdherence float64 `json:"average_task_adherence"`
		} `json:"baselines"`
		Candidates []struct {
			Name                          string   `json:"name"`
			CriticalOmissions             []string `json:"critical_omissions"`
			UnsupportedClaims             []string `json:"unsupported_claims"`
			BaselineMaterialPointsOmitted []string `json:"baseline_material_points_omitted"`
			MaterialContradictions        []string `json:"material_contradictions"`
			JudgeCount                    int      `json:"judge_count"`
			AverageCorrectness            float64  `json:"average_correctness"`
			AverageCompleteness           float64  `json:"average_completeness"`
			AverageGrounding              float64  `json:"average_grounding"`
			AverageTaskAdherence          float64  `json:"average_task_adherence"`
			AllNotWorse                   bool     `json:"all_not_worse"`
			AllCoreConclusionMatch        bool     `json:"all_core_conclusion_match"`
		} `json:"candidates"`
	}
	if err := decodeAggregateOutputJSON(
		aggregate,
		"judges.json",
		filepath.Join(runDir, "quality", "judges.json"),
		&judges,
	); err != nil {
		return nil, err
	}

	caseByName := make(map[string]int, len(metrics.Cases))
	for index := range metrics.Cases {
		caseByName[metrics.Cases[index].Name] = index
	}
	if len(names) == 0 {
		names = make([]string, 0, len(metrics.Cases))
		for _, current := range metrics.Cases {
			names = append(names, current.Name)
		}
	}
	staticByName := make(map[string]int, len(static.Cases))
	for index := range static.Cases {
		staticByName[static.Cases[index].Name] = index
	}
	judgeByName := make(map[string]int, len(judges.Candidates))
	for index := range judges.Candidates {
		judgeByName[judges.Candidates[index].Name] = index
	}

	result := make([]EvidenceMetric, 0, len(names))
	for _, name := range names {
		caseIndex, ok := caseByName[name]
		if !ok {
			return nil, fmt.Errorf("metrics has no case %q", name)
		}
		current := metrics.Cases[caseIndex]
		summary := EvidenceMetric{
			Name:                          current.Name,
			Task:                          current.Task,
			Variant:                       current.Variant,
			Profile:                       current.Profile,
			Completed:                     current.Completed,
			RegularInputTokens:            current.RegularInputTokens,
			CachedInputTokens:             current.CachedInputTokens,
			OutputTokens:                  current.OutputTokens,
			EffectiveTokens:               current.EffectiveTokens,
			TotalToolCalls:                current.ToolCallCount,
			RepoViewToolCalls:             current.RepoViewToolCallCount,
			OtherToolCalls:                current.OtherToolCallCount,
			RepoViewInvocations:           current.RepoViewInvocationCount,
			TemporalEdges:                 current.TemporalToolEdgeCount,
			OutputReferenceEdges:          current.OutputReferenceEdgeCount,
			RepoViewInvocationCap:         current.RepoViewInvocationCap,
			RepoViewInvocationCapExceeded: current.RepoViewInvocationCapExceeded,
			RepoViewBoundViolations:       current.RepoViewBoundViolationCount,
			RepoViewBudgetTamperCommands:  current.RepoViewBudgetTamperCommandCount,
			RepoViewChangedInvocations:    current.RepoViewChangedInvocationCount,
			RepoViewFirstChanged:          current.RepoViewFirstInvocationChanged,
			RepoViewDeepSequenceExact:     current.RepoViewDeepSequenceExact,
			RepoViewDeepDependencyExact:   current.RepoViewDeepDependencyExact,
			RepoViewNavigationValid:       current.RepoViewNavigationSemanticsValid,
			MechanicalNavigationEnforced:  current.MechanicalNavigationEnforced,
			RepoViewFindInvocations:       current.RepoViewFindInvocationCount,
			RepoViewInspectInvocations:    current.RepoViewInspectInvocationCount,
			RepoViewOutlineInvocations:    current.RepoViewOutlineInvocationCount,
			ToolTypes:                     current.ToolTypes,
			Operations:                    current.Operations,
			ToolStatsFile:                 current.ToolStatsFile,
			CallGraphDOTFile:              current.CallGraphDOTFile,
			CallGraphMarkdownFile:         current.CallGraphMarkdownFile,
		}
		comparisonCount := 0
		for _, comparison := range metrics.Comparisons {
			if comparison.Task != current.Task || comparison.Profile != current.Profile {
				continue
			}
			comparisonCount++
			summary.HasComparison = true
			summary.EffectiveReductionPercent = comparison.EffectiveReductionPercent
		}
		if comparisonCount > 1 {
			return nil, fmt.Errorf(
				"metrics has duplicate comparisons for %s/%s",
				current.Task,
				current.Profile,
			)
		}
		if staticIndex, ok := staticByName[name]; ok {
			summary.StaticScorePercent = static.Cases[staticIndex].ScorePercent
			summary.StaticRequiredPass = static.Cases[staticIndex].RequiredPass
		}
		if judgeIndex, ok := judgeByName[name]; ok {
			judge := judges.Candidates[judgeIndex]
			summary.JudgeCount = judge.JudgeCount
			summary.JudgeStatus = "not-worse"
			summary.JudgeNotWorse = judge.AllNotWorse
			summary.JudgeCoreConclusionMatch = judge.AllCoreConclusionMatch
			summary.JudgeCriticalOmissions = len(judge.CriticalOmissions)
			summary.JudgeUnsupportedClaims = len(judge.UnsupportedClaims)
			summary.JudgeBaselinePointsOmitted = len(
				judge.BaselineMaterialPointsOmitted,
			)
			summary.JudgeMaterialContradictions = len(judge.MaterialContradictions)
			if !judge.AllNotWorse {
				summary.JudgeStatus = "worse"
			}
			summary.AverageCorrectness = judge.AverageCorrectness
			summary.AverageCompleteness = judge.AverageCompleteness
			summary.AverageGrounding = judge.AverageGrounding
			summary.AverageTaskAdherence = judge.AverageTaskAdherence
		} else {
			for _, judge := range judges.Baselines {
				if judge.Name != name {
					continue
				}
				summary.JudgeCount = judge.JudgeCount
				summary.JudgeStatus = "reference"
				summary.AverageCorrectness = judge.AverageCorrectness
				summary.AverageCompleteness = judge.AverageCompleteness
				summary.AverageGrounding = judge.AverageGrounding
				summary.AverageTaskAdherence = judge.AverageTaskAdherence
				break
			}
		}
		result = append(result, summary)
	}
	return result, nil
}

func decodeAggregateJSON(
	aggregate *qualityAggregateSnapshot,
	relative string,
	fallbackPath string,
	target any,
) error {
	if aggregate == nil {
		return readJSON(fallbackPath, target)
	}
	content, ok := aggregate.inputs[relative]
	if !ok {
		return fmt.Errorf("quality aggregate omits input %s", relative)
	}
	if err := json.Unmarshal(content, target); err != nil {
		return fmt.Errorf("decode quality input %s: %w", relative, err)
	}
	return nil
}

func decodeAggregateOutputJSON(
	aggregate *qualityAggregateSnapshot,
	name string,
	fallbackPath string,
	target any,
) error {
	if aggregate == nil {
		return readJSON(fallbackPath, target)
	}
	content, ok := aggregate.outputs[name]
	if !ok {
		return fmt.Errorf("quality aggregate omits output %s", name)
	}
	if err := json.Unmarshal(content, target); err != nil {
		return fmt.Errorf("decode quality output %s: %w", name, err)
	}
	return nil
}

func ValidatePromotion(
	metrics []EvidenceMetric,
	minimumJudgeCount int,
) []CheckResult {
	baselines := make(map[string]EvidenceMetric)
	for _, metric := range metrics {
		if metric.Variant == "baseline" {
			baselines[metric.Task] = metric
		}
	}

	var results []CheckResult
	candidateCount := 0
	for _, candidate := range metrics {
		if candidate.Variant != "optimized" {
			continue
		}
		candidateCount++
		prefix := "promotion " + candidate.Name + ": "
		baseline, hasBaseline := baselines[candidate.Task]
		results = append(results,
			promotionCheck(prefix+"completed", candidate.Completed, true),
			promotionCheck(
				prefix+"has matching token comparison",
				candidate.HasComparison,
				true,
			),
			promotionCheck(
				prefix+"effective token saving is positive",
				candidate.EffectiveReductionPercent,
				"> 0",
				candidate.HasComparison && candidate.EffectiveReductionPercent > 0,
			),
			promotionCheck(
				prefix+"has matching baseline",
				hasBaseline,
				true,
			),
			promotionCheck(
				prefix+"required static criteria pass",
				candidate.StaticRequiredPass,
				true,
			),
			promotionCheck(
				prefix+"static quality is not worse",
				candidate.StaticScorePercent,
				fmt.Sprintf(">= %.1f", baseline.StaticScorePercent),
				hasBaseline &&
					candidate.StaticScorePercent >= baseline.StaticScorePercent,
			),
			promotionCheck(
				prefix+"uses repo-view navigation",
				candidate.RepoViewInvocations,
				"> 0",
				candidate.RepoViewInvocations > 0,
			),
			promotionCheck(
				prefix+"begins with exactly one changed call",
				map[string]any{
					"changed_invocations": candidate.RepoViewChangedInvocations,
					"first_changed":       candidate.RepoViewFirstChanged,
				},
				"changed_invocations=1, first_changed=true",
				candidate.RepoViewChangedInvocations == 1 &&
					candidate.RepoViewFirstChanged,
			),
			promotionCheck(
				prefix+"mechanical navigation semantics pass",
				candidate.RepoViewNavigationValid &&
					candidate.MechanicalNavigationEnforced,
				true,
			),
			promotionCheck(
				prefix+"has a hard navigation cap",
				candidate.RepoViewInvocationCap,
				"> 0",
				candidate.RepoViewInvocationCap > 0,
			),
			promotionCheck(
				prefix+"stays within navigation cap",
				candidate.RepoViewInvocationCapExceeded,
				false,
			),
			promotionCheck(
				prefix+"has no result-bound violations",
				candidate.RepoViewBoundViolations,
				0,
			),
			promotionCheck(
				prefix+"has no budget tampering",
				candidate.RepoViewBudgetTamperCommands,
				0,
			),
		)
		if strings.HasPrefix(candidate.Task, "deep-") &&
			candidate.Profile == "investigative-verified-high" {
			results = append(results,
				promotionCheck(
					prefix+"uses the exact verified deep tool trace",
					map[string]any{
						"repo_view_invocations": candidate.RepoViewInvocations,
						"changed":               candidate.RepoViewChangedInvocations,
						"find":                  candidate.RepoViewFindInvocations,
						"inspect":               candidate.RepoViewInspectInvocations,
						"outline":               candidate.RepoViewOutlineInvocations,
						"other_tool_calls":      candidate.OtherToolCalls,
						"sequence_exact":        candidate.RepoViewDeepSequenceExact,
						"dependency_awk_exact":  candidate.RepoViewDeepDependencyExact,
					},
					"repo-view=8, changed=1, find=2, inspect=4, outline=1, other=1, exact=true",
					candidate.RepoViewInvocations == 8 &&
						candidate.RepoViewChangedInvocations == 1 &&
						candidate.RepoViewFindInvocations == 2 &&
						candidate.RepoViewInspectInvocations == 4 &&
						candidate.RepoViewOutlineInvocations == 1 &&
						candidate.OtherToolCalls == 1 &&
						candidate.RepoViewDeepSequenceExact &&
						candidate.RepoViewDeepDependencyExact,
				),
			)
		}
		if minimumJudgeCount > 0 {
			results = append(results,
				promotionCheck(
					prefix+"judge count",
					candidate.JudgeCount,
					fmt.Sprintf(">= %d", minimumJudgeCount),
					candidate.JudgeCount >= minimumJudgeCount,
				),
				promotionCheck(
					prefix+"judges find no regression",
					candidate.JudgeNotWorse,
					true,
				),
				promotionCheck(
					prefix+"judge critical omissions",
					candidate.JudgeCriticalOmissions,
					0,
				),
				promotionCheck(
					prefix+"judge unsupported claims",
					candidate.JudgeUnsupportedClaims,
					0,
				),
				promotionCheck(
					prefix+"judge material contradictions",
					candidate.JudgeMaterialContradictions,
					0,
				),
				promotionCheck(
					prefix+"judge baseline points omitted",
					candidate.JudgeBaselinePointsOmitted,
					0,
				),
			)
		}
	}
	if candidateCount == 0 {
		results = append(results, promotionCheck(
			"promotion has an optimized candidate",
			0,
			"> 0",
			false,
		))
	}
	return results
}

func promotionCheck(
	description string,
	actual any,
	expected any,
	passed ...bool,
) CheckResult {
	result := actual == expected
	if len(passed) > 0 {
		result = passed[0]
	}
	return CheckResult{
		Description: description,
		Passed:      result,
		Actual:      actual,
		Expected:    expected,
	}
}

func Passed(checks []CheckResult) bool {
	for _, check := range checks {
		if !check.Passed {
			return false
		}
	}
	return true
}

func readJSON(path string, target any) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	if err := json.Unmarshal(data, target); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	return nil
}

func validateToolAccounting(
	runDir string,
	aggregates ...*qualityAggregateSnapshot,
) []CheckResult {
	var aggregate *qualityAggregateSnapshot
	if len(aggregates) > 0 {
		aggregate = aggregates[0]
	}
	enforceCurrentNavigation := aggregate == nil || aggregate.validation.StrictEvidence
	var metrics any
	var err error
	if aggregate != nil {
		metrics, err = loadJSONBytes(
			"quality input metrics.json",
			aggregate.inputs["metrics.json"],
		)
	} else {
		metrics, err = loadJSON(filepath.Join(runDir, "metrics.json"))
	}
	if err != nil {
		return []CheckResult{{
			Description: "metrics and tool accounting are readable",
			Passed:      false,
			Error:       err.Error(),
		}}
	}
	root, ok := metrics.(map[string]any)
	if !ok {
		return []CheckResult{{
			Description: "metrics root is an object",
			Passed:      false,
			Error:       "metrics.json root is not an object",
		}}
	}
	rawCases, ok := root["cases"].([]any)
	if !ok {
		return []CheckResult{{
			Description: "metrics cases are present",
			Passed:      false,
			Error:       "metrics.json has no cases array",
		}}
	}

	var results []CheckResult
	for _, rawCase := range rawCases {
		current, ok := rawCase.(map[string]any)
		if !ok {
			continue
		}
		name, _ := current["name"].(string)
		description := "tool accounting and call graph: " + name
		total, totalOK := number(current["tool_call_count"])
		repoView, repoViewOK := number(current["repo_view_tool_call_count"])
		other, otherOK := number(current["other_tool_call_count"])
		temporal, temporalOK := number(current["temporal_tool_edge_count"])
		outputReferences, outputReferencesOK := number(current["output_reference_edge_count"])
		invocationsValid, _ := current["repo_view_invocation_accounting_valid"].(bool)
		repoViewCallsValid, _ := current["repo_view_tool_call_accounting_valid"].(bool)
		accountingValid, _ := current["tool_call_accounting_valid"].(bool)
		budgetAccountingValid, budgetAccountingPresent :=
			current["repo_view_budget_accounting_valid"].(bool)
		commandShapeValid, commandShapePresent :=
			current["repo_view_command_shape_valid"].(bool)
		firstInvocationChanged, firstInvocationPresent :=
			current["repo_view_first_invocation_changed"].(bool)
		navigationSemanticsValid, navigationSemanticsPresent :=
			current["repo_view_navigation_semantics_valid"].(bool)
		mechanicalSemanticsEnforced, mechanicalSemanticsPresent :=
			current["mechanical_navigation_semantics_enforced"].(bool)
		changedInvocations, changedInvocationsOK := number(
			current["repo_view_changed_invocation_count"],
		)
		variant, _ := current["variant"].(string)
		task, _ := current["task"].(string)
		profile, _ := current["profile"].(string)
		repoViewInvocations, repoViewInvocationsOK := number(
			current["repo_view_invocation_count"],
		)
		findInvocations, findInvocationsOK := number(
			current["repo_view_find_invocation_count"],
		)
		inspectInvocations, inspectInvocationsOK := number(
			current["repo_view_inspect_invocation_count"],
		)
		outlineInvocations, outlineInvocationsOK := number(
			current["repo_view_outline_invocation_count"],
		)
		deepSequenceExact, deepSequencePresent :=
			current["repo_view_deep_command_sequence_exact"].(bool)
		deepDependencyExact, deepDependencyPresent :=
			current["repo_view_deep_dependency_awk_exact"].(bool)
		verifiedDeep := enforceCurrentNavigation && variant == "optimized" &&
			strings.HasPrefix(task, "deep-") &&
			profile == "investigative-verified-high"
		invocationCap, invocationCapOK := number(
			current["repo_view_invocation_cap"],
		)
		if !totalOK || !repoViewOK || !otherOK || !temporalOK ||
			!outputReferencesOK || !budgetAccountingPresent ||
			!commandShapePresent || !firstInvocationPresent ||
			!navigationSemanticsPresent || !mechanicalSemanticsPresent ||
			!changedInvocationsOK || !invocationCapOK ||
			(verifiedDeep &&
				(!repoViewInvocationsOK || !findInvocationsOK ||
					!inspectInvocationsOK || !outlineInvocationsOK ||
					!deepSequencePresent || !deepDependencyPresent)) {
			results = append(results, CheckResult{
				Description: description,
				Passed:      false,
				Error:       "missing numeric tool-accounting fields",
			})
			continue
		}

		maxTemporal := math.Max(total-1, 0)
		verifiedDeepValid := !verifiedDeep ||
			(repoViewInvocations == 8 &&
				changedInvocations == 1 &&
				findInvocations == 2 &&
				inspectInvocations == 4 &&
				outlineInvocations == 1 &&
				other == 1 &&
				deepSequenceExact &&
				deepDependencyExact)
		passed := total == repoView+other &&
			temporal <= maxTemporal &&
			accountingValid &&
			invocationsValid &&
			repoViewCallsValid &&
			(!enforceCurrentNavigation ||
				(commandShapeValid &&
					(invocationCap == 0 || budgetAccountingValid) &&
					(variant != "optimized" ||
						(mechanicalSemanticsEnforced &&
							firstInvocationChanged &&
							navigationSemanticsValid &&
							changedInvocations == 1)))) &&
			verifiedDeepValid
		var problems []string
		if total != repoView+other {
			problems = append(problems, "total != repo-view + other")
		}
		if temporal > maxTemporal {
			problems = append(problems, "temporal edge count exceeds max(total-1, 0)")
		}
		if !accountingValid || !invocationsValid || !repoViewCallsValid {
			problems = append(problems, "analyzer cross-check failed")
		}
		if enforceCurrentNavigation && !commandShapeValid {
			problems = append(problems, "repo-view command shape is invalid")
		}
		if enforceCurrentNavigation && variant == "optimized" &&
			!mechanicalSemanticsEnforced {
			problems = append(
				problems,
				"mechanical navigation semantics were not enforced",
			)
		}
		if enforceCurrentNavigation && variant == "optimized" &&
			(!firstInvocationChanged ||
				!navigationSemanticsValid ||
				changedInvocations != 1) {
			problems = append(
				problems,
				"optimized navigation does not begin with one valid changed call",
			)
		}
		if enforceCurrentNavigation && invocationCap > 0 && !budgetAccountingValid {
			problems = append(problems, "repo-view budget accounting is invalid")
		}
		if !verifiedDeepValid {
			problems = append(
				problems,
				"verified deep trace is not the exact 8 repo-view plus 1 awk contract",
			)
		}

		for _, field := range []string{"call_graph_dot_file", "call_graph_markdown_file"} {
			relative, _ := current[field].(string)
			if relative == "" {
				passed = false
				problems = append(problems, field+" missing")
				continue
			}
			if aggregate != nil {
				if _, ok := aggregate.inputs[relative]; !ok {
					passed = false
					problems = append(problems, field+": missing from immutable snapshot")
				}
			} else {
				if _, err := os.Stat(
					filepath.Join(runDir, filepath.FromSlash(relative)),
				); err != nil {
					passed = false
					problems = append(problems, field+": "+err.Error())
				}
			}
		}
		statsRelative, _ := current["tool_stats_file"].(string)
		if statsRelative == "" {
			passed = false
			problems = append(problems, "tool_stats_file missing")
		} else {
			var statsDocument any
			var err error
			if aggregate != nil {
				statsDocument, err = loadJSONBytes(
					"quality input "+statsRelative,
					aggregate.inputs[statsRelative],
				)
			} else {
				statsPath := filepath.Join(runDir, filepath.FromSlash(statsRelative))
				statsDocument, err = loadJSON(statsPath)
			}
			if err != nil {
				passed = false
				problems = append(problems, "tool_stats_file: "+err.Error())
			} else if err := validateStatsDocument(
				statsDocument,
				total,
				temporal,
				outputReferences,
			); err != nil {
				passed = false
				problems = append(problems, "tool_stats_file: "+err.Error())
			}
		}
		results = append(results, CheckResult{
			Description: description,
			Passed:      passed,
			Actual: map[string]any{
				"total":                  total,
				"repo_view":              repoView,
				"other":                  other,
				"temporal_edges":         temporal,
				"output_reference_edges": outputReferences,
			},
			Expected: "total = repo-view + other; temporal edges <= max(total-1, 0); graph artifacts exist",
			Error:    strings.Join(problems, "; "),
		})
	}
	return results
}

func validateStatsDocument(document any, total, temporal, outputReferences float64) error {
	root, ok := document.(map[string]any)
	if !ok {
		return fmt.Errorf("root is not an object")
	}
	statsTotal, ok := number(root["total_tool_calls"])
	if !ok || statsTotal != total {
		return fmt.Errorf("total_tool_calls does not match metrics")
	}
	statsTemporal, ok := number(root["temporal_edge_count"])
	if !ok || statsTemporal != temporal {
		return fmt.Errorf("temporal_edge_count does not match metrics")
	}
	statsReferences, ok := number(root["output_reference_edge_count"])
	if !ok || statsReferences != outputReferences {
		return fmt.Errorf("output_reference_edge_count does not match metrics")
	}
	graph, ok := root["call_graph"].(map[string]any)
	if !ok {
		return fmt.Errorf("call_graph is missing")
	}
	nodes, nodesOK := graph["nodes"].([]any)
	if graph["nodes"] == nil && total == 0 {
		nodesOK = true
	}
	if !nodesOK || float64(len(nodes)) != total {
		return fmt.Errorf("call_graph node count does not match total tool calls")
	}
	edges, edgesOK := graph["edges"].([]any)
	if graph["edges"] == nil && temporal == 0 && outputReferences == 0 {
		edgesOK = true
	}
	if !edgesOK {
		return fmt.Errorf("call_graph edges are missing")
	}
	var actualTemporal int
	var actualReferences int
	for _, rawEdge := range edges {
		edge, ok := rawEdge.(map[string]any)
		if !ok {
			continue
		}
		switch edge["kind"] {
		case "next_tool_call":
			actualTemporal++
		case "output_reference":
			actualReferences++
		}
	}
	if float64(actualTemporal) != temporal || float64(actualReferences) != outputReferences {
		return fmt.Errorf("call_graph edge counts do not match metrics")
	}
	return nil
}

func loadSourceSnapshot(
	runDir, source string,
	aggregate *qualityAggregateSnapshot,
) (any, error) {
	if aggregate == nil {
		return loadSource(runDir, source)
	}
	if source == "answer" {
		return loadAnswersSnapshot(aggregate)
	}
	if source == "quality.static_criterion" {
		document, err := loadJSONBytes(
			"quality output static.json",
			aggregate.outputs["static.json"],
		)
		if err != nil {
			return nil, err
		}
		return flattenStaticCriteria(document)
	}

	var content []byte
	var collection string
	switch source {
	case "metrics.case":
		content = aggregate.inputs["metrics.json"]
		collection = "cases"
	case "metrics.comparison":
		content = aggregate.inputs["metrics.json"]
		collection = "comparisons"
	case "quality.static":
		content = aggregate.outputs["static.json"]
		collection = "cases"
	case "quality.judge":
		content = aggregate.outputs["judges.json"]
		collection = "candidates"
	default:
		return nil, fmt.Errorf("unknown assertion source %q", source)
	}
	document, err := loadJSONBytes("quality snapshot for "+source, content)
	if err != nil {
		return nil, err
	}
	root, ok := document.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("quality snapshot for %s is not an object", source)
	}
	value, ok := root[collection]
	if !ok {
		return nil, fmt.Errorf(
			"quality snapshot for %s has no %s collection",
			source,
			collection,
		)
	}
	return value, nil
}

func loadAnswersSnapshot(aggregate *qualityAggregateSnapshot) (any, error) {
	document, err := loadJSONBytes(
		"quality input metrics.json",
		aggregate.inputs["metrics.json"],
	)
	if err != nil {
		return nil, err
	}
	root, ok := document.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("metrics root is not an object")
	}
	rawCases, ok := root["cases"].([]any)
	if !ok {
		return nil, fmt.Errorf("metrics has no cases collection")
	}
	answers := make([]any, 0, len(rawCases))
	for _, rawCase := range rawCases {
		current, ok := rawCase.(map[string]any)
		if !ok {
			continue
		}
		relative, _ := current["answer_file"].(string)
		content, ok := aggregate.inputs[relative]
		if relative == "" || !ok {
			return nil, fmt.Errorf("quality snapshot omits answer %s", relative)
		}
		answers = append(answers, map[string]any{
			"name":    current["name"],
			"task":    current["task"],
			"profile": current["profile"],
			"content": string(content),
		})
	}
	return answers, nil
}

func loadSource(runDir, source string) (any, error) {
	if source == "answer" {
		return loadAnswers(runDir)
	}
	if source == "quality.static_criterion" {
		return loadStaticCriteria(runDir)
	}

	var path string
	var collection string
	switch source {
	case "metrics.case":
		path = filepath.Join(runDir, "metrics.json")
		collection = "cases"
	case "metrics.comparison":
		path = filepath.Join(runDir, "metrics.json")
		collection = "comparisons"
	case "quality.static":
		path = filepath.Join(runDir, "quality", "static.json")
		collection = "cases"
	case "quality.judge":
		path = filepath.Join(runDir, "quality", "judges.json")
		collection = "candidates"
	default:
		return nil, fmt.Errorf("unknown assertion source %q", source)
	}
	document, err := loadJSON(path)
	if err != nil {
		return nil, err
	}
	root, ok := document.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("%s root is not an object", path)
	}
	value, ok := root[collection]
	if !ok {
		return nil, fmt.Errorf("%s has no %s collection", path, collection)
	}
	return value, nil
}

func loadAnswers(runDir string) (any, error) {
	document, err := loadJSON(filepath.Join(runDir, "metrics.json"))
	if err != nil {
		return nil, err
	}
	root, ok := document.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("metrics root is not an object")
	}
	rawCases, ok := root["cases"].([]any)
	if !ok {
		return nil, fmt.Errorf("metrics has no cases collection")
	}
	answers := make([]any, 0, len(rawCases))
	for _, rawCase := range rawCases {
		current, ok := rawCase.(map[string]any)
		if !ok {
			continue
		}
		relative, _ := current["answer_file"].(string)
		if relative == "" {
			continue
		}
		content, err := os.ReadFile(filepath.Join(runDir, filepath.FromSlash(relative)))
		if err != nil {
			return nil, fmt.Errorf("read answer %s: %w", relative, err)
		}
		answers = append(answers, map[string]any{
			"name":    current["name"],
			"task":    current["task"],
			"profile": current["profile"],
			"content": string(content),
		})
	}
	return answers, nil
}

func loadStaticCriteria(runDir string) (any, error) {
	document, err := loadJSON(filepath.Join(runDir, "quality", "static.json"))
	if err != nil {
		return nil, err
	}
	return flattenStaticCriteria(document)
}

func flattenStaticCriteria(document any) (any, error) {
	root, ok := document.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("static quality root is not an object")
	}
	rawCases, ok := root["cases"].([]any)
	if !ok {
		return nil, fmt.Errorf("static quality has no cases collection")
	}
	var criteria []any
	for _, rawCase := range rawCases {
		current, ok := rawCase.(map[string]any)
		if !ok {
			continue
		}
		rawCriteria, _ := current["criteria"].([]any)
		for _, rawCriterion := range rawCriteria {
			criterion, ok := rawCriterion.(map[string]any)
			if !ok {
				continue
			}
			flattened := make(map[string]any, len(criterion)+4)
			for key, value := range criterion {
				flattened[key] = value
			}
			for _, key := range []string{"name", "task", "profile", "variant"} {
				flattened[key] = current[key]
			}
			criteria = append(criteria, flattened)
		}
	}
	return criteria, nil
}

func loadJSON(path string) (any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return loadJSONBytes(path, data)
}

func loadJSONBytes(name string, data []byte) (any, error) {
	if data == nil {
		return nil, fmt.Errorf("%s is missing", name)
	}
	var value any
	if err := json.Unmarshal(data, &value); err != nil {
		return nil, fmt.Errorf("decode %s: %w", name, err)
	}
	return value, nil
}

func selectField(document any, selector map[string]any, field string) (any, error) {
	items, ok := document.([]any)
	if !ok {
		return nil, fmt.Errorf("assertion source is not an array")
	}
	var matches []map[string]any
	for _, raw := range items {
		item, ok := raw.(map[string]any)
		if !ok || !matchesSelector(item, selector) {
			continue
		}
		matches = append(matches, item)
	}
	if len(matches) != 1 {
		return nil, fmt.Errorf("selector matched %d objects, expected exactly one", len(matches))
	}
	var current any = matches[0]
	for _, part := range strings.Split(field, ".") {
		object, ok := current.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("field %q crosses a non-object at %q", field, part)
		}
		var exists bool
		current, exists = object[part]
		if !exists {
			return nil, fmt.Errorf("field %q does not exist", field)
		}
	}
	return current, nil
}

func matchesSelector(item map[string]any, selector map[string]any) bool {
	for key, expected := range selector {
		actual, ok := item[key]
		if !ok || !valuesEqual(actual, expected) {
			return false
		}
	}
	return true
}

func compare(actual any, operator string, expected any) (bool, error) {
	switch operator {
	case "eq":
		return valuesEqual(actual, expected), nil
	case "ne":
		return !valuesEqual(actual, expected), nil
	case "gt", "gte", "lt", "lte":
		actualNumber, ok := number(actual)
		if !ok {
			return false, fmt.Errorf("actual value %v is not numeric", actual)
		}
		expectedNumber, ok := number(expected)
		if !ok {
			return false, fmt.Errorf("expected value %v is not numeric", expected)
		}
		switch operator {
		case "gt":
			return actualNumber > expectedNumber, nil
		case "gte":
			return actualNumber >= expectedNumber, nil
		case "lt":
			return actualNumber < expectedNumber, nil
		default:
			return actualNumber <= expectedNumber, nil
		}
	case "contains":
		actualString, ok := actual.(string)
		if !ok {
			return false, fmt.Errorf("actual value is not a string")
		}
		expectedString, ok := expected.(string)
		if !ok {
			return false, fmt.Errorf("expected value is not a string")
		}
		return strings.Contains(actualString, expectedString), nil
	default:
		return false, fmt.Errorf("unknown assertion operator %q", operator)
	}
}

func valuesEqual(left, right any) bool {
	leftNumber, leftOK := number(left)
	rightNumber, rightOK := number(right)
	if leftOK && rightOK {
		return leftNumber == rightNumber
	}
	return reflect.DeepEqual(left, right)
}

func number(value any) (float64, bool) {
	switch typed := value.(type) {
	case float64:
		return typed, true
	case float32:
		return float64(typed), true
	case int:
		return float64(typed), true
	case int64:
		return float64(typed), true
	case json.Number:
		parsed, err := typed.Float64()
		return parsed, err == nil
	case string:
		parsed, err := strconv.ParseFloat(typed, 64)
		return parsed, err == nil
	default:
		return 0, false
	}
}
