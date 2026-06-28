# Phase 1 regression audit before Phase 2

- Audit date: 2026-06-28 (Asia/Shanghai)
- Baseline commit: `dab108d chore: update fuzz-smoke test duration and documentation`

## Scope and evidence

The audit inspected the working tree before edits, re-read the plan, Go standards, all accepted ADRs, and the Phase 0/1 reports. The only pre-existing untracked files were the owner-supplied root plan/standards and their documentation mirror; they were preserved.

Before Phase 2 changes, the following passed: formatting, `go vet`, golangci-lint, all unit tests, all race tests, deterministic REGISTER fuzz smoke, existing SIPp registration/call/reject/CANCEL/BYE scenarios, govulncheck, and linux/amd64 plus linux/arm64 builds. The real MySQL gate was repeated during Phase 2 against MySQL 8.4.

## Findings

1. No Phase 1 functional regression was reproduced.
2. The fixed-count fuzz change in `dab108d` removed the previous CI deadline flake and passed again before Phase 2 work.
3. Existing sipgo integration tests still emit occasional upstream transport-reference and non-2xx ACK cleanup warnings while passing protocol assertions. They remain an interoperability/upstream-lifecycle risk, not a hidden test failure.
4. Phase 1 deliberately had no SDP/media, re-INVITE, REFER, or NOTIFY behavior. Those are Phase 2 work, not Phase 1 regressions.

## Disposition

Phase 1 met its admission gate. Phase 2 implementation proceeded without changing the Phase 1 authentication, registration, storage, or call actor ownership boundaries.
