//go:build !linux

package taskctl

import (
	"errors"
	"os"
)

func createAnonymousAtomicPublicationFile(*os.File) (*os.File, error) {
	return nil, errors.New(
		"descriptor-bound create-only atomic publication is supported only on Linux",
	)
}

func linkAnonymousAtomicPublicationFile(*os.File, *os.File, string) error {
	return errors.New(
		"descriptor-bound create-only atomic publication is supported only on Linux",
	)
}

func atomicPublicationFileIsAnonymous(os.FileInfo) bool { return false }
