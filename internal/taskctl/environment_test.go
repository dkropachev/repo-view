package taskctl

import (
	"strings"
	"testing"
)

func TestValidateTaskctlScalarEnvironmentValueRejectsUnsafeValues(t *testing.T) {
	if err := validateTaskctlScalarEnvironmentValue(
		taskctlInputEnvironment,
		"nul\x00path",
		maximumTaskctlEnvironmentPathBytes,
	); err == nil || !strings.Contains(err.Error(), "control byte") {
		t.Fatalf("NUL value error = %v", err)
	}
	if err := validateTaskctlScalarEnvironmentValue(
		taskctlInputEnvironment,
		strings.Repeat("x", maximumTaskctlEnvironmentPathBytes+1),
		maximumTaskctlEnvironmentPathBytes,
	); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized scalar error = %v", err)
	}
}

func TestEmptyArrayEnvironmentsBehaveAsUnsetMakeInputs(t *testing.T) {
	t.Setenv(taskctlRepositoriesJSONEnvironment, "")
	repositories, err := resolveTaskctlRepositoryEnvironment(
		repeatableStrings{"github.com/acme/repo=/src/repo"},
	)
	if err != nil {
		t.Fatalf("resolve empty exported repositories: %v", err)
	}
	if len(repositories) != 1 || repositories[0].Upstream != "github.com/acme/repo" ||
		repositories[0].Path != "/src/repo" {
		t.Fatalf("resolved repositories = %#v", repositories)
	}
}

func TestDecodeCanonicalTaskctlEnvironmentJSONArray(t *testing.T) {
	var stringsDestination []string
	if err := decodeCanonicalTaskctlEnvironmentJSONArray(
		`["a","nested/b"]`,
		&stringsDestination,
	); err != nil {
		t.Fatalf("canonical string array rejected: %v", err)
	}
	if len(stringsDestination) != 2 || stringsDestination[1] != "nested/b" {
		t.Fatalf("decoded strings = %#v", stringsDestination)
	}

	for name, raw := range map[string]string{
		"empty":          "",
		"null":           "null",
		"whitespace":     `[ "a"]`,
		"trailing space": `["a"] `,
		"wrong element":  `[1]`,
		"multiple":       `[] []`,
	} {
		t.Run(name, func(t *testing.T) {
			var destination []string
			if err := decodeCanonicalTaskctlEnvironmentJSONArray(
				raw,
				&destination,
			); err == nil {
				t.Fatalf("noncanonical array %q was accepted", raw)
			}
		})
	}

	tooLarge := "[\"" + strings.Repeat("x", maximumTaskctlEnvironmentJSONBytes) + "\"]"
	if err := decodeCanonicalTaskctlEnvironmentJSONArray(
		tooLarge,
		&stringsDestination,
	); err == nil || !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("oversized environment error = %v", err)
	}
}

func TestResolveTaskctlRepositoryEnvironmentIsStrict(t *testing.T) {
	t.Setenv(
		taskctlRepositoriesJSONEnvironment,
		`[{"upstream":"github.com/acme/repo","path":"/src/repo"}]`,
	)
	inputs, err := resolveTaskctlRepositoryEnvironment(nil)
	if err != nil {
		t.Fatalf("canonical repository array rejected: %v", err)
	}
	if len(inputs) != 1 || inputs[0].Upstream != "github.com/acme/repo" ||
		inputs[0].Path != "/src/repo" {
		t.Fatalf("repository inputs = %#v", inputs)
	}
	if _, err := resolveTaskctlRepositoryEnvironment(
		repeatableStrings{"github.com/acme/repo=/src/repo"},
	); err == nil || !strings.Contains(err.Error(), "cannot be mixed") {
		t.Fatalf("mixed repository inputs error = %v", err)
	}

	for name, raw := range map[string]string{
		"unknown field":   `[{"upstream":"x","path":"/x","extra":true}]`,
		"duplicate key":   `[{"upstream":"x","upstream":"y","path":"/x"}]`,
		"wrong key order": `[{"path":"/x","upstream":"x"}]`,
	} {
		t.Run(name, func(t *testing.T) {
			t.Setenv(taskctlRepositoriesJSONEnvironment, raw)
			if _, err := resolveTaskctlRepositoryEnvironment(nil); err == nil {
				t.Fatalf("invalid repository array %q was accepted", raw)
			}
		})
	}
}
