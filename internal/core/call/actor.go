package call

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"
)

const (
	// MaxMailboxCapacity is the validated hard limit for one call mailbox.
	MaxMailboxCapacity     = 1024
	shutdownPublishTimeout = time.Second
)

var (
	// ErrActorClosed indicates that a call actor has exited.
	ErrActorClosed = errors.New("call actor closed")
	// ErrInvalidTransition indicates a disallowed state transition.
	ErrInvalidTransition = errors.New("invalid call state transition")
)

// Clock supplies deterministic actor timestamps.
type Clock interface {
	Now() time.Time
}

// SystemClock reads current UTC time.
type SystemClock struct{}

// Now returns current UTC time.
func (SystemClock) Now() time.Time { return time.Now().UTC() }

// ActorConfig bounds one call actor.
type ActorConfig struct {
	MailboxCapacity int
}

// Transition describes one requested state change.
type Transition struct {
	State         State
	LegID         LegID
	Reason        string
	ProtocolEvent string
}

type command struct {
	transition Transition
	result     chan error
}

// Actor is the sole writer of one Call.
type Actor struct {
	call      Call
	publisher Publisher
	clock     Clock
	mailbox   chan command
	done      chan struct{}
	snapshot  atomic.Pointer[Snapshot]
	sequence  uint64
}

// NewActor constructs a stopped actor. Run controls its goroutine lifecycle.
func NewActor(cfg ActorConfig, current Call, publisher Publisher, clock Clock) (*Actor, error) {
	if cfg.MailboxCapacity <= 0 || cfg.MailboxCapacity > MaxMailboxCapacity {
		return nil, fmt.Errorf("mailbox capacity must be between 1 and %d", MaxMailboxCapacity)
	}
	if publisher == nil {
		return nil, fmt.Errorf("event publisher is required")
	}
	if clock == nil {
		return nil, fmt.Errorf("clock is required")
	}
	actor := &Actor{
		call: current, publisher: publisher, clock: clock,
		mailbox: make(chan command, cfg.MailboxCapacity), done: make(chan struct{}),
	}
	actor.storeSnapshot()
	return actor, nil
}

// Run owns state changes until a terminal state or context cancellation.
func (a *Actor) Run(ctx context.Context) error {
	defer close(a.done)
	if err := a.publish(ctx, "created", "", a.call.State, "", ""); err != nil {
		return fmt.Errorf("publish initial call event: %w", err)
	}
	for {
		select {
		case <-ctx.Done():
			if !isTerminal(a.call.State) {
				shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), shutdownPublishTimeout)
				defer cancel()
				if err := a.apply(shutdownCtx, Transition{State: StateTerminating, Reason: "context canceled", ProtocolEvent: "shutdown"}); err != nil {
					return err
				}
				if err := a.apply(shutdownCtx, Transition{State: StateTerminated, Reason: "context canceled", ProtocolEvent: "shutdown"}); err != nil {
					return err
				}
			}
			return nil
		case command := <-a.mailbox:
			err := a.apply(ctx, command.transition)
			command.result <- err
			if err == nil && isTerminal(a.call.State) {
				return nil
			}
		}
	}
}

// Transition requests and waits for one serialized state change.
func (a *Actor) Transition(ctx context.Context, transition Transition) error {
	result := make(chan error, 1)
	command := command{transition: transition, result: result}
	select {
	case <-a.done:
		return ErrActorClosed
	case <-ctx.Done():
		return ctx.Err()
	case a.mailbox <- command:
	}
	select {
	case err := <-result:
		return err
	case <-a.done:
		return ErrActorClosed
	case <-ctx.Done():
		return ctx.Err()
	}
}

// Done is closed by Run after the actor exits.
func (a *Actor) Done() <-chan struct{} { return a.done }

// Snapshot returns the latest immutable state.
func (a *Actor) Snapshot() Snapshot {
	snapshot := a.snapshot.Load()
	return *snapshot
}

func (a *Actor) apply(ctx context.Context, transition Transition) error {
	if transition.State == a.call.State {
		return nil
	}
	if !allowedTransition(a.call.State, transition.State) {
		return fmt.Errorf("%w: %s to %s", ErrInvalidTransition, a.call.State, transition.State)
	}
	oldState := a.call.State
	now := a.clock.Now().UTC()
	a.call.State = transition.State
	if transition.State == StateAnswered {
		a.call.AnsweredAt = now
	}
	if isTerminal(transition.State) {
		a.call.EndedAt = now
		a.call.Cause = transition.Reason
	}
	if err := a.publish(ctx, transition.ProtocolEvent, transition.Reason, oldState, transition.State, transition.LegID); err != nil {
		a.call.State = oldState
		return fmt.Errorf("publish call transition: %w", err)
	}
	a.storeSnapshot()
	return nil
}

func (a *Actor) publish(ctx context.Context, protocolEvent, reason string, oldState, newState State, legID LegID) error {
	a.sequence++
	return a.publisher.Publish(ctx, Event{
		Sequence: a.sequence, TenantID: a.call.TenantID, CallID: a.call.ID, LegID: legID, NodeID: a.call.NodeID,
		CallerLeg: a.call.CallerLeg, CalleeLeg: a.call.CalleeLeg, Direction: a.call.Direction,
		OldState: oldState, NewState: newState, Reason: reason, ProtocolEvent: protocolEvent,
		OccurredAt: a.clock.Now().UTC(),
	})
}

func (a *Actor) storeSnapshot() {
	snapshot := Snapshot(a.call)
	a.snapshot.Store(&snapshot)
}

func allowedTransition(from, to State) bool {
	switch from {
	case StateNew:
		return to == StateRouting || to == StateFailed || to == StateTerminating
	case StateRouting:
		return to == StateRinging || to == StateAnswered || to == StateFailed || to == StateTerminating
	case StateRinging:
		return to == StateAnswered || to == StateFailed || to == StateTerminating
	case StateAnswered:
		return to == StateTerminating
	case StateTerminating:
		return to == StateTerminated || to == StateFailed
	case StateTerminated, StateFailed:
		return false
	default:
		return false
	}
}

func isTerminal(state State) bool { return state == StateTerminated || state == StateFailed }
