package tokenbench

import (
	"sync"
	"testing"
)

var artifactPolicyTestMutex sync.Mutex

// useArtifactBuildPolicyForTest is compiled only into package tests. Holding
// the mutex through cleanup prevents parallel tests from observing a temporary
// link-policy value; production code has no exported mutation hook.
func useArtifactBuildPolicyForTest(t *testing.T, digest string) {
	t.Helper()
	artifactPolicyTestMutex.Lock()
	previous := trustedArtifactManifestSHA256
	trustedArtifactManifestSHA256 = digest
	t.Cleanup(func() {
		trustedArtifactManifestSHA256 = previous
		artifactPolicyTestMutex.Unlock()
	})
}
