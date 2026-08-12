//go:build !linux

package source

import (
	"context"
	"errors"
	"os"
	"os/exec"

	"github.com/yapless/scopesifter/internal/processpolicy"
)

func pinnedGitExecutionSupported() bool { return false }

func newPinnedGitCommand(
	_ context.Context,
	_ *os.File,
	_ string,
	arguments []string,
) (*exec.Cmd, error) {
	if err := processpolicy.ValidateGit(arguments...); err != nil {
		return nil, err
	}
	return nil, errors.New("pinned Git executable invocation is supported only on Linux")
}

func pinnedGitExecutableHasOneLink(os.FileInfo) bool { return false }
