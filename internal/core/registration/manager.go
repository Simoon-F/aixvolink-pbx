package registration

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"slices"
	"time"

	"github.com/google/uuid"
)

// Store persists registration bindings atomically.
type Store interface {
	Upsert(ctx context.Context, binding Binding, maxBindings int) error
	Delete(ctx context.Context, tenantID TenantID, aor AoR, contact string) error
	DeleteAll(ctx context.Context, tenantID TenantID, aor AoR) error
	ListActive(ctx context.Context, tenantID TenantID, aor AoR, now time.Time) ([]Binding, error)
	DeleteExpired(ctx context.Context, now time.Time) (int64, error)
}

// Clock supplies deterministic lifecycle time.
type Clock interface {
	Now() time.Time
}

// SystemClock reads the system clock.
type SystemClock struct{}

// Now returns the current UTC time.
func (SystemClock) Now() time.Time { return time.Now().UTC() }

// Request describes one authenticated registration update.
type Request struct {
	TenantID    TenantID
	AoR         AoR
	Contact     string
	RouteTarget string
	Transport   Transport
	Received    netip.AddrPort
	UserAgent   string
	Q           float32
	Expires     time.Duration
	MaxBindings int
}

// Manager applies registration lifecycle policy to a Store.
type Manager struct {
	store Store
	clock Clock
}

// NewManager constructs a registration manager.
func NewManager(store Store, clock Clock) (*Manager, error) {
	if store == nil {
		return nil, fmt.Errorf("registration store is required")
	}
	if clock == nil {
		return nil, fmt.Errorf("clock is required")
	}
	return &Manager{store: store, clock: clock}, nil
}

// Apply creates, refreshes, or removes one binding.
func (m *Manager) Apply(ctx context.Context, request Request) (Binding, error) {
	if request.Expires < 0 {
		return Binding{}, fmt.Errorf("expiration must not be negative")
	}
	if request.Expires == 0 {
		if request.Contact == "*" {
			return Binding{}, m.store.DeleteAll(ctx, request.TenantID, request.AoR)
		}
		return Binding{}, m.store.Delete(ctx, request.TenantID, request.AoR, request.Contact)
	}
	if request.MaxBindings <= 0 {
		return Binding{}, fmt.Errorf("max bindings must be positive")
	}

	now := m.clock.Now().UTC()
	if !request.Received.IsValid() {
		return Binding{}, fmt.Errorf("received address is required")
	}
	id, err := uuid.NewV7()
	if err != nil {
		return Binding{}, fmt.Errorf("generate registration ID: %w", err)
	}
	binding := Binding{
		ID:          ID(id.String()),
		TenantID:    request.TenantID,
		AoR:         request.AoR,
		Contact:     request.Contact,
		RouteTarget: request.RouteTarget,
		Transport:   request.Transport,
		Received:    request.Received,
		UserAgent:   request.UserAgent,
		Q:           request.Q,
		ExpiresAt:   now.Add(request.Expires),
		UpdatedAt:   now,
	}
	if err := binding.Validate(now); err != nil {
		return Binding{}, fmt.Errorf("validate registration: %w", err)
	}
	if err := m.store.Upsert(ctx, binding, request.MaxBindings); err != nil {
		return Binding{}, fmt.Errorf("upsert registration: %w", err)
	}
	return binding, nil
}

// Resolve returns the preferred active binding for an AoR.
func (m *Manager) Resolve(ctx context.Context, tenantID TenantID, aor AoR) (Binding, error) {
	bindings, err := m.List(ctx, tenantID, aor)
	if err != nil {
		return Binding{}, fmt.Errorf("list active registrations: %w", err)
	}
	if len(bindings) == 0 {
		return Binding{}, ErrNotFound
	}
	slices.SortFunc(bindings, func(left, right Binding) int {
		if left.Q > right.Q {
			return -1
		}
		if left.Q < right.Q {
			return 1
		}
		return right.UpdatedAt.Compare(left.UpdatedAt)
	})
	return bindings[0], nil
}

// List returns all active bindings for an AoR.
func (m *Manager) List(ctx context.Context, tenantID TenantID, aor AoR) ([]Binding, error) {
	bindings, err := m.store.ListActive(ctx, tenantID, aor, m.clock.Now().UTC())
	if err != nil {
		return nil, fmt.Errorf("list active registrations: %w", err)
	}
	return bindings, nil
}

// Expire removes bindings whose expiration is not after now.
func (m *Manager) Expire(ctx context.Context) (int64, error) {
	count, err := m.store.DeleteExpired(ctx, m.clock.Now().UTC())
	if err != nil {
		return 0, fmt.Errorf("delete expired registrations: %w", err)
	}
	return count, nil
}

// IsBindingLimit reports a wrapped binding-limit error.
func IsBindingLimit(err error) bool { return errors.Is(err, ErrBindingLimit) }
