//go:build !linux

package main

import (
	"context"
	"errors"
	"io"
)

type delegatedRunResult struct{ exitCode int }

func (result *delegatedRunResult) Error() string { return "delegated run is unsupported" }

func reexecRunInPrivateMountNamespace(
	context.Context,
	[]string,
	int,
	io.Writer,
	io.Writer,
) error {
	return errors.New("publishable runs require a private Linux mount namespace")
}

func enterPrivateMountNamespaceChild() error {
	return errors.New("publishable runs require a private Linux mount namespace")
}
