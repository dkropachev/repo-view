//go:build !linux

package releaseartifacts

import (
	"errors"
	"os"
)

func createSealedReleaseArtifact(string, []byte) (*os.File, error) {
	return nil, errors.New("immutable descriptor-bound release publication requires Linux")
}

func verifySealedReleaseArtifact(*os.File) error {
	return errors.New("immutable descriptor-bound release publication requires Linux")
}
