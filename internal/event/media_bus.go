package event

import (
	"context"
	"fmt"

	mediasession "github.com/Simoon-F/aixvolink-pbx/internal/media/session"
)

// MediaWriter persists or exports call-correlated media summaries.
type MediaWriter interface {
	WriteMediaSummary(ctx context.Context, summary mediasession.Summary) error
}

// MediaBus is a bounded single-consumer quality-sample queue.
type MediaBus struct {
	summaries chan mediasession.Summary
}

// NewMediaBus constructs a queue with explicit capacity.
func NewMediaBus(capacity int) (*MediaBus, error) {
	if capacity <= 0 {
		return nil, fmt.Errorf("media summary capacity must be positive")
	}
	return &MediaBus{summaries: make(chan mediasession.Summary, capacity)}, nil
}

// PublishMediaSummary applies bounded backpressure outside RTP packet loops.
func (b *MediaBus) PublishMediaSummary(ctx context.Context, summary mediasession.Summary) error {
	select {
	case b.summaries <- summary:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Run delivers summaries serially until cancellation.
func (b *MediaBus) Run(ctx context.Context, writer MediaWriter) error {
	if writer == nil {
		return fmt.Errorf("media summary writer is required")
	}
	for {
		select {
		case summary := <-b.summaries:
			if err := writer.WriteMediaSummary(ctx, summary); err != nil {
				return fmt.Errorf("write media summary: %w", err)
			}
		case <-ctx.Done():
			return nil
		}
	}
}

// Depth returns the current bounded queue depth.
func (b *MediaBus) Depth() int { return len(b.summaries) }
