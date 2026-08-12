package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yapless/scopesifter/navigator"
)

func TestMCPCommandServesExactStdioSurface(t *testing.T) {
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
	gitContent, err := os.ReadFile(gitPath)
	if err != nil {
		t.Fatal(err)
	}
	gitDigest := sha256.Sum256(gitContent)

	root := t.TempDir()
	mcpTestGit(t, gitPath, root, "init", "-q")
	mcpTestGit(t, gitPath, root, "config", "user.email", "scopesifter@example.test")
	mcpTestGit(t, gitPath, root, "config", "user.name", "scopesifter MCP test")
	if err := os.WriteFile(
		filepath.Join(root, "demo.go"),
		[]byte("package demo\n\nfunc Target() {}\n"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	mcpTestGit(t, gitPath, root, "add", "demo.go")
	mcpTestGit(t, gitPath, root, "commit", "-q", "-m", "base")
	base := mcpTestGitOutput(t, gitPath, root, "rev-parse", "HEAD")

	binary := filepath.Join(t.TempDir(), "scopesifter")
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build scopesifter: %v\n%s", err, output)
	}

	invalid := exec.Command(binary, "mcp")
	invalidOutput, invalidErr := invalid.CombinedOutput()
	var exitError *exec.ExitError
	if !errors.As(invalidErr, &exitError) || exitError.ExitCode() != 2 ||
		!strings.Contains(string(invalidOutput), "--root is required") {
		t.Fatalf("invalid command = %v, output = %s", invalidErr, invalidOutput)
	}

	var serverStderr bytes.Buffer
	command := exec.Command(
		binary,
		"mcp",
		"--root", root,
		"--base", base,
		"--git", gitPath,
		"--git-sha256", hex.EncodeToString(gitDigest[:]),
	)
	command.Env = []string{}
	command.Dir = root
	command.Stderr = &serverStderr
	client := mcp.NewClient(
		&mcp.Implementation{Name: "scopesifter-command-test", Version: "v1"},
		&mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}},
	)
	session, err := client.Connect(
		context.Background(),
		&mcp.CommandTransport{Command: command, TerminateDuration: time.Second},
		nil,
	)
	if err != nil {
		t.Fatalf("connect to scopesifter MCP: %v; stderr: %s", err, serverStderr.String())
	}
	tools, err := session.ListTools(context.Background(), nil)
	if err != nil {
		_ = session.Close()
		t.Fatal(err)
	}
	if len(tools.Tools) != 4 {
		_ = session.Close()
		t.Fatalf("tools = %#v", tools.Tools)
	}
	for index, want := range []string{"changed", "find", "inspect", "outline"} {
		tool := tools.Tools[index]
		if tool.Name != want || tool.Annotations == nil || !tool.Annotations.ReadOnlyHint {
			_ = session.Close()
			t.Fatalf("tool %d = %#v", index, tool)
		}
	}
	response, err := session.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "outline", Arguments: map[string]any{"path": "demo.go"},
	})
	if err != nil || response.IsError {
		_ = session.Close()
		t.Fatalf("outline = %#v, %v", response, err)
	}
	if err := session.Close(); err != nil {
		t.Fatalf("close MCP subprocess: %v; stderr: %s", err, serverStderr.String())
	}
	if serverStderr.Len() != 0 {
		t.Fatalf("MCP server wrote stderr: %s", serverStderr.String())
	}

	cache := navigator.ChangedStateCache{
		SchemaVersion: navigator.ChangedStateSchemaVersion,
		BaseCommit:    base,
		HeadCommit:    base,
		HeadSubject:   "base",
		ChangedFiles:  []navigator.ChangedFileState{},
		Patch:         "",
	}
	cacheRaw, err := json.Marshal(cache)
	if err != nil {
		t.Fatal(err)
	}
	cachePath := filepath.Join(t.TempDir(), "changed-state.json")
	if err := os.WriteFile(cachePath, cacheRaw, 0o600); err != nil {
		t.Fatal(err)
	}
	cacheDigest := sha256.Sum256(cacheRaw)
	var cacheStderr bytes.Buffer
	cacheCommand := exec.Command(
		binary,
		"mcp",
		"--root", root,
		"--base", base,
		"--head", base,
		"--changed-state-cache", cachePath,
		"--changed-state-cache-sha256", hex.EncodeToString(cacheDigest[:]),
	)
	cacheCommand.Env = []string{}
	cacheCommand.Dir = root
	cacheCommand.Stderr = &cacheStderr
	cacheClient := mcp.NewClient(
		&mcp.Implementation{Name: "scopesifter-cache-command-test", Version: "v1"},
		&mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}},
	)
	cacheSession, err := cacheClient.Connect(
		context.Background(),
		&mcp.CommandTransport{Command: cacheCommand, TerminateDuration: time.Second},
		nil,
	)
	if err != nil {
		t.Fatalf("connect to cache-only scopesifter MCP: %v; stderr: %s", err, cacheStderr.String())
	}
	response, err = cacheSession.CallTool(context.Background(), &mcp.CallToolParams{
		Name: "outline", Arguments: map[string]any{"path": "demo.go"},
	})
	if err != nil || response.IsError {
		_ = cacheSession.Close()
		t.Fatalf("cache-only outline = %#v, %v; stderr: %s", response, err, cacheStderr.String())
	}
	if err := cacheSession.Close(); err != nil {
		t.Fatalf("close cache-only MCP subprocess: %v; stderr: %s", err, cacheStderr.String())
	}
	if cacheStderr.Len() != 0 {
		t.Fatalf("cache-only MCP server wrote stderr: %s", cacheStderr.String())
	}
}

func mcpTestGit(t *testing.T, gitPath, root string, args ...string) {
	t.Helper()
	command := exec.Command(gitPath, args...)
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, output)
	}
}

func mcpTestGitOutput(t *testing.T, gitPath, root string, args ...string) string {
	t.Helper()
	command := exec.Command(gitPath, args...)
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return strings.TrimSpace(string(output))
}
