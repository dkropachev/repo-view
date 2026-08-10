package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/dkropachev/repo-view/benchmarks/tokenbench/harness"
	"github.com/dkropachev/repo-view/benchmarks/tokenbench/harness/conformance"
	"github.com/dkropachev/repo-view/benchmarks/tokenbench/internal/selfexec"
)

type decodeForgingAdapterWrapper struct{ *Adapter }

func (*decodeForgingAdapterWrapper) Decode(
	context.Context,
	harness.RawExecution,
) (harness.Observation, error) {
	return harness.Observation{}, nil
}

func TestCurrentExecutableSHA256UsesPinnedRunningImage(t *testing.T) {
	want, err := selfexec.Current()
	if err != nil {
		t.Fatal(err)
	}
	got, err := currentExecutableSHA256()
	if err != nil {
		t.Fatal(err)
	}
	if got != want.SHA256 {
		t.Fatalf("currentExecutableSHA256() = %q, want %q", got, want.SHA256)
	}
}

func TestConformance(t *testing.T) {
	t.Parallel()
	invocation := invocationFixture(t)
	conformance.Run(t, conformance.Fixture{
		Adapter:    adapterFixture(t),
		Resolve:    resolveRequest(invocation),
		Invocation: invocation,
		Execution:  executionFixture(t),
	})
}

func TestBuildPreservesCommonInputAndPinsArgv(t *testing.T) {
	t.Parallel()
	adapter := adapterFixture(t)
	baseline := invocationFixture(t)
	candidate := cloneInvocation(t, baseline)
	candidate.MCPServers = []harness.MCPServer{mcpServerFixture()}
	conformance.RunPair(t, adapter, baseline, candidate)

	process, err := adapter.Build(context.Background(), baseline)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(process.Stdin, baseline.Prompt) ||
		!reflect.DeepEqual(process.Environment, baseline.Environment) ||
		process.Directory != baseline.WorkingDirectory ||
		process.TimeoutMillis != baseline.TimeoutMillis {
		t.Fatalf("Build changed common process input: %+v", process)
	}
	wantPrefix := []string{
		baseline.Executable,
		"exec",
		"--json",
		"--ephemeral",
		"--ignore-user-config",
		"--ignore-rules",
		"--strict-config",
		"--color", "never",
		"--sandbox", "read-only",
		"--model", "gpt-5.4",
		"--cd", "/source",
	}
	if !reflect.DeepEqual(process.Argv[:len(wantPrefix)], wantPrefix) {
		t.Fatalf("unexpected Codex argv prefix:\n got: %q\nwant: %q", process.Argv[:len(wantPrefix)], wantPrefix)
	}
	for _, argument := range process.Argv {
		if !utf8.ValidString(argument) || strings.ContainsRune(argument, '\x00') {
			t.Fatalf("invalid argv text %q", argument)
		}
		if argument == "--ask-for-approval" {
			t.Fatal("Codex v0.144.0 does not accept --ask-for-approval after exec")
		}
	}
	if contains(process.Argv, "-") {
		t.Fatal("Build added a prompt positional instead of using exact stdin")
	}
	if !containsPair(process.Argv, "-c", `developer_instructions="Follow \"rules\".\nNo writes\\ever."`) {
		t.Fatalf("developer instructions were not encoded as one TOML value: %q", process.Argv)
	}
	if !containsPair(process.Argv, "-c", `approval_policy="never"`) {
		t.Fatal("common argv did not pin approval_policy=never")
	}
	if !containsPair(process.Argv, "-c", `mcp_servers={}`) {
		t.Fatal("common argv did not pin an empty MCP registry")
	}
	for _, assignment := range []string{
		`openai_base_url="http://127.0.0.1:43119/v1"`,
		`debug.config_lockfile.export_dir="/tokenbench/config-lock"`,
		`debug.config_lockfile.save_fields_resolved_from_model_catalog=true`,
		`shell_environment_policy.set={PATH="/snapshot/toolbox"}`,
	} {
		if !containsPair(process.Argv, "-c", assignment) {
			t.Fatalf("common argv omitted runtime assignment %q", assignment)
		}
	}
	if containsJoined(process.Argv, OfflineLocalProxyCapability) {
		t.Fatal("offline local capability leaked from the exact environment into argv")
	}
	if process.Environment["PATH"] != adapter.layout.ToolboxRoot {
		t.Fatal("built process PATH differs from the immutable toolbox root")
	}
	if got := digestJSON(t, process.Argv); got != "c658fbb45963664a63f02772e350c0e22c841eb2aa41d4eeb0e98cbfa27bc643" {
		t.Fatalf("Codex v0.144.0 argv snapshot changed: got %s", got)
	}

	suffix, err := adapter.MCPArguments(context.Background(), candidate.MCPServers[0])
	if err != nil {
		t.Fatal(err)
	}
	if got := digestJSON(t, suffix); got != "631d2c44aae1c8599ac300a1bf1a479239ae9ac2a5e280a587961191ae6b5987" {
		t.Fatalf("Codex v0.144.0 MCP argv snapshot changed: got %s", got)
	}
	if !containsPair(suffix, "-c", `mcp_servers.repo_view.enabled_tools=["changed","find","inspect","outline"]`) {
		t.Fatalf("MCP suffix omitted exact tool allowlist: %q", suffix)
	}
	if containsJoined(suffix, "/source") == false ||
		containsJoined(suffix, "--git-sha256") == false {
		t.Fatalf("MCP suffix omitted canonical server arguments: %q", suffix)
	}
	if _, err := adapter.Build(context.Background(), candidate); err == nil {
		t.Fatal("Build accepted an arm-dependent MCP registry")
	}
}

func TestProductionIdentityRejectsDecodeWrapper(t *testing.T) {
	t.Parallel()
	layout := adapterFixture(t).layout
	production, err := NewProduction(layout)
	if err != nil {
		t.Fatal(err)
	}
	identity, ok := BuiltInProductionIdentity(production)
	if !ok || !strings.HasPrefix(identity, productionAdapter+"/sha256:") {
		t.Fatalf("production identity = %q, %t", identity, ok)
	}
	generic, err := New(layout)
	if err != nil {
		t.Fatal(err)
	}
	if identity, ok := BuiltInProductionIdentity(generic); ok || identity != "" {
		t.Fatalf("generic adapter received production identity %q, %t", identity, ok)
	}
	wrapper := &decodeForgingAdapterWrapper{Adapter: production}
	if identity, ok := BuiltInProductionIdentity(wrapper); ok || identity != "" {
		t.Fatalf("decode wrapper received production identity %q, %t", identity, ok)
	}
}

func TestCanonicalProcessValidationRejectsSharedHiddenOverrides(t *testing.T) {
	t.Parallel()
	adapter := adapterFixture(t)
	invocation := invocationFixture(t)
	process, err := adapter.Build(context.Background(), invocation)
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateCanonicalProcess(invocation, process); err != nil {
		t.Fatalf("canonical Codex process was rejected: %v", err)
	}
	reconstructed, err := AdapterForCanonicalProcess(invocation, process)
	if err != nil {
		t.Fatalf("canonical replay adapter was rejected: %v", err)
	}
	if got, err := reconstructed.RuntimeLayout(); err != nil || got != adapter.layout {
		t.Fatalf("reconstructed replay layout = %#v, %v", got, err)
	}
	for _, test := range []struct {
		name   string
		mutate func(*harness.ProcessSpec)
	}{
		{"model flag", func(value *harness.ProcessSpec) {
			for index := range value.Argv {
				if value.Argv[index] == "--model" {
					value.Argv[index+1] = "gpt-5.6"
					return
				}
			}
		}},
		{"config override", func(value *harness.ProcessSpec) {
			value.Argv = append(value.Argv, "-c", `model="gpt-5.6"`)
		}},
		{"toolbox config override", func(value *harness.ProcessSpec) {
			for index := range value.Argv {
				if value.Argv[index] == `shell_environment_policy.set={PATH="/snapshot/toolbox"}` {
					value.Argv[index] = `shell_environment_policy.set={PATH="/ambient/bin"}`
					return
				}
			}
		}},
		{"PATH environment override", func(value *harness.ProcessSpec) {
			value.Environment["PATH"] = "/ambient/bin"
		}},
		{"extra environment", func(value *harness.ProcessSpec) {
			value.Environment["HIDDEN"] = "1"
		}},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			mutated := process
			mutated.Argv = append([]string(nil), process.Argv...)
			mutated.Environment = cloneMap(process.Environment)
			test.mutate(&mutated)
			if err := ValidateCanonicalProcess(invocation, mutated); err == nil {
				t.Fatal("hidden common Codex override was accepted")
			}
			if _, err := AdapterForCanonicalProcess(invocation, mutated); err == nil {
				t.Fatal("replay adapter reconstruction accepted a hidden override")
			}
		})
	}
}

func TestResolvePinsIdentityAndSnapshotAllowlist(t *testing.T) {
	t.Parallel()
	adapter := adapterFixture(t)
	request := resolveRequest(invocationFixture(t))
	first, err := adapter.Resolve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second, err := adapter.Resolve(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if first != second {
		t.Fatalf("Resolve is nondeterministic:\n%+v\n%+v", first, second)
	}
	if first.Kind != "codex" || first.AdapterVersion != adapterVersion ||
		first.ExecutableVersion != executableVersion || first.DecoderSchema != decoderSchema ||
		first.Model != request.Model || first.ModelRevision != request.ExpectedModelRevision {
		t.Fatalf("unexpected resolved identity: %+v", first)
	}
	if first.AdapterControlConfigSHA256 != "8f06f9ca4654864b328ff6f28cf0453a6845b311a33a017470a296cd3dfb2d36" {
		t.Fatalf("Codex control manifest snapshot changed: got %s", first.AdapterControlConfigSHA256)
	}
	if first.AdapterConfigSHA256 != "4abc48e987ab8f041960711d0b880befe0e1e620956152f8cfb9b638e430b8a0" {
		t.Fatalf("Codex resolved configuration snapshot changed: got %s", first.AdapterConfigSHA256)
	}
	if err := harness.ValidateIdentity(first); err != nil {
		t.Fatal(err)
	}
	changedLayout := runtimeLayoutFixture()
	changedLayout.ToolboxRoot = "/other-snapshot/toolbox"
	changedAdapter, err := New(changedLayout)
	if err != nil {
		t.Fatal(err)
	}
	changedRequest := request
	changedRequest.Environment = changedLayout.Environment()
	changedIdentity, err := changedAdapter.Resolve(context.Background(), changedRequest)
	if err != nil {
		t.Fatal(err)
	}
	if changedIdentity.AdapterControlConfigSHA256 == first.AdapterControlConfigSHA256 ||
		changedIdentity.AdapterConfigSHA256 == first.AdapterConfigSHA256 {
		t.Fatal("toolbox root was absent from adapter identities")
	}
	wantSnapshots := []Snapshot{
		{"gpt-5.4", "gpt-5.4@gpt-5.4-2026-03-05", "gpt-5.4-2026-03-05"},
	}
	if got := Snapshots(); !reflect.DeepEqual(got, wantSnapshots) {
		t.Fatalf("snapshot allowlist changed:\n got: %+v\nwant: %+v", got, wantSnapshots)
	}
	copyOfSnapshots := Snapshots()
	copyOfSnapshots[0].RequestedModel = "mutated"
	if reflect.DeepEqual(copyOfSnapshots, Snapshots()) {
		t.Fatal("Snapshots did not return a defensive copy")
	}
	wantExecutableDigests := []string{"08b012d75651efb22b5162be253cd4d28752594082671098e123229b896ba77e"}
	if got := ExecutableSHA256Allowlist(); !reflect.DeepEqual(got, wantExecutableDigests) {
		t.Fatalf("executable allowlist changed: got %q, want %q", got, wantExecutableDigests)
	}
	copyOfDigests := ExecutableSHA256Allowlist()
	copyOfDigests[0] = strings.Repeat("0", 64)
	if reflect.DeepEqual(copyOfDigests, ExecutableSHA256Allowlist()) {
		t.Fatal("ExecutableSHA256Allowlist did not return a defensive copy")
	}
}

func TestCommonEnvironmentIsExactAndDefensive(t *testing.T) {
	t.Parallel()
	invocation := invocationFixture(t)
	request := resolveRequest(invocation)
	request.Environment = map[string]string{}
	adapter := adapterFixture(t)
	first, err := adapter.CommonEnvironment(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"HOME":              "/tokenbench/home",
		"CODEX_HOME":        "/tokenbench/codex-home",
		"CODEX_SQLITE_HOME": "/tokenbench/codex-home/sqlite",
		"TMPDIR":            "/tokenbench/tmp",
		"PATH":              "/snapshot/toolbox",
		"CODEX_API_KEY":     OfflineLocalProxyCapability,
	}
	if !reflect.DeepEqual(first, want) {
		t.Fatalf("unexpected common environment: got %q, want %q", first, want)
	}
	first["CODEX_API_KEY"] = "mutated"
	second, err := adapter.CommonEnvironment(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(second, want) {
		t.Fatal("CommonEnvironment did not return a defensive copy")
	}
	request.Environment = map[string]string{"CODEX_API_KEY": OfflineLocalProxyCapability}
	if _, err := adapter.CommonEnvironment(context.Background(), request); err == nil {
		t.Fatal("CommonEnvironment accepted a caller-authored environment")
	}
}

func TestCommonEnvironmentIgnoresAmbientPATH(t *testing.T) {
	t.Setenv("PATH", "/ambient/attacker-bin")
	layout := runtimeLayoutFixture()
	adapter := adapterFixture(t)
	request := resolveRequest(invocationFixture(t))
	request.Environment = map[string]string{}
	environment, err := adapter.CommonEnvironment(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if environment["PATH"] != layout.ToolboxRoot || environment["PATH"] == os.Getenv("PATH") {
		t.Fatalf("common PATH = %q, ambient PATH = %q", environment["PATH"], os.Getenv("PATH"))
	}
}

func TestNewRejectsInvalidRuntimeLayout(t *testing.T) {
	t.Parallel()
	base := runtimeLayoutFixture()
	tests := []struct {
		name   string
		mutate func(*RuntimeLayout)
	}{
		{"local capability", func(value *RuntimeLayout) { value.LocalProxyCapability = "secret" }},
		{"HTTPS proxy", func(value *RuntimeLayout) { value.ProxyURL = "https://127.0.0.1:43119/v1" }},
		{"remote proxy", func(value *RuntimeLayout) { value.ProxyURL = "http://localhost:43119/v1" }},
		{"missing port", func(value *RuntimeLayout) { value.ProxyURL = "http://127.0.0.1/v1" }},
		{"noncanonical port", func(value *RuntimeLayout) { value.ProxyURL = "http://127.0.0.1:043119/v1" }},
		{"wrong path", func(value *RuntimeLayout) { value.ProxyURL = "http://127.0.0.1:43119/" }},
		{"query", func(value *RuntimeLayout) { value.ProxyURL += "?secret=value" }},
		{"relative home", func(value *RuntimeLayout) { value.Home = "home" }},
		{"unclean Codex home", func(value *RuntimeLayout) { value.CodexHome = "/tokenbench/x/../codex-home" }},
		{"missing toolbox", func(value *RuntimeLayout) { value.ToolboxRoot = "" }},
		{"unclean toolbox", func(value *RuntimeLayout) { value.ToolboxRoot = "/snapshot/x/../toolbox" }},
		{"toolbox PATH list", func(value *RuntimeLayout) { value.ToolboxRoot = "/snapshot/toolbox:/usr/bin" }},
		{"equal directories", func(value *RuntimeLayout) { value.Temp = value.Home }},
		{"nested directories", func(value *RuntimeLayout) { value.ConfigLock = filepath.Join(value.Home, "lock") }},
		{"toolbox overlap", func(value *RuntimeLayout) { value.ToolboxRoot = filepath.Join(value.Home, "toolbox") }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			layout := base
			test.mutate(&layout)
			if _, err := New(layout); err == nil {
				t.Fatal("New accepted invalid runtime layout")
			}
		})
	}
	if _, err := (&Adapter{}).RuntimeLayout(); err == nil {
		t.Fatal("zero-value Adapter was accepted")
	}
	adapter := adapterFixture(t)
	got, err := adapter.RuntimeLayout()
	if err != nil || got != base {
		t.Fatalf("RuntimeLayout() = %+v, %v; want %+v", got, err, base)
	}
	if commitment, err := base.Commitment(); err != nil || commitment != "f4d5b9b7630c4f3db0662154672af392706e9fd64e14bbf6211f2bf3e37d8490" {
		t.Fatalf("RuntimeLayout commitment = %q, %v", commitment, err)
	}
	if !reflect.DeepEqual(base.Environment(), runtimeEnvironment(base)) {
		t.Fatal("RuntimeLayout.Environment diverged from adapter environment")
	}
	if base.Environment()["PATH"] != base.ToolboxRoot {
		t.Fatal("RuntimeLayout environment PATH differs from ToolboxRoot")
	}
	wantAssignments := []string{
		`openai_base_url="http://127.0.0.1:43119/v1"`,
		`debug.config_lockfile.export_dir="/tokenbench/config-lock"`,
		`debug.config_lockfile.save_fields_resolved_from_model_catalog=true`,
		`shell_environment_policy.set={PATH="/snapshot/toolbox"}`,
	}
	if got := base.ConfigAssignments(); !reflect.DeepEqual(got, wantAssignments) {
		t.Fatalf("RuntimeLayout.ConfigAssignments = %q, want %q", got, wantAssignments)
	}
}

func TestResolveRejectsUnpinnedOrInvalidInputs(t *testing.T) {
	t.Parallel()
	base := resolveRequest(invocationFixture(t))
	tests := []struct {
		name   string
		mutate func(*harness.ResolveRequest)
	}{
		{"nil environment", func(value *harness.ResolveRequest) { value.Environment = nil }},
		{"live credential", func(value *harness.ResolveRequest) { value.Environment["CODEX_API_KEY"] = "secret" }},
		{"extra environment", func(value *harness.ResolveRequest) { value.Environment["PATH"] = "/usr/bin" }},
		{"unknown snapshot", func(value *harness.ResolveRequest) { value.ExpectedModelRevision = "gpt-5.4@gpt-5.4-latest" }},
		{"model mismatch", func(value *harness.ResolveRequest) { value.Model = "gpt-5.6" }},
		{"unknown effort", func(value *harness.ResolveRequest) { value.ReasoningEffort = "extreme" }},
		{"unsupported minimal effort", func(value *harness.ResolveRequest) { value.ReasoningEffort = "minimal" }},
		{"writable", func(value *harness.ResolveRequest) { value.PermissionProfile = "workspace-write" }},
		{"relative executable", func(value *harness.ResolveRequest) { value.Executable = "codex" }},
		{"unclean directory", func(value *harness.ResolveRequest) { value.WorkingDirectory = "/source/../source" }},
		{"bad digest", func(value *harness.ResolveRequest) { value.ExecutableSHA256 = strings.Repeat("A", 64) }},
		{"unrecognized executable", func(value *harness.ResolveRequest) { value.ExecutableSHA256 = strings.Repeat("a", 64) }},
		{"bad revision", func(value *harness.ResolveRequest) { value.SourceBaseRevision = "main" }},
		{"zero timeout", func(value *harness.ResolveRequest) { value.TimeoutMillis = 0 }},
		{"NUL instructions", func(value *harness.ResolveRequest) { value.DeveloperInstructions = "bad\x00text" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			request := base
			request.Environment = cloneMap(base.Environment)
			test.mutate(&request)
			if _, err := adapterFixture(t).Resolve(context.Background(), request); err == nil {
				t.Fatal("Resolve accepted invalid input")
			}
		})
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := adapterFixture(t).Resolve(cancelled, base); err == nil {
		t.Fatal("Resolve ignored cancellation")
	}
}

func TestResolvePinsReasoningEffortPerSnapshot(t *testing.T) {
	t.Parallel()
	base := resolveRequest(invocationFixture(t))
	tests := []struct {
		model    string
		revision string
		effort   string
		valid    bool
	}{
		{"gpt-5.4", "gpt-5.4@gpt-5.4-2026-03-05", "max", false},
		{"gpt-5.6", "gpt-5.6@gpt-5.6-sol", "max", false},
		{"gpt-5.6-sol", "gpt-5.6-sol@gpt-5.6-sol", "ultra", false},
		{"gpt-5.6-terra", "gpt-5.6-terra@gpt-5.6-terra", "ultra", false},
		{"gpt-5.6-luna", "gpt-5.6-luna@gpt-5.6-luna", "ultra", false},
	}
	for _, test := range tests {
		t.Run(test.model+"/"+test.effort, func(t *testing.T) {
			t.Parallel()
			request := base
			request.Environment = cloneMap(base.Environment)
			request.Model = test.model
			request.ExpectedModelRevision = test.revision
			request.ReasoningEffort = test.effort
			_, err := adapterFixture(t).Resolve(context.Background(), request)
			if (err == nil) != test.valid {
				t.Fatalf("Resolve error = %v, valid=%v", err, test.valid)
			}
		})
	}
}

func TestBuildRejectsInvalidCommonInvocation(t *testing.T) {
	t.Parallel()
	base := invocationFixture(t)
	tests := []struct {
		name   string
		mutate func(*harness.Invocation)
	}{
		{"arguments", func(value *harness.Invocation) { value.Arguments = []string{"--profile", "ambient"} }},
		{"empty prompt", func(value *harness.Invocation) { value.Prompt = nil }},
		{"invalid prompt", func(value *harness.Invocation) { value.Prompt = []byte{0xff} }},
		{"model mismatch", func(value *harness.Invocation) { value.Model = "gpt-5.6" }},
		{"foreign identity", func(value *harness.Invocation) { value.HarnessIdentity.AdapterVersion = "other" }},
		{"MCP registry", func(value *harness.Invocation) { value.MCPServers = []harness.MCPServer{mcpServerFixture()} }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			invocation := cloneInvocation(t, base)
			test.mutate(&invocation)
			if _, err := adapterFixture(t).Build(context.Background(), invocation); err == nil {
				t.Fatal("Build accepted invalid common invocation")
			}
		})
	}
}

func TestMCPArgumentsRejectsNoncanonicalRegistration(t *testing.T) {
	t.Parallel()
	base := mcpServerFixture()
	tests := []struct {
		name   string
		mutate func(*harness.MCPServer)
	}{
		{"name", func(value *harness.MCPServer) { value.Name = "other" }},
		{"optional", func(value *harness.MCPServer) { value.Required = false }},
		{"writable", func(value *harness.MCPServer) { value.ReadOnly = false }},
		{"relative command", func(value *harness.MCPServer) { value.Command = "repo-view" }},
		{"bad command digest", func(value *harness.MCPServer) { value.ExecutableSHA256 = "bad" }},
		{"nil environment", func(value *harness.MCPServer) { value.Environment = nil }},
		{"nonempty environment", func(value *harness.MCPServer) { value.Environment["SECRET"] = "value" }},
		{"extra argument", func(value *harness.MCPServer) { value.Arguments = append(value.Arguments, "--extra") }},
		{"relative root", func(value *harness.MCPServer) { value.Arguments[2] = "source" }},
		{"bad base", func(value *harness.MCPServer) { value.Arguments[4] = "HEAD" }},
		{"relative Git", func(value *harness.MCPServer) { value.Arguments[6] = "git" }},
		{"bad Git digest", func(value *harness.MCPServer) { value.Arguments[8] = "bad" }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			server := cloneMCPServer(base)
			test.mutate(&server)
			if _, err := adapterFixture(t).MCPArguments(context.Background(), server); err == nil {
				t.Fatal("MCPArguments accepted an invalid registration")
			}
		})
	}
}

func TestTOMLEncodingIsCanonicalAndInjectionSafe(t *testing.T) {
	t.Parallel()
	tests := []struct {
		input string
		want  string
	}{
		{"plain", `"plain"`},
		{"quote\"slash\\", `"quote\"slash\\"`},
		{"line\nnext\ttab\r", `"line\nnext\ttab\r"`},
		{"delete\x7f", `"delete\u007F"`},
		{"snowman ☃", `"snowman ☃"`},
	}
	for _, test := range tests {
		got, err := tomlString(test.input)
		if err != nil {
			t.Fatal(err)
		}
		if got != test.want {
			t.Fatalf("tomlString(%q) = %q, want %q", test.input, got, test.want)
		}
	}
	if _, err := tomlString("bad\x00value"); err == nil {
		t.Fatal("tomlString accepted NUL")
	}
	if _, err := tomlString(string([]byte{0xff})); err == nil {
		t.Fatal("tomlString accepted invalid UTF-8")
	}
	array, err := tomlStringArray([]string{"a", "b\nc"})
	if err != nil || array != `["a","b\nc"]` {
		t.Fatalf("unexpected TOML array %q: %v", array, err)
	}
	object, err := tomlStringMap(map[string]string{"z": "last", "a": "first"})
	if err != nil || object != `{a="first",z="last"}` {
		t.Fatalf("unexpected TOML map %q: %v", object, err)
	}
	if _, err := tomlStringMap(map[string]string{"bad.key": "value"}); err == nil {
		t.Fatal("tomlStringMap accepted a non-bare key")
	}
}

func TestExportedManifestsAreCanonicalAndDefensive(t *testing.T) {
	t.Parallel()
	disabled := DisabledFeatures()
	enabled := EnabledFeatures()
	if len(disabled) == 0 || !reflect.DeepEqual(enabled, []string{"shell_tool", "unified_exec"}) {
		t.Fatalf("unexpected feature manifest: disabled=%q enabled=%q", disabled, enabled)
	}
	if got := digestJSON(t, disabled); got != "55f13b34d5420b0c222a8f30d55acab6d3646c25d08290e484ad4c7e55d439ea" {
		t.Fatalf("disabled feature manifest snapshot changed: got %s", got)
	}
	seen := make(map[string]struct{}, len(disabled)+len(enabled))
	for index, feature := range append(append([]string(nil), disabled...), enabled...) {
		if feature == "" {
			t.Fatalf("feature %d is empty", index)
		}
		if _, duplicate := seen[feature]; duplicate {
			t.Fatalf("duplicate feature %q", feature)
		}
		seen[feature] = struct{}{}
	}
	disabled[0] = "mutated"
	enabled[0] = "mutated"
	if DisabledFeatures()[0] == "mutated" || EnabledFeatures()[0] == "mutated" {
		t.Fatal("feature manifest accessors did not return defensive copies")
	}
	tools := AllowedMCPTools()
	if !reflect.DeepEqual(tools, []string{"changed", "find", "inspect", "outline"}) {
		t.Fatalf("unexpected MCP tool manifest: %q", tools)
	}
	tools[0] = "mutated"
	if AllowedMCPTools()[0] == "mutated" {
		t.Fatal("AllowedMCPTools did not return a defensive copy")
	}
}

func invocationFixture(t *testing.T) harness.Invocation {
	t.Helper()
	invocation := harness.Invocation{
		Environment:            runtimeEnvironment(runtimeLayoutFixture()),
		Arguments:              []string{},
		MCPServers:             []harness.MCPServer{},
		Prompt:                 []byte("Answer from repository evidence.\n"),
		Executable:             "/tools/codex",
		ExecutableSHA256:       "08b012d75651efb22b5162be253cd4d28752594082671098e123229b896ba77e",
		Model:                  "gpt-5.4",
		RequestedModel:         "gpt-5.4",
		ModelRevision:          "gpt-5.4@gpt-5.4-2026-03-05",
		ReasoningEffort:        "high",
		PermissionProfile:      "read-only",
		DeveloperInstructions:  "Follow \"rules\".\nNo writes\\ever.",
		WorkingDirectory:       "/source",
		SourceRevision:         strings.Repeat("1", 40),
		SourceBaseRevision:     strings.Repeat("0", 40),
		SourceTreeSHA256:       strings.Repeat("b", 64),
		GitExecutable:          "/usr/bin/git",
		GitExecutableSHA256:    strings.Repeat("c", 64),
		GitMetadataSHA256:      strings.Repeat("d", 64),
		RunnerExecutable:       "/tools/tokenbench",
		RunnerExecutableSHA256: strings.Repeat("e", 64),
		TimeoutMillis:          30_000,
	}
	identity, err := adapterFixture(t).Resolve(context.Background(), resolveRequest(invocation))
	if err != nil {
		t.Fatal(err)
	}
	invocation.HarnessIdentity = identity
	return invocation
}

func runtimeLayoutFixture() RuntimeLayout {
	return RuntimeLayout{
		ProxyURL:             "http://127.0.0.1:43119/v1",
		Home:                 "/tokenbench/home",
		CodexHome:            "/tokenbench/codex-home",
		Temp:                 "/tokenbench/tmp",
		ConfigLock:           "/tokenbench/config-lock",
		ToolboxRoot:          "/snapshot/toolbox",
		LocalProxyCapability: OfflineLocalProxyCapability,
	}
}

func adapterFixture(t *testing.T) *Adapter {
	t.Helper()
	adapter, err := New(runtimeLayoutFixture())
	if err != nil {
		t.Fatal(err)
	}
	return adapter
}

func mcpServerFixture() harness.MCPServer {
	return harness.MCPServer{
		Environment:      map[string]string{},
		Name:             "repo_view",
		Command:          "/tools/repo-view",
		ExecutableSHA256: strings.Repeat("f", 64),
		Arguments: []string{
			"mcp",
			"--root", "/source",
			"--base", strings.Repeat("0", 40),
			"--git", "/usr/bin/git",
			"--git-sha256", strings.Repeat("c", 64),
		},
		Required: true,
		ReadOnly: true,
	}
}

func executionFixture(t *testing.T) harness.RawExecution {
	t.Helper()
	return harness.RawExecution{
		Stdout:   readTestdata(t, "testdata/exec-success.jsonl"),
		Stderr:   []byte{},
		ExitCode: 0,
		Artifacts: []harness.Artifact{
			{
				Name:      ResponsesTraceArtifactName,
				MediaType: ResponsesTraceMediaType,
				Data:      readTestdata(t, "testdata/responses-trace-success.json"),
			},
			{
				Name:      EffectiveConfigArtifactName,
				MediaType: EffectiveConfigMediaType,
				Data:      readTestdata(t, "testdata/effective-config.toml"),
			},
		},
	}
}

func readTestdata(t *testing.T, path string) []byte {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func cloneInvocation(t *testing.T, source harness.Invocation) harness.Invocation {
	t.Helper()
	raw, err := json.Marshal(source)
	if err != nil {
		t.Fatal(err)
	}
	var result harness.Invocation
	if err := json.Unmarshal(raw, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func cloneMCPServer(source harness.MCPServer) harness.MCPServer {
	result := source
	result.Environment = cloneMap(source.Environment)
	result.Arguments = append([]string(nil), source.Arguments...)
	return result
}

func digestJSON(t *testing.T, value any) string {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return digest(raw)
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func containsPair(values []string, first, second string) bool {
	for index := 0; index+1 < len(values); index++ {
		if values[index] == first && values[index+1] == second {
			return true
		}
	}
	return false
}

func containsJoined(values []string, substring string) bool {
	return strings.Contains(strings.Join(values, "\x00"), substring)
}
