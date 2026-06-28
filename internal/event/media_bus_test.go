package event_test

import (
	"context"
	"testing"

	"github.com/Simoon-F/aixvolink-pbx/internal/event"
	mediasession "github.com/Simoon-F/aixvolink-pbx/internal/media/session"
)

func TestMediaBusIsBoundedAndContextAware(t *testing.T) {
	bus, err := event.NewMediaBus(1)
	if err != nil {
		t.Fatalf("NewMediaBus() error = %v", err)
	}
	if err := bus.PublishMediaSummary(context.Background(), mediasession.Summary{CallID: "call-1"}); err != nil {
		t.Fatalf("PublishMediaSummary() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := bus.PublishMediaSummary(ctx, mediasession.Summary{CallID: "call-2"}); err == nil {
		t.Fatal("PublishMediaSummary(full) error = nil")
	}
}
