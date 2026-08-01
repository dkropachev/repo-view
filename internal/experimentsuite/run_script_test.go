package experimentsuite

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

const runScriptCompletedJSONL = `{"type":"thread.started","thread_id":"test"}` + "\n" +
	`{"type":"turn.started"}` + "\n" +
	`{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":1,"reasoning_output_tokens":0}}` + "\n"

func TestRunScriptRejectsMissingValues(t *testing.T) {
	bashPath, runScript := requireRunScriptTestTools(t, "bash")
	for _, option := range []string{
		"--task",
		"--variant",
		"--profile",
		"--baseline-from",
		"--order",
		"--run-id",
		"--source",
		"--commit",
		"--prompt-commit",
		"--model",
		"--model-mode",
		"--codex-version",
		"--go-version",
		"--base",
		"--worktree",
		"--evidence-root",
	} {
		t.Run(strings.TrimPrefix(option, "--"), func(t *testing.T) {
			output, err := exec.Command(bashPath, runScript, option).CombinedOutput()
			assertRunScriptExit(t, err, 2, output)
			if !strings.Contains(string(output), "missing value for "+option) {
				t.Fatalf("missing-value diagnostic = %q", output)
			}
		})
	}
	for _, test := range []struct {
		name  string
		value string
	}{
		{name: "empty", value: ""},
		{name: "next option", value: "--dry-run"},
		{name: "option-like", value: "-path"},
	} {
		t.Run(test.name, func(t *testing.T) {
			output, err := exec.Command(
				bashPath,
				runScript,
				"--worktree",
				test.value,
			).CombinedOutput()
			assertRunScriptExit(t, err, 2, output)
			if !strings.Contains(string(output), "missing value for --worktree") {
				t.Fatalf("missing-value diagnostic = %q", output)
			}
		})
	}
}

func TestRunScriptDefaultDoesNotConfigureModel(t *testing.T) {
	bashPath, runScript := requireRunScriptTestTools(
		t,
		"bash", "git", "go", "jq", "tar", "sha256sum", "flock", "realpath",
		"awk", "date", "mktemp", "uname",
	)
	sourceRepo, head := initializeRunScriptGitTarget(t)
	tempRoot := t.TempDir()
	evidenceRoot := filepath.Join(tempRoot, "evidence")
	runDir := filepath.Join(evidenceRoot, "router-model")
	command := exec.Command(
		bashPath,
		runScript,
		"--task", "explain",
		"--variant", "all",
		"--run-id", "router-model",
		"--source", sourceRepo,
		"--commit", head,
		"--base", head,
		"--worktree", filepath.Join(tempRoot, "target"),
		"--evidence-root", evidenceRoot,
	)
	command.Env = runScriptTestEnvironment(t, false)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("router-selected run failed: %v\n%s", err, output)
	}
	observation, err := os.ReadFile(filepath.Join(runDir, "codex-observation.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(observation, []byte("arg=-m\n")) ||
		bytes.Contains(observation, []byte("arg=test-model\n")) ||
		bytes.Contains(observation, []byte("model_reasoning_effort")) {
		t.Fatalf("router-selected invocation configured a model:\n%s", observation)
	}
	manifestContent, err := os.ReadFile(filepath.Join(runDir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(manifestContent, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest["model"] != "router-selected" ||
		manifest["model_mode"] != "router" ||
		manifest["model_configuration"] != "none" {
		t.Fatalf("router-selected manifest = %#v", manifest)
	}
}

func TestRunScriptRejectsUnpairedOptimizedRun(t *testing.T) {
	bashPath, runScript := requireRunScriptTestTools(t, "bash", "awk")
	output, err := exec.Command(
		bashPath,
		runScript,
		"--variant", "optimized",
		"--dry-run",
	).CombinedOutput()
	assertRunScriptExit(t, err, 2, output)
	if !strings.Contains(
		string(output),
		"--variant optimized requires --baseline-from",
	) {
		t.Fatalf("unpaired optimized diagnostic = %q", output)
	}
}

func TestRunScriptRejectsUnsafeRunIDsAndInvalidProfiles(t *testing.T) {
	bashPath, runScript := requireRunScriptTestTools(t, "bash", "awk")
	for _, runID := range []string{
		"../escape",
		"/absolute",
		"nested/run",
		".",
		strings.Repeat("a", 129),
	} {
		t.Run(strings.ReplaceAll(runID, "/", "_"), func(t *testing.T) {
			output, err := exec.Command(
				bashPath,
				runScript,
				"--run-id",
				runID,
				"--dry-run",
			).CombinedOutput()
			assertRunScriptExit(t, err, 2, output)
			if !strings.Contains(string(output), "invalid --run-id:") {
				t.Fatalf("unsafe-run-id diagnostic = %q", output)
			}
		})
	}

	output, err := exec.Command(
		bashPath,
		runScript,
		"--profile",
		"guarded-high,guarded-high",
		"--dry-run",
	).CombinedOutput()
	assertRunScriptExit(t, err, 2, output)
	if !strings.Contains(string(output), "duplicate profile: guarded-high") {
		t.Fatalf("duplicate-profile diagnostic = %q", output)
	}
	output, err = exec.Command(
		bashPath,
		runScript,
		"--profile",
		"guarded-high,",
		"--dry-run",
	).CombinedOutput()
	assertRunScriptExit(t, err, 2, output)
	if !strings.Contains(string(output), "invalid --profile: empty profile name") {
		t.Fatalf("empty-profile diagnostic = %q", output)
	}

	t.Run("locale independent ASCII", func(t *testing.T) {
		localeOutput, err := exec.Command("locale", "-a").Output()
		if err != nil {
			t.Skipf("cannot list installed locales: %v", err)
		}
		localeName := ""
		for _, installed := range strings.Fields(string(localeOutput)) {
			if strings.EqualFold(installed, "en_US.utf8") ||
				strings.EqualFold(installed, "en_US.UTF-8") {
				localeName = installed
				break
			}
		}
		if localeName == "" {
			t.Skip("locale-sensitive run-ID regression requires an en_US UTF-8 locale")
		}
		localeEnvironment := make([]string, 0, len(os.Environ())+1)
		for _, variable := range os.Environ() {
			if !strings.HasPrefix(variable, "LC_ALL=") {
				localeEnvironment = append(localeEnvironment, variable)
			}
		}
		localeEnvironment = append(localeEnvironment, "LC_ALL="+localeName)
		command := exec.Command(
			bashPath,
			runScript,
			"--run-id",
			"é",
			"--dry-run",
		)
		command.Env = localeEnvironment
		output, err := command.CombinedOutput()
		assertRunScriptExit(t, err, 2, output)
		if !strings.Contains(string(output), "invalid --run-id:") {
			t.Fatalf("locale-sensitive run-ID diagnostic = %q", output)
		}

		command = exec.Command(
			bashPath,
			runScript,
			"--profile",
			"é",
			"--dry-run",
		)
		command.Env = localeEnvironment
		output, err = command.CombinedOutput()
		assertRunScriptExit(t, err, 2, output)
		if !strings.Contains(string(output), "invalid profile name:") {
			t.Fatalf("locale-sensitive profile diagnostic = %q", output)
		}
	})
}

func TestRunScriptRequiresFullLowercaseTargetCommit(t *testing.T) {
	bashPath, runScript := requireRunScriptTestTools(t, "bash", "awk")
	for _, commit := range []string{
		"17a4e2825",
		strings.Repeat("A", 40),
		strings.Repeat("a", 39),
		strings.Repeat("a", 41),
		strings.Repeat("g", 40),
	} {
		output, err := exec.Command(
			bashPath,
			runScript,
			"--commit", commit,
			"--dry-run",
		).CombinedOutput()
		assertRunScriptExit(t, err, 2, output)
		if !strings.Contains(string(output), "invalid --commit:") {
			t.Fatalf("target-commit diagnostic = %q", output)
		}
	}
}

func TestRunScriptRequiresPromptCommitTargetPrefix(t *testing.T) {
	bashPath, runScript := requireRunScriptTestTools(t, "bash", "awk")
	target := strings.Repeat("a", 40)
	for _, promptCommit := range []string{
		"A",
		"a",
		"b",
		"not-hex",
		strings.Repeat("a", 41),
	} {
		output, err := exec.Command(
			bashPath,
			runScript,
			"--commit", target,
			"--prompt-commit", promptCommit,
			"--dry-run",
		).CombinedOutput()
		assertRunScriptExit(t, err, 2, output)
		if !strings.Contains(string(output), "invalid --prompt-commit:") {
			t.Fatalf("prompt-commit diagnostic = %q", output)
		}
	}

	command := exec.Command(
		bashPath,
		runScript,
		"--task", "explain",
		"--commit", target,
		"--dry-run",
	)
	command.Env = runScriptTestEnvironment(t, false)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("derived prompt commit failed: %v\n%s", err, output)
	}
	if !strings.Contains(string(output), "prompt_commit="+target[:9]+"\n") {
		t.Fatalf("derived prompt commit missing:\n%s", output)
	}
}

func TestRunScriptDocumentsAndPreflightsPortableLocking(t *testing.T) {
	_, runScript := requireRunScriptTestTools(t, "bash")
	content, err := os.ReadFile(runScript)
	if err != nil {
		t.Fatal(err)
	}
	script := string(content)
	for _, required := range []string{
		"Requires Bash 4+ on Linux, including realpath with -m support.",
		"for required in awk cmp find git go codex gzip jq mktemp sort stat tar sha256sum realpath tee",
		`mkdir -m 700 -- "${worktree_lock}"`,
		`mkdir -m 700 -- "${run_claim_lock}"`,
		`stat -Lc '%d:%i' -- "${directory_path}"`,
		`release_owned_lock_directory`,
	} {
		if !strings.Contains(script, required) {
			t.Errorf("run.sh missing locking contract %q", required)
		}
	}
	for _, forbidden := range []string{
		`exec 9> "${worktree_lock}"`,
		`exec 8> "${run_claim_lock}"`,
		"flock -n",
	} {
		if strings.Contains(script, forbidden) {
			t.Errorf("run.sh contains clobber-prone locking %q", forbidden)
		}
	}
}

func TestRunScriptRejectsUnexpectedCodexVersion(t *testing.T) {
	bashPath, runScript := requireRunScriptTestTools(
		t,
		"bash", "git", "jq", "tar", "sha256sum", "flock", "realpath", "awk",
	)
	sourceRepo, head := initializeRunScriptGitTarget(t)
	tempRoot := t.TempDir()
	evidenceRoot := filepath.Join(tempRoot, "evidence")
	command := exec.Command(
		bashPath,
		runScript,
		"--task", "explain",
		"--variant", "baseline",
		"--run-id", "codex-version-mismatch",
		"--source", sourceRepo,
		"--commit", head,
		"--base", head,
		"--worktree", filepath.Join(tempRoot, "target"),
		"--evidence-root", evidenceRoot,
	)
	command.Env = append(
		runScriptTestEnvironment(t, true),
		"LSP_CODEX_VERSION=codex-test 2",
	)
	output, err := command.CombinedOutput()
	assertRunScriptExit(t, err, 1, output)
	if !strings.Contains(
		string(output),
		"Codex version mismatch: codex-test 1 != codex-test 2",
	) {
		t.Fatalf("Codex-version diagnostic = %q", output)
	}
	assertRunNotPublished(t, evidenceRoot, "codex-version-mismatch")
	assertNoRunScriptPartialDirectories(
		t,
		evidenceRoot,
		"codex-version-mismatch",
	)
}

func TestRunScriptClaimsRunDirectoryWithoutOverwriting(t *testing.T) {
	bashPath, runScript := requireRunScriptTestTools(
		t,
		"bash", "git", "jq", "tar", "sha256sum", "flock", "realpath", "awk",
	)
	tempRoot := t.TempDir()
	evidenceRoot := filepath.Join(tempRoot, "evidence")
	runDir := filepath.Join(evidenceRoot, "existing")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(runDir, "sentinel")
	if err := os.WriteFile(sentinel, []byte("preserve\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	command := exec.Command(
		bashPath,
		runScript,
		"--run-id", "existing",
		"--worktree", filepath.Join(tempRoot, "target"),
		"--evidence-root", evidenceRoot,
	)
	command.Env = runScriptTestEnvironment(t, true)
	output, err := command.CombinedOutput()
	assertRunScriptExit(t, err, 1, output)
	if !strings.Contains(string(output), "run already exists:") {
		t.Fatalf("run claim diagnostic = %q", output)
	}
	content, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "preserve\n" {
		t.Fatalf("existing run artifact changed to %q", content)
	}
}

func TestRunScriptLocksCanonicalWorktree(t *testing.T) {
	bashPath, runScript := requireRunScriptTestTools(
		t,
		"bash", "git", "jq", "tar", "sha256sum", "realpath", "awk",
	)
	tempRoot := t.TempDir()
	canonicalParent := filepath.Join(tempRoot, "canonical")
	if err := os.Mkdir(canonicalParent, 0o755); err != nil {
		t.Fatal(err)
	}
	aliasParent := filepath.Join(tempRoot, "alias")
	if err := os.Symlink(canonicalParent, aliasParent); err != nil {
		t.Skipf("cannot create worktree path alias: %v", err)
	}
	canonicalWorktree := filepath.Join(canonicalParent, "target")
	lockPath := canonicalWorktree + ".lock"
	if err := os.Mkdir(lockPath, 0o700); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(lockPath)

	evidenceRoot := filepath.Join(tempRoot, "evidence")
	command := exec.Command(
		bashPath,
		runScript,
		"--run-id", "locked",
		"--worktree", filepath.Join(aliasParent, "target"),
		"--evidence-root", evidenceRoot,
	)
	command.Env = runScriptTestEnvironment(t, true)
	output, err := command.CombinedOutput()
	assertRunScriptExit(t, err, 1, output)
	if !strings.Contains(string(output), "experiment worktree is already in use: "+canonicalWorktree) {
		t.Fatalf("worktree-lock diagnostic = %q", output)
	}
	if _, err := os.Stat(filepath.Join(evidenceRoot, "locked")); !os.IsNotExist(err) {
		t.Fatalf("contended run directory was claimed: %v", err)
	}
}

func TestRunScriptDoesNotClobberLockSymlinks(t *testing.T) {
	bashPath, runScript := requireRunScriptTestTools(
		t,
		"bash", "git", "jq", "tar", "sha256sum", "realpath", "awk",
	)
	sourceRepo, head := initializeRunScriptGitTarget(t)

	for _, test := range []struct {
		name       string
		runID      string
		lockPath   func(worktree, evidenceRoot string) string
		diagnostic string
	}{
		{
			name:  "worktree",
			runID: "worktree-link",
			lockPath: func(worktree, _ string) string {
				return worktree + ".lock"
			},
			diagnostic: "experiment worktree is already in use:",
		},
		{
			name:  "run claim",
			runID: "claim-link",
			lockPath: func(_, evidenceRoot string) string {
				return filepath.Join(evidenceRoot, ".claim-link.lock")
			},
			diagnostic: "run is already in progress:",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			tempRoot := t.TempDir()
			worktree := filepath.Join(tempRoot, "target")
			evidenceRoot := filepath.Join(tempRoot, "evidence")
			if err := os.MkdirAll(evidenceRoot, 0o755); err != nil {
				t.Fatal(err)
			}
			sentinel := filepath.Join(tempRoot, "sentinel")
			writeRunScriptFile(t, sentinel, "preserve\n")
			lockPath := test.lockPath(worktree, evidenceRoot)
			if err := os.MkdirAll(filepath.Dir(lockPath), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.Symlink(sentinel, lockPath); err != nil {
				t.Skipf("cannot create lock symlink: %v", err)
			}

			command := exec.Command(
				bashPath,
				runScript,
				"--task", "explain",
				"--variant", "baseline",
				"--run-id", test.runID,
				"--source", sourceRepo,
				"--commit", head,
				"--base", head,
				"--worktree", worktree,
				"--evidence-root", evidenceRoot,
			)
			command.Env = runScriptTestEnvironment(t, true)
			output, err := command.CombinedOutput()
			assertRunScriptExit(t, err, 1, output)
			if !strings.Contains(string(output), test.diagnostic) {
				t.Fatalf("lock-symlink diagnostic = %q", output)
			}
			content, err := os.ReadFile(sentinel)
			if err != nil {
				t.Fatal(err)
			}
			if string(content) != "preserve\n" {
				t.Fatalf("lock symlink target changed to %q", content)
			}
			info, err := os.Lstat(lockPath)
			if err != nil {
				t.Fatal(err)
			}
			if info.Mode()&os.ModeSymlink == 0 {
				t.Fatalf("lock path was replaced: %v", info.Mode())
			}
			if test.name == "run claim" {
				if _, err := os.Lstat(worktree + ".lock"); !os.IsNotExist(err) {
					t.Fatalf("owned worktree lock was not cleaned: %v", err)
				}
			}
			assertRunNotPublished(t, evidenceRoot, test.runID)
		})
	}
}

func TestRunScriptDoesNotRemoveReplacedLockDirectory(t *testing.T) {
	bashPath, runScript := requireRunScriptTestTools(
		t,
		"bash", "git", "jq", "tar", "sha256sum", "realpath", "stat", "awk",
	)
	sourceRepo, head := initializeRunScriptGitTarget(t)

	for _, test := range []struct {
		name     string
		lockPath func(worktree, evidenceRoot, runID string) string
	}{
		{
			name: "worktree",
			lockPath: func(worktree, _ string, _ string) string {
				return worktree + ".lock"
			},
		},
		{
			name: "run claim",
			lockPath: func(_, evidenceRoot, runID string) string {
				return filepath.Join(evidenceRoot, "."+runID+".lock")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			tempRoot := t.TempDir()
			worktree := filepath.Join(tempRoot, "target")
			evidenceRoot := filepath.Join(tempRoot, "evidence")
			runID := "replace-" + strings.ReplaceAll(test.name, " ", "-")
			ready := filepath.Join(tempRoot, "codex-version-ready")
			release := filepath.Join(tempRoot, "codex-version-release")
			command := exec.Command(
				bashPath,
				runScript,
				"--task", "explain",
				"--variant", "baseline",
				"--run-id", runID,
				"--source", sourceRepo,
				"--commit", head,
				"--base", head,
				"--worktree", worktree,
				"--evidence-root", evidenceRoot,
			)
			command.Env = append(
				runScriptTestEnvironment(t, true),
				"LSP_CODEX_VERSION=codex-test 2",
				"RUN_SCRIPT_CODEX_VERSION_READY="+ready,
				"RUN_SCRIPT_CODEX_VERSION_RELEASE="+release,
			)
			var commandOutput bytes.Buffer
			command.Stdout = &commandOutput
			command.Stderr = &commandOutput
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			done := make(chan error, 1)
			go func() {
				done <- command.Wait()
			}()
			completed := false
			defer func() {
				if completed {
					return
				}
				_ = os.WriteFile(release, []byte("release\n"), 0o644)
				_ = command.Process.Kill()
				select {
				case <-done:
				case <-time.After(5 * time.Second):
				}
			}()

			waitForRunScriptPath(
				t,
				ready,
				done,
				&completed,
				&commandOutput,
			)
			lockPath := test.lockPath(worktree, evidenceRoot, runID)
			displacedPath := lockPath + ".displaced"
			if err := os.Rename(lockPath, displacedPath); err != nil {
				t.Fatal(err)
			}
			if err := os.Mkdir(lockPath, 0o700); err != nil {
				t.Fatal(err)
			}
			replacementInfo, err := os.Stat(lockPath)
			if err != nil {
				t.Fatal(err)
			}
			writeRunScriptFile(t, release, "release\n")

			var runError error
			select {
			case runError = <-done:
				completed = true
			case <-time.After(10 * time.Second):
				t.Fatal("timed out waiting for lock replacement run")
			}
			assertRunScriptExit(
				t,
				runError,
				1,
				commandOutput.Bytes(),
			)
			if !strings.Contains(
				commandOutput.String(),
				"Codex version mismatch:",
			) {
				t.Fatalf(
					"replacement run diagnostic = %q",
					commandOutput.String(),
				)
			}
			remainingInfo, err := os.Stat(lockPath)
			if err != nil {
				t.Fatalf("replacement lock directory was removed: %v", err)
			}
			if !os.SameFile(replacementInfo, remainingInfo) {
				t.Fatal("replacement lock directory identity changed")
			}
			if _, err := os.Stat(displacedPath); err != nil {
				t.Fatalf("displaced owned lock was altered: %v", err)
			}
			assertRunNotPublished(t, evidenceRoot, runID)
			assertNoRunScriptPartialDirectories(t, evidenceRoot, runID)
		})
	}
}

func TestRunScriptDoesNotRemoveReplacedPrivateStage(t *testing.T) {
	bashPath, runScript := requireRunScriptTestTools(
		t,
		"bash", "git", "jq", "tar", "sha256sum", "realpath", "stat", "awk",
	)
	sourceRepo, head := initializeRunScriptGitTarget(t)
	tempRoot := t.TempDir()
	worktree := filepath.Join(tempRoot, "target")
	evidenceRoot := filepath.Join(tempRoot, "evidence")
	runID := "stage-replacement"
	ready := filepath.Join(tempRoot, "codex-version-ready")
	release := filepath.Join(tempRoot, "codex-version-release")
	command := exec.Command(
		bashPath,
		runScript,
		"--task", "explain",
		"--variant", "baseline",
		"--run-id", runID,
		"--source", sourceRepo,
		"--commit", head,
		"--base", head,
		"--worktree", worktree,
		"--evidence-root", evidenceRoot,
	)
	command.Env = append(
		runScriptTestEnvironment(t, true),
		"LSP_CODEX_VERSION=codex-test 2",
		"RUN_SCRIPT_CODEX_VERSION_READY="+ready,
		"RUN_SCRIPT_CODEX_VERSION_RELEASE="+release,
	)
	var commandOutput bytes.Buffer
	command.Stdout = &commandOutput
	command.Stderr = &commandOutput
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- command.Wait()
	}()
	completed := false
	defer func() {
		if completed {
			return
		}
		_ = os.WriteFile(release, []byte("release\n"), 0o644)
		_ = command.Process.Kill()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}()

	waitForRunScriptPath(t, ready, done, &completed, &commandOutput)
	stages, err := filepath.Glob(
		filepath.Join(evidenceRoot, "."+runID+".partial.*"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(stages) != 1 {
		t.Fatalf("private stage candidates = %v, want one", stages)
	}
	stagePath := stages[0]
	displacedPath := stagePath + ".displaced"
	if err := os.Rename(stagePath, displacedPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(stagePath, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(stagePath, "sentinel")
	writeRunScriptFile(t, sentinel, "preserve\n")
	writeRunScriptFile(t, release, "release\n")

	var runError error
	select {
	case runError = <-done:
		completed = true
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for stage replacement run")
	}
	assertRunScriptExit(t, runError, 1, commandOutput.Bytes())
	content, err := os.ReadFile(sentinel)
	if err != nil {
		t.Fatalf("replacement stage was removed: %v", err)
	}
	if string(content) != "preserve\n" {
		t.Fatalf("replacement stage sentinel changed to %q", content)
	}
	if _, err := os.Stat(displacedPath); err != nil {
		t.Fatalf("displaced private stage was altered: %v", err)
	}
	assertRunNotPublished(t, evidenceRoot, runID)
	if _, err := os.Lstat(worktree + ".lock"); !os.IsNotExist(err) {
		t.Fatalf("worktree lock remained after failure: %v", err)
	}
	if _, err := os.Lstat(
		filepath.Join(evidenceRoot, "."+runID+".lock"),
	); !os.IsNotExist(err) {
		t.Fatalf("run-claim lock remained after failure: %v", err)
	}
}

func TestRunScriptFetchesImmutableTargetFromRequestedSource(t *testing.T) {
	bashPath, runScript := requireRunScriptTestTools(
		t,
		"bash", "git", "jq", "tar", "sha256sum", "flock", "realpath", "awk",
	)
	sourceA, target := initializeRunScriptGitTargetWithContent(t, "source-a\n")
	sourceB, _ := initializeRunScriptGitTargetWithContent(t, "source-b\n")
	tempRoot := t.TempDir()
	worktree := filepath.Join(tempRoot, "target")
	if output, err := exec.Command(
		"git", "clone", "--quiet", sourceA, worktree,
	).CombinedOutput(); err != nil {
		t.Fatalf("clone stale worktree: %v\n%s", err, output)
	}

	evidenceRoot := filepath.Join(tempRoot, "evidence")
	command := exec.Command(
		bashPath,
		runScript,
		"--task", "explain",
		"--variant", "baseline",
		"--run-id", "source-bound",
		"--source", sourceB,
		"--commit", target,
		"--base", target,
		"--worktree", worktree,
		"--evidence-root", evidenceRoot,
	)
	command.Env = runScriptTestEnvironment(t, true)
	output, err := command.CombinedOutput()
	assertRunScriptExit(t, err, 1, output)
	if !strings.Contains(string(output), "failed to fetch target commit") {
		t.Fatalf("source-bound target diagnostic = %q", output)
	}
	assertRunNotPublished(t, evidenceRoot, "source-bound")
}

func TestRunScriptSupportsSHA256TargetRepository(t *testing.T) {
	bashPath, runScript := requireRunScriptTestTools(
		t,
		"bash", "git", "jq", "tar", "sha256sum", "realpath", "stat", "awk",
	)
	sourceRepo, target := initializeRunScriptSHA256GitTarget(t)
	tempRoot := t.TempDir()
	evidenceRoot := filepath.Join(tempRoot, "evidence")
	command := exec.Command(
		bashPath,
		runScript,
		"--task", "explain",
		"--variant", "baseline",
		"--run-id", "sha256-target",
		"--source", sourceRepo,
		"--commit", target,
		"--base", target,
		"--worktree", filepath.Join(tempRoot, "target"),
		"--evidence-root", evidenceRoot,
	)
	command.Env = runScriptTestEnvironment(t, true)
	output, err := command.CombinedOutput()
	assertRunScriptExit(t, err, 99, output)
	for _, unexpected := range []string{
		"failed to fetch target commit",
		"object format mismatch",
	} {
		if strings.Contains(string(output), unexpected) {
			t.Fatalf("SHA-256 target failed before build: %s", output)
		}
	}
	assertRunNotPublished(t, evidenceRoot, "sha256-target")
	assertNoRunScriptPartialDirectories(t, evidenceRoot, "sha256-target")
}

func TestRunScriptIsolatesGitForReusedWorktreeCheckout(t *testing.T) {
	bashPath, runScript := requireRunScriptTestTools(
		t,
		"bash", "git", "jq", "tar", "sha256sum", "flock", "realpath", "awk",
	)
	sourceRepo, first := initializeRunScriptGitTargetWithContent(t, "first\n")
	target := appendRunScriptGitCommit(t, sourceRepo, "second\n")
	tempRoot := t.TempDir()
	worktree := filepath.Join(tempRoot, "target")
	if output, err := exec.Command(
		"git", "clone", "--quiet", sourceRepo, worktree,
	).CombinedOutput(); err != nil {
		t.Fatalf("clone reused worktree: %v\n%s", err, output)
	}
	if output, err := exec.Command(
		"git", "-C", worktree, "checkout", "--quiet", "--detach", first,
	).CombinedOutput(); err != nil {
		t.Fatalf("rewind reused worktree: %v\n%s", err, output)
	}

	hookMarker := filepath.Join(tempRoot, "post-checkout-ran")
	hooksDir := filepath.Join(tempRoot, "hooks")
	if err := os.Mkdir(hooksDir, 0o755); err != nil {
		t.Fatal(err)
	}
	hook := filepath.Join(hooksDir, "post-checkout")
	writeRunScriptFile(
		t,
		hook,
		"#!/bin/sh\n: > \""+hookMarker+"\"\n",
	)
	if err := os.Chmod(hook, 0o755); err != nil {
		t.Fatal(err)
	}
	if output, err := exec.Command(
		"git", "-C", worktree, "config", "core.hooksPath", hooksDir,
	).CombinedOutput(); err != nil {
		t.Fatalf("configure reused-worktree hook: %v\n%s", err, output)
	}

	evidenceRoot := filepath.Join(tempRoot, "evidence")
	command := exec.Command(
		bashPath,
		runScript,
		"--task", "explain",
		"--variant", "baseline",
		"--run-id", "isolated-git",
		"--source", sourceRepo,
		"--commit", target,
		"--base", first,
		"--worktree", worktree,
		"--evidence-root", evidenceRoot,
	)
	command.Env = append(
		runScriptTestEnvironment(t, true),
		"GIT_CONFIG='malformed",
		"GIT_CONFIG_PARAMETERS='malformed",
		"GIT_CONFIG_COUNT=1",
		"GIT_CONFIG_KEY_0=alias.rev-parse",
		"GIT_CONFIG_VALUE_0=!false",
		"GIT_DIR="+filepath.Join(tempRoot, "ambient-git-dir"),
		"GIT_WORK_TREE="+filepath.Join(tempRoot, "ambient-work-tree"),
		"GIT_INDEX_FILE="+filepath.Join(tempRoot, "ambient-index"),
		"GIT_OBJECT_DIRECTORY="+filepath.Join(tempRoot, "ambient-objects"),
		"GIT_ALTERNATE_OBJECT_DIRECTORIES="+filepath.Join(tempRoot, "ambient-alternates"),
		"GIT_COMMON_DIR="+filepath.Join(tempRoot, "ambient-common"),
		"GIT_EXEC_PATH="+filepath.Join(tempRoot, "ambient-exec"),
		"GIT_EXTERNAL_DIFF="+filepath.Join(tempRoot, "ambient-diff"),
		"GIT_SSH="+filepath.Join(tempRoot, "ambient-git-ssh"),
		"GIT_SSH_COMMAND=ambient-git-ssh-command",
		"GIT_SSH_VARIANT=hostile",
		"GIT_ASKPASS="+filepath.Join(tempRoot, "ambient-git-askpass"),
		"SSH_ASKPASS="+filepath.Join(tempRoot, "ambient-ssh-askpass"),
		"GIT_PROXY_COMMAND=ambient-git-proxy",
		"GIT_NAMESPACE=ambient-namespace",
		"GIT_REPLACE_REF_BASE=refs/ambient/",
		"GIT_CEILING_DIRECTORIES=/",
		"GIT_DISCOVERY_ACROSS_FILESYSTEM=1",
		"GIT_NO_REPLACE_OBJECTS=0",
		"GIT_OPTIONAL_LOCKS=1",
	)
	output, err := command.CombinedOutput()
	assertRunScriptExit(t, err, 99, output)
	if _, err := os.Stat(hookMarker); !os.IsNotExist(err) {
		t.Fatalf("ambient post-checkout hook ran: %v", err)
	}
	assertRunNotPublished(t, evidenceRoot, "isolated-git")
	assertNoRunScriptPartialDirectories(t, evidenceRoot, "isolated-git")
}

func TestRunScriptRejectsReusedWorktreeFilters(t *testing.T) {
	bashPath, runScript := requireRunScriptTestTools(
		t,
		"bash", "git", "jq", "tar", "sha256sum", "flock", "realpath", "awk",
	)
	sourceRepo, target := initializeRunScriptGitTarget(t)
	tempRoot := t.TempDir()
	worktree := filepath.Join(tempRoot, "target")
	if output, err := exec.Command(
		"git", "clone", "--quiet", sourceRepo, worktree,
	).CombinedOutput(); err != nil {
		t.Fatalf("clone reused worktree: %v\n%s", err, output)
	}
	filterMarker := filepath.Join(tempRoot, "filter-ran")
	filterCommand := "sh -c ': > \"" + filterMarker + "\"; cat'"
	if output, err := exec.Command(
		"git", "-C", worktree, "config", "filter.ambient.smudge", filterCommand,
	).CombinedOutput(); err != nil {
		t.Fatalf("configure reused-worktree filter: %v\n%s", err, output)
	}

	evidenceRoot := filepath.Join(tempRoot, "evidence")
	command := exec.Command(
		bashPath,
		runScript,
		"--task", "explain",
		"--variant", "baseline",
		"--run-id", "unsafe-filter",
		"--source", sourceRepo,
		"--commit", target,
		"--base", target,
		"--worktree", worktree,
		"--evidence-root", evidenceRoot,
	)
	command.Env = runScriptTestEnvironment(t, true)
	output, err := command.CombinedOutput()
	assertRunScriptExit(t, err, 1, output)
	if !strings.Contains(
		string(output),
		"experiment worktree has unsafe local Git configuration:",
	) {
		t.Fatalf("unsafe-filter diagnostic = %q", output)
	}
	if _, err := os.Stat(filterMarker); !os.IsNotExist(err) {
		t.Fatalf("reused-worktree filter ran: %v", err)
	}
	assertRunNotPublished(t, evidenceRoot, "unsafe-filter")
	assertNoRunScriptPartialDirectories(t, evidenceRoot, "unsafe-filter")
}

func TestRunScriptRejectsHiddenReusedWorktreeState(t *testing.T) {
	bashPath, runScript := requireRunScriptTestTools(
		t,
		"bash", "git", "jq", "tar", "sha256sum", "flock", "realpath", "awk",
	)
	sourceRepo, _ := initializeRunScriptGitTarget(t)
	writeRunScriptFile(
		t,
		filepath.Join(sourceRepo, ".gitignore"),
		"ignored.go\n",
	)
	for _, arguments := range [][]string{
		{"add", ".gitignore"},
		{
			"-c", "user.name=Run Script Test",
			"-c", "user.email=run-script-test@example.invalid",
			"-c", "commit.gpgSign=false",
			"-c", "core.hooksPath=/dev/null",
			"commit", "--quiet", "--no-gpg-sign", "--no-verify", "-m", "ignore fixture",
		},
	} {
		command := exec.Command(
			"git",
			append([]string{"-C", sourceRepo}, arguments...)...,
		)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s failed: %v\n%s", arguments[0], err, output)
		}
	}
	targetOutput, err := exec.Command(
		"git", "-C", sourceRepo, "rev-parse", "HEAD",
	).Output()
	if err != nil {
		t.Fatal(err)
	}
	target := strings.TrimSpace(string(targetOutput))

	for _, test := range []struct {
		name       string
		prepare    func(*testing.T, string)
		diagnostic string
	}{
		{
			name: "assume unchanged",
			prepare: func(t *testing.T, worktree string) {
				t.Helper()
				writeRunScriptFile(
					t,
					filepath.Join(worktree, "fixture.txt"),
					"hidden modification\n",
				)
				if output, err := exec.Command(
					"git", "-C", worktree,
					"update-index", "--assume-unchanged", "fixture.txt",
				).CombinedOutput(); err != nil {
					t.Fatalf("mark assume-unchanged: %v\n%s", err, output)
				}
			},
			diagnostic: "experiment worktree has non-default index flags:",
		},
		{
			name: "ignored untracked",
			prepare: func(t *testing.T, worktree string) {
				t.Helper()
				writeRunScriptFile(
					t,
					filepath.Join(worktree, "ignored.go"),
					"package hidden\n",
				)
			},
			diagnostic: "untracked or ignored worktree paths would survive checkout:",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			tempRoot := t.TempDir()
			worktree := filepath.Join(tempRoot, "target")
			if output, err := exec.Command(
				"git", "clone", "--quiet", sourceRepo, worktree,
			).CombinedOutput(); err != nil {
				t.Fatalf("clone reused worktree: %v\n%s", err, output)
			}
			test.prepare(t, worktree)

			evidenceRoot := filepath.Join(tempRoot, "evidence")
			command := exec.Command(
				bashPath,
				runScript,
				"--task", "explain",
				"--variant", "baseline",
				"--run-id", "hidden-worktree-state",
				"--source", sourceRepo,
				"--commit", target,
				"--base", target,
				"--worktree", worktree,
				"--evidence-root", evidenceRoot,
			)
			command.Env = runScriptTestEnvironment(t, true)
			output, err := command.CombinedOutput()
			assertRunScriptExit(t, err, 1, output)
			if !strings.Contains(string(output), test.diagnostic) {
				t.Fatalf("hidden-worktree-state diagnostic = %q", output)
			}
			assertRunNotPublished(t, evidenceRoot, "hidden-worktree-state")
			assertNoRunScriptPartialDirectories(
				t,
				evidenceRoot,
				"hidden-worktree-state",
			)
		})
	}
}

func TestRunScriptRejectsTargetSubmodules(t *testing.T) {
	bashPath, runScript := requireRunScriptTestTools(
		t,
		"bash", "git", "jq", "tar", "sha256sum", "flock", "realpath", "awk",
	)
	submoduleRepo, _ := initializeRunScriptGitTargetWithContent(t, "nested\n")
	sourceRepo, _ := initializeRunScriptGitTarget(t)
	// Keep enough entries after the submodule to fill the ls-files pipe. This
	// catches first-match awk filters that exit early and turn the intended
	// validation failure into SIGPIPE under pipefail.
	if err := os.MkdirAll(filepath.Join(sourceRepo, "zz-fixtures"), 0o755); err != nil {
		t.Fatal(err)
	}
	for index := range 2_000 {
		writeRunScriptFile(
			t,
			filepath.Join(sourceRepo, "zz-fixtures", fmt.Sprintf("%04d.txt", index)),
			strings.Repeat("x", 64)+"\n",
		)
	}
	if output, err := exec.Command(
		"git", "-C", sourceRepo,
		"-c", "protocol.file.allow=always",
		"submodule", "add", "--quiet", submoduleRepo, "nested",
	).CombinedOutput(); err != nil {
		t.Fatalf("add target submodule: %v\n%s", err, output)
	}
	for _, arguments := range [][]string{
		{"add", ".gitmodules", "nested", "zz-fixtures"},
		{
			"-c", "user.name=Run Script Test",
			"-c", "user.email=run-script-test@example.invalid",
			"-c", "commit.gpgSign=false",
			"-c", "core.hooksPath=/dev/null",
			"commit", "--quiet", "--no-gpg-sign", "--no-verify", "-m", "submodule",
		},
	} {
		command := exec.Command(
			"git",
			append([]string{"-C", sourceRepo}, arguments...)...,
		)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s failed: %v\n%s", arguments[0], err, output)
		}
	}
	targetOutput, err := exec.Command(
		"git", "-C", sourceRepo, "rev-parse", "HEAD",
	).Output()
	if err != nil {
		t.Fatal(err)
	}
	target := strings.TrimSpace(string(targetOutput))

	tempRoot := t.TempDir()
	evidenceRoot := filepath.Join(tempRoot, "evidence")
	command := exec.Command(
		bashPath,
		runScript,
		"--task", "explain",
		"--variant", "baseline",
		"--run-id", "submodules",
		"--source", sourceRepo,
		"--commit", target,
		"--base", target,
		"--worktree", filepath.Join(tempRoot, "target"),
		"--evidence-root", evidenceRoot,
	)
	command.Env = runScriptTestEnvironment(t, true)
	output, err := command.CombinedOutput()
	assertRunScriptExit(t, err, 1, output)
	if !strings.Contains(
		string(output),
		"target contains submodules that are not materialized reproducibly:",
	) {
		t.Fatalf("submodule diagnostic = %q", output)
	}
	assertRunNotPublished(t, evidenceRoot, "submodules")
	assertNoRunScriptPartialDirectories(t, evidenceRoot, "submodules")
}

func TestRunScriptRequiresIgnoredWorktreeLockPath(t *testing.T) {
	bashPath, _ := requireRunScriptTestTools(
		t,
		"bash", "git", "jq", "tar", "sha256sum", "flock", "realpath", "awk",
	)
	sourceRoot, runScript := initializeRunScriptSourceFixture(t)
	writeRunScriptFile(
		t,
		filepath.Join(sourceRoot, ".gitignore"),
		"generated/target/\n",
	)
	worktree := filepath.Join(sourceRoot, "generated", "target")
	if err := os.MkdirAll(worktree, 0o755); err != nil {
		t.Fatal(err)
	}
	tempRoot := t.TempDir()
	evidenceRoot := filepath.Join(tempRoot, "evidence")
	command := exec.Command(
		bashPath,
		runScript,
		"--task", "explain",
		"--variant", "baseline",
		"--run-id", "worktree-lock-ignore",
		"--worktree", worktree,
		"--evidence-root", evidenceRoot,
	)
	command.Env = runScriptTestEnvironment(t, true)
	output, err := command.CombinedOutput()
	assertRunScriptExit(t, err, 2, output)
	if !strings.Contains(
		string(output),
		"--worktree lock inside the repo-view source must be ignored by Git:",
	) {
		t.Fatalf("worktree-lock diagnostic = %q", output)
	}
	assertRunNotPublished(t, evidenceRoot, "worktree-lock-ignore")
	assertNoRunScriptPartialDirectories(t, evidenceRoot, "worktree-lock-ignore")
}

func TestRunScriptRejectsUnsafeRepoViewSourceGitConfig(t *testing.T) {
	bashPath, _ := requireRunScriptTestTools(
		t,
		"bash", "git", "jq", "tar", "sha256sum", "flock", "realpath", "awk",
	)
	sourceRoot, runScript := initializeRunScriptSourceFixture(t)
	filterMarker := filepath.Join(t.TempDir(), "source-filter-ran")
	filterCommand := "sh -c ': > \"" + filterMarker + "\"; cat'"
	if output, err := exec.Command(
		"git", "-C", sourceRoot,
		"config", "filter.ambient.smudge", filterCommand,
	).CombinedOutput(); err != nil {
		t.Fatalf("configure source filter: %v\n%s", err, output)
	}

	tempRoot := t.TempDir()
	evidenceRoot := filepath.Join(tempRoot, "evidence")
	command := exec.Command(
		bashPath,
		runScript,
		"--task", "explain",
		"--variant", "baseline",
		"--run-id", "unsafe-source-config",
		"--worktree", filepath.Join(tempRoot, "target"),
		"--evidence-root", evidenceRoot,
	)
	command.Env = runScriptTestEnvironment(t, true)
	output, err := command.CombinedOutput()
	assertRunScriptExit(t, err, 1, output)
	if !strings.Contains(
		string(output),
		"repo-view source has unsafe local Git configuration:",
	) {
		t.Fatalf("unsafe-source-config diagnostic = %q", output)
	}
	if _, err := os.Stat(filterMarker); !os.IsNotExist(err) {
		t.Fatalf("repo-view source filter ran: %v", err)
	}
	assertRunNotPublished(t, evidenceRoot, "unsafe-source-config")
	assertNoRunScriptPartialDirectories(t, evidenceRoot, "unsafe-source-config")
}

func TestRunScriptRequiresAncestorCommitBase(t *testing.T) {
	bashPath, runScript := requireRunScriptTestTools(
		t,
		"bash", "git", "jq", "tar", "sha256sum", "flock", "realpath", "awk",
	)
	sourceRepo, first := initializeRunScriptGitTargetWithContent(t, "first\n")
	second := appendRunScriptGitCommit(t, sourceRepo, "second\n")

	for _, test := range []struct {
		name       string
		target     string
		base       string
		diagnostic string
	}{
		{
			name:       "non-commit",
			target:     second,
			base:       "HEAD^{tree}",
			diagnostic: "base does not resolve to a commit:",
		},
		{
			name:       "not ancestor",
			target:     first,
			base:       second,
			diagnostic: "base commit is not an ancestor of target:",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			tempRoot := t.TempDir()
			evidenceRoot := filepath.Join(tempRoot, "evidence")
			command := exec.Command(
				bashPath,
				runScript,
				"--task", "explain",
				"--variant", "baseline",
				"--run-id", "invalid-base",
				"--source", sourceRepo,
				"--commit", test.target,
				"--base", test.base,
				"--worktree", filepath.Join(tempRoot, "target"),
				"--evidence-root", evidenceRoot,
			)
			command.Env = runScriptTestEnvironment(t, true)
			output, err := command.CombinedOutput()
			assertRunScriptExit(t, err, 1, output)
			if !strings.Contains(string(output), test.diagnostic) {
				t.Fatalf("base diagnostic = %q", output)
			}
			assertRunNotPublished(t, evidenceRoot, "invalid-base")
		})
	}
}

func TestRunScriptResolvesNamedBaseFromRequestedSource(t *testing.T) {
	bashPath, runScript := requireRunScriptTestTools(
		t,
		"bash", "git", "jq", "tar", "sha256sum", "flock", "realpath", "awk",
	)
	sourceA, _ := initializeRunScriptGitTargetWithContent(t, "source-a\n")
	sourceB := filepath.Join(t.TempDir(), "source-b")
	if output, err := exec.Command(
		"git", "clone", "--quiet", sourceA, sourceB,
	).CombinedOutput(); err != nil {
		t.Fatalf("clone updated source: %v\n%s", err, output)
	}
	target := appendRunScriptGitCommit(t, sourceB, "source-b\n")
	tempRoot := t.TempDir()
	worktree := filepath.Join(tempRoot, "target")
	if output, err := exec.Command(
		"git", "clone", "--quiet", sourceA, worktree,
	).CombinedOutput(); err != nil {
		t.Fatalf("clone stale worktree: %v\n%s", err, output)
	}
	baselineDir := filepath.Join(tempRoot, "baseline")
	if err := os.Mkdir(baselineDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRunScriptJSON(
		t,
		filepath.Join(baselineDir, "manifest.json"),
		runScriptBaselineManifest(
			t,
			target,
			target[:9],
			target,
			"main",
			runScriptFakeGoVersion,
		),
	)
	writeRunScriptFile(
		t,
		filepath.Join(baselineDir, "baseline-explain.jsonl"),
		runScriptCompletedJSONL,
	)
	writeRunScriptFile(
		t,
		filepath.Join(baselineDir, "baseline-explain.exit-code"),
		"0\n",
	)

	evidenceRoot := filepath.Join(tempRoot, "evidence")
	command := exec.Command(
		bashPath,
		runScript,
		"--task", "explain",
		"--variant", "baseline",
		"--baseline-from", baselineDir,
		"--run-id", "source-base",
		"--source", sourceB,
		"--commit", target,
		"--prompt-commit", target[:9],
		"--base", "main",
		"--worktree", worktree,
		"--evidence-root", evidenceRoot,
	)
	command.Env = runScriptTestEnvironment(t, true)
	output, err := command.CombinedOutput()
	assertRunScriptExit(t, err, 99, output)
	if strings.Contains(string(output), "baseline manifest base_commit mismatch:") {
		t.Fatalf("named base resolved from stale worktree:\n%s", output)
	}
	assertRunNotPublished(t, evidenceRoot, "source-base")
	assertNoRunScriptPartialDirectories(t, evidenceRoot, "source-base")
}

func TestRunScriptRejectsUnignoredEvidenceRootInsideSource(t *testing.T) {
	bashPath, _ := requireRunScriptTestTools(
		t,
		"bash", "git", "jq", "tar", "sha256sum", "flock", "realpath", "awk",
	)
	fixtureRoot, runScript := initializeRunScriptSourceFixture(t)
	sourceRepo, target := initializeRunScriptGitTarget(t)
	evidenceRoot := filepath.Join(fixtureRoot, "unignored-evidence")
	command := exec.Command(
		bashPath,
		runScript,
		"--task", "explain",
		"--variant", "baseline",
		"--run-id", "recursive",
		"--source", sourceRepo,
		"--commit", target,
		"--base", target,
		"--worktree", filepath.Join(t.TempDir(), "target"),
		"--evidence-root", evidenceRoot,
	)
	command.Env = runScriptTestEnvironment(t, true)
	output, err := command.CombinedOutput()
	assertRunScriptExit(t, err, 2, output)
	if !strings.Contains(
		string(output),
		"--evidence-root inside the repo-view source must be ignored by Git:",
	) {
		t.Fatalf("recursive evidence-root diagnostic = %q", output)
	}
	assertRunNotPublished(t, evidenceRoot, "recursive")
}

func TestRunScriptRejectsConcurrentRepoViewSourceChange(t *testing.T) {
	bashPath, _ := requireRunScriptTestTools(
		t,
		"bash", "git", "jq", "tar", "sha256sum", "flock", "realpath", "awk",
	)
	fixtureRoot, runScript := initializeRunScriptSourceFixture(t)
	sourceRepo, target := initializeRunScriptGitTarget(t)
	tempRoot := t.TempDir()
	evidenceRoot := filepath.Join(tempRoot, "evidence")
	ready := filepath.Join(tempRoot, "tar-ready")
	release := filepath.Join(tempRoot, "tar-release")
	command := exec.Command(
		bashPath,
		runScript,
		"--task", "explain",
		"--variant", "baseline",
		"--run-id", "source-race",
		"--source", sourceRepo,
		"--commit", target,
		"--base", target,
		"--worktree", filepath.Join(tempRoot, "target"),
		"--evidence-root", evidenceRoot,
	)
	command.Env = runScriptTarPauseEnvironment(
		t,
		runScriptTestEnvironment(t, true),
		ready,
		release,
	)
	var commandOutput bytes.Buffer
	command.Stdout = &commandOutput
	command.Stderr = &commandOutput
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- command.Wait()
	}()
	completed := false
	defer func() {
		if completed {
			return
		}
		_ = os.WriteFile(release, []byte("release\n"), 0o644)
		_ = command.Process.Kill()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}()

	waitForRunScriptPath(t, ready, done, &completed, &commandOutput)
	writeRunScriptFile(
		t,
		filepath.Join(fixtureRoot, "snapshot-sentinel.txt"),
		"changed during snapshot\n",
	)
	writeRunScriptFile(t, release, "release\n")

	var runError error
	select {
	case runError = <-done:
		completed = true
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for source-race rejection")
	}
	assertRunScriptExit(t, runError, 1, commandOutput.Bytes())
	if !strings.Contains(
		commandOutput.String(),
		"repo-view source changed while snapshotting:",
	) {
		t.Fatalf("source-race diagnostic = %q", commandOutput.String())
	}
	assertRunNotPublished(t, evidenceRoot, "source-race")
	assertNoRunScriptPartialDirectories(t, evidenceRoot, "source-race")
}

func TestRunScriptRejectsRepoViewSourceSymlink(t *testing.T) {
	bashPath, _ := requireRunScriptTestTools(
		t,
		"bash", "git", "jq", "tar", "sha256sum", "realpath", "awk",
	)
	fixtureRoot, runScript := initializeRunScriptSourceFixture(t)
	sourceRepo, target := initializeRunScriptGitTarget(t)
	tempRoot := t.TempDir()
	external := filepath.Join(tempRoot, "external-source")
	writeRunScriptFile(t, external, "outside\n")
	sourceLink := filepath.Join(fixtureRoot, "external-source-link")
	if err := os.Symlink(external, sourceLink); err != nil {
		t.Skipf("cannot create source symlink: %v", err)
	}
	evidenceRoot := filepath.Join(tempRoot, "evidence")
	worktree := filepath.Join(tempRoot, "target")
	command := exec.Command(
		bashPath,
		runScript,
		"--task", "explain",
		"--variant", "baseline",
		"--run-id", "source-link",
		"--source", sourceRepo,
		"--commit", target,
		"--base", target,
		"--worktree", worktree,
		"--evidence-root", evidenceRoot,
	)
	command.Env = runScriptTestEnvironment(t, true)
	output, err := command.CombinedOutput()
	assertRunScriptExit(t, err, 1, output)
	if !strings.Contains(
		string(output),
		"repo-view source contains non-regular file: external-source-link",
	) {
		t.Fatalf("source-symlink diagnostic = %q", output)
	}
	content, err := os.ReadFile(external)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "outside\n" {
		t.Fatalf("external symlink target changed to %q", content)
	}
	assertRunNotPublished(t, evidenceRoot, "source-link")
	assertNoRunScriptPartialDirectories(t, evidenceRoot, "source-link")
	if _, err := os.Lstat(worktree + ".lock"); !os.IsNotExist(err) {
		t.Fatalf("worktree lock remained after source rejection: %v", err)
	}
	if _, err := os.Lstat(
		filepath.Join(evidenceRoot, ".source-link.lock"),
	); !os.IsNotExist(err) {
		t.Fatalf("run-claim lock remained after source rejection: %v", err)
	}
}

func TestRunScriptRejectsRepoViewSourceFIFOWithoutBlocking(t *testing.T) {
	bashPath, _ := requireRunScriptTestTools(
		t,
		"bash", "git", "jq", "mkfifo", "tar", "sha256sum", "realpath", "awk",
	)
	fixtureRoot, runScript := initializeRunScriptSourceFixture(t)
	sourceRepo, target := initializeRunScriptGitTarget(t)
	fifoPath := filepath.Join(fixtureRoot, "source-fifo")
	if output, err := exec.Command("mkfifo", fifoPath).CombinedOutput(); err != nil {
		t.Skipf("cannot create source FIFO: %v\n%s", err, output)
	}
	hashCommand := exec.Command(
		"git", "-C", fixtureRoot, "hash-object", "-w", "--stdin",
	)
	hashCommand.Stdin = strings.NewReader("indexed FIFO placeholder\n")
	blobOutput, err := hashCommand.Output()
	if err != nil {
		t.Fatal(err)
	}
	blob := strings.TrimSpace(string(blobOutput))
	if output, err := exec.Command(
		"git",
		"-C", fixtureRoot,
		"update-index",
		"--add",
		"--cacheinfo", "100644,"+blob+",source-fifo",
	).CombinedOutput(); err != nil {
		t.Fatalf("index FIFO path: %v\n%s", err, output)
	}
	tempRoot := t.TempDir()
	evidenceRoot := filepath.Join(tempRoot, "evidence")
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	command := exec.CommandContext(
		ctx,
		bashPath,
		runScript,
		"--task", "explain",
		"--variant", "baseline",
		"--run-id", "source-fifo",
		"--source", sourceRepo,
		"--commit", target,
		"--base", target,
		"--worktree", filepath.Join(tempRoot, "target"),
		"--evidence-root", evidenceRoot,
	)
	command.Env = runScriptTestEnvironment(t, true)
	output, err := command.CombinedOutput()
	if ctx.Err() != nil {
		t.Fatalf("runner blocked while snapshotting FIFO:\n%s", output)
	}
	assertRunScriptExit(t, err, 1, output)
	if !strings.Contains(
		string(output),
		"repo-view source contains non-regular file: source-fifo",
	) {
		t.Fatalf("source-FIFO diagnostic = %q", output)
	}
	assertRunNotPublished(t, evidenceRoot, "source-fifo")
	assertNoRunScriptPartialDirectories(t, evidenceRoot, "source-fifo")
}

func TestRunScriptRejectsRepoViewSourceHardlink(t *testing.T) {
	bashPath, _ := requireRunScriptTestTools(
		t,
		"bash", "git", "jq", "tar", "sha256sum", "realpath", "stat", "awk",
	)
	fixtureRoot, runScript := initializeRunScriptSourceFixture(t)
	sourceRepo, target := initializeRunScriptGitTarget(t)
	tempRoot := t.TempDir()
	external := filepath.Join(tempRoot, "external-source")
	writeRunScriptFile(t, external, "outside\n")
	sourceLink := filepath.Join(fixtureRoot, "external-source-hardlink")
	if err := os.Link(external, sourceLink); err != nil {
		t.Skipf("cannot create source hardlink: %v", err)
	}
	evidenceRoot := filepath.Join(tempRoot, "evidence")
	command := exec.Command(
		bashPath,
		runScript,
		"--task", "explain",
		"--variant", "baseline",
		"--run-id", "source-hardlink",
		"--source", sourceRepo,
		"--commit", target,
		"--base", target,
		"--worktree", filepath.Join(tempRoot, "target"),
		"--evidence-root", evidenceRoot,
	)
	command.Env = runScriptTestEnvironment(t, true)
	output, err := command.CombinedOutput()
	assertRunScriptExit(t, err, 1, output)
	if !strings.Contains(
		string(output),
		"repo-view source contains multiply-linked file: "+
			"external-source-hardlink",
	) {
		t.Fatalf("source-hardlink diagnostic = %q", output)
	}
	content, err := os.ReadFile(external)
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "outside\n" {
		t.Fatalf("external hardlink target changed to %q", content)
	}
	assertRunNotPublished(t, evidenceRoot, "source-hardlink")
	assertNoRunScriptPartialDirectories(t, evidenceRoot, "source-hardlink")
}

func TestRunScriptValidatesBaselineBeforeImport(t *testing.T) {
	bashPath, _ := requireRunScriptTestTools(
		t,
		"bash", "git", "jq", "tar", "sha256sum", "flock", "realpath", "awk",
	)
	_, runScript := initializeRunScriptSourceFixture(t)
	sourceRepo, head := initializeRunScriptGitTarget(t)
	compatibleManifest, err := json.Marshal(runScriptBaselineManifest(
		t,
		head,
		head[:9],
		head,
		head,
		runScriptFakeGoVersion,
	))
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name            string
		task            string
		manifest        bool
		manifestContent string
		mutate          func(map[string]any)
		removeConfig    bool
		configContent   *string
		jsonl           *string
		exitCode        *string
		wantOutput      string
	}{
		{
			name:       "missing manifest",
			wantOutput: "baseline manifest missing:",
		},
		{
			name:       "missing JSONL",
			manifest:   true,
			wantOutput: "baseline JSONL missing for explain:",
		},
		{
			name:         "missing generation config",
			manifest:     true,
			removeConfig: true,
			jsonl:        stringPointer(runScriptCompletedJSONL),
			exitCode:     stringPointer("0\n"),
			wantOutput:   "baseline generation config missing:",
		},
		{
			name:          "generation config bytes mismatch",
			manifest:      true,
			configContent: stringPointer("{\"tampered\":true}"),
			jsonl:         stringPointer(runScriptCompletedJSONL),
			exitCode:      stringPointer("0\n"),
			wantOutput:    "baseline generation config digest mismatch:",
		},
		{
			name:       "incomplete JSONL",
			manifest:   true,
			jsonl:      stringPointer("{\"type\":\"item.completed\"}\n"),
			exitCode:   stringPointer("0\n"),
			wantOutput: "baseline JSONL is invalid or incomplete for explain:",
		},
		{
			name:     "thread starts after turn",
			manifest: true,
			jsonl: stringPointer(
				"{\"type\":\"turn.started\"}\n" +
					"{\"type\":\"thread.started\",\"thread_id\":\"test\"}\n" +
					"{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":1,\"cached_input_tokens\":0,\"output_tokens\":1}}\n",
			),
			exitCode:   stringPointer("0\n"),
			wantOutput: "baseline JSONL is invalid or incomplete for explain:",
		},
		{
			name:     "command event precedes turn",
			manifest: true,
			jsonl: stringPointer(
				"{\"type\":\"thread.started\",\"thread_id\":\"test\"}\n" +
					"{\"type\":\"item.completed\",\"item\":{\"type\":\"command_execution\"}}\n" +
					"{\"type\":\"turn.started\"}\n" +
					"{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":1,\"cached_input_tokens\":0,\"output_tokens\":1}}\n",
			),
			exitCode:   stringPointer("0\n"),
			wantOutput: "baseline JSONL is invalid or incomplete for explain:",
		},
		{
			name:     "terminal event is not last",
			manifest: true,
			jsonl: stringPointer(
				runScriptCompletedJSONL +
					"{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\"}}\n",
			),
			exitCode:   stringPointer("0\n"),
			wantOutput: "baseline JSONL is invalid or incomplete for explain:",
		},
		{
			name:     "thread id is empty",
			manifest: true,
			jsonl: stringPointer(
				"{\"type\":\"thread.started\",\"thread_id\":\"\"}\n" +
					"{\"type\":\"turn.started\"}\n" +
					"{\"type\":\"turn.completed\",\"usage\":{\"input_tokens\":1,\"cached_input_tokens\":0,\"output_tokens\":1}}\n",
			),
			exitCode:   stringPointer("0\n"),
			wantOutput: "baseline JSONL is invalid or incomplete for explain:",
		},
		{
			name:       "completed event missing usage",
			manifest:   true,
			jsonl:      stringPointer("{\"type\":\"turn.completed\"}\n"),
			exitCode:   stringPointer("0\n"),
			wantOutput: "baseline JSONL is invalid or incomplete for explain:",
		},
		{
			name:     "completed event has unusable usage",
			manifest: true,
			jsonl: stringPointer(
				"{\"type\":\"turn.completed\",\"usage\":[]}\n",
			),
			exitCode:   stringPointer("0\n"),
			wantOutput: "baseline JSONL is invalid or incomplete for explain:",
		},
		{
			name:     "fractional token usage",
			manifest: true,
			jsonl: stringPointer(
				`{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":0.5}}` + "\n",
			),
			exitCode:   stringPointer("0\n"),
			wantOutput: "baseline JSONL is invalid or incomplete for explain:",
		},
		{
			name:     "cached tokens exceed input",
			manifest: true,
			jsonl: stringPointer(
				`{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":2,"output_tokens":1}}` + "\n",
			),
			exitCode:   stringPointer("0\n"),
			wantOutput: "baseline JSONL is invalid or incomplete for explain:",
		},
		{
			name:     "fractional reasoning usage",
			manifest: true,
			jsonl: stringPointer(
				`{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":1,"reasoning_output_tokens":0.5}}` + "\n",
			),
			exitCode:   stringPointer("0\n"),
			wantOutput: "baseline JSONL is invalid or incomplete for explain:",
		},
		{
			name:       "non-object JSONL event",
			manifest:   true,
			jsonl:      stringPointer("null\n" + runScriptCompletedJSONL),
			exitCode:   stringPointer("0\n"),
			wantOutput: "baseline JSONL is invalid or incomplete for explain:",
		},
		{
			name:     "concatenated completed sessions",
			manifest: true,
			jsonl: stringPointer(
				runScriptCompletedJSONL + runScriptCompletedJSONL,
			),
			exitCode:   stringPointer("0\n"),
			wantOutput: "baseline JSONL is invalid or incomplete for explain:",
		},
		{
			name:       "missing exit code",
			manifest:   true,
			jsonl:      stringPointer(runScriptCompletedJSONL),
			wantOutput: "baseline exit code missing for explain:",
		},
		{
			name:       "failed exit code",
			manifest:   true,
			jsonl:      stringPointer(runScriptCompletedJSONL),
			exitCode:   stringPointer("1\n"),
			wantOutput: "baseline exit code is not zero for explain: 1",
		},
		{
			name:       "later selected task missing",
			task:       "all",
			manifest:   true,
			jsonl:      stringPointer(runScriptCompletedJSONL),
			exitCode:   stringPointer("0\n"),
			wantOutput: "baseline JSONL missing for review:",
		},
		{
			name: "multiple manifest documents",
			manifestContent: "{}\n" +
				string(compatibleManifest) + "\n",
			jsonl:      stringPointer(runScriptCompletedJSONL),
			exitCode:   stringPointer("0\n"),
			wantOutput: "baseline manifest is invalid:",
		},
		{
			name:     "target mismatch",
			manifest: true,
			mutate: func(manifest map[string]any) {
				manifest["target_commit"] = strings.Repeat("0", 40)
			},
			jsonl:      stringPointer(runScriptCompletedJSONL),
			exitCode:   stringPointer("0\n"),
			wantOutput: "baseline manifest target_commit mismatch:",
		},
		{
			name:     "prompt mismatch",
			manifest: true,
			mutate: func(manifest map[string]any) {
				manifest["prompt_commit"] = "other-prompt"
			},
			jsonl:      stringPointer(runScriptCompletedJSONL),
			exitCode:   stringPointer("0\n"),
			wantOutput: "baseline manifest prompt_commit mismatch:",
		},
		{
			name:     "base commit mismatch",
			manifest: true,
			mutate: func(manifest map[string]any) {
				manifest["base_commit"] = strings.Repeat("0", 40)
			},
			jsonl:      stringPointer(runScriptCompletedJSONL),
			exitCode:   stringPointer("0\n"),
			wantOutput: "baseline manifest base_commit mismatch:",
		},
		{
			name:     "base ref mismatch",
			manifest: true,
			mutate: func(manifest map[string]any) {
				manifest["base_ref"] = "other-base"
			},
			jsonl:      stringPointer(runScriptCompletedJSONL),
			exitCode:   stringPointer("0\n"),
			wantOutput: "baseline manifest base_ref mismatch:",
		},
		{
			name:     "model mismatch",
			manifest: true,
			mutate: func(manifest map[string]any) {
				manifest["model"] = "other-model"
			},
			jsonl:      stringPointer(runScriptCompletedJSONL),
			exitCode:   stringPointer("0\n"),
			wantOutput: "baseline manifest model mismatch:",
		},
		{
			name:     "Codex version mismatch",
			manifest: true,
			mutate: func(manifest map[string]any) {
				manifest["codex_version"] = "codex-test 0"
			},
			jsonl:      stringPointer(runScriptCompletedJSONL),
			exitCode:   stringPointer("0\n"),
			wantOutput: "baseline manifest codex_version mismatch:",
		},
		{
			name:     "generation isolation mismatch",
			manifest: true,
			mutate: func(manifest map[string]any) {
				manifest["generation_isolation"] = "legacy-ambient-v0"
			},
			jsonl:      stringPointer(runScriptCompletedJSONL),
			exitCode:   stringPointer("0\n"),
			wantOutput: "baseline manifest generation_isolation mismatch:",
		},
		{
			name:     "generation config mismatch",
			manifest: true,
			mutate: func(manifest map[string]any) {
				manifest["generation_config_sha256"] = strings.Repeat("0", 64)
			},
			jsonl:      stringPointer(runScriptCompletedJSONL),
			exitCode:   stringPointer("0\n"),
			wantOutput: "baseline manifest generation_config_sha256 mismatch:",
		},
		{
			name:     "missing prompt digest",
			manifest: true,
			mutate: func(manifest map[string]any) {
				delete(
					manifest["prompt_digests"].(map[string]string),
					"explain",
				)
			},
			jsonl:      stringPointer(runScriptCompletedJSONL),
			exitCode:   stringPointer("0\n"),
			wantOutput: "baseline manifest is missing prompt digest for explain:",
		},
		{
			name:     "prompt digest mismatch",
			manifest: true,
			mutate: func(manifest map[string]any) {
				manifest["prompt_digests"].(map[string]string)["explain"] =
					strings.Repeat("0", 64)
			},
			jsonl:      stringPointer(runScriptCompletedJSONL),
			exitCode:   stringPointer("0\n"),
			wantOutput: "baseline manifest prompt digest mismatch for explain:",
		},
		{
			name:     "absolute Go paths are audit only",
			manifest: true,
			mutate: func(manifest map[string]any) {
				manifest["go_root"] = "/relocated/go"
				manifest["go_path"] = "/relocated/path"
				manifest["go_mod_cache"] = "/relocated/mod"
				manifest["go_cache"] = "/relocated/cache"
			},
			jsonl:      stringPointer("{\"type\":\"item.completed\"}\n"),
			exitCode:   stringPointer("0\n"),
			wantOutput: "baseline JSONL is invalid or incomplete for explain:",
		},
		{
			name:     "Go version mismatch",
			manifest: true,
			mutate: func(manifest map[string]any) {
				manifest["go_version"] = "go version go0.0 test"
			},
			jsonl:      stringPointer(runScriptCompletedJSONL),
			exitCode:   stringPointer("0\n"),
			wantOutput: "baseline manifest go_version mismatch:",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tempRoot := t.TempDir()
			baselineDir := filepath.Join(tempRoot, "baseline")
			if err := os.Mkdir(baselineDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if test.manifest || test.manifestContent != "" {
				manifestTasks := []string{"explain"}
				if test.task == "all" {
					manifestTasks = []string{"explain", "review"}
				}
				manifest := runScriptBaselineManifest(
					t,
					head,
					head[:9],
					head,
					head,
					runScriptFakeGoVersion,
					manifestTasks...,
				)
				canonicalGenerationConfig := runScriptGenerationConfig(t, manifest)
				if test.mutate != nil {
					test.mutate(manifest)
				}
				manifestPath := filepath.Join(baselineDir, "manifest.json")
				if test.manifestContent != "" {
					writeRunScriptFile(t, manifestPath, test.manifestContent)
					if err := os.WriteFile(
						filepath.Join(baselineDir, "generation-config.json"),
						canonicalGenerationConfig,
						0o644,
					); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(
						filepath.Join(baselineDir, "profiles-snapshot.tsv"),
						runScriptProfilesSnapshot(t),
						0o644,
					); err != nil {
						t.Fatal(err)
					}
					if err := os.Mkdir(
						filepath.Join(baselineDir, "prompts"),
						0o755,
					); err != nil {
						t.Fatal(err)
					}
					for _, promptTask := range manifestTasks {
						writeRunScriptFile(
							t,
							filepath.Join(
								baselineDir,
								"prompts",
								promptTask+".txt",
							),
							runScriptRenderedPrompt(
								t,
								promptTask,
								head[:9],
								head,
							),
						)
					}
				} else {
					writeRunScriptJSON(t, manifestPath, manifest)
					if err := os.WriteFile(
						filepath.Join(baselineDir, "generation-config.json"),
						canonicalGenerationConfig,
						0o644,
					); err != nil {
						t.Fatal(err)
					}
				}
			}
			if test.jsonl != nil {
				writeRunScriptFile(
					t,
					filepath.Join(baselineDir, "baseline-explain.jsonl"),
					*test.jsonl,
				)
			}
			generationConfigPath := filepath.Join(
				baselineDir,
				"generation-config.json",
			)
			if test.removeConfig {
				if err := os.Remove(generationConfigPath); err != nil {
					t.Fatal(err)
				}
			} else if test.configContent != nil {
				writeRunScriptFile(
					t,
					generationConfigPath,
					*test.configContent,
				)
			}
			if test.exitCode != nil {
				writeRunScriptFile(
					t,
					filepath.Join(baselineDir, "baseline-explain.exit-code"),
					*test.exitCode,
				)
			}

			evidenceRoot := filepath.Join(tempRoot, "evidence")
			runDir := filepath.Join(evidenceRoot, "import")
			selectedTask := test.task
			if selectedTask == "" {
				selectedTask = "explain"
			}
			command := exec.Command(
				bashPath,
				runScript,
				"--task", selectedTask,
				"--variant", "baseline",
				"--baseline-from", baselineDir,
				"--run-id", "import",
				"--source", sourceRepo,
				"--commit", head,
				"--prompt-commit", head[:9],
				"--base", head,
				"--worktree", filepath.Join(tempRoot, "target"),
				"--evidence-root", evidenceRoot,
			)
			command.Env = runScriptTestEnvironment(t, true)
			output, err := command.CombinedOutput()
			assertRunScriptExit(t, err, 1, output)
			if !strings.Contains(string(output), test.wantOutput) {
				t.Fatalf("baseline diagnostic = %q, want substring %q", output, test.wantOutput)
			}
			if _, err := os.Stat(filepath.Join(runDir, "baseline-explain.jsonl")); !os.IsNotExist(err) {
				t.Fatalf("baseline JSONL was copied before validation completed: %v", err)
			}
		})
	}
}

func TestRunScriptImportsFromPrivateValidatedSnapshot(t *testing.T) {
	bashPath, runScript := requireRunScriptTestTools(
		t,
		"bash", "git", "jq", "tar", "sha256sum", "flock", "realpath", "awk",
		"sleep",
	)
	sourceRepo, head := initializeRunScriptGitTarget(t)
	tempRoot := t.TempDir()
	baselineDir := filepath.Join(tempRoot, "baseline")
	if err := os.Mkdir(baselineDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRunScriptJSON(
		t,
		filepath.Join(baselineDir, "manifest.json"),
		runScriptBaselineManifest(
			t,
			head,
			head[:9],
			head,
			head,
			runScriptFakeGoVersion,
		),
	)
	baselineJSONL := filepath.Join(baselineDir, "baseline-explain.jsonl")
	baselineExitCode := filepath.Join(baselineDir, "baseline-explain.exit-code")
	writeRunScriptFile(t, baselineJSONL, runScriptCompletedJSONL)
	writeRunScriptFile(t, baselineExitCode, "0\n")

	ready := filepath.Join(tempRoot, "go-build-ready")
	release := filepath.Join(tempRoot, "go-build-release")
	evidenceRoot := filepath.Join(tempRoot, "evidence")
	runDir := filepath.Join(evidenceRoot, "snapshot")
	command := exec.Command(
		bashPath,
		runScript,
		"--task", "explain",
		"--variant", "baseline",
		"--baseline-from", baselineDir,
		"--run-id", "snapshot",
		"--source", sourceRepo,
		"--commit", head,
		"--prompt-commit", head[:9],
		"--base", head,
		"--worktree", filepath.Join(tempRoot, "target"),
		"--evidence-root", evidenceRoot,
	)
	command.Env = append(
		runScriptTestEnvironment(t, true),
		"RUN_SCRIPT_GO_READY="+ready,
		"RUN_SCRIPT_GO_RELEASE="+release,
		"RUN_SCRIPT_GO_PASSTHROUGH=1",
	)
	var commandOutput bytes.Buffer
	command.Stdout = &commandOutput
	command.Stderr = &commandOutput
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- command.Wait()
	}()
	completed := false
	defer func() {
		if completed {
			return
		}
		_ = os.WriteFile(release, []byte("release\n"), 0o644)
		_ = command.Process.Kill()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
		}
	}()

	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(ready); err == nil {
			break
		}
		select {
		case err := <-done:
			completed = true
			t.Fatalf("run.sh exited before downstream build pause: %v\n%s", err, commandOutput.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatal("timed out waiting for downstream build")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if _, err := os.Stat(runDir); !os.IsNotExist(err) {
		t.Fatalf("final run became visible before terminal publication: %v", err)
	}

	writeRunScriptFile(
		t,
		baselineJSONL,
		runScriptCompletedJSONL+runScriptCompletedJSONL,
	)
	writeRunScriptFile(t, baselineExitCode, "1\n")
	writeRunScriptFile(t, release, "release\n")

	var runError error
	select {
	case runError = <-done:
		completed = true
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for run.sh after releasing downstream build")
	}
	if runError != nil {
		t.Fatalf("run.sh failed after releasing downstream build: %v\n%s", runError, commandOutput.String())
	}
	for path, want := range map[string]string{
		"baseline-explain.jsonl":     runScriptCompletedJSONL,
		"baseline-explain.exit-code": "0\n",
	} {
		content, err := os.ReadFile(filepath.Join(runDir, path))
		if err != nil {
			t.Fatal(err)
		}
		if string(content) != want {
			t.Fatalf("published snapshot %s = %q, want %q", path, content, want)
		}
	}
	if _, err := os.Stat(filepath.Join(runDir, ".baseline-import")); !os.IsNotExist(err) {
		t.Fatalf("private baseline stage was not retired after publication: %v", err)
	}
	if _, err := os.Stat(filepath.Join(runDir, ".source-snapshot")); !os.IsNotExist(err) {
		t.Fatalf("private source stage was published: %v", err)
	}
	assertRunCompletionMarker(t, runDir, "success", 0)
}

func TestRunScriptCleansPrivateStageOnSetupFailure(t *testing.T) {
	bashPath, runScript := requireRunScriptTestTools(
		t,
		"bash", "git", "jq", "tar", "sha256sum", "flock", "realpath", "awk",
	)
	sourceRepo, target := initializeRunScriptGitTarget(t)
	tempRoot := t.TempDir()
	evidenceRoot := filepath.Join(tempRoot, "evidence")
	command := exec.Command(
		bashPath,
		runScript,
		"--task", "explain",
		"--variant", "baseline",
		"--run-id", "setup-failed",
		"--source", sourceRepo,
		"--commit", target,
		"--base", target,
		"--worktree", filepath.Join(tempRoot, "target"),
		"--evidence-root", evidenceRoot,
	)
	command.Env = runScriptTestEnvironment(t, true)
	output, err := command.CombinedOutput()
	assertRunScriptExit(t, err, 99, output)
	assertRunNotPublished(t, evidenceRoot, "setup-failed")
	assertNoRunScriptPartialDirectories(t, evidenceRoot, "setup-failed")
}

func TestRunScriptPublishesOnlyValidatedCodexTranscripts(t *testing.T) {
	bashPath, runScript := requireRunScriptTestTools(
		t,
		"bash", "git", "go", "jq", "tar", "sha256sum", "flock", "realpath",
		"awk", "date", "mktemp", "uname",
	)
	sourceRepo, target := initializeRunScriptGitTarget(t)
	for _, test := range []struct {
		name        string
		mode        string
		wantExit    int
		wantOutcome string
	}{
		{
			name:        "complete",
			mode:        "completed",
			wantOutcome: "success",
		},
		{
			name:        "incomplete",
			mode:        "incomplete",
			wantExit:    1,
			wantOutcome: "failed",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			tempRoot := t.TempDir()
			evidenceRoot := filepath.Join(tempRoot, "evidence")
			runDir := filepath.Join(evidenceRoot, test.name)
			command := exec.Command(
				bashPath,
				runScript,
				"--task", "explain",
				"--variant", "baseline",
				"--run-id", test.name,
				"--source", sourceRepo,
				"--commit", target,
				"--base", target,
				"--worktree", filepath.Join(tempRoot, "target"),
				"--evidence-root", evidenceRoot,
			)
			command.Env = runScriptTestEnvironment(t, false)
			output, err := command.CombinedOutput()
			if test.wantExit == 0 {
				if err != nil {
					t.Fatalf("complete run failed: %v\n%s", err, output)
				}
			} else {
				assertRunScriptExit(t, err, test.wantExit, output)
			}
			if test.mode == "incomplete" {
				if !strings.Contains(
					string(output),
					"Codex JSONL lifecycle or exit code is invalid for "+
						"baseline-explain:",
				) {
					t.Fatalf("incomplete-transcript diagnostic = %q", output)
				}
				assertRunNotPublished(t, evidenceRoot, test.name)
				assertNoRunScriptPartialDirectories(
					t,
					evidenceRoot,
					test.name,
				)
				return
			}
			assertRunCompletionMarker(
				t,
				runDir,
				test.wantOutcome,
				test.wantExit,
			)
			exitCode, err := os.ReadFile(
				filepath.Join(runDir, "baseline-explain.exit-code"),
			)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.TrimSpace(string(exitCode)); got != strconv.Itoa(test.wantExit) {
				t.Fatalf("validated exit code = %q, want %d", got, test.wantExit)
			}
		})
	}
}

func TestRunScriptIsolatesAndPinsCodexGeneration(t *testing.T) {
	bashPath, runScript := requireRunScriptTestTools(
		t,
		"bash", "git", "go", "jq", "tar", "sha256sum", "flock", "realpath",
		"awk", "date", "mktemp", "uname",
	)
	sourceRepo, head := initializeRunScriptGitTarget(t)
	tempRoot := t.TempDir()
	originalCodexHome := filepath.Join(tempRoot, "ambient-codex-home")
	if err := os.Mkdir(originalCodexHome, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRunScriptFile(
		t,
		filepath.Join(originalCodexHome, "auth.json"),
		"{\"token\":\"test-only\"}\n",
	)
	writeRunScriptFile(
		t,
		filepath.Join(originalCodexHome, "config.toml"),
		"model = \"ambient-model\"\n",
	)
	evidenceRoot := filepath.Join(tempRoot, "evidence")
	runDir := filepath.Join(evidenceRoot, "isolated-codex")
	observation := filepath.Join(runDir, "codex-observation.txt")
	capturedPrompt := filepath.Join(runDir, "codex-prompt.txt")
	command := exec.Command(
		bashPath,
		runScript,
		"--task", "explain",
		"--variant", "baseline",
		"--run-id", "isolated-codex",
		"--source", sourceRepo,
		"--commit", head,
		"--base", "main",
		"--worktree", filepath.Join(tempRoot, "target"),
		"--evidence-root", evidenceRoot,
	)
	command.Env = append(
		runScriptTestEnvironment(t, false),
		"CODEX_HOME="+originalCodexHome,
		"OPENAI_API_KEY=hostile-openai-key",
		"CODEX_API_KEY=hostile-codex-key",
		"HTTPS_PROXY=http://hostile.invalid",
		"RUST_LOG=trace",
		"GOENV="+filepath.Join(tempRoot, "hostile-goenv"),
		"GOTOOLCHAIN=go0.0.0+auto",
		"GOOS=plan9",
		"GOARCH=386",
		"CGO_ENABLED=1",
		"GOPROXY=http://hostile.invalid",
		"GIT_DIR="+filepath.Join(tempRoot, "ambient-git-dir"),
		"GIT_WORK_TREE="+filepath.Join(tempRoot, "ambient-work-tree"),
		"GIT_INDEX_FILE="+filepath.Join(tempRoot, "ambient-index"),
		"GIT_OBJECT_DIRECTORY="+filepath.Join(tempRoot, "ambient-objects"),
		"GIT_ALTERNATE_OBJECT_DIRECTORIES="+filepath.Join(tempRoot, "ambient-alternates"),
		"GIT_COMMON_DIR="+filepath.Join(tempRoot, "ambient-common"),
		"GIT_EXEC_PATH="+filepath.Join(tempRoot, "ambient-exec"),
		"GIT_EXTERNAL_DIFF="+filepath.Join(tempRoot, "ambient-diff"),
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("isolated Codex run failed: %v\n%s", err, output)
	}
	observedContent, err := os.ReadFile(observation)
	if err != nil {
		t.Fatal(err)
	}
	observed := string(observedContent)
	for _, requirement := range []string{
		"auth_link=true",
		"git_dir=unset",
		"git_work_tree=unset",
		"git_index_file=unset",
		"git_object_directory=unset",
		"git_alternate_object_directories=unset",
		"git_common_dir=unset",
		"git_exec_path=unset",
		"git_external_diff=unset",
		"git_ssh=unset",
		"git_ssh_command=unset",
		"git_ssh_variant=unset",
		"git_askpass=unset",
		"ssh_askpass=unset",
		"git_proxy_command=unset",
		"git_namespace=unset",
		"git_replace_ref_base=unset",
		"git_ceiling_directories=unset",
		"git_discovery_across_filesystem=0",
		"git_no_replace_objects=1",
		"git_optional_locks=0",
		"git_config_nosystem=1",
		"git_config_global=/dev/null",
		"git_attr_nosystem=1",
		"git_config_count=10",
		"gowork=off",
		"goflags=-mod=readonly -trimpath -buildvcs=false",
		"goenv=off",
		"gotoolchain=local",
		"goos=unset",
		"goarch=unset",
		"cgo_enabled=unset",
		"goproxy=https://proxy.golang.org,direct",
		"openai_api_key=unset",
		"codex_api_key=unset",
		"https_proxy=unset",
		"rust_log=unset",
		"arg=--ignore-user-config",
		"arg=--ignore-rules",
		`arg=default_permissions="benchmark"`,
		`arg=shell_environment_policy.inherit="none"`,
		"arg=shell_environment_policy.ignore_default_excludes=false",
		"arg=shell_environment_policy.experimental_use_profile=false",
		"arg=project_doc_max_bytes=0",
		"arg=project_doc_fallback_filenames=[]",
		"arg=mcp_servers={}",
		"arg=apps._default.enabled=false",
		"arg=--disable",
		"arg=hooks",
		"arg=tool_router",
		"arg=workflows",
		"arg=code_mode",
		"arg=code_mode_host",
		"arg=code_mode_only",
	} {
		if !strings.Contains(observed, requirement+"\n") {
			t.Errorf("Codex observation missing %q:\n%s", requirement, observed)
		}
	}
	canonicalAuth, err := filepath.EvalSymlinks(
		filepath.Join(originalCodexHome, "auth.json"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(
		observed,
		`arg=permissions.benchmark={extends=":read-only", filesystem={`,
	) ||
		!strings.Contains(observed, `":root"="deny"`) ||
		!strings.Contains(observed, `":minimal"="read"`) ||
		!strings.Contains(observed, canonicalAuth+`"="deny"`) ||
		!strings.Contains(observed, "arg=shell_environment_policy.set={") {
		t.Errorf("Codex permission/environment policy is incomplete:\n%s", observed)
	}
	for _, goSetting := range []string{
		"GOROOT=",
		"GOPATH=",
		"GOMODCACHE=",
		"GOCACHE=",
		`GOENV="off"`,
		`GOTOOLCHAIN="local"`,
	} {
		if !strings.Contains(observed, goSetting) {
			t.Errorf("Codex shell environment missing %q:\n%s", goSetting, observed)
		}
	}
	if strings.Contains(observed, "arg=-m\n") ||
		strings.Contains(observed, "arg=test-model\n") ||
		strings.Contains(observed, "model_reasoning_effort") {
		t.Errorf("default Codex invocation configured a model:\n%s", observed)
	}
	if strings.Contains(observed, "codex_home="+originalCodexHome+"\n") ||
		!strings.Contains(observed, "/.isolated-codex.partial.") ||
		!strings.Contains(observed, "/.codex-home\n") {
		t.Errorf("Codex did not use a private staged home:\n%s", observed)
	}
	if _, err := os.Lstat(filepath.Join(runDir, ".codex-home")); !os.IsNotExist(err) {
		t.Fatalf("private Codex home was published: %v", err)
	}
	promptContent, err := os.ReadFile(capturedPrompt)
	if err != nil {
		t.Fatal(err)
	}
	wantPrompt := runScriptRenderedPrompt(t, "explain", head[:9], head)
	if string(promptContent) != wantPrompt {
		t.Fatalf(
			"rendered prompt did not use canonical identities:\ngot:\n%s\nwant:\n%s",
			promptContent,
			wantPrompt,
		)
	}

	manifestContent, err := os.ReadFile(filepath.Join(runDir, "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err := json.Unmarshal(manifestContent, &manifest); err != nil {
		t.Fatal(err)
	}
	expectedGenerationConfig := runScriptGenerationConfig(t, manifest)
	expectedGenerationDigest := sha256.Sum256(expectedGenerationConfig)
	if manifest["model"] != "router-selected" ||
		manifest["model_mode"] != "router" ||
		manifest["model_configuration"] != "none" ||
		manifest["codex_version"] != "codex-test 1" ||
		manifest["generation_isolation"] !=
			runScriptGenerationIsolation ||
		manifest["generation_config_sha256"] !=
			fmt.Sprintf("%x", expectedGenerationDigest) ||
		manifest["go_version"] != runScriptRealGoVersion(t) {
		t.Fatalf("generation pins missing from manifest: %#v", manifest)
	}
	generationConfig, err := os.ReadFile(
		filepath.Join(runDir, "generation-config.json"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(generationConfig, expectedGenerationConfig) {
		t.Fatalf(
			"normalized generation config differs from test contract:\n%s",
			generationConfig,
		)
	}
	generationConfigDigest := sha256.Sum256(
		generationConfig,
	)
	if fmt.Sprintf("%x", generationConfigDigest) !=
		manifest["generation_config_sha256"] {
		t.Fatalf("normalized generation config digest mismatch: %s", generationConfig)
	}
	if bytes.Contains(generationConfig, []byte(tempRoot)) ||
		bytes.Contains(generationConfig, []byte(originalCodexHome)) {
		t.Fatalf("generation config contains machine-local paths: %s", generationConfig)
	}
	promptDigests, ok := manifest["prompt_digests"].(map[string]any)
	if !ok || len(promptDigests) != 1 {
		t.Fatalf("selected-task prompt digests missing: %#v", manifest)
	}
	promptDigest := sha256.Sum256(promptContent)
	if promptDigests["explain"] != fmt.Sprintf("%x", promptDigest) {
		t.Fatalf("manifest prompt digest does not bind executed prompt: %#v", promptDigests)
	}
	snapshottedPrompt, err := os.ReadFile(
		filepath.Join(runDir, "prompts", "explain.txt"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(snapshottedPrompt, promptContent) {
		t.Fatalf("rendered prompt snapshot differs from executed prompt")
	}
	profilesSnapshot, err := os.ReadFile(
		filepath.Join(runDir, "profiles-snapshot.tsv"),
	)
	if err != nil {
		t.Fatal(err)
	}
	profilesDigest := sha256.Sum256(profilesSnapshot)
	if manifest["profiles_snapshot_path"] != "profiles-snapshot.tsv" ||
		manifest["profiles_snapshot_sha256"] != fmt.Sprintf("%x", profilesDigest) {
		t.Fatalf("profile snapshot provenance missing: %#v", manifest)
	}
	for _, field := range []string{
		"go_root",
		"go_path",
		"go_mod_cache",
		"go_cache",
	} {
		value, ok := manifest[field].(string)
		if !ok || value == "" {
			t.Fatalf("generation Go path %s missing from manifest: %#v", field, manifest)
		}
	}
	assertRunCompletionMarker(t, runDir, "success", 0)
}

func TestRunScriptRejectsTargetMutationDuringCodexRun(t *testing.T) {
	bashPath, runScript := requireRunScriptTestTools(
		t,
		"bash", "git", "go", "jq", "tar", "sha256sum", "flock", "realpath",
		"awk", "date", "find", "mktemp", "sort", "uname",
	)
	sourceRepo, head := initializeRunScriptGitTarget(t)
	tempRoot := t.TempDir()
	worktree := filepath.Join(tempRoot, "target")
	evidenceRoot := filepath.Join(tempRoot, "evidence")
	command := exec.Command(
		bashPath,
		runScript,
		"--task", "explain",
		"--variant", "baseline",
		"--run-id", "target-race",
		"--source", sourceRepo,
		"--commit", head,
		"--base", head,
		"--worktree", worktree,
		"--evidence-root", evidenceRoot,
	)
	command.Env = runScriptTestEnvironment(t, false)
	output, err := command.CombinedOutput()
	assertRunScriptExit(t, err, 1, output)
	if !strings.Contains(string(output), "experiment worktree is dirty:") {
		t.Fatalf("target-race diagnostic = %q", output)
	}
	assertRunNotPublished(t, evidenceRoot, "target-race")
	assertNoRunScriptPartialDirectories(t, evidenceRoot, "target-race")
}

func TestRunScriptUsesExternalCacheForOptimizedSnapshot(t *testing.T) {
	bashPath, runScript := requireRunScriptTestTools(
		t,
		"bash", "git", "go", "jq", "tar", "sha256sum", "flock", "realpath",
		"awk", "date", "find", "mktemp", "sort", "uname",
	)
	sourceRepo, head := initializeRunScriptGitTarget(t)
	tempRoot := t.TempDir()
	evidenceRoot := filepath.Join(tempRoot, "evidence")
	runDir := filepath.Join(evidenceRoot, "optimized-cache")
	command := exec.Command(
		bashPath,
		runScript,
		"--task", "deep-explain",
		"--variant", "all",
		"--profile", "guarded-high",
		"--run-id", "optimized-cache",
		"--source", sourceRepo,
		"--commit", head,
		"--base", head,
		"--worktree", filepath.Join(tempRoot, "target"),
		"--evidence-root", evidenceRoot,
	)
	command.Env = append(
		runScriptTestEnvironment(t, false),
		"GOENV="+filepath.Join(tempRoot, "hostile-goenv"),
		"GOTOOLCHAIN=go0.0.0+auto",
		"GOOS=plan9",
		"GOARCH=386",
		"CGO_ENABLED=1",
		"GOPROXY=http://hostile.invalid",
		"GIT_CONFIG='malformed",
		"GIT_CONFIG_PARAMETERS='malformed",
		"GIT_DIR="+filepath.Join(tempRoot, "ambient-git-dir"),
		"GIT_WORK_TREE="+filepath.Join(tempRoot, "ambient-work-tree"),
		"GIT_INDEX_FILE="+filepath.Join(tempRoot, "ambient-index"),
		"GIT_OBJECT_DIRECTORY="+filepath.Join(tempRoot, "ambient-objects"),
		"GIT_ALTERNATE_OBJECT_DIRECTORIES="+filepath.Join(tempRoot, "ambient-alternates"),
		"GIT_COMMON_DIR="+filepath.Join(tempRoot, "ambient-common"),
		"GIT_EXEC_PATH="+filepath.Join(tempRoot, "ambient-exec"),
		"GIT_EXTERNAL_DIFF="+filepath.Join(tempRoot, "ambient-diff"),
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("optimized snapshot run failed: %v\n%s", err, output)
	}
	invocation, err := os.ReadFile(
		filepath.Join(runDir, "optimized-guarded-high-deep-explain.invocation"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(invocation, []byte("REPO_VIEW_CACHE_DIR=")) {
		t.Fatalf("optimized invocation has no external cache:\n%s", invocation)
	}
	capturedPrompt, err := os.ReadFile(filepath.Join(runDir, "codex-prompt.txt"))
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		"MANDATORY experiment navigation protocol",
		"repo-view changed --root . --base " + head,
		"Do not use git, rg, grep, sed, cat, nl, head, tail, find, ls",
		"Do not run repo-view --help or any subcommand --help",
		"Every find, inspect, and outline command must include --root .",
		"Issue exactly one repo-view command at a time",
		"Positional inputs to repo-view find must be identifiers",
		"use --include scope on inspect; do not use --include all",
		"Keep follow-up --context at 12 or less",
		"Before answering this reservation task, explicitly close these rubric points",
		"RealTimeSource.Now returns time.Now().UTC()",
		"ReserveN can retain a caller-provided monotonic timestamp",
		"The direct production path must exercise the changed Reserve or ReserveN result",
		"successful immediate and delayed Wait with event-clock advancement",
		"missing successful delayed ReserveN case",
		"repository-wide wall-versus-monotonic coverage gap",
		"Preserve the documented operational controls' zero-failure",
		"For the dynamic path, use service/worker/pernamespaceworker.go",
		"newShardReaderRateLimiter directly passes NewReaderPriorityRateLimiter",
		"Do not cite MultiRateLimiterImpl.Wait as the MultiRequestRateLimiterImpl implementation",
		"Cite the Reservation interface from common/quotas/reservation.go",
		"repo-view find ClockedReservation --root . --include refs --return locations",
		"Do not combine it with other symbols",
		"directly inspect the TestClockedRateLimiter_WaitN_NoRecycle scope",
		"Use exactly eight repo-view commands and at most one dependency-source shell command",
		"The eight repository commands are",
		"After the eight repository calls, use at most one bounded awk command",
		"For command 6, directly inspect service/worker/scheduler/fx.go:120",
		"For command 7, directly inspect service/history/queues/reader_quotas.go:14",
		"common/quotas/multi_request_rate_limiter_impl.go:17, :56, and :70",
		"common/quotas/rate_limiter_impl_test.go:23",
		"Use the exact labels \"Measured results\" and \"Inferred downstream benefit\"",
		"the Reservation interface does not promise a particular zero-argument clock source",
		"Scope negative performance-evidence statements to the inspected changed documentation",
		"Report node health exactly as documented: all three nodes remained up",
		"Do not call that UP/NORMAL",
		"RateLimiterImpl.Reserve and ReserveN are declared to return the Reservation interface",
		"runtime dynamic type through a type assertion, reflection, or equivalent behavior",
	} {
		if !bytes.Contains(capturedPrompt, []byte(required)) {
			t.Errorf("optimized prompt missing %q:\n%s", required, capturedPrompt)
		}
	}
	if bytes.Contains(capturedPrompt, []byte(
		"complete navigation budget for this simple task",
	)) {
		t.Fatalf("deep prompt contains simple-task guard:\n%s", capturedPrompt)
	}
	for _, contract := range []string{
		"REPO_VIEW_REQUIRED_ROOT=",
		"REPO_VIEW_REQUIRED_BASE_COMMIT=",
		"REPO_VIEW_REQUIRED_CHANGED_RETURN=",
		"REPO_VIEW_REQUIRED_CHANGED_CONTEXT=",
		"REPO_VIEW_REQUIRE_NAVIGATION_SEMANTICS=1",
	} {
		if !bytes.Contains(invocation, []byte(contract)) {
			t.Fatalf(
				"optimized invocation missing %s:\n%s",
				contract,
				invocation,
			)
		}
	}
	for _, manifestPath := range []string{
		"manifest.json",
		"generation-config.json",
	} {
		content, err := os.ReadFile(filepath.Join(runDir, manifestPath))
		if err != nil {
			t.Fatal(err)
		}
		var metadata map[string]any
		if err := json.Unmarshal(content, &metadata); err != nil {
			t.Fatal(err)
		}
		if metadata["mechanical_navigation_semantics_enforced"] != true {
			t.Fatalf(
				"%s lacks enforced navigation marker: %#v",
				manifestPath,
				metadata,
			)
		}
	}
	for _, internalPath := range []string{
		".repo-view-cache",
		".source-snapshot",
		".source-verifier.git",
		".codex-home",
		".shell-home",
	} {
		if _, err := os.Lstat(filepath.Join(runDir, internalPath)); !os.IsNotExist(err) {
			t.Errorf("private runtime path was published (%s): %v", internalPath, err)
		}
	}
	if _, err := os.Stat(
		filepath.Join(runDir, "changed-packet-guarded-high.json"),
	); err != nil {
		t.Fatal(err)
	}
	assertRunCompletionMarker(t, runDir, "success", 0)
}

func TestRunScriptImportsCompatibleCompleteBaseline(t *testing.T) {
	bashPath, runScript := requireRunScriptTestTools(
		t,
		"bash", "git", "go", "jq", "tar", "sha256sum", "flock", "realpath",
		"awk", "date", "mktemp", "uname",
	)
	sourceRepo, head := initializeRunScriptGitTarget(t)
	tempRoot := t.TempDir()
	baselineDir := filepath.Join(tempRoot, "baseline")
	if err := os.Mkdir(baselineDir, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRunScriptJSON(
		t,
		filepath.Join(baselineDir, "manifest.json"),
		runScriptBaselineManifest(
			t,
			head,
			head[:9],
			head,
			head,
			runScriptRealGoVersion(t),
		),
	)
	writeRunScriptFile(
		t,
		filepath.Join(baselineDir, "baseline-explain.jsonl"),
		runScriptCompletedJSONL,
	)
	writeRunScriptFile(
		t,
		filepath.Join(baselineDir, "baseline-explain.exit-code"),
		"0\n",
	)
	writeRunScriptFile(
		t,
		filepath.Join(baselineDir, "baseline-explain.stderr"),
		"preserved diagnostic\n",
	)

	evidenceRoot := filepath.Join(tempRoot, "evidence")
	runDir := filepath.Join(evidenceRoot, "compatible")
	command := exec.Command(
		bashPath,
		runScript,
		"--task", "explain",
		"--variant", "baseline",
		"--baseline-from", baselineDir,
		"--run-id", "compatible",
		"--source", sourceRepo,
		"--commit", head,
		"--prompt-commit", head[:9],
		"--base", head,
		"--worktree", filepath.Join(tempRoot, "target"),
		"--evidence-root", evidenceRoot,
	)
	command.Env = runScriptTestEnvironment(t, false)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("compatible baseline import failed: %v\n%s", err, output)
	}
	for path, want := range map[string]string{
		"baseline-explain.jsonl":     runScriptCompletedJSONL,
		"baseline-explain.exit-code": "0\n",
		"baseline-explain.stderr":    "preserved diagnostic\n",
	} {
		content, err := os.ReadFile(filepath.Join(runDir, path))
		if err != nil {
			t.Fatal(err)
		}
		if string(content) != want {
			t.Fatalf("%s = %q, want %q", path, content, want)
		}
	}
	importedGenerationConfig, err := os.ReadFile(
		filepath.Join(runDir, "baseline-source-generation-config.json"),
	)
	if err != nil {
		t.Fatal(err)
	}
	currentGenerationConfig, err := os.ReadFile(
		filepath.Join(runDir, "generation-config.json"),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(importedGenerationConfig, currentGenerationConfig) {
		t.Fatalf(
			"imported generation config changed:\n%s",
			importedGenerationConfig,
		)
	}
	assertRunCompletionMarker(t, runDir, "success", 0)
	checksumCommand := exec.Command("sha256sum", "-c", "source-SHA256SUMS")
	checksumCommand.Dir = runDir
	if output, err := checksumCommand.CombinedOutput(); err != nil {
		t.Fatalf("source checksum validation failed: %v\n%s", err, output)
	}
	archiveOutput, err := exec.Command(
		"tar", "-tzf", filepath.Join(runDir, "repo-view-source.tar.gz"),
	).CombinedOutput()
	if err != nil {
		t.Fatalf("list source archive: %v\n%s", err, archiveOutput)
	}
	if bytes.Contains(archiveOutput, []byte(".partial.")) ||
		bytes.Contains(archiveOutput, []byte("repo-view.bin")) {
		t.Fatalf("source archive contains generated run artifacts:\n%s", archiveOutput)
	}
}

func requireRunScriptTestTools(t *testing.T, tools ...string) (string, string) {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("run.sh integration requires a Unix shell environment")
	}
	var bashPath string
	for _, tool := range tools {
		path, err := exec.LookPath(tool)
		if err != nil {
			t.Skipf("run.sh integration requires %s: %v", tool, err)
		}
		if tool == "bash" {
			bashPath = path
		}
	}
	runScript, err := filepath.Abs(filepath.Join(
		"..", "..", "experiments", "lsp-replacement", "run.sh",
	))
	if err != nil {
		t.Fatal(err)
	}
	return bashPath, runScript
}

func runScriptTestEnvironment(t *testing.T, fakeGo bool) []string {
	t.Helper()
	fakeBin := t.TempDir()
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	expectedGoVersion := runScriptRealGoVersion(t)
	writeRunScriptFile(t, filepath.Join(fakeBin, "codex"), `#!/bin/sh
if [ "${1-}" = "--version" ]; then
  if [ -n "${RUN_SCRIPT_CODEX_VERSION_READY-}" ]; then
    : > "$RUN_SCRIPT_CODEX_VERSION_READY"
    while [ ! -e "$RUN_SCRIPT_CODEX_VERSION_RELEASE" ]; do
      sleep 0.01
    done
  fi
  printf '%s\n' 'codex-test 1'
  exit 0
fi
if [ -n "${CODEX_HOME-}" ]; then
  {
    printf 'codex_home=%s\n' "${CODEX_HOME-unset}"
    if [ -L "${CODEX_HOME-unset}/auth.json" ]; then
      printf '%s\n' 'auth_link=true'
    else
      printf '%s\n' 'auth_link=false'
    fi
    printf 'git_dir=%s\n' "${GIT_DIR-unset}"
    printf 'git_work_tree=%s\n' "${GIT_WORK_TREE-unset}"
    printf 'git_index_file=%s\n' "${GIT_INDEX_FILE-unset}"
    printf 'git_object_directory=%s\n' "${GIT_OBJECT_DIRECTORY-unset}"
    printf 'git_alternate_object_directories=%s\n' "${GIT_ALTERNATE_OBJECT_DIRECTORIES-unset}"
    printf 'git_common_dir=%s\n' "${GIT_COMMON_DIR-unset}"
    printf 'git_exec_path=%s\n' "${GIT_EXEC_PATH-unset}"
    printf 'git_external_diff=%s\n' "${GIT_EXTERNAL_DIFF-unset}"
    printf 'git_ssh=%s\n' "${GIT_SSH-unset}"
    printf 'git_ssh_command=%s\n' "${GIT_SSH_COMMAND-unset}"
    printf 'git_ssh_variant=%s\n' "${GIT_SSH_VARIANT-unset}"
    printf 'git_askpass=%s\n' "${GIT_ASKPASS-unset}"
    printf 'ssh_askpass=%s\n' "${SSH_ASKPASS-unset}"
    printf 'git_proxy_command=%s\n' "${GIT_PROXY_COMMAND-unset}"
    printf 'git_namespace=%s\n' "${GIT_NAMESPACE-unset}"
    printf 'git_replace_ref_base=%s\n' "${GIT_REPLACE_REF_BASE-unset}"
    printf 'git_ceiling_directories=%s\n' "${GIT_CEILING_DIRECTORIES-unset}"
    printf 'git_discovery_across_filesystem=%s\n' "${GIT_DISCOVERY_ACROSS_FILESYSTEM-unset}"
    printf 'git_no_replace_objects=%s\n' "${GIT_NO_REPLACE_OBJECTS-unset}"
    printf 'git_optional_locks=%s\n' "${GIT_OPTIONAL_LOCKS-unset}"
    printf 'git_config_nosystem=%s\n' "${GIT_CONFIG_NOSYSTEM-unset}"
    printf 'git_config_global=%s\n' "${GIT_CONFIG_GLOBAL-unset}"
    printf 'git_attr_nosystem=%s\n' "${GIT_ATTR_NOSYSTEM-unset}"
    printf 'git_config_count=%s\n' "${GIT_CONFIG_COUNT-unset}"
    printf 'gowork=%s\n' "${GOWORK-unset}"
    printf 'goflags=%s\n' "${GOFLAGS-unset}"
    printf 'goenv=%s\n' "${GOENV-unset}"
    printf 'gotoolchain=%s\n' "${GOTOOLCHAIN-unset}"
    printf 'goos=%s\n' "${GOOS-unset}"
    printf 'goarch=%s\n' "${GOARCH-unset}"
    printf 'cgo_enabled=%s\n' "${CGO_ENABLED-unset}"
    printf 'goproxy=%s\n' "${GOPROXY-unset}"
    printf 'openai_api_key=%s\n' "${OPENAI_API_KEY-unset}"
    printf 'codex_api_key=%s\n' "${CODEX_API_KEY-unset}"
    printf 'https_proxy=%s\n' "${HTTPS_PROXY-unset}"
    printf 'rust_log=%s\n' "${RUST_LOG-unset}"
    for argument in "$@"; do
      printf 'arg=%s\n' "$argument"
    done
  } > "${CODEX_HOME}/../codex-observation.txt"
  last_argument=
  worktree=
  previous_argument=
  for argument in "$@"; do
    last_argument=$argument
    if [ "$previous_argument" = "-C" ]; then
      worktree=$argument
    fi
    previous_argument=$argument
  done
  printf '%s' "$last_argument" > "${CODEX_HOME}/../codex-prompt.txt"
fi
case "${CODEX_HOME-}" in
  *.target-race.partial.*)
    printf '%s\n' 'mutated during Codex run' > "${worktree}/fixture.txt"
    ;;
esac
case "${CODEX_HOME-}" in
  *.incomplete.partial.*) run_script_codex_mode=incomplete ;;
  *) run_script_codex_mode=completed ;;
esac
case "$run_script_codex_mode" in
  completed)
    printf '%s\n' '{"type":"thread.started","thread_id":"test"}'
    printf '%s\n' '{"type":"turn.started"}'
    printf '%s\n' '{"type":"turn.completed","usage":{"input_tokens":1,"cached_input_tokens":0,"output_tokens":1,"reasoning_output_tokens":0}}'
    exit 0
    ;;
  incomplete)
    printf '%s\n' '{"type":"item.completed","item":{"type":"agent_message","text":"incomplete"}}'
    exit 0
    ;;
esac
exit 1
	`)
	if fakeGo {
		expectedGoVersion = runScriptFakeGoVersion
		writeRunScriptFile(t, filepath.Join(fakeBin, "go"), `#!/bin/sh
if [ "${1-}" = "version" ]; then
  printf '%s\n' 'go version go1.26.5 test'
  exit 0
fi
if [ "${1-}" = "env" ]; then
  exec "$RUN_SCRIPT_REAL_GO" "$@"
fi
if [ "${1-}" = "build" ] && [ -n "${RUN_SCRIPT_GO_READY-}" ]; then
  : > "$RUN_SCRIPT_GO_READY"
  while [ ! -e "$RUN_SCRIPT_GO_RELEASE" ]; do
    sleep 0.01
  done
fi
if [ "${RUN_SCRIPT_GO_PASSTHROUGH-}" = "1" ]; then
  exec "$RUN_SCRIPT_REAL_GO" "$@"
fi
exit 99
`)
	}
	for _, path := range []string{filepath.Join(fakeBin, "codex")} {
		if err := os.Chmod(path, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if fakeGo {
		if err := os.Chmod(filepath.Join(fakeBin, "go"), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	environment := make([]string, 0, len(os.Environ())+2)
	goTempDir := os.Getenv("GOTMPDIR")
	for _, variable := range os.Environ() {
		if strings.HasPrefix(variable, "PATH=") ||
			strings.HasPrefix(variable, "RUN_SCRIPT_REAL_GO=") ||
			strings.HasPrefix(variable, "RUN_SCRIPT_CODEX_MODE=") ||
			strings.HasPrefix(variable, "RUN_SCRIPT_CODEX_OBSERVATION=") ||
			strings.HasPrefix(variable, "RUN_SCRIPT_CODEX_PROMPT=") ||
			strings.HasPrefix(variable, "RUN_SCRIPT_MUTATE_TARGET=") ||
			strings.HasPrefix(variable, "RUN_SCRIPT_CODEX_VERSION_") ||
			strings.HasPrefix(variable, "RUN_SCRIPT_GO_") ||
			strings.HasPrefix(variable, "RUN_SCRIPT_TAR_") ||
			strings.HasPrefix(variable, "LSP_MODEL=") ||
			strings.HasPrefix(variable, "LSP_MODEL_MODE=") ||
			strings.HasPrefix(variable, "LSP_CODEX_VERSION=") ||
			strings.HasPrefix(variable, "LSP_GO_VERSION=") ||
			strings.HasPrefix(variable, "CODEX_HOME=") ||
			strings.HasPrefix(variable, "GOFLAGS=") ||
			strings.HasPrefix(variable, "GOWORK=") ||
			(goTempDir != "" && strings.HasPrefix(variable, "TMPDIR=")) {
			continue
		}
		environment = append(environment, variable)
	}
	environment = append(
		environment,
		"PATH="+fakeBin+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GOFLAGS=-buildvcs=false",
		"GOWORK=off",
		"LSP_MODEL=test-model",
		"LSP_CODEX_VERSION=codex-test 1",
		"LSP_GO_VERSION="+expectedGoVersion,
	)
	if fakeGo {
		environment = append(
			environment,
			"RUN_SCRIPT_REAL_GO="+realGo,
		)
	}
	if goTempDir != "" {
		environment = append(environment, "TMPDIR="+goTempDir)
	}
	return environment
}

func runScriptTarPauseEnvironment(
	t *testing.T,
	environment []string,
	ready, release string,
) []string {
	t.Helper()
	realTar, err := exec.LookPath("tar")
	if err != nil {
		t.Fatal(err)
	}
	fakeBin := t.TempDir()
	once := ready + ".once"
	tarPath := filepath.Join(fakeBin, "tar")
	writeRunScriptFile(t, tarPath, `#!/bin/sh
set -eu
if [ ! -e "$RUN_SCRIPT_TAR_ONCE" ]; then
  "$RUN_SCRIPT_REAL_TAR" "$@"
  : > "$RUN_SCRIPT_TAR_ONCE"
  : > "$RUN_SCRIPT_TAR_READY"
  while [ ! -e "$RUN_SCRIPT_TAR_RELEASE" ]; do
    sleep 0.01
  done
  exit 0
fi
exec "$RUN_SCRIPT_REAL_TAR" "$@"
`)
	if err := os.Chmod(tarPath, 0o755); err != nil {
		t.Fatal(err)
	}
	result := make([]string, 0, len(environment)+4)
	for _, variable := range environment {
		if strings.HasPrefix(variable, "PATH=") {
			result = append(
				result,
				"PATH="+fakeBin+string(os.PathListSeparator)+
					strings.TrimPrefix(variable, "PATH="),
			)
			continue
		}
		result = append(result, variable)
	}
	return append(
		result,
		"RUN_SCRIPT_REAL_TAR="+realTar,
		"RUN_SCRIPT_TAR_ONCE="+once,
		"RUN_SCRIPT_TAR_READY="+ready,
		"RUN_SCRIPT_TAR_RELEASE="+release,
	)
}

func waitForRunScriptPath(
	t *testing.T,
	path string,
	done <-chan error,
	completed *bool,
	output *bytes.Buffer,
) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, err := os.Stat(path); err == nil {
			return
		}
		select {
		case err := <-done:
			*completed = true
			t.Fatalf("run.sh exited before creating %s: %v\n%s", path, err, output.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("timed out waiting for %s", path)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func assertRunNotPublished(t *testing.T, evidenceRoot, runID string) {
	t.Helper()
	if _, err := os.Lstat(filepath.Join(evidenceRoot, runID)); !os.IsNotExist(err) {
		t.Fatalf("final run was published: %v", err)
	}
}

func assertNoRunScriptPartialDirectories(t *testing.T, evidenceRoot, runID string) {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(evidenceRoot, "."+runID+".partial.*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 0 {
		t.Fatalf("private run stages remain after failure: %v", matches)
	}
}

func assertRunCompletionMarker(
	t *testing.T,
	runDir, wantOutcome string,
	wantExit int,
) {
	t.Helper()
	content, err := os.ReadFile(filepath.Join(runDir, "run-complete.json"))
	if err != nil {
		t.Fatal(err)
	}
	var marker struct {
		State    string `json:"state"`
		Outcome  string `json:"outcome"`
		ExitCode int    `json:"exit_code"`
	}
	if err := json.Unmarshal(content, &marker); err != nil {
		t.Fatal(err)
	}
	if marker.State != "complete" ||
		marker.Outcome != wantOutcome ||
		marker.ExitCode != wantExit {
		t.Fatalf(
			"run completion marker = %+v, want state=complete outcome=%s exit=%d",
			marker,
			wantOutcome,
			wantExit,
		)
	}
}

const (
	runScriptFakeGoVersion       = "go version go1.26.5 test"
	runScriptGenerationIsolation = "root-deny-explicit-read-inherit-none-" +
		"go-env-v3"
)

func runScriptRealGoVersion(t *testing.T) string {
	t.Helper()
	output, err := exec.Command("go", "version").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(output))
}

func runScriptManifestStringMap(
	t *testing.T,
	manifest map[string]any,
	field string,
) map[string]string {
	t.Helper()
	result := make(map[string]string)
	switch values := manifest[field].(type) {
	case map[string]string:
		for key, value := range values {
			result[key] = value
		}
	case map[string]any:
		for key, raw := range values {
			value, ok := raw.(string)
			if !ok {
				t.Fatalf("manifest %s[%s] is not a string: %#v", field, key, raw)
			}
			result[key] = value
		}
	default:
		t.Fatalf("manifest %s is not a string map: %#v", field, manifest[field])
	}
	return result
}

func runScriptGenerationConfig(
	t *testing.T,
	manifest map[string]any,
) []byte {
	t.Helper()
	profilesSnapshotSHA256, ok := manifest["profiles_snapshot_sha256"].(string)
	if !ok || profilesSnapshotSHA256 == "" {
		t.Fatalf("manifest profile snapshot digest is missing: %#v", manifest)
	}
	mechanical, ok := manifest["mechanical_navigation_semantics_enforced"].(bool)
	if !ok {
		t.Fatalf("manifest mechanical marker is missing: %#v", manifest)
	}
	config := map[string]any{
		"auth_source_permission": "deny-if-present",
		"baseline_developer_instructions": "Do not call collaboration, " +
			"subagent, spawn-agent, or agent-wait tools. Do not read or " +
			"invoke Codex skills, plugins, hooks, or marketplace resources; " +
			"they are outside this benchmark.",
		"feature_flags": []string{
			"--disable", "multi_agent",
			"--disable", "multi_agent_v2",
			"--disable", "enable_fanout",
			"--disable", "collaboration_modes",
			"--disable", "hooks",
			"--disable", "tool_router",
			"--disable", "workflows",
			"--disable", "code_mode",
			"--disable", "code_mode_host",
			"--disable", "code_mode_only",
		},
		"generation_isolation":                     runScriptGenerationIsolation,
		"profiles_snapshot_path":                   "profiles-snapshot.tsv",
		"profiles_snapshot_sha256":                 profilesSnapshotSHA256,
		"prompt_files":                             runScriptManifestStringMap(t, manifest, "prompt_files"),
		"prompt_digests":                           runScriptManifestStringMap(t, manifest, "prompt_digests"),
		"mechanical_navigation_semantics_enforced": mechanical,
		"mechanical_navigation_contract": map[string]string{
			"required_root":                "<worktree>",
			"required_base_commit":         "<resolved-base>",
			"required_changed_return":      "<profile-return>",
			"required_changed_context":     "<profile-context>",
			"require_navigation_semantics": "1",
		},
		"codex_environment": []string{
			"env",
			"-i",
			"PATH=<generation-path>",
			"HOME=<shell-home>",
			"CODEX_HOME=<codex-home>",
			"LANG=C",
			"LC_ALL=C",
			"TZ=UTC",
			"GOROOT=<go-root>",
			"GOPATH=<go-path>",
			"GOMODCACHE=<go-mod-cache>",
			"GOCACHE=<go-cache>",
			"GO111MODULE=on",
			"GOENV=off",
			"GOTOOLCHAIN=local",
			"GOWORK=off",
			"GOFLAGS=-mod=readonly -trimpath -buildvcs=false",
			"GOPROXY=https://proxy.golang.org,direct",
			"GONOPROXY=",
			"GOPRIVATE=",
			"GONOSUMDB=",
			"GOSUMDB=sum.golang.org",
			"GOINSECURE=",
			"GOVCS=public:git|hg,private:all",
			"GOAUTH=off",
			"GIT_CONFIG_NOSYSTEM=1",
			"GIT_CONFIG_GLOBAL=/dev/null",
			"GIT_ATTR_NOSYSTEM=1",
			"GIT_CONFIG_COUNT=10",
			"GIT_CONFIG_KEY_0=core.hooksPath",
			"GIT_CONFIG_VALUE_0=/dev/null",
			"GIT_CONFIG_KEY_1=core.attributesFile",
			"GIT_CONFIG_VALUE_1=/dev/null",
			"GIT_CONFIG_KEY_2=core.excludesFile",
			"GIT_CONFIG_VALUE_2=/dev/null",
			"GIT_CONFIG_KEY_3=core.autocrlf",
			"GIT_CONFIG_VALUE_3=false",
			"GIT_CONFIG_KEY_4=core.eol",
			"GIT_CONFIG_VALUE_4=lf",
			"GIT_CONFIG_KEY_5=core.safecrlf",
			"GIT_CONFIG_VALUE_5=false",
			"GIT_CONFIG_KEY_6=core.fsmonitor",
			"GIT_CONFIG_VALUE_6=false",
			"GIT_CONFIG_KEY_7=core.untrackedCache",
			"GIT_CONFIG_VALUE_7=false",
			"GIT_CONFIG_KEY_8=core.sparseCheckout",
			"GIT_CONFIG_VALUE_8=false",
			"GIT_CONFIG_KEY_9=core.filemode",
			"GIT_CONFIG_VALUE_9=true",
			"GIT_TERMINAL_PROMPT=0",
			"GIT_NO_REPLACE_OBJECTS=1",
			"GIT_OPTIONAL_LOCKS=0",
			"GIT_DISCOVERY_ACROSS_FILESYSTEM=0",
			"GIT_PAGER=cat",
			"PAGER=cat",
		},
		"codex_isolation_flags": []string{
			"--ignore-user-config",
			"--ignore-rules",
			"-c",
			`default_permissions="benchmark"`,
			"-c",
			`permissions.benchmark={extends=":read-only", filesystem={` +
				`":root"="deny", ":minimal"="read", ` +
				`"<worktree>"="read", "<go-root>"="read", ` +
				`"<go-mod-cache>"="read", "<go-cache>"="read", ` +
				`"<repo-view-cache>"="read", "<shell-home>"="read", ` +
				`"<codex-executable>"="read", ` +
				`"<codex-home>"="deny"}}`,
			"-c",
			`shell_environment_policy.inherit="none"`,
			"-c",
			"shell_environment_policy.ignore_default_excludes=false",
			"-c",
			"shell_environment_policy.experimental_use_profile=false",
			"-c",
			`shell_environment_policy.set={` +
				`PATH="<repo-view-cache>/bin:<codex-bin>:` +
				`<go-root>/bin:/usr/local/bin:/usr/bin:/bin",` +
				`HOME="<shell-home>",LANG="C",LC_ALL="C",TZ="UTC",` +
				`GOROOT="<go-root>",GOPATH="<go-path>",` +
				`GOMODCACHE="<go-mod-cache>",GOCACHE="<go-cache>",` +
				`GOENV="off",GOTOOLCHAIN="local",GOWORK="off",` +
				`GOFLAGS="-mod=readonly -trimpath -buildvcs=false",` +
				`GIT_CONFIG_NOSYSTEM="1",` +
				`GIT_CONFIG_GLOBAL="/dev/null",` +
				`GIT_ATTR_NOSYSTEM="1",GIT_CONFIG_COUNT="10",` +
				`GIT_CONFIG_KEY_0="core.hooksPath",` +
				`GIT_CONFIG_VALUE_0="/dev/null",` +
				`GIT_CONFIG_KEY_1="core.attributesFile",` +
				`GIT_CONFIG_VALUE_1="/dev/null",` +
				`GIT_CONFIG_KEY_2="core.excludesFile",` +
				`GIT_CONFIG_VALUE_2="/dev/null",` +
				`GIT_CONFIG_KEY_3="core.autocrlf",` +
				`GIT_CONFIG_VALUE_3="false",` +
				`GIT_CONFIG_KEY_4="core.eol",GIT_CONFIG_VALUE_4="lf",` +
				`GIT_CONFIG_KEY_5="core.safecrlf",` +
				`GIT_CONFIG_VALUE_5="false",` +
				`GIT_CONFIG_KEY_6="core.fsmonitor",` +
				`GIT_CONFIG_VALUE_6="false",` +
				`GIT_CONFIG_KEY_7="core.untrackedCache",` +
				`GIT_CONFIG_VALUE_7="false",` +
				`GIT_CONFIG_KEY_8="core.sparseCheckout",` +
				`GIT_CONFIG_VALUE_8="false",` +
				`GIT_CONFIG_KEY_9="core.filemode",` +
				`GIT_CONFIG_VALUE_9="true",` +
				`GIT_TERMINAL_PROMPT="0",GIT_NO_REPLACE_OBJECTS="1",` +
				`GIT_OPTIONAL_LOCKS="0",` +
				`GIT_DISCOVERY_ACROSS_FILESYSTEM="0",` +
				`GIT_PAGER="cat",PAGER="cat"}`,
			"-c",
			"project_doc_max_bytes=0",
			"-c",
			"project_doc_fallback_filenames=[]",
			"-c",
			"mcp_servers={}",
			"-c",
			"apps._default.enabled=false",
		},
		"host_go_environment": []string{
			"env",
			"-u", "GOOS",
			"-u", "GOARCH",
			"-u", "GO386",
			"-u", "GOAMD64",
			"-u", "GOARM",
			"-u", "GOARM64",
			"-u", "GOMIPS",
			"-u", "GOMIPS64",
			"-u", "GOPPC64",
			"-u", "GORISCV64",
			"-u", "GOWASM",
			"-u", "CGO_ENABLED",
			"-u", "CC",
			"-u", "CXX",
			"-u", "CGO_CFLAGS",
			"-u", "CGO_CPPFLAGS",
			"-u", "CGO_CXXFLAGS",
			"-u", "CGO_LDFLAGS",
			"-u", "PKG_CONFIG",
			"-u", "GOROOT",
			"-u", "GOPATH",
			"-u", "GOMODCACHE",
			"-u", "GOCACHE",
			"-u", "GOEXPERIMENT",
			"-u", "GODEBUG",
			"GO111MODULE=on",
			"GOENV=off",
			"GOTOOLCHAIN=local",
			"GOWORK=off",
			"GOFLAGS=-mod=readonly -trimpath -buildvcs=false",
			"GOPROXY=https://proxy.golang.org,direct",
			"GONOPROXY=",
			"GOPRIVATE=",
			"GONOSUMDB=",
			"GOSUMDB=sum.golang.org",
			"GOINSECURE=",
			"GOVCS=public:git|hg,private:all",
			"GOAUTH=off",
		},
	}
	var buffer bytes.Buffer
	encoder := json.NewEncoder(&buffer)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(config); err != nil {
		t.Fatal(err)
	}
	content := bytes.TrimSuffix(buffer.Bytes(), []byte("\n"))
	return content
}

func runScriptBaselineManifest(
	t *testing.T,
	targetCommit, promptCommit, baseCommit, baseRef, goVersion string,
	tasks ...string,
) map[string]any {
	t.Helper()
	if len(tasks) == 0 {
		tasks = []string{"explain"}
	}
	command := exec.Command(
		"go", "env", "-json", "GOROOT", "GOPATH", "GOMODCACHE", "GOCACHE",
	)
	environment := make([]string, 0, len(os.Environ())+3)
	for _, variable := range os.Environ() {
		if strings.HasPrefix(variable, "GOENV=") ||
			strings.HasPrefix(variable, "GOWORK=") ||
			strings.HasPrefix(variable, "GOFLAGS=") {
			continue
		}
		environment = append(environment, variable)
	}
	environment = append(
		environment,
		"GOENV=off",
		"GOWORK=off",
		"GOFLAGS=-mod=readonly",
	)
	command.Env = environment
	output, err := command.Output()
	if err != nil {
		t.Fatal(err)
	}
	var goEnvironment struct {
		Root     string `json:"GOROOT"`
		Path     string `json:"GOPATH"`
		ModCache string `json:"GOMODCACHE"`
		Cache    string `json:"GOCACHE"`
	}
	if err := json.Unmarshal(output, &goEnvironment); err != nil {
		t.Fatal(err)
	}
	promptDigests := runScriptPromptDigests(
		t,
		promptCommit,
		baseCommit,
		tasks...,
	)
	promptFiles := make(map[string]string, len(tasks))
	for _, task := range tasks {
		promptFiles[task] = "prompts/" + task + ".txt"
	}
	profilesSnapshot := runScriptProfilesSnapshot(t)
	profilesDigest := sha256.Sum256(profilesSnapshot)
	manifest := map[string]any{
		"target_commit":        targetCommit,
		"prompt_commit":        promptCommit,
		"base_commit":          baseCommit,
		"base_ref":             baseRef,
		"model":                "router-selected",
		"codex_version":        "codex-test 1",
		"generation_isolation": runScriptGenerationIsolation,
		"mechanical_navigation_semantics_enforced": false,
		"profiles_snapshot_path":                   "profiles-snapshot.tsv",
		"profiles_snapshot_sha256":                 fmt.Sprintf("%x", profilesDigest),
		"go_version":                               goVersion,
		"go_root":                                  goEnvironment.Root,
		"go_path":                                  goEnvironment.Path,
		"go_mod_cache":                             goEnvironment.ModCache,
		"go_cache":                                 goEnvironment.Cache,
		"prompt_files":                             promptFiles,
		"prompt_digests":                           promptDigests,
	}
	generationConfig := runScriptGenerationConfig(t, manifest)
	generationDigest := sha256.Sum256(generationConfig)
	manifest["generation_config_sha256"] = fmt.Sprintf("%x", generationDigest)
	return manifest
}

func runScriptPromptDigests(
	t *testing.T,
	promptCommit, baseCommit string,
	tasks ...string,
) map[string]string {
	t.Helper()
	if len(tasks) == 0 {
		tasks = []string{"explain"}
	}
	digests := make(map[string]string)
	for _, task := range tasks {
		rendered := runScriptRenderedPrompt(
			t,
			task,
			promptCommit,
			baseCommit,
		)
		digest := sha256.Sum256([]byte(rendered))
		digests[task] = fmt.Sprintf("%x", digest)
	}
	return digests
}

func runScriptProfilesSnapshot(t *testing.T) []byte {
	t.Helper()
	sourceRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(
		sourceRoot,
		"experiments",
		"lsp-replacement",
		"profiles.tsv",
	))
	if err != nil {
		t.Fatal(err)
	}
	return content
}

func runScriptRenderedPrompt(
	t *testing.T,
	task, promptCommit, baseCommit string,
) string {
	t.Helper()
	sourceRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(filepath.Join(
		sourceRoot,
		"experiments",
		"lsp-replacement",
		"prompts",
		task+".txt",
	))
	if err != nil {
		t.Fatal(err)
	}
	rendered := strings.TrimRight(string(content), "\n")
	rendered = strings.ReplaceAll(
		rendered,
		"{{COMMIT_SHORT}}",
		promptCommit,
	)
	return strings.ReplaceAll(rendered, "{{BASE}}", baseCommit)
}

func initializeRunScriptGitTarget(t *testing.T) (string, string) {
	t.Helper()
	return initializeRunScriptGitTargetWithContent(t, "fixture\n")
}

func initializeRunScriptGitTargetWithContent(
	t *testing.T,
	content string,
) (string, string) {
	t.Helper()
	repo := t.TempDir()
	writeRunScriptFile(t, filepath.Join(repo, "fixture.txt"), content)
	for _, arguments := range [][]string{
		{"init", "--quiet"},
		{"add", "fixture.txt"},
		{
			"-c", "user.name=Run Script Test",
			"-c", "user.email=run-script-test@example.invalid",
			"-c", "commit.gpgSign=false",
			"-c", "core.hooksPath=/dev/null",
			"commit", "--quiet", "--no-gpg-sign", "--no-verify", "-m", "fixture",
		},
		{"branch", "-M", "main"},
	} {
		command := exec.Command("git", append([]string{"-C", repo}, arguments...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s failed: %v\n%s", arguments[0], err, output)
		}
	}
	output, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return repo, strings.TrimSpace(string(output))
}

func initializeRunScriptSHA256GitTarget(t *testing.T) (string, string) {
	t.Helper()
	repo := t.TempDir()
	if output, err := exec.Command(
		"git", "-C", repo, "init", "--quiet", "--object-format=sha256",
	).CombinedOutput(); err != nil {
		t.Skipf("Git does not support SHA-256 repositories: %v\n%s", err, output)
	}
	writeRunScriptFile(t, filepath.Join(repo, "fixture.txt"), "fixture\n")
	for _, arguments := range [][]string{
		{"add", "fixture.txt"},
		{
			"-c", "user.name=Run Script Test",
			"-c", "user.email=run-script-test@example.invalid",
			"-c", "commit.gpgSign=false",
			"-c", "core.hooksPath=/dev/null",
			"commit", "--quiet", "--no-gpg-sign", "--no-verify", "-m", "fixture",
		},
		{"branch", "-M", "main"},
	} {
		command := exec.Command("git", append([]string{"-C", repo}, arguments...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s failed: %v\n%s", arguments[0], err, output)
		}
	}
	output, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	target := strings.TrimSpace(string(output))
	if len(target) != 64 {
		t.Fatalf("SHA-256 target length = %d, want 64", len(target))
	}
	return repo, target
}

func appendRunScriptGitCommit(t *testing.T, repo, content string) string {
	t.Helper()
	writeRunScriptFile(t, filepath.Join(repo, "fixture.txt"), content)
	for _, arguments := range [][]string{
		{"add", "fixture.txt"},
		{
			"-c", "user.name=Run Script Test",
			"-c", "user.email=run-script-test@example.invalid",
			"-c", "commit.gpgSign=false",
			"-c", "core.hooksPath=/dev/null",
			"commit", "--quiet", "--no-gpg-sign", "--no-verify", "-m", "next fixture",
		},
	} {
		command := exec.Command("git", append([]string{"-C", repo}, arguments...)...)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s failed: %v\n%s", arguments[0], err, output)
		}
	}
	output, err := exec.Command("git", "-C", repo, "rev-parse", "HEAD").Output()
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(output))
}

func initializeRunScriptSourceFixture(t *testing.T) (string, string) {
	t.Helper()
	sourceRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	fixtureRoot := t.TempDir()
	for _, relative := range []string{
		"experiments/lsp-replacement/run.sh",
		"experiments/lsp-replacement/config.env",
		"experiments/lsp-replacement/profiles.tsv",
		"experiments/lsp-replacement/prompts/explain.txt",
		"experiments/lsp-replacement/prompts/review.txt",
		"experiments/lsp-replacement/prompts/deep-explain.txt",
		"experiments/lsp-replacement/prompts/deep-review.txt",
	} {
		source := filepath.Join(sourceRoot, filepath.FromSlash(relative))
		content, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		destination := filepath.Join(fixtureRoot, filepath.FromSlash(relative))
		if err := os.MkdirAll(filepath.Dir(destination), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(destination, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runScript := filepath.Join(
		fixtureRoot,
		"experiments",
		"lsp-replacement",
		"run.sh",
	)
	if err := os.Chmod(runScript, 0o755); err != nil {
		t.Fatal(err)
	}
	writeRunScriptFile(
		t,
		filepath.Join(fixtureRoot, "snapshot-sentinel.txt"),
		"before snapshot\n",
	)
	for _, arguments := range [][]string{
		{"init", "--quiet"},
		{"add", "."},
		{
			"-c", "user.name=Run Script Test",
			"-c", "user.email=run-script-test@example.invalid",
			"-c", "commit.gpgSign=false",
			"-c", "core.hooksPath=/dev/null",
			"commit", "--quiet", "--no-gpg-sign", "--no-verify", "-m", "runner fixture",
		},
	} {
		command := exec.Command(
			"git",
			append([]string{"-C", fixtureRoot}, arguments...)...,
		)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("git %s failed: %v\n%s", arguments[0], err, output)
		}
	}
	return fixtureRoot, runScript
}

func writeRunScriptJSON(t *testing.T, path string, value any) {
	t.Helper()
	content, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	writeRunScriptFile(t, path, string(content)+"\n")
	if filepath.Base(path) == "manifest.json" {
		manifest, ok := value.(map[string]any)
		if ok && manifest["generation_config_sha256"] != nil {
			if err := os.WriteFile(
				filepath.Join(filepath.Dir(path), "generation-config.json"),
				runScriptGenerationConfig(t, manifest),
				0o644,
			); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(
				filepath.Join(filepath.Dir(path), "profiles-snapshot.tsv"),
				runScriptProfilesSnapshot(t),
				0o644,
			); err != nil {
				t.Fatal(err)
			}
			promptFiles := runScriptManifestStringMap(t, manifest, "prompt_files")
			promptDirectory := filepath.Join(filepath.Dir(path), "prompts")
			if err := os.MkdirAll(promptDirectory, 0o755); err != nil {
				t.Fatal(err)
			}
			for task, relative := range promptFiles {
				rendered := runScriptRenderedPrompt(
					t,
					task,
					manifest["prompt_commit"].(string),
					manifest["base_commit"].(string),
				)
				writeRunScriptFile(
					t,
					filepath.Join(filepath.Dir(path), filepath.FromSlash(relative)),
					rendered,
				)
			}
		}
	}
}

func writeRunScriptFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func stringPointer(value string) *string {
	return &value
}

func assertRunScriptExit(t *testing.T, err error, want int, output []byte) {
	t.Helper()
	exitError := &exec.ExitError{}
	ok := errors.As(err, &exitError)
	if !ok {
		t.Fatalf("run.sh exit = %v, want %d\n%s", err, want, output)
	}
	if got := exitError.ExitCode(); got != want {
		t.Fatalf("run.sh exit = %d, want %d\n%s", got, want, output)
	}
}
