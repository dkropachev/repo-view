package grammargen

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

type artifact struct {
	path string
	data []byte
}

type preparedArtifact struct {
	temporaryPath string
	backupPath    string
	artifact
	installed bool
}

func installArtifacts(artifacts []artifact) (returnErr error) {
	prepared := make([]preparedArtifact, 0, len(artifacts))
	defer func() {
		for _, item := range prepared {
			if err := os.Remove(item.temporaryPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				returnErr = errors.Join(returnErr, fmt.Errorf("remove staged artifact: %w", err))
			}
		}
	}()

	for _, item := range artifacts {
		if err := os.MkdirAll(filepath.Dir(item.path), 0o755); err != nil {
			return fmt.Errorf("create artifact directory: %w", err)
		}
		temporary, err := os.CreateTemp(filepath.Dir(item.path), "."+filepath.Base(item.path)+".new-")
		if err != nil {
			return fmt.Errorf("create staged artifact: %w", err)
		}
		temporaryPath := temporary.Name()
		if err := temporary.Chmod(0o644); err != nil {
			_ = temporary.Close()
			_ = os.Remove(temporaryPath)
			return fmt.Errorf("set staged artifact mode: %w", err)
		}
		if _, err := temporary.Write(item.data); err != nil {
			_ = temporary.Close()
			_ = os.Remove(temporaryPath)
			return fmt.Errorf("write staged artifact: %w", err)
		}
		if err := temporary.Close(); err != nil {
			_ = os.Remove(temporaryPath)
			return fmt.Errorf("close staged artifact: %w", err)
		}
		prepared = append(prepared, preparedArtifact{
			artifact:      item,
			temporaryPath: temporaryPath,
		})
	}

	for index := range prepared {
		item := &prepared[index]
		if _, err := os.Lstat(item.path); err == nil {
			backup, err := reserveBackupPath(item.path)
			if err != nil {
				return rollbackArtifacts(prepared, index, err)
			}
			if err := os.Rename(item.path, backup); err != nil {
				_ = os.Remove(backup)
				return rollbackArtifacts(
					prepared,
					index,
					fmt.Errorf("back up %s: %w", item.path, err),
				)
			}
			item.backupPath = backup
		} else if !errors.Is(err, os.ErrNotExist) {
			return rollbackArtifacts(
				prepared,
				index,
				fmt.Errorf("inspect %s: %w", item.path, err),
			)
		}

		if err := os.Rename(item.temporaryPath, item.path); err != nil {
			return rollbackArtifacts(
				prepared,
				index+1,
				fmt.Errorf("install %s: %w", item.path, err),
			)
		}
		item.installed = true
	}

	for index := range prepared {
		if prepared[index].backupPath == "" {
			continue
		}
		if err := os.Remove(prepared[index].backupPath); err != nil {
			return fmt.Errorf("remove backup for %s: %w", prepared[index].path, err)
		}
		prepared[index].backupPath = ""
	}
	return nil
}

func reserveBackupPath(path string) (string, error) {
	backup, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".old-")
	if err != nil {
		return "", fmt.Errorf("reserve backup path: %w", err)
	}
	backupPath := backup.Name()
	if err := backup.Close(); err != nil {
		_ = os.Remove(backupPath)
		return "", fmt.Errorf("close backup placeholder: %w", err)
	}
	if err := os.Remove(backupPath); err != nil {
		return "", fmt.Errorf("remove backup placeholder: %w", err)
	}
	return backupPath, nil
}

func rollbackArtifacts(prepared []preparedArtifact, count int, cause error) error {
	rollbackErrors := []error{cause}
	for index := count - 1; index >= 0; index-- {
		item := &prepared[index]
		if item.installed {
			if err := os.Remove(item.path); err != nil && !errors.Is(err, os.ErrNotExist) {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("remove new %s: %w", item.path, err))
			}
			item.installed = false
		}
		if item.backupPath != "" {
			if err := os.Rename(item.backupPath, item.path); err != nil {
				rollbackErrors = append(rollbackErrors, fmt.Errorf("restore %s: %w", item.path, err))
			} else {
				item.backupPath = ""
			}
		}
	}
	return errors.Join(rollbackErrors...)
}
