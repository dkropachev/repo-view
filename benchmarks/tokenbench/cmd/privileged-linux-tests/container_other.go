//go:build !linux

package main

import (
	"errors"
	"io"
)

func containerMain(io.Writer, io.Writer) error {
	return errors.New("the privileged lane requires Linux")
}

func cgroupEntry(string, string, []string) error {
	return errors.New("cgroup entry requires Linux")
}
