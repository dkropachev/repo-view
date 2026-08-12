//go:build !aix && !darwin && !dragonfly && !freebsd && !illumos && !linux && !netbsd && !openbsd && !solaris

package main

func runSignalLifecycleHelper() bool {
	return false
}
