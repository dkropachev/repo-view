// Command taskctl generates and validates authenticated benchmark task
// artifacts without a script runtime.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/yapless/scopesifter/internal/taskctl"
)

// releaseRevision is replaced only by the deterministic release builder. It
// deliberately remains part of the executable so a released taskctl image is
// cryptographically bound to the source commit that produced it.
var (
	releaseRevision       = "development"
	releaseRevisionMarker = "scopesifter.release-revision=development"
)

func main() {
	if !validReleaseRevision(releaseRevision) ||
		releaseRevisionMarker != "scopesifter.release-revision="+releaseRevision {
		fmt.Fprintln(os.Stderr, "taskctl: operational release revision must be lowercase 40-hex")
		os.Exit(1)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	exitCode := taskctl.Run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	stop()
	os.Exit(exitCode)
}

func validReleaseRevision(revision string) bool {
	if len(revision) != 40 {
		return false
	}
	for _, character := range revision {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}
	return true
}
