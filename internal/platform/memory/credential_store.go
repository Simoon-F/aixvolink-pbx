package memory

import (
	"context"
	"errors"
	"sync"

	sipauth "github.com/Simoon-F/aixvolink-pbx/internal/sip/auth"
)

// ErrCredentialNotFound indicates an unknown realm and username pair.
var ErrCredentialNotFound = errors.New("credential not found")

// CredentialStore is a bounded static credential collection for development and tests.
type CredentialStore struct {
	mu          sync.RWMutex
	credentials map[credentialKey]sipauth.Credential
}

type credentialKey struct{ realm, username string }

// NewCredentialStore constructs a store from detached credential values.
func NewCredentialStore(credentials []sipauth.Credential) *CredentialStore {
	store := &CredentialStore{credentials: make(map[credentialKey]sipauth.Credential, len(credentials))}
	for _, credential := range credentials {
		store.credentials[credentialKey{realm: credential.Realm, username: credential.Username}] = credential
	}
	return store
}

// LookupCredential resolves one credential.
func (s *CredentialStore) LookupCredential(ctx context.Context, realm, username string) (sipauth.Credential, error) {
	if err := ctx.Err(); err != nil {
		return sipauth.Credential{}, err
	}
	s.mu.RLock()
	credential, exists := s.credentials[credentialKey{realm: realm, username: username}]
	s.mu.RUnlock()
	if !exists {
		return sipauth.Credential{}, ErrCredentialNotFound
	}
	return credential, nil
}
