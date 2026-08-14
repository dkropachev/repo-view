package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/yapless/scopesifter/internal/scopesiftermcp"
)

func runMCP(args []string) int {
	flags := flag.NewFlagSet("mcp", flag.ContinueOnError)
	flags.Usage = func() {
		printCommandUsage(
			flags.Output(),
			"scopesifter mcp",
			"Serve the fixed scopesifter navigation tool surface over stdio.",
		)
		flags.PrintDefaults()
	}
	root := flags.String("root", "", "absolute canonical repository root (required)")
	base := flags.String("base", "", "canonical full Git base commit (required)")
	gitExecutable := flags.String("git", "", "absolute canonical Git executable (required)")
	gitSHA256 := flags.String("git-sha256", "", "lowercase SHA-256 of --git (required)")
	adaptiveOutputCache := flags.Bool(
		"adaptive-output-cache",
		false,
		"persist adaptive MCP output budgets in the OS user-cache directory",
	)
	if showCommandHelp(flags, args) {
		return 0
	}
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected argument:", flags.Arg(0))
		return 2
	}
	for _, required := range []struct {
		name  string
		value string
	}{
		{name: "--root", value: *root},
		{name: "--base", value: *base},
		{name: "--git", value: *gitExecutable},
		{name: "--git-sha256", value: *gitSHA256},
	} {
		if required.value == "" {
			fmt.Fprintln(os.Stderr, required.name, "is required")
			return 2
		}
	}
	if !validFullGitObjectID(*base) {
		fmt.Fprintln(os.Stderr, "--base must be a canonical full Git object ID")
		return 2
	}
	err := scopesiftermcp.Run(
		context.Background(),
		scopesiftermcp.Config{
			AdaptiveOutputCache: *adaptiveOutputCache,
			Root:                *root,
			Base:                *base,
			GitExecutable:       *gitExecutable,
			GitExecutableSHA256: *gitSHA256,
		},
		&mcp.StdioTransport{},
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
