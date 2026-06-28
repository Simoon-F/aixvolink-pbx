# Contributing to AixvoLinkPBX

## Before coding

1. Read `AixvoLinkPBX-PLAN.md` and `docs/development/go-standards.md`.
2. Keep changes within the active implementation phase.
3. Add or update an ADR before changing an accepted architectural decision.

## Local checks

Run the same core checks as CI:

```sh
make fmt-check
make vet
make lint
make test
make test-race
make vulncheck
make build
```

Protocol or media changes require an automated test. Media hot-path changes also require `make bench`. Never commit credentials, complete Authorization headers, TURN credentials, SRTP keys, real phone numbers, or packet-by-packet logs.

Commits should be focused and use an imperative subject. Pull requests must describe protocol impact, verification, performance impact, and rollback behavior.
