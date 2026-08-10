//go:build linux && !amd64 && !arm64

package runner

const (
	armInitAuditArchitecture = uint32(0)
	x32SyscallBit            = uint32(0x40000000)
)

var armInitNetworkServerSyscalls = []uint32{}
