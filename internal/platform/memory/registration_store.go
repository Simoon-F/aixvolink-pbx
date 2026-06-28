// Package memory provides bounded in-process adapters for development and tests.
package memory

import (
	"context"
	"sync"
	"time"

	"github.com/Simoon-F/aixvolink-pbx/internal/core/registration"
)

// RegistrationStore stores registration bindings in process memory.
type RegistrationStore struct {
	mu       sync.RWMutex
	bindings map[bindingKey]registration.Binding
}

type bindingKey struct {
	tenantID registration.TenantID
	aor      registration.AoR
	contact  string
}

// NewRegistrationStore constructs an empty store.
func NewRegistrationStore() *RegistrationStore {
	return &RegistrationStore{bindings: make(map[bindingKey]registration.Binding)}
}

// Upsert atomically enforces the binding limit and stores a binding.
func (s *RegistrationStore) Upsert(ctx context.Context, binding registration.Binding, maxBindings int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	key := bindingKey{tenantID: binding.TenantID, aor: binding.AoR, contact: binding.Contact}

	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.bindings[key]; !exists {
		activeCount := 0
		for storedKey, stored := range s.bindings {
			if storedKey.tenantID == binding.TenantID && storedKey.aor == binding.AoR && stored.ExpiresAt.After(binding.UpdatedAt) {
				activeCount++
			}
		}
		if activeCount >= maxBindings {
			return registration.ErrBindingLimit
		}
	}
	s.bindings[key] = binding
	return nil
}

// Delete removes one contact binding.
func (s *RegistrationStore) Delete(ctx context.Context, tenantID registration.TenantID, aor registration.AoR, contact string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	delete(s.bindings, bindingKey{tenantID: tenantID, aor: aor, contact: contact})
	s.mu.Unlock()
	return nil
}

// DeleteAll removes every binding for one AoR.
func (s *RegistrationStore) DeleteAll(ctx context.Context, tenantID registration.TenantID, aor registration.AoR) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	s.mu.Lock()
	for key := range s.bindings {
		if key.tenantID == tenantID && key.aor == aor {
			delete(s.bindings, key)
		}
	}
	s.mu.Unlock()
	return nil
}

// ListActive returns detached copies of active bindings.
func (s *RegistrationStore) ListActive(ctx context.Context, tenantID registration.TenantID, aor registration.AoR, now time.Time) ([]registration.Binding, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	s.mu.RLock()
	bindings := make([]registration.Binding, 0)
	for key, binding := range s.bindings {
		if key.tenantID == tenantID && key.aor == aor && binding.ExpiresAt.After(now) {
			bindings = append(bindings, binding)
		}
	}
	s.mu.RUnlock()
	return bindings, nil
}

// DeleteExpired removes all expired bindings.
func (s *RegistrationStore) DeleteExpired(ctx context.Context, now time.Time) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	var deleted int64
	s.mu.Lock()
	for key, binding := range s.bindings {
		if !binding.ExpiresAt.After(now) {
			delete(s.bindings, key)
			deleted++
		}
	}
	s.mu.Unlock()
	return deleted, nil
}
