package grammargen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestVerifyFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "input")
	data := []byte("pinned grammar input\n")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := verifyFile(path, digestBytes(data), "fixture"); err != nil {
		t.Fatalf("verifyFile() error = %v", err)
	}
	err := verifyFile(path, strings.Repeat("0", 64), "fixture")
	if err == nil || !strings.Contains(err.Error(), "unexpected fixture checksum") {
		t.Fatalf("verifyFile() error = %v, want checksum error", err)
	}
}

func TestPinnedSpecs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		language          string
		commit            string
		packageName       string
		outputDirectory   string
		pinCount          int
		corpusCount       int
		generatedParser   bool
		correctABIVersion bool
		splitLexer        bool
	}{
		{
			language: "csharp", commit: "9150f7d56bb47f1a809fa23623f1ba1413e93fa9",
			packageName: "csharpgrammar", outputDirectory: "internal/csharpgrammar",
			pinCount: 2, correctABIVersion: true,
		},
		{
			language: "kotlin", commit: "1852ea17b7f60fb3f9d84e0b1555d56b46b39fb1",
			packageName: "kotlingrammar", outputDirectory: "internal/kotlingrammar",
			pinCount: 2,
		},
		{
			language: "swift", commit: "8d02b7ff390a17a43ce90c4e987c49315cfc4be6",
			packageName: "swiftgrammar", outputDirectory: "internal/swiftgrammar",
			pinCount: 15, corpusCount: 10, generatedParser: true, splitLexer: true,
		},
	}
	for _, test := range tests {
		t.Run(test.language, func(t *testing.T) {
			t.Parallel()
			spec, err := specFor(test.language)
			if err != nil {
				t.Fatal(err)
			}
			if spec.upstreamCommit != test.commit {
				t.Errorf("upstreamCommit = %q, want %q", spec.upstreamCommit, test.commit)
			}
			if spec.packageName != test.packageName {
				t.Errorf("packageName = %q, want %q", spec.packageName, test.packageName)
			}
			if spec.outputDirectory != test.outputDirectory {
				t.Errorf("outputDirectory = %q, want %q", spec.outputDirectory, test.outputDirectory)
			}
			if len(spec.pins) != test.pinCount {
				t.Errorf("len(pins) = %d, want %d", len(spec.pins), test.pinCount)
			}
			if len(spec.corpusPins) != test.corpusCount {
				t.Errorf("len(corpusPins) = %d, want %d", len(spec.corpusPins), test.corpusCount)
			}
			if (spec.generatedParser != nil) != test.generatedParser {
				t.Errorf("generatedParser present = %t, want %t", spec.generatedParser != nil, test.generatedParser)
			}
			if spec.correctABIVersion != test.correctABIVersion {
				t.Errorf("correctABIVersion = %t, want %t", spec.correctABIVersion, test.correctABIVersion)
			}
			if spec.splitLexer != test.splitLexer {
				t.Errorf("splitLexer = %t, want %t", spec.splitLexer, test.splitLexer)
			}
		})
	}

	if _, err := specFor("rust"); err == nil {
		t.Fatal("specFor(rust) succeeded, want unsupported-language error")
	}
}
