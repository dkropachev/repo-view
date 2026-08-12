package taskctl

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"

	"github.com/yapless/scopesifter/internal/gitdiffcontract"
	"github.com/yapless/scopesifter/internal/processpolicy"
)

const (
	sourceAuditBlobBatchMaximumHeader = 128
	sourceAuditBlobBatchCopyBuffer    = 32 << 10
)

var (
	sourceAuditBlobBatchObjectID    = regexp.MustCompile(`^[0-9a-f]{40}$`)
	errSourceAuditBlobBatchMissing  = errors.New("source-audit batch blob is missing")
	errSourceAuditBlobBatchProtocol = errors.New("source-audit batch protocol error")
)

func validateSourceAuditBlobBatchGitArguments(arguments []string) error {
	if len(arguments) != 2 || arguments[0] != "cat-file" || arguments[1] != "--batch" {
		return errors.New("source-audit batch Git invocation is outside its closed grammar")
	}
	if err := processpolicy.ValidateGit(arguments...); err != nil {
		return fmt.Errorf("validate source-audit batch Git invocation: %w", err)
	}
	return nil
}

// sourceAuditBlobBatch owns one native Git cat-file process and serializes all
// requests sent to it. A protocol or context failure poisons the batch because
// the request/response framing can no longer be trusted. A missing object or a
// destination writer failure does not: both responses are consumed exactly.
type sourceAuditBlobBatch struct {
	stdin         io.WriteCloser
	closeErr      error
	poisoned      error
	lifetime      context.Context
	stderr        *sourceAuditBlobBatchErrorBuffer
	stdout        *bufio.Reader
	gate          chan struct{}
	repositoryPin *sourceAuditRepositoryPin
	cancel        context.CancelCauseFunc
	timeout       context.CancelFunc
	gitPin        *sourceAuditGitInvocationPin
	command       *exec.Cmd
	closed        bool
}

// newSourceAuditBlobBatch opens one pinned native Git process for repository.
// Its ReadBlob method has the source.GitBlobReader streaming contract.
func newSourceAuditBlobBatch(
	ctx context.Context,
	repository string,
	repositoryInfo os.FileInfo,
	git sourceAuditGitRunner,
) (_ *sourceAuditBlobBatch, resultErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	root, err := validateSourceAuditBlobBatchRepository(repository)
	if err != nil {
		return nil, err
	}
	repositoryPin, err := openSourceAuditRepositoryPin(root, repositoryInfo)
	if err != nil {
		return nil, err
	}
	cleanupRepositoryPin := true
	defer func() {
		if cleanupRepositoryPin {
			resultErr = errors.Join(resultErr, repositoryPin.close())
		}
	}()
	arguments := []string{"cat-file", "--batch"}
	if err := validateSourceAuditBlobBatchGitArguments(arguments); err != nil {
		return nil, err
	}

	timedContext, timeout := context.WithTimeout(ctx, sourceAuditCommandTimeout)
	lifetime, cancel := context.WithCancelCause(timedContext)
	cleanupLifetime := true
	defer func() {
		if cleanupLifetime {
			timeout()
		}
	}()
	command, gitPin, err := git.commandContext(lifetime, arguments...)
	if err != nil {
		cancel(err)
		return nil, fmt.Errorf("open authenticated Git for source-audit blob batch: %w", err)
	}
	cleanupPin := true
	defer func() {
		if cleanupPin {
			resultErr = errors.Join(resultErr, gitPin.close())
		}
	}()

	standardInput, err := command.StdinPipe()
	if err != nil {
		cancel(err)
		return nil, fmt.Errorf("open source-audit batch Git stdin: %w", err)
	}
	standardOutput, err := command.StdoutPipe()
	if err != nil {
		cancel(err)
		_ = standardInput.Close()
		return nil, fmt.Errorf("open source-audit batch Git stdout: %w", err)
	}
	standardError := &sourceAuditBlobBatchErrorBuffer{limit: sourceAuditMaximumError}
	if err := configureSourceAuditRepositoryCommand(command, repositoryPin); err != nil {
		cancel(err)
		_ = standardInput.Close()
		return nil, err
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
	command.Stderr = standardError
	if err := command.Start(); err != nil {
		cancel(err)
		_ = standardInput.Close()
		return nil, fmt.Errorf("start source-audit batch Git: %w", err)
	}
	if err := gitPin.validate(); err != nil {
		cancel(err)
		_ = standardInput.Close()
		waitErr := command.Wait()
		return nil, errors.Join(err, waitErr)
	}
	if err := repositoryPin.validate(); err != nil {
		cancel(err)
		_ = standardInput.Close()
		waitErr := command.Wait()
		return nil, errors.Join(err, waitErr)
	}

	batch := &sourceAuditBlobBatch{
		gate:          make(chan struct{}, 1),
		command:       command,
		gitPin:        gitPin,
		repositoryPin: repositoryPin,
		stdin:         standardInput,
		stdout:        bufio.NewReaderSize(standardOutput, sourceAuditBlobBatchMaximumHeader),
		stderr:        standardError,
		lifetime:      lifetime,
		cancel:        cancel,
		timeout:       timeout,
	}
	batch.gate <- struct{}{}
	cleanupPin = false
	cleanupRepositoryPin = false
	cleanupLifetime = false
	return batch, nil
}

func validateSourceAuditBlobBatchRepository(repository string) (string, error) {
	if repository == "" || !filepath.IsAbs(repository) || filepath.Clean(repository) != repository {
		return "", errors.New("source-audit batch repository must be a canonical absolute path")
	}
	if filepath.Dir(repository) == repository {
		return "", errors.New("source-audit batch repository must not be a filesystem root")
	}
	info, err := os.Lstat(repository)
	if err != nil {
		return "", fmt.Errorf("inspect source-audit batch repository: %w", err)
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("source-audit batch repository is not a real directory")
	}
	resolved, err := filepath.EvalSymlinks(repository)
	if err != nil {
		return "", fmt.Errorf("resolve source-audit batch repository: %w", err)
	}
	if resolved != repository {
		return "", errors.New("source-audit batch repository traverses a symlink")
	}
	gitDirectory := filepath.Join(repository, ".git")
	gitInfo, err := os.Lstat(gitDirectory)
	if err != nil {
		return "", fmt.Errorf("inspect source-audit batch .git directory: %w", err)
	}
	if !gitInfo.IsDir() || gitInfo.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("source-audit batch repository lacks a real .git directory")
	}
	resolvedGit, err := filepath.EvalSymlinks(gitDirectory)
	if err != nil {
		return "", fmt.Errorf("resolve source-audit batch .git directory: %w", err)
	}
	if resolvedGit != gitDirectory {
		return "", errors.New("source-audit batch .git directory traverses a symlink")
	}
	return repository, nil
}

// ReadBlob streams the exact raw bytes of objectID into destination.
func (batch *sourceAuditBlobBatch) ReadBlob(
	ctx context.Context,
	objectID string,
	destination io.Writer,
) error {
	if batch == nil {
		return errors.New("source-audit blob batch is required")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if !sourceAuditBlobBatchObjectID.MatchString(objectID) {
		return errors.New("source-audit batch object ID must be lowercase 40-hex")
	}
	if destination == nil {
		return errors.New("source-audit batch blob destination is required")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-batch.gate:
	}
	defer func() { batch.gate <- struct{}{} }()
	if err := ctx.Err(); err != nil {
		return err
	}
	if batch.closed {
		return errors.New("source-audit blob batch is closed")
	}
	if batch.poisoned != nil {
		return fmt.Errorf("source-audit blob batch is unusable: %w", batch.poisoned)
	}
	if err := batch.repositoryPin.validate(); err != nil {
		return batch.poison(err, ctx)
	}
	if cause := context.Cause(batch.lifetime); cause != nil {
		batch.poisoned = cause
		return cause
	}

	stopCancellation := context.AfterFunc(ctx, func() { batch.cancel(ctx.Err()) })
	defer stopCancellation()
	if _, err := io.WriteString(batch.stdin, objectID+"\n"); err != nil {
		return batch.poison(sourceAuditBlobBatchProtocolError("write request: %v", err), ctx)
	}
	err := readSourceAuditBlobBatchResponse(ctx, batch.stdout, objectID, destination)
	if err != nil {
		if pinErr := batch.repositoryPin.validate(); pinErr != nil {
			return batch.poison(pinErr, ctx)
		}
		if errors.Is(err, errSourceAuditBlobBatchProtocol) ||
			errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return batch.poison(err, ctx)
		}
		return err
	}
	if err := batch.repositoryPin.validate(); err != nil {
		return batch.poison(err, ctx)
	}
	if batch.stderr.overflowed() {
		return batch.poison(
			sourceAuditBlobBatchProtocolError("standard error exceeded %d bytes", sourceAuditMaximumError),
			ctx,
		)
	}
	if err := ctx.Err(); err != nil {
		return batch.poison(err, ctx)
	}
	if cause := context.Cause(batch.lifetime); cause != nil {
		return batch.poison(cause, ctx)
	}
	return nil
}

func (batch *sourceAuditBlobBatch) poison(err error, callContext context.Context) error {
	if callErr := callContext.Err(); callErr != nil {
		err = callErr
	} else if cause := context.Cause(batch.lifetime); cause != nil {
		err = cause
	}
	if batch.poisoned == nil {
		batch.poisoned = err
	}
	batch.cancel(batch.poisoned)
	return batch.poisoned
}

// Close sends EOF, waits for Git, validates the pinned executable again, and
// releases every descriptor. It is safe to call repeatedly.
func (batch *sourceAuditBlobBatch) Close() error {
	if batch == nil {
		return nil
	}
	<-batch.gate
	defer func() { batch.gate <- struct{}{} }()
	if batch.closed {
		return batch.closeErr
	}
	batch.closed = true

	inputErr := batch.stdin.Close()
	if inputErr != nil {
		batch.cancel(inputErr)
	}
	waitErr := batch.command.Wait()
	lifetimeCause := context.Cause(batch.lifetime)
	closeErr := batch.gitPin.close()
	repositoryCloseErr := batch.repositoryPin.close()
	batch.cancel(context.Canceled)
	batch.timeout()

	var result []error
	if batch.poisoned != nil {
		result = append(result, batch.poisoned)
	} else if lifetimeCause != nil {
		result = append(result, lifetimeCause)
	}
	if inputErr != nil {
		result = append(result, fmt.Errorf("close source-audit batch Git stdin: %w", inputErr))
	}
	if waitErr != nil && batch.poisoned == nil && lifetimeCause == nil {
		result = append(result, sourceAuditBlobBatchWaitError(waitErr, batch.stderr.bytes()))
	}
	if batch.stderr.overflowed() {
		result = append(result, fmt.Errorf(
			"source-audit batch Git stderr exceeded %d bytes",
			sourceAuditMaximumError,
		))
	}
	if closeErr != nil {
		result = append(result, fmt.Errorf("close authenticated source-audit batch Git: %w", closeErr))
	}
	if repositoryCloseErr != nil {
		result = append(result, fmt.Errorf(
			"close authenticated source-audit batch repository: %w",
			repositoryCloseErr,
		))
	}
	batch.closeErr = errors.Join(result...)
	return batch.closeErr
}

func readSourceAuditBlobBatchResponse(
	ctx context.Context,
	reader *bufio.Reader,
	objectID string,
	destination io.Writer,
) error {
	header, err := reader.ReadSlice('\n')
	if err != nil {
		if errors.Is(err, bufio.ErrBufferFull) {
			return sourceAuditBlobBatchProtocolError(
				"response header exceeds %d bytes",
				sourceAuditBlobBatchMaximumHeader,
			)
		}
		return sourceAuditBlobBatchProtocolError("read response header: %v", err)
	}
	if len(header) > sourceAuditBlobBatchMaximumHeader {
		return sourceAuditBlobBatchProtocolError(
			"response header exceeds %d bytes",
			sourceAuditBlobBatchMaximumHeader,
		)
	}
	if bytes.Equal(header, []byte(objectID+" missing\n")) {
		return fmt.Errorf("%w: %s", errSourceAuditBlobBatchMissing, objectID)
	}
	fields := bytes.Split(header[:len(header)-1], []byte{' '})
	if len(fields) != 3 || string(fields[0]) != objectID || string(fields[1]) != "blob" {
		return sourceAuditBlobBatchProtocolError("malformed response header")
	}
	sizeField := string(fields[2])
	if sizeField == "" || len(sizeField) > 1 && sizeField[0] == '0' {
		return sourceAuditBlobBatchProtocolError("noncanonical blob size")
	}
	for _, digit := range []byte(sizeField) {
		if digit < '0' || digit > '9' {
			return sourceAuditBlobBatchProtocolError("noncanonical blob size")
		}
	}
	size, err := strconv.ParseInt(sizeField, 10, 64)
	if err != nil || size < 0 {
		return sourceAuditBlobBatchProtocolError("invalid blob size")
	}
	if size > sourceAuditMaximumBlobBytes {
		return sourceAuditBlobBatchProtocolError(
			"blob exceeds %d bytes",
			sourceAuditMaximumBlobBytes,
		)
	}
	writerErr, protocolErr := copySourceAuditBlobBatchBytes(
		ctx,
		reader,
		destination,
		size,
	)
	if protocolErr != nil {
		return protocolErr
	}
	trailer, err := reader.ReadByte()
	if err != nil {
		return sourceAuditBlobBatchProtocolError("read blob trailer: %v", err)
	}
	if trailer != '\n' {
		return sourceAuditBlobBatchProtocolError("blob trailer is not LF")
	}
	return writerErr
}

func copySourceAuditBlobBatchBytes(
	ctx context.Context,
	reader io.Reader,
	destination io.Writer,
	size int64,
) (error, error) {
	buffer := make([]byte, sourceAuditBlobBatchCopyBuffer)
	var writerErr error
	for remaining := size; remaining > 0; {
		if err := ctx.Err(); err != nil {
			return writerErr, err
		}
		chunk := int64(len(buffer))
		if remaining < chunk {
			chunk = remaining
		}
		content := buffer[:int(chunk)]
		if _, err := io.ReadFull(reader, content); err != nil {
			return writerErr, sourceAuditBlobBatchProtocolError("read blob body: %v", err)
		}
		remaining -= chunk
		if writerErr != nil {
			continue
		}
		for len(content) != 0 {
			written, err := destination.Write(content)
			if written < 0 || written > len(content) {
				writerErr = errors.New("source-audit blob destination returned an invalid write count")
				break
			}
			content = content[written:]
			if err != nil {
				writerErr = err
				break
			}
			if written == 0 {
				writerErr = io.ErrShortWrite
				break
			}
		}
	}
	return writerErr, nil
}

func sourceAuditBlobBatchProtocolError(format string, arguments ...any) error {
	return fmt.Errorf("%w: %s", errSourceAuditBlobBatchProtocol, fmt.Sprintf(format, arguments...))
}

func sourceAuditBlobBatchWaitError(err error, standardError []byte) error {
	detail := bytes.TrimSpace(standardError)
	if len(detail) == 0 {
		return fmt.Errorf("wait for source-audit batch Git: %w", err)
	}
	return fmt.Errorf("wait for source-audit batch Git: %w: %s", err, detail)
}

type sourceAuditBlobBatchErrorBuffer struct {
	buffer   bytes.Buffer
	limit    int
	mu       sync.Mutex
	overflow bool
}

func (buffer *sourceAuditBlobBatchErrorBuffer) Write(content []byte) (int, error) {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	remaining := buffer.limit - buffer.buffer.Len()
	if remaining > len(content) {
		remaining = len(content)
	}
	if remaining > 0 {
		_, _ = buffer.buffer.Write(content[:remaining])
	}
	if remaining < len(content) {
		buffer.overflow = true
	}
	return len(content), nil
}

func (buffer *sourceAuditBlobBatchErrorBuffer) overflowed() bool {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return buffer.overflow
}

func (buffer *sourceAuditBlobBatchErrorBuffer) bytes() []byte {
	buffer.mu.Lock()
	defer buffer.mu.Unlock()
	return bytes.Clone(buffer.buffer.Bytes())
}
