package grammargen

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
)

type commandRunner interface {
	run(context.Context, string, string, ...string) ([]byte, error)
}

type executableRunner struct{}

func (executableRunner) run(
	ctx context.Context,
	directory string,
	name string,
	arguments ...string,
) ([]byte, error) {
	command := exec.CommandContext(ctx, name, arguments...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		output = bytes.TrimSpace(output)
		if len(output) > 0 {
			return output, fmt.Errorf("run %s: %w: %s", name, err, output)
		}
		return output, fmt.Errorf("run %s: %w", name, err)
	}
	return output, nil
}
