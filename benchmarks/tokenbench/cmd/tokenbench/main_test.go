package main

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/yapless/scopesifter/benchmarks/tokenbench"
	"github.com/yapless/scopesifter/benchmarks/tokenbench/cas"
	"github.com/yapless/scopesifter/benchmarks/tokenbench/evidence"
	"github.com/yapless/scopesifter/benchmarks/tokenbench/source"
	"golang.org/x/sys/unix"
)

func TestValidateAndPlanRejectNonCodexAdapter(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	sourceRoot, base, head := commandTestRepository(t, directory)
	treeDigest, err := source.TreeDigest(context.Background(), sourceRoot)
	if err != nil {
		t.Fatal(err)
	}
	harnessPath := commandTestExecutable(t, directory, "fake-harness", "harness")
	scopeSifterPath := commandTestExecutable(t, directory, "scopesifter", "scopesifter")
	gitExecutable, gitSHA256 := commandTestGitIdentity(t)
	prompt := []byte("Explain the repository change.\n")
	promptPath := filepath.Join(directory, "prompt.md")
	if err := os.WriteFile(promptPath, prompt, 0o600); err != nil {
		t.Fatal(err)
	}
	suite := tokenbench.Suite{
		SchemaVersion:          tokenbench.SuiteSchemaVersion,
		ID:                     "cli-fixture",
		PromptFile:             "prompt.md",
		HarnessKind:            "fake",
		HarnessExecutable:      harnessPath,
		HarnessSHA256:          tokenbench.SHA256([]byte("harness")),
		ArtifactManifestSHA256: tokenbench.SHA256([]byte("artifact-manifest")),
		GitExecutable:          gitExecutable,
		GitExecutableSHA256:    gitSHA256,
		Model:                  "fixed-model",
		ExpectedModelRevision:  "fixed-model@2026-08-01",
		ReasoningEffort:        "medium",
		PermissionProfile:      "read-only",
		DeveloperInstructions:  "common instructions",
		SourceRoot:             sourceRoot,
		SourceRevision:         head,
		SourceBaseRevision:     base,
		SourceTreeSHA256:       treeDigest,
		TimeoutMillis:          30_000,
		Repetitions:            10,
		Seed:                   42,
	}
	suiteRaw, err := json.Marshal(suite)
	if err != nil {
		t.Fatal(err)
	}
	suitePath := filepath.Join(directory, "suite.json")
	if err := os.WriteFile(suitePath, suiteRaw, 0o600); err != nil {
		t.Fatal(err)
	}

	for _, arguments := range [][]string{
		{"validate", "--suite", suitePath},
		{
			"plan", "--suite", suitePath, "--scopesifter-mcp", scopeSifterPath,
			"--state-root", filepath.Join(directory, "planned-runtime"),
		},
	} {
		var stdout, stderr bytes.Buffer
		exitCode := run(context.Background(), arguments, &stdout, &stderr)
		if exitCode == 0 || !strings.Contains(stderr.String(), "only the built-in Codex adapter") {
			t.Fatalf("non-Codex command was accepted: args=%q exit=%d stderr=%q", arguments, exitCode, stderr.String())
		}
	}
}

func TestDispatchUsesGoCommandRunnerAtCodexCompatibilityPath(t *testing.T) {
	t.Parallel()
	var stdout, stderr bytes.Buffer
	exitCode := dispatch(
		context.Background(),
		"/snapshot/toolbox/bash",
		"/snapshot/toolbox",
		[]string{"-c", "printf '%s\\n' go-native"},
		strings.NewReader(""),
		&stdout,
		&stderr,
	)
	if exitCode != 0 || stdout.String() != "go-native\n" || stderr.Len() != 0 {
		t.Fatalf(
			"dispatch() exit=%d stdout=%q stderr=%q",
			exitCode,
			stdout.String(),
			stderr.String(),
		)
	}
}

func TestValidateRejectsCandidateOverride(t *testing.T) {
	t.Parallel()
	directory := t.TempDir()
	promptPath := filepath.Join(directory, "prompt.md")
	if err := os.WriteFile(promptPath, []byte("task\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	raw := []byte(`{
  "schema_version":"tokenbench.suite/v2",
  "candidate_prompt":"answer key"
}`)
	suitePath := filepath.Join(directory, "suite.json")
	if err := os.WriteFile(suitePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	if exitCode := run(
		context.Background(),
		[]string{"validate", "--suite", suitePath},
		&stdout,
		&stderr,
	); exitCode == 0 || !strings.Contains(stderr.String(), "unknown field") {
		t.Fatalf("candidate override was not rejected: exit=%d stderr=%q", exitCode, stderr.String())
	}
}

func TestCLIRejectsSerializedPlanAndExternalAdapterAuthority(t *testing.T) {
	for _, test := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "run requires repetition",
			args: []string{
				"run", "--suite", "/suite.json", "--artifact-bundle", "/artifacts",
				"--snapshot-root", "/snapshot",
				"--state-root", "/state", "--cas", "/cas", "--root-out", "/root.json",
				"--credential-fd", "3", "--signing-key-file", "/signing-key",
			},
			want: "explicit nonnegative --repetition",
		},
		{
			name: "serialized plan",
			args: []string{"run", "--plan", "/untrusted-plan.json"},
			want: "flag provided but not defined: -plan",
		},
		{
			name: "unbundled scopesifter path cannot bypass bundle",
			args: []string{"run", "--scopesifter-mcp", "/scopesifter"},
			want: "flag provided but not defined: -scopesifter-mcp",
		},
		{
			name: "external adapter",
			args: []string{"plan", "--adapter-command", "/external-adapter"},
			want: "flag provided but not defined: -adapter-command",
		},
		{
			name: "verify trust",
			args: []string{"verify", "--cas", "/cas", "--root", "/root.json"},
			want: "--trust-policy",
		},
		{
			name: "replay trust",
			args: []string{
				"replay", "--cas", "/cas", "--root", "/root.json",
				"--signing-key-file", "/key", "--root-out", "/replay.json",
			},
			want: "--trust-policy",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if exit := run(context.Background(), test.args, &stdout, &stderr); exit == 0 || !strings.Contains(stderr.String(), test.want) {
				t.Fatalf("exit=%d stderr=%q, want %q", exit, stderr.String(), test.want)
			}
		})
	}
}

func TestRunPathsKeepMutableOutputsSnapshotBundleAndSourceDisjoint(t *testing.T) {
	directory := t.TempDir()
	valid := runPaths{
		StateRoot:          filepath.Join(directory, "state"),
		SnapshotRoot:       filepath.Join(directory, "snapshot"),
		ArtifactBundleRoot: filepath.Join(directory, "artifacts"),
		CAS:                filepath.Join(directory, "cas"),
		RootOutput:         filepath.Join(directory, "root.json"),
		SigningKeyFile:     filepath.Join(directory, "signing.key"),
		TrustPolicy:        filepath.Join(directory, "trust.json"),
		SourceRoot:         filepath.Join(directory, "source"),
	}
	resolved, err := resolveRunPaths(valid)
	if err != nil || !reflect.DeepEqual(resolved, valid) {
		t.Fatalf("valid run paths = %#v, %v", resolved, err)
	}

	for _, test := range []struct {
		name   string
		mutate func(*runPaths)
	}{
		{
			name: "snapshot below writable state",
			mutate: func(paths *runPaths) {
				paths.SnapshotRoot = filepath.Join(paths.StateRoot, "snapshot")
			},
		},
		{
			name: "artifact bundle below source",
			mutate: func(paths *runPaths) {
				paths.ArtifactBundleRoot = filepath.Join(paths.SourceRoot, "artifacts")
			},
		},
		{
			name: "CAS contains signing key",
			mutate: func(paths *runPaths) {
				paths.SigningKeyFile = filepath.Join(paths.CAS, "key")
			},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			forged := valid
			test.mutate(&forged)
			if _, err := resolveRunPaths(forged); err == nil ||
				!strings.Contains(err.Error(), "disjoint") {
				t.Fatalf("overlapping paths accepted: %v", err)
			}
		})
	}
}

func TestCanonicalSignedRootReferencesAreExact(t *testing.T) {
	directory := t.TempDir()
	root := cas.ObjectRef{
		Digest:    "sha256:" + strings.Repeat("0", 64),
		Size:      123,
		MediaType: attestationRootType,
	}
	canonical, err := canonicalRoot(root)
	if err != nil {
		t.Fatal(err)
	}
	validPath := filepath.Join(directory, "valid.json")
	if err := os.WriteFile(validPath, canonical, 0o600); err != nil {
		t.Fatal(err)
	}
	decoded, err := readCanonicalRoot(validPath)
	if err != nil || decoded != root {
		t.Fatalf("canonical root = %#v, %v", decoded, err)
	}

	variants := map[string][]byte{
		"trailing newline": append(append([]byte(nil), canonical...), '\n'),
		"reordered":        []byte(`{"media_type":"` + root.MediaType + `","size":123,"digest":"` + root.Digest + `"}`),
		"unknown":          append(canonical[:len(canonical)-1], []byte(`,"extra":true}`)...),
		"duplicate":        []byte(`{"digest":"` + root.Digest + `","digest":"` + root.Digest + `","size":123,"media_type":"` + root.MediaType + `"}`),
	}
	unsigned := root
	unsigned.MediaType = "application/json"
	variants["unsigned media type"], _ = json.Marshal(unsigned)
	for name, raw := range variants {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(directory, strings.ReplaceAll(name, " ", "-")+".json")
			if err := os.WriteFile(path, raw, 0o600); err != nil {
				t.Fatal(err)
			}
			if _, err := readCanonicalRoot(path); err == nil {
				t.Fatal("noncanonical or unsigned root reference was accepted")
			}
		})
	}
}

func TestSigningKeySourceIsPinnedPrivateAndCanonical(t *testing.T) {
	directory := t.TempDir()
	seed := bytes.Repeat([]byte{0x42}, ed25519.SeedSize)
	canonical := []byte(base64.RawURLEncoding.EncodeToString(seed))
	validPath := filepath.Join(directory, "valid.key")
	if err := os.WriteFile(validPath, canonical, 0o600); err != nil {
		t.Fatal(err)
	}
	signer, err := loadEd25519Signer(validPath)
	if err != nil || len(signer.AttestationPublicKey()) != ed25519.PublicKeySize {
		t.Fatalf("load canonical signer: %v", err)
	}

	newlinePath := filepath.Join(directory, "newline.key")
	secretText := append(append([]byte(nil), canonical...), '\n')
	if err := os.WriteFile(newlinePath, secretText, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := loadEd25519Signer(newlinePath); !errors.Is(err, errSigningKeySource) ||
		strings.Contains(err.Error(), string(secretText)) {
		t.Fatalf("noncanonical key error leaked or varied: %v", err)
	}

	modePath := filepath.Join(directory, "group-readable.key")
	if err := os.WriteFile(modePath, canonical, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(modePath, 0o640); err != nil {
		t.Fatal(err)
	}
	if _, err := loadEd25519Signer(modePath); !errors.Is(err, errSigningKeySource) {
		t.Fatalf("group-readable key was accepted: %v", err)
	}

	hardlinkSource := filepath.Join(directory, "linked.key")
	hardlink := filepath.Join(directory, "linked-copy.key")
	if err := os.WriteFile(hardlinkSource, canonical, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Link(hardlinkSource, hardlink); err != nil {
		t.Fatal(err)
	}
	if _, err := loadEd25519Signer(hardlinkSource); !errors.Is(err, errSigningKeySource) {
		t.Fatalf("multiply-linked key was accepted: %v", err)
	}

	symlink := filepath.Join(directory, "symlink.key")
	if err := os.Symlink(validPath, symlink); err != nil {
		t.Fatal(err)
	}
	if _, err := loadEd25519Signer(symlink); !errors.Is(err, errSigningKeySource) {
		t.Fatalf("symlink key was accepted: %v", err)
	}
}

func TestCredentialErrorsNeverContainCredentialBytes(t *testing.T) {
	secret := "SUPER-SECRET-CREDENTIAL\n"
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := writer.WriteString(secret); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	descriptor, err := syscall.Dup(int(reader.Fd()))
	if err != nil {
		t.Fatal(err)
	}
	if err := reader.Close(); err != nil {
		t.Fatal(err)
	}
	_, err = loadCredentialFD(descriptor)
	if !errors.Is(err, errCredentialSource) || strings.Contains(err.Error(), secret) ||
		strings.Contains(err.Error(), "SUPER-SECRET") {
		t.Fatalf("credential error leaked or varied: %v", err)
	}
}

func TestCredentialDescriptorIsCanonicalClosedAndNotInherited(t *testing.T) {
	for _, raw := range []string{"+3", "03", "2", "256", "3x"} {
		var descriptor secretDescriptor
		if err := descriptor.Set(raw); err == nil {
			t.Fatalf("noncanonical descriptor %q was accepted", raw)
		}
	}
	var duplicate secretDescriptor
	if err := duplicate.Set("3"); err != nil {
		t.Fatal(err)
	}
	if err := duplicate.Set("4"); err == nil {
		t.Fatal("duplicate credential descriptor was accepted")
	}

	path := filepath.Join(t.TempDir(), "credential")
	if err := os.WriteFile(path, []byte("canonical-secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	file, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	descriptor, err := unix.FcntlInt(file.Fd(), unix.F_DUPFD, 100)
	if err != nil {
		file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	credential, err := loadCredentialFD(descriptor)
	if err != nil || string(credential) != "canonical-secret" {
		t.Fatalf("credential = %q, %v", credential, err)
	}
	clear(credential)
	if !descriptorIsClosed(descriptor) {
		t.Fatal("credential descriptor remained open after its single read")
	}
	command := exec.Command(
		"/bin/sh",
		"-c",
		fmt.Sprintf("test ! -e /proc/self/fd/%d", descriptor),
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("closed credential descriptor reached child: %v: %s", err, output)
	}

	source, err := os.ReadFile("commands.go")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(source, []byte("credential-file")) ||
		bytes.Contains(source, []byte("canonical-secret")) {
		t.Fatal("CLI source exposes a credential path/value argument")
	}
}

func TestCredentialPipesAreReadOnlyPrivateAndDeadlineBounded(t *testing.T) {
	t.Run("valid anonymous pipe", func(t *testing.T) {
		reader, writer, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		if _, err := writer.WriteString("pipe-secret"); err != nil {
			t.Fatal(err)
		}
		if err := writer.Close(); err != nil {
			t.Fatal(err)
		}
		descriptor := duplicateCredentialDescriptor(t, reader)
		credential, err := loadCredentialFDWithin(descriptor, 100*time.Millisecond)
		if err != nil || string(credential) != "pipe-secret" {
			t.Fatalf("pipe credential = %q, %v", credential, err)
		}
		clear(credential)
		if !descriptorIsClosed(descriptor) {
			t.Fatal("anonymous credential pipe remained open")
		}
	})

	t.Run("world-readable fifo", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "credential.fifo")
		if err := unix.Mkfifo(path, 0o666); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0o666); err != nil {
			t.Fatal(err)
		}
		descriptor, err := unix.Open(path, unix.O_RDONLY|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := loadCredentialFDWithin(descriptor, 25*time.Millisecond); !errors.Is(err, errCredentialSource) {
			t.Fatalf("world-readable FIFO error = %v", err)
		}
		if !descriptorIsClosed(descriptor) {
			t.Fatal("rejected FIFO descriptor remained open")
		}
	})

	t.Run("read-write fifo", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "credential.fifo")
		if err := unix.Mkfifo(path, 0o600); err != nil {
			t.Fatal(err)
		}
		descriptor, err := unix.Open(path, unix.O_RDWR|unix.O_NONBLOCK|unix.O_CLOEXEC, 0)
		if err != nil {
			t.Fatal(err)
		}
		started := time.Now()
		if _, err := loadCredentialFDWithin(descriptor, time.Second); !errors.Is(err, errCredentialSource) {
			t.Fatalf("read-write FIFO error = %v", err)
		}
		if time.Since(started) > 250*time.Millisecond {
			t.Fatal("read-write FIFO waited for an EOF instead of being rejected")
		}
		if !descriptorIsClosed(descriptor) {
			t.Fatal("rejected read-write FIFO remained open")
		}
	})

	t.Run("writer never closes", func(t *testing.T) {
		reader, writer, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer writer.Close()
		descriptor := duplicateCredentialDescriptor(t, reader)
		started := time.Now()
		if _, err := loadCredentialFDWithin(descriptor, 40*time.Millisecond); !errors.Is(err, errCredentialSource) {
			t.Fatalf("nonterminating pipe error = %v", err)
		}
		elapsed := time.Since(started)
		if elapsed < 20*time.Millisecond || elapsed > time.Second {
			t.Fatalf("bounded pipe read elapsed %s", elapsed)
		}
		if !descriptorIsClosed(descriptor) {
			t.Fatal("timed-out pipe descriptor remained open")
		}
	})
}

func TestCredentialCloseNeverTargetsAReusedDescriptorNumber(t *testing.T) {
	path := filepath.Join(t.TempDir(), "credential")
	if err := os.WriteFile(path, []byte("credential"), 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	descriptor := duplicateCredentialDescriptor(t, source)
	credential, err := loadCredentialFD(descriptor)
	if err != nil {
		t.Fatal(err)
	}
	clear(credential)

	unrelated, err := os.Open("/dev/null")
	if err != nil {
		t.Fatal(err)
	}
	defer unrelated.Close()
	if err := unix.Dup3(int(unrelated.Fd()), descriptor, unix.O_CLOEXEC); err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := unix.Close(descriptor); err != nil {
			t.Errorf("close unrelated reused descriptor: %v", err)
		}
	}()
	if _, err := unix.FcntlInt(uintptr(descriptor), unix.F_GETFD, 0); err != nil {
		t.Fatalf("unrelated descriptor occupying the consumed number was closed: %v", err)
	}

	implementation, err := os.ReadFile("files.go")
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(implementation, []byte("unix.Close(descriptor)")) ||
		bytes.Contains(implementation, []byte("verifyDescriptorClosed")) {
		t.Fatal("credential loader closes or probes a raw descriptor number after ownership Close")
	}
}

func duplicateCredentialDescriptor(t *testing.T, file *os.File) int {
	t.Helper()
	descriptor, err := unix.FcntlInt(file.Fd(), unix.F_DUPFD, 100)
	if err != nil {
		file.Close()
		t.Fatal(err)
	}
	if descriptor > 255 {
		_ = unix.Close(descriptor)
		file.Close()
		t.Fatalf("credential test descriptor %d exceeds CLI range", descriptor)
	}
	if err := file.Close(); err != nil {
		_ = unix.Close(descriptor)
		t.Fatal(err)
	}
	return descriptor
}

func descriptorIsClosed(descriptor int) bool {
	_, err := unix.FcntlInt(uintptr(descriptor), unix.F_GETFD, 0)
	return errors.Is(err, unix.EBADF)
}

func TestCleanupBoundaryPrecedesSigningAndAttemptsBothClosers(t *testing.T) {
	executorFailure := errors.New("executor cleanup failed")
	lifecycleFailure := errors.New("lifecycle cleanup failed")
	snapshotFailure := errors.New("snapshot cleanup failed")
	order := make([]string, 0, 3)
	err := closeExecutionBoundary(
		func(context.Context) error {
			order = append(order, "executor")
			return executorFailure
		},
		func(context.Context) error {
			order = append(order, "lifecycle")
			return lifecycleFailure
		},
		func() error {
			order = append(order, "snapshot")
			return snapshotFailure
		},
	)
	if !reflect.DeepEqual(order, []string{"executor", "lifecycle", "snapshot"}) ||
		!errors.Is(err, executorFailure) || !errors.Is(err, lifecycleFailure) ||
		!errors.Is(err, snapshotFailure) {
		t.Fatalf("cleanup order/errors = %q, %v", order, err)
	}

	signerRead := false
	err = afterClosedCodexExecution(
		func() (tokenbench.Run, error) { return tokenbench.Run{}, executorFailure },
		func(tokenbench.Run) error {
			signerRead = true
			return nil
		},
	)
	if !errors.Is(err, executorFailure) || signerRead {
		t.Fatalf("signer ran before successful cleanup: called=%t err=%v", signerRead, err)
	}
}

func TestExclusiveOutputNeverOverwritesAndAbortRemovesOnlyStaging(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "root.json")
	output, err := claimExclusiveOutput(path, 0o600, "test root")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("claim exposed a partial final output: %v", err)
	}
	if err := output.commit([]byte("first")); err != nil {
		t.Fatal(err)
	}
	output.abort()
	if _, err := claimExclusiveOutput(path, 0o600, "test root"); err == nil {
		t.Fatal("exclusive output overwrote an existing file")
	}
	content, err := os.ReadFile(path)
	if err != nil || string(content) != "first" {
		t.Fatalf("exclusive content = %q, %v", content, err)
	}

	abortedPath := filepath.Join(directory, "aborted.json")
	aborted, err := claimExclusiveOutput(abortedPath, 0o600, "test root")
	if err != nil {
		t.Fatal(err)
	}
	aborted.abort()
	if _, err := os.Lstat(abortedPath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("abort exposed a final output: %v", err)
	}
}

func TestExclusiveOutputCrashLeavesNoPartialFinal(t *testing.T) {
	const helperEnvironment = "TOKENBENCH_TEST_CRASH_STAGING"
	if path := os.Getenv(helperEnvironment); path != "" {
		output, err := claimExclusiveOutput(path, 0o600, "crash-test output")
		if err != nil {
			os.Exit(21)
		}
		if _, err := output.file.Write([]byte("partial")); err != nil {
			os.Exit(22)
		}
		if err := output.file.Sync(); err != nil {
			os.Exit(23)
		}
		// Intentionally skip abort and every Close to model abrupt process loss.
		os.Exit(0)
	}

	directory := t.TempDir()
	path := filepath.Join(directory, "result.json")
	command := exec.Command(os.Args[0], "-test.run=^TestExclusiveOutputCrashLeavesNoPartialFinal$")
	command.Env = append(os.Environ(), helperEnvironment+"="+path)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("crash helper: %v: %s", err, output)
	}
	if _, err := os.Lstat(path); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("crash exposed a partial final output: %v", err)
	}
	stale, err := filepath.Glob(filepath.Join(directory, ".result.json.tokenbench-staging-*"))
	if err != nil || len(stale) != 1 {
		t.Fatalf("stale private staging files = %q, %v", stale, err)
	}

	retry, err := claimExclusiveOutput(path, 0o600, "retry output")
	if err != nil {
		t.Fatalf("stale random staging name blocked retry: %v", err)
	}
	defer retry.abort()
	if err := retry.commit([]byte("complete")); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "complete" {
		t.Fatalf("retried final output = %q, %v", raw, err)
	}
}

func TestExclusiveOutputNoReplaceWinsFinalRace(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "result.json")
	output, err := claimExclusiveOutput(path, 0o600, "raced output")
	if err != nil {
		t.Fatal(err)
	}
	defer output.abort()
	if err := os.WriteFile(path, []byte("racer"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := output.commit([]byte("ours")); err == nil {
		t.Fatal("exclusive output replaced a concurrently created final file")
	}
	output.abort()
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "racer" {
		t.Fatalf("raced final output = %q, %v", raw, err)
	}
	staging, err := filepath.Glob(filepath.Join(directory, ".result.json.tokenbench-staging-*"))
	if err != nil || len(staging) != 0 {
		t.Fatalf("failed publication left staging files = %q, %v", staging, err)
	}
}

func TestPublicationOutcomesFinalizeKnownRootsAndTypedRecovery(t *testing.T) {
	root := cas.ObjectRef{
		Digest:    "sha256:" + strings.Repeat("7", 64),
		Size:      123,
		MediaType: attestationRootType,
	}
	complete := evidence.PublicationResult{
		IntendedRoot:     root,
		State:            evidence.PublicationComplete,
		Durable:          true,
		GraphVerified:    true,
		RecoveryRequired: false,
	}
	uncertainRoot := root
	for _, test := range []struct {
		name   string
		result evidence.PublicationResult
		want   bool
	}{
		{"complete", complete, true},
		{"visible", evidence.PublicationResult{IntendedRoot: root, UncertainObject: &uncertainRoot, UncertainStage: "test/visible", State: evidence.PublicationVisible, RecoveryRequired: true}, false},
		{"indeterminate", evidence.PublicationResult{IntendedRoot: root, UncertainObject: &uncertainRoot, UncertainStage: "test/indeterminate", State: evidence.PublicationIndeterminate, RecoveryRequired: true}, false},
		{"complete but not durable", evidence.PublicationResult{IntendedRoot: root, State: evidence.PublicationComplete, GraphVerified: true}, false},
		{"complete but graph unverified", evidence.PublicationResult{IntendedRoot: root, State: evidence.PublicationComplete, Durable: true}, false},
		{"complete with invalid root", evidence.PublicationResult{State: evidence.PublicationComplete, Durable: true, GraphVerified: true}, false},
	} {
		t.Run("classification/"+test.name, func(t *testing.T) {
			if got := finalizablePublication(test.result); got != test.want {
				t.Fatalf("finalizablePublication = %t, want %t", got, test.want)
			}
		})
	}

	t.Run("complete warning still finalizes root", func(t *testing.T) {
		directory := t.TempDir()
		path := filepath.Join(directory, "root.json")
		output, err := claimExclusiveOutput(path, 0o600, "known root")
		if err != nil {
			t.Fatal(err)
		}
		defer output.abort()
		err = finalizeCompletePublication(
			output, path, evidence.CaptureBundle, complete, cas.ErrRootPublished,
		)
		var warning *publishedRootError
		if !errors.As(err, &warning) || !errors.Is(err, cas.ErrRootPublished) ||
			warning.RootPath != path || warning.RecoveryPath != "" {
			t.Fatalf("known-root warning = %#v, %v", warning, err)
		}
		observed, readErr := readCanonicalRoot(path)
		if readErr != nil || observed != root {
			t.Fatalf("final known root = %#v, %v", observed, readErr)
		}
	})

	t.Run("root conflict emits recovery without overwrite", func(t *testing.T) {
		directory := t.TempDir()
		path := filepath.Join(directory, "root.json")
		output, err := claimExclusiveOutput(path, 0o600, "known root")
		if err != nil {
			t.Fatal(err)
		}
		defer output.abort()
		if err := os.WriteFile(path, []byte("foreign"), 0o600); err != nil {
			t.Fatal(err)
		}
		err = finalizeCompletePublication(
			output, path, evidence.CaptureBundle, complete, nil,
		)
		var warning *publishedRootError
		if !errors.As(err, &warning) || warning.RootPath != "" ||
			warning.RecoveryPath != path+".recovery.json" {
			t.Fatalf("known-root recovery = %#v, %v", warning, err)
		}
		foreign, readErr := os.ReadFile(path)
		if readErr != nil || string(foreign) != "foreign" {
			t.Fatalf("conflicting root was replaced: %q, %v", foreign, readErr)
		}
		assertRecoveryRecord(
			t,
			warning.RecoveryPath,
			recoveryCompleteOutput,
			string(evidence.CaptureBundle),
			path,
			complete,
		)
	})

	for _, state := range []evidence.PublicationState{
		evidence.PublicationVisible,
		evidence.PublicationIndeterminate,
		evidence.PublicationRetryable,
	} {
		t.Run("incomplete "+string(state)+" has no success-shaped root", func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, "root.json")
			incomplete := evidence.PublicationResult{
				IntendedRoot:     root,
				UncertainObject:  &uncertainRoot,
				UncertainStage:   "test/" + string(state),
				State:            state,
				RecoveryRequired: true,
			}
			cause := errors.New("publication did not complete")
			err := finalizeIncompletePublication(
				path, evidence.ReplayBundle, incomplete, cause,
			)
			var recovery *publicationRecoveryError
			if !errors.As(err, &recovery) || !errors.Is(err, cause) ||
				recovery.RecoveryPath != path+".recovery.json" ||
				recovery.Result.IntendedRoot != root || recovery.Result.State != state {
				t.Fatalf("incomplete recovery = %#v, %v", recovery, err)
			}
			if _, statErr := os.Lstat(path); !errors.Is(statErr, os.ErrNotExist) {
				t.Fatalf("%s outcome created a root reference: %v", state, statErr)
			}
			assertRecoveryRecord(
				t,
				recovery.RecoveryPath,
				recoveryIncomplete,
				string(evidence.ReplayBundle),
				path,
				incomplete,
			)
		})
	}
}

func assertRecoveryRecord(
	t *testing.T,
	path, status, bundleKind, intendedRoot string,
	wantPublication evidence.PublicationResult,
) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var record publicationRecoveryRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		t.Fatal(err)
	}
	if record.SchemaVersion != recoverySchema || record.Status != status ||
		record.BundleKind != bundleKind || record.IntendedRootPath != intendedRoot ||
		!reflect.DeepEqual(record.Publication, wantPublication) {
		t.Fatalf("recovery record = %#v", record)
	}
	canonical, err := json.Marshal(record)
	if err != nil || !bytes.Equal(raw, canonical) {
		t.Fatalf("recovery record is not exact compact JSON: %v\n%s", err, raw)
	}
	if _, err := readCanonicalRoot(path); err == nil {
		t.Fatal("recovery record was accepted as a successful signed-root reference")
	}
}

func commandTestExecutable(t *testing.T, directory, name, content string) string {
	t.Helper()
	path := filepath.Join(directory, name)
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatal(err)
	}
	return path
}

func commandTestRepository(t *testing.T, directory string) (string, string, string) {
	t.Helper()
	root := filepath.Join(directory, "source")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	commandTestGit(t, root, "init", "--quiet")
	path := filepath.Join(root, "file.txt")
	if err := os.WriteFile(path, []byte("base\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	commandTestGit(t, root, "add", "file.txt")
	commandTestCommit(t, root, "base")
	base := strings.TrimSpace(commandTestGit(t, root, "rev-parse", "HEAD"))
	if err := os.WriteFile(path, []byte("head\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	commandTestGit(t, root, "add", "file.txt")
	commandTestCommit(t, root, "head")
	head := strings.TrimSpace(commandTestGit(t, root, "rev-parse", "HEAD"))
	return root, base, head
}

func commandTestCommit(t *testing.T, root, message string) {
	t.Helper()
	commandTestGit(
		t,
		root,
		"-c", "user.name=Tokenbench Test",
		"-c", "user.email=tokenbench@example.invalid",
		"commit", "--quiet", "-m", message,
	)
}

func commandTestGit(t *testing.T, root string, arguments ...string) string {
	t.Helper()
	command := exec.Command("git", append([]string{"-C", root}, arguments...)...)
	command.Env = append(
		os.Environ(),
		"GIT_CONFIG_GLOBAL="+os.DevNull,
		"GIT_CONFIG_SYSTEM="+os.DevNull,
		"GIT_CONFIG_NOSYSTEM=1",
	)
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v: %s", arguments, err, output)
	}
	return string(output)
}

func commandTestGitIdentity(t *testing.T) (string, string) {
	t.Helper()
	path, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	path, err = filepath.Abs(path)
	if err != nil {
		t.Fatal(err)
	}
	path, err = filepath.EvalSymlinks(filepath.Clean(path))
	if err != nil {
		t.Fatal(err)
	}
	digest, err := tokenbench.FileSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	return path, digest
}
