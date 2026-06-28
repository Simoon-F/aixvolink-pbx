# ADR-0007: Phase 2 anchored media engine

- Status: Accepted
- Date: 2026-06-28
- Deciders: AixvoLinkPBX maintainers

## Context

Phase 2 needs bounded RTP/RTCP anchoring, G.711 negotiation, quality evidence, RFC 4733 DTMF, and early media while preserving the Call actor and hot-path constraints in ADR-0001 through ADR-0003. Diago proves the required primitives but its process-global port policy and full session ownership do not express AixvoLinkPBX admission, lifecycle, and diagnostic boundaries.

## Decision

AixvoLinkPBX owns a media session factory under `internal/media/session`. Each call media session leases two even RTP/odd RTCP UDP port pairs from a bounded `internal/media/portalloc` pool: one pair faces the caller leg and one faces the callee leg. A lease binds sockets before admission and returns its pair exactly once on close. Port exhaustion rejects the SIP offer with 503 and never falls back to an unbounded or operating-system-selected port.

SDP is parsed and generated through `pion/sdp`; RTP and RTCP packets use `pion/rtp` and `pion/rtcp`. Phase 2 accepts only plain `RTP/AVP`, PCMU/8000, PCMA/8000, and optional telephone-event/8000. The B2BUA terminates offer/answer independently on both legs, advertises its leg-facing port, and starts forwarding after a valid 183 or 200 answer. No common G.711 codec produces SIP 488; no transcoder is invoked.

Each direction has one packet-loop owner. It validates RTP version, payload type, SSRC, and learned source, then rewrites payload type, SSRC, sequence, and timestamp before sending from the opposite leg-facing socket. Symmetric RTP may learn one source port only from the SDP-advertised IP and then pins it. RFC 4733 payloads are decoded for bounded counters and forwarded with the negotiated payload mapping; they are not converted to SIP INFO.

RTCP has explicit reader/writer lifecycle owned by the session. Fixed-interval SR or RR packets and immutable quality snapshots cover packets, bytes, sequence loss, duplicate, reorder, jitter, RTT, SSRC changes, one-way media, and inactivity. RTP loops perform no database, HTTP, file, or per-packet log work. Slow summary consumers are outside packet loops and have a bounded timeout.

## Consequences

- Two port pairs per call make source validation and per-leg quality attribution explicit at the cost of four UDP ports and four sockets per active call.
- Diago remains an unmodified reference/selective dependency; no MPL-covered source is copied or modified.
- Phase 2 does not add SRTP, ICE, WebRTC, transcoding, recording, carrier logic, or a jitter buffer.
- Re-INVITE/UPDATE renegotiation must replace or reconfigure a media session under the call actor; it cannot mutate packet-loop state ad hoc.
- The initial 100-call gate requires at least 400 configured UDP ports plus headroom.

## Alternatives considered

- One shared RTP socket pair per call: rejected because two remote sources complicate leg attribution, source pinning, and RTCP routing.
- Adopt Diago as the full media owner: rejected by ADR-0003 because bounded allocation, lifecycle, and diagnostics remain Core responsibilities.
- Forward packets without header rewriting: rejected because a B2BUA media anchor must isolate leg SSRC, sequence, timestamp, and payload mappings.
- Add a jitter buffer immediately: deferred until loss/jitter evidence demonstrates a need; it would add latency and more hot-path state.
