# Phase 0 validation report

- Date: 2026-06-28 (Asia/Shanghai)
- Repository: `github.com/Simoon-F/aixvolink-pbx`
- Scope: project initialization and isolated protocol/media technical validation only
- Host: Apple Mac16,11, Apple M4 Pro, 14 logical CPUs, 48 GiB RAM, darwin/arm64
- Primary toolchain: Go 1.26.4; minimum-toolchain regression: Go 1.25.0

## Outcome

Phase 0 code, documentation, dependency locks, CI, and three isolated spikes are implemented. The SIP spike responds to real UDP and TCP transactions, the Diago spike echoes PCMU and PCMA RTP over real UDP sockets, and the Pion spike completes a browser audio PeerConnection plus automated Opus RTP echo through ICE and DTLS-SRTP.

The Core repository license is Apache-2.0. Commercial hosted services and enterprise components remain separate and communicate with Core through stable HTTP, gRPC, or WebSocket protocols. Phase 0 is complete and the repository satisfies the technical and documentation gates for Phase 1.

## Commands executed and results

| Command or action | Result |
|---|---|
| `git status --short` and `rg --files` before edits | Pass; only the two required baseline documents existed, both untracked |
| Full segmented reads of `AixvoLinkPBX-PLAN.md` and `AixvoLinkPBX-GO-STANDARDS.md` | Completed before implementation |
| Official upstream tag/module inspection for sipgo, Diago, and Pion | Locked sipgo v1.4.0, Diago v0.29.0, Pion WebRTC v4.2.15, Pion RTP v1.10.2 |
| `gofmt -w .` and `make fmt-check` equivalent check | Pass; no unformatted Go files |
| `go vet ./...` | Pass |
| `golangci-lint run ./...` with v2.12.2 | Pass, `0 issues` |
| `go test -count=1 ./...` | Pass on all packages and three spikes |
| `go test -race -count=1 ./...` | Pass on all packages and three spikes |
| `GOTOOLCHAIN=go1.25.0 go test -count=1 ./...` | Pass; confirms declared minimum toolchain |
| `govulncheck ./...` with v1.5.0 | Pass, `No vulnerabilities found` |
| `go-licenses check ./spikes/...` with v1.6.0 | Pass; build graph limited to MIT, BSD-2-Clause, BSD-3-Clause, and MPL-2.0 |
| `make build` | Pass for linux/amd64 and linux/arm64 with `CGO_ENABLED=0` |
| `go test -run='^$' -bench=. -benchmem ./spikes/diago/...` | Pass; results below |
| Chromium at `http://127.0.0.1:18080`, **Connect test tone** | Pass; page reached `connected`, no browser console warning/error |
| Chromium **Connect microphone** | Page loaded; embedded browser denied device permission before signaling, so microphone-device validation remains manual |
| `git diff --check` | Pass |

The license audit warns that assembly files cannot be recursively inspected as Go source; their owning modules' root license files were still classified. Direct upstream files were independently checked: sipgo BSD-2-Clause, Diago MPL-2.0, and Pion MIT.

## Spike evidence

### sipgo

- Bound UDP and TCP on one loopback port.
- Parsed RFC 3261 `OPTIONS` requests with sipgo and returned transaction-aware `200 OK` responses.
- Confirmed RFC 3581 `rport` behavior during the test: without `rport`, a UDP response followed the Via-advertised port rather than the client's ephemeral source port.
- Context cancellation closes both sockets and the sipgo user agent; all test I/O has a three-second deadline.

### Diago

- Allocated an RTP/RTCP UDP pair through `media.NewMediaSession`.
- Parsed and echoed PCMU payload type 0 and PCMA payload type 8 byte-for-byte.
- Learned only the validated packet's UDP source for the echo destination.
- Enforced a read poll timeout, overall idle timeout, payload allowlist, cancellation, and resource cleanup.

### Pion

- Served a browser page with microphone and permission-free Web Audio test-tone sources.
- Real Chromium completed host-candidate offer/answer and reached `PeerConnectionStateConnected` with no console warning/error.
- Automated Pion-to-Pion test completed HTTP signaling, ICE, DTLS-SRTP, sent Opus RTP, and verified echoed payload.
- Peer count, SDP body, HTTP timings, and session lifetime are bounded; service shutdown closes and joins owned peers/readers.

## Benchmark and basic resource data

Measured on Apple M4 Pro with Go 1.26.4:

```text
BenchmarkDiagoRTPParse-14  249675658  4.726 ns/op  0 B/op  0 allocs/op
```

This microbenchmark measures Diago's RTP unmarshal path for one fixed 160-byte PCMU payload. It is not a call-capacity claim and excludes socket I/O, scheduling, RTCP, bridging, encryption, and observability.

Cross-built Phase 0 command sizes:

```text
linux/amd64  2,520,825 bytes
linux/arm64  2,433,211 bytes
```

The main command intentionally exposes version/build validation only. It is not a PBX service in Phase 0.

## Confirmed technical capabilities

- sipgo provides the required parser, transaction, and UDP/TCP listener foundations; upstream also exposes TLS/WS/WSS APIs.
- Diago provides usable G.711 media session and RTP/RTCP primitives. It will be reused selectively behind AixvoLinkPBX-owned media boundaries rather than owning call architecture.
- Pion can terminate browser-compatible audio with ICE and DTLS-SRTP and exposes RTP for a future SIP-media bridge.
- All spike lifecycle paths are bounded and pass the race detector.
- Pure-Go linux/amd64 and linux/arm64 builds work.
- Security-fixed transitive floors eliminate the module-level findings initially reported against older `x/crypto`, `x/net`, and `x/sys`; this requires Go 1.25 or newer.

## Open risks and unverified cases

1. SIP TLS, WS, and WSS are upstream-supported but were not exercised by the requested UDP/TCP spike. Certificate policy and JsSIP signaling remain later interoperability gates.
2. Browser testing currently covers Chromium host candidates and a synthetic Web Audio source. Real microphone hardware, Chrome/Edge versions, ICE restart, public STUN, TURN UDP/TCP/TLS, relay candidates, and adverse NAT are unverified.
3. The Diago test is a single-endpoint echo, not sustained two-leg load, RTCP quality validation, packet-loss testing, or a production port allocator.
4. MPL-2.0 obligations are documented and no Diago files were copied or modified, but a formal legal review is still required before production distribution.
5. The business parameters listed in plan section 22 remain unconfirmed: peak calls/CPS, clients, codecs, carrier security modes, TURN topology, recording retention, first region/numbers, target hosts, frontend stack, and tenant isolation.
6. Phase 1 CI additions for meaningful SIPp, fuzz, and MySQL integration scenarios do not exist yet because Phase 0 deliberately contains no registrar, persistence, or B2BUA behavior to test.

## Phase 1 admission checklist

| Gate | Status |
|---|---|
| Professional repository/module structure | Pass |
| Required project and contribution documents | Pass |
| Four required architecture decisions | Pass |
| Three isolated runnable spikes with tests and cleanup | Pass |
| CI format/vet/lint/test/race/vulnerability/build gates | Pass |
| Locked dependencies and automated license classification | Pass |
| No Phase 1 PBX/business implementation introduced | Pass |
| Owner-approved Core license | Pass: Apache-2.0 |

**Admission decision: ready.** Phase 1 may begin after the outstanding business parameters that affect its implementation are confirmed and recorded.

## Recommended next-phase prompt

```text
Continue AixvoLinkPBX from the completed Phase 0 repository. First read
AixvoLinkPBX-PLAN.md, docs/development/go-standards.md, all accepted ADRs,
and docs/phase-0-validation.md. Execute Phase 1 only: implement UDP/TCP SIP
REGISTER with Digest authentication, binding refresh/unregister/expiry, and
the minimum internal two-leg B2BUA call flow with call_id-correlated events.
Preserve the per-call actor ownership and dependency boundaries from ADR-0001
through ADR-0004. Before coding, confirm and record the plan section 22
capacity/client/security parameters that affect Phase 1. Add table-driven,
race, SIPp, parser fuzz-smoke, and lifecycle cleanup tests; do not add Web UI,
carrier adapters, WebRTC production integration, transcoding, or commercial
features. Run every CI gate and write a Phase 1 validation report. Do not
commit unless explicitly requested.
```
