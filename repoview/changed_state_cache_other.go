//go:build !unix

package repoview

import "os"

func openChangedStateCache(path string) (*os.File, error) {
	return os.Open(path)
}
