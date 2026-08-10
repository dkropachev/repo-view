//go:build linux

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

const physicalPathBindHelperEnvironment = "TOKENBENCH_PHYSICAL_PATH_BIND_HELPER"

func TestPhysicalPathSeparationRejectsHardLinkAliases(t *testing.T) {
	directory := t.TempDir()
	first := filepath.Join(directory, "first")
	second := filepath.Join(directory, "second")
	if err := os.WriteFile(first, []byte("authority\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(first, second); err != nil {
		t.Fatal(err)
	}
	err := requireDisjointPaths([]namedPath{
		{name: "first authority", path: first},
		{name: "second authority", path: second},
	})
	if err == nil || !strings.Contains(err.Error(), "physically disjoint") {
		t.Fatalf("hard-link aliases were accepted: %v", err)
	}
}

func TestPhysicalPathSeparationRejectsSymlinkedParent(t *testing.T) {
	directory := t.TempDir()
	realParent := filepath.Join(directory, "real")
	linkedParent := filepath.Join(directory, "linked")
	if err := os.Mkdir(realParent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(realParent, linkedParent); err != nil {
		t.Fatal(err)
	}
	err := requireDisjointPaths([]namedPath{
		{name: "first output", path: filepath.Join(linkedParent, "first")},
		{name: "second output", path: filepath.Join(directory, "second")},
	})
	if err == nil || !strings.Contains(err.Error(), "without symbolic links") {
		t.Fatalf("symlinked output parent was accepted: %v", err)
	}
}

func TestPhysicalPathSeparationAcceptsAbsentSiblings(t *testing.T) {
	directory := t.TempDir()
	if err := requireDisjointPaths([]namedPath{
		{name: "first output", path: filepath.Join(directory, "first")},
		{name: "second output", path: filepath.Join(directory, "second")},
	}); err != nil {
		t.Fatalf("disjoint absent sibling paths were rejected: %v", err)
	}
}

func TestPhysicalPathSeparationRejectsBindMountAliases(t *testing.T) {
	if os.Getenv(physicalPathBindHelperEnvironment) == "1" {
		runPhysicalPathBindMountAssertions(t)
		return
	}
	command := exec.Command(
		os.Args[0],
		"-test.v",
		"-test.run=^TestPhysicalPathSeparationRejectsBindMountAliases$",
	)
	command.Env = append(os.Environ(), physicalPathBindHelperEnvironment+"=1")
	command.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNS}
	output, err := command.CombinedOutput()
	if errors.Is(err, unix.EPERM) {
		privilegedPhysicalPathTestUnavailable(t, "create private mount namespace: %v", err)
	}
	if err != nil {
		t.Fatalf("bind-mount alias helper failed: %v\n%s", err, output)
	}
	if bytesContainTestSkip(output) {
		t.Skip("bind-mount alias prerequisite is unavailable")
	}
}

func runPhysicalPathBindMountAssertions(t *testing.T) {
	t.Helper()
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		privilegedPhysicalPathTestUnavailable(t, "make helper mounts private: %v", err)
	}
	directory := t.TempDir()
	realRoot := filepath.Join(directory, "real")
	realChild := filepath.Join(realRoot, "child")
	alias := filepath.Join(directory, "alias")
	for _, path := range []string{realChild, alias} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	payload := filepath.Join(realChild, "payload")
	if err := os.WriteFile(payload, []byte("payload\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := unix.Mount(realChild, alias, "", unix.MS_BIND, ""); err != nil {
		privilegedPhysicalPathTestUnavailable(t, "create bind alias: %v", err)
	}
	t.Cleanup(func() {
		if err := unix.Unmount(alias, 0); err != nil {
			t.Errorf("unmount bind alias: %v", err)
		}
	})

	for _, test := range []struct {
		name  string
		paths []namedPath
	}{
		{
			name: "same directory inode",
			paths: []namedPath{
				{name: "real child", path: realChild},
				{name: "bind alias", path: alias},
			},
		},
		{
			name: "aliased descendant",
			paths: []namedPath{
				{name: "real root", path: realRoot},
				{name: "aliased payload", path: filepath.Join(alias, "payload")},
			},
		},
		{
			name: "absent output under aliased descendant",
			paths: []namedPath{
				{name: "real root", path: realRoot},
				{name: "aliased output", path: filepath.Join(alias, "future")},
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := requireDisjointPaths(test.paths)
			if err == nil || !strings.Contains(err.Error(), "physically disjoint") {
				t.Fatalf("bind-mount overlap was accepted: %v", err)
			}
		})
	}
}

func privilegedPhysicalPathTestUnavailable(
	t *testing.T,
	format string,
	arguments ...any,
) {
	t.Helper()
	message := strings.TrimSpace(strings.ReplaceAll(
		formatTestMessage(format, arguments...),
		"\n",
		" ",
	))
	if os.Getenv("TOKENBENCH_REQUIRE_PRIVILEGED_TESTS") == "1" {
		t.Fatalf("required privileged kernel test prerequisite failed: %s", message)
	}
	t.Skip(message)
}

func formatTestMessage(format string, arguments ...any) string {
	if len(arguments) == 0 {
		return format
	}
	return fmt.Sprintf(format, arguments...)
}

func bytesContainTestSkip(output []byte) bool {
	return strings.Contains(string(output), "--- SKIP:")
}
