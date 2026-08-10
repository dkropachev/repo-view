package evidence

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"time"

	"github.com/dkropachev/repo-view/benchmarks/tokenbench/cas"
)

const (
	AttestationSchemaVersion          = "tokenbench.attestation/v1"
	AttestationStatementSchemaVersion = "tokenbench.attestation-statement/v1"
	AttestationProject                = "github.com/dkropachev/repo-view/benchmarks/tokenbench"

	attestationMediaType = "application/vnd.tokenbench.attestation.v1+json"
	attestationDomain    = "repo-view/tokenbench/attestation/ed25519/v1\x00"

	maxAttestationBytes        = 64 << 10
	maxLineageDepth            = 16
	keyIDPrefix                = "ed25519-sha256:"
	publicationRecoveryTimeout = 30 * time.Second
)

var (
	// ErrInvalidAttestation marks a malformed envelope, invalid signature, or
	// mismatch between a signed statement and the recursively loaded subject.
	ErrInvalidAttestation = errors.New("invalid tokenbench attestation")

	// ErrUntrustedAttestation means no out-of-band trust-policy authorization
	// permits the signing key to attest the requested bundle kind.
	ErrUntrustedAttestation = errors.New("untrusted tokenbench attestation")

	// ErrRevokedAttestation means the current trust policy explicitly revoked
	// the signing key. Revocation is intentionally retroactive in v1.
	ErrRevokedAttestation = errors.New("revoked tokenbench attestation key")

	// ErrRetiredAttestation means the current policy no longer permits the key
	// to create or authenticate evidence. Historical verification requires an
	// independently authenticated historical policy or trusted timestamp.
	ErrRetiredAttestation = errors.New("retired tokenbench attestation key")

	// ErrAttestationSigning deliberately hides errors returned by a signer so
	// a private-key implementation cannot leak secret material through errors.
	ErrAttestationSigning = errors.New("tokenbench attestation signing failed")
)

// PublicationState is the recovery-relevant state of one intended root.
type PublicationState string

const (
	PublicationComplete      PublicationState = "complete"
	PublicationVisible       PublicationState = "visible"
	PublicationIndeterminate PublicationState = "indeterminate"
	PublicationRetryable     PublicationState = "retryable"
)

// PublicationResult is authoritative even when publication also returns an
// error. Once IntendedRoot is constructed it is never discarded. Complete is
// the sole state that proves a durable, recursively verified graph and needs no
// recovery; every other state retains live publication authority for retry.
type PublicationResult struct {
	IntendedRoot     cas.ObjectRef    `json:"intended_root"`
	UncertainObject  *cas.ObjectRef   `json:"uncertain_object,omitempty"`
	UncertainStage   string           `json:"uncertain_stage,omitempty"`
	State            PublicationState `json:"state"`
	Durable          bool             `json:"durable"`
	GraphVerified    bool             `json:"graph_verified"`
	RecoveryRequired bool             `json:"recovery_required"`
	CleanupWarning   bool             `json:"cleanup_warning"`
}

// Validate enforces the state invariants before an operational result is
// persisted as either a canonical root or a recovery record.
func (result PublicationResult) Validate() error {
	if result.IntendedRoot.Digest == "" {
		if result.State != PublicationRetryable || result.Durable ||
			result.GraphVerified || !result.RecoveryRequired ||
			result.UncertainObject != nil || result.CleanupWarning {
			return errors.New("rootless publication result is not canonical retryable state")
		}
	} else if err := result.IntendedRoot.Validate(); err != nil {
		return fmt.Errorf("publication intended root: %w", err)
	}
	if (result.UncertainObject == nil) != (result.UncertainStage == "") {
		return errors.New("publication uncertainty object and stage must appear together")
	}
	if result.UncertainObject != nil {
		if err := result.UncertainObject.Validate(); err != nil {
			return fmt.Errorf("publication uncertain object: %w", err)
		}
	}
	switch result.State {
	case PublicationComplete:
		if !result.Durable || !result.GraphVerified || result.RecoveryRequired ||
			result.UncertainObject != nil {
			return errors.New("complete publication lacks durable verified finality")
		}
	case PublicationVisible, PublicationIndeterminate, PublicationRetryable:
		if !result.RecoveryRequired || result.Durable && result.GraphVerified {
			return errors.New("incomplete publication state violates recovery invariants")
		}
		if result.IntendedRoot.Digest != "" && result.UncertainObject == nil {
			return errors.New("rootful incomplete publication lacks an exact uncertainty")
		}
	default:
		return errors.New("publication state is invalid")
	}
	return nil
}

// BundleKind is the signed semantic role of one evidence subject.
type BundleKind string

const (
	CaptureBundle BundleKind = "capture"
	ReplayBundle  BundleKind = "replay"
)

// AttestationStatement is the complete canonical payload signed by Ed25519.
// Subject commits the full transitive evidence graph. Parents always refer to
// attestation envelopes, never unsigned capture or replay subjects.
type AttestationStatement struct {
	SchemaVersion string          `json:"schema_version"`
	Project       string          `json:"project"`
	KeyID         string          `json:"key_id"`
	BundleKind    BundleKind      `json:"bundle_kind"`
	Subject       cas.ObjectRef   `json:"subject"`
	Parents       []cas.ObjectRef `json:"parents"`
}

// AttestationEnvelope is the only conformant public evidence root. Public key
// material is deliberately absent: trust always comes from an out-of-band
// Verifier, never from the evidence being verified.
type AttestationEnvelope struct { //nolint:govet,nolintlint // Field order defines canonical signed JSON.
	SchemaVersion string               `json:"schema_version"`
	Statement     AttestationStatement `json:"statement"`
	Signature     string               `json:"signature"`
}

// AttestationSigner supplies one Ed25519 signature only at final evidence
// publication. Implementations must not place private configuration in their
// public key, signature, or returned errors.
type AttestationSigner interface {
	AttestationPublicKey() ed25519.PublicKey
	SignAttestation(context.Context, []byte) ([]byte, error)
}

// Ed25519Signer is an in-memory signer whose private key is never serializable.
// Official automation should still isolate the key from benchmark children;
// this type prevents publication, not same-process memory compromise.
type Ed25519Signer struct {
	privateKey ed25519.PrivateKey
}

// NewEd25519Signer validates and defensively copies a standard Ed25519 private
// key. It rejects a key whose embedded public half does not match its seed.
func NewEd25519Signer(privateKey ed25519.PrivateKey) (*Ed25519Signer, error) {
	if len(privateKey) != ed25519.PrivateKeySize {
		return nil, errors.New("Ed25519 signing key has an invalid size")
	}
	derived := ed25519.NewKeyFromSeed(privateKey[:ed25519.SeedSize])
	if subtle.ConstantTimeCompare(derived, privateKey) != 1 {
		return nil, errors.New("Ed25519 signing key is internally inconsistent")
	}
	return &Ed25519Signer{privateKey: append(ed25519.PrivateKey(nil), derived...)}, nil
}

// AttestationPublicKey returns a defensive copy containing no private bytes.
func (signer *Ed25519Signer) AttestationPublicKey() ed25519.PublicKey {
	if signer == nil || len(signer.privateKey) != ed25519.PrivateKeySize {
		return nil
	}
	publicKey := signer.privateKey.Public().(ed25519.PublicKey)
	return append(ed25519.PublicKey(nil), publicKey...)
}

// SignAttestation signs exact domain-separated canonical statement bytes.
func (signer *Ed25519Signer) SignAttestation(
	ctx context.Context,
	message []byte,
) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if signer == nil || len(signer.privateKey) != ed25519.PrivateKeySize {
		return nil, ErrAttestationSigning
	}
	signature := ed25519.Sign(signer.privateKey, message)
	return append([]byte(nil), signature...), nil
}

// MarshalJSON prevents accidental inclusion of a signer in serializable
// evidence or configuration.
func (*Ed25519Signer) MarshalJSON() ([]byte, error) {
	return nil, errors.New("tokenbench attestation signer is not serializable")
}

func (signer *Ed25519Signer) String() string {
	publicKey := signer.AttestationPublicKey()
	if len(publicKey) != ed25519.PublicKeySize {
		return "tokenbench Ed25519 signer (invalid)"
	}
	return "tokenbench Ed25519 signer " + attestationKeyID(publicKey)
}

func (signer *Ed25519Signer) GoString() string { return signer.String() }

// VerifiedEvidence is the typed result of authenticating one complete root.
// Exactly one of Capture or Replay is non-nil.
type VerifiedEvidence struct {
	Capture           *Capture
	Replay            *Replay
	Root              cas.ObjectRef
	Subject           cas.ObjectRef
	BundleKind        BundleKind
	KeyID             string
	TrustPolicySHA256 string
	Parents           []cas.ObjectRef
}

// VerifyEvidence authenticates the envelope with an out-of-band trust policy,
// then recursively integrity-checks and semantically validates its subject and
// complete signed lineage.
func VerifyEvidence(
	ctx context.Context,
	store *cas.Store,
	root cas.ObjectRef,
	verifier *Verifier,
) (VerifiedEvidence, error) {
	if store == nil {
		return VerifiedEvidence{}, errors.New("evidence store is required")
	}
	if verifier == nil {
		return VerifiedEvidence{}, errors.New("attestation verifier is required")
	}
	state := &verificationState{visiting: make(map[cas.ObjectRef]struct{})}
	return verifyEvidenceRoot(ctx, store, root, verifier, state, 0)
}

type verificationState struct {
	visiting map[cas.ObjectRef]struct{}
}

func verifyEvidenceRoot(
	ctx context.Context,
	store *cas.Store,
	root cas.ObjectRef,
	verifier *Verifier,
	state *verificationState,
	depth int,
) (VerifiedEvidence, error) {
	if depth > maxLineageDepth {
		return VerifiedEvidence{}, fmt.Errorf("%w: lineage exceeds maximum depth", ErrInvalidAttestation)
	}
	if _, exists := state.visiting[root]; exists {
		return VerifiedEvidence{}, fmt.Errorf("%w: lineage contains a cycle", ErrInvalidAttestation)
	}
	state.visiting[root] = struct{}{}
	defer delete(state.visiting, root)

	envelope, err := loadVerifiedEnvelope(ctx, store, root, verifier)
	if err != nil {
		return VerifiedEvidence{}, err
	}
	result := VerifiedEvidence{
		Root:              root,
		Subject:           envelope.Statement.Subject,
		BundleKind:        envelope.Statement.BundleKind,
		KeyID:             envelope.Statement.KeyID,
		TrustPolicySHA256: verifier.PolicySHA256(),
		Parents:           append([]cas.ObjectRef(nil), envelope.Statement.Parents...),
	}
	switch envelope.Statement.BundleKind {
	case CaptureBundle:
		capture, err := loadCaptureSubject(ctx, store, root, envelope)
		if err != nil {
			return VerifiedEvidence{}, err
		}
		result.Capture = &capture
	case ReplayBundle:
		replay, err := loadReplaySubject(
			ctx,
			store,
			root,
			envelope,
			verifier,
			state,
			depth,
		)
		if err != nil {
			return VerifiedEvidence{}, err
		}
		result.Replay = &replay
	default:
		return VerifiedEvidence{}, fmt.Errorf("%w: unsupported bundle kind", ErrInvalidAttestation)
	}
	return result, nil
}

func loadVerifiedEnvelope(
	ctx context.Context,
	store *cas.Store,
	root cas.ObjectRef,
	verifier *Verifier,
) (AttestationEnvelope, error) {
	if root.MediaType != attestationMediaType {
		return AttestationEnvelope{}, fmt.Errorf(
			"%w: conformant root must be an attestation envelope",
			ErrInvalidAttestation,
		)
	}
	if err := preflightObject(root, maxAttestationBytes, "attestation envelope"); err != nil {
		return AttestationEnvelope{}, fmt.Errorf("%w: %w", ErrInvalidAttestation, err)
	}
	raw, err := store.Read(ctx, root)
	if err != nil {
		return AttestationEnvelope{}, err
	}
	var envelope AttestationEnvelope
	if err := decodeStrict(raw, &envelope); err != nil {
		return AttestationEnvelope{}, fmt.Errorf("%w: decode envelope: %w", ErrInvalidAttestation, err)
	}
	if envelope.SchemaVersion != AttestationSchemaVersion {
		return AttestationEnvelope{}, fmt.Errorf("%w: unsupported envelope schema", ErrInvalidAttestation)
	}
	if err := validateAttestationStatement(envelope.Statement); err != nil {
		return AttestationEnvelope{}, err
	}
	signature, err := base64.RawURLEncoding.DecodeString(envelope.Signature)
	if err != nil || len(signature) != ed25519.SignatureSize ||
		base64.RawURLEncoding.EncodeToString(signature) != envelope.Signature {
		return AttestationEnvelope{}, fmt.Errorf("%w: signature encoding is invalid", ErrInvalidAttestation)
	}
	message, err := attestationSigningMessage(envelope.Statement)
	if err != nil {
		return AttestationEnvelope{}, err
	}
	if err := verifier.verify(
		envelope.Statement.BundleKind,
		envelope.Statement.KeyID,
		message,
		signature,
	); err != nil {
		return AttestationEnvelope{}, err
	}
	envelope.Statement.Parents = append([]cas.ObjectRef(nil), envelope.Statement.Parents...)
	return envelope, nil
}

func validateAttestationStatement(statement AttestationStatement) error {
	if statement.SchemaVersion != AttestationStatementSchemaVersion ||
		statement.Project != AttestationProject {
		return fmt.Errorf("%w: statement context is invalid", ErrInvalidAttestation)
	}
	if !validAttestationKeyID(statement.KeyID) {
		return fmt.Errorf("%w: statement key id is invalid", ErrInvalidAttestation)
	}
	if statement.Parents == nil {
		return fmt.Errorf("%w: statement parents must be a canonical array", ErrInvalidAttestation)
	}
	if err := statement.Subject.Validate(); err != nil {
		return fmt.Errorf("%w: statement subject: %w", ErrInvalidAttestation, err)
	}
	switch statement.BundleKind {
	case CaptureBundle:
		if statement.Subject.MediaType != captureMediaType || len(statement.Parents) != 0 {
			return fmt.Errorf("%w: capture statement shape is invalid", ErrInvalidAttestation)
		}
		if statement.Subject.Size > maxCaptureManifestBytes {
			return fmt.Errorf("%w: capture subject is too large", ErrInvalidAttestation)
		}
	case ReplayBundle:
		if statement.Subject.MediaType != replayMediaType || len(statement.Parents) != 1 {
			return fmt.Errorf("%w: replay statement shape is invalid", ErrInvalidAttestation)
		}
		if statement.Subject.Size > maxReplayManifestBytes {
			return fmt.Errorf("%w: replay subject is too large", ErrInvalidAttestation)
		}
	default:
		return fmt.Errorf("%w: statement bundle kind is invalid", ErrInvalidAttestation)
	}
	for _, parent := range statement.Parents {
		if parent.MediaType != attestationMediaType {
			return fmt.Errorf("%w: parent is not an attestation root", ErrInvalidAttestation)
		}
		if err := preflightObject(parent, maxAttestationBytes, "attestation parent"); err != nil {
			return fmt.Errorf("%w: %w", ErrInvalidAttestation, err)
		}
	}
	return nil
}

func newAttestationEnvelope(
	ctx context.Context,
	signer AttestationSigner,
	kind BundleKind,
	subject cas.ObjectRef,
	parents []cas.ObjectRef,
) (AttestationEnvelope, error) {
	if signer == nil || isNilInterface(signer) {
		return AttestationEnvelope{}, errors.New("attestation signer is required")
	}
	publicKey := append(ed25519.PublicKey(nil), signer.AttestationPublicKey()...)
	if len(publicKey) != ed25519.PublicKeySize {
		return AttestationEnvelope{}, errors.New("attestation signer public key is invalid")
	}
	statement := AttestationStatement{
		SchemaVersion: AttestationStatementSchemaVersion,
		Project:       AttestationProject,
		KeyID:         attestationKeyID(publicKey),
		BundleKind:    kind,
		Subject:       subject,
		Parents:       append([]cas.ObjectRef(nil), parents...),
	}
	if statement.Parents == nil {
		statement.Parents = make([]cas.ObjectRef, 0)
	}
	if err := validateAttestationStatement(statement); err != nil {
		return AttestationEnvelope{}, err
	}
	message, err := attestationSigningMessage(statement)
	if err != nil {
		return AttestationEnvelope{}, err
	}
	if err := ctx.Err(); err != nil {
		return AttestationEnvelope{}, err
	}
	signature, signErr := signer.SignAttestation(ctx, append([]byte(nil), message...))
	if err := ctx.Err(); err != nil {
		return AttestationEnvelope{}, err
	}
	if signErr != nil || len(signature) != ed25519.SignatureSize ||
		!ed25519.Verify(publicKey, message, signature) {
		return AttestationEnvelope{}, ErrAttestationSigning
	}
	return AttestationEnvelope{
		Statement:     statement,
		Signature:     base64.RawURLEncoding.EncodeToString(signature),
		SchemaVersion: AttestationSchemaVersion,
	}, nil
}

func putAttestation(
	ctx context.Context,
	transaction *cas.Transaction,
	signer AttestationSigner,
	kind BundleKind,
	subject cas.ObjectRef,
	parents []cas.ObjectRef,
) (cas.ObjectRef, error) {
	envelope, err := newAttestationEnvelope(ctx, signer, kind, subject, parents)
	if err != nil {
		return cas.ObjectRef{}, err
	}
	return putJSON(ctx, transaction, attestationMediaType, envelope)
}

type authorizedAttestationSigner struct {
	signer    AttestationSigner
	publicKey ed25519.PublicKey
}

func (signer authorizedAttestationSigner) AttestationPublicKey() ed25519.PublicKey {
	return append(ed25519.PublicKey(nil), signer.publicKey...)
}

func (signer authorizedAttestationSigner) SignAttestation(
	ctx context.Context,
	message []byte,
) ([]byte, error) {
	return signer.signer.SignAttestation(ctx, message)
}

func authorizeAttestationSigner(
	signer AttestationSigner,
	verifier *Verifier,
	kind BundleKind,
) (AttestationSigner, error) {
	if signer == nil || isNilInterface(signer) {
		return nil, errors.New("attestation signer is required")
	}
	publicKey := append(ed25519.PublicKey(nil), signer.AttestationPublicKey()...)
	if len(publicKey) != ed25519.PublicKeySize {
		return nil, errors.New("attestation signer public key is invalid")
	}
	if _, err := verifier.authorize(kind, attestationKeyID(publicKey)); err != nil {
		return nil, fmt.Errorf("authorize %s attestation signer: %w", kind, err)
	}
	return authorizedAttestationSigner{signer: signer, publicKey: publicKey}, nil
}

// commitAttestedRoot preserves the intended root and resolves transient
// visibility/durability uncertainty with bounded, idempotent verification.
func commitAttestedRoot(
	ctx context.Context,
	store *cas.Store,
	transaction *cas.Transaction,
	root cas.ObjectRef,
	verifier *Verifier,
	objects []cas.ObjectRef,
) (PublicationResult, error) {
	commit, commitErr := transaction.CommitDetailed(ctx, root)
	return resolveAttestedCommit(store, root, verifier, objects, commit, commitErr)
}

func resolveAttestedCommit(
	store *cas.Store,
	root cas.ObjectRef,
	verifier *Verifier,
	objects []cas.ObjectRef,
	commit cas.CommitResult,
	commitErr error,
) (PublicationResult, error) {
	result := PublicationResult{
		IntendedRoot:     root,
		State:            PublicationRetryable,
		RecoveryRequired: true,
	}
	result.CleanupWarning = commit.CleanupPending
	result.UncertainObject = cloneObjectRef(commit.UncertainObject)
	result.UncertainStage = commit.UncertainStage

	recoveryCtx, cancel := context.WithTimeout(context.Background(), publicationRecoveryTimeout)
	defer cancel()
	_, graphErr := VerifyEvidence(recoveryCtx, store, root, verifier)
	result.GraphVerified = graphErr == nil

	// CommitDetailed proves transaction publication order and root durability,
	// but replay evidence also includes an already-existing parent graph. Sync
	// the complete enumerated graph on every path before claiming finality.
	durabilityErr := store.EnsureDurable(recoveryCtx, objects)
	result.Durable = durabilityErr == nil
	if durabilityErr == nil {
		_, graphErr = VerifyEvidence(recoveryCtx, store, root, verifier)
		result.GraphVerified = graphErr == nil
	}
	if durabilityErr != nil {
		var objectErr *cas.ObjectOperationError
		if errors.As(durabilityErr, &objectErr) {
			uncertain := objectErr.Ref
			result.UncertainObject = &uncertain
			result.UncertainStage = "durability_recovery/" + objectErr.Stage
		}
	}
	if result.Durable && result.GraphVerified {
		result.State = PublicationComplete
		result.RecoveryRequired = false
		result.UncertainObject = nil
		result.UncertainStage = ""
		return validatedPublicationResult(result, commitErr)
	}

	rootVisible := store.Verify(recoveryCtx, root) == nil
	switch {
	case rootVisible || result.GraphVerified || commit.State == cas.CommitVisible ||
		commit.State == cas.CommitDurable:
		result.State = PublicationVisible
	case commit.State == cas.CommitIndeterminate:
		result.State = PublicationIndeterminate
	default:
		result.State = PublicationRetryable
	}
	if result.UncertainObject == nil {
		uncertain := root
		result.UncertainObject = &uncertain
		if result.GraphVerified {
			result.UncertainStage = "evidence_graph_durability"
		} else {
			result.UncertainStage = "evidence_graph_verification"
		}
	}
	return validatedPublicationResult(
		result,
		errors.Join(commitErr, durabilityErr, graphErr, recoveryCtx.Err()),
	)
}

func validatedPublicationResult(result PublicationResult, err error) (PublicationResult, error) {
	if validationErr := result.Validate(); validationErr != nil {
		return result, errors.Join(err, validationErr)
	}
	return result, err
}

func cloneObjectRef(source *cas.ObjectRef) *cas.ObjectRef {
	if source == nil {
		return nil
	}
	clone := *source
	return &clone
}

func attestationSigningMessage(statement AttestationStatement) ([]byte, error) {
	canonical, err := json.Marshal(statement)
	if err != nil {
		return nil, fmt.Errorf("%w: encode signed statement", ErrInvalidAttestation)
	}
	message := make([]byte, 0, len(attestationDomain)+len(canonical))
	message = append(message, attestationDomain...)
	message = append(message, canonical...)
	return message, nil
}

func attestationKeyID(publicKey ed25519.PublicKey) string {
	digest := sha256.Sum256(publicKey)
	return keyIDPrefix + hex.EncodeToString(digest[:])
}

func validAttestationKeyID(value string) bool {
	if len(value) != len(keyIDPrefix)+sha256.Size*2 ||
		value[:len(keyIDPrefix)] != keyIDPrefix {
		return false
	}
	decoded, err := hex.DecodeString(value[len(keyIDPrefix):])
	return err == nil && hex.EncodeToString(decoded) == value[len(keyIDPrefix):]
}

func isNilInterface(value any) bool {
	reflected := reflect.ValueOf(value)
	kind := reflected.Kind()
	if kind == reflect.Chan || kind == reflect.Func || kind == reflect.Interface ||
		kind == reflect.Map || kind == reflect.Pointer || kind == reflect.Slice {
		return reflected.IsNil()
	}
	return false
}
