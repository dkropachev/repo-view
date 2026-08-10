//go:build linux

package selfexec

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const helperEnvironment = "TOKENBENCH_SELFEXEC_HELPER"

func TestCurrentPinsRunningExecutable(t *testing.T) {
	identity, err := Current()
	if err != nil {
		t.Fatalf("Current() error = %v", err)
	}
	if !filepath.IsAbs(identity.Path) || filepath.Clean(identity.Path) != identity.Path {
		t.Fatalf("Current().Path = %q, want canonical absolute path", identity.Path)
	}
	if len(identity.SHA256) != sha256HexLength || strings.ToLower(identity.SHA256) != identity.SHA256 {
		t.Fatalf("Current().SHA256 = %q, want lowercase SHA-256", identity.SHA256)
	}
	again, err := Current()
	if err != nil {
		t.Fatalf("second Current() error = %v", err)
	}
	if again != identity {
		t.Fatalf("second Current() = %#v, want %#v", again, identity)
	}
}

func TestCurrentRejectsByteIdenticalPathReplacement(t *testing.T) {
	testExecutable, err := os.Executable()
	if err != nil {
		t.Fatalf("os.Executable() error = %v", err)
	}
	testInfo, err := os.Stat(testExecutable)
	if err != nil {
		t.Fatalf("stat test executable: %v", err)
	}

	tempDir := t.TempDir()
	helperPath := filepath.Join(tempDir, "selfexec-helper")
	replacementPath := filepath.Join(tempDir, "selfexec-replacement")
	copyExecutable(t, testExecutable, helperPath, testInfo.Mode().Perm())
	copyExecutable(t, testExecutable, replacementPath, testInfo.Mode().Perm())

	command := exec.Command(helperPath, "-test.run=^TestSelfExecutableHelperProcess$")
	command.Env = append(os.Environ(), helperEnvironment+"=1")
	stdin, err := command.StdinPipe()
	if err != nil {
		t.Fatalf("create helper stdin: %v", err)
	}
	stdout, err := command.StdoutPipe()
	if err != nil {
		t.Fatalf("create helper stdout: %v", err)
	}
	var stderr bytes.Buffer
	command.Stderr = &stderr
	if err := command.Start(); err != nil {
		t.Fatalf("start helper: %v", err)
	}
	waited := false
	t.Cleanup(func() {
		if waited {
			return
		}
		_ = command.Process.Kill()
		_ = command.Wait()
	})

	output := bufio.NewReader(stdout)
	ready, err := output.ReadString('\n')
	if err != nil {
		t.Fatalf("read helper readiness: %v (stderr: %s)", err, stderr.String())
	}
	if !strings.HasPrefix(ready, "READY ") {
		t.Fatalf("helper readiness = %q, want READY (stderr: %s)", ready, stderr.String())
	}

	// Replace the path atomically with a byte-identical executable. A verifier
	// that re-reads only the mutable pathname will see the expected digest even
	// though the running inode has been unlinked.
	if err := os.Rename(replacementPath, helperPath); err != nil {
		t.Fatalf("replace live executable path: %v", err)
	}
	if _, err := io.WriteString(stdin, "verify\n"); err != nil {
		t.Fatalf("release helper: %v", err)
	}
	if err := stdin.Close(); err != nil {
		t.Fatalf("close helper stdin: %v", err)
	}
	rejected, err := output.ReadString('\n')
	if err != nil {
		t.Fatalf("read helper result: %v (stderr: %s)", err, stderr.String())
	}
	if err := command.Wait(); err != nil {
		waited = true
		t.Fatalf("helper failed: %v (result: %s, stderr: %s)", err, rejected, stderr.String())
	}
	waited = true
	if !strings.HasPrefix(rejected, "REJECTED ") {
		t.Fatalf("helper result = %q, want REJECTED", rejected)
	}
}

func TestSelfExecutableHelperProcess(t *testing.T) {
	if os.Getenv(helperEnvironment) != "1" {
		return
	}
	identity, err := Current()
	if err != nil {
		fmt.Fprintf(os.Stderr, "initial identity: %v\n", err)
		os.Exit(2)
	}
	fmt.Printf("READY %s\n", identity.SHA256)
	if _, err := bufio.NewReader(os.Stdin).ReadString('\n'); err != nil {
		fmt.Fprintf(os.Stderr, "wait for replacement: %v\n", err)
		os.Exit(3)
	}
	if _, err := Current(); err == nil {
		fmt.Println("ACCEPTED")
		os.Exit(4)
	} else {
		fmt.Printf("REJECTED %v\n", err)
	}
	os.Exit(0)
}

func copyExecutable(t *testing.T, source, destination string, mode os.FileMode) {
	t.Helper()
	input, err := os.Open(source)
	if err != nil {
		t.Fatalf("open executable source: %v", err)
	}
	defer input.Close()
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, mode)
	if err != nil {
		t.Fatalf("create executable copy: %v", err)
	}
	if _, err := io.Copy(output, input); err != nil {
		_ = output.Close()
		t.Fatalf("copy executable: %v", err)
	}
	if err := output.Close(); err != nil {
		t.Fatalf("close executable copy: %v", err)
	}
}

const sha256HexLength = 64
