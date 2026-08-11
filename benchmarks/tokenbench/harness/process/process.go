// Package process adapts any executable that implements the tokenbench JSON
// adapter protocol. It is the stable integration path for non-Codex harnesses.
package process

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/dkropachev/repo-view/benchmarks/tokenbench/harness"
)

const (
	protocolVersion  = "tokenbench.external-adapter/v2"
	bridgeVersion    = "tokenbench.process-bridge/v1"
	defaultMaxOutput = 8 << 20
	defaultWaitDelay = 250 * time.Millisecond
)

// Adapter invokes one external adapter executable without a shell.
type Adapter struct {
	environment   map[string]string
	controlSHA256 string
	commandSHA256 string
	command       string
	kind          string
	arguments     []string
	timeout       time.Duration
	maxOutput     int
	waitDelay     time.Duration
}

// Config is common control-plane configuration for an external adapter.
type Config struct {
	Environment   map[string]string
	Command       string
	Kind          string
	Arguments     []string
	CommitmentKey []byte
	Timeout       time.Duration
	MaxOutput     int
}

// New constructs an external adapter. Command must be absolute, and Environment
// is complete rather than merged with the parent process environment.
func New(config Config) (*Adapter, error) {
	switch {
	case !validString(config.Command):
		return nil, errors.New("external adapter command contains invalid text")
	case !filepath.IsAbs(config.Command):
		return nil, errors.New("external adapter command must be an absolute path")
	case config.Kind == "":
		return nil, errors.New("external adapter kind is required")
	case !validString(config.Kind):
		return nil, errors.New("external adapter kind contains invalid text")
	case config.Timeout <= 0:
		return nil, errors.New("external adapter timeout must be positive")
	case len(config.Environment) != 0:
		return nil, errors.New("external adapter environment is prohibited")
	case len(config.CommitmentKey) != 0:
		return nil, errors.New("external adapter secret commitment keys are prohibited")
	}
	for index, argument := range config.Arguments {
		if !validString(argument) {
			return nil, fmt.Errorf(
				"external adapter argument %d contains invalid text",
				index,
			)
		}
	}
	for key, value := range config.Environment {
		if key == "" || strings.ContainsRune(key, '=') || !validString(key) {
			return nil, fmt.Errorf("invalid external adapter environment key %q", key)
		}
		if !validString(value) {
			return nil, fmt.Errorf(
				"external adapter environment value for %q contains invalid text",
				key,
			)
		}
	}
	maxOutput := config.MaxOutput
	if maxOutput == 0 {
		maxOutput = defaultMaxOutput
	}
	if maxOutput < 0 {
		return nil, errors.New("external adapter output limit must not be negative")
	}
	commandDigest, err := executableSHA256(config.Command)
	if err != nil {
		return nil, err
	}
	controlDigest, err := configurationSHA256(config, commandDigest, maxOutput)
	if err != nil {
		return nil, err
	}
	return &Adapter{
		environment:   cloneMap(config.Environment),
		controlSHA256: controlDigest,
		commandSHA256: commandDigest,
		arguments:     append([]string(nil), config.Arguments...),
		command:       filepath.Clean(config.Command),
		kind:          config.Kind,
		timeout:       config.Timeout,
		maxOutput:     maxOutput,
		waitDelay:     defaultWaitDelay,
	}, nil
}

// Kind implements harness.Adapter.
func (adapter *Adapter) Kind() string {
	return adapter.kind
}

// Resolve implements harness.Adapter.
func (adapter *Adapter) Resolve(
	ctx context.Context,
	request harness.ResolveRequest,
) (harness.Identity, error) {
	response, err := adapter.call(ctx, wireRequest{
		ProtocolVersion: protocolVersion,
		Operation:       "resolve",
		Resolve:         &request,
	})
	if err != nil {
		return harness.Identity{}, err
	}
	if response.Identity == nil {
		return harness.Identity{}, errors.New("external adapter omitted identity")
	}
	// These commitments are wrapper-owned: the child cannot reliably know the
	// exact executable bytes or private parent-side control configuration.
	response.Identity.AdapterExecutableSHA256 = adapter.commandSHA256
	response.Identity.AdapterControlConfigSHA256 = adapter.controlSHA256
	if response.Identity.Kind != adapter.kind {
		return harness.Identity{}, fmt.Errorf(
			"external adapter returned kind %q, want %q",
			response.Identity.Kind,
			adapter.kind,
		)
	}
	if err := harness.ValidateIdentity(*response.Identity); err != nil {
		return harness.Identity{}, fmt.Errorf("external adapter identity: %w", err)
	}
	return *response.Identity, nil
}

// MCPArguments implements harness.Adapter.
func (adapter *Adapter) MCPArguments(
	ctx context.Context,
	server harness.MCPServer,
) ([]string, error) {
	response, err := adapter.call(ctx, wireRequest{
		MCPServer:       &server,
		ProtocolVersion: protocolVersion,
		Operation:       "mcp_arguments",
	})
	if err != nil {
		return nil, err
	}
	if err := validateArguments(response.Arguments); err != nil {
		return nil, fmt.Errorf("external adapter MCP arguments: %w", err)
	}
	return append([]string(nil), response.Arguments...), nil
}

func executableSHA256(path string) (string, error) {
	before, err := os.Lstat(path)
	if err != nil {
		return "", fmt.Errorf("inspect external adapter executable: %w", err)
	}
	if !before.Mode().IsRegular() || before.Mode().Perm()&0o111 == 0 {
		return "", errors.New("external adapter command is not an executable regular file")
	}
	if hasMultipleLinks(before) {
		return "", errors.New("external adapter command must not be hard-linked")
	}
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open external adapter executable: %w", err)
	}
	defer file.Close()
	opened, err := file.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return "", errors.New("external adapter executable changed before open")
	}
	hasher := sha256.New()
	if _, err := io.Copy(hasher, file); err != nil {
		return "", fmt.Errorf("read external adapter executable: %w", err)
	}
	openedAfter, err := file.Stat()
	if err != nil {
		return "", err
	}
	after, err := os.Lstat(path)
	if err != nil || !os.SameFile(before, openedAfter) || !os.SameFile(before, after) ||
		before.Size() != openedAfter.Size() || before.Size() != after.Size() ||
		!before.ModTime().Equal(openedAfter.ModTime()) ||
		!before.ModTime().Equal(after.ModTime()) {
		return "", errors.New("external adapter executable changed while hashing")
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func configurationSHA256(
	config Config,
	commandSHA256 string,
	maxOutput int,
) (string, error) {
	canonical := struct {
		Environment     map[string]string `json:"environment"`
		Command         string            `json:"command"`
		CommandSHA256   string            `json:"command_sha256"`
		Kind            string            `json:"kind"`
		ProtocolVersion string            `json:"protocol_version"`
		BridgeVersion   string            `json:"bridge_version"`
		Arguments       []string          `json:"arguments"`
		TimeoutNanos    int64             `json:"timeout_nanos"`
		MaxOutput       int               `json:"max_output"`
		WaitDelayNanos  int64             `json:"wait_delay_nanos"`
	}{
		Environment:     cloneMap(config.Environment),
		Command:         filepath.Clean(config.Command),
		CommandSHA256:   commandSHA256,
		Kind:            config.Kind,
		Arguments:       append([]string(nil), config.Arguments...),
		ProtocolVersion: protocolVersion,
		BridgeVersion:   bridgeVersion,
		TimeoutNanos:    config.Timeout.Nanoseconds(),
		MaxOutput:       maxOutput,
		WaitDelayNanos:  defaultWaitDelay.Nanoseconds(),
	}
	raw, err := json.Marshal(canonical)
	if err != nil {
		return "", fmt.Errorf("encode external adapter configuration: %w", err)
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

// Build implements harness.Adapter.
func (adapter *Adapter) Build(
	ctx context.Context,
	invocation harness.Invocation,
) (harness.ProcessSpec, error) {
	if len(invocation.MCPServers) != 0 {
		return harness.ProcessSpec{}, errors.New(
			"external adapter Build accepts only the common invocation; use MCPArguments for registration",
		)
	}
	if invocation.HarnessIdentity.Kind != adapter.kind {
		return harness.ProcessSpec{}, fmt.Errorf(
			"invocation adapter kind %q does not match external adapter kind %q",
			invocation.HarnessIdentity.Kind,
			adapter.kind,
		)
	}
	if invocation.HarnessIdentity.AdapterExecutableSHA256 != adapter.commandSHA256 {
		return harness.ProcessSpec{}, errors.New(
			"invocation adapter executable digest does not match external adapter",
		)
	}
	if invocation.HarnessIdentity.AdapterControlConfigSHA256 != adapter.controlSHA256 {
		return harness.ProcessSpec{}, errors.New(
			"invocation adapter control configuration digest does not match external adapter",
		)
	}
	response, err := adapter.call(ctx, wireRequest{
		ProtocolVersion: protocolVersion,
		Operation:       "build",
		Invocation:      &invocation,
	})
	if err != nil {
		return harness.ProcessSpec{}, err
	}
	if response.Process == nil {
		return harness.ProcessSpec{}, errors.New("external adapter omitted process")
	}
	if err := validateProcessStrings(*response.Process); err != nil {
		return harness.ProcessSpec{}, fmt.Errorf("external adapter process: %w", err)
	}
	if err := harness.ValidateProcessSpec(*response.Process); err != nil {
		return harness.ProcessSpec{}, fmt.Errorf("external adapter process: %w", err)
	}
	return *response.Process, nil
}

// Decode implements harness.Adapter.
func (adapter *Adapter) Decode(
	ctx context.Context,
	execution harness.RawExecution,
) (harness.Observation, error) {
	response, err := adapter.call(ctx, wireRequest{
		ProtocolVersion: protocolVersion,
		Operation:       "decode",
		Execution:       &execution,
	})
	if err != nil {
		return harness.Observation{}, err
	}
	if response.Observation == nil {
		return harness.Observation{}, errors.New("external adapter omitted observation")
	}
	if err := harness.ValidateUsage(response.Observation.Usage); err != nil {
		return harness.Observation{}, fmt.Errorf("external adapter observation: %w", err)
	}
	return harness.CloneObservation(*response.Observation), nil
}

type wireRequest struct {
	Resolve         *harness.ResolveRequest `json:"resolve,omitempty"`
	Invocation      *harness.Invocation     `json:"invocation,omitempty"`
	Execution       *harness.RawExecution   `json:"execution,omitempty"`
	MCPServer       *harness.MCPServer      `json:"mcp_server,omitempty"`
	ProtocolVersion string                  `json:"protocol_version"`
	Operation       string                  `json:"operation"`
}

type wireResponse struct {
	Identity        *harness.Identity    `json:"identity,omitempty"`
	Process         *harness.ProcessSpec `json:"process,omitempty"`
	Observation     *harness.Observation `json:"observation,omitempty"`
	ProtocolVersion string               `json:"protocol_version"`
	Error           string               `json:"error,omitempty"`
	Arguments       []string             `json:"arguments,omitempty"`
}

func (adapter *Adapter) call(
	ctx context.Context,
	request wireRequest,
) (wireResponse, error) {
	currentDigest, err := executableSHA256(adapter.command)
	if err != nil {
		return wireResponse{}, err
	}
	if currentDigest != adapter.commandSHA256 {
		return wireResponse{}, errors.New("external adapter executable changed after resolution")
	}
	input, err := json.Marshal(request)
	if err != nil {
		return wireResponse{}, fmt.Errorf("encode external adapter request: %w", err)
	}
	callContext, cancel := context.WithTimeout(ctx, adapter.timeout)
	defer cancel()
	command := exec.CommandContext(callContext, adapter.command, adapter.arguments...)
	command.Dir = filepath.Dir(adapter.command)
	command.Env = environmentList(adapter.environment)
	command.Stdin = bytes.NewReader(input)
	stdout := newLimitBuffer(adapter.maxOutput)
	stderr := newLimitBuffer(adapter.maxOutput)
	command.Stdout = stdout
	command.Stderr = stderr
	command.WaitDelay = adapter.waitDelay
	isolateCommand(command)
	runErr := command.Run()
	cleanupCommandGroup(command)
	afterDigest, verifyErr := executableSHA256(adapter.command)
	if verifyErr != nil {
		return wireResponse{}, fmt.Errorf(
			"reverify external adapter executable: %w",
			verifyErr,
		)
	}
	if afterDigest != adapter.commandSHA256 {
		return wireResponse{}, errors.New(
			"external adapter executable changed during invocation",
		)
	}
	if runErr != nil {
		if errors.Is(stdout.err, errOutputLimit) || errors.Is(stderr.err, errOutputLimit) {
			return wireResponse{}, errors.New("external adapter output exceeded limit")
		}
		if errors.Is(callContext.Err(), context.DeadlineExceeded) {
			return wireResponse{}, errors.New("external adapter timed out")
		}
		return wireResponse{}, errors.New("external adapter process failed")
	}
	if stderr.buffer.Len() != 0 {
		return wireResponse{}, errors.New("external adapter wrote to stderr")
	}
	if !utf8.Valid(stdout.buffer.Bytes()) {
		return wireResponse{}, errors.New(
			"decode external adapter response: response is not valid UTF-8",
		)
	}
	if err := rejectDuplicateKeys(stdout.buffer.Bytes()); err != nil {
		return wireResponse{}, fmt.Errorf("decode external adapter response: %w", err)
	}

	var response wireResponse
	decoder := json.NewDecoder(bytes.NewReader(stdout.buffer.Bytes()))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&response); err != nil {
		return wireResponse{}, fmt.Errorf("decode external adapter response: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return wireResponse{}, errors.New("external adapter returned multiple JSON values")
		}
		return wireResponse{}, fmt.Errorf("decode external adapter response: %w", err)
	}
	if response.ProtocolVersion != protocolVersion {
		return wireResponse{}, fmt.Errorf(
			"external adapter protocol %q, want %q",
			response.ProtocolVersion,
			protocolVersion,
		)
	}
	if err := validateResponseShape(request.Operation, response); err != nil {
		return wireResponse{}, err
	}
	if response.Error != "" {
		return wireResponse{}, errors.New("external adapter reported an error")
	}
	return response, nil
}

func validateResponseShape(operation string, response wireResponse) error {
	if response.Error != "" {
		if response.Identity != nil || response.Process != nil ||
			response.Observation != nil || len(response.Arguments) != 0 {
			return errors.New("external adapter returned fields alongside an error")
		}
		return nil
	}
	switch operation {
	case "resolve":
		if response.Identity == nil || response.Process != nil ||
			response.Observation != nil || len(response.Arguments) != 0 {
			return errors.New("external adapter returned invalid resolve response shape")
		}
	case "build":
		if response.Identity != nil || response.Process == nil ||
			response.Observation != nil || len(response.Arguments) != 0 {
			return errors.New("external adapter returned invalid build response shape")
		}
	case "decode":
		if response.Identity != nil || response.Process != nil ||
			response.Observation == nil || len(response.Arguments) != 0 {
			return errors.New("external adapter returned invalid decode response shape")
		}
	case "mcp_arguments":
		if response.Identity != nil || response.Process != nil ||
			response.Observation != nil || len(response.Arguments) == 0 {
			return errors.New("external adapter returned invalid MCP arguments response shape")
		}
	default:
		return fmt.Errorf("unsupported external adapter operation %q", operation)
	}
	return nil
}

func rejectDuplicateKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("object key is not a string")
				}
				if _, exists := seen[key]; exists {
					return fmt.Errorf("duplicate object key %q", key)
				}
				seen[key] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
			_, err := decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err := decoder.Token()
			return err
		default:
			return fmt.Errorf("unexpected delimiter %q", delimiter)
		}
	}
	return walk()
}

func environmentList(environment map[string]string) []string {
	keys := make([]string, 0, len(environment))
	for key := range environment {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	result := make([]string, 0, len(keys))
	for _, key := range keys {
		result = append(result, key+"="+environment[key])
	}
	return result
}

func cloneMap(source map[string]string) map[string]string {
	clone := make(map[string]string, len(source))
	for key, value := range source {
		clone[key] = value
	}
	return clone
}

func validString(value string) bool {
	return utf8.ValidString(value) && !strings.ContainsRune(value, '\x00')
}

func validateArguments(arguments []string) error {
	for index, argument := range arguments {
		if !validString(argument) {
			return fmt.Errorf("argument %d contains invalid text", index)
		}
	}
	return nil
}

func validateProcessStrings(process harness.ProcessSpec) error {
	if !validString(process.Directory) {
		return errors.New("directory contains invalid text")
	}
	if err := validateArguments(process.Argv); err != nil {
		return err
	}
	for key, value := range process.Environment {
		if !validString(key) {
			return fmt.Errorf("environment key %q contains invalid text", key)
		}
		if !validString(value) {
			return fmt.Errorf("environment value for %q contains invalid text", key)
		}
	}
	return nil
}

var errOutputLimit = errors.New("output limit exceeded")

type limitBuffer struct {
	err    error
	buffer bytes.Buffer
	limit  int
}

func newLimitBuffer(limit int) *limitBuffer {
	return &limitBuffer{limit: limit}
}

func (buffer *limitBuffer) Write(content []byte) (int, error) {
	if len(content) == 0 {
		return 0, nil
	}
	remaining := buffer.limit - buffer.buffer.Len()
	if remaining <= 0 {
		buffer.err = errOutputLimit
		return 0, errOutputLimit
	}
	if len(content) > remaining {
		written, _ := buffer.buffer.Write(content[:remaining])
		buffer.err = errOutputLimit
		return written, errOutputLimit
	}
	return buffer.buffer.Write(content)
}

var _ harness.Adapter = (*Adapter)(nil)
