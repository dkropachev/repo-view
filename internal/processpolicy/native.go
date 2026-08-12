package processpolicy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
)

// ResolveNativeExecutable resolves an executable using the current PATH and
// verifies that both its visible and canonical names are non-script roles and
// that the target has a native executable header rather than a shebang.
func ResolveNativeExecutable(executable string) (string, error) {
	resolved, file, err := OpenNativeExecutable(executable)
	if err != nil {
		return "", err
	}
	if err := file.Close(); err != nil {
		return "", fmt.Errorf("process policy: close native executable: %w", err)
	}
	return resolved, nil
}

// OpenNativeExecutable resolves and opens the native executable that is
// visible under executable. The returned descriptor pins the validated inode;
// callers must close it.
func OpenNativeExecutable(executable string) (string, *os.File, error) {
	if err := ValidateExecutable(executable); err != nil {
		return "", nil, err
	}
	resolved, err := exec.LookPath(executable)
	if err != nil {
		return "", nil, fmt.Errorf("process policy: resolve native executable %q: %w", executable, err)
	}
	resolved, err = filepath.Abs(resolved)
	if err != nil {
		return "", nil, fmt.Errorf("process policy: make executable path absolute: %w", err)
	}
	canonical, err := filepath.EvalSymlinks(filepath.Clean(resolved))
	if err != nil {
		return "", nil, fmt.Errorf("process policy: canonicalize executable %q: %w", executable, err)
	}
	if err := ValidateExecutable(canonical); err != nil {
		return "", nil, fmt.Errorf("process policy: canonical executable is forbidden: %w", err)
	}
	before, err := os.Lstat(canonical)
	if err != nil {
		return "", nil, fmt.Errorf("process policy: inspect native executable: %w", err)
	}
	if before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() || before.Mode().Perm()&0o111 == 0 {
		return "", nil, errors.New("process policy: executable is not an executable regular file")
	}
	file, err := os.Open(canonical)
	if err != nil {
		return "", nil, fmt.Errorf("process policy: open native executable: %w", err)
	}
	valid := false
	defer func() {
		if !valid {
			_ = file.Close()
		}
	}()
	info, err := file.Stat()
	if err != nil {
		return "", nil, fmt.Errorf("process policy: inspect opened native executable: %w", err)
	}
	if !os.SameFile(before, info) || info.Mode() != before.Mode() || info.Size() != before.Size() {
		return "", nil, errors.New("process policy: executable changed while opening")
	}
	if err := ValidateNativeFile(file); err != nil {
		return "", nil, err
	}
	valid = true
	return canonical, file, nil
}

// NativeCommandContext constructs a command that executes the already-open,
// validated native image on Unix. This closes the validation-to-exec pathname
// replacement race. The caller must keep the returned descriptor open until
// the command has started and then close it.
func NativeCommandContext(
	ctx context.Context,
	executable string,
	arguments ...string,
) (*exec.Cmd, *os.File, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	canonical, file, err := OpenNativeExecutable(executable)
	if err != nil {
		return nil, nil, err
	}
	commandPath := canonical
	if runtime.GOOS != "windows" {
		commandPath = "/dev/fd/3"
		if runtime.GOOS == "linux" {
			commandPath = "/proc/self/fd/3"
		}
	}
	command := exec.CommandContext(ctx, commandPath, arguments...)
	command.Args[0] = executable
	if runtime.GOOS != "windows" {
		command.ExtraFiles = []*os.File{file}
	}
	return command, file, nil
}

// NativeCommand is NativeCommandContext with a background context.
func NativeCommand(executable string, arguments ...string) (*exec.Cmd, *os.File, error) {
	return NativeCommandContext(context.Background(), executable, arguments...)
}

// ValidateNativeFile verifies a pinned executable file without changing its
// caller-visible offset.
func ValidateNativeFile(file *os.File) error {
	if file == nil {
		return errors.New("process policy: native executable file is required")
	}
	position, err := file.Seek(0, io.SeekCurrent)
	if err != nil {
		return fmt.Errorf("process policy: read executable offset: %w", err)
	}
	defer func() { _, _ = file.Seek(position, io.SeekStart) }()
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("process policy: rewind native executable: %w", err)
	}
	header := make([]byte, 4)
	if _, err := io.ReadFull(file, header); err != nil {
		return fmt.Errorf("process policy: read native executable header: %w", err)
	}
	if !nativeExecutableHeader(header) {
		return errors.New("process policy: executable does not have a native binary header")
	}
	return nil
}

func nativeExecutableHeader(header []byte) bool {
	if len(header) < 4 {
		return false
	}
	if bytes.Equal(header, []byte{0x7f, 'E', 'L', 'F'}) ||
		bytes.Equal(header[:2], []byte{'M', 'Z'}) {
		return true
	}
	magic := [4]byte{header[0], header[1], header[2], header[3]}
	switch magic {
	case [4]byte{0xfe, 0xed, 0xfa, 0xce}, [4]byte{0xce, 0xfa, 0xed, 0xfe},
		[4]byte{0xfe, 0xed, 0xfa, 0xcf}, [4]byte{0xcf, 0xfa, 0xed, 0xfe},
		[4]byte{0xca, 0xfe, 0xba, 0xbe}, [4]byte{0xbe, 0xba, 0xfe, 0xca}:
		return true
	default:
		return false
	}
}
