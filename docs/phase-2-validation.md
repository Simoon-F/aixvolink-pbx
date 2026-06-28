# Phase 2 validation report

- Date: 2026-06-28 (Asia/Shanghai)
- Repository: `github.com/Simoon-F/aixvolink-pbx`
- Scope: bounded RTP/RTCP anchoring, G.711, quality metrics, RFC 4733 DTMF, early media, and Phase 2 in-dialog control only
- Host: Apple M4 Pro, 14 logical CPUs, darwin/arm64
- Toolchain used: Go 1.26.4; compatibility rerun with the module minimum Go 1.25.0

## Outcome

Phase 2 is implemented within ADR-0007. Each admitted call leases two even-RTP/odd-RTCP socket pairs from a bounded pool, negotiates independent PCMU/PCMA and telephone-event payload mappings, and anchors both media directions with per-leg RTP header isolation. RTCP SR/RR, loss, reorder, duplicate, jitter, RTT, timeout, one-way-media, and drop counters are exposed as immutable call-correlated summaries and persisted asynchronously outside packet loops.

Valid 183 SDP starts early media. Caller re-INVITE hold/resume is serialized through the Call actor, and blind REFER/NOTIFY is relayed across independent dialog legs. No WebRTC production signaling, carrier adapter, UI, recording, HA, commercial service, transcoder, ICE, DTLS, or SRTP was added.

The pre-Phase 2 audit found no failing Phase 1 acceptance gate. During final Phase 2 regression testing, a real overlapping re-INVITE defect was found: an INVITE destined for 491 advanced the dialog CSeq before the previous ACK was processed, which could leave the dialog permanently returning 491. Re-INVITE admission is now reserved before dialog mutation, ACK cleanup is matched by CSeq/transaction, and the protocol test performs the bounded 491 retry required of a UAC. The scenario passed 50 consecutive normal runs and 10 consecutive race-enabled runs.

## Implemented components

- `internal/media/portalloc`: bounded, context-aware RTP/RTCP pair leases with bind-before-admit, exhaustion, exact-once release, and reuse.
- `internal/sip/sdp`: Pion-based RTP/AVP audio offer/answer for PCMU, PCMA, and telephone-event with explicit directions and RTCP addresses.
- `internal/media/rtp`: fixed-memory sequence/loss/reorder/duplicate/jitter tracking and zero-allocation PT/SSRC/sequence/timestamp rewriting.
- `internal/media/session`: two-leg RTP forwarding, symmetric-source pinning, RTCP readers/writers, quality snapshots, inactivity/one-way detection, and bounded publisher timeouts.
- `internal/media/dtmf`: RFC 4733 event validation, decode/encode, counters, and negotiated payload forwarding.
- B2BUA integration: 183/200 media negotiation, 488/503 failure mapping, media lifecycle, re-INVITE hold/resume, and blind REFER/NOTIFY.
- Bounded media event bus and MySQL migration `000002_phase2_media.sql` for asynchronous quality samples.
- SIPp PCMU RTP stream through both anchored legs with a callee echo and server-side nonzero packet evidence.

## Commands executed and evidence

All final commands ran under `set -euo pipefail`; a failure stopped the chain.

| Command | Result |
|---|---|
| Initial `git status --short`, repository inventory, and full reads of plan, standards, all ADRs, and Phase 0/1 reports | Completed before edits; pre-existing untracked plan/standards/development files preserved |
| `make fmt-check` | Pass |
| `make vet` | Pass |
| `GOLANGCI_LINT=/tmp/aixvolinkpbx-tools/golangci-lint make lint` (v2.12.2) | Pass, `0 issues` |
| `make test` | Pass for all packages, including real UDP RTP/RTCP integration and loss/timeout/exhaustion paths |
| `make test-race` | Pass for all packages and network integrations |
| `go test ./test/integration -run TestInternalCallRelaysHoldResumeAndBlindTransfer -count=50` | Pass after CSeq/ACK regression fix |
| `go test -race ./test/integration -run TestInternalCallRelaysHoldResumeAndBlindTransfer -count=10` | Pass |
| `make fuzz-smoke` | Pass; REGISTER parser executed 100,022 cases and SDP parser 100,000 cases within the two-minute limits |
| `AIXVOLINKPBX_TEST_MYSQL_DSN=... make integration-test` against Docker MySQL 8.4 | Pass; both migrations plus registration, CDR/event, and media-quality persistence assertions |
| `make sipp-test` with SIPp 3.7.7 | Pass; answer/BYE, reject, CANCEL, early media, and nonzero PCMU RTP in both anchored directions |
| `make media-bench` | Pass; 100 concurrent anchored sessions and RTP hot-path benchmarks below |
| `GOVULNCHECK=/tmp/aixvolinkpbx-tools/govulncheck make vulncheck` (v1.5.0) | Pass, `No vulnerabilities found` |
| `GO_LICENSES=/tmp/aixvolinkpbx-tools/go-licenses make license-check` (v1.6.0) | Pass; Apache-2.0, MIT, BSD-2-Clause, BSD-3-Clause, and MPL-2.0 allowlist |
| `make build` | Pass; pure-Go linux/amd64 and linux/arm64 binaries |
| `GOTOOLCHAIN=go1.25.0 go test -count=1 ./...` | Pass with the declared minimum toolchain |
| `git diff --check` | Pass |

## Performance and resource baseline

Measured locally on Apple M4 Pro with Go 1.26.4:

```text
100 concurrent calls, 10,000 anchored RTP packets: 226.945125 ms, 44,064 packets/s
BenchmarkRewrite-14         10.29 ns/op   0 B/op   0 allocs/op
BenchmarkTrackerObserve-14   7.227 ns/op  0 B/op   0 allocs/op
```

The 100-call test opens 200 PBX RTP/RTCP pairs, therefore 400 bounded PBX UDP sockets, and sends 50 packets in each direction for every call. CPU and allocation profiles were captured to `/tmp/aixvolinkpbx-phase2-{cpu,mem}.pprof`. CPU samples were led by `Rewriter.Rewrite` and `Tracker.Observe`; benchmark loops allocate zero bytes per packet. Profile allocations were setup/profiler and `NewRewriter`, not packet-loop allocations.

This is a deterministic local development gate, not a production capacity claim. It does not simulate sustained real-time packet pacing, WAN jitter, 10 calls/s signaling, durable-database contention, multiple nodes, or a fixed 4-vCPU deployment.

## Acceptance matrix

| Phase 2 gate | Evidence | Status |
|---|---|---|
| Bidirectional PCMU/PCMA anchored audio | UDP session and B2BUA integration tests; SIPp PCMU stream/echo evidence | Pass |
| Bounded ports and deterministic exhaustion | Pool unit tests and concurrent session exhaustion test | Pass |
| RTCP and call-correlated quality metrics | SR/RR generation/ingestion, RTT/loss/jitter tests, MySQL quality row | Pass |
| Loss, one-way media, inactivity, and cleanup | Session integration tests under normal and race modes | Pass |
| RFC 4733 DTMF | codec tests plus mapped payload traversal assertion | Pass |
| Early media | 183 SDP RTP test before final answer and SIPp ladder | Pass |
| Hold/resume and blind transfer interoperability | re-INVITE/ACK and REFER/NOTIFY UDP integration test | Pass |
| 100 concurrent calls | 100 sessions and 10,000 RTP packets with exact cleanup | Pass as development baseline |
| Full CI, vulnerability, license, and dual-architecture build | Commands above | Pass |
| Phase 3+ features excluded | No production WebRTC, carrier, UI, recording, HA, or commercial implementation | Pass |

## Dependency and license verification

The dependency graph is locked by `go.mod` and `go.sum`. Phase 2 directly uses `pion/rtp v1.10.2`, `pion/rtcp v1.2.16`, and `pion/sdp/v3 v3.0.18`, all MIT. Existing `sipgo v1.4.0` is BSD-2-Clause; Diago `v0.29.0` is MPL-2.0 and remains unmodified reference/spike code; Pion WebRTC `v4.2.15` remains confined to its Phase 0 spike. Required notice and source-availability handling is recorded in `THIRD_PARTY_NOTICES.md`. Automated license checking passed, but release packaging still requires the authoritative upstream license texts and formal legal review.

## Remaining risks

1. Media is plain RTP/AVP. Internet-facing deployment still requires the later ICE/DTLS-SRTP/WebRTC edge or an equivalent trusted secure boundary.
2. Symmetric RTP pins the first source port from the SDP-advertised IP. Authentication, ACLs, topology hiding, and anti-abuse controls remain deployment requirements.
3. Each active call consumes four PBX UDP ports and four sockets. Production ranges, file-descriptor limits, NAT/firewall policy, and multi-node allocation must be sized from the still-unconfirmed plan inputs.
4. The 128-packet duplicate/reorder window and SSRC restart handling are intentionally bounded and simplified. Extreme reordering, long pauses, and endpoint restart interoperability need field traces.
5. RTCP covers basic SR/RR and RTT. RTCP mux, extended reports, SRTP/SRTCP, jitter buffering, and transcoding are outside Phase 2.
6. Hold/resume currently uses re-INVITE; UPDATE, PRACK/100rel, session timers, attended transfer/Replaces, and complex proxy Record-Route cases are not implemented.
7. The media summary bus is bounded but has no durable shutdown flush/outbox acknowledgement; abrupt process loss can drop queued summaries.
8. sipgo v1.4.0 intermittently emits UDP reference/late-ACK warnings during rapid local teardown although assertions and race tests pass. This remains an upstream lifecycle/interoperability watch item.
9. SIPp proves one PCMU stream and echo plus signaling ladders. PCMA, RTCP, loss, timeout, and RFC 4733 packet assertions are covered by Go UDP integrations rather than SIPp.
10. The local 100-call burst does not establish the plan's production capacity or quality target; a paced soak on the target CPU/network is still required.

## Phase 3 admission decision

**Ready for Phase 3 development, not production deployment.** All Phase 2 functional and repository gates pass, including bounded resource exhaustion, race testing, real RTP/RTCP traffic, SIPp media evidence, the 100-session development baseline, vulnerability scan, license audit, and both Linux builds. Production sizing and secure-edge decisions remain explicit prerequisites before any capacity or exposure commitment.

## Recommended next prompt

```text
Continue AixvoLinkPBX from the completed Phase 2 repository. Fully read the
plan, Go standards, every ADR, and the Phase 0/1/2 validation reports. First
audit all Phase 2 gates and repair regressions. Execute Phase 3 only: implement
the plan-defined Twilio and Bandwidth carrier adapters behind the existing
Core boundaries, including normalized signaling/events, credentials and
webhook validation, bounded retries/timeouts, failure mapping, and carrier
contract tests. Do not add production WebRTC signaling, UI, recording, HA, or
commercial services. Create an ADR before any architectural deviation. Run
unit, race, integration, protocol/contract, security, full CI, and relevant
load tests; update dependency/license notices and write the Phase 3 validation
report with evidence, risks, and the Phase 4 admission decision. Do not commit.
```
