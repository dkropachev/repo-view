// Command testhelper is a static test fixture for authenticated descriptor
// execution. It is built by the launcher tests with CGO disabled.
package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	helperModeValue          = "taskctl-launcher-test-helper"
	helperRecordCWDValue     = "record-cwd"
	helperSpawnChildValue    = "spawn-child"
	helperWaitForParentValue = "wait-for-parent-death"
	helperChildModeValue     = "taskctl-launcher-test-child"
	helperIgnoreSignalsValue = "ignore-signals"
)

func main() {
	os.Exit(run())
}

func run() int {
	if os.Getenv("TASKCTL_REPOSITORY_BINDINGS") == helperChildModeValue {
		return runChild()
	}
	if os.Getenv("TASKCTL_REPOSITORY_BINDINGS") != helperModeValue {
		return 95
	}
	input, err := io.ReadAll(os.Stdin)
	if err != nil {
		return 91
	}
	marker := os.Getenv("TASKCTL_OUTPUT")
	value := os.Getenv("TASKCTL_INPUT")
	if os.Getenv("TASKCTL_SOURCE_SELECTIONS") == helperWaitForParentValue {
		if value == helperIgnoreSignalsValue {
			signal.Ignore(syscall.SIGTERM)
		}
		if marker == "" || os.WriteFile(marker, []byte(strconv.Itoa(os.Getpid())), 0o600) != nil {
			return 92
		}
		select {}
	}
	content := strings.Join(os.Args[1:], "\x00") + "\n" + value + "\n" + string(input)
	if os.Getenv("TASKCTL_SOURCE_SELECTIONS") == helperRecordCWDValue {
		cwd, cwdErr := os.Getwd()
		if cwdErr != nil {
			return 93
		}
		content += "\nCWD=" + cwd
	}
	if os.Getenv("TASKCTL_SOURCE_SELECTIONS") == helperSpawnChildValue {
		self, err := os.Open("/proc/self/exe")
		if err != nil {
			return 90
		}
		defer self.Close()
		command := exec.Command("/proc/self/fd/3")
		command.ExtraFiles = []*os.File{self}
		command.Stdin = os.Stdin
		command.Stdout = os.Stdout
		command.Stderr = os.Stderr
		command.Env = []string{
			"TASKCTL_REPOSITORY_BINDINGS=" + helperChildModeValue,
			"TASKCTL_OUTPUT=" + marker,
			"TASKCTL_INPUT=" + value,
		}
		if err := command.Start(); err != nil {
			_, _ = fmt.Fprintln(os.Stderr, "start descendant:", err)
			return 90
		}
		if err := os.WriteFile(marker, []byte(strconv.Itoa(command.Process.Pid)), 0o600); err != nil {
			return 89
		}
		if value == helperIgnoreSignalsValue {
			signal.Ignore(syscall.SIGTERM)
		}
		select {}
	}
	for _, forbidden := range []string{
		"LD_PRELOAD",
		"LD_LIBRARY_PATH",
		"PATH",
		"HOME",
		"GODEBUG",
		"GOTRACEBACK",
		"TASKCTL_EXECUTABLE_SHA256",
		"UNREVIEWED_LAUNCHER_VALUE",
	} {
		if _, present := os.LookupEnv(forbidden); present {
			return 94
		}
	}
	if marker == "" || os.WriteFile(marker, []byte(content), 0o600) != nil {
		return 92
	}
	_, _ = fmt.Fprintf(os.Stdout, "helper stdout: %s\n", value)
	_, _ = fmt.Fprintf(os.Stderr, "helper stderr: %s\n", value)
	return 0
}

func runChild() int {
	marker := os.Getenv("TASKCTL_OUTPUT")
	if marker == "" {
		return 88
	}
	if os.Getenv("TASKCTL_INPUT") == helperIgnoreSignalsValue {
		signal.Ignore(syscall.SIGTERM)
	}
	ready := marker + ".ready"
	if err := os.WriteFile(ready, []byte(strconv.Itoa(os.Getpid())), 0o600); err != nil {
		return 87
	}
	for {
		time.Sleep(time.Hour)
	}
}
