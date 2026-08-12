package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"
)

const (
	requiredEnvironment  = "TOKENBENCH_REQUIRE_PRIVILEGED_TESTS"
	containerEnvironment = "TOKENBENCH_PRIVILEGED_CONTAINER"
	pinnedImageDefault   = "golang:1.26.5-bookworm@sha256:6c5605ab3a9a9fb3c4eafe5b3d63cdbf3881caf113262b67862547b54a9db599"
)

type binarySpec struct {
	output      string
	packagePath string
}

var testBinaries = []binarySpec{
	{output: "runner.test", packagePath: "./benchmarks/tokenbench/runner"},
	{output: "snapshot.test", packagePath: "./benchmarks/tokenbench/snapshot"},
	{output: "source.test", packagePath: "./benchmarks/tokenbench/source"},
	{output: "workspace.test", packagePath: "./benchmarks/tokenbench/workspace"},
	{output: "tokenbench-command.test", packagePath: "./benchmarks/tokenbench/cmd/tokenbench"},
}

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintf(os.Stderr, "tokenbench privileged tests: %v\n", err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	switch {
	case len(args) == 0:
		return hostMain(stdout, stderr)
	case len(args) == 1 && args[0] == "--container":
		if os.Getenv(containerEnvironment) != "1" {
			return errors.New("container mode requires its host marker")
		}
		return containerMain(stdout, stderr)
	case len(args) >= 4 && args[0] == "--cgroup-entry":
		return cgroupEntry(args[1], args[2], args[3:])
	default:
		return errors.New("usage: privileged-linux-tests [--container|--cgroup-entry DELEGATION BINARY ARG...]")
	}
}

func hostMain(stdout, stderr io.Writer) error {
	if runtime.GOOS != "linux" {
		return errors.New("the privileged lane requires Linux")
	}
	if runtime.GOARCH != "amd64" {
		return errors.New("the x32 seccomp probe requires an x86-64 runner")
	}
	if _, err := exec.LookPath("go"); err != nil {
		return errors.New("required command is unavailable: go")
	}

	engine := os.Getenv("TOKENBENCH_CONTAINER_ENGINE")
	if engine == "" {
		engine = "docker"
	}
	enginePath, err := exec.LookPath(engine)
	if err != nil {
		return fmt.Errorf("required container engine is unavailable: %s", engine)
	}
	image := os.Getenv("TOKENBENCH_PRIVILEGED_IMAGE")
	if image == "" {
		image = pinnedImageDefault
	}
	if err := validatePinnedImage(image); err != nil {
		return err
	}

	repositoryRoot, err := findRepositoryRoot()
	if err != nil {
		return err
	}
	binaryDirectory, err := os.MkdirTemp("", "tokenbench-privileged-tests-")
	if err != nil {
		return fmt.Errorf("create test binary directory: %w", err)
	}
	defer os.RemoveAll(binaryDirectory)

	if err := compileLinuxBinaries(repositoryRoot, binaryDirectory, stdout, stderr); err != nil {
		return err
	}
	hostMountNamespace, err := os.Readlink("/proc/self/ns/mnt")
	if err != nil {
		return fmt.Errorf("read host mount namespace: %w", err)
	}
	hostCgroupNamespace, err := os.Readlink("/proc/self/ns/cgroup")
	if err != nil {
		return fmt.Errorf("read host cgroup namespace: %w", err)
	}

	dockerArguments := []string{
		"run", "--rm", "--privileged", "--cgroupns=private", "--network=none",
		"--platform=linux/amd64",
		"--tmpfs", "/tmp:rw,nosuid,nodev,exec,size=1g",
		"--mount", "type=bind,src=" + repositoryRoot + ",dst=/workspace,readonly",
		"--mount", "type=bind,src=" + binaryDirectory + ",dst=/tokenbench-tests,readonly",
		"--env", containerEnvironment + "=1",
		"--env", "TOKENBENCH_HOST_MOUNT_NAMESPACE=" + hostMountNamespace,
		"--env", "TOKENBENCH_HOST_CGROUP_NAMESPACE=" + hostCgroupNamespace,
		image,
		"/tokenbench-tests/privileged-linux-tests", "--container",
	}
	command := exec.Command(enginePath, dockerArguments...)
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("run privileged container: %w", err)
	}
	return nil
}

func validatePinnedImage(image string) error {
	const prefix = "@sha256:"
	if strings.Count(image, prefix) != 1 {
		return errors.New("container image must contain exactly one sha256 digest")
	}
	parts := strings.SplitN(image, prefix, 2)
	if parts[0] == "" || len(parts[1]) != 64 {
		return errors.New("container image must be pinned by a complete sha256 digest")
	}
	for _, character := range parts[1] {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return errors.New("container image sha256 digest must be lowercase hexadecimal")
		}
	}
	return nil
}

func findRepositoryRoot() (string, error) {
	directory, err := os.Getwd()
	if err != nil {
		return "", fmt.Errorf("read working directory: %w", err)
	}
	for {
		module, readErr := os.ReadFile(filepath.Join(directory, "go.mod"))
		if readErr == nil && strings.HasPrefix(string(module), "module github.com/yapless/scopesifter\n") {
			return directory, nil
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return "", errors.New("could not locate the scopesifter repository root")
		}
		directory = parent
	}
}

func compileLinuxBinaries(repositoryRoot, binaryDirectory string, stdout, stderr io.Writer) error {
	buildEnvironment := replaceEnvironment(os.Environ(), map[string]string{
		"CGO_ENABLED": "0",
		"GOOS":        "linux",
		"GOARCH":      "amd64",
	})
	for _, spec := range testBinaries {
		arguments := []string{
			"test", "-mod=readonly", "-c",
			"-o", filepath.Join(binaryDirectory, spec.output),
			spec.packagePath,
		}
		if err := runBuild(repositoryRoot, buildEnvironment, arguments, stdout, stderr); err != nil {
			return fmt.Errorf("compile %s: %w", spec.packagePath, err)
		}
	}
	arguments := []string{
		"build", "-mod=readonly", "-trimpath",
		"-o", filepath.Join(binaryDirectory, "privileged-linux-tests"),
		"./benchmarks/tokenbench/cmd/privileged-linux-tests",
	}
	if err := runBuild(repositoryRoot, buildEnvironment, arguments, stdout, stderr); err != nil {
		return fmt.Errorf("compile privileged test coordinator: %w", err)
	}
	return nil
}

func runBuild(directory string, environment, arguments []string, stdout, stderr io.Writer) error {
	command := exec.Command("go", arguments...)
	command.Dir = directory
	command.Env = environment
	command.Stdout = stdout
	command.Stderr = stderr
	return command.Run()
}

func replaceEnvironment(environment []string, replacements map[string]string) []string {
	result := make([]string, 0, len(environment)+len(replacements))
	for _, entry := range environment {
		key, _, found := strings.Cut(entry, "=")
		if _, replace := replacements[key]; found && replace {
			continue
		}
		result = append(result, entry)
	}
	keys := make([]string, 0, len(replacements))
	for key := range replacements {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	for _, key := range keys {
		result = append(result, key+"="+replacements[key])
	}
	return result
}

func testExpression(names []string) string {
	escaped := make([]string, len(names))
	for index, name := range names {
		escaped[index] = regexp.QuoteMeta(name)
	}
	return "^(" + strings.Join(escaped, "|") + ")$"
}

func validateTestOutput(output []byte, required []string) error {
	wanted := make(map[string]struct{}, len(required))
	for _, name := range required {
		if name == "" || strings.Contains(name, "/") {
			return fmt.Errorf("invalid required top-level test name %q", name)
		}
		if _, duplicate := wanted[name]; duplicate {
			return fmt.Errorf("duplicate required test name %q", name)
		}
		wanted[name] = struct{}{}
	}

	seen := make(map[string]int, len(required))
	runs := make(map[string]int, len(required))
	finalPass := false
	scanner := bufio.NewScanner(strings.NewReader(string(output)))
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if line == "PASS" {
			finalPass = true
		}
		statusLine := strings.TrimSpace(line)
		if strings.HasPrefix(statusLine, "=== RUN   ") {
			fields := strings.Fields(strings.TrimPrefix(statusLine, "=== RUN   "))
			if len(fields) == 0 {
				return errors.New("test binary emitted a malformed RUN line")
			}
			name := fields[0]
			if !strings.Contains(name, "/") {
				if _, ok := wanted[name]; !ok {
					return fmt.Errorf("unexpected top-level test ran: %s", name)
				}
				runs[name]++
			}
		}
		for _, status := range []string{"PASS", "SKIP", "FAIL"} {
			prefix := "--- " + status + ": "
			if !strings.HasPrefix(statusLine, prefix) {
				continue
			}
			fields := strings.Fields(strings.TrimPrefix(statusLine, prefix))
			if len(fields) == 0 {
				return errors.New("test binary emitted a malformed status line")
			}
			name := fields[0]
			if status == "SKIP" {
				return fmt.Errorf("required privileged test was skipped: %s", name)
			}
			if strings.Contains(name, "/") {
				continue
			}
			if _, ok := wanted[name]; !ok {
				return fmt.Errorf("unexpected top-level test ran: %s", name)
			}
			if status != "PASS" {
				return fmt.Errorf("required privileged test did not pass: %s", name)
			}
			seen[name]++
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("parse test output: %w", err)
	}
	if !finalPass {
		return errors.New("test binary omitted its final PASS marker")
	}
	for _, name := range required {
		if runs[name] != 1 {
			return fmt.Errorf("required test did not run exactly once: %s", name)
		}
		if seen[name] != 1 {
			return fmt.Errorf("required test did not pass exactly once: %s", name)
		}
	}
	return nil
}
