package study

import (
	"errors"
	"fmt"
	"strings"

	"github.com/dkropachev/repo-view/benchmarks/tokenbench/cas"
)

const (
	// TaskBundleSchemaVersion identifies the sole supported task-artifact wire
	// contract.
	TaskBundleSchemaVersion = "tokenbench.task-bundle/v1"

	TaskPromptMediaType              = "application/vnd.tokenbench.task-prompt.v1+text"
	PinnedToolchainManifestMediaType = "application/vnd.tokenbench.pinned-toolchain.v1+json"
	HiddenEvaluatorBundleMediaType   = "application/vnd.tokenbench.hidden-evaluator-bundle.v1+json"
	GoldPatchMediaType               = "application/vnd.tokenbench.gold-patch.v1+diff"

	maxTaskBundleBytes            = 1 << 20
	maxTaskPromptBytes            = int64(1 << 20)
	maxPinnedToolchainBytes       = int64(4 << 20)
	maxHiddenEvaluatorBundleBytes = int64(64 << 20)
	maxGoldPatchBytes             = int64(16 << 20)
	maxTaskBundleReferencedBytes  = int64(8 << 30)
	taskBundleArtifactCount       = taskCatalogTaskCount*3 + tasksPerCatalogFamily
)

// TaskArtifactRole is one closed task-bundle artifact role. Values are ordered
// lexically in every TaskBundleTask.
type TaskArtifactRole string

const (
	TaskArtifactGoldPatch             TaskArtifactRole = "gold_patch"
	TaskArtifactHiddenEvaluatorBundle TaskArtifactRole = "hidden_evaluator_bundle"
	TaskArtifactPrompt                TaskArtifactRole = "prompt"
	TaskArtifactToolchainManifest     TaskArtifactRole = "toolchain_manifest"
)

// TaskBundle binds the complete task catalog to content-addressed authoring
// artifacts. It records identities only and does not open or execute objects.
//
//nolint:govet,nolintlint // Field order is the byte-level canonical wire order.
type TaskBundle struct {
	SchemaVersion string           `json:"schema_version"`
	CatalogSHA256 string           `json:"catalog_sha256"`
	Tasks         []TaskBundleTask `json:"tasks"`
}

// TaskBundleTask binds the artifacts for one catalog task. Artifacts are
// strictly sorted by role. Code tasks have four roles; all other tasks have
// three and cannot represent a null gold patch.
//
//nolint:govet,nolintlint // Field order is the byte-level canonical wire order.
type TaskBundleTask struct {
	TaskID    string         `json:"task_id"`
	Artifacts []TaskArtifact `json:"artifacts"`
}

// TaskArtifact gives one role a safe opaque CAS identity. ObjectRef carries
// the exact digest, byte size, and media type; no path or URI is accepted by
// this contract.
//
//nolint:govet,nolintlint // Field order is the byte-level canonical wire order.
type TaskArtifact struct {
	Role   TaskArtifactRole `json:"role"`
	Object cas.ObjectRef    `json:"object"`
}

// DecodeTaskBundle accepts only byte-exact canonical JSON and validates it
// against a complete, already decoded task catalog.
func DecodeTaskBundle(catalog TaskCatalog, raw []byte) (TaskBundle, error) {
	if len(raw) == 0 || len(raw) > maxTaskBundleBytes {
		return TaskBundle{}, fmt.Errorf("task bundle must contain 1..%d bytes", maxTaskBundleBytes)
	}
	var bundle TaskBundle
	if err := decodeCanonical(raw, &bundle); err != nil {
		return TaskBundle{}, fmt.Errorf("decode task bundle: %w", err)
	}
	if err := bundle.Validate(catalog); err != nil {
		return TaskBundle{}, err
	}
	return bundle, nil
}

// EncodeTaskBundle returns the sole canonical JSON representation of a valid
// catalog-bound bundle. It never sorts or repairs caller input.
func EncodeTaskBundle(catalog TaskCatalog, bundle TaskBundle) ([]byte, error) {
	if err := bundle.Validate(catalog); err != nil {
		return nil, err
	}
	return canonicalJSON(bundle)
}

// SHA256 returns the identity of the canonical, catalog-bound task bundle.
func (bundle TaskBundle) SHA256(catalog TaskCatalog) (string, error) {
	if err := bundle.Validate(catalog); err != nil {
		return "", err
	}
	return canonicalDigest(bundle)
}

// Validate checks the exact catalog commitment, task/order/count equality,
// closed family-specific role sets, object metadata, digest bindings, and
// aggregate bounds.
func (bundle TaskBundle) Validate(catalog TaskCatalog) error {
	if err := catalog.Validate(); err != nil {
		return fmt.Errorf("task bundle catalog: %w", err)
	}
	if bundle.SchemaVersion != TaskBundleSchemaVersion {
		return fmt.Errorf("unexpected task bundle schema %q", bundle.SchemaVersion)
	}
	catalogSHA256, err := catalog.SHA256()
	if err != nil {
		return fmt.Errorf("digest task bundle catalog: %w", err)
	}
	if bundle.CatalogSHA256 != catalogSHA256 {
		return errors.New("task bundle catalog_sha256 does not match the supplied catalog")
	}
	if len(bundle.Tasks) != taskCatalogTaskCount {
		return fmt.Errorf("task bundle must contain exactly %d tasks", taskCatalogTaskCount)
	}

	artifactCount := 0
	var referencedBytes int64
	objectsByDigest := make(map[string]cas.ObjectRef, taskBundleArtifactCount)
	for index, task := range bundle.Tasks {
		if index > 0 && bundle.Tasks[index-1].TaskID >= task.TaskID {
			return errors.New("task bundle tasks are not strictly sorted by task_id")
		}
		catalogTask := catalog.Tasks[index]
		if task.TaskID != catalogTask.TaskID {
			return fmt.Errorf(
				"task bundle task %d has task_id %q; want %q",
				index,
				task.TaskID,
				catalogTask.TaskID,
			)
		}
		roles := taskBundleRoles(catalogTask.Family)
		if len(task.Artifacts) != len(roles) {
			return fmt.Errorf(
				"task %q has %d artifacts; want %d for family %s",
				task.TaskID,
				len(task.Artifacts),
				len(roles),
				catalogTask.Family,
			)
		}
		for artifactIndex, artifact := range task.Artifacts {
			wantRole := roles[artifactIndex]
			if artifact.Role != wantRole {
				return fmt.Errorf(
					"task %q artifact %d has role %q; want %q",
					task.TaskID,
					artifactIndex,
					artifact.Role,
					wantRole,
				)
			}
			wantDigest, wantMediaType, maximumBytes := taskArtifactContract(catalogTask, wantRole)
			if err := validateTaskArtifactReference(
				task.TaskID,
				artifact,
				wantDigest,
				wantMediaType,
				maximumBytes,
			); err != nil {
				return err
			}
			if previous, exists := objectsByDigest[artifact.Object.Digest]; exists &&
				previous != artifact.Object {
				return fmt.Errorf(
					"task %q role %q gives an existing digest inconsistent object metadata",
					task.TaskID,
					artifact.Role,
				)
			}
			objectsByDigest[artifact.Object.Digest] = artifact.Object
			if artifact.Object.Size > maxTaskBundleReferencedBytes-referencedBytes {
				return fmt.Errorf(
					"task bundle artifact references exceed %d total bytes",
					maxTaskBundleReferencedBytes,
				)
			}
			referencedBytes += artifact.Object.Size
			artifactCount++
		}
	}
	if artifactCount != taskBundleArtifactCount {
		return fmt.Errorf(
			"task bundle contains %d artifact references; want %d",
			artifactCount,
			taskBundleArtifactCount,
		)
	}
	canonical, err := canonicalJSON(bundle)
	if err != nil {
		return fmt.Errorf("encode task bundle for size validation: %w", err)
	}
	if len(canonical) > maxTaskBundleBytes {
		return fmt.Errorf("task bundle exceeds %d canonical bytes", maxTaskBundleBytes)
	}
	return nil
}

func taskBundleRoles(family CatalogTaskFamily) []TaskArtifactRole {
	if family == CatalogFamilyCode {
		return []TaskArtifactRole{
			TaskArtifactGoldPatch,
			TaskArtifactHiddenEvaluatorBundle,
			TaskArtifactPrompt,
			TaskArtifactToolchainManifest,
		}
	}
	return []TaskArtifactRole{
		TaskArtifactHiddenEvaluatorBundle,
		TaskArtifactPrompt,
		TaskArtifactToolchainManifest,
	}
}

func taskArtifactContract(
	task CatalogTask,
	role TaskArtifactRole,
) (digest string, mediaType string, maximumBytes int64) {
	switch role {
	case TaskArtifactGoldPatch:
		return task.GoldPatch.PatchSHA256, GoldPatchMediaType, maxGoldPatchBytes
	case TaskArtifactHiddenEvaluatorBundle:
		return task.HiddenEvaluatorBundleSHA256,
			HiddenEvaluatorBundleMediaType,
			maxHiddenEvaluatorBundleBytes
	case TaskArtifactPrompt:
		return task.PromptSHA256, TaskPromptMediaType, maxTaskPromptBytes
	case TaskArtifactToolchainManifest:
		return task.ToolchainSHA256,
			PinnedToolchainManifestMediaType,
			maxPinnedToolchainBytes
	default:
		return "", "", 0
	}
}

func validateTaskArtifactReference(
	taskID string,
	artifact TaskArtifact,
	wantDigest string,
	wantMediaType string,
	maximumBytes int64,
) error {
	if err := artifact.Object.Validate(); err != nil {
		return fmt.Errorf("task %q role %q object: %w", taskID, artifact.Role, err)
	}
	if artifact.Object.Size <= 0 || artifact.Object.Size > maximumBytes {
		return fmt.Errorf(
			"task %q role %q object size must be in 1..%d",
			taskID,
			artifact.Role,
			maximumBytes,
		)
	}
	if artifact.Object.MediaType != wantMediaType {
		return fmt.Errorf(
			"task %q role %q media_type must be %q",
			taskID,
			artifact.Role,
			wantMediaType,
		)
	}
	digest, ok := strings.CutPrefix(artifact.Object.Digest, "sha256:")
	if !ok || digest != wantDigest {
		return fmt.Errorf(
			"task %q role %q digest does not match the catalog",
			taskID,
			artifact.Role,
		)
	}
	return nil
}
