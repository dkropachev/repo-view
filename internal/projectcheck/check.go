// Package projectcheck implements repository-wide CI policy checks in Go.
package projectcheck

import (
	"bufio"
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

var bashMarkers = [][]byte{
	[]byte("#!/usr/bin/env ba" + "sh"),
	[]byte("#!/bin/ba" + "sh"),
	[]byte("#!/usr/bin/ba" + "sh"),
	[]byte("/bin/ba" + "sh"),
	[]byte("/usr/bin/ba" + "sh"),
	[]byte("shell: ba" + "sh"),
}

// ValidateJSON parses every tracked JSON file as exactly one JSON value.
func ValidateJSON(root string) error {
	paths, err := trackedFiles(root)
	if err != nil {
		return err
	}
	var failures []error
	for _, path := range paths {
		if filepath.Ext(path) != ".json" {
			continue
		}
		file, err := os.Open(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			failures = append(failures, fmt.Errorf("open %s: %w", path, err))
			continue
		}
		decoder := json.NewDecoder(bufio.NewReader(file))
		var document any
		decodeErr := decoder.Decode(&document)
		if decodeErr == nil {
			var trailing any
			if err := decoder.Decode(&trailing); err == nil {
				decodeErr = errors.New("multiple JSON values")
			} else if !errors.Is(err, io.EOF) {
				decodeErr = fmt.Errorf("read trailing JSON: %w", err)
			}
		}
		closeErr := file.Close()
		if decodeErr != nil {
			failures = append(failures, fmt.Errorf("parse %s: %w", path, decodeErr))
		} else if closeErr != nil {
			failures = append(failures, fmt.Errorf("close %s: %w", path, closeErr))
		}
	}
	return errors.Join(failures...)
}

// ValidateNoBash rejects ScopeSifter-owned Bash scripts and execution markers.
// Go test files may contain inert shell fixture bytes used to test source
// parsing and command normalization; they cannot be executable entrypoints.
func ValidateNoBash(root string) error {
	paths, err := trackedFiles(root)
	if err != nil {
		return err
	}
	var failures []error
	for _, path := range paths {
		if strings.HasSuffix(path, ".sh") {
			failures = append(failures, fmt.Errorf("tracked shell-script path: %s", path))
			continue
		}
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(path)))
		if err != nil {
			failures = append(failures, fmt.Errorf("read %s: %w", path, err))
			continue
		}
		if bytes.HasPrefix(data, []byte("#!")) && bytes.Contains(bytes.SplitN(data, []byte("\n"), 2)[0], []byte("bash")) {
			failures = append(failures, fmt.Errorf("Bash shebang: %s", path))
			continue
		}
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		for _, marker := range bashMarkers {
			if bytes.Contains(data, marker) {
				failures = append(failures, fmt.Errorf("Bash marker %q in %s", marker, path))
				break
			}
		}
	}
	return errors.Join(failures...)
}

func trackedFiles(root string) ([]string, error) {
	command := exec.Command("git", "ls-files", "-z")
	command.Dir = root
	output, err := command.Output()
	if err != nil {
		return nil, fmt.Errorf("list tracked files: %w", err)
	}
	parts := bytes.Split(output, []byte{0})
	paths := make([]string, 0, len(parts))
	for _, part := range parts {
		if len(part) == 0 {
			continue
		}
		path := string(part)
		if filepath.IsAbs(path) || filepath.Clean(path) != filepath.FromSlash(path) {
			return nil, fmt.Errorf("unsafe tracked path: %q", path)
		}
		paths = append(paths, path)
	}
	sort.Strings(paths)
	return paths, nil
}
