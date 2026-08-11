package study

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"testing"

	"github.com/dkropachev/repo-view/benchmarks/tokenbench/cas"
)

type memoryTaskArtifactAuthority struct {
	mu       sync.Mutex
	objects  map[string][]byte
	calls    []cas.ObjectRef
	behavior func(context.Context, cas.ObjectRef, []byte, io.Writer) error
}

func (authority *memoryTaskArtifactAuthority) Copy(
	ctx context.Context,
	ref cas.ObjectRef,
	destination io.Writer,
) error {
	authority.mu.Lock()
	content, exists := authority.objects[ref.Digest]
	content = append([]byte(nil), content...)
	authority.calls = append(authority.calls, ref)
	behavior := authority.behavior
	authority.mu.Unlock()
	if !exists {
		return errors.New("test authority object is absent")
	}
	if behavior != nil {
		return behavior(ctx, ref, content, destination)
	}
	_, err := destination.Write(content)
	return err
}

func (authority *memoryTaskArtifactAuthority) callCount() int {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	return len(authority.calls)
}

func (authority *memoryTaskArtifactAuthority) callsFor(digest string) int {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	count := 0
	for _, ref := range authority.calls {
		if ref.Digest == digest {
			count++
		}
	}
	return count
}

func (authority *memoryTaskArtifactAuthority) replace(digest string, content []byte) {
	authority.mu.Lock()
	defer authority.mu.Unlock()
	authority.objects[digest] = append([]byte(nil), content...)
}

func taskBundleLoaderUniqueFixture(
	t *testing.T,
) (TaskCatalog, TaskBundle, map[string][]byte) {
	t.Helper()
	catalog := decodedTaskCatalogFixture(t)
	bundle := taskBundleFixture(t, catalog)
	objects := make(map[string][]byte, taskBundleArtifactCount)
	for taskIndex, task := range bundle.Tasks {
		for artifactIndex := range task.Artifacts {
			artifact := &bundle.Tasks[taskIndex].Artifacts[artifactIndex]
			content := []byte(taskArtifactFixtureContent(artifact.Role, task.TaskID))
			if got := taskArtifactSHA256(content); "sha256:"+got != artifact.Object.Digest {
				t.Fatalf(
					"fixture content digest for %q/%q = %q, want %q",
					task.TaskID,
					artifact.Role,
					got,
					artifact.Object.Digest,
				)
			}
			artifact.Object.Size = int64(len(content))
			objects[artifact.Object.Digest] = content
		}
	}
	if err := bundle.Validate(catalog); err != nil {
		t.Fatalf("unique loader fixture: %v", err)
	}
	return catalog, bundle, objects
}

func taskBundleLoaderSharedFixture(
	t *testing.T,
) (TaskCatalog, TaskBundle, map[TaskArtifactRole][]byte) {
	t.Helper()
	contentByRole := map[TaskArtifactRole][]byte{
		TaskArtifactGoldPatch:             []byte("diff --git a/task b/task\n"),
		TaskArtifactHiddenEvaluatorBundle: []byte(`{"schema_version":"test.hidden-evaluator/v1"}`),
		TaskArtifactPrompt:                []byte("Explain or change the selected behavior."),
		TaskArtifactToolchainManifest:     []byte(`{"schema_version":"test.toolchain/v1"}`),
	}
	catalog := decodedTaskCatalogFixture(t)
	for taskIndex := range catalog.Tasks {
		task := &catalog.Tasks[taskIndex]
		task.PromptSHA256 = taskArtifactSHA256(contentByRole[TaskArtifactPrompt])
		task.ToolchainSHA256 = taskArtifactSHA256(
			contentByRole[TaskArtifactToolchainManifest],
		)
		task.HiddenEvaluatorBundleSHA256 = taskArtifactSHA256(
			contentByRole[TaskArtifactHiddenEvaluatorBundle],
		)
		if task.GoldPatch != nil {
			task.GoldPatch.PatchSHA256 = taskArtifactSHA256(
				contentByRole[TaskArtifactGoldPatch],
			)
		}
	}
	if err := catalog.Validate(); err != nil {
		t.Fatalf("shared loader fixture catalog: %v", err)
	}
	bundle := taskBundleFixture(t, catalog)
	for taskIndex := range bundle.Tasks {
		for artifactIndex := range bundle.Tasks[taskIndex].Artifacts {
			artifact := &bundle.Tasks[taskIndex].Artifacts[artifactIndex]
			artifact.Object.Size = int64(len(contentByRole[artifact.Role]))
		}
	}
	if err := bundle.Validate(catalog); err != nil {
		t.Fatalf("shared loader fixture bundle: %v", err)
	}
	return catalog, bundle, contentByRole
}

func taskArtifactFixtureContent(role TaskArtifactRole, taskID string) string {
	switch role {
	case TaskArtifactGoldPatch:
		return "patch:" + taskID
	case TaskArtifactHiddenEvaluatorBundle:
		return "evaluator:" + taskID
	case TaskArtifactPrompt:
		return "prompt:" + taskID
	case TaskArtifactToolchainManifest:
		return "toolchain:" + taskID
	default:
		return "invalid:" + taskID
	}
}

func taskArtifactSHA256(content []byte) string {
	digest := sha256.Sum256(content)
	return hex.EncodeToString(digest[:])
}

func TestLoadAuthenticatedTaskBundleUsesCASAndReturnsDefensiveTypedAccess(
	t *testing.T,
) {
	catalog, bundle, contentByRole := taskBundleLoaderSharedFixture(t)
	root := filepath.Join(t.TempDir(), "cas")
	if err := os.Mkdir(root, 0o700); err != nil {
		t.Fatal(err)
	}
	store, err := cas.Open(root, cas.Options{MaxObjectBytes: maxHiddenEvaluatorBundleBytes})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Errorf("close CAS: %v", err)
		}
	})
	transaction, err := store.Begin()
	if err != nil {
		t.Fatal(err)
	}
	var rootRef cas.ObjectRef
	for _, role := range []TaskArtifactRole{
		TaskArtifactGoldPatch,
		TaskArtifactHiddenEvaluatorBundle,
		TaskArtifactPrompt,
		TaskArtifactToolchainManifest,
	} {
		_, mediaType, _ := taskArtifactContract(catalog.Tasks[0], role)
		ref, putErr := transaction.Put(
			context.Background(),
			mediaType,
			bytes.NewReader(contentByRole[role]),
		)
		if putErr != nil {
			t.Fatal(putErr)
		}
		rootRef = ref
	}
	if err := transaction.Commit(context.Background(), rootRef); err != nil {
		t.Fatal(err)
	}

	loaded, err := LoadAuthenticatedTaskBundle(
		context.Background(),
		catalog,
		bundle,
		store,
	)
	if err != nil {
		t.Fatal(err)
	}
	wantBundleSHA256, err := bundle.SHA256(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.CatalogSHA256() != bundle.CatalogSHA256 ||
		loaded.BundleSHA256() != wantBundleSHA256 {
		t.Fatal("loaded bundle identities changed")
	}

	taskIDs := loaded.TaskIDs()
	if len(taskIDs) != taskCatalogTaskCount || !sort.StringsAreSorted(taskIDs) {
		t.Fatalf("loaded task IDs are not the complete sorted catalog: %v", taskIDs)
	}
	firstTaskID := taskIDs[0]
	taskIDs[0] = "mutated"
	if loaded.TaskIDs()[0] != firstTaskID {
		t.Fatal("caller mutated loaded task IDs")
	}
	if _, exists := loaded.Task("../../objects/sha256"); exists {
		t.Fatal("path-like task lookup resolved")
	}
	if _, exists := loaded.Task("file:///tmp/object"); exists {
		t.Fatal("URI-like task lookup resolved")
	}

	task, exists := loaded.Task(firstTaskID)
	if !exists || task.TaskID() != firstTaskID || task.Family() != CatalogFamilyCode {
		t.Fatalf("unexpected typed task: %#v, exists=%t", task, exists)
	}
	wantRoles := taskBundleRoles(CatalogFamilyCode)
	roles := task.Roles()
	if !reflect.DeepEqual(roles, wantRoles) {
		t.Fatalf("task roles = %v, want %v", roles, wantRoles)
	}
	roles[0] = TaskArtifactRole("mutated")
	if !reflect.DeepEqual(task.Roles(), wantRoles) {
		t.Fatal("caller mutated loaded task roles")
	}
	if _, exists := task.Artifact(TaskArtifactRole("file:///tmp/object")); exists {
		t.Fatal("URI-like artifact role resolved")
	}

	prompt, exists := task.Artifact(TaskArtifactPrompt)
	if !exists || prompt.TaskID() != firstTaskID || prompt.Role() != TaskArtifactPrompt {
		t.Fatalf("unexpected prompt handle: %#v, exists=%t", prompt, exists)
	}
	wantRef := findTaskArtifact(t, &bundle.Tasks[0], TaskArtifactPrompt).Object
	gotRef := prompt.Object()
	if gotRef != wantRef {
		t.Fatalf("prompt object = %#v, want %#v", gotRef, wantRef)
	}
	gotRef.Digest = "sha256:" + taskArtifactSHA256([]byte("mutated"))
	if prompt.Object() != wantRef {
		t.Fatal("caller mutated prompt object identity")
	}
	content, err := prompt.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(content, contentByRole[TaskArtifactPrompt]) {
		t.Fatalf("prompt content = %q", content)
	}
	content[0] ^= 0xff
	again, err := prompt.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again, contentByRole[TaskArtifactPrompt]) {
		t.Fatal("caller mutation escaped into a later artifact read")
	}
	if err := prompt.Verify(context.Background()); err != nil {
		t.Fatal(err)
	}

	for _, candidate := range loaded.TaskIDs() {
		review, ok := loaded.Task(candidate)
		if ok && review.Family() == CatalogFamilyReview {
			if _, exists := review.Artifact(TaskArtifactGoldPatch); exists {
				t.Fatal("review task exposed a gold patch")
			}
			return
		}
	}
	t.Fatal("fixture has no review task")
}

func TestLoadAuthenticatedTaskBundleAuthenticatesDuplicateObjectsOnce(t *testing.T) {
	catalog, bundle, contentByRole := taskBundleLoaderSharedFixture(t)
	objects := make(map[string][]byte, len(contentByRole))
	for _, task := range bundle.Tasks {
		for _, artifact := range task.Artifacts {
			objects[artifact.Object.Digest] = contentByRole[artifact.Role]
		}
	}
	authority := &memoryTaskArtifactAuthority{objects: objects}
	loaded, err := LoadAuthenticatedTaskBundle(
		context.Background(), catalog, bundle, authority,
	)
	if err != nil {
		t.Fatal(err)
	}
	if got := authority.callCount(); got != len(contentByRole) {
		t.Fatalf("authority calls = %d, want %d distinct objects", got, len(contentByRole))
	}
	task, _ := loaded.Task(bundle.Tasks[0].TaskID)
	prompt, _ := task.Artifact(TaskArtifactPrompt)
	if _, err := prompt.Read(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := authority.callsFor(prompt.Object().Digest); got != 2 {
		t.Fatalf("prompt authority calls after read = %d, want 2", got)
	}
}

func TestLoadAuthenticatedTaskBundleSnapshotsBeforeReentrantAuthority(t *testing.T) {
	catalog, bundle, objects := taskBundleLoaderUniqueFixture(t)
	wantBundleSHA256, err := bundle.SHA256(catalog)
	if err != nil {
		t.Fatal(err)
	}
	lastTaskIndex := len(bundle.Tasks) - 1
	lastPrompt := findTaskArtifact(t, &bundle.Tasks[lastTaskIndex], TaskArtifactPrompt)
	wantLastPrompt := lastPrompt.Object
	replacementPrompt := findTaskArtifact(t, &bundle.Tasks[0], TaskArtifactPrompt).Object

	authority := &memoryTaskArtifactAuthority{objects: objects}
	var mutateOnce sync.Once
	authority.behavior = func(
		_ context.Context,
		_ cas.ObjectRef,
		content []byte,
		out io.Writer,
	) error {
		mutateOnce.Do(func() {
			lastPrompt.Object = replacementPrompt
		})
		_, writeErr := out.Write(content)
		return writeErr
	}
	loaded, err := LoadAuthenticatedTaskBundle(
		context.Background(), catalog, bundle, authority,
	)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.BundleSHA256() != wantBundleSHA256 {
		t.Fatal("reentrant caller mutation changed the authenticated bundle identity")
	}
	loadedTask, exists := loaded.Task(bundle.Tasks[lastTaskIndex].TaskID)
	if !exists {
		t.Fatal("authenticated bundle lost the future task during reentrant mutation")
	}
	loadedPrompt, exists := loadedTask.Artifact(TaskArtifactPrompt)
	if !exists || loadedPrompt.Object() != wantLastPrompt {
		t.Fatalf(
			"reentrant caller mutation changed loaded prompt to %#v",
			loadedPrompt.Object(),
		)
	}
}

func TestLoadAuthenticatedTaskBundleFailsClosedOnBadAuthorityReads(t *testing.T) {
	errAuthority := errors.New("test authority failure")
	tests := []struct {
		name      string
		wantError error
		behavior  func(context.Context, cas.ObjectRef, []byte, io.Writer) error
	}{
		{
			name:      "corrupt",
			wantError: cas.ErrIntegrity,
			behavior: func(_ context.Context, _ cas.ObjectRef, content []byte, out io.Writer) error {
				content[0] ^= 0xff
				_, err := out.Write(content)
				return err
			},
		},
		{
			name:      "short",
			wantError: cas.ErrIntegrity,
			behavior: func(_ context.Context, _ cas.ObjectRef, content []byte, out io.Writer) error {
				_, err := out.Write(content[:len(content)-1])
				return err
			},
		},
		{
			name:      "oversize ignoring writer error",
			wantError: cas.ErrIntegrity,
			behavior: func(_ context.Context, _ cas.ObjectRef, content []byte, out io.Writer) error {
				_, _ = out.Write(append(content, 0))
				return nil
			},
		},
		{
			name:      "partial authority failure",
			wantError: errAuthority,
			behavior: func(_ context.Context, _ cas.ObjectRef, content []byte, out io.Writer) error {
				_, _ = out.Write(content[:len(content)/2])
				return errAuthority
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			catalog, bundle, objects := taskBundleLoaderUniqueFixture(t)
			target := bundle.Tasks[7].Artifacts[2]
			authority := &memoryTaskArtifactAuthority{objects: objects}
			authority.behavior = func(
				ctx context.Context,
				ref cas.ObjectRef,
				content []byte,
				out io.Writer,
			) error {
				if ref.Digest == target.Object.Digest {
					return test.behavior(ctx, ref, content, out)
				}
				_, err := out.Write(content)
				return err
			}
			loaded, err := LoadAuthenticatedTaskBundle(
				context.Background(), catalog, bundle, authority,
			)
			if loaded != nil || !errors.Is(err, test.wantError) {
				t.Fatalf("load result = %#v, error = %v, want nil and %v", loaded, err, test.wantError)
			}
			var accessErr *TaskArtifactAccessError
			if !errors.As(err, &accessErr) || accessErr.Stage != "load" ||
				accessErr.TaskID != bundle.Tasks[7].TaskID || accessErr.Role != target.Role ||
				accessErr.Object != target.Object {
				t.Fatalf("load access error = %#v", accessErr)
			}
			if strings.Contains(err.Error(), errAuthority.Error()) {
				t.Fatal("rendered access error exposed arbitrary authority text")
			}
		})
	}
}

func TestAuthenticatedTaskArtifactRejectsReplacementBeforeReturningBytes(t *testing.T) {
	catalog, bundle, objects := taskBundleLoaderUniqueFixture(t)
	authority := &memoryTaskArtifactAuthority{objects: objects}
	loaded, err := LoadAuthenticatedTaskBundle(
		context.Background(), catalog, bundle, authority,
	)
	if err != nil {
		t.Fatal(err)
	}
	task, _ := loaded.Task(bundle.Tasks[0].TaskID)
	prompt, _ := task.Artifact(TaskArtifactPrompt)
	original := append([]byte(nil), objects[prompt.Object().Digest]...)
	replacement := append([]byte(nil), original...)
	replacement[len(replacement)-1] ^= 0xff
	authority.replace(prompt.Object().Digest, replacement)

	content, err := prompt.Read(context.Background())
	if content != nil || !errors.Is(err, cas.ErrIntegrity) {
		t.Fatalf("read after replacement = %q, %v", content, err)
	}
	var accessErr *TaskArtifactAccessError
	if !errors.As(err, &accessErr) || accessErr.Stage != "read" {
		t.Fatalf("replacement error = %#v", accessErr)
	}
	if err := prompt.Verify(context.Background()); !errors.Is(err, cas.ErrIntegrity) {
		t.Fatalf("verify after replacement error = %v", err)
	}

	authority.replace(prompt.Object().Digest, original)
	content, err = prompt.Read(context.Background())
	if err != nil || !bytes.Equal(content, original) {
		t.Fatalf("read after restoration = %q, %v", content, err)
	}
}

func TestLoadAuthenticatedTaskBundleHonorsCancellation(t *testing.T) {
	catalog, bundle, objects := taskBundleLoaderUniqueFixture(t)
	t.Run("before load", func(t *testing.T) {
		authority := &memoryTaskArtifactAuthority{objects: objects}
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		loaded, err := LoadAuthenticatedTaskBundle(ctx, catalog, bundle, authority)
		if loaded != nil || !errors.Is(err, context.Canceled) || authority.callCount() != 0 {
			t.Fatalf("canceled load = %#v, %v, calls=%d", loaded, err, authority.callCount())
		}
	})

	t.Run("during authority copy", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		authority := &memoryTaskArtifactAuthority{objects: objects}
		authority.behavior = func(
			_ context.Context,
			_ cas.ObjectRef,
			content []byte,
			out io.Writer,
		) error {
			_, _ = out.Write(content[:1])
			cancel()
			_, _ = out.Write(content[1:])
			return nil
		}
		loaded, err := LoadAuthenticatedTaskBundle(ctx, catalog, bundle, authority)
		if loaded != nil || !errors.Is(err, context.Canceled) || authority.callCount() != 1 {
			t.Fatalf("mid-copy cancellation = %#v, %v, calls=%d", loaded, err, authority.callCount())
		}
	})

	authority := &memoryTaskArtifactAuthority{objects: objects}
	loaded, err := LoadAuthenticatedTaskBundle(
		context.Background(), catalog, bundle, authority,
	)
	if err != nil {
		t.Fatal(err)
	}
	task, _ := loaded.Task(bundle.Tasks[0].TaskID)
	prompt, _ := task.Artifact(TaskArtifactPrompt)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	callsBefore := authority.callCount()
	content, err := prompt.Read(ctx)
	if content != nil || !errors.Is(err, context.Canceled) ||
		authority.callCount() != callsBefore {
		t.Fatalf("canceled artifact read = %q, %v", content, err)
	}
}

func TestLoadAuthenticatedTaskBundleRejectsInvalidInputsBeforeCASAccess(t *testing.T) {
	catalog, bundle, objects := taskBundleLoaderUniqueFixture(t)
	authority := &memoryTaskArtifactAuthority{objects: objects}
	invalid := cloneTaskBundle(t, bundle)
	invalid.CatalogSHA256 = taskArtifactSHA256([]byte("another catalog"))
	if loaded, err := LoadAuthenticatedTaskBundle(
		context.Background(), catalog, invalid, authority,
	); loaded != nil || err == nil || authority.callCount() != 0 {
		t.Fatalf("invalid bundle load = %#v, %v, calls=%d", loaded, err, authority.callCount())
	}

	if loaded, err := LoadAuthenticatedTaskBundle(
		context.Background(), catalog, bundle, nil,
	); loaded != nil || err == nil {
		t.Fatalf("nil authority load = %#v, %v", loaded, err)
	}
	var typedNil *memoryTaskArtifactAuthority
	if loaded, err := LoadAuthenticatedTaskBundle(
		context.Background(), catalog, bundle, typedNil,
	); loaded != nil || err == nil {
		t.Fatalf("typed nil authority load = %#v, %v", loaded, err)
	}
	if loaded, err := LoadAuthenticatedTaskBundle(
		nil, //nolint:staticcheck // The public boundary must fail closed on a nil context.
		catalog, bundle, authority,
	); loaded != nil || err == nil {
		t.Fatalf("nil context load = %#v, %v", loaded, err)
	}

	var zero AuthenticatedTaskArtifact
	if content, err := zero.Read(context.Background()); content != nil || err == nil {
		t.Fatalf("zero artifact read = %q, %v", content, err)
	}
	if err := zero.Verify(context.Background()); err == nil {
		t.Fatal("zero artifact verification succeeded")
	}
}
