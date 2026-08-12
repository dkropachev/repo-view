package codexlauncher

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strings"
	"unicode"

	"github.com/yapless/scopesifter/internal/processpolicy"
)

var fixedGoVariables = map[string]string{
	"CGO_ENABLED": "0",
	"GO111MODULE": "on",
	"GOAUTH":      "off",
	"GOENV":       "off",
	"GOFLAGS":     "-mod=readonly -trimpath -buildvcs=false",
	"GONOPROXY":   "none",
	"GONOSUMDB":   "",
	"GOPRIVATE":   "",
	"GOPROXY":     "https://proxy.golang.org",
	"GOSUMDB":     "sum.golang.org",
	"GOTOOLCHAIN": "local",
	"GOVCS":       "*:off",
	"GOWORK":      "off",
}

// Streams are the inherited standard streams for the build and Codex process.
type Streams struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

type process struct {
	stdin       io.Reader
	stdout      io.Writer
	stderr      io.Writer
	name        string
	directory   string
	arguments   []string
	environment []string
}

type executor interface {
	run(process) error
}

type osExecutor struct {
	signals <-chan os.Signal
}

func (e osExecutor) run(p process) (resultErr error) {
	if err := processpolicy.ValidateExecutable(p.name); err != nil {
		return fmt.Errorf("reject launcher executable: %w", err)
	}
	switch p.name {
	case "go":
		if err := validateLauncherBuildProcess(p); err != nil {
			return err
		}
	case "codex":
		if err := validateLauncherCodexProcess(p); err != nil {
			return err
		}
	default:
		return fmt.Errorf("unsupported launcher executable %q", p.name)
	}
	command, nativeFile, err := processpolicy.NativeCommand(p.name, p.arguments...)
	if err != nil {
		return fmt.Errorf("pin launcher executable: %w", err)
	}
	defer func() {
		resultErr = errors.Join(resultErr, nativeFile.Close())
	}()
	command.Dir = p.directory
	command.Env = p.environment
	command.Stdin = p.stdin
	command.Stdout = p.stdout
	command.Stderr = p.stderr
	if err := command.Start(); err != nil {
		return err
	}
	waited := make(chan error, 1)
	go func() {
		waited <- command.Wait()
	}()
	var forwarded os.Signal
	for {
		select {
		case err := <-waited:
			if err == nil && forwarded != nil {
				return forwardedSignalError{signal: forwarded}
			}
			return err
		case received := <-e.signals:
			if received == nil {
				continue
			}
			if forwarded == nil {
				forwarded = received
			}
			if err := command.Process.Signal(received); err != nil &&
				!errors.Is(err, os.ErrProcessDone) {
				fmt.Fprintf(p.stderr, "failed to forward %v to %s: %v\n", received, p.name, err)
			}
		}
	}
}

func validateLauncherBuildProcess(p process) error {
	if len(p.arguments) != 6 || p.arguments[0] != "build" ||
		p.arguments[1] != "-ldflags" || p.arguments[3] != "-o" ||
		p.arguments[5] != "./cmd/scopesifter" {
		return errors.New("launcher Go build arguments do not match the fixed role")
	}
	if err := processpolicy.Validate(p.name, p.arguments...); err != nil {
		return fmt.Errorf("launcher Go build violates process policy: %w", err)
	}
	if p.directory == "" || !filepath.IsAbs(p.directory) || filepath.Clean(p.directory) != p.directory {
		return errors.New("launcher Go build directory is not canonical and absolute")
	}
	output := p.arguments[4]
	if !filepath.IsAbs(output) || filepath.Clean(output) != output || filepath.Base(output) != "scopesifter" {
		return errors.New("launcher Go build output is not the fixed binary role")
	}
	if !safeLauncherLinkerFlags(p.arguments[2]) {
		return errors.New("launcher Go linker flags are outside the generated -X grammar")
	}
	required := buildEnvironmentValues(p.directory)
	seen := make(map[string]bool, len(required))
	for _, entry := range p.environment {
		name, value, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		expected, constrained := required[name]
		if launcherGoEnvironmentVariableControlled(name) && !constrained {
			return fmt.Errorf("launcher Go environment retains prohibited %s", name)
		}
		if constrained {
			if seen[name] || value != expected {
				return fmt.Errorf("launcher Go environment does not bind %s exactly", name)
			}
			seen[name] = true
		}
	}
	for name := range required {
		if !seen[name] {
			return fmt.Errorf("launcher Go environment omits required binding %s", name)
		}
	}
	return nil
}

func safeLauncherLinkerFlags(flags string) bool {
	if flags == "" || strings.ContainsAny(flags, "\r\n\\`$;@") {
		return false
	}
	assignments, valid := parseLauncherLinkerAssignments(flags)
	if !valid {
		return false
	}
	for _, required := range []string{
		"main.enforcedLimitCap",
		"main.enforcedContextCap",
		"main.enforcedMaxCodeLinesCap",
		"main.enforcedMaxPatchLinesCap",
	} {
		if _, found := assignments[required]; !found {
			return false
		}
	}
	semanticNames := []string{
		"main.enforcedNavigationRoot",
		"main.enforcedNavigationBaseCommit",
		"main.enforcedChangedReturn",
		"main.enforcedChangedContext",
		"main.enforcedNavigationSemantics",
	}
	semanticCount := 0
	for _, name := range semanticNames {
		if _, found := assignments[name]; found {
			semanticCount++
		}
	}
	if semanticCount != 0 && semanticCount != len(semanticNames) {
		return false
	}
	_, commandCap := assignments["main.enforcedNavigationCommandCap"]
	_, transcript := assignments["main.enforcedNavigationTranscriptPath"]
	return commandCap == transcript
}

func parseLauncherLinkerAssignments(flags string) (map[string]string, bool) {
	assignments := make(map[string]string)
	remaining := flags
	for remaining != "" {
		if !strings.HasPrefix(remaining, "-X ") {
			return nil, false
		}
		remaining = strings.TrimPrefix(remaining, "-X ")
		assignment, rest, valid := linkerAssignment(remaining)
		if !valid {
			return nil, false
		}
		name, value, found := strings.Cut(assignment, "=")
		if !found || value == "" || !validLauncherLinkerValue(name, value) {
			return nil, false
		}
		if _, duplicate := assignments[name]; duplicate {
			return nil, false
		}
		assignments[name] = value
		remaining = rest
	}
	return assignments, true
}

func linkerAssignment(value string) (string, string, bool) {
	if value == "" {
		return "", "", false
	}
	if value[0] == '\'' || value[0] == '"' {
		quote := value[0]
		end := strings.IndexByte(value[1:], quote)
		if end < 0 {
			return "", "", false
		}
		end++
		assignment := value[1:end]
		rest := value[end+1:]
		if rest != "" {
			if !strings.HasPrefix(rest, " -X ") {
				return "", "", false
			}
			rest = rest[1:]
		}
		return assignment, rest, assignment != ""
	}
	separator := strings.Index(value, " -X ")
	if separator < 0 {
		if strings.IndexFunc(value, unicode.IsSpace) >= 0 || strings.ContainsAny(value, "'\"") {
			return "", "", false
		}
		return value, "", true
	}
	assignment := value[:separator]
	if assignment == "" || strings.IndexFunc(assignment, unicode.IsSpace) >= 0 ||
		strings.ContainsAny(assignment, "'\"") {
		return "", "", false
	}
	return assignment, value[separator+1:], true
}

func validLauncherLinkerValue(name, value string) bool {
	if containsControl(value) {
		return false
	}
	switch name {
	case "main.enforcedLimitCap", "main.enforcedContextCap",
		"main.enforcedMaxCodeLinesCap", "main.enforcedMaxPatchLinesCap",
		"main.enforcedChangedContext", "main.enforcedNavigationCommandCap":
		return decimalPattern.MatchString(value)
	case "main.enforcedNavigationBaseCommit":
		return objectIDPattern.MatchString(value)
	case "main.enforcedChangedReturn":
		return oneOf(value, "locations", "line", "context", "scope")
	case "main.enforcedNavigationSemantics":
		return value == "1"
	case "main.enforcedNavigationRoot", "main.enforcedNavigationTranscriptPath":
		return filepath.IsAbs(value) && filepath.Clean(value) == value
	default:
		return false
	}
}

func validateLauncherCodexProcess(p process) error {
	if p.directory != "" || len(p.arguments) < 2 || p.arguments[0] != "-c" ||
		!strings.HasPrefix(p.arguments[1], "developer_instructions=\"") {
		return errors.New("launcher Codex process does not begin with generated instructions")
	}
	if len(p.environment) == 0 || environmentMap(p.environment)["PATH"] == "" {
		return errors.New("launcher Codex process lacks its generated environment")
	}
	return nil
}

type forwardedSignalError struct {
	signal os.Signal
}

func (e forwardedSignalError) Error() string {
	return fmt.Sprintf("process interrupted by %v", e.signal)
}

func (e forwardedSignalError) ExitCode() int {
	return signalExitStatus(e.signal)
}

// Run builds the mechanically capped scopesifter binary and runs Codex with it.
// Configuration failures use exit status 2; child-process failures preserve the
// child exit status.
func Run(root string, arguments []string, environment []string, streams Streams) int {
	signals := make(chan os.Signal, 4)
	signal.Notify(signals, launcherSignals()...)
	defer signal.Stop(signals)
	return run(root, arguments, environment, streams, osExecutor{signals: signals})
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

	buildEnvironment := launcherBuildEnvironment(environment, root)
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

func launcherBuildEnvironment(environment []string, root string) []string {
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, found := strings.Cut(entry, "=")
		if !found || launcherGoEnvironmentVariableControlled(name) {
			continue
		}
		filtered = append(filtered, entry)
	}
	return replaceEnvironment(filtered, nil, buildEnvironmentValues(root))
}

func launcherGoEnvironmentVariableControlled(name string) bool {
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
