package gitdiffcontract

import (
	"reflect"
	"slices"
	"testing"
)

func TestInvocationPrefixIgnoresOnlySubmoduleWorktreeDirtiness(t *testing.T) {
	t.Parallel()
	prefix := InvocationPrefix()
	index := slices.Index(prefix, "diff.ignoreSubmodules=dirty")
	if index < 1 || prefix[index-1] != "-c" {
		t.Fatalf("InvocationPrefix() omits exact -c diff.ignoreSubmodules=dirty pair: %#v", prefix)
	}
	if Version != "scopesifter.git-diff/v3" {
		t.Fatalf("Version = %q, want semantic contract v3", Version)
	}
}

func TestPatchArgumentsFixSemanticOptionsAndContext(t *testing.T) {
	base := "1111111111111111111111111111111111111111"
	head := "2222222222222222222222222222222222222222"
	for _, test := range []struct {
		name         string
		contextLines string
		arguments    []string
	}{
		{name: "returned patch", contextLines: "3", arguments: PatchArguments(base, head)},
		{name: "changed lines", contextLines: "0", arguments: ChangedLineArguments(base, head)},
	} {
		t.Run(test.name, func(t *testing.T) {
			arguments := test.arguments
			for _, required := range []string{
				"--find-renames=50%", "-l20000", "--diff-algorithm=myers",
				"--ignore-submodules=dirty", "--no-indent-heuristic", "--full-index", "--src-prefix=a/",
				"--dst-prefix=b/", "--inter-hunk-context=0",
			} {
				if !contains(arguments, required) {
					t.Fatalf("arguments omit %q: %#v", required, arguments)
				}
			}
			if !contains(arguments, "--unified="+test.contextLines) {
				t.Fatalf("arguments = %#v", arguments)
			}
			if got := arguments[len(arguments)-2:]; !reflect.DeepEqual(got, []string{base + "..." + head, "--"}) {
				t.Fatalf("revision suffix = %#v", got)
			}
		})
	}
}

func TestMetadataArgumentsShareRenameContract(t *testing.T) {
	base := "base"
	head := "head"
	for _, test := range []struct {
		name      string
		format    string
		arguments []string
	}{
		{name: "status", format: "--name-status", arguments: NameStatusArguments(base, head)},
		{name: "paths", format: "--name-only", arguments: NameOnlyArguments(base, head)},
		{name: "binary", format: "--numstat", arguments: NumstatArguments(base, head)},
	} {
		t.Run(test.name, func(t *testing.T) {
			for _, required := range []string{
				test.format, "-z", "--ignore-submodules=dirty", "--find-renames=50%", "-l20000",
			} {
				if !contains(test.arguments, required) {
					t.Fatalf("metadata arguments omit %q: %#v", required, test.arguments)
				}
			}
			if got := test.arguments[len(test.arguments)-2:]; !reflect.DeepEqual(got, []string{base + "..." + head, "--"}) {
				t.Fatalf("revision suffix = %#v", got)
			}
		})
	}
}

func TestParseChangedSpansMergesAndAnchorsDeletion(t *testing.T) {
	patch := []byte("@@ -1,0 +2,2 @@\n@@ -5,1 +4,0 @@\n@@ -9,1 +4,2 @@\n")
	spans, err := ParseChangedSpans(patch, 100, 1_000)
	if err != nil {
		t.Fatal(err)
	}
	want := []LineSpan{{Start: 2, End: 5}}
	if !reflect.DeepEqual(spans, want) {
		t.Fatalf("spans = %+v, want %+v", spans, want)
	}
	if _, err := ParseChangedSpans(patch, 0, 1_000); err == nil {
		t.Fatal("accepted an unbounded parser configuration")
	}
}

func contains(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
