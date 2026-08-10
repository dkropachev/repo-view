//go:build !unix

package source

import "os"

func hasMultipleLinks(_ os.FileInfo) bool {
	return false
}
