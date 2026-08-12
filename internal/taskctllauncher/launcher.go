// Package taskctllauncher authenticates and executes the repository-local
// taskctl binary without reopening it by pathname at the execution boundary.
package taskctllauncher

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"
)

const executableSHA256Environment = "TASKCTL_EXECUTABLE_SHA256"

// installedLauncherPath is provisioned outside the repository after its
// release digest and static ELF shape have been independently verified. Every
// path component is root-owned and non-writable by group or other.
const installedLauncherPath = "/usr/local/libexec/scopesifter/taskctl-launcher"

var inspectExecutableRole = [2]string{"inspect", "executable-sha256"}
var installLauncherRole = [2]string{"install", "trusted-launcher"}

var operationalReleaseRevision = "development"

// SetOperationalReleaseRevision binds the main package's link-time release
// revision to operational launcher admission. Release builds set main's value
// with -X; development builds remain intentionally non-operational.
func SetOperationalReleaseRevision(revision string) {
	operationalReleaseRevision = revision
}

// reviewedChildEnvironmentNames is the complete data-only environment surface
// accepted by taskctl. Launcher authentication controls and all loader,
// runtime, locale, credential, and path-selection variables are deliberately
// absent from the child environment.
var reviewedChildEnvironmentNames = [...]string{
	"TASKCTL_OUTPUT",
	"TASKCTL_INPUT",
	"TASKCTL_INPUT_SHA256",
	"TASKCTL_GIT_EXECUTABLE",
	"TASKCTL_GIT_SHA256",
	"TASKCTL_REPOSITORIES_JSON",
	"TASKCTL_REPOSITORY_BINDINGS",
	"TASKCTL_REPOSITORY_BINDINGS_SHA256",
	"TASKCTL_SOURCE_SELECTIONS",
	"TASKCTL_SOURCE_SELECTIONS_SHA256",
}

var reviewedRoles = map[[2]string]struct{}{
	inspectExecutableRole:                      {},
	{"generate", "source-audit"}:               {},
	{"generate", "source-repository-bindings"}: {},
	{"generate", "source-selections"}:          {},
	{"validate", "source-audit"}:               {},
	{"validate", "source-repository-bindings"}: {},
	{"validate", "source-selections"}:          {},
}

// Run authenticates and executes bin/taskctl from the current repository
// directory. The root-only installation role is convenience for installing an
// already independently attested raw launcher at the fixed path; it is not a
// trust bootstrap or verifier. Only an exact reviewed verb and role are
// accepted.
func Run(
	ctx context.Context,
	arguments []string,
	stdin io.Reader,
	stdout, stderr io.Writer,
) (resultErr error) {
	return run(
		ctx,
		arguments,
		stdin,
		stdout,
		stderr,
		verifyOperationalLauncher,
		operationalReleaseRevision,
	)
}

func run(
	ctx context.Context,
	arguments []string,
	stdin io.Reader,
	stdout, stderr io.Writer,
	verifyLauncher func() error,
	releaseRevision string,
) (resultErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := validateArguments(arguments); err != nil {
		return err
	}
	if len(arguments) == 3 &&
		[2]string{arguments[0], arguments[1]} == installLauncherRole {
		// Installation is convenience for an administrator who has already
		// independently verified the release attestation and SHA-256 before
		// authorizing this raw artifact to run as root. Candidate code is already
		// executing here: this is deliberately not a trust bootstrap or verifier.
		if err := verifyReleaseRevision(releaseRevision); err != nil {
			return err
		}
		return installPlatform(arguments[2], stdout)
	}
	if err := verifyReleaseRevision(releaseRevision); err != nil {
		return err
	}
	if verifyLauncher == nil {
		return errors.New("taskctl launcher: launcher trust check is required")
	}
	if err := verifyLauncher(); err != nil {
		return fmt.Errorf("taskctl launcher: authenticate installed launcher: %w", err)
	}
	root, err := os.Getwd()
	if err != nil {
		return errors.New("taskctl launcher: determine repository working directory")
	}
	cwd, cwdErr := os.Open(".")
	if cwdErr != nil {
		return errors.New("taskctl launcher: pin repository working directory")
	}
	defer func() { resultErr = errors.Join(resultErr, cwd.Close()) }()
	if [2]string{arguments[0], arguments[1]} == inspectExecutableRole {
		return inspectPlatform(root, stdout, launcherHooks{expectedCWD: cwd})
	}
	expectedSHA256, found := os.LookupEnv(executableSHA256Environment)
	if !found {
		return errors.New("taskctl launcher: TASKCTL_EXECUTABLE_SHA256 is required")
	}
	return runPlatform(ctx, root, arguments, expectedSHA256, stdin, stdout, stderr, launcherHooks{expectedCWD: cwd})
}

func validateArguments(arguments []string) error {
	if len(arguments) == 3 &&
		[2]string{arguments[0], arguments[1]} == installLauncherRole {
		if !validLowerSHA256(arguments[2]) {
			return errors.New("taskctl launcher: install digest must be lowercase 64-hex")
		}
		return nil
	}
	if len(arguments) != 2 {
		return errors.New("taskctl launcher: expected one reviewed verb and role")
	}
	if _, ok := reviewedRoles[[2]string{arguments[0], arguments[1]}]; !ok {
		return errors.New("taskctl launcher: taskctl role is not reviewed")
	}
	return nil
}

func verifyReleaseRevision(revision string) error {
	if len(revision) != 40 || !validLowerHex(revision) {
		return errors.New("taskctl launcher: operational release revision must be lowercase 40-hex")
	}
	return nil
}

func closedChildEnvironment() []string {
	environment := make([]string, 0, len(reviewedChildEnvironmentNames))
	for _, name := range reviewedChildEnvironmentNames {
		if value, present := os.LookupEnv(name); present {
			environment = append(environment, name+"="+value)
		}
	}
	return environment
}

func validLowerSHA256(value string) bool {
	return len(value) == sha256.Size*2 && validLowerHex(value)
}

func validLowerHex(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range value {
		if (character >= '0' && character <= '9') ||
			(character >= 'a' && character <= 'f') {
			continue
		}
		return false
	}
	return true
}

type launcherHooks struct {
	beforeStart             func() error
	afterCommandStart       func() error
	afterWaitBeforeVerify   func() error
	waitCommand             func(*exec.Cmd) error
	expectedCWD             *os.File
	cancellationGrace       time.Duration
	terminationCleanupGrace time.Duration
}
