package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
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
	mcpTestGit(t, gitPath, root, "config", "user.email", "repo-view@example.test")
	mcpTestGit(t, gitPath, root, "config", "user.name", "repo-view MCP test")
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

	binary := filepath.Join(t.TempDir(), "repo-view")
	build := exec.Command("go", "build", "-o", binary, ".")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build repo-view: %v\n%s", err, output)
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
		&mcp.Implementation{Name: "repo-view-command-test", Version: "v1"},
		&mcp.ClientOptions{Capabilities: &mcp.ClientCapabilities{}},
	)
	session, err := client.Connect(
		context.Background(),
		&mcp.CommandTransport{Command: command, TerminateDuration: time.Second},
		nil,
	)
	if err != nil {
		t.Fatalf("connect to repo-view MCP: %v; stderr: %s", err, serverStderr.String())
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
