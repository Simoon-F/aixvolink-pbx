// Package auth implements SIP Digest authentication policy using audited primitives.
package auth

import (
	"context"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/Simoon-F/aixvolink-pbx/internal/core/registration"
	"github.com/icholy/digest"
)

const (
	nonceRandomBytes = 16
	nonceMACBytes    = sha256.Size
	nonceTimeBytes   = 8
)

var (
	// ErrMissingCredentials indicates that Authorization was not provided.
	ErrMissingCredentials = errors.New("digest credentials missing")
	// ErrInvalidCredentials indicates that credentials did not authenticate.
	ErrInvalidCredentials = errors.New("digest credentials invalid")
	// ErrStaleNonce indicates that a well-formed nonce has expired.
	ErrStaleNonce = errors.New("digest nonce stale")
	// ErrReplay indicates a repeated or decreasing nonce count.
	ErrReplay = errors.New("digest nonce replay detected")
	// ErrReplayCapacity indicates that the bounded replay cache is full.
	ErrReplayCapacity = errors.New("digest replay cache capacity reached")
)

// Credential stores a precomputed MD5 HA1 instead of a plaintext password.
type Credential struct {
	TenantID    registration.TenantID
	Username    string
	Realm       string
	HA1         string
	MaxBindings int
}

// CredentialStore resolves credentials without exposing plaintext secrets.
type CredentialStore interface {
	LookupCredential(ctx context.Context, realm, username string) (Credential, error)
}

// Clock supplies deterministic nonce time.
type Clock interface {
	Now() time.Time
}

// SystemClock reads current UTC time.
type SystemClock struct{}

// Now returns current UTC time.
func (SystemClock) Now() time.Time { return time.Now().UTC() }

// Config defines bounded Digest authentication policy.
type Config struct {
	Realm            string
	NonceSecret      []byte
	NonceTTL         time.Duration
	MaxReplayEntries int
}

// Authenticator validates RFC 7616-style qop=auth credentials for SIP.
type Authenticator struct {
	cfg    Config
	store  CredentialStore
	clock  Clock
	mu     sync.Mutex
	replay map[replayKey]replayEntry
}

type replayKey struct {
	nonce    string
	username string
}

type replayEntry struct {
	maxNonceCount int
	expiresAt     time.Time
}

// New constructs an Authenticator without starting background work.
func New(cfg Config, store CredentialStore, clock Clock) (*Authenticator, error) {
	if cfg.Realm == "" {
		return nil, fmt.Errorf("digest realm is required")
	}
	if len(cfg.NonceSecret) < 32 {
		return nil, fmt.Errorf("nonce secret must contain at least 32 bytes")
	}
	if cfg.NonceTTL <= 0 {
		return nil, fmt.Errorf("nonce TTL must be positive")
	}
	if cfg.MaxReplayEntries <= 0 {
		return nil, fmt.Errorf("max replay entries must be positive")
	}
	if store == nil {
		return nil, fmt.Errorf("credential store is required")
	}
	if clock == nil {
		return nil, fmt.Errorf("clock is required")
	}
	return &Authenticator{
		cfg: cfg, store: store, clock: clock,
		replay: make(map[replayKey]replayEntry, cfg.MaxReplayEntries),
	}, nil
}

// Challenge returns a fresh WWW-Authenticate value.
func (a *Authenticator) Challenge(stale bool) (string, error) {
	nonce, err := a.issueNonce()
	if err != nil {
		return "", err
	}
	return (&digest.Challenge{
		Realm: a.cfg.Realm, Nonce: nonce, Algorithm: "MD5", QOP: []string{"auth"}, Stale: stale,
	}).String(), nil
}

// Verify authenticates one SIP request and records its nonce count.
func (a *Authenticator) Verify(ctx context.Context, method, requestURI, authorization string) (Credential, error) {
	if authorization == "" {
		return Credential{}, ErrMissingCredentials
	}
	credentials, err := digest.ParseCredentials(authorization)
	if err != nil {
		return Credential{}, errors.Join(ErrInvalidCredentials, err)
	}
	if credentials.Realm != a.cfg.Realm || credentials.URI != requestURI || !strings.EqualFold(credentials.Algorithm, "MD5") || credentials.QOP != "auth" || credentials.Cnonce == "" || credentials.Nc <= 0 {
		return Credential{}, ErrInvalidCredentials
	}
	expiresAt, err := a.validateNonce(credentials.Nonce)
	if err != nil {
		return Credential{}, err
	}
	credential, err := a.store.LookupCredential(ctx, credentials.Realm, credentials.Username)
	if err != nil {
		return Credential{}, errors.Join(ErrInvalidCredentials, err)
	}
	if credential.Realm != credentials.Realm || credential.Username != credentials.Username || len(credential.HA1) != md5.Size*2 {
		return Credential{}, ErrInvalidCredentials
	}
	expected, err := digest.Digest(&digest.Challenge{
		Realm: a.cfg.Realm, Nonce: credentials.Nonce, Algorithm: "MD5", QOP: []string{"auth"},
	}, digest.Options{
		Method: method, URI: requestURI, Username: credentials.Username, A1: credential.HA1,
		Count: credentials.Nc, Cnonce: credentials.Cnonce,
	})
	if err != nil {
		return Credential{}, errors.Join(ErrInvalidCredentials, err)
	}
	if subtle.ConstantTimeCompare([]byte(strings.ToLower(credentials.Response)), []byte(strings.ToLower(expected.Response))) != 1 {
		return Credential{}, ErrInvalidCredentials
	}
	if err := a.recordNonceCount(credentials.Nonce, credentials.Username, credentials.Nc, expiresAt); err != nil {
		return Credential{}, err
	}
	return credential, nil
}

func (a *Authenticator) issueNonce() (string, error) {
	payload := make([]byte, nonceTimeBytes+nonceRandomBytes)
	binary.BigEndian.PutUint64(payload[:nonceTimeBytes], uint64(a.clock.Now().UTC().Unix()))
	if _, err := rand.Read(payload[nonceTimeBytes:]); err != nil {
		return "", fmt.Errorf("generate nonce randomness: %w", err)
	}
	mac := hmac.New(sha256.New, a.cfg.NonceSecret)
	_, _ = mac.Write(payload)
	payload = append(payload, mac.Sum(nil)...)
	return base64.RawURLEncoding.EncodeToString(payload), nil
}

func (a *Authenticator) validateNonce(encoded string) (time.Time, error) {
	payload, err := base64.RawURLEncoding.DecodeString(encoded)
	if err != nil || len(payload) != nonceTimeBytes+nonceRandomBytes+nonceMACBytes {
		return time.Time{}, ErrInvalidCredentials
	}
	message := payload[:nonceTimeBytes+nonceRandomBytes]
	providedMAC := payload[nonceTimeBytes+nonceRandomBytes:]
	mac := hmac.New(sha256.New, a.cfg.NonceSecret)
	_, _ = mac.Write(message)
	if !hmac.Equal(providedMAC, mac.Sum(nil)) {
		return time.Time{}, ErrInvalidCredentials
	}
	issuedAt := time.Unix(int64(binary.BigEndian.Uint64(message[:nonceTimeBytes])), 0).UTC()
	now := a.clock.Now().UTC()
	if issuedAt.After(now.Add(time.Minute)) {
		return time.Time{}, ErrInvalidCredentials
	}
	expiresAt := issuedAt.Add(a.cfg.NonceTTL)
	if !expiresAt.After(now) {
		return expiresAt, ErrStaleNonce
	}
	return expiresAt, nil
}

func (a *Authenticator) recordNonceCount(nonce, username string, nonceCount int, expiresAt time.Time) error {
	now := a.clock.Now().UTC()
	key := replayKey{nonce: nonce, username: username}
	a.mu.Lock()
	defer a.mu.Unlock()
	if entry, exists := a.replay[key]; exists {
		if nonceCount <= entry.maxNonceCount {
			return ErrReplay
		}
		a.replay[key] = replayEntry{maxNonceCount: nonceCount, expiresAt: expiresAt}
		return nil
	}
	if len(a.replay) >= a.cfg.MaxReplayEntries {
		for replayKey, entry := range a.replay {
			if !entry.expiresAt.After(now) {
				delete(a.replay, replayKey)
			}
		}
	}
	if len(a.replay) >= a.cfg.MaxReplayEntries {
		return ErrReplayCapacity
	}
	a.replay[key] = replayEntry{maxNonceCount: nonceCount, expiresAt: expiresAt}
	return nil
}

// ComputeHA1 computes the RFC 2617 MD5 HA1 value for credential provisioning.
func ComputeHA1(username, realm, password string) string {
	sum := md5.Sum([]byte(username + ":" + realm + ":" + password))
	return hex.EncodeToString(sum[:])
}
