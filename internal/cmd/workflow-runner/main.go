package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/yapless/scopesifter/internal/workflowrunner"
)

func main() {
	os.Exit(run())
}

func run() int {
	if len(os.Args) != 3 || os.Args[1] != "--" {
		fmt.Fprintln(os.Stderr, "workflow runner: expected -- followed by one Actions run-file path")
		return 2
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := workflowrunner.Run(ctx, ".", os.Args[2], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "workflow runner: %v\n", err)
		return 1
	}
	return 0
}
