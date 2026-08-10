//go:build linux

package snapshot

import (
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

const maximumMountInfoBytes = 8 << 20

func establishReadOnlySelfBind(path string, expected os.FileInfo) (MountIdentity, error) {
	if err := rejectDescendantMounts(path); err != nil {
		return MountIdentity{}, fmt.Errorf("preflight snapshot descendant mounts: %w", err)
	}
	beforeNamespace, err := currentMountNamespaceIdentity()
	if err != nil {
		return MountIdentity{}, err
	}
	beforeParent, err := containingPrivateMount(path)
	if err != nil {
		return MountIdentity{}, fmt.Errorf("preflight snapshot mount propagation: %w", err)
	}
	if err := unix.Mount(path, path, "", unix.MS_BIND, ""); err != nil {
		return MountIdentity{}, fmt.Errorf(
			"create snapshot self-bind mount (CAP_SYS_ADMIN is required): %w",
			err,
		)
	}
	mounted := true
	defer func() {
		if mounted {
			_ = unix.Unmount(path, 0)
		}
	}()
	flags := uintptr(unix.MS_BIND | unix.MS_REMOUNT | unix.MS_RDONLY |
		unix.MS_NOSUID | unix.MS_NODEV)
	if err := unix.Mount("", path, "", flags, ""); err != nil {
		return MountIdentity{}, fmt.Errorf("remount snapshot self-bind read-only: %w", err)
	}
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(expected, current) {
		return MountIdentity{}, errors.Join(
			errors.New("snapshot root inode changed across self-bind mount"),
			err,
		)
	}
	identity, err := readMountIdentity(path, current)
	if err != nil {
		return MountIdentity{}, err
	}
	if identity.MountNamespaceDevice != beforeNamespace.device ||
		identity.MountNamespaceInode != beforeNamespace.inode ||
		identity.ParentID != beforeParent.id ||
		identity.ParentMountPoint != beforeParent.point ||
		identity.ParentFilesystemRoot != beforeParent.root ||
		!reflect.DeepEqual(identity.ParentOptionalFields, beforeParent.optional) {
		return MountIdentity{}, errors.New("mount namespace or private parent changed during self-bind")
	}
	if err := verifyMountFlags(path); err != nil {
		return MountIdentity{}, err
	}
	mounted = false
	return identity, nil
}

func readMountIdentity(path string, expected os.FileInfo) (MountIdentity, error) {
	raw, err := readMountInfo()
	if err != nil {
		return MountIdentity{}, err
	}
	if err := rejectDescendantMountsFromMountInfo(raw, path); err != nil {
		return MountIdentity{}, err
	}
	var matches []MountIdentity
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		identity, ok, err := parseMountInfoLine(scanner.Text(), path, expected)
		if err != nil {
			return MountIdentity{}, err
		}
		if ok {
			matches = append(matches, identity)
		}
	}
	if err := scanner.Err(); err != nil {
		return MountIdentity{}, err
	}
	if len(matches) != 1 {
		return MountIdentity{}, fmt.Errorf(
			"snapshot root has %d exact mountinfo entries, want 1",
			len(matches),
		)
	}
	identity := matches[0]
	namespace, err := currentMountNamespaceIdentity()
	if err != nil {
		return MountIdentity{}, err
	}
	parent, err := mountByID(raw, identity.ParentID)
	if err != nil {
		return MountIdentity{}, fmt.Errorf("identify snapshot parent mount: %w", err)
	}
	if hasPropagationField(parent.optional) {
		return MountIdentity{}, errors.New("snapshot parent mount is shared or slave")
	}
	expectedRoot, err := bindFilesystemRoot(parent.root, parent.point, path)
	if err != nil {
		return MountIdentity{}, fmt.Errorf("derive snapshot self-bind root: %w", err)
	}
	identity.MountNamespaceDevice = namespace.device
	identity.MountNamespaceInode = namespace.inode
	identity.ParentMountPoint = parent.point
	identity.ParentFilesystemRoot = parent.root
	identity.ParentOptionalFields = parent.optional
	identity.SelfBind = identity.FilesystemRoot == expectedRoot
	identity.Commitment, err = mountCommitment(identity)
	if err != nil {
		return MountIdentity{}, err
	}
	if err := identity.validate(path); err != nil {
		return MountIdentity{}, err
	}
	return identity, nil
}

type namespaceIdentity struct {
	device uint64
	inode  uint64
}

type parentMountIdentity struct {
	point    string
	root     string
	optional []string
	id       uint64
}

func currentMountNamespaceIdentity() (namespaceIdentity, error) {
	info, err := os.Stat("/proc/self/ns/mnt")
	if err != nil {
		return namespaceIdentity{}, fmt.Errorf("stat mount namespace: %w", err)
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat == nil || stat.Dev == 0 || stat.Ino == 0 {
		return namespaceIdentity{}, errors.New("mount namespace lacks inode identity")
	}
	return namespaceIdentity{device: stat.Dev, inode: stat.Ino}, nil
}

func readMountInfo() ([]byte, error) {
	file, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return nil, fmt.Errorf("open mountinfo: %w", err)
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maximumMountInfoBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read mountinfo: %w", err)
	}
	if len(raw) > maximumMountInfoBytes {
		return nil, errors.New("mountinfo exceeds its audit limit")
	}
	return raw, nil
}

func containingPrivateMount(path string) (parentMountIdentity, error) {
	raw, err := readMountInfo()
	if err != nil {
		return parentMountIdentity{}, err
	}
	var selected parentMountIdentity
	found := false
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		candidate, err := parseParentMount(scanner.Text())
		if err != nil {
			return parentMountIdentity{}, err
		}
		if !pathWithin(candidate.point, path) {
			continue
		}
		if !found || len(candidate.point) >= len(selected.point) {
			selected, found = candidate, true
		}
	}
	if err := scanner.Err(); err != nil {
		return parentMountIdentity{}, err
	}
	if !found {
		return parentMountIdentity{}, errors.New("no containing mountinfo record")
	}
	if !validAbsolutePath(selected.root) {
		return parentMountIdentity{}, errors.New("containing mount filesystem root is not an absolute path")
	}
	if hasPropagationField(selected.optional) {
		return parentMountIdentity{}, errors.New("containing mount is shared or slave")
	}
	return selected, nil
}

func mountByID(raw []byte, want uint64) (parentMountIdentity, error) {
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	var result parentMountIdentity
	found := false
	for scanner.Scan() {
		candidate, err := parseParentMount(scanner.Text())
		if err != nil {
			return parentMountIdentity{}, err
		}
		if candidate.id == want {
			if found {
				return parentMountIdentity{}, errors.New("duplicate parent mount ID")
			}
			result, found = candidate, true
		}
	}
	if err := scanner.Err(); err != nil {
		return parentMountIdentity{}, err
	}
	if !found {
		return parentMountIdentity{}, errors.New("parent mount ID is absent")
	}
	return result, nil
}

func parseParentMount(line string) (parentMountIdentity, error) {
	fields := strings.Fields(line)
	separator := -1
	for index, field := range fields {
		if field == "-" {
			separator = index
			break
		}
	}
	if separator < 6 || separator+3 >= len(fields) {
		return parentMountIdentity{}, errors.New("mountinfo contains a malformed record")
	}
	id, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil || id == 0 {
		return parentMountIdentity{}, errors.New("mountinfo contains an invalid mount ID")
	}
	point, err := decodeMountInfoPath(fields[4])
	if err != nil || !validAbsolutePath(point) {
		return parentMountIdentity{}, errors.Join(errors.New("mountinfo contains an invalid point"), err)
	}
	root, err := decodeMountInfoPath(fields[3])
	// Some unrelated pseudo-filesystem records (notably nsfs) use opaque
	// non-path roots such as mnt:[4026531840]. Preserve those while scanning;
	// a selected containing/parent mount is separately required to be absolute.
	if err != nil || root == "" || len(root) > maximumPathBytes {
		return parentMountIdentity{}, errors.Join(errors.New("mountinfo contains an invalid root"), err)
	}
	optional := append([]string{}, fields[6:separator]...)
	sort.Strings(optional)
	return parentMountIdentity{id: id, point: point, root: root, optional: optional}, nil
}

func parseMountInfoLine(
	line, wantPath string,
	expected os.FileInfo,
) (MountIdentity, bool, error) {
	fields := strings.Fields(line)
	separator := -1
	for index, field := range fields {
		if field == "-" {
			separator = index
			break
		}
	}
	if separator < 6 || separator+3 >= len(fields) {
		return MountIdentity{}, false, errors.New("mountinfo contains a malformed record")
	}
	mountPoint, err := decodeMountInfoPath(fields[4])
	if err != nil {
		return MountIdentity{}, false, err
	}
	if mountPoint != wantPath {
		return MountIdentity{}, false, nil
	}
	filesystemRoot, err := decodeMountInfoPath(fields[3])
	if err != nil {
		return MountIdentity{}, false, err
	}
	sourceName, err := decodeMountInfoPath(fields[separator+2])
	if err != nil {
		return MountIdentity{}, false, err
	}
	mountID, mountErr := strconv.ParseUint(fields[0], 10, 64)
	parentID, parentErr := strconv.ParseUint(fields[1], 10, 64)
	if mountErr != nil || parentErr != nil || mountID == 0 || parentID == 0 {
		return MountIdentity{}, false, errors.New("mountinfo contains invalid mount IDs")
	}
	stat, ok := expected.Sys().(*syscall.Stat_t)
	if !ok || stat == nil {
		return MountIdentity{}, false, errors.New("snapshot root lacks Linux inode identity")
	}
	mountOptions := canonicalOptions(fields[5])
	optionalFields := append([]string{}, fields[6:separator]...)
	sort.Strings(optionalFields)
	superOptions := canonicalOptions(fields[separator+3])
	identity := MountIdentity{
		SchemaVersion:  MountSchemaVersion,
		MountID:        mountID,
		ParentID:       parentID,
		MajorMinor:     fields[2],
		FilesystemRoot: filesystemRoot,
		MountPoint:     mountPoint,
		MountOptions:   mountOptions,
		OptionalFields: optionalFields,
		FilesystemType: fields[separator+1],
		Source:         sourceName,
		SuperOptions:   superOptions,
		Device:         stat.Dev,
		Inode:          stat.Ino,
		ReadOnly:       containsOption(mountOptions, "ro") && !containsOption(mountOptions, "rw"),
		NoSUID:         containsOption(mountOptions, "nosuid"),
		NoDev:          containsOption(mountOptions, "nodev"),
		// SelfBind is derived only after the parent mount's own filesystem root
		// is known. A parent can itself expose a filesystem subtree.
		SelfBind: false,
	}
	return identity, true, nil
}

func rejectDescendantMounts(path string) error {
	raw, err := readMountInfo()
	if err != nil {
		return err
	}
	return rejectDescendantMountsFromMountInfo(raw, path)
}

func rejectDescendantMountsFromMountInfo(raw []byte, root string) error {
	if !validAbsolutePath(root) {
		return errors.New("descendant-mount root must be absolute and canonical")
	}
	scanner := bufio.NewScanner(bytes.NewReader(raw))
	scanner.Buffer(make([]byte, 4096), 1<<20)
	for scanner.Scan() {
		identity, err := parseParentMount(scanner.Text())
		if err != nil {
			return err
		}
		if identity.point != root && pathWithin(root, identity.point) {
			return fmt.Errorf(
				"path %q contains descendant mount %q",
				root,
				identity.point,
			)
		}
	}
	return scanner.Err()
}

func decodeMountInfoPath(value string) (string, error) {
	replacer := strings.NewReplacer(
		`\040`, " ",
		`\011`, "\t",
		`\012`, "\n",
		`\134`, `\`,
	)
	decoded := replacer.Replace(value)
	if strings.Contains(decoded, `\0`) || strings.ContainsRune(decoded, '\x00') {
		return "", errors.New("mountinfo path contains an unsupported escape")
	}
	return decoded, nil
}

func canonicalOptions(value string) []string {
	if value == "" {
		return []string{}
	}
	options := strings.Split(value, ",")
	sort.Strings(options)
	return options
}

func containsOption(options []string, want string) bool {
	index := sort.SearchStrings(options, want)
	return index < len(options) && options[index] == want
}

func verifyMountFlags(path string) error {
	var status unix.Statfs_t
	if err := unix.Statfs(path, &status); err != nil {
		return fmt.Errorf("stat snapshot mount: %w", err)
	}
	required := int64(unix.ST_RDONLY | unix.ST_NOSUID | unix.ST_NODEV)
	if status.Flags&required != required {
		return fmt.Errorf(
			"snapshot mount flags %#x omit required %#x",
			status.Flags,
			required,
		)
	}
	return nil
}

func verifyMountIdentity(path string, expectedInfo os.FileInfo, expected MountIdentity) error {
	current, err := os.Lstat(path)
	if err != nil || !os.SameFile(expectedInfo, current) {
		return errors.Join(errors.New("snapshot mountpoint inode changed"), err)
	}
	actual, err := readMountIdentity(path, current)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(actual, expected) {
		return errors.New("snapshot read-only mount identity changed")
	}
	return verifyMountFlags(path)
}
