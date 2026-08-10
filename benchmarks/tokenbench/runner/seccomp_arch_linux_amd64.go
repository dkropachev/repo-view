//go:build linux && amd64

package runner

import "golang.org/x/sys/unix"

const (
	armInitAuditArchitecture = uint32(unix.AUDIT_ARCH_X86_64)
	x32SyscallBit            = uint32(0x40000000)
)

// listen(2) can implicitly autobind an unbound INET stream socket without
// traversing the bind syscall or Landlock's socket_bind hook. Arms are
// outbound-only clients, so deny the complete server surface.
var armInitNetworkServerSyscalls = []uint32{
	uint32(unix.SYS_SOCKETPAIR),
	uint32(unix.SYS_BIND),
	uint32(unix.SYS_LISTEN),
	uint32(unix.SYS_ACCEPT),
	uint32(unix.SYS_ACCEPT4),
}
