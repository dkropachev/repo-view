package taskctl

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

const sourceAuditInputFileCount = 2

type sourceAuditInputSpec struct {
	label string
	path  string
}

type sourceAuditInputFile struct {
	info   os.FileInfo
	label  string
	path   string
	data   []byte
	digest [sha256.Size]byte
}

// sourceAuditInputSnapshot retains the exact two direct inputs admitted at
// the beginning of one source audit: the 144-task source-selection manifest
// and the 12-repository binding document.
type sourceAuditInputSnapshot struct {
	files map[string]sourceAuditInputFile
	paths []string
}

func newSourceAuditInputSnapshot(
	repositoryBindings, sourceSelections string,
) (*sourceAuditInputSnapshot, error) {
	specs := [...]sourceAuditInputSpec{
		{label: "repository bindings", path: repositoryBindings},
		{label: "source selections", path: sourceSelections},
	}
	snapshot := &sourceAuditInputSnapshot{
		files: make(map[string]sourceAuditInputFile, sourceAuditInputFileCount),
		paths: make([]string, 0, sourceAuditInputFileCount),
	}
	var totalBytes int64
	for _, spec := range specs {
		input, err := captureSourceAuditInput(spec)
		if err != nil {
			return nil, fmt.Errorf("capture %s: %w", spec.label, err)
		}
		if previous, found := snapshot.files[input.path]; found {
			return nil, fmt.Errorf(
				"source-audit inputs %s and %s use the same canonical path %s",
				previous.label,
				input.label,
				input.path,
			)
		}
		for _, previousPath := range snapshot.paths {
			previous := snapshot.files[previousPath]
			if os.SameFile(previous.info, input.info) {
				return nil, fmt.Errorf(
					"source-audit inputs %s and %s are aliases of the same file",
					previous.label,
					input.label,
				)
			}
		}
		totalBytes += int64(len(input.data))
		if totalBytes > int64(maximumTaskctlInputBytes) {
			return nil, fmt.Errorf(
				"source-audit inputs exceed %d total bytes",
				maximumTaskctlInputBytes,
			)
		}
		snapshot.files[input.path] = input
		snapshot.paths = append(snapshot.paths, input.path)
	}
	if err := snapshot.revalidate(); err != nil {
		return nil, fmt.Errorf("stabilize source-audit input snapshot: %w", err)
	}
	return snapshot, nil
}

func (snapshot *sourceAuditInputSnapshot) bytesFor(path string) ([]byte, error) {
	input, err := snapshot.inputFor(path)
	if err != nil {
		return nil, err
	}
	return bytes.Clone(input.data), nil
}

func (snapshot *sourceAuditInputSnapshot) identityFor(
	path string,
) (string, os.FileInfo, error) {
	input, err := snapshot.inputFor(path)
	if err != nil {
		return "", nil, err
	}
	return input.path, input.info, nil
}

func (snapshot *sourceAuditInputSnapshot) inputFor(
	path string,
) (sourceAuditInputFile, error) {
	if snapshot == nil || len(snapshot.files) != sourceAuditInputFileCount {
		return sourceAuditInputFile{}, errors.New("source-audit input snapshot is incomplete")
	}
	absolute, err := filepath.Abs(path)
	if err != nil {
		return sourceAuditInputFile{}, fmt.Errorf("make source-audit input absolute: %w", err)
	}
	input, found := snapshot.files[filepath.Clean(absolute)]
	if !found {
		return sourceAuditInputFile{}, fmt.Errorf("path is not an admitted source-audit input: %s", absolute)
	}
	return input, nil
}

func (snapshot *sourceAuditInputSnapshot) inputPaths() []string {
	if snapshot == nil {
		return nil
	}
	return append([]string(nil), snapshot.paths...)
}

func (snapshot *sourceAuditInputSnapshot) revalidate() error {
	if snapshot == nil || len(snapshot.files) != sourceAuditInputFileCount ||
		len(snapshot.paths) != sourceAuditInputFileCount {
		return errors.New("source-audit input snapshot is incomplete")
	}
	current := make([]sourceAuditInputFile, 0, sourceAuditInputFileCount)
	var totalBytes int64
	for _, path := range snapshot.paths {
		want, found := snapshot.files[path]
		if !found {
			return errors.New("source-audit input snapshot path index is inconsistent")
		}
		got, err := captureSourceAuditInput(sourceAuditInputSpec{
			label: want.label,
			path:  want.path,
		})
		if err != nil {
			return fmt.Errorf("revalidate %s: %w", want.label, err)
		}
		if got.path != want.path || !os.SameFile(want.info, got.info) {
			return fmt.Errorf("source-audit input %s changed file identity", want.label)
		}
		if got.info.Mode() != want.info.Mode() || got.info.Size() != want.info.Size() ||
			got.info.ModTime() != want.info.ModTime() {
			return fmt.Errorf("source-audit input %s changed file metadata", want.label)
		}
		if got.digest != want.digest || !bytes.Equal(got.data, want.data) {
			return fmt.Errorf("source-audit input %s changed bytes", want.label)
		}
		for _, previous := range current {
			if os.SameFile(previous.info, got.info) {
				return fmt.Errorf(
					"source-audit inputs %s and %s became aliases of the same file",
					previous.label,
					got.label,
				)
			}
		}
		totalBytes += int64(len(got.data))
		if totalBytes > int64(maximumTaskctlInputBytes) {
			return fmt.Errorf(
				"source-audit inputs exceed %d total bytes",
				maximumTaskctlInputBytes,
			)
		}
		current = append(current, got)
	}
	return nil
}

func captureSourceAuditInput(spec sourceAuditInputSpec) (sourceAuditInputFile, error) {
	absolute, err := filepath.Abs(spec.path)
	if err != nil {
		return sourceAuditInputFile{}, fmt.Errorf("make path absolute: %w", err)
	}
	absolute = filepath.Clean(absolute)
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return sourceAuditInputFile{}, err
	}
	if filepath.Clean(resolved) != absolute {
		return sourceAuditInputFile{}, errors.New("input path must not traverse symlinks")
	}
	before, err := os.Lstat(absolute)
	if err != nil {
		return sourceAuditInputFile{}, err
	}
	data, err := readRegularFile(absolute)
	if err != nil {
		return sourceAuditInputFile{}, err
	}
	after, err := os.Lstat(absolute)
	if err != nil {
		return sourceAuditInputFile{}, err
	}
	if !before.Mode().IsRegular() || !after.Mode().IsRegular() ||
		!os.SameFile(before, after) || before.Mode() != after.Mode() ||
		before.Size() != after.Size() || before.ModTime() != after.ModTime() ||
		int64(len(data)) != after.Size() {
		return sourceAuditInputFile{}, errors.New("input changed while capturing snapshot")
	}
	return sourceAuditInputFile{
		label:  spec.label,
		path:   absolute,
		data:   data,
		digest: sha256.Sum256(data),
		info:   after,
	}, nil
}
