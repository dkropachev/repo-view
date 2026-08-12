package process

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/yapless/scopesifter/benchmarks/tokenbench/harness"
	"github.com/yapless/scopesifter/benchmarks/tokenbench/harness/conformance"
)

func TestExternalAdapterConformance(t *testing.T) {
	t.Parallel()
	adapter := helperAdapter(t, "ok")
	resolve := harness.ResolveRequest{
		Environment:            map[string]string{"LANG": "C"},
		Executable:             "/bin/echo",
		ExecutableSHA256:       strings.Repeat("1", 64),
		Model:                  "fixed-model",
		ExpectedModelRevision:  "fixed-model@revision-v1",
		ReasoningEffort:        "medium",
		PermissionProfile:      "read-only",
		DeveloperInstructions:  "common",
		WorkingDirectory:       "/tmp",
		SourceRevision:         strings.Repeat("1", 40),
		SourceBaseRevision:     strings.Repeat("0", 40),
		SourceTreeSHA256:       strings.Repeat("2", 64),
		GitExecutable:          "/usr/bin/git",
		GitExecutableSHA256:    strings.Repeat("3", 64),
		GitMetadataSHA256:      strings.Repeat("4", 64),
		RunnerExecutable:       "/usr/bin/tokenbench",
		RunnerExecutableSHA256: strings.Repeat("5", 64),
		TimeoutMillis:          1000,
	}
	identity, err := adapter.Resolve(context.Background(), resolve)
	if err != nil {
		t.Fatal(err)
	}
	invocation := harness.Invocation{
		Environment:            map[string]string{"LANG": "C"},
		Arguments:              []string{"exec"},
		MCPServers:             []harness.MCPServer{},
		Prompt:                 []byte("prompt\n"),
		HarnessIdentity:        identity,
		Executable:             resolve.Executable,
		ExecutableSHA256:       resolve.ExecutableSHA256,
		Model:                  "fixed-model",
		RequestedModel:         resolve.Model,
		ModelRevision:          resolve.ExpectedModelRevision,
		ReasoningEffort:        resolve.ReasoningEffort,
		PermissionProfile:      resolve.PermissionProfile,
		DeveloperInstructions:  "common",
		WorkingDirectory:       "/tmp",
		SourceRevision:         strings.Repeat("1", 40),
		SourceBaseRevision:     strings.Repeat("0", 40),
		SourceTreeSHA256:       strings.Repeat("2", 64),
		GitExecutable:          "/usr/bin/git",
		GitExecutableSHA256:    strings.Repeat("3", 64),
		GitMetadataSHA256:      strings.Repeat("4", 64),
		RunnerExecutable:       "/usr/bin/tokenbench",
		RunnerExecutableSHA256: strings.Repeat("5", 64),
		TimeoutMillis:          resolve.TimeoutMillis,
	}
	conformance.Run(t, conformance.Fixture{
		Adapter:    adapter,
		Resolve:    resolve,
		Invocation: invocation,
		Execution: harness.RawExecution{
			Stdout:   []byte("raw"),
			ExitCode: 0,
		},
	})
	candidate := invocation
	candidate.Environment = map[string]string{"LANG": "C"}
	candidate.Arguments = append([]string(nil), invocation.Arguments...)
	candidate.Prompt = append([]byte(nil), invocation.Prompt...)
	candidate.MCPServers = []harness.MCPServer{{
		Environment:      map[string]string{},
		Name:             "scopesifter",
		Command:          "/tools/scopesifter",
		ExecutableSHA256: strings.Repeat("6", 64),
		Arguments:        []string{"mcp", "--root", "/tmp", "--base", strings.Repeat("0", 40)},
		Required:         true,
		ReadOnly:         true,
	}}
	conformance.RunPair(t, adapter, invocation, candidate)
}

func TestExternalAdapterBuildBindsIdentity(t *testing.T) {
	t.Parallel()
	adapter := helperAdapter(t, "ok")
	request := resolveRequestFixture()
	identity, err := adapter.Resolve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}

	for name, mutate := range map[string]func(*harness.Identity){
		"kind": func(value *harness.Identity) {
			value.Kind = "other-adapter"
		},
		"executable digest": func(value *harness.Identity) {
			value.AdapterExecutableSHA256 = strings.Repeat("f", 64)
		},
		"control configuration digest": func(value *harness.Identity) {
			value.AdapterControlConfigSHA256 = strings.Repeat("f", 64)
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			foreign := identity
			mutate(&foreign)
			_, err := adapter.Build(context.Background(), harness.Invocation{
				HarnessIdentity: foreign,
			})
			if err == nil || !strings.Contains(err.Error(), "does not match") {
				t.Fatalf("expected identity binding error, got %v", err)
			}
		})
	}
}

func TestExternalAdapterUsesExecutableDirectory(t *testing.T) {
	t.Parallel()
	adapter := helperAdapter(t, "check-cwd")
	if _, err := adapter.Resolve(context.Background(), resolveRequestFixture()); err != nil {
		t.Fatal(err)
	}
}

func TestExternalAdapterRejectsInvalidConfigurationStrings(t *testing.T) {
	t.Parallel()
	base := func() Config {
		return Config{
			Environment: map[string]string{},
			Arguments:   []string{"--adapter"},
			Command:     os.Args[0],
			Kind:        "external-fixture",
			Timeout:     time.Second,
		}
	}
	for name, mutate := range map[string]func(*Config){
		"command NUL": func(config *Config) {
			config.Command += "\x00suffix"
		},
		"kind invalid UTF-8": func(config *Config) {
			config.Kind = string([]byte{0xff})
		},
		"argument NUL": func(config *Config) {
			config.Arguments = []string{"bad\x00argument"}
		},
		"environment key NUL": func(config *Config) {
			config.Environment = map[string]string{"BAD\x00KEY": "value"}
		},
		"environment value NUL": func(config *Config) {
			config.Environment = map[string]string{"KEY": "bad\x00value"}
		},
		"environment prohibited": func(config *Config) {
			config.Environment = map[string]string{"KEY": "value"}
		},
		"commitment key prohibited": func(config *Config) {
			config.CommitmentKey = []byte(strings.Repeat("k", 32))
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			config := base()
			mutate(&config)
			if _, err := New(config); err == nil {
				t.Fatal("expected invalid configuration error")
			}
		})
	}
}

func TestExternalAdapterRejectsHardLinkedExecutable(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	command := filepath.Join(directory, "adapter")
	if err := os.WriteFile(command, []byte("adapter"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(command, filepath.Join(directory, "alias")); err != nil {
		t.Fatal(err)
	}
	if _, err := New(Config{
		Command: command,
		Kind:    "external-fixture",
		Timeout: time.Second,
	}); err == nil || !strings.Contains(err.Error(), "hard-linked") {
		t.Fatalf("hard-linked adapter was accepted: %v", err)
	}
}

func TestExternalAdapterRejectsScriptRuntimeExecutable(t *testing.T) {
	t.Parallel()
	if _, err := New(Config{
		Command: "/tmp/bash",
		Kind:    "external-fixture",
		Timeout: time.Second,
	}); err == nil || !strings.Contains(err.Error(), "script runtime") {
		t.Fatalf("script-runtime adapter was accepted: %v", err)
	}
}

func TestExternalAdapterRejectsScriptRuntimeArgument(t *testing.T) {
	t.Parallel()
	if _, err := New(Config{
		Command:   "/usr/bin/env",
		Arguments: []string{"bash", "-c", "true"},
		Kind:      "external-fixture",
		Timeout:   time.Second,
	}); err == nil || !strings.Contains(err.Error(), "script runtime") {
		t.Fatalf("script-runtime adapter argument was accepted: %v", err)
	}
}

func TestExternalAdapterRejectsInvalidProcessStrings(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{
		"process-argv-nul",
		"process-directory-nul",
		"process-environment-key-nul",
		"process-environment-value-nul",
	} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			adapter := helperAdapter(t, mode)
			request := resolveRequestFixture()
			identity, err := adapter.Resolve(context.Background(), request)
			if err != nil {
				t.Fatal(err)
			}
			_, err = adapter.Build(context.Background(), harness.Invocation{
				Environment:      map[string]string{},
				HarnessIdentity:  identity,
				Prompt:           []byte("prompt"),
				WorkingDirectory: "/tmp",
				TimeoutMillis:    1000,
			})
			if err == nil || !strings.Contains(err.Error(), "invalid text") {
				t.Fatalf("expected invalid process text error, got %v", err)
			}
		})
	}
}

func TestExternalAdapterRejectsInvalidMCPArgument(t *testing.T) {
	t.Parallel()
	adapter := helperAdapter(t, "mcp-argument-nul")
	_, err := adapter.MCPArguments(context.Background(), harness.MCPServer{Name: "scopesifter"})
	if err == nil || !strings.Contains(err.Error(), "invalid text") {
		t.Fatalf("expected invalid MCP argument error, got %v", err)
	}
}

func TestLimitBufferWriterContract(t *testing.T) {
	t.Parallel()
	buffer := newLimitBuffer(4)

	if written, err := buffer.Write(nil); written != 0 || err != nil {
		t.Fatalf("empty write = (%d, %v), want (0, nil)", written, err)
	}
	if written, err := buffer.Write([]byte("abcdef")); written != 4 ||
		!errors.Is(err, errOutputLimit) {
		t.Fatalf("overflow write = (%d, %v), want (4, limit error)", written, err)
	}
	if got := buffer.buffer.String(); got != "abcd" {
		t.Fatalf("buffer = %q, want %q", got, "abcd")
	}
	if written, err := buffer.Write([]byte("z")); written != 0 ||
		!errors.Is(err, errOutputLimit) {
		t.Fatalf("full write = (%d, %v), want (0, limit error)", written, err)
	}
}

func resolveRequestFixture() harness.ResolveRequest {
	return harness.ResolveRequest{
		Executable:             "/bin/echo",
		ExecutableSHA256:       strings.Repeat("1", 64),
		Model:                  "fixed-model",
		ExpectedModelRevision:  "fixed-model@revision-v1",
		ReasoningEffort:        "medium",
		PermissionProfile:      "read-only",
		DeveloperInstructions:  "common",
		WorkingDirectory:       "/tmp",
		SourceRevision:         strings.Repeat("1", 40),
		SourceBaseRevision:     strings.Repeat("0", 40),
		SourceTreeSHA256:       strings.Repeat("2", 64),
		GitExecutable:          "/usr/bin/git",
		GitExecutableSHA256:    strings.Repeat("3", 64),
		GitMetadataSHA256:      strings.Repeat("4", 64),
		RunnerExecutable:       "/usr/bin/tokenbench",
		RunnerExecutableSHA256: strings.Repeat("5", 64),
		TimeoutMillis:          1000,
	}
}

func TestExternalAdapterRejectsProtocolDrift(t *testing.T) {
	t.Parallel()
	if protocolVersion != "tokenbench.external-adapter/v2" {
		t.Fatalf("external adapter protocol = %q", protocolVersion)
	}
	for _, mode := range []string{"old-version", "bad-version"} {
		adapter := helperAdapter(t, mode)
		_, err := adapter.Resolve(context.Background(), harness.ResolveRequest{})
		if err == nil || !strings.Contains(err.Error(), "protocol") {
			t.Fatalf("%s: expected protocol error, got %v", mode, err)
		}
	}
}

func TestExternalAdapterV2PreservesProviderTotal(t *testing.T) {
	t.Parallel()
	observation, err := helperAdapter(t, "ok").Decode(
		context.Background(),
		harness.RawExecution{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if total := observation.Usage.ProviderTotalTokens; total == nil || *total != 13 {
		t.Fatalf("external adapter provider total = %v", total)
	}
}

func TestExternalAdapterRejectsStderr(t *testing.T) {
	t.Parallel()
	adapter := helperAdapter(t, "stderr")
	_, err := adapter.Resolve(context.Background(), harness.ResolveRequest{})
	if err == nil || !strings.Contains(err.Error(), "stderr") {
		t.Fatalf("expected stderr error, got %v", err)
	}
	if strings.Contains(err.Error(), "credential-looking-stderr-value") {
		t.Fatal("external adapter stderr leaked into the parent error")
	}
}

func TestExternalAdapterRejectsChildErrorWithoutLeakingIt(t *testing.T) {
	t.Parallel()
	adapter := helperAdapter(t, "response-error")
	_, err := adapter.Resolve(context.Background(), harness.ResolveRequest{})
	if err == nil || !strings.Contains(err.Error(), "reported an error") {
		t.Fatalf("expected fixed child error, got %v", err)
	}
	if strings.Contains(err.Error(), "credential-looking-response-value") {
		t.Fatal("external adapter response error leaked into the parent error")
	}
}

func TestExternalAdapterRejectsDuplicateResponseKeys(t *testing.T) {
	t.Parallel()
	adapter := helperAdapter(t, "duplicate")
	_, err := adapter.Resolve(context.Background(), harness.ResolveRequest{})
	if err == nil || !strings.Contains(err.Error(), "duplicate") {
		t.Fatalf("expected duplicate-key error, got %v", err)
	}
}

func TestExternalAdapterRejectsInvalidUTF8Response(t *testing.T) {
	t.Parallel()
	adapter := helperAdapter(t, "invalid-utf8")
	_, err := adapter.Resolve(context.Background(), harness.ResolveRequest{})
	if err == nil || !strings.Contains(err.Error(), "valid UTF-8") {
		t.Fatalf("expected UTF-8 error, got %v", err)
	}
}

func TestExternalAdapterOutputLimit(t *testing.T) {
	t.Parallel()
	adapter, err := New(Config{
		Arguments: []string{"-test.run=TestExternalAdapterHelperProcess", "--", "overflow"},
		Command:   os.Args[0],
		Kind:      "external-fixture",
		Timeout:   5 * time.Second,
		MaxOutput: 64,
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = adapter.Resolve(context.Background(), harness.ResolveRequest{})
	if err == nil || !strings.Contains(err.Error(), "limit") {
		t.Fatalf("expected output-limit error, got %v", err)
	}
}

func TestExternalAdapterBoundsDescendantPipeWait(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("test helper uses a Unix sleep descendant")
	}
	adapter := helperAdapter(t, "pipe-holder")
	started := time.Now()
	_, err := adapter.Resolve(context.Background(), resolveRequestFixture())
	if err == nil {
		t.Fatal("adapter descendant holding stdio pipes was accepted")
	}
	if elapsed := time.Since(started); elapsed > 2*time.Second {
		t.Fatalf("adapter pipe wait exceeded hard bound: %s: %v", elapsed, err)
	}
}

func helperAdapter(t *testing.T, mode string) *Adapter {
	t.Helper()
	adapter, err := New(Config{
		Arguments: []string{"-test.run=TestExternalAdapterHelperProcess", "--", mode},
		Command:   os.Args[0],
		Kind:      "external-fixture",
		Timeout:   5 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func TestExternalAdapterHelperProcess(t *testing.T) {
	mode := ""
	for index, argument := range os.Args {
		if argument == "--" && index+1 < len(os.Args) {
			mode = os.Args[index+1]
			break
		}
	}
	if mode == "" {
		return
	}
	if mode == "stderr" {
		_, _ = os.Stderr.WriteString("credential-looking-stderr-value")
	}
	if mode == "overflow" {
		_, _ = os.Stdout.WriteString(strings.Repeat("x", 1024))
		os.Exit(0)
	}
	if mode == "duplicate" {
		_, _ = os.Stdout.WriteString(`{"protocol_version":"tokenbench.external-adapter/v2","protocol_version":"wrong"}`)
		os.Exit(0)
	}
	if mode == "invalid-utf8" {
		_, _ = os.Stdout.Write([]byte{
			'{', '"', 'p', 'r', 'o', 't', 'o', 'c', 'o', 'l', '_',
			'v', 'e', 'r', 's', 'i', 'o', 'n', '"', ':', '"', 0xff,
			'"', '}',
		})
		os.Exit(0)
	}
	raw, err := io.ReadAll(os.Stdin)
	if err != nil {
		os.Exit(2)
	}
	var request wireRequest
	if err := json.Unmarshal(raw, &request); err != nil {
		os.Exit(2)
	}
	if mode == "pipe-holder" {
		descendant := exec.Command("/bin/sleep", "30")
		descendant.Stdout = os.Stdout
		descendant.Stderr = os.Stderr
		if err := descendant.Start(); err != nil {
			os.Exit(2)
		}
	}
	version := protocolVersion
	switch mode {
	case "old-version":
		version = "tokenbench.external-adapter/v1"
	case "bad-version":
		version = "wrong/v1"
	}
	response := wireResponse{ProtocolVersion: version}
	if mode == "response-error" {
		response.Error = "credential-looking-response-value"
		if err := json.NewEncoder(os.Stdout).Encode(response); err != nil {
			os.Exit(2)
		}
		os.Exit(0)
	}
	if mode == "check-cwd" {
		current, cwdErr := os.Getwd()
		if cwdErr != nil {
			response.Error = cwdErr.Error()
		} else if current != filepath.Dir(os.Args[0]) {
			response.Error = "external adapter inherited a nondeterministic working directory"
		}
	}
	switch request.Operation {
	case "resolve":
		adapterDigest, digestErr := executableSHA256(os.Args[0])
		if digestErr != nil {
			os.Exit(2)
		}
		response.Identity = &harness.Identity{
			AdapterExecutableSHA256: adapterDigest,
			AdapterConfigSHA256:     strings.Repeat("4", 64),
			Kind:                    "external-fixture",
			AdapterVersion:          "fixture-adapter/v1",
			ExecutableSHA256:        request.Resolve.ExecutableSHA256,
			ExecutableVersion:       "fixture/v1",
			Model:                   request.Resolve.Model,
			ModelRevision:           request.Resolve.ExpectedModelRevision,
			ReasoningEffort:         request.Resolve.ReasoningEffort,
			DecoderSchema:           "fixture-output/v1",
		}
	case "build":
		response.Process = &harness.ProcessSpec{
			Environment:   request.Invocation.Environment,
			Argv:          []string{"/bin/echo", "fixture"},
			Stdin:         request.Invocation.Prompt,
			Directory:     request.Invocation.WorkingDirectory,
			TimeoutMillis: request.Invocation.TimeoutMillis,
		}
		switch mode {
		case "process-argv-nul":
			response.Process.Argv = []string{"/bin/echo", "bad\x00argument"}
		case "process-directory-nul":
			response.Process.Directory = "/tmp/bad\x00directory"
		case "process-environment-key-nul":
			response.Process.Environment = map[string]string{"BAD\x00KEY": "value"}
		case "process-environment-value-nul":
			response.Process.Environment = map[string]string{"KEY": "bad\x00value"}
		}
	case "decode":
		providerTotal := int64(13)
		response.Observation = &harness.Observation{
			Usage: harness.Usage{
				ProviderTotalTokens: &providerTotal,
				InputTokens:         10,
				CachedInputTokens:   2,
				OutputTokens:        3,
				ReasoningTokens:     1,
			},
			FinalAnswer: "fixture answer",
			Model:       "fixed-model",
			ToolCalls:   []string{},
			Completed:   true,
		}
	case "mcp_arguments":
		response.Arguments = []string{"--mcp", request.MCPServer.Name}
		if mode == "mcp-argument-nul" {
			response.Arguments[1] = "bad\x00argument"
		}
	default:
		response.Error = "unknown operation"
	}
	if err := json.NewEncoder(os.Stdout).Encode(response); err != nil {
		os.Exit(2)
	}
	os.Exit(0)
}
