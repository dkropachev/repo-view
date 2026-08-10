//go:build !unix

package tokenbench

import "os"

func hasMultipleLinks(os.FileInfo) bool {
	return false
}
