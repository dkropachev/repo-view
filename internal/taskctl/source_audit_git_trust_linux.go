//go:build linux

package taskctl

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

// sourceAuditGitPlatformTrust records the complete root-to-executable path that
// made one authenticated Git invocation admissible. Every component is owned
// by root and is not group/world writable, so an unprivileged invoking user
// cannot alter either the executable inode or the pathname used to reach it.
// Git may be dynamically linked: its child receives a closed environment, so
// ambient loader controls cannot redirect it. The kernel, root administrator,
// ELF interpreter, loader configuration, and root-managed shared libraries are
// the explicit operating-system trust base rather than bytes authenticated by
// the source-audit Git digest.
type sourceAuditGitPlatformTrust struct {
	components []sourceAuditGitTrustedPathIdentity
}

type sourceAuditGitTrustedPathIdentity struct {
	path   string
	device uint64
	inode  uint64
	mode   uint32
	links  uint64
	uid    uint32
	gid    uint32
}

func captureSourceAuditGitPlatformTrust(
	path string,
	executable *os.File,
) (sourceAuditGitPlatformTrust, error) {
	paths, err := sourceAuditGitTrustedPathComponents(path)
	if err != nil {
		return sourceAuditGitPlatformTrust{}, err
	}
	components := make([]sourceAuditGitTrustedPathIdentity, 0, len(paths))
	for index, componentPath := range paths {
		identity, err := inspectSourceAuditGitTrustedPath(
			componentPath,
			index == len(paths)-1,
		)
		if err != nil {
			return sourceAuditGitPlatformTrust{}, err
		}
		components = append(components, identity)
	}
	trust := sourceAuditGitPlatformTrust{components: components}
	if err := trust.validate(path, executable); err != nil {
		return sourceAuditGitPlatformTrust{}, err
	}
	return trust, nil
}

func (trust sourceAuditGitPlatformTrust) validate(path string, executable *os.File) error {
	if len(trust.components) == 0 || executable == nil {
		return errors.New("authenticated source-audit Git installation trust is incomplete")
	}
	paths, err := sourceAuditGitTrustedPathComponents(path)
	if err != nil {
		return err
	}
	if len(paths) != len(trust.components) {
		return errors.New("authenticated source-audit Git installation path changed")
	}
	for index, componentPath := range paths {
		if trust.components[index].path != componentPath {
			return errors.New("authenticated source-audit Git installation path changed")
		}
		current, err := inspectSourceAuditGitTrustedPath(
			componentPath,
			index == len(paths)-1,
		)
		if err != nil {
			return err
		}
		if current != trust.components[index] {
			return fmt.Errorf(
				"authenticated source-audit Git installation component %q changed identity",
				componentPath,
			)
		}
	}
	opened, err := identifySourceAuditGitTrustedDescriptor(executable)
	if err != nil {
		return err
	}
	if opened != trust.components[len(trust.components)-1] {
		return errors.New("authenticated source-audit Git descriptor changed from its trusted installation identity")
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return fmt.Errorf("resolve authenticated source-audit Git installation after inspection: %w", err)
	}
	if canonical != path {
		return errors.New("authenticated source-audit Git installation path changed or traverses a symlink")
	}
	return nil
}

func sourceAuditGitTrustedPathComponents(path string) ([]string, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, errors.New("authenticated source-audit Git installation path must be canonical and absolute")
	}
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, fmt.Errorf("resolve authenticated source-audit Git installation: %w", err)
	}
	if canonical != path {
		return nil, errors.New("authenticated source-audit Git installation path must contain no symbolic-link component")
	}
	components := []string{string(filepath.Separator)}
	current := string(filepath.Separator)
	for _, name := range strings.Split(strings.TrimPrefix(path, string(filepath.Separator)), string(filepath.Separator)) {
		if name == "" {
			return nil, errors.New("authenticated source-audit Git installation path is malformed")
		}
		current = filepath.Join(current, name)
		components = append(components, current)
	}
	return components, nil
}

func inspectSourceAuditGitTrustedPath(
	path string,
	executable bool,
) (sourceAuditGitTrustedPathIdentity, error) {
	var status unix.Stat_t
	if err := unix.Lstat(path, &status); err != nil {
		return sourceAuditGitTrustedPathIdentity{}, fmt.Errorf(
			"inspect authenticated source-audit Git installation component %q: %w",
			path,
			err,
		)
	}
	identity := sourceAuditGitIdentityFromStat(path, status)
	if status.Uid != 0 {
		return sourceAuditGitTrustedPathIdentity{}, fmt.Errorf(
			"authenticated source-audit Git installation component %q must be root-owned",
			path,
		)
	}
	if status.Mode&0o022 != 0 {
		return sourceAuditGitTrustedPathIdentity{}, fmt.Errorf(
			"authenticated source-audit Git installation component %q must not be group/world writable",
			path,
		)
	}
	if executable {
		if status.Mode&(unix.S_ISUID|unix.S_ISGID|unix.S_ISVTX) != 0 {
			return sourceAuditGitTrustedPathIdentity{}, errors.New(
				"authenticated source-audit Git installation executable must not have special mode bits",
			)
		}
		if status.Mode&unix.S_IFMT != unix.S_IFREG || status.Mode&0o111 == 0 {
			return sourceAuditGitTrustedPathIdentity{}, errors.New(
				"authenticated source-audit Git installation executable must be an executable regular file",
			)
		}
		if status.Nlink != 1 {
			return sourceAuditGitTrustedPathIdentity{}, errors.New(
				"authenticated source-audit Git installation executable must have exactly one filesystem link",
			)
		}
		capability := make([]byte, 1)
		if _, err := unix.Getxattr(path, "security.capability", capability); err == nil {
			return sourceAuditGitTrustedPathIdentity{}, errors.New(
				"authenticated source-audit Git installation executable has file capabilities",
			)
		} else if !errors.Is(err, unix.ENODATA) && !errors.Is(err, unix.ENOTSUP) {
			return sourceAuditGitTrustedPathIdentity{}, fmt.Errorf(
				"inspect authenticated source-audit Git installation capabilities: %w",
				err,
			)
		}
	} else if status.Mode&unix.S_IFMT != unix.S_IFDIR {
		return sourceAuditGitTrustedPathIdentity{}, fmt.Errorf(
			"authenticated source-audit Git installation ancestor %q is not a directory",
			path,
		)
	}
	return identity, nil
}

func identifySourceAuditGitTrustedDescriptor(
	executable *os.File,
) (sourceAuditGitTrustedPathIdentity, error) {
	if executable == nil {
		return sourceAuditGitTrustedPathIdentity{}, errors.New(
			"authenticated source-audit Git executable descriptor is missing",
		)
	}
	var status unix.Stat_t
	if err := unix.Fstat(int(executable.Fd()), &status); err != nil {
		return sourceAuditGitTrustedPathIdentity{}, fmt.Errorf(
			"inspect authenticated source-audit Git executable descriptor: %w",
			err,
		)
	}
	return sourceAuditGitIdentityFromStat(executable.Name(), status), nil
}

func sourceAuditGitIdentityFromStat(
	path string,
	status unix.Stat_t,
) sourceAuditGitTrustedPathIdentity {
	return sourceAuditGitTrustedPathIdentity{
		path:   path,
		device: uint64(status.Dev), //nolint:unconvert // Stat_t field widths vary across Linux architectures.
		inode:  status.Ino,
		mode:   status.Mode,
		links:  uint64(status.Nlink), //nolint:unconvert // Stat_t field widths vary across Linux architectures.
		uid:    status.Uid,
		gid:    status.Gid,
	}
}
