package processpolicy

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestResolveNativeExecutableRejectsShebangAndScriptSymlink(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	script := filepath.Join(root, "tool")
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveNativeExecutable(script); err == nil ||
		!strings.Contains(err.Error(), "native binary header") {
		t.Fatalf("shebang executable accepted: %v", err)
	}
	link := filepath.Join(root, "native-name")
	if err := os.Symlink("/bin/sh", link); err != nil {
		t.Fatal(err)
	}
	if _, err := ResolveNativeExecutable(link); err == nil ||
		!strings.Contains(err.Error(), "canonical executable is forbidden") {
		t.Fatalf("script-runtime symlink accepted: %v", err)
	}
}

func TestResolveNativeExecutableAcceptsNativeHeader(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "native-tool")
	if err := os.WriteFile(path, append([]byte{0x7f, 'E', 'L', 'F'}, make([]byte, 16)...), 0o700); err != nil {
		t.Fatal(err)
	}
	resolved, err := ResolveNativeExecutable(path)
	if err != nil || resolved != path {
		t.Fatalf("native executable resolved to %q: %v", resolved, err)
	}
}
