//go:build !linux

package taskctl

import (
	"errors"
	"os/exec"
)

func configureSourceAuditRepositoryCommand(
	_ *exec.Cmd,
	_ *sourceAuditRepositoryPin,
) error {
	return errors.New("pinned source-audit repository invocation is supported only on Linux")
}
