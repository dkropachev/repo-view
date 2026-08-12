//go:build linux

package workspace

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"reflect"
	"strconv"
	"strings"

	"github.com/yapless/scopesifter/benchmarks/tokenbench/snapshot"
	"golang.org/x/sys/unix"
)

const (
	captureGitExecutablePath = "/proc/self/fd/3"
	captureGitObjectsPath    = "/proc/self/fd/4"
	captureGitDirectoryPath  = "/proc/self/fd/5"
	captureGitWorktreePath   = "/proc/self/fd/6"

	captureResultIndex = "result.index"
	captureReplayIndex = "replay.index"

	maximumCaptureGitErrorBytes = 1 << 20
	maximumHashArgumentBytes    = 64 << 10
	maximumHashArgumentCount    = 512
)

var errCaptureOutputLimit = errors.New("capture Git output exceeds its committed bound")

type captureGitRunner struct {
	executable     *os.File
	objects        *os.File
	directory      *os.File
	worktree       *os.File
	executableInfo os.FileInfo
	objectsInfo    os.FileInfo
	directoryInfo  os.FileInfo
	worktreeInfo   os.FileInfo
	displayPath    string
	baseRevision   string
	objectIDLength int
}

type captureIndexEntry struct {
	path     string
	objectID string
	mode     uint32
}

func (arm *ArmAuthority) capturePatchLocked(
	ctx context.Context,
	initialDigest string,
	result []worktreeEntry,
) (outcome Outcome, resultErr error) {
	if err := directoryIsEmpty(arm.capture); err != nil {
		return Outcome{}, fmt.Errorf("verify capture scratch is private and empty: %w", err)
	}
	ready, err := arm.captureScratchAvailable(result)
	if err != nil {
		return Outcome{}, err
	}
	if !ready {
		return arm.finalizeCaptureOutcome(ctx, failedCaptureOutcome(
			StatusLimitExceeded,
			initialDigest,
			"capture_storage_limit",
		), result)
	}
	git, err := arm.prepareCaptureGit()
	if err != nil {
		return Outcome{}, err
	}
	defer func() {
		if closeErr := git.directory.Close(); closeErr != nil {
			outcome = Outcome{}
			resultErr = errors.Join(resultErr, fmt.Errorf("close capture Git directory: %w", closeErr))
		}
	}()

	baseTree, err := git.baseTree(ctx)
	if err != nil {
		return Outcome{}, err
	}
	if err := git.readTree(ctx, captureResultIndex, arm.pair.baseRevision); err != nil {
		return Outcome{}, err
	}
	files, err := git.hashResultFiles(ctx, arm.pair.baseManifest, result)
	if err != nil {
		return Outcome{}, err
	}
	if err := git.updateResultIndex(ctx, arm.pair.baseManifest, result, files); err != nil {
		return Outcome{}, err
	}
	resultTree, err := git.writeTree(ctx, captureResultIndex)
	if err != nil {
		return Outcome{}, err
	}
	resultDigest := worktreeManifestDigest(result)
	if (resultDigest == initialDigest) != (resultTree == baseTree) {
		return arm.finalizeCaptureOutcome(ctx, failedCaptureOutcome(
			StatusInvalidTree,
			initialDigest,
			"unrepresentable_workspace",
		), result)
	}
	if resultTree == baseTree {
		if err := git.verifyEmptyRoundTrip(ctx, baseTree); err != nil {
			return Outcome{}, err
		}
		return arm.finalizeCaptureOutcome(ctx, Outcome{
			SchemaVersion:      OutcomeSchemaVersion,
			Status:             StatusNoChange,
			InitialTreeSHA256:  initialDigest,
			ResultTreeSHA256:   resultDigest,
			ResultTreeObjectID: resultTree,
			PatchSHA256:        digest(nil),
		}, result)
	}

	numstat, err := git.diffOutput(
		ctx,
		arm.pair.inputs.Limits.MaximumPatchBytes,
		"--numstat",
		"-z",
	)
	if errors.Is(err, errCaptureOutputLimit) {
		return arm.finalizeCaptureOutcome(ctx, failedCaptureOutcome(
			StatusLimitExceeded,
			initialDigest,
			"change_metadata_limit",
		), result)
	}
	if err != nil {
		return Outcome{}, err
	}
	changedFiles, changedLines, err := parseCaptureNumstat(
		numstat,
		arm.pair.inputs.Limits,
	)
	if errors.Is(err, errCaptureOutputLimit) {
		return arm.finalizeCaptureOutcome(ctx, failedCaptureOutcome(
			StatusLimitExceeded,
			initialDigest,
			"change_count_limit",
		), result)
	}
	if err != nil {
		return Outcome{}, err
	}

	patch, err := git.diffOutput(
		ctx,
		arm.pair.inputs.Limits.MaximumPatchBytes,
		"--binary",
		"--full-index",
		"--unified=3",
		"--diff-algorithm=myers",
		"--no-indent-heuristic",
		"--inter-hunk-context=0",
		"--src-prefix=a/",
		"--dst-prefix=b/",
	)
	if errors.Is(err, errCaptureOutputLimit) {
		return arm.finalizeCaptureOutcome(ctx, failedCaptureOutcome(
			StatusLimitExceeded,
			initialDigest,
			"patch_limit",
		), result)
	}
	if err != nil {
		return Outcome{}, err
	}
	if len(patch) == 0 || changedFiles == 0 {
		return Outcome{}, errors.New("changed workspace produced an empty Git patch")
	}
	if err := git.verifyPatchRoundTrip(ctx, patch, resultTree); err != nil {
		return Outcome{}, err
	}
	return arm.finalizeCaptureOutcome(ctx, Outcome{
		SchemaVersion:      OutcomeSchemaVersion,
		Status:             StatusCaptured,
		InitialTreeSHA256:  initialDigest,
		ResultTreeSHA256:   resultDigest,
		ResultTreeObjectID: resultTree,
		PatchSHA256:        digest(patch),
		Patch:              patch,
		ChangedFiles:       changedFiles,
		ChangedLines:       changedLines,
	}, result)
}

func (arm *ArmAuthority) finalizeCaptureOutcome(
	ctx context.Context,
	outcome Outcome,
	expected []worktreeEntry,
) (Outcome, error) {
	if err := arm.reverifyLocked(ctx, true); err != nil {
		return Outcome{}, fmt.Errorf("reverify frozen workspace after capture: %w", err)
	}
	if expected != nil {
		actual, err := scanCapturedWorktree(
			ctx,
			arm.overlayRoot,
			arm.pair.baseManifest,
			arm.pair.inputs.Limits,
		)
		if err != nil || !reflect.DeepEqual(actual, expected) {
			return Outcome{}, errors.Join(
				errors.New("frozen workspace changed during capture"),
				err,
			)
		}
	}
	if err := outcome.Validate(arm.pair.inputs.Limits); err != nil {
		return Outcome{}, fmt.Errorf("validate captured workspace outcome: %w", err)
	}
	return outcome, nil
}

func failedCaptureOutcome(status Status, initialDigest, violation string) Outcome {
	return Outcome{
		SchemaVersion:     OutcomeSchemaVersion,
		Status:            status,
		InitialTreeSHA256: initialDigest,
		ViolationCode:     violation,
	}
}

func (arm *ArmAuthority) prepareCaptureGit() (_ *captureGitRunner, resultErr error) {
	var format string
	switch len(arm.pair.baseRevision) {
	case 40:
		format = "sha1"
	case 64:
		format = "sha256"
	default:
		return nil, errors.New("workspace base revision has an unsupported object format")
	}
	if !validGitObjectID(arm.pair.baseRevision) {
		return nil, errors.New("workspace base revision is invalid")
	}

	directory, _, err := createDirectoryAt(arm.capture, "repository")
	if err != nil {
		return nil, fmt.Errorf("create capture Git directory: %w", err)
	}
	defer func() {
		if resultErr != nil {
			resultErr = errors.Join(resultErr, directory.Close())
		}
	}()
	objects, _, err := createDirectoryAt(directory, "objects")
	if err != nil {
		return nil, err
	}
	info, _, err := createDirectoryAt(objects, "info")
	if err == nil {
		err = writeCaptureFile(info, "alternates", []byte(captureGitObjectsPath+"\n"))
	}
	if closeErr := infoClose(info); closeErr != nil {
		err = errors.Join(err, closeErr)
	}
	if err == nil {
		var pack *os.File
		pack, _, err = createDirectoryAt(objects, "pack")
		if closeErr := infoClose(pack); closeErr != nil {
			err = errors.Join(err, closeErr)
		}
	}
	if closeErr := objects.Close(); closeErr != nil {
		err = errors.Join(err, closeErr)
	}
	if err != nil {
		return nil, fmt.Errorf("create capture Git object store: %w", err)
	}
	for _, name := range []string{"refs", "home", "tmp", "templates"} {
		created, _, createErr := createDirectoryAt(directory, name)
		if createErr == nil {
			createErr = created.Close()
		}
		if createErr != nil {
			return nil, fmt.Errorf("create capture Git %s directory: %w", name, createErr)
		}
	}
	config := captureGitConfig(format)
	if err := writeCaptureFile(directory, "config", []byte(config)); err != nil {
		return nil, err
	}
	if err := writeCaptureFile(
		directory,
		"HEAD",
		[]byte(arm.pair.baseRevision+"\n"),
	); err != nil {
		return nil, err
	}
	if err := directory.Sync(); err != nil {
		return nil, err
	}
	directoryInfo, err := directory.Stat()
	if err != nil {
		return nil, err
	}
	worktreeInfo, err := arm.overlayRoot.Stat()
	if err != nil {
		return nil, err
	}
	return &captureGitRunner{
		executable:     arm.pair.verifierGit,
		objects:        arm.pair.gitObjects,
		directory:      directory,
		worktree:       arm.overlayRoot,
		executableInfo: arm.pair.verifierInfo,
		objectsInfo:    arm.pair.objectsInfo,
		directoryInfo:  directoryInfo,
		worktreeInfo:   worktreeInfo,
		displayPath:    arm.pair.verifierGit.Name(),
		baseRevision:   arm.pair.baseRevision,
		objectIDLength: len(arm.pair.baseRevision),
	}, nil
}

func captureGitConfig(format string) string {
	repositoryVersion := "0"
	extensions := ""
	if format == "sha256" {
		repositoryVersion = "1"
		extensions = "[extensions]\n\tobjectFormat = sha256\n"
	}
	return "[core]\n" +
		"\trepositoryFormatVersion = " + repositoryVersion + "\n" +
		"\tbare = false\n" +
		"\tfileMode = true\n" +
		"\tsymlinks = false\n" +
		"\tignoreCase = false\n" +
		"\tlogAllRefUpdates = false\n" +
		"\tprecomposeUnicode = false\n" +
		"[diff]\n" +
		"\trenames = false\n" +
		extensions
}

func infoClose(file *os.File) error {
	if file == nil {
		return nil
	}
	return file.Close()
}

func writeCaptureFile(parent *os.File, name string, content []byte) (resultErr error) {
	if parent == nil {
		return errors.New("capture file parent descriptor is absent")
	}
	descriptor, err := openCaptureFile(parent, name)
	if err != nil {
		return err
	}
	file := os.NewFile(uintptr(descriptor), name)
	defer func() { resultErr = errors.Join(resultErr, file.Close()) }()
	written, err := file.Write(content)
	if err != nil || written != len(content) {
		return errors.Join(errors.New("write capture file completely"), err)
	}
	if err := file.Sync(); err != nil {
		return err
	}
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0o600 ||
		info.Size() != int64(len(content)) {
		return errors.Join(errors.New("capture file identity is invalid"), err)
	}
	return parent.Sync()
}

func openCaptureFile(parent *os.File, name string) (int, error) {
	return unix.Openat(
		int(parent.Fd()),
		name,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		0o600,
	)
}

func (git *captureGitRunner) baseTree(ctx context.Context) (string, error) {
	output, err := git.output(
		ctx,
		captureResultIndex,
		git.objectIDLength+1,
		nil,
		"rev-parse",
		"--verify",
		git.baseRevision+"^{tree}",
	)
	if err != nil {
		return "", err
	}
	return git.parseObjectID(output, "base tree")
}

func (git *captureGitRunner) readTree(ctx context.Context, index, tree string) error {
	_, err := git.output(ctx, index, 0, nil, "read-tree", tree)
	return err
}

func (git *captureGitRunner) writeTree(ctx context.Context, index string) (string, error) {
	output, err := git.output(ctx, index, git.objectIDLength+1, nil, "write-tree")
	if err != nil {
		return "", err
	}
	return git.parseObjectID(output, "result tree")
}

func (git *captureGitRunner) hashResultFiles(
	ctx context.Context,
	base []worktreeEntry,
	result []worktreeEntry,
) ([]captureIndexEntry, error) {
	updates := captureFileUpdates(base, result)
	files := make([]captureIndexEntry, 0, len(updates))
	batchPaths := make([]string, 0, maximumHashArgumentCount)
	batchEntries := make([]worktreeEntry, 0, maximumHashArgumentCount)
	argumentBytes := 0
	flush := func() error {
		if len(batchEntries) == 0 {
			return nil
		}
		arguments := []string{"hash-object", "-w", "--no-filters", "--"}
		arguments = append(arguments, batchPaths...)
		output, err := git.output(
			ctx,
			captureResultIndex,
			len(batchEntries)*(git.objectIDLength+1),
			nil,
			arguments...,
		)
		if err != nil {
			return err
		}
		lines := bytes.Split(output, []byte{'\n'})
		if len(lines) != len(batchEntries)+1 || len(lines[len(lines)-1]) != 0 {
			return errors.New("capture Git returned a malformed blob list")
		}
		for index, entry := range batchEntries {
			objectID := string(lines[index])
			if err := git.validateObjectID(objectID); err != nil {
				return err
			}
			files = append(files, captureIndexEntry{
				path: entry.path, objectID: objectID, mode: entry.mode,
			})
		}
		batchPaths = batchPaths[:0]
		batchEntries = batchEntries[:0]
		argumentBytes = 0
		return nil
	}
	for _, entry := range updates {
		path := captureGitWorktreePath + "/" + entry.path
		if len(batchEntries) != 0 &&
			(len(batchEntries) >= maximumHashArgumentCount ||
				argumentBytes+len(path)+1 > maximumHashArgumentBytes) {
			if err := flush(); err != nil {
				return nil, err
			}
		}
		batchPaths = append(batchPaths, path)
		batchEntries = append(batchEntries, entry)
		argumentBytes += len(path) + 1
	}
	if err := flush(); err != nil {
		return nil, err
	}
	return files, nil
}

func (git *captureGitRunner) updateResultIndex(
	ctx context.Context,
	base []worktreeEntry,
	result []worktreeEntry,
	files []captureIndexEntry,
) error {
	resultPaths := make(map[string]struct{}, len(result))
	for _, entry := range result {
		if entry.kind == snapshot.ManifestKindFile {
			resultPaths[entry.path] = struct{}{}
		}
	}
	deletions := make([]string, 0)
	for _, entry := range base {
		if entry.kind != snapshot.ManifestKindFile {
			continue
		}
		if _, exists := resultPaths[entry.path]; !exists {
			deletions = append(deletions, entry.path)
		}
	}
	reader := &captureIndexInfoReader{
		deletions: deletions,
		entries:   files,
		zeroID:    strings.Repeat("0", git.objectIDLength),
	}
	_, err := git.output(
		ctx,
		captureResultIndex,
		0,
		reader,
		"update-index",
		"-z",
		"--index-info",
	)
	return err
}

func (git *captureGitRunner) diffOutput(
	ctx context.Context,
	limit int,
	extra ...string,
) ([]byte, error) {
	arguments := []string{
		"diff",
		"--cached",
		"--no-renames",
		"--no-ext-diff",
		"--no-textconv",
		"--no-color",
		"--ignore-submodules=none",
	}
	arguments = append(arguments, extra...)
	arguments = append(arguments, git.baseRevision, "--")
	return git.output(ctx, captureResultIndex, limit, nil, arguments...)
}

func (git *captureGitRunner) verifyEmptyRoundTrip(
	ctx context.Context,
	baseTree string,
) error {
	if err := git.readTree(ctx, captureReplayIndex, git.baseRevision); err != nil {
		return err
	}
	replayed, err := git.writeTree(ctx, captureReplayIndex)
	if err != nil {
		return err
	}
	if replayed != baseTree {
		return errors.New("fresh base index does not reproduce the base tree")
	}
	return nil
}

func (git *captureGitRunner) verifyPatchRoundTrip(
	ctx context.Context,
	patch []byte,
	resultTree string,
) error {
	if err := git.readTree(ctx, captureReplayIndex, git.baseRevision); err != nil {
		return err
	}
	if _, err := git.output(
		ctx,
		captureReplayIndex,
		0,
		bytes.NewReader(patch),
		"apply",
		"--cached",
		"--binary",
		"--whitespace=nowarn",
		"-",
	); err != nil {
		return fmt.Errorf("apply captured patch to a fresh base index: %w", err)
	}
	replayed, err := git.writeTree(ctx, captureReplayIndex)
	if err != nil {
		return err
	}
	if replayed != resultTree {
		return errors.New("captured patch does not reproduce the result tree")
	}
	return nil
}

func (git *captureGitRunner) parseObjectID(output []byte, label string) (string, error) {
	if len(output) != git.objectIDLength+1 || output[len(output)-1] != '\n' {
		return "", fmt.Errorf("capture Git returned a malformed %s object ID", label)
	}
	objectID := string(output[:len(output)-1])
	if err := git.validateObjectID(objectID); err != nil {
		return "", err
	}
	return objectID, nil
}

func (git *captureGitRunner) validateObjectID(value string) error {
	if len(value) != git.objectIDLength {
		return errors.New("capture Git returned an object ID with the wrong length")
	}
	decoded, err := hex.DecodeString(value)
	if err != nil || hex.EncodeToString(decoded) != value {
		return errors.New("capture Git returned a noncanonical object ID")
	}
	return nil
}

func (git *captureGitRunner) output(
	ctx context.Context,
	index string,
	stdoutLimit int,
	stdin io.Reader,
	arguments ...string,
) ([]byte, error) {
	if err := git.verifyDescriptors(); err != nil {
		return nil, err
	}
	command := exec.CommandContext(ctx, captureGitExecutablePath, arguments...)
	command.Args[0] = git.displayPath
	command.ExtraFiles = []*os.File{git.executable, git.objects, git.directory, git.worktree}
	command.Dir = "/"
	command.Env = git.environment(index)
	command.Stdin = stdin
	stdout := &captureBoundedBuffer{limit: stdoutLimit}
	stderr := &captureBoundedBuffer{limit: maximumCaptureGitErrorBytes}
	command.Stdout = stdout
	command.Stderr = stderr
	commandErr := command.Run()
	if err := git.verifyDescriptors(); err != nil {
		return nil, fmt.Errorf("verify capture descriptors after Git: %w", err)
	}
	if stdout.exceeded {
		return nil, errCaptureOutputLimit
	}
	if stderr.exceeded {
		return nil, errors.New("capture Git error output exceeded its bound")
	}
	if commandErr != nil {
		label := "Git"
		if len(arguments) != 0 {
			label += " " + arguments[0]
		}
		return nil, fmt.Errorf("capture %s failed: %w: %s", label, commandErr, bytes.TrimSpace(stderr.Bytes()))
	}
	return append([]byte(nil), stdout.Bytes()...), nil
}

func (git *captureGitRunner) environment(index string) []string {
	return []string{
		"GIT_DIR=" + captureGitDirectoryPath,
		"GIT_WORK_TREE=" + captureGitWorktreePath,
		"GIT_INDEX_FILE=" + captureGitDirectoryPath + "/" + index,
		"GIT_OBJECT_DIRECTORY=" + captureGitDirectoryPath + "/objects",
		"GIT_CONFIG_GLOBAL=/dev/null",
		"GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_ATTR_NOSYSTEM=1",
		"GIT_NO_REPLACE_OBJECTS=1",
		"GIT_OPTIONAL_LOCKS=0",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_CONFIG_COUNT=0",
		"GIT_EXEC_PATH=/nonexistent",
		"GIT_TEMPLATE_DIR=" + captureGitDirectoryPath + "/templates",
		"GIT_CEILING_DIRECTORIES=/",
		"GIT_DISCOVERY_ACROSS_FILESYSTEM=0",
		"GIT_PAGER=",
		"PAGER=",
		"HOME=" + captureGitDirectoryPath + "/home",
		"TMPDIR=" + captureGitDirectoryPath + "/tmp",
		"PATH=",
		"LANG=C",
		"LC_ALL=C",
		"TZ=UTC",
	}
}

func (git *captureGitRunner) verifyDescriptors() error {
	if git.executable == nil || git.objects == nil || git.directory == nil || git.worktree == nil {
		return errors.New("capture Git descriptor set is incomplete")
	}
	executable, executableErr := git.executable.Stat()
	objects, objectsErr := git.objects.Stat()
	directory, directoryErr := git.directory.Stat()
	worktree, worktreeErr := git.worktree.Stat()
	if executableErr != nil || objectsErr != nil || directoryErr != nil || worktreeErr != nil ||
		!sameFileInfo(git.executableInfo, executable) ||
		!sameFileInfo(git.objectsInfo, objects) ||
		!sameDescriptorDirectory(git.directoryInfo, directory) ||
		!sameFileInfo(git.worktreeInfo, worktree) {
		return errors.Join(
			errors.New("capture Git descriptor identity changed"),
			executableErr,
			objectsErr,
			directoryErr,
			worktreeErr,
		)
	}
	return nil
}

func sameDescriptorDirectory(left, right os.FileInfo) bool {
	return left != nil && right != nil && left.IsDir() && right.IsDir() &&
		os.SameFile(left, right) && left.Mode() == right.Mode()
}

type captureBoundedBuffer struct {
	buffer   bytes.Buffer
	limit    int
	exceeded bool
}

func (buffer *captureBoundedBuffer) Write(content []byte) (int, error) {
	if buffer.limit < 0 {
		buffer.exceeded = true
		return 0, errCaptureOutputLimit
	}
	remaining := buffer.limit - buffer.buffer.Len()
	if len(content) <= remaining {
		return buffer.buffer.Write(content)
	}
	if remaining > 0 {
		_, _ = buffer.buffer.Write(content[:remaining])
	}
	buffer.exceeded = true
	return remaining, errCaptureOutputLimit
}

func (buffer *captureBoundedBuffer) Bytes() []byte { return buffer.buffer.Bytes() }

type captureIndexInfoReader struct {
	deletions []string
	entries   []captureIndexEntry
	zeroID    string
	current   []byte
	offset    int
	index     int
	adding    bool
}

func (reader *captureIndexInfoReader) Read(destination []byte) (int, error) {
	written := 0
	for written < len(destination) {
		if reader.offset < len(reader.current) {
			copied := copy(destination[written:], reader.current[reader.offset:])
			written += copied
			reader.offset += copied
			continue
		}
		reader.current = nil
		reader.offset = 0
		if !reader.adding && reader.index >= len(reader.deletions) {
			reader.adding = true
			reader.index = 0
		}
		if reader.adding {
			if reader.index >= len(reader.entries) {
				if written == 0 {
					return 0, io.EOF
				}
				return written, nil
			}
			entry := reader.entries[reader.index]
			reader.index++
			mode := "100644"
			if entry.mode == 0o755 {
				mode = "100755"
			}
			reader.current = []byte(mode + " blob " + entry.objectID + "\t" + entry.path + "\x00")
			continue
		}
		path := reader.deletions[reader.index]
		reader.index++
		reader.current = []byte("0 " + reader.zeroID + "\t" + path + "\x00")
	}
	return written, nil
}

func parseCaptureNumstat(content []byte, limits Limits) (int, int, error) {
	if len(content) == 0 || content[len(content)-1] != 0 {
		return 0, 0, errors.New("capture Git returned malformed numstat data")
	}
	records := bytes.Split(content[:len(content)-1], []byte{0})
	seen := make(map[string]struct{}, min(len(records), limits.MaximumChangedFiles+1))
	changedLines := 0
	for _, record := range records {
		firstTab := bytes.IndexByte(record, '\t')
		secondRelative := -1
		if firstTab >= 0 {
			secondRelative = bytes.IndexByte(record[firstTab+1:], '\t')
		}
		if firstTab <= 0 || secondRelative <= 0 {
			return 0, 0, errors.New("capture Git returned malformed numstat fields")
		}
		secondTab := firstTab + 1 + secondRelative
		path := string(record[secondTab+1:])
		if !validWorktreeRelativePath(path) || path == "." {
			return 0, 0, errors.New("capture Git returned an invalid changed path")
		}
		if _, exists := seen[path]; exists {
			return 0, 0, errors.New("capture Git returned a duplicate changed path")
		}
		seen[path] = struct{}{}
		if len(seen) > limits.MaximumChangedFiles {
			return 0, 0, errCaptureOutputLimit
		}
		added, deleted, err := parseCaptureLineCounts(
			record[:firstTab],
			record[firstTab+1:secondTab],
		)
		if err != nil {
			return 0, 0, err
		}
		if added > limits.MaximumPatchBytes-changedLines ||
			deleted > limits.MaximumPatchBytes-changedLines-added {
			return 0, 0, errCaptureOutputLimit
		}
		changedLines += added + deleted
	}
	return len(seen), changedLines, nil
}

func parseCaptureLineCounts(added, deleted []byte) (int, int, error) {
	if bytes.Equal(added, []byte{'-'}) && bytes.Equal(deleted, []byte{'-'}) {
		return 0, 0, nil
	}
	parse := func(value []byte) (int, error) {
		if len(value) == 0 || (len(value) > 1 && value[0] == '0') {
			return 0, errors.New("capture Git returned a noncanonical line count")
		}
		parsed, err := strconv.ParseUint(string(value), 10, 31)
		if err != nil {
			return 0, errors.New("capture Git returned an invalid line count")
		}
		return int(parsed), nil
	}
	addedCount, err := parse(added)
	if err != nil {
		return 0, 0, err
	}
	deletedCount, err := parse(deleted)
	if err != nil {
		return 0, 0, err
	}
	return addedCount, deletedCount, nil
}
