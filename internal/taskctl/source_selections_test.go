package taskctl

import (
	"bytes"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
)

func TestBuildAndValidateSourceSelectionsCanonicalRoundTrip(t *testing.T) {
	t.Parallel()

	tasks := sourceSelectionFixtureTasks(t)
	canonical, err := BuildSourceSelections(tasks)
	if err != nil {
		t.Fatalf("BuildSourceSelections() error = %v", err)
	}
	if len(canonical) == 0 || canonical[len(canonical)-1] != '\n' {
		t.Fatalf("canonical manifest lacks one final newline: %q", canonical)
	}
	if len(canonical) > 1 && canonical[len(canonical)-2] == '\n' {
		t.Fatalf("canonical manifest has more than one final newline: %q", canonical[len(canonical)-2:])
	}

	decoded, decodedCanonical, err := DecodeSourceSelections(canonical)
	if err != nil {
		t.Fatalf("DecodeSourceSelections() error = %v", err)
	}
	if !bytes.Equal(decodedCanonical, canonical) {
		t.Fatal("DecodeSourceSelections() returned different canonical bytes")
	}
	if !reflect.DeepEqual(decoded, tasks) {
		t.Fatal("DecodeSourceSelections() returned different tasks")
	}
	validated, err := ValidateSourceSelections(canonical)
	if err != nil {
		t.Fatalf("ValidateSourceSelections() error = %v", err)
	}
	if !reflect.DeepEqual(validated, tasks) {
		t.Fatal("ValidateSourceSelections() returned different tasks")
	}

	var document sourceSelectionsDocumentV1
	if err := json.Unmarshal(canonical, &document); err != nil {
		t.Fatal(err)
	}
	if document.Schema != sourceSelectionsSchemaV1 {
		t.Fatalf("schema = %q, want %q", document.Schema, sourceSelectionsSchemaV1)
	}
	if len(document.Selections) != sourceAuditTaskCount {
		t.Fatalf("selection count = %d, want %d", len(document.Selections), sourceAuditTaskCount)
	}
	index := 0
	seen := make(map[string]struct{}, sourceAuditTaskCount)
	for _, repository := range sourceAuditRepositories {
		for _, family := range sourceAuditFamilies {
			for _, tier := range sourceAuditTiers {
				record := document.Selections[index]
				wantID := repository.language + "." + repository.slug + "." + family + "." + tier
				if record.TaskID != wantID || record.Upstream != repository.upstream ||
					record.Family != family || record.Tier != tier {
					t.Fatalf("canonical record %d = %#v, want %s/%s/%s/%s", index, record, wantID, repository.upstream, family, tier)
				}
				if _, duplicate := seen[record.TaskID]; duplicate {
					t.Fatalf("canonical records repeat task ID %q", record.TaskID)
				}
				seen[record.TaskID] = struct{}{}
				index++
			}
		}
	}
	if index != sourceAuditTaskCount {
		t.Fatalf("checked %d Cartesian records, want %d", index, sourceAuditTaskCount)
	}
}

func TestBuildAndDecodeSourceSelectionAuthoringCanonicalRoundTrip(t *testing.T) {
	t.Parallel()

	records := sourceSelectionAuthoringFixtureRecords(t)
	canonical, err := BuildSourceSelectionAuthoring(records)
	if err != nil {
		t.Fatalf("BuildSourceSelectionAuthoring() error = %v", err)
	}
	decoded, decodedCanonical, err := DecodeSourceSelectionAuthoring(canonical)
	if err != nil {
		t.Fatalf("DecodeSourceSelectionAuthoring() error = %v", err)
	}
	if !reflect.DeepEqual(decoded, records) {
		t.Fatal("decoded source-selection authoring records differ")
	}
	if !bytes.Equal(decodedCanonical, canonical) {
		t.Fatal("decoded canonical authoring bytes differ")
	}

	var document sourceSelectionAuthoringDocumentV1
	if err := json.Unmarshal(canonical, &document); err != nil {
		t.Fatal(err)
	}
	if document.Schema != sourceSelectionAuthoringSchemaV1 {
		t.Fatalf("schema = %q, want %q", document.Schema, sourceSelectionAuthoringSchemaV1)
	}
	if len(document.Selections) != sourceAuditTaskCount {
		t.Fatalf("record count = %d, want %d", len(document.Selections), sourceAuditTaskCount)
	}
	for index, record := range document.Selections {
		if record != records[index] {
			t.Fatalf("authoring record %d = %#v, want %#v", index, record, records[index])
		}
	}
}

func TestBuildSourceSelectionAuthoringCanonicalizesInputOrder(t *testing.T) {
	t.Parallel()

	records := sourceSelectionAuthoringFixtureRecords(t)
	want, err := BuildSourceSelectionAuthoring(records)
	if err != nil {
		t.Fatal(err)
	}
	for left, right := 0, len(records)-1; left < right; left, right = left+1, right-1 {
		records[left], records[right] = records[right], records[left]
	}
	got, err := BuildSourceSelectionAuthoring(records)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("authoring output depends on record input order")
	}
}

func TestBuildSourceSelectionAuthoringRejectsInexactTaskSet(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		edit func([]SourceSelectionAuthoringRecordV1) []SourceSelectionAuthoringRecordV1
		want string
	}{
		{
			name: "missing",
			edit: func(records []SourceSelectionAuthoringRecordV1) []SourceSelectionAuthoringRecordV1 {
				return records[:len(records)-1]
			},
			want: "143 records",
		},
		{
			name: "duplicate",
			edit: func(records []SourceSelectionAuthoringRecordV1) []SourceSelectionAuthoringRecordV1 {
				records[len(records)-1] = records[0]
				return records
			},
			want: "repeats task ID",
		},
		{
			name: "unknown",
			edit: func(records []SourceSelectionAuthoringRecordV1) []SourceSelectionAuthoringRecordV1 {
				records[0].TaskID = "cpp.seastar.code.tiny"
				return records
			},
			want: "unknown task ID",
		},
		{
			name: "invalid checkout",
			edit: func(records []SourceSelectionAuthoringRecordV1) []SourceSelectionAuthoringRecordV1 {
				records[0].VisibleCheckout = strings.Repeat("A", 40)
				return records
			},
			want: "lowercase 40-hex",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			records := sourceSelectionAuthoringFixtureRecords(t)
			if _, err := BuildSourceSelectionAuthoring(test.edit(records)); err == nil ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("BuildSourceSelectionAuthoring() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestDecodeSourceSelectionAuthoringRejectsSchemaFieldsSetAndCanonicalDrift(t *testing.T) {
	t.Parallel()

	canonical := sourceSelectionAuthoringFixtureJSON(t)
	compact := &bytes.Buffer{}
	if err := json.Compact(compact, canonical); err != nil {
		t.Fatal(err)
	}
	reordered := mutateSourceSelectionJSON(t, canonical, func(document map[string]any) {
		selections := document["selections"].([]any)
		selections[0], selections[1] = selections[1], selections[0]
	})
	missing := mutateSourceSelectionJSON(t, canonical, func(document map[string]any) {
		selections := document["selections"].([]any)
		document["selections"] = selections[:len(selections)-1]
	})
	duplicate := mutateSourceSelectionJSON(t, canonical, func(document map[string]any) {
		selections := document["selections"].([]any)
		selections[len(selections)-1] = selections[0]
	})
	unknownField := mutateSourceSelectionJSON(t, canonical, func(document map[string]any) {
		firstSourceSelectionRecord(document)["upstream"] = "scylladb/seastar"
	})
	wrongSchema := mutateSourceSelectionJSON(t, canonical, func(document map[string]any) {
		document["schema"] = sourceSelectionsSchemaV1
	})
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{name: "wrong schema", data: wrongSchema, want: "authoring schema"},
		{name: "noncanonical", data: compact.Bytes(), want: "not canonical JSON"},
		{name: "record order", data: reordered, want: "not canonical JSON"},
		{name: "missing record", data: missing, want: "143 records"},
		{name: "duplicate task", data: duplicate, want: "repeats task ID"},
		{name: "locked metadata field", data: unknownField, want: "unknown field"},
		{
			name: "unknown top-level field",
			data: bytes.Replace(canonical, []byte("{\n"), []byte("{\n  \"unknown\": true,\n"), 1),
			want: "unknown field",
		},
		{
			name: "duplicate record key",
			data: bytes.Replace(
				canonical,
				[]byte(`"visible_checkout": "0000000000000000000000000000000000000001"`),
				[]byte(`"visible_checkout": "0000000000000000000000000000000000000001", "visible_checkout": "0000000000000000000000000000000000000001"`),
				1,
			),
			want: "duplicate JSON object key",
		},
		{name: "trailing value", data: append(append([]byte(nil), canonical...), []byte("{}\n")...), want: "multiple JSON values"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, _, err := DecodeSourceSelectionAuthoring(test.data); err == nil ||
				!strings.Contains(err.Error(), test.want) {
				t.Fatalf("DecodeSourceSelectionAuthoring() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestDecodeSourceSelectionAuthoringEnforcesResourceBounds(t *testing.T) {
	t.Parallel()

	tooLarge := make([]byte, sourceSelectionAuthoringMaximumBytes+1)
	if _, _, err := DecodeSourceSelectionAuthoring(tooLarge); err == nil ||
		!strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized authoring input error = %v", err)
	}
	tooDeep := append(
		[]byte(`{"schema":"scopesifter.source-selection-authoring.v1","selections":`),
		bytes.Repeat([]byte{'['}, maximumTaskctlJSONDepth+2)...,
	)
	tooDeep = append(tooDeep, bytes.Repeat([]byte{']'}, maximumTaskctlJSONDepth+2)...)
	tooDeep = append(tooDeep, '}')
	if _, _, err := DecodeSourceSelectionAuthoring(tooDeep); err == nil ||
		!strings.Contains(err.Error(), "nesting exceeds") {
		t.Fatalf("over-depth authoring input error = %v", err)
	}
}

func TestGenerateSourceSelectionsDerivesLockedMetadataFromAuthoring(t *testing.T) {
	t.Parallel()

	input := sourceSelectionAuthoringFixtureJSON(t)
	got, err := GenerateSourceSelections(input)
	if err != nil {
		t.Fatalf("GenerateSourceSelections() error = %v", err)
	}
	want := sourceSelectionFixtureJSON(t)
	if !bytes.Equal(got, want) {
		t.Fatal("generated source selections differ from locked canonical artifact")
	}
	if bytes.Equal(got, input) {
		t.Fatal("generated artifact unexpectedly equals distinct authoring schema")
	}
}

func TestGenerateSourceSelectionsRejectsFinalManifestAsAuthoringInput(t *testing.T) {
	t.Parallel()

	if _, err := GenerateSourceSelections(sourceSelectionFixtureJSON(t)); err == nil ||
		!strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("GenerateSourceSelections() error = %v, want final-schema rejection", err)
	}
}

func TestBuildSourceSelectionsCanonicalizesInputOrder(t *testing.T) {
	t.Parallel()

	tasks := sourceSelectionFixtureTasks(t)
	want, err := BuildSourceSelections(tasks)
	if err != nil {
		t.Fatal(err)
	}
	for left, right := 0, len(tasks)-1; left < right; left, right = left+1, right-1 {
		tasks[left], tasks[right] = tasks[right], tasks[left]
	}
	got, err := BuildSourceSelections(tasks)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		t.Fatal("BuildSourceSelections() output depends on task input order")
	}
}

func TestBuildSourceSelectionsRejectsInexactTaskSet(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		edit func([]sourceAuditTask) []sourceAuditTask
		want string
	}{
		{
			name: "missing",
			edit: func(tasks []sourceAuditTask) []sourceAuditTask { return tasks[:len(tasks)-1] },
			want: "143 tasks",
		},
		{
			name: "duplicate",
			edit: func(tasks []sourceAuditTask) []sourceAuditTask {
				tasks[len(tasks)-1] = tasks[0]
				return tasks
			},
			want: "repeat task ID",
		},
		{
			name: "unknown task ID",
			edit: func(tasks []sourceAuditTask) []sourceAuditTask {
				tasks[0].id = "cpp.seastar.code.tiny"
				return tasks
			},
			want: "unknown task ID",
		},
		{
			name: "repository drift",
			edit: func(tasks []sourceAuditTask) []sourceAuditTask {
				tasks[0].repository.upstream = "example.invalid/seastar"
				return tasks
			},
			want: "locked scylladb/seastar descriptor",
		},
		{
			name: "family drift",
			edit: func(tasks []sourceAuditTask) []sourceAuditTask {
				tasks[0].family = "review"
				return tasks
			},
			want: "family is",
		},
		{
			name: "tier drift",
			edit: func(tasks []sourceAuditTask) []sourceAuditTask {
				tasks[0].tier = "huge"
				return tasks
			},
			want: "tier is",
		},
		{
			name: "short checkout",
			edit: func(tasks []sourceAuditTask) []sourceAuditTask {
				tasks[0].checkout = "0123456789abcdef"
				return tasks
			},
			want: "lowercase 40-hex",
		},
		{
			name: "uppercase checkout",
			edit: func(tasks []sourceAuditTask) []sourceAuditTask {
				tasks[0].checkout = "000000000000000000000000000000000000000A"
				return tasks
			},
			want: "lowercase 40-hex",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			tasks := sourceSelectionFixtureTasks(t)
			_, err := BuildSourceSelections(test.edit(tasks))
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("BuildSourceSelections() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateSourceSelectionsRejectsSchemaAndSetDrift(t *testing.T) {
	t.Parallel()

	canonical := sourceSelectionFixtureJSON(t)
	tests := []struct {
		name string
		edit func(map[string]any)
		want string
	}{
		{
			name: "schema missing",
			edit: func(document map[string]any) { delete(document, "schema") },
			want: "schema is",
		},
		{
			name: "schema legacy",
			edit: func(document map[string]any) { document["schema"] = "scopesifter.source-selections.v0" },
			want: "schema is",
		},
		{
			name: "selections missing",
			edit: func(document map[string]any) { delete(document, "selections") },
			want: "contains 0 records",
		},
		{
			name: "selection missing",
			edit: func(document map[string]any) {
				selections := document["selections"].([]any)
				document["selections"] = selections[:len(selections)-1]
			},
			want: "contains 143 records",
		},
		{
			name: "selection extra",
			edit: func(document map[string]any) {
				selections := document["selections"].([]any)
				document["selections"] = append(selections, selections[0])
			},
			want: "contains 145 records",
		},
		{
			name: "selection duplicate",
			edit: func(document map[string]any) {
				selections := document["selections"].([]any)
				selections[len(selections)-1] = selections[0]
			},
			want: "repeat task ID",
		},
		{
			name: "task unknown",
			edit: func(document map[string]any) {
				firstSourceSelectionRecord(document)["task_id"] = "cpp.seastar.code.tiny"
			},
			want: "unknown task ID",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			data := mutateSourceSelectionJSON(t, canonical, test.edit)
			_, err := ValidateSourceSelections(data)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateSourceSelections() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateSourceSelectionsRejectsMissingOrDriftingRecordFields(t *testing.T) {
	t.Parallel()

	canonical := sourceSelectionFixtureJSON(t)
	tests := []struct {
		field string
		want  string
	}{
		{"task_id", "missing task_id"},
		{"upstream", "upstream is"},
		{"family", "family is"},
		{"tier", "tier is"},
		{"visible_checkout", "visible checkout"},
	}
	for _, test := range tests {
		t.Run(test.field, func(t *testing.T) {
			t.Parallel()
			data := mutateSourceSelectionJSON(t, canonical, func(document map[string]any) {
				delete(firstSourceSelectionRecord(document), test.field)
			})
			_, err := ValidateSourceSelections(data)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateSourceSelections() error = %v, want %q", err, test.want)
			}
		})
	}

	drift := []struct {
		name  string
		field string
		value string
		want  string
	}{
		{"upstream", "upstream", "example.invalid/seastar", "upstream is"},
		{"family", "family", "review", "family is"},
		{"tier", "tier", "huge", "tier is"},
		{"uppercase checkout", "visible_checkout", "000000000000000000000000000000000000000A", "lowercase 40-hex"},
		{"short checkout", "visible_checkout", "000000000000000000000000000000000000000", "lowercase 40-hex"},
	}
	for _, test := range drift {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			data := mutateSourceSelectionJSON(t, canonical, func(document map[string]any) {
				firstSourceSelectionRecord(document)[test.field] = test.value
			})
			_, err := ValidateSourceSelections(data)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateSourceSelections() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateSourceSelectionsRejectsUnknownAndDuplicateJSONFields(t *testing.T) {
	t.Parallel()

	canonical := sourceSelectionFixtureJSON(t)
	firstTask := "\"task_id\": \"cpp.seastar.code.small\""
	tests := []struct {
		name string
		data []byte
		want string
	}{
		{
			name: "unknown top-level field",
			data: bytes.Replace(canonical, []byte("{\n"), []byte("{\n  \"legacy\": true,\n"), 1),
			want: "unknown field",
		},
		{
			name: "unknown record field",
			data: bytes.Replace(canonical, []byte(firstTask), []byte(firstTask+",\n      \"legacy\": true"), 1),
			want: "unknown field",
		},
		{
			name: "duplicate top-level field",
			data: bytes.Replace(
				canonical,
				[]byte("\"schema\": \""+sourceSelectionsSchemaV1+"\""),
				[]byte("\"schema\": \""+sourceSelectionsSchemaV1+"\",\n  \"schema\": \""+sourceSelectionsSchemaV1+"\""),
				1,
			),
			want: "duplicate JSON object key",
		},
		{
			name: "case-fold duplicate record field",
			data: bytes.Replace(canonical, []byte(firstTask), []byte(firstTask+",\n      \"Task_ID\": \"cpp.seastar.code.small\""), 1),
			want: "case-fold duplicate JSON object keys",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := ValidateSourceSelections(test.data)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("ValidateSourceSelections() error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateSourceSelectionsRejectsNoncanonicalBytes(t *testing.T) {
	t.Parallel()

	canonical := sourceSelectionFixtureJSON(t)
	compact := &bytes.Buffer{}
	if err := json.Compact(compact, canonical); err != nil {
		t.Fatal(err)
	}
	reordered := mutateSourceSelectionJSON(t, canonical, func(document map[string]any) {
		selections := document["selections"].([]any)
		selections[0], selections[1] = selections[1], selections[0]
	})
	tests := []struct {
		name string
		data []byte
	}{
		{"compact", compact.Bytes()},
		{"extra newline", append(append([]byte(nil), canonical...), '\n')},
		{"record order", reordered},
		{"legacy tasks field", bytes.Replace(canonical, []byte("\"selections\""), []byte("\"tasks\""), 1)},
		{"trailing value", append(append([]byte(nil), canonical...), []byte("{}\n")...)},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := ValidateSourceSelections(test.data)
			if err == nil {
				t.Fatal("ValidateSourceSelections() unexpectedly accepted noncanonical bytes")
			}
		})
	}
}

func TestValidateSourceSelectionsEnforcesResourceBounds(t *testing.T) {
	t.Parallel()

	tooLarge := make([]byte, sourceSelectionsMaximumBytes+1)
	if _, err := ValidateSourceSelections(tooLarge); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized manifest error = %v", err)
	}

	tooDeep := append([]byte(`{"schema":"scopesifter.source-selections.v1","selections":`),
		bytes.Repeat([]byte{'['}, maximumTaskctlJSONDepth+2)...)
	tooDeep = append(tooDeep, bytes.Repeat([]byte{']'}, maximumTaskctlJSONDepth+2)...)
	tooDeep = append(tooDeep, '}')
	if _, err := ValidateSourceSelections(tooDeep); err == nil || !strings.Contains(err.Error(), "nesting exceeds") {
		t.Fatalf("over-depth manifest error = %v", err)
	}
}

func sourceSelectionFixtureTasks(t *testing.T) []sourceAuditTask {
	t.Helper()
	tasks, err := expectedSourceSelectionTasks()
	if err != nil {
		t.Fatal(err)
	}
	for index := range tasks {
		tasks[index].checkout = fmt.Sprintf("%040x", index+1)
	}
	return tasks
}

func sourceSelectionFixtureJSON(t *testing.T) []byte {
	t.Helper()
	canonical, err := BuildSourceSelections(sourceSelectionFixtureTasks(t))
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func sourceSelectionAuthoringFixtureRecords(
	t *testing.T,
) []SourceSelectionAuthoringRecordV1 {
	t.Helper()
	tasks := sourceSelectionFixtureTasks(t)
	records := make([]SourceSelectionAuthoringRecordV1, 0, len(tasks))
	for _, task := range tasks {
		records = append(records, SourceSelectionAuthoringRecordV1{
			TaskID:          task.id,
			VisibleCheckout: task.checkout,
		})
	}
	return records
}

func sourceSelectionAuthoringFixtureJSON(t *testing.T) []byte {
	t.Helper()
	canonical, err := BuildSourceSelectionAuthoring(
		sourceSelectionAuthoringFixtureRecords(t),
	)
	if err != nil {
		t.Fatal(err)
	}
	return canonical
}

func mutateSourceSelectionJSON(
	t *testing.T,
	data []byte,
	mutate func(map[string]any),
) []byte {
	t.Helper()
	var document map[string]any
	if err := json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	mutate(document)
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(encoded, '\n')
}

func firstSourceSelectionRecord(document map[string]any) map[string]any {
	return document["selections"].([]any)[0].(map[string]any)
}
