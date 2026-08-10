package evidence

import (
	"context"
	"errors"
	"fmt"
	"reflect"

	"github.com/dkropachev/repo-view/benchmarks/tokenbench"
	"github.com/dkropachev/repo-view/benchmarks/tokenbench/cas"
	"github.com/dkropachev/repo-view/benchmarks/tokenbench/harness"
	harnesscodex "github.com/dkropachev/repo-view/benchmarks/tokenbench/harness/codex"
)

const (
	ReplaySchemaVersion    = "tokenbench.replay/v1"
	replayMediaType        = "application/vnd.tokenbench.replay.v1+json"
	maxReplayManifestBytes = 1 << 20
)

// DecoderIdentity pins the pure offline decoder used for replay. Executable
// and config digests refer to publishable code/config only; replay receives no
// credentials, model client, clock, randomness, or writable source capability.
type DecoderIdentity struct {
	Kind             string `json:"kind"`
	Version          string `json:"version"`
	Schema           string `json:"schema"`
	ExecutableSHA256 string `json:"executable_sha256"`
	ConfigSHA256     string `json:"config_sha256"`
}

type replayDecoder interface {
	Identity() DecoderIdentity
	Decode(context.Context, harness.RawExecution) (harness.Observation, error)
}

// CodexDecoder is the sole publishable replay implementation. Its private
// adapter binding prevents a caller-supplied in-process implementation from
// self-attesting arbitrary observations.
type CodexDecoder struct {
	adapter *harnesscodex.Adapter
}

// NewCodexDecoder binds replay to the exact built-in Codex adapter instance
// used by the capture's runtime layout.
func NewCodexDecoder(adapter *harnesscodex.Adapter) (*CodexDecoder, error) {
	if adapter == nil {
		return nil, errors.New("Codex replay adapter is required")
	}
	if _, err := adapter.RuntimeLayout(); err != nil {
		return nil, fmt.Errorf("validate Codex replay adapter: %w", err)
	}
	return &CodexDecoder{adapter: adapter}, nil
}

// ReplayAttempt references a newly decoded observation, or retains the
// capture's original ordinary execution failure without invoking a decoder.
type ReplayAttempt struct {
	Observation *cas.ObjectRef             `json:"observation,omitempty"`
	Failure     *tokenbench.AttemptFailure `json:"failure,omitempty"`
	Arm         tokenbench.Arm             `json:"arm"`
}

// ReplayManifest is a deterministic derived subject. Parent is the exact
// attested capture root, not an unsigned capture subject.
type ReplayManifest struct {
	Parent          cas.ObjectRef   `json:"parent"`
	Baseline        ReplayAttempt   `json:"baseline"`
	Candidate       ReplayAttempt   `json:"candidate"`
	DecoderIdentity DecoderIdentity `json:"decoder_identity"`
	SchemaVersion   string          `json:"schema_version"`
}

// Replay is a recursively verified derived result.
type Replay struct {
	Root        cas.ObjectRef
	Subject     cas.ObjectRef
	Attestation AttestationEnvelope
	Manifest    ReplayManifest
	Capture     Capture
	Baseline    *harness.Observation
	Candidate   *harness.Observation
}

// ReplayCapture loads and authenticates parent, invokes only the supplied
// offline decoder for successful arms, validates the reconstructed run, and
// publishes a new linked root. A decoder failure publishes nothing.
func ReplayCapture(
	ctx context.Context,
	store *cas.Store,
	parent cas.ObjectRef,
	verifier *Verifier,
	decoder *CodexDecoder,
	signer AttestationSigner,
) (PublicationResult, error) {
	if decoder == nil {
		return retryablePublication(), errors.New("built-in Codex replay decoder is required")
	}
	authorizedSigner, err := authorizeAttestationSigner(signer, verifier, ReplayBundle)
	if err != nil {
		return retryablePublication(), err
	}
	capture, err := LoadCapture(ctx, store, parent, verifier)
	if err != nil {
		return retryablePublication(), fmt.Errorf("load replay parent: %w", err)
	}
	identity, err := decoder.identity(ctx, capture)
	if err != nil {
		return retryablePublication(), err
	}
	return replayLoadedCapture(
		ctx, store, parent, capture, verifier, identity, decoder.adapter, authorizedSigner,
	)
}

func replayCaptureWithDecoder(
	ctx context.Context,
	store *cas.Store,
	parent cas.ObjectRef,
	verifier *Verifier,
	decoder replayDecoder,
	signer AttestationSigner,
) (PublicationResult, error) {
	if decoder == nil {
		return retryablePublication(), errors.New("replay decoder is required")
	}
	identity := decoder.Identity()
	if err := validateDecoderIdentity(identity); err != nil {
		return retryablePublication(), err
	}
	authorizedSigner, err := authorizeAttestationSigner(signer, verifier, ReplayBundle)
	if err != nil {
		return retryablePublication(), err
	}
	capture, err := LoadCapture(ctx, store, parent, verifier)
	if err != nil {
		return retryablePublication(), fmt.Errorf("load replay parent: %w", err)
	}
	return replayLoadedCapture(
		ctx, store, parent, capture, verifier, identity, decoder, authorizedSigner,
	)
}

func replayLoadedCapture(
	ctx context.Context,
	store *cas.Store,
	parent cas.ObjectRef,
	capture Capture,
	verifier *Verifier,
	identity DecoderIdentity,
	decoder interface {
		Decode(context.Context, harness.RawExecution) (harness.Observation, error)
	},
	signer AttestationSigner,
) (PublicationResult, error) {
	if identity.Schema == "" || identity.Schema != capture.Run.Plan.Baseline.HarnessIdentity.DecoderSchema {
		return retryablePublication(), fmt.Errorf(
			"replay decoder schema %q does not match capture schema %q",
			identity.Schema,
			capture.Run.Plan.Baseline.HarnessIdentity.DecoderSchema,
		)
	}

	reconstructed := capture.Run
	baselineObservation, err := replayAttempt(ctx, decoder, capture.Run.Baseline)
	if err != nil {
		return retryablePublication(), fmt.Errorf("decode baseline replay: %w", err)
	}
	candidateObservation, err := replayAttempt(ctx, decoder, capture.Run.Candidate)
	if err != nil {
		return retryablePublication(), fmt.Errorf("decode candidate replay: %w", err)
	}
	if replayableAttempt(capture.Run.Baseline) {
		reconstructed.Baseline.Failure = nil
		reconstructed.Baseline.Observation = baselineObservation
	}
	if replayableAttempt(capture.Run.Candidate) {
		reconstructed.Candidate.Failure = nil
		reconstructed.Candidate.Observation = candidateObservation
	}
	if err := reconstructed.Validate(); err != nil {
		return retryablePublication(), fmt.Errorf("validate replayed observations: %w", err)
	}
	// Reauthenticate the full parent graph immediately before staging derived
	// objects. This closes the decoder-time dependency window even for a store
	// whose backing files are being attacked concurrently.
	reverified, err := LoadCapture(ctx, store, parent, verifier)
	if err != nil {
		return retryablePublication(), fmt.Errorf("reverify replay parent: %w", err)
	}
	initialDigest, err := tokenbench.JSONSHA256(capture.Run)
	if err != nil {
		return retryablePublication(), err
	}
	reverifiedDigest, err := tokenbench.JSONSHA256(reverified.Run)
	if err != nil || initialDigest != reverifiedDigest {
		return retryablePublication(), errors.New("replay parent changed during decoding")
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
	baseline, err := putReplayAttempt(
		ctx,
		transaction,
		capture.Run.Baseline,
		baselineObservation,
	)
	if err != nil {
		return retryablePublication(), err
	}
	candidate, err := putReplayAttempt(
		ctx,
		transaction,
		capture.Run.Candidate,
		candidateObservation,
	)
	if err != nil {
		return retryablePublication(), err
	}
	objects := publicationObjectSet{}
	objects.addCapture(capture)
	if baseline.Observation != nil {
		objects.add(*baseline.Observation)
	}
	if candidate.Observation != nil {
		objects.add(*candidate.Observation)
	}
	manifest := ReplayManifest{
		Parent:          parent,
		Baseline:        baseline,
		Candidate:       candidate,
		DecoderIdentity: identity,
		SchemaVersion:   ReplaySchemaVersion,
	}
	subject, err := putJSON(ctx, transaction, replayMediaType, manifest)
	if err != nil {
		return retryablePublication(), err
	}
	objects.add(subject)
	root, err := putAttestation(
		ctx,
		transaction,
		signer,
		ReplayBundle,
		subject,
		[]cas.ObjectRef{parent},
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

func (decoder *CodexDecoder) identity(
	ctx context.Context,
	capture Capture,
) (DecoderIdentity, error) {
	if decoder == nil || decoder.adapter == nil {
		return DecoderIdentity{}, errors.New("Codex replay decoder is not configured")
	}
	invocation := capture.Run.Plan.Baseline
	if invocation.HarnessIdentity.Kind != "codex" ||
		capture.Run.Plan.Candidate.HarnessIdentity != invocation.HarnessIdentity {
		return DecoderIdentity{}, errors.New("capture does not use one common Codex harness identity")
	}
	resolved, err := decoder.adapter.Resolve(ctx, harness.ResolveRequest{
		Environment:            cloneStringMap(invocation.Environment),
		Executable:             invocation.Executable,
		ExecutableSHA256:       invocation.ExecutableSHA256,
		Model:                  invocation.RequestedModel,
		ExpectedModelRevision:  invocation.ModelRevision,
		ReasoningEffort:        invocation.ReasoningEffort,
		PermissionProfile:      invocation.PermissionProfile,
		DeveloperInstructions:  invocation.DeveloperInstructions,
		WorkingDirectory:       invocation.WorkingDirectory,
		SourceRevision:         invocation.SourceRevision,
		SourceBaseRevision:     invocation.SourceBaseRevision,
		SourceTreeSHA256:       invocation.SourceTreeSHA256,
		GitExecutable:          invocation.GitExecutable,
		GitExecutableSHA256:    invocation.GitExecutableSHA256,
		GitMetadataSHA256:      invocation.GitMetadataSHA256,
		RunnerExecutable:       invocation.RunnerExecutable,
		RunnerExecutableSHA256: invocation.RunnerExecutableSHA256,
		TimeoutMillis:          invocation.TimeoutMillis,
	})
	if err != nil {
		return DecoderIdentity{}, fmt.Errorf("resolve built-in Codex replay decoder: %w", err)
	}
	if resolved != invocation.HarnessIdentity {
		return DecoderIdentity{}, errors.New("built-in Codex replay decoder differs from capture identity")
	}
	identity := DecoderIdentity{
		Kind:             "codex",
		Version:          resolved.AdapterVersion,
		Schema:           resolved.DecoderSchema,
		ExecutableSHA256: resolved.AdapterExecutableSHA256,
		ConfigSHA256:     resolved.AdapterConfigSHA256,
	}
	if err := validateDecoderIdentity(identity); err != nil {
		return DecoderIdentity{}, err
	}
	return identity, nil
}

// LoadReplay recursively authenticates a replay, its observations, and its
// parent capture, then validates the resulting normalized run.
func LoadReplay(
	ctx context.Context,
	store *cas.Store,
	root cas.ObjectRef,
	verifier *Verifier,
) (Replay, error) {
	verified, err := VerifyEvidence(ctx, store, root, verifier)
	if err != nil {
		return Replay{}, err
	}
	if verified.Replay == nil || verified.BundleKind != ReplayBundle {
		return Replay{}, fmt.Errorf("%w: root is not a replay", ErrInvalidAttestation)
	}
	return *verified.Replay, nil
}

func loadReplaySubject(
	ctx context.Context,
	store *cas.Store,
	root cas.ObjectRef,
	envelope AttestationEnvelope,
	verifier *Verifier,
	state *verificationState,
	depth int,
) (Replay, error) {
	subject := envelope.Statement.Subject
	if err := preflightObject(subject, maxReplayManifestBytes, "replay manifest"); err != nil {
		return Replay{}, err
	}
	raw, err := store.Read(ctx, subject)
	if err != nil {
		return Replay{}, err
	}
	var manifest ReplayManifest
	if err := decodeStrict(raw, &manifest); err != nil {
		return Replay{}, fmt.Errorf("decode replay manifest: %w", err)
	}
	if manifest.SchemaVersion != ReplaySchemaVersion {
		return Replay{}, fmt.Errorf("unexpected replay schema %q", manifest.SchemaVersion)
	}
	if err := validateDecoderIdentity(manifest.DecoderIdentity); err != nil {
		return Replay{}, err
	}
	if len(envelope.Statement.Parents) != 1 ||
		manifest.Parent != envelope.Statement.Parents[0] {
		return Replay{}, fmt.Errorf(
			"%w: replay manifest parent differs from signed lineage",
			ErrInvalidAttestation,
		)
	}
	parent, err := verifyEvidenceRoot(
		ctx,
		store,
		manifest.Parent,
		verifier,
		state,
		depth+1,
	)
	if err != nil {
		return Replay{}, fmt.Errorf("load replay parent: %w", err)
	}
	if parent.Capture == nil || parent.BundleKind != CaptureBundle {
		return Replay{}, fmt.Errorf("%w: replay parent is not a capture", ErrInvalidAttestation)
	}
	capture := *parent.Capture
	if err := validateConformantDecoderIdentity(manifest.DecoderIdentity, capture); err != nil {
		return Replay{}, err
	}
	baseline, err := loadReplayAttempt(ctx, store, manifest.Baseline, capture.Run.Baseline)
	if err != nil {
		return Replay{}, fmt.Errorf("load replay baseline: %w", err)
	}
	candidate, err := loadReplayAttempt(ctx, store, manifest.Candidate, capture.Run.Candidate)
	if err != nil {
		return Replay{}, fmt.Errorf("load replay candidate: %w", err)
	}
	reconstructed := capture.Run
	if replayableAttempt(capture.Run.Baseline) {
		reconstructed.Baseline.Failure = nil
		reconstructed.Baseline.Observation = baseline
	}
	if replayableAttempt(capture.Run.Candidate) {
		reconstructed.Candidate.Failure = nil
		reconstructed.Candidate.Observation = candidate
	}
	if err := reconstructed.Validate(); err != nil {
		return Replay{}, fmt.Errorf("validate loaded replay: %w", err)
	}
	return Replay{
		Root:        root,
		Subject:     subject,
		Attestation: envelope,
		Manifest:    manifest,
		Capture:     capture,
		Baseline:    baseline,
		Candidate:   candidate,
	}, nil
}

func validateConformantDecoderIdentity(identity DecoderIdentity, capture Capture) error {
	harnessIdentity := capture.Run.Plan.Baseline.HarnessIdentity
	if identity.Kind != "codex" ||
		identity.Version != harnessIdentity.AdapterVersion ||
		identity.Schema != harnessIdentity.DecoderSchema ||
		identity.ExecutableSHA256 != harnessIdentity.AdapterExecutableSHA256 ||
		identity.ConfigSHA256 != harnessIdentity.AdapterConfigSHA256 {
		return fmt.Errorf(
			"%w: replay decoder does not match the attested built-in Codex capture",
			ErrInvalidAttestation,
		)
	}
	return nil
}

func replayAttempt(
	ctx context.Context,
	decoder interface {
		Decode(context.Context, harness.RawExecution) (harness.Observation, error)
	},
	attempt tokenbench.ArmRun,
) (*harness.Observation, error) {
	if !replayableAttempt(attempt) {
		return nil, nil
	}
	observation, err := decoder.Decode(ctx, cloneRaw(attempt.Raw))
	if err != nil {
		return nil, err
	}
	second, err := decoder.Decode(ctx, cloneRaw(attempt.Raw))
	if err != nil {
		return nil, fmt.Errorf("repeat decoder invocation: %w", err)
	}
	if observation.ToolCalls == nil || second.ToolCalls == nil {
		return nil, errors.New("replay decoder returned a noncanonical nil tool-call list")
	}
	if !reflect.DeepEqual(observation, second) {
		return nil, errors.New("replay decoder is nondeterministic")
	}
	clone := observation
	clone.ToolCalls = append(make([]string, 0, len(observation.ToolCalls)), observation.ToolCalls...)
	return &clone, nil
}

func cloneStringMap(source map[string]string) map[string]string {
	result := make(map[string]string, len(source))
	for key, value := range source {
		result[key] = value
	}
	return result
}

func putReplayAttempt(
	ctx context.Context,
	transaction *cas.Transaction,
	attempt tokenbench.ArmRun,
	observation *harness.Observation,
) (ReplayAttempt, error) {
	result := ReplayAttempt{Arm: attempt.Arm}
	if observation != nil {
		ref, err := putJSON(ctx, transaction, observationMediaType, observation)
		if err != nil {
			return ReplayAttempt{}, err
		}
		result.Observation = &ref
	} else {
		result.Failure = cloneFailure(attempt.Failure)
	}
	return result, nil
}

func loadReplayAttempt(
	ctx context.Context,
	store *cas.Store,
	attempt ReplayAttempt,
	parent tokenbench.ArmRun,
) (*harness.Observation, error) {
	if attempt.Arm != parent.Arm {
		return nil, errors.New("replay arm does not match parent")
	}
	if !replayableAttempt(parent) {
		if attempt.Observation != nil || attempt.Failure == nil ||
			*attempt.Failure != *parent.Failure {
			return nil, errors.New("replay did not preserve parent execution failure")
		}
		return nil, nil
	}
	if attempt.Failure != nil || attempt.Observation == nil {
		return nil, errors.New("replayable parent arm requires a replay observation")
	}
	if attempt.Observation.MediaType != observationMediaType {
		return nil, errors.New("replay observation media type is invalid")
	}
	if err := preflightObject(*attempt.Observation, maxObservationBytes, "replay observation"); err != nil {
		return nil, err
	}
	raw, err := store.Read(ctx, *attempt.Observation)
	if err != nil {
		return nil, err
	}
	var observation harness.Observation
	if err := decodeStrict(raw, &observation); err != nil {
		return nil, err
	}
	return &observation, nil
}

func replayableAttempt(attempt tokenbench.ArmRun) bool {
	return attempt.Failure == nil || attempt.Failure.Kind == "decoder_error" ||
		attempt.Failure.Kind == "invalid_observation"
}

func validateDecoderIdentity(identity DecoderIdentity) error {
	switch {
	case identity.Kind == "":
		return errors.New("decoder identity kind is required")
	case identity.Version == "":
		return errors.New("decoder identity version is required")
	case identity.Schema == "":
		return errors.New("decoder identity schema is required")
	case !tokenbench.ValidSHA256(identity.ExecutableSHA256):
		return errors.New("decoder executable digest is invalid")
	case !tokenbench.ValidSHA256(identity.ConfigSHA256):
		return errors.New("decoder configuration digest is invalid")
	default:
		return nil
	}
}

func cloneRaw(source harness.RawExecution) harness.RawExecution {
	clone := source
	clone.Stdout = append([]byte(nil), source.Stdout...)
	clone.Stderr = append([]byte(nil), source.Stderr...)
	clone.Artifacts = make([]harness.Artifact, len(source.Artifacts))
	for index, artifact := range source.Artifacts {
		clone.Artifacts[index] = artifact
		clone.Artifacts[index].Data = append([]byte(nil), artifact.Data...)
	}
	clone.Resources = harness.CloneResourceOutcome(source.Resources)
	return clone
}
