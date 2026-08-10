//go:build !linux

package cas

import (
	"errors"
	"os"
)

func pinStagedFile(*os.File) (*os.File, error) {
	return nil, errors.New("CAS inode pinning is unsupported on this platform")
}
