// Package releaseartifacts builds and validates deterministic ScopeSifter
// release archives without relying on a shell script.
package releaseartifacts

import (
	"archive/tar"
	"archive/zip"
	"bufio"
	"bytes"
	"compress/gzip"
	"crypto/sha1"
	"crypto/sha256"
	"debug/buildinfo"
	"debug/elf"
	"debug/macho"
	"debug/pe"
	"errors"
	"fmt"
	"hash"
	"io"
	"io/fs"
	"os"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yapless/scopesifter/internal/gitdiffcontract"
	"github.com/yapless/scopesifter/internal/processpolicy"
	"golang.org/x/mod/modfile"
)

const noticeName = "THIRD_PARTY_NOTICES.md"

const releaseRepository = "yapless/scopesifter"

const (
	maximumReleaseTreeEntries      = 100_000
	maximumReleaseTreeListingBytes = 16 << 20
	maximumReleaseBlobBytes        = 128 << 20
	maximumReleaseTreeBytes        = 512 << 20
	maximumReleaseGitErrorBytes    = 1 << 20
)

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
	commit, err := validateReleaseCheckout(root, refName)
	if err != nil {
		return err
	}
	destination := filepath.Join(root, "dist")
	if _, err := os.Lstat(destination); err == nil {
		return fmt.Errorf("release destination already exists: %s", destination)
	} else if !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("inspect release destination: %w", err)
	}

	workRoot, err := os.MkdirTemp("", "scopesifter-release-artifacts-")
	if err != nil {
		return fmt.Errorf("create release staging directory: %w", err)
	}
	defer os.RemoveAll(workRoot)
	sourceRoot := filepath.Join(workRoot, "source")
	if err := os.Mkdir(sourceRoot, 0o700); err != nil {
		return fmt.Errorf("create committed release source directory: %w", err)
	}
	if err := materializeReleaseTree(root, sourceRoot, commit); err != nil {
		return fmt.Errorf("materialize committed release source: %w", err)
	}
	if err := requireRegularFile(filepath.Join(sourceRoot, "go.mod")); err != nil {
		return fmt.Errorf("validate committed repository root: %w", err)
	}
	if err := validateReleaseModule(filepath.Join(sourceRoot, "go.mod")); err != nil {
		return err
	}
	noticePath := filepath.Join(sourceRoot, noticeName)
	if err := requireRegularFile(noticePath); err != nil {
		return fmt.Errorf("validate committed third-party notice: %w", err)
	}
	notice, err := os.ReadFile(noticePath)
	if err != nil {
		return fmt.Errorf("read committed third-party notice: %w", err)
	}
	stagedDist := filepath.Join(workRoot, "dist")
	if err := os.Mkdir(stagedDist, 0o755); err != nil {
		return fmt.Errorf("create staged dist directory: %w", err)
	}

	for _, item := range targets {
		if err := buildTarget(sourceRoot, workRoot, stagedDist, noticePath, version, commit, item); err != nil {
			return err
		}
	}
	if err := writeChecksums(stagedDist); err != nil {
		return err
	}
	if err := validateArtifactSet(stagedDist, version, commit, notice); err != nil {
		return err
	}
	publishRoot, err := os.MkdirTemp(root, ".release-publish-")
	if err != nil {
		return fmt.Errorf("create release publication directory: %w", err)
	}
	defer os.RemoveAll(publishRoot)
	if err := copyArtifactDirectory(stagedDist, publishRoot); err != nil {
		return err
	}
	if err := os.Chmod(publishRoot, 0o755); err != nil {
		return fmt.Errorf("set release destination mode: %w", err)
	}
	if err := os.Rename(publishRoot, destination); err != nil {
		return fmt.Errorf("publish staged release artifacts: %w", err)
	}
	return nil
}

func copyArtifactDirectory(source, destination string) error {
	entries, err := os.ReadDir(source)
	if err != nil {
		return fmt.Errorf("read validated artifact directory: %w", err)
	}
	for _, entry := range entries {
		if entry.IsDir() {
			return fmt.Errorf("unexpected directory in validated artifacts: %s", entry.Name())
		}
		data, err := os.ReadFile(filepath.Join(source, entry.Name()))
		if err != nil {
			return fmt.Errorf("read validated artifact %s: %w", entry.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(destination, entry.Name()), data, 0o644); err != nil {
			return fmt.Errorf("copy validated artifact %s: %w", entry.Name(), err)
		}
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
	commit, err := validateReleaseCheckout(root, refName)
	if err != nil {
		return err
	}
	dist := filepath.Join(root, "dist")
	sourceRoot, err := os.MkdirTemp("", "scopesifter-release-source-")
	if err != nil {
		return fmt.Errorf("create committed release source directory: %w", err)
	}
	defer os.RemoveAll(sourceRoot)
	if err := materializeReleaseTree(root, sourceRoot, commit); err != nil {
		return fmt.Errorf("materialize committed release source: %w", err)
	}
	if err := validateReleaseModule(filepath.Join(sourceRoot, "go.mod")); err != nil {
		return err
	}
	notice, err := os.ReadFile(filepath.Join(sourceRoot, noticeName))
	if err != nil {
		return fmt.Errorf("read committed third-party notice: %w", err)
	}
	if err := validateArtifactSet(dist, version, commit, notice); err != nil {
		return fmt.Errorf("refuse unvalidated release artifacts: %w", err)
	}
	entries, err := os.ReadDir(dist)
	if err != nil {
		return fmt.Errorf("read release artifacts: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		names = append(names, entry.Name())
	}
	args := releaseCreateArguments(refName, dist, names)
	ghEnvironment, err := releasePublishEnvironment(os.Environ())
	if err != nil {
		return err
	}
	command, ghFile, err := processpolicy.NativeCommand("gh", args...)
	if err != nil {
		return fmt.Errorf("pin native GitHub CLI: %w", err)
	}
	command.Dir = root
	command.Env = ghEnvironment
	command.Stdin = nil
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	runErr := command.Run()
	closeErr := ghFile.Close()
	if runErr != nil {
		return fmt.Errorf("publish GitHub release: %w", runErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close native GitHub CLI image: %w", closeErr)
	}
	return nil
}

func releaseCreateArguments(refName, dist string, names []string) []string {
	args := []string{"release", "create", refName, "--repo", releaseRepository}
	for _, name := range names {
		args = append(args, filepath.Join(dist, name))
	}
	return append(args,
		"--generate-notes",
		"--title", "ScopeSifter "+refName,
		"--verify-tag",
	)
}

func buildTarget(root, workRoot, dist, noticePath, version, commit string, item target) error {
	binaryName := "scopesifter"
	if item.goos == "windows" {
		binaryName += ".exe"
	}
	binaryPath := filepath.Join(workRoot, binaryName)
	arguments := []string{
		"build", "-mod=readonly", "-trimpath", "-buildvcs=false",
		"-ldflags=-s -w -X main.releaseRevision=" + commit,
		"-o", binaryPath, "./cmd/scopesifter",
	}
	command, goFile, err := processpolicy.NativeCommand("go", arguments...)
	if err != nil {
		return fmt.Errorf("pin native Go tool: %w", err)
	}
	command.Dir = root
	command.Env = releaseBuildEnvironment(os.Environ(), item, workRoot)
	command.Stdout = os.Stdout
	command.Stderr = os.Stderr
	runErr := command.Run()
	closeErr := goFile.Close()
	if runErr != nil {
		return fmt.Errorf("build %s/%s binary: %w", item.goos, item.goarch, runErr)
	}
	if closeErr != nil {
		return fmt.Errorf("close native Go image: %w", closeErr)
	}
	binary, err := os.ReadFile(binaryPath)
	if err != nil {
		return fmt.Errorf("read %s/%s binary: %w", item.goos, item.goarch, err)
	}
	if err := validateExecutable(binary, item, commit); err != nil {
		return fmt.Errorf("validate built %s/%s binary: %w", item.goos, item.goarch, err)
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
	compressed.ModTime = time.Unix(0, 0).UTC()
	compressed.OS = 255
	archive := tar.NewWriter(compressed)
	for _, entry := range []struct {
		name string
		data []byte
		mode int64
	}{{binaryName, binary, 0o755}, {noticeName, notice, 0o644}} {
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
		data []byte
		mode fs.FileMode
	}{{binaryName, binary, 0o755}, {noticeName, notice, 0o644}} {
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

func validateArtifactSet(dist, version, commit string, notice []byte) error {
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
		if err := requireRegularFile(filepath.Join(dist, entry.Name())); err != nil {
			return fmt.Errorf("release artifact %s is unsafe: %w", entry.Name(), err)
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
			if err := validateTarGzip(filepath.Join(dist, name), notice, commit); err != nil {
				return err
			}
		case strings.HasSuffix(name, ".zip"):
			if err := validateZip(filepath.Join(dist, name), notice, commit); err != nil {
				return err
			}
		}
	}
	return nil
}

func validateTarGzip(path string, notice []byte, commit string) (returnErr error) {
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open tar archive: %w", err)
	}
	defer func() {
		if err := file.Close(); returnErr == nil && err != nil {
			returnErr = fmt.Errorf("close tar archive: %w", err)
		}
	}()
	compressed, err := gzip.NewReader(file)
	if err != nil {
		return fmt.Errorf("open gzip stream: %w", err)
	}
	defer func() {
		if err := compressed.Close(); returnErr == nil && err != nil {
			returnErr = fmt.Errorf("close gzip stream: %w", err)
		}
	}()
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
		if err := validateArchiveEntry(header.Name, fs.FileMode(header.Mode), header.Size, archive, notice, targetFromArchivePath(path), commit); err != nil {
			return fmt.Errorf("validate tar entry: %w", err)
		}
	}
	return validateArchiveNames(names, "scopesifter")
}

func validateZip(path string, notice []byte, commit string) (returnErr error) {
	archive, err := zip.OpenReader(path)
	if err != nil {
		return fmt.Errorf("open zip archive: %w", err)
	}
	defer func() {
		if err := archive.Close(); returnErr == nil && err != nil {
			returnErr = fmt.Errorf("close zip archive: %w", err)
		}
	}()
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
		entryErr := validateArchiveEntry(entry.Name, entry.Mode(), int64(entry.UncompressedSize64), reader, notice, targetFromArchivePath(path), commit)
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

func validateArchiveEntry(name string, mode fs.FileMode, size int64, reader io.Reader, notice []byte, expected target, commit string) error {
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
		binary, err := io.ReadAll(io.LimitReader(reader, size+1))
		if err != nil {
			return fmt.Errorf("read %s: %w", name, err)
		}
		if int64(len(binary)) != size {
			return fmt.Errorf("%s content length = %d, want %d", name, len(binary), size)
		}
		if err := validateExecutable(binary, expected, commit); err != nil {
			return fmt.Errorf("validate %s target: %w", name, err)
		}
	default:
		return fmt.Errorf("unexpected archive entry: %s", name)
	}
	return nil
}

func targetFromArchivePath(path string) target {
	name := filepath.Base(path)
	for _, item := range targets {
		marker := "_" + item.goos + "_" + item.goarch
		if strings.Contains(name, marker+".") {
			return item
		}
	}
	return target{}
}

func validateExecutable(binary []byte, expected target, commit string) error {
	if expected.goos == "" || expected.goarch == "" {
		return errors.New("archive filename does not identify a supported target")
	}
	reader := bytes.NewReader(binary)
	switch expected.goos {
	case "linux":
		file, err := elf.NewFile(reader)
		if err != nil {
			return fmt.Errorf("parse ELF: %w", err)
		}
		want := elf.EM_X86_64
		if expected.goarch == "arm64" {
			want = elf.EM_AARCH64
		}
		machine := file.Machine
		if err := file.Close(); err != nil {
			return fmt.Errorf("close ELF: %w", err)
		}
		if machine != want {
			return fmt.Errorf("ELF machine = %s, want %s", machine, want)
		}
	case "darwin":
		file, err := macho.NewFile(reader)
		if err != nil {
			return fmt.Errorf("parse Mach-O: %w", err)
		}
		want := macho.CpuAmd64
		if expected.goarch == "arm64" {
			want = macho.CpuArm64
		}
		cpu := file.Cpu
		if err := file.Close(); err != nil {
			return fmt.Errorf("close Mach-O: %w", err)
		}
		if cpu != want {
			return fmt.Errorf("Mach-O CPU = %s, want %s", cpu, want)
		}
	case "windows":
		file, err := pe.NewFile(reader)
		if err != nil {
			return fmt.Errorf("parse PE: %w", err)
		}
		machine := file.Machine
		if err := file.Close(); err != nil {
			return fmt.Errorf("close PE: %w", err)
		}
		if expected.goarch != "amd64" || machine != pe.IMAGE_FILE_MACHINE_AMD64 {
			return fmt.Errorf("PE machine = %#x, want AMD64", machine)
		}
	default:
		return fmt.Errorf("unsupported operating system %q", expected.goos)
	}
	info, err := buildinfo.Read(bytes.NewReader(binary))
	if err != nil {
		return fmt.Errorf("read Go build information: %w", err)
	}
	if info.Main.Path != "github.com/yapless/scopesifter" {
		return fmt.Errorf("go main module = %q, want github.com/yapless/scopesifter", info.Main.Path)
	}
	settings := make(map[string]string, len(info.Settings))
	for _, setting := range info.Settings {
		settings[setting.Key] = setting.Value
	}
	if settings["vcs.revision"] != "" || settings["vcs.modified"] != "" {
		return errors.New("release binary unexpectedly contains ambient VCS build settings")
	}
	if occurrences := bytes.Count(binary, []byte(commit)); occurrences != 1 {
		return fmt.Errorf("release revision occurs %d times in binary, want exactly once", occurrences)
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
	if len(refName) < 2 || refName[0] != 'v' || !validSemanticVersion(refName[1:]) {
		return "", fmt.Errorf("release ref must be a canonical v-prefixed semantic version: %q", refName)
	}
	return refName[1:], nil
}

func validSemanticVersion(version string) bool {
	coreAndPre, build, hasBuild := strings.Cut(version, "+")
	if hasBuild && (!validIdentifiers(build, false) || strings.Contains(build, "+")) {
		return false
	}
	core, prerelease, hasPrerelease := strings.Cut(coreAndPre, "-")
	if hasPrerelease && !validIdentifiers(prerelease, true) {
		return false
	}
	parts := strings.Split(core, ".")
	if len(parts) != 3 {
		return false
	}
	for _, part := range parts {
		if !validNumericIdentifier(part, false) {
			return false
		}
	}
	return true
}

func validateReleaseCheckout(root, refName string) (string, error) {
	configuration, err := gitOutputBytes(root, processpolicy.GitRepositoryConfigArguments()...)
	if err != nil {
		return "", fmt.Errorf("inspect release repository Git configuration: %w", err)
	}
	if err := processpolicy.ValidateGitWorktreeConfig(configuration); err != nil {
		return "", fmt.Errorf("reject release repository Git configuration: %w", err)
	}
	head, err := gitOutput(root, "rev-parse", "--verify", "HEAD^{commit}")
	if err != nil {
		return "", fmt.Errorf("resolve release checkout HEAD: %w", err)
	}
	tag, err := gitOutput(root, "rev-parse", "--verify", "refs/tags/"+refName+"^{commit}")
	if err != nil {
		return "", fmt.Errorf("resolve release tag %s: %w", refName, err)
	}
	if head != tag {
		return "", fmt.Errorf("release checkout HEAD %s does not match tag %s at %s", head, refName, tag)
	}
	if !validObjectID(head) {
		return "", fmt.Errorf("release commit has invalid object ID %q", head)
	}
	return head, nil
}

func gitOutput(root string, arguments ...string) (string, error) {
	output, err := gitOutputBytes(root, arguments...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func gitOutputBytes(root string, arguments ...string) ([]byte, error) {
	return gitOutputBytesLimit(root, maximumReleaseTreeListingBytes, arguments...)
}

func gitOutputBytesLimit(root string, limit int, arguments ...string) ([]byte, error) {
	if limit <= 0 || limit > maximumReleaseBlobBytes {
		return nil, errors.New("release Git output limit is invalid")
	}
	safeArguments := arguments
	if !processpolicy.IsGitRepositoryConfigQuery(arguments) {
		safeArguments = append(gitdiffcontract.InvocationPrefix(), arguments...)
	}
	if err := processpolicy.ValidateGit(safeArguments...); err != nil {
		return nil, fmt.Errorf("reject release Git invocation: %w", err)
	}
	command, gitFile, err := processpolicy.NativeCommand("git", safeArguments...)
	if err != nil {
		return nil, fmt.Errorf("pin native Git: %w", err)
	}
	command.Dir = root
	command.Env = gitdiffcontract.Environment(os.DevNull)
	stdout := &releaseBoundedBuffer{limit: limit}
	stderr := &releaseBoundedBuffer{limit: maximumReleaseGitErrorBytes}
	command.Stdout = stdout
	command.Stderr = stderr
	runErr := command.Run()
	closeErr := gitFile.Close()
	if runErr != nil {
		return nil, fmt.Errorf("git %s: %w: %s", strings.Join(arguments, " "), runErr, bytes.TrimSpace(stderr.Bytes()))
	}
	if closeErr != nil {
		return nil, fmt.Errorf("close native Git image: %w", closeErr)
	}
	return stdout.Bytes(), nil
}

type releaseBoundedBuffer struct {
	data  []byte
	limit int
}

func (buffer *releaseBoundedBuffer) Write(data []byte) (int, error) {
	written := len(data)
	remaining := buffer.limit - len(buffer.data)
	if remaining <= 0 {
		return 0, errors.New("release Git output exceeded its limit")
	}
	if len(data) > remaining {
		buffer.data = append(buffer.data, data[:remaining]...)
		return remaining, errors.New("release Git output exceeded its limit")
	}
	buffer.data = append(buffer.data, data...)
	return written, nil
}

func (buffer *releaseBoundedBuffer) Bytes() []byte {
	return bytes.Clone(buffer.data)
}

type releaseTreeEntry struct {
	objectID string
	path     string
	mode     fs.FileMode
}

func materializeReleaseTree(repositoryRoot, destination, commit string) error {
	listing, err := gitOutputBytes(
		repositoryRoot,
		"ls-tree", "-r", "-z", "--full-tree", commit, "--",
	)
	if err != nil {
		return fmt.Errorf("list committed release tree: %w", err)
	}
	entries, err := parseReleaseTree(listing)
	if err != nil {
		return err
	}
	objectFormat, err := gitOutput(repositoryRoot, "rev-parse", "--show-object-format")
	if err != nil {
		return fmt.Errorf("read release repository object format: %w", err)
	}
	var total int64
	for _, entry := range entries {
		content, err := gitOutputBytesLimit(
			repositoryRoot,
			maximumReleaseBlobBytes,
			"cat-file", "blob", entry.objectID,
		)
		if err != nil {
			return fmt.Errorf("read committed release path %s: %w", entry.path, err)
		}
		if int64(len(content)) > maximumReleaseBlobBytes ||
			int64(len(content)) > maximumReleaseTreeBytes-total {
			return fmt.Errorf("committed release source exceeds its size limit at %s", entry.path)
		}
		if err := validateGitBlobID(objectFormat, entry.objectID, content); err != nil {
			return fmt.Errorf("authenticate committed release path %s: %w", entry.path, err)
		}
		total += int64(len(content))
		target := filepath.Join(destination, filepath.FromSlash(entry.path))
		if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
			return fmt.Errorf("create committed release directory for %s: %w", entry.path, err)
		}
		file, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, entry.mode)
		if err != nil {
			return fmt.Errorf("create committed release path %s: %w", entry.path, err)
		}
		_, writeErr := file.Write(content)
		closeErr := file.Close()
		if writeErr != nil || closeErr != nil {
			return fmt.Errorf("write committed release path %s: %w", entry.path, errors.Join(writeErr, closeErr))
		}
	}
	return nil
}

func validateReleaseModule(path string) error {
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read committed go.mod: %w", err)
	}
	module, err := modfile.Parse("go.mod", content, nil)
	if err != nil {
		return fmt.Errorf("parse committed go.mod: %w", err)
	}
	for _, replacement := range module.Replace {
		if replacement.New.Version == "" {
			return fmt.Errorf(
				"committed go.mod replacement for %s uses a local filesystem path",
				replacement.Old.Path,
			)
		}
	}
	return nil
}

func parseReleaseTree(listing []byte) ([]releaseTreeEntry, error) {
	if len(listing) != 0 && listing[len(listing)-1] != 0 {
		return nil, errors.New("committed release tree listing is not NUL terminated")
	}
	entries := make([]releaseTreeEntry, 0)
	seen := make(map[string]struct{})
	for record := range bytes.SplitSeq(listing, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		if len(entries) >= maximumReleaseTreeEntries {
			return nil, errors.New("committed release tree has too many entries")
		}
		metadata, rawPath, found := bytes.Cut(record, []byte{'\t'})
		fields := bytes.Fields(metadata)
		if !found || len(fields) != 3 || string(fields[1]) != "blob" ||
			(string(fields[0]) != "100644" && string(fields[0]) != "100755") {
			return nil, fmt.Errorf("unsupported committed release tree entry %q", record)
		}
		path := string(rawPath)
		if !safeReleaseTreePath(path) {
			return nil, fmt.Errorf("unsafe committed release path %q", path)
		}
		if _, duplicate := seen[path]; duplicate {
			return nil, fmt.Errorf("duplicate committed release path %q", path)
		}
		seen[path] = struct{}{}
		objectID := string(fields[2])
		if !validObjectID(objectID) {
			return nil, fmt.Errorf("invalid committed release object ID %q", objectID)
		}
		mode := fs.FileMode(0o644)
		if string(fields[0]) == "100755" {
			mode = 0o755
		}
		entries = append(entries, releaseTreeEntry{mode: mode, objectID: objectID, path: path})
	}
	if len(entries) == 0 {
		return nil, errors.New("committed release tree is empty")
	}
	return entries, nil
}

func safeReleaseTreePath(value string) bool {
	if value == "" || !utf8.ValidString(value) || strings.ContainsAny(value, "\x00\r\n\\") ||
		filepath.IsAbs(filepath.FromSlash(value)) || pathpkg.Clean(value) != value ||
		value == "." || value == ".." || strings.HasPrefix(value, "../") {
		return false
	}
	for _, component := range strings.Split(value, "/") {
		if strings.EqualFold(component, ".git") {
			return false
		}
	}
	return true
}

func validateGitBlobID(objectFormat, objectID string, content []byte) error {
	var digest hash.Hash
	switch objectFormat {
	case "sha1":
		digest = sha1.New() //nolint:gosec // Git SHA-1 repositories require SHA-1 object authentication.
	case "sha256":
		digest = sha256.New()
	default:
		return fmt.Errorf("unsupported Git object format %q", objectFormat)
	}
	_, _ = fmt.Fprintf(digest, "blob %d%c", len(content), byte(0))
	_, _ = digest.Write(content)
	if actual := fmt.Sprintf("%x", digest.Sum(nil)); actual != objectID {
		return fmt.Errorf("git blob ID = %s, want %s", actual, objectID)
	}
	return nil
}

func validObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if !strings.ContainsRune("0123456789abcdef", character) {
			return false
		}
	}
	return true
}

func validIdentifiers(value string, rejectNumericLeadingZero bool) bool {
	if value == "" {
		return false
	}
	for _, identifier := range strings.Split(value, ".") {
		if identifier == "" {
			return false
		}
		numeric := true
		for _, character := range identifier {
			if character < '0' || character > '9' {
				numeric = false
			}
			if (character < 'a' || character > 'z') &&
				(character < 'A' || character > 'Z') &&
				(character < '0' || character > '9') && character != '-' {
				return false
			}
		}
		if rejectNumericLeadingZero && numeric && !validNumericIdentifier(identifier, false) {
			return false
		}
	}
	return true
}

func validNumericIdentifier(value string, allowLeadingZero bool) bool {
	if value == "" || (!allowLeadingZero && len(value) > 1 && value[0] == '0') {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
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

func releaseBuildEnvironment(environment []string, item target, cacheRoot string) []string {
	clean := make([]string, 0, len(environment))
	for _, entry := range environment {
		name, _, found := strings.Cut(entry, "=")
		if !found {
			continue
		}
		if releaseGoEnvironmentVariableControlled(name) {
			continue
		}
		clean = append(clean, entry)
	}
	return replaceEnvironment(clean, map[string]string{
		"CGO_ENABLED":  "0",
		"GO111MODULE":  "on",
		"GOAMD64":      "v1",
		"GOARCH":       item.goarch,
		"GOARM64":      "v8.0",
		"GOAUTH":       "off",
		"GOENV":        "off",
		"GOEXPERIMENT": "",
		"GOFLAGS":      "-mod=readonly -trimpath -buildvcs=false",
		"GOCACHE":      filepath.Join(cacheRoot, "go-build-cache"),
		"GOMODCACHE":   filepath.Join(cacheRoot, "go-module-cache"),
		"GONOPROXY":    "none",
		"GONOSUMDB":    "",
		"GOOS":         item.goos,
		"GOPRIVATE":    "",
		"GOPROXY":      "https://proxy.golang.org",
		"GOSUMDB":      "sum.golang.org",
		"GOTOOLCHAIN":  "local",
		"GOVCS":        "*:off",
		"GOWORK":       "off",
	})
}

func releaseGoEnvironmentVariableControlled(name string) bool {
	if strings.HasPrefix(name, "GO") || strings.HasPrefix(name, "CGO") {
		return true
	}
	switch name {
	case "AR", "CC", "CXX", "FC", "GCCGO", "PKG_CONFIG":
		return true
	default:
		return false
	}
}

func releasePublishEnvironment(environment []string) ([]string, error) {
	token := ""
	for _, entry := range environment {
		name, value, found := strings.Cut(entry, "=")
		if found && name == "GH_TOKEN" {
			token = value
		}
	}
	if token == "" || strings.ContainsAny(token, "\x00\r\n") {
		return nil, errors.New("release publication requires a valid GH_TOKEN")
	}
	return []string{
		"GH_HOST=github.com",
		"GH_PROMPT_DISABLED=1",
		"GH_TOKEN=" + token,
		"HOME=/",
		"LANG=C",
		"LC_ALL=C",
		"NO_COLOR=1",
		"PAGER=cat",
		"PATH=",
		"TZ=UTC",
		"XDG_CONFIG_HOME=/",
	}, nil
}
