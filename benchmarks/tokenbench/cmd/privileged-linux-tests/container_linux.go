//go:build linux

package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

const (
	cgroupRoot        = "/sys/fs/cgroup"
	fsverityImage     = "/tmp/tokenbench-fsverity.ext4"
	fsverityRoot      = "/tokenbench-fsverity"
	cgroup2SuperMagic = 0x63677270
)

type privilegedSuite struct {
	environment map[string]string
	binary      string
	timeout     string
	names       []string
	delegated   bool
}

var privilegedSuites = []privilegedSuite{
	{
		binary: "/tokenbench-tests/runner.test", timeout: "8m", delegated: true,
		names: []string{
			"TestCgroupManagerAppliesExactArmLimitsAndReusesStablePath",
			"TestArmCleanupRetriesTransientRmdirWithinDeadline",
			"TestPrivilegedGoCommandRunnerDiscoveryPath",
			"TestLandlockBlocksCgroupEscapeAndAllowsOnlyPinnedWritableRoots",
			"TestLandlockFullPolicyDeniesHostReadsExecutablesAndLoaderBypass",
			"TestPrivilegedExactConnectKernelBoundary",
			"TestPrivilegedExactConnectRejectsAncestorProgram",
			"TestPrivilegedArmInitPIDNamespaceBoundary",
			"TestProcessInspectionSeccompKillsX32SyscallTable",
		},
		environment: map[string]string{
			commandRunnerImageEnvironment:   "/tokenbench-tests/tokenbench",
			commandRunnerUtilityEnvironment: "/tokenbench-tests/privileged-linux-tests",
		},
	},
	{
		binary: "/tokenbench-tests/tokenbench-command.test", timeout: "2m",
		names: []string{"TestPhysicalPathSeparationRejectsBindMountAliases"},
	},
	{
		binary: "/tokenbench-tests/source.test", timeout: "2m",
		names: []string{"TestPrivilegedTreeDigestRejectsMountedGitlink"},
	},
	{
		binary: "/tokenbench-tests/snapshot.test", timeout: "2m",
		names: []string{
			"TestImmutableFileHasMeasuredFSVerity",
			"TestFSVerityMerkleBlockSizeIsPageCompatible",
			"TestReadOnlySelfBindFailsClosedWithoutAuthority",
			"TestPrivilegedMountedAuthorityCloseReleasesKernelBoundary",
		},
		environment: map[string]string{
			"TOKENBENCH_FSVERITY_TEST_ROOT": fsverityRoot,
			"TMPDIR":                        fsverityRoot + "/tmp",
		},
	},
	{
		binary: "/tokenbench-tests/workspace.test", timeout: "2m",
		names: []string{
			"TestPrivilegedWorkspaceMountLifecycle",
			"TestPrivilegedWorkspaceMinimumEntryLimitReservesPrivateLayout",
			"TestPrivilegedWorkspaceCleanupFollowsRelocatedActiveMounts",
			"TestPrivilegedWorkspaceCleanupFollowsRootRelocatedDuringAttach",
			"TestPrivilegedWorkspaceRestrictiveUmaskConstructionAndCleanup",
			"TestPrivilegedWorkspaceUnidentifiedPostMkdirClosesPair",
			"TestPrivilegedWorkspaceMaximumEntriesIncludesCacheRoot",
		},
	},
}

func containerMain(stdout, stderr io.Writer) (resultErr error) {
	if os.Geteuid() != 0 {
		return errors.New("privileged container must run as root")
	}
	if err := assertDistinctNamespace("mnt", os.Getenv("TOKENBENCH_HOST_MOUNT_NAMESPACE")); err != nil {
		return err
	}
	if err := assertDistinctNamespace("cgroup", os.Getenv("TOKENBENCH_HOST_CGROUP_NAMESPACE")); err != nil {
		return err
	}
	for _, name := range []string{"mkfs.ext4", "mount", "umount"} {
		if _, err := exec.LookPath(name); err != nil {
			return fmt.Errorf("required command is unavailable: %s", name)
		}
	}
	if err := unix.Mount("", "/", "", unix.MS_REC|unix.MS_PRIVATE, ""); err != nil {
		return fmt.Errorf("make container mount tree private: %w", err)
	}

	cleanupFSVerity, err := setupFSVerity(stdout, stderr)
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, cleanupFSVerity())
	}()

	delegation, cleanupCgroup, err := setupCgroupDelegation()
	if err != nil {
		return err
	}
	defer func() {
		resultErr = errors.Join(resultErr, cleanupCgroup())
	}()

	for _, suite := range privilegedSuites {
		if err := runPrivilegedSuite(suite, delegation, stdout); err != nil {
			return err
		}
		if suite.delegated {
			if err := closeCgroupDelegation(delegation); err != nil {
				return err
			}
		}
	}
	return nil
}

func assertDistinctNamespace(kind, inherited string) error {
	if inherited == "" {
		return fmt.Errorf("host %s namespace identity was not supplied", kind)
	}
	observed, err := os.Readlink("/proc/self/ns/" + kind)
	if err != nil {
		return fmt.Errorf("read container %s namespace: %w", kind, err)
	}
	if observed == inherited {
		return fmt.Errorf("container did not receive a private %s namespace", kind)
	}
	return nil
}

func setupFSVerity(stdout, stderr io.Writer) (func() error, error) {
	image, err := os.OpenFile(fsverityImage, os.O_RDWR|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return nil, fmt.Errorf("create fs-verity image: %w", err)
	}
	if err := image.Truncate(512 << 20); err != nil {
		_ = image.Close()
		_ = os.Remove(fsverityImage)
		return nil, fmt.Errorf("size fs-verity image: %w", err)
	}
	if err := image.Close(); err != nil {
		_ = os.Remove(fsverityImage)
		return nil, fmt.Errorf("close fs-verity image: %w", err)
	}
	removeImage := true
	defer func() {
		if removeImage {
			_ = os.Remove(fsverityImage)
		}
	}()

	if err := runExternal(stdout, stderr, "mkfs.ext4", "-q", "-F", "-O", "verity", fsverityImage); err != nil {
		return nil, fmt.Errorf("format fs-verity image: %w", err)
	}
	if err := os.Mkdir(fsverityRoot, 0o755); err != nil {
		return nil, fmt.Errorf("create fs-verity mountpoint: %w", err)
	}
	mounted := false
	defer func() {
		if !mounted {
			_ = os.Remove(fsverityRoot)
		}
	}()
	if err := runExternal(stdout, stderr, "mount", "-t", "ext4", "-o", "loop,nosuid,nodev", fsverityImage, fsverityRoot); err != nil {
		return nil, fmt.Errorf("mount fs-verity image: %w", err)
	}
	mounted = true
	if err := os.Mkdir(filepath.Join(fsverityRoot, "tmp"), 0o755); err != nil {
		_ = runExternal(stdout, stderr, "umount", fsverityRoot)
		_ = os.Remove(fsverityRoot)
		return nil, fmt.Errorf("create fs-verity temporary directory: %w", err)
	}
	removeImage = false
	return func() error {
		var cleanupErr error
		if err := runExternal(stdout, stderr, "umount", fsverityRoot); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("unmount fs-verity image: %w", err))
		} else if err := os.Remove(fsverityRoot); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove fs-verity mountpoint: %w", err))
		}
		if err := os.Remove(fsverityImage); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove fs-verity image: %w", err))
		}
		return cleanupErr
	}, nil
}

func runExternal(stdout, stderr io.Writer, name string, arguments ...string) error {
	command := exec.Command(name, arguments...)
	command.Stdout = stdout
	command.Stderr = stderr
	return command.Run()
}

func setupCgroupDelegation() (string, func() error, error) {
	var filesystem unix.Statfs_t
	if err := unix.Statfs(cgroupRoot, &filesystem); err != nil {
		return "", nil, fmt.Errorf("inspect cgroup-v2 mount: %w", err)
	}
	if uint64(filesystem.Type) != cgroup2SuperMagic {
		return "", nil, errors.New("unified cgroup v2 is unavailable")
	}
	if err := unix.Access(filepath.Join(cgroupRoot, "cgroup.procs"), unix.W_OK); err != nil {
		return "", nil, errors.New("cgroup-v2 mount is not writable")
	}
	controllers, err := readWords(filepath.Join(cgroupRoot, "cgroup.controllers"))
	if err != nil {
		return "", nil, fmt.Errorf("read cgroup-v2 controllers: %w", err)
	}
	for _, required := range []string{"cpu", "memory", "pids"} {
		if !slices.Contains(controllers, required) {
			return "", nil, fmt.Errorf("cgroup-v2 controller is unavailable: %s", required)
		}
	}

	driver := filepath.Join(cgroupRoot, "tokenbench-ci-driver-v1")
	delegation := filepath.Join(cgroupRoot, "tokenbench-ci-delegation-v1")
	for _, path := range []string{driver, delegation} {
		if _, err := os.Lstat(path); err == nil {
			return "", nil, errors.New("stale tokenbench CI cgroup exists")
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", nil, fmt.Errorf("inspect cgroup path: %w", err)
		}
	}
	if err := os.Mkdir(driver, 0o755); err != nil {
		return "", nil, fmt.Errorf("create CI driver cgroup: %w", err)
	}
	cleaned := false
	cleanup := func() error {
		if cleaned {
			return nil
		}
		var cleanupErr error
		if err := os.Remove(delegation); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove delegated cgroup: %w", err))
		}
		if err := os.WriteFile(filepath.Join(cgroupRoot, "cgroup.subtree_control"), []byte("-cpu -memory -pids\n"), 0); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("disable cgroup controllers: %w", err))
		}
		pid := strconv.Itoa(os.Getpid()) + "\n"
		if err := os.WriteFile(filepath.Join(cgroupRoot, "cgroup.procs"), []byte(pid), 0); err != nil {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("return driver to cgroup root: %w", err))
		}
		if err := os.Remove(driver); err != nil && !errors.Is(err, os.ErrNotExist) {
			cleanupErr = errors.Join(cleanupErr, fmt.Errorf("remove driver cgroup: %w", err))
		}
		if cleanupErr == nil {
			cleaned = true
		}
		return cleanupErr
	}

	pid := strconv.Itoa(os.Getpid()) + "\n"
	if err := os.WriteFile(filepath.Join(driver, "cgroup.procs"), []byte(pid), 0); err != nil {
		_ = cleanup()
		return "", nil, fmt.Errorf("enter CI driver cgroup: %w", err)
	}
	driverProcesses, err := readWords(filepath.Join(driver, "cgroup.procs"))
	if err != nil || len(driverProcesses) != 1 || driverProcesses[0] != strconv.Itoa(os.Getpid()) {
		_ = cleanup()
		return "", nil, errors.New("could not isolate the CI driver cgroup")
	}
	if err := os.WriteFile(filepath.Join(cgroupRoot, "cgroup.subtree_control"), []byte("+cpu +memory +pids\n"), 0); err != nil {
		_ = cleanup()
		return "", nil, fmt.Errorf("enable cgroup controllers: %w", err)
	}
	if err := os.Mkdir(delegation, 0o755); err != nil {
		_ = cleanup()
		return "", nil, fmt.Errorf("create delegated cgroup: %w", err)
	}
	for name, value := range map[string]string{
		"pids.max":   "4096\n",
		"memory.max": strconv.FormatInt(32<<30, 10) + "\n",
	} {
		if err := os.WriteFile(filepath.Join(delegation, name), []byte(value), 0); err != nil {
			_ = cleanup()
			return "", nil, fmt.Errorf("set delegated cgroup %s: %w", name, err)
		}
	}
	probe := filepath.Join(delegation, "writable-probe")
	if err := os.Mkdir(probe, 0o755); err != nil {
		_ = cleanup()
		return "", nil, fmt.Errorf("create delegated cgroup probe: %w", err)
	}
	if err := os.Remove(probe); err != nil {
		_ = cleanup()
		return "", nil, fmt.Errorf("remove delegated cgroup probe: %w", err)
	}
	return delegation, cleanup, nil
}

func closeCgroupDelegation(delegation string) error {
	processes, err := readWords(filepath.Join(delegation, "cgroup.procs"))
	if err != nil {
		return fmt.Errorf("read delegated cgroup processes: %w", err)
	}
	if len(processes) != 0 {
		return fmt.Errorf("runner left the delegated cgroup populated: %v", processes)
	}
	entries, err := os.ReadDir(delegation)
	if err != nil {
		return fmt.Errorf("read delegated cgroup: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return fmt.Errorf("runner left a child cgroup behind: %s", entry.Name())
		}
	}
	return nil
}

func runPrivilegedSuite(suite privilegedSuite, delegation string, stdout io.Writer) error {
	arguments := []string{
		"-test.run=" + testExpression(suite.names),
		"-test.v", "-test.count=1", "-test.timeout=" + suite.timeout,
	}
	environment := map[string]string{requiredEnvironment: "1"}
	for key, value := range suite.environment {
		environment[key] = value
	}
	var command *exec.Cmd
	if suite.delegated {
		coordinator, err := os.Executable()
		if err != nil {
			return fmt.Errorf("resolve privileged test coordinator: %w", err)
		}
		entryArguments := append([]string{"--cgroup-entry", delegation, suite.binary}, arguments...)
		command = exec.Command(coordinator, entryArguments...)
	} else {
		command = exec.Command(suite.binary, arguments...)
	}
	command.Env = replaceEnvironment(os.Environ(), environment)
	output, err := command.CombinedOutput()
	_, _ = stdout.Write(output)
	if err != nil {
		return fmt.Errorf("privileged test binary %s failed: %w", filepath.Base(suite.binary), err)
	}
	if err := validateTestOutput(output, suite.names); err != nil {
		return fmt.Errorf("validate %s output: %w", filepath.Base(suite.binary), err)
	}
	return nil
}

func cgroupEntry(delegation, binary string, arguments []string) error {
	if os.Getenv(containerEnvironment) != "1" {
		return errors.New("cgroup entry requires its container marker")
	}
	cleanDelegation := filepath.Clean(delegation)
	if cleanDelegation != delegation || !strings.HasPrefix(cleanDelegation, cgroupRoot+string(filepath.Separator)) {
		return errors.New("delegated cgroup path is invalid")
	}
	if !filepath.IsAbs(binary) || len(arguments) == 0 {
		return errors.New("cgroup entry requires an absolute binary and test arguments")
	}
	pid := strconv.Itoa(os.Getpid()) + "\n"
	if err := os.WriteFile(filepath.Join(delegation, "cgroup.procs"), []byte(pid), 0); err != nil {
		return fmt.Errorf("enter delegated cgroup: %w", err)
	}
	environment := replaceEnvironment(os.Environ(), map[string]string{requiredEnvironment: "1"})
	return syscall.Exec(binary, append([]string{binary}, arguments...), environment)
}

func readWords(path string) ([]string, error) {
	content, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return strings.Fields(string(content)), nil
}
