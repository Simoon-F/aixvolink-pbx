# ADR-0005: Phase 1 MySQL data access

- Status: Accepted
- Date: 2026-06-28
- Deciders: AixvoLinkPBX maintainers

## Context

Phase 1 needs durable SIP credentials, registration bindings, CDR summaries, and ordered call events. The Go standards require one deliberate data-access style and prohibit mixing ORMs and sqlc without an ADR. The schema is small, transaction boundaries are protocol-sensitive, and generated query infrastructure would add more surface than the current queries justify.

## Decision

Use the standard `database/sql` API with `github.com/go-sql-driver/mysql v1.10.0`. Handwritten SQL is confined to `internal/platform/mysql`; core packages depend only on narrow store interfaces. Migrations remain forward-only files under `migrations/` and are not run automatically by the PBX process.

Registration upsert and per-AoR binding-limit enforcement share one MySQL transaction and row/gap lock. Call actors publish to a bounded in-memory bus; one asynchronous consumer writes CDR and event rows so SIP/call-owner goroutines do not execute database I/O.

All calls use caller-provided contexts. Timestamps are UTC `DATETIME(6)`. Tables include tenant/time indexes from their first migration. Phase 1 uses one MySQL node; Redis and multi-node registration coordination remain out of scope.

## Consequences

- Transaction and lock behavior are visible and directly testable against MySQL 8.
- There is no runtime reflection or ORM model coupling in core packages.
- Handwritten scan lists require careful review when schemas change.
- If query volume or schema complexity grows materially, adopting sqlc requires a superseding ADR and cannot coexist casually with a second access style.

## Alternatives considered

- ORM: rejected because hidden query/transaction behavior is undesirable on registrar and call paths.
- sqlc now: deferred because Phase 1 has a small query set and generated-code infrastructure would not yet pay for itself.
- Memory-only persistence: rejected because Phase 1 explicitly requires registration storage and CDR evidence.
