//go:build aix || darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd || solaris

package main

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

const signalHelperEnvironment = "SCOPESIFTER_CODEX_SIGNAL_HELPER"

func runSignalLifecycleHelper() bool {
	if os.Getenv(signalHelperEnvironment) != "1" {
		return false
	}
	received := make(chan os.Signal, 1)
	signal.Notify(received, os.Interrupt, syscall.SIGTERM)
	if err := os.WriteFile(os.Getenv("SCOPESIFTER_CODEX_READY_FILE"), []byte("ready\n"), 0o600); err != nil {
		os.Exit(91)
	}
	forwarded := <-received
	if err := os.WriteFile(
		os.Getenv("SCOPESIFTER_CODEX_SIGNAL_FILE"),
		[]byte(forwarded.String()+"\n"),
		0o600,
	); err != nil {
		os.Exit(92)
	}
	signal.Stop(received)
	signal.Reset(forwarded)
	value, ok := forwarded.(syscall.Signal)
	if !ok {
		os.Exit(93)
	}
	if err := syscall.Kill(os.Getpid(), value); err != nil {
		os.Exit(94)
	}
	time.Sleep(time.Second)
	os.Exit(95)
	return true
}

func TestCommandForwardsTerminationAndCleansRunArtifacts(t *testing.T) {
	for _, terminationSignal := range []syscall.Signal{syscall.SIGINT, syscall.SIGTERM} {
		t.Run(terminationSignal.String(), func(t *testing.T) {
			testCommandSignalCleanup(t, terminationSignal)
		})
	}
}

func testCommandSignalCleanup(t *testing.T, terminationSignal syscall.Signal) {
	t.Helper()
	root := filepath.Join("..", "..")
	temporary := t.TempDir()
	launcher := filepath.Join(temporary, "scopesifter-codex")
	build := exec.Command(
		"go",
		"build",
		"-trimpath",
		"-o",
		launcher,
		"./cmd/scopesifter-codex",
	)
	build.Dir = root
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build launcher: %v\n%s", err, output)
	}
	testExecutable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	fakeBin := filepath.Join(temporary, "fake-bin")
	if err := os.Mkdir(fakeBin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(testExecutable, filepath.Join(fakeBin, "codex")); err != nil {
		t.Fatal(err)
	}
	readyPath := filepath.Join(temporary, "codex-ready")
	receivedPath := filepath.Join(temporary, "codex-signal")
	binDir := filepath.Join(temporary, "run directory")
	command := exec.Command(launcher, "exec", "--json", "wait for termination")
	command.Dir = root
	command.Env = environmentWithoutScopeSifter()
	command.Env = append(command.Env,
		"PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"),
		signalHelperEnvironment+"=1",
		"SCOPESIFTER_CODEX_READY_FILE="+readyPath,
		"SCOPESIFTER_CODEX_SIGNAL_FILE="+receivedPath,
		"SCOPESIFTER_CACHE_DIR="+filepath.Join(temporary, "cache"),
		"SCOPESIFTER_BIN_DIR="+binDir,
		"SCOPESIFTER_NAVIGATION_COMMAND_CAP=2",
	)
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	waitForPath(t, readyPath, command)

	binaryPath := filepath.Join(binDir, "scopesifter")
	if _, err := os.Stat(binaryPath); err != nil {
		terminateAndWait(command)
		t.Fatalf("compiled binary is absent before interrupt: %v", err)
	}
	entries, err := os.ReadDir(binDir)
	if err != nil {
		terminateAndWait(command)
		t.Fatal(err)
	}
	transcriptPath := ""
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "navigation-transcript.") &&
			strings.HasSuffix(entry.Name(), ".jsonl") {
			transcriptPath = filepath.Join(binDir, entry.Name())
			break
		}
	}
	if transcriptPath == "" {
		terminateAndWait(command)
		t.Fatalf("navigation transcript is absent before interrupt: entries = %#v", entries)
	}

	if err := command.Process.Signal(terminationSignal); err != nil {
		terminateAndWait(command)
		t.Fatal(err)
	}
	waited := make(chan error, 1)
	go func() {
		waited <- command.Wait()
	}()
	select {
	case err := <-waited:
		var exitError *exec.ExitError
		if !errors.As(err, &exitError) || exitError.ExitCode() != 128+int(terminationSignal) {
			t.Fatalf("launcher error = %v, output = %s", err, output.String())
		}
	case <-time.After(20 * time.Second):
		_ = command.Process.Kill()
		<-waited
		t.Fatalf("launcher did not exit after %s; output = %s", terminationSignal, output.String())
	}

	receivedSignal, err := os.ReadFile(receivedPath)
	if err != nil {
		t.Fatalf("Codex did not record the forwarded signal: %v; output = %s", err, output.String())
	}
	if string(receivedSignal) != terminationSignal.String()+"\n" {
		t.Fatalf("Codex received %q, want %q", receivedSignal, terminationSignal.String()+"\n")
	}
	for _, path := range []string{binaryPath, transcriptPath, binDir} {
		if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("interrupt cleanup left %s: %v", path, err)
		}
	}
}

func environmentWithoutScopeSifter() []string {
	environment := make([]string, 0, len(os.Environ()))
	for _, variable := range os.Environ() {
		if !strings.HasPrefix(variable, "SCOPESIFTER_") {
			environment = append(environment, variable)
		}
	}
	return environment
}

func waitForPath(t *testing.T, path string, command *exec.Cmd) {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !errors.Is(err, os.ErrNotExist) {
			terminateAndWait(command)
			t.Fatalf("wait for %s: %v", path, err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	terminateAndWait(command)
	t.Fatalf("timed out waiting for %s", path)
}

func terminateAndWait(command *exec.Cmd) {
	_ = command.Process.Kill()
	_ = command.Wait()
}
