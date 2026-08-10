//go:build linux

package source

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestPinnedGitExecutableRejectsAtomicPathSubstitution(t *testing.T) {
	realGit, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	realGit, err = filepath.EvalSymlinks(realGit)
	if err != nil {
		t.Fatal(err)
	}
	realInfo, err := os.Stat(realGit)
	if err != nil {
		t.Fatal(err)
	}

	directory := t.TempDir()
	gitPath := filepath.Join(directory, "git")
	copyExecutableForTest(t, realGit, gitPath, realInfo.Mode().Perm())
	runner, err := resolveGitRunnerAt(gitPath)
	if err != nil {
		t.Fatal(err)
	}
	defer runner.close()

	marker := filepath.Join(directory, "substituted-executable-ran")
	maliciousPath := filepath.Join(directory, "malicious-git")
	buildMarkerExecutable(t, maliciousPath, marker)
	swappedWith := ""
	t.Cleanup(func() {
		if swappedWith != "" {
			_ = exchangePaths(gitPath, swappedWith)
		}
	})

	if err := exchangePaths(gitPath, maliciousPath); err != nil {
		t.Fatalf("atomically substitute Git path: %v", err)
	}
	swappedWith = maliciousPath

	// Exercise the launch primitive directly while the display path names the
	// malicious inode. Execution must still use the retained trusted descriptor.
	command, err := newPinnedGitCommand(
		context.Background(),
		runner.executable,
		runner.path,
		[]string{"-C", directory, "--version"},
	)
	if err != nil {
		t.Fatal(err)
	}
	command.Env = gitEnvironment()
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("execute retained Git inode: %v: %s", err, output)
	}
	if !strings.Contains(string(output), "git version") {
		t.Fatalf("retained Git output = %q", output)
	}
	assertMarkerAbsent(t, marker)

	// The complete runner also requires its canonical display path to remain
	// bound to the retained inode, so a substituted path fails before launch.
	if _, err := runner.output(context.Background(), directory, "--version"); err == nil ||
		!strings.Contains(err.Error(), "no longer identifies the opened inode") {
		t.Fatalf("runner accepted substituted Git path: %v", err)
	}
	assertMarkerAbsent(t, marker)

	if err := exchangePaths(gitPath, maliciousPath); err != nil {
		t.Fatalf("restore Git path: %v", err)
	}
	swappedWith = ""
	if _, err := runner.output(context.Background(), directory, "--version"); err != nil {
		t.Fatalf("runner did not recover after exact inode restoration: %v", err)
	}

	// A byte-identical replacement has the expected digest but is still a new
	// inode and therefore cannot satisfy the retained execution identity.
	identicalPath := filepath.Join(directory, "byte-identical-git")
	copyExecutableForTest(t, gitPath, identicalPath, realInfo.Mode().Perm())
	if err := exchangePaths(gitPath, identicalPath); err != nil {
		t.Fatalf("atomically substitute byte-identical Git path: %v", err)
	}
	swappedWith = identicalPath
	if err := runner.verify(); err == nil ||
		!strings.Contains(err.Error(), "no longer identifies the opened inode") {
		t.Fatalf("runner accepted byte-identical replacement inode: %v", err)
	}
	if err := exchangePaths(gitPath, identicalPath); err != nil {
		t.Fatalf("restore Git after byte-identical substitution: %v", err)
	}
	swappedWith = ""
	if err := runner.verify(); err != nil {
		t.Fatalf("runner did not verify restored Git inode: %v", err)
	}
}

func exchangePaths(left, right string) error {
	return unix.Renameat2(
		unix.AT_FDCWD,
		left,
		unix.AT_FDCWD,
		right,
		unix.RENAME_EXCHANGE,
	)
}

func copyExecutableForTest(
	t *testing.T,
	source string,
	destination string,
	mode os.FileMode,
) {
	t.Helper()
	input, err := os.Open(source)
	if err != nil {
		t.Fatal(err)
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		t.Fatal(err)
	}
	if err := output.Close(); err != nil {
		t.Fatal(err)
	}
}

func buildMarkerExecutable(t *testing.T, destination, marker string) {
	t.Helper()
	source := filepath.Join(t.TempDir(), "main.go")
	program := fmt.Sprintf(`package main

import "os"

func main() {
	if err := os.WriteFile(%q, []byte("executed"), 0600); err != nil {
		os.Exit(91)
	}
}
`, marker)
	if err := os.WriteFile(source, []byte(program), 0o600); err != nil {
		t.Fatal(err)
	}
	command := exec.Command("go", "build", "-o", destination, source)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("build substituted executable: %v: %s", err, output)
	}
}

func assertMarkerAbsent(t *testing.T, marker string) {
	t.Helper()
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("substituted Git executable ran")
	} else if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("inspect substituted-execution marker: %v", err)
	}
}
