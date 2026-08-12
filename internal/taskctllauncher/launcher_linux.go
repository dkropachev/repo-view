//go:build linux

package taskctllauncher

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"debug/elf"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	maximumTaskctlExecutableBytes  int64 = 256 << 20
	installedLauncherMode                = 0o555
	createdTrustedDirectoryMode          = 0o755
	defaultCancellationGrace             = 2 * time.Second
	defaultTerminationCleanupGrace       = 2 * time.Second
)

var processWorkingDirectoryMu sync.Mutex

type executableIdentity struct {
	device      uint64
	inode       uint64
	mode        uint32
	links       uint64
	uid         uint32
	gid         uint32
	size        int64
	modifiedSec int64
	modifiedNS  int64
	changedSec  int64
	changedNS   int64
}

type directoryIdentity struct {
	device uint64
	inode  uint64
	mode   uint32
	uid    uint32
	gid    uint32
}

func runPlatform(
	ctx context.Context,
	repositoryRoot string,
	arguments []string,
	expectedSHA256 string,
	stdin io.Reader,
	stdout, stderr io.Writer,
	hooks launcherHooks,
) (resultErr error) {
	if !validLowerSHA256(expectedSHA256) {
		return errors.New("taskctl launcher: TASKCTL_EXECUTABLE_SHA256 must be lowercase 64-hex")
	}
	root, rootIdentity, err := openCanonicalRepositoryRoot(repositoryRoot)
	if err != nil {
		return fmt.Errorf("taskctl launcher: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, root.Close()) }()
	if hooks.expectedCWD != nil {
		cwdIdentity, identityErr := identifyDirectory(hooks.expectedCWD)
		if identityErr != nil || cwdIdentity != rootIdentity {
			return errors.New("taskctl launcher: repository pathname does not identify the launcher's working directory")
		}
	}

	executable, err := openTaskctlExecutable(root)
	if err != nil {
		return fmt.Errorf("taskctl launcher: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, executable.Close()) }()
	sealedExecutable, initialIdentity, _, err := prepareSealedExecutable(executable, expectedSHA256)
	if err != nil {
		return fmt.Errorf("taskctl launcher: authenticate bin/taskctl: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, sealedExecutable.Close()) }()
	if hooks.beforeStart != nil {
		if err := hooks.beforeStart(); err != nil {
			return fmt.Errorf("taskctl launcher: before-start check: %w", err)
		}
	}

	// Descriptor execution uses the child fd 3 procfs link. The launcher must
	// be direct-executed in the trusted host mount namespace; the in-process
	// checks cannot authenticate procfs or establish that namespace boundary.
	// The sealed fd prevents source-byte substitution within that boundary.
	command := exec.Command("/proc/self/fd/3", arguments...)
	command.Args[0] = "bin/taskctl"
	command.ExtraFiles = []*os.File{sealedExecutable}
	command.Stdin = stdin
	command.Stdout = stdout
	command.Stderr = stderr
	command.Env = closedChildEnvironment()
	command.SysProcAttr = &syscall.SysProcAttr{
		Setpgid:   true,
		Pdeathsig: syscall.SIGKILL,
	}
	grace := hooks.cancellationGrace
	if grace <= 0 {
		grace = defaultCancellationGrace
	}
	cleanupGrace := hooks.terminationCleanupGrace
	if cleanupGrace <= 0 {
		cleanupGrace = defaultTerminationCleanupGrace
	}
	runErr := runCommandInPinnedDirectory(ctx, command, root, grace, cleanupGrace, hooks)
	if hooks.afterWaitBeforeVerify != nil {
		runErr = errors.Join(runErr, hooks.afterWaitBeforeVerify())
	}
	verificationErr := verifyAfterExecution(
		repositoryRoot,
		rootIdentity,
		executable,
		initialIdentity,
		expectedSHA256,
	)
	if runErr != nil || verificationErr != nil {
		return errors.Join(
			wrapOptionalError("taskctl process", runErr),
			wrapOptionalError("post-execution authentication", verificationErr),
		)
	}
	return nil
}

// runCommandInPinnedDirectory changes the launcher's own working directory to
// the already-authenticated directory descriptor only for the duration of
// fork/exec. The child therefore inherits the pinned directory directly; no
// /proc/self/fd pathname is resolved during Go's pre-exec descriptor shuffle.
//
// Changing cwd is process-wide. The launcher is a single-purpose executable,
// and this mutex serializes all launcher-owned cwd transitions. Embedding this
// internal package in a process whose unrelated goroutines use relative paths
// is outside the launcher boundary.
func runCommandInPinnedDirectory(
	ctx context.Context,
	command *exec.Cmd,
	root *os.File,
	cancellationGrace time.Duration,
	terminationCleanupGrace time.Duration,
	hooks launcherHooks,
) (resultErr error) {
	// Linux parent-death signaling is tied to the thread that created the child,
	// not merely to the launcher process. Keep that thread alive and owned by
	// this lifecycle until the exact child has exited or cleanup has timed out.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	started, pidfd, startErr := startCommandInPinnedDirectory(
		ctx,
		command,
		root,
		hooks.afterCommandStart,
	)
	if pidfd >= 0 {
		defer func() {
			if closeErr := unix.Close(pidfd); closeErr != nil {
				resultErr = errors.Join(
					resultErr,
					fmt.Errorf("taskctl launcher: close child pidfd: %w", closeErr),
				)
			}
		}()
	}
	waitCommand := hooks.waitCommand
	if waitCommand == nil {
		waitCommand = func(command *exec.Cmd) error { return command.Wait() }
	}
	if startErr != nil {
		if started {
			wait := make(chan error, 1)
			go func() { wait <- waitCommand(command) }()
			killErr := signalChildProcess(command.Process.Pid, pidfd, unix.SIGKILL)
			return awaitTerminatedChild(
				startErr,
				killErr,
				command.Process.Pid,
				pidfd,
				wait,
				terminationCleanupGrace,
			)
		}
		return startErr
	}

	wait := make(chan error, 1)
	go func() { wait <- waitCommand(command) }()
	select {
	case err := <-wait:
		return err
	case <-ctx.Done():
		return cancelCommandGroup(
			ctx.Err(),
			command.Process.Pid,
			pidfd,
			wait,
			cancellationGrace,
			terminationCleanupGrace,
		)
	}
}

func startCommandInPinnedDirectory(
	ctx context.Context,
	command *exec.Cmd,
	root *os.File,
	afterStart func() error,
) (started bool, pidfd int, resultErr error) {
	pidfd = -1
	if command == nil || root == nil {
		return false, pidfd, errors.New("taskctl launcher: command and repository descriptor are required")
	}
	processWorkingDirectoryMu.Lock()
	defer processWorkingDirectoryMu.Unlock()
	if err := ctx.Err(); err != nil {
		return false, pidfd, err
	}
	original, err := os.Open(".")
	if err != nil {
		return false, pidfd, errors.New("taskctl launcher: pin original working directory")
	}
	defer func() { resultErr = errors.Join(resultErr, original.Close()) }()
	if command.SysProcAttr == nil {
		command.SysProcAttr = &syscall.SysProcAttr{}
	}
	if command.SysProcAttr.PidFD != nil {
		return false, pidfd, errors.New("taskctl launcher: child pidfd destination is already configured")
	}
	command.SysProcAttr.Setpgid = true
	command.SysProcAttr.Pdeathsig = syscall.SIGKILL
	command.SysProcAttr.PidFD = &pidfd
	if err := unix.Fchdir(int(root.Fd())); err != nil {
		return false, pidfd, fmt.Errorf("taskctl launcher: enter pinned repository directory: %w", err)
	}
	startErr := command.Start()
	started = startErr == nil
	var pidfdErr error
	if started && pidfd < 0 {
		pidfdErr = errors.New("taskctl launcher: kernel did not return an exact child pidfd")
	}
	var afterStartErr error
	if started && afterStart != nil {
		afterStartErr = afterStart()
	}
	restoreErr := unix.Fchdir(int(original.Fd()))
	if startErr != nil || pidfdErr != nil || afterStartErr != nil || restoreErr != nil {
		return started, pidfd, errors.Join(
			wrapOptionalError("taskctl launcher: start taskctl", startErr),
			wrapOptionalError("taskctl launcher: establish child lifecycle handle", pidfdErr),
			wrapOptionalError("taskctl launcher: after-start check", afterStartErr),
			wrapOptionalError("taskctl launcher: restore working directory", restoreErr),
		)
	}
	return true, pidfd, nil
}

func cancelCommandGroup(
	contextErr error,
	processGroupID int,
	pidfd int,
	wait <-chan error,
	grace time.Duration,
	cleanupGrace time.Duration,
) error {
	// This is best-effort termination of the launched process group. A
	// descendant that deliberately creates a different session or process group
	// has escaped this boundary; complete descendant containment belongs to the
	// task runner's cgroup lifecycle, not this launcher.
	if grace <= 0 {
		grace = defaultCancellationGrace
	}
	if cleanupGrace <= 0 {
		cleanupGrace = defaultTerminationCleanupGrace
	}
	termErr := signalChildProcess(processGroupID, pidfd, unix.SIGTERM)
	timer := time.NewTimer(grace)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	waitChannel := wait
	waitDone := wait == nil
	var waitErr error
	for {
		leaderExited, leaderErr := childLeaderExited(pidfd, waitDone)
		groupExists, groupErr := processGroupExists(processGroupID)
		if leaderErr != nil || groupErr != nil {
			return errors.Join(contextErr, termErr, waitErr, leaderErr, groupErr)
		}
		if waitDone && leaderExited && !groupExists {
			return errors.Join(contextErr, termErr, waitErr)
		}
		select {
		case waitErr = <-waitChannel:
			waitDone = true
			waitChannel = nil
		case <-ticker.C:
		case <-timer.C:
			killErr := signalChildProcess(processGroupID, pidfd, unix.SIGKILL)
			return awaitTerminatedChild(
				contextErr,
				errors.Join(termErr, waitErr, killErr),
				processGroupID,
				pidfd,
				waitChannel,
				cleanupGrace,
			)
		}
	}
}

func awaitTerminatedChild(
	primaryErr error,
	signalErr error,
	processGroupID int,
	pidfd int,
	wait <-chan error,
	grace time.Duration,
) error {
	if grace <= 0 {
		grace = defaultTerminationCleanupGrace
	}
	timer := time.NewTimer(grace)
	defer timer.Stop()
	ticker := time.NewTicker(10 * time.Millisecond)
	defer ticker.Stop()
	waitChannel := wait
	waitDone := wait == nil
	var waitErr error
	for {
		leaderExited, leaderErr := childLeaderExited(pidfd, waitDone)
		groupExists, groupErr := processGroupExists(processGroupID)
		if leaderErr != nil || groupErr != nil {
			return errors.Join(primaryErr, signalErr, waitErr, leaderErr, groupErr)
		}
		if waitDone && leaderExited && !groupExists {
			return errors.Join(primaryErr, signalErr, waitErr)
		}
		select {
		case waitErr = <-waitChannel:
			waitDone = true
			waitChannel = nil
		case <-ticker.C:
		case <-timer.C:
			leaderExited, leaderErr = childLeaderExited(pidfd, waitDone)
			groupExists, groupErr = processGroupExists(processGroupID)
			if leaderErr != nil || groupErr != nil {
				return errors.Join(primaryErr, signalErr, waitErr, leaderErr, groupErr)
			}
			var timeoutErr error
			if !waitDone {
				timeoutErr = errors.Join(
					timeoutErr,
					errors.New("taskctl launcher: timed out waiting for taskctl Wait after termination"),
				)
			}
			if !leaderExited {
				timeoutErr = errors.Join(
					timeoutErr,
					errors.New("taskctl launcher: taskctl remained alive after termination signal"),
				)
			}
			if groupExists {
				timeoutErr = errors.Join(
					timeoutErr,
					errors.New("taskctl launcher: child process group remained after termination signal"),
				)
			}
			return errors.Join(primaryErr, signalErr, waitErr, timeoutErr)
		}
	}
}

func childLeaderExited(pidfd int, waitDone bool) (bool, error) {
	if pidfd < 0 {
		return waitDone, nil
	}
	return pidfdProcessExited(pidfd)
}

func signalChildProcess(processGroupID, pidfd int, signal unix.Signal) error {
	return errors.Join(
		signalProcessGroup(processGroupID, signal),
		signalPidfd(pidfd, signal),
	)
}

func signalPidfd(pidfd int, signal unix.Signal) error {
	if pidfd < 0 {
		return errors.New("taskctl launcher: exact child pidfd is unavailable")
	}
	err := unix.PidfdSendSignal(pidfd, signal, nil, 0)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("taskctl launcher: signal exact child pidfd: %w", err)
	}
	return nil
}

func pidfdProcessExited(pidfd int) (bool, error) {
	if pidfd < 0 {
		return false, errors.New("taskctl launcher: exact child pidfd is unavailable")
	}
	status := []unix.PollFd{{
		Fd:     int32(pidfd),
		Events: unix.POLLIN,
	}}
	for {
		count, err := unix.Poll(status, 0)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			return false, fmt.Errorf("taskctl launcher: inspect exact child pidfd: %w", err)
		}
		if count == 0 {
			return false, nil
		}
		return status[0].Revents&(unix.POLLIN|unix.POLLHUP|unix.POLLERR) != 0, nil
	}
}

func signalProcessGroup(processGroupID int, signal unix.Signal) error {
	if processGroupID <= 0 {
		return errors.New("taskctl launcher: invalid child process-group ID")
	}
	err := unix.Kill(-processGroupID, signal)
	if errors.Is(err, unix.ESRCH) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("taskctl launcher: signal child process group: %w", err)
	}
	return nil
}

func processGroupExists(processGroupID int) (bool, error) {
	if processGroupID <= 0 {
		return false, errors.New("taskctl launcher: invalid child process-group ID")
	}
	err := unix.Kill(-processGroupID, 0)
	switch {
	case err == nil, errors.Is(err, unix.EPERM):
		return true, nil
	case errors.Is(err, unix.ESRCH):
		return false, nil
	default:
		return false, fmt.Errorf("taskctl launcher: inspect child process group: %w", err)
	}
}

func inspectPlatform(
	repositoryRoot string,
	stdout io.Writer,
	hooks launcherHooks,
) (resultErr error) {
	root, rootIdentity, err := openCanonicalRepositoryRoot(repositoryRoot)
	if err != nil {
		return fmt.Errorf("taskctl launcher: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, root.Close()) }()
	if hooks.expectedCWD != nil {
		cwdIdentity, identityErr := identifyDirectory(hooks.expectedCWD)
		if identityErr != nil || cwdIdentity != rootIdentity {
			return errors.New("taskctl launcher: repository pathname does not identify the launcher's working directory")
		}
	}

	executable, err := openTaskctlExecutable(root)
	if err != nil {
		return fmt.Errorf("taskctl launcher: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, executable.Close()) }()
	sealed, identity, digest, err := prepareSealedExecutable(executable, "")
	if err != nil {
		return fmt.Errorf("taskctl launcher: inspect bin/taskctl: %w", err)
	}
	if err := sealed.Close(); err != nil {
		return fmt.Errorf("taskctl launcher: close inspected sealed executable: %w", err)
	}
	if err := verifyAfterExecution(
		repositoryRoot,
		rootIdentity,
		executable,
		identity,
		digest,
	); err != nil {
		return fmt.Errorf("taskctl launcher: post-inspection authentication: %w", err)
	}
	if _, err := fmt.Fprintln(stdout, digest); err != nil {
		return fmt.Errorf("taskctl launcher: write executable SHA-256: %w", err)
	}
	return nil
}

func openCanonicalRepositoryRoot(path string) (*os.File, directoryIdentity, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, directoryIdentity{}, errors.New("repository working directory must be a clean absolute path")
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, directoryIdentity{}, fmt.Errorf("canonicalize repository working directory: %w", err)
	}
	if canonical != path {
		return nil, directoryIdentity{}, errors.New("repository working directory must contain no symbolic-link component")
	}
	descriptor, err := unix.Openat2(unix.AT_FDCWD, path, &unix.OpenHow{
		Flags:   uint64(unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW),
		Resolve: unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_SYMLINKS,
	})
	if err != nil {
		return nil, directoryIdentity{}, fmt.Errorf("open repository working directory without symbolic links: %w", err)
	}
	root := os.NewFile(uintptr(descriptor), path)
	if root == nil {
		_ = unix.Close(descriptor)
		return nil, directoryIdentity{}, errors.New("open repository working directory descriptor")
	}
	identity, err := identifyDirectory(root)
	if err != nil {
		_ = root.Close()
		return nil, directoryIdentity{}, err
	}
	canonicalAfter, err := filepath.EvalSymlinks(path)
	if err != nil || canonicalAfter != path {
		_ = root.Close()
		return nil, directoryIdentity{}, errors.New("repository working directory changed while opening")
	}
	visible, err := os.Stat(path)
	if err != nil {
		_ = root.Close()
		return nil, directoryIdentity{}, fmt.Errorf("inspect repository working directory pathname: %w", err)
	}
	pinned, err := root.Stat()
	if err != nil || !os.SameFile(visible, pinned) || visible.Mode() != pinned.Mode() {
		_ = root.Close()
		return nil, directoryIdentity{}, errors.New("repository working directory pathname does not match its pinned descriptor")
	}
	return root, identity, nil
}

func openTaskctlExecutable(root *os.File) (*os.File, error) {
	if root == nil {
		return nil, errors.New("repository descriptor is required")
	}
	descriptor, err := unix.Openat2(int(root.Fd()), "bin/taskctl", &unix.OpenHow{
		Flags: uint64(unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK),
		Resolve: unix.RESOLVE_BENEATH |
			unix.RESOLVE_NO_MAGICLINKS |
			unix.RESOLVE_NO_SYMLINKS,
	})
	if err != nil {
		return nil, fmt.Errorf("open fixed bin/taskctl without symbolic links: %w", err)
	}
	file := os.NewFile(uintptr(descriptor), "bin/taskctl")
	if file == nil {
		_ = unix.Close(descriptor)
		return nil, errors.New("open bin/taskctl descriptor")
	}
	return file, nil
}

func prepareSealedExecutable(
	file *os.File,
	expectedSHA256 string,
) (_ *os.File, identity executableIdentity, actualSHA256 string, resultErr error) {
	if file == nil {
		return nil, executableIdentity{}, "", errors.New("executable descriptor is required")
	}
	before, err := identifyExecutable(file)
	if err != nil {
		return nil, executableIdentity{}, "", err
	}
	if before.mode&unix.S_IFMT != unix.S_IFREG || before.mode&0o111 == 0 {
		return nil, executableIdentity{}, "", errors.New("executable must be an executable regular file")
	}
	if before.links != 1 {
		return nil, executableIdentity{}, "", errors.New("executable must have exactly one hard link")
	}
	if before.uid != uint32(os.Geteuid()) {
		return nil, executableIdentity{}, "", errors.New("executable must be owned by the current user")
	}
	if before.size < 0 || before.size > maximumTaskctlExecutableBytes {
		return nil, executableIdentity{}, "", errors.New("executable exceeds the 256 MiB authentication limit")
	}
	if expectedSHA256 != "" && !validLowerSHA256(expectedSHA256) {
		return nil, executableIdentity{}, "", errors.New("expected executable SHA-256 must be lowercase 64-hex")
	}

	descriptor, err := unix.MemfdCreate(
		"scopesifter-taskctl-authenticated",
		unix.MFD_CLOEXEC|unix.MFD_ALLOW_SEALING|unix.MFD_EXEC,
	)
	if err != nil {
		return nil, executableIdentity{}, "", fmt.Errorf("create executable memfd: %w", err)
	}
	sealed := os.NewFile(uintptr(descriptor), "authenticated-taskctl")
	if sealed == nil {
		_ = unix.Close(descriptor)
		return nil, executableIdentity{}, "", errors.New("create executable memfd file")
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, sealed.Close())
		}
	}()

	digest := sha256.New()
	written, err := io.Copy(
		io.MultiWriter(sealed, digest),
		io.NewSectionReader(file, 0, before.size),
	)
	if err != nil {
		return nil, executableIdentity{}, "", fmt.Errorf("copy executable into sealed image: %w", err)
	}
	if written != before.size {
		return nil, executableIdentity{}, "", errors.New("executable size changed while copying")
	}
	actual := fmt.Sprintf("%x", digest.Sum(nil))
	if expectedSHA256 != "" && actual != expectedSHA256 {
		return nil, executableIdentity{}, "", fmt.Errorf("executable SHA-256 is %s, want %s", actual, expectedSHA256)
	}
	after, err := identifyExecutable(file)
	if err != nil {
		return nil, executableIdentity{}, "", err
	}
	if after != before {
		return nil, executableIdentity{}, "", errors.New("executable changed while it was copied")
	}
	if err := sealed.Sync(); err != nil {
		return nil, executableIdentity{}, "", fmt.Errorf("sync executable memfd: %w", err)
	}
	if err := sealed.Chmod(0o500); err != nil {
		return nil, executableIdentity{}, "", fmt.Errorf("set executable memfd mode: %w", err)
	}
	requiredSeals := unix.F_SEAL_WRITE |
		unix.F_SEAL_GROW |
		unix.F_SEAL_SHRINK |
		unix.F_SEAL_EXEC |
		unix.F_SEAL_SEAL
	if _, err := unix.FcntlInt(sealed.Fd(), unix.F_ADD_SEALS, requiredSeals); err != nil {
		if errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) ||
			errors.Is(err, unix.EOPNOTSUPP) {
			return nil, executableIdentity{}, "", errors.New("kernel does not support required executable memfd seals")
		}
		return nil, executableIdentity{}, "", fmt.Errorf("seal executable memfd: %w", err)
	}
	seals, err := unix.FcntlInt(sealed.Fd(), unix.F_GET_SEALS, 0)
	if err != nil {
		return nil, executableIdentity{}, "", fmt.Errorf("inspect executable memfd seals: %w", err)
	}
	if seals&requiredSeals != requiredSeals {
		return nil, executableIdentity{}, "", errors.New("executable memfd did not retain all required seals")
	}
	sealedIdentity, err := identifyExecutable(sealed)
	if err != nil {
		return nil, executableIdentity{}, "", err
	}
	if sealedIdentity.mode&unix.S_IFMT != unix.S_IFREG ||
		sealedIdentity.mode&0o777 != 0o500 || sealedIdentity.size != before.size {
		return nil, executableIdentity{}, "", errors.New("sealed executable identity is invalid")
	}
	sealedDigest, err := digestExecutable(sealed, before.size)
	if err != nil {
		return nil, executableIdentity{}, "", err
	}
	if sealedDigest != actual {
		return nil, executableIdentity{}, "", errors.New("sealed executable changed after authentication")
	}
	if err := validateStaticELF(sealed); err != nil {
		return nil, executableIdentity{}, "", err
	}
	return sealed, before, actual, nil
}

func authenticateExecutable(file *os.File, expectedSHA256 string) (executableIdentity, error) {
	sealed, identity, _, err := prepareSealedExecutable(file, expectedSHA256)
	if err != nil {
		return executableIdentity{}, err
	}
	if err := sealed.Close(); err != nil {
		return executableIdentity{}, fmt.Errorf("close authenticated executable image: %w", err)
	}
	return identity, nil
}

func validateStaticELF(file *os.File) (resultErr error) {
	image, err := elf.NewFile(file)
	if err != nil {
		return fmt.Errorf("parse executable ELF image: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, image.Close()) }()
	if image.Type != elf.ET_EXEC && image.Type != elf.ET_DYN {
		return fmt.Errorf("executable ELF type %s is not executable", image.Type)
	}
	expectedMachine, expectedClass, expectedData, ok := nativeELFIdentity()
	if !ok || image.Machine != expectedMachine || image.Class != expectedClass || image.Data != expectedData {
		return fmt.Errorf("executable ELF machine %s does not match linux/%s", image.Machine, runtime.GOARCH)
	}
	if image.Version != elf.EV_CURRENT {
		return fmt.Errorf("executable ELF version %s is not current", image.Version)
	}
	if image.OSABI != elf.ELFOSABI_NONE && image.OSABI != elf.ELFOSABI_LINUX {
		return fmt.Errorf("executable ELF OS ABI %s is not Linux", image.OSABI)
	}
	for _, program := range image.Progs {
		if program.Type == elf.PT_INTERP {
			return errors.New("executable ELF image contains a program interpreter")
		}
	}
	libraries, err := image.ImportedLibraries()
	if err != nil {
		return fmt.Errorf("inspect executable ELF dynamic dependencies: %w", err)
	}
	if len(libraries) != 0 {
		return fmt.Errorf("executable ELF image has dynamic dependencies: %v", libraries)
	}
	return nil
}

func nativeELFIdentity() (elf.Machine, elf.Class, elf.Data, bool) {
	switch runtime.GOARCH {
	case "386":
		return elf.EM_386, elf.ELFCLASS32, elf.ELFDATA2LSB, true
	case "amd64":
		return elf.EM_X86_64, elf.ELFCLASS64, elf.ELFDATA2LSB, true
	case "arm":
		return elf.EM_ARM, elf.ELFCLASS32, elf.ELFDATA2LSB, true
	case "arm64":
		return elf.EM_AARCH64, elf.ELFCLASS64, elf.ELFDATA2LSB, true
	case "ppc64":
		return elf.EM_PPC64, elf.ELFCLASS64, elf.ELFDATA2MSB, true
	case "ppc64le":
		return elf.EM_PPC64, elf.ELFCLASS64, elf.ELFDATA2LSB, true
	case "riscv64":
		return elf.EM_RISCV, elf.ELFCLASS64, elf.ELFDATA2LSB, true
	case "s390x":
		return elf.EM_S390, elf.ELFCLASS64, elf.ELFDATA2MSB, true
	default:
		return elf.EM_NONE, elf.ELFCLASSNONE, elf.ELFDATANONE, false
	}
}

// installPlatform is convenience for installing a raw release artifact only
// after the administrator has independently verified its release attestation
// and SHA-256. Because the candidate code is already executing as root, none of
// these checks constitute a trust bootstrap or authenticate the candidate.
func installPlatform(expectedSHA256 string, stdout io.Writer) error {
	if !validLowerSHA256(expectedSHA256) {
		return errors.New("taskctl launcher: install digest must be lowercase 64-hex")
	}
	if err := verifyInitialIdentityMappings(); err != nil {
		return fmt.Errorf("trusted launcher installation boundary: %w", err)
	}
	if err := verifyExecutionIdentity(true); err != nil {
		return err
	}
	if err := installTrustedLauncher(
		"/",
		installedLauncherPath,
		"/proc/self/exe",
		expectedSHA256,
		0,
		0,
	); err != nil {
		return fmt.Errorf("taskctl launcher: install trusted launcher: %w", err)
	}
	if _, err := fmt.Fprintln(stdout, installedLauncherPath); err != nil {
		return fmt.Errorf("taskctl launcher: report installed launcher path: %w", err)
	}
	return nil
}

func installTrustedLauncher(
	authorityRoot, launcherPath, executingPath, expectedSHA256 string,
	expectedUID, expectedGID uint32,
) (resultErr error) {
	if !cleanAbsolutePath(authorityRoot) || !cleanAbsolutePath(launcherPath) ||
		!cleanAbsolutePath(executingPath) {
		return errors.New("installation paths must be clean and absolute")
	}
	if !validLowerSHA256(expectedSHA256) {
		return errors.New("installation digest must be lowercase 64-hex")
	}
	relative, err := trustedRelativePath(authorityRoot, launcherPath)
	if err != nil {
		return err
	}
	base := filepath.Base(relative)
	if base == "." || base == string(filepath.Separator) || base == "" {
		return errors.New("installed launcher basename is invalid")
	}

	source, err := os.Open(executingPath)
	if err != nil {
		return fmt.Errorf("open executing launcher image: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, source.Close()) }()
	sourceIdentity, err := authenticateProvisioningSource(source, expectedSHA256)
	if err != nil {
		return fmt.Errorf("authenticate executing launcher image: %w", err)
	}
	visibleSource, err := os.Stat(executingPath)
	if err != nil {
		return fmt.Errorf("inspect executing launcher image: %w", err)
	}
	pinnedSource, err := source.Stat()
	if err != nil || !os.SameFile(visibleSource, pinnedSource) {
		return errors.New("executing launcher pathname does not identify its pinned inode")
	}

	directory, err := ensureTrustedDirectoryPath(
		authorityRoot,
		filepath.Dir(relative),
		expectedUID,
		expectedGID,
	)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, directory.Close()) }()
	if err := verifyReplaceableInstalledLauncher(directory, base, expectedUID, expectedGID); err != nil {
		return err
	}

	temporaryName, temporary, err := createLauncherTemporaryFile(
		directory,
		expectedUID,
		expectedGID,
	)
	if err != nil {
		return err
	}
	renamed := false
	defer func() {
		resultErr = errors.Join(resultErr, temporary.Close())
		if !renamed {
			unlinkErr := unix.Unlinkat(int(directory.Fd()), temporaryName, 0)
			if unlinkErr != nil && !errors.Is(unlinkErr, unix.ENOENT) {
				resultErr = errors.Join(resultErr, fmt.Errorf("remove launcher temporary file: %w", unlinkErr))
			}
		}
	}()

	digest := sha256.New()
	written, err := io.Copy(
		io.MultiWriter(temporary, digest),
		io.NewSectionReader(source, 0, sourceIdentity.size),
	)
	if err != nil {
		return fmt.Errorf("copy executing launcher image: %w", err)
	}
	if written != sourceIdentity.size {
		return errors.New("executing launcher size changed while copying")
	}
	if actual := fmt.Sprintf("%x", digest.Sum(nil)); actual != expectedSHA256 {
		return fmt.Errorf("copied launcher SHA-256 is %s, want %s", actual, expectedSHA256)
	}
	afterSource, err := identifyExecutable(source)
	if err != nil {
		return err
	}
	if afterSource != sourceIdentity {
		return errors.New("executing launcher changed while it was copied")
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync launcher temporary image: %w", err)
	}
	temporaryIdentity, err := identifyExecutable(temporary)
	if err != nil {
		return err
	}
	if temporaryIdentity.uid != expectedUID || temporaryIdentity.gid != expectedGID {
		if err := unix.Fchown(int(temporary.Fd()), int(expectedUID), int(expectedGID)); err != nil {
			return fmt.Errorf("set launcher temporary ownership: %w", err)
		}
	}
	if err := unix.Fchmod(int(temporary.Fd()), installedLauncherMode); err != nil {
		return fmt.Errorf("set installed launcher mode: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		return fmt.Errorf("sync prepared launcher image: %w", err)
	}
	if err := verifyInstalledLauncherDescriptor(
		temporary,
		expectedSHA256,
		expectedUID,
		expectedGID,
	); err != nil {
		return fmt.Errorf("verify prepared launcher image: %w", err)
	}
	if err := unix.Renameat(
		int(directory.Fd()), temporaryName,
		int(directory.Fd()), base,
	); err != nil {
		return fmt.Errorf("atomically install launcher image: %w", err)
	}
	renamed = true
	if err := directory.Sync(); err != nil {
		return fmt.Errorf("sync installed launcher directory: %w", err)
	}
	if err := verifyProvisionedLauncher(
		authorityRoot,
		launcherPath,
		temporary,
		expectedSHA256,
		expectedUID,
		expectedGID,
	); err != nil {
		return fmt.Errorf("post-verify installed launcher: %w", err)
	}
	return nil
}

func authenticateProvisioningSource(
	file *os.File,
	expectedSHA256 string,
) (executableIdentity, error) {
	identity, err := identifyExecutable(file)
	if err != nil {
		return executableIdentity{}, err
	}
	if identity.mode&unix.S_IFMT != unix.S_IFREG || identity.mode&0o111 == 0 {
		return executableIdentity{}, errors.New("executing launcher must be an executable regular file")
	}
	if identity.mode&(unix.S_ISUID|unix.S_ISGID|unix.S_ISVTX) != 0 {
		return executableIdentity{}, errors.New("executing launcher must have no special mode bits")
	}
	if identity.links != 1 {
		return executableIdentity{}, errors.New("executing launcher must have exactly one hard link")
	}
	if identity.size < 0 || identity.size > maximumTaskctlExecutableBytes {
		return executableIdentity{}, errors.New("executing launcher exceeds the 256 MiB authentication limit")
	}
	if err := verifyNoFileCapabilities(file); err != nil {
		return executableIdentity{}, fmt.Errorf("executing launcher capabilities: %w", err)
	}
	digest, err := digestExecutable(file, identity.size)
	if err != nil {
		return executableIdentity{}, err
	}
	if digest != expectedSHA256 {
		return executableIdentity{}, fmt.Errorf("executing launcher SHA-256 is %s, want %s", digest, expectedSHA256)
	}
	if err := validateStaticELF(file); err != nil {
		return executableIdentity{}, fmt.Errorf("executing launcher is not a static native image: %w", err)
	}
	after, err := identifyExecutable(file)
	if err != nil {
		return executableIdentity{}, err
	}
	if after != identity {
		return executableIdentity{}, errors.New("executing launcher changed during authentication")
	}
	return identity, nil
}

func trustedRelativePath(authorityRoot, target string) (string, error) {
	canonicalAuthority, err := filepath.EvalSymlinks(authorityRoot)
	if err != nil || canonicalAuthority != authorityRoot {
		return "", errors.New("trusted launcher authority root must contain no symbolic-link component")
	}
	relative, err := filepath.Rel(authorityRoot, target)
	if err != nil || relative == "." || relative == ".." || filepath.IsAbs(relative) ||
		filepath.Clean(relative) != relative || filepath.Dir(relative) == ".." ||
		len(relative) >= 3 && relative[:3] == ".."+string(filepath.Separator) {
		return "", errors.New("installed launcher must be below its authority root")
	}
	return relative, nil
}

func ensureTrustedDirectoryPath(
	authorityRoot, relative string,
	expectedUID, expectedGID uint32,
) (_ *os.File, resultErr error) {
	if relative == "" || filepath.IsAbs(relative) || filepath.Clean(relative) != relative {
		return nil, errors.New("trusted directory path must be clean and relative")
	}
	descriptor, err := unix.Openat2(unix.AT_FDCWD, authorityRoot, &unix.OpenHow{
		Flags:   uint64(unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW),
		Resolve: unix.RESOLVE_NO_MAGICLINKS | unix.RESOLVE_NO_SYMLINKS,
	})
	if err != nil {
		return nil, fmt.Errorf("open trusted launcher authority root: %w", err)
	}
	current := os.NewFile(uintptr(descriptor), authorityRoot)
	if current == nil {
		_ = unix.Close(descriptor)
		return nil, errors.New("open trusted launcher authority descriptor")
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, current.Close())
		}
	}()
	if err := verifyTrustedDirectoryDescriptor(current, expectedUID, expectedGID); err != nil {
		return nil, fmt.Errorf("verify trusted launcher authority root: %w", err)
	}
	if relative == "." {
		return current, nil
	}
	for _, component := range strings.Split(relative, string(filepath.Separator)) {
		if component == "" || component == "." || component == ".." {
			return nil, errors.New("trusted directory path contains an invalid component")
		}
		created := false
		if err := unix.Mkdirat(int(current.Fd()), component, createdTrustedDirectoryMode); err != nil {
			if !errors.Is(err, unix.EEXIST) {
				return nil, fmt.Errorf("create trusted launcher directory %s: %w", component, err)
			}
		} else {
			created = true
		}
		if created {
			if err := unix.Fchmodat(
				int(current.Fd()),
				component,
				createdTrustedDirectoryMode,
				0,
			); err != nil {
				return nil, fmt.Errorf("set newly created launcher directory mode: %w", err)
			}
		}
		nextDescriptor, err := unix.Openat2(int(current.Fd()), component, &unix.OpenHow{
			Flags: uint64(unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW),
			Resolve: unix.RESOLVE_BENEATH |
				unix.RESOLVE_NO_MAGICLINKS |
				unix.RESOLVE_NO_SYMLINKS,
		})
		if err != nil {
			return nil, fmt.Errorf("open trusted launcher directory %s: %w", component, err)
		}
		next := os.NewFile(uintptr(nextDescriptor), component)
		if next == nil {
			_ = unix.Close(nextDescriptor)
			return nil, fmt.Errorf("open trusted launcher directory descriptor %s", component)
		}
		if created {
			identity, identityErr := identifyDirectory(next)
			if identityErr != nil {
				_ = next.Close()
				return nil, identityErr
			}
			if identity.uid != expectedUID || identity.gid != expectedGID {
				if err := unix.Fchown(int(next.Fd()), int(expectedUID), int(expectedGID)); err != nil {
					_ = next.Close()
					return nil, fmt.Errorf("set trusted launcher directory ownership: %w", err)
				}
			}
			if err := unix.Fchmod(int(next.Fd()), createdTrustedDirectoryMode); err != nil {
				_ = next.Close()
				return nil, fmt.Errorf("set trusted launcher directory mode: %w", err)
			}
			if err := next.Sync(); err != nil {
				_ = next.Close()
				return nil, fmt.Errorf("sync trusted launcher directory: %w", err)
			}
			if err := current.Sync(); err != nil {
				_ = next.Close()
				return nil, fmt.Errorf("sync trusted launcher parent directory: %w", err)
			}
		}
		if err := verifyTrustedDirectoryDescriptor(next, expectedUID, expectedGID); err != nil {
			_ = next.Close()
			return nil, fmt.Errorf("verify trusted launcher directory %s: %w", component, err)
		}
		if err := current.Close(); err != nil {
			_ = next.Close()
			return nil, fmt.Errorf("close trusted launcher parent directory: %w", err)
		}
		current = next
	}
	return current, nil
}

func verifyTrustedDirectoryDescriptor(file *os.File, expectedUID, expectedGID uint32) error {
	identity, err := identifyDirectory(file)
	if err != nil {
		return err
	}
	if identity.uid != expectedUID || identity.gid != expectedGID {
		return fmt.Errorf(
			"directory ownership is %d:%d, want %d:%d",
			identity.uid, identity.gid, expectedUID, expectedGID,
		)
	}
	if identity.mode&(unix.S_ISUID|unix.S_ISGID|unix.S_ISVTX) != 0 {
		return errors.New("directory has special mode bits")
	}
	if identity.mode&0o022 != 0 {
		return errors.New("directory is writable by group or other")
	}
	if identity.mode&0o111 != 0o111 {
		return errors.New("directory is not searchable by owner, group, and other")
	}
	return nil
}

func verifyReplaceableInstalledLauncher(
	directory *os.File,
	base string,
	expectedUID, expectedGID uint32,
) (resultErr error) {
	descriptor, err := unix.Openat2(int(directory.Fd()), base, &unix.OpenHow{
		Flags: uint64(unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK),
		Resolve: unix.RESOLVE_BENEATH |
			unix.RESOLVE_NO_MAGICLINKS |
			unix.RESOLVE_NO_SYMLINKS,
	})
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open existing installed launcher: %w", err)
	}
	file := os.NewFile(uintptr(descriptor), base)
	if file == nil {
		_ = unix.Close(descriptor)
		return errors.New("open existing installed launcher descriptor")
	}
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	if err := verifyInstalledLauncherDescriptor(file, "", expectedUID, expectedGID); err != nil {
		return fmt.Errorf("existing installed launcher is not safely replaceable: %w", err)
	}
	return nil
}

func createLauncherTemporaryFile(
	directory *os.File,
	expectedUID, expectedGID uint32,
) (string, *os.File, error) {
	for range 32 {
		random := make([]byte, 16)
		if _, err := io.ReadFull(rand.Reader, random); err != nil {
			return "", nil, fmt.Errorf("generate launcher temporary name: %w", err)
		}
		name := ".taskctl-launcher.install-" + hex.EncodeToString(random)
		descriptor, err := unix.Openat(
			int(directory.Fd()),
			name,
			unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW,
			0o600,
		)
		if errors.Is(err, unix.EEXIST) {
			continue
		}
		if err != nil {
			return "", nil, fmt.Errorf("create launcher temporary file: %w", err)
		}
		file := os.NewFile(uintptr(descriptor), name)
		if file == nil {
			_ = unix.Close(descriptor)
			_ = unix.Unlinkat(int(directory.Fd()), name, 0)
			return "", nil, errors.New("create launcher temporary file descriptor")
		}
		identity, identityErr := identifyExecutable(file)
		if identityErr != nil || identity.mode&unix.S_IFMT != unix.S_IFREG ||
			(identity.mode&0o7777)&^uint32(0o600) != 0 || identity.links != 1 ||
			identity.uid != expectedUID || identity.gid != expectedGID {
			_ = file.Close()
			_ = unix.Unlinkat(int(directory.Fd()), name, 0)
			return "", nil, errors.New("launcher temporary file identity is invalid")
		}
		if err := unix.Fchmod(int(file.Fd()), 0o600); err != nil {
			_ = file.Close()
			_ = unix.Unlinkat(int(directory.Fd()), name, 0)
			return "", nil, fmt.Errorf("set launcher temporary mode: %w", err)
		}
		identity, identityErr = identifyExecutable(file)
		if identityErr != nil || identity.mode&0o7777 != 0o600 {
			_ = file.Close()
			_ = unix.Unlinkat(int(directory.Fd()), name, 0)
			return "", nil, errors.New("launcher temporary file mode is invalid")
		}
		return name, file, nil
	}
	return "", nil, errors.New("exhausted launcher temporary-name attempts")
}

func verifyInstalledLauncherDescriptor(
	file *os.File,
	expectedSHA256 string,
	expectedUID, expectedGID uint32,
) error {
	identity, err := identifyExecutable(file)
	if err != nil {
		return err
	}
	if identity.mode&unix.S_IFMT != unix.S_IFREG ||
		identity.mode&0o7777 != installedLauncherMode {
		return fmt.Errorf("installed launcher mode must be exactly %#o", installedLauncherMode)
	}
	if identity.uid != expectedUID || identity.gid != expectedGID {
		return fmt.Errorf(
			"installed launcher ownership is %d:%d, want %d:%d",
			identity.uid, identity.gid, expectedUID, expectedGID,
		)
	}
	if identity.links != 1 {
		return errors.New("installed launcher must have exactly one hard link")
	}
	if identity.size < 0 || identity.size > maximumTaskctlExecutableBytes {
		return errors.New("installed launcher exceeds the 256 MiB authentication limit")
	}
	if err := verifyNoFileCapabilities(file); err != nil {
		return fmt.Errorf("installed launcher capabilities: %w", err)
	}
	if expectedSHA256 != "" {
		digest, err := digestExecutable(file, identity.size)
		if err != nil {
			return err
		}
		if digest != expectedSHA256 {
			return fmt.Errorf("installed launcher SHA-256 is %s, want %s", digest, expectedSHA256)
		}
	}
	if err := validateStaticELF(file); err != nil {
		return fmt.Errorf("installed launcher is not a static native image: %w", err)
	}
	after, err := identifyExecutable(file)
	if err != nil {
		return err
	}
	if after != identity {
		return errors.New("installed launcher changed during verification")
	}
	return nil
}

func verifyProvisionedLauncher(
	authorityRoot, launcherPath string,
	installed *os.File,
	expectedSHA256 string,
	expectedUID, expectedGID uint32,
) (resultErr error) {
	relative, err := trustedRelativePath(authorityRoot, launcherPath)
	if err != nil {
		return err
	}
	if err := verifyTrustedLauncherPathComponents(
		authorityRoot,
		relative,
		expectedUID,
		expectedGID,
	); err != nil {
		return err
	}
	descriptor, err := unix.Openat2(unix.AT_FDCWD, launcherPath, &unix.OpenHow{
		Flags: uint64(unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK),
		Resolve: unix.RESOLVE_NO_MAGICLINKS |
			unix.RESOLVE_NO_SYMLINKS,
	})
	if err != nil {
		return fmt.Errorf("open installed launcher without symbolic links: %w", err)
	}
	visible := os.NewFile(uintptr(descriptor), launcherPath)
	if visible == nil {
		_ = unix.Close(descriptor)
		return errors.New("open installed launcher descriptor")
	}
	defer func() { resultErr = errors.Join(resultErr, visible.Close()) }()
	visibleInfo, err := visible.Stat()
	if err != nil {
		return fmt.Errorf("inspect installed launcher: %w", err)
	}
	installedInfo, err := installed.Stat()
	if err != nil || !os.SameFile(visibleInfo, installedInfo) {
		return errors.New("installed launcher pathname does not identify the installed inode")
	}
	return verifyInstalledLauncherDescriptor(
		visible,
		expectedSHA256,
		expectedUID,
		expectedGID,
	)
}

func verifyOperationalLauncher() error {
	if err := verifyInitialIdentityMappings(); err != nil {
		return fmt.Errorf("trusted launcher boundary: %w", err)
	}
	if err := verifyExecutionIdentity(false); err != nil {
		return err
	}
	return verifyTrustedLauncherInstallation(
		"/",
		installedLauncherPath,
		"/proc/self/exe",
		0,
		0,
	)
}

// verifyInitialIdentityMappings rejects an unprivileged user namespace, which
// could otherwise manufacture namespace-local UID 0 path ownership. Launcher
// path authentication is still relative to the caller's mount namespace: the
// administrator must direct-execute the launcher in the trusted host mount
// namespace. A process already privileged in the initial user namespace can
// create or enter another mount namespace and is part of that host boundary.
func verifyInitialIdentityMappings() error {
	for _, path := range []string{"/proc/self/uid_map", "/proc/self/gid_map"} {
		file, err := os.Open(path)
		if err != nil {
			return fmt.Errorf("open identity mapping %s: %w", path, err)
		}
		data, readErr := io.ReadAll(io.LimitReader(file, 257))
		closeErr := file.Close()
		if readErr != nil || closeErr != nil {
			return errors.Join(
				wrapOptionalError("read identity mapping "+path, readErr),
				wrapOptionalError("close identity mapping "+path, closeErr),
			)
		}
		if len(data) > 256 {
			return fmt.Errorf("identity mapping %s is unexpectedly large", path)
		}
		fields := strings.Fields(string(data))
		if len(fields) != 3 {
			return fmt.Errorf("identity mapping %s is not the full initial mapping", path)
		}
		values := [3]uint64{}
		for index, field := range fields {
			value, parseErr := strconv.ParseUint(field, 10, 64)
			if parseErr != nil {
				return fmt.Errorf("parse identity mapping %s: %w", path, parseErr)
			}
			values[index] = value
		}
		if values != [3]uint64{0, 0, 1<<32 - 1} {
			return fmt.Errorf("identity mapping %s is not the full initial mapping", path)
		}
	}
	return nil
}

func verifyExecutionIdentity(requireRoot bool) error {
	realUID, effectiveUID, savedUID := unix.Getresuid()
	realGID, effectiveGID, savedGID := unix.Getresgid()
	if realUID != effectiveUID || realUID != savedUID {
		return fmt.Errorf(
			"taskctl launcher: real/effective/saved UIDs differ (%d/%d/%d)",
			realUID, effectiveUID, savedUID,
		)
	}
	if realGID != effectiveGID || realGID != savedGID {
		return fmt.Errorf(
			"taskctl launcher: real/effective/saved GIDs differ (%d/%d/%d)",
			realGID, effectiveGID, savedGID,
		)
	}
	if requireRoot && (realUID != 0 || realGID != 0) {
		return errors.New("taskctl launcher: installation requires real/effective/saved UID and GID 0")
	}
	header := unix.CapUserHeader{Version: unix.LINUX_CAPABILITY_VERSION_3}
	data := [2]unix.CapUserData{}
	if err := unix.Capget(&header, &data[0]); err != nil {
		return fmt.Errorf("taskctl launcher: inspect process capabilities: %w", err)
	}
	if realUID != 0 {
		for _, word := range data {
			if word.Effective != 0 || word.Permitted != 0 || word.Inheritable != 0 {
				return errors.New("taskctl launcher: non-root execution must have no process capabilities")
			}
		}
		return nil
	}
	for _, word := range data {
		if word.Inheritable != 0 {
			return errors.New("taskctl launcher: root execution must have no inheritable capabilities")
		}
	}
	return nil
}

func verifyTrustedLauncherInstallation(
	authorityRoot, launcherPath, executingPath string,
	expectedUID, expectedGID uint32,
) (resultErr error) {
	if !cleanAbsolutePath(authorityRoot) || !cleanAbsolutePath(launcherPath) ||
		!cleanAbsolutePath(executingPath) {
		return errors.New("trusted launcher paths must be clean and absolute")
	}
	relative, err := trustedRelativePath(authorityRoot, launcherPath)
	if err != nil {
		return err
	}
	if err := verifyTrustedLauncherPathComponents(authorityRoot, relative, expectedUID, expectedGID); err != nil {
		return err
	}
	canonicalLauncher, err := filepath.EvalSymlinks(launcherPath)
	if err != nil || canonicalLauncher != launcherPath {
		return errors.New("installed launcher path must contain no symbolic-link component")
	}

	descriptor, err := unix.Openat2(unix.AT_FDCWD, launcherPath, &unix.OpenHow{
		Flags: uint64(unix.O_RDONLY | unix.O_CLOEXEC | unix.O_NOFOLLOW | unix.O_NONBLOCK),
		Resolve: unix.RESOLVE_NO_MAGICLINKS |
			unix.RESOLVE_NO_SYMLINKS,
	})
	if err != nil {
		return fmt.Errorf("open installed launcher without symbolic links: %w", err)
	}
	launcher := os.NewFile(uintptr(descriptor), launcherPath)
	if launcher == nil {
		_ = unix.Close(descriptor)
		return errors.New("open installed launcher descriptor")
	}
	defer func() { resultErr = errors.Join(resultErr, launcher.Close()) }()
	identity, err := identifyExecutable(launcher)
	if err != nil {
		return err
	}
	if identity.mode&unix.S_IFMT != unix.S_IFREG ||
		identity.mode&0o7777 != installedLauncherMode {
		return fmt.Errorf("installed launcher mode must be exactly %#o", installedLauncherMode)
	}
	if identity.uid != expectedUID {
		return fmt.Errorf("installed launcher owner is %d, want %d", identity.uid, expectedUID)
	}
	if identity.gid != expectedGID {
		return fmt.Errorf("installed launcher group is %d, want %d", identity.gid, expectedGID)
	}
	if identity.links != 1 {
		return errors.New("installed launcher must have exactly one hard link")
	}
	if err := verifyNoFileCapabilities(launcher); err != nil {
		return fmt.Errorf("installed launcher capabilities: %w", err)
	}
	visible, err := os.Stat(launcherPath)
	if err != nil {
		return fmt.Errorf("inspect installed launcher pathname: %w", err)
	}
	pinned, err := launcher.Stat()
	if err != nil || !os.SameFile(visible, pinned) || visible.Mode() != pinned.Mode() {
		return errors.New("installed launcher pathname does not match its pinned descriptor")
	}
	executing, err := os.Stat(executingPath)
	if err != nil {
		return fmt.Errorf("inspect executing launcher image: %w", err)
	}
	if !os.SameFile(executing, pinned) {
		return errors.New("running image is not the fixed installed launcher inode")
	}
	if err := validateStaticELF(launcher); err != nil {
		return fmt.Errorf("installed launcher is not a static native image: %w", err)
	}
	if err := verifyTrustedLauncherPathComponents(authorityRoot, relative, expectedUID, expectedGID); err != nil {
		return fmt.Errorf("revalidate installed launcher path: %w", err)
	}
	visibleAfter, err := os.Stat(launcherPath)
	if err != nil || !os.SameFile(visibleAfter, pinned) || visibleAfter.Mode() != pinned.Mode() {
		return errors.New("installed launcher pathname changed during authentication")
	}
	return nil
}

func verifyTrustedLauncherPathComponents(
	authorityRoot, relative string,
	expectedUID, expectedGID uint32,
) error {
	components := []string{authorityRoot}
	current := authorityRoot
	for remaining := relative; remaining != "" && remaining != "."; {
		component, rest, found := cutPathComponent(remaining)
		current = filepath.Join(current, component)
		components = append(components, current)
		if !found {
			break
		}
		remaining = rest
	}
	for index, path := range components {
		var status unix.Stat_t
		if err := unix.Lstat(path, &status); err != nil {
			return fmt.Errorf("inspect trusted launcher path component %s: %w", path, err)
		}
		last := index == len(components)-1
		wantType := uint32(unix.S_IFDIR)
		if last {
			wantType = unix.S_IFREG
		}
		if status.Mode&unix.S_IFMT != wantType {
			return fmt.Errorf("trusted launcher path component has unsafe type: %s", path)
		}
		if status.Uid != expectedUID {
			return fmt.Errorf("trusted launcher path component %s owner is %d, want %d", path, status.Uid, expectedUID)
		}
		if status.Gid != expectedGID {
			return fmt.Errorf("trusted launcher path component %s group is %d, want %d", path, status.Gid, expectedGID)
		}
		if status.Mode&(unix.S_ISUID|unix.S_ISGID|unix.S_ISVTX) != 0 {
			return fmt.Errorf("trusted launcher path component has special mode bits: %s", path)
		}
		if status.Mode&0o022 != 0 {
			return fmt.Errorf("trusted launcher path component is writable by group or other: %s", path)
		}
		if last && status.Mode&0o777 != installedLauncherMode {
			return fmt.Errorf("installed launcher mode must be exactly %#o", installedLauncherMode)
		}
	}
	return nil
}

func verifyNoFileCapabilities(file *os.File) error {
	if file == nil {
		return errors.New("file descriptor is required")
	}
	_, err := unix.Fgetxattr(int(file.Fd()), "security.capability", nil)
	switch {
	case err == nil:
		return errors.New("security.capability xattr is present")
	case errors.Is(err, unix.ENODATA), errors.Is(err, unix.EOPNOTSUPP):
		return nil
	default:
		return fmt.Errorf("inspect security.capability xattr: %w", err)
	}
}

func cutPathComponent(path string) (component, remaining string, found bool) {
	for index, character := range path {
		if character == filepath.Separator {
			return path[:index], path[index+1:], true
		}
	}
	return path, "", false
}

func cleanAbsolutePath(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path
}

func verifyAfterExecution(
	repositoryRoot string,
	initialRoot directoryIdentity,
	executable *os.File,
	initialExecutable executableIdentity,
	expectedSHA256 string,
) (resultErr error) {
	pinnedIdentity, err := authenticateExecutable(executable, expectedSHA256)
	if err != nil {
		return fmt.Errorf("re-authenticate pinned executable: %w", err)
	}
	if pinnedIdentity != initialExecutable {
		return errors.New("pinned executable identity changed during execution")
	}

	root, currentRoot, err := openCanonicalRepositoryRoot(repositoryRoot)
	if err != nil {
		return fmt.Errorf("reopen repository working directory: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, root.Close()) }()
	if currentRoot != initialRoot {
		return errors.New("repository working directory identity changed during execution")
	}
	visible, err := openTaskctlExecutable(root)
	if err != nil {
		return fmt.Errorf("reopen visible executable: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, visible.Close()) }()
	visibleIdentity, err := authenticateExecutable(visible, expectedSHA256)
	if err != nil {
		return fmt.Errorf("re-authenticate visible executable: %w", err)
	}
	if visibleIdentity != initialExecutable {
		return errors.New("bin/taskctl pathname no longer names the executed inode")
	}
	return nil
}

func identifyExecutable(file *os.File) (executableIdentity, error) {
	var status unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &status); err != nil {
		return executableIdentity{}, fmt.Errorf("inspect executable descriptor: %w", err)
	}
	return executableIdentity{
		device:      uint64(status.Dev), //nolint:unconvert // Stat_t field widths vary across Linux architectures.
		inode:       status.Ino,
		mode:        status.Mode,
		links:       uint64(status.Nlink), //nolint:unconvert // Stat_t field widths vary across Linux architectures.
		uid:         status.Uid,
		gid:         status.Gid,
		size:        status.Size,
		modifiedSec: status.Mtim.Sec,
		modifiedNS:  status.Mtim.Nsec,
		changedSec:  status.Ctim.Sec,
		changedNS:   status.Ctim.Nsec,
	}, nil
}

func identifyDirectory(file *os.File) (directoryIdentity, error) {
	var status unix.Stat_t
	if err := unix.Fstat(int(file.Fd()), &status); err != nil {
		return directoryIdentity{}, fmt.Errorf("inspect repository working directory descriptor: %w", err)
	}
	if status.Mode&unix.S_IFMT != unix.S_IFDIR {
		return directoryIdentity{}, errors.New("repository working directory is not a directory")
	}
	return directoryIdentity{
		device: uint64(status.Dev), //nolint:unconvert // Stat_t field widths vary across Linux architectures.
		inode:  status.Ino,
		mode:   status.Mode,
		uid:    status.Uid,
		gid:    status.Gid,
	}, nil
}

func digestExecutable(file *os.File, size int64) (string, error) {
	digest := sha256.New()
	written, err := io.Copy(digest, io.NewSectionReader(file, 0, size))
	if err != nil {
		return "", fmt.Errorf("hash executable: %w", err)
	}
	if written != size {
		return "", errors.New("executable size changed while hashing")
	}
	return fmt.Sprintf("%x", digest.Sum(nil)), nil
}

func wrapOptionalError(label string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", label, err)
}
