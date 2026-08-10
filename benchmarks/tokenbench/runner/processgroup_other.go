//go:build !unix

package runner

import "os/exec"

func processContainmentSupported() bool { return false }

func isolateCommand(_ *exec.Cmd) {}

func cleanupCommandGroup(_ *exec.Cmd) {}
