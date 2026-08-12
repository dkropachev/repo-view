//go:build !linux

package commandrunner

import (
	"context"
	"errors"
	"os"
)

// VerifyPinnedEntrypoint is unavailable outside the Linux publishable runtime.
func VerifyPinnedEntrypoint(context.Context, string, *os.File) error {
	return errors.New("command-runner entrypoint probe requires Linux")
}
