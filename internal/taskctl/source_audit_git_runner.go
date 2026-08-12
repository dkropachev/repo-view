package taskctl

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/yapless/scopesifter/internal/gitdiffcontract"
	"github.com/yapless/scopesifter/internal/processpolicy"
)

// sourceAuditGitIdentity is the single native-Git identity authenticated by a
// source-repository binding document. Every Git process in an audit is opened
// from this exact canonical path and checked against this exact digest.
type sourceAuditGitIdentity struct {
	executable string
	sha256     string
}

type sourceAuditGitRunner struct {
	identity sourceAuditGitIdentity
}

type sourceAuditGitInvocationPin struct {
	file         *os.File
	before       os.FileInfo
	want         sourceAuditGitIdentity
	installation sourceAuditGitPlatformTrust
}

type sourceAuditGitResult struct {
	stdout  []byte
	stderr  []byte
	success bool
}

type sourceAuditBoundedBuffer struct {
	bytes.Buffer
	limit int
}

func (buffer *sourceAuditBoundedBuffer) Write(content []byte) (int, error) {
	remaining := buffer.limit - buffer.Len()
	if remaining <= 0 {
		return 0, errors.New("source-audit Git output exceeded its bound")
	}
	if len(content) > remaining {
		written, _ := buffer.Buffer.Write(content[:remaining])
		return written, errors.New("source-audit Git output exceeded its bound")
	}
	return buffer.Buffer.Write(content)
}

func validateSourceAuditGitIdentity(identity sourceAuditGitIdentity) error {
	if identity.executable == "" || !filepath.IsAbs(identity.executable) ||
		filepath.Clean(identity.executable) != identity.executable {
		return errors.New("source-audit Git executable must be an absolute canonical path")
	}
	if !sourceAuditDigest.MatchString(identity.sha256) {
		return errors.New("source-audit Git SHA-256 must be lowercase hexadecimal")
	}
	return nil
}

func newSourceAuditGitRunner(identity sourceAuditGitIdentity) (sourceAuditGitRunner, error) {
	if err := validateSourceAuditGitIdentity(identity); err != nil {
		return sourceAuditGitRunner{}, err
	}
	canonical, err := filepath.EvalSymlinks(identity.executable)
	if err != nil {
		return sourceAuditGitRunner{}, fmt.Errorf("resolve source-audit Git executable: %w", err)
	}
	if canonical != identity.executable {
		return sourceAuditGitRunner{}, errors.New("source-audit Git executable path traverses a symlink")
	}
	runner := sourceAuditGitRunner{identity: identity}
	pin, err := runner.openPin()
	if err != nil {
		return sourceAuditGitRunner{}, err
	}
	if err := pin.close(); err != nil {
		return sourceAuditGitRunner{}, err
	}
	return runner, nil
}

func (runner sourceAuditGitRunner) openPin() (*sourceAuditGitInvocationPin, error) {
	if err := validateSourceAuditGitIdentity(runner.identity); err != nil {
		return nil, err
	}
	resolved, file, err := processpolicy.OpenNativeExecutable(runner.identity.executable)
	if err != nil {
		return nil, fmt.Errorf("open authenticated source-audit Git: %w", err)
	}
	accepted := false
	defer func() {
		if !accepted {
			_ = file.Close()
		}
	}()
	if resolved != runner.identity.executable {
		return nil, fmt.Errorf(
			"source-audit Git executable resolved to %s, want %s",
			resolved,
			runner.identity.executable,
		)
	}
	before, err := file.Stat()
	if err != nil {
		return nil, fmt.Errorf("inspect authenticated source-audit Git: %w", err)
	}
	if err := validateSourceAuditGitPin(file, before, runner.identity); err != nil {
		return nil, err
	}
	installation, err := captureSourceAuditGitPlatformTrust(runner.identity.executable, file)
	if err != nil {
		return nil, err
	}
	accepted = true
	return &sourceAuditGitInvocationPin{
		file: file, before: before, want: runner.identity, installation: installation,
	}, nil
}

func (runner sourceAuditGitRunner) commandContext(
	ctx context.Context,
	arguments ...string,
) (*exec.Cmd, *sourceAuditGitInvocationPin, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	command, file, err := processpolicy.NativeCommandContext(
		ctx,
		runner.identity.executable,
		arguments...,
	)
	if err != nil {
		return nil, nil, fmt.Errorf("open authenticated source-audit Git command: %w", err)
	}
	accepted := false
	defer func() {
		if !accepted {
			_ = file.Close()
		}
	}()
	before, err := file.Stat()
	if err != nil {
		return nil, nil, fmt.Errorf("inspect authenticated source-audit Git command: %w", err)
	}
	if err := validateSourceAuditGitPin(file, before, runner.identity); err != nil {
		return nil, nil, err
	}
	installation, err := captureSourceAuditGitPlatformTrust(runner.identity.executable, file)
	if err != nil {
		return nil, nil, err
	}
	accepted = true
	return command, &sourceAuditGitInvocationPin{
		file: file, before: before, want: runner.identity, installation: installation,
	}, nil
}

func (pin *sourceAuditGitInvocationPin) validate() error {
	if pin == nil || pin.file == nil {
		return errors.New("authenticated source-audit Git pin is incomplete")
	}
	identityErr := validateSourceAuditGitPin(pin.file, pin.before, pin.want)
	installationErr := pin.installation.validate(pin.want.executable, pin.file)
	return errors.Join(identityErr, installationErr)
}

func (pin *sourceAuditGitInvocationPin) close() error {
	if pin == nil || pin.file == nil {
		return nil
	}
	validationErr := pin.validate()
	closeErr := pin.file.Close()
	pin.file = nil
	if closeErr != nil {
		closeErr = fmt.Errorf("close authenticated source-audit Git: %w", closeErr)
	}
	return errors.Join(validationErr, closeErr)
}

func validateSourceAuditGitPin(
	file *os.File,
	wantInfo os.FileInfo,
	wantIdentity sourceAuditGitIdentity,
) error {
	if file == nil || wantInfo == nil {
		return errors.New("authenticated source-audit Git pin is incomplete")
	}
	if err := processpolicy.ValidateNativeFile(file); err != nil {
		return fmt.Errorf("validate authenticated source-audit Git: %w", err)
	}
	info, err := file.Stat()
	if err != nil {
		return fmt.Errorf("inspect authenticated source-audit Git: %w", err)
	}
	if !os.SameFile(wantInfo, info) || wantInfo.Mode() != info.Mode() ||
		wantInfo.Size() != info.Size() || !wantInfo.ModTime().Equal(info.ModTime()) {
		return errors.New("authenticated source-audit Git changed during use")
	}
	if !sourceAuditFileHasOneLink(info) {
		return errors.New("authenticated source-audit Git must have exactly one filesystem link")
	}
	digest, err := hashSourceAuditGitFile(file, info.Size())
	if err != nil {
		return fmt.Errorf("hash authenticated source-audit Git: %w", err)
	}
	if digest != wantIdentity.sha256 {
		return fmt.Errorf(
			"source-audit Git SHA-256 is %s, want %s",
			digest,
			wantIdentity.sha256,
		)
	}
	canonical, err := filepath.EvalSymlinks(wantIdentity.executable)
	if err != nil {
		return fmt.Errorf("resolve authenticated source-audit Git path: %w", err)
	}
	if canonical != wantIdentity.executable {
		return errors.New("authenticated source-audit Git path changed or traverses a symlink")
	}
	pathInfo, err := os.Lstat(wantIdentity.executable)
	if err != nil {
		return fmt.Errorf("inspect authenticated source-audit Git path: %w", err)
	}
	if !os.SameFile(info, pathInfo) || info.Mode() != pathInfo.Mode() ||
		info.Size() != pathInfo.Size() || !info.ModTime().Equal(pathInfo.ModTime()) {
		return errors.New("authenticated source-audit Git path no longer identifies the pinned executable")
	}
	return nil
}

func hashSourceAuditGitFile(file *os.File, size int64) (string, error) {
	if size < 0 {
		return "", errors.New("authenticated source-audit Git has a negative size")
	}
	hasher := sha256.New()
	if _, err := io.Copy(hasher, io.NewSectionReader(file, 0, size)); err != nil {
		return "", err
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

func (runner sourceAuditGitRunner) execute(
	ctx context.Context,
	directory string,
	directoryInfo os.FileInfo,
	stdout io.Writer,
	arguments ...string,
) (sourceAuditGitResult, error) {
	if err := validateSourceAuditGitInvocation(directory, arguments); err != nil {
		return sourceAuditGitResult{}, err
	}
	if ctx == nil {
		ctx = context.Background()
	}
	commandContext, cancel := context.WithTimeout(ctx, sourceAuditCommandTimeout)
	defer cancel()
	repositoryPin, err := openSourceAuditRepositoryPin(directory, directoryInfo)
	if err != nil {
		return sourceAuditGitResult{}, err
	}
	repositoryOpen := true
	defer func() {
		if repositoryOpen {
			_ = repositoryPin.close()
		}
	}()
	command, pin, err := runner.commandContext(commandContext, arguments...)
	if err != nil {
		repositoryErr := repositoryPin.close()
		repositoryOpen = false
		return sourceAuditGitResult{}, errors.Join(err, repositoryErr)
	}
	if err := configureSourceAuditRepositoryCommand(command, repositoryPin); err != nil {
		repositoryErr := repositoryPin.close()
		repositoryOpen = false
		return sourceAuditGitResult{}, errors.Join(err, pin.close(), repositoryErr)
	}
	command.Env = append(
		gitdiffcontract.Environment(os.DevNull),
		"GIT_CONFIG_PARAMETERS='core.commitGraph'='false' 'protocol.allow'='never' 'remote.origin.promisor'='false' 'remote.origin.partialclonefilter'='' 'remote.origin.url'='/nonexistent'",
		"GIT_CONFIG_COUNT=0",
		"GIT_ALLOW_PROTOCOL=",
		"GIT_NO_LAZY_FETCH=1",
		"GIT_GRAFT_FILE="+os.DevNull,
		"GIT_SHALLOW_FILE="+os.DevNull,
		"GIT_TEMPLATE_DIR=/nonexistent",
	)
	captured := &sourceAuditBoundedBuffer{limit: sourceAuditMaximumOutput}
	if stdout == nil {
		stdout = captured
	}
	standardError := &sourceAuditBoundedBuffer{limit: sourceAuditMaximumError}
	command.Stdout = stdout
	command.Stderr = standardError
	runErr := command.Run()
	pinErr := pin.close()
	repositoryErr := repositoryPin.close()
	repositoryOpen = false
	if err := errors.Join(pinErr, repositoryErr); err != nil {
		return sourceAuditGitResult{}, err
	}
	if err := commandContext.Err(); err != nil {
		return sourceAuditGitResult{}, err
	}
	result := sourceAuditGitResult{
		stdout: bytes.Clone(captured.Bytes()), stderr: bytes.Clone(standardError.Bytes()),
		success: runErr == nil,
	}
	if runErr == nil {
		return result, nil
	}
	var exitError *exec.ExitError
	if errors.As(runErr, &exitError) {
		return result, nil
	}
	return sourceAuditGitResult{}, runErr
}

func (runner sourceAuditGitRunner) requiredOutput(
	ctx context.Context,
	directory string,
	directoryInfo os.FileInfo,
	operation string,
	arguments ...string,
) ([]byte, error) {
	result, err := runner.execute(ctx, directory, directoryInfo, nil, arguments...)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", operation, err)
	}
	if !result.success {
		return nil, sourceAuditGitFailure(operation, result)
	}
	return result.stdout, nil
}
