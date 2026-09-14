package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"
)

var ErrUnauthorized = errors.New("unauthorized")

type ManagedKey struct {
	ID        string
	DigestHex string
	ExpiresAt *time.Time
	RevokedAt *time.Time
}

type managedCredential struct {
	digest    [sha256.Size]byte
	expiresAt *time.Time
	revokedAt *time.Time
}

type ManagedCounts struct {
	Total   int `json:"total"`
	Active  int `json:"active"`
	Revoked int `json:"revoked"`
	Expired int `json:"expired"`
}

type Verifier struct {
	mu      sync.RWMutex
	static  map[string][sha256.Size]byte
	managed map[string]managedCredential
}

func NewVerifier(spec string) (*Verifier, error) {
	v := &Verifier{
		static:  make(map[string][sha256.Size]byte),
		managed: make(map[string]managedCredential),
	}
	for _, entry := range strings.Split(spec, ",") {
		entry = strings.TrimSpace(entry)
		if entry == "" {
			continue
		}
		id, digest, ok := strings.Cut(entry, ":")
		if !ok || id == "" || digest == "" {
			return nil, fmt.Errorf("invalid API_KEYS entry %q", entry)
		}
		if err := validateID(id); err != nil {
			return nil, err
		}
		expected, err := decodeDigest(id, digest)
		if err != nil {
			return nil, err
		}
		if _, exists := v.static[id]; exists {
			return nil, fmt.Errorf("duplicate key id %q", id)
		}
		v.static[id] = expected
	}
	if len(v.static) == 0 {
		return nil, errors.New("no API keys configured")
	}
	return v, nil
}

func (v *Verifier) ReplaceManaged(keys []ManagedKey) error {
	next := make(map[string]managedCredential, len(keys))
	for _, key := range keys {
		if err := validateID(key.ID); err != nil {
			return err
		}
		if _, exists := v.static[key.ID]; exists {
			return fmt.Errorf("managed key id %q collides with static API_KEYS entry", key.ID)
		}
		if _, exists := next[key.ID]; exists {
			return fmt.Errorf("duplicate managed key id %q", key.ID)
		}
		digest, err := decodeDigest(key.ID, key.DigestHex)
		if err != nil {
			return err
		}
		next[key.ID] = managedCredential{digest: digest, expiresAt: cloneTime(key.ExpiresAt), revokedAt: cloneTime(key.RevokedAt)}
	}
	v.mu.Lock()
	v.managed = next
	v.mu.Unlock()
	return nil
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

	v.mu.RLock()
	staticDigest, isStatic := v.static[id]
	managed, isManaged := v.managed[id]
	v.mu.RUnlock()

	if !isStatic && !isManaged {
		// Keep the unknown-key path doing comparable hashing work.
		dummy := sha256.Sum256([]byte(secret))
		_ = subtle.ConstantTimeCompare(dummy[:], dummy[:])
		return "", ErrUnauthorized
	}

	expected := staticDigest
	if isManaged {
		now := time.Now().UTC()
		if managed.revokedAt != nil || (managed.expiresAt != nil && !now.Before(*managed.expiresAt)) {
			dummy := sha256.Sum256([]byte(secret))
			_ = subtle.ConstantTimeCompare(dummy[:], managed.digest[:])
			return "", ErrUnauthorized
		}
		expected = managed.digest
	}

	actual := sha256.Sum256([]byte(secret))
	if subtle.ConstantTimeCompare(expected[:], actual[:]) != 1 {
		return "", ErrUnauthorized
	}
	return id, nil
}

func (v *Verifier) ManagedCounts(now time.Time) ManagedCounts {
	now = now.UTC()
	v.mu.RLock()
	defer v.mu.RUnlock()
	counts := ManagedCounts{Total: len(v.managed)}
	for _, key := range v.managed {
		switch {
		case key.revokedAt != nil:
			counts.Revoked++
		case key.expiresAt != nil && !now.Before(*key.expiresAt):
			counts.Expired++
		default:
			counts.Active++
		}
	}
	return counts
}

func validateID(id string) error {
	if id == "" {
		return errors.New("key id is required")
	}
	if strings.ContainsAny(id, ".:") {
		return fmt.Errorf("key id %q may not contain '.' or ':'", id)
	}
	return nil
}

func decodeDigest(id, digest string) ([sha256.Size]byte, error) {
	var expected [sha256.Size]byte
	raw, err := hex.DecodeString(digest)
	if err != nil || len(raw) != sha256.Size {
		return expected, fmt.Errorf("invalid SHA-256 digest for key %q", id)
	}
	copy(expected[:], raw)
	return expected, nil
}

func cloneTime(in *time.Time) *time.Time {
	if in == nil {
		return nil
	}
	v := in.UTC()
	return &v
}
