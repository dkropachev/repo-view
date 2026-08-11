package navigator

import (
	"bufio"
	"fmt"
	"os/exec"
	"strings"
)

const maximumGitTreeEntryBytes = 1 << 20

type gitRevisionOutputBuffer struct {
	data      []byte
	limit     int
	truncated bool
}

func (w *gitRevisionOutputBuffer) Write(input []byte) (int, error) {
	remaining := max(0, w.limit-len(w.data))
	w.data = append(w.data, input[:min(len(input), remaining)]...)
	if len(input) > remaining {
		w.truncated = true
	}
	return len(input), nil
}

func canonicalGitObjectID(objectID string) bool {
	if len(objectID) != 40 && len(objectID) != 64 {
		return false
	}
	for _, character := range []byte(objectID) {
		if (character < '0' || character > '9') &&
			(character < 'a' || character > 'f') {
			return false
		}
	}
	return true
}

func boundedGitRevisionOutput(cmd *exec.Cmd, limit int) ([]byte, error) {
	stdout := &gitRevisionOutputBuffer{limit: limit}
	stderr := &gitRevisionOutputBuffer{limit: maximumGitStderrBytes}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if err := cmd.Run(); err != nil {
		if detail := strings.TrimSpace(string(stderr.data)); detail != "" {
			return nil, fmt.Errorf("%w: %s", err, detail)
		}
		return nil, err
	}
	if stdout.truncated {
		return nil, fmt.Errorf("git metadata output exceeds %d bytes", limit)
	}
	return stdout.data, nil
}

func (r *View) validateGitRevisionRegularFile(
	relative, revision string,
) error {
	output, err := boundedGitRevisionOutput(
		r.gitCommand("ls-tree", "-z", revision, "--", relative),
		maximumGitTreeEntryBytes,
	)
	if err != nil {
		return fmt.Errorf("inspect %q at Git revision %s: %w", relative, revision, err)
	}
	if len(output) == 0 {
		return fmt.Errorf("git revision %s does not contain %q", revision, relative)
	}
	if output[len(output)-1] != 0 || strings.Count(string(output), "\x00") != 1 {
		return fmt.Errorf("git revision %s returned invalid metadata for %q", revision, relative)
	}
	record := output[:len(output)-1]
	metadata, path, found := strings.Cut(string(record), "\t")
	fields := strings.Fields(metadata)
	if !found || path != relative || len(fields) != 3 ||
		(fields[0] != "100644" && fields[0] != "100755") || fields[1] != "blob" {
		return fmt.Errorf("git revision %s path %q is not a regular file", revision, relative)
	}
	return nil
}

// readGitLinesAtRevision reads a regular source file from an immutable Git
// tree rather than from the working tree. Revisions must already be resolved
// to a canonical object ID so a path can never be interpreted as an option or
// as part of a caller-controlled revision expression.
func (r *View) readGitLinesAtRevision(
	relative, revision string,
) ([]string, string, error) {
	clean, err := cleanRepositoryPath(relative)
	if err != nil {
		return nil, "", err
	}
	if !canonicalGitObjectID(revision) {
		return nil, "", fmt.Errorf("git revision is not a canonical object ID: %q", revision)
	}
	if err := r.validateGitRevisionRegularFile(clean, revision); err != nil {
		return nil, "", err
	}

	cmd := r.gitCommand("cat-file", "blob", revision+":"+clean)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, "", err
	}
	stderr := &gitRevisionOutputBuffer{limit: maximumGitStderrBytes}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return nil, "", err
	}

	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 1024), maximumSourceLineBytes)
	var lines []string
	for scanner.Scan() {
		lines = append(lines, scanner.Text())
	}
	if scanErr := scanner.Err(); scanErr != nil {
		_ = cmd.Process.Kill()
		_ = stdout.Close()
		_ = cmd.Wait()
		return nil, "", fmt.Errorf(
			"read %q at Git revision %s: %w", clean, revision, scanErr,
		)
	}
	if waitErr := cmd.Wait(); waitErr != nil {
		detail := strings.TrimSpace(string(stderr.data))
		if detail != "" {
			return nil, "", fmt.Errorf(
				"read %q at Git revision %s: %w: %s",
				clean, revision, waitErr, detail,
			)
		}
		return nil, "", fmt.Errorf(
			"read %q at Git revision %s: %w", clean, revision, waitErr,
		)
	}
	return lines, clean, nil
}
