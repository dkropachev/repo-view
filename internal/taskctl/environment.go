package taskctl

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	taskctlOutputEnvironment                   = "TASKCTL_OUTPUT"
	taskctlInputEnvironment                    = "TASKCTL_INPUT"
	taskctlInputSHA256Environment              = "TASKCTL_INPUT_SHA256"
	taskctlGitExecutableEnvironment            = "TASKCTL_GIT_EXECUTABLE"
	taskctlGitSHA256Environment                = "TASKCTL_GIT_SHA256"
	taskctlRepositoriesJSONEnvironment         = "TASKCTL_REPOSITORIES_JSON"
	taskctlRepositoryBindingsEnvironment       = "TASKCTL_REPOSITORY_BINDINGS"
	taskctlRepositoryBindingsSHA256Environment = "TASKCTL_REPOSITORY_BINDINGS_SHA256"
	taskctlSourceSelectionsEnvironment         = "TASKCTL_SOURCE_SELECTIONS"
	taskctlSourceSelectionsSHA256Environment   = "TASKCTL_SOURCE_SELECTIONS_SHA256"

	maximumTaskctlEnvironmentJSONBytes = 1 << 20
	maximumTaskctlEnvironmentPathBytes = 4096
	maximumTaskctlEnvironmentHashBytes = 64
)

type sourceAuditRepositoryBindingEnvironment struct {
	Upstream string `json:"upstream"`
	Path     string `json:"path"`
}

func taskctlEnvironmentString(name string) string {
	return os.Getenv(name)
}

func validateTaskctlScalarEnvironments(verb, role string, arguments []string) error {
	pathNames := []string(nil)
	hashNames := []string(nil)
	switch {
	case (verb == "generate" || verb == "validate") && role == "source-audit":
		pathNames = []string{
			taskctlRepositoryBindingsEnvironment,
			taskctlSourceSelectionsEnvironment,
			taskctlGitExecutableEnvironment,
		}
		if verb == "generate" {
			pathNames = append(pathNames, taskctlOutputEnvironment)
		} else {
			pathNames = append(pathNames, taskctlInputEnvironment)
		}
		hashNames = []string{
			taskctlRepositoryBindingsSHA256Environment,
			taskctlSourceSelectionsSHA256Environment,
			taskctlGitSHA256Environment,
		}
	case verb == "generate" && role == "source-repository-bindings":
		pathNames = []string{taskctlGitExecutableEnvironment, taskctlOutputEnvironment}
		hashNames = []string{taskctlGitSHA256Environment}
	case verb == "validate" && role == "source-repository-bindings":
		pathNames = []string{taskctlInputEnvironment, taskctlGitExecutableEnvironment}
		hashNames = []string{taskctlInputSHA256Environment, taskctlGitSHA256Environment}
	case verb == "validate" && role == "source-selections":
		pathNames = []string{taskctlInputEnvironment}
		hashNames = []string{taskctlInputSHA256Environment}
	case verb == "generate" && role == "source-selections":
		pathNames = []string{taskctlInputEnvironment, taskctlOutputEnvironment}
		hashNames = []string{taskctlInputSHA256Environment}
	}
	for _, name := range pathNames {
		if taskctlScalarFlagProvided(arguments, taskctlScalarEnvironmentFlag(name)) {
			continue
		}
		if err := validateTaskctlScalarEnvironment(name, maximumTaskctlEnvironmentPathBytes); err != nil {
			return err
		}
	}
	for _, name := range hashNames {
		if taskctlScalarFlagProvided(arguments, taskctlScalarEnvironmentFlag(name)) {
			continue
		}
		if err := validateTaskctlScalarEnvironment(name, maximumTaskctlEnvironmentHashBytes); err != nil {
			return err
		}
	}
	return nil
}

func taskctlScalarEnvironmentFlag(name string) string {
	switch name {
	case taskctlOutputEnvironment:
		return "output"
	case taskctlInputEnvironment:
		return "input"
	case taskctlInputSHA256Environment:
		return "input-sha256"
	case taskctlGitExecutableEnvironment:
		return "git-executable"
	case taskctlGitSHA256Environment:
		return "git-sha256"
	case taskctlRepositoryBindingsEnvironment:
		return "repository-bindings"
	case taskctlRepositoryBindingsSHA256Environment:
		return "repository-bindings-sha256"
	case taskctlSourceSelectionsEnvironment:
		return "source-selections"
	case taskctlSourceSelectionsSHA256Environment:
		return "source-selections-sha256"
	default:
		return ""
	}
}

func taskctlScalarFlagProvided(arguments []string, name string) bool {
	if name == "" {
		return false
	}
	wanted := "--" + name
	shortWanted := "-" + name
	for index := 0; index < len(arguments); index++ {
		argument := arguments[index]
		if argument == wanted || argument == shortWanted ||
			strings.HasPrefix(argument, wanted+"=") ||
			strings.HasPrefix(argument, shortWanted+"=") {
			return true
		}
		if strings.HasPrefix(argument, "-") && !strings.Contains(argument, "=") {
			index++
		}
	}
	return false
}

func validateTaskctlScalarEnvironment(name string, maximumBytes int) error {
	value, present := os.LookupEnv(name)
	if !present {
		return nil
	}
	return validateTaskctlScalarEnvironmentValue(name, value, maximumBytes)
}

func validateTaskctlScalarEnvironmentValue(name, value string, maximumBytes int) error {
	if len(value) > maximumBytes {
		return fmt.Errorf("%s exceeds %d bytes", name, maximumBytes)
	}
	for _, valueByte := range []byte(value) {
		if valueByte < 0x20 || valueByte == 0x7f {
			return fmt.Errorf("%s contains a control byte", name)
		}
	}
	return nil
}

func resolveTaskctlRepositoryEnvironment(
	explicit repeatableStrings,
) ([]SourceAuditRepositoryBindingInput, error) {
	raw, present := os.LookupEnv(taskctlRepositoriesJSONEnvironment)
	// Make exports unset documented inputs as empty values. Treat only that
	// empty value as absent; a caller
	// that wants an explicit empty repository set must provide canonical [].
	if raw == "" {
		present = false
	}
	if present && len(explicit) != 0 {
		return nil, fmt.Errorf(
			"%s and repeated --repository flags cannot be mixed",
			taskctlRepositoriesJSONEnvironment,
		)
	}
	if present {
		var values []sourceAuditRepositoryBindingEnvironment
		if err := decodeCanonicalTaskctlEnvironmentJSONArray(raw, &values); err != nil {
			return nil, err
		}
		inputs := make([]SourceAuditRepositoryBindingInput, 0, len(values))
		for _, value := range values {
			inputs = append(inputs, SourceAuditRepositoryBindingInput(value))
		}
		return inputs, nil
	}

	inputs := make([]SourceAuditRepositoryBindingInput, 0, len(explicit))
	for _, value := range explicit {
		upstream, path, found := splitRepositoryFlag(value)
		if !found {
			return nil, fmt.Errorf("repository binding %q must be UPSTREAM=PATH", value)
		}
		inputs = append(inputs, SourceAuditRepositoryBindingInput{Upstream: upstream, Path: path})
	}
	return inputs, nil
}

func splitRepositoryFlag(value string) (string, string, bool) {
	for index := range len(value) {
		if value[index] != '=' {
			continue
		}
		if index == 0 || index == len(value)-1 {
			return "", "", false
		}
		return value[:index], value[index+1:], true
	}
	return "", "", false
}

func decodeCanonicalTaskctlEnvironmentJSONArray(raw string, destination any) error {
	const name = taskctlRepositoriesJSONEnvironment
	if len(raw) == 0 {
		return fmt.Errorf("%s must be a nonempty canonical JSON array", name)
	}
	if len(raw) > maximumTaskctlEnvironmentJSONBytes {
		return fmt.Errorf(
			"%s exceeds %d bytes",
			name,
			maximumTaskctlEnvironmentJSONBytes,
		)
	}
	if raw[0] != '[' {
		return fmt.Errorf("%s must be a canonical JSON array", name)
	}
	data := []byte(raw)
	if err := validateUniqueJSONKeys(data); err != nil {
		return fmt.Errorf("decode %s: %w", name, err)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return fmt.Errorf("decode %s: %w", name, err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("decode %s: multiple JSON values", name)
		}
		return fmt.Errorf("decode trailing %s JSON: %w", name, err)
	}
	canonical, err := json.Marshal(destination)
	if err != nil {
		return fmt.Errorf("encode canonical %s: %w", name, err)
	}
	if !bytes.Equal(data, canonical) {
		return fmt.Errorf("%s is not canonical compact JSON", name)
	}
	return nil
}
