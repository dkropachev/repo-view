//go:build !linux

package tokenbench

import (
	"errors"
	"os"
)

func openArtifactRootNoSymlinks(string) (*os.File, error) {
	return nil, errors.New("publishable artifact loading requires Linux openat2")
}

func openArtifactFileNoSymlinks(*os.File, string) (*os.File, error) {
	return nil, errors.New("publishable artifact loading requires Linux openat2")
}
