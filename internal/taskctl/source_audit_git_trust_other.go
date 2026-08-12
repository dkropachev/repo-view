//go:build !linux

package taskctl

import (
	"errors"
	"os"
)

type sourceAuditGitPlatformTrust struct{}

func captureSourceAuditGitPlatformTrust(
	_ string,
	_ *os.File,
) (sourceAuditGitPlatformTrust, error) {
	return sourceAuditGitPlatformTrust{}, errors.New(
		"OS-trusted authenticated source-audit Git is supported only on Linux",
	)
}

func (sourceAuditGitPlatformTrust) validate(_ string, _ *os.File) error {
	return errors.New("OS-trusted authenticated source-audit Git is supported only on Linux")
}
