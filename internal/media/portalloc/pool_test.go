package portalloc_test

import (
	"context"
	"errors"
	"net/netip"
	"testing"

	"github.com/Simoon-F/aixvolink-pbx/internal/media/portalloc"
)

func TestPoolExhaustsAndReusesReleasedPair(t *testing.T) {
	pool, err := portalloc.New(portalloc.Config{BindIP: netip.MustParseAddr("127.0.0.1"), StartPort: 24000, EndPort: 24003})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	first, err := pool.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire(first) error = %v", err)
	}
	second, err := pool.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire(second) error = %v", err)
	}
	if _, err := pool.Acquire(context.Background()); !errors.Is(err, portalloc.ErrExhausted) {
		t.Fatalf("Acquire(exhausted) error = %v", err)
	}
	if pool.InUse() != 2 {
		t.Fatalf("InUse() = %d, want 2", pool.InUse())
	}
	releasedPort := first.RTPPort()
	if err := first.Release(); err != nil {
		t.Fatalf("Release() error = %v", err)
	}
	reused, err := pool.Acquire(context.Background())
	if err != nil {
		t.Fatalf("Acquire(reused) error = %v", err)
	}
	if reused.RTPPort() != releasedPort {
		t.Fatalf("reused port = %d, want %d", reused.RTPPort(), releasedPort)
	}
	if err := first.Release(); err != nil {
		t.Fatalf("second Release() error = %v", err)
	}
	if err := reused.Release(); err != nil {
		t.Fatalf("Release(reused) error = %v", err)
	}
	if err := second.Release(); err != nil {
		t.Fatalf("Release(second) error = %v", err)
	}
	if pool.InUse() != 0 {
		t.Fatalf("InUse() = %d, want 0", pool.InUse())
	}
}

func TestPoolValidatesPortRange(t *testing.T) {
	tests := []portalloc.Config{
		{},
		{BindIP: netip.MustParseAddr("127.0.0.1"), StartPort: 24001, EndPort: 24003},
		{BindIP: netip.MustParseAddr("127.0.0.1"), StartPort: 24000, EndPort: 24002},
	}
	for _, config := range tests {
		if _, err := portalloc.New(config); err == nil {
			t.Fatalf("New(%+v) error = nil", config)
		}
	}
}
