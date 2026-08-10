package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/dkropachev/repo-view/benchmarks/tokenbench"
	"github.com/dkropachev/repo-view/benchmarks/tokenbench/source"
)

func TestPlanCommandProducesVerifiedSoleDeltaPlan(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	sourceRoot, base, head := commandTestRepository(t, directory)
	treeDigest, err := source.TreeDigest(context.Background(), sourceRoot)
	if err != nil {
		t.Fatal(err)
	}
	harnessPath := commandTestExecutable(t, directory, "fake-harness", "harness")
	repoViewPath := commandTestExecutable(t, directory, "repo-view", "repo-view")
	gitExecutable, gitSHA256 := commandTestGitIdentity(t)
	prompt := []byte("Explain the repository change.\n")
	promptPath := filepath.Join(directory, "prompt.md")
	if err := os.WriteFile(promptPath, prompt, 0o600); err != nil {
		t.Fatal(err)
	}
	suite := tokenbench.Suite{
		SchemaVersion:         tokenbench.SuiteSchemaVersion,
		ID:                    "cli-fixture",
		PromptFile:            "prompt.md",
		HarnessKind:           "fake",
		HarnessExecutable:     harnessPath,
		HarnessSHA256:         tokenbench.SHA256([]byte("harness")),
		GitExecutable:         gitExecutable,
		GitExecutableSHA256:   gitSHA256,
		Model:                 "fixed-model",
		ExpectedModelRevision: "fixed-model@2026-08-01",
		ReasoningEffort:       "medium",
		PermissionProfile:     "read-only",
		DeveloperInstructions: "common instructions",
		SourceRoot:            sourceRoot,
		SourceRevision:        head,
		SourceBaseRevision:    base,
		SourceTreeSHA256:      treeDigest,
		TimeoutMillis:         30_000,
		Repetitions:           10,
		Seed:                  42,
	}
	suiteRaw, err := json.Marshal(suite)
	if err != nil {
		t.Fatal(err)
	}
	suitePath := filepath.Join(directory, "suite.json")
	if err := os.WriteFile(suitePath, suiteRaw, 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	exitCode := run(
		context.Background(),
		[]string{"plan", "--suite", suitePath, "--repo-view-mcp", repoViewPath},
		&stdout,
		&stderr,
	)
	if exitCode != 0 {
		t.Fatalf("plan exit %d: %s", exitCode, stderr.String())
	}
	plan, err := tokenbench.DecodePlan(stdout.Bytes())
	if err != nil {
		t.Fatalf("decode emitted plan: %v\n%s", err, stdout.String())
	}
	if !bytes.Equal(plan.Baseline.Prompt, prompt) ||
		!bytes.Equal(plan.Candidate.Prompt, prompt) {
		t.Fatal("emitted plan changed prompt bytes")
	}
	if len(plan.Baseline.MCPServers) != 0 || len(plan.Candidate.MCPServers) != 1 {
		t.Fatal("emitted plan does not contain the sole MCP delta")
	}
	if plan.Candidate.MCPServers[0].ExecutableSHA256 !=
		tokenbench.SHA256([]byte("repo-view")) {
		t.Fatal("emitted plan did not bind actual repo-view executable")
	}
}

func TestValidateRejectsCandidateOverride(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	promptPath := filepath.Join(directory, "prompt.md")
	if err := os.WriteFile(promptPath, []byte("task\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{
  "schema_version":"tokenbench.suite/v1",
  "candidate_prompt":"answer key"
}`)
	suitePath := filepath.Join(directory, "suite.json")
	if err := os.WriteFile(suitePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if exitCode := run(
		context.Background(),
		[]string{"validate", "--suite", suitePath},
		&stdout,
		&stderr,
	); exitCode == 0 || !strings.Contains(stderr.String(), "unknown field") {
		t.Fatalf("candidate override was not rejected: exit=%d stderr=%q", exitCode, stderr.String())
	}
}

func commandTestExecutable(t *testing.T, directory, name, content string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func commandTestRepository(t *testing.T, directory string) (string, string, string) {
	t.Helper()
	root := filepath.Join(directory, "source")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	commandTestGit(t, root, "init", "--quiet")
	path := filepath.Join(root, "file.txt")
	if err := os.WriteFile(path, []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	commandTestGit(t, root, "add", "file.txt")
	commandTestCommit(t, root, "base")
	base := strings.TrimSpace(commandTestGit(t, root, "rev-parse", "HEAD"))
	if err := os.WriteFile(path, []byte("head\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	commandTestGit(t, root, "add", "file.txt")
	commandTestCommit(t, root, "head")
	head := strings.TrimSpace(commandTestGit(t, root, "rev-parse", "HEAD"))
	return root, base, head
}

func commandTestCommit(t *testing.T, root, message string) {
	t.Helper()
	commandTestGit(
		t,
		root,
		"-c", "user.name=Tokenbench Test",
		"-c", "user.email=tokenbench@example.invalid",
		"commit", "--quiet", "-m", message,
	)
}

func commandTestGit(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	command.Env = append(
		os.Environ(),
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_SYSTEM="+os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
	return string(output)
}

func commandTestGitIdentity(t *testing.T) (string, string) {
	t.Helper()
	path, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	path, err = filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := tokenbench.FileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, digest
}
