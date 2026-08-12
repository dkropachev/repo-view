// Package commandrunner implements the Go-native command interpreter used by
// Codex tool calls in the closed tokenbench runtime.
package commandrunner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"sync"

	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

const codexDiscoveryBasename = "bash"

// Implementation is the immutable semantic identity committed into execution
// inputs. Change it whenever parsing, execution, or exit-status behavior
// changes in a way that can affect an observed model tool call.
const Implementation = "tokenbench.command-runner/go+mvdan-sh-v3.13.1/v1"

// Invoked reports whether argv0 is the compatibility pathname through which
// Codex v0.144.0 discovers the closed command runner. That Codex release has no
// configurable default-shell path on its CLI: on Linux it searches for this
// basename before falling back to ambient absolute paths. The executable bytes
// at this pathname are the tokenbench Go runner, never a Bash image. Requiring
// the sole PATH directory prevents an unrelated renamed binary from selecting
// this mode outside the code-owned toolbox layout.
func Invoked(argv0, pathEnvironment string) bool {
	return filepath.IsAbs(argv0) && filepath.Clean(argv0) == argv0 &&
		filepath.Base(argv0) == codexDiscoveryBasename &&
		filepath.IsAbs(pathEnvironment) && filepath.Clean(pathEnvironment) == pathEnvironment &&
		!strings.ContainsRune(pathEnvironment, filepath.ListSeparator) &&
		filepath.Dir(argv0) == pathEnvironment
}

// Run interprets the exact non-login argv shape emitted by Codex v0.144.0.
// Parsing and orchestration stay in this Go process; external commands are
// resolved through the caller's closed PATH and remain constrained by the
// runner's Landlock executable allowlist.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if ctx == nil {
		fmt.Fprintln(stderr, "tokenbench command runner: context is required")
		return 125
	}
	if len(args) != 2 || args[0] != "-c" {
		fmt.Fprintln(stderr, "tokenbench command runner: expected exactly -c COMMAND")
		return 2
	}

	program, err := syntax.NewParser(syntax.Variant(syntax.LangBash)).Parse(
		strings.NewReader(args[1]),
		"codex-command",
	)
	if err != nil {
		fmt.Fprintf(stderr, "tokenbench command runner: parse command: %v\n", err)
		return 2
	}
	// Pipeline stages may write diagnostics concurrently. Callers are allowed to
	// provide single-writer captures, so serialize both streams at this boundary.
	runner, err := interp.New(
		interp.StdIO(
			stdin,
			&synchronizedWriter{writer: stdout},
			&synchronizedWriter{writer: stderr},
		),
		interp.ExecHandlers(func(_ interp.ExecHandlerFunc) interp.ExecHandlerFunc {
			return interp.DefaultExecHandler(-1)
		}),
	)
	if err != nil {
		fmt.Fprintf(stderr, "tokenbench command runner: initialize: %v\n", err)
		return 125
	}
	if err := runner.Run(ctx, program); err != nil {
		var status interp.ExitStatus
		if errors.As(err, &status) {
			return int(status)
		}
		if ctx.Err() != nil {
			return 124
		}
		fmt.Fprintf(stderr, "tokenbench command runner: execute: %v\n", err)
		return 125
	}
	return 0
}

type synchronizedWriter struct {
	writer io.Writer
	mu     sync.Mutex
}

func (writer *synchronizedWriter) Write(content []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()
	return writer.writer.Write(content)
}
