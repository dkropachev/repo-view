package study

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func taskCatalogFixture(t *testing.T) TaskCatalog {
	t.Helper()
	tasks := make([]CatalogTask, 0, taskCatalogTaskCount)
	for _, repository := range lockedCatalogRepositories {
		for _, family := range catalogFamilies {
			for _, tier := range catalogTiers {
				taskID := strings.Join([]string{
					string(repository.language),
					repository.taskSlug,
					string(family),
					string(tier),
				}, ".")
				baseObjectID := catalogFixtureDigest("base:" + taskID)[:40]
				var headObjectID *string
				if family != CatalogFamilyExplain {
					head := catalogFixtureDigest("head:" + taskID)[:40]
					headObjectID = &head
				}
				var goldPatch *CatalogGoldPatch
				if family == CatalogFamilyCode {
					goldPatch = &CatalogGoldPatch{
						PatchSHA256:        catalogFixtureDigest("patch:" + taskID),
						ResultTreeObjectID: catalogFixtureDigest("result:" + taskID)[:40],
					}
				}
				rubric := []RubricItem{}
				if family != CatalogFamilyCode {
					rubric = append(rubric, RubricItem{
						ItemID:        "rubric.evidence",
						Requirement:   "Explains the relevant behavior with source evidence.",
						MaximumPoints: 3,
					})
				}
				commands := []CatalogCommand{{
					CommandID:     "verify",
					Argv:          []string{"./verify", "--task", taskID},
					TimeoutMillis: 60_000,
				}}
				if family == CatalogFamilyCode {
					commands = []CatalogCommand{
						{
							CommandID:     "build",
							Argv:          []string{"./verify", "--phase", "build", "--task", taskID},
							TimeoutMillis: 60_000,
						},
						{
							CommandID:     "fail-to-pass",
							Argv:          []string{"./verify", "--phase", "fail-to-pass", "--task", taskID},
							TimeoutMillis: 60_000,
						},
						{
							CommandID:     "pass-to-pass",
							Argv:          []string{"./verify", "--phase", "pass-to-pass", "--task", taskID},
							TimeoutMillis: 60_000,
						},
					}
				}
				tasks = append(tasks, CatalogTask{
					TaskID:         taskID,
					Language:       repository.language,
					RepoSlug:       repository.taskSlug,
					RepositorySlug: repository.repositorySlug,
					Source: CatalogSource{
						UpstreamURL:      repository.upstreamURL,
						SourceURL:        "https://github.com/yapless/" + repository.repositorySlug,
						BaseObjectID:     baseObjectID,
						HeadObjectID:     headObjectID,
						SourceTreeSHA256: catalogFixtureDigest("tree:" + taskID),
					},
					Family:          family,
					Tier:            tier,
					PromptSHA256:    catalogFixtureDigest("prompt:" + taskID),
					ToolchainSHA256: catalogFixtureDigest("toolchain:" + taskID),
					Commands:        commands,
					Ceilings:        catalogTierCeilings(tier),
					Facts: []FactItem{{
						ItemID:        "fact.behavior",
						Requirement:   "States the expected repository behavior.",
						Expected:      "The expected repository behavior is present.",
						MaximumPoints: 2,
					}},
					Rubric:                      rubric,
					HiddenEvaluatorBundleSHA256: catalogFixtureDigest("evaluator:" + taskID),
					GoldPatch:                   goldPatch,
					Exclusions: []CatalogExclusion{{
						Code:      "dependency_unavailable",
						Condition: "A pinned build dependency is unavailable from the immutable environment.",
					}},
				})
			}
		}
	}
	sort.Slice(tasks, func(left, right int) bool { return tasks[left].TaskID < tasks[right].TaskID })
	catalog := TaskCatalog{
		SchemaVersion: TaskCatalogSchemaVersion,
		CatalogID:     "scopesifter-confirmatory",
		Tasks:         tasks,
	}
	if err := catalog.Validate(); err != nil {
		t.Fatalf("catalog fixture: %v", err)
	}
	return catalog
}

func catalogFixtureDigest(value string) string {
	digest := sha256.Sum256([]byte(value))
	return hex.EncodeToString(digest[:])
}

func cloneTaskCatalog(t *testing.T, catalog TaskCatalog) TaskCatalog {
	t.Helper()
	raw, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	var clone TaskCatalog
	if err := json.Unmarshal(raw, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func findCatalogTask(t *testing.T, catalog *TaskCatalog, family CatalogTaskFamily) *CatalogTask {
	t.Helper()
	for index := range catalog.Tasks {
		if catalog.Tasks[index].Family == family {
			return &catalog.Tasks[index]
		}
	}
	t.Fatalf("fixture has no %s task", family)
	return nil
}

func TestTaskCatalogCanonicalRoundTrip(t *testing.T) {
	catalog := taskCatalogFixture(t)
	raw, err := EncodeTaskCatalog(catalog)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeTaskCatalog(raw)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, catalog) {
		t.Fatal("catalog changed during canonical round trip")
	}
	// A v1 field or list-order change must deliberately update this wire golden.
	const wantDigest = "3a225ac3087e1ef1e899cd6651424dfc2d1ab9da5cb550d9f0b9a7c0b77d60a8"
	if rawDigest := catalogFixtureDigest(string(raw)); rawDigest != wantDigest {
		t.Fatalf("encoded catalog digest = %q, want %q", rawDigest, wantDigest)
	}
	gotDigest, err := catalog.SHA256()
	if err != nil {
		t.Fatal(err)
	}
	if gotDigest != wantDigest {
		t.Fatalf("catalog digest = %q, want %q", gotDigest, wantDigest)
	}
	if len(decoded.Tasks) != taskCatalogTaskCount {
		t.Fatalf("decoded %d tasks", len(decoded.Tasks))
	}
}

func TestDecodeTaskCatalogRejectsNoncanonicalJSON(t *testing.T) {
	raw, err := EncodeTaskCatalog(taskCatalogFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	var indented bytes.Buffer
	if err := json.Indent(&indented, raw, "", "  "); err != nil {
		t.Fatal(err)
	}
	cases := map[string][]byte{
		"unknown field":    append([]byte(`{"unknown":true,`), raw[1:]...),
		"duplicate field":  append([]byte(`{"schema_version":"tokenbench.task-catalog/v1",`), raw[1:]...),
		"trailing value":   append(append([]byte(nil), raw...), []byte(`{}`)...),
		"trailing newline": append(append([]byte(nil), raw...), '\n'),
		"noncanonical":     indented.Bytes(),
		"invalid utf8":     append(append([]byte(nil), raw...), 0xff),
	}
	for name, candidate := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeTaskCatalog(candidate); err == nil {
				t.Fatal("invalid catalog JSON was accepted")
			}
		})
	}
	if _, err := DecodeTaskCatalog(bytes.Repeat([]byte{' '}, maxTaskCatalogBytes+1)); err == nil {
		t.Fatal("oversized task catalog was accepted")
	}
}

func TestDecodeTaskCatalogRequiresEveryField(t *testing.T) {
	raw, err := EncodeTaskCatalog(taskCatalogFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]json.RawMessage
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	delete(document, "catalog_id")
	missing, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeTaskCatalog(missing); err == nil {
		t.Fatal("catalog with a missing required field was accepted")
	}
	document["catalog_id"] = json.RawMessage("null")
	nullValue, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeTaskCatalog(nullValue); err == nil {
		t.Fatal("catalog with a null required field was accepted")
	}
}

func TestTaskCatalogValidationBoundsCanonicalBytes(t *testing.T) {
	catalog := taskCatalogFixture(t)
	largeArgument := strings.Repeat("x", maxCatalogCommandArgument)
	modifiedTasks := 0
	for taskIndex := range catalog.Tasks {
		if catalog.Tasks[taskIndex].Family == CatalogFamilyCode {
			continue
		}
		commands := make([]CatalogCommand, 0, maxCatalogCommands)
		for commandIndex := range maxCatalogCommands {
			arguments := make([]string, 16)
			for argumentIndex := range arguments {
				arguments[argumentIndex] = largeArgument
			}
			commands = append(commands, CatalogCommand{
				CommandID:     "command-" + twoDigits(commandIndex),
				Argv:          arguments,
				TimeoutMillis: 60_000,
			})
		}
		catalog.Tasks[taskIndex].Commands = commands
		modifiedTasks++
		if modifiedTasks == 18 {
			break
		}
	}
	if err := catalog.Validate(); err == nil || !strings.Contains(err.Error(), "canonical bytes") {
		t.Fatalf("oversized in-memory catalog was not rejected: %v", err)
	}
}

func TestTaskCatalogRejectsCorpusAndDistributionDrift(t *testing.T) {
	valid := taskCatalogFixture(t)
	cases := map[string]func(*TaskCatalog){
		"schema":     func(value *TaskCatalog) { value.SchemaVersion = "tokenbench.task-catalog/v2" },
		"catalog id": func(value *TaskCatalog) { value.CatalogID = "Scope Sifter" },
		"task count": func(value *TaskCatalog) { value.Tasks = value.Tasks[:len(value.Tasks)-1] },
		"task order": func(value *TaskCatalog) { value.Tasks[0], value.Tasks[1] = value.Tasks[1], value.Tasks[0] },
		"duplicate task": func(value *TaskCatalog) {
			value.Tasks[len(value.Tasks)-1] = value.Tasks[0]
			sort.Slice(value.Tasks, func(left, right int) bool { return value.Tasks[left].TaskID < value.Tasks[right].TaskID })
		},
		"language": func(value *TaskCatalog) {
			value.Tasks[0].Language = "ruby"
			value.Tasks[0].TaskID = "ruby.fmt.code.huge"
			sort.Slice(value.Tasks, func(left, right int) bool { return value.Tasks[left].TaskID < value.Tasks[right].TaskID })
		},
		"repo slug":       func(value *TaskCatalog) { value.Tasks[0].RepoSlug = "other" },
		"repository slug": func(value *TaskCatalog) { value.Tasks[0].RepositorySlug = "corpus-cpp-other" },
		"family":          func(value *TaskCatalog) { value.Tasks[0].Family = "locate" },
		"tier":            func(value *TaskCatalog) { value.Tasks[0].Tier = "enormous" },
		"task id":         func(value *TaskCatalog) { value.Tasks[0].TaskID += ".extra" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			catalog := cloneTaskCatalog(t, valid)
			mutate(&catalog)
			if err := catalog.Validate(); err == nil {
				t.Fatal("invalid catalog was accepted")
			}
		})
	}
}

func TestTaskCatalogRejectsProvenanceAndDigestDrift(t *testing.T) {
	valid := taskCatalogFixture(t)
	cases := map[string]func(*CatalogTask){
		"upstream":                func(task *CatalogTask) { task.Source.UpstreamURL = "https://github.com/example/project" },
		"source":                  func(task *CatalogTask) { task.Source.SourceURL = "https://github.com/yapless/other" },
		"missing comparison head": func(task *CatalogTask) { task.Source.HeadObjectID = nil },
		"base object":             func(task *CatalogTask) { task.Source.BaseObjectID = strings.Repeat("g", 40) },
		"head object": func(task *CatalogTask) {
			invalid := strings.Repeat("f", 39)
			task.Source.HeadObjectID = &invalid
		},
		"mixed object formats": func(task *CatalogTask) {
			head := catalogFixtureDigest("sha256 head")
			task.Source.HeadObjectID = &head
		},
		"equal revisions": func(task *CatalogTask) {
			task.Source.HeadObjectID = &task.Source.BaseObjectID
		},
		"source tree": func(task *CatalogTask) { task.Source.SourceTreeSHA256 = "bad" },
		"prompt":      func(task *CatalogTask) { task.PromptSHA256 = "bad" },
		"toolchain":   func(task *CatalogTask) { task.ToolchainSHA256 = "bad" },
		"evaluator":   func(task *CatalogTask) { task.HiddenEvaluatorBundleSHA256 = "bad" },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			catalog := cloneTaskCatalog(t, valid)
			mutate(findCatalogTask(t, &catalog, CatalogFamilyReview))
			if err := catalog.Validate(); err == nil {
				t.Fatal("invalid provenance was accepted")
			}
		})
	}
}

func TestTaskCatalogRejectsCommandCeilingAndQualityDrift(t *testing.T) {
	valid := taskCatalogFixture(t)
	cases := map[string]func(*CatalogTask){
		"nil commands":         func(task *CatalogTask) { task.Commands = nil },
		"duplicate command":    func(task *CatalogTask) { task.Commands = append(task.Commands, task.Commands[0]) },
		"empty argv":           func(task *CatalogTask) { task.Commands[0].Argv = nil },
		"nul argv":             func(task *CatalogTask) { task.Commands[0].Argv[0] = "bad\x00argument" },
		"timeout":              func(task *CatalogTask) { task.Commands[0].TimeoutMillis = 0 },
		"ceilings":             func(task *CatalogTask) { task.Ceilings.ChangeLineCeiling++ },
		"nil facts":            func(task *CatalogTask) { task.Facts = nil },
		"nil rubric":           func(task *CatalogTask) { task.Rubric = nil },
		"duplicate quality id": func(task *CatalogTask) { task.Rubric[0].ItemID = task.Facts[0].ItemID },
		"unblindable fact":     func(task *CatalogTask) { task.Facts[0].Requirement = "Compares the candidate result." },
		"fact points":          func(task *CatalogTask) { task.Facts[0].MaximumPoints = 0 },
		"nil exclusions":       func(task *CatalogTask) { task.Exclusions = nil },
		"duplicate exclusion":  func(task *CatalogTask) { task.Exclusions = append(task.Exclusions, task.Exclusions[0]) },
		"empty exclusion":      func(task *CatalogTask) { task.Exclusions[0].Condition = "" },
		"biased exclusion": func(task *CatalogTask) {
			task.Exclusions[0].Condition = "The candidate answer is shorter."
		},
		"biased exclusion code": func(task *CatalogTask) {
			task.Exclusions[0].Code = "candidate_has_more_tokens"
		},
		"arm order exclusion": func(task *CatalogTask) {
			task.Exclusions[0].Condition = "Exclude after the second arm order."
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			catalog := cloneTaskCatalog(t, valid)
			mutate(findCatalogTask(t, &catalog, CatalogFamilyReview))
			if err := catalog.Validate(); err == nil {
				t.Fatal("invalid task details were accepted")
			}
		})
	}
}

func TestTaskCatalogCodeCommandContract(t *testing.T) {
	valid := taskCatalogFixture(t)
	for _, requiredID := range []string{"build", "fail-to-pass", "pass-to-pass"} {
		t.Run("missing "+requiredID, func(t *testing.T) {
			catalog := cloneTaskCatalog(t, valid)
			code := findCatalogTask(t, &catalog, CatalogFamilyCode)
			for index, command := range code.Commands {
				if command.CommandID == requiredID {
					code.Commands = append(code.Commands[:index], code.Commands[index+1:]...)
					break
				}
			}
			if err := catalog.Validate(); err == nil {
				t.Fatalf("code task without %q was accepted", requiredID)
			}
		})
	}
	t.Run("near-name alias", func(t *testing.T) {
		catalog := cloneTaskCatalog(t, valid)
		code := findCatalogTask(t, &catalog, CatalogFamilyCode)
		code.Commands[1].CommandID = "fail_to_pass"
		if err := catalog.Validate(); err == nil {
			t.Fatal("noncanonical fail-to-pass command alias was accepted")
		}
	})
	t.Run("additional command", func(t *testing.T) {
		catalog := cloneTaskCatalog(t, valid)
		code := findCatalogTask(t, &catalog, CatalogFamilyCode)
		code.Commands = append(code.Commands, CatalogCommand{
			CommandID:     "verify",
			Argv:          []string{"./verify", "--task", code.TaskID},
			TimeoutMillis: 60_000,
		})
		if err := catalog.Validate(); err != nil {
			t.Fatalf("sorted additional code command was rejected: %v", err)
		}
	})
}

func TestTaskCatalogExclusionsDistinguishArchitectureFromTreatmentArm(t *testing.T) {
	catalog := taskCatalogFixture(t)
	task := findCatalogTask(t, &catalog, CatalogFamilyReview)
	task.Exclusions[0] = CatalogExclusion{
		Code:      "linux_arm64_builder_unavailable",
		Condition: "The pinned Linux arm64 builder image is unavailable.",
	}
	if err := catalog.Validate(); err != nil {
		t.Fatalf("arm64 architecture exclusion was rejected as treatment language: %v", err)
	}
}

func TestTaskCatalogGoldPatchIsClosedByFamily(t *testing.T) {
	valid := taskCatalogFixture(t)
	t.Run("missing from code", func(t *testing.T) {
		catalog := cloneTaskCatalog(t, valid)
		findCatalogTask(t, &catalog, CatalogFamilyCode).GoldPatch = nil
		if err := catalog.Validate(); err == nil {
			t.Fatal("code task without a gold patch was accepted")
		}
	})
	t.Run("invalid identity", func(t *testing.T) {
		catalog := cloneTaskCatalog(t, valid)
		findCatalogTask(t, &catalog, CatalogFamilyCode).GoldPatch.PatchSHA256 = "bad"
		if err := catalog.Validate(); err == nil {
			t.Fatal("invalid gold patch was accepted")
		}
	})
	t.Run("mixed result tree format", func(t *testing.T) {
		catalog := cloneTaskCatalog(t, valid)
		findCatalogTask(t, &catalog, CatalogFamilyCode).GoldPatch.ResultTreeObjectID = catalogFixtureDigest("sha256 result tree")
		if err := catalog.Validate(); err == nil {
			t.Fatal("mixed Git object formats were accepted")
		}
	})
	t.Run("present on review", func(t *testing.T) {
		catalog := cloneTaskCatalog(t, valid)
		review := findCatalogTask(t, &catalog, CatalogFamilyReview)
		review.GoldPatch = &CatalogGoldPatch{
			PatchSHA256:        catalogFixtureDigest("unexpected patch"),
			ResultTreeObjectID: catalogFixtureDigest("unexpected tree")[:40],
		}
		if err := catalog.Validate(); err == nil {
			t.Fatal("review task with a gold patch was accepted")
		}
	})
}

func TestTaskCatalogFamilySpecificHeadAndRubricRules(t *testing.T) {
	valid := taskCatalogFixture(t)
	if err := valid.Validate(); err != nil {
		t.Fatalf("code tasks with objective facts and empty rubrics were rejected: %v", err)
	}
	for _, family := range []CatalogTaskFamily{CatalogFamilyCode, CatalogFamilyReview} {
		t.Run(string(family)+" head", func(t *testing.T) {
			catalog := cloneTaskCatalog(t, valid)
			findCatalogTask(t, &catalog, family).Source.HeadObjectID = nil
			if err := catalog.Validate(); err == nil {
				t.Fatal("comparison task without head_object_id was accepted")
			}
		})
	}
	for _, family := range []CatalogTaskFamily{CatalogFamilyReview, CatalogFamilyExplain} {
		t.Run(string(family)+" rubric", func(t *testing.T) {
			catalog := cloneTaskCatalog(t, valid)
			findCatalogTask(t, &catalog, family).Rubric = []RubricItem{}
			if err := catalog.Validate(); err == nil {
				t.Fatal("judged task without rubric items was accepted")
			}
		})
	}
}

func TestTaskCatalogSchemaMatchesClosedGoSurface(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("schemas", "task-catalog-v1.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	type schemaObject struct {
		AdditionalProperties *bool                      `json:"additionalProperties"`
		Required             []string                   `json:"required"`
		Properties           map[string]json.RawMessage `json:"properties"`
	}
	var document struct {
		SchemaVersion string                     `json:"$schema"`
		ID            string                     `json:"$id"`
		Definitions   map[string]json.RawMessage `json:"$defs"`
		schemaObject
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		t.Fatal(err)
	}
	if document.SchemaVersion != "https://json-schema.org/draft/2020-12/schema" {
		t.Fatalf("unexpected JSON Schema version %q", document.SchemaVersion)
	}
	if document.ID != "https://github.com/yapless/scopesifter/benchmarks/tokenbench/study/schemas/task-catalog-v1.schema.json" {
		t.Fatalf("unexpected task catalog schema ID %q", document.ID)
	}
	checkClosedSchemaObject(t, "catalog", document.schemaObject)
	for _, name := range []string{"source", "command", "ceilings", "fact", "rubric", "goldPatch", "exclusion", "task"} {
		definition, ok := document.Definitions[name]
		if !ok {
			t.Fatalf("schema is missing %q", name)
		}
		var object schemaObject
		if err := json.Unmarshal(definition, &object); err != nil {
			t.Fatal(err)
		}
		checkClosedSchemaObject(t, name, object)
	}

	var tasksProperty struct {
		Minimum int `json:"minItems"`
		Maximum int `json:"maxItems"`
	}
	if err := json.Unmarshal(document.Properties["tasks"], &tasksProperty); err != nil {
		t.Fatal(err)
	}
	if tasksProperty.Minimum != taskCatalogTaskCount || tasksProperty.Maximum != taskCatalogTaskCount {
		t.Fatalf("schema task count = %d..%d", tasksProperty.Minimum, tasksProperty.Maximum)
	}
}

func checkClosedSchemaObject(t *testing.T, name string, object struct {
	AdditionalProperties *bool                      `json:"additionalProperties"`
	Required             []string                   `json:"required"`
	Properties           map[string]json.RawMessage `json:"properties"`
}) {
	t.Helper()
	if object.AdditionalProperties == nil || *object.AdditionalProperties {
		t.Fatalf("schema object %s permits additional properties", name)
	}
	required := append([]string(nil), object.Required...)
	properties := make([]string, 0, len(object.Properties))
	for property := range object.Properties {
		properties = append(properties, property)
	}
	sort.Strings(required)
	sort.Strings(properties)
	if !reflect.DeepEqual(required, properties) {
		t.Fatalf("schema object %s required=%v properties=%v", name, required, properties)
	}
}
