//go:build !unix

package repoview

import "os"

func hasMultipleLinks(os.FileInfo) bool {
	return false
}
