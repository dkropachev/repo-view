package codexlauncher

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

type recordingExecutor struct {
	processes []process
	errors    map[string]error
	output    string
}

func (e *recordingExecutor) run(p process) error {
	copyOfProcess := p
	copyOfProcess.arguments = append([]string(nil), p.arguments...)
	copyOfProcess.environment = append([]string(nil), p.environment...)
	e.processes = append(e.processes, copyOfProcess)
	if p.name == "go" {
		for index, argument := range p.arguments {
			if argument == "-o" && index+1 < len(p.arguments) {
				if err := os.WriteFile(p.arguments[index+1], []byte("binary"), 0o700); err != nil {
					return err
				}
			}
		}
	}
	if p.name == "codex" && e.output != "" {
		if _, err := io.WriteString(p.stdout, e.output); err != nil {
			return err
		}
	}
	return e.errors[p.name]
}

type codedError int

func (e codedError) Error() string { return fmt.Sprintf("exit status %d", e) }
func (e codedError) ExitCode() int { return int(e) }

func TestRunPreservesBuildAndCodexContract(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	cache := filepath.Join(root, "cache", "bin")
	binParent := filepath.Join(root, "runs")
	if err := os.Mkdir(binParent, 0o700); err != nil {
		t.Fatal(err)
	}
	binDir := filepath.Join(binParent, "one run")
	environment := []string{
		"PATH=/tools/bin",
		"HOME=/user/home",
		"PWD=/target/repository",
		"GOOS=plan9",
		"GOARCH=arm64",
		"CGO_ENABLED=1",
		"GOROOT=/untrusted/go",
		"GOFLAGS=-mod=mod",
		"GOPROXY=direct",
		"GOVCS=*:all",
		"GO_UNREVIEWED_DELEGATOR=/tmp/tool",
		"CC=/tmp/compiler-wrapper",
		"SCOPESIFTER_CACHE_DIR=" + cache,
		"SCOPESIFTER_BIN_DIR=" + binDir,
		"SCOPESIFTER_LIMIT_CAP=21",
		"SCOPESIFTER_CONTEXT_CAP=25",
		"SCOPESIFTER_MAX_CODE_LINES_CAP=61",
		"SCOPESIFTER_MAX_PATCH_LINES_CAP=301",
		"SCOPESIFTER_REASONING_EFFORT=xhigh",
		"SCOPESIFTER_NAVIGATION_COMMAND_CAP=3",
		"SCOPESIFTER_NAVIGATION_BUDGET_FILE=/must/not/leak",
	}
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	executor := &recordingExecutor{output: "{\"type\":\"event\"}\n", errors: map[string]error{}}
	status := run(
		root,
		[]string{"exec", "-C", "/target repository", "--json", "prompt"},
		environment,
		Streams{Stdin: strings.NewReader("input"), Stdout: &stdout, Stderr: &stderr},
		executor,
	)
	if status != 0 {
		t.Fatalf("status = %d, stderr = %q", status, stderr.String())
	}
	if stdout.String() != executor.output {
		t.Fatalf("stdout = %q", stdout.String())
	}
	if len(executor.processes) != 2 {
		t.Fatalf("processes = %#v", executor.processes)
	}

	build := executor.processes[0]
	if build.name != "go" || build.directory != root {
		t.Fatalf("build = %#v", build)
	}
	if len(build.arguments) != 6 || build.arguments[0] != "build" ||
		build.arguments[1] != "-ldflags" || build.arguments[3] != "-o" ||
		build.arguments[4] != filepath.Join(binDir, "scopesifter") ||
		build.arguments[5] != "./cmd/scopesifter" {
		t.Fatalf("build arguments = %#v", build.arguments)
	}
	linkerFlags := build.arguments[2]
	for _, wanted := range []string{
		"-X main.enforcedLimitCap=21",
		"-X main.enforcedContextCap=25",
		"-X main.enforcedMaxCodeLinesCap=61",
		"-X main.enforcedMaxPatchLinesCap=301",
		"-X main.enforcedNavigationCommandCap=3",
		"main.enforcedNavigationTranscriptPath=",
	} {
		if !strings.Contains(linkerFlags, wanted) {
			t.Errorf("linker flags omit %q: %s", wanted, linkerFlags)
		}
	}
	buildEnv := environmentMap(build.environment)
	for name := range buildEnv {
		_, fixed := fixedGoVariables[name]
		if launcherGoEnvironmentVariableControlled(name) && name != "PWD" && !fixed {
			t.Errorf("build environment retains uncontrolled %s", name)
		}
	}
	for name, wanted := range fixedGoVariables {
		if buildEnv[name] != wanted {
			t.Errorf("build %s = %q, want %q", name, buildEnv[name], wanted)
		}
	}
	if buildEnv["HOME"] != "/user/home" || buildEnv["PATH"] != "/tools/bin" {
		t.Fatalf("unrelated build environment was not preserved: %#v", buildEnv)
	}
	if buildEnv["PWD"] != root {
		t.Fatalf("build PWD = %q, want %q", buildEnv["PWD"], root)
	}

	codex := executor.processes[1]
	if codex.name != "codex" || codex.directory != "" {
		t.Fatalf("codex = %#v", codex)
	}
	if !equalStrings(codex.arguments[len(codex.arguments)-5:], []string{
		"exec", "-C", "/target repository", "--json", "prompt",
	}) {
		t.Fatalf("Codex argv tail = %#v", codex.arguments)
	}
	joinedArguments := strings.Join(codex.arguments, "\n")
	for _, wanted := range []string{
		"developer_instructions=\"ScopeSifter CLI is available",
		"not as a required first action",
		"model_reasoning_effort=\"xhigh\"",
	} {
		if !strings.Contains(joinedArguments, wanted) {
			t.Errorf("Codex arguments omit %q", wanted)
		}
	}
	if strings.Contains(joinedArguments, "{{") {
		t.Errorf("Codex instructions retain an unexpanded token")
	}
	codexEnv := environmentMap(codex.environment)
	for _, removed := range []string{
		"SCOPESIFTER_NAVIGATION_COMMAND_CAP",
		"SCOPESIFTER_NAVIGATION_BUDGET_FILE",
	} {
		if _, present := codexEnv[removed]; present {
			t.Errorf("Codex environment retains %s", removed)
		}
	}
	for name, wanted := range map[string]string{
		"PATH":                            binDir + string(os.PathListSeparator) + "/tools/bin",
		"SCOPESIFTER_LIMIT_CAP":           "21",
		"SCOPESIFTER_CONTEXT_CAP":         "25",
		"SCOPESIFTER_MAX_CODE_LINES_CAP":  "61",
		"SCOPESIFTER_MAX_PATCH_LINES_CAP": "301",
	} {
		if codexEnv[name] != wanted {
			t.Errorf("Codex %s = %q, want %q", name, codexEnv[name], wanted)
		}
	}
	if _, err := os.Lstat(binDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("run directory was not removed: %v", err)
	}
	transcriptPath := linkerValue(linkerFlags, "main.enforcedNavigationTranscriptPath=")
	if transcriptPath == "" {
		t.Fatal("transcript linker value is absent")
	}
	if _, err := os.Lstat(transcriptPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("transcript was not removed: %v", err)
	}
}

func TestRunOmitsOptionalReasoningAndTranscript(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	executor := &recordingExecutor{errors: map[string]error{}}
	status := run(root, []string{"exec"}, []string{
		"PATH=/tools",
		"SCOPESIFTER_CACHE_DIR=" + filepath.Join(root, "cache"),
		"SCOPESIFTER_REASONING_EFFORT=inherit",
		"SCOPESIFTER_ANSWER_GUARD=off",
	}, Streams{Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard}, executor)
	if status != 0 {
		t.Fatalf("status = %d", status)
	}
	if len(executor.processes) != 2 {
		t.Fatalf("process count = %d", len(executor.processes))
	}
	arguments := strings.Join(executor.processes[1].arguments, "\n")
	if strings.Contains(arguments, "model_reasoning_effort") ||
		strings.Contains(arguments, answerGuardInstructions) {
		t.Fatalf("optional instructions leaked into arguments: %s", arguments)
	}
	if strings.Contains(executor.processes[0].arguments[2], "NavigationTranscript") {
		t.Fatal("uncapped build unexpectedly embeds transcript settings")
	}
}

func TestRunPropagatesBuildAndCodexExitStatus(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		errors map[string]error
		want   int
		calls  int
	}{
		{"build", map[string]error{"go": codedError(17)}, 17, 1},
		{"codex", map[string]error{"codex": codedError(23)}, 23, 2},
		{"start failure", map[string]error{"codex": errors.New("missing")}, 1, 2},
		{"not found", map[string]error{"codex": exec.ErrNotFound}, 127, 2},
		{"not executable", map[string]error{"codex": os.ErrPermission}, 126, 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			executor := &recordingExecutor{errors: test.errors}
			status := run(root, []string{"exec"}, []string{
				"PATH=/tools",
				"SCOPESIFTER_CACHE_DIR=" + filepath.Join(root, "cache"),
			}, Streams{Stdin: strings.NewReader(""), Stdout: io.Discard, Stderr: io.Discard}, executor)
			if status != test.want || len(executor.processes) != test.calls {
				t.Fatalf("status = %d, calls = %d; want %d, %d", status, len(executor.processes), test.want, test.calls)
			}
		})
	}
}

func TestRunPropagatesCappedCodexExitAndRemovesTranscript(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	executor := &recordingExecutor{
		errors: map[string]error{"codex": codedError(29)},
		output: "partial output\n",
	}
	var stdout bytes.Buffer
	status := run(root, []string{"exec", "--json"}, []string{
		"PATH=/tools",
		"SCOPESIFTER_CACHE_DIR=" + filepath.Join(root, "cache"),
		"SCOPESIFTER_NAVIGATION_COMMAND_CAP=2",
	}, Streams{Stdin: strings.NewReader(""), Stdout: &stdout, Stderr: io.Discard}, executor)
	if status != 29 || stdout.String() != executor.output {
		t.Fatalf("status = %d, stdout = %q", status, stdout.String())
	}
	if len(executor.processes) != 2 {
		t.Fatalf("process count = %d", len(executor.processes))
	}
	flags := executor.processes[0].arguments[2]
	transcriptPath := linkerValue(flags, "main.enforcedNavigationTranscriptPath=")
	if transcriptPath == "" {
		t.Fatal("transcript path is absent")
	}
	if _, err := os.Lstat(transcriptPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("transcript was not removed: %v", err)
	}
}

func TestValidateDirectoryPath(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	created := filepath.Join(root, "one", "two")
	if err := validateDirectoryPath(created, "TEST", true); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(created)
	if err != nil || !info.IsDir() {
		t.Fatalf("created directory = %v, %v", info, err)
	}
	if info.Mode().Perm()&0o077 != 0 {
		t.Fatalf("created directory permissions = %o, want private", info.Mode().Perm())
	}
	file := filepath.Join(root, "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(root, "link")
	if err := os.Symlink(created, symlink); err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		path string
		want string
	}{
		{"relative", "clean absolute"},
		{string(filepath.Separator), "clean absolute"},
		{root + string(filepath.Separator), "clean absolute"},
		{root + string(filepath.Separator) + string(filepath.Separator) + "x", "clean absolute"},
		{filepath.Join(root, "one") + "/../x", "clean absolute"},
		{root + "/control\nname", "clean absolute"},
		{filepath.Join(file, "child"), "non-directory component"},
		{filepath.Join(symlink, "child"), "symlink component"},
	} {
		if err := validateDirectoryPath(test.path, "TEST", true); err == nil || !strings.Contains(err.Error(), test.want) {
			t.Errorf("path %q: error = %v, want containing %q", test.path, err, test.want)
		}
	}
	missing := filepath.Join(root, "absent", "child")
	if err := validateDirectoryPath(missing, "TEST", false); err == nil ||
		!strings.Contains(err.Error(), "parent directory does not exist") {
		t.Fatalf("error = %v", err)
	}
}

func TestPrepareDirectoriesRejectsUncleanBinParent(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	var stderr bytes.Buffer
	_, status := prepareDirectories(config{
		cacheDir: filepath.Join(root, "cache"),
		binDir:   root + "//run",
	}, &stderr)
	if status != 1 || !strings.Contains(stderr.String(), "clean absolute directory path") {
		t.Fatalf("status = %d, stderr = %q", status, stderr.String())
	}
}

func TestQuoteLinkerArgument(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		input string
		want  string
	}{
		{"main.value=plain", "main.value=plain"},
		{"main.value=two words", "'main.value=two words'"},
		{"main.value=double\"quote", "'main.value=double\"quote'"},
		{"main.value=single'quote", "\"main.value=single'quote\""},
		{"main.value=single' quote", "\"main.value=single' quote\""},
	} {
		got, err := quoteLinkerArgument(test.input)
		if err != nil || got != test.want {
			t.Errorf("quoteLinkerArgument(%q) = %q, %v; want %q", test.input, got, err, test.want)
		}
	}
	if _, err := quoteLinkerArgument("main.value=both'\""); err == nil {
		t.Fatal("argument containing both quote characters was accepted")
	}
}

func TestDeveloperInstructionsUseExactBoundedText(t *testing.T) {
	t.Parallel()
	const want = `ScopeSifter CLI is available: find an exact symbol/path with scopesifter find, read PATH:LINE with scopesifter inspect, or map changes with scopesifter changed. Use it only when it replaces shell navigation, not as a required first action.`
	c := config{answerGuard: "off"}
	instructions := developerInstructions(c)
	if instructions != want {
		t.Fatalf("advisory instructions = %q, want %q", instructions, want)
	}
	if got := len([]byte(instructions)); got > 240 {
		t.Fatalf("navigation instruction bytes = %d, want at most 240", got)
	}
	assertNoDirectedNavigationInstructions(t, instructions)
}

func assertNoDirectedNavigationInstructions(t *testing.T, instructions string) {
	t.Helper()
	for _, forced := range []string{
		"Use scopesifter as the primary code-navigation tool.",
		"Pick exactly one initial command",
		"scopesifter changed --root . --base <BASE>",
		"changed is the only navigation command",
		"start with changed",
		"wrapper requirements, not suggestions",
		"invalidates the run",
	} {
		if strings.Contains(instructions, forced) {
			t.Errorf("advisory instructions retain forced language %q", forced)
		}
	}
}

func TestDefaultInstructionsFitTotalByteBudget(t *testing.T) {
	t.Parallel()
	c := config{answerGuard: defaultAnswerGuard}
	instructions := developerInstructions(c)
	if instructions != navigationInstructions+"\n"+answerGuardInstructions {
		t.Fatalf("default instructions omit expected content: %q", instructions)
	}
	assertNoDirectedNavigationInstructions(t, instructions)
	if got := len([]byte(instructions)); got > 720 {
		t.Fatalf("default advisory instruction bytes = %d, want at most 720", got)
	}
}

func TestOSExecutorRejectsExecutablesOutsideLauncherRoles(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"bash", "python3", "unreviewed-native-tool"} {
		err := (osExecutor{}).run(process{name: name})
		if err == nil || !strings.Contains(err.Error(), "executable") {
			t.Fatalf("OS executor accepted %q: %v", name, err)
		}
	}
}

func TestValidateLauncherProcessesRejectRoleConfusion(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	buildEnvironment := launcherBuildEnvironment([]string{"PATH=/usr/bin"}, root)
	validBuild := process{
		name: "go", directory: root, environment: buildEnvironment,
		arguments: []string{
			"build", "-ldflags", strings.Join([]string{
				"-X main.enforcedLimitCap=20",
				"-X main.enforcedContextCap=20",
				"-X main.enforcedMaxCodeLinesCap=60",
				"-X main.enforcedMaxPatchLinesCap=300",
			}, " "),
			"-o", filepath.Join(root, "bin", "scopesifter"), "./cmd/scopesifter",
		},
	}
	if err := validateLauncherBuildProcess(validBuild); err != nil {
		t.Fatalf("generated build process rejected: %v", err)
	}
	for _, mutate := range []func(*process){
		func(value *process) { value.arguments = []string{"run", "/tmp/tool.go"} },
		func(value *process) { value.arguments[2] = "-extld=/tmp/tool" },
		func(value *process) {
			value.arguments[2] += " -linkmode=external -extld=/bin/bash"
		},
		func(value *process) { value.environment = append(value.environment, "GOFLAGS=-toolexec=/tmp/tool") },
		func(value *process) { value.environment = append(value.environment, "GO_UNREVIEWED=/tmp/tool") },
		func(value *process) { value.environment = append(value.environment, "CC=/tmp/compiler-wrapper") },
	} {
		candidate := validBuild
		candidate.arguments = append([]string(nil), validBuild.arguments...)
		candidate.environment = append([]string(nil), validBuild.environment...)
		mutate(&candidate)
		if err := validateLauncherBuildProcess(candidate); err == nil {
			t.Fatalf("role-confused Go process accepted: %#v", candidate)
		}
	}
	if err := validateLauncherCodexProcess(process{
		name: "codex", arguments: []string{"exec", "prompt"}, environment: []string{"PATH=/usr/bin"},
	}); err == nil {
		t.Fatal("Codex process without generated instruction prefix accepted")
	}
}

func equalStrings(left []string, right []string) bool {
	if len(left) != len(right) {
		return false
	}
	for index := range left {
		if left[index] != right[index] {
			return false
		}
	}
	return true
}

func linkerValue(flags string, prefix string) string {
	index := strings.Index(flags, prefix)
	if index < 0 {
		return ""
	}
	value := flags[index+len(prefix):]
	value = strings.TrimPrefix(value, "'")
	if end := strings.IndexAny(value, "' "); end >= 0 {
		value = value[:end]
	}
	return value
}
