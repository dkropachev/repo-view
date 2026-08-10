//go:build linux

package runner

import (
	"encoding/binary"
	"os"
	"testing"
	"unsafe"

	"golang.org/x/sys/unix"
)

type bpfSockAddrLayoutMirror struct {
	UserFamily uint32
	UserIPv4   uint32
	UserIPv6   [4]uint32
	UserPort   uint32
	Family     uint32
	Type       uint32
	Protocol   uint32
	SourceIPv4 uint32
	SourceIPv6 [4]uint32
	Socket     uint64
}

func TestBPFSockAddrOffsetsMatchLinuxUAPI(t *testing.T) {
	for name, observed := range map[string]int16{
		"user_family": int16(unsafe.Offsetof(bpfSockAddrLayoutMirror{}.UserFamily)),
		"user_ip4":    int16(unsafe.Offsetof(bpfSockAddrLayoutMirror{}.UserIPv4)),
		"user_port":   int16(unsafe.Offsetof(bpfSockAddrLayoutMirror{}.UserPort)),
		"family":      int16(unsafe.Offsetof(bpfSockAddrLayoutMirror{}.Family)),
		"type":        int16(unsafe.Offsetof(bpfSockAddrLayoutMirror{}.Type)),
		"protocol":    int16(unsafe.Offsetof(bpfSockAddrLayoutMirror{}.Protocol)),
	} {
		want := map[string]int16{
			"user_family": bpfSockAddrUserFamilyOffset,
			"user_ip4":    bpfSockAddrUserIPv4Offset,
			"user_port":   bpfSockAddrUserPortOffset,
			"family":      bpfSockAddrFamilyOffset,
			"type":        bpfSockAddrTypeOffset,
			"protocol":    bpfSockAddrProtocolOffset,
		}[name]
		if observed != want {
			t.Fatalf("bpf_sock_addr %s offset = %d, want %d", name, observed, want)
		}
	}
	if size := unsafe.Sizeof(bpfSockAddrLayoutMirror{}); size != 72 {
		t.Fatalf("bpf_sock_addr mirror size = %d, want 72", size)
	}
}

func TestExactIPv4ConnectProgramDeniesAddressPortAndProtocolEscapes(t *testing.T) {
	const allowedPort = uint16(43127)
	allowed := bpfSockAddrTestContext{
		userFamily: unix.AF_INET,
		userIPv4:   networkIPv4(127, 0, 0, 1),
		userPort:   networkPort(allowedPort),
		family:     unix.AF_INET,
		socketType: unix.SOCK_STREAM,
		protocol:   unix.IPPROTO_TCP,
	}
	program := exactIPv4ConnectInstructions(allowedPort)
	if got := evaluateConnectProgram(t, program, allowed); got != 1 {
		t.Fatalf("exact allowed destination verdict = %d, want 1", got)
	}
	for name, mutate := range map[string]func(*bpfSockAddrTestContext){
		"same port on public IPv4": func(context *bpfSockAddrTestContext) {
			context.userIPv4 = networkIPv4(203, 0, 113, 7)
		},
		"same port on other loopback": func(context *bpfSockAddrTestContext) {
			context.userIPv4 = networkIPv4(127, 0, 0, 2)
		},
		"different port": func(context *bpfSockAddrTestContext) {
			context.userPort = networkPort(allowedPort + 1)
		},
		"datagram type": func(context *bpfSockAddrTestContext) {
			context.socketType = unix.SOCK_DGRAM
		},
		"udp protocol": func(context *bpfSockAddrTestContext) {
			context.protocol = unix.IPPROTO_UDP
		},
		"wrong user family": func(context *bpfSockAddrTestContext) {
			context.userFamily = unix.AF_INET6
		},
		"wrong socket family": func(context *bpfSockAddrTestContext) {
			context.family = unix.AF_INET6
		},
	} {
		t.Run(name, func(t *testing.T) {
			changed := allowed
			mutate(&changed)
			if got := evaluateConnectProgram(t, program, changed); got != 0 {
				t.Fatalf("escaped destination verdict = %d, want 0", got)
			}
		})
	}
	if got := evaluateConnectProgram(t, denyConnectInstructions(), allowed); got != 0 {
		t.Fatalf("IPv6 deny program verdict = %d, want 0", got)
	}
}

func TestExactConnectProgramAndBPFAttributesAreBounded(t *testing.T) {
	program := exactIPv4ConnectInstructions(65535)
	if len(program) != 16 || len(program) > 128 {
		t.Fatalf("exact connect program length = %d, want 16 and bounded", len(program))
	}
	denyIndex := len(program) - 2
	for index := 1; index < 12; index += 2 {
		if destination := index + 1 + int(program[index].Off); destination != denyIndex {
			t.Fatalf("conditional %d jumps to %d, want deny index %d", index, destination, denyIndex)
		}
	}
	if bpfVerifierLogBytes <= 0 || bpfVerifierLogBytes > 64<<10 {
		t.Fatalf("BPF verifier log bound = %d", bpfVerifierLogBytes)
	}
	for name, gotWant := range map[string][2]uintptr{
		"instruction":   {unsafe.Sizeof(bpfInstruction{}), 8},
		"program load":  {unsafe.Sizeof(bpfProgramLoadAttr{}), 72},
		"link create":   {unsafe.Sizeof(bpfLinkCreateAttr{}), 16},
		"link detach":   {unsafe.Sizeof(bpfLinkDetachAttr{}), 4},
		"object info":   {unsafe.Sizeof(bpfObjectInfoAttr{}), 16},
		"program query": {unsafe.Sizeof(bpfProgramQueryAttr{}), 32},
	} {
		if gotWant[0] != gotWant[1] {
			t.Fatalf("%s UAPI attribute size = %d, want %d", name, gotWant[0], gotWant[1])
		}
	}
}

func TestNetworkPolicyIdentityDistinguishesExactAndPortOnlyModes(t *testing.T) {
	exact := networkPolicyIdentity(conformantCodexConstruction, true)
	if !exact.ExactDestination || exact.IPv4Destination != "127.0.0.1" ||
		exact.IPv6Connect != "deny" || exact.Attachment != "per-arm-bpf-link" ||
		exact.AttachFlags != "link-create-zero;direct-query-allow-multi" ||
		exact.EffectiveChain != "sole-program-at-each-attach-type" {
		t.Fatalf("conformant network identity = %#v", exact)
	}
	portOnly := networkPolicyIdentity(genericConstruction, true)
	if portOnly.ExactDestination ||
		portOnly.Enforcement != "landlock-port-only-nonpublishable" ||
		portOnly.IPv4Destination != "any-address-on-listed-port" {
		t.Fatalf("generic contained network identity = %#v", portOnly)
	}
	uncontained := networkPolicyIdentity(genericConstruction, false)
	if uncontained.ExactDestination || uncontained.Enforcement != "none-nonpublishable" {
		t.Fatalf("generic uncontained network identity = %#v", uncontained)
	}
}

func TestPartialExactConnectInstallRetainsCleanupCapability(t *testing.T) {
	directory, err := os.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer directory.Close()
	arm := &armCgroup{directory: directory}
	if err := arm.installExactConnectPolicy(443); err == nil {
		t.Fatal("non-cgroup directory accepted an exact connect attachment")
	}
	if arm.networkPolicy == nil {
		t.Fatal("partial exact connect installation lost its cleanup capability")
	}
	if err := arm.cleanupConnectPolicy(); err != nil {
		t.Fatalf("clean partial exact connect installation: %v", err)
	}
	if arm.networkPolicy != nil {
		t.Fatal("partial exact connect installation remained after cleanup")
	}
}

func TestPIDNamespaceMarkerIsFailClosed(t *testing.T) {
	t.Setenv(armInitPIDNamespaceEnvironment, "")
	if expected, err := parseArmInitPIDNamespaceExpectation(); err != nil || expected {
		t.Fatalf("empty PID namespace marker = %t, %v", expected, err)
	}
	t.Setenv(armInitPIDNamespaceEnvironment, armInitVersion)
	if expected, err := parseArmInitPIDNamespaceExpectation(); err != nil || !expected {
		t.Fatalf("valid PID namespace marker = %t, %v", expected, err)
	}
	t.Setenv(armInitPIDNamespaceEnvironment, armInitVersion+"-tampered")
	if _, err := parseArmInitPIDNamespaceExpectation(); err == nil {
		t.Fatal("tampered PID namespace marker was accepted")
	}
}

type bpfSockAddrTestContext struct {
	userFamily uint32
	userIPv4   uint32
	userPort   uint32
	family     uint32
	socketType uint32
	protocol   uint32
}

func networkIPv4(first, second, third, fourth byte) uint32 {
	return binary.NativeEndian.Uint32([]byte{first, second, third, fourth})
}

func networkPort(port uint16) uint32 {
	return uint32(binary.NativeEndian.Uint16([]byte{byte(port >> 8), byte(port)}))
}

func evaluateConnectProgram(
	t *testing.T,
	program []bpfInstruction,
	context bpfSockAddrTestContext,
) int32 {
	t.Helper()
	registers := [3]int32{}
	load := func(offset int16) uint32 {
		switch offset {
		case bpfSockAddrUserFamilyOffset:
			return context.userFamily
		case bpfSockAddrUserIPv4Offset:
			return context.userIPv4
		case bpfSockAddrUserPortOffset:
			return context.userPort
		case bpfSockAddrFamilyOffset:
			return context.family
		case bpfSockAddrTypeOffset:
			return context.socketType
		case bpfSockAddrProtocolOffset:
			return context.protocol
		default:
			t.Fatalf("program loaded unexpected bpf_sock_addr offset %d", offset)
			return 0
		}
	}
	for pc, steps := 0, 0; pc >= 0 && pc < len(program) && steps <= len(program); steps++ {
		instruction := program[pc]
		switch instruction.Code {
		case bpfLoadWord:
			if instruction.Regs != bpfRegisters(2, 1) {
				t.Fatalf("load at %d used registers %#x", pc, instruction.Regs)
			}
			registers[2] = int32(load(instruction.Off))
			pc++
		case bpfJumpNotEqualImm:
			if registers[2] != instruction.Imm {
				pc += int(instruction.Off) + 1
			} else {
				pc++
			}
		case bpfMove64Imm:
			if instruction.Regs != bpfRegisters(0, 0) {
				t.Fatalf("move at %d used registers %#x", pc, instruction.Regs)
			}
			registers[0] = instruction.Imm
			pc++
		case bpfExit:
			return registers[0]
		default:
			t.Fatalf("unsupported instruction %#x at %d", instruction.Code, pc)
		}
	}
	t.Fatal("BPF program did not terminate within its instruction bound")
	return -1
}
