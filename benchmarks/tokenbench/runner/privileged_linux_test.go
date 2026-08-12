//go:build linux

package runner

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yapless/scopesifter/benchmarks/tokenbench/harness"
	"github.com/yapless/scopesifter/benchmarks/tokenbench/internal/commandrunner"
	"golang.org/x/sys/unix"
)

const (
	requirePrivilegedTestsEnvironment      = "TOKENBENCH_REQUIRE_PRIVILEGED_TESTS"
	commandRunnerImageFixtureEnvironment   = "TOKENBENCH_COMMAND_RUNNER_IMAGE"
	commandRunnerUtilityFixtureEnvironment = "TOKENBENCH_COMMAND_RUNNER_UTILITY"
	commandRunnerUtilityFlag               = "--command-runner-utility"
	commandRunnerUtilityMarker             = "tokenbench-command-runner-utility-v1"
)

func privilegedTestUnavailable(t *testing.T, format string, arguments ...any) {
	t.Helper()
	message := fmt.Sprintf(format, arguments...)
	if os.Getenv(requirePrivilegedTestsEnvironment) == "1" {
		t.Fatalf("required privileged kernel test prerequisite failed: %s", message)
	}
	t.Skip(message)
}

func privilegedCgroupManager(t *testing.T) *cgroupManager {
	t.Helper()
	manager, err := discoverCgroupManager(time.Second, false)
	if err != nil {
		privilegedTestUnavailable(t, "delegated writable cgroup v2 unavailable: %v", err)
	}
	t.Cleanup(func() {
		if err := manager.close(); err != nil {
			t.Errorf("close privileged cgroup manager: %v", err)
		}
	})
	return manager
}

func privilegedArm(t *testing.T, manager *cgroupManager) (*armCgroup, *bool) {
	t.Helper()
	arm, err := manager.newArm()
	if err != nil {
		t.Fatalf("create privileged arm cgroup: %v", err)
	}
	cleaned := new(bool)
	t.Cleanup(func() {
		if !*cleaned {
			if err := arm.killAndRemove(time.Second); err != nil {
				t.Errorf("clean privileged arm cgroup: %v", err)
			}
		}
	})
	return arm, cleaned
}

func privilegedArmInit(t *testing.T) (*pinnedCommonExecutable, *os.File, *os.File) {
	t.Helper()
	launcher, _, err := prepareArmInit(false)
	if err != nil {
		privilegedTestUnavailable(t, "Landlock arm-init unavailable: %v", err)
	}
	t.Cleanup(func() {
		if err := launcher.close(); err != nil {
			t.Errorf("close privileged arm-init: %v", err)
		}
	})
	common, err := os.Open(os.DevNull)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = common.Close() })
	devNull, err := openDevNullRule()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = devNull.Close() })
	return launcher, common, devNull
}

func TestPrivilegedGoCommandRunnerDiscoveryPath(t *testing.T) {
	imageSource := privilegedExecutableFixture(
		t,
		commandRunnerImageFixtureEnvironment,
	)
	utilitySource := privilegedExecutableFixture(
		t,
		commandRunnerUtilityFixtureEnvironment,
	)
	runtimeRoot := t.TempDir()
	toolbox := filepath.Join(runtimeRoot, "toolbox")
	workingDirectory := filepath.Join(runtimeRoot, "work")
	wrongToolbox := filepath.Join(runtimeRoot, "wrong-toolbox")
	for _, directory := range []string{toolbox, workingDirectory, wrongToolbox} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	discoveryPath := filepath.Join(toolbox, "bash")
	utilityPath := filepath.Join(toolbox, "grep")
	wrongDiscoveryPath := filepath.Join(wrongToolbox, "bash")
	imageDigest := copyStaticExecutableFixture(t, imageSource, discoveryPath)
	_ = copyStaticExecutableFixture(t, utilitySource, utilityPath)
	_ = copyStaticExecutableFixture(t, utilitySource, wrongDiscoveryPath)
	if pinned, err := pinExecutable(discoveryPath, imageDigest, true, true, true); err == nil {
		_ = pinned.close()
		t.Fatal("generic executable pinning accepted the reserved discovery basename")
	}
	discoveryImage, err := os.Open(discoveryPath)
	if err != nil {
		t.Fatal(err)
	}
	defer discoveryImage.Close()
	if err := commandrunner.VerifyPinnedEntrypoint(
		t.Context(), discoveryPath, discoveryImage,
	); err != nil {
		t.Fatalf("verify production command-runner entrypoint: %v", err)
	}
	wrongImage, err := os.Open(wrongDiscoveryPath)
	if err != nil {
		t.Fatal(err)
	}
	defer wrongImage.Close()
	if err := commandrunner.VerifyPinnedEntrypoint(
		t.Context(), wrongDiscoveryPath, wrongImage,
	); err == nil {
		t.Fatal("semantic entrypoint probe accepted a different static Go image")
	}

	executor := newContainedTestExecutorConfig(t, Config{
		ReadOnlyPaths:   []string{toolbox, workingDirectory},
		ExecutablePaths: []string{utilityPath},
	})
	request := func(arguments ...string) ExecutionRequest {
		return ExecutionRequest{
			Arm: BaselineArm,
			Invocation: harness.Invocation{
				Executable:       discoveryPath,
				ExecutableSHA256: imageDigest,
			},
			Process: harness.ProcessSpec{
				Environment: map[string]string{
					"HOME":   workingDirectory,
					"LC_ALL": "C",
					"PATH":   toolbox,
					"PWD":    workingDirectory,
					"TMPDIR": workingDirectory,
				},
				Directory:     workingDirectory,
				Argv:          append([]string{discoveryPath}, arguments...),
				Stdin:         []byte{},
				TimeoutMillis: 30_000,
			},
		}
	}
	ordinary, ordinaryErr := executor.Prepare(t.Context(), request("-c", "grep"))
	if ordinary != nil {
		_ = ordinary.Abort(t.Context())
	}
	if ordinaryErr == nil {
		t.Fatal("ordinary executor role accepted the reserved discovery basename")
	}
	run := func(t *testing.T, arguments ...string) harness.RawExecution {
		t.Helper()
		prepared, err := executor.prepare(
			context.Background(), request(arguments...), verifiedCommandRunnerDiscovery,
		)
		if err != nil {
			t.Fatalf("prepare command-runner fixture %q: %v", arguments, err)
		}
		raw, err := prepared.Execute(context.Background())
		if err != nil {
			t.Fatalf("run command-runner fixture %q: %v", arguments, err)
		}
		return raw
	}

	raw := run(t, "-c", "grep "+commandRunnerUtilityFlag+" print")
	if raw.ExitCode != 0 || string(raw.Stdout) != commandRunnerUtilityMarker+"\n" ||
		len(raw.Stderr) != 0 {
		t.Fatalf("allowlisted Go utility execution: %+v", raw)
	}
	raw = run(t, "-lc", "printf forbidden")
	if raw.ExitCode != 2 || len(raw.Stdout) != 0 ||
		!strings.Contains(string(raw.Stderr), "expected exactly -c COMMAND") {
		t.Fatalf("unsupported command-runner argv was not rejected: %+v", raw)
	}
	for _, test := range []struct {
		name       string
		command    string
		exitCode   int
		diagnostic string
	}{
		{
			name:       "unlisted basename",
			command:    "sh -c 'printf forbidden'",
			exitCode:   125,
			diagnostic: "prohibited script runtime",
		},
		{
			name:       "host absolute path",
			command:    "/bin/sh -c 'printf forbidden'",
			exitCode:   125,
			diagnostic: "prohibited script runtime",
		},
		{
			name:       "host absolute native path",
			command:    "/bin/cat",
			exitCode:   125,
			diagnostic: "bare approved role",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			raw := run(t, "-c", test.command)
			if raw.ExitCode != test.exitCode || len(raw.Stdout) != 0 ||
				!strings.Contains(string(raw.Stderr), test.diagnostic) {
				t.Fatalf("unapproved shell was not rejected: %+v", raw)
			}
		})
	}

	prepared, err := executor.prepare(
		context.Background(),
		request("-c", "grep "+commandRunnerUtilityFlag+" sleep-tree"),
		verifiedCommandRunnerDiscovery,
	)
	if err != nil {
		t.Fatal(err)
	}
	cancelCtx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	started := time.Now()
	raw, err = prepared.Execute(cancelCtx)
	if err != nil {
		t.Fatalf("cancel command-runner tree: %v", err)
	}
	if !raw.Cancelled || raw.TimedOut || time.Since(started) > 3*time.Second {
		t.Fatalf("command-runner cancellation classification: %+v", raw)
	}
	leafPID := commandRunnerLeafPID(t, raw.Stdout)
	if leafPID <= 1 {
		t.Fatalf("command-runner leaf received invalid namespace PID %d", leafPID)
	}
	if err := harness.ValidateResourceOutcome(raw.Resources); err != nil {
		t.Fatalf("command-runner cancellation resources: %v", err)
	}
	if raw.Resources.PIDsCurrent != 0 || raw.Resources.PIDsPeak < 3 {
		t.Fatalf("command-runner tree was not fully reaped: %+v", raw.Resources)
	}
	if _, err := os.Lstat(executor.containment.pairPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("command-runner arm cgroup survived cancellation: %v", err)
	}
	raw = run(t, "-c", "grep "+commandRunnerUtilityFlag+" print")
	if raw.ExitCode != 0 || string(raw.Stdout) != commandRunnerUtilityMarker+"\n" ||
		len(raw.Stderr) != 0 {
		t.Fatalf("command-runner arm was not reusable after cancellation: %+v", raw)
	}
}

func privilegedExecutableFixture(t *testing.T, environmentKey string) string {
	t.Helper()
	path := os.Getenv(environmentKey)
	if path == "" {
		privilegedTestUnavailable(t, "%s is unset", environmentKey)
	}
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		t.Fatalf("%s is not an absolute canonical path", environmentKey)
	}
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 ||
		info.Mode()&os.ModeSymlink != 0 {
		t.Fatalf("%s is not an executable regular file: %v", environmentKey, err)
	}
	return path
}

func copyStaticExecutableFixture(t *testing.T, source, destination string) string {
	t.Helper()
	input, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	if err := validateStaticELF(input); err != nil {
		t.Fatalf("fixture %s is not a static ELF image: %v", source, err)
	}
	digest, err := hashOpenFile(input)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := input.Seek(0, io.SeekStart); err != nil {
		t.Fatal(err)
	}
	output, err := os.OpenFile(destination, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o700)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		t.Fatal(err)
	}
	if err := output.Sync(); err != nil {
		_ = output.Close()
		t.Fatal(err)
	}
	if err := output.Chmod(0o555); err != nil {
		_ = output.Close()
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
	return digest
}

func commandRunnerLeafPID(t *testing.T, output []byte) int {
	t.Helper()
	for _, field := range strings.Fields(string(output)) {
		value, ok := strings.CutPrefix(field, "leaf-pid=")
		if !ok {
			continue
		}
		pid, err := strconv.Atoi(value)
		if err == nil && pid > 0 {
			return pid
		}
	}
	t.Fatalf("command-runner cancellation output omitted leaf PID: %q", output)
	return 0
}

func TestArmCleanupRetriesTransientRmdirWithinDeadline(t *testing.T) {
	manager := privilegedCgroupManager(t)
	arm, cleaned := privilegedArm(t, manager)
	realRemove := manager.removeArm
	calls := 0
	manager.removeArm = func(name string) error {
		calls++
		if calls <= 2 {
			return unix.EBUSY
		}
		return realRemove(name)
	}
	t.Cleanup(func() { manager.removeArm = realRemove })
	if err := arm.killAndRemove(time.Second); err != nil {
		t.Fatalf("retry transient arm rmdir: %v", err)
	}
	*cleaned = true
	if calls != 3 {
		t.Fatalf("arm rmdir calls = %d, want 3", calls)
	}
}

func TestPrivilegedExactConnectKernelBoundary(t *testing.T) {
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	port := listener.Addr().(*net.TCPAddr).Port
	if port <= 0 || port > 65535 {
		t.Fatalf("invalid loopback listener port %d", port)
	}

	manager := privilegedCgroupManager(t)
	bpfArm, bpfCleaned := privilegedArm(t, manager)
	if err := bpfArm.installExactConnectPolicy(uint16(port)); err != nil {
		privilegedTestUnavailable(t, "cgroup connect BPF unavailable: %v", err)
	}
	assertSoleKernelBPFPolicy(t, bpfArm.networkPolicy)
	runExactBPFConnectProbe(t, bpfArm, port)
	if err := bpfArm.killAndRemove(time.Second); err != nil {
		t.Fatal(err)
	}
	*bpfCleaned = true

	arm, cleaned := privilegedArm(t, manager)
	if err := arm.installExactConnectPolicy(uint16(port)); err != nil {
		t.Fatalf("reinstall exact cgroup connect BPF policy: %v", err)
	}
	assertSoleKernelBPFPolicy(t, arm.networkPolicy)
	launcher, common, devNull := privilegedArmInit(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "/proc/self/fd/3", "-test.run=^TestRunnerHelperProcess$")
	command.Args[0] = "tokenbench-exact-network-probe"
	command.ExtraFiles = []*os.File{launcher.launchFile, launcher.launchFile, common, devNull}
	command.Env = []string{
		"TOKENBENCH_HELPER=1",
		"TOKENBENCH_OPERATION=exact-network-policy",
		"TOKENBENCH_ALLOWED_PORT=" + strconv.Itoa(port),
		armInitMarkerEnvironment + "=" + armInitVersion,
		armInitFDLayoutEnvironment + "=" + formatArmInitFDLayout(
			0,
			0,
			0,
			[]uint16{uint16(port)},
			[]uint16{},
		),
	}
	command.Dir = "/"
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := configureContainedCommand(command, arm, false); err != nil {
		t.Fatal(err)
	}
	runErr := command.Run()
	cleanupErr := arm.killAndRemove(time.Second)
	if cleanupErr == nil {
		*cleaned = true
	}
	if runErr != nil || cleanupErr != nil || stdout.String() != "exact-network-ok\n" || stderr.Len() != 0 {
		t.Fatalf(
			"exact network kernel probe: run=%v cleanup=%v stdout=%q stderr=%q",
			runErr,
			cleanupErr,
			stdout.String(),
			stderr.String(),
		)
	}
}

func runExactBPFConnectProbe(t *testing.T, arm *armCgroup, port int) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, executable, "-test.run=^TestRunnerHelperProcess$")
	command.Env = []string{
		"TOKENBENCH_HELPER=1",
		"TOKENBENCH_OPERATION=exact-connect-bpf",
		"TOKENBENCH_ALLOWED_PORT=" + strconv.Itoa(port),
	}
	command.Dir = "/"
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := configureContainedCommand(command, arm, false); err != nil {
		t.Fatal(err)
	}
	if err := command.Run(); err != nil || stdout.String() != "exact-bpf-connect-ok\n" ||
		stderr.Len() != 0 {
		t.Fatalf(
			"exact BPF connect probe: run=%v stdout=%q stderr=%q",
			err,
			stdout.String(),
			stderr.String(),
		)
	}
}

func TestPrivilegedExactConnectRejectsAncestorProgram(t *testing.T) {
	manager := privilegedCgroupManager(t)
	baseline, _, err := queryCgroupBPFPrograms(
		int(manager.lease.Fd()),
		unix.BPF_CGROUP_INET4_CONNECT,
		unix.BPF_F_QUERY_EFFECTIVE,
	)
	if err != nil {
		privilegedTestUnavailable(t, "query ancestor cgroup BPF chain: %v", err)
	}
	if len(baseline) != 0 {
		privilegedTestUnavailable(t, "delegation inherits unexpected cgroup BPF programs: %v", baseline)
	}

	arm, cleaned := privilegedArm(t, manager)
	programFD, ancestorID, err := loadCgroupConnectProgram(
		"tb_ancestor",
		unix.BPF_CGROUP_INET4_CONNECT,
		[]bpfInstruction{
			{Code: bpfMove64Imm, Regs: bpfRegisters(0, 0), Imm: 1},
			{Code: bpfExit},
		},
	)
	if err != nil {
		privilegedTestUnavailable(t, "load ancestor cgroup BPF program: %v", err)
	}
	linkFD := -1
	t.Cleanup(func() {
		if linkFD >= 0 {
			_ = detachCgroupBPFLink(linkFD)
			_ = unix.Close(linkFD)
		}
		if programFD >= 0 {
			_ = unix.Close(programFD)
		}
	})
	linkFD, err = createCgroupBPFLink(
		programFD,
		int(manager.lease.Fd()),
		unix.BPF_CGROUP_INET4_CONNECT,
	)
	if err != nil {
		privilegedTestUnavailable(t, "attach ancestor cgroup BPF program: %v", err)
	}

	installErr := arm.installExactConnectPolicy(443)
	if installErr == nil || !strings.Contains(installErr.Error(), "effective chain drifted") {
		t.Fatalf("exact policy with ancestor program = %v, want effective-chain rejection", installErr)
	}
	effective, _, queryErr := queryCgroupBPFPrograms(
		int(arm.directory.Fd()),
		unix.BPF_CGROUP_INET4_CONNECT,
		unix.BPF_F_QUERY_EFFECTIVE,
	)
	if queryErr != nil || len(effective) != 2 || !containsBPFProgram(effective, ancestorID) {
		t.Fatalf("effective ancestor chain = %v, %v; want two programs including %d", effective, queryErr, ancestorID)
	}
	if err := detachCgroupBPFLink(linkFD); err != nil {
		t.Fatal(err)
	}
	if err := unix.Close(linkFD); err != nil {
		t.Fatal(err)
	}
	linkFD = -1
	if err := unix.Close(programFD); err != nil {
		t.Fatal(err)
	}
	programFD = -1
	if err := arm.verifyExactConnectPolicy(); err != nil {
		t.Fatalf("exact policy did not recover after ancestor detach: %v", err)
	}
	if err := arm.killAndRemove(time.Second); err != nil {
		t.Fatal(err)
	}
	*cleaned = true
}

func TestPrivilegedArmInitPIDNamespaceBoundary(t *testing.T) {
	manager := privilegedCgroupManager(t)
	launcher, common, devNull := privilegedArmInit(t)
	if err := probeArmInitBoundary(
		manager,
		launcher.launchFile,
		common,
		devNull,
		10*time.Second,
		true,
	); err != nil {
		privilegedTestUnavailable(
			t,
			"PID namespace/Landlock/no_new_privs/seccomp/capability boundary unavailable: %v",
			err,
		)
	}
}

func assertSoleKernelBPFPolicy(t *testing.T, policy *cgroupConnectPolicy) {
	t.Helper()
	if policy == nil {
		t.Fatal("exact cgroup BPF policy is nil")
	}
	for _, attachment := range []cgroupBPFLink{policy.ipv4, policy.ipv6} {
		direct, directFlags, err := queryCgroupBPFPrograms(policy.target, attachment.attachType, 0)
		if err != nil || directFlags != unix.BPF_F_ALLOW_MULTI ||
			len(direct) != 1 || direct[0] != attachment.programID {
			t.Fatalf(
				"direct cgroup BPF chain = ids %v flags %#x err %v, want sole program %d",
				direct,
				directFlags,
				err,
				attachment.programID,
			)
		}
		effective, effectiveFlags, err := queryCgroupBPFPrograms(
			policy.target,
			attachment.attachType,
			unix.BPF_F_QUERY_EFFECTIVE,
		)
		if err != nil || effectiveFlags != 0 ||
			len(effective) != 1 || effective[0] != attachment.programID {
			t.Fatalf(
				"effective cgroup BPF chain = ids %v flags %#x err %v, want sole program %d",
				effective,
				effectiveFlags,
				err,
				attachment.programID,
			)
		}
	}
}

func containsBPFProgram(programs []uint32, want uint32) bool {
	for _, program := range programs {
		if program == want {
			return true
		}
	}
	return false
}
