package taskctl

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestValidatePublicationPathRejectsUnsafeDestinations(t *testing.T) {
	root := t.TempDir()
	inputDirectory := filepath.Join(root, "input")
	outputDirectory := filepath.Join(root, "output")
	if err := os.Mkdir(inputDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outputDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	inputFile := filepath.Join(root, "input.json")
	if err := os.WriteFile(inputFile, []byte("input\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	hardLink := filepath.Join(outputDirectory, "hard-link.json")
	if err := os.Link(inputFile, hardLink); err != nil {
		t.Fatal(err)
	}
	symlinkDirectory := filepath.Join(root, "output-link")
	if err := os.Symlink(outputDirectory, symlinkDirectory); err != nil {
		t.Fatal(err)
	}
	inputs := []publicationInputPath{
		{label: "input tree", path: inputDirectory, directory: true},
		{label: "input document", path: inputFile},
	}

	tests := []struct {
		name   string
		output string
		want   string
	}{
		{name: "relative", output: "result.json", want: "canonical and absolute"},
		{name: "directory subtree", output: filepath.Join(inputDirectory, "result.json"), want: "overlaps input tree"},
		{name: "exact file", output: inputFile, want: "aliases input document"},
		{name: "hard link", output: hardLink, want: "aliases input document"},
		{name: "symlink directory", output: filepath.Join(symlinkDirectory, "result.json"), want: "symlink"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := validatePublicationPath(test.output, inputs...)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validatePublicationPath() error = %v, want %q", err, test.want)
			}
		})
	}

	safe := filepath.Join(outputDirectory, "result.json")
	if err := validatePublicationPath(safe, inputs...); err != nil {
		t.Fatalf("safe publication rejected: %v", err)
	}
	if err := os.WriteFile(safe, []byte("existing\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := validatePublicationPath(safe, inputs...); err == nil ||
		!strings.Contains(err.Error(), "create-only") {
		t.Fatalf("existing output error = %v, want create-only rejection", err)
	}
}

func TestValidatePublicationPathRejectsNoncanonicalInputs(t *testing.T) {
	root := t.TempDir()
	output := filepath.Join(root, "result.json")
	relativeInput := publicationInputPath{label: "relative", path: "input.json"}
	if err := validatePublicationPath(output, relativeInput); err == nil ||
		!strings.Contains(err.Error(), "canonical and absolute") {
		t.Fatalf("relative input error = %v", err)
	}

	realDirectory := filepath.Join(root, "real")
	if err := os.Mkdir(realDirectory, 0o700); err != nil {
		t.Fatal(err)
	}
	linkedDirectory := filepath.Join(root, "linked")
	if err := os.Symlink(realDirectory, linkedDirectory); err != nil {
		t.Fatal(err)
	}
	if err := validatePublicationPath(output, publicationInputPath{
		label: "linked", path: linkedDirectory, directory: true,
	}); err == nil || !strings.Contains(err.Error(), "symlink") {
		t.Fatalf("symlink input error = %v", err)
	}
}

func TestPublicationPathSnapshotRejectsReplacement(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "input")
	outputParent := filepath.Join(root, "output")
	if err := os.WriteFile(input, []byte("input\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outputParent, 0o700); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(outputParent, "report")
	snapshot, err := capturePublicationPath(output, publicationInputPath{
		label: "input", path: input,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(outputParent, outputParent+"-displaced"); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(outputParent, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := snapshot.revalidate(); err == nil ||
		!strings.Contains(err.Error(), "output directory identity changed") {
		t.Fatalf("output-parent replacement error = %v", err)
	}
}

func TestCaptureSourceAuditPublicationProtectsEveryInputKind(t *testing.T) {
	root := t.TempDir()
	repositoryDirectory := filepath.Join(root, "repository")
	outputDirectory := filepath.Join(root, "published")
	for _, directory := range []string{
		repositoryDirectory, outputDirectory,
	} {
		if err := os.Mkdir(directory, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	sourceSelections := filepath.Join(root, "source-selections.json")
	gitExecutable := filepath.Join(root, "git")
	for path, data := range map[string][]byte{
		sourceSelections: []byte("{}\n"),
		gitExecutable:    []byte("git\n"),
	} {
		if err := os.WriteFile(path, data, 0o700); err != nil {
			t.Fatal(err)
		}
	}
	bindingPath := filepath.Join(root, "repository-bindings.json")
	document := sourceAuditRepositoryBindingDocumentV3{
		Schema:        sourceAuditRepositoryBindingSchemaV3,
		GitExecutable: gitExecutable,
		GitSHA256:     strings.Repeat("0", 64),
		Repositories: []sourceAuditRepositoryBindingV3{{
			Upstream:                   "fixture/repository",
			Path:                       repositoryDirectory,
			Origin:                     "https://example.invalid/fixture/repository.git",
			OrdinaryRefInventorySHA256: strings.Repeat("1", 64),
		}},
	}
	bindingBytes, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bindingPath, append(bindingBytes, '\n'), 0o600); err != nil {
		t.Fatal(err)
	}
	bindingInfo, err := os.Lstat(bindingPath)
	if err != nil {
		t.Fatal(err)
	}
	selectionInfo, err := os.Lstat(sourceSelections)
	if err != nil {
		t.Fatal(err)
	}
	gitInfo, err := os.Lstat(gitExecutable)
	if err != nil {
		t.Fatal(err)
	}
	repositoryInfo, err := os.Lstat(repositoryDirectory)
	if err != nil {
		t.Fatal(err)
	}
	prepared := &preparedSourceAudit{
		options: SourceAuditOptions{
			RepositoryBindings: bindingPath,
			SourceSelections:   sourceSelections,
			GitExecutable:      gitExecutable,
		},
		inputs: &sourceAuditInputSnapshot{
			files: map[string]sourceAuditInputFile{
				bindingPath:      {path: bindingPath, info: bindingInfo},
				sourceSelections: {path: sourceSelections, info: selectionInfo},
			},
			paths: []string{bindingPath, sourceSelections},
		},
		expectedGit: sourceAuditGitIdentity{executable: gitExecutable},
		gitInfo:     gitInfo,
		repositoryBindings: sourceAuditRepositoryBindingSet{
			paths:     map[string]string{"scylladb/seastar": repositoryDirectory},
			pathInfos: map[string]os.FileInfo{"scylladb/seastar": repositoryInfo},
		},
	}
	for index, repository := range sourceAuditRepositories {
		path := repositoryDirectory
		info := repositoryInfo
		if index != 0 {
			path = filepath.Join(root, fmt.Sprintf("repository-%02d-%s", index, repository.slug))
			if err := os.Mkdir(path, 0o700); err != nil {
				t.Fatal(err)
			}
			info, err = os.Lstat(path)
			if err != nil {
				t.Fatal(err)
			}
		}
		prepared.repositoryBindings.paths[repository.upstream] = path
		prepared.repositoryBindings.pathInfos[repository.upstream] = info
	}

	tests := []struct {
		name   string
		output string
		want   string
	}{
		{name: "bindings", output: bindingPath, want: "repository bindings"},
		{name: "selections", output: sourceSelections, want: "source selections"},
		{name: "repository", output: filepath.Join(repositoryDirectory, "audit.md"), want: "scylladb/seastar"},
		{name: "Git", output: gitExecutable, want: "Git executable"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := captureSourceAuditPublication(prepared, test.output)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("validateSourceAuditPublication() error = %v, want %q", err, test.want)
			}
		})
	}

	if _, err := captureSourceAuditPublication(
		prepared,
		filepath.Join(outputDirectory, "audit.md"),
	); err != nil {
		t.Fatalf("safe source-audit publication rejected: %v", err)
	}
}

func TestRunGeneratorsRejectPublicationOverlapBeforeGeneration(t *testing.T) {
	t.Run("repository bindings", func(t *testing.T) {
		root := t.TempDir()
		repository := filepath.Join(root, "repository")
		if err := os.Mkdir(repository, 0o700); err != nil {
			t.Fatal(err)
		}
		gitExecutable := filepath.Join(root, "git")
		if err := os.WriteFile(gitExecutable, []byte("not invoked\n"), 0o700); err != nil {
			t.Fatal(err)
		}
		output := filepath.Join(repository, "bindings.json")
		var stderr bytes.Buffer
		code := Run(context.Background(), []string{
			"generate", "source-repository-bindings",
			"--git-executable", gitExecutable,
			"--git-sha256", strings.Repeat("0", 64),
			"--repository", "fixture/repository=" + repository,
			"--output", output,
		}, nil, &stderr)
		if code != 1 || !strings.Contains(stderr.String(), "overlaps repository fixture/repository") {
			t.Fatalf("Run() exit = %d, stderr = %q", code, stderr.String())
		}
		if _, err := os.Lstat(output); !os.IsNotExist(err) {
			t.Fatalf("unsafe output was touched: %v", err)
		}
	})
}
