//go:build linux

package taskctl

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"golang.org/x/sys/unix"
)

const publicationNestedMountHelperEnvironment = "TASKCTL_PUBLICATION_NESTED_MOUNT_HELPER"

func TestPhysicalPublicationTopologyRejectsEndpointReplacement(t *testing.T) {
	root := t.TempDir()
	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	if err := os.WriteFile(first, []byte("first\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(second, []byte("second\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	firstInfo, err := os.Lstat(first)
	if err != nil {
		t.Fatal(err)
	}
	secondInfo, err := os.Lstat(second)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(first, first+"-displaced"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(first, []byte("replacement\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = requirePhysicallyDisjointPublicationPaths([]publicationPhysicalPath{
		{name: "first", path: first, expected: firstInfo, exists: true},
		{name: "second", path: second, expected: secondInfo, exists: true},
	})
	if err == nil || !strings.Contains(err.Error(), "differs from its inspected identity") {
		t.Fatalf("endpoint replacement error = %v", err)
	}
}

func TestPhysicalPublicationTopologyRejectsAbsentOutputParentReplacement(t *testing.T) {
	root := t.TempDir()
	outputParent := filepath.Join(root, "output")
	input := filepath.Join(root, "input")
	if err := os.Mkdir(outputParent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(input, []byte("input\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	parentInfo, err := os.Lstat(outputParent)
	if err != nil {
		t.Fatal(err)
	}
	inputInfo, err := os.Lstat(input)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(outputParent, outputParent+"-displaced"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outputParent, 0o700); err != nil {
		t.Fatal(err)
	}
	err = requirePhysicallyDisjointPublicationPaths([]publicationPhysicalPath{
		{
			name: "output", path: filepath.Join(outputParent, "report.md"),
			expected: parentInfo, exists: false,
		},
		{name: "input", path: input, expected: inputInfo, exists: true},
	})
	if err == nil || !strings.Contains(err.Error(), "differs from its inspected identity") {
		t.Fatalf("output-parent replacement error = %v", err)
	}
}

func TestPhysicalPublicationTopologyDetectsBindAliasFromMountRoots(t *testing.T) {
	left := publicationPhysicalIdentity{
		filesystemPath: "/physical/repository", mountID: 10,
		deviceMajor: 8, deviceMinor: 1, mode: unix.S_IFDIR, exists: true,
	}
	right := publicationPhysicalIdentity{
		filesystemPath: "/physical/repository/nested", mountID: 20,
		deviceMajor: 8, deviceMinor: 1, mode: unix.S_IFDIR, exists: true,
	}
	if !publicationPhysicalPathsOverlap(left, right) {
		t.Fatal("bind-mounted directory aliases were not detected from physical mount roots")
	}
}

func TestPhysicalPublicationTopologyDetectsAliasIntoNestedMount(t *testing.T) {
	input := publicationPhysicalIdentity{
		path: "/repository", filesystemPath: "/repository", mountID: 10,
		deviceMajor: 8, deviceMinor: 1, mode: unix.S_IFDIR, exists: true,
	}
	output := publicationPhysicalIdentity{
		path: "/publish/report.md", filesystemPath: "/report.md", mountID: 30,
		deviceMajor: 0, deviceMinor: 42, mode: unix.S_IFDIR, exists: false,
	}
	mounts := map[uint64]publicationMountRecord{
		10: {
			root: "/", mountPoint: "/", mountID: 10,
			deviceMajor: 8, deviceMinor: 1,
		},
		20: {
			root: "/", mountPoint: "/repository/nested", mountID: 20,
			deviceMajor: 0, deviceMinor: 42,
		},
		30: {
			root: "/", mountPoint: "/publish", mountID: 30,
			deviceMajor: 0, deviceMinor: 42,
		},
	}
	if !publicationPhysicalPathsOverlapInMountNamespace(input, output, mounts) {
		t.Fatal("output alias into a nested cross-device mount was not detected")
	}
}

func TestPhysicalPublicationTopologyRejectsRealNestedMountAlias(t *testing.T) {
	if os.Getenv(publicationNestedMountHelperEnvironment) == "1" {
		runPhysicalPublicationNestedMountAssertions(t)
		return
	}

	t.Setenv(publicationNestedMountHelperEnvironment, "1")
	command := exec.Command(
		os.Args[0],
		"-test.v",
		"-test.run=^TestPhysicalPublicationTopologyRejectsRealNestedMountAlias$",
	)
	command.Env = os.Environ()
	command.SysProcAttr = &syscall.SysProcAttr{Cloneflags: unix.CLONE_NEWNS}
	output, err := command.CombinedOutput()
	if errors.Is(err, unix.EPERM) {
		publicationPrivilegedTestUnavailable(t, "create private mount namespace: %v", err)
	}
	if err != nil {
		t.Fatalf("nested-mount helper failed: %v\n%s", err, output)
	}
	if strings.Contains(string(output), "--- SKIP:") {
		t.Skip("nested-mount publication prerequisite is unavailable")
	}
	if !strings.Contains(
		string(output),
		"--- PASS: TestPhysicalPublicationTopologyRejectsRealNestedMountAlias",
	) {
		t.Fatalf("nested-mount helper omitted its passing test result:\n%s", output)
	}
}

func runPhysicalPublicationNestedMountAssertions(t *testing.T) {
	t.Helper()
	if os.Geteuid() != 0 {
		publicationPrivilegedTestUnavailable(t, "nested-mount test requires effective UID 0")
	}
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		publicationPrivilegedTestUnavailable(t, "make helper mounts private: %v", err)
	}

	directory := t.TempDir()
	repository := filepath.Join(directory, "repository")
	nested := filepath.Join(repository, "nested")
	backing := filepath.Join(directory, "backing")
	alias := filepath.Join(directory, "publish")
	for _, path := range []string{nested, backing, alias} {
		if err := os.MkdirAll(path, 0o700); err != nil {
			t.Fatal(err)
		}
	}

	if err := unix.Mount(
		"taskctl-publication-test",
		backing,
		"tmpfs",
		unix.MS_NOSUID|unix.MS_NODEV|unix.MS_NOEXEC,
		"size=1048576,mode=0700",
	); err != nil {
		publicationPrivilegedTestUnavailable(t, "mount cross-device test filesystem: %v", err)
	}
	backingMounted := true
	nestedMounted := false
	aliasMounted := false
	t.Cleanup(func() {
		for _, mounted := range []struct {
			path   string
			active *bool
		}{
			{path: alias, active: &aliasMounted},
			{path: nested, active: &nestedMounted},
			{path: backing, active: &backingMounted},
		} {
			if !*mounted.active {
				continue
			}
			if err := unix.Unmount(mounted.path, 0); err != nil {
				t.Errorf("unmount %s: %v", mounted.path, err)
			}
		}
	})
	if err := unix.Mount(backing, nested, "", unix.MS_BIND, ""); err != nil {
		t.Fatalf("bind nested input mount: %v", err)
	}
	nestedMounted = true
	if err := unix.Mount(backing, alias, "", unix.MS_BIND, ""); err != nil {
		t.Fatalf("bind publication alias: %v", err)
	}
	aliasMounted = true

	repositoryInfo, err := os.Lstat(repository)
	if err != nil {
		t.Fatal(err)
	}
	aliasInfo, err := os.Lstat(alias)
	if err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(alias, "report.md")
	err = requirePhysicallyDisjointPublicationPaths([]publicationPhysicalPath{
		{name: "output", path: output, expected: aliasInfo, exists: false},
		{name: "repository", path: repository, expected: repositoryInfo, exists: true},
	})
	if err == nil || !strings.Contains(err.Error(), "physically disjoint") {
		t.Fatalf("nested-mount publication alias was accepted: %v", err)
	}
	if _, statErr := os.Lstat(output); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("rejected publication unexpectedly created output: %v", statErr)
	}
}

func publicationPrivilegedTestUnavailable(t *testing.T, format string, arguments ...any) {
	t.Helper()
	if os.Getenv("TOKENBENCH_REQUIRE_PRIVILEGED_TESTS") == "1" {
		t.Fatalf("required privileged kernel test prerequisite failed: "+format, arguments...)
	}
	t.Skipf(format, arguments...)
}
