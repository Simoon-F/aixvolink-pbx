# AixvoLinkPBX

AixvoLinkPBX is an open-core, programmable SIP and WebRTC PBX written in Go.
The repository has completed **Phase 2: anchored G.711 media and basic PBX behavior**. It provides a bounded single-node UDP/TCP registrar, SIP B2BUA, anchored PCMU/PCMA RTP/RTCP, RFC 4733 DTMF, early media, hold/resume, and blind transfer.

## Requirements

- Go 1.25 or newer
- GNU Make
- Optional CI tools installed by `make tools`

## Phase 2 verification

```sh
make test
make test-race
make fuzz-smoke
make sipp-test
make media-bench
make build
```

MySQL integration tests additionally require `AIXVOLINKPBX_TEST_MYSQL_DSN`; see `make integration-test`. Apply the SQL files under `migrations` in numeric order, set `AIXVOLINKPBX_MYSQL_DSN` and a 32-byte-or-longer `AIXVOLINKPBX_NONCE_SECRET`, then run `go run ./cmd/aixvolinkpbx` for the service.

The isolated protocol experiments are documented in:

- [`spikes/sipgo`](spikes/sipgo/README.md)
- [`spikes/diago`](spikes/diago/README.md)
- [`spikes/pion`](spikes/pion/README.md)

Architecture decisions are in [`docs/adr`](docs/adr). Executed evidence is recorded in the Phase 0, Phase 1, and [`Phase 2 validation report`](docs/phase-2-validation.md).

## Scope boundary

Phase 2 contains registration, internal call control, anchored plain RTP/AVP media, quality samples, DTMF, early media, hold, and blind transfer. WebRTC/SRTP, administration UI, carrier adapters, recording, high availability, and commercial services remain deliberately absent.

## Security

Example listeners bind to loopback by default. Example configuration contains no credentials, public domains, phone numbers, or external server addresses. Do not expose a spike directly to an untrusted network.

## Development

Read [`AixvoLinkPBX-PLAN.md`](AixvoLinkPBX-PLAN.md), [`docs/development/go-standards.md`](docs/development/go-standards.md), and [`CONTRIBUTING.md`](CONTRIBUTING.md) before changing code.
