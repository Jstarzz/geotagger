package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
)

var ErrUnauthorized = errors.New("unauthorized")

type Verifier struct {
	keys map[string][sha256.Size]byte
}

func NewVerifier(spec string) (*Verifier, error) {
	v := &Verifier{keys: make(map[string][sha256.Size]byte)}
	for _, entry := range strings.Split(spec, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		id, digest, ok := strings.Cut(entry, ":")
		if !ok || id == "" || digest == "" {
			return nil, fmt.Errorf("invalid API_KEYS entry %q", entry)
		}
		if strings.Contains(id, ".") {
			return nil, fmt.Errorf("key id %q may not contain '.'", id)
		}
		raw, err := hex.DecodeString(digest)
		if err != nil || len(raw) != sha256.Size {
			return nil, fmt.Errorf("invalid SHA-256 digest for key %q", id)
		}
		var expected [sha256.Size]byte
		copy(expected[:], raw)
		if _, exists := v.keys[id]; exists {
			return nil, fmt.Errorf("duplicate key id %q", id)
		}
		v.keys[id] = expected
	}
	if len(v.keys) == 0 {
		return nil, errors.New("no API keys configured")
	}
	return v, nil
}

func (v *Verifier) VerifyAuthorization(header string) (string, error) {
	const prefix = "Bearer "
	if !strings.HasPrefix(header, prefix) {
		return "", ErrUnauthorized
	}
	token := strings.TrimSpace(strings.TrimPrefix(header, prefix))
	id, secret, ok := strings.Cut(token, ".")
	if !ok || id == "" || secret == "" {
		return "", ErrUnauthorized
	}
	expected, ok := v.keys[id]
	if !ok {
		// Keep the unknown-key path doing comparable hashing work.
		dummy := sha256.Sum256([]byte(secret))
		_ = subtle.ConstantTimeCompare(dummy[:], dummy[:])
		return "", ErrUnauthorized
	}
	actual := sha256.Sum256([]byte(secret))
	if subtle.ConstantTimeCompare(expected[:], actual[:]) != 1 {
		return "", ErrUnauthorized
	}
	return id, nil
}
