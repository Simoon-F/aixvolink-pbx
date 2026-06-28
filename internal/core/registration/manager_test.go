package registration_test

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/Simoon-F/aixvolink-pbx/internal/core/registration"
	"github.com/Simoon-F/aixvolink-pbx/internal/platform/memory"
)

type fixedClock struct{ now time.Time }

func (c *fixedClock) Now() time.Time { return c.now }

func TestManagerHandlesRefreshUnregisterAndExpiry(t *testing.T) {
	clock := &fixedClock{now: time.Date(2026, 6, 28, 8, 0, 0, 0, time.UTC)}
	store := memory.NewRegistrationStore()
	manager, err := registration.NewManager(store, clock)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	request := registration.Request{
		TenantID:    "tenant-1",
		AoR:         "sip:1001@example.invalid",
		Contact:     "sip:1001@192.0.2.10:5060",
		RouteTarget: "127.0.0.1:15061",
		Transport:   registration.TransportUDP,
		Received:    netip.MustParseAddrPort("127.0.0.1:15061"),
		Q:           1,
		Expires:     time.Minute,
		MaxBindings: 2,
	}

	created, err := manager.Apply(context.Background(), request)
	if err != nil {
		t.Fatalf("Apply(create) error = %v", err)
	}
	clock.now = clock.now.Add(10 * time.Second)
	request.Expires = 2 * time.Minute
	refreshed, err := manager.Apply(context.Background(), request)
	if err != nil {
		t.Fatalf("Apply(refresh) error = %v", err)
	}
	if !refreshed.ExpiresAt.After(created.ExpiresAt) {
		t.Fatal("refresh did not extend expiration")
	}

	request.Expires = 0
	if _, err := manager.Apply(context.Background(), request); err != nil {
		t.Fatalf("Apply(unregister) error = %v", err)
	}
	if _, err := manager.Resolve(context.Background(), request.TenantID, request.AoR); !errors.Is(err, registration.ErrNotFound) {
		t.Fatalf("Resolve() error = %v, want ErrNotFound", err)
	}

	request.Expires = time.Second
	if _, err := manager.Apply(context.Background(), request); err != nil {
		t.Fatalf("Apply(expiring) error = %v", err)
	}
	clock.now = clock.now.Add(2 * time.Second)
	deleted, err := manager.Expire(context.Background())
	if err != nil {
		t.Fatalf("Expire() error = %v", err)
	}
	if deleted != 1 {
		t.Fatalf("Expire() deleted = %d, want 1", deleted)
	}
}

func TestManagerEnforcesBindingLimitAndPreference(t *testing.T) {
	clock := &fixedClock{now: time.Date(2026, 6, 28, 8, 0, 0, 0, time.UTC)}
	manager, err := registration.NewManager(memory.NewRegistrationStore(), clock)
	if err != nil {
		t.Fatalf("NewManager() error = %v", err)
	}
	base := registration.Request{
		TenantID: "tenant-1", AoR: "sip:1001@example.invalid",
		Transport: registration.TransportUDP, Expires: time.Minute, MaxBindings: 2,
	}

	for index, contact := range []string{"sip:1001@device-a.invalid", "sip:1001@device-b.invalid"} {
		request := base
		request.Contact = contact
		request.RouteTarget = netip.MustParseAddrPort("127.0.0.1:15061").String()
		request.Received = netip.MustParseAddrPort("127.0.0.1:15061")
		request.Q = float32(index) / 2
		if _, err := manager.Apply(context.Background(), request); err != nil {
			t.Fatalf("Apply(%s) error = %v", contact, err)
		}
	}
	preferred, err := manager.Resolve(context.Background(), base.TenantID, base.AoR)
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}
	if preferred.Contact != "sip:1001@device-b.invalid" {
		t.Fatalf("preferred Contact = %q", preferred.Contact)
	}

	request := base
	request.Contact = "sip:1001@device-c.invalid"
	request.RouteTarget = "127.0.0.1:15063"
	request.Received = netip.MustParseAddrPort("127.0.0.1:15063")
	request.Q = 1
	if _, err := manager.Apply(context.Background(), request); !registration.IsBindingLimit(err) {
		t.Fatalf("Apply(over limit) error = %v", err)
	}
}

func BenchmarkManagerRefresh(b *testing.B) {
	clock := &fixedClock{now: time.Date(2026, 6, 28, 8, 0, 0, 0, time.UTC)}
	manager, err := registration.NewManager(memory.NewRegistrationStore(), clock)
	if err != nil {
		b.Fatal(err)
	}
	request := registration.Request{
		TenantID: "tenant-bench", AoR: "sip:1001@example.invalid", Contact: "sip:1001@127.0.0.1:5062",
		RouteTarget: "127.0.0.1:5062", Transport: registration.TransportUDP,
		Received: netip.MustParseAddrPort("127.0.0.1:5062"), Q: 1, Expires: time.Minute, MaxBindings: 2,
	}
	b.ReportAllocs()
	b.ResetTimer()
	for range b.N {
		if _, err := manager.Apply(context.Background(), request); err != nil {
			b.Fatal(err)
		}
	}
}
