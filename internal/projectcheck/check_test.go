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
	writeTracked(t, root, "Makefile", ".PHONY: ci-test\nci-test:\n\tgo test ./...\n")
	writeTracked(t, root, "scripts/tool", "#!/usr/bin/env lua\n")
	writeTracked(t, root, ".github/workflows/workflow.yml", "jobs:\n  test:\n    steps:\n      - run: make ci-test\n        shell: FISH\n")
	err := ValidateNoScripts(root)
	if err == nil || !strings.Contains(err.Error(), "project-owned shebang: scripts/tool") ||
		!strings.Contains(err.Error(), "workflow shell is not the exact Go target runner") {
		t.Fatalf("script policy error = %v", err)
	}
}

func TestValidateNoScriptsRejectsMakeWorkflowAndGoExecution(t *testing.T) {
	t.Parallel()
	root := newRepository(t)
	writeTracked(t, root, "Makefile", ".PHONY: run\nrun:\n\tgo test ./...\n")
	writeTracked(
		t,
		root,
		".github/workflows/ci.yaml",
		"jobs:\n  test:\n    steps:\n      - run: /usr/bin/Fish -c true\n        shell: "+WorkflowShell+"\n",
	)
	writeTracked(t, root, "runner_test.go", "package runner\nimport x \"os/exec\"\nfunc run() { _ = x.Command(\"z\" + \"sh\", \"-c\", \"true\") }\n")
	err := ValidateNoScripts(root)
	if err == nil || !strings.Contains(err.Error(), "workflow run command") ||
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
			if err == nil || !strings.Contains(err.Error(), "project-owned shebang: tool") {
				t.Fatalf("%s shebang error = %v", runtimeName, err)
			}
		})
	}
}

func TestValidateNoScriptsRejectsCanonicalPolicyRuntimeAndSuffix(t *testing.T) {
	t.Parallel()
	root := newRepository(t)
	writeTracked(t, root, "runner.go", "package runner\nimport \"os/exec\"\nfunc run() { _ = exec.Command(\"julia\", \"-e\", \"println(1)\") }\n")
	writeTracked(t, root, "fixture.jl", "println(1)\n")
	err := ValidateNoScripts(root)
	if err == nil || !strings.Contains(err.Error(), "tracked script path: fixture.jl") ||
		!strings.Contains(err.Error(), "script-runtime process execution in runner.go") {
		t.Fatalf("canonical process policy was not enforced: %v", err)
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
			if err == nil || !strings.Contains(err.Error(), "project-owned shebang: tool") {
				t.Fatalf("%s error = %v", name, err)
			}
		})
	}
}

func TestValidateNoScriptsRejectsScriptSuffixes(t *testing.T) {
	t.Parallel()
	for _, suffix := range []string{
		".sh", ".bash", ".dash", ".ash", ".zsh", ".ksh", ".fish",
		".py", ".pyw", ".rb", ".pl", ".ps1", ".bat", ".cmd",
		".js", ".jsx", ".mjs", ".cjs", ".ts", ".tsx", ".lua", ".awk",
		".php", ".tcl", ".r", ".groovy", ".kts", ".nu", ".vbs", ".wsf", ".wsh",
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

func TestValidateNoScriptsAllowsWorkflowGoTargetRunner(t *testing.T) {
	t.Parallel()
	root := newRepository(t)
	writeTracked(t, root, "Makefile", ".PHONY: ci-test\nci-test:\n\tgo test ./...\n")
	workflow := "defaults:\n  run:\n    shell: " + WorkflowShell + "\njobs:\n  test:\n" +
		"    runs-on: ubuntu-latest\n    timeout-minutes: 20\n    steps:\n" +
		"      - run: ci-test\n"
	writeTracked(t, root, ".github/workflows/ci.yml", workflow)
	if err := ValidateNoScripts(root); err != nil {
		t.Fatalf("Go workflow target runner rejected: %v", err)
	}
}

func TestValidateNoScriptsRejectsWorkflowShellRunnerSubstitution(t *testing.T) {
	t.Parallel()
	for _, shell := range []string{
		"sh",
		"bash",
		"go run -mod=readonly ./internal/cmd/workflow-runner --fixed {0}",
	} {
		t.Run(shell, func(t *testing.T) {
			t.Parallel()
			root := newRepository(t)
			writeTracked(t, root, "Makefile", ".PHONY: ci-test\nci-test:\n\tgo test ./...\n")
			workflow := "defaults:\n  run:\n    shell: " + shell + "\njobs:\n  test:\n" +
				"    runs-on: ubuntu-latest\n    timeout-minutes: 20\n    steps:\n" +
				"      - run: ci-test\n"
			writeTracked(t, root, ".github/workflows/ci.yml", workflow)
			err := ValidateNoScripts(root)
			if err == nil || !strings.Contains(err.Error(), "workflow shell is not the exact Go target runner") {
				t.Fatalf("workflow shell %q error = %v", shell, err)
			}
		})
	}
}

func TestValidateNoScriptsRejectsReviewedTargetMissingFromMakefiles(t *testing.T) {
	t.Parallel()
	root := newRepository(t)
	writeTracked(t, root, "Makefile", ".PHONY: ci-test\nci-test:\n\tgo test ./...\n")
	workflow := "defaults:\n  run:\n    shell: " + WorkflowShell + "\njobs:\n  test:\n" +
		"    runs-on: ubuntu-latest\n    timeout-minutes: 20\n    steps:\n" +
		"      - run: ci-build\n"
	writeTracked(t, root, ".github/workflows/ci.yml", workflow)
	err := ValidateNoScripts(root)
	if err == nil || !strings.Contains(err.Error(), `unknown Make target "ci-build"`) {
		t.Fatalf("missing Make target error = %v", err)
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
			writeTracked(t, root, "Makefile", ".PHONY: ci-test\nci-test:\n\tgo test ./...\n")
			workflow := "defaults:\n  run:\n    shell: " + WorkflowShell + "\njobs:\n  test:\n    steps:\n      - run: " + command + "\n"
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
	writeTracked(t, root, "Makefile", ".PHONY: ci-test ci-build\nci-test:\n\tgo test ./...\nci-build:\n\tgo build ./...\n")
	workflow := "defaults:\n  run:\n    shell: " + WorkflowShell + "\njobs:\n  test:\n" +
		"    runs-on: ubuntu-latest\n    timeout-minutes: 20\n    steps:\n      - run: |\n" +
		"          ci-test\n" +
		"          ci-build\n"
	writeTracked(t, root, ".github/workflows/ci.yml", workflow)
	err := ValidateNoScripts(root)
	if err == nil || !strings.Contains(err.Error(), "must be one exact reviewed Make target token") {
		t.Fatalf("multi-command workflow block error = %v", err)
	}
}

func TestValidateNoScriptsRejectsExecutableTrackedFile(t *testing.T) {
	t.Parallel()
	root := newRepository(t)
	writeTrackedMode(t, root, "tool.bin", "compiled-looking bytes\n", 0o755)
	err := ValidateNoScripts(root)
	if err == nil || !strings.Contains(err.Error(), "tracked executable or special-mode path: tool.bin (100755)") {
		t.Fatalf("executable mode error = %v", err)
	}
}

func TestValidateNoScriptsRejectsShellCapableMakeSyntax(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"at prefix":       "run:\n\t@bash -c true\n",
		"ignore prefix":   "run:\n\t-bash -c true\n",
		"force prefix":    "run:\n\t+sh -c true\n",
		"operator":        "run:\n\tgo test ./... && echo bad\n",
		"heredoc":         "run:\n\tgo run ./cmd/tool <<EOF\n",
		"continuation":    "run:\n\tgo test \\\n./...\n",
		"inline recipe":   "run: ; bash -c true\n",
		"shell assign":    "SHELL := /bin/bash\nrun:\n\tgo test ./...\n",
		"shell function":  "X := $(shell bash -c true)\nrun:\n\tgo test ./...\n",
		"define":          "define X\nbash -c true\nendef\n",
		"eval":            "$(eval X := bad)\n",
		"expansion":       "run:\n\tgo run $(TARGET)\n",
		"Go runner arg":   "run:\n\tgo run ./cmd/tool -engine=bash\n",
		"Bash build name": "run:\n\tgo build -o bin/bash ./cmd/tool\n",
		"option target":   ".PHONY: -fevil\n-fevil:\n\tgo test ./...\n",
	}
	for name, makefile := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := newRepository(t)
			writeTracked(t, root, "Makefile", makefile)
			if err := ValidateNoScripts(root); err == nil || !strings.Contains(err.Error(), "Make") {
				t.Fatalf("unsafe Make syntax accepted: %v", err)
			}
		})
	}
}

func TestValidateNoScriptsRejectsContainerBuildFilesAndGoGenerate(t *testing.T) {
	t.Parallel()
	root := newRepository(t)
	writeTracked(t, root, "support/Containerfile.toolchain", "RUN bash -c true\n")
	writeTracked(t, root, "generate.go", "package generate\n//go:generate bash -c true\n")
	err := ValidateNoScripts(root)
	if err == nil ||
		!strings.Contains(err.Error(), "tracked shell-capable container build file: support/Containerfile.toolchain") ||
		!strings.Contains(err.Error(), "go generation directive is forbidden in generate.go") {
		t.Fatalf("container or Go generation automation accepted: %v", err)
	}
}

func TestValidateNoScriptsRejectsScriptAutomationConfigs(t *testing.T) {
	t.Parallel()
	for _, name := range []string{
		"package.json", "deno.json", "bunfig.toml", "Justfile", "Rakefile", "Taskfile.yml", ".envrc",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := newRepository(t)
			writeTracked(t, root, name, "script automation bytes\n")
			err := ValidateNoScripts(root)
			if err == nil || !strings.Contains(err.Error(), "tracked script-automation config: "+name) {
				t.Fatalf("script automation config accepted: %v", err)
			}
		})
	}
}

func TestValidateNoScriptsAcceptsNarrowMakeGrammar(t *testing.T) {
	t.Parallel()
	root := newRepository(t)
	writeTracked(t, root, "Makefile", "include make/tool.mk\n\n.PHONY: build\nbuild:\n\tmkdir -p bin\n\tgo build -trimpath -o bin/tool ./cmd/tool\n")
	writeTracked(t, root, "make/tool.mk", ".PHONY: ci-test\nexport FIXTURE_ROOT\nci-test:\n\tgo test ./... -count=1\n")
	if err := ValidateNoScripts(root); err != nil {
		t.Fatalf("narrow Make grammar rejected: %v", err)
	}
}

func TestValidateTrackedMakeTargetAcceptsCompleteSafeGraph(t *testing.T) {
	t.Parallel()
	root := newRepository(t)
	writeTracked(t, root, "Makefile", "include make/ci.mk\n")
	writeTracked(t, root, "make/ci.mk", ".PHONY: ci-test\nci-test:\n\tgo test ./... -count=1\n")
	if err := ValidateTrackedMakeTarget(root, "ci-test"); err != nil {
		t.Fatalf("safe tracked Make graph rejected: %v", err)
	}
}

func TestValidateTrackedMakeTargetRejectsSpecialModeMakePath(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	command := exec.Command("git", "init", "-q", root)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v: %s", err, output)
	}
	writeTrackedMode(t, root, "Makefile", ".PHONY: ci-test\nci-test:\n\tgo test ./...\n", 0o755)
	err := ValidateTrackedMakeTarget(root, "ci-test")
	if err == nil || !strings.Contains(err.Error(), "must have mode 100644") {
		t.Fatalf("special-mode Makefile error = %v", err)
	}
}

func TestValidateTrackedMakeTargetRejectsWorktreeSymlink(t *testing.T) {
	t.Parallel()
	root := newRepository(t)
	writeTracked(t, root, "Makefile", ".PHONY: ci-test\nci-test:\n\tgo test ./...\n")
	replacement := filepath.Join(root, "replacement")
	if err := os.WriteFile(replacement, []byte(".PHONY: ci-test\nci-test:\n\tgo test ./...\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	makefile := filepath.Join(root, "Makefile")
	if err := os.Remove(makefile); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(replacement, makefile); err != nil {
		t.Fatal(err)
	}
	err := ValidateTrackedMakeTarget(root, "ci-test")
	if err == nil || !strings.Contains(err.Error(), "not a regular non-symlink file") {
		t.Fatalf("worktree Makefile symlink error = %v", err)
	}
}

func TestValidateNoScriptsRejectsImplicitMakeEntrypoints(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"GNUmakefile", "makefile"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := newRepository(t)
			writeTracked(t, root, name, ".PHONY: ci-test\nci-test:\n\tbash -c true\n")
			err := ValidateNoScripts(root)
			if err == nil || !strings.Contains(err.Error(), "tracked implicit Make entrypoint is forbidden: "+name) {
				t.Fatalf("implicit Make entrypoint accepted: %v", err)
			}
		})
	}
}

func TestValidateNoScriptsRejectsMultipleMakeIncludeTokens(t *testing.T) {
	t.Parallel()
	root := newRepository(t)
	writeTracked(t, root, "Makefile", "include evil.txt safe.mk\n")
	writeTracked(t, root, "evil.txt safe.mk", "include safe.mk\n")
	writeTracked(t, root, "evil.txt", "$(info bypass)\n")
	writeTracked(t, root, "safe.mk", "# safe\n")
	err := ValidateNoScripts(root)
	if err == nil || !strings.Contains(err.Error(), "unsafe Make include in Makefile:1") {
		t.Fatalf("multiple Make include tokens accepted: %v", err)
	}
}

func TestValidateNoScriptsRejectsWorkflowEnvironmentInjection(t *testing.T) {
	t.Parallel()
	for name, value := range map[string]string{
		"GOFLAGS":   "-toolexec=/bin/bash",
		"MAKEFLAGS": "--eval=$$(shell bash -c true)",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := newRepository(t)
			writeTracked(t, root, "Makefile", ".PHONY: ci-test\nci-test:\n\tgo test ./...\n")
			workflow := "defaults:\n  run:\n    shell: " + WorkflowShell + "\njobs:\n  test:\n" +
				"    runs-on: ubuntu-latest\n    timeout-minutes: 20\n    steps:\n" +
				"      - run: ci-test\n        env:\n          " + name + ": " + value + "\n"
			writeTracked(t, root, ".github/workflows/ci.yml", workflow)
			err := ValidateNoScripts(root)
			if err == nil || !strings.Contains(err.Error(), "environment differs from the reviewed target-specific set") {
				t.Fatalf("workflow %s injection accepted: %v", name, err)
			}
		})
	}
}

func TestValidateNoScriptsRejectsUnknownWorkflowExecutionSurfaces(t *testing.T) {
	t.Parallel()
	tests := map[string]struct {
		extra   string
		failure string
	}{
		"job container": {
			extra:   "    container: ubuntu:latest\n",
			failure: "workflow job boundary differs",
		},
		"job environment": {
			extra:   "    environment: unsafe\n",
			failure: "workflow job boundary differs",
		},
		"job dependency": {
			extra:   "    needs: unsafe\n",
			failure: "workflow job boundary differs",
		},
		"job services": {
			extra:   "    services:\n      database:\n        image: postgres:latest\n",
			failure: "field services not found",
		},
		"job env": {
			extra:   "    env:\n      MAKEFLAGS: unsafe\n",
			failure: "field env not found",
		},
	}
	for name, test := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := newRepository(t)
			workflow := "jobs:\n  test:\n    runs-on: ubuntu-latest\n    timeout-minutes: 20\n" + test.extra +
				"    steps:\n      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1\n" +
				"        with:\n          persist-credentials: false\n"
			writeTracked(t, root, ".github/workflows/ci.yml", workflow)
			err := ValidateNoScripts(root)
			if err == nil || !strings.Contains(err.Error(), test.failure) {
				t.Fatalf("unknown workflow surface %s accepted: %v", name, err)
			}
		})
	}
}

func TestValidateNoScriptsPinsReviewedWorkflowActions(t *testing.T) {
	t.Parallel()
	root := newRepository(t)
	workflow := "jobs:\n  test:\n    runs-on: ubuntu-latest\n    timeout-minutes: 20\n    steps:\n" +
		"      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1\n" +
		"        with:\n          persist-credentials: false\n"
	writeTracked(t, root, ".github/workflows/ci.yml", workflow)
	if err := ValidateNoScripts(root); err != nil {
		t.Fatalf("reviewed pinned action rejected: %v", err)
	}
	writeTracked(t, root, ".github/workflows/ci.yml", strings.Replace(
		workflow,
		"actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1",
		"./.github/actions/checkout",
		1,
	))
	err := ValidateNoScripts(root)
	if err == nil || !strings.Contains(err.Error(), "workflow action is local, unapproved, unpinned, or malformed") {
		t.Fatalf("local workflow action accepted: %v", err)
	}
}

func TestValidateNoScriptsAcceptsOnlyReviewedAttestationInputs(t *testing.T) {
	t.Parallel()
	const action = "actions/attest@1e69f48acb82d1966a394da916b4c1698aa569d6"
	for _, inputs := range []map[string]string{
		{"subject-checksums": "dist/SHA256SUMS"},
		{"subject-path": "dist/SHA256SUMS"},
	} {
		if err := validateWorkflowAction("test", action, inputs, nil); err != nil {
			t.Fatalf("reviewed attestation inputs %v rejected: %v", inputs, err)
		}
	}
	if err := validateWorkflowAction(
		"test",
		action,
		map[string]string{"subject-path": "dist/**"},
		nil,
	); err == nil ||
		!strings.Contains(err.Error(), "differs from the reviewed") {
		t.Fatalf("unreviewed attestation input accepted: %v", err)
	}
}

func TestValidateNoScriptsAcceptsExactReleaseTestGoSetup(t *testing.T) {
	t.Parallel()
	const action = "actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e"
	inputs := map[string]string{"cache": "false", "go-version": "1.26.5"}
	if err := validateWorkflowAction("test", action, inputs, nil); err != nil {
		t.Fatalf("exact release-test Go setup rejected: %v", err)
	}
	inputs["cache"] = "true"
	if err := validateWorkflowAction("test", action, inputs, nil); err == nil ||
		!strings.Contains(err.Error(), "differs from the reviewed") {
		t.Fatalf("cached release-test Go setup accepted: %v", err)
	}
}

const reviewedReleaseWorkflowFixture = `name: Release

on:
  push:
    tags:
      - "v*"

permissions:
  contents: read

concurrency:
  group: release-${{ github.ref }}

defaults:
  run:
    shell: ` + WorkflowShell + `

jobs:
  test:
    name: Test
    runs-on: ubuntu-24.04
    timeout-minutes: 20
    permissions:
      contents: read

    steps:
      - name: Check out repository
        uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1
        with:
          persist-credentials: false

      - name: Set up Go
        uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e
        with:
          go-version: "1.26.5"
          cache: false

      - name: Test
        run: ci-test

  release:
    name: Release
    needs: test
    environment: release
    runs-on: ubuntu-24.04
    container: golang:1.26.5-bookworm@sha256:0d327c83532d3cdeeeebab56ce85962bf09cb89545355b10207c7771b0c3713f
    timeout-minutes: 20
    permissions:
      artifact-metadata: write
      attestations: write
      contents: read
      id-token: write

    steps:
      - name: Check out repository
        uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1
        with:
          persist-credentials: false

      - name: Build release archives
        run: release-artifacts

      - name: Attest release artifacts
        uses: actions/attest@1e69f48acb82d1966a394da916b4c1698aa569d6
        with:
          subject-checksums: dist/SHA256SUMS

      - name: Attest release manifest
        uses: actions/attest@1e69f48acb82d1966a394da916b4c1698aa569d6
        with:
          subject-path: dist/SHA256SUMS

      - name: Publish GitHub release
        env:
          GH_TOKEN: ${{ secrets.SCOPESIFTER_RELEASE_TOKEN }}
        run: release-publish
`

func TestReleaseWorkflowFixtureMatchesExactReviewedContract(t *testing.T) {
	t.Parallel()
	if err := rejectWorkflowScripts(
		".github/workflows/release.yml",
		reviewedReleaseWorkflowFixture,
		releaseWorkflowTargets(),
	); err != nil {
		t.Fatalf("exact reviewed release workflow rejected: %v", err)
	}
}

func TestTrackedReleaseWorkflowMatchesExactReviewedContract(t *testing.T) {
	t.Parallel()
	content, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if err := rejectWorkflowScripts(
		".github/workflows/release.yml",
		string(content),
		releaseWorkflowTargets(),
	); err != nil {
		t.Fatalf("tracked release workflow rejected: %v", err)
	}
}

func TestReleaseWorkflowRejectsContractMutations(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		old  string
		new  string
	}{
		{name: "workflow name", old: "name: Release\n\non:", new: "name: Publish\n\non:"},
		{name: "tag trigger", old: `- "v*"`, new: `- "release-*"`},
		{name: "branch trigger", old: "    tags:\n", new: "    branches: [main]\n    tags:\n"},
		{name: "workflow permission", old: "permissions:\n  contents: read\n\nconcurrency:", new: "permissions:\n  contents: write\n\nconcurrency:"},
		{name: "concurrency group", old: "  group: release-${{ github.ref }}\n", new: "  group: release-${{ github.sha }}\n"},
		{name: "concurrency cancellation", old: "  group: release-${{ github.ref }}\n", new: "  group: release-${{ github.ref }}\n  cancel-in-progress: true\n"},
		{name: "default shell", old: WorkflowShell, new: WorkflowShell + " --fixed"},
		{name: "test job identifier", old: "jobs:\n  test:\n", new: "jobs:\n  verify:\n"},
		{name: "test job name", old: "  test:\n    name: Test\n", new: "  test:\n    name: Verify\n"},
		{name: "test runner", old: "  test:\n    name: Test\n    runs-on: ubuntu-24.04\n", new: "  test:\n    name: Test\n    runs-on: ubuntu-latest\n"},
		{name: "test timeout", old: "  test:\n    name: Test\n    runs-on: ubuntu-24.04\n    timeout-minutes: 20\n", new: "  test:\n    name: Test\n    runs-on: ubuntu-24.04\n    timeout-minutes: 21\n"},
		{name: "test permission", old: "  test:\n    name: Test\n    runs-on: ubuntu-24.04\n    timeout-minutes: 20\n    permissions:\n      contents: read\n", new: "  test:\n    name: Test\n    runs-on: ubuntu-24.04\n    timeout-minutes: 20\n    permissions:\n      contents: write\n"},
		{name: "test setup pin", old: "actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e", new: "actions/setup-go@main"},
		{name: "test Go version", old: `go-version: "1.26.5"`, new: `go-version: "1.26.4"`},
		{name: "test Go cache", old: "          cache: false\n", new: "          cache: true\n"},
		{name: "test target", old: "      - name: Test\n        run: ci-test\n", new: "      - name: Test\n        run: ci-vet\n"},
		{name: "release job identifier", old: "\n  release:\n    name: Release\n", new: "\n  publish:\n    name: Release\n"},
		{name: "release job name", old: "  release:\n    name: Release\n", new: "  release:\n    name: Publish\n"},
		{name: "release dependency", old: "    needs: test\n", new: "    needs: verify\n"},
		{name: "release environment", old: "    environment: release\n", new: "    environment: production\n"},
		{name: "release runner", old: "    environment: release\n    runs-on: ubuntu-24.04\n", new: "    environment: release\n    runs-on: ubuntu-latest\n"},
		{name: "release container", old: "golang:1.26.5-bookworm@sha256:0d327c83532d3cdeeeebab56ce85962bf09cb89545355b10207c7771b0c3713f", new: "golang:1.26.5-bookworm"},
		{name: "release timeout", old: "    container: golang:1.26.5-bookworm@sha256:0d327c83532d3cdeeeebab56ce85962bf09cb89545355b10207c7771b0c3713f\n    timeout-minutes: 20\n", new: "    container: golang:1.26.5-bookworm@sha256:0d327c83532d3cdeeeebab56ce85962bf09cb89545355b10207c7771b0c3713f\n    timeout-minutes: 21\n"},
		{name: "release permission", old: "      id-token: write\n", new: "      id-token: read\n"},
		{
			name: "release step order",
			old: "      - name: Build release archives\n" +
				"        run: release-artifacts\n\n" +
				"      - name: Attest release artifacts\n" +
				"        uses: actions/attest@1e69f48acb82d1966a394da916b4c1698aa569d6\n" +
				"        with:\n          subject-checksums: dist/SHA256SUMS\n",
			new: "      - name: Attest release artifacts\n" +
				"        uses: actions/attest@1e69f48acb82d1966a394da916b4c1698aa569d6\n" +
				"        with:\n          subject-checksums: dist/SHA256SUMS\n\n" +
				"      - name: Build release archives\n" +
				"        run: release-artifacts\n",
		},
		{name: "checkout pin", old: "actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1", new: "actions/checkout@main"},
		{name: "checkout inputs", old: "persist-credentials: false", new: "persist-credentials: true"},
		{
			name: "setup Go in release job",
			old:  "      - name: Build release archives\n        run: release-artifacts\n",
			new: "      - name: Set up Go again\n" +
				"        uses: actions/setup-go@b7ad1dad31e06c5925ef5d2fc7ad053ef454303e\n" +
				"        with:\n          go-version: \"1.26.5\"\n          cache: false\n\n" +
				"      - name: Build release archives\n        run: release-artifacts\n",
		},
		{name: "artifact attestation", old: "subject-checksums: dist/SHA256SUMS", new: "subject-checksums: dist/OTHER"},
		{name: "manifest attestation", old: "subject-path: dist/SHA256SUMS", new: "subject-path: dist/**"},
		{
			name: "missing manifest attestation",
			old: "\n      - name: Attest release manifest\n" +
				"        uses: actions/attest@1e69f48acb82d1966a394da916b4c1698aa569d6\n" +
				"        with:\n          subject-path: dist/SHA256SUMS\n",
			new: "",
		},
		{name: "publish credential", old: "GH_TOKEN: ${{ secrets.SCOPESIFTER_RELEASE_TOKEN }}", new: "GH_TOKEN: ${{ github.token }}"},
		{name: "publish target", old: "run: release-publish", new: "run: release-artifacts"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if !strings.Contains(reviewedReleaseWorkflowFixture, test.old) {
				t.Fatalf("fixture lacks mutation source %q", test.old)
			}
			mutated := strings.Replace(reviewedReleaseWorkflowFixture, test.old, test.new, 1)
			err := rejectWorkflowScripts(
				".github/workflows/release.yml",
				mutated,
				releaseWorkflowTargets(),
			)
			if err == nil || !strings.Contains(err.Error(), "release workflow differs from the exact reviewed contract") {
				t.Fatalf("release workflow mutation accepted: %v", err)
			}
		})
	}
}

func releaseWorkflowTargets() map[string]struct{} {
	return map[string]struct{}{
		"ci-test":           {},
		"release-artifacts": {},
		"release-publish":   {},
	}
}

func TestValidateNoScriptsRejectsPartialOrMisplacedElevatedPermissions(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		path        string
		permissions string
	}{
		{
			path: ".github/workflows/release.yml",
			permissions: "permissions:\n  contents: write\n  id-token: write\n" +
				"  attestations: write\n",
		},
		{
			path: ".github/workflows/ci.yml",
			permissions: "permissions:\n  contents: write\n  id-token: write\n" +
				"  attestations: write\n  artifact-metadata: write\n",
		},
	} {
		root := newRepository(t)
		workflow := test.permissions + "jobs:\n  test:\n    runs-on: ubuntu-latest\n" +
			"    timeout-minutes: 20\n    steps:\n" +
			"      - uses: actions/checkout@3d3c42e5aac5ba805825da76410c181273ba90b1\n" +
			"        with:\n          persist-credentials: false\n"
		writeTracked(t, root, test.path, workflow)
		if err := ValidateNoScripts(root); err == nil ||
			!strings.Contains(err.Error(), "permissions differ") {
			t.Fatalf("permissions %q at %s accepted: %v", test.permissions, test.path, err)
		}
	}
}

func TestValidateNoScriptsRestrictsElevatedPermissionsToReleaseJob(t *testing.T) {
	t.Parallel()
	elevated := workflowPermissions{
		ArtifactMetadata: "write",
		Attestations:     "write",
		Contents:         "read",
		IDToken:          "write",
	}
	if err := validateWorkflowJobPermissions(
		".github/workflows/release.yml",
		"release",
		elevated,
	); err != nil {
		t.Fatalf("exact release-job permissions rejected: %v", err)
	}
	for _, test := range []struct {
		path string
		job  string
	}{
		{path: ".github/workflows/release.yml", job: "test"},
		{path: ".github/workflows/ci.yml", job: "release"},
	} {
		if err := validateWorkflowJobPermissions(test.path, test.job, elevated); err == nil ||
			!strings.Contains(err.Error(), "job permissions differ") {
			t.Fatalf("elevated permissions accepted at %s job %s: %v", test.path, test.job, err)
		}
	}
}

func TestValidateNoScriptsValidatesWorkflowShellPerRunStep(t *testing.T) {
	t.Parallel()
	root := newRepository(t)
	writeTracked(t, root, "Makefile", ".PHONY: ci-test\nci-test:\n\tgo test ./...\n")
	workflow := "jobs:\n  test:\n    steps:\n      - run: ci-test\n        shell: " + WorkflowShell + "\n      - run: ci-test\n"
	writeTracked(t, root, ".github/workflows/ci.yml", workflow)
	err := ValidateNoScripts(root)
	if err == nil || !strings.Contains(err.Error(), "step 2") || !strings.Contains(err.Error(), "lacks the exact Go target runner") {
		t.Fatalf("unscoped workflow shell was accepted: %v", err)
	}
}

func TestValidateNoScriptsParsesQuotedFlowWorkflowKeys(t *testing.T) {
	t.Parallel()
	root := newRepository(t)
	writeTracked(t, root, "Makefile", ".PHONY: ci-test\nci-test:\n\tgo test ./...\n")
	writeTracked(
		t,
		root,
		".github/workflows/ci.yml",
		`{"jobs":{"test":{"steps":[{"run":"go test ./...","shell":"`+WorkflowShell+`"}]}}}`+"\n",
	)
	err := ValidateNoScripts(root)
	if err == nil || !strings.Contains(err.Error(), "workflow run command") {
		t.Fatalf("flow-style direct command was not parsed and rejected: %v", err)
	}
}

func TestValidateNoScriptsRejectsNonPlainWorkflowDefaults(t *testing.T) {
	t.Parallel()
	root := newRepository(t)
	writeTracked(t, root, "Makefile", ".PHONY: ci-test\nci-test:\n\tgo test ./...\n")
	workflow := "defaults:\n  run:\n    shell: " + WorkflowShell + "\njobs:\n  test:\n    defaults:\n      run:\n        shell: bash\n    steps:\n      - run: ci-test\n        shell: " + WorkflowShell + "\n"
	writeTracked(t, root, ".github/workflows/ci.yml", workflow)
	err := ValidateNoScripts(root)
	if err == nil || !strings.Contains(err.Error(), "job test default") {
		t.Fatalf("non-plain unused job default was accepted: %v", err)
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

func TestValidateNoScriptsRejectsGoProcessAPIScriptBypasses(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"constant command": `package runner
import "os/exec"
const interpreter = "ba" + "sh"
func run() { _ = exec.Command(interpreter, "-c", "true") }
`,
		"dot imported Command": `package runner
import . "os/exec"
func run() { _ = Command("/bin/sh", "-c", "true") }
`,
		"dot imported CommandContext": `package runner
import (
	"context"
	. "os/exec"
)
func run() { _ = CommandContext(context.Background(), "python3", "-c", "pass") }
`,
		"exec Cmd literal": `package runner
import "os/exec"
var command = exec.Cmd{Path: "/usr/bin/zsh", Args: []string{"zsh", "-c", "true"}}
`,
		"dot imported Cmd literal": `package runner
import . "os/exec"
var command = Cmd{Path: "pwsh.exe"}
`,
		"os StartProcess": `package runner
import "os"
func run() { _, _ = os.StartProcess("/bin/dash", nil, nil) }
`,
		"syscall Exec": `package runner
import "syscall"
func run() { _ = syscall.Exec("/bin/bash", nil, nil) }
`,
		"syscall ForkExec": `package runner
import "syscall"
func run() { _, _ = syscall.ForkExec("python3", nil, nil) }
`,
		"syscall StartProcess": `package runner
import "syscall"
func run() { _, _ = syscall.StartProcess("node", nil, nil) }
`,
		"unix Exec": `package runner
import unix "golang.org/x/sys/unix"
func run() { _ = unix.Exec("ruby", nil, nil) }
`,
		"unix ForkExec": `package runner
import . "golang.org/x/sys/unix"
func run() { _, _ = ForkExec("perl", nil, nil) }
`,
		"execabs Command": `package runner
import "golang.org/x/sys/execabs"
func run() { _ = execabs.Command("bash", "-c", "true") }
`,
		"parenthesized process function": `package runner
import "os/exec"
func run() { _ = (exec.Command)("bash", "-c", "true") }
`,
		"process function alias": `package runner
import "os/exec"
func run() {
	command := exec.Command
	_ = command("bash", "-c", "true")
}
`,
		"local executable variable": `package runner
import "os/exec"
func run() {
	runtime := "bash"
	_ = exec.Command(runtime, "-c", "true")
}
`,
		"reassigned executable variable": `package runner
import "os/exec"
func run(condition bool) {
	runtime := "git"
	if condition {
		runtime = "bash"
	}
	_ = exec.Command(runtime, "status")
}
`,
		"exec Cmd type alias": `package runner
import "os/exec"
type command = exec.Cmd
var process = command{Path: "bash", Args: []string{"bash", "-c", "true"}}
`,
		"process function reflection": `package runner
import (
	"os/exec"
	"reflect"
)
func run() { reflect.ValueOf(exec.Command).Call(nil) }
`,
		"uninitialized exec Cmd value": `package runner
import "os/exec"
var command exec.Cmd
func init() { command.Path = "bash" }
`,
		"empty exec Cmd composite": `package runner
import "os/exec"
var command = &exec.Cmd{}
func init() { command.Path = "bash" }
`,
		"new exec Cmd": `package runner
import "os/exec"
func run() {
	command := new(exec.Cmd)
	command.Path = "bash"
	command.Args = []string{"bash", "-c", "true"}
	_ = command.Run()
}
`,
		"embedded exec Cmd": `package runner
import "os/exec"
type runner struct { command exec.Cmd }
func run() { _ = runner{} }
`,
		"script delegated through Git": `package runner
import "os/exec"
func run() { _ = exec.Command("git", "-c", "alias.pwn=!bash -c true", "pwn") }
`,
		"mutated exec Cmd": `package runner
import "os/exec"
func run() {
	command := exec.Command("git", "status")
	command.Path = "/bin/bash"
	command.Args = []string{"bash", "-c", "true"}
	_ = command.Run()
}
`,
	}
	for name, source := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := newRepository(t)
			writeTracked(t, root, "runner.go", source)
			err := ValidateNoScripts(root)
			if err == nil || !strings.Contains(err.Error(), "script-runtime process execution in runner.go") {
				t.Fatalf("%s error = %v", name, err)
			}
		})
	}
}

func TestValidateNoScriptsRejectsGoDispatcherScriptBypasses(t *testing.T) {
	t.Parallel()
	tests := map[string]string{
		"env":     `exec.Command("/usr/bin/env", "-i", "bash", "-c", "true")`,
		"env -S":  `exec.Command("env", "-S", "python3 -c pass")`,
		"xargs":   `exec.Command("xargs", "sh", "-c", "true")`,
		"nice":    `exec.Command("nice", "-n", "5", "node", "tool")`,
		"timeout": `exec.Command("timeout", "5", "ruby", "tool.rb")`,
		"sudo":    `exec.Command("sudo", "--", "pwsh", "-Command", "exit")`,
	}
	for name, call := range tests {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			root := newRepository(t)
			source := "package runner\nimport \"os/exec\"\nfunc run() { _ = " + call + " }\n"
			writeTracked(t, root, "runner.go", source)
			err := ValidateNoScripts(root)
			if err == nil || !strings.Contains(err.Error(), "script-runtime process execution in runner.go") {
				t.Fatalf("%s error = %v", name, err)
			}
		})
	}
}

func TestValidateNoScriptsRejectsUnapprovedDynamicGoProcessCalls(t *testing.T) {
	t.Parallel()
	root := newRepository(t)
	source := `package runner
import (
	"os/exec"
	"strings"
)
func run(arguments []string) {
	executable := strings.Join([]string{"ba", "sh"}, "")
	_ = exec.Command(executable, arguments...)
}
`
	writeTracked(t, root, "runner.go", source)
	err := ValidateNoScripts(root)
	if err == nil || !strings.Contains(err.Error(), "script-runtime process execution in runner.go") {
		t.Fatalf("unapproved dynamic process call accepted: %v", err)
	}
}

func TestValidateNoScriptsRejectsCGIProcessLauncher(t *testing.T) {
	t.Parallel()
	root := newRepository(t)
	source := `package runner
import (
	"net/http"
	"net/http/cgi"
)
func run(response http.ResponseWriter, request *http.Request) {
	(&cgi.Handler{Path: "/bin/bash", Args: []string{"-c", "true"}}).ServeHTTP(response, request)
}
`
	writeTracked(t, root, "runner.go", source)
	err := ValidateNoScripts(root)
	if err == nil || !strings.Contains(err.Error(), "unapproved process-capable Go package in runner.go") {
		t.Fatalf("CGI process launcher accepted: %v", err)
	}
}

func TestValidateNoScriptsAllowsLiteralAndShadowedGoCalls(t *testing.T) {
	t.Parallel()
	root := newRepository(t)
	writeTracked(t, root, "runner.go", "package runner\nimport \"os/exec\"\nfunc run() { _ = exec.Command(\"git\", \"status\") }\n")
	writeTracked(t, root, "shadow.go", `package runner
import "os/exec"
type localExecutor struct{}
func (localExecutor) Command(string, ...string) int { return 0 }
var _ = exec.ErrNotFound
func shadow() {
	exec := localExecutor{}
	_ = exec.Command("bash", "-c", "true")
}
`)
	if err := ValidateNoScripts(root); err != nil {
		t.Fatalf("literal or shadowed process call rejected: %v", err)
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
	writeTracked(t, root, "Makefile", "# project automation\n")
	return root
}

func writeTracked(t *testing.T, root, path, content string) {
	t.Helper()
	writeTrackedMode(t, root, path, content, 0o644)
}

func writeTrackedMode(t *testing.T, root, path, content string, mode os.FileMode) {
	t.Helper()
	fullPath := filepath.Join(root, filepath.FromSlash(path))
	if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fullPath, []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("git", "add", "--", path)
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git add: %v: %s", err, output)
	}
}
