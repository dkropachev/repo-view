//go:build linux

package snapshot

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/yapless/scopesifter/internal/gitdiffcontract"
	"golang.org/x/sys/unix"
)

const (
	maximumGitErrorBytes    = 64 << 10
	maximumGitIdentityBytes = 64 << 10
)

type boundedBuffer struct {
	bytes.Buffer
	limit int
}

func (buffer *boundedBuffer) Write(content []byte) (int, error) {
	remaining := buffer.limit - buffer.Len()
	if remaining <= 0 {
		return 0, errors.New("git output exceeds its snapshot limit")
	}
	if len(content) > remaining {
		written, _ := buffer.Buffer.Write(content[:remaining])
		return written, errors.New("git output exceeds its snapshot limit")
	}
	return buffer.Buffer.Write(content)
}

func buildChangedStateCache(
	ctx context.Context,
	root, gitPath, gitSHA256, base, head string,
) (ChangedStateCache, []byte, error) {
	resolvedHead, err := snapshotGitText(
		ctx, root, gitPath, gitSHA256, maximumGitIdentityBytes,
		"rev-parse", "--verify", "--end-of-options", "HEAD^{commit}",
	)
	if err != nil || resolvedHead != head {
		return ChangedStateCache{}, nil, errors.Join(
			fmt.Errorf("changed-state HEAD resolved to %q, want %q", resolvedHead, head),
			err,
		)
	}
	resolvedBase, err := snapshotGitText(
		ctx, root, gitPath, gitSHA256, maximumGitIdentityBytes,
		"rev-parse", "--verify", "--end-of-options", base+"^{commit}",
	)
	if err != nil || resolvedBase != base {
		return ChangedStateCache{}, nil, errors.Join(
			fmt.Errorf("changed-state base resolved to %q, want %q", resolvedBase, base),
			err,
		)
	}
	if _, err := snapshotGitOutput(
		ctx, root, gitPath, gitSHA256, maximumGitIdentityBytes,
		"merge-base", "--is-ancestor", base, head,
	); err != nil {
		return ChangedStateCache{}, nil, fmt.Errorf("verify changed-state ancestry: %w", err)
	}
	headSubject, err := snapshotGitText(
		ctx, root, gitPath, gitSHA256, maximumHeadSubjectBytes+2,
		"show", "-s", "--format=%s", head,
	)
	if err != nil {
		return ChangedStateCache{}, nil, fmt.Errorf("read changed-state head subject: %w", err)
	}
	if len(headSubject) > maximumHeadSubjectBytes || !utf8.ValidString(headSubject) ||
		strings.ContainsAny(headSubject, "\x00\r\n") {
		return ChangedStateCache{}, nil, errors.New("changed-state head subject is invalid")
	}
	statusOutput, err := snapshotGitOutput(
		ctx, root, gitPath, gitSHA256, int(maximumRegularFileBytes),
		gitdiffcontract.NameStatusArguments(base, head)...,
	)
	if err != nil {
		return ChangedStateCache{}, nil, fmt.Errorf("list changed-state files: %w", err)
	}
	files, err := parseChangedFileStates(statusOutput)
	if err != nil {
		return ChangedStateCache{}, nil, err
	}
	numstatOutput, err := snapshotGitOutput(
		ctx, root, gitPath, gitSHA256, int(maximumRegularFileBytes),
		gitdiffcontract.NumstatArguments(base, head)...,
	)
	if err != nil {
		return ChangedStateCache{}, nil, fmt.Errorf("classify changed-state binary files: %w", err)
	}
	binaryByPath, err := parseBinaryPaths(numstatOutput)
	if err != nil {
		return ChangedStateCache{}, nil, err
	}
	for index := range files {
		binary, exists := binaryByPath[files[index].Path]
		if !exists {
			return ChangedStateCache{}, nil, fmt.Errorf(
				"changed path %q is absent from canonical numstat output",
				files[index].Path,
			)
		}
		files[index].Binary = binary
	}
	patchOutput, err := snapshotGitOutput(
		ctx, root, gitPath, gitSHA256, maximumChangedPatchBytes,
		gitdiffcontract.PatchArguments(base, head)...,
	)
	if err != nil {
		return ChangedStateCache{}, nil, fmt.Errorf("read full changed-state patch: %w", err)
	}
	if !utf8.Valid(patchOutput) || bytes.IndexByte(patchOutput, 0) >= 0 {
		return ChangedStateCache{}, nil, errors.New("changed-state patch is not canonical UTF-8 text")
	}
	aggregate := 0
	for index := range files {
		if err := ctx.Err(); err != nil {
			return ChangedStateCache{}, nil, err
		}
		path := files[index].Path
		patchArguments := append(gitdiffcontract.PatchArguments(base, head), path)
		filePatch, err := snapshotGitOutput(
			ctx, root, gitPath, gitSHA256, maximumPerFilePatchBytes,
			patchArguments...,
		)
		if err != nil {
			return ChangedStateCache{}, nil, fmt.Errorf("read canonical patch for %q: %w", path, err)
		}
		if !utf8.Valid(filePatch) || bytes.IndexByte(filePatch, 0) >= 0 {
			return ChangedStateCache{}, nil, fmt.Errorf("canonical patch for %q is not UTF-8 text", path)
		}
		if aggregate > maximumAggregatePatchBytes-len(filePatch) {
			return ChangedStateCache{}, nil, errors.New("changed-state per-file patches exceed aggregate limit")
		}
		aggregate += len(filePatch)
		spanArguments := append(gitdiffcontract.ChangedLineArguments(base, head), path)
		spanOutput, err := snapshotGitOutput(
			ctx, root, gitPath, gitSHA256, maximumPerFilePatchBytes,
			spanArguments...,
		)
		if err != nil {
			return ChangedStateCache{}, nil, fmt.Errorf("read changed spans for %q: %w", path, err)
		}
		spans, err := parseChangedSpans(spanOutput)
		if err != nil {
			return ChangedStateCache{}, nil, fmt.Errorf("parse changed spans for %q: %w", path, err)
		}
		files[index].Lines = spans
		files[index].Patch = string(filePatch)
		files[index].PatchSHA256 = digest(filePatch)
	}
	cache := ChangedStateCache{
		SchemaVersion: ChangedStateSchemaVersion,
		BaseCommit:    base,
		HeadCommit:    head,
		HeadSubject:   headSubject,
		ChangedFiles:  files,
		Patch:         string(patchOutput),
	}
	if err := cache.Validate(); err != nil {
		return ChangedStateCache{}, nil, fmt.Errorf("validate generated changed-state cache: %w", err)
	}
	raw, err := json.Marshal(cache)
	if err != nil {
		return ChangedStateCache{}, nil, fmt.Errorf("encode changed-state cache: %w", err)
	}
	if int64(len(raw)) > maximumRegularFileBytes {
		return ChangedStateCache{}, nil, errors.New("encoded changed-state cache exceeds snapshot file limit")
	}
	return cache, raw, nil
}

func parseChangedFileStates(output []byte) ([]ChangedFileState, error) {
	records := bytes.Split(output, []byte{0})
	files := make([]ChangedFileState, 0)
	for index := 0; index < len(records); {
		if len(records[index]) == 0 {
			index++
			continue
		}
		statusToken := string(records[index])
		index++
		if len(statusToken) == 0 {
			return nil, errors.New("git returned an empty changed status")
		}
		status := statusToken[:1]
		state := ChangedFileState{}
		switch status {
		case "A":
			state.Status = "added"
		case "D":
			state.Status = "deleted"
		case "M":
			state.Status = "modified"
		case "T":
			state.Status = "type-changed"
		case "R", "C":
			if len(statusToken) < 2 {
				return nil, errors.New("git returned a rename/copy without similarity")
			}
			similarity, err := strconv.Atoi(statusToken[1:])
			if err != nil || similarity < 1 || similarity > 100 {
				return nil, errors.New("git returned an invalid rename/copy similarity")
			}
			state.Similarity = similarity
			if status == "R" {
				state.Status = "renamed"
			} else {
				state.Status = "copied"
			}
		default:
			return nil, fmt.Errorf("git returned unsupported changed status %q", statusToken)
		}
		if status == "R" || status == "C" {
			if index+1 >= len(records) {
				return nil, errors.New("git returned a truncated rename/copy record")
			}
			state.PreviousPath = filepath.ToSlash(string(records[index]))
			state.Path = filepath.ToSlash(string(records[index+1]))
			index += 2
		} else {
			if index >= len(records) {
				return nil, errors.New("git returned a truncated changed path record")
			}
			state.Path = filepath.ToSlash(string(records[index]))
			index++
		}
		if !validRepositoryPath(state.Path) ||
			state.PreviousPath != "" && !validRepositoryPath(state.PreviousPath) {
			return nil, errors.New("git returned an unsafe changed path")
		}
		files = append(files, state)
		if len(files) > maximumChangedFiles {
			return nil, errors.New("changed-state file list exceeds its limit")
		}
	}
	sort.Slice(files, func(left, right int) bool { return files[left].Path < files[right].Path })
	for index := 1; index < len(files); index++ {
		if files[index-1].Path == files[index].Path {
			return nil, errors.New("git returned duplicate changed paths")
		}
	}
	return files, nil
}

func parseBinaryPaths(output []byte) (map[string]bool, error) {
	records := bytes.Split(output, []byte{0})
	result := make(map[string]bool)
	for index := 0; index < len(records); {
		record := records[index]
		index++
		if len(record) == 0 {
			continue
		}
		firstTab := bytes.IndexByte(record, '\t')
		secondRelative := -1
		if firstTab >= 0 {
			secondRelative = bytes.IndexByte(record[firstTab+1:], '\t')
		}
		if firstTab <= 0 || secondRelative < 0 {
			return nil, errors.New("git returned malformed canonical numstat output")
		}
		secondTab := firstTab + 1 + secondRelative
		added := string(record[:firstTab])
		deleted := string(record[firstTab+1 : secondTab])
		pathBytes := record[secondTab+1:]
		if len(pathBytes) == 0 {
			// With -z, a rename/copy is: counts+NUL, old+NUL, new+NUL.
			if index+1 >= len(records) {
				return nil, errors.New("git returned truncated rename numstat output")
			}
			pathBytes = records[index+1]
			index += 2
		}
		path := filepath.ToSlash(string(pathBytes))
		if !validRepositoryPath(path) {
			return nil, errors.New("git returned unsafe canonical numstat path")
		}
		if _, exists := result[path]; exists {
			return nil, errors.New("git returned duplicate canonical numstat path")
		}
		if (added == "-") != (deleted == "-") {
			return nil, errors.New("git returned inconsistent binary numstat counts")
		}
		if added != "-" {
			if _, err := strconv.ParseUint(added, 10, 64); err != nil {
				return nil, errors.New("git returned invalid numstat additions")
			}
			if _, err := strconv.ParseUint(deleted, 10, 64); err != nil {
				return nil, errors.New("git returned invalid numstat deletions")
			}
		}
		result[path] = added == "-" && deleted == "-"
	}
	return result, nil
}

func parseChangedSpans(output []byte) ([]ChangedLineSpan, error) {
	shared, err := gitdiffcontract.ParseChangedSpans(
		output,
		maximumChangedSpansPerFile,
		maximumChangedLine,
	)
	if err != nil {
		return nil, err
	}
	spans := make([]ChangedLineSpan, len(shared))
	for index, span := range shared {
		spans[index] = ChangedLineSpan{Start: span.Start, End: span.End}
	}
	return spans, nil
}

func snapshotGitText(
	ctx context.Context,
	root, gitPath, gitSHA256 string,
	limit int,
	arguments ...string,
) (string, error) {
	output, err := snapshotGitOutput(ctx, root, gitPath, gitSHA256, limit, arguments...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(output)), nil
}

func snapshotGitOutput(
	ctx context.Context,
	root, gitPath, gitSHA256 string,
	limit int,
	arguments ...string,
) (output []byte, resultErr error) {
	if ctx == nil || limit <= 0 || int64(limit) > maximumRegularFileBytes {
		return nil, errors.New("invalid bounded Git invocation")
	}
	before, err := os.Lstat(gitPath)
	if err != nil || before.Mode()&os.ModeSymlink != 0 || !before.Mode().IsRegular() ||
		before.Mode().Perm()&0o111 == 0 || hasMultipleLinks(before) {
		return nil, errors.New("snapshot verifier Git is not a pinned executable")
	}
	descriptor, err := unix.Open(gitPath, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, err
	}
	gitFile := os.NewFile(uintptr(descriptor), gitPath)
	defer func() {
		if closeErr := gitFile.Close(); closeErr != nil {
			output = nil
			resultErr = errors.Join(resultErr, closeErr)
		}
	}()
	opened, err := gitFile.Stat()
	if err != nil || !os.SameFile(before, opened) {
		return nil, errors.New("snapshot verifier Git changed while opening")
	}
	digestValue, err := hashOpenFile(gitFile, opened.Size())
	if err != nil || digestValue != gitSHA256 {
		return nil, errors.Join(errors.New("snapshot verifier Git digest changed"), err)
	}
	safe := gitdiffcontract.InvocationPrefix()
	safe = append(safe, arguments...)
	command := exec.CommandContext(ctx, "/proc/self/fd/3", safe...)
	command.Args[0] = gitPath
	command.ExtraFiles = []*os.File{gitFile}
	command.Dir = root
	command.Env = gitdiffcontract.Environment(os.DevNull)
	stdout := &boundedBuffer{limit: limit}
	stderr := &boundedBuffer{limit: maximumGitErrorBytes}
	command.Stdout = stdout
	command.Stderr = stderr
	if err := command.Run(); err != nil {
		return nil, fmt.Errorf(
			"verifier Git %s: %w: %s",
			strings.Join(arguments, " "),
			err,
			bytes.TrimSpace(stderr.Bytes()),
		)
	}
	after, err := gitFile.Stat()
	if err != nil || !sameStableFile(opened, after) {
		return nil, errors.New("snapshot verifier Git changed during invocation")
	}
	digestAfter, err := hashOpenFile(gitFile, after.Size())
	if err != nil || digestAfter != gitSHA256 {
		return nil, errors.Join(errors.New("snapshot verifier Git digest changed after invocation"), err)
	}
	return append([]byte(nil), stdout.Bytes()...), nil
}
