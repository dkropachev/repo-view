//go:build !linux

package commandrunner

import (
	"context"
	"errors"
)

// VerifyEntrypoint is unavailable outside the Linux publishable runtime.
func VerifyEntrypoint(context.Context, string) error {
	return errors.New("command-runner entrypoint probe requires Linux")
}
