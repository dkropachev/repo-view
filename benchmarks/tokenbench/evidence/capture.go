// Package evidence publishes and verifies immutable tokenbench run and replay
// manifests in a content-addressed store.
package evidence

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/dkropachev/repo-view/benchmarks/tokenbench"
	"github.com/dkropachev/repo-view/benchmarks/tokenbench/cas"
	"github.com/dkropachev/repo-view/benchmarks/tokenbench/harness"
	harnesscodex "github.com/dkropachev/repo-view/benchmarks/tokenbench/harness/codex"
)

const (
	CaptureSchemaVersion = "tokenbench.capture/v2"

	captureMediaType     = "application/vnd.tokenbench.capture.v2+json"
	planMediaType        = "application/vnd.tokenbench.plan.v2+json"
	observationMediaType = "application/vnd.tokenbench.observation.v1+json"
	stdoutMediaType      = "application/vnd.tokenbench.stdout"
	stderrMediaType      = "application/vnd.tokenbench.stderr"
	artifactMediaType    = "application/vnd.tokenbench.artifact"

	maxCaptureManifestBytes = 1 << 20
	maxPlanObjectBytes      = 64 << 20
	maxObservationBytes     = 32 << 20
)

// ArtifactObject binds an adapter artifact's semantic envelope to immutable
// bytes. Object.MediaType is the CAS transport type; MediaType is the original
// adapter-declared type and may include parameters.
type ArtifactObject struct {
	Object    cas.ObjectRef `json:"object"`
	Name      string        `json:"name"`
	MediaType string        `json:"media_type"`
}

// RawCapture stores exact process termination state and references its byte
// streams and sanitized adapter artifacts.
type RawCapture struct {
	Stdout          cas.ObjectRef            `json:"stdout"`
	Stderr          cas.ObjectRef            `json:"stderr"`
	Artifacts       []ArtifactObject         `json:"artifacts"`
	Resources       *harness.ResourceOutcome `json:"resources"`
	ExitCode        int                      `json:"exit_code"`
	LaunchFailed    bool                     `json:"launch_failed"`
	TimedOut        bool                     `json:"timed_out"`
	Cancelled       bool                     `json:"cancelled"`
	StdoutTruncated bool                     `json:"stdout_truncated"`
	StderrTruncated bool                     `json:"stderr_truncated"`
}

// Attempt stores exactly one normalized observation or ordinary failure.
type Attempt struct {
	Observation *cas.ObjectRef             `json:"observation,omitempty"`
	Failure     *tokenbench.AttemptFailure `json:"failure,omitempty"`
	Raw         RawCapture                 `json:"raw"`
	Arm         tokenbench.Arm             `json:"arm"`
}

// CaptureManifest is the immutable capture subject. PublishRun signs its exact
// ObjectRef and publishes an attestation envelope as the conformant root.
type CaptureManifest struct {
	Plan             cas.ObjectRef               `json:"plan"`
	Baseline         Attempt                     `json:"baseline"`
	Candidate        Attempt                     `json:"candidate"`
	ExecutorIdentity tokenbench.ExecutorIdentity `json:"executor_identity"`
	Order            [2]tokenbench.Arm           `json:"order"`
	SchemaVersion    string                      `json:"schema_version"`
	Repetition       int                         `json:"repetition"`
}

// Capture is a recursively verified, reconstructed run and its immutable root.
type Capture struct {
	Root        cas.ObjectRef
	Subject     cas.ObjectRef
	Attestation AttestationEnvelope
	Manifest    CaptureManifest
	Run         tokenbench.Run
}

type publicationObjectSet struct {
	refs []cas.ObjectRef
}

func (objects *publicationObjectSet) add(ref cas.ObjectRef) {
	objects.refs = append(objects.refs, ref)
}

func (objects *publicationObjectSet) addAttempt(attempt Attempt) {
	objects.add(attempt.Raw.Stdout)
	objects.add(attempt.Raw.Stderr)
	for _, artifact := range attempt.Raw.Artifacts {
		objects.add(artifact.Object)
	}
	if attempt.Observation != nil {
		objects.add(*attempt.Observation)
	}
}

func (objects *publicationObjectSet) addCapture(capture Capture) {
	objects.add(capture.Root)
	objects.add(capture.Subject)
	objects.add(capture.Manifest.Plan)
	objects.addAttempt(capture.Manifest.Baseline)
	objects.addAttempt(capture.Manifest.Candidate)
}

func retryablePublication() PublicationResult {
	return PublicationResult{
		State:            PublicationRetryable,
		RecoveryRequired: true,
	}
}

// PublishRun stages the capture subject and every referenced object, signs the
// subject, and publishes the attestation envelope last. Only a live runner-
// sealed Run has publication authority.
func PublishRun(
	ctx context.Context,
	store *cas.Store,
	run tokenbench.Run,
	signer AttestationSigner,
	verifier *Verifier,
) (PublicationResult, error) {
	if store == nil {
		return retryablePublication(), errors.New("evidence store is required")
	}
	authorizedSigner, err := authorizeAttestationSigner(signer, verifier, CaptureBundle)
	if err != nil {
		return retryablePublication(), err
	}
	snapshot, finish, err := tokenbench.AcquireLiveCapture(run)
	if err != nil {
		return retryablePublication(), fmt.Errorf("acquire live capture authority: %w", err)
	}
	result := retryablePublication()
	defer func() { finish(result.State == PublicationComplete) }()
	result, err = publishRun(ctx, store, snapshot, authorizedSigner, verifier)
	return result, err
}

func publishRun(
	ctx context.Context,
	store *cas.Store,
	run tokenbench.Run,
	signer AttestationSigner,
	verifier *Verifier,
) (PublicationResult, error) {
	if store == nil {
		return retryablePublication(), errors.New("evidence store is required")
	}
	authorizedSigner, err := authorizeAttestationSigner(signer, verifier, CaptureBundle)
	if err != nil {
		return retryablePublication(), err
	}
	signer = authorizedSigner
	if err := run.Validate(); err != nil {
		return retryablePublication(), fmt.Errorf("validate run before publication: %w", err)
	}
	if err := validateConformantCapture(run); err != nil {
		return retryablePublication(), fmt.Errorf("validate conformant capture before publication: %w", err)
	}
	transaction, err := store.Begin()
	if err != nil {
		return retryablePublication(), err
	}
	committed := false
	defer func() {
		if !committed {
			_ = transaction.Abort()
		}
	}()

	planRef, err := putJSON(ctx, transaction, planMediaType, run.Plan)
	if err != nil {
		return retryablePublication(), err
	}
	objects := publicationObjectSet{}
	objects.add(planRef)
	baseline, err := putAttempt(ctx, transaction, run.Baseline)
	if err != nil {
		return retryablePublication(), fmt.Errorf("publish baseline: %w", err)
	}
	objects.addAttempt(baseline)
	candidate, err := putAttempt(ctx, transaction, run.Candidate)
	if err != nil {
		return retryablePublication(), fmt.Errorf("publish candidate: %w", err)
	}
	objects.addAttempt(candidate)
	manifest := CaptureManifest{
		Plan:             planRef,
		Baseline:         baseline,
		Candidate:        candidate,
		ExecutorIdentity: run.ExecutorIdentity,
		Order:            run.Order,
		SchemaVersion:    CaptureSchemaVersion,
		Repetition:       run.Repetition,
	}
	subject, err := putJSON(ctx, transaction, captureMediaType, manifest)
	if err != nil {
		return retryablePublication(), err
	}
	objects.add(subject)
	root, err := putAttestation(
		ctx,
		transaction,
		signer,
		CaptureBundle,
		subject,
		make([]cas.ObjectRef, 0),
	)
	if err != nil {
		return retryablePublication(), err
	}
	objects.add(root)
	result, commitErr := commitAttestedRoot(
		ctx, store, transaction, root, verifier, objects.refs,
	)
	if result.State == PublicationComplete && !result.CleanupWarning {
		committed = true
	}
	return result, commitErr
}

// LoadCapture authenticates an attestation envelope with an out-of-band trust
// policy, then recursively verifies and reconstructs its capture subject.
func LoadCapture(
	ctx context.Context,
	store *cas.Store,
	root cas.ObjectRef,
	verifier *Verifier,
) (Capture, error) {
	verified, err := VerifyEvidence(ctx, store, root, verifier)
	if err != nil {
		return Capture{}, err
	}
	if verified.Capture == nil || verified.BundleKind != CaptureBundle {
		return Capture{}, fmt.Errorf("%w: root is not a capture", ErrInvalidAttestation)
	}
	return *verified.Capture, nil
}

func loadCaptureSubject(
	ctx context.Context,
	store *cas.Store,
	root cas.ObjectRef,
	envelope AttestationEnvelope,
) (Capture, error) {
	subject := envelope.Statement.Subject
	if err := preflightObject(subject, maxCaptureManifestBytes, "capture manifest"); err != nil {
		return Capture{}, err
	}
	rawManifest, err := store.Read(ctx, subject)
	if err != nil {
		return Capture{}, err
	}
	var manifest CaptureManifest
	if err := decodeStrict(rawManifest, &manifest); err != nil {
		return Capture{}, fmt.Errorf("decode capture manifest: %w", err)
	}
	if manifest.SchemaVersion != CaptureSchemaVersion {
		return Capture{}, fmt.Errorf("unexpected capture schema %q", manifest.SchemaVersion)
	}
	if manifest.Plan.MediaType != planMediaType {
		return Capture{}, fmt.Errorf("unexpected plan media type %q", manifest.Plan.MediaType)
	}
	if err := preflightObject(manifest.Plan, maxPlanObjectBytes, "capture plan"); err != nil {
		return Capture{}, err
	}
	rawPlan, err := store.Read(ctx, manifest.Plan)
	if err != nil {
		return Capture{}, fmt.Errorf("read capture plan: %w", err)
	}
	plan, err := tokenbench.DecodePlan(rawPlan)
	if err != nil {
		return Capture{}, fmt.Errorf("decode capture plan: %w", err)
	}
	baseline, err := loadAttempt(ctx, store, manifest.Baseline)
	if err != nil {
		return Capture{}, fmt.Errorf("load baseline: %w", err)
	}
	candidate, err := loadAttempt(ctx, store, manifest.Candidate)
	if err != nil {
		return Capture{}, fmt.Errorf("load candidate: %w", err)
	}
	run := tokenbench.Run{
		Plan:             plan,
		Baseline:         baseline,
		Candidate:        candidate,
		ExecutorIdentity: manifest.ExecutorIdentity,
		Order:            manifest.Order,
		SchemaVersion:    tokenbench.RunSchemaVersion,
		Repetition:       manifest.Repetition,
	}
	if err := run.Validate(); err != nil {
		return Capture{}, fmt.Errorf("validate reconstructed capture: %w", err)
	}
	if err := validateConformantCapture(run); err != nil {
		return Capture{}, err
	}
	return Capture{
		Root:        root,
		Subject:     subject,
		Attestation: envelope,
		Manifest:    manifest,
		Run:         run,
	}, nil
}

func validateConformantCapture(run tokenbench.Run) error {
	identity := run.ExecutorIdentity
	if identity.Kind != "process" ||
		identity.Version != "tokenbench.process-executor/v1" ||
		!tokenbench.ValidSHA256(identity.ConfigSHA256) {
		return fmt.Errorf(
			"%w: capture executor is not the built-in conformant process runner",
			ErrInvalidAttestation,
		)
	}
	if run.Plan.Baseline.HarnessIdentity.Kind != "codex" ||
		run.Plan.Candidate.HarnessIdentity.Kind != "codex" ||
		run.Plan.Candidate.HarnessIdentity != run.Plan.Baseline.HarnessIdentity {
		return fmt.Errorf(
			"%w: capture harness is not the built-in Codex harness",
			ErrInvalidAttestation,
		)
	}
	harnessIdentity := run.Plan.Baseline.HarnessIdentity
	if harnessIdentity.AdapterExecutableSHA256 != run.Plan.Baseline.RunnerExecutableSHA256 ||
		run.Plan.Candidate.RunnerExecutableSHA256 != run.Plan.Baseline.RunnerExecutableSHA256 {
		return fmt.Errorf(
			"%w: Codex adapter executable is not the attested tokenbench runner",
			ErrInvalidAttestation,
		)
	}
	if !containsString(
		harnesscodex.ExecutableSHA256Allowlist(),
		harnessIdentity.ExecutableSHA256,
	) {
		return fmt.Errorf("%w: Codex executable digest is not allowlisted", ErrInvalidAttestation)
	}
	matchedSnapshot := false
	for _, snapshot := range harnesscodex.Snapshots() {
		if snapshot.RequestedModel == run.Plan.Baseline.RequestedModel &&
			snapshot.ModelRevision == harnessIdentity.ModelRevision &&
			harnessIdentity.Model == snapshot.RequestedModel {
			matchedSnapshot = true
			break
		}
	}
	if !matchedSnapshot {
		return fmt.Errorf("%w: Codex model snapshot is not allowlisted", ErrInvalidAttestation)
	}
	// Plan.Validate proves that the candidate is this exact common process plus
	// the sole committed repo_view treatment encoding. The adapter's canonical
	// validator deliberately accepts only the common (baseline) invocation.
	if err := harnesscodex.ValidateCanonicalProcess(
		run.Plan.Baseline,
		run.Plan.RenderedProcesses.Baseline,
	); err != nil {
		return fmt.Errorf("%w: validate canonical Codex process: %v", ErrInvalidAttestation, err)
	}
	return nil
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func putAttempt(
	ctx context.Context,
	transaction *cas.Transaction,
	run tokenbench.ArmRun,
) (Attempt, error) {
	stdout, err := transaction.Put(ctx, stdoutMediaType, bytes.NewReader(run.Raw.Stdout))
	if err != nil {
		return Attempt{}, err
	}
	stderr, err := transaction.Put(ctx, stderrMediaType, bytes.NewReader(run.Raw.Stderr))
	if err != nil {
		return Attempt{}, err
	}
	artifacts := make([]ArtifactObject, len(run.Raw.Artifacts))
	for index, artifact := range run.Raw.Artifacts {
		object, err := transaction.Put(
			ctx,
			artifactMediaType,
			bytes.NewReader(artifact.Data),
		)
		if err != nil {
			return Attempt{}, err
		}
		artifacts[index] = ArtifactObject{
			Object:    object,
			Name:      artifact.Name,
			MediaType: artifact.MediaType,
		}
	}
	result := Attempt{
		Failure: cloneFailure(run.Failure),
		Raw: RawCapture{
			Stdout:          stdout,
			Stderr:          stderr,
			Artifacts:       artifacts,
			Resources:       harness.CloneResourceOutcome(run.Raw.Resources),
			ExitCode:        run.Raw.ExitCode,
			LaunchFailed:    run.Raw.LaunchFailed,
			TimedOut:        run.Raw.TimedOut,
			Cancelled:       run.Raw.Cancelled,
			StdoutTruncated: run.Raw.StdoutTruncated,
			StderrTruncated: run.Raw.StderrTruncated,
		},
		Arm: run.Arm,
	}
	if run.Observation != nil {
		ref, err := putJSON(ctx, transaction, observationMediaType, run.Observation)
		if err != nil {
			return Attempt{}, err
		}
		result.Observation = &ref
	}
	return result, nil
}

func loadAttempt(
	ctx context.Context,
	store *cas.Store,
	attempt Attempt,
) (tokenbench.ArmRun, error) {
	if attempt.Arm != tokenbench.BaselineArm && attempt.Arm != tokenbench.CandidateArm {
		return tokenbench.ArmRun{}, fmt.Errorf("invalid attempt arm %q", attempt.Arm)
	}
	if (attempt.Observation == nil) == (attempt.Failure == nil) {
		return tokenbench.ArmRun{}, errors.New("attempt must contain exactly one observation or failure")
	}
	if err := validateFailure(attempt.Failure); err != nil {
		return tokenbench.ArmRun{}, err
	}
	if attempt.Raw.Stdout.MediaType != stdoutMediaType ||
		attempt.Raw.Stderr.MediaType != stderrMediaType {
		return tokenbench.ArmRun{}, errors.New("attempt stream media type is invalid")
	}
	if err := preflightObject(attempt.Raw.Stdout, harness.MaxRawStreamBytes, "attempt stdout"); err != nil {
		return tokenbench.ArmRun{}, err
	}
	if err := preflightObject(attempt.Raw.Stderr, harness.MaxRawStreamBytes, "attempt stderr"); err != nil {
		return tokenbench.ArmRun{}, err
	}
	if attempt.Raw.Artifacts == nil || len(attempt.Raw.Artifacts) > harness.MaxArtifactCount {
		return tokenbench.ArmRun{}, errors.New("attempt artifact list is not canonical")
	}
	if attempt.Raw.Resources != nil {
		if err := harness.ValidateResourceOutcome(attempt.Raw.Resources); err != nil {
			return tokenbench.ArmRun{}, fmt.Errorf("attempt resource outcome: %w", err)
		}
	}
	artifactBytes := int64(0)
	for _, artifact := range attempt.Raw.Artifacts {
		if err := preflightObject(artifact.Object, harness.MaxArtifactBytes, "attempt artifact"); err != nil {
			return tokenbench.ArmRun{}, err
		}
		if artifact.Object.Size > int64(harness.MaxArtifactBytes)-artifactBytes {
			return tokenbench.ArmRun{}, errors.New("attempt artifact references exceed their total byte limit")
		}
		artifactBytes += artifact.Object.Size
	}
	stdout, err := store.Read(ctx, attempt.Raw.Stdout)
	if err != nil {
		return tokenbench.ArmRun{}, err
	}
	stderr, err := store.Read(ctx, attempt.Raw.Stderr)
	if err != nil {
		return tokenbench.ArmRun{}, err
	}
	artifacts := make([]harness.Artifact, len(attempt.Raw.Artifacts))
	for index, artifact := range attempt.Raw.Artifacts {
		if artifact.Object.MediaType != artifactMediaType {
			return tokenbench.ArmRun{}, fmt.Errorf("artifact %q has invalid CAS media type", artifact.Name)
		}
		data, err := store.Read(ctx, artifact.Object)
		if err != nil {
			return tokenbench.ArmRun{}, err
		}
		artifacts[index] = harness.Artifact{
			Name:      artifact.Name,
			MediaType: artifact.MediaType,
			Data:      data,
		}
	}
	if err := harness.ValidateArtifacts(artifacts); err != nil {
		return tokenbench.ArmRun{}, err
	}
	result := tokenbench.ArmRun{
		Failure: cloneFailure(attempt.Failure),
		Raw: harness.RawExecution{
			Stdout:          stdout,
			Stderr:          stderr,
			Artifacts:       artifacts,
			Resources:       harness.CloneResourceOutcome(attempt.Raw.Resources),
			ExitCode:        attempt.Raw.ExitCode,
			LaunchFailed:    attempt.Raw.LaunchFailed,
			TimedOut:        attempt.Raw.TimedOut,
			Cancelled:       attempt.Raw.Cancelled,
			StdoutTruncated: attempt.Raw.StdoutTruncated,
			StderrTruncated: attempt.Raw.StderrTruncated,
		},
		Arm: attempt.Arm,
	}
	if attempt.Observation != nil {
		if attempt.Observation.MediaType != observationMediaType {
			return tokenbench.ArmRun{}, errors.New("observation media type is invalid")
		}
		if err := preflightObject(*attempt.Observation, maxObservationBytes, "observation"); err != nil {
			return tokenbench.ArmRun{}, err
		}
		raw, err := store.Read(ctx, *attempt.Observation)
		if err != nil {
			return tokenbench.ArmRun{}, err
		}
		var observation harness.Observation
		if err := decodeStrict(raw, &observation); err != nil {
			return tokenbench.ArmRun{}, fmt.Errorf("decode observation: %w", err)
		}
		result.Observation = &observation
	}
	return result, nil
}

func preflightObject(ref cas.ObjectRef, maximum int64, name string) error {
	if err := ref.Validate(); err != nil {
		return fmt.Errorf("%s reference: %w", name, err)
	}
	if ref.Size > maximum {
		return fmt.Errorf("%s reference exceeds its schema byte limit", name)
	}
	return nil
}

func putJSON(
	ctx context.Context,
	transaction *cas.Transaction,
	mediaType string,
	value any,
) (cas.ObjectRef, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return cas.ObjectRef{}, fmt.Errorf("encode evidence object: %w", err)
	}
	return transaction.Put(ctx, mediaType, bytes.NewReader(raw))
}

func cloneFailure(source *tokenbench.AttemptFailure) *tokenbench.AttemptFailure {
	if source == nil {
		return nil
	}
	clone := *source
	return &clone
}

func validateFailure(failure *tokenbench.AttemptFailure) error {
	if failure == nil {
		return nil
	}
	if failure.Stage == "" || failure.Kind == "" || failure.Message == "" {
		return errors.New("attempt failure fields are required")
	}
	return nil
}
