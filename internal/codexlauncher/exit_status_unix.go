//go:build aix || darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd || solaris

package codexlauncher

import (
	"os/exec"
	"syscall"
)

func signaledProcessExitStatus(err *exec.ExitError) int {
	status, ok := err.Sys().(syscall.WaitStatus)
	if !ok || !status.Signaled() {
		return 1
	}
	return 128 + int(status.Signal())
}
