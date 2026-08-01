package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dkropachev/repo-view/internal/experimentsuite"
)

func TestValidateGoTestExecution(t *testing.T) {
	t.Run("all literal alternatives ran", func(t *testing.T) {
		output := []byte(
			"{\"Action\":\"run\",\"Test\":\"TestFirst\"}\n" +
				"{\"Action\":\"run\",\"Test\":\"TestSecond\"}\n" +
				"{\"Action\":\"pass\",\"Test\":\"TestFirst\"}\n" +
				"{\"Action\":\"pass\",\"Test\":\"TestSecond\"}\n",
		)
		if err := validateGoTestExecution("TestFirst|TestSecond", output); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("no tests ran", func(t *testing.T) {
		output := []byte(
			"{\"Action\":\"run\",\"Package\":\"example.com/test\"}\n" +
				"{\"Action\":\"pass\",\"Package\":\"example.com/test\"}\n",
		)
		err := validateGoTestExecution("TestRenamed", output)
		if err == nil || !strings.Contains(err.Error(), "matched no tests") {
			t.Fatalf("validateGoTestExecution() error = %v, want no-tests error", err)
		}
	})

	t.Run("one literal alternative is stale", func(t *testing.T) {
		output := []byte(
			"{\"Action\":\"run\",\"Test\":\"TestCurrent\"}\n" +
				"{\"Action\":\"pass\",\"Test\":\"TestCurrent\"}\n",
		)
		err := validateGoTestExecution("TestCurrent|TestRenamed", output)
		if err == nil || !strings.Contains(err.Error(), "did not execute: TestRenamed") {
			t.Fatalf("validateGoTestExecution() error = %v, want stale-test error", err)
		}
	})

	t.Run("repeated literal alternative does not hide a stale alternative", func(t *testing.T) {
		output := []byte(
			"{\"Action\":\"run\",\"Test\":\"TestCurrent\"}\n" +
				"{\"Action\":\"pass\",\"Test\":\"TestCurrent\"}\n",
		)
		err := validateGoTestExecution(
			"TestCurrent|TestCurrent|TestRenamed",
			output,
		)
		if err == nil || !strings.Contains(err.Error(), "did not execute: TestRenamed") {
			t.Fatalf("validateGoTestExecution() error = %v, want stale-test error", err)
		}
	})

	t.Run("unicode literal alternative is checked", func(t *testing.T) {
		output := []byte(
			"{\"Action\":\"run\",\"Test\":\"TestCurrent\"}\n" +
				"{\"Action\":\"pass\",\"Test\":\"TestCurrent\"}\n",
		)
		err := validateGoTestExecution("TestCurrent|TestRenamé", output)
		if err == nil || !strings.Contains(err.Error(), "did not execute: TestRenamé") {
			t.Fatalf("validateGoTestExecution() error = %v, want Unicode stale-test error", err)
		}
	})

	t.Run("empty literal alternative is rejected", func(t *testing.T) {
		output := []byte("{\"Action\":\"run\",\"Test\":\"TestCurrent\"}\n")
		err := validateGoTestExecution("TestCurrent|", output)
		if err == nil || !strings.Contains(err.Error(), "empty alternative") {
			t.Fatalf("validateGoTestExecution() error = %v, want empty-alternative error", err)
		}
	})

	t.Run("subtest ran", func(t *testing.T) {
		output := []byte(
			"{\"Action\":\"run\",\"Test\":\"TestParent\"}\n" +
				"{\"Action\":\"run\",\"Test\":\"TestParent/current\"}\n" +
				"{\"Action\":\"pass\",\"Test\":\"TestParent/current\"}\n",
		)
		if err := validateGoTestExecution("TestParent/current", output); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("parent run does not satisfy stale subtest", func(t *testing.T) {
		output := []byte("{\"Action\":\"run\",\"Test\":\"TestParent\"}\n")
		err := validateGoTestExecution("TestParent/renamed", output)
		if err == nil || !strings.Contains(err.Error(), "matched no tests") {
			t.Fatalf("validateGoTestExecution() error = %v, want stale-subtest error", err)
		}
	})

	t.Run("literal subtest alternatives are all checked", func(t *testing.T) {
		output := []byte(
			"{\"Action\":\"run\",\"Test\":\"TestParent\"}\n" +
				"{\"Action\":\"run\",\"Test\":\"TestParent/current\"}\n" +
				"{\"Action\":\"pass\",\"Test\":\"TestParent/current\"}\n",
		)
		err := validateGoTestExecution(
			"TestParent/current|TestParent/renamed",
			output,
		)
		if err == nil ||
			!strings.Contains(err.Error(), "did not execute: TestParent/renamed") {
			t.Fatalf("validateGoTestExecution() error = %v, want stale-subtest error", err)
		}
	})

	t.Run("regular expression matched a test", func(t *testing.T) {
		output := []byte(
			"{\"Action\":\"run\",\"Test\":\"TestCurrent\"}\n" +
				"{\"Action\":\"pass\",\"Test\":\"TestCurrent\"}\n",
		)
		if err := validateGoTestExecution("Test.*", output); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("mixed regular expression does not hide stale literal", func(t *testing.T) {
		output := []byte(
			"{\"Action\":\"run\",\"Test\":\"TestCurrent\"}\n" +
				"{\"Action\":\"pass\",\"Test\":\"TestCurrent\"}\n",
		)
		err := validateGoTestExecution("Test.*|TestRenamed", output)
		if err == nil || !strings.Contains(err.Error(), "did not execute: TestRenamed") {
			t.Fatalf("validateGoTestExecution() error = %v, want stale-test error", err)
		}
	})

	t.Run("each regular expression alternative must match", func(t *testing.T) {
		output := []byte(
			"{\"Action\":\"run\",\"Test\":\"TestCurrent\"}\n" +
				"{\"Action\":\"pass\",\"Test\":\"TestCurrent\"}\n",
		)
		err := validateGoTestExecution("TestCurrent.*|TestRenamed.*", output)
		if err == nil ||
			!strings.Contains(err.Error(), "did not execute: TestRenamed.*") {
			t.Fatalf("validateGoTestExecution() error = %v, want stale-pattern error", err)
		}
	})

	t.Run("escaped slash cannot borrow split path match", func(t *testing.T) {
		output := []byte(
			"{\"Action\":\"run\",\"Test\":\"TestParent/child\"}\n" +
				"{\"Action\":\"pass\",\"Test\":\"TestParent/child\"}\n",
		)
		err := validateGoTestExecution(
			`TestParent\/child|TestParent/child`,
			output,
		)
		if err == nil ||
			!strings.Contains(err.Error(), `did not execute: TestParent\/child`) {
			t.Fatalf("validateGoTestExecution() error = %v, want escaped-path error", err)
		}
	})

	t.Run("skipped literal test does not pass validation", func(t *testing.T) {
		output := []byte(
			"{\"Action\":\"run\",\"Test\":\"TestSkipped\"}\n" +
				"{\"Action\":\"skip\",\"Test\":\"TestSkipped\"}\n",
		)
		err := validateGoTestExecution("TestSkipped", output)
		if err == nil || !strings.Contains(err.Error(), "did not pass: TestSkipped") {
			t.Fatalf("validateGoTestExecution() error = %v, want skipped-test error", err)
		}
	})
}

func TestParseGoTestFilter(t *testing.T) {
	testCases := []struct {
		name string
		run  string
		want string
	}{
		{
			name: "top-level alternatives",
			run:  "TestA|TestB",
			want: "^(?:TestA)$|^(?:TestB)$",
		},
		{
			name: "path alternatives",
			run:  "TestA/sub|TestB/leaf",
			want: "^(?:TestA)$/^(?:sub)$|^(?:TestB)$/^(?:leaf)$",
		},
		{
			name: "component alternatives",
			run:  "TestA/(sub|leaf)",
			want: "^(?:TestA)$/^(?:(sub|leaf))$",
		},
		{
			name: "slash in character class",
			run:  "TestA/[a/b]",
			want: "^(?:TestA)$/^(?:[a/b])$",
		},
		{
			name: "escaped slash",
			run:  `TestA\/sub`,
			want: `^(?:TestA\/sub)$`,
		},
		{
			name: "empty child wildcard",
			run:  "TestA/",
			want: "^(?:TestA)$/^(?:.*)$",
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			filter, err := parseGoTestFilter(testCase.run)
			if err != nil {
				t.Fatal(err)
			}
			if got := filter.exactPattern(); got != testCase.want {
				t.Fatalf("exact pattern = %q, want %q", got, testCase.want)
			}
		})
	}

	for _, run := range []string{"", "|TestA", "TestA||TestB", "TestA|"} {
		t.Run("reject empty "+run, func(t *testing.T) {
			if _, err := parseGoTestFilter(run); err == nil ||
				!strings.Contains(err.Error(), "empty alternative") {
				t.Fatalf("parseGoTestFilter(%q) error = %v, want empty-alternative error", run, err)
			}
		})
	}

	t.Run("unbalanced grouping cannot escape exact wrapper", func(t *testing.T) {
		if _, err := parseGoTestFilter("TestA)|TestB("); err == nil ||
			!strings.Contains(err.Error(), "compile go test selector") {
			t.Fatalf("parseGoTestFilter() error = %v, want regexp error", err)
		}
	})
}

func TestRunGoTestRejectsStaleSelectorsAndPreservesLog(t *testing.T) {
	workspaceRoot := t.TempDir()
	writeTestFile(
		t,
		workspaceRoot,
		"go.mod",
		"module example.com/unrelated\n\ngo 1.26\n",
	)
	writeTestFile(
		t,
		workspaceRoot,
		"go.work",
		"go 1.26\n\nuse .\n",
	)
	t.Setenv("GOWORK", filepath.Join(workspaceRoot, "go.work"))
	t.Setenv("GOFLAGS", "-short")

	repoRoot := t.TempDir()
	writeTestFile(t, repoRoot, "go.mod", "module example.com/selection\n\ngo 1.26\n")
	writeTestFile(
		t,
		repoRoot,
		"selection_test.go",
		"package selection\n\n"+
			"import \"testing\"\n\n"+
			"func TestCurrent(t *testing.T) {}\n\n"+
			"func TestCurrentExtra(t *testing.T) { t.Fatal(\"prefix match ran\") }\n\n"+
			"func TestÉxisting(t *testing.T) {}\n\n"+
			"func TestSkipped(t *testing.T) { t.Skip(\"fixture\") }\n\n"+
			"func TestShortSensitive(t *testing.T) {\n"+
			"\tif testing.Short() { t.Skip(\"ambient GOFLAGS leaked\") }\n"+
			"}\n\n"+
			"func TestParent(t *testing.T) {\n"+
			"\tt.Run(\"current\", func(t *testing.T) {\n"+
			"\t\tt.Run(\"leaf\", func(t *testing.T) {})\n"+
			"\t})\n"+
			"\tt.Run(\"with space\", func(t *testing.T) {})\n"+
			"}\n",
	)

	testCases := []struct {
		name      string
		run       string
		wantPass  bool
		wantError string
		wantLog   string
	}{
		{
			name:     "top-level match",
			run:      "TestCurrent",
			wantPass: true,
			wantLog:  `"Test":"TestCurrent"`,
		},
		{
			name:     "unicode top-level match",
			run:      "TestÉxisting",
			wantPass: true,
			wantLog:  `"Test":"TestÉxisting"`,
		},
		{
			name:      "no matches",
			run:       "TestRenamed",
			wantError: "matched no tests",
			wantLog:   "no tests to run",
		},
		{
			name:      "skipped test",
			run:       "TestSkipped",
			wantError: "did not pass: TestSkipped",
			wantLog:   `"Action":"skip"`,
		},
		{
			name:     "ambient goflags ignored",
			run:      "TestShortSensitive",
			wantPass: true,
			wantLog:  "--- PASS: TestShortSensitive",
		},
		{
			name:      "one stale alternative",
			run:       "TestCurrent|TestRenamed",
			wantError: "did not execute: TestRenamed",
			wantLog:   `"Test":"TestCurrent"`,
		},
		{
			name:      "empty alternative",
			run:       "TestCurrent|",
			wantError: "empty alternative",
			wantLog:   "empty alternative",
		},
		{
			name:     "subtest match",
			run:      "TestParent/current",
			wantPass: true,
			wantLog:  `"Test":"TestParent/current"`,
		},
		{
			name:     "nested subtest match",
			run:      "TestParent/current/leaf",
			wantPass: true,
			wantLog:  `"Test":"TestParent/current/leaf"`,
		},
		{
			name:     "rewritten subtest name match",
			run:      "TestParent/with space",
			wantPass: true,
			wantLog:  `"Test":"TestParent/with_space"`,
		},
		{
			name:     "subtest component regular expression",
			run:      "TestParent/(current|with_space)",
			wantPass: true,
			wantLog:  `"Test":"TestParent/with_space"`,
		},
		{
			name:     "empty subtest component wildcard",
			run:      "TestParent/",
			wantPass: true,
			wantLog:  `"Test":"TestParent/current"`,
		},
		{
			name:      "stale subtest",
			run:       "TestParent/renamed",
			wantError: "matched no tests",
			wantLog:   `"Test":"TestParent"`,
		},
		{
			name:      "one stale subtest alternative",
			run:       "TestParent/current|TestParent/renamed",
			wantError: "did not execute: TestParent/renamed",
			wantLog:   `"Test":"TestParent/current"`,
		},
		{
			name:      "stale nested subtest",
			run:       "TestParent/current/renamed",
			wantError: "matched no tests",
			wantLog:   `"Test":"TestParent/current"`,
		},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			outcome := runGoTest(
				context.Background(),
				options{
					repoRoot:  repoRoot,
					outputDir: filepath.Join(repoRoot, "suite-output"),
				},
				experimentsuite.GoTest{
					Package: ".",
					Run:     testCase.run,
				},
				testCase.name,
			)
			if testCase.wantPass && !outcome.passed {
				t.Fatalf("runGoTest() failed for selector %q: %v", testCase.run, outcome.err)
			}
			if !testCase.wantPass && outcome.passed {
				t.Fatalf("runGoTest() passed with stale selector %q", testCase.run)
			}
			if testCase.wantError != "" &&
				(outcome.err == nil ||
					!strings.Contains(outcome.err.Error(), testCase.wantError)) {
				t.Fatalf(
					"runGoTest() error = %v, want error containing %q",
					outcome.err,
					testCase.wantError,
				)
			}
			logData, err := os.ReadFile(outcome.log)
			if err != nil {
				t.Fatalf("read go test log: %v", err)
			}
			if !strings.Contains(string(logData), testCase.wantLog) {
				t.Fatalf("go test log does not contain %q:\n%s", testCase.wantLog, logData)
			}
		})
	}
}

func TestWriteResultsRecordsEvidencePreparationModes(t *testing.T) {
	for _, command := range []string{"replay", "resolve"} {
		t.Run(command, func(t *testing.T) {
			outputDir := t.TempDir()
			result := suiteResult{
				SchemaVersion:      1,
				Command:            command,
				EvidenceAnalysis:   "reused",
				QualityAggregation: "executed",
				Passed:             true,
			}
			if err := writeResults(outputDir, result); err != nil {
				t.Fatal(err)
			}
			content, err := os.ReadFile(filepath.Join(outputDir, "summary.md"))
			if err != nil {
				t.Fatal(err)
			}
			for _, line := range []string{
				"- Evidence analysis: `reused`",
				"- Quality aggregation: `executed`",
			} {
				if !strings.Contains(string(content), line) {
					t.Errorf("summary missing %q:\n%s", line, content)
				}
			}
		})
	}
}

func TestReplayRecordsNotRunForMissingAllowedEvidence(t *testing.T) {
	repoRoot := t.TempDir()
	manifestPath := filepath.Join(repoRoot, "cases.json")
	manifest := experimentsuite.Manifest{
		SchemaVersion: 1,
		Cases: []experimentsuite.Case{{
			ID:                     "missing",
			Level:                  1,
			Complexity:             "fixture",
			Outcome:                "accepted",
			Evidence:               "missing/run",
			SourceChecksumSHA256:   strings.Repeat("1", 64),
			QualityAggregateSHA256: strings.Repeat("1", 64),
			Assertions: []experimentsuite.Assertion{{
				Description: "unreached",
				Source:      "metrics.case",
				Field:       "completed",
				Operator:    "eq",
				Value:       true,
			}},
		}},
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(manifestPath, data, 0o644); err != nil {
		t.Fatal(err)
	}
	outputDir := filepath.Join(repoRoot, "output")
	status := run(context.Background(), []string{
		"replay",
		"--repo-root", repoRoot,
		"--manifest", manifestPath,
		"--evidence-root", filepath.Join(repoRoot, "evidence"),
		"--output", outputDir,
		"--allow-missing",
	})
	if status != 0 {
		t.Fatalf("run() status = %d, want 0", status)
	}
	var result suiteResult
	content, err := os.ReadFile(filepath.Join(outputDir, "results.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(content, &result); err != nil {
		t.Fatal(err)
	}
	if result.EvidenceAnalysis != evidenceStageNotRun ||
		result.QualityAggregation != evidenceStageNotRun {
		t.Fatalf(
			"preparation modes = %q/%q, want not run/not run",
			result.EvidenceAnalysis,
			result.QualityAggregation,
		)
	}
}

func TestPrepareOutputDirRejectsExistingAndSymlinkedPaths(t *testing.T) {
	root := t.TempDir()
	existing := filepath.Join(root, "existing")
	if err := os.Mkdir(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := prepareOutputDir(existing); err == nil ||
		!strings.Contains(err.Error(), "already exists") {
		t.Fatalf("prepareOutputDir(existing) error = %v", err)
	}

	external := t.TempDir()
	link := filepath.Join(root, "linked-parent")
	if err := os.Symlink(external, link); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := prepareOutputDir(filepath.Join(link, "output")); err == nil ||
		!strings.Contains(err.Error(), "non-directory component") {
		t.Fatalf("prepareOutputDir(symlink) error = %v", err)
	}
}

func TestWriteFileAtomicDoesNotFollowDestinationSymlink(t *testing.T) {
	directory := t.TempDir()
	external := filepath.Join(t.TempDir(), "external.txt")
	if err := os.WriteFile(external, []byte("unchanged"), 0o644); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(directory, "result.txt")
	if err := os.Symlink(external, destination); err != nil {
		t.Skipf("symlinks unavailable: %v", err)
	}
	if err := writeFileAtomic(destination, []byte("published"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(destination)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "published" {
		t.Fatalf("destination = %q", got)
	}
	got, err = os.ReadFile(external)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "unchanged" {
		t.Fatalf("external target changed: %q", got)
	}
}

func TestEvidencePreparationTrackerReportsMixedStages(t *testing.T) {
	var tracker evidencePreparationTracker
	tracker.record("executed", evidencePreparationOutcome{
		analysis: evidenceStageExecuted,
		quality:  evidenceStageNotRun,
	})
	tracker.record("reused", evidencePreparationOutcome{
		analysis: evidenceStageReused,
		quality:  evidenceStageNotRun,
	})
	analysis, quality := tracker.modes()
	if analysis != "mixed" || quality != evidenceStageNotRun {
		t.Fatalf("preparation modes = %q/%q, want mixed/not run", analysis, quality)
	}
}

func TestRecordedStrictJudgeRepeats(t *testing.T) {
	runDir := t.TempDir()
	qualityDir := filepath.Join(runDir, "quality")
	if err := os.Mkdir(qualityDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeTestFile(t, qualityDir, "quality.json", `{"required_judge_count":2}`)
	got, err := recordedStrictJudgeRepeats(runDir)
	if err != nil {
		t.Fatal(err)
	}
	if got != 2 {
		t.Fatalf("judge repeats = %d, want 2", got)
	}

	writeTestFile(t, qualityDir, "quality.json", `{"required_judge_count":0}`)
	if _, err := recordedStrictJudgeRepeats(runDir); err == nil ||
		!strings.Contains(err.Error(), "invalid required judge count") {
		t.Fatalf("invalid judge count error = %v", err)
	}
}

func writeTestFile(t *testing.T, dir, name, contents string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(contents), 0o644); err != nil {
		t.Fatal(err)
	}
}
