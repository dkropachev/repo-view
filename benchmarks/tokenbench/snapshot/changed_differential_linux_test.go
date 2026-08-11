//go:build linux

package snapshot

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"github.com/scopesifter/scopesifter/internal/gitdiffcontract"
	"github.com/scopesifter/scopesifter/navigator"
)

func TestChangedStateCacheMatchesCanonicalDirectGit(t *testing.T) {
	gitPath, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is unavailable")
	}
	gitPath, err = filepath.Abs(gitPath)
	if err != nil {
		t.Fatal(err)
	}
	gitPath, err = filepath.EvalSymlinks(gitPath)
	if err != nil {
		t.Fatal(err)
	}
	gitRaw, err := os.ReadFile(gitPath)
	if err != nil {
		t.Fatal(err)
	}
	gitSHA256 := digest(gitRaw)

	root := t.TempDir()
	differentialGit(t, gitPath, root, "init", "-q")
	differentialGit(t, gitPath, root, "config", "user.email", "contract@example.test")
	differentialGit(t, gitPath, root, "config", "user.name", "Git diff contract")
	differentialGit(t, gitPath, root, "config", "commit.gpgsign", "false")
	writeDifferentialFile(t, root, ".gitattributes", "binary.dat binary\n")
	writeDifferentialFile(t, root, "modified.go", "package fixture\n\nfunc Modified() { println(\"before\") }\n\nfunc SharedTarget() {}\n")
	writeDifferentialFile(t, root, "deleted.go", "package fixture\n\nfunc Deleted() {}\n")
	writeDifferentialFile(t, root, "unchanged.go", "package fixture\n\nfunc SharedTarget() {}\n")
	writeDifferentialFile(t, root, "old_name.go", "package fixture\n\nfunc UniquelyRenamed() {\n\tprintln(\"rename-only fixture\")\n}\n")
	writeDifferentialFile(t, root, "indent.txt", "1\n2\na\n\nb\n3\n4\n")
	writeDifferentialBytes(t, root, "binary.dat", []byte{0, 1, 2, 3, 4})
	differentialGit(t, gitPath, root, "add", ".")
	differentialGit(t, gitPath, root, "commit", "-q", "-m", "base")
	base := differentialGitText(t, gitPath, root, "rev-parse", "HEAD")

	writeDifferentialFile(t, root, "modified.go", "package fixture\n\nfunc Modified() { println(\"after\") }\n\nfunc SharedTarget() {}\n")
	writeDifferentialFile(t, root, "added.go", "package fixture\n\nfunc Added() {}\n")
	if err := os.Remove(filepath.Join(root, "deleted.go")); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(
		filepath.Join(root, "old_name.go"),
		filepath.Join(root, "renamed.go"),
	); err != nil {
		t.Fatal(err)
	}
	writeDifferentialFile(t, root, "indent.txt", "1\n2\na\n\nb\na\n\nb\n3\n4\n")
	writeDifferentialBytes(t, root, "binary.dat", []byte{0, 1, 9, 3, 4})
	differentialGit(t, gitPath, root, "add", "-A")
	differentialGit(t, gitPath, root, "commit", "-q", "-m", "head")
	head := differentialGitText(t, gitPath, root, "rev-parse", "HEAD")

	cache, raw, err := buildChangedStateCache(
		t.Context(), root, gitPath, gitSHA256, base, head,
	)
	if err != nil {
		t.Fatal(err)
	}
	assertDifferentialMetadata(t, cache)
	assertCanonicalPatchBytes(t, root, gitPath, gitSHA256, base, head, cache)
	assertIndentHeuristicFixture(t, root, gitPath, gitSHA256, base, head)

	cachePath := filepath.Join(t.TempDir(), "changed-state.json")
	if err := os.WriteFile(cachePath, raw, 0o600); err != nil {
		t.Fatal(err)
	}
	direct, err := navigator.NewWithGit(root, gitPath, gitSHA256)
	if err != nil {
		t.Fatal(err)
	}
	cached, err := navigator.NewWithChangedStateCache(
		root, cachePath, digest(raw), base, head,
	)
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name     string
		includes []string
		excludes []string
	}{
		{name: "all"},
		{name: "modified", includes: []string{"modified.go"}},
		{name: "added", includes: []string{"added.go"}},
		{name: "deleted", includes: []string{"deleted.go"}},
		{name: "renamed", includes: []string{"renamed.go"}},
		{name: "binary", includes: []string{"*.dat"}},
		{name: "indent-sensitive", includes: []string{"indent.txt"}},
		{name: "go-only", includes: []string{"*.go"}},
		{name: "include-exclude", includes: []string{"*.go"}, excludes: []string{"deleted.go", "added.go"}},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			options := navigator.Options{
				Base:          base,
				PathGlobs:     append([]string(nil), test.includes...),
				ExcludeGlobs:  append([]string(nil), test.excludes...),
				Return:        navigator.ReturnLocations,
				Context:       3,
				Limit:         10_000,
				MaxCodeLines:  20,
				MaxPatchLines: 100_000,
			}
			directResponse, err := direct.Changed(options)
			if err != nil {
				t.Fatalf("direct Git: %v", err)
			}
			cachedResponse, err := cached.Changed(options)
			if err != nil {
				t.Fatalf("changed-state cache: %v", err)
			}
			if !reflect.DeepEqual(directResponse, cachedResponse) {
				t.Fatalf(
					"direct/cache response mismatch\ndirect: %#v\ncached: %#v",
					directResponse,
					cachedResponse,
				)
			}
		})
	}
	directFind, err := direct.Find("SharedTarget", navigator.Options{
		Base: base, ChangedOnly: true, Return: navigator.ReturnLocations,
		Limit: 100, MaxCodeLines: 20, MaxPatchLines: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	cachedFind, err := cached.Find("SharedTarget", navigator.Options{
		Base: base, ChangedOnly: true, Return: navigator.ReturnLocations,
		Limit: 100, MaxCodeLines: 20, MaxPatchLines: 100,
	})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(directFind, cachedFind) || len(directFind.Results) == 0 {
		t.Fatalf("changed-only direct/cache mismatch: %#v / %#v", directFind, cachedFind)
	}
	for _, result := range directFind.Results {
		if result.Path == "unchanged.go" {
			t.Fatalf("changed-only result leaked unchanged path: %#v", result)
		}
	}
}

func assertDifferentialMetadata(t *testing.T, cache ChangedStateCache) {
	t.Helper()
	want := map[string]struct {
		status     string
		previous   string
		binary     bool
		similarity int
	}{
		"added.go":    {status: "added"},
		"binary.dat":  {status: "modified", binary: true},
		"deleted.go":  {status: "deleted"},
		"indent.txt":  {status: "modified"},
		"modified.go": {status: "modified"},
		"renamed.go":  {status: "renamed", previous: "old_name.go", similarity: 100},
	}
	if len(cache.ChangedFiles) != len(want) {
		t.Fatalf("changed files = %#v", cache.ChangedFiles)
	}
	for _, file := range cache.ChangedFiles {
		expected, ok := want[file.Path]
		if !ok || file.Status != expected.status || file.PreviousPath != expected.previous ||
			file.Binary != expected.binary || file.Similarity != expected.similarity {
			t.Fatalf("changed metadata for %q = %#v, expected %#v", file.Path, file, expected)
		}
	}
}

func assertCanonicalPatchBytes(
	t *testing.T,
	root, gitPath, gitSHA256, base, head string,
	cache ChangedStateCache,
) {
	t.Helper()
	aggregate, err := snapshotGitOutput(
		t.Context(), root, gitPath, gitSHA256, maximumChangedPatchBytes,
		gitdiffcontract.PatchArguments(base, head)...,
	)
	if err != nil {
		t.Fatal(err)
	}
	if cache.Patch != string(aggregate) {
		t.Fatal("cached aggregate patch bytes differ from the canonical Git contract")
	}
	joined := make([]string, 0, len(cache.ChangedFiles))
	for _, file := range cache.ChangedFiles {
		arguments := append(gitdiffcontract.PatchArguments(base, head), file.Path)
		patch, err := snapshotGitOutput(
			t.Context(), root, gitPath, gitSHA256, maximumPerFilePatchBytes,
			arguments...,
		)
		if err != nil {
			t.Fatal(err)
		}
		if file.Patch != string(patch) {
			t.Fatalf("cached per-file patch bytes differ for %q", file.Path)
		}
		if file.Patch != "" {
			joined = append(joined, strings.TrimRight(file.Patch, "\n"))
		}
	}
	if len(joined) == 0 {
		t.Fatal("fixture produced no per-file patch bytes")
	}
	fullIndex := regexp.MustCompile(`(?m)^index ([0-9a-f]+)\.\.([0-9a-f]+)(?: |$)`)
	matches := fullIndex.FindAllStringSubmatch(strings.Join(joined, "\n"), -1)
	if len(matches) == 0 {
		t.Fatal("canonical patches omitted index object IDs")
	}
	for _, match := range matches {
		if len(match[1]) != len(base) || len(match[2]) != len(head) {
			t.Fatalf("canonical patch abbreviated an object ID: %q", match[0])
		}
	}
}

func assertIndentHeuristicFixture(
	t *testing.T,
	root, gitPath, gitSHA256, base, head string,
) {
	t.Helper()
	canonicalArguments := append(
		gitdiffcontract.PatchArguments(base, head),
		"indent.txt",
	)
	canonical, err := snapshotGitOutput(
		t.Context(), root, gitPath, gitSHA256, maximumPerFilePatchBytes,
		canonicalArguments...,
	)
	if err != nil {
		t.Fatal(err)
	}
	indentArguments := gitdiffcontract.PatchArguments(base, head)
	for index, argument := range indentArguments {
		if argument == "--no-indent-heuristic" {
			indentArguments[index] = "--indent-heuristic"
		}
	}
	indentArguments = append(indentArguments, "indent.txt")
	indented, err := snapshotGitOutput(
		t.Context(), root, gitPath, gitSHA256, maximumPerFilePatchBytes,
		indentArguments...,
	)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(canonical, indented) {
		t.Fatal("indent-sensitive fixture did not distinguish the configured heuristic")
	}
}

func differentialGit(t *testing.T, gitPath, root string, arguments ...string) {
	t.Helper()
	command := exec.Command(gitPath, arguments...)
	command.Dir = root
	command.Env = gitdiffcontract.Environment(os.DevNull)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(arguments, " "), err, output)
	}
}

func differentialGitText(t *testing.T, gitPath, root string, arguments ...string) string {
	t.Helper()
	command := exec.Command(gitPath, arguments...)
	command.Dir = root
	command.Env = gitdiffcontract.Environment(os.DevNull)
	output, err := command.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(arguments, " "), err)
	}
	return strings.TrimSpace(string(output))
}

func writeDifferentialFile(t *testing.T, root, relative, content string) {
	t.Helper()
	writeDifferentialBytes(t, root, relative, []byte(content))
}

func writeDifferentialBytes(t *testing.T, root, relative string, content []byte) {
	t.Helper()
	path := filepath.Join(root, relative)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
}
