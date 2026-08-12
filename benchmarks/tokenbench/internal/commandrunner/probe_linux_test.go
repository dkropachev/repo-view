//go:build linux

package commandrunner

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestMain(main *testing.M) {
	if Invoked(os.Args[0], os.Getenv("PATH")) {
		os.Exit(Run(
			context.Background(), os.Args[1:], os.Stdin, os.Stdout, os.Stderr,
		))
	}
	os.Exit(main.Run())
}

func TestVerifyPinnedEntrypointExecutesDescriptorNotPath(t *testing.T) {
	image, err := os.Open("/proc/self/exe")
	if err != nil {
		t.Fatal(err)
	}
	defer image.Close()
	toolbox := filepath.Join(t.TempDir(), "toolbox")
	if err := os.Mkdir(toolbox, 0o700); err != nil {
		t.Fatal(err)
	}
	discoveryPath := filepath.Join(toolbox, "bash")
	if err := os.WriteFile(discoveryPath, []byte("not the opened image"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := VerifyPinnedEntrypoint(t.Context(), discoveryPath, image); err != nil {
		t.Fatalf("descriptor-bound entrypoint probe: %v", err)
	}
	if _, err := image.Stat(); err != nil {
		t.Fatalf("entrypoint probe closed its caller-owned image: %v", err)
	}
}

func TestVerifyPinnedEntrypointRejectsWrongImageAndPath(t *testing.T) {
	wrongImage, err := os.Open("/bin/true")
	if err != nil {
		t.Fatal(err)
	}
	defer wrongImage.Close()
	discoveryPath := filepath.Join(t.TempDir(), "bash")
	if err := VerifyPinnedEntrypoint(t.Context(), discoveryPath, wrongImage); err == nil {
		t.Fatal("entrypoint probe accepted a different native image")
	}
	image, err := os.Open("/proc/self/exe")
	if err != nil {
		t.Fatal(err)
	}
	defer image.Close()
	for _, invalid := range []string{"bash", filepath.Join(t.TempDir(), "runner")} {
		if err := VerifyPinnedEntrypoint(t.Context(), invalid, image); err == nil {
			t.Fatalf("entrypoint probe accepted invalid discovery path %q", invalid)
		}
	}
}
