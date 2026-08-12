//go:build !linux

package taskctllauncher

import (
	"context"
	"errors"
	"io"
)

func runPlatform(
	context.Context,
	string,
	[]string,
	string,
	io.Reader,
	io.Writer,
	io.Writer,
	launcherHooks,
) error {
	return errors.New("taskctl launcher: authenticated descriptor execution requires Linux")
}

func inspectPlatform(string, io.Writer, launcherHooks) error {
	return errors.New("taskctl launcher: authenticated descriptor inspection requires Linux")
}

func installPlatform(string, io.Writer) error {
	return errors.New("taskctl launcher: trusted launcher installation requires Linux")
}

func verifyOperationalLauncher() error {
	return errors.New("taskctl launcher: fixed installed-launcher authentication requires Linux")
}
