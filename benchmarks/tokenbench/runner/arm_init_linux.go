//go:build linux

package runner

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	armInitVersion             = "tokenbench.arm-init/v1"
	armInitMarkerEnvironment   = "TOKENBENCH_INTERNAL_ARM_INIT_V1"
	armInitFDLayoutEnvironment = "TOKENBENCH_INTERNAL_FD_LAYOUT_V1"
	armInitProbeEnvironment    = "TOKENBENCH_INTERNAL_ARM_PROBE_V1"
	armInitSeccompPolicy       = "deny-inspection-namespace-kernel-unix-ioring/v2"
	armInitTargetFD            = 4
	commonMCPExecutableFD      = 5
	armInitDevNullRuleFD       = 6
	firstWritableFD            = 7
	minimumLandlockABI         = 5
)

const landlockHandledWriteAccess = uint64(
	unix.LANDLOCK_ACCESS_FS_WRITE_FILE |
		unix.LANDLOCK_ACCESS_FS_REMOVE_DIR |
		unix.LANDLOCK_ACCESS_FS_REMOVE_FILE |
		unix.LANDLOCK_ACCESS_FS_MAKE_CHAR |
		unix.LANDLOCK_ACCESS_FS_MAKE_DIR |
		unix.LANDLOCK_ACCESS_FS_MAKE_REG |
		unix.LANDLOCK_ACCESS_FS_MAKE_SOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_FIFO |
		unix.LANDLOCK_ACCESS_FS_MAKE_BLOCK |
		unix.LANDLOCK_ACCESS_FS_MAKE_SYM |
		unix.LANDLOCK_ACCESS_FS_REFER |
		unix.LANDLOCK_ACCESS_FS_TRUNCATE,
)

const landlockHandledReadExecuteAccess = uint64(
	unix.LANDLOCK_ACCESS_FS_EXECUTE |
		unix.LANDLOCK_ACCESS_FS_READ_FILE |
		unix.LANDLOCK_ACCESS_FS_READ_DIR,
)

const landlockHandledNetworkAccess = uint64(
	unix.LANDLOCK_ACCESS_NET_BIND_TCP | unix.LANDLOCK_ACCESS_NET_CONNECT_TCP,
)

const landlockRuleNetPort = 2

type landlockRulesetAttr struct {
	HandledAccessFS  uint64
	HandledAccessNet uint64
}

type landlockNetPortAttr struct {
	AllowedAccess uint64
	Port          uint64
}

type armInitLayout struct {
	writableFirst   int
	writableCount   int
	readOnlyFirst   int
	readOnlyCount   int
	executableFirst int
	executableCount int
	connectPorts    []uint16
	bindPorts       []uint16
}

func formatArmInitFDLayout(
	writableCount, readOnlyCount, executableCount int,
	connectPorts, bindPorts []uint16,
) string {
	readOnlyFirst := firstWritableFD + writableCount
	executableFirst := readOnlyFirst + readOnlyCount
	return fmt.Sprintf(
		"target=%d;common=%d;devnull=%d;writable=%d+%d;readonly=%d+%d;executable=%d+%d;connect=%s;bind=%s",
		armInitTargetFD,
		commonMCPExecutableFD,
		armInitDevNullRuleFD,
		firstWritableFD,
		writableCount,
		readOnlyFirst,
		readOnlyCount,
		executableFirst,
		executableCount,
		formatArmInitPorts(connectPorts),
		formatArmInitPorts(bindPorts),
	)
}

func formatArmInitPorts(ports []uint16) string {
	if len(ports) == 0 {
		return "-"
	}
	values := make([]string, len(ports))
	for index, port := range ports {
		values[index] = strconv.FormatUint(uint64(port), 10)
	}
	return strings.Join(values, ",")
}

func parseArmInitFDLayout(value string) (armInitLayout, error) {
	fields := strings.Split(value, ";")
	if len(fields) != 8 || fields[0] != "target="+strconv.Itoa(armInitTargetFD) ||
		fields[1] != "common="+strconv.Itoa(commonMCPExecutableFD) ||
		fields[2] != "devnull="+strconv.Itoa(armInitDevNullRuleFD) {
		return armInitLayout{}, errors.New("invalid arm-init fixed descriptor layout")
	}
	writableFirst, writableCount, err := parseArmInitSpan(fields[3], "writable", firstWritableFD)
	if err != nil {
		return armInitLayout{}, err
	}
	readOnlyFirst, readOnlyCount, err := parseArmInitSpan(
		fields[4],
		"readonly",
		writableFirst+writableCount,
	)
	if err != nil {
		return armInitLayout{}, err
	}
	executableFirst, executableCount, err := parseArmInitSpan(
		fields[5],
		"executable",
		readOnlyFirst+readOnlyCount,
	)
	if err != nil {
		return armInitLayout{}, err
	}
	if executableFirst+executableCount > firstWritableFD+192 {
		return armInitLayout{}, errors.New("arm-init descriptor layout is oversized")
	}
	connect, err := parseArmInitPorts(fields[6], "connect")
	if err != nil {
		return armInitLayout{}, err
	}
	bind, err := parseArmInitPorts(fields[7], "bind")
	if err != nil {
		return armInitLayout{}, err
	}
	return armInitLayout{
		writableFirst: writableFirst, writableCount: writableCount,
		readOnlyFirst: readOnlyFirst, readOnlyCount: readOnlyCount,
		executableFirst: executableFirst, executableCount: executableCount,
		connectPorts: connect, bindPorts: bind,
	}, nil
}

func parseArmInitSpan(field, name string, expectedFirst int) (int, int, error) {
	prefix := name + "="
	span, ok := strings.CutPrefix(field, prefix)
	if !ok {
		return 0, 0, fmt.Errorf("arm-init layout omitted %s descriptor span", name)
	}
	firstText, countText, ok := strings.Cut(span, "+")
	first, firstErr := strconv.Atoi(firstText)
	count, countErr := strconv.Atoi(countText)
	if !ok || firstErr != nil || countErr != nil || first != expectedFirst || count < 0 ||
		count > 64 || strconv.Itoa(first) != firstText || strconv.Itoa(count) != countText {
		return 0, 0, fmt.Errorf("invalid arm-init %s descriptor span", name)
	}
	return first, count, nil
}

func parseArmInitPorts(field, name string) ([]uint16, error) {
	value, ok := strings.CutPrefix(field, name+"=")
	if !ok {
		return nil, fmt.Errorf("arm-init layout omitted %s ports", name)
	}
	if value == "-" {
		return []uint16{}, nil
	}
	parts := strings.Split(value, ",")
	if len(parts) == 0 || len(parts) > 16 {
		return nil, fmt.Errorf("arm-init %s port list is empty or oversized", name)
	}
	ports := make([]uint16, len(parts))
	for index, part := range parts {
		port, err := strconv.ParseUint(part, 10, 16)
		if err != nil || port == 0 || strconv.FormatUint(port, 10) != part ||
			index != 0 && uint64(ports[index-1]) >= port {
			return nil, fmt.Errorf("arm-init %s ports are not canonical", name)
		}
		ports[index] = uint16(port)
	}
	return ports, nil
}

type landlockPathBeneathAttr struct {
	AllowedAccess uint64
	ParentFD      int32
	Reserved      uint32
}

// init turns the runner-containing executable into a code-owned arm-init
// without depending on a mutable helper pathname. Normal process startup is
// untouched because the marker is inserted only by Executor.run and removed
// before the approved target is executed.
func init() {
	if os.Getenv(armInitProbeEnvironment) == armInitVersion &&
		os.Getenv(armInitMarkerEnvironment) == "" {
		if err := runArmInitProbe(); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "tokenbench arm-init probe: %v\n", err)
			os.Exit(125)
		}
		_, _ = fmt.Fprint(
			os.Stdout,
			armInitVersion+":atomic-cgroup+landlock+no-new-privs+target-dumpable+seccomp\n",
		)
		os.Exit(0)
	}
	if os.Getenv(armInitMarkerEnvironment) != armInitVersion {
		return
	}
	if err := runArmInit(); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "tokenbench arm-init: %v\n", err)
		os.Exit(125)
	}
}

func runArmInitProbe() error {
	membership, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return err
	}
	if !strings.HasSuffix(string(membership), "/"+pairCgroupName+"\n") {
		return fmt.Errorf("probe membership is not the pair cgroup: %q", membership)
	}
	noNewPrivileges, err := unix.PrctlRetInt(unix.PR_GET_NO_NEW_PRIVS, 0, 0, 0, 0)
	if err != nil || noNewPrivileges != 1 {
		return fmt.Errorf("probe did not retain no_new_privs: value=%d err=%v", noNewPrivileges, err)
	}
	dumpable, err := unix.PrctlRetInt(unix.PR_GET_DUMPABLE, 0, 0, 0, 0)
	if err != nil || dumpable != 1 {
		return fmt.Errorf("probe target dumpability: value=%d err=%v", dumpable, err)
	}
	status, err := os.ReadFile("/proc/self/status")
	if err != nil {
		return err
	}
	if !strings.Contains(string(status), "Seccomp:\t2\n") {
		return errors.New("probe target did not retain seccomp filter mode")
	}
	_, _, ptraceErr := unix.RawSyscall(unix.SYS_PTRACE, unix.PTRACE_TRACEME, 0, 0)
	if ptraceErr != unix.EPERM {
		return fmt.Errorf("probe ptrace syscall was not filtered: %v", ptraceErr)
	}
	unixSocket, socketErr := unix.Socket(unix.AF_UNIX, unix.SOCK_STREAM|unix.SOCK_CLOEXEC, 0)
	if unixSocket >= 0 {
		_ = unix.Close(unixSocket)
	}
	if socketErr != unix.EPERM {
		return fmt.Errorf("probe AF_UNIX socket was not filtered: %v", socketErr)
	}
	return nil
}

func runArmInit() error {
	layout, err := parseArmInitFDLayout(os.Getenv(armInitFDLayoutEnvironment))
	if err != nil {
		return err
	}
	if err := os.Unsetenv(armInitMarkerEnvironment); err != nil {
		return err
	}
	if err := os.Unsetenv(armInitFDLayoutEnvironment); err != nil {
		return err
	}
	if err := validateArmInitFDLayout(layout); err != nil {
		return err
	}
	membership, err := os.ReadFile("/proc/self/cgroup")
	if err != nil {
		return fmt.Errorf("read arm cgroup membership: %w", err)
	}
	if !strings.HasSuffix(string(membership), "/"+pairCgroupName+"\n") {
		return fmt.Errorf("arm-init did not start in the fixed pair cgroup: %q", membership)
	}
	if err := unix.Prctl(unix.PR_SET_NO_NEW_PRIVS, 1, 0, 0, 0); err != nil {
		return fmt.Errorf("set no_new_privs: %w", err)
	}
	if err := unix.Prctl(unix.PR_SET_DUMPABLE, 0, 0, 0, 0); err != nil {
		return fmt.Errorf("disable arm dumpability: %w", err)
	}
	abi, err := landlockABI()
	if err != nil {
		return err
	}
	if abi < minimumLandlockABI {
		return fmt.Errorf("Landlock ABI %d is below required ABI %d", abi, minimumLandlockABI)
	}
	if err := restrictArmAccess(layout); err != nil {
		return err
	}
	if err := restrictProcessInspection(); err != nil {
		return err
	}
	if err := unix.CloseRange(3, ^uint(0), unix.CLOSE_RANGE_CLOEXEC); err != nil {
		return fmt.Errorf("close inherited descriptor range on exec: %w", err)
	}
	if _, err := unix.FcntlInt(uintptr(commonMCPExecutableFD), unix.F_SETFD, 0); err != nil {
		return fmt.Errorf("retain common MCP FD5: %w", err)
	}
	if err := verifyExecDescriptorClosure(); err != nil {
		return err
	}
	// FD5 deliberately remains non-CLOEXEC. Both arms receive the same opened,
	// read-only repo-view inode; only the candidate references it as MCP command.
	return unix.Exec(
		fmt.Sprintf("/proc/self/fd/%d", armInitTargetFD),
		os.Args,
		os.Environ(),
	)
}

func restrictProcessInspection() error {
	filters := []unix.SockFilter{{
		Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS,
		K:    0, // offsetof(struct seccomp_data, nr)
	}}
	for _, number := range []uint32{
		uint32(unix.SYS_PTRACE),
		uint32(unix.SYS_PROCESS_VM_READV),
		uint32(unix.SYS_PROCESS_VM_WRITEV),
		uint32(unix.SYS_KCMP),
		uint32(unix.SYS_BPF),
		uint32(unix.SYS_PERF_EVENT_OPEN),
		uint32(unix.SYS_USERFAULTFD),
		uint32(unix.SYS_OPEN_BY_HANDLE_AT),
		uint32(unix.SYS_MOUNT),
		uint32(unix.SYS_UMOUNT2),
		uint32(unix.SYS_PIVOT_ROOT),
		uint32(unix.SYS_UNSHARE),
		uint32(unix.SYS_SETNS),
		uint32(unix.SYS_OPEN_TREE),
		uint32(unix.SYS_MOVE_MOUNT),
		uint32(unix.SYS_FSOPEN),
		uint32(unix.SYS_FSCONFIG),
		uint32(unix.SYS_FSMOUNT),
		uint32(unix.SYS_FSPICK),
		uint32(unix.SYS_MOUNT_SETATTR),
		uint32(unix.SYS_IO_URING_SETUP),
		uint32(unix.SYS_IO_URING_ENTER),
		uint32(unix.SYS_IO_URING_REGISTER),
		uint32(unix.SYS_SOCKETPAIR),
		uint32(unix.SYS_BIND),
	} {
		filters = append(filters,
			unix.SockFilter{
				Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K,
				Jf:   1,
				K:    number,
			},
			unix.SockFilter{
				Code: unix.BPF_RET | unix.BPF_K,
				K:    unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM),
			},
		)
	}
	// clone3 carries flags behind a pointer, which classic seccomp cannot
	// inspect. Return ENOSYS so libc safely falls back to inspectable clone.
	filters = append(filters,
		unix.SockFilter{
			Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K,
			Jf:   1,
			K:    uint32(unix.SYS_CLONE3),
		},
		unix.SockFilter{
			Code: unix.BPF_RET | unix.BPF_K,
			K:    unix.SECCOMP_RET_ERRNO | uint32(unix.ENOSYS),
		},
	)
	// Only TCP IPv4/IPv6 sockets are useful to a contained arm. This denies
	// raw/UDP/netlink sockets and both pathname and abstract AF_UNIX channels.
	filters = append(filters,
		unix.SockFilter{
			Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K,
			Jf:   8,
			K:    uint32(unix.SYS_SOCKET),
		},
		unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 16},
		unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jt: 2, K: unix.AF_INET},
		unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jt: 1, K: unix.AF_INET6},
		unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)},
		unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 24},
		unix.SockFilter{Code: unix.BPF_ALU | unix.BPF_AND | unix.BPF_K, K: 0xf},
		unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K, Jt: 1, K: unix.SOCK_STREAM},
		unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)},
		unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0},
	)
	const namespaceCloneFlags = uint32(
		unix.CLONE_NEWCGROUP |
			unix.CLONE_NEWIPC |
			unix.CLONE_NEWNET |
			unix.CLONE_NEWNS |
			unix.CLONE_NEWPID |
			unix.CLONE_NEWTIME |
			unix.CLONE_NEWUSER |
			unix.CLONE_NEWUTS,
	)
	filters = append(filters,
		unix.SockFilter{
			Code: unix.BPF_JMP | unix.BPF_JEQ | unix.BPF_K,
			Jf:   4,
			K:    uint32(unix.SYS_CLONE),
		},
		unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 16},
		unix.SockFilter{Code: unix.BPF_JMP | unix.BPF_JSET | unix.BPF_K, Jf: 1, K: namespaceCloneFlags},
		unix.SockFilter{Code: unix.BPF_RET | unix.BPF_K, K: unix.SECCOMP_RET_ERRNO | uint32(unix.EPERM)},
		unix.SockFilter{Code: unix.BPF_LD | unix.BPF_W | unix.BPF_ABS, K: 0},
	)
	filters = append(filters, unix.SockFilter{
		Code: unix.BPF_RET | unix.BPF_K,
		K:    unix.SECCOMP_RET_ALLOW,
	})
	program := unix.SockFprog{Len: uint16(len(filters)), Filter: &filters[0]}
	if err := unix.Prctl(
		unix.PR_SET_SECCOMP,
		unix.SECCOMP_MODE_FILTER,
		uintptr(unsafe.Pointer(&program)),
		0,
		0,
	); err != nil {
		return fmt.Errorf("install process-inspection seccomp filter: %w", err)
	}
	return nil
}

func verifyExecDescriptorClosure() error {
	for descriptor := 3; descriptor < 4096; descriptor++ {
		flags, err := unix.FcntlInt(uintptr(descriptor), unix.F_GETFD, 0)
		if errors.Is(err, unix.EBADF) {
			continue
		}
		if err != nil {
			return fmt.Errorf("inspect inherited FD %d: %w", descriptor, err)
		}
		if descriptor == commonMCPExecutableFD {
			if flags&unix.FD_CLOEXEC != 0 {
				return errors.New("common MCP FD5 is unexpectedly close-on-exec")
			}
			continue
		}
		if flags&unix.FD_CLOEXEC == 0 {
			return fmt.Errorf("unexpected inherited FD %d would survive target exec", descriptor)
		}
	}
	return nil
}

func validateArmInitFDLayout(layout armInitLayout) error {
	for _, descriptor := range []int{3, armInitTargetFD} {
		var stat unix.Stat_t
		if err := unix.Fstat(descriptor, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFREG ||
			stat.Mode&0o111 == 0 {
			return fmt.Errorf("arm-init executable FD %d is not an executable regular file", descriptor)
		}
	}
	flags, err := unix.FcntlInt(uintptr(commonMCPExecutableFD), unix.F_GETFL, 0)
	if err != nil || flags&unix.O_ACCMODE != unix.O_RDONLY {
		return errors.New("common MCP FD5 is not read-only")
	}
	var common unix.Stat_t
	if err := unix.Fstat(commonMCPExecutableFD, &common); err != nil {
		return fmt.Errorf("inspect common MCP FD5: %w", err)
	}
	commonType := common.Mode & unix.S_IFMT
	if commonType != unix.S_IFREG && commonType != unix.S_IFCHR {
		return errors.New("common MCP FD5 has an unsupported file type")
	}
	var devNull unix.Stat_t
	if err := unix.Fstat(armInitDevNullRuleFD, &devNull); err != nil ||
		devNull.Mode&unix.S_IFMT != unix.S_IFCHR {
		return errors.New("arm-init /dev/null rule FD6 is not a character device")
	}
	for descriptor := layout.writableFirst; descriptor < layout.writableFirst+layout.writableCount; descriptor++ {
		var stat unix.Stat_t
		if err := unix.Fstat(descriptor, &stat); err != nil || stat.Mode&unix.S_IFMT != unix.S_IFDIR {
			return fmt.Errorf("Landlock writable FD %d is not a directory", descriptor)
		}
	}
	for descriptor := layout.readOnlyFirst; descriptor < layout.readOnlyFirst+layout.readOnlyCount; descriptor++ {
		var stat unix.Stat_t
		kind := uint32(0)
		if err := unix.Fstat(descriptor, &stat); err == nil {
			kind = stat.Mode & unix.S_IFMT
		}
		if kind != unix.S_IFREG && kind != unix.S_IFDIR {
			return fmt.Errorf("Landlock read-only FD %d is not a file or directory", descriptor)
		}
	}
	for descriptor := layout.executableFirst; descriptor < layout.executableFirst+layout.executableCount; descriptor++ {
		var stat unix.Stat_t
		if err := unix.Fstat(descriptor, &stat); err != nil ||
			stat.Mode&unix.S_IFMT != unix.S_IFREG || stat.Mode&0o111 == 0 {
			return fmt.Errorf("Landlock executable FD %d is not an executable regular file", descriptor)
		}
	}
	return nil
}

func landlockABI() (int, error) {
	result, _, errno := unix.Syscall6(
		unix.SYS_LANDLOCK_CREATE_RULESET,
		0,
		0,
		unix.LANDLOCK_CREATE_RULESET_VERSION,
		0,
		0,
		0,
	)
	if errno != 0 {
		return 0, fmt.Errorf("query Landlock ABI: %w", errno)
	}
	if result == 0 || result > 1<<16 {
		return 0, errors.New("kernel returned an invalid Landlock ABI")
	}
	return int(result), nil
}

func restrictArmAccess(layout armInitLayout) error {
	fullFilesystemPolicy := layout.readOnlyCount != 0 || layout.executableCount != 0
	handledFilesystem := landlockHandledWriteAccess
	if fullFilesystemPolicy {
		handledFilesystem |= landlockHandledReadExecuteAccess
	}
	rulesetAttribute := landlockRulesetAttr{
		HandledAccessFS:  handledFilesystem,
		HandledAccessNet: landlockHandledNetworkAccess,
	}
	ruleset, _, errno := unix.Syscall6(
		unix.SYS_LANDLOCK_CREATE_RULESET,
		uintptr(unsafe.Pointer(&rulesetAttribute)),
		unsafe.Sizeof(rulesetAttribute),
		0,
		0,
		0,
		0,
	)
	if errno != 0 {
		return fmt.Errorf("create Landlock ruleset: %w", errno)
	}
	rulesetFD := int(ruleset)
	defer unix.Close(rulesetFD)
	devNullAccess := uint64(unix.LANDLOCK_ACCESS_FS_WRITE_FILE)
	if fullFilesystemPolicy {
		devNullAccess |= unix.LANDLOCK_ACCESS_FS_READ_FILE
	}
	if err := addLandlockPathRule(rulesetFD, armInitDevNullRuleFD, devNullAccess); err != nil {
		return fmt.Errorf("allow Landlock /dev/null access: %w", err)
	}
	if fullFilesystemPolicy {
		if err := addLandlockPathRule(
			rulesetFD,
			armInitTargetFD,
			unix.LANDLOCK_ACCESS_FS_READ_FILE|unix.LANDLOCK_ACCESS_FS_EXECUTE,
		); err != nil {
			return fmt.Errorf("allow Landlock target executable: %w", err)
		}
		var common unix.Stat_t
		if err := unix.Fstat(commonMCPExecutableFD, &common); err != nil {
			return err
		}
		commonAccess := uint64(unix.LANDLOCK_ACCESS_FS_READ_FILE)
		if common.Mode&unix.S_IFMT == unix.S_IFREG {
			commonAccess |= unix.LANDLOCK_ACCESS_FS_EXECUTE
		}
		if err := addLandlockPathRule(rulesetFD, commonMCPExecutableFD, commonAccess); err != nil {
			return fmt.Errorf("allow Landlock common MCP executable: %w", err)
		}
		procSelf, err := unix.Open("/proc/self", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
		if err != nil {
			return fmt.Errorf("open /proc/self Landlock root: %w", err)
		}
		defer unix.Close(procSelf)
		if err := addLandlockPathRule(
			rulesetFD,
			procSelf,
			unix.LANDLOCK_ACCESS_FS_READ_FILE|unix.LANDLOCK_ACCESS_FS_READ_DIR,
		); err != nil {
			return fmt.Errorf("allow Landlock /proc/self reads: %w", err)
		}
	}
	for descriptor := layout.writableFirst; descriptor < layout.writableFirst+layout.writableCount; descriptor++ {
		allowed := landlockHandledWriteAccess
		if fullFilesystemPolicy {
			allowed |= unix.LANDLOCK_ACCESS_FS_READ_FILE | unix.LANDLOCK_ACCESS_FS_READ_DIR
		}
		if err := addLandlockPathRule(rulesetFD, descriptor, allowed); err != nil {
			return fmt.Errorf("add Landlock writable root FD %d: %w", descriptor, err)
		}
	}
	for descriptor := layout.readOnlyFirst; descriptor < layout.readOnlyFirst+layout.readOnlyCount; descriptor++ {
		var stat unix.Stat_t
		if err := unix.Fstat(descriptor, &stat); err != nil {
			return err
		}
		allowed := uint64(unix.LANDLOCK_ACCESS_FS_READ_FILE)
		if stat.Mode&unix.S_IFMT == unix.S_IFDIR {
			allowed |= unix.LANDLOCK_ACCESS_FS_READ_DIR
		}
		if err := addLandlockPathRule(rulesetFD, descriptor, allowed); err != nil {
			return fmt.Errorf("add Landlock read-only root FD %d: %w", descriptor, err)
		}
	}
	for descriptor := layout.executableFirst; descriptor < layout.executableFirst+layout.executableCount; descriptor++ {
		if err := addLandlockPathRule(
			rulesetFD,
			descriptor,
			unix.LANDLOCK_ACCESS_FS_READ_FILE|unix.LANDLOCK_ACCESS_FS_EXECUTE,
		); err != nil {
			return fmt.Errorf("add Landlock executable FD %d: %w", descriptor, err)
		}
	}
	for _, port := range layout.connectPorts {
		if err := addLandlockPortRule(
			rulesetFD,
			unix.LANDLOCK_ACCESS_NET_CONNECT_TCP,
			port,
		); err != nil {
			return fmt.Errorf("allow Landlock TCP connect port %d: %w", port, err)
		}
	}
	for _, port := range layout.bindPorts {
		if err := addLandlockPortRule(
			rulesetFD,
			unix.LANDLOCK_ACCESS_NET_BIND_TCP,
			port,
		); err != nil {
			return fmt.Errorf("allow Landlock TCP bind port %d: %w", port, err)
		}
	}
	_, _, errno = unix.Syscall6(
		unix.SYS_LANDLOCK_RESTRICT_SELF,
		uintptr(rulesetFD),
		0,
		0,
		0,
		0,
		0,
	)
	if errno != 0 {
		return fmt.Errorf("enter Landlock domain: %w", errno)
	}
	return nil
}

func addLandlockPortRule(rulesetFD int, allowed uint64, port uint16) error {
	attribute := landlockNetPortAttr{AllowedAccess: allowed, Port: uint64(port)}
	_, _, errno := unix.Syscall6(
		unix.SYS_LANDLOCK_ADD_RULE,
		uintptr(rulesetFD),
		landlockRuleNetPort,
		uintptr(unsafe.Pointer(&attribute)),
		0,
		0,
		0,
	)
	if errno != 0 {
		return errno
	}
	return nil
}

func addLandlockPathRule(rulesetFD, parentFD int, allowed uint64) error {
	pathAttribute := landlockPathBeneathAttr{
		AllowedAccess: allowed,
		ParentFD:      int32(parentFD),
	}
	_, _, errno := unix.Syscall6(
		unix.SYS_LANDLOCK_ADD_RULE,
		uintptr(rulesetFD),
		unix.LANDLOCK_RULE_PATH_BENEATH,
		uintptr(unsafe.Pointer(&pathAttribute)),
		0,
		0,
		0,
	)
	if errno != 0 {
		return errno
	}
	return nil
}
