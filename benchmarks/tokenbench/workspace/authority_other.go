//go:build !linux

package workspace

import (
	"context"
	"errors"

	"github.com/scopesifter/scopesifter/benchmarks/tokenbench/snapshot"
)

// PairAuthority has no live implementation outside Linux.
type PairAuthority struct{}

// ArmAuthority has no live implementation outside Linux.
type ArmAuthority struct{}

// Prepare fails closed where detached tmpfs and OverlayFS authorities are not
// available.
func Prepare(context.Context, *snapshot.Authority, PrepareRequest) (*PairAuthority, error) {
	return nil, errors.New("writable workspace authority requires Linux")
}

func (*PairAuthority) Inputs() (Inputs, error) {
	return Inputs{}, errors.New("writable workspace authority requires Linux")
}

func (*PairAuthority) Reverify(context.Context) error {
	return errors.New("writable workspace authority requires Linux")
}

func (*PairAuthority) BeginArm(context.Context) (*ArmAuthority, error) {
	return nil, errors.New("writable workspace authority requires Linux")
}

func (*PairAuthority) Close() error { return nil }

func (*PairAuthority) Closed() bool { return false }

func (*ArmAuthority) Paths() (ArmPaths, error) {
	return ArmPaths{}, errors.New("writable workspace authority requires Linux")
}

func (*ArmAuthority) RequireFresh(context.Context) error {
	return errors.New("writable workspace authority requires Linux")
}

func (*ArmAuthority) Reverify(context.Context) error {
	return errors.New("writable workspace authority requires Linux")
}

func (*ArmAuthority) Close() error { return nil }

func (*ArmAuthority) Closed() bool { return false }
