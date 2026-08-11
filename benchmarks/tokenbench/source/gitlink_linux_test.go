//go:build linux

package source

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestPrivilegedTreeDigestRejectsMountedGitlink(t *testing.T) {
	if os.Getenv("TOKENBENCH_REQUIRE_PRIVILEGED_TESTS") != "1" {
		t.Skip("privileged mount test is exercised by the required kernel lane")
	}
	if os.Geteuid() != 0 {
		t.Fatal("required privileged gitlink test is not running as root")
	}
	repository, gitlinkPath := newGitlinkRepository(t, "dependency", "base")
	if err := os.Mkdir(gitlinkPath, 0o700); err != nil {
		t.Fatal(err)
	}
	external := t.TempDir()
	writeFile(t, filepath.Join(external, "answer-key"), "must remain outside")
	if err := unix.Mount(external, gitlinkPath, "", unix.MS_BIND, ""); err != nil {
		t.Fatalf("create descendant bind mount: %v", err)
	}
	t.Cleanup(func() {
		if err := unix.Unmount(gitlinkPath, unix.MNT_DETACH); err != nil {
			t.Errorf("unmount gitlink fixture: %v", err)
		}
	})

	if _, err := TreeDigest(context.Background(), repository.root); err == nil ||
		!strings.Contains(err.Error(), "descendant mount") {
		t.Fatalf("mounted gitlink was accepted: %v", err)
	}
}
