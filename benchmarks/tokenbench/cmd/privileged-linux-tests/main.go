package main

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strings"

	"github.com/yapless/scopesifter/internal/processpolicy"
)

const (
	requiredEnvironment             = "TOKENBENCH_REQUIRE_PRIVILEGED_TESTS"
	containerEnvironment            = "TOKENBENCH_PRIVILEGED_CONTAINER"
	commandRunnerImageEnvironment   = "TOKENBENCH_COMMAND_RUNNER_IMAGE"
	commandRunnerUtilityEnvironment = "TOKENBENCH_COMMAND_RUNNER_UTILITY"
	commandRunnerUtilityFlag        = "--command-runner-utility"
	pinnedImageDefault              = "golang:1.26.5-bookworm@sha256:6c5605ab3a9a9fb3c4eafe5b3d63cdbf3881caf113262b67862547b54a9db599"
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
	case len(args) == 2 && args[0] == commandRunnerUtilityFlag:
		return runCommandRunnerUtility(args[1], stdout, stderr)
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
	engine := os.Getenv("TOKENBENCH_CONTAINER_ENGINE")
	if engine == "" {
		engine = "docker"
	}
	if err := validateContainerEngineExecutable(engine); err != nil {
		return err
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

	dockerArguments := privilegedContainerArguments(
		repositoryRoot,
		binaryDirectory,
		image,
		hostMountNamespace,
		hostCgroupNamespace,
	)
	if err := validateContainerEngineInvocation(engine, dockerArguments); err != nil {
		return err
	}
	command, engineFile, err := processpolicy.NativeCommand(engine, dockerArguments...)
	if err != nil {
		return fmt.Errorf("pin native container engine %q: %w", engine, err)
	}
	command.Stdout = stdout
	command.Stderr = stderr
	commandErr := command.Run()
	closeErr := engineFile.Close()
	if err := errors.Join(commandErr, closeErr); err != nil {
		return fmt.Errorf("run privileged container: %w", err)
	}
	return nil
}

func privilegedContainerArguments(
	repositoryRoot,
	binaryDirectory,
	image,
	hostMountNamespace,
	hostCgroupNamespace string,
) []string {
	return []string{
		"run", "--rm", "--privileged", "--cgroupns=private", "--network=none",
		"--platform=linux/amd64",
		"--entrypoint=/tokenbench-tests/privileged-linux-tests",
		"--tmpfs", "/tmp:rw,nosuid,nodev,exec,size=1g",
		"--mount", "type=bind,src=" + repositoryRoot + ",dst=/workspace,readonly",
		"--mount", "type=bind,src=" + binaryDirectory + ",dst=/tokenbench-tests,readonly",
		"--env", containerEnvironment + "=1",
		"--env", "TOKENBENCH_HOST_MOUNT_NAMESPACE=" + hostMountNamespace,
		"--env", "TOKENBENCH_HOST_CGROUP_NAMESPACE=" + hostCgroupNamespace,
		image,
		"--container",
	}
}

func validateContainerEngineExecutable(engine string) error {
	if err := processpolicy.ValidateExecutable(engine); err != nil {
		return fmt.Errorf("validate configured container engine: %w", err)
	}
	switch filepath.Base(filepath.Clean(engine)) {
	case "docker", "nerdctl", "podman":
		return nil
	default:
		return fmt.Errorf("configured container engine %q is not an approved native engine", engine)
	}
}

func validateContainerEngineInvocation(engine string, arguments []string) error {
	if err := validateContainerEngineExecutable(engine); err != nil {
		return err
	}
	if len(arguments) != 21 {
		return errors.New("privileged container invocation has an unexpected argument count")
	}
	fixed := map[int]string{
		0: "run", 1: "--rm", 2: "--privileged", 3: "--cgroupns=private", 4: "--network=none",
		5: "--platform=linux/amd64", 6: "--entrypoint=/tokenbench-tests/privileged-linux-tests",
		7: "--tmpfs", 8: "/tmp:rw,nosuid,nodev,exec,size=1g",
		9: "--mount", 11: "--mount", 13: "--env", 14: containerEnvironment + "=1",
		15: "--env", 17: "--env", 20: "--container",
	}
	for index, expected := range fixed {
		if arguments[index] != expected {
			return fmt.Errorf("privileged container argument %d does not match its fixed role", index)
		}
	}
	if err := validateReadOnlyBindMount(arguments[10], "/workspace"); err != nil {
		return fmt.Errorf("validate repository mount: %w", err)
	}
	if err := validateReadOnlyBindMount(arguments[12], "/tokenbench-tests"); err != nil {
		return fmt.Errorf("validate test-binary mount: %w", err)
	}
	if err := validateNamespaceEnvironment(arguments[16], "TOKENBENCH_HOST_MOUNT_NAMESPACE", "mnt"); err != nil {
		return err
	}
	if err := validateNamespaceEnvironment(arguments[18], "TOKENBENCH_HOST_CGROUP_NAMESPACE", "cgroup"); err != nil {
		return err
	}
	if err := validatePinnedImage(arguments[19]); err != nil {
		return err
	}
	return nil
}

func validateReadOnlyBindMount(argument, destination string) error {
	const prefix = "type=bind,src="
	suffix := ",dst=" + destination + ",readonly"
	if !strings.HasPrefix(argument, prefix) || !strings.HasSuffix(argument, suffix) {
		return errors.New("bind mount does not match its fixed read-only shape")
	}
	source := strings.TrimSuffix(strings.TrimPrefix(argument, prefix), suffix)
	if !filepath.IsAbs(source) || filepath.Clean(source) != source || strings.Contains(source, ",") {
		return errors.New("bind mount source is not a clean absolute path")
	}
	return nil
}

func validateNamespaceEnvironment(argument, key, kind string) error {
	prefix := key + "=" + kind + ":["
	if !strings.HasPrefix(argument, prefix) || !strings.HasSuffix(argument, "]") {
		return fmt.Errorf("%s does not contain a kernel namespace identity", key)
	}
	identity := strings.TrimSuffix(strings.TrimPrefix(argument, prefix), "]")
	if identity == "" || strings.Trim(identity, "0123456789") != "" {
		return fmt.Errorf("%s contains an invalid kernel namespace identity", key)
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
	if strings.HasPrefix(parts[0], "-") || strings.ContainsAny(parts[0], " \t\r\n,=") {
		return errors.New("container image name contains prohibited option or field delimiters")
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
	buildEnvironment := privilegedBuildEnvironment(os.Environ(), repositoryRoot)
	for _, spec := range testBinaries {
		arguments := []string{
			"test", "-mod=readonly", "-c",
			"-o", filepath.Join(binaryDirectory, spec.output),
			spec.packagePath,
		}
		if err := runBuild(repositoryRoot, binaryDirectory, buildEnvironment, arguments, stdout, stderr); err != nil {
			return fmt.Errorf("compile %s: %w", spec.packagePath, err)
		}
	}
	arguments := []string{
		"build", "-mod=readonly", "-trimpath",
		"-o", filepath.Join(binaryDirectory, "tokenbench"),
		"./benchmarks/tokenbench/cmd/tokenbench",
	}
	if err := runBuild(repositoryRoot, binaryDirectory, buildEnvironment, arguments, stdout, stderr); err != nil {
		return fmt.Errorf("compile production tokenbench command runner: %w", err)
	}
	arguments = []string{
		"build", "-mod=readonly", "-trimpath",
		"-o", filepath.Join(binaryDirectory, "privileged-linux-tests"),
		"./benchmarks/tokenbench/cmd/privileged-linux-tests",
	}
	if err := runBuild(repositoryRoot, binaryDirectory, buildEnvironment, arguments, stdout, stderr); err != nil {
		return fmt.Errorf("compile privileged test coordinator: %w", err)
	}
	return nil
}

func runBuild(directory, binaryDirectory string, environment, arguments []string, stdout, stderr io.Writer) error {
	if err := validateBuildInvocation("go", directory, binaryDirectory, environment, arguments); err != nil {
		return err
	}
	command, goFile, err := processpolicy.NativeCommand("go", arguments...)
	if err != nil {
		return fmt.Errorf("pin native Go compiler: %w", err)
	}
	command.Dir = directory
	command.Env = environment
	command.Stdout = stdout
	command.Stderr = stderr
	return errors.Join(command.Run(), goFile.Close())
}

func validateBuildInvocation(goPath, directory, binaryDirectory string, environment, arguments []string) error {
	if err := processpolicy.ValidateExecutable(goPath); err != nil {
		return fmt.Errorf("validate Go compiler executable: %w", err)
	}
	if filepath.Base(filepath.Clean(goPath)) != "go" {
		return errors.New("build executable does not have the exact Go compiler role")
	}
	repositoryRoot, err := findRepositoryRoot()
	if err != nil {
		return err
	}
	if directory != repositoryRoot || !filepath.IsAbs(directory) || filepath.Clean(directory) != directory {
		return errors.New("build directory is not the active repository root")
	}
	if !filepath.IsAbs(binaryDirectory) || filepath.Clean(binaryDirectory) != binaryDirectory {
		return errors.New("build output directory is not a clean absolute path")
	}
	if err := validateBuildEnvironment(environment, directory); err != nil {
		return err
	}
	if len(arguments) != 6 || arguments[3] != "-o" {
		return errors.New("go build invocation has an unexpected argument shape")
	}
	output := arguments[4]
	if output != filepath.Join(binaryDirectory, filepath.Base(output)) {
		return errors.New("go build output escapes the dedicated binary directory")
	}
	packagePath, found := buildPackageForOutput(filepath.Base(output))
	if !found || arguments[5] != packagePath {
		return errors.New("go build invocation does not match an approved output and package role")
	}
	if strings.HasSuffix(filepath.Base(output), ".test") {
		if !slices.Equal(arguments[:4], []string{"test", "-mod=readonly", "-c", "-o"}) {
			return errors.New("go test-binary build flags do not match the fixed grammar")
		}
	} else if !slices.Equal(arguments[:4], []string{"build", "-mod=readonly", "-trimpath", "-o"}) {
		return errors.New("go command build flags do not match the fixed grammar")
	}
	return nil
}

func validateBuildEnvironment(environment []string, directory string) error {
	required := map[string]string{
		"CGO_ENABLED": "0",
		"GOARCH":      "amd64",
		"GOAUTH":      "off",
		"GOENV":       "off",
		"GOFLAGS":     "-buildvcs=false",
		"GONOPROXY":   "none",
		"GONOSUMDB":   "",
		"GOOS":        "linux",
		"GOPRIVATE":   "",
		"GOPROXY":     "https://proxy.golang.org",
		"GOSUMDB":     "sum.golang.org",
		"GOTOOLCHAIN": "local",
		"GOVCS":       "*:off",
		"GOWORK":      "off",
		"PWD":         directory,
	}
	seen := make(map[string]bool, len(required))
	for _, entry := range environment {
		key, value, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		expected, constrained := required[key]
		if buildEnvironmentVariableControlled(key) && !constrained {
			return fmt.Errorf("build environment retains prohibited %s", key)
		}
		if !constrained {
			continue
		}
		if seen[key] || value != expected {
			return fmt.Errorf("build environment does not bind %s exactly", key)
		}
		seen[key] = true
	}
	for key := range required {
		if !seen[key] {
			return fmt.Errorf("build environment omits required binding %s", key)
		}
	}
	return nil
}

func privilegedBuildEnvironment(environment []string, directory string) []string {
	replacements := map[string]string{
		"CGO_ENABLED": "0",
		"GOARCH":      "amd64",
		"GOAUTH":      "off",
		"GOENV":       "off",
		"GOFLAGS":     "-buildvcs=false",
		"GONOPROXY":   "none",
		"GONOSUMDB":   "",
		"GOOS":        "linux",
		"GOPRIVATE":   "",
		"GOPROXY":     "https://proxy.golang.org",
		"GOSUMDB":     "sum.golang.org",
		"GOTOOLCHAIN": "local",
		"GOVCS":       "*:off",
		"GOWORK":      "off",
		"PWD":         directory,
	}
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, found := strings.Cut(entry, "=")
		if !found || buildEnvironmentVariableControlled(name) {
			continue
		}
		filtered = append(filtered, entry)
	}
	return replaceEnvironment(filtered, replacements)
}

func buildEnvironmentVariableControlled(name string) bool {
	if strings.HasPrefix(name, "GO") || strings.HasPrefix(name, "CGO") {
		return true
	}
	switch name {
	case "AR", "CC", "CXX", "FC", "GCCGO", "PKG_CONFIG", "PWD":
		return true
	default:
		return false
	}
}

func buildPackageForOutput(output string) (string, bool) {
	switch output {
	case "runner.test":
		return "./benchmarks/tokenbench/runner", true
	case "snapshot.test":
		return "./benchmarks/tokenbench/snapshot", true
	case "source.test":
		return "./benchmarks/tokenbench/source", true
	case "workspace.test":
		return "./benchmarks/tokenbench/workspace", true
	case "tokenbench-command.test", "tokenbench":
		return "./benchmarks/tokenbench/cmd/tokenbench", true
	case "privileged-linux-tests":
		return "./benchmarks/tokenbench/cmd/privileged-linux-tests", true
	default:
		return "", false
	}
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
