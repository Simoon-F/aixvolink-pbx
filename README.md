# AixvoLinkPBX

AixvoLinkPBX is an open-core, programmable SIP and WebRTC PBX written in Go.
The repository is currently at **Phase 0: project initialization and technical validation**. It does not yet provide a PBX service.

## Requirements

- Go 1.25 or newer
- GNU Make
- Optional CI tools installed by `make tools`

## Phase 0 quick start

```sh
make test
make test-race
make build
```

The isolated protocol experiments are documented in:

- [`spikes/sipgo`](spikes/sipgo/README.md)
- [`spikes/diago`](spikes/diago/README.md)
- [`spikes/pion`](spikes/pion/README.md)

Architecture decisions are in [`docs/adr`](docs/adr), and the executed Phase 0 evidence is recorded in [`docs/phase-0-validation.md`](docs/phase-0-validation.md).

## Scope boundary

Phase 0 contains dependency and interoperability experiments only. Registrar, B2BUA, production media engine, administration UI, carrier adapters, storage, and commercial services are deliberately absent.

## Security

Example listeners bind to loopback by default. Example configuration contains no credentials, public domains, phone numbers, or external server addresses. Do not expose a spike directly to an untrusted network.

## Development

Read [`AixvoLinkPBX-PLAN.md`](AixvoLinkPBX-PLAN.md), [`docs/development/go-standards.md`](docs/development/go-standards.md), and [`CONTRIBUTING.md`](CONTRIBUTING.md) before changing code.
