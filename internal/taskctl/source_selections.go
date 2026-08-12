package taskctl

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
)

const (
	sourceSelectionsSchemaV1             = "scopesifter.source-selections.v1"
	sourceSelectionAuthoringSchemaV1     = "scopesifter.source-selection-authoring.v1"
	sourceSelectionsMaximumBytes         = 1 << 20
	sourceSelectionAuthoringMaximumBytes = 1 << 20
)

type sourceSelectionsDocumentV1 struct {
	Schema     string                    `json:"schema"`
	Selections []sourceSelectionRecordV1 `json:"selections"`
}

type sourceSelectionRecordV1 struct {
	TaskID          string `json:"task_id"`
	Upstream        string `json:"upstream"`
	Family          string `json:"family"`
	Tier            string `json:"tier"`
	VisibleCheckout string `json:"visible_checkout"`
}

type sourceSelectionAuthoringDocumentV1 struct {
	Schema     string                             `json:"schema"`
	Selections []SourceSelectionAuthoringRecordV1 `json:"selections"`
}

// SourceSelectionAuthoringRecordV1 is one explicit author choice. Repository,
// family, and tier are deliberately absent: GenerateSourceSelections derives
// those values from the locked task contract rather than trusting duplicated
// authoring metadata.
type SourceSelectionAuthoringRecordV1 struct {
	TaskID          string `json:"task_id"`
	VisibleCheckout string `json:"visible_checkout"`
}

// GenerateSourceSelections validates an authenticated canonical authoring
// document and expands its choices into the canonical source-selections.v1
// artifact. Callers must authenticate data before calling this function.
func GenerateSourceSelections(data []byte) ([]byte, error) {
	records, _, err := DecodeSourceSelectionAuthoring(data)
	if err != nil {
		return nil, fmt.Errorf("validate source-selection generator input: %w", err)
	}
	expected, err := expectedSourceSelectionTasks()
	if err != nil {
		return nil, fmt.Errorf("load locked source-selection contract: %w", err)
	}
	checkouts := make(map[string]string, len(records))
	for _, record := range records {
		checkouts[record.TaskID] = record.VisibleCheckout
	}
	tasks := make([]sourceAuditTask, len(expected))
	copy(tasks, expected)
	for index := range tasks {
		checkout, found := checkouts[tasks[index].id]
		if !found {
			return nil, fmt.Errorf(
				"source-selection authoring input is missing task ID %q",
				tasks[index].id,
			)
		}
		tasks[index].checkout = checkout
	}
	canonical, err := BuildSourceSelections(tasks)
	if err != nil {
		return nil, fmt.Errorf("build canonical source selections: %w", err)
	}
	return canonical, nil
}

// BuildSourceSelectionAuthoring returns canonical authoring-schema JSON for an
// exact task set. Input order does not affect output.
func BuildSourceSelectionAuthoring(
	records []SourceSelectionAuthoringRecordV1,
) ([]byte, error) {
	expected, err := expectedSourceSelectionTasks()
	if err != nil {
		return nil, err
	}
	if len(records) != len(expected) {
		return nil, fmt.Errorf(
			"source-selection authoring input contains %d records, want exactly %d",
			len(records),
			len(expected),
		)
	}

	expectedByID := make(map[string]struct{}, len(expected))
	for _, task := range expected {
		if _, duplicate := expectedByID[task.id]; duplicate {
			return nil, fmt.Errorf("source-selection contract repeats task ID %q", task.id)
		}
		expectedByID[task.id] = struct{}{}
	}
	provided := make(map[string]SourceSelectionAuthoringRecordV1, len(records))
	for _, record := range records {
		if _, found := expectedByID[record.TaskID]; !found {
			return nil, fmt.Errorf(
				"source-selection authoring input contains unknown task ID %q",
				record.TaskID,
			)
		}
		if _, duplicate := provided[record.TaskID]; duplicate {
			return nil, fmt.Errorf(
				"source-selection authoring input repeats task ID %q",
				record.TaskID,
			)
		}
		if !isLowerHexObjectID(record.VisibleCheckout) {
			return nil, fmt.Errorf(
				"source-selection authoring record %s visible checkout is not lowercase 40-hex",
				record.TaskID,
			)
		}
		provided[record.TaskID] = record
	}

	document := sourceSelectionAuthoringDocumentV1{
		Schema:     sourceSelectionAuthoringSchemaV1,
		Selections: make([]SourceSelectionAuthoringRecordV1, 0, len(expected)),
	}
	for _, task := range expected {
		record, found := provided[task.id]
		if !found {
			return nil, fmt.Errorf(
				"source-selection authoring input is missing task ID %q",
				task.id,
			)
		}
		document.Selections = append(document.Selections, record)
	}
	canonical, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode canonical source-selection authoring input: %w", err)
	}
	return append(canonical, '\n'), nil
}

// DecodeSourceSelectionAuthoring strictly decodes a canonical schema-v1
// authoring document and returns its records in locked task order. The returned
// canonical bytes are byte-identical to data on success.
func DecodeSourceSelectionAuthoring(
	data []byte,
) ([]SourceSelectionAuthoringRecordV1, []byte, error) {
	if len(data) > sourceSelectionAuthoringMaximumBytes {
		return nil, nil, fmt.Errorf(
			"source-selection authoring input exceeds %d bytes",
			sourceSelectionAuthoringMaximumBytes,
		)
	}
	if err := validateUniqueJSONKeys(data); err != nil {
		return nil, nil, fmt.Errorf("decode source-selection authoring input: %w", err)
	}

	var document sourceSelectionAuthoringDocumentV1
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return nil, nil, fmt.Errorf("decode source-selection authoring input: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, nil, errors.New(
				"decode source-selection authoring input: multiple JSON values",
			)
		}
		return nil, nil, fmt.Errorf(
			"decode trailing source-selection authoring JSON: %w",
			err,
		)
	}
	if document.Schema != sourceSelectionAuthoringSchemaV1 {
		return nil, nil, fmt.Errorf(
			"source-selection authoring schema is %q, want %q",
			document.Schema,
			sourceSelectionAuthoringSchemaV1,
		)
	}
	canonical, err := BuildSourceSelectionAuthoring(document.Selections)
	if err != nil {
		return nil, nil, fmt.Errorf("validate source-selection authoring input: %w", err)
	}
	if !bytes.Equal(data, canonical) {
		return nil, nil, errors.New(
			"source-selection authoring input is not canonical JSON",
		)
	}
	return document.Selections, canonical, nil
}

// BuildSourceSelections renders the exact 12-repository x 3-family x 4-tier
// source selection matrix as canonical schema-v1 JSON. The input order does
// not affect the output; every expected task must appear exactly once.
func BuildSourceSelections(tasks []sourceAuditTask) ([]byte, error) {
	expected, err := expectedSourceSelectionTasks()
	if err != nil {
		return nil, err
	}
	if len(tasks) != len(expected) {
		return nil, fmt.Errorf(
			"source selections contain %d tasks, want exactly %d",
			len(tasks),
			len(expected),
		)
	}

	expectedByID := make(map[string]sourceAuditTask, len(expected))
	for _, task := range expected {
		if _, duplicate := expectedByID[task.id]; duplicate {
			return nil, fmt.Errorf("source selection contract repeats task ID %q", task.id)
		}
		expectedByID[task.id] = task
	}

	provided := make(map[string]sourceAuditTask, len(tasks))
	for _, task := range tasks {
		expectedTask, found := expectedByID[task.id]
		if !found {
			return nil, fmt.Errorf("source selections contain unknown task ID %q", task.id)
		}
		if _, duplicate := provided[task.id]; duplicate {
			return nil, fmt.Errorf("source selections repeat task ID %q", task.id)
		}
		if task.repository != expectedTask.repository {
			return nil, fmt.Errorf(
				"source selection %s repository is not the locked %s descriptor",
				task.id,
				expectedTask.repository.upstream,
			)
		}
		if task.family != expectedTask.family {
			return nil, fmt.Errorf(
				"source selection %s family is %q, want %q",
				task.id,
				task.family,
				expectedTask.family,
			)
		}
		if task.tier != expectedTask.tier {
			return nil, fmt.Errorf(
				"source selection %s tier is %q, want %q",
				task.id,
				task.tier,
				expectedTask.tier,
			)
		}
		if !isLowerHexObjectID(task.checkout) {
			return nil, fmt.Errorf(
				"source selection %s visible checkout is not lowercase 40-hex",
				task.id,
			)
		}
		provided[task.id] = task
	}

	document := sourceSelectionsDocumentV1{
		Schema:     sourceSelectionsSchemaV1,
		Selections: make([]sourceSelectionRecordV1, 0, len(expected)),
	}
	for _, expectedTask := range expected {
		task, found := provided[expectedTask.id]
		if !found {
			return nil, fmt.Errorf("source selections are missing task ID %q", expectedTask.id)
		}
		document.Selections = append(document.Selections, sourceSelectionRecordV1{
			TaskID:          task.id,
			Upstream:        task.repository.upstream,
			Family:          task.family,
			Tier:            task.tier,
			VisibleCheckout: task.checkout,
		})
	}

	canonical, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode canonical source selections: %w", err)
	}
	return append(canonical, '\n'), nil
}

// DecodeSourceSelections strictly decodes and validates a canonical schema-v1
// source selection manifest. It returns the locked task order and the canonical
// bytes, which are byte-identical to data on success.
func DecodeSourceSelections(data []byte) ([]sourceAuditTask, []byte, error) {
	if len(data) > sourceSelectionsMaximumBytes {
		return nil, nil, fmt.Errorf(
			"source selection manifest exceeds %d bytes",
			sourceSelectionsMaximumBytes,
		)
	}
	if err := validateUniqueJSONKeys(data); err != nil {
		return nil, nil, fmt.Errorf("decode source selection manifest: %w", err)
	}

	var document sourceSelectionsDocumentV1
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&document); err != nil {
		return nil, nil, fmt.Errorf("decode source selection manifest: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return nil, nil, errors.New("decode source selection manifest: multiple JSON values")
		}
		return nil, nil, fmt.Errorf("decode trailing source selection JSON: %w", err)
	}
	if document.Schema != sourceSelectionsSchemaV1 {
		return nil, nil, fmt.Errorf(
			"source selection schema is %q, want %q",
			document.Schema,
			sourceSelectionsSchemaV1,
		)
	}
	if len(document.Selections) != sourceAuditTaskCount {
		return nil, nil, fmt.Errorf(
			"source selection manifest contains %d records, want exactly %d",
			len(document.Selections),
			sourceAuditTaskCount,
		)
	}

	repositoriesByTaskID, err := expectedSourceSelectionTaskMap()
	if err != nil {
		return nil, nil, err
	}
	tasks := make([]sourceAuditTask, 0, len(document.Selections))
	seen := make(map[string]struct{}, len(document.Selections))
	for index, selection := range document.Selections {
		if selection.TaskID == "" {
			return nil, nil, fmt.Errorf("source selection record %d is missing task_id", index)
		}
		if _, duplicate := seen[selection.TaskID]; duplicate {
			return nil, nil, fmt.Errorf("source selections repeat task ID %q", selection.TaskID)
		}
		seen[selection.TaskID] = struct{}{}
		expectedTask, found := repositoriesByTaskID[selection.TaskID]
		if !found {
			return nil, nil, fmt.Errorf("source selections contain unknown task ID %q", selection.TaskID)
		}
		if selection.Upstream != expectedTask.repository.upstream {
			return nil, nil, fmt.Errorf(
				"source selection %s upstream is %q, want %q",
				selection.TaskID,
				selection.Upstream,
				expectedTask.repository.upstream,
			)
		}
		if selection.Family != expectedTask.family {
			return nil, nil, fmt.Errorf(
				"source selection %s family is %q, want %q",
				selection.TaskID,
				selection.Family,
				expectedTask.family,
			)
		}
		if selection.Tier != expectedTask.tier {
			return nil, nil, fmt.Errorf(
				"source selection %s tier is %q, want %q",
				selection.TaskID,
				selection.Tier,
				expectedTask.tier,
			)
		}
		if !isLowerHexObjectID(selection.VisibleCheckout) {
			return nil, nil, fmt.Errorf(
				"source selection %s visible checkout is not lowercase 40-hex",
				selection.TaskID,
			)
		}
		tasks = append(tasks, sourceAuditTask{
			id:         selection.TaskID,
			repository: expectedTask.repository,
			family:     selection.Family,
			tier:       selection.Tier,
			checkout:   selection.VisibleCheckout,
		})
	}

	canonical, err := BuildSourceSelections(tasks)
	if err != nil {
		return nil, nil, fmt.Errorf("validate source selection manifest: %w", err)
	}
	if !bytes.Equal(data, canonical) {
		return nil, nil, errors.New("source selection manifest is not canonical JSON")
	}
	return tasks, canonical, nil
}

// ValidateSourceSelections validates a canonical schema-v1 source selection
// manifest and returns its exact 144 tasks in locked repository/family/tier
// order.
func ValidateSourceSelections(data []byte) ([]sourceAuditTask, error) {
	tasks, _, err := DecodeSourceSelections(data)
	return tasks, err
}

func expectedSourceSelectionTasks() ([]sourceAuditTask, error) {
	count := len(sourceAuditRepositories) * len(sourceAuditFamilies) * len(sourceAuditTiers)
	if count != sourceAuditTaskCount {
		return nil, fmt.Errorf(
			"source selection contract contains %d Cartesian cells, want %d",
			count,
			sourceAuditTaskCount,
		)
	}
	tasks := make([]sourceAuditTask, 0, count)
	for _, repository := range sourceAuditRepositories {
		for _, family := range sourceAuditFamilies {
			for _, tier := range sourceAuditTiers {
				tasks = append(tasks, sourceAuditTask{
					id:         repository.language + "." + repository.slug + "." + family + "." + tier,
					repository: repository,
					family:     family,
					tier:       tier,
				})
			}
		}
	}
	return tasks, nil
}

func expectedSourceSelectionTaskMap() (map[string]sourceAuditTask, error) {
	tasks, err := expectedSourceSelectionTasks()
	if err != nil {
		return nil, err
	}
	byID := make(map[string]sourceAuditTask, len(tasks))
	for _, task := range tasks {
		if _, duplicate := byID[task.id]; duplicate {
			return nil, fmt.Errorf("source selection contract repeats task ID %q", task.id)
		}
		byID[task.id] = task
	}
	return byID, nil
}

func isLowerHexObjectID(value string) bool {
	if len(value) != 40 {
		return false
	}
	for _, character := range []byte(value) {
		if (character < '0' || character > '9') && (character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}
