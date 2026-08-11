//go:build aix || darwin || dragonfly || freebsd || linux || netbsd || openbsd || solaris

package navigator

import (
	"os"
	"syscall"
)

func regularFileOpenFlags() int {
	return os.O_RDONLY | syscall.O_NONBLOCK | syscall.O_NOFOLLOW
}
