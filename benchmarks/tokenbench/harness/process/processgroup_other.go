//go:build !unix

package process

import "os/exec"

func isolateCommand(*exec.Cmd) {}

func cleanupCommandGroup(*exec.Cmd) {}
