//go:build !aix && !darwin && !dragonfly && !freebsd && !linux && !netbsd && !openbsd && !solaris

package navigator

import "os"

func regularFileOpenFlags() int {
	return os.O_RDONLY
}
