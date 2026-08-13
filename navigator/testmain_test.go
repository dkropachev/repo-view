package navigator

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

const movingGitTestConfigName = "moving-git.json"

type movingGitTestConfig struct {
	RealGit string `json:"real_git"`
	Commit  string `json:"commit"`
	State   string `json:"state"`
}

func TestMain(m *testing.M) {
	executable, err := os.Executable()
	if err == nil {
		switch filepath.Base(executable) {
		case "git":
			os.Exit(runNavigatorGitTestHelper(filepath.Dir(executable)))
		case "fsmonitor-test", "filter-test":
			marker := executable + ".marker"
			if err := os.WriteFile(marker, []byte("executed"), 0o600); err != nil {
				os.Exit(98)
			}
			os.Exit(0)
		}
	}
	os.Exit(m.Run())
}

func runNavigatorGitTestHelper(directory string) int {
	configPath := filepath.Join(directory, movingGitTestConfigName)
	raw, err := os.ReadFile(configPath)
	if errors.Is(err, os.ErrNotExist) {
		marker := filepath.Join(directory, "git-ran")
		if err := os.WriteFile(marker, []byte("intercepted"), 0o600); err != nil {
			return 98
		}
		return 99
	}
	if err != nil {
		return 98
	}
	var config movingGitTestConfig
	if err := json.Unmarshal(raw, &config); err != nil ||
		config.RealGit == "" || config.Commit == "" || config.State == "" {
		return 98
	}
	flip := false
	for _, argument := range os.Args[1:] {
		if argument == "HEAD" || argument == "HEAD^{commit}" {
			flip = true
			break
		}
	}
	status := runTestGit(config.RealGit, os.Args[1:], os.Stdout, os.Stderr)
	if !flip || status != 0 {
		return status
	}
	state, err := os.OpenFile(config.State, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if errors.Is(err, os.ErrExist) {
		return status
	}
	if err != nil {
		return 98
	}
	if err := state.Close(); err != nil {
		return 98
	}
	if runTestGit(
		config.RealGit,
		[]string{"update-ref", "HEAD", config.Commit},
		io.Discard,
		io.Discard,
	) != 0 {
		return 97
	}
	return status
}

func runTestGit(path string, arguments []string, stdout, stderr io.Writer) int {
	command := exec.Command(path, arguments...)
	command.Stdin = os.Stdin
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()
	if err == nil {
		return 0
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitCode()
	}
	return 98
}

func copyNavigatorTestExecutable(t *testing.T, path string) {
	t.Helper()
	testExecutable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(testExecutable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o700); err != nil {
		t.Fatal(err)
	}
}

func assertNavigatorFSMonitor(t *testing.T, executable string) {
	t.Helper()
	if output, err := exec.Command(executable).CombinedOutput(); err != nil {
		t.Fatalf("fsmonitor fixture: %v: %s", err, output)
	}
	marker := executable + ".marker"
	content, err := os.ReadFile(marker)
	if err != nil || string(content) != "executed" {
		t.Fatalf("fsmonitor fixture marker = %q, %v", content, err)
	}
	if err := os.Remove(marker); err != nil {
		t.Fatal(err)
	}
}

func writeMovingGitTestConfig(t *testing.T, directory string, config movingGitTestConfig) {
	t.Helper()
	raw, err := json.Marshal(config)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(directory, movingGitTestConfigName), raw, 0o600); err != nil {
		t.Fatal(err)
	}
}
