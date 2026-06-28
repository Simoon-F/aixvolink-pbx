// Package registration defines SIP registration domain state and lifecycle rules.
package registration

import (
	"errors"
	"fmt"
	"net/netip"
	"time"
)

// ID identifies one registration binding.
type ID string

// TenantID identifies the tenant that owns a registration.
type TenantID string

// AoR is a normalized SIP address of record.
type AoR string

// Transport identifies a SIP transport.
type Transport string

const (
	// TransportUDP is SIP over UDP.
	TransportUDP Transport = "udp"
	// TransportTCP is SIP over TCP.
	TransportTCP Transport = "tcp"
)

var (
	// ErrBindingLimit indicates that an AoR has reached its configured device limit.
	ErrBindingLimit = errors.New("registration binding limit reached")
	// ErrNotFound indicates that no active registration can be resolved.
	ErrNotFound = errors.New("registration not found")
)

// Binding is one routable Contact for an address of record.
type Binding struct {
	ID          ID
	TenantID    TenantID
	AoR         AoR
	Contact     string
	RouteTarget string
	Transport   Transport
	Received    netip.AddrPort
	UserAgent   string
	Q           float32
	ExpiresAt   time.Time
	UpdatedAt   time.Time
}

// Validate rejects incomplete or unsafe binding state.
func (b Binding) Validate(now time.Time) error {
	if b.ID == "" {
		return fmt.Errorf("registration ID is required")
	}
	if b.TenantID == "" {
		return fmt.Errorf("tenant ID is required")
	}
	if b.AoR == "" {
		return fmt.Errorf("address of record is required")
	}
	if b.Contact == "" {
		return fmt.Errorf("contact is required")
	}
	if b.RouteTarget == "" {
		return fmt.Errorf("route target is required")
	}
	if b.Transport != TransportUDP && b.Transport != TransportTCP {
		return fmt.Errorf("unsupported transport %q", b.Transport)
	}
	if !b.Received.IsValid() {
		return fmt.Errorf("received address is required")
	}
	if b.Q < 0 || b.Q > 1 {
		return fmt.Errorf("q value must be between 0 and 1")
	}
	if !b.ExpiresAt.After(now) {
		return fmt.Errorf("expiration must be in the future")
	}
	if b.UpdatedAt.IsZero() {
		return fmt.Errorf("updated time is required")
	}
	return nil
}
