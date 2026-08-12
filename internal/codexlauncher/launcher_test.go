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
		"SCOPESIFTER_CACHE_DIR=" + cache,
		"SCOPESIFTER_BIN_DIR=" + binDir,
		"SCOPESIFTER_CHANGED_RETURN=scope",
		"SCOPESIFTER_CHANGED_CONTEXT=0004",
		"SCOPESIFTER_CHANGED_LIMIT=21",
		"SCOPESIFTER_CHANGED_MAX_CODE_LINES=61",
		"SCOPESIFTER_CHANGED_MAX_PATCH_LINES=301",
		"SCOPESIFTER_REASONING_EFFORT=xhigh",
		"SCOPESIFTER_NAVIGATION_POLICY=adaptive",
		"SCOPESIFTER_NAVIGATION_CONTEXT_CAP=25",
		"SCOPESIFTER_NAVIGATION_COMMAND_CAP=3",
		"SCOPESIFTER_NAVIGATION_BUDGET_FILE=/must/not/leak",
		"SCOPESIFTER_REQUIRE_NAVIGATION_SEMANTICS=1",
		"SCOPESIFTER_REQUIRED_ROOT=/target repository",
		"SCOPESIFTER_REQUIRED_BASE_COMMIT=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"SCOPESIFTER_REQUIRED_CHANGED_RETURN=scope",
		"SCOPESIFTER_REQUIRED_CHANGED_CONTEXT=4",
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
		"-X 'main.enforcedNavigationRoot=/target repository'",
		"-X main.enforcedNavigationBaseCommit=aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"-X main.enforcedChangedReturn=scope",
		"-X main.enforcedChangedContext=4",
		"-X main.enforcedNavigationSemantics=1",
		"-X main.enforcedNavigationCommandCap=3",
		"main.enforcedNavigationTranscriptPath=",
	} {
		if !strings.Contains(linkerFlags, wanted) {
			t.Errorf("linker flags omit %q: %s", wanted, linkerFlags)
		}
	}
	buildEnv := environmentMap(build.environment)
	for _, removed := range sanitizedGoVariables {
		if _, present := buildEnv[removed]; present {
			t.Errorf("build environment retains %s", removed)
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
		"developer_instructions=\"Use scopesifter",
		"start with changed",
		"--limit 21 or less",
		"--context 25 or less",
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
		strings.Contains(arguments, standardAnswerGuard) {
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

func TestDeveloperInstructionsCoverAllPolicies(t *testing.T) {
	t.Parallel()
	for _, policy := range []string{"terminal", "adaptive", "batched"} {
		c := config{
			changedReturn:        "context",
			changedContext:       "4",
			changedLimit:         "20",
			changedMaxCodeLines:  "60",
			changedMaxPatchLines: "300",
			navigationContextCap: "20",
			navigationCommandCap: "40",
			navigationPolicy:     policy,
			answerGuard:          "on",
		}
		instructions := developerInstructions(c)
		wantedInstructions := []string{
			"Use scopesifter as the primary code-navigation tool.",
			"scopesifter changed --root . --base <BASE>",
			"scopesifter find <SYMBOL>...",
			"scopesifter inspect <PATH:LINE>...",
			"scopesifter outline <PATH>...",
		}
		if policy == "batched" {
			wantedInstructions = append(
				wantedInstructions,
				strings.ReplaceAll(batchedAnswerGuard, "<BACKTICK>", "`"),
			)
		} else {
			wantedInstructions = append(wantedInstructions, standardAnswerGuard)
		}
		for _, wanted := range wantedInstructions {
			if !strings.Contains(instructions, wanted) {
				t.Errorf("%s instructions omit %q", policy, wanted)
			}
		}
		if strings.Contains(instructions, "{{") || strings.Contains(instructions, "<BACKTICK>") {
			t.Errorf("%s instructions retain a template token", policy)
		}
		if policy == "batched" {
			for _, wanted := range []string{
				"Use at most 40 scopesifter invocations",
				"time.Time.UTC strips the monotonic reading",
				"11. Preserve unresolved out-of-repository risk",
			} {
				if !strings.Contains(instructions, wanted) {
					t.Errorf("batched instructions omit %q", wanted)
				}
			}
		}
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
