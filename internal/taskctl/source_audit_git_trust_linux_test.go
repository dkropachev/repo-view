//go:build linux

package taskctl

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSourceAuditGitRunnerAcceptsRootOwnedSystemGit(t *testing.T) {
	identity := sourceAuditTestGitIdentityForPath(t, sourceAuditTestGitExecutable)
	runner, err := newSourceAuditGitRunner(identity)
	if err != nil {
		t.Fatalf("authenticate root-owned system Git: %v", err)
	}
	repository := filepath.Join(t.TempDir(), "repository")
	runSourceAuditTestGit(t, "", nil, "init", "-q", repository)
	result, err := runner.execute(
		context.Background(),
		repository,
		sourceAuditTestRepositoryInfo(t, repository),
		nil,
		"rev-parse", "--show-toplevel",
	)
	if err != nil {
		t.Fatalf("execute root-owned system Git: %v", err)
	}
	if !result.success || string(result.stdout) != repository+"\n" {
		t.Fatalf(
			"root-owned system Git result = success:%t stdout:%q stderr:%q",
			result.success,
			result.stdout,
			result.stderr,
		)
	}
}

func TestSourceAuditGitRunnerRejectsUserOwnedCopyWithCorrectDigest(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("a root test process cannot create a user-owned Git copy")
	}
	content, err := os.ReadFile(sourceAuditTestGitExecutable)
	if err != nil {
		t.Fatal(err)
	}
	copyPath := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(copyPath, content, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := inspectSourceAuditGitTrustedPath(copyPath, true); err == nil ||
		!strings.Contains(err.Error(), "must be root-owned") {
		t.Fatalf("user-owned Git executable trust error = %v", err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(content))
	if _, err := newSourceAuditGitRunner(sourceAuditGitIdentity{
		executable: copyPath,
		sha256:     digest,
	}); err == nil || (!strings.Contains(err.Error(), "must be root-owned") &&
		!strings.Contains(err.Error(), "must not be group/world writable")) {
		t.Fatalf("correctly hashed user-owned Git error = %v", err)
	}
}

func sourceAuditTestGitIdentityForPath(t *testing.T, path string) sourceAuditGitIdentity {
	t.Helper()
	content, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return sourceAuditGitIdentity{
		executable: path,
		sha256:     fmt.Sprintf("%x", sha256.Sum256(content)),
	}
}
