package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/yapless/scopesifter/internal/taskctllauncher"
)

// releaseRevision is replaced by the release builder with
// -X main.releaseRevision=<commit>. Keeping the value on the executed path
// makes the raw launcher artifact unambiguously bindable to its source commit.
var (
	releaseRevision       = "development"
	releaseRevisionMarker = "scopesifter.release-revision=development"
)

func init() {
	if releaseRevisionMarker == "scopesifter.release-revision="+releaseRevision {
		taskctllauncher.SetOperationalReleaseRevision(releaseRevision)
	}
}

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := taskctllauncher.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
