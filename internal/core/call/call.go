// Package call defines business call state and its single-writer actor.
package call

import (
	"fmt"
	"time"

	"github.com/google/uuid"
)

// ID identifies one business call.
type ID string

// LegID identifies one independently controlled SIP dialog leg.
type LegID string

// TenantID identifies the tenant that owns a call.
type TenantID string

// NodeID identifies the PBX node processing a call.
type NodeID string

// State is a stable serialized call state.
type State string

const (
	StateNew         State = "new"
	StateRouting     State = "routing"
	StateRinging     State = "ringing"
	StateEarlyMedia  State = "early_media"
	StateAnswered    State = "answered"
	StateHeld        State = "held"
	StateTerminating State = "terminating"
	StateTerminated  State = "terminated"
	StateFailed      State = "failed"
)

// Direction is the source category of a call.
type Direction string

const (
	// DirectionInternal is a call between registered users.
	DirectionInternal Direction = "internal"
)

// Call is mutable only by its owning actor.
type Call struct {
	ID         ID
	TenantID   TenantID
	NodeID     NodeID
	Direction  Direction
	CallerLeg  LegID
	CalleeLeg  LegID
	State      State
	StartedAt  time.Time
	AnsweredAt time.Time
	EndedAt    time.Time
	Cause      string
}

// New constructs a valid internal call in StateNew.
func New(tenantID TenantID, nodeID NodeID, now time.Time) (Call, error) {
	if tenantID == "" {
		return Call{}, fmt.Errorf("tenant ID is required")
	}
	if nodeID == "" {
		return Call{}, fmt.Errorf("node ID is required")
	}
	callID, err := uuid.NewV7()
	if err != nil {
		return Call{}, fmt.Errorf("generate call ID: %w", err)
	}
	callerLegID, err := uuid.NewV7()
	if err != nil {
		return Call{}, fmt.Errorf("generate caller leg ID: %w", err)
	}
	calleeLegID, err := uuid.NewV7()
	if err != nil {
		return Call{}, fmt.Errorf("generate callee leg ID: %w", err)
	}
	return Call{
		ID: ID(callID.String()), TenantID: tenantID, NodeID: nodeID,
		Direction: DirectionInternal, CallerLeg: LegID(callerLegID.String()), CalleeLeg: LegID(calleeLegID.String()),
		State: StateNew, StartedAt: now.UTC(),
	}, nil
}

// Snapshot is an immutable actor state view.
type Snapshot Call
