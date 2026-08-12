package taskctl

import (
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // The test authenticates native Git SHA-1 objects.
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/yapless/scopesifter/benchmarks/tokenbench/source"
	"github.com/yapless/scopesifter/internal/gitdiffcontract"
	"github.com/yapless/scopesifter/internal/processpolicy"
)

func TestParseSourceAuditModesClassifiesEveryNonRegularMode(t *testing.T) {
	listing := bytes.Join([][]byte{
		[]byte("100644 blob 1111111111111111111111111111111111111111\tplain.txt"),
		[]byte("100755 blob 2222222222222222222222222222222222222222\ttool"),
		[]byte("120000 blob 3333333333333333333333333333333333333333\tlinked|path"),
		[]byte("160000 commit 4444444444444444444444444444444444444444\tsubmodule"),
		[]byte("040000 tree 5555555555555555555555555555555555555555\tdirectory"),
		[]byte("100644 commit 6666666666666666666666666666666666666666\twrong-type"),
		{},
	}, []byte{0})
	symlinks, gitlinks, unsupported, err := parseSourceAuditModes(listing)
	if err != nil {
		t.Fatalf("parseSourceAuditModes() error = %v", err)
	}
	if want := []sourceAuditMode{{"linked|path", "120000"}}; !reflect.DeepEqual(symlinks, want) {
		t.Fatalf("symlinks = %#v, want %#v", symlinks, want)
	}
	if want := []sourceAuditMode{{"submodule", "160000"}}; !reflect.DeepEqual(gitlinks, want) {
		t.Fatalf("gitlinks = %#v, want %#v", gitlinks, want)
	}
	wantUnsupported := []sourceAuditMode{
		{"directory", "040000/tree"},
		{"wrong-type", "100644/commit"},
	}
	if !reflect.DeepEqual(unsupported, wantUnsupported) {
		t.Fatalf("unsupported = %#v, want %#v", unsupported, wantUnsupported)
	}
	if escaped := sourceAuditByteEscape("linked|path\nnext`\\"); escaped != `linked\x7cpath\x0anext\x60\x5c` {
		t.Fatalf("markdown escape = %q", escaped)
	}
}

func TestParseSourceAuditTreeRequiresNULTerminator(t *testing.T) {
	record := []byte("100644 blob 1111111111111111111111111111111111111111\tplain.txt")
	if _, _, _, _, err := parseSourceAuditTree(record); err == nil ||
		!strings.Contains(err.Error(), "not NUL terminated") {
		t.Fatalf("parseSourceAuditTree() error = %v, want missing terminator rejection", err)
	}
}

func TestParseSourceAuditTreeRejectsBackslashPath(t *testing.T) {
	record := []byte("100644 blob 1111111111111111111111111111111111111111\ta\\b\x00")
	if _, _, _, _, err := parseSourceAuditTree(record); err == nil ||
		!strings.Contains(err.Error(), "forbidden byte") {
		t.Fatalf("parseSourceAuditTree() error = %v, want backslash rejection", err)
	}
}

func TestAuthenticateSourceAuditCommit(t *testing.T) {
	treeID := strings.Repeat("1", 40)
	validContent := []byte(
		"tree " + treeID + "\n" +
			"author A <a@example.invalid> 1700000000 +0000\n" +
			"committer A <a@example.invalid> 1700000000 +0000\n\nmessage\n",
	)
	objectID := sourceAuditTestCommitID(validContent)
	got, err := authenticateSourceAuditCommit(objectID, validContent)
	if err != nil || got != treeID {
		t.Fatalf("authenticateSourceAuditCommit() = %q, %v; want %s", got, err, treeID)
	}
	authorContainsTree := []byte(
		"tree " + treeID + "\n" +
			"author tree person <a@example.invalid> 1700000000 +0000\n" +
			"committer A <a@example.invalid> 1700000000 +0000\n\nmessage with \r allowed\n",
	)
	if got, err := authenticateSourceAuditCommit(
		sourceAuditTestCommitID(authorContainsTree),
		authorContainsTree,
	); err != nil || got != treeID {
		t.Fatalf("legitimate header/body bytes rejected: %q, %v", got, err)
	}

	tests := []struct {
		name    string
		commit  string
		content []byte
		want    string
	}{
		{"forged bytes", strings.Repeat("2", 40), validContent, "hashes to"},
		{"tree not first", "", []byte("author A <a@example.invalid> 1 +0000\ntree " + treeID + "\n\nmessage\n"), "does not start"},
		{"duplicate tree", "", []byte("tree " + treeID + "\ntree " + strings.Repeat("2", 40) + "\n\nmessage\n"), "duplicate"},
		{"missing header terminator", "", []byte("tree " + treeID + "\nauthor A <a@example.invalid> 1 +0000\n"), "duplicate or malformed"},
		{"missing author and committer headers", "", []byte("tree " + treeID + "\n\n"), "duplicate or malformed"},
		{"NUL", "", append(append([]byte(nil), validContent...), 0), "contains NUL"},
		{"oversized", "", make([]byte, sourceAuditMaximumCommit+1), "outside"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			commit := test.commit
			if commit == "" {
				commit = sourceAuditTestCommitID(test.content)
			}
			_, err := authenticateSourceAuditCommit(commit, test.content)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error = %v, want %q", err, test.want)
			}
		})
	}
}

func sourceAuditTestCommitID(content []byte) string {
	hasher := sha1.New() //nolint:gosec // The test authenticates native Git SHA-1 objects.
	_, _ = io.WriteString(hasher, "commit "+strconv.Itoa(len(content))+"\x00")
	_, _ = hasher.Write(content)
	return hex.EncodeToString(hasher.Sum(nil))
}

func TestSourceAuditDirectDigestAuthenticatesSelectedCommit(t *testing.T) {
	ctx := context.Background()
	repository := filepath.Join(t.TempDir(), "source")
	if err := os.Mkdir(repository, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(repository, sourceAuditDirectoryMode); err != nil {
		t.Fatal(err)
	}
	runSourceAuditTestGit(t, "", nil, "init", "-q", repository)
	if err := os.WriteFile(filepath.Join(repository, "payload.txt"), []byte("audited bytes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	blob := strings.TrimSpace(runSourceAuditTestGit(
		t,
		repository,
		nil,
		"hash-object", "-w", "--no-filters", "--", "payload.txt",
	))
	runSourceAuditTestGit(t, repository, nil, "update-index", "--add", "--cacheinfo", "100644", blob, "payload.txt")
	tree := strings.TrimSpace(runSourceAuditTestGit(t, repository, nil, "write-tree"))
	commit := strings.TrimSpace(runSourceAuditTestGit(t, repository, strings.NewReader("direct digest commit\n"), "commit-tree", tree))
	runSourceAuditTestGit(t, repository, nil, "update-ref", "refs/heads/main", commit)
	runSourceAuditTestGit(t, repository, nil, "symbolic-ref", "HEAD", "refs/heads/main")
	runSourceAuditTestGit(t, repository, nil, "update-ref", "refs/remotes/origin/main", commit)

	git := sourceAuditTestGitRunner(t)
	repositoryInfo := sourceAuditTestRepositoryInfo(t, repository)
	witnesses, err := ordinarySourceAuditRefWitnesses(
		ctx,
		git,
		repository,
		repositoryInfo,
		commit,
		[]sourceAuditOrdinaryRef{{
			name: "refs/remotes/origin/main", objectID: commit,
			objectType: "commit", commitID: commit,
		}},
	)
	if err != nil {
		t.Fatalf("ordinarySourceAuditRefWitnesses() error = %v", err)
	}
	if want := []string{"refs/heads/main"}; !reflect.DeepEqual(witnesses, want) {
		t.Fatalf("ordinary-ref witnesses = %#v, want %#v", witnesses, want)
	}
	unrelated := strings.TrimSpace(runSourceAuditTestGit(
		t,
		repository,
		strings.NewReader("unrelated direct digest commit\n"),
		"commit-tree",
		tree,
	))
	runSourceAuditTestGit(t, repository, nil, "update-ref", "refs/remotes/origin/main", unrelated)
	runSourceAuditTestGit(t, repository, nil, "update-ref", "refs/remotes/origin/attacker", commit)
	witnesses, err = ordinarySourceAuditRefWitnesses(
		ctx,
		git,
		repository,
		repositoryInfo,
		commit,
		[]sourceAuditOrdinaryRef{{
			name: "refs/remotes/origin/main", objectID: unrelated,
			objectType: "commit", commitID: unrelated,
		}},
	)
	if err != nil || len(witnesses) != 0 {
		t.Fatalf("mutable/new refs affected retained witnesses: %v, %v", witnesses, err)
	}
	entries, symlinks, gitlinks, unsupported, err := inspectSourceAuditTree(
		ctx, git, repository, repositoryInfo, commit,
	)
	if err != nil {
		t.Fatalf("inspectSourceAuditTree() error = %v", err)
	}
	if len(symlinks) != 0 || len(gitlinks) != 0 || len(unsupported) != 0 {
		t.Fatalf("unexpected non-regular modes: symlinks=%v gitlinks=%v unsupported=%v", symlinks, gitlinks, unsupported)
	}

	wantDigest, err := source.TreeDigest(ctx, repository)
	if err != nil {
		t.Fatalf("source.TreeDigest() error = %v", err)
	}
	status, gotDigest := digestSourceAuditTree(
		ctx, git, repository, repositoryInfo, commit, entries,
	)
	if status != "pass" || gotDigest != wantDigest {
		t.Fatalf("digestSourceAuditTree() = %s/%q, want pass/%q", status, gotDigest, wantDigest)
	}
	driftedEntries := append([]sourceAuditTreeEntry(nil), entries...)
	driftedEntries[0].path = "different.txt"
	status, detail := digestSourceAuditTree(
		ctx, git, repository, repositoryInfo, commit, driftedEntries,
	)
	if status != "reject" || !strings.Contains(detail, "reconstructed root tree ID") {
		t.Fatalf("tree-drift result = %s/%q, want reconstructed-tree rejection", status, detail)
	}
}

func TestVerifySourceAuditRepositoryObjectDatabaseRejectsCorruption(t *testing.T) {
	repository := filepath.Join(t.TempDir(), "repository")
	if err := os.Mkdir(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	runSourceAuditTestGit(t, "", nil, "init", "-q", repository)
	if err := os.WriteFile(filepath.Join(repository, "file.txt"), []byte("content\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runSourceAuditTestGit(t, repository, nil, "add", "file.txt")
	runSourceAuditTestGit(t, repository, nil, "commit", "-q", "-m", "commit")
	git := sourceAuditTestGitRunner(t)
	repositoryInfo := sourceAuditTestRepositoryInfo(t, repository)
	if err := verifySourceAuditRepositoryObjectDatabase(
		context.Background(), git, repository, repositoryInfo,
	); err != nil {
		t.Fatalf("valid repository failed fsck: %v", err)
	}
	commit := strings.TrimSpace(runSourceAuditTestGit(t, repository, nil, "rev-parse", "HEAD"))
	objectPath := filepath.Join(repository, ".git", "objects", commit[:2], commit[2:])
	if err := os.Chmod(objectPath, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(objectPath, []byte("corrupt"), 0o444); err != nil {
		t.Fatal(err)
	}
	if err := verifySourceAuditRepositoryObjectDatabase(
		context.Background(), git, repository, repositoryInfo,
	); err == nil {
		t.Fatal("corrupt repository passed fsck")
	}
}

func TestBuildSourceAuditRealMatrix(t *testing.T) {
	if os.Getenv("SCOPESIFTER_RUN_SOURCE_AUDIT_INTEGRATION") != "1" {
		t.Skip("set SCOPESIFTER_RUN_SOURCE_AUDIT_INTEGRATION=1 for the 107-state audit")
	}
	repositoryBindings := os.Getenv("SCOPESIFTER_SOURCE_AUDIT_REPOSITORY_BINDINGS")
	repositoryBindingsSHA256 := os.Getenv("SCOPESIFTER_SOURCE_AUDIT_REPOSITORY_BINDINGS_SHA256")
	sourceSelections := os.Getenv("SCOPESIFTER_SOURCE_AUDIT_SOURCE_SELECTIONS")
	sourceSelectionsSHA256 := os.Getenv("SCOPESIFTER_SOURCE_AUDIT_SOURCE_SELECTIONS_SHA256")
	reportPath := os.Getenv("SCOPESIFTER_SOURCE_AUDIT_REPORT")
	if repositoryBindings == "" || repositoryBindingsSHA256 == "" ||
		sourceSelections == "" || sourceSelectionsSHA256 == "" {
		t.Fatal("repository bindings, source selections, and their SHA-256 values are required")
	}
	git := sourceAuditTestGitRunner(t)
	got, err := BuildSourceAudit(context.Background(), SourceAuditOptions{
		RepositoryBindings:       repositoryBindings,
		RepositoryBindingsSHA256: repositoryBindingsSHA256,
		SourceSelections:         sourceSelections,
		SourceSelectionsSHA256:   sourceSelectionsSHA256,
		GitExecutable:            git.identity.executable,
		GitSHA256:                git.identity.sha256,
	})
	if err != nil {
		t.Fatalf("BuildSourceAudit() error = %v", err)
	}
	for _, required := range [][]byte{
		[]byte("- Unique-state outcomes: all 107 pass."),
		[]byte("- Cell outcomes: all 144 pass."),
		[]byte("without creating a checkout"),
	} {
		if !bytes.Contains(got, required) {
			t.Fatalf("real source-audit report lacks %q", required)
		}
	}
	if reportPath == "" {
		return
	}
	want, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, want) {
		limit := min(len(got), len(want))
		index := 0
		for index < limit && got[index] == want[index] {
			index++
		}
		start := max(0, index-80)
		end := min(limit, index+160)
		t.Fatalf(
			"BuildSourceAudit() differs from canonical report at byte %d (got length %d, want %d)\ngot:  %q\nwant: %q",
			index,
			len(got),
			len(want),
			got[start:end],
			want[start:end],
		)
	}
}

func runSourceAuditTestGit(t *testing.T, directory string, stdin *strings.Reader, arguments ...string) string {
	t.Helper()
	command, executable, err := processpolicy.NativeCommand("git", arguments...)
	if err != nil {
		t.Fatalf("pin test Git: %v", err)
	}
	command.Dir = directory
	command.Env = append(
		gitdiffcontract.Environment(os.DevNull),
		"GIT_AUTHOR_NAME=Source Audit Test",
		"GIT_AUTHOR_EMAIL=source-audit@example.invalid",
		"GIT_AUTHOR_DATE=1700000000 +0000",
		"GIT_COMMITTER_NAME=Source Audit Test",
		"GIT_COMMITTER_EMAIL=source-audit@example.invalid",
		"GIT_COMMITTER_DATE=1700000000 +0000",
	)
	if stdin != nil {
		command.Stdin = stdin
	}
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	runErr := command.Run()
	closeErr := executable.Close()
	if runErr != nil || closeErr != nil {
		t.Fatalf("git %q failed: run=%v close=%v stderr=%s", arguments, runErr, closeErr, stderr.Bytes())
	}
	return stdout.String()
}
