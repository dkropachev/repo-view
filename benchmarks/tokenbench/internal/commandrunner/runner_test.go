package commandrunner

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestInvokedOnlyForPinnedCodexDiscoveryBasename(t *testing.T) {
	t.Parallel()
	if !Invoked("/snapshot/toolbox/bash", "/snapshot/toolbox") {
		t.Fatal("pinned Codex discovery pathname was not recognized")
	}
	for _, input := range []struct {
		argv0 string
		path  string
	}{
		{argv0: "bash", path: "/snapshot/toolbox"},
		{argv0: "/snapshot/toolbox/bash", path: "/other"},
		{argv0: "/snapshot/toolbox/sh", path: "/snapshot/toolbox"},
		{argv0: "/snapshot/toolbox/bash.exe", path: "/snapshot/toolbox"},
		{argv0: "/snapshot/toolbox/bash", path: "/snapshot/toolbox:/other"},
		{},
	} {
		if Invoked(input.argv0, input.path) {
			t.Fatalf("unexpected command-runner dispatch for %+v", input)
		}
	}
}

func TestRunSupportsNavigationCommandSubsetWithoutExternalShell(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	input := filepath.Join(directory, "input.txt")
	if err := os.WriteFile(input, []byte("beta\nalpha\nbeta\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	var stdout, stderr bytes.Buffer
	command := "cat " + quoteWord(input) + " | sort | grep beta | wc -l"
	exit := Run(context.Background(), []string{"-c", command}, nil, &stdout, &stderr)
	if exit != 0 || strings.TrimSpace(stdout.String()) != "2" || stderr.Len() != 0 {
		t.Fatalf("Run() exit=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
}

func TestParserBuildsLiteralArgvPipeline(t *testing.T) {
	t.Parallel()
	program, err := parseCommandProgram(`rg 'a|$b*' "two words" plain\ value | wc -l`)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"rg", "a|$b*", "two words", "plain value"}
	if len(program.pipeline) != 2 || strings.Join(program.pipeline[0], "\x00") != strings.Join(want, "\x00") ||
		strings.Join(program.pipeline[1], "\x00") != "wc\x00-l" {
		t.Fatalf("parseCommandProgram()=%q", program.pipeline)
	}
}

func TestRunRejectsShellLanguage(t *testing.T) {
	t.Parallel()
	for _, command := range []string{
		"source config",
		". config",
		"eval 'rg --files'",
		"value=ok rg needle",
		"rg $value",
		`rg "${value}"`,
		"while true; do true; done",
		"until false; do true; done",
		"if true; then rg needle; fi",
		"for value in x; do rg needle; done",
		"case x in x) rg needle;; esac",
		"lookup() { rg needle; }",
		"rg $(cat input)",
		"rg `cat input`",
		"rg <(cat input)",
		"cat input > >(cat)",
		"rg needle &",
		"rg needle && wc -l",
		"rg needle || wc -l",
		"rg needle; wc -l",
		"cat > output",
		"cat < input",
		"cat <<EOF",
		"rg *.go",
		"rg file?.go",
		"rg {one,two}",
		"rg needle\nwc -l",
		"# rg needle",
	} {
		var stdout, stderr bytes.Buffer
		if exit := Run(context.Background(), []string{"-c", command}, nil, &stdout, &stderr); exit != 2 {
			t.Errorf("Run(%q)=%d, want parse rejection 2; stderr=%q", command, exit, stderr.String())
		}
	}
}

func TestRunPreservesExternalExitStatusAndRejectsOtherArgv(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		args []string
		want int
	}{
		{args: []string{"-lc", "true"}, want: 2},
		{args: []string{"-c"}, want: 2},
		{args: nil, want: 2},
	} {
		var stdout, stderr bytes.Buffer
		if got := Run(context.Background(), test.args, nil, &stdout, &stderr); got != test.want {
			t.Fatalf("Run(%q)=%d, want %d; stderr=%q", test.args, got, test.want, stderr.String())
		}
	}

	var stdout, stderr bytes.Buffer
	exit := Run(
		context.Background(),
		[]string{"-c", "grep absent"},
		strings.NewReader("present\n"),
		&stdout,
		&stderr,
	)
	if exit != 1 || stdout.Len() != 0 || stderr.Len() != 0 {
		t.Fatalf("Run(grep no match)=%d stdout=%q stderr=%q", exit, stdout.String(), stderr.String())
	}
}

func TestRunCancelsExternalCommandWithoutGracePeriod(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	var stdout, stderr bytes.Buffer
	started := time.Now()
	exit := Run(ctx, []string{"-c", "tail -f /dev/null"}, nil, &stdout, &stderr)
	if elapsed := time.Since(started); exit != 124 || elapsed > time.Second {
		t.Fatalf(
			"Run(cancelled external)=%d after %s, want 124 before 1s; stderr=%q",
			exit,
			elapsed,
			stderr.String(),
		)
	}
}

func TestRunRejectsExecutablePathsAndScriptImages(t *testing.T) {
	for _, command := range []string{"./tool.sh", "/tmp/tool.py", "/bin/cat"} {
		var stdout, stderr bytes.Buffer
		if exit := Run(context.Background(), []string{"-c", command}, nil, &stdout, &stderr); exit != 125 {
			t.Errorf("Run(%q)=%d, want policy rejection 125; stderr=%q", command, exit, stderr.String())
		}
	}

	directory := t.TempDir()
	disguisedScript := filepath.Join(directory, "cat")
	if err := os.WriteFile(disguisedScript, []byte("#!/bin/false\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory)
	var stdout, stderr bytes.Buffer
	if exit := Run(context.Background(), []string{"-c", "cat"}, nil, &stdout, &stderr); exit != 125 {
		t.Fatalf("Run(disguised script)=%d, want native-image rejection 125; stderr=%q", exit, stderr.String())
	}
}

func TestClosedCommandEnvironmentDropsToolConfiguration(t *testing.T) {
	t.Parallel()
	got := closedCommandEnvironment()
	want := []string{"HOME=/", "LC_ALL=C", "PATH=", "TZ=UTC"}
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("closedCommandEnvironment()=%q, want %q", got, want)
	}
}

func TestValidateExternalCommandRejectsScriptAndDelegatingTools(t *testing.T) {
	t.Parallel()
	for _, arguments := range [][]string{
		{"bash", "-c", "true"},
		{"awk", "BEGIN { system(\"sh\") }"},
		{"sed", "-n", "1p"},
		{"xargs", "sh"},
		{"find", ".", "-exec", "sh", "{}", ";"},
		{"find", ".", "-delete"},
		{"find", ".", "-fprintf", "output", "%p"},
		{"rg", "--pre=python3", "needle"},
		{"rg", "--search-zip", "needle"},
		{"rg", "-z", "needle"},
		{"rg", "-nzi", "needle"},
		{"sort", "--co=/bin/true", "input"},
		{"sort", "--com=/bin/true", "input"},
		{"sort", "--compress-program=gzip", "input"},
		{"sort", "-ooutput", "input"},
		{"sort", "-uooutput", "input"},
		{"sort", "-ruooutput", "input"},
		{"sort", "--o=output", "input"},
		{"sort", "--out=output", "input"},
		{"sort", "--output=output", "input"},
		{"sort", "-T", "/tmp", "input"},
		{"sort", "-T/tmp", "input"},
		{"sort", "-uT/tmp", "input"},
		{"sort", "--t=/tmp", "input"},
		{"sort", "--temporary-directory=/tmp", "input"},
		{"/bin/cat", "input"},
		{"./cat", "input"},
	} {
		if err := validateExternalCommand(arguments); err == nil {
			t.Errorf("external command accepted: %q", arguments)
		}
	}
	for _, arguments := range [][]string{
		{"rg", "needle", "tool.sh"},
		{"rg", "--no-search-zip", "needle"},
		{"rg", "--", "-z"},
		{"find", ".", "-name", "*.sh"},
		{"cat", "tool.sh"},
		{"sort", "--", "-ooperand"},
		{"sort", "--", "-Toperand"},
	} {
		if err := validateExternalCommand(arguments); err != nil {
			t.Errorf("inert script path rejected for %q: %v", arguments, err)
		}
	}
}

func quoteWord(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
