package projectcheck

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidateNoScripts(t *testing.T) {
	t.Parallel()
	root := newRepository(t)
	writeTracked(t, root, "main.go", "package main\n")
	writeTracked(t, root, "fixture_test.go", "package fixture\nconst inert = `#!/bin/bash`\n")
	if err := ValidateNoScripts(root); err != nil {
		t.Fatalf("valid repository: %v", err)
	}

	writeTracked(t, root, "run.sh", "#!/usr/bin/env bash\n")
	err := ValidateNoScripts(root)
	if err == nil || !strings.Contains(err.Error(), "tracked script path: run.sh") {
		t.Fatalf("script path error = %v", err)
	}
}

func TestValidateNoScriptsRejectsExtensionlessEntryAndWorkflow(t *testing.T) {
	t.Parallel()
	root := newRepository(t)
	writeTracked(t, root, "scripts/tool", "#!/usr/bin/env -S dash -eu\n")
	writeTracked(t, root, ".github/workflows/workflow.yml", "shell: FISH\n")
	err := ValidateNoScripts(root)
	if err == nil || !strings.Contains(err.Error(), "script shebang: scripts/tool") ||
		!strings.Contains(err.Error(), "workflow shell is not plain sh in .github/workflows/workflow.yml") {
		t.Fatalf("script policy error = %v", err)
	}
}

func TestValidateNoScriptsRejectsMakeWorkflowAndGoExecution(t *testing.T) {
	t.Parallel()
	root := newRepository(t)
	writeTracked(t, root, "Makefile", "run:\n\tDASH -c true\n")
	writeTracked(t, root, ".github/workflows/ci.yaml", "steps:\n  - run: /usr/bin/Fish -c true\n")
	writeTracked(t, root, "runner_test.go", "package runner\nimport x \"os/exec\"\nfunc run() { _ = x.Command(\"z\" + \"sh\", \"-c\", \"true\") }\n")
	err := ValidateNoScripts(root)
	if err == nil || !strings.Contains(err.Error(), "script runtime in Make recipe Makefile") ||
		!strings.Contains(err.Error(), "workflow run command in .github/workflows/ci.yaml") ||
		!strings.Contains(err.Error(), "script-runtime process execution in runner_test.go") {
		t.Fatalf("script execution policy error = %v", err)
	}
}

func TestValidateNoScriptsRejectsSupportedShellShebangs(t *testing.T) {
	t.Parallel()
	for _, runtimeName := range []string{
		"sh", "bash", "dash", "ash", "zsh", "ksh", "fish", "busybox",
		"python", "python2", "python3", "python3.14", "node", "ruby", "perl",
		"pwsh", "powershell",
	} {
		t.Run(runtimeName, func(t *testing.T) {
			t.Parallel()
			root := newRepository(t)
			writeTracked(t, root, "tool", "#!/usr/bin/env -S "+runtimeName+" -e\n")
			err := ValidateNoScripts(root)
			if err == nil || !strings.Contains(err.Error(), "script shebang: tool") {
				t.Fatalf("%s shebang error = %v", runtimeName, err)
			}
		})
	}
}

func TestValidateNoScriptsNormalizesRuntimePathStyles(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"POSIX shebang":   "#!/usr/bin/python3\n",
		"Windows shebang": "#!C:\\Tools\\pwsh.exe\n",
	}
	for name, shebang := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := newRepository(t)
			writeTracked(t, root, "tool", shebang)
			err := ValidateNoScripts(root)
			if err == nil || !strings.Contains(err.Error(), "script shebang: tool") {
				t.Fatalf("%s error = %v", name, err)
			}
		})
	}
}

func TestValidateNoScriptsRejectsScriptSuffixes(t *testing.T) {
	t.Parallel()
	for _, suffix := range []string{
		".sh", ".bash", ".dash", ".ash", ".zsh", ".ksh", ".fish",
		".py", ".rb", ".pl", ".ps1", ".bat", ".cmd",
	} {
		t.Run(suffix, func(t *testing.T) {
			t.Parallel()
			root := newRepository(t)
			path := "tool" + suffix
			writeTracked(t, root, path, "inert fixture bytes\n")
			err := ValidateNoScripts(root)
			if err == nil || !strings.Contains(err.Error(), "tracked script path: "+path) {
				t.Fatalf("%s path error = %v", suffix, err)
			}
		})
	}
}

func TestValidateNoScriptsAllowsWorkflowPOSIXRunnerForMake(t *testing.T) {
	t.Parallel()
	root := newRepository(t)
	writeTracked(t, root, "Makefile", "ci-test:\n\tgo test ./...\n")
	writeTracked(t, root, ".github/workflows/ci.yml", "defaults:\n  run:\n    shell: sh\nsteps:\n  - run: make ci-test\n")
	if err := ValidateNoScripts(root); err != nil {
		t.Fatalf("POSIX workflow runner for Make rejected: %v", err)
	}
}

func TestValidateNoScriptsRejectsNonMakeWorkflowCommands(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"direct tool":        "go test ./...",
		"environment prefix": "MODE=strict make ci-test",
		"operator":           "make ci-test && make ci-build",
		"unknown target":     "make absent",
	}
	for name, command := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := newRepository(t)
			writeTracked(t, root, "Makefile", "ci-test:\n\tgo test ./...\n")
			workflow := "defaults:\n  run:\n    shell: sh\nsteps:\n  - run: " + command + "\n"
			writeTracked(t, root, ".github/workflows/ci.yml", workflow)
			err := ValidateNoScripts(root)
			if err == nil || !strings.Contains(err.Error(), "workflow run command") {
				t.Fatalf("workflow command %q error = %v", command, err)
			}
		})
	}
}

func TestValidateNoScriptsRejectsMultiCommandWorkflowBlock(t *testing.T) {
	t.Parallel()
	root := newRepository(t)
	writeTracked(t, root, "Makefile", "ci-test:\n\tgo test ./...\nci-build:\n\tgo build ./...\n")
	writeTracked(t, root, ".github/workflows/ci.yml", "defaults:\n  run:\n    shell: sh\nsteps:\n  - run: |\n      make ci-test\n      make ci-build\n")
	err := ValidateNoScripts(root)
	if err == nil || !strings.Contains(err.Error(), "run block must contain one Make invocation") {
		t.Fatalf("multi-command workflow block error = %v", err)
	}
}

func TestValidateNoScriptsRejectsGoScriptRuntimeCalls(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"Command":        `x.Command("z" + "sh", "-c", "true")`,
		"CommandContext": `x.CommandContext(c.Background(), "/usr/bin/py" + "thon3", "-c", "pass")`,
		"syscall Exec":   `y.Exec("/bin/da" + "sh", nil, nil)`,
		"Windows path":   `x.Command("C:\\Tools\\pwsh.exe", "-Command", "exit")`,
	}
	for name, call := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := newRepository(t)
			source := "package runner\nimport (\n c \"context\"\n x \"os/exec\"\n y \"syscall\"\n)\nfunc run() { _ = " + call + " }\n"
			writeTracked(t, root, "runner.go", source)
			err := ValidateNoScripts(root)
			if err == nil || !strings.Contains(err.Error(), "script-runtime process execution in runner.go") {
				t.Fatalf("%s error = %v", name, err)
			}
		})
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
