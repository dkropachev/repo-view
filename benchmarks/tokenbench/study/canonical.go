package study

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"unicode/utf8"
)

func decodeCanonical(raw []byte, destination any) error {
	if !utf8.Valid(raw) {
		return errors.New("JSON is not valid UTF-8")
	}
	if err := rejectDuplicateKeys(raw); err != nil {
		return err
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(destination); err != nil {
		return err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return errors.New("multiple JSON values")
		}
		return err
	}
	canonical, err := json.Marshal(destination)
	if err != nil {
		return fmt.Errorf("encode canonical JSON: %w", err)
	}
	if !bytes.Equal(raw, canonical) {
		return errors.New("JSON is not the canonical required-field encoding")
	}
	return nil
}

func rejectDuplicateKeys(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	var walk func() error
	walk = func() error {
		token, err := decoder.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := make(map[string]struct{})
			for decoder.More() {
				keyToken, err := decoder.Token()
				if err != nil {
					return err
				}
				key, ok := keyToken.(string)
				if !ok {
					return errors.New("JSON object key is not a string")
				}
				if _, exists := seen[key]; exists {
					return fmt.Errorf("duplicate JSON object key %q", key)
				}
				seen[key] = struct{}{}
				if err := walk(); err != nil {
					return err
				}
			}
			_, err := decoder.Token()
			return err
		case '[':
			for decoder.More() {
				if err := walk(); err != nil {
					return err
				}
			}
			_, err := decoder.Token()
			return err
		default:
			return fmt.Errorf("unexpected JSON delimiter %q", delimiter)
		}
	}
	if err := walk(); err != nil {
		return err
	}
	if token, err := decoder.Token(); !errors.Is(err, io.EOF) {
		if err == nil {
			return fmt.Errorf("unexpected trailing JSON token %v", token)
		}
		return err
	}
	return nil
}

func canonicalJSON(value any) ([]byte, error) {
	return json.Marshal(value)
}

func canonicalDigest(value any) (string, error) {
	raw, err := canonicalJSON(value)
	if err != nil {
		return "", err
	}
	digest := sha256.Sum256(raw)
	return hex.EncodeToString(digest[:]), nil
}

// canonicalPrivateSeal binds the complete exported canonical representation of
// an in-memory verified value. The returned seal is intentionally private to
// the value's Go type: persisted evidence must be reconstructed and verified,
// not trusted merely because it contains seal-shaped bytes.
func canonicalPrivateSeal(domain string, value any) ([sha256.Size]byte, error) {
	raw, err := canonicalJSON(value)
	if err != nil {
		return [sha256.Size]byte{}, err
	}
	hasher := sha256.New()
	hasher.Write([]byte("scopesifter/tokenbench/private-canonical-seal/v2\x00"))
	writeCommitmentField(hasher, []byte(domain))
	writeCommitmentField(hasher, raw)
	var seal [sha256.Size]byte
	copy(seal[:], hasher.Sum(nil))
	return seal, nil
}

func validSHA256(value string) bool {
	if len(value) != sha256.Size*2 {
		return false
	}
	decoded, err := hex.DecodeString(value)
	return err == nil && len(decoded) == sha256.Size &&
		value == hex.EncodeToString(decoded)
}
