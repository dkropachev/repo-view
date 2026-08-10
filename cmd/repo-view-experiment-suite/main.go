package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/dkropachev/repo-view/internal/experimentsuite"
)

type options struct {
	repoRoot          string
	manifestPath      string
	resolutionPath    string
	evidenceRoot      string
	caseIDs           string
	outputDir         string
	repairAttempt     string
	maxLevel          int
	judgeRepeats      int
	skipAnalyze       bool
	skipQuality       bool
	allowMissing      bool
	preparationBudget *evidencePreparationBudget
}

type caseResult struct {
	FixtureQualityProvenance string                           `json:"fixture_quality_provenance,omitempty"`
	QualityProvenance        string                           `json:"quality_provenance,omitempty"`
	Complexity               string                           `json:"complexity"`
	Outcome                  string                           `json:"expected_outcome"`
	CurrentStatus            string                           `json:"current_status,omitempty"`
	RootCause                string                           `json:"root_cause,omitempty"`
	Fix                      string                           `json:"fix,omitempty"`
	FixtureEvidence          string                           `json:"fixture_evidence,omitempty"`
	Error                    string                           `json:"error,omitempty"`
	Evidence                 string                           `json:"evidence"`
	ID                       string                           `json:"id"`
	FixtureMetrics           []experimentsuite.EvidenceMetric `json:"fixture_metrics,omitempty"`
	Checks                   []experimentsuite.CheckResult    `json:"checks,omitempty"`
	Metrics                  []experimentsuite.EvidenceMetric `json:"metrics,omitempty"`
	Level                    int                              `json:"level"`
	Passed                   bool                             `json:"passed"`
	Skipped                  bool                             `json:"skipped"`
}

type suiteResult struct {
	Command                  string       `json:"command"`
	EvidenceAnalysis         string       `json:"evidence_analysis,omitempty"`
	QualityAggregation       string       `json:"quality_aggregation,omitempty"`
	StartedAt                string       `json:"started_at"`
	FinishedAt               string       `json:"finished_at"`
	Manifest                 string       `json:"manifest"`
	ManifestSHA256           string       `json:"manifest_sha256"`
	ResolutionManifest       string       `json:"resolution_manifest,omitempty"`
	ResolutionManifestSHA256 string       `json:"resolution_manifest_sha256,omitempty"`
	Cases                    []caseResult `json:"cases"`
	SchemaVersion            int          `json:"schema_version"`
	Passed                   bool         `json:"passed"`
}

const (
	evidenceStageExecuted = "executed"
	evidenceStageReused   = "reused"
	evidenceStageNotRun   = "not run"
)

type evidencePreparationOutcome struct {
	err         error
	evidenceDir string
	workspace   *evidencePreparationWorkspace
	analysis    string
	quality     string
}

type evidencePreparationKey struct {
	runDir            string
	qualityProvenance string
	qualityDigest     string
	judgeModelMode    string
	judgeModel        string
	judgeRepeats      int
	skipAnalyze       bool
	skipQuality       bool
	enforce           bool
}

type strictQualityReplayConfig struct {
	qualityDigest  string
	judgeModelMode string
	judgeModel     string
	judgeRepeats   int
	enforce        bool
}

type evidencePreparationTracker struct {
	outcomes map[string]evidencePreparationOutcome
	sources  map[string]evidenceTreeSnapshot
}

func (tracker *evidencePreparationTracker) record(
	runDir string,
	outcome evidencePreparationOutcome,
) {
	if tracker.outcomes == nil {
		tracker.outcomes = make(map[string]evidencePreparationOutcome)
	}
	if _, exists := tracker.outcomes[runDir]; !exists {
		tracker.outcomes[runDir] = outcome
	}
}

func (tracker *evidencePreparationTracker) modes() (string, string) {
	analysis := make(map[string]bool)
	quality := make(map[string]bool)
	for _, outcome := range tracker.outcomes {
		analysis[outcome.analysis] = true
		quality[outcome.quality] = true
	}
	return aggregateEvidenceStageMode(analysis), aggregateEvidenceStageMode(quality)
}

func (tracker *evidencePreparationTracker) recordSource(
	workspace *evidencePreparationWorkspace,
) error {
	if workspace == nil {
		return nil
	}
	return tracker.recordSourceSnapshot(workspace.source, workspace.sourceSnapshot)
}

func (tracker *evidencePreparationTracker) recordSourceSnapshot(
	source string,
	snapshot evidenceTreeSnapshot,
) error {
	if tracker.sources == nil {
		tracker.sources = make(map[string]evidenceTreeSnapshot)
	}
	if existing, ok := tracker.sources[source]; ok {
		if !evidenceSnapshotsEqual(existing, snapshot) {
			return fmt.Errorf(
				"canonical evidence changed between staging snapshots: %s",
				source,
			)
		}
		return nil
	}
	tracker.sources[source] = snapshot
	return nil
}

func (tracker *evidencePreparationTracker) verifySources() error {
	paths := make([]string, 0, len(tracker.sources))
	for path := range tracker.sources {
		paths = append(paths, path)
	}
	sort.Strings(paths)
	var verifyErr error
	for _, path := range paths {
		current, err := snapshotEvidenceTree(path)
		if err != nil {
			verifyErr = appendPreparationError(
				verifyErr,
				fmt.Errorf("recheck canonical evidence %s: %w", path, err),
			)
			continue
		}
		if !evidenceSnapshotsEqual(tracker.sources[path], current) {
			verifyErr = appendPreparationError(
				verifyErr,
				fmt.Errorf("canonical evidence changed before publication: %s", path),
			)
		}
	}
	return verifyErr
}

func aggregateEvidenceStageMode(modes map[string]bool) string {
	if len(modes) == 0 {
		return evidenceStageNotRun
	}
	if len(modes) > 1 {
		return "mixed"
	}
	for mode := range modes {
		return mode
	}
	return evidenceStageNotRun
}

func main() {
	os.Exit(run(context.Background(), os.Args[1:]))
}

func run(ctx context.Context, args []string) int {
	if len(args) == 0 {
		usage()
		return 2
	}
	if args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		usage()
		return 0
	}
	command, compatibilityAlias := normalizeCommand(args[0])
	if compatibilityAlias {
		fmt.Fprintln(os.Stderr, "warning: command \"reply\" is a compatibility alias for \"replay\"")
	}
	switch command {
	case "list", "replay", "resolve", "live", "repair":
	default:
		fmt.Fprintf(os.Stderr, "unknown command: %s\n", command)
		usage()
		return 2
	}

	flags := flag.NewFlagSet(command, flag.ContinueOnError)
	flags.SetOutput(os.Stderr)
	opts := options{}
	flags.StringVar(&opts.repoRoot, "repo-root", ".", "repo-view repository root")
	flags.StringVar(&opts.manifestPath, "manifest", "experiments/lsp-replacement/suite/cases.json", "suite manifest")
	if command == "resolve" || command == "repair" {
		flags.StringVar(
			&opts.resolutionPath,
			"resolutions",
			"experiments/lsp-replacement/suite/resolutions.json",
			"failed-case resolution manifest",
		)
	}
	flags.StringVar(&opts.evidenceRoot, "evidence-root", "experiments/lsp-replacement/evidence", "local evidence root")
	flags.StringVar(&opts.caseIDs, "case", "", "comma-separated case IDs")
	flags.IntVar(&opts.maxLevel, "max-level", 0, "run cases through this complexity level; 0 means all")
	flags.StringVar(&opts.outputDir, "output", "", "suite result directory")
	if command == "replay" || command == "resolve" {
		flags.BoolVar(&opts.skipAnalyze, "skip-analyze", false, "reuse existing generated metrics")
		flags.BoolVar(&opts.skipQuality, "skip-quality", false, "reuse existing deterministic quality results")
		flags.BoolVar(&opts.allowMissing, "allow-missing", false, "skip missing local evidence")
	}
	if command == "live" || command == "repair" {
		flags.IntVar(&opts.judgeRepeats, "judge-repeats", 2, "source-grounded judges per task")
	}
	if command == "repair" {
		flags.StringVar(
			&opts.repairAttempt,
			"attempt",
			"",
			"existing repair attempt to re-audit instead of generating a new run",
		)
	}
	if err := flags.Parse(args[1:]); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintf(os.Stderr, "unexpected argument: %s\n", flags.Arg(0))
		return 2
	}

	absoluteRoot, err := filepath.Abs(opts.repoRoot)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	opts.repoRoot = absoluteRoot
	opts.manifestPath = resolve(opts.repoRoot, opts.manifestPath)
	if opts.resolutionPath != "" {
		opts.resolutionPath = resolve(opts.repoRoot, opts.resolutionPath)
	}
	opts.evidenceRoot = resolve(opts.repoRoot, opts.evidenceRoot)
	if opts.repairAttempt != "" {
		opts.repairAttempt = resolve(opts.repoRoot, opts.repairAttempt)
	}
	manifest, manifestSHA256, err := experimentsuite.LoadManifestSnapshot(
		opts.manifestPath,
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	requested := parseCaseIDs(opts.caseIDs)
	selected, err := experimentsuite.SelectCases(manifest, requested, opts.maxLevel)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if command == "live" || command == "repair" {
		if opts.judgeRepeats < 1 {
			fmt.Fprintln(os.Stderr, "--judge-repeats must be at least 1 for quality confirmation")
			return 2
		}
	}
	if command == "live" {
		if len(requested) == 0 {
			var liveCases []experimentsuite.Case
			for _, testCase := range selected {
				if testCase.Live != nil {
					liveCases = append(liveCases, testCase)
				}
			}
			selected = liveCases
			if len(selected) == 0 {
				fmt.Fprintln(os.Stderr, "no live-enabled suite cases selected")
				return 2
			}
		}
	}
	if command == "repair" {
		var repairCases []experimentsuite.Case
		for _, testCase := range selected {
			if testCase.Outcome == "rejected" {
				repairCases = append(repairCases, testCase)
				continue
			}
			if len(requested) > 0 {
				fmt.Fprintf(
					os.Stderr,
					"accepted case %s is not a failed-case repair target\n",
					testCase.ID,
				)
				return 2
			}
		}
		selected = repairCases
		if len(selected) == 0 {
			fmt.Fprintln(os.Stderr, "no failed cases selected for repair")
			return 2
		}
		if opts.repairAttempt != "" && len(selected) != 1 {
			fmt.Fprintln(os.Stderr, "--attempt requires exactly one selected failed case")
			return 2
		}
	}

	if command == "list" {
		printCases(selected)
		return 0
	}
	var selectedResolutions []experimentsuite.ResolutionCase
	var resolutionManifestSHA256 string
	if command == "resolve" || command == "repair" {
		resolutionManifest, digest, err :=
			experimentsuite.LoadResolutionManifestSnapshot(opts.resolutionPath)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		resolutionManifestSHA256 = digest
		selectedResolutions, err = experimentsuite.SelectResolutions(
			resolutionManifest,
			selected,
		)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}
	if opts.outputDir == "" {
		opts.outputDir = filepath.Join(
			opts.evidenceRoot,
			"suites",
			time.Now().UTC().Format("20060102T150405.000000000Z")+"-"+command,
		)
	} else {
		opts.outputDir = resolve(opts.repoRoot, opts.outputDir)
	}
	if command == "replay" || command == "resolve" {
		if err := rejectEvidenceOutputOverlap(
			opts.outputDir,
			opts.evidenceRoot,
			selected,
			selectedResolutions,
			command,
		); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
	}
	if err := prepareOutputDir(opts.outputDir); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	started := time.Now().UTC()
	var results []caseResult
	var preparation evidencePreparationTracker
	switch command {
	case "replay":
		results = replay(ctx, opts, selected, &preparation)
	case "resolve":
		results = resolveCases(
			ctx,
			opts,
			selected,
			selectedResolutions,
			&preparation,
		)
	case "repair":
		results = repairCases(ctx, opts, selected, selectedResolutions)
	default:
		results = live(ctx, opts, selected)
	}
	finished := time.Now().UTC()
	result := suiteResult{
		SchemaVersion:  1,
		Command:        command,
		StartedAt:      started.Format(time.RFC3339Nano),
		FinishedAt:     finished.Format(time.RFC3339Nano),
		Manifest:       opts.manifestPath,
		ManifestSHA256: manifestSHA256,
		Cases:          results,
		Passed:         allPassed(results),
	}
	if command == "replay" || command == "resolve" {
		result.EvidenceAnalysis, result.QualityAggregation = preparation.modes()
	}
	if command == "resolve" || command == "repair" {
		result.ResolutionManifest = opts.resolutionPath
		result.ResolutionManifestSHA256 = resolutionManifestSHA256
	}
	if command == "replay" || command == "resolve" {
		if err := preparation.verifySources(); err != nil {
			result.Cases = failPreparedEvidenceResults(
				result.Cases,
				fmt.Errorf(
					"verify canonical evidence immediately before publication: %w",
					err,
				),
			)
			result.Passed = false
		}
	}
	if err := writeResults(opts.outputDir, result); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	fmt.Printf("suite summary: %s\n", filepath.Join(opts.outputDir, "summary.md"))
	if !result.Passed {
		return 1
	}
	return 0
}

func normalizeCommand(command string) (string, bool) {
	if command == "reply" {
		return "replay", true
	}
	return command, false
}

func rejectEvidenceOutputOverlap(
	outputDir string,
	evidenceRoot string,
	cases []experimentsuite.Case,
	resolutions []experimentsuite.ResolutionCase,
	command string,
) error {
	evidenceDirs := make(map[string]bool)
	for _, testCase := range cases {
		evidenceDirs[filepath.Join(
			evidenceRoot,
			filepath.FromSlash(testCase.Evidence),
		)] = true
	}
	if command == "resolve" {
		for _, resolution := range resolutions {
			evidenceDirs[filepath.Join(
				evidenceRoot,
				filepath.FromSlash(resolution.Evidence),
			)] = true
		}
	}
	for evidenceDir := range evidenceDirs {
		relative, err := filepath.Rel(evidenceDir, outputDir)
		if err != nil {
			return fmt.Errorf(
				"compare output and selected evidence directories: %w",
				err,
			)
		}
		if relative == "." ||
			(!filepath.IsAbs(relative) && relative != ".." &&
				!strings.HasPrefix(relative, ".."+string(filepath.Separator))) {
			return fmt.Errorf(
				"output directory must not equal or be inside selected evidence: "+
					"output=%s evidence=%s",
				outputDir,
				evidenceDir,
			)
		}
		if err := rejectPhysicalEvidenceOutputOverlap(outputDir, evidenceDir); err != nil {
			return err
		}
	}
	return nil
}

func rejectPhysicalEvidenceOutputOverlap(outputDir, evidenceDir string) error {
	evidenceInfo, err := os.Stat(evidenceDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("inspect selected evidence directory %s: %w", evidenceDir, err)
	}
	if !evidenceInfo.IsDir() {
		return nil
	}
	for ancestor := filepath.Dir(outputDir); ; ancestor = filepath.Dir(ancestor) {
		ancestorInfo, err := os.Stat(ancestor)
		if err != nil {
			if !os.IsNotExist(err) {
				return fmt.Errorf("inspect output ancestor %s: %w", ancestor, err)
			}
		} else if ancestorInfo.IsDir() && os.SameFile(evidenceInfo, ancestorInfo) {
			return fmt.Errorf(
				"output directory has a physical ancestor equal to selected evidence: "+
					"output=%s ancestor=%s evidence=%s",
				outputDir,
				ancestor,
				evidenceDir,
			)
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			break
		}
	}
	return nil
}

func replay(
	ctx context.Context,
	opts options,
	cases []experimentsuite.Case,
	tracker *evidencePreparationTracker,
) []caseResult {
	if opts.preparationBudget == nil {
		opts.preparationBudget = newEvidencePreparationBudget()
	}
	prepared := make(map[evidencePreparationKey]evidencePreparationOutcome)
	var results []caseResult
	for _, testCase := range cases {
		runDir := filepath.Join(opts.evidenceRoot, filepath.FromSlash(testCase.Evidence))
		result := caseResult{
			ID:                testCase.ID,
			Level:             testCase.Level,
			Complexity:        testCase.Complexity,
			Outcome:           testCase.Outcome,
			QualityProvenance: testCase.QualityProvenance,
			Evidence:          runDir,
		}
		if err := ensureRealDirectoryPath(runDir); err != nil {
			tracker.record(runDir, evidencePreparationOutcome{
				analysis: evidenceStageNotRun,
				quality:  evidenceStageNotRun,
				err:      err,
			})
			if opts.allowMissing && os.IsNotExist(err) {
				result.Passed = true
				result.Skipped = true
				result.Error = err.Error()
			} else {
				result.Error = err.Error()
			}
			results = append(results, result)
			continue
		}
		preparedRunDir, err := prepareEvidenceRun(
			ctx,
			opts,
			runDir,
			testCase.QualityProvenance,
			testCase.QualityAggregateSHA256,
			prepared,
			tracker,
		)
		if err != nil {
			result.Error = err.Error()
			results = append(results, result)
			continue
		}
		result.Checks = experimentsuite.ValidateTrackedEvidenceProvenance(
			preparedRunDir,
			testCase.Assertions,
			testCase.SourceChecksumSHA256,
			testCase.QualityAggregateSHA256,
			testCase.QualityProvenance,
		)
		result.Passed = experimentsuite.Passed(result.Checks)
		results = append(results, result)
		printCaseResult(result)
	}
	return finalizePreparedEvidence(results, prepared)
}

type goTestOutcome struct {
	err    error
	log    string
	passed bool
}

type goTestEvent struct {
	Action string `json:"Action"`
	Test   string `json:"Test"`
}

func resolveCases(
	ctx context.Context,
	opts options,
	cases []experimentsuite.Case,
	resolutions []experimentsuite.ResolutionCase,
	tracker *evidencePreparationTracker,
) []caseResult {
	if opts.preparationBudget == nil {
		opts.preparationBudget = newEvidencePreparationBudget()
	}
	resolutionByID := make(map[string]experimentsuite.ResolutionCase, len(resolutions))
	for _, resolution := range resolutions {
		resolutionByID[resolution.ID] = resolution
	}
	prepared := make(map[evidencePreparationKey]evidencePreparationOutcome)
	testOutcomes := make(map[string]goTestOutcome)
	var results []caseResult
	for _, testCase := range cases {
		resolution := resolutionByID[testCase.ID]
		runDir := filepath.Join(opts.evidenceRoot, filepath.FromSlash(resolution.Evidence))
		fixtureRunDir := filepath.Join(
			opts.evidenceRoot,
			filepath.FromSlash(testCase.Evidence),
		)
		result := caseResult{
			ID:                       testCase.ID,
			Level:                    testCase.Level,
			Complexity:               testCase.Complexity,
			Outcome:                  testCase.Outcome,
			RootCause:                resolution.RootCause,
			Fix:                      resolution.Fix,
			FixtureEvidence:          fixtureRunDir,
			FixtureQualityProvenance: testCase.QualityProvenance,
			Evidence:                 runDir,
			QualityProvenance:        resolution.QualityProvenance,
		}
		preparedEvidenceDirs := []string{fixtureRunDir, runDir}
		for index, evidence := range []struct {
			dir        string
			provenance string
			digest     string
		}{
			{
				fixtureRunDir,
				testCase.QualityProvenance,
				testCase.QualityAggregateSHA256,
			},
			{
				runDir,
				resolution.QualityProvenance,
				resolution.QualityAggregateSHA256,
			},
		} {
			evidenceDir := evidence.dir
			if err := ensureRealDirectoryPath(evidenceDir); err != nil {
				tracker.record(evidenceDir, evidencePreparationOutcome{
					analysis: evidenceStageNotRun,
					quality:  evidenceStageNotRun,
					err:      err,
				})
				if opts.allowMissing && os.IsNotExist(err) {
					result.Passed = true
					result.Skipped = true
					result.Error = err.Error()
				} else {
					result.Error = err.Error()
				}
				break
			}
			preparedRunDir, err := prepareEvidenceRun(
				ctx,
				opts,
				evidenceDir,
				evidence.provenance,
				evidence.digest,
				prepared,
				tracker,
			)
			if err != nil {
				result.Error = err.Error()
				break
			}
			preparedEvidenceDirs[index] = preparedRunDir
		}
		if result.Error != "" {
			result.CurrentStatus = resolutionStatus(
				resolution.Status,
				result.Passed,
				result.Skipped,
			)
			results = append(results, result)
			continue
		}

		for _, goTest := range resolution.GoTests {
			key := goTest.Package + "\x00" + goTest.Run
			outcome, ok := testOutcomes[key]
			if !ok {
				outcome = runGoTest(ctx, opts, goTest, key)
				testOutcomes[key] = outcome
			}
			check := experimentsuite.CheckResult{
				Description: "Go regression: " + goTest.Description,
				Passed:      outcome.passed,
				Actual:      "pass",
				Expected:    "pass",
			}
			if !outcome.passed {
				check.Actual = "fail"
				check.Error = fmt.Sprintf("%v; log=%s", outcome.err, outcome.log)
			}
			result.Checks = append(result.Checks, check)
		}
		result.Checks = append(
			result.Checks,
			prefixChecks(
				"Fixture evidence: ",
				experimentsuite.ValidateTrackedEvidenceProvenance(
					preparedEvidenceDirs[0],
					testCase.Assertions,
					testCase.SourceChecksumSHA256,
					testCase.QualityAggregateSHA256,
					testCase.QualityProvenance,
				),
			)...,
		)
		result.Checks = append(
			result.Checks,
			prefixChecks(
				"Current resolution: ",
				experimentsuite.ValidateTrackedEvidenceProvenance(
					preparedEvidenceDirs[1],
					resolution.Assertions,
					resolution.SourceChecksumSHA256,
					resolution.QualityAggregateSHA256,
					resolution.QualityProvenance,
				),
			)...,
		)
		fixtureMetrics, fixtureMetricErr :=
			experimentsuite.SummarizeTrackedEvidenceProvenance(
				preparedEvidenceDirs[0],
				resolution.FixtureMetricCases,
				testCase.QualityAggregateSHA256,
				testCase.QualityProvenance,
			)
		result.FixtureMetrics = fixtureMetrics
		if fixtureMetricErr != nil {
			result.Error = "fixture metrics: " + fixtureMetricErr.Error()
		}
		metrics, metricErr := experimentsuite.SummarizeTrackedEvidenceProvenance(
			preparedEvidenceDirs[1],
			resolution.MetricCases,
			resolution.QualityAggregateSHA256,
			resolution.QualityProvenance,
		)
		result.Metrics = metrics
		if metricErr != nil {
			if result.Error != "" {
				result.Error += "; "
			}
			result.Error += "current metrics: " + metricErr.Error()
		} else if testCase.Outcome == "rejected" {
			result.Checks = append(
				result.Checks,
				prefixChecks(
					"Current promotion: ",
					experimentsuite.ValidatePromotion(metrics, 2),
				)...,
			)
		}
		result.Passed = result.Error == "" && experimentsuite.Passed(result.Checks)
		result.CurrentStatus = resolutionStatus(resolution.Status, result.Passed, false)
		results = append(results, result)
		printCaseResult(result)
	}
	return finalizePreparedEvidence(results, prepared)
}

func prepareEvidence(
	ctx context.Context,
	opts options,
	runDir string,
	qualityProvenance string,
	qualityDigest string,
	prepared map[evidencePreparationKey]evidencePreparationOutcome,
	tracker *evidencePreparationTracker,
) error {
	_, err := prepareEvidenceRun(
		ctx,
		opts,
		runDir,
		qualityProvenance,
		qualityDigest,
		prepared,
		tracker,
	)
	return err
}

func prepareEvidenceRun(
	ctx context.Context,
	opts options,
	runDir string,
	qualityProvenance string,
	qualityDigest string,
	prepared map[evidencePreparationKey]evidencePreparationOutcome,
	tracker *evidencePreparationTracker,
) (string, error) {
	switch qualityProvenance {
	case "strict-current", "legacy-unisolated-attested", "non-strict":
	default:
		return "", fmt.Errorf("unsupported quality provenance %q", qualityProvenance)
	}
	if err := ensureRealDirectoryPath(runDir); err != nil {
		return "", fmt.Errorf("unsafe evidence directory: %w", err)
	}
	canonicalSnapshot, err := snapshotEvidenceTree(runDir)
	if err != nil {
		return "", fmt.Errorf("bounded canonical evidence preflight: %w", err)
	}
	if err := tracker.recordSourceSnapshot(runDir, canonicalSnapshot); err != nil {
		return "", err
	}
	verifiedConfig, err := experimentsuite.ReadQualityReplayConfig(
		runDir,
		qualityDigest,
		qualityProvenance,
	)
	if err != nil {
		return "", fmt.Errorf(
			"verify immutable tracked quality aggregate before staging "+
				"(replay/resolve will not repair canonical evidence): %w",
			err,
		)
	}
	strictConfig := strictQualityReplayConfig{
		qualityDigest:  verifiedConfig.AggregateSHA256,
		judgeRepeats:   verifiedConfig.JudgeRepeats,
		judgeModelMode: verifiedConfig.JudgeModelMode,
		judgeModel:     verifiedConfig.JudgeModel,
		enforce:        verifiedConfig.Enforce,
	}
	preparationKey := evidencePreparationKey{
		runDir:            runDir,
		qualityProvenance: qualityProvenance,
		qualityDigest:     strictConfig.qualityDigest,
		skipAnalyze:       opts.skipAnalyze,
		skipQuality:       opts.skipQuality,
		judgeRepeats:      strictConfig.judgeRepeats,
		judgeModelMode:    strictConfig.judgeModelMode,
		judgeModel:        strictConfig.judgeModel,
		enforce:           strictConfig.enforce,
	}
	if outcome, done := prepared[preparationKey]; done {
		return outcome.evidenceDir, outcome.err
	}
	outcome := evidencePreparationOutcome{
		evidenceDir: runDir,
		analysis:    evidenceStageNotRun,
		quality:     evidenceStageNotRun,
	}
	var workspace *evidencePreparationWorkspace
	if !opts.skipAnalyze || !opts.skipQuality {
		if opts.preparationBudget == nil {
			opts.preparationBudget = newEvidencePreparationBudget()
		}
		workspace, outcome.err = stageEvidenceForPreparation(
			opts.outputDir,
			runDir,
			opts.preparationBudget,
		)
		if outcome.err != nil {
			outcome.err = fmt.Errorf("stage evidence for preparation: %w", outcome.err)
		} else {
			outcome.evidenceDir = workspace.runDir
			outcome.workspace = workspace
			if err := tracker.recordSource(workspace); err != nil {
				outcome.err = err
			}
		}
	}
	workingRunDir := outcome.evidenceDir
	if !opts.skipAnalyze {
		if err := ensureRealDirectoryPath(workingRunDir); err != nil {
			outcome.err = fmt.Errorf("unsafe evidence directory before analysis: %w", err)
		}
	}
	if outcome.err == nil && !opts.skipAnalyze {
		outcome.analysis = evidenceStageExecuted
		outcome.err = runCommand(
			ctx,
			opts.repoRoot,
			filepath.Join(opts.repoRoot, "experiments/lsp-replacement/analyze.sh"),
			workingRunDir,
		)
	} else if outcome.err == nil && opts.skipAnalyze {
		if _, err := os.Stat(filepath.Join(workingRunDir, "metrics.json")); err != nil {
			outcome.err = fmt.Errorf("reuse evidence analysis: %w", err)
		} else {
			outcome.analysis = evidenceStageReused
		}
	}
	if outcome.err == nil && workspace != nil && !opts.skipAnalyze {
		analyzedSnapshot, err := snapshotEvidenceTree(workingRunDir)
		if err != nil {
			outcome.err = fmt.Errorf("validate analyzed evidence bounds: %w", err)
		} else if err := workspace.resizeReservation(analyzedSnapshot); err != nil {
			outcome.err = fmt.Errorf(
				"reserve analyzed evidence against suite budget: %w",
				err,
			)
		}
	}
	if outcome.err == nil && !opts.skipQuality {
		if err := ensureRealDirectoryPath(workingRunDir); err != nil {
			outcome.err = fmt.Errorf("unsafe evidence directory before quality aggregation: %w", err)
		}
		qualityArgs := []string{workingRunDir}
		var qualityEnvironment []string
		if outcome.err == nil && qualityProvenance == "strict-current" {
			qualityArgs = append(
				qualityArgs,
				"--judge-repeats", strconv.Itoa(strictConfig.judgeRepeats),
				"--model-mode", strictConfig.judgeModelMode,
			)
			if strictConfig.judgeRepeats > 0 {
				qualityArgs = append(qualityArgs, "--reuse-judges-only")
			}
			if strictConfig.enforce {
				qualityArgs = append(qualityArgs, "--enforce")
			}
			qualityEnvironment = strictQualityReplayEnvironment(strictConfig)
		} else if outcome.err == nil && qualityProvenance == "legacy-unisolated-attested" {
			qualityArgs = append(qualityArgs, "--bind-legacy-judges")
		}
		if opts.skipAnalyze {
			qualityArgs = append(qualityArgs, "--skip-analyze")
		}
		if outcome.err == nil {
			outcome.quality = evidenceStageExecuted
			outcome.err = runCommandWithStdoutEnvironment(
				ctx,
				opts.repoRoot,
				io.Discard,
				qualityEnvironment,
				filepath.Join(opts.repoRoot, "experiments/lsp-replacement/quality-check.sh"),
				qualityArgs...,
			)
		}
	} else if outcome.err == nil {
		if _, err := os.Stat(
			filepath.Join(workingRunDir, "quality", "quality.json"),
		); err != nil {
			outcome.err = fmt.Errorf("reuse quality aggregation: %w", err)
		} else {
			outcome.quality = evidenceStageReused
		}
	}
	if workspace != nil {
		if err := workspace.verifySourceUnchanged(); err != nil {
			outcome.err = appendPreparationError(outcome.err, err)
		}
		var regeneratedSnapshot evidenceTreeSnapshot
		if outcome.err == nil {
			regeneratedSnapshot, err = snapshotEvidenceTree(workingRunDir)
			if err != nil {
				outcome.err = fmt.Errorf(
					"validate regenerated evidence bounds: %w",
					err,
				)
			}
		}
		if outcome.err == nil {
			if err := workspace.resizeReservation(regeneratedSnapshot); err != nil {
				outcome.err = fmt.Errorf(
					"reserve regenerated evidence against suite budget: %w",
					err,
				)
			}
		}
		if outcome.err == nil {
			regeneratedConfig, err := experimentsuite.ReadQualityReplayConfig(
				workingRunDir,
				strictConfig.qualityDigest,
				qualityProvenance,
			)
			if err != nil {
				outcome.err = fmt.Errorf(
					"verify regenerated quality aggregate in staging: %w",
					err,
				)
			} else if regeneratedConfig != verifiedConfig {
				outcome.err = fmt.Errorf(
					"regenerated quality replay configuration changed in staging",
				)
			}
		}
		if outcome.err != nil {
			if err := cleanupEvidencePreparationWorkspace(workspace); err != nil {
				workspace.budget.poison(err)
				outcome.err = appendPreparationError(
					outcome.err,
					fmt.Errorf("clean failed staged evidence: %w", err),
				)
			} else {
				outcome.workspace = nil
			}
		}
	}
	prepared[preparationKey] = outcome
	tracker.record(runDir, outcome)
	return outcome.evidenceDir, outcome.err
}

const (
	maximumEvidencePreparationEntries    = 100_000
	maximumEvidencePreparationDepth      = 64
	maximumEvidencePreparationPathBytes  = 4_096
	maximumEvidencePreparationFileBytes  = int64(512 << 20)
	maximumEvidencePreparationTreeBytes  = int64(2 << 30)
	maximumEvidencePreparationStages     = 128
	maximumEvidencePreparationAllEntries = 250_000
	maximumEvidencePreparationAllBytes   = int64(4 << 30)
	evidencePreparationStagePrefix       = ".repo-view-evidence-preparation-"
)

type evidenceTreeEntry struct {
	Path   string `json:"path"`
	Kind   string `json:"kind"`
	SHA256 string `json:"sha256,omitempty"`
	Size   int64  `json:"size"`
	Mode   uint32 `json:"mode"`
}

type evidenceTreeSnapshot struct {
	canonical  []byte
	entries    []evidenceTreeEntry
	totalBytes int64
}

type evidencePreparationReservation struct {
	entries int
	bytes   int64
	active  bool
}

type evidencePreparationBudget struct {
	mu             sync.Mutex
	maximumStages  int
	maximumEntries int
	maximumBytes   int64
	stages         int
	entries        int
	bytes          int64
	poisoned       error
}

func newEvidencePreparationBudget() *evidencePreparationBudget {
	return newEvidencePreparationBudgetWithLimits(
		maximumEvidencePreparationStages,
		maximumEvidencePreparationAllEntries,
		maximumEvidencePreparationAllBytes,
	)
}

func newEvidencePreparationBudgetWithLimits(
	maximumStages int,
	maximumEntries int,
	maximumBytes int64,
) *evidencePreparationBudget {
	return &evidencePreparationBudget{
		maximumStages:  maximumStages,
		maximumEntries: maximumEntries,
		maximumBytes:   maximumBytes,
	}
}

func (budget *evidencePreparationBudget) reserve(
	snapshot evidenceTreeSnapshot,
) (*evidencePreparationReservation, error) {
	if budget == nil {
		return nil, fmt.Errorf("evidence staging budget is unavailable")
	}
	budget.mu.Lock()
	defer budget.mu.Unlock()
	if budget.poisoned != nil {
		return nil, fmt.Errorf(
			"evidence staging budget is unavailable after cleanup failure: %w",
			budget.poisoned,
		)
	}
	if budget.maximumStages < 1 || budget.maximumEntries < 1 ||
		budget.maximumBytes < 1 {
		return nil, fmt.Errorf("evidence staging budget has invalid limits")
	}
	neededEntries := len(snapshot.entries)
	if budget.stages >= budget.maximumStages {
		return nil, fmt.Errorf(
			"evidence staging budget exceeds %d stages",
			budget.maximumStages,
		)
	}
	if neededEntries > budget.maximumEntries-budget.entries {
		return nil, fmt.Errorf(
			"evidence staging budget exceeds %d total entries",
			budget.maximumEntries,
		)
	}
	if snapshot.totalBytes > budget.maximumBytes-budget.bytes {
		return nil, fmt.Errorf(
			"evidence staging budget exceeds %d total bytes",
			budget.maximumBytes,
		)
	}
	budget.stages++
	budget.entries += neededEntries
	budget.bytes += snapshot.totalBytes
	return &evidencePreparationReservation{
		entries: neededEntries,
		bytes:   snapshot.totalBytes,
		active:  true,
	}, nil
}

func (budget *evidencePreparationBudget) release(
	reservation *evidencePreparationReservation,
) error {
	if reservation == nil {
		return nil
	}
	if budget == nil {
		return fmt.Errorf("evidence staging budget is unavailable during cleanup")
	}
	budget.mu.Lock()
	defer budget.mu.Unlock()
	if !reservation.active {
		return nil
	}
	if budget.stages < 1 || budget.entries < reservation.entries ||
		budget.bytes < reservation.bytes {
		return fmt.Errorf("evidence staging budget accounting underflow")
	}
	budget.stages--
	budget.entries -= reservation.entries
	budget.bytes -= reservation.bytes
	reservation.active = false
	return nil
}

func (budget *evidencePreparationBudget) resize(
	reservation *evidencePreparationReservation,
	snapshot evidenceTreeSnapshot,
) error {
	if reservation == nil {
		return fmt.Errorf("evidence staging reservation is inactive")
	}
	if budget == nil {
		return fmt.Errorf("evidence staging budget is unavailable")
	}
	budget.mu.Lock()
	defer budget.mu.Unlock()
	if !reservation.active {
		return fmt.Errorf("evidence staging reservation is inactive")
	}
	if budget.poisoned != nil {
		return fmt.Errorf(
			"evidence staging budget is unavailable after cleanup failure: %w",
			budget.poisoned,
		)
	}
	if budget.maximumStages < 1 || budget.maximumEntries < 1 ||
		budget.maximumBytes < 1 {
		return fmt.Errorf("evidence staging budget has invalid limits")
	}
	if budget.stages < 1 || budget.stages > budget.maximumStages ||
		budget.entries < reservation.entries || budget.bytes < reservation.bytes {
		return fmt.Errorf("evidence staging budget accounting underflow")
	}
	otherEntries := budget.entries - reservation.entries
	otherBytes := budget.bytes - reservation.bytes
	neededEntries := len(snapshot.entries)
	if neededEntries > budget.maximumEntries-otherEntries {
		return fmt.Errorf(
			"evidence staging budget exceeds %d total entries after regeneration",
			budget.maximumEntries,
		)
	}
	if snapshot.totalBytes > budget.maximumBytes-otherBytes {
		return fmt.Errorf(
			"evidence staging budget exceeds %d total bytes after regeneration",
			budget.maximumBytes,
		)
	}
	budget.entries = otherEntries + neededEntries
	budget.bytes = otherBytes + snapshot.totalBytes
	reservation.entries = neededEntries
	reservation.bytes = snapshot.totalBytes
	return nil
}

func (budget *evidencePreparationBudget) poison(cause error) {
	if budget == nil || cause == nil {
		return
	}
	budget.mu.Lock()
	defer budget.mu.Unlock()
	if budget.poisoned == nil {
		budget.poisoned = cause
	}
}

type evidencePreparationWorkspace struct {
	root           string
	runDir         string
	source         string
	sourceSnapshot evidenceTreeSnapshot
	budget         *evidencePreparationBudget
	reservation    *evidencePreparationReservation
}

func stageEvidenceForPreparation(
	parent string,
	source string,
	budget *evidencePreparationBudget,
) (_ *evidencePreparationWorkspace, returnErr error) {
	sourceSnapshot, err := snapshotEvidenceTree(source)
	if err != nil {
		return nil, fmt.Errorf("snapshot canonical evidence: %w", err)
	}
	reservation, err := budget.reserve(sourceSnapshot)
	if err != nil {
		return nil, err
	}
	if parent == "" {
		parent = os.TempDir()
	}
	parent, err = filepath.Abs(parent)
	if err != nil {
		return nil, appendPreparationError(
			fmt.Errorf("resolve staging parent: %w", err),
			budget.release(reservation),
		)
	}
	parent = filepath.Clean(parent)
	if err := ensureRealDirectoryPath(parent); err != nil {
		return nil, appendPreparationError(
			fmt.Errorf("unsafe staging parent: %w", err),
			budget.release(reservation),
		)
	}
	root, err := os.MkdirTemp(parent, evidencePreparationStagePrefix)
	if err != nil {
		return nil, appendPreparationError(
			fmt.Errorf("create evidence staging directory: %w", err),
			budget.release(reservation),
		)
	}
	defer func() {
		if returnErr != nil {
			cleanupErr := os.RemoveAll(root)
			returnErr = appendPreparationError(returnErr, cleanupErr)
			if cleanupErr != nil {
				budget.poison(cleanupErr)
				return
			}
			releaseErr := budget.release(reservation)
			returnErr = appendPreparationError(returnErr, releaseErr)
			if releaseErr != nil {
				budget.poison(releaseErr)
			}
		}
	}()
	runDir := filepath.Join(root, "run")
	if err := os.Mkdir(runDir, 0o700); err != nil {
		return nil, fmt.Errorf("create staged evidence root: %w", err)
	}
	if err := copyEvidenceSnapshot(source, runDir, sourceSnapshot); err != nil {
		return nil, fmt.Errorf("copy canonical evidence into staging: %w", err)
	}
	stagedSnapshot, err := snapshotEvidenceTree(runDir)
	if err != nil {
		return nil, fmt.Errorf("snapshot staged evidence: %w", err)
	}
	if !evidenceSnapshotsEqual(sourceSnapshot, stagedSnapshot) {
		return nil, fmt.Errorf("staged evidence differs from canonical snapshot")
	}
	currentSource, err := snapshotEvidenceTree(source)
	if err != nil {
		return nil, fmt.Errorf("recheck canonical evidence after staging: %w", err)
	}
	if !evidenceSnapshotsEqual(sourceSnapshot, currentSource) {
		return nil, fmt.Errorf("canonical evidence changed while it was staged")
	}
	return &evidencePreparationWorkspace{
		root:           root,
		runDir:         runDir,
		source:         source,
		sourceSnapshot: sourceSnapshot,
		budget:         budget,
		reservation:    reservation,
	}, nil
}

func (workspace *evidencePreparationWorkspace) verifySourceUnchanged() error {
	current, err := snapshotEvidenceTree(workspace.source)
	if err != nil {
		return fmt.Errorf("recheck canonical evidence after preparation: %w", err)
	}
	if !evidenceSnapshotsEqual(workspace.sourceSnapshot, current) {
		return fmt.Errorf("canonical evidence changed during staged preparation")
	}
	return nil
}

func (workspace *evidencePreparationWorkspace) resizeReservation(
	snapshot evidenceTreeSnapshot,
) error {
	if workspace == nil {
		return fmt.Errorf("evidence staging workspace is unavailable")
	}
	return workspace.budget.resize(workspace.reservation, snapshot)
}

func snapshotEvidenceTree(root string) (evidenceTreeSnapshot, error) {
	if err := ensureRealDirectoryPath(root); err != nil {
		return evidenceTreeSnapshot{}, err
	}
	rootInfo, err := os.Lstat(root)
	if err != nil {
		return evidenceTreeSnapshot{}, err
	}
	var entries []evidenceTreeEntry
	var totalBytes int64
	err = filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if path == root {
			if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 ||
				!os.SameFile(rootInfo, info) {
				return fmt.Errorf("evidence root changed identity: %s", root)
			}
			return nil
		}
		if len(entries) >= maximumEvidencePreparationEntries {
			return fmt.Errorf(
				"evidence tree exceeds %d entries",
				maximumEvidencePreparationEntries,
			)
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		if relative == "." || relative == "" || filepath.IsAbs(relative) ||
			filepath.Clean(relative) != relative || relative == ".." ||
			strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("invalid evidence-relative path %q", relative)
		}
		relative = filepath.ToSlash(relative)
		if len(relative) > maximumEvidencePreparationPathBytes {
			return fmt.Errorf("evidence path is too long: %q", relative)
		}
		if strings.Count(relative, "/")+1 > maximumEvidencePreparationDepth {
			return fmt.Errorf(
				"evidence path exceeds maximum depth %d: %q",
				maximumEvidencePreparationDepth,
				relative,
			)
		}
		entry := evidenceTreeEntry{
			Path: relative,
			Mode: uint32(info.Mode().Perm()),
		}
		switch {
		case info.Mode()&os.ModeSymlink != 0:
			return fmt.Errorf("evidence tree contains symlink: %s", path)
		case info.IsDir():
			entry.Kind = "directory"
		case info.Mode().IsRegular():
			if info.Size() < 0 || info.Size() > maximumEvidencePreparationFileBytes {
				return fmt.Errorf(
					"evidence file exceeds %d bytes: %s",
					maximumEvidencePreparationFileBytes,
					path,
				)
			}
			if info.Size() > maximumEvidencePreparationTreeBytes-totalBytes {
				return fmt.Errorf(
					"evidence tree exceeds %d bytes",
					maximumEvidencePreparationTreeBytes,
				)
			}
			entry.Kind = "file"
			entry.Size = info.Size()
			entry.SHA256, err = digestEvidenceFile(path, info, nil)
			if err != nil {
				return err
			}
			totalBytes += info.Size()
		default:
			return fmt.Errorf("evidence tree contains special file: %s", path)
		}
		entries = append(entries, entry)
		return nil
	})
	if err != nil {
		return evidenceTreeSnapshot{}, err
	}
	rootAfter, err := os.Lstat(root)
	if err != nil {
		return evidenceTreeSnapshot{}, err
	}
	if !rootAfter.IsDir() || rootAfter.Mode()&os.ModeSymlink != 0 ||
		!os.SameFile(rootInfo, rootAfter) {
		return evidenceTreeSnapshot{}, fmt.Errorf(
			"evidence root changed while it was read: %s",
			root,
		)
	}
	canonical, err := json.Marshal(entries)
	if err != nil {
		return evidenceTreeSnapshot{}, fmt.Errorf("encode evidence snapshot: %w", err)
	}
	return evidenceTreeSnapshot{
		canonical:  canonical,
		entries:    entries,
		totalBytes: totalBytes,
	}, nil
}

func copyEvidenceSnapshot(
	source string,
	destination string,
	snapshot evidenceTreeSnapshot,
) error {
	var directories []evidenceTreeEntry
	for _, entry := range snapshot.entries {
		target := filepath.Join(destination, filepath.FromSlash(entry.Path))
		switch entry.Kind {
		case "directory":
			if err := os.Mkdir(target, 0o700); err != nil {
				return err
			}
			directories = append(directories, entry)
		case "file":
			sourcePath := filepath.Join(source, filepath.FromSlash(entry.Path))
			info, err := os.Lstat(sourcePath)
			if err != nil {
				return err
			}
			if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 ||
				info.Size() != entry.Size || uint32(info.Mode().Perm()) != entry.Mode {
				return fmt.Errorf("evidence file changed before copy: %s", sourcePath)
			}
			output, err := os.OpenFile(
				target,
				os.O_CREATE|os.O_EXCL|os.O_WRONLY,
				0o600,
			)
			if err != nil {
				return err
			}
			digest, copyErr := digestEvidenceFile(sourcePath, info, output)
			closeErr := output.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			if digest != entry.SHA256 {
				return fmt.Errorf("evidence file changed while copied: %s", sourcePath)
			}
			if err := os.Chmod(target, os.FileMode(entry.Mode)); err != nil {
				return err
			}
		default:
			return fmt.Errorf("unsupported evidence snapshot entry kind %q", entry.Kind)
		}
	}
	for index := len(directories) - 1; index >= 0; index-- {
		entry := directories[index]
		target := filepath.Join(destination, filepath.FromSlash(entry.Path))
		if err := os.Chmod(target, os.FileMode(entry.Mode)); err != nil {
			return err
		}
	}
	return nil
}

func digestEvidenceFile(
	path string,
	expected os.FileInfo,
	copyTo io.Writer,
) (string, error) {
	if err := ensureRealDirectoryPath(filepath.Dir(path)); err != nil {
		return "", err
	}
	before, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() ||
		!os.SameFile(expected, before) || before.Size() != expected.Size() ||
		before.Mode().Perm() != expected.Mode().Perm() {
		return "", fmt.Errorf("evidence file changed identity: %s", path)
	}
	input, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer input.Close()
	opened, err := input.Stat()
	if err != nil {
		return "", err
	}
	if !opened.Mode().IsRegular() || !os.SameFile(before, opened) ||
		opened.Size() != before.Size() || opened.Mode().Perm() != before.Mode().Perm() {
		return "", fmt.Errorf("evidence file changed while opened: %s", path)
	}
	hasher := sha256.New()
	writers := []io.Writer{hasher}
	if copyTo != nil {
		writers = append(writers, copyTo)
	}
	written, err := io.Copy(
		io.MultiWriter(writers...),
		io.LimitReader(input, before.Size()+1),
	)
	if err != nil {
		return "", err
	}
	if written != before.Size() {
		return "", fmt.Errorf("evidence file size changed while read: %s", path)
	}
	after, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if after.Mode()&os.ModeSymlink != 0 || !after.Mode().IsRegular() ||
		!os.SameFile(before, after) || after.Size() != before.Size() ||
		after.Mode().Perm() != before.Mode().Perm() {
		return "", fmt.Errorf("evidence file changed after read: %s", path)
	}
	if err := ensureRealDirectoryPath(filepath.Dir(path)); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func evidenceSnapshotsEqual(left, right evidenceTreeSnapshot) bool {
	return bytes.Equal(left.canonical, right.canonical)
}

func cleanupPreparedEvidence(
	prepared map[evidencePreparationKey]evidencePreparationOutcome,
) error {
	roots := make(map[string]bool)
	var cleanupErr error
	for _, outcome := range prepared {
		workspace := outcome.workspace
		if workspace == nil || roots[workspace.root] {
			continue
		}
		roots[workspace.root] = true
		if err := cleanupEvidencePreparationWorkspace(workspace); err != nil {
			cleanupErr = appendPreparationError(
				cleanupErr,
				err,
			)
		}
	}
	return cleanupErr
}

func cleanupEvidencePreparationWorkspace(
	workspace *evidencePreparationWorkspace,
) error {
	if workspace == nil {
		return nil
	}
	root := workspace.root
	if !filepath.IsAbs(root) || filepath.Clean(root) != root ||
		filepath.Dir(root) == root ||
		!strings.HasPrefix(filepath.Base(root), evidencePreparationStagePrefix) {
		return fmt.Errorf("refusing to remove invalid evidence staging root: %s", root)
	}
	if err := os.RemoveAll(root); err != nil {
		return fmt.Errorf("remove evidence staging root %s: %w", root, err)
	}
	if err := workspace.budget.release(workspace.reservation); err != nil {
		return fmt.Errorf("release evidence staging budget for %s: %w", root, err)
	}
	return nil
}

func verifyPreparedEvidenceSources(
	prepared map[evidencePreparationKey]evidencePreparationOutcome,
) error {
	roots := make(map[string]bool)
	var verifyErr error
	for _, outcome := range prepared {
		workspace := outcome.workspace
		if workspace == nil || roots[workspace.root] {
			continue
		}
		roots[workspace.root] = true
		if err := workspace.verifySourceUnchanged(); err != nil {
			verifyErr = appendPreparationError(
				verifyErr,
				fmt.Errorf("final canonical evidence verification for %s: %w", workspace.source, err),
			)
		}
	}
	return verifyErr
}

func finalizePreparedEvidence(
	results []caseResult,
	prepared map[evidencePreparationKey]evidencePreparationOutcome,
) []caseResult {
	finalErr := verifyPreparedEvidenceSources(prepared)
	if cleanupErr := cleanupPreparedEvidence(prepared); cleanupErr != nil {
		finalErr = appendPreparationError(
			finalErr,
			fmt.Errorf("clean staged evidence: %w", cleanupErr),
		)
	}
	if verifyErr := verifyPreparedEvidenceSources(prepared); verifyErr != nil {
		finalErr = appendPreparationError(
			finalErr,
			fmt.Errorf("verify canonical evidence after staging cleanup: %w", verifyErr),
		)
	}
	if finalErr == nil {
		return results
	}
	return failPreparedEvidenceResults(
		results,
		fmt.Errorf("finalize staged evidence: %w", finalErr),
	)
}

func failPreparedEvidenceResults(results []caseResult, err error) []caseResult {
	for index := range results {
		results[index].Passed = false
		results[index].Skipped = false
		if results[index].Error != "" {
			results[index].Error += "; "
		}
		results[index].Error += err.Error()
	}
	return results
}

func appendPreparationError(current, next error) error {
	if next == nil {
		return current
	}
	if current == nil {
		return next
	}
	return fmt.Errorf("%w; %w", current, next)
}

func strictQualityReplayEnvironment(config strictQualityReplayConfig) []string {
	environment := make([]string, 0, len(os.Environ())+1)
	for _, entry := range os.Environ() {
		name, _, _ := strings.Cut(entry, "=")
		if name == "LSP_JUDGE_MODEL" || name == "LSP_JUDGE_MODEL_MODE" {
			continue
		}
		environment = append(environment, entry)
	}
	if config.judgeModelMode == "pinned" {
		environment = append(environment, "LSP_JUDGE_MODEL="+config.judgeModel)
	}
	return environment
}

func prefixChecks(
	prefix string,
	checks []experimentsuite.CheckResult,
) []experimentsuite.CheckResult {
	for index := range checks {
		checks[index].Description = prefix + checks[index].Description
	}
	return checks
}

func resolutionStatus(expected string, passed, skipped bool) string {
	if skipped {
		return "unknown"
	}
	if passed {
		return expected
	}
	if expected == "accepted" {
		return "regressed"
	}
	return "unresolved"
}

func runGoTest(
	ctx context.Context,
	opts options,
	goTest experimentsuite.GoTest,
	key string,
) goTestOutcome {
	sum := sha256.Sum256([]byte(key))
	logDir := filepath.Join(opts.outputDir, "go-tests")
	if err := ensureDirectory(logDir, 0o700); err != nil {
		return goTestOutcome{err: err}
	}
	logPath := filepath.Join(logDir, hex.EncodeToString(sum[:8])+".log")
	tempDir, err := os.MkdirTemp(logDir, ".go-test-tmp-")
	if err != nil {
		return goTestOutcome{log: logPath, err: err}
	}
	defer func() { _ = os.RemoveAll(tempDir) }()

	filter, err := parseGoTestFilter(goTest.Run)
	if err != nil {
		if writeErr := writeFileAtomic(
			logPath,
			[]byte(err.Error()+"\n"),
		); writeErr != nil {
			return goTestOutcome{log: logPath, err: writeErr}
		}
		return goTestOutcome{log: logPath, err: err}
	}
	command := exec.CommandContext(
		ctx,
		"go",
		"test",
		goTest.Package,
		"-json",
		"-run",
		filter.exactPattern(),
		"-count=1",
	)
	command.Dir = opts.repoRoot
	for _, item := range os.Environ() {
		if strings.HasPrefix(item, "TMPDIR=") ||
			strings.HasPrefix(item, "GOTMPDIR=") ||
			strings.HasPrefix(item, "GOWORK=") ||
			strings.HasPrefix(item, "GOFLAGS=") {
			continue
		}
		command.Env = append(command.Env, item)
	}
	command.Env = append(
		command.Env,
		"TMPDIR="+tempDir,
		"GOTMPDIR="+tempDir,
		"GOWORK=off",
		"GOFLAGS=",
	)
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err = command.Run()
	logOutput := append([]byte(nil), stdout.Bytes()...)
	logOutput = append(logOutput, stderr.Bytes()...)
	if writeErr := writeFileAtomic(logPath, logOutput); writeErr != nil {
		return goTestOutcome{log: logPath, err: writeErr}
	}
	if err == nil {
		err = validateGoTestExecution(goTest.Run, stdout.Bytes())
	}
	return goTestOutcome{
		passed: err == nil,
		log:    logPath,
		err:    err,
	}
}

func validateGoTestExecution(run string, output []byte) error {
	filter, err := parseGoTestFilter(run)
	if err != nil {
		return err
	}
	matcher, err := filter.matcher()
	if err != nil {
		return err
	}
	executed := make(map[string]bool)
	passed := make(map[string]bool)
	decoder := json.NewDecoder(strings.NewReader(string(output)))
	for {
		var event goTestEvent
		if decodeErr := decoder.Decode(&event); decodeErr != nil {
			if errors.Is(decodeErr, io.EOF) {
				break
			}
			return fmt.Errorf("decode go test event: %w", decodeErr)
		}
		if event.Action == "run" &&
			event.Test != "" &&
			matcher.matches(event.Test) {
			executed[event.Test] = true
		}
		if event.Action == "pass" &&
			event.Test != "" &&
			matcher.matches(event.Test) {
			passed[event.Test] = true
		}
	}
	if len(executed) == 0 {
		return fmt.Errorf("go test selector %q matched no tests", run)
	}

	var missing []string
	var notPassed []string
	for index, alternative := range matcher {
		executedAlternative := false
		passedAlternative := false
		for name := range executed {
			if alternative.matchesExact(name) {
				executedAlternative = true
				if passed[name] {
					passedAlternative = true
					break
				}
			}
		}
		name := strings.Join(filter[index], "/")
		if !executedAlternative {
			missing = append(
				missing,
				name,
			)
		} else if !passedAlternative {
			notPassed = append(notPassed, name)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf(
			"go test selector %q did not execute: %s",
			run,
			strings.Join(missing, ", "),
		)
	}
	if len(notPassed) > 0 {
		return fmt.Errorf(
			"go test selector %q did not pass: %s",
			run,
			strings.Join(notPassed, ", "),
		)
	}
	return nil
}

type goTestFilter [][]string

// parseGoTestFilter mirrors the separator rules used by testing.splitRegexp:
// slashes and pipes split only when they are outside character classes,
// parentheses, and escapes.
func parseGoTestFilter(run string) (goTestFilter, error) {
	var filter goTestFilter
	var path []string
	start := 0
	charClassDepth := 0
	parenthesisDepth := 0
	for index := 0; index < len(run); index++ {
		switch run[index] {
		case '[':
			charClassDepth++
		case ']':
			charClassDepth--
			if charClassDepth < 0 {
				charClassDepth = 0
			}
		case '(':
			if charClassDepth == 0 {
				parenthesisDepth++
			}
		case ')':
			if charClassDepth == 0 {
				parenthesisDepth--
			}
		case '\\':
			index++
		case '/', '|':
			if charClassDepth != 0 || parenthesisDepth != 0 {
				continue
			}
			path = append(path, run[start:index])
			start = index + 1
			if run[index] == '|' {
				filter = append(filter, path)
				path = nil
			}
		}
	}
	path = append(path, run[start:])
	filter = append(filter, path)
	for _, alternative := range filter {
		if len(alternative) == 1 && alternative[0] == "" {
			return nil, fmt.Errorf(
				"go test selector %q contains an empty alternative",
				run,
			)
		}
	}
	for _, alternative := range filter {
		for _, component := range alternative {
			if _, err := regexp.Compile(rewriteGoTestName(component)); err != nil {
				return nil, fmt.Errorf(
					"compile go test selector component %q: %w",
					component,
					err,
				)
			}
		}
	}
	return filter, nil
}

func (filter goTestFilter) exactPattern() string {
	var result strings.Builder
	for alternativeIndex, alternative := range filter {
		if alternativeIndex > 0 {
			result.WriteByte('|')
		}
		for componentIndex, component := range alternative {
			if componentIndex > 0 {
				result.WriteByte('/')
			}
			if component == "" {
				component = ".*"
			}
			result.WriteString("^(?:")
			result.WriteString(component)
			result.WriteString(")$")
		}
	}
	return result.String()
}

type goTestAlternativeMatcher []*regexp.Regexp

type goTestMatcher []goTestAlternativeMatcher

func (filter goTestFilter) matcher() (goTestMatcher, error) {
	matcher := make(goTestMatcher, 0, len(filter))
	for _, alternative := range filter {
		patterns := make(goTestAlternativeMatcher, 0, len(alternative))
		for _, component := range alternative {
			if component == "" {
				component = ".*"
			}
			pattern := rewriteGoTestName("^(?:" + component + ")$")
			compiled, err := regexp.Compile(pattern)
			if err != nil {
				return nil, fmt.Errorf(
					"compile go test selector component %q: %w",
					component,
					err,
				)
			}
			patterns = append(patterns, compiled)
		}
		matcher = append(matcher, patterns)
	}
	return matcher, nil
}

func (alternative goTestAlternativeMatcher) matchesExact(name string) bool {
	components := strings.Split(name, "/")
	if len(components) != len(alternative) {
		return false
	}
	for index, pattern := range alternative {
		if !pattern.MatchString(components[index]) {
			return false
		}
	}
	return true
}

func (matcher goTestMatcher) matches(name string) bool {
	components := strings.Split(name, "/")
	for _, alternative := range matcher {
		if len(components) < len(alternative) {
			continue
		}
		matched := true
		for index, pattern := range alternative {
			if !pattern.MatchString(components[index]) {
				matched = false
				break
			}
		}
		if matched {
			return true
		}
	}
	return false
}

// rewriteGoTestName matches testing's rewriting of filter components and
// subtest names before regular-expression matching.
func rewriteGoTestName(name string) string {
	var result []byte
	for _, character := range name {
		switch {
		case isGoTestSpace(character):
			result = append(result, '_')
		case !strconv.IsPrint(character):
			quoted := strconv.QuoteRune(character)
			result = append(result, quoted[1:len(quoted)-1]...)
		default:
			result = append(result, string(character)...)
		}
	}
	return string(result)
}

func isGoTestSpace(character rune) bool {
	if character < 0x2000 {
		switch character {
		case '\t', '\n', '\v', '\f', '\r', ' ', 0x85, 0xA0, 0x1680:
			return true
		}
		return false
	}
	if character <= 0x200a {
		return true
	}
	switch character {
	case 0x2028, 0x2029, 0x202f, 0x205f, 0x3000:
		return true
	}
	return false
}

func live(ctx context.Context, opts options, cases []experimentsuite.Case) []caseResult {
	var results []caseResult
	for _, testCase := range cases {
		result := caseResult{
			ID:                testCase.ID,
			Level:             testCase.Level,
			Complexity:        testCase.Complexity,
			Outcome:           testCase.Outcome,
			QualityProvenance: "strict-current",
		}
		if testCase.Live == nil {
			result.Error = "rejected case is replay-only"
			results = append(results, result)
			printCaseResult(result)
			continue
		}
		runID := "suite-" + testCase.ID + "-" + time.Now().UTC().Format("20060102T150405Z")
		runsRoot := filepath.Join(opts.evidenceRoot, "runs")
		if err := ensureDirectory(runsRoot, 0o755); err != nil {
			result.Error = "prepare live evidence root: " + err.Error()
			results = append(results, result)
			printCaseResult(result)
			continue
		}
		runDir := filepath.Join(runsRoot, runID)
		result.Evidence = runDir
		sourceRepo := filepath.Join(
			opts.repoRoot,
			filepath.FromSlash(testCase.Live.Source),
		)
		variant := "all"
		runArgs := []string{
			"--task", testCase.Live.Task,
			"--variant", variant,
			"--profile", testCase.Live.Profile,
			"--source", sourceRepo,
			"--commit", testCase.Live.Commit,
			"--prompt-commit", testCase.Live.PromptCommit,
			"--base", testCase.Live.Base,
			"--model-mode", testCase.Live.ModelMode,
			"--evidence-root", runsRoot,
			"--run-id", runID,
		}
		if testCase.Live.ModelMode == "pinned" {
			runArgs = append(runArgs, "--model", testCase.Live.Model)
		}
		baselineFrom := ""
		if testCase.Live.BaselineFrom != "" {
			baselineFrom = filepath.Join(
				opts.evidenceRoot,
				filepath.FromSlash(testCase.Live.BaselineFrom),
			)
			runArgs[3] = "optimized"
			runArgs = append(runArgs, "--baseline-from", baselineFrom)
		}
		if err := runCommand(
			ctx,
			opts.repoRoot,
			filepath.Join(opts.repoRoot, "experiments/lsp-replacement/run.sh"),
			runArgs...,
		); err != nil {
			result.Error = err.Error()
			results = append(results, result)
			printCaseResult(result)
			continue
		}
		if err := ensureRealDirectoryPath(runDir); err != nil {
			result.Error = "unsafe generated evidence directory: " + err.Error()
			results = append(results, result)
			printCaseResult(result)
			continue
		}
		qualityArgs := []string{
			runDir,
			"--enforce",
			"--model-mode", testCase.Live.ModelMode,
		}
		if opts.judgeRepeats > 0 {
			qualityArgs = append(
				qualityArgs,
				"--judge-repeats",
				fmt.Sprintf("%d", opts.judgeRepeats),
			)
		}
		if err := runCommand(
			ctx,
			opts.repoRoot,
			filepath.Join(opts.repoRoot, "experiments/lsp-replacement/quality-check.sh"),
			qualityArgs...,
		); err != nil {
			result.Error = err.Error()
			results = append(results, result)
			printCaseResult(result)
			continue
		}
		result.Checks = experimentsuite.ValidateEvidence(runDir, testCase.Assertions)
		identityPaths := []string{sourceRepo}
		if baselineFrom != "" {
			identityPaths = append(identityPaths, baselineFrom)
		}
		result.Checks = append(
			result.Checks,
			experimentsuite.ValidateLiveIdentity(
				runDir,
				*testCase.Live,
				identityPaths...,
			),
		)
		result.Passed = experimentsuite.Passed(result.Checks)
		results = append(results, result)
		printCaseResult(result)
	}
	return results
}

func repairCases(
	ctx context.Context,
	opts options,
	cases []experimentsuite.Case,
	resolutions []experimentsuite.ResolutionCase,
) []caseResult {
	resolutionByID := make(map[string]experimentsuite.ResolutionCase, len(resolutions))
	for _, resolution := range resolutions {
		resolutionByID[resolution.ID] = resolution
	}

	var results []caseResult
	for _, testCase := range cases {
		resolution := resolutionByID[testCase.ID]
		result := caseResult{
			ID:                testCase.ID,
			Level:             testCase.Level,
			Complexity:        testCase.Complexity,
			Outcome:           testCase.Outcome,
			RootCause:         resolution.RootCause,
			Fix:               resolution.Fix,
			CurrentStatus:     "failed",
			QualityProvenance: "strict-current",
		}
		if resolution.Repair == nil {
			result.Error = "case has no repair configuration"
			results = append(results, result)
			printCaseResult(result)
			continue
		}

		sourceRepo := filepath.Join(
			opts.repoRoot,
			filepath.FromSlash(resolution.Repair.Source),
		)
		baselineFrom := ""
		runDir := opts.repairAttempt
		if runDir == "" {
			runID := time.Now().UTC().Format("20060102T150405Z")
			repairsRoot := filepath.Join(opts.evidenceRoot, "repairs", testCase.ID)
			if err := ensureDirectory(repairsRoot, 0o755); err != nil {
				result.Error = "prepare repair evidence root: " + err.Error()
			}
			runDir = filepath.Join(repairsRoot, runID)
			sourceRepo := filepath.Join(
				opts.repoRoot,
				filepath.FromSlash(resolution.Repair.Source),
			)
			variant := "all"
			runArgs := []string{
				"--task", resolution.Repair.Task,
				"--variant", variant,
				"--profile", resolution.Repair.Profile,
				"--source", sourceRepo,
				"--commit", resolution.Repair.Commit,
				"--prompt-commit", resolution.Repair.PromptCommit,
				"--base", resolution.Repair.Base,
				"--model-mode", resolution.Repair.ModelMode,
				"--evidence-root", repairsRoot,
				"--run-id", runID,
			}
			if resolution.Repair.ModelMode == "pinned" {
				runArgs = append(runArgs, "--model", resolution.Repair.Model)
			}
			if resolution.Repair.BaselineFrom != "" {
				baselineFrom = filepath.Join(
					opts.evidenceRoot,
					filepath.FromSlash(resolution.Repair.BaselineFrom),
				)
				runArgs[3] = "optimized"
				runArgs = append(runArgs, "--baseline-from", baselineFrom)
			}
			if result.Error == "" {
				if err := runCommand(
					ctx,
					opts.repoRoot,
					filepath.Join(opts.repoRoot, "experiments/lsp-replacement/run.sh"),
					runArgs...,
				); err != nil {
					result.Error = err.Error()
				}
			}
		} else if err := ensureRealDirectoryPath(runDir); err != nil {
			result.Error = "repair attempt: " + err.Error()
		}
		result.Evidence = runDir
		if result.Error == "" {
			if err := ensureRealDirectoryPath(runDir); err != nil {
				result.Error = "unsafe repair evidence directory: " + err.Error()
			}
		}
		identityPaths := []string{sourceRepo}
		if resolution.Repair.BaselineFrom != "" {
			if baselineFrom == "" {
				baselineFrom = filepath.Join(
					opts.evidenceRoot,
					filepath.FromSlash(resolution.Repair.BaselineFrom),
				)
			}
			identityPaths = append(identityPaths, baselineFrom)
		}
		if result.Error == "" {
			identityCheck := experimentsuite.ValidateLiveIdentity(
				runDir,
				*resolution.Repair,
				identityPaths...,
			)
			result.Checks = append(result.Checks, identityCheck)
			if !identityCheck.Passed {
				result.Error = "repair identity: " + identityCheck.Error
			}
		}
		if result.Error == "" {
			if err := runCommand(
				ctx,
				opts.repoRoot,
				filepath.Join(opts.repoRoot, "experiments/lsp-replacement/quality-check.sh"),
				runDir,
				"--model-mode", resolution.Repair.ModelMode,
				"--enforce",
			); err != nil {
				result.Error = "deterministic quality gate: " + err.Error()
			}
		}
		if result.Error == "" {
			preJudgeMetrics, err := experimentsuite.SummarizeEvidence(
				runDir,
				resolution.MetricCases,
			)
			if err != nil {
				result.Error = "pre-judge metrics: " + err.Error()
			} else if !experimentsuite.Passed(
				experimentsuite.ValidatePromotion(preJudgeMetrics, 0),
			) {
				result.Error = "pre-judge token or navigation promotion gate failed"
			}
		}
		if result.Error == "" {
			if err := ensureRealDirectoryPath(runDir); err != nil {
				result.Error = "unsafe repair evidence directory before judge quality gate: " + err.Error()
			} else if err := runCommand(
				ctx,
				opts.repoRoot,
				filepath.Join(opts.repoRoot, "experiments/lsp-replacement/quality-check.sh"),
				runDir,
				"--judge-repeats",
				fmt.Sprintf("%d", opts.judgeRepeats),
				"--model-mode", resolution.Repair.ModelMode,
				"--enforce",
			); err != nil {
				result.Error = "judge quality gate: " + err.Error()
			}
		}

		result.Checks = append(
			result.Checks,
			experimentsuite.ValidateEvidence(
				runDir,
				resolution.Assertions,
			)...,
		)
		result.Checks = append(
			result.Checks,
			experimentsuite.ValidateLiveIdentity(
				runDir,
				*resolution.Repair,
				filepath.Join(
					opts.repoRoot,
					filepath.FromSlash(resolution.Repair.Source),
				),
			),
		)
		metrics, metricErr := experimentsuite.SummarizeEvidence(
			runDir,
			resolution.MetricCases,
		)
		result.Metrics = metrics
		if metricErr != nil {
			if result.Error != "" {
				result.Error += "; "
			}
			result.Error += "repair metrics: " + metricErr.Error()
		} else {
			result.Checks = append(
				result.Checks,
				experimentsuite.ValidatePromotion(metrics, opts.judgeRepeats)...,
			)
		}
		result.Passed = result.Error == "" && experimentsuite.Passed(result.Checks)
		if result.Passed {
			result.CurrentStatus = "resolved"
		}
		results = append(results, result)
		printCaseResult(result)
	}
	return results
}

func runCommand(ctx context.Context, workDir, name string, args ...string) error {
	return runCommandWithStdout(ctx, workDir, os.Stdout, name, args...)
}

func runCommandWithStdout(
	ctx context.Context,
	workDir string,
	stdout io.Writer,
	name string,
	args ...string,
) error {
	return runCommandWithStdoutEnvironment(
		ctx,
		workDir,
		stdout,
		nil,
		name,
		args...,
	)
}

func runCommandWithStdoutEnvironment(
	ctx context.Context,
	workDir string,
	stdout io.Writer,
	environment []string,
	name string,
	args ...string,
) error {
	command := exec.CommandContext(ctx, name, args...)
	command.Dir = workDir
	command.Stdout = stdout
	command.Stderr = os.Stderr
	if environment != nil {
		command.Env = environment
	}
	if err := command.Run(); err != nil {
		return fmt.Errorf("%s failed: %w", filepath.Base(name), err)
	}
	return nil
}

func prepareOutputDir(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("output directory must be a clean absolute path: %s", path)
	}
	parent := filepath.Dir(path)
	if parent == path {
		return fmt.Errorf("refusing to use filesystem root as output directory: %s", path)
	}
	if err := ensureDirectory(parent, 0o755); err != nil {
		return fmt.Errorf("prepare output parent: %w", err)
	}
	if err := os.Mkdir(path, 0o700); err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("output directory already exists: %s", path)
		}
		return fmt.Errorf("create output directory: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return fmt.Errorf("verify output directory: %w", err)
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("output path is not a private directory: %s", path)
	}
	return nil
}

func ensureRealDirectoryPath(path string) error {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return fmt.Errorf("directory must be a clean absolute path: %s", path)
	}
	return inspectRealDirectoryPath(path)
}

func inspectRealDirectoryPath(path string) error {
	parent := filepath.Dir(path)
	if parent != path {
		if err := inspectRealDirectoryPath(parent); err != nil {
			return err
		}
	}
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("directory path traverses symlink: %s", path)
	}
	if !info.IsDir() {
		return fmt.Errorf("directory path contains non-directory component: %s", path)
	}
	return nil
}

func ensureDirectory(path string, perm os.FileMode) error {
	path = filepath.Clean(path)
	parent := filepath.Dir(path)
	if parent != path {
		if err := ensureDirectory(parent, perm); err != nil {
			return err
		}
	}
	info, err := os.Lstat(path)
	if err == nil {
		if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
			return fmt.Errorf("directory path contains non-directory component: %s", path)
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err := os.Mkdir(path, perm); err != nil {
		if !errors.Is(err, os.ErrExist) {
			return err
		}
	}
	info, err = os.Lstat(path)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("directory path changed while creating it: %s", path)
	}
	return nil
}

func writeFileAtomic(path string, data []byte) error {
	directory := filepath.Dir(path)
	info, err := os.Lstat(directory)
	if err != nil {
		return err
	}
	if info.Mode()&os.ModeSymlink != 0 || !info.IsDir() {
		return fmt.Errorf("output parent is not a directory: %s", directory)
	}
	temporary, err := os.CreateTemp(directory, ".publish-*")
	if err != nil {
		return err
	}
	temporaryPath := temporary.Name()
	defer func() {
		_ = temporary.Close()
		_ = os.Remove(temporaryPath)
	}()
	if err := temporary.Chmod(0o644); err != nil {
		return err
	}
	if _, err := temporary.Write(data); err != nil {
		return err
	}
	if err := temporary.Close(); err != nil {
		return err
	}
	if err := os.Rename(temporaryPath, path); err != nil {
		return err
	}
	temporaryPath = ""
	return nil
}

func writeResults(outputDir string, result suiteResult) error {
	jsonData, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	jsonData = append(jsonData, '\n')
	if err := writeFileAtomic(filepath.Join(outputDir, "results.json"), jsonData); err != nil {
		return err
	}
	if result.Command == "resolve" {
		return writeResolutionSummary(outputDir, result)
	}
	if result.Command == "repair" {
		return writeRepairSummary(outputDir, result)
	}

	var summary strings.Builder
	fmt.Fprintln(&summary, "# LSP-Replacement Regression Suite")
	fmt.Fprintf(&summary, "\n- Command: `%s`\n", result.Command)
	fmt.Fprintf(&summary, "- Started: `%s`\n", result.StartedAt)
	fmt.Fprintf(&summary, "- Finished: `%s`\n", result.FinishedAt)
	fmt.Fprintf(&summary, "- Manifest SHA-256: `%s`\n", result.ManifestSHA256)
	fmt.Fprintf(&summary, "- Result: **%s**\n", passLabel(result.Passed))
	writeEvidencePreparationModes(&summary, result)
	fmt.Fprintln(&summary)
	if result.Command == "replay" {
		fmt.Fprintln(
			&summary,
			"A rejected case passes replay when its fixture rejection signature is reproduced; it is not an accepted model result.",
		)
		fmt.Fprintln(&summary)
	}
	fmt.Fprintln(&summary, "| Level | Case | Expected | Status | Evidence | Checks |")
	fmt.Fprintln(&summary, "| ---: | --- | --- | --- | --- | ---: |")
	for _, current := range result.Cases {
		status := passLabel(current.Passed)
		if current.Skipped {
			status = "SKIP"
		}
		passedChecks := 0
		for _, check := range current.Checks {
			if check.Passed {
				passedChecks++
			}
		}
		fmt.Fprintf(
			&summary,
			"| %d | `%s` | %s | **%s** | `%s` (%s) | %d/%d |\n",
			current.Level,
			current.ID,
			current.Outcome,
			status,
			current.Evidence,
			current.QualityProvenance,
			passedChecks,
			len(current.Checks),
		)
	}
	fmt.Fprintln(&summary, "\n## Failures")
	failures := 0
	for _, current := range result.Cases {
		if current.Passed {
			continue
		}
		failures++
		if current.Error != "" {
			fmt.Fprintf(&summary, "\n- `%s`: %s\n", current.ID, current.Error)
		}
		for _, check := range current.Checks {
			if !check.Passed {
				fmt.Fprintf(
					&summary,
					"\n- `%s`: %s; actual=`%v`, expected=`%v`, error=%s\n",
					current.ID,
					check.Description,
					check.Actual,
					check.Expected,
					check.Error,
				)
			}
		}
	}
	if failures == 0 {
		fmt.Fprintln(&summary, "\nNone.")
	}
	return writeFileAtomic(filepath.Join(outputDir, "summary.md"), []byte(summary.String()))
}

func writeRepairSummary(outputDir string, result suiteResult) error {
	var summary strings.Builder
	fmt.Fprintln(&summary, "# Failed-Case Repair Suite")
	fmt.Fprintf(&summary, "\n- Started: `%s`\n", result.StartedAt)
	fmt.Fprintf(&summary, "- Finished: `%s`\n", result.FinishedAt)
	fmt.Fprintf(&summary, "- Manifest SHA-256: `%s`\n", result.ManifestSHA256)
	fmt.Fprintf(
		&summary,
		"- Resolution manifest SHA-256: `%s`\n",
		result.ResolutionManifestSHA256,
	)
	fmt.Fprintf(&summary, "- Result: **%s**\n", passLabel(result.Passed))
	fmt.Fprintln(
		&summary,
		"\nMetric notation: calls are `total/repo-view/other`; repo-view operations `C/F/I/O` are `changed/find/inspect/outline`; tokens are `regular/cached/output/effective`. Judge scores `C/C/G/A` are correctness/completeness/grounding/adherence; issues `O/U/B/X` are critical omissions/unsupported claims/baseline points omitted/material contradictions. Static quality is deterministic weighted required-criterion coverage, not a model judgment.",
	)
	fmt.Fprintln(
		&summary,
		"\n| Case | Task | Result | Calls baseline | Calls candidate | repo-view candidate C/F/I/O | Tokens baseline | Tokens candidate | Saved | Static baseline/candidate | Judges C/C/G/A | Judge issues O/U/B/X | Verdict | Evidence |",
	)
	fmt.Fprintln(
		&summary,
		"| --- | --- | --- | ---: | ---: | ---: | --- | --- | ---: | ---: | --- | ---: | --- | --- |",
	)
	for _, current := range result.Cases {
		for _, candidate := range current.Metrics {
			if candidate.Variant != "optimized" {
				continue
			}
			baseline := findMetric(current.Metrics, "baseline", candidate.Task)
			savings := "n/a"
			if candidate.HasComparison {
				savings = fmt.Sprintf("%.2f%%", candidate.EffectiveReductionPercent)
			}
			judges := "n/a"
			if candidate.JudgeCount > 0 {
				judges = fmt.Sprintf(
					"%d x %.1f/%.1f/%.1f/%.1f",
					candidate.JudgeCount,
					candidate.AverageCorrectness,
					candidate.AverageCompleteness,
					candidate.AverageGrounding,
					candidate.AverageTaskAdherence,
				)
			}
			verdict := candidate.JudgeStatus
			if verdict == "" {
				verdict = "n/a"
			}
			fmt.Fprintf(
				&summary,
				"| `%s` | %s | **%s** | %s | %s | %d (%d/%d/%d/%d) | %s | %s | %s | %.1f%%/%.1f%% | %s | %d/%d/%d/%d | %s | `%s` |\n",
				current.ID,
				candidate.Task,
				passLabel(current.Passed),
				formatCalls(baseline),
				formatCalls(candidate),
				candidate.RepoViewInvocations,
				candidate.RepoViewChangedInvocations,
				candidate.RepoViewFindInvocations,
				candidate.RepoViewInspectInvocations,
				candidate.RepoViewOutlineInvocations,
				formatTokens(baseline),
				formatTokens(candidate),
				savings,
				baseline.StaticScorePercent,
				candidate.StaticScorePercent,
				judges,
				candidate.JudgeCriticalOmissions,
				candidate.JudgeUnsupportedClaims,
				candidate.JudgeBaselinePointsOmitted,
				candidate.JudgeMaterialContradictions,
				verdict,
				current.Evidence,
			)
		}
	}
	fmt.Fprintln(&summary, "\n## Failures")
	failures := 0
	for _, current := range result.Cases {
		if current.Passed {
			continue
		}
		failures++
		if current.Error != "" {
			fmt.Fprintf(&summary, "\n- `%s`: %s\n", current.ID, current.Error)
		}
		for _, check := range current.Checks {
			if check.Passed {
				continue
			}
			fmt.Fprintf(
				&summary,
				"\n- `%s`: %s; actual=`%v`, expected=`%v`, error=%s\n",
				current.ID,
				check.Description,
				check.Actual,
				check.Expected,
				check.Error,
			)
		}
	}
	if failures == 0 {
		fmt.Fprintln(&summary, "\nNone.")
	}
	return writeFileAtomic(filepath.Join(outputDir, "summary.md"), []byte(summary.String()))
}

func writeResolutionSummary(outputDir string, result suiteResult) error {
	var summary strings.Builder
	passedCases := 0
	resolvedCases := 0
	acceptedCases := 0
	passedChecks := 0
	totalChecks := 0
	for _, current := range result.Cases {
		if current.Passed {
			passedCases++
		}
		switch current.CurrentStatus {
		case "resolved":
			resolvedCases++
		case "accepted":
			acceptedCases++
		}
		currentPassed, currentTotal := checkCounts(current.Checks)
		passedChecks += currentPassed
		totalChecks += currentTotal
	}
	fmt.Fprintln(&summary, "# Failed-Case Resolution Suite")
	fmt.Fprintf(&summary, "\n- Started: `%s`\n", result.StartedAt)
	fmt.Fprintf(&summary, "- Finished: `%s`\n", result.FinishedAt)
	fmt.Fprintf(&summary, "- Case manifest SHA-256: `%s`\n", result.ManifestSHA256)
	fmt.Fprintf(
		&summary,
		"- Resolution manifest SHA-256: `%s`\n",
		result.ResolutionManifestSHA256,
	)
	fmt.Fprintf(&summary, "- Result: **%s**\n", passLabel(result.Passed))
	writeEvidencePreparationModes(&summary, result)
	fmt.Fprintf(
		&summary,
		"- Cases: **%d/%d passed** (%d resolved, %d accepted)\n",
		passedCases,
		len(result.Cases),
		resolvedCases,
		acceptedCases,
	)
	fmt.Fprintf(&summary, "- Checks: **%d/%d passed**\n", passedChecks, totalChecks)
	fmt.Fprintln(
		&summary,
		"\nMetric notation: calls are `total/repo-view/other`; repo-view operations `C/F/I/O` are `changed/find/inspect/outline`; tokens are `regular/cached/output/effective`; edges are `temporal/output-reference`. Judge scores `C/C/G/A` are correctness/completeness/grounding/adherence; issues `O/U/B/X` are critical omissions/unsupported claims/baseline points omitted/material contradictions. Static quality is deterministic weighted required-criterion coverage, not a model judgment.",
	)

	fmt.Fprintln(&summary, "\n## All Cases")
	fmt.Fprintln(
		&summary,
		"\n| Level | Case | Fixture outcome | Resolution status | Result | Checks | Fixture evidence | Replacement evidence |",
	)
	fmt.Fprintln(&summary, "| ---: | --- | --- | --- | --- | ---: | --- | --- |")
	for _, current := range result.Cases {
		status := passLabel(current.Passed)
		if current.Skipped {
			status = "SKIP"
		}
		passedChecks, totalChecks := checkCounts(current.Checks)
		fmt.Fprintf(
			&summary,
			"| %d | `%s` | %s | **%s** | **%s** | %d/%d | `%s` (%s) | `%s` (%s) |\n",
			current.Level,
			current.ID,
			current.Outcome,
			strings.ToUpper(current.CurrentStatus),
			status,
			passedChecks,
			totalChecks,
			current.FixtureEvidence,
			current.FixtureQualityProvenance,
			current.Evidence,
			current.QualityProvenance,
		)
	}

	fmt.Fprintln(&summary, "\n## Causes And Fixes")
	fmt.Fprintln(&summary, "\n| Case | Root cause | Current fix |")
	fmt.Fprintln(&summary, "| --- | --- | --- |")
	for _, current := range result.Cases {
		fmt.Fprintf(
			&summary,
			"| `%s` | %s | %s |\n",
			current.ID,
			escapeTable(current.RootCause),
			escapeTable(current.Fix),
		)
	}

	fmt.Fprintln(&summary, "\n## Failed Cases: Per-Case Stats")
	fmt.Fprintln(
		&summary,
		"\n| Case | Task | Complete baseline/candidate | Calls baseline | Calls candidate | repo-view invocations C/F/I/O | Edges candidate | Tokens baseline | Tokens candidate | Effective saved | Static baseline/candidate | Candidate judges C/C/G/A | Judge issues O/U/B/X | Verdict |",
	)
	fmt.Fprintln(
		&summary,
		"| --- | --- | --- | ---: | ---: | ---: | ---: | --- | --- | ---: | ---: | --- | ---: | --- |",
	)
	for _, current := range result.Cases {
		if current.Outcome != "rejected" {
			continue
		}
		for _, candidate := range current.FixtureMetrics {
			if candidate.Variant != "optimized" {
				continue
			}
			baseline := findMetric(current.FixtureMetrics, "baseline", candidate.Task)
			savings := "n/a"
			if candidate.HasComparison {
				savings = fmt.Sprintf("%.2f%%", candidate.EffectiveReductionPercent)
			}
			judges := "n/a"
			if candidate.JudgeCount > 0 {
				judges = fmt.Sprintf(
					"%d x %.1f/%.1f/%.1f/%.1f",
					candidate.JudgeCount,
					candidate.AverageCorrectness,
					candidate.AverageCompleteness,
					candidate.AverageGrounding,
					candidate.AverageTaskAdherence,
				)
			}
			verdict := candidate.JudgeStatus
			if verdict == "" {
				verdict = "n/a"
			}
			fmt.Fprintf(
				&summary,
				"| `%s` | %s | %t/%t | %s | %s | %d (%d/%d/%d/%d) | %d/%d | %s | %s | %s | %.1f%%/%.1f%% | %s | %d/%d/%d/%d | %s |\n",
				current.ID,
				candidate.Task,
				baseline.Completed,
				candidate.Completed,
				formatCalls(baseline),
				formatCalls(candidate),
				candidate.RepoViewInvocations,
				candidate.RepoViewChangedInvocations,
				candidate.RepoViewFindInvocations,
				candidate.RepoViewInspectInvocations,
				candidate.RepoViewOutlineInvocations,
				candidate.TemporalEdges,
				candidate.OutputReferenceEdges,
				formatTokens(baseline),
				formatTokens(candidate),
				savings,
				baseline.StaticScorePercent,
				candidate.StaticScorePercent,
				judges,
				candidate.JudgeCriticalOmissions,
				candidate.JudgeUnsupportedClaims,
				candidate.JudgeBaselinePointsOmitted,
				candidate.JudgeMaterialContradictions,
				verdict,
			)
		}
	}

	fmt.Fprintln(&summary, "\n## Failed Cases: Per-Tool Stats And Graphs")
	fmt.Fprintln(
		&summary,
		"\nEach tool value is `tool calls/invocations/output characters`.",
	)
	fmt.Fprintln(
		&summary,
		"\n| Case | Task | Baseline tools and operations | Candidate tools and operations | Baseline graph | Candidate graph |",
	)
	fmt.Fprintln(&summary, "| --- | --- | --- | --- | --- | --- |")
	for _, current := range result.Cases {
		if current.Outcome != "rejected" {
			continue
		}
		for _, candidate := range current.FixtureMetrics {
			if candidate.Variant != "optimized" {
				continue
			}
			baseline := findMetric(current.FixtureMetrics, "baseline", candidate.Task)
			baselineGraph := filepath.Join(
				current.FixtureEvidence,
				filepath.FromSlash(baseline.CallGraphMarkdownFile),
			)
			candidateGraph := filepath.Join(
				current.FixtureEvidence,
				filepath.FromSlash(candidate.CallGraphMarkdownFile),
			)
			fmt.Fprintf(
				&summary,
				"| `%s` | %s | %s | %s | `%s` | `%s` |\n",
				current.ID,
				candidate.Task,
				escapeTable(formatToolBreakdown(baseline)),
				escapeTable(formatToolBreakdown(candidate)),
				baselineGraph,
				candidateGraph,
			)
		}
	}

	type uniqueMetric struct {
		evidence string
		metric   experimentsuite.EvidenceMetric
	}
	seenMetrics := make(map[string]bool)
	var metrics []uniqueMetric
	for _, current := range result.Cases {
		for _, metric := range current.Metrics {
			key := current.Evidence + "\x00" + metric.Name
			if seenMetrics[key] {
				continue
			}
			seenMetrics[key] = true
			metrics = append(metrics, uniqueMetric{
				evidence: current.Evidence,
				metric:   metric,
			})
		}
	}

	fmt.Fprintln(&summary, "\n## Current Replacement: Tool And Graph Results")
	fmt.Fprintln(
		&summary,
		"\nRows are unique by evidence path and run case. Cases that share one verified replacement do not duplicate its rows.",
	)
	fmt.Fprintln(
		&summary,
		"\n| Run case | Variant | Calls total/repo-view/other | repo-view invocations C/F/I/O | Edges temporal/reference | Tool types | Operations (calls/invocations/output chars) | Call graph |",
	)
	fmt.Fprintln(
		&summary,
		"| --- | --- | ---: | ---: | ---: | --- | --- | --- |",
	)
	for _, current := range metrics {
		metric := current.metric
		graph := filepath.Join(current.evidence, filepath.FromSlash(metric.CallGraphMarkdownFile))
		fmt.Fprintf(
			&summary,
			"| `%s` | %s | %d/%d/%d | %d (%d/%d/%d/%d) | %d/%d | %s | %s | `%s` |\n",
			metric.Name,
			metric.Variant,
			metric.TotalToolCalls,
			metric.RepoViewToolCalls,
			metric.OtherToolCalls,
			metric.RepoViewInvocations,
			metric.RepoViewChangedInvocations,
			metric.RepoViewFindInvocations,
			metric.RepoViewInspectInvocations,
			metric.RepoViewOutlineInvocations,
			metric.TemporalEdges,
			metric.OutputReferenceEdges,
			escapeTable(formatToolStats(metric.ToolTypes)),
			escapeTable(formatToolStats(metric.Operations)),
			graph,
		)
	}

	fmt.Fprintln(&summary, "\n## Current Replacement: Token And Quality Results")
	fmt.Fprintln(
		&summary,
		"\n| Run case | Calls total/repo-view/other | Regular input | Cached input | Output | Effective | Effective saved | Static | Judges C/C/G/A | Judge issues O/U/B/X | Verdict |",
	)
	fmt.Fprintln(
		&summary,
		"| --- | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | ---: | --- |",
	)
	for _, current := range metrics {
		metric := current.metric
		savings := "n/a"
		if metric.HasComparison {
			savings = fmt.Sprintf("%.2f%%", metric.EffectiveReductionPercent)
		}
		judges := "n/a"
		if metric.JudgeCount > 0 {
			judges = fmt.Sprintf(
				"%d x %.1f/%.1f/%.1f/%.1f",
				metric.JudgeCount,
				metric.AverageCorrectness,
				metric.AverageCompleteness,
				metric.AverageGrounding,
				metric.AverageTaskAdherence,
			)
		}
		fmt.Fprintf(
			&summary,
			"| `%s` | %d/%d/%d | %d | %d | %d | %.1f | %s | %.1f%% | %s | %d/%d/%d/%d | %s |\n",
			metric.Name,
			metric.TotalToolCalls,
			metric.RepoViewToolCalls,
			metric.OtherToolCalls,
			metric.RegularInputTokens,
			metric.CachedInputTokens,
			metric.OutputTokens,
			metric.EffectiveTokens,
			savings,
			metric.StaticScorePercent,
			judges,
			metric.JudgeCriticalOmissions,
			metric.JudgeUnsupportedClaims,
			metric.JudgeBaselinePointsOmitted,
			metric.JudgeMaterialContradictions,
			metric.JudgeStatus,
		)
	}

	fmt.Fprintln(&summary, "\n## Failures")
	failures := 0
	for _, current := range result.Cases {
		if current.Passed {
			continue
		}
		failures++
		if current.Error != "" {
			fmt.Fprintf(&summary, "\n- `%s`: %s\n", current.ID, current.Error)
		}
		for _, check := range current.Checks {
			if !check.Passed {
				fmt.Fprintf(
					&summary,
					"\n- `%s`: %s; actual=`%v`, expected=`%v`, error=%s\n",
					current.ID,
					check.Description,
					check.Actual,
					check.Expected,
					check.Error,
				)
			}
		}
	}
	if failures == 0 {
		fmt.Fprintln(&summary, "\nNone.")
	}
	return writeFileAtomic(filepath.Join(outputDir, "summary.md"), []byte(summary.String()))
}

func writeEvidencePreparationModes(summary *strings.Builder, result suiteResult) {
	if result.EvidenceAnalysis != "" {
		fmt.Fprintf(
			summary,
			"- Evidence analysis: `%s`\n",
			result.EvidenceAnalysis,
		)
	}
	if result.QualityAggregation != "" {
		fmt.Fprintf(
			summary,
			"- Quality aggregation: `%s`\n",
			result.QualityAggregation,
		)
	}
}

func checkCounts(checks []experimentsuite.CheckResult) (int, int) {
	passed := 0
	for _, check := range checks {
		if check.Passed {
			passed++
		}
	}
	return passed, len(checks)
}

func formatToolStats(stats []experimentsuite.ToolStat) string {
	if len(stats) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(stats))
	for _, stat := range stats {
		parts = append(parts, fmt.Sprintf(
			"%s %d/%d/%d",
			stat.Name,
			stat.ToolCalls,
			stat.Invocations,
			stat.OutputCharacters,
		))
	}
	return strings.Join(parts, "; ")
}

func findMetric(
	metrics []experimentsuite.EvidenceMetric,
	variant string,
	task string,
) experimentsuite.EvidenceMetric {
	for _, metric := range metrics {
		if metric.Variant == variant && metric.Task == task {
			return metric
		}
	}
	return experimentsuite.EvidenceMetric{}
}

func formatCalls(metric experimentsuite.EvidenceMetric) string {
	return fmt.Sprintf(
		"%d/%d/%d",
		metric.TotalToolCalls,
		metric.RepoViewToolCalls,
		metric.OtherToolCalls,
	)
}

func formatTokens(metric experimentsuite.EvidenceMetric) string {
	return fmt.Sprintf(
		"%d/%d/%d/%.1f",
		metric.RegularInputTokens,
		metric.CachedInputTokens,
		metric.OutputTokens,
		metric.EffectiveTokens,
	)
}

func formatToolBreakdown(metric experimentsuite.EvidenceMetric) string {
	return "types: " + formatToolStats(metric.ToolTypes) +
		"; operations: " + formatToolStats(metric.Operations)
}

func escapeTable(value string) string {
	value = strings.ReplaceAll(value, "|", `\|`)
	value = strings.ReplaceAll(value, "\n", " ")
	return strings.TrimSpace(value)
}

func printCases(cases []experimentsuite.Case) {
	fmt.Println("LEVEL\tOUTCOME\tLIVE\tCASE\tEVIDENCE")
	for _, testCase := range cases {
		live := "no"
		if testCase.Live != nil {
			live = "yes"
		}
		fmt.Printf(
			"%d\t%s\t%s\t%s\t%s\n",
			testCase.Level,
			testCase.Outcome,
			live,
			testCase.ID,
			testCase.Evidence,
		)
	}
}

func printCaseResult(result caseResult) {
	status := passLabel(result.Passed)
	if result.Skipped {
		status = "SKIP"
	}
	fmt.Printf("%s %s (%s)\n", status, result.ID, result.Outcome)
}

func parseCaseIDs(value string) map[string]bool {
	result := make(map[string]bool)
	for _, id := range strings.Split(value, ",") {
		id = strings.TrimSpace(id)
		if id != "" {
			result[id] = true
		}
	}
	return result
}

func resolve(root, path string) string {
	if filepath.IsAbs(path) {
		return filepath.Clean(path)
	}
	return filepath.Join(root, filepath.FromSlash(path))
}

func allPassed(results []caseResult) bool {
	for _, result := range results {
		if !result.Passed {
			return false
		}
	}
	return true
}

func passLabel(passed bool) string {
	if passed {
		return "PASS"
	}
	return "FAIL"
}

func usage() {
	lines := []string{
		"Usage: repo-view-experiment-suite COMMAND [options]",
		"",
		"Commands:",
		"  list    list simple-to-complex accepted and rejected cases",
		"  replay  regenerate and validate local fixture evidence",
		"  reply   compatibility alias for replay",
		"  resolve verify every retained failed case against current code and evidence",
		"  live    rerun live-enabled accepted cases with Codex and quality judges",
		"  repair  rerun failed cases with staged token and quality promotion gates",
	}
	fmt.Fprintln(os.Stderr, strings.Join(lines, "\n"))
}
