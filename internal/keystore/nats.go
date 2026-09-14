package keystore

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/Jstarzz/geotagger/internal/auth"
	"github.com/nats-io/nats.go"
)

const (
	DefaultBucket = "GEOTAGGER_API_KEYS"
	maxBucketBytes int64 = 64 << 20
)

type Record struct {
	ID               string     `json:"id"`
	SecretSHA256     string     `json:"secret_sha256"`
	DisplayName      string     `json:"display_name,omitempty"`
	Owner            string     `json:"owner,omitempty"`
	Environment      string     `json:"environment,omitempty"`
	CreatedAt        time.Time  `json:"created_at"`
	RotatedAt        *time.Time `json:"rotated_at,omitempty"`
	ExpiresAt        *time.Time `json:"expires_at,omitempty"`
	RevokedAt        *time.Time `json:"revoked_at,omitempty"`
	RevocationReason string     `json:"revocation_reason,omitempty"`
	Revision         uint64     `json:"-"`
}

type CreateInput struct {
	ID          string
	DisplayName string
	Owner       string
	Environment string
	ExpiresAt   *time.Time
}

type Store struct {
	nc *nats.Conn
	kv nats.KeyValue
}

func Open(url, bucket string) (*Store, error) {
	if bucket == "" {
		bucket = DefaultBucket
	}
	nc, err := nats.Connect(url,
		nats.Name("geotagger-key-store"),
		nats.Timeout(2*time.Second),
		nats.ReconnectWait(500*time.Millisecond),
		nats.MaxReconnects(-1),
	)
	if err != nil {
		return nil, fmt.Errorf("connect nats for key store: %w", err)
	}
	js, err := nc.JetStream()
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("key store jetstream: %w", err)
	}
	kv, err := js.KeyValue(bucket)
	if errors.Is(err, nats.ErrBucketNotFound) {
		kv, err = js.CreateKeyValue(&nats.KeyValueConfig{
			Bucket:      bucket,
			Description: "GeoTagger managed machine API credentials",
			History:     5,
			MaxBytes:    maxBucketBytes,
			Storage:     nats.FileStorage,
			Replicas:    1,
		})
	}
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("open key store bucket %q: %w", bucket, err)
	}
	status, err := kv.Status()
	if err != nil {
		nc.Close()
		return nil, fmt.Errorf("key store status: %w", err)
	}
	if status.Config().Storage != nats.FileStorage {
		nc.Close()
		return nil, fmt.Errorf("key store bucket %q is not file-backed", bucket)
	}
	return &Store{nc: nc, kv: kv}, nil
}

func (s *Store) Healthy() bool {
	return s != nil && s.nc != nil && s.nc.IsConnected()
}

func (s *Store) Close() error {
	if s == nil || s.nc == nil {
		return nil
	}
	if err := s.nc.Drain(); err != nil {
		s.nc.Close()
		return err
	}
	return nil
}

func (s *Store) List() ([]Record, error) {
	keys, err := s.kv.Keys()
	if errors.Is(err, nats.ErrNoKeysFound) {
		return []Record{}, nil
	}
	if err != nil {
		return nil, fmt.Errorf("list managed keys: %w", err)
	}
	records := make([]Record, 0, len(keys))
	for _, key := range keys {
		entry, err := s.kv.Get(key)
		if errors.Is(err, nats.ErrKeyNotFound) || errors.Is(err, nats.ErrKeyDeleted) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("get managed key %q: %w", key, err)
		}
		record, err := decodeEntry(entry)
		if err != nil {
			return nil, err
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, j int) bool { return records[i].ID < records[j].ID })
	return records, nil
}

func (s *Store) Create(input CreateInput) (Record, string, error) {
	if err := ValidateID(input.ID); err != nil {
		return Record{}, "", err
	}
	now := time.Now().UTC()
	secret, digest, err := generateSecret()
	if err != nil {
		return Record{}, "", err
	}
	record := Record{
		ID:           input.ID,
		SecretSHA256: digest,
		DisplayName:  strings.TrimSpace(input.DisplayName),
		Owner:        strings.TrimSpace(input.Owner),
		Environment:  strings.TrimSpace(input.Environment),
		CreatedAt:    now,
		ExpiresAt:    normalizeTime(input.ExpiresAt),
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return Record{}, "", err
	}
	revision, err := s.kv.Create(record.ID, payload)
	if err != nil {
		if errors.Is(err, nats.ErrKeyExists) || errors.Is(err, nats.ErrKeyRevisionMismatch) {
			return Record{}, "", fmt.Errorf("managed key %q already exists", record.ID)
		}
		return Record{}, "", fmt.Errorf("create managed key: %w", err)
	}
	record.Revision = revision
	return record, record.ID + "." + secret, nil
}

func (s *Store) Rotate(id string) (Record, string, error) {
	entry, err := s.kv.Get(id)
	if err != nil {
		return Record{}, "", mapGetError(id, err)
	}
	record, err := decodeEntry(entry)
	if err != nil {
		return Record{}, "", err
	}
	if record.RevokedAt != nil {
		return Record{}, "", fmt.Errorf("managed key %q is revoked; create a replacement key id instead", id)
	}
	secret, digest, err := generateSecret()
	if err != nil {
		return Record{}, "", err
	}
	now := time.Now().UTC()
	record.SecretSHA256 = digest
	record.RotatedAt = &now
	payload, err := json.Marshal(record)
	if err != nil {
		return Record{}, "", err
	}
	revision, err := s.kv.Update(id, payload, entry.Revision())
	if err != nil {
		return Record{}, "", fmt.Errorf("rotate managed key %q: %w", id, err)
	}
	record.Revision = revision
	return record, id + "." + secret, nil
}

func (s *Store) Revoke(id, reason string) (Record, error) {
	entry, err := s.kv.Get(id)
	if err != nil {
		return Record{}, mapGetError(id, err)
	}
	record, err := decodeEntry(entry)
	if err != nil {
		return Record{}, err
	}
	if record.RevokedAt != nil {
		return record, nil
	}
	now := time.Now().UTC()
	record.RevokedAt = &now
	record.RevocationReason = strings.TrimSpace(reason)
	payload, err := json.Marshal(record)
	if err != nil {
		return Record{}, err
	}
	revision, err := s.kv.Update(id, payload, entry.Revision())
	if err != nil {
		return Record{}, fmt.Errorf("revoke managed key %q: %w", id, err)
	}
	record.Revision = revision
	return record, nil
}

// StartVerifierSync loads the current KV snapshot and then keeps the in-memory
// verifier synchronized from JetStream watch events. The returned channel emits
// exactly one startup result after the initial snapshot has been applied.
func (s *Store) StartVerifierSync(ctx context.Context, verifier *auth.Verifier, onError func(error)) (<-chan error, error) {
	watcher, err := s.kv.WatchAll(nats.Context(ctx))
	if err != nil {
		return nil, fmt.Errorf("watch managed keys: %w", err)
	}
	ready := make(chan error, 1)
	go func() {
		defer watcher.Stop() //nolint:errcheck
		defer close(ready)
		state := make(map[string]Record)
		initialized := false
		readySent := false
		apply := func() error {
			keys := make([]auth.ManagedKey, 0, len(state))
			for _, record := range state {
				keys = append(keys, auth.ManagedKey{
					ID: record.ID, DigestHex: record.SecretSHA256,
					ExpiresAt: record.ExpiresAt, RevokedAt: record.RevokedAt,
				})
			}
			return verifier.ReplaceManaged(keys)
		}
		report := func(err error) {
			if onError != nil {
				onError(err)
			}
		}
		for {
			select {
			case <-ctx.Done():
				if !readySent {
					ready <- ctx.Err()
				}
				return
			case err, ok := <-watcher.Error():
				if ok && err != nil {
					if !readySent {
						ready <- err
						readySent = true
						return
					}
					report(fmt.Errorf("managed key watch: %w", err))
				}
			case entry, ok := <-watcher.Updates():
				if !ok {
					if !readySent {
						ready <- errors.New("managed key watch closed before initial snapshot")
					}
					return
				}
				if entry == nil {
					if !initialized {
						err := apply()
						ready <- err
						readySent = true
						if err != nil {
							return
						}
						initialized = true
					}
					continue
				}
				if entry.Operation() == nats.KeyValueDelete || entry.Operation() == nats.KeyValuePurge {
					delete(state, entry.Key())
				} else {
					record, err := decodeEntry(entry)
					if err != nil {
						if !readySent {
							ready <- err
							readySent = true
							return
						}
						report(err)
						continue
					}
					state[record.ID] = record
				}
				if initialized {
					if err := apply(); err != nil {
						report(err)
					}
				}
			}
		}
	}()
	return ready, nil
}

func ValidateID(id string) error {
	if len(id) < 1 || len(id) > 64 {
		return errors.New("key id must be 1-64 characters")
	}
	for _, r := range id {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '-' || r == '_' {
			continue
		}
		return errors.New("key id may contain only letters, numbers, '-' and '_'")
	}
	return nil
}

func decodeEntry(entry nats.KeyValueEntry) (Record, error) {
	var record Record
	dec := json.NewDecoder(strings.NewReader(string(entry.Value())))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&record); err != nil {
		return Record{}, fmt.Errorf("decode managed key %q: %w", entry.Key(), err)
	}
	if record.ID != entry.Key() {
		return Record{}, fmt.Errorf("managed key record id %q does not match KV key %q", record.ID, entry.Key())
	}
	if err := ValidateID(record.ID); err != nil {
		return Record{}, fmt.Errorf("managed key %q: %w", record.ID, err)
	}
	if _, err := hex.DecodeString(record.SecretSHA256); err != nil || len(record.SecretSHA256) != sha256.Size*2 {
		return Record{}, fmt.Errorf("managed key %q has invalid secret digest", record.ID)
	}
	record.CreatedAt = record.CreatedAt.UTC()
	record.RotatedAt = normalizeTime(record.RotatedAt)
	record.ExpiresAt = normalizeTime(record.ExpiresAt)
	record.RevokedAt = normalizeTime(record.RevokedAt)
	record.Revision = entry.Revision()
	return record, nil
}

func generateSecret() (secret, digest string, err error) {
	buf := make([]byte, 32)
	if _, err = rand.Read(buf); err != nil {
		return "", "", fmt.Errorf("generate API secret: %w", err)
	}
	secret = base64.RawURLEncoding.EncodeToString(buf)
	sum := sha256.Sum256([]byte(secret))
	digest = hex.EncodeToString(sum[:])
	return secret, digest, nil
}

func normalizeTime(in *time.Time) *time.Time {
	if in == nil {
		return nil
	}
	v := in.UTC()
	return &v
}

func mapGetError(id string, err error) error {
	if errors.Is(err, nats.ErrKeyNotFound) || errors.Is(err, nats.ErrKeyDeleted) {
		return fmt.Errorf("managed key %q not found", id)
	}
	return fmt.Errorf("get managed key %q: %w", id, err)
}
