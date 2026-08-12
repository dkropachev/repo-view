//go:build !linux

package main

import (
	"errors"
	"io"
)

func runCommandRunnerUtility(string, io.Writer, io.Writer) error {
	return errors.New("command-runner utility requires Linux")
}
