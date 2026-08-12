//go:build linux

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"syscall"
	"time"

	"github.com/yapless/scopesifter/internal/processpolicy"
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
		arguments := []string{commandRunnerUtilityFlag, "sleep-leaf"}
		if err := validateCommandRunnerUtilityInvocation(executable, arguments); err != nil {
			return err
		}
		child, executableFile, err := processpolicy.NativeCommand(executable, arguments...)
		if err != nil {
			return fmt.Errorf("pin command-runner utility image: %w", err)
		}
		child.Env = []string{}
		child.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
		if err := child.Start(); err != nil {
			_ = executableFile.Close()
			return fmt.Errorf("start command-runner utility leaf: %w", err)
		}
		if err := executableFile.Close(); err != nil {
			_ = child.Process.Kill()
			_ = child.Wait()
			return fmt.Errorf("close command-runner utility image: %w", err)
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

func validateCommandRunnerUtilityInvocation(executable string, arguments []string) error {
	currentExecutable, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolve current command-runner utility image: %w", err)
	}
	if !filepath.IsAbs(executable) || filepath.Clean(executable) != filepath.Clean(currentExecutable) {
		return errors.New("command-runner utility child does not use the current executable")
	}
	if !slices.Equal(arguments, []string{commandRunnerUtilityFlag, "sleep-leaf"}) {
		return errors.New("command-runner utility child arguments do not match the fixed leaf role")
	}
	if err := processpolicy.Validate(executable, arguments...); err != nil {
		return fmt.Errorf("validate command-runner utility child: %w", err)
	}
	return nil
}
