# ADR-0004: Open Core and commercial service boundary

- Status: Accepted
- Date: 2026-06-28
- Deciders: AixvoLinkPBX maintainers

## Context

The open-core model must fund hosted and enterprise capabilities without making an independently deployed PBX unreliable or dependent on an Aixvo control plane. A stable boundary is also needed to prevent Go implementation details from coupling separately released commercial services to Core.

## Decision

Open Core includes everything required for independently deployed basic calls: SIP registration and authentication, B2BUA and basic routing, RTP/RTCP and WebRTC termination, external ICE server configuration, PCMU/PCMA calling, DTMF, hold, blind transfer, basic Twilio/Bandwidth adapters, CDR and basic quality evidence, Prometheus metrics, a basic management API/UI, and open Media Stream/AI provider interfaces.

Commercial or hosted products may provide managed PBX/TURN and SLA, multi-region clustering and disaster recovery, automatic scaling, advanced least-cost routing and carrier operations, enterprise RBAC/SSO/audit/compliance controls, long-term cross-node analytics and advanced attribution, and managed transcription, summarization, quality, or AI agents.

Core never requires online license validation, Aixvo cloud connectivity, or a commercial control plane to place an ordinary supported call. Commercial services integrate through versioned HTTP, gRPC, or WebSocket contracts with bounded timeouts and failure isolation. Go native plugins and imports from private commercial modules into Core are prohibited.

AI and commercial consumers receive media only through a bounded side channel. Their timeout, congestion, or failure drops the optional copy and cannot block the RTP path or terminate a call. Public contracts and a local example provider remain in Core.

AixvoLinkPBX Core is licensed under Apache-2.0. Commercial hosted services and enterprise components remain separate works and integrate through the stable network protocols above. No Contributor License Agreement is required at Phase 0; introducing one later is a separate governance decision that requires explicit review.

## Consequences

- The community build remains useful and independently operable.
- Hosted differentiation concentrates on operations, scale, enterprise governance, and managed intelligence.
- Protocol contracts need versioning and compatibility tests.
- Some capabilities exist in both tiers at different service levels; documentation must distinguish function from managed operation clearly.

## Alternatives considered

- License checks in the call path: rejected because loss of a control plane would break basic calls.
- Go native plugins: rejected because they create toolchain and ABI coupling.
- Keep carrier adapters or WebRTC commercial-only: rejected because Core would not be a functional PBX for the stated use cases.
