//go:build !aix && !darwin && !dragonfly && !freebsd && !illumos && !linux && !netbsd && !openbsd && !solaris

package codexlauncher

import (
	"os"
	"os/exec"
)

func signaledProcessExitStatus(_ *exec.ExitError) int {
	return 1
}

func launcherSignals() []os.Signal {
	return []os.Signal{os.Interrupt}
}

func signalExitStatus(signal os.Signal) int {
	if signal == os.Interrupt {
		return 130
	}
	return 1
}
