# ADR-0002: Per-call actor concurrency model

- Status: Accepted
- Date: 2026-06-28
- Deciders: AixvoLinkPBX maintainers

## Context

SIP transactions, timers, media status, and administrative actions arrive concurrently. CANCEL versus 200 OK, duplicate requests, and one-side-first termination must be deterministic. Locks spread across handlers make ownership unclear, while unbounded goroutines or queues turn overload into memory growth and delayed calls.

## Decision

Each active `Call` has one actor goroutine and one bounded mailbox. That actor is the sole writer of call and leg lifecycle state. SIP transport, transaction callbacks, media summaries, and timers translate input into immutable internal events; they never mutate the call directly.

The Phase 1 starting mailbox capacity is configurable with a default of 64 events and a hard validated maximum. A call manager owns actor creation and removal and enforces global active-call limits. Event submission is context-aware and never creates a goroutine merely to wait for capacity.

Protocol-critical events such as transaction completion, CANCEL, BYE, and shutdown are not silently dropped. If they cannot be admitted within their bounded dispatch deadline, the system records overload, rejects new work where the protocol permits, and drives deterministic termination. Low-priority diagnostic samples may be dropped with an aggregate counter. No retry is implicit or unbounded.

The actor owns its mailbox close and exit sequence. It stops timers, tells media sessions to stop, publishes one terminal snapshot, and then unregisters itself. A process-level context initiates shutdown; per-call and per-transaction contexts remain distinct. Tests use injected clocks for state timers and exercise race cases without real sleeps.

RTP packet loops are separate lifecycle-owned workers. They do not send per-packet actor events and never perform synchronous database, HTTP, file, or structured logging work. They expose bounded periodic summaries or atomic snapshots.

## Consequences

- Call transitions are serial, auditable, and race-testable.
- Overload behavior is explicit rather than hidden in memory growth.
- One goroutine per active call is an accepted initial cost; capacity tests will measure it before production sizing.
- Mailbox sizing and dispatch deadlines require benchmark and SIPp evidence in Phase 1 and may be tuned without changing ownership semantics.

## Alternatives considered

- A mutex-protected global call map with handlers mutating calls: rejected because ownership and multi-step transition atomicity remain unclear.
- One goroutine per event: rejected because fan-out is unbounded and ordering becomes nondeterministic.
- A single global actor: rejected because an expensive or stuck call would head-of-line block unrelated calls.
- Unbounded mailboxes: rejected because overload becomes increasing latency and memory use.
