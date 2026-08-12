// Package snapshot creates and validates the immutable filesystem image used
// by publishable tokenbench executions. Serializable values in this package
// are audit data. Only an Authority returned by Build can authorize live use.
package snapshot

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/yapless/scopesifter/benchmarks/tokenbench/internal/commandrunner"
)

const (
	OriginSchemaVersion    = "tokenbench.origin-inputs/v4"
	ExecutionSchemaVersion = "tokenbench.execution-inputs/v4"

	ManifestKindDirectory         = "directory"
	ManifestKindFile              = "regular-file"
	FSVerityAlgorithm             = "sha256"
	GoCommandRunnerImplementation = commandrunner.Implementation

	logicalSnapshotRoot  = "snapshot-root"
	logicalSourceRoot    = "source/worktree"
	logicalGitRoot       = "source/git"
	logicalToolsRoot     = "tools"
	logicalToolboxRoot   = "toolbox"
	logicalCodex         = "tool/codex"
	logicalScopeSifter   = "tool/scopesifter"
	logicalGit           = "tool/verifier-git"
	logicalCommandRunner = "tool/command-runner"
	logicalRunnerInit    = "tool/runner-arm-init"
	logicalChangedCache  = "cache/changed-state"
)

const emptySHA256 = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// SourceOrigin is the already-verified authored source identity. Root is the
// origin pathname, never the pathname used by a target process.
type SourceOrigin struct {
	Root              string `json:"root"`
	Revision          string `json:"revision"`
	Base              string `json:"base"`
	TreeSHA256        string `json:"tree_sha256"`
	GitMetadataSHA256 string `json:"git_metadata_sha256"`
}

// FileOrigin pins one role to exact origin bytes. Roles are fields rather
// than an open list so a caller cannot expand the executable surface.
type FileOrigin struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

// UtilityOrigins is the closed executable surface available to command-runner
// calls.
// Every role must be a distinct static image: multicall and argv[0]-selected
// applet surfaces are intentionally not representable.
type UtilityOrigins struct {
	Ripgrep FileOrigin `json:"ripgrep"`
	Find    FileOrigin `json:"find"`
	Head    FileOrigin `json:"head"`
	Tail    FileOrigin `json:"tail"`
	WC      FileOrigin `json:"wc"`
	Sort    FileOrigin `json:"sort"`
	Cut     FileOrigin `json:"cut"`
	Tr      FileOrigin `json:"tr"`
	Cat     FileOrigin `json:"cat"`
	LS      FileOrigin `json:"ls"`
	Grep    FileOrigin `json:"grep"`
}

// OriginInputs commits every mutable pathname from which a conformant image
// was constructed. Runner and ArmInit intentionally name one image. The same
// pinned Go image also supplies the command runner inside the immutable
// snapshot; it therefore has no separate mutable artifact origin.
type OriginInputs struct {
	SchemaVersion string         `json:"schema_version"`
	Source        SourceOrigin   `json:"source"`
	Codex         FileOrigin     `json:"codex"`
	ScopeSifter   FileOrigin     `json:"scopesifter"`
	Git           FileOrigin     `json:"git"`
	Utilities     UtilityOrigins `json:"utilities"`
	Runner        FileOrigin     `json:"runner"`
	ArmInit       FileOrigin     `json:"arm_init"`
	Commitment    string         `json:"commitment"`
}

// UtilityPaths mirrors UtilityOrigins after immutable snapshot construction.
type UtilityPaths struct {
	Ripgrep string `json:"ripgrep"`
	Find    string `json:"find"`
	Head    string `json:"head"`
	Tail    string `json:"tail"`
	WC      string `json:"wc"`
	Sort    string `json:"sort"`
	Cut     string `json:"cut"`
	Tr      string `json:"tr"`
	Cat     string `json:"cat"`
	LS      string `json:"ls"`
	Grep    string `json:"grep"`
}

// ChangedLineSpan is one inclusive, one-based range in the HEAD worktree.
type ChangedLineSpan struct {
	Start int `json:"start"`
	End   int `json:"end"`
}

// ChangedFileState is one sorted repository-relative changed path.
type ChangedFileState struct { //nolint:govet,nolintlint // Field order defines canonical cache JSON.
	// Path is the repository-relative path exposed by changed-only queries. For
	// a rename it is the destination path; PreviousPath records the source.
	Path         string            `json:"path"`
	PreviousPath string            `json:"previous_path,omitempty"`
	Status       string            `json:"status"`
	Similarity   int               `json:"similarity"`
	Binary       bool              `json:"binary"`
	Lines        []ChangedLineSpan `json:"lines"`
	// Patch is the exact canonical diff produced when Path is the sole
	// pathspec. Cache-only Changed joins these records after applying filters;
	// it never attempts to split or reinterpret the aggregate patch.
	Patch       string `json:"patch"`
	PatchSHA256 string `json:"patch_sha256"`
}

// ChangedStateCache is the complete Git-derived state consumed by the
// cache-only scopesifter backend. Patch is the full bounded base...HEAD patch.
type ChangedStateCache struct { //nolint:govet,nolintlint // Field order defines canonical cache JSON.
	SchemaVersion string             `json:"schema_version"`
	BaseCommit    string             `json:"base_commit"`
	HeadCommit    string             `json:"head_commit"`
	HeadSubject   string             `json:"head_subject"`
	ChangedFiles  []ChangedFileState `json:"changed_files"`
	Patch         string             `json:"patch"`
}

// Validate checks canonical ordering, path safety, bounds, and revision
// binding for one decoded cache without consulting Git or the filesystem.
func (cache ChangedStateCache) Validate() error {
	if cache.SchemaVersion != ChangedStateSchemaVersion {
		return fmt.Errorf("unexpected changed-state schema %q", cache.SchemaVersion)
	}
	if !validGitObjectID(cache.BaseCommit) || !validGitObjectID(cache.HeadCommit) {
		return errors.New("changed-state revisions are invalid")
	}
	if len(cache.HeadSubject) > maximumHeadSubjectBytes ||
		!utf8.ValidString(cache.HeadSubject) || strings.ContainsRune(cache.HeadSubject, '\x00') ||
		strings.ContainsAny(cache.HeadSubject, "\r\n") {
		return errors.New("changed-state head subject is invalid")
	}
	if cache.ChangedFiles == nil || len(cache.ChangedFiles) > maximumChangedFiles {
		return errors.New("changed-state file list is nil or oversized")
	}
	previous := ""
	aggregatePatchBytes := 0
	expandedChangedLines := 0
	for index, file := range cache.ChangedFiles {
		if !validRepositoryPath(file.Path) || index != 0 && previous >= file.Path {
			return errors.New("changed-state files are not canonical and strictly sorted")
		}
		previous = file.Path
		switch file.Status {
		case "added", "deleted", "modified", "type-changed":
			if file.PreviousPath != "" || file.Similarity != 0 {
				return errors.New("changed-state non-rename metadata is noncanonical")
			}
		case "renamed", "copied":
			if !validRepositoryPath(file.PreviousPath) ||
				file.PreviousPath == file.Path || file.Similarity < 1 || file.Similarity > 100 {
				return errors.New("changed-state rename metadata is invalid")
			}
		default:
			return errors.New("changed-state status is invalid")
		}
		if file.Lines == nil || len(file.Lines) > maximumChangedSpansPerFile {
			return errors.New("changed-state line spans are nil or oversized")
		}
		lastEnd := 0
		for _, span := range file.Lines {
			if span.Start <= 0 || span.End < span.Start ||
				span.End > maximumChangedLine || span.Start <= lastEnd+boolToInt(lastEnd != 0) {
				return errors.New("changed-state line spans are invalid or unmerged")
			}
			width := span.End - span.Start + 1
			if expandedChangedLines > maximumExpandedChangedLines-width {
				return errors.New("changed-state expanded line count exceeds its limit")
			}
			expandedChangedLines += width
			lastEnd = span.End
		}
		if len(file.Patch) > maximumPerFilePatchBytes ||
			!utf8.ValidString(file.Patch) || strings.ContainsRune(file.Patch, '\x00') ||
			!validSHA256(file.PatchSHA256) || digest([]byte(file.Patch)) != file.PatchSHA256 {
			return errors.New("changed-state per-file patch is invalid")
		}
		if aggregatePatchBytes > maximumAggregatePatchBytes-len(file.Patch) {
			return errors.New("changed-state per-file patches exceed their aggregate limit")
		}
		aggregatePatchBytes += len(file.Patch)
	}
	if len(cache.Patch) > maximumChangedPatchBytes || !utf8.ValidString(cache.Patch) ||
		strings.ContainsRune(cache.Patch, '\x00') {
		return errors.New("changed-state patch is invalid or oversized")
	}
	return nil
}

func validRepositoryPath(value string) bool {
	if value == "" || len(value) > maximumPathBytes || !utf8.ValidString(value) ||
		strings.ContainsRune(value, '\x00') ||
		strings.HasPrefix(value, "/") || filepath.IsAbs(filepath.FromSlash(value)) {
		return false
	}
	clean := filepath.ToSlash(filepath.Clean(filepath.FromSlash(value)))
	return clean == value && value != "." && value != ".." &&
		!strings.HasPrefix(value, "../") &&
		strings.Count(value, "/") <= maximumPathDepth &&
		value != ".git" && !strings.HasPrefix(value, ".git/")
}

func boolToInt(value bool) int {
	if value {
		return 1
	}
	return 0
}

// ChangedStateIdentity commits the canonical cache file without duplicating
// the potentially large patch in ExecutionInputs.
type ChangedStateIdentity struct {
	SchemaVersion     string `json:"schema_version"`
	Path              string `json:"path"`
	SHA256            string `json:"sha256"`
	BaseCommit        string `json:"base_commit"`
	HeadCommit        string `json:"head_commit"`
	HeadSubjectSHA256 string `json:"head_subject_sha256"`
	ChangedFileCount  int    `json:"changed_file_count"`
	PatchBytes        int    `json:"patch_bytes"`
	PerFilePatchBytes int    `json:"per_file_patch_bytes"`
}

// MountIdentity is a kernel-observed self-bind mount. It is never accepted as
// Build input. ReadOnly+NoSUID+NoDev make ordinary same-UID directory-entry
// replacement impossible while the authority is live.
type MountIdentity struct { //nolint:govet,nolintlint // Field order defines canonical mount JSON.
	SchemaVersion string `json:"schema_version"`
	MountID       uint64 `json:"mount_id"`
	ParentID      uint64 `json:"parent_id"`
	// MountNamespaceDevice/Inode pin /proc/self/ns/mnt. ParentMountPoint,
	// ParentFilesystemRoot, and ParentOptionalFields prove both the source root
	// of the self-bind and that its containing mount was neither shared nor a
	// slave when the self-bind was created and whenever it is reverified.
	MountNamespaceDevice uint64   `json:"mount_namespace_device"`
	MountNamespaceInode  uint64   `json:"mount_namespace_inode"`
	ParentMountPoint     string   `json:"parent_mount_point"`
	ParentFilesystemRoot string   `json:"parent_filesystem_root"`
	ParentOptionalFields []string `json:"parent_optional_fields"`
	MajorMinor           string   `json:"major_minor"`
	FilesystemRoot       string   `json:"filesystem_root"`
	MountPoint           string   `json:"mount_point"`
	MountOptions         []string `json:"mount_options"`
	OptionalFields       []string `json:"optional_fields"`
	FilesystemType       string   `json:"filesystem_type"`
	Source               string   `json:"source"`
	SuperOptions         []string `json:"super_options"`
	Device               uint64   `json:"device"`
	Inode                uint64   `json:"inode"`
	ReadOnly             bool     `json:"read_only"`
	NoSUID               bool     `json:"nosuid"`
	NoDev                bool     `json:"nodev"`
	SelfBind             bool     `json:"self_bind"`
	Commitment           string   `json:"commitment"`
}

// ELFIdentity is a bounded, platform-neutral description of an ELF image.
// Publishable executable roles must be Static. LoaderSHA256 is empty exactly
// when Interpreter is empty; dynamic loaders are never executable authority.
type ELFIdentity struct {
	Class        string   `json:"class"`
	Data         string   `json:"data"`
	Machine      string   `json:"machine"`
	Type         string   `json:"type"`
	Interpreter  string   `json:"interpreter"`
	LoaderSHA256 string   `json:"loader_sha256"`
	Needed       []string `json:"needed"`
	Static       bool     `json:"static"`
}

// ManifestEntry describes one inode reachable below SnapshotRoot. Entries are
// sorted by LogicalOrigin, paths are unique, and regular files carry both a
// conventional content digest and the kernel's fs-verity measurement digest.
type ManifestEntry struct { //nolint:govet,nolintlint // Field order defines canonical manifest JSON.
	LogicalOrigin       string       `json:"logical_origin"`
	SnapshotPath        string       `json:"snapshot_path"`
	Kind                string       `json:"kind"`
	Mode                uint32       `json:"mode"`
	Size                int64        `json:"size"`
	SHA256              string       `json:"sha256"`
	FSVerity            bool         `json:"fs_verity"`
	FSVerityAlgorithm   string       `json:"fs_verity_algorithm"`
	FSVerityMeasurement string       `json:"fs_verity_measurement"`
	ELF                 *ELFIdentity `json:"elf"`
}

// ExecutionInputs is the common immutable filesystem authority committed by
// a v6 plan. Both arms use these exact paths and policy lists. The lists are
// derived by this package and are not accepted as Build input.
type ExecutionInputs struct { //nolint:govet,nolintlint // Field order defines canonical execution JSON.
	SchemaVersion                string               `json:"schema_version"`
	SnapshotRoot                 string               `json:"snapshot_root"`
	SourceRoot                   string               `json:"source_root"`
	GitMetadataRoot              string               `json:"git_metadata_root"`
	CodexExecutable              string               `json:"codex_executable"`
	ScopeSifterExecutable        string               `json:"scopesifter_executable"`
	VerifierGitExecutable        string               `json:"verifier_git_executable"`
	CommandRunnerExecutable      string               `json:"command_runner_executable"`
	Utilities                    UtilityPaths         `json:"utilities"`
	ToolboxRoot                  string               `json:"toolbox_root"`
	RunnerExecutable             string               `json:"runner_executable"`
	ArmInitExecutable            string               `json:"arm_init_executable"`
	RunnerArmInitSameImage       bool                 `json:"runner_arm_init_same_image"`
	CommandRunnerImplementation  string               `json:"command_runner_implementation"`
	CommandRunnerRunnerSameImage bool                 `json:"command_runner_runner_same_image"`
	SourceRevision               string               `json:"source_revision"`
	SourceBaseRevision           string               `json:"source_base_revision"`
	SourceTreeSHA256             string               `json:"source_tree_sha256"`
	GitMetadataSHA256            string               `json:"git_metadata_sha256"`
	OriginCommitment             string               `json:"origin_commitment"`
	ChangedState                 ChangedStateIdentity `json:"changed_state"`
	ChangedStateCache            ChangedStateCache    `json:"changed_state_cache"`
	PathIsolation                MountIdentity        `json:"path_isolation"`
	Manifest                     []ManifestEntry      `json:"manifest"`
	ManifestSHA256               string               `json:"manifest_sha256"`
	ReadOnlyPaths                []string             `json:"read_only_paths"`
	ExecutablePaths              []string             `json:"executable_paths"`
	Commitment                   string               `json:"commitment"`
}

// NewOriginInputs validates and commits the explicit code-owned origin roles.
func NewOriginInputs(
	source SourceOrigin,
	codex, scopeSifter, git FileOrigin,
	utilities UtilityOrigins,
	runnerArmInit FileOrigin,
) (OriginInputs, error) {
	inputs := OriginInputs{
		SchemaVersion: OriginSchemaVersion,
		Source:        source,
		Codex:         codex,
		ScopeSifter:   scopeSifter,
		Git:           git,
		Utilities:     utilities,
		Runner:        runnerArmInit,
		ArmInit:       runnerArmInit,
	}
	commitment, err := originCommitment(inputs)
	if err != nil {
		return OriginInputs{}, err
	}
	inputs.Commitment = commitment
	if err := inputs.Validate(); err != nil {
		return OriginInputs{}, err
	}
	return inputs, nil
}

// Validate is a pure validation of the serialized origin commitment.
func (inputs OriginInputs) Validate() error {
	switch {
	case inputs.SchemaVersion != OriginSchemaVersion:
		return fmt.Errorf("unexpected origin-input schema %q", inputs.SchemaVersion)
	case !validAbsolutePath(inputs.Source.Root):
		return errors.New("origin source root must be absolute and canonical")
	case !validGitObjectID(inputs.Source.Revision):
		return errors.New("origin source revision is invalid")
	case !validGitObjectID(inputs.Source.Base):
		return errors.New("origin source base revision is invalid")
	case !validSHA256(inputs.Source.TreeSHA256):
		return errors.New("origin source tree digest is invalid")
	case !validSHA256(inputs.Source.GitMetadataSHA256):
		return errors.New("origin Git metadata digest is invalid")
	}
	for _, role := range []struct {
		name string
		file FileOrigin
	}{
		{"Codex", inputs.Codex},
		{"scopesifter", inputs.ScopeSifter},
		{"Git", inputs.Git},
		{"runner", inputs.Runner},
		{"arm-init", inputs.ArmInit},
	} {
		if !validAbsolutePath(role.file.Path) || !validSHA256(role.file.SHA256) {
			return fmt.Errorf("origin %s file identity is invalid", role.name)
		}
	}
	if inputs.Runner != inputs.ArmInit {
		return errors.New("runner and arm-init must explicitly use the same origin image")
	}
	uniqueRoles := []FileOrigin{
		inputs.Codex,
		inputs.ScopeSifter,
		inputs.Git,
		inputs.Runner,
	}
	for name, file := range inputs.Utilities.named() {
		if !validAbsolutePath(file.Path) || !validSHA256(file.SHA256) {
			return fmt.Errorf("origin utility %s file identity is invalid", name)
		}
		uniqueRoles = append(uniqueRoles, file)
	}
	for left := range uniqueRoles {
		for right := left + 1; right < len(uniqueRoles); right++ {
			if uniqueRoles[left].Path == uniqueRoles[right].Path {
				return errors.New("distinct executable roles share an origin pathname")
			}
			if uniqueRoles[left].SHA256 == uniqueRoles[right].SHA256 {
				return errors.New("distinct executable roles share an origin image digest")
			}
		}
	}
	commitment, err := originCommitment(inputs)
	if err != nil {
		return err
	}
	if inputs.Commitment != commitment {
		return errors.New("origin-input commitment is invalid")
	}
	return nil
}

func originCommitment(inputs OriginInputs) (string, error) {
	inputs.Commitment = ""
	raw, err := json.Marshal(inputs)
	if err != nil {
		return "", fmt.Errorf("encode origin-input commitment: %w", err)
	}
	return digest(raw), nil
}

// Validate is pure: it checks only recorded values and commitments and never
// probes a pathname, the running executable, or ambient configuration.
func (inputs ExecutionInputs) Validate() error {
	if inputs.SchemaVersion != ExecutionSchemaVersion {
		return fmt.Errorf("unexpected execution-input schema %q", inputs.SchemaVersion)
	}
	if !validAbsolutePath(inputs.SnapshotRoot) || inputs.SnapshotRoot == "/" {
		return errors.New("execution snapshot root must be absolute, canonical, and non-root")
	}
	wantPaths := struct {
		source, metadata, codex, scopeSifter, verifierGit, commandRunner, runner, cache string
	}{
		source:        filepath.Join(inputs.SnapshotRoot, "source"),
		metadata:      filepath.Join(inputs.SnapshotRoot, "source", ".git"),
		codex:         filepath.Join(inputs.SnapshotRoot, "tools", "codex"),
		scopeSifter:   filepath.Join(inputs.SnapshotRoot, "tools", "scopesifter"),
		verifierGit:   filepath.Join(inputs.SnapshotRoot, "tools", "verifier-git"),
		commandRunner: filepath.Join(inputs.SnapshotRoot, "toolbox", "bash"),
		runner:        filepath.Join(inputs.SnapshotRoot, "tools", "runner-arm-init"),
		cache:         filepath.Join(inputs.SnapshotRoot, "cache", "changed-state.json"),
	}
	switch {
	case inputs.SourceRoot != wantPaths.source:
		return errors.New("execution source path is not code-owned")
	case inputs.GitMetadataRoot != wantPaths.metadata:
		return errors.New("execution Git metadata path is not code-owned")
	case inputs.CodexExecutable != wantPaths.codex:
		return errors.New("execution Codex path is not code-owned")
	case inputs.ScopeSifterExecutable != wantPaths.scopeSifter:
		return errors.New("execution scopesifter path is not code-owned")
	case inputs.VerifierGitExecutable != wantPaths.verifierGit:
		return errors.New("execution verifier Git path is not code-owned")
	case inputs.CommandRunnerExecutable != wantPaths.commandRunner ||
		inputs.ToolboxRoot != filepath.Dir(wantPaths.commandRunner):
		return errors.New("execution command-runner/toolbox path is not code-owned")
	case inputs.RunnerExecutable != wantPaths.runner ||
		inputs.ArmInitExecutable != wantPaths.runner || !inputs.RunnerArmInitSameImage:
		return errors.New("execution runner/arm-init identity is not the exact shared image")
	case !inputs.CommandRunnerRunnerSameImage:
		return errors.New("execution command-runner/runner same-image assertion is missing")
	case inputs.CommandRunnerImplementation != GoCommandRunnerImplementation:
		return errors.New("execution command-runner implementation identity is not code-owned")
	case !validGitObjectID(inputs.SourceRevision):
		return errors.New("execution source revision is invalid")
	case !validGitObjectID(inputs.SourceBaseRevision):
		return errors.New("execution source base revision is invalid")
	case !validSHA256(inputs.SourceTreeSHA256):
		return errors.New("execution source tree digest is invalid")
	case !validSHA256(inputs.GitMetadataSHA256):
		return errors.New("execution Git metadata digest is invalid")
	case !validSHA256(inputs.OriginCommitment):
		return errors.New("execution origin commitment is invalid")
	}
	wantReadOnly := []string{inputs.SourceRoot, wantPaths.cache}
	wantExecutable := []string{
		inputs.CodexExecutable,
		inputs.ScopeSifterExecutable,
		inputs.CommandRunnerExecutable,
	}
	wantUtilities := utilityPathsForRoot(inputs.ToolboxRoot)
	if !reflect.DeepEqual(inputs.Utilities, wantUtilities) {
		return errors.New("execution utility paths are not code-owned")
	}
	wantExecutable = append(wantExecutable, inputs.Utilities.values()...)
	sort.Strings(wantExecutable)
	if !reflect.DeepEqual(inputs.ReadOnlyPaths, wantReadOnly) {
		return errors.New("execution read-only policy is not code-owned")
	}
	if !reflect.DeepEqual(inputs.ExecutablePaths, wantExecutable) {
		return errors.New("execution executable policy is not code-owned")
	}
	if len(inputs.Manifest) == 0 || len(inputs.Manifest) > maximumManifestEntries {
		return errors.New("execution manifest is empty or oversized")
	}
	byPath := make(map[string]ManifestEntry, len(inputs.Manifest))
	previousLogical := ""
	var totalBytes int64
	for index, entry := range inputs.Manifest {
		if err := validateManifestEntry(inputs.SnapshotRoot, entry); err != nil {
			return fmt.Errorf("manifest entry %d: %w", index, err)
		}
		if index != 0 && previousLogical >= entry.LogicalOrigin {
			return errors.New("execution manifest logical origins are not strictly sorted")
		}
		previousLogical = entry.LogicalOrigin
		expectedLogical, ok := logicalOriginForPath(inputs.SnapshotRoot, entry.SnapshotPath)
		if !ok || entry.LogicalOrigin != expectedLogical {
			return fmt.Errorf(
				"manifest path %q has noncanonical logical origin %q",
				entry.SnapshotPath,
				entry.LogicalOrigin,
			)
		}
		if _, exists := byPath[entry.SnapshotPath]; exists {
			return fmt.Errorf("execution manifest path %q is duplicated", entry.SnapshotPath)
		}
		byPath[entry.SnapshotPath] = entry
		if entry.Kind == ManifestKindFile {
			if totalBytes > maximumSnapshotBytes-entry.Size {
				return errors.New("execution manifest exceeds its byte limit")
			}
			totalBytes += entry.Size
		}
	}
	executablePaths := append([]string{
		inputs.CodexExecutable,
		inputs.ScopeSifterExecutable,
		inputs.VerifierGitExecutable,
		inputs.CommandRunnerExecutable,
		inputs.RunnerExecutable,
	}, inputs.Utilities.values()...)
	for _, path := range executablePaths {
		entry, exists := byPath[path]
		if !exists || entry.Kind != ManifestKindFile || entry.ELF == nil ||
			!entry.ELF.Static || entry.ELF.Interpreter != "" ||
			entry.ELF.LoaderSHA256 != "" || len(entry.ELF.Needed) != 0 ||
			entry.Mode&0o111 == 0 {
			return fmt.Errorf("publishable executable %q is not a static image in the manifest", path)
		}
	}
	if byPath[inputs.CommandRunnerExecutable].SHA256 != byPath[inputs.RunnerExecutable].SHA256 {
		return errors.New("execution command runner is not the pinned Go runner image")
	}
	if err := validateExecutableManifestABIs(byPath, inputs.RunnerExecutable, executablePaths); err != nil {
		return err
	}
	if err := validateChangedStateIdentity(inputs, byPath, wantPaths.cache); err != nil {
		return err
	}
	if err := inputs.PathIsolation.validate(inputs.SnapshotRoot); err != nil {
		return fmt.Errorf("execution path isolation: %w", err)
	}
	for _, required := range []string{
		inputs.SnapshotRoot,
		inputs.SourceRoot,
		inputs.GitMetadataRoot,
		filepath.Join(inputs.SnapshotRoot, "tools"),
		inputs.ToolboxRoot,
		filepath.Join(inputs.SnapshotRoot, "cache"),
	} {
		entry, exists := byPath[required]
		if !exists || entry.Kind != ManifestKindDirectory {
			return fmt.Errorf("required snapshot directory %q is missing", required)
		}
	}
	manifestDigest, err := manifestSHA256(inputs.Manifest)
	if err != nil {
		return err
	}
	if inputs.ManifestSHA256 != manifestDigest {
		return errors.New("execution manifest digest is invalid")
	}
	commitment, err := executionCommitment(inputs)
	if err != nil {
		return err
	}
	if inputs.Commitment != commitment {
		return errors.New("execution-input commitment is invalid")
	}
	return nil
}

// ValidateBinding is the pure v4 bridge between mutable origins and the
// immutable image. It proves that every copied executable's manifest bytes and
// every source identity equal the committed origin role.
func ValidateBinding(origins OriginInputs, execution ExecutionInputs) error {
	if err := origins.Validate(); err != nil {
		return fmt.Errorf("origin inputs: %w", err)
	}
	if err := execution.Validate(); err != nil {
		return fmt.Errorf("execution inputs: %w", err)
	}
	if execution.OriginCommitment != origins.Commitment {
		return errors.New("execution origin commitment differs from origin inputs")
	}
	if execution.SourceRevision != origins.Source.Revision ||
		execution.SourceBaseRevision != origins.Source.Base ||
		execution.SourceTreeSHA256 != origins.Source.TreeSHA256 ||
		execution.GitMetadataSHA256 != origins.Source.GitMetadataSHA256 {
		return errors.New("execution source identity differs from origin inputs")
	}
	manifest := make(map[string]ManifestEntry, len(execution.Manifest))
	for _, entry := range execution.Manifest {
		manifest[entry.SnapshotPath] = entry
	}
	roles := []struct {
		name   string
		origin FileOrigin
		path   string
	}{
		{"Codex", origins.Codex, execution.CodexExecutable},
		{"scopesifter", origins.ScopeSifter, execution.ScopeSifterExecutable},
		{"verifier Git", origins.Git, execution.VerifierGitExecutable},
		{"command runner", origins.Runner, execution.CommandRunnerExecutable},
		{"runner/arm-init", origins.Runner, execution.RunnerExecutable},
	}
	for _, role := range roles {
		entry, exists := manifest[role.path]
		if !exists || entry.Kind != ManifestKindFile || entry.SHA256 != role.origin.SHA256 {
			return fmt.Errorf("execution %s bytes differ from origin inputs", role.name)
		}
	}
	utilityOrigins := origins.Utilities.named()
	utilityPaths := execution.Utilities.named()
	for name, origin := range utilityOrigins {
		path, exists := utilityPaths[name]
		entry, manifestExists := manifest[path]
		if !exists || !manifestExists || entry.Kind != ManifestKindFile ||
			entry.SHA256 != origin.SHA256 {
			return fmt.Errorf("execution utility %s bytes differ from origin inputs", name)
		}
	}
	return nil
}

const MountSchemaVersion = "tokenbench.readonly-self-bind/v2"

func (identity MountIdentity) validate(snapshotRoot string) error {
	switch {
	case identity.SchemaVersion != MountSchemaVersion:
		return fmt.Errorf("unexpected mount identity schema %q", identity.SchemaVersion)
	case identity.MountID == 0 || identity.ParentID == 0 || identity.MountID == identity.ParentID:
		return errors.New("mount identity IDs are invalid")
	case identity.MountNamespaceDevice == 0 || identity.MountNamespaceInode == 0:
		return errors.New("mount namespace identity is invalid")
	case !validAbsolutePath(identity.ParentMountPoint) ||
		!pathWithin(identity.ParentMountPoint, snapshotRoot):
		return errors.New("mount parent point does not contain the snapshot")
	case !validAbsolutePath(identity.ParentFilesystemRoot):
		return errors.New("mount parent filesystem root is invalid")
	case identity.ParentOptionalFields == nil ||
		!strictlySortedUnique(identity.ParentOptionalFields) ||
		hasPropagationField(identity.ParentOptionalFields):
		return errors.New("mount parent propagation is not private")
	case identity.MajorMinor == "" || strings.Count(identity.MajorMinor, ":") != 1:
		return errors.New("mount identity device number is invalid")
	case !validAbsolutePath(identity.FilesystemRoot):
		return errors.New("mount identity filesystem root is invalid")
	case identity.MountPoint != snapshotRoot:
		return errors.New("mount identity point differs from the snapshot root")
	case identity.MountOptions == nil || identity.OptionalFields == nil || identity.SuperOptions == nil ||
		!strictlySortedUnique(identity.MountOptions) ||
		!strictlySortedUnique(identity.OptionalFields) ||
		!strictlySortedUnique(identity.SuperOptions):
		return errors.New("mount identity options are not canonical")
	case hasPropagationField(identity.OptionalFields):
		return errors.New("snapshot mount propagation is not private")
	case identity.FilesystemType == "" || len(identity.FilesystemType) > 128 ||
		identity.Source == "" || len(identity.Source) > maximumPathBytes:
		return errors.New("mount identity filesystem is invalid")
	case identity.Device == 0 || identity.Inode == 0:
		return errors.New("mount identity inode is invalid")
	case !identity.ReadOnly || !identity.NoSUID || !identity.NoDev || !identity.SelfBind:
		return errors.New("mount identity lacks read-only/nosuid/nodev self-bind proof")
	}
	expectedRoot, err := bindFilesystemRoot(
		identity.ParentFilesystemRoot,
		identity.ParentMountPoint,
		snapshotRoot,
	)
	if err != nil || identity.FilesystemRoot != expectedRoot {
		return errors.Join(errors.New("mount identity is not an exact self-bind"), err)
	}
	commitment, err := mountCommitment(identity)
	if err != nil {
		return err
	}
	if identity.Commitment != commitment {
		return errors.New("mount identity commitment is invalid")
	}
	return nil
}

func hasPropagationField(fields []string) bool {
	for _, field := range fields {
		if strings.HasPrefix(field, "shared:") || strings.HasPrefix(field, "master:") {
			return true
		}
	}
	return false
}

func mountCommitment(identity MountIdentity) (string, error) {
	identity.Commitment = ""
	raw, err := json.Marshal(identity)
	if err != nil {
		return "", err
	}
	return digest(raw), nil
}

func strictlySortedUnique(values []string) bool {
	for index, value := range values {
		if value == "" || len(value) > 512 || !utf8.ValidString(value) ||
			strings.ContainsRune(value, '\x00') || index != 0 && values[index-1] >= value {
			return false
		}
	}
	return true
}

func logicalOriginForPath(root, path string) (string, bool) {
	if path == root {
		return logicalSnapshotRoot, true
	}
	relative, err := filepath.Rel(root, path)
	if err != nil || relative == "." || relative == ".." ||
		strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return "", false
	}
	relative = filepath.ToSlash(relative)
	switch relative {
	case "source":
		return logicalSourceRoot, true
	case "source/.git":
		return logicalGitRoot, true
	case "tools":
		return logicalToolsRoot, true
	case "toolbox":
		return logicalToolboxRoot, true
	case "cache":
		return "cache", true
	case "tools/codex":
		return logicalCodex, true
	case "tools/scopesifter":
		return logicalScopeSifter, true
	case "tools/verifier-git":
		return logicalGit, true
	case "tools/runner-arm-init":
		return logicalRunnerInit, true
	case "toolbox/bash":
		return logicalCommandRunner, true
	case "cache/changed-state.json":
		return logicalChangedCache, true
	}
	if name, found := strings.CutPrefix(relative, "toolbox/"); found {
		if _, exists := utilityLogicalNames[name]; exists {
			return "tool/" + name, true
		}
	}
	if suffix, found := strings.CutPrefix(relative, "source/.git/"); found {
		return logicalGitRoot + "/" + suffix, true
	}
	if suffix, found := strings.CutPrefix(relative, "source/"); found {
		return logicalSourceRoot + "/" + suffix, true
	}
	return "", false
}

const ChangedStateSchemaVersion = "tokenbench.changed-state-cache/v1"

var utilityLogicalNames = map[string]struct{}{
	"rg": {}, "find": {}, "head": {}, "tail": {},
	"wc": {}, "sort": {}, "cut": {}, "tr": {}, "cat": {}, "ls": {},
	"grep": {},
}

func (origins UtilityOrigins) named() map[string]FileOrigin {
	return map[string]FileOrigin{
		"rg":   origins.Ripgrep,
		"find": origins.Find, "head": origins.Head, "tail": origins.Tail,
		"wc": origins.WC, "sort": origins.Sort, "cut": origins.Cut,
		"tr": origins.Tr, "cat": origins.Cat, "ls": origins.LS,
		"grep": origins.Grep,
	}
}

func (paths UtilityPaths) values() []string {
	return []string{
		paths.Ripgrep, paths.Find, paths.Head,
		paths.Tail, paths.WC, paths.Sort, paths.Cut, paths.Tr, paths.Cat,
		paths.LS, paths.Grep,
	}
}

func (paths UtilityPaths) named() map[string]string {
	return map[string]string{
		"rg":   paths.Ripgrep,
		"find": paths.Find, "head": paths.Head, "tail": paths.Tail,
		"wc": paths.WC, "sort": paths.Sort, "cut": paths.Cut,
		"tr": paths.Tr, "cat": paths.Cat, "ls": paths.LS,
		"grep": paths.Grep,
	}
}

func utilityPathsForRoot(root string) UtilityPaths {
	return UtilityPaths{
		Ripgrep: filepath.Join(root, "rg"), Find: filepath.Join(root, "find"),
		Head: filepath.Join(root, "head"), Tail: filepath.Join(root, "tail"),
		WC: filepath.Join(root, "wc"), Sort: filepath.Join(root, "sort"),
		Cut: filepath.Join(root, "cut"), Tr: filepath.Join(root, "tr"),
		Cat: filepath.Join(root, "cat"), LS: filepath.Join(root, "ls"),
		Grep: filepath.Join(root, "grep"),
	}
}

func validateChangedStateIdentity(
	inputs ExecutionInputs,
	byPath map[string]ManifestEntry,
	wantPath string,
) error {
	state := inputs.ChangedState
	cache := inputs.ChangedStateCache
	entry, exists := byPath[wantPath]
	if err := cache.Validate(); err != nil {
		return fmt.Errorf("changed-state cache: %w", err)
	}
	cacheRaw, err := json.Marshal(cache)
	if err != nil {
		return err
	}
	switch {
	case state.SchemaVersion != ChangedStateSchemaVersion:
		return errors.New("changed-state cache schema is invalid")
	case state.Path != wantPath:
		return errors.New("changed-state cache path is not code-owned")
	case !exists || entry.Kind != ManifestKindFile || entry.SHA256 != state.SHA256:
		return errors.New("changed-state cache is not bound to its manifest file")
	case digest(cacheRaw) != state.SHA256:
		return errors.New("embedded changed-state cache digest is invalid")
	case state.BaseCommit != inputs.SourceBaseRevision ||
		state.HeadCommit != inputs.SourceRevision:
		return errors.New("changed-state cache revisions do not match the source")
	case cache.BaseCommit != state.BaseCommit || cache.HeadCommit != state.HeadCommit:
		return errors.New("embedded changed-state cache revisions differ from its identity")
	case !validSHA256(state.HeadSubjectSHA256):
		return errors.New("changed-state head-subject digest is invalid")
	case digest([]byte(cache.HeadSubject)) != state.HeadSubjectSHA256:
		return errors.New("changed-state head-subject digest differs from cache")
	case state.ChangedFileCount < 0 || state.ChangedFileCount > maximumChangedFiles:
		return errors.New("changed-state file count is invalid")
	case state.ChangedFileCount != len(cache.ChangedFiles):
		return errors.New("changed-state file count differs from cache")
	case state.PatchBytes < 0 || state.PatchBytes > maximumChangedPatchBytes:
		return errors.New("changed-state patch byte count is invalid")
	case state.PatchBytes != len(cache.Patch):
		return errors.New("changed-state patch byte count differs from cache")
	}
	if state.PerFilePatchBytes < 0 || state.PerFilePatchBytes > maximumAggregatePatchBytes ||
		state.PerFilePatchBytes != changedStatePerFilePatchBytes(cache) {
		return errors.New("changed-state per-file patch byte count differs from cache")
	}
	return nil
}

func changedStatePerFilePatchBytes(cache ChangedStateCache) int {
	total := 0
	for _, file := range cache.ChangedFiles {
		total += len(file.Patch)
	}
	return total
}

func validateManifestEntry(root string, entry ManifestEntry) error {
	if entry.LogicalOrigin == "" || len(entry.LogicalOrigin) > maximumLogicalOriginBytes ||
		!utf8.ValidString(entry.LogicalOrigin) || strings.ContainsRune(entry.LogicalOrigin, '\x00') {
		return errors.New("logical origin is invalid")
	}
	if !validAbsolutePath(entry.SnapshotPath) || !pathWithin(root, entry.SnapshotPath) {
		return errors.New("snapshot path escapes the committed root")
	}
	if entry.Mode&^uint32(0o777) != 0 {
		return errors.New("mode contains unsupported bits")
	}
	switch entry.Kind {
	case ManifestKindDirectory:
		if entry.Size != 0 || entry.SHA256 != emptySHA256 || entry.FSVerity ||
			entry.FSVerityAlgorithm != "" || entry.FSVerityMeasurement != "" ||
			entry.ELF != nil {
			return errors.New("directory manifest fields are not canonical")
		}
	case ManifestKindFile:
		if entry.Size < 0 || entry.Size > maximumRegularFileBytes ||
			!validSHA256(entry.SHA256) || !entry.FSVerity ||
			entry.FSVerityAlgorithm != FSVerityAlgorithm ||
			!validSHA256(entry.FSVerityMeasurement) {
			return errors.New("regular-file identity or fs-verity measurement is invalid")
		}
		if entry.ELF != nil {
			if err := entry.ELF.validate(); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("unsupported manifest kind %q", entry.Kind)
	}
	return nil
}

func (identity ELFIdentity) validate() error {
	if identity.Class == "" || identity.Data == "" || identity.Machine == "" ||
		identity.Type == "" || identity.Needed == nil || len(identity.Needed) > 128 {
		return errors.New("ELF identity is incomplete")
	}
	for index, library := range identity.Needed {
		if library == "" || len(library) > 512 || !utf8.ValidString(library) ||
			strings.ContainsRune(library, '\x00') ||
			index != 0 && identity.Needed[index-1] >= library {
			return errors.New("ELF dependency list is not canonical")
		}
	}
	if identity.Interpreter == "" {
		if identity.LoaderSHA256 != "" {
			return errors.New("static ELF identity has a loader digest")
		}
	} else if !validAbsolutePath(identity.Interpreter) ||
		!validSHA256(identity.LoaderSHA256) {
		return errors.New("dynamic ELF loader identity is invalid")
	}
	static := identity.Interpreter == "" && len(identity.Needed) == 0
	if identity.Static != static {
		return errors.New("ELF static classification is inconsistent")
	}
	return nil
}

func validateExecutableManifestABIs(
	manifest map[string]ManifestEntry,
	runnerPath string,
	executablePaths []string,
) error {
	runner, exists := manifest[runnerPath]
	if !exists || runner.ELF == nil {
		return errors.New("runner ELF ABI identity is absent")
	}
	for _, path := range executablePaths {
		entry, exists := manifest[path]
		if !exists || entry.ELF == nil {
			return fmt.Errorf("executable %q ELF ABI identity is absent", path)
		}
		if !sameELFABI(*runner.ELF, *entry.ELF) {
			return fmt.Errorf("executable %q ELF ABI differs from runner/arm-init", path)
		}
	}
	return nil
}

func sameELFABI(left, right ELFIdentity) bool {
	return left.Class == right.Class && left.Data == right.Data && left.Machine == right.Machine
}

func manifestSHA256(entries []ManifestEntry) (string, error) {
	raw, err := json.Marshal(entries)
	if err != nil {
		return "", fmt.Errorf("encode execution manifest: %w", err)
	}
	return digest(raw), nil
}

func executionCommitment(inputs ExecutionInputs) (string, error) {
	inputs.Commitment = ""
	raw, err := json.Marshal(inputs)
	if err != nil {
		return "", fmt.Errorf("encode execution-input commitment: %w", err)
	}
	return digest(raw), nil
}

func digest(content []byte) string {
	value := sha256.Sum256(content)
	return hex.EncodeToString(value[:])
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && value == hex.EncodeToString(decoded)
}

func validGitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && value == hex.EncodeToString(decoded)
}

func validAbsolutePath(value string) bool {
	return value != "" && len(value) <= maximumPathBytes && utf8.ValidString(value) &&
		!strings.ContainsRune(value, '\x00') && filepath.IsAbs(value) &&
		filepath.Clean(value) == value
}

func pathWithin(root, path string) bool {
	relative, err := filepath.Rel(root, path)
	return err == nil && relative != ".." &&
		!strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

// bindFilesystemRoot derives the root field Linux mountinfo reports when a
// path below a possibly-subtree-mounted parent is bind-mounted onto itself.
// For example, /mnt rooted at /subtree and target /mnt/run yields
// /subtree/run, not the host pathname /mnt/run.
func bindFilesystemRoot(parentRoot, parentPoint, target string) (string, error) {
	if !validAbsolutePath(parentRoot) || !validAbsolutePath(parentPoint) ||
		!validAbsolutePath(target) || !pathWithin(parentPoint, target) {
		return "", errors.New("self-bind parent or target path is invalid")
	}
	relative, err := filepath.Rel(parentPoint, target)
	if err != nil {
		return "", err
	}
	result := parentRoot
	if relative != "." {
		result = filepath.Join(parentRoot, relative)
	}
	if !validAbsolutePath(result) || !pathWithin(parentRoot, result) {
		return "", errors.New("derived self-bind filesystem root is invalid")
	}
	return result, nil
}

func cloneInputs(inputs ExecutionInputs) ExecutionInputs {
	clone := inputs
	clone.Manifest = make([]ManifestEntry, len(inputs.Manifest))
	for index, entry := range inputs.Manifest {
		clone.Manifest[index] = cloneManifestEntry(entry)
	}
	clone.ReadOnlyPaths = append([]string(nil), inputs.ReadOnlyPaths...)
	clone.ExecutablePaths = append([]string(nil), inputs.ExecutablePaths...)
	clone.PathIsolation.MountOptions = cloneStrings(inputs.PathIsolation.MountOptions)
	clone.PathIsolation.OptionalFields = cloneStrings(inputs.PathIsolation.OptionalFields)
	clone.PathIsolation.SuperOptions = cloneStrings(inputs.PathIsolation.SuperOptions)
	clone.PathIsolation.ParentOptionalFields = cloneStrings(inputs.PathIsolation.ParentOptionalFields)
	clone.ChangedStateCache.ChangedFiles = make(
		[]ChangedFileState,
		len(inputs.ChangedStateCache.ChangedFiles),
	)
	for index, file := range inputs.ChangedStateCache.ChangedFiles {
		clone.ChangedStateCache.ChangedFiles[index] = file
		clone.ChangedStateCache.ChangedFiles[index].Lines = make(
			[]ChangedLineSpan,
			len(file.Lines),
		)
		copy(clone.ChangedStateCache.ChangedFiles[index].Lines, file.Lines)
	}
	return clone
}

func cloneManifestEntry(entry ManifestEntry) ManifestEntry {
	clone := entry
	if entry.ELF != nil {
		identity := *entry.ELF
		identity.Needed = make([]string, len(entry.ELF.Needed))
		copy(identity.Needed, entry.ELF.Needed)
		clone.ELF = &identity
	}
	return clone
}

// Clone returns a deep copy suitable for embedding in a plan without sharing
// mutable slices with the live Authority.
func (inputs ExecutionInputs) Clone() ExecutionInputs { return cloneInputs(inputs) }

func cloneStrings(values []string) []string {
	if values == nil {
		return nil
	}
	clone := make([]string, len(values))
	copy(clone, values)
	return clone
}
