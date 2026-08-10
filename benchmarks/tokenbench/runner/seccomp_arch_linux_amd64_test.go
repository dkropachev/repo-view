//go:build linux && amd64

package runner

import (
	"errors"
	"os"
	"os/exec"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

const x32SeccompProbeEnvironment = "TOKENBENCH_TEST_X32_SECCOMP_PROBE"

func TestProcessInspectionSeccompKillsX32SyscallTable(t *testing.T) {
	if os.Getenv(x32SeccompProbeEnvironment) == "1" {
		if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
			os.Exit(120)
		}
		if err := restrictProcessInspection(); err != nil {
			os.Exit(121)
		}
		_, _, _ = unix.RawSyscall(uintptr(unix.SYS_GETPID)|uintptr(x32SyscallBit), 0, 0, 0)
		os.Exit(122)
	}

	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(executable, "-test.run=^TestProcessInspectionSeccompKillsX32SyscallTable$")
	command.Env = append(os.Environ(), x32SeccompProbeEnvironment+"=1")
	err = command.Run()
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) {
		t.Fatalf("x32 probe returned %v, want seccomp signal", err)
	}
	status, ok := exitError.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() || status.Signal() != syscall.SIGSYS {
		t.Fatalf("x32 probe status = %v, want SIGSYS", exitError.Sys())
	}
}
