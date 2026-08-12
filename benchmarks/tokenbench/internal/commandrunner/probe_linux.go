//go:build linux

package commandrunner

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"

	"github.com/yapless/scopesifter/internal/processpolicy"
	"golang.org/x/sys/unix"
)

const (
	entrypointProbeMarker         = "tokenbench-command-runner-entrypoint-v1"
	entrypointProbeDiagnostic     = "tokenbench command runner: expected exactly -c COMMAND\n"
	entrypointProbeTimeout        = 2 * time.Second
	entrypointProbeMaxOutputBytes = 4 << 10
)

type entrypointProbeCapture struct {
	content  []byte
	overflow bool
}

func (capture *entrypointProbeCapture) Write(content []byte) (int, error) {
	written := len(content)
	remaining := entrypointProbeMaxOutputBytes - len(capture.content)
	if remaining > 0 {
		if remaining > len(content) {
			remaining = len(content)
		}
		capture.content = append(capture.content, content[:remaining]...)
	}
	if remaining < len(content) {
		capture.overflow = true
	}
	return written, nil
}

// VerifyPinnedEntrypoint executes one already-open image through the exact
// discovery argv0 and PATH contract. It never reopens executable: the child
// receives a duplicate of image as descriptor 3 and executes that descriptor.
// Digest equality alone cannot prove that a runner-containing executable routes
// this invocation to Run, so snapshot construction requires this bounded
// semantic proof before granting live authority. The caller remains responsible
// for binding image's inode and digest to executable before and after this call.
func VerifyPinnedEntrypoint(ctx context.Context, executable string, image *os.File) error {
	if ctx == nil {
		return errors.New("command-runner entrypoint probe context is required")
	}
	toolbox := filepath.Dir(executable)
	if !Invoked(executable, toolbox) {
		return errors.New("command-runner entrypoint probe path is not the pinned discovery pathname")
	}
	if image == nil {
		return errors.New("command-runner entrypoint probe image is required")
	}
	info, err := image.Stat()
	if err != nil {
		return fmt.Errorf("inspect command-runner entrypoint probe image: %w", err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0o111 == 0 {
		return errors.New("command-runner entrypoint probe image is not an executable regular file")
	}
	if err := processpolicy.ValidateNativeFile(image); err != nil {
		return fmt.Errorf("validate command-runner entrypoint probe image: %w", err)
	}
	duplicateFD, err := unix.FcntlInt(image.Fd(), unix.F_DUPFD_CLOEXEC, 3)
	if err != nil {
		return fmt.Errorf("duplicate command-runner entrypoint probe image: %w", err)
	}
	duplicate := os.NewFile(uintptr(duplicateFD), "tokenbench-command-runner-probe")
	if duplicate == nil {
		_ = unix.Close(duplicateFD)
		return errors.New("adopt duplicated command-runner entrypoint probe image")
	}
	defer duplicate.Close()

	probeCtx, cancel := context.WithTimeout(ctx, entrypointProbeTimeout)
	defer cancel()
	command := exec.CommandContext(
		probeCtx,
		"/proc/self/fd/3",
		"-lc",
		entrypointProbeMarker,
	)
	command.Args[0] = executable
	command.ExtraFiles = []*os.File{duplicate}
	command.Dir = "/"
	command.Env = []string{
		"HOME=/",
		"LC_ALL=C",
		"PATH=" + toolbox,
		"PWD=/",
		"TMPDIR=/tmp",
		"TZ=UTC",
	}
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	command.WaitDelay = 100 * time.Millisecond
	var stdout, stderr entrypointProbeCapture
	command.Stdout = &stdout
	command.Stderr = &stderr
	err = command.Run()
	if probeCtx.Err() != nil {
		return fmt.Errorf("execute command-runner entrypoint probe: %w", probeCtx.Err())
	}
	if err == nil {
		return errors.New("command-runner entrypoint probe unexpectedly accepted unsupported argv")
	}
	var exitError *exec.ExitError
	if !errors.As(err, &exitError) || exitError.ExitCode() != 2 {
		return fmt.Errorf("execute command-runner entrypoint probe: %w", err)
	}
	if stdout.overflow || stderr.overflow || len(stdout.content) != 0 ||
		string(stderr.content) != entrypointProbeDiagnostic {
		return fmt.Errorf(
			"command-runner entrypoint probe returned unexpected bounded output: stdout=%q stderr=%q overflow=%t",
			stdout.content,
			stderr.content,
			stdout.overflow || stderr.overflow,
		)
	}
	return nil
}
