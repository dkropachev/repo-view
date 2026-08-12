//go:build linux

package taskctl

import (
	"errors"
	"os/exec"
)

const sourceAuditPinnedRepositoryPath = "/proc/self/fd/4/"

func configureSourceAuditRepositoryCommand(
	command *exec.Cmd,
	pin *sourceAuditRepositoryPin,
) error {
	if command == nil || pin == nil || pin.file == nil {
		return errors.New("source-audit repository command pin is incomplete")
	}
	if len(command.ExtraFiles) != 1 {
		return errors.New("source-audit Git executable descriptor layout changed")
	}
	command.ExtraFiles = append(command.ExtraFiles, pin.file)
	command.Args = append(
		[]string{command.Args[0], "-C", sourceAuditPinnedRepositoryPath},
		command.Args[1:]...,
	)
	return nil
}
