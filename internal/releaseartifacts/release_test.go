package releaseartifacts

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestVersionFromRef(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		ref     string
		want    string
		wantErr bool
	}{
		{name: "semantic version", ref: "v1.2.3", want: "1.2.3"},
		{name: "prerelease and build", ref: "v1.2.3-rc.1+build.5", want: "1.2.3-rc.1+build.5"},
		{name: "missing prefix", ref: "1.2.3", wantErr: true},
		{name: "empty", ref: "v", wantErr: true},
		{name: "slash", ref: "v1/release", wantErr: true},
		{name: "missing patch", ref: "v1.2", wantErr: true},
		{name: "leading zero", ref: "v01.2.3", wantErr: true},
		{name: "numeric prerelease leading zero", ref: "v1.2.3-01", wantErr: true},
		{name: "double build separator", ref: "v1.2.3+a+b", wantErr: true},
		{name: "empty identifier", ref: "v1.2.3-rc..1", wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := versionFromRef(test.ref)
			if test.wantErr {
				if err == nil || !strings.Contains(err.Error(), "canonical v-prefixed semantic version") {
					t.Fatalf("versionFromRef(%q) error = %v", test.ref, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("versionFromRef(%q): %v", test.ref, err)
			}
			if got != test.want {
				t.Fatalf("versionFromRef(%q) = %q, want %q", test.ref, got, test.want)
			}
		})
	}
}

func TestBuildIsDeterministicClosedAndBoundToTag(t *testing.T) {
	root, commit := newReleaseRepository(t, "v1.2.3")
	if err := Build(root, "v1.2.3"); err != nil {
		t.Fatalf("first Build: %v", err)
	}
	first := filepath.Join(t.TempDir(), "dist")
	copyTree(t, filepath.Join(root, "dist"), first)
	if err := os.RemoveAll(filepath.Join(root, "dist")); err != nil {
		t.Fatal(err)
	}
	if err := Build(root, "v1.2.3"); err != nil {
		t.Fatalf("second Build: %v", err)
	}
	second := filepath.Join(root, "dist")
	assertTreesEqual(t, first, second)

	notice := []byte("notice\n")
	if err := validateArtifactSet(second, "1.2.3", commit, notice); err != nil {
		t.Fatalf("validate built artifact set: %v", err)
	}
	archive := filepath.Join(second, "scopesifter_1.2.3_linux_amd64.tar.gz")
	file, err := os.OpenFile(archive, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.Write([]byte("corruption")); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	if err := validateArtifactSet(second, "1.2.3", commit, notice); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("corrupt artifact error = %v, want checksum mismatch", err)
	}
}

func TestBuildRefusesExistingDestination(t *testing.T) {
	root, _ := newReleaseRepository(t, "v1.0.0")
	if err := os.Mkdir(filepath.Join(root, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := Build(root, "v1.0.0")
	if err == nil || !strings.Contains(err.Error(), "destination already exists") {
		t.Fatalf("Build error = %v, want existing destination refusal", err)
	}
}

func TestReleaseCheckoutRequiresMatchingTagAndCleanTrackedTree(t *testing.T) {
	root, _ := newReleaseRepository(t, "v1.0.0")
	if _, err := validateReleaseCheckout(root, "v2.0.0"); err == nil || !strings.Contains(err.Error(), "resolve release tag") {
		t.Fatalf("missing tag error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, noticeName), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := validateReleaseCheckout(root, "v1.0.0"); err == nil || !strings.Contains(err.Error(), "tracked modifications") {
		t.Fatalf("dirty checkout error = %v", err)
	}
}

func TestValidateArtifactSetRejectsSymlink(t *testing.T) {
	dist := t.TempDir()
	target := filepath.Join(t.TempDir(), "outside")
	if err := os.WriteFile(target, []byte("data"), 0o644); err != nil {
		t.Fatal(err)
	}
	name := "scopesifter_1.2.3_linux_amd64.tar.gz"
	if err := os.Symlink(target, filepath.Join(dist, name)); err != nil {
		t.Fatal(err)
	}
	if err := validateArtifactSet(dist, "1.2.3", strings.Repeat("a", 40), []byte("notice")); err == nil {
		t.Fatal("symlinked artifact unexpectedly validated")
	}
}

func TestReleaseCreateArguments(t *testing.T) {
	t.Parallel()
	got := releaseCreateArguments("v1.2.3", "/repo/dist", []string{"SHA256SUMS", "scopesifter_1.2.3_linux_amd64.tar.gz"})
	want := []string{
		"release", "create", "v1.2.3",
		"/repo/dist/SHA256SUMS", "/repo/dist/scopesifter_1.2.3_linux_amd64.tar.gz",
		"--generate-notes", "--title", "ScopeSifter v1.2.3", "--verify-tag",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("releaseCreateArguments() = %#v, want %#v", got, want)
	}
}

func TestReplaceEnvironment(t *testing.T) {
	t.Parallel()
	got := replaceEnvironment(
		[]string{"PATH=/bin", "GOOS=plan9", "GOARCH=386", "UNRELATED=value"},
		map[string]string{"GOOS": "linux", "GOARCH": "amd64", "CGO_ENABLED": "0"},
	)
	want := []string{"PATH=/bin", "UNRELATED=value", "CGO_ENABLED=0", "GOARCH=amd64", "GOOS=linux"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("replaceEnvironment() = %#v, want %#v", got, want)
	}
}

func newReleaseRepository(t *testing.T, tag string) (string, string) {
	t.Helper()
	root := t.TempDir()
	write := func(path, content string) {
		t.Helper()
		fullPath := filepath.Join(root, filepath.FromSlash(path))
		if err := os.MkdirAll(filepath.Dir(fullPath), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(fullPath, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("go.mod", "module github.com/yapless/scopesifter\n\ngo 1.26\n")
	write(noticeName, "notice\n")
	write("cmd/scopesifter/main.go", "package main\nfunc main() {}\n")
	gitRun(t, root, "init", "-q", "-b", "main")
	gitRun(t, root, "config", "user.name", "Release Test")
	gitRun(t, root, "config", "user.email", "release@example.invalid")
	gitRun(t, root, "add", ".")
	gitRun(t, root, "commit", "-q", "-m", "release fixture")
	gitRun(t, root, "tag", tag)
	commit, err := gitOutput(root, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	return root, commit
}

func gitRun(t *testing.T, root string, arguments ...string) {
	t.Helper()
	command := exec.Command("git", arguments...)
	command.Dir = root
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v: %s", strings.Join(arguments, " "), err, output)
	}
}

func copyTree(t *testing.T, source, destination string) {
	t.Helper()
	if err := os.Mkdir(destination, 0o755); err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(source)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		data, err := os.ReadFile(filepath.Join(source, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(destination, entry.Name()), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func assertTreesEqual(t *testing.T, first, second string) {
	t.Helper()
	firstEntries, err := os.ReadDir(first)
	if err != nil {
		t.Fatal(err)
	}
	secondEntries, err := os.ReadDir(second)
	if err != nil {
		t.Fatal(err)
	}
	if len(firstEntries) != len(secondEntries) {
		t.Fatalf("artifact counts differ: %d and %d", len(firstEntries), len(secondEntries))
	}
	for index, entry := range firstEntries {
		if entry.Name() != secondEntries[index].Name() {
			t.Fatalf("artifact names differ: %s and %s", entry.Name(), secondEntries[index].Name())
		}
		firstData, err := os.ReadFile(filepath.Join(first, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		secondData, err := os.ReadFile(filepath.Join(second, entry.Name()))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(firstData, secondData) {
			t.Fatalf("artifact differs across builds: %s", entry.Name())
		}
	}
}
