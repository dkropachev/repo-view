package processpolicy

import (
	"errors"
	"testing"
)

func TestValidateRejectsRuntimeExecutables(t *testing.T) {
	for _, test := range []struct {
		name       string
		executable string
		match      string
	}{
		{name: "empty", executable: ""},
		{name: "whitespace", executable: "  \t"},
		{name: "unix path", executable: "/usr/bin/bash", match: "bash"},
		{name: "mixed case", executable: "/opt/bin/PyThOn3.13", match: "python3.13"},
		{name: "windows path", executable: `C:\Windows\System32\WindowsPowerShell\v1.0\PowerShell.EXE`, match: "powershell"},
		{name: "windows command host", executable: "CMD.EXE", match: "cmd"},
		{name: "dos command host", executable: "COMMAND.COM", match: "command.com"},
		{name: "versioned runtime", executable: "ruby3.3", match: "ruby3.3"},
		{name: "versioned debug runtime", executable: "python3.13t", match: "python3.13t"},
		{name: "javascript runtime", executable: "node.exe", match: "node"},
		{name: "typescript runtime", executable: "ts-node", match: "ts-node"},
		{name: "nushell", executable: "Nu.exe", match: "nu"},
		{name: "r runtime", executable: "Rscript", match: "rscript"},
		{name: "jvm script runtime", executable: "GroovySH", match: "groovysh"},
		{name: "windows script host", executable: "wscript.exe", match: "wscript"},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateExecutable(test.executable)
			var violation *ViolationError
			if !errors.As(err, &violation) {
				t.Fatalf("Validate(%q) error = %v, want ViolationError", test.executable, err)
			}
			if violation.ArgumentIndex != ExecutableArgumentIndex {
				t.Fatalf("argument index = %d", violation.ArgumentIndex)
			}
			if test.match == "" {
				if violation.Kind != InvalidExecutable {
					t.Fatalf("kind = %q", violation.Kind)
				}
				return
			}
			if violation.Kind != ScriptRuntime || violation.Match != test.match {
				t.Fatalf("violation = %+v", violation)
			}
		})
	}
}

func TestValidateRejectsScriptExecutableSuffixes(t *testing.T) {
	for _, test := range []struct {
		executable string
		match      string
	}{
		{executable: "/tmp/build.SH", match: ".sh"},
		{executable: `C:\tasks\release.PS1`, match: ".ps1"},
		{executable: "/tmp/tool.py.exe", match: ".py"},
		{executable: ".zshrc", match: ".zshrc"},
		{executable: "worker.kts", match: ".kts"},
		{executable: "runner.vbs", match: ".vbs"},
	} {
		t.Run(test.executable, func(t *testing.T) {
			err := ValidateExecutable(test.executable)
			var violation *ViolationError
			if !errors.As(err, &violation) {
				t.Fatalf("Validate(%q) error = %v, want ViolationError", test.executable, err)
			}
			if violation.Kind != ScriptFile || violation.Match != test.match {
				t.Fatalf("violation = %+v", violation)
			}
		})
	}
}

func TestValidateRejectsRuntimeReferencesInArguments(t *testing.T) {
	for _, test := range []struct {
		name     string
		argument string
		match    string
		wantKind ViolationKind
	}{
		{name: "standalone", argument: "bash", match: "bash", wantKind: ScriptRuntime},
		{name: "unix path", argument: "/usr/bin/env bash", match: "bash", wantKind: ScriptRuntime},
		{name: "windows path", argument: `--shell=C:\Tools\PWSH.EXE`, match: "pwsh", wantKind: ScriptRuntime},
		{name: "assignment", argument: "interpreter=python3.12", match: "python3.12", wantKind: ScriptRuntime},
		{name: "colon delimiter", argument: "exec:node", match: "node", wantKind: ScriptRuntime},
		{name: "comma delimiter", argument: "runner=deno,--allow-read", match: "deno", wantKind: ScriptRuntime},
		{name: "command separator", argument: "true;zsh -c exit", match: "zsh", wantKind: ScriptRuntime},
		{name: "quoted command", argument: `command="Rscript job.R"`, match: "rscript", wantKind: ScriptRuntime},
		{name: "flag name", argument: "--bash", match: "bash", wantKind: ScriptRuntime},
		{name: "hyphen delimiter", argument: "use-bash-now", match: "bash", wantKind: ScriptRuntime},
		{name: "underscore delimiter", argument: "BASH_ENV=/tmp/config", match: "bash", wantKind: ScriptRuntime},
		{name: "env split option", argument: "-Sbash", match: "bash", wantKind: ScriptRuntime},
		{name: "env split version", argument: "-SPython3.11", match: "python3.11", wantKind: ScriptRuntime},
		{name: "script path", argument: "--file=/tmp/build.sh", match: ".sh", wantKind: ScriptFile},
		{name: "script URL", argument: "https://example.test/install.PY?raw=1", match: ".py", wantKind: ScriptFile},
		{name: "trailing punctuation", argument: "run(script.mjs),then", match: ".mjs", wantKind: ScriptFile},
		{name: "windows script", argument: `file:C:\Temp\deploy.CMD`, match: ".cmd", wantKind: ScriptFile},
		{name: "profile", argument: "source=.bash_profile", match: ".bash_profile", wantKind: ScriptFile},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := Validate("native-tool", "safe", test.argument, "also-safe")
			var violation *ViolationError
			if !errors.As(err, &violation) {
				t.Fatalf("Validate argument %q error = %v, want ViolationError", test.argument, err)
			}
			if violation.ArgumentIndex != 1 || violation.Value != test.argument ||
				violation.Kind != test.wantKind || violation.Match != test.match {
				t.Fatalf("violation = %+v", violation)
			}
		})
	}
}

func TestValidatePermitsNativeToolsAndOrdinaryArguments(t *testing.T) {
	for _, test := range []struct {
		name       string
		executable string
		arguments  []string
	}{
		{name: "git", executable: "/usr/bin/git", arguments: []string{"-c", "core.quotePath=false", "status", "--short"}},
		{name: "go", executable: "go", arguments: []string{"test", "./...", "-run", "TestShellfish"}},
		{name: "make", executable: "make", arguments: []string{"--no-builtin-rules", "verify"}},
		{name: "native windows", executable: `C:\Program Files\Tool\worker.EXE`, arguments: []string{"/quiet", `C:\data\report.json`}},
		{name: "similar words", executable: "native-tool", arguments: []string{"bashful", "pythonista", "fisherman", "relationship", "nodemailer"}},
		{name: "non-script suffixes", executable: "compiler", arguments: []string{"archive.py.tar", "report.rst", "module.ts.backup", "data.json"}},
		{name: "ordinary flags", executable: "rg", arguments: []string{"--color=always", "--glob=*.go", "needle", "."}},
		{name: "compiler", executable: "kotlinc", arguments: []string{"Main.kt", "-include-runtime", "-d", "main.jar"}},
		{name: "package manager", executable: "npm", arguments: []string{"view", "tree-sitter", "version"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := Validate(test.executable, test.arguments...); err != nil {
				t.Fatalf("Validate(%q, %#v) = %v", test.executable, test.arguments, err)
			}
		})
	}
}

func TestValidateReportsFirstViolationDeterministically(t *testing.T) {
	err := Validate("git", "status", "python", "build.sh")
	var violation *ViolationError
	if !errors.As(err, &violation) {
		t.Fatalf("error = %v, want ViolationError", err)
	}
	if violation.ArgumentIndex != 1 || violation.Match != "python" {
		t.Fatalf("violation = %+v", violation)
	}
	if got := err.Error(); got != `process policy: argument 1 "python" refers to prohibited script runtime "python"` {
		t.Fatalf("error text = %q", got)
	}
}
