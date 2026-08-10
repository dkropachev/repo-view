//go:build !linux

package snapshot

import (
	"context"
	"errors"
)

type BuildRequest struct {
	Root    string
	Origins OriginInputs
}

type Authority struct{}

func Build(context.Context, BuildRequest) (*Authority, error) {
	return nil, errors.New("immutable execution snapshots require Linux fs-verity")
}

func (*Authority) Inputs() (ExecutionInputs, error) {
	return ExecutionInputs{}, errors.New("immutable execution snapshots require Linux fs-verity")
}

func (*Authority) Reverify(context.Context) error {
	return errors.New("immutable execution snapshots require Linux fs-verity")
}

func (*Authority) RequireConformant(context.Context) error {
	return errors.New("immutable execution snapshots require Linux fs-verity")
}

func (*Authority) Close() error { return nil }

func (*Authority) Closed() bool { return false }
