package taskctl

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	sourceAuditRepositoryBindingSchemaV3        = "scopesifter.source-audit-repository-bindings.v3"
	sourceAuditRepositoryBindingMaximumBytes    = 1 << 20
	sourceAuditRepositoryMaximumMetadataEntries = 500_000
	sourceAuditRepositoryMaximumMetadataBytes   = int64(2 << 30)
)

var sourceAuditRepositoryRefName = regexp.MustCompile(`^refs/[A-Za-z0-9][A-Za-z0-9._/-]*$`)

// sourceAuditRepositoryBindingDocumentV3 binds the authenticated Git identity
// and each audited upstream name to
// both a local repository identity and the exact ordinary refs that were
// admitted when the binding was written. The inventory digest is SHA-256 over
// records sorted bytewise by refname and encoded without a header as:
//
//	refname NUL object-id NUL object-type NUL peeled-object-id NUL peeled-object-type NUL
//
// Ordinary refs are refs/remotes/origin/* (including origin/HEAD) and
// refs/tags/*. Object IDs are full lowercase SHA-1 values. The object type is
// the type of the object named directly by the ref, so annotated tags retain
// type "tag" and bind the exact commit obtained by peeling the tag. Direct
// commit refs encode empty peeled fields.
type sourceAuditRepositoryBindingDocumentV3 struct {
	Schema        string                           `json:"schema"`
	GitExecutable string                           `json:"git_executable"`
	GitSHA256     string                           `json:"git_sha256"`
	Repositories  []sourceAuditRepositoryBindingV3 `json:"repositories"`
}

type sourceAuditRepositoryBindingV3 struct {
	Upstream                   string `json:"upstream"`
	Path                       string `json:"path"`
	Origin                     string `json:"origin"`
	OrdinaryRefInventorySHA256 string `json:"ordinary_ref_inventory_sha256"`
}

// SourceAuditRepositoryBindingInput names one expected upstream repository and
// its canonical absolute local path. BuildSourceAuditRepositoryBindings
// requires exactly one input for each of the 12 source-audit repositories.
type SourceAuditRepositoryBindingInput struct {
	Upstream string
	Path     string
}

// sourceAuditRepositoryBindingSet is the fully validated, immutable admission
// view used by one source audit. ordinaryRefs contains the exact records whose
// canonical digest matched the binding document during initial validation;
// witness checks must use these retained records instead of re-reading refs.
type sourceAuditRepositoryBindingSet struct {
	paths        map[string]string
	pathInfos    map[string]os.FileInfo
	ordinaryRefs map[string][]sourceAuditOrdinaryRef
	git          sourceAuditGitRunner
}

func sameSourceAuditRepositoryBindingSet(
	left, right sourceAuditRepositoryBindingSet,
) bool {
	if left.git.identity != right.git.identity || len(left.paths) != len(right.paths) ||
		len(left.pathInfos) != len(right.pathInfos) ||
		len(left.ordinaryRefs) != len(right.ordinaryRefs) {
		return false
	}
	for upstream, leftPath := range left.paths {
		if right.paths[upstream] != leftPath {
			return false
		}
		leftInfo, leftFound := left.pathInfos[upstream]
		rightInfo, rightFound := right.pathInfos[upstream]
		if !leftFound || !rightFound || !os.SameFile(leftInfo, rightInfo) ||
			leftInfo.Mode() != rightInfo.Mode() || leftInfo.ModTime() != rightInfo.ModTime() {
			return false
		}
		leftRefs, found := left.ordinaryRefs[upstream]
		if !found {
			return false
		}
		rightRefs, found := right.ordinaryRefs[upstream]
		if !found || len(leftRefs) != len(rightRefs) {
			return false
		}
		for index := range leftRefs {
			if leftRefs[index] != rightRefs[index] {
				return false
			}
		}
	}
	return true
}

type sourceAuditRepositoryPathIdentity struct {
	info     os.FileInfo
	upstream string
	path     string
}

type sourceAuditRepositoryBindingQuery uint8

const (
	sourceAuditRepositoryTopLevel sourceAuditRepositoryBindingQuery = iota + 1
	sourceAuditRepositoryGitDirectory
	sourceAuditRepositoryObjectFormat
	sourceAuditRepositoryConfiguration
	sourceAuditRepositoryOrigin
	sourceAuditRepositoryOrdinaryRefs
)

// sourceAuditRepositoryBindingProbe deliberately exposes a fixed query enum,
// not caller-selected Git arguments. This keeps the repository binding parser
// hermetic in tests and preserves a closed native-Git grammar in production.
type sourceAuditRepositoryBindingProbe interface {
	query(
		context.Context,
		string,
		os.FileInfo,
		sourceAuditRepositoryBindingQuery,
	) ([]byte, error)
}

type nativeSourceAuditRepositoryBindingProbe struct {
	git sourceAuditGitRunner
}

func verifySourceAuditRepositoryObjectDatabase(
	ctx context.Context,
	git sourceAuditGitRunner,
	repositoryPath string,
	repositoryInfo os.FileInfo,
) error {
	result, err := git.execute(
		ctx,
		repositoryPath,
		repositoryInfo,
		io.Discard,
		"fsck", "--full", "--strict", "--no-reflogs", "--no-progress", "--no-dangling",
	)
	if err != nil {
		return err
	}
	if !result.success {
		return sourceAuditGitFailure("verify repository object database", result)
	}
	return nil
}

func (probe nativeSourceAuditRepositoryBindingProbe) query(
	ctx context.Context,
	root string,
	rootInfo os.FileInfo,
	query sourceAuditRepositoryBindingQuery,
) ([]byte, error) {
	var operation string
	var arguments []string
	switch query {
	case sourceAuditRepositoryTopLevel:
		operation = "resolve repository top level"
		arguments = []string{"rev-parse", "--show-toplevel"}
	case sourceAuditRepositoryGitDirectory:
		operation = "resolve repository Git directory"
		arguments = []string{"rev-parse", "--git-dir"}
	case sourceAuditRepositoryObjectFormat:
		operation = "resolve repository object format"
		arguments = []string{"rev-parse", "--show-object-format"}
	case sourceAuditRepositoryConfiguration:
		operation = "read repository configuration"
		arguments = []string{"config", "--local", "--no-includes", "--null", "--list"}
	case sourceAuditRepositoryOrigin:
		operation = "read repository origin"
		arguments = []string{
			"config", "--local", "--no-includes", "--null", "--get-all", "remote.origin.url",
		}
	case sourceAuditRepositoryOrdinaryRefs:
		operation = "read ordinary repository refs"
		arguments = []string{
			"for-each-ref",
			"--format=%(refname)%00%(objectname)%00%(objecttype)%00%(*objectname)%00%(*objecttype)%00",
			"refs/remotes/origin",
			"refs/tags",
		}
	default:
		return nil, errors.New("unknown source-audit repository query")
	}
	return probe.git.requiredOutput(ctx, root, rootInfo, operation, arguments...)
}

// BuildSourceAuditRepositoryBindings returns the canonical v3 repository
// binding document. Input order is irrelevant. The local repositories are
// read only through the same closed native-Git probe and fail-closed identity
// validation used when a binding document is consumed.
func BuildSourceAuditRepositoryBindings(
	ctx context.Context,
	inputs []SourceAuditRepositoryBindingInput,
	gitExecutable string,
	gitSHA256 string,
) ([]byte, error) {
	git, err := newSourceAuditGitRunner(sourceAuditGitIdentity{
		executable: gitExecutable,
		sha256:     gitSHA256,
	})
	if err != nil {
		return nil, fmt.Errorf("authenticate source-audit Git: %w", err)
	}
	first, err := buildSourceAuditRepositoryBindings(
		ctx,
		inputs,
		git.identity,
		nativeSourceAuditRepositoryBindingProbe{git: git},
	)
	if err != nil {
		return nil, err
	}
	validated, err := sourceAuditRepositoryBindingsFromBytes(ctx, first, git.identity)
	if err != nil {
		return nil, fmt.Errorf("revalidate generated repository bindings: %w", err)
	}
	for _, repository := range sourceAuditRepositories {
		if err := verifySourceAuditRepositoryObjectDatabase(
			ctx,
			git,
			validated.paths[repository.upstream],
			validated.pathInfos[repository.upstream],
		); err != nil {
			return nil, fmt.Errorf("verify %s object database: %w", repository.upstream, err)
		}
	}
	second, err := buildSourceAuditRepositoryBindings(
		ctx,
		inputs,
		git.identity,
		nativeSourceAuditRepositoryBindingProbe{git: git},
	)
	if err != nil {
		return nil, fmt.Errorf("reinspect repository bindings after object verification: %w", err)
	}
	if !bytes.Equal(first, second) {
		return nil, errors.New("repository bindings changed during object verification")
	}
	return second, nil
}

func buildSourceAuditRepositoryBindings(
	ctx context.Context,
	inputs []SourceAuditRepositoryBindingInput,
	gitIdentity sourceAuditGitIdentity,
	probe sourceAuditRepositoryBindingProbe,
) ([]byte, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if probe == nil {
		return nil, errors.New("source-audit repository probe is required")
	}
	if err := validateSourceAuditGitIdentity(gitIdentity); err != nil {
		return nil, err
	}
	indexed, err := indexSourceAuditRepositoryBindingInputs(inputs)
	if err != nil {
		return nil, err
	}

	document := sourceAuditRepositoryBindingDocumentV3{
		Schema:        sourceAuditRepositoryBindingSchemaV3,
		GitExecutable: gitIdentity.executable,
		GitSHA256:     gitIdentity.sha256,
		Repositories:  make([]sourceAuditRepositoryBindingV3, 0, len(sourceAuditRepositories)),
	}
	identities := make([]sourceAuditRepositoryPathIdentity, 0, len(sourceAuditRepositories))
	for _, repository := range sourceAuditRepositories {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		root, rootInfo, err := validateSourceAuditRepositoryBindingPath(
			repository.upstream,
			indexed[repository.upstream],
		)
		if err != nil {
			return nil, err
		}
		if err := rejectSourceAuditRepositoryIdentityOverlap(
			identities,
			repository.upstream,
			root,
			rootInfo,
		); err != nil {
			return nil, err
		}
		identities = append(
			identities,
			sourceAuditRepositoryPathIdentity{
				info: rootInfo, upstream: repository.upstream, path: root,
			},
		)
		inspection, err := inspectSourceAuditRepositoryBindingIdentity(
			ctx,
			probe,
			repository,
			root,
			rootInfo,
		)
		if err != nil {
			return nil, err
		}
		document.Repositories = append(document.Repositories, sourceAuditRepositoryBindingV3{
			Upstream:                   repository.upstream,
			Path:                       root,
			Origin:                     repository.originURL(),
			OrdinaryRefInventorySHA256: inspection.inventorySHA256,
		})
	}
	encoded, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("encode repository bindings: %w", err)
	}
	encoded = append(encoded, '\n')
	if len(encoded) > sourceAuditRepositoryBindingMaximumBytes {
		return nil, fmt.Errorf(
			"repository binding document exceeds %d bytes",
			sourceAuditRepositoryBindingMaximumBytes,
		)
	}
	return encoded, nil
}

func indexSourceAuditRepositoryBindingInputs(
	inputs []SourceAuditRepositoryBindingInput,
) (map[string]string, error) {
	expected := make(map[string]struct{}, len(sourceAuditRepositories))
	for _, repository := range sourceAuditRepositories {
		expected[repository.upstream] = struct{}{}
	}
	indexed := make(map[string]string, len(inputs))
	for _, input := range inputs {
		if _, found := expected[input.Upstream]; !found {
			return nil, fmt.Errorf(
				"repository binding inputs contain unknown upstream %q",
				input.Upstream,
			)
		}
		if _, duplicate := indexed[input.Upstream]; duplicate {
			return nil, fmt.Errorf(
				"repository binding inputs repeat upstream %q",
				input.Upstream,
			)
		}
		indexed[input.Upstream] = input.Path
	}
	missing := make([]string, 0)
	for _, repository := range sourceAuditRepositories {
		if _, found := indexed[repository.upstream]; !found {
			missing = append(missing, repository.upstream)
		}
	}
	if len(missing) != 0 {
		return nil, fmt.Errorf(
			"repository binding inputs are missing %s",
			strings.Join(missing, ", "),
		)
	}
	if len(inputs) != len(sourceAuditRepositories) {
		return nil, fmt.Errorf(
			"repository binding inputs contain %d repositories, want exactly %d",
			len(inputs),
			len(sourceAuditRepositories),
		)
	}
	return indexed, nil
}

func sourceAuditRepositoryBindingsFromBytes(
	ctx context.Context,
	data []byte,
	expectedGit sourceAuditGitIdentity,
) (sourceAuditRepositoryBindingSet, error) {
	document, err := decodeSourceAuditRepositoryBindingDocument(data)
	if err != nil {
		return sourceAuditRepositoryBindingSet{}, fmt.Errorf("validate repository bindings: %w", err)
	}
	if err := validateSourceAuditGitIdentity(expectedGit); err != nil {
		return sourceAuditRepositoryBindingSet{}, fmt.Errorf("expected repository-binding Git: %w", err)
	}
	if document.GitExecutable != expectedGit.executable || document.GitSHA256 != expectedGit.sha256 {
		return sourceAuditRepositoryBindingSet{}, fmt.Errorf(
			"repository-binding Git identity is %s %s, want independently supplied %s %s",
			document.GitExecutable,
			document.GitSHA256,
			expectedGit.executable,
			expectedGit.sha256,
		)
	}
	git, err := newSourceAuditGitRunner(expectedGit)
	if err != nil {
		return sourceAuditRepositoryBindingSet{}, fmt.Errorf(
			"authenticate repository-binding Git: %w",
			err,
		)
	}
	bindings, err := validateSourceAuditRepositoryBindings(
		ctx,
		data,
		expectedGit,
		nativeSourceAuditRepositoryBindingProbe{git: git},
	)
	if err != nil {
		return sourceAuditRepositoryBindingSet{}, fmt.Errorf("validate repository bindings: %w", err)
	}
	bindings.git = git
	return bindings, nil
}

func validateSourceAuditRepositoryBindings(
	ctx context.Context,
	data []byte,
	expectedGit sourceAuditGitIdentity,
	probe sourceAuditRepositoryBindingProbe,
) (sourceAuditRepositoryBindingSet, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return sourceAuditRepositoryBindingSet{}, err
	}
	if probe == nil {
		return sourceAuditRepositoryBindingSet{}, errors.New("source-audit repository probe is required")
	}
	if len(data) > sourceAuditRepositoryBindingMaximumBytes {
		return sourceAuditRepositoryBindingSet{}, fmt.Errorf(
			"repository binding document exceeds %d bytes",
			sourceAuditRepositoryBindingMaximumBytes,
		)
	}
	document, err := decodeSourceAuditRepositoryBindingDocument(data)
	if err != nil {
		return sourceAuditRepositoryBindingSet{}, err
	}
	if document.Schema != sourceAuditRepositoryBindingSchemaV3 {
		return sourceAuditRepositoryBindingSet{}, fmt.Errorf(
			"repository binding schema is %q, want %q",
			document.Schema,
			sourceAuditRepositoryBindingSchemaV3,
		)
	}
	gitIdentity := sourceAuditGitIdentity{
		executable: document.GitExecutable,
		sha256:     document.GitSHA256,
	}
	if err := validateSourceAuditGitIdentity(expectedGit); err != nil {
		return sourceAuditRepositoryBindingSet{}, fmt.Errorf("expected repository binding Git identity: %w", err)
	}
	if err := validateSourceAuditGitIdentity(gitIdentity); err != nil {
		return sourceAuditRepositoryBindingSet{}, fmt.Errorf("repository binding Git identity: %w", err)
	}
	if gitIdentity != expectedGit {
		return sourceAuditRepositoryBindingSet{}, fmt.Errorf(
			"repository binding Git identity is %s %s, want %s %s",
			gitIdentity.executable,
			gitIdentity.sha256,
			expectedGit.executable,
			expectedGit.sha256,
		)
	}
	if len(document.Repositories) != len(sourceAuditRepositories) {
		return sourceAuditRepositoryBindingSet{}, fmt.Errorf(
			"repository bindings contain %d repositories, want exactly %d",
			len(document.Repositories),
			len(sourceAuditRepositories),
		)
	}

	expected := make(map[string]sourceAuditRepository, len(sourceAuditRepositories))
	for _, repository := range sourceAuditRepositories {
		if _, duplicate := expected[repository.upstream]; duplicate {
			return sourceAuditRepositoryBindingSet{}, fmt.Errorf("duplicate expected repository %q", repository.upstream)
		}
		expected[repository.upstream] = repository
	}
	bindings := make(map[string]sourceAuditRepositoryBindingV3, len(document.Repositories))
	for _, binding := range document.Repositories {
		if _, found := expected[binding.Upstream]; !found {
			return sourceAuditRepositoryBindingSet{}, fmt.Errorf("repository bindings contain unknown upstream %q", binding.Upstream)
		}
		if _, duplicate := bindings[binding.Upstream]; duplicate {
			return sourceAuditRepositoryBindingSet{}, fmt.Errorf("repository bindings repeat upstream %q", binding.Upstream)
		}
		bindings[binding.Upstream] = binding
	}
	for index, repository := range sourceAuditRepositories {
		if document.Repositories[index].Upstream != repository.upstream {
			return sourceAuditRepositoryBindingSet{}, fmt.Errorf(
				"repository bindings are not in locked repository order at index %d: got %q, want %q",
				index,
				document.Repositories[index].Upstream,
				repository.upstream,
			)
		}
	}

	paths := make(map[string]string, len(sourceAuditRepositories))
	pathInfos := make(map[string]os.FileInfo, len(sourceAuditRepositories))
	ordinaryRefs := make(map[string][]sourceAuditOrdinaryRef, len(sourceAuditRepositories))
	identities := make([]sourceAuditRepositoryPathIdentity, 0, len(sourceAuditRepositories))
	for _, repository := range sourceAuditRepositories {
		if err := ctx.Err(); err != nil {
			return sourceAuditRepositoryBindingSet{}, err
		}
		binding, found := bindings[repository.upstream]
		if !found {
			return sourceAuditRepositoryBindingSet{}, fmt.Errorf("repository bindings are missing %s", repository.upstream)
		}
		if binding.Origin != repository.originURL() {
			return sourceAuditRepositoryBindingSet{}, fmt.Errorf(
				"%s binding origin is %q, want %q",
				repository.upstream,
				binding.Origin,
				repository.originURL(),
			)
		}
		if !sourceAuditDigest.MatchString(binding.OrdinaryRefInventorySHA256) {
			return sourceAuditRepositoryBindingSet{}, fmt.Errorf(
				"%s ordinary-ref inventory digest is not lowercase SHA-256",
				repository.upstream,
			)
		}

		root, rootInfo, err := validateSourceAuditRepositoryBindingPath(
			repository.upstream,
			binding.Path,
		)
		if err != nil {
			return sourceAuditRepositoryBindingSet{}, err
		}
		if err := rejectSourceAuditRepositoryIdentityOverlap(
			identities,
			repository.upstream,
			root,
			rootInfo,
		); err != nil {
			return sourceAuditRepositoryBindingSet{}, err
		}
		identities = append(
			identities,
			sourceAuditRepositoryPathIdentity{
				info: rootInfo, upstream: repository.upstream, path: root,
			},
		)
		refs, err := validateSourceAuditRepositoryBindingIdentity(
			ctx,
			probe,
			repository,
			root,
			rootInfo,
			binding.OrdinaryRefInventorySHA256,
		)
		if err != nil {
			return sourceAuditRepositoryBindingSet{}, err
		}
		paths[repository.upstream] = root
		pathInfos[repository.upstream] = rootInfo
		ordinaryRefs[repository.upstream] = refs
	}
	return sourceAuditRepositoryBindingSet{
		paths: paths, pathInfos: pathInfos, ordinaryRefs: ordinaryRefs,
		git: sourceAuditGitRunner{identity: gitIdentity},
	}, nil
}

func validateSourceAuditRepositoryBindingPath(
	upstream string,
	path string,
) (string, os.FileInfo, error) {
	if path == "" || !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return "", nil, fmt.Errorf(
			"%s repository path must be a canonical absolute path",
			upstream,
		)
	}
	if filepath.Dir(path) == path {
		return "", nil, fmt.Errorf("%s repository path must not be a filesystem root", upstream)
	}
	rootInfo, err := os.Lstat(path)
	if err != nil {
		return "", nil, fmt.Errorf("inspect %s repository path: %w", upstream, err)
	}
	if !rootInfo.IsDir() || rootInfo.Mode()&os.ModeSymlink != 0 {
		return "", nil, fmt.Errorf("%s repository path is not a real directory", upstream)
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil {
		return "", nil, fmt.Errorf("resolve %s repository path: %w", upstream, err)
	}
	if resolved != path {
		return "", nil, fmt.Errorf("%s repository path traverses a symlink", upstream)
	}

	gitDirectory := filepath.Join(path, ".git")
	gitInfo, err := os.Lstat(gitDirectory)
	if err != nil {
		return "", nil, fmt.Errorf("inspect %s standalone .git directory: %w", upstream, err)
	}
	if !gitInfo.IsDir() || gitInfo.Mode()&os.ModeSymlink != 0 {
		return "", nil, fmt.Errorf("%s repository does not have a standalone .git directory", upstream)
	}
	resolvedGit, err := filepath.EvalSymlinks(gitDirectory)
	if err != nil {
		return "", nil, fmt.Errorf("resolve %s .git directory: %w", upstream, err)
	}
	if resolvedGit != gitDirectory {
		return "", nil, fmt.Errorf("%s .git directory traverses a symlink", upstream)
	}
	if err := validateSourceAuditRepositoryMetadataTree(gitDirectory); err != nil {
		return "", nil, fmt.Errorf("%s repository: %w", upstream, err)
	}
	objectsDirectory := filepath.Join(gitDirectory, "objects")
	objectsInfo, err := os.Lstat(objectsDirectory)
	if err != nil {
		return "", nil, fmt.Errorf("inspect %s Git object directory: %w", upstream, err)
	}
	if !objectsInfo.IsDir() || objectsInfo.Mode()&os.ModeSymlink != 0 {
		return "", nil, fmt.Errorf("%s Git object directory is not a real directory", upstream)
	}
	resolvedObjects, err := filepath.EvalSymlinks(objectsDirectory)
	if err != nil {
		return "", nil, fmt.Errorf("resolve %s Git object directory: %w", upstream, err)
	}
	if resolvedObjects != objectsDirectory {
		return "", nil, fmt.Errorf("%s Git object directory traverses a symlink", upstream)
	}
	objectsInformation := filepath.Join(objectsDirectory, "info")
	if information, err := os.Lstat(objectsInformation); err == nil {
		if !information.IsDir() || information.Mode()&os.ModeSymlink != 0 {
			return "", nil, fmt.Errorf("%s Git object information path is not a real directory", upstream)
		}
		resolvedInformation, resolveErr := filepath.EvalSymlinks(objectsInformation)
		if resolveErr != nil {
			return "", nil, fmt.Errorf("resolve %s Git object information directory: %w", upstream, resolveErr)
		}
		if resolvedInformation != objectsInformation {
			return "", nil, fmt.Errorf("%s Git object information directory traverses a symlink", upstream)
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", nil, fmt.Errorf("inspect %s Git object information directory: %w", upstream, err)
	}
	commonDirectory := filepath.Join(gitDirectory, "commondir")
	if _, err := os.Lstat(commonDirectory); err == nil {
		return "", nil, fmt.Errorf("%s repository uses a Git common directory", upstream)
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", nil, fmt.Errorf("inspect %s Git common directory: %w", upstream, err)
	}
	for _, relative := range []string{
		filepath.Join("objects", "info", "alternates"),
		filepath.Join("objects", "info", "http-alternates"),
		filepath.Join("info", "grafts"),
		"shallow",
	} {
		if err := rejectSourceAuditRepositoryAlternate(gitDirectory, relative); err != nil {
			return "", nil, fmt.Errorf("%s repository: %w", upstream, err)
		}
	}
	return path, rootInfo, nil
}

func validateSourceAuditRepositoryMetadataTree(gitDirectory string) error {
	entries := 0
	var totalBytes int64
	return filepath.WalkDir(gitDirectory, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		entries++
		if entries > sourceAuditRepositoryMaximumMetadataEntries {
			return fmt.Errorf("git metadata exceeds %d entries", sourceAuditRepositoryMaximumMetadataEntries)
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("git metadata contains symlink %s", path)
		}
		if !entry.IsDir() && !entry.Type().IsRegular() {
			return fmt.Errorf("git metadata contains nonregular path %s", path)
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !sourceAuditFileHasOneLink(info) {
			return fmt.Errorf("git metadata file must have exactly one filesystem link: %s", path)
		}
		if info.Size() < 0 || info.Size() > sourceAuditMaximumBlobBytes {
			return fmt.Errorf("git metadata file exceeds %d bytes: %s", sourceAuditMaximumBlobBytes, path)
		}
		if totalBytes > sourceAuditRepositoryMaximumMetadataBytes-info.Size() {
			return fmt.Errorf("git metadata exceeds %d bytes", sourceAuditRepositoryMaximumMetadataBytes)
		}
		totalBytes += info.Size()
		return nil
	})
}

func rejectSourceAuditRepositoryIdentityOverlap(
	identities []sourceAuditRepositoryPathIdentity,
	upstream, root string,
	rootInfo os.FileInfo,
) error {
	for _, previous := range identities {
		if os.SameFile(previous.info, rootInfo) {
			return fmt.Errorf(
				"repository bindings alias %s and %s",
				previous.upstream,
				upstream,
			)
		}
		if sourceAuditPathsOverlap(previous.path, root) {
			return fmt.Errorf(
				"repository bindings overlap %s at %s and %s at %s",
				previous.upstream,
				previous.path,
				upstream,
				root,
			)
		}
	}
	return nil
}

func rejectSourceAuditRepositoryAlternate(gitDirectory, relative string) error {
	path := filepath.Join(gitDirectory, relative)
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect Git %s: %w", filepath.ToSlash(relative), err)
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("git %s is not a regular file", filepath.ToSlash(relative))
	}
	if info.Size() < 0 || info.Size() > 1<<20 {
		return fmt.Errorf("git %s exceeds its size bound", filepath.ToSlash(relative))
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read Git %s: %w", filepath.ToSlash(relative), err)
	}
	if len(bytes.TrimSpace(content)) != 0 {
		return fmt.Errorf("git local metadata override is not allowed in %s", filepath.ToSlash(relative))
	}
	return nil
}

func validateSourceAuditRepositoryBindingIdentity(
	ctx context.Context,
	probe sourceAuditRepositoryBindingProbe,
	repository sourceAuditRepository,
	root string,
	rootInfo os.FileInfo,
	expectedInventoryDigest string,
) ([]sourceAuditOrdinaryRef, error) {
	inspection, err := inspectSourceAuditRepositoryBindingIdentity(
		ctx,
		probe,
		repository,
		root,
		rootInfo,
	)
	if err != nil {
		return nil, err
	}
	if inspection.inventorySHA256 != expectedInventoryDigest {
		return nil, fmt.Errorf(
			"%s ordinary-ref inventory SHA-256 is %s, want %s",
			repository.upstream,
			inspection.inventorySHA256,
			expectedInventoryDigest,
		)
	}
	return append([]sourceAuditOrdinaryRef(nil), inspection.ordinaryRefs...), nil
}

type sourceAuditRepositoryBindingInspection struct {
	inventorySHA256 string
	ordinaryRefs    []sourceAuditOrdinaryRef
}

func inspectSourceAuditRepositoryBindingIdentity(
	ctx context.Context,
	probe sourceAuditRepositoryBindingProbe,
	repository sourceAuditRepository,
	root string,
	rootInfo os.FileInfo,
) (sourceAuditRepositoryBindingInspection, error) {
	query := func(kind sourceAuditRepositoryBindingQuery, label string) ([]byte, error) {
		output, err := probe.query(ctx, root, rootInfo, kind)
		if err != nil {
			return nil, fmt.Errorf("%s: %s: %w", repository.upstream, label, err)
		}
		return output, nil
	}

	topLevelOutput, err := query(sourceAuditRepositoryTopLevel, "resolve repository top level")
	if err != nil {
		return sourceAuditRepositoryBindingInspection{}, err
	}
	topLevel, err := parseSourceAuditRepositoryLine(topLevelOutput)
	if err != nil {
		return sourceAuditRepositoryBindingInspection{}, fmt.Errorf("%s repository top level: %w", repository.upstream, err)
	}
	if topLevel != root {
		return sourceAuditRepositoryBindingInspection{}, fmt.Errorf("%s path is not the exact Git top level", repository.upstream)
	}

	gitDirectoryOutput, err := query(
		sourceAuditRepositoryGitDirectory,
		"resolve repository Git directory",
	)
	if err != nil {
		return sourceAuditRepositoryBindingInspection{}, err
	}
	gitDirectory, err := parseSourceAuditRepositoryLine(gitDirectoryOutput)
	if err != nil {
		return sourceAuditRepositoryBindingInspection{}, fmt.Errorf("%s repository Git directory: %w", repository.upstream, err)
	}
	if !filepath.IsAbs(gitDirectory) {
		gitDirectory = filepath.Join(root, gitDirectory)
	}
	if filepath.Clean(gitDirectory) != filepath.Join(root, ".git") {
		return sourceAuditRepositoryBindingInspection{}, fmt.Errorf("%s does not use its standalone .git directory", repository.upstream)
	}
	resolvedGitDirectory, err := filepath.EvalSymlinks(gitDirectory)
	if err != nil {
		return sourceAuditRepositoryBindingInspection{}, fmt.Errorf("resolve %s reported Git directory: %w", repository.upstream, err)
	}
	if resolvedGitDirectory != filepath.Join(root, ".git") {
		return sourceAuditRepositoryBindingInspection{}, fmt.Errorf("%s reported Git directory traverses a symlink", repository.upstream)
	}

	objectFormatOutput, err := query(
		sourceAuditRepositoryObjectFormat,
		"resolve repository object format",
	)
	if err != nil {
		return sourceAuditRepositoryBindingInspection{}, err
	}
	objectFormat, err := parseSourceAuditRepositoryLine(objectFormatOutput)
	if err != nil {
		return sourceAuditRepositoryBindingInspection{}, fmt.Errorf("%s repository object format: %w", repository.upstream, err)
	}
	if objectFormat != "sha1" {
		return sourceAuditRepositoryBindingInspection{}, fmt.Errorf("%s must use the locked SHA-1 object format", repository.upstream)
	}

	configuration, err := query(
		sourceAuditRepositoryConfiguration,
		"read repository configuration",
	)
	if err != nil {
		return sourceAuditRepositoryBindingInspection{}, err
	}
	if err := validateSourceAuditRepositoryConfig(configuration); err != nil {
		return sourceAuditRepositoryBindingInspection{}, fmt.Errorf("%s repository configuration: %w", repository.upstream, err)
	}
	if err := validateSourceAuditRepositoryPartialCloneConfig(configuration); err != nil {
		return sourceAuditRepositoryBindingInspection{}, fmt.Errorf("%s repository configuration: %w", repository.upstream, err)
	}

	originOutput, err := query(sourceAuditRepositoryOrigin, "read repository origin")
	if err != nil {
		return sourceAuditRepositoryBindingInspection{}, err
	}
	origin, err := parseSourceAuditRepositoryNULValue(originOutput)
	if err != nil {
		return sourceAuditRepositoryBindingInspection{}, fmt.Errorf("%s repository origin: %w", repository.upstream, err)
	}
	if origin != repository.originURL() {
		return sourceAuditRepositoryBindingInspection{}, fmt.Errorf(
			"%s origin is %q, want %q",
			repository.upstream,
			origin,
			repository.originURL(),
		)
	}

	ordinaryRefs, err := query(sourceAuditRepositoryOrdinaryRefs, "read ordinary repository refs")
	if err != nil {
		return sourceAuditRepositoryBindingInspection{}, err
	}
	parsedRefs, canonical, err := parseSourceAuditOrdinaryRefInventory(ordinaryRefs)
	if err != nil {
		return sourceAuditRepositoryBindingInspection{}, fmt.Errorf("%s ordinary-ref inventory: %w", repository.upstream, err)
	}
	digest := sha256.Sum256(canonical)
	return sourceAuditRepositoryBindingInspection{
		inventorySHA256: hex.EncodeToString(digest[:]),
		ordinaryRefs:    parsedRefs,
	}, nil
}

func parseSourceAuditRepositoryLine(output []byte) (string, error) {
	if len(output) == 0 {
		return "", errors.New("output is empty")
	}
	if output[len(output)-1] != '\n' {
		return "", errors.New("output is not newline terminated")
	}
	value := output[:len(output)-1]
	if len(value) == 0 || bytes.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("output is not exactly one nonempty line")
	}
	return string(value), nil
}

func parseSourceAuditRepositoryNULValue(output []byte) (string, error) {
	if len(output) < 2 || output[len(output)-1] != 0 {
		return "", errors.New("output is not NUL terminated")
	}
	value := output[:len(output)-1]
	if len(value) == 0 || bytes.ContainsAny(value, "\x00\r\n") {
		return "", errors.New("output does not contain exactly one nonempty value")
	}
	return string(value), nil
}

func validateSourceAuditRepositoryPartialCloneConfig(configuration []byte) error {
	var promisor, filter []string
	for _, record := range bytes.Split(configuration, []byte{0}) {
		if len(record) == 0 {
			continue
		}
		separator := bytes.IndexByte(record, '\n')
		if separator <= 0 || separator == len(record)-1 {
			return errors.New("repository Git partial-clone configuration is malformed")
		}
		name := strings.ToLower(string(record[:separator]))
		value := string(record[separator+1:])
		switch name {
		case "remote.origin.promisor":
			promisor = append(promisor, value)
		case "remote.origin.partialclonefilter":
			filter = append(filter, value)
		}
	}
	if len(promisor) == 0 && len(filter) == 0 {
		return nil
	}
	if len(promisor) != 1 || len(filter) != 1 {
		return errors.New(
			"repository Git partial-clone configuration must contain exactly one promisor/filter pair",
		)
	}
	if promisor[0] != "true" || filter[0] != "blob:none" {
		return errors.New(
			"repository Git partial-clone configuration must be exactly promisor=true and filter=blob:none",
		)
	}
	return nil
}

type sourceAuditOrdinaryRef struct {
	name       string
	objectID   string
	objectType string
	commitID   string
}

func sourceAuditOrdinaryRefInventorySHA256(output []byte) (string, error) {
	_, canonical, err := parseSourceAuditOrdinaryRefInventory(output)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(canonical)
	return hex.EncodeToString(digest[:]), nil
}

func canonicalSourceAuditOrdinaryRefInventory(output []byte) ([]byte, error) {
	_, canonical, err := parseSourceAuditOrdinaryRefInventory(output)
	return canonical, err
}

func parseSourceAuditOrdinaryRefInventory(
	output []byte,
) ([]sourceAuditOrdinaryRef, []byte, error) {
	if len(output) == 0 {
		return nil, nil, errors.New("ordinary-ref inventory is empty")
	}
	if len(output) > sourceAuditMaximumOutput {
		return nil, nil, fmt.Errorf("ordinary-ref inventory exceeds %d bytes", sourceAuditMaximumOutput)
	}
	if output[len(output)-1] != '\n' {
		return nil, nil, errors.New("ordinary-ref inventory is not newline terminated")
	}
	lines := bytes.Split(output[:len(output)-1], []byte{'\n'})
	if len(lines) > sourceAuditMaximumEntries {
		return nil, nil, fmt.Errorf(
			"ordinary-ref inventory exceeds %d entries",
			sourceAuditMaximumEntries,
		)
	}
	refs := make([]sourceAuditOrdinaryRef, 0, len(lines))
	seen := make(map[string]struct{}, len(lines))
	for _, line := range lines {
		fields := bytes.Split(line, []byte{0})
		if len(fields) != 6 || len(fields[5]) != 0 {
			return nil, nil, errors.New("ordinary-ref record is not five NUL-terminated fields")
		}
		name := string(fields[0])
		objectID := string(fields[1])
		objectType := string(fields[2])
		peeledObjectID := string(fields[3])
		peeledObjectType := string(fields[4])
		if len(name) > sourceAuditMaximumPathSize ||
			!validSourceAuditRepositoryRefName(name) {
			return nil, nil, fmt.Errorf("ordinary-ref name %q is not canonical", name)
		}
		if !strings.HasPrefix(name, "refs/remotes/origin/") &&
			!strings.HasPrefix(name, "refs/tags/") {
			return nil, nil, fmt.Errorf("ordinary-ref name %q is outside origin heads and tags", name)
		}
		if name == "refs/remotes/origin/" || name == "refs/tags/" {
			return nil, nil, fmt.Errorf("ordinary-ref name %q has no ref suffix", name)
		}
		if !commitPattern.MatchString(objectID) {
			return nil, nil, fmt.Errorf("ordinary-ref %q has a non-SHA-1 object ID", name)
		}
		switch objectType {
		case "blob", "commit", "tag", "tree":
		default:
			return nil, nil, fmt.Errorf("ordinary-ref %q has invalid object type %q", name, objectType)
		}
		if strings.HasPrefix(name, "refs/remotes/origin/") && objectType != "commit" {
			return nil, nil, fmt.Errorf("remote head %q names a %s, want commit", name, objectType)
		}
		commitID := objectID
		switch objectType {
		case "commit":
			if peeledObjectID != "" || peeledObjectType != "" {
				return nil, nil, fmt.Errorf("ordinary-ref %q has unexpected peeled fields", name)
			}
		case "tag":
			if !commitPattern.MatchString(peeledObjectID) || peeledObjectType != "commit" {
				return nil, nil, fmt.Errorf("annotated tag %q does not peel directly to a commit", name)
			}
			commitID = peeledObjectID
		default:
			return nil, nil, fmt.Errorf("tag ref %q names a %s, want commit or annotated commit tag", name, objectType)
		}
		if _, duplicate := seen[name]; duplicate {
			return nil, nil, fmt.Errorf("ordinary-ref inventory repeats %q", name)
		}
		seen[name] = struct{}{}
		refs = append(refs, sourceAuditOrdinaryRef{name, objectID, objectType, commitID})
	}
	sort.Slice(refs, func(left, right int) bool {
		return refs[left].name < refs[right].name
	})
	var canonical bytes.Buffer
	canonical.Grow(len(output))
	for _, ref := range refs {
		canonical.WriteString(ref.name)
		canonical.WriteByte(0)
		canonical.WriteString(ref.objectID)
		canonical.WriteByte(0)
		canonical.WriteString(ref.objectType)
		canonical.WriteByte(0)
		if ref.objectType == "tag" {
			canonical.WriteString(ref.commitID)
			canonical.WriteByte(0)
			canonical.WriteString("commit")
		} else {
			canonical.WriteByte(0)
		}
		canonical.WriteByte(0)
	}
	return refs, canonical.Bytes(), nil
}

func validSourceAuditRepositoryRefName(name string) bool {
	if !sourceAuditRepositoryRefName.MatchString(name) ||
		strings.Contains(name, "..") ||
		strings.Contains(name, "//") ||
		strings.Contains(name, "@{") ||
		strings.HasSuffix(name, "/") ||
		strings.HasSuffix(name, ".") {
		return false
	}
	for _, component := range strings.Split(name, "/") {
		if strings.HasPrefix(component, ".") || strings.HasSuffix(component, ".lock") {
			return false
		}
	}
	return true
}

func decodeSourceAuditRepositoryBindingDocument(
	data []byte,
) (sourceAuditRepositoryBindingDocumentV3, error) {
	if len(data) > sourceAuditRepositoryBindingMaximumBytes {
		return sourceAuditRepositoryBindingDocumentV3{}, fmt.Errorf(
			"repository binding document exceeds %d bytes",
			sourceAuditRepositoryBindingMaximumBytes,
		)
	}
	var raw map[string]json.RawMessage
	if err := decodeSourceAuditJSON(data, &raw); err != nil {
		return sourceAuditRepositoryBindingDocumentV3{}, fmt.Errorf(
			"decode repository binding document: %w",
			err,
		)
	}
	if err := requireExactSourceAuditJSONFields(
		raw,
		"schema",
		"git_executable",
		"git_sha256",
		"repositories",
	); err != nil {
		return sourceAuditRepositoryBindingDocumentV3{}, fmt.Errorf(
			"repository binding document: %w",
			err,
		)
	}
	var document sourceAuditRepositoryBindingDocumentV3
	if err := json.Unmarshal(raw["schema"], &document.Schema); err != nil {
		return sourceAuditRepositoryBindingDocumentV3{}, fmt.Errorf("decode repository schema: %w", err)
	}
	if err := json.Unmarshal(raw["git_executable"], &document.GitExecutable); err != nil {
		return sourceAuditRepositoryBindingDocumentV3{}, fmt.Errorf("decode repository Git executable: %w", err)
	}
	if err := json.Unmarshal(raw["git_sha256"], &document.GitSHA256); err != nil {
		return sourceAuditRepositoryBindingDocumentV3{}, fmt.Errorf("decode repository Git SHA-256: %w", err)
	}
	var repositories []map[string]json.RawMessage
	if err := json.Unmarshal(raw["repositories"], &repositories); err != nil {
		return sourceAuditRepositoryBindingDocumentV3{}, fmt.Errorf("decode repository bindings: %w", err)
	}
	document.Repositories = make([]sourceAuditRepositoryBindingV3, 0, len(repositories))
	for index, fields := range repositories {
		if err := requireExactSourceAuditJSONFields(
			fields,
			"upstream",
			"path",
			"origin",
			"ordinary_ref_inventory_sha256",
		); err != nil {
			return sourceAuditRepositoryBindingDocumentV3{}, fmt.Errorf(
				"repository binding %d: %w",
				index,
				err,
			)
		}
		var binding sourceAuditRepositoryBindingV3
		for name, destination := range map[string]*string{
			"upstream":                      &binding.Upstream,
			"path":                          &binding.Path,
			"origin":                        &binding.Origin,
			"ordinary_ref_inventory_sha256": &binding.OrdinaryRefInventorySHA256,
		} {
			if err := json.Unmarshal(fields[name], destination); err != nil {
				return sourceAuditRepositoryBindingDocumentV3{}, fmt.Errorf(
					"decode repository binding %d field %q: %w",
					index,
					name,
					err,
				)
			}
		}
		document.Repositories = append(document.Repositories, binding)
	}
	canonical, err := json.MarshalIndent(document, "", "  ")
	if err != nil {
		return sourceAuditRepositoryBindingDocumentV3{}, fmt.Errorf(
			"encode canonical repository binding document: %w",
			err,
		)
	}
	canonical = append(canonical, '\n')
	if !bytes.Equal(data, canonical) {
		return sourceAuditRepositoryBindingDocumentV3{}, errors.New(
			"repository binding document is not canonical indented JSON",
		)
	}
	if len(document.Repositories) == len(sourceAuditRepositories) {
		expected := make(map[string]struct{}, len(sourceAuditRepositories))
		for _, repository := range sourceAuditRepositories {
			expected[repository.upstream] = struct{}{}
		}
		seen := make(map[string]struct{}, len(document.Repositories))
		exactSet := true
		for _, binding := range document.Repositories {
			if _, found := expected[binding.Upstream]; !found {
				exactSet = false
			}
			if _, duplicate := seen[binding.Upstream]; duplicate {
				exactSet = false
			}
			seen[binding.Upstream] = struct{}{}
		}
		if exactSet {
			for index, repository := range sourceAuditRepositories {
				if document.Repositories[index].Upstream != repository.upstream {
					return sourceAuditRepositoryBindingDocumentV3{}, fmt.Errorf(
						"repository bindings are not in locked repository order at index %d: got %q, want %q",
						index,
						document.Repositories[index].Upstream,
						repository.upstream,
					)
				}
			}
		}
	}
	return document, nil
}

func requireExactSourceAuditJSONFields(
	fields map[string]json.RawMessage,
	expected ...string,
) error {
	wanted := make(map[string]struct{}, len(expected))
	for _, name := range expected {
		wanted[name] = struct{}{}
		if _, found := fields[name]; !found {
			return fmt.Errorf("missing required field %q", name)
		}
	}
	for name := range fields {
		if _, found := wanted[name]; !found {
			return fmt.Errorf("unknown field %q", name)
		}
	}
	return nil
}
