//go:build !unix

package process

import "os"

func hasMultipleLinks(os.FileInfo) bool {
	return false
}
