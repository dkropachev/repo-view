//go:build !aix && !darwin && !dragonfly && !freebsd && !illumos && !linux && !netbsd && !openbsd && !solaris

package codexlauncher

import "os/exec"

func signaledProcessExitStatus(_ *exec.ExitError) int {
	return 1
}
