package taskctl

import (
	"bytes"
	"context"
	"crypto/sha1" //nolint:gosec // Git SHA-1 object identity is the audited format.
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	pathpkg "path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/yapless/scopesifter/benchmarks/tokenbench/source"
)

const (
	sourceAuditTaskCount        = 144
	sourceAuditStateCount       = 107
	sourceAuditMaximumEntries   = 100_000
	sourceAuditMaximumPathSize  = 4_096
	sourceAuditMaximumDepth     = 128
	sourceAuditMaximumOutput    = 64 << 20
	sourceAuditMaximumError     = 64 << 10
	sourceAuditMaximumCommit    = 1 << 20
	sourceAuditMaximumBlobBytes = 64 << 20
	sourceAuditDirectoryMode    = 0o775
	sourceAuditCommandTimeout   = 5 * time.Minute
)

var (
	sourceAuditDigest   = regexp.MustCompile(`^[0-9a-f]{64}$`)
	sourceAuditFamilies = []string{"code", "review", "explain"}
	sourceAuditTiers    = []string{"small", "medium", "large", "huge"}
)

type sourceAuditRepository struct {
	language string
	slug     string
	upstream string
}

var sourceAuditRepositories = []sourceAuditRepository{
	{"cpp", "seastar", "scylladb/seastar"},
	{"cpp", "fmt", "fmtlib/fmt"},
	{"go", "go-git", "go-git/go-git"},
	{"go", "chi", "go-chi/chi"},
	{"rust", "scylla-driver", "scylladb/scylla-rust-driver"},
	{"rust", "clap", "clap-rs/clap"},
	{"java", "scylla-driver", "scylladb/java-driver"},
	{"java", "commons-lang", "apache/commons-lang"},
	{"python", "scylla-ccm", "scylladb/scylla-ccm"},
	{"python", "click", "pallets/click"},
	{"typescript", "got", "sindresorhus/got"},
	{"typescript", "kysely", "kysely-org/kysely"},
}

func (repository sourceAuditRepository) originURL() string {
	return "https://github.com/" + repository.upstream + ".git"
}

type sourceAuditTask struct {
	id         string
	repository sourceAuditRepository
	family     string
	tier       string
	checkout   string
}

type sourceAuditState struct {
	upstream string
	commit   string
}

type sourceAuditMode struct {
	path string
	mode string
}

type sourceAuditTreeEntry struct {
	path     string
	mode     string
	objectID string
}

type sourceAuditStateResult struct {
	status      string
	detail      string
	witnesses   []string
	symlinks    []sourceAuditMode
	gitlinks    []sourceAuditMode
	unsupported []sourceAuditMode
}

// SourceAuditOptions identifies the immutable candidate inputs and audited
// local repositories used by BuildSourceAudit. Selected Git objects are
// authenticated and digested directly; no audit worktree is created.
type SourceAuditOptions struct {
	RepositoryBindings       string
	RepositoryBindingsSHA256 string
	SourceSelections         string
	SourceSelectionsSHA256   string
	GitExecutable            string
	GitSHA256                string
}

// preparedSourceAudit is one authenticated, immutable admission snapshot.
// Publication checks and report construction must consume this same value so
// neither operation can observe a different binding document or input inode.
type preparedSourceAudit struct {
	gitInfo            os.FileInfo
	inputs             *sourceAuditInputSnapshot
	options            SourceAuditOptions
	repositoryBindings sourceAuditRepositoryBindingSet
	expectedGit        sourceAuditGitIdentity
	tasks              []sourceAuditTask
	bindingBytes       []byte
}

func (prepared *preparedSourceAudit) revalidate(ctx context.Context) error {
	if prepared == nil || prepared.inputs == nil || prepared.gitInfo == nil {
		return errors.New("prepared source-audit snapshot is incomplete")
	}
	if err := prepared.inputs.revalidate(); err != nil {
		return fmt.Errorf("revalidate prepared source-audit inputs: %w", err)
	}
	gitPin, err := prepared.repositoryBindings.git.openPin()
	if err != nil {
		return fmt.Errorf("reopen prepared source-audit Git: %w", err)
	}
	currentGit := gitPin.before
	closeErr := gitPin.close()
	if err := errors.Join(closeErr); err != nil {
		return fmt.Errorf("revalidate prepared source-audit Git: %w", err)
	}
	if !os.SameFile(prepared.gitInfo, currentGit) ||
		prepared.gitInfo.Mode() != currentGit.Mode() ||
		prepared.gitInfo.Size() != currentGit.Size() ||
		!prepared.gitInfo.ModTime().Equal(currentGit.ModTime()) {
		return errors.New("prepared source-audit Git changed identity")
	}
	current, err := sourceAuditRepositoryBindingsFromBytes(
		ctx,
		prepared.bindingBytes,
		prepared.expectedGit,
	)
	if err != nil {
		return fmt.Errorf("revalidate prepared repository bindings: %w", err)
	}
	if !sameSourceAuditRepositoryBindingSet(prepared.repositoryBindings, current) {
		return errors.New("prepared repository bindings changed")
	}
	return nil
}

// BuildSourceAudit derives and audits the exact 12-repository x 3-family x
// 4-tier source matrix. It returns the canonical Markdown report bytes and
// never modifies an input repository or report.
func BuildSourceAudit(
	ctx context.Context,
	options SourceAuditOptions,
) (report []byte, resultErr error) {
	prepared, err := prepareSourceAudit(ctx, options)
	if err != nil {
		return nil, err
	}
	return buildPreparedSourceAudit(ctx, prepared)
}

func prepareSourceAudit(
	ctx context.Context,
	options SourceAuditOptions,
) (*preparedSourceAudit, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if options.RepositoryBindings == "" || options.RepositoryBindingsSHA256 == "" ||
		options.SourceSelections == "" || options.SourceSelectionsSHA256 == "" ||
		options.GitExecutable == "" || options.GitSHA256 == "" {
		return nil, errors.New("source audit requires repository bindings and SHA-256, source selections and SHA-256, Git executable, and Git SHA-256")
	}
	if !sourceAuditDigest.MatchString(options.RepositoryBindingsSHA256) ||
		!sourceAuditDigest.MatchString(options.SourceSelectionsSHA256) {
		return nil, errors.New("source-audit input SHA-256 values must be lowercase 64-hex")
	}
	expectedGit := sourceAuditGitIdentity{
		executable: options.GitExecutable,
		sha256:     options.GitSHA256,
	}
	if err := validateSourceAuditGitIdentity(expectedGit); err != nil {
		return nil, fmt.Errorf("validate independently supplied source-audit Git: %w", err)
	}
	inputs, err := newSourceAuditInputSnapshot(
		options.RepositoryBindings,
		options.SourceSelections,
	)
	if err != nil {
		return nil, fmt.Errorf("capture source-audit inputs: %w", err)
	}
	selectionBytes, err := inputs.bytesFor(options.SourceSelections)
	if err != nil {
		return nil, fmt.Errorf("read captured source selections: %w", err)
	}
	if actual := fmt.Sprintf("%x", sha256.Sum256(selectionBytes)); actual != options.SourceSelectionsSHA256 {
		return nil, fmt.Errorf("source selections SHA-256 is %s, want %s", actual, options.SourceSelectionsSHA256)
	}
	tasks, err := ValidateSourceSelections(selectionBytes)
	if err != nil {
		return nil, fmt.Errorf("validate source selections: %w", err)
	}
	if err := validateSourceAuditCoverage(tasks); err != nil {
		return nil, err
	}

	bindingBytes, err := inputs.bytesFor(options.RepositoryBindings)
	if err != nil {
		return nil, fmt.Errorf("read captured repository bindings: %w", err)
	}
	if actual := fmt.Sprintf("%x", sha256.Sum256(bindingBytes)); actual != options.RepositoryBindingsSHA256 {
		return nil, fmt.Errorf("repository bindings SHA-256 is %s, want %s", actual, options.RepositoryBindingsSHA256)
	}
	repositoryBindings, err := sourceAuditRepositoryBindingsFromBytes(
		ctx,
		bindingBytes,
		expectedGit,
	)
	if err != nil {
		return nil, err
	}
	gitPin, err := repositoryBindings.git.openPin()
	if err != nil {
		return nil, fmt.Errorf("pin prepared source-audit Git: %w", err)
	}
	gitInfo := gitPin.before
	if err := gitPin.close(); err != nil {
		return nil, fmt.Errorf("close prepared source-audit Git pin: %w", err)
	}
	return &preparedSourceAudit{
		options:            options,
		inputs:             inputs,
		tasks:              append([]sourceAuditTask(nil), tasks...),
		bindingBytes:       bytes.Clone(bindingBytes),
		expectedGit:        expectedGit,
		gitInfo:            gitInfo,
		repositoryBindings: repositoryBindings,
	}, nil
}

func buildPreparedSourceAudit(
	ctx context.Context,
	prepared *preparedSourceAudit,
) (report []byte, resultErr error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if prepared == nil || prepared.inputs == nil || prepared.gitInfo == nil ||
		len(prepared.tasks) != sourceAuditTaskCount ||
		len(prepared.bindingBytes) == 0 ||
		len(prepared.repositoryBindings.paths) != len(sourceAuditRepositories) ||
		len(prepared.repositoryBindings.pathInfos) != len(sourceAuditRepositories) {
		return nil, errors.New("prepared source-audit snapshot is incomplete")
	}
	if err := prepared.revalidate(ctx); err != nil {
		return nil, err
	}
	inputs := prepared.inputs
	tasks := prepared.tasks
	bindingBytes := prepared.bindingBytes
	expectedGit := prepared.expectedGit
	repositoryBindings := prepared.repositoryBindings
	repositoryPaths := repositoryBindings.paths
	for _, repository := range sourceAuditRepositories {
		if err := verifySourceAuditRepositoryObjectDatabase(
			ctx,
			repositoryBindings.git,
			repositoryPaths[repository.upstream],
			repositoryBindings.pathInfos[repository.upstream],
		); err != nil {
			return nil, fmt.Errorf("verify %s object database: %w", repository.upstream, err)
		}
	}
	stateTasks := make(map[sourceAuditState][]sourceAuditTask)
	for _, task := range tasks {
		key := sourceAuditState{task.repository.upstream, task.checkout}
		stateTasks[key] = append(stateTasks[key], task)
	}
	stateResults := make(map[sourceAuditState]sourceAuditStateResult, len(stateTasks))
	for _, repository := range sourceAuditRepositories {
		keys := make([]sourceAuditState, 0)
		for key := range stateTasks {
			if key.upstream == repository.upstream {
				keys = append(keys, key)
			}
		}
		sort.Slice(keys, func(left, right int) bool { return keys[left].commit < keys[right].commit })
		for _, key := range keys {
			if err := ctx.Err(); err != nil {
				return nil, err
			}
			result, inspectErr := auditSourceState(
				ctx,
				repositoryBindings.git,
				repository,
				repositoryPaths[repository.upstream],
				repositoryBindings.pathInfos[repository.upstream],
				repositoryBindings.ordinaryRefs[repository.upstream],
				key.commit,
			)
			if inspectErr != nil {
				return nil, fmt.Errorf("audit %s at %s: %w", repository.upstream, key.commit, inspectErr)
			}
			stateResults[key] = result
		}
	}
	if len(stateResults) != sourceAuditStateCount {
		return nil, fmt.Errorf(
			"final source audit has %d unique states, want exactly %d",
			len(stateResults),
			sourceAuditStateCount,
		)
	}
	stateKeys := make([]sourceAuditState, 0, len(stateResults))
	for key := range stateResults {
		stateKeys = append(stateKeys, key)
	}
	sort.Slice(stateKeys, func(left, right int) bool {
		if stateKeys[left].upstream != stateKeys[right].upstream {
			return stateKeys[left].upstream < stateKeys[right].upstream
		}
		return stateKeys[left].commit < stateKeys[right].commit
	})
	for _, key := range stateKeys {
		result := stateResults[key]
		if result.status != "pass" {
			return nil, fmt.Errorf(
				"final source audit rejected %s at %s: %s",
				key.upstream,
				key.commit,
				result.detail,
			)
		}
	}
	finalRepositoryBindings, err := sourceAuditRepositoryBindingsFromBytes(
		ctx,
		bindingBytes,
		expectedGit,
	)
	if err != nil {
		return nil, fmt.Errorf("revalidate repository bindings after audit: %w", err)
	}
	if !sameSourceAuditRepositoryBindingSet(repositoryBindings, finalRepositoryBindings) {
		return nil, errors.New("repository bindings changed during audit")
	}
	for _, repository := range sourceAuditRepositories {
		if err := verifySourceAuditRepositoryObjectDatabase(
			ctx,
			finalRepositoryBindings.git,
			finalRepositoryBindings.paths[repository.upstream],
			finalRepositoryBindings.pathInfos[repository.upstream],
		); err != nil {
			return nil, fmt.Errorf("reverify %s object database after audit: %w", repository.upstream, err)
		}
	}
	if err := inputs.revalidate(); err != nil {
		return nil, fmt.Errorf("revalidate source-audit inputs after audit: %w", err)
	}
	return renderSourceAuditReport(tasks, stateTasks, stateResults), nil
}

func validateSourceAuditCoverage(tasks []sourceAuditTask) error {
	ids := make(map[string]struct{}, len(tasks))
	tuples := make(map[string]int, len(tasks))
	repositories := make(map[string]int, len(tasks))
	for _, task := range tasks {
		ids[task.id] = struct{}{}
		tuples[strings.Join([]string{task.repository.language, task.repository.slug, task.family, task.tier}, "\x00")]++
		repositories[strings.Join([]string{task.repository.upstream, task.family, task.tier}, "\x00")]++
	}
	if len(tasks) != sourceAuditTaskCount || len(ids) != sourceAuditTaskCount {
		return fmt.Errorf("task IDs are not exactly 144 unique values: %d/%d", len(tasks), len(ids))
	}
	if len(tuples) != sourceAuditTaskCount {
		return errors.New("task Cartesian coverage is not exact")
	}
	for _, count := range tuples {
		if count != 1 {
			return errors.New("task Cartesian coverage is not exact")
		}
	}
	if len(repositories) != sourceAuditTaskCount {
		return errors.New("12 x 3 x 4 repository coverage failed")
	}
	for _, count := range repositories {
		if count != 1 {
			return errors.New("12 x 3 x 4 repository coverage failed")
		}
	}
	return nil
}

func validateSourceAuditRepositoryConfig(configuration []byte) error {
	if len(configuration) != 0 && configuration[len(configuration)-1] != 0 {
		return errors.New("repository Git configuration is not NUL terminated")
	}
	records := bytes.Split(configuration, []byte{0})
	if len(records) == 0 || len(records[len(records)-1]) != 0 {
		return errors.New("repository Git configuration is malformed")
	}
	values := make(map[string][]string)
	for index := range len(records) - 1 {
		record := records[index]
		separator := bytes.IndexByte(record, '\n')
		if separator <= 0 || separator == len(record)-1 {
			return errors.New("repository Git configuration record is not name/newline/value")
		}
		name := strings.ToLower(string(record[:separator]))
		value := string(record[separator+1:])
		if strings.ContainsAny(name, "\x00\r\n\t =") || strings.ContainsAny(value, "\x00\r\n") {
			return errors.New("repository Git configuration record is malformed")
		}
		allowed := name == "core.bare" || name == "core.filemode" ||
			name == "core.logallrefupdates" || name == "core.repositoryformatversion" ||
			name == "remote.origin.url" || name == "remote.origin.fetch" ||
			name == "remote.origin.promisor" || name == "remote.origin.partialclonefilter"
		if strings.HasPrefix(name, "branch.") {
			allowed = strings.HasSuffix(name, ".merge") || strings.HasSuffix(name, ".remote")
		}
		if !allowed {
			return fmt.Errorf("repository Git configuration key %q is not admitted", name)
		}
		values[name] = append(values[name], value)
	}
	if fetch := values["remote.origin.fetch"]; len(fetch) != 1 ||
		fetch[0] != "+refs/heads/*:refs/remotes/origin/*" {
		return errors.New("repository Git configuration must contain exactly the canonical origin-head fetch refspec")
	}
	return nil
}

func sourceAuditPathsOverlap(left, right string) bool {
	within := func(parent, child string) bool {
		relative, err := filepath.Rel(parent, child)
		return err == nil && (relative == "." ||
			(relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))))
	}
	return within(left, right) || within(right, left)
}

func auditSourceState(
	ctx context.Context,
	git sourceAuditGitRunner,
	repository sourceAuditRepository,
	repositoryPath string,
	repositoryInfo os.FileInfo,
	ordinaryRefs []sourceAuditOrdinaryRef,
	commit string,
) (sourceAuditStateResult, error) {
	result := sourceAuditStateResult{status: "reject", detail: "commit object absent"}
	objectCheck, err := git.execute(
		ctx,
		repositoryPath,
		repositoryInfo,
		nil,
		"cat-file", "-e", commit+"^{commit}",
	)
	if err != nil {
		return result, err
	}
	if !objectCheck.success {
		return result, sourceAuditGitFailure("verify selected commit object", objectCheck)
	}
	witnesses, err := ordinarySourceAuditRefWitnesses(
		ctx,
		git,
		repositoryPath,
		repositoryInfo,
		commit,
		ordinaryRefs,
	)
	if err != nil {
		return result, err
	}
	result.witnesses = witnesses
	entries, symlinks, gitlinks, unsupported, err := inspectSourceAuditTree(
		ctx, git, repositoryPath, repositoryInfo, commit,
	)
	if err != nil {
		return result, err
	}
	result.symlinks = symlinks
	result.gitlinks = gitlinks
	result.unsupported = unsupported
	switch {
	case len(witnesses) == 0:
		result.detail = "not reachable from an ordinary upstream head or tag"
	case len(symlinks) != 0:
		result.detail = joinSourceAuditModes("tracked symlink", symlinks)
	case len(unsupported) != 0:
		result.detail = joinSourceAuditModes("unsupported tree entry", unsupported)
	case repository.upstream == "scylladb/seastar" &&
		(len(gitlinks) != 1 || gitlinks[0].path != "dpdk" || gitlinks[0].mode != "160000"):
		result.detail = joinSourceAuditModes("unexpected Seastar gitlink", gitlinks)
	case repository.upstream != "scylladb/seastar" && len(gitlinks) != 0:
		result.detail = joinSourceAuditModes("unsupported gitlink", gitlinks)
	default:
		status, detail := digestSourceAuditTree(
			ctx, git, repositoryPath, repositoryInfo, commit, entries,
		)
		result.status = status
		result.detail = detail
	}
	return result, nil
}

func ordinarySourceAuditRefWitnesses(
	ctx context.Context,
	git sourceAuditGitRunner,
	repositoryPath string,
	repositoryInfo os.FileInfo,
	commit string,
	ordinaryRefs []sourceAuditOrdinaryRef,
) ([]string, error) {
	result, err := git.execute(
		ctx,
		repositoryPath,
		repositoryInfo,
		nil,
		"for-each-ref", "--contains", commit,
		"--format=%(refname)%00%(objectname)%00%(objecttype)%00%(*objectname)%00%(*objecttype)%00",
		"refs/remotes/origin", "refs/tags",
	)
	if err != nil {
		return nil, err
	}
	if !result.success {
		return nil, sourceAuditGitFailure("resolve bound ordinary-ref witnesses", result)
	}
	if len(result.stdout) == 0 {
		return nil, nil
	}
	currentRefs, _, err := parseSourceAuditOrdinaryRefInventory(result.stdout)
	if err != nil {
		return nil, fmt.Errorf("parse ordinary-ref witnesses: %w", err)
	}
	bound := make(map[sourceAuditOrdinaryRef]struct{}, len(ordinaryRefs))
	for _, ref := range ordinaryRefs {
		bound[ref] = struct{}{}
	}
	unique := make(map[string]struct{}, len(currentRefs))
	for _, currentRef := range currentRefs {
		if _, admitted := bound[currentRef]; !admitted ||
			currentRef.name == "refs/remotes/origin/HEAD" {
			continue
		}
		ref := currentRef.name
		switch {
		case strings.HasPrefix(ref, "refs/remotes/origin/"):
			ref = "refs/heads/" + strings.TrimPrefix(ref, "refs/remotes/origin/")
		case strings.HasPrefix(ref, "refs/tags/"):
		default:
			return nil, fmt.Errorf("bound ordinary-ref %q is outside origin heads and tags", ref)
		}
		if !validSourceAuditRepositoryRefName(ref) {
			return nil, fmt.Errorf("ordinary-ref witness %q is not canonical", ref)
		}
		unique[ref] = struct{}{}
	}
	witnesses := make([]string, 0, len(unique))
	for ref := range unique {
		witnesses = append(witnesses, ref)
	}
	sort.Strings(witnesses)
	return witnesses, nil
}

func inspectSourceAuditTree(
	ctx context.Context,
	git sourceAuditGitRunner,
	repositoryPath string,
	repositoryInfo os.FileInfo,
	commit string,
) ([]sourceAuditTreeEntry, []sourceAuditMode, []sourceAuditMode, []sourceAuditMode, error) {
	result, err := git.execute(
		ctx, repositoryPath, repositoryInfo, nil, "ls-tree", "-r", "-z", commit,
	)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if !result.success {
		return nil, nil, nil, nil, sourceAuditGitFailure("inspect tree modes", result)
	}
	return parseSourceAuditTree(result.stdout)
}

func parseSourceAuditModes(listing []byte) (
	symlinks []sourceAuditMode,
	gitlinks []sourceAuditMode,
	unsupported []sourceAuditMode,
	err error,
) {
	_, symlinks, gitlinks, unsupported, err = parseSourceAuditTree(listing)
	return symlinks, gitlinks, unsupported, err
}

func parseSourceAuditTree(listing []byte) (
	entries []sourceAuditTreeEntry,
	symlinks []sourceAuditMode,
	gitlinks []sourceAuditMode,
	unsupported []sourceAuditMode,
	err error,
) {
	if len(listing) != 0 && listing[len(listing)-1] != 0 {
		return nil, nil, nil, nil, errors.New("ls-tree output is not NUL terminated")
	}
	entryCount := 0
	for _, record := range bytes.Split(listing, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		entryCount++
		if entryCount > sourceAuditMaximumEntries {
			return nil, nil, nil, nil, fmt.Errorf(
				"tree listing exceeds %d entries",
				sourceAuditMaximumEntries,
			)
		}
		parts := bytes.SplitN(record, []byte{'\t'}, 2)
		if len(parts) != 2 || len(parts[1]) == 0 {
			return nil, nil, nil, nil, errors.New("malformed NUL-framed ls-tree record")
		}
		metadata := strings.Fields(string(parts[0]))
		if len(metadata) != 3 {
			return nil, nil, nil, nil, errors.New("malformed ls-tree metadata")
		}
		mode, objectType := metadata[0], metadata[1]
		entryPath := string(parts[1])
		if err := validateSourceAuditTreePath(entryPath); err != nil {
			return nil, nil, nil, nil, fmt.Errorf("unsafe tree path %q: %w", parts[1], err)
		}
		if !commitPattern.MatchString(metadata[2]) {
			return nil, nil, nil, nil, errors.New("tree entry has a noncanonical object ID")
		}
		entry := sourceAuditMode{path: entryPath, mode: mode}
		entries = append(entries, sourceAuditTreeEntry{
			path: entryPath, mode: mode, objectID: metadata[2],
		})
		switch {
		case mode == "120000" && objectType == "blob":
			symlinks = append(symlinks, entry)
		case mode == "160000" && objectType == "commit":
			gitlinks = append(gitlinks, entry)
		case (mode != "100644" && mode != "100755") || objectType != "blob":
			entry.mode = mode + "/" + objectType
			unsupported = append(unsupported, entry)
		}
	}
	return entries, symlinks, gitlinks, unsupported, nil
}

func validateSourceAuditTreePath(value string) error {
	switch {
	case value == "":
		return errors.New("path is empty")
	case len(value) > sourceAuditMaximumPathSize:
		return fmt.Errorf("path exceeds %d bytes", sourceAuditMaximumPathSize)
	case !utf8.ValidString(value):
		return errors.New("path is not valid UTF-8")
	case strings.ContainsAny(value, "\x00\r\n\\"):
		return errors.New("path contains a forbidden byte")
	case pathpkg.IsAbs(value):
		return errors.New("path is absolute")
	case pathpkg.Clean(value) != value || value == ".":
		return errors.New("path is not canonical")
	case value == ".." || strings.HasPrefix(value, "../"):
		return errors.New("path escapes the tree")
	case strings.Count(value, "/") > sourceAuditMaximumDepth:
		return fmt.Errorf("path exceeds depth %d", sourceAuditMaximumDepth)
	}
	for _, component := range strings.Split(value, "/") {
		if strings.EqualFold(component, ".git") {
			return errors.New("path collides with Git metadata")
		}
	}
	return nil
}

func digestSourceAuditTree(
	ctx context.Context,
	git sourceAuditGitRunner,
	sourceRepository string,
	sourceRepositoryInfo os.FileInfo,
	commit string,
	entries []sourceAuditTreeEntry,
) (status, detail string) {
	commitContent, err := git.requiredOutput(
		ctx,
		sourceRepository,
		sourceRepositoryInfo,
		"read selected commit object",
		"cat-file", "commit", commit,
	)
	if err != nil {
		return "reject", err.Error()
	}
	commitTreeID, err := authenticateSourceAuditCommit(commit, commitContent)
	if err != nil {
		return "reject", err.Error()
	}
	batch, err := newSourceAuditBlobBatch(
		ctx,
		sourceRepository,
		sourceRepositoryInfo,
		git,
	)
	if err != nil {
		return "reject", "open authenticated blob reader: " + err.Error()
	}
	objectEntries := make([]source.GitTreeEntry, 0, len(entries))
	for _, entry := range entries {
		objectEntries = append(objectEntries, source.GitTreeEntry{
			Path: entry.path, Mode: entry.mode, ObjectID: entry.objectID,
		})
	}
	digest, digestErr := source.TreeDigestFromGitObjects(
		ctx,
		"sha1",
		commitTreeID,
		sourceAuditDirectoryMode,
		objectEntries,
		source.GitBlobReader(batch.ReadBlob),
	)
	if err := errors.Join(digestErr, batch.Close()); err != nil {
		return "reject", "authenticate and digest selected tree: " + err.Error()
	}
	if !sourceAuditDigest.MatchString(digest) {
		return "reject", fmt.Sprintf("non-canonical digest output: %q", digest)
	}
	return "pass", digest
}

func authenticateSourceAuditCommit(commit string, content []byte) (string, error) {
	if !commitPattern.MatchString(commit) {
		return "", errors.New("selected commit ID is not canonical SHA-1")
	}
	if len(content) == 0 || len(content) > sourceAuditMaximumCommit {
		return "", fmt.Errorf("selected commit content size is outside 1..%d bytes", sourceAuditMaximumCommit)
	}
	if bytes.IndexByte(content, 0) >= 0 {
		return "", errors.New("selected commit content contains NUL")
	}
	hasher := sha1.New() //nolint:gosec // Git SHA-1 object identity is the audited format.
	_, _ = io.WriteString(hasher, "commit "+strconv.Itoa(len(content))+"\x00")
	_, _ = hasher.Write(content)
	if actual := hex.EncodeToString(hasher.Sum(nil)); actual != commit {
		return "", fmt.Errorf("selected commit object hashes to %s, want %s", actual, commit)
	}
	lineEnd := bytes.IndexByte(content, '\n')
	if lineEnd != 45 || !bytes.HasPrefix(content, []byte("tree ")) ||
		!commitPattern.Match(content[5:45]) {
		return "", errors.New("selected commit does not start with one canonical tree header")
	}
	headerEnd := bytes.Index(content, []byte("\n\n"))
	if headerEnd <= lineEnd {
		return "", errors.New("selected commit contains duplicate or malformed tree headers")
	}
	for _, header := range bytes.Split(content[lineEnd+1:headerEnd], []byte{'\n'}) {
		if bytes.HasPrefix(header, []byte("tree ")) {
			return "", errors.New("selected commit contains duplicate or malformed tree headers")
		}
	}
	return string(content[5:45]), nil
}

func validateSourceAuditGitInvocation(directory string, arguments []string) error {
	if len(arguments) == 0 {
		return errors.New("source-audit Git invocation is empty")
	}
	validCommit := func(value string) bool { return commitPattern.MatchString(value) }
	validCommitExpression := func(value string) bool {
		return strings.HasSuffix(value, "^{commit}") && validCommit(strings.TrimSuffix(value, "^{commit}"))
	}
	validAbsolute := func(value string) bool {
		return filepath.IsAbs(value) && filepath.Clean(value) == value && !strings.HasPrefix(filepath.Base(value), "-")
	}
	validDirectory := directory == "" || validAbsolute(directory)
	if !validDirectory {
		return errors.New("source-audit Git working directory must be a clean absolute path")
	}
	approved := false
	switch arguments[0] {
	case "cat-file":
		approved = len(arguments) == 3 && ((arguments[1] == "-e" &&
			validCommitExpression(arguments[2])) ||
			(arguments[1] == "commit" && validCommit(arguments[2])))
	case "for-each-ref":
		if len(arguments) == 4 {
			approved = arguments[1] == "--format=%(refname)%00%(objectname)%00%(objecttype)%00%(*objectname)%00%(*objecttype)%00" &&
				arguments[2] == "refs/remotes/origin" && arguments[3] == "refs/tags"
		}
		if len(arguments) == 6 {
			approved = arguments[1] == "--contains" && validCommit(arguments[2]) &&
				arguments[3] == "--format=%(refname)%00%(objectname)%00%(objecttype)%00%(*objectname)%00%(*objecttype)%00" &&
				arguments[4] == "refs/remotes/origin" && arguments[5] == "refs/tags"
		}
	case "fsck":
		approved = len(arguments) == 6 && arguments[1] == "--full" &&
			arguments[2] == "--strict" && arguments[3] == "--no-reflogs" &&
			arguments[4] == "--no-progress" && arguments[5] == "--no-dangling"
	case "ls-tree":
		approved = len(arguments) == 4 && arguments[1] == "-r" && arguments[2] == "-z" && validCommit(arguments[3])
	case "rev-parse":
		approved = directory != "" && (len(arguments) == 2 &&
			(arguments[1] == "--show-toplevel" || arguments[1] == "--git-dir" ||
				arguments[1] == "--show-object-format"))
	case "config":
		approved = directory != "" && ((len(arguments) == 5 && arguments[1] == "--local" &&
			arguments[2] == "--no-includes" && arguments[3] == "--null" &&
			arguments[4] == "--list") ||
			(len(arguments) == 6 && arguments[1] == "--local" &&
				arguments[2] == "--no-includes" && arguments[3] == "--null" &&
				arguments[4] == "--get-all" && arguments[5] == "remote.origin.url"))
	}
	if !approved {
		return fmt.Errorf("unapproved source-audit Git invocation: %q", arguments)
	}
	return nil
}

func sourceAuditGitFailure(operation string, result sourceAuditGitResult) error {
	detail := bytes.TrimSpace(result.stderr)
	if len(detail) == 0 {
		detail = bytes.TrimSpace(result.stdout)
	}
	if len(detail) == 0 {
		return fmt.Errorf("%s failed", operation)
	}
	return fmt.Errorf("%s failed: %s", operation, detail)
}

func joinSourceAuditModes(label string, modes []sourceAuditMode) string {
	values := make([]string, 0, len(modes))
	for _, mode := range modes {
		values = append(values, fmt.Sprintf("%s %s (%s)", label, mode.path, mode.mode))
	}
	return strings.Join(values, "; ")
}

func renderSourceAuditReport(
	tasks []sourceAuditTask,
	stateTasks map[sourceAuditState][]sourceAuditTask,
	stateResults map[sourceAuditState]sourceAuditStateResult,
) []byte {
	canonicalByState := make(map[sourceAuditState]string, len(stateResults))
	for _, task := range tasks {
		key := sourceAuditState{task.repository.upstream, task.checkout}
		if _, found := canonicalByState[key]; !found {
			canonicalByState[key] = task.id
		}
	}
	lines := []string{
		"# Tokenbench production source-admission audit",
		"",
		"Audited the final selected model-visible checkout in the canonical 144-record source-selection manifest: code uses the selected base, review uses the selected review head, and explain uses the selected immutable snapshot. Ordinary reachability means reachability from an upstream `refs/heads/*` or `refs/tags/*`; GitHub pull-request-only refs are excluded. The digest runner is the reviewed opaque-gitlink production framing shared by `source.TreeDigest` and `source.TreeDigestFromGitObjects`; selected commit trees and regular blobs are authenticated and digested directly in memory without creating a checkout.",
		"",
		"Symlinks remain rejected. Seastar's `dpdk` entry is admitted only as an opaque gitlink bound to its path, mode, and commit ID; the direct audit never materializes or reads the nested repository. Production worktree verification separately requires an absent or stable empty gitlink directory and rejects initialized content or residual submodule metadata. No tree is sanitized.",
		"",
		"## Mechanical result",
		"",
		"- Task cells: 144 unique IDs; exact 12 repositories x 3 families x 4 tiers coverage: pass.",
		fmt.Sprintf("- Unique repository/commit states: %d; duplicate task cells reuse the first listed state result.", len(stateResults)),
		fmt.Sprintf("- Unique-state outcomes: all %d pass.", len(stateResults)),
		fmt.Sprintf("- Cell outcomes: all %d pass.", len(tasks)),
		"- Every selected state passed reachability, mode, authenticated-object, and production digest gates.",
		"",
		"## Exact 144-cell matrix",
		"",
		"| Task ID | Visible checkout | Ordinary reachability witness | Modes | Production admission | Duplicate-state reuse |",
		"|---|---|---|---|---|---|",
	}
	for _, task := range tasks {
		key := sourceAuditState{task.repository.upstream, task.checkout}
		result := stateResults[key]
		witness := "none"
		if len(result.witnesses) != 0 {
			witness = result.witnesses[0]
		}
		modes := make([]string, 0, len(result.symlinks)+len(result.gitlinks)+len(result.unsupported))
		for _, mode := range result.symlinks {
			modes = append(modes, fmt.Sprintf("`%s` `%s` symlink", sourceAuditByteEscape(mode.path), mode.mode))
		}
		for _, mode := range result.gitlinks {
			modes = append(modes, fmt.Sprintf("`%s` `%s` gitlink", sourceAuditByteEscape(mode.path), mode.mode))
		}
		for _, mode := range result.unsupported {
			modes = append(modes, fmt.Sprintf("`%s` `%s` unsupported", sourceAuditByteEscape(mode.path), mode.mode))
		}
		modeText := "regular blobs only (`100644`/`100755`)"
		if len(modes) != 0 {
			modeText = strings.Join(modes, "; ")
		}
		admission := "PASS `sha256:" + sourceAuditByteEscape(result.detail) + "`"
		reuse := "canonical run"
		if canonicalByState[key] != task.id {
			reuse = "reuse `" + canonicalByState[key] + "`"
		}
		kind := "snapshot"
		switch task.family {
		case "code":
			kind = "base"
		case "review":
			kind = "head"
		}
		lines = append(lines, fmt.Sprintf(
			"| `%s` | `%s` (%s %s) | `%s` | %s | %s | %s |",
			task.id,
			task.checkout,
			task.family,
			kind,
			sourceAuditByteEscape(witness),
			modeText,
			admission,
			reuse,
		))
	}
	lines = append(lines, "", "## Unique-state evidence", "")
	for _, repository := range sourceAuditRepositories {
		lines = append(lines, "### `"+repository.upstream+"`", "")
		keys := make([]sourceAuditState, 0)
		for key := range stateResults {
			if key.upstream == repository.upstream {
				keys = append(keys, key)
			}
		}
		sort.Slice(keys, func(left, right int) bool { return keys[left].commit < keys[right].commit })
		for _, key := range keys {
			result := stateResults[key]
			taskIDs := make([]string, 0, len(stateTasks[key]))
			for _, task := range stateTasks[key] {
				taskIDs = append(taskIDs, "`"+task.id+"`")
			}
			witnesses := "none"
			if len(result.witnesses) != 0 {
				quoted := make([]string, 0, len(result.witnesses))
				for _, witness := range result.witnesses {
					quoted = append(quoted, "`"+sourceAuditByteEscape(witness)+"`")
				}
				witnesses = strings.Join(quoted, ", ")
			}
			lines = append(lines,
				fmt.Sprintf("- `%s` — PASS; cells: %s.", key.commit, strings.Join(taskIDs, ", ")),
				"  Ordinary-ref witnesses: "+witnesses+".",
				"  Evidence: "+sourceAuditByteEscape(result.detail)+".",
			)
		}
		lines = append(lines, "")
	}
	lines = append(lines,
		"## Validation method",
		"",
		"- Strictly decoded the independently checksummed canonical source-selection manifest and required its exact locked 12-repository x 3-family x 4-tier Cartesian product.",
		"- Inspected each unique commit with `git ls-tree -r -z`, preserving dynamic path and ref bytes with reversible hexadecimal escaping and classifying `120000`, `160000`, and all non-regular modes before digest admission.",
		"- Applied strict `git fsck` to locally present object storage, then intersected `git for-each-ref --contains` results by the complete ref name, direct object ID/type, and peeled object ID/type with the exact independently checksummed initial repository-binding inventory; later, altered, local, and `refs/pull/*` refs were never admitted.",
		"- Independently rehashed each selected raw commit object, required its sole first `tree` header to match the root tree reconstructed from rehashed blobs, exact modes, paths, and opaque gitlinks, and rejected any mismatch.",
		"- For admitted states, reconstructed the recursive Git tree in memory, matched it to the authenticated selected commit tree, authenticated every regular blob, and invoked the same production framing used by `source.TreeDigest`; opaque gitlinks bind their modes, paths, and commit IDs without object reads. Shared explain snapshots reuse that exact unique-state result.",
		"- No upstream repository, local Git worktree, GitHub repository, source-selection manifest, or source tree was mutated.",
		"",
	)
	return []byte(strings.Join(lines, "\n"))
}

func sourceAuditByteEscape(value string) string {
	var escaped strings.Builder
	for index := range len(value) {
		character := value[index]
		if character >= 0x20 && character <= 0x7e &&
			character != '\\' && character != '|' && character != '`' {
			escaped.WriteByte(character)
			continue
		}
		fmt.Fprintf(&escaped, "\\x%02x", character)
	}
	return escaped.String()
}
