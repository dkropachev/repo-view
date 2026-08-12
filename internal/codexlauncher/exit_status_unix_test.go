//go:build aix || darwin || dragonfly || freebsd || illumos || linux || netbsd || openbsd || solaris

package codexlauncher

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
	"time"
)

func TestProcessExitStatusPreservesSignalConvention(t *testing.T) {
	command := exec.Command(os.Args[0], "-test.run=TestSignalExitHelper")
	command.Env = append(os.Environ(), "SCOPESIFTER_SIGNAL_EXIT_HELPER=1")
	err := command.Run()
	if status := processExitStatus(err); status != 128+int(syscall.SIGTERM) {
		t.Fatalf("status = %d, want %d", status, 128+int(syscall.SIGTERM))
	}
}

func TestSignalExitHelper(t *testing.T) {
	if os.Getenv("SCOPESIFTER_SIGNAL_EXIT_HELPER") != "1" {
		return
	}
	if err := syscall.Kill(os.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	t.Fatal("process survived SIGTERM")
}
