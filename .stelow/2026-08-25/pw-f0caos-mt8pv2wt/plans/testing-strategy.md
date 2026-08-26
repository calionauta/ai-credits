---
wf: pw-f0caos-mt8pv2wt
product_type: software
appetite: Core
review_mode: Auto
---

# Testing Strategy — ai-credits lib

Approach per PLAN §13: **TDD for critical paths** (ledger, reservations,
pricing) + risk-based tests for crypto/relay. No real-LLM tests (gogogo rule);
all LLM calls go through httptest fakes/stubs.

## Risk-based test map

| Area | TDD? | Coverage |
|------|------|----------|
| Ledger (Grant/Refund/Balance) | ✅ Yes | idempotency, negative refund, `balance == SUM(ledger)` invariant |
| Pricing (Cost/EstimateMax/Credits) | ✅ Yes | exact cost tables, ceil, unknown model, `EstimateMax ≥ Cost` |
| Reservations | ✅ Yes | canonical 500/137/363; **100-goroutine `-race`** → exactly 20/80; duplicate requestID; closed-state errors |
| Monthly + Reconcile | ⚠️ after | same/diff periods; orphan expiry; mismatch report (never auto-fix) |
| BYOK crypto | ❌ after | Put/Get round-trip; wrong key → decrypt error |
| BYOK relay | ❌ after | httptest fake OpenAI-compat incl. SSE; 404 without credential; key injection |
| App integrations | ❌ after | gogogo gateway with `internal/llm/fakeserver`; treinamento chat with LLM stub |

## Tooling & gates

- Test runner: `go test -race ./...` (CI + local, stage gate).
- Lint: `golangci-lint run ./...` (errcheck, govet, staticcheck, gosec,
  revive, gocritic, sloglint, gocognit, testifylint, contextcheck, errorlint,
  modernize).
- Security: `govulncheck` (CI).
- Time: `cfg.Now` clock injection in every time-dependent test.
- Concurrency: `-race` mandatory (reservation scope).
- No test hits a real LLM API.

## Scope embedding

Test-unit tasks are embedded per-scope (runs last within each scope as the
validate step), not standalone test-* scopes — matches the zero-dep lib
layout where each `_test.go` sits next to its source.

## DoD hook

- Stage-level acceptance criteria in PLAN §10 drive each scope's tests.
- Project AC (spec-product_v1 §AC): #1 schema idempotent, #2 grant replay,
  #3 settle 500/137/363, #4 100-goroutine race, #5 unknown model, #6 BYOK 404,
  #7 Stripe replay no double.