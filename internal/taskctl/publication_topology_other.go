//go:build !linux

package taskctl

import "errors"

func requirePhysicallyDisjointPublicationPaths(paths []publicationPhysicalPath) error {
	if len(paths) < 2 {
		return nil
	}
	return errors.New("physical filesystem path separation is unsupported on this platform")
}
