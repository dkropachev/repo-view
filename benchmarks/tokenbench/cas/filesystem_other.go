//go:build !unix

package cas

import "os"

func filesystemID(_ os.FileInfo) (uint64, bool) {
	return 0, false
}

func multipleLinks(_ os.FileInfo) bool {
	return false
}
