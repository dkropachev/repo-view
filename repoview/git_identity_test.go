package repoview

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestIsolatedGitEnvironmentDoesNotInheritAmbientValues(t *testing.T) {
	t.Setenv("PATH", "/ambient/path")
	t.Setenv("HOME", "/ambient/home")
	t.Setenv("LD_PRELOAD", "/ambient/library")
	t.Setenv("GIT_DIR", "/ambient/git")
	t.Setenv("GIT_CONFIG_COUNT", "1")

	got := make(map[string]string)
	for _, entry := range isolatedGitEnvironment() {
		name, value, ok := strings.Cut(entry, "=")
		if !ok || name == "" {
			t.Fatalf("invalid environment entry %q", entry)
		}
		if _, duplicate := got[name]; duplicate {
			t.Fatalf("duplicate environment entry %q", name)
		}
		got[name] = value
	}
	for _, forbidden := range []string{
		"PATH", "HOME", "LD_PRELOAD", "GIT_DIR", "GIT_CONFIG_COUNT",
	} {
		if _, ok := got[forbidden]; ok {
			t.Fatalf("inherited forbidden environment variable %s", forbidden)
		}
	}
	for _, required := range []string{
		"GIT_ATTR_NOSYSTEM",
		"GIT_CONFIG_GLOBAL",
		"GIT_CONFIG_SYSTEM",
		"GIT_CONFIG_NOSYSTEM",
		"GIT_LITERAL_PATHSPECS",
		"GIT_NO_REPLACE_OBJECTS",
		"GIT_OPTIONAL_LOCKS",
		"GIT_PAGER",
		"GIT_TERMINAL_PROMPT",
		"LANG",
		"LC_ALL",
		"TZ",
	} {
		if _, ok := got[required]; !ok {
			t.Fatalf("missing deterministic environment variable %s", required)
		}
	}
	wantCount := 12
	if runtime.GOOS == "windows" && os.Getenv("SystemRoot") != "" {
		wantCount++
	}
	if len(got) != wantCount {
		t.Fatalf("git environment has %d entries, want %d: %#v", len(got), wantCount, got)
	}
}

func TestPinnedGitCommandExecutesOpenedInodeAfterPathReplacement(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("descriptor-path execution is Unix-specific")
	}
	original, err := exec.LookPath("git")
	if err != nil {
		t.Fatal(err)
	}
	original, err = filepath.EvalSymlinks(original)
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(original)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "git")
	if err := os.WriteFile(path, content, 0o700); err != nil {
		t.Fatal(err)
	}
	digest, _, err := stableExecutableSHA256(path)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := newGitExecutableIdentity(path, digest)
	if err != nil {
		t.Fatal(err)
	}
	command, pinned, err := identity.command("--version")
	if err != nil {
		t.Fatal(err)
	}
	defer pinned.Close()
	if err := os.Rename(path, path+".verified"); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("not the verified Git executable\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	output, err := command.Output()
	if err != nil {
		t.Fatalf("execute pinned Git descriptor: %v", err)
	}
	if !strings.HasPrefix(string(output), "git version ") {
		t.Fatalf("opened descriptor executed unexpected bytes: %q", output)
	}
	if err := identity.verify(); err == nil {
		t.Fatal("post-invocation path replacement was not detected")
	}
}
