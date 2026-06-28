package call_test

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/Simoon-F/aixvolink-pbx/internal/core/call"
)

type fixedClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *fixedClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *fixedClock) Advance(duration time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(duration)
	c.mu.Unlock()
}

type recorder struct {
	mu     sync.Mutex
	events []call.Event
}

func (r *recorder) Publish(ctx context.Context, event call.Event) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	r.mu.Lock()
	r.events = append(r.events, event)
	r.mu.Unlock()
	return nil
}

func TestActorSerializesCallLifecycleWithCorrelatedEvents(t *testing.T) {
	clock := &fixedClock{now: time.Date(2026, 6, 28, 8, 0, 0, 0, time.UTC)}
	current, err := call.New("tenant-1", "node-1", clock.now)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	recorder := &recorder{}
	actor, err := call.NewActor(call.ActorConfig{MailboxCapacity: 64}, current, recorder, clock)
	if err != nil {
		t.Fatalf("NewActor() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() { result <- actor.Run(ctx) }()

	transitions := []call.Transition{
		{State: call.StateRouting, LegID: current.CallerLeg, ProtocolEvent: "INVITE"},
		{State: call.StateRinging, LegID: current.CalleeLeg, ProtocolEvent: "180"},
		{State: call.StateAnswered, LegID: current.CalleeLeg, ProtocolEvent: "200"},
		{State: call.StateTerminating, LegID: current.CallerLeg, ProtocolEvent: "BYE"},
		{State: call.StateTerminated, LegID: current.CallerLeg, ProtocolEvent: "200", Reason: "normal clearing"},
	}
	for _, transition := range transitions {
		clock.Advance(time.Millisecond)
		if err := actor.Transition(context.Background(), transition); err != nil {
			t.Fatalf("Transition(%s) error = %v", transition.State, err)
		}
	}
	if err := <-result; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	snapshot := actor.Snapshot()
	if snapshot.State != call.StateTerminated || snapshot.Cause != "normal clearing" {
		t.Fatalf("Snapshot() = %+v", snapshot)
	}
	recorder.mu.Lock()
	defer recorder.mu.Unlock()
	if len(recorder.events) != 6 {
		t.Fatalf("event count = %d, want 6", len(recorder.events))
	}
	for _, event := range recorder.events {
		if event.CallID != current.ID || event.NodeID != current.NodeID || event.TenantID != current.TenantID {
			t.Fatalf("uncorrelated event = %+v", event)
		}
	}
}

func TestActorRejectsInvalidTransition(t *testing.T) {
	clock := &fixedClock{now: time.Now().UTC()}
	current, err := call.New("tenant-1", "node-1", clock.now)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	actor, err := call.NewActor(call.ActorConfig{MailboxCapacity: 1}, current, &recorder{}, clock)
	if err != nil {
		t.Fatalf("NewActor() error = %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() { result <- actor.Run(ctx) }()
	if err := actor.Transition(context.Background(), call.Transition{State: call.StateAnswered}); !errors.Is(err, call.ErrInvalidTransition) {
		t.Fatalf("Transition() error = %v", err)
	}
	cancel()
	if err := <-result; err != nil {
		t.Fatalf("Run() error = %v", err)
	}
}
