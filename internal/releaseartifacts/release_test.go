package releaseartifacts

import (
	"bytes"
	"os"
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
		wantErr string
	}{
		{name: "semantic version", ref: "v1.2.3", want: "1.2.3"},
		{name: "slash sanitized", ref: "v1/release", want: "1-release"},
		{name: "missing prefix", ref: "1.2.3", wantErr: "must start with v"},
		{name: "empty", ref: "v", wantErr: "version is empty"},
		{name: "unsafe", ref: "v1 2", wantErr: "unsafe character"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			got, err := versionFromRef(test.ref)
			if test.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), test.wantErr) {
					t.Fatalf("versionFromRef(%q) error = %v, want containing %q", test.ref, err, test.wantErr)
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

func TestArchivesAreDeterministicAndClosed(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	binary := []byte("deterministic-binary\x00")
	notice := []byte("notice\n")
	tests := []struct {
		name     string
		write    func(string, string, []byte, []byte) error
		validate func(string) error
		ext      string
		binary   string
	}{
		{name: "tar gzip", write: writeTarGzip, validate: func(path string) error { return validateTarGzip(path, notice) }, ext: ".tar.gz", binary: "scopesifter"},
		{name: "zip", write: writeZip, validate: func(path string) error { return validateZip(path, notice) }, ext: ".zip", binary: "scopesifter.exe"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			first := filepath.Join(root, strings.ReplaceAll(test.name, " ", "-")+"-first"+test.ext)
			second := filepath.Join(root, strings.ReplaceAll(test.name, " ", "-")+"-second"+test.ext)
			if err := test.write(first, test.binary, binary, notice); err != nil {
				t.Fatal(err)
			}
			if err := test.write(second, test.binary, binary, notice); err != nil {
				t.Fatal(err)
			}
			firstBytes, err := os.ReadFile(first)
			if err != nil {
				t.Fatal(err)
			}
			secondBytes, err := os.ReadFile(second)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(firstBytes, secondBytes) {
				t.Fatal("repeated archive builds differ")
			}
			if err := test.validate(first); err != nil {
				t.Fatalf("validate archive: %v", err)
			}
		})
	}
}

func TestValidateArtifactSetAndChecksums(t *testing.T) {
	t.Parallel()
	dist := t.TempDir()
	const version = "1.2.3"
	for _, item := range targets {
		base := "scopesifter_" + version + "_" + item.goos + "_" + item.goarch
		if item.goos == "windows" {
			if err := writeZip(filepath.Join(dist, base+".zip"), "scopesifter.exe", []byte(item.goarch), []byte("notice")); err != nil {
				t.Fatal(err)
			}
			continue
		}
		if err := writeTarGzip(filepath.Join(dist, base+".tar.gz"), "scopesifter", []byte(item.goarch), []byte("notice")); err != nil {
			t.Fatal(err)
		}
	}
	if err := writeChecksums(dist); err != nil {
		t.Fatal(err)
	}
	if err := validateArtifactSet(dist, version, []byte("notice")); err != nil {
		t.Fatalf("validate artifact set: %v", err)
	}
	archive := filepath.Join(dist, "scopesifter_1.2.3_linux_amd64.tar.gz")
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
	if err := validateArtifactSet(dist, version, []byte("notice")); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("corrupt artifact error = %v, want checksum mismatch", err)
	}
}

func TestBuildRefusesExistingDestinationBeforeExecutingBuild(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.invalid/test\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, noticeName), []byte("notice\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(root, "dist"), 0o755); err != nil {
		t.Fatal(err)
	}
	err := Build(root, "v1.0.0")
	if err == nil || !strings.Contains(err.Error(), "destination already exists") {
		t.Fatalf("Build error = %v, want existing destination refusal", err)
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
