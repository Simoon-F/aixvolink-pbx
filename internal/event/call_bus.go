// Package event provides bounded asynchronous domain-event delivery.
package event

import (
	"context"
	"fmt"

	"github.com/Simoon-F/aixvolink-pbx/internal/core/call"
)

// CallWriter persists or exports ordered call events.
type CallWriter interface {
	WriteCallEvent(ctx context.Context, event call.Event) error
}

// CallBus is a bounded single-consumer call event queue.
type CallBus struct {
	events chan call.Event
}

// NewCallBus constructs a queue with explicit capacity.
func NewCallBus(capacity int) (*CallBus, error) {
	if capacity <= 0 {
		return nil, fmt.Errorf("call event capacity must be positive")
	}
	return &CallBus{events: make(chan call.Event, capacity)}, nil
}

// Publish applies backpressure until capacity or context cancellation.
func (b *CallBus) Publish(ctx context.Context, event call.Event) error {
	select {
	case b.events <- event:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Run delivers events serially and performs no work in call actor goroutines.
func (b *CallBus) Run(ctx context.Context, writer CallWriter) error {
	if writer == nil {
		return fmt.Errorf("call event writer is required")
	}
	for {
		select {
		case event := <-b.events:
			if err := writer.WriteCallEvent(ctx, event); err != nil {
				return fmt.Errorf("write call event: %w", err)
			}
		case <-ctx.Done():
			return nil
		}
	}
}

// Depth returns the current queue depth for low-cardinality metrics.
func (b *CallBus) Depth() int { return len(b.events) }
