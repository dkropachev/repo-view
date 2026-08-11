package tokenbench

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/scopesifter/scopesifter/benchmarks/tokenbench/harness"
)

const paritySchemaVersion = "tokenbench.parity/v2"

// ProveParity verifies exact semantic equality after removing the one allowed
// candidate MCP registration. No broad ignore list is used.
func ProveParity(
	baseline harness.Invocation,
	candidate harness.Invocation,
) (ParityProof, error) {
	if err := validateInvocation(baseline); err != nil {
		return ParityProof{}, fmt.Errorf("baseline invocation: %w", err)
	}
	if err := validateInvocation(candidate); err != nil {
		return ParityProof{}, fmt.Errorf("candidate invocation: %w", err)
	}
	if baseline.MCPServers == nil || len(baseline.MCPServers) != 0 {
		return ParityProof{}, errors.New("baseline must have no MCP server registrations")
	}
	if len(candidate.MCPServers) != 1 {
		return ParityProof{}, fmt.Errorf(
			"candidate must have exactly one MCP server registration, got %d",
			len(candidate.MCPServers),
		)
	}
	registration := candidate.MCPServers[0]
	if err := validateScopeSifterRegistration(registration, candidate); err != nil {
		return ParityProof{}, err
	}

	baselineCommon := cloneInvocation(baseline)
	candidateCommon := cloneInvocation(candidate)
	baselineCommon.MCPServers = nil
	candidateCommon.MCPServers = nil
	if !reflect.DeepEqual(baselineCommon, candidateCommon) {
		pointers, err := differentJSONPointers(baselineCommon, candidateCommon)
		if err != nil {
			return ParityProof{}, err
		}
		return ParityProof{}, fmt.Errorf(
			"forbidden baseline/candidate differences at %v",
			pointers,
		)
	}

	commonJSON, err := json.Marshal(baselineCommon)
	if err != nil {
		return ParityProof{}, fmt.Errorf("marshal common invocation: %w", err)
	}
	baselineJSON, err := json.Marshal(baseline)
	if err != nil {
		return ParityProof{}, fmt.Errorf("marshal baseline invocation: %w", err)
	}
	candidateJSON, err := json.Marshal(candidate)
	if err != nil {
		return ParityProof{}, fmt.Errorf("marshal candidate invocation: %w", err)
	}
	registrationJSON, err := json.Marshal(registration)
	if err != nil {
		return ParityProof{}, fmt.Errorf("marshal scopesifter registration: %w", err)
	}
	emptyServersJSON, err := json.Marshal(baseline.MCPServers)
	if err != nil {
		return ParityProof{}, fmt.Errorf("marshal baseline registrations: %w", err)
	}
	candidateServersJSON, err := json.Marshal(candidate.MCPServers)
	if err != nil {
		return ParityProof{}, fmt.Errorf("marshal candidate registrations: %w", err)
	}

	return ParityProof{
		Differences: []Difference{{
			JSONPointer:     "/mcp_servers",
			Authorization:   "candidate-only required read-only scopesifter MCP registration",
			BaselineSHA256:  SHA256(emptyServersJSON),
			CandidateSHA256: SHA256(candidateServersJSON),
		}},
		SchemaVersion:                 paritySchemaVersion,
		CommonInvocationSHA256:        SHA256(commonJSON),
		BaselineInvocationSHA256:      SHA256(baselineJSON),
		CandidateInvocationSHA256:     SHA256(candidateJSON),
		PromptSHA256:                  SHA256(baseline.Prompt),
		ScopeSifterRegistrationSHA256: SHA256(registrationJSON),
	}, nil
}

func validateInvocation(invocation harness.Invocation) error {
	switch {
	case len(invocation.Prompt) == 0:
		return errors.New("prompt must not be empty")
	case !utf8.Valid(invocation.Prompt):
		return errors.New("prompt must be valid UTF-8")
	case invocation.Environment == nil:
		return errors.New("process environment must be a canonical object")
	case invocation.Arguments == nil:
		return errors.New("harness arguments must be a canonical empty array")
	case len(invocation.Arguments) != 0:
		return errors.New("authored harness arguments are not allowed")
	case !filepath.IsAbs(invocation.Executable):
		return errors.New("harness executable must be absolute")
	case filepath.Clean(invocation.Executable) != invocation.Executable:
		return errors.New("harness executable path must be canonical")
	case !ValidSHA256(invocation.ExecutableSHA256):
		return errors.New("harness executable digest is invalid")
	case !isPinnedModel(invocation.RequestedModel):
		return errors.New("requested model is not explicit")
	case strings.TrimSpace(invocation.Model) == "":
		return errors.New("resolved model is required")
	case invocation.Model != invocation.RequestedModel:
		return errors.New("resolved model does not match requested model")
	case !validModelRevision(invocation.ModelRevision):
		return errors.New("resolved model revision is invalid")
	case !strings.HasPrefix(invocation.ModelRevision, invocation.Model+"@"):
		return errors.New("resolved model revision does not identify the resolved model")
	case invocation.ReasoningEffort == "":
		return errors.New("reasoning effort is required")
	case invocation.PermissionProfile != "read-only":
		return errors.New("permission profile must be read-only")
	case !filepath.IsAbs(invocation.WorkingDirectory):
		return errors.New("working directory must be absolute")
	case filepath.Clean(invocation.WorkingDirectory) != invocation.WorkingDirectory:
		return errors.New("working directory must be canonical")
	case !validGitObjectID(invocation.SourceRevision):
		return errors.New("source revision is invalid")
	case !validGitObjectID(invocation.SourceBaseRevision):
		return errors.New("source base revision is invalid")
	case !ValidSHA256(invocation.SourceTreeSHA256):
		return errors.New("source tree digest is invalid")
	case !filepath.IsAbs(invocation.GitExecutable):
		return errors.New("git executable must be absolute")
	case filepath.Clean(invocation.GitExecutable) != invocation.GitExecutable:
		return errors.New("git executable path must be canonical")
	case !ValidSHA256(invocation.GitExecutableSHA256):
		return errors.New("git executable digest is invalid")
	case !ValidSHA256(invocation.GitMetadataSHA256):
		return errors.New("git metadata digest is invalid")
	case !filepath.IsAbs(invocation.RunnerExecutable):
		return errors.New("tokenbench runner executable must be absolute")
	case filepath.Clean(invocation.RunnerExecutable) != invocation.RunnerExecutable:
		return errors.New("tokenbench runner executable path must be canonical")
	case !ValidSHA256(invocation.RunnerExecutableSHA256):
		return errors.New("tokenbench runner executable digest is invalid")
	case invocation.TimeoutMillis <= 0:
		return errors.New("timeout must be positive")
	}
	if err := harness.ValidatePublishableEnvironment(invocation.Environment); err != nil {
		return err
	}
	if err := harness.ValidateIdentity(invocation.HarnessIdentity); err != nil {
		return err
	}
	switch {
	case invocation.HarnessIdentity.ExecutableSHA256 != invocation.ExecutableSHA256:
		return errors.New("resolved identity executable digest does not match invocation")
	case invocation.HarnessIdentity.Model != invocation.Model:
		return errors.New("resolved identity model does not match invocation")
	case invocation.HarnessIdentity.ModelRevision != invocation.ModelRevision:
		return errors.New("resolved identity model revision does not match invocation")
	case invocation.HarnessIdentity.ReasoningEffort != invocation.ReasoningEffort:
		return errors.New("resolved identity reasoning does not match invocation")
	default:
		return nil
	}
}

func validateScopeSifterRegistration(
	registration harness.MCPServer,
	invocation harness.Invocation,
) error {
	gitArguments := []string{
		"mcp",
		"--root", invocation.WorkingDirectory,
		"--base", invocation.SourceBaseRevision,
		"--git", invocation.GitExecutable,
		"--git-sha256", invocation.GitExecutableSHA256,
	}
	cachePath := filepath.Join(
		filepath.Dir(invocation.WorkingDirectory),
		"cache",
		"changed-state.json",
	)
	cacheArguments := []string{
		"mcp",
		"--root", invocation.WorkingDirectory,
		"--base", invocation.SourceBaseRevision,
		"--head", invocation.SourceRevision,
		"--changed-state-cache", cachePath,
		"--changed-state-cache-sha256", "",
	}
	validArguments := reflect.DeepEqual(registration.Arguments, gitArguments)
	if len(registration.Arguments) == len(cacheArguments) &&
		ValidSHA256(registration.Arguments[len(registration.Arguments)-1]) {
		cacheArguments[len(cacheArguments)-1] = registration.Arguments[len(registration.Arguments)-1]
		validArguments = reflect.DeepEqual(registration.Arguments, cacheArguments)
	}
	switch {
	case registration.Name != "scopesifter":
		return fmt.Errorf(
			"candidate MCP server must be named scopesifter, got %q",
			registration.Name,
		)
	case !filepath.IsAbs(registration.Command):
		return errors.New("scopesifter MCP command must be an absolute path")
	case filepath.Clean(registration.Command) != registration.Command:
		return errors.New("scopesifter MCP command must be a canonical path")
	case !ValidSHA256(registration.ExecutableSHA256):
		return errors.New("scopesifter MCP executable digest is invalid")
	case !registration.Required:
		return errors.New("scopesifter MCP server must be required")
	case !registration.ReadOnly:
		return errors.New("scopesifter MCP server must be declared read-only")
	case registration.Environment == nil || len(registration.Environment) != 0:
		return errors.New("scopesifter MCP registration must not contain environment hints")
	case !validArguments:
		return fmt.Errorf(
			"scopesifter MCP arguments must be a code-owned Git or cache-only form, got %q",
			registration.Arguments,
		)
	default:
		return nil
	}
}

func differentJSONPointers(left, right any) ([]string, error) {
	leftJSON, err := json.Marshal(left)
	if err != nil {
		return nil, err
	}
	rightJSON, err := json.Marshal(right)
	if err != nil {
		return nil, err
	}
	if bytes.Equal(leftJSON, rightJSON) {
		return nil, nil
	}
	var leftValue, rightValue any
	if err := json.Unmarshal(leftJSON, &leftValue); err != nil {
		return nil, err
	}
	if err := json.Unmarshal(rightJSON, &rightValue); err != nil {
		return nil, err
	}
	pointers := make([]string, 0)
	collectJSONDifferences("", leftValue, rightValue, &pointers)
	return pointers, nil
}

func collectJSONDifferences(path string, left, right any, output *[]string) {
	if reflect.DeepEqual(left, right) {
		return
	}
	leftObject, leftOK := left.(map[string]any)
	rightObject, rightOK := right.(map[string]any)
	if leftOK && rightOK {
		keys := make(map[string]struct{}, len(leftObject)+len(rightObject))
		for key := range leftObject {
			keys[key] = struct{}{}
		}
		for key := range rightObject {
			keys[key] = struct{}{}
		}
		sortedKeys := make([]string, 0, len(keys))
		for key := range keys {
			sortedKeys = append(sortedKeys, key)
		}
		sort.Strings(sortedKeys)
		for _, key := range sortedKeys {
			collectJSONDifferences(
				path+"/"+escapeJSONPointer(key),
				leftObject[key],
				rightObject[key],
				output,
			)
		}
		return
	}
	if path == "" {
		path = "/"
	}
	*output = append(*output, path)
}

func escapeJSONPointer(value string) string {
	value = bytes.NewBufferString(value).String()
	value = string(bytes.ReplaceAll([]byte(value), []byte("~"), []byte("~0")))
	return string(bytes.ReplaceAll([]byte(value), []byte("/"), []byte("~1")))
}
