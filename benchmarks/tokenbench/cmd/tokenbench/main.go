package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/yapless/scopesifter/benchmarks/tokenbench/internal/commandrunner"
)

func main() {
	os.Exit(dispatch(
		context.Background(), os.Args[0], os.Getenv("PATH"), os.Args[1:],
		os.Stdin, os.Stdout, os.Stderr,
	))
}

func dispatch(
	ctx context.Context,
	argv0 string,
	pathEnvironment string,
	args []string,
	stdin io.Reader,
	stdout, stderr io.Writer,
) int {
	if commandrunner.Invoked(argv0, pathEnvironment) {
		return commandrunner.Run(ctx, args, stdin, stdout, stderr)
	}
	return run(ctx, args, stdout, stderr)
}

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}
	var err error
	switch args[0] {
	case "validate":
		err = validateCommand(ctx, args[1:], stdout, stderr)
	case "plan":
		err = planCommand(ctx, args[1:], stdout, stderr)
	case "run":
		err = runCommand(ctx, args[1:], stdout, stderr)
	case "verify":
		err = verifyCommand(ctx, args[1:], stdout, stderr)
	case "replay":
		err = replayCommand(ctx, args[1:], stdout, stderr)
	case "help", "-h", "--help":
		printUsage(stdout)
		return 0
	default:
		fmt.Fprintf(stderr, "unknown command %q\n", args[0])
		printUsage(stderr)
		return 2
	}
	if err != nil {
		var delegated *delegatedRunResult
		if errors.As(err, &delegated) {
			return delegated.exitCode
		}
		if !errors.Is(err, flag.ErrHelp) {
			fmt.Fprintf(stderr, "tokenbench: %v\n", err)
		}
		return 1
	}
	return 0
}

func writeJSON(writer io.Writer, value any) error {
	encoder := json.NewEncoder(writer)
	encoder.SetIndent("", "  ")
	encoder.SetEscapeHTML(false)
	return encoder.Encode(value)
}

func printUsage(writer io.Writer) {
	fmt.Fprintln(writer, "usage: tokenbench <validate|plan|run|verify|replay> [options]")
	fmt.Fprintln(writer, "  validate  validate one authored built-in Codex suite")
	fmt.Fprintln(writer, "  plan      emit an audit-only Codex plan (never execution authority)")
	fmt.Fprintln(writer, "  run       execute exactly one explicit suite repetition and sign a capture")
	fmt.Fprintln(writer, "  verify    authenticate a signed capture or replay under an explicit policy")
	fmt.Fprintln(writer, "  replay    offline-decode an authenticated capture and sign the derived root")
	fmt.Fprintln(writer, "run reads the upstream credential once from --credential-fd N (3..255) and closes it before launch")
	fmt.Fprintln(writer, "signing-key files contain exactly one unpadded base64url 32-byte Ed25519 seed, with no newline")
}
