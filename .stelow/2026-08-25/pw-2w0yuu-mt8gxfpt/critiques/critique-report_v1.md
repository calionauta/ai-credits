# Critique Report — ai-credits spec-product v1

> Pre-flight check before product gate. Reviewed: PLAN.md, spec-product_v1.

## Findings

1. **Clear**: executable spec with definitive Go signatures, schema SQL, formulas, and
   per-stage acceptance criteria. A fresh implementer can follow it end to end.
2. **Unambiguous decisions made**: refund policy (§6.4), reservation timeout (§6.5),
   lazy monthly grant (§6.6), single pool (§6.7), BYOK relay in-process (§7.2),
   Stripe-in-app not in-lib (§1, §10 Stage 8).
3. **Edge cases covered**: concurrency test spec (100 goroutines → 20/80), duplicate
   idempotency inside tx, negative-balance guard on Reserve, orphan reservation expiry,
   reconcile mismatch reporting (no autofix).
4. **Risks flagged and bounded**:
   - SQLite-specific SQL in a driver-agnostic lib → documented constraint (apps pass
     SQLite); acceptable for the two known consumers.
   - BYOK relay trusts `X-Auth-User` → app must strip external headers; documented as app
     responsibility (§7.3, README).
   - goai version mismatch (0.7.6 vs 0.9.6) → lib owns its `Usage` type; per-app mapper.
5. **No rabbit holes left unmanaged**: all five flagged in shape have explicit resolutions.

## Verdict

**Approve.** Spec is complete, internally consistent, and matches the stated appetite
(Core). No rework needed before gate.

## Gate checklist (product gate)

- [x] spec-product artifact present (`.stelow/2026-08-25/pw-2w0yuu-mt8gxfpt/specs/spec-product_v1.md`)
- [x] Appetite stated (Core)
- [x] Hill chart with rabbit holes
- [x] IN/OUT for v1 explicit
- [x] DoD measurable (PLAN.md §16)
