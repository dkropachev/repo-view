//go:build linux

package workspace

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCaptureGitBuildsDeterministicBinaryPatchAndRoundTrips(t *testing.T) {
	t.Parallel()
	fixture := newCaptureGitFixture(t, "sha1")
	defer fixture.close()

	if err := os.WriteFile(
		filepath.Join(fixture.worktreePath, "README"),
		[]byte("changed\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(fixture.worktreePath, "deleted.txt")); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(fixture.worktreePath, "binary.bin"),
		[]byte{0, 1, 2, 3, 0xff, 0xfe, 0, 4},
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(fixture.worktreePath, "script.sh"), 0o644); err != nil {
		t.Fatal(err)
	}
	oddPath := filepath.Join(fixture.worktreePath, "tab\tline\nname.txt")
	if err := os.WriteFile(oddPath, []byte("odd\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	result, err := scanCapturedWorktree(
		context.Background(),
		fixture.worktree,
		fixture.baseManifest,
		fixture.limits,
	)
	if err != nil {
		t.Fatal(err)
	}
	git := fixture.prepare(t)
	baseTree, err := git.baseTree(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := git.readTree(context.Background(), captureResultIndex, fixture.baseRevision); err != nil {
		t.Fatal(err)
	}
	files, err := git.hashResultFiles(context.Background(), fixture.baseManifest, result)
	if err != nil {
		t.Fatal(err)
	}
	if err := git.updateResultIndex(context.Background(), fixture.baseManifest, result, files); err != nil {
		t.Fatal(err)
	}
	resultTree, err := git.writeTree(context.Background(), captureResultIndex)
	if err != nil {
		t.Fatal(err)
	}
	if resultTree == baseTree {
		t.Fatal("mutated worktree produced the base tree")
	}
	numstat, err := git.diffOutput(
		context.Background(),
		fixture.limits.MaximumPatchBytes,
		"--numstat",
		"-z",
	)
	if err != nil {
		t.Fatal(err)
	}
	changedFiles, changedLines, err := parseCaptureNumstat(numstat, fixture.limits)
	if err != nil {
		t.Fatal(err)
	}
	if changedFiles != 5 || changedLines < 2 {
		t.Fatalf("change totals = files:%d lines:%d", changedFiles, changedLines)
	}
	patch, err := git.diffOutput(
		context.Background(),
		fixture.limits.MaximumPatchBytes,
		"--binary",
		"--full-index",
		"--unified=3",
		"--diff-algorithm=myers",
		"--no-indent-heuristic",
		"--inter-hunk-context=0",
		"--src-prefix=a/",
		"--dst-prefix=b/",
	)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(patch, []byte("GIT binary patch\n")) {
		t.Fatal("capture omitted the binary patch body")
	}
	if err := git.verifyPatchRoundTrip(context.Background(), patch, resultTree); err != nil {
		t.Fatal(err)
	}
	second, err := git.diffOutput(
		context.Background(),
		fixture.limits.MaximumPatchBytes,
		"--binary",
		"--full-index",
		"--unified=3",
		"--diff-algorithm=myers",
		"--no-indent-heuristic",
		"--inter-hunk-context=0",
		"--src-prefix=a/",
		"--dst-prefix=b/",
	)
	if err != nil || !bytes.Equal(second, patch) {
		t.Fatalf("capture patch is not deterministic: %v", err)
	}
}

func TestCaptureGitPreservesOpaqueGitlinkForUnchangedWorktree(t *testing.T) {
	t.Parallel()
	fixture := newCaptureGitFixture(t, "sha1")
	defer fixture.close()
	result, err := scanCapturedWorktree(
		context.Background(),
		fixture.worktree,
		fixture.baseManifest,
		fixture.limits,
	)
	if err != nil {
		t.Fatal(err)
	}
	git := fixture.prepare(t)
	baseTree, err := git.baseTree(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := git.readTree(context.Background(), captureResultIndex, fixture.baseRevision); err != nil {
		t.Fatal(err)
	}
	files, err := git.hashResultFiles(context.Background(), fixture.baseManifest, result)
	if err != nil {
		t.Fatal(err)
	}
	if err := git.updateResultIndex(context.Background(), fixture.baseManifest, result, files); err != nil {
		t.Fatal(err)
	}
	resultTree, err := git.writeTree(context.Background(), captureResultIndex)
	if err != nil {
		t.Fatal(err)
	}
	if resultTree != baseTree {
		t.Fatalf("unchanged physical tree lost opaque gitlink: got %s want %s", resultTree, baseTree)
	}
	if err := git.verifyEmptyRoundTrip(context.Background(), baseTree); err != nil {
		t.Fatal(err)
	}
}

func TestCaptureGitSupportsSHA256Repository(t *testing.T) {
	t.Parallel()
	fixture := newCaptureGitFixture(t, "sha256")
	defer fixture.close()
	result, err := scanCapturedWorktree(
		context.Background(),
		fixture.worktree,
		fixture.baseManifest,
		fixture.limits,
	)
	if err != nil {
		t.Fatal(err)
	}
	git := fixture.prepare(t)
	if git.objectIDLength != 64 {
		t.Fatalf("SHA-256 object ID length = %d", git.objectIDLength)
	}
	baseTree, err := git.baseTree(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := git.readTree(context.Background(), captureResultIndex, fixture.baseRevision); err != nil {
		t.Fatal(err)
	}
	files, err := git.hashResultFiles(context.Background(), fixture.baseManifest, result)
	if err != nil {
		t.Fatal(err)
	}
	if err := git.updateResultIndex(context.Background(), fixture.baseManifest, result, files); err != nil {
		t.Fatal(err)
	}
	resultTree, err := git.writeTree(context.Background(), captureResultIndex)
	if err != nil {
		t.Fatal(err)
	}
	if resultTree != baseTree || len(resultTree) != 64 {
		t.Fatalf("SHA-256 unchanged trees differ: got %s want %s", resultTree, baseTree)
	}
}

func TestCaptureGitCanReplaceOpaqueGitlinkWithFiles(t *testing.T) {
	t.Parallel()
	fixture := newCaptureGitFixture(t, "sha1")
	defer fixture.close()
	dependency := filepath.Join(fixture.worktreePath, "vendor", "dependency")
	if err := os.MkdirAll(dependency, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dependency, "file.txt"), []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	result, err := scanCapturedWorktree(
		context.Background(),
		fixture.worktree,
		fixture.baseManifest,
		fixture.limits,
	)
	if err != nil {
		t.Fatal(err)
	}
	git := fixture.prepare(t)
	baseTree, err := git.baseTree(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if err := git.readTree(context.Background(), captureResultIndex, fixture.baseRevision); err != nil {
		t.Fatal(err)
	}
	files, err := git.hashResultFiles(context.Background(), fixture.baseManifest, result)
	if err != nil {
		t.Fatal(err)
	}
	if err := git.updateResultIndex(context.Background(), fixture.baseManifest, result, files); err != nil {
		t.Fatal(err)
	}
	resultTree, err := git.writeTree(context.Background(), captureResultIndex)
	if err != nil {
		t.Fatal(err)
	}
	if resultTree == baseTree {
		t.Fatal("materialized files did not replace the opaque gitlink")
	}
	patch, err := git.diffOutput(
		context.Background(),
		fixture.limits.MaximumPatchBytes,
		"--binary",
		"--full-index",
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := git.verifyPatchRoundTrip(context.Background(), patch, resultTree); err != nil {
		t.Fatal(err)
	}
}

func TestCaptureGitDiffOutputEnforcesExactBound(t *testing.T) {
	t.Parallel()
	fixture := newCaptureGitFixture(t, "sha1")
	defer fixture.close()
	if err := os.WriteFile(
		filepath.Join(fixture.worktreePath, "README"),
		[]byte("changed\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	result, err := scanCapturedWorktree(
		context.Background(),
		fixture.worktree,
		fixture.baseManifest,
		fixture.limits,
	)
	if err != nil {
		t.Fatal(err)
	}
	git := fixture.prepare(t)
	if err := git.readTree(context.Background(), captureResultIndex, fixture.baseRevision); err != nil {
		t.Fatal(err)
	}
	files, err := git.hashResultFiles(context.Background(), fixture.baseManifest, result)
	if err != nil {
		t.Fatal(err)
	}
	if err := git.updateResultIndex(context.Background(), fixture.baseManifest, result, files); err != nil {
		t.Fatal(err)
	}
	if _, err := git.diffOutput(
		context.Background(),
		1,
		"--binary",
		"--full-index",
	); !errors.Is(err, errCaptureOutputLimit) {
		t.Fatalf("one-byte diff limit error = %v, want output-limit classification", err)
	}
}

func TestParseCaptureNumstat(t *testing.T) {
	t.Parallel()
	limits := Limits{
		MaximumUpperBytes: 1 << 20, MaximumEntries: 8, MaximumFileBytes: 1 << 20,
		MaximumPatchBytes: 10, MaximumChangedFiles: 2,
	}
	tests := []struct {
		name       string
		content    []byte
		limits     Limits
		wantFiles  int
		wantLines  int
		wantLimit  bool
		wantReject bool
	}{
		{
			name: "text and binary", content: []byte("2\t3\ta.txt\x00-\t-\tb.bin\x00"),
			limits: limits, wantFiles: 2, wantLines: 5,
		},
		{name: "missing terminator", content: []byte("1\t0\ta"), limits: limits, wantReject: true},
		{name: "empty", content: nil, limits: limits, wantReject: true},
		{name: "missing fields", content: []byte("1\ta\x00"), limits: limits, wantReject: true},
		{name: "empty path", content: []byte("1\t0\t\x00"), limits: limits, wantReject: true},
		{name: "absolute path", content: []byte("1\t0\t/a\x00"), limits: limits, wantReject: true},
		{name: "duplicate path", content: []byte("1\t0\ta\x002\t0\ta\x00"), limits: limits, wantReject: true},
		{name: "mixed binary marker", content: []byte("-\t1\ta\x00"), limits: limits, wantReject: true},
		{name: "leading zero", content: []byte("01\t0\ta\x00"), limits: limits, wantReject: true},
		{name: "negative count", content: []byte("-1\t0\ta\x00"), limits: limits, wantReject: true},
		{name: "count overflow", content: []byte("2147483648\t0\ta\x00"), limits: limits, wantReject: true},
		{
			name: "file limit", content: []byte("1\t0\ta\x001\t0\tb\x00"),
			limits:    func() Limits { value := limits; value.MaximumChangedFiles = 1; return value }(),
			wantLimit: true,
		},
		{name: "line limit", content: []byte("6\t5\ta\x00"), limits: limits, wantLimit: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			files, lines, err := parseCaptureNumstat(test.content, test.limits)
			switch {
			case test.wantLimit && !errors.Is(err, errCaptureOutputLimit):
				t.Fatalf("error = %v, want output-limit classification", err)
			case test.wantReject && err == nil:
				t.Fatal("malformed numstat was accepted")
			case !test.wantLimit && !test.wantReject && err != nil:
				t.Fatal(err)
			case err == nil && (files != test.wantFiles || lines != test.wantLines):
				t.Fatalf("totals = files:%d lines:%d, want files:%d lines:%d", files, lines, test.wantFiles, test.wantLines)
			}
		})
	}
}

func TestCaptureBoundedBuffer(t *testing.T) {
	t.Parallel()
	buffer := &captureBoundedBuffer{limit: 3}
	if written, err := buffer.Write([]byte("ab")); written != 2 || err != nil {
		t.Fatalf("first write = %d, %v", written, err)
	}
	if written, err := buffer.Write([]byte("cd")); written != 1 || !errors.Is(err, errCaptureOutputLimit) {
		t.Fatalf("bounded write = %d, %v", written, err)
	}
	if !buffer.exceeded || !bytes.Equal(buffer.Bytes(), []byte("abc")) {
		t.Fatalf("bounded buffer = %q, exceeded=%v", buffer.Bytes(), buffer.exceeded)
	}
}

type captureGitFixture struct {
	pair         *PairAuthority
	arm          *ArmAuthority
	worktree     *os.File
	worktreePath string
	baseRevision string
	baseManifest []worktreeEntry
	limits       Limits
	ownedFiles   []*os.File
}

func newCaptureGitFixture(t *testing.T, objectFormat string) *captureGitFixture {
	t.Helper()
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	gitPath, err = filepath.EvalSymlinks(gitPath)
	if err != nil {
		t.Fatal(err)
	}
	repository := filepath.Join(t.TempDir(), "base")
	runFixtureGit(t, "", gitPath, "init", "--quiet", "--object-format="+objectFormat, repository)
	for path, content := range map[string][]byte{
		".gitattributes": []byte("*.bin binary\n"),
		"README":         []byte("base\n"),
		"binary.bin":     {0, 1, 2, 0, 3},
		"deleted.txt":    []byte("delete me\n"),
		"script.sh":      []byte("#!/bin/sh\nexit 0\n"),
	} {
		mode := os.FileMode(0o644)
		if path == "script.sh" {
			mode = 0o755
		}
		if err := os.WriteFile(filepath.Join(repository, path), content, mode); err != nil {
			t.Fatal(err)
		}
	}
	runFixtureGit(t, repository, gitPath, "add", "--", ".")
	objectIDLength := 40
	if objectFormat == "sha256" {
		objectIDLength = 64
	}
	runFixtureGit(
		t,
		repository,
		gitPath,
		"update-index",
		"--add",
		"--cacheinfo",
		"160000,"+strings.Repeat("1", objectIDLength)+",vendor/dependency",
	)
	runFixtureGit(t, repository, gitPath, "-c", "user.name=Tokenbench", "-c", "user.email=tokenbench@example.invalid", "commit", "--quiet", "-m", "base")
	baseRevision := strings.TrimSpace(runFixtureGit(t, repository, gitPath, "rev-parse", "HEAD"))

	worktreePath := filepath.Join(t.TempDir(), "worktree")
	if err := os.Mkdir(worktreePath, 0o700); err != nil {
		t.Fatal(err)
	}
	for path, mode := range map[string]os.FileMode{
		".gitattributes": 0o644,
		"README":         0o644,
		"binary.bin":     0o644,
		"deleted.txt":    0o644,
		"script.sh":      0o755,
	} {
		content, err := os.ReadFile(filepath.Join(repository, path))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(worktreePath, path), content, mode); err != nil {
			t.Fatal(err)
		}
	}
	worktree, err := os.Open(worktreePath)
	if err != nil {
		t.Fatal(err)
	}
	limits := Limits{
		MaximumUpperBytes:   64 << 20,
		MaximumEntries:      1_024,
		MaximumFileBytes:    8 << 20,
		MaximumPatchBytes:   4 << 20,
		MaximumChangedFiles: 128,
	}
	baseManifest, err := scanCapturedWorktree(
		context.Background(),
		worktree,
		nil,
		limits,
	)
	if err != nil {
		t.Fatal(err)
	}
	gitExecutable, err := os.Open(gitPath)
	if err != nil {
		t.Fatal(err)
	}
	gitInfo, err := gitExecutable.Stat()
	if err != nil {
		t.Fatal(err)
	}
	objects, err := os.Open(filepath.Join(repository, ".git", "objects"))
	if err != nil {
		t.Fatal(err)
	}
	objectsInfo, err := objects.Stat()
	if err != nil {
		t.Fatal(err)
	}
	capturePath := filepath.Join(t.TempDir(), "capture")
	if err := os.Mkdir(capturePath, 0o700); err != nil {
		t.Fatal(err)
	}
	capture, err := os.Open(capturePath)
	if err != nil {
		t.Fatal(err)
	}
	pair := &PairAuthority{
		verifierGit:  gitExecutable,
		gitObjects:   objects,
		verifierInfo: gitInfo,
		objectsInfo:  objectsInfo,
		baseRevision: baseRevision,
		baseManifest: baseManifest,
		inputs:       Inputs{Limits: limits},
	}
	arm := &ArmAuthority{
		pair: pair, capture: capture, overlayRoot: worktree,
	}
	return &captureGitFixture{
		pair: pair, arm: arm, worktree: worktree, worktreePath: worktreePath,
		baseRevision: baseRevision, baseManifest: baseManifest, limits: limits,
		ownedFiles: []*os.File{capture, objects, gitExecutable, worktree},
	}
}

func (fixture *captureGitFixture) prepare(t *testing.T) *captureGitRunner {
	t.Helper()
	git, err := fixture.arm.prepareCaptureGit()
	if err != nil {
		t.Fatal(err)
	}
	fixture.ownedFiles = append(fixture.ownedFiles, git.directory)
	return git
}

func (fixture *captureGitFixture) close() {
	for index := len(fixture.ownedFiles) - 1; index >= 0; index-- {
		_ = fixture.ownedFiles[index].Close()
	}
}

func runFixtureGit(t *testing.T, directory, gitPath string, arguments ...string) string {
	t.Helper()
	command := exec.Command(gitPath, arguments...)
	command.Dir = directory
	command.Env = []string{
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_TERMINAL_PROMPT=0",
		"LANG=C",
		"LC_ALL=C",
		"TZ=UTC",
	}
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(arguments, " "), err, output)
	}
	return string(output)
}
