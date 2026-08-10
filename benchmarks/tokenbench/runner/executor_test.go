package runner

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/dkropachev/repo-view/benchmarks/tokenbench/harness"
	"golang.org/x/sys/unix"
)

func TestExecutorUsesExactEnvironmentAndStdin(t *testing.T) {
	executor, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := runPrepared(context.Background(), executor, helperRequest(t, "echo"))
	if err != nil {
		t.Fatal(err)
	}
	if raw.ExitCode != 0 || raw.TimedOut || raw.Cancelled {
		t.Fatalf("unexpected termination: %+v", raw)
	}
	if got, want := string(raw.Stdout), "value\nprompt bytes"; got != want {
		t.Fatalf("stdout %q, want %q", got, want)
	}
}

func TestExecutorBoundsOutputAndCancels(t *testing.T) {
	executor, err := New(Config{MaxStdoutBytes: 32})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := runPrepared(context.Background(), executor, helperRequest(t, "overflow"))
	if err != nil {
		t.Fatal(err)
	}
	if !raw.StdoutTruncated || raw.Cancelled || len(raw.Stdout) != 32 {
		t.Fatalf("output limit was not retained: %+v", raw)
	}
}

func TestExecutorRejectsLimitsOutsidePublicationSchema(t *testing.T) {
	tests := []struct {
		name   string
		config Config
	}{
		{"stdout", Config{MaxStdoutBytes: harness.MaxRawStreamBytes + 1}},
		{"stderr", Config{MaxStderrBytes: harness.MaxRawStreamBytes + 1}},
		{"artifacts", Config{MaxArtifactBytes: harness.MaxArtifactBytes + 1}},
		{"wait delay", Config{WaitDelay: maximumWaitDelay + time.Nanosecond}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := New(test.config); err == nil {
				t.Fatal("New() accepted a limit outside the publication schema")
			}
		})
	}
}

func TestExecutorTimeoutIsClassified(t *testing.T) {
	executor, err := New(Config{WaitDelay: 50 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	request := helperRequest(t, "wait")
	request.Process.TimeoutMillis = 25
	raw, err := runPrepared(context.Background(), executor, request)
	if err != nil {
		t.Fatal(err)
	}
	if !raw.TimedOut || raw.Cancelled {
		t.Fatalf("timeout was not classified: %+v", raw)
	}
}

type lifecycleFixture struct {
	before int
	after  int
}

func (*lifecycleFixture) Identity() string { return "test-lifecycle/v1" }

func (fixture *lifecycleFixture) BeginArm(
	context.Context,
	ExecutionRequest,
) (ArmSession, error) {
	fixture.before++
	return fixture, nil
}

func (fixture *lifecycleFixture) Finish(
	_ context.Context,
	_ ExecutionRequest,
	_ harness.RawExecution,
) ([]harness.Artifact, error) {
	fixture.after++
	return []harness.Artifact{{
		Name:      "fixture",
		MediaType: "application/octet-stream",
		Data:      []byte("captured"),
	}}, nil
}

func (*lifecycleFixture) Abort(context.Context) error { return nil }

type retryingAbortLifecycle struct {
	abortCalls int
}

func (*retryingAbortLifecycle) Identity() string { return "retrying-abort-lifecycle/v1" }

func (lifecycle *retryingAbortLifecycle) BeginArm(
	context.Context,
	ExecutionRequest,
) (ArmSession, error) {
	return lifecycle, nil
}

func (*retryingAbortLifecycle) Finish(
	context.Context,
	ExecutionRequest,
	harness.RawExecution,
) ([]harness.Artifact, error) {
	return []harness.Artifact{}, nil
}

func (lifecycle *retryingAbortLifecycle) Abort(context.Context) error {
	lifecycle.abortCalls++
	if lifecycle.abortCalls == 1 {
		return errors.New("transient lifecycle abort failure")
	}
	return nil
}

func TestPreparedAbortCallsLiveSessionAndCanRetry(t *testing.T) {
	lifecycle := &retryingAbortLifecycle{}
	executor, err := New(Config{Lifecycle: lifecycle})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := executor.Close(context.Background()); err != nil {
			t.Errorf("Executor.Close(): %v", err)
		}
	})
	prepared, err := executor.Prepare(context.Background(), helperRequest(t, "echo"))
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Abort(context.Background()); err == nil {
		t.Fatal("first Abort unexpectedly ignored lifecycle failure")
	}
	if lifecycle.abortCalls != 1 {
		t.Fatalf("first Abort calls = %d, want 1", lifecycle.abortCalls)
	}
	if err := prepared.Abort(context.Background()); err != nil {
		t.Fatalf("retry Abort: %v", err)
	}
	if lifecycle.abortCalls != 2 {
		t.Fatalf("retry Abort calls = %d, want 2", lifecycle.abortCalls)
	}
}

func TestExecutorLifecycleProducesBoundedArtifacts(t *testing.T) {
	lifecycle := &lifecycleFixture{}
	executor, err := New(Config{Lifecycle: lifecycle})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := runPrepared(context.Background(), executor, helperRequest(t, "echo"))
	if err != nil {
		t.Fatal(err)
	}
	if lifecycle.before != 1 || lifecycle.after != 1 ||
		len(raw.Artifacts) != 1 || string(raw.Artifacts[0].Data) != "captured" {
		t.Fatalf("unexpected lifecycle result: before=%d after=%d raw=%+v", lifecycle.before, lifecycle.after, raw)
	}
}

type invalidArtifactLifecycle struct{}

func (invalidArtifactLifecycle) Identity() string { return "invalid-artifact-lifecycle/v1" }

func (invalidArtifactLifecycle) BeginArm(
	context.Context,
	ExecutionRequest,
) (ArmSession, error) {
	return invalidArtifactSession{}, nil
}

type invalidArtifactSession struct{}

func (invalidArtifactSession) Finish(
	context.Context,
	ExecutionRequest,
	harness.RawExecution,
) ([]harness.Artifact, error) {
	return nil, nil
}

func (invalidArtifactSession) Abort(context.Context) error { return nil }

func TestExecutorRejectsLifecycleArtifactsBeforeReturningRaw(t *testing.T) {
	executor, err := New(Config{Lifecycle: invalidArtifactLifecycle{}})
	if err != nil {
		t.Fatal(err)
	}
	_, err = runPrepared(context.Background(), executor, helperRequest(t, "echo"))
	var integrity *IntegrityError
	if !errors.As(err, &integrity) || integrity.Stage != "validate arm artifacts" {
		t.Fatalf("Execute() error = %T %v, want artifact IntegrityError", err, err)
	}
}

type mutatingLifecycle struct{}

func (mutatingLifecycle) Identity() string { return "mutating-lifecycle/v1" }

func (mutatingLifecycle) BeginArm(
	_ context.Context,
	request ExecutionRequest,
) (ArmSession, error) {
	request.Process.Argv[0] = "/attacker"
	request.Process.Environment["TOKENBENCH_TEST_VALUE"] = "attacker"
	request.Process.Stdin[0] = 'X'
	if len(request.Invocation.Prompt) != 0 {
		request.Invocation.Prompt[0] = 'X'
	}
	return mutatingSession{}, nil
}

type mutatingSession struct{}

func (mutatingSession) Finish(
	_ context.Context,
	_ ExecutionRequest,
	raw harness.RawExecution,
) ([]harness.Artifact, error) {
	if len(raw.Stdout) != 0 {
		raw.Stdout[0] = 'X'
	}
	return []harness.Artifact{}, nil
}

func (mutatingSession) Abort(context.Context) error { return nil }

func TestLifecycleCannotMutateLaunchedInputsOrCapture(t *testing.T) {
	executor, err := New(Config{Lifecycle: mutatingLifecycle{}})
	if err != nil {
		t.Fatal(err)
	}
	raw, err := runPrepared(context.Background(), executor, helperRequest(t, "echo"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := string(raw.Stdout), "value\nprompt bytes"; got != want {
		t.Fatalf("lifecycle mutated execution: got %q, want %q", got, want)
	}
}

func TestRunnerOwnsIntegrityErrorClassification(t *testing.T) {
	executor, err := New(Config{})
	if err != nil {
		t.Fatal(err)
	}
	request := helperRequest(t, "echo")
	request.Invocation.ExecutableSHA256 = strings.Repeat("0", sha256.Size*2)
	prepared, err := executor.Prepare(context.Background(), request)
	if prepared == nil {
		t.Fatal("Prepare() omitted its cleanup capability")
	}
	t.Cleanup(func() { _ = prepared.Abort(context.Background()) })
	var integrity *IntegrityError
	if !errors.As(err, &integrity) || integrity.Stage != "pin harness executable" {
		t.Fatalf("Prepare() error = %T %v, want runner IntegrityError", err, err)
	}
}

func TestRawCaptureValidationPrecedesLifecyclePublication(t *testing.T) {
	tests := []struct {
		name string
		raw  harness.RawExecution
	}{
		{
			name: "stdout bound",
			raw: harness.RawExecution{
				Stdout:    make([]byte, harness.MaxRawStreamBytes+1),
				Artifacts: []harness.Artifact{},
			},
		},
		{
			name: "nil artifacts",
			raw:  harness.RawExecution{},
		},
		{
			name: "conflicting termination",
			raw: harness.RawExecution{
				Artifacts:       []harness.Artifact{},
				Cancelled:       true,
				StdoutTruncated: true,
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if err := validateRawCapture(test.raw); err == nil {
				t.Fatal("validateRawCapture() accepted an invalid raw capture")
			}
		})
	}
}

func runPrepared(
	ctx context.Context,
	executor *Executor,
	request ExecutionRequest,
) (harness.RawExecution, error) {
	prepared, err := executor.Prepare(ctx, request)
	if err != nil {
		return harness.RawExecution{}, err
	}
	return prepared.Execute(ctx)
}

func helperRequest(t *testing.T, operation string) ExecutionRequest {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(executable)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		t.Fatal(err)
	}
	digest := hex.EncodeToString(hash.Sum(nil))
	directory := t.TempDir()
	return ExecutionRequest{
		Arm: BaselineArm,
		Invocation: harness.Invocation{
			Executable:       executable,
			ExecutableSHA256: digest,
		},
		Process: harness.ProcessSpec{
			Environment: map[string]string{
				"TOKENBENCH_HELPER":     "1",
				"TOKENBENCH_OPERATION":  operation,
				"TOKENBENCH_TEST_VALUE": "value",
			},
			Directory: directory,
			Argv: []string{
				executable,
				"-test.run=^TestRunnerHelperProcess$",
			},
			Stdin:         []byte("prompt bytes"),
			TimeoutMillis: 5_000,
		},
	}
}

func TestRunnerHelperProcess(t *testing.T) {
	if os.Getenv("TOKENBENCH_HELPER") != "1" {
		return
	}
	switch os.Getenv("TOKENBENCH_OPERATION") {
	case "echo":
		content, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprint(os.Stderr, err)
			os.Exit(3)
		}
		fmt.Printf("%s\n%s", os.Getenv("TOKENBENCH_TEST_VALUE"), content)
	case "overflow":
		fmt.Print(strings.Repeat("x", 1<<20))
	case "wait":
		time.Sleep(5 * time.Second)
	case "cgroup":
		content, err := os.ReadFile("/proc/self/cgroup")
		if err != nil {
			fmt.Fprint(os.Stderr, err)
			os.Exit(3)
		}
		fmt.Print(string(content))
	case "spawn-escape":
		spawnEscapedHelper()
	case "spawn-escape-wait":
		spawnEscapedHelper()
		time.Sleep(5 * time.Minute)
	case "escaped-child":
		for {
			time.Sleep(time.Minute)
		}
	case "escape-cgroup":
		membership, err := os.ReadFile("/proc/self/cgroup")
		if err != nil {
			fmt.Fprint(os.Stderr, err)
			os.Exit(3)
		}
		relative := strings.TrimSpace(strings.TrimPrefix(string(membership), "0::"))
		hostProcesses := filepath.Join(
			cgroupMountPath,
			filepath.Dir(strings.TrimPrefix(relative, "/")),
			hostCgroupName,
			"cgroup.procs",
		)
		err = os.WriteFile(hostProcesses, []byte(strconv.Itoa(os.Getpid())+"\n"), 0)
		if err == nil {
			fmt.Print("escaped")
		} else {
			fmt.Printf("denied:%v", err)
		}
	case "write-file":
		if err := os.WriteFile(os.Getenv("TOKENBENCH_WRITE_PATH"), []byte("allowed"), 0o600); err != nil {
			fmt.Fprint(os.Stderr, err)
			os.Exit(3)
		}
	case "fd-state":
		entries, err := os.ReadDir("/proc/self/fd")
		if err != nil {
			fmt.Fprint(os.Stderr, err)
			os.Exit(3)
		}
		for _, entry := range entries {
			target, _ := os.Readlink(filepath.Join("/proc/self/fd", entry.Name()))
			fmt.Printf("%s=%s\n", entry.Name(), target)
		}
	case "fd5-state":
		target, err := os.Readlink("/proc/self/fd/5")
		if err != nil {
			fmt.Fprint(os.Stderr, err)
			os.Exit(3)
		}
		flags, flagErr := unix.FcntlInt(5, unix.F_GETFL, 0)
		seals, sealErr := unix.FcntlInt(5, unix.F_GET_SEALS, 0)
		_, writeErr := unix.Write(5, []byte("forbidden"))
		fmt.Printf(
			"target=%s flags=%d flag_err=%v seals=%d seal_err=%v write_err=%v",
			target,
			flags&unix.O_ACCMODE,
			flagErr,
			seals,
			sealErr,
			writeErr,
		)
	case "security-state":
		dumpable, err := unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0)
		if err != nil {
			fmt.Fprint(os.Stderr, err)
			os.Exit(3)
		}
		status, err := os.ReadFile("/proc/self/status")
		if err != nil {
			fmt.Fprint(os.Stderr, err)
			os.Exit(3)
		}
		fmt.Printf("dumpable=%d\n%s", dumpable, status)
	case "full-policy":
		allowed, allowedErr := os.ReadFile(os.Getenv("TOKENBENCH_ALLOWED_READ"))
		_, deniedReadErr := os.ReadFile("/etc/passwd")
		_, procRootErr := os.ReadFile("/proc/self/root/etc/passwd")
		_, curlErr := exec.Command("/usr/bin/curl", "--version").Output()
		loaderErr := exec.Command(
			os.Getenv("TOKENBENCH_LOADER"),
			"/usr/bin/curl",
			"--version",
		).Run()
		fmt.Printf(
			"allowed=%s allowed_err=%v denied_read=%v proc_root=%v curl=%v loader=%v",
			allowed,
			allowedErr,
			deniedReadErr,
			procRootErr,
			curlErr,
			loaderErr,
		)
	case "network-policy":
		allowedAddress := net.JoinHostPort("127.0.0.1", os.Getenv("TOKENBENCH_ALLOWED_PORT"))
		allowedConnection, allowedErr := net.DialTimeout("tcp4", allowedAddress, time.Second)
		if allowedConnection != nil {
			_ = allowedConnection.Close()
		}
		allowedPort, _ := strconv.Atoi(os.Getenv("TOKENBENCH_ALLOWED_PORT"))
		deniedAddress := net.JoinHostPort("127.0.0.1", strconv.Itoa(allowedPort%65535+1))
		deniedConnection, deniedErr := net.DialTimeout("tcp4", deniedAddress, time.Second)
		if deniedConnection != nil {
			_ = deniedConnection.Close()
		}
		listener, bindErr := net.Listen("tcp4", "127.0.0.1:0")
		if listener != nil {
			_ = listener.Close()
		}
		unixSocket, unixErr := net.Listen("unix", filepath.Join(os.TempDir(), "tokenbench-denied.sock"))
		if unixSocket != nil {
			_ = unixSocket.Close()
		}
		fmt.Printf(
			"allowed=%v denied=%v bind=%v unix=%v",
			allowedErr,
			deniedErr,
			bindErr,
			unixErr,
		)
	default:
		os.Exit(4)
	}
	os.Exit(0)
}

func spawnEscapedHelper() {
	executable, err := os.Executable()
	if err != nil {
		fmt.Fprint(os.Stderr, err)
		os.Exit(3)
	}
	command := exec.Command(executable, "-test.run=^TestRunnerHelperProcess$")
	command.Env = make([]string, 0, len(os.Environ())+1)
	for _, value := range os.Environ() {
		if !strings.HasPrefix(value, "TOKENBENCH_OPERATION=") {
			command.Env = append(command.Env, value)
		}
	}
	command.Env = append(command.Env, "TOKENBENCH_OPERATION=escaped-child")
	command.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := command.Start(); err != nil {
		fmt.Fprint(os.Stderr, err)
		os.Exit(3)
	}
	fmt.Println(command.Process.Pid)
	if err := command.Process.Release(); err != nil {
		fmt.Fprint(os.Stderr, err)
		os.Exit(3)
	}
}

func escapedPID(raw harness.RawExecution) (int, error) {
	value := strings.TrimSpace(string(raw.Stdout))
	pid, err := strconv.Atoi(value)
	if err != nil || pid <= 0 || strconv.Itoa(pid) != value {
		return 0, fmt.Errorf("invalid escaped PID output %q", raw.Stdout)
	}
	return pid, nil
}
