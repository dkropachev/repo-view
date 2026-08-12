package taskctl

import (
	"bytes"
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRunGenerateSourceSelectionsPublishesCanonicalArtifact(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "selection-authoring-input.json")
	output := filepath.Join(root, "source-selections.json")
	authoring := sourceSelectionAuthoringFixtureJSON(t)
	canonical := sourceSelectionFixtureJSON(t)
	if err := os.WriteFile(input, authoring, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(authoring))
	var stderr bytes.Buffer
	code := Run(context.Background(), []string{
		"generate", "source-selections",
		"--input", input,
		"--input-sha256", digest,
		"--output", output,
	}, nil, &stderr)
	if code != 0 {
		t.Fatalf("generate exit = %d, stderr = %q", code, stderr.String())
	}
	got, err := os.ReadFile(output)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, canonical) {
		t.Fatal("generated source selections differ from canonical input")
	}
}

func TestRunGenerateSourceSelectionsRejectsUnauthenticatedOrOverlappingInput(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "source-selections.json")
	authoring := sourceSelectionAuthoringFixtureJSON(t)
	if err := os.WriteFile(input, authoring, 0o600); err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(authoring))
	tests := []struct {
		name   string
		hash   string
		output string
		want   string
	}{
		{name: "digest", hash: strings.Repeat("0", 64), output: filepath.Join(root, "wrong.json"), want: "SHA-256 is"},
		{name: "same path", hash: digest, output: input, want: "aliases"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			var stderr bytes.Buffer
			code := Run(context.Background(), []string{
				"generate", "source-selections",
				"--input", input,
				"--input-sha256", test.hash,
				"--output", test.output,
			}, nil, &stderr)
			if code != 1 || !strings.Contains(stderr.String(), test.want) {
				t.Fatalf("exit = %d, stderr = %q, want %q", code, stderr.String(), test.want)
			}
		})
	}
}

func TestRunRejectsWritableFilesystemChecksumRoles(t *testing.T) {
	for _, arguments := range [][]string{
		{"generate", "checksums"},
		{"generate", "pointer-checksums"},
		{"validate", "checksums"},
		{"validate", "pointer-checksums"},
	} {
		var stderr bytes.Buffer
		if code := Run(context.Background(), arguments, nil, &stderr); code != 2 {
			t.Fatalf("Run(%q) exit = %d, want usage exit 2; stderr = %q", arguments, code, stderr.String())
		}
		if strings.Contains(stderr.String(), arguments[1]) {
			t.Fatalf("disabled role %q remains advertised in usage: %q", arguments, stderr.String())
		}
	}
}

func TestRunGenerateSourceRepositoryBindingsRejectsMalformedInput(t *testing.T) {
	var stderr bytes.Buffer
	code := Run(context.Background(), []string{
		"generate", "source-repository-bindings",
		"--git-executable", "/usr/bin/git",
		"--git-sha256", strings.Repeat("0", 64),
		"--repository", "missing-separator",
		"--output", filepath.Join(t.TempDir(), "repository-bindings.json"),
	}, nil, &stderr)
	if code != 1 {
		t.Fatalf("Run() exit = %d, want 1; stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "must be UPSTREAM=PATH") {
		t.Fatalf("stderr = %q, want malformed repository binding error", stderr.String())
	}
}

func TestRunValidateSourceRepositoryBindingsRequiresInput(t *testing.T) {
	var stderr bytes.Buffer
	code := Run(context.Background(), []string{
		"validate", "source-repository-bindings",
	}, nil, &stderr)
	if code != 1 {
		t.Fatalf("Run() exit = %d, want 1; stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stderr.String(), "requires --git-executable") {
		t.Fatalf("stderr = %q, want required input error", stderr.String())
	}
}
