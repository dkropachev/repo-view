//go:build !unix

package codex

import "os"

func hasMultipleLinks(_ os.FileInfo) bool {
	return false
}
