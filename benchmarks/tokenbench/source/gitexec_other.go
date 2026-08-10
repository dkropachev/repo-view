//go:build !linux

package source

import (
	"context"
	"errors"
	"os"
	"os/exec"
)

func pinnedGitExecutionSupported() bool { return false }

func newPinnedGitCommand(
	context.Context,
	*os.File,
	string,
	[]string,
) (*exec.Cmd, error) {
	return nil, errors.New("pinned Git executable invocation is supported only on Linux")
}

func pinnedGitExecutableHasOneLink(os.FileInfo) bool { return false }
