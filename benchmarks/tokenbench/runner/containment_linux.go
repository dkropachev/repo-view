//go:build linux

package runner

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/dkropachev/repo-view/benchmarks/tokenbench/harness"
	"golang.org/x/sys/unix"
)

const (
	cgroupContainmentVersion = "tokenbench.cgroup-v2/v1"
	cgroupMountPath          = "/sys/fs/cgroup"
	hostCgroupName           = "tokenbench-host-v1"
	pairCgroupName           = "tokenbench-pair-v1"
	stableArmPathIdentity    = "exclusive-lease/" + pairCgroupName
	maximumAncestorPIDs      = uint64(4096)
	maximumAncestorMemory    = uint64(32 << 30)
	armMaximumPIDs           = uint64(256)
	armMaximumMemory         = uint64(8 << 30)
	armCPUQuotaMicros        = uint64(50_000)
	armCPUPeriodMicros       = uint64(100_000)
	maximumCgroupFileBytes   = 4096
	maximumRecoveryDepth     = 8
)

var requiredControllers = []string{"cpu", "memory", "pids"}

// cgroupManager owns an exclusive lease on the runner's delegated cgroup. It
// moves the runner into a host sibling so controllers can be enabled, then
// recreates the same direct pair child for every arm. This gives every launch
// fresh kernel accounting without exposing arm/order metadata in membership.
type cgroupManager struct {
	root              *os.Root
	lease             *os.File
	delegatedPath     string
	delegatedRelative string
	pairPath          string
	ancestorPIDs      uint64
	ancestorMemory    uint64
	inheritedControls []cgroupControlObservation
	resourceKeys      cgroupResourceCounterKeys
	requireBounded    bool
	cleanupTimeout    time.Duration

	mu                 sync.Mutex
	closed             bool
	controllersEnabled bool
	hostCreated        bool
	movedToHost        bool
	active             map[string]struct{}
	removeHost         func(string) error
}

type armCgroup struct {
	manager   *cgroupManager
	name      string
	directory *os.File

	mu        sync.Mutex
	cleanupMu sync.Mutex
	launched  bool
	cleaned   bool
	resources *harness.ResourceOutcome
}

type cgroupIdentity struct {
	Version                 string                     `json:"version"`
	AtomicCloneIntoCgroup   bool                       `json:"atomic_clone_into_cgroup"`
	KillEntireSubtree       bool                       `json:"kill_entire_subtree"`
	RequireBoundedAncestor  bool                       `json:"require_bounded_ancestor"`
	MaximumAncestorPIDs     uint64                     `json:"maximum_ancestor_pids"`
	MaximumAncestorMemory   uint64                     `json:"maximum_ancestor_memory_bytes"`
	ObservedAncestorPIDs    uint64                     `json:"observed_ancestor_pids"`
	ObservedAncestorMemory  uint64                     `json:"observed_ancestor_memory_bytes"`
	CleanupTimeoutNanos     int64                      `json:"cleanup_timeout_nanos"`
	StableArmPath           string                     `json:"stable_arm_path"`
	ExclusiveDelegationLock bool                       `json:"exclusive_delegation_lock"`
	StaleRecovery           bool                       `json:"stale_recovery"`
	ArmMaximumPIDs          uint64                     `json:"arm_maximum_pids"`
	ArmMaximumMemory        uint64                     `json:"arm_maximum_memory_bytes"`
	ArmMaximumSwap          uint64                     `json:"arm_maximum_swap_bytes"`
	ArmCPUQuotaMicros       uint64                     `json:"arm_cpu_quota_micros"`
	ArmCPUPeriodMicros      uint64                     `json:"arm_cpu_period_micros"`
	ArmOOMGroup             bool                       `json:"arm_oom_group"`
	ArmMaximumDepth         uint64                     `json:"arm_maximum_depth"`
	ArmMaximumDescendants   uint64                     `json:"arm_maximum_descendants"`
	InheritedControls       []cgroupControlObservation `json:"inherited_controls"`
	ResourceCounterKeys     cgroupResourceCounterKeys  `json:"resource_counter_keys"`
}

type cgroupControlObservation struct {
	Scope   string `json:"scope"`
	Name    string `json:"name"`
	Present bool   `json:"present"`
	Value   string `json:"value"`
}

type cgroupResourceCounterKeys struct {
	CPUStat           []string `json:"cpu_stat"`
	MemoryEvents      []string `json:"memory_events"`
	MemoryEventsLocal []string `json:"memory_events_local"`
	PIDsEvents        []string `json:"pids_events"`
}

var inheritedControlNames = []string{
	"cgroup.controllers",
	"cgroup.freeze",
	"cgroup.max.depth",
	"cgroup.max.descendants",
	"cgroup.subtree_control",
	"cgroup.type",
	"cpu.max",
	"cpu.max.burst",
	"cpu.uclamp.max",
	"cpu.uclamp.min",
	"cpu.weight",
	"cpu.weight.nice",
	"cpuset.cpus",
	"cpuset.cpus.effective",
	"cpuset.cpus.exclusive.effective",
	"cpuset.cpus.partition",
	"cpuset.mems",
	"cpuset.mems.effective",
	"io.max",
	"io.weight",
	"memory.high",
	"memory.low",
	"memory.max",
	"memory.min",
	"memory.oom.group",
	"memory.swap.high",
	"memory.swap.max",
	"pids.max",
}

func discoverCgroupManager(cleanup time.Duration, requireBounded bool) (_ *cgroupManager, resultErr error) {
	var filesystem unix.Statfs_t
	if err := unix.Statfs(cgroupMountPath, &filesystem); err != nil {
		return nil, fmt.Errorf("inspect cgroup-v2 filesystem: %w", err)
	}
	if filesystem.Type != unix.CGROUP2_SUPER_MAGIC {
		return nil, errors.New("runner requires a unified cgroup-v2 filesystem")
	}
	relative, err := currentCgroupPath()
	if err != nil {
		return nil, err
	}
	path := filepath.Clean(filepath.Join(cgroupMountPath, filepath.FromSlash(relative)))
	inside, err := filepath.Rel(cgroupMountPath, path)
	if err != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
		return nil, errors.New("current cgroup escaped the cgroup-v2 mount")
	}
	root, err := os.OpenRoot(path)
	if err != nil {
		return nil, fmt.Errorf("open current cgroup: %w", err)
	}
	lease, err := os.Open(path)
	if err != nil {
		_ = root.Close()
		return nil, fmt.Errorf("open cgroup delegation lease: %w", err)
	}
	if err := unix.Flock(int(lease.Fd()), unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = lease.Close()
		_ = root.Close()
		return nil, fmt.Errorf("acquire exclusive cgroup delegation lease: %w", err)
	}
	manager := &cgroupManager{
		root:              root,
		lease:             lease,
		delegatedPath:     path,
		delegatedRelative: relative,
		pairPath:          filepath.Join(path, pairCgroupName),
		requireBounded:    requireBounded,
		cleanupTimeout:    cleanup,
		active:            make(map[string]struct{}),
	}
	manager.removeHost = manager.root.Remove
	valid := false
	defer func() {
		if !valid {
			resultErr = errors.Join(resultErr, manager.close())
		}
	}()
	for _, name := range []string{
		"cgroup.controllers", "cgroup.events", "cgroup.kill", "cgroup.procs",
		"cgroup.subtree_control", "memory.max", "pids.max",
	} {
		if _, err := root.Stat(name); err != nil {
			return nil, fmt.Errorf("current cgroup lacks %s: %w", name, err)
		}
	}
	pids, err := readBound(root, "pids.max")
	if err != nil {
		return nil, fmt.Errorf("read ancestor pids.max: %w", err)
	}
	memory, err := readBound(root, "memory.max")
	if err != nil {
		return nil, fmt.Errorf("read ancestor memory.max: %w", err)
	}
	manager.ancestorPIDs = pids
	manager.ancestorMemory = memory
	if requireBounded {
		if pids == 0 || pids > maximumAncestorPIDs {
			return nil, fmt.Errorf(
				"runner cgroup pids.max must be finite and at most %d, got %s",
				maximumAncestorPIDs,
				formatBound(pids),
			)
		}
		if memory == 0 || memory > maximumAncestorMemory {
			return nil, fmt.Errorf(
				"runner cgroup memory.max must be finite and at most %d, got %s",
				maximumAncestorMemory,
				formatBound(memory),
			)
		}
	}
	if err := manager.recoverStaleChildren(); err != nil {
		return nil, err
	}
	if err := manager.disableControllers(); err != nil {
		return nil, err
	}
	if err := requireOnlySelf(root); err != nil {
		return nil, err
	}
	if err := root.Mkdir(hostCgroupName, 0o700); err != nil {
		return nil, fmt.Errorf("create runner host cgroup: %w", err)
	}
	manager.hostCreated = true
	if err := writeCgroupFile(
		root,
		filepath.ToSlash(filepath.Join(hostCgroupName, "cgroup.procs")),
		strconv.Itoa(os.Getpid())+"\n",
	); err != nil {
		_ = root.Remove(hostCgroupName)
		return nil, fmt.Errorf("move runner into host cgroup: %w", err)
	}
	manager.movedToHost = true
	hostRelative := filepath.ToSlash(filepath.Join(relative, hostCgroupName))
	if err := waitForOwnCgroup(hostRelative, cleanup); err != nil {
		return nil, err
	}
	if err := manager.enableControllers(); err != nil {
		return nil, err
	}
	manager.controllersEnabled = true
	manager.inheritedControls, err = manager.captureInheritedControls()
	if err != nil {
		return nil, fmt.Errorf("capture initial inherited cgroup controls: %w", err)
	}
	probe, err := manager.newArm()
	if err != nil {
		return nil, fmt.Errorf("create configured arm cgroup probe: %w", err)
	}
	if err := probe.killAndRemove(cleanup); err != nil {
		return nil, fmt.Errorf("remove configured arm cgroup probe: %w", err)
	}
	valid = true
	return manager, nil
}

func currentCgroupPath() (string, error) {
	content, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return "", fmt.Errorf("read current cgroup: %w", err)
	}
	if len(content) == 0 || len(content) > maximumCgroupFileBytes {
		return "", errors.New("current cgroup document is empty or oversized")
	}
	lines := strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
	if len(lines) != 1 || !strings.HasPrefix(lines[0], "0::/") {
		return "", errors.New("runner requires one canonical unified cgroup-v2 membership")
	}
	relative := strings.TrimPrefix(lines[0], "0::/")
	if relative == "" {
		return "", errors.New("runner may not use the cgroup-v2 filesystem root")
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(relative)))
	if clean != relative || clean == "." || strings.HasPrefix(clean, "../") {
		return "", errors.New("current cgroup path is not canonical")
	}
	return relative, nil
}

func waitForOwnCgroup(want string, timeout time.Duration) error {
	end := time.Now().Add(timeout)
	for {
		got, err := currentCgroupPath()
		if err == nil && got == want {
			return nil
		}
		if !time.Now().Before(end) {
			return fmt.Errorf("runner cgroup membership did not become %q (last %q: %v)", want, got, err)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func readBound(root *os.Root, name string) (uint64, error) {
	content, err := root.ReadFile(name)
	if err != nil {
		return 0, err
	}
	if len(content) == 0 || len(content) > 64 || content[len(content)-1] != '\n' {
		return 0, errors.New("cgroup bound is not canonical")
	}
	value := strings.TrimSuffix(string(content), "\n")
	if value == "max" {
		return 0, nil
	}
	parsed, err := strconv.ParseUint(value, 10, 64)
	if err != nil || parsed == 0 || strconv.FormatUint(parsed, 10) != value {
		return 0, errors.New("cgroup bound is not a positive canonical integer")
	}
	return parsed, nil
}

func formatBound(value uint64) string {
	if value == 0 {
		return "max"
	}
	return strconv.FormatUint(value, 10)
}

func (manager *cgroupManager) identity() cgroupIdentity {
	return cgroupIdentity{
		Version:                 cgroupContainmentVersion,
		AtomicCloneIntoCgroup:   true,
		KillEntireSubtree:       true,
		RequireBoundedAncestor:  manager.requireBounded,
		MaximumAncestorPIDs:     maximumAncestorPIDs,
		MaximumAncestorMemory:   maximumAncestorMemory,
		ObservedAncestorPIDs:    manager.ancestorPIDs,
		ObservedAncestorMemory:  manager.ancestorMemory,
		CleanupTimeoutNanos:     manager.cleanupTimeout.Nanoseconds(),
		StableArmPath:           stableArmPathIdentity,
		ExclusiveDelegationLock: true,
		StaleRecovery:           true,
		ArmMaximumPIDs:          armMaximumPIDs,
		ArmMaximumMemory:        armMaximumMemory,
		ArmMaximumSwap:          0,
		ArmCPUQuotaMicros:       armCPUQuotaMicros,
		ArmCPUPeriodMicros:      armCPUPeriodMicros,
		ArmOOMGroup:             true,
		ArmMaximumDepth:         0,
		ArmMaximumDescendants:   0,
		InheritedControls:       append([]cgroupControlObservation(nil), manager.inheritedControls...),
		ResourceCounterKeys:     cloneCgroupResourceCounterKeys(manager.resourceKeys),
	}
}

func cloneCgroupResourceCounterKeys(source cgroupResourceCounterKeys) cgroupResourceCounterKeys {
	return cgroupResourceCounterKeys{
		CPUStat:           append([]string(nil), source.CPUStat...),
		MemoryEvents:      append([]string(nil), source.MemoryEvents...),
		MemoryEventsLocal: append([]string(nil), source.MemoryEventsLocal...),
		PIDsEvents:        append([]string(nil), source.PIDsEvents...),
	}
}

func readCgroupWords(root *os.Root, name string) (map[string]struct{}, error) {
	content, err := root.ReadFile(name)
	if err != nil {
		return nil, err
	}
	if len(content) > maximumCgroupFileBytes ||
		len(content) != 0 && content[len(content)-1] != '\n' {
		return nil, errors.New("cgroup word list is oversized or noncanonical")
	}
	words := strings.Fields(string(content))
	result := make(map[string]struct{}, len(words))
	for _, word := range words {
		if _, duplicate := result[word]; duplicate {
			return nil, errors.New("cgroup word list contains a duplicate value")
		}
		result[word] = struct{}{}
	}
	return result, nil
}

func writeCgroupFile(root *os.Root, name, content string) error {
	file, err := root.OpenFile(name, os.O_WRONLY, 0)
	if err != nil {
		return err
	}
	_, writeErr := io.WriteString(file, content)
	return errors.Join(writeErr, file.Close())
}

func readCgroupProcesses(root *os.Root) ([]int, error) {
	content, err := root.ReadFile("cgroup.procs")
	if err != nil {
		return nil, err
	}
	if len(content) > maximumCgroupFileBytes {
		return nil, errors.New("cgroup.procs is oversized")
	}
	fields := strings.Fields(string(content))
	processes := make([]int, len(fields))
	for index, field := range fields {
		pid, err := strconv.Atoi(field)
		if err != nil || pid <= 0 || strconv.Itoa(pid) != field {
			return nil, errors.New("cgroup.procs contains a malformed process ID")
		}
		processes[index] = pid
	}
	return processes, nil
}

func requireOnlySelf(root *os.Root) error {
	processes, err := readCgroupProcesses(root)
	if err != nil {
		return fmt.Errorf("read delegated cgroup processes: %w", err)
	}
	if len(processes) != 1 || processes[0] != os.Getpid() {
		return fmt.Errorf(
			"delegated cgroup must contain only runner PID %d, got %v",
			os.Getpid(),
			processes,
		)
	}
	return nil
}

func (manager *cgroupManager) enableControllers() error {
	controllers, err := readCgroupWords(manager.root, "cgroup.controllers")
	if err != nil {
		return fmt.Errorf("read delegated cgroup controllers: %w", err)
	}
	for _, required := range requiredControllers {
		if _, ok := controllers[required]; !ok {
			return fmt.Errorf("delegated cgroup lacks required %s controller", required)
		}
	}
	if err := writeCgroupFile(manager.root, "cgroup.subtree_control", "+cpu +memory +pids\n"); err != nil {
		return fmt.Errorf("enable delegated cgroup controllers: %w", err)
	}
	manager.controllersEnabled = true
	enabled, err := readCgroupWords(manager.root, "cgroup.subtree_control")
	if err != nil {
		return fmt.Errorf("read delegated cgroup subtree control: %w", err)
	}
	for _, required := range requiredControllers {
		if _, ok := enabled[required]; !ok {
			return fmt.Errorf("delegated cgroup did not enable required %s controller", required)
		}
	}
	return nil
}

func (manager *cgroupManager) disableControllers() error {
	enabled, err := readCgroupWords(manager.root, "cgroup.subtree_control")
	if err != nil {
		return fmt.Errorf("read delegated cgroup subtree control: %w", err)
	}
	var disable []string
	for _, controller := range requiredControllers {
		if _, ok := enabled[controller]; ok {
			disable = append(disable, "-"+controller)
		}
	}
	if len(disable) == 0 {
		manager.controllersEnabled = false
		return nil
	}
	if err := writeCgroupFile(
		manager.root,
		"cgroup.subtree_control",
		strings.Join(disable, " ")+"\n",
	); err != nil {
		return fmt.Errorf("disable delegated cgroup controllers: %w", err)
	}
	manager.controllersEnabled = false
	return nil
}

func configureArmLimits(root *os.Root) error {
	for _, write := range armLimitValues() {
		if err := writeCgroupFile(root, write.name, write.value); err != nil {
			return fmt.Errorf("set arm cgroup %s: %w", write.name, err)
		}
	}
	return verifyArmLimitValues(root)
}

func armLimitValues() []struct {
	name  string
	value string
} {
	return []struct {
		name  string
		value string
	}{
		{"pids.max", strconv.FormatUint(armMaximumPIDs, 10) + "\n"},
		{"memory.max", strconv.FormatUint(armMaximumMemory, 10) + "\n"},
		{"memory.swap.max", "0\n"},
		{"memory.oom.group", "1\n"},
		{"cpu.max", fmt.Sprintf("%d %d\n", armCPUQuotaMicros, armCPUPeriodMicros)},
		{"cgroup.max.depth", "0\n"},
		{"cgroup.max.descendants", "0\n"},
	}
}

func verifyArmLimitValues(root *os.Root) error {
	for _, write := range armLimitValues() {
		readback, err := root.ReadFile(write.name)
		if err != nil {
			return fmt.Errorf("read back arm cgroup %s: %w", write.name, err)
		}
		if string(readback) != write.value {
			return fmt.Errorf(
				"arm cgroup %s readback %q, want %q",
				write.name,
				readback,
				write.value,
			)
		}
	}
	return nil
}

func (manager *cgroupManager) verifyPolicy() error {
	pids, err := readBound(manager.root, "pids.max")
	if err != nil || pids != manager.ancestorPIDs {
		return fmt.Errorf("ancestor pids.max drifted: got %s, want %s: %w", formatBound(pids), formatBound(manager.ancestorPIDs), err)
	}
	memory, err := readBound(manager.root, "memory.max")
	if err != nil || memory != manager.ancestorMemory {
		return fmt.Errorf("ancestor memory.max drifted: got %s, want %s: %w", formatBound(memory), formatBound(manager.ancestorMemory), err)
	}
	enabled, err := readCgroupWords(manager.root, "cgroup.subtree_control")
	if err != nil {
		return err
	}
	if len(enabled) != len(requiredControllers) {
		return errors.New("delegated cgroup controller set drifted")
	}
	for _, controller := range requiredControllers {
		if _, ok := enabled[controller]; !ok {
			return fmt.Errorf("delegated cgroup disabled %s controller", controller)
		}
	}
	observed, err := manager.captureInheritedControls()
	if err != nil {
		return fmt.Errorf("capture inherited cgroup controls: %w", err)
	}
	if len(observed) != len(manager.inheritedControls) {
		return errors.New("inherited cgroup control set drifted")
	}
	for index, got := range observed {
		want := manager.inheritedControls[index]
		if got != want {
			return fmt.Errorf(
				"inherited cgroup control drifted at %s/%s: present=%v value=%q, want present=%v value=%q",
				got.Scope,
				got.Name,
				got.Present,
				got.Value,
				want.Present,
				want.Value,
			)
		}
	}
	return nil
}

func (manager *cgroupManager) captureInheritedControls() ([]cgroupControlObservation, error) {
	host, err := manager.root.OpenRoot(hostCgroupName)
	if err != nil {
		return nil, fmt.Errorf("open runner host cgroup controls: %w", err)
	}
	defer host.Close()
	result := make([]cgroupControlObservation, 0, len(inheritedControlNames)*2)
	for _, scoped := range []struct {
		name string
		root *os.Root
	}{
		{name: "delegated", root: manager.root},
		{name: "host", root: host},
	} {
		for _, name := range inheritedControlNames {
			content, err := scoped.root.ReadFile(name)
			if errors.Is(err, os.ErrNotExist) {
				result = append(result, cgroupControlObservation{
					Scope: scoped.name, Name: name, Present: false,
				})
				continue
			}
			if err != nil {
				return nil, fmt.Errorf("read %s cgroup control %s: %w", scoped.name, name, err)
			}
			if len(content) > maximumCgroupFileBytes || bytes.IndexByte(content, 0) >= 0 {
				return nil, fmt.Errorf("%s cgroup control %s is oversized or contains NUL", scoped.name, name)
			}
			result = append(result, cgroupControlObservation{
				Scope: scoped.name, Name: name, Present: true, Value: string(content),
			})
		}
	}
	return result, nil
}

func (arm *armCgroup) verifyLimits() error {
	arm.mu.Lock()
	if arm.cleaned || arm.directory == nil {
		arm.mu.Unlock()
		return errors.New("arm cgroup is unavailable")
	}
	directoryInfo, err := arm.directory.Stat()
	arm.mu.Unlock()
	if err != nil {
		return err
	}
	root, err := arm.manager.root.OpenRoot(arm.name)
	if err != nil {
		return err
	}
	openedInfo, statErr := root.Stat(".")
	if statErr != nil || !os.SameFile(directoryInfo, openedInfo) {
		_ = root.Close()
		return errors.New("arm cgroup path changed after it was pinned")
	}
	verifyErr := verifyArmLimitValues(root)
	closeErr := root.Close()
	return errors.Join(verifyErr, closeErr)
}

func (manager *cgroupManager) recoverStaleChildren() error {
	directory, err := manager.root.Open(".")
	if err != nil {
		return fmt.Errorf("open delegated cgroup for recovery: %w", err)
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if readErr != nil || closeErr != nil {
		return errors.Join(readErr, closeErr)
	}
	deadline := time.Now().Add(manager.cleanupTimeout)
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		if !strings.HasPrefix(entry.Name(), "tokenbench-") {
			return fmt.Errorf("delegated cgroup contains foreign child %q", entry.Name())
		}
		if err := cleanupDetachedCgroup(manager.root, entry.Name(), deadline, 0); err != nil {
			return fmt.Errorf("recover stale cgroup %q: %w", entry.Name(), err)
		}
	}
	return nil
}

func cleanupDetachedCgroup(root *os.Root, name string, deadline time.Time, depth int) error {
	if depth > maximumRecoveryDepth {
		return errors.New("stale cgroup nesting exceeds recovery bound")
	}
	child, err := root.OpenRoot(name)
	if err != nil {
		return err
	}
	if empty, emptyErr := cgroupRootEmpty(child); emptyErr != nil {
		_ = child.Close()
		return emptyErr
	} else if !empty {
		if err := writeCgroupFile(child, "cgroup.kill", "1\n"); err != nil {
			_ = child.Close()
			return err
		}
	}
	for {
		empty, err := cgroupRootEmpty(child)
		if err != nil {
			_ = child.Close()
			return err
		}
		if empty {
			break
		}
		if !time.Now().Before(deadline) {
			_ = child.Close()
			return errors.New("stale cgroup remained populated after cgroup.kill")
		}
		time.Sleep(2 * time.Millisecond)
	}
	directory, err := child.Open(".")
	if err != nil {
		_ = child.Close()
		return err
	}
	entries, readErr := directory.ReadDir(-1)
	closeErr := directory.Close()
	if readErr != nil || closeErr != nil {
		_ = child.Close()
		return errors.Join(readErr, closeErr)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			if err := cleanupDetachedCgroup(child, entry.Name(), deadline, depth+1); err != nil {
				_ = child.Close()
				return err
			}
		}
	}
	if err := child.Close(); err != nil {
		return err
	}
	for {
		err := root.Remove(name)
		if err == nil || errors.Is(err, os.ErrNotExist) {
			return nil
		}
		if !time.Now().Before(deadline) {
			return err
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func (manager *cgroupManager) newArm() (*armCgroup, error) {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closed || !manager.movedToHost || !manager.controllersEnabled {
		return nil, errors.New("runner cgroup manager is unavailable")
	}
	if len(manager.active) != 0 {
		return nil, errors.New("runner permits only one active arm in its pair cgroup")
	}
	if err := manager.verifyPolicy(); err != nil {
		return nil, fmt.Errorf("verify inherited cgroup policy before arm creation: %w", err)
	}
	if err := manager.root.Mkdir(pairCgroupName, 0o700); err != nil {
		return nil, fmt.Errorf("create arm cgroup: %w", err)
	}
	armRoot, err := manager.root.OpenRoot(pairCgroupName)
	if err != nil {
		_ = manager.root.Remove(pairCgroupName)
		return nil, fmt.Errorf("open arm cgroup root: %w", err)
	}
	if err := configureArmLimits(armRoot); err != nil {
		_ = armRoot.Close()
		_ = manager.root.Remove(pairCgroupName)
		return nil, err
	}
	initialResources, err := captureCgroupResourceOutcome(armRoot)
	if err != nil {
		_ = armRoot.Close()
		_ = manager.root.Remove(pairCgroupName)
		return nil, fmt.Errorf("capture initial arm resource counters: %w", err)
	}
	keys := cgroupResourceKeys(initialResources)
	if len(manager.resourceKeys.CPUStat) == 0 {
		manager.resourceKeys = keys
	} else if err := requireCgroupResourceKeys(keys, manager.resourceKeys); err != nil {
		_ = armRoot.Close()
		_ = manager.root.Remove(pairCgroupName)
		return nil, err
	}
	if err := requireZeroInitialResources(initialResources); err != nil {
		_ = armRoot.Close()
		_ = manager.root.Remove(pairCgroupName)
		return nil, err
	}
	if err := armRoot.Close(); err != nil {
		_ = manager.root.Remove(pairCgroupName)
		return nil, fmt.Errorf("close configured arm cgroup root: %w", err)
	}
	directory, err := manager.root.Open(pairCgroupName)
	if err != nil {
		_ = manager.root.Remove(pairCgroupName)
		return nil, fmt.Errorf("open arm cgroup: %w", err)
	}
	manager.active[pairCgroupName] = struct{}{}
	return &armCgroup{manager: manager, name: pairCgroupName, directory: directory}, nil
}

func (arm *armCgroup) killAndRemove(deadline time.Duration) error {
	arm.cleanupMu.Lock()
	defer arm.cleanupMu.Unlock()
	arm.mu.Lock()
	if arm.cleaned {
		arm.mu.Unlock()
		return nil
	}
	arm.mu.Unlock()
	if deadline <= 0 || deadline > arm.manager.cleanupTimeout {
		deadline = arm.manager.cleanupTimeout
	}
	end := time.Now().Add(deadline)
	if err := arm.manager.verifyPolicy(); err != nil {
		return fmt.Errorf("verify ancestor cgroup policy before cleanup: %w", err)
	}
	if err := arm.verifyLimits(); err != nil {
		return fmt.Errorf("verify arm cgroup policy before cleanup: %w", err)
	}
	empty, err := arm.isEmpty()
	if err != nil {
		return err
	}
	if !empty {
		if err := arm.writeControl("cgroup.kill", "1\n"); err != nil {
			return fmt.Errorf("kill arm cgroup: %w", err)
		}
	}
	for {
		empty, err := arm.isEmpty()
		if err != nil {
			return err
		}
		if empty {
			break
		}
		if !time.Now().Before(end) {
			return errors.New("arm cgroup remained populated after cgroup.kill")
		}
		time.Sleep(2 * time.Millisecond)
	}
	if err := arm.manager.verifyPolicy(); err != nil {
		return fmt.Errorf("verify ancestor cgroup policy after cleanup: %w", err)
	}
	if err := arm.verifyLimits(); err != nil {
		return fmt.Errorf("verify arm cgroup policy after cleanup: %w", err)
	}
	resources, err := arm.captureResources()
	if err != nil {
		return fmt.Errorf("capture final arm resource counters: %w", err)
	}
	arm.mu.Lock()
	arm.resources = harness.CloneResourceOutcome(resources)
	arm.mu.Unlock()
	arm.mu.Lock()
	if arm.directory != nil {
		if err := arm.directory.Close(); err != nil {
			arm.mu.Unlock()
			return fmt.Errorf("close arm cgroup: %w", err)
		}
		arm.directory = nil
	}
	arm.mu.Unlock()
	for {
		err := arm.manager.root.Remove(arm.name)
		if err == nil {
			break
		}
		if !time.Now().Before(end) {
			return fmt.Errorf("remove empty arm cgroup: %w", err)
		}
		time.Sleep(2 * time.Millisecond)
	}
	arm.manager.mu.Lock()
	delete(arm.manager.active, arm.name)
	arm.manager.mu.Unlock()
	arm.mu.Lock()
	arm.cleaned = true
	arm.mu.Unlock()
	return nil
}

func (arm *armCgroup) resourceOutcome() *harness.ResourceOutcome {
	arm.mu.Lock()
	defer arm.mu.Unlock()
	return harness.CloneResourceOutcome(arm.resources)
}

func (arm *armCgroup) captureResources() (*harness.ResourceOutcome, error) {
	arm.mu.Lock()
	if arm.directory == nil {
		arm.mu.Unlock()
		return nil, errors.New("arm cgroup is unavailable")
	}
	directoryInfo, err := arm.directory.Stat()
	arm.mu.Unlock()
	if err != nil {
		return nil, err
	}
	root, err := arm.manager.root.OpenRoot(arm.name)
	if err != nil {
		return nil, err
	}
	openedInfo, statErr := root.Stat(".")
	if statErr != nil || !os.SameFile(directoryInfo, openedInfo) {
		_ = root.Close()
		return nil, errors.New("arm cgroup path changed before resource capture")
	}
	resources, captureErr := captureCgroupResourceOutcome(root)
	closeErr := root.Close()
	if captureErr == nil {
		captureErr = requireCgroupResourceKeys(
			cgroupResourceKeys(resources),
			arm.manager.resourceKeys,
		)
	}
	return resources, errors.Join(captureErr, closeErr)
}

func captureCgroupResourceOutcome(root *os.Root) (*harness.ResourceOutcome, error) {
	cpu, err := readCgroupCounters(root, "cpu.stat")
	if err != nil {
		return nil, err
	}
	memory, err := readCgroupCounters(root, "memory.events")
	if err != nil {
		return nil, err
	}
	memoryLocal, err := readCgroupCounters(root, "memory.events.local")
	if err != nil {
		return nil, err
	}
	pids, err := readCgroupCounters(root, "pids.events")
	if err != nil {
		return nil, err
	}
	memoryCurrent, err := readCgroupScalar(root, "memory.current")
	if err != nil {
		return nil, err
	}
	memoryPeak, err := readCgroupScalar(root, "memory.peak")
	if err != nil {
		return nil, err
	}
	pidsCurrent, err := readCgroupScalar(root, "pids.current")
	if err != nil {
		return nil, err
	}
	pidsPeak, err := readCgroupScalar(root, "pids.peak")
	if err != nil {
		return nil, err
	}
	result := &harness.ResourceOutcome{
		Version:            harness.ResourceOutcomeVersion,
		CPUStat:            cpu,
		MemoryEvents:       memory,
		MemoryEventsLocal:  memoryLocal,
		PIDsEvents:         pids,
		MemoryCurrentBytes: memoryCurrent,
		MemoryPeakBytes:    memoryPeak,
		PIDsCurrent:        pidsCurrent,
		PIDsPeak:           pidsPeak,
	}
	if err := harness.ValidateResourceOutcome(result); err != nil {
		return nil, err
	}
	return result, nil
}

func readCgroupCounters(root *os.Root, name string) ([]harness.ResourceCounter, error) {
	content, err := root.ReadFile(name)
	if err != nil {
		return nil, fmt.Errorf("read cgroup resource counter %s: %w", name, err)
	}
	if len(content) == 0 || len(content) > maximumCgroupFileBytes ||
		content[len(content)-1] != '\n' || bytes.IndexByte(content, 0) >= 0 {
		return nil, fmt.Errorf("cgroup resource counter %s is noncanonical", name)
	}
	lines := strings.Split(strings.TrimSuffix(string(content), "\n"), "\n")
	result := make([]harness.ResourceCounter, 0, len(lines))
	seen := make(map[string]struct{}, len(lines))
	for _, line := range lines {
		fields := strings.Fields(line)
		if len(fields) != 2 || strings.Join(fields, " ") != line {
			return nil, fmt.Errorf("cgroup resource counter %s has a malformed line", name)
		}
		if _, duplicate := seen[fields[0]]; duplicate {
			return nil, fmt.Errorf("cgroup resource counter %s has duplicate key %s", name, fields[0])
		}
		value, err := strconv.ParseUint(fields[1], 10, 64)
		if err != nil || strconv.FormatUint(value, 10) != fields[1] {
			return nil, fmt.Errorf("cgroup resource counter %s has invalid value for %s", name, fields[0])
		}
		seen[fields[0]] = struct{}{}
		result = append(result, harness.ResourceCounter{Name: fields[0], Value: value})
	}
	sort.Slice(result, func(left, right int) bool {
		return result[left].Name < result[right].Name
	})
	return result, nil
}

func readCgroupScalar(root *os.Root, name string) (uint64, error) {
	content, err := root.ReadFile(name)
	if err != nil {
		return 0, fmt.Errorf("read cgroup resource scalar %s: %w", name, err)
	}
	if len(content) < 2 || len(content) > 64 || content[len(content)-1] != '\n' {
		return 0, fmt.Errorf("cgroup resource scalar %s is noncanonical", name)
	}
	text := strings.TrimSuffix(string(content), "\n")
	value, err := strconv.ParseUint(text, 10, 64)
	if err != nil || strconv.FormatUint(value, 10) != text {
		return 0, fmt.Errorf("cgroup resource scalar %s is invalid", name)
	}
	return value, nil
}

func cgroupResourceKeys(outcome *harness.ResourceOutcome) cgroupResourceCounterKeys {
	names := func(counters []harness.ResourceCounter) []string {
		result := make([]string, len(counters))
		for index, counter := range counters {
			result[index] = counter.Name
		}
		return result
	}
	return cgroupResourceCounterKeys{
		CPUStat:           names(outcome.CPUStat),
		MemoryEvents:      names(outcome.MemoryEvents),
		MemoryEventsLocal: names(outcome.MemoryEventsLocal),
		PIDsEvents:        names(outcome.PIDsEvents),
	}
}

func requireCgroupResourceKeys(got, want cgroupResourceCounterKeys) error {
	for _, comparison := range []struct {
		name      string
		got, want []string
	}{
		{"cpu.stat", got.CPUStat, want.CPUStat},
		{"memory.events", got.MemoryEvents, want.MemoryEvents},
		{"memory.events.local", got.MemoryEventsLocal, want.MemoryEventsLocal},
		{"pids.events", got.PIDsEvents, want.PIDsEvents},
	} {
		if !slices.Equal(comparison.got, comparison.want) {
			return fmt.Errorf(
				"cgroup resource counter keys drifted for %s: got %v, want %v",
				comparison.name,
				comparison.got,
				comparison.want,
			)
		}
	}
	return nil
}

func requireZeroInitialResources(outcome *harness.ResourceOutcome) error {
	for _, counters := range [][]harness.ResourceCounter{
		outcome.CPUStat,
		outcome.MemoryEvents,
		outcome.MemoryEventsLocal,
		outcome.PIDsEvents,
	} {
		for _, counter := range counters {
			if counter.Value != 0 {
				return fmt.Errorf("fresh arm cgroup counter %s started at %d", counter.Name, counter.Value)
			}
		}
	}
	if outcome.MemoryCurrentBytes != 0 || outcome.MemoryPeakBytes != 0 ||
		outcome.PIDsCurrent != 0 || outcome.PIDsPeak != 0 {
		return fmt.Errorf("fresh arm cgroup scalar resources are nonzero: %+v", outcome)
	}
	return nil
}

func (arm *armCgroup) writeControl(name, content string) error {
	return writeCgroupFile(
		arm.manager.root,
		filepath.ToSlash(filepath.Join(arm.name, name)),
		content,
	)
}

func (arm *armCgroup) isEmpty() (bool, error) {
	root, err := arm.manager.root.OpenRoot(arm.name)
	if err != nil {
		return false, err
	}
	empty, emptyErr := cgroupRootEmpty(root)
	closeErr := root.Close()
	return empty, errors.Join(emptyErr, closeErr)
}

func cgroupRootEmpty(root *os.Root) (bool, error) {
	events, err := root.ReadFile("cgroup.events")
	if err != nil {
		return false, fmt.Errorf("read cgroup events: %w", err)
	}
	if len(events) == 0 || len(events) > maximumCgroupFileBytes ||
		events[len(events)-1] != '\n' {
		return false, errors.New("cgroup events are empty, oversized, or noncanonical")
	}
	populated := -1
	for _, line := range strings.Split(strings.TrimSuffix(string(events), "\n"), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 {
			return false, errors.New("cgroup events are malformed")
		}
		if fields[0] == "populated" {
			if populated != -1 || fields[1] != "0" && fields[1] != "1" {
				return false, errors.New("cgroup populated event is malformed")
			}
			populated, _ = strconv.Atoi(fields[1])
		}
	}
	if populated == -1 {
		return false, errors.New("cgroup omitted its populated event")
	}
	if populated != 0 {
		return false, nil
	}
	processes, err := readCgroupProcesses(root)
	if err != nil {
		return false, fmt.Errorf("read cgroup processes: %w", err)
	}
	return len(processes) == 0, nil
}

func (manager *cgroupManager) close() error {
	manager.mu.Lock()
	defer manager.mu.Unlock()
	if manager.closed {
		return nil
	}
	if len(manager.active) != 0 {
		return errors.New("cannot close cgroup manager with active arm cgroups")
	}
	if manager.controllersEnabled {
		if err := manager.disableControllers(); err != nil {
			return err
		}
	}
	if manager.movedToHost {
		if err := writeCgroupFile(
			manager.root,
			"cgroup.procs",
			strconv.Itoa(os.Getpid())+"\n",
		); err != nil {
			return fmt.Errorf("restore runner to delegated cgroup: %w", err)
		}
		if err := waitForOwnCgroup(manager.delegatedRelative, manager.cleanupTimeout); err != nil {
			return err
		}
		manager.movedToHost = false
	}
	if manager.hostCreated {
		if err := manager.removeHost(hostCgroupName); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove runner host cgroup: %w", err)
		}
		manager.hostCreated = false
	}
	if manager.lease != nil {
		unlockErr := unix.Flock(int(manager.lease.Fd()), unix.LOCK_UN)
		closeErr := manager.lease.Close()
		if unlockErr != nil || closeErr != nil {
			return errors.Join(unlockErr, closeErr)
		}
		manager.lease = nil
	}
	if err := manager.root.Close(); err != nil {
		return err
	}
	manager.closed = true
	return nil
}

func processContainmentSupported() bool { return true }

func isolateCommand(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pdeathsig: syscall.SIGKILL}
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
}

func cleanupCommandGroup(command *exec.Cmd) {
	if command.Process != nil {
		_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
	}
}

func configureContainedCommand(command *exec.Cmd, arm *armCgroup) error {
	if arm == nil {
		return errors.New("arm cgroup is required")
	}
	if err := arm.manager.verifyPolicy(); err != nil {
		return fmt.Errorf("verify ancestor cgroup policy before launch: %w", err)
	}
	if err := arm.verifyLimits(); err != nil {
		return fmt.Errorf("verify arm cgroup policy before launch: %w", err)
	}
	arm.mu.Lock()
	defer arm.mu.Unlock()
	if arm.cleaned || arm.launched || arm.directory == nil {
		return errors.New("arm cgroup is unavailable or already consumed")
	}
	command.SysProcAttr = &syscall.SysProcAttr{
		Setpgid:     true,
		Pdeathsig:   syscall.SIGKILL,
		UseCgroupFD: true,
		CgroupFD:    int(arm.directory.Fd()),
	}
	command.Cancel = func() error {
		if command.Process == nil {
			return nil
		}
		err := syscall.Kill(-command.Process.Pid, syscall.SIGTERM)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		return err
	}
	arm.launched = true
	return nil
}
