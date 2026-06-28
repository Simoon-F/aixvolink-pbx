# AixvoLinkPBX

AixvoLinkPBX is an open-core, programmable SIP and WebRTC PBX written in Go.
The repository has completed **Phase 1: Registrar and internal SIP calling**. It provides a bounded single-node UDP/TCP registrar and basic SIP B2BUA; RTP/media starts in Phase 2.

## Requirements

- Go 1.25 or newer
- GNU Make
- Optional CI tools installed by `make tools`

## Phase 1 verification

```sh
make test
make test-race
make fuzz-smoke
make sipp-test
make build
```

MySQL integration tests additionally require `AIXVOLINKPBX_TEST_MYSQL_DSN`; see `make integration-test`. Apply `migrations/000001_phase1.sql`, set `AIXVOLINKPBX_MYSQL_DSN` and a 32-byte-or-longer `AIXVOLINKPBX_NONCE_SECRET`, then run `go run ./cmd/aixvolinkpbx` for the service.

The isolated protocol experiments are documented in:

- [`spikes/sipgo`](spikes/sipgo/README.md)
- [`spikes/diago`](spikes/diago/README.md)
- [`spikes/pion`](spikes/pion/README.md)

Architecture decisions are in [`docs/adr`](docs/adr). Executed evidence is recorded in [`docs/phase-0-validation.md`](docs/phase-0-validation.md) and [`docs/phase-1-validation.md`](docs/phase-1-validation.md).

## Scope boundary

Phase 1 contains registration, internal SIP call control, CDRs, and correlated events. Production media, administration UI, carrier adapters, high availability, and commercial services remain deliberately absent.

## Security

Example listeners bind to loopback by default. Example configuration contains no credentials, public domains, phone numbers, or external server addresses. Do not expose a spike directly to an untrusted network.

## Development

Read [`AixvoLinkPBX-PLAN.md`](AixvoLinkPBX-PLAN.md), [`docs/development/go-standards.md`](docs/development/go-standards.md), and [`CONTRIBUTING.md`](CONTRIBUTING.md) before changing code.
