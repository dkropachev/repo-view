package scopesiftermcp

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yapless/scopesifter/navigator"
)

func TestServicePropagatesRequestCancellation(t *testing.T) {
	fixture := newFixture(t)
	view, err := navigator.NewWithGit(
		fixture.root,
		fixture.git,
		fixture.gitSHA256,
	)
	if err != nil {
		t.Fatal(err)
	}
	service := &service{view: view, base: fixture.base}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, _, err = service.find(ctx, nil, findInput{Query: "Helper"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("find error = %v, want context cancellation", err)
	}
}

func TestBooleanSchemaHonorsDefault(t *testing.T) {
	for _, want := range []bool{false, true} {
		schema := booleanSchema(want)
		if got, ok := schema["default"].(bool); !ok || got != want {
			t.Fatalf("boolean schema default = %#v, want %t", schema["default"], want)
		}
	}
}

func TestServerManifestAndToolsAreReadOnly(t *testing.T) {
	fixture := newFixture(t)
	before := snapshotTree(t, fixture.root)
	server := newFixtureServer(t, fixture)
	client, closeSessions := connect(t, server)
	defer closeSessions()

	initial := client.InitializeResult()
	if initial == nil || initial.ServerInfo == nil {
		t.Fatal("MCP initialization omitted server identity")
	}
	if initial.ServerInfo.Name != ImplementationName ||
		initial.ServerInfo.Version != ImplementationVersion {
		t.Fatalf("server identity = %#v", initial.ServerInfo)
	}
	if initial.Instructions != "" {
		t.Fatalf("server instructions = %q, want none", initial.Instructions)
	}
	capabilities := initial.Capabilities
	if capabilities == nil || capabilities.Tools == nil ||
		capabilities.Logging != nil || //nolint:staticcheck // No deprecated logging capability may be advertised.
		capabilities.Prompts != nil ||
		capabilities.Resources != nil || capabilities.Completions != nil ||
		len(capabilities.Experimental) != 0 || len(capabilities.Extensions) != 0 {
		t.Fatalf("server capabilities = %#v", capabilities)
	}
	if capabilities.Tools.ListChanged {
		t.Fatal("fixed tool list advertises listChanged")
	}

	result, err := client.ListTools(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	discoveryJSON, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("MCP tool-discovery payload: %d bytes", len(discoveryJSON))
	if len(discoveryJSON) >= 4_700 {
		t.Fatalf("MCP tool-discovery payload = %d bytes, want below 4700", len(discoveryJSON))
	}
	wantProperties := map[string][]string{
		"changed": {
			"exclude_globs", "limit", "path_globs",
		},
		"find": {
			"changed_only", "exclude_globs", "include", "limit", "match", "path_globs", "query",
		},
		"inspect": {
			"changed_only", "exclude_globs", "include", "location", "path_globs",
		},
		"outline": {
			"limit", "path",
		},
	}
	wantNames := []string{"changed", "find", "inspect", "outline"}
	wantRequired := map[string][]string{
		"changed": {},
		"find":    {"query"},
		"inspect": {"location"},
		"outline": {"path"},
	}
	if len(result.Tools) != len(wantNames) {
		t.Fatalf("tools = %#v", result.Tools)
	}
	for index, tool := range result.Tools {
		if tool.Name != wantNames[index] {
			t.Fatalf("tool %d name = %q, want %q", index, tool.Name, wantNames[index])
		}
		if tool.Title != "" || tool.Description == "" || tool.OutputSchema != nil {
			t.Fatalf("tool %q metadata = %#v", tool.Name, tool)
		}
		inputSchemaJSON, err := json.Marshal(tool.InputSchema)
		if err != nil {
			t.Fatal(err)
		}
		if bytes.Contains(inputSchemaJSON, []byte(`"description"`)) {
			t.Fatalf("tool %q input schema contains redundant prose", tool.Name)
		}
		annotations := tool.Annotations
		if annotations == nil || !annotations.ReadOnlyHint ||
			annotations.OpenWorldHint == nil || *annotations.OpenWorldHint ||
			annotations.DestructiveHint == nil || *annotations.DestructiveHint ||
			!annotations.IdempotentHint {
			t.Fatalf("tool %q annotations = %#v", tool.Name, annotations)
		}
		schema := normalizedObject(t, tool.InputSchema)
		if additional, ok := schema["additionalProperties"].(bool); !ok || additional {
			t.Fatalf("tool %q has open input schema: %#v", tool.Name, schema)
		}
		properties, ok := schema["properties"].(map[string]any)
		if !ok {
			t.Fatalf("tool %q properties = %#v", tool.Name, schema["properties"])
		}
		gotProperties := make([]string, 0, len(properties))
		for name := range properties {
			gotProperties = append(gotProperties, name)
			for _, forbidden := range []string{"root", "base", "git", "git_sha256", "prompt", "arm"} {
				if name == forbidden {
					t.Fatalf("tool %q exposes forbidden input %q", tool.Name, name)
				}
			}
		}
		sort.Strings(gotProperties)
		if !reflect.DeepEqual(gotProperties, wantProperties[tool.Name]) {
			t.Fatalf("tool %q properties = %q, want %q", tool.Name, gotProperties, wantProperties[tool.Name])
		}
		if required := stringArray(schema["required"]); !reflect.DeepEqual(required, wantRequired[tool.Name]) {
			t.Fatalf("tool %q required = %q, want %q", tool.Name, required, wantRequired[tool.Name])
		}
		switch tool.Name {
		case "changed":
			if !strings.Contains(tool.Description, "Start bug/branch/PR work here") ||
				!strings.Contains(tool.Description, "base-to-HEAD changes") {
				t.Fatalf("changed description = %q", tool.Description)
			}
		case "find":
			if !strings.Contains(tool.Description, "Exact identifier or path") ||
				!strings.Contains(tool.Description, "matching changed PATH:LINE") {
				t.Fatalf("find description = %q", tool.Description)
			}
			matchSchema := normalizedObject(t, properties["match"])
			if matchSchema["default"] != "auto" ||
				!reflect.DeepEqual(stringArray(matchSchema["enum"]), []string{"auto", "symbol", "path"}) {
				t.Fatalf("find match schema = %#v", matchSchema)
			}
		case "inspect":
			if !strings.Contains(tool.Description, "bounded scope/imports/relations") ||
				!strings.Contains(tool.Description, "PATH:LINE") {
				t.Fatalf("inspect description = %q", tool.Description)
			}
		case "outline":
			if !strings.Contains(tool.Description, "Index-only definitions") ||
				!strings.Contains(tool.Description, "inspect returned PATH:LINE") {
				t.Fatalf("outline description = %q", tool.Description)
			}
		}
		if tool.Name != "inspect" {
			assertIntegerProperty(
				t,
				tool.Name,
				properties,
				"limit",
				defaultLimit,
				maximumLimit,
			)
		}
	}

	view, err := navigator.NewWithGit(
		fixture.root,
		fixture.git,
		fixture.gitSHA256,
	)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	assertToolOutput(t, client, "changed", map[string]any{}, func() (navigator.ChangedResponse, error) {
		return view.Changed(navigator.Options{
			Base: fixture.base, Include: navigator.IncludeAll,
			Return: navigator.ReturnLocations, Context: defaultContext,
			Limit: defaultLimit, MaxCodeLines: defaultMaxCodeLines,
			MaxPatchLines: defaultMaxPatchLines,
		})
	})
	assertToolOutput(t, client, "find", map[string]any{
		"query": "Helper",
	}, func() (navigator.FindResponse, error) {
		response, err := view.Find("Helper", navigator.Options{
			Base: fixture.base, Include: navigator.IncludeBoth,
			Match: navigator.FindMatchAuto, Return: navigator.ReturnLocations, Context: defaultContext,
			Limit: defaultLimit, MaxCodeLines: defaultMaxCodeLines,
			MaxPatchLines: defaultMaxPatchLines, NoComments: true, NoStrings: true,
		})
		return response, err
	})
	assertToolOutput(t, client, "find", map[string]any{
		"query": "missing.go", "match": "path",
	}, func() (navigator.FindResponse, error) {
		response, err := view.Find("missing.go", navigator.Options{
			Base: fixture.base, Include: navigator.IncludeBoth,
			Match: navigator.FindMatchPath, Return: navigator.ReturnLocations, Context: defaultContext,
			Limit: defaultLimit, MaxCodeLines: defaultMaxCodeLines,
			MaxPatchLines: defaultMaxPatchLines, NoComments: true, NoStrings: true,
		})
		response.Symbol = ""
		return response, err
	})
	assertToolOutput(t, client, "inspect", map[string]any{
		"location": "demo.go:6",
	}, func() (navigator.InspectResponse, error) {
		return view.Inspect("demo.go:6", navigator.Options{
			Base: fixture.base, Include: navigator.IncludeScope,
			Return: navigator.ReturnScope, Context: defaultContext,
			Limit: defaultLimit, MaxCodeLines: defaultMaxCodeLines,
			MaxPatchLines: defaultMaxPatchLines, NoComments: true, NoStrings: true,
		})
	})
	assertToolOutput(t, client, "outline", map[string]any{
		"path": "demo.go",
	}, func() (navigator.OutlineResponse, error) {
		return view.Outline("demo.go", navigator.Options{
			Base: fixture.base, Include: navigator.IncludeDefs,
			Return: navigator.ReturnLocations, Limit: defaultLimit,
			MaxCodeLines: defaultMaxCodeLines, MaxPatchLines: defaultMaxPatchLines,
		})
	})

	for _, call := range []struct {
		name      string
		arguments map[string]any
	}{
		{name: "find", arguments: map[string]any{"query": "Helper", "root": "/tmp"}},
		{name: "find", arguments: map[string]any{}},
		{name: "find", arguments: map[string]any{"query": "Helper", "limit": maximumLimit + 1}},
		{name: "find", arguments: map[string]any{"query": "Helper", "match": "text"}},
		{name: "find", arguments: map[string]any{"query": "Helper", "response": "full"}},
		{name: "inspect", arguments: map[string]any{"location": "../outside.go:1"}},
		{name: "outline", arguments: map[string]any{"path": "demo.go", "return": "context"}},
		{name: "outline", arguments: map[string]any{"path": "demo.go", "response": "full"}},
	} {
		response, err := client.CallTool(ctx, &mcp.CallToolParams{
			Name: call.name, Arguments: call.arguments,
		})
		if err != nil {
			t.Fatalf("call %s: %v", call.name, err)
		}
		if !response.IsError {
			t.Fatalf("invalid %s call succeeded: %#v", call.name, response)
		}
	}

	if after := snapshotTree(t, fixture.root); !reflect.DeepEqual(after, before) {
		t.Fatal("read-only MCP calls changed repository or Git metadata")
	}
}

func TestServerChangedReturnsExactEditedLine(t *testing.T) {
	fixture := newFixture(t)
	server := newFixtureServer(t, fixture)
	client, closeSessions := connect(t, server)
	defer closeSessions()

	response, err := client.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "changed", Arguments: map[string]any{},
	})
	if err != nil || response.IsError {
		t.Fatalf("changed = %#v, %v", response, err)
	}
	raw, err := json.Marshal(response.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var changed leanResponse
	if err := json.Unmarshal(raw, &changed); err != nil {
		t.Fatal(err)
	}
	if len(changed.Results) != 1 || changed.Results[0].Location != "demo.go:7" ||
		changed.Next == nil || changed.Next.Tool != "inspect" ||
		changed.Next.Arguments["location"] != "demo.go:7" {
		t.Fatalf("changed action = %#v", changed)
	}
}

func TestServerOutlineOmitsDefinitionSource(t *testing.T) {
	fixture := newFixture(t)
	server := newFixtureServer(t, fixture)
	client, closeSessions := connect(t, server)
	defer closeSessions()

	response, err := client.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "outline", Arguments: map[string]any{"path": "demo.go"},
	})
	if err != nil || response.IsError {
		t.Fatalf("outline = %#v, %v", response, err)
	}
	raw, err := json.Marshal(response.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(raw, []byte(`"signature"`)) || bytes.Contains(raw, []byte("func Helper()")) {
		t.Fatalf("outline leaked definition source: %s", raw)
	}
}

func TestServerFindPathPrioritizesChangedLocation(t *testing.T) {
	fixture := newFixture(t)
	server := newFixtureServer(t, fixture)
	client, closeSessions := connect(t, server)
	defer closeSessions()

	response, err := client.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "find", Arguments: map[string]any{"query": "demo.go"},
	})
	if err != nil || response.IsError {
		t.Fatalf("path find = %#v, %v", response, err)
	}
	raw, err := json.Marshal(response.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var found leanResponse
	if err := json.Unmarshal(raw, &found); err != nil {
		t.Fatal(err)
	}
	if found.Target != "demo.go" || found.Outcome != string(navigator.FindOutcomeFile) ||
		len(found.Results) != 1 {
		t.Fatalf("enriched path response = %#v", found)
	}
	result := found.Results[0]
	if result.Location != "demo.go:7" || result.Kind != "changed" ||
		result.Scope != "Caller" {
		t.Fatalf("changed path candidate = %#v", result)
	}
	if found.Next == nil || found.Next.Tool != "inspect" ||
		found.Next.Arguments["location"] != "demo.go:7" {
		t.Fatalf("changed path next = %#v", found.Next)
	}
	if len(raw) > structuredOutputBudget {
		t.Fatalf("enriched path response = %d bytes, want at most %d", len(raw), structuredOutputBudget)
	}
}

func TestServerFindRelatedActionIsAcceptedAndUseful(t *testing.T) {
	fixture := newFixture(t)
	server := newFixtureServer(t, fixture)
	client, closeSessions := connect(t, server)
	defer closeSessions()

	response, err := client.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "find", Arguments: map[string]any{"query": "demo", "match": "path"},
	})
	if err != nil || response.IsError {
		t.Fatalf("path find = %#v, %v", response, err)
	}
	raw, err := json.Marshal(response.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var found leanResponse
	if err := json.Unmarshal(raw, &found); err != nil {
		t.Fatal(err)
	}
	if found.Related == nil || found.Related.Tool != "find" {
		t.Fatalf("related action = %#v in %#v", found.Related, found)
	}
	related, err := client.CallTool(context.Background(), &mcp.CallToolParams{
		Name: found.Related.Tool, Arguments: found.Related.Arguments,
	})
	if err != nil || related.IsError {
		t.Fatalf("related find = %#v, %v", related, err)
	}
	relatedRaw, err := json.Marshal(related.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var references leanResponse
	if err := json.Unmarshal(relatedRaw, &references); err != nil {
		t.Fatal(err)
	}
	if len(references.Results) != 1 ||
		references.Results[0].Location != "demo_test.go:3" ||
		references.Results[0].Kind != "ref" {
		t.Fatalf("related references = %#v", references)
	}
}

func TestFileEnrichmentFailureFallsBackToOriginalResult(t *testing.T) {
	fixture := newFixture(t)
	view, err := navigator.NewWithGit(
		fixture.root,
		fixture.git,
		fixture.gitSHA256,
	)
	if err != nil {
		t.Fatal(err)
	}
	service := &service{view: view, base: fixture.base}
	original := navigator.FindResponse{
		Query:     "demo.go",
		MatchedAs: navigator.FindOutcomeFile,
		Results: []navigator.Result{{
			Path: "demo.go", Kind: "file", Finding: navigator.FindingFile,
		}},
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	got := service.enrichFileFindWithChanges(ctx, findInput{}, original)
	if !reflect.DeepEqual(got, original) {
		t.Fatalf("failed optional enrichment = %#v, want %#v", got, original)
	}
}

func TestChangedFileEnrichmentPreservesOutcomeAndLimit(t *testing.T) {
	original := navigator.FindResponse{
		Query:            "*.go",
		MatchedAs:        navigator.FindOutcomeFile,
		ResultsTruncated: false,
		Results: []navigator.Result{
			{Path: "a.go", Kind: "file", Finding: navigator.FindingFile},
			{Path: "b.go", Kind: "file", Finding: navigator.FindingFile},
		},
	}
	changed := navigator.ChangedResponse{
		ResultsTruncated: true,
		Results: []navigator.Result{
			{Path: "a.go", Line: 4, Kind: "", Code: "source", CodeStartLine: 4},
			{Path: "a.go", Line: 9, Kind: "other"},
			{Path: "outside.go", Line: 2, Kind: "changed"},
		},
	}
	got := mergeChangedFileResults(
		original,
		changed,
		map[string]struct{}{"a.go": {}, "b.go": {}},
		1,
	)
	if got.MatchedAs != navigator.FindOutcomeFile || len(got.Results) != 1 ||
		!got.ResultsTruncated {
		t.Fatalf("bounded enriched response = %#v", got)
	}
	result := got.Results[0]
	if result.Path != "a.go" || result.Line != 4 || result.Kind != "changed" ||
		result.Code != "" || result.CodeStartLine != 0 {
		t.Fatalf("enriched result = %#v", result)
	}
}

func TestServerAutoUsesLeanEvidenceWithinBudget(t *testing.T) {
	fixture := newFixture(t)
	large := "package demo\n\nfunc Helper() {}\n\nfunc Caller() {\n"
	for index := range 200 {
		large += fmt.Sprintf("\tprintln(%q)\n", fmt.Sprintf("%03d-%s", index, strings.Repeat("x", 96)))
	}
	large += "\tHelper()\n}\n"
	writeFixtureFile(t, fixture.root, large)
	server := newFixtureServer(t, fixture)
	client, closeSessions := connect(t, server)
	defer closeSessions()

	auto, err := client.CallTool(context.Background(), &mcp.CallToolParams{
		Name:      "inspect",
		Arguments: map[string]any{"location": "demo.go:6"},
	})
	if err != nil || auto.IsError {
		t.Fatalf("auto inspect = %#v, %v", auto, err)
	}
	autoJSON, err := json.Marshal(auto.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if len(autoJSON) > structuredOutputBudget ||
		!bytes.Contains(autoJSON, []byte(`"target":"demo.go:6"`)) ||
		!bytes.Contains(autoJSON, []byte(`"evidence"`)) ||
		bytes.Contains(autoJSON, []byte(`"original_bytes"`)) ||
		bytes.Contains(autoJSON, []byte("println(100)")) {
		t.Fatalf("auto structured output (%d bytes) = %s", len(autoJSON), autoJSON)
	}
	if text := toolText(auto); text == "" || len(text) > maximumCompactTextBytes ||
		strings.Contains(text, string(autoJSON)) {
		t.Fatalf("auto text content = %q", text)
	}
}

func TestServerRejectsPublicFullResponse(t *testing.T) {
	fixture := newFixture(t)
	server := newFixtureServer(t, fixture)
	client, closeSessions := connect(t, server)
	defer closeSessions()
	response, err := client.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "inspect", Arguments: map[string]any{
			"location": "demo.go:6", "response": responseFull,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !response.IsError {
		t.Fatalf("public response=full succeeded: %#v", response)
	}
}

func TestServerUsesPinnedGitAndIgnoresAmbientInterception(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("self-exec interception fixture uses Unix symlink semantics")
	}
	fixture := newFixture(t)
	fakeDirectory := t.TempDir()
	marker := filepath.Join(fakeDirectory, "ambient-git-ran")
	fakeGit := filepath.Join(fakeDirectory, "git")
	testExecutable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	copyExecutable(t, testExecutable, fakeGit)
	assertAmbientGitInterceptor(t, fakeGit, marker)
	t.Setenv("PATH", fakeDirectory)
	t.Setenv("HOME", t.TempDir())
	t.Setenv("GIT_DIR", filepath.Join(t.TempDir(), ".git"))
	t.Setenv("GIT_CONFIG_COUNT", "1")
	t.Setenv("GIT_CONFIG_KEY_0", "diff.external")
	t.Setenv("GIT_CONFIG_VALUE_0", fakeGit)
	t.Setenv("LD_PRELOAD", "/definitely/not/a/library.so")

	server := newFixtureServer(t, fixture)
	client, closeSessions := connect(t, server)
	defer closeSessions()
	response, err := client.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "changed", Arguments: map[string]any{},
	})
	if err != nil || response.IsError {
		t.Fatalf("changed call = %#v, %v", response, err)
	}
	if _, err := os.Lstat(marker); !os.IsNotExist(err) {
		t.Fatalf("ambient git executable ran: %v", err)
	}
}

func TestMain(m *testing.M) {
	executable, executableErr := os.Executable()
	if executableErr == nil && filepath.Base(executable) == "git" {
		if err := os.WriteFile(
			filepath.Join(filepath.Dir(executable), "ambient-git-ran"),
			[]byte("intercepted"),
			0o600,
		); err != nil {
			os.Exit(98)
		}
		os.Exit(99)
	}
	os.Exit(m.Run())
}

func TestServerRejectsPinnedGitDigestDrift(t *testing.T) {
	fixture := newFixture(t)
	copyPath := filepath.Join(t.TempDir(), "git")
	copyExecutable(t, fixture.git, copyPath)
	copySHA256 := fileSHA256(t, copyPath)
	fixture.git = copyPath
	fixture.gitSHA256 = copySHA256
	server := newFixtureServer(t, fixture)

	content, err := os.ReadFile(copyPath)
	if err != nil {
		t.Fatal(err)
	}
	content = append(content, '\n')
	if err := os.WriteFile(copyPath, content, 0o700); err != nil {
		t.Fatal(err)
	}
	client, closeSessions := connect(t, server)
	defer closeSessions()
	response, err := client.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "changed", Arguments: map[string]any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !response.IsError || !strings.Contains(toolText(response), "git executable") {
		t.Fatalf("digest drift response = %#v", response)
	}
}

type fixture struct {
	root      string
	base      string
	git       string
	gitSHA256 string
}

func newFixture(t *testing.T) fixture {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is unavailable")
	}
	gitPath, err = filepath.Abs(gitPath)
	if err != nil {
		t.Fatal(err)
	}
	gitPath, err = filepath.EvalSymlinks(gitPath)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	gitRun(t, gitPath, root, "init", "-q")
	gitRun(t, gitPath, root, "config", "user.email", "scopesifter@example.test")
	gitRun(t, gitPath, root, "config", "user.name", "scopesifter MCP test")
	gitRun(t, gitPath, root, "config", "commit.gpgsign", "false")
	writeFixtureFile(t, root, `package demo

func Helper() {}

func Caller() {
	Helper()
}
`)
	if err := os.WriteFile(
		filepath.Join(root, "demo_test.go"),
		[]byte("package demo\n\nfunc TestCaller() { Caller() }\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	gitRun(t, gitPath, root, "add", "demo.go", "demo_test.go")
	gitRun(t, gitPath, root, "commit", "-q", "-m", "base")
	base := gitOutput(t, gitPath, root, "rev-parse", "HEAD")
	writeFixtureFile(t, root, `package demo

func Helper() {}

func Caller() {
	Helper()
	println("changed")
}
`)
	gitRun(t, gitPath, root, "add", "demo.go")
	gitRun(t, gitPath, root, "commit", "-q", "-m", "head")
	return fixture{
		root: root, base: base, git: gitPath, gitSHA256: fileSHA256(t, gitPath),
	}
}

func newFixtureServer(t *testing.T, fixture fixture) *mcp.Server {
	t.Helper()
	server, err := New(Config{
		Root:                fixture.root,
		Base:                fixture.base,
		GitExecutable:       fixture.git,
		GitExecutableSHA256: fixture.gitSHA256,
	})
	if err != nil {
		t.Fatal(err)
	}
	return server
}

func connect(t *testing.T, server *mcp.Server) (*mcp.ClientSession, func()) {
	t.Helper()
	ctx := context.Background()
	clientTransport, serverTransport := mcp.NewInMemoryTransports()
	serverSession, err := server.Connect(ctx, serverTransport, nil)
	if err != nil {
		t.Fatal(err)
	}
	client := mcp.NewClient(
		&mcp.Implementation{Name: "scopesifter-test", Version: "v1"},
		&mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}},
	)
	clientSession, err := client.Connect(ctx, clientTransport, nil)
	if err != nil {
		_ = serverSession.Close()
		t.Fatal(err)
	}
	return clientSession, func() {
		if err := clientSession.Close(); err != nil {
			t.Errorf("close MCP client: %v", err)
		}
		if err := serverSession.Close(); err != nil {
			t.Errorf("close MCP server: %v", err)
		}
	}
}

func assertToolOutput[T any](
	t *testing.T,
	client *mcp.ClientSession,
	name string,
	arguments map[string]any,
	direct func() (T, error),
) {
	t.Helper()
	response, err := client.CallTool(context.Background(), &mcp.CallToolParams{
		Name: name, Arguments: arguments,
	})
	if err != nil {
		t.Fatalf("call %s: %v", name, err)
	}
	if response.IsError {
		t.Fatalf("call %s returned tool error: %s", name, toolText(response))
	}
	text := toolText(response)
	if text == "" || len(text) > maximumCompactTextBytes || strings.HasPrefix(strings.TrimSpace(text), "{") {
		t.Fatalf("%s text content = %q", name, text)
	}
	expected, err := direct()
	if err != nil {
		t.Fatalf("direct %s: %v", name, err)
	}
	raw, err := json.Marshal(response.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var actual leanResponse
	if err := json.Unmarshal(raw, &actual); err != nil {
		t.Fatalf("decode %s structured output: %v", name, err)
	}
	plan, err := leanResponseFor(name, expected)
	if err != nil {
		t.Fatalf("plan expected %s output: %v", name, err)
	}
	if !fitLeanResponse(&plan, name, structuredOutputBudget) {
		t.Fatalf("expected %s output cannot fit fixed budget", name)
	}
	if !reflect.DeepEqual(actual, plan.response) {
		t.Fatalf("%s output = %#v, want %#v", name, actual, plan.response)
	}
	if text == string(raw) || strings.Contains(text, string(raw)) {
		t.Fatalf("%s duplicated structured JSON in text content", name)
	}
}

func normalizedObject(t *testing.T, value any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	var object map[string]any
	if err := json.Unmarshal(raw, &object); err != nil {
		t.Fatal(err)
	}
	return object
}

func stringArray(value any) []string {
	items, ok := value.([]any)
	if !ok {
		return []string{}
	}
	result := make([]string, 0, len(items))
	for _, item := range items {
		text, ok := item.(string)
		if !ok {
			return nil
		}
		result = append(result, text)
	}
	return result
}

func assertIntegerProperty(
	t *testing.T,
	toolName string,
	properties map[string]any,
	propertyName string,
	defaultValue, maximumValue int,
) {
	t.Helper()
	property, ok := properties[propertyName].(map[string]any)
	if !ok || property["type"] != "integer" || property["minimum"] != float64(1) ||
		property["default"] != float64(defaultValue) ||
		property["maximum"] != float64(maximumValue) {
		t.Fatalf(
			"tool %q property %q = %#v",
			toolName,
			propertyName,
			property,
		)
	}
}

func toolText(response *mcp.CallToolResult) string {
	var texts []string
	for _, content := range response.Content {
		if text, ok := content.(*mcp.TextContent); ok {
			texts = append(texts, text.Text)
		}
	}
	return strings.Join(texts, "\n")
}

type treeEntry struct {
	Mode   fs.FileMode
	SHA256 string
}

func snapshotTree(t *testing.T, root string) map[string]treeEntry {
	t.Helper()
	result := make(map[string]treeEntry)
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		relative, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		item := treeEntry{Mode: info.Mode()}
		if info.Mode().IsRegular() {
			content, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			digest := sha256.Sum256(content)
			item.SHA256 = hex.EncodeToString(digest[:])
		}
		result[filepath.ToSlash(relative)] = item
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return result
}

func gitRun(t *testing.T, gitPath, root string, args ...string) {
	t.Helper()
	command := exec.Command(gitPath, args...)
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func gitOutput(t *testing.T, gitPath, root string, args ...string) string {
	t.Helper()
	command := exec.Command(gitPath, args...)
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(output))
}

func writeFixtureFile(t *testing.T, root, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(root, "demo.go"), []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func copyExecutable(t *testing.T, source, target string) {
	t.Helper()
	content, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, content, 0o700); err != nil {
		t.Fatal(err)
	}
}

func assertAmbientGitInterceptor(t *testing.T, executable, marker string) {
	t.Helper()
	err := exec.Command(executable).Run()
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 99 {
		t.Fatalf("ambient Git interceptor exit = %v, want 99", err)
	}
	content, err := os.ReadFile(marker)
	if err != nil || string(content) != "intercepted" {
		t.Fatalf("ambient Git interceptor marker = %q, %v", content, err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
}
