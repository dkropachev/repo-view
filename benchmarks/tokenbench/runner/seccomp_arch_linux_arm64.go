//go:build linux && arm64

package runner

import "golang.org/x/sys/unix"

const (
	armInitAuditArchitecture = uint32(unix.AUDIT_ARCH_AARCH64)
	x32SyscallBit            = uint32(0x40000000)
)

var armInitNetworkServerSyscalls = []uint32{
	uint32(unix.SYS_SOCKETPAIR),
	uint32(unix.SYS_BIND),
	uint32(unix.SYS_LISTEN),
	uint32(unix.SYS_ACCEPT),
	uint32(unix.SYS_ACCEPT4),
}
