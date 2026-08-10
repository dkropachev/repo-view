package evidence

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
)

const (
	TrustPolicySchemaVersion = "tokenbench.trust-policy/v1"
	maxTrustPolicyBytes      = 64 << 10
	maxTrustedKeys           = 64
)

// KeyStatus controls current verification. Because v1 has no trusted
// timestamp, both retired and revoked keys are rejected by a current policy.
type KeyStatus string

const (
	KeyActive  KeyStatus = "active"
	KeyRetired KeyStatus = "retired"
	KeyRevoked KeyStatus = "revoked"
)

// TrustPolicy is an out-of-band trust anchor. Evidence never embeds it and a
// verifier must authenticate its exact bytes independently of the bundle.
type TrustPolicy struct {
	SchemaVersion string             `json:"schema_version"`
	Project       string             `json:"project"`
	Keys          []TrustedKeyPolicy `json:"keys"`
}

// TrustedKeyPolicy contains one raw Ed25519 public key in canonical unpadded
// base64url and the bundle roles it may attest.
type TrustedKeyPolicy struct {
	KeyID     string       `json:"key_id"`
	PublicKey string       `json:"public_key"`
	Roles     []BundleKind `json:"roles"`
	Status    KeyStatus    `json:"status"`
}

type trustedKey struct {
	publicKey ed25519.PublicKey
	roles     map[BundleKind]struct{}
	status    KeyStatus
}

// Verifier is an immutable snapshot of one strictly decoded trust policy.
type Verifier struct {
	keys         map[string]trustedKey
	policySHA256 string
}

// DecodeTrustPolicy strictly decodes canonical, bounded JSON and snapshots all
// public keys and role decisions. It performs no network or ambient lookup.
func DecodeTrustPolicy(raw []byte) (*Verifier, error) {
	if len(raw) == 0 || len(raw) > maxTrustPolicyBytes {
		return nil, errors.New("trust policy size is invalid")
	}
	var policy TrustPolicy
	if err := decodeStrict(raw, &policy); err != nil {
		return nil, fmt.Errorf("decode trust policy: %w", err)
	}
	if policy.SchemaVersion != TrustPolicySchemaVersion ||
		policy.Project != AttestationProject {
		return nil, errors.New("trust policy context is invalid")
	}
	if policy.Keys == nil || len(policy.Keys) == 0 || len(policy.Keys) > maxTrustedKeys {
		return nil, errors.New("trust policy key count is invalid")
	}
	keys := make(map[string]trustedKey, len(policy.Keys))
	previousKeyID := ""
	for _, configured := range policy.Keys {
		if !validAttestationKeyID(configured.KeyID) ||
			(previousKeyID != "" && configured.KeyID <= previousKeyID) {
			return nil, errors.New("trust policy key ids must be unique and sorted")
		}
		previousKeyID = configured.KeyID
		publicKey, err := base64.RawURLEncoding.DecodeString(configured.PublicKey)
		if err != nil || len(publicKey) != ed25519.PublicKeySize ||
			base64.RawURLEncoding.EncodeToString(publicKey) != configured.PublicKey {
			return nil, errors.New("trust policy public key encoding is invalid")
		}
		if attestationKeyID(publicKey) != configured.KeyID {
			return nil, errors.New("trust policy key id does not match its public key")
		}
		if configured.Status != KeyActive && configured.Status != KeyRetired &&
			configured.Status != KeyRevoked {
			return nil, errors.New("trust policy key status is invalid")
		}
		if configured.Roles == nil || len(configured.Roles) == 0 || len(configured.Roles) > 2 {
			return nil, errors.New("trust policy key roles are invalid")
		}
		roles := make(map[BundleKind]struct{}, len(configured.Roles))
		previousRole := BundleKind("")
		for _, role := range configured.Roles {
			if role != CaptureBundle && role != ReplayBundle {
				return nil, errors.New("trust policy key role is invalid")
			}
			if previousRole != "" && role <= previousRole {
				return nil, errors.New("trust policy key roles must be unique and sorted")
			}
			previousRole = role
			roles[role] = struct{}{}
		}
		keys[configured.KeyID] = trustedKey{
			publicKey: append(ed25519.PublicKey(nil), publicKey...),
			roles:     roles,
			status:    configured.Status,
		}
	}
	digest := sha256.Sum256(raw)
	return &Verifier{
		keys:         keys,
		policySHA256: hex.EncodeToString(digest[:]),
	}, nil
}

// PolicySHA256 identifies the exact out-of-band policy bytes used.
func (verifier *Verifier) PolicySHA256() string {
	if verifier == nil {
		return ""
	}
	return verifier.policySHA256
}

func (verifier *Verifier) verify(
	kind BundleKind,
	keyID string,
	message, signature []byte,
) error {
	if verifier == nil || verifier.keys == nil {
		return errors.New("attestation verifier is required")
	}
	key, err := verifier.authorize(kind, keyID)
	if err != nil {
		return err
	}
	if !ed25519.Verify(key.publicKey, message, signature) {
		return fmt.Errorf("%w: signature verification failed", ErrInvalidAttestation)
	}
	return nil
}

func (verifier *Verifier) authorize(kind BundleKind, keyID string) (trustedKey, error) {
	if verifier == nil || verifier.keys == nil {
		return trustedKey{}, errors.New("attestation verifier is required")
	}
	key, exists := verifier.keys[keyID]
	if !exists {
		return trustedKey{}, ErrUntrustedAttestation
	}
	switch key.status {
	case KeyActive:
		// Continue below.
	case KeyRetired:
		return trustedKey{}, ErrRetiredAttestation
	case KeyRevoked:
		return trustedKey{}, ErrRevokedAttestation
	default:
		return trustedKey{}, ErrUntrustedAttestation
	}
	if _, allowed := key.roles[kind]; !allowed {
		return trustedKey{}, ErrUntrustedAttestation
	}
	return key, nil
}
