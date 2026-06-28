package event_test

import (
	"context"
	"errors"
	"testing"

	"github.com/Simoon-F/aixvolink-pbx/internal/core/call"
	"github.com/Simoon-F/aixvolink-pbx/internal/event"
)

func TestCallBusIsBoundedAndContextAware(t *testing.T) {
	bus, err := event.NewCallBus(1)
	if err != nil {
		t.Fatalf("NewCallBus() error = %v", err)
	}
	if err := bus.Publish(context.Background(), call.Event{CallID: "call-1"}); err != nil {
		t.Fatalf("Publish(first) error = %v", err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if err := bus.Publish(canceled, call.Event{CallID: "call-2"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("Publish(full) error = %v", err)
	}
	if bus.Depth() != 1 {
		t.Fatalf("Depth() = %d, want 1", bus.Depth())
	}
}
