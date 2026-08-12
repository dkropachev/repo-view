//go:build linux

package taskctl

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteAtomicPinnedPublishesDescriptorBytesWithoutStagingName(t *testing.T) {
	parentPath := filepath.Join(t.TempDir(), "published")
	if err := os.Mkdir(parentPath, 0o700); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(parentPath, "report.json")
	wanted := []byte("canonical publication\n")
	if err := writeAtomicPinned(outputPath, wanted, atomicPublicationHooks{}); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(wanted) {
		t.Fatalf("published output = %q, want %q", got, wanted)
	}
	info, err := os.Lstat(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0o644 ||
		!sourceAuditFileHasOneLink(info) {
		t.Fatalf("published identity = %v, want one-link regular 0644", info)
	}
	assertAtomicPublicationDirectoryEntries(t, parentPath, "report.json")
}

func TestWriteAtomicPinnedRejectsPreexistingDestinationWithoutModification(t *testing.T) {
	parentPath := filepath.Join(t.TempDir(), "published")
	if err := os.Mkdir(parentPath, 0o700); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(parentPath, "report.json")
	original := []byte("existing destination survives\n")
	if err := os.WriteFile(outputPath, original, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Lstat(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	err = writeAtomicPinned(
		outputPath,
		[]byte("must not replace existing bytes\n"),
		atomicPublicationHooks{},
	)
	if err == nil || !strings.Contains(err.Error(), "create-only") {
		t.Fatalf("writeAtomicPinned() error = %v, want create-only rejection", err)
	}
	after, err := os.Lstat(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(outputPath)
	if err != nil || string(got) != string(original) || !os.SameFile(before, after) ||
		before.Mode() != after.Mode() {
		t.Fatalf("preexisting destination changed: got=%q before=%v after=%v err=%v", got, before, after, err)
	}
	assertAtomicPublicationDirectoryEntries(t, parentPath, "report.json")
}

func TestWriteAtomicPinnedRacingDestinationCreationWinsUntouched(t *testing.T) {
	parentPath := filepath.Join(t.TempDir(), "published")
	if err := os.Mkdir(parentPath, 0o700); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(parentPath, "report.json")
	racingBytes := []byte("racing destination survives\n")
	var racingIdentity os.FileInfo
	err := writeAtomicPinned(
		outputPath,
		[]byte("descriptor bytes must not replace the race winner\n"),
		atomicPublicationHooks{
			beforeDescriptorLink: func() error {
				if err := os.WriteFile(outputPath, racingBytes, 0o600); err != nil {
					return err
				}
				var err error
				racingIdentity, err = os.Lstat(outputPath)
				return err
			},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "appeared before create-only") {
		t.Fatalf("writeAtomicPinned() error = %v, want atomic EEXIST rejection", err)
	}
	current, err := os.Lstat(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(outputPath)
	if err != nil || string(got) != string(racingBytes) ||
		racingIdentity == nil || !os.SameFile(racingIdentity, current) ||
		racingIdentity.Mode() != current.Mode() {
		t.Fatalf("racing destination changed: got=%q current=%v err=%v", got, current, err)
	}
	assertAtomicPublicationDirectoryEntries(t, parentPath, "report.json")
}

func TestWriteAtomicPinnedTreatsFailedPostLinkInspectionAsCommitted(t *testing.T) {
	parentPath := filepath.Join(t.TempDir(), "published")
	if err := os.Mkdir(parentPath, 0o700); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(parentPath, "report.json")
	racingBytes := []byte("racing destination survives ambiguous link result\n")
	err := writeAtomicPinned(
		outputPath,
		[]byte("anonymous bytes must not be reclaimed after ambiguous commit\n"),
		atomicPublicationHooks{
			beforeDescriptorLink: func() error {
				return os.WriteFile(outputPath, racingBytes, 0o600)
			},
			beforeLinkErrorCommitInspection: func(file *os.File) {
				_ = file.Close()
			},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "cannot determine whether atomic publication committed") {
		t.Fatalf("writeAtomicPinned() error = %v, want ambiguous-commit rejection", err)
	}
	got, readErr := os.ReadFile(outputPath)
	if readErr != nil || string(got) != string(racingBytes) {
		t.Fatalf("racing destination = %q, %v; want unchanged %q", got, readErr, racingBytes)
	}
	assertAtomicPublicationDirectoryEntries(t, parentPath, "report.json")
}

func TestWriteAtomicPinnedKeepsCommittedFileAfterPostLinkFailure(t *testing.T) {
	parentPath := filepath.Join(t.TempDir(), "published")
	if err := os.Mkdir(parentPath, 0o700); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(parentPath, "report.json")
	wanted := []byte("committed publication\n")
	err := writeAtomicPinned(
		outputPath,
		wanted,
		atomicPublicationHooks{
			afterDescriptorLinkBeforeValidation: func() error {
				return errors.New("forced post-link failure")
			},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "forced post-link failure") {
		t.Fatalf("writeAtomicPinned() error = %v, want forced failure", err)
	}
	got, readErr := os.ReadFile(outputPath)
	if readErr != nil || string(got) != string(wanted) {
		t.Fatalf("committed publication = %q, %v; want %q", got, readErr, wanted)
	}
	assertAtomicPublicationDirectoryEntries(t, parentPath, "report.json")
}

func TestWriteAtomicPinnedNeverModifiesPostLinkReplacement(t *testing.T) {
	parentPath := filepath.Join(t.TempDir(), "published")
	if err := os.Mkdir(parentPath, 0o700); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(parentPath, "report.json")
	retainedPublicationPath := filepath.Join(parentPath, "retained-publication.json")
	wanted := []byte("committed publication\n")
	replacement := []byte("unrelated replacement\n")

	err := writeAtomicPinned(
		outputPath,
		wanted,
		atomicPublicationHooks{
			afterDescriptorLinkBeforeValidation: func() error {
				if err := os.Rename(outputPath, retainedPublicationPath); err != nil {
					return err
				}
				return os.WriteFile(outputPath, replacement, 0o600)
			},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "changed unexpectedly") {
		t.Fatalf("writeAtomicPinned() error = %v, want replacement rejection", err)
	}
	gotReplacement, err := os.ReadFile(outputPath)
	if err != nil || string(gotReplacement) != string(replacement) {
		t.Fatalf("replacement = %q, %v; want unchanged %q", gotReplacement, err, replacement)
	}
	gotPublication, err := os.ReadFile(retainedPublicationPath)
	if err != nil || string(gotPublication) != string(wanted) {
		t.Fatalf("retained publication = %q, %v; want %q", gotPublication, err, wanted)
	}
	assertAtomicPublicationDirectoryEntries(
		t,
		parentPath,
		"report.json",
		"retained-publication.json",
	)
}

func TestWriteAtomicPinsParentAcrossPathSwapWithoutNamedResidue(t *testing.T) {
	root := t.TempDir()
	parentPath := filepath.Join(root, "published")
	displacedPath := filepath.Join(root, "displaced")
	if err := os.Mkdir(parentPath, 0o700); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(parentPath, "report.json")
	substituteOutput := []byte("substitute output\n")

	err := writeAtomicPinned(
		outputPath,
		[]byte("trusted publication\n"),
		atomicPublicationHooks{
			beforeDescriptorLink: func() error {
				if err := os.Rename(parentPath, displacedPath); err != nil {
					return err
				}
				if err := os.Mkdir(parentPath, 0o700); err != nil {
					return err
				}
				return os.WriteFile(outputPath, substituteOutput, 0o600)
			},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "pathname changed") {
		t.Fatalf("writeAtomicPinned() error = %v, want parent-change rejection", err)
	}
	gotOutput, readErr := os.ReadFile(outputPath)
	if readErr != nil || string(gotOutput) != string(substituteOutput) {
		t.Fatalf("substitute output = %q, %v; want unchanged %q", gotOutput, readErr, substituteOutput)
	}
	assertAtomicPublicationDirectoryEntries(t, parentPath, "report.json")
	assertAtomicPublicationDirectoryEntries(t, displacedPath)
}

func TestWriteAtomicPreCommitFailureLeavesNoNamedResidue(t *testing.T) {
	parentPath := filepath.Join(t.TempDir(), "published")
	if err := os.Mkdir(parentPath, 0o700); err != nil {
		t.Fatal(err)
	}
	err := writeAtomicPinned(
		filepath.Join(parentPath, "report.json"),
		[]byte("secret intermediate bytes\n"),
		atomicPublicationHooks{
			beforeDescriptorLink: func() error {
				return errors.New("forced pre-link failure")
			},
		},
	)
	if err == nil || !strings.Contains(err.Error(), "forced pre-link failure") {
		t.Fatalf("writeAtomicPinned() error = %v, want forced failure", err)
	}
	assertAtomicPublicationDirectoryEntries(t, parentPath)
}

func TestWriteAtomicRejectsParentSwappedAfterGeneration(t *testing.T) {
	root := t.TempDir()
	parentPath := filepath.Join(root, "published")
	displacedPath := filepath.Join(root, "displaced")
	if err := os.Mkdir(parentPath, 0o700); err != nil {
		t.Fatal(err)
	}
	outputPath := filepath.Join(parentPath, "report.json")
	expectedParent, err := captureAtomicPublicationParent(outputPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(parentPath, displacedPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(parentPath, 0o700); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(parentPath, "sentinel")
	if err := os.WriteFile(sentinel, []byte("survive\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	err = writeAtomicWithParent(outputPath, []byte("trusted\n"), expectedParent)
	if err == nil || !strings.Contains(err.Error(), "since generation began") {
		t.Fatalf("writeAtomicWithParent() error = %v, want parent-swap rejection", err)
	}
	if _, err := os.Lstat(outputPath); !os.IsNotExist(err) {
		t.Fatalf("substitute output was touched: %v", err)
	}
	if got, err := os.ReadFile(sentinel); err != nil || string(got) != "survive\n" {
		t.Fatalf("substitute sentinel = %q, %v", got, err)
	}
}

func assertAtomicPublicationDirectoryEntries(t *testing.T, path string, names ...string) {
	t.Helper()
	entries, err := os.ReadDir(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(names) {
		t.Fatalf("atomic publication directory entries = %v, want %v", entries, names)
	}
	for index, name := range names {
		if entries[index].Name() != name {
			t.Fatalf("atomic publication directory entry %d = %q, want %q", index, entries[index].Name(), name)
		}
	}
}
