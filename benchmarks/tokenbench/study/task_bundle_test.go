package study

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/dkropachev/repo-view/benchmarks/tokenbench/cas"
)

func decodedTaskCatalogFixture(t *testing.T) TaskCatalog {
	t.Helper()
	raw, err := EncodeTaskCatalog(taskCatalogFixture(t))
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := DecodeTaskCatalog(raw)
	if err != nil {
		t.Fatal(err)
	}
	return catalog
}

func taskBundleFixture(t *testing.T, catalog TaskCatalog) TaskBundle {
	t.Helper()
	catalogSHA256, err := catalog.SHA256()
	if err != nil {
		t.Fatal(err)
	}
	bundle := TaskBundle{
		SchemaVersion: TaskBundleSchemaVersion,
		CatalogSHA256: catalogSHA256,
		Tasks:         make([]TaskBundleTask, 0, len(catalog.Tasks)),
	}
	for _, task := range catalog.Tasks {
		artifacts := make([]TaskArtifact, 0, len(taskBundleRoles(task.Family)))
		for _, role := range taskBundleRoles(task.Family) {
			digest, mediaType, _ := taskArtifactContract(task, role)
			artifacts = append(artifacts, TaskArtifact{
				Role: role,
				Object: cas.ObjectRef{
					Digest:    "sha256:" + digest,
					Size:      taskArtifactFixtureSize(role),
					MediaType: mediaType,
				},
			})
		}
		bundle.Tasks = append(bundle.Tasks, TaskBundleTask{
			TaskID:    task.TaskID,
			Artifacts: artifacts,
		})
	}
	if err := bundle.Validate(catalog); err != nil {
		t.Fatalf("task bundle fixture: %v", err)
	}
	return bundle
}

func taskArtifactFixtureSize(role TaskArtifactRole) int64 {
	switch role {
	case TaskArtifactGoldPatch:
		return 404
	case TaskArtifactHiddenEvaluatorBundle:
		return 303
	case TaskArtifactPrompt:
		return 101
	case TaskArtifactToolchainManifest:
		return 202
	default:
		return 1
	}
}

func cloneTaskBundle(t *testing.T, bundle TaskBundle) TaskBundle {
	t.Helper()
	raw, err := json.Marshal(bundle)
	if err != nil {
		t.Fatal(err)
	}
	var clone TaskBundle
	if err := json.Unmarshal(raw, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func findTaskBundleTask(
	t *testing.T,
	bundle *TaskBundle,
	catalog TaskCatalog,
	family CatalogTaskFamily,
) *TaskBundleTask {
	t.Helper()
	for index, task := range catalog.Tasks {
		if task.Family == family {
			return &bundle.Tasks[index]
		}
	}
	t.Fatalf("fixture has no %s task", family)
	return nil
}

func findTaskArtifact(t *testing.T, task *TaskBundleTask, role TaskArtifactRole) *TaskArtifact {
	t.Helper()
	for index := range task.Artifacts {
		if task.Artifacts[index].Role == role {
			return &task.Artifacts[index]
		}
	}
	t.Fatalf("task %q has no %s artifact", task.TaskID, role)
	return nil
}

func TestTaskBundleCanonicalCatalogBoundRoundTrip(t *testing.T) {
	catalog := decodedTaskCatalogFixture(t)
	bundle := taskBundleFixture(t, catalog)
	raw, err := EncodeTaskBundle(catalog, bundle)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := DecodeTaskBundle(catalog, raw)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(decoded, bundle) {
		t.Fatal("task bundle changed during canonical round trip")
	}
	if len(decoded.Tasks) != taskCatalogTaskCount {
		t.Fatalf("decoded %d task bundle entries", len(decoded.Tasks))
	}
	artifactCount := 0
	for _, task := range decoded.Tasks {
		artifactCount += len(task.Artifacts)
	}
	if artifactCount != taskBundleArtifactCount {
		t.Fatalf("decoded %d artifact references", artifactCount)
	}
	// A v1 field, role, media type, or ordering change must deliberately update
	// this complete-wire golden.
	const wantSHA256 = "7087201204e9777388afe2acceb534fde553f3c54878c3978a68abeaa58cba52"
	if rawSHA256 := catalogFixtureDigest(string(raw)); rawSHA256 != wantSHA256 {
		t.Fatalf("encoded task bundle SHA-256 = %q, want %q", rawSHA256, wantSHA256)
	}
	gotSHA256, err := bundle.SHA256(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if gotSHA256 != wantSHA256 {
		t.Fatalf("task bundle SHA-256 = %q, want %q", gotSHA256, wantSHA256)
	}
}

func TestDecodeTaskBundleRejectsNoncanonicalAndNullJSON(t *testing.T) {
	catalog := decodedTaskCatalogFixture(t)
	bundle := taskBundleFixture(t, catalog)
	raw, err := EncodeTaskBundle(catalog, bundle)
	if err != nil {
		t.Fatal(err)
	}
	var indented bytes.Buffer
	if err := json.Indent(&indented, raw, "", "  "); err != nil {
		t.Fatal(err)
	}
	nullTasks := cloneTaskBundle(t, bundle)
	nullTasks.Tasks = nil
	nullTasksRaw, err := canonicalJSON(nullTasks)
	if err != nil {
		t.Fatal(err)
	}
	nullArtifacts := cloneTaskBundle(t, bundle)
	nullArtifacts.Tasks[0].Artifacts = nil
	nullArtifactsRaw, err := canonicalJSON(nullArtifacts)
	if err != nil {
		t.Fatal(err)
	}
	objectRaw, err := json.Marshal(bundle.Tasks[0].Artifacts[0].Object)
	if err != nil {
		t.Fatal(err)
	}
	nullObjectRaw := []byte(strings.Replace(
		string(raw),
		`"object":`+string(objectRaw),
		`"object":null`,
		1,
	))
	missingCatalogSHA256Raw := []byte(strings.Replace(
		string(raw),
		`"catalog_sha256":"`+bundle.CatalogSHA256+`",`,
		"",
		1,
	))
	cases := map[string][]byte{
		"unknown field": append([]byte(`{"unknown":true,`), raw[1:]...),
		"duplicate field": append(
			[]byte(`{"schema_version":"tokenbench.task-bundle/v1",`),
			raw[1:]...,
		),
		"trailing value":   append(append([]byte(nil), raw...), []byte(`{}`)...),
		"trailing newline": append(append([]byte(nil), raw...), '\n'),
		"noncanonical":     indented.Bytes(),
		"invalid utf8":     append(append([]byte(nil), raw...), 0xff),
		"null tasks":       nullTasksRaw,
		"null artifacts":   nullArtifactsRaw,
		"null object":      nullObjectRaw,
		"missing field":    missingCatalogSHA256Raw,
	}
	for name, candidate := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := DecodeTaskBundle(catalog, candidate); err == nil {
				t.Fatal("invalid task bundle JSON was accepted")
			}
		})
	}
	if _, err := DecodeTaskBundle(catalog, nil); err == nil {
		t.Fatal("empty task bundle was accepted")
	}
	if _, err := DecodeTaskBundle(
		catalog,
		bytes.Repeat([]byte{' '}, maxTaskBundleBytes+1),
	); err == nil {
		t.Fatal("oversized task bundle was accepted")
	}
}

func TestTaskBundleBindsEveryCatalogArtifactDigest(t *testing.T) {
	catalog := decodedTaskCatalogFixture(t)
	valid := taskBundleFixture(t, catalog)
	roles := []TaskArtifactRole{
		TaskArtifactGoldPatch,
		TaskArtifactHiddenEvaluatorBundle,
		TaskArtifactPrompt,
		TaskArtifactToolchainManifest,
	}
	for _, role := range roles {
		t.Run(string(role), func(t *testing.T) {
			changedCatalog := cloneTaskCatalog(t, catalog)
			changedTask := findCatalogTask(t, &changedCatalog, CatalogFamilyCode)
			changedDigest := catalogFixtureDigest("changed catalog " + string(role))
			switch role {
			case TaskArtifactGoldPatch:
				changedTask.GoldPatch.PatchSHA256 = changedDigest
			case TaskArtifactHiddenEvaluatorBundle:
				changedTask.HiddenEvaluatorBundleSHA256 = changedDigest
			case TaskArtifactPrompt:
				changedTask.PromptSHA256 = changedDigest
			case TaskArtifactToolchainManifest:
				changedTask.ToolchainSHA256 = changedDigest
			default:
				t.Fatalf("unexpected role %q", role)
			}
			changedCatalogSHA256, err := changedCatalog.SHA256()
			if err != nil {
				t.Fatal(err)
			}
			bundle := cloneTaskBundle(t, valid)
			bundle.CatalogSHA256 = changedCatalogSHA256
			if err := bundle.Validate(changedCatalog); err == nil {
				t.Fatal("artifact reference was accepted after its catalog digest changed")
			}
		})
	}
}

func TestTaskBundleRejectsCatalogAndTaskIdentityDrift(t *testing.T) {
	catalog := decodedTaskCatalogFixture(t)
	valid := taskBundleFixture(t, catalog)
	cases := map[string]func(*TaskBundle){
		"schema": func(bundle *TaskBundle) {
			bundle.SchemaVersion = "tokenbench.task-bundle/v2"
		},
		"catalog digest": func(bundle *TaskBundle) {
			bundle.CatalogSHA256 = strings.Repeat("f", 64)
		},
		"missing task": func(bundle *TaskBundle) {
			bundle.Tasks = bundle.Tasks[:len(bundle.Tasks)-1]
		},
		"duplicate task": func(bundle *TaskBundle) {
			bundle.Tasks[len(bundle.Tasks)-1] = bundle.Tasks[0]
		},
		"task order": func(bundle *TaskBundle) {
			bundle.Tasks[0], bundle.Tasks[1] = bundle.Tasks[1], bundle.Tasks[0]
		},
		"wrong task id": func(bundle *TaskBundle) {
			bundle.Tasks[0].TaskID += ".other"
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			bundle := cloneTaskBundle(t, valid)
			mutate(&bundle)
			if err := bundle.Validate(catalog); err == nil {
				t.Fatal("task identity drift was accepted")
			}
		})
	}

	otherCatalog := cloneTaskCatalog(t, catalog)
	otherCatalog.CatalogID = "scopesifter-confirmatory-other"
	if err := otherCatalog.Validate(); err != nil {
		t.Fatal(err)
	}
	if err := valid.Validate(otherCatalog); err == nil {
		t.Fatal("task bundle was accepted against a different valid catalog")
	}

	invalidCatalog := cloneTaskCatalog(t, catalog)
	invalidCatalog.Tasks = invalidCatalog.Tasks[:len(invalidCatalog.Tasks)-1]
	if err := valid.Validate(invalidCatalog); err == nil {
		t.Fatal("task bundle was accepted against an invalid catalog")
	}
}

func TestTaskBundleArtifactRolesAreClosedByFamily(t *testing.T) {
	catalog := decodedTaskCatalogFixture(t)
	valid := taskBundleFixture(t, catalog)
	t.Run("missing gold patch from code", func(t *testing.T) {
		bundle := cloneTaskBundle(t, valid)
		code := findTaskBundleTask(t, &bundle, catalog, CatalogFamilyCode)
		code.Artifacts = code.Artifacts[1:]
		if err := bundle.Validate(catalog); err == nil {
			t.Fatal("code task without gold patch artifact was accepted")
		}
	})
	t.Run("gold patch on review", func(t *testing.T) {
		bundle := cloneTaskBundle(t, valid)
		review := findTaskBundleTask(t, &bundle, catalog, CatalogFamilyReview)
		review.Artifacts = append([]TaskArtifact{{
			Role: TaskArtifactGoldPatch,
			Object: cas.ObjectRef{
				Digest:    "sha256:" + catalogFixtureDigest("unexpected gold patch"),
				Size:      1,
				MediaType: GoldPatchMediaType,
			},
		}}, review.Artifacts...)
		if err := bundle.Validate(catalog); err == nil {
			t.Fatal("review task with gold patch artifact was accepted")
		}
	})
	t.Run("duplicate role", func(t *testing.T) {
		bundle := cloneTaskBundle(t, valid)
		task := &bundle.Tasks[0]
		task.Artifacts[1] = task.Artifacts[0]
		if err := bundle.Validate(catalog); err == nil {
			t.Fatal("duplicate artifact role was accepted")
		}
	})
	t.Run("role order", func(t *testing.T) {
		bundle := cloneTaskBundle(t, valid)
		task := &bundle.Tasks[0]
		task.Artifacts[0], task.Artifacts[1] = task.Artifacts[1], task.Artifacts[0]
		if err := bundle.Validate(catalog); err == nil {
			t.Fatal("unsorted artifact roles were accepted")
		}
	})
}

func TestTaskBundleRejectsArtifactReferenceDrift(t *testing.T) {
	catalog := decodedTaskCatalogFixture(t)
	valid := taskBundleFixture(t, catalog)
	cases := map[string]func(*TaskArtifact){
		"catalog digest": func(artifact *TaskArtifact) {
			artifact.Object.Digest = "sha256:" + strings.Repeat("f", 64)
		},
		"digest algorithm": func(artifact *TaskArtifact) {
			artifact.Object.Digest = "sha512:" + strings.Repeat("f", 64)
		},
		"uppercase digest": func(artifact *TaskArtifact) {
			artifact.Object.Digest = "sha256:" + strings.Repeat("F", 64)
		},
		"zero size": func(artifact *TaskArtifact) {
			artifact.Object.Size = 0
		},
		"oversized prompt": func(artifact *TaskArtifact) {
			artifact.Object.Size = maxTaskPromptBytes + 1
		},
		"wrong media type": func(artifact *TaskArtifact) {
			artifact.Object.MediaType = "text/plain"
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			bundle := cloneTaskBundle(t, valid)
			artifact := findTaskArtifact(t, &bundle.Tasks[0], TaskArtifactPrompt)
			mutate(artifact)
			if err := bundle.Validate(catalog); err == nil {
				t.Fatal("invalid artifact reference was accepted")
			}
		})
	}
}

func TestTaskBundleRejectsConflictingCASIdentityMetadata(t *testing.T) {
	catalog := decodedTaskCatalogFixture(t)
	catalog.Tasks[1].ToolchainSHA256 = catalog.Tasks[0].ToolchainSHA256
	bundle := taskBundleFixture(t, catalog)
	toolchain := findTaskArtifact(t, &bundle.Tasks[1], TaskArtifactToolchainManifest)
	toolchain.Object.Size++
	if err := bundle.Validate(catalog); err == nil {
		t.Fatal("one digest with conflicting CAS metadata was accepted")
	}
}

func TestTaskBundleRejectsCrossRoleObjectAliasing(t *testing.T) {
	catalog := decodedTaskCatalogFixture(t)
	catalog.Tasks[0].HiddenEvaluatorBundleSHA256 = catalog.Tasks[0].PromptSHA256
	bundle := TaskBundle{
		SchemaVersion: TaskBundleSchemaVersion,
		Tasks:         make([]TaskBundleTask, 0, len(catalog.Tasks)),
	}
	catalogSHA256, err := catalog.SHA256()
	if err != nil {
		t.Fatal(err)
	}
	bundle.CatalogSHA256 = catalogSHA256
	for _, task := range catalog.Tasks {
		artifacts := make([]TaskArtifact, 0, len(taskBundleRoles(task.Family)))
		for _, role := range taskBundleRoles(task.Family) {
			digest, mediaType, _ := taskArtifactContract(task, role)
			artifacts = append(artifacts, TaskArtifact{
				Role: role,
				Object: cas.ObjectRef{
					Digest: "sha256:" + digest, Size: 1, MediaType: mediaType,
				},
			})
		}
		bundle.Tasks = append(bundle.Tasks, TaskBundleTask{TaskID: task.TaskID, Artifacts: artifacts})
	}
	if err := bundle.Validate(catalog); err == nil {
		t.Fatal("one object digest aliased across artifact roles was accepted")
	}
}

func TestTaskBundleBoundsAggregateReferencedBytes(t *testing.T) {
	catalog := decodedTaskCatalogFixture(t)
	bundle := taskBundleFixture(t, catalog)
	for taskIndex := range bundle.Tasks {
		artifact := findTaskArtifact(
			t,
			&bundle.Tasks[taskIndex],
			TaskArtifactHiddenEvaluatorBundle,
		)
		artifact.Object.Size = maxHiddenEvaluatorBundleBytes
	}
	if err := bundle.Validate(catalog); err == nil || !strings.Contains(err.Error(), "total bytes") {
		t.Fatalf("oversized aggregate references were not rejected: %v", err)
	}
}

func TestTaskBundleSchemaMatchesClosedGoSurface(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("schemas", "task-bundle-v1.schema.json"))
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
	if document.ID != "https://github.com/scopesifter/scopesifter/benchmarks/tokenbench/study/schemas/task-bundle-v1.schema.json" {
		t.Fatalf("unexpected task bundle schema ID %q", document.ID)
	}
	checkClosedSchemaObject(t, "task bundle", document.schemaObject)
	for _, name := range []string{"objectRef", "artifact", "task"} {
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

	var taskDefinition struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(document.Definitions["task"], &taskDefinition); err != nil {
		t.Fatal(err)
	}
	var artifactsProperty struct {
		Minimum int `json:"minItems"`
		Maximum int `json:"maxItems"`
	}
	if err := json.Unmarshal(taskDefinition.Properties["artifacts"], &artifactsProperty); err != nil {
		t.Fatal(err)
	}
	if artifactsProperty.Minimum != 3 || artifactsProperty.Maximum != 4 {
		t.Fatalf("schema artifact count = %d..%d", artifactsProperty.Minimum, artifactsProperty.Maximum)
	}

	roles := []string{
		string(TaskArtifactGoldPatch),
		string(TaskArtifactHiddenEvaluatorBundle),
		string(TaskArtifactPrompt),
		string(TaskArtifactToolchainManifest),
	}
	if !sort.StringsAreSorted(roles) {
		t.Fatal("Go task artifact roles are not lexically ordered")
	}
	var artifactDefinition struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(document.Definitions["artifact"], &artifactDefinition); err != nil {
		t.Fatal(err)
	}
	var roleProperty struct {
		Enum []string `json:"enum"`
	}
	if err := json.Unmarshal(artifactDefinition.Properties["role"], &roleProperty); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(roleProperty.Enum, roles) {
		t.Fatalf("schema roles = %v, want %v", roleProperty.Enum, roles)
	}

	var objectDefinition struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(document.Definitions["objectRef"], &objectDefinition); err != nil {
		t.Fatal(err)
	}
	var mediaTypeProperty struct {
		Enum []string `json:"enum"`
	}
	if err := json.Unmarshal(objectDefinition.Properties["media_type"], &mediaTypeProperty); err != nil {
		t.Fatal(err)
	}
	wantMediaTypes := []string{
		GoldPatchMediaType,
		HiddenEvaluatorBundleMediaType,
		PinnedToolchainManifestMediaType,
		TaskPromptMediaType,
	}
	if !reflect.DeepEqual(mediaTypeProperty.Enum, wantMediaTypes) {
		t.Fatalf("schema media types = %v, want %v", mediaTypeProperty.Enum, wantMediaTypes)
	}
}
