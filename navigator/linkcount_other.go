//go:build !unix

package navigator

import "os"

func hasMultipleLinks(os.FileInfo) bool {
	return false
}
