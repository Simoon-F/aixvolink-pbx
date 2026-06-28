# ADR 0006: Phase 1 SIP authentication boundary

- Status: Accepted
- Date: 2026-06-28

## Context

Phase 1 requires Digest-authenticated registration and internal calls without introducing the policy engine, edge proxy, or multi-node routing planned for later phases. The B2BUA must still reject an unauthenticated calling source.

## Decision

REGISTER uses Digest MD5 with `qop=auth`, precomputed HA1 credentials, HMAC-SHA-256 protected time-bounded nonces, nonce-count replay rejection, and a bounded replay cache. Plaintext passwords are not stored by Core.

For Phase 1 only, an INVITE is admitted when its source IP:port and transport exactly match an active binding for the From AoR in the configured tenant. This is a deliberately narrow single-node trust boundary, not a claim that source tuples are user authentication. Stable Core interfaces remain the registration store, call events, and SIP protocol boundary.

## Consequences

- Stolen or spoofable source tuples, shared NAT endpoints, connection migration, and registrations routed through a proxy can invalidate this assumption.
- Production edge deployment must add authenticated INVITE policy or a trusted edge identity before exposing Core beyond a controlled network.
- The Phase 1 server binds loopback by default, and the risk is recorded in the validation report rather than silently expanding Phase 1 into edge-security work.

## Alternatives considered

- Challenge every INVITE with Digest. This is stronger for direct endpoints but requires a broader authentication and interoperability policy than the Phase 1 plan defines.
- Trust the From header without source correlation. Rejected because it permits trivial caller impersonation.
