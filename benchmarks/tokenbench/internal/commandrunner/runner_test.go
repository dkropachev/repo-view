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
		t.Fatal("compatibility pathname was not recognized")
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

func TestRunSupportsVariablesConditionalsAndRedirection(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	output := filepath.Join(directory, "output.txt")
	command := "value=ok; if [ \"$value\" = ok ]; then printf '%s\\n' \"$value\" > " + quoteWord(output) + "; fi"
	var stdout, stderr bytes.Buffer
	if exit := Run(context.Background(), []string{"-c", command}, nil, &stdout, &stderr); exit != 0 {
		t.Fatalf("Run() exit=%d stderr=%q", exit, stderr.String())
	}
	raw, err := os.ReadFile(output)
	if err != nil || string(raw) != "ok\n" {
		t.Fatalf("redirection bytes=%q err=%v", raw, err)
	}
}

func TestRunPreservesExitStatusAndRejectsOtherArgv(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		args []string
		want int
	}{
		{args: []string{"-c", "exit 17"}, want: 17},
		{args: []string{"-lc", "true"}, want: 2},
		{args: []string{"-c"}, want: 2},
		{args: nil, want: 2},
	} {
		var stdout, stderr bytes.Buffer
		if got := Run(context.Background(), test.args, nil, &stdout, &stderr); got != test.want {
			t.Fatalf("Run(%q)=%d, want %d; stderr=%q", test.args, got, test.want, stderr.String())
		}
	}
}

func TestRunHonorsCancellation(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	var stdout, stderr bytes.Buffer
	exit := Run(ctx, []string{"-c", "while :; do :; done"}, nil, &stdout, &stderr)
	if exit != 124 {
		t.Fatalf("Run(cancelled)=%d, want 124; stderr=%q", exit, stderr.String())
	}
}

func TestRunCancelsExternalCommandWithoutGracePeriod(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	var stdout, stderr bytes.Buffer
	started := time.Now()
	exit := Run(ctx, []string{"-c", "sleep 30"}, nil, &stdout, &stderr)
	if elapsed := time.Since(started); exit != 124 || elapsed > time.Second {
		t.Fatalf(
			"Run(cancelled external)=%d after %s, want 124 before 1s; stderr=%q",
			exit,
			elapsed,
			stderr.String(),
		)
	}
}

func quoteWord(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}
