//go:build linux

package commandrunner

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

const (
	entrypointProbeMarker         = "tokenbench-command-runner-entrypoint-v1"
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

// VerifyEntrypoint executes one already-pinned image through the exact
// discovery argv0 and PATH contract. Digest equality alone cannot prove that a
// runner-containing executable routes that invocation to Run; snapshot
// construction therefore requires this bounded semantic proof before granting
// live authority.
func VerifyEntrypoint(ctx context.Context, executable string) error {
	if ctx == nil {
		return errors.New("command-runner entrypoint probe context is required")
	}
	if !filepath.IsAbs(executable) || filepath.Clean(executable) != executable ||
		filepath.Base(executable) != codexDiscoveryBasename {
		return errors.New("command-runner entrypoint probe path is not the pinned discovery pathname")
	}

	probeCtx, cancel := context.WithTimeout(ctx, entrypointProbeTimeout)
	defer cancel()
	toolbox := filepath.Dir(executable)
	command := exec.CommandContext(
		probeCtx,
		executable,
		"-c",
		"cat",
	)
	command.Args[0] = executable
	command.Dir = "/"
	command.Env = []string{
		"HOME=/",
		"PATH=" + toolbox,
		"PWD=/",
		"TMPDIR=/tmp",
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
	command.Stdin = strings.NewReader(entrypointProbeMarker + "\n")
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("execute command-runner entrypoint probe: %w", err)
	}
	if stdout.overflow || stderr.overflow || string(stdout.content) != entrypointProbeMarker+"\n" ||
		len(stderr.content) != 0 {
		return fmt.Errorf(
			"command-runner entrypoint probe returned unexpected bounded output: stdout=%q stderr=%q overflow=%t",
			stdout.content,
			stderr.content,
			stdout.overflow || stderr.overflow,
		)
	}
	return nil
}
