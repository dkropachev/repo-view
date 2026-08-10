//go:build !linux

package main

import "errors"

func requirePhysicallyDisjointPaths([]namedPath) error {
	return errors.New("physical filesystem path separation is unsupported on this platform")
}
