//go:build linux

package taskctl

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

const maximumPublicationMountInfoBytes = 8 << 20

type publicationPhysicalIdentity struct {
	file           *os.File
	expected       os.FileInfo
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

type publicationMountRecord struct {
	root        string
	mountPoint  string
	mountID     uint64
	deviceMajor uint32
	deviceMinor uint32
}

// requirePhysicallyDisjointPublicationPaths closes the lexical-check gap made
// by bind mounts. Existing endpoints are opened without following symlinks;
// an absent output is represented by its pinned existing parent and leaf.
func requirePhysicallyDisjointPublicationPaths(
	named []publicationPhysicalPath,
) (resultErr error) {
	if len(named) < 2 {
		return nil
	}
	namespaceBefore, err := os.Stat("/proc/self/ns/mnt")
	if err != nil {
		return errors.New("inspect mount namespace for physical publication separation")
	}
	rootDescriptor, err := unix.Open("/", unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC, 0)
	if err != nil {
		return errors.New("open filesystem root for physical publication separation")
	}
	root := os.NewFile(uintptr(rootDescriptor), "/")
	if root == nil {
		_ = unix.Close(rootDescriptor)
		return errors.New("open filesystem root for physical publication separation")
	}
	defer func() { resultErr = errors.Join(resultErr, root.Close()) }()

	identities := make([]publicationPhysicalIdentity, 0, len(named))
	defer func() {
		for index := range identities {
			if identities[index].file != nil {
				resultErr = errors.Join(resultErr, identities[index].file.Close())
				identities[index].file = nil
			}
		}
	}()
	for _, candidate := range named {
		file, openErr := openPublicationPhysicalPath(root, candidate)
		if openErr != nil {
			return fmt.Errorf("inspect %s physical path without symbolic links: %w", candidate.name, openErr)
		}
		identity, identityErr := publicationPhysicalIdentityForFile(candidate, file)
		if identityErr != nil {
			_ = file.Close()
			return fmt.Errorf("identify %s physical path: %w", candidate.name, identityErr)
		}
		identities = append(identities, identity)
	}

	mountInfo, err := readPublicationMountInfo()
	if err != nil {
		return err
	}
	mounts, err := parsePublicationMountInfo(mountInfo)
	if err != nil {
		return err
	}
	for index := range identities {
		if err := bindPublicationFilesystemPath(&identities[index], mounts); err != nil {
			return fmt.Errorf("resolve %s physical mount topology: %w", identities[index].name, err)
		}
	}
	for left := range identities {
		for right := left + 1; right < len(identities); right++ {
			if publicationPhysicalPathsOverlapInMountNamespace(
				identities[left],
				identities[right],
				mounts,
			) {
				return fmt.Errorf(
					"%s and %s paths must be physically disjoint",
					identities[left].name,
					identities[right].name,
				)
			}
		}
	}
	for index := range identities {
		if err := reverifyPublicationPhysicalPath(root, identities[index]); err != nil {
			return fmt.Errorf("reverify %s physical path: %w", identities[index].name, err)
		}
	}
	mountInfoAfter, err := readPublicationMountInfo()
	if err != nil {
		return err
	}
	namespaceAfter, err := os.Stat("/proc/self/ns/mnt")
	if err != nil || !os.SameFile(namespaceBefore, namespaceAfter) ||
		!bytes.Equal(mountInfo, mountInfoAfter) {
		return errors.New("mount topology changed during physical publication separation")
	}
	return nil
}

func openPublicationPhysicalPath(
	root *os.File,
	candidate publicationPhysicalPath,
) (*os.File, error) {
	path := candidate.path
	if root == nil || candidate.expected == nil || !filepath.IsAbs(path) ||
		filepath.Clean(path) != path || path == "/" {
		return nil, errors.New("physical path and expected identity must be complete")
	}
	if candidate.exists {
		file, err := openPublicationTopologyAt(root, strings.TrimPrefix(path, "/"), false)
		if err != nil {
			return nil, err
		}
		return file, nil
	}
	parentPath := filepath.Dir(path)
	parentRelative := strings.TrimPrefix(parentPath, "/")
	if parentRelative == "" {
		parentRelative = "."
	}
	parent, err := openPublicationTopologyAt(root, parentRelative, true)
	if err != nil {
		return nil, fmt.Errorf("open existing parent: %w", err)
	}
	leaf, leafErr := openPublicationTopologyAt(parent, filepath.Base(path), false)
	if leafErr == nil {
		_ = leaf.Close()
		_ = parent.Close()
		return nil, errors.New("path appeared while its parent was opened")
	}
	if !errors.Is(leafErr, unix.ENOENT) {
		_ = parent.Close()
		return nil, fmt.Errorf("prove path leaf absent: %w", leafErr)
	}
	return parent, nil
}

func openPublicationTopologyAt(
	directory *os.File,
	relative string,
	requireDirectory bool,
) (*os.File, error) {
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

func publicationPhysicalIdentityForFile(
	candidate publicationPhysicalPath,
	file *os.File,
) (publicationPhysicalIdentity, error) {
	if file == nil || candidate.expected == nil {
		return publicationPhysicalIdentity{}, errors.New("physical path descriptor or expected identity is unavailable")
	}
	openedInfo, err := file.Stat()
	if err != nil {
		return publicationPhysicalIdentity{}, err
	}
	if !os.SameFile(candidate.expected, openedInfo) ||
		candidate.expected.Mode() != openedInfo.Mode() {
		return publicationPhysicalIdentity{}, errors.New("opened physical path differs from its inspected identity")
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
		return publicationPhysicalIdentity{}, err
	}
	if status.Mask&requiredMask != requiredMask || status.Ino == 0 || status.Mnt_id == 0 {
		return publicationPhysicalIdentity{}, errors.New("filesystem omits required inode or mount identity")
	}
	kind := status.Mode & unix.S_IFMT
	if candidate.exists && kind != unix.S_IFREG && kind != unix.S_IFDIR {
		return publicationPhysicalIdentity{}, errors.New("existing path is not a regular file or directory")
	}
	if !candidate.exists && kind != unix.S_IFDIR {
		return publicationPhysicalIdentity{}, errors.New("absent path parent is not a directory")
	}
	return publicationPhysicalIdentity{
		file: file, expected: candidate.expected,
		name: candidate.name, path: candidate.path,
		mountID: status.Mnt_id, inode: status.Ino,
		deviceMajor: status.Dev_major, deviceMinor: status.Dev_minor,
		mode: status.Mode, exists: candidate.exists,
	}, nil
}

func readPublicationMountInfo() (raw []byte, resultErr error) {
	file, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return nil, errors.New("open mountinfo for physical publication separation")
	}
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	raw, err = io.ReadAll(io.LimitReader(file, maximumPublicationMountInfoBytes+1))
	if err != nil || len(raw) == 0 || len(raw) > maximumPublicationMountInfoBytes {
		return nil, errors.Join(errors.New("read bounded mountinfo for physical publication separation"), err)
	}
	return raw, nil
}

func parsePublicationMountInfo(raw []byte) (map[uint64]publicationMountRecord, error) {
	records := make(map[uint64]publicationMountRecord)
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
		mountID, err := parseCanonicalPublicationUint(fields[0], 64)
		if err != nil || mountID == 0 {
			return nil, errors.New("mountinfo contains an invalid mount ID")
		}
		if _, duplicate := records[mountID]; duplicate {
			return nil, errors.New("mountinfo contains a duplicate mount ID")
		}
		deviceMajor, deviceMinor, err := parsePublicationDevice(fields[2])
		if err != nil {
			return nil, err
		}
		root, err := decodePublicationMountField(fields[3])
		if err != nil {
			return nil, err
		}
		mountPoint, err := decodePublicationMountField(fields[4])
		if err != nil || !validPublicationMountPath(mountPoint) {
			return nil, errors.New("mountinfo contains an invalid mount point")
		}
		records[mountID] = publicationMountRecord{
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

func parsePublicationDevice(value string) (uint32, uint32, error) {
	majorRaw, minorRaw, ok := strings.Cut(value, ":")
	major, majorErr := parseCanonicalPublicationUint(majorRaw, 32)
	minor, minorErr := parseCanonicalPublicationUint(minorRaw, 32)
	if !ok || majorErr != nil || minorErr != nil {
		return 0, 0, errors.New("mountinfo contains an invalid device identity")
	}
	return uint32(major), uint32(minor), nil
}

func parseCanonicalPublicationUint(value string, bits int) (uint64, error) {
	parsed, err := strconv.ParseUint(value, 10, bits)
	if err != nil || strconv.FormatUint(parsed, 10) != value {
		return 0, errors.New("integer is not canonical decimal")
	}
	return parsed, nil
}

func decodePublicationMountField(value string) (string, error) {
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

func validPublicationMountPath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path &&
		!strings.ContainsRune(path, '\x00')
}

func bindPublicationFilesystemPath(
	identity *publicationPhysicalIdentity,
	mounts map[uint64]publicationMountRecord,
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
	if !validPublicationMountPath(record.root) {
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
	if !validPublicationMountPath(physical) {
		return errors.New("opened path has an unsupported physical filesystem path")
	}
	identity.filesystemPath = physical
	return nil
}

func publicationPhysicalPathsOverlap(
	left, right publicationPhysicalIdentity,
) bool {
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
	return leftDirectory && publicationPhysicalPathContains(left.filesystemPath, right.filesystemPath) ||
		rightDirectory && publicationPhysicalPathContains(right.filesystemPath, left.filesystemPath)
}

// publicationPhysicalPathsOverlapInMountNamespace extends endpoint identity
// comparison to every filesystem subtree mounted below an existing directory.
// Comparing only the directory endpoint misses a nested mount: an unrelated
// namespace path can bind the mounted filesystem and place the output inside
// the protected directory subtree without sharing the directory's device.
//
// Mount points are matched conservatively by their stable namespace paths. A
// stacked but hidden mount may therefore cause rejection, which is preferable
// to publishing through topology that cannot be proved disjoint.
func publicationPhysicalPathsOverlapInMountNamespace(
	left, right publicationPhysicalIdentity,
	mounts map[uint64]publicationMountRecord,
) bool {
	return publicationPhysicalPathsOverlap(left, right) ||
		publicationMountedDirectoryContains(left, right, mounts) ||
		publicationMountedDirectoryContains(right, left, mounts)
}

func publicationMountedDirectoryContains(
	directory, candidate publicationPhysicalIdentity,
	mounts map[uint64]publicationMountRecord,
) bool {
	if !directory.exists || directory.mode&unix.S_IFMT != unix.S_IFDIR {
		return false
	}
	for _, mount := range mounts {
		if mount.deviceMajor != candidate.deviceMajor ||
			mount.deviceMinor != candidate.deviceMinor ||
			!validPublicationMountPath(mount.root) ||
			!publicationPhysicalPathContains(directory.path, mount.mountPoint) {
			continue
		}
		candidateDirectory := candidate.exists && candidate.mode&unix.S_IFMT == unix.S_IFDIR
		if publicationPhysicalPathContains(mount.root, candidate.filesystemPath) ||
			candidateDirectory && publicationPhysicalPathContains(candidate.filesystemPath, mount.root) {
			return true
		}
	}
	return false
}

func publicationPhysicalPathContains(parent, child string) bool {
	relative, err := filepath.Rel(parent, child)
	return err == nil && (relative == "." || relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator)))
}

func reverifyPublicationPhysicalPath(
	root *os.File,
	expected publicationPhysicalIdentity,
) error {
	candidate := publicationPhysicalPath{
		name: expected.name, path: expected.path,
		expected: expected.expected, exists: expected.exists,
	}
	current, err := openPublicationPhysicalPath(root, candidate)
	if err != nil {
		return err
	}
	currentIdentity, identityErr := publicationPhysicalIdentityForFile(
		candidate,
		current,
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
