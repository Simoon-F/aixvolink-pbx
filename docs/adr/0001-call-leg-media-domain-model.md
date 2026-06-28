# ADR-0001: Call, Leg, and Media Session domain model

- Status: Accepted
- Date: 2026-06-28
- Deciders: AixvoLinkPBX maintainers

## Context

SIP `Call-ID` identifies protocol dialogs imperfectly: a B2BUA creates independent dialogs, retries can create additional legs, and a single business call can own multiple media negotiations. Diagnosis, CDR, and observability require stable internal identities that do not depend on a phone number or one vendor's headers.

## Decision

The core model has three aggregates with explicit ownership:

- `Call` is one business communication attempt. It owns `CallID` (UUIDv7), `TenantID`, `NodeID`, direction, routing result, lifecycle timestamps, normalized cause, and the IDs of its legs and media sessions. Its initial states are `new`, `routing`, `ringing`, `early_media`, `answered`, `held`, `terminating`, `terminated`, and `failed`. State changes occur only through the call actor described by ADR-0002.
- `Leg` is one independently controlled SIP dialog side. It owns `LegID`, protocol `SIPCallID`, local and remote URIs, tags, CSeq state, transport and addresses, direction, dialog state, SDP snapshots, negotiated codec, trunk or registration reference, timestamps, SIP status, and hangup cause. Protocol status and normalized business cause remain separate types.
- `MediaSession` connects exactly two media endpoints for one negotiated interval. It owns `MediaSessionID`, related `CallID` and leg IDs, endpoint addresses, RTP/RTCP identifiers, codec and payload mapping, ICE candidate pair, DTLS/SRTP profile, DTMF payload type, timestamps, and summarized counters. A re-negotiation may replace a session rather than mutating historical evidence.

All persistent events, CDR rows, diagnostic samples, and call-scoped logs carry `CallID` and `NodeID`; leg or media events additionally carry their corresponding ID. IDs are typed values in core packages. Protocol/library objects are adapted at package boundaries and are not the domain model.

The actor owns mutable `Call` and `Leg` lifecycle state. A media worker owns packet-loop state and publishes fixed-interval immutable summaries to the actor. Neither aggregate performs database, HTTP, or vendor calls directly.

## Consequences

- Both B2BUA legs and successive media negotiations remain correlated without relying on a phone number.
- SIP, carrier, storage, and API representations can evolve independently.
- UUIDv7 generation, exact cause taxonomy, persistence schemas, and detailed transition tables remain Phase 1 decisions with dedicated tests.
- The model requires explicit adapters and more identifiers than a SIP-only implementation, but that cost buys reliable diagnosis.

## Alternatives considered

- Use SIP `Call-ID` as the business identifier: rejected because B2BUA legs have independent identifiers and retries/forks are ambiguous.
- Combine call, dialog, and media in one session object: rejected because their lifecycles and concurrency owners differ.
- Persist raw sipgo or Pion structures: rejected because it couples the core and storage contract to dependency APIs.
