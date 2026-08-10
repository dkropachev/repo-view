//go:build linux

package runner

import (
	"encoding/binary"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"
)

const (
	exactConnectPolicyVersion = "tokenbench.cgroup-connect-bpf/v2"
	bpfVerifierLogBytes       = 16 << 10
	bpfObjectNameBytes        = 16

	bpfLoadWord        = uint8(0x61) // BPF_LDX | BPF_W | BPF_MEM.
	bpfJumpNotEqualImm = uint8(0x55) // BPF_JMP | BPF_JNE | BPF_K.
	bpfMove64Imm       = uint8(0xb7) // BPF_ALU64 | BPF_MOV | BPF_K.
	bpfExit            = uint8(0x95) // BPF_JMP | BPF_EXIT.

	bpfSockAddrUserFamilyOffset = int16(0)
	bpfSockAddrUserIPv4Offset   = int16(4)
	bpfSockAddrUserPortOffset   = int16(24)
	bpfSockAddrFamilyOffset     = int16(28)
	bpfSockAddrTypeOffset       = int16(32)
	bpfSockAddrProtocolOffset   = int16(36)
)

type connectPolicyIdentity struct {
	Version          string `json:"version"`
	Enforcement      string `json:"enforcement"`
	IPv4Destination  string `json:"ipv4_destination"`
	IPv6Connect      string `json:"ipv6_connect"`
	Transport        string `json:"transport"`
	PortAuthority    string `json:"port_authority"`
	Attachment       string `json:"attachment"`
	AttachFlags      string `json:"attach_flags"`
	EffectiveChain   string `json:"effective_chain"`
	ExactDestination bool   `json:"exact_destination"`
}

func networkPolicyIdentity(
	construction constructionMode,
	contained bool,
) connectPolicyIdentity {
	switch {
	case construction == conformantCodexConstruction:
		return connectPolicyIdentity{
			Version:          exactConnectPolicyVersion,
			Enforcement:      "cgroup-v2-bpf-sock-addr+landlock",
			ExactDestination: true,
			IPv4Destination:  "127.0.0.1",
			IPv6Connect:      "deny",
			Transport:        "tcp-stream-only",
			PortAuthority:    "one-arm-lifecycle-proxy-port",
			Attachment:       "per-arm-bpf-link",
			AttachFlags:      "link-create-zero;direct-query-allow-multi",
			EffectiveChain:   "sole-program-at-each-attach-type",
		}
	case contained:
		return connectPolicyIdentity{
			Version:          exactConnectPolicyVersion,
			Enforcement:      "landlock-port-only-nonpublishable",
			ExactDestination: false,
			IPv4Destination:  "any-address-on-listed-port",
			IPv6Connect:      "any-address-on-listed-port",
			Transport:        "tcp-stream-only",
			PortAuthority:    "arm-lifecycle-port-list",
			Attachment:       "none",
			AttachFlags:      "none",
			EffectiveChain:   "not-verified-nonpublishable",
		}
	default:
		return connectPolicyIdentity{
			Version:          exactConnectPolicyVersion,
			Enforcement:      "none-nonpublishable",
			ExactDestination: false,
			IPv4Destination:  "unrestricted",
			IPv6Connect:      "unrestricted",
			Transport:        "unrestricted",
			PortAuthority:    "none",
			Attachment:       "none",
			AttachFlags:      "none",
			EffectiveChain:   "none",
		}
	}
}

type bpfInstruction struct {
	Code uint8
	Regs uint8
	Off  int16
	Imm  int32
}

type bpfProgramLoadAttr struct {
	ProgramType        uint32
	InstructionCount   uint32
	Instructions       uint64
	License            uint64
	LogLevel           uint32
	LogSize            uint32
	LogBuffer          uint64
	KernelVersion      uint32
	ProgramFlags       uint32
	ProgramName        [bpfObjectNameBytes]byte
	ProgramInterface   uint32
	ExpectedAttachType uint32
}

type bpfLinkCreateAttr struct {
	ProgramFD  uint32
	TargetFD   uint32
	AttachType uint32
	Flags      uint32
}

type bpfLinkDetachAttr struct {
	LinkFD uint32
}

type bpfObjectInfoAttr struct {
	ObjectFD uint32
	InfoSize uint32
	Info     uint64
}

type bpfProgramInfoPrefix struct {
	ProgramType uint32
	ID          uint32
}

type bpfProgramQueryAttr struct {
	TargetFD     uint32
	AttachType   uint32
	QueryFlags   uint32
	AttachFlags  uint32
	ProgramIDs   uint64
	ProgramCount uint32
	Reserved     uint32
}

type cgroupBPFLink struct {
	programFD  int
	linkFD     int
	programID  uint32
	attachType uint32
}

type cgroupConnectPolicy struct {
	ipv4    cgroupBPFLink
	ipv6    cgroupBPFLink
	target  int
	mu      sync.Mutex
	port    uint16
	cleaned bool
}

func exactIPv4ConnectInstructions(port uint16) []bpfInstruction {
	ipv4 := binary.NativeEndian.Uint32([]byte{127, 0, 0, 1})
	networkPort := binary.NativeEndian.Uint16([]byte{byte(port >> 8), byte(port)})
	checks := []struct {
		offset int16
		value  uint32
	}{
		{bpfSockAddrUserFamilyOffset, unix.AF_INET},
		{bpfSockAddrFamilyOffset, unix.AF_INET},
		{bpfSockAddrTypeOffset, unix.SOCK_STREAM},
		{bpfSockAddrProtocolOffset, unix.IPPROTO_TCP},
		{bpfSockAddrUserIPv4Offset, ipv4},
		{bpfSockAddrUserPortOffset, uint32(networkPort)},
	}
	instructions := make([]bpfInstruction, 0, len(checks)*2+4)
	for _, check := range checks {
		instructions = append(instructions,
			bpfInstruction{Code: bpfLoadWord, Regs: bpfRegisters(2, 1), Off: check.offset},
			bpfInstruction{Code: bpfJumpNotEqualImm, Regs: bpfRegisters(2, 0), Imm: int32(check.value)},
		)
	}
	instructions = append(instructions,
		bpfInstruction{Code: bpfMove64Imm, Regs: bpfRegisters(0, 0), Imm: 1},
		bpfInstruction{Code: bpfExit},
		bpfInstruction{Code: bpfMove64Imm, Regs: bpfRegisters(0, 0)},
		bpfInstruction{Code: bpfExit},
	)
	denyIndex := len(instructions) - 2
	for index := 1; index < len(checks)*2; index += 2 {
		instructions[index].Off = int16(denyIndex - index - 1)
	}
	return instructions
}

func denyConnectInstructions() []bpfInstruction {
	return []bpfInstruction{
		{Code: bpfMove64Imm, Regs: bpfRegisters(0, 0)},
		{Code: bpfExit},
	}
}

func bpfRegisters(destination, source uint8) uint8 {
	return destination&0xf | source<<4
}

func loadCgroupConnectProgram(
	name string,
	attachType uint32,
	instructions []bpfInstruction,
) (int, uint32, error) {
	if name == "" || len(name) >= bpfObjectNameBytes || len(instructions) == 0 ||
		len(instructions) > 128 {
		return -1, 0, errors.New("cgroup connect BPF program metadata is invalid")
	}
	license := []byte("GPL\x00")
	verifierLog := make([]byte, bpfVerifierLogBytes)
	attribute := bpfProgramLoadAttr{
		ProgramType:        unix.BPF_PROG_TYPE_CGROUP_SOCK_ADDR,
		InstructionCount:   uint32(len(instructions)),
		Instructions:       uint64(uintptr(unsafe.Pointer(&instructions[0]))),
		License:            uint64(uintptr(unsafe.Pointer(&license[0]))),
		LogLevel:           1,
		LogSize:            uint32(len(verifierLog)),
		LogBuffer:          uint64(uintptr(unsafe.Pointer(&verifierLog[0]))),
		ExpectedAttachType: attachType,
	}
	copy(attribute.ProgramName[:], name)
	result, err := bpfCall(unix.BPF_PROG_LOAD, unsafe.Pointer(&attribute), unsafe.Sizeof(attribute))
	runtime.KeepAlive(instructions)
	runtime.KeepAlive(license)
	runtime.KeepAlive(verifierLog)
	if err != nil {
		log := strings.TrimRight(string(verifierLog), "\x00")
		if log != "" {
			return -1, 0, fmt.Errorf("load cgroup connect BPF program: %w (verifier: %s)", err, log)
		}
		return -1, 0, fmt.Errorf("load cgroup connect BPF program: %w", err)
	}
	programFD := int(result)
	programID, infoErr := bpfProgramID(programFD)
	if infoErr != nil {
		return -1, 0, errors.Join(infoErr, unix.Close(programFD))
	}
	return programFD, programID, nil
}

func createCgroupBPFLink(programFD, targetFD int, attachType uint32) (int, error) {
	if programFD < 0 || targetFD < 0 {
		return -1, errors.New("cgroup BPF link requires valid program and target descriptors")
	}
	attribute := bpfLinkCreateAttr{
		ProgramFD:  uint32(programFD),
		TargetFD:   uint32(targetFD),
		AttachType: attachType,
	}
	result, err := bpfCall(unix.BPF_LINK_CREATE, unsafe.Pointer(&attribute), unsafe.Sizeof(attribute))
	if err != nil {
		return -1, fmt.Errorf("attach cgroup connect BPF link: %w", err)
	}
	return int(result), nil
}

func bpfProgramID(programFD int) (uint32, error) {
	if programFD < 0 {
		return 0, errors.New("bpf program descriptor is invalid")
	}
	info := bpfProgramInfoPrefix{}
	attribute := bpfObjectInfoAttr{
		ObjectFD: uint32(programFD),
		InfoSize: uint32(unsafe.Sizeof(info)),
		Info:     uint64(uintptr(unsafe.Pointer(&info))),
	}
	_, err := bpfCall(unix.BPF_OBJ_GET_INFO_BY_FD, unsafe.Pointer(&attribute), unsafe.Sizeof(attribute))
	runtime.KeepAlive(&info)
	if err != nil {
		return 0, fmt.Errorf("read cgroup connect BPF program identity: %w", err)
	}
	if info.ID == 0 || info.ProgramType != unix.BPF_PROG_TYPE_CGROUP_SOCK_ADDR {
		return 0, errors.New("cgroup connect BPF program returned an invalid identity")
	}
	return info.ID, nil
}

func queryCgroupBPFPrograms(
	targetFD int,
	attachType uint32,
	queryFlags uint32,
) ([]uint32, uint32, error) {
	if targetFD < 0 {
		return nil, 0, errors.New("cgroup BPF query target descriptor is invalid")
	}
	if queryFlags != 0 && queryFlags != unix.BPF_F_QUERY_EFFECTIVE {
		return nil, 0, errors.New("cgroup BPF query flags are invalid")
	}
	ids := make([]uint32, 64)
	attribute := bpfProgramQueryAttr{
		TargetFD:     uint32(targetFD),
		AttachType:   attachType,
		QueryFlags:   queryFlags,
		ProgramIDs:   uint64(uintptr(unsafe.Pointer(&ids[0]))),
		ProgramCount: uint32(len(ids)),
	}
	_, err := bpfCall(unix.BPF_PROG_QUERY, unsafe.Pointer(&attribute), unsafe.Sizeof(attribute))
	runtime.KeepAlive(ids)
	if err != nil {
		return nil, 0, fmt.Errorf("query cgroup connect BPF attachment: %w", err)
	}
	if attribute.ProgramCount > uint32(len(ids)) {
		return nil, 0, errors.New("cgroup connect BPF attachment count exceeded its bound")
	}
	return append([]uint32(nil), ids[:attribute.ProgramCount]...), attribute.AttachFlags, nil
}

func detachCgroupBPFLink(linkFD int) error {
	if linkFD < 0 {
		return errors.New("cgroup BPF link descriptor is invalid")
	}
	attribute := bpfLinkDetachAttr{LinkFD: uint32(linkFD)}
	_, err := bpfCall(unix.BPF_LINK_DETACH, unsafe.Pointer(&attribute), unsafe.Sizeof(attribute))
	if err != nil {
		return fmt.Errorf("detach cgroup connect BPF link: %w", err)
	}
	return nil
}

func bpfCall(command int, attribute unsafe.Pointer, size uintptr) (uintptr, error) {
	result, _, errno := unix.Syscall(unix.SYS_BPF, uintptr(command), uintptr(attribute), size)
	if errno != 0 {
		return 0, errno
	}
	return result, nil
}

func (policy *cgroupConnectPolicy) install() error {
	if policy == nil || policy.port == 0 || policy.target < 0 {
		return errors.New("exact cgroup connect policy is incomplete")
	}
	var err error
	policy.ipv4.programFD, policy.ipv4.programID, err = loadCgroupConnectProgram(
		"tb_connect4",
		unix.BPF_CGROUP_INET4_CONNECT,
		exactIPv4ConnectInstructions(policy.port),
	)
	policy.ipv4.attachType = unix.BPF_CGROUP_INET4_CONNECT
	if err != nil {
		return err
	}
	policy.ipv4.linkFD, err = createCgroupBPFLink(
		policy.ipv4.programFD,
		policy.target,
		policy.ipv4.attachType,
	)
	if err != nil {
		return err
	}
	policy.ipv6.programFD, policy.ipv6.programID, err = loadCgroupConnectProgram(
		"tb_connect6",
		unix.BPF_CGROUP_INET6_CONNECT,
		denyConnectInstructions(),
	)
	policy.ipv6.attachType = unix.BPF_CGROUP_INET6_CONNECT
	if err != nil {
		return err
	}
	policy.ipv6.linkFD, err = createCgroupBPFLink(
		policy.ipv6.programFD,
		policy.target,
		policy.ipv6.attachType,
	)
	if err != nil {
		return err
	}
	return policy.verifyLocked()
}

func (policy *cgroupConnectPolicy) verify() error {
	if policy == nil {
		return errors.New("exact cgroup connect policy is missing")
	}
	policy.mu.Lock()
	defer policy.mu.Unlock()
	if policy.cleaned {
		return errors.New("exact cgroup connect policy is already detached")
	}
	return policy.verifyLocked()
}

func (policy *cgroupConnectPolicy) verifyLocked() error {
	for _, attachment := range []cgroupBPFLink{policy.ipv4, policy.ipv6} {
		if attachment.programFD < 0 || attachment.linkFD < 0 || attachment.programID == 0 {
			return errors.New("exact cgroup connect policy has an incomplete attachment")
		}
		programID, err := bpfProgramID(attachment.programFD)
		if err != nil {
			return err
		}
		if programID != attachment.programID {
			return errors.New("cgroup connect BPF program identity drifted")
		}
		ids, attachFlags, err := queryCgroupBPFPrograms(
			policy.target,
			attachment.attachType,
			0,
		)
		if err != nil {
			return err
		}
		// BPF_LINK_CREATE accepts link flags zero, but cgroup link attachment is
		// represented internally and reported by direct query as ALLOW_MULTI.
		// Sole direct/effective IDs are what exclude peer and ancestor programs.
		if attachFlags != unix.BPF_F_ALLOW_MULTI || len(ids) != 1 || ids[0] != attachment.programID {
			return fmt.Errorf(
				"cgroup connect BPF direct attachment drifted: flags=%#x ids=%v, want flags=%#x ids=[%d]",
				attachFlags,
				ids,
				unix.BPF_F_ALLOW_MULTI,
				attachment.programID,
			)
		}
		effective, effectiveFlags, err := queryCgroupBPFPrograms(
			policy.target,
			attachment.attachType,
			unix.BPF_F_QUERY_EFFECTIVE,
		)
		if err != nil {
			return err
		}
		if effectiveFlags != 0 || len(effective) != 1 || effective[0] != attachment.programID {
			return fmt.Errorf(
				"cgroup connect BPF effective chain drifted: flags=%#x ids=%v, want flags=0 ids=[%d]",
				effectiveFlags,
				effective,
				attachment.programID,
			)
		}
	}
	return nil
}

func (policy *cgroupConnectPolicy) cleanup() error {
	if policy == nil {
		return nil
	}
	policy.mu.Lock()
	defer policy.mu.Unlock()
	if policy.cleaned {
		return nil
	}
	var resultErr error
	if policy.ipv4.linkFD >= 0 && policy.ipv6.linkFD >= 0 {
		resultErr = errors.Join(resultErr, policy.verifyLocked())
	}
	for _, attachment := range []*cgroupBPFLink{&policy.ipv6, &policy.ipv4} {
		hadLink := attachment.linkFD >= 0
		if attachment.linkFD >= 0 {
			resultErr = errors.Join(resultErr, detachCgroupBPFLink(attachment.linkFD))
			resultErr = errors.Join(resultErr, unix.Close(attachment.linkFD))
			attachment.linkFD = -1
		}
		if hadLink && attachment.attachType != 0 {
			ids, attachFlags, err := queryCgroupBPFPrograms(
				policy.target,
				attachment.attachType,
				0,
			)
			resultErr = errors.Join(resultErr, err)
			if err == nil && (attachFlags != 0 || len(ids) != 0) {
				resultErr = errors.Join(
					resultErr,
					fmt.Errorf(
						"cgroup connect BPF direct attachment remained after detach: flags=%#x ids=%v",
						attachFlags,
						ids,
					),
				)
			}
		}
		if attachment.programFD >= 0 {
			resultErr = errors.Join(resultErr, unix.Close(attachment.programFD))
			attachment.programFD = -1
		}
	}
	if resultErr == nil {
		policy.cleaned = true
	}
	return resultErr
}

func newCgroupConnectPolicy(port uint16, targetFD int) *cgroupConnectPolicy {
	return &cgroupConnectPolicy{
		port:   port,
		target: targetFD,
		ipv4:   cgroupBPFLink{programFD: -1, linkFD: -1},
		ipv6:   cgroupBPFLink{programFD: -1, linkFD: -1},
	}
}

func (arm *armCgroup) installExactConnectPolicy(port uint16) error {
	if arm == nil || port == 0 {
		return errors.New("exact cgroup connect policy requires one nonzero port")
	}
	arm.mu.Lock()
	defer arm.mu.Unlock()
	if arm.cleaned || arm.launched || arm.directory == nil || arm.networkPolicy != nil {
		return errors.New("arm cgroup is unavailable or already has a connect policy")
	}
	policy := newCgroupConnectPolicy(port, int(arm.directory.Fd()))
	arm.networkPolicy = policy
	if err := policy.install(); err != nil {
		return fmt.Errorf("install exact cgroup connect policy: %w", err)
	}
	return nil
}

func (arm *armCgroup) verifyExactConnectPolicy() error {
	arm.mu.Lock()
	policy := arm.networkPolicy
	arm.mu.Unlock()
	if policy == nil {
		return errors.New("arm cgroup omitted its exact connect policy")
	}
	return policy.verify()
}

func (arm *armCgroup) cleanupConnectPolicy() error {
	arm.mu.Lock()
	policy := arm.networkPolicy
	arm.mu.Unlock()
	if policy == nil {
		return nil
	}
	if err := policy.cleanup(); err != nil {
		return err
	}
	arm.mu.Lock()
	if arm.networkPolicy == policy {
		arm.networkPolicy = nil
	}
	arm.mu.Unlock()
	return nil
}

func probeExactConnectPolicy(manager *cgroupManager, timeoutDuration time.Duration) error {
	arm, err := manager.newArm()
	if err != nil {
		return err
	}
	installErr := arm.installExactConnectPolicy(1)
	cleanupErr := arm.killAndRemove(timeoutDuration)
	if cleanupErr != nil {
		cleanupErr = errors.Join(cleanupErr, arm.killAndRemove(timeoutDuration))
	}
	return errors.Join(installErr, cleanupErr)
}
