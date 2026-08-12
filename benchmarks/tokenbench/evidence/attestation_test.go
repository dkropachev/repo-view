package evidence

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/yapless/scopesifter/benchmarks/tokenbench/cas"
)

func TestAttestationCanonicalGolden(t *testing.T) {
	seed := sha256.Sum256([]byte("tokenbench attestation golden key"))
	signer, err := NewEd25519Signer(ed25519.NewKeyFromSeed(seed[:]))
	if err != nil {
		t.Fatal(err)
	}
	subject := cas.ObjectRef{
		Digest:    "sha256:" + strings.Repeat("1", 64),
		Size:      123,
		MediaType: captureMediaType,
	}
	envelope, err := newAttestationEnvelope(
		context.Background(), signer, CaptureBundle, subject, []cas.ObjectRef{},
	)
	if err != nil {
		t.Fatal(err)
	}
	statementJSON, err := json.Marshal(envelope.Statement)
	if err != nil {
		t.Fatal(err)
	}
	envelopeJSON, err := json.Marshal(envelope)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(envelopeJSON)
	const wantKeyID = "ed25519-sha256:be0246fd46710425b99307206e9e695c05e04cf833c511f49c6e4841f958baec"
	const wantStatement = `{"schema_version":"tokenbench.attestation-statement/v2","project":"github.com/yapless/scopesifter/benchmarks/tokenbench","key_id":"ed25519-sha256:be0246fd46710425b99307206e9e695c05e04cf833c511f49c6e4841f958baec","bundle_kind":"capture","subject":{"digest":"sha256:1111111111111111111111111111111111111111111111111111111111111111","size":123,"media_type":"application/vnd.tokenbench.capture.v6+json"},"parents":[]}`
	const wantSignature = "8IIn9ihRl3FbZZAHFV5PTMPjWA3jvRktHsu008xp1KuJQryfd2PFgMXydjM2f1lgiyxnNg0Jap0hzLEIWhCtCg"
	const wantRootDigest = "2a0add06502e7bbb2c4045695b2248451f86e8712f7064f7765c7d6d061ae115"
	if envelope.Statement.KeyID != wantKeyID ||
		string(statementJSON) != wantStatement ||
		envelope.Signature != wantSignature ||
		hex.EncodeToString(digest[:]) != wantRootDigest {
		t.Fatalf(
			"attestation golden changed:\nkey=%s\nstatement=%s\nsignature=%s\nroot=%s",
			envelope.Statement.KeyID,
			statementJSON,
			envelope.Signature,
			hex.EncodeToString(digest[:]),
		)
	}
}

func TestPublicationResultStateInvariants(t *testing.T) {
	root := cas.ObjectRef{
		Digest:    "sha256:" + strings.Repeat("1", 64),
		Size:      1,
		MediaType: attestationMediaType,
	}
	uncertain := root
	valid := []PublicationResult{
		retryablePublication(),
		{
			IntendedRoot: root, UncertainObject: &uncertain,
			UncertainStage: "root_durability", State: PublicationVisible,
			GraphVerified: true, RecoveryRequired: true,
		},
		{
			IntendedRoot: root, UncertainObject: &uncertain,
			UncertainStage: "atomic_rename", State: PublicationIndeterminate,
			RecoveryRequired: true,
		},
		{
			IntendedRoot: root, State: PublicationComplete, Durable: true,
			GraphVerified: true, CleanupWarning: true,
		},
	}
	for index, result := range valid {
		if err := result.Validate(); err != nil {
			t.Fatalf("valid result %d rejected: %+v: %v", index, result, err)
		}
	}

	invalid := []PublicationResult{
		{},
		{State: PublicationRetryable, RecoveryRequired: true, CleanupWarning: true},
		{IntendedRoot: root, State: PublicationVisible, RecoveryRequired: true},
		{
			IntendedRoot: root, UncertainObject: &uncertain,
			State: PublicationVisible, RecoveryRequired: true,
		},
		{
			IntendedRoot: root, UncertainObject: &uncertain,
			UncertainStage: "still_uncertain", State: PublicationComplete,
			Durable: true, GraphVerified: true,
		},
		{
			IntendedRoot: root, State: PublicationComplete,
			Durable: true, GraphVerified: false,
		},
		{
			IntendedRoot: root, UncertainObject: &uncertain,
			UncertainStage: "contradiction", State: PublicationRetryable,
			Durable: true, GraphVerified: true, RecoveryRequired: true,
		},
	}
	for index, result := range invalid {
		if err := result.Validate(); err == nil {
			t.Fatalf("invalid result %d accepted: %+v", index, result)
		}
	}
}

func TestAttestationRejectsMutationUnknownRoleAndRevocation(t *testing.T) {
	store := openTestStore(t)
	run := validRun(t)
	signer := testSigner(t, "mutation signer")
	root := publishTestRun(t, store, run, signer)

	t.Run("valid", func(t *testing.T) {
		verifier := testVerifier(t, testTrustKey{
			signer: signer,
			roles:  []BundleKind{CaptureBundle},
			status: KeyActive,
		})
		verified, err := VerifyEvidence(context.Background(), store, root, verifier)
		if err != nil {
			t.Fatal(err)
		}
		if verified.Capture == nil || verified.Replay != nil ||
			verified.Subject != verified.Capture.Subject ||
			verified.TrustPolicySHA256 != verifier.PolicySHA256() {
			t.Fatalf("invalid typed verification result: %+v", verified)
		}
	})

	for _, test := range []struct {
		name     string
		verifier *Verifier
		want     error
	}{
		{
			"unknown key",
			testVerifier(t, testTrustKey{
				signer: testSigner(t, "unknown signer"),
				roles:  []BundleKind{CaptureBundle},
				status: KeyActive,
			}),
			ErrUntrustedAttestation,
		},
		{
			"wrong role",
			testVerifier(t, testTrustKey{
				signer: signer,
				roles:  []BundleKind{ReplayBundle},
				status: KeyActive,
			}),
			ErrUntrustedAttestation,
		},
		{
			"revoked",
			testVerifier(t, testTrustKey{
				signer: signer,
				roles:  []BundleKind{CaptureBundle},
				status: KeyRevoked,
			}),
			ErrRevokedAttestation,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			_, err := VerifyEvidence(context.Background(), store, root, test.verifier)
			if !errors.Is(err, test.want) {
				t.Fatalf("got %v, want %v", err, test.want)
			}
		})
	}

	t.Run("retired is rejected by current policy", func(t *testing.T) {
		verifier := testVerifier(t, testTrustKey{
			signer: signer,
			roles:  []BundleKind{CaptureBundle},
			status: KeyRetired,
		})
		if _, err := VerifyEvidence(context.Background(), store, root, verifier); !errors.Is(err, ErrRetiredAttestation) {
			t.Fatalf("retired key error = %v", err)
		}
	})

	envelope := readTestEnvelope(t, store, root)
	mutations := []struct {
		name   string
		mutate func(*AttestationEnvelope)
	}{
		{"project", func(value *AttestationEnvelope) { value.Statement.Project += "/other" }},
		{"kind", func(value *AttestationEnvelope) { value.Statement.BundleKind = ReplayBundle }},
		{"subject", func(value *AttestationEnvelope) {
			value.Statement.Subject.Digest = "sha256:" + strings.Repeat("2", 64)
		}},
		{"signature", func(value *AttestationEnvelope) {
			if value.Signature[0] == 'A' {
				value.Signature = "B" + value.Signature[1:]
			} else {
				value.Signature = "A" + value.Signature[1:]
			}
		}},
	}
	verifier := testVerifier(t, testTrustKey{
		signer: signer,
		roles:  []BundleKind{CaptureBundle, ReplayBundle},
		status: KeyActive,
	})
	for _, test := range mutations {
		t.Run("mutation "+test.name, func(t *testing.T) {
			mutated := envelope
			mutated.Statement.Parents = append(
				[]cas.ObjectRef(nil), envelope.Statement.Parents...,
			)
			test.mutate(&mutated)
			mutatedRoot := putTestJSONRoot(t, store, attestationMediaType, mutated)
			if _, err := VerifyEvidence(
				context.Background(), store, mutatedRoot, verifier,
			); err == nil {
				t.Fatal("mutated attestation was accepted")
			}
		})
	}
}

func TestUnsignedAndNonconformantCaptureAreRejected(t *testing.T) {
	store := openTestStore(t)
	signer, verifier := testSignerAndVerifier(
		t, []BundleKind{CaptureBundle}, KeyActive,
	)
	run := validRun(t)
	root := publishTestRun(t, store, run, signer)
	capture, err := LoadCapture(context.Background(), store, root, verifier)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadCapture(
		context.Background(), store, capture.Subject, verifier,
	); !errors.Is(err, ErrInvalidAttestation) {
		t.Fatalf("unsigned subject error = %v", err)
	}

	run.ExecutorIdentity.Kind = "fixture"
	result, err := publishRun(context.Background(), store, run, signer, verifier)
	if !errors.Is(err, ErrInvalidAttestation) || result.IntendedRoot.Digest != "" ||
		result.State != PublicationRetryable {
		t.Fatalf("nonconformant publication result = %+v, error = %v", result, err)
	}
}

func TestReplaySignedLineageAndParentRevocation(t *testing.T) {
	store := openTestStore(t)
	run := validRun(t)
	captureSigner := testSigner(t, "capture lineage signer")
	replaySigner := testSigner(t, "replay lineage signer")
	active := testVerifier(t,
		testTrustKey{
			signer: captureSigner,
			roles:  []BundleKind{CaptureBundle},
			status: KeyActive,
		},
		testTrustKey{
			signer: replaySigner,
			roles:  []BundleKind{ReplayBundle},
			status: KeyActive,
		},
	)
	parent := publishTestRun(t, store, run, captureSigner)
	decoder := fixtureDecoder{identity: decoderIdentityForRun(run)}
	replayRoot, err := replayCaptureWithDecoder(
		context.Background(), store, parent, active, decoder, replaySigner,
	)
	if err != nil {
		t.Fatal(err)
	}
	if replayRoot.State != PublicationComplete {
		t.Fatalf("replay state = %q", replayRoot.State)
	}
	loaded, err := LoadReplay(context.Background(), store, replayRoot.IntendedRoot, active)
	if err != nil {
		t.Fatal(err)
	}
	if loaded.Manifest.Parent != parent ||
		len(loaded.Attestation.Statement.Parents) != 1 ||
		loaded.Attestation.Statement.Parents[0] != parent {
		t.Fatalf("replay did not preserve attested lineage: %+v", loaded)
	}

	revokedParent := testVerifier(t,
		testTrustKey{
			signer: captureSigner,
			roles:  []BundleKind{CaptureBundle},
			status: KeyRevoked,
		},
		testTrustKey{
			signer: replaySigner,
			roles:  []BundleKind{ReplayBundle},
			status: KeyActive,
		},
	)
	if _, err := LoadReplay(
		context.Background(), store, replayRoot.IntendedRoot, revokedParent,
	); !errors.Is(err, ErrRevokedAttestation) {
		t.Fatalf("revoked parent error = %v", err)
	}
}

func TestReplayRejectsSignedManifestParentMismatch(t *testing.T) {
	store := openTestStore(t)
	run := validRun(t)
	firstSigner := testSigner(t, "first capture signer")
	secondSigner := testSigner(t, "second capture signer")
	verifier := testVerifier(t,
		testTrustKey{
			signer: firstSigner,
			roles:  []BundleKind{CaptureBundle, ReplayBundle},
			status: KeyActive,
		},
		testTrustKey{
			signer: secondSigner,
			roles:  []BundleKind{CaptureBundle},
			status: KeyActive,
		},
	)
	firstParent := publishTestRun(t, store, run, firstSigner)
	secondParent := publishTestRun(t, store, run, secondSigner)
	replayRoot, err := replayCaptureWithDecoder(
		context.Background(),
		store,
		firstParent,
		verifier,
		fixtureDecoder{identity: decoderIdentityForRun(run)},
		firstSigner,
	)
	if err != nil {
		t.Fatal(err)
	}
	replay, err := LoadReplay(context.Background(), store, replayRoot.IntendedRoot, verifier)
	if err != nil {
		t.Fatal(err)
	}
	manifest := replay.Manifest
	manifest.Parent = secondParent
	mismatched := putTestAttestedSubject(
		t,
		store,
		firstSigner,
		ReplayBundle,
		replayMediaType,
		manifest,
		[]cas.ObjectRef{firstParent},
	)
	if _, err := LoadReplay(
		context.Background(), store, mismatched, verifier,
	); !errors.Is(err, ErrInvalidAttestation) {
		t.Fatalf("parent mismatch error = %v", err)
	}
}

func TestReplayLineageDepthIsBoundedBeforeGraphExpansion(t *testing.T) {
	store := openTestStore(t)
	run := validRun(t)
	signer := testSigner(t, "lineage depth signer")
	verifier := testVerifier(t, testTrustKey{
		signer: signer,
		roles:  []BundleKind{CaptureBundle, ReplayBundle},
		status: KeyActive,
	})
	parent := publishTestRun(t, store, run, signer)
	validReplay, err := replayCaptureWithDecoder(
		context.Background(),
		store,
		parent,
		verifier,
		fixtureDecoder{identity: decoderIdentityForRun(run)},
		signer,
	)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadReplay(context.Background(), store, validReplay.IntendedRoot, verifier)
	if err != nil {
		t.Fatal(err)
	}
	manifest := loaded.Manifest
	current := parent
	for range maxLineageDepth + 2 {
		manifest.Parent = current
		current = putTestAttestedSubject(
			t,
			store,
			signer,
			ReplayBundle,
			replayMediaType,
			manifest,
			[]cas.ObjectRef{current},
		)
	}
	_, err = VerifyEvidence(context.Background(), store, current, verifier)
	if !errors.Is(err, ErrInvalidAttestation) ||
		!strings.Contains(err.Error(), "maximum depth") {
		t.Fatalf("unbounded lineage error = %v", err)
	}
}

func TestTrustPolicyIsStrictBoundedAndRoleCanonical(t *testing.T) {
	signer := testSigner(t, "trust policy signer")
	publicKey := signer.AttestationPublicKey()
	valid := TrustPolicy{
		SchemaVersion: TrustPolicySchemaVersion,
		Project:       AttestationProject,
		Keys: []TrustedKeyPolicy{{
			KeyID:     attestationKeyID(publicKey),
			PublicKey: base64.RawURLEncoding.EncodeToString(publicKey),
			Roles:     []BundleKind{CaptureBundle, ReplayBundle},
			Status:    KeyActive,
		}},
	}
	raw, err := json.Marshal(valid)
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := DecodeTrustPolicy(raw)
	if err != nil {
		t.Fatal(err)
	}
	digest := sha256.Sum256(raw)
	if verifier.PolicySHA256() != hex.EncodeToString(digest[:]) {
		t.Fatal("verifier did not bind the exact trust policy bytes")
	}

	unknown := append([]byte(nil), raw[:len(raw)-1]...)
	unknown = append(unknown, []byte(`,"unknown":true}`)...)
	reversed := valid
	reversed.Keys = append([]TrustedKeyPolicy(nil), valid.Keys...)
	reversed.Keys[0].Roles = []BundleKind{ReplayBundle, CaptureBundle}
	reversedRaw, _ := json.Marshal(reversed)
	duplicate := valid
	duplicate.Keys = append(append([]TrustedKeyPolicy(nil), valid.Keys...), valid.Keys[0])
	duplicateRaw, _ := json.Marshal(duplicate)
	mismatch := valid
	mismatch.Keys = append([]TrustedKeyPolicy(nil), valid.Keys...)
	mismatch.Keys[0].KeyID = keyIDPrefix + strings.Repeat("0", 64)
	mismatchRaw, _ := json.Marshal(mismatch)

	for _, test := range []struct {
		name string
		raw  []byte
	}{
		{"noncanonical whitespace", append(append([]byte(nil), raw...), '\n')},
		{"unknown field", unknown},
		{"reversed roles", reversedRaw},
		{"duplicate key", duplicateRaw},
		{"mismatched key id", mismatchRaw},
		{"oversized", bytes.Repeat([]byte{'x'}, maxTrustPolicyBytes+1)},
	} {
		t.Run(test.name, func(t *testing.T) {
			if _, err := DecodeTrustPolicy(test.raw); err == nil {
				t.Fatal("invalid trust policy was accepted")
			}
		})
	}
}

func TestSigningSecretsAreNotSerializedOrLeakedBySignerErrors(t *testing.T) {
	seed := sha256.Sum256([]byte("never-publish-this-private-seed"))
	privateKey := ed25519.NewKeyFromSeed(seed[:])
	signer, err := NewEd25519Signer(privateKey)
	if err != nil {
		t.Fatal(err)
	}
	if raw, err := json.Marshal(signer); err == nil || raw != nil {
		t.Fatal("private signer was JSON serializable")
	}
	formatted := fmt.Sprintf("%#v", signer)
	if strings.Contains(formatted, hex.EncodeToString(seed[:])) {
		t.Fatal("private signer formatting exposed seed bytes")
	}

	leaking := errorSigner{
		publicKey: signer.AttestationPublicKey(),
		err:       errors.New("secret sentinel from signer"),
	}
	_, err = newAttestationEnvelope(
		context.Background(),
		leaking,
		CaptureBundle,
		cas.ObjectRef{
			Digest:    "sha256:" + strings.Repeat("3", 64),
			Size:      1,
			MediaType: captureMediaType,
		},
		[]cas.ObjectRef{},
	)
	if !errors.Is(err, ErrAttestationSigning) ||
		strings.Contains(err.Error(), "secret sentinel") {
		t.Fatalf("signer error leaked: %v", err)
	}

	storePath := t.TempDir()
	store, err := cas.Open(storePath, cas.Options{MaxObjectBytes: 4 << 20})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = store.Close() })
	_ = publishTestRun(t, store, validRun(t), signer)
	privateEncodings := [][]byte{
		seed[:],
		privateKey,
		[]byte(hex.EncodeToString(seed[:])),
		[]byte(base64.RawURLEncoding.EncodeToString(seed[:])),
	}
	err = filepath.Walk(storePath, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if !info.Mode().IsRegular() {
			return nil
		}
		raw, readErr := os.ReadFile(path)
		if readErr != nil {
			return readErr
		}
		for _, secret := range privateEncodings {
			if bytes.Contains(raw, secret) {
				return errors.New("private signing key appeared in CAS bytes")
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestPublicationRecoveryNeverTrustsRootBlobWithoutCompleteGraph(t *testing.T) {
	t.Run("old root with missing child stays incomplete", func(t *testing.T) {
		storePath := t.TempDir()
		store, err := cas.Open(storePath, cas.Options{MaxObjectBytes: 4 << 20})
		if err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := store.Close(); err != nil {
				t.Errorf("close CAS store: %v", err)
			}
		}()
		run := validRun(t)
		signer, verifier := testSignerAndVerifier(
			t, []BundleKind{CaptureBundle}, KeyActive,
		)
		root := publishTestRun(t, store, run, signer)
		capture, err := LoadCapture(context.Background(), store, root, verifier)
		if err != nil {
			t.Fatal(err)
		}
		missing := capture.Manifest.Baseline.Raw.Artifacts[0].Object
		if err := os.Remove(testCASObjectPath(storePath, missing)); err != nil {
			t.Fatal(err)
		}
		objects := publicationObjectSet{}
		objects.addCapture(capture)
		uncertain := root
		result, err := resolveAttestedCommit(
			store,
			root,
			verifier,
			objects.refs,
			cas.CommitResult{
				Root:            root,
				State:           cas.CommitIndeterminate,
				UncertainObject: &uncertain,
				UncertainStage:  "atomic_rename",
			},
			cas.ErrPublicationUnknown,
		)
		if err == nil || result.IntendedRoot != root ||
			result.State != PublicationVisible || result.GraphVerified ||
			!result.RecoveryRequired || result.UncertainObject == nil ||
			*result.UncertainObject != missing ||
			result.UncertainStage != "durability_recovery/verify_before_sync" {
			t.Fatalf("missing-child result = %+v, err=%v", result, err)
		}
		if err := result.Validate(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("late complete visibility is durably recovered", func(t *testing.T) {
		store := openTestStore(t)
		run := validRun(t)
		signer, verifier := testSignerAndVerifier(
			t, []BundleKind{CaptureBundle}, KeyActive,
		)
		root := publishTestRun(t, store, run, signer)
		capture, err := LoadCapture(context.Background(), store, root, verifier)
		if err != nil {
			t.Fatal(err)
		}
		objects := publicationObjectSet{}
		objects.addCapture(capture)
		uncertain := capture.Manifest.Baseline.Raw.Stdout
		result, err := resolveAttestedCommit(
			store,
			root,
			verifier,
			objects.refs,
			cas.CommitResult{
				Root:            root,
				State:           cas.CommitIndeterminate,
				UncertainObject: &uncertain,
				UncertainStage:  "atomic_rename",
			},
			cas.ErrPublicationUnknown,
		)
		if !errors.Is(err, cas.ErrPublicationUnknown) ||
			result.State != PublicationComplete || !result.Durable ||
			!result.GraphVerified || result.RecoveryRequired ||
			result.UncertainObject != nil {
			t.Fatalf("late-visible result = %+v, err=%v", result, err)
		}
		if err := result.Validate(); err != nil {
			t.Fatal(err)
		}
	})

	t.Run("cleanup warning may accompany complete evidence", func(t *testing.T) {
		store := openTestStore(t)
		run := validRun(t)
		signer, verifier := testSignerAndVerifier(
			t, []BundleKind{CaptureBundle}, KeyActive,
		)
		root := publishTestRun(t, store, run, signer)
		capture, err := LoadCapture(context.Background(), store, root, verifier)
		if err != nil {
			t.Fatal(err)
		}
		objects := publicationObjectSet{}
		objects.addCapture(capture)
		result, err := resolveAttestedCommit(
			store,
			root,
			verifier,
			objects.refs,
			cas.CommitResult{
				Root:           root,
				State:          cas.CommitDurable,
				Durable:        true,
				CleanupPending: true,
			},
			errors.Join(cas.ErrRootPublished, cas.ErrCleanupPending),
		)
		if result.State != PublicationComplete || !result.CleanupWarning ||
			!errors.Is(err, cas.ErrCleanupPending) {
			t.Fatalf("cleanup-warning result = %+v, err=%v", result, err)
		}
	})
}

func TestPublicationRequiresActiveSignerRoleBeforeStaging(t *testing.T) {
	store := openTestStore(t)
	run := validRun(t)
	signer := testSigner(t, "publication authorization signer")
	for _, test := range []struct {
		name   string
		roles  []BundleKind
		status KeyStatus
		want   error
	}{
		{"wrong role", []BundleKind{ReplayBundle}, KeyActive, ErrUntrustedAttestation},
		{"retired", []BundleKind{CaptureBundle}, KeyRetired, ErrRetiredAttestation},
		{"revoked", []BundleKind{CaptureBundle}, KeyRevoked, ErrRevokedAttestation},
	} {
		t.Run(test.name, func(t *testing.T) {
			verifier := testVerifier(t, testTrustKey{
				signer: signer,
				roles:  test.roles,
				status: test.status,
			})
			result, err := PublishRun(
				context.Background(), store, run, signer, verifier,
			)
			if !errors.Is(err, test.want) || result.IntendedRoot.Digest != "" ||
				result.State != PublicationRetryable {
				t.Fatalf("result=%+v err=%v, want %v", result, err, test.want)
			}
		})
	}
}

func testCASObjectPath(root string, ref cas.ObjectRef) string {
	hexDigest := strings.TrimPrefix(ref.Digest, "sha256:")
	return filepath.Join(root, "objects", "sha256", hexDigest[:2], hexDigest[2:])
}

type errorSigner struct {
	publicKey ed25519.PublicKey
	err       error
}

func (signer errorSigner) AttestationPublicKey() ed25519.PublicKey {
	return append(ed25519.PublicKey(nil), signer.publicKey...)
}

func (signer errorSigner) SignAttestation(context.Context, []byte) ([]byte, error) {
	return nil, signer.err
}

type testTrustKey struct {
	signer *Ed25519Signer
	roles  []BundleKind
	status KeyStatus
}

func testSigner(t *testing.T, label string) *Ed25519Signer {
	t.Helper()
	seed := sha256.Sum256([]byte(label))
	signer, err := NewEd25519Signer(ed25519.NewKeyFromSeed(seed[:]))
	if err != nil {
		t.Fatal(err)
	}
	return signer
}

func testVerifier(t *testing.T, keys ...testTrustKey) *Verifier {
	t.Helper()
	configured := make([]TrustedKeyPolicy, len(keys))
	for index, key := range keys {
		publicKey := key.signer.AttestationPublicKey()
		configured[index] = TrustedKeyPolicy{
			KeyID:     attestationKeyID(publicKey),
			PublicKey: base64.RawURLEncoding.EncodeToString(publicKey),
			Roles:     append([]BundleKind(nil), key.roles...),
			Status:    key.status,
		}
	}
	sort.Slice(configured, func(left, right int) bool {
		return configured[left].KeyID < configured[right].KeyID
	})
	raw, err := json.Marshal(TrustPolicy{
		SchemaVersion: TrustPolicySchemaVersion,
		Project:       AttestationProject,
		Keys:          configured,
	})
	if err != nil {
		t.Fatal(err)
	}
	verifier, err := DecodeTrustPolicy(raw)
	if err != nil {
		t.Fatal(err)
	}
	return verifier
}

func readTestEnvelope(
	t *testing.T,
	store *cas.Store,
	root cas.ObjectRef,
) AttestationEnvelope {
	t.Helper()
	raw, err := store.Read(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	var envelope AttestationEnvelope
	if err := decodeStrict(raw, &envelope); err != nil {
		t.Fatal(err)
	}
	return envelope
}

func putTestJSONRoot(
	t *testing.T,
	store *cas.Store,
	mediaType string,
	value any,
) cas.ObjectRef {
	t.Helper()
	transaction, err := store.Begin()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = transaction.Abort() })
	root, err := putJSON(context.Background(), transaction, mediaType, value)
	if err != nil {
		t.Fatal(err)
	}
	if err := transaction.Commit(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	return root
}

func putTestAttestedSubject(
	t *testing.T,
	store *cas.Store,
	signer AttestationSigner,
	kind BundleKind,
	mediaType string,
	value any,
	parents []cas.ObjectRef,
) cas.ObjectRef {
	t.Helper()
	transaction, err := store.Begin()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = transaction.Abort() })
	subject, err := putJSON(context.Background(), transaction, mediaType, value)
	if err != nil {
		t.Fatal(err)
	}
	root, err := putAttestation(
		context.Background(), transaction, signer, kind, subject, parents,
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := transaction.Commit(context.Background(), root); err != nil {
		t.Fatal(err)
	}
	return root
}
