# Phase 2 media and in-dialog protocol behavior

## Initial call and media anchoring

```text
Caller                 AixvoLinkPBX                  Callee
  | INVITE + SDP(A)          |                          |
  |------------------------->| INVITE + PBX SDP(B)     |
  |                          |------------------------->|
  |                          | 180                      |
  | 180                      |<-------------------------|
  |<-------------------------|                          |
  |                          | 200 + SDP(C)             |
  | 200 + PBX SDP(D)         |<-------------------------|
  |<-------------------------| ACK                      |
  | ACK                      |------------------------->|
  |==== RTP/RTCP to A-facing port == PBX == RTP/RTCP to B-facing port ====|
```

Only RTP/AVP PCMU/8000 and PCMA/8000 are accepted. Optional telephone-event/8000 payload types are mapped independently per leg. The anchor rewrites SSRC, sequence, timestamp, and payload type. No common G.711 codec returns 488; exhausted media ports return 503.

## Early media

A valid 183 with SDP completes the independent callee-leg negotiation, starts the media session, transitions the Call actor to `early_media`, and is relayed with the caller-facing PBX SDP. The final 200 must keep the negotiated codec mapping; it may update the pinned remote media address.

## Hold and resume

An in-dialog re-INVITE from the caller is serialized through the call command mailbox and relayed as an independent re-INVITE to the callee leg. `sendonly`, `recvonly`, or `inactive` moves the Call to `held`; `sendrecv` resumes `answered`. A second re-INVITE before ACK receives 491. ACK terminates the matching server transaction.

## Blind transfer

Caller REFER is validated in-dialog, relayed to the callee leg with Refer-To/Referred-By, and its final response is returned to the caller. Refer NOTIFY is relayed in the reverse direction with Event, Subscription-State, Content-Type, and sipfrag body. The transfer target's resulting call remains a normal independently admitted call.

## Executable evidence

- `test/integration/internal_call_test.go`: real UDP SDP negotiation, bidirectional RTP, RFC 4733, 183 media, hold/resume, REFER/NOTIFY, and 488 behavior.
- `internal/media/session/session_integration_test.go`: RTCP generation/ingestion, payload mapping, loss, one-way media, inactivity, and cleanup.
- `test/sipp`: G.711 SDP call, early-media, rejection, CANCEL, and BYE ladders plus a PCMU stream echoed through both anchored legs against a fresh process.
