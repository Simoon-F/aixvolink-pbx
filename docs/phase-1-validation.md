# Phase 1 validation report

- Date: 2026-06-28 (Asia/Shanghai)
- Repository: `github.com/Simoon-F/aixvolink-pbx`
- Scope: Registrar, internal SIP calls, basic B2BUA state, CDRs, and structured events only
- Host: Apple M4 Pro, 14 logical CPUs, darwin/arm64
- Toolchain used: Go 1.26.4; module minimum remains Go 1.25

## Outcome

Phase 1 is implemented without introducing RTP/media, carrier adapters, administration UI, or commercial services. UDP and TCP REGISTER support Digest authentication and bounded binding lifecycle. A basic B2BUA creates independent dialog legs for internal calls and handles answer, rejection, caller/callee BYE, and pre-answer CANCEL. Every call event carries tenant, node, call, leg, sequence, and both leg IDs; an asynchronous bounded bus persists ordered CDR/event data to MySQL 8 outside the call actor.

The Phase 0 audit findings are closed: CI now runs on both `main` and `master` and includes real MySQL integration, SIP parser fuzz smoke, and SIPp protocol scenarios.

## Implemented components

- Typed registration domain, refresh/unregister/expiry policy, binding limit, q preference, memory and MySQL stores.
- Digest MD5 `qop=auth` using HA1, HMAC-protected expiring nonces, nonce-count replay rejection, and bounded replay state.
- UDP/TCP sipgo service assembly with explicit socket ownership, timeouts, cleanup, and maximum active-call/mailbox bounds.
- Call/leg identifiers, actor-owned state transitions, immutable snapshots, structured correlated events, and bounded asynchronous persistence.
- Basic independent-leg B2BUA routing active registered contacts, relaying provisional/final responses and propagating CANCEL/BYE.
- MySQL migration for credentials, registrations, calls, and ordered call events with tenant/time lookup indexes.
- SIPp scenarios and a test-only in-memory server; no real credentials, domains, or public addresses.

## Commands executed and evidence

| Command | Result |
|---|---|
| Initial `git status --short`, file inventory, full plan/standards/ADR/report reads | Completed before edits; existing user files preserved |
| `make fmt-check` | Pass |
| `make vet` | Pass |
| `GOLANGCI_LINT=/tmp/aixvolinkpbx-tools/golangci-lint make lint` (v2.12.2) | Pass, `0 issues` |
| `make test` | Pass for all Core, registrar, auth, event, spikes, and integration packages |
| `make test-race` | Pass for all packages, including network integration tests |
| `AIXVOLINKPBX_TEST_MYSQL_DSN=... make integration-test` against Docker MySQL 8.4 | Pass; migration, credential, registration, CDR, and event assertions passed |
| `make sipp-test` with SIPp 3.7.7 | Pass; Digest registration, answer/BYE, 486/ACK, and CANCEL/487/ACK |
| `make fuzz-smoke` | Pass; 5 seconds, 2,283,352 executions, no panic |
| `GOVULNCHECK=... make vulncheck` (v1.5.0) | Pass, `No vulnerabilities found` |
| `GO_LICENSES=... make license-check` (v1.6.0) | Pass; allowed Apache-2.0, MIT, BSD-2-Clause, BSD-3-Clause, MPL-2.0 |
| `make build` | Pass, pure-Go linux/amd64 and linux/arm64 binaries |
| `make bench` | Pass; baseline below |
| `git diff --check` | Pass |

The SIPp test exposed a scenario defect during validation: CANCEL initially used a new Via branch and correctly received 405 because it did not match the INVITE transaction. The scenario now reuses the INVITE branch and verifies 200 to CANCEL plus 487 to the INVITE. This is retained as executable protocol evidence.

## Benchmark and pressure baseline

Measured on Apple M4 Pro with Go 1.26.4:

```text
BenchmarkManagerRefresh-14  3494114  342.2 ns/op  64 B/op  2 allocs/op
BenchmarkDiagoRTPParse-14  257982590  4.556 ns/op  0 B/op  0 allocs/op
```

An additional SIPp registration run sent 100 complete unauthenticated/authenticated REGISTER exchanges at a configured 50 calls/s. It completed in 2.003 seconds at 49.925 calls/s with 100 successes, zero retransmissions, zero timeouts, and zero unexpected messages. These are local development baselines, not production capacity claims; they exclude network latency, durable MySQL registration writes, HA, observability exporters, and media.

## Acceptance matrix

| Phase 1 gate | Evidence | Status |
|---|---|---|
| Register, refresh, unregister, and expiry are correct | Domain tests, UDP/TCP integration, real MySQL test | Pass |
| Call, rejection, CANCEL, and BYE pass SIPp | `test/sipp` via `make sipp-test` | Pass |
| Every event correlates by `call_id` | Actor/integration assertions and MySQL ordered event query | Pass |
| Unit, race, integration, and protocol tests pass | Commands above | Pass |
| No Phase 2 implementation introduced | No RTP anchoring, SDP/media negotiation, DTMF, or early media | Pass |

## Remaining risks

1. ADR 0006 permits Phase 1 INVITEs only when their source tuple matches an active registration. This is not sufficient for an untrusted Internet edge, shared NAT, proxy topology, or source migration; authenticated INVITE or trusted-edge identity is required before such deployment.
2. SIPp call control currently covers UDP. REGISTER is tested over both UDP and TCP, but TCP dialog and real endpoint interoperability remain additional hardening work.
3. The B2BUA covers initial dialogs only. Re-INVITE, UPDATE, PRACK/100rel, session timers, forking, Record-Route proxies, and overlapping transactions are not implemented.
4. The event bus is bounded and applies backpressure, but process termination does not yet provide a durable outbox/flush acknowledgement. Abrupt termination can lose queued events.
5. MySQL schema and migration were validated on 8.4 in a clean database. Upgrade/rollback migrations, partitioning, retention, failover, and sustained contention are not yet characterized.
6. Digest MD5 is retained for SIP endpoint compatibility. TLS transport, credential provisioning/rotation, account lockout, and edge rate limiting remain deployment security requirements.
7. The capacity and topology inputs in plan section 22 are still unconfirmed; the local pressure result cannot set production sizing.

## Phase 2 admission decision

**Conditionally ready for Phase 2 development.** All Phase 1 functional acceptance gates pass. Before any production exposure or capacity commitment, the owner must define plan section 22 capacity, client, network, and security inputs. Phase 2 work may begin in repository scope using conservative bounded defaults, while the risks above remain explicit gates for production readiness.

## Recommended next prompt

```text
Continue AixvoLinkPBX from the completed Phase 1 repository. Fully read the
plan, Go standards, all ADRs, and both phase validation reports. First audit
the Phase 1 gates and repair regressions. Execute Phase 2 only: implement a
bounded RTP port pool and anchored two-leg PCMU/PCMA media, RTCP and quality
metrics, RFC 4733 DTMF, and early media according to the plan. Do not add
WebRTC production signaling, carrier adapters, UI, recording, HA, or
commercial features. Preserve call-actor ownership and keep packet hot paths
free of synchronous database/HTTP work and per-packet logging. Add unit,
race, RTP/RTCP integration, packet-loss/timeout, port-exhaustion, and SIPp
media tests; run benchmarks, all CI gates, and write the Phase 2 validation
report. Create an ADR before any architectural deviation. Do not commit.
```
