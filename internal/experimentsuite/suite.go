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
	"sort"
	"strconv"
	"strings"
)

type Manifest struct {
	SchemaVersion int    `json:"schema_version"`
	Cases         []Case `json:"cases"`
}

type ResolutionManifest struct {
	SchemaVersion int              `json:"schema_version"`
	Cases         []ResolutionCase `json:"cases"`
}

type Case struct {
	ID                     string      `json:"id"`
	Level                  int         `json:"level"`
	Complexity             string      `json:"complexity"`
	Outcome                string      `json:"outcome"`
	Evidence               string      `json:"evidence"`
	SourceChecksumSHA256   string      `json:"source_checksum_sha256"`
	QualityAggregateSHA256 string      `json:"quality_aggregate_sha256"`
	QualityProvenance      string      `json:"quality_provenance,omitempty"`
	Description            string      `json:"description"`
	Live                   *LiveConfig `json:"live,omitempty"`
	Assertions             []Assertion `json:"assertions"`
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
}

type Assertion struct {
	Description string         `json:"description"`
	Source      string         `json:"source"`
	Selector    map[string]any `json:"selector,omitempty"`
	Field       string         `json:"field"`
	Operator    string         `json:"operator"`
	Value       any            `json:"value"`
}

type CheckResult struct {
	Description string `json:"description"`
	Passed      bool   `json:"passed"`
	Actual      any    `json:"actual,omitempty"`
	Expected    any    `json:"expected,omitempty"`
	Error       string `json:"error,omitempty"`
}

type ToolStat struct {
	Name             string `json:"name"`
	ToolCalls        int    `json:"tool_calls"`
	Invocations      int    `json:"invocations"`
	OutputCharacters int    `json:"output_characters"`
}

type EvidenceMetric struct {
	Name                          string     `json:"name"`
	Task                          string     `json:"task"`
	Variant                       string     `json:"variant"`
	Profile                       string     `json:"profile"`
	Completed                     bool       `json:"completed"`
	RegularInputTokens            int64      `json:"regular_input_tokens"`
	CachedInputTokens             int64      `json:"cached_input_tokens"`
	OutputTokens                  int64      `json:"output_tokens"`
	EffectiveTokens               float64    `json:"effective_tokens"`
	TotalToolCalls                int        `json:"total_tool_calls"`
	RepoViewToolCalls             int        `json:"repo_view_tool_calls"`
	OtherToolCalls                int        `json:"other_tool_calls"`
	RepoViewInvocations           int        `json:"repo_view_invocations"`
	TemporalEdges                 int        `json:"temporal_edges"`
	OutputReferenceEdges          int        `json:"output_reference_edges"`
	RepoViewInvocationCap         int        `json:"repo_view_invocation_cap"`
	RepoViewInvocationCapExceeded bool       `json:"repo_view_invocation_cap_exceeded"`
	RepoViewBoundViolations       int        `json:"repo_view_bound_violations"`
	RepoViewBudgetTamperCommands  int        `json:"repo_view_budget_tamper_commands"`
	RepoViewChangedInvocations    int        `json:"repo_view_changed_invocations"`
	RepoViewFirstChanged          bool       `json:"repo_view_first_invocation_changed"`
	RepoViewNavigationValid       bool       `json:"repo_view_navigation_semantics_valid"`
	MechanicalNavigationEnforced  bool       `json:"mechanical_navigation_semantics_enforced"`
	RepoViewFindInvocations       int        `json:"repo_view_find_invocations"`
	RepoViewInspectInvocations    int        `json:"repo_view_inspect_invocations"`
	RepoViewOutlineInvocations    int        `json:"repo_view_outline_invocations"`
	ToolTypes                     []ToolStat `json:"tool_types"`
	Operations                    []ToolStat `json:"operations"`
	ToolStatsFile                 string     `json:"tool_stats_file"`
	CallGraphDOTFile              string     `json:"call_graph_dot_file"`
	CallGraphMarkdownFile         string     `json:"call_graph_markdown_file"`
	HasComparison                 bool       `json:"has_comparison"`
	EffectiveReductionPercent     float64    `json:"effective_reduction_percent"`
	StaticScorePercent            float64    `json:"static_score_percent"`
	StaticRequiredPass            bool       `json:"static_required_pass"`
	JudgeCount                    int        `json:"judge_count"`
	JudgeStatus                   string     `json:"judge_status"`
	JudgeNotWorse                 bool       `json:"judge_not_worse"`
	JudgeCoreConclusionMatch      bool       `json:"judge_core_conclusion_match"`
	JudgeCriticalOmissions        int        `json:"judge_critical_omissions"`
	JudgeUnsupportedClaims        int        `json:"judge_unsupported_claims"`
	JudgeBaselinePointsOmitted    int        `json:"judge_baseline_points_omitted"`
	JudgeMaterialContradictions   int        `json:"judge_material_contradictions"`
	AverageCorrectness            float64    `json:"average_correctness"`
	AverageCompleteness           float64    `json:"average_completeness"`
	AverageGrounding              float64    `json:"average_grounding"`
	AverageTaskAdherence          float64    `json:"average_task_adherence"`
}

func LoadManifest(path string) (Manifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Manifest{}, err
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return Manifest{}, fmt.Errorf("decode suite manifest: %w", err)
	}
	if manifest.SchemaVersion != 1 {
		return Manifest{}, fmt.Errorf("unsupported suite schema version %d", manifest.SchemaVersion)
	}
	seen := make(map[string]bool)
	for _, testCase := range manifest.Cases {
		if !validSuiteSlug(testCase.ID) || seen[testCase.ID] {
			return Manifest{}, fmt.Errorf("missing or duplicate suite case ID %q", testCase.ID)
		}
		seen[testCase.ID] = true
		if testCase.Level < 1 ||
			!validSuiteRelativePath(testCase.Evidence) ||
			len(testCase.Assertions) == 0 {
			return Manifest{}, fmt.Errorf(
				"case %s requires a positive level, safe evidence path, and assertions",
				testCase.ID,
			)
		}
		if !validSHA256Digest(testCase.SourceChecksumSHA256) {
			return Manifest{}, fmt.Errorf(
				"case %s requires a lowercase source checksum SHA-256 digest",
				testCase.ID,
			)
		}
		if !validSHA256Digest(testCase.QualityAggregateSHA256) {
			return Manifest{}, fmt.Errorf(
				"case %s requires a lowercase quality aggregate SHA-256 digest",
				testCase.ID,
			)
		}
		if testCase.QualityProvenance != "" &&
			!validQualityProvenance(testCase.QualityProvenance) {
			return Manifest{}, fmt.Errorf(
				"case %s has invalid quality provenance %q",
				testCase.ID,
				testCase.QualityProvenance,
			)
		}
		if testCase.Outcome != "accepted" && testCase.Outcome != "rejected" {
			return Manifest{}, fmt.Errorf("case %s has invalid outcome %q", testCase.ID, testCase.Outcome)
		}
		if testCase.Outcome == "accepted" &&
			testCase.QualityProvenance == "non-strict" {
			return Manifest{}, fmt.Errorf(
				"accepted case %s cannot use non-strict quality provenance",
				testCase.ID,
			)
		}
		if testCase.Live != nil {
			if testCase.Outcome != "accepted" {
				return Manifest{}, fmt.Errorf("rejected case %s cannot be live-enabled", testCase.ID)
			}
			if !validLiveConfig(*testCase.Live) {
				return Manifest{}, fmt.Errorf("live case %s has incomplete configuration", testCase.ID)
			}
		}
	}
	sort.SliceStable(manifest.Cases, func(i, j int) bool {
		if manifest.Cases[i].Level == manifest.Cases[j].Level {
			return manifest.Cases[i].ID < manifest.Cases[j].ID
		}
		return manifest.Cases[i].Level < manifest.Cases[j].Level
	})
	return manifest, nil
}

func LoadResolutionManifest(path string) (ResolutionManifest, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ResolutionManifest{}, err
	}
	var manifest ResolutionManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return ResolutionManifest{}, fmt.Errorf("decode resolution manifest: %w", err)
	}
	if manifest.SchemaVersion != 1 {
		return ResolutionManifest{}, fmt.Errorf(
			"unsupported resolution schema version %d",
			manifest.SchemaVersion,
		)
	}
	seen := make(map[string]bool)
	for _, resolution := range manifest.Cases {
		if !validSuiteSlug(resolution.ID) || seen[resolution.ID] {
			return ResolutionManifest{}, fmt.Errorf(
				"missing or duplicate resolution case ID %q",
				resolution.ID,
			)
		}
		seen[resolution.ID] = true
		if resolution.Status != "accepted" && resolution.Status != "resolved" {
			return ResolutionManifest{}, fmt.Errorf(
				"resolution %s has invalid status %q",
				resolution.ID,
				resolution.Status,
			)
		}
		if resolution.QualityProvenance == "non-strict" {
			return ResolutionManifest{}, fmt.Errorf(
				"resolution %s cannot use non-strict quality provenance",
				resolution.ID,
			)
		}
		if resolution.RootCause == "" ||
			resolution.Fix == "" ||
			!validSuiteRelativePath(resolution.Evidence) ||
			len(resolution.MetricCases) == 0 ||
			len(resolution.Assertions) == 0 {
			return ResolutionManifest{}, fmt.Errorf(
				"resolution %s requires cause, fix, safe evidence, metric cases, and assertions",
				resolution.ID,
			)
		}
		if !validSHA256Digest(resolution.SourceChecksumSHA256) {
			return ResolutionManifest{}, fmt.Errorf(
				"resolution %s requires a lowercase source checksum SHA-256 digest",
				resolution.ID,
			)
		}
		if !validSHA256Digest(resolution.QualityAggregateSHA256) {
			return ResolutionManifest{}, fmt.Errorf(
				"resolution %s requires a lowercase quality aggregate SHA-256 digest",
				resolution.ID,
			)
		}
		if resolution.QualityProvenance != "" &&
			!validQualityProvenance(resolution.QualityProvenance) {
			return ResolutionManifest{}, fmt.Errorf(
				"resolution %s has invalid quality provenance %q",
				resolution.ID,
				resolution.QualityProvenance,
			)
		}
		if resolution.Status == "resolved" && resolution.Repair == nil {
			return ResolutionManifest{}, fmt.Errorf(
				"resolved case %s requires a repair configuration",
				resolution.ID,
			)
		}
		if resolution.Repair != nil && !validLiveConfig(*resolution.Repair) {
			return ResolutionManifest{}, fmt.Errorf(
				"resolution %s has an incomplete repair configuration",
				resolution.ID,
			)
		}
		for _, goTest := range resolution.GoTests {
			if goTest.Description == "" ||
				!validLocalGoPackage(goTest.Package) ||
				goTest.Run == "" {
				return ResolutionManifest{}, fmt.Errorf(
					"resolution %s has an incomplete Go test",
					resolution.ID,
				)
			}
		}
	}
	return manifest, nil
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

func validateSourceChecksums(runDir string, trackedDigest ...string) CheckResult {
	result := CheckResult{
		Description: "source artifacts match source-SHA256SUMS",
		Expected:    append([]string(nil), sourceChecksumArtifacts...),
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

	expected := make(map[string]string, len(sourceChecksumArtifacts))
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

	actual := make(map[string]string, len(sourceChecksumArtifacts))
	for _, name := range sourceChecksumArtifacts {
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
	return validSuiteTask(config.Task) &&
		validSuiteSlug(config.Profile) &&
		validOptionalSuiteRelativePath(config.BaselineFrom) &&
		validSuiteRelativePath(config.Source) &&
		validGitCommit(config.Commit) &&
		validPromptCommit(config.PromptCommit) &&
		strings.HasPrefix(config.Commit, config.PromptCommit) &&
		validGitCommit(config.Base) &&
		config.Base != config.Commit &&
		(config.ModelMode == "router" || config.ModelMode == "pinned")
}

// ValidateLiveIdentity proves that a live or repair run used the repository
// identity declared by its suite manifest. Without this check, ambient
// LSP_TARGET_COMMIT or LSP_BASE_REF values can silently change the workload.
func ValidateLiveIdentity(
	runDir string,
	config LiveConfig,
	resolvedSource ...string,
) CheckResult {
	expectedSource := config.Source
	if len(resolvedSource) > 0 {
		expectedSource = resolvedSource[0]
	}
	result := CheckResult{
		Description: "live run matches manifest repository and routing identity",
		Expected: map[string]string{
			"source_repo":   expectedSource,
			"target_commit": config.Commit,
			"prompt_commit": config.PromptCommit,
			"base_commit":   config.Base,
			"model_mode":    config.ModelMode,
		},
	}
	data, err := os.ReadFile(filepath.Join(runDir, "manifest.json"))
	if err != nil {
		result.Error = err.Error()
		return result
	}
	var manifest struct {
		SourceRepo   string `json:"source_repo"`
		TargetCommit string `json:"target_commit"`
		PromptCommit string `json:"prompt_commit"`
		BaseCommit   string `json:"base_commit"`
		ModelMode    string `json:"model_mode"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		result.Error = err.Error()
		return result
	}
	result.Actual = map[string]string{
		"source_repo":   manifest.SourceRepo,
		"target_commit": manifest.TargetCommit,
		"prompt_commit": manifest.PromptCommit,
		"base_commit":   manifest.BaseCommit,
		"model_mode":    manifest.ModelMode,
	}
	if manifest.SourceRepo != expectedSource ||
		manifest.TargetCommit != config.Commit ||
		manifest.PromptCommit != config.PromptCommit ||
		manifest.BaseCommit != config.Base ||
		manifest.ModelMode != config.ModelMode {
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
	// Promotion relocates the files. The recorded path is never opened, so
	// resolve only its clean basename and open that allowed name inside runDir.
	path = filepath.Base(clean)
	for _, allowed := range sourceChecksumArtifacts {
		if path == allowed {
			return path, nil
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

type qualityAggregateSnapshot struct {
	manifestDigest string
	outputs        map[string][]byte
	inputs         map[string][]byte
	validation     qualityValidation
}

type qualityInputCommitment struct {
	SchemaVersion             int                        `json:"schema_version"`
	Validation                qualityValidation          `json:"validation"`
	Inputs                    map[string]string          `json:"inputs"`
	Snapshots                 map[string]string          `json:"snapshots"`
	Generators                map[string]string          `json:"generators"`
	AnalysisEnvironment       qualityAnalysisEnvironment `json:"analysis_environment"`
	JudgeEnvironmentSemantics []string                   `json:"judge_environment_semantics"`
}

type qualityValidation struct {
	StrictEvidence         bool   `json:"strict_evidence"`
	AggregateStatus        string `json:"aggregate_status"`
	Enforce                bool   `json:"enforce"`
	BindLegacyJudges       bool   `json:"bind_legacy_judges"`
	SkipAnalyze            bool   `json:"skip_analyze"`
	JudgeRepeats           int    `json:"judge_repeats"`
	MetricsSchemaVersion   int    `json:"metrics_schema_version"`
	MetricsFormula         string `json:"metrics_formula"`
	GenerationIsolation    string `json:"generation_isolation"`
	GenerationConfigSHA256 string `json:"generation_config_sha256"`
	JudgeCacheSchema       int    `json:"judge_cache_schema"`
}

type qualityAnalysisEnvironment struct {
	GoVersion string `json:"go_version"`
	GOENV     string `json:"GOENV"`
	GOWORK    string `json:"GOWORK"`
	GOFLAGS   string `json:"GOFLAGS"`
}

type qualityGenerationConfig struct {
	GenerationIsolation          string            `json:"generation_isolation"`
	DeveloperInstructions        string            `json:"baseline_developer_instructions"`
	FeatureFlags                 []string          `json:"feature_flags"`
	CodexIsolationFlags          []string          `json:"codex_isolation_flags"`
	CodexEnvironment             []string          `json:"codex_environment"`
	HostGoEnvironment            []string          `json:"host_go_environment"`
	ProfilesSnapshotPath         string            `json:"profiles_snapshot_path"`
	ProfilesSnapshotSHA256       string            `json:"profiles_snapshot_sha256"`
	PromptFiles                  map[string]string `json:"prompt_files"`
	PromptDigests                map[string]string `json:"prompt_digests"`
	MechanicalNavigationEnforced bool              `json:"mechanical_navigation_semantics_enforced"`
	MechanicalNavigationContract struct {
		RequiredRoot           string `json:"required_root"`
		RequiredBaseCommit     string `json:"required_base_commit"`
		RequiredChangedReturn  string `json:"required_changed_return"`
		RequiredChangedContext string `json:"required_changed_context"`
		RequireNavigation      string `json:"require_navigation_semantics"`
	} `json:"mechanical_navigation_contract"`
	AuthSourcePermission string `json:"auth_source_permission"`
}

func validateQualityAggregate(runDir string) error {
	_, err := readQualityAggregate(runDir, "")
	return err
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
		SchemaVersion int               `json:"schema_version"`
		Files         map[string]string `json:"files"`
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
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	opened, err := file.Stat()
	if err != nil {
		_ = file.Close()
		return nil, err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(before, opened) {
		_ = file.Close()
		return nil, fmt.Errorf("file identity changed while opening")
	}
	content, err := io.ReadAll(file)
	if err != nil {
		_ = file.Close()
		return nil, err
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
		!os.SameFile(opened, after) {
		_ = file.Close()
		return nil, fmt.Errorf("file identity changed while reading")
	}
	if err := file.Close(); err != nil {
		return nil, err
	}
	return content, nil
}

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
		commitment.Validation.JudgeCacheSchema != 6 ||
		commitment.Validation.JudgeRepeats < 0 {
		return fmt.Errorf("quality input commitment has incompatible validation semantics")
	}
	switch commitment.Validation.AggregateStatus {
	case "strict-current":
		if !commitment.Validation.StrictEvidence ||
			commitment.Validation.BindLegacyJudges {
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
			commitment.Validation.BindLegacyJudges {
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
		SchemaVersion      int    `json:"schema_version"`
		Formula            string `json:"formula"`
		AnalysisProvenance struct {
			ProfilesSource string `json:"profiles_source"`
			ProfilesPath   string `json:"profiles_path"`
			ProfilesSHA256 string `json:"profiles_sha256"`
		} `json:"analysis_provenance"`
		Cases []struct {
			Name                  string `json:"name"`
			AnswerFile            string `json:"answer_file"`
			CommandsFile          string `json:"commands_file"`
			ToolStatsFile         string `json:"tool_stats_file"`
			CallGraphDOTFile      string `json:"call_graph_dot_file"`
			CallGraphMarkdownFile string `json:"call_graph_markdown_file"`
		} `json:"cases"`
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
		if len(generationConfigFields) != 13 ||
			generationConfig.GenerationIsolation != qualityGenerationIsolation ||
			generationConfig.DeveloperInstructions != qualityNoCollaboration ||
			!reflect.DeepEqual(
				generationConfig.FeatureFlags,
				[]string{
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
				},
			) ||
			!containsAll(
				generationConfig.CodexIsolationFlags,
				"--ignore-user-config",
				"--ignore-rules",
			) ||
			!containsAll(
				generationConfig.CodexEnvironment,
				"env",
				"-i",
				"GOENV=off",
				"GOTOOLCHAIN=local",
				"GOWORK=off",
			) ||
			!containsAll(
				generationConfig.HostGoEnvironment,
				"env",
				"GOENV=off",
				"GOTOOLCHAIN=local",
				"GOWORK=off",
			) ||
			generationConfig.ProfilesSnapshotPath !=
				"profiles-snapshot.tsv" ||
			!validSHA256Digest(
				generationConfig.ProfilesSnapshotSHA256,
			) ||
			len(generationConfig.PromptFiles) == 0 ||
			len(generationConfig.PromptFiles) !=
				len(generationConfig.PromptDigests) ||
			!generationConfig.MechanicalNavigationEnforced ||
			generationConfig.MechanicalNavigationContract.RequiredRoot !=
				"<worktree>" ||
			generationConfig.MechanicalNavigationContract.RequiredBaseCommit !=
				"<resolved-base>" ||
			generationConfig.MechanicalNavigationContract.RequiredChangedReturn !=
				"<profile-return>" ||
			generationConfig.MechanicalNavigationContract.RequiredChangedContext !=
				"<profile-context>" ||
			generationConfig.MechanicalNavigationContract.RequireNavigation != "1" ||
			generationConfig.AuthSourcePermission != "deny-if-present" {
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
		runCompleteBytes, ok := snapshot.inputs["run-complete.json"]
		if !ok {
			return fmt.Errorf(
				"strict quality input commitment omits run-complete.json",
			)
		}
		var completion struct {
			SchemaVersion int    `json:"schema_version"`
			State         string `json:"state"`
			Outcome       string `json:"outcome"`
			ExitCode      int    `json:"exit_code"`
			CompletedAt   string `json:"completed_at"`
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
			SchemaVersion          int               `json:"schema_version"`
			TaskSelection          string            `json:"task_selection"`
			VariantSelection       string            `json:"variant_selection"`
			Profiles               []string          `json:"profiles"`
			BaselineFrom           *string           `json:"baseline_from"`
			GenerationIsolation    string            `json:"generation_isolation"`
			GenerationConfigSHA256 string            `json:"generation_config_sha256"`
			ProfilesSnapshotPath   string            `json:"profiles_snapshot_path"`
			ProfilesSnapshotSHA256 string            `json:"profiles_snapshot_sha256"`
			MechanicalNavigation   bool              `json:"mechanical_navigation_semantics_enforced"`
			PromptFiles            map[string]string `json:"prompt_files"`
			PromptDigests          map[string]string `json:"prompt_digests"`
			TargetCommit           string            `json:"target_commit"`
			PromptCommit           string            `json:"prompt_commit"`
			BaseCommit             string            `json:"base_commit"`
			BaseRef                string            `json:"base_ref"`
			Model                  string            `json:"model"`
			CodexVersion           string            `json:"codex_version"`
			GoVersion              string            `json:"go_version"`
		}
		if err := json.Unmarshal(manifestBytes, &manifest); err != nil {
			return fmt.Errorf("decode snapshotted manifest: %w", err)
		}
		if manifest.SchemaVersion != 1 {
			return fmt.Errorf("snapshotted manifest has incompatible schema")
		}
		if commitment.Validation.StrictEvidence {
			if !validGitCommit(manifest.TargetCommit) ||
				!validPromptCommit(manifest.PromptCommit) ||
				!strings.HasPrefix(
					manifest.TargetCommit,
					manifest.PromptCommit,
				) ||
				!validGitCommit(manifest.BaseCommit) ||
				manifest.BaseRef == "" ||
				manifest.Model == "" ||
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
				SchemaVersion          int               `json:"schema_version"`
				GenerationIsolation    string            `json:"generation_isolation"`
				GenerationConfigSHA256 string            `json:"generation_config_sha256"`
				ProfilesSnapshotPath   string            `json:"profiles_snapshot_path"`
				ProfilesSnapshotSHA256 string            `json:"profiles_snapshot_sha256"`
				MechanicalNavigation   bool              `json:"mechanical_navigation_semantics_enforced"`
				PromptFiles            map[string]string `json:"prompt_files"`
				PromptDigests          map[string]string `json:"prompt_digests"`
				TargetCommit           string            `json:"target_commit"`
				PromptCommit           string            `json:"prompt_commit"`
				BaseCommit             string            `json:"base_commit"`
				BaseRef                string            `json:"base_ref"`
				Model                  string            `json:"model"`
				CodexVersion           string            `json:"codex_version"`
				GoVersion              string            `json:"go_version"`
			}
			if err := json.Unmarshal(
				baselineManifestBytes,
				&baselineManifest,
			); err != nil {
				return fmt.Errorf("decode baseline source manifest: %w", err)
			}
			if baselineManifest.SchemaVersion != 1 ||
				baselineManifest.GenerationIsolation !=
					manifest.GenerationIsolation ||
				baselineManifest.GenerationConfigSHA256 !=
					manifest.GenerationConfigSHA256 ||
				baselineManifest.ProfilesSnapshotPath !=
					manifest.ProfilesSnapshotPath ||
				baselineManifest.ProfilesSnapshotSHA256 !=
					manifest.ProfilesSnapshotSHA256 ||
				baselineManifest.MechanicalNavigation !=
					manifest.MechanicalNavigation ||
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
				baselineManifest.CodexVersion != manifest.CodexVersion ||
				baselineManifest.GoVersion != manifest.GoVersion {
				return fmt.Errorf(
					"baseline source manifest disagrees with quality manifest",
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
				manifest.GenerationConfigSHA256 ||
				!bytes.Equal(baselineConfigBytes, generationConfigBytes) {
				return fmt.Errorf(
					"baseline source generation config disagrees with quality run",
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

func containsAll(values []string, required ...string) bool {
	seen := make(map[string]bool, len(values))
	for _, value := range values {
		seen[value] = true
	}
	for _, value := range required {
		if !seen[value] {
			return false
		}
	}
	return true
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
			Completed                        bool       `json:"completed"`
			RegularInputTokens               int64      `json:"regular_input_tokens"`
			CachedInputTokens                int64      `json:"cached_input_tokens"`
			OutputTokens                     int64      `json:"output_tokens"`
			EffectiveTokens                  float64    `json:"effective_tokens"`
			ToolCallCount                    int        `json:"tool_call_count"`
			RepoViewToolCallCount            int        `json:"repo_view_tool_call_count"`
			OtherToolCallCount               int        `json:"other_tool_call_count"`
			RepoViewInvocationCount          int        `json:"repo_view_invocation_count"`
			TemporalToolEdgeCount            int        `json:"temporal_tool_edge_count"`
			OutputReferenceEdgeCount         int        `json:"output_reference_edge_count"`
			RepoViewInvocationCap            int        `json:"repo_view_invocation_cap"`
			RepoViewInvocationCapExceeded    bool       `json:"repo_view_invocation_cap_exceeded"`
			RepoViewBoundViolationCount      int        `json:"repo_view_bound_violation_count"`
			RepoViewBudgetTamperCommandCount int        `json:"repo_view_budget_tamper_command_count"`
			RepoViewChangedInvocationCount   int        `json:"repo_view_changed_invocation_count"`
			RepoViewFirstInvocationChanged   bool       `json:"repo_view_first_invocation_changed"`
			RepoViewNavigationSemanticsValid bool       `json:"repo_view_navigation_semantics_valid"`
			MechanicalNavigationEnforced     bool       `json:"mechanical_navigation_semantics_enforced"`
			RepoViewFindInvocationCount      int        `json:"repo_view_find_invocation_count"`
			RepoViewInspectInvocationCount   int        `json:"repo_view_inspect_invocation_count"`
			RepoViewOutlineInvocationCount   int        `json:"repo_view_outline_invocation_count"`
			ToolTypes                        []ToolStat `json:"tool_types"`
			Operations                       []ToolStat `json:"operations"`
			ToolStatsFile                    string     `json:"tool_stats_file"`
			CallGraphDOTFile                 string     `json:"call_graph_dot_file"`
			CallGraphMarkdownFile            string     `json:"call_graph_markdown_file"`
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
			JudgeCount                    int      `json:"judge_count"`
			AllNotWorse                   bool     `json:"all_not_worse"`
			AllCoreConclusionMatch        bool     `json:"all_core_conclusion_match"`
			CriticalOmissions             []string `json:"critical_omissions"`
			UnsupportedClaims             []string `json:"unsupported_claims"`
			BaselineMaterialPointsOmitted []string `json:"baseline_material_points_omitted"`
			MaterialContradictions        []string `json:"material_contradictions"`
			AverageCorrectness            float64  `json:"average_correctness"`
			AverageCompleteness           float64  `json:"average_completeness"`
			AverageGrounding              float64  `json:"average_grounding"`
			AverageTaskAdherence          float64  `json:"average_task_adherence"`
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
		invocationCap, invocationCapOK := number(
			current["repo_view_invocation_cap"],
		)
		if !totalOK || !repoViewOK || !otherOK || !temporalOK ||
			!outputReferencesOK || !budgetAccountingPresent ||
			!commandShapePresent || !firstInvocationPresent ||
			!navigationSemanticsPresent || !mechanicalSemanticsPresent ||
			!changedInvocationsOK || !invocationCapOK {
			results = append(results, CheckResult{
				Description: description,
				Passed:      false,
				Error:       "missing numeric tool-accounting fields",
			})
			continue
		}

		maxTemporal := math.Max(total-1, 0)
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
							changedInvocations == 1))))
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
