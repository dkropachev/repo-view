// Package commandrunner implements the closed Go-native command dispatcher used by
// Codex tool calls in the closed tokenbench runtime.
package commandrunner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"

	"github.com/yapless/scopesifter/internal/processpolicy"
)

const codexDiscoveryBasename = "bash"

// Implementation is the immutable semantic identity committed into execution
// inputs. Change it whenever parsing, execution, or exit-status behavior
// changes in a way that can affect an observed model tool call.
const Implementation = "tokenbench.command-runner/go-argv-pipeline-v1/v6"

// Invoked reports whether argv0 is the pinned discovery pathname through which
// Codex v0.144.0 finds the closed command runner. That Codex release has no
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

// Run dispatches the exact non-login argv shape emitted by Codex v0.144.0.
// Its closed grammar is one pipeline of literal argv commands. It has no shell
// assignments, expansion, control flow, command lists, or redirection.
func Run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	if stderr == nil {
		stderr = io.Discard
	}
	if ctx == nil {
		fmt.Fprintln(stderr, "tokenbench command runner: context is required")
		return 125
	}
	if len(args) != 2 || args[0] != "-c" {
		fmt.Fprintln(stderr, "tokenbench command runner: expected exactly -c COMMAND")
		return 2
	}

	program, err := parseCommandProgram(args[1])
	if err != nil {
		fmt.Fprintf(stderr, "tokenbench command runner: parse command: %v\n", err)
		return 2
	}
	exitCode, err := executePipeline(ctx, program.pipeline, stdin, stdout, stderr)
	if err != nil {
		if ctx.Err() != nil {
			return 124
		}
		fmt.Fprintf(stderr, "tokenbench command runner: execute: %v\n", err)
		return 125
	}
	return exitCode
}

func validateExternalCommand(arguments []string) error {
	if len(arguments) == 0 {
		return errors.New("tokenbench command runner: external command is empty")
	}
	if err := processpolicy.ValidateExecutable(arguments[0]); err != nil {
		return fmt.Errorf("tokenbench command runner: reject external command: %w", err)
	}
	if filepath.Base(arguments[0]) != arguments[0] || strings.ContainsAny(arguments[0], `/\\`) {
		return errors.New("tokenbench command runner: external commands must use a bare approved role")
	}
	command := filepath.Base(arguments[0])
	if _, allowed := approvedExternalCommands[command]; !allowed {
		return fmt.Errorf("tokenbench command runner: external command %q is not approved", command)
	}
	optionsEnded := false
	for _, argument := range arguments[1:] {
		if argument == "--" {
			optionsEnded = true
			continue
		}
		switch command {
		case "find":
			if argument == "-exec" || argument == "-execdir" ||
				argument == "-ok" || argument == "-okdir" || argument == "-delete" ||
				argument == "-fls" || argument == "-fprint" || argument == "-fprint0" ||
				argument == "-fprintf" {
				return errors.New("tokenbench command runner: find delegation or mutation is forbidden")
			}
		case "rg":
			if !optionsEnded && (argument == "--pre" || strings.HasPrefix(argument, "--pre=") ||
				argument == "--hostname-bin" || strings.HasPrefix(argument, "--hostname-bin=") ||
				argument == "--search-zip" ||
				strings.HasPrefix(argument, "-") && !strings.HasPrefix(argument, "--") &&
					strings.ContainsRune(argument[1:], 'z')) {
				return errors.New("tokenbench command runner: ripgrep program delegation is forbidden")
			}
		case "sort":
			// GNU long options accept unique abbreviations. Reject every prefix it
			// accepts for --compress-program and --output, plus every short-option
			// cluster containing -o, not only the standalone forms.
			if !optionsEnded && (strings.HasPrefix(argument, "--co") ||
				strings.HasPrefix(argument, "--o") ||
				strings.HasPrefix(argument, "--t") ||
				strings.HasPrefix(argument, "-") && !strings.HasPrefix(argument, "--") &&
					strings.ContainsAny(argument[1:], "oT")) {
				return errors.New("tokenbench command runner: sort delegation or file output is forbidden")
			}
		}
	}
	return nil
}

var approvedExternalCommands = map[string]struct{}{
	"cat": {}, "cut": {}, "find": {}, "grep": {}, "head": {}, "ls": {},
	"rg": {}, "sort": {}, "tail": {}, "tr": {}, "wc": {},
}

func executePipeline(
	ctx context.Context,
	pipeline [][]string,
	stdin io.Reader,
	stdout, stderr io.Writer,
) (int, error) {
	if len(pipeline) == 0 {
		return 0, errors.New("command pipeline is empty")
	}
	if stdout == nil {
		stdout = io.Discard
	}
	if stderr == nil {
		stderr = io.Discard
	}
	commands := make([]*exec.Cmd, len(pipeline))
	executableFiles := make([]*os.File, len(pipeline))
	defer closeExecutableFiles(executableFiles)
	synchronizedStderr := &synchronizedWriter{writer: stderr}
	for index, arguments := range pipeline {
		if err := validateExternalCommand(arguments); err != nil {
			return 0, err
		}
		command, executableFile, err := processpolicy.NativeCommandContext(
			ctx,
			arguments[0],
			arguments[1:]...,
		)
		if err != nil {
			return 0, fmt.Errorf("open approved native command %q: %w", arguments[0], err)
		}
		command.Env = closedCommandEnvironment()
		command.Stderr = synchronizedStderr
		commands[index] = command
		executableFiles[index] = executableFile
	}

	pipes := make([][2]*os.File, len(commands)-1)
	for index := range pipes {
		reader, writer, err := os.Pipe()
		if err != nil {
			closePipelinePipes(pipes)
			return 0, fmt.Errorf("create pipeline %d: %w", index, err)
		}
		pipes[index] = [2]*os.File{reader, writer}
		commands[index].Stdout = writer
		commands[index+1].Stdin = reader
	}
	commands[0].Stdin = stdin
	commands[len(commands)-1].Stdout = &synchronizedWriter{writer: stdout}

	started := make([]*exec.Cmd, 0, len(commands))
	for index := len(commands) - 1; index >= 0; index-- {
		startErr := commands[index].Start()
		_ = executableFiles[index].Close()
		executableFiles[index] = nil
		if startErr != nil {
			closePipelinePipes(pipes)
			for _, command := range started {
				if command.Process != nil {
					_ = command.Process.Kill()
				}
			}
			for _, command := range started {
				_ = command.Wait()
			}
			return 0, fmt.Errorf("start pipeline stage %d: %w", index, startErr)
		}
		started = append(started, commands[index])
	}
	closePipelinePipes(pipes)

	var finalError error
	for index, command := range commands {
		waitErr := command.Wait()
		if index == len(commands)-1 {
			finalError = waitErr
		}
	}
	if finalError == nil {
		return 0, nil
	}
	var exitError *exec.ExitError
	if errors.As(finalError, &exitError) && exitError.ExitCode() >= 0 {
		return exitError.ExitCode(), nil
	}
	return 0, fmt.Errorf("wait for final pipeline stage: %w", finalError)
}

func closeExecutableFiles(files []*os.File) {
	for _, file := range files {
		if file != nil {
			_ = file.Close()
		}
	}
}

func closedCommandEnvironment() []string {
	return []string{
		"HOME=/",
		"LC_ALL=C",
		"PATH=",
		"TZ=UTC",
	}
}

func closePipelinePipes(pipes [][2]*os.File) {
	for _, pipe := range pipes {
		if pipe[0] != nil {
			_ = pipe[0].Close()
		}
		if pipe[1] != nil {
			_ = pipe[1].Close()
		}
	}
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
