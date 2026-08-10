package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/dkropachev/repo-view/internal/repoviewmcp"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func runMCP(args []string) int {
	flags := flag.NewFlagSet("mcp", flag.ContinueOnError)
	flags.Usage = func() {
		printCommandUsage(
			flags.Output(),
			"repo-view mcp",
			"Serve the fixed read-only repo-view tool surface over stdio.",
		)
		flags.PrintDefaults()
	}
	root := flags.String("root", "", "absolute canonical repository root (required)")
	base := flags.String("base", "", "canonical full Git base commit (required)")
	head := flags.String("head", "", "canonical full Git head commit (cache mode)")
	gitExecutable := flags.String("git", "", "absolute canonical Git executable (Git mode)")
	gitSHA256 := flags.String("git-sha256", "", "lowercase SHA-256 of --git (Git mode)")
	cachePath := flags.String(
		"changed-state-cache",
		"",
		"absolute canonical changed-state cache path (cache mode)",
	)
	cacheSHA256 := flags.String(
		"changed-state-cache-sha256",
		"",
		"lowercase SHA-256 of --changed-state-cache (cache mode)",
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
	gitMode := *gitExecutable != "" || *gitSHA256 != ""
	cacheMode := *head != "" || *cachePath != "" || *cacheSHA256 != ""
	if gitMode == cacheMode {
		fmt.Fprintln(
			os.Stderr,
			"exactly one provider is required: --git with --git-sha256, or --head with --changed-state-cache and --changed-state-cache-sha256",
		)
		return 2
	}
	if gitMode && (*gitExecutable == "" || *gitSHA256 == "") {
		fmt.Fprintln(os.Stderr, "--git and --git-sha256 are both required in Git mode")
		return 2
	}
	if cacheMode && (*head == "" || *cachePath == "" || *cacheSHA256 == "") {
		fmt.Fprintln(
			os.Stderr,
			"--head, --changed-state-cache, and --changed-state-cache-sha256 are all required in cache mode",
		)
		return 2
	}
	if cacheMode && !validFullGitObjectID(*head) {
		fmt.Fprintln(os.Stderr, "--head must be a canonical full Git object ID")
		return 2
	}
	err := repoviewmcp.Run(
		context.Background(),
		repoviewmcp.Config{
			Root:                *root,
			Base:                *base,
			Head:                *head,
			GitExecutable:       *gitExecutable,
			GitExecutableSHA256: *gitSHA256,
			CachePath:           *cachePath,
			CacheSHA256:         *cacheSHA256,
		},
		&mcp.StdioTransport{},
	)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}
