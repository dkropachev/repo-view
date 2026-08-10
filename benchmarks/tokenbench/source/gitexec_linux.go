//go:build linux

package source

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"syscall"
)

const pinnedGitExecutablePath = "/proc/self/fd/3"

func pinnedGitExecutionSupported() bool { return true }

func newPinnedGitCommand(
	ctx context.Context,
	executable *os.File,
	displayPath string,
	arguments []string,
) (*exec.Cmd, error) {
	if executable == nil {
		return nil, errors.New("pinned Git executable descriptor is unavailable")
	}
	command := exec.CommandContext(ctx, pinnedGitExecutablePath, arguments...)
	command.Args[0] = displayPath
	command.ExtraFiles = []*os.File{executable}
	return command, nil
}

func pinnedGitExecutableHasOneLink(info os.FileInfo) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && stat.Nlink == 1
}
