package taskctl

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type sourceAuditRepositoryProbeFixture struct {
	byRoot map[string]map[sourceAuditRepositoryBindingQuery][]byte
	err    map[string]map[sourceAuditRepositoryBindingQuery]error
	calls  []string
}

func TestBuildSourceAuditRepositoryBindingsIsCanonicalAndOrderIndependent(t *testing.T) {
	document, probe, paths := sourceAuditRepositoryBindingFixture(t)
	inputs := make([]SourceAuditRepositoryBindingInput, 0, len(sourceAuditRepositories))
	for index := len(sourceAuditRepositories) - 1; index >= 0; index-- {
		repository := sourceAuditRepositories[index]
		inputs = append(inputs, SourceAuditRepositoryBindingInput{
			Upstream: repository.upstream,
			Path:     paths[repository.upstream],
		})
	}
	first, err := buildSourceAuditRepositoryBindings(context.Background(), inputs, sourceAuditRepositoryTestGitIdentity(), probe)
	if err != nil {
		t.Fatalf("buildSourceAuditRepositoryBindings() error = %v", err)
	}
	secondInputs := append([]SourceAuditRepositoryBindingInput(nil), inputs...)
	for left, right := 0, len(secondInputs)-1; left < right; left, right = left+1, right-1 {
		secondInputs[left], secondInputs[right] = secondInputs[right], secondInputs[left]
	}
	second, err := buildSourceAuditRepositoryBindings(context.Background(), secondInputs, sourceAuditRepositoryTestGitIdentity(), probe)
	if err != nil {
		t.Fatalf("second buildSourceAuditRepositoryBindings() error = %v", err)
	}
	if string(first) != string(second) {
		t.Fatalf("generated binding bytes depend on input order\nfirst:  %s\nsecond: %s", first, second)
	}
	want, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	want = append(want, '\n')
	if string(first) != string(want) {
		t.Fatalf("canonical generated bindings differ\ngot:  %s\nwant: %s", first, want)
	}
	if first[len(first)-1] != '\n' || first[len(first)-2] == '\n' {
		t.Fatalf("canonical generated bindings have invalid final newline: %q", first[len(first)-min(2, len(first)):])
	}
	validated, err := validateSourceAuditRepositoryBindings(context.Background(), first, sourceAuditRepositoryTestGitIdentity(), probe)
	if err != nil {
		t.Fatalf("validate generated repository bindings: %v", err)
	}
	for upstream, path := range paths {
		if validated.paths[upstream] != path {
			t.Fatalf("validated generated path for %s = %q, want %q", upstream, validated.paths[upstream], path)
		}
	}
}

func TestBuildSourceAuditRepositoryBindingsRequiresExactUniqueInputs(t *testing.T) {
	tests := []struct {
		name string
		edit func([]SourceAuditRepositoryBindingInput) []SourceAuditRepositoryBindingInput
		want string
	}{
		{
			"missing",
			func(inputs []SourceAuditRepositoryBindingInput) []SourceAuditRepositoryBindingInput {
				return inputs[:len(inputs)-1]
			},
			"inputs are missing",
		},
		{
			"extra",
			func(inputs []SourceAuditRepositoryBindingInput) []SourceAuditRepositoryBindingInput {
				return append(inputs, SourceAuditRepositoryBindingInput{
					Upstream: "example.invalid/extra",
					Path:     "/nonexistent",
				})
			},
			"unknown upstream",
		},
		{
			"duplicate",
			func(inputs []SourceAuditRepositoryBindingInput) []SourceAuditRepositoryBindingInput {
				inputs[len(inputs)-1] = inputs[0]
				return inputs
			},
			"repeat upstream",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, probe, paths := sourceAuditRepositoryBindingFixture(t)
			inputs := make([]SourceAuditRepositoryBindingInput, 0, len(sourceAuditRepositories))
			for _, repository := range sourceAuditRepositories {
				inputs = append(inputs, SourceAuditRepositoryBindingInput{
					Upstream: repository.upstream,
					Path:     paths[repository.upstream],
				})
			}
			_, err := buildSourceAuditRepositoryBindings(
				context.Background(),
				test.edit(inputs),
				sourceAuditRepositoryTestGitIdentity(),
				probe,
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("generator error = %v, want %q", err, test.want)
			}
			if len(probe.calls) != 0 {
				t.Fatalf("invalid input triggered %d repository queries", len(probe.calls))
			}
		})
	}
}

func (probe *sourceAuditRepositoryProbeFixture) query(
	_ context.Context,
	root string,
	_ os.FileInfo,
	query sourceAuditRepositoryBindingQuery,
) ([]byte, error) {
	probe.calls = append(probe.calls, fmt.Sprintf("%s:%d", root, query))
	if err := probe.err[root][query]; err != nil {
		return nil, err
	}
	output, found := probe.byRoot[root][query]
	if !found {
		return nil, fmt.Errorf("unexpected fixture query %d for %s", query, root)
	}
	return append([]byte(nil), output...), nil
}

func TestValidateSourceAuditRepositoryBindingsAcceptsExactTwelve(t *testing.T) {
	document, probe, want := sourceAuditRepositoryBindingFixture(t)
	data := marshalSourceAuditRepositoryBindingFixture(t, document)
	got, err := validateSourceAuditRepositoryBindings(context.Background(), data, sourceAuditRepositoryTestGitIdentity(), probe)
	if err != nil {
		t.Fatalf("validateSourceAuditRepositoryBindings() error = %v", err)
	}
	if len(got.paths) != len(want) {
		t.Fatalf("repository paths = %d, want %d", len(got.paths), len(want))
	}
	for upstream, path := range want {
		if got.paths[upstream] != path {
			t.Fatalf("repository path for %s = %q, want %q", upstream, got.paths[upstream], path)
		}
	}
	if gotCalls := len(probe.calls); gotCalls != len(sourceAuditRepositories)*6 {
		t.Fatalf("probe call count = %d, want %d", gotCalls, len(sourceAuditRepositories)*6)
	}
}

func TestValidateSourceAuditRepositoryBindingsRejectsSelfSignedGitIdentity(t *testing.T) {
	document, probe, _ := sourceAuditRepositoryBindingFixture(t)
	expected := sourceAuditRepositoryTestGitIdentity()
	document.GitExecutable = "/different/git"
	_, err := validateSourceAuditRepositoryBindings(
		context.Background(),
		marshalSourceAuditRepositoryBindingFixture(t, document),
		expected,
		probe,
	)
	if err == nil || !strings.Contains(err.Error(), "want") {
		t.Fatalf("Git identity mismatch error = %v", err)
	}
	if len(probe.calls) != 0 {
		t.Fatalf("self-signed Git identity triggered %d repository queries", len(probe.calls))
	}
}

func TestValidateSourceAuditRepositoryBindingsRejectsSchemaAndSetDrift(t *testing.T) {
	tests := []struct {
		name string
		edit func(*sourceAuditRepositoryBindingDocumentV3)
		want string
	}{
		{
			"schema",
			func(document *sourceAuditRepositoryBindingDocumentV3) { document.Schema = "legacy" },
			"schema",
		},
		{
			"missing",
			func(document *sourceAuditRepositoryBindingDocumentV3) {
				document.Repositories = document.Repositories[:len(document.Repositories)-1]
			},
			"want exactly 12",
		},
		{
			"unknown",
			func(document *sourceAuditRepositoryBindingDocumentV3) {
				document.Repositories[0].Upstream = "example.invalid/repository"
			},
			"unknown upstream",
		},
		{
			"duplicate",
			func(document *sourceAuditRepositoryBindingDocumentV3) {
				document.Repositories[1].Upstream = document.Repositories[0].Upstream
			},
			"repeat upstream",
		},
		{
			"wrong binding origin",
			func(document *sourceAuditRepositoryBindingDocumentV3) {
				document.Repositories[0].Origin = "https://github.com/attacker/repository.git"
			},
			"binding origin",
		},
		{
			"noncanonical digest",
			func(document *sourceAuditRepositoryBindingDocumentV3) {
				document.Repositories[0].OrdinaryRefInventorySHA256 = strings.Repeat("A", 64)
			},
			"not lowercase SHA-256",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			document, probe, _ := sourceAuditRepositoryBindingFixture(t)
			test.edit(&document)
			_, err := validateSourceAuditRepositoryBindings(
				context.Background(),
				marshalSourceAuditRepositoryBindingFixture(t, document),
				sourceAuditRepositoryTestGitIdentity(),
				probe,
			)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validation error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestValidateSourceAuditRepositoryBindingsRejectsUnknownAndDuplicateJSONFields(t *testing.T) {
	document, probe, _ := sourceAuditRepositoryBindingFixture(t)
	data := marshalSourceAuditRepositoryBindingFixture(t, document)
	withUnknown := strings.Replace(
		string(data),
		`"schema":`,
		`"unexpected":true,"schema":`,
		1,
	)
	_, err := validateSourceAuditRepositoryBindings(
		context.Background(),
		[]byte(withUnknown),
		sourceAuditRepositoryTestGitIdentity(),
		probe,
	)
	if err == nil || !strings.Contains(err.Error(), "unknown field") {
		t.Fatalf("unknown-field error = %v", err)
	}
	withDuplicate := strings.Replace(
		string(data),
		`"schema":`,
		`"Schema":"alias","schema":`,
		1,
	)
	_, err = validateSourceAuditRepositoryBindings(
		context.Background(),
		[]byte(withDuplicate),
		sourceAuditRepositoryTestGitIdentity(),
		probe,
	)
	if err == nil || !strings.Contains(err.Error(), "case-fold duplicate") {
		t.Fatalf("duplicate-key error = %v", err)
	}
}

func TestValidateSourceAuditRepositoryBindingsRequiresCanonicalBytesAndLockedOrder(t *testing.T) {
	document, _, _ := sourceAuditRepositoryBindingFixture(t)
	canonical := marshalSourceAuditRepositoryBindingFixture(t, document)

	var compact bytes.Buffer
	if err := json.Compact(&compact, canonical); err != nil {
		t.Fatal(err)
	}
	compact.WriteByte('\n')

	type reorderedDocument struct {
		GitExecutable string                           `json:"git_executable"`
		Schema        string                           `json:"schema"`
		GitSHA256     string                           `json:"git_sha256"`
		Repositories  []sourceAuditRepositoryBindingV3 `json:"repositories"`
	}
	reorderedFields, err := json.MarshalIndent(reorderedDocument{
		GitExecutable: document.GitExecutable,
		Schema:        document.Schema,
		GitSHA256:     document.GitSHA256,
		Repositories:  document.Repositories,
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	reorderedFields = append(reorderedFields, '\n')

	for _, test := range []struct {
		name string
		data []byte
	}{
		{name: "compact JSON", data: compact.Bytes()},
		{name: "reordered fields", data: reorderedFields},
		{name: "extra whitespace", data: append(append([]byte(nil), canonical...), '\n')},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, probe, _ := sourceAuditRepositoryBindingFixture(t)
			_, err := validateSourceAuditRepositoryBindings(
				context.Background(),
				test.data,
				sourceAuditRepositoryTestGitIdentity(),
				probe,
			)
			if err == nil || !strings.Contains(err.Error(), "not canonical indented JSON") {
				t.Fatalf("canonical-byte validation error = %v", err)
			}
			if len(probe.calls) != 0 {
				t.Fatalf("noncanonical document triggered %d repository queries", len(probe.calls))
			}
		})
	}

	t.Run("reordered repositories", func(t *testing.T) {
		document, probe, _ := sourceAuditRepositoryBindingFixture(t)
		document.Repositories[0], document.Repositories[1] =
			document.Repositories[1], document.Repositories[0]
		_, err := validateSourceAuditRepositoryBindings(
			context.Background(),
			marshalSourceAuditRepositoryBindingFixture(t, document),
			sourceAuditRepositoryTestGitIdentity(),
			probe,
		)
		if err == nil || !strings.Contains(err.Error(), "locked repository order") {
			t.Fatalf("repository-order validation error = %v", err)
		}
		if len(probe.calls) != 0 {
			t.Fatalf("reordered repositories triggered %d repository queries", len(probe.calls))
		}
	})
}

func TestValidateSourceAuditRepositoryBindingsRejectsPathIdentityAndInventoryDrift(t *testing.T) {
	t.Run("relative path", func(t *testing.T) {
		document, probe, _ := sourceAuditRepositoryBindingFixture(t)
		document.Repositories[0].Path = "relative/repository"
		_, err := validateSourceAuditRepositoryBindings(
			context.Background(),
			marshalSourceAuditRepositoryBindingFixture(t, document),
			sourceAuditRepositoryTestGitIdentity(),
			probe,
		)
		if err == nil || !strings.Contains(err.Error(), "canonical absolute path") {
			t.Fatalf("relative path error = %v", err)
		}
	})

	t.Run("symlink path", func(t *testing.T) {
		document, probe, _ := sourceAuditRepositoryBindingFixture(t)
		target := document.Repositories[0].Path
		link := filepath.Join(t.TempDir(), "linked-repository")
		if err := os.Symlink(target, link); err != nil {
			t.Fatal(err)
		}
		document.Repositories[0].Path = link
		_, err := validateSourceAuditRepositoryBindings(
			context.Background(),
			marshalSourceAuditRepositoryBindingFixture(t, document),
			sourceAuditRepositoryTestGitIdentity(),
			probe,
		)
		if err == nil || !strings.Contains(err.Error(), "real directory") {
			t.Fatalf("symlink path error = %v", err)
		}
	})

	t.Run("git file", func(t *testing.T) {
		document, probe, _ := sourceAuditRepositoryBindingFixture(t)
		gitPath := filepath.Join(document.Repositories[0].Path, ".git")
		if err := os.RemoveAll(gitPath); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(gitPath, []byte("gitdir: elsewhere\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := validateSourceAuditRepositoryBindings(
			context.Background(),
			marshalSourceAuditRepositoryBindingFixture(t, document),
			sourceAuditRepositoryTestGitIdentity(),
			probe,
		)
		if err == nil || !strings.Contains(err.Error(), "standalone .git directory") {
			t.Fatalf("git-file error = %v", err)
		}
	})

	t.Run("common directory", func(t *testing.T) {
		document, probe, _ := sourceAuditRepositoryBindingFixture(t)
		commonDirectory := filepath.Join(document.Repositories[0].Path, ".git", "commondir")
		if err := os.WriteFile(commonDirectory, []byte("..\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := validateSourceAuditRepositoryBindings(
			context.Background(),
			marshalSourceAuditRepositoryBindingFixture(t, document),
			sourceAuditRepositoryTestGitIdentity(),
			probe,
		)
		if err == nil || !strings.Contains(err.Error(), "Git common directory") {
			t.Fatalf("commondir error = %v", err)
		}
	})

	t.Run("object alternate", func(t *testing.T) {
		document, probe, _ := sourceAuditRepositoryBindingFixture(t)
		info := filepath.Join(document.Repositories[0].Path, ".git", "objects", "info")
		if err := os.MkdirAll(info, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(info, "alternates"), []byte("/external\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := validateSourceAuditRepositoryBindings(
			context.Background(),
			marshalSourceAuditRepositoryBindingFixture(t, document),
			sourceAuditRepositoryTestGitIdentity(),
			probe,
		)
		if err == nil || !strings.Contains(err.Error(), "local metadata override is not allowed") {
			t.Fatalf("alternate error = %v", err)
		}
	})

	t.Run("hard-linked metadata", func(t *testing.T) {
		document, probe, _ := sourceAuditRepositoryBindingFixture(t)
		metadata := filepath.Join(document.Repositories[0].Path, ".git", "config")
		if err := os.WriteFile(metadata, []byte("metadata\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Link(metadata, filepath.Join(t.TempDir(), "metadata-alias")); err != nil {
			t.Fatal(err)
		}
		_, err := validateSourceAuditRepositoryBindings(
			context.Background(),
			marshalSourceAuditRepositoryBindingFixture(t, document),
			sourceAuditRepositoryTestGitIdentity(),
			probe,
		)
		if err == nil || !strings.Contains(err.Error(), "exactly one filesystem link") {
			t.Fatalf("hard-linked metadata error = %v", err)
		}
	})

	t.Run("nested repositories", func(t *testing.T) {
		document, probe, _ := sourceAuditRepositoryBindingFixture(t)
		parent := document.Repositories[0].Path
		nested := filepath.Join(parent, "nested")
		if err := os.MkdirAll(filepath.Join(nested, ".git", "objects"), 0o755); err != nil {
			t.Fatal(err)
		}
		oldRoot := document.Repositories[1].Path
		document.Repositories[1].Path = nested
		probe.byRoot[nested] = probe.byRoot[oldRoot]
		probe.byRoot[nested][sourceAuditRepositoryTopLevel] = []byte(nested + "\n")
		_, err := validateSourceAuditRepositoryBindings(
			context.Background(),
			marshalSourceAuditRepositoryBindingFixture(t, document),
			sourceAuditRepositoryTestGitIdentity(),
			probe,
		)
		if err == nil || !strings.Contains(err.Error(), "overlap") {
			t.Fatalf("nested-repository error = %v", err)
		}
	})

	t.Run("reported origin", func(t *testing.T) {
		document, probe, _ := sourceAuditRepositoryBindingFixture(t)
		root := document.Repositories[0].Path
		probe.byRoot[root][sourceAuditRepositoryOrigin] = []byte("https://github.com/attacker/repository.git\x00")
		_, err := validateSourceAuditRepositoryBindings(
			context.Background(),
			marshalSourceAuditRepositoryBindingFixture(t, document),
			sourceAuditRepositoryTestGitIdentity(),
			probe,
		)
		if err == nil || !strings.Contains(err.Error(), "origin is") {
			t.Fatalf("reported-origin error = %v", err)
		}
	})

	t.Run("configuration", func(t *testing.T) {
		document, probe, _ := sourceAuditRepositoryBindingFixture(t)
		root := document.Repositories[0].Path
		probe.byRoot[root][sourceAuditRepositoryConfiguration] = []byte(
			"core.hookspath\n/tmp/hooks\x00remote.origin.fetch\n+refs/heads/*:refs/remotes/origin/*\x00",
		)
		_, err := validateSourceAuditRepositoryBindings(
			context.Background(),
			marshalSourceAuditRepositoryBindingFixture(t, document),
			sourceAuditRepositoryTestGitIdentity(),
			probe,
		)
		if err == nil || !strings.Contains(err.Error(), "not admitted") {
			t.Fatalf("configuration error = %v", err)
		}
	})

	t.Run("inventory", func(t *testing.T) {
		document, probe, _ := sourceAuditRepositoryBindingFixture(t)
		root := document.Repositories[0].Path
		probe.byRoot[root][sourceAuditRepositoryOrdinaryRefs] = sourceAuditRepositoryInventoryFixture(777)
		_, err := validateSourceAuditRepositoryBindings(
			context.Background(),
			marshalSourceAuditRepositoryBindingFixture(t, document),
			sourceAuditRepositoryTestGitIdentity(),
			probe,
		)
		if err == nil || !strings.Contains(err.Error(), "inventory SHA-256") {
			t.Fatalf("inventory error = %v", err)
		}
	})

	t.Run("probe", func(t *testing.T) {
		document, probe, _ := sourceAuditRepositoryBindingFixture(t)
		root := document.Repositories[0].Path
		probe.err[root] = map[sourceAuditRepositoryBindingQuery]error{
			sourceAuditRepositoryObjectFormat: errors.New("query failed"),
		}
		_, err := validateSourceAuditRepositoryBindings(
			context.Background(),
			marshalSourceAuditRepositoryBindingFixture(t, document),
			sourceAuditRepositoryTestGitIdentity(),
			probe,
		)
		if err == nil || !strings.Contains(err.Error(), "query failed") {
			t.Fatalf("probe error = %v", err)
		}
	})
}

func TestValidateSourceAuditRepositoryPartialCloneConfigRequiresExactPair(t *testing.T) {
	base := "remote.origin.fetch\n+refs/heads/*:refs/remotes/origin/*\x00"
	tests := []struct {
		name    string
		records string
		want    string
	}{
		{name: "absent", records: base},
		{
			name: "exact pair",
			records: base + "remote.origin.promisor\ntrue\x00" +
				"remote.origin.partialclonefilter\nblob:none\x00",
		},
		{
			name:    "promisor only",
			records: base + "remote.origin.promisor\ntrue\x00",
			want:    "exactly one promisor/filter pair",
		},
		{
			name:    "filter only",
			records: base + "remote.origin.partialclonefilter\nblob:none\x00",
			want:    "exactly one promisor/filter pair",
		},
		{
			name: "duplicate promisor",
			records: base + "remote.origin.promisor\ntrue\x00" +
				"remote.origin.promisor\ntrue\x00" +
				"remote.origin.partialclonefilter\nblob:none\x00",
			want: "exactly one promisor/filter pair",
		},
		{
			name: "duplicate filter",
			records: base + "remote.origin.promisor\ntrue\x00" +
				"remote.origin.partialclonefilter\nblob:none\x00" +
				"remote.origin.partialclonefilter\nblob:none\x00",
			want: "exactly one promisor/filter pair",
		},
		{
			name: "wrong promisor",
			records: base + "remote.origin.promisor\nfalse\x00" +
				"remote.origin.partialclonefilter\nblob:none\x00",
			want: "exactly promisor=true and filter=blob:none",
		},
		{
			name: "wrong filter",
			records: base + "remote.origin.promisor\ntrue\x00" +
				"remote.origin.partialclonefilter\ntree:0\x00",
			want: "exactly promisor=true and filter=blob:none",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validateSourceAuditRepositoryPartialCloneConfig([]byte(test.records))
			if test.want == "" {
				if err != nil {
					t.Fatalf("valid partial-clone configuration rejected: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("partial-clone configuration error = %v, want %q", err, test.want)
			}
		})
	}
}

func TestNativeSourceAuditRepositoryBindingProbeReadsLocalRepository(t *testing.T) {
	git := sourceAuditTestGitRunner(t)
	repository := sourceAuditRepositories[0]
	root := filepath.Join(t.TempDir(), "repository")
	runSourceAuditTestGit(t, "", nil, "init", "-q", root)
	if err := os.WriteFile(filepath.Join(root, "README"), []byte("probe fixture\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runSourceAuditTestGit(t, root, nil, "add", "README")
	runSourceAuditTestGit(t, root, nil, "commit", "-q", "-m", "probe fixture")
	commit := strings.TrimSpace(runSourceAuditTestGit(t, root, nil, "rev-parse", "HEAD"))
	runSourceAuditTestGit(t, root, nil, "remote", "add", "origin", repository.originURL())
	runSourceAuditTestGit(t, root, nil, "config", "remote.origin.promisor", "true")
	runSourceAuditTestGit(t, root, nil, "config", "remote.origin.partialclonefilter", "blob:none")
	runSourceAuditTestGit(t, root, nil, "update-ref", "refs/remotes/origin/main", commit)

	inspection, err := inspectSourceAuditRepositoryBindingIdentity(
		context.Background(),
		nativeSourceAuditRepositoryBindingProbe{git: git},
		repository,
		root,
		sourceAuditTestRepositoryInfo(t, root),
	)
	if err != nil {
		t.Fatalf("native repository probe rejected canonical local repository: %v", err)
	}
	if len(inspection.ordinaryRefs) != 1 {
		t.Fatalf("ordinary refs = %d, want 1", len(inspection.ordinaryRefs))
	}
	ref := inspection.ordinaryRefs[0]
	if ref.name != "refs/remotes/origin/main" || ref.objectID != commit ||
		ref.objectType != "commit" || ref.commitID != commit {
		t.Fatalf("ordinary ref = %#v, want origin/main at %s", ref, commit)
	}
	if !sourceAuditDigest.MatchString(inspection.inventorySHA256) {
		t.Fatalf("inventory SHA-256 = %q", inspection.inventorySHA256)
	}
}

func TestSourceAuditOrdinaryRefInventorySHA256IsCanonicalAndStrict(t *testing.T) {
	first := sourceAuditRepositoryInventoryFixture(1)
	lines := strings.Split(strings.TrimSuffix(string(first), "\n"), "\n")
	reversed := []byte(lines[1] + "\n" + lines[0] + "\n")
	want, err := sourceAuditOrdinaryRefInventorySHA256(first)
	if err != nil {
		t.Fatal(err)
	}
	got, err := sourceAuditOrdinaryRefInventorySHA256(reversed)
	if err != nil {
		t.Fatal(err)
	}
	if got != want {
		t.Fatalf("order-independent inventory digest = %s, want %s", got, want)
	}
	canonical, err := canonicalSourceAuditOrdinaryRefInventory(reversed)
	if err != nil {
		t.Fatal(err)
	}
	expected := []byte(strings.ReplaceAll(strings.TrimSuffix(string(first), "\n"), "\n", ""))
	if string(canonical) != string(expected) {
		t.Fatalf("canonical inventory = %q, want %q", canonical, expected)
	}

	tests := []struct {
		name   string
		output []byte
		want   string
	}{
		{"empty", nil, "empty"},
		{"missing newline", first[:len(first)-1], "newline terminated"},
		{"duplicate", append(append([]byte(nil), first...), first...), "repeats"},
		{
			"local head",
			[]byte("refs/heads/main\x00" + strings.Repeat("1", 40) + "\x00commit\x00\x00\x00\n"),
			"outside origin heads and tags",
		},
		{
			"upper object id",
			[]byte("refs/tags/v1\x00" + strings.Repeat("A", 40) + "\x00commit\x00\x00\x00\n"),
			"non-SHA-1",
		},
		{
			"bad object type",
			[]byte("refs/tags/v1\x00" + strings.Repeat("1", 40) + "\x00unknown\x00\x00\x00\n"),
			"invalid object type",
		},
		{
			"remote blob",
			[]byte("refs/remotes/origin/main\x00" + strings.Repeat("1", 40) + "\x00blob\x00\x00\x00\n"),
			"want commit",
		},
		{
			"invalid ref component",
			[]byte("refs/tags/.hidden\x00" + strings.Repeat("1", 40) + "\x00commit\x00\x00\x00\n"),
			"not canonical",
		},
		{
			"annotated tag without peeled commit",
			[]byte("refs/tags/v1\x00" + strings.Repeat("1", 40) + "\x00tag\x00\x00\x00\n"),
			"does not peel directly to a commit",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := sourceAuditOrdinaryRefInventorySHA256(test.output)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("inventory error = %v, want %q", err, test.want)
			}
		})
	}
}

func sourceAuditRepositoryBindingFixture(
	t *testing.T,
) (
	sourceAuditRepositoryBindingDocumentV3,
	*sourceAuditRepositoryProbeFixture,
	map[string]string,
) {
	t.Helper()
	root := t.TempDir()
	document := sourceAuditRepositoryBindingDocumentV3{
		Schema:        sourceAuditRepositoryBindingSchemaV3,
		GitExecutable: sourceAuditRepositoryTestGitIdentity().executable,
		GitSHA256:     sourceAuditRepositoryTestGitIdentity().sha256,
		Repositories:  make([]sourceAuditRepositoryBindingV3, 0, len(sourceAuditRepositories)),
	}
	probe := &sourceAuditRepositoryProbeFixture{
		byRoot: make(map[string]map[sourceAuditRepositoryBindingQuery][]byte),
		err:    make(map[string]map[sourceAuditRepositoryBindingQuery]error),
	}
	want := make(map[string]string, len(sourceAuditRepositories))
	for index, repository := range sourceAuditRepositories {
		repositoryRoot := filepath.Join(root, fmt.Sprintf("repository-%02d", index))
		if err := os.MkdirAll(filepath.Join(repositoryRoot, ".git", "objects"), 0o755); err != nil {
			t.Fatal(err)
		}
		inventory := sourceAuditRepositoryInventoryFixture(index + 1)
		inventoryDigest, err := sourceAuditOrdinaryRefInventorySHA256(inventory)
		if err != nil {
			t.Fatal(err)
		}
		document.Repositories = append(document.Repositories, sourceAuditRepositoryBindingV3{
			Upstream:                   repository.upstream,
			Path:                       repositoryRoot,
			Origin:                     repository.originURL(),
			OrdinaryRefInventorySHA256: inventoryDigest,
		})
		probe.byRoot[repositoryRoot] = map[sourceAuditRepositoryBindingQuery][]byte{
			sourceAuditRepositoryTopLevel:     []byte(repositoryRoot + "\n"),
			sourceAuditRepositoryGitDirectory: []byte(".git\n"),
			sourceAuditRepositoryObjectFormat: []byte("sha1\n"),
			sourceAuditRepositoryConfiguration: []byte(
				"core.filemode\ntrue\x00remote.origin.url\n" + repository.originURL() +
					"\x00remote.origin.fetch\n+refs/heads/*:refs/remotes/origin/*\x00",
			),
			sourceAuditRepositoryOrigin:       []byte(repository.originURL() + "\x00"),
			sourceAuditRepositoryOrdinaryRefs: inventory,
		}
		want[repository.upstream] = repositoryRoot
	}
	return document, probe, want
}

func sourceAuditRepositoryInventoryFixture(sequence int) []byte {
	return []byte(fmt.Sprintf(
		"refs/remotes/origin/main\x00%040x\x00commit\x00\x00\x00\n"+
			"refs/tags/v1\x00%040x\x00tag\x00%040x\x00commit\x00\n",
		sequence,
		sequence+100,
		sequence+200,
	))
}

func marshalSourceAuditRepositoryBindingFixture(
	t *testing.T,
	document sourceAuditRepositoryBindingDocumentV3,
) []byte {
	t.Helper()
	type binding struct {
		Upstream                   string `json:"upstream"`
		Path                       string `json:"path"`
		Origin                     string `json:"origin"`
		OrdinaryRefInventorySHA256 string `json:"ordinary_ref_inventory_sha256"`
	}
	type schema struct {
		Schema        string    `json:"schema"`
		GitExecutable string    `json:"git_executable"`
		GitSHA256     string    `json:"git_sha256"`
		Repositories  []binding `json:"repositories"`
	}
	encoded := schema{
		Schema: document.Schema, GitExecutable: document.GitExecutable, GitSHA256: document.GitSHA256,
	}
	for _, repository := range document.Repositories {
		encoded.Repositories = append(encoded.Repositories, binding(repository))
	}
	data, err := json.MarshalIndent(encoded, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return append(data, '\n')
}

func sourceAuditRepositoryTestGitIdentity() sourceAuditGitIdentity {
	return sourceAuditGitIdentity{executable: "/usr/bin/git", sha256: strings.Repeat("0", 64)}
}
