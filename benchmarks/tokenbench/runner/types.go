package runner

import (
	"context"
	"fmt"

	"github.com/dkropachev/repo-view/benchmarks/tokenbench/harness"
)

// Arm identifies one side of a paired execution without coupling the process
// runner to tokenbench's publication package.
type Arm string

const (
	BaselineArm  Arm = "baseline"
	CandidateArm Arm = "candidate"
)

// ExecutionRequest is the complete, already-approved process and invocation
// presented to the runner. Callers must convert their publication-layer type
// explicitly at this trust boundary.
type ExecutionRequest struct {
	Arm        Arm                 `json:"arm"`
	Repetition int                 `json:"repetition"`
	Invocation harness.Invocation  `json:"invocation"`
	Process    harness.ProcessSpec `json:"process"`
}

// ExecutorIdentity pins the runner implementation and all non-secret runner
// configuration. It deliberately contains no publication authority bit.
type ExecutorIdentity struct {
	Kind         string `json:"kind"`
	Version      string `json:"version"`
	ConfigSHA256 string `json:"config_sha256"`
}

// PreparedExecution is an opaque, single-use execution capability bound to
// one approved runner request.
type PreparedExecution interface {
	Execute(context.Context) (harness.RawExecution, error)
	Abort(context.Context) error
}

// IntegrityError marks a runner-control failure that invalidates parity or
// captured evidence. Publication code should stop the pair when errors.As
// finds this concrete type.
type IntegrityError struct {
	Stage string
	Err   error
}

// NewIntegrityError constructs a typed fail-closed runner error.
func NewIntegrityError(stage string, err error) error {
	if err == nil {
		return nil
	}
	return &IntegrityError{Stage: stage, Err: err}
}

func (err *IntegrityError) Error() string {
	return fmt.Sprintf("%s: %v", err.Stage, err.Err)
}

func (err *IntegrityError) Unwrap() error { return err.Err }
