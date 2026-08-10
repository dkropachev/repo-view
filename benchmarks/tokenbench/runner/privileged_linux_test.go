//go:build linux

package runner

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

const requirePrivilegedTestsEnvironment = "TOKENBENCH_REQUIRE_PRIVILEGED_TESTS"

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
