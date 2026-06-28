# ADR-0003: SIP, media, and WebRTC dependencies

- Status: Accepted
- Date: 2026-06-28
- Deciders: AixvoLinkPBX maintainers

## Context

AixvoLinkPBX must not implement partial SIP parsing/transactions, ICE, DTLS, SRTP, or cryptography. Phase 0 needs executable evidence that candidate Go libraries provide the required primitives, plus a boundary that prevents experimental code from becoming an accidental production architecture.

## Decision

Lock these initial versions in `go.mod` and `go.sum`:

- `github.com/emiago/sipgo v1.4.0` for SIP parsing, UDP/TCP/TLS/WS/WSS transports, transactions, and dialog foundations.
- `github.com/emiago/diago v0.29.0` for media behavior reference and selective reuse of codec, SDP, RTP/RTCP, DTMF, playback/recording, and bridge primitives.
- `github.com/pion/webrtc/v4 v4.2.15` for PeerConnection, ICE, DTLS-SRTP, RTP/RTCP access, STUN, and TURN clients.
- Pion submodules at the versions selected by Minimal Version Selection; direct `pion/rtp v1.10.2` is used by the executable media verification.
- Go 1.25 is the minimum toolchain. Security-fixed transitive floors are `golang.org/x/crypto v0.52.0`, `golang.org/x/net v0.55.0`, and `golang.org/x/sys v0.45.0`; the prior dependency-selected versions had known module-level vulnerabilities and the fixed releases require Go 1.25.

The production code will wrap these dependencies behind responsibility-specific packages. Core call and routing packages do not import them. Upgrades to sipgo, Diago, Pion, or cryptographic transports require protocol and browser interoperability regression tests.

Diago is not adopted as the owner of the full PBX architecture. The Phase 0 spike confirms its media session can bind RTP/RTCP and echo PCMU/PCMA with zero allocations in the isolated RTP parse benchmark. AixvoLinkPBX retains ownership of port allocation policy, source validation, media worker lifecycle, hot-path budgets, metrics, and backpressure. Reuse is decided per Diago package/file behavior after tests.

Pion terminates browser WebRTC. It does not implement SIP signaling, and external coturn remains independently deployed. The Phase 0 Pion spike uses host candidates only; STUN/TURN and JsSIP/WSS interoperability remain explicit later-phase gates.

## Evidence and licenses

The Phase 0 automated tests demonstrated sipgo UDP/TCP `OPTIONS` transactions, Diago PCMU/PCMA RTP echo, and a Pion-to-Pion HTTP offer/answer with ICE, DTLS-SRTP, and Opus RTP echo. Manual browser instructions exercise the same Pion endpoint.

sipgo is BSD-2-Clause, Diago is MPL-2.0, and Pion is MIT. No upstream source is copied or modified. MPL-covered modifications, if any occur later, must remain available under MPL-2.0. CI checks the build dependency graph against the approved permissive/MPL set; legal review is still required before a production release.

## Consequences

- Security-sensitive protocol machinery comes from maintained upstream projects.
- Dependency API changes are isolated by adapters and version locks.
- Diago's MPL-2.0 file-level obligations must be tracked carefully.
- TLS/WSS SIP, JsSIP, relay candidates, adverse NAT, and cross-browser behavior are not proven by the current spikes and remain risks.

## Alternatives considered

- Hand-written parsers or security protocols: rejected by the project security baseline.
- Adopt Diago as the complete PBX framework: rejected because call ownership, overload policy, and diagnostic contracts must remain AixvoLinkPBX-controlled.
- Embed a TURN server: rejected because coturn is an external independently operated service.
