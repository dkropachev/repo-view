//go:build !unix

package navigator

import "os"

func openChangedStateCache(path string) (*os.File, error) {
	return os.Open(path)
}
