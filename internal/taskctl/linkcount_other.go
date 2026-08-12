//go:build !unix

package taskctl

import "os"

func sourceAuditFileHasOneLink(_ os.FileInfo) bool {
	return false
}

func sourceAuditFileOwnedByCurrentUser(_ os.FileInfo) bool {
	return false
}
