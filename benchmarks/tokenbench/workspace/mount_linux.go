//go:build linux

package workspace

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"syscall"
	"unicode/utf8"

	"golang.org/x/sys/unix"
)

const maximumMountInfoBytes = 8 << 20

type namespaceIdentity struct {
	device uint64
	inode  uint64
}

type mountRecord struct {
	majorMinor   string
	root         string
	point        string
	filesystem   string
	source       string
	options      []string
	optional     []string
	superOptions []string
	namespace    namespaceIdentity
	id           uint64
	parentID     uint64
}

func currentMountNamespaceIdentity() (namespaceIdentity, error) {
	info, err := os.Stat("/proc/self/ns/mnt")
	if err != nil {
		return namespaceIdentity{}, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Dev == 0 || stat.Ino == 0 {
		return namespaceIdentity{}, errors.New("mount namespace lacks Linux inode identity")
	}
	return namespaceIdentity{device: stat.Dev, inode: stat.Ino}, nil
}

func readMountInfo() ([]byte, error) {
	file, err := os.Open("/proc/self/mountinfo")
	if err != nil {
		return nil, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maximumMountInfoBytes+1))
	if err != nil {
		return nil, err
	}
	if len(raw) > maximumMountInfoBytes {
		return nil, errors.New("mountinfo exceeds its workspace audit limit")
	}
	return raw, nil
}

func parseMountRecord(line string, namespace namespaceIdentity) (mountRecord, error) {
	fields := strings.Fields(line)
	separator := -1
	for index, field := range fields {
		if field == "-" {
			separator = index
			break
		}
	}
	if separator < 6 || separator+3 >= len(fields) {
		return mountRecord{}, errors.New("mountinfo contains a malformed record")
	}
	id, err := strconv.ParseUint(fields[0], 10, 64)
	if err != nil || id == 0 {
		return mountRecord{}, errors.New("mountinfo contains an invalid mount ID")
	}
	parentID, err := strconv.ParseUint(fields[1], 10, 64)
	if err != nil || parentID == 0 {
		return mountRecord{}, errors.New("mountinfo contains an invalid parent mount ID")
	}
	root, err := decodeMountInfoPath(fields[3])
	if err != nil {
		return mountRecord{}, err
	}
	point, err := decodeMountInfoPath(fields[4])
	if err != nil {
		return mountRecord{}, err
	}
	source, err := decodeMountInfoPath(fields[separator+2])
	if err != nil {
		return mountRecord{}, err
	}
	options, err := canonicalMountFields(fields[5])
	if err != nil {
		return mountRecord{}, err
	}
	optional := append([]string(nil), fields[6:separator]...)
	if err := validateMountTokens(optional); err != nil {
		return mountRecord{}, err
	}
	superOptions, err := canonicalMountFields(fields[separator+3])
	if err != nil {
		return mountRecord{}, err
	}
	if !filepath.IsAbs(root) || !filepath.IsAbs(point) ||
		filepath.Clean(root) != root || filepath.Clean(point) != point {
		return mountRecord{}, errors.New("mountinfo contains a noncanonical root or mount point")
	}
	if fields[2] == "" || fields[separator+1] == "" || source == "" {
		return mountRecord{}, errors.New("mountinfo contains an empty filesystem identity")
	}
	return mountRecord{
		namespace: namespace, id: id, parentID: parentID,
		majorMinor: fields[2], root: root, point: point,
		options: options, optional: optional,
		filesystem: fields[separator+1], source: source, superOptions: superOptions,
	}, nil
}

func canonicalMountFields(value string) ([]string, error) {
	fields := strings.Split(value, ",")
	if len(fields) == 0 {
		return nil, errors.New("mountinfo contains an empty option list")
	}
	for _, field := range fields {
		if field == "" {
			return nil, errors.New("mountinfo contains an empty option")
		}
	}
	if err := validateMountTokens(fields); err != nil {
		return nil, err
	}
	return fields, nil
}

func validateMountTokens(fields []string) error {
	seen := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		if len(field) > maximumPathBytes || !utf8.ValidString(field) ||
			strings.ContainsAny(field, "\x00\r\n") {
			return errors.New("mountinfo contains an invalid token")
		}
		if _, exists := seen[field]; exists {
			return errors.New("mountinfo contains a duplicate token")
		}
		seen[field] = struct{}{}
	}
	return nil
}

func decodeMountInfoPath(value string) (string, error) {
	replacer := strings.NewReplacer(
		`\040`, " ",
		`\011`, "\t",
		`\012`, "\n",
		`\134`, `\`,
	)
	decoded := replacer.Replace(value)
	if strings.Contains(decoded, `\0`) || !utf8.ValidString(decoded) ||
		strings.ContainsRune(decoded, '\x00') {
		return "", errors.New("mountinfo path contains an unsupported escape")
	}
	return decoded, nil
}

func allMountRecords() ([]mountRecord, error) {
	raw, err := readMountInfo()
	if err != nil {
		return nil, err
	}
	namespace, err := currentMountNamespaceIdentity()
	if err != nil {
		return nil, err
	}
	records := make([]mountRecord, 0, 256)
	scanner := bufio.NewScanner(strings.NewReader(string(raw)))
	scanner.Buffer(make([]byte, 64<<10), maximumMountInfoBytes)
	for scanner.Scan() {
		record, err := parseMountRecord(scanner.Text(), namespace)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return records, nil
}

func mountRecordAt(path string) (mountRecord, error) {
	records, err := allMountRecords()
	if err != nil {
		return mountRecord{}, err
	}
	var found mountRecord
	count := 0
	for _, record := range records {
		if record.point == path {
			found = record
			count++
		}
	}
	if count != 1 {
		return mountRecord{}, fmt.Errorf("workspace path has %d exact mountinfo records, want 1", count)
	}
	return found, nil
}

func mountRecordByID(id uint64) (mountRecord, error) {
	records, err := allMountRecords()
	if err != nil {
		return mountRecord{}, err
	}
	var found mountRecord
	count := 0
	for _, record := range records {
		if record.id == id {
			found = record
			count++
		}
	}
	if count != 1 {
		return mountRecord{}, fmt.Errorf("mount ID %d has %d records, want 1", id, count)
	}
	return found, nil
}

func mountRecordForDescriptor(file *os.File) (mountRecord, error) {
	if file == nil || file.Fd() == ^uintptr(0) {
		return mountRecord{}, errors.New("mount descriptor is absent")
	}
	var stat unix.Statx_t
	if err := unix.Statx(
		int(file.Fd()),
		"",
		unix.AT_EMPTY_PATH|unix.AT_STATX_SYNC_AS_STAT,
		unix.STATX_MNT_ID,
		&stat,
	); err != nil {
		return mountRecord{}, fmt.Errorf("read mount descriptor identity: %w", err)
	}
	if stat.Mask&unix.STATX_MNT_ID == 0 || stat.Mnt_id == 0 {
		return mountRecord{}, errors.New("mount descriptor omitted its mount ID")
	}
	return mountRecordByID(stat.Mnt_id)
}

// retainedMountRecord resolves the live mount through its retained descriptor
// when one is still available and otherwise through the mount ID captured from
// that descriptor. A mountpoint or any of its ancestors may be renamed without
// changing this identity.
func retainedMountRecord(file *os.File, expected mountRecord) (mountRecord, error) {
	var (
		current mountRecord
		err     error
	)
	switch {
	case file != nil:
		current, err = mountRecordForDescriptor(file)
	case expected.id != 0:
		current, err = mountRecordByID(expected.id)
	default:
		return mountRecord{}, errors.New("retained mount identity is absent")
	}
	if err != nil {
		return mountRecord{}, err
	}
	if expected.id != 0 && !sameRetainedMountIdentity(current, expected) {
		return mountRecord{}, errors.New("retained mount identity changed")
	}
	return current, nil
}

func sameRetainedMountIdentity(left, right mountRecord) bool {
	// The mountpoint and parent mount describe the mount's current attachment,
	// not the mount object itself. All other fields are retained identity and
	// policy and must remain unchanged.
	left.point, right.point = "", ""
	left.parentID, right.parentID = 0, 0
	return reflect.DeepEqual(left, right)
}

func mountpointForUnmount(
	file *os.File,
	expectedInfo os.FileInfo,
	expected mountRecord,
) (mountRecord, error) {
	current, err := retainedMountRecord(file, expected)
	if err != nil {
		return mountRecord{}, err
	}
	path, err := openDirectoryNoSymlinks(current.point)
	if err != nil {
		return mountRecord{}, fmt.Errorf("open retained mountpoint %q: %w", current.point, err)
	}
	defer path.Close()
	pathRecord, err := mountRecordForDescriptor(path)
	if err != nil {
		return mountRecord{}, err
	}
	info, infoErr := path.Stat()
	if infoErr != nil || expectedInfo == nil || !os.SameFile(expectedInfo, info) {
		return mountRecord{}, errors.Join(
			errors.New("retained mountpoint inode changed"),
			infoErr,
		)
	}
	if !reflect.DeepEqual(pathRecord, current) {
		return mountRecord{}, errors.New("retained mountpoint attachment changed")
	}
	return current, nil
}

func rejectMountDescendants(rootID uint64) error {
	if rootID == 0 {
		return errors.New("retained mount identity is absent")
	}
	records, err := allMountRecords()
	if err != nil {
		return err
	}
	byID := make(map[uint64]mountRecord, len(records))
	for _, record := range records {
		byID[record.id] = record
	}
	for _, record := range records {
		if record.id == rootID {
			continue
		}
		seen := make(map[uint64]struct{}, 8)
		for parentID := record.parentID; parentID != 0; {
			if parentID == rootID {
				return fmt.Errorf("retained mount has descendant mount %q", record.point)
			}
			if _, duplicate := seen[parentID]; duplicate {
				break
			}
			seen[parentID] = struct{}{}
			parent, exists := byID[parentID]
			if !exists || parent.parentID == parentID {
				break
			}
			parentID = parent.parentID
		}
	}
	return nil
}

func containingPrivateMount(path string) (mountRecord, error) {
	records, err := allMountRecords()
	if err != nil {
		return mountRecord{}, err
	}
	var selected mountRecord
	for _, record := range records {
		if pathWithin(record.point, path) && len(record.point) > len(selected.point) {
			selected = record
		}
	}
	if selected.id == 0 {
		return mountRecord{}, errors.New("workspace root has no containing mount")
	}
	if mountIsSharedOrSlave(selected) {
		return mountRecord{}, errors.New("workspace containing mount is shared or slave")
	}
	return selected, nil
}

func pathWithin(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." && !filepath.IsAbs(relative) &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func mountIsSharedOrSlave(record mountRecord) bool {
	for _, field := range record.optional {
		if strings.HasPrefix(field, "shared:") ||
			strings.HasPrefix(field, "master:") ||
			strings.HasPrefix(field, "propagate_from:") {
			return true
		}
	}
	return false
}

func rejectDescendantMounts(root string) error {
	records, err := allMountRecords()
	if err != nil {
		return err
	}
	for _, record := range records {
		if record.point != root && pathWithin(root, record.point) {
			return fmt.Errorf("workspace contains descendant mount %q", record.point)
		}
	}
	return nil
}

func verifyArmMountTopology(root string, overlay mountRecord) error {
	records, err := allMountRecords()
	if err != nil {
		return err
	}
	foundOverlay := 0
	for _, record := range records {
		if record.point == root || !pathWithin(root, record.point) {
			continue
		}
		if record.id == overlay.id && reflect.DeepEqual(record, overlay) {
			foundOverlay++
			continue
		}
		return fmt.Errorf("workspace contains unexpected descendant mount %q", record.point)
	}
	if foundOverlay != 1 {
		return fmt.Errorf("workspace contains %d retained overlay mounts, want 1", foundOverlay)
	}
	return nil
}

func verifyMountRecord(path string, expectedInfo os.FileInfo, expected mountRecord) error {
	if expectedInfo == nil || expected.id == 0 {
		return errors.New("workspace retained mount identity is absent")
	}
	current, err := os.Lstat(path)
	if err != nil || current.Mode()&os.ModeSymlink != 0 || !current.IsDir() ||
		!os.SameFile(expectedInfo, current) {
		return errors.Join(errors.New("workspace mount pathname changed"), err)
	}
	record, err := mountRecordAt(path)
	if err != nil {
		return err
	}
	if !reflect.DeepEqual(record, expected) {
		return errors.New("workspace mount identity changed")
	}
	stat, ok := current.Sys().(*syscall.Stat_t)
	if !ok || stat.Ino == 0 ||
		fmt.Sprintf("%d:%d", unix.Major(stat.Dev), unix.Minor(stat.Dev)) != record.majorMinor {
		return errors.New("workspace mount inode identity is invalid")
	}
	return nil
}

func hasMountOption(fields []string, want string) bool {
	for _, field := range fields {
		if field == want {
			return true
		}
	}
	return false
}

func validateTmpfsRecord(record mountRecord, parent mountRecord) error {
	switch {
	case record.parentID != parent.id:
		return errors.New("workspace tmpfs parent mount is incorrect")
	case record.root != "/" || record.filesystem != "tmpfs":
		return errors.New("workspace root is not a fresh tmpfs mount")
	case mountIsSharedOrSlave(record):
		return errors.New("workspace tmpfs is shared or slave")
	case !hasMountOption(record.options, "rw") ||
		!hasMountOption(record.options, "nosuid") ||
		!hasMountOption(record.options, "nodev") ||
		!hasMountOption(record.options, "noexec") ||
		!hasMountOption(record.options, "noatime"):
		return errors.New("workspace tmpfs lacks required mount restrictions")
	default:
		return nil
	}
}

func validateOverlayRecord(
	record mountRecord,
	root mountRecord,
	lowerPath, upperPath, workPath string,
) error {
	switch {
	case record.parentID != root.id:
		return errors.New("workspace overlay is not beneath the bounded tmpfs")
	case record.root != "/" || record.filesystem != "overlay":
		return errors.New("workspace model root is not an OverlayFS mount")
	case mountIsSharedOrSlave(record):
		return errors.New("workspace overlay is shared or slave")
	case !hasMountOption(record.options, "rw") ||
		!hasMountOption(record.options, "nosuid") ||
		!hasMountOption(record.options, "nodev") ||
		!hasMountOption(record.options, "noatime") ||
		hasMountOption(record.options, "noexec"):
		return errors.New("workspace overlay has incorrect mount restrictions")
	case !hasMountOption(record.superOptions, "lowerdir="+lowerPath) ||
		!hasMountOption(record.superOptions, "upperdir="+upperPath) ||
		!hasMountOption(record.superOptions, "workdir="+workPath):
		return errors.New("workspace overlay does not use the retained directory inputs")
	case !hasMountOption(record.superOptions, "xino=off") ||
		hasMountOption(record.superOptions, "index=on") ||
		hasMountOption(record.superOptions, "nfs_export=on") ||
		hasMountOption(record.superOptions, "redirect_dir=on") ||
		hasMountOption(record.superOptions, "metacopy=on"):
		return errors.New("workspace overlay has incorrect superblock policy")
	default:
		return nil
	}
}

func (pair *PairAuthority) attachBoundedTmpfs(limits Limits) (resultErr error) {
	pageSize := int64(os.Getpagesize())
	effectiveBytes := limits.MaximumUpperBytes / pageSize * pageSize
	if effectiveBytes < pageSize {
		return errors.New("workspace upper byte limit is smaller than one page")
	}
	filesystemFD, err := unix.Fsopen("tmpfs", unix.FSOPEN_CLOEXEC)
	if err != nil {
		return fmt.Errorf("open detached workspace tmpfs context: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, unix.Close(filesystemFD)) }()
	options := [][2]string{
		{"size", strconv.FormatInt(effectiveBytes, 10)},
		{"nr_inodes", strconv.Itoa(workspaceTmpfsInodeLimit(limits))},
		{"mode", "0700"},
	}
	for _, option := range options {
		key, value := option[0], option[1]
		if err := unix.FsconfigSetString(filesystemFD, key, value); err != nil {
			return fmt.Errorf("configure workspace tmpfs %s: %w", key, err)
		}
	}
	if err := unix.FsconfigCreate(filesystemFD); err != nil {
		return fmt.Errorf("create detached workspace tmpfs: %w", err)
	}
	mountFD, err := unix.Fsmount(
		filesystemFD,
		unix.FSMOUNT_CLOEXEC,
		unix.MOUNT_ATTR_NOSUID|unix.MOUNT_ATTR_NODEV|unix.MOUNT_ATTR_NOEXEC|unix.MOUNT_ATTR_NOATIME,
	)
	if err != nil {
		return fmt.Errorf("instantiate detached workspace tmpfs: %w", err)
	}
	mountFile := os.NewFile(uintptr(mountFD), "detached workspace tmpfs")
	defer func() {
		if mountFile != nil {
			resultErr = errors.Join(resultErr, mountFile.Close())
		}
	}()
	pair.mountedInfo, err = mountFile.Stat()
	if err != nil {
		return fmt.Errorf("stat detached workspace tmpfs: %w", err)
	}
	if err := unix.MoveMount(
		mountFD,
		"",
		int(pair.underlyingRoot.Fd()),
		"",
		unix.MOVE_MOUNT_F_EMPTY_PATH|unix.MOVE_MOUNT_T_EMPTY_PATH,
	); err != nil {
		return fmt.Errorf("attach workspace tmpfs to pinned root: %w", err)
	}
	pair.rootMountFile = mountFile
	mountFile = nil
	pair.mounted = true
	pair.rootMount, err = mountRecordForDescriptor(pair.rootMountFile)
	if err != nil {
		return err
	}
	if err := validateTmpfsRecord(pair.rootMount, pair.parentMount); err != nil {
		return err
	}
	if err := verifyMountRecord(pair.rootPath, pair.mountedInfo, pair.rootMount); err != nil {
		return err
	}
	pair.mountedRoot, err = openDirectoryNoSymlinks(pair.rootPath)
	if err != nil {
		return fmt.Errorf("open mounted workspace tmpfs: %w", err)
	}
	descriptorInfo, err := pair.mountedRoot.Stat()
	if err != nil || !os.SameFile(pair.mountedInfo, descriptorInfo) {
		return errors.Join(errors.New("workspace tmpfs descriptor does not match its mount path"), err)
	}
	if err := verifyMountRecord(pair.rootPath, descriptorInfo, pair.rootMount); err != nil {
		return err
	}
	return verifyTmpfsBounds(pair.mountedRoot, limits)
}

func workspaceTmpfsInodeLimit(limits Limits) int {
	return limits.MaximumEntries + workspaceInfrastructureInodes
}

func verifyTmpfsBounds(root *os.File, limits Limits) error {
	if root == nil {
		return errors.New("workspace tmpfs descriptor is absent")
	}
	var stats unix.Statfs_t
	if err := unix.Fstatfs(int(root.Fd()), &stats); err != nil {
		return err
	}
	if stats.Type != unix.TMPFS_MAGIC || stats.Bsize <= 0 {
		return errors.New("workspace root is not a bounded tmpfs filesystem")
	}
	blocks := stats.Blocks
	blockSize := uint64(stats.Bsize)
	if blocks != 0 && blockSize > ^uint64(0)/blocks {
		return errors.New("workspace tmpfs byte capacity overflows")
	}
	if blocks*blockSize > uint64(limits.MaximumUpperBytes) ||
		stats.Files > uint64(workspaceTmpfsInodeLimit(limits)) {
		return errors.New("workspace tmpfs exceeds its committed capacity")
	}
	info, err := root.Stat()
	if err != nil {
		return err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) || info.Mode().Perm() != 0o700 {
		return errors.New("workspace tmpfs root ownership or mode is invalid")
	}
	return nil
}

func (arm *ArmAuthority) attachOverlay() (resultErr error) {
	filesystemFD, err := unix.Fsopen("overlay", unix.FSOPEN_CLOEXEC)
	if err != nil {
		return fmt.Errorf("open detached workspace overlay context: %w", err)
	}
	defer func() { resultErr = errors.Join(resultErr, unix.Close(filesystemFD)) }()
	lowerPath, err := retainedDescriptorPath(arm.lower)
	if err != nil {
		return err
	}
	upperPath, err := retainedDescriptorPath(arm.upper)
	if err != nil {
		return err
	}
	workPath, err := retainedDescriptorPath(arm.work)
	if err != nil {
		return err
	}
	if err := unix.FsconfigSetString(filesystemFD, "lowerdir", lowerPath); err != nil {
		return fmt.Errorf("bind workspace lower directory: %w", err)
	}
	if err := unix.FsconfigSetString(filesystemFD, "upperdir", upperPath); err != nil {
		return fmt.Errorf("bind workspace upper directory: %w", err)
	}
	if err := unix.FsconfigSetString(filesystemFD, "workdir", workPath); err != nil {
		return fmt.Errorf("bind workspace work directory: %w", err)
	}
	options := [][2]string{
		{"index", "off"},
		{"nfs_export", "off"},
		{"redirect_dir", "off"},
		{"metacopy", "off"},
		{"xino", "off"},
	}
	for _, option := range options {
		if err := unix.FsconfigSetString(filesystemFD, option[0], option[1]); err != nil {
			return fmt.Errorf("configure workspace overlay %s: %w", option[0], err)
		}
	}
	if err := unix.FsconfigCreate(filesystemFD); err != nil {
		return fmt.Errorf("create detached workspace overlay: %w", err)
	}
	mountFD, err := unix.Fsmount(
		filesystemFD,
		unix.FSMOUNT_CLOEXEC,
		unix.MOUNT_ATTR_NOSUID|unix.MOUNT_ATTR_NODEV|unix.MOUNT_ATTR_NOATIME,
	)
	if err != nil {
		return fmt.Errorf("instantiate detached workspace overlay: %w", err)
	}
	mountFile := os.NewFile(uintptr(mountFD), "detached workspace overlay")
	defer func() {
		if mountFile != nil {
			resultErr = errors.Join(resultErr, mountFile.Close())
		}
	}()
	arm.overlayInfo, err = mountFile.Stat()
	if err != nil {
		return fmt.Errorf("stat detached workspace overlay: %w", err)
	}
	if err := unix.MoveMount(
		mountFD,
		"",
		int(arm.target.Fd()),
		"",
		unix.MOVE_MOUNT_F_EMPTY_PATH|unix.MOVE_MOUNT_T_EMPTY_PATH,
	); err != nil {
		return fmt.Errorf("attach workspace overlay to pinned target: %w", err)
	}
	arm.overlayFile = mountFile
	mountFile = nil
	arm.mounted = true
	arm.overlayMount, err = mountRecordForDescriptor(arm.overlayFile)
	if err != nil {
		return err
	}
	if err := validateOverlayRecord(
		arm.overlayMount,
		arm.pair.rootMount,
		lowerPath,
		upperPath,
		workPath,
	); err != nil {
		return err
	}
	if err := verifyMountRecord(arm.paths.ModelRoot, arm.overlayInfo, arm.overlayMount); err != nil {
		return err
	}
	arm.overlayRoot, err = openDirectoryAt(arm.pair.mountedRoot, worktreeDirectory)
	if err != nil {
		return err
	}
	descriptorInfo, err := arm.overlayRoot.Stat()
	if err != nil {
		return err
	}
	if !os.SameFile(arm.overlayInfo, descriptorInfo) {
		return errors.New("workspace overlay descriptor does not match its mount path")
	}
	if err := verifyGitWhiteout(arm.upper, arm.overlayRoot); err != nil {
		return err
	}
	return nil
}

func retainedDescriptorPath(file *os.File) (string, error) {
	if file == nil {
		return "", errors.New("workspace mount input descriptor is absent")
	}
	descriptor := file.Fd()
	if descriptor == ^uintptr(0) {
		return "", errors.New("workspace mount input descriptor is closed")
	}
	return "/proc/self/fd/" + strconv.FormatUint(uint64(descriptor), 10), nil
}

func retainWorkspaceRoot(path string) (_ *os.File, _ os.FileInfo, resultErr error) {
	root, err := openDirectoryNoSymlinks(path)
	if err != nil {
		return nil, nil, fmt.Errorf("open borrowed workspace mountpoint: %w", err)
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, root.Close())
		}
	}()
	info, err := root.Stat()
	if err != nil {
		return nil, nil, err
	}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok || !info.IsDir() || info.Mode().Perm() != 0o700 ||
		stat.Uid != uint32(os.Geteuid()) {
		return nil, nil, errors.New("borrowed workspace mountpoint must be an owned mode-0700 directory")
	}
	if err := directoryIsEmpty(root); err != nil {
		return nil, nil, fmt.Errorf("borrowed workspace mountpoint must be empty: %w", err)
	}
	if err := verifyRetainedWorkspaceRoot(path, root, info); err != nil {
		return nil, nil, err
	}
	return root, info, nil
}

func verifyRetainedWorkspaceRoot(
	path string,
	expected *os.File,
	expectedInfo os.FileInfo,
) (resultErr error) {
	if expected == nil || expectedInfo == nil {
		return errors.New("borrowed workspace mountpoint identity is absent")
	}
	liveExpected, err := expected.Stat()
	if err != nil || !os.SameFile(expectedInfo, liveExpected) ||
		!liveExpected.IsDir() || liveExpected.Mode().Perm() != 0o700 {
		return errors.Join(errors.New("borrowed workspace mountpoint descriptor changed"), err)
	}
	current, err := openDirectoryNoSymlinks(path)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, current.Close()) }()
	currentInfo, err := current.Stat()
	if err != nil {
		return err
	}
	currentStat, ok := currentInfo.Sys().(*syscall.Stat_t)
	if !ok || !os.SameFile(expectedInfo, currentInfo) ||
		currentInfo.Mode() != expectedInfo.Mode() ||
		currentStat.Uid != uint32(os.Geteuid()) {
		return errors.New("borrowed workspace mountpoint pathname changed")
	}
	return nil
}

func verifyRetainedWorkspaceRootDescriptor(
	expected *os.File,
	expectedInfo os.FileInfo,
) error {
	if expected == nil || expectedInfo == nil {
		return errors.New("borrowed workspace mountpoint identity is absent")
	}
	current, err := expected.Stat()
	if err != nil || !os.SameFile(expectedInfo, current) || !current.IsDir() ||
		current.Mode().Perm() != 0o700 {
		return errors.Join(errors.New("borrowed workspace mountpoint descriptor changed"), err)
	}
	stat, ok := current.Sys().(*syscall.Stat_t)
	if !ok || stat.Uid != uint32(os.Geteuid()) {
		return errors.New("borrowed workspace mountpoint ownership changed")
	}
	return nil
}

func openDirectoryNoSymlinks(path string) (*os.File, error) {
	descriptor, err := unix.Openat2(unix.AT_FDCWD, path, &unix.OpenHow{
		Flags:   unix.O_RDONLY | unix.O_DIRECTORY | unix.O_CLOEXEC,
		Resolve: unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_MAGICLINKS,
	})
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(descriptor), path), nil
}

func openDirectoryAt(parent *os.File, name string) (*os.File, error) {
	if parent == nil {
		return nil, errors.New("directory parent descriptor is absent")
	}
	descriptor, err := unix.Openat(
		int(parent.Fd()),
		name,
		unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return nil, err
	}
	return os.NewFile(uintptr(descriptor), name), nil
}

type directoryClaim struct {
	name    string
	device  uint64
	inode   uint64
	removed bool
}

func (claim directoryClaim) valid() bool {
	return claim.name != "" && claim.device != 0 && claim.inode != 0
}

func (claim directoryClaim) created() bool {
	return claim.name != ""
}

func createDirectoryAt(parent *os.File, name string) (*os.File, directoryClaim, error) {
	if parent == nil {
		return nil, directoryClaim{}, errors.New("workspace root descriptor is absent")
	}
	if err := unix.Mkdirat(int(parent.Fd()), name, 0o700); err != nil {
		return nil, directoryClaim{}, err
	}
	claim := directoryClaim{name: name}
	var created unix.Stat_t
	if err := unix.Fstatat(
		int(parent.Fd()),
		name,
		&created,
		unix.AT_SYMLINK_NOFOLLOW,
	); err != nil {
		return nil, claim, fmt.Errorf("identify created workspace directory: %w", err)
	}
	claim.device, claim.inode = created.Dev, created.Ino
	if created.Mode&unix.S_IFMT != unix.S_IFDIR ||
		created.Uid != uint32(os.Geteuid()) || !claim.valid() {
		return nil, claim, errors.New("created workspace directory identity is invalid")
	}

	// mkdir(2) applies the process-wide umask. Pin the newly created inode with
	// O_PATH, then normalize that exact inode through its proc descriptor before
	// asking for a readable directory descriptor.
	pathFD, err := unix.Openat(
		int(parent.Fd()),
		name,
		unix.O_PATH|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return nil, claim, err
	}
	path := os.NewFile(uintptr(pathFD), "created workspace directory")
	defer path.Close()
	var pinned unix.Stat_t
	if err := unix.Fstat(pathFD, &pinned); err != nil ||
		pinned.Dev != claim.device || pinned.Ino != claim.inode {
		return nil, claim, errors.Join(
			errors.New("created workspace directory changed while pinning"),
			err,
		)
	}
	descriptorPath, err := retainedDescriptorPath(path)
	if err != nil {
		return nil, claim, err
	}
	if err := unix.Fchmodat(unix.AT_FDCWD, descriptorPath, 0o700, 0); err != nil {
		return nil, claim, fmt.Errorf("set workspace directory mode: %w", err)
	}
	directory, err := openDirectoryAt(parent, name)
	if err != nil {
		return nil, claim, err
	}
	info, err := directory.Stat()
	if err != nil || info.Mode().Perm() != 0o700 || !sameFileClaim(info, claim) {
		return directory, claim, errors.Join(errors.New("workspace directory mode or identity is invalid"), err)
	}
	if err := verifyDirectoryPathAt(parent, name, directory); err != nil {
		return directory, claim, fmt.Errorf("verify created workspace directory path: %w", err)
	}
	if err := parent.Sync(); err != nil {
		return directory, claim, err
	}
	if err := verifyDirectoryPathAt(parent, name, directory); err != nil {
		return directory, claim, fmt.Errorf("reverify created workspace directory after sync: %w", err)
	}
	return directory, claim, nil
}

func sameFileClaim(info os.FileInfo, claim directoryClaim) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && claim.valid() && stat.Dev == claim.device && stat.Ino == claim.inode
}

const inodeReserveName = ".tokenbench-inode-reserve"

func (arm *ArmAuthority) retainInodeBudget() error {
	if arm.capture == nil || arm.pair == nil {
		return errors.New("workspace inode reserve authority is absent")
	}
	wantFree := uint64(arm.pair.inputs.Limits.MaximumEntries)
	for {
		var stats unix.Statfs_t
		if err := unix.Fstatfs(int(arm.pair.mountedRoot.Fd()), &stats); err != nil {
			return err
		}
		switch {
		case stats.Ffree < wantFree:
			return errors.New("workspace infrastructure exceeds its inode reserve")
		case stats.Ffree == wantFree:
			return arm.capture.Sync()
		}
		reserveFD, err := unix.Openat(
			int(arm.capture.Fd()),
			inodeReserveName,
			unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW,
			0o600,
		)
		if err != nil {
			return fmt.Errorf("retain workspace infrastructure inode: %w", err)
		}
		reserve := os.NewFile(uintptr(reserveFD), "workspace infrastructure inode")
		arm.inodeReserve = append(arm.inodeReserve, reserve)
		if err := unix.Unlinkat(int(arm.capture.Fd()), inodeReserveName, 0); err != nil {
			return fmt.Errorf("unlink retained workspace infrastructure inode: %w", err)
		}
	}
}

func verifyDirectoryPathAt(
	parent *os.File,
	name string,
	expected *os.File,
) (resultErr error) {
	if parent == nil || expected == nil {
		return errors.New("workspace directory identity is absent")
	}
	expectedInfo, err := expected.Stat()
	if err != nil || !expectedInfo.IsDir() || expectedInfo.Mode().Perm() != 0o700 {
		return errors.Join(errors.New("workspace directory descriptor is invalid"), err)
	}
	current, err := openDirectoryAt(parent, name)
	if err != nil {
		return err
	}
	defer func() { resultErr = errors.Join(resultErr, current.Close()) }()
	currentInfo, err := current.Stat()
	if err != nil || !os.SameFile(expectedInfo, currentInfo) ||
		currentInfo.Mode().Perm() != 0o700 {
		return errors.Join(errors.New("workspace directory pathname changed"), err)
	}
	var pathStat unix.Stat_t
	if err := unix.Fstatat(
		int(parent.Fd()),
		name,
		&pathStat,
		unix.AT_SYMLINK_NOFOLLOW,
	); err != nil {
		return err
	}
	var openedStat unix.Stat_t
	if err := unix.Fstat(int(current.Fd()), &openedStat); err != nil {
		return err
	}
	if pathStat.Dev != openedStat.Dev || pathStat.Ino != openedStat.Ino ||
		pathStat.Mode&unix.S_IFMT != unix.S_IFDIR || openedStat.Mode&unix.S_IFMT != unix.S_IFDIR ||
		pathStat.Uid != uint32(os.Geteuid()) || openedStat.Uid != uint32(os.Geteuid()) {
		return errors.New("workspace directory changed after it was opened")
	}
	return nil
}

func directoryIsEmpty(directory *os.File) error {
	if directory == nil {
		return errors.New("directory descriptor is absent")
	}
	stream, err := openDirectoryAt(directory, ".")
	if err != nil {
		return err
	}
	names, readErr := stream.Readdirnames(1)
	closeErr := stream.Close()
	if errors.Is(readErr, io.EOF) && len(names) == 0 && closeErr == nil {
		return nil
	}
	if (readErr != nil && !errors.Is(readErr, io.EOF)) || closeErr != nil {
		return errors.Join(readErr, closeErr)
	}
	return errors.New("directory is not empty")
}

func createGitWhiteout(upper *os.File) error {
	if upper == nil {
		return errors.New("workspace upper directory is absent")
	}
	if err := unix.Mknodat(
		int(upper.Fd()),
		".git",
		unix.S_IFCHR|0o600,
		int(unix.Mkdev(0, 0)),
	); err != nil {
		return fmt.Errorf("create opaque workspace Git whiteout: %w", err)
	}
	return upper.Sync()
}

func verifyGitWhiteout(upper, merged *os.File) error {
	if upper == nil || merged == nil {
		return errors.New("workspace whiteout descriptors are absent")
	}
	var whiteout unix.Stat_t
	if err := unix.Fstatat(int(upper.Fd()), ".git", &whiteout, unix.AT_SYMLINK_NOFOLLOW); err != nil {
		return err
	}
	if whiteout.Mode&unix.S_IFMT != unix.S_IFCHR || unix.Major(whiteout.Rdev) != 0 ||
		unix.Minor(whiteout.Rdev) != 0 || whiteout.Nlink != 1 {
		return errors.New("workspace Git whiteout identity is invalid")
	}
	var hidden unix.Stat_t
	err := unix.Fstatat(int(merged.Fd()), ".git", &hidden, unix.AT_SYMLINK_NOFOLLOW)
	if !errors.Is(err, unix.ENOENT) {
		return errors.New("immutable Git metadata is visible in the writable workspace")
	}
	return nil
}
