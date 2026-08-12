//go:build !linux

package releaseartifacts

import (
	"errors"
	"net/http"
)

func newReleaseHTTPTransport() (*http.Transport, error) {
	return nil, errors.New("trusted GitHub release transport requires Linux")
}
