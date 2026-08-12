// Package workspace defines the audit records for the future runner-owned code
// workspace. Values in this package never authorize paths, mounts, or execution.
package workspace

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	// InputsSchemaVersion identifies the sole workspace-input audit shape.
	InputsSchemaVersion = "tokenbench.workspace-inputs/v1"
	// OutcomeSchemaVersion identifies the sole workspace-outcome audit shape.
	OutcomeSchemaVersion = "tokenbench.workspace-outcome/v1"

	maximumUpperBytes   = int64(16 << 30)
	maximumEntries      = 1_000_000
	maximumFileBytes    = int64(1 << 30)
	maximumPatchBytes   = 256 << 20
	maximumChangedFiles = 100_000
	maximumPathBytes    = 4_096
	// This temporary capacity constructs the tmpfs root, fixed private layout,
	// OverlayFS work metadata, and the code-owned .git whiteout. Before an arm
	// becomes visible, code pins every unused infrastructure inode so exactly
	// MaximumEntries remain shared by worktree changes and CacheRoot.
	workspaceInfrastructureInodes = 64

	mountPolicyDocument = "tokenbench.workspace-mount-policy/v1\n" +
		"root=borrowed-empty-mountpoint,detached-tmpfs,nosuid,nodev,noexec,noatime,bounded-bytes,exact-shared-worktree-cache-inode-budget,code-pinned-infrastructure\n" +
		"arm=detached-overlay,nosuid,nodev,noatime,index-off,nfs-export-off,redirect-off,metacopy-off,xino-off\n" +
		"mount-inputs=retained-descriptors-via-proc-self-fd\n" +
		"layout=worktree,upper,work,cache,capture\n" +
		"model-root-mode=0700\n" +
		"git=opaque-whiteout\n" +
		"cleanup=retained-mount-id,normal-unmount-only,descriptor-relative-owned-layout"
)

var violationCodePattern = regexp.MustCompile(`^[a-z][a-z0-9_]{0,63}$`)

var requiredMountPolicySHA256 = digest([]byte(mountPolicyDocument))

// Limits bounds one fresh arm workspace. MaximumEntries is both the merged-tree
// scan bound and the exact shared writable-inode budget for OverlayFS changes
// plus CacheRoot. These values are committed before execution and must be the
// same for both arms.
type Limits struct {
	MaximumUpperBytes   int64 `json:"maximum_upper_bytes"`
	MaximumEntries      int   `json:"maximum_entries"`
	MaximumFileBytes    int64 `json:"maximum_file_bytes"`
	MaximumPatchBytes   int   `json:"maximum_patch_bytes"`
	MaximumChangedFiles int   `json:"maximum_changed_files"`
}

// Inputs records the exact common workspace identity. Exported paths are audit
// data only; only the private live state retained by PairAuthority can
// authorize their use.
type Inputs struct {
	SchemaVersion      string `json:"schema_version"`
	ModelRoot          string `json:"model_root"`
	ImmutableLowerRoot string `json:"immutable_lower_root"`
	BaseTreeSHA256     string `json:"base_tree_sha256"`
	SnapshotCommitment string `json:"snapshot_commitment"`
	ChangedStateSHA256 string `json:"changed_state_sha256"`
	MountPolicySHA256  string `json:"mount_policy_sha256"`
	Commitment         string `json:"commitment"`
	Limits             Limits `json:"limits"`
}

// Status classifies capture without turning an agent output failure into an
// integrity success.
type Status string

const (
	StatusCaptured      Status = "captured"
	StatusNoChange      Status = "no-change"
	StatusLimitExceeded Status = "limit-exceeded"
	StatusInvalidTree   Status = "invalid-tree"
)

// Outcome records a bounded workspace result and its exact patch bytes. A
// later evidence layer stores Patch in CAS and publishes the remaining fields
// as authenticated metadata.
type Outcome struct {
	SchemaVersion      string `json:"schema_version"`
	Status             Status `json:"status"`
	InitialTreeSHA256  string `json:"initial_tree_sha256"`
	ResultTreeSHA256   string `json:"result_tree_sha256"`
	ResultTreeObjectID string `json:"result_tree_object_id"`
	PatchSHA256        string `json:"patch_sha256"`
	ViolationCode      string `json:"violation_code"`
	Patch              []byte `json:"patch"`
	ChangedFiles       int    `json:"changed_files"`
	ChangedLines       int    `json:"changed_lines"`
}

// Validate enforces finite runner-owned resource bounds.
func (limits Limits) Validate() error {
	switch {
	case limits.MaximumUpperBytes <= 0 || limits.MaximumUpperBytes > maximumUpperBytes:
		return fmt.Errorf("maximum_upper_bytes must be in [1,%d]", maximumUpperBytes)
	case limits.MaximumEntries <= 0 || limits.MaximumEntries > maximumEntries:
		return fmt.Errorf("maximum_entries must be in [1,%d]", maximumEntries)
	case limits.MaximumFileBytes <= 0 || limits.MaximumFileBytes > maximumFileBytes:
		return fmt.Errorf("maximum_file_bytes must be in [1,%d]", maximumFileBytes)
	case limits.MaximumFileBytes > limits.MaximumUpperBytes:
		return errors.New("maximum_file_bytes must not exceed maximum_upper_bytes")
	case limits.MaximumPatchBytes <= 0 || limits.MaximumPatchBytes > maximumPatchBytes:
		return fmt.Errorf("maximum_patch_bytes must be in [1,%d]", maximumPatchBytes)
	case limits.MaximumChangedFiles <= 0 || limits.MaximumChangedFiles > maximumChangedFiles:
		return fmt.Errorf("maximum_changed_files must be in [1,%d]", maximumChangedFiles)
	case limits.MaximumChangedFiles > limits.MaximumEntries:
		return errors.New("maximum_changed_files must not exceed maximum_entries")
	}
	return nil
}

// Validate checks the closed audit shape and its self-commitment. It does not
// inspect or authorize either path.
func (inputs Inputs) Validate() error {
	limitsErr := inputs.Limits.Validate()
	switch {
	case inputs.SchemaVersion != InputsSchemaVersion:
		return fmt.Errorf("workspace inputs schema_version must be %q", InputsSchemaVersion)
	case !validAbsoluteNonRootPath(inputs.ModelRoot):
		return errors.New("model_root must be an absolute canonical non-root path")
	case !validAbsoluteNonRootPath(inputs.ImmutableLowerRoot):
		return errors.New("immutable_lower_root must be an absolute canonical non-root path")
	case pathsOverlap(inputs.ModelRoot, inputs.ImmutableLowerRoot):
		return errors.New("model_root and immutable_lower_root must be disjoint")
	case !validSHA256(inputs.BaseTreeSHA256):
		return errors.New("base_tree_sha256 must be a lowercase SHA-256 digest")
	case !validSHA256(inputs.SnapshotCommitment):
		return errors.New("snapshot_commitment must be a lowercase SHA-256 digest")
	case !validSHA256(inputs.ChangedStateSHA256):
		return errors.New("changed_state_sha256 must be a lowercase SHA-256 digest")
	case inputs.MountPolicySHA256 != requiredMountPolicySHA256:
		return errors.New("mount_policy_sha256 must identify the code-owned workspace policy")
	case limitsErr != nil:
		return limitsErr
	case inputs.Commitment != inputs.ComputeCommitment():
		return errors.New("workspace inputs commitment mismatch")
	}
	return nil
}

// ComputeCommitment derives the framed identity of every common input except
// the commitment field itself.
func (inputs Inputs) ComputeCommitment() string {
	hasher := sha256.New()
	writeFrame(hasher, []byte(inputs.SchemaVersion))
	writeFrame(hasher, []byte(inputs.ModelRoot))
	writeFrame(hasher, []byte(inputs.ImmutableLowerRoot))
	writeFrame(hasher, []byte(inputs.BaseTreeSHA256))
	writeFrame(hasher, []byte(inputs.SnapshotCommitment))
	writeFrame(hasher, []byte(inputs.ChangedStateSHA256))
	writeInt64(hasher, inputs.Limits.MaximumUpperBytes)
	writeInt64(hasher, int64(inputs.Limits.MaximumEntries))
	writeInt64(hasher, inputs.Limits.MaximumFileBytes)
	writeInt64(hasher, int64(inputs.Limits.MaximumPatchBytes))
	writeInt64(hasher, int64(inputs.Limits.MaximumChangedFiles))
	writeFrame(hasher, []byte(inputs.MountPolicySHA256))
	return hex.EncodeToString(hasher.Sum(nil))
}

// Validate checks one outcome against the precommitted finite limits.
func (outcome Outcome) Validate(limits Limits) error {
	if err := limits.Validate(); err != nil {
		return fmt.Errorf("validate workspace limits: %w", err)
	}
	switch {
	case outcome.SchemaVersion != OutcomeSchemaVersion:
		return fmt.Errorf("workspace outcome schema_version must be %q", OutcomeSchemaVersion)
	case !validSHA256(outcome.InitialTreeSHA256):
		return errors.New("initial_tree_sha256 must be a lowercase SHA-256 digest")
	case outcome.ChangedFiles < 0 || outcome.ChangedFiles > limits.MaximumChangedFiles:
		return errors.New("changed_files exceeds the committed bounds")
	case outcome.ChangedLines < 0:
		return errors.New("changed_lines must not be negative")
	case outcome.ChangedLines > limits.MaximumPatchBytes:
		return errors.New("changed_lines exceeds the finite patch bound")
	}

	switch outcome.Status {
	case StatusCaptured:
		switch {
		case !validSHA256(outcome.ResultTreeSHA256):
			return errors.New("captured result_tree_sha256 is invalid")
		case !validGitObjectID(outcome.ResultTreeObjectID):
			return errors.New("captured result_tree_object_id is invalid")
		case outcome.ResultTreeSHA256 == outcome.InitialTreeSHA256:
			return errors.New("captured result tree must differ from the initial tree")
		case outcome.ChangedFiles == 0:
			return errors.New("captured outcome must contain a changed file")
		case len(outcome.Patch) == 0 || len(outcome.Patch) > limits.MaximumPatchBytes:
			return errors.New("captured patch is empty or exceeds the committed bound")
		case outcome.PatchSHA256 != digest(outcome.Patch):
			return errors.New("captured patch digest mismatch")
		case outcome.ViolationCode != "":
			return errors.New("captured outcome must not contain a violation code")
		}
	case StatusNoChange:
		switch {
		case outcome.ResultTreeSHA256 != outcome.InitialTreeSHA256:
			return errors.New("no-change result must equal the initial tree")
		case !validGitObjectID(outcome.ResultTreeObjectID):
			return errors.New("no-change result_tree_object_id is invalid")
		case len(outcome.Patch) != 0 || outcome.PatchSHA256 != digest(nil):
			return errors.New("no-change patch must be the canonical empty patch")
		case outcome.ChangedFiles != 0 || outcome.ChangedLines != 0 || outcome.ViolationCode != "":
			return errors.New("no-change outcome contains change or violation data")
		}
	case StatusLimitExceeded, StatusInvalidTree:
		if outcome.ResultTreeSHA256 != "" || outcome.ResultTreeObjectID != "" ||
			outcome.PatchSHA256 != "" || len(outcome.Patch) != 0 ||
			outcome.ChangedFiles != 0 || outcome.ChangedLines != 0 {
			return errors.New("failed workspace outcome contains a captured result")
		}
		if !violationCodePattern.MatchString(outcome.ViolationCode) {
			return errors.New("failed workspace outcome requires a canonical violation code")
		}
	default:
		return errors.New("workspace outcome status is invalid")
	}
	return nil
}

func validAbsoluteNonRootPath(path string) bool {
	return len(path) <= maximumPathBytes && utf8.ValidString(path) &&
		!strings.ContainsRune(path, '\x00') && filepath.IsAbs(path) &&
		filepath.Clean(path) == path && filepath.Dir(path) != path
}

func pathsOverlap(left, right string) bool {
	leftToRight, err := filepath.Rel(left, right)
	if err == nil && !pathEscapes(leftToRight) {
		return true
	}
	rightToLeft, err := filepath.Rel(right, left)
	return err == nil && !pathEscapes(rightToLeft)
}

func pathEscapes(relative string) bool {
	return relative == ".." || filepath.IsAbs(relative) ||
		len(relative) > 3 && relative[:3] == ".."+string(filepath.Separator)
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}

func validGitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && hex.EncodeToString(decoded) == value
}

func digest(content []byte) string {
	value := sha256.Sum256(content)
	return hex.EncodeToString(value[:])
}

func writeFrame(hasher hash.Hash, value []byte) {
	writeInt64(hasher, int64(len(value)))
	_, _ = hasher.Write(value)
}

func writeInt64(hasher hash.Hash, value int64) {
	var encoded [8]byte
	for index := range encoded {
		encoded[len(encoded)-1-index] = byte(value >> (index * 8))
	}
	_, _ = hasher.Write(encoded[:])
}
