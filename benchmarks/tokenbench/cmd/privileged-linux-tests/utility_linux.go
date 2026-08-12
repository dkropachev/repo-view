//go:build linux

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"syscall"
	"time"
)

const commandRunnerUtilityMarker = "tokenbench-command-runner-utility-v1"

func runCommandRunnerUtility(mode string, stdout, stderr io.Writer) error {
	switch mode {
	case "print":
		_, err := fmt.Fprintln(stdout, commandRunnerUtilityMarker)
		return err
	case "sleep-tree":
		executable, err := os.Executable()
		if err != nil {
			return fmt.Errorf("resolve command-runner utility image: %w", err)
		}
		child := exec.Command(executable, commandRunnerUtilityFlag, "sleep-leaf")
		child.Env = []string{}
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := child.Start(); err != nil {
			return fmt.Errorf("start command-runner utility leaf: %w", err)
		}
		if _, err := fmt.Fprintf(stdout, "leaf-pid=%d\n", child.Process.Pid); err != nil {
			_ = child.Process.Kill()
			_ = child.Wait()
			return err
		}
		if err := child.Wait(); err != nil {
			return fmt.Errorf("wait for command-runner utility leaf: %w", err)
		}
		return nil
	case "sleep-leaf":
		time.Sleep(10 * time.Minute)
		return nil
	default:
		_, _ = fmt.Fprintf(stderr, "unknown command-runner utility mode %q\n", mode)
		return errors.New("unknown command-runner utility mode")
	}
}
