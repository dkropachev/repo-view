//go:build linux

package main

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"golang.org/x/sys/unix"
)

const maximumPathTopologyMountInfoBytes = 8 << 20

type physicalPathIdentity struct {
	file           *os.File
	name           string
	path           string
	filesystemPath string
	mountID        uint64
	inode          uint64
	deviceMajor    uint32
	deviceMinor    uint32
	mode           uint16
	exists         bool
}

type topologyMountRecord struct {
	root        string
	mountPoint  string
	mountID     uint64
	deviceMajor uint32
	deviceMinor uint32
}

// requirePhysicallyDisjointPaths closes the gap left by lexical path checks:
// bind mounts can expose the same inode or an ancestor tree at unrelated path
// names. Existing endpoints are opened directly, while absent endpoints are
// represented by a securely opened existing parent plus their single leaf.
func requirePhysicallyDisjointPaths(named []namedPath) (resultErr error) {
	if len(named) < 2 {
		return nil
	}
	namespaceBefore, err := os.Stat("/proc/self/ns/mnt")
	if err != nil {
		return errors.New("inspect mount namespace for physical path separation")
	}
	rootDescriptor, err := unix.Open(
		"/",
		unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC,
		0,
	)
	if err != nil {
		return errors.New("open filesystem root for physical path separation")
	}
	root := os.NewFile(uintptr(rootDescriptor), "/")
	if root == nil {
		_ = unix.Close(rootDescriptor)
		return errors.New("open filesystem root for physical path separation")
	}
	defer func() {
		resultErr = errors.Join(resultErr, root.Close())
	}()

	identities := make([]physicalPathIdentity, 0, len(named))
	defer func() {
		for index := range identities {
			if identities[index].file != nil {
				resultErr = errors.Join(resultErr, identities[index].file.Close())
				identities[index].file = nil
			}
		}
	}()
	for _, candidate := range named {
		file, exists, openErr := openPhysicalPath(root, candidate.path)
		if openErr != nil {
			return fmt.Errorf("inspect %s physical path without symbolic links: %w", candidate.name, openErr)
		}
		identity, identityErr := physicalIdentityForFile(candidate, file, exists)
		if identityErr != nil {
			_ = file.Close()
			return fmt.Errorf("identify %s physical path: %w", candidate.name, identityErr)
		}
		identities = append(identities, identity)
	}

	mountInfo, err := readPathTopologyMountInfo()
	if err != nil {
		return err
	}
	mounts, err := parsePathTopologyMountInfo(mountInfo)
	if err != nil {
		return err
	}
	for index := range identities {
		if err := bindPhysicalFilesystemPath(&identities[index], mounts); err != nil {
			return fmt.Errorf("resolve %s physical mount topology: %w", identities[index].name, err)
		}
	}
	for left := range identities {
		for right := left + 1; right < len(identities); right++ {
			if physicalPathsOverlap(identities[left], identities[right]) {
				return fmt.Errorf(
					"%s and %s paths must be physically disjoint",
					identities[left].name,
					identities[right].name,
				)
			}
		}
	}

	for index := range identities {
		if err := reverifyPhysicalPath(root, identities[index]); err != nil {
			return fmt.Errorf("reverify %s physical path: %w", identities[index].name, err)
		}
	}
	mountInfoAfter, err := readPathTopologyMountInfo()
	if err != nil {
		return err
	}
	namespaceAfter, err := os.Stat("/proc/self/ns/mnt")
	if err != nil || !os.SameFile(namespaceBefore, namespaceAfter) ||
		!bytes.Equal(mountInfo, mountInfoAfter) {
		return errors.New("mount topology changed during physical path separation")
	}
	return nil
}

func openPhysicalPath(root *os.File, path string) (*os.File, bool, error) {
	if root == nil || !filepath.IsAbs(path) || filepath.Clean(path) != path || path == "/" {
		return nil, false, errors.New("physical path must be absolute, canonical, and non-root")
	}
	file, err := openPathTopologyAt(root, strings.TrimPrefix(path, "/"), false)
	if err == nil {
		return file, true, nil
	}
	if !errors.Is(err, unix.ENOENT) {
		return nil, false, err
	}
	parentPath := filepath.Dir(path)
	parentRelative := strings.TrimPrefix(parentPath, "/")
	if parentRelative == "" {
		parentRelative = "."
	}
	parent, err := openPathTopologyAt(root, parentRelative, true)
	if err != nil {
		return nil, false, fmt.Errorf("open existing parent: %w", err)
	}
	leaf, leafErr := openPathTopologyAt(parent, filepath.Base(path), false)
	if leafErr == nil {
		_ = leaf.Close()
		_ = parent.Close()
		return nil, false, errors.New("path appeared while its parent was opened")
	}
	if !errors.Is(leafErr, unix.ENOENT) {
		_ = parent.Close()
		return nil, false, fmt.Errorf("prove path leaf absent: %w", leafErr)
	}
	return parent, false, nil
}

func openPathTopologyAt(directory *os.File, relative string, requireDirectory bool) (*os.File, error) {
	if directory == nil || relative == "" || filepath.IsAbs(relative) {
		return nil, errors.New("invalid descriptor-relative physical path")
	}
	flags := uint64(unix.O_PATH | unix.O_CLOEXEC | unix.O_NOFOLLOW)
	if requireDirectory {
		flags |= unix.O_DIRECTORY
	}
	descriptor, err := unix.Openat2(int(directory.Fd()), relative, &unix.OpenHow{
		Flags: flags,
		Resolve: uint64(
			unix.RESOLVE_BENEATH |
				unix.RESOLVE_NO_SYMLINKS |
				unix.RESOLVE_NO_MAGICLINKS,
		),
	})
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(descriptor), relative)
	if file == nil {
		_ = unix.Close(descriptor)
		return nil, errors.New("open descriptor-relative physical path")
	}
	return file, nil
}

func physicalIdentityForFile(
	candidate namedPath,
	file *os.File,
	exists bool,
) (physicalPathIdentity, error) {
	if file == nil {
		return physicalPathIdentity{}, errors.New("physical path descriptor is unavailable")
	}
	var status unix.Statx_t
	const requiredMask = unix.STATX_TYPE | unix.STATX_INO | unix.STATX_MNT_ID
	if err := unix.Statx(
		int(file.Fd()),
		"",
		unix.AT_EMPTY_PATH|unix.AT_SYMLINK_NOFOLLOW,
		requiredMask,
		&status,
	); err != nil {
		return physicalPathIdentity{}, err
	}
	if status.Mask&requiredMask != requiredMask || status.Ino == 0 || status.Mnt_id == 0 {
		return physicalPathIdentity{}, errors.New("filesystem omits required inode or mount identity")
	}
	kind := status.Mode & unix.S_IFMT
	if exists && kind != unix.S_IFREG && kind != unix.S_IFDIR {
		return physicalPathIdentity{}, errors.New("existing path is not a regular file or directory")
	}
	if !exists && kind != unix.S_IFDIR {
		return physicalPathIdentity{}, errors.New("absent path parent is not a directory")
	}
	return physicalPathIdentity{
		file: file, name: candidate.name, path: candidate.path,
		mountID: status.Mnt_id, inode: status.Ino,
		deviceMajor: status.Dev_major, deviceMinor: status.Dev_minor,
		mode: status.Mode, exists: exists,
	}, nil
}

func readPathTopologyMountInfo() (raw []byte, resultErr error) {
	file, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return nil, errors.New("open mountinfo for physical path separation")
	}
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	raw, err = io.ReadAll(io.LimitReader(file, maximumPathTopologyMountInfoBytes+1))
	if err != nil || len(raw) == 0 || len(raw) > maximumPathTopologyMountInfoBytes {
		return nil, errors.Join(errors.New("read bounded mountinfo for physical path separation"), err)
	}
	return raw, nil
}

func parsePathTopologyMountInfo(raw []byte) (map[uint64]topologyMountRecord, error) {
	records := make(map[uint64]topologyMountRecord)
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		separator := -1
		for index, field := range fields {
			if field == "-" {
				separator = index
				break
			}
		}
		if separator < 6 || separator+3 >= len(fields) {
			return nil, errors.New("mountinfo contains a malformed record")
		}
		mountID, err := parseCanonicalTopologyUint(fields[0], 64)
		if err != nil || mountID == 0 {
			return nil, errors.New("mountinfo contains an invalid mount ID")
		}
		if _, duplicate := records[mountID]; duplicate {
			return nil, errors.New("mountinfo contains a duplicate mount ID")
		}
		deviceMajor, deviceMinor, err := parseTopologyDevice(fields[2])
		if err != nil {
			return nil, err
		}
		root, err := decodePathTopologyMountField(fields[3])
		if err != nil {
			return nil, err
		}
		mountPoint, err := decodePathTopologyMountField(fields[4])
		if err != nil || !validPhysicalMountPath(mountPoint) {
			return nil, errors.New("mountinfo contains an invalid mount point")
		}
		records[mountID] = topologyMountRecord{
			root: root, mountPoint: mountPoint, mountID: mountID,
			deviceMajor: deviceMajor, deviceMinor: deviceMinor,
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	if len(records) == 0 {
		return nil, errors.New("mountinfo contains no records")
	}
	return records, nil
}

func parseTopologyDevice(value string) (uint32, uint32, error) {
	majorRaw, minorRaw, ok := strings.Cut(value, ":")
	major, majorErr := parseCanonicalTopologyUint(majorRaw, 32)
	minor, minorErr := parseCanonicalTopologyUint(minorRaw, 32)
	if !ok || majorErr != nil || minorErr != nil {
		return 0, 0, errors.New("mountinfo contains an invalid device identity")
	}
	return uint32(major), uint32(minor), nil
}

func parseCanonicalTopologyUint(value string, bits int) (uint64, error) {
	parsed, err := strconv.ParseUint(value, 10, bits)
	if err != nil || strconv.FormatUint(parsed, 10) != value {
		return 0, errors.New("integer is not canonical decimal")
	}
	return parsed, nil
}

func decodePathTopologyMountField(value string) (string, error) {
	var decoded strings.Builder
	decoded.Grow(len(value))
	for index := 0; index < len(value); {
		if value[index] != '\\' {
			decoded.WriteByte(value[index])
			index++
			continue
		}
		if index+4 > len(value) {
			return "", errors.New("mountinfo path contains a truncated escape")
		}
		switch value[index : index+4] {
		case `\040`:
			decoded.WriteByte(' ')
		case `\011`:
			decoded.WriteByte('\t')
		case `\012`:
			decoded.WriteByte('\n')
		case `\134`:
			decoded.WriteByte('\\')
		default:
			return "", errors.New("mountinfo path contains an unsupported escape")
		}
		index += 4
	}
	result := decoded.String()
	if result == "" || strings.ContainsRune(result, '\x00') {
		return "", errors.New("mountinfo path is empty or contains NUL")
	}
	return result, nil
}

func validPhysicalMountPath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path &&
		!strings.ContainsRune(path, '\x00')
}

func bindPhysicalFilesystemPath(
	identity *physicalPathIdentity,
	mounts map[uint64]topologyMountRecord,
) error {
	if identity == nil {
		return errors.New("physical identity is unavailable")
	}
	record, exists := mounts[identity.mountID]
	if !exists || record.mountID != identity.mountID ||
		record.deviceMajor != identity.deviceMajor ||
		record.deviceMinor != identity.deviceMinor {
		return errors.New("opened path mount identity is absent or inconsistent")
	}
	if !validPhysicalMountPath(record.root) {
		return errors.New("opened path filesystem root is unsupported")
	}
	resolvedPath := identity.path
	if !identity.exists {
		resolvedPath = filepath.Dir(identity.path)
	}
	relative, err := filepath.Rel(record.mountPoint, resolvedPath)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return errors.New("opened path is outside its reported mount point")
	}
	physical := record.root
	if relative != "." {
		physical = filepath.Join(record.root, relative)
	}
	if !identity.exists {
		physical = filepath.Join(physical, filepath.Base(identity.path))
	}
	if !validPhysicalMountPath(physical) {
		return errors.New("opened path has an unsupported physical filesystem path")
	}
	identity.filesystemPath = physical
	return nil
}

func physicalPathsOverlap(left, right physicalPathIdentity) bool {
	if left.exists && right.exists &&
		left.deviceMajor == right.deviceMajor && left.deviceMinor == right.deviceMinor &&
		left.inode == right.inode {
		return true
	}
	if left.deviceMajor != right.deviceMajor || left.deviceMinor != right.deviceMinor {
		return false
	}
	leftDirectory := !left.exists || left.mode&unix.S_IFMT == unix.S_IFDIR
	rightDirectory := !right.exists || right.mode&unix.S_IFMT == unix.S_IFDIR
	return leftDirectory && physicalPathContains(left.filesystemPath, right.filesystemPath) ||
		rightDirectory && physicalPathContains(right.filesystemPath, left.filesystemPath)
}

func physicalPathContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && (relative == "." || relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func reverifyPhysicalPath(root *os.File, expected physicalPathIdentity) error {
	current, exists, err := openPhysicalPath(root, expected.path)
	if err != nil {
		return err
	}
	currentIdentity, identityErr := physicalIdentityForFile(
		namedPath{name: expected.name, path: expected.path},
		current,
		exists,
	)
	closeErr := current.Close()
	if identityErr != nil || closeErr != nil {
		return errors.Join(identityErr, closeErr)
	}
	if currentIdentity.exists != expected.exists ||
		currentIdentity.mountID != expected.mountID ||
		currentIdentity.deviceMajor != expected.deviceMajor ||
		currentIdentity.deviceMinor != expected.deviceMinor ||
		currentIdentity.inode != expected.inode ||
		currentIdentity.mode&unix.S_IFMT != expected.mode&unix.S_IFMT {
		return errors.New("physical path or parent changed during separation check")
	}
	return nil
}
