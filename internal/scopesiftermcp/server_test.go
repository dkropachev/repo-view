package scopesiftermcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
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

	_, _, err = service.find(ctx, nil, findInput{Symbol: "Helper"})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("find error = %v, want context cancellation", err)
	}
}

func TestServerCacheOnlyProviderAndProviderExclusivity(t *testing.T) {
	const (
		base = "1111111111111111111111111111111111111111"
		head = "2222222222222222222222222222222222222222"
	)
	root := t.TempDir()
	writeFixtureFile(t, root, `package demo

func Helper() {}
`)
	patch := "diff --git a/demo.go b/demo.go\n+func Helper() {}\n"
	patchDigest := sha256.Sum256([]byte(patch))
	cache := navigator.ChangedStateCache{
		SchemaVersion: navigator.ChangedStateSchemaVersion,
		BaseCommit:    base,
		HeadCommit:    head,
		HeadSubject:   "cache-only",
		ChangedFiles: []navigator.ChangedFileState{{
			Path: "demo.go", Status: "modified",
			Lines: []navigator.ChangedLineSpan{{Start: 3, End: 3}},
			Patch: patch, PatchSHA256: hex.EncodeToString(patchDigest[:]),
		}},
		Patch: patch,
	}
	raw, err := json.Marshal(cache)
	if err != nil {
		t.Fatal(err)
	}
	cachePath := filepath.Join(t.TempDir(), "changed-state.json")
	if err := os.WriteFile(cachePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	cacheDigest := sha256.Sum256(raw)
	config := Config{
		Root: root, Base: base, Head: head, CachePath: cachePath,
		CacheSHA256: hex.EncodeToString(cacheDigest[:]),
	}
	server, err := New(config)
	if err != nil {
		t.Fatal(err)
	}
	client, closeSessions := connect(t, server)
	defer closeSessions()
	response, err := client.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "changed", Arguments: map[string]any{"path_globs": []string{"demo.go"}},
	})
	if err != nil || response.IsError {
		t.Fatalf("cache-only changed = %#v, %v", response, err)
	}

	invalid := []Config{
		{Root: root, Base: base},
		{
			Root: root, Base: base, Head: head, CachePath: cachePath,
			CacheSHA256: config.CacheSHA256, GitExecutable: "/git",
		},
		{Root: root, Base: base, Head: head},
	}
	for index, candidate := range invalid {
		if _, err := New(candidate); err == nil {
			t.Fatalf("invalid provider configuration %d was accepted", index)
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
		t.Fatalf("server instructions = %q, want empty", initial.Instructions)
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
	wantProperties := map[string][]string{
		"changed": {
			"context", "drop_comments", "drop_docstrings", "exclude_globs",
			"limit", "max_code_lines", "max_patch_lines", "path_globs", "return",
		},
		"find": {
			"changed_only", "context", "drop_comments", "drop_docstrings",
			"exclude_globs", "include", "include_comments", "include_strings",
			"limit", "max_code_lines", "path_globs", "return", "symbol",
		},
		"inspect": {
			"changed_only", "context", "drop_comments", "drop_docstrings",
			"exclude_globs", "include", "include_comments", "include_strings",
			"limit", "location", "max_code_lines", "path_globs", "return",
		},
		"outline": {
			"drop_comments", "drop_docstrings", "limit", "max_code_lines", "path", "return",
		},
	}
	wantNames := []string{"changed", "find", "inspect", "outline"}
	wantRequired := map[string][]string{
		"changed": {},
		"find":    {"symbol"},
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
		if tool.Title != "" || tool.Description == "" || tool.OutputSchema == nil {
			t.Fatalf("tool %q metadata = %#v", tool.Name, tool)
		}
		annotations := tool.Annotations
		if annotations == nil || !annotations.ReadOnlyHint ||
			annotations.OpenWorldHint == nil || *annotations.OpenWorldHint ||
			annotations.DestructiveHint == nil || *annotations.DestructiveHint ||
			annotations.IdempotentHint {
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
		assertIntegerProperty(
			t,
			tool.Name,
			properties,
			"limit",
			defaultLimit,
			maximumLimit,
		)
		assertIntegerProperty(
			t,
			tool.Name,
			properties,
			"max_code_lines",
			defaultMaxCodeLines,
			maximumMaxCodeLines,
		)
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
			Return: navigator.ReturnContext, Context: defaultContext,
			Limit: defaultLimit, MaxCodeLines: defaultMaxCodeLines,
			MaxPatchLines: defaultMaxPatchLines,
		})
	})
	assertToolOutput(t, client, "find", map[string]any{"symbol": "Helper"}, func() (navigator.FindResponse, error) {
		return view.Find("Helper", navigator.Options{
			Base: fixture.base, Include: navigator.IncludeBoth,
			Return: navigator.ReturnScope, Context: defaultContext,
			Limit: defaultLimit, MaxCodeLines: defaultMaxCodeLines,
			MaxPatchLines: defaultMaxPatchLines, NoComments: true, NoStrings: true,
		})
	})
	assertToolOutput(t, client, "inspect", map[string]any{"location": "demo.go:6"}, func() (navigator.InspectResponse, error) {
		return view.Inspect("demo.go:6", navigator.Options{
			Base: fixture.base, Include: navigator.IncludeScope,
			Return: navigator.ReturnScope, Context: defaultContext,
			Limit: defaultLimit, MaxCodeLines: defaultMaxCodeLines,
			MaxPatchLines: defaultMaxPatchLines, NoComments: true, NoStrings: true,
		})
	})
	assertToolOutput(t, client, "outline", map[string]any{"path": "demo.go"}, func() (navigator.OutlineResponse, error) {
		return view.Outline("demo.go", navigator.Options{
			Base: fixture.base, Include: navigator.IncludeDefs,
			Return: navigator.ReturnLine, Limit: defaultLimit,
			MaxCodeLines: defaultMaxCodeLines, MaxPatchLines: defaultMaxPatchLines,
		})
	})

	for _, call := range []struct {
		name      string
		arguments map[string]any
	}{
		{name: "find", arguments: map[string]any{"symbol": "Helper", "root": "/tmp"}},
		{name: "find", arguments: map[string]any{}},
		{name: "find", arguments: map[string]any{"symbol": "Helper", "limit": maximumLimit + 1}},
		{name: "inspect", arguments: map[string]any{"location": "../outside.go:1"}},
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

func TestServerUsesPinnedGitAndIgnoresAmbientInterception(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("shell interception fixture is Unix-specific")
	}
	fixture := newFixture(t)
	marker := filepath.Join(t.TempDir(), "ambient-git-ran")
	fakeDirectory := t.TempDir()
	fakeGit := filepath.Join(fakeDirectory, "git")
	if err := os.WriteFile(
		fakeGit,
		[]byte("#!/bin/sh\nprintf intercepted > '"+marker+"'\nexit 99\n"),
		0o700,
	); err != nil {
		t.Fatal(err)
	}
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
	writeFixtureFile(t, root, `package demo

func Helper() {}

func Caller() {
	Helper()
}
`)
	gitRun(t, gitPath, root, "add", "demo.go")
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
	expected, err := direct()
	if err != nil {
		t.Fatalf("direct %s: %v", name, err)
	}
	raw, err := json.Marshal(response.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	var actual T
	if err := json.Unmarshal(raw, &actual); err != nil {
		t.Fatalf("decode %s structured output: %v", name, err)
	}
	if !reflect.DeepEqual(actual, expected) {
		t.Fatalf("%s output = %#v, want %#v", name, actual, expected)
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
