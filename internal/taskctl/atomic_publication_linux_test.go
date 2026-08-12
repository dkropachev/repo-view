//go:build linux

package taskctl

import (
	"os"
	"os/exec"
	"syscall"
	"testing"
)

const atomicPublicationUmaskHelper = "TASKCTL_ATOMIC_PUBLICATION_UMASK_HELPER"

func TestAnonymousAtomicPublicationIgnoresRestrictiveUmask(t *testing.T) {
	if os.Getenv(atomicPublicationUmaskHelper) == "1" {
		_ = syscall.Umask(0o777)
		directory, err := os.Open(os.Args[len(os.Args)-1])
		if err != nil {
			t.Fatal(err)
		}
		defer directory.Close()
		file, err := createAnonymousAtomicPublicationFile(directory)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		info, err := file.Stat()
		if err != nil {
			t.Fatal(err)
		}
		if got := info.Mode().Perm(); got != 0o600 {
			t.Fatalf("anonymous publication mode = %04o, want 0600", got)
		}
		return
	}

	directory := t.TempDir()
	command := exec.Command(os.Args[0], "-test.run=^TestAnonymousAtomicPublicationIgnoresRestrictiveUmask$", directory)
	command.Env = append(os.Environ(), atomicPublicationUmaskHelper+"=1")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("restrictive-umask helper failed: %v\n%s", err, output)
	}
}
