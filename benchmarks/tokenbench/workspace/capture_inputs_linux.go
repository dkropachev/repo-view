//go:build linux

package workspace

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"syscall"

	"github.com/yapless/scopesifter/benchmarks/tokenbench/snapshot"
	"golang.org/x/sys/unix"
)

func (pair *PairAuthority) pinCaptureInputs(
	ctx context.Context,
	authority *snapshot.Authority,
	inputs snapshot.ExecutionInputs,
) (resultErr error) {
	objectsPath := filepath.Join(inputs.GitMetadataRoot, "objects")
	paths := []string{inputs.SourceRoot, inputs.VerifierGitExecutable, objectsPath}
	expected := make([]snapshot.ManifestEntry, len(paths))
	for index, path := range paths {
		entry, err := exactSnapshotEntry(inputs, path)
		if err != nil {
			return fmt.Errorf("resolve retained snapshot role %q: %w", path, err)
		}
		expected[index] = entry
	}

	retained, err := authority.RetainPaths(ctx, paths)
	if err != nil {
		return fmt.Errorf("retain workspace snapshot inputs: %w", err)
	}
	defer func() {
		if resultErr == nil {
			return
		}
		for index := range retained {
			if retained[index].File != nil {
				resultErr = errors.Join(resultErr, retained[index].File.Close())
			}
		}
	}()
	if len(retained) != len(paths) {
		return errors.New("snapshot authority returned an incomplete retained path set")
	}
	for index := range retained {
		if !reflect.DeepEqual(retained[index].Entry, expected[index]) {
			return fmt.Errorf("retained snapshot role %q has a changed manifest identity", paths[index])
		}
	}

	sourceInfo, err := verifyRetainedSnapshotRole(
		retained[0], snapshot.ManifestKindDirectory, false,
	)
	if err != nil {
		return fmt.Errorf("verify retained source root: %w", err)
	}
	gitInfo, err := verifyRetainedSnapshotRole(
		retained[1], snapshot.ManifestKindFile, true,
	)
	if err != nil {
		return fmt.Errorf("verify retained capture Git executable: %w", err)
	}
	objectsInfo, err := verifyRetainedSnapshotRole(
		retained[2], snapshot.ManifestKindDirectory, false,
	)
	if err != nil {
		return fmt.Errorf("verify retained capture Git object directory: %w", err)
	}

	pair.sourceRoot, retained[0].File = retained[0].File, nil
	pair.verifierGit, retained[1].File = retained[1].File, nil
	pair.gitObjects, retained[2].File = retained[2].File, nil
	pair.sourceInfo = sourceInfo
	pair.verifierInfo = gitInfo
	pair.objectsInfo = objectsInfo
	return nil
}

func exactSnapshotEntry(
	inputs snapshot.ExecutionInputs,
	path string,
) (snapshot.ManifestEntry, error) {
	var result snapshot.ManifestEntry
	found := false
	for _, entry := range inputs.Manifest {
		if entry.SnapshotPath != path {
			continue
		}
		if found {
			return snapshot.ManifestEntry{}, errors.New("snapshot manifest path is duplicated")
		}
		result = entry
		found = true
	}
	if !found {
		return snapshot.ManifestEntry{}, errors.New("snapshot manifest path is absent")
	}
	return result, nil
}

func verifyRetainedSnapshotRole(
	retained snapshot.RetainedPath,
	wantKind string,
	requireExecutable bool,
) (os.FileInfo, error) {
	if retained.File == nil {
		return nil, errors.New("retained snapshot descriptor is absent")
	}
	info, err := retained.File.Stat()
	if err != nil {
		return nil, err
	}
	entry := retained.Entry
	if entry.Kind != wantKind || info.Mode().Perm() != os.FileMode(entry.Mode) {
		return nil, errors.New("retained snapshot role does not match its manifest")
	}
	switch wantKind {
	case snapshot.ManifestKindDirectory:
		if !info.IsDir() {
			return nil, errors.New("retained snapshot directory is not a directory")
		}
	case snapshot.ManifestKindFile:
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !entry.FSVerity || entry.FSVerityAlgorithm != snapshot.FSVerityAlgorithm ||
			!info.Mode().IsRegular() || !ok || stat.Nlink != 1 ||
			info.Size() != entry.Size || (requireExecutable && info.Mode().Perm()&0o111 == 0) {
			return nil, errors.New("retained snapshot file does not match its manifest")
		}
	default:
		return nil, errors.New("retained snapshot role kind is unsupported")
	}
	return info, nil
}

func duplicateRetainedFile(
	file *os.File,
	identity os.FileInfo,
	description string,
) (*os.File, error) {
	if file == nil || identity == nil {
		return nil, fmt.Errorf("%s is absent", description)
	}
	descriptor, err := unix.FcntlInt(file.Fd(), unix.F_DUPFD_CLOEXEC, 0)
	if err != nil {
		return nil, fmt.Errorf("duplicate %s: %w", description, err)
	}
	duplicate := os.NewFile(uintptr(descriptor), file.Name())
	opened, err := duplicate.Stat()
	if err != nil || !sameFileInfo(identity, opened) {
		closeErr := duplicate.Close()
		return nil, errors.Join(fmt.Errorf("%s changed while duplicating", description), err, closeErr)
	}
	return duplicate, nil
}

func (pair *PairAuthority) verifyCaptureInputsLocked() error {
	if pair.sourceRoot == nil || pair.verifierGit == nil || pair.gitObjects == nil ||
		pair.sourceInfo == nil || pair.verifierInfo == nil || pair.objectsInfo == nil {
		return errors.New("workspace retained snapshot inputs are absent")
	}
	sourceInfo, sourceErr := pair.sourceRoot.Stat()
	gitInfo, gitErr := pair.verifierGit.Stat()
	objectsInfo, objectsErr := pair.gitObjects.Stat()
	if sourceErr != nil || gitErr != nil || objectsErr != nil ||
		!sameFileInfo(pair.sourceInfo, sourceInfo) ||
		!sameFileInfo(pair.verifierInfo, gitInfo) ||
		!sameFileInfo(pair.objectsInfo, objectsInfo) {
		return errors.Join(
			errors.New("workspace retained snapshot input identity changed"),
			sourceErr,
			gitErr,
			objectsErr,
		)
	}
	return nil
}

func sameFileInfo(left, right os.FileInfo) bool {
	return left != nil && right != nil && os.SameFile(left, right) &&
		left.Mode() == right.Mode() && left.Size() == right.Size() &&
		left.ModTime().Equal(right.ModTime())
}
