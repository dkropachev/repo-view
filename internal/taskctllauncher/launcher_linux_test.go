//go:build linux

package taskctllauncher

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const (
	helperModeValue          = "taskctl-launcher-test-helper"
	helperRecordCWDValue     = "record-cwd"
	helperSpawnChildValue    = "spawn-child"
	helperWaitForParentValue = "wait-for-parent-death"
	helperChildModeValue     = "taskctl-launcher-test-child"
	helperIgnoreSignalsValue = "ignore-signals"

	parentDeathHarnessEnvironment = "TASKCTL_TEST_PARENT_DEATH_HARNESS"
	parentDeathRootEnvironment    = "TASKCTL_TEST_PARENT_DEATH_ROOT"
	parentDeathDigestEnvironment  = "TASKCTL_TEST_PARENT_DEATH_DIGEST"
)

var launcherHelperExecutable string

func TestMain(m *testing.M) {
	if os.Getenv(parentDeathHarnessEnvironment) == "1" {
		os.Exit(m.Run())
	}
	workRoot, err := os.MkdirTemp("", "taskctl-launcher-test-helper-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	launcherHelperExecutable = filepath.Join(workRoot, "testhelper")
	command := exec.Command(
		"go",
		"build",
		"-mod=readonly",
		"-trimpath",
		"-buildvcs=false",
		"-o",
		launcherHelperExecutable,
		"./internal/taskctllauncher/testhelper",
	)
	command.Dir = filepath.Clean(filepath.Join("..", ".."))
	command.Env = launcherHelperBuildEnvironment(os.Environ(), workRoot)
	if output, err := command.CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build static launcher helper: %v: %s\n", err, output)
		_ = os.RemoveAll(workRoot)
		os.Exit(1)
	}
	code := m.Run()
	if err := os.RemoveAll(workRoot); err != nil && code == 0 {
		fmt.Fprintln(os.Stderr, err)
		code = 1
	}
	os.Exit(code)
}

func TestRunPlatformParentSIGKILLTerminatesTaskctl(t *testing.T) {
	root, _, expectedSHA256 := newLauncherRepository(t)
	marker := filepath.Join(t.TempDir(), "taskctl-pid")
	command := exec.Command(
		os.Args[0],
		"-test.run=^TestLauncherParentDeathHarness$",
	)
	command.Env = []string{
		parentDeathHarnessEnvironment + "=1",
		parentDeathRootEnvironment + "=" + root,
		parentDeathDigestEnvironment + "=" + expectedSHA256,
		"TASKCTL_REPOSITORY_BINDINGS=" + helperModeValue,
		"TASKCTL_OUTPUT=" + marker,
		"TASKCTL_INPUT=" + helperIgnoreSignalsValue,
		"TASKCTL_SOURCE_SELECTIONS=" + helperWaitForParentValue,
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	waited := false
	t.Cleanup(func() {
		if waited || command.Process == nil {
			return
		}
		_ = command.Process.Kill()
		_ = command.Wait()
	})
	waitForPath(t, marker, 5*time.Second)
	pidBytes, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	taskctlPID, err := strconv.Atoi(string(pidBytes))
	if err != nil {
		t.Fatal(err)
	}
	if err := command.Process.Kill(); err != nil {
		t.Fatal(err)
	}
	waitErr := command.Wait()
	waited = true
	var exitErr *exec.ExitError
	if !errors.As(waitErr, &exitErr) || exitErr.ProcessState == nil ||
		exitErr.ProcessState.Sys().(syscall.WaitStatus).Signal() != syscall.SIGKILL {
		t.Fatalf("launcher wait = %v, want SIGKILL; stderr = %q", waitErr, stderr.String())
	}
	waitForProcessExit(t, taskctlPID, 5*time.Second)
}

func TestLauncherParentDeathHarness(t *testing.T) {
	if os.Getenv(parentDeathHarnessEnvironment) != "1" {
		return
	}
	err := runPlatform(
		context.Background(),
		os.Getenv(parentDeathRootEnvironment),
		[]string{"generate", "source-audit"},
		os.Getenv(parentDeathDigestEnvironment),
		nil,
		io.Discard,
		io.Discard,
		launcherHooks{},
	)
	t.Fatalf("taskctl stopped before its launcher was killed: %v", err)
}

func TestRunPlatformExecutesAuthenticatedDescriptor(t *testing.T) {
	root, executable, expectedSHA256 := newLauncherRepository(t)
	marker := filepath.Join(t.TempDir(), "marker")
	configureLauncherHelper(t, marker)
	t.Setenv("TASKCTL_INPUT", "inherited")
	t.Setenv("LD_PRELOAD", "/attacker/libinject.so")
	t.Setenv("LD_LIBRARY_PATH", "/attacker")
	t.Setenv("PATH", "/attacker/bin")
	t.Setenv("HOME", "/attacker/home")
	t.Setenv("GODEBUG", "inittrace=1")
	t.Setenv("GOTRACEBACK", "crash")
	t.Setenv("UNREVIEWED_LAUNCHER_VALUE", "must-not-pass")
	t.Setenv(executableSHA256Environment, expectedSHA256)

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	err := runPlatform(
		context.Background(),
		root,
		[]string{"generate", "source-audit"},
		expectedSHA256,
		strings.NewReader("standard input"),
		&stdout,
		&stderr,
		launcherHooks{},
	)
	if err != nil {
		t.Fatalf("run authenticated %s: %v", executable, err)
	}
	markerBytes, err := os.ReadFile(marker)
	if err != nil {
		t.Fatalf("read helper marker: %v", err)
	}
	if got, want := string(markerBytes), "generate\x00source-audit\ninherited\nstandard input"; got != want {
		t.Fatalf("helper marker = %q, want %q", got, want)
	}
	if got, want := stdout.String(), "helper stdout: inherited\n"; got != want {
		t.Fatalf("stdout = %q, want %q", got, want)
	}
	if got, want := stderr.String(), "helper stderr: inherited\n"; got != want {
		t.Fatalf("stderr = %q, want %q", got, want)
	}
}

func TestRunPlatformPinnedDirectorySurvivesLowDescriptorPressure(t *testing.T) {
	root, executable, expectedSHA256 := newLauncherRepository(t)
	marker := filepath.Join(t.TempDir(), "marker")
	configureLauncherHelper(t, marker)
	t.Setenv("TASKCTL_SOURCE_SELECTIONS", helperRecordCWDValue)

	// Deliberately close stdin so the next descriptor opened by runPlatform can
	// occupy fd 0. This covers the low-fd condition that made a /proc/self/fd/N
	// pre-exec chdir dependent on Go's descriptor-remapping order.
	savedStdin, err := unix.Dup(0)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.Close(0); err != nil {
		_ = unix.Close(savedStdin)
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := unix.Dup3(savedStdin, 0, 0); err != nil {
			t.Errorf("restore stdin: %v", err)
		}
		_ = unix.Close(savedStdin)
	})

	err = runPlatform(
		context.Background(),
		root,
		[]string{"generate", "source-audit"},
		expectedSHA256,
		nil,
		io.Discard,
		io.Discard,
		launcherHooks{},
	)
	if err != nil {
		t.Fatalf("run authenticated %s under low-fd pressure: %v", executable, err)
	}
	markerBytes, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(markerBytes), "generate\x00source-audit\n\n\nCWD="+root; got != want {
		t.Fatalf("helper cwd marker = %q, want %q", got, want)
	}
}

func TestRunPlatformCancellationTerminatesEntireProcessGroup(t *testing.T) {
	root, _, expectedSHA256 := newLauncherRepository(t)
	marker := filepath.Join(t.TempDir(), "child-pid")
	configureLauncherHelper(t, marker)
	t.Setenv("TASKCTL_SOURCE_SELECTIONS", helperSpawnChildValue)
	t.Setenv("TASKCTL_INPUT", helperIgnoreSignalsValue)
	var stderr bytes.Buffer
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runPlatform(
			ctx,
			root,
			[]string{"generate", "source-audit"},
			expectedSHA256,
			nil,
			&stderr,
			io.Discard,
			launcherHooks{cancellationGrace: 50 * time.Millisecond},
		)
	}()
	ready := marker + ".ready"
	waitForPath(t, ready, 5*time.Second)
	pidBytes, err := os.ReadFile(ready)
	if err != nil {
		t.Fatal(err)
	}
	childPID, err := strconv.Atoi(string(pidBytes))
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), context.Canceled.Error()) {
			t.Fatalf("cancellation error = %v; stderr=%q", err, stderr.String())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("launcher did not finish after cancellation")
	}
	waitForProcessExit(t, childPID, 5*time.Second)
}

func TestCancellationCleanupBoundsMissingWaitNotification(t *testing.T) {
	root := t.TempDir()
	rootFile, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer rootFile.Close()
	marker := filepath.Join(t.TempDir(), "taskctl-pid")
	command := blockingLauncherHelperCommand(marker)
	ctx, cancel := context.WithCancel(context.Background())
	releaseWait := make(chan struct{})
	defer close(releaseWait)
	done := make(chan error, 1)
	go func() {
		done <- runCommandInPinnedDirectory(
			ctx,
			command,
			rootFile,
			20*time.Millisecond,
			40*time.Millisecond,
			launcherHooks{waitCommand: func(command *exec.Cmd) error {
				err := command.Wait()
				<-releaseWait
				return err
			}},
		)
	}()
	waitForPath(t, marker, 5*time.Second)
	started := time.Now()
	cancel()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "timed out waiting for taskctl Wait") ||
			!strings.Contains(err.Error(), context.Canceled.Error()) {
			t.Fatalf("bounded cancellation error = %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation cleanup blocked past its second deadline")
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("cancellation cleanup took %s", elapsed)
	}
}

func TestStartFailureCleanupBoundsMissingWaitNotification(t *testing.T) {
	root := t.TempDir()
	rootFile, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer rootFile.Close()
	command := blockingLauncherHelperCommand(filepath.Join(t.TempDir(), "taskctl-pid"))
	releaseWait := make(chan struct{})
	defer close(releaseWait)
	want := errors.New("injected after-start failure")
	started := time.Now()
	done := make(chan error, 1)
	go func() {
		done <- runCommandInPinnedDirectory(
			context.Background(),
			command,
			rootFile,
			20*time.Millisecond,
			40*time.Millisecond,
			launcherHooks{
				afterCommandStart: func() error { return want },
				waitCommand: func(command *exec.Cmd) error {
					err := command.Wait()
					<-releaseWait
					return err
				},
			},
		)
	}()
	select {
	case err = <-done:
	case <-time.After(time.Second):
		t.Fatal("start-failure cleanup blocked past its second deadline")
	}
	if !errors.Is(err, want) || !strings.Contains(err.Error(), "timed out waiting for taskctl Wait") {
		t.Fatalf("bounded start-failure cleanup error = %v", err)
	}
	if elapsed := time.Since(started); elapsed >= time.Second {
		t.Fatalf("start-failure cleanup blocked for %s", elapsed)
	}
}

func TestStartFailureCleanupAcceptsWaitNotificationBeforeSecondDeadline(t *testing.T) {
	root := t.TempDir()
	rootFile, err := os.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	defer rootFile.Close()
	command := blockingLauncherHelperCommand(filepath.Join(t.TempDir(), "taskctl-pid"))
	want := errors.New("injected after-start failure")
	const waitDelay = 30 * time.Millisecond
	started := time.Now()
	err = runCommandInPinnedDirectory(
		context.Background(),
		command,
		rootFile,
		20*time.Millisecond,
		2*time.Second,
		launcherHooks{
			afterCommandStart: func() error { return want },
			waitCommand: func(command *exec.Cmd) error {
				err := command.Wait()
				time.Sleep(waitDelay)
				return err
			},
		},
	)
	if !errors.Is(err, want) || strings.Contains(err.Error(), "timed out waiting for taskctl Wait") {
		t.Fatalf("delayed start-failure cleanup error = %v", err)
	}
	if elapsed := time.Since(started); elapsed < waitDelay || elapsed >= time.Second {
		t.Fatalf("delayed Wait cleanup took %s, want [%s, %s)", elapsed, waitDelay, time.Second)
	}
}

func TestRunPlatformRejectsWrongDigestWithoutExecution(t *testing.T) {
	root, _, _ := newLauncherRepository(t)
	marker := filepath.Join(t.TempDir(), "marker")
	configureLauncherHelper(t, marker)

	err := runPlatform(
		context.Background(),
		root,
		[]string{"validate", "source-selections"},
		strings.Repeat("0", sha256.Size*2),
		nil,
		io.Discard,
		io.Discard,
		launcherHooks{},
	)
	if err == nil || !strings.Contains(err.Error(), "executable SHA-256") {
		t.Fatalf("wrong-digest error = %v", err)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("helper unexpectedly executed; marker stat error = %v", err)
	}
}

func TestRunPlatformExecutesPinnedFileNotPathReplacement(t *testing.T) {
	root, executable, expectedSHA256 := newLauncherRepository(t)
	marker := filepath.Join(t.TempDir(), "marker")
	configureLauncherHelper(t, marker)

	err := runPlatform(
		context.Background(),
		root,
		[]string{"generate", "source-audit"},
		expectedSHA256,
		nil,
		io.Discard,
		io.Discard,
		launcherHooks{beforeStart: func() error {
			if err := os.Rename(executable, executable+".trusted"); err != nil {
				return err
			}
			return copyExecutable("/usr/bin/touch", executable)
		}},
	)
	if err == nil || !strings.Contains(err.Error(), "post-execution authentication") {
		t.Fatalf("replacement error = %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("pinned trusted helper did not execute: %v", err)
	}
	for _, maliciousOutput := range []string{"generate", "source-audit"} {
		if _, err := os.Stat(filepath.Join(root, maliciousOutput)); !os.IsNotExist(err) {
			t.Fatalf("replacement executable ran and created %s; stat error = %v", maliciousOutput, err)
		}
	}
}

func TestRunPlatformExecutesSealedCopyNotInPlaceMutation(t *testing.T) {
	root, executable, expectedSHA256 := newLauncherRepository(t)
	marker := filepath.Join(t.TempDir(), "marker")
	configureLauncherHelper(t, marker)

	err := runPlatform(
		context.Background(),
		root,
		[]string{"generate", "source-audit"},
		expectedSHA256,
		nil,
		io.Discard,
		io.Discard,
		launcherHooks{beforeStart: func() error {
			return replaceExecutableContents("/usr/bin/touch", executable)
		}},
	)
	if err == nil || !strings.Contains(err.Error(), "post-execution authentication") {
		t.Fatalf("in-place mutation error = %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("sealed trusted helper did not execute: %v", err)
	}
	for _, maliciousOutput := range []string{"generate", "source-audit"} {
		if _, err := os.Stat(filepath.Join(root, maliciousOutput)); !os.IsNotExist(err) {
			t.Fatalf("mutated executable ran and created %s; stat error = %v", maliciousOutput, err)
		}
	}
}

func TestRunPlatformRejectsDynamicELFWithoutExecution(t *testing.T) {
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(root, "bin", "taskctl")
	if err := copyExecutable("/usr/bin/touch", executable); err != nil {
		t.Fatal(err)
	}
	err := runPlatform(
		context.Background(),
		root,
		[]string{"generate", "source-audit"},
		fileSHA256(t, executable),
		nil,
		io.Discard,
		io.Discard,
		launcherHooks{},
	)
	if err == nil || !strings.Contains(err.Error(), "program interpreter") {
		t.Fatalf("dynamic ELF error = %v", err)
	}
	for _, output := range []string{"generate", "source-audit"} {
		if _, err := os.Stat(filepath.Join(root, output)); !os.IsNotExist(err) {
			t.Fatalf("dynamic executable ran and created %s; stat error = %v", output, err)
		}
	}
}

func TestPrepareSealedExecutableRetainsRequiredSeals(t *testing.T) {
	_, executable, expectedSHA256 := newLauncherRepository(t)
	file, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	sealed, _, actual, err := prepareSealedExecutable(file, expectedSHA256)
	if err != nil {
		t.Fatal(err)
	}
	defer sealed.Close()
	if actual != expectedSHA256 {
		t.Fatalf("sealed digest = %s, want %s", actual, expectedSHA256)
	}
	required := unix.F_SEAL_WRITE |
		unix.F_SEAL_GROW |
		unix.F_SEAL_SHRINK |
		unix.F_SEAL_EXEC |
		unix.F_SEAL_SEAL
	seals, err := unix.FcntlInt(sealed.Fd(), unix.F_GET_SEALS, 0)
	if err != nil {
		t.Fatal(err)
	}
	if seals&required != required {
		t.Fatalf("sealed flags = %#x, want at least %#x", seals, required)
	}
	if _, err := sealed.WriteAt([]byte{0}, 0); err == nil {
		t.Fatal("sealed executable remained writable")
	}
	if err := sealed.Truncate(0); err == nil {
		t.Fatal("sealed executable remained truncatable")
	}
	if err := sealed.Chmod(0o600); err == nil {
		t.Fatal("sealed executable mode remained changeable")
	}
}

func TestRunPlatformRejectsSymlinkAndHardLink(t *testing.T) {
	source := launcherHelperExecutable
	expectedSHA256 := fileSHA256(t, source)

	t.Run("symlink", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, "bin"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(source, filepath.Join(root, "bin", "taskctl")); err != nil {
			t.Fatal(err)
		}
		err := runPlatform(context.Background(), root, []string{"generate", "source-audit"}, expectedSHA256, nil, io.Discard, io.Discard, launcherHooks{})
		if err == nil || !strings.Contains(err.Error(), "open fixed bin/taskctl without symbolic links") {
			t.Fatalf("symlink error = %v", err)
		}
	})

	t.Run("hard link", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, "bin"), 0o700); err != nil {
			t.Fatal(err)
		}
		original := filepath.Join(root, "original")
		if err := copyExecutable(source, original); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(original, filepath.Join(root, "bin", "taskctl")); err != nil {
			t.Fatal(err)
		}
		err := runPlatform(context.Background(), root, []string{"generate", "source-audit"}, fileSHA256(t, original), nil, io.Discard, io.Discard, launcherHooks{})
		if err == nil || !strings.Contains(err.Error(), "exactly one hard link") {
			t.Fatalf("hard-link error = %v", err)
		}
	})
}

func TestValidateArgumentsAllowsOnlyExactReviewedRoles(t *testing.T) {
	for role := range reviewedRoles {
		if err := validateArguments([]string{role[0], role[1]}); err != nil {
			t.Fatalf("reviewed role %q rejected: %v", role, err)
		}
	}
	for _, arguments := range [][]string{
		nil,
		{"generate"},
		{"generate", "checksums"},
		{"generate", "pointer-checksums"},
		{"validate", "checksums"},
		{"validate", "pointer-checksums"},
		{"generate", "checksums", "--root"},
		{"run", "checksums"},
		{"generate", "unknown"},
		{"Generate", "checksums"},
		{"install", "trusted-launcher"},
		{"install", "trusted-launcher", strings.Repeat("A", sha256.Size*2)},
	} {
		if err := validateArguments(arguments); err == nil {
			t.Fatalf("unreviewed arguments %q accepted", arguments)
		}
	}
	if err := validateArguments([]string{
		installLauncherRole[0],
		installLauncherRole[1],
		strings.Repeat("a", sha256.Size*2),
	}); err != nil {
		t.Fatalf("installation role rejected: %v", err)
	}
}

func TestRunRejectsDevelopmentOrMalformedReleaseRevision(t *testing.T) {
	for _, revision := range []string{
		"",
		"development",
		strings.Repeat("A", 40),
		strings.Repeat("a", 39),
		strings.Repeat("g", 40),
	} {
		trustChecks := 0
		err := run(
			context.Background(),
			[]string{"generate", "source-audit"},
			nil,
			io.Discard,
			io.Discard,
			func() error {
				trustChecks++
				return nil
			},
			revision,
		)
		if err == nil || !strings.Contains(err.Error(), "release revision") {
			t.Fatalf("revision %q error = %v", revision, err)
		}
		if trustChecks != 0 {
			t.Fatalf("revision %q reached installed launcher trust check", revision)
		}
	}
	if err := verifyReleaseRevision(strings.Repeat("a", 40)); err != nil {
		t.Fatalf("canonical release revision rejected: %v", err)
	}
}

func TestClosedChildEnvironmentContainsOnlyReviewedTaskctlData(t *testing.T) {
	want := make([]string, 0, len(reviewedChildEnvironmentNames))
	for index, name := range reviewedChildEnvironmentNames {
		value := fmt.Sprintf("value-%d", index)
		if index == len(reviewedChildEnvironmentNames)-1 {
			value = ""
		}
		t.Setenv(name, value)
		want = append(want, name+"="+value)
	}
	t.Setenv(executableSHA256Environment, strings.Repeat("a", sha256.Size*2))
	t.Setenv("TASKCTL_UNREVIEWED", "forbidden")
	t.Setenv("LD_PRELOAD", "/attacker/inject.so")
	t.Setenv("LD_LIBRARY_PATH", "/attacker")
	t.Setenv("PATH", "/attacker/bin")
	t.Setenv("HOME", "/attacker/home")

	got := closedChildEnvironment()
	if strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("closed child environment = %q, want %q", got, want)
	}
}

func TestInspectRoleAuthenticatesWithoutExecutingOrTrustingItsOwnOutput(t *testing.T) {
	root, _, expectedSHA256 := newLauncherRepository(t)
	t.Chdir(root)
	marker := filepath.Join(t.TempDir(), "marker")
	configureLauncherHelper(t, marker)
	t.Setenv(executableSHA256Environment, strings.Repeat("0", sha256.Size*2))
	var output bytes.Buffer
	trustChecks := 0
	err := run(
		context.Background(),
		[]string{inspectExecutableRole[0], inspectExecutableRole[1]},
		nil,
		&output,
		io.Discard,
		func() error {
			trustChecks++
			return nil
		},
		strings.Repeat("a", 40),
	)
	if err != nil {
		t.Fatal(err)
	}
	if trustChecks != 1 {
		t.Fatalf("launcher trust checks = %d, want 1", trustChecks)
	}
	if got, want := output.String(), expectedSHA256+"\n"; got != want {
		t.Fatalf("inspection output = %q, want %q", got, want)
	}
	if _, err := os.Stat(marker); !os.IsNotExist(err) {
		t.Fatalf("inspection executed taskctl; marker stat error = %v", err)
	}
}

func TestRunRejectsFailedInstalledLauncherAuthenticationFirst(t *testing.T) {
	want := errors.New("untrusted launcher")
	err := run(
		context.Background(),
		[]string{"generate", "source-audit"},
		nil,
		io.Discard,
		io.Discard,
		func() error { return want },
		strings.Repeat("a", 40),
	)
	if !errors.Is(err, want) || !strings.Contains(err.Error(), "authenticate installed launcher") {
		t.Fatalf("launcher trust error = %v", err)
	}
}

func TestVerifyTrustedLauncherInstallation(t *testing.T) {
	authority := t.TempDir()
	if err := os.Chmod(authority, 0o700); err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(authority, "libexec", "scopesifter")
	if err := os.MkdirAll(directory, 0o700); err != nil {
		t.Fatal(err)
	}
	source := launcherHelperExecutable
	launcher := filepath.Join(directory, "taskctl-launcher")
	if err := copyExecutable(source, launcher); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(launcher, installedLauncherMode); err != nil {
		t.Fatal(err)
	}
	uid := uint32(os.Geteuid())
	gid := uint32(os.Getegid())
	if err := verifyTrustedLauncherInstallation(authority, launcher, launcher, uid, gid); err != nil {
		t.Fatalf("trusted installation rejected: %v", err)
	}

	t.Run("different running inode", func(t *testing.T) {
		other := filepath.Join(directory, "other")
		if err := copyExecutable(source, other); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(other, installedLauncherMode); err != nil {
			t.Fatal(err)
		}
		err := verifyTrustedLauncherInstallation(authority, launcher, other, uid, gid)
		if err == nil || !strings.Contains(err.Error(), "running image is not") {
			t.Fatalf("different-running-inode error = %v", err)
		}
	})

	t.Run("wrong owner authority", func(t *testing.T) {
		err := verifyTrustedLauncherInstallation(authority, launcher, launcher, uid+1, gid)
		if err == nil || !strings.Contains(err.Error(), "owner") {
			t.Fatalf("wrong-owner error = %v", err)
		}
	})

	t.Run("wrong group authority", func(t *testing.T) {
		err := verifyTrustedLauncherInstallation(authority, launcher, launcher, uid, gid+1)
		if err == nil || !strings.Contains(err.Error(), "group") {
			t.Fatalf("wrong-group error = %v", err)
		}
	})

	for _, test := range []struct {
		name string
		mode uint32
	}{
		{name: "owner writable", mode: 0o755},
		{name: "not world executable", mode: 0o550},
		{name: "setuid", mode: unix.S_ISUID | installedLauncherMode},
		{name: "setgid", mode: unix.S_ISGID | installedLauncherMode},
		{name: "sticky", mode: unix.S_ISVTX | installedLauncherMode},
	} {
		t.Run(test.name, func(t *testing.T) {
			if err := unix.Chmod(launcher, test.mode); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() { _ = os.Chmod(launcher, installedLauncherMode) })
			err := verifyTrustedLauncherInstallation(authority, launcher, launcher, uid, gid)
			if err == nil || !strings.Contains(err.Error(), "mode") {
				t.Fatalf("unsafe-mode error = %v", err)
			}
		})
	}

	t.Run("writable parent", func(t *testing.T) {
		if err := os.Chmod(directory, 0o770); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(directory, 0o700) })
		err := verifyTrustedLauncherInstallation(authority, launcher, launcher, uid, gid)
		if err == nil || !strings.Contains(err.Error(), "writable by group or other") {
			t.Fatalf("writable-parent error = %v", err)
		}
	})

	t.Run("dynamic launcher", func(t *testing.T) {
		dynamic := filepath.Join(directory, "dynamic-launcher")
		if err := copyExecutable("/usr/bin/touch", dynamic); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(dynamic, installedLauncherMode); err != nil {
			t.Fatal(err)
		}
		err := verifyTrustedLauncherInstallation(authority, dynamic, dynamic, uid, gid)
		if err == nil || !strings.Contains(err.Error(), "program interpreter") {
			t.Fatalf("dynamic-launcher error = %v", err)
		}
	})
}

func TestVerifyExecutionIdentityRejectsPrivilegeTransitions(t *testing.T) {
	if os.Getuid() != os.Geteuid() || os.Getgid() != os.Getegid() {
		t.Skip("test process already has a privilege transition")
	}
	if err := verifyExecutionIdentity(false); err != nil {
		t.Fatalf("ordinary execution identity rejected: %v", err)
	}
	if os.Geteuid() != 0 {
		if err := verifyExecutionIdentity(true); err == nil ||
			!strings.Contains(err.Error(), "UID and GID 0") {
			t.Fatalf("non-root installation error = %v", err)
		}
	}
}

func TestVerifyInitialIdentityMappings(t *testing.T) {
	if err := verifyInitialIdentityMappings(); err != nil {
		t.Fatalf("initial identity mapping rejected: %v", err)
	}
}

func TestRunPlatformRejectsNonCanonicalRepositoryRoot(t *testing.T) {
	root, _, expectedSHA256 := newLauncherRepository(t)
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}
	err := runPlatform(context.Background(), alias, []string{"generate", "source-audit"}, expectedSHA256, nil, io.Discard, io.Discard, launcherHooks{})
	if err == nil || !strings.Contains(err.Error(), "symbolic-link component") {
		t.Fatalf("noncanonical-root error = %v", err)
	}
}

func TestRunPlatformChildUsesPinnedRepositoryDirectory(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "repository")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	source := launcherHelperExecutable
	executable := filepath.Join(root, "bin", "taskctl")
	if err := copyExecutable(source, executable); err != nil {
		t.Fatal(err)
	}
	expectedSHA256 := fileSHA256(t, executable)
	marker := filepath.Join(t.TempDir(), "marker")
	configureLauncherHelper(t, marker)
	t.Setenv("TASKCTL_SOURCE_SELECTIONS", helperRecordCWDValue)

	err := runPlatform(
		context.Background(),
		root,
		[]string{"generate", "source-audit"},
		expectedSHA256,
		nil,
		io.Discard,
		io.Discard,
		launcherHooks{beforeStart: func() error {
			if err := os.Rename(root, root+".pinned"); err != nil {
				return err
			}
			return os.Mkdir(root, 0o700)
		}},
	)
	if err == nil || !strings.Contains(err.Error(), "post-execution authentication") {
		t.Fatalf("repository replacement error = %v", err)
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatalf("helper did not run from pinned repository directory: %v", err)
	}
	markerBytes, err := os.ReadFile(marker)
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(markerBytes), "generate\x00source-audit\n\n\nCWD="+root+".pinned"; got != want {
		t.Fatalf("helper cwd marker = %q, want %q", got, want)
	}
}

func TestRunPlatformRejectsRepositoryPathThatDiffersFromPinnedCWD(t *testing.T) {
	root, _, expectedSHA256 := newLauncherRepository(t)
	other := t.TempDir()
	current, err := os.Open(other)
	if err != nil {
		t.Fatal(err)
	}
	defer current.Close()

	err = runPlatform(
		context.Background(),
		root,
		[]string{"generate", "source-audit"},
		expectedSHA256,
		nil,
		io.Discard,
		io.Discard,
		launcherHooks{expectedCWD: current},
	)
	if err == nil || !strings.Contains(err.Error(), "does not identify the launcher's working directory") {
		t.Fatalf("working-directory mismatch error = %v", err)
	}
}

func TestProvisionTrustedLauncherInstallsAndReplacesExactImage(t *testing.T) {
	authority := t.TempDir()
	if err := os.Chmod(authority, 0o755); err != nil {
		t.Fatal(err)
	}
	launcher := filepath.Join(authority, "usr", "local", "libexec", "scopesifter", "taskctl-launcher")
	digest := fileSHA256(t, launcherHelperExecutable)
	uid := uint32(os.Geteuid())
	gid := uint32(os.Getegid())

	oldUmask := unix.Umask(0o777)
	installErr := installTrustedLauncher(
		authority,
		launcher,
		launcherHelperExecutable,
		digest,
		uid,
		gid,
	)
	unix.Umask(oldUmask)
	if installErr != nil {
		t.Fatalf("install launcher under restrictive umask: %v", installErr)
	}
	assertInstalledLauncher(t, launcher, digest, uid, gid)

	if err := installTrustedLauncher(
		authority,
		launcher,
		launcherHelperExecutable,
		digest,
		uid,
		gid,
	); err != nil {
		t.Fatalf("reinstall launcher: %v", err)
	}
	assertInstalledLauncher(t, launcher, digest, uid, gid)
	matches, err := filepath.Glob(filepath.Join(filepath.Dir(launcher), ".taskctl-launcher.install-*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("temporary launcher files remain: %v", matches)
	}
}

func TestProvisionTrustedLauncherRejectsWrongDigestAndUnsafeDestination(t *testing.T) {
	t.Run("wrong source digest", func(t *testing.T) {
		authority := t.TempDir()
		launcher := filepath.Join(authority, "libexec", "scopesifter", "taskctl-launcher")
		err := installTrustedLauncher(
			authority,
			launcher,
			launcherHelperExecutable,
			strings.Repeat("0", sha256.Size*2),
			uint32(os.Geteuid()),
			uint32(os.Getegid()),
		)
		if err == nil || !strings.Contains(err.Error(), "SHA-256") {
			t.Fatalf("wrong-digest error = %v", err)
		}
		if _, err := os.Stat(launcher); !os.IsNotExist(err) {
			t.Fatalf("launcher unexpectedly installed: %v", err)
		}
	})

	t.Run("unsafe existing launcher", func(t *testing.T) {
		authority := t.TempDir()
		if err := os.Chmod(authority, 0o755); err != nil {
			t.Fatal(err)
		}
		directory := filepath.Join(authority, "libexec", "scopesifter")
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
		launcher := filepath.Join(directory, "taskctl-launcher")
		if err := copyExecutable(launcherHelperExecutable, launcher); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(launcher, 0o755); err != nil {
			t.Fatal(err)
		}
		err := installTrustedLauncher(
			authority,
			launcher,
			launcherHelperExecutable,
			fileSHA256(t, launcherHelperExecutable),
			uint32(os.Geteuid()),
			uint32(os.Getegid()),
		)
		if err == nil || !strings.Contains(err.Error(), "not safely replaceable") {
			t.Fatalf("unsafe-destination error = %v", err)
		}
		info, statErr := os.Stat(launcher)
		if statErr != nil {
			t.Fatal(statErr)
		}
		if info.Mode().Perm() != 0o755 {
			t.Fatalf("unsafe destination was modified to mode %o", info.Mode().Perm())
		}
	})

	t.Run("symlink parent", func(t *testing.T) {
		authority := t.TempDir()
		if err := os.Chmod(authority, 0o755); err != nil {
			t.Fatal(err)
		}
		outside := t.TempDir()
		if err := os.Symlink(outside, filepath.Join(authority, "libexec")); err != nil {
			t.Fatal(err)
		}
		launcher := filepath.Join(authority, "libexec", "scopesifter", "taskctl-launcher")
		err := installTrustedLauncher(
			authority,
			launcher,
			launcherHelperExecutable,
			fileSHA256(t, launcherHelperExecutable),
			uint32(os.Geteuid()),
			uint32(os.Getegid()),
		)
		if err == nil {
			t.Fatal("symlink parent was accepted")
		}
		if matches, globErr := filepath.Glob(filepath.Join(outside, "*")); globErr != nil || len(matches) != 0 {
			t.Fatalf("symlink target changed: matches=%v error=%v", matches, globErr)
		}
	})
}

func assertInstalledLauncher(
	t *testing.T,
	path, digest string,
	uid, gid uint32,
) {
	t.Helper()
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if err := verifyInstalledLauncherDescriptor(file, digest, uid, gid); err != nil {
		t.Fatal(err)
	}
}

func waitForPath(t *testing.T, path string, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			return
		} else if !os.IsNotExist(err) {
			t.Fatal(err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", path)
}

func waitForProcessExit(t *testing.T, pid int, timeout time.Duration) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		err := unix.Kill(pid, 0)
		if errors.Is(err, unix.ESRCH) {
			return
		}
		if err != nil && !errors.Is(err, unix.EPERM) {
			t.Fatal(err)
		}
		status, readErr := os.ReadFile(filepath.Join("/proc", strconv.Itoa(pid), "stat"))
		if readErr == nil {
			fields := strings.Fields(string(status))
			if len(fields) >= 3 && fields[2] == "Z" {
				return
			}
		} else if os.IsNotExist(readErr) {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("process %d survived launcher cancellation", pid)
}

func newLauncherRepository(t *testing.T) (string, string, string) {
	t.Helper()
	source := launcherHelperExecutable
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "bin"), 0o700); err != nil {
		t.Fatal(err)
	}
	executable := filepath.Join(root, "bin", "taskctl")
	if err := copyExecutable(source, executable); err != nil {
		t.Fatal(err)
	}
	return root, executable, fileSHA256(t, executable)
}

func configureLauncherHelper(t *testing.T, marker string) {
	t.Helper()
	t.Setenv("TASKCTL_REPOSITORY_BINDINGS", helperModeValue)
	t.Setenv("TASKCTL_OUTPUT", marker)
}

func blockingLauncherHelperCommand(marker string) *exec.Cmd {
	command := exec.Command(launcherHelperExecutable)
	command.Env = []string{
		"TASKCTL_REPOSITORY_BINDINGS=" + helperModeValue,
		"TASKCTL_OUTPUT=" + marker,
		"TASKCTL_INPUT=" + helperIgnoreSignalsValue,
		"TASKCTL_SOURCE_SELECTIONS=" + helperWaitForParentValue,
	}
	command.Stdin = nil
	command.Stdout = io.Discard
	command.Stderr = io.Discard
	command.SysProcAttr = &syscall.SysProcAttr{
		Setpgid:   true,
		Pdeathsig: syscall.SIGKILL,
	}
	return command
}

func copyExecutable(source, destination string) (resultErr error) {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, input.Close()) }()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, output.Close()) }()
	if _, err := io.Copy(output, input); err != nil {
		return err
	}
	return output.Sync()
}

func replaceExecutableContents(source, destination string) (resultErr error) {
	input, err := os.Open(source)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, input.Close()) }()
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_TRUNC, 0)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, output.Close()) }()
	if _, err := io.Copy(output, input); err != nil {
		return err
	}
	return output.Sync()
}

func fileSHA256(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return fmt.Sprintf("%x", sha256.Sum256(data))
}

func launcherHelperBuildEnvironment(environment []string, workRoot string) []string {
	clean := make([]string, 0, len(environment)+15)
	for _, entry := range environment {
		name, _, found := strings.Cut(entry, "=")
		if !found || strings.HasPrefix(name, "GO") || strings.HasPrefix(name, "CGO") {
			continue
		}
		switch name {
		case "AR", "CC", "CXX", "FC", "GCCGO", "PKG_CONFIG":
			continue
		}
		clean = append(clean, entry)
	}
	return append(clean,
		"CGO_ENABLED=0",
		"GO111MODULE=on",
		"GOARCH="+runtime.GOARCH,
		"GOAUTH=off",
		"GOCACHE="+filepath.Join(workRoot, "build-cache"),
		"GOENV=off",
		"GOFLAGS=-mod=readonly -trimpath -buildvcs=false",
		"GONOPROXY=none",
		"GONOSUMDB=",
		"GOOS="+runtime.GOOS,
		"GOPRIVATE=",
		"GOPROXY=off",
		"GOSUMDB=off",
		"GOTOOLCHAIN=local",
		"GOVCS=*:off",
		"GOWORK=off",
	)
}
