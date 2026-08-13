package releaseartifacts

import (
	"bytes"
	"fmt"
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

func TestReleaseCheckoutRequiresMatchingTagAndSnapshotUsesCommittedTree(t *testing.T) {
	root, commit := newReleaseRepository(t, "v1.0.0")
	if _, err := validateReleaseCheckout(root, "v2.0.0"); err == nil || !strings.Contains(err.Error(), "resolve release tag") {
		t.Fatalf("missing tag error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, noticeName), []byte("changed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	destination := t.TempDir()
	if err := materializeReleaseTree(root, destination, commit); err != nil {
		t.Fatal(err)
	}
	notice, err := os.ReadFile(filepath.Join(destination, noticeName))
	if err != nil {
		t.Fatal(err)
	}
	if string(notice) != "notice\n" {
		t.Fatalf("snapshot notice = %q, want committed bytes", notice)
	}
}

func TestReleaseSnapshotExcludesUntrackedAndIndexHiddenSource(t *testing.T) {
	for _, test := range []struct {
		name string
		flag string
	}{
		{name: "assume unchanged", flag: "--assume-unchanged"},
		{name: "skip worktree", flag: "--skip-worktree"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root, commit := newReleaseRepository(t, "v1.0.0")
			mainPath := "cmd/scopesifter/main.go"
			gitRun(t, root, "update-index", test.flag, mainPath)
			if err := os.WriteFile(filepath.Join(root, filepath.FromSlash(mainPath)), []byte("package main\nfunc main() { panic(\"worktree\") }\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "cmd/scopesifter/untracked.go"), []byte("package main\nfunc untracked() {}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			destination := t.TempDir()
			if err := materializeReleaseTree(root, destination, commit); err != nil {
				t.Fatal(err)
			}
			committed, err := os.ReadFile(filepath.Join(destination, filepath.FromSlash(mainPath)))
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Contains(committed, []byte("worktree")) {
				t.Fatalf("snapshot used index-hidden worktree bytes: %q", committed)
			}
			if _, err := os.Stat(filepath.Join(destination, "cmd/scopesifter/untracked.go")); !os.IsNotExist(err) {
				t.Fatalf("snapshot included untracked Go source: %v", err)
			}
		})
	}
}

func TestReleaseCheckoutRejectsCleanFilterBeforeItCanExecute(t *testing.T) {
	root, _ := newReleaseRepository(t, "v1.0.0")
	write := func(path, content string) {
		if err := os.WriteFile(filepath.Join(root, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(".gitattributes", "*.go filter=answer\n")
	gitRun(t, root, "add", ".gitattributes")
	gitRun(t, root, "commit", "-q", "-m", "attributes")
	gitRun(t, root, "tag", "-f", "v1.0.0")
	filter := filepath.Join(t.TempDir(), "release-filter-test")
	copyReleaseTestExecutable(t, filter)
	gitRun(t, root, "config", "filter.answer.clean", filter)
	write("cmd/scopesifter/main.go", "package main\nfunc main() { println(\"changed\") }\n")

	if _, err := validateReleaseCheckout(root, "v1.0.0"); err == nil ||
		!strings.Contains(err.Error(), "can delegate execution") {
		t.Fatalf("configured clean filter was not rejected: %v", err)
	}
	if _, err := os.Stat(filter + ".marker"); !os.IsNotExist(err) {
		t.Fatalf("configured clean filter executed: %v", err)
	}
}

func TestReleaseCheckoutRejectsWorktreeConfigDelegationAndRedirection(t *testing.T) {
	for _, name := range []string{"filter.answer.clean", "core.worktree"} {
		setting := struct {
			name  string
			value string
		}{name: name, value: t.TempDir()}
		t.Run(setting.name, func(t *testing.T) {
			root, _ := newReleaseRepository(t, "v1.0.0")
			gitRun(t, root, "config", "extensions.worktreeConfig", "true")
			gitRun(t, root, "config", "--worktree", setting.name, setting.value)
			if _, err := validateReleaseCheckout(root, "v1.0.0"); err == nil ||
				!strings.Contains(err.Error(), "can delegate execution") {
				t.Fatalf("worktree configuration %s was not rejected: %v", setting.name, err)
			}
		})
	}
}

func TestReleaseCheckoutDoesNotExecuteInitializedSubmoduleCleanFilter(t *testing.T) {
	root, _ := newReleaseRepository(t, "v1.0.0")
	submodule := filepath.Join(root, "module")
	if err := os.Mkdir(submodule, 0o755); err != nil {
		t.Fatal(err)
	}
	gitRun(t, submodule, "init", "-q", "-b", "main")
	gitRun(t, submodule, "config", "user.name", "Release Test")
	gitRun(t, submodule, "config", "user.email", "release@example.invalid")
	if err := os.WriteFile(
		filepath.Join(submodule, ".gitattributes"),
		[]byte("*.go filter=answer\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	childPath := filepath.Join(submodule, "child.go")
	if err := os.WriteFile(childPath, []byte("package child\n\nfunc initial() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, submodule, "add", ".gitattributes", "child.go")
	gitRun(t, submodule, "commit", "-q", "-m", "initial child")
	childCommit, err := gitOutput(submodule, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "update-index", "--add", "--cacheinfo", "160000,"+childCommit+",module")
	if err := os.WriteFile(filepath.Join(root, ".gitmodules"), []byte(
		"[submodule \"module\"]\n\tpath = module\n\turl = ./module\n\tignore = none\n",
	), 0o644); err != nil {
		t.Fatal(err)
	}
	gitRun(t, root, "add", ".gitmodules")
	gitRun(t, root, "commit", "-q", "-m", "add gitlink")
	gitRun(t, root, "tag", "-f", "v1.0.0")
	expectedCommit, err := gitOutput(root, "rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}

	filter := filepath.Join(t.TempDir(), "release-filter-test")
	copyReleaseTestExecutable(t, filter)
	gitRun(t, submodule, "config", "filter.answer.clean", filter)
	gitRun(t, submodule, "config", "filter.answer.required", "true")
	if err := os.WriteFile(childPath, []byte("package child\n\nfunc dirty() {}\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	destination := t.TempDir()
	err = materializeReleaseTree(root, destination, expectedCommit)
	if err == nil || !strings.Contains(err.Error(), "unsupported committed release tree entry") {
		t.Fatalf("committed gitlink was not rejected: %v", err)
	}
	if _, err := os.Stat(filter + ".marker"); !os.IsNotExist(err) {
		t.Fatalf("initialized submodule clean filter executed: %v", err)
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

func TestParseReleaseTreeRejectsUnsafeEntries(t *testing.T) {
	t.Parallel()
	oid := strings.Repeat("a", 40)
	valid := []byte("100644 blob " + oid + "\tcmd/main.go\x00")
	entries, err := parseReleaseTree(valid)
	if err != nil || len(entries) != 1 || entries[0].path != "cmd/main.go" {
		t.Fatalf("valid release tree = %#v, %v", entries, err)
	}
	for _, listing := range [][]byte{
		[]byte("100644 blob " + oid + "\tcmd/main.go"),
		[]byte("120000 blob " + oid + "\tlink\x00"),
		[]byte("160000 commit " + oid + "\tmodule\x00"),
		[]byte("100644 blob " + oid + "\t../escape\x00"),
		[]byte("100644 blob " + oid + "\tpath\\escape\x00"),
		[]byte("100644 blob " + oid + "\t.git/config\x00"),
		[]byte("100644 blob bad\tfile\x00"),
		append(bytes.Clone(valid), valid...),
	} {
		if _, err := parseReleaseTree(listing); err == nil {
			t.Errorf("unsafe release tree listing accepted: %q", listing)
		}
	}
}

func TestValidateReleaseModuleRejectsLocalReplacement(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "go.mod")
	if err := os.WriteFile(path, []byte(
		"module example.invalid/release\n\ngo 1.26.5\n\nreplace example.invalid/dependency => ../dependency\n",
	), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validateReleaseModule(path); err == nil || !strings.Contains(err.Error(), "local filesystem") {
		t.Fatalf("local module replacement was not rejected: %v", err)
	}
}

func TestOpenReleaseArtifactsPinsValidatedBytes(t *testing.T) {
	set := map[string][]byte{"artifact": []byte("reviewed bytes")}
	artifacts, err := openReleaseArtifacts(set)
	if err != nil {
		t.Fatal(err)
	}
	defer artifacts[0].file.Close()
	set["artifact"] = []byte("substitute")
	if err := verifyOpenReleaseArtifacts(artifacts); err != nil {
		t.Fatalf("sealed artifact changed after input-map mutation: %v", err)
	}
	content, err := os.ReadFile("/proc/self/fd/" + fmt.Sprint(artifacts[0].file.Fd()))
	if err != nil || string(content) != "reviewed bytes" {
		t.Fatalf("sealed artifact content = %q, %v", content, err)
	}
}

func TestOpenReleaseArtifactsRejectMutation(t *testing.T) {
	artifacts, err := openReleaseArtifacts(map[string][]byte{"artifact": []byte("reviewed bytes")})
	if err != nil {
		t.Fatal(err)
	}
	defer artifacts[0].file.Close()
	if _, err := artifacts[0].file.WriteAt([]byte("mutate"), 0); err == nil {
		t.Fatal("sealed release artifact accepted WriteAt")
	}
	if err := verifyOpenReleaseArtifacts(artifacts); err != nil {
		t.Fatalf("failed mutation changed sealed artifact: %v", err)
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

func TestReleaseBuildEnvironmentRejectsAmbientGoConfiguration(t *testing.T) {
	t.Parallel()
	got := releaseBuildEnvironment([]string{
		"PATH=/tools",
		"GOWORK=/malicious/workspace",
		"GOFLAGS=-tags=ambient",
		"GOAMD64=v4",
		"GOEXPERIMENT=arenas",
		"CGO_CFLAGS=-Dambient",
		"CC=/untrusted/compiler",
	}, target{goos: "linux", goarch: "amd64"}, "/release-cache")
	want := []string{
		"PATH=/tools",
		"CGO_ENABLED=0",
		"GO111MODULE=on",
		"GOAMD64=v1",
		"GOARCH=amd64",
		"GOARM64=v8.0",
		"GOAUTH=off",
		"GOCACHE=/release-cache/go-build-cache",
		"GOENV=off",
		"GOEXPERIMENT=",
		"GOFLAGS=-mod=readonly -trimpath -buildvcs=false",
		"GOMODCACHE=/release-cache/go-module-cache",
		"GONOPROXY=none",
		"GONOSUMDB=",
		"GOOS=linux",
		"GOPRIVATE=",
		"GOPROXY=https://proxy.golang.org",
		"GOSUMDB=sum.golang.org",
		"GOTOOLCHAIN=local",
		"GOVCS=*:off",
		"GOWORK=off",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("releaseBuildEnvironment() = %#v, want %#v", got, want)
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
	write("go.mod", "module github.com/yapless/scopesifter\n\ngo 1.26.5\n")
	write(noticeName, "notice\n")
	write(
		"cmd/scopesifter/main.go",
		"package main\nvar releaseRevision = \"development\"\nvar releaseRevisionMarker = \"scopesifter.release-revision=development\"\nfunc main() { println(releaseRevision, releaseRevisionMarker) }\n",
	)
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

func TestMain(m *testing.M) {
	executable, err := os.Executable()
	if err == nil && filepath.Base(executable) == "release-filter-test" {
		if err := os.WriteFile(executable+".marker", []byte("executed"), 0o600); err != nil {
			os.Exit(98)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func copyReleaseTestExecutable(t *testing.T, path string) {
	t.Helper()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	content, err := os.ReadFile(executable)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o700); err != nil {
		t.Fatal(err)
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
