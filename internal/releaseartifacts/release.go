// Package releaseartifacts builds and validates deterministic ScopeSifter
// release archives without relying on a shell script.
package releaseartifacts

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const noticeName = "THIRD_PARTY_NOTICES.md"

var archiveTime = time.Date(1980, time.January, 1, 0, 0, 0, 0, time.UTC)

type target struct {
	goos   string
	goarch string
}

var targets = []target{
	{goos: "linux", goarch: "amd64"},
	{goos: "linux", goarch: "arm64"},
	{goos: "darwin", goarch: "amd64"},
	{goos: "darwin", goarch: "arm64"},
	{goos: "windows", goarch: "amd64"},
}

// Build creates the complete release artifact set under root/dist. The
// destination must not already exist, which prevents stale files from being
// included in a release.
func Build(root, refName string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve repository root: %w", err)
	}
	version, err := versionFromRef(refName)
	if err != nil {
		return err
	}
	if err := requireRegularFile(filepath.Join(root, "go.mod")); err != nil {
		return fmt.Errorf("validate repository root: %w", err)
	}
	noticePath := filepath.Join(root, noticeName)
	if err := requireRegularFile(noticePath); err != nil {
		return fmt.Errorf("validate third-party notice: %w", err)
	}
	notice, err := os.ReadFile(noticePath)
	if err != nil {
		return fmt.Errorf("read third-party notice: %w", err)
	}
	destination := filepath.Join(root, "dist")
	if _, err := os.Lstat(destination); err == nil {
		return fmt.Errorf("release destination already exists: %s", destination)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("inspect release destination: %w", err)
	}

	workRoot, err := os.MkdirTemp(root, ".release-artifacts-")
	if err != nil {
		return fmt.Errorf("create release staging directory: %w", err)
	}
	defer os.RemoveAll(workRoot)
	stagedDist := filepath.Join(workRoot, "dist")
	if err := os.Mkdir(stagedDist, 0o755); err != nil {
		return fmt.Errorf("create staged dist directory: %w", err)
	}

	for _, item := range targets {
		if err := buildTarget(root, workRoot, stagedDist, noticePath, version, item); err != nil {
			return err
		}
	}
	if err := writeChecksums(stagedDist); err != nil {
		return err
	}
	if err := validateArtifactSet(stagedDist, version, notice); err != nil {
		return err
	}
	if err := os.Rename(stagedDist, destination); err != nil {
		return fmt.Errorf("publish staged release artifacts: %w", err)
	}
	return nil
}

// Publish invokes the GitHub CLI with the exact validated artifacts from
// root/dist. Authentication is supplied by the caller through GH_TOKEN.
func Publish(root, refName string) error {
	root, err := filepath.Abs(root)
	if err != nil {
		return fmt.Errorf("resolve repository root: %w", err)
	}
	version, err := versionFromRef(refName)
	if err != nil {
		return err
	}
	dist := filepath.Join(root, "dist")
	notice, err := os.ReadFile(filepath.Join(root, noticeName))
	if err != nil {
		return fmt.Errorf("read third-party notice: %w", err)
	}
	if err := validateArtifactSet(dist, version, notice); err != nil {
		return fmt.Errorf("refuse unvalidated release artifacts: %w", err)
	}
	entries, err := os.ReadDir(dist)
	if err != nil {
		return fmt.Errorf("read release artifacts: %w", err)
	}
	args := []string{"release", "create", refName}
	for _, entry := range entries {
		args = append(args, filepath.Join(dist, entry.Name()))
	}
	args = append(args,
		"--generate-notes",
		"--title", "ScopeSifter "+refName,
		"--verify-tag",
	)
	command := exec.Command("gh", args...)
	command.Dir = root
	command.Stdin = os.Stdin
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("publish GitHub release: %w", err)
	}
	return nil
}

func buildTarget(root, workRoot, dist, noticePath, version string, item target) error {
	binaryName := "scopesifter"
	if item.goos == "windows" {
		binaryName += ".exe"
	}
	binaryPath := filepath.Join(workRoot, binaryName)
	command := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", binaryPath, "./cmd/scopesifter")
	command.Dir = root
	command.Env = replaceEnvironment(os.Environ(), map[string]string{
		"CGO_ENABLED": "0",
		"GOOS":        item.goos,
		"GOARCH":      item.goarch,
	})
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	if err := command.Run(); err != nil {
		return fmt.Errorf("build %s/%s binary: %w", item.goos, item.goarch, err)
	}
	binary, err := os.ReadFile(binaryPath)
	if err != nil {
		return fmt.Errorf("read %s/%s binary: %w", item.goos, item.goarch, err)
	}
	notice, err := os.ReadFile(noticePath)
	if err != nil {
		return fmt.Errorf("read third-party notice: %w", err)
	}
	base := fmt.Sprintf("scopesifter_%s_%s_%s", version, item.goos, item.goarch)
	if item.goos == "windows" {
		if err := writeZip(filepath.Join(dist, base+".zip"), binaryName, binary, notice); err != nil {
			return err
		}
	} else if err := writeTarGzip(filepath.Join(dist, base+".tar.gz"), binaryName, binary, notice); err != nil {
		return err
	}
	if err := os.Remove(binaryPath); err != nil {
		return fmt.Errorf("remove staged %s/%s binary: %w", item.goos, item.goarch, err)
	}
	return nil
}

func writeTarGzip(path, binaryName string, binary, notice []byte) (returnErr error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("create tar archive: %w", err)
	}
	defer func() {
		if err := file.Close(); returnErr == nil && err != nil {
			returnErr = fmt.Errorf("close tar archive: %w", err)
		}
	}()
	compressed, err := gzip.NewWriterLevel(file, gzip.BestCompression)
	if err != nil {
		return fmt.Errorf("create gzip writer: %w", err)
	}
	compressed.Header.ModTime = time.Unix(0, 0).UTC()
	compressed.Header.OS = 255
	archive := tar.NewWriter(compressed)
	for _, entry := range []struct {
		name string
		mode int64
		data []byte
	}{{binaryName, 0o755, binary}, {noticeName, 0o644, notice}} {
		header := &tar.Header{
			Name:       entry.name,
			Mode:       entry.mode,
			Size:       int64(len(entry.data)),
			ModTime:    time.Unix(0, 0).UTC(),
			AccessTime: time.Time{},
			ChangeTime: time.Time{},
			Typeflag:   tar.TypeReg,
			Format:     tar.FormatPAX,
		}
		if err := archive.WriteHeader(header); err != nil {
			return fmt.Errorf("write tar header %s: %w", entry.name, err)
		}
		if _, err := archive.Write(entry.data); err != nil {
			return fmt.Errorf("write tar entry %s: %w", entry.name, err)
		}
	}
	if err := archive.Close(); err != nil {
		return fmt.Errorf("close tar writer: %w", err)
	}
	if err := compressed.Close(); err != nil {
		return fmt.Errorf("close gzip writer: %w", err)
	}
	return nil
}

func writeZip(path, binaryName string, binary, notice []byte) (returnErr error) {
	file, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("create zip archive: %w", err)
	}
	defer func() {
		if err := file.Close(); returnErr == nil && err != nil {
			returnErr = fmt.Errorf("close zip archive: %w", err)
		}
	}()
	archive := zip.NewWriter(file)
	for _, entry := range []struct {
		name string
		mode fs.FileMode
		data []byte
	}{{binaryName, 0o755, binary}, {noticeName, 0o644, notice}} {
		header := &zip.FileHeader{Name: entry.name, Method: zip.Deflate}
		header.SetMode(entry.mode)
		header.Modified = archiveTime
		writer, err := archive.CreateHeader(header)
		if err != nil {
			return fmt.Errorf("create zip entry %s: %w", entry.name, err)
		}
		if _, err := writer.Write(entry.data); err != nil {
			return fmt.Errorf("write zip entry %s: %w", entry.name, err)
		}
	}
	if err := archive.Close(); err != nil {
		return fmt.Errorf("close zip writer: %w", err)
	}
	return nil
}

func writeChecksums(dist string) error {
	entries, err := os.ReadDir(dist)
	if err != nil {
		return fmt.Errorf("read staged artifacts: %w", err)
	}
	lines := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			return fmt.Errorf("unexpected directory in staged artifacts: %s", entry.Name())
		}
		data, err := os.ReadFile(filepath.Join(dist, entry.Name()))
		if err != nil {
			return fmt.Errorf("read staged artifact %s: %w", entry.Name(), err)
		}
		digest := sha256.Sum256(data)
		lines = append(lines, fmt.Sprintf("%x  %s\n", digest, entry.Name()))
	}
	sort.Strings(lines)
	if err := os.WriteFile(filepath.Join(dist, "SHA256SUMS"), []byte(strings.Join(lines, "")), 0o644); err != nil {
		return fmt.Errorf("write SHA256SUMS: %w", err)
	}
	return nil
}

func validateArtifactSet(dist, version string, notice []byte) error {
	entries, err := os.ReadDir(dist)
	if err != nil {
		return fmt.Errorf("read dist directory: %w", err)
	}
	want := map[string]bool{"SHA256SUMS": false}
	for _, item := range targets {
		extension := ".tar.gz"
		if item.goos == "windows" {
			extension = ".zip"
		}
		want[fmt.Sprintf("scopesifter_%s_%s_%s%s", version, item.goos, item.goarch, extension)] = false
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return fmt.Errorf("unexpected directory in dist: %s", entry.Name())
		}
		if _, ok := want[entry.Name()]; !ok {
			return fmt.Errorf("unexpected release artifact: %s", entry.Name())
		}
		want[entry.Name()] = true
	}
	for name, found := range want {
		if !found {
			return fmt.Errorf("missing release artifact: %s", name)
		}
	}
	if err := verifyChecksums(dist); err != nil {
		return err
	}
	for name := range want {
		switch {
		case strings.HasSuffix(name, ".tar.gz"):
			if err := validateTarGzip(filepath.Join(dist, name), notice); err != nil {
				return err
			}
		case strings.HasSuffix(name, ".zip"):
			if err := validateZip(filepath.Join(dist, name), notice); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateTarGzip(path string, notice []byte) error {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open tar archive: %w", err)
	}
	defer file.Close()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("open gzip stream: %w", err)
	}
	defer compressed.Close()
	names := make(map[string]struct{}, 2)
	archive := tar.NewReader(compressed)
	for {
		header, err := archive.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read tar archive: %w", err)
		}
		if header.Typeflag != tar.TypeReg {
			return fmt.Errorf("non-regular tar entry: %s", header.Name)
		}
		if _, exists := names[header.Name]; exists {
			return fmt.Errorf("duplicate tar entry: %s", header.Name)
		}
		names[header.Name] = struct{}{}
		if err := validateArchiveEntry(header.Name, fs.FileMode(header.Mode), header.Size, archive, notice); err != nil {
			return fmt.Errorf("validate tar entry: %w", err)
		}
	}
	return validateArchiveNames(names, "scopesifter")
}

func validateZip(path string, notice []byte) error {
	archive, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("open zip archive: %w", err)
	}
	defer archive.Close()
	names := make(map[string]struct{}, len(archive.File))
	for _, entry := range archive.File {
		if !entry.Mode().IsRegular() {
			return fmt.Errorf("non-regular zip entry: %s", entry.Name)
		}
		if _, exists := names[entry.Name]; exists {
			return fmt.Errorf("duplicate zip entry: %s", entry.Name)
		}
		names[entry.Name] = struct{}{}
		reader, err := entry.Open()
		if err != nil {
			return fmt.Errorf("open zip entry %s: %w", entry.Name, err)
		}
		entryErr := validateArchiveEntry(entry.Name, entry.Mode(), int64(entry.UncompressedSize64), reader, notice)
		closeErr := reader.Close()
		if entryErr != nil {
			return fmt.Errorf("validate zip entry: %w", entryErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close zip entry %s: %w", entry.Name, closeErr)
		}
	}
	return validateArchiveNames(names, "scopesifter.exe")
}

func validateArchiveEntry(name string, mode fs.FileMode, size int64, reader io.Reader, notice []byte) error {
	switch name {
	case noticeName:
		if mode.Perm() != 0o644 {
			return fmt.Errorf("%s mode = %04o, want 0644", name, mode.Perm())
		}
		if size != int64(len(notice)) {
			return fmt.Errorf("%s size = %d, want %d", name, size, len(notice))
		}
		actual, err := io.ReadAll(reader)
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		if !bytes.Equal(actual, notice) {
			return fmt.Errorf("%s content differs from repository notice", name)
		}
	case "scopesifter", "scopesifter.exe":
		if mode.Perm() != 0o755 {
			return fmt.Errorf("%s mode = %04o, want 0755", name, mode.Perm())
		}
		if size <= 0 {
			return fmt.Errorf("%s is empty", name)
		}
		if _, err := io.Copy(io.Discard, reader); err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
	default:
		return fmt.Errorf("unexpected archive entry: %s", name)
	}
	return nil
}

func validateArchiveNames(names map[string]struct{}, binaryName string) error {
	if len(names) != 2 {
		return fmt.Errorf("release archive has %d entries, want 2", len(names))
	}
	if _, ok := names[noticeName]; !ok {
		return fmt.Errorf("release archive lacks %s", noticeName)
	}
	if _, ok := names[binaryName]; !ok {
		return fmt.Errorf("release archive lacks %s", binaryName)
	}
	return nil
}

func verifyChecksums(dist string) error {
	file, err := os.Open(filepath.Join(dist, "SHA256SUMS"))
	if err != nil {
		return fmt.Errorf("open SHA256SUMS: %w", err)
	}
	defer file.Close()
	seen := make(map[string]struct{})
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		parts := strings.SplitN(scanner.Text(), "  ", 2)
		if len(parts) != 2 || len(parts[0]) != sha256.Size*2 || filepath.Base(parts[1]) != parts[1] {
			return fmt.Errorf("invalid SHA256SUMS line: %q", scanner.Text())
		}
		if _, duplicate := seen[parts[1]]; duplicate {
			return fmt.Errorf("duplicate SHA256SUMS entry: %s", parts[1])
		}
		seen[parts[1]] = struct{}{}
		data, err := os.ReadFile(filepath.Join(dist, parts[1]))
		if err != nil {
			return fmt.Errorf("read checksummed artifact %s: %w", parts[1], err)
		}
		digest := fmt.Sprintf("%x", sha256.Sum256(data))
		if digest != parts[0] {
			return fmt.Errorf("checksum mismatch for %s", parts[1])
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read SHA256SUMS: %w", err)
	}
	if len(seen) != len(targets) {
		return fmt.Errorf("SHA256SUMS has %d entries, want %d", len(seen), len(targets))
	}
	return nil
}

func versionFromRef(refName string) (string, error) {
	if !strings.HasPrefix(refName, "v") {
		return "", fmt.Errorf("release ref must start with v: %q", refName)
	}
	version := strings.ReplaceAll(strings.TrimPrefix(refName, "v"), "/", "-")
	if version == "" {
		return "", errors.New("release version is empty")
	}
	for _, character := range version {
		if (character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(character >= '0' && character <= '9') ||
			strings.ContainsRune("._-", character) {
			continue
		}
		return "", fmt.Errorf("release version contains unsafe character %q", character)
	}
	return version, nil
}

func requireRegularFile(path string) error {
	info, err := os.Lstat(path)
	if err != nil {
		return err
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("not a regular file: %s", path)
	}
	return nil
}

func replaceEnvironment(environment []string, replacements map[string]string) []string {
	result := make([]string, 0, len(environment)+len(replacements))
	for _, entry := range environment {
		name, _, found := strings.Cut(entry, "=")
		if found {
			if _, replace := replacements[name]; replace {
				continue
			}
		}
		result = append(result, entry)
	}
	keys := make([]string, 0, len(replacements))
	for key := range replacements {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		result = append(result, key+"="+replacements[key])
	}
	return result
}
