//go:build !linux

package source

import (
	"errors"
	"os"
	"path/filepath"
)

func verifyOpaqueGitlink(root, relative string) (gitlinkMaterialization, error) {
	_, err := os.Lstat(filepath.Join(root, filepath.FromSlash(relative)))
	if errors.Is(err, os.ErrNotExist) {
		return gitlinkMaterialization{}, nil
	}
	if err != nil {
		return gitlinkMaterialization{}, err
	}
	return gitlinkMaterialization{}, errors.New(
		"materialized gitlinks require Linux mount-safe verification",
	)
}
