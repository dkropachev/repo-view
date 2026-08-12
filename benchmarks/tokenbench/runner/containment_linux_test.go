//go:build linux

package runner

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yapless/scopesifter/benchmarks/tokenbench/harness"
	"golang.org/x/sys/unix"
)

func TestCgroupManagerAppliesExactArmLimitsAndReusesStablePath(t *testing.T) {
	manager, err := discoverCgroupManager(time.Second, false)
	if err != nil {
		privilegedTestUnavailable(t, "delegated cgroup-v2 fixture unavailable: %v", err)
	}
	t.Cleanup(func() {
		if err := manager.close(); err != nil {
			t.Errorf("close cgroup manager: %v", err)
		}
	})
	var firstPath string
	for iteration := range 2 {
		arm, err := manager.newArm()
		if err != nil {
			t.Fatalf("newArm(%d): %v", iteration, err)
		}
		if arm.name != pairCgroupName {
			t.Fatalf("arm path = %q, want stable pair lease %q", arm.name, pairCgroupName)
		}
		armRoot, err := manager.root.OpenRoot(arm.name)
		if err != nil {
			t.Fatal(err)
		}
		assertCgroupValue(t, armRoot, "pids.max", strconv.FormatUint(armMaximumPIDs, 10)+"\n")
		assertCgroupValue(t, armRoot, "memory.max", strconv.FormatUint(armMaximumMemory, 10)+"\n")
		assertCgroupValue(t, armRoot, "memory.oom.group", "1\n")
		assertCgroupValue(t, armRoot, "cgroup.max.depth", "0\n")
		assertCgroupValue(t, armRoot, "cgroup.max.descendants", "0\n")
		if err := armRoot.Close(); err != nil {
			t.Fatal(err)
		}
		path := filepath.Clean(manager.pairPath)
		if iteration == 0 {
			firstPath = path
		} else if path != firstPath {
			t.Fatalf("model-visible arm cgroup path drifted: %q != %q", path, firstPath)
		}
		if err := arm.killAndRemove(time.Second); err != nil {
			t.Fatalf("killAndRemove(%d): %v", iteration, err)
		}
	}
}

func TestExecutorClosePreventsPrepareAndRemovesPairCgroup(t *testing.T) {
	executor, err := New(Config{allowUnboundedContainment: true})
	if err != nil {
		privilegedTestUnavailable(t, "delegated cgroup-v2 fixture unavailable: %v", err)
	}
	pairPath := executor.containment.pairPath
	prepared, err := executor.Prepare(context.Background(), helperRequest(t, "wait"))
	if err != nil {
		t.Fatal(err)
	}
	if err := prepared.Abort(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := executor.Close(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(pairPath); !os.IsNotExist(err) {
		t.Fatalf("pair cgroup remains after Close: %v", err)
	}
	if _, err := executor.Prepare(context.Background(), helperRequest(t, "echo")); err == nil {
		t.Fatal("Prepare succeeded after Executor.Close")
	}
	if err := executor.Close(context.Background()); err != nil {
		t.Fatalf("second Close was not idempotent: %v", err)
	}
}

func TestCgroupManagerCloseRetriesTransientHostRemoval(t *testing.T) {
	manager, err := discoverCgroupManager(time.Second, false)
	if err != nil {
		privilegedTestUnavailable(t, "delegated cgroup-v2 fixture unavailable: %v", err)
	}
	t.Cleanup(func() {
		manager.removeHost = manager.root.Remove
		if err := manager.close(); err != nil {
			t.Errorf("close cgroup manager: %v", err)
		}
	})
	realRemove := manager.removeHost
	calls := 0
	manager.removeHost = func(name string) error {
		calls++
		if calls == 1 {
			return unix.EBUSY
		}
		return realRemove(name)
	}
	if err := manager.close(); !errors.Is(err, unix.EBUSY) {
		t.Fatalf("first close error = %v, want EBUSY", err)
	}
	if manager.closed || !manager.hostCreated || manager.lease == nil {
		t.Fatalf(
			"failed Close released ownership: closed=%v host_created=%v lease_nil=%v",
			manager.closed,
			manager.hostCreated,
			manager.lease == nil,
		)
	}
	if err := manager.close(); err != nil {
		t.Fatalf("retry close: %v", err)
	}
	if calls != 2 || !manager.closed || manager.hostCreated || manager.lease != nil {
		t.Fatalf(
			"retry Close state: calls=%d closed=%v host_created=%v lease_nil=%v",
			calls,
			manager.closed,
			manager.hostCreated,
			manager.lease == nil,
		)
	}
}

func TestArmCleanupRetriesTransientRemovalWithoutLosingInodePin(t *testing.T) {
	manager, err := discoverCgroupManager(time.Second, false)
	if err != nil {
		privilegedTestUnavailable(t, "delegated cgroup-v2 fixture unavailable: %v", err)
	}
	t.Cleanup(func() {
		manager.removeArm = manager.root.Remove
		if err := manager.close(); err != nil {
			t.Errorf("close cgroup manager: %v", err)
		}
	})
	arm, err := manager.newArm()
	if err != nil {
		t.Fatal(err)
	}
	manager.removeArm = func(string) error { return unix.EBUSY }
	if err := arm.killAndRemove(time.Nanosecond); !errors.Is(err, unix.EBUSY) {
		t.Fatalf("first killAndRemove error = %v, want EBUSY", err)
	}
	arm.mu.Lock()
	pinRetained := arm.directory != nil
	cleaned := arm.cleaned
	arm.mu.Unlock()
	manager.mu.Lock()
	_, active := manager.active[arm.name]
	manager.mu.Unlock()
	if !pinRetained || cleaned || !active {
		t.Fatalf(
			"failed cleanup lost retry state: pin=%v cleaned=%v active=%v",
			pinRetained,
			cleaned,
			active,
		)
	}
	manager.removeArm = manager.root.Remove
	if err := arm.killAndRemove(time.Second); err != nil {
		t.Fatalf("retry killAndRemove: %v", err)
	}
	if err := manager.close(); err != nil {
		t.Fatalf("close after cleanup retry: %v", err)
	}
}

func TestContainedArmsObserveIdenticalCgroupPath(t *testing.T) {
	executor := newContainedTestExecutor(t, nil)
	first, err := runPrepared(context.Background(), executor, helperRequest(t, "cgroup"))
	if err != nil {
		t.Fatal(err)
	}
	second, err := runPrepared(context.Background(), executor, helperRequest(t, "cgroup"))
	if err != nil {
		t.Fatal(err)
	}
	if string(first.Stdout) != string(second.Stdout) {
		t.Fatalf("arm-visible cgroup paths differ: %q != %q", first.Stdout, second.Stdout)
	}
	if !strings.HasSuffix(string(first.Stdout), "/"+pairCgroupName+"\n") {
		t.Fatalf("arm did not observe stable pair cgroup: %q", first.Stdout)
	}
	for index, raw := range []harness.RawExecution{first, second} {
		if err := harness.ValidateResourceOutcome(raw.Resources); err != nil {
			t.Fatalf("arm %d resource outcome: %v", index, err)
		}
		usage, ok := harness.ResourceCounterValue(raw.Resources.CPUStat, "usage_usec")
		if !ok || usage == 0 || raw.Resources.PIDsPeak == 0 || raw.Resources.PIDsCurrent != 0 {
			t.Fatalf("arm %d resource accounting is incomplete: %+v", index, raw.Resources)
		}
	}
}

func TestCgroupCleanupKillsSetsidDescendant(t *testing.T) {
	executor := newContainedTestExecutor(t, nil)
	raw, err := runPrepared(context.Background(), executor, helperRequest(t, "spawn-escape"))
	if err != nil {
		t.Fatal(err)
	}
	pid, err := escapedPID(raw)
	if err != nil {
		t.Fatal(err)
	}
	assertProcessGone(t, pid)
}

func TestCgroupCleanupKillsDescendantOnTimeoutAndCancellation(t *testing.T) {
	tests := []struct {
		name string
		run  func(*testing.T, *Executor) harness.RawExecution
	}{
		{
			name: "timeout",
			run: func(t *testing.T, executor *Executor) harness.RawExecution {
				t.Helper()
				request := helperRequest(t, "spawn-escape-wait")
				request.Process.TimeoutMillis = 100
				raw, err := runPrepared(context.Background(), executor, request)
				if err != nil {
					t.Fatal(err)
				}
				if !raw.TimedOut {
					t.Fatalf("execution was not classified as timed out: %+v", raw)
				}
				return raw
			},
		},
		{
			name: "cancellation",
			run: func(t *testing.T, executor *Executor) harness.RawExecution {
				t.Helper()
				ctx, cancel := context.WithCancel(context.Background())
				time.AfterFunc(100*time.Millisecond, cancel)
				raw, err := runPrepared(ctx, executor, helperRequest(t, "spawn-escape-wait"))
				if err != nil {
					t.Fatal(err)
				}
				if !raw.Cancelled {
					t.Fatalf("execution was not classified as cancelled: %+v", raw)
				}
				return raw
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			executor := newContainedTestExecutor(t, nil)
			raw := test.run(t, executor)
			pid, err := escapedPID(raw)
			if err != nil {
				t.Fatal(err)
			}
			assertProcessGone(t, pid)
		})
	}
}

type cleanupOrderingLifecycle struct {
	finishObservedEmpty bool
}

func (*cleanupOrderingLifecycle) Identity() string { return "cleanup-ordering-lifecycle/v1" }

func (lifecycle *cleanupOrderingLifecycle) BeginArm(
	context.Context,
	ExecutionRequest,
) (ArmSession, error) {
	return &cleanupOrderingSession{lifecycle: lifecycle}, nil
}

type cleanupOrderingSession struct {
	lifecycle *cleanupOrderingLifecycle
}

func (session *cleanupOrderingSession) Finish(
	_ context.Context,
	_ ExecutionRequest,
	raw harness.RawExecution,
) ([]harness.Artifact, error) {
	pid, err := escapedPID(raw)
	if err != nil {
		return nil, err
	}
	if processExists(pid) {
		return nil, errors.New("lifecycle Finish ran before descendant cleanup")
	}
	session.lifecycle.finishObservedEmpty = true
	return []harness.Artifact{}, nil
}

func (*cleanupOrderingSession) Abort(context.Context) error { return nil }

func TestCgroupCleanupPrecedesLifecycleFinish(t *testing.T) {
	lifecycle := &cleanupOrderingLifecycle{}
	executor := newContainedTestExecutor(t, lifecycle)
	if _, err := runPrepared(context.Background(), executor, helperRequest(t, "spawn-escape")); err != nil {
		t.Fatal(err)
	}
	if !lifecycle.finishObservedEmpty {
		t.Fatal("lifecycle did not observe an empty descendant set")
	}
}

func TestLandlockBlocksCgroupEscapeAndAllowsOnlyPinnedWritableRoots(t *testing.T) {
	writable := t.TempDir()
	executor := newContainedTestExecutorConfig(t, Config{WritablePaths: []string{writable}})
	raw, err := runPrepared(context.Background(), executor, helperRequest(t, "escape-cgroup"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(raw.Stdout), "denied:") ||
		!strings.Contains(string(raw.Stdout), "permission denied") {
		t.Fatalf("Landlock did not deny cgroup escape: stdout=%q stderr=%q", raw.Stdout, raw.Stderr)
	}
	request := helperRequest(t, "write-file")
	target := filepath.Join(writable, "created")
	request.Process.Environment["TOKENBENCH_WRITE_PATH"] = target
	if raw, err := runPrepared(context.Background(), executor, request); err != nil || raw.ExitCode != 0 {
		t.Fatalf("write in pinned root failed: raw=%+v err=%v", raw, err)
	}
	if content, err := os.ReadFile(target); err != nil || string(content) != "allowed" {
		t.Fatalf("allowed write content=%q err=%v", content, err)
	}
}

func TestLandlockFullPolicyDeniesHostReadsExecutablesAndLoaderBypass(t *testing.T) {
	request := helperRequest(t, "full-policy")
	allowedPath := filepath.Join(request.Process.Directory, "allowed.txt")
	if err := os.WriteFile(allowedPath, []byte("approved"), 0o600); err != nil {
		t.Fatal(err)
	}
	config, loader := fullPolicyTestConfig(t, request, nil)
	request.Process.Environment["TOKENBENCH_ALLOWED_READ"] = allowedPath
	request.Process.Environment["TOKENBENCH_LOADER"] = loader
	executor := newContainedTestExecutorConfig(t, config)
	raw, err := runPrepared(context.Background(), executor, request)
	if err != nil {
		t.Fatal(err)
	}
	output := string(raw.Stdout)
	if !strings.Contains(output, "allowed=approved allowed_err=<nil>") ||
		!strings.Contains(output, "/etc/passwd: permission denied") ||
		!strings.Contains(output, "/proc/self/root/etc/passwd: permission denied") ||
		!strings.Contains(output, "curl=fork/exec /usr/bin/curl: permission denied") ||
		!strings.Contains(output, "loader=exit status") {
		t.Fatalf("full Landlock policy was bypassed: stdout=%q stderr=%q", raw.Stdout, raw.Stderr)
	}
}

type networkPolicyLifecycle struct {
	listener net.Listener
	port     uint16
}

func (*networkPolicyLifecycle) Identity() string { return "network-policy-lifecycle/v1" }

func (lifecycle *networkPolicyLifecycle) BeginArm(
	context.Context,
	ExecutionRequest,
) (ArmSession, error) {
	if lifecycle.listener == nil || lifecycle.port == 0 {
		return nil, errors.New("test proxy listener is closed")
	}
	return lifecycle, nil
}

func (*networkPolicyLifecycle) Finish(
	context.Context,
	ExecutionRequest,
	harness.RawExecution,
) ([]harness.Artifact, error) {
	return []harness.Artifact{}, nil
}

func (lifecycle *networkPolicyLifecycle) Abort(context.Context) error {
	if lifecycle.listener == nil {
		return nil
	}
	err := lifecycle.listener.Close()
	lifecycle.listener = nil
	return err
}

func (lifecycle *networkPolicyLifecycle) AllowedConnectTCPPorts() []uint16 {
	return []uint16{lifecycle.port}
}

func (*networkPolicyLifecycle) AllowedBindTCPPorts() []uint16 { return []uint16{} }

func TestLandlockAllowsOnlyActiveProxyTCPPortAndDeniesBindAndUnix(t *testing.T) {
	request := helperRequest(t, "network-policy")
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	if port <= 0 || port > 65535 {
		_ = listener.Close()
		t.Fatal("invalid test proxy port")
	}
	lifecycle := &networkPolicyLifecycle{listener: listener, port: uint16(port)}
	config, _ := fullPolicyTestConfig(t, request, lifecycle)
	request.Process.Environment["TOKENBENCH_ALLOWED_PORT"] = strconv.Itoa(port)
	executor := newContainedTestExecutorConfig(t, config)
	prepared, err := executor.Prepare(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	raw, err := prepared.Execute(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	output := string(raw.Stdout)
	if !strings.Contains(output, "allowed=<nil>") ||
		!strings.Contains(output, "denied=dial tcp4") ||
		!strings.Contains(output, "operation not permitted") ||
		!strings.Contains(output, "bind=listen tcp4") ||
		!strings.Contains(output, "unix=listen unix") {
		t.Fatalf("network policy result: stdout=%q stderr=%q", raw.Stdout, raw.Stderr)
	}
}

func fullPolicyTestConfig(
	t *testing.T,
	request ExecutionRequest,
	lifecycle Lifecycle,
) (Config, string) {
	t.Helper()
	loader, err := filepath.EvalSymlinks("/lib64/ld-linux-x86-64.so.2")
	if err != nil {
		privilegedTestUnavailable(t, "dynamic loader fixture unavailable: %v", err)
	}
	libc, err := filepath.EvalSymlinks("/usr/lib/x86_64-linux-gnu/libc.so.6")
	if err != nil {
		privilegedTestUnavailable(t, "libc fixture unavailable: %v", err)
	}
	readOnly := []string{request.Process.Directory, libc}
	if _, err := os.Stat("/etc/ld.so.cache"); err == nil {
		readOnly = append(readOnly, "/etc/ld.so.cache")
	}
	return Config{
		Lifecycle:       lifecycle,
		ReadOnlyPaths:   readOnly,
		ExecutablePaths: []string{loader},
	}, loader
}

func TestArmInitFDLayoutClosesPolicyAndCgroupDescriptors(t *testing.T) {
	for _, count := range []int{0, 1, 4} {
		t.Run(strconv.Itoa(count), func(t *testing.T) {
			paths := make([]string, count)
			for index := range paths {
				paths[index] = t.TempDir()
			}
			executor := newContainedTestExecutorConfig(t, Config{WritablePaths: paths})
			raw, err := runPrepared(context.Background(), executor, helperRequest(t, "fd-state"))
			if err != nil {
				t.Fatal(err)
			}
			for _, path := range paths {
				if strings.Contains(string(raw.Stdout), path) {
					t.Fatalf("target inherited writable policy FD for %q: %s", path, raw.Stdout)
				}
			}
			if strings.Contains(string(raw.Stdout), "/cgroup.procs") ||
				strings.Contains(string(raw.Stdout), "/"+pairCgroupName+"=") {
				t.Fatalf("target retained a writable cgroup FD: %s", raw.Stdout)
			}
		})
	}
}

func TestCommonFD5IsReadOnlySealedExecutableImage(t *testing.T) {
	request := helperRequest(t, "fd5-state")
	content, err := os.ReadFile(request.Invocation.Executable)
	if err != nil {
		t.Fatal(err)
	}
	common := filepath.Join(t.TempDir(), "scopesifter")
	if err := os.WriteFile(common, content, 0o555); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(common, 0o555); err != nil {
		t.Fatal(err)
	}
	executor := newContainedTestExecutorConfig(t, Config{
		CommonMCPExecutable:       common,
		CommonMCPExecutableSHA256: request.Invocation.ExecutableSHA256,
	})
	raw, err := runPrepared(context.Background(), executor, request)
	if err != nil {
		t.Fatal(err)
	}
	stdout := string(raw.Stdout)
	fields := strings.Fields(stdout)
	seals := 0
	for _, field := range fields {
		if value, ok := strings.CutPrefix(field, "seals="); ok {
			seals, _ = strconv.Atoi(value)
		}
	}
	if !strings.Contains(stdout, "target=/memfd:tokenbench-executable-v1 (deleted)") ||
		!strings.Contains(stdout, "flags=0") || seals&executableSealMask != executableSealMask ||
		strings.Contains(stdout, "write_err=<nil>") {
		t.Fatalf("FD5 is not the expected immutable read-only image: %s", stdout)
	}
}

func TestArmInitTargetSecurityStateIsHonestAndFiltered(t *testing.T) {
	executor := newContainedTestExecutor(t, nil)
	raw, err := runPrepared(context.Background(), executor, helperRequest(t, "security-state"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw.Stdout), "NoNewPrivs:\t1\n") {
		t.Fatalf("target did not inherit no_new_privs: %s", raw.Stdout)
	}
	if !strings.Contains(string(raw.Stdout), "dumpable=1\n") {
		t.Fatalf("target dumpability was not recorded honestly: %s", raw.Stdout)
	}
	if !strings.Contains(string(raw.Stdout), "Seccomp:\t2\n") {
		t.Fatalf("target did not inherit seccomp filter mode: %s", raw.Stdout)
	}
	for _, field := range []string{"CapInh", "CapPrm", "CapEff", "CapBnd", "CapAmb"} {
		if !strings.Contains(string(raw.Stdout), field+":\t0000000000000000\n") {
			t.Fatalf("target retained Linux capabilities in %s: %s", field, raw.Stdout)
		}
	}
}

func newContainedTestExecutor(t *testing.T, lifecycle Lifecycle) *Executor {
	t.Helper()
	return newContainedTestExecutorConfig(t, Config{Lifecycle: lifecycle})
}

func newContainedTestExecutorConfig(t *testing.T, config Config) *Executor {
	t.Helper()
	config.WaitDelay = 25 * time.Millisecond
	config.allowUnboundedContainment = true
	executor, err := New(config)
	if err != nil {
		privilegedTestUnavailable(
			t,
			"exclusive delegated cgroup-v2/Landlock fixture unavailable: %v",
			err,
		)
	}
	t.Cleanup(func() {
		if err := executor.Close(context.Background()); err != nil {
			t.Errorf("Executor.Close(): %v", err)
		}
	})
	return executor
}

func assertProcessGone(t *testing.T, pid int) {
	t.Helper()
	end := time.Now().Add(2 * time.Second)
	for processExists(pid) && time.Now().Before(end) {
		time.Sleep(2 * time.Millisecond)
	}
	if processExists(pid) {
		t.Fatalf("escaped descendant PID %d survived cgroup cleanup", pid)
	}
}

func processExists(pid int) bool {
	_, err := os.Stat(filepath.Join("/proc", strconv.Itoa(pid)))
	return err == nil
}

func TestCgroupRootEmptyWaitsForPIDsController(t *testing.T) {
	t.Parallel()
	rootPath := t.TempDir()
	for name, content := range map[string]string{
		"cgroup.events": "populated 0\nfrozen 0\n",
		"cgroup.procs":  "",
		"pids.current":  "1\n",
	} {
		if err := os.WriteFile(filepath.Join(rootPath, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = root.Close() })

	empty, err := cgroupRootEmpty(root)
	if err != nil {
		t.Fatal(err)
	}
	if empty {
		t.Fatal("cgroup was declared empty before pids.current reached zero")
	}
	if err := os.WriteFile(filepath.Join(rootPath, "pids.current"), []byte("0\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	empty, err = cgroupRootEmpty(root)
	if err != nil {
		t.Fatal(err)
	}
	if !empty {
		t.Fatal("cgroup was not declared empty after every emptiness signal reached zero")
	}
}

func assertCgroupValue(t *testing.T, root *os.Root, name, want string) {
	t.Helper()
	value, err := root.ReadFile(name)
	if err != nil {
		t.Fatalf("read %s: %v", name, err)
	}
	if string(value) != want {
		t.Fatalf("%s = %q, want %q", name, value, want)
	}
}
