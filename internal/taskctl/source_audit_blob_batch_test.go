package taskctl

import (
	"bufio"
	"bytes"
	"compress/zlib"
	"context"
	"crypto/sha1" //nolint:gosec // The test writes native Git SHA-1 objects.
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/yapless/scopesifter/internal/processpolicy"
)

const sourceAuditTestGitExecutable = "/usr/bin/git"

func sourceAuditTestGitRunner(t *testing.T) sourceAuditGitRunner {
	t.Helper()
	path, file, err := processpolicy.OpenNativeExecutable(sourceAuditTestGitExecutable)
	if err != nil {
		t.Fatal(err)
	}
	info, err := file.Stat()
	if err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	digest, err := hashSourceAuditGitFile(file, info.Size())
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		t.Fatal(errors.Join(err, closeErr))
	}
	runner, err := newSourceAuditGitRunner(sourceAuditGitIdentity{
		executable: path,
		sha256:     digest,
	})
	if err != nil {
		t.Fatal(err)
	}
	return runner
}

func sourceAuditTestRepositoryInfo(t *testing.T, repository string) os.FileInfo {
	t.Helper()
	info, err := os.Lstat(repository)
	if err != nil {
		t.Fatal(err)
	}
	return info
}

func TestSourceAuditBlobBatchReadsMultipleBlobsAndSurvivesMissingObject(t *testing.T) {
	t.Parallel()
	repository, blobs := sourceAuditBlobBatchTestRepository(t, [][]byte{
		[]byte("first blob\n"),
		[]byte("second blob without a final newline"),
	})
	batch, err := newSourceAuditBlobBatch(
		context.Background(), repository, sourceAuditTestRepositoryInfo(t, repository),
		sourceAuditTestGitRunner(t),
	)
	if err != nil {
		t.Fatal(err)
	}
	reader := batch.ReadBlob

	for _, blob := range blobs {
		var output bytes.Buffer
		if err := reader(context.Background(), blob.objectID, &output); err != nil {
			t.Fatalf("read %s: %v", blob.objectID, err)
		}
		if !bytes.Equal(output.Bytes(), blob.content) {
			t.Fatalf("read %s = %q, want %q", blob.objectID, output.Bytes(), blob.content)
		}
	}
	missing := strings.Repeat("f", 40)
	if err := reader(context.Background(), missing, io.Discard); !errors.Is(
		err,
		errSourceAuditBlobBatchMissing,
	) {
		t.Fatalf("missing-object error = %v", err)
	}
	var afterMissing bytes.Buffer
	if err := reader(context.Background(), blobs[0].objectID, &afterMissing); err != nil {
		t.Fatalf("read after missing object: %v", err)
	}
	if !bytes.Equal(afterMissing.Bytes(), blobs[0].content) {
		t.Fatalf("read after missing object = %q", afterMissing.Bytes())
	}
	if err := batch.Close(); err != nil {
		t.Fatalf("close batch: %v", err)
	}
	if err := batch.Close(); err != nil {
		t.Fatalf("second close: %v", err)
	}
	if err := reader(context.Background(), blobs[0].objectID, io.Discard); err == nil ||
		!strings.Contains(err.Error(), "closed") {
		t.Fatalf("read after close error = %v", err)
	}
}

func TestSourceAuditBlobBatchRejectsInvalidRequestsAndCancellation(t *testing.T) {
	t.Parallel()
	repository, blobs := sourceAuditBlobBatchTestRepository(t, [][]byte{[]byte("blob")})
	batch, err := newSourceAuditBlobBatch(
		context.Background(), repository, sourceAuditTestRepositoryInfo(t, repository),
		sourceAuditTestGitRunner(t),
	)
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		if err := batch.Close(); err != nil {
			t.Errorf("close batch: %v", err)
		}
	}()
	for _, objectID := range []string{
		strings.Repeat("a", 39),
		strings.Repeat("A", 40),
		strings.Repeat("g", 40),
	} {
		if err := batch.ReadBlob(context.Background(), objectID, io.Discard); err == nil {
			t.Fatalf("ReadBlob accepted object ID %q", objectID)
		}
	}
	if err := batch.ReadBlob(context.Background(), blobs[0].objectID, nil); err == nil {
		t.Fatal("ReadBlob accepted a nil destination")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := batch.ReadBlob(canceled, blobs[0].objectID, io.Discard); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("canceled ReadBlob error = %v", err)
	}
	if err := batch.ReadBlob(context.Background(), blobs[0].objectID, io.Discard); err != nil {
		t.Fatalf("valid read after rejected requests: %v", err)
	}
}

func TestSourceAuditBlobBatchRejectsRepositoryReplacementBeforeStart(t *testing.T) {
	repository, _ := sourceAuditBlobBatchTestRepository(t, [][]byte{[]byte("blob")})
	expected := sourceAuditTestRepositoryInfo(t, repository)
	retained := repository + "-retained"
	marker := filepath.Join(repository, "replacement-marker")
	sourceAuditRepositoryPinHook = func(stage, path string) error {
		if stage != "after-open" || path != repository {
			return nil
		}
		if err := os.Rename(repository, retained); err != nil {
			return err
		}
		if err := os.Mkdir(repository, 0o700); err != nil {
			return err
		}
		return os.WriteFile(marker, []byte("replacement\n"), 0o600)
	}
	t.Cleanup(func() { sourceAuditRepositoryPinHook = nil })

	batch, err := newSourceAuditBlobBatch(
		context.Background(), repository, expected, sourceAuditTestGitRunner(t),
	)
	if batch != nil || err == nil || !strings.Contains(err.Error(), "admitted identity") {
		t.Fatalf("batch replacement result = batch:%v error:%v", batch, err)
	}
	content, readErr := os.ReadFile(marker)
	if readErr != nil || string(content) != "replacement\n" {
		t.Fatalf("replacement marker changed: content=%q error=%v", content, readErr)
	}
}

func TestSourceAuditBlobBatchRejectsRepositoryReplacementDuringRead(t *testing.T) {
	repository, blobs := sourceAuditBlobBatchTestRepository(t, [][]byte{[]byte("blob")})
	expected := sourceAuditTestRepositoryInfo(t, repository)
	batch, err := newSourceAuditBlobBatch(
		context.Background(), repository, expected, sourceAuditTestGitRunner(t),
	)
	if err != nil {
		t.Fatal(err)
	}
	retained := repository + "-retained"
	if err := os.Rename(repository, retained); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(repository, 0o700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(repository, "replacement-marker")
	if err := os.WriteFile(marker, []byte("replacement\n"), 0o600); err != nil {
		t.Fatal(err)
	}

	err = batch.ReadBlob(context.Background(), blobs[0].objectID, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "admitted identity") {
		t.Fatalf("read through replaced repository error = %v", err)
	}
	if closeErr := batch.Close(); closeErr == nil ||
		!strings.Contains(closeErr.Error(), "admitted identity") {
		t.Fatalf("close through replaced repository error = %v", closeErr)
	}
	content, readErr := os.ReadFile(marker)
	if readErr != nil || string(content) != "replacement\n" {
		t.Fatalf("replacement marker changed: content=%q error=%v", content, readErr)
	}
}

func TestReadSourceAuditBlobBatchResponseRejectsMalformedAndBoundedInput(t *testing.T) {
	t.Parallel()
	objectID := strings.Repeat("1", 40)
	otherID := strings.Repeat("2", 40)
	tests := []struct {
		name     string
		response string
	}{
		{"header without LF", objectID + " blob 1"},
		{"oversized header", strings.Repeat("x", sourceAuditBlobBatchMaximumHeader+1) + "\n"},
		{"wrong object", otherID + " blob 1\na\n"},
		{"wrong type", objectID + " tree 1\na\n"},
		{"leading zero size", objectID + " blob 01\na\n"},
		{"negative size", objectID + " blob -1\n"},
		{"oversized blob", fmt.Sprintf("%s blob %d\n", objectID, sourceAuditMaximumBlobBytes+1)},
		{"truncated blob", objectID + " blob 2\na"},
		{"missing trailer", objectID + " blob 1\na"},
		{"invalid trailer", objectID + " blob 1\nax"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := readSourceAuditBlobBatchResponse(
				context.Background(),
				bufio.NewReader(strings.NewReader(test.response)),
				objectID,
				io.Discard,
			)
			if !errors.Is(err, errSourceAuditBlobBatchProtocol) {
				t.Fatalf("error = %v, want protocol error", err)
			}
		})
	}

	missing := objectID + " missing\n"
	if err := readSourceAuditBlobBatchResponse(
		context.Background(),
		bufio.NewReader(strings.NewReader(missing)),
		objectID,
		io.Discard,
	); !errors.Is(err, errSourceAuditBlobBatchMissing) {
		t.Fatalf("missing response error = %v", err)
	}
}

func TestReadSourceAuditBlobBatchResponseDrainsAfterWriterFailure(t *testing.T) {
	t.Parallel()
	objectID := strings.Repeat("3", 40)
	responses := objectID + " blob 3\nabc\n" + objectID + " blob 3\ndef\n"
	reader := bufio.NewReader(strings.NewReader(responses))
	want := errors.New("destination failed")
	err := readSourceAuditBlobBatchResponse(
		context.Background(),
		reader,
		objectID,
		sourceAuditBlobBatchFailingWriter{err: want},
	)
	if !errors.Is(err, want) {
		t.Fatalf("writer error = %v, want %v", err, want)
	}
	var output bytes.Buffer
	if err := readSourceAuditBlobBatchResponse(
		context.Background(),
		reader,
		objectID,
		&output,
	); err != nil {
		t.Fatalf("second response: %v", err)
	}
	if output.String() != "def" {
		t.Fatalf("second response = %q, want def", output.String())
	}
}

func TestSourceAuditBlobBatchErrorBufferIsBounded(t *testing.T) {
	t.Parallel()
	buffer := &sourceAuditBlobBatchErrorBuffer{limit: 4}
	if written, err := buffer.Write([]byte("abcdef")); err != nil || written != 6 {
		t.Fatalf("Write = (%d, %v), want (6, nil)", written, err)
	}
	if got := string(buffer.bytes()); got != "abcd" {
		t.Fatalf("buffer = %q, want abcd", got)
	}
	if !buffer.overflowed() {
		t.Fatal("stderr overflow was not recorded")
	}
}

func TestSourceAuditBlobBatchGitArgumentsHaveClosedGrammar(t *testing.T) {
	t.Parallel()
	if err := validateSourceAuditBlobBatchGitArguments([]string{"cat-file", "--batch"}); err != nil {
		t.Fatalf("canonical arguments rejected: %v", err)
	}
	for _, arguments := range [][]string{
		nil,
		{"cat-file"},
		{"cat-file", "blob", strings.Repeat("1", 40)},
		{"cat-file", "--batch-check"},
		{"cat-file", "--batch", "extra"},
	} {
		if err := validateSourceAuditBlobBatchGitArguments(arguments); err == nil {
			t.Fatalf("arguments accepted: %q", arguments)
		}
	}
}

type sourceAuditBlobBatchTestBlob struct {
	objectID string
	content  []byte
}

func sourceAuditBlobBatchTestRepository(
	t *testing.T,
	contents [][]byte,
) (string, []sourceAuditBlobBatchTestBlob) {
	t.Helper()
	repository := t.TempDir()
	gitDirectory := filepath.Join(repository, ".git")
	for _, directory := range []string{
		gitDirectory,
		filepath.Join(gitDirectory, "objects"),
		filepath.Join(gitDirectory, "refs", "heads"),
	} {
		if err := os.MkdirAll(directory, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(
		filepath.Join(gitDirectory, "HEAD"),
		[]byte("ref: refs/heads/main\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(gitDirectory, "config"),
		[]byte("[core]\n\trepositoryformatversion = 0\n\tbare = false\n"),
		0o644,
	); err != nil {
		t.Fatal(err)
	}
	blobs := make([]sourceAuditBlobBatchTestBlob, 0, len(contents))
	for _, content := range contents {
		header := fmt.Sprintf("blob %d%c", len(content), byte(0))
		hasher := sha1.New() //nolint:gosec // Git SHA-1 object identity is the protocol under test.
		_, _ = io.WriteString(hasher, header)
		_, _ = hasher.Write(content)
		objectID := hex.EncodeToString(hasher.Sum(nil))
		objectDirectory := filepath.Join(gitDirectory, "objects", objectID[:2])
		if err := os.MkdirAll(objectDirectory, 0o755); err != nil {
			t.Fatal(err)
		}
		objectPath := filepath.Join(objectDirectory, objectID[2:])
		file, err := os.OpenFile(objectPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o444)
		if err != nil {
			t.Fatal(err)
		}
		compressor := zlib.NewWriter(file)
		_, writeHeaderErr := io.WriteString(compressor, header)
		_, writeContentErr := compressor.Write(content)
		compressErr := compressor.Close()
		closeErr := file.Close()
		if err := errors.Join(writeHeaderErr, writeContentErr, compressErr, closeErr); err != nil {
			t.Fatal(err)
		}
		blobs = append(blobs, sourceAuditBlobBatchTestBlob{
			objectID: objectID,
			content:  bytes.Clone(content),
		})
	}
	return repository, blobs
}

type sourceAuditBlobBatchFailingWriter struct{ err error }

func (writer sourceAuditBlobBatchFailingWriter) Write([]byte) (int, error) {
	return 0, writer.err
}
