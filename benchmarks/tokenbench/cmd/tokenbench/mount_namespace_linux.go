//go:build linux

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

const (
	mountNamespaceChildEnvironment = "TOKENBENCH_PRIVATE_MOUNT_CHILD"
	mountNamespaceChildValue       = "tokenbench.mount-namespace/v1"
)

type delegatedRunResult struct{ exitCode int }

func (result *delegatedRunResult) Error() string {
	return fmt.Sprintf("delegated run exited with status %d", result.exitCode)
}

func reexecRunInPrivateMountNamespace(
	ctx context.Context,
	arguments []string,
	credentialDescriptor int,
	stdout, stderr io.Writer,
) error {
	if _, exists := os.LookupEnv(mountNamespaceChildEnvironment); exists {
		return errors.New("private mount-namespace marker is reserved to tokenbench")
	}
	childArguments, err := rewriteCredentialDescriptor(arguments, 3)
	if err != nil {
		return err
	}
	childArguments = append(childArguments, "--private-mount-child")

	duplicate, err := unix.FcntlInt(
		uintptr(credentialDescriptor),
		unix.F_DUPFD_CLOEXEC,
		3,
	)
	if err != nil {
		return fmt.Errorf("pin credential descriptor for mount-namespace child: %w", err)
	}
	credential := os.NewFile(uintptr(duplicate), "tokenbench-credential-source")
	if credential == nil {
		_ = unix.Close(duplicate)
		return errors.New("pin credential descriptor for mount-namespace child")
	}
	defer credential.Close()

	command := exec.CommandContext(
		ctx,
		"/proc/self/exe",
		append([]string{"run"}, childArguments...)...,
	)
	command.Env = append(
		append([]string(nil), os.Environ()...),
		mountNamespaceChildEnvironment+"="+mountNamespaceChildValue,
	)
	command.ExtraFiles = []*os.File{credential}
	command.Stdout = stdout
	command.Stderr = stderr
	command.Stdin = nil
	command.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags: unix.CLONE_NEWNS,
		Pdeathsig:  syscall.SIGKILL,
	}
	err = command.Run()
	if err == nil {
		return &delegatedRunResult{exitCode: 0}
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		code := exit.ExitCode()
		if code < 0 {
			code = 1
		}
		return &delegatedRunResult{exitCode: code}
	}
	return fmt.Errorf("start private mount-namespace child: %w", err)
}

func rewriteCredentialDescriptor(arguments []string, replacement int) ([]string, error) {
	result := append([]string(nil), arguments...)
	found := 0
	for index := 0; index < len(result); index++ {
		switch {
		case result[index] == "--credential-fd":
			if index+1 >= len(result) {
				return nil, errors.New("credential descriptor argument is incomplete")
			}
			result[index+1] = strconv.Itoa(replacement)
			found++
			index++
		case strings.HasPrefix(result[index], "--credential-fd="):
			result[index] = "--credential-fd=" + strconv.Itoa(replacement)
			found++
		}
	}
	if found != 1 {
		return nil, errors.New("credential descriptor must occur exactly once")
	}
	return result, nil
}

func enterPrivateMountNamespaceChild() error {
	value, exists := os.LookupEnv(mountNamespaceChildEnvironment)
	if !exists || value != mountNamespaceChildValue {
		return errors.New("private mount child lacks its re-exec marker")
	}
	if err := os.Unsetenv(mountNamespaceChildEnvironment); err != nil {
		return errors.New("clear private mount child marker")
	}
	self, err := os.Stat("/proc/self/ns/mnt")
	if err != nil {
		return fmt.Errorf("inspect child mount namespace: %w", err)
	}
	parentPath := filepath.Join("/proc", strconv.Itoa(os.Getppid()), "ns", "mnt")
	parent, err := os.Stat(parentPath)
	if err != nil {
		return fmt.Errorf("inspect parent mount namespace: %w", err)
	}
	if os.SameFile(self, parent) {
		return errors.New("run child did not enter a distinct mount namespace")
	}
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("make child mount namespace recursively private: %w", err)
	}
	if err := verifyPrivateMountPropagation("/proc/self/mountinfo"); err != nil {
		return err
	}
	return nil
}

func verifyPrivateMountPropagation(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open mount propagation state: %w", err)
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	buffer := make([]byte, 64<<10)
	scanner.Buffer(buffer, 4<<20)
	lines := 0
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		separator := -1
		for index := 6; index < len(fields); index++ {
			if fields[index] == "-" {
				separator = index
				break
			}
		}
		if len(fields) < 10 || separator < 6 {
			return errors.New("mount propagation state is malformed")
		}
		for _, optional := range fields[6:separator] {
			if strings.HasPrefix(optional, "shared:") ||
				strings.HasPrefix(optional, "master:") ||
				strings.HasPrefix(optional, "propagate_from:") || optional == "unbindable" {
				return fmt.Errorf("mount %q retains propagation field %q", fields[4], optional)
			}
		}
		lines++
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read mount propagation state: %w", err)
	}
	if lines == 0 {
		return errors.New("mount propagation state is empty")
	}
	return nil
}
