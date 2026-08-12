package taskctl

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type publicationInputPath struct {
	expected  os.FileInfo
	label     string
	path      string
	directory bool
}

type publicationPhysicalPath struct {
	expected os.FileInfo
	name     string
	path     string
	exists   bool
}

type publicationPathSnapshot struct {
	output       string
	outputInfo   os.FileInfo
	outputParent os.FileInfo
	inputs       []publicationInputPath
}

// validatePublicationPath keeps an atomic generation destination separate
// from every path used to derive it. Directory inputs protect their complete
// subtrees; file inputs protect both their pathname and an existing hard-link
// identity. All names must already be canonical absolute, symlink-free paths
// so comparisons cannot be changed by the caller's spelling.
func validatePublicationPath(output string, inputs ...publicationInputPath) error {
	_, err := capturePublicationPath(output, inputs...)
	return err
}

// capturePublicationPath validates the complete lexical and physical path
// topology and retains every endpoint identity so the same topology can be
// required again immediately before publication.
func capturePublicationPath(
	output string,
	inputs ...publicationInputPath,
) (*publicationPathSnapshot, error) {
	outputPath, outputInfo, outputParent, err := inspectPublicationOutput(output)
	if err != nil {
		return nil, fmt.Errorf("output: %w", err)
	}
	retained := make([]publicationInputPath, 0, len(inputs))
	physical := make([]publicationPhysicalPath, 0, len(inputs)+1)
	physicalOutputIdentity := outputInfo
	physicalOutputExists := outputInfo != nil
	if physicalOutputIdentity == nil {
		physicalOutputIdentity = outputParent
	}
	physical = append(physical, publicationPhysicalPath{
		name:     "output",
		path:     outputPath,
		expected: physicalOutputIdentity,
		exists:   physicalOutputExists,
	})
	for _, input := range inputs {
		inputPath, inputInfo, err := inspectPublicationInput(input)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", input.label, err)
		}
		if input.expected != nil &&
			(!os.SameFile(input.expected, inputInfo) || input.expected.Mode() != inputInfo.Mode()) {
			return nil, fmt.Errorf("%s: path changed from its authenticated identity", input.label)
		}
		if input.directory {
			if publicationPathWithin(inputPath, outputPath) {
				return nil, fmt.Errorf(
					"output path %s overlaps %s directory %s",
					outputPath,
					input.label,
					inputPath,
				)
			}
		} else if outputPath == inputPath ||
			(outputInfo != nil && os.SameFile(outputInfo, inputInfo)) {
			return nil, fmt.Errorf(
				"output path %s aliases %s file %s",
				outputPath,
				input.label,
				inputPath,
			)
		}
		input.path = inputPath
		input.expected = inputInfo
		retained = append(retained, input)
		physical = append(physical, publicationPhysicalPath{
			name:     input.label,
			path:     inputPath,
			expected: inputInfo,
			exists:   true,
		})
	}
	if outputInfo != nil {
		return nil, fmt.Errorf(
			"output path %s already exists; atomic publication is create-only and did not modify it",
			outputPath,
		)
	}
	if err := requirePhysicallyDisjointPublicationPaths(physical); err != nil {
		return nil, err
	}
	return &publicationPathSnapshot{
		output: outputPath, outputInfo: outputInfo,
		outputParent: outputParent, inputs: retained,
	}, nil
}

func (snapshot *publicationPathSnapshot) revalidate() error {
	if snapshot == nil || snapshot.output == "" || len(snapshot.inputs) == 0 {
		return errors.New("publication path snapshot is incomplete")
	}
	current, err := capturePublicationPath(snapshot.output, snapshot.inputs...)
	if err != nil {
		return err
	}
	if (snapshot.outputInfo == nil) != (current.outputInfo == nil) {
		return errors.New("output path existence changed during generation")
	}
	if snapshot.outputInfo != nil &&
		(!os.SameFile(snapshot.outputInfo, current.outputInfo) ||
			snapshot.outputInfo.Mode() != current.outputInfo.Mode()) {
		return errors.New("output path identity changed during generation")
	}
	if snapshot.outputParent == nil || current.outputParent == nil ||
		!os.SameFile(snapshot.outputParent, current.outputParent) ||
		snapshot.outputParent.Mode() != current.outputParent.Mode() {
		return errors.New("output directory identity changed during generation")
	}
	return nil
}

func inspectPublicationOutput(path string) (string, os.FileInfo, os.FileInfo, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", nil, nil, errors.New("path must be canonical and absolute")
	}
	directory := filepath.Dir(path)
	_, parentInfo, err := inspectCanonicalPublicationPath(directory, true)
	if err != nil {
		return "", nil, nil, fmt.Errorf("directory: %w", err)
	}
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return path, nil, parentInfo, nil
	}
	if err != nil {
		return "", nil, nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return "", nil, nil, errors.New("existing path is not a regular non-symlink file")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", nil, nil, err
	}
	if resolved != path {
		return "", nil, nil, errors.New("existing path traverses a symlink")
	}
	return path, info, parentInfo, nil
}

func inspectPublicationInput(input publicationInputPath) (string, os.FileInfo, error) {
	if input.label == "" {
		return "", nil, errors.New("input label is empty")
	}
	return inspectCanonicalPublicationPath(input.path, input.directory)
}

func inspectCanonicalPublicationPath(path string, directory bool) (string, os.FileInfo, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", nil, errors.New("path must be canonical and absolute")
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", nil, err
	}
	if info.Mode()&os.ModeSymlink != 0 {
		return "", nil, errors.New("path must not be a symlink")
	}
	if directory {
		if !info.IsDir() {
			return "", nil, errors.New("path is not a directory")
		}
	} else if !info.Mode().IsRegular() {
		return "", nil, errors.New("path is not a regular file")
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", nil, err
	}
	if resolved != path {
		return "", nil, errors.New("path traverses a symlink")
	}
	return path, info, nil
}

func publicationPathWithin(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && (relative == "." ||
		(relative != ".." && !filepath.IsAbs(relative) &&
			!strings.HasPrefix(relative, ".."+string(filepath.Separator))))
}
