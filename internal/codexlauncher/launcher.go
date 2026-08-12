package codexlauncher

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"unicode"
)

var sanitizedGoVariables = []string{
	"GOOS",
	"GOARCH",
	"GO386",
	"GOAMD64",
	"GOARM",
	"GOARM64",
	"GOMIPS",
	"GOMIPS64",
	"GOPPC64",
	"GORISCV64",
	"GOWASM",
	"CGO_ENABLED",
	"CC",
	"CXX",
	"CGO_CFLAGS",
	"CGO_CPPFLAGS",
	"CGO_CXXFLAGS",
	"CGO_LDFLAGS",
	"PKG_CONFIG",
	"GOROOT",
	"GOEXPERIMENT",
	"GODEBUG",
}

var fixedGoVariables = map[string]string{
	"GO111MODULE": "on",
	"GOENV":       "off",
	"GOTOOLCHAIN": "local",
	"GOWORK":      "off",
	"GOFLAGS":     "-mod=readonly -trimpath -buildvcs=false",
}

// Streams are the inherited standard streams for the build and Codex process.
type Streams struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

type process struct {
	name        string
	arguments   []string
	directory   string
	environment []string
	stdin       io.Reader
	stdout      io.Writer
	stderr      io.Writer
}

type executor interface {
	run(process) error
}

type osExecutor struct{}

func (osExecutor) run(p process) error {
	command := exec.Command(p.name, p.arguments...)
	command.Dir = p.directory
	command.Env = p.environment
	command.Stdin = p.stdin
	command.Stdout = p.stdout
	command.Stderr = p.stderr
	return command.Run()
}

// Run builds the mechanically capped scopesifter binary and runs Codex with it.
// Configuration failures use exit status 2; child-process failures preserve the
// child exit status.
func Run(root string, arguments []string, environment []string, streams Streams) int {
	return run(root, arguments, environment, streams, osExecutor{})
}

func run(
	root string,
	arguments []string,
	environment []string,
	streams Streams,
	execRunner executor,
) int {
	c, err := loadConfig(root, environment, arguments)
	if err != nil {
		fmt.Fprintln(streams.Stderr, err)
		return 2
	}

	binDir, status := prepareDirectories(c, streams.Stderr)
	if status != 0 {
		return status
	}
	transcriptPath := ""
	defer func() {
		if transcriptPath != "" {
			if removeErr := os.Remove(transcriptPath); removeErr != nil &&
				!errors.Is(removeErr, os.ErrNotExist) {
				fmt.Fprintf(streams.Stderr, "failed to remove navigation transcript: %v\n", removeErr)
			}
		}
		binaryPath := filepath.Join(binDir, "scopesifter")
		if removeErr := os.Remove(binaryPath); removeErr != nil &&
			!errors.Is(removeErr, os.ErrNotExist) {
			fmt.Fprintf(streams.Stderr, "failed to remove scopesifter binary: %v\n", removeErr)
		}
		if removeErr := os.Remove(binDir); removeErr != nil &&
			!errors.Is(removeErr, os.ErrNotExist) {
			fmt.Fprintf(streams.Stderr, "failed to remove scopesifter run directory: %v\n", removeErr)
		}
	}()

	linkerFlags := []string{
		"-X main.enforcedLimitCap=" + c.changedLimit,
		"-X main.enforcedContextCap=" + c.navigationContextCap,
		"-X main.enforcedMaxCodeLinesCap=" + c.changedMaxCodeLines,
		"-X main.enforcedMaxPatchLinesCap=" + c.changedMaxPatchLines,
	}
	if c.navigationSemanticsConfigured {
		optional := []string{
			"main.enforcedNavigationRoot=" + c.requiredNavigationRoot,
			"main.enforcedNavigationBaseCommit=" + c.requiredNavigationBaseCommit,
			"main.enforcedChangedReturn=" + c.requiredChangedReturn,
			"main.enforcedChangedContext=" + c.requiredChangedContext,
		}
		for _, argument := range optional {
			quoted, quoteErr := quoteLinkerArgument(argument)
			if quoteErr != nil {
				fmt.Fprintln(streams.Stderr, quoteErr)
				return 1
			}
			linkerFlags = append(linkerFlags, "-X "+quoted)
		}
		linkerFlags = append(linkerFlags, "-X main.enforcedNavigationSemantics=1")
	}
	if !decimalIsZero(c.navigationCommandCap) {
		transcript, createErr := os.CreateTemp(binDir, "navigation-transcript.*.jsonl")
		if createErr != nil {
			fmt.Fprintln(streams.Stderr, createErr)
			return 1
		}
		transcriptPath = transcript.Name()
		if closeErr := transcript.Close(); closeErr != nil {
			fmt.Fprintln(streams.Stderr, closeErr)
			return 1
		}
		quotedPath, quoteErr := quoteLinkerArgument(
			"main.enforcedNavigationTranscriptPath=" + transcriptPath,
		)
		if quoteErr != nil {
			fmt.Fprintln(streams.Stderr, quoteErr)
			return 1
		}
		linkerFlags = append(
			linkerFlags,
			"-X main.enforcedNavigationCommandCap="+c.navigationCommandCap,
			"-X "+quotedPath,
		)
	}

	buildEnvironment := replaceEnvironment(
		environment,
		sanitizedGoVariables,
		buildEnvironmentValues(root),
	)
	build := process{
		name: "go",
		arguments: []string{
			"build",
			"-ldflags",
			strings.Join(linkerFlags, " "),
			"-o",
			filepath.Join(binDir, "scopesifter"),
			"./cmd/scopesifter",
		},
		directory:   root,
		environment: buildEnvironment,
		stdin:       streams.Stdin,
		stdout:      streams.Stdout,
		stderr:      streams.Stderr,
	}
	if buildErr := execRunner.run(build); buildErr != nil {
		return reportProcessFailure(buildErr, streams.Stderr)
	}

	instructions := developerInstructions(c)
	codexArguments := []string{
		"-c",
		"developer_instructions=\"" + instructions + "\"",
	}
	if c.reasoningEffort != "" && c.reasoningEffort != "inherit" {
		codexArguments = append(
			codexArguments,
			"-c",
			"model_reasoning_effort=\""+c.reasoningEffort+"\"",
		)
	}
	codexArguments = append(codexArguments, arguments...)
	codexEnvironment := replaceEnvironment(
		environment,
		[]string{
			"SCOPESIFTER_NAVIGATION_COMMAND_CAP",
			"SCOPESIFTER_NAVIGATION_BUDGET_FILE",
		},
		map[string]string{
			"PATH":                            binDir + string(os.PathListSeparator) + environmentMap(environment)["PATH"],
			"SCOPESIFTER_LIMIT_CAP":           c.changedLimit,
			"SCOPESIFTER_CONTEXT_CAP":         c.navigationContextCap,
			"SCOPESIFTER_MAX_CODE_LINES_CAP":  c.changedMaxCodeLines,
			"SCOPESIFTER_MAX_PATCH_LINES_CAP": c.changedMaxPatchLines,
		},
	)
	codex := process{
		name:        "codex",
		arguments:   codexArguments,
		environment: codexEnvironment,
		stdin:       streams.Stdin,
		stdout:      streams.Stdout,
		stderr:      streams.Stderr,
	}
	if transcriptPath == "" {
		return reportProcessFailure(execRunner.run(codex), streams.Stderr)
	}

	transcript, openErr := os.OpenFile(
		transcriptPath,
		os.O_WRONLY|os.O_TRUNC|os.O_APPEND,
		0,
	)
	if openErr != nil {
		fmt.Fprintln(streams.Stderr, openErr)
		return 1
	}
	codex.stdout = io.MultiWriter(streams.Stdout, transcript)
	codexErr := execRunner.run(codex)
	closeErr := transcript.Close()
	if removeErr := os.Remove(transcriptPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
		fmt.Fprintln(streams.Stderr, removeErr)
		if codexErr == nil && closeErr == nil {
			return 1
		}
	}
	transcriptPath = ""
	if codexErr != nil {
		return reportProcessFailure(codexErr, streams.Stderr)
	}
	if closeErr != nil {
		fmt.Fprintln(streams.Stderr, closeErr)
		return 1
	}
	return 0
}

func buildEnvironmentValues(root string) map[string]string {
	values := make(map[string]string, len(fixedGoVariables)+1)
	for name, value := range fixedGoVariables {
		values[name] = value
	}
	values["PWD"] = root
	return values
}

func prepareDirectories(c config, stderr io.Writer) (string, int) {
	if err := validateDirectoryPath(c.cacheDir, "SCOPESIFTER_CACHE_DIR", true); err != nil {
		fmt.Fprintln(stderr, err)
		return "", 1
	}
	if c.binDir == "" {
		binDir, err := os.MkdirTemp(c.cacheDir, "scopesifter-run.*")
		if err != nil {
			fmt.Fprintln(stderr, err)
			return "", 1
		}
		return binDir, 0
	}
	parent := lexicalParent(c.binDir)
	if err := validateDirectoryPath(parent, "SCOPESIFTER_BIN_DIR", false); err != nil {
		fmt.Fprintln(stderr, err)
		return "", 1
	}
	if _, err := os.Lstat(c.binDir); err == nil || !errors.Is(err, os.ErrNotExist) {
		fmt.Fprintf(stderr, "SCOPESIFTER_BIN_DIR target must not already exist: %s\n", c.binDir)
		return "", 2
	}
	if err := os.Mkdir(c.binDir, 0o700); err != nil {
		fmt.Fprintf(stderr, "failed to create SCOPESIFTER_BIN_DIR: %s\n", c.binDir)
		return "", 2
	}
	return c.binDir, 0
}

func lexicalParent(path string) string {
	separator := string(filepath.Separator)
	volume := filepath.VolumeName(path)
	index := strings.LastIndex(path, separator)
	if index <= len(volume) {
		return volume + separator
	}
	return path[:index]
}

func validateDirectoryPath(path string, label string, createMissing bool) error {
	if !filepath.IsAbs(path) ||
		path == string(filepath.Separator) ||
		strings.HasSuffix(path, string(filepath.Separator)) ||
		strings.Contains(path, string(filepath.Separator)+string(filepath.Separator)) ||
		containsDotComponent(path) ||
		containsControl(path) {
		return fmt.Errorf("%s must be a clean absolute directory path: %s", label, path)
	}

	volume := filepath.VolumeName(path)
	relative := strings.TrimPrefix(path[len(volume):], string(filepath.Separator))
	current := volume + string(filepath.Separator)
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		current = filepath.Join(current, component)
		info, err := os.Lstat(current)
		if err == nil {
			if info.Mode()&os.ModeSymlink != 0 {
				return fmt.Errorf("%s must not contain a symlink component: %s", label, current)
			}
			if !info.IsDir() {
				return fmt.Errorf("%s contains a non-directory component: %s", label, current)
			}
			continue
		}
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if !createMissing {
			return fmt.Errorf("%s parent directory does not exist: %s", label, current)
		}
		if err := os.Mkdir(current, 0o700); err != nil {
			return fmt.Errorf("failed to create %s component: %s", label, current)
		}
		created, err := os.Lstat(current)
		if err != nil || created.Mode()&os.ModeSymlink != 0 || !created.IsDir() {
			return fmt.Errorf("%s component changed while it was created: %s", label, current)
		}
	}
	return nil
}

func containsDotComponent(path string) bool {
	for _, component := range strings.Split(path, string(filepath.Separator)) {
		if component == "." || component == ".." {
			return true
		}
	}
	return false
}

func containsControl(value string) bool {
	for _, character := range value {
		if unicode.IsControl(character) {
			return true
		}
	}
	return false
}

func quoteLinkerArgument(argument string) (string, error) {
	hasSingle := strings.Contains(argument, "'")
	hasDouble := strings.Contains(argument, "\"")
	hasSpace := strings.IndexFunc(argument, unicode.IsSpace) >= 0
	if hasSingle && hasDouble {
		return "", fmt.Errorf("linker argument contains both quote characters")
	}
	if hasSpace && hasSingle {
		return "\"" + argument + "\"", nil
	}
	if hasSpace || hasDouble {
		return "'" + argument + "'", nil
	}
	if hasSingle {
		return "\"" + argument + "\"", nil
	}
	return argument, nil
}

func replaceEnvironment(
	environment []string,
	remove []string,
	replacements map[string]string,
) []string {
	removed := make(map[string]struct{}, len(remove)+len(replacements))
	for _, name := range remove {
		removed[name] = struct{}{}
	}
	for name := range replacements {
		removed[name] = struct{}{}
	}
	result := make([]string, 0, len(environment)+len(replacements))
	for _, entry := range environment {
		name, _, ok := strings.Cut(entry, "=")
		if _, drop := removed[name]; ok && drop {
			continue
		}
		result = append(result, entry)
	}
	for name, value := range replacements {
		result = append(result, name+"="+value)
	}
	return result
}

func processExitStatus(err error) int {
	if err == nil {
		return 0
	}
	var processError *exec.ExitError
	if errors.As(err, &processError) {
		if processError.ExitCode() >= 0 {
			return processError.ExitCode()
		}
		return signaledProcessExitStatus(processError)
	}
	var exitError interface{ ExitCode() int }
	if errors.As(err, &exitError) && exitError.ExitCode() >= 0 {
		return exitError.ExitCode()
	}
	return 1
}

func reportProcessFailure(err error, stderr io.Writer) int {
	if err == nil {
		return 0
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return processExitStatus(err)
	}
	var coded interface{ ExitCode() int }
	if errors.As(err, &coded) && coded.ExitCode() >= 0 {
		return coded.ExitCode()
	}
	fmt.Fprintln(stderr, err)
	if errors.Is(err, exec.ErrNotFound) {
		return 127
	}
	if errors.Is(err, os.ErrPermission) {
		return 126
	}
	return 1
}
