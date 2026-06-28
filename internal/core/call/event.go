package call

import (
	"context"
	"time"
)

// Event is an immutable state-transition record.
type Event struct {
	Sequence      uint64
	TenantID      TenantID
	CallID        ID
	LegID         LegID
	NodeID        NodeID
	CallerLeg     LegID
	CalleeLeg     LegID
	Direction     Direction
	OldState      State
	NewState      State
	Reason        string
	ProtocolEvent string
	OccurredAt    time.Time
}

// Publisher accepts ordered call events with bounded backpressure.
type Publisher interface {
	Publish(ctx context.Context, event Event) error
}
