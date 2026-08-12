package projectcheck

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateNoBash(t *testing.T) {
	t.Parallel()
	root := newRepository(t)
	writeTracked(t, root, "main.go", "package main\n")
	writeTracked(t, root, "fixture_test.go", "package fixture\nconst inert = `#!/bin/bash`\n")
	if err := ValidateNoBash(root); err != nil {
		t.Fatalf("valid repository: %v", err)
	}

	writeTracked(t, root, "run.sh", "#!/usr/bin/env bash\n")
	err := ValidateNoBash(root)
	if err == nil || !strings.Contains(err.Error(), "tracked shell-script path: run.sh") {
		t.Fatalf("shell path error = %v", err)
	}
}

func TestValidateNoBashRejectsExtensionlessEntryAndWorkflow(t *testing.T) {
	t.Parallel()
	root := newRepository(t)
	writeTracked(t, root, "scripts/tool", "#!/bin/bash\n")
	writeTracked(t, root, ".github/workflows/workflow.yml", "shell: BASH\n")
	err := ValidateNoBash(root)
	if err == nil || !strings.Contains(err.Error(), "bash shebang: scripts/tool") ||
		!strings.Contains(err.Error(), "bash workflow shell in .github/workflows/workflow.yml") {
		t.Fatalf("Bash policy error = %v", err)
	}
}

func TestValidateNoBashRejectsMakeWorkflowAndGoExecution(t *testing.T) {
	t.Parallel()
	root := newRepository(t)
	writeTracked(t, root, "Makefile", "run:\n\tBASH -c true\n")
	writeTracked(t, root, ".github/workflows/ci.yaml", "steps:\n  - run: /bin/Bash -c true\n")
	writeTracked(t, root, "runner_test.go", "package runner\nimport x \"os/exec\"\nfunc run() { _ = x.Command(\"ba\" + \"sh\", \"-c\", \"true\") }\n")
	err := ValidateNoBash(root)
	if err == nil || !strings.Contains(err.Error(), "bash Make recipe in Makefile") ||
		!strings.Contains(err.Error(), "bash workflow command in .github/workflows/ci.yaml") ||
		!strings.Contains(err.Error(), "bash process execution in runner_test.go") {
		t.Fatalf("Bash execution policy error = %v", err)
	}
}

func TestValidateJSON(t *testing.T) {
	t.Parallel()
	root := newRepository(t)
	writeTracked(t, root, "valid.json", "{\"ok\":true}\n")
	if err := ValidateJSON(root); err != nil {
		t.Fatalf("valid JSON: %v", err)
	}
	writeTracked(t, root, "invalid.json", "{\"ok\":}\n")
	if err := ValidateJSON(root); err == nil || !strings.Contains(err.Error(), "invalid.json") {
		t.Fatalf("invalid JSON error = %v", err)
	}
}

func newRepository(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	command := exec.Command("git", "init", "-q", root)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	return root
}

func writeTracked(t *testing.T, root, path, content string) {
	t.Helper()
	fullPath := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("git", "add", "--", path)
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, output)
	}
}
